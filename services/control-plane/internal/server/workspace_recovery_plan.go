package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

const workspaceRecoveryPlanSchemaVersion = 1

type workspaceRecoveryReleaseBinding struct {
	MainSHA              string `json:"mainSha"`
	CloudImageDigest     string `json:"cloudImageDigest"`
	WorkspaceImageDigest string `json:"workspaceImageDigest"`
}

type workspaceRecoveryTargetBinding struct {
	LaunchOperationID    string `json:"launchOperationId"`
	AccountID            string `json:"accountId"`
	WorkspaceID          string `json:"workspaceId"`
	ComputeAllocationID  string `json:"computeAllocationId"`
	StorageID            string `json:"storageId"`
	Stage                string `json:"stage"`
	WorkspaceImageDigest string `json:"workspaceImageDigest"`
	AuthorityDigest      string `json:"authorityDigest"`
	PoolID               string `json:"poolId,omitempty"`
	NodePoolID           string `json:"nodePoolId,omitempty"`
	MachineName          string `json:"machineName,omitempty"`
	NodeName             string `json:"nodeName,omitempty"`
	CVMInstanceID        string `json:"cvmInstanceId,omitempty"`
	PrivateIPDigest      string `json:"privateIpDigest,omitempty"`
	InstanceType         string `json:"instanceType,omitempty"`
	Zone                 string `json:"zone,omitempty"`
	NodeOwnershipState   string `json:"nodeOwnershipState,omitempty"`
	CVMOwnershipState    string `json:"cvmOwnershipState,omitempty"`
	StorageState         string `json:"storageState,omitempty"`
	StorageProviderID    string `json:"storageProviderResourceId,omitempty"`
	WorkspaceAPIKeyID    int64  `json:"workspaceApiKeyId,omitempty"`
}

type workspaceRecoveryMutationCounts struct {
	Sub2API    int `json:"sub2api"`
	Tencent    int `json:"tencent"`
	Kubernetes int `json:"kubernetes"`
}

type workspaceRecoveryPlanFailureDTO struct {
	SchemaVersion           int                                          `json:"schemaVersion"`
	Status                  string                                       `json:"status"`
	RecoveryEligible        bool                                         `json:"recoveryEligible"`
	FailureStage            string                                       `json:"failureStage"`
	ReadbackError           string                                       `json:"readbackError"`
	ErrorCode               string                                       `json:"errorCode"`
	MutationCounts          workspaceRecoveryMutationCounts              `json:"mutationCounts"`
	ProviderIdentityFailure *clients.ComputeClaimProviderIdentityFailure `json:"providerIdentityFailure,omitempty"`
	ComputeClaimEvidence    *workspaceRecoveryComputeClaimEvidence       `json:"computeClaimEvidence,omitempty"`
}

type workspaceRecoveryPlanFailure struct {
	cause                   error
	failureStage            string
	readbackError           string
	errorCode               string
	providerIdentityFailure *clients.ComputeClaimProviderIdentityFailure
	computeClaimEvidence    *workspaceRecoveryComputeClaimEvidence
}

// workspaceComputeClaimTrace is a bounded, GET-only projection of the
// Control Plane recovery decision path. It intentionally does not persist a
// plan or CurrentDecision and cannot authorize a mutation.
func (app *controlPlaneServer) traceWorkspaceComputeClaim(ctx context.Context, service *controlplane.Service, accountID, operationID string) (map[string]any, error) {
	operation, found, err := app.workspaceLaunchOperation(ctx, operationID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errBillingReviewNotFound
	}
	if operation.AccountID != accountID {
		return nil, errBillingReviewIdentity
	}

	canonical := workspaceComputeClaimCanonical(operation)
	legacy := workspaceComputeClaimLegacyCandidate(operation)
	storageBoundary := workspaceComputeClaimStorageBoundaryCandidate(operation)
	candidate := workspaceComputeClaimRecoveryCandidate(operation)
	terminal := operation.ComputeClaimTerminalEvidence
	storageBudget := operation.ContinuationAttemptBudgets["storage"]
	recoveryStage, recoveryStagePresent := workspaceLaunchReadbackRecoveryStage(operation)
	allowExistingStorageOperation := workspaceComputeClaimStageAwareReadback(operation)
	var authoritativeDecision any
	if operation.CurrentDecision != nil {
		authoritativeDecision = operation.CurrentDecision
	}

	trace := map[string]any{
		"schemaVersion": 1,
		"operationMode": "compute_claim_trace",
		"status":        "evidence_only",
		"launch": map[string]any{
			"status":    operation.Status,
			"phase":     operation.Phase,
			"errorCode": operation.ErrorCode,
		},
		"candidate": map[string]any{
			"canonical":                 canonical,
			"legacy":                    legacy,
			"storageBoundary":           storageBoundary,
			"recoveryCandidate":         candidate,
			"terminalEvidenceBlocksOld": terminal != nil,
		},
		"terminalEvidence": workspaceComputeClaimTraceTerminalEvidence(terminal),
		"identity": map[string]any{
			"accountMatches":            operation.AccountID == accountID,
			"launchOperationIdMatches":  operation.ID == operationID,
			"workspaceIdPresent":        operation.WorkspaceID != "",
			"computeIdPresent":          operation.ComputeID != "",
			"storageIdPresent":          operation.StorageID != "",
			"nodePoolIdPresent":         operation.ComputeNodePoolID != "",
			"computeClaimIdentityValid": validWorkspaceLaunchComputeClaimIdentity(operation),
		},
		"storage": map[string]any{
			"continuationBudget":            workspaceComputeClaimTraceBudget(storageBudget),
			"recoveryStage":                 recoveryStage,
			"recoveryStagePresent":          recoveryStagePresent,
			"allowExistingStorageOperation": allowExistingStorageOperation,
		},
		"authoritativeDecision": authoritativeDecision,
		"mutationCounts": map[string]int{
			"sub2api": 0, "tencent": 0, "kubernetes": 0,
		},
	}

	firstFalsePredicate := ""
	expected, actual, authority, nextAction := "", "", "", ""
	readbackErrors := []string{}
	setFirst := func(predicate, want, got, source string) {
		if firstFalsePredicate == "" {
			firstFalsePredicate, expected, actual, authority = predicate, want, got, source
		}
	}

	userID, userErr := app.sub2APIUserID(ctx, operation.AccountID)
	chargeConfirmed := userErr == nil && workspaceLaunchChargeConfirmed(operation, userID)
	identity := trace["identity"].(map[string]any)
	identity["debitChargeConfirmed"] = chargeConfirmed
	identity["debitReadback"] = map[string]any{
		"state": func() string {
			if userErr != nil {
				return "unavailable"
			}
			if chargeConfirmed {
				return "confirmed"
			}
			return "unconfirmed"
		}(),
	}
	if userErr != nil {
		readbackErrors = append(readbackErrors, "control_plane_account_read")
		setFirst("controlPlane.debitIdentity", "confirmed", "unavailable", "control-plane")
	} else if !chargeConfirmed {
		setFirst("controlPlane.debitConfirmed", "confirmed", "unconfirmed", "control-plane")
	}
	if !candidate {
		setFirst("controlPlane.workspaceComputeClaimRecoveryCandidate", "true", "false", "control-plane")
	}

	loaded := false
	loadAttempted := candidate
	loadError := "none"
	traceOperation := operation
	input := workspaceComputeClaimRecoveryRequestForOperation(operation)
	var proof clients.ComputeClaimRecoveryProof
	var proofErr error
	var reducerDecision *CurrentDecision
	if candidate && userErr == nil && chargeConfirmed && validWorkspaceLaunchComputeClaimIdentity(operation) {
		loadedOperation, loadErr := app.loadWorkspaceComputeClaimOperation(ctx, operationID, input, true)
		if loadErr != nil {
			loadError = workspaceComputeClaimTraceErrorCode(loadErr)
			readbackErrors = append(readbackErrors, loadError)
			setFirst("controlPlane.loadWorkspaceComputeClaimOperation", "loaded", loadError, "control-plane")
		} else {
			loaded, traceOperation = true, loadedOperation
		}
	} else if candidate {
		loadError = "precondition_not_satisfied"
		setFirst("controlPlane.loadWorkspaceComputeClaimOperation", "preconditions", loadError, "control-plane")
	}
	trace["controlPlane"] = map[string]any{
		"loadAttempted": loadAttempted,
		"loaded":        loaded,
		"errorCode":     loadError,
	}

	if loaded {
		input = workspaceComputeClaimRecoveryRequestForOperation(traceOperation)
		proof, proofErr = collectWorkspaceComputeClaimEvidence(ctx, service, traceOperation, input)
		evaluation := evaluateWorkspaceComputeClaimProof(traceOperation, input, proof, false)
		trace["providerTruth"] = workspaceComputeClaimTraceProviderTruth(proof, proofErr)
		trace["proofEligibility"] = workspaceComputeClaimTraceProofEligibility(evaluation)
		reducer := currentDecisionForComputeClaimEvaluation(traceOperation, proofErr, evaluation)
		reducerDecision = &reducer
		trace["reducer"] = map[string]any{
			"called":    true,
			"persisted": operation.CurrentDecision != nil,
			"decision":  reducer,
		}
		if proofErr != nil {
			readbackErrors = append(readbackErrors, workspaceComputeClaimTraceErrorCode(proofErr))
		}
		if evaluation.FirstFalsePredicate != "" {
			eligibility := trace["proofEligibility"].(map[string]any)
			setFirst(
				stringValue(eligibility["firstFalsePredicate"]),
				stringValue(eligibility["expected"]),
				stringValue(eligibility["actual"]),
				stringValue(eligibility["authority"]),
			)
		} else if proofErr != nil {
			setFirst("provider.computeClaimEvidence", "available", workspaceComputeClaimTraceErrorCode(proofErr), "fabric.computeClaimProof")
		}
	} else {
		trace["providerTruth"] = map[string]any{
			"collectorCalled": false,
			"state":           "not_called",
		}
		trace["proofEligibility"] = map[string]any{
			"called":   false,
			"eligible": false,
		}
		trace["reducer"] = map[string]any{
			"called":    false,
			"persisted": operation.CurrentDecision != nil,
		}
	}

	if reducerDecision != nil {
		nextAction = reducerDecision.NextAction
		if firstFalsePredicate == "" {
			firstFalsePredicate, expected, actual, authority = reducerDecision.FirstFalsePredicate, reducerDecision.Expected, reducerDecision.Actual, reducerDecision.Authority
		}
		if firstFalsePredicate == "" && reducerDecision.CurrentStage == "succeeded" {
			nextAction = nextActionNone
		}
	}
	if firstFalsePredicate == "" {
		firstFalsePredicate, expected, actual, authority = "controlPlane.reducer", "decision", "not_proven", "control-plane"
	}
	if nextAction == "" {
		nextAction = "MANUAL_REVIEW"
	}
	trace["firstFalsePredicate"], trace["expected"], trace["actual"], trace["authority"], trace["nextAction"] = firstFalsePredicate, expected, actual, authority, nextAction
	trace["readbackErrors"] = uniqueWorkspaceComputeClaimTraceErrors(readbackErrors)
	return trace, nil
}

func workspaceComputeClaimTraceBudget(budget workspaceLaunchStageBudget) map[string]int {
	return map[string]int{"attempted": budget.Attempted, "confirmed": budget.Confirmed, "unknown": budget.Unknown, "max": budget.Max}
}

func workspaceComputeClaimTraceTerminalEvidence(evidence *clients.ComputeClaimTerminalEvidence) map[string]any {
	result := map[string]any{"present": evidence != nil}
	if evidence == nil {
		return result
	}
	result["status"] = evidence.Status
	result["stage"] = evidence.Stage
	result["errorCode"] = evidence.ErrorCode
	result["readbackStatus"] = evidence.ReadbackStatus
	result["nodeOwnershipState"] = evidence.NodeOwnershipState
	result["cvmOwnershipState"] = evidence.CVMOwnershipState
	result["attempted"] = evidence.Attempted
	result["confirmed"] = evidence.Confirmed
	result["unknown"] = evidence.Unknown
	return result
}

func workspaceComputeClaimTraceProviderTruth(proof clients.ComputeClaimRecoveryProof, proofErr error) map[string]any {
	result := map[string]any{
		"collectorCalled":         true,
		"state":                   "available",
		"computeState":            "unknown",
		"storageState":            proof.StorageState,
		"nodeOwnershipState":      proof.NodeOwnershipState,
		"cvmOwnershipState":       proof.CVMOwnershipState,
		"eligible":                proof.Eligible,
		"reason":                  proof.Reason,
		"failureStage":            proof.FailureStage,
		"providerErrorClass":      proof.ProviderErrorClass,
		"tencentMutationCount":    proof.TencentMutationCount,
		"kubernetesMutationCount": proof.KubernetesMutationCount,
	}
	if proofErr != nil {
		result["state"] = "unavailable"
		result["errorCode"] = workspaceComputeClaimTraceErrorCode(proofErr)
	}
	if proof.Eligible && proof.Reason == "none" {
		result["computeState"] = "ready"
	}
	return result
}

func workspaceComputeClaimTraceProofEligibility(evaluation workspaceComputeClaimProofEvaluation) map[string]any {
	result := map[string]any{
		"called":              true,
		"eligible":            evaluation.Eligible,
		"function":            "evaluateWorkspaceComputeClaimProof",
		"firstFalsePredicate": evaluation.FirstFalsePredicate,
		"expected":            evaluation.Expected,
		"actual":              evaluation.Actual,
		"authority":           evaluation.Authority,
		"condition":           evaluation.Condition,
	}
	return result
}

func workspaceComputeClaimTraceErrorCode(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, errBillingReviewNotFound):
		return "workspace_launch_not_found"
	case errors.Is(err, errBillingReviewIdentity), errors.Is(err, errWorkspaceComputeClaimIdentity):
		return "identity_mismatch"
	case errors.Is(err, errBillingReviewChargeFact):
		return "charge_unconfirmed"
	case errors.Is(err, errWorkspaceComputeClaimNotPending):
		return "compute_claim_not_pending"
	case errors.Is(err, errWorkspaceComputeClaimProof):
		return "compute_claim_proof_failed"
	default:
		return "readback_unavailable"
	}
}

