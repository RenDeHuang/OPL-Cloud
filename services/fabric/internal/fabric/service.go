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
	"reflect"
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
	workspaceImageRepository         = "uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app"
)

type Provider interface {
	MonthlyPreflight(ctx context.Context, input MonthlyPreflightInput) (MonthlyPreflight, error)
	PrepareComputeAllocation(ctx context.Context, input ComputeAllocationInput) (ComputeAllocationPreparation, error)
	CreateComputeAllocation(ctx context.Context, input ComputeAllocationExecution) (ComputeAllocation, error)
	TagComputeMachine(ctx context.Context, machine ProviderMachine, ownership MachineOwnership) error
	SyncComputeAllocation(ctx context.Context, allocation ComputeAllocation) (ComputeAllocation, error)
	RenewComputeAllocation(ctx context.Context, allocation ComputeAllocation) (ComputeAllocation, error)
	DestroyComputeAllocation(ctx context.Context, allocation ComputeAllocation) (ComputeAllocation, error)
	CreateStorageVolume(ctx context.Context, input StorageVolumeInput) (StorageVolume, error)
	SyncStorageVolume(ctx context.Context, volume StorageVolume) (StorageVolume, error)
	RenewStorageVolume(ctx context.Context, volume StorageVolume) (StorageVolume, error)
	DestroyStorageVolume(ctx context.Context, volume StorageVolume) (StorageVolume, error)
	CreateStorageSnapshot(ctx context.Context, input StorageSnapshotInput, volume StorageVolume) (StorageSnapshot, error)
	SyncStorageSnapshot(ctx context.Context, snapshot StorageSnapshot) (StorageSnapshot, error)
	RestoreStorageSnapshot(ctx context.Context, input StorageRestoreInput, snapshot StorageSnapshot) (StorageVolume, error)
	DestroyStorageSnapshot(ctx context.Context, snapshot StorageSnapshot) (StorageSnapshot, error)
	CreateStorageAttachment(ctx context.Context, input StorageAttachmentInput, compute ComputeAllocation, volume StorageVolume) (StorageAttachment, error)
	DetachStorageAttachment(ctx context.Context, attachment StorageAttachment) (StorageAttachment, error)
	CreateWorkspaceRuntime(ctx context.Context, input WorkspaceRuntimeInput, compute ComputeAllocation, volume StorageVolume) (WorkspaceRuntime, error)
	DestroyWorkspaceRuntime(ctx context.Context, workspaceID string) (WorkspaceRuntime, error)
	WorkspaceRuntimeStatus(ctx context.Context, workspaceID string) (WorkspaceRuntime, error)
	UpsertGatewaySecret(ctx context.Context, input GatewaySecretInput) (GatewaySecret, error)
	Readiness(ctx context.Context) (map[string]any, error)
}

type monthlyProviderTruthProvider interface {
	MonthlyProviderTruth(context.Context, ComputeAllocation, StorageVolume) (MonthlyProviderTruth, error)
}

type computeClaimRecoveryProvider interface {
	ProveComputeClaimRecovery(context.Context, ComputeAllocation, ComputeAllocationPreparation, MachineOwnership) (ComputeClaimProviderProof, error)
}

type computeClaimRecoveryClaimProvider interface {
	ClaimComputeRecovery(context.Context, ComputeAllocation, ComputeAllocationPreparation, MachineOwnership) (ComputeClaimProviderClaim, error)
}

type computeClaimRecoveryNodeOnlyProvider interface {
	ClaimComputeRecoveryNodeOnly(context.Context, ComputeAllocation, ComputeAllocationPreparation, MachineOwnership) (ComputeClaimProviderClaim, error)
}

type storageRecoveryDiscoveryProvider interface {
	DiscoverStorageRecovery(context.Context, StorageVolumeInput) (StorageRecoveryDiscovery, error)
}

type runtimeGatewaySecretProvider interface {
	BindWorkspaceRuntimeGatewaySecret(context.Context, WorkspaceRuntimeGatewaySecretInput) (WorkspaceRuntimeGatewaySecretBinding, error)
	WorkspaceRuntimeGatewaySecret(context.Context, string) (WorkspaceRuntimeGatewaySecretBinding, error)
}

// gatewaySecretReadbackProvider is optional so older test/providers can still
// implement the write surface while production Tencent uses a direct GET.
type gatewaySecretReadbackProvider interface {
	ReadGatewaySecret(context.Context, GatewaySecretInput) (GatewaySecret, error)
}

type providerFactsReader interface {
	ReadComputeAllocation(context.Context, ComputeAllocation) (ComputeAllocation, error)
	ReadStorageVolume(context.Context, StorageVolume) (StorageVolume, error)
	ReadStorageAttachment(context.Context, StorageAttachment, ComputeAllocation, StorageVolume) (StorageAttachment, error)
}

type storageAttachmentReadbackProvider interface {
	ReadStorageAttachment(context.Context, StorageAttachment, ComputeAllocation, StorageVolume) (StorageAttachment, error)
}

type storageVolumeStatusReader interface {
	ReadStorageVolumeStatus(context.Context, StorageVolume) (StorageVolume, error)
}

type computeAllocationReadbackProvider interface {
	ReadComputeAllocation(context.Context, ComputeAllocation) (ComputeAllocation, error)
}

type computeAllocationDiscoveryProvider interface {
	DiscoverComputeAllocation(context.Context, ComputeAllocation, ComputeAllocationPreparation) (ComputeAllocation, error)
}

type normalComputeClaimStageProvider interface {
	TagComputeMachineCVM(context.Context, ProviderMachine, MachineOwnership) error
	ClaimComputeNode(context.Context, ComputeAllocation, MachineOwnership) error
}

type stagedStorageProvider interface {
	CreateCBSVolume(context.Context, StorageVolumeInput) (StorageVolume, error)
	ReadCBSVolume(context.Context, StorageVolumeInput, StorageVolume) (StorageVolume, error)
	ApplyStaticStorageBinding(context.Context, StorageVolume) (StorageVolume, error)
	ReadStaticStorageBinding(context.Context, StorageVolume) (StorageVolume, error)
}

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
	return next
}

type runtimeHealthSummaryProvider interface {
	RuntimeHealthSummary(context.Context) (RuntimeHealthSummary, error)
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
	transfers                        TransferStore
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
	transferStore, _ := operations.(TransferStore)
	if transferStore == nil {
		transferStore = newMemoryTransferStore()
	}
	return &Service{
		provider: provider, computes: computes, volumes: volumes, snapshots: snapshots, attachments: attachments,
		destroying: map[string]bool{}, reconciling: map[string]bool{}, operations: operations, transfers: transferStore,
		now:                           func() time.Time { return time.Now().UTC() },
		computeAllocationPollInterval: computeAllocationPollInterval, computeAllocationPollWindow: computeAllocationPollWindow,
		computeAllocationAttemptTimeout: computeAllocationAttemptTimeout, computeAllocationFinalizeTimeout: computeAllocationFinalizeTimeout,
	}
}

func (s *Service) Catalog(_ context.Context) Catalog {
	return Catalog{
		SchemaVersion: 1,
		Owner:         "OPL Fabric",
		WorkspacePackages: []WorkspacePackage{
			{ID: "basic", Name: "Basic Workspace", ComputeProfileID: "cpu-basic", CPU: 2, MemoryGB: 4, DiskGB: 10, Provider: "tencent-tke", Available: true},
			{ID: "pro", Name: "Pro Workspace", ComputeProfileID: "cpu-pro", CPU: 8, MemoryGB: 16, DiskGB: 100, Provider: "tencent-tke", Available: true},
		},
		StorageClasses: []StorageClass{{ID: "workspace-cbs", StorageClassName: "cbs", Provider: "tencent-tke", Available: true}},
		IngressDomains: []IngressDomain{{ID: "workspace", Host: "workspace.medopl.cn", PathPattern: "/w/<workspaceId>/", Available: true}},
	}
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
	if result.ResourceType != input.ResourceType || result.PackageID != input.PackageID || result.SizeGB != input.SizeGB || result.Zone != input.Zone || !result.Available ||
		result.ChargeType != "PREPAID" || result.PeriodMonths != 1 || result.RenewFlag != "NOTIFY_AND_MANUAL_RENEW" || result.ProviderPriceCNY <= 0 ||
		math.IsNaN(result.ProviderPriceCNY) || math.IsInf(result.ProviderPriceCNY, 0) || !validRequestIDs ||
		(input.ResourceType == "compute" && strings.TrimSpace(result.NodePoolID) == "") {
		return MonthlyPreflight{}, ErrMonthlyPreflightUnavailable
	}
	return result, nil
}

func (s *Service) ReadComputePoolHead(ctx context.Context, nodePoolID string) (ComputePoolHeadReadback, error) {
	result := ComputePoolHeadReadback{SchemaVersion: 1, Status: "unknown", ContinuationState: "unknown", FailureStage: "compute_pool_head", ErrorCode: "fabric_compute_pool_head_unavailable"}
	if !validComputePoolNodePoolID(nodePoolID) {
		return result, ErrInvalidMonthlyPreflight
	}
	head, found, err := s.operations.ComputePoolHead(ctx, nodePoolID)
	if err != nil {
		return result, fmt.Errorf("%w: compute_pool_head_read_failed", ErrMonthlyPreflightUnavailable)
	}
	if !found {
		result.Status, result.ContinuationState, result.FailureStage, result.ErrorCode = "absent", "absent", "none", "none"
		return result, nil
	}
	result.Status = head.Status
	var allocation ComputeAllocation
	plan, hasPlan := decodeComputeAllocationPlan(head)
	if !decodeOperationResource(head, &allocation) || !hasPlan || head.ComputePoolKey != nodePoolID || allocation.NodePoolID != nodePoolID ||
		validateComputeAllocationPreparation(plan, allocation, packagePlan(allocation.PackageID)) != nil || validateNewComputeAllocation(allocation, plan) != nil {
		return result, nil
	}
	if head.Status == "started" {
		result.ContinuationState, result.FailureStage, result.ErrorCode = "continuable", "none", "none"
		return result, nil
	}
	if head.Status != "claim_pending" {
		return result, nil
	}
	_, manualPresent, manualValid := decodeComputeClaimRecoveryMutation(head)
	if manualPresent {
		if manualValid {
			result.ContinuationState, result.ErrorCode = "blocked", "fabric_compute_pool_head_manual_recovery"
		}
		return result, nil
	}
	binding, bindingOK := automaticComputeClaimRecoveryBinding(head, allocation, plan)
	persisted, bindingPresent, bindingValid := decodeComputeClaimRecoveryBinding(head)
	ownership, ownershipErr := s.operations.MachineOwnership(ctx, allocation.ID)
	requestHashRecovery := exactUnmarkedLegacyKubectlClientRejection(head, allocation, plan)
	historicalRecovery := ownershipErr == nil && exactHistoricalComputeClaimRecoveryWithoutLedger(head, allocation, plan, ownership)
	if bindingOK && (requestHashRecovery || historicalRecovery || !bindingPresent || bindingValid && persisted == binding) &&
		ownershipErr == nil && validComputeClaimRecoveryOwnership(allocation, ownership) {
		result.ContinuationState, result.FailureStage, result.ErrorCode = "continuable", "none", "none"
	}
	return result, nil
}

func exactHistoricalComputeClaimRecoveryWithoutLedger(operation FabricOperation, allocation ComputeAllocation, plan ComputeAllocationPreparation, ownership MachineOwnership) bool {
	input, inputOK := automaticComputeClaimRecoveryInput(operation, allocation, plan)
	if !inputOK || allocation.Status != "quarantined" || ownership.Status != "quarantined" ||
		!validComputeClaimRecoveryOwnership(allocation, ownership) {
		return false
	}
	persisted, bindingPresent, bindingValid := decodeComputeClaimRecoveryBinding(operation)
	_, mutationPresent, _ := decodeComputeClaimRecoveryMutation(operation)
	_, reconciliationPresent, _ := decodeComputeClaimRecoveryReconciliation(operation)
	_, clientRejectionPresent, _ := decodeComputeClaimNodeClientRejectionRecovery(operation)
	return bindingPresent && bindingValid && persisted == historicalComputeClaimRecoveryBinding(input) &&
		!mutationPresent && !reconciliationPresent && !clientRejectionPresent
}

func exactUnmarkedLegacyKubectlClientRejection(operation FabricOperation, allocation ComputeAllocation, plan ComputeAllocationPreparation) bool {
	input, inputOK := automaticComputeClaimRecoveryInput(operation, allocation, plan)
	if !inputOK {
		return false
	}
	persisted, bindingPresent, bindingValid := decodeComputeClaimRecoveryBinding(operation)
	if !bindingPresent || !bindingValid {
		return false
	}
	provenance, provenanceOK := isolatedRequestHashReconciliationProvenance(operation, input, persisted, bindingPresent, bindingValid)
	if !provenanceOK || provenance.SchemaVersion != 2 {
		return false
	}
	reconciliation, reconciliationPresent, reconciliationValid := decodeComputeClaimRecoveryReconciliation(operation)
	if !reconciliationPresent || !reconciliationValid || !exactLegacyKubectlClientRejectedReconciliation(reconciliation) ||
		!computeClaimRecoveryReconciliationMatches(reconciliation, operation, input, persisted, computeClaimRecoveryMutationLedger{}) {
		return false
	}
	_, clientRejectionPresent, clientRejectionValid := decodeComputeClaimNodeClientRejectionRecovery(operation)
	return !clientRejectionPresent && !clientRejectionValid
}

type computePoolHeadTerminalizationCandidate struct {
	operation  FabricOperation
	allocation ComputeAllocation
	plan       ComputeAllocationPreparation
	ownership  MachineOwnership
	binding    computeClaimRecoveryBinding
	ledger     computeClaimRecoveryMutationLedger
	readback   ComputePoolHeadTerminalizationReadback
}

func (s *Service) ReadComputePoolHeadTerminalization(ctx context.Context, nodePoolID string) (ComputePoolHeadTerminalizationReadback, error) {
	candidate, err := s.computePoolHeadTerminalizationCandidate(ctx, nodePoolID)
	if err != nil {
		return ComputePoolHeadTerminalizationReadback{SchemaVersion: 1, Status: "blocked"}, err
	}
	return candidate.readback, nil
}

func (s *Service) computePoolHeadTerminalizationCandidate(ctx context.Context, nodePoolID string) (computePoolHeadTerminalizationCandidate, error) {
	if !validComputePoolNodePoolID(nodePoolID) {
		return computePoolHeadTerminalizationCandidate{}, ErrInvalidComputePoolHeadTerminalization
	}
	operation, found, err := s.operations.ComputePoolHead(ctx, nodePoolID)
	if err != nil || !found || operation.Status != "claim_pending" || operation.ComputePoolKey != nodePoolID {
		return computePoolHeadTerminalizationCandidate{}, fmt.Errorf("%w: exact_claim_pending_head_required", ErrComputePoolHeadTerminalizationUnavailable)
	}
	var allocation ComputeAllocation
	plan, hasPlan := decodeComputeAllocationPlan(operation)
	if !decodeOperationResource(operation, &allocation) || !hasPlan || allocation.ID != operation.ResourceID || allocation.NodePoolID != nodePoolID ||
		(allocation.Status != "compute_claim_pending" && allocation.Status != "quarantined") ||
		validateComputeAllocationPreparation(plan, allocation, packagePlan(allocation.PackageID)) != nil || validateNewComputeAllocation(allocation, plan) != nil {
		return computePoolHeadTerminalizationCandidate{}, fmt.Errorf("%w: allocation_identity_invalid", ErrComputePoolHeadTerminalizationUnavailable)
	}
	binding, bindingPresent, bindingValid := decodeComputeClaimRecoveryBinding(operation)
	ledger, ledgerPresent, ledgerValid := decodeComputeClaimRecoveryMutation(operation)
	ownership, ownershipErr := s.operations.MachineOwnership(ctx, allocation.ID)
	if !bindingPresent || !bindingValid || !validComputePoolHeadTerminalizationBinding(operation, binding) || !ledgerPresent || !ledgerValid ||
		ownershipErr != nil || ownership.Status != "quarantined" || !validComputeClaimRecoveryOwnership(allocation, ownership) {
		return computePoolHeadTerminalizationCandidate{}, fmt.Errorf("%w: terminalization_binding_invalid", ErrComputePoolHeadTerminalizationUnavailable)
	}
	bindingDigest := computeClaimIdentityDigest(binding.LaunchOperationID + "|" + binding.IdempotencyKey + "|" + binding.TargetHash + "|" + binding.RequestHash)
	_, _, ledgerDigest := computeClaimMutationLedgerEvidence(operation)
	approvalDigest := hashInput(struct {
		SchemaVersion  int
		OperationID    string
		ResourceID     string
		Status         string
		RequestHash    string
		IdempotencyKey string
		ComputePoolKey string
		Allocation     ComputeAllocation
		Plan           ComputeAllocationPreparation
		Ownership      MachineOwnership
		Binding        computeClaimRecoveryBinding
		Ledger         computeClaimRecoveryMutationLedger
	}{1, operation.OperationID, operation.ResourceID, operation.Status, operation.RequestHash, operation.IdempotencyKey, operation.ComputePoolKey, allocation, plan, ownership, binding, ledger})
	readback := ComputePoolHeadTerminalizationReadback{
		SchemaVersion: 1, Status: "candidate", HeadStatus: operation.Status, AllocationStatus: allocation.Status, OwnershipStatus: ownership.Status,
		ApprovalDigest: approvalDigest, BindingDigest: bindingDigest, ManualRecoveryLedgerDigest: ledgerDigest,
	}
	return computePoolHeadTerminalizationCandidate{operation: operation, allocation: allocation, plan: plan, ownership: ownership, binding: binding, ledger: ledger, readback: readback}, nil
}

func (s *Service) TerminalizeComputePoolHead(ctx context.Context, input ComputePoolHeadTerminalizationInput) (ComputePoolHeadTerminalizationReadback, error) {
	if !validComputePoolNodePoolID(input.NodePoolID) || !validComputePoolTerminalizationToken(input.ApprovalID) ||
		input.IdempotencyKey != input.ApprovalID || !validComputePoolTerminalizationToken(input.IdempotencyKey) || !validSHA256Hex(input.ApprovalDigest) {
		return ComputePoolHeadTerminalizationReadback{}, ErrInvalidComputePoolHeadTerminalization
	}
	if replay, found, err := s.computePoolHeadTerminalizationReplay(ctx, input); found || err != nil {
		return replay, err
	}
	candidate, err := s.computePoolHeadTerminalizationCandidate(ctx, input.NodePoolID)
	if err != nil {
		return ComputePoolHeadTerminalizationReadback{}, err
	}
	if subtle.ConstantTimeCompare([]byte(candidate.readback.ApprovalDigest), []byte(input.ApprovalDigest)) != 1 {
		return ComputePoolHeadTerminalizationReadback{}, ErrComputePoolHeadTerminalizationConflict
	}
	approval := input
	if err := terminalizeComputeClaimPendingWithApproval(ctx, s, candidate.operation, candidate.allocation, candidate.plan, "compute_claim_finalization", "operator_terminalized", nil, nil, &approval); err != nil {
		if replay, found, replayErr := s.computePoolHeadTerminalizationReplay(ctx, input); found || replayErr != nil {
			return replay, replayErr
		}
		return ComputePoolHeadTerminalizationReadback{}, err
	}
	result := candidate.readback
	result.Status, result.HeadStatus, result.TerminalStatus = "succeeded", "failed", "terminal_unprovable"
	return result, nil
}

func (s *Service) ReadComputePoolHeadTerminalizationResult(ctx context.Context, input ComputePoolHeadTerminalizationInput) (ComputePoolHeadTerminalizationReadback, error) {
	if !validComputePoolNodePoolID(input.NodePoolID) || !validComputePoolTerminalizationToken(input.ApprovalID) ||
		input.IdempotencyKey != input.ApprovalID || !validSHA256Hex(input.ApprovalDigest) {
		return ComputePoolHeadTerminalizationReadback{}, ErrInvalidComputePoolHeadTerminalization
	}
	if replay, found, err := s.computePoolHeadTerminalizationReplay(ctx, input); found || err != nil {
		return replay, err
	}
	candidate, err := s.computePoolHeadTerminalizationCandidate(ctx, input.NodePoolID)
	if err != nil {
		return ComputePoolHeadTerminalizationReadback{}, err
	}
	if subtle.ConstantTimeCompare([]byte(candidate.readback.ApprovalDigest), []byte(input.ApprovalDigest)) != 1 {
		return ComputePoolHeadTerminalizationReadback{}, ErrComputePoolHeadTerminalizationConflict
	}
	result := candidate.readback
	result.Status = "pending"
	return result, nil
}

func (s *Service) computePoolHeadTerminalizationReplay(ctx context.Context, input ComputePoolHeadTerminalizationInput) (ComputePoolHeadTerminalizationReadback, bool, error) {
	operations, err := s.operations.List(ctx)
	if err != nil {
		return ComputePoolHeadTerminalizationReadback{}, false, err
	}
	for _, operation := range operations {
		evidence, present, valid := decodeComputeClaimTerminalEvidence(operation)
		if !present || !valid || evidence.OperatorApprovalID != input.ApprovalID && evidence.OperatorIdempotencyKey != input.IdempotencyKey {
			continue
		}
		if evidence.OperatorApprovalID != input.ApprovalID || evidence.OperatorIdempotencyKey != input.IdempotencyKey || evidence.OperatorApprovalDigest != input.ApprovalDigest ||
			operation.ComputePoolKey != input.NodePoolID || operation.Status != "failed" {
			return ComputePoolHeadTerminalizationReadback{}, true, ErrComputePoolHeadTerminalizationConflict
		}
		return ComputePoolHeadTerminalizationReadback{
			SchemaVersion: 1, Status: "succeeded", HeadStatus: "failed", AllocationStatus: "quarantined", OwnershipStatus: "quarantined",
			TerminalStatus: "terminal_unprovable", ApprovalDigest: evidence.OperatorApprovalDigest, BindingDigest: evidence.BindingDigest,
			ManualRecoveryLedgerDigest: evidence.ManualRecoveryLedgerDigest, Replayed: true,
		}, true, nil
	}
	return ComputePoolHeadTerminalizationReadback{}, false, nil
}

