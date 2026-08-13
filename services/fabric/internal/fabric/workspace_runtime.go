package fabric

import (
	"context"
	"crypto/sha256"
	"fmt"
	"maps"
	"strings"
	"time"
)

const runtimeClaimStaleAfter = 2 * time.Minute

func (s *Service) claimRuntimeOperation(ctx context.Context, operation FabricOperation) (FabricOperation, bool, error) {
	stored, claimed, err := s.operations.ClaimRuntime(ctx, operation)
	// A stale runtime operation is never reclaimed into a new provider lease.
	// The caller must first prove the already-attempted resource by readback and
	// use the dedicated CAS path below.  This is what keeps a lost response from
	// becoming a second apply/patch.
	return stored, claimed, err
}

func runtimeOperationNeedsReadback(operation FabricOperation, now time.Time) bool {
	if operation.Status == "failed" {
		return true
	}
	return operation.Status == "started" && !operation.StartedAt.IsZero() && !now.Before(operation.StartedAt.Add(runtimeClaimStaleAfter))
}

func (s *Service) convergeRuntimeOperationReadback(ctx context.Context, expected FabricOperation, resource any, extra map[string]any) (FabricOperation, error) {
	converger, ok := s.operations.(runtimeReadbackConverger)
	if !ok {
		return FabricOperation{}, ErrRuntimeOperationNotCurrent
	}
	next := expected
	next.Status = "succeeded"
	next.FinishedAt = s.now()
	next.ErrorCode = ""
	next.Retryable = false
	fillOperationResource(&next, resource)
	if len(extra) > 0 {
		payload := maps.Clone(next.RedactedProviderPayload)
		if payload == nil {
			payload = map[string]any{}
		}
		for key, value := range extra {
			payload[key] = value
		}
		next.RedactedProviderPayload = payload
	}
	if err := converger.ConvergeRuntimeReadback(ctx, expected, next); err != nil {
		return FabricOperation{}, err
	}
	return next, nil
}

func runtimeReadbackMatches(result WorkspaceRuntime, input WorkspaceRuntimeInput) bool {
	return strings.HasPrefix(result.ID, "rt_") && result.OperationID == input.RuntimeOperationID &&
		result.WorkspaceID == input.WorkspaceID && (result.Status == "running" || result.Status == "unready") && result.ServiceName != "" &&
		result.ImageID == input.ImageID
}

func gatewaySecretReadbackMatches(result GatewaySecret, input GatewaySecretInput) bool {
	return result.SecretRef == gatewaySecretName(input.WorkspaceID) && result.Fingerprint == input.Fingerprint &&
		result.Version != "" && strings.TrimSpace(result.Version) == result.Version
}

