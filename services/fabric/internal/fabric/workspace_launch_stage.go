package fabric

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const WorkspaceLaunchFabricSchemaVersion = 1

const workspaceLaunchPreflightPayloadKey = "workspaceLaunchPreflight"
const workspaceLaunchStageRecordPayloadKey = "workspaceLaunchStageRecord"

var (
	ErrWorkspaceLaunchInputInvalid = errors.New("workspace_launch_input_invalid")
	ErrWorkspaceLaunchUnavailable  = errors.New("workspace_launch_unavailable")
	ErrWorkspaceLaunchPending      = errors.New("workspace_launch_pending")
)

type LegacyWorkspaceLaunchStageIdentity struct {
	Stage                 string `json:"stage"`
	ResourceRef           string `json:"resourceRef"`
	PersistedOperationRef string `json:"persistedOperationRef,omitempty"`
}

type LegacyWorkspaceLaunchStageReadback struct {
	Stage                    string `json:"stage"`
	State                    string `json:"state"`
	OperationRef             string `json:"operationRef,omitempty"`
	IdempotencyIdentity      string `json:"idempotencyIdentity,omitempty"`
	ResourceBindingRef       string `json:"resourceBindingRef,omitempty"`
	AuthoritativeReadbackRef string `json:"authoritativeReadbackRef,omitempty"`
}

type LegacyWorkspaceLaunchBindingInput struct {
	SchemaVersion           int                                  `json:"schemaVersion"`
	LaunchOperationID       string                               `json:"launchOperationId"`
	AccountID               string                               `json:"accountId"`
	WorkspaceID             string                               `json:"workspaceId"`
	RequestHash             string                               `json:"requestHash"`
	PackageID               string                               `json:"packageId"`
	SizeGB                  int                                  `json:"sizeGb"`
	WorkspaceImageDigest    string                               `json:"workspaceImageDigest"`
	WorkspaceAPIKeyID       int64                                `json:"workspaceApiKeyId"`
	WorkspaceKeyFingerprint string                               `json:"workspaceKeyFingerprint"`
	Stages                  []LegacyWorkspaceLaunchStageIdentity `json:"stages"`
}

type LegacyWorkspaceLaunchBindingResult struct {
	SchemaVersion       int                                  `json:"schemaVersion"`
	State               string                               `json:"state"`
	Reason              string                               `json:"reason"`
	LaunchOperationID   string                               `json:"launchOperationId"`
	AccountID           string                               `json:"accountId"`
	WorkspaceID         string                               `json:"workspaceId"`
	ProviderProfileRef  string                               `json:"providerProfileRef,omitempty"`
	PreflightBindingRef string                               `json:"preflightBindingRef,omitempty"`
	Resources           WorkspaceLaunchResources             `json:"resources,omitempty"`
	Stages              []LegacyWorkspaceLaunchStageReadback `json:"stages,omitempty"`
}

type legacyGatewaySecretReadbackProvider interface {
	ReadGatewaySecretByDigest(context.Context, GatewaySecretReadbackInput) (GatewaySecret, error)
}

func validLegacyWorkspaceLaunchStageIdentity(input LegacyWorkspaceLaunchStageIdentity) bool {
	for _, value := range []string{input.Stage, input.ResourceRef} {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return false
		}
	}
	return input.PersistedOperationRef == strings.TrimSpace(input.PersistedOperationRef)
}

func legacyWorkspaceLaunchStageStoreIdentity(stage, resourceRef, workspaceID string) (string, string, string) {
	identity, ok := map[string][2]string{
		"ensure_compute_allocation": {"create_compute_allocation", "compute_allocation"},
		"storage":                   {"create_storage_volume", "storage_volume"},
		"attachment":                {"create_storage_attachment", "storage_attachment"},
		"secret":                    {"upsert_gateway_secret", "gateway_secret"},
		"runtime":                   {"create_workspace_runtime", "workspace_runtime"},
	}[stage]
	if !ok {
		return "", "", ""
	}
	if stage == "runtime" {
		resourceRef = workspaceID
	}
	return identity[0], identity[1], resourceRef
}