func validComputePoolNodePoolID(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 200 && !strings.ContainsAny(value, "\r\n\t ")
}

func validComputePoolTerminalizationToken(value string) bool {
	if len(value) < 16 || len(value) > 120 || value != strings.TrimSpace(value) {
		return false
	}
	for _, character := range value {
		if character != '-' && character != '_' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func validComputePoolHeadTerminalizationBinding(operation FabricOperation, binding computeClaimRecoveryBinding) bool {
	launchOperationID, ok := strings.CutSuffix(strings.TrimSpace(operation.IdempotencyKey), ":compute")
	return ok && launchOperationID != "" && binding.LaunchOperationID == launchOperationID &&
		binding.IdempotencyKey != "" && binding.IdempotencyKey == strings.TrimSpace(binding.IdempotencyKey) && len(binding.IdempotencyKey) <= 200 &&
		validSHA256Hex(binding.TargetHash) && validSHA256Hex(binding.RequestHash)
}

func validSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

func (s *Service) MonthlyPreflightReport(ctx context.Context, input MonthlyPreflightReportInput) (MonthlyPreflightReport, error) {
	provider, ok := s.provider.(monthlyPreflightReportProvider)
	if !ok {
		return MonthlyPreflightReport{}, ErrMonthlyPreflightUnavailable
	}
	report, err := provider.MonthlyPreflightReport(ctx, input)
	if err != nil {
		return MonthlyPreflightReport{}, err
	}
	report.Sub2APIMutationCount = 0
	report.TencentMutationCount = 0
	report.KubernetesMutationCount = 0
	return report, nil
}

func (s *Service) MonthlyProviderTruth(ctx context.Context, computeID, storageID string) (MonthlyProviderTruth, error) {
	computeID, storageID = strings.TrimSpace(computeID), strings.TrimSpace(storageID)
	if computeID == "" || storageID == "" {
		return MonthlyProviderTruth{ComputeState: "unknown", StorageState: "unknown"}, ErrInvalidMonthlyProviderTruth
	}
	s.mu.Lock()
	compute, storage := cloneComputeAllocation(s.computes[computeID]), cloneStorageVolume(s.volumes[storageID])
	s.mu.Unlock()
	unknown := unknownMonthlyProviderTruth(compute, storage)
	if !validMonthlyProviderTruthIdentity(compute, storage) {
		return unknown, fmt.Errorf("%w: local_identity", ErrMonthlyProviderTruthUnavailable)
	}
	provider, ok := s.provider.(monthlyProviderTruthProvider)
	if !ok {
		return unknown, fmt.Errorf("%w: provider_unsupported", ErrMonthlyProviderTruthUnavailable)
	}
	result, err := provider.MonthlyProviderTruth(ctx, compute, storage)
	if err != nil {
		return unknown, fmt.Errorf("%w: %v", ErrMonthlyProviderTruthUnavailable, err)
	}
	if !validMonthlyProviderTruthResult(result, compute, storage) {
		return unknown, fmt.Errorf("%w: provider_identity", ErrMonthlyProviderTruthUnavailable)
	}
	return result, nil
}

// ComputeProviderTruth reads only the Compute Claim provider proof. It is the
// authoritative GET-only collector for the compute stage and deliberately
// keeps a later Storage read failure out of the Compute result.
func (s *Service) ComputeProviderTruth(ctx context.Context, input ComputeClaimRecoveryInput) (ComputeProviderTruth, error) {
	input.AllowExistingStorageOperation = true
	s.mu.Lock()
	compute := cloneComputeAllocation(s.computes[strings.TrimSpace(input.ComputeAllocationID)])
	s.mu.Unlock()
	if compute.ID != strings.TrimSpace(input.ComputeAllocationID) {
		_, persisted, _, _, _, persistedErr := s.computeClaimRecoveryLocalState(ctx, input)
		if persistedErr == nil {
			compute = cloneComputeAllocation(persisted)
		}
	}
	truth := ComputeProviderTruth{
		SchemaVersion: 1, State: "unknown", ComputeState: "unknown", StorageState: "unknown", Compute: compute,
	}
	proof, err := s.ComputeClaimRecoveryProof(ctx, input)
	truth.Reason, truth.FailureStage, truth.ProviderErrorClass = proof.Reason, proof.FailureStage, proof.ProviderErrorClass
	truth.NodeOwnershipState, truth.CVMOwnershipState, truth.Proof = proof.NodeOwnershipState, proof.CVMOwnershipState, &proof
	truth.StorageState = normalizedComputeStorageState(proof.StorageState)
	truth.ProviderRequestID = compute.ProviderRequestID
	if proof.ComputeAllocationID != "" && proof.ComputeAllocationID != compute.ID {
		return truth, fmt.Errorf("%w: compute_identity", ErrComputeClaimRecoveryUnavailable)
	}
	if err != nil {
		return truth, err
	}
	if !proof.Eligible || proof.Reason != "none" {
		return truth, fmt.Errorf("%w: %s", ErrComputeClaimRecoveryUnavailable, firstNonEmpty(proof.Reason, "provider_describe"))
	}
	truth.State, truth.ComputeState = "ready", "ready"
	truth.Compute.Status = "ready"
	truth.Compute.MachineName, truth.Compute.NodeName = proof.MachineName, proof.NodeName
	truth.Compute.CVMInstanceID, truth.Compute.InstanceID = proof.CVMInstanceID, proof.CVMInstanceID
	truth.Compute.PrivateIP, truth.Compute.InstanceType, truth.Compute.Zone = proof.PrivateIP, proof.InstanceType, proof.Zone
	truth.Compute.ChargeType, truth.Compute.RenewFlag, truth.Compute.Deadline = proof.ChargeType, proof.RenewFlag, proof.Deadline
	truth.Compute.ProviderResourceID = proof.CVMInstanceID
	truth.Compute.ProviderRequestID = firstNonEmpty(truth.ProviderRequestID, compute.ProviderRequestID)
	return truth, nil
}

func normalizedComputeStorageState(value string) string {
	switch value {
	case "storage_not_started", "absent":
		return "absent"
	case "storage_existing_exact", "ready":
		return "ready"
	default:
		return "unknown"
	}
}

func (s *Service) ComputeClaimRecoveryProof(ctx context.Context, input ComputeClaimRecoveryInput) (ComputeClaimRecoveryProof, error) {
	proof := newComputeClaimRecoveryProof(input)
	if !validComputeClaimRecoveryInput(input) {
		proof.Reason = "local_identity"
		return proof, ErrInvalidComputeClaimRecovery
	}
	operations, err := s.operations.List(ctx)
	if err != nil {
		proof.Reason = "local_identity"
		return proof, fmt.Errorf("%w: local_identity", ErrComputeClaimRecoveryUnavailable)
	}
	computeOperations := make([]FabricOperation, 0, 1)
	for _, operation := range operations {
		if operation.Action == "create_compute_allocation" && (operation.ResourceID == input.ComputeAllocationID ||
			operation.IdempotencyKey == input.LaunchOperationID+":compute" || operation.AccountID == input.AccountID && operation.WorkspaceID == input.WorkspaceID) {
			computeOperations = append(computeOperations, operation)
		}
	}
	if len(computeOperations) != 1 {
		proof.Reason = "local_identity"
		return proof, fmt.Errorf("%w: local_identity", ErrComputeClaimRecoveryUnavailable)
	}
	operation := computeOperations[0]
	var allocation ComputeAllocation
	plan, hasPlan := decodeComputeAllocationPlan(operation)
	if operation.AccountID != input.AccountID || operation.WorkspaceID != input.WorkspaceID || operation.IdempotencyKey != input.LaunchOperationID+":compute" ||
		(operation.Status != "failed" && operation.Status != "claim_pending" && operation.Status != "succeeded") || !decodeOperationResource(operation, &allocation) || !hasPlan ||
		!validComputeClaimRecoveryLocalIdentity(input, allocation, plan) {
		proof.Reason = "local_identity"
		return proof, fmt.Errorf("%w: local_identity", ErrComputeClaimRecoveryUnavailable)
	}
	ownership, err := s.operations.MachineOwnership(ctx, input.ComputeAllocationID)
	if err != nil || !validComputeClaimRecoveryOwnership(allocation, ownership) {
		proof.Reason = "local_identity"
		return proof, fmt.Errorf("%w: local_identity", ErrComputeClaimRecoveryUnavailable)
	}
	provider, ok := s.provider.(computeClaimRecoveryProvider)
	if !ok {
		proof.Reason = "provider_describe"
		return proof, fmt.Errorf("%w: provider_describe", ErrComputeClaimRecoveryUnavailable)
	}
	providerProof, err := provider.ProveComputeClaimRecovery(ctx, allocation, plan, ownership)
	if err != nil {
		proof.Reason = safeComputeClaimRecoveryReason(providerProof.Reason, "provider_describe")
		if validComputeClaimProviderFailureEvidence(providerProof) {
			proof.FailureStage = providerProof.FailureStage
			proof.ProviderErrorClass = providerProof.ProviderErrorClass
			proof.ProviderIdentityFailure = cloneComputeClaimProviderIdentityFailure(providerProof.ProviderIdentityFailure)
		}
		return proof, fmt.Errorf("%w: %s", ErrComputeClaimRecoveryUnavailable, proof.Reason)
	}
	if !validComputeClaimProviderProof(providerProof, allocation, plan) {
		proof.Reason = safeComputeClaimRecoveryReason(providerProof.Reason, "identity_mismatch")
		return proof, fmt.Errorf("%w: %s", ErrComputeClaimRecoveryUnavailable, proof.Reason)
	}
	// Compute ownership is authoritative for the first incomplete stage. Read
	// CVM and Node before applying any later storage-operation disposition so an
	// attempted or conflicting storage record cannot hide Node ownership truth.
	applyComputeClaimRecoveryProviderProof(&proof, providerProof)
	storageDisposition := computeClaimRecoveryStorageOperationDisposition(operations, input)
	if storageDisposition == computeClaimStorageOperationUnknown && !input.AllowExistingStorageOperation {
		proof.Reason = "storage_already_started"
		return proof, fmt.Errorf("%w: storage_already_started", ErrComputeClaimRecoveryUnavailable)
	}
	if storageDisposition == computeClaimStorageOperationConflict && !input.AllowExistingStorageOperation {
		proof.Reason = "identity_mismatch"
		return proof, fmt.Errorf("%w: identity_mismatch", ErrComputeClaimRecoveryUnavailable)
	}
	if input.AllowExistingStorageOperation && storageDisposition != computeClaimStorageOperationAbsent {
		// Storage is a later stage. Once the compute provider has proved the
		// original CVM and Node identity, an attempted or conflicting storage
		// record remains unknown and cannot block the safe Node-only continuation.
		// The storage worker must reconcile it before any CBS mutation.
		proof.Eligible, proof.Reason, proof.StorageState, proof.StorageProviderResourceID = true, "none", "storage_attempt_unknown", ""
		return proof, nil
	}
	storageProvider, ok := s.provider.(storageRecoveryDiscoveryProvider)
	if !ok {
		if !input.AllowExistingStorageOperation {
			proof.Reason = "provider_describe"
			return proof, fmt.Errorf("%w: provider_describe", ErrComputeClaimRecoveryUnavailable)
		}
		// A missing Storage readback capability is an unknown downstream stage;
		// it must not erase an already authoritative Compute proof.
		proof.Eligible, proof.Reason, proof.StorageState, proof.StorageProviderResourceID = true, "none", "storage_attempt_unknown", ""
		return proof, nil
	}
	storageInput := StorageVolumeInput{
		ID: input.StorageVolumeID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeAllocationID,
		Zone: allocation.Zone, SizeGB: packagePlan(input.PackageID).DiskGB, IdempotencyKey: input.LaunchOperationID + ":storage",
	}
	storageOperation := newOperation(
		"create_storage_volume", "storage_volume", storageInput.ID, storageInput.AccountID, storageInput.WorkspaceID,
		storageInput.IdempotencyKey, hashInput(storageInput), s.now(),
	)
	storageInput.OperationID = storageOperation.OperationID
	storageDiscovery, err := storageProvider.DiscoverStorageRecovery(ctx, storageInput)
	if err != nil {
		if input.AllowExistingStorageOperation && storageDiscovery.MutationCount == 0 {
			// Storage readback is independent of the already-proved Compute
			// ownership. Preserve the unknown stage and let the launch worker
			// reconcile it before any storage mutation.
			proof.Eligible, proof.Reason, proof.StorageState, proof.StorageProviderResourceID = true, "none", "storage_attempt_unknown", ""
			return proof, nil
		}
		proof.Reason = safeComputeClaimRecoveryReason(storageDiscovery.Reason, "provider_describe")
		return proof, fmt.Errorf("%w: %s", ErrComputeClaimRecoveryUnavailable, proof.Reason)
	}
	if !validStorageRecoveryDiscovery(storageDiscovery) {
		if input.AllowExistingStorageOperation && storageDiscovery.MutationCount == 0 {
			proof.Eligible, proof.Reason, proof.StorageState, proof.StorageProviderResourceID = true, "none", "storage_attempt_unknown", ""
			return proof, nil
		}
		proof.Reason = safeComputeClaimRecoveryReason(storageDiscovery.Reason, "identity_mismatch")
		return proof, fmt.Errorf("%w: %s", ErrComputeClaimRecoveryUnavailable, proof.Reason)
	}
	proof.Eligible, proof.Reason, proof.StorageState = true, "none", storageDiscovery.State
	proof.StorageProviderResourceID = storageDiscovery.ProviderResourceID
	return proof, nil
}

func applyComputeClaimRecoveryProviderProof(proof *ComputeClaimRecoveryProof, providerProof ComputeClaimProviderProof) {
	proof.MachineName, proof.NodeName, proof.CVMInstanceID = providerProof.MachineName, providerProof.NodeName, providerProof.CVMInstanceID
	proof.PrivateIP, proof.InstanceType, proof.Zone = providerProof.PrivateIP, providerProof.InstanceType, providerProof.Zone
	proof.ChargeType, proof.PeriodMonths, proof.RenewFlag, proof.Deadline = providerProof.ChargeType, providerProof.PeriodMonths, providerProof.RenewFlag, providerProof.Deadline
	proof.NodeOwnershipState, proof.CVMOwnershipState = providerProof.NodeOwnershipState, providerProof.CVMOwnershipState
}

type computeClaimStorageOperationDisposition string

const (
	computeClaimStorageOperationAbsent   computeClaimStorageOperationDisposition = "absent"
	computeClaimStorageOperationExact    computeClaimStorageOperationDisposition = "exact"
	computeClaimStorageOperationUnknown  computeClaimStorageOperationDisposition = "attempted_unknown"
	computeClaimStorageOperationConflict computeClaimStorageOperationDisposition = "conflict"
)

func computeClaimRecoveryStorageOperationDisposition(operations []FabricOperation, input ComputeClaimRecoveryInput) computeClaimStorageOperationDisposition {
	matches := make([]FabricOperation, 0, 1)
	for _, operation := range operations {
		if operation.Action == "create_storage_volume" &&
			(operation.ResourceID == input.StorageVolumeID || operation.IdempotencyKey == input.LaunchOperationID+":storage" ||
				operation.AccountID == input.AccountID && operation.WorkspaceID == input.WorkspaceID) {
			matches = append(matches, operation)
		}
	}
	if len(matches) == 0 {
		return computeClaimStorageOperationAbsent
	}
	if len(matches) != 1 {
		return computeClaimStorageOperationConflict
	}
	operation := matches[0]
	if operation.ResourceKind != "storage_volume" || operation.ResourceID != input.StorageVolumeID ||
		operation.IdempotencyKey != input.LaunchOperationID+":storage" || operation.AccountID != input.AccountID ||
		operation.WorkspaceID != input.WorkspaceID {
		return computeClaimStorageOperationConflict
	}
	switch operation.Status {
	case "started", "failed", "succeeded":
	default:
		return computeClaimStorageOperationConflict
	}
	if operation.ID == "" || operation.OperationID == "" || operation.RequestHash == "" {
		return computeClaimStorageOperationUnknown
	}
	var storage StorageVolume
	if !decodeOperationResource(operation, &storage) || storage.ID != input.StorageVolumeID ||
		storage.OperationID != input.LaunchOperationID+":storage" || storage.AccountID != input.AccountID || storage.WorkspaceID != input.WorkspaceID {
		return computeClaimStorageOperationUnknown
	}
	return computeClaimStorageOperationExact
}

func (s *Service) ComputeClaimRecoveryIdentityEvidence(ctx context.Context, input ComputeClaimRecoveryClaimInput) (*ComputeClaimIdentityEvidence, error) {
	if !validComputeClaimRecoveryClaimInput(input) {
		return nil, ErrInvalidComputeClaimRecovery
	}
	operation, _, _, _, _, err := s.computeClaimRecoveryLocalState(ctx, input.ComputeClaimRecoveryInput)
	if err != nil {
		return nil, err
	}
	return computeClaimIdentityEvidence(operation, input), nil
}

func validStorageRecoveryDiscovery(discovery StorageRecoveryDiscovery) bool {
	if discovery.MutationCount != 0 || strings.TrimSpace(discovery.ProviderRequestID) == "" {
		return false
	}
	switch discovery.State {
	case "storage_not_started":
		return discovery.ProviderResourceID == "" && discovery.Reason == ""
	case "storage_existing_exact":
		return strings.HasPrefix(discovery.ProviderResourceID, "disk-") && discovery.Reason == ""
	default:
		return false
	}
}

func newComputeClaimRecoveryProof(input ComputeClaimRecoveryInput) ComputeClaimRecoveryProof {
	return ComputeClaimRecoveryProof{
		SchemaVersion: 1, Reason: "local_identity", StorageState: "unknown", LaunchOperationID: input.LaunchOperationID,
		AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ComputeAllocationID: input.ComputeAllocationID,
		StorageVolumeID: input.StorageVolumeID, PackageID: input.PackageID, NodePoolID: input.NodePoolID,
		PoolID: input.PoolID, Evidence: &ComputeClaimEvidence{},
	}
}

func validComputeClaimRecoveryInput(input ComputeClaimRecoveryInput) bool {
	values := []string{input.LaunchOperationID, input.AccountID, input.WorkspaceID, input.ComputeAllocationID, input.StorageVolumeID, input.PackageID, input.PoolID, input.NodePoolID}
	for _, value := range values {
		if value == "" || value != strings.TrimSpace(value) {
			return false
		}
	}
	return input.PackageID == "basic" || input.PackageID == "pro"
}

func validComputeClaimRecoveryLocalIdentity(input ComputeClaimRecoveryInput, allocation ComputeAllocation, plan ComputeAllocationPreparation) bool {
	persistedPeriodMonths := strings.TrimSpace(allocation.ProviderData["periodMonths"])
	if allocation.ID != input.ComputeAllocationID || allocation.AccountID != input.AccountID || allocation.WorkspaceID != input.WorkspaceID ||
		allocation.PackageID != input.PackageID || allocation.Provider != "tencent-tke" || allocation.PoolID != input.PoolID || allocation.NodePoolID != input.NodePoolID ||
		allocation.PoolID != plan.PoolID || plan.PackageID != input.PackageID || plan.NodePoolID != input.NodePoolID || plan.PoolID != packagePlan(input.PackageID).ID ||
		plan.InstanceType != packagePlan(input.PackageID).InstanceType || plan.BeforeMachineNames == nil || plan.BaselineReplicas < 0 || plan.TargetReplicas != plan.BaselineReplicas+1 ||
		int64(len(plan.BeforeMachineNames)) != plan.BaselineReplicas || allocation.MachineName == "" || allocation.InstanceType != plan.InstanceType ||
		!strings.HasPrefix(firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID), "ins-") || allocation.NodeName == "" || allocation.PrivateIP == "" ||
		allocation.Zone == "" || allocation.ChargeType != "PREPAID" || allocation.RenewFlag != "NOTIFY_AND_MANUAL_RENEW" || allocation.Deadline == "" ||
		allocation.ProviderData["instanceType"] != plan.InstanceType || allocation.ProviderData["zone"] != allocation.Zone ||
		allocation.ProviderData["chargeType"] != "PREPAID" || (persistedPeriodMonths != "" && persistedPeriodMonths != "1") ||
		allocation.ProviderData["renewFlag"] != "NOTIFY_AND_MANUAL_RENEW" || allocation.ProviderData["deadline"] != allocation.Deadline ||
		allocation.ProviderData["machineName"] != allocation.MachineName {
		return false
	}
	seen := map[string]bool{}
	for _, name := range plan.BeforeMachineNames {
		if name == "" || seen[name] || name == allocation.MachineName {
			return false
		}
		seen[name] = true
	}
	return true
}

func validComputeClaimRecoveryOwnership(allocation ComputeAllocation, ownership MachineOwnership) bool {
	return ownership.ResourceID == allocation.ID && ownership.AccountID == allocation.AccountID && ownership.WorkspaceID == allocation.WorkspaceID &&
		ownership.PackageID == allocation.PackageID && ownership.NodePoolID == allocation.NodePoolID && ownership.MachineID == allocation.MachineName &&
		ownership.InstanceID == firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID) && ownership.NodeName == allocation.NodeName &&
		ownership.ReleasedAt == nil && (ownership.Status == "quarantined" || ownership.Status == "active")
}

func validComputeClaimProviderProof(proof ComputeClaimProviderProof, allocation ComputeAllocation, plan ComputeAllocationPreparation) bool {
	deadline, deadlineErr := time.Parse(time.RFC3339, proof.Deadline)
	return proof.Status == "proven" && (proof.NodeOwnershipState == "unallocated" || proof.NodeOwnershipState == "target_owned") &&
		(proof.CVMOwnershipState == "recoverable" || proof.CVMOwnershipState == "target_owned") &&
		proof.MachineName == allocation.MachineName && proof.NodeName == allocation.NodeName && proof.CVMInstanceID == firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID) &&
		proof.PrivateIP == allocation.PrivateIP && proof.InstanceType == plan.InstanceType && proof.Zone == allocation.Zone && proof.ChargeType == "PREPAID" &&
		proof.PeriodMonths == 1 && proof.RenewFlag == "NOTIFY_AND_MANUAL_RENEW" && proof.Deadline == allocation.Deadline && deadlineErr == nil && !deadline.IsZero() &&
		proof.FailureStage == "" && proof.ProviderErrorClass == "" && proof.ProviderIdentityFailure == nil
}

