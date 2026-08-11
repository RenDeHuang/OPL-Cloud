package fabric

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"strings"
	"sync"
	"time"
)

const (
	storageProvisionTimeout          = 10 * time.Minute
	computeAllocationPollInterval    = 10 * time.Second
	computeAllocationPollWindow      = 10 * time.Minute
	computeAllocationAttemptTimeout  = 30 * time.Second
	computeAllocationFinalizeTimeout = 2 * time.Minute
	providerFactsBatchTimeout        = 5 * time.Second
	providerFactsBatchWorkerCount    = 8
	runtimeHealthSummaryTimeout      = 5 * time.Second
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

type Service struct {
	provider                         Provider
	mu                               sync.Mutex
	jobMu                            sync.Mutex
	computes                         map[string]ComputeAllocation
	volumes                          map[string]StorageVolume
	snapshots                        map[string]StorageSnapshot
	attachments                      map[string]StorageAttachment
	destroying                       map[string]bool
	reconciling                      map[string]bool
	operations                       OperationStore
	now                              func() time.Time
	computeAllocationPollInterval    time.Duration
	computeAllocationPollWindow      time.Duration
	computeAllocationAttemptTimeout  time.Duration
	computeAllocationFinalizeTimeout time.Duration
}

const runtimeClaimStaleAfter = 2 * time.Minute

func (s *Service) claimRuntimeOperation(ctx context.Context, operation FabricOperation) (FabricOperation, bool, error) {
	stored, claimed, err := s.operations.ClaimRuntime(ctx, operation)
	// A stale runtime operation is never reclaimed into a new provider lease.
	// The caller must first prove the already-attempted resource by readback and
	// use the dedicated CAS path below.  This is what keeps a lost response from
	// becoming a second apply/patch.
	return stored, claimed, err
}

func runtimeOperationNeedsReadback(operation FabricOperation, now time.Time) bool {
	if operation.Status == "failed" {
		return true
	}
	return operation.Status == "started" && !operation.StartedAt.IsZero() && !now.Before(operation.StartedAt.Add(runtimeClaimStaleAfter))
}

func (s *Service) convergeRuntimeOperationReadback(ctx context.Context, expected FabricOperation, resource any, extra map[string]any) (FabricOperation, error) {
	converger, ok := s.operations.(runtimeReadbackConverger)
	if !ok {
		return FabricOperation{}, ErrRuntimeOperationNotCurrent
	}
	next := expected
	next.Status = "succeeded"
	next.FinishedAt = s.now()
	next.ErrorCode = ""
	next.Retryable = false
	fillOperationResource(&next, resource)
	if len(extra) > 0 {
		payload := maps.Clone(next.RedactedProviderPayload)
		if payload == nil {
			payload = map[string]any{}
		}
		for key, value := range extra {
			payload[key] = value
		}
		next.RedactedProviderPayload = payload
	}
	if err := converger.ConvergeRuntimeReadback(ctx, expected, next); err != nil {
		return FabricOperation{}, err
	}
	return next, nil
}

func attachmentReadbackMatches(result StorageAttachment, input StorageAttachmentInput, compute ComputeAllocation, volume StorageVolume) bool {
	return strings.HasPrefix(result.ID, "att_") && result.OperationID == input.IdempotencyKey &&
		result.WorkspaceID == input.WorkspaceID && result.ComputeID == input.ComputeID && result.VolumeID == input.VolumeID &&
		result.Status == "attached" && result.ProviderAttachmentID != "" && result.ProviderRequestID != "" &&
		compute.AccountID != "" && compute.WorkspaceID == input.WorkspaceID && volume.AccountID == compute.AccountID && volume.WorkspaceID == input.WorkspaceID
}

func runtimeReadbackMatches(result WorkspaceRuntime, input WorkspaceRuntimeInput) bool {
	return strings.HasPrefix(result.ID, "rt_") && result.OperationID == input.RuntimeOperationID &&
		result.WorkspaceID == input.WorkspaceID && (result.Status == "running" || result.Status == "unready") && result.ServiceName != "" &&
		result.ImageID == input.ImageID
}

func gatewaySecretReadbackMatches(result GatewaySecret, input GatewaySecretInput) bool {
	return result.SecretRef == gatewaySecretName(input.WorkspaceID) && result.Fingerprint == input.Fingerprint &&
		result.Version != "" && strings.TrimSpace(result.Version) == result.Version
}

func NewService(provider Provider) *Service {
	return NewServiceWithOperationStore(provider, NewMemoryOperationStore())
}

func NewServiceWithOperationStore(provider Provider, operations OperationStore) *Service {
	if operations == nil {
		operations = NewMemoryOperationStore()
	}
	computes, volumes, snapshots, attachments, _ := replayResourceState(context.Background(), operations)
	return &Service{
		provider: provider, computes: computes, volumes: volumes, snapshots: snapshots, attachments: attachments,
		destroying: map[string]bool{}, reconciling: map[string]bool{}, operations: operations,
		now:                           func() time.Time { return time.Now().UTC() },
		computeAllocationPollInterval: computeAllocationPollInterval, computeAllocationPollWindow: computeAllocationPollWindow,
		computeAllocationAttemptTimeout: computeAllocationAttemptTimeout, computeAllocationFinalizeTimeout: computeAllocationFinalizeTimeout,
	}
}

func (s *Service) Catalog(_ context.Context) Catalog {
	return s.provider.Descriptor().Catalog
}

func (s *Service) MonthlyPreflight(ctx context.Context, input MonthlyPreflightInput) (MonthlyPreflight, error) {
	if (input.ResourceType != "compute" && input.ResourceType != "storage") || (input.PackageID != "basic" && input.PackageID != "pro") || input.Zone == "" || input.Zone != strings.TrimSpace(input.Zone) ||
		(input.ResourceType == "compute" && input.SizeGB != 0) || (input.ResourceType == "storage" && (input.SizeGB < 10 || input.SizeGB%10 != 0)) {
		return MonthlyPreflight{}, ErrInvalidMonthlyPreflight
	}
	result, err := s.provider.MonthlyPreflight(ctx, input)
	if err != nil {
		return MonthlyPreflight{}, fmt.Errorf("%w: %v", ErrMonthlyPreflightUnavailable, err)
	}
	requiredRequestIDs := []string{"nodePool", "subnets", "availability", "quota"}
	if input.ResourceType == "storage" {
		requiredRequestIDs = []string{"quota", "price"}
	}
	validRequestIDs := len(result.ProviderRequestIDs) > 0
	for _, key := range requiredRequestIDs {
		validRequestIDs = validRequestIDs && strings.TrimSpace(result.ProviderRequestIDs[key]) != ""
	}
	pricingValid := !s.provider.Descriptor().RequiresMonthlyPricing ||
		result.ChargeType == "PREPAID" && result.PeriodMonths == 1 && result.RenewFlag == "NOTIFY_AND_MANUAL_RENEW" && result.ProviderPriceCNY > 0
	if result.ResourceType != input.ResourceType || result.PackageID != input.PackageID || result.SizeGB != input.SizeGB || result.Zone != input.Zone || !result.Available ||
		!pricingValid || math.IsNaN(result.ProviderPriceCNY) || math.IsInf(result.ProviderPriceCNY, 0) || !validRequestIDs ||
		(input.ResourceType == "compute" && strings.TrimSpace(result.NodePoolID) == "") {
		return MonthlyPreflight{}, ErrMonthlyPreflightUnavailable
	}
	return result, nil
}

func workspaceLaunchResourceLockKey(launchOperationID string) string {
	return "workspace-launch-resources:" + strings.TrimSpace(launchOperationID)
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
	if input.LaunchBinding != nil {
		requestHash = input.LaunchBinding.RequestHash
	}
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
	if err := bindLaunchStageOperation(&operation, input.LaunchBinding); err != nil {
		return ComputeAllocation{}, err
	}
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

func automaticComputeClaimRecoveryInput(operation FabricOperation, allocation ComputeAllocation, plan ComputeAllocationPreparation) (ComputeClaimRecoveryClaimInput, bool) {
	launchBinding, ok := decodeLaunchStageBinding(operation)
	if !ok || launchBinding.Stage != "ensure_compute_allocation" || allocation.ID != operation.ResourceID {
		return ComputeClaimRecoveryClaimInput{}, false
	}
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: ComputeClaimRecoveryInput{
			LaunchOperationID: launchBinding.LaunchOperationID, AccountID: allocation.AccountID, WorkspaceID: allocation.WorkspaceID,
			ComputeAllocationID: allocation.ID, StorageVolumeID: "vol_" + stableID("vol", allocation.AccountID, launchBinding.LaunchOperationID, "storage")[:18],
			PackageID: allocation.PackageID, PoolID: plan.PoolID, NodePoolID: plan.NodePoolID,
		},
		MachineName: allocation.MachineName, NodeName: allocation.NodeName,
		CVMInstanceID: firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID), PrivateIP: allocation.PrivateIP,
		InstanceType: plan.InstanceType, Zone: allocation.Zone, IdempotencyKey: operation.IdempotencyKey,
	}
	if !validComputeClaimRecoveryInput(claimInput.ComputeClaimRecoveryInput) || claimInput.IdempotencyKey != launchBinding.IdempotencyKey ||
		!strings.HasPrefix(claimInput.CVMInstanceID, "ins-") {
		return ComputeClaimRecoveryClaimInput{}, false
	}
	for _, value := range []string{claimInput.MachineName, claimInput.NodeName, claimInput.CVMInstanceID, claimInput.PrivateIP, claimInput.InstanceType, claimInput.Zone, claimInput.IdempotencyKey} {
		if value == "" || value != strings.TrimSpace(value) {
			return ComputeClaimRecoveryClaimInput{}, false
		}
	}
	if launchBinding.LaunchOperationID == "" || launchBinding.AccountID != allocation.AccountID || launchBinding.WorkspaceID != allocation.WorkspaceID {
		return ComputeClaimRecoveryClaimInput{}, false
	}
	return claimInput, true
}

