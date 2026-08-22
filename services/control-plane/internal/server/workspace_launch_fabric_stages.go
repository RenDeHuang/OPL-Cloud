package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"opl-cloud/services/control-plane/internal/clients"
)

var workspaceLaunchFabricStages = map[string]string{
	"ensure_compute_allocation": "ensure_compute_allocation",
	"storage":                   "ensure_storage",
	"attachment":                "ensure_attachment",
	"secret":                    "ensure_gateway_secret",
	"runtime":                   "ensure_runtime",
}

func (a *controlPlaneWorkspaceLaunchStageAdapter) readWorkspaceLaunchFabricStage(ctx context.Context, operation workspaceLaunchReconcileOperation) (workspaceLaunchStageObservation, error) {
	input, err := a.workspaceLaunchFabricStageInput(ctx, operation, false)
	if err != nil {
		return workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, err
	}
	result, err := a.service.ReadWorkspaceLaunchStage(ctx, input)
	if err != nil {
		return workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, err
	}
	return workspaceLaunchFabricObservation(operation, input, result)
}

func (a *controlPlaneWorkspaceLaunchStageAdapter) mutateWorkspaceLaunchFabricStage(ctx context.Context, operation workspaceLaunchReconcileOperation, idempotencyKey string) error {
	if idempotencyKey != workspaceLaunchStageIdempotencyKey(operation, 1) {
		return errInvalidWorkspaceLaunchOperation
	}
	input, err := a.workspaceLaunchFabricStageInput(ctx, operation, operation.Stage == "secret")
	if err != nil {
		return err
	}
	_, err = a.service.EnsureWorkspaceLaunchStage(ctx, input)
	return err
}

func (a *controlPlaneWorkspaceLaunchStageAdapter) workspaceLaunchFabricStageInput(ctx context.Context, operation workspaceLaunchReconcileOperation, includeCredential bool) (clients.WorkspaceLaunchStageInput, error) {
	action, ok := workspaceLaunchFabricStages[operation.Stage]
	if !ok {
		return clients.WorkspaceLaunchStageInput{}, errInvalidWorkspaceLaunchOperation
	}
	resources := workspaceLaunchFabricResources(operation)
	binding := clients.WorkspaceLaunchStageBinding{
		SchemaVersion:     clients.WorkspaceLaunchFabricSchemaVersion,
		LaunchOperationID: operation.ID,
		AccountID:         operation.stringFact("accountId"),
		WorkspaceID:       operation.stringFact("workspaceId"),
		Stage:             operation.Stage,
		Action:            action,
		FabricOperationID: operation.ID + ":" + operation.Stage,
		IdempotencyKey:    workspaceLaunchStageIdempotencyKey(operation, 1),
	}
	binding.ExpectedResourceBinding = workspaceLaunchCurrentStageBinding(operation)
	input := clients.WorkspaceLaunchStageInput{
		Binding: binding, ProviderProfileRef: operation.stringFact("providerProfileRef"),
		PreflightBindingRef: operation.stringFact("preflightBindingRef"), SpecDigest: operation.stringFact("specDigest"), PackageID: operation.stringFact("packageId"),
		SizeGB: operation.intFact("sizeGb"), WorkspaceImageDigest: operation.stringFact("workspaceImageDigest"), Resources: resources,
	}
	input.Binding.RequestHash = workspaceLaunchFabricRequestHash(input, operation.stringFact("requestHash"))
	if includeCredential {
		keyID := operation.int64Fact("workspaceApiKeyId")
		if !operation.boolFact("resourceBillingEnabled") {
			// The delegated list endpoint may expose the one-time key on
			// Sub2API installations that support customer-owned reveals. If it
			// does not, the key-stage materialization has already written the
			// Fabric secret and only the key identity is required for readback.
			input.GatewayCredential = &clients.WorkspaceLaunchGatewayCredential{KeyID: keyID}
			if keys, err := a.service.GatewayWorkspaceKeysForConvergence(ctx, a.keyCredential, operation.int64Fact("sub2apiUserId"), workspaceReservedKeyName(operation.stringFact("workspaceId"))); err == nil {
				for _, key := range keys {
					if key.ID == keyID && key.Status == "active" && strings.TrimSpace(key.Key) != "" && workspaceLaunchCredentialFingerprint(key.Key) == operation.stringFact("workspaceKeyFingerprint") {
						input.GatewayCredential.Value = key.Key
						break
					}
				}
			}
			if strings.TrimSpace(input.GatewayCredential.Value) == "" {
				key, err := a.service.GatewayUserKey(ctx, a.keyCredential, operation.int64Fact("sub2apiUserId"), keyID)
				if err == nil && key.Status == "active" && strings.TrimSpace(key.Key) != "" && workspaceLaunchCredentialFingerprint(key.Key) == operation.stringFact("workspaceKeyFingerprint") {
					input.GatewayCredential.Value = key.Key
				}
			}
			return input, nil
		}
		if a.workspaceLaunchKeyMutationCredentialValid(operation) {
			key, err := a.service.GatewayUserKey(ctx, a.keyCredential, operation.int64Fact("sub2apiUserId"), keyID)
			if err != nil {
				return clients.WorkspaceLaunchStageInput{}, err
			}
			if key.Status != "active" || strings.TrimSpace(key.Key) == "" || workspaceLaunchCredentialFingerprint(key.Key) != operation.stringFact("workspaceKeyFingerprint") {
				return clients.WorkspaceLaunchStageInput{}, errInvalidWorkspaceLaunchOperation
			}
			input.GatewayCredential = &clients.WorkspaceLaunchGatewayCredential{KeyID: key.ID, Value: key.Key}
			return input, nil
		}
		var keys []clients.Sub2APIWorkspaceKey
		var err error
		if a.workspaceLaunchKeyMutationCredentialValid(operation) {
			keys, err = a.service.GatewayWorkspaceKeysForConvergence(ctx, a.keyCredential, operation.int64Fact("sub2apiUserId"), workspaceReservedKeyName(operation.stringFact("workspaceId")))
		} else {
			keys, err = a.service.WorkspaceKeysForConvergence(ctx, operation.int64Fact("sub2apiUserId"), workspaceReservedKeyName(operation.stringFact("workspaceId")))
		}
		if err != nil {
			return clients.WorkspaceLaunchStageInput{}, err
		}
		for _, key := range keys {
			if key.ID != keyID {
				continue
			}
			if key.Status != "active" || strings.TrimSpace(key.Key) == "" || workspaceLaunchCredentialFingerprint(key.Key) != operation.stringFact("workspaceKeyFingerprint") {
				return clients.WorkspaceLaunchStageInput{}, errInvalidWorkspaceLaunchOperation
			}
			input.GatewayCredential = &clients.WorkspaceLaunchGatewayCredential{KeyID: key.ID, Value: key.Key}
			break
		}
		if input.GatewayCredential == nil {
			return clients.WorkspaceLaunchStageInput{}, errInvalidWorkspaceLaunchOperation
		}
	}
	return input, nil
}