func validComputeClaimProviderFailureEvidence(proof ComputeClaimProviderProof) bool {
	return proof.Reason == "identity_mismatch" && proof.FailureStage != "" && validComputeClaimFailureStage(proof.FailureStage) &&
		proof.ProviderErrorClass != "" && validComputeClaimProviderErrorClass(proof.ProviderErrorClass) &&
		validComputeClaimProviderIdentityFailure(proof.ProviderIdentityFailure)
}

func validComputeClaimProviderIdentityFailure(value *ComputeClaimProviderIdentityFailure) bool {
	if value == nil || !validComputeClaimProviderIdentityPredicate(value.Predicate) || value.ExpectedDigest == value.ActualDigest {
		return false
	}
	for _, digest := range []string{value.ExpectedDigest, value.ActualDigest} {
		if len(digest) != 64 {
			return false
		}
		if _, err := hex.DecodeString(digest); err != nil || strings.ToLower(digest) != digest {
			return false
		}
	}
	return true
}

func validComputeClaimProviderIdentityPredicate(value string) bool {
	switch value {
	case "compute_claim.request_contract", "compute_claim.machine_selection", "compute_claim.node_pool_identity",
		"compute_claim.machine_identity", "compute_claim.tke_instance_identity", "compute_claim.network_identity",
		"compute_claim.cvm_identity", "compute_claim.cvm_billing", "compute_claim.cvm_ownership_shape",
		"compute_claim.cvm_ownership.instance_name", "compute_claim.cvm_ownership.opl_account_id",
		"compute_claim.cvm_ownership.opl_workspace_id", "compute_claim.cvm_ownership.opl_resource_id",
		"compute_claim.cvm_ownership.opl_operation_id", "compute_claim.provider_response_identity",
		"compute_claim.kubernetes_node_identity":
		return true
	default:
		return false
	}
}

func cloneComputeClaimProviderIdentityFailure(value *ComputeClaimProviderIdentityFailure) *ComputeClaimProviderIdentityFailure {
	if !validComputeClaimProviderIdentityFailure(value) {
		return nil
	}
	clone := *value
	return &clone
}

func safeComputeClaimRecoveryReason(value, fallback string) string {
	switch value {
	case "local_identity", "provider_describe", "iam_rbac", "multiple_candidate", "identity_mismatch", "node_ownership_conflict", "storage_already_started":
		return value
	default:
		return fallback
	}
}

func validComputeClaimFailureStage(value string) bool {
	switch value {
	case "", "cvm_pre_read", "cvm_conflict_check", "cvm_mutation_precondition", "cvm_rename_readback", "cvm_tag_readback", "cvm_final_readback",
		"cvm_provisioner_transport", "cvm_mutation_evidence", "node_pre_cvm_read", "node_pre_read", "node_conflict_check", "node_patch_build",
		"node_patch_readback", "node_final_readback", "claim_final_readback":
		return true
	default:
		return false
	}
}

func validComputeClaimProviderErrorClass(value string) bool {
	switch value {
	case "", "client_unavailable", "malformed_readback", "ownership_conflict", "readback_mismatch", "timeout", "iam_rbac", "provider_error",
		"transport_error", "evidence_incomplete":
		return true
	default:
		return false
	}
}

func validComputeClaimMutationEvidence(evidence ComputeClaimMutationEvidence, count, maximum int, domain string) bool {
	return validComputeClaimMutationEvidenceShape(evidence, count, maximum, domain) && evidence.Unknown == 0 &&
		evidence.Confirmed == evidence.Attempted && len(evidence.Missing) == 0
}

func validComputeClaimMissingField(domain, field string) bool {
	switch domain {
	case "cvm":
		switch field {
		case "instance", "instance_name", "opl_account_id", "opl_workspace_id", "opl_resource_id", "opl_operation_id":
			return true
		}
	case "node":
		return field == "node_ownership"
	}
	return false
}

func validComputeClaimMutationEvidenceShape(evidence ComputeClaimMutationEvidence, count, maximum int, domain string) bool {
	if count < 0 || count > maximum || evidence.Attempted != count || evidence.Confirmed < 0 || evidence.Confirmed > evidence.Attempted ||
		evidence.Unknown < 0 || evidence.Unknown > evidence.Attempted || evidence.Confirmed+evidence.Unknown > evidence.Attempted {
		return false
	}
	seen := map[string]bool{}
	for _, field := range evidence.Missing {
		if !validComputeClaimMissingField(domain, field) || seen[field] {
			return false
		}
		seen[field] = true
	}
	return true
}

func validComputeClaimEvidence(claim ComputeClaimProviderClaim) bool {
	return claim.Evidence != nil && validComputeClaimFailureStage(claim.FailureStage) && validComputeClaimProviderErrorClass(claim.ProviderErrorClass) &&
		validComputeClaimMutationEvidence(claim.Evidence.CVM, claim.TencentMutationCount, 5, "cvm") &&
		validComputeClaimMutationEvidence(claim.Evidence.Node, claim.KubernetesMutationCount, 1, "node") &&
		claim.FailureStage == "" && claim.ProviderErrorClass == ""
}

func (s *Service) ClaimComputeRecovery(ctx context.Context, input ComputeClaimRecoveryClaimInput) (ComputeClaimRecoveryProof, error) {
	result := newComputeClaimRecoveryProof(input.ComputeClaimRecoveryInput)
	if !validComputeClaimRecoveryClaimInput(input) {
		result.Reason = "local_identity"
		return result, ErrInvalidComputeClaimRecovery
	}
	err := s.operations.WithPoolLock(ctx, workspaceLaunchResourceLockKey(input.LaunchOperationID), func(lockCtx context.Context) error {
		operation, allocation, plan, ownership, localReason, err := s.computeClaimRecoveryLocalState(lockCtx, input.ComputeClaimRecoveryInput)
		if err != nil {
			result.Eligible, result.Reason = false, safeComputeClaimRecoveryReason(localReason, "local_identity")
			return err
		}
		binding := newComputeClaimRecoveryBinding(input)
		persistedBinding, bindingPresent, bindingValid := decodeComputeClaimRecoveryBinding(operation)
		mutationLedger, mutationPresent, mutationValid := decodeComputeClaimRecoveryMutation(operation)
		reconciliation, reconciliationPresent, reconciliationValid := decodeComputeClaimRecoveryReconciliation(operation)
		clientRejection, clientRejectionPresent, clientRejectionValid := decodeComputeClaimNodeClientRejectionRecovery(operation)
		historicalBinding := bindingPresent && bindingValid && persistedBinding != binding &&
			persistedBinding == historicalComputeClaimRecoveryBinding(input)
		reconciliationProvenance, requestHashBinding := isolatedRequestHashReconciliationProvenance(operation, input, persistedBinding, bindingPresent, bindingValid)
		reconciliationCandidate := requestHashBinding && allocation.Status == "quarantined" && ownership.Status == "quarantined" &&
			(operation.Status == "claim_pending" && reconciliationProvenance.SchemaVersion == 1 ||
				operation.Status == "failed" && reconciliationProvenance.SchemaVersion == 2)
		reconciliationReplay := requestHashBinding && reconciliationPresent && reconciliationValid &&
			(reconciliation.SchemaVersion == 1 && mutationPresent && mutationValid || reconciliation.SchemaVersion == 2 && !mutationPresent) &&
			computeClaimRecoveryReconciliationMatches(reconciliation, operation, input, persistedBinding, mutationLedger)
		requestHashReconciliation := reconciliationCandidate || reconciliationReplay
		historicalWithoutLedger := historicalBinding && !mutationPresent
		historicalNodeContinuation := historicalBinding && mutationPresent && mutationValid && confirmedCVMOnlyObservedComputeClaimRecoveryMutation(mutationLedger)
		historicalReservedReplay := historicalBinding && mutationPresent && mutationValid && validNodeReservedComputeClaimRecoveryMutation(mutationLedger)
		historicalCompletedReplay := historicalBinding && mutationPresent && mutationValid && successfulNodeClaimRecoveryMutation(mutationLedger) && ownership.Status == "active"
		if bindingPresent && (!bindingValid || persistedBinding != binding && !historicalWithoutLedger && !historicalNodeContinuation && !historicalReservedReplay && !historicalCompletedReplay &&
			!requestHashReconciliation) {
			result.Eligible, result.Reason = false, "identity_mismatch"
			return ErrComputeClaimRecoveryIdempotencyConflict
		}
		if mutationPresent && (!mutationValid || !bindingPresent) || reconciliationPresent && (!reconciliationValid || !reconciliationReplay) ||
			clientRejectionPresent && (!clientRejectionValid || !reconciliationPresent) {
			result.Eligible, result.Reason = false, "identity_mismatch"
			return ErrComputeClaimRecoveryIdempotencyConflict
		}
		proof, proofErr := s.ComputeClaimRecoveryProof(lockCtx, input.ComputeClaimRecoveryInput)
		result = proof
		if proofErr != nil {
			if mutationPresent && result.Reason != "local_identity" && result.Reason != "storage_already_started" {
				readbackReason := result.Reason
				applyComputeClaimRecoveryReplayFailure(&result, mutationLedger, readbackReason)
				return fmt.Errorf("%w: %s", ErrComputeClaimRecoveryUnavailable, result.Reason)
			}
			return proofErr
		}
		if input.MachineName != proof.MachineName || input.NodeName != proof.NodeName || input.CVMInstanceID != proof.CVMInstanceID || input.PrivateIP != proof.PrivateIP ||
			input.InstanceType != proof.InstanceType || input.Zone != proof.Zone || input.PoolID != proof.PoolID {
			result.Eligible, result.Reason = false, "identity_mismatch"
			return fmt.Errorf("%w: identity_mismatch", ErrComputeClaimRecoveryUnavailable)
		}
		reconciledCVMOwnership := requestHashReconciliation &&
			(proof.CVMOwnershipState == "recoverable" || proof.CVMOwnershipState == "target_owned")
		if requestHashReconciliation {
			if !reconciledCVMOwnership || (proof.NodeOwnershipState != "unallocated" && proof.NodeOwnershipState != "target_owned") {
				result.Eligible, result.Reason = false, "identity_mismatch"
				return fmt.Errorf("%w: identity_mismatch", ErrComputeClaimRecoveryUnavailable)
			}
			expectedReconciliation := newComputeClaimRecoveryReconciliation(operation, allocation, plan, ownership, input, proof, persistedBinding, mutationLedger)
			if reconciliationPresent {
				if reconciliation.AuthorityDigest != expectedReconciliation.AuthorityDigest {
					result.Eligible, result.Reason = false, "identity_mismatch"
					return ErrComputeClaimRecoveryIdempotencyConflict
				}
			} else {
				verified := operation
				if verified.Status == "failed" {
					verified.Status, verified.ErrorCode, verified.FinishedAt = "claim_pending", "", time.Time{}
				}
				verified.RedactedProviderPayload = withComputeClaimRecoveryReconciliation(verified.RedactedProviderPayload, expectedReconciliation)
				if err := s.operations.SaveComputeClaimRecovery(lockCtx, operation, verified); err != nil {
					result.Eligible, result.Reason = false, "local_identity"
					return err
				}
				operation, reconciliation, reconciliationPresent = verified, expectedReconciliation, true
			}
		}
		if !bindingPresent {
			if operation.Status == "succeeded" {
				result.Eligible, result.Reason = false, "identity_mismatch"
				return ErrComputeClaimRecoveryIdempotencyConflict
			}
			pending := operation
			pending.Status, pending.ErrorCode, pending.FinishedAt = "claim_pending", "", time.Time{}
			pending.RedactedProviderPayload = withComputeClaimRecoveryBinding(operation.RedactedProviderPayload, binding)
			if err := s.operations.SaveComputeClaimRecovery(lockCtx, operation, pending); err != nil {
				result.Eligible, result.Reason = false, "local_identity"
				return err
			}
			operation = pending
		}
		reserveHistoricalNodeClaim := historicalWithoutLedger && ownership.Status != "active" &&
			proof.CVMOwnershipState == "target_owned" && proof.NodeOwnershipState == "unallocated"
		if historicalWithoutLedger && !reserveHistoricalNodeClaim {
			result.Eligible, result.Reason = false, "identity_mismatch"
			return ErrComputeClaimRecoveryIdempotencyConflict
		}
		if mutationPresent && mutationLedger.State == "reserved" {
			applyComputeClaimRecoveryMutation(&result, mutationLedger)
			return fmt.Errorf("%w: %s", ErrComputeClaimRecoveryUnavailable, result.Reason)
		}
		resumeObservedNodeClaim := mutationPresent &&
			(recoverableObservedComputeClaimRecoveryMutation(mutationLedger) || historicalNodeContinuation) &&
			proof.CVMOwnershipState == "target_owned" && proof.NodeOwnershipState == "unallocated"
		legacyClientRejectedNodeCall := requestHashReconciliation && reconciliationPresent && !clientRejectionPresent &&
			exactLegacyKubectlClientRejectedReconciliation(reconciliation) && reconciledCVMOwnership && proof.NodeOwnershipState == "unallocated"
		reconciledNodeContinuation := requestHashReconciliation && reconciliationPresent &&
			(reconciliation.State == "verified" || legacyClientRejectedNodeCall) &&
			ownership.Status != "active" && reconciledCVMOwnership && proof.NodeOwnershipState == "unallocated"
		activeNodeContinuation := ownership.Status == "active" && !mutationPresent && bindingPresent && bindingValid && persistedBinding == binding &&
			proof.CVMOwnershipState == "target_owned" && proof.NodeOwnershipState == "unallocated"
		createBudget, createPresent, createValid := normalLaunchStageBudget(operation.RedactedProviderPayload, "compute_create")
		cvmBudget, cvmPresent, cvmValid := normalLaunchStageBudget(operation.RedactedProviderPayload, "compute_claim_cvm")
		_, nodePresent, nodeValid := normalLaunchStageBudget(operation.RedactedProviderPayload, "compute_claim_node")
		currentNodeContinuation := operation.Status == "claim_pending" && !mutationPresent &&
			proof.CVMOwnershipState == "target_owned" && proof.NodeOwnershipState == "unallocated" &&
			createPresent && createValid && createBudget == confirmedNormalLaunchMutationBudget() &&
			cvmPresent && cvmValid && cvmBudget == confirmedNormalLaunchMutationBudget() && !nodePresent && nodeValid
		nodeOnlyContinuation := resumeObservedNodeClaim || reserveHistoricalNodeClaim || activeNodeContinuation || reconciledNodeContinuation || currentNodeContinuation
		resumeReservedNodeReadback := mutationPresent && validNodeReservedComputeClaimRecoveryMutation(mutationLedger) &&
			proof.CVMOwnershipState == "target_owned" && proof.NodeOwnershipState == "target_owned"
		reconciledNodeReadback := requestHashReconciliation && reconciliationPresent &&
			(reconciliation.State == "verified" || reconciliation.State == "node_reserved" || reconciliation.State == "observed") &&
			reconciledCVMOwnership && proof.NodeOwnershipState == "target_owned"
		reservedNodeOutcomeUnknown := mutationPresent && validNodeReservedComputeClaimRecoveryMutation(mutationLedger) &&
			proof.CVMOwnershipState == "target_owned" && proof.NodeOwnershipState == "unallocated"
		if reservedNodeOutcomeUnknown {
			applyComputeClaimRecoveryMutation(&result, mutationLedger)
			return fmt.Errorf("%w: %s", ErrComputeClaimRecoveryUnavailable, result.Reason)
		}
		if mutationPresent && mutationLedger.State == "node_reserved" && !resumeReservedNodeReadback {
			result.Eligible, result.Reason = false, "identity_mismatch"
			return ErrComputeClaimRecoveryIdempotencyConflict
		}
		if requestHashReconciliation && reconciliationPresent && !legacyClientRejectedNodeCall &&
			(reconciliation.State == "node_reserved" || reconciliation.State == "observed") && !reconciledNodeReadback {
			result.Eligible, result.Reason = false, "provider_describe"
			result.FailureStage, result.ProviderErrorClass = reconciliation.FailureStage, reconciliation.ProviderErrorClass
			result.Evidence = &ComputeClaimEvidence{CVM: cloneComputeClaimMutationEvidence(mutationLedger.Evidence.CVM), Node: cloneComputeClaimMutationEvidence(reconciliation.Node)}
			return fmt.Errorf("%w: provider_describe", ErrComputeClaimRecoveryUnavailable)
		}
		if ownership.Status == "active" && !activeNodeContinuation {
			if proof.CVMOwnershipState != "target_owned" && !reconciledCVMOwnership || proof.NodeOwnershipState != "target_owned" {
				result.Eligible, result.Reason = false, "identity_mismatch"
				return fmt.Errorf("%w: identity_mismatch", ErrComputeClaimRecoveryUnavailable)
			}
		} else if !reconciledNodeReadback &&
			(activeNodeContinuation || proof.CVMOwnershipState != "target_owned" || proof.NodeOwnershipState != "target_owned") {
			provider, providerOK := s.provider.(computeClaimRecoveryClaimProvider)
			nodeOnlyProvider, nodeOnlyProviderOK := s.provider.(computeClaimRecoveryNodeOnlyProvider)
			if nodeOnlyContinuation && !nodeOnlyProviderOK || !nodeOnlyContinuation && !providerOK {
				result.Eligible, result.Reason = false, "provider_describe"
				return fmt.Errorf("%w: provider_describe", ErrComputeClaimRecoveryUnavailable)
			}
			if mutationPresent && !resumeObservedNodeClaim && !reconciledNodeContinuation {
				applyComputeClaimRecoveryReplayFailure(&result, mutationLedger, proof.Reason)
				return fmt.Errorf("%w: %s", ErrComputeClaimRecoveryUnavailable, result.Reason)
			}
			if nodeOnlyContinuation {
				if reconciledNodeContinuation {
					var rejectedCall computeClaimNodeClientRejectionRecovery
					if legacyClientRejectedNodeCall {
						rejectedCall = newComputeClaimNodeClientRejectionRecovery(reconciliation)
						clientRejection, clientRejectionPresent = rejectedCall, true
					}
					reconciliation.State, reconciliation.FailureStage, reconciliation.ProviderErrorClass = "node_reserved", "node_patch_readback", "transport_error"
					reconciliation.Node = ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"node_ownership"}}
					reserved := operation
					reserved.RedactedProviderPayload = withComputeClaimRecoveryReconciliation(reserved.RedactedProviderPayload, reconciliation)
					if legacyClientRejectedNodeCall {
						reserved.RedactedProviderPayload = withComputeClaimNodeClientRejectionRecovery(reserved.RedactedProviderPayload, rejectedCall)
					}
					if err := s.operations.SaveComputeClaimRecovery(lockCtx, operation, reserved); err != nil {
						result.Eligible, result.Reason = false, "local_identity"
						return err
					}
					operation = reserved
				} else if reserveHistoricalNodeClaim || activeNodeContinuation || currentNodeContinuation {
					mutationLedger = legacyNodeReservedComputeClaimRecoveryMutation()
				} else {
					mutationLedger = nodeReservedComputeClaimRecoveryMutation(mutationLedger)
				}
				if !reconciledNodeContinuation {
					reserved := operation
					reserved.RedactedProviderPayload = withComputeClaimRecoveryMutation(reserved.RedactedProviderPayload, mutationLedger)
					if err := s.operations.SaveComputeClaimRecovery(lockCtx, operation, reserved); err != nil {
						result.Eligible, result.Reason = false, "local_identity"
						return err
					}
					operation = reserved
				}
			}
			if !nodeOnlyContinuation {
				mutationLedger = reservedComputeClaimRecoveryMutation()
				reserved := operation
				reserved.RedactedProviderPayload = withComputeClaimRecoveryMutation(operation.RedactedProviderPayload, mutationLedger)
				if err := s.operations.SaveComputeClaimRecovery(lockCtx, operation, reserved); err != nil {
					result.Eligible, result.Reason = false, "local_identity"
					return err
				}
				operation = reserved
			}
			if !reconciledNodeContinuation {
				mutationPresent = true
			}
			var claimed ComputeClaimProviderClaim
			var claimErr error
			if nodeOnlyContinuation {
				claimed, claimErr = nodeOnlyProvider.ClaimComputeRecoveryNodeOnly(lockCtx, allocation, plan, ownership)
			} else {
				claimed, claimErr = provider.ClaimComputeRecovery(lockCtx, allocation, plan, ownership)
			}
			result.TencentMutationCount = max(0, claimed.TencentMutationCount)
			result.KubernetesMutationCount = max(0, claimed.KubernetesMutationCount)
			result.FailureStage = claimed.FailureStage
			result.ProviderErrorClass = claimed.ProviderErrorClass
			if claimed.Evidence != nil {
				result.Evidence = &ComputeClaimEvidence{
					CVM:  cloneComputeClaimMutationEvidence(claimed.Evidence.CVM),
					Node: cloneComputeClaimMutationEvidence(claimed.Evidence.Node),
				}
			}
			claimedCVMOwnership := claimed.Proof.CVMOwnershipState == "target_owned" ||
				reconciledNodeContinuation && claimed.Proof.CVMOwnershipState == "recoverable"
			claimSucceeded := claimErr == nil && validComputeClaimProviderProof(claimed.Proof, allocation, plan) &&
				claimedCVMOwnership && claimed.Proof.NodeOwnershipState == "target_owned" && validComputeClaimEvidence(claimed)
			if nodeOnlyContinuation {
				claimSucceeded = claimSucceeded && claimed.TencentMutationCount == 0 &&
					reflect.DeepEqual(claimed.Evidence.CVM, ComputeClaimMutationEvidence{})
			}
			if !claimSucceeded {
				result.Eligible = false
				result.Reason = safeComputeClaimRecoveryReason(claimed.Proof.Reason, "identity_mismatch")
				if claimErr != nil && claimed.Proof.Reason == "" {
					result.Reason = "provider_describe"
				}
			} else {
				result.MachineName, result.NodeName, result.CVMInstanceID = claimed.Proof.MachineName, claimed.Proof.NodeName, claimed.Proof.CVMInstanceID
				result.PrivateIP, result.InstanceType, result.Zone = claimed.Proof.PrivateIP, claimed.Proof.InstanceType, claimed.Proof.Zone
				result.ChargeType, result.PeriodMonths, result.RenewFlag, result.Deadline = claimed.Proof.ChargeType, claimed.Proof.PeriodMonths, claimed.Proof.RenewFlag, claimed.Proof.Deadline
				result.NodeOwnershipState, result.CVMOwnershipState, result.Eligible, result.Reason = "target_owned", "target_owned", true, "none"
			}
			if reconciledNodeContinuation {
				reconciliation.State = "observed"
				reconciliation.FailureStage, reconciliation.ProviderErrorClass = result.FailureStage, result.ProviderErrorClass
				if result.Evidence != nil && validComputeClaimMutationEvidenceShape(result.Evidence.Node, result.KubernetesMutationCount, 1, "node") {
					reconciliation.Node = cloneComputeClaimMutationEvidence(result.Evidence.Node)
				}
				if claimSucceeded {
					reconciliation.State, reconciliation.FailureStage, reconciliation.ProviderErrorClass = "succeeded", "", ""
				}
			} else if nodeOnlyContinuation {
				mutationLedger = observedNodeClaimRecoveryMutation(mutationLedger, result)
			} else {
				mutationLedger = observedComputeClaimRecoveryMutation(result)
			}
			observed := operation
			if reconciledNodeContinuation {
				observed.RedactedProviderPayload = withComputeClaimRecoveryReconciliation(operation.RedactedProviderPayload, reconciliation)
			} else {
				observed.RedactedProviderPayload = withComputeClaimRecoveryMutation(operation.RedactedProviderPayload, mutationLedger)
			}
			if err := s.operations.SaveComputeClaimRecovery(lockCtx, operation, observed); err != nil {
				result.Eligible, result.Reason = false, "local_identity"
				return err
			}
			operation = observed
			if !claimSucceeded {
				if !reconciledNodeContinuation {
					applyComputeClaimRecoveryMutation(&result, mutationLedger)
				}
				return fmt.Errorf("%w: %s", ErrComputeClaimRecoveryUnavailable, result.Reason)
			}
		}
		if mutationPresent && mutationLedger.State == "node_reserved" && proof.NodeOwnershipState == "target_owned" {
			mutationLedger = observedNodeClaimReadbackMutation(mutationLedger)
		}
		if requestHashReconciliation && reconciliationPresent && reconciliation.State != "succeeded" && proof.NodeOwnershipState == "target_owned" {
			reconciliation.State, reconciliation.FailureStage, reconciliation.ProviderErrorClass = "succeeded", "", ""
			if reconciliation.Node.Attempted == 1 {
				reconciliation.Node = ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1}
			} else {
				reconciliation.Node = ComputeClaimMutationEvidence{}
			}
		}
		if requestHashReconciliation && reconciliationPresent && reconciliation.State == "succeeded" {
			result.NodeOwnershipState, result.CVMOwnershipState, result.Eligible, result.Reason = "target_owned", "target_owned", true, "none"
		}
		allocation.Status = "ready"
		allocation.CostTags = oplCostTags(allocation.AccountID, allocation.WorkspaceID, allocation.ID, ownership.ID)
		allocation.NodeSelector = tkeNodeSelector(allocation.ProviderData, allocation.NodeName)
		ownership.Status, ownership.ReleasedAt = "active", nil
		if err := s.operations.ActivateComputeClaimRecoveryOwnership(lockCtx, ownership); err != nil {
			result.Eligible, result.Reason = false, "local_identity"
			return err
		}
		if operation.Status != "succeeded" {
			recovered := operation
			recovered.Status, recovered.ErrorCode, recovered.FinishedAt = "succeeded", "", s.now()
			finalBinding := binding
			if historicalBinding || requestHashReconciliation {
				finalBinding = persistedBinding
			}
			recovered.RedactedProviderPayload = preserveNormalLaunchMutationBudget(computeAllocationOperationPayload(allocation, plan), operation.RedactedProviderPayload)
			recovered.RedactedProviderPayload = withComputeClaimRecoveryBinding(recovered.RedactedProviderPayload, finalBinding)
			if terminal, present, valid := decodeComputeClaimTerminalEvidence(operation); present && valid {
				recovered.RedactedProviderPayload = withComputeClaimTerminalEvidence(recovered.RedactedProviderPayload, terminal)
			}
			if mutationPresent {
				recovered.RedactedProviderPayload = withComputeClaimRecoveryMutation(recovered.RedactedProviderPayload, mutationLedger)
			}
			if requestHashReconciliation && reconciliationPresent {
				recovered.RedactedProviderPayload = withComputeClaimRecoveryReconciliation(recovered.RedactedProviderPayload, reconciliation)
			}
			if clientRejectionPresent {
				recovered.RedactedProviderPayload = withComputeClaimNodeClientRejectionRecovery(recovered.RedactedProviderPayload, clientRejection)
			}
			if err := s.operations.SaveComputeClaimRecovery(lockCtx, operation, recovered); err != nil {
				result.Eligible, result.Reason = false, "local_identity"
				return err
			}
		}
		s.mu.Lock()
		s.computes[allocation.ID] = allocation
		s.mu.Unlock()
		return nil
	})
	if evidence, evidenceErr := s.ComputeClaimRecoveryIdentityEvidence(ctx, input); evidenceErr == nil {
		result.IdentityEvidence = evidence
	}
	return result, err
}

