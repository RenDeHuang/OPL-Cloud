package fabric

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type localDockerWorkspaceLaunchState struct {
	Compute     *ComputeAllocation            `json:"compute,omitempty"`
	ComputePlan *ComputeAllocationPreparation `json:"computePlan,omitempty"`
	Storage     *StorageVolume                `json:"storage,omitempty"`
	Attachment  *StorageAttachment            `json:"attachment,omitempty"`
	Secret      *GatewaySecret                `json:"secret,omitempty"`
	Runtime     *WorkspaceRuntime             `json:"runtime,omitempty"`
}

func encodeLocalDockerWorkspaceLaunchState(state localDockerWorkspaceLaunchState) (json.RawMessage, error) {
	body, err := json.Marshal(state)
	return body, err
}

func decodeLocalDockerWorkspaceLaunchState(record workspaceLaunchStageRecord) (localDockerWorkspaceLaunchState, error) {
	var state localDockerWorkspaceLaunchState
	if len(record.ProviderState) == 0 || json.Unmarshal(record.ProviderState, &state) != nil {
		return state, ErrLaunchStageBindingConflict
	}
	return state, nil
}

func localDockerWorkspaceLaunchResources(input WorkspaceLaunchStageInput) WorkspaceLaunchResources {
	return input.Resources
}

