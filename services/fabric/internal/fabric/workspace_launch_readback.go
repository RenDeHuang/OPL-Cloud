package fabric

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"
)

var (
	ErrWorkspaceLaunchStageReadbackInvalid     = errors.New("workspace_launch_stage_readback_invalid")
	ErrWorkspaceLaunchStageReadbackUnavailable = errors.New("workspace_launch_stage_readback_unavailable")
)

type WorkspaceLaunchStageReadbackInput struct {
	Stage                    string `json:"stage"`
	FabricRecordID           string `json:"fabricRecordId"`
	FabricOperationID        string `json:"fabricOperationId"`
	AccountID                string `json:"accountId"`
	WorkspaceID              string `json:"workspaceId"`
	IdempotencyKey           string `json:"idempotencyKey"`
	RequestHash              string `json:"requestHash"`
	ComputeID                string `json:"computeId,omitempty"`
	StorageID                string `json:"storageId,omitempty"`
	AttachmentID             string `json:"attachmentId,omitempty"`
	AttachmentOperationID    string `json:"attachmentOperationId,omitempty"`
	RuntimeID                string `json:"runtimeId,omitempty"`
	RuntimeOperationID       string `json:"runtimeOperationId,omitempty"`
	ImageID                  string `json:"imageId,omitempty"`
	GatewaySecretRef         string `json:"gatewaySecretRef,omitempty"`
	GatewaySecretFingerprint string `json:"gatewaySecretFingerprint,omitempty"`
	WorkspaceAPIKeyID        int64  `json:"workspaceApiKeyId,omitempty"`
	ExpectedBindingDigest    string `json:"expectedBindingDigest,omitempty"`
}

type WorkspaceLaunchStageReadbackProof struct {
	SchemaVersion                int             `json:"schemaVersion"`
	Eligible                     bool            `json:"eligible"`
	Reason                       string          `json:"reason"`
	Stage                        string          `json:"stage"`
	PriorStatus                  string          `json:"priorStatus"`
	BindingDigest                string          `json:"bindingDigest"`
	Operation                    FabricOperation `json:"operation"`
	Sub2APIMutationCount         int             `json:"sub2apiMutationCount"`
	TencentMutationCount         int             `json:"tencentMutationCount"`
	KubernetesMutationCount      int             `json:"kubernetesMutationCount"`
	FabricOperationMutationCount int             `json:"fabricOperationMutationCount"`
}

type GatewaySecretReadbackInput struct {
	AccountID         string
	WorkspaceID       string
	WorkspaceAPIKeyID int64
	SecretRef         string
	Fingerprint       string
	KeyDigest         string
}

type gatewaySecretDigestReadbackProvider interface {
	ReadGatewaySecretByDigest(context.Context, GatewaySecretReadbackInput) (GatewaySecret, error)
}

type workspaceLaunchStageSpec struct {
	action, resourceKind, resourceID string
}

func workspaceLaunchReadbackSpec(input WorkspaceLaunchStageReadbackInput) (workspaceLaunchStageSpec, error) {
	switch input.Stage {
	case "attachment":
		if input.AttachmentID == "" || input.AttachmentOperationID == "" || input.ComputeID == "" || input.StorageID == "" ||
			input.IdempotencyKey != input.AttachmentOperationID {
			return workspaceLaunchStageSpec{}, ErrWorkspaceLaunchStageReadbackInvalid
		}
		return workspaceLaunchStageSpec{action: "create_storage_attachment", resourceKind: "storage_attachment", resourceID: input.AttachmentID}, nil
	case "secret":
		if input.GatewaySecretRef == "" || input.WorkspaceAPIKeyID <= 0 {
			return workspaceLaunchStageSpec{}, ErrWorkspaceLaunchStageReadbackInvalid
		}
		return workspaceLaunchStageSpec{action: "upsert_gateway_secret", resourceKind: "gateway_secret", resourceID: input.GatewaySecretRef}, nil
	case "runtime":
		if input.ComputeID == "" || input.StorageID == "" || input.AttachmentID == "" || input.AttachmentOperationID == "" ||
			input.RuntimeID == "" || input.RuntimeOperationID == "" || input.ImageID == "" || input.GatewaySecretRef == "" ||
			input.IdempotencyKey != input.RuntimeOperationID {
			return workspaceLaunchStageSpec{}, ErrWorkspaceLaunchStageReadbackInvalid
		}
		return workspaceLaunchStageSpec{action: "create_workspace_runtime", resourceKind: "workspace_runtime", resourceID: input.WorkspaceID}, nil
	default:
		return workspaceLaunchStageSpec{}, ErrWorkspaceLaunchStageReadbackInvalid
	}
}

