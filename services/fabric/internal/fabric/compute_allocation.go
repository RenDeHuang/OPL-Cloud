package fabric

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"
)

type normalLaunchMutationBudget struct {
	Attempted int `json:"attempted"`
	Confirmed int `json:"confirmed"`
	Unknown   int `json:"unknown"`
	Max       int `json:"max"`
}

func reservedNormalLaunchMutationBudget() normalLaunchMutationBudget {
	return normalLaunchMutationBudget{Attempted: 1, Confirmed: 0, Unknown: 1, Max: 1}
}

func confirmedNormalLaunchMutationBudget() normalLaunchMutationBudget {
	return normalLaunchMutationBudget{Attempted: 1, Confirmed: 1, Unknown: 0, Max: 1}
}

func validNormalLaunchMutationBudget(value normalLaunchMutationBudget) bool {
	return value.Max == 1 && value.Attempted == 1 && value.Confirmed >= 0 && value.Confirmed <= value.Attempted &&
		value.Unknown >= 0 && value.Unknown <= value.Attempted && value.Confirmed+value.Unknown == value.Attempted
}

func normalLaunchStageBudget(payload map[string]any, stage string) (normalLaunchMutationBudget, bool, bool) {
	if payload == nil {
		return normalLaunchMutationBudget{}, false, true
	}
	budgets, present := payload["normalLaunchMutationBudget"]
	if !present {
		return normalLaunchMutationBudget{}, false, true
	}
	body, err := json.Marshal(budgets)
	if err != nil {
		return normalLaunchMutationBudget{}, false, false
	}
	decoded := map[string]normalLaunchMutationBudget{}
	if json.Unmarshal(body, &decoded) != nil {
		return normalLaunchMutationBudget{}, false, false
	}
	value, present := decoded[stage]
	if !present {
		return normalLaunchMutationBudget{}, false, true
	}
	return value, true, validNormalLaunchMutationBudget(value)
}

func withNormalLaunchStageBudget(payload map[string]any, stage string, budget normalLaunchMutationBudget) map[string]any {
	next := maps.Clone(payload)
	if next == nil {
		next = map[string]any{}
	}
	budgets := map[string]any{}
	if current, ok := next["normalLaunchMutationBudget"]; ok {
		if body, err := json.Marshal(current); err == nil {
			_ = json.Unmarshal(body, &budgets)
		}
	}
	budgets[stage] = map[string]any{
		"attempted": budget.Attempted,
		"confirmed": budget.Confirmed,
		"unknown":   budget.Unknown,
		"max":       budget.Max,
	}
	next["normalLaunchMutationBudget"] = budgets
	return next
}

func preserveNormalLaunchMutationBudget(next, current map[string]any) map[string]any {
	if value, ok := current["normalLaunchMutationBudget"]; ok {
		next["normalLaunchMutationBudget"] = value
	}
	return preserveLaunchStageBinding(next, current)
}
func (s *Service) CreateComputeAllocation(ctx context.Context, input ComputeAllocationInput) (ComputeAllocation, error) {
	if input.PackageID != "basic" && input.PackageID != "pro" {
		return ComputeAllocation{}, ErrUnsupportedComputePackage
	}
	if strings.TrimSpace(input.NodePoolID) == "" {
		input.NodePoolID = strings.TrimSpace(s.provider.Descriptor().DefaultComputePoolIDs[input.PackageID])
	}
	if input.NodePoolID == "" {
		return ComputeAllocation{}, fmt.Errorf("compute_node_pool_id_required")
	}
	if strings.TrimSpace(input.IdempotencyKey) == "" {
		return ComputeAllocation{}, fmt.Errorf("compute_idempotency_key_required")
	}
	requestHash := hashInput(input)
	now := s.now()
	id := firstNonEmpty(input.ID, "ca_"+stableSuffix("create_compute_allocation", input.IdempotencyKey)[:18])
	input.ID = id
	allocation := ComputeAllocation{
		ID:                id,
		AccountID:         input.AccountID,
		WorkspaceID:       input.WorkspaceID,
		PackageID:         firstNonEmpty(input.PackageID, "basic"),
		NodePoolID:        strings.TrimSpace(input.NodePoolID),
		Status:            "provisioning",
		Provider:          s.provider.Descriptor().Name,
		ProviderRequestID: providerRequestID("compute", input.IdempotencyKey),
		CreatedAt:         now,
	}
	operation := newOperation("create_compute_allocation", "compute_allocation", id, input.AccountID, input.WorkspaceID, input.IdempotencyKey, requestHash, now)
	operation.ID = "fop_compute_claim_" + stableSuffix("create_compute_allocation", input.IdempotencyKey)
	operation.Status = "started"
	operation.CreatedAt = now
	operation.ComputePoolKey = allocation.NodePoolID
	allocation.OperationID = operation.OperationID
	fillOperationResource(&operation, allocation)
	stored, claimed, err := s.operations.ClaimComputePoolRuntime(ctx, operation)
	if err != nil {
		return ComputeAllocation{}, err
	}
	if stored.ComputePoolKey != operation.ComputePoolKey {
		return ComputeAllocation{}, ErrComputeIdempotencyConflict
	}
	if !claimed {
		replayed, err := replayComputeAllocationOperation(stored, requestHash)
		if err == nil && stored.Status == "started" {
			s.startComputeAllocation(stored, replayed, input.DryRun)
		}
		return replayed, err
	}
	input.OperationID = operation.OperationID
	s.startComputeAllocation(stored, allocation, input.DryRun)
	return allocation, nil
}

