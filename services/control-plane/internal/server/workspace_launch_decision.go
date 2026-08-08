package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"opl-cloud/services/control-plane/internal/clients"
)

// EvidenceState is deliberately per-source. A failed Storage read must not
// turn an already-read Compute or Node fact into unknown.
type EvidenceState string

const (
	EvidencePresent     EvidenceState = "present"
	EvidenceAbsent      EvidenceState = "absent"
	EvidenceUnavailable EvidenceState = "unavailable"
	EvidenceConflict    EvidenceState = "conflict"
)

type StageEvidence struct {
	State     EvidenceState `json:"state"`
	Confirmed bool          `json:"confirmed"`
	Expected  string        `json:"expected,omitempty"`
	Actual    string        `json:"actual,omitempty"`
	Authority string        `json:"authority,omitempty"`
}

// EvidenceSnapshot is the Compute Claim decision input. It contains only
// normalized facts; network access and mutation are owned by callers.
type EvidenceSnapshot struct {
	Debit        StageEvidence `json:"debit"`
	ComputeClaim StageEvidence `json:"computeClaim"`
	Storage      StageEvidence `json:"storage"`
	Attachment   StageEvidence `json:"attachment"`
	Secret       StageEvidence `json:"secret"`
	Runtime      StageEvidence `json:"runtime"`
	Activation   StageEvidence `json:"activation"`
	Receipt      StageEvidence `json:"receipt"`
}

// CurrentDecision is the single persisted business snapshot. Attempt counters
// stay in the stage-attempt ledger (ContinuationAttemptBudgets), never here.
type CurrentDecision struct {
	CurrentStage        string `json:"currentStage"`
	StageState          string `json:"stageState"`
	FirstFalsePredicate string `json:"firstFalsePredicate,omitempty"`
	Expected            string `json:"expected,omitempty"`
	Actual              string `json:"actual,omitempty"`
	Authority           string `json:"authority,omitempty"`
	NextAction          string `json:"nextAction"`
	RequiresApproval    bool   `json:"requiresApproval"`
	AllowedMutation     string `json:"allowedMutation"`
	StageAttemptID      string `json:"stageAttemptId,omitempty"`
	MutationState       string `json:"mutationState"`
	EvidenceDigest      string `json:"evidenceDigest"`
	DecisionVersion     int64  `json:"decisionVersion"`
}

const (
	nextActionGetOnlyReconcileStorage = "GET_ONLY_RECONCILE_STORAGE"
	nextActionNodeOnlyContinuation    = "NODE_ONLY_CONTINUATION_ONCE"
	nextActionResumeExistingStorage   = "RESUME_EXISTING_STORAGE"
	nextActionContinueOriginalLaunch  = "CONTINUE_ORIGINAL_LAUNCH"
	nextActionManualReview            = "MANUAL_REVIEW"
	nextActionNone                    = "NONE"
)