func (s *Service) workspaceLaunchReadbackOperation(ctx context.Context, input WorkspaceLaunchStageReadbackInput, spec workspaceLaunchStageSpec) (FabricOperation, error) {
	if input.FabricRecordID == "" || input.FabricOperationID == "" || input.AccountID == "" || input.WorkspaceID == "" ||
		input.IdempotencyKey == "" || len(input.RequestHash) != 64 || strings.TrimSpace(input.FabricRecordID) != input.FabricRecordID ||
		strings.TrimSpace(input.FabricOperationID) != input.FabricOperationID || strings.TrimSpace(input.IdempotencyKey) != input.IdempotencyKey ||
		input.ExpectedBindingDigest != "" && !validWorkspaceLaunchReadbackDigest(input.ExpectedBindingDigest) {
		return FabricOperation{}, ErrWorkspaceLaunchStageReadbackInvalid
	}
	operations, err := s.operations.List(ctx)
	if err != nil {
		return FabricOperation{}, fmt.Errorf("%w: operation_store", ErrWorkspaceLaunchStageReadbackUnavailable)
	}
	matches := make([]FabricOperation, 0, 1)
	for _, operation := range operations {
		related := operation.ID == input.FabricRecordID || operation.OperationID == input.FabricOperationID ||
			operation.Action == spec.action && operation.IdempotencyKey == input.IdempotencyKey
		if !related {
			continue
		}
		if operation.ID != input.FabricRecordID || operation.OperationID != input.FabricOperationID || operation.Action != spec.action ||
			operation.ResourceKind != spec.resourceKind || operation.ResourceID != spec.resourceID || operation.AccountID != input.AccountID ||
			operation.WorkspaceID != input.WorkspaceID || operation.IdempotencyKey != input.IdempotencyKey || operation.RequestHash != input.RequestHash ||
			(operation.Status != "started" && operation.Status != "failed" && operation.Status != "succeeded") {
			return FabricOperation{}, ErrWorkspaceLaunchStageReadbackInvalid
		}
		matches = append(matches, operation)
	}
	if len(matches) != 1 {
		return FabricOperation{}, ErrWorkspaceLaunchStageReadbackInvalid
	}
	if matches[0].Status == "succeeded" && input.ExpectedBindingDigest == "" {
		return FabricOperation{}, ErrWorkspaceLaunchStageReadbackInvalid
	}
	return matches[0], nil
}

func validWorkspaceLaunchReadbackDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func workspaceLaunchReadbackKeyDigest(operation FabricOperation) (string, bool) {
	value, ok := operation.RedactedProviderPayload["keyDigest"].(string)
	if !ok || len(value) != 64 {
		return "", false
	}
	decoded, err := hex.DecodeString(value)
	return value, err == nil && len(decoded) == 32
}

func workspaceLaunchReadbackExpectedHash(input WorkspaceLaunchStageReadbackInput, operation FabricOperation) (string, error) {
	switch input.Stage {
	case "attachment":
		return hashInput(StorageAttachmentInput{WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.StorageID}), nil
	case "secret":
		keyDigest, ok := workspaceLaunchReadbackKeyDigest(operation)
		if !ok {
			return "", ErrWorkspaceLaunchStageReadbackInvalid
		}
		fingerprint := "sha256:" + keyDigest
		if input.GatewaySecretFingerprint != "" && input.GatewaySecretFingerprint != fingerprint {
			return "", ErrWorkspaceLaunchStageReadbackInvalid
		}
		return hashInput(map[string]any{
			"accountId": input.AccountID, "workspaceId": input.WorkspaceID,
			"workspaceApiKeyId": input.WorkspaceAPIKeyID, "fingerprint": fingerprint,
		}), nil
	case "runtime":
		return hashInput(WorkspaceRuntimeInput{
			WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.StorageID,
			AttachmentID: input.AttachmentID, AttachmentOperationID: input.AttachmentOperationID,
			RuntimeOperationID: input.RuntimeOperationID, ImageID: input.ImageID, GatewaySecretRef: input.GatewaySecretRef,
		}), nil
	default:
		return "", ErrWorkspaceLaunchStageReadbackInvalid
	}
}

func exactWorkspaceLaunchCostTags(tags map[string]string, accountID, workspaceID, resourceID, operationID string) bool {
	return tags["opl_account_id"] == accountID && tags["opl_workspace_id"] == workspaceID &&
		tags["opl_resource_id"] == resourceID && tags["opl_operation_id"] == operationID
}

func workspaceLaunchReadbackNextOperation(expected FabricOperation, resource any, extra map[string]any, now func() time.Time) FabricOperation {
	next := expected
	next.Status = "succeeded"
	next.FinishedAt = now()
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
	return next
}