func workspaceLaunchFabricObservation(operation workspaceLaunchReconcileOperation, input clients.WorkspaceLaunchStageInput, result clients.WorkspaceLaunchStageResult) (workspaceLaunchStageObservation, error) {
	if result.SchemaVersion != clients.WorkspaceLaunchFabricSchemaVersion || result.Binding != input.Binding ||
		!workspaceLaunchResourcesPreserveIdentity(input.Resources, result.Resources) {
		return workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, nil
	}
	switch {
	case result.State == workspaceLaunchStageAbsent && (result.Reason == "no_stage_record" || result.Reason == "started_no_resource" || result.Reason == "failed_no_resource"):
		return workspaceLaunchStageObservation{State: workspaceLaunchStageAbsent}, nil
	case result.State == workspaceLaunchStagePending && result.Reason == "provider_provisioning":
		return workspaceLaunchStageObservation{State: workspaceLaunchStagePending}, nil
	case operation.Stage == "ensure_compute_allocation" && result.State == workspaceLaunchStagePending && result.Reason == "ownership_pending":
		return workspaceLaunchStageObservation{State: workspaceLaunchStageOwnershipPending}, nil
	case result.State == workspaceLaunchStageReady && result.Reason == "none":
		facts, err := workspaceLaunchFabricStageFacts(operation.Stage, result.Resources, operation)
		if err != nil {
			return workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, nil
		}
		if _, err := validateWorkspaceLaunchStageFacts(operation.Stage, facts, true); err != nil {
			return workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, nil
		}
		return workspaceLaunchStageObservation{State: workspaceLaunchStageReady, Facts: facts}, nil
	case result.State == workspaceLaunchStageUnknown && (result.Reason == "failed_no_resource_unproven" || result.Reason == "resource_absence_status_conflict"):
		return workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, nil
	default:
		return workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, nil
	}
}

