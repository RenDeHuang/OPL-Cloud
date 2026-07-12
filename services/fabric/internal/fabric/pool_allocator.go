package fabric

import (
	"context"
	"fmt"
	"sort"
	"time"
)

var (
	poolReconcileAttempts = 30
	poolReconcileDelay    = 10 * time.Second
)

func (s *Service) reconcileComputePool(packageID string, dryRun bool) {
	if packageID == "" {
		packageID = "basic"
	}
	plan := packagePlan(packageID)
	poolKey := plan.ID + ":" + plan.InstanceType
	_ = s.operations.WithPoolLock(context.Background(), poolKey, func(ctx context.Context) error {
		var lastErr error
		for attempt := 0; attempt < poolReconcileAttempts; attempt++ {
			complete, progressed, err := s.reconcileComputePoolOnce(ctx, packageID, dryRun)
			if err == nil && complete {
				return nil
			}
			if err != nil {
				lastErr = err
			} else if !progressed {
				lastErr = fmt.Errorf("compute_machine_unavailable")
			}
			if !progressed && attempt+1 < poolReconcileAttempts {
				time.Sleep(poolReconcileDelay)
			}
		}
		return s.failPendingComputeOperations(ctx, packageID, lastErr)
	})
}

func (s *Service) failPendingComputeOperations(ctx context.Context, packageID string, cause error) error {
	pending, err := s.pendingComputeOperations(ctx, packageID)
	if err != nil {
		return err
	}
	if cause == nil {
		cause = fmt.Errorf("compute_machine_unavailable")
	}
	for _, operation := range pending {
		var resource ComputeAllocation
		if !decodeOperationResource(operation, &resource) {
			continue
		}
		resource.Status = "failed"
		if ownership, ownershipErr := s.operations.MachineOwnership(ctx, resource.ID); ownershipErr == nil && ownership.Status == "quarantined" {
			resource.Status = "quarantined"
			resource.Provider = "tencent-tke"
			resource.ProviderResourceID = "machine/" + ownership.MachineID
			resource.NodePoolID = ownership.NodePoolID
			resource.MachineName = ownership.MachineID
			resource.InstanceID = ownership.InstanceID
			resource.CVMInstanceID = ownership.InstanceID
			resource.NodeName = ownership.NodeName
		}
		if err := s.recordOperation(ctx, operation, "failed", resource, cause); err != nil {
			return err
		}
		s.mu.Lock()
		s.computes[resource.ID] = resource
		s.mu.Unlock()
	}
	return nil
}

func (s *Service) reconcileComputePoolOnce(ctx context.Context, packageID string, dryRun bool) (bool, bool, error) {
	pending, err := s.pendingComputeOperations(ctx, packageID)
	if err != nil {
		return false, false, err
	}
	ownerships, err := s.operations.ListMachineOwnerships(ctx)
	if err != nil {
		return false, false, err
	}
	active := make([]MachineOwnership, 0, len(ownerships))
	ownedMachines := map[string]bool{}
	for _, ownership := range ownerships {
		if ownership.Status != "released" {
			ownedMachines[ownership.MachineID] = true
		}
		if ownership.PackageID == packageID && (ownership.Status == "claimed" || ownership.Status == "active") {
			active = append(active, ownership)
		}
	}
	plan := packagePlan(packageID)
	state, err := s.provider.ReconcileComputePool(ctx, ComputePoolDemand{PoolID: plan.ID, PackageID: packageID, NodePoolID: plan.NodePoolID, InstanceType: plan.InstanceType, DesiredReplicas: int64(len(active) + len(pending)), DryRun: dryRun})
	if err != nil {
		return false, false, err
	}
	machines := make([]ProviderMachine, 0, len(state.Machines))
	for _, machine := range state.Machines {
		if machine.Ready && machine.MachineID != "" && !ownedMachines[machine.MachineID] && (machine.InstanceType == "" || machine.InstanceType == plan.InstanceType) {
			machines = append(machines, machine)
		}
	}
	sort.Slice(machines, func(i, j int) bool { return machines[i].MachineID < machines[j].MachineID })
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].CreatedAt.Equal(pending[j].CreatedAt) {
			return pending[i].ResourceID < pending[j].ResourceID
		}
		return pending[i].CreatedAt.Before(pending[j].CreatedAt)
	})
	limit := len(pending)
	if len(machines) < limit {
		limit = len(machines)
	}
	for i := 0; i < limit; i++ {
		operation := pending[i]
		var resource ComputeAllocation
		if !decodeOperationResource(operation, &resource) {
			continue
		}
		machine := machines[i]
		ownership := MachineOwnership{ID: "owner_" + stableSuffix(resource.ID, machine.MachineID)[:16], ResourceID: resource.ID, AccountID: resource.AccountID, WorkspaceID: resource.WorkspaceID, PackageID: packageID, NodePoolID: state.NodePoolID, MachineID: machine.MachineID, InstanceID: machine.InstanceID, NodeName: machine.NodeName, Status: "claimed", ProviderRequestID: state.ProviderRequestID, ClaimedAt: s.now()}
		claimed, _, err := s.operations.ClaimMachine(ctx, ownership)
		if err != nil {
			continue
		}
		if err := s.provider.TagComputeMachine(ctx, machine, claimed); err != nil {
			if deleteErr := s.provider.DeleteComputeMachine(ctx, machine); deleteErr == nil {
				now := s.now()
				claimed.Status = "released"
				claimed.ReleasedAt = &now
			} else {
				claimed.Status = "quarantined"
			}
			_ = s.operations.SaveMachineOwnership(ctx, claimed)
			continue
		}
		claimed.Status = "active"
		if err := s.operations.SaveMachineOwnership(ctx, claimed); err != nil {
			continue
		}
		resource.Status = "running"
		resource.Provider = "tencent-tke"
		resource.ProviderRequestID = state.ProviderRequestID
		resource.ProviderResourceID = "machine/" + machine.MachineID
		resource.PoolID = state.PoolID
		resource.NodePoolID = state.NodePoolID
		resource.MachineName = machine.MachineID
		resource.InstanceID = machine.InstanceID
		resource.CVMInstanceID = machine.InstanceID
		resource.NodeName = machine.NodeName
		resource.PrivateIP = machine.PrivateIP
		resource.PublicIP = machine.PublicIP
		resource.CreatedAt = firstTime(resource.CreatedAt, s.now())
		if err := s.recordOperation(ctx, operation, "succeeded", resource, nil); err != nil {
			return false, i > 0, err
		}
		s.mu.Lock()
		s.computes[resource.ID] = resource
		s.mu.Unlock()
	}
	remaining, err := s.pendingComputeOperations(ctx, packageID)
	return len(remaining) == 0, limit > 0, err
}

func (s *Service) pendingComputeOperations(ctx context.Context, packageID string) ([]FabricOperation, error) {
	records, err := s.operations.List(ctx)
	if err != nil {
		return nil, err
	}
	latest := map[string]FabricOperation{}
	for _, operation := range records {
		if operation.Action == "create_compute_allocation" {
			latest[operation.OperationID] = operation
		}
	}
	out := []FabricOperation{}
	for _, operation := range latest {
		if operation.Status != "started" {
			continue
		}
		var resource ComputeAllocation
		if !decodeOperationResource(operation, &resource) || firstNonEmpty(resource.PackageID, "basic") != packageID {
			continue
		}
		out = append(out, operation)
	}
	return out, nil
}

func firstTime(value, fallback time.Time) time.Time {
	if value.IsZero() {
		return fallback
	}
	return value
}

var _ = fmt.Sprintf