func automaticComputeClaimRecoveryBinding(operation FabricOperation, allocation ComputeAllocation, plan ComputeAllocationPreparation) (computeClaimRecoveryBinding, bool) {
	claimInput, ok := automaticComputeClaimRecoveryInput(operation, allocation, plan)
	if !ok {
		return computeClaimRecoveryBinding{}, false
	}
	return newComputeClaimRecoveryBinding(claimInput), true
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

func validateNewComputeAllocation(allocation ComputeAllocation, prepared ComputeAllocationPreparation) error {
	if prepared.NodePoolID == "" || allocation.NodePoolID != prepared.NodePoolID || allocation.PoolID != prepared.PoolID || allocation.PackageID != prepared.PackageID ||
		allocation.InstanceType != prepared.InstanceType || allocation.MachineName == "" || !strings.HasPrefix(firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID), "ins-") ||
		allocation.NodeName == "" || allocation.PrivateIP == "" || allocation.Zone == "" || allocation.ChargeType != "PREPAID" ||
		allocation.RenewFlag != "NOTIFY_AND_MANUAL_RENEW" || allocation.Deadline == "" {
		return fmt.Errorf("compute_provider_readback_mismatch")
	}
	for _, existing := range prepared.BeforeMachineNames {
		if allocation.MachineName == existing {
			return fmt.Errorf("compute_allocation_machine_not_new")
		}
	}
	return nil
}

func (s *Service) readComputeAllocationAfterReservation(ctx context.Context, allocation ComputeAllocation, prepared ComputeAllocationPreparation, dryRun bool) (ComputeAllocation, error) {
	if reader, ok := s.provider.(computeAllocationDiscoveryProvider); ok {
		return reader.DiscoverComputeAllocation(ctx, allocation, prepared)
	}
	if reader, ok := s.provider.(computeAllocationReadbackProvider); ok {
		return reader.ReadComputeAllocation(ctx, allocation)
	}
	// Legacy providers can only read back after they have returned a complete
	// identity. They must never be asked to create/scale as a replay fallback.
	if allocation.MachineName == "" || firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID) == "" || allocation.NodeName == "" {
		return allocation, ErrComputeAllocationPending
	}
	return s.provider.SyncComputeAllocation(ctx, allocation)
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

// terminalizeComputeClaimPending records an unprovable claim as a failed,
// quarantined operation.  The payload is cloned so existing stage budgets,
// mutation ledger, and binding remain part of the CAS identity and no provider
// write is retried by a replay.
func terminalizeComputeClaimPending(ctx context.Context, s *Service, operation FabricOperation, allocation ComputeAllocation, prepared ComputeAllocationPreparation, stage, readbackStatus string, cause error, proof *ComputeClaimProviderProof) error {
	return terminalizeComputeClaimPendingWithApproval(ctx, s, operation, allocation, prepared, stage, readbackStatus, cause, proof, nil)
}

