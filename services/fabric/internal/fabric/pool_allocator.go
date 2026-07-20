package fabric

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	poolReconcileAttempts = 90
	poolReconcileDelay    = 10 * time.Second
)

func (s *Service) reconcileComputePool(packageID string, dryRun bool) error {
	if packageID == "" {
		packageID = "basic"
	}
	plan := packagePlan(packageID)
	poolKey := plan.ID + ":" + plan.InstanceType
	return s.operations.WithPoolLock(context.Background(), poolKey, func(ctx context.Context) error {
		return s.reconcileComputePoolLocked(ctx, packageID, dryRun)
	})
}

func (s *Service) reconcileComputePoolLocked(ctx context.Context, packageID string, dryRun bool) error {
	var lastErr error
	var providerErr error
	providerResultUnknown := false
	for attempt := 0; attempt < poolReconcileAttempts; attempt++ {
		complete, progressed, err := s.reconcileComputePoolOnce(ctx, packageID, dryRun)
		providerResultUnknown = err != nil
		if err == nil && complete {
			return nil
		}
		if err != nil {
			lastErr = err
			providerErr = err
		} else if !progressed && providerErr == nil {
			lastErr = fmt.Errorf("compute_machine_unavailable")
		}
		if !progressed && attempt+1 < poolReconcileAttempts {
			time.Sleep(poolReconcileDelay)
		}
	}
	if providerErr != nil {
		lastErr = providerErr
	}
	if err := s.preparePendingComputeFailures(ctx, packageID, lastErr, providerResultUnknown); err != nil {
		return err
	}
	if err := s.reconcileFailedComputeCleanup(ctx, packageID, dryRun); err != nil {
		return err
	}
	if err := s.finalizePendingComputeFailures(ctx, packageID, lastErr); err != nil {
		return err
	}
	return lastErr
}

func (s *Service) preparePendingComputeFailures(ctx context.Context, packageID string, cause error, providerResultUnknown bool) error {
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
			if err := s.recordOperation(ctx, operation, "failed", resource, cause); err != nil {
				return err
			}
			s.mu.Lock()
			s.computes[resource.ID] = resource
			s.mu.Unlock()
			continue
		}
		currentReplicas, currentErr := strconv.ParseInt(resource.ProviderData["currentReplicas"], 10, 64)
		observedMachines, machinesErr := strconv.Atoi(resource.ProviderData["observedMachines"])
		if providerResultUnknown || currentErr != nil || currentReplicas != 0 || machinesErr != nil || observedMachines != 0 {
			if resource.ProviderData == nil {
				resource.ProviderData = map[string]string{}
			}
			resource.Status = "provisioning"
			resource.ProviderData["recoveryAction"] = "manual_review"
			if err := s.recordOperation(ctx, operation, "started", resource, cause); err != nil {
				return err
			}
			s.mu.Lock()
			s.computes[resource.ID] = resource
			s.mu.Unlock()
			continue
		}
		resource.Status = "provisioning"
		if err := s.recordOperation(ctx, operation, "canceling", resource, cause); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) reconcileFailedComputeCleanup(ctx context.Context, packageID string, dryRun bool) error {
	var lastErr error
	for attempt := 0; attempt < poolReconcileAttempts; attempt++ {
		complete, progressed, err := s.reconcileComputePoolOnce(ctx, packageID, dryRun)
		if err == nil && complete {
			return nil
		}
		if err != nil {
			lastErr = err
		} else if !progressed {
			lastErr = fmt.Errorf("compute_cleanup_unconfirmed")
		}
		if !progressed && attempt+1 < poolReconcileAttempts {
			time.Sleep(poolReconcileDelay)
		}
	}
	return lastErr
}