// ReduceLaunchStage is pure: it only orders already-normalized facts and
// never calls a provider or authorizes a write on its own.
func ReduceLaunchStage(snapshot EvidenceSnapshot) CurrentDecision {
	decision := CurrentDecision{MutationState: "none", AllowedMutation: "none"}
	if !snapshot.Debit.Confirmed {
		return decisionForEvidence(&decision, "debit", snapshot.Debit, "provider.debit", "confirmed", "debit")
	}

	compute := snapshot.ComputeClaim
	computeConfirmed := compute.Confirmed || strings.TrimSpace(compute.Actual) == "target_owned"
	if !computeConfirmed {
		if compute.State == EvidenceConflict || compute.State == EvidenceUnavailable || strings.TrimSpace(compute.Actual) == "unknown" {
			return decisionForEvidence(&decision, "compute_claim", compute, firstNonEmptyDecision(compute.Authority, "provider.nodeOwnership"), "target_owned", "unknown")
		}
		decision.CurrentStage = "compute_claim"
		decision.StageState = "pending"
		decision.FirstFalsePredicate = firstNonEmptyDecision(compute.Authority, "provider.nodeOwnership")
		decision.Expected = firstNonEmptyDecision(compute.Expected, "target_owned")
		decision.Actual = firstNonEmptyDecision(compute.Actual, "unallocated")
		decision.Authority = firstNonEmptyDecision(compute.Authority, "provider.nodeOwnership")
		decision.NextAction = nextActionNodeOnlyContinuation
		decision.AllowedMutation = "node_only_continuation"
		decision.MutationState = "pending"
		return decision
	}

	storage := snapshot.Storage
	if !storage.Confirmed {
		return decisionForEvidence(&decision, "storage", storage, firstNonEmptyDecision(storage.Authority, "provider.storage"), "confirmed", firstNonEmptyDecision(storage.Actual, "attempted_unknown"))
	}
	for _, stage := range []struct {
		name      string
		evidence  StageEvidence
		authority string
	}{
		{name: "attachment", evidence: snapshot.Attachment, authority: "fabric.attachment"},
		{name: "secret", evidence: snapshot.Secret, authority: "fabric.gatewaySecret"},
		{name: "runtime", evidence: snapshot.Runtime, authority: "fabric.runtime"},
		{name: "activation", evidence: snapshot.Activation, authority: "controlPlane.activation"},
		{name: "receipt", evidence: snapshot.Receipt, authority: "ledger.receipt"},
	} {
		if !stage.evidence.Confirmed {
			return decisionForEvidence(&decision, stage.name, stage.evidence, stage.authority, "confirmed", "pending")
		}
	}
	decision.CurrentStage, decision.StageState, decision.NextAction = "succeeded", "confirmed", nextActionNone
	return decision
}

func decisionForEvidence(decision *CurrentDecision, stage string, evidence StageEvidence, predicate, expected, fallbackActual string) CurrentDecision {
	decision.CurrentStage = stage
	decision.Authority = firstNonEmptyDecision(evidence.Authority, predicate)
	decision.Expected = firstNonEmptyDecision(evidence.Expected, expected)
	decision.Actual = firstNonEmptyDecision(evidence.Actual, fallbackActual)
	decision.FirstFalsePredicate = decision.Authority
	if stage == "storage" && evidence.Actual == "attempted_unknown" {
		decision.StageState, decision.NextAction, decision.RequiresApproval, decision.MutationState = "unknown", nextActionGetOnlyReconcileStorage, true, "frozen"
		return *decision
	}
	switch evidence.State {
	case EvidenceUnavailable, EvidenceConflict:
		decision.StageState, decision.NextAction, decision.RequiresApproval, decision.MutationState = "unknown", nextActionManualReview, true, "frozen"
	default:
		decision.StageState, decision.MutationState = "pending", "none"
		if stage == "storage" {
			decision.NextAction = nextActionResumeExistingStorage
		} else {
			decision.NextAction = nextActionContinueOriginalLaunch
		}
	}
	return *decision
}

// AuthorizeStageMutation consumes an already persisted CurrentDecision. It is
// intentionally incapable of deriving a decision from raw provider evidence.
func AuthorizeStageMutation(decision CurrentDecision, mutation string) bool {
	return decision.CurrentStage == "compute_claim" && strings.TrimSpace(mutation) == "node_only_continuation" &&
		!decision.RequiresApproval && decision.StageState == "pending" && decision.MutationState == "pending" &&
		decision.AllowedMutation == "node_only_continuation"
}