// ReadLegacyWorkspaceLaunchBinding is a migration-only, GET-only projection.
// It never claims, appends, or updates an operation and never invokes an
// ensure/mutation provider method.
func (s *Service) ReadLegacyWorkspaceLaunchBinding(ctx context.Context, input LegacyWorkspaceLaunchBindingInput) (LegacyWorkspaceLaunchBindingResult, error) {
	result := LegacyWorkspaceLaunchBindingResult{SchemaVersion: WorkspaceLaunchFabricSchemaVersion, LaunchOperationID: input.LaunchOperationID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID}
	if input.SchemaVersion != 2 || strings.TrimSpace(input.LaunchOperationID) == "" || strings.TrimSpace(input.AccountID) == "" || strings.TrimSpace(input.WorkspaceID) == "" ||
		strings.TrimSpace(input.RequestHash) == "" || strings.TrimSpace(input.PackageID) == "" || input.SizeGB <= 0 || strings.TrimSpace(input.WorkspaceImageDigest) == "" || len(input.Stages) == 0 {
		result.State, result.Reason = "conflict", "legacy_input_invalid"
		return result, nil
	}
	preflight := workspaceLaunchPreflightAdmission{
		SchemaVersion: WorkspaceLaunchFabricSchemaVersion,
		Input: WorkspaceLaunchPreflightInput{
			SchemaVersion: WorkspaceLaunchFabricSchemaVersion, LaunchOperationID: input.LaunchOperationID,
			AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, PackageID: input.PackageID, SizeGB: input.SizeGB,
			WorkspaceImageDigest: input.WorkspaceImageDigest, RequestHash: input.RequestHash,
		},
		ProviderProfileRef: s.provider.Descriptor().Name,
	}
	preflight.BindingRef = "fabric-preflight:" + hashInput(preflight)
	persistedPreflight, err := s.workspaceLaunchPreflight(ctx, preflight.BindingRef)
	if errors.Is(err, ErrLaunchStageBindingNotFound) {
		result.State, result.Reason = "unknown", "legacy_preflight_history_missing"
		return result, nil
	}
	if err != nil || persistedPreflight != preflight {
		result.State, result.Reason = "conflict", "legacy_preflight_identity_mismatch"
		return result, nil
	}
	result.ProviderProfileRef, result.PreflightBindingRef = preflight.ProviderProfileRef, preflight.BindingRef
	byStage := make(map[string]LegacyWorkspaceLaunchStageIdentity, len(input.Stages))
	for index, identity := range input.Stages {
		action, _, _ := legacyWorkspaceLaunchStageStoreIdentity(identity.Stage, identity.ResourceRef, input.WorkspaceID)
		if !validLegacyWorkspaceLaunchStageIdentity(identity) || action == "" || byStage[identity.Stage].Stage != "" ||
			index > 0 && workspaceLaunchRequiredPriorStages(identity.Stage)[len(workspaceLaunchRequiredPriorStages(identity.Stage))-1] != input.Stages[index-1].Stage {
			result.State, result.Reason = "conflict", "legacy_stage_identity_invalid"
			return result, nil
		}
		byStage[identity.Stage] = identity
	}
	resources := WorkspaceLaunchResources{}
	decoded := make(map[string]any)
	readbacks := make(map[string]LegacyWorkspaceLaunchStageReadback, len(byStage))
	finish := func(state, reason string) LegacyWorkspaceLaunchBindingResult {
		result.State, result.Reason, result.Resources = state, reason, resources
		result.Stages = result.Stages[:0]
		for _, identity := range input.Stages {
			result.Stages = append(result.Stages, readbacks[identity.Stage])
		}
		return result
	}
	for _, identity := range input.Stages {
		stage := identity.Stage
		action, resourceKind, historyResourceID := legacyWorkspaceLaunchStageStoreIdentity(stage, identity.ResourceRef, input.WorkspaceID)
		history, err := s.operations.LegacyLaunchOperationHistory(ctx, LegacyLaunchOperationIdentity{
			Action: action, ResourceKind: resourceKind, ResourceID: historyResourceID,
			AccountID: input.AccountID, WorkspaceID: input.WorkspaceID,
		})
		if err != nil {
			return LegacyWorkspaceLaunchBindingResult{}, err
		}
		if len(history) == 0 {
			readbacks[stage] = LegacyWorkspaceLaunchStageReadback{Stage: stage, State: "unknown"}
			return finish("unknown", "legacy_operation_history_missing"), nil
		}
		var logicalID, requestHash, idempotencyKey, providerProfileRef string
		var succeeded *FabricOperation
		for index := range history {
			operation := history[index]
			if operation.Action != action || operation.ResourceKind != resourceKind || operation.ResourceID != historyResourceID || operation.AccountID != input.AccountID || operation.WorkspaceID != input.WorkspaceID ||
				identity.PersistedOperationRef != "" && operation.OperationID != identity.PersistedOperationRef || strings.TrimSpace(operation.Provider) == "" || strings.TrimSpace(operation.OperationID) == "" {
				readbacks[stage] = LegacyWorkspaceLaunchStageReadback{Stage: stage, State: "conflict"}
				result.Stages = append(result.Stages, readbacks[stage])
				result.State, result.Reason = "conflict", "legacy_operation_identity_drift"
				return result, nil
			}
			if logicalID == "" {
				logicalID, requestHash, idempotencyKey, providerProfileRef = operation.OperationID, operation.RequestHash, operation.IdempotencyKey, operation.Provider
			} else if logicalID != operation.OperationID || requestHash != operation.RequestHash || idempotencyKey != operation.IdempotencyKey || providerProfileRef != operation.Provider {
				readbacks[stage] = LegacyWorkspaceLaunchStageReadback{Stage: stage, State: "conflict"}
				result.Stages = append(result.Stages, readbacks[stage])
				result.State, result.Reason = "conflict", "legacy_competing_logical_operation"
				return result, nil
			}
			if operation.Status == "succeeded" {
				if succeeded != nil {
					readbacks[stage] = LegacyWorkspaceLaunchStageReadback{Stage: stage, State: "conflict"}
					result.Stages = append(result.Stages, readbacks[stage])
					result.State, result.Reason = "conflict", "legacy_succeeded_operation_not_unique"
					return result, nil
				}
				candidate := operation
				succeeded = &candidate
			}
		}
		if result.ProviderProfileRef == "" {
			result.ProviderProfileRef = providerProfileRef
		} else if result.ProviderProfileRef != providerProfileRef {
			readbacks[stage] = LegacyWorkspaceLaunchStageReadback{Stage: stage, State: "conflict"}
			return finish("conflict", "legacy_provider_profile_drift"), nil
		}
		if result.ProviderProfileRef != s.provider.Descriptor().Name {
			readbacks[stage] = LegacyWorkspaceLaunchStageReadback{Stage: stage, State: "conflict"}
			return finish("conflict", "legacy_provider_profile_unavailable"), nil
		}
		if succeeded == nil {
			readbacks[stage] = LegacyWorkspaceLaunchStageReadback{Stage: stage, State: "pending", OperationRef: logicalID, IdempotencyIdentity: idempotencyKey}
			result.Stages = append(result.Stages, readbacks[stage])
			continue
		}
		if _, ok := succeeded.RedactedProviderPayload["resource"]; !ok {
			readbacks[stage] = LegacyWorkspaceLaunchStageReadback{Stage: stage, State: "unknown", OperationRef: logicalID, IdempotencyIdentity: idempotencyKey, ResourceBindingRef: succeeded.ID}
			result.Stages = append(result.Stages, readbacks[stage])
			continue
		}
		decoded[stage] = succeeded.RedactedProviderPayload
		readbacks[stage] = LegacyWorkspaceLaunchStageReadback{Stage: stage, OperationRef: succeeded.OperationID, IdempotencyIdentity: succeeded.IdempotencyKey, ResourceBindingRef: succeeded.ID, State: "ready"}
	}
	if _, ok := byStage["ensure_compute_allocation"]; !ok {
		result.State, result.Reason = "absent", "legacy_compute_ref_missing"
		return result, nil
	}
	if computeStage := readbacks["ensure_compute_allocation"]; computeStage.State != "ready" {
		return finish(computeStage.State, "legacy_compute_"+computeStage.State), nil
	}
	compute, ok := decodeLegacyResource[ComputeAllocation](legacyResourcePayload(decoded["ensure_compute_allocation"]))
	if !ok || compute.ID != byStage["ensure_compute_allocation"].ResourceRef || compute.AccountID != input.AccountID || compute.WorkspaceID != input.WorkspaceID || compute.PackageID != input.PackageID {
		result.State, result.Reason = "conflict", "legacy_compute_identity_mismatch"
		return result, nil
	}
	if reader, ok := s.provider.(computeAllocationDiscoveryProvider); ok {
		plan, planOK := decodeLegacyOperationPlan(decoded["ensure_compute_allocation"])
		if !planOK {
			result.State, result.Reason = "unknown", "legacy_compute_plan_unavailable"
			return result, nil
		}
		readback, err := reader.DiscoverComputeAllocation(ctx, compute, plan)
		if err != nil {
			result.State, result.Reason = "unknown", "legacy_compute_readback_unavailable"
			return result, nil
		}
		compute = readback
	} else if reader, ok := s.provider.(computeAllocationReadbackProvider); ok {
		readback, err := reader.ReadComputeAllocation(ctx, compute)
		if err != nil {
			result.State, result.Reason = "unknown", "legacy_compute_readback_unavailable"
			return result, nil
		}
		compute = readback
	} else {
		result.State, result.Reason = "unknown", "legacy_compute_readback_unavailable"
		return result, nil
	}
	if compute.ID != inputStagesResourceID(byStage, "ensure_compute_allocation") || compute.AccountID != input.AccountID || compute.WorkspaceID != input.WorkspaceID || !isReadyResourceStatus(compute.Status) {
		result.State, result.Reason = "conflict", "legacy_compute_readback_mismatch"
		return result, nil
	}
	resources.ComputeAllocationID, resources.ComputeBindingRef = compute.ID, readbacks["ensure_compute_allocation"].ResourceBindingRef
	readback := readbacks["ensure_compute_allocation"]
	readback.AuthoritativeReadbackRef = "fabric-readback:" + hashInput(compute)
	readbacks["ensure_compute_allocation"] = readback

	storagePayload, storagePresent := decoded["storage"]
	if !storagePresent {
		return finish("ready", "legacy_partial_history"), nil
	}
	if stage := readbacks["storage"]; stage.State != "ready" {
		return finish(stage.State, "legacy_storage_"+stage.State), nil
	}
	storage, ok := decodeLegacyResource[StorageVolume](legacyResourcePayload(storagePayload))
	if !ok || storage.ID != byStage["storage"].ResourceRef || storage.AccountID != input.AccountID || storage.WorkspaceID != input.WorkspaceID {
		result.State, result.Reason = "conflict", "legacy_storage_identity_mismatch"
		return result, nil
	}
	reader, ok := s.provider.(storageVolumeStatusReader)
	if !ok {
		result.State, result.Reason = "unknown", "legacy_storage_readback_unavailable"
		return result, nil
	}
	storageReadback, err := reader.ReadStorageVolumeStatus(ctx, storage)
	if err != nil {
		result.State, result.Reason = "unknown", "legacy_storage_readback_unavailable"
		return result, nil
	}
	storage = storageReadback
	if storage.ID != byStage["storage"].ResourceRef || storage.AccountID != input.AccountID || storage.WorkspaceID != input.WorkspaceID || !isReadyResourceStatus(storage.Status) {
		result.State, result.Reason = "conflict", "legacy_storage_readback_mismatch"
		return result, nil
	}
	resources.StorageID, resources.StorageBindingRef = storage.ID, readbacks["storage"].ResourceBindingRef
	readback = readbacks["storage"]
	readback.AuthoritativeReadbackRef = "fabric-readback:" + hashInput(storage)
	readbacks["storage"] = readback

	attachmentPayload, attachmentPresent := decoded["attachment"]
	if !attachmentPresent {
		return finish("ready", "legacy_partial_history"), nil
	}
	if stage := readbacks["attachment"]; stage.State != "ready" {
		return finish(stage.State, "legacy_attachment_"+stage.State), nil
	}
	attachment, ok := decodeLegacyResource[StorageAttachment](legacyResourcePayload(attachmentPayload))
	if !ok || attachment.ID != byStage["attachment"].ResourceRef || attachment.WorkspaceID != input.WorkspaceID || attachment.ComputeID != compute.ID || attachment.VolumeID != storage.ID {
		result.State, result.Reason = "conflict", "legacy_attachment_identity_mismatch"
		return result, nil
	}
	attachmentReader, ok := s.provider.(storageAttachmentReadbackProvider)
	if !ok {
		result.State, result.Reason = "unknown", "legacy_attachment_readback_unavailable"
		return result, nil
	}
	attachmentReadback, err := attachmentReader.ReadStorageAttachment(ctx, attachment, compute, storage)
	if err != nil {
		result.State, result.Reason = "unknown", "legacy_attachment_readback_unavailable"
		return result, nil
	}
	attachment = attachmentReadback
	if attachment.ID != byStage["attachment"].ResourceRef || attachment.WorkspaceID != input.WorkspaceID || attachment.ComputeID != compute.ID || attachment.VolumeID != storage.ID || attachment.Status != "attached" {
		result.State, result.Reason = "conflict", "legacy_attachment_readback_mismatch"
		return result, nil
	}
	resources.AttachmentID, resources.AttachmentBindingRef = attachment.ID, readbacks["attachment"].ResourceBindingRef
	readback = readbacks["attachment"]
	readback.AuthoritativeReadbackRef = "fabric-readback:" + hashInput(attachment)
	readbacks["attachment"] = readback

	secretPayload, secretPresent := decoded["secret"]
	if !secretPresent {
		return finish("ready", "legacy_partial_history"), nil
	}
	if stage := readbacks["secret"]; stage.State != "ready" {
		return finish(stage.State, "legacy_secret_"+stage.State), nil
	}
	secret, ok := decodeLegacyResource[GatewaySecret](legacyResourcePayload(secretPayload))
	if !ok || secret.SecretRef != byStage["secret"].ResourceRef || secret.Fingerprint != input.WorkspaceKeyFingerprint {
		result.State, result.Reason = "conflict", "legacy_secret_identity_mismatch"
		return result, nil
	}
	secretReader, ok := s.provider.(legacyGatewaySecretReadbackProvider)
	if !ok || input.WorkspaceAPIKeyID <= 0 {
		result.State, result.Reason = "unknown", "legacy_secret_readback_unavailable"
		return result, nil
	}
	secretReadback, err := secretReader.ReadGatewaySecretByDigest(ctx, GatewaySecretReadbackInput{AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, WorkspaceAPIKeyID: input.WorkspaceAPIKeyID, SecretRef: secret.SecretRef, Fingerprint: secret.Fingerprint, KeyDigest: strings.TrimPrefix(secret.Fingerprint, "sha256:")})
	if err != nil {
		result.State, result.Reason = "unknown", "legacy_secret_readback_unavailable"
		return result, nil
	}
	secret = secretReadback
	if secret.SecretRef != byStage["secret"].ResourceRef || secret.Fingerprint != input.WorkspaceKeyFingerprint || secret.Version == "" {
		result.State, result.Reason = "conflict", "legacy_secret_readback_mismatch"
		return result, nil
	}
	resources.GatewaySecretRef, resources.GatewaySecretVersion, resources.GatewaySecretFingerprint, resources.SecretBindingRef = secret.SecretRef, secret.Version, secret.Fingerprint, readbacks["secret"].ResourceBindingRef
	readback = readbacks["secret"]
	readback.AuthoritativeReadbackRef = "fabric-readback:" + hashInput(secret)
	readbacks["secret"] = readback

	runtimePayload, runtimePresent := decoded["runtime"]
	if !runtimePresent {
		return finish("ready", "legacy_partial_history"), nil
	}
	if stage := readbacks["runtime"]; stage.State != "ready" {
		return finish(stage.State, "legacy_runtime_"+stage.State), nil
	}
	runtime, ok := decodeLegacyResource[WorkspaceRuntime](legacyResourcePayload(runtimePayload))
	if !ok || runtime.ID != byStage["runtime"].ResourceRef || runtime.WorkspaceID != input.WorkspaceID {
		result.State, result.Reason = "conflict", "legacy_runtime_identity_mismatch"
		return result, nil
	}
	runtimeReadback, err := s.provider.WorkspaceRuntimeStatus(ctx, input.WorkspaceID)
	if err != nil {
		result.State, result.Reason = "unknown", "legacy_runtime_readback_unavailable"
		return result, nil
	}
	runtime = runtimeReadback
	if runtime.ID != byStage["runtime"].ResourceRef || runtime.WorkspaceID != input.WorkspaceID || !runtime.Ready || runtime.URL == "" || runtime.ImageID != input.WorkspaceImageDigest || runtime.Access.SecretRef != secret.SecretRef {
		result.State, result.Reason = "conflict", "legacy_runtime_readback_mismatch"
		return result, nil
	}
	resources.RuntimeID, resources.RuntimeServiceName, resources.RuntimeUsername, resources.RuntimeURL = runtime.ID, runtime.ServiceName, runtime.Access.Username, runtime.URL
	resources.RuntimeCredentialStatus, resources.RuntimeCredentialVersion, resources.RuntimeCredentialSecretRef, resources.RuntimeBindingRef = runtime.Access.CredentialStatus, runtime.Access.CredentialVersion, runtime.Access.SecretRef, readbacks["runtime"].ResourceBindingRef
	readback = readbacks["runtime"]
	readback.AuthoritativeReadbackRef = "fabric-readback:" + hashInput(runtime)
	readbacks["runtime"] = readback
	return finish("ready", "none"), nil
}