func terminalizeComputeClaimPendingWithApproval(ctx context.Context, s *Service, operation FabricOperation, allocation ComputeAllocation, prepared ComputeAllocationPreparation, stage, readbackStatus string, cause error, proof *ComputeClaimProviderProof, approval *ComputePoolHeadTerminalizationInput) error {
	if operation.Status != "claim_pending" || allocation.ID == "" {
		return cause
	}
	cvmBudget, cvmPresent, cvmValid := normalLaunchStageBudget(operation.RedactedProviderPayload, "compute_claim_cvm")
	nodeBudget, nodePresent, nodeValid := normalLaunchStageBudget(operation.RedactedProviderPayload, "compute_claim_node")
	if !cvmPresent || !cvmValid {
		cvmBudget = normalLaunchMutationBudget{}
	}
	if !nodePresent || !nodeValid {
		nodeBudget = normalLaunchMutationBudget{}
	}
	binding, bindingPresent, bindingValid := decodeComputeClaimRecoveryBinding(operation)
	if !bindingPresent || !bindingValid {
		binding = terminalComputeClaimBinding(operation, allocation, prepared)
	}
	now := s.now()
	evidence := terminalComputeClaimEvidence(operation, allocation, prepared, stage, readbackStatus, cause, cvmBudget, nodeBudget, now, binding, proof)
	if approval != nil {
		_, _, ledgerDigest := computeClaimMutationLedgerEvidence(operation)
		evidence.OperatorApprovalID, evidence.OperatorApprovalDigest = approval.ApprovalID, approval.ApprovalDigest
		evidence.OperatorIdempotencyKey, evidence.ManualRecoveryLedgerDigest = approval.IdempotencyKey, ledgerDigest
	}
	evidence.StorageVolumeID = "vol_" + stableID("vol", allocation.AccountID, evidence.LaunchOperationID+":storage")[:18]
	evidence.PoolID = prepared.PoolID
	allocation.Status = "quarantined"
	if allocation.ProviderData == nil {
		allocation.ProviderData = map[string]string{}
	}
	allocation.ProviderData["recoveryAction"] = "manual_review"
	allocation.ClaimTerminalEvidence = &evidence
	next := operation
	next.Status, next.ErrorCode, next.Retryable, next.FinishedAt = "failed", evidence.ErrorCode, false, now
	next.ProviderRequestID = firstNonEmpty(allocation.ProviderRequestID, next.ProviderRequestID)
	payload := maps.Clone(operation.RedactedProviderPayload)
	if payload == nil {
		payload = map[string]any{}
	}
	payload["resource"] = allocation
	payload["providerResourceId"] = allocation.ProviderResourceID
	payload["nodeName"] = allocation.NodeName
	payload["instanceId"] = firstNonEmpty(allocation.CVMInstanceID, allocation.InstanceID)
	payload["costTags"] = allocation.CostTags
	if prepared.NodePoolID != "" {
		payload["allocationPlan"] = prepared
	}
	if !bindingPresent || !bindingValid {
		payload = withComputeClaimRecoveryBinding(payload, binding)
	}
	payload = withComputeClaimTerminalEvidence(payload, evidence)
	next.RedactedProviderPayload = payload
	if err := s.operations.SaveComputeClaimRecovery(ctx, operation, next); err != nil {
		return err
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

func (s *Service) CreateStorageVolume(ctx context.Context, input StorageVolumeInput) (StorageVolume, error) {
	input.AllowExistingExactReplay = false
	if input.SizeGB < 10 || input.SizeGB%10 != 0 {
		return StorageVolume{}, ErrInvalidStorageSize
	}
	if !validStorageRecoveryExpectation(input.ExpectedRecoveryState, input.ExpectedProviderResourceID) {
		return StorageVolume{}, fmt.Errorf("storage_recovery_expectation_invalid")
	}
	if input.ID == "" {
		if strings.TrimSpace(input.IdempotencyKey) == "" {
			return StorageVolume{}, fmt.Errorf("storage_idempotency_key_required")
		}
		input.ID = "vol_" + stableSuffix("create_storage_volume", input.IdempotencyKey)[:16]
	}
	s.mu.Lock()
	compute := s.computes[input.ComputeID]
	s.mu.Unlock()
	computeZone := strings.TrimSpace(compute.Zone)
	if input.LaunchBinding == nil && computeZone == "" {
		computeZone = strings.TrimSpace(compute.ProviderData["zone"])
	}
	if compute.ID == "" || compute.AccountID != input.AccountID || compute.WorkspaceID != input.WorkspaceID ||
		!isReadyResourceStatus(compute.Status) || computeZone == "" || strings.TrimSpace(input.Zone) != computeZone {
		return StorageVolume{}, fmt.Errorf("storage_compute_zone_mismatch")
	}
	requestHash := hashInput(input)
	if input.LaunchBinding != nil {
		requestHash = input.LaunchBinding.RequestHash
	}
	var volume StorageVolume
	lockKey := "storage-create:" + firstNonEmpty(input.IdempotencyKey, input.ID)
	if input.LaunchBinding != nil {
		lockKey = workspaceLaunchResourceLockKey(input.LaunchBinding.LaunchOperationID)
	}
	err := s.operations.WithPoolLock(ctx, lockKey, func(lockCtx context.Context) error {
		var err error
		operation := newOperation("create_storage_volume", "storage_volume", input.ID, input.AccountID, input.WorkspaceID, input.IdempotencyKey, requestHash, s.now())
		if err := bindLaunchStageOperation(&operation, input.LaunchBinding); err != nil {
			return err
		}
		typedLaunch := input.LaunchBinding != nil
		if typedLaunch {
			stored, getErr := s.operations.Get(lockCtx, operation.ID)
			switch {
			case getErr == nil:
				persisted, ok := decodeLaunchStageBinding(stored)
				if !ok || persisted != *input.LaunchBinding || stored.Action != operation.Action || stored.ResourceKind != operation.ResourceKind ||
					stored.ResourceID != operation.ResourceID || stored.RequestHash != requestHash {
					return ErrLaunchStageBindingConflict
				}
				if stored.Status == "succeeded" && decodeOperationResource(stored, &volume) {
					return nil
				}
				if stored.Status != "started" && stored.Status != "failed" {
					return ErrLaunchStageBindingConflict
				}
				operation = stored
				input.AllowExistingExactReplay = true
			case errors.Is(getErr, ErrOperationNotFound):
				operation.Status, operation.CreatedAt = "started", s.now()
				fillOperationResource(&operation, StorageVolume{
					ID: input.ID, OperationID: input.IdempotencyKey, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID,
					Provider: s.provider.Descriptor().Name, ProviderRequestID: providerRequestID("storage", input.IdempotencyKey),
				})
				if err := s.operations.Append(lockCtx, operation); err != nil {
					return err
				}
			default:
				return getErr
			}
		} else {
			operations, err := s.operations.List(lockCtx)
			if err != nil {
				return err
			}
			for index := len(operations) - 1; index >= 0; index-- {
				candidate := operations[index]
				if candidate.Action != "create_storage_volume" || candidate.IdempotencyKey != input.IdempotencyKey || candidate.ResourceID != input.ID {
					continue
				}
				if candidate.RequestHash != requestHash {
					return fmt.Errorf("storage_create_idempotency_conflict")
				}
				if candidate.Status == "succeeded" && decodeOperationResource(candidate, &volume) {
					return nil
				}
				input.AllowExistingExactReplay = true
				break
			}
		}
		input.OperationID = operation.OperationID
		if !typedLaunch {
			if err := s.recordOperation(lockCtx, operation, "started", StorageVolume{ID: input.ID, OperationID: input.IdempotencyKey, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Provider: s.provider.Descriptor().Name, ProviderRequestID: providerRequestID("storage", input.IdempotencyKey)}, nil); err != nil {
				return err
			}
		}
		volume, err = s.provider.CreateStorageVolume(s.providerMutationContext(lockCtx, operation), input)
		volume.ID = input.ID
		volume.OperationID = input.IdempotencyKey
		volume.AccountID = firstNonEmpty(volume.AccountID, input.AccountID)
		volume.WorkspaceID = firstNonEmpty(volume.WorkspaceID, input.WorkspaceID)
		volume.Provider = firstNonEmpty(volume.Provider, s.provider.Descriptor().Name)
		volume.Zone = firstNonEmpty(volume.Zone, input.Zone)
		if volume.SizeGB == 0 {
			volume.SizeGB = input.SizeGB
		}
		if err != nil {
			knownCBS := strings.HasPrefix(volume.ProviderResourceID, "disk-")
			if knownCBS {
				volume.Status = "quarantined"
			}
			if typedLaunch {
				if operation.Status == "started" {
					next := operation
					next.Status, next.ErrorCode, next.FinishedAt = "failed", errorCode(err), s.now()
					fillOperationResource(&next, volume)
					if saveErr := s.operations.SaveRuntime(lockCtx, next); saveErr != nil {
						return saveErr
					}
				}
			} else if recordErr := s.recordOperation(lockCtx, operation, "failed", volume, err); recordErr != nil {
				return recordErr
			}
			if knownCBS {
				s.mu.Lock()
				s.volumes[volume.ID] = volume
				s.mu.Unlock()
			}
			return err
		}
		if typedLaunch {
			next := operation
			next.Status, next.ErrorCode, next.FinishedAt = "succeeded", "", s.now()
			fillOperationResource(&next, volume)
			if operation.Status == "started" {
				if err := s.operations.SaveRuntime(lockCtx, next); err != nil {
					return err
				}
			} else {
				converger, ok := s.operations.(runtimeReadbackConverger)
				if !ok {
					return ErrRuntimeOperationNotCurrent
				}
				if err := converger.ConvergeRuntimeReadback(lockCtx, operation, next); err != nil {
					return err
				}
			}
		} else if err := s.recordOperation(lockCtx, operation, "succeeded", volume, nil); err != nil {
			return err
		}
		s.mu.Lock()
		s.volumes[volume.ID] = volume
		s.mu.Unlock()
		return nil
	})
	return volume, err
}

func validStorageRecoveryExpectation(state, providerResourceID string) bool {
	switch state {
	case "":
		return providerResourceID == ""
	case "storage_not_started":
		return providerResourceID == ""
	case "storage_existing_exact":
		return strings.HasPrefix(providerResourceID, "disk-")
	default:
		return false
	}
}

func (s *Service) GetStorageVolume(_ context.Context, volumeID string) (StorageVolume, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	volume, ok := s.volumes[volumeID]
	return volume, ok
}

func (s *Service) ReadStorageVolume(ctx context.Context, volumeID string) (StorageVolume, error) {
	s.mu.Lock()
	existing := cloneStorageVolume(s.volumes[volumeID])
	s.mu.Unlock()
	if existing.ID == "" {
		return StorageVolume{}, fmt.Errorf("storage_volume_not_found")
	}
	reader, ok := s.provider.(storageVolumeStatusReader)
	if !ok {
		return existing, nil
	}
	volume, err := reader.ReadStorageVolumeStatus(ctx, existing)
	if volume.ID == "" {
		volume.ID = existing.ID
	}
	if volume.AccountID == "" {
		volume.AccountID = existing.AccountID
	}
	if volume.WorkspaceID == "" {
		volume.WorkspaceID = existing.WorkspaceID
	}
	if volume.Provider == "" {
		volume.Provider = existing.Provider
	}
	if volume.ProviderResourceID == "" {
		volume.ProviderResourceID = existing.ProviderResourceID
	}
	if volume.ProviderRequestID == "" {
		volume.ProviderRequestID = existing.ProviderRequestID
	}
	return volume, err
}

func (s *Service) CreateStorageSnapshot(ctx context.Context, input StorageSnapshotInput) (StorageSnapshot, error) {
	if input.AccountID == "" || input.WorkspaceID == "" || input.VolumeID == "" || input.IdempotencyKey == "" {
		return StorageSnapshot{}, fmt.Errorf("storage_snapshot_input_required")
	}
	requestHash := hashInput(input)
	operations, err := s.operations.List(ctx)
	if err != nil {
		return StorageSnapshot{}, err
	}
	for _, operation := range operations {
		if operation.Action != "create_storage_snapshot" || operation.IdempotencyKey != input.IdempotencyKey {
			continue
		}
		if operation.RequestHash != requestHash {
			return StorageSnapshot{}, fmt.Errorf("storage_snapshot_idempotency_conflict")
		}
		var replayed StorageSnapshot
		if operation.Status == "succeeded" && decodeOperationResource(operation, &replayed) {
			return replayed, nil
		}
	}
	s.mu.Lock()
	volume := s.volumes[input.VolumeID]
	s.mu.Unlock()
	if volume.ID == "" || volume.Status != "ready" {
		return StorageSnapshot{}, fmt.Errorf("storage_volume_not_ready")
	}
	now := s.now()
	id := "snap-" + stableSuffix(input.WorkspaceID, input.VolumeID, input.IdempotencyKey)[:16]
	operation := newOperation("create_storage_snapshot", "storage_snapshot", id, input.AccountID, input.WorkspaceID, input.IdempotencyKey, requestHash, now)
	input.OperationID = operation.OperationID
	if err := s.recordOperation(ctx, operation, "started", StorageSnapshot{ID: id, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, VolumeID: input.VolumeID, Status: "creating", Provider: "tencent-tke", ProviderRequestID: providerRequestID("snapshot", input.IdempotencyKey), CreatedAt: now}, nil); err != nil {
		return StorageSnapshot{}, err
	}
	snapshot, err := s.provider.CreateStorageSnapshot(ctx, input, volume)
	if snapshot.ID == "" {
		snapshot.ID = id
	}
	if err != nil {
		_ = s.recordOperation(ctx, operation, "failed", snapshot, err)
		return snapshot, err
	}
	operation.ResourceID = snapshot.ID
	if err := s.recordOperation(ctx, operation, "succeeded", snapshot, nil); err != nil {
		return snapshot, err
	}
	s.mu.Lock()
	s.snapshots[snapshot.ID] = snapshot
	s.mu.Unlock()
	return snapshot, nil
}

func (s *Service) GetStorageSnapshot(_ context.Context, snapshotID string) (StorageSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, ok := s.snapshots[snapshotID]
	return snapshot, ok
}

func (s *Service) SyncStorageSnapshot(ctx context.Context, snapshotID string) (StorageSnapshot, error) {
	s.mu.Lock()
	snapshot := s.snapshots[snapshotID]
	s.mu.Unlock()
	if snapshot.ID == "" {
		return StorageSnapshot{}, fmt.Errorf("storage_snapshot_not_found")
	}
	operation := newOperation("sync_storage_snapshot", "storage_snapshot", snapshotID, snapshot.AccountID, snapshot.WorkspaceID, "", hashInput(map[string]string{"id": snapshotID}), s.now())
	if err := s.recordOperation(ctx, operation, "started", snapshot, nil); err != nil {
		return StorageSnapshot{}, err
	}
	synced, err := s.provider.SyncStorageSnapshot(ctx, snapshot)
	if err != nil {
		_ = s.recordOperation(ctx, operation, "failed", synced, err)
		return synced, err
	}
	if err := s.recordOperation(ctx, operation, "succeeded", synced, nil); err != nil {
		return synced, err
	}
	s.mu.Lock()
	s.snapshots[snapshotID] = synced
	s.mu.Unlock()
	return synced, nil
}

func (s *Service) RestoreStorageSnapshot(ctx context.Context, input StorageRestoreInput) (StorageVolume, error) {
	if input.SnapshotID == "" || input.AccountID == "" || input.WorkspaceID == "" || input.TargetVolumeID == "" || input.IdempotencyKey == "" {
		return StorageVolume{}, fmt.Errorf("storage_restore_input_required")
	}
	s.mu.Lock()
	snapshot := s.snapshots[input.SnapshotID]
	s.mu.Unlock()
	if snapshot.ID == "" || snapshot.Status != "ready" {
		return StorageVolume{}, fmt.Errorf("storage_snapshot_not_ready")
	}
	operation := newOperation("restore_storage_snapshot", "storage_volume", input.TargetVolumeID, input.AccountID, input.WorkspaceID, input.IdempotencyKey, hashInput(input), s.now())
	input.OperationID = operation.OperationID
	if err := s.recordOperation(ctx, operation, "started", StorageVolume{ID: input.TargetVolumeID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "restoring", Provider: snapshot.Provider, ProviderRequestID: providerRequestID("restore", input.IdempotencyKey)}, nil); err != nil {
		return StorageVolume{}, err
	}
	volume, err := s.provider.RestoreStorageSnapshot(ctx, input, snapshot)
	if err != nil {
		_ = s.recordOperation(ctx, operation, "failed", volume, err)
		return volume, err
	}
	if err := s.recordOperation(ctx, operation, "succeeded", volume, nil); err != nil {
		return volume, err
	}
	s.mu.Lock()
	s.volumes[volume.ID] = volume
	s.mu.Unlock()
	return volume, nil
}

func (s *Service) DestroyStorageSnapshot(ctx context.Context, snapshotID string) (StorageSnapshot, error) {
	s.mu.Lock()
	snapshot := s.snapshots[snapshotID]
	s.mu.Unlock()
	if snapshot.ID == "" {
		return StorageSnapshot{}, fmt.Errorf("storage_snapshot_not_found")
	}
	operation := newOperation("destroy_storage_snapshot", "storage_snapshot", snapshotID, snapshot.AccountID, snapshot.WorkspaceID, "", hashInput(map[string]string{"id": snapshotID}), s.now())
	if err := s.recordOperation(ctx, operation, "started", snapshot, nil); err != nil {
		return StorageSnapshot{}, err
	}
	destroyed, err := s.provider.DestroyStorageSnapshot(ctx, snapshot)
	if err != nil {
		_ = s.recordOperation(ctx, operation, "failed", destroyed, err)
		return destroyed, err
	}
	if err := s.recordOperation(ctx, operation, "succeeded", destroyed, nil); err != nil {
		return destroyed, err
	}
	s.mu.Lock()
	s.snapshots[snapshotID] = destroyed
	s.mu.Unlock()
	return destroyed, nil
}

func (s *Service) DestroyStorageVolume(ctx context.Context, volumeID string) (StorageVolume, error) {
	s.mu.Lock()
	existing := s.volumes[volumeID]
	s.mu.Unlock()
	if existing.ID == "" {
		operation := newOperation("destroy_storage_volume", "storage_volume", volumeID, "", "", "", hashInput(map[string]string{"id": volumeID}), time.Now().UTC())
		operation.ProviderRequestID = providerRequestID("destroy-storage", volumeID)
		err := fmt.Errorf("storage_volume_not_found")
		_ = s.recordOperation(ctx, operation, "rejected", StorageVolume{ID: volumeID}, err)
		return StorageVolume{}, err
	}
	operation := newOperation("destroy_storage_volume", "storage_volume", volumeID, existing.AccountID, existing.WorkspaceID, "", hashInput(map[string]string{"id": volumeID}), time.Now().UTC())
	if err := s.recordOperation(ctx, operation, "started", existing, nil); err != nil {
		return StorageVolume{}, err
	}
	volume, err := s.provider.DestroyStorageVolume(ctx, existing)
	if err != nil {
		_ = s.recordOperation(ctx, operation, "failed", volume, err)
		return volume, err
	}
	if err := s.recordOperation(ctx, operation, "succeeded", volume, nil); err != nil {
		return volume, err
	}
	s.mu.Lock()
	s.volumes[volumeID] = volume
	s.mu.Unlock()
	return volume, nil
}

func (s *Service) SyncStorageVolume(ctx context.Context, volumeID string) (StorageVolume, error) {
	s.mu.Lock()
	existing := s.volumes[volumeID]
	s.mu.Unlock()
	if existing.ID == "" {
		operation := newOperation("sync_storage_volume", "storage_volume", volumeID, "", "", "", hashInput(map[string]string{"id": volumeID}), time.Now().UTC())
		operation.ProviderRequestID = providerRequestID("sync-storage", volumeID)
		err := fmt.Errorf("storage_volume_not_found")
		_ = s.recordOperation(ctx, operation, "rejected", StorageVolume{ID: volumeID}, err)
		return StorageVolume{}, err
	}
	if isRetainedStorageStatus(existing.Status) {
		return existing, nil
	}
	operation := newOperation("sync_storage_volume", "storage_volume", volumeID, existing.AccountID, existing.WorkspaceID, "", hashInput(existing), time.Now().UTC())
	if err := s.recordOperation(ctx, operation, "started", existing, nil); err != nil {
		return StorageVolume{}, err
	}
	volume, err := s.provider.SyncStorageVolume(ctx, existing)
	if volume.ID == "" {
		volume.ID = existing.ID
	}
	if volume.AccountID == "" {
		volume.AccountID = existing.AccountID
	}
	if volume.WorkspaceID == "" {
		volume.WorkspaceID = existing.WorkspaceID
	}
	if volume.Provider == "" {
		volume.Provider = firstNonEmpty(existing.Provider, s.provider.Descriptor().Name)
	}
	if volume.ProviderResourceID == "" {
		volume.ProviderResourceID = existing.ProviderResourceID
	}
	if volume.ProviderRequestID == "" {
		volume.ProviderRequestID = existing.ProviderRequestID
	}
	if err != nil {
		_ = s.recordOperation(ctx, operation, "failed", volume, err)
		return volume, err
	}
	// A paid launch owns the original storage identity for its full recovery
	// window. A pending readback must never turn a timeout into a destructive
	// cleanup or a replacement resource; the caller's durable stage budget
	// decides whether to retry or move to manual review.
	if err := s.recordOperation(ctx, operation, "succeeded", volume, nil); err != nil {
		return volume, err
	}
	s.mu.Lock()
	s.volumes[volumeID] = volume
	s.mu.Unlock()
	return volume, nil
}

func (s *Service) RenewStorageVolume(ctx context.Context, volumeID, idempotencyKey string) (StorageVolume, error) {
	if strings.TrimSpace(volumeID) == "" || strings.TrimSpace(idempotencyKey) == "" {
		return StorageVolume{}, fmt.Errorf("storage_renew_identity_required")
	}
	var result StorageVolume
	err := s.operations.WithPoolLock(ctx, "storage-renew:"+volumeID, func(lockCtx context.Context) error {
		s.mu.Lock()
		existing := s.volumes[volumeID]
		s.mu.Unlock()
		if existing.ID == "" || !strings.HasPrefix(existing.ProviderResourceID, "disk-") || strings.TrimSpace(existing.Deadline) == "" {
			return fmt.Errorf("storage_volume_renew_identity_required")
		}
		requestHash := hashInput(map[string]string{"id": volumeID})
		operations, err := s.operations.List(lockCtx)
		if err != nil {
			return err
		}
		operation := newOperation("renew_storage_volume", "storage_volume", volumeID, existing.AccountID, existing.WorkspaceID, idempotencyKey, requestHash, s.now())
		started := false
		for _, candidate := range operations {
			if candidate.Action != operation.Action || candidate.IdempotencyKey != idempotencyKey {
				continue
			}
			if candidate.RequestHash != requestHash {
				return fmt.Errorf("storage_renew_idempotency_conflict")
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
		result, err = s.provider.RenewStorageVolume(lockCtx, existing)
		if err != nil {
			_ = s.recordOperation(lockCtx, operation, "failed", result, err)
			return err
		}
		if !validStorageRenewal(existing, result) {
			err = fmt.Errorf("storage_renewal_readback_mismatch")
			result = existing
			_ = s.recordOperation(lockCtx, operation, "failed", result, err)
			return err
		}
		if isRetainedStorageStatus(existing.Status) {
			result.Status = "pending"
		}
		if err := s.recordOperation(lockCtx, operation, "succeeded", result, nil); err != nil {
			return err
		}
		s.mu.Lock()
		s.volumes[volumeID] = result
		s.mu.Unlock()
		return nil
	})
	return result, err
}

func (s *Service) CreateStorageAttachment(ctx context.Context, input StorageAttachmentInput) (StorageAttachment, error) {
	if strings.TrimSpace(input.IdempotencyKey) == "" {
		return StorageAttachment{}, fmt.Errorf("storage_attachment_idempotency_key_required")
	}
	requestHash := hashInput(input)
	s.mu.Lock()
	compute := s.computes[input.ComputeID]
	volume := s.volumes[input.VolumeID]
	s.mu.Unlock()
	now := s.now()
	attachmentID := "att_" + stableSuffix(input.IdempotencyKey)[:18]
	operation := newOperation("create_storage_attachment", "storage_attachment", attachmentID, compute.AccountID, input.WorkspaceID, input.IdempotencyKey, requestHash, now)
	if err := validateAttachmentInput(input, compute, volume); err != nil {
		operation.ProviderRequestID = providerRequestID("storage-attach", input.IdempotencyKey)
		_ = s.recordOperation(ctx, operation, "rejected", StorageAttachment{ID: operation.ResourceID, WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID, ProviderRequestID: operation.ProviderRequestID}, err)
		return StorageAttachment{}, err
	}
	operation.ID = "fop_attachment_claim_" + stableSuffix("create_storage_attachment", input.IdempotencyKey)
	operation.Status = "started"
	operation.CreatedAt = now
	fillOperationResource(&operation, StorageAttachment{ID: attachmentID, OperationID: input.IdempotencyKey, WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID, Provider: s.provider.Descriptor().Name, ProviderRequestID: providerRequestID("storage-attach", input.IdempotencyKey)})
	if err := bindLaunchStageOperation(&operation, input.LaunchBinding); err != nil {
		return StorageAttachment{}, err
	}
	input.OperationID = input.IdempotencyKey
	stored, claimed, err := s.claimRuntimeOperation(ctx, operation)
	if err != nil {
		return StorageAttachment{}, err
	}
	if !claimed {
		if stored.RequestHash != requestHash {
			return StorageAttachment{}, ErrStorageAttachmentIdempotencyConflict
		}
		if runtimeOperationNeedsReadback(stored, now) {
			reader, ok := s.provider.(storageAttachmentReadbackProvider)
			if !ok {
				return StorageAttachment{}, ErrStorageAttachmentOperationFailed
			}
			candidate := StorageAttachment{ID: stored.ResourceID, OperationID: input.IdempotencyKey, WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID}
			_ = decodeOperationResource(stored, &candidate)
			candidate.OperationID, candidate.WorkspaceID = input.IdempotencyKey, input.WorkspaceID
			candidate.ComputeID, candidate.VolumeID = input.ComputeID, input.VolumeID
			readback, readErr := reader.ReadStorageAttachment(ctx, candidate, compute, volume)
			if readErr != nil || !attachmentReadbackMatches(readback, input, compute, volume) {
				return StorageAttachment{}, ErrStorageAttachmentOperationFailed
			}
			if _, convergeErr := s.convergeRuntimeOperationReadback(ctx, stored, readback, nil); convergeErr != nil {
				return StorageAttachment{}, convergeErr
			}
			s.mu.Lock()
			s.attachments[readback.ID] = readback
			s.mu.Unlock()
			return readback, nil
		}
		return replayStorageAttachmentOperation(stored, requestHash)
	}
	attachment, err := s.provider.CreateStorageAttachment(s.providerMutationContext(ctx, operation), input, compute, volume)
	attachment.OperationID = input.IdempotencyKey
	if err != nil {
		_ = s.saveStorageAttachmentOperation(ctx, stored, "failed", attachment, err)
		return attachment, err
	}
	if err := s.saveStorageAttachmentOperation(ctx, stored, "succeeded", attachment, nil); err != nil {
		return attachment, err
	}
	s.mu.Lock()
	s.attachments[attachment.ID] = attachment
	s.mu.Unlock()
	return attachment, nil
}

func replayStorageAttachmentOperation(operation FabricOperation, requestHash string) (StorageAttachment, error) {
	if operation.RequestHash != requestHash {
		return StorageAttachment{}, ErrStorageAttachmentIdempotencyConflict
	}
	switch operation.Status {
	case "started":
		return StorageAttachment{}, ErrStorageAttachmentOperationInProgress
	case "succeeded":
		var attachment StorageAttachment
		if decodeOperationResource(operation, &attachment) {
			return attachment, nil
		}
	}
	// ponytail: provider attach is not safely repeatable; reconciliation must resolve failed or corrupt claims.
	return StorageAttachment{}, ErrStorageAttachmentOperationFailed
}

func (s *Service) saveStorageAttachmentOperation(ctx context.Context, operation FabricOperation, status string, attachment StorageAttachment, operationErr error) error {
	operation.Status = status
	operation.FinishedAt = s.now()
	operation.ErrorCode = errorCode(operationErr)
	operation.Retryable = false
	fillOperationResource(&operation, attachment)
	return s.operations.SaveRuntime(ctx, operation)
}

func (s *Service) DetachStorageAttachment(ctx context.Context, attachmentID string) (StorageAttachment, error) {
	s.mu.Lock()
	existing := s.attachments[attachmentID]
	s.mu.Unlock()
	if existing.ID == "" {
		operation := newOperation("detach_storage_attachment", "storage_attachment", attachmentID, "", "", "", hashInput(map[string]string{"id": attachmentID}), time.Now().UTC())
		operation.ProviderRequestID = providerRequestID("detach-attachment", attachmentID)
		err := fmt.Errorf("storage_attachment_not_found")
		_ = s.recordOperation(ctx, operation, "rejected", StorageAttachment{ID: attachmentID}, err)
		return StorageAttachment{}, err
	}
	operation := newOperation("detach_storage_attachment", "storage_attachment", attachmentID, "", existing.WorkspaceID, "", hashInput(map[string]string{"id": attachmentID}), time.Now().UTC())
	if err := s.recordOperation(ctx, operation, "started", existing, nil); err != nil {
		return StorageAttachment{}, err
	}
	attachment, err := s.provider.DetachStorageAttachment(ctx, existing)
	if err != nil {
		_ = s.recordOperation(ctx, operation, "failed", attachment, err)
		return attachment, err
	}
	if err := s.recordOperation(ctx, operation, "succeeded", attachment, nil); err != nil {
		return attachment, err
	}
	s.mu.Lock()
	s.attachments[attachmentID] = attachment
	s.mu.Unlock()
	return attachment, nil
}

func (s *Service) CreateWorkspaceRuntime(ctx context.Context, input WorkspaceRuntimeInput) (WorkspaceRuntime, error) {
	if strings.TrimSpace(input.IdempotencyKey) == "" {
		return WorkspaceRuntime{}, fmt.Errorf("runtime_idempotency_key_required")
	}
	s.mu.Lock()
	compute := s.computes[input.ComputeID]
	volume := s.volumes[input.VolumeID]
	attachment := s.attachments[input.AttachmentID]
	s.mu.Unlock()
	action := "create_workspace_runtime"
	var original WorkspaceRuntime
	if input.RuntimeOperationID != input.IdempotencyKey {
		var err error
		original, err = s.workspaceRuntimeForUpdate(ctx, input, compute)
		if err != nil {
			return WorkspaceRuntime{}, err
		}
		action = "update_workspace_runtime"
	}
	requestHash := hashInput(input)
	if input.LaunchBinding != nil {
		requestHash = input.LaunchBinding.RequestHash
	}
	now := s.now()
	operation := newOperation(action, "workspace_runtime", input.WorkspaceID, compute.AccountID, input.WorkspaceID, input.IdempotencyKey, requestHash, now)
	operation.ID = "fop_runtime_claim_" + stableSuffix(action, input.IdempotencyKey)
	operation.Status = "started"
	operation.CreatedAt = now
	fillOperationResource(&operation, WorkspaceRuntime{ID: original.ID, OperationID: input.RuntimeOperationID, WorkspaceID: input.WorkspaceID, ProviderRequestID: providerRequestID("runtime", input.IdempotencyKey)})
	if err := bindLaunchStageOperation(&operation, input.LaunchBinding); err != nil {
		return WorkspaceRuntime{}, err
	}
	input.OperationID = input.IdempotencyKey
	stored, claimed, err := s.claimRuntimeOperation(ctx, operation)
	if err != nil {
		return WorkspaceRuntime{}, err
	}
	if !claimed {
		if stored.RequestHash != requestHash {
			return WorkspaceRuntime{}, ErrRuntimeIdempotencyConflict
		}
		if runtimeOperationNeedsReadback(stored, now) {
			if err := validateRuntimeInput(input, compute, volume, attachment, action == "update_workspace_runtime", s.provider.ValidateWorkspaceImageReference); err != nil {
				return WorkspaceRuntime{}, ErrRuntimeOperationFailed
			}
			readback, readErr := s.provider.WorkspaceRuntimeStatus(ctx, input.WorkspaceID)
			readback.Access.Password = ""
			if readErr != nil || !runtimeReadbackMatches(readback, input) {
				return WorkspaceRuntime{}, ErrRuntimeOperationFailed
			}
			if action == "update_workspace_runtime" && (readback.ID != original.ID || readback.WorkspaceID != original.WorkspaceID) {
				return WorkspaceRuntime{}, ErrRuntimeOperationFailed
			}
			if _, convergeErr := s.convergeRuntimeOperationReadback(ctx, stored, readback, nil); convergeErr != nil {
				return WorkspaceRuntime{}, convergeErr
			}
			return readback, nil
		}
		return replayRuntimeOperation(stored, requestHash)
	}
	if err := validateRuntimeInput(input, compute, volume, attachment, action == "update_workspace_runtime", s.provider.ValidateWorkspaceImageReference); err != nil {
		_ = s.saveRuntimeOperation(ctx, stored, "failed", WorkspaceRuntime{WorkspaceID: input.WorkspaceID, ProviderRequestID: stored.ProviderRequestID}, err)
		return WorkspaceRuntime{}, err
	}
	runtime, err := s.provider.CreateWorkspaceRuntime(s.providerMutationContext(ctx, operation), input, compute, volume)
	runtime.OperationID = input.RuntimeOperationID
	runtime.Access.Password = ""
	if err == nil && runtime.ImageID != input.ImageID {
		err = fmt.Errorf("workspace_runtime_image_mismatch")
	}
	if err == nil && action == "update_workspace_runtime" && (runtime.ID != original.ID || runtime.WorkspaceID != original.WorkspaceID) {
		err = fmt.Errorf("workspace_runtime_identity_mismatch")
	}
	if err != nil {
		_ = s.saveRuntimeOperation(ctx, stored, "failed", runtime, err)
		return runtime, err
	}
	if err := s.saveRuntimeOperation(ctx, stored, "succeeded", runtime, nil); err != nil {
		return runtime, err
	}
	return runtime, nil
}

func (s *Service) workspaceRuntimeForUpdate(ctx context.Context, input WorkspaceRuntimeInput, compute ComputeAllocation) (WorkspaceRuntime, error) {
	if strings.TrimSpace(input.RuntimeOperationID) == "" || strings.TrimSpace(input.WorkspaceID) == "" || compute.ID == "" {
		return WorkspaceRuntime{}, fmt.Errorf("runtime_operation_identity_mismatch")
	}
	operations, err := s.operations.List(ctx)
	if err != nil {
		return WorkspaceRuntime{}, err
	}
	matches := make([]WorkspaceRuntime, 0, 1)
	for _, operation := range operations {
		if operation.Action != "create_workspace_runtime" || operation.ResourceKind != "workspace_runtime" || operation.Status != "succeeded" ||
			operation.ResourceID != input.WorkspaceID || operation.AccountID != compute.AccountID || operation.WorkspaceID != input.WorkspaceID ||
			operation.IdempotencyKey != input.RuntimeOperationID {
			continue
		}
		var runtime WorkspaceRuntime
		if !decodeOperationResource(operation, &runtime) || runtime.ID == "" || runtime.WorkspaceID != input.WorkspaceID || runtime.OperationID != input.RuntimeOperationID {
			return WorkspaceRuntime{}, fmt.Errorf("runtime_operation_identity_mismatch")
		}
		matches = append(matches, runtime)
	}
	if len(matches) != 1 {
		return WorkspaceRuntime{}, fmt.Errorf("runtime_operation_identity_mismatch")
	}
	return matches[0], nil
}

func (s *Service) DestroyWorkspaceRuntime(ctx context.Context, workspaceID, idempotencyKey string) (WorkspaceRuntime, error) {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(idempotencyKey) == "" {
		return WorkspaceRuntime{}, fmt.Errorf("runtime_destroy_identity_required")
	}
	requestHash := hashInput(map[string]string{"workspaceId": workspaceID})
	now := s.now()
	operation := newOperation("destroy_workspace_runtime", "workspace_runtime", workspaceID, "", workspaceID, idempotencyKey, requestHash, now)
	operation.ID = "fop_runtime_destroy_claim_" + stableSuffix("destroy_workspace_runtime", idempotencyKey)
	operation.Status = "started"
	operation.CreatedAt = now
	fillOperationResource(&operation, WorkspaceRuntime{WorkspaceID: workspaceID, ProviderRequestID: providerRequestID("runtime-destroy", idempotencyKey)})
	stored, claimed, err := s.operations.ClaimRuntime(ctx, operation)
	if err != nil {
		return WorkspaceRuntime{}, err
	}
	if !claimed {
		return replayRuntimeOperation(stored, requestHash)
	}
	runtime, err := s.provider.DestroyWorkspaceRuntime(ctx, workspaceID)
	runtime.Access.Password = ""
	runtime.WorkspaceID = firstNonEmpty(runtime.WorkspaceID, workspaceID)
	runtime.ProviderRequestID = firstNonEmpty(runtime.ProviderRequestID, providerRequestID("runtime-destroy", idempotencyKey))
	if err != nil {
		_ = s.saveRuntimeOperation(ctx, stored, "failed", runtime, err)
		return runtime, err
	}
	if err := s.saveRuntimeOperation(ctx, stored, "succeeded", runtime, nil); err != nil {
		return runtime, err
	}
	return runtime, nil
}

func replayRuntimeOperation(operation FabricOperation, requestHash string) (WorkspaceRuntime, error) {
	if operation.RequestHash != requestHash {
		return WorkspaceRuntime{}, ErrRuntimeIdempotencyConflict
	}
	switch operation.Status {
	case "started":
		return WorkspaceRuntime{}, ErrRuntimeOperationInProgress
	case "succeeded":
		var runtime WorkspaceRuntime
		if decodeOperationResource(operation, &runtime) {
			runtime.Access.Password = ""
			return runtime, nil
		}
	}
	// ponytail: provider apply is not safely repeatable; reconciliation must resolve failed or corrupt claims.
	return WorkspaceRuntime{}, ErrRuntimeOperationFailed
}

func (s *Service) saveRuntimeOperation(ctx context.Context, operation FabricOperation, status string, runtime WorkspaceRuntime, operationErr error) error {
	operation.Status = status
	operation.FinishedAt = s.now()
	operation.ErrorCode = errorCode(operationErr)
	operation.Retryable = false
	fillOperationResource(&operation, runtime)
	return s.operations.SaveRuntime(ctx, operation)
}

func (s *Service) WorkspaceRuntimeStatus(ctx context.Context, workspaceID string) (WorkspaceRuntime, error) {
	runtime, err := s.provider.WorkspaceRuntimeStatus(ctx, workspaceID)
	if err != nil {
		return runtime, err
	}
	if runtime.Status != "running" && runtime.Status != "unready" {
		return runtime, nil
	}
	operations, err := s.operations.List(ctx)
	if err != nil {
		return runtime, err
	}
	matches := make([]FabricOperation, 0, 1)
	for _, operation := range operations {
		if operation.Action != "create_workspace_runtime" || operation.ResourceKind != "workspace_runtime" || operation.Status != "succeeded" || operation.WorkspaceID != workspaceID || operation.ResourceID != workspaceID {
			continue
		}
		matches = append(matches, operation)
	}
	var created WorkspaceRuntime
	if runtime.WorkspaceID != workspaceID || len(matches) != 1 || matches[0].ID == "" || matches[0].CreatedAt.IsZero() || !decodeOperationResource(matches[0], &created) ||
		created.WorkspaceID != workspaceID || strings.TrimSpace(created.ID) == "" || strings.TrimSpace(created.OperationID) == "" ||
		runtime.ID != "" && runtime.ID != created.ID || runtime.OperationID != "" && runtime.OperationID != created.OperationID {
		return runtime, fmt.Errorf("workspace_runtime_identity_unavailable")
	}
	runtime.ID, runtime.OperationID = created.ID, created.OperationID
	return runtime, nil
}

func (s *Service) UpsertGatewaySecret(ctx context.Context, input GatewaySecretInput) (GatewaySecret, error) {
	if strings.TrimSpace(input.AccountID) == "" || strings.TrimSpace(input.WorkspaceID) == "" || input.WorkspaceAPIKeyID <= 0 || strings.TrimSpace(input.GatewayAPIKey) == "" || strings.TrimSpace(input.IdempotencyKey) == "" {
		return GatewaySecret{}, fmt.Errorf("gateway_secret_input_required")
	}
	keyDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(input.GatewayAPIKey)))
	if input.Fingerprint != "sha256:"+keyDigest {
		return GatewaySecret{}, fmt.Errorf("gateway_secret_fingerprint_mismatch")
	}
	requestHash := hashInput(map[string]any{"accountId": input.AccountID, "workspaceId": input.WorkspaceID, "workspaceApiKeyId": input.WorkspaceAPIKeyID, "fingerprint": input.Fingerprint})
	if input.LaunchBinding != nil {
		requestHash = input.LaunchBinding.RequestHash
	}
	now := s.now()
	secretRef := gatewaySecretName(input.WorkspaceID)
	operation := newOperation("upsert_gateway_secret", "gateway_secret", secretRef, input.AccountID, input.WorkspaceID, input.IdempotencyKey, requestHash, now)
	operation.ID = "fop_gateway_secret_claim_" + stableSuffix("upsert_gateway_secret", input.IdempotencyKey)
	operation.Status = "started"
	operation.CreatedAt = now
	operation.ProviderRequestID = providerRequestID("gateway-secret", input.IdempotencyKey)
	operation.RedactedProviderPayload = map[string]any{"resource": GatewaySecret{SecretRef: secretRef}, "keyDigest": keyDigest}
	if err := bindLaunchStageOperation(&operation, input.LaunchBinding); err != nil {
		return GatewaySecret{}, err
	}
	stored, claimed, err := s.claimRuntimeOperation(ctx, operation)
	if err != nil {
		return GatewaySecret{}, err
	}
	if !claimed {
		if stored.RequestHash != requestHash {
			return GatewaySecret{}, ErrGatewaySecretIdempotencyConflict
		}
		if runtimeOperationNeedsReadback(stored, now) {
			var readback GatewaySecret
			var readErr error
			switch provider := s.provider.(type) {
			case gatewaySecretReadbackProvider:
				readback, readErr = provider.ReadGatewaySecret(ctx, input)
			case runtimeGatewaySecretProvider:
				var binding WorkspaceRuntimeGatewaySecretBinding
				binding, readErr = provider.WorkspaceRuntimeGatewaySecret(ctx, input.WorkspaceID)
				if readErr == nil && (binding.WorkspaceID != input.WorkspaceID || binding.WorkspaceAPIKeyID != input.WorkspaceAPIKeyID || !binding.Bound) {
					readErr = fmt.Errorf("gateway_secret_readback_mismatch")
				}
				readback = GatewaySecret{SecretRef: binding.SecretRef, Version: keyDigest[:16], Fingerprint: binding.Fingerprint}
			default:
				readErr = fmt.Errorf("gateway_secret_readback_unavailable")
			}
			if readErr != nil || !gatewaySecretReadbackMatches(readback, input) {
				return GatewaySecret{}, fmt.Errorf("gateway_secret_operation_%s", stored.Status)
			}
			if _, convergeErr := s.convergeRuntimeOperationReadback(ctx, stored, readback, map[string]any{"keyDigest": keyDigest}); convergeErr != nil {
				return GatewaySecret{}, convergeErr
			}
			return readback, nil
		}
		if stored.Status == "succeeded" {
			var replayed GatewaySecret
			if decodeOperationResource(stored, &replayed) {
				return replayed, nil
			}
		}
		return GatewaySecret{}, fmt.Errorf("gateway_secret_operation_%s", stored.Status)
	}
	secret, providerErr := s.provider.UpsertGatewaySecret(s.providerMutationContext(ctx, operation), input)
	stored.Status = operationStatus(providerErr)
	stored.FinishedAt = s.now()
	stored.ErrorCode = errorCode(providerErr)
	binding := stored.RedactedProviderPayload[launchStageBindingPayloadKey]
	stored.RedactedProviderPayload = map[string]any{"resource": secret, "keyDigest": keyDigest}
	if binding != nil {
		stored.RedactedProviderPayload[launchStageBindingPayloadKey] = binding
	}
	if saveErr := s.operations.SaveRuntime(ctx, stored); saveErr != nil && providerErr == nil {
		return GatewaySecret{}, saveErr
	}
	return secret, providerErr
}

func (s *Service) BindWorkspaceRuntimeGatewaySecret(ctx context.Context, input WorkspaceRuntimeGatewaySecretInput) (WorkspaceRuntimeGatewaySecretBinding, error) {
	if strings.TrimSpace(input.WorkspaceID) == "" || input.WorkspaceAPIKeyID <= 0 || input.SecretRef != gatewaySecretName(input.WorkspaceID) || strings.TrimSpace(input.Fingerprint) == "" || strings.TrimSpace(input.IdempotencyKey) == "" {
		return WorkspaceRuntimeGatewaySecretBinding{}, fmt.Errorf("workspace_runtime_gateway_secret_input_required")
	}
	provider, ok := s.provider.(runtimeGatewaySecretProvider)
	if !ok {
		return WorkspaceRuntimeGatewaySecretBinding{}, fmt.Errorf("workspace_runtime_gateway_secret_unavailable")
	}
	return provider.BindWorkspaceRuntimeGatewaySecret(ctx, input)
}

func (s *Service) WorkspaceRuntimeGatewaySecret(ctx context.Context, workspaceID string) (WorkspaceRuntimeGatewaySecretBinding, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return WorkspaceRuntimeGatewaySecretBinding{}, fmt.Errorf("workspace_runtime_gateway_secret_input_required")
	}
	provider, ok := s.provider.(runtimeGatewaySecretProvider)
	if !ok {
		return WorkspaceRuntimeGatewaySecretBinding{}, fmt.Errorf("workspace_runtime_gateway_secret_unavailable")
	}
	return provider.WorkspaceRuntimeGatewaySecret(ctx, workspaceID)
}

func (s *Service) ProviderFactsBatch(ctx context.Context, input ProviderFactsBatchInput) (ProviderFactsBatch, error) {
	result := ProviderFactsBatch{Items: make([]ProviderFact, len(input.Items))}
	if len(input.Items) == 0 || len(input.Items) > 50 {
		return ProviderFactsBatch{}, fmt.Errorf("provider_facts_batch_invalid")
	}
	batchCtx, cancel := context.WithTimeout(ctx, providerFactsBatchTimeout)
	defer cancel()
	type job struct {
		index int
		item  ProviderFactInput
	}
	jobs := make(chan job, len(input.Items))
	for index, item := range input.Items {
		result.Items[index] = ProviderFact{AccountID: item.AccountID, WorkspaceID: item.WorkspaceID, ResourceType: item.ResourceType, ResourceID: item.ResourceID}
		jobs <- job{index: index, item: item}
	}
	close(jobs)
	workers := providerFactsBatchWorkerCount
	if len(input.Items) < workers {
		workers = len(input.Items)
	}
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for {
				select {
				case <-batchCtx.Done():
					return
				case next, ok := <-jobs:
					if !ok {
						return
					}
					result.Items[next.index] = s.providerFact(batchCtx, next.item)
				}
			}
		}()
	}
	wait.Wait()
	if batchCtx.Err() != nil {
		for index := range result.Items {
			if !result.Items[index].Available && result.Items[index].ErrorCode == "" {
				result.Items[index].ErrorCode = "provider_facts_timeout"
			}
		}
	}
	return result, nil
}