func (s *Service) CreateWorkspaceRuntime(ctx context.Context, input WorkspaceRuntimeInput) (WorkspaceRuntime, error) {
	if strings.TrimSpace(input.IdempotencyKey) == "" {
		return WorkspaceRuntime{}, fmt.Errorf("runtime_idempotency_key_required")
	}
	s.mu.Lock()
	compute := s.computes[input.ComputeID]
	volume := s.volumes[input.VolumeID]
	attachment := s.attachments[input.AttachmentID]
	s.mu.Unlock()
	action := "create_workspace_runtime"
	var original WorkspaceRuntime
	if input.RuntimeOperationID != input.IdempotencyKey {
		var err error
		original, err = s.workspaceRuntimeForUpdate(ctx, input, compute)
		if err != nil {
			return WorkspaceRuntime{}, err
		}
		action = "update_workspace_runtime"
	}
	requestHash := hashInput(input)
	now := s.now()
	operation := newOperation(action, "workspace_runtime", input.WorkspaceID, compute.AccountID, input.WorkspaceID, input.IdempotencyKey, requestHash, now)
	operation.ID = "fop_runtime_claim_" + stableSuffix(action, input.IdempotencyKey)
	operation.Status = "started"
	operation.CreatedAt = now
	fillOperationResource(&operation, WorkspaceRuntime{ID: original.ID, OperationID: input.RuntimeOperationID, WorkspaceID: input.WorkspaceID, ProviderRequestID: providerRequestID("runtime", input.IdempotencyKey)})
	input.OperationID = input.IdempotencyKey
	stored, claimed, err := s.claimRuntimeOperation(ctx, operation)
	if err != nil {
		return WorkspaceRuntime{}, err
	}
	if !claimed {
		if stored.RequestHash != requestHash {
			return WorkspaceRuntime{}, ErrRuntimeIdempotencyConflict
		}
		if runtimeOperationNeedsReadback(stored, now) {
			if err := validateRuntimeInput(input, compute, volume, attachment, action == "update_workspace_runtime", s.provider.ValidateWorkspaceImageReference); err != nil {
				return WorkspaceRuntime{}, ErrRuntimeOperationFailed
			}
			readback, readErr := s.provider.WorkspaceRuntimeStatus(ctx, input.WorkspaceID)
			readback.Access.Password = ""
			if readErr != nil || !runtimeReadbackMatches(readback, input) {
				return WorkspaceRuntime{}, ErrRuntimeOperationFailed
			}
			if action == "update_workspace_runtime" && (readback.ID != original.ID || readback.WorkspaceID != original.WorkspaceID) {
				return WorkspaceRuntime{}, ErrRuntimeOperationFailed
			}
			if _, convergeErr := s.convergeRuntimeOperationReadback(ctx, stored, readback, nil); convergeErr != nil {
				return WorkspaceRuntime{}, convergeErr
			}
			return readback, nil
		}
		return replayRuntimeOperation(stored, requestHash)
	}
	if err := validateRuntimeInput(input, compute, volume, attachment, action == "update_workspace_runtime", s.provider.ValidateWorkspaceImageReference); err != nil {
		_ = s.saveRuntimeOperation(ctx, stored, "failed", WorkspaceRuntime{WorkspaceID: input.WorkspaceID, ProviderRequestID: stored.ProviderRequestID}, err)
		return WorkspaceRuntime{}, err
	}
	runtime, err := s.provider.CreateWorkspaceRuntime(s.providerMutationContext(ctx, operation), input, compute, volume)
	runtime.OperationID = input.RuntimeOperationID
	runtime.Access.Password = ""
	if err == nil && runtime.ImageID != input.ImageID {
		err = fmt.Errorf("workspace_runtime_image_mismatch")
	}
	if err == nil && action == "update_workspace_runtime" && (runtime.ID != original.ID || runtime.WorkspaceID != original.WorkspaceID) {
		err = fmt.Errorf("workspace_runtime_identity_mismatch")
	}
	if err != nil {
		_ = s.saveRuntimeOperation(ctx, stored, "failed", runtime, err)
		return runtime, err
	}
	if err := s.saveRuntimeOperation(ctx, stored, "succeeded", runtime, nil); err != nil {
		return runtime, err
	}
	return runtime, nil
}

func (s *Service) workspaceRuntimeForUpdate(ctx context.Context, input WorkspaceRuntimeInput, compute ComputeAllocation) (WorkspaceRuntime, error) {
	if strings.TrimSpace(input.RuntimeOperationID) == "" || strings.TrimSpace(input.WorkspaceID) == "" || compute.ID == "" {
		return WorkspaceRuntime{}, fmt.Errorf("runtime_operation_identity_mismatch")
	}
	operations, err := s.operations.List(ctx)
	if err != nil {
		return WorkspaceRuntime{}, err
	}
	matches := make([]WorkspaceRuntime, 0, 1)
	for _, operation := range operations {
		if operation.Action != "create_workspace_runtime" || operation.ResourceKind != "workspace_runtime" || operation.Status != "succeeded" ||
			operation.ResourceID != input.WorkspaceID || operation.AccountID != compute.AccountID || operation.WorkspaceID != input.WorkspaceID ||
			operation.IdempotencyKey != input.RuntimeOperationID {
			continue
		}
		var runtime WorkspaceRuntime
		if !decodeOperationResource(operation, &runtime) || runtime.ID == "" || runtime.WorkspaceID != input.WorkspaceID || runtime.OperationID != input.RuntimeOperationID {
			return WorkspaceRuntime{}, fmt.Errorf("runtime_operation_identity_mismatch")
		}
		matches = append(matches, runtime)
	}
	if len(matches) != 1 {
		return WorkspaceRuntime{}, fmt.Errorf("runtime_operation_identity_mismatch")
	}
	return matches[0], nil
}