func uniqueWorkspaceComputeClaimTraceErrors(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func (failure *workspaceRecoveryPlanFailure) Error() string { return failure.errorCode }
func (failure *workspaceRecoveryPlanFailure) Unwrap() error { return failure.cause }

func workspaceRecoveryPlanClassifiedFailure(cause error, failureStage, readbackError, errorCode string) error {
	return &workspaceRecoveryPlanFailure{
		cause: cause, failureStage: failureStage, readbackError: readbackError, errorCode: errorCode,
	}
}

func workspaceRecoveryPlanProofFailure(proof clients.ComputeClaimRecoveryProof, cause error) error {
	if cause == nil {
		cause = errWorkspaceComputeClaimProof
	} else {
		cause = errors.Join(errWorkspaceComputeClaimProof, cause)
	}
	failureStage := "fabric_proof"
	if proof.FailureStage != "" && safeWorkspaceComputeClaimFailureStage(proof.FailureStage) {
		failureStage = proof.FailureStage
	}
	readbackError := "workspace_compute_claim_proof_failed"
	if proof.ProviderErrorClass != "" && safeWorkspaceComputeClaimProviderErrorClass(proof.ProviderErrorClass) {
		readbackError = proof.ProviderErrorClass
	} else if proof.Reason != "" && proof.Reason != "none" && safeWorkspaceComputeClaimReason(proof.Reason) {
		readbackError = proof.Reason
	}
	failure := &workspaceRecoveryPlanFailure{
		cause: cause, failureStage: failureStage, readbackError: readbackError, errorCode: "workspace_recovery_plan_fabric_proof_failed",
	}
	if validWorkspaceComputeClaimProviderIdentityFailure(proof.ProviderIdentityFailure) {
		value := *proof.ProviderIdentityFailure
		failure.providerIdentityFailure = &value
	}
	failure.computeClaimEvidence = workspaceRecoveryComputeClaimEvidenceFromProof(proof)
	return failure
}

func workspaceRecoveryPlanFailureProjection(err error) workspaceRecoveryPlanFailureDTO {
	failureStage, readbackError, errorCode := "unknown", "unknown", "workspace_recovery_plan_unavailable"
	var classified *workspaceRecoveryPlanFailure
	switch {
	case errors.As(err, &classified):
		failureStage, readbackError, errorCode = classified.failureStage, classified.readbackError, classified.errorCode
	case errors.Is(err, errBillingReviewNotFound):
		failureStage, readbackError, errorCode = "operation_read", "workspace_launch_not_found", "workspace_recovery_plan_operation_read_failed"
	case errors.Is(err, errBillingReviewIdentity):
		failureStage, readbackError, errorCode = "account_identity", "billing_review_identity_mismatch", "workspace_recovery_plan_account_identity_mismatch"
	case errors.Is(err, errBillingReviewChargeFact):
		failureStage, readbackError, errorCode = "account_identity", "billing_review_charge_fact_unconfirmed", "workspace_recovery_plan_account_identity_mismatch"
	case errors.Is(err, errWorkspaceComputeClaimNotPending):
		failureStage, readbackError, errorCode = "control_plane_state", "workspace_compute_claim_not_pending", "workspace_recovery_plan_state_ineligible"
	case errors.Is(err, errWorkspaceComputeClaimIdentity):
		failureStage, readbackError, errorCode = "fabric_identity", "workspace_compute_claim_identity_mismatch", "workspace_recovery_plan_fabric_identity_invalid"
	case errors.Is(err, errWorkspaceComputeClaimProof), errors.Is(err, errBillingReviewProviderFact):
		failureStage, readbackError, errorCode = "fabric_proof", "workspace_compute_claim_proof_failed", "workspace_recovery_plan_fabric_proof_failed"
	case errors.Is(err, errWorkspaceLaunchCASConflict):
		failureStage, readbackError, errorCode = "state_persist", "workspace_launch_cas_conflict", "workspace_recovery_plan_state_persist_failed"
	}
	return workspaceRecoveryPlanFailureDTO{
		SchemaVersion: 1, Status: "blocked", RecoveryEligible: false,
		FailureStage: failureStage, ReadbackError: readbackError, ErrorCode: errorCode,
		MutationCounts: workspaceRecoveryMutationCounts{},
		ProviderIdentityFailure: func() *clients.ComputeClaimProviderIdentityFailure {
			if classified == nil || !validWorkspaceComputeClaimProviderIdentityFailure(classified.providerIdentityFailure) {
				return nil
			}
			value := *classified.providerIdentityFailure
			return &value
		}(),
		ComputeClaimEvidence: func() *workspaceRecoveryComputeClaimEvidence {
			if classified == nil {
				return nil
			}
			return classified.computeClaimEvidence
		}(),
	}
}

func validWorkspaceComputeClaimProviderIdentityFailure(value *clients.ComputeClaimProviderIdentityFailure) bool {
	if value == nil || value.ExpectedDigest == value.ActualDigest || !computeClaimApprovalDigestPattern.MatchString(value.ExpectedDigest) ||
		!computeClaimApprovalDigestPattern.MatchString(value.ActualDigest) {
		return false
	}
	switch value.Predicate {
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

type workspaceRecoveryPlanStage struct {
	Stage  string `json:"stage"`
	Status string `json:"status"`
}

type workspaceRecoveryPlanMismatch struct {
	Field          string `json:"field"`
	Expected       string `json:"expected,omitempty"`
	Actual         string `json:"actual,omitempty"`
	ExpectedDigest string `json:"expectedDigest,omitempty"`
	ActualDigest   string `json:"actualDigest,omitempty"`
}

type workspaceRecoveryPlan struct {
	SchemaVersion          int                                    `json:"schemaVersion"`
	Generation             int                                    `json:"generation,omitempty"`
	PredecessorPlanDigest  string                                 `json:"predecessorPlanDigest,omitempty"`
	PredecessorExecutionID string                                 `json:"predecessorExecutionId,omitempty"`
	PlanID                 string                                 `json:"planId"`
	PlanDigest             string                                 `json:"planDigest"`
	Status                 string                                 `json:"status"`
	Action                 string                                 `json:"action"`
	GeneratedAt            string                                 `json:"generatedAt"`
	ValidatedAt            string                                 `json:"validatedAt,omitempty"`
	ReleaseBinding         workspaceRecoveryReleaseBinding        `json:"releaseBinding"`
	TargetBinding          workspaceRecoveryTargetBinding         `json:"targetBinding"`
	Stages                 []workspaceRecoveryPlanStage           `json:"stages"`
	AllowedDecisions       []string                               `json:"allowedDecisions"`
	DecisionBinding        workspaceRecoveryDecisionBinding       `json:"decisionBinding"`
	IdentityEvidence       []clients.ComputeClaimIdentityCheck    `json:"identityEvidence"`
	MutationCounts         workspaceRecoveryMutationCounts        `json:"mutationCounts"`
	OperationID            string                                 `json:"operationId"`
	Mismatches             []workspaceRecoveryPlanMismatch        `json:"mismatches"`
	ExecutionID            string                                 `json:"executionId,omitempty"`
	RunID                  string                                 `json:"runId,omitempty"`
	URL                    string                                 `json:"url,omitempty"`
	ReceiptID              string                                 `json:"receiptId,omitempty"`
	ErrorCode              string                                 `json:"errorCode,omitempty"`
	SuccessorGate          *workspaceRecoverySuccessorGateDTO     `json:"-"`
	ComputeClaimEvidence   *workspaceRecoveryComputeClaimEvidence `json:"-"`
}

type workspaceRecoveryDecisionBinding struct {
	DecisionDigest  string                          `json:"decisionDigest"`
	EvidenceDigest  string                          `json:"evidenceDigest"`
	DecisionVersion int64                           `json:"decisionVersion"`
	CurrentStage    string                          `json:"currentStage"`
	StageAttemptID  string                          `json:"stageAttemptId"`
	AllowedMutation string                          `json:"allowedMutation"`
	MutationBudget  workspaceRecoveryMutationCounts `json:"mutationBudget"`
}

type workspaceRecoveryMutationOutcome struct {
	Status                   string                                 `json:"status"`
	Counts                   workspaceRecoveryMutationCounts        `json:"counts"`
	FabricOperationMutations int                                    `json:"fabricOperationMutations"`
	Source                   string                                 `json:"source,omitempty"`
	EvidenceDigest           string                                 `json:"evidenceDigest,omitempty"`
	ComputeClaimEvidence     *workspaceRecoveryComputeClaimEvidence `json:"computeClaimEvidence,omitempty"`
}

type workspaceRecoveryComputeClaimReconciliationEvidence struct {
	SchemaVersion      int                                           `json:"schemaVersion"`
	Consumer           string                                        `json:"consumer"`
	Generation         string                                        `json:"generation"`
	ProvenanceSource   string                                        `json:"provenanceSource,omitempty"`
	ProvenanceDigest   string                                        `json:"provenanceDigest,omitempty"`
	State              string                                        `json:"state"`
	FailureStage       string                                        `json:"failureStage,omitempty"`
	ProviderErrorClass string                                        `json:"providerErrorClass,omitempty"`
	Node               workspaceRecoveryComputeClaimMutationEvidence `json:"node"`
}

type workspaceRecoveryComputeClaimMutationEvidence struct {
	Attempted int      `json:"attempted"`
	Confirmed int      `json:"confirmed"`
	Unknown   int      `json:"unknown"`
	Missing   []string `json:"missing"`
}

type workspaceRecoveryComputeClaimEvidence struct {
	SchemaVersion            int                                                  `json:"schemaVersion"`
	BindingClassification    string                                               `json:"bindingClassification"`
	MismatchField            string                                               `json:"mismatchField,omitempty"`
	ExpectedDigest           string                                               `json:"expectedDigest,omitempty"`
	ActualDigest             string                                               `json:"actualDigest,omitempty"`
	MutationLedger           string                                               `json:"mutationLedger"`
	MutationLedgerOutcome    string                                               `json:"mutationLedgerOutcome"`
	CVM                      workspaceRecoveryComputeClaimMutationEvidence        `json:"cvm"`
	Node                     workspaceRecoveryComputeClaimMutationEvidence        `json:"node"`
	LedgerFailureStage       string                                               `json:"ledgerFailureStage"`
	LedgerProviderErrorClass string                                               `json:"ledgerProviderErrorClass"`
	FailureStage             string                                               `json:"failureStage"`
	ProviderErrorClass       string                                               `json:"providerErrorClass"`
	Reconciliation           *workspaceRecoveryComputeClaimReconciliationEvidence `json:"reconciliation,omitempty"`
}

func workspaceRecoveryComputeClaimMutationEvidenceProjection(value clients.ComputeClaimMutationEvidence) workspaceRecoveryComputeClaimMutationEvidence {
	return workspaceRecoveryComputeClaimMutationEvidence{
		Attempted: value.Attempted, Confirmed: value.Confirmed, Unknown: value.Unknown,
		Missing: append([]string{}, value.Missing...),
	}
}

type workspaceRecoveryPlanDTO struct {
	PlanID               string                                 `json:"planId"`
	PlanDigest           string                                 `json:"planDigest"`
	Status               string                                 `json:"status"`
	OperationID          string                                 `json:"operationId,omitempty"`
	Stages               []workspaceRecoveryPlanStage           `json:"stages"`
	Mismatches           []workspaceRecoveryPlanMismatch        `json:"mismatches"`
	MutationCounts       workspaceRecoveryMutationCounts        `json:"mutationCounts"`
	ExecutionID          string                                 `json:"executionId,omitempty"`
	RunID                string                                 `json:"runId,omitempty"`
	URL                  string                                 `json:"url,omitempty"`
	ReceiptID            string                                 `json:"receiptId,omitempty"`
	ErrorCode            string                                 `json:"errorCode,omitempty"`
	SuccessorGate        *workspaceRecoverySuccessorGateDTO     `json:"successorGate,omitempty"`
	ComputeClaimEvidence *workspaceRecoveryComputeClaimEvidence `json:"computeClaimEvidence,omitempty"`
}

type workspaceRecoverySuccessorGateDTO struct {
	Applicable             bool   `json:"applicable"`
	Allowed                bool   `json:"allowed"`
	PlanState              string `json:"planState"`
	ExecutionState         string `json:"executionState"`
	CompletionState        string `json:"completionState"`
	LeaseState             string `json:"leaseState"`
	IdentityState          string `json:"identityState"`
	PersistedMutationState string `json:"persistedMutationState"`
	FabricLedgerState      string `json:"fabricLedgerState"`
}

func workspaceRecoveryPlanHTTPProjection(plan workspaceRecoveryPlan) workspaceRecoveryPlanDTO {
	mismatches := make([]workspaceRecoveryPlanMismatch, 0, len(plan.Mismatches))
	for _, mismatch := range plan.Mismatches {
		switch mismatch.Field {
		case "release.mainSha", "controlPlane.stage", "provider.nodeOwnership", "provider.cvmOwnership", "provider.storageState":
		default:
			if mismatch.ExpectedDigest == "" && mismatch.Expected != "" {
				check := workspaceComputeClaimIdentityDigestCheck(mismatch.Field, mismatch.Expected, mismatch.Actual)
				mismatch.ExpectedDigest, mismatch.ActualDigest = check.ExpectedDigest, check.ActualDigest
			}
			mismatch.Expected, mismatch.Actual = "", ""
		}
		mismatches = append(mismatches, mismatch)
	}
	return workspaceRecoveryPlanDTO{
		PlanID: plan.PlanID, PlanDigest: plan.PlanDigest, Status: plan.Status, OperationID: plan.OperationID,
		Stages: append([]workspaceRecoveryPlanStage(nil), plan.Stages...), Mismatches: mismatches, MutationCounts: plan.MutationCounts,
		ExecutionID: plan.ExecutionID, RunID: plan.RunID, URL: plan.URL, ReceiptID: plan.ReceiptID, ErrorCode: plan.ErrorCode,
		SuccessorGate: plan.SuccessorGate, ComputeClaimEvidence: plan.ComputeClaimEvidence,
	}
}

type workspaceRecoveryExecution struct {
	ExecutionID         string                                   `json:"executionId"`
	RunIdentity         string                                   `json:"runIdentity"`
	PlanID              string                                   `json:"planId"`
	PlanDigest          string                                   `json:"planDigest"`
	ApprovalDigest      string                                   `json:"approvalDigest"`
	Decision            string                                   `json:"decision"`
	Reviewer            string                                   `json:"reviewer"`
	Status              string                                   `json:"status"`
	LeaseToken          string                                   `json:"leaseToken,omitempty"`
	LeaseExpiresAt      string                                   `json:"leaseExpiresAt,omitempty"`
	StartedAt           string                                   `json:"startedAt"`
	CompletedAt         string                                   `json:"completedAt,omitempty"`
	ErrorCode           string                                   `json:"errorCode,omitempty"`
	MutationOutcome     workspaceRecoveryMutationOutcome         `json:"mutationOutcome"`
	Approval            *workspaceLaunchReadbackRecoveryApproval `json:"approval,omitempty"`
	ComputeClaimRequest *workspaceComputeClaimRecoveryRequest    `json:"computeClaimRequest,omitempty"`
}

type workspaceRecoveryPlanHistoryEntry struct {
	Plan       workspaceRecoveryPlan      `json:"plan"`
	Execution  workspaceRecoveryExecution `json:"execution"`
	ArchivedAt string                     `json:"archivedAt"`
}

func deployedImageDigest(value string) string {
	value = strings.TrimSpace(value)
	_, digest, ok := strings.Cut(value, "@")
	if !ok || !computeClaimCloudDigestPattern.MatchString(digest) {
		return ""
	}
	return digest
}

func currentWorkspaceRecoveryReleaseBinding() (workspaceRecoveryReleaseBinding, error) {
	binding := workspaceRecoveryReleaseBinding{
		MainSHA:              strings.TrimSpace(os.Getenv("OPL_RELEASE_SHA")),
		CloudImageDigest:     deployedImageDigest(os.Getenv("OPL_CLOUD_IMAGE")),
		WorkspaceImageDigest: deployedImageDigest(os.Getenv("OPL_WORKSPACE_IMAGE")),
	}
	if !computeClaimMergedSHAPattern.MatchString(binding.MainSHA) || !computeClaimCloudDigestPattern.MatchString(binding.CloudImageDigest) ||
		!computeClaimCloudDigestPattern.MatchString(binding.WorkspaceImageDigest) {
		return workspaceRecoveryReleaseBinding{}, errWorkspaceComputeClaimIdentity
	}
	return binding, nil
}

func workspaceRecoveryAuthorityDigest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func workspaceRecoveryPlanDigest(plan workspaceRecoveryPlan) string {
	material := struct {
		SchemaVersion          int                              `json:"schemaVersion"`
		Generation             int                              `json:"generation,omitempty"`
		PredecessorPlanDigest  string                           `json:"predecessorPlanDigest,omitempty"`
		PredecessorExecutionID string                           `json:"predecessorExecutionId,omitempty"`
		Action                 string                           `json:"action"`
		ReleaseBinding         workspaceRecoveryReleaseBinding  `json:"releaseBinding"`
		TargetBinding          workspaceRecoveryTargetBinding   `json:"targetBinding"`
		Stages                 []workspaceRecoveryPlanStage     `json:"stages"`
		AllowedDecisions       []string                         `json:"allowedDecisions"`
		DecisionBinding        workspaceRecoveryDecisionBinding `json:"decisionBinding"`
		MutationCounts         workspaceRecoveryMutationCounts  `json:"mutationCounts"`
	}{
		SchemaVersion: plan.SchemaVersion, Generation: plan.Generation, PredecessorPlanDigest: plan.PredecessorPlanDigest,
		PredecessorExecutionID: plan.PredecessorExecutionID, Action: plan.Action, ReleaseBinding: plan.ReleaseBinding,
		TargetBinding: plan.TargetBinding, Stages: plan.Stages, AllowedDecisions: plan.AllowedDecisions,
		DecisionBinding: plan.DecisionBinding, MutationCounts: plan.MutationCounts,
	}
	return workspaceRecoveryAuthorityDigest(material)
}

func workspaceRecoveryPlanStages(operation workspaceLaunchOperation) []workspaceRecoveryPlanStage {
	stages := make([]workspaceRecoveryPlanStage, 0, len(workspaceLaunchContinuationStages))
	for _, name := range workspaceLaunchContinuationStages {
		budget := operation.ContinuationAttemptBudgets[name]
		status := "pending"
		switch {
		case budget.Unknown > 0:
			status = "manual_review"
		case budget.Confirmed == budget.Max:
			status = "completed"
		}
		stages = append(stages, workspaceRecoveryPlanStage{Stage: name, Status: status})
	}
	return stages
}

func workspaceRecoveryPlanIdentityEvidence(operation workspaceLaunchOperation, proof workspaceLaunchReadbackRecoveryProof, release workspaceRecoveryReleaseBinding, expectedPrivateIPs ...string) []clients.ComputeClaimIdentityCheck {
	expectedPrivateIP := operation.ComputePrivateIP
	if len(expectedPrivateIPs) != 0 {
		expectedPrivateIP = expectedPrivateIPs[0]
	}
	return []clients.ComputeClaimIdentityCheck{
		workspaceComputeClaimIdentityCheck("controlPlane.launchOperationId", operation.ID, proof.Target.LaunchOperationID),
		workspaceComputeClaimIdentityCheck("controlPlane.accountId", operation.AccountID, proof.Target.AccountID),
		workspaceComputeClaimIdentityCheck("controlPlane.workspaceId", operation.WorkspaceID, proof.Target.WorkspaceID),
		workspaceComputeClaimIdentityCheck("controlPlane.computeAllocationId", operation.ComputeID, proof.Target.ComputeAllocationID),
		workspaceComputeClaimIdentityCheck("controlPlane.storageId", operation.StorageID, proof.Target.StorageID),
		workspaceComputeClaimIdentityDigestCheck("release.workspaceImageDigest", deployedImageDigest(operation.WorkspaceImageDigest), release.WorkspaceImageDigest),
		workspaceComputeClaimIdentityDigestCheck("target.privateIp", expectedPrivateIP, proof.Target.PrivateIP),
	}
}

func newWorkspaceReadbackRecoveryPlan(operation workspaceLaunchOperation, proof workspaceLaunchReadbackRecoveryProof, release workspaceRecoveryReleaseBinding, expectedPrivateIP string) (workspaceRecoveryPlan, error) {
	authorityDigest := workspaceRecoveryAuthorityDigest(proof)
	if authorityDigest == "" || !proof.Eligible || proof.Reason != "none" || proof.Sub2APIMutationCount != 0 || proof.TencentMutationCount != 0 || proof.KubernetesMutationCount != 0 {
		return workspaceRecoveryPlan{}, errBillingReviewProviderFact
	}
	plan := workspaceRecoveryPlan{
		SchemaVersion: workspaceRecoveryPlanSchemaVersion, Status: "diagnosed", Action: "unknown_stage_continue",
		GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), ReleaseBinding: release,
		OperationID: operation.ID, Mismatches: []workspaceRecoveryPlanMismatch{},
		TargetBinding: workspaceRecoveryTargetBinding{
			LaunchOperationID: operation.ID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID,
			ComputeAllocationID: operation.ComputeID, StorageID: operation.StorageID, Stage: proof.Stage,
			WorkspaceImageDigest: operation.WorkspaceImageDigest, AuthorityDigest: authorityDigest,
		},
		Stages: workspaceRecoveryPlanStages(operation), AllowedDecisions: []string{"continue", "escalate"},
		IdentityEvidence: workspaceRecoveryPlanIdentityEvidence(operation, proof, release, expectedPrivateIP),
		MutationCounts:   workspaceRecoveryMutationCounts{},
	}
	for _, check := range plan.IdentityEvidence {
		if !check.Matches {
			plan.Status = "blocked"
			plan.Mismatches = append(plan.Mismatches, workspaceRecoveryPlanMismatchFromCheck(check))
		}
	}
	plan.PlanDigest = workspaceRecoveryPlanDigest(plan)
	plan.PlanID = "recovery-plan-" + plan.PlanDigest[:20]
	return plan, nil
}

func (app *controlPlaneServer) workspaceRecoveryPlanExpectedPrivateIP(operation workspaceLaunchOperation) (string, error) {
	compute, ok := app.getCompute(operation.ComputeID)
	privateIP := strings.TrimSpace(stringValue(compute["privateIp"]))
	if !ok || !workspaceLaunchResourceIdentityMatches("compute", compute, operation) || privateIP == "" {
		return "", errBillingReviewIdentity
	}
	return privateIP, nil
}

func workspaceComputeClaimRecoveryRequestForOperation(operation workspaceLaunchOperation) workspaceComputeClaimRecoveryRequest {
	input := workspaceComputeClaimRecoveryRequest{
		LaunchOperationID: operation.ID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID,
		ComputeID: operation.ComputeID, StorageID: operation.StorageID, PackageID: operation.PackageID,
		PoolID: operation.ComputePoolID, NodePoolID: operation.ComputeNodePoolID, MachineName: operation.ComputeMachineName,
		NodeName: operation.ComputeNodeName, CVMInstanceID: operation.ComputeCVMInstanceID, PrivateIP: operation.ComputePrivateIP,
		InstanceType: operation.ComputeInstanceType, Zone: operation.ComputeZone,
	}
	if approval := operation.ComputeClaimApproval; approval != nil {
		input.MergedMainSHA, input.CloudImageDigest, input.ApprovalID, input.ApprovalDigest = approval.MergedMainSHA, approval.CloudImageDigest, approval.ApprovalID, approval.ApprovalDigest
		input.ExpiresAt, input.Confirmation, input.WorkspaceImageDigest = approval.ExpiresAt, approval.Confirmation, approval.WorkspaceImageDigest
		input.CustomerEmail, input.RecoveryKey = approval.Customer.Email, approval.RecoveryKey
		input.Resources, input.AttemptLimits = approval.Resources, approval.AttemptLimits
		input.AllowedWrites, input.ForbiddenWrites = append([]string(nil), approval.AllowedWrites...), append([]string(nil), approval.ForbiddenWrites...)
	}
	return input
}

func (app *controlPlaneServer) workspaceComputeClaimRecoveryProofForPlan(ctx context.Context, service *controlplane.Service, operation workspaceLaunchOperation) (workspaceComputeClaimRecoveryRequest, clients.ComputeClaimRecoveryProof, *clients.ComputeClaimIdentityEvidence, error) {
	if !workspaceComputeClaimRecoveryCandidate(operation) {
		return workspaceComputeClaimRecoveryRequest{}, clients.ComputeClaimRecoveryProof{}, nil, errWorkspaceComputeClaimNotPending
	}
	userID, err := app.sub2APIUserID(ctx, operation.AccountID)
	validIdentity := validWorkspaceLaunchComputeClaimIdentity(operation)
	if workspaceComputeClaimLegacyCandidate(operation) {
		validIdentity = validWorkspaceLaunchLegacyComputeClaimIdentity(operation)
	}
	storageBoundaryHydration := workspaceComputeClaimStorageBoundaryCandidate(operation) && !validWorkspaceLaunchComputeClaimIdentity(operation)
	if err != nil || !workspaceLaunchChargeConfirmed(operation, userID) || !validIdentity && !storageBoundaryHydration {
		return workspaceComputeClaimRecoveryRequest{}, clients.ComputeClaimRecoveryProof{}, nil, errWorkspaceComputeClaimIdentity
	}
	input := workspaceComputeClaimRecoveryRequestForOperation(operation)
	if workspaceComputeClaimLegacyCandidate(operation) || !validWorkspaceLaunchComputeClaimIdentity(operation) {
		allocation, readErr := service.ReadMonthlyCompute(ctx, operation.ComputeID)
		if readErr != nil || !hydrateWorkspaceComputeClaimIdentityFromAllocation(&operation, allocation) {
			return workspaceComputeClaimRecoveryRequest{}, clients.ComputeClaimRecoveryProof{}, nil, errWorkspaceComputeClaimIdentity
		}
		input = workspaceComputeClaimRecoveryRequestForOperation(operation)
	}
	proof, proofErr := collectWorkspaceComputeClaimEvidence(ctx, service, operation, input)
	if proof.SchemaVersion != 1 || proof.TencentMutationCount != 0 || proof.KubernetesMutationCount != 0 || proof.Sub2APIMutationCount != 0 {
		return input, proof, nil, workspaceRecoveryPlanProofFailure(proof, proofErr)
	}
	if proofErr != nil {
		evidence, evidenceErr := service.ComputeClaimRecoveryIdentityEvidence(ctx, clients.ComputeClaimRecoveryClaimInput{
			ComputeClaimRecoveryInput: workspaceComputeClaimRecoveryInput(operation, input), MachineName: input.MachineName,
			NodeName: input.NodeName, CVMInstanceID: input.CVMInstanceID, PrivateIP: input.PrivateIP,
			InstanceType: input.InstanceType, Zone: input.Zone,
		})
		if evidenceErr == nil && evidence != nil {
			proof.IdentityEvidence = evidence
		}
		return input, proof, evidence, workspaceRecoveryPlanProofFailure(proof, proofErr)
	}
	evidence, evidenceErr := service.ComputeClaimRecoveryIdentityEvidence(ctx, clients.ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: workspaceComputeClaimRecoveryInput(operation, input), MachineName: proof.MachineName,
		NodeName: proof.NodeName, CVMInstanceID: proof.CVMInstanceID, PrivateIP: proof.PrivateIP,
		InstanceType: proof.InstanceType, Zone: proof.Zone,
	})
	if evidenceErr != nil || evidence == nil {
		return input, proof, nil, workspaceRecoveryPlanClassifiedFailure(
			errors.Join(errWorkspaceComputeClaimIdentity, evidenceErr), "fabric_identity", "fabric_identity_evidence_unavailable", "workspace_recovery_plan_fabric_identity_invalid",
		)
	}
	return input, proof, evidence, nil
}

func workspaceComputeClaimPlanIdentityEvidence(operation workspaceLaunchOperation, input workspaceComputeClaimRecoveryRequest, proof clients.ComputeClaimRecoveryProof, evidence *clients.ComputeClaimIdentityEvidence) []clients.ComputeClaimIdentityCheck {
	checks := []clients.ComputeClaimIdentityCheck{
		workspaceComputeClaimIdentityCheck("controlPlane.launchOperationId", operation.ID, proof.LaunchOperationID),
		workspaceComputeClaimIdentityCheck("controlPlane.accountId", operation.AccountID, proof.AccountID),
		workspaceComputeClaimIdentityCheck("controlPlane.workspaceId", operation.WorkspaceID, proof.WorkspaceID),
		workspaceComputeClaimIdentityCheck("controlPlane.computeAllocationId", operation.ComputeID, proof.ComputeAllocationID),
		workspaceComputeClaimIdentityCheck("controlPlane.storageId", operation.StorageID, proof.StorageVolumeID),
		workspaceComputeClaimIdentityCheck("fabric.poolId", input.PoolID, proof.PoolID),
		workspaceComputeClaimIdentityCheck("fabric.nodePoolId", operation.ComputeNodePoolID, proof.NodePoolID),
		workspaceComputeClaimIdentityCheck("provider.machineName", input.MachineName, proof.MachineName),
		workspaceComputeClaimIdentityCheck("kubernetes.nodeName", input.NodeName, proof.NodeName),
		workspaceComputeClaimIdentityCheck("tencent.cvmInstanceId", input.CVMInstanceID, proof.CVMInstanceID),
		workspaceComputeClaimIdentityDigestCheck("tencent.privateIp", input.PrivateIP, proof.PrivateIP),
		workspaceComputeClaimIdentityCheck("tencent.instanceType", input.InstanceType, proof.InstanceType),
		workspaceComputeClaimIdentityCheck("tencent.zone", input.Zone, proof.Zone),
		workspaceComputeClaimIdentityAllowedCheck("provider.nodeOwnership", "unallocated_or_target_owned", proof.NodeOwnershipState, "unallocated", "target_owned"),
		workspaceComputeClaimIdentityAllowedCheck("provider.cvmOwnership", "recoverable_or_target_owned", proof.CVMOwnershipState, "recoverable", "target_owned"),
	}
	checks = append(checks, evidence.Checks...)
	if workspaceComputeClaimRequestHashReconciliation(evidence) {
		checks = append(checks, workspaceComputeClaimIdentityCheck(
			"fabric.bindingReconciliationCandidate", "request_hash_only_candidate", "request_hash_only_candidate",
		))
	} else {
		bindingAuthority := (evidence.BindingClassification == "current" || evidence.BindingClassification == "compute-claim") &&
			computeClaimApprovalDigestPattern.MatchString(evidence.BindingDigest)
		checks = append(checks, workspaceComputeClaimIdentityCheck(
			"fabric.bindingRecoveryAuthority", "current_or_compute_claim",
			map[bool]string{true: "current_or_compute_claim", false: "classification_only"}[bindingAuthority],
		))
	}
	return checks
}

func workspaceComputeClaimRequestHashReconciliation(evidence *clients.ComputeClaimIdentityEvidence) bool {
	if evidence == nil || evidence.BindingClassification != "request-hash-reconciliation" ||
		!computeClaimApprovalDigestPattern.MatchString(evidence.BindingDigest) ||
		!computeClaimApprovalDigestPattern.MatchString(evidence.MutationLedgerDigest) {
		return false
	}
	expectedFields := []string{
		"fabric.operationId", "fabric.operationIdempotencyKey", "fabric.operationRequestHash",
		"binding.present", "binding.valid", "binding.compatibility", "binding.launchOperationId",
		"binding.idempotencyKey", "binding.targetHash", "binding.requestHash",
	}
	if len(evidence.Checks) != len(expectedFields) {
		return false
	}
	for index, field := range expectedFields {
		check := evidence.Checks[index]
		if check.Field != field || field != "binding.requestHash" && !check.Matches ||
			field == "binding.requestHash" && (check.Matches || !computeClaimApprovalDigestPattern.MatchString(check.ExpectedDigest) ||
				!computeClaimApprovalDigestPattern.MatchString(check.ActualDigest) || check.ExpectedDigest == check.ActualDigest) {
			return false
		}
	}
	if evidence.MutationLedger == "absent" {
		absentDigest := sha256.Sum256([]byte("absent"))
		return evidence.MutationLedgerOutcome == "confirmed_zero" &&
			evidence.MutationLedgerDigest == hex.EncodeToString(absentDigest[:]) && evidence.MutationEvidence == nil &&
			evidence.FailureStage == "" && evidence.ProviderErrorClass == ""
	}
	if evidence.MutationLedger != "observed" || evidence.MutationLedgerOutcome != "unknown" ||
		evidence.MutationEvidence == nil || evidence.FailureStage != "cvm_tag_readback" || evidence.ProviderErrorClass != "provider_error" {
		return false
	}
	cvm, node := evidence.MutationEvidence.CVM, evidence.MutationEvidence.Node
	return cvm.Attempted == 1 && cvm.Confirmed == 0 && cvm.Unknown == 1 && len(cvm.Missing) == 1 && cvm.Missing[0] == "opl_account_id" &&
		node.Attempted == 0 && node.Confirmed == 0 && node.Unknown == 0 && len(node.Missing) == 0
}

func workspaceComputeClaimRecoverableCVMOnly(evidence *clients.ComputeClaimIdentityEvidence) bool {
	if evidence == nil || evidence.MutationEvidence == nil || evidence.MutationLedger != "observed" || evidence.MutationLedgerOutcome != "nonzero" ||
		!computeClaimApprovalDigestPattern.MatchString(evidence.MutationLedgerDigest) ||
		!computeClaimApprovalDigestPattern.MatchString(evidence.BindingDigest) ||
		(evidence.BindingClassification != "current" && evidence.BindingClassification != "compute-claim") ||
		!safeWorkspaceComputeClaimFailureStage(evidence.FailureStage) || !safeWorkspaceComputeClaimProviderErrorClass(evidence.ProviderErrorClass) {
		return false
	}
	expectedFields := []string{
		"fabric.operationId", "fabric.operationIdempotencyKey", "fabric.operationRequestHash",
		"binding.present", "binding.valid", "binding.compatibility", "binding.launchOperationId",
		"binding.idempotencyKey", "binding.targetHash", "binding.requestHash",
	}
	if len(evidence.Checks) != len(expectedFields) {
		return false
	}
	for index, field := range expectedFields {
		if evidence.Checks[index].Field != field || !evidence.Checks[index].Matches {
			return false
		}
	}
	cvm, node := evidence.MutationEvidence.CVM, evidence.MutationEvidence.Node
	return cvm.Attempted > 0 && workspaceComputeClaimMutationEvidenceMatches(cvm, cvm.Attempted, 5, "cvm", true) &&
		node.Attempted == 0 && workspaceComputeClaimMutationEvidenceMatches(node, 0, 1, "node", true)
}

func workspaceComputeClaimLegacyKubectlClientRejected(evidence *clients.ComputeClaimIdentityEvidence) bool {
	if !workspaceComputeClaimRequestHashReconciliation(evidence) || evidence.Reconciliation == nil {
		return false
	}
	mismatch, ok := workspaceRecoveryComputeClaimRequestHashMismatch(evidence)
	if !ok {
		return false
	}
	reconciliation, node := evidence.Reconciliation, evidence.Reconciliation.Node
	return reconciliation.SchemaVersion == 2 && reconciliation.Consumer == "claim_compute_recovery" &&
		reconciliation.Generation == "normal_launch_terminal_evidence_v1" &&
		reconciliation.ProvenanceSource == "normal_launch_terminal_evidence" &&
		computeClaimApprovalDigestPattern.MatchString(reconciliation.ProvenanceDigest) && reconciliation.State == "observed" &&
		reconciliation.ExpectedRequestHashDigest == mismatch.ExpectedDigest && reconciliation.PersistedRequestHashDigest == mismatch.ActualDigest &&
		reconciliation.FailureStage == "node_patch_readback" && reconciliation.ProviderErrorClass == "provider_error" &&
		node.Attempted == 1 && node.Confirmed == 0 && node.Unknown == 0 && len(node.Missing) == 1 && node.Missing[0] == "node_ownership"
}

func newWorkspaceComputeClaimRecoveryPlan(operation workspaceLaunchOperation, input workspaceComputeClaimRecoveryRequest, proof clients.ComputeClaimRecoveryProof, evaluation workspaceComputeClaimProofEvaluation, evidence *clients.ComputeClaimIdentityEvidence, release workspaceRecoveryReleaseBinding) (workspaceRecoveryPlan, error) {
	authorityDigest := workspaceRecoveryAuthorityDigest(struct {
		Proof    clients.ComputeClaimRecoveryProof     `json:"proof"`
		Evidence *clients.ComputeClaimIdentityEvidence `json:"evidence"`
	}{Proof: proof, Evidence: evidence})
	if authorityDigest == "" || proof.Sub2APIMutationCount != 0 || proof.TencentMutationCount != 0 || proof.KubernetesMutationCount != 0 {
		return workspaceRecoveryPlan{}, workspaceRecoveryPlanProofFailure(proof, nil)
	}
	privateIPDigest := workspaceRecoveryAuthorityDigest(proof.PrivateIP)
	decision := currentDecisionForComputeClaimEvaluation(operation, nil, evaluation)
	mutationBudget := workspaceRecoveryMutationCounts{}
	nodeMutationAuthorized := evaluation.Eligible && proof.NodeOwnershipState == "unallocated" && AuthorizeStageMutation(decision, "node_only_continuation")
	if nodeMutationAuthorized {
		mutationBudget.Kubernetes = 1
	}
	decisionBinding := workspaceRecoveryDecisionBinding{
		DecisionDigest: workspaceRecoveryAuthorityDigest(decision), EvidenceDigest: decision.EvidenceDigest,
		DecisionVersion: decision.DecisionVersion, CurrentStage: decision.CurrentStage,
		StageAttemptID: decision.StageAttemptID, AllowedMutation: decision.AllowedMutation, MutationBudget: mutationBudget,
	}
	if decisionBinding.DecisionDigest == "" || decisionBinding.EvidenceDigest == "" {
		return workspaceRecoveryPlan{}, errBillingReviewIdentity
	}
	plan := workspaceRecoveryPlan{
		SchemaVersion: workspaceRecoveryPlanSchemaVersion, Status: "diagnosed", Action: "compute_claim_continue",
		GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), ReleaseBinding: release,
		OperationID: operation.ID, Mismatches: []workspaceRecoveryPlanMismatch{},
		TargetBinding: workspaceRecoveryTargetBinding{
			LaunchOperationID: operation.ID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID,
			ComputeAllocationID: operation.ComputeID, StorageID: operation.StorageID, Stage: "compute_claim",
			WorkspaceImageDigest: operation.WorkspaceImageDigest, AuthorityDigest: authorityDigest,
			PoolID: proof.PoolID, NodePoolID: proof.NodePoolID, MachineName: proof.MachineName, NodeName: proof.NodeName,
			CVMInstanceID: proof.CVMInstanceID, PrivateIPDigest: privateIPDigest, InstanceType: proof.InstanceType, Zone: proof.Zone,
			NodeOwnershipState: proof.NodeOwnershipState, CVMOwnershipState: proof.CVMOwnershipState,
			StorageState: proof.StorageState, StorageProviderID: proof.StorageProviderResourceID, WorkspaceAPIKeyID: operation.WorkspaceAPIKeyID,
		},
		Stages:           append([]workspaceRecoveryPlanStage{{Stage: "compute_claim", Status: "manual_review"}}, workspaceRecoveryPlanStages(operation)...),
		AllowedDecisions: []string{"continue", "escalate"}, DecisionBinding: decisionBinding, MutationCounts: workspaceRecoveryMutationCounts{},
	}
	if !evaluation.Eligible {
		plan.Status = "blocked"
		plan.Mismatches = append(plan.Mismatches, workspaceRecoveryPlanMismatch{
			Field: evaluation.FirstFalsePredicate, Expected: evaluation.Expected, Actual: evaluation.Actual,
		})
	} else if proof.NodeOwnershipState == "unallocated" && !nodeMutationAuthorized {
		plan.Status = "blocked"
		plan.Mismatches = append(plan.Mismatches, workspaceRecoveryPlanMismatch{
			Field: "decision.allowedMutation", Expected: "node_only_continuation", Actual: decision.AllowedMutation,
		})
	}
	plan.IdentityEvidence = workspaceComputeClaimPlanIdentityEvidence(operation, input, proof, evidence)
	requestHashReconciliation := workspaceComputeClaimRequestHashReconciliation(evidence)
	for _, check := range plan.IdentityEvidence {
		if requestHashReconciliation && check.Field == "binding.requestHash" {
			continue
		}
		if !check.Matches {
			plan.Status = "blocked"
			plan.Mismatches = append(plan.Mismatches, workspaceRecoveryPlanMismatchFromCheck(check))
		}
	}
	plan.PlanDigest = workspaceRecoveryPlanDigest(plan)
	plan.PlanID = "recovery-plan-" + plan.PlanDigest[:20]
	return plan, nil
}

