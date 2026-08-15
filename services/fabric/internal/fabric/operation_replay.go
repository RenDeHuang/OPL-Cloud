package fabric

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func replayResourceState(ctx context.Context, operations OperationStore) (map[string]ComputeAllocation, map[string]StorageVolume, map[string]StorageSnapshot, map[string]StorageAttachment, map[string]WorkspaceRuntime) {
	computes := map[string]ComputeAllocation{}
	volumes := map[string]StorageVolume{}
	snapshots := map[string]StorageSnapshot{}
	attachments := map[string]StorageAttachment{}
	runtimes := map[string]WorkspaceRuntime{}
	records, err := operations.List(ctx)
	if err != nil {
		return computes, volumes, snapshots, attachments, runtimes
	}
	for _, operation := range records {
		switch operation.ResourceKind {
		case "compute_allocation":
			var resource ComputeAllocation
			if !decodeOperationResource(operation, &resource) {
				continue
			}
			if operation.Status == "started" && operation.Action != "create_compute_allocation" {
				continue
			}
			if operation.Status == "failed" && !strings.HasPrefix(operation.Action, "create_") {
				continue
			}
			computes[resource.ID] = resource
		case "storage_volume":
			var resource StorageVolume
			if !decodeOperationResource(operation, &resource) {
				continue
			}
			if operation.Status != "succeeded" {
				if operation.Status != "failed" || operation.Action != "create_storage_volume" || !strings.HasPrefix(resource.ProviderResourceID, "disk-") {
					continue
				}
				resource.Status = "quarantined"
			}
			volumes[resource.ID] = resource
		case "storage_snapshot":
			var resource StorageSnapshot
			if operation.Status != "succeeded" || !decodeOperationResource(operation, &resource) {
				continue
			}
			snapshots[resource.ID] = resource
		case "storage_attachment":
			var resource StorageAttachment
			if operation.Status != "succeeded" || !decodeOperationResource(operation, &resource) {
				continue
			}
			attachments[resource.ID] = resource
		case "workspace_runtime":
			var resource WorkspaceRuntime
			if operation.Status != "succeeded" || !decodeOperationResource(operation, &resource) {
				continue
			}
			runtimes[resource.WorkspaceID] = resource
		}
	}
	return computes, volumes, snapshots, attachments, runtimes
}

func decodeOperationResource(operation FabricOperation, target any) bool {
	resource, ok := operation.RedactedProviderPayload["resource"]
	if !ok {
		return false
	}
	data, err := json.Marshal(resource)
	if err != nil {
		return false
	}
	return json.Unmarshal(data, target) == nil
}

func newOperation(action string, resourceKind string, resourceID string, accountID string, workspaceID string, idempotencyKey string, requestHash string, now time.Time) FabricOperation {
	operationID := "op_" + action + "_" + stableSuffix(firstNonEmpty(idempotencyKey, resourceID, accountID, workspaceID, fmt.Sprintf("%d", now.UnixNano())), resourceKind, action)[:12]
	return FabricOperation{
		OperationID:    operationID,
		CallerService:  "control-plane",
		Action:         action,
		ResourceKind:   resourceKind,
		ResourceID:     resourceID,
		AccountID:      accountID,
		WorkspaceID:    workspaceID,
		IdempotencyKey: idempotencyKey,
		RequestHash:    requestHash,
		StartedAt:      now,
	}
}

func (s *Service) recordOperation(ctx context.Context, base FabricOperation, status string, resource any, operationErr error) error {
	now := s.now()
	operation := base
	operation.ID = fabricID("fop", firstNonEmpty(base.OperationID, base.ResourceID)+"_"+status, now)
	operation.Status = status
	operation.CreatedAt = now
	if status != "started" {
		operation.FinishedAt = now
	}
	if operationErr != nil {
		operation.ErrorCode = errorCode(operationErr)
	}
	fillOperationResource(&operation, resource)
	return s.operations.Append(ctx, operation)
}