func (s *Service) startComputeAllocation(operation FabricOperation, allocation ComputeAllocation, dryRun bool) {
	s.mu.Lock()
	if s.reconciling[allocation.ID] {
		s.mu.Unlock()
		return
	}
	s.reconciling[allocation.ID] = true
	s.computes[allocation.ID] = allocation
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.reconciling, allocation.ID)
			s.mu.Unlock()
		}()
		s.finishCreateComputeAllocation(operation, allocation, dryRun)
	}()
}

func (s *Service) finishCreateComputeAllocation(operation FabricOperation, allocation ComputeAllocation, dryRun bool) {
	s.finishCreateComputeAllocationLegacy(operation, allocation, dryRun)
}

// finishCreateComputeAllocationLegacy preserves the established compute
// contract for callers outside the normal Workspace launch. The durable
// reservation/readback budget above is intentionally a narrow launch boundary;
// unrelated compute operations retain their existing retry semantics.
func (s *Service) finishCreateComputeAllocationLegacy(operation FabricOperation, allocation ComputeAllocation, dryRun bool) {
	plan, planOK := providerPlan(s.provider, allocation.PackageID)
	if !planOK {
		_ = computeAllocationFailure(context.Background(), s, operation, allocation, ComputeAllocationPreparation{}, ErrUnsupportedComputePackage)
		return
	}
	poolKey := allocation.NodePoolID
	leaseOwner, err := newLeaseToken()
	if err != nil {
		return
	}
	claimLease := func(duration time.Duration) bool {
		now := s.now()
		current, claimed, claimErr := s.operations.TryClaimComputePoolHead(context.Background(), operation.ID, poolKey, leaseOwner, now, now.Add(duration))
		if claimErr != nil || !claimed {
			return false
		}
		operation = current
		return true
	}
	pollLease := s.computeAllocationAttemptTimeout + 2*s.computeAllocationPollInterval
	if !claimLease(pollLease) {
		return
	}
	terminal := false
	defer func() {
		if !terminal {
			_ = s.operations.ReleaseComputePoolHead(context.Background(), operation.ID, poolKey, leaseOwner)
		}
	}()

	prepared, hasPlan := decodeComputeAllocationPlan(operation)
	if !hasPlan {
		prepareCtx, cancel := context.WithTimeout(context.Background(), s.computeAllocationAttemptTimeout)
		prepared, err = s.provider.PrepareComputeAllocation(prepareCtx, ComputeAllocationInput{
			ID: allocation.ID, AccountID: allocation.AccountID, WorkspaceID: allocation.WorkspaceID,
			PackageID: allocation.PackageID, NodePoolID: allocation.NodePoolID, DryRun: dryRun,
		})
		cancel()
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return
		}
		if err != nil {
			terminal = true
			_ = computeAllocationFailure(context.Background(), s, operation, allocation, prepared, err)
			return
		}
		if err := validateComputeAllocationPreparation(prepared, allocation, plan); err != nil {
			terminal = true
			_ = computeAllocationFailure(context.Background(), s, operation, allocation, prepared, err)
			return
		}
		operation.RedactedProviderPayload = preserveLaunchStageBinding(computeAllocationOperationPayload(allocation, prepared), operation.RedactedProviderPayload)
		if err := s.operations.SaveRuntime(context.Background(), operation); err != nil {
			return
		}
	}

	pollDeadline := time.Now().Add(s.computeAllocationPollWindow)
	var result ComputeAllocation
	attempted := false
	for {
		if attempted && !time.Now().Before(pollDeadline) {
			return
		}
		if !claimLease(pollLease) {
			return
		}
		attemptCtx, cancel := context.WithTimeout(context.Background(), s.computeAllocationAttemptTimeout)
		result, err = s.provider.CreateComputeAllocation(s.providerMutationContext(attemptCtx, operation), ComputeAllocationExecution{Allocation: allocation, Plan: prepared, DryRun: dryRun})
		cancel()
		attempted = true
		result = mergeComputeAllocation(result, allocation, prepared)
		if errors.Is(err, ErrComputeAllocationPending) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			operation.RedactedProviderPayload = preserveLaunchStageBinding(computeAllocationOperationPayload(result, prepared), operation.RedactedProviderPayload)
			operation.ProviderRequestID = firstNonEmpty(result.ProviderRequestID, operation.ProviderRequestID)
			if saveErr := s.operations.SaveRuntime(context.Background(), operation); saveErr != nil {
				return
			}
			s.mu.Lock()
			s.computes[result.ID] = result
			s.mu.Unlock()
			remaining := time.Until(pollDeadline)
			if remaining <= 0 {
				return
			}
			wait := min(s.computeAllocationPollInterval, remaining)
			timer := time.NewTimer(wait)
			<-timer.C
			continue
		}
		if err != nil {
			terminal = true
			_ = computeAllocationFailure(context.Background(), s, operation, result, prepared, err)
			return
		}
		break
	}

	if !claimLease(s.computeAllocationFinalizeTimeout + s.computeAllocationPollInterval) {
		return
	}
	finalizeCtx, cancel := context.WithTimeout(context.Background(), s.computeAllocationFinalizeTimeout)
	defer cancel()
	if err := s.provider.ValidateComputeAllocation(result, prepared); err != nil {
		terminal = true
		_ = computeAllocationFailure(context.Background(), s, operation, result, prepared, err)
		return
	}
	providerIdentity := firstNonEmpty(result.ProviderResourceID, result.ID)
	machine := ProviderMachine{
		MachineID: firstNonEmpty(result.MachineName, providerIdentity), InstanceID: firstNonEmpty(result.InstanceID, result.CVMInstanceID, providerIdentity), NodeName: firstNonEmpty(result.NodeName, providerIdentity),
		PrivateIP: result.PrivateIP, PublicIP: result.PublicIP, InstanceType: result.InstanceType, Zone: result.Zone,
		ChargeType: result.ChargeType, RenewFlag: result.RenewFlag, Deadline: result.Deadline, Ready: true,
	}
	ownership := MachineOwnership{
		ID: "owner_" + stableSuffix(result.ID, result.MachineName)[:16], ResourceID: result.ID, AccountID: result.AccountID,
		WorkspaceID: result.WorkspaceID, PackageID: result.PackageID, NodePoolID: result.NodePoolID, MachineID: machine.MachineID,
		InstanceID: machine.InstanceID, NodeName: machine.NodeName, Status: "claimed",
		ProviderRequestID: result.ProviderRequestID, ClaimedAt: s.now(),
	}
	claimed, _, claimErr := s.operations.ClaimMachine(finalizeCtx, ownership)
	if claimErr != nil {
		terminal = true
		_ = computeAllocationFailure(context.Background(), s, operation, result, prepared, claimErr)
		return
	}
	result.CostTags = oplCostTags(result.AccountID, result.WorkspaceID, result.ID, claimed.ID)
	if tagErr := s.provider.TagComputeMachine(s.providerMutationContext(finalizeCtx, operation), machine, claimed); tagErr != nil {
		claimed.Status = "quarantined"
		_ = s.operations.SaveMachineOwnership(context.Background(), claimed)
		terminal = true
		_ = computeAllocationClaimPending(context.Background(), s, operation, result, prepared, tagErr)
		return
	}
	verified, verifyErr := s.provider.SyncComputeAllocation(finalizeCtx, result)
	verified = mergeComputeAllocation(verified, result, prepared)
	if verifyErr != nil || s.provider.ValidateComputeAllocation(verified, prepared) != nil || !isReadyResourceStatus(verified.Status) {
		claimed.Status = "quarantined"
		_ = s.operations.SaveMachineOwnership(context.Background(), claimed)
		if verifyErr == nil {
			verifyErr = fmt.Errorf("compute_provider_readback_mismatch")
		}
		terminal = true
		_ = computeAllocationClaimPending(context.Background(), s, operation, verified, prepared, verifyErr)
		return
	}
	claimed.Status = "active"
	if err := s.operations.SaveMachineOwnership(finalizeCtx, claimed); err != nil {
		terminal = true
		_ = computeAllocationFailure(context.Background(), s, operation, verified, prepared, err)
		return
	}
	operation.Status = "succeeded"
	operation.FinishedAt = s.now()
	operation.ProviderRequestID = firstNonEmpty(verified.ProviderRequestID, operation.ProviderRequestID)
	operation.RedactedProviderPayload = preserveLaunchStageBinding(computeAllocationOperationPayload(verified, prepared), operation.RedactedProviderPayload)
	if err := s.operations.SaveRuntime(finalizeCtx, operation); err != nil {
		return
	}
	terminal = true
	s.mu.Lock()
	s.computes[verified.ID] = verified
	s.mu.Unlock()
}