func (s *Service) DestroyWorkspaceRuntime(ctx context.Context, workspaceID, idempotencyKey string) (WorkspaceRuntime, error) {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(idempotencyKey) == "" {
		return WorkspaceRuntime{}, fmt.Errorf("runtime_destroy_identity_required")
	}
	requestHash := hashInput(map[string]string{"workspaceId": workspaceID})
	now := s.now()
	operation := newOperation("destroy_workspace_runtime", "workspace_runtime", workspaceID, "", workspaceID, idempotencyKey, requestHash, now)
	operation.ID = "fop_runtime_destroy_claim_" + stableSuffix("destroy_workspace_runtime", idempotencyKey)
	operation.Status = "started"
	operation.CreatedAt = now
	fillOperationResource(&operation, WorkspaceRuntime{WorkspaceID: workspaceID, ProviderRequestID: providerRequestID("runtime-destroy", idempotencyKey)})
	stored, claimed, err := s.operations.ClaimRuntime(ctx, operation)
	if err != nil {
		return WorkspaceRuntime{}, err
	}
	if !claimed {
		return replayRuntimeOperation(stored, requestHash)
	}
	runtime, err := s.provider.DestroyWorkspaceRuntime(ctx, workspaceID)
	runtime.Access.Password = ""
	runtime.WorkspaceID = firstNonEmpty(runtime.WorkspaceID, workspaceID)
	runtime.ProviderRequestID = firstNonEmpty(runtime.ProviderRequestID, providerRequestID("runtime-destroy", idempotencyKey))
	if err != nil {
		_ = s.saveRuntimeOperation(ctx, stored, "failed", runtime, err)
		return runtime, err
	}
	if err := s.saveRuntimeOperation(ctx, stored, "succeeded", runtime, nil); err != nil {
		return runtime, err
	}
	return runtime, nil
}

func replayRuntimeOperation(operation FabricOperation, requestHash string) (WorkspaceRuntime, error) {
	if operation.RequestHash != requestHash {
		return WorkspaceRuntime{}, ErrRuntimeIdempotencyConflict
	}
	switch operation.Status {
	case "started":
		return WorkspaceRuntime{}, ErrRuntimeOperationInProgress
	case "succeeded":
		var runtime WorkspaceRuntime
		if decodeOperationResource(operation, &runtime) {
			runtime.Access.Password = ""
			return runtime, nil
		}
	}
	// ponytail: provider apply is not safely repeatable; reconciliation must resolve failed or corrupt claims.
	return WorkspaceRuntime{}, ErrRuntimeOperationFailed
}

func (s *Service) saveRuntimeOperation(ctx context.Context, operation FabricOperation, status string, runtime WorkspaceRuntime, operationErr error) error {
	operation.Status = status
	operation.FinishedAt = s.now()
	operation.ErrorCode = errorCode(operationErr)
	operation.Retryable = false
	fillOperationResource(&operation, runtime)
	return s.operations.SaveRuntime(ctx, operation)
}

func (s *Service) WorkspaceRuntimeStatus(ctx context.Context, workspaceID string) (WorkspaceRuntime, error) {
	runtime, err := s.provider.WorkspaceRuntimeStatus(ctx, workspaceID)
	if err != nil {
		return runtime, err
	}
	if runtime.Status != "running" && runtime.Status != "unready" {
		return runtime, nil
	}
	operations, err := s.operations.List(ctx)
	if err != nil {
		return runtime, err
	}
	matches := make([]FabricOperation, 0, 1)
	for _, operation := range operations {
		if operation.Action != "create_workspace_runtime" || operation.ResourceKind != "workspace_runtime" || operation.Status != "succeeded" || operation.WorkspaceID != workspaceID || operation.ResourceID != workspaceID {
			continue
		}
		matches = append(matches, operation)
	}
	var created WorkspaceRuntime
	if runtime.WorkspaceID != workspaceID || len(matches) != 1 || matches[0].ID == "" || matches[0].CreatedAt.IsZero() || !decodeOperationResource(matches[0], &created) ||
		created.WorkspaceID != workspaceID || strings.TrimSpace(created.ID) == "" || strings.TrimSpace(created.OperationID) == "" ||
		runtime.ID != "" && runtime.ID != created.ID || runtime.OperationID != "" && runtime.OperationID != created.OperationID {
		return runtime, fmt.Errorf("workspace_runtime_identity_unavailable")
	}
	runtime.ID, runtime.OperationID = created.ID, created.OperationID
	return runtime, nil
}