func workspaceRecoveryPlanProjection(operation workspaceLaunchOperation) workspaceRecoveryPlan {
	if operation.RecoveryPlan == nil {
		return workspaceRecoveryPlan{}
	}
	plan := *operation.RecoveryPlan
	plan.OperationID = operation.ID
	if plan.Mismatches == nil {
		plan.Mismatches = []workspaceRecoveryPlanMismatch{}
	}
	plan.URL, plan.ReceiptID = operation.URL, operation.ReceiptID
	if operation.RecoveryExecution != nil {
		plan.ExecutionID = operation.RecoveryExecution.ExecutionID
		plan.RunID = operation.RecoveryExecution.RunIdentity
		if operation.RecoveryExecution.ErrorCode != "" {
			plan.ErrorCode = operation.RecoveryExecution.ErrorCode
		}
		plan.ComputeClaimEvidence = operation.RecoveryExecution.MutationOutcome.ComputeClaimEvidence
	}
	return plan
}

func workspaceRecoveryPlanMismatchFromCheck(check clients.ComputeClaimIdentityCheck) workspaceRecoveryPlanMismatch {
	return workspaceRecoveryPlanMismatch{
		Field: check.Field, Expected: check.Expected, Actual: check.Actual,
		ExpectedDigest: check.ExpectedDigest, ActualDigest: check.ActualDigest,
	}
}