func decodeLegacyResource[T any](value any) (T, bool) {
	var out T
	body, err := json.Marshal(value)
	if err != nil || json.Unmarshal(body, &out) != nil {
		return out, false
	}
	return out, true
}

func decodeLegacyOperationPlan(value any) (ComputeAllocationPreparation, bool) {
	body, ok := value.(map[string]any)
	if !ok {
		return ComputeAllocationPreparation{}, false
	}
	planValue, ok := body["allocationPlan"]
	if !ok {
		return ComputeAllocationPreparation{}, false
	}
	return decodeLegacyResource[ComputeAllocationPreparation](planValue)
}

func legacyResourcePayload(value any) any {
	body, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return body["resource"]
}

func inputStagesResourceID(stages map[string]LegacyWorkspaceLaunchStageIdentity, stage string) string {
	return stages[stage].ResourceRef
}

type WorkspaceLaunchPreflightInput struct {
	SchemaVersion        int    `json:"schemaVersion"`
	LaunchOperationID    string `json:"launchOperationId"`
	AccountID            string `json:"accountId"`
	WorkspaceID          string `json:"workspaceId"`
	PackageID            string `json:"packageId"`
	SizeGB               int    `json:"sizeGb"`
	WorkspaceImageDigest string `json:"workspaceImageDigest"`
	RequestHash          string `json:"requestHash"`
}

