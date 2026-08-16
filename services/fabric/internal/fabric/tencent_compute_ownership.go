package fabric

import (
	"context"
	"errors"
	"fmt"
)

func (p *TencentProvider) TagComputeMachine(ctx context.Context, machine ProviderMachine, ownership MachineOwnership) error {
	prepared := ComputeAllocationPreparation{}
	if journal := providerMutationJournalFromContext(ctx); journal != nil {
		var ok bool
		prepared, ok = decodeComputeAllocationPlan(journal.parentOperation)
		if !ok {
			return fmt.Errorf("compute_machine_parent_plan_required")
		}
	}
	return p.convergeComputeMachineOwnership(ctx, computeMachineOwnershipAllocation(machine, ownership, prepared), prepared, ownership)
}

func computeMachineOwnershipAllocation(machine ProviderMachine, ownership MachineOwnership, prepared ComputeAllocationPreparation) ComputeAllocation {
	return ComputeAllocation{
		ID: ownership.ResourceID, AccountID: ownership.AccountID, WorkspaceID: ownership.WorkspaceID,
		PackageID: ownership.PackageID, PoolID: prepared.PoolID, NodePoolID: ownership.NodePoolID,
		MachineName: machine.MachineID, InstanceID: machine.InstanceID, CVMInstanceID: machine.InstanceID,
		NodeName: machine.NodeName, PrivateIP: machine.PrivateIP, PublicIP: machine.PublicIP,
		InstanceType: machine.InstanceType, Zone: machine.Zone, ChargeType: machine.ChargeType,
		RenewFlag: machine.RenewFlag, Deadline: machine.Deadline,
	}
}

func providerMachineFromComputeAllocation(allocation ComputeAllocation) ProviderMachine {
	return ProviderMachine{
		MachineID: allocation.MachineName, InstanceID: firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID), NodeName: allocation.NodeName,
		PrivateIP: allocation.PrivateIP, PublicIP: allocation.PublicIP, InstanceType: allocation.InstanceType, Zone: allocation.Zone,
		ChargeType: allocation.ChargeType, RenewFlag: allocation.RenewFlag, Deadline: allocation.Deadline, Ready: true,
	}
}

func (p *TencentProvider) convergeComputeMachineOwnership(ctx context.Context, allocation ComputeAllocation, prepared ComputeAllocationPreparation, ownership MachineOwnership) error {
	machine := providerMachineFromComputeAllocation(allocation)
	cvmMutation, err := beginProviderMutation(ctx, "tencent_cvm_ownership_tag", "compute_binding", ownership.ResourceID, machine.InstanceID)
	if err != nil {
		return err
	}
	err = p.convergeComputeMachineCVMOwnership(ctx, cvmMutation, allocation, prepared, machine, ownership)
	if err != nil {
		if errors.Is(err, ErrWorkspaceLaunchPending) {
			return err
		}
		_ = cvmMutation.complete(ctx, ownership.ProviderRequestID, ownership, err)
		return err
	}
	if err := cvmMutation.complete(ctx, ownership.ProviderRequestID, ownership, nil); err != nil {
		return err
	}

	nodeMutation, err := beginProviderMutation(ctx, "tencent_kubernetes_node_claim", "compute_binding", ownership.ResourceID, machine.NodeName)
	if err != nil {
		return err
	}
	err = p.convergeComputeNodeOwnership(ctx, nodeMutation, allocation, prepared, ownership)
	if err != nil {
		if errors.Is(err, ErrWorkspaceLaunchPending) {
			return err
		}
		_ = nodeMutation.complete(ctx, ownership.ProviderRequestID, ownership, err)
		return err
	}
	return nodeMutation.complete(ctx, ownership.ProviderRequestID, ownership, nil)
}

func (p *TencentProvider) convergeComputeMachineCVMOwnership(ctx context.Context, attempt *providerMutationAttempt, allocation ComputeAllocation, prepared ComputeAllocationPreparation, machine ProviderMachine, ownership MachineOwnership) error {
	if attempt == nil {
		return p.TagComputeMachineCVM(ctx, machine, ownership)
	}
	proof, err := p.ProveComputeClaimRecovery(ctx, allocation, prepared, ownership)
	if err != nil {
		return err
	}
	if proof.CVMOwnershipState == "target_owned" {
		return nil
	}
	if proof.CVMOwnershipState != "recoverable" {
		return fmt.Errorf("compute_machine_ownership_readback_mismatch")
	}
	if attempt.Fresh {
		return p.TagComputeMachineCVM(ctx, machine, ownership)
	}
	claimed, err := attempt.claimReplay(ctx)
	if err != nil {
		if errors.Is(err, ErrRuntimeOperationNotCurrent) {
			return ErrWorkspaceLaunchPending
		}
		return err
	}
	if !claimed {
		return ErrWorkspaceLaunchPending
	}
	proof, err = p.ProveComputeClaimRecovery(ctx, allocation, prepared, ownership)
	if err != nil {
		return err
	}
	if proof.CVMOwnershipState == "target_owned" {
		return nil
	}
	if proof.CVMOwnershipState != "recoverable" {
		return fmt.Errorf("compute_machine_ownership_readback_mismatch")
	}
	if err := attempt.markReplayDispatch(ctx); err != nil {
		return err
	}
	return p.TagComputeMachineCVM(ctx, machine, ownership)
}

func (p *TencentProvider) convergeComputeNodeOwnership(ctx context.Context, attempt *providerMutationAttempt, allocation ComputeAllocation, prepared ComputeAllocationPreparation, ownership MachineOwnership) error {
	if attempt == nil || attempt.Fresh {
		return p.ClaimComputeNode(ctx, allocation, ownership)
	}
	proof, err := p.ProveComputeClaimRecovery(ctx, allocation, prepared, ownership)
	if err != nil {
		return err
	}
	if proof.CVMOwnershipState != "target_owned" {
		return fmt.Errorf("compute_machine_ownership_readback_mismatch")
	}
	if proof.NodeOwnershipState == "target_owned" {
		return nil
	}
	if proof.NodeOwnershipState != "unallocated" {
		return fmt.Errorf("compute_machine_ownership_readback_mismatch")
	}
	claimed, err := attempt.claimReplay(ctx)
	if err != nil {
		if errors.Is(err, ErrRuntimeOperationNotCurrent) {
			return ErrWorkspaceLaunchPending
		}
		return err
	}
	if !claimed {
		return ErrWorkspaceLaunchPending
	}
	proof, err = p.ProveComputeClaimRecovery(ctx, allocation, prepared, ownership)
	if err != nil {
		return err
	}
	if proof.CVMOwnershipState != "target_owned" {
		return fmt.Errorf("compute_machine_ownership_readback_mismatch")
	}
	if proof.NodeOwnershipState == "target_owned" {
		return nil
	}
	if proof.NodeOwnershipState != "unallocated" {
		return fmt.Errorf("compute_machine_ownership_readback_mismatch")
	}
	if err := attempt.markReplayDispatch(ctx); err != nil {
		return err
	}
	return p.ClaimComputeNode(ctx, allocation, ownership)
}

func (p *TencentProvider) readComputeMachineOwnership(ctx context.Context, allocation ComputeAllocation, prepared ComputeAllocationPreparation, ownership MachineOwnership, requireNode bool) error {
	proof, err := p.ProveComputeClaimRecovery(ctx, allocation, prepared, ownership)
	if err != nil {
		return err
	}
	if proof.CVMOwnershipState != "target_owned" || requireNode && proof.NodeOwnershipState != "target_owned" {
		return fmt.Errorf("compute_machine_ownership_readback_mismatch")
	}
	return nil
}