func workspaceRecoveryPlanMismatches(persisted, current workspaceRecoveryPlan) []workspaceRecoveryPlanMismatch {
	checks := []clients.ComputeClaimIdentityCheck{
		workspaceComputeClaimIdentityCheck("release.mainSha", persisted.ReleaseBinding.MainSHA, current.ReleaseBinding.MainSHA),
		workspaceComputeClaimIdentityDigestCheck("release.cloudImageDigest", persisted.ReleaseBinding.CloudImageDigest, current.ReleaseBinding.CloudImageDigest),
		workspaceComputeClaimIdentityDigestCheck("release.workspaceImageDigest", persisted.ReleaseBinding.WorkspaceImageDigest, current.ReleaseBinding.WorkspaceImageDigest),
		workspaceComputeClaimIdentityCheck("controlPlane.launchOperationId", persisted.TargetBinding.LaunchOperationID, current.TargetBinding.LaunchOperationID),
		workspaceComputeClaimIdentityCheck("controlPlane.accountId", persisted.TargetBinding.AccountID, current.TargetBinding.AccountID),
		workspaceComputeClaimIdentityCheck("controlPlane.workspaceId", persisted.TargetBinding.WorkspaceID, current.TargetBinding.WorkspaceID),
		workspaceComputeClaimIdentityCheck("controlPlane.computeAllocationId", persisted.TargetBinding.ComputeAllocationID, current.TargetBinding.ComputeAllocationID),
		workspaceComputeClaimIdentityCheck("controlPlane.storageId", persisted.TargetBinding.StorageID, current.TargetBinding.StorageID),
		workspaceComputeClaimIdentityCheck("controlPlane.stage", persisted.TargetBinding.Stage, current.TargetBinding.Stage),
		workspaceComputeClaimIdentityDigestCheck("controlPlane.workspaceImageDigest", persisted.TargetBinding.WorkspaceImageDigest, current.TargetBinding.WorkspaceImageDigest),
		workspaceComputeClaimIdentityDigestCheck("authority.binding", persisted.TargetBinding.AuthorityDigest, current.TargetBinding.AuthorityDigest),
		workspaceComputeClaimIdentityCheck("fabric.poolId", persisted.TargetBinding.PoolID, current.TargetBinding.PoolID),
		workspaceComputeClaimIdentityCheck("fabric.nodePoolId", persisted.TargetBinding.NodePoolID, current.TargetBinding.NodePoolID),
		workspaceComputeClaimIdentityCheck("provider.machineName", persisted.TargetBinding.MachineName, current.TargetBinding.MachineName),
		workspaceComputeClaimIdentityCheck("kubernetes.nodeName", persisted.TargetBinding.NodeName, current.TargetBinding.NodeName),
		workspaceComputeClaimIdentityCheck("tencent.cvmInstanceId", persisted.TargetBinding.CVMInstanceID, current.TargetBinding.CVMInstanceID),
		workspaceComputeClaimIdentityDigestCheck("tencent.privateIp", persisted.TargetBinding.PrivateIPDigest, current.TargetBinding.PrivateIPDigest),
		workspaceComputeClaimIdentityCheck("tencent.instanceType", persisted.TargetBinding.InstanceType, current.TargetBinding.InstanceType),
		workspaceComputeClaimIdentityCheck("tencent.zone", persisted.TargetBinding.Zone, current.TargetBinding.Zone),
		workspaceComputeClaimIdentityCheck("provider.nodeOwnership", persisted.TargetBinding.NodeOwnershipState, current.TargetBinding.NodeOwnershipState),
		workspaceComputeClaimIdentityCheck("provider.cvmOwnership", persisted.TargetBinding.CVMOwnershipState, current.TargetBinding.CVMOwnershipState),
		workspaceComputeClaimIdentityCheck("provider.storageState", persisted.TargetBinding.StorageState, current.TargetBinding.StorageState),
		workspaceComputeClaimIdentityCheck("provider.storageResourceId", persisted.TargetBinding.StorageProviderID, current.TargetBinding.StorageProviderID),
		workspaceComputeClaimIdentityCheck("controlPlane.workspaceApiKeyId", persisted.TargetBinding.WorkspaceAPIKeyID, current.TargetBinding.WorkspaceAPIKeyID),
		workspaceComputeClaimIdentityDigestCheck("decision.digest", persisted.DecisionBinding.DecisionDigest, current.DecisionBinding.DecisionDigest),
		workspaceComputeClaimIdentityDigestCheck("decision.evidenceDigest", persisted.DecisionBinding.EvidenceDigest, current.DecisionBinding.EvidenceDigest),
		workspaceComputeClaimIdentityCheck("decision.version", persisted.DecisionBinding.DecisionVersion, current.DecisionBinding.DecisionVersion),
		workspaceComputeClaimIdentityCheck("decision.currentStage", persisted.DecisionBinding.CurrentStage, current.DecisionBinding.CurrentStage),
		workspaceComputeClaimIdentityCheck("decision.stageAttemptId", persisted.DecisionBinding.StageAttemptID, current.DecisionBinding.StageAttemptID),
		workspaceComputeClaimIdentityCheck("decision.allowedMutation", persisted.DecisionBinding.AllowedMutation, current.DecisionBinding.AllowedMutation),
		workspaceComputeClaimIdentityCheck("decision.mutationBudget", persisted.DecisionBinding.MutationBudget, current.DecisionBinding.MutationBudget),
	}
	mismatches := make([]workspaceRecoveryPlanMismatch, 0)
	for _, check := range checks {
		if !check.Matches {
			mismatches = append(mismatches, workspaceRecoveryPlanMismatchFromCheck(check))
		}
	}
	return mismatches
}

