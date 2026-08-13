package fabric

import (
	"context"
	"maps"
	"reflect"
	"strings"
)

type ComputeClaimRecoveryInput struct {
	LaunchOperationID   string `json:"launchOperationId"`
	AccountID           string `json:"accountId"`
	WorkspaceID         string `json:"workspaceId"`
	ComputeAllocationID string `json:"computeAllocationId"`
	StorageVolumeID     string `json:"storageVolumeId"`
	PackageID           string `json:"packageId"`
	PoolID              string `json:"poolId"`
	NodePoolID          string `json:"nodePoolId"`
	// AllowExistingStorageOperation is restricted to the server-owned
	// compute-first readback shape. It never authorizes a storage write.
	AllowExistingStorageOperation bool `json:"allowExistingStorageOperation,omitempty"`
}

type ComputeClaimRecoveryClaimInput struct {
	ComputeClaimRecoveryInput
	MachineName          string `json:"machineName"`
	NodeName             string `json:"nodeName"`
	CVMInstanceID        string `json:"cvmInstanceId"`
	PrivateIP            string `json:"privateIp"`
	InstanceType         string `json:"instanceType"`
	Zone                 string `json:"zone"`
	NodeOnlyContinuation bool   `json:"nodeOnlyContinuation,omitempty"`
	IdempotencyKey       string `json:"-"`
}

type ComputeClaimProviderProof struct {
	Status                  string                               `json:"status"`
	Reason                  string                               `json:"reason,omitempty"`
	NodeOwnershipState      string                               `json:"nodeOwnershipState"`
	CVMOwnershipState       string                               `json:"cvmOwnershipState"`
	MachineName             string                               `json:"machineName"`
	NodeName                string                               `json:"nodeName"`
	CVMInstanceID           string                               `json:"cvmInstanceId"`
	PrivateIP               string                               `json:"privateIp"`
	InstanceType            string                               `json:"instanceType"`
	Zone                    string                               `json:"zone"`
	ChargeType              string                               `json:"chargeType"`
	PeriodMonths            int                                  `json:"periodMonths"`
	RenewFlag               string                               `json:"renewFlag"`
	Deadline                string                               `json:"deadline"`
	FailureStage            string                               `json:"failureStage,omitempty"`
	ProviderErrorClass      string                               `json:"providerErrorClass,omitempty"`
	ProviderIdentityFailure *ComputeClaimProviderIdentityFailure `json:"providerIdentityFailure,omitempty"`
}

type ComputeClaimProviderIdentityFailure struct {
	Predicate      string `json:"predicate"`
	ExpectedDigest string `json:"expectedDigest"`
	ActualDigest   string `json:"actualDigest"`
}

// ComputeClaimMutationEvidence separates calls attempted from mutations that
// were confirmed by an authoritative readback. Unknown is deliberately kept
// distinct from zero so callers cannot mistake an unavailable read for proof.
type ComputeClaimMutationEvidence struct {
	Attempted int      `json:"attempted"`
	Confirmed int      `json:"confirmed"`
	Unknown   int      `json:"unknown"`
	Missing   []string `json:"missing,omitempty"`
}

type ComputeClaimEvidence struct {
	CVM  ComputeClaimMutationEvidence `json:"cvm"`
	Node ComputeClaimMutationEvidence `json:"node"`
}

// ComputeClaimTerminalEvidence is persisted when the automatic claim worker
// can no longer prove a claim stage.  It is intentionally redacted and keeps
// the original operation/resource binding so an operator can diagnose the
// exact continuation without replaying a provider mutation.
type ComputeClaimTerminalEvidence struct {
	SchemaVersion              int                                `json:"schemaVersion"`
	Stage                      string                             `json:"stage"`
	Status                     string                             `json:"status"`
	ErrorCode                  string                             `json:"errorCode"`
	Reason                     string                             `json:"reason,omitempty"`
	ReadbackStatus             string                             `json:"readbackStatus"`
	AttemptCount               int                                `json:"attemptCount"`
	Attempted                  int                                `json:"attempted"`
	Confirmed                  int                                `json:"confirmed"`
	Unknown                    int                                `json:"unknown"`
	Max                        int                                `json:"max"`
	StartedAt                  string                             `json:"startedAt"`
	FinishedAt                 string                             `json:"finishedAt"`
	FabricRecordID             string                             `json:"fabricRecordId"`
	OperationID                string                             `json:"operationId"`
	IdempotencyKey             string                             `json:"idempotencyKey"`
	RequestHash                string                             `json:"requestHash"`
	LaunchOperationID          string                             `json:"launchOperationId,omitempty"`
	AccountID                  string                             `json:"accountId"`
	WorkspaceID                string                             `json:"workspaceId"`
	ComputeAllocationID        string                             `json:"computeAllocationId"`
	StorageVolumeID            string                             `json:"storageVolumeId,omitempty"`
	PackageID                  string                             `json:"packageId"`
	PoolID                     string                             `json:"poolId,omitempty"`
	NodePoolID                 string                             `json:"nodePoolId"`
	MachineName                string                             `json:"machineName,omitempty"`
	NodeName                   string                             `json:"nodeName,omitempty"`
	CVMInstanceID              string                             `json:"cvmInstanceId,omitempty"`
	CVMOwnershipState          string                             `json:"cvmOwnershipState,omitempty"`
	NodeOwnershipState         string                             `json:"nodeOwnershipState,omitempty"`
	BindingDigest              string                             `json:"bindingDigest,omitempty"`
	OperatorApprovalID         string                             `json:"operatorApprovalId,omitempty"`
	OperatorApprovalDigest     string                             `json:"operatorApprovalDigest,omitempty"`
	OperatorIdempotencyKey     string                             `json:"operatorIdempotencyKey,omitempty"`
	ManualRecoveryLedgerDigest string                             `json:"manualRecoveryLedgerDigest,omitempty"`
	Evidence                   *ComputeClaimEvidence              `json:"evidence,omitempty"`
	StageBudgets               map[string]ComputeClaimStageBudget `json:"stageBudgets,omitempty"`
}