func (s *Service) UpsertGatewaySecret(ctx context.Context, input GatewaySecretInput) (GatewaySecret, error) {
	if strings.TrimSpace(input.AccountID) == "" || strings.TrimSpace(input.WorkspaceID) == "" || input.WorkspaceAPIKeyID <= 0 || strings.TrimSpace(input.GatewayAPIKey) == "" || strings.TrimSpace(input.IdempotencyKey) == "" {
		return GatewaySecret{}, fmt.Errorf("gateway_secret_input_required")
	}
	keyDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(input.GatewayAPIKey)))
	if input.Fingerprint != "sha256:"+keyDigest {
		return GatewaySecret{}, fmt.Errorf("gateway_secret_fingerprint_mismatch")
	}
	requestHash := hashInput(map[string]any{"accountId": input.AccountID, "workspaceId": input.WorkspaceID, "workspaceApiKeyId": input.WorkspaceAPIKeyID, "fingerprint": input.Fingerprint})
	now := s.now()
	secretRef := gatewaySecretName(input.WorkspaceID)
	operation := newOperation("upsert_gateway_secret", "gateway_secret", secretRef, input.AccountID, input.WorkspaceID, input.IdempotencyKey, requestHash, now)
	operation.ID = "fop_gateway_secret_claim_" + stableSuffix("upsert_gateway_secret", input.IdempotencyKey)
	operation.Status = "started"
	operation.CreatedAt = now
	operation.ProviderRequestID = providerRequestID("gateway-secret", input.IdempotencyKey)
	operation.RedactedProviderPayload = map[string]any{"resource": GatewaySecret{SecretRef: secretRef}, "keyDigest": keyDigest}
	stored, claimed, err := s.claimRuntimeOperation(ctx, operation)
	if err != nil {
		return GatewaySecret{}, err
	}
	if !claimed {
		if stored.RequestHash != requestHash {
			return GatewaySecret{}, ErrGatewaySecretIdempotencyConflict
		}
		if runtimeOperationNeedsReadback(stored, now) {
			var readback GatewaySecret
			var readErr error
			switch provider := s.provider.(type) {
			case gatewaySecretReadbackProvider:
				readback, readErr = provider.ReadGatewaySecret(ctx, input)
			case runtimeGatewaySecretProvider:
				var binding WorkspaceRuntimeGatewaySecretBinding
				binding, readErr = provider.WorkspaceRuntimeGatewaySecret(ctx, input.WorkspaceID)
				if readErr == nil && (binding.WorkspaceID != input.WorkspaceID || binding.WorkspaceAPIKeyID != input.WorkspaceAPIKeyID || !binding.Bound) {
					readErr = fmt.Errorf("gateway_secret_readback_mismatch")
				}
				readback = GatewaySecret{SecretRef: binding.SecretRef, Version: keyDigest[:16], Fingerprint: binding.Fingerprint}
			default:
				readErr = fmt.Errorf("gateway_secret_readback_unavailable")
			}
			if readErr != nil || !gatewaySecretReadbackMatches(readback, input) {
				return GatewaySecret{}, fmt.Errorf("gateway_secret_operation_%s", stored.Status)
			}
			if _, convergeErr := s.convergeRuntimeOperationReadback(ctx, stored, readback, map[string]any{"keyDigest": keyDigest}); convergeErr != nil {
				return GatewaySecret{}, convergeErr
			}
			return readback, nil
		}
		if stored.Status == "succeeded" {
			var replayed GatewaySecret
			if decodeOperationResource(stored, &replayed) {
				return replayed, nil
			}
		}
		return GatewaySecret{}, fmt.Errorf("gateway_secret_operation_%s", stored.Status)
	}
	secret, providerErr := s.provider.UpsertGatewaySecret(s.providerMutationContext(ctx, operation), input)
	stored.Status = operationStatus(providerErr)
	stored.FinishedAt = s.now()
	stored.ErrorCode = errorCode(providerErr)
	binding := stored.RedactedProviderPayload[launchStageBindingPayloadKey]
	stored.RedactedProviderPayload = map[string]any{"resource": secret, "keyDigest": keyDigest}
	if binding != nil {
		stored.RedactedProviderPayload[launchStageBindingPayloadKey] = binding
	}
	if saveErr := s.operations.SaveRuntime(ctx, stored); saveErr != nil && providerErr == nil {
		return GatewaySecret{}, saveErr
	}
	return secret, providerErr
}