func workspaceRecoveryHistoryProvesArchivedNodeClientRejection(operation workspaceLaunchOperation) bool {
	for _, entry := range operation.RecoveryHistory {
		outcome := entry.Execution.MutationOutcome
		if entry.ArchivedAt != "" && entry.Plan.Status == "failed" && entry.Plan.Action == "compute_claim_continue" && entry.Plan.OperationID == operation.ID &&
			entry.Execution.Status == "failed" && entry.Execution.CompletedAt != "" &&
			entry.Execution.LeaseToken == "" && entry.Execution.LeaseExpiresAt == "" &&
			entry.Execution.PlanID == entry.Plan.PlanID && entry.Execution.PlanDigest == entry.Plan.PlanDigest &&
			entry.Execution.ErrorCode == "workspace_compute_claim_provider_describe" &&
			outcome.Status == "nonzero" && outcome.Counts == (workspaceRecoveryMutationCounts{Kubernetes: 1}) &&
			outcome.FabricOperationMutations == 0 && outcome.Source == "compute_claim_response" {
			return true
		}
	}
	return false
}

func workspaceRecoveryExecutionSuccessorGate(operation workspaceLaunchOperation, evidence *clients.ComputeClaimIdentityEvidence, evaluation *workspaceComputeClaimProofEvaluation) (workspaceRecoveryMutationOutcome, workspaceRecoverySuccessorGateDTO) {
	gate := workspaceRecoverySuccessorGateDTO{
		Applicable: true, PlanState: "missing", ExecutionState: "missing", CompletionState: "missing",
		LeaseState: "released", IdentityState: "unavailable", PersistedMutationState: "missing", FabricLedgerState: "unavailable",
	}
	if operation.RecoveryPlan != nil {
		gate.PlanState = "nonterminal"
		if operation.RecoveryPlan.Status == "failed" || operation.RecoveryPlan.Status == "blocked" {
			gate.PlanState = "terminal"
		}
	}
	if operation.RecoveryExecution != nil {
		gate.ExecutionState = "nonterminal"
		if operation.RecoveryExecution.Status == "failed" {
			gate.ExecutionState = "failed"
		}
		if operation.RecoveryExecution.CompletedAt != "" {
			gate.CompletionState = "completed"
		}
		switch {
		case operation.RecoveryExecution.LeaseToken != "" && operation.RecoveryExecution.LeaseExpiresAt != "":
			gate.LeaseState = "held"
		case operation.RecoveryExecution.LeaseToken != "" || operation.RecoveryExecution.LeaseExpiresAt != "":
			gate.LeaseState = "partial"
		}
		if operation.RecoveryPlan != nil {
			gate.IdentityState = "mismatch"
			if operation.RecoveryExecution.PlanID == operation.RecoveryPlan.PlanID && operation.RecoveryExecution.PlanDigest == operation.RecoveryPlan.PlanDigest {
				gate.IdentityState = "matches"
			}
		}
		outcome := operation.RecoveryExecution.MutationOutcome
		switch outcome.Status {
		case "":
			gate.PersistedMutationState = "missing"
		case "confirmed_zero":
			gate.PersistedMutationState = "invalid"
			if outcome.Counts == (workspaceRecoveryMutationCounts{}) && outcome.FabricOperationMutations == 0 {
				gate.PersistedMutationState = "confirmed_zero"
			}
		case "nonzero":
			gate.PersistedMutationState = "nonzero"
		case "unknown":
			gate.PersistedMutationState = "unknown"
		default:
			gate.PersistedMutationState = "invalid"
		}
	}
	if evidence != nil {
		switch {
		case evidence.MutationLedger == "absent" && (evidence.MutationLedgerOutcome == "" || evidence.MutationLedgerOutcome == "confirmed_zero"):
			gate.FabricLedgerState = "absent"
		case evidence.MutationLedger == "observed" && evidence.MutationLedgerOutcome == "confirmed_zero" &&
			computeClaimApprovalDigestPattern.MatchString(evidence.MutationLedgerDigest):
			gate.FabricLedgerState = "confirmed_zero"
		case evidence.MutationLedger == "observed" && evidence.MutationLedgerOutcome == "nonzero":
			gate.FabricLedgerState = "nonzero"
		case evidence.MutationLedger == "observed" && evidence.MutationLedgerOutcome == "unknown":
			gate.FabricLedgerState = "unknown"
		default:
			gate.FabricLedgerState = "invalid"
		}
	}
	if gate.PlanState != "terminal" || gate.ExecutionState != "failed" || gate.CompletionState != "completed" ||
		gate.LeaseState != "released" || gate.IdentityState != "matches" {
		return workspaceRecoveryMutationOutcome{}, gate
	}
	outcome := operation.RecoveryExecution.MutationOutcome
	if workspaceComputeClaimRecoverableCVMOnly(evidence) {
		counts := workspaceRecoveryMutationCounts{Tencent: evidence.MutationEvidence.CVM.Attempted}
		persistedCountsCompatible := outcome.Counts == (workspaceRecoveryMutationCounts{}) || outcome.Counts == counts
		persistedOutcomeCompatible := outcome.Status == "" || outcome.Status == "unknown" || outcome.Status == "nonzero" && outcome.Counts == counts
		if persistedCountsCompatible && persistedOutcomeCompatible && outcome.FabricOperationMutations == 0 {
			gate.Allowed = true
			return workspaceRecoveryMutationOutcome{
				Status: "nonzero", Counts: counts, Source: "fabric_mutation_ledger_recoverable_cvm_only", EvidenceDigest: evidence.MutationLedgerDigest,
			}, gate
		}
		return workspaceRecoveryMutationOutcome{}, gate
	}
	if workspaceComputeClaimLegacyKubectlClientRejected(evidence) {
		if outcome.Status == "nonzero" && outcome.Counts == (workspaceRecoveryMutationCounts{Kubernetes: 1}) &&
			outcome.FabricOperationMutations == 0 && outcome.Source == "compute_claim_response" {
			gate.Allowed = true
			return outcome, gate
		}
		// The historical client rejection is authoritative even when the
		// successor's failed execution was recorded before the response could
		// be classified. Preserve the consumed Node budget as an attempted
		// call; API acceptance remains zero in the Fabric reconciliation.
		if outcome.Status == "unknown" && outcome.Counts == (workspaceRecoveryMutationCounts{}) &&
			outcome.FabricOperationMutations == 0 && outcome.Source == "compute_claim_response" && gate.FabricLedgerState == "absent" {
			gate.Allowed = true
			return workspaceRecoveryMutationOutcome{
				Status: "nonzero", Counts: workspaceRecoveryMutationCounts{Kubernetes: 1}, Source: "compute_claim_response",
			}, gate
		}
		return workspaceRecoveryMutationOutcome{}, gate
	}
	if outcome.Status == "unknown" && outcome.Counts == (workspaceRecoveryMutationCounts{}) && outcome.FabricOperationMutations == 0 &&
		operation.RecoveryExecution.ErrorCode == "workspace_launch_storage_attempt_unknown" &&
		operation.ContinuationAttemptBudgets["storage"] == (workspaceLaunchStageBudget{Attempted: 1, Unknown: 1, Max: workspaceLaunchStageMax}) {
		gate.Allowed = true
		return outcome, gate
	}
	absentDigest := sha256.Sum256([]byte("absent"))
	if outcome.Status == "nonzero" && outcome.Counts == (workspaceRecoveryMutationCounts{Kubernetes: 1}) &&
		outcome.FabricOperationMutations == 0 && outcome.Source == "compute_claim_response" &&
		operation.RecoveryExecution.ErrorCode == "workspace_launch_storage_attempt_unknown" &&
		operation.ContinuationAttemptBudgets["storage"] == (workspaceLaunchStageBudget{Attempted: 1, Unknown: 1, Max: workspaceLaunchStageMax}) &&
		workspaceRecoveryHistoryProvesArchivedNodeClientRejection(operation) && evidence != nil &&
		evidence.MutationLedger == "absent" && evidence.MutationLedgerOutcome == "confirmed_zero" &&
		evidence.MutationLedgerDigest == hex.EncodeToString(absentDigest[:]) && evidence.MutationEvidence == nil &&
		evidence.FailureStage == "" && evidence.ProviderErrorClass == "" &&
		evaluation != nil && evaluation.Eligible && evaluation.FirstFalsePredicate == "provider.nodeOwnership" &&
		evaluation.Expected == "target_owned" && evaluation.Actual == "unallocated" && evaluation.Authority == "provider.nodeOwnership" &&
		evaluation.CVMOwnershipState == "target_owned" && evaluation.NodeOwnershipState == "unallocated" {
		gate.Allowed = true
		return outcome, gate
	}
	if outcome.Status == "nonzero" && outcome.Counts == (workspaceRecoveryMutationCounts{Kubernetes: 1}) &&
		outcome.FabricOperationMutations == 0 && outcome.Source == "compute_claim_response" &&
		operation.RecoveryExecution.ErrorCode == "workspace_launch_storage_attempt_unknown" &&
		operation.ContinuationAttemptBudgets["storage"] == (workspaceLaunchStageBudget{Attempted: 1, Unknown: 1, Max: workspaceLaunchStageMax}) &&
		evaluation != nil && evaluation.Eligible && evaluation.FirstFalsePredicate == "" &&
		evaluation.CVMOwnershipState == "target_owned" && evaluation.NodeOwnershipState == "target_owned" {
		gate.Allowed = true
		return outcome, gate
	}
	if outcome.Status == "confirmed_zero" && outcome.Counts == (workspaceRecoveryMutationCounts{}) && outcome.FabricOperationMutations == 0 && evidence == nil {
		gate.Allowed = true
		return outcome, gate
	}
	if outcome.Status != "" && outcome.Status != "unknown" && outcome.Status != "confirmed_zero" || operation.RecoveryPlan.Action != "compute_claim_continue" || evidence == nil {
		return workspaceRecoveryMutationOutcome{}, gate
	}
	source, evidenceDigest := "", ""
	switch {
	case evidence.MutationLedger == "absent" && (evidence.MutationLedgerOutcome == "" || evidence.MutationLedgerOutcome == "confirmed_zero"):
		source = "fabric_mutation_ledger_absent"
		if computeClaimApprovalDigestPattern.MatchString(evidence.MutationLedgerDigest) {
			evidenceDigest = evidence.MutationLedgerDigest
		}
	case evidence.MutationLedger == "observed" && evidence.MutationLedgerOutcome == "confirmed_zero" &&
		computeClaimApprovalDigestPattern.MatchString(evidence.MutationLedgerDigest):
		source, evidenceDigest = "fabric_mutation_ledger_confirmed_zero", evidence.MutationLedgerDigest
	default:
		return workspaceRecoveryMutationOutcome{}, gate
	}
	gate.Allowed = true
	return workspaceRecoveryMutationOutcome{Status: "confirmed_zero", Source: source, EvidenceDigest: evidenceDigest}, gate
}

func workspaceRecoveryExecutionConfirmedZero(operation workspaceLaunchOperation, evidence *clients.ComputeClaimIdentityEvidence) (workspaceRecoveryMutationOutcome, bool) {
	outcome, gate := workspaceRecoveryExecutionSuccessorGate(operation, evidence, nil)
	return outcome, gate.Allowed
}

func workspaceRecoveryMutationOutcomeFromComputeClaim(proof clients.ComputeClaimRecoveryProof) workspaceRecoveryMutationOutcome {
	outcome := workspaceRecoveryMutationOutcome{Status: "unknown", Source: "compute_claim_response"}
	if !proof.Eligible || proof.Reason != "none" {
		outcome.ComputeClaimEvidence = workspaceRecoveryComputeClaimEvidenceFromProof(proof)
	}
	if !workspaceComputeClaimEvidenceMatches(proof, false) || proof.Sub2APIMutationCount < 0 || proof.TencentMutationCount < 0 || proof.KubernetesMutationCount < 0 {
		return outcome
	}
	outcome.Counts = workspaceRecoveryMutationCounts{
		Sub2API: proof.Sub2APIMutationCount, Tencent: proof.TencentMutationCount, Kubernetes: proof.KubernetesMutationCount,
	}
	if outcome.Counts != (workspaceRecoveryMutationCounts{}) {
		outcome.Status = "nonzero"
	}
	return outcome
}

func safeWorkspaceRecoveryComputeClaimBindingClassification(value string) bool {
	switch value {
	case "current", "compute-claim", "request-hash-reconciliation", "known-legacy", "other":
		return true
	default:
		return false
	}
}