func (s *Service) RuntimeHealthSummary(ctx context.Context) (RuntimeHealthSummary, error) {
	provider, ok := s.provider.(runtimeHealthSummaryProvider)
	if !ok {
		return RuntimeHealthSummary{}, ErrRuntimeHealthSummaryUnavailable
	}
	readCtx, cancel := context.WithTimeout(ctx, runtimeHealthSummaryTimeout)
	defer cancel()
	summary, err := provider.RuntimeHealthSummary(readCtx)
	if err != nil {
		return RuntimeHealthSummary{}, fmt.Errorf("%w: %v", ErrRuntimeHealthSummaryUnavailable, err)
	}
	if summary.Total < 0 || summary.Ready < 0 || summary.Unready < 0 || summary.Ready+summary.Unready != summary.Total {
		return RuntimeHealthSummary{}, fmt.Errorf("%w: invalid_counts", ErrRuntimeHealthSummaryUnavailable)
	}
	return summary, nil
}

func (s *Service) providerFact(ctx context.Context, input ProviderFactInput) ProviderFact {
	result := ProviderFact{AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: input.ResourceType, ResourceID: input.ResourceID}
	if input.AccountID == "" || input.WorkspaceID == "" || input.ResourceID == "" {
		result.ErrorCode = "provider_fact_identity_required"
		return result
	}
	s.mu.Lock()
	compute := s.computes[input.ResourceID]
	storage := s.volumes[input.ResourceID]
	attachment := s.attachments[input.ResourceID]
	attachmentCompute := s.computes[attachment.ComputeID]
	attachmentStorage := s.volumes[attachment.VolumeID]
	s.mu.Unlock()
	var facts ProviderResourceFacts
	var err error
	switch input.ResourceType {
	case "compute":
		provider, ok := s.provider.(providerFactsReader)
		if !ok {
			result.ErrorCode = "provider_facts_unavailable"
			return result
		}
		if compute.ID == "" || compute.AccountID != input.AccountID || compute.WorkspaceID != input.WorkspaceID {
			result.ErrorCode = "provider_fact_identity_mismatch"
			return result
		}
		compute, err = provider.ReadComputeAllocation(ctx, compute)
		facts = ProviderResourceFacts{PackageOrSpec: firstNonEmpty(compute.InstanceType, compute.ProviderData["instanceType"]), ProviderID: firstNonEmpty(compute.ProviderResourceID, compute.InstanceID, compute.CVMInstanceID), Zone: firstNonEmpty(compute.Zone, compute.ProviderData["zone"]), Status: firstNonEmpty(compute.CVMStatus, compute.Status), ExpiresAt: compute.Deadline, LastReadAt: s.now().Format(time.RFC3339Nano)}
	case "storage":
		provider, ok := s.provider.(providerFactsReader)
		if !ok {
			result.ErrorCode = "provider_facts_unavailable"
			return result
		}
		if storage.ID == "" || storage.AccountID != input.AccountID || storage.WorkspaceID != input.WorkspaceID {
			result.ErrorCode = "provider_fact_identity_mismatch"
			return result
		}
		storage, err = provider.ReadStorageVolume(ctx, storage)
		facts = ProviderResourceFacts{PackageOrSpec: firstNonEmpty(storage.DiskType, storage.StorageClass), ProviderID: storage.ProviderResourceID, Zone: storage.Zone, Status: firstNonEmpty(storage.CBSStatus, storage.Status), ExpiresAt: storage.Deadline, LastReadAt: s.now().Format(time.RFC3339Nano)}
	case "attachment":
		provider, ok := s.provider.(providerFactsReader)
		if !ok {
			result.ErrorCode = "provider_facts_unavailable"
			return result
		}
		if attachment.ID == "" || attachment.WorkspaceID != input.WorkspaceID || attachmentCompute.AccountID != input.AccountID || attachmentCompute.WorkspaceID != input.WorkspaceID || attachmentStorage.AccountID != input.AccountID || attachmentStorage.WorkspaceID != input.WorkspaceID {
			result.ErrorCode = "provider_fact_identity_mismatch"
			return result
		}
		attachment, err = provider.ReadStorageAttachment(ctx, attachment, attachmentCompute, attachmentStorage)
		facts = ProviderResourceFacts{PackageOrSpec: "/data", ProviderID: attachment.ProviderAttachmentID, Status: attachment.Status, LastReadAt: s.now().Format(time.RFC3339Nano)}
	case "runtime":
		var runtime WorkspaceRuntime
		runtime, err = s.WorkspaceRuntimeStatus(ctx, input.WorkspaceID)
		if err == nil && (runtime.ID != input.ResourceID || runtime.WorkspaceID != input.WorkspaceID) {
			result.ErrorCode = "provider_fact_identity_mismatch"
			return result
		}
		facts = ProviderResourceFacts{ProviderID: runtime.ServiceName, Status: runtime.Status, LastReadAt: s.now().Format(time.RFC3339Nano)}
	default:
		result.ErrorCode = "provider_fact_resource_type_invalid"
		return result
	}
	if err != nil {
		result.ErrorCode = errorCode(err)
		return result
	}
	result.Available, result.Facts = true, facts
	return result
}