func workspaceLaunchFabricStageFacts(stage string, resources clients.WorkspaceLaunchResources, operation workspaceLaunchReconcileOperation) (map[string]any, error) {
	switch stage {
	case "ensure_compute_allocation":
		return map[string]any{"computeAllocationId": resources.ComputeAllocationID, "computeBindingRef": resources.ComputeBindingRef}, nil
	case "storage":
		return map[string]any{"storageId": resources.StorageID, "storageBindingRef": resources.StorageBindingRef}, nil
	case "attachment":
		return map[string]any{"attachmentId": resources.AttachmentID, "attachmentBindingRef": resources.AttachmentBindingRef}, nil
	case "secret":
		return map[string]any{
			"gatewaySecretRef": resources.GatewaySecretRef, "gatewaySecretVersion": resources.GatewaySecretVersion,
			"secretBindingRef": resources.SecretBindingRef, "workspaceKeyStatus": "configured",
			"workspaceKeyFingerprint": operation.stringFact("workspaceKeyFingerprint"),
		}, nil
	case "runtime":
		return map[string]any{
			"runtimeId": resources.RuntimeID, "runtimeReady": true, "runtimeServiceName": resources.RuntimeServiceName,
			"runtimeBindingRef": resources.RuntimeBindingRef, "runtimeUsername": resources.RuntimeUsername, "url": resources.RuntimeURL,
			"credentialStatus": resources.RuntimeCredentialStatus, "credentialVersion": resources.RuntimeCredentialVersion,
			"credentialSecretRef": resources.RuntimeCredentialSecretRef,
		}, nil
	default:
		return nil, errInvalidWorkspaceLaunchOperation
	}
}

func workspaceLaunchFabricResources(operation workspaceLaunchReconcileOperation) clients.WorkspaceLaunchResources {
	return clients.WorkspaceLaunchResources{
		ComputeAllocationID: operation.stringFact("computeAllocationId"), ComputeBindingRef: operation.stringFact("computeBindingRef"),
		StorageID: operation.stringFact("storageId"), StorageBindingRef: operation.stringFact("storageBindingRef"),
		AttachmentID: operation.stringFact("attachmentId"), AttachmentBindingRef: operation.stringFact("attachmentBindingRef"),
		GatewaySecretRef: operation.stringFact("gatewaySecretRef"), GatewaySecretVersion: operation.stringFact("gatewaySecretVersion"),
		GatewaySecretFingerprint: operation.stringFact("workspaceKeyFingerprint"), SecretBindingRef: operation.stringFact("secretBindingRef"),
		RuntimeID: operation.stringFact("runtimeId"), RuntimeServiceName: operation.stringFact("runtimeServiceName"),
		RuntimeUsername: operation.stringFact("runtimeUsername"), RuntimeURL: operation.stringFact("url"),
		RuntimeCredentialStatus: operation.stringFact("credentialStatus"), RuntimeCredentialVersion: operation.stringFact("credentialVersion"),
		RuntimeCredentialSecretRef: operation.stringFact("credentialSecretRef"), RuntimeBindingRef: operation.stringFact("runtimeBindingRef"),
	}
}

func workspaceLaunchResourcesPreserveIdentity(current, result clients.WorkspaceLaunchResources) bool {
	currentJSON, _ := json.Marshal(current)
	resultJSON, _ := json.Marshal(result)
	var currentFields, resultFields map[string]string
	if json.Unmarshal(currentJSON, &currentFields) != nil || json.Unmarshal(resultJSON, &resultFields) != nil {
		return false
	}
	for field, value := range currentFields {
		if value != "" && resultFields[field] != value {
			return false
		}
	}
	return true
}

func workspaceLaunchCurrentStageBinding(operation workspaceLaunchReconcileOperation) string {
	return map[string]string{
		"ensure_compute_allocation": operation.stringFact("computeBindingRef"),
		"storage":                   operation.stringFact("storageBindingRef"), "attachment": operation.stringFact("attachmentBindingRef"),
		"secret": operation.stringFact("secretBindingRef"), "runtime": operation.stringFact("runtimeBindingRef"),
	}[operation.Stage]
}

func workspaceLaunchFabricRequestHash(input clients.WorkspaceLaunchStageInput, launchRequestHash string) string {
	payload, _ := json.Marshal(struct {
		LaunchRequestHash string                           `json:"launchRequestHash"`
		Action            string                           `json:"action"`
		PackageID         string                           `json:"packageId"`
		SizeGB            int                              `json:"sizeGb"`
		ImageDigest       string                           `json:"imageDigest"`
		Resources         clients.WorkspaceLaunchResources `json:"resources"`
	}{launchRequestHash, input.Binding.Action, input.PackageID, input.SizeGB, input.WorkspaceImageDigest, input.Resources})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func workspaceLaunchResumeAuthorizationDigest(authorization workspaceLaunchResumeAuthorization) string {
	payload, err := json.Marshal(authorization)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("sha256:%x", digest[:])
}