type WorkspaceLaunchPreflight struct {
	SchemaVersion      int    `json:"schemaVersion"`
	Available          bool   `json:"available"`
	Reason             string `json:"reason"`
	LaunchOperationID  string `json:"launchOperationId"`
	RequestHash        string `json:"requestHash"`
	ProviderProfileRef string `json:"providerProfileRef"`
	BindingRef         string `json:"bindingRef"`
}

type workspaceLaunchPreflightAdmission struct {
	SchemaVersion      int                           `json:"schemaVersion"`
	Input              WorkspaceLaunchPreflightInput `json:"input"`
	ProviderProfileRef string                        `json:"providerProfileRef"`
	BindingRef         string                        `json:"bindingRef"`
}

type WorkspaceLaunchResources struct {
	ComputeAllocationID        string `json:"computeAllocationId,omitempty"`
	ComputeBindingRef          string `json:"computeBindingRef,omitempty"`
	StorageID                  string `json:"storageId,omitempty"`
	StorageBindingRef          string `json:"storageBindingRef,omitempty"`
	AttachmentID               string `json:"attachmentId,omitempty"`
	AttachmentBindingRef       string `json:"attachmentBindingRef,omitempty"`
	GatewaySecretRef           string `json:"gatewaySecretRef,omitempty"`
	GatewaySecretVersion       string `json:"gatewaySecretVersion,omitempty"`
	GatewaySecretFingerprint   string `json:"gatewaySecretFingerprint,omitempty"`
	SecretBindingRef           string `json:"secretBindingRef,omitempty"`
	RuntimeID                  string `json:"runtimeId,omitempty"`
	RuntimeServiceName         string `json:"runtimeServiceName,omitempty"`
	RuntimeUsername            string `json:"runtimeUsername,omitempty"`
	RuntimeURL                 string `json:"runtimeUrl,omitempty"`
	RuntimeCredentialStatus    string `json:"runtimeCredentialStatus,omitempty"`
	RuntimeCredentialVersion   string `json:"runtimeCredentialVersion,omitempty"`
	RuntimeCredentialSecretRef string `json:"runtimeCredentialSecretRef,omitempty"`
	RuntimeBindingRef          string `json:"runtimeBindingRef,omitempty"`
}

type WorkspaceLaunchGatewayCredential struct {
	KeyID int64  `json:"keyId"`
	Value string `json:"value"`
}

type WorkspaceLaunchStageInput struct {
	Binding              WorkspaceLaunchStageBinding       `json:"binding"`
	ProviderProfileRef   string                            `json:"providerProfileRef"`
	PreflightBindingRef  string                            `json:"preflightBindingRef"`
	PackageID            string                            `json:"packageId"`
	SizeGB               int                               `json:"sizeGb"`
	WorkspaceImageDigest string                            `json:"workspaceImageDigest"`
	Resources            WorkspaceLaunchResources          `json:"resources"`
	GatewayCredential    *WorkspaceLaunchGatewayCredential `json:"gatewayCredential,omitempty"`
}

type WorkspaceLaunchStageResult struct {
	SchemaVersion int                         `json:"schemaVersion"`
	State         string                      `json:"state"`
	Reason        string                      `json:"reason"`
	Binding       WorkspaceLaunchStageBinding `json:"binding"`
	Resources     WorkspaceLaunchResources    `json:"resources"`
}