func validateComputeAllocationPreparation(prepared ComputeAllocationPreparation, allocation ComputeAllocation, expected plan) error {
	if prepared.PoolID != expected.ID || prepared.PackageID != allocation.PackageID || prepared.NodePoolID != allocation.NodePoolID ||
		prepared.InstanceType != expected.InstanceType || prepared.MaxReplicas <= 0 || prepared.BaselineReplicas < 0 || prepared.TargetReplicas != prepared.BaselineReplicas+1 || prepared.TargetReplicas > prepared.MaxReplicas ||
		int64(len(prepared.BeforeMachineNames)) != prepared.BaselineReplicas {
		return fmt.Errorf("compute_allocation_preparation_mismatch")
	}
	seen := map[string]bool{}
	for _, name := range prepared.BeforeMachineNames {
		if strings.TrimSpace(name) == "" || seen[name] {
			return fmt.Errorf("compute_allocation_preparation_mismatch")
		}
		seen[name] = true
	}
	return nil
}

func mergeComputeAllocation(current, fallback ComputeAllocation, prepared ComputeAllocationPreparation) ComputeAllocation {
	current.ID = firstNonEmpty(current.ID, fallback.ID)
	current.AccountID = firstNonEmpty(current.AccountID, fallback.AccountID)
	current.WorkspaceID = firstNonEmpty(current.WorkspaceID, fallback.WorkspaceID)
	current.PackageID = firstNonEmpty(current.PackageID, fallback.PackageID, prepared.PackageID)
	current.Status = firstNonEmpty(current.Status, fallback.Status, "provisioning")
	current.Provider = firstNonEmpty(current.Provider, fallback.Provider)
	current.ProviderRequestID = firstNonEmpty(current.ProviderRequestID, fallback.ProviderRequestID)
	current.PoolID = firstNonEmpty(current.PoolID, fallback.PoolID, prepared.PoolID)
	current.NodePoolID = firstNonEmpty(current.NodePoolID, fallback.NodePoolID, prepared.NodePoolID)
	current.InstanceType = firstNonEmpty(current.InstanceType, fallback.InstanceType, prepared.InstanceType)
	if current.CreatedAt.IsZero() {
		current.CreatedAt = fallback.CreatedAt
	}
	if current.ProviderData == nil {
		current.ProviderData = maps.Clone(fallback.ProviderData)
	}
	if current.ProviderData == nil {
		current.ProviderData = map[string]string{}
	}
	current.ProviderData["instanceType"] = firstNonEmpty(current.ProviderData["instanceType"], prepared.InstanceType)
	return current
}