func workspaceRecoveryComputeClaimIdentityChecksValid(evidence *clients.ComputeClaimIdentityEvidence) bool {
	fields := []string{
		"fabric.operationId", "fabric.operationIdempotencyKey", "fabric.operationRequestHash",
		"binding.present", "binding.valid",
	}
	if len(evidence.Checks) == 10 {
		fields = append(fields, "binding.compatibility", "binding.launchOperationId", "binding.idempotencyKey", "binding.targetHash", "binding.requestHash")
	} else if len(evidence.Checks) != len(fields) || evidence.BindingClassification != "other" {
		return false
	}
	for index, field := range fields {
		if evidence.Checks[index].Field != field {
			return false
		}
	}
	return true
}

func workspaceRecoveryComputeClaimRequestHashMismatch(evidence *clients.ComputeClaimIdentityEvidence) (clients.ComputeClaimIdentityCheck, bool) {
	if evidence.BindingClassification != "request-hash-reconciliation" || len(evidence.Checks) != 10 {
		return clients.ComputeClaimIdentityCheck{}, false
	}
	for _, check := range evidence.Checks {
		if check.Field != "binding.requestHash" {
			continue
		}
		return check, !check.Matches && computeClaimApprovalDigestPattern.MatchString(check.ExpectedDigest) &&
			computeClaimApprovalDigestPattern.MatchString(check.ActualDigest) && check.ExpectedDigest != check.ActualDigest
	}
	return clients.ComputeClaimIdentityCheck{}, false
}

func workspaceRecoveryComputeClaimLedgerEvidenceValid(evidence *clients.ComputeClaimIdentityEvidence) bool {
	if !computeClaimApprovalDigestPattern.MatchString(evidence.MutationLedgerDigest) ||
		!safeWorkspaceComputeClaimFailureStage(evidence.FailureStage) ||
		!safeWorkspaceComputeClaimProviderErrorClass(evidence.ProviderErrorClass) ||
		(evidence.FailureStage == "") != (evidence.ProviderErrorClass == "") {
		return false
	}
	validMutationEvidence := func() bool {
		if evidence.MutationEvidence == nil {
			return true
		}
		return workspaceComputeClaimMutationEvidenceMatches(evidence.MutationEvidence.CVM, evidence.MutationEvidence.CVM.Attempted, 5, "cvm", false) &&
			workspaceComputeClaimMutationEvidenceMatches(evidence.MutationEvidence.Node, evidence.MutationEvidence.Node.Attempted, 1, "node", false)
	}
	switch evidence.MutationLedger {
	case "absent":
		return evidence.MutationLedgerOutcome == "confirmed_zero" && evidence.MutationEvidence == nil &&
			evidence.FailureStage == "" && evidence.ProviderErrorClass == ""
	case "observed":
		if evidence.MutationEvidence == nil || !validMutationEvidence() {
			return false
		}
		switch evidence.MutationLedgerOutcome {
		case "confirmed_zero":
			return evidence.MutationEvidence.CVM.Attempted == 0 && evidence.MutationEvidence.Node.Attempted == 0
		case "nonzero":
			return evidence.MutationEvidence.CVM.Attempted+evidence.MutationEvidence.Node.Attempted > 0
		case "unknown":
			return true
		default:
			return false
		}
	case "reserved", "node_reserved":
		return evidence.MutationLedgerOutcome == "unknown" && validMutationEvidence()
	case "invalid":
		return evidence.MutationLedgerOutcome == "unknown" && evidence.MutationEvidence == nil
	default:
		return false
	}
}

func workspaceRecoveryComputeClaimEvidenceFromProof(proof clients.ComputeClaimRecoveryProof) *workspaceRecoveryComputeClaimEvidence {
	evidence := proof.IdentityEvidence
	if evidence == nil || !safeWorkspaceRecoveryComputeClaimBindingClassification(evidence.BindingClassification) ||
		!computeClaimApprovalDigestPattern.MatchString(evidence.BindingDigest) || !workspaceRecoveryComputeClaimIdentityChecksValid(evidence) ||
		!workspaceRecoveryComputeClaimLedgerEvidenceValid(evidence) || proof.Evidence == nil ||
		proof.FailureStage == "" || proof.ProviderErrorClass == "" ||
		!safeWorkspaceComputeClaimFailureStage(proof.FailureStage) || !safeWorkspaceComputeClaimProviderErrorClass(proof.ProviderErrorClass) ||
		!workspaceComputeClaimMutationEvidenceMatches(proof.Evidence.CVM, proof.Evidence.CVM.Attempted, 5, "cvm", false) ||
		!workspaceComputeClaimMutationEvidenceMatches(proof.Evidence.Node, proof.Evidence.Node.Attempted, 1, "node", false) {
		return nil
	}
	mismatch, mismatchPresent := workspaceRecoveryComputeClaimRequestHashMismatch(evidence)
	if evidence.BindingClassification == "request-hash-reconciliation" && !mismatchPresent {
		return nil
	}
	cvm, node := clients.ComputeClaimMutationEvidence{}, clients.ComputeClaimMutationEvidence{}
	if evidence.MutationEvidence != nil {
		cvm, node = evidence.MutationEvidence.CVM, evidence.MutationEvidence.Node
	}
	result := &workspaceRecoveryComputeClaimEvidence{
		SchemaVersion: 1, BindingClassification: evidence.BindingClassification,
		MutationLedger: evidence.MutationLedger, MutationLedgerOutcome: evidence.MutationLedgerOutcome,
		CVM:                workspaceRecoveryComputeClaimMutationEvidenceProjection(cvm),
		Node:               workspaceRecoveryComputeClaimMutationEvidenceProjection(node),
		LedgerFailureStage: evidence.FailureStage, LedgerProviderErrorClass: evidence.ProviderErrorClass,
		FailureStage: proof.FailureStage, ProviderErrorClass: proof.ProviderErrorClass,
	}
	if mismatchPresent {
		result.MismatchField, result.ExpectedDigest, result.ActualDigest = mismatch.Field, mismatch.ExpectedDigest, mismatch.ActualDigest
	}
	if reconciliation := evidence.Reconciliation; mismatchPresent && reconciliation != nil &&
		reconciliation.Consumer == "claim_compute_recovery" && (reconciliation.SchemaVersion == 1 || reconciliation.SchemaVersion == 2) &&
		(reconciliation.Generation == "isolated_request_hash_v1" || reconciliation.Generation == "normal_launch_terminal_evidence_v1") &&
		(reconciliation.State == "verified" || reconciliation.State == "node_reserved" || reconciliation.State == "observed" || reconciliation.State == "succeeded") &&
		reconciliation.ExpectedRequestHashDigest == mismatch.ExpectedDigest && reconciliation.PersistedRequestHashDigest == mismatch.ActualDigest &&
		(reconciliation.FailureStage == "") == (reconciliation.ProviderErrorClass == "") &&
		safeWorkspaceComputeClaimFailureStage(reconciliation.FailureStage) && safeWorkspaceComputeClaimProviderErrorClass(reconciliation.ProviderErrorClass) &&
		workspaceComputeClaimMutationEvidenceMatches(reconciliation.Node, reconciliation.Node.Attempted, 1, "node", false) {
		provenanceValid := reconciliation.SchemaVersion == 1 && reconciliation.ProvenanceSource == "" && reconciliation.ProvenanceDigest == "" ||
			reconciliation.SchemaVersion == 2 && reconciliation.ProvenanceSource == "normal_launch_terminal_evidence" &&
				computeClaimApprovalDigestPattern.MatchString(reconciliation.ProvenanceDigest)
		if provenanceValid {
			result.Reconciliation = &workspaceRecoveryComputeClaimReconciliationEvidence{
				SchemaVersion: reconciliation.SchemaVersion, Consumer: reconciliation.Consumer, Generation: reconciliation.Generation,
				ProvenanceSource: reconciliation.ProvenanceSource, ProvenanceDigest: reconciliation.ProvenanceDigest,
				State: reconciliation.State, FailureStage: reconciliation.FailureStage,
				ProviderErrorClass: reconciliation.ProviderErrorClass,
				Node:               workspaceRecoveryComputeClaimMutationEvidenceProjection(reconciliation.Node),
			}
		}
	}
	return result
}

func newWorkspaceRecoverySuccessor(plan workspaceRecoveryPlan, predecessor workspaceRecoveryPlan, execution workspaceRecoveryExecution, historyLength int) workspaceRecoveryPlan {
	plan.Generation = predecessor.Generation + 1
	if plan.Generation <= historyLength {
		plan.Generation = historyLength + 1
	}
	plan.PredecessorPlanDigest = predecessor.PlanDigest
	plan.PredecessorExecutionID = execution.ExecutionID
	plan.PlanDigest = workspaceRecoveryPlanDigest(plan)
	plan.PlanID = "recovery-plan-" + plan.PlanDigest[:20]
	return plan
}

func workspaceRecoveryPlanReadbackCandidate(operation workspaceLaunchOperation) (workspaceLaunchOperation, bool) {
	_, ok := workspaceLaunchReadbackRecoveryStage(operation)
	return operation, ok
}

func (app *controlPlaneServer) diagnoseWorkspaceRecoveryPlan(ctx context.Context, service *controlplane.Service, accountID, operationID string) (workspaceRecoveryPlan, error) {
	operation, ok, err := app.workspaceLaunchOperation(ctx, operationID)
	if err != nil || !ok {
		if err == nil {
			err = errBillingReviewNotFound
		}
		return workspaceRecoveryPlan{}, err
	}
	if accountID == "" || operation.AccountID != accountID {
		return workspaceRecoveryPlan{}, errBillingReviewIdentity
	}
	release, err := currentWorkspaceRecoveryReleaseBinding()
	if err != nil {
		return workspaceRecoveryPlan{}, workspaceRecoveryPlanClassifiedFailure(
			err, "release_binding", "release_binding_invalid", "workspace_recovery_plan_release_binding_invalid",
		)
	}

	unlock := app.lockResource("workspace-launch", operation.AccountID)
	defer unlock()
	operation, ok, err = app.workspaceLaunchOperation(ctx, operationID)
	if err != nil || !ok || operation.AccountID != accountID {
		if err == nil {
			err = errBillingReviewIdentity
		}
		return workspaceRecoveryPlan{}, err
	}
	readbackCandidate, stageReadback := workspaceRecoveryPlanReadbackCandidate(operation)
	var plan workspaceRecoveryPlan
	var computeEvidence *clients.ComputeClaimIdentityEvidence
	var recovered workspaceLaunchOperation
	var proof workspaceLaunchReadbackRecoveryProof
	var readbackErr error
	var computeDecision *CurrentDecision
	var computeEvaluation *workspaceComputeClaimProofEvaluation
	computeClaimCandidate := workspaceComputeClaimRecoveryCandidate(operation)
	if computeClaimCandidate {
		// A later Storage unknown is only a Storage mutation boundary. Read the
		// authoritative Compute Claim first so Node ownership can converge.
		stageReadback = false
	}
	if computeClaimCandidate {
		computeInput, computeProof, evidence, computeErr := app.workspaceComputeClaimRecoveryProofForPlan(ctx, service, operation)
		if computeErr != nil {
			return workspaceRecoveryPlan{}, computeErr
		}
		if !validWorkspaceLaunchComputeClaimIdentity(operation) && !persistWorkspaceComputeClaimIdentityFromProof(&operation, computeProof) {
			return workspaceRecoveryPlan{}, errWorkspaceComputeClaimIdentity
		}
		computeEvidence = evidence
		evaluation := evaluateWorkspaceComputeClaimProof(operation, computeInput, computeProof, false)
		computeEvaluation = &evaluation
		decision := currentDecisionForComputeClaimEvaluation(operation, nil, evaluation)
		computeDecision = &decision
		plan, err = newWorkspaceComputeClaimRecoveryPlan(operation, computeInput, computeProof, evaluation, evidence, release)
	} else if stageReadback {
		recovered, proof, readbackErr = app.workspaceLaunchReadbackRecoveryProofForOperation(ctx, service, readbackCandidate)
		if readbackErr != nil {
			return workspaceRecoveryPlan{}, readbackErr
		}
		expectedPrivateIP, privateIPErr := app.workspaceRecoveryPlanExpectedPrivateIP(recovered)
		if privateIPErr != nil {
			return workspaceRecoveryPlan{}, privateIPErr
		}
		plan, err = newWorkspaceReadbackRecoveryPlan(recovered, proof, release, expectedPrivateIP)
	} else {
		recovered, proof, readbackErr = app.workspaceLaunchReadbackRecoveryProofForOperation(ctx, service, readbackCandidate)
		if readbackErr != nil {
			return workspaceRecoveryPlan{}, readbackErr
		}
		expectedPrivateIP, privateIPErr := app.workspaceRecoveryPlanExpectedPrivateIP(recovered)
		if privateIPErr != nil {
			return workspaceRecoveryPlan{}, privateIPErr
		}
		plan, err = newWorkspaceReadbackRecoveryPlan(recovered, proof, release, expectedPrivateIP)
	}
	if err != nil {
		return workspaceRecoveryPlan{}, err
	}
	if operation.RecoveryExecution != nil {
		if operation.RecoveryExecution.Status != "failed" {
			return workspaceRecoveryPlanProjection(operation), nil
		}
		predecessorPlan := *operation.RecoveryPlan
		predecessorExecution := *operation.RecoveryExecution
		predecessorPlan.Status = "failed"
		predecessorPlan.ErrorCode = predecessorExecution.ErrorCode
		if stageReadback {
			_, terminalGate := workspaceRecoveryExecutionSuccessorGate(operation, nil, nil)
			if terminalGate.PlanState != "terminal" || terminalGate.ExecutionState != "failed" || terminalGate.CompletionState != "completed" ||
				terminalGate.LeaseState != "released" || terminalGate.IdentityState != "matches" {
				projected := workspaceRecoveryPlanProjection(operation)
				projected.SuccessorGate = &terminalGate
				return projected, nil
			}
		} else {
			outcome, successorGate := workspaceRecoveryExecutionSuccessorGate(operation, computeEvidence, computeEvaluation)
			if !successorGate.Allowed {
				projected := workspaceRecoveryPlanProjection(operation)
				projected.SuccessorGate = &successorGate
				return projected, nil
			}
			predecessorExecution.MutationOutcome = outcome
			plan.SuccessorGate = &successorGate
		}
		operation.RecoveryHistory = append(operation.RecoveryHistory, workspaceRecoveryPlanHistoryEntry{
			Plan: predecessorPlan, Execution: predecessorExecution, ArchivedAt: time.Now().UTC().Format(time.RFC3339Nano),
		})
		plan = newWorkspaceRecoverySuccessor(plan, predecessorPlan, predecessorExecution, len(operation.RecoveryHistory)-1)
		operation.RecoveryPlan = &plan
		operation.RecoveryExecution = nil
		if err := app.persistWorkspaceRecoveryPlanDecision(ctx, &operation, computeDecision); err != nil {
			if errors.Is(err, errWorkspaceLaunchCASConflict) {
				current, found, loadErr := app.workspaceLaunchOperation(ctx, operationID)
				if loadErr == nil && found && current.RecoveryPlan != nil && current.RecoveryPlan.PlanDigest == plan.PlanDigest &&
					len(current.RecoveryHistory) >= len(operation.RecoveryHistory) {
					return workspaceRecoveryPlanProjection(current), nil
				}
			}
			return workspaceRecoveryPlan{}, err
		}
		return plan, nil
	}
	if operation.RecoveryPlan != nil && operation.RecoveryPlan.Generation > 0 {
		plan.Generation = operation.RecoveryPlan.Generation
		plan.PredecessorPlanDigest = operation.RecoveryPlan.PredecessorPlanDigest
		plan.PredecessorExecutionID = operation.RecoveryPlan.PredecessorExecutionID
		plan.PlanDigest = workspaceRecoveryPlanDigest(plan)
		plan.PlanID = "recovery-plan-" + plan.PlanDigest[:20]
	}
	if operation.RecoveryPlan != nil && operation.RecoveryPlan.PlanDigest == plan.PlanDigest {
		if computeDecision != nil && !sameCurrentDecisionAuthority(operation.CurrentDecision, *computeDecision) {
			if err := app.persistWorkspaceLaunchWithDecision(ctx, &operation, *computeDecision); err != nil {
				return workspaceRecoveryPlan{}, err
			}
		}
		return *operation.RecoveryPlan, nil
	}
	operation.RecoveryPlan = &plan
	operation.RecoveryExecution = nil
	if err := app.persistWorkspaceRecoveryPlanDecision(ctx, &operation, computeDecision); err != nil {
		if errors.Is(err, errWorkspaceLaunchCASConflict) {
			current, found, loadErr := app.workspaceLaunchOperation(ctx, operationID)
			if loadErr == nil && found && current.RecoveryPlan != nil && current.RecoveryPlan.PlanDigest == plan.PlanDigest {
				return *current.RecoveryPlan, nil
			}
		}
		return workspaceRecoveryPlan{}, err
	}
	return plan, nil
}