func validComputeClaimRecoveryClaimInput(input ComputeClaimRecoveryClaimInput) bool {
	if !validComputeClaimRecoveryInput(input.ComputeClaimRecoveryInput) {
		return false
	}
	for _, value := range []string{input.MachineName, input.NodeName, input.CVMInstanceID, input.PrivateIP, input.InstanceType, input.Zone, input.IdempotencyKey} {
		if value == "" || value != strings.TrimSpace(value) {
			return false
		}
	}
	return strings.HasPrefix(input.CVMInstanceID, "ins-") && input.IdempotencyKey == input.LaunchOperationID+":compute"
}

type computeClaimRecoveryBinding struct {
	LaunchOperationID string `json:"launchOperationId"`
	IdempotencyKey    string `json:"idempotencyKey"`
	TargetHash        string `json:"targetHash"`
	RequestHash       string `json:"requestHash"`
}

const (
	computeClaimRecoveryMutationPayloadKey       = "computeClaimRecoveryMutation"
	computeClaimRecoveryReconciliationPayloadKey = "computeClaimRecoveryReconciliation"
	computeClaimTerminalEvidencePayloadKey       = "computeClaimTerminalEvidence"
	computeClaimNodeClientRejectionPayloadKey    = "computeClaimNodeClientRejectionRecovery"
)

type computeClaimRecoveryReconciliation struct {
	SchemaVersion              int                          `json:"schemaVersion"`
	Consumer                   string                       `json:"consumer"`
	Generation                 string                       `json:"generation"`
	ProvenanceSource           string                       `json:"provenanceSource,omitempty"`
	ProvenanceDigest           string                       `json:"provenanceDigest,omitempty"`
	State                      string                       `json:"state"`
	BindingDigest              string                       `json:"bindingDigest"`
	ExpectedRequestHashDigest  string                       `json:"expectedRequestHashDigest"`
	PersistedRequestHashDigest string                       `json:"persistedRequestHashDigest"`
	MutationLedgerDigest       string                       `json:"mutationLedgerDigest"`
	AuthorityDigest            string                       `json:"authorityDigest"`
	FailureStage               string                       `json:"failureStage,omitempty"`
	ProviderErrorClass         string                       `json:"providerErrorClass,omitempty"`
	Node                       ComputeClaimMutationEvidence `json:"node"`
}

type computeClaimNodeClientRejectionRecovery struct {
	SchemaVersion              int    `json:"schemaVersion"`
	Classification             string `json:"classification"`
	Invocation                 string `json:"invocation"`
	RecordedCalls              int    `json:"recordedCalls"`
	APIAcceptedMutations       int    `json:"apiAcceptedMutations"`
	SourceReconciliationDigest string `json:"sourceReconciliationDigest"`
}

type computeClaimRecoveryReconciliationProvenance struct {
	SchemaVersion int
	Generation    string
	Source        string
	Digest        string
}

type computeClaimRecoveryMutationLedger struct {
	State                   string               `json:"state"`
	Reason                  string               `json:"reason"`
	TencentMutationCount    int                  `json:"tencentMutationCount"`
	KubernetesMutationCount int                  `json:"kubernetesMutationCount"`
	FailureStage            string               `json:"failureStage,omitempty"`
	ProviderErrorClass      string               `json:"providerErrorClass,omitempty"`
	Evidence                ComputeClaimEvidence `json:"evidence"`
}

func reservedComputeClaimRecoveryMutation() computeClaimRecoveryMutationLedger {
	return computeClaimRecoveryMutationLedger{
		State: "reserved", Reason: "provider_describe", TencentMutationCount: 5, KubernetesMutationCount: 1,
		FailureStage: "cvm_provisioner_transport", ProviderErrorClass: "transport_error",
		Evidence: ComputeClaimEvidence{
			CVM: ComputeClaimMutationEvidence{
				Attempted: 5, Unknown: 5,
				Missing: []string{"instance_name", "opl_account_id", "opl_workspace_id", "opl_resource_id", "opl_operation_id"},
			},
			Node: ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"node_ownership"}},
		},
	}
}

func nodeReservedComputeClaimRecoveryMutation(observed computeClaimRecoveryMutationLedger) computeClaimRecoveryMutationLedger {
	return computeClaimRecoveryMutationLedger{
		State: "node_reserved", Reason: "provider_describe", TencentMutationCount: observed.TencentMutationCount, KubernetesMutationCount: 1,
		FailureStage: "node_patch_readback", ProviderErrorClass: "transport_error",
		Evidence: ComputeClaimEvidence{
			CVM: ComputeClaimMutationEvidence{
				Attempted: observed.Evidence.CVM.Attempted, Confirmed: observed.Evidence.CVM.Attempted,
			},
			Node: ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"node_ownership"}},
		},
	}
}

func legacyNodeReservedComputeClaimRecoveryMutation() computeClaimRecoveryMutationLedger {
	return nodeReservedComputeClaimRecoveryMutation(computeClaimRecoveryMutationLedger{
		State: "observed", Reason: "none", Evidence: ComputeClaimEvidence{},
	})
}

func validNodeReservedComputeClaimRecoveryMutation(ledger computeClaimRecoveryMutationLedger) bool {
	return ledger.State == "node_reserved" && ledger.Reason == "provider_describe" && ledger.FailureStage == "node_patch_readback" &&
		ledger.ProviderErrorClass == "transport_error" && ledger.TencentMutationCount >= 0 && ledger.TencentMutationCount <= 5 &&
		ledger.KubernetesMutationCount == 1 && ledger.Evidence.CVM.Attempted == ledger.TencentMutationCount &&
		ledger.Evidence.CVM.Confirmed == ledger.TencentMutationCount && ledger.Evidence.CVM.Unknown == 0 && len(ledger.Evidence.CVM.Missing) == 0 &&
		reflect.DeepEqual(ledger.Evidence.Node, ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"node_ownership"}})
}

func successfulNodeClaimRecoveryMutation(ledger computeClaimRecoveryMutationLedger) bool {
	return ledger.State == "observed" && ledger.Reason == "none" && ledger.TencentMutationCount >= 0 && ledger.TencentMutationCount <= 5 &&
		ledger.KubernetesMutationCount == 1 && validComputeClaimMutationEvidence(ledger.Evidence.CVM, ledger.TencentMutationCount, 5, "cvm") &&
		reflect.DeepEqual(ledger.Evidence.Node, ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1})
}

func observedNodeClaimRecoveryMutation(reserved computeClaimRecoveryMutationLedger, result ComputeClaimRecoveryProof) computeClaimRecoveryMutationLedger {
	ledger := reserved
	ledger.State = "observed"
	ledger.Reason = safeComputeClaimRecoveryReason(result.Reason, "provider_describe")
	if result.Reason == "none" {
		ledger.Reason = "none"
	}
	ledger.FailureStage = result.FailureStage
	ledger.ProviderErrorClass = result.ProviderErrorClass
	if result.Evidence != nil && result.TencentMutationCount == 0 && reflect.DeepEqual(result.Evidence.CVM, ComputeClaimMutationEvidence{}) &&
		validComputeClaimMutationEvidenceShape(result.Evidence.Node, result.KubernetesMutationCount, 1, "node") {
		ledger.KubernetesMutationCount = result.KubernetesMutationCount
		ledger.Evidence.Node = cloneComputeClaimMutationEvidence(result.Evidence.Node)
	}
	if ledger.Reason == "none" {
		ledger.FailureStage, ledger.ProviderErrorClass = "", ""
	}
	return ledger
}

func observedNodeClaimReadbackMutation(reserved computeClaimRecoveryMutationLedger) computeClaimRecoveryMutationLedger {
	ledger := reserved
	ledger.State, ledger.Reason, ledger.FailureStage, ledger.ProviderErrorClass = "observed", "none", "", ""
	ledger.Evidence.Node.Confirmed = ledger.Evidence.Node.Attempted
	ledger.Evidence.Node.Unknown = 0
	ledger.Evidence.Node.Missing = nil
	return ledger
}

func observedComputeClaimRecoveryMutation(result ComputeClaimRecoveryProof) computeClaimRecoveryMutationLedger {
	ledger := reservedComputeClaimRecoveryMutation()
	ledger.State = "observed"
	ledger.Reason = safeComputeClaimRecoveryReason(result.Reason, "identity_mismatch")
	if result.Reason == "none" {
		ledger.Reason = "none"
	}
	if validComputeClaimFailureStage(result.FailureStage) {
		ledger.FailureStage = result.FailureStage
	}
	if validComputeClaimProviderErrorClass(result.ProviderErrorClass) {
		ledger.ProviderErrorClass = result.ProviderErrorClass
	}
	if result.Evidence != nil &&
		validComputeClaimMutationEvidenceShape(result.Evidence.CVM, result.TencentMutationCount, 5, "cvm") &&
		validComputeClaimMutationEvidenceShape(result.Evidence.Node, result.KubernetesMutationCount, 1, "node") {
		ledger.TencentMutationCount = result.TencentMutationCount
		ledger.KubernetesMutationCount = result.KubernetesMutationCount
		ledger.Evidence = ComputeClaimEvidence{
			CVM:  cloneComputeClaimMutationEvidence(result.Evidence.CVM),
			Node: cloneComputeClaimMutationEvidence(result.Evidence.Node),
		}
	}
	if ledger.Reason == "none" {
		ledger.FailureStage, ledger.ProviderErrorClass = "", ""
	}
	return ledger
}

func validComputeClaimRecoveryMutationLedger(ledger computeClaimRecoveryMutationLedger) bool {
	if ledger.State != "reserved" && ledger.State != "node_reserved" && ledger.State != "observed" {
		return false
	}
	if ledger.Reason != "none" && safeComputeClaimRecoveryReason(ledger.Reason, "") != ledger.Reason {
		return false
	}
	valid := validComputeClaimFailureStage(ledger.FailureStage) && validComputeClaimProviderErrorClass(ledger.ProviderErrorClass) &&
		validComputeClaimMutationEvidenceShape(ledger.Evidence.CVM, ledger.TencentMutationCount, 5, "cvm") &&
		validComputeClaimMutationEvidenceShape(ledger.Evidence.Node, ledger.KubernetesMutationCount, 1, "node")
	if !valid {
		return false
	}
	if ledger.State == "reserved" {
		return reflect.DeepEqual(ledger, reservedComputeClaimRecoveryMutation())
	}
	if ledger.State == "node_reserved" {
		return validNodeReservedComputeClaimRecoveryMutation(ledger)
	}
	if ledger.Reason == "none" {
		return ledger.FailureStage == "" && ledger.ProviderErrorClass == "" &&
			validComputeClaimMutationEvidence(ledger.Evidence.CVM, ledger.TencentMutationCount, 5, "cvm") &&
			validComputeClaimMutationEvidence(ledger.Evidence.Node, ledger.KubernetesMutationCount, 1, "node")
	}
	return true
}

func recoverableObservedComputeClaimRecoveryMutation(ledger computeClaimRecoveryMutationLedger) bool {
	if ledger.State != "observed" || ledger.Reason != "provider_describe" || ledger.FailureStage != "cvm_tag_readback" ||
		ledger.TencentMutationCount < 1 || ledger.TencentMutationCount > 5 || ledger.KubernetesMutationCount != 0 ||
		ledger.Evidence.CVM.Attempted != ledger.TencentMutationCount || ledger.Evidence.CVM.Confirmed != 0 || ledger.Evidence.CVM.Unknown != 0 ||
		!reflect.DeepEqual(ledger.Evidence.Node, ComputeClaimMutationEvidence{}) || len(ledger.Evidence.CVM.Missing) == 0 {
		return false
	}
	for _, field := range ledger.Evidence.CVM.Missing {
		switch field {
		case "opl_account_id", "opl_workspace_id", "opl_resource_id", "opl_operation_id":
		default:
			return false
		}
	}
	return true
}

func confirmedCVMOnlyObservedComputeClaimRecoveryMutation(ledger computeClaimRecoveryMutationLedger) bool {
	return ledger.State == "observed" && ledger.Reason == "none" && ledger.FailureStage == "" && ledger.ProviderErrorClass == "" &&
		ledger.TencentMutationCount >= 1 && ledger.TencentMutationCount <= 5 && ledger.KubernetesMutationCount == 0 &&
		validComputeClaimMutationEvidence(ledger.Evidence.CVM, ledger.TencentMutationCount, 5, "cvm") &&
		reflect.DeepEqual(ledger.Evidence.Node, ComputeClaimMutationEvidence{})
}

func decodeComputeClaimRecoveryMutation(operation FabricOperation) (computeClaimRecoveryMutationLedger, bool, bool) {
	value, ok := operation.RedactedProviderPayload[computeClaimRecoveryMutationPayloadKey]
	if !ok {
		return computeClaimRecoveryMutationLedger{}, false, false
	}
	body, err := json.Marshal(value)
	if err != nil {
		return computeClaimRecoveryMutationLedger{}, true, false
	}
	var ledger computeClaimRecoveryMutationLedger
	if json.Unmarshal(body, &ledger) != nil || !validComputeClaimRecoveryMutationLedger(ledger) {
		return computeClaimRecoveryMutationLedger{}, true, false
	}
	return ledger, true, true
}

func withComputeClaimRecoveryMutation(payload map[string]any, ledger computeClaimRecoveryMutationLedger) map[string]any {
	result := maps.Clone(payload)
	if result == nil {
		result = map[string]any{}
	}
	result[computeClaimRecoveryMutationPayloadKey] = map[string]any{
		"state": ledger.State, "reason": ledger.Reason,
		"tencentMutationCount": ledger.TencentMutationCount, "kubernetesMutationCount": ledger.KubernetesMutationCount,
		"failureStage": ledger.FailureStage, "providerErrorClass": ledger.ProviderErrorClass,
		"evidence": map[string]any{
			"cvm": map[string]any{
				"attempted": ledger.Evidence.CVM.Attempted, "confirmed": ledger.Evidence.CVM.Confirmed,
				"unknown": ledger.Evidence.CVM.Unknown, "missing": append([]string(nil), ledger.Evidence.CVM.Missing...),
			},
			"node": map[string]any{
				"attempted": ledger.Evidence.Node.Attempted, "confirmed": ledger.Evidence.Node.Confirmed,
				"unknown": ledger.Evidence.Node.Unknown, "missing": append([]string(nil), ledger.Evidence.Node.Missing...),
			},
		},
	}
	return result
}

func applyComputeClaimRecoveryMutation(result *ComputeClaimRecoveryProof, ledger computeClaimRecoveryMutationLedger) {
	result.Eligible = false
	result.Reason = ledger.Reason
	result.TencentMutationCount = ledger.TencentMutationCount
	result.KubernetesMutationCount = ledger.KubernetesMutationCount
	result.FailureStage = ledger.FailureStage
	result.ProviderErrorClass = ledger.ProviderErrorClass
	result.Evidence = &ComputeClaimEvidence{
		CVM:  cloneComputeClaimMutationEvidence(ledger.Evidence.CVM),
		Node: cloneComputeClaimMutationEvidence(ledger.Evidence.Node),
	}
}

func applyComputeClaimRecoveryReplayFailure(result *ComputeClaimRecoveryProof, ledger computeClaimRecoveryMutationLedger, readbackReason string) {
	applyComputeClaimRecoveryMutation(result, ledger)
	if ledger.State != "observed" || ledger.Reason != "none" {
		return
	}
	result.Reason = safeComputeClaimRecoveryReason(readbackReason, "identity_mismatch")
	if result.Reason == "none" {
		result.Reason = "identity_mismatch"
	}
	result.FailureStage = "claim_final_readback"
	result.ProviderErrorClass = "readback_mismatch"
}

func validComputeClaimRecoveryMutationTransition(current, next FabricOperation) bool {
	currentLedger, currentPresent, currentValid := decodeComputeClaimRecoveryMutation(current)
	nextLedger, nextPresent, nextValid := decodeComputeClaimRecoveryMutation(next)
	if currentPresent && !currentValid || nextPresent && !nextValid {
		return false
	}
	if !currentPresent {
		return !nextPresent || nextLedger.State == "reserved" || validLegacyNodeReservationTransition(current, next, nextLedger)
	}
	if !nextPresent {
		return false
	}
	if (nextLedger.State == "reserved" || nextLedger.State == "node_reserved") && next.Status == "succeeded" {
		return false
	}
	switch currentLedger.State {
	case "reserved":
		return nextLedger.State == "observed" || reflect.DeepEqual(currentLedger, nextLedger)
	case "observed":
		return reflect.DeepEqual(currentLedger, nextLedger) ||
			(recoverableObservedComputeClaimRecoveryMutation(currentLedger) || confirmedCVMOnlyObservedComputeClaimRecoveryMutation(currentLedger)) &&
				reflect.DeepEqual(nextLedger, nodeReservedComputeClaimRecoveryMutation(currentLedger))
	case "node_reserved":
		return nextLedger.State == "observed" || reflect.DeepEqual(currentLedger, nextLedger)
	default:
		return false
	}
}