func computeAllocationFailure(ctx context.Context, s *Service, operation FabricOperation, allocation ComputeAllocation, prepared ComputeAllocationPreparation, cause error) error {
	if allocation.ID == "" {
		return cause
	}
	allocation.Status = "quarantined"
	if allocation.ProviderData == nil {
		allocation.ProviderData = map[string]string{}
	}
	allocation.ProviderData["recoveryAction"] = "manual_review"
	operation.Status = "failed"
	operation.ErrorCode = errorCode(cause)
	operation.FinishedAt = s.now()
	operation.ProviderRequestID = firstNonEmpty(allocation.ProviderRequestID, operation.ProviderRequestID)
	operation.RedactedProviderPayload = preserveNormalLaunchMutationBudget(computeAllocationOperationPayload(allocation, prepared), operation.RedactedProviderPayload)
	if saveErr := s.operations.SaveRuntime(ctx, operation); saveErr != nil {
		return saveErr
	}
	s.mu.Lock()
	s.computes[allocation.ID] = allocation
	s.mu.Unlock()
	return cause
}

func computeAllocationClaimPending(ctx context.Context, s *Service, operation FabricOperation, allocation ComputeAllocation, prepared ComputeAllocationPreparation, cause error) error {
	if allocation.ID == "" {
		return cause
	}
	allocation.Status = "compute_claim_pending"
	if allocation.ProviderData == nil {
		allocation.ProviderData = map[string]string{}
	}
	allocation.ProviderData["recoveryAction"] = "compute_claim_recovery"
	operation.Status = "claim_pending"
	operation.ErrorCode = errorCode(cause)
	operation.FinishedAt = time.Time{}
	operation.ProviderRequestID = firstNonEmpty(allocation.ProviderRequestID, operation.ProviderRequestID)
	operation.RedactedProviderPayload = preserveNormalLaunchMutationBudget(computeAllocationOperationPayload(allocation, prepared), operation.RedactedProviderPayload)
	if saveErr := s.operations.SaveRuntime(ctx, operation); saveErr != nil {
		return saveErr
	}
	s.mu.Lock()
	s.computes[allocation.ID] = allocation
	s.mu.Unlock()
	return cause
}