func workspaceLaunchReadbackPublicOperation(operation FabricOperation) FabricOperation {
	public := operation
	public.RedactedProviderPayload = maps.Clone(operation.RedactedProviderPayload)
	delete(public.RedactedProviderPayload, "keyDigest")
	return public
}

func workspaceLaunchReadbackBinding(stage string, operation FabricOperation) string {
	return hashInput(map[string]any{
		"stage": stage, "fabricRecordId": operation.ID, "fabricOperationId": operation.OperationID,
		"action": operation.Action, "resourceKind": operation.ResourceKind, "resourceId": operation.ResourceID,
		"accountId": operation.AccountID, "workspaceId": operation.WorkspaceID,
		"idempotencyKey": operation.IdempotencyKey, "requestHash": operation.RequestHash,
		"resource": operation.RedactedProviderPayload["resource"],
	})
}

func (s *Service) workspaceLaunchStageReadback(ctx context.Context, input WorkspaceLaunchStageReadbackInput) (FabricOperation, FabricOperation, WorkspaceLaunchStageReadbackProof, error) {
	spec, err := workspaceLaunchReadbackSpec(input)
	if err != nil {
		return FabricOperation{}, FabricOperation{}, WorkspaceLaunchStageReadbackProof{}, err
	}
	expected, err := s.workspaceLaunchReadbackOperation(ctx, input, spec)
	if err != nil {
		return FabricOperation{}, FabricOperation{}, WorkspaceLaunchStageReadbackProof{}, err
	}
	expectedHash, err := workspaceLaunchReadbackExpectedHash(input, expected)
	if err != nil || expectedHash == "" || expectedHash != expected.RequestHash {
		return FabricOperation{}, FabricOperation{}, WorkspaceLaunchStageReadbackProof{}, ErrWorkspaceLaunchStageReadbackInvalid
	}

	var resource any
	var extra map[string]any
	switch input.Stage {
	case "attachment":
		s.mu.Lock()
		compute, volume := s.computes[input.ComputeID], s.volumes[input.StorageID]
		s.mu.Unlock()
		reader, ok := s.provider.(storageAttachmentReadbackProvider)
		if !ok || compute.ID != input.ComputeID || volume.ID != input.StorageID {
			return FabricOperation{}, FabricOperation{}, WorkspaceLaunchStageReadbackProof{}, ErrWorkspaceLaunchStageReadbackInvalid
		}
		attachment, readErr := reader.ReadStorageAttachment(ctx, StorageAttachment{
			ID: input.AttachmentID, OperationID: input.AttachmentOperationID, WorkspaceID: input.WorkspaceID,
			ComputeID: input.ComputeID, VolumeID: input.StorageID,
		}, compute, volume)
		if readErr != nil {
			return FabricOperation{}, FabricOperation{}, WorkspaceLaunchStageReadbackProof{}, fmt.Errorf("%w: attachment", ErrWorkspaceLaunchStageReadbackUnavailable)
		}
		if !attachmentReadbackMatches(attachment, StorageAttachmentInput{
			WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.StorageID, IdempotencyKey: input.IdempotencyKey,
		}, compute, volume) || attachment.ID != input.AttachmentID ||
			!exactWorkspaceLaunchCostTags(attachment.CostTags, input.AccountID, input.WorkspaceID, input.AttachmentID, input.AttachmentOperationID) {
			return FabricOperation{}, FabricOperation{}, WorkspaceLaunchStageReadbackProof{}, ErrWorkspaceLaunchStageReadbackInvalid
		}
		resource = attachment
	case "secret":
		reader, ok := s.provider.(gatewaySecretDigestReadbackProvider)
		keyDigest, digestOK := workspaceLaunchReadbackKeyDigest(expected)
		if !ok || !digestOK {
			return FabricOperation{}, FabricOperation{}, WorkspaceLaunchStageReadbackProof{}, ErrWorkspaceLaunchStageReadbackInvalid
		}
		fingerprint := "sha256:" + keyDigest
		secret, readErr := reader.ReadGatewaySecretByDigest(ctx, GatewaySecretReadbackInput{
			AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, WorkspaceAPIKeyID: input.WorkspaceAPIKeyID,
			SecretRef: input.GatewaySecretRef, Fingerprint: fingerprint, KeyDigest: keyDigest,
		})
		if readErr != nil {
			return FabricOperation{}, FabricOperation{}, WorkspaceLaunchStageReadbackProof{}, fmt.Errorf("%w: secret", ErrWorkspaceLaunchStageReadbackUnavailable)
		}
		if secret.SecretRef != input.GatewaySecretRef || secret.Fingerprint != fingerprint || secret.Version != keyDigest[:16] {
			return FabricOperation{}, FabricOperation{}, WorkspaceLaunchStageReadbackProof{}, ErrWorkspaceLaunchStageReadbackInvalid
		}
		resource, extra = secret, map[string]any{"keyDigest": keyDigest}
	case "runtime":
		s.mu.Lock()
		compute, volume, attachment := s.computes[input.ComputeID], s.volumes[input.StorageID], s.attachments[input.AttachmentID]
		s.mu.Unlock()
		runtimeInput := WorkspaceRuntimeInput{
			WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.StorageID,
			AttachmentID: input.AttachmentID, AttachmentOperationID: input.AttachmentOperationID,
			RuntimeOperationID: input.RuntimeOperationID, ImageID: input.ImageID, GatewaySecretRef: input.GatewaySecretRef,
			IdempotencyKey: input.IdempotencyKey,
		}
		if err := validateRuntimeInput(runtimeInput, compute, volume, attachment, false, s.provider.ValidateWorkspaceImageReference); err != nil {
			return FabricOperation{}, FabricOperation{}, WorkspaceLaunchStageReadbackProof{}, ErrWorkspaceLaunchStageReadbackInvalid
		}
		runtime, readErr := s.provider.WorkspaceRuntimeStatus(ctx, input.WorkspaceID)
		runtime.Access.Password = ""
		if readErr != nil {
			return FabricOperation{}, FabricOperation{}, WorkspaceLaunchStageReadbackProof{}, fmt.Errorf("%w: runtime", ErrWorkspaceLaunchStageReadbackUnavailable)
		}
		if !runtimeReadbackMatches(runtime, runtimeInput) || runtime.ID != input.RuntimeID ||
			!exactWorkspaceLaunchCostTags(runtime.CostTags, input.AccountID, input.WorkspaceID, input.RuntimeID, input.RuntimeOperationID) {
			return FabricOperation{}, FabricOperation{}, WorkspaceLaunchStageReadbackProof{}, ErrWorkspaceLaunchStageReadbackInvalid
		}
		resource = runtime
	}

	next := workspaceLaunchReadbackNextOperation(expected, resource, extra, s.now)
	public := workspaceLaunchReadbackPublicOperation(next)
	proof := WorkspaceLaunchStageReadbackProof{
		SchemaVersion: 1, Eligible: true, Reason: "none", Stage: input.Stage, PriorStatus: expected.Status,
		BindingDigest: workspaceLaunchReadbackBinding(input.Stage, public), Operation: public,
	}
	if proof.BindingDigest == "" {
		return FabricOperation{}, FabricOperation{}, WorkspaceLaunchStageReadbackProof{}, ErrWorkspaceLaunchStageReadbackInvalid
	}
	return expected, next, proof, nil
}