func validLegacyNodeReservationTransition(current, next FabricOperation, nextLedger computeClaimRecoveryMutationLedger) bool {
	currentBinding, currentPresent, currentValid := decodeComputeClaimRecoveryBinding(current)
	nextBinding, nextPresent, nextValid := decodeComputeClaimRecoveryBinding(next)
	return currentPresent && currentValid && nextPresent && nextValid && currentBinding == nextBinding &&
		(currentBinding.IdempotencyKey == currentBinding.LaunchOperationID+":compute-claim" ||
			currentBinding.IdempotencyKey == currentBinding.LaunchOperationID+":compute") &&
		current.IdempotencyKey == currentBinding.LaunchOperationID+":compute" &&
		reflect.DeepEqual(nextLedger, legacyNodeReservedComputeClaimRecoveryMutation())
}

func newComputeClaimRecoveryBinding(input ComputeClaimRecoveryClaimInput) computeClaimRecoveryBinding {
	target := struct {
		MachineName   string `json:"machineName"`
		NodeName      string `json:"nodeName"`
		CVMInstanceID string `json:"cvmInstanceId"`
		PrivateIP     string `json:"privateIp"`
		InstanceType  string `json:"instanceType"`
		Zone          string `json:"zone"`
	}{input.MachineName, input.NodeName, input.CVMInstanceID, input.PrivateIP, input.InstanceType, input.Zone}
	bindingInput := input
	bindingInput.AllowExistingStorageOperation = false
	return computeClaimRecoveryBinding{
		LaunchOperationID: input.LaunchOperationID,
		IdempotencyKey:    input.IdempotencyKey,
		TargetHash:        hashInput(target),
		RequestHash:       hashInput(bindingInput),
	}
}

func historicalComputeClaimRecoveryBinding(input ComputeClaimRecoveryClaimInput) computeClaimRecoveryBinding {
	legacy := input
	legacy.IdempotencyKey = input.LaunchOperationID + ":compute-claim"
	return newComputeClaimRecoveryBinding(legacy)
}

func expectedComputeClaimRecoveryOperation(input ComputeClaimRecoveryInput) FabricOperation {
	expectedInput := ComputeAllocationInput{
		ID: input.ComputeAllocationID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID,
		PackageID: input.PackageID, NodePoolID: input.NodePoolID, IdempotencyKey: input.LaunchOperationID + ":compute",
	}
	expected := newOperation(
		"create_compute_allocation", "compute_allocation", expectedInput.ID, expectedInput.AccountID,
		expectedInput.WorkspaceID, expectedInput.IdempotencyKey, hashInput(expectedInput), time.Time{},
	)
	return expected
}

func canonicalComputeClaimRecoveryOperation(operation FabricOperation, input ComputeClaimRecoveryInput) bool {
	expected := expectedComputeClaimRecoveryOperation(input)
	return operation.OperationID == expected.OperationID && operation.CallerService == expected.CallerService &&
		operation.Action == expected.Action && operation.ResourceKind == expected.ResourceKind && operation.ResourceID == expected.ResourceID &&
		operation.AccountID == expected.AccountID && operation.WorkspaceID == expected.WorkspaceID &&
		operation.IdempotencyKey == expected.IdempotencyKey && operation.RequestHash == expected.RequestHash
}

func computeClaimRecoveryBindingDigest(binding computeClaimRecoveryBinding) string {
	body, err := json.Marshal(binding)
	if err != nil {
		return ""
	}
	return computeClaimIdentityDigest(string(body))
}

func computeClaimRecoveryMutationDigest(ledger computeClaimRecoveryMutationLedger) string {
	body, err := json.Marshal(ledger)
	if err != nil {
		return ""
	}
	return computeClaimIdentityDigest(string(body))
}

func validComputeClaimRecoveryDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func isolatedRequestHashReconciliationLedger(ledger computeClaimRecoveryMutationLedger) bool {
	if ledger.State != "observed" || ledger.Reason != "provider_describe" || ledger.FailureStage != "cvm_tag_readback" ||
		ledger.ProviderErrorClass != "provider_error" ||
		ledger.TencentMutationCount != 1 || ledger.KubernetesMutationCount != 0 ||
		!reflect.DeepEqual(ledger.Evidence.Node, ComputeClaimMutationEvidence{}) ||
		ledger.Evidence.CVM.Attempted != 1 || ledger.Evidence.CVM.Confirmed != 0 || ledger.Evidence.CVM.Unknown != 1 ||
		!reflect.DeepEqual(ledger.Evidence.CVM.Missing, []string{"opl_account_id"}) {
		return false
	}
	return true
}

func normalLaunchTerminalRequestHashReconciliationEvidence(
	operation FabricOperation,
	input ComputeClaimRecoveryClaimInput,
	binding computeClaimRecoveryBinding,
) (string, bool) {
	if _, present, _ := decodeComputeClaimRecoveryMutation(operation); present {
		return "", false
	}
	createBudget, createPresent, createValid := normalLaunchStageBudget(operation.RedactedProviderPayload, "compute_create")
	cvmBudget, cvmPresent, cvmValid := normalLaunchStageBudget(operation.RedactedProviderPayload, "compute_claim_cvm")
	_, nodePresent, nodeValid := normalLaunchStageBudget(operation.RedactedProviderPayload, "compute_claim_node")
	terminal, terminalPresent, terminalValid := decodeComputeClaimTerminalEvidence(operation)
	wantBindingDigest := computeClaimIdentityDigest(binding.LaunchOperationID + "|" + binding.IdempotencyKey + "|" + binding.TargetHash + "|" + binding.RequestHash)
	if !createPresent || !createValid || createBudget != confirmedNormalLaunchMutationBudget() ||
		!cvmPresent || !cvmValid || cvmBudget != reservedNormalLaunchMutationBudget() || nodePresent || !nodeValid ||
		!terminalPresent || !terminalValid || terminal.Stage != "compute_claim_cvm" || terminal.Status != "terminal_unprovable" ||
		terminal.AttemptCount != 1 || terminal.Attempted != 1 || terminal.Confirmed != 0 || terminal.Unknown != 1 || terminal.Max != 1 ||
		terminal.FabricRecordID != operation.ID || terminal.OperationID != operation.OperationID || terminal.IdempotencyKey != operation.IdempotencyKey ||
		terminal.RequestHash != operation.RequestHash || terminal.LaunchOperationID != input.LaunchOperationID || terminal.AccountID != input.AccountID ||
		terminal.WorkspaceID != input.WorkspaceID || terminal.ComputeAllocationID != input.ComputeAllocationID || terminal.StorageVolumeID != input.StorageVolumeID ||
		terminal.PackageID != input.PackageID || terminal.PoolID != input.PoolID || terminal.NodePoolID != input.NodePoolID ||
		terminal.MachineName != input.MachineName || terminal.NodeName != input.NodeName || terminal.CVMInstanceID != input.CVMInstanceID ||
		terminal.BindingDigest != wantBindingDigest || terminal.OperatorApprovalID != "" || terminal.OperatorApprovalDigest != "" ||
		terminal.OperatorIdempotencyKey != "" || terminal.ManualRecoveryLedgerDigest != "" || terminal.Evidence != nil || len(terminal.StageBudgets) != 1 ||
		terminal.StageBudgets["compute_claim_cvm"] != (ComputeClaimStageBudget{Attempted: 1, Confirmed: 0, Unknown: 1, Max: 1}) {
		return "", false
	}
	return hashInput(struct {
		ComputeCreate normalLaunchMutationBudget
		ComputeClaim  normalLaunchMutationBudget
		Terminal      ComputeClaimTerminalEvidence
	}{createBudget, cvmBudget, terminal}), true
}

func isolatedRequestHashReconciliationProvenance(
	operation FabricOperation,
	input ComputeClaimRecoveryClaimInput,
	persisted computeClaimRecoveryBinding,
	present, valid bool,
) (computeClaimRecoveryReconciliationProvenance, bool) {
	if !present || !valid || !canonicalComputeClaimRecoveryOperation(operation, input.ComputeClaimRecoveryInput) {
		return computeClaimRecoveryReconciliationProvenance{}, false
	}
	want := newComputeClaimRecoveryBinding(input)
	if persisted.LaunchOperationID != want.LaunchOperationID || persisted.IdempotencyKey != want.IdempotencyKey ||
		persisted.TargetHash != want.TargetHash || persisted.RequestHash == want.RequestHash {
		return computeClaimRecoveryReconciliationProvenance{}, false
	}
	ledger, ledgerPresent, ledgerValid := decodeComputeClaimRecoveryMutation(operation)
	if ledgerPresent {
		if !ledgerValid || !isolatedRequestHashReconciliationLedger(ledger) {
			return computeClaimRecoveryReconciliationProvenance{}, false
		}
		return computeClaimRecoveryReconciliationProvenance{
			SchemaVersion: 1, Generation: "isolated_request_hash_v1", Source: "manual_recovery_ledger", Digest: computeClaimRecoveryMutationDigest(ledger),
		}, true
	}
	digest, ok := normalLaunchTerminalRequestHashReconciliationEvidence(operation, input, persisted)
	if !ok {
		return computeClaimRecoveryReconciliationProvenance{}, false
	}
	return computeClaimRecoveryReconciliationProvenance{
		SchemaVersion: 2, Generation: "normal_launch_terminal_evidence_v1", Source: "normal_launch_terminal_evidence", Digest: digest,
	}, true
}

func isolatedRequestHashReconciliationBinding(operation FabricOperation, input ComputeClaimRecoveryClaimInput, persisted computeClaimRecoveryBinding, present, valid bool) bool {
	_, ok := isolatedRequestHashReconciliationProvenance(operation, input, persisted, present, valid)
	return ok
}

func computeClaimRecoveryAllocationIdentityDigest(allocation ComputeAllocation) string {
	identity := struct {
		ID, AccountID, WorkspaceID, PackageID, Provider, ProviderResourceID, ProviderRequestID string
		PoolID, NodePoolID, MachineName, InstanceID, CVMInstanceID, NodeName, PrivateIP        string
		InstanceType, Zone, ChargeType, RenewFlag, Deadline                                    string
	}{
		allocation.ID, allocation.AccountID, allocation.WorkspaceID, allocation.PackageID, allocation.Provider,
		allocation.ProviderResourceID, allocation.ProviderRequestID, allocation.PoolID, allocation.NodePoolID,
		allocation.MachineName, allocation.InstanceID, allocation.CVMInstanceID, allocation.NodeName, allocation.PrivateIP,
		allocation.InstanceType, allocation.Zone, allocation.ChargeType, allocation.RenewFlag, allocation.Deadline,
	}
	return hashInput(identity)
}

func computeClaimRecoveryOwnershipIdentityDigest(ownership MachineOwnership) string {
	identity := struct {
		ID, ResourceID, AccountID, WorkspaceID, PackageID, NodePoolID string
		MachineID, InstanceID, NodeName, ProviderRequestID            string
		ReleasedAt                                                    *time.Time
	}{
		ownership.ID, ownership.ResourceID, ownership.AccountID, ownership.WorkspaceID, ownership.PackageID, ownership.NodePoolID,
		ownership.MachineID, ownership.InstanceID, ownership.NodeName, ownership.ProviderRequestID, ownership.ReleasedAt,
	}
	return hashInput(identity)
}

func newComputeClaimRecoveryReconciliation(
	operation FabricOperation,
	allocation ComputeAllocation,
	plan ComputeAllocationPreparation,
	ownership MachineOwnership,
	input ComputeClaimRecoveryClaimInput,
	proof ComputeClaimRecoveryProof,
	binding computeClaimRecoveryBinding,
	ledger computeClaimRecoveryMutationLedger,
) computeClaimRecoveryReconciliation {
	want := newComputeClaimRecoveryBinding(input)
	provenance, provenanceOK := isolatedRequestHashReconciliationProvenance(operation, input, binding, true, true)
	if provenanceOK && provenance.SchemaVersion == 2 {
		authority := struct {
			FabricRecordID              string                         `json:"fabricRecordId"`
			OperationID                 string                         `json:"operationId"`
			OperationHash               string                         `json:"operationHash"`
			AllocationID                string                         `json:"allocationId"`
			AllocationProviderRequestID string                         `json:"allocationProviderRequestId"`
			AllocationIdentityDigest    string                         `json:"allocationIdentityDigest"`
			AdmittedAllocationStatus    string                         `json:"admittedAllocationStatus"`
			Plan                        ComputeAllocationPreparation   `json:"plan"`
			OwnershipID                 string                         `json:"ownershipId"`
			OwnershipProviderRequestID  string                         `json:"ownershipProviderRequestId"`
			OwnershipIdentityDigest     string                         `json:"ownershipIdentityDigest"`
			AdmittedOwnershipStatus     string                         `json:"admittedOwnershipStatus"`
			Input                       ComputeClaimRecoveryClaimInput `json:"input"`
			ChargeType                  string                         `json:"chargeType"`
			PeriodMonths                int                            `json:"periodMonths"`
			RenewFlag                   string                         `json:"renewFlag"`
			Deadline                    string                         `json:"deadline"`
			StorageState                string                         `json:"storageState"`
			Binding                     computeClaimRecoveryBinding    `json:"binding"`
			ProvenanceSource            string                         `json:"provenanceSource"`
			ProvenanceDigest            string                         `json:"provenanceDigest"`
		}{
			FabricRecordID: operation.ID, OperationID: operation.OperationID, OperationHash: operation.RequestHash,
			AllocationID: allocation.ID, AllocationProviderRequestID: allocation.ProviderRequestID,
			AllocationIdentityDigest: computeClaimRecoveryAllocationIdentityDigest(allocation), AdmittedAllocationStatus: "quarantined", Plan: plan,
			OwnershipID: ownership.ID, OwnershipProviderRequestID: ownership.ProviderRequestID,
			OwnershipIdentityDigest: computeClaimRecoveryOwnershipIdentityDigest(ownership), AdmittedOwnershipStatus: "quarantined", Input: input,
			ChargeType: proof.ChargeType, PeriodMonths: proof.PeriodMonths, RenewFlag: proof.RenewFlag, Deadline: proof.Deadline,
			StorageState: proof.StorageState, Binding: binding, ProvenanceSource: provenance.Source, ProvenanceDigest: provenance.Digest,
		}
		return computeClaimRecoveryReconciliation{
			SchemaVersion: 2, Consumer: "claim_compute_recovery", Generation: provenance.Generation,
			ProvenanceSource: provenance.Source, ProvenanceDigest: provenance.Digest, State: "verified",
			BindingDigest: computeClaimRecoveryBindingDigest(binding), ExpectedRequestHashDigest: computeClaimIdentityDigest(want.RequestHash),
			PersistedRequestHashDigest: computeClaimIdentityDigest(binding.RequestHash), AuthorityDigest: hashInput(authority),
		}
	}
	authority := struct {
		FabricRecordID              string                             `json:"fabricRecordId"`
		OperationID                 string                             `json:"operationId"`
		OperationHash               string                             `json:"operationHash"`
		AllocationID                string                             `json:"allocationId"`
		AllocationProviderRequestID string                             `json:"allocationProviderRequestId"`
		AllocationIdentityDigest    string                             `json:"allocationIdentityDigest"`
		AdmittedAllocationStatus    string                             `json:"admittedAllocationStatus"`
		Plan                        ComputeAllocationPreparation       `json:"plan"`
		OwnershipID                 string                             `json:"ownershipId"`
		OwnershipProviderRequestID  string                             `json:"ownershipProviderRequestId"`
		OwnershipIdentityDigest     string                             `json:"ownershipIdentityDigest"`
		AdmittedOwnershipStatus     string                             `json:"admittedOwnershipStatus"`
		Input                       ComputeClaimRecoveryClaimInput     `json:"input"`
		ChargeType                  string                             `json:"chargeType"`
		PeriodMonths                int                                `json:"periodMonths"`
		RenewFlag                   string                             `json:"renewFlag"`
		Deadline                    string                             `json:"deadline"`
		StorageState                string                             `json:"storageState"`
		Binding                     computeClaimRecoveryBinding        `json:"binding"`
		Ledger                      computeClaimRecoveryMutationLedger `json:"ledger"`
	}{
		FabricRecordID: operation.ID, OperationID: operation.OperationID, OperationHash: operation.RequestHash,
		AllocationID: allocation.ID, AllocationProviderRequestID: allocation.ProviderRequestID,
		AllocationIdentityDigest: computeClaimRecoveryAllocationIdentityDigest(allocation), AdmittedAllocationStatus: "quarantined", Plan: plan,
		OwnershipID: ownership.ID, OwnershipProviderRequestID: ownership.ProviderRequestID,
		OwnershipIdentityDigest: computeClaimRecoveryOwnershipIdentityDigest(ownership), AdmittedOwnershipStatus: "quarantined", Input: input,
		ChargeType: proof.ChargeType, PeriodMonths: proof.PeriodMonths, RenewFlag: proof.RenewFlag, Deadline: proof.Deadline,
		StorageState: proof.StorageState, Binding: binding, Ledger: ledger,
	}
	return computeClaimRecoveryReconciliation{
		SchemaVersion: 1, Consumer: "claim_compute_recovery", Generation: "isolated_request_hash_v1", State: "verified",
		BindingDigest:              computeClaimRecoveryBindingDigest(binding),
		ExpectedRequestHashDigest:  computeClaimIdentityDigest(want.RequestHash),
		PersistedRequestHashDigest: computeClaimIdentityDigest(binding.RequestHash),
		MutationLedgerDigest:       computeClaimRecoveryMutationDigest(ledger), AuthorityDigest: hashInput(authority),
	}
}

func validComputeClaimRecoveryReconciliation(value computeClaimRecoveryReconciliation) bool {
	if value.Consumer != "claim_compute_recovery" || !validComputeClaimRecoveryDigest(value.BindingDigest) || !validComputeClaimRecoveryDigest(value.ExpectedRequestHashDigest) ||
		!validComputeClaimRecoveryDigest(value.PersistedRequestHashDigest) || value.ExpectedRequestHashDigest == value.PersistedRequestHashDigest ||
		!validComputeClaimRecoveryDigest(value.AuthorityDigest) {
		return false
	}
	switch value.SchemaVersion {
	case 1:
		if value.Generation != "isolated_request_hash_v1" || value.ProvenanceSource != "" || value.ProvenanceDigest != "" ||
			!validComputeClaimRecoveryDigest(value.MutationLedgerDigest) {
			return false
		}
	case 2:
		if value.Generation != "normal_launch_terminal_evidence_v1" || value.ProvenanceSource != "normal_launch_terminal_evidence" ||
			!validComputeClaimRecoveryDigest(value.ProvenanceDigest) || value.MutationLedgerDigest != "" {
			return false
		}
	default:
		return false
	}
	switch value.State {
	case "verified":
		return value.FailureStage == "" && value.ProviderErrorClass == "" && reflect.DeepEqual(value.Node, ComputeClaimMutationEvidence{})
	case "node_reserved":
		return value.FailureStage == "node_patch_readback" && value.ProviderErrorClass == "transport_error" &&
			reflect.DeepEqual(value.Node, ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"node_ownership"}})
	case "observed":
		return validComputeClaimFailureStage(value.FailureStage) && validComputeClaimProviderErrorClass(value.ProviderErrorClass) &&
			validComputeClaimMutationEvidenceShape(value.Node, value.Node.Attempted, 1, "node")
	case "succeeded":
		return value.FailureStage == "" && value.ProviderErrorClass == "" &&
			(reflect.DeepEqual(value.Node, ComputeClaimMutationEvidence{}) || reflect.DeepEqual(value.Node, ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1}))
	default:
		return false
	}
}

func exactLegacyKubectlClientRejectedReconciliation(value computeClaimRecoveryReconciliation) bool {
	return value.SchemaVersion == 2 && value.Generation == "normal_launch_terminal_evidence_v1" &&
		value.ProvenanceSource == "normal_launch_terminal_evidence" && value.State == "observed" &&
		value.FailureStage == "node_patch_readback" && value.ProviderErrorClass == "provider_error" &&
		reflect.DeepEqual(value.Node, ComputeClaimMutationEvidence{Attempted: 1, Missing: []string{"node_ownership"}})
}

func newComputeClaimNodeClientRejectionRecovery(source computeClaimRecoveryReconciliation) computeClaimNodeClientRejectionRecovery {
	return computeClaimNodeClientRejectionRecovery{
		SchemaVersion: 1, Classification: "kubectl_client_validation_rejected", Invocation: "patch_json_filename_stdin_v1",
		RecordedCalls: 1, APIAcceptedMutations: 0, SourceReconciliationDigest: hashInput(source),
	}
}

func validComputeClaimNodeClientRejectionRecovery(value computeClaimNodeClientRejectionRecovery) bool {
	return value.SchemaVersion == 1 && value.Classification == "kubectl_client_validation_rejected" &&
		value.Invocation == "patch_json_filename_stdin_v1" && value.RecordedCalls == 1 && value.APIAcceptedMutations == 0 &&
		validComputeClaimRecoveryDigest(value.SourceReconciliationDigest)
}

func decodeComputeClaimNodeClientRejectionRecovery(operation FabricOperation) (computeClaimNodeClientRejectionRecovery, bool, bool) {
	value, present := operation.RedactedProviderPayload[computeClaimNodeClientRejectionPayloadKey]
	if !present {
		return computeClaimNodeClientRejectionRecovery{}, false, false
	}
	body, err := json.Marshal(value)
	if err != nil {
		return computeClaimNodeClientRejectionRecovery{}, true, false
	}
	var recovery computeClaimNodeClientRejectionRecovery
	if json.Unmarshal(body, &recovery) != nil || !validComputeClaimNodeClientRejectionRecovery(recovery) {
		return computeClaimNodeClientRejectionRecovery{}, true, false
	}
	return recovery, true, true
}