type workspaceLaunchStageRecord struct {
	SchemaVersion       int                      `json:"schemaVersion"`
	ProviderProfileRef  string                   `json:"providerProfileRef"`
	PreflightBindingRef string                   `json:"preflightBindingRef"`
	RequestResources    WorkspaceLaunchResources `json:"requestResources"`
	Resources           WorkspaceLaunchResources `json:"resources"`
	GatewayKeyID        int64                    `json:"gatewayKeyId,omitempty"`
	ProviderState       json.RawMessage          `json:"providerState,omitempty"`
}

type persistedWorkspaceLaunchStageRecord struct {
	Record workspaceLaunchStageRecord `json:"record"`
	Digest string                     `json:"digest"`
}

type WorkspaceLaunchProviderRequest struct {
	Input   WorkspaceLaunchStageInput
	Current workspaceLaunchStageRecord
	Prior   map[string]workspaceLaunchStageRecord
}

type WorkspaceLaunchProviderResult struct {
	Resources     WorkspaceLaunchResources
	ProviderState json.RawMessage
}

type workspaceLaunchProvider interface {
	EnsureWorkspaceLaunchStage(context.Context, WorkspaceLaunchProviderRequest) (WorkspaceLaunchProviderResult, error)
	ReadWorkspaceLaunchStage(context.Context, WorkspaceLaunchProviderRequest) (WorkspaceLaunchProviderResult, error)
}

func validWorkspaceLaunchPreflightInput(input WorkspaceLaunchPreflightInput) bool {
	if input.SchemaVersion != WorkspaceLaunchFabricSchemaVersion || input.SizeGB < 10 || input.SizeGB%10 != 0 {
		return false
	}
	for _, value := range []string{input.LaunchOperationID, input.AccountID, input.WorkspaceID, input.PackageID, input.WorkspaceImageDigest, input.RequestHash} {
		if value == "" || value != strings.TrimSpace(value) {
			return false
		}
	}
	return validWorkspaceLaunchHash(input.RequestHash)
}

func (s *Service) PreflightWorkspaceLaunch(ctx context.Context, input WorkspaceLaunchPreflightInput) (WorkspaceLaunchPreflight, error) {
	if !validWorkspaceLaunchPreflightInput(input) {
		return WorkspaceLaunchPreflight{}, ErrWorkspaceLaunchInputInvalid
	}
	if _, ok := providerPlan(s.provider, input.PackageID); !ok || !s.provider.ValidateWorkspaceImageReference(input.WorkspaceImageDigest) {
		return WorkspaceLaunchPreflight{
			SchemaVersion: 1, Available: false, Reason: "provider_profile_unavailable", LaunchOperationID: input.LaunchOperationID,
			RequestHash: input.RequestHash, ProviderProfileRef: s.provider.Descriptor().Name,
		}, nil
	}
	if _, err := s.provider.Readiness(ctx); err != nil {
		return WorkspaceLaunchPreflight{
			SchemaVersion: 1, Available: false, Reason: "provider_unavailable", LaunchOperationID: input.LaunchOperationID,
			RequestHash: input.RequestHash, ProviderProfileRef: s.provider.Descriptor().Name,
		}, nil
	}
	result := WorkspaceLaunchPreflight{
		SchemaVersion: 1, Available: true, Reason: "none", LaunchOperationID: input.LaunchOperationID,
		RequestHash: input.RequestHash, ProviderProfileRef: s.provider.Descriptor().Name,
	}
	admission := workspaceLaunchPreflightAdmission{
		SchemaVersion: 1, Input: input, ProviderProfileRef: result.ProviderProfileRef,
	}
	admission.BindingRef = "fabric-preflight:" + hashInput(admission)
	result.BindingRef = admission.BindingRef
	if err := s.persistWorkspaceLaunchPreflight(ctx, admission); err != nil {
		return WorkspaceLaunchPreflight{}, err
	}
	return result, nil
}

func (s *Service) persistWorkspaceLaunchPreflight(ctx context.Context, admission workspaceLaunchPreflightAdmission) error {
	now := s.now()
	operation := newOperation(
		"admit_workspace_launch", "workspace_launch_preflight", admission.Input.LaunchOperationID,
		admission.Input.AccountID, admission.Input.WorkspaceID, admission.BindingRef, hashInput(admission), now,
	)
	operation.ID, operation.OperationID = admission.BindingRef, admission.BindingRef
	operation.Provider = admission.ProviderProfileRef
	operation.Status, operation.CreatedAt, operation.FinishedAt = "succeeded", now, now
	operation.RedactedProviderPayload = map[string]any{workspaceLaunchPreflightPayloadKey: admission}
	stored, claimed, err := s.operations.ClaimRuntime(ctx, operation)
	if err != nil {
		return err
	}
	if persisted, ok := decodeWorkspaceLaunchPreflight(stored); !ok || persisted != admission || (!claimed && stored.RequestHash != operation.RequestHash) {
		return ErrLaunchStageBindingConflict
	}
	return nil
}

func decodeWorkspaceLaunchPreflight(operation FabricOperation) (workspaceLaunchPreflightAdmission, bool) {
	value, ok := operation.RedactedProviderPayload[workspaceLaunchPreflightPayloadKey]
	if !ok {
		return workspaceLaunchPreflightAdmission{}, false
	}
	var admission workspaceLaunchPreflightAdmission
	body, err := json.Marshal(value)
	if err != nil || json.Unmarshal(body, &admission) != nil || admission.SchemaVersion != 1 ||
		!validWorkspaceLaunchPreflightInput(admission.Input) || admission.ProviderProfileRef == "" ||
		admission.BindingRef != "fabric-preflight:"+hashInput(workspaceLaunchPreflightAdmission{
			SchemaVersion: admission.SchemaVersion, Input: admission.Input, ProviderProfileRef: admission.ProviderProfileRef,
		}) {
		return workspaceLaunchPreflightAdmission{}, false
	}
	if operation.ID != admission.BindingRef || operation.OperationID != admission.BindingRef || operation.Action != "admit_workspace_launch" ||
		operation.ResourceKind != "workspace_launch_preflight" || operation.ResourceID != admission.Input.LaunchOperationID ||
		operation.AccountID != admission.Input.AccountID || operation.WorkspaceID != admission.Input.WorkspaceID ||
		operation.Provider != admission.ProviderProfileRef || operation.IdempotencyKey != admission.BindingRef ||
		operation.RequestHash != hashInput(admission) || operation.Status != "succeeded" {
		return workspaceLaunchPreflightAdmission{}, false
	}
	return admission, true
}