func (s *Service) BindWorkspaceRuntimeGatewaySecret(ctx context.Context, input WorkspaceRuntimeGatewaySecretInput) (WorkspaceRuntimeGatewaySecretBinding, error) {
	if strings.TrimSpace(input.WorkspaceID) == "" || input.WorkspaceAPIKeyID <= 0 || input.SecretRef != gatewaySecretName(input.WorkspaceID) || strings.TrimSpace(input.Fingerprint) == "" || strings.TrimSpace(input.IdempotencyKey) == "" {
		return WorkspaceRuntimeGatewaySecretBinding{}, fmt.Errorf("workspace_runtime_gateway_secret_input_required")
	}
	provider, ok := s.provider.(runtimeGatewaySecretProvider)
	if !ok {
		return WorkspaceRuntimeGatewaySecretBinding{}, fmt.Errorf("workspace_runtime_gateway_secret_unavailable")
	}
	return provider.BindWorkspaceRuntimeGatewaySecret(ctx, input)
}

func (s *Service) WorkspaceRuntimeGatewaySecret(ctx context.Context, workspaceID string) (WorkspaceRuntimeGatewaySecretBinding, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return WorkspaceRuntimeGatewaySecretBinding{}, fmt.Errorf("workspace_runtime_gateway_secret_input_required")
	}
	provider, ok := s.provider.(runtimeGatewaySecretProvider)
	if !ok {
		return WorkspaceRuntimeGatewaySecretBinding{}, fmt.Errorf("workspace_runtime_gateway_secret_unavailable")
	}
	return provider.WorkspaceRuntimeGatewaySecret(ctx, workspaceID)
}
func validateRuntimeInput(input WorkspaceRuntimeInput, compute ComputeAllocation, volume StorageVolume, attachment StorageAttachment, update bool, validImage func(string) bool) error {
	if compute.ID == "" {
		return fmt.Errorf("compute_allocation_not_found")
	}
	if volume.ID == "" {
		return fmt.Errorf("storage_volume_not_found")
	}
	if compute.AccountID == "" || volume.AccountID == "" || compute.AccountID != volume.AccountID {
		return fmt.Errorf("resource_account_mismatch")
	}
	if strings.TrimSpace(input.WorkspaceID) == "" || input.WorkspaceID != compute.WorkspaceID || input.WorkspaceID != volume.WorkspaceID {
		return fmt.Errorf("resource_workspace_mismatch")
	}
	if attachment.ID == "" {
		return fmt.Errorf("storage_attachment_not_found")
	}
	if input.AttachmentID != attachment.ID || input.AttachmentOperationID == "" || input.AttachmentOperationID != attachment.OperationID ||
		attachment.WorkspaceID != input.WorkspaceID || attachment.ComputeID != input.ComputeID || attachment.VolumeID != input.VolumeID || attachment.Status != "attached" {
		return fmt.Errorf("storage_attachment_identity_mismatch")
	}
	if input.RuntimeOperationID == "" || update == (input.RuntimeOperationID == input.IdempotencyKey) {
		return fmt.Errorf("runtime_operation_identity_mismatch")
	}
	if !isReadyResourceStatus(compute.Status) || volume.Status != "ready" {
		return fmt.Errorf("resource_status_invalid")
	}
	if validImage == nil || !validImage(input.ImageID) {
		return fmt.Errorf("workspace_image_identity_invalid")
	}
	if strings.TrimSpace(input.GatewaySecretRef) == "" || input.GatewaySecretRef != gatewaySecretName(input.WorkspaceID) {
		return fmt.Errorf("gateway_secret_ref_mismatch")
	}
	return nil
}

func validWorkspaceRuntimeImageIdentity(value string) bool {
	value = strings.TrimSpace(value)
	prefix := workspaceImageRepository + "@sha256:"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+sha256.Size*2 {
		return false
	}
	digest := strings.TrimPrefix(value, prefix)
	return digest == strings.ToLower(digest) && validDigest(digest)
}