func (app *controlPlaneServer) persistWorkspaceRecoveryPlanDecision(ctx context.Context, operation *workspaceLaunchOperation, decision *CurrentDecision) error {
	if decision != nil {
		return app.persistWorkspaceLaunchWithDecision(ctx, operation, *decision)
	}
	return app.persistWorkspaceLaunch(ctx, operation)
}

func (app *controlPlaneServer) getWorkspaceRecoveryPlan(ctx context.Context, operationID string) (workspaceRecoveryPlan, error) {
	operation, ok, err := app.workspaceLaunchOperation(ctx, operationID)
	if err != nil || !ok || operation.RecoveryPlan == nil {
		if err == nil {
			err = errBillingReviewNotFound
		}
		return workspaceRecoveryPlan{}, err
	}
	return workspaceRecoveryPlanProjection(operation), nil
}

func (app *controlPlaneServer) validateWorkspaceRecoveryPlan(ctx context.Context, service *controlplane.Service, operationID, planID, planDigest string) (workspaceRecoveryPlan, error) {
	operation, ok, err := app.workspaceLaunchOperation(ctx, operationID)
	if err != nil || !ok || operation.RecoveryPlan == nil {
		if err == nil {
			err = errBillingReviewNotFound
		}
		return workspaceRecoveryPlan{}, err
	}
	if operation.RecoveryPlan.PlanID != planID || operation.RecoveryPlan.PlanDigest != planDigest {
		return workspaceRecoveryPlan{}, errBillingReviewIdentity
	}

	unlock := app.lockResource("workspace-launch", operation.AccountID)
	defer unlock()
	operation, ok, err = app.workspaceLaunchOperation(ctx, operationID)
	if err != nil || !ok || operation.RecoveryPlan == nil || operation.RecoveryPlan.PlanID != planID || operation.RecoveryPlan.PlanDigest != planDigest {
		if err == nil {
			err = errBillingReviewIdentity
		}
		return workspaceRecoveryPlan{}, err
	}
	if operation.RecoveryExecution != nil &&
		(operation.RecoveryExecution.Status == "completed" || operation.RecoveryExecution.Status == "failed") {
		return workspaceRecoveryPlanProjection(operation), nil
	}
	release, err := currentWorkspaceRecoveryReleaseBinding()
	if err != nil {
		return workspaceRecoveryPlan{}, err
	}
	var current workspaceRecoveryPlan
	var computeDecision *CurrentDecision
	if operation.RecoveryPlan.Action == "compute_claim_continue" {
		computeInput, computeProof, evidence, computeErr := app.workspaceComputeClaimRecoveryProofForPlan(ctx, service, operation)
		if computeErr != nil {
			return workspaceRecoveryPlan{}, computeErr
		}
		evaluation := evaluateWorkspaceComputeClaimProof(operation, computeInput, computeProof, false)
		decision := currentDecisionForComputeClaimEvaluation(operation, nil, evaluation)
		computeDecision = &decision
		current, err = newWorkspaceComputeClaimRecoveryPlan(operation, computeInput, computeProof, evaluation, evidence, release)
	} else {
		recovered, proof, proofErr := app.workspaceLaunchReadbackRecoveryProofForOperation(ctx, service, operation)
		if proofErr != nil {
			return workspaceRecoveryPlan{}, proofErr
		}
		expectedPrivateIP, privateIPErr := app.workspaceRecoveryPlanExpectedPrivateIP(recovered)
		if privateIPErr != nil {
			return workspaceRecoveryPlan{}, privateIPErr
		}
		current, err = newWorkspaceReadbackRecoveryPlan(recovered, proof, release, expectedPrivateIP)
	}
	if err != nil {
		return workspaceRecoveryPlan{}, err
	}
	validated := *operation.RecoveryPlan
	validated.ValidatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	validated.Mismatches = workspaceRecoveryPlanMismatches(validated, current)
	validated.Status, validated.ErrorCode = "validated", ""
	if len(validated.Mismatches) != 0 {
		validated.Status, validated.ErrorCode = "blocked", "identity_mismatch"
	}
	operation.RecoveryPlan = &validated
	if err := app.persistWorkspaceRecoveryPlanDecision(ctx, &operation, computeDecision); err != nil {
		return workspaceRecoveryPlan{}, err
	}
	return workspaceRecoveryPlanProjection(operation), nil
}

func newWorkspaceRecoveryExecution(plan workspaceRecoveryPlan, proof workspaceLaunchReadbackRecoveryProof, decision, reviewer string) workspaceRecoveryExecution {
	executionID := "recovery-exec-" + workspaceRecoveryAuthorityDigest([]string{plan.PlanID, plan.PlanDigest, decision, reviewer})[:20]
	approval := workspaceLaunchReadbackRecoveryApproval{
		SchemaVersion:        1,
		ApprovalID:           "recovery-approval-" + plan.PlanDigest[:20],
		ExpiresAt:            time.Now().UTC().Add(15 * time.Minute).Format(time.RFC3339),
		MergedMainSHA:        plan.ReleaseBinding.MainSHA,
		CloudImageDigest:     plan.ReleaseBinding.CloudImageDigest,
		WorkspaceImageDigest: proof.WorkspaceImageDigest,
		Confirmation:         workspaceLaunchReadbackRecoveryConfirmation,
		IdempotencyKey:       executionID,
		RecoveryKey:          "recovery-plan-" + plan.PlanDigest[:20],
		Stage:                proof.Stage,
		Customer:             proof.Customer,
		Target:               proof.Target,
		Resources:            proof.Resources,
		OperationIDs:         proof.OperationIDs,
		AttemptBudget:        proof.AttemptBudget,
		AllowedWrites:        append([]string(nil), proof.AllowedWrites...),
		ForbiddenWrites:      append([]string(nil), proof.ForbiddenWrites...),
	}
	approval.ApprovalDigest = workspaceLaunchReadbackRecoveryApprovalDigest(approval)
	now := time.Now().UTC()
	return workspaceRecoveryExecution{
		ExecutionID:    executionID,
		RunIdentity:    "control-plane-run-" + workspaceRecoveryAuthorityDigest([]string{executionID, approval.ApprovalDigest})[:20],
		PlanID:         plan.PlanID,
		PlanDigest:     plan.PlanDigest,
		ApprovalDigest: approval.ApprovalDigest,
		Decision:       decision,
		Reviewer:       reviewer,
		Status:         "running",
		LeaseToken:     workspaceRecoveryAuthorityDigest([]string{"lease", executionID, approval.ApprovalDigest}),
		LeaseExpiresAt: now.Add(5 * time.Minute).Format(time.RFC3339Nano),
		StartedAt:      now.Format(time.RFC3339Nano),
		Approval:       &approval,
	}
}

func newWorkspaceComputeClaimRecoveryExecution(operation workspaceLaunchOperation, plan workspaceRecoveryPlan, input workspaceComputeClaimRecoveryRequest, proof clients.ComputeClaimRecoveryProof, customer workspaceLaunchReadbackRecoveryCustomer, decision, reviewer string) (workspaceRecoveryExecution, error) {
	targetOperation := operation
	if !persistWorkspaceComputeClaimIdentityFromProof(&targetOperation, proof) {
		return workspaceRecoveryExecution{}, errWorkspaceComputeClaimIdentity
	}
	executionID := "recovery-exec-" + workspaceRecoveryAuthorityDigest([]string{plan.PlanID, plan.PlanDigest, decision, reviewer})[:20]
	now := time.Now().UTC()
	binding := workspaceComputeClaimApprovalBinding{
		SchemaVersion:        2,
		ApprovalID:           "recovery-approval-" + plan.PlanDigest[:20],
		ExpiresAt:            now.Add(15 * time.Minute).Format(time.RFC3339),
		MergedMainSHA:        plan.ReleaseBinding.MainSHA,
		CloudImageDigest:     plan.ReleaseBinding.CloudImageDigest,
		WorkspaceImageDigest: operation.WorkspaceImageDigest,
		Confirmation:         "RECOVER_PROVEN_COMPUTE_AND_CONTINUE_ORIGINAL_LAUNCH",
		IdempotencyKey:       executionID,
		RecoveryKey:          "recovery-plan-" + plan.PlanDigest[:20],
		Customer:             workspaceComputeClaimApprovalCustomer{Email: customer.Email, AccountID: operation.AccountID},
		Target:               workspaceComputeClaimApprovalTargetFromOperation(targetOperation),
		Resources:            workspaceComputeClaimExpectedResources(targetOperation, proof.StorageState, proof.StorageProviderResourceID),
		AttemptLimits: workspaceComputeClaimAttemptLimits{
			Claim: workspaceComputeClaimProviderAttemptLimits{
				Sub2API: plan.DecisionBinding.MutationBudget.Sub2API, Tencent: plan.DecisionBinding.MutationBudget.Tencent,
				Kubernetes: plan.DecisionBinding.MutationBudget.Kubernetes,
			},
			Storage: 1, Attachment: 1, Secret: 1, Runtime: 1, Activation: 1, Receipt: 1,
		},
		AllowedWrites:   workspaceComputeClaimAllowedWritesForStorage(proof.StorageState),
		ForbiddenWrites: append([]string(nil), workspaceComputeClaimForbiddenWrites...),
	}
	binding.ApprovalDigest = workspaceComputeClaimApprovalDigest(binding)
	input.MachineName, input.NodeName, input.CVMInstanceID, input.PrivateIP = proof.MachineName, proof.NodeName, proof.CVMInstanceID, proof.PrivateIP
	input.PoolID, input.InstanceType, input.Zone = proof.PoolID, proof.InstanceType, proof.Zone
	input.ApprovalID, input.ApprovalDigest, input.ExpiresAt = binding.ApprovalID, binding.ApprovalDigest, binding.ExpiresAt
	input.MergedMainSHA, input.CloudImageDigest, input.WorkspaceImageDigest = binding.MergedMainSHA, binding.CloudImageDigest, binding.WorkspaceImageDigest
	input.CustomerEmail, input.RecoveryKey, input.Confirmation = binding.Customer.Email, binding.RecoveryKey, binding.Confirmation
	input.Resources, input.AttemptLimits = binding.Resources, binding.AttemptLimits
	input.AllowedWrites, input.ForbiddenWrites = append([]string(nil), binding.AllowedWrites...), append([]string(nil), binding.ForbiddenWrites...)
	return workspaceRecoveryExecution{
		ExecutionID: executionID,
		RunIdentity: "control-plane-run-" + workspaceRecoveryAuthorityDigest([]string{executionID, binding.ApprovalDigest})[:20],
		PlanID:      plan.PlanID, PlanDigest: plan.PlanDigest, ApprovalDigest: binding.ApprovalDigest,
		Decision: decision, Reviewer: reviewer, Status: "running",
		LeaseToken:     workspaceRecoveryAuthorityDigest([]string{"lease", executionID, binding.ApprovalDigest}),
		LeaseExpiresAt: now.Add(5 * time.Minute).Format(time.RFC3339Nano), StartedAt: now.Format(time.RFC3339Nano),
		ComputeClaimRequest: &input,
	}, nil
}