func computeAllocationOperationPayload(allocation ComputeAllocation, prepared ComputeAllocationPreparation) map[string]any {
	payload := map[string]any{"resource": allocation, "providerResourceId": allocation.ProviderResourceID, "nodeName": allocation.NodeName, "instanceId": firstNonEmpty(allocation.CVMInstanceID, allocation.InstanceID), "costTags": allocation.CostTags}
	if prepared.NodePoolID != "" {
		payload["allocationPlan"] = prepared
	}
	return payload
}

func decodeComputeAllocationPlan(operation FabricOperation) (ComputeAllocationPreparation, bool) {
	value, ok := operation.RedactedProviderPayload["allocationPlan"]
	if !ok {
		return ComputeAllocationPreparation{}, false
	}
	body, err := json.Marshal(value)
	if err != nil {
		return ComputeAllocationPreparation{}, false
	}
	var prepared ComputeAllocationPreparation
	if json.Unmarshal(body, &prepared) != nil {
		return ComputeAllocationPreparation{}, false
	}
	return prepared, prepared.NodePoolID != ""
}

func replayComputeAllocationOperation(operation FabricOperation, requestHash string) (ComputeAllocation, error) {
	if operation.RequestHash != requestHash {
		return ComputeAllocation{}, ErrComputeIdempotencyConflict
	}
	var allocation ComputeAllocation
	if !decodeOperationResource(operation, &allocation) {
		return ComputeAllocation{}, ErrComputeOperationFailed
	}
	if operation.Status == "started" || operation.Status == "claim_pending" || operation.Status == "succeeded" {
		return allocation, nil
	}
	return allocation, ErrComputeOperationFailed
}

func (s *Service) GetComputeAllocation(_ context.Context, allocationID string) (ComputeAllocation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	allocation, ok := s.computes[allocationID]
	return allocation, ok
}