func (p *LocalDockerProvider) EnsureWorkspaceLaunchStage(ctx context.Context, request WorkspaceLaunchProviderRequest) (WorkspaceLaunchProviderResult, error) {
	input, binding := request.Input, request.Input.Binding
	resources := localDockerWorkspaceLaunchResources(input)
	state := localDockerWorkspaceLaunchState{}
	switch binding.Stage {
	case "ensure_compute_allocation":
		computeID := firstNonEmpty(resources.ComputeAllocationID, workspaceLaunchComputeID(binding))
		poolID := p.Descriptor().DefaultComputePoolIDs[input.PackageID]
		allocation := ComputeAllocation{
			ID: computeID, OperationID: binding.FabricOperationID, AccountID: binding.AccountID, WorkspaceID: binding.WorkspaceID,
			PackageID: input.PackageID, Status: "provisioning", Provider: p.Descriptor().Name,
		}
		prepared, err := p.PrepareComputeAllocation(ctx, ComputeAllocationInput{
			ID: computeID, AccountID: binding.AccountID, WorkspaceID: binding.WorkspaceID, PackageID: input.PackageID, NodePoolID: poolID,
		})
		if err != nil {
			return WorkspaceLaunchProviderResult{}, err
		}
		allocation.NodePoolID = prepared.NodePoolID
		allocation, err = p.CreateComputeAllocation(ctx, ComputeAllocationExecution{Allocation: allocation, Plan: prepared})
		if err != nil {
			return WorkspaceLaunchProviderResult{}, err
		}
		allocation, err = p.ReadComputeAllocation(ctx, allocation)
		if err != nil || p.ValidateComputeAllocation(allocation, prepared) != nil || !isReadyResourceStatus(allocation.Status) {
			return WorkspaceLaunchProviderResult{}, firstNonNil(err, ErrWorkspaceLaunchUnavailable)
		}
		state.Compute, state.ComputePlan = &allocation, &prepared
		resources.ComputeAllocationID, resources.ComputeBindingRef = allocation.ID, binding.FabricOperationID
	case "storage":
		prior, err := decodeLocalDockerWorkspaceLaunchState(request.Prior["ensure_compute_allocation"])
		if err != nil || prior.Compute == nil {
			return WorkspaceLaunchProviderResult{}, ErrLaunchStageBindingConflict
		}
		storageID := firstNonEmpty(resources.StorageID, workspaceLaunchStorageID(binding))
		volume, err := p.CreateStorageVolume(ctx, StorageVolumeInput{
			ID: storageID, AccountID: binding.AccountID, WorkspaceID: binding.WorkspaceID, ComputeID: prior.Compute.ID,
			Zone: prior.Compute.Zone, SizeGB: input.SizeGB, IdempotencyKey: binding.IdempotencyKey, OperationID: binding.FabricOperationID,
		})
		if err != nil {
			return WorkspaceLaunchProviderResult{}, err
		}
		volume, err = p.ReadStorageVolume(ctx, volume)
		if err != nil || !isReadyResourceStatus(volume.Status) {
			return WorkspaceLaunchProviderResult{}, firstNonNil(err, ErrWorkspaceLaunchUnavailable)
		}
		state.Storage = &volume
		resources.StorageID, resources.StorageBindingRef = volume.ID, binding.FabricOperationID
	case "attachment":
		computeState, computeErr := decodeLocalDockerWorkspaceLaunchState(request.Prior["ensure_compute_allocation"])
		storageState, storageErr := decodeLocalDockerWorkspaceLaunchState(request.Prior["storage"])
		if computeErr != nil || storageErr != nil || computeState.Compute == nil || storageState.Storage == nil {
			return WorkspaceLaunchProviderResult{}, ErrLaunchStageBindingConflict
		}
		attachment, err := p.CreateStorageAttachment(ctx, StorageAttachmentInput{
			WorkspaceID: binding.WorkspaceID, ComputeID: computeState.Compute.ID, VolumeID: storageState.Storage.ID,
			IdempotencyKey: binding.IdempotencyKey, OperationID: binding.FabricOperationID,
		}, *computeState.Compute, *storageState.Storage)
		if err != nil {
			return WorkspaceLaunchProviderResult{}, err
		}
		if attachment.ID != workspaceLaunchAttachmentID(binding) {
			return WorkspaceLaunchProviderResult{}, ErrWorkspaceLaunchUnavailable
		}
		state.Attachment = &attachment
		resources.AttachmentID, resources.AttachmentBindingRef = attachment.ID, binding.FabricOperationID
	case "secret":
		credential := input.GatewayCredential
		if credential == nil || credential.KeyID != request.Current.GatewayKeyID || credential.KeyID <= 0 || strings.TrimSpace(credential.Value) == "" {
			return WorkspaceLaunchProviderResult{}, ErrWorkspaceLaunchInputInvalid
		}
		secret, err := p.UpsertGatewaySecret(ctx, GatewaySecretInput{
			AccountID: binding.AccountID, WorkspaceID: binding.WorkspaceID, WorkspaceAPIKeyID: credential.KeyID,
			Fingerprint: resources.GatewaySecretFingerprint, GatewayAPIKey: credential.Value, IdempotencyKey: binding.IdempotencyKey,
		})
		if err != nil {
			return WorkspaceLaunchProviderResult{}, err
		}
		state.Secret = &secret
		resources.GatewaySecretRef, resources.GatewaySecretVersion = secret.SecretRef, secret.Version
		resources.GatewaySecretFingerprint, resources.SecretBindingRef = secret.Fingerprint, binding.FabricOperationID
	case "runtime":
		computeState, computeErr := decodeLocalDockerWorkspaceLaunchState(request.Prior["ensure_compute_allocation"])
		storageState, storageErr := decodeLocalDockerWorkspaceLaunchState(request.Prior["storage"])
		attachmentState, attachmentErr := decodeLocalDockerWorkspaceLaunchState(request.Prior["attachment"])
		secretState, secretErr := decodeLocalDockerWorkspaceLaunchState(request.Prior["secret"])
		if computeErr != nil || storageErr != nil || attachmentErr != nil || secretErr != nil || computeState.Compute == nil ||
			storageState.Storage == nil || attachmentState.Attachment == nil || secretState.Secret == nil {
			return WorkspaceLaunchProviderResult{}, ErrLaunchStageBindingConflict
		}
		runtime, err := p.CreateWorkspaceRuntime(ctx, WorkspaceRuntimeInput{
			WorkspaceID: binding.WorkspaceID, ComputeID: computeState.Compute.ID, VolumeID: storageState.Storage.ID,
			AttachmentID: attachmentState.Attachment.ID, AttachmentOperationID: attachmentState.Attachment.OperationID,
			RuntimeOperationID: binding.FabricOperationID, ImageID: input.WorkspaceImageDigest, GatewaySecretRef: secretState.Secret.SecretRef,
			IdempotencyKey: binding.IdempotencyKey, OperationID: binding.FabricOperationID,
		}, *computeState.Compute, *storageState.Storage)
		if err != nil {
			return WorkspaceLaunchProviderResult{}, err
		}
		runtime.Access = RuntimeAccess{
			Username: "opl", CredentialStatus: "configured", CredentialVersion: secretState.Secret.Version, SecretRef: secretState.Secret.SecretRef,
		}
		state.Runtime = &runtime
		applyWorkspaceLaunchRuntimeResources(&resources, runtime, binding.FabricOperationID)
	default:
		return WorkspaceLaunchProviderResult{}, ErrWorkspaceLaunchInputInvalid
	}
	providerState, err := encodeLocalDockerWorkspaceLaunchState(state)
	return WorkspaceLaunchProviderResult{Resources: resources, ProviderState: providerState}, err
}

