package server

import "testing"

func TestReduceLaunchStageKeepsComputeFirstWhenStorageIsUnknown(t *testing.T) {
	decision := ReduceLaunchStage(EvidenceSnapshot{
		Debit: StageEvidence{State: EvidencePresent, Confirmed: true},
		ComputeClaim: StageEvidence{
			State:     EvidencePresent,
			Expected:  "target_owned",
			Actual:    "unallocated",
			Authority: "provider.nodeOwnership",
		},
		Storage: StageEvidence{
			State:     EvidenceUnavailable,
			Expected:  "confirmed",
			Actual:    "attempted_unknown",
			Authority: "provider.storage",
		},
	})

	if decision.CurrentStage != "compute_claim" || decision.StageState != "pending" ||
		decision.FirstFalsePredicate != "provider.nodeOwnership" || decision.Expected != "target_owned" ||
		decision.Actual != "unallocated" || decision.NextAction != "NODE_ONLY_CONTINUATION_ONCE" ||
		decision.AllowedMutation != "node_only_continuation" || decision.RequiresApproval {
		t.Fatalf("storage unknown masked compute decision: %#v", decision)
	}
}

func TestReduceLaunchStageOnlyEntersStorageAfterTargetOwned(t *testing.T) {
	decision := ReduceLaunchStage(EvidenceSnapshot{
		Debit:        StageEvidence{State: EvidencePresent, Confirmed: true},
		ComputeClaim: StageEvidence{State: EvidencePresent, Expected: "target_owned", Actual: "target_owned", Authority: "provider.nodeOwnership"},
		Storage:      StageEvidence{State: EvidenceUnavailable, Expected: "confirmed", Actual: "attempted_unknown", Authority: "provider.storage"},
	})

	if decision.CurrentStage != "storage" || decision.StageState != "unknown" ||
		decision.FirstFalsePredicate != "provider.storage" || decision.NextAction != "GET_ONLY_RECONCILE_STORAGE" ||
		decision.AllowedMutation != "none" || !decision.RequiresApproval {
		t.Fatalf("storage decision did not follow confirmed compute: %#v", decision)
	}
}

func TestCurrentDecisionDoesNotAuthorizeNodeMutationWithoutProviderProof(t *testing.T) {
	operation := workspaceLaunchOperation{
		ID:     "workspace-launch-proof-required",
		Status: "compute_claim_pending",
		Phase:  "compute_claim_pending",
		ContinuationAttemptBudgets: map[string]workspaceLaunchStageBudget{
			"storage": {Attempted: 1, Unknown: 1, Max: 1},
		},
	}

	decision := currentDecisionForWorkspaceLaunch(operation)

	if decision.CurrentStage != "compute_claim" || decision.StageState != "unknown" ||
		decision.FirstFalsePredicate != "provider.nodeOwnership" || decision.Expected != "target_owned" ||
		decision.Actual != "unknown" || decision.NextAction != "MANUAL_REVIEW" ||
		decision.AllowedMutation != "none" || !decision.RequiresApproval ||
		AuthorizeStageMutation(decision, "node_only_continuation") {
		t.Fatalf("phase-only state authorized Node mutation: %#v", decision)
	}
}

func TestCurrentDecisionDoesNotAuthorizeNodeOnlyContinuationForRecoverableCVM(t *testing.T) {
	operation := workspaceLaunchOperation{
		ID:     "workspace-launch-cvm-target-owned-required",
		Status: "compute_claim_pending",
		Phase:  "compute_claim_pending",
		ContinuationAttemptBudgets: map[string]workspaceLaunchStageBudget{
			"storage": {Attempted: 1, Unknown: 1, Max: 1},
		},
	}
	proof := computeClaimRecoveryProofForLaunchStorage(operation, "unallocated", "storage_attempt_unknown", "")
	proof.CVMOwnershipState = "recoverable"

	decision := currentDecisionForComputeClaimProof(operation, proof, nil)

	if decision.CurrentStage != "compute_claim" || decision.StageState != "unknown" ||
		decision.FirstFalsePredicate != "provider.cvmOwnership" || decision.Expected != "target_owned" ||
		decision.Actual != "recoverable" || decision.NextAction != "MANUAL_REVIEW" ||
		decision.AllowedMutation != "none" || !decision.RequiresApproval ||
		AuthorizeStageMutation(decision, "node_only_continuation") {
		t.Fatalf("recoverable CVM authorized Node-only continuation: %#v", decision)
	}
}

func TestP0ReducerDoesNotAuthorizeMutationOutsideComputeClaim(t *testing.T) {
	tests := []struct {
		name     string
		snapshot EvidenceSnapshot
		mutation string
		stage    string
		action   string
	}{
		{
			name: "debit",
			snapshot: EvidenceSnapshot{
				Debit: StageEvidence{State: EvidencePresent},
			},
			mutation: "continue_debit",
			stage:    "debit",
			action:   "CONTINUE_ORIGINAL_LAUNCH",
		},
		{
			name: "storage",
			snapshot: EvidenceSnapshot{
				Debit:        StageEvidence{State: EvidencePresent, Confirmed: true},
				ComputeClaim: StageEvidence{State: EvidencePresent, Confirmed: true, Actual: "target_owned"},
				Storage:      StageEvidence{State: EvidenceAbsent, Actual: "authoritative_absent"},
			},
			mutation: "continue_storage",
			stage:    "storage",
			action:   "RESUME_EXISTING_STORAGE",
		},
		{
			name: "secret",
			snapshot: EvidenceSnapshot{
				Debit:        StageEvidence{State: EvidencePresent, Confirmed: true},
				ComputeClaim: StageEvidence{State: EvidencePresent, Confirmed: true, Actual: "target_owned"},
				Storage:      StageEvidence{State: EvidencePresent, Confirmed: true},
				Attachment:   StageEvidence{State: EvidencePresent, Confirmed: true},
				Secret:       StageEvidence{State: EvidencePresent},
			},
			mutation: "continue_secret",
			stage:    "secret",
			action:   "CONTINUE_ORIGINAL_LAUNCH",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision := ReduceLaunchStage(test.snapshot)
			if decision.CurrentStage != test.stage || decision.NextAction != test.action ||
				decision.AllowedMutation != "none" || decision.MutationState != "none" ||
				AuthorizeStageMutation(decision, test.mutation) {
				t.Fatalf("non-Compute stage received P0 mutation authority: %#v", decision)
			}
		})
	}
}