func withComputeClaimNodeClientRejectionRecovery(payload map[string]any, value computeClaimNodeClientRejectionRecovery) map[string]any {
	result := maps.Clone(payload)
	if result == nil {
		result = map[string]any{}
	}
	result[computeClaimNodeClientRejectionPayloadKey] = map[string]any{
		"schemaVersion": value.SchemaVersion, "classification": value.Classification, "invocation": value.Invocation,
		"recordedCalls": value.RecordedCalls, "apiAcceptedMutations": value.APIAcceptedMutations,
		"sourceReconciliationDigest": value.SourceReconciliationDigest,
	}
	return result
}

func computeClaimRecoveryReconciliationMatches(
	value computeClaimRecoveryReconciliation,
	operation FabricOperation,
	input ComputeClaimRecoveryClaimInput,
	binding computeClaimRecoveryBinding,
	ledger computeClaimRecoveryMutationLedger,
) bool {
	want := newComputeClaimRecoveryBinding(input)
	if value.BindingDigest != computeClaimRecoveryBindingDigest(binding) {
		return false
	}
	if value.ExpectedRequestHashDigest != computeClaimIdentityDigest(want.RequestHash) ||
		value.PersistedRequestHashDigest != computeClaimIdentityDigest(binding.RequestHash) ||
		!canonicalComputeClaimRecoveryOperation(operation, input.ComputeClaimRecoveryInput) {
		return false
	}
	if value.SchemaVersion == 1 {
		return value.MutationLedgerDigest == computeClaimRecoveryMutationDigest(ledger)
	}
	provenance, ok := isolatedRequestHashReconciliationProvenance(operation, input, binding, true, true)
	return ok && provenance.SchemaVersion == 2 && value.ProvenanceSource == provenance.Source && value.ProvenanceDigest == provenance.Digest
}

func decodeComputeClaimRecoveryReconciliation(operation FabricOperation) (computeClaimRecoveryReconciliation, bool, bool) {
	value, present := operation.RedactedProviderPayload[computeClaimRecoveryReconciliationPayloadKey]
	if !present {
		return computeClaimRecoveryReconciliation{}, false, false
	}
	body, err := json.Marshal(value)
	if err != nil {
		return computeClaimRecoveryReconciliation{}, true, false
	}
	var reconciliation computeClaimRecoveryReconciliation
	if json.Unmarshal(body, &reconciliation) != nil || !validComputeClaimRecoveryReconciliation(reconciliation) {
		return computeClaimRecoveryReconciliation{}, true, false
	}
	return reconciliation, true, true
}

func withComputeClaimRecoveryReconciliation(payload map[string]any, value computeClaimRecoveryReconciliation) map[string]any {
	result := maps.Clone(payload)
	if result == nil {
		result = map[string]any{}
	}
	result[computeClaimRecoveryReconciliationPayloadKey] = map[string]any{
		"schemaVersion": value.SchemaVersion, "consumer": value.Consumer, "generation": value.Generation, "state": value.State,
		"provenanceSource": value.ProvenanceSource, "provenanceDigest": value.ProvenanceDigest,
		"bindingDigest": value.BindingDigest, "expectedRequestHashDigest": value.ExpectedRequestHashDigest,
		"persistedRequestHashDigest": value.PersistedRequestHashDigest, "mutationLedgerDigest": value.MutationLedgerDigest,
		"authorityDigest": value.AuthorityDigest, "failureStage": value.FailureStage, "providerErrorClass": value.ProviderErrorClass,
		"node": map[string]any{"attempted": value.Node.Attempted, "confirmed": value.Node.Confirmed, "unknown": value.Node.Unknown, "missing": append([]string(nil), value.Node.Missing...)},
	}
	return result
}

func validComputeClaimRecoveryReconciliationTransition(current, next FabricOperation) bool {
	currentValue, currentPresent, currentValid := decodeComputeClaimRecoveryReconciliation(current)
	nextValue, nextPresent, nextValid := decodeComputeClaimRecoveryReconciliation(next)
	currentRejection, currentRejectionPresent, currentRejectionValid := decodeComputeClaimNodeClientRejectionRecovery(current)
	nextRejection, nextRejectionPresent, nextRejectionValid := decodeComputeClaimNodeClientRejectionRecovery(next)
	if currentPresent && !currentValid || nextPresent && !nextValid {
		return false
	}
	if currentRejectionPresent && !currentRejectionValid || nextRejectionPresent && !nextRejectionValid {
		return false
	}
	if !currentPresent {
		return !currentRejectionPresent && !nextRejectionPresent && (!nextPresent || nextValue.State == "verified")
	}
	legacyClientRejectionReservation := !currentRejectionPresent && nextRejectionPresent &&
		exactLegacyKubectlClientRejectedReconciliation(currentValue) && nextValue.State == "node_reserved" &&
		nextRejection == newComputeClaimNodeClientRejectionRecovery(currentValue)
	if currentRejectionPresent && (!nextRejectionPresent || currentRejection != nextRejection) ||
		!currentRejectionPresent && nextRejectionPresent && !legacyClientRejectionReservation {
		return false
	}
	if !nextPresent || currentValue.SchemaVersion != nextValue.SchemaVersion || currentValue.Consumer != nextValue.Consumer ||
		currentValue.Generation != nextValue.Generation || currentValue.ProvenanceSource != nextValue.ProvenanceSource || currentValue.ProvenanceDigest != nextValue.ProvenanceDigest ||
		currentValue.BindingDigest != nextValue.BindingDigest ||
		currentValue.ExpectedRequestHashDigest != nextValue.ExpectedRequestHashDigest ||
		currentValue.PersistedRequestHashDigest != nextValue.PersistedRequestHashDigest ||
		currentValue.MutationLedgerDigest != nextValue.MutationLedgerDigest || currentValue.AuthorityDigest != nextValue.AuthorityDigest {
		return false
	}
	switch currentValue.State {
	case "verified":
		return nextValue.State == "verified" || nextValue.State == "node_reserved" || nextValue.State == "succeeded"
	case "node_reserved":
		return nextValue.State == "node_reserved" || nextValue.State == "observed" || nextValue.State == "succeeded"
	case "observed":
		return nextValue.State == "observed" || nextValue.State == "succeeded" || legacyClientRejectionReservation
	case "succeeded":
		return nextValue.State == "succeeded" && reflect.DeepEqual(currentValue, nextValue)
	default:
		return false
	}
}

func computeClaimIdentityDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func computeClaimIdentityEvidence(operation FabricOperation, input ComputeClaimRecoveryClaimInput) *ComputeClaimIdentityEvidence {
	want := newComputeClaimRecoveryBinding(input)
	historical := historicalComputeClaimRecoveryBinding(input)
	expectedOperation := expectedComputeClaimRecoveryOperation(input.ComputeClaimRecoveryInput)
	got, present, valid := decodeComputeClaimRecoveryBinding(operation)
	bindingClassification, bindingDigest := classifyComputeClaimRecoveryBinding(operation, input, got, present, valid)
	ledgerState, ledgerOutcome, ledgerDigest := computeClaimMutationLedgerEvidence(operation)
	checks := []ComputeClaimIdentityCheck{
		{Field: "fabric.operationId", Matches: operation.OperationID == expectedOperation.OperationID, Expected: expectedOperation.OperationID, Actual: operation.OperationID},
		{Field: "fabric.operationIdempotencyKey", Matches: operation.IdempotencyKey == input.LaunchOperationID+":compute", Expected: input.LaunchOperationID + ":compute", Actual: operation.IdempotencyKey},
		{Field: "fabric.operationRequestHash", Matches: operation.RequestHash == expectedOperation.RequestHash, ExpectedDigest: computeClaimIdentityDigest(expectedOperation.RequestHash), ActualDigest: computeClaimIdentityDigest(operation.RequestHash)},
		{Field: "binding.present", Matches: present, Expected: "present", Actual: map[bool]string{true: "present", false: "absent"}[present]},
		{Field: "binding.valid", Matches: valid, Expected: "valid", Actual: map[bool]string{true: "valid", false: "invalid"}[valid]},
	}
	if present && valid {
		bindingKind := bindingClassification
		expected := want
		switch bindingClassification {
		case "current":
		case "compute-claim":
			expected = historical
		}
		compatible := bindingClassification == "current" || bindingClassification == "compute-claim" || bindingClassification == "request-hash-reconciliation"
		checks = append(checks,
			ComputeClaimIdentityCheck{Field: "binding.compatibility", Matches: compatible, Expected: "current_compute_claim_or_request_hash_reconciliation", Actual: bindingKind},
			ComputeClaimIdentityCheck{Field: "binding.launchOperationId", Matches: got.LaunchOperationID == expected.LaunchOperationID, Expected: expected.LaunchOperationID, Actual: got.LaunchOperationID},
			ComputeClaimIdentityCheck{Field: "binding.idempotencyKey", Matches: got.IdempotencyKey == expected.IdempotencyKey, Expected: expected.IdempotencyKey, Actual: got.IdempotencyKey},
			ComputeClaimIdentityCheck{Field: "binding.targetHash", Matches: got.TargetHash == expected.TargetHash, ExpectedDigest: computeClaimIdentityDigest(expected.TargetHash), ActualDigest: computeClaimIdentityDigest(got.TargetHash)},
			ComputeClaimIdentityCheck{Field: "binding.requestHash", Matches: got.RequestHash == expected.RequestHash, ExpectedDigest: computeClaimIdentityDigest(expected.RequestHash), ActualDigest: computeClaimIdentityDigest(got.RequestHash)},
		)
	}
	result := &ComputeClaimIdentityEvidence{
		Checks: checks, BindingClassification: bindingClassification, BindingDigest: bindingDigest,
		MutationLedger: ledgerState, MutationLedgerOutcome: ledgerOutcome, MutationLedgerDigest: ledgerDigest,
	}
	if ledger, ledgerPresent, ledgerValid := decodeComputeClaimRecoveryMutation(operation); ledgerPresent && ledgerValid {
		result.MutationEvidence = &ComputeClaimEvidence{
			CVM:  cloneComputeClaimMutationEvidence(ledger.Evidence.CVM),
			Node: cloneComputeClaimMutationEvidence(ledger.Evidence.Node),
		}
		result.FailureStage = ledger.FailureStage
		result.ProviderErrorClass = ledger.ProviderErrorClass
	}
	if reconciliation, present, valid := decodeComputeClaimRecoveryReconciliation(operation); present && valid {
		result.Reconciliation = &ComputeClaimReconciliationEvidence{
			SchemaVersion: reconciliation.SchemaVersion, Consumer: reconciliation.Consumer, Generation: reconciliation.Generation,
			ProvenanceSource: reconciliation.ProvenanceSource, ProvenanceDigest: reconciliation.ProvenanceDigest, State: reconciliation.State,
			ExpectedRequestHashDigest:  reconciliation.ExpectedRequestHashDigest,
			PersistedRequestHashDigest: reconciliation.PersistedRequestHashDigest,
			FailureStage:               reconciliation.FailureStage, ProviderErrorClass: reconciliation.ProviderErrorClass,
			Node: cloneComputeClaimMutationEvidence(reconciliation.Node),
		}
	}
	return result
}

func classifyComputeClaimRecoveryBinding(operation FabricOperation, input ComputeClaimRecoveryClaimInput, got computeClaimRecoveryBinding, present, valid bool) (string, string) {
	value, rawPresent := operation.RedactedProviderPayload["computeClaimRecovery"]
	if !rawPresent {
		return "other", computeClaimIdentityDigest("absent")
	}
	body, err := json.Marshal(value)
	if err != nil {
		return "other", computeClaimIdentityDigest("invalid")
	}
	digest := computeClaimIdentityDigest(string(body))
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || len(fields) != 4 {
		return "other", digest
	}
	for _, field := range []string{"launchOperationId", "idempotencyKey", "targetHash", "requestHash"} {
		if _, ok := fields[field]; !ok {
			return "other", digest
		}
	}
	if !present || !valid {
		return "other", digest
	}
	if got == newComputeClaimRecoveryBinding(input) {
		return "current", digest
	}
	if got == historicalComputeClaimRecoveryBinding(input) {
		return "compute-claim", digest
	}
	if isolatedRequestHashReconciliationBinding(operation, input, got, present, valid) {
		return "request-hash-reconciliation", digest
	}
	if knownLegacyComputeClaimRecoveryIdempotencyKey(got.IdempotencyKey) {
		legacy := input
		legacy.IdempotencyKey = got.IdempotencyKey
		if got == newComputeClaimRecoveryBinding(legacy) {
			return "known-legacy", digest
		}
	}
	return "other", digest
}

func knownLegacyComputeClaimRecoveryIdempotencyKey(value string) bool {
	const prefix = "recovery-exec-"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+20 {
		return false
	}
	for _, character := range value[len(prefix):] {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func computeClaimMutationLedgerEvidence(operation FabricOperation) (string, string, string) {
	value, present := operation.RedactedProviderPayload[computeClaimRecoveryMutationPayloadKey]
	if !present {
		return "absent", "confirmed_zero", computeClaimIdentityDigest("absent")
	}
	body, err := json.Marshal(value)
	if err != nil {
		return "invalid", "unknown", computeClaimIdentityDigest("invalid")
	}
	digest := computeClaimIdentityDigest(string(body))
	ledger, _, valid := decodeComputeClaimRecoveryMutation(operation)
	if !valid {
		return "invalid", "unknown", digest
	}
	if ledger.State != "observed" {
		return ledger.State, "unknown", digest
	}
	complete := validComputeClaimMutationEvidence(ledger.Evidence.CVM, ledger.TencentMutationCount, 5, "cvm") &&
		validComputeClaimMutationEvidence(ledger.Evidence.Node, ledger.KubernetesMutationCount, 1, "node")
	if !complete {
		return ledger.State, "unknown", digest
	}
	if ledger.TencentMutationCount == 0 && ledger.KubernetesMutationCount == 0 {
		return ledger.State, "confirmed_zero", digest
	}
	return ledger.State, "nonzero", digest
}

func decodeComputeClaimRecoveryBinding(operation FabricOperation) (computeClaimRecoveryBinding, bool, bool) {
	value, ok := operation.RedactedProviderPayload["computeClaimRecovery"]
	if !ok {
		return computeClaimRecoveryBinding{}, false, false
	}
	body, err := json.Marshal(value)
	if err != nil {
		return computeClaimRecoveryBinding{}, true, false
	}
	var binding computeClaimRecoveryBinding
	if json.Unmarshal(body, &binding) != nil || binding.LaunchOperationID == "" || binding.IdempotencyKey == "" || binding.TargetHash == "" || binding.RequestHash == "" {
		return computeClaimRecoveryBinding{}, true, false
	}
	return binding, true, true
}

func withComputeClaimRecoveryBinding(payload map[string]any, binding computeClaimRecoveryBinding) map[string]any {
	result := maps.Clone(payload)
	if result == nil {
		result = map[string]any{}
	}
	result["computeClaimRecovery"] = map[string]any{
		"launchOperationId": binding.LaunchOperationID,
		"idempotencyKey":    binding.IdempotencyKey,
		"targetHash":        binding.TargetHash,
		"requestHash":       binding.RequestHash,
	}
	return result
}

func decodeComputeClaimTerminalEvidence(operation FabricOperation) (ComputeClaimTerminalEvidence, bool, bool) {
	value, ok := operation.RedactedProviderPayload[computeClaimTerminalEvidencePayloadKey]
	if !ok {
		return ComputeClaimTerminalEvidence{}, false, false
	}
	body, err := json.Marshal(value)
	if err != nil {
		return ComputeClaimTerminalEvidence{}, true, false
	}
	var evidence ComputeClaimTerminalEvidence
	if json.Unmarshal(body, &evidence) != nil || !validComputeClaimTerminalEvidence(evidence) {
		return ComputeClaimTerminalEvidence{}, true, false
	}
	return evidence, true, true
}

func withComputeClaimTerminalEvidence(payload map[string]any, evidence ComputeClaimTerminalEvidence) map[string]any {
	result := maps.Clone(payload)
	if result == nil {
		result = map[string]any{}
	}
	result[computeClaimTerminalEvidencePayloadKey] = map[string]any{
		"schemaVersion": evidence.SchemaVersion, "stage": evidence.Stage, "status": evidence.Status,
		"errorCode": evidence.ErrorCode, "reason": evidence.Reason, "readbackStatus": evidence.ReadbackStatus,
		"attemptCount": evidence.AttemptCount, "attempted": evidence.Attempted, "confirmed": evidence.Confirmed,
		"unknown": evidence.Unknown, "max": evidence.Max, "startedAt": evidence.StartedAt, "finishedAt": evidence.FinishedAt,
		"fabricRecordId": evidence.FabricRecordID, "operationId": evidence.OperationID, "idempotencyKey": evidence.IdempotencyKey, "requestHash": evidence.RequestHash,
		"launchOperationId": evidence.LaunchOperationID, "accountId": evidence.AccountID, "workspaceId": evidence.WorkspaceID,
		"computeAllocationId": evidence.ComputeAllocationID, "storageVolumeId": evidence.StorageVolumeID, "packageId": evidence.PackageID,
		"poolId": evidence.PoolID, "nodePoolId": evidence.NodePoolID, "machineName": evidence.MachineName,
		"nodeName": evidence.NodeName, "cvmInstanceId": evidence.CVMInstanceID, "cvmOwnershipState": evidence.CVMOwnershipState,
		"nodeOwnershipState": evidence.NodeOwnershipState, "bindingDigest": evidence.BindingDigest,
		"operatorApprovalId": evidence.OperatorApprovalID, "operatorApprovalDigest": evidence.OperatorApprovalDigest,
		"operatorIdempotencyKey": evidence.OperatorIdempotencyKey, "manualRecoveryLedgerDigest": evidence.ManualRecoveryLedgerDigest,
		"evidence": evidence.Evidence, "stageBudgets": evidence.StageBudgets,
	}
	return result
}

func validComputeClaimTerminalEvidence(evidence ComputeClaimTerminalEvidence) bool {
	if evidence.SchemaVersion != 1 || evidence.Status != "terminal_unprovable" || evidence.Stage == "" || evidence.ErrorCode == "" ||
		evidence.ReadbackStatus == "" || evidence.FabricRecordID == "" || evidence.OperationID == "" || evidence.IdempotencyKey == "" || evidence.RequestHash == "" ||
		evidence.AccountID == "" || evidence.WorkspaceID == "" || evidence.ComputeAllocationID == "" || evidence.PackageID == "" || evidence.NodePoolID == "" ||
		evidence.StartedAt == "" || evidence.FinishedAt == "" || evidence.AttemptCount < 0 || evidence.Attempted < 0 || evidence.Confirmed < 0 || evidence.Unknown < 0 || evidence.Max < 0 ||
		!validComputeClaimTerminalStage(evidence.Stage) || !validComputeClaimTerminalReadback(evidence.ReadbackStatus) {
		return false
	}
	if safeComputeClaimTerminalToken(evidence.ErrorCode) != evidence.ErrorCode || evidence.Reason != "" && safeComputeClaimTerminalToken(evidence.Reason) != evidence.Reason {
		return false
	}
	operatorFields := []string{evidence.OperatorApprovalID, evidence.OperatorApprovalDigest, evidence.OperatorIdempotencyKey, evidence.ManualRecoveryLedgerDigest}
	operatorFieldCount := 0
	for _, field := range operatorFields {
		if field != "" {
			operatorFieldCount++
		}
	}
	if operatorFieldCount != 0 && (operatorFieldCount != len(operatorFields) || !validComputePoolTerminalizationToken(evidence.OperatorApprovalID) ||
		evidence.OperatorApprovalID != evidence.OperatorIdempotencyKey || !validSHA256Hex(evidence.OperatorApprovalDigest) || !validSHA256Hex(evidence.ManualRecoveryLedgerDigest)) {
		return false
	}
	started, startedErr := time.Parse(time.RFC3339Nano, evidence.StartedAt)
	finished, finishedErr := time.Parse(time.RFC3339Nano, evidence.FinishedAt)
	if startedErr != nil || finishedErr != nil || finished.Before(started) || evidence.Attempted > evidence.Max || evidence.Confirmed > evidence.Attempted || evidence.Unknown > evidence.Attempted || evidence.Confirmed+evidence.Unknown > evidence.Attempted || evidence.AttemptCount != evidence.Attempted {
		return false
	}
	if evidence.StageBudgets != nil {
		for stage, budget := range evidence.StageBudgets {
			if stage != "compute_claim_cvm" && stage != "compute_claim_node" || budget.Max != 1 || budget.Attempted != 1 || budget.Confirmed < 0 || budget.Confirmed > budget.Attempted || budget.Unknown < 0 || budget.Unknown > budget.Attempted || budget.Confirmed+budget.Unknown != budget.Attempted {
				return false
			}
		}
	}
	return true
}

func validComputeClaimTerminalStage(stage string) bool {
	switch stage {
	case "compute_claim_cvm", "compute_claim_node", "compute_claim_finalization":
		return true
	default:
		return false
	}
}

func validComputeClaimTerminalReadback(status string) bool {
	switch status {
	case "not_attempted", "unavailable", "mismatch", "unallocated", "target_owned", "ownership_unavailable", "operator_terminalized":
		return true
	default:
		return false
	}
}

func terminalComputeClaimBinding(operation FabricOperation, allocation ComputeAllocation, plan ComputeAllocationPreparation) computeClaimRecoveryBinding {
	launchOperationID, ok := strings.CutSuffix(strings.TrimSpace(operation.IdempotencyKey), ":compute")
	if !ok || launchOperationID == "" {
		launchOperationID = strings.TrimSpace(operation.IdempotencyKey)
	}
	target := struct {
		MachineName   string `json:"machineName"`
		NodeName      string `json:"nodeName"`
		CVMInstanceID string `json:"cvmInstanceId"`
		PrivateIP     string `json:"privateIp"`
		InstanceType  string `json:"instanceType"`
		Zone          string `json:"zone"`
	}{allocation.MachineName, allocation.NodeName, firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID), allocation.PrivateIP, firstNonEmpty(plan.InstanceType, allocation.InstanceType), allocation.Zone}
	return computeClaimRecoveryBinding{
		LaunchOperationID: launchOperationID, IdempotencyKey: operation.IdempotencyKey,
		TargetHash: hashInput(target), RequestHash: operation.RequestHash,
	}
}

func terminalComputeClaimEvidence(operation FabricOperation, allocation ComputeAllocation, plan ComputeAllocationPreparation, stage, readbackStatus string, cause error, cvmBudget, nodeBudget normalLaunchMutationBudget, now time.Time, binding computeClaimRecoveryBinding, proof *ComputeClaimProviderProof) ComputeClaimTerminalEvidence {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	startedAt := operation.StartedAt
	if startedAt.IsZero() {
		startedAt = now
	}
	code := safeComputeClaimTerminalToken(errorCode(cause))
	if code == "" || code == "provider_error" {
		code = "unprovable"
	}
	code = "compute_claim_terminal_" + strings.TrimPrefix(stage, "compute_claim_") + "_" + code
	if len(code) > 120 {
		code = "compute_claim_terminal_unprovable"
	}
	reason := safeComputeClaimTerminalToken(errorCode(cause))
	evidence := ComputeClaimTerminalEvidence{
		SchemaVersion: 1, Stage: stage, Status: "terminal_unprovable", ErrorCode: code, Reason: reason,
		ReadbackStatus: readbackStatus, Attempted: cvmBudget.Attempted + nodeBudget.Attempted,
		Confirmed: cvmBudget.Confirmed + nodeBudget.Confirmed, Unknown: cvmBudget.Unknown + nodeBudget.Unknown,
		Max: cvmBudget.Max + nodeBudget.Max, StartedAt: startedAt.UTC().Format(time.RFC3339Nano), FinishedAt: now.UTC().Format(time.RFC3339Nano),
		FabricRecordID: operation.ID, OperationID: operation.OperationID, IdempotencyKey: operation.IdempotencyKey, RequestHash: operation.RequestHash,
		AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, ComputeAllocationID: operation.ResourceID,
		PackageID: allocation.PackageID, NodePoolID: allocation.NodePoolID, MachineName: allocation.MachineName,
		NodeName: allocation.NodeName, CVMInstanceID: firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID),
		BindingDigest: computeClaimIdentityDigest(binding.LaunchOperationID + "|" + binding.IdempotencyKey + "|" + binding.TargetHash + "|" + binding.RequestHash),
		StageBudgets:  map[string]ComputeClaimStageBudget{},
	}
	if cvmBudget.Max > 0 {
		evidence.StageBudgets["compute_claim_cvm"] = ComputeClaimStageBudget{Attempted: cvmBudget.Attempted, Confirmed: cvmBudget.Confirmed, Unknown: cvmBudget.Unknown, Max: cvmBudget.Max}
	}
	if nodeBudget.Max > 0 {
		evidence.StageBudgets["compute_claim_node"] = ComputeClaimStageBudget{Attempted: nodeBudget.Attempted, Confirmed: nodeBudget.Confirmed, Unknown: nodeBudget.Unknown, Max: nodeBudget.Max}
	}
	evidence.AttemptCount = evidence.Attempted
	if binding.LaunchOperationID != "" {
		evidence.LaunchOperationID = binding.LaunchOperationID
	}
	if proof != nil {
		evidence.CVMOwnershipState, evidence.NodeOwnershipState = proof.CVMOwnershipState, proof.NodeOwnershipState
	}
	return evidence
}

func safeComputeClaimTerminalToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var builder strings.Builder
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '_' {
			builder.WriteRune(char)
		} else {
			return ""
		}
	}
	return builder.String()
}

func (s *Service) computeClaimRecoveryLocalState(ctx context.Context, input ComputeClaimRecoveryInput) (FabricOperation, ComputeAllocation, ComputeAllocationPreparation, MachineOwnership, string, error) {
	operations, err := s.operations.List(ctx)
	if err != nil {
		return FabricOperation{}, ComputeAllocation{}, ComputeAllocationPreparation{}, MachineOwnership{}, "local_identity", err
	}
	var operation FabricOperation
	computeCount := 0
	for _, candidate := range operations {
		if candidate.Action == "create_compute_allocation" && (candidate.ResourceID == input.ComputeAllocationID ||
			candidate.IdempotencyKey == input.LaunchOperationID+":compute" || candidate.AccountID == input.AccountID && candidate.WorkspaceID == input.WorkspaceID) {
			computeCount++
			operation = candidate
		}
	}
	storageDisposition := computeClaimRecoveryStorageOperationDisposition(operations, input)
	if storageDisposition == computeClaimStorageOperationUnknown && !input.AllowExistingStorageOperation {
		return FabricOperation{}, ComputeAllocation{}, ComputeAllocationPreparation{}, MachineOwnership{}, "storage_already_started",
			fmt.Errorf("%w: storage_already_started", ErrComputeClaimRecoveryUnavailable)
	}
	if storageDisposition == computeClaimStorageOperationConflict && !input.AllowExistingStorageOperation {
		return FabricOperation{}, ComputeAllocation{}, ComputeAllocationPreparation{}, MachineOwnership{}, "identity_mismatch",
			fmt.Errorf("%w: identity_mismatch", ErrComputeClaimRecoveryUnavailable)
	}
	if computeCount != 1 || operation.AccountID != input.AccountID || operation.WorkspaceID != input.WorkspaceID ||
		operation.IdempotencyKey != input.LaunchOperationID+":compute" ||
		(operation.Status != "failed" && operation.Status != "claim_pending" && operation.Status != "succeeded") {
		return FabricOperation{}, ComputeAllocation{}, ComputeAllocationPreparation{}, MachineOwnership{}, "local_identity",
			fmt.Errorf("%w: local_identity", ErrComputeClaimRecoveryUnavailable)
	}
	var allocation ComputeAllocation
	plan, hasPlan := decodeComputeAllocationPlan(operation)
	if !decodeOperationResource(operation, &allocation) || !hasPlan || !validComputeClaimRecoveryLocalIdentity(input, allocation, plan) {
		return FabricOperation{}, ComputeAllocation{}, ComputeAllocationPreparation{}, MachineOwnership{}, "local_identity",
			fmt.Errorf("%w: local_identity", ErrComputeClaimRecoveryUnavailable)
	}
	ownership, err := s.operations.MachineOwnership(ctx, input.ComputeAllocationID)
	if err != nil || !validComputeClaimRecoveryOwnership(allocation, ownership) {
		return FabricOperation{}, ComputeAllocation{}, ComputeAllocationPreparation{}, MachineOwnership{}, "local_identity",
			fmt.Errorf("%w: local_identity", ErrComputeClaimRecoveryUnavailable)
	}
	return operation, allocation, plan, ownership, "", nil
}

func workspaceLaunchResourceLockKey(launchOperationID string) string {
	return "workspace-launch-resources:" + strings.TrimSpace(launchOperationID)
}

func unknownMonthlyProviderTruth(compute ComputeAllocation, storage StorageVolume) MonthlyProviderTruth {
	return MonthlyProviderTruth{ComputeState: "unknown", StorageState: "unknown", Compute: cloneComputeAllocation(compute), Storage: cloneStorageVolume(storage)}
}

func cloneComputeAllocation(value ComputeAllocation) ComputeAllocation {
	value.ProviderData = maps.Clone(value.ProviderData)
	value.CostTags = maps.Clone(value.CostTags)
	value.NodeSelector = maps.Clone(value.NodeSelector)
	return value
}

func cloneStorageVolume(value StorageVolume) StorageVolume {
	value.ProviderData = maps.Clone(value.ProviderData)
	value.CostTags = maps.Clone(value.CostTags)
	return value
}

func validMonthlyProviderTruthIdentity(compute ComputeAllocation, storage StorageVolume) bool {
	instanceID := firstNonEmpty(compute.InstanceID, compute.CVMInstanceID)
	instanceType := firstNonEmpty(compute.InstanceType, compute.ProviderData["instanceType"])
	computeZone := firstNonEmpty(compute.Zone, compute.ProviderData["zone"])
	return compute.ID != "" && compute.AccountID != "" && compute.WorkspaceID != "" &&
		(compute.PackageID == "basic" || compute.PackageID == "pro") && compute.Provider == "tencent-tke" && compute.ProviderResourceID != "" &&
		compute.NodePoolID != "" && firstNonEmpty(compute.MachineName, compute.ProviderData["machineName"]) != "" && strings.HasPrefix(instanceID, "ins-") && compute.PrivateIP != "" &&
		instanceType == packagePlan(compute.PackageID).InstanceType && computeZone != "" && validMonthlyTruthTags(compute.CostTags, compute.AccountID, compute.WorkspaceID, compute.ID) &&
		storage.ID != "" && storage.AccountID == compute.AccountID && storage.WorkspaceID == compute.WorkspaceID && storage.Provider == "tencent-tke" &&
		strings.HasPrefix(storage.ProviderResourceID, "disk-") && storage.SizeGB > 0 && storage.DiskType != "" && storage.Zone == computeZone &&
		validMonthlyTruthTags(storage.CostTags, storage.AccountID, storage.WorkspaceID, storage.ID)
}

func validMonthlyTruthTags(tags map[string]string, accountID, workspaceID, resourceID string) bool {
	return tags["opl_account_id"] == accountID && tags["opl_workspace_id"] == workspaceID && tags["opl_resource_id"] == resourceID && strings.TrimSpace(tags["opl_operation_id"]) != ""
}

func validMonthlyProviderTruthResult(result MonthlyProviderTruth, compute ComputeAllocation, storage StorageVolume) bool {
	validState := func(value string) bool { return value == "ready" || value == "absent" || value == "unknown" }
	return validState(result.ComputeState) && validState(result.StorageState) &&
		result.Compute.ID == compute.ID && result.Compute.AccountID == compute.AccountID && result.Compute.WorkspaceID == compute.WorkspaceID &&
		result.Storage.ID == storage.ID && result.Storage.AccountID == storage.AccountID && result.Storage.WorkspaceID == storage.WorkspaceID &&
		validMonthlyProviderTruthIdentity(result.Compute, result.Storage)
}

func (s *Service) MachineOwnership(ctx context.Context, resourceID string) (MachineOwnership, error) {
	return s.operations.MachineOwnership(ctx, strings.TrimSpace(resourceID))
}

func (s *Service) CreateComputeAllocation(ctx context.Context, input ComputeAllocationInput) (ComputeAllocation, error) {
	if input.PackageID != "basic" && input.PackageID != "pro" {
		return ComputeAllocation{}, ErrUnsupportedComputePackage
	}
	if strings.TrimSpace(input.NodePoolID) == "" {
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
		Provider:          "tencent-tke",
		ProviderRequestID: providerRequestID("compute", input.IdempotencyKey),
		CreatedAt:         now,
	}
	operation := newOperation("create_compute_allocation", "compute_allocation", id, input.AccountID, input.WorkspaceID, input.IdempotencyKey, requestHash, now)
	operation.ID = "fop_compute_claim_" + stableSuffix("create_compute_allocation", input.IdempotencyKey)
	operation.Status = "started"
	operation.CreatedAt = now
	operation.ComputePoolKey = allocation.NodePoolID
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

func automaticComputeClaimRecoveryInput(operation FabricOperation, allocation ComputeAllocation, plan ComputeAllocationPreparation) (ComputeClaimRecoveryClaimInput, bool) {
	launchOperationID, ok := strings.CutSuffix(strings.TrimSpace(operation.IdempotencyKey), ":compute")
	if !ok || launchOperationID == "" || allocation.ID != operation.ResourceID {
		return ComputeClaimRecoveryClaimInput{}, false
	}
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: ComputeClaimRecoveryInput{
			LaunchOperationID: launchOperationID, AccountID: allocation.AccountID, WorkspaceID: allocation.WorkspaceID,
			ComputeAllocationID: allocation.ID, StorageVolumeID: "vol_" + stableID("vol", allocation.AccountID, launchOperationID+":storage")[:18],
			PackageID: allocation.PackageID, PoolID: plan.PoolID, NodePoolID: plan.NodePoolID,
		},
		MachineName: allocation.MachineName, NodeName: allocation.NodeName,
		CVMInstanceID: firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID), PrivateIP: allocation.PrivateIP,
		InstanceType: plan.InstanceType, Zone: allocation.Zone, IdempotencyKey: operation.IdempotencyKey,
	}
	if !validComputeClaimRecoveryClaimInput(claimInput) {
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
	if !normalWorkspaceComputeBudgetEnabled(operation, s.provider) {
		s.finishCreateComputeAllocationLegacy(operation, allocation, dryRun)
		return
	}
	plan := packagePlan(allocation.PackageID)
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
		operation.RedactedProviderPayload = computeAllocationOperationPayload(allocation, prepared)
		if err := s.operations.SaveRuntime(context.Background(), operation); err != nil {
			return
		}
	}

	// The create reservation is durable and consumes the only external compute
	// write. Once it exists, every replay uses Describe/readback; a second
	// CreateComputeAllocation call is never safe after a lost response.
	createBudget, createBudgetPresent, createBudgetValid := normalLaunchStageBudget(operation.RedactedProviderPayload, "compute_create")
	if !createBudgetValid {
		terminal = true
		_ = computeAllocationFailure(context.Background(), s, operation, allocation, prepared, fmt.Errorf("compute_create_budget_invalid"))
		return
	}
	freshCreateReservation := !createBudgetPresent
	if freshCreateReservation {
		createBudget = reservedNormalLaunchMutationBudget()
		operation.RedactedProviderPayload = withNormalLaunchStageBudget(computeAllocationOperationPayload(allocation, prepared), "compute_create", createBudget)
		if err := s.operations.SaveRuntime(context.Background(), operation); err != nil {
			return
		}
		createBudgetPresent = true
	}

	var result ComputeAllocation
	if createBudget.Confirmed == 1 {
		if !decodeOperationResource(operation, &result) {
			result = allocation
		}
		result = mergeComputeAllocation(result, allocation, prepared)
	} else {
		if !createBudgetPresent || createBudget.Attempted == 0 {
			// Defensive branch: the reservation above should make this unreachable.
			return
		}
		if createBudget.Unknown == 1 && createBudget.Confirmed == 0 && operation.RedactedProviderPayload != nil {
			// Only the first owner of a fresh reservation may issue the create.
			// A replay sees the same budget and jumps directly to readback below.
			if freshCreateReservation {
				if allocation.MachineName == "" && allocation.InstanceID == "" && allocation.NodeName == "" {
					attemptCtx, cancel := context.WithTimeout(context.Background(), s.computeAllocationAttemptTimeout)
					result, err = s.provider.CreateComputeAllocation(attemptCtx, ComputeAllocationExecution{Allocation: allocation, Plan: prepared, DryRun: dryRun})
					cancel()
					result = mergeComputeAllocation(result, allocation, prepared)
					operation.ProviderRequestID = firstNonEmpty(result.ProviderRequestID, operation.ProviderRequestID)
					operation.RedactedProviderPayload = preserveNormalLaunchMutationBudget(computeAllocationOperationPayload(result, prepared), operation.RedactedProviderPayload)
					if err == nil && validateNewComputeAllocation(result, prepared) == nil {
						createBudget = confirmedNormalLaunchMutationBudget()
						operation.RedactedProviderPayload = withNormalLaunchStageBudget(operation.RedactedProviderPayload, "compute_create", createBudget)
						if saveErr := s.operations.SaveRuntime(context.Background(), operation); saveErr != nil {
							return
						}
					} else if saveErr := s.operations.SaveRuntime(context.Background(), operation); saveErr != nil {
						return
					}
				}
			}
		}
	}

	if createBudget.Confirmed != 1 {
		pollDeadline := time.Now().Add(s.computeAllocationPollWindow)
		for {
			if !claimLease(pollLease) {
				return
			}
			readCtx, cancel := context.WithTimeout(context.Background(), s.computeAllocationAttemptTimeout)
			readback, readErr := s.readComputeAllocationAfterReservation(readCtx, allocation, prepared, dryRun)
			cancel()
			readback = mergeComputeAllocation(readback, allocation, prepared)
			if readErr == nil && validateNewComputeAllocation(readback, prepared) == nil && isReadyResourceStatus(readback.Status) {
				result = readback
				createBudget = confirmedNormalLaunchMutationBudget()
				operation.ProviderRequestID = firstNonEmpty(result.ProviderRequestID, operation.ProviderRequestID)
				operation.RedactedProviderPayload = withNormalLaunchStageBudget(preserveNormalLaunchMutationBudget(computeAllocationOperationPayload(result, prepared), operation.RedactedProviderPayload), "compute_create", createBudget)
				if saveErr := s.operations.SaveRuntime(context.Background(), operation); saveErr != nil {
					return
				}
				break
			}
			if time.Now().After(pollDeadline) {
				terminal = true
				_ = computeAllocationFailure(context.Background(), s, operation, result, prepared, ErrComputeAllocationPending)
				return
			}
			wait := min(s.computeAllocationPollInterval, time.Until(pollDeadline))
			if wait <= 0 {
				return
			}
			timer := time.NewTimer(wait)
			<-timer.C
		}
	}
	if result.ID == "" {
		if !decodeOperationResource(operation, &result) {
			result = allocation
		}
		result = mergeComputeAllocation(result, allocation, prepared)
	}

	if !claimLease(s.computeAllocationFinalizeTimeout + s.computeAllocationPollInterval) {
		return
	}
	finalizeCtx, cancel := context.WithTimeout(context.Background(), s.computeAllocationFinalizeTimeout)
	defer cancel()
	if err := validateNewComputeAllocation(result, prepared); err != nil {
		terminal = true
		_ = computeAllocationFailure(context.Background(), s, operation, result, prepared, err)
		return
	}
	machine := ProviderMachine{
		MachineID: result.MachineName, InstanceID: firstNonEmpty(result.InstanceID, result.CVMInstanceID), NodeName: result.NodeName,
		PrivateIP: result.PrivateIP, PublicIP: result.PublicIP, InstanceType: result.InstanceType, Zone: result.Zone,
		ChargeType: result.ChargeType, RenewFlag: result.RenewFlag, Deadline: result.Deadline, Ready: true,
	}
	ownership := MachineOwnership{
		ID: "owner_" + stableSuffix(result.ID, result.MachineName)[:16], ResourceID: result.ID, AccountID: result.AccountID,
		WorkspaceID: result.WorkspaceID, PackageID: result.PackageID, NodePoolID: result.NodePoolID, MachineID: result.MachineName,
		InstanceID: firstNonEmpty(result.InstanceID, result.CVMInstanceID), NodeName: result.NodeName, Status: "claimed",
		ProviderRequestID: result.ProviderRequestID, ClaimedAt: s.now(),
	}
	claimed, _, claimErr := s.operations.ClaimMachine(finalizeCtx, ownership)
	if claimErr != nil {
		terminal = true
		_ = computeAllocationFailure(context.Background(), s, operation, result, prepared, claimErr)
		return
	}
	result.CostTags = oplCostTags(result.AccountID, result.WorkspaceID, result.ID, claimed.ID)
	claimErr = fmt.Errorf("compute_claim_stage_provider_unavailable")
	if split, ok := s.provider.(normalComputeClaimStageProvider); ok {
		claimErr = s.convergeNormalComputeClaimStages(finalizeCtx, &operation, result, prepared, machine, claimed, split)
	}
	if claimErr == nil {
		claimErr = errors.New("compute_claim_control_plane_decision_required")
	}
	claimed.Status = "quarantined"
	_ = s.operations.SaveMachineOwnership(context.Background(), claimed)
	terminal = true
	_ = computeAllocationClaimPending(context.Background(), s, operation, result, prepared, claimErr)
}

func normalWorkspaceComputeBudgetEnabled(operation FabricOperation, provider Provider) bool {
	if !strings.HasSuffix(strings.TrimSpace(operation.IdempotencyKey), ":compute") {
		return false
	}
	_, ok := provider.(computeAllocationReadbackProvider)
	return ok
}

func (s *Service) convergeNormalComputeClaimStages(ctx context.Context, operation *FabricOperation, allocation ComputeAllocation, prepared ComputeAllocationPreparation, machine ProviderMachine, ownership MachineOwnership, provider normalComputeClaimStageProvider) error {
	reader, ok := s.provider.(computeClaimRecoveryProvider)
	if !ok {
		return fmt.Errorf("compute_claim_readback_unavailable")
	}
	type claimStage struct {
		name   string
		mutate func() error
		proved func(ComputeClaimProviderProof) bool
	}
	stages := []claimStage{
		{
			name: "compute_claim_cvm",
			mutate: func() error {
				return provider.TagComputeMachineCVM(ctx, machine, ownership)
			},
			proved: func(proof ComputeClaimProviderProof) bool { return proof.CVMOwnershipState == "target_owned" },
		},
	}
	for _, stage := range stages {
		budget, present, valid := normalLaunchStageBudget(operation.RedactedProviderPayload, stage.name)
		if !valid {
			return fmt.Errorf("%s_budget_invalid", stage.name)
		}
		fresh := !present
		if fresh {
			budget = reservedNormalLaunchMutationBudget()
			operation.RedactedProviderPayload = withNormalLaunchStageBudget(preserveNormalLaunchMutationBudget(computeAllocationOperationPayload(allocation, prepared), operation.RedactedProviderPayload), stage.name, budget)
			if err := s.operations.SaveRuntime(ctx, *operation); err != nil {
				return err
			}
		}
		if budget.Confirmed == 1 {
			continue
		}
		var mutationErr error
		if fresh {
			mutationErr = stage.mutate()
		}
		proof, readErr := reader.ProveComputeClaimRecovery(ctx, allocation, prepared, ownership)
		if readErr != nil || !validComputeClaimProviderProof(proof, allocation, prepared) || !stage.proved(proof) {
			if readErr != nil {
				return readErr
			}
			if mutationErr != nil {
				return mutationErr
			}
			return fmt.Errorf("%s_readback_mismatch", stage.name)
		}
		budget = confirmedNormalLaunchMutationBudget()
		operation.RedactedProviderPayload = withNormalLaunchStageBudget(preserveNormalLaunchMutationBudget(computeAllocationOperationPayload(allocation, prepared), operation.RedactedProviderPayload), stage.name, budget)
		if err := s.operations.SaveRuntime(ctx, *operation); err != nil {
			return err
		}
	}
	return errors.New("compute_claim_control_plane_decision_required")
}