func (s *Service) workspaceLaunchPreflight(ctx context.Context, ref string) (workspaceLaunchPreflightAdmission, error) {
	operation, err := s.operations.Get(ctx, ref)
	if errors.Is(err, ErrOperationNotFound) {
		return workspaceLaunchPreflightAdmission{}, ErrLaunchStageBindingNotFound
	}
	if err != nil {
		return workspaceLaunchPreflightAdmission{}, err
	}
	admission, ok := decodeWorkspaceLaunchPreflight(operation)
	if !ok {
		return workspaceLaunchPreflightAdmission{}, ErrLaunchStageBindingConflict
	}
	return admission, nil
}

func (s *Service) validateWorkspaceLaunchStageInput(ctx context.Context, input WorkspaceLaunchStageInput) error {
	if !validWorkspaceLaunchStageBinding(input.Binding) || strings.TrimSpace(input.ProviderProfileRef) == "" ||
		strings.TrimSpace(input.PreflightBindingRef) == "" || strings.TrimSpace(input.PackageID) == "" ||
		input.SizeGB < 10 || input.SizeGB%10 != 0 || !s.provider.ValidateWorkspaceImageReference(input.WorkspaceImageDigest) {
		return ErrWorkspaceLaunchInputInvalid
	}
	if _, ok := providerPlan(s.provider, input.PackageID); !ok {
		return ErrWorkspaceLaunchInputInvalid
	}
	admission, err := s.workspaceLaunchPreflight(ctx, input.PreflightBindingRef)
	if err != nil {
		return err
	}
	preflight := admission.Input
	if admission.ProviderProfileRef != input.ProviderProfileRef || preflight.LaunchOperationID != input.Binding.LaunchOperationID ||
		preflight.AccountID != input.Binding.AccountID || preflight.WorkspaceID != input.Binding.WorkspaceID ||
		preflight.PackageID != input.PackageID || preflight.SizeGB != input.SizeGB ||
		preflight.WorkspaceImageDigest != input.WorkspaceImageDigest {
		return ErrLaunchStageBindingConflict
	}
	if admission.ProviderProfileRef != s.provider.Descriptor().Name || input.Binding.RequestHash != workspaceLaunchStageRequestHash(input, preflight.RequestHash) {
		return ErrLaunchStageBindingConflict
	}
	if err := s.validateWorkspaceLaunchExpectedBinding(ctx, input); err != nil {
		return err
	}
	return nil
}

func validWorkspaceLaunchHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func workspaceLaunchStageRequestHash(input WorkspaceLaunchStageInput, launchRequestHash string) string {
	return hashInput(struct {
		LaunchRequestHash string                   `json:"launchRequestHash"`
		Action            string                   `json:"action"`
		PackageID         string                   `json:"packageId"`
		SizeGB            int                      `json:"sizeGb"`
		ImageDigest       string                   `json:"imageDigest"`
		Resources         WorkspaceLaunchResources `json:"resources"`
	}{launchRequestHash, input.Binding.Action, input.PackageID, input.SizeGB, input.WorkspaceImageDigest, input.Resources})
}

func workspaceLaunchCurrentStageBinding(stage string, resources WorkspaceLaunchResources) string {
	return map[string]string{
		"ensure_compute_allocation": resources.ComputeBindingRef,
		"storage":                   resources.StorageBindingRef,
		"attachment":                resources.AttachmentBindingRef,
		"secret":                    resources.SecretBindingRef,
		"runtime":                   resources.RuntimeBindingRef,
	}[stage]
}

func (s *Service) validateWorkspaceLaunchExpectedBinding(ctx context.Context, input WorkspaceLaunchStageInput) error {
	expected := workspaceLaunchCurrentStageBinding(input.Binding.Stage, input.Resources)
	if input.Binding.ExpectedResourceBinding != expected {
		return ErrLaunchStageBindingConflict
	}
	if expected == "" {
		return nil
	}
	operation, err := s.operations.Get(ctx, expected)
	if err != nil {
		return ErrLaunchStageBindingConflict
	}
	persisted, ok := decodeLaunchStageBinding(operation)
	record, recordOK := decodeWorkspaceLaunchStageRecord(operation)
	if !ok || !recordOK || operation.Status != "succeeded" || persisted.LaunchOperationID != input.Binding.LaunchOperationID ||
		persisted.AccountID != input.Binding.AccountID || persisted.WorkspaceID != input.Binding.WorkspaceID ||
		persisted.Stage != input.Binding.Stage || operation.ID != expected || record.ProviderProfileRef != input.ProviderProfileRef ||
		!workspaceLaunchResourcesContain(input.Resources, record.Resources) {
		return ErrLaunchStageBindingConflict
	}
	return nil
}

func workspaceLaunchResourcesContain(actual, expected WorkspaceLaunchResources) bool {
	actualBody, _ := json.Marshal(actual)
	expectedBody, _ := json.Marshal(expected)
	actualFields, expectedFields := map[string]string{}, map[string]string{}
	if json.Unmarshal(actualBody, &actualFields) != nil || json.Unmarshal(expectedBody, &expectedFields) != nil {
		return false
	}
	for field, expectedValue := range expectedFields {
		if expectedValue != "" && actualFields[field] != expectedValue {
			return false
		}
	}
	return true
}

func setWorkspaceLaunchStageRecord(operation *FabricOperation, record workspaceLaunchStageRecord) {
	if operation.RedactedProviderPayload == nil {
		operation.RedactedProviderPayload = map[string]any{}
	}
	operation.RedactedProviderPayload[workspaceLaunchStageRecordPayloadKey] = persistedWorkspaceLaunchStageRecord{
		Record: record, Digest: hashInput(record),
	}
}

func decodeWorkspaceLaunchStageRecord(operation FabricOperation) (workspaceLaunchStageRecord, bool) {
	value, ok := operation.RedactedProviderPayload[workspaceLaunchStageRecordPayloadKey]
	if !ok {
		return workspaceLaunchStageRecord{}, false
	}
	body, err := json.Marshal(value)
	if err != nil {
		return workspaceLaunchStageRecord{}, false
	}
	var persisted persistedWorkspaceLaunchStageRecord
	if json.Unmarshal(body, &persisted) != nil || persisted.Record.SchemaVersion != 1 ||
		persisted.Digest == "" || persisted.Digest != hashInput(persisted.Record) ||
		persisted.Record.ProviderProfileRef == "" || persisted.Record.PreflightBindingRef == "" {
		return workspaceLaunchStageRecord{}, false
	}
	return persisted.Record, true
}

func workspaceLaunchStageCredentialKeyID(input WorkspaceLaunchStageInput) int64 {
	if input.GatewayCredential == nil {
		return 0
	}
	return input.GatewayCredential.KeyID
}

