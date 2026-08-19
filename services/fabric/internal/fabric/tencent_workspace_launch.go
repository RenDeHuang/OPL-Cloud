package fabric

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
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
	plan, planErr := decodeTencentWorkspacePlanEnvelope(request.ProviderPlan, input.PackageID, input.SizeGB)
	if planErr != nil {
		return WorkspaceLaunchProviderResult{}, planErr
	}
	ctx = withTencentWorkspacePlan(ctx, plan)
	resources := input.Resources
	state := tencentWorkspaceLaunchState{}
	var err error
	switch binding.Stage {
	case "ensure_compute_allocation":
		computeID := firstNonEmpty(resources.ComputeAllocationID, workspaceLaunchComputeID(binding))
		pool := packageNodePoolConfig{NodePoolID: plan.NodePoolID, MaxReplicas: plan.MaxReplicas}
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
		prepared := ComputeAllocationPreparation{}
		if recovered, recoverErr := p.tencentWorkspaceLaunchComputeStateFromMutation(ctx, binding, input.PackageID); recoverErr == nil && recovered.Compute != nil && recovered.ComputePlan != nil {
			allocation, prepared = *recovered.Compute, *recovered.ComputePlan
		} else if !errors.Is(recoverErr, ErrOperationNotFound) {
			return WorkspaceLaunchProviderResult{}, recoverErr
		} else {
			prepared, err = p.PrepareComputeAllocation(ctx, ComputeAllocationInput{
				ID: computeID, AccountID: binding.AccountID, WorkspaceID: binding.WorkspaceID, PackageID: input.PackageID, NodePoolID: pool.NodePoolID,
			})
			if err != nil {
				return WorkspaceLaunchProviderResult{}, err
			}
		}
		allocation, err = p.CreateComputeAllocation(ctx, ComputeAllocationExecution{Allocation: allocation, Plan: prepared})
		if errors.Is(err, ErrComputeAllocationPending) {
			return WorkspaceLaunchProviderResult{}, ErrWorkspaceLaunchPending
		}
		if err != nil || p.ValidateComputeAllocation(allocation, prepared) != nil || (prepared.Zone != "" && allocation.Zone != prepared.Zone) {
			return WorkspaceLaunchProviderResult{}, firstNonNil(err, ErrWorkspaceLaunchUnavailable)
		}
		ownership, err := p.ensureWorkspaceLaunchComputeOwnership(ctx, allocation, prepared)
		if err != nil {
			return WorkspaceLaunchProviderResult{}, err
		}
		allocation, err = p.DiscoverComputeAllocation(ctx, allocation, prepared)
		if err != nil || p.ValidateComputeAllocation(allocation, prepared) != nil || (prepared.Zone != "" && allocation.Zone != prepared.Zone) || !isReadyResourceStatus(allocation.Status) {
			return WorkspaceLaunchProviderResult{}, firstNonNil(err, ErrWorkspaceLaunchUnavailable)
		}
		state.Compute, state.ComputePlan, state.Ownership = &allocation, &prepared, &ownership
		resources.ComputeAllocationID, resources.ComputeBindingRef = allocation.ID, binding.FabricOperationID
	case "storage":
		computeState, err := decodeTencentWorkspaceLaunchState(request.Prior["ensure_compute_allocation"])
		if err != nil || computeState.Compute == nil {
			return WorkspaceLaunchProviderResult{}, ErrLaunchStageBindingConflict
		}
		if computeState.Compute.Zone != plan.Zone {
			return WorkspaceLaunchProviderResult{}, ErrLaunchStageBindingConflict
		}
		storageID := firstNonEmpty(resources.StorageID, workspaceLaunchStorageID(binding))
		volume, err := p.CreateStorageVolume(ctx, StorageVolumeInput{
			ID: storageID, AccountID: binding.AccountID, WorkspaceID: binding.WorkspaceID, ComputeID: computeState.Compute.ID,
			Zone: computeState.Compute.Zone, SizeGB: plan.Storage.SizeGB, IdempotencyKey: binding.IdempotencyKey, OperationID: binding.FabricOperationID,
		})
		if err != nil {
			return WorkspaceLaunchProviderResult{}, err
		}
		volume, err = p.ReadStorageVolume(ctx, volume)
		if err != nil || !isReadyResourceStatus(volume.Status) {
			return WorkspaceLaunchProviderResult{}, firstNonNil(err, ErrWorkspaceLaunchUnavailable)
		}
		volume, err = p.ReadStaticStorageBinding(ctx, volume)
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
		if credential == nil || credential.KeyID != request.Current.GatewayKeyID || credential.KeyID <= 0 {
			return WorkspaceLaunchProviderResult{}, ErrWorkspaceLaunchInputInvalid
		}
		var secret GatewaySecret
		var err error
		if strings.TrimSpace(credential.Value) == "" {
			secret, err = p.ReadGatewaySecretByDigest(ctx, GatewaySecretReadbackInput{
				AccountID: binding.AccountID, WorkspaceID: binding.WorkspaceID, WorkspaceAPIKeyID: credential.KeyID,
				SecretRef: gatewaySecretName(binding.WorkspaceID), Fingerprint: resources.GatewaySecretFingerprint,
				KeyDigest: strings.TrimPrefix(resources.GatewaySecretFingerprint, "sha256:"),
			})
		} else {
			secret, err = p.UpsertGatewaySecret(ctx, GatewaySecretInput{
				AccountID: binding.AccountID, WorkspaceID: binding.WorkspaceID, WorkspaceAPIKeyID: credential.KeyID,
				Fingerprint: resources.GatewaySecretFingerprint, GatewayAPIKey: credential.Value, IdempotencyKey: binding.IdempotencyKey,
			})
		}
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
		secretRecord := request.Prior["secret"]
		secretState, secretErr := decodeTencentWorkspaceLaunchState(secretRecord)
		if computeErr != nil || storageErr != nil || attachmentErr != nil || secretErr != nil || computeState.Compute == nil ||
			storageState.Storage == nil || attachmentState.Attachment == nil || secretState.Secret == nil || secretRecord.GatewayKeyID <= 0 {
			return WorkspaceLaunchProviderResult{}, ErrLaunchStageBindingConflict
		}
		runtimeInput := WorkspaceRuntimeInput{
			WorkspaceID: binding.WorkspaceID, ComputeID: computeState.Compute.ID, VolumeID: storageState.Storage.ID,
			AttachmentID: attachmentState.Attachment.ID, AttachmentOperationID: attachmentState.Attachment.OperationID,
			RuntimeOperationID: binding.FabricOperationID, ImageID: input.WorkspaceImageDigest, GatewaySecretRef: secretState.Secret.SecretRef,
			IdempotencyKey: binding.IdempotencyKey, OperationID: binding.FabricOperationID,
		}
		runtime, err := p.createWorkspaceRuntime(ctx, runtimeInput, *computeState.Compute, *storageState.Storage, tencentWorkspaceRuntimeGatewayBinding{
			WorkspaceAPIKeyID: secretRecord.GatewayKeyID, SecretRef: secretState.Secret.SecretRef, Fingerprint: secretState.Secret.Fingerprint,
		})
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
	plan, planErr := decodeTencentWorkspacePlanEnvelope(request.ProviderPlan, input.PackageID, input.SizeGB)
	if planErr != nil {
		return WorkspaceLaunchProviderResult{}, planErr
	}
	ctx = withTencentWorkspacePlan(ctx, plan)
	resources := input.Resources
	state, stateErr := decodeTencentWorkspaceLaunchState(request.Current)
	switch binding.Stage {
	case "ensure_compute_allocation":
		if stateErr != nil || state.Compute == nil || state.ComputePlan == nil || state.Ownership == nil {
			var err error
			state, err = p.tencentWorkspaceLaunchComputeStateFromMutation(ctx, binding, input.PackageID)
			if err != nil {
				return WorkspaceLaunchProviderResult{}, err
			}
		}
		readback, err := p.DiscoverComputeAllocation(ctx, *state.Compute, *state.ComputePlan)
		if err != nil {
			return WorkspaceLaunchProviderResult{}, err
		}
		if p.ValidateComputeAllocation(readback, *state.ComputePlan) != nil || (state.ComputePlan.Zone != "" && readback.Zone != state.ComputePlan.Zone) || !isReadyResourceStatus(readback.Status) {
			return WorkspaceLaunchProviderResult{}, ErrWorkspaceLaunchPending
		}
		if state.Ownership == nil {
			expected, ownershipErr := workspaceLaunchComputeOwnership(readback)
			if ownershipErr != nil {
				return WorkspaceLaunchProviderResult{}, ownershipErr
			}
			proof, ownershipErr := p.ProveComputeClaimRecovery(ctx, readback, *state.ComputePlan, expected)
			if ownershipErr != nil || (proof.CVMOwnershipState != "recoverable" && proof.CVMOwnershipState != "target_owned") ||
				(proof.NodeOwnershipState != "unallocated" && proof.NodeOwnershipState != "target_owned") {
				return WorkspaceLaunchProviderResult{}, firstNonNil(ownershipErr, ErrLaunchStageBindingConflict)
			}
			return WorkspaceLaunchProviderResult{}, ErrWorkspaceLaunchResourceAbsent
		}
		if p.readComputeMachineOwnership(ctx, readback, *state.ComputePlan, *state.Ownership, true) != nil {
			return WorkspaceLaunchProviderResult{}, ErrLaunchStageBindingConflict
		}
		state.Compute = &readback
		resources.ComputeAllocationID, resources.ComputeBindingRef = readback.ID, binding.FabricOperationID
	case "storage":
		computeState, computeErr := decodeTencentWorkspaceLaunchState(request.Prior["ensure_compute_allocation"])
		if computeErr != nil || computeState.Compute == nil || computeState.Compute.Zone != plan.Zone {
			return WorkspaceLaunchProviderResult{}, ErrLaunchStageBindingConflict
		}
		storageInput := StorageVolumeInput{
			ID: workspaceLaunchStorageID(binding), AccountID: binding.AccountID, WorkspaceID: binding.WorkspaceID, ComputeID: computeState.Compute.ID,
			SizeGB: plan.Storage.SizeGB, Zone: plan.Zone, IdempotencyKey: binding.IdempotencyKey, OperationID: binding.FabricOperationID,
		}
		if stateErr != nil || state.Storage == nil {
			volume, err := p.tencentWorkspaceLaunchStorageFromMutation(ctx, binding, storageInput)
			if err != nil {
				return WorkspaceLaunchProviderResult{}, err
			}
			state.Storage = &volume
		}
		readback, err := p.ReadCBSVolume(ctx, storageInput, *state.Storage)
		if err != nil {
			return WorkspaceLaunchProviderResult{}, err
		}
		if !isReadyResourceStatus(readback.Status) {
			return WorkspaceLaunchProviderResult{}, ErrWorkspaceLaunchPending
		}
		readback, err = p.ReadStaticStorageBinding(ctx, readback)
		if err != nil {
			return WorkspaceLaunchProviderResult{}, err
		}
		if !isReadyResourceStatus(readback.Status) {
			return WorkspaceLaunchProviderResult{}, ErrWorkspaceLaunchPending
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
			return WorkspaceLaunchProviderResult{}, err
		}
		state.Secret = &readback
		resources.GatewaySecretRef, resources.GatewaySecretVersion = readback.SecretRef, readback.Version
		resources.GatewaySecretFingerprint, resources.SecretBindingRef = readback.Fingerprint, binding.FabricOperationID
	case "runtime":
		computeState, computeErr := decodeTencentWorkspaceLaunchState(request.Prior["ensure_compute_allocation"])
		storageState, storageErr := decodeTencentWorkspaceLaunchState(request.Prior["storage"])
		attachmentState, attachmentErr := decodeTencentWorkspaceLaunchState(request.Prior["attachment"])
		secretRecord := request.Prior["secret"]
		secretState, secretErr := decodeTencentWorkspaceLaunchState(secretRecord)
		if computeErr != nil || storageErr != nil || attachmentErr != nil || secretErr != nil || computeState.Compute == nil ||
			storageState.Storage == nil || attachmentState.Attachment == nil || secretState.Secret == nil || secretRecord.GatewayKeyID <= 0 {
			return WorkspaceLaunchProviderResult{}, ErrLaunchStageBindingConflict
		}
		runtimeInput := WorkspaceRuntimeInput{
			WorkspaceID: binding.WorkspaceID, ComputeID: computeState.Compute.ID, VolumeID: storageState.Storage.ID,
			AttachmentID: attachmentState.Attachment.ID, AttachmentOperationID: attachmentState.Attachment.OperationID,
			RuntimeOperationID: binding.FabricOperationID, ImageID: input.WorkspaceImageDigest, GatewaySecretRef: secretState.Secret.SecretRef,
			IdempotencyKey: binding.IdempotencyKey, OperationID: binding.FabricOperationID,
		}
		runtimeID := "rt_" + stableSuffix(binding.WorkspaceID, binding.FabricOperationID)[:18]
		serviceName := firstNonEmpty(computeState.Compute.ServiceName, k8sName(computeState.Compute.ID))
		readback, err := p.readWorkspaceRuntime(ctx, runtimeInput, runtimeID, serviceName,
			oplCostTags(binding.AccountID, binding.WorkspaceID, runtimeID, binding.FabricOperationID), tencentWorkspaceRuntimeGatewayBinding{
				WorkspaceAPIKeyID: secretRecord.GatewayKeyID, SecretRef: secretState.Secret.SecretRef, Fingerprint: secretState.Secret.Fingerprint,
			})
		if err != nil || !readback.Ready ||
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
	if journal == nil {
		return MachineOwnership{}, ErrLaunchStageBindingConflict
	}
	requested, err := workspaceLaunchComputeOwnership(allocation)
	if err != nil {
		return MachineOwnership{}, err
	}
	requested.ClaimedAt = journal.now()
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

func workspaceLaunchComputeOwnership(allocation ComputeAllocation) (MachineOwnership, error) {
	instanceID := firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID)
	if allocation.ID == "" || allocation.AccountID == "" || allocation.WorkspaceID == "" || allocation.PackageID == "" || allocation.NodePoolID == "" ||
		allocation.MachineName == "" || allocation.NodeName == "" || instanceID == "" {
		return MachineOwnership{}, ErrLaunchStageBindingConflict
	}
	return MachineOwnership{
		ID: "owner_" + stableSuffix(allocation.ID, allocation.MachineName)[:16], ResourceID: allocation.ID, AccountID: allocation.AccountID,
		WorkspaceID: allocation.WorkspaceID, PackageID: allocation.PackageID, NodePoolID: allocation.NodePoolID,
		MachineID: allocation.MachineName, InstanceID: instanceID, NodeName: allocation.NodeName, Status: "claimed",
		ProviderRequestID: allocation.ProviderRequestID,
	}, nil
}

func (p *TencentProvider) tencentWorkspaceLaunchComputeStateFromMutation(ctx context.Context, binding WorkspaceLaunchStageBinding, packageID string) (tencentWorkspaceLaunchState, error) {
	journal := providerMutationJournalFromContext(ctx)
	if journal == nil {
		return tencentWorkspaceLaunchState{}, ErrLaunchStageBindingConflict
	}
	computeID := workspaceLaunchComputeID(binding)
	operation, found, err := journal.operations.LatestResourceOperation(ctx, "compute_allocation", computeID)
	if err != nil {
		return tencentWorkspaceLaunchState{}, err
	}
	if !found {
		return tencentWorkspaceLaunchState{}, ErrOperationNotFound
	}
	child, ok := decodeProviderMutationBinding(operation)
	if !ok || child.Parent != binding || child.Action != "tencent_compute_allocation_create" || child.ResourceKind != "compute_allocation" ||
		child.ResourceID != computeID || child.ExpectedResourceBinding == "" {
		return tencentWorkspaceLaunchState{}, ErrLaunchStageBindingConflict
	}
	nodePoolID := child.ExpectedResourceBinding
	ownership, ownershipErr := journal.operations.MachineOwnership(ctx, computeID)
	if ownershipErr == nil {
		if ownership.ResourceID != computeID || ownership.AccountID != binding.AccountID || ownership.WorkspaceID != binding.WorkspaceID ||
			ownership.PackageID != packageID || ownership.NodePoolID != nodePoolID {
			return tencentWorkspaceLaunchState{}, ErrLaunchStageBindingConflict
		}
	} else if !errors.Is(ownershipErr, ErrMachineOwnershipNotFound) {
		return tencentWorkspaceLaunchState{}, ownershipErr
	}
	var mutationState tencentComputeMutationState
	boundPlan, boundPlanErr := p.workspacePlanForContext(ctx, packageID)
	if !decodeProviderMutationState(operation, &mutationState) || mutationState.Allocation.ID != computeID ||
		mutationState.Allocation.AccountID != binding.AccountID || mutationState.Allocation.WorkspaceID != binding.WorkspaceID ||
		mutationState.Allocation.PackageID != packageID || mutationState.Allocation.NodePoolID != nodePoolID ||
		boundPlanErr != nil || mutationState.Plan.PoolID != boundPlan.Compute.ID || mutationState.Plan.PackageID != packageID || mutationState.Plan.NodePoolID != nodePoolID ||
		(mutationState.Plan.Zone != "" && mutationState.Plan.Zone != boundPlan.Zone) {
		return tencentWorkspaceLaunchState{}, ErrLaunchStageBindingConflict
	}
	allocation := mutationState.Allocation
	if !decodeOperationResource(operation, &allocation) {
		return tencentWorkspaceLaunchState{Compute: &allocation, ComputePlan: &mutationState.Plan}, nil
	}
	if ownershipErr != nil {
		return tencentWorkspaceLaunchState{Compute: &allocation, ComputePlan: &mutationState.Plan}, nil
	}
	if allocation.ID != computeID || allocation.AccountID != binding.AccountID || allocation.WorkspaceID != binding.WorkspaceID ||
		allocation.PackageID != packageID || allocation.PoolID != mutationState.Plan.PoolID || allocation.NodePoolID != nodePoolID ||
		allocation.MachineName != ownership.MachineID || allocation.ProviderResourceID != ownership.InstanceID ||
		allocation.InstanceID != ownership.InstanceID || allocation.CVMInstanceID != ownership.InstanceID ||
		allocation.NodeName != ownership.NodeName || allocation.PrivateIP == "" {
		return tencentWorkspaceLaunchState{}, ErrLaunchStageBindingConflict
	}
	return tencentWorkspaceLaunchState{Compute: &allocation, ComputePlan: &mutationState.Plan, Ownership: &ownership}, nil
}

func (p *TencentProvider) tencentWorkspaceLaunchStorageFromMutation(ctx context.Context, binding WorkspaceLaunchStageBinding, input StorageVolumeInput) (StorageVolume, error) {
	journal := providerMutationJournalFromContext(ctx)
	if journal == nil {
		return StorageVolume{}, ErrLaunchStageBindingConflict
	}
	parent, parentOK := decodeLaunchStageBinding(journal.parentOperation)
	provider := p.Descriptor().Name
	if !parentOK || journal.parent != binding || parent != binding || journal.provider != provider || journal.parentOperation.Provider != provider ||
		journal.parentOperation.ID != binding.FabricOperationID || journal.parentOperation.OperationID != binding.FabricOperationID ||
		journal.parentOperation.Action != binding.Action || journal.parentOperation.ResourceKind != "workspace_launch_stage" ||
		journal.parentOperation.ResourceID != binding.FabricOperationID || journal.parentOperation.AccountID != binding.AccountID ||
		journal.parentOperation.WorkspaceID != binding.WorkspaceID || journal.parentOperation.IdempotencyKey != binding.IdempotencyKey ||
		journal.parentOperation.RequestHash != binding.RequestHash {
		return StorageVolume{}, ErrLaunchStageBindingConflict
	}
	plan, planOK := tencentWorkspacePlanFromContext(ctx)
	storageID := workspaceLaunchStorageID(binding)
	if !planOK || input.ID != storageID || input.AccountID != binding.AccountID || input.WorkspaceID != binding.WorkspaceID ||
		input.OperationID != binding.FabricOperationID || input.IdempotencyKey != binding.IdempotencyKey ||
		input.Zone != plan.Zone || input.SizeGB != plan.Storage.SizeGB {
		return StorageVolume{}, ErrLaunchStageBindingConflict
	}
	operationID := providerMutationOperationID(binding, "tencent_cbs_create", "storage_volume", storageID, "")
	operation, err := journal.operations.Get(ctx, operationID)
	if err != nil {
		return StorageVolume{}, err
	}
	child, childOK := decodeProviderMutationBinding(operation)
	expectedChild := providerMutationBinding{
		SchemaVersion: 1, Parent: binding, FabricOperationID: operationID, Action: "tencent_cbs_create",
		ResourceKind: "storage_volume", ResourceID: storageID,
	}
	if !childOK || child != expectedChild || operation.Provider != provider {
		return StorageVolume{}, ErrLaunchStageBindingConflict
	}
	var state tencentCBSCreateMutationState
	if !decodeProviderMutationState(operation, &state) || !state.matches(input) || !state.matchesWorkspacePlan(plan) ||
		state.OperationID != binding.FabricOperationID || state.AccountID != binding.AccountID || state.WorkspaceID != binding.WorkspaceID || state.StorageID != storageID {
		return StorageVolume{}, ErrLaunchStageBindingConflict
	}
	volume := tencentCBSCreateVolume(input, state, time.Time{})
	var persisted StorageVolume
	if decodeOperationResource(operation, &persisted) {
		if !state.matchesVolume(input, persisted) {
			return StorageVolume{}, ErrLaunchStageBindingConflict
		}
		volume = persisted
	} else if operation.Status != "started" {
		return StorageVolume{}, ErrLaunchStageBindingConflict
	}
	return volume, nil
}
