package fabric

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		poolID := p.Descriptor().DefaultComputePoolIDs[input.PackageID]
		allocation := ComputeAllocation{
			ID: computeID, OperationID: binding.FabricOperationID, AccountID: binding.AccountID, WorkspaceID: binding.WorkspaceID,
			PackageID: input.PackageID, NodePoolID: poolID, Status: "provisioning", Provider: p.Descriptor().Name,
			ProviderRequestID: providerRequestID("compute", binding.IdempotencyKey),
		}
		prepared, err := p.PrepareComputeAllocation(ctx, ComputeAllocationInput{
			ID: computeID, AccountID: binding.AccountID, WorkspaceID: binding.WorkspaceID, PackageID: input.PackageID, NodePoolID: poolID,
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
			p.readWorkspaceLaunchComputeOwnership(ctx, readback, *state.ComputePlan, *state.Ownership, true) != nil {
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
	machine := ProviderMachine{
		MachineID: allocation.MachineName, InstanceID: firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID), NodeName: allocation.NodeName,
		PrivateIP: allocation.PrivateIP, PublicIP: allocation.PublicIP, InstanceType: allocation.InstanceType, Zone: allocation.Zone,
		ChargeType: allocation.ChargeType, RenewFlag: allocation.RenewFlag, Deadline: allocation.Deadline, Ready: true,
	}
	requested := MachineOwnership{
		ID: "owner_" + stableSuffix(allocation.ID, allocation.MachineName)[:16], ResourceID: allocation.ID, AccountID: allocation.AccountID,
		WorkspaceID: allocation.WorkspaceID, PackageID: allocation.PackageID, NodePoolID: allocation.NodePoolID,
		MachineID: machine.MachineID, InstanceID: machine.InstanceID, NodeName: machine.NodeName, Status: "claimed",
		ProviderRequestID: allocation.ProviderRequestID, ClaimedAt: journal.now(),
	}
	ownership, _, err := journal.operations.ClaimMachine(ctx, requested)
	if err != nil {
		return MachineOwnership{}, err
	}
	cvmAttempt, err := beginProviderMutation(ctx, "tencent_cvm_ownership_tag", "compute_binding", ownership.ResourceID, machine.InstanceID)
	if err == nil {
		if cvmAttempt == nil || cvmAttempt.Fresh {
			err = p.TagComputeMachineCVM(ctx, machine, ownership)
		} else {
			err = p.readWorkspaceLaunchComputeOwnership(ctx, allocation, prepared, ownership, false)
		}
	}
	if err != nil {
		_ = cvmAttempt.complete(ctx, ownership.ProviderRequestID, ownership, err)
		ownership.Status = "quarantined"
		_ = journal.operations.SaveMachineOwnership(ctx, ownership)
		return ownership, err
	}
	if err := cvmAttempt.complete(ctx, ownership.ProviderRequestID, ownership, nil); err != nil {
		return ownership, err
	}
	nodeAttempt, err := beginProviderMutation(ctx, "tencent_kubernetes_node_claim", "compute_binding", ownership.ResourceID, machine.NodeName)
	if err == nil {
		if nodeAttempt == nil || nodeAttempt.Fresh {
			err = p.ClaimComputeNode(ctx, allocation, ownership)
		} else {
			err = p.readWorkspaceLaunchComputeOwnership(ctx, allocation, prepared, ownership, true)
		}
	}
	if err != nil {
		_ = nodeAttempt.complete(ctx, ownership.ProviderRequestID, ownership, err)
		ownership.Status = "quarantined"
		_ = journal.operations.SaveMachineOwnership(ctx, ownership)
		return ownership, err
	}
	if err := nodeAttempt.complete(ctx, ownership.ProviderRequestID, ownership, nil); err != nil {
		return ownership, err
	}
	ownership.Status = "active"
	if err := journal.operations.SaveMachineOwnership(ctx, ownership); err != nil {
		return ownership, err
	}
	return ownership, nil
}

func (p *TencentProvider) readWorkspaceLaunchComputeOwnership(ctx context.Context, allocation ComputeAllocation, prepared ComputeAllocationPreparation, ownership MachineOwnership, requireNode bool) error {
	proof, err := p.ProveComputeClaimRecovery(ctx, allocation, prepared, ownership)
	if err != nil || proof.CVMOwnershipState != "target_owned" || requireNode && proof.NodeOwnershipState != "target_owned" {
		return firstNonNil(err, fmt.Errorf("compute_machine_ownership_readback_mismatch"))
	}
	return nil
}

func (p *TencentProvider) tencentWorkspaceLaunchComputeStateFromMutation(ctx context.Context, binding WorkspaceLaunchStageBinding, packageID string) (tencentWorkspaceLaunchState, error) {
	journal := providerMutationJournalFromContext(ctx)
	if journal == nil {
		return tencentWorkspaceLaunchState{}, ErrLaunchStageBindingConflict
	}
	computeID := workspaceLaunchComputeID(binding)
	nodePoolID := p.Descriptor().DefaultComputePoolIDs[packageID]
	operationID := providerMutationOperationID(binding, "tencent_compute_allocation_create", "compute_allocation", computeID, nodePoolID)
	operation, err := journal.operations.Get(ctx, operationID)
	if err != nil {
		return tencentWorkspaceLaunchState{}, err
	}
	var mutationState tencentComputeMutationState
	if !decodeProviderMutationState(operation, &mutationState) {
		return tencentWorkspaceLaunchState{}, ErrLaunchStageBindingConflict
	}
	allocation := mutationState.Allocation
	_ = decodeOperationResource(operation, &allocation)
	ownership, err := journal.operations.MachineOwnership(ctx, computeID)
	if err != nil {
		return tencentWorkspaceLaunchState{}, err
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