func (s *Service) WorkspaceLaunchStageReadbackProof(ctx context.Context, input WorkspaceLaunchStageReadbackInput) (WorkspaceLaunchStageReadbackProof, error) {
	_, _, proof, err := s.workspaceLaunchStageReadback(ctx, input)
	return proof, err
}

func (s *Service) ConvergeWorkspaceLaunchStageReadback(ctx context.Context, input WorkspaceLaunchStageReadbackInput) (WorkspaceLaunchStageReadbackProof, error) {
	expected, next, proof, err := s.workspaceLaunchStageReadback(ctx, input)
	if err != nil {
		return proof, err
	}
	if input.ExpectedBindingDigest == "" || input.ExpectedBindingDigest != proof.BindingDigest {
		return WorkspaceLaunchStageReadbackProof{}, ErrWorkspaceLaunchStageReadbackInvalid
	}
	if expected.Status == "succeeded" {
		expectedPublic := workspaceLaunchReadbackPublicOperation(expected)
		if workspaceLaunchReadbackBinding(input.Stage, expectedPublic) != proof.BindingDigest {
			return WorkspaceLaunchStageReadbackProof{}, ErrWorkspaceLaunchStageReadbackInvalid
		}
		proof.Operation = expectedPublic
		proof.PriorStatus = "succeeded"
		return proof, nil
	}
	converger, ok := s.operations.(runtimeReadbackConverger)
	if !ok {
		return WorkspaceLaunchStageReadbackProof{}, ErrRuntimeOperationNotCurrent
	}
	if err := converger.ConvergeRuntimeReadback(ctx, expected, next); err != nil {
		return WorkspaceLaunchStageReadbackProof{}, err
	}
	proof.FabricOperationMutationCount = 1
	if input.Stage == "attachment" {
		var attachment StorageAttachment
		if decodeOperationResource(next, &attachment) {
			s.mu.Lock()
			s.attachments[attachment.ID] = attachment
			s.mu.Unlock()
		}
	}
	return proof, nil
}