func newWorkspaceLaunchStageOperation(input WorkspaceLaunchStageInput, provider string, now func() time.Time) (FabricOperation, workspaceLaunchStageRecord, error) {
	binding := input.Binding
	record := workspaceLaunchStageRecord{
		SchemaVersion: 1, ProviderProfileRef: provider, PreflightBindingRef: input.PreflightBindingRef,
		RequestResources: input.Resources, Resources: input.Resources, GatewayKeyID: workspaceLaunchStageCredentialKeyID(input),
	}
	operation := newOperation(binding.Action, "workspace_launch_stage", binding.FabricOperationID, binding.AccountID, binding.WorkspaceID, binding.IdempotencyKey, binding.RequestHash, now())
	operation.ID, operation.OperationID = binding.FabricOperationID, binding.FabricOperationID
	operation.Provider, operation.Status, operation.CreatedAt = provider, "started", now()
	setWorkspaceLaunchStageRecord(&operation, record)
	if err := bindLaunchStageOperation(&operation, &binding); err != nil {
		return FabricOperation{}, workspaceLaunchStageRecord{}, err
	}
	return operation, record, nil
}

func workspaceLaunchStageOperationMatches(operation FabricOperation, input WorkspaceLaunchStageInput, provider string) (workspaceLaunchStageRecord, bool) {
	binding, bindingOK := decodeLaunchStageBinding(operation)
	record, recordOK := decodeWorkspaceLaunchStageRecord(operation)
	if !bindingOK || !recordOK || binding != input.Binding || operation.ID != input.Binding.FabricOperationID ||
		operation.OperationID != input.Binding.FabricOperationID || operation.Action != input.Binding.Action ||
		operation.ResourceKind != "workspace_launch_stage" || operation.ResourceID != input.Binding.FabricOperationID ||
		operation.Provider != provider || record.ProviderProfileRef != provider || record.PreflightBindingRef != input.PreflightBindingRef ||
		record.RequestResources != input.Resources {
		return workspaceLaunchStageRecord{}, false
	}
	keyID := workspaceLaunchStageCredentialKeyID(input)
	if input.Binding.Stage == "secret" {
		if record.GatewayKeyID <= 0 || keyID > 0 && record.GatewayKeyID != keyID {
			return workspaceLaunchStageRecord{}, false
		}
	} else if record.GatewayKeyID != 0 {
		return workspaceLaunchStageRecord{}, false
	}
	return record, true
}

func workspaceLaunchRequiredPriorStages(stage string) []string {
	switch stage {
	case "storage":
		return []string{"ensure_compute_allocation"}
	case "attachment":
		return []string{"ensure_compute_allocation", "storage"}
	case "secret":
		return []string{"ensure_compute_allocation", "storage", "attachment"}
	case "runtime":
		return []string{"ensure_compute_allocation", "storage", "attachment", "secret"}
	default:
		return nil
	}
}

func workspaceLaunchStageBindingRef(stage string, resources WorkspaceLaunchResources) string {
	return map[string]string{
		"ensure_compute_allocation": resources.ComputeBindingRef,
		"storage":                   resources.StorageBindingRef,
		"attachment":                resources.AttachmentBindingRef,
		"secret":                    resources.SecretBindingRef,
		"runtime":                   resources.RuntimeBindingRef,
	}[stage]
}

func workspaceLaunchComputeID(binding WorkspaceLaunchStageBinding) string {
	return "ca_" + stableSuffix("create_compute_allocation", binding.IdempotencyKey)[:18]
}

func workspaceLaunchStorageID(binding WorkspaceLaunchStageBinding) string {
	return "vol_" + stableSuffix("create_storage_volume", binding.IdempotencyKey)[:16]
}

func workspaceLaunchAttachmentID(binding WorkspaceLaunchStageBinding) string {
	return "att_" + stableSuffix(binding.IdempotencyKey)[:18]
}

func (s *Service) WorkspaceLaunchProviderRequest(ctx context.Context, input WorkspaceLaunchStageInput, current workspaceLaunchStageRecord) (WorkspaceLaunchProviderRequest, error) {
	request := WorkspaceLaunchProviderRequest{Input: input, Current: current, Prior: map[string]workspaceLaunchStageRecord{}}
	for _, stage := range workspaceLaunchRequiredPriorStages(input.Binding.Stage) {
		ref := workspaceLaunchStageBindingRef(stage, input.Resources)
		if ref == "" {
			return WorkspaceLaunchProviderRequest{}, ErrLaunchStageBindingConflict
		}
		operation, err := s.operations.Get(ctx, ref)
		if err != nil {
			return WorkspaceLaunchProviderRequest{}, ErrLaunchStageBindingConflict
		}
		binding, bindingOK := decodeLaunchStageBinding(operation)
		record, recordOK := decodeWorkspaceLaunchStageRecord(operation)
		if !bindingOK || !recordOK || operation.Status != "succeeded" || operation.ID != ref || binding.Stage != stage ||
			binding.LaunchOperationID != input.Binding.LaunchOperationID || binding.AccountID != input.Binding.AccountID ||
			binding.WorkspaceID != input.Binding.WorkspaceID || record.ProviderProfileRef != input.ProviderProfileRef ||
			workspaceLaunchStageBindingRef(stage, record.Resources) != ref || !workspaceLaunchResourcesContain(input.Resources, record.Resources) {
			return WorkspaceLaunchProviderRequest{}, ErrLaunchStageBindingConflict
		}
		request.Prior[stage] = record
	}
	return request, nil
}

func validWorkspaceLaunchProviderResult(input WorkspaceLaunchStageInput, result WorkspaceLaunchProviderResult) bool {
	if !workspaceLaunchResourcesContain(result.Resources, input.Resources) || workspaceLaunchStageBindingRef(input.Binding.Stage, result.Resources) != input.Binding.FabricOperationID {
		return false
	}
	switch input.Binding.Stage {
	case "ensure_compute_allocation":
		return result.Resources.ComputeAllocationID != ""
	case "storage":
		return result.Resources.StorageID != ""
	case "attachment":
		return result.Resources.AttachmentID != ""
	case "secret":
		return result.Resources.GatewaySecretRef != "" && result.Resources.GatewaySecretVersion != "" &&
			result.Resources.GatewaySecretFingerprint == input.Resources.GatewaySecretFingerprint
	case "runtime":
		return result.Resources.RuntimeID != "" && result.Resources.RuntimeServiceName != "" && result.Resources.RuntimeURL != ""
	default:
		return false
	}
}

func pendingWorkspaceLaunchStageResult(input WorkspaceLaunchStageInput, reason string) WorkspaceLaunchStageResult {
	if reason == "" {
		reason = "operation_pending"
	}
	return WorkspaceLaunchStageResult{SchemaVersion: 1, State: "pending", Reason: reason, Binding: input.Binding, Resources: input.Resources}
}