func (app *controlPlaneServer) reserveWorkspaceRecoveryExecution(ctx context.Context, service *controlplane.Service, operationID, planID, planDigest, decision, reviewer string) (workspaceRecoveryExecution, bool, error) {
	reviewer = strings.TrimSpace(reviewer)
	if reviewer == "" {
		return workspaceRecoveryExecution{}, false, errBillingReviewIdentity
	}
	operation, ok, err := app.workspaceLaunchOperation(ctx, operationID)
	if err != nil || !ok || operation.RecoveryPlan == nil {
		if err == nil {
			err = errBillingReviewNotFound
		}
		return workspaceRecoveryExecution{}, false, err
	}
	unlock := app.lockResource("workspace-launch", operation.AccountID)
	defer unlock()
	operation, ok, err = app.workspaceLaunchOperation(ctx, operationID)
	if err != nil || !ok || operation.RecoveryPlan == nil {
		if err == nil {
			err = errBillingReviewNotFound
		}
		return workspaceRecoveryExecution{}, false, err
	}
	plan := operation.RecoveryPlan
	if plan.PlanID != planID || plan.PlanDigest != planDigest || plan.Status != "validated" || len(plan.Mismatches) != 0 || plan.ErrorCode != "" {
		return workspaceRecoveryExecution{}, false, errBillingReviewIdentity
	}
	if operation.RecoveryExecution != nil {
		execution := *operation.RecoveryExecution
		readbackApprovalValid := execution.Approval != nil && execution.ApprovalDigest == execution.Approval.ApprovalDigest
		computeApprovalValid := execution.ComputeClaimRequest != nil && execution.ApprovalDigest == execution.ComputeClaimRequest.ApprovalDigest
		if execution.PlanID != planID || execution.PlanDigest != planDigest || execution.Decision != decision || execution.Reviewer != reviewer ||
			execution.ApprovalDigest == "" || !readbackApprovalValid && !computeApprovalValid {
			return workspaceRecoveryExecution{}, false, errBillingReviewIdentity
		}
		return execution, false, nil
	}

	release, err := currentWorkspaceRecoveryReleaseBinding()
	if err != nil {
		return workspaceRecoveryExecution{}, false, err
	}
	var current workspaceRecoveryPlan
	var execution workspaceRecoveryExecution
	var computeDecision *CurrentDecision
	if plan.Action == "compute_claim_continue" {
		input, proof, evidence, proofErr := app.workspaceComputeClaimRecoveryProofForPlan(ctx, service, operation)
		if proofErr != nil {
			return workspaceRecoveryExecution{}, false, proofErr
		}
		evaluation := evaluateWorkspaceComputeClaimProof(operation, input, proof, false)
		currentDecision := currentDecisionForComputeClaimEvaluation(operation, nil, evaluation)
		computeDecision = &currentDecision
		current, err = newWorkspaceComputeClaimRecoveryPlan(operation, input, proof, evaluation, evidence, release)
		if err == nil {
			customer, customerErr := app.workspaceLaunchReadbackRecoveryCustomer(ctx, operation)
			if customerErr != nil {
				return workspaceRecoveryExecution{}, false, customerErr
			}
			execution, err = newWorkspaceComputeClaimRecoveryExecution(operation, *plan, input, proof, customer, decision, reviewer)
		}
	} else {
		recovered, proof, proofErr := app.workspaceLaunchReadbackRecoveryProofForOperation(ctx, service, operation)
		if proofErr != nil {
			return workspaceRecoveryExecution{}, false, proofErr
		}
		expectedPrivateIP, privateIPErr := app.workspaceRecoveryPlanExpectedPrivateIP(recovered)
		if privateIPErr != nil {
			return workspaceRecoveryExecution{}, false, privateIPErr
		}
		current, err = newWorkspaceReadbackRecoveryPlan(recovered, proof, release, expectedPrivateIP)
		if err == nil {
			execution = newWorkspaceRecoveryExecution(*plan, proof, decision, reviewer)
		}
	}
	if err != nil {
		return workspaceRecoveryExecution{}, false, err
	}
	if mismatches := workspaceRecoveryPlanMismatches(*plan, current); len(mismatches) != 0 {
		plan.Status, plan.ErrorCode, plan.Mismatches = "blocked", "identity_mismatch", mismatches
		if persistErr := app.persistWorkspaceRecoveryPlanDecision(ctx, &operation, computeDecision); persistErr != nil {
			return workspaceRecoveryExecution{}, false, persistErr
		}
		return workspaceRecoveryExecution{}, false, errBillingReviewIdentity
	}
	operation.RecoveryExecution = &execution
	plan.Status = "executing"
	if err := app.persistWorkspaceRecoveryPlanDecision(ctx, &operation, computeDecision); err != nil {
		if errors.Is(err, errWorkspaceLaunchCASConflict) {
			currentOperation, found, loadErr := app.workspaceLaunchOperation(ctx, operationID)
			if loadErr == nil && found && currentOperation.RecoveryExecution != nil && currentOperation.RecoveryExecution.PlanDigest == planDigest {
				return *currentOperation.RecoveryExecution, false, nil
			}
		}
		return workspaceRecoveryExecution{}, false, err
	}
	return execution, true, nil
}

func (app *controlPlaneServer) reacquireWorkspaceRecoveryExecution(ctx context.Context, operationID, planID, planDigest, decision string) (workspaceRecoveryExecution, bool, error) {
	operation, ok, err := app.workspaceLaunchOperation(ctx, operationID)
	if err != nil || !ok || operation.RecoveryPlan == nil || operation.RecoveryExecution == nil {
		if err == nil {
			err = errBillingReviewNotFound
		}
		return workspaceRecoveryExecution{}, false, err
	}
	unlock := app.lockResource("workspace-launch", operation.AccountID)
	defer unlock()
	operation, ok, err = app.workspaceLaunchOperation(ctx, operationID)
	if err != nil || !ok || operation.RecoveryPlan == nil || operation.RecoveryExecution == nil {
		if err == nil {
			err = errBillingReviewNotFound
		}
		return workspaceRecoveryExecution{}, false, err
	}
	execution := *operation.RecoveryExecution
	readbackApprovalValid := execution.Approval != nil && execution.ApprovalDigest == execution.Approval.ApprovalDigest
	computeApprovalValid := execution.ComputeClaimRequest != nil && execution.ApprovalDigest == execution.ComputeClaimRequest.ApprovalDigest
	if operation.RecoveryPlan.PlanID != planID || operation.RecoveryPlan.PlanDigest != planDigest || execution.PlanID != planID || execution.PlanDigest != planDigest ||
		execution.Decision != decision || execution.ApprovalDigest == "" || !readbackApprovalValid && !computeApprovalValid {
		return workspaceRecoveryExecution{}, false, errBillingReviewIdentity
	}
	if execution.Status == "completed" || execution.Status == "failed" {
		return execution, false, nil
	}
	now := time.Now().UTC()
	if (execution.LeaseToken == "") != (execution.LeaseExpiresAt == "") {
		return workspaceRecoveryExecution{}, false, errBillingReviewIdentity
	}
	if execution.LeaseExpiresAt != "" {
		leaseExpiresAt, parseErr := time.Parse(time.RFC3339Nano, execution.LeaseExpiresAt)
		if parseErr != nil {
			return workspaceRecoveryExecution{}, false, errBillingReviewIdentity
		}
		if leaseExpiresAt.After(now) {
			return execution, false, nil
		}
	}
	execution.LeaseExpiresAt = now.Add(5 * time.Minute).Format(time.RFC3339Nano)
	execution.LeaseToken = workspaceRecoveryAuthorityDigest([]string{"lease", execution.ExecutionID, execution.RunIdentity, execution.LeaseExpiresAt})
	operation.RecoveryExecution = &execution
	persist := func() error {
		if operation.CurrentDecision != nil {
			return app.persistWorkspaceLaunchWithDecision(ctx, &operation, *operation.CurrentDecision)
		}
		return app.persistWorkspaceLaunch(ctx, &operation)
	}
	if err := persist(); err != nil {
		if errors.Is(err, errWorkspaceLaunchCASConflict) {
			current, found, loadErr := app.workspaceLaunchOperation(ctx, operationID)
			if loadErr == nil && found && current.RecoveryExecution != nil && current.RecoveryExecution.ExecutionID == execution.ExecutionID {
				return *current.RecoveryExecution, false, nil
			}
		}
		return workspaceRecoveryExecution{}, false, err
	}
	return execution, true, nil
}

func workspaceRecoveryExecutionErrorCode(operation workspaceLaunchOperation, err error) string {
	if operation.ErrorCode != "" {
		return operation.ErrorCode
	}
	switch {
	case errors.Is(err, errBillingReviewIdentity):
		return "identity_mismatch"
	case errors.Is(err, errBillingReviewProviderFact):
		return "provider_truth_unavailable"
	case err != nil:
		return "recovery_execution_failed"
	default:
		return ""
	}
}

func syncWorkspaceRecoveryTerminalState(operation *workspaceLaunchOperation) {
	if operation.RecoveryPlan == nil || operation.RecoveryExecution == nil || operation.RecoveryExecution.LeaseToken != "" || operation.RecoveryExecution.LeaseExpiresAt != "" ||
		operation.RecoveryExecution.Status == "completed" || operation.RecoveryExecution.Status == "failed" {
		return
	}
	execution, plan := operation.RecoveryExecution, operation.RecoveryPlan
	plan.Stages = workspaceRecoveryPlanStages(*operation)
	if plan.Action == "compute_claim_continue" {
		computeStatus := "manual_review"
		if operation.ComputeClaimProof != nil {
			computeStatus = "completed"
		}
		plan.Stages = append([]workspaceRecoveryPlanStage{{Stage: "compute_claim", Status: computeStatus}}, plan.Stages...)
	}
	plan.URL, plan.ReceiptID = operation.URL, operation.ReceiptID
	switch {
	case operation.Status == "succeeded" && operation.Phase == "succeeded" && operation.URL != "" && operation.ReceiptID != "":
		execution.Status, plan.Status, execution.ErrorCode, plan.ErrorCode = "completed", "completed", "", ""
		execution.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
	case operation.Status == "manual_review":
		code := workspaceRecoveryExecutionErrorCode(*operation, nil)
		execution.Status, plan.Status, execution.ErrorCode, plan.ErrorCode = "failed", "failed", code, code
		execution.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
}

func (app *controlPlaneServer) finalizeWorkspaceRecoveryExecution(ctx context.Context, operationID, executionID, leaseToken string, mutationOutcome workspaceRecoveryMutationOutcome, executionErr error) (workspaceRecoveryPlan, error) {
	if leaseToken == "" {
		return workspaceRecoveryPlan{}, errBillingReviewIdentity
	}
	operation, ok, err := app.workspaceLaunchOperation(ctx, operationID)
	if err != nil || !ok || operation.RecoveryPlan == nil || operation.RecoveryExecution == nil {
		if err == nil {
			err = errBillingReviewNotFound
		}
		return workspaceRecoveryPlan{}, err
	}
	unlock := app.lockResource("workspace-launch", operation.AccountID)
	defer unlock()
	operation, ok, err = app.workspaceLaunchOperation(ctx, operationID)
	if err != nil || !ok || operation.RecoveryPlan == nil || operation.RecoveryExecution == nil || operation.RecoveryExecution.ExecutionID != executionID ||
		operation.RecoveryExecution.LeaseToken != leaseToken {
		if err == nil {
			err = errBillingReviewIdentity
		}
		return workspaceRecoveryPlan{}, err
	}
	execution, plan := operation.RecoveryExecution, operation.RecoveryPlan
	execution.MutationOutcome = mutationOutcome
	plan.Stages = workspaceRecoveryPlanStages(operation)
	if plan.Action == "compute_claim_continue" {
		computeStatus := "manual_review"
		if operation.ComputeClaimProof != nil {
			computeStatus = "completed"
		}
		plan.Stages = append([]workspaceRecoveryPlanStage{{Stage: "compute_claim", Status: computeStatus}}, plan.Stages...)
	}
	plan.URL, plan.ReceiptID = operation.URL, operation.ReceiptID
	execution.LeaseToken, execution.LeaseExpiresAt = "", ""
	switch {
	case operation.Status == "succeeded" && operation.Phase == "succeeded" && operation.URL != "" && operation.ReceiptID != "":
		execution.Status, plan.Status, execution.ErrorCode, plan.ErrorCode = "completed", "completed", "", ""
		execution.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
	case operation.Status == "manual_review" || executionErr != nil:
		code := workspaceRecoveryExecutionErrorCode(operation, executionErr)
		execution.Status, plan.Status, execution.ErrorCode, plan.ErrorCode = "failed", "failed", code, code
		execution.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
	default:
		execution.Status, plan.Status = "running", "executing"
	}
	if err := app.persistWorkspaceLaunch(ctx, &operation); err != nil {
		if errors.Is(err, errWorkspaceLaunchCASConflict) {
			current, found, loadErr := app.workspaceLaunchOperation(ctx, operationID)
			if loadErr == nil && found && current.RecoveryPlan != nil && current.RecoveryExecution != nil && current.RecoveryExecution.ExecutionID == executionID {
				return workspaceRecoveryPlanProjection(current), nil
			}
		}
		return workspaceRecoveryPlan{}, err
	}
	return workspaceRecoveryPlanProjection(operation), nil
}

func (app *controlPlaneServer) executeWorkspaceRecoveryPlan(ctx context.Context, service *controlplane.Service, operationID, planID, planDigest, decision, reviewer string) (workspaceRecoveryPlan, error) {
	operation, found, err := app.workspaceLaunchOperation(ctx, operationID)
	if err != nil {
		return workspaceRecoveryPlan{}, err
	}
	var execution workspaceRecoveryExecution
	if found && operation.RecoveryExecution != nil {
		execution = *operation.RecoveryExecution
		if execution.PlanID != planID || execution.PlanDigest != planDigest || execution.Decision != decision ||
			execution.Reviewer != strings.TrimSpace(reviewer) || execution.Approval == nil && execution.ComputeClaimRequest == nil {
			return workspaceRecoveryPlan{}, errBillingReviewIdentity
		}
		if execution.Status == "completed" || execution.Status == "failed" {
			return workspaceRecoveryPlanProjection(operation), nil
		}
		var won bool
		execution, won, err = app.reacquireWorkspaceRecoveryExecution(ctx, operationID, planID, planDigest, decision)
		if err != nil {
			return workspaceRecoveryPlan{}, err
		}
		if !won {
			current, currentFound, loadErr := app.workspaceLaunchOperation(ctx, operationID)
			if loadErr != nil || !currentFound {
				return workspaceRecoveryPlan{}, errors.Join(loadErr, errBillingReviewNotFound)
			}
			return workspaceRecoveryPlanProjection(current), nil
		}
	} else {
		validated, validateErr := app.validateWorkspaceRecoveryPlan(ctx, service, operationID, planID, planDigest)
		if validateErr != nil {
			return workspaceRecoveryPlan{}, validateErr
		}
		if validated.Status != "validated" || len(validated.Mismatches) != 0 {
			return workspaceRecoveryPlan{}, errBillingReviewIdentity
		}
		var won bool
		execution, won, err = app.reserveWorkspaceRecoveryExecution(ctx, service, operationID, planID, planDigest, decision, reviewer)
		if err != nil {
			return workspaceRecoveryPlan{}, err
		}
		if !won {
			current, currentFound, loadErr := app.workspaceLaunchOperation(ctx, operationID)
			if loadErr != nil || !currentFound {
				return workspaceRecoveryPlan{}, errors.Join(loadErr, errBillingReviewNotFound)
			}
			return workspaceRecoveryPlanProjection(current), nil
		}
	}
	var executionErr error
	mutationOutcome := workspaceRecoveryMutationOutcome{Status: "unknown", Source: "recovery_execution"}
	if execution.ComputeClaimRequest != nil {
		claimProof, claimErr := app.claimWorkspaceCompute(ctx, service, *execution.ComputeClaimRequest, execution.ExecutionID)
		mutationOutcome = workspaceRecoveryMutationOutcomeFromComputeClaim(claimProof)
		executionErr = claimErr
		if executionErr == nil {
			current, currentFound, loadErr := app.workspaceLaunchOperation(ctx, operationID)
			if loadErr != nil || !currentFound {
				executionErr = errors.Join(loadErr, errBillingReviewNotFound)
			} else if current.Status == "preparing" && current.Phase == "storage_fulfilling" {
				executionErr = app.fulfillWorkspaceLaunch(ctx, service, &current)
			}
		}
	} else if execution.Approval != nil {
		_, _, executionErr = app.recoverWorkspaceLaunchReviewWithReplay(ctx, service, billingReviewResolutionInput{
			ResourceType: "workspace_launch", ResourceID: operationID, AccountID: execution.Approval.Customer.AccountID,
			BillingOperationID: operationID, EvidenceRef: "recovery-plan:" + planID, IdempotencyKey: execution.ExecutionID,
			Reviewer: reviewer, ReadbackApproval: execution.Approval,
		})
	} else {
		return workspaceRecoveryPlan{}, errBillingReviewIdentity
	}
	return app.finalizeWorkspaceRecoveryExecution(ctx, operationID, execution.ExecutionID, execution.LeaseToken, mutationOutcome, executionErr)
}