type ComputeClaimStageBudget struct {
	Attempted int `json:"attempted"`
	Confirmed int `json:"confirmed"`
	Unknown   int `json:"unknown"`
	Max       int `json:"max"`
}

type ComputeClaimIdentityCheck struct {
	Field          string `json:"field"`
	Matches        bool   `json:"matches"`
	Expected       string `json:"expected,omitempty"`
	Actual         string `json:"actual,omitempty"`
	ExpectedDigest string `json:"expectedDigest,omitempty"`
	ActualDigest   string `json:"actualDigest,omitempty"`
}

type ComputeClaimIdentityEvidence struct {
	Checks                []ComputeClaimIdentityCheck         `json:"checks"`
	BindingClassification string                              `json:"bindingClassification"`
	BindingDigest         string                              `json:"bindingDigest"`
	MutationLedger        string                              `json:"mutationLedger"`
	MutationLedgerOutcome string                              `json:"mutationLedgerOutcome"`
	MutationLedgerDigest  string                              `json:"mutationLedgerDigest"`
	MutationEvidence      *ComputeClaimEvidence               `json:"mutationEvidence,omitempty"`
	FailureStage          string                              `json:"failureStage"`
	ProviderErrorClass    string                              `json:"providerErrorClass"`
	Reconciliation        *ComputeClaimReconciliationEvidence `json:"reconciliation,omitempty"`
}

type ComputeClaimReconciliationEvidence struct {
	SchemaVersion              int                          `json:"schemaVersion"`
	Consumer                   string                       `json:"consumer"`
	Generation                 string                       `json:"generation"`
	ProvenanceSource           string                       `json:"provenanceSource,omitempty"`
	ProvenanceDigest           string                       `json:"provenanceDigest,omitempty"`
	State                      string                       `json:"state"`
	ExpectedRequestHashDigest  string                       `json:"expectedRequestHashDigest"`
	PersistedRequestHashDigest string                       `json:"persistedRequestHashDigest"`
	FailureStage               string                       `json:"failureStage,omitempty"`
	ProviderErrorClass         string                       `json:"providerErrorClass,omitempty"`
	Node                       ComputeClaimMutationEvidence `json:"node"`
}