func (p *LocalDockerProvider) ReadWorkspaceLaunchStage(ctx context.Context, request WorkspaceLaunchProviderRequest) (WorkspaceLaunchProviderResult, error) {
	input, binding := request.Input, request.Input.Binding
	resources := input.Resources
	state, stateErr := decodeLocalDockerWorkspaceLaunchState(request.Current)
	switch binding.Stage {
	case "ensure_compute_allocation":
		allocation := ComputeAllocation{ID: firstNonEmpty(resources.ComputeAllocationID, workspaceLaunchComputeID(binding)), OperationID: binding.FabricOperationID, AccountID: binding.AccountID, WorkspaceID: binding.WorkspaceID, PackageID: input.PackageID}
		if stateErr == nil && state.Compute != nil {
			allocation = *state.Compute
		}
		readback, err := p.ReadComputeAllocation(ctx, allocation)
		if err != nil || !isReadyResourceStatus(readback.Status) {
			return WorkspaceLaunchProviderResult{}, firstNonNil(err, ErrWorkspaceLaunchPending)
		}
		state.Compute = &readback
		resources.ComputeAllocationID, resources.ComputeBindingRef = readback.ID, binding.FabricOperationID
	case "storage":
		volume := StorageVolume{ID: firstNonEmpty(resources.StorageID, workspaceLaunchStorageID(binding)), AccountID: binding.AccountID, WorkspaceID: binding.WorkspaceID}
		if stateErr == nil && state.Storage != nil {
			volume = *state.Storage
		}
		readback, err := p.ReadStorageVolume(ctx, volume)
		if err != nil || !isReadyResourceStatus(readback.Status) {
			return WorkspaceLaunchProviderResult{}, firstNonNil(err, ErrWorkspaceLaunchPending)
		}
		state.Storage = &readback
		resources.StorageID, resources.StorageBindingRef = readback.ID, binding.FabricOperationID
	case "attachment":
		computeState, computeErr := decodeLocalDockerWorkspaceLaunchState(request.Prior["ensure_compute_allocation"])
		storageState, storageErr := decodeLocalDockerWorkspaceLaunchState(request.Prior["storage"])
		if computeErr != nil || storageErr != nil || computeState.Compute == nil || storageState.Storage == nil {
			return WorkspaceLaunchProviderResult{}, ErrLaunchStageBindingConflict
		}
		attachment := StorageAttachment{ID: workspaceLaunchAttachmentID(binding), OperationID: binding.FabricOperationID, WorkspaceID: binding.WorkspaceID, ComputeID: computeState.Compute.ID, VolumeID: storageState.Storage.ID}
		if stateErr == nil && state.Attachment != nil {
			attachment = *state.Attachment
		}
		readback, err := p.ReadStorageAttachment(ctx, attachment, *computeState.Compute, *storageState.Storage)
		if err != nil || readback.Status != "attached" || readback.ID != workspaceLaunchAttachmentID(binding) {
			return WorkspaceLaunchProviderResult{}, firstNonNil(err, ErrWorkspaceLaunchPending)
		}
		state.Attachment = &readback
		resources.AttachmentID, resources.AttachmentBindingRef = readback.ID, binding.FabricOperationID
	case "secret":
		fingerprint := resources.GatewaySecretFingerprint
		readback, err := p.ReadGatewaySecretByDigest(ctx, GatewaySecretReadbackInput{
			AccountID: binding.AccountID, WorkspaceID: binding.WorkspaceID, WorkspaceAPIKeyID: request.Current.GatewayKeyID,
			SecretRef: gatewaySecretName(binding.WorkspaceID), Fingerprint: fingerprint, KeyDigest: strings.TrimPrefix(fingerprint, "sha256:"),
		})
		if err != nil {
			return WorkspaceLaunchProviderResult{}, ErrWorkspaceLaunchPending
		}
		state.Secret = &readback
		resources.GatewaySecretRef, resources.GatewaySecretVersion = readback.SecretRef, readback.Version
		resources.GatewaySecretFingerprint, resources.SecretBindingRef = readback.Fingerprint, binding.FabricOperationID
	case "runtime":
		readback, err := p.WorkspaceRuntimeStatus(ctx, binding.WorkspaceID)
		if err != nil || !readback.Ready || readback.ID != localRuntimeID(binding.WorkspaceID) || readback.OperationID != binding.FabricOperationID || readback.ImageID != input.WorkspaceImageDigest {
			return WorkspaceLaunchProviderResult{}, firstNonNil(err, ErrWorkspaceLaunchPending)
		}
		secretState, secretErr := decodeLocalDockerWorkspaceLaunchState(request.Prior["secret"])
		if secretErr != nil || secretState.Secret == nil {
			return WorkspaceLaunchProviderResult{}, ErrLaunchStageBindingConflict
		}
		readback.Access = RuntimeAccess{Username: "opl", CredentialStatus: "configured", CredentialVersion: secretState.Secret.Version, SecretRef: secretState.Secret.SecretRef}
		state.Runtime = &readback
		applyWorkspaceLaunchRuntimeResources(&resources, readback, binding.FabricOperationID)
	default:
		return WorkspaceLaunchProviderResult{}, ErrWorkspaceLaunchInputInvalid
	}
	providerState, err := encodeLocalDockerWorkspaceLaunchState(state)
	if err != nil {
		return WorkspaceLaunchProviderResult{}, fmt.Errorf("local_docker_workspace_launch_state_invalid: %w", err)
	}
	return WorkspaceLaunchProviderResult{Resources: resources, ProviderState: providerState}, nil
}

func applyWorkspaceLaunchRuntimeResources(resources *WorkspaceLaunchResources, runtime WorkspaceRuntime, bindingRef string) {
	resources.RuntimeID, resources.RuntimeServiceName = runtime.ID, runtime.ServiceName
	resources.RuntimeUsername, resources.RuntimeURL = runtime.Access.Username, runtime.URL
	resources.RuntimeCredentialStatus, resources.RuntimeCredentialVersion = runtime.Access.CredentialStatus, runtime.Access.CredentialVersion
	resources.RuntimeCredentialSecretRef, resources.RuntimeBindingRef = runtime.Access.SecretRef, bindingRef
}