func (s *Service) finalizePendingComputeFailures(ctx context.Context, packageID string, cause error) error {
	operations, err := s.operations.List(ctx)
	if err != nil {
		return err
	}
	latest := map[string]FabricOperation{}
	for _, operation := range operations {
		if operation.Action == "create_compute_allocation" {
			latest[operation.OperationID] = operation
		}
	}
	for _, operation := range latest {
		if operation.Status != "canceling" {
			continue
		}
		var resource ComputeAllocation
		if !decodeOperationResource(operation, &resource) || firstNonEmpty(resource.PackageID, "basic") != packageID {
			continue
		}
		resource.Status = "failed"
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
		if ownership.PackageID == packageID && (ownership.Status == "claimed" || ownership.Status == "active" || ownership.Status == "quarantined") {
			active = append(active, ownership)
		}
	}
	plan := packagePlan(packageID)
	nodePoolID, err := computePoolNodePoolID(active, pending)
	if err != nil {
		return false, false, err
	}
	state, err := s.provider.ReconcileComputePool(ctx, ComputePoolDemand{PoolID: plan.ID, PackageID: packageID, NodePoolID: nodePoolID, InstanceType: plan.InstanceType, DesiredReplicas: int64(len(active) + len(pending)), DryRun: dryRun})
	if evidenceErr := s.recordPoolReconcileEvidence(ctx, pending, state, err); evidenceErr != nil {
		return false, false, evidenceErr
	}
	if err != nil {
		return false, false, err
	}
	machines := make([]ProviderMachine, 0, len(state.Machines))
	for _, machine := range state.Machines {
		if machine.MachineID != "" && !ownedMachines[machine.MachineID] {
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
		resource.Status = "provisioning"
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
		resource.ChargeType = machine.ChargeType
		resource.RenewFlag = machine.RenewFlag
		resource.Deadline = machine.Deadline
		resource.ProviderData = map[string]string{"instanceType": machine.InstanceType, "zone": machine.Zone, "chargeType": machine.ChargeType, "renewFlag": machine.RenewFlag, "deadline": machine.Deadline, "machineName": machine.MachineID}
		resource.CostTags = oplCostTags(resource.AccountID, resource.WorkspaceID, resource.ID, claimed.ID)
		resource.CreatedAt = firstTime(resource.CreatedAt, s.now())
		if !machine.Ready || machine.InstanceType != plan.InstanceType || machine.Zone == "" || machine.ChargeType != "PREPAID" || machine.RenewFlag != "NOTIFY_AND_MANUAL_RENEW" || machine.Deadline == "" {
			resource.Status = "quarantined"
			resource.ProviderData["recoveryAction"] = "manual_review"
			claimed.Status = "quarantined"
			if err := s.operations.SaveMachineOwnership(ctx, claimed); err != nil {
				return false, i > 0, err
			}
			if err := s.recordOperation(ctx, operation, "failed", resource, fmt.Errorf("compute_provider_readback_mismatch")); err != nil {
				return false, i > 0, err
			}
			s.mu.Lock()
			s.computes[resource.ID] = resource
			s.mu.Unlock()
			continue
		}
		if tagErr := s.provider.TagComputeMachine(ctx, machine, claimed); tagErr != nil {
			resource.Status = "quarantined"
			claimed.Status = "quarantined"
			if err := s.operations.SaveMachineOwnership(ctx, claimed); err != nil {
				return false, i > 0, err
			}
			if err := s.recordOperation(ctx, operation, "failed", resource, tagErr); err != nil {
				return false, i > 0, err
			}
			s.mu.Lock()
			s.computes[resource.ID] = resource
			s.mu.Unlock()
			continue
		}
		verified, verifyErr := s.provider.SyncComputeAllocation(ctx, resource)
		if verified.ID != "" {
			resource = verified
		}
		if verifyErr == nil && !isExternallyDeletedComputeStatus(resource.Status) &&
			(!isReadyResourceStatus(resource.Status) || resource.ChargeType != "PREPAID" || resource.RenewFlag != "NOTIFY_AND_MANUAL_RENEW" || resource.Deadline == "" || resource.ProviderData["zone"] == "" || resource.ProviderData["instanceType"] != plan.InstanceType) {
			verifyErr = fmt.Errorf("compute_provider_readback_mismatch")
		}
		if verifyErr != nil {
			resource.Status = "quarantined"
			claimed.Status = "quarantined"
			if err := s.operations.SaveMachineOwnership(ctx, claimed); err != nil {
				return false, i > 0, err
			}
			if err := s.recordOperation(ctx, operation, "failed", resource, verifyErr); err != nil {
				return false, i > 0, err
			}
			s.mu.Lock()
			s.computes[resource.ID] = resource
			s.mu.Unlock()
			continue
		}
		if isExternallyDeletedComputeStatus(resource.Status) {
			now := s.now()
			claimed.Status = "released"
			claimed.ReleasedAt = &now
			_ = s.operations.SaveMachineOwnership(ctx, claimed)
			continue
		}
		claimed.Status = "active"
		if err := s.operations.SaveMachineOwnership(ctx, claimed); err != nil {
			continue
		}
		if err := s.recordOperation(ctx, operation, "succeeded", resource, nil); err != nil {
			return false, i > 0, err
		}
		s.mu.Lock()
		s.computes[resource.ID] = resource
		s.mu.Unlock()
	}
	remaining, err := s.pendingComputeOperations(ctx, packageID)
	return len(remaining) == 0 && state.CurrentReplicas == state.DesiredReplicas, limit > 0, err
}

func computePoolNodePoolID(active []MachineOwnership, pending []FabricOperation) (string, error) {
	ids := map[string]struct{}{}
	for _, ownership := range active {
		if id := strings.TrimSpace(ownership.NodePoolID); id != "" {
			ids[id] = struct{}{}
		}
	}
	for _, operation := range pending {
		var resource ComputeAllocation
		if decodeOperationResource(operation, &resource) {
			if id := strings.TrimSpace(resource.NodePoolID); id != "" {
				ids[id] = struct{}{}
			}
		}
	}
	if len(ids) > 1 {
		return "", fmt.Errorf("compute_node_pool_identity_conflict")
	}
	for id := range ids {
		return id, nil
	}
	return "", nil
}

func (s *Service) recordPoolReconcileEvidence(ctx context.Context, pending []FabricOperation, state ComputePoolState, reconcileErr error) error {
	for index := range pending {
		var resource ComputeAllocation
		if !decodeOperationResource(pending[index], &resource) {
			continue
		}
		if resource.ProviderData == nil {
			resource.ProviderData = map[string]string{}
		}
		attempt, _ := strconv.Atoi(resource.ProviderData["poolReconcileAttempt"])
		for key, value := range state.ProviderData {
			resource.ProviderData[key] = value
		}
		resource.ProviderData["poolReconcileAttempt"] = strconv.Itoa(attempt + 1)
		resource.ProviderData["desiredReplicas"] = strconv.FormatInt(state.DesiredReplicas, 10)
		resource.ProviderData["currentReplicas"] = strconv.FormatInt(state.CurrentReplicas, 10)
		resource.ProviderData["observedMachines"] = strconv.Itoa(len(state.Machines))
		resource.ProviderData["nodePoolId"] = state.NodePoolID
		resource.ProviderData["describeMachinesRequestId"] = state.ProviderRequestID
		resource.PoolID = firstNonEmpty(state.PoolID, resource.PoolID)
		resource.NodePoolID = firstNonEmpty(state.NodePoolID, resource.NodePoolID)
		resource.ProviderRequestID = firstNonEmpty(state.ProviderRequestID, resource.ProviderRequestID)
		if err := s.recordOperation(ctx, pending[index], "started", resource, reconcileErr); err != nil {
			return err
		}
		fillOperationResource(&pending[index], resource)
	}
	return nil
}

func (s *Service) pendingComputeOperations(ctx context.Context, packageID string) ([]FabricOperation, error) {
	records, err := s.operations.List(ctx)
	if err != nil {
		return nil, err
	}
	latest := map[string]FabricOperation{}
	destroyRequested := map[string]bool{}
	for _, operation := range records {
		if operation.Action == "create_compute_allocation" {
			latest[operation.OperationID] = operation
		}
		if operation.Action == "destroy_compute_allocation" && operation.Status != "rejected" {
			destroyRequested[operation.ResourceID] = true
		}
	}
	out := []FabricOperation{}
	for _, operation := range latest {
		if operation.Status != "started" || destroyRequested[operation.ResourceID] {
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