func (s *Service) Readiness(ctx context.Context) (map[string]any, error) {
	return s.provider.Readiness(ctx)
}

func (s *Service) ListOperations(ctx context.Context) ([]FabricOperation, error) {
	return s.operations.List(ctx)
}

func (s *Service) CreateJob(ctx context.Context, input JobInput) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if input.OrganizationID == "" || input.WorkspaceID == "" || input.ProjectID == "" || input.TaskID == "" || input.RequestID == "" || input.ApprovalID == "" || input.IdempotencyKey == "" {
		return Job{}, ErrInvalidJobInput
	}
	requestHash := hashInput(input)
	operations, err := s.operations.List(ctx)
	if err != nil {
		return Job{}, err
	}
	// ponytail: linear scan is enough for the initial job volume; add an indexed store query when measured throughput requires it.
	for _, operation := range operations {
		if operation.ResourceKind != "job" || operation.Action != "create_job" || operation.IdempotencyKey != input.IdempotencyKey {
			continue
		}
		if operation.RequestHash != requestHash {
			return Job{}, ErrJobIdempotencyConflict
		}
		var job Job
		if decodeOperationResource(operation, &job) {
			job.Replayed = true
			return job, nil
		}
	}
	now := s.now()
	job := Job{
		JobID:          "job-" + stableSuffix(input.IdempotencyKey, input.RequestID, input.TaskID)[:16],
		OrganizationID: input.OrganizationID,
		WorkspaceID:    input.WorkspaceID,
		ProjectID:      input.ProjectID,
		TaskID:         input.TaskID,
		RequestID:      input.RequestID,
		ApprovalID:     input.ApprovalID,
		EnvironmentRef: input.EnvironmentRef,
		Status:         "queued",
		Attempt:        1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	operation := newOperation("create_job", "job", job.JobID, "", input.WorkspaceID, input.IdempotencyKey, requestHash, now)
	operation.ProviderRequestID = job.JobID
	if err := s.recordOperation(ctx, operation, job.Status, job, nil); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) Job(ctx context.Context, jobID string) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	return s.jobLocked(ctx, jobID, true)
}