type ComputeClaimRecoveryProof struct {
	SchemaVersion             int                                  `json:"schemaVersion"`
	Eligible                  bool                                 `json:"eligible"`
	Reason                    string                               `json:"reason"`
	RecoveryClassification    string                               `json:"recoveryClassification,omitempty"`
	StorageState              string                               `json:"storageState"`
	StorageProviderResourceID string                               `json:"storageProviderResourceId,omitempty"`
	LaunchOperationID         string                               `json:"launchOperationId"`
	AccountID                 string                               `json:"accountId"`
	WorkspaceID               string                               `json:"workspaceId"`
	ComputeAllocationID       string                               `json:"computeAllocationId"`
	StorageVolumeID           string                               `json:"storageVolumeId"`
	PackageID                 string                               `json:"packageId"`
	PoolID                    string                               `json:"poolId"`
	NodePoolID                string                               `json:"nodePoolId"`
	MachineName               string                               `json:"machineName,omitempty"`
	NodeName                  string                               `json:"nodeName,omitempty"`
	CVMInstanceID             string                               `json:"cvmInstanceId,omitempty"`
	PrivateIP                 string                               `json:"privateIp,omitempty"`
	InstanceType              string                               `json:"instanceType,omitempty"`
	Zone                      string                               `json:"zone,omitempty"`
	ChargeType                string                               `json:"chargeType,omitempty"`
	PeriodMonths              int                                  `json:"periodMonths,omitempty"`
	RenewFlag                 string                               `json:"renewFlag,omitempty"`
	Deadline                  string                               `json:"deadline,omitempty"`
	NodeOwnershipState        string                               `json:"nodeOwnershipState,omitempty"`
	CVMOwnershipState         string                               `json:"cvmOwnershipState,omitempty"`
	Sub2APIMutationCount      int                                  `json:"sub2apiMutationCount"`
	TencentMutationCount      int                                  `json:"tencentMutationCount"`
	KubernetesMutationCount   int                                  `json:"kubernetesMutationCount"`
	FailureStage              string                               `json:"failureStage,omitempty"`
	ProviderErrorClass        string                               `json:"providerErrorClass,omitempty"`
	ProviderIdentityFailure   *ComputeClaimProviderIdentityFailure `json:"providerIdentityFailure,omitempty"`
	Evidence                  *ComputeClaimEvidence                `json:"evidence,omitempty"`
	IdentityEvidence          *ComputeClaimIdentityEvidence        `json:"identityEvidence,omitempty"`
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
func validComputeClaimRecoveryTransition(current, next FabricOperation) bool {
	if current.Status == "failed" && next.Status == "claim_pending" ||
		current.Status == "claim_pending" && next.Status == "claim_pending" ||
		current.Status == "claim_pending" && next.Status == "succeeded" ||
		current.Status == "claim_pending" && next.Status == "failed" {
		return true
	}
	if current.Status != "succeeded" || next.Status != "succeeded" {
		return false
	}
	currentLedger, currentPresent, currentValid := decodeComputeClaimRecoveryMutation(current)
	nextLedger, nextPresent, nextValid := decodeComputeClaimRecoveryMutation(next)
	validNodeTransition := !currentPresent && nextPresent && nextValid && nextLedger.State == "node_reserved" &&
		(validLegacyNodeReservationTransition(current, next, nextLedger) || validConfirmedNodeDriftReservationTransition(current, next, nextLedger)) ||
		currentPresent && currentValid && currentLedger.State == "node_reserved" && nextPresent && nextValid &&
			nextLedger.State == "observed" && nextLedger.TencentMutationCount == 0 && currentLedger.Generation == nextLedger.Generation &&
			currentLedger.AttemptDigest == nextLedger.AttemptDigest &&
			reflect.DeepEqual(nextLedger.Evidence.CVM, ComputeClaimMutationEvidence{})
	if !validNodeTransition {
		return false
	}
	currentWithoutPayload, nextWithoutPayload := current, next
	currentWithoutPayload.RedactedProviderPayload, nextWithoutPayload.RedactedProviderPayload = nil, nil
	if !reflect.DeepEqual(currentWithoutPayload, nextWithoutPayload) {
		return false
	}
	currentPayload, nextPayload := maps.Clone(current.RedactedProviderPayload), maps.Clone(next.RedactedProviderPayload)
	delete(currentPayload, computeClaimRecoveryMutationPayloadKey)
	delete(nextPayload, computeClaimRecoveryMutationPayloadKey)
	return reflect.DeepEqual(currentPayload, nextPayload)
}

func sameComputeClaimRecoveryOperation(current, next FabricOperation) bool {
	return current.ID == next.ID && current.Action == "create_compute_allocation" && current.Action == next.Action &&
		current.ResourceKind == next.ResourceKind && current.ResourceID == next.ResourceID && current.AccountID == next.AccountID &&
		current.WorkspaceID == next.WorkspaceID && current.IdempotencyKey == next.IdempotencyKey && current.RequestHash == next.RequestHash
}

func validComputeClaimRecoveryBindingTransition(current, next FabricOperation) bool {
	currentBinding, currentPresent, currentValid := decodeComputeClaimRecoveryBinding(current)
	nextBinding, nextPresent, nextValid := decodeComputeClaimRecoveryBinding(next)
	if !nextPresent || !nextValid {
		return false
	}
	if currentPresent {
		return currentValid && currentBinding == nextBinding
	}
	if next.Status == "claim_pending" {
		return true
	}
	if next.Status != "failed" {
		return false
	}
	launchOperationID, ok := strings.CutSuffix(current.IdempotencyKey, ":compute")
	return ok && nextBinding.LaunchOperationID == launchOperationID && nextBinding.IdempotencyKey == current.IdempotencyKey && nextBinding.RequestHash == current.RequestHash
}

func validComputeClaimTerminalTransition(current, next FabricOperation) bool {
	if next.Status != "failed" {
		return true
	}
	if current.Status != "claim_pending" || next.FinishedAt.IsZero() {
		return false
	}
	evidence, present, valid := decodeComputeClaimTerminalEvidence(next)
	return present && valid && evidence.FabricRecordID == current.ID && evidence.OperationID == current.OperationID && evidence.IdempotencyKey == current.IdempotencyKey &&
		evidence.RequestHash == current.RequestHash && evidence.ComputeAllocationID == current.ResourceID && evidence.AccountID == current.AccountID && evidence.WorkspaceID == current.WorkspaceID &&
		next.ErrorCode == evidence.ErrorCode
}