func firstNonEmptyDecision(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func digestLaunchEvidence(snapshot EvidenceSnapshot) string {
	encoded, _ := json.Marshal(snapshot)
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func workspaceLaunchEvidenceSnapshot(operation workspaceLaunchOperation) EvidenceSnapshot {
	snapshot := EvidenceSnapshot{
		Debit:        StageEvidence{State: EvidencePresent, Confirmed: operation.Phase != "debit_pending" && operation.Phase != "key_pending", Authority: "controlPlane.debit"},
		ComputeClaim: StageEvidence{State: EvidenceUnavailable, Actual: "unknown", Authority: "provider.nodeOwnership", Expected: "target_owned"},
		Storage:      StageEvidence{State: EvidencePresent, Authority: "provider.storage", Expected: "confirmed"},
		Attachment:   stageEvidenceForWorkspaceLaunch(operation, "attachment", "fabric.attachment"),
		Secret:       stageEvidenceForWorkspaceLaunch(operation, "secret", "fabric.gatewaySecret"),
		Runtime:      stageEvidenceForWorkspaceLaunch(operation, "runtime", "fabric.runtime"),
		Activation:   stageEvidenceForWorkspaceLaunch(operation, "activation", "controlPlane.activation"),
		Receipt:      stageEvidenceForWorkspaceLaunch(operation, "receipt", "ledger.receipt"),
	}
	if operation.ComputeClaimProof != nil {
		snapshot.ComputeClaim.Actual = operation.ComputeClaimProof.NodeOwnershipState
		snapshot.ComputeClaim.State = EvidencePresent
		snapshot.ComputeClaim.Confirmed = operation.ComputeClaimProof.NodeOwnershipState == "target_owned" && operation.Phase != "compute_claim_pending"
	} else if state := operation.ComputeClaimTerminalEvidenceNodeState(); state != "" {
		snapshot.ComputeClaim.State, snapshot.ComputeClaim.Actual = EvidencePresent, state
	} else if workspaceLaunchPhaseAfterComputeClaim(operation.Phase) {
		snapshot.ComputeClaim.State, snapshot.ComputeClaim.Confirmed, snapshot.ComputeClaim.Actual = EvidencePresent, true, "target_owned"
	}
	storageBudget := operation.ContinuationAttemptBudgets["storage"]
	if storageBudget.Unknown > 0 {
		snapshot.Storage.State, snapshot.Storage.Actual = EvidenceUnavailable, "attempted_unknown"
	} else if storageBudget.Confirmed > 0 || operation.Phase == "attaching" || operation.Phase == "secret_writing" || operation.Phase == "runtime_starting" || operation.Phase == "activating" || operation.Phase == "receipt_pending" || operation.Phase == "succeeded" {
		snapshot.Storage.Confirmed, snapshot.Storage.Actual = true, "confirmed"
	}
	return snapshot
}

func workspaceLaunchEvidenceSnapshotWithComputeProof(operation workspaceLaunchOperation, proof clients.ComputeClaimRecoveryProof, proofErr error) EvidenceSnapshot {
	snapshot := workspaceLaunchEvidenceSnapshot(operation)
	snapshot.ComputeClaim.Confirmed = false
	switch proof.NodeOwnershipState {
	case "target_owned":
		snapshot.ComputeClaim.State, snapshot.ComputeClaim.Confirmed, snapshot.ComputeClaim.Actual = EvidencePresent, true, "target_owned"
	case "unallocated":
		snapshot.ComputeClaim.State, snapshot.ComputeClaim.Actual = EvidencePresent, "unallocated"
	case "conflict":
		snapshot.ComputeClaim.State, snapshot.ComputeClaim.Actual = EvidenceConflict, "conflict"
	default:
		snapshot.ComputeClaim.State, snapshot.ComputeClaim.Actual = EvidenceUnavailable, "unknown"
	}
	if proofErr != nil && proof.NodeOwnershipState != "target_owned" && proof.NodeOwnershipState != "unallocated" {
		snapshot.ComputeClaim.State, snapshot.ComputeClaim.Actual = EvidenceUnavailable, "unknown"
	}
	switch proof.StorageState {
	case "storage_not_started":
		snapshot.Storage = StageEvidence{State: EvidenceAbsent, Expected: "confirmed", Actual: "authoritative_absent", Authority: "provider.storage"}
	case "storage_existing_exact":
		snapshot.Storage = StageEvidence{State: EvidencePresent, Expected: "confirmed", Actual: "exact_existing", Authority: "provider.storage"}
	case "storage_attempt_unknown", "unknown":
		snapshot.Storage = StageEvidence{State: EvidenceUnavailable, Expected: "confirmed", Actual: "attempted_unknown", Authority: "provider.storage"}
	}
	return snapshot
}

func currentDecisionForEvidence(operation workspaceLaunchOperation, snapshot EvidenceSnapshot) CurrentDecision {
	decision := ReduceLaunchStage(snapshot)
	decision.EvidenceDigest = digestLaunchEvidence(snapshot)
	decision.StageAttemptID = operation.ID + ":" + decision.CurrentStage
	if sameCurrentDecisionFacts(operation.CurrentDecision, decision) {
		decision.DecisionVersion = operation.CurrentDecision.DecisionVersion
	} else if operation.CurrentDecision != nil && operation.CurrentDecision.DecisionVersion > 0 {
		decision.DecisionVersion = operation.CurrentDecision.DecisionVersion + 1
	} else {
		decision.DecisionVersion = 1
	}
	return decision
}

func sameCurrentDecisionFacts(got *CurrentDecision, want CurrentDecision) bool {
	return got != nil && got.CurrentStage == want.CurrentStage && got.StageState == want.StageState &&
		got.FirstFalsePredicate == want.FirstFalsePredicate && got.Expected == want.Expected && got.Actual == want.Actual &&
		got.Authority == want.Authority && got.NextAction == want.NextAction && got.RequiresApproval == want.RequiresApproval &&
		got.AllowedMutation == want.AllowedMutation && got.StageAttemptID == want.StageAttemptID &&
		got.MutationState == want.MutationState && got.EvidenceDigest == want.EvidenceDigest
}

func currentDecisionForWorkspaceLaunch(operation workspaceLaunchOperation) CurrentDecision {
	return currentDecisionForEvidence(operation, workspaceLaunchEvidenceSnapshot(operation))
}

func currentDecisionForComputeClaimProof(operation workspaceLaunchOperation, proof clients.ComputeClaimRecoveryProof, proofErr error) CurrentDecision {
	return currentDecisionForEvidence(operation, workspaceLaunchEvidenceSnapshotWithComputeProof(operation, proof, proofErr))
}

func sameCurrentDecisionAuthority(got *CurrentDecision, want CurrentDecision) bool {
	return got != nil && got.CurrentStage == want.CurrentStage && got.StageState == want.StageState &&
		got.FirstFalsePredicate == want.FirstFalsePredicate && got.Expected == want.Expected && got.Actual == want.Actual &&
		got.Authority == want.Authority && got.NextAction == want.NextAction && got.RequiresApproval == want.RequiresApproval &&
		got.AllowedMutation == want.AllowedMutation && got.StageAttemptID == want.StageAttemptID &&
		got.MutationState == want.MutationState && got.EvidenceDigest == want.EvidenceDigest && got.DecisionVersion == want.DecisionVersion
}

func workspaceLaunchPhaseAfterComputeClaim(phase string) bool {
	switch phase {
	case "storage_fulfilling", "attaching", "secret_writing", "runtime_starting", "activating", "receipt_pending", "succeeded":
		return true
	default:
		return false
	}
}

func stageEvidenceForWorkspaceLaunch(operation workspaceLaunchOperation, stage, authority string) StageEvidence {
	budget := operation.ContinuationAttemptBudgets[stage]
	evidence := StageEvidence{State: EvidencePresent, Confirmed: budget.Confirmed > 0, Expected: "confirmed", Actual: "pending", Authority: authority}
	if budget.Unknown > 0 {
		evidence.State, evidence.Actual = EvidenceUnavailable, "attempted_unknown"
	} else if evidence.Confirmed {
		evidence.Actual = "confirmed"
	}
	if operation.Phase == "succeeded" {
		evidence.Confirmed, evidence.Actual = true, "confirmed"
	}
	return evidence
}

// ComputeClaimTerminalEvidenceNodeState is kept as a tiny compatibility seam
// for old terminal records; it never infers provider truth.
func (operation workspaceLaunchOperation) ComputeClaimTerminalEvidenceNodeState() string {
	if operation.ComputeClaimTerminalEvidence == nil {
		return ""
	}
	return operation.ComputeClaimTerminalEvidence.NodeOwnershipState
}