func (s *Service) jobLocked(ctx context.Context, jobID string, expire bool) (Job, error) {
	operations, err := s.operations.List(ctx)
	if err != nil {
		return Job{}, err
	}
	var job Job
	leaseTokenHash := ""
	found := false
	for _, operation := range operations {
		if operation.ResourceKind == "job" && operation.ResourceID == jobID && decodeOperationResource(operation, &job) {
			found = true
			leaseTokenHash, _ = operation.RedactedProviderPayload["leaseTokenHash"].(string)
		}
	}
	if !found {
		return Job{}, ErrJobNotFound
	}
	job.leaseTokenHash = leaseTokenHash
	if expire && job.Status == "running" && job.LeaseExpiresAt != nil && !s.now().Before(*job.LeaseExpiresAt) {
		job.Status = "timed_out"
		job.ErrorCode = "lease_expired"
		job.UpdatedAt = s.now()
		if err := s.appendJobTransition(ctx, "timeout_job", "timeout-"+job.JobID+fmt.Sprintf("-%d", job.Attempt), hashInput(map[string]any{"jobId": job.JobID, "attempt": job.Attempt}), job, "runner"); err != nil {
			return Job{}, err
		}
	}
	return job, nil
}

func (s *Service) CancelJob(ctx context.Context, jobID string, idempotencyKey string) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if idempotencyKey == "" {
		return Job{}, ErrInvalidJobInput
	}
	requestHash := hashInput(map[string]string{"jobId": jobID})
	if replayed, ok, err := s.replayedJobTransition(ctx, "cancel_job", jobID, idempotencyKey, requestHash); ok || err != nil {
		return replayed, err
	}
	job, err := s.jobLocked(ctx, jobID, true)
	if err != nil {
		return Job{}, err
	}
	if job.Status == "cancelled" {
		job.Replayed = true
		return job, nil
	}
	if job.Status != "queued" && job.Status != "running" {
		return Job{}, ErrJobStateConflict
	}
	now := s.now()
	job.Status = "cancelled"
	job.UpdatedAt = now
	if err := s.appendJobTransition(ctx, "cancel_job", idempotencyKey, requestHash, job, "control-plane"); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) ClaimJob(ctx context.Context, jobID string, input JobClaimInput) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if jobID == "" || input.RunnerID == "" || input.IdempotencyKey == "" {
		return Job{}, ErrInvalidJobInput
	}
	requestHash := hashInput(map[string]string{"jobId": jobID, "runnerId": input.RunnerID})
	if replayed, ok, err := s.replayedJobTransition(ctx, "claim_job", jobID, input.IdempotencyKey, requestHash); ok || err != nil {
		return replayed, err
	}
	job, err := s.jobLocked(ctx, jobID, true)
	if err != nil {
		return Job{}, err
	}
	if job.Status != "queued" {
		return Job{}, ErrJobStateConflict
	}
	now := s.now()
	token, err := newLeaseToken()
	if err != nil {
		return Job{}, err
	}
	expiresAt := now.Add(30 * time.Second)
	job.Status = "running"
	job.LeaseOwner = input.RunnerID
	job.LeaseExpiresAt = &expiresAt
	job.LeaseToken = token
	job.leaseTokenHash = stableSuffix(token)
	job.UpdatedAt = now
	if err := s.appendJobTransition(ctx, "claim_job", input.IdempotencyKey, requestHash, job, "runner"); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) HeartbeatJob(ctx context.Context, jobID string, input JobHeartbeatInput) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if jobID == "" || input.RunnerID == "" || input.LeaseToken == "" || input.IdempotencyKey == "" {
		return Job{}, ErrInvalidJobInput
	}
	requestHash := hashInput(map[string]string{"jobId": jobID, "runnerId": input.RunnerID, "leaseTokenHash": stableSuffix(input.LeaseToken)})
	if replayed, ok, err := s.replayedJobTransition(ctx, "heartbeat_job", jobID, input.IdempotencyKey, requestHash); ok || err != nil {
		return replayed, err
	}
	job, err := s.activeLeasedJob(ctx, jobID, input.RunnerID, input.LeaseToken)
	if err != nil {
		return Job{}, err
	}
	now := s.now()
	expiresAt := now.Add(30 * time.Second)
	job.LeaseExpiresAt = &expiresAt
	job.UpdatedAt = now
	if err := s.appendJobTransition(ctx, "heartbeat_job", input.IdempotencyKey, requestHash, job, "runner"); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) CompleteJob(ctx context.Context, jobID string, input JobCompleteInput) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if jobID == "" || input.RunnerID == "" || input.LeaseToken == "" || len(input.ArtifactIDs) == 0 || len(input.ReviewIDs) == 0 || input.IdempotencyKey == "" {
		return Job{}, ErrInvalidJobInput
	}
	requestHash := hashInput(struct {
		JobID, RunnerID, LeaseTokenHash string
		ArtifactIDs, ReviewIDs          []string
	}{jobID, input.RunnerID, stableSuffix(input.LeaseToken), input.ArtifactIDs, input.ReviewIDs})
	if replayed, ok, err := s.replayedJobTransition(ctx, "complete_job", jobID, input.IdempotencyKey, requestHash); ok || err != nil {
		return replayed, err
	}
	job, err := s.activeLeasedJob(ctx, jobID, input.RunnerID, input.LeaseToken)
	if err != nil {
		return Job{}, err
	}
	job.Status = "succeeded"
	job.ArtifactIDs = append([]string(nil), input.ArtifactIDs...)
	job.ReviewIDs = append([]string(nil), input.ReviewIDs...)
	job.ErrorCode = ""
	job.UpdatedAt = s.now()
	if err := s.appendJobTransition(ctx, "complete_job", input.IdempotencyKey, requestHash, job, "runner"); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) FailJob(ctx context.Context, jobID string, input JobFailInput) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if jobID == "" || input.RunnerID == "" || input.LeaseToken == "" || input.ErrorCode == "" || input.IdempotencyKey == "" {
		return Job{}, ErrInvalidJobInput
	}
	requestHash := hashInput(map[string]string{"jobId": jobID, "runnerId": input.RunnerID, "leaseTokenHash": stableSuffix(input.LeaseToken), "errorCode": input.ErrorCode})
	if replayed, ok, err := s.replayedJobTransition(ctx, "fail_job", jobID, input.IdempotencyKey, requestHash); ok || err != nil {
		return replayed, err
	}
	job, err := s.activeLeasedJob(ctx, jobID, input.RunnerID, input.LeaseToken)
	if err != nil {
		return Job{}, err
	}
	job.Status = "failed"
	job.ErrorCode = input.ErrorCode
	job.UpdatedAt = s.now()
	if err := s.appendJobTransition(ctx, "fail_job", input.IdempotencyKey, requestHash, job, "runner"); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) RetryJob(ctx context.Context, jobID, idempotencyKey string) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if jobID == "" || idempotencyKey == "" {
		return Job{}, ErrInvalidJobInput
	}
	requestHash := hashInput(map[string]string{"jobId": jobID})
	if replayed, ok, err := s.replayedJobTransition(ctx, "retry_job", jobID, idempotencyKey, requestHash); ok || err != nil {
		return replayed, err
	}
	job, err := s.jobLocked(ctx, jobID, true)
	if err != nil {
		return Job{}, err
	}
	if job.Status != "failed" && job.Status != "timed_out" {
		return Job{}, ErrJobStateConflict
	}
	job.Status = "queued"
	job.Attempt++
	job.LeaseOwner = ""
	job.LeaseExpiresAt = nil
	job.LeaseToken = ""
	job.leaseTokenHash = ""
	job.ArtifactIDs = nil
	job.ReviewIDs = nil
	job.ErrorCode = ""
	job.UpdatedAt = s.now()
	if err := s.appendJobTransition(ctx, "retry_job", idempotencyKey, requestHash, job, "control-plane"); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) activeLeasedJob(ctx context.Context, jobID, runnerID, leaseToken string) (Job, error) {
	job, err := s.jobLocked(ctx, jobID, true)
	if err != nil {
		return Job{}, err
	}
	if job.Status != "running" {
		return Job{}, ErrJobStateConflict
	}
	if job.LeaseOwner != runnerID || subtle.ConstantTimeCompare([]byte(job.leaseTokenHash), []byte(stableSuffix(leaseToken))) != 1 {
		return Job{}, ErrJobLeaseMismatch
	}
	return job, nil
}