func fillOperationResource(operation *FabricOperation, resource any) {
	launchBinding := operation.RedactedProviderPayload[launchStageBindingPayloadKey]
	providerBinding := operation.RedactedProviderPayload[providerMutationBindingPayloadKey]
	providerState := operation.RedactedProviderPayload[providerMutationStatePayloadKey]
	providerReplayEpoch := operation.RedactedProviderPayload[providerMutationReplayEpochPayloadKey]
	providerChildResourceID := operation.ResourceID
	switch value := resource.(type) {
	case ComputeAllocation:
		operation.ResourceID = firstNonEmpty(value.ID, operation.ResourceID)
		operation.AccountID = firstNonEmpty(value.AccountID, operation.AccountID)
		operation.WorkspaceID = firstNonEmpty(value.WorkspaceID, operation.WorkspaceID)
		operation.Provider = firstNonEmpty(value.Provider, operation.Provider)
		operation.ProviderRequestID = firstNonEmpty(value.ProviderRequestID, operation.ProviderRequestID)
		operation.RedactedProviderPayload = map[string]any{"resource": value, "providerResourceId": value.ProviderResourceID, "nodeName": value.NodeName, "instanceId": firstNonEmpty(value.CVMInstanceID, value.InstanceID), "costTags": value.CostTags}
	case StorageVolume:
		operation.ResourceID = firstNonEmpty(value.ID, operation.ResourceID)
		operation.AccountID = firstNonEmpty(value.AccountID, operation.AccountID)
		operation.WorkspaceID = firstNonEmpty(value.WorkspaceID, operation.WorkspaceID)
		operation.Provider = firstNonEmpty(value.Provider, operation.Provider)
		operation.ProviderRequestID = firstNonEmpty(value.ProviderRequestID, operation.ProviderRequestID)
		operation.RedactedProviderPayload = map[string]any{"resource": value, "providerResourceId": value.ProviderResourceID, "storageClass": value.StorageClass, "sizeGb": value.SizeGB, "costTags": value.CostTags}
	case StorageSnapshot:
		operation.ResourceID = firstNonEmpty(value.ID, operation.ResourceID)
		operation.AccountID = firstNonEmpty(value.AccountID, operation.AccountID)
		operation.WorkspaceID = firstNonEmpty(value.WorkspaceID, operation.WorkspaceID)
		operation.Provider = firstNonEmpty(value.Provider, operation.Provider)
		operation.ProviderRequestID = firstNonEmpty(value.ProviderRequestID, operation.ProviderRequestID)
		operation.RedactedProviderPayload = map[string]any{"resource": value, "providerSnapshotRef": value.ProviderSnapshotRef, "volumeId": value.VolumeID, "snapshotClass": value.SnapshotClass}
	case StorageAttachment:
		operation.ResourceID = firstNonEmpty(value.ID, operation.ResourceID)
		operation.WorkspaceID = firstNonEmpty(value.WorkspaceID, operation.WorkspaceID)
		operation.Provider = firstNonEmpty(value.Provider, operation.Provider)
		operation.ProviderRequestID = firstNonEmpty(value.ProviderRequestID, operation.ProviderRequestID)
		operation.RedactedProviderPayload = map[string]any{"resource": value, "providerAttachmentId": value.ProviderAttachmentID, "computeId": value.ComputeID, "volumeId": value.VolumeID, "costTags": value.CostTags}
	case WorkspaceRuntime:
		redacted := value
		credentialConfigured := value.Access.CredentialStatus == "configured" || value.Access.Password != ""
		if redacted.Access.Password != "" {
			redacted.Access.Password = ""
			redacted.Access.CredentialStatus = firstNonEmpty(redacted.Access.CredentialStatus, "configured")
		}
		operation.ResourceID = firstNonEmpty(value.WorkspaceID, operation.ResourceID)
		operation.WorkspaceID = firstNonEmpty(value.WorkspaceID, operation.WorkspaceID)
		operation.ProviderRequestID = firstNonEmpty(value.ProviderRequestID, operation.ProviderRequestID)
		operation.RedactedProviderPayload = map[string]any{"resource": redacted, "serviceName": value.ServiceName, "ready": value.Ready, "credentialConfigured": credentialConfigured, "credentialVersion": value.Access.CredentialVersion, "secretRef": value.Access.SecretRef, "costTags": value.CostTags}
	case GatewaySecret:
		operation.ResourceID = firstNonEmpty(value.SecretRef, operation.ResourceID)
		operation.RedactedProviderPayload = map[string]any{"resource": value}
	case Job:
		redacted := value
		redacted.LeaseToken = ""
		redacted.leaseTokenHash = ""
		operation.ResourceID = value.JobID
		operation.WorkspaceID = value.WorkspaceID
		operation.ProviderRequestID = firstNonEmpty(operation.ProviderRequestID, value.JobID)
		operation.RedactedProviderPayload = map[string]any{"resource": redacted, "leaseTokenHash": value.leaseTokenHash}
	}
	if launchBinding != nil {
		operation.RedactedProviderPayload[launchStageBindingPayloadKey] = launchBinding
	}
	if providerBinding != nil {
		operation.ResourceID = providerChildResourceID
		operation.RedactedProviderPayload[providerMutationBindingPayloadKey] = providerBinding
	}
	if providerState != nil {
		operation.RedactedProviderPayload[providerMutationStatePayloadKey] = providerState
	}
	if providerReplayEpoch != nil {
		operation.RedactedProviderPayload[providerMutationReplayEpochPayloadKey] = providerReplayEpoch
	}
}

func operationStatus(err error) string {
	if err != nil {
		return "failed"
	}
	return "succeeded"
}

func errorCode(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	if text == "" {
		return "provider_error"
	}
	return strings.Fields(text)[0]
}
func hashInput(input any) string {
	data, err := json.Marshal(input)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func stableSuffix(values ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(values, ":")))
	return hex.EncodeToString(sum[:])
}

func fabricID(prefix string, owner string, now time.Time) string {
	return fmt.Sprintf("%s_%s_%d", prefix, owner, now.UnixNano())
}

func providerRequestID(prefix string, key string) string {
	if key == "" {
		key = "no-idempotency-key"
	}
	return fmt.Sprintf("%s_%s", prefix, key)
}