func (s *Service) SyncComputeAllocation(ctx context.Context, allocationID string) (ComputeAllocation, error) {
	s.mu.Lock()
	existing := s.computes[allocationID]
	s.mu.Unlock()
	if existing.ID == "" {
		operation := newOperation("sync_compute_allocation", "compute_allocation", allocationID, "", "", "", hashInput(map[string]string{"id": allocationID}), time.Now().UTC())
		operation.ProviderRequestID = providerRequestID("sync-compute", allocationID)
		err := fmt.Errorf("compute_allocation_not_found")
		_ = s.recordOperation(ctx, operation, "rejected", ComputeAllocation{ID: allocationID}, err)
		return ComputeAllocation{}, err
	}
	if existing.Status == "provisioning" && (existing.MachineName == "" || firstNonEmpty(existing.InstanceID, existing.CVMInstanceID) == "" || existing.NodeName == "") {
		operations, err := s.operations.List(ctx)
		if err != nil {
			return existing, err
		}
		for index := len(operations) - 1; index >= 0; index-- {
			operation := operations[index]
			if operation.Action != "create_compute_allocation" || operation.ResourceID != allocationID {
				continue
			}
			if operation.Status == "started" {
				return existing, nil
			}
			if operation.Status == "succeeded" {
				if !decodeOperationResource(operation, &existing) || existing.MachineName == "" || firstNonEmpty(existing.InstanceID, existing.CVMInstanceID) == "" || existing.NodeName == "" {
					return existing, fmt.Errorf("compute_machine_identity_required")
				}
				s.mu.Lock()
				s.computes[allocationID] = existing
				s.mu.Unlock()
			}
			break
		}
	}
	if existing.Status == "failed" && existing.NodePoolID == "" && existing.MachineName == "" && existing.InstanceID == "" {
		return existing, nil
	}
	operation := newOperation("sync_compute_allocation", "compute_allocation", allocationID, existing.AccountID, existing.WorkspaceID, "", hashInput(existing), time.Now().UTC())
	if err := s.recordOperation(ctx, operation, "started", existing, nil); err != nil {
		return ComputeAllocation{}, err
	}
	allocation, err := s.provider.SyncComputeAllocation(ctx, existing)
	if err != nil {
		_ = s.recordOperation(ctx, operation, "failed", allocation, err)
		return allocation, err
	}
	if allocation.ID == "" {
		allocation.ID = existing.ID
	}
	if allocation.AccountID == "" {
		allocation.AccountID = existing.AccountID
	}
	if allocation.WorkspaceID == "" {
		allocation.WorkspaceID = existing.WorkspaceID
	}
	if allocation.PackageID == "" {
		allocation.PackageID = existing.PackageID
	}
	if allocation.Provider == "" {
		allocation.Provider = firstNonEmpty(existing.Provider, s.provider.Descriptor().Name)
	}
	if isExternallyDeletedComputeStatus(allocation.Status) {
		if err := s.releaseMachineOwnership(ctx, allocationID); err != nil {
			_ = s.recordOperation(ctx, operation, "failed", allocation, err)
			return allocation, err
		}
	} else if isReadyResourceStatus(allocation.Status) {
		ownership, ownershipErr := s.operations.MachineOwnership(ctx, allocationID)
		if ownershipErr != nil && ownershipErr != ErrMachineOwnershipNotFound {
			_ = s.recordOperation(ctx, operation, "failed", allocation, ownershipErr)
			return allocation, ownershipErr
		}
		if ownershipErr == nil && (ownership.Status == "claimed" || ownership.Status == "quarantined") {
			allocation.Status = "compute_claim_pending"
		}
	}
	if err := s.recordOperation(ctx, operation, "succeeded", allocation, nil); err != nil {
		return allocation, err
	}
	s.mu.Lock()
	s.computes[allocationID] = allocation
	s.mu.Unlock()
	return allocation, nil
}

func (s *Service) RenewComputeAllocation(ctx context.Context, allocationID, idempotencyKey string) (ComputeAllocation, error) {
	if strings.TrimSpace(allocationID) == "" || strings.TrimSpace(idempotencyKey) == "" {
		return ComputeAllocation{}, fmt.Errorf("compute_renew_identity_required")
	}
	var result ComputeAllocation
	err := s.operations.WithPoolLock(ctx, "compute-renew:"+allocationID, func(lockCtx context.Context) error {
		s.mu.Lock()
		existing := s.computes[allocationID]
		s.mu.Unlock()
		if !validComputeRenewalIdentity(existing) {
			return fmt.Errorf("compute_allocation_renew_identity_required")
		}
		requestHash := hashInput(map[string]string{"id": allocationID})
		operations, err := s.operations.List(lockCtx)
		if err != nil {
			return err
		}
		operation := newOperation("renew_compute_allocation", "compute_allocation", allocationID, existing.AccountID, existing.WorkspaceID, idempotencyKey, requestHash, s.now())
		started := false
		for _, candidate := range operations {
			if candidate.Action != operation.Action || candidate.IdempotencyKey != idempotencyKey {
				continue
			}
			if candidate.RequestHash != requestHash {
				return fmt.Errorf("compute_renew_idempotency_conflict")
			}
			if candidate.Status == "succeeded" && decodeOperationResource(candidate, &result) {
				return nil
			}
			operation = candidate
			started = true
		}
		if !started {
			if err := s.recordOperation(lockCtx, operation, "started", existing, nil); err != nil {
				return err
			}
		}
		request := existing
		request.ProviderData = maps.Clone(existing.ProviderData)
		request.CostTags = maps.Clone(existing.CostTags)
		result, err = s.provider.RenewComputeAllocation(lockCtx, request)
		if err != nil {
			_ = s.recordOperation(lockCtx, operation, "failed", result, err)
			return err
		}
		if !validComputeRenewal(existing, result) {
			err = fmt.Errorf("compute_renewal_readback_mismatch")
			result = existing
			_ = s.recordOperation(lockCtx, operation, "failed", result, err)
			return err
		}
		if err := s.recordOperation(lockCtx, operation, "succeeded", result, nil); err != nil {
			return err
		}
		s.mu.Lock()
		s.computes[allocationID] = result
		s.mu.Unlock()
		return nil
	})
	return result, err
}