func (s *Service) replayedJobTransition(ctx context.Context, action, jobID, idempotencyKey, requestHash string) (Job, bool, error) {
	operations, err := s.operations.List(ctx)
	if err != nil {
		return Job{}, false, err
	}
	for _, operation := range operations {
		if operation.ResourceKind != "job" || operation.ResourceID != jobID || operation.Action != action || operation.IdempotencyKey != idempotencyKey {
			continue
		}
		if operation.RequestHash != requestHash {
			return Job{}, false, ErrJobIdempotencyConflict
		}
		var job Job
		if decodeOperationResource(operation, &job) {
			job.Replayed = true
			return job, true, nil
		}
	}
	return Job{}, false, nil
}

func (s *Service) appendJobTransition(ctx context.Context, action, idempotencyKey, requestHash string, job Job, caller string) error {
	operation := newOperation(action, "job", job.JobID, "", job.WorkspaceID, idempotencyKey, requestHash, s.now())
	operation.ProviderRequestID = job.JobID
	operation.CallerService = caller
	return s.recordOperation(ctx, operation, job.Status, job, nil)
}

func newLeaseToken() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return "lease-" + hex.EncodeToString(data), nil
}

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
		operation.RedactedProviderPayload[providerMutationBindingPayloadKey] = providerBinding
	}
	if providerState != nil {
		operation.RedactedProviderPayload[providerMutationStatePayloadKey] = providerState
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

func validateAttachmentInput(input StorageAttachmentInput, compute ComputeAllocation, volume StorageVolume) error {
	if compute.ID == "" {
		return fmt.Errorf("compute_allocation_not_found")
	}
	if volume.ID == "" {
		return fmt.Errorf("storage_volume_not_found")
	}
	if compute.AccountID == "" || volume.AccountID == "" || compute.AccountID != volume.AccountID {
		return fmt.Errorf("resource_account_mismatch")
	}
	if strings.TrimSpace(input.WorkspaceID) == "" || input.WorkspaceID != compute.WorkspaceID || input.WorkspaceID != volume.WorkspaceID {
		return fmt.Errorf("resource_workspace_mismatch")
	}
	if !isReadyResourceStatus(compute.Status) || volume.Status != "ready" {
		return fmt.Errorf("resource_status_invalid")
	}
	return nil
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

func validStorageRenewal(existing, renewed StorageVolume) bool {
	return renewed.ID == existing.ID && renewed.AccountID == existing.AccountID && renewed.WorkspaceID == existing.WorkspaceID &&
		renewed.ProviderResourceID == existing.ProviderResourceID && renewed.ProviderData["diskChargeType"] == "PREPAID" &&
		renewed.RenewFlag == "NOTIFY_AND_MANUAL_RENEW" && renewalDeadlineIncreased(existing.Deadline, renewed.Deadline)
}

func renewalDeadlineIncreased(previous, current string) bool {
	previousTime, previousErr := time.Parse(time.RFC3339, previous)
	currentTime, currentErr := time.Parse(time.RFC3339, current)
	return previousErr == nil && currentErr == nil && currentTime.After(previousTime)
}

func validateRuntimeInput(input WorkspaceRuntimeInput, compute ComputeAllocation, volume StorageVolume, attachment StorageAttachment, update bool, validImage func(string) bool) error {
	if compute.ID == "" {
		return fmt.Errorf("compute_allocation_not_found")
	}
	if volume.ID == "" {
		return fmt.Errorf("storage_volume_not_found")
	}
	if compute.AccountID == "" || volume.AccountID == "" || compute.AccountID != volume.AccountID {
		return fmt.Errorf("resource_account_mismatch")
	}
	if strings.TrimSpace(input.WorkspaceID) == "" || input.WorkspaceID != compute.WorkspaceID || input.WorkspaceID != volume.WorkspaceID {
		return fmt.Errorf("resource_workspace_mismatch")
	}
	if attachment.ID == "" {
		return fmt.Errorf("storage_attachment_not_found")
	}
	if input.AttachmentID != attachment.ID || input.AttachmentOperationID == "" || input.AttachmentOperationID != attachment.OperationID ||
		attachment.WorkspaceID != input.WorkspaceID || attachment.ComputeID != input.ComputeID || attachment.VolumeID != input.VolumeID || attachment.Status != "attached" {
		return fmt.Errorf("storage_attachment_identity_mismatch")
	}
	if input.RuntimeOperationID == "" || update == (input.RuntimeOperationID == input.IdempotencyKey) {
		return fmt.Errorf("runtime_operation_identity_mismatch")
	}
	if !isReadyResourceStatus(compute.Status) || volume.Status != "ready" {
		return fmt.Errorf("resource_status_invalid")
	}
	if validImage == nil || !validImage(input.ImageID) {
		return fmt.Errorf("workspace_image_identity_invalid")
	}
	if strings.TrimSpace(input.GatewaySecretRef) == "" || input.GatewaySecretRef != gatewaySecretName(input.WorkspaceID) {
		return fmt.Errorf("gateway_secret_ref_mismatch")
	}
	return nil
}

func validWorkspaceRuntimeImageIdentity(value string) bool {
	value = strings.TrimSpace(value)
	prefix := workspaceImageRepository + "@sha256:"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+sha256.Size*2 {
		return false
	}
	digest := strings.TrimPrefix(value, prefix)
	return digest == strings.ToLower(digest) && validDigest(digest)
}

func isReadyResourceStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running", "ready", "active":
		return true
	default:
		return false
	}
}

func isRetainedStorageStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "retained", "released":
		return true
	default:
		return false
	}
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