// finishCreateComputeAllocationLegacy preserves the established compute
// contract for callers outside the normal Workspace launch. The durable
// reservation/readback budget above is intentionally a narrow launch boundary;
// unrelated compute operations retain their existing retry semantics.
func (s *Service) finishCreateComputeAllocationLegacy(operation FabricOperation, allocation ComputeAllocation, dryRun bool) {
	plan := packagePlan(allocation.PackageID)
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
		operation.RedactedProviderPayload = computeAllocationOperationPayload(allocation, prepared)
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
		result, err = s.provider.CreateComputeAllocation(attemptCtx, ComputeAllocationExecution{Allocation: allocation, Plan: prepared, DryRun: dryRun})
		cancel()
		attempted = true
		result = mergeComputeAllocation(result, allocation, prepared)
		if errors.Is(err, ErrComputeAllocationPending) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			operation.RedactedProviderPayload = computeAllocationOperationPayload(result, prepared)
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
	if err := validateNewComputeAllocation(result, prepared); err != nil {
		terminal = true
		_ = computeAllocationFailure(context.Background(), s, operation, result, prepared, err)
		return
	}
	machine := ProviderMachine{
		MachineID: result.MachineName, InstanceID: firstNonEmpty(result.InstanceID, result.CVMInstanceID), NodeName: result.NodeName,
		PrivateIP: result.PrivateIP, PublicIP: result.PublicIP, InstanceType: result.InstanceType, Zone: result.Zone,
		ChargeType: result.ChargeType, RenewFlag: result.RenewFlag, Deadline: result.Deadline, Ready: true,
	}
	ownership := MachineOwnership{
		ID: "owner_" + stableSuffix(result.ID, result.MachineName)[:16], ResourceID: result.ID, AccountID: result.AccountID,
		WorkspaceID: result.WorkspaceID, PackageID: result.PackageID, NodePoolID: result.NodePoolID, MachineID: result.MachineName,
		InstanceID: firstNonEmpty(result.InstanceID, result.CVMInstanceID), NodeName: result.NodeName, Status: "claimed",
		ProviderRequestID: result.ProviderRequestID, ClaimedAt: s.now(),
	}
	claimed, _, claimErr := s.operations.ClaimMachine(finalizeCtx, ownership)
	if claimErr != nil {
		terminal = true
		_ = computeAllocationFailure(context.Background(), s, operation, result, prepared, claimErr)
		return
	}
	result.CostTags = oplCostTags(result.AccountID, result.WorkspaceID, result.ID, claimed.ID)
	if tagErr := s.provider.TagComputeMachine(finalizeCtx, machine, claimed); tagErr != nil {
		claimed.Status = "quarantined"
		_ = s.operations.SaveMachineOwnership(context.Background(), claimed)
		terminal = true
		_ = computeAllocationClaimPending(context.Background(), s, operation, result, prepared, tagErr)
		return
	}
	verified, verifyErr := s.provider.SyncComputeAllocation(finalizeCtx, result)
	verified = mergeComputeAllocation(verified, result, prepared)
	if verifyErr != nil || validateNewComputeAllocation(verified, prepared) != nil || !isReadyResourceStatus(verified.Status) {
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
	operation.RedactedProviderPayload = computeAllocationOperationPayload(verified, prepared)
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
	current.Provider = firstNonEmpty(current.Provider, fallback.Provider, "tencent-tke")
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
		allocation.Provider = firstNonEmpty(existing.Provider, "tencent-tke")
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
	// The staged boundary belongs to the Workspace launch orchestrator. Keep
	// other monthly storage callers on their established provider contract until
	// they explicitly carry the launch storage identity.
	if staged, ok := s.provider.(stagedStorageProvider); ok && strings.HasSuffix(strings.TrimSpace(input.IdempotencyKey), ":storage") {
		return s.createStorageVolumeStaged(ctx, input, staged)
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
	computeZone := strings.TrimSpace(compute.ProviderData["zone"])
	if compute.ID == "" || compute.AccountID != input.AccountID || compute.WorkspaceID != input.WorkspaceID ||
		!isReadyResourceStatus(compute.Status) || computeZone == "" || strings.TrimSpace(input.Zone) != computeZone {
		return StorageVolume{}, fmt.Errorf("storage_compute_zone_mismatch")
	}
	requestHash := hashInput(input)
	var volume StorageVolume
	lockKey := "storage-create:" + firstNonEmpty(input.IdempotencyKey, input.ID)
	if strings.HasSuffix(input.IdempotencyKey, ":storage") {
		lockKey = workspaceLaunchResourceLockKey(strings.TrimSuffix(input.IdempotencyKey, ":storage"))
	}
	err := s.operations.WithPoolLock(ctx, lockKey, func(lockCtx context.Context) error {
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
		operation := newOperation("create_storage_volume", "storage_volume", input.ID, input.AccountID, input.WorkspaceID, input.IdempotencyKey, requestHash, s.now())
		input.OperationID = operation.OperationID
		if err := s.recordOperation(lockCtx, operation, "started", StorageVolume{ID: input.ID, OperationID: input.IdempotencyKey, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Provider: "tencent-tke", ProviderRequestID: providerRequestID("storage", input.IdempotencyKey)}, nil); err != nil {
			return err
		}
		volume, err = s.provider.CreateStorageVolume(lockCtx, input)
		volume.ID = input.ID
		volume.OperationID = input.IdempotencyKey
		volume.AccountID = firstNonEmpty(volume.AccountID, input.AccountID)
		volume.WorkspaceID = firstNonEmpty(volume.WorkspaceID, input.WorkspaceID)
		volume.Provider = firstNonEmpty(volume.Provider, "tencent-tke")
		volume.Zone = firstNonEmpty(volume.Zone, input.Zone)
		if volume.SizeGB == 0 {
			volume.SizeGB = input.SizeGB
		}
		if err != nil {
			knownCBS := strings.HasPrefix(volume.ProviderResourceID, "disk-")
			if knownCBS {
				volume.Status = "quarantined"
			}
			if recordErr := s.recordOperation(lockCtx, operation, "failed", volume, err); recordErr != nil {
				return recordErr
			}
			if knownCBS {
				s.mu.Lock()
				s.volumes[volume.ID] = volume
				s.mu.Unlock()
			}
			return err
		}
		if err := s.recordOperation(lockCtx, operation, "succeeded", volume, nil); err != nil {
			return err
		}
		s.mu.Lock()
		s.volumes[volume.ID] = volume
		s.mu.Unlock()
		return nil
	})
	return volume, err
}

func (s *Service) createStorageVolumeStaged(ctx context.Context, input StorageVolumeInput, provider stagedStorageProvider) (StorageVolume, error) {
	if input.ID == "" {
		input.ID = "vol_" + stableSuffix("create_storage_volume", input.IdempotencyKey)[:16]
	}
	s.mu.Lock()
	compute := s.computes[input.ComputeID]
	s.mu.Unlock()
	computeZone := strings.TrimSpace(compute.ProviderData["zone"])
	if compute.ID == "" || compute.AccountID != input.AccountID || compute.WorkspaceID != input.WorkspaceID ||
		!isReadyResourceStatus(compute.Status) || computeZone == "" || strings.TrimSpace(input.Zone) != computeZone {
		return StorageVolume{}, fmt.Errorf("storage_compute_zone_mismatch")
	}
	requestHash := hashInput(input)
	now := s.now()
	parent := newOperation("create_storage_volume", "storage_volume", input.ID, input.AccountID, input.WorkspaceID, input.IdempotencyKey, requestHash, now)
	parent.ID = "fop_storage_create_" + stableSuffix(input.IdempotencyKey)[:18]
	parent.Status = "started"
	parent.CreatedAt = now
	parentResource := StorageVolume{ID: input.ID, OperationID: input.IdempotencyKey, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Provider: "tencent-tke", SizeGB: input.SizeGB, Zone: input.Zone, CreatedAt: now}
	fillOperationResource(&parent, parentResource)
	storedParent, claimedParent, err := s.operations.ClaimRuntime(ctx, parent)
	if err != nil {
		return StorageVolume{}, err
	}
	if storedParent.RequestHash != requestHash {
		return StorageVolume{}, fmt.Errorf("storage_create_idempotency_conflict")
	}
	if !claimedParent && storedParent.Status == "succeeded" {
		var replayed StorageVolume
		if decodeOperationResource(storedParent, &replayed) {
			return replayed, nil
		}
		return StorageVolume{}, fmt.Errorf("storage_operation_corrupt")
	}
	parent = storedParent
	input.OperationID = parent.OperationID

	volume, err := s.runStorageCBSStage(ctx, input, parent, provider)
	if err != nil {
		return volume, err
	}
	volume, err = s.runStorageStaticBindingStage(ctx, input, parent, volume, provider)
	if err != nil {
		return volume, err
	}
	parent.Status = "succeeded"
	parent.FinishedAt = s.now()
	fillOperationResource(&parent, volume)
	parent.RedactedProviderPayload = withNormalLaunchStageBudget(parent.RedactedProviderPayload, "cbs_create", confirmedNormalLaunchMutationBudget())
	parent.RedactedProviderPayload = withNormalLaunchStageBudget(parent.RedactedProviderPayload, "static_binding_apply", confirmedNormalLaunchMutationBudget())
	if err := s.operations.SaveRuntime(ctx, parent); err != nil {
		return volume, err
	}
	s.mu.Lock()
	s.volumes[volume.ID] = volume
	s.mu.Unlock()
	return volume, nil
}

func (s *Service) runStorageCBSStage(ctx context.Context, input StorageVolumeInput, parent FabricOperation, provider stagedStorageProvider) (StorageVolume, error) {
	stageKey := input.IdempotencyKey + ":cbs_create"
	stageHash := hashInput(map[string]any{"input": input, "stage": "cbs_create", "parentOperationId": parent.OperationID})
	now := s.now()
	stage := newOperation("cbs_create", "storage_volume", input.ID, input.AccountID, input.WorkspaceID, stageKey, stageHash, now)
	stage.ID = "fop_cbs_create_" + stableSuffix(stageKey)[:18]
	stage.Status = "started"
	stage.CreatedAt = now
	initial := StorageVolume{ID: input.ID, OperationID: input.IdempotencyKey, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Provider: "tencent-tke", Status: "pending", SizeGB: input.SizeGB, Zone: input.Zone, CostTags: oplCostTags(input.AccountID, input.WorkspaceID, input.ID, parent.OperationID), CreatedAt: now}
	fillOperationResource(&stage, initial)
	budget, present, valid := normalLaunchStageBudget(stage.RedactedProviderPayload, "cbs_create")
	if !present {
		budget = reservedNormalLaunchMutationBudget()
		stage.RedactedProviderPayload = withNormalLaunchStageBudget(stage.RedactedProviderPayload, "cbs_create", budget)
	}
	if !valid {
		return initial, fmt.Errorf("cbs_create_budget_invalid")
	}
	stored, claimed, err := s.operations.ClaimRuntime(ctx, stage)
	if err != nil {
		return initial, err
	}
	if stored.RequestHash != stageHash {
		return initial, fmt.Errorf("cbs_create_idempotency_conflict")
	}
	if !claimed {
		var persisted StorageVolume
		_ = decodeOperationResource(stored, &persisted)
		if stored.Status == "succeeded" && validStorageStageVolume(persisted, input) {
			return persisted, nil
		}
		readback, readErr := provider.ReadCBSVolume(ctx, input, persisted)
		readback = normalizeStorageStageVolume(readback, input, parent.OperationID)
		if readErr != nil || !validStorageStageVolume(readback, input) {
			if readErr == nil {
				readErr = fmt.Errorf("cbs_create_readback_mismatch")
			}
			return readback, readErr
		}
		budget = confirmedNormalLaunchMutationBudget()
		if _, convergeErr := s.convergeRuntimeOperationReadback(ctx, stored, readback, map[string]any{"normalLaunchMutationBudget": map[string]any{"cbs_create": map[string]any{"attempted": budget.Attempted, "confirmed": budget.Confirmed, "unknown": budget.Unknown, "max": budget.Max}}}); convergeErr != nil {
			return readback, convergeErr
		}
		return readback, nil
	}
	// Reservation is saved by ClaimRuntime before the provider call.
	created, providerErr := provider.CreateCBSVolume(ctx, input)
	created = normalizeStorageStageVolume(created, input, parent.OperationID)
	if providerErr != nil {
		stored.Status = "failed"
		stored.FinishedAt = s.now()
		stored.ErrorCode = errorCode(providerErr)
		fillOperationResource(&stored, created)
		stored.RedactedProviderPayload = withNormalLaunchStageBudget(stored.RedactedProviderPayload, "cbs_create", budget)
		_ = s.operations.SaveRuntime(ctx, stored)
		return created, providerErr
	}
	if !validStorageStageVolume(created, input) {
		providerErr = fmt.Errorf("cbs_create_readback_mismatch")
		stored.Status = "failed"
		stored.FinishedAt = s.now()
		stored.ErrorCode = errorCode(providerErr)
		fillOperationResource(&stored, created)
		stored.RedactedProviderPayload = withNormalLaunchStageBudget(stored.RedactedProviderPayload, "cbs_create", budget)
		_ = s.operations.SaveRuntime(ctx, stored)
		return created, providerErr
	}
	budget = confirmedNormalLaunchMutationBudget()
	stored.Status = "succeeded"
	stored.FinishedAt = s.now()
	fillOperationResource(&stored, created)
	stored.RedactedProviderPayload = withNormalLaunchStageBudget(stored.RedactedProviderPayload, "cbs_create", budget)
	if err := s.operations.SaveRuntime(ctx, stored); err != nil {
		return created, err
	}
	return created, nil
}

func (s *Service) runStorageStaticBindingStage(ctx context.Context, input StorageVolumeInput, parent FabricOperation, volume StorageVolume, provider stagedStorageProvider) (StorageVolume, error) {
	stageKey := input.IdempotencyKey + ":static_binding_apply"
	stageHash := hashInput(map[string]any{"input": input, "stage": "static_binding_apply", "parentOperationId": parent.OperationID, "providerResourceId": volume.ProviderResourceID})
	now := s.now()
	stage := newOperation("static_binding_apply", "storage_volume", input.ID, input.AccountID, input.WorkspaceID, stageKey, stageHash, now)
	stage.ID = "fop_static_binding_" + stableSuffix(stageKey)[:18]
	stage.Status = "started"
	stage.CreatedAt = now
	fillOperationResource(&stage, volume)
	budget := reservedNormalLaunchMutationBudget()
	stage.RedactedProviderPayload = withNormalLaunchStageBudget(stage.RedactedProviderPayload, "static_binding_apply", budget)
	stored, claimed, err := s.operations.ClaimRuntime(ctx, stage)
	if err != nil {
		return volume, err
	}
	if stored.RequestHash != stageHash {
		return volume, fmt.Errorf("static_binding_apply_idempotency_conflict")
	}
	if !claimed {
		var persisted StorageVolume
		_ = decodeOperationResource(stored, &persisted)
		if persisted.ProviderResourceID != volume.ProviderResourceID || !validStorageStageVolume(persisted, input) {
			return volume, fmt.Errorf("static_binding_apply_identity_mismatch")
		}
		readback, readErr := provider.ReadStaticStorageBinding(ctx, persisted)
		readback = normalizeStorageStageVolume(readback, input, parent.OperationID)
		if readErr != nil || !validStorageStageVolume(readback, input) || readback.Status != "ready" {
			if readErr == nil {
				readErr = fmt.Errorf("static_binding_apply_readback_mismatch")
			}
			return readback, readErr
		}
		budget = confirmedNormalLaunchMutationBudget()
		if _, convergeErr := s.convergeRuntimeOperationReadback(ctx, stored, readback, map[string]any{"normalLaunchMutationBudget": map[string]any{"static_binding_apply": map[string]any{"attempted": budget.Attempted, "confirmed": budget.Confirmed, "unknown": budget.Unknown, "max": budget.Max}}}); convergeErr != nil {
			return readback, convergeErr
		}
		return readback, nil
	}
	bound, providerErr := provider.ApplyStaticStorageBinding(ctx, volume)
	bound = normalizeStorageStageVolume(bound, input, parent.OperationID)
	if providerErr != nil || !validStorageStageVolume(bound, input) || bound.Status != "ready" {
		if providerErr == nil {
			providerErr = fmt.Errorf("static_binding_apply_readback_mismatch")
		}
		stored.Status = "failed"
		stored.FinishedAt = s.now()
		stored.ErrorCode = errorCode(providerErr)
		fillOperationResource(&stored, bound)
		stored.RedactedProviderPayload = withNormalLaunchStageBudget(stored.RedactedProviderPayload, "static_binding_apply", budget)
		_ = s.operations.SaveRuntime(ctx, stored)
		return bound, providerErr
	}
	budget = confirmedNormalLaunchMutationBudget()
	stored.Status = "succeeded"
	stored.FinishedAt = s.now()
	fillOperationResource(&stored, bound)
	stored.RedactedProviderPayload = withNormalLaunchStageBudget(stored.RedactedProviderPayload, "static_binding_apply", budget)
	if err := s.operations.SaveRuntime(ctx, stored); err != nil {
		return bound, err
	}
	return bound, nil
}

func normalizeStorageStageVolume(volume StorageVolume, input StorageVolumeInput, operationID string) StorageVolume {
	volume.ID = firstNonEmpty(volume.ID, input.ID)
	volume.OperationID = firstNonEmpty(volume.OperationID, input.IdempotencyKey)
	volume.AccountID = firstNonEmpty(volume.AccountID, input.AccountID)
	volume.WorkspaceID = firstNonEmpty(volume.WorkspaceID, input.WorkspaceID)
	volume.Provider = firstNonEmpty(volume.Provider, "tencent-tke")
	volume.SizeGB = firstInt(volume.SizeGB, input.SizeGB)
	volume.Zone = firstNonEmpty(volume.Zone, input.Zone)
	volume.CostTags = firstStringMap(volume.CostTags, oplCostTags(input.AccountID, input.WorkspaceID, input.ID, operationID))
	return volume
}

func validStorageStageVolume(volume StorageVolume, input StorageVolumeInput) bool {
	return volume.ID == input.ID && volume.AccountID == input.AccountID && volume.WorkspaceID == input.WorkspaceID && volume.SizeGB == input.SizeGB && volume.Zone == input.Zone && strings.HasPrefix(volume.ProviderResourceID, "disk-") && volume.Provider == "tencent-tke" && volume.RenewFlag == "NOTIFY_AND_MANUAL_RENEW" && volume.DiskType != "" && volume.Deadline != ""
}

func firstInt(value, fallback int) int {
	if value != 0 {
		return value
	}
	return fallback
}

func firstStringMap(value, fallback map[string]string) map[string]string {
	if len(value) != 0 {
		return value
	}
	return fallback
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
		volume.Provider = firstNonEmpty(existing.Provider, "tencent-tke")
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
	fillOperationResource(&operation, StorageAttachment{ID: attachmentID, OperationID: input.IdempotencyKey, WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID, Provider: "tencent-tke", ProviderRequestID: providerRequestID("storage-attach", input.IdempotencyKey)})
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
	attachment, err := s.provider.CreateStorageAttachment(ctx, input, compute, volume)
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
	now := s.now()
	operation := newOperation(action, "workspace_runtime", input.WorkspaceID, compute.AccountID, input.WorkspaceID, input.IdempotencyKey, requestHash, now)
	operation.ID = "fop_runtime_claim_" + stableSuffix(action, input.IdempotencyKey)
	operation.Status = "started"
	operation.CreatedAt = now
	fillOperationResource(&operation, WorkspaceRuntime{ID: original.ID, OperationID: input.RuntimeOperationID, WorkspaceID: input.WorkspaceID, ProviderRequestID: providerRequestID("runtime", input.IdempotencyKey)})
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
			if err := validateRuntimeInput(input, compute, volume, attachment, action == "update_workspace_runtime"); err != nil {
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
	if err := validateRuntimeInput(input, compute, volume, attachment, action == "update_workspace_runtime"); err != nil {
		_ = s.saveRuntimeOperation(ctx, stored, "failed", WorkspaceRuntime{WorkspaceID: input.WorkspaceID, ProviderRequestID: stored.ProviderRequestID}, err)
		return WorkspaceRuntime{}, err
	}
	runtime, err := s.provider.CreateWorkspaceRuntime(ctx, input, compute, volume)
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
	now := s.now()
	secretRef := gatewaySecretName(input.WorkspaceID)
	operation := newOperation("upsert_gateway_secret", "gateway_secret", secretRef, input.AccountID, input.WorkspaceID, input.IdempotencyKey, requestHash, now)
	operation.ID = "fop_gateway_secret_claim_" + stableSuffix("upsert_gateway_secret", input.IdempotencyKey)
	operation.Status = "started"
	operation.CreatedAt = now
	operation.ProviderRequestID = providerRequestID("gateway-secret", input.IdempotencyKey)
	operation.RedactedProviderPayload = map[string]any{"resource": GatewaySecret{SecretRef: secretRef}, "keyDigest": keyDigest}
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
	secret, providerErr := s.provider.UpsertGatewaySecret(ctx, input)
	stored.Status = operationStatus(providerErr)
	stored.FinishedAt = s.now()
	stored.ErrorCode = errorCode(providerErr)
	stored.RedactedProviderPayload = map[string]any{"resource": secret, "keyDigest": keyDigest}
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

func validateRuntimeInput(input WorkspaceRuntimeInput, compute ComputeAllocation, volume StorageVolume, attachment StorageAttachment, update bool) error {
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
	if !validWorkspaceRuntimeImageIdentity(input.ImageID) {
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
