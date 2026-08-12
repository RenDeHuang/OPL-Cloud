package fabric

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

type tencentWorkspaceLaunchState struct {
	Compute     *ComputeAllocation            `json:"compute,omitempty"`
	ComputePlan *ComputeAllocationPreparation `json:"computePlan,omitempty"`
	Ownership   *MachineOwnership             `json:"ownership,omitempty"`
	Storage     *StorageVolume                `json:"storage,omitempty"`
	Attachment  *StorageAttachment            `json:"attachment,omitempty"`
	Secret      *GatewaySecret                `json:"secret,omitempty"`
	Runtime     *WorkspaceRuntime             `json:"runtime,omitempty"`
}

func encodeTencentWorkspaceLaunchState(state tencentWorkspaceLaunchState) (json.RawMessage, error) {
	body, err := json.Marshal(state)
	return body, err
}

func decodeTencentWorkspaceLaunchState(record workspaceLaunchStageRecord) (tencentWorkspaceLaunchState, error) {
	var state tencentWorkspaceLaunchState
	if len(record.ProviderState) == 0 || json.Unmarshal(record.ProviderState, &state) != nil {
		return state, ErrLaunchStageBindingConflict
	}
	return state, nil
}

func (p *TencentProvider) EnsureWorkspaceLaunchStage(ctx context.Context, request WorkspaceLaunchProviderRequest) (WorkspaceLaunchProviderResult, error) {
	input, binding := request.Input, request.Input.Binding
	resources := input.Resources
	state := tencentWorkspaceLaunchState{}
	switch binding.Stage {
	case "ensure_compute_allocation":
		computeID := firstNonEmpty(resources.ComputeAllocationID, workspaceLaunchComputeID(binding))
		pool, err := configuredPackageNodePool(input.PackageID)
		if err != nil {
			return WorkspaceLaunchProviderResult{}, err
		}
		if journal := providerMutationJournalFromContext(ctx); journal != nil {
			ownership, ownershipErr := journal.operations.MachineOwnership(ctx, computeID)
			if ownershipErr == nil && (ownership.ResourceID != computeID || ownership.AccountID != binding.AccountID ||
				ownership.WorkspaceID != binding.WorkspaceID || ownership.PackageID != input.PackageID || ownership.NodePoolID != pool.NodePoolID) {
				return WorkspaceLaunchProviderResult{}, ErrLaunchStageBindingConflict
			}
			if ownershipErr != nil && !errors.Is(ownershipErr, ErrMachineOwnershipNotFound) {
				return WorkspaceLaunchProviderResult{}, ownershipErr
			}
		}
		allocation := ComputeAllocation{
			ID: computeID, OperationID: binding.FabricOperationID, AccountID: binding.AccountID, WorkspaceID: binding.WorkspaceID,
			PackageID: input.PackageID, NodePoolID: pool.NodePoolID, Status: "provisioning", Provider: p.Descriptor().Name,
			ProviderRequestID: providerRequestID("compute", binding.IdempotencyKey),
		}
		prepared, err := p.PrepareComputeAllocation(ctx, ComputeAllocationInput{
			ID: computeID, AccountID: binding.AccountID, WorkspaceID: binding.WorkspaceID, PackageID: input.PackageID, NodePoolID: pool.NodePoolID,
		})
		if err != nil {
			return WorkspaceLaunchProviderResult{}, err
		}
		allocation, err = p.CreateComputeAllocation(ctx, ComputeAllocationExecution{Allocation: allocation, Plan: prepared})
		if errors.Is(err, ErrComputeAllocationPending) {
			return WorkspaceLaunchProviderResult{}, ErrWorkspaceLaunchPending
		}
		if err != nil || p.ValidateComputeAllocation(allocation, prepared) != nil {
			return WorkspaceLaunchProviderResult{}, firstNonNil(err, ErrWorkspaceLaunchUnavailable)
		}
		ownership, err := p.ensureWorkspaceLaunchComputeOwnership(ctx, allocation, prepared)
		if err != nil {
			return WorkspaceLaunchProviderResult{}, err
		}
		allocation, err = p.DiscoverComputeAllocation(ctx, allocation, prepared)
		if err != nil || p.ValidateComputeAllocation(allocation, prepared) != nil || !isReadyResourceStatus(allocation.Status) {
			return WorkspaceLaunchProviderResult{}, firstNonNil(err, ErrWorkspaceLaunchUnavailable)
		}
		state.Compute, state.ComputePlan, state.Ownership = &allocation, &prepared, &ownership
		resources.ComputeAllocationID, resources.ComputeBindingRef = allocation.ID, binding.FabricOperationID
	case "storage":
		computeState, err := decodeTencentWorkspaceLaunchState(request.Prior["ensure_compute_allocation"])
		if err != nil || computeState.Compute == nil {
			return WorkspaceLaunchProviderResult{}, ErrLaunchStageBindingConflict
		}
		storageID := firstNonEmpty(resources.StorageID, workspaceLaunchStorageID(binding))
		volume, err := p.CreateStorageVolume(ctx, StorageVolumeInput{
			ID: storageID, AccountID: binding.AccountID, WorkspaceID: binding.WorkspaceID, ComputeID: computeState.Compute.ID,
			Zone: computeState.Compute.Zone, SizeGB: input.SizeGB, IdempotencyKey: binding.IdempotencyKey, OperationID: binding.FabricOperationID,
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
		computeState, computeErr := decodeTencentWorkspaceLaunchState(request.Prior["ensure_compute_allocation"])
		storageState, storageErr := decodeTencentWorkspaceLaunchState(request.Prior["storage"])
		if computeErr != nil || storageErr != nil || computeState.Compute == nil || storageState.Storage == nil {
			return WorkspaceLaunchProviderResult{}, ErrLaunchStageBindingConflict
		}
		attachment, err := p.CreateStorageAttachment(ctx, StorageAttachmentInput{
			WorkspaceID: binding.WorkspaceID, ComputeID: computeState.Compute.ID, VolumeID: storageState.Storage.ID,
			IdempotencyKey: binding.IdempotencyKey, OperationID: binding.FabricOperationID,
		}, *computeState.Compute, *storageState.Storage)
		if err != nil || attachment.ID != workspaceLaunchAttachmentID(binding) {
			return WorkspaceLaunchProviderResult{}, firstNonNil(err, ErrWorkspaceLaunchUnavailable)
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
		computeState, computeErr := decodeTencentWorkspaceLaunchState(request.Prior["ensure_compute_allocation"])
		storageState, storageErr := decodeTencentWorkspaceLaunchState(request.Prior["storage"])
		attachmentState, attachmentErr := decodeTencentWorkspaceLaunchState(request.Prior["attachment"])
		secretState, secretErr := decodeTencentWorkspaceLaunchState(request.Prior["secret"])
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
		if err != nil || !runtime.Ready || runtime.Access.Username == "" || runtime.Access.CredentialStatus == "" || runtime.Access.CredentialVersion == "" || runtime.Access.SecretRef == "" {
			return WorkspaceLaunchProviderResult{}, firstNonNil(err, ErrWorkspaceLaunchUnavailable)
		}
		state.Runtime = &runtime
		applyWorkspaceLaunchRuntimeResources(&resources, runtime, binding.FabricOperationID)
	default:
		return WorkspaceLaunchProviderResult{}, ErrWorkspaceLaunchInputInvalid
	}
	providerState, err := encodeTencentWorkspaceLaunchState(state)
	return WorkspaceLaunchProviderResult{Resources: resources, ProviderState: providerState}, err
}
func (p *TencentProvider) ReadWorkspaceLaunchStage(ctx context.Context, request WorkspaceLaunchProviderRequest) (WorkspaceLaunchProviderResult, error) {
	input, binding := request.Input, request.Input.Binding
	resources := input.Resources
	state, stateErr := decodeTencentWorkspaceLaunchState(request.Current)
	switch binding.Stage {
	case "ensure_compute_allocation":
		if stateErr != nil || state.Compute == nil || state.ComputePlan == nil || state.Ownership == nil {
			var err error
			state, err = p.tencentWorkspaceLaunchComputeStateFromMutation(ctx, binding, input.PackageID)
			if err != nil {
				return WorkspaceLaunchProviderResult{}, ErrWorkspaceLaunchPending
			}
		}
		readback, err := p.DiscoverComputeAllocation(ctx, *state.Compute, *state.ComputePlan)
		if err != nil || p.ValidateComputeAllocation(readback, *state.ComputePlan) != nil || !isReadyResourceStatus(readback.Status) ||
			p.readComputeMachineOwnership(ctx, readback, *state.ComputePlan, *state.Ownership, true) != nil {
			return WorkspaceLaunchProviderResult{}, firstNonNil(err, ErrWorkspaceLaunchPending)
		}
		state.Compute = &readback
		resources.ComputeAllocationID, resources.ComputeBindingRef = readback.ID, binding.FabricOperationID
	case "storage":
		if stateErr != nil || state.Storage == nil {
			volume, err := p.tencentWorkspaceLaunchStorageFromMutation(ctx, binding)
			if err != nil {
				return WorkspaceLaunchProviderResult{}, ErrWorkspaceLaunchPending
			}
			state.Storage = &volume
		}
		readback, err := p.ReadStorageVolume(ctx, *state.Storage)
		if err != nil || !isReadyResourceStatus(readback.Status) {
			return WorkspaceLaunchProviderResult{}, firstNonNil(err, ErrWorkspaceLaunchPending)
		}
		state.Storage = &readback
		resources.StorageID, resources.StorageBindingRef = readback.ID, binding.FabricOperationID
	case "attachment":
		computeState, computeErr := decodeTencentWorkspaceLaunchState(request.Prior["ensure_compute_allocation"])
		storageState, storageErr := decodeTencentWorkspaceLaunchState(request.Prior["storage"])
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
		if err != nil || !readback.Ready || readback.OperationID != binding.FabricOperationID || readback.ImageID != input.WorkspaceImageDigest ||
			readback.Access.Username == "" || readback.Access.CredentialStatus == "" || readback.Access.CredentialVersion == "" || readback.Access.SecretRef == "" {
			return WorkspaceLaunchProviderResult{}, firstNonNil(err, ErrWorkspaceLaunchPending)
		}
		state.Runtime = &readback
		applyWorkspaceLaunchRuntimeResources(&resources, readback, binding.FabricOperationID)
	default:
		return WorkspaceLaunchProviderResult{}, ErrWorkspaceLaunchInputInvalid
	}
	providerState, err := encodeTencentWorkspaceLaunchState(state)
	return WorkspaceLaunchProviderResult{Resources: resources, ProviderState: providerState}, err
}

func (p *TencentProvider) ensureWorkspaceLaunchComputeOwnership(ctx context.Context, allocation ComputeAllocation, prepared ComputeAllocationPreparation) (MachineOwnership, error) {
	journal := providerMutationJournalFromContext(ctx)
	if journal == nil || allocation.ID == "" || allocation.AccountID == "" || allocation.WorkspaceID == "" || allocation.MachineName == "" ||
		allocation.NodeName == "" || firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID) == "" {
		return MachineOwnership{}, ErrLaunchStageBindingConflict
	}
	instanceID := firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID)
	requested := MachineOwnership{
		ID: "owner_" + stableSuffix(allocation.ID, allocation.MachineName)[:16], ResourceID: allocation.ID, AccountID: allocation.AccountID,
		WorkspaceID: allocation.WorkspaceID, PackageID: allocation.PackageID, NodePoolID: allocation.NodePoolID,
		MachineID: allocation.MachineName, InstanceID: instanceID, NodeName: allocation.NodeName, Status: "claimed",
		ProviderRequestID: allocation.ProviderRequestID, ClaimedAt: journal.now(),
	}
	ownership, _, err := journal.operations.ClaimMachine(ctx, requested)
	if err != nil {
		return MachineOwnership{}, err
	}
	err = p.convergeComputeMachineOwnership(ctx, allocation, prepared, ownership)
	if err != nil {
		ownership.Status = "quarantined"
		_ = journal.operations.SaveMachineOwnership(ctx, ownership)
		return ownership, err
	}
	ownership.Status = "active"
	if err := journal.operations.SaveMachineOwnership(ctx, ownership); err != nil {
		return ownership, err
	}
	return ownership, nil
}

func (p *TencentProvider) tencentWorkspaceLaunchComputeStateFromMutation(ctx context.Context, binding WorkspaceLaunchStageBinding, packageID string) (tencentWorkspaceLaunchState, error) {
	journal := providerMutationJournalFromContext(ctx)
	if journal == nil {
		return tencentWorkspaceLaunchState{}, ErrLaunchStageBindingConflict
	}
	computeID := workspaceLaunchComputeID(binding)
	ownership, err := journal.operations.MachineOwnership(ctx, computeID)
	if err != nil {
		return tencentWorkspaceLaunchState{}, err
	}
	if ownership.ResourceID != computeID || ownership.AccountID != binding.AccountID || ownership.WorkspaceID != binding.WorkspaceID ||
		ownership.PackageID != packageID || ownership.NodePoolID == "" {
		return tencentWorkspaceLaunchState{}, ErrLaunchStageBindingConflict
	}
	operationID := providerMutationOperationID(binding, "tencent_compute_allocation_create", "compute_allocation", computeID, ownership.NodePoolID)
	operation, err := journal.operations.Get(ctx, operationID)
	if errors.Is(err, ErrOperationNotFound) {
		return tencentWorkspaceLaunchState{}, ErrLaunchStageBindingConflict
	}
	if err != nil {
		return tencentWorkspaceLaunchState{}, err
	}
	child, ok := decodeProviderMutationBinding(operation)
	if !ok || child.Parent != binding || child.Action != "tencent_compute_allocation_create" || child.ResourceKind != "compute_allocation" ||
		child.ResourceID != computeID || child.ExpectedResourceBinding != ownership.NodePoolID {
		return tencentWorkspaceLaunchState{}, ErrLaunchStageBindingConflict
	}
	var mutationState tencentComputeMutationState
	if !decodeProviderMutationState(operation, &mutationState) || mutationState.Allocation.ID != computeID ||
		mutationState.Allocation.AccountID != binding.AccountID || mutationState.Allocation.WorkspaceID != binding.WorkspaceID ||
		mutationState.Allocation.PackageID != packageID || mutationState.Allocation.NodePoolID != ownership.NodePoolID ||
		mutationState.Plan.PoolID != packagePlan(packageID).ID || mutationState.Plan.PackageID != packageID || mutationState.Plan.NodePoolID != ownership.NodePoolID {
		return tencentWorkspaceLaunchState{}, ErrLaunchStageBindingConflict
	}
	allocation := mutationState.Allocation
	if !decodeOperationResource(operation, &allocation) {
		return tencentWorkspaceLaunchState{}, ErrWorkspaceLaunchPending
	}
	if allocation.ID != computeID || allocation.AccountID != binding.AccountID || allocation.WorkspaceID != binding.WorkspaceID ||
		allocation.PackageID != packageID || allocation.PoolID != mutationState.Plan.PoolID || allocation.NodePoolID != ownership.NodePoolID ||
		allocation.MachineName != ownership.MachineID || allocation.ProviderResourceID != ownership.InstanceID ||
		allocation.InstanceID != ownership.InstanceID || allocation.CVMInstanceID != ownership.InstanceID ||
		allocation.NodeName != ownership.NodeName || allocation.PrivateIP == "" {
		return tencentWorkspaceLaunchState{}, ErrLaunchStageBindingConflict
	}
	return tencentWorkspaceLaunchState{Compute: &allocation, ComputePlan: &mutationState.Plan, Ownership: &ownership}, nil
}

func (p *TencentProvider) tencentWorkspaceLaunchStorageFromMutation(ctx context.Context, binding WorkspaceLaunchStageBinding) (StorageVolume, error) {
	journal := providerMutationJournalFromContext(ctx)
	if journal == nil {
		return StorageVolume{}, ErrLaunchStageBindingConflict
	}
	storageID := workspaceLaunchStorageID(binding)
	operationID := providerMutationOperationID(binding, "tencent_cbs_create", "storage_volume", storageID, "")
	operation, err := journal.operations.Get(ctx, operationID)
	if err != nil {
		return StorageVolume{}, err
	}
	var volume StorageVolume
	if !decodeOperationResource(operation, &volume) || volume.ID != storageID {
		return StorageVolume{}, ErrWorkspaceLaunchPending
	}
	return volume, nil
}