func (s *Service) persistWorkspaceLaunchStageResult(ctx context.Context, current FabricOperation, record workspaceLaunchStageRecord, result WorkspaceLaunchProviderResult) error {
	next := current
	next.Status, next.ErrorCode, next.Retryable, next.FinishedAt = "succeeded", "", false, s.now()
	record.Resources, record.ProviderState = result.Resources, append(json.RawMessage(nil), result.ProviderState...)
	setWorkspaceLaunchStageRecord(&next, record)
	if current.Status == "started" {
		return s.operations.SaveRuntime(ctx, next)
	}
	converger, ok := s.operations.(runtimeReadbackConverger)
	if !ok {
		return ErrRuntimeOperationNotCurrent
	}
	return converger.ConvergeRuntimeReadback(ctx, current, next)
}

func (s *Service) failWorkspaceLaunchStage(ctx context.Context, current FabricOperation, err error) {
	if current.Status != "started" {
		return
	}
	next := current
	next.Status, next.ErrorCode, next.Retryable, next.FinishedAt = "failed", errorCode(err), false, s.now()
	_ = s.operations.SaveRuntime(ctx, next)
}

func (s *Service) EnsureWorkspaceLaunchStage(ctx context.Context, input WorkspaceLaunchStageInput) (WorkspaceLaunchStageResult, error) {
	if err := s.validateWorkspaceLaunchStageInput(ctx, input); err != nil {
		return WorkspaceLaunchStageResult{}, err
	}
	if input.Binding.Stage == "secret" && (input.GatewayCredential == nil || input.GatewayCredential.KeyID <= 0 || strings.TrimSpace(input.GatewayCredential.Value) == "") {
		return WorkspaceLaunchStageResult{}, ErrWorkspaceLaunchInputInvalid
	}
	stageProvider, ok := s.provider.(workspaceLaunchProvider)
	if !ok {
		return WorkspaceLaunchStageResult{}, ErrWorkspaceLaunchUnavailable
	}
	operation, record, err := newWorkspaceLaunchStageOperation(input, s.provider.Descriptor().Name, s.now)
	if err != nil {
		return WorkspaceLaunchStageResult{}, err
	}
	stored, _, err := s.operations.ClaimRuntime(ctx, operation)
	if err != nil {
		return WorkspaceLaunchStageResult{}, err
	}
	record, ok = workspaceLaunchStageOperationMatches(stored, input, s.provider.Descriptor().Name)
	if !ok {
		return WorkspaceLaunchStageResult{}, ErrLaunchStageBindingConflict
	}
	if stored.Status == "succeeded" {
		return s.readWorkspaceLaunchStage(ctx, input, stored, record)
	}
	request, err := s.WorkspaceLaunchProviderRequest(ctx, input, record)
	if err != nil {
		return WorkspaceLaunchStageResult{}, err
	}
	providerResult, err := stageProvider.EnsureWorkspaceLaunchStage(s.providerMutationContext(ctx, stored), request)
	if errors.Is(err, ErrWorkspaceLaunchPending) {
		return pendingWorkspaceLaunchStageResult(input, stored.ErrorCode), nil
	}
	if err != nil {
		s.failWorkspaceLaunchStage(ctx, stored, err)
		return WorkspaceLaunchStageResult{}, err
	}
	if !validWorkspaceLaunchProviderResult(input, providerResult) {
		err = ErrWorkspaceLaunchUnavailable
		s.failWorkspaceLaunchStage(ctx, stored, err)
		return WorkspaceLaunchStageResult{}, err
	}
	if err := s.persistWorkspaceLaunchStageResult(ctx, stored, record, providerResult); err != nil {
		latest, getErr := s.operations.Get(ctx, input.Binding.FabricOperationID)
		if getErr != nil || latest.Status != "succeeded" {
			return WorkspaceLaunchStageResult{}, err
		}
	}
	return WorkspaceLaunchStageResult{SchemaVersion: 1, State: "ready", Reason: "none", Binding: input.Binding, Resources: providerResult.Resources}, nil
}

func (s *Service) ReadWorkspaceLaunchStage(ctx context.Context, input WorkspaceLaunchStageInput) (WorkspaceLaunchStageResult, error) {
	if err := s.validateWorkspaceLaunchStageInput(ctx, input); err != nil {
		return WorkspaceLaunchStageResult{}, err
	}
	operation, err := s.operations.Get(ctx, input.Binding.FabricOperationID)
	if errors.Is(err, ErrOperationNotFound) {
		return WorkspaceLaunchStageResult{}, ErrLaunchStageBindingNotFound
	}
	if err != nil {
		return WorkspaceLaunchStageResult{}, err
	}
	record, ok := workspaceLaunchStageOperationMatches(operation, input, s.provider.Descriptor().Name)
	if !ok {
		return WorkspaceLaunchStageResult{}, ErrLaunchStageBindingConflict
	}
	return s.readWorkspaceLaunchStage(ctx, input, operation, record)
}

func (s *Service) readWorkspaceLaunchStage(ctx context.Context, input WorkspaceLaunchStageInput, operation FabricOperation, record workspaceLaunchStageRecord) (WorkspaceLaunchStageResult, error) {
	stageProvider, ok := s.provider.(workspaceLaunchProvider)
	if !ok {
		return WorkspaceLaunchStageResult{}, ErrWorkspaceLaunchUnavailable
	}
	request, err := s.WorkspaceLaunchProviderRequest(ctx, input, record)
	if err != nil {
		return WorkspaceLaunchStageResult{}, err
	}
	providerResult, err := stageProvider.ReadWorkspaceLaunchStage(s.providerMutationContext(ctx, operation), request)
	if errors.Is(err, ErrWorkspaceLaunchPending) {
		return pendingWorkspaceLaunchStageResult(input, operation.ErrorCode), nil
	}
	if err != nil {
		return WorkspaceLaunchStageResult{}, err
	}
	if !validWorkspaceLaunchProviderResult(input, providerResult) {
		return WorkspaceLaunchStageResult{}, ErrWorkspaceLaunchUnavailable
	}
	if operation.Status != "succeeded" {
		if err := s.persistWorkspaceLaunchStageResult(ctx, operation, record, providerResult); err != nil {
			return WorkspaceLaunchStageResult{}, err
		}
	} else if !workspaceLaunchResourcesContain(providerResult.Resources, record.Resources) || !workspaceLaunchResourcesContain(record.Resources, providerResult.Resources) {
		return WorkspaceLaunchStageResult{}, ErrLaunchStageBindingConflict
	}
	return WorkspaceLaunchStageResult{SchemaVersion: 1, State: "ready", Reason: "none", Binding: input.Binding, Resources: providerResult.Resources}, nil
}