func (s *Service) DestroyComputeAllocation(ctx context.Context, allocationID string) (ComputeAllocation, error) {
	s.mu.Lock()
	existing := s.computes[allocationID]
	s.mu.Unlock()
	if existing.ID == "" {
		operation := newOperation("destroy_compute_allocation", "compute_allocation", allocationID, "", "", "", hashInput(map[string]string{"id": allocationID}), time.Now().UTC())
		operation.ProviderRequestID = providerRequestID("destroy-compute", allocationID)
		err := fmt.Errorf("compute_allocation_not_found")
		_ = s.recordOperation(ctx, operation, "rejected", ComputeAllocation{ID: allocationID}, err)
		return ComputeAllocation{}, err
	}
	plan := packagePlan(firstNonEmpty(existing.PackageID, "basic"))
	operation := newOperation("destroy_compute_allocation", "compute_allocation", allocationID, existing.AccountID, existing.WorkspaceID, "", hashInput(map[string]string{"id": allocationID}), time.Now().UTC())
	allocation := existing
	startWorker := false
	err := s.operations.WithPoolLock(ctx, "compute-destroy:"+allocationID, func(lockCtx context.Context) error {
		latest, found, err := s.latestComputeDestroyOperation(lockCtx, allocationID)
		if err != nil {
			return err
		}
		if found && (latest.Status == "started" || latest.Status == "succeeded") {
			operation = latest
			_ = decodeOperationResource(latest, &allocation)
			if latest.Status == "succeeded" {
				return nil
			}
			s.mu.Lock()
			startWorker = !s.destroying[allocationID]
			s.destroying[allocationID] = true
			s.mu.Unlock()
			return nil
		}
		if !isExternallyDeletedComputeStatus(allocation.Status) {
			allocation.Status = "destroying"
		}
		if err := s.recordOperation(lockCtx, operation, "started", allocation, nil); err != nil {
			return err
		}
		s.mu.Lock()
		s.computes[allocationID] = allocation
		s.destroying[allocationID] = true
		s.mu.Unlock()
		startWorker = true
		return nil
	})
	if err != nil {
		return allocation, err
	}
	if startWorker {
		go s.finishDestroyComputeAllocation(operation, allocation, plan)
	}
	return allocation, nil
}

func (s *Service) finishDestroyComputeAllocation(operation FabricOperation, existing ComputeAllocation, plan plan) {
	ctx := context.Background()
	allocation := existing
	err := s.operations.WithPoolLock(ctx, plan.ID+":"+plan.InstanceType, func(lockCtx context.Context) error {
		if latest, found, err := s.latestComputeDestroyOperation(lockCtx, existing.ID); err != nil {
			return err
		} else if found && latest.Status == "succeeded" {
			_ = decodeOperationResource(latest, &allocation)
			return nil
		}
		s.mu.Lock()
		current := s.computes[existing.ID]
		s.mu.Unlock()
		var providerErr error
		allocation, providerErr = s.provider.DestroyComputeAllocation(lockCtx, current)
		if providerErr != nil {
			return providerErr
		}
		if err := s.releaseMachineOwnership(lockCtx, existing.ID); err != nil {
			return err
		}
		return s.cancelPendingComputeCreation(lockCtx, existing.ID, allocation)
	})
	if err != nil {
		if allocation.ID == "" {
			allocation = existing
		}
		allocation.Status = "destroying"
		_ = s.recordOperation(ctx, operation, "failed", allocation, err)
	} else {
		_ = s.recordOperation(ctx, operation, "succeeded", allocation, nil)
		s.mu.Lock()
		s.computes[existing.ID] = allocation
		s.mu.Unlock()
	}
	s.mu.Lock()
	delete(s.destroying, existing.ID)
	s.mu.Unlock()
}

func (s *Service) releaseMachineOwnership(ctx context.Context, resourceID string) error {
	ownership, err := s.operations.MachineOwnership(ctx, resourceID)
	if err == ErrMachineOwnershipNotFound {
		return nil
	}
	if err != nil || ownership.Status == "released" {
		return err
	}
	now := s.now()
	ownership.Status = "released"
	ownership.ReleasedAt = &now
	return s.operations.SaveMachineOwnership(ctx, ownership)
}

func isExternallyDeletedComputeStatus(status string) bool {
	switch status {
	case "external_deleted", "deleted", "missing":
		return true
	default:
		return false
	}
}

func (s *Service) latestComputeDestroyOperation(ctx context.Context, allocationID string) (FabricOperation, bool, error) {
	operations, err := s.operations.List(ctx)
	if err != nil {
		return FabricOperation{}, false, err
	}
	for index := len(operations) - 1; index >= 0; index-- {
		if operations[index].Action == "destroy_compute_allocation" && operations[index].ResourceID == allocationID {
			return operations[index], true, nil
		}
	}
	return FabricOperation{}, false, nil
}

func (s *Service) cancelPendingComputeCreation(ctx context.Context, allocationID string, allocation ComputeAllocation) error {
	operations, err := s.operations.List(ctx)
	if err != nil {
		return err
	}
	latest := FabricOperation{}
	for _, candidate := range operations {
		if candidate.Action == "create_compute_allocation" && candidate.ResourceID == allocationID {
			latest = candidate
		}
	}
	if latest.Status != "started" && latest.Status != "canceling" {
		return nil
	}
	return s.recordOperation(ctx, latest, "failed", allocation, fmt.Errorf("compute_create_canceled"))
}
func validComputeRenewal(existing, renewed ComputeAllocation) bool {
	instanceID := firstNonEmpty(existing.InstanceID, existing.CVMInstanceID)
	if !validComputeRenewalIdentity(existing) || !validComputeRenewalIdentity(renewed) || renewed.ProviderData["instanceType"] != existing.ProviderData["instanceType"] || renewed.ProviderData["zone"] != existing.ProviderData["zone"] {
		return false
	}
	for _, key := range []string{"opl_account_id", "opl_workspace_id", "opl_resource_id", "opl_operation_id"} {
		if renewed.CostTags[key] != existing.CostTags[key] {
			return false
		}
	}
	return renewed.ID == existing.ID && renewed.AccountID == existing.AccountID && renewed.WorkspaceID == existing.WorkspaceID &&
		firstNonEmpty(renewed.InstanceID, renewed.CVMInstanceID) == instanceID &&
		(renewed.InstanceID == "" || renewed.InstanceID == instanceID) && (renewed.CVMInstanceID == "" || renewed.CVMInstanceID == instanceID) &&
		renewed.ChargeType == "PREPAID" && renewed.RenewFlag == "NOTIFY_AND_MANUAL_RENEW" && renewalDeadlineIncreased(existing.Deadline, renewed.Deadline)
}

func validComputeRenewalIdentity(allocation ComputeAllocation) bool {
	instanceID := firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID)
	if allocation.ID == "" || allocation.AccountID == "" || allocation.WorkspaceID == "" || !strings.HasPrefix(instanceID, "ins-") || strings.TrimSpace(allocation.Deadline) == "" || strings.TrimSpace(allocation.ProviderData["instanceType"]) == "" || strings.TrimSpace(allocation.ProviderData["zone"]) == "" {
		return false
	}
	return allocation.CostTags["opl_account_id"] == allocation.AccountID && allocation.CostTags["opl_workspace_id"] == allocation.WorkspaceID && allocation.CostTags["opl_resource_id"] == allocation.ID && strings.TrimSpace(allocation.CostTags["opl_operation_id"]) != ""
}
