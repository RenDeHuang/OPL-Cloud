package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

type workspaceRecoveryPlanFabric struct {
	*monthlyFabric
	identityEvidence *clients.ComputeClaimIdentityEvidence
}

func (f *workspaceRecoveryPlanFabric) ComputeClaimRecoveryIdentityEvidence(_ context.Context, _ clients.ComputeClaimRecoveryClaimInput) (*clients.ComputeClaimIdentityEvidence, error) {
	return f.identityEvidence, nil
}

func useWorkspaceRecoveryPlanIdentityEvidence(t *testing.T, fixture *workspaceLaunchWorkerFixture, evidence *clients.ComputeClaimIdentityEvidence) {
	t.Helper()
	fixture.service = controlplane.NewService(fixture.ledger, &workspaceRecoveryPlanFabric{
		monthlyFabric: fixture.fabric, identityEvidence: evidence,
	}, fixture.sub2API)
	server, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)
}

func recoverableCVMOnlyIdentityEvidence() clients.ComputeClaimIdentityEvidence {
	fields := []string{
		"fabric.operationId", "fabric.operationIdempotencyKey", "fabric.operationRequestHash",
		"binding.present", "binding.valid", "binding.compatibility", "binding.launchOperationId",
		"binding.idempotencyKey", "binding.targetHash", "binding.requestHash",
	}
	checks := make([]clients.ComputeClaimIdentityCheck, 0, len(fields))
	for _, field := range fields {
		checks = append(checks, clients.ComputeClaimIdentityCheck{Field: field, Matches: true})
	}
	return clients.ComputeClaimIdentityEvidence{
		Checks: checks, BindingClassification: "current", BindingDigest: strings.Repeat("b", 64),
		MutationLedger: "observed", MutationLedgerOutcome: "nonzero", MutationLedgerDigest: strings.Repeat("d", 64),
		MutationEvidence: &clients.ComputeClaimEvidence{
			CVM: clients.ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1},
		},
		FailureStage: "cvm_tag_readback", ProviderErrorClass: "readback_mismatch",
	}
}

func requestHashReconciliationIdentityEvidence() clients.ComputeClaimIdentityEvidence {
	fields := []string{
		"fabric.operationId", "fabric.operationIdempotencyKey", "fabric.operationRequestHash",
		"binding.present", "binding.valid", "binding.compatibility", "binding.launchOperationId",
		"binding.idempotencyKey", "binding.targetHash", "binding.requestHash",
	}
	checks := make([]clients.ComputeClaimIdentityCheck, 0, len(fields))
	for _, field := range fields {
		check := clients.ComputeClaimIdentityCheck{Field: field, Matches: true, Expected: "exact", Actual: "exact"}
		if field == "binding.requestHash" {
			check = clients.ComputeClaimIdentityCheck{
				Field: field, Matches: false, ExpectedDigest: strings.Repeat("a", 64), ActualDigest: strings.Repeat("b", 64),
			}
		}
		checks = append(checks, check)
	}
	return clients.ComputeClaimIdentityEvidence{
		Checks: checks, BindingClassification: "request-hash-reconciliation", BindingDigest: strings.Repeat("c", 64),
		MutationLedger: "observed", MutationLedgerOutcome: "unknown", MutationLedgerDigest: strings.Repeat("d", 64),
		MutationEvidence: &clients.ComputeClaimEvidence{
			CVM: clients.ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"opl_account_id"}},
		},
		FailureStage: "cvm_tag_readback", ProviderErrorClass: "provider_error",
	}
}

func terminalRequestHashReconciliationIdentityEvidence() clients.ComputeClaimIdentityEvidence {
	evidence := requestHashReconciliationIdentityEvidence()
	evidence.MutationLedger = "absent"
	evidence.MutationLedgerOutcome = "confirmed_zero"
	evidence.MutationLedgerDigest = "5ad38304b535c2987dbd24657c1a11b884984ff600d9f389deb0d4e634fee792"
	evidence.MutationEvidence = nil
	evidence.FailureStage = ""
	evidence.ProviderErrorClass = ""
	return evidence
}

func legacyKubectlClientRejectedIdentityEvidence() clients.ComputeClaimIdentityEvidence {
	evidence := terminalRequestHashReconciliationIdentityEvidence()
	evidence.Reconciliation = &clients.ComputeClaimReconciliationEvidence{
		SchemaVersion: 2, Consumer: "claim_compute_recovery", Generation: "normal_launch_terminal_evidence_v1",
		ProvenanceSource: "normal_launch_terminal_evidence", ProvenanceDigest: strings.Repeat("e", 64), State: "observed",
		ExpectedRequestHashDigest: evidence.Checks[9].ExpectedDigest, PersistedRequestHashDigest: evidence.Checks[9].ActualDigest,
		FailureStage: "node_patch_readback", ProviderErrorClass: "provider_error",
		Node: clients.ComputeClaimMutationEvidence{Attempted: 1, Missing: []string{"node_ownership"}},
	}
	return evidence
}

func TestWorkspaceRecoveryPlanDiagnoseAdmitsOnlyFabricRequestHashReconciliationCandidateWithoutMutation(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")
	evidence := requestHashReconciliationIdentityEvidence()
	useWorkspaceRecoveryPlanIdentityEvidence(t, &fixture, &evidence)

	response := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID})
	plan := recoveryPlanResponse(t, response)
	persisted := fixture.operation(t)
	if response.Code != http.StatusOK || plan.Status != "diagnosed" || len(plan.Mismatches) != 0 ||
		persisted.RecoveryPlan == nil || persisted.RecoveryPlan.Status != "diagnosed" || len(fixture.fabric.computeClaimCalls) != 0 ||
		len(fixture.fabric.storageIDs) != 0 || len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeIDs) != 1 {
		t.Fatalf("status=%d plan=%#v operation=%#v claims=%d storage=%d charges=%d computes=%d", response.Code, plan, persisted,
			len(fixture.fabric.computeClaimCalls), len(fixture.fabric.storageIDs), len(fixture.sub2API.charges), len(fixture.fabric.computeIDs))
	}
}

func TestWorkspaceRecoveryPlanDiagnoseAdmitsFabricTerminalRequestHashReconciliationCandidateWithoutMutation(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")
	evidence := terminalRequestHashReconciliationIdentityEvidence()
	useWorkspaceRecoveryPlanIdentityEvidence(t, &fixture, &evidence)

	response := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID})
	plan := recoveryPlanResponse(t, response)
	persisted := fixture.operation(t)
	if response.Code != http.StatusOK || plan.Status != "diagnosed" || len(plan.Mismatches) != 0 ||
		persisted.RecoveryPlan == nil || persisted.RecoveryPlan.Status != "diagnosed" || len(fixture.fabric.computeClaimCalls) != 0 ||
		len(fixture.fabric.storageIDs) != 0 || len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeIDs) != 1 {
		t.Fatalf("status=%d plan=%#v operation=%#v claims=%d storage=%d charges=%d computes=%d", response.Code, plan, persisted,
			len(fixture.fabric.computeClaimCalls), len(fixture.fabric.storageIDs), len(fixture.sub2API.charges), len(fixture.fabric.computeIDs))
	}
}

func TestWorkspaceRecoveryPlanTerminalRequestHashReconciliationRejectsDriftWithoutMutation(t *testing.T) {
	tests := map[string]func(*clients.ComputeClaimIdentityEvidence){
		"ledger outcome": func(evidence *clients.ComputeClaimIdentityEvidence) {
			evidence.MutationLedgerOutcome = "unknown"
		},
		"ledger digest": func(evidence *clients.ComputeClaimIdentityEvidence) {
			evidence.MutationLedgerDigest = strings.Repeat("f", 64)
		},
		"mutation evidence": func(evidence *clients.ComputeClaimIdentityEvidence) {
			evidence.MutationEvidence = &clients.ComputeClaimEvidence{}
		},
		"failure stage": func(evidence *clients.ComputeClaimIdentityEvidence) {
			evidence.FailureStage = "cvm_tag_readback"
		},
		"provider class": func(evidence *clients.ComputeClaimIdentityEvidence) {
			evidence.ProviderErrorClass = "provider_error"
		},
		"target mismatch": func(evidence *clients.ComputeClaimIdentityEvidence) {
			evidence.Checks[8].Matches = false
			evidence.Checks[8].ExpectedDigest = strings.Repeat("e", 64)
			evidence.Checks[8].ActualDigest = strings.Repeat("f", 64)
		},
		"check missing": func(evidence *clients.ComputeClaimIdentityEvidence) {
			evidence.Checks = evidence.Checks[:len(evidence.Checks)-1]
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
			t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
			fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
			fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")
			evidence := terminalRequestHashReconciliationIdentityEvidence()
			mutate(&evidence)
			useWorkspaceRecoveryPlanIdentityEvidence(t, &fixture, &evidence)

			response := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID})
			plan := recoveryPlanResponse(t, response)
			if response.Code != http.StatusOK || plan.Status != "blocked" || len(plan.Mismatches) == 0 || len(fixture.fabric.computeClaimCalls) != 0 ||
				len(fixture.fabric.storageIDs) != 0 || len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeIDs) != 1 {
				t.Fatalf("status=%d plan=%#v claims=%d storage=%d charges=%d computes=%d", response.Code, plan,
					len(fixture.fabric.computeClaimCalls), len(fixture.fabric.storageIDs), len(fixture.sub2API.charges), len(fixture.fabric.computeIDs))
			}
		})
	}
}

func TestWorkspaceRecoveryPlanRequestHashReconciliationRejectsAnyAdditionalMismatchWithoutMutation(t *testing.T) {
	tests := map[string]func(*clients.ComputeClaimIdentityEvidence){
		"wrong missing": func(evidence *clients.ComputeClaimIdentityEvidence) {
			evidence.MutationEvidence.CVM.Missing = []string{"opl_account_id", "opl_workspace_id"}
		},
		"wrong failure stage": func(evidence *clients.ComputeClaimIdentityEvidence) {
			evidence.FailureStage = "cvm_final_readback"
		},
		"wrong provider class": func(evidence *clients.ComputeClaimIdentityEvidence) {
			evidence.ProviderErrorClass = "readback_mismatch"
		},
		"target mismatch": func(evidence *clients.ComputeClaimIdentityEvidence) {
			evidence.Checks[8].Matches = false
			evidence.Checks[8].ExpectedDigest = strings.Repeat("e", 64)
			evidence.Checks[8].ActualDigest = strings.Repeat("f", 64)
		},
		"check order": func(evidence *clients.ComputeClaimIdentityEvidence) {
			evidence.Checks[7], evidence.Checks[8] = evidence.Checks[8], evidence.Checks[7]
		},
		"check missing": func(evidence *clients.ComputeClaimIdentityEvidence) {
			evidence.Checks = evidence.Checks[:len(evidence.Checks)-1]
		},
		"invalid digest": func(evidence *clients.ComputeClaimIdentityEvidence) {
			evidence.Checks[9].ActualDigest = "invalid"
		},
		"equal digest": func(evidence *clients.ComputeClaimIdentityEvidence) {
			evidence.Checks[9].ActualDigest = evidence.Checks[9].ExpectedDigest
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
			t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
			fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
			fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")
			evidence := requestHashReconciliationIdentityEvidence()
			mutate(&evidence)
			useWorkspaceRecoveryPlanIdentityEvidence(t, &fixture, &evidence)

			response := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID})
			plan := recoveryPlanResponse(t, response)
			if response.Code != http.StatusOK || plan.Status != "blocked" || len(plan.Mismatches) == 0 || len(fixture.fabric.computeClaimCalls) != 0 ||
				len(fixture.fabric.storageIDs) != 0 || len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeIDs) != 1 {
				t.Fatalf("status=%d plan=%#v claims=%d storage=%d charges=%d computes=%d", response.Code, plan,
					len(fixture.fabric.computeClaimCalls), len(fixture.fabric.storageIDs), len(fixture.sub2API.charges), len(fixture.fabric.computeIDs))
			}
		})
	}
}

func TestWorkspaceRecoveryPlanRequestHashReconciliationExecutesOriginalLaunchOnce(t *testing.T) {
	assertWorkspaceRecoveryPlanRequestHashReconciliationExecutesOriginalLaunchOnce(t, requestHashReconciliationIdentityEvidence())
}

func TestWorkspaceRecoveryPlanTerminalRequestHashReconciliationExecutesOriginalLaunchOnce(t *testing.T) {
	assertWorkspaceRecoveryPlanRequestHashReconciliationExecutesOriginalLaunchOnce(t, terminalRequestHashReconciliationIdentityEvidence())
}

func TestWorkspaceRecoveryPlanProjectsPersistedRequestHashReconciliationFailureEvidence(t *testing.T) {
	evidence := requestHashReconciliationIdentityEvidence()
	evidence.Reconciliation = &clients.ComputeClaimReconciliationEvidence{
		SchemaVersion: 1, Consumer: "claim_compute_recovery", Generation: "isolated_request_hash_v1", State: "node_reserved",
		ExpectedRequestHashDigest: evidence.Checks[9].ExpectedDigest, PersistedRequestHashDigest: evidence.Checks[9].ActualDigest,
		FailureStage: "node_patch_readback", ProviderErrorClass: "transport_error",
		Node: clients.ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"node_ownership"}},
	}
	proof := clients.ComputeClaimRecoveryProof{
		IdentityEvidence: &evidence, FailureStage: "node_patch_readback", ProviderErrorClass: "transport_error",
		Evidence: &clients.ComputeClaimEvidence{
			CVM:  clients.ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"opl_account_id"}},
			Node: clients.ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"node_ownership"}},
		},
	}
	outcome := workspaceRecoveryMutationOutcomeFromComputeClaim(proof)
	got := outcome.ComputeClaimEvidence
	if got == nil || got.BindingClassification != "request-hash-reconciliation" || got.MismatchField != "binding.requestHash" ||
		got.ExpectedDigest != evidence.Checks[9].ExpectedDigest || got.ActualDigest != evidence.Checks[9].ActualDigest ||
		got.MutationLedger != "observed" || got.MutationLedgerOutcome != "unknown" || got.FailureStage != "node_patch_readback" ||
		got.ProviderErrorClass != "transport_error" || got.Reconciliation == nil || got.Reconciliation.State != "node_reserved" ||
		got.Reconciliation.Consumer != "claim_compute_recovery" || got.Reconciliation.Generation != "isolated_request_hash_v1" {
		t.Fatalf("projected reconciliation evidence=%#v", got)
	}
	serialized, err := json.Marshal(got)
	if err != nil || bytes.Contains(serialized, []byte("acct-")) || bytes.Contains(serialized, []byte("workspace-launch-")) ||
		bytes.Contains(serialized, []byte("ins-")) || bytes.Contains(serialized, []byte("nodeName")) {
		t.Fatalf("reconciliation evidence leaked identity: %s err=%v", serialized, err)
	}
}

func TestWorkspaceRecoveryPlanKeepsAbsentMutationLedgerEvidenceZero(t *testing.T) {
	evidence := terminalRequestHashReconciliationIdentityEvidence()
	evidence.Reconciliation = &clients.ComputeClaimReconciliationEvidence{
		SchemaVersion: 2, Consumer: "claim_compute_recovery", Generation: "normal_launch_terminal_evidence_v1",
		ProvenanceSource: "normal_launch_terminal_evidence", ProvenanceDigest: strings.Repeat("e", 64), State: "observed",
		ExpectedRequestHashDigest: evidence.Checks[9].ExpectedDigest, PersistedRequestHashDigest: evidence.Checks[9].ActualDigest,
		FailureStage: "node_patch_readback", ProviderErrorClass: "transport_error",
		Node: clients.ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"node_ownership"}},
	}
	proof := clients.ComputeClaimRecoveryProof{
		IdentityEvidence: &evidence, FailureStage: "node_patch_readback", ProviderErrorClass: "transport_error",
		Evidence: &clients.ComputeClaimEvidence{
			Node: clients.ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"node_ownership"}},
		},
	}

	got := workspaceRecoveryComputeClaimEvidenceFromProof(proof)
	if got == nil || got.MutationLedger != "absent" || got.MutationLedgerOutcome != "confirmed_zero" ||
		got.CVM.Attempted != 0 || got.CVM.Confirmed != 0 || got.CVM.Unknown != 0 || len(got.CVM.Missing) != 0 ||
		got.Node.Attempted != 0 || got.Node.Confirmed != 0 || got.Node.Unknown != 0 || len(got.Node.Missing) != 0 ||
		got.Reconciliation == nil || got.Reconciliation.SchemaVersion != 2 || got.Reconciliation.State != "observed" ||
		got.Reconciliation.Node.Attempted != 1 || got.Reconciliation.Node.Unknown != 1 ||
		len(got.Reconciliation.Node.Missing) != 1 || got.Reconciliation.Node.Missing[0] != "node_ownership" {
		t.Fatalf("absent mutation ledger mixed with proof attempts: %#v", got)
	}
}

func TestWorkspaceRecoveryPlanOmitsOneSidedReconciliationFailureEvidence(t *testing.T) {
	evidence := requestHashReconciliationIdentityEvidence()
	evidence.Reconciliation = &clients.ComputeClaimReconciliationEvidence{
		SchemaVersion: 1, Consumer: "claim_compute_recovery", Generation: "isolated_request_hash_v1", State: "observed",
		ExpectedRequestHashDigest: evidence.Checks[9].ExpectedDigest, PersistedRequestHashDigest: evidence.Checks[9].ActualDigest,
		FailureStage: "node_patch_readback",
		Node:         clients.ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"node_ownership"}},
	}
	proof := clients.ComputeClaimRecoveryProof{
		IdentityEvidence: &evidence, FailureStage: "cvm_pre_read", ProviderErrorClass: "readback_mismatch",
		Evidence: &clients.ComputeClaimEvidence{
			CVM: clients.ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"opl_account_id"}},
		},
	}

	got := workspaceRecoveryComputeClaimEvidenceFromProof(proof)
	if got == nil || got.FailureStage != "cvm_pre_read" || got.ProviderErrorClass != "readback_mismatch" || got.Reconciliation != nil {
		t.Fatalf("one-sided reconciliation evidence must not invalidate the safe failure envelope: %#v", got)
	}
	serialized, err := json.Marshal(got)
	if err != nil || bytes.Contains(serialized, []byte(`"reconciliation"`)) {
		t.Fatalf("one-sided reconciliation evidence escaped projection: %s err=%v", serialized, err)
	}
}

func TestWorkspaceRecoveryPlanProjectsPersistedComputeClaimFailureEvidenceWithoutRequestHashMismatch(t *testing.T) {
	evidence := recoverableCVMOnlyIdentityEvidence()
	evidence.BindingClassification = "compute-claim"
	evidence.MutationLedgerOutcome = "unknown"
	evidence.MutationEvidence = &clients.ComputeClaimEvidence{
		CVM: clients.ComputeClaimMutationEvidence{
			Attempted: 1,
			Missing:   []string{"opl_account_id", "opl_workspace_id", "opl_resource_id", "opl_operation_id"},
		},
	}
	evidence.ProviderErrorClass = "readback_mismatch"
	proof := clients.ComputeClaimRecoveryProof{
		Eligible: false, Reason: "provider_describe",
		LaunchOperationID: "workspace-launch-sensitive", AccountID: "acct-sensitive", WorkspaceID: "ws-sensitive",
		CVMInstanceID: "ins-sensitive", NodeName: "10.0.0.18", PrivateIP: "10.0.0.18",
		IdentityEvidence: &evidence, FailureStage: "cvm_pre_read", ProviderErrorClass: "readback_mismatch",
		Evidence: &clients.ComputeClaimEvidence{
			CVM: clients.ComputeClaimMutationEvidence{
				Attempted: 1,
				Missing:   []string{"opl_account_id", "opl_workspace_id", "opl_resource_id", "opl_operation_id"},
			},
		},
	}

	outcome := workspaceRecoveryMutationOutcomeFromComputeClaim(proof)
	got := outcome.ComputeClaimEvidence
	if got == nil || got.BindingClassification != "compute-claim" || got.MismatchField != "" || got.ExpectedDigest != "" || got.ActualDigest != "" ||
		got.MutationLedger != "observed" || got.MutationLedgerOutcome != "unknown" ||
		got.CVM.Attempted != 1 || got.CVM.Confirmed != 0 || got.CVM.Unknown != 0 || len(got.CVM.Missing) != 4 ||
		got.Node.Attempted != 0 || got.Node.Confirmed != 0 || got.Node.Unknown != 0 || len(got.Node.Missing) != 0 ||
		got.LedgerFailureStage != "cvm_tag_readback" || got.LedgerProviderErrorClass != "readback_mismatch" ||
		got.FailureStage != "cvm_pre_read" || got.ProviderErrorClass != "readback_mismatch" || got.Reconciliation != nil {
		t.Fatalf("projected compute-claim evidence=%#v", got)
	}
	serialized, err := json.Marshal(got)
	if err != nil || bytes.Contains(serialized, []byte("mismatchField")) || bytes.Contains(serialized, []byte("expectedDigest")) ||
		bytes.Contains(serialized, []byte("actualDigest")) || bytes.Contains(serialized, []byte("bindingDigest")) ||
		bytes.Contains(serialized, []byte("workspace-launch-sensitive")) || bytes.Contains(serialized, []byte("acct-sensitive")) ||
		bytes.Contains(serialized, []byte("ws-sensitive")) || bytes.Contains(serialized, []byte("ins-sensitive")) ||
		bytes.Contains(serialized, []byte("10.0.0.18")) {
		t.Fatalf("compute-claim evidence leaked protected identity: %s err=%v", serialized, err)
	}
}

func assertWorkspaceRecoveryPlanRequestHashReconciliationExecutesOriginalLaunchOnce(t *testing.T, evidence clients.ComputeClaimIdentityEvidence) {
	t.Helper()
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")
	claimResult := computeClaimRecoveryProofForLaunch(operation, "target_owned")
	claimResult.KubernetesMutationCount = 1
	claimResult.Evidence = &clients.ComputeClaimEvidence{
		Node: clients.ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1},
	}
	fixture.fabric.computeClaimResult = &claimResult
	configureWorkspaceLaunchFulfillment(t, fixture)
	configureWorkspaceComputeClaimReadback(fixture, operation)
	useWorkspaceRecoveryPlanIdentityEvidence(t, &fixture, &evidence)

	diagnosed := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
	if diagnosed.Status != "diagnosed" || diagnosed.OperationID != operation.ID || len(diagnosed.Mismatches) != 0 {
		t.Fatalf("diagnosed plan=%#v", diagnosed)
	}
	read := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodGet, "", nil))
	if read.PlanID != diagnosed.PlanID || read.PlanDigest != diagnosed.PlanDigest || read.OperationID != operation.ID {
		t.Fatalf("read plan=%#v diagnosed=%#v", read, diagnosed)
	}
	validated := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", map[string]any{
		"planId": diagnosed.PlanID, "planDigest": diagnosed.PlanDigest,
	}))
	if validated.Status != "validated" || len(validated.Mismatches) != 0 {
		t.Fatalf("validated plan=%#v", validated)
	}
	executeBody := map[string]any{
		"planId": validated.PlanID, "planDigest": validated.PlanDigest, "decision": "continue", "confirmation": "CONTINUE_RECOVERY_PLAN",
	}
	firstResponse := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/execute", executeBody)
	first := recoveryPlanResponse(t, firstResponse)
	if firstResponse.Code != http.StatusOK || first.Status != "completed" || first.ExecutionID == "" || first.RunID == "" || first.URL == "" || first.ReceiptID == "" {
		t.Fatalf("execute status=%d plan=%#v body=%s", firstResponse.Code, first, firstResponse.Body.String())
	}
	current := fixture.operation(t)
	if current.ID != operation.ID || current.Status != "succeeded" || current.RecoveryExecution == nil || current.RecoveryExecution.ExecutionID != first.ExecutionID ||
		len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeIDs) != 1 || len(fixture.fabric.computeClaimCalls) != 1 ||
		len(fixture.fabric.storageIDs) != 1 || len(fixture.sub2API.refunds) != 0 {
		t.Fatalf("current=%#v charges=%d computes=%d claims=%d storage=%d refunds=%d", current, len(fixture.sub2API.charges),
			len(fixture.fabric.computeIDs), len(fixture.fabric.computeClaimCalls), len(fixture.fabric.storageIDs), len(fixture.sub2API.refunds))
	}
	if fixture.fabric.computeClaimCalls[0].LaunchOperationID != operation.ID || fixture.fabric.computeClaimCalls[0].ComputeAllocationID != operation.ComputeID ||
		fixture.fabric.computeClaimCalls[0].StorageVolumeID != operation.StorageID || fixture.fabric.computeClaimKeys[0] != operation.ID+":compute" {
		t.Fatalf("claim calls=%#v keys=%#v", fixture.fabric.computeClaimCalls, fixture.fabric.computeClaimKeys)
	}

	secondResponse := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/execute", executeBody)
	second := recoveryPlanResponse(t, secondResponse)
	if secondResponse.Code != http.StatusOK || second.ExecutionID != first.ExecutionID || second.RunID != first.RunID || second.URL != first.URL || second.ReceiptID != first.ReceiptID ||
		len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeIDs) != 1 || len(fixture.fabric.computeClaimCalls) != 1 ||
		len(fixture.fabric.storageIDs) != 1 || len(fixture.sub2API.refunds) != 0 {
		t.Fatalf("replay status=%d first=%#v second=%#v charges=%d computes=%d claims=%d storage=%d refunds=%d", secondResponse.Code, first, second,
			len(fixture.sub2API.charges), len(fixture.fabric.computeIDs), len(fixture.fabric.computeClaimCalls), len(fixture.fabric.storageIDs), len(fixture.sub2API.refunds))
	}
}

func requestWorkspaceRecoveryPlan(t *testing.T, fixture workspaceLaunchWorkerFixture, method, suffix string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	operation := fixture.operation(t)
	request := httptest.NewRequest(method, "/api/operator/workspace-launches/"+operation.ID+"/recovery-plan"+suffix, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	if suffix == "/execute" {
		request.Header.Set("Idempotency-Key", "recovery-plan:"+stringValue(body["planDigest"]))
	}
	addAuth(request, fixture.operator)
	response := httptest.NewRecorder()
	fixture.server.ServeHTTP(response, request)
	return response
}

func TestWorkspaceRecoveryPlanRoutesRequireOperatorSessionAndCSRF(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "runtime", "basic")
	fixture := scenario.fixture
	fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
	server, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)
	path := "/api/operator/workspace-launches/" + scenario.unknown.ID + "/recovery-plan/diagnose"
	body := `{"accountId":"` + scenario.unknown.AccountID + `"}`

	unauthenticated := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	unauthenticated.Header.Set("Content-Type", "application/json")
	unauthenticatedResponse := httptest.NewRecorder()
	server.ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d body=%s", unauthenticatedResponse.Code, unauthenticatedResponse.Body.String())
	}

	owner := tenantOwnerSessionForTest(t, server)
	ownerResponse := requestWithSession(t, server, owner, http.MethodPost, path, body)
	if ownerResponse.Code != http.StatusForbidden {
		t.Fatalf("non-operator status=%d body=%s", ownerResponse.Code, ownerResponse.Body.String())
	}

	missingCSRF := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	missingCSRF.Header.Set("Content-Type", "application/json")
	addSessionCookies(missingCSRF, fixture.operator)
	missingCSRFResponse := httptest.NewRecorder()
	server.ServeHTTP(missingCSRFResponse, missingCSRF)
	if missingCSRFResponse.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d body=%s", missingCSRFResponse.Code, missingCSRFResponse.Body.String())
	}

	operatorResponse := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": scenario.unknown.AccountID})
	if operatorResponse.Code != http.StatusOK {
		t.Fatalf("operator status=%d body=%s", operatorResponse.Code, operatorResponse.Body.String())
	}
	if scenario.readback.stageConvergeCalls != 0 || workspaceLaunchStageWriteCount(fixture, "runtime") != scenario.beforeCurrentWrites {
		t.Fatalf("authorization checks crossed mutation boundary: converge=%d", scenario.readback.stageConvergeCalls)
	}
}

func TestWorkspaceRecoveryPlanExecuteRejectsIdempotencyKeyNotBoundToPlan(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "runtime", "basic")
	fixture := scenario.fixture
	fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
	server, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)
	diagnosed := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": scenario.unknown.AccountID}))
	validated := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", map[string]any{
		"planId": diagnosed.PlanID, "planDigest": diagnosed.PlanDigest,
	}))
	body, err := json.Marshal(map[string]any{
		"planId": validated.PlanID, "planDigest": validated.PlanDigest, "decision": "continue", "confirmation": "CONTINUE_RECOVERY_PLAN",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/operator/workspace-launches/"+scenario.unknown.ID+"/recovery-plan/execute", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "recovery-plan:unbound")
	addAuth(request, fixture.operator)
	response := httptest.NewRecorder()
	fixture.server.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unbound idempotency key status=%d body=%s", response.Code, response.Body.String())
	}
	persisted := fixture.operation(t)
	if persisted.RecoveryExecution != nil || scenario.readback.stageConvergeCalls != 0 || workspaceLaunchStageWriteCount(fixture, "runtime") != scenario.beforeCurrentWrites {
		t.Fatalf("unbound idempotency key crossed execution boundary: operation=%#v converge=%d", persisted, scenario.readback.stageConvergeCalls)
	}
}

func recoveryPlanResponse(t *testing.T, response *httptest.ResponseRecorder) workspaceRecoveryPlanDTO {
	t.Helper()
	var plan workspaceRecoveryPlanDTO
	if err := json.NewDecoder(response.Body).Decode(&plan); err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestWorkspaceRecoveryPlanRetiresLegacyPublicRecoveryRoutes(t *testing.T) {
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "runtime", "basic")
	fixture := scenario.fixture
	fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
	server, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)
	operation := fixture.operation(t)
	routes := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/operator/workspace-launches/" + operation.ID + "/recover"},
		{http.MethodGet, "/api/operator/workspace-launches/" + operation.ID + "/readback-recovery-proof"},
		{http.MethodPost, "/api/operator/workspace-launches/" + operation.ID + "/compute-claim-recovery/proof"},
		{http.MethodPost, "/api/operator/workspace-launches/" + operation.ID + "/compute-claim-recovery/validate"},
		{http.MethodPost, "/api/operator/workspace-launches/" + operation.ID + "/compute-claim-recovery/claim"},
	}
	for _, route := range routes {
		request := httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "legacy-recovery-route")
		addAuth(request, fixture.operator)
		response := httptest.NewRecorder()
		fixture.server.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("legacy route %s %s status=%d body=%s", route.method, route.path, response.Code, response.Body.String())
		}
	}
	if scenario.readback.stageConvergeCalls != 0 || workspaceLaunchStageWriteCount(fixture, "runtime") != scenario.beforeCurrentWrites {
		t.Fatalf("retired routes crossed mutation boundary: converge=%d", scenario.readback.stageConvergeCalls)
	}
}

func TestWorkspaceRecoveryPlanHTTPProjectionOmitsInternalAuthorityBindings(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "runtime", "basic")
	fixture := scenario.fixture
	fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
	server, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)

	response := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": scenario.unknown.AccountID})
	if response.Code != http.StatusOK {
		t.Fatalf("diagnose status=%d body=%s", response.Code, response.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"action", "generatedAt", "validatedAt", "releaseBinding", "targetBinding", "allowedDecisions", "identityEvidence", "approval", "computeClaimRequest"} {
		if _, ok := payload[forbidden]; ok {
			t.Fatalf("HTTP projection exposed internal authority field %q: %s", forbidden, response.Body.String())
		}
	}
	for _, required := range []string{"planId", "planDigest", "status", "operationId", "stages", "mismatches", "mutationCounts"} {
		if _, ok := payload[required]; !ok {
			t.Fatalf("HTTP projection omitted Console field %q: %s", required, response.Body.String())
		}
	}
	counts, ok := payload["mutationCounts"].(map[string]any)
	if !ok || len(counts) != 3 || counts["sub2api"] != float64(0) || counts["tencent"] != float64(0) || counts["kubernetes"] != float64(0) {
		t.Fatalf("HTTP projection mutation counts invalid: %s", response.Body.String())
	}
	persisted := fixture.operation(t)
	if persisted.RecoveryPlan == nil || persisted.RecoveryPlan.ReleaseBinding.MainSHA == "" || persisted.RecoveryPlan.TargetBinding.AuthorityDigest == "" {
		t.Fatalf("safe projection discarded persisted authority: %#v", persisted.RecoveryPlan)
	}
}

func TestWorkspaceRecoveryPlanIdentityEvidenceComparesPrivateIPToPersistedLaunch(t *testing.T) {
	operation := workspaceLaunchOperation{
		ID: "workspace-launch-private-ip", AccountID: "acct-private-ip", WorkspaceID: "ws-private-ip",
		ComputeID: "ca-private-ip", StorageID: "vol-private-ip", ComputePrivateIP: "10.20.30.41",
	}
	proof := workspaceLaunchReadbackRecoveryProof{Target: workspaceLaunchReadbackRecoveryTarget{
		LaunchOperationID: operation.ID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID,
		ComputeAllocationID: operation.ComputeID, StorageID: operation.StorageID, PrivateIP: "10.20.30.99",
	}}
	checks := workspaceRecoveryPlanIdentityEvidence(operation, proof, workspaceRecoveryReleaseBinding{})
	found := false
	for _, check := range checks {
		if check.Field == "target.privateIp" {
			found = true
			if check.Matches || check.ExpectedDigest == check.ActualDigest {
				t.Fatalf("private IP drift was self-compared: %#v", check)
			}
		}
	}
	if !found {
		t.Fatal("private IP identity evidence missing")
	}
}

func TestWorkspaceRecoveryPlanDiagnoseBuildsAndPersistsServerAuthority(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "secret", "basic")
	fixture := scenario.fixture
	fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
	server, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)

	response := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{
		"accountId": scenario.unknown.AccountID,
	})
	if response.Code != http.StatusOK {
		t.Fatalf("diagnose status=%d body=%s", response.Code, response.Body.String())
	}
	plan := recoveryPlanResponse(t, response)
	if plan.PlanID == "" || plan.PlanDigest == "" || plan.Status != "diagnosed" || plan.OperationID != scenario.unknown.ID {
		t.Fatalf("diagnosed plan=%#v launchPrivateIP=%q proofPrivateIP=%q", plan, scenario.unknown.ComputePrivateIP, scenario.readback.providerTruth.Compute.PrivateIP)
	}
	persisted, ok, err := fixture.app.workspaceLaunchOperation(context.Background(), scenario.unknown.ID)
	if err != nil || !ok || persisted.RecoveryPlan == nil || persisted.RecoveryPlan.PlanID != plan.PlanID || persisted.RecoveryPlan.PlanDigest != plan.PlanDigest ||
		persisted.RecoveryPlan.Action != "unknown_stage_continue" || persisted.RecoveryPlan.ReleaseBinding.MainSHA != strings.Repeat("a", 40) ||
		persisted.RecoveryPlan.ReleaseBinding.CloudImageDigest != "sha256:"+strings.Repeat("b", 64) || persisted.RecoveryPlan.TargetBinding.LaunchOperationID != scenario.unknown.ID ||
		persisted.RecoveryPlan.TargetBinding.AccountID != scenario.unknown.AccountID || persisted.RecoveryPlan.TargetBinding.WorkspaceID != scenario.unknown.WorkspaceID ||
		persisted.RecoveryPlan.TargetBinding.WorkspaceImageDigest != scenario.unknown.WorkspaceImageDigest || persisted.RecoveryPlan.MutationCounts != (workspaceRecoveryMutationCounts{}) {
		t.Fatalf("persisted recovery plan operation=%#v ok=%v err=%v", persisted, ok, err)
	}
	if scenario.readback.stageConvergeCalls != 0 || workspaceLaunchStageWriteCount(fixture, "secret") != scenario.beforeCurrentWrites {
		t.Fatalf("diagnosis performed recovery mutation: converge=%d before=%d after=%d", scenario.readback.stageConvergeCalls, scenario.beforeCurrentWrites, workspaceLaunchStageWriteCount(fixture, "secret"))
	}
}

func TestWorkspaceRecoveryPlanDiagnoseKeepsComputeClaimPendingWhenNodeOwnershipIsUnallocated(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "storage", "basic")
	fixture := scenario.fixture
	// Production-shaped readback: the compute resource is ready, but the exact
	// Node still carries the unallocated workspace taint despite the target label.
	// Storage has already been attempted with an unknown provider result.
	compute := fixture.fabric.computeSync
	compute.ID = scenario.unknown.ComputeID
	compute.AccountID = scenario.unknown.AccountID
	compute.WorkspaceID = scenario.unknown.WorkspaceID
	compute.PackageID = scenario.unknown.PackageID
	compute.Status = "ready"
	compute.ProviderData = map[string]string{
		"nodeLabel.oplcloud.cn/workspace-id": scenario.unknown.WorkspaceID,
		"nodeTaint.oplcloud.cn/workspace-id": "unallocated",
		"nodeResourceVersion":                "7",
	}
	fixture.fabric.providerTruth = &clients.MonthlyProviderTruth{
		ComputeState: "ready", StorageState: "unknown",
		Compute: compute,
		Storage: scenario.readback.providerTruth.Storage,
	}
	fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
	server, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)

	// Production readback can prove one exact started Fabric storage operation
	// while the Control Plane storage budget is still untouched. The later
	// storage uncertainty must not hide the earlier Node ownership stage.
	stalePhase := scenario.unknown
	stalePhase.Phase = "compute_claim_pending"
	stalePhase.ErrorCode = "workspace_recovery_plan_fabric_proof_failed"
	stalePhase.ContinuationAttemptBudgets["storage"] = workspaceLaunchStageBudget{Max: workspaceLaunchStageMax}
	stalePhase.ComputePoolID = compute.PoolID
	stalePhase.ComputeNodePoolID = compute.NodePoolID
	stalePhase.ComputeMachineName = compute.MachineName
	stalePhase.ComputeNodeName = compute.NodeName
	stalePhase.ComputeCVMInstanceID = compute.CVMInstanceID
	stalePhase.ComputePrivateIP = compute.PrivateIP
	stalePhase.ComputeInstanceType = compute.InstanceType
	stalePhase.ComputeZone = compute.Zone
	stalePhase.ComputeChargeType = compute.ChargeType
	stalePhase.ComputeRenewFlag = compute.RenewFlag
	stalePhase.ComputeDeadline = compute.Deadline
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunchStorage(stalePhase, "unallocated", "storage_attempt_unknown", "")
	fixture.fabric.computeClaimProof.Deadline = stalePhase.ComputeDeadline
	fixture.fabric.computeProviderTruth = &clients.ComputeProviderTruth{
		SchemaVersion: 1, State: "ready", ComputeState: "ready", StorageState: "unknown", Compute: compute,
		NodeOwnershipState: "unallocated", CVMOwnershipState: "target_owned", Proof: &fixture.fabric.computeClaimProof,
	}
	// Recovery must consume the same Compute-only authority as Normal Launch.
	// Calling the old full proof here would reproduce the production short-circuit.
	fixture.fabric.computeClaimProofFn = func(input clients.ComputeClaimRecoveryInput) (clients.ComputeClaimRecoveryProof, error) {
		t.Fatalf("legacy full proof called for compute evidence: %#v", input)
		return clients.ComputeClaimRecoveryProof{}, errors.New("legacy full proof called")
	}
	claimed := computeClaimRecoveryProofForLaunchStorage(stalePhase, "target_owned", "storage_attempt_unknown", "")
	claimed.Deadline = stalePhase.ComputeDeadline
	claimed.KubernetesMutationCount = 1
	claimed.Evidence = &clients.ComputeClaimEvidence{
		Node: clients.ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1},
	}
	fixture.fabric.computeClaimResult = &claimed
	configureWorkspaceComputeClaimReadback(fixture, stalePhase)
	predecessorDigest := strings.Repeat("c", 64)
	stalePhase.RecoveryPlan = &workspaceRecoveryPlan{
		SchemaVersion: workspaceRecoveryPlanSchemaVersion, PlanID: "recovery-plan-" + predecessorDigest[:20], PlanDigest: predecessorDigest,
		Status: "failed", Action: "compute_claim_continue", OperationID: stalePhase.ID,
		ErrorCode: "workspace_recovery_plan_fabric_proof_failed",
	}
	stalePhase.RecoveryExecution = &workspaceRecoveryExecution{
		ExecutionID: "recovery-exec-storage-attempt-unknown", RunIdentity: "control-plane-run-storage-attempt-unknown",
		PlanID: stalePhase.RecoveryPlan.PlanID, PlanDigest: predecessorDigest, ApprovalDigest: strings.Repeat("d", 64), Decision: "continue",
		Status: "failed", StartedAt: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano),
		CompletedAt: time.Now().UTC().Format(time.RFC3339Nano), ErrorCode: stalePhase.ErrorCode,
		MutationOutcome: workspaceRecoveryMutationOutcome{Status: "unknown", Source: "recovery_execution"},
	}
	if _, decodeErr := decodeWorkspaceLaunchOperation(workspaceLaunchOperationRow(stalePhase)); decodeErr != nil {
		t.Fatalf("stale compute-claim fixture rejected: %v operation=%+v", decodeErr, stalePhase)
	}
	if err := fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(stalePhase)); err != nil {
		t.Fatal(err)
	}
	storageWrites := len(fixture.fabric.storageIDs)

	response := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{
		"accountId": stalePhase.AccountID,
	})
	plan := recoveryPlanResponse(t, response)
	persisted, ok, err := fixture.app.workspaceLaunchOperation(context.Background(), stalePhase.ID)
	if err != nil || !ok {
		t.Fatalf("persisted operation read failed: ok=%v err=%v", ok, err)
	}
	storageBudget := persisted.ContinuationAttemptBudgets["storage"]
	firstIncompleteStage := ""
	if persisted.RecoveryPlan != nil && len(persisted.RecoveryPlan.Stages) > 0 {
		firstIncompleteStage = persisted.RecoveryPlan.Stages[0].Stage
	}
	firstFalsePredicate := ""
	for _, check := range persisted.RecoveryPlan.IdentityEvidence {
		if check.Field == "provider.nodeOwnership" && check.Actual != "target_owned" {
			firstFalsePredicate = check.Field
			break
		}
	}
	if response.Code != http.StatusOK || plan.Status != "diagnosed" || plan.MutationCounts != (workspaceRecoveryMutationCounts{}) ||
		persisted.RecoveryPlan == nil || persisted.RecoveryPlan.Action != "compute_claim_continue" || persisted.RecoveryPlan.TargetBinding.Stage != "compute_claim" ||
		firstIncompleteStage != "compute_claim" || firstFalsePredicate != "provider.nodeOwnership" ||
		persisted.RecoveryExecution != nil || len(persisted.RecoveryHistory) != 1 ||
		persisted.RecoveryHistory[0].Plan.PlanDigest != predecessorDigest || persisted.RecoveryHistory[0].Execution.MutationOutcome.Status != "confirmed_zero" ||
		storageBudget != (workspaceLaunchStageBudget{Max: workspaceLaunchStageMax}) ||
		persisted.RecoveryPlan.TargetBinding.NodeOwnershipState != "unallocated" || persisted.RecoveryPlan.TargetBinding.StorageState != "storage_attempt_unknown" ||
		scenario.readback.stageProofCalls != 0 || len(fixture.fabric.computeProviderInputs) != 1 || len(fixture.fabric.computeClaimInputs) != 0 || len(fixture.fabric.computeClaimCalls) != 0 ||
		len(fixture.fabric.storageIDs) != storageWrites || len(fixture.sub2API.charges) != 1 || len(fixture.sub2API.refunds) != 0 {
		t.Fatalf("compute phase was not kept first: status=%d plan=%#v operation=%#v stageProofCalls=%d computeProofs=%d claims=%d storageWrites=%d/%d charges=%d refunds=%d",
			response.Code, plan, persisted, scenario.readback.stageProofCalls, len(fixture.fabric.computeClaimInputs), len(fixture.fabric.computeClaimCalls), len(fixture.fabric.storageIDs), storageWrites, len(fixture.sub2API.charges), len(fixture.sub2API.refunds))
	}

	validatedResponse := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", map[string]any{
		"planId": plan.PlanID, "planDigest": plan.PlanDigest,
	})
	validated := recoveryPlanResponse(t, validatedResponse)
	if validatedResponse.Code != http.StatusOK || validated.Status != "validated" {
		t.Fatalf("validate status=%d plan=%#v body=%s", validatedResponse.Code, validated, validatedResponse.Body.String())
	}
	beforeDownstreamWrites := map[string]int{}
	for _, stage := range []string{"attachment", "secret", "runtime", "activation", "receipt"} {
		beforeDownstreamWrites[stage] = workspaceLaunchStageWriteCount(fixture, stage)
	}
	fixture.fabric.storageSyncErr = errors.New("storage provider readback unavailable")
	executeResponse := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/execute", map[string]any{
		"planId": validated.PlanID, "planDigest": validated.PlanDigest, "decision": "continue", "confirmation": "CONTINUE_RECOVERY_PLAN",
	})
	executed := recoveryPlanResponse(t, executeResponse)
	current := fixture.operation(t)
	if executeResponse.Code != http.StatusOK || executed.Status != "failed" || current.Status != "manual_review" || current.Phase != "storage_fulfilling" ||
		current.ErrorCode != "workspace_launch_storage_attempt_unknown" || current.ComputeClaimProof == nil || current.ComputeClaimProof.NodeOwnershipState != "target_owned" ||
		current.ContinuationAttemptBudgets["storage"] != (workspaceLaunchStageBudget{Attempted: 1, Unknown: 1, Max: workspaceLaunchStageMax}) ||
		len(fixture.fabric.computeClaimCalls) != 1 || countStrings(*fixture.events, "fabric.compute.get") != 1 || len(fixture.fabric.storageIDs) != storageWrites ||
		len(fixture.sub2API.charges) != 1 || len(fixture.sub2API.refunds) != 0 {
		t.Fatalf("Node continuation did not isolate unknown Storage: status=%d plan=%#v operation=%#v claims=%d computeGets=%d storageWrites=%d/%d charges=%d refunds=%d",
			executeResponse.Code, executed, current, len(fixture.fabric.computeClaimCalls), countStrings(*fixture.events, "fabric.compute.get"),
			len(fixture.fabric.storageIDs), storageWrites, len(fixture.sub2API.charges), len(fixture.sub2API.refunds))
	}
	for stage, before := range beforeDownstreamWrites {
		if after := workspaceLaunchStageWriteCount(fixture, stage); after != before {
			t.Fatalf("Storage unknown crossed %s mutation boundary: before=%d after=%d", stage, before, after)
		}
	}
}

func TestWorkspaceRecoveryPlanDiagnosePrioritizesComputeClaimOverStorageUnknown(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	operation.Status, operation.Phase, operation.ErrorCode = "manual_review", "storage_fulfilling", "workspace_launch_storage_attempt_unknown"
	operation.ContinuationAttemptBudgets["storage"] = workspaceLaunchStageBudget{Attempted: 1, Unknown: 1, Max: workspaceLaunchStageMax}
	fixture.fabric.providerTruthErr = errors.New("monthly_provider_truth_unavailable")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunchStorage(operation, "unallocated", "storage_attempt_unknown", "")
	operation.ComputeClaimProof = &fixture.fabric.computeClaimProof
	fixture.fabric.computeClaimProofFn = func(input clients.ComputeClaimRecoveryInput) (clients.ComputeClaimRecoveryProof, error) {
		if !input.AllowExistingStorageOperation {
			return fixture.fabric.computeClaimProof, errors.New("compute_claim_recovery_storage_already_started")
		}
		return fixture.fabric.computeClaimProof, nil
	}
	if err := fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(operation)); err != nil {
		t.Fatal(err)
	}

	response := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID})
	if response.Code != http.StatusOK {
		t.Fatalf("compute claim was hidden by Storage unknown: status=%d body=%s", response.Code, response.Body.String())
	}
	plan := recoveryPlanResponse(t, response)
	persisted := fixture.operation(t)
	if plan.Status != "diagnosed" || persisted.RecoveryPlan == nil || persisted.RecoveryPlan.Action != "compute_claim_continue" || persisted.RecoveryPlan.TargetBinding.Stage != "compute_claim" ||
		len(fixture.fabric.computeClaimInputs) != 1 || !fixture.fabric.computeClaimInputs[0].AllowExistingStorageOperation ||
		len(fixture.fabric.storageIDs) != 0 || len(fixture.sub2API.charges) != 1 ||
		countStrings(*fixture.events, "fabric.monthly-provider-truth") != 0 {
		t.Fatalf("stage isolation failed: plan=%#v operation=%#v computeInputs=%#v storage=%d charges=%d events=%#v", plan, persisted,
			fixture.fabric.computeClaimInputs, len(fixture.fabric.storageIDs), len(fixture.sub2API.charges), *fixture.events)
	}
}

func TestWorkspaceRecoveryPlanReadAndValidateReportExactReleaseDriftWithoutExternalMutation(t *testing.T) {
	mainSHA := strings.Repeat("a", 40)
	t.Setenv("OPL_RELEASE_SHA", mainSHA)
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "runtime", "basic")
	fixture := scenario.fixture
	fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
	server, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)
	diagnose := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": scenario.unknown.AccountID})
	if diagnose.Code != http.StatusOK {
		t.Fatalf("diagnose status=%d body=%s", diagnose.Code, diagnose.Body.String())
	}
	plan := recoveryPlanResponse(t, diagnose)

	read := requestWorkspaceRecoveryPlan(t, fixture, http.MethodGet, "", nil)
	if read.Code != http.StatusOK {
		t.Fatalf("read status=%d body=%s", read.Code, read.Body.String())
	}
	persisted := recoveryPlanResponse(t, read)
	if persisted.PlanID != plan.PlanID || persisted.PlanDigest != plan.PlanDigest || persisted.OperationID != scenario.unknown.ID || len(persisted.Mismatches) != 0 {
		t.Fatalf("persisted plan=%#v", persisted)
	}

	validateBody := map[string]any{"planId": plan.PlanID, "planDigest": plan.PlanDigest}
	validatedResponse := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", validateBody)
	if validatedResponse.Code != http.StatusOK {
		t.Fatalf("validate status=%d body=%s", validatedResponse.Code, validatedResponse.Body.String())
	}
	validated := recoveryPlanResponse(t, validatedResponse)
	validatedOperation := fixture.operation(t)
	if validated.Status != "validated" || len(validated.Mismatches) != 0 || validatedOperation.RecoveryPlan == nil || validatedOperation.RecoveryPlan.ValidatedAt == "" {
		t.Fatalf("validated plan=%#v", validated)
	}

	driftedSHA := strings.Repeat("c", 40)
	t.Setenv("OPL_RELEASE_SHA", driftedSHA)
	driftResponse := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", validateBody)
	if driftResponse.Code != http.StatusOK {
		t.Fatalf("drift validate status=%d body=%s", driftResponse.Code, driftResponse.Body.String())
	}
	drifted := recoveryPlanResponse(t, driftResponse)
	if drifted.Status != "blocked" || len(drifted.Mismatches) != 1 || drifted.Mismatches[0].Field != "release.mainSha" ||
		drifted.Mismatches[0].Expected != mainSHA || drifted.Mismatches[0].Actual != driftedSHA || drifted.ErrorCode != "identity_mismatch" {
		t.Fatalf("drifted plan=%#v", drifted)
	}
	if scenario.readback.stageConvergeCalls != 0 || workspaceLaunchStageWriteCount(fixture, "runtime") != scenario.beforeCurrentWrites {
		t.Fatalf("validation performed recovery mutation: converge=%d before=%d after=%d", scenario.readback.stageConvergeCalls, scenario.beforeCurrentWrites, workspaceLaunchStageWriteCount(fixture, "runtime"))
	}
}

func TestWorkspaceRecoveryPlanExecuteUsesOnePersistedExecutionAndOriginalLaunchContinuation(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "secret", "basic")
	fixture := scenario.fixture
	fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
	server, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)

	diagnosed := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": scenario.unknown.AccountID}))
	validated := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", map[string]any{
		"planId": diagnosed.PlanID, "planDigest": diagnosed.PlanDigest,
	}))
	if validated.Status != "validated" {
		t.Fatalf("validated plan=%#v", validated)
	}
	executeBody := map[string]any{
		"planId": validated.PlanID, "planDigest": validated.PlanDigest, "decision": "continue", "confirmation": "CONTINUE_RECOVERY_PLAN",
	}
	firstResponse := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/execute", executeBody)
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("execute status=%d body=%s", firstResponse.Code, firstResponse.Body.String())
	}
	first := recoveryPlanResponse(t, firstResponse)
	if first.ExecutionID == "" || first.RunID == "" || first.Status != "completed" || first.URL == "" || first.ReceiptID == "" {
		t.Fatalf("executed plan=%#v", first)
	}
	writesAfterFirst := map[string]int{}
	for _, stage := range workspaceLaunchContinuationStages {
		writesAfterFirst[stage] = workspaceLaunchStageWriteCount(fixture, stage)
	}
	secondResponse := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/execute", executeBody)
	if secondResponse.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", secondResponse.Code, secondResponse.Body.String())
	}
	second := recoveryPlanResponse(t, secondResponse)
	if second.ExecutionID != first.ExecutionID || second.RunID != first.RunID || second.URL != first.URL || second.ReceiptID != first.ReceiptID {
		t.Fatalf("execution replay drift first=%#v second=%#v", first, second)
	}
	for _, stage := range workspaceLaunchContinuationStages {
		if got := workspaceLaunchStageWriteCount(fixture, stage); got != writesAfterFirst[stage] {
			t.Fatalf("replay repeated %s write: before=%d after=%d", stage, writesAfterFirst[stage], got)
		}
	}
	if len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeIDs) != 1 || len(fixture.fabric.storageIDs) != 1 {
		t.Fatalf("recovery repeated commercial/provider mutation: charges=%d compute=%d storage=%d", len(fixture.sub2API.charges), len(fixture.fabric.computeIDs), len(fixture.fabric.storageIDs))
	}
	persisted, ok, err := fixture.app.workspaceLaunchOperation(context.Background(), scenario.unknown.ID)
	if err != nil || !ok || persisted.RecoveryExecution == nil || persisted.RecoveryExecution.ExecutionID != first.ExecutionID || persisted.Status != "succeeded" {
		t.Fatalf("persisted execution operation=%#v ok=%v err=%v", persisted, ok, err)
	}
}

func TestWorkspaceComputeClaimStageAwareReadbackAllowsLegacyManualReviewBeforeStorage(t *testing.T) {
	operation := workspaceLaunchOperation{
		Status: "manual_review",
		Phase:  "compute_fulfilling",
	}
	if !workspaceComputeClaimStageAwareReadback(operation) {
		t.Fatal("legacy manual-review compute claim must allow storage readback without making a storage decision")
	}
	if workspaceComputeClaimStageAwareReadback(workspaceLaunchOperation{Status: "manual_review", Phase: "storage_fulfilling"}) {
		t.Fatal("storage-stage manual review must not authorize compute-first storage bypass")
	}
}

func TestWorkspaceRecoveryPlanExecutionLeaseHasOneCrossInstanceWinner(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "runtime", "basic")
	fixture := scenario.fixture
	fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
	server, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)
	diagnosed := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": scenario.unknown.AccountID}))
	validated := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", map[string]any{
		"planId": diagnosed.PlanID, "planDigest": diagnosed.PlanDigest,
	}))
	peer, err := newControlPlaneAppWithStore(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	type reservation struct {
		execution workspaceRecoveryExecution
		won       bool
		err       error
	}
	results := make(chan reservation, 2)
	for _, app := range []*controlPlaneServer{fixture.app, peer} {
		go func(candidate *controlPlaneServer) {
			execution, won, reserveErr := candidate.reserveWorkspaceRecoveryExecution(context.Background(), fixture.service, scenario.unknown.ID, validated.PlanID, validated.PlanDigest, "continue", "usr-admin")
			results <- reservation{execution: execution, won: won, err: reserveErr}
		}(app)
	}
	winners := 0
	var executionID, runID string
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if executionID == "" {
			executionID, runID = result.execution.ExecutionID, result.execution.RunIdentity
		} else if result.execution.ExecutionID != executionID || result.execution.RunIdentity != runID {
			t.Fatalf("lease contenders received different execution identity: first=%s/%s second=%s/%s", executionID, runID, result.execution.ExecutionID, result.execution.RunIdentity)
		}
		if result.won {
			winners++
		}
	}
	if winners != 1 || executionID == "" || runID == "" {
		t.Fatalf("execution lease winners=%d execution=%s run=%s", winners, executionID, runID)
	}
	if scenario.readback.stageConvergeCalls != 0 || workspaceLaunchStageWriteCount(fixture, "runtime") != scenario.beforeCurrentWrites {
		t.Fatalf("lease reservation crossed recovery mutation boundary: converge=%d before=%d after=%d", scenario.readback.stageConvergeCalls, scenario.beforeCurrentWrites, workspaceLaunchStageWriteCount(fixture, "runtime"))
	}
}

func TestWorkspaceRecoveryPlanConcurrentConsoleExecuteDoesNotEnterProviderTwice(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")
	claimed := computeClaimRecoveryProofForLaunch(operation, "target_owned")
	fixture.fabric.computeClaimResult = &claimed
	configureWorkspaceLaunchFulfillment(t, fixture)
	configureWorkspaceComputeClaimReadback(fixture, operation)
	diagnosed := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
	validated := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", map[string]any{
		"planId": diagnosed.PlanID, "planDigest": diagnosed.PlanDigest,
	}))
	peerServer, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	peerFixture := fixture
	peerFixture.server, peerFixture.operator = peerServer, reservedOperatorSessionForTest(t, peerServer)

	providerEntered := make(chan struct{}, 2)
	releaseProvider := make(chan struct{})
	fixture.fabric.beforeComputeClaim = func() {
		providerEntered <- struct{}{}
		<-releaseProvider
	}
	executeBody := map[string]any{
		"planId": validated.PlanID, "planDigest": validated.PlanDigest, "decision": "continue", "confirmation": "CONTINUE_RECOVERY_PLAN",
	}
	firstResult := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstResult <- requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/execute", executeBody)
	}()
	select {
	case <-providerEntered:
	case <-time.After(2 * time.Second):
		close(releaseProvider)
		t.Fatal("first execution did not enter compute claim provider")
	}
	secondResult := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		secondResult <- requestWorkspaceRecoveryPlan(t, peerFixture, http.MethodPost, "/execute", executeBody)
	}()
	var second *httptest.ResponseRecorder
	select {
	case <-providerEntered:
		close(releaseProvider)
		<-firstResult
		<-secondResult
		t.Fatal("lease loser entered compute claim provider")
	case second = <-secondResult:
	case <-time.After(2 * time.Second):
		close(releaseProvider)
		<-firstResult
		<-secondResult
		t.Fatal("lease loser did not return persisted execution")
	}
	if second.Code != http.StatusOK {
		close(releaseProvider)
		<-firstResult
		t.Fatalf("lease loser status=%d body=%s", second.Code, second.Body.String())
	}
	loserPlan := recoveryPlanResponse(t, second)
	if loserPlan.Status != "executing" || loserPlan.ExecutionID == "" || loserPlan.RunID == "" {
		close(releaseProvider)
		<-firstResult
		t.Fatalf("lease loser projection=%#v", loserPlan)
	}
	close(releaseProvider)
	first := <-firstResult
	if first.Code != http.StatusOK || len(fixture.fabric.computeClaimCalls) != 1 {
		t.Fatalf("winner status=%d body=%s claims=%d", first.Code, first.Body.String(), len(fixture.fabric.computeClaimCalls))
	}
}

func TestWorkspaceRecoveryPlanExpiredLeaseReconcilesSameExecutionAfterRestart(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")
	claimed := computeClaimRecoveryProofForLaunch(operation, "target_owned")
	fixture.fabric.computeClaimResult = &claimed
	configureWorkspaceLaunchFulfillment(t, fixture)
	configureWorkspaceComputeClaimReadback(fixture, operation)
	diagnosed := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
	validated := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", map[string]any{
		"planId": diagnosed.PlanID, "planDigest": diagnosed.PlanDigest,
	}))
	execution, won, err := fixture.app.reserveWorkspaceRecoveryExecution(context.Background(), fixture.service, operation.ID, validated.PlanID, validated.PlanDigest, "continue", "usr-admin")
	if err != nil || !won {
		t.Fatalf("initial execution reservation won=%v err=%v execution=%#v", won, err, execution)
	}
	persisted := fixture.operation(t)
	persisted.RecoveryExecution.LeaseExpiresAt = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(persisted)))
	restartedServer, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	restarted := fixture
	restarted.server, restarted.operator = restartedServer, reservedOperatorSessionForTest(t, restartedServer)
	response := requestWorkspaceRecoveryPlan(t, restarted, http.MethodPost, "/execute", map[string]any{
		"planId": validated.PlanID, "planDigest": validated.PlanDigest, "decision": "continue", "confirmation": "CONTINUE_RECOVERY_PLAN",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("restart reconcile status=%d body=%s", response.Code, response.Body.String())
	}
	completed := recoveryPlanResponse(t, response)
	if completed.Status != "completed" || completed.ExecutionID != execution.ExecutionID || completed.RunID != execution.RunIdentity || completed.URL == "" || completed.ReceiptID == "" ||
		len(fixture.fabric.computeClaimCalls) != 1 || len(fixture.fabric.storageIDs) != 1 || len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeIDs) != 1 {
		t.Fatalf("restart reconcile did not reuse exact execution: plan=%#v claims=%d storage=%d charges=%d computes=%d", completed, len(fixture.fabric.computeClaimCalls), len(fixture.fabric.storageIDs), len(fixture.sub2API.charges), len(fixture.fabric.computeIDs))
	}
}

func TestWorkspaceRecoveryPlanReleasedLeaseCanBeReacquired(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "runtime", "basic")
	fixture := scenario.fixture
	fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
	server, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)
	diagnosed := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": scenario.unknown.AccountID}))
	validated := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", map[string]any{
		"planId": diagnosed.PlanID, "planDigest": diagnosed.PlanDigest,
	}))
	execution, won, err := fixture.app.reserveWorkspaceRecoveryExecution(context.Background(), fixture.service, scenario.unknown.ID, validated.PlanID, validated.PlanDigest, "continue", "usr-admin")
	if err != nil || !won {
		t.Fatalf("initial execution reservation won=%v err=%v execution=%#v", won, err, execution)
	}
	persisted := fixture.operation(t)
	persisted.RecoveryExecution.LeaseToken, persisted.RecoveryExecution.LeaseExpiresAt = "", ""
	mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(persisted)))

	restarted, err := newControlPlaneAppWithStore(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	reacquired, won, err := restarted.reacquireWorkspaceRecoveryExecution(context.Background(), scenario.unknown.ID, validated.PlanID, validated.PlanDigest, "continue")
	if err != nil || !won || reacquired.ExecutionID != execution.ExecutionID || reacquired.RunIdentity != execution.RunIdentity || reacquired.LeaseToken == "" || reacquired.LeaseExpiresAt == "" {
		t.Fatalf("released lease reacquire won=%v err=%v initial=%#v reacquired=%#v", won, err, execution, reacquired)
	}
	if scenario.readback.stageConvergeCalls != 0 || workspaceLaunchStageWriteCount(fixture, "runtime") != scenario.beforeCurrentWrites {
		t.Fatalf("lease reacquire crossed provider boundary: converge=%d runtime=%d", scenario.readback.stageConvergeCalls, workspaceLaunchStageWriteCount(fixture, "runtime"))
	}
}

func TestWorkspaceRecoveryPlanRejectsPartialOrInvalidReleasedLease(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "runtime", "basic")
	fixture := scenario.fixture
	fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
	server, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)
	diagnosed := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": scenario.unknown.AccountID}))
	validated := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", map[string]any{
		"planId": diagnosed.PlanID, "planDigest": diagnosed.PlanDigest,
	}))
	execution, won, err := fixture.app.reserveWorkspaceRecoveryExecution(context.Background(), fixture.service, scenario.unknown.ID, validated.PlanID, validated.PlanDigest, "continue", "usr-admin")
	if err != nil || !won {
		t.Fatalf("initial execution reservation won=%v err=%v execution=%#v", won, err, execution)
	}
	for _, test := range []struct {
		name       string
		leaseToken string
		expiresAt  string
	}{
		{name: "missing token", expiresAt: time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano)},
		{name: "missing expiry", leaseToken: execution.LeaseToken},
		{name: "invalid expiry", leaseToken: execution.LeaseToken, expiresAt: "not-a-timestamp"},
	} {
		t.Run(test.name, func(t *testing.T) {
			persisted := fixture.operation(t)
			persisted.RecoveryExecution.LeaseToken, persisted.RecoveryExecution.LeaseExpiresAt = test.leaseToken, test.expiresAt
			mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(persisted)))
			_, won, err := fixture.app.reacquireWorkspaceRecoveryExecution(context.Background(), scenario.unknown.ID, validated.PlanID, validated.PlanDigest, "continue")
			if !errors.Is(err, errBillingReviewIdentity) || won {
				t.Fatalf("partial or invalid lease reacquire won=%v err=%v", won, err)
			}
		})
	}
	if scenario.readback.stageConvergeCalls != 0 || workspaceLaunchStageWriteCount(fixture, "runtime") != scenario.beforeCurrentWrites {
		t.Fatalf("invalid lease crossed provider boundary: converge=%d runtime=%d", scenario.readback.stageConvergeCalls, workspaceLaunchStageWriteCount(fixture, "runtime"))
	}
}

func TestWorkspaceRecoveryPlanWorkerRetriesDelayedRuntimeAndSynchronizesTerminalReadback(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "runtime", "basic")
	fixture := scenario.fixture
	ready := fixture.fabric.runtimeStatus
	unready := ready
	unready.Status, unready.Ready = "unready", false
	fixture.fabric.runtimeStatusResults = []clients.WorkspaceRuntime{unready}
	fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
	server, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)
	diagnosed := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": scenario.unknown.AccountID}))
	validated := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", map[string]any{
		"planId": diagnosed.PlanID, "planDigest": diagnosed.PlanDigest,
	}))
	execute := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/execute", map[string]any{
		"planId": validated.PlanID, "planDigest": validated.PlanDigest, "decision": "continue", "confirmation": "CONTINUE_RECOVERY_PLAN",
	})
	if execute.Code != http.StatusOK {
		t.Fatalf("initial delayed Runtime execute status=%d body=%s", execute.Code, execute.Body.String())
	}
	waitingPlan := recoveryPlanResponse(t, execute)
	waiting := fixture.operation(t)
	if waitingPlan.Status != "executing" || waiting.Status != "waiting" || waiting.Phase != "runtime_starting" || waiting.RecoveryExecution == nil ||
		waiting.RecoveryExecution.LeaseToken != "" || waiting.RecoveryExecution.LeaseExpiresAt != "" {
		t.Fatalf("delayed Runtime recovery plan=%#v launch=%#v", waitingPlan, waiting)
	}

	fixture.fabric.runtimeStatusErr = errors.New("transient Runtime readback failure")
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err == nil {
		t.Fatal("transient Runtime readback failure was not returned")
	}
	retryable := fixture.operation(t)
	if retryable.Status != "retryable" || retryable.Phase != "runtime_starting" || retryable.RecoveryPlan == nil || retryable.RecoveryPlan.Status != "executing" ||
		retryable.RecoveryExecution == nil || retryable.RecoveryExecution.Status != "running" {
		t.Fatalf("transient Runtime readback was not retryable: %#v", retryable)
	}
	fixture.fabric.runtimeStatusErr = nil
	restarted, err := newControlPlaneAppWithStore(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	completed, found, err := restarted.workspaceLaunchOperation(context.Background(), scenario.unknown.ID)
	if err != nil || !found || completed.Status != "succeeded" || completed.Phase != "succeeded" || completed.URL == "" || completed.ReceiptID == "" ||
		completed.RecoveryPlan == nil || completed.RecoveryPlan.Status != "completed" || completed.RecoveryPlan.URL != completed.URL || completed.RecoveryPlan.ReceiptID != completed.ReceiptID ||
		completed.RecoveryExecution == nil || completed.RecoveryExecution.Status != "completed" || completed.RecoveryExecution.CompletedAt == "" {
		t.Fatalf("restarted worker terminal readback launch=%#v found=%v err=%v", completed, found, err)
	}
	if workspaceLaunchStageWriteCount(fixture, "runtime") != scenario.beforeCurrentWrites || len(fixture.fabric.storageIDs) != 1 || len(fixture.fabric.computeIDs) != 1 ||
		len(fixture.sub2API.charges) != 1 || len(fixture.ledger.receiptInputs) != 1 {
		t.Fatalf("Runtime retry repeated write: runtime=%d storage=%d compute=%d charges=%d receipts=%d", workspaceLaunchStageWriteCount(fixture, "runtime"), len(fixture.fabric.storageIDs), len(fixture.fabric.computeIDs), len(fixture.sub2API.charges), len(fixture.ledger.receiptInputs))
	}
}

func TestWorkspaceRecoveryPlanExpiredLeaseRejectsStaleHolderFinalize(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "runtime", "basic")
	fixture := scenario.fixture
	fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
	server, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)
	diagnosed := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": scenario.unknown.AccountID}))
	validated := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", map[string]any{
		"planId": diagnosed.PlanID, "planDigest": diagnosed.PlanDigest,
	}))
	stale, won, err := fixture.app.reserveWorkspaceRecoveryExecution(context.Background(), fixture.service, scenario.unknown.ID, validated.PlanID, validated.PlanDigest, "continue", "usr-admin")
	if err != nil || !won || stale.LeaseToken == "" {
		t.Fatalf("initial reservation won=%v err=%v execution=%#v", won, err, stale)
	}
	persisted := fixture.operation(t)
	persisted.RecoveryExecution.LeaseExpiresAt = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(persisted)))
	fresh, won, err := fixture.app.reacquireWorkspaceRecoveryExecution(context.Background(), scenario.unknown.ID, validated.PlanID, validated.PlanDigest, "continue")
	if err != nil || !won || fresh.LeaseToken == "" || fresh.LeaseToken == stale.LeaseToken {
		t.Fatalf("lease takeover won=%v err=%v stale=%#v fresh=%#v", won, err, stale, fresh)
	}
	if _, err := fixture.app.finalizeWorkspaceRecoveryExecution(context.Background(), scenario.unknown.ID, stale.ExecutionID, stale.LeaseToken, workspaceRecoveryMutationOutcome{Status: "unknown"}, nil); !errors.Is(err, errBillingReviewIdentity) {
		t.Fatalf("stale holder finalize err=%v", err)
	}
	current := fixture.operation(t)
	if current.RecoveryExecution == nil || current.RecoveryExecution.LeaseToken != fresh.LeaseToken || current.RecoveryExecution.Status != "running" {
		t.Fatalf("stale holder changed current lease: %#v", current.RecoveryExecution)
	}
	if scenario.readback.stageConvergeCalls != 0 || workspaceLaunchStageWriteCount(fixture, "runtime") != scenario.beforeCurrentWrites {
		t.Fatalf("lease fencing crossed recovery mutation boundary: converge=%d", scenario.readback.stageConvergeCalls)
	}
}

func TestWorkspaceRecoveryPlanExecuteRejectsBlockedPlanAndKeepsManualReview(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "runtime", "basic")
	fixture := scenario.fixture
	fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
	server, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)
	diagnosed := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": scenario.unknown.AccountID}))
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("c", 40))
	blocked := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", map[string]any{
		"planId": diagnosed.PlanID, "planDigest": diagnosed.PlanDigest,
	}))
	response := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/execute", map[string]any{
		"planId": blocked.PlanID, "planDigest": blocked.PlanDigest, "decision": "continue", "confirmation": "CONTINUE_RECOVERY_PLAN",
	})
	if response.Code != http.StatusConflict {
		t.Fatalf("blocked execute status=%d body=%s", response.Code, response.Body.String())
	}
	persisted, ok, err := fixture.app.workspaceLaunchOperation(context.Background(), scenario.unknown.ID)
	if err != nil || !ok || persisted.Status != "manual_review" || persisted.RecoveryExecution != nil {
		t.Fatalf("blocked execution changed launch operation=%#v ok=%v err=%v", persisted, ok, err)
	}
	if scenario.readback.stageConvergeCalls != 0 || workspaceLaunchStageWriteCount(fixture, "runtime") != scenario.beforeCurrentWrites {
		t.Fatalf("blocked execution mutated provider state: converge=%d before=%d after=%d", scenario.readback.stageConvergeCalls, scenario.beforeCurrentWrites, workspaceLaunchStageWriteCount(fixture, "runtime"))
	}
}

func TestWorkspaceRecoveryPlanDiagnoseAndValidateComputeClaimFromServerAuthority(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")

	diagnoseResponse := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID})
	if diagnoseResponse.Code != http.StatusOK {
		t.Fatalf("compute claim diagnose status=%d body=%s", diagnoseResponse.Code, diagnoseResponse.Body.String())
	}
	diagnosed := recoveryPlanResponse(t, diagnoseResponse)
	persistedPlan := fixture.operation(t).RecoveryPlan
	if diagnosed.Status != "diagnosed" || persistedPlan == nil || persistedPlan.Action != "compute_claim_continue" || persistedPlan.TargetBinding.Stage != "compute_claim" ||
		persistedPlan.TargetBinding.CVMInstanceID != operation.ComputeCVMInstanceID || persistedPlan.TargetBinding.NodeName != operation.ComputeNodeName ||
		persistedPlan.TargetBinding.WorkspaceAPIKeyID != operation.WorkspaceAPIKeyID || persistedPlan.MutationCounts != (workspaceRecoveryMutationCounts{}) {
		t.Fatalf("compute claim diagnosed plan=%#v", diagnosed)
	}
	validateResponse := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", map[string]any{
		"planId": diagnosed.PlanID, "planDigest": diagnosed.PlanDigest,
	})
	if validateResponse.Code != http.StatusOK {
		t.Fatalf("compute claim validate status=%d body=%s", validateResponse.Code, validateResponse.Body.String())
	}
	validated := recoveryPlanResponse(t, validateResponse)
	validatedOperation := fixture.operation(t)
	if validated.Status != "validated" || len(validated.Mismatches) != 0 || validatedOperation.RecoveryPlan == nil || validatedOperation.RecoveryPlan.ValidatedAt == "" {
		t.Fatalf("compute claim validated plan=%#v", validated)
	}
	persisted := fixture.operation(t)
	if persisted.Status != "compute_claim_pending" || persisted.Phase != "compute_claim_pending" || persisted.ComputeClaimApproval != nil ||
		len(fixture.fabric.computeClaimInputs) != 2 || len(fixture.fabric.computeClaimCalls) != 0 || len(fixture.fabric.storageIDs) != 0 {
		t.Fatalf("compute claim plan crossed zero-mutation boundary: operation=%#v proofs=%#v claims=%#v storage=%#v", persisted, fixture.fabric.computeClaimInputs, fixture.fabric.computeClaimCalls, fixture.fabric.storageIDs)
	}
}

func TestWorkspaceRecoveryPlanDiagnosePersistsComputeDecisionAuthority(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunchStorage(operation, "unallocated", "storage_attempt_unknown", "")

	response := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID})
	if response.Code != http.StatusOK {
		t.Fatalf("compute claim diagnose status=%d body=%s", response.Code, response.Body.String())
	}
	persisted := fixture.operation(t)
	decision := persisted.CurrentDecision
	if persisted.RecoveryPlan == nil || decision == nil || decision.CurrentStage != "compute_claim" || decision.StageState != "pending" ||
		decision.FirstFalsePredicate != "provider.nodeOwnership" || decision.Expected != "target_owned" || decision.Actual != "unallocated" ||
		decision.NextAction != "NODE_ONLY_CONTINUATION_ONCE" || decision.AllowedMutation != "node_only_continuation" ||
		decision.RequiresApproval || !AuthorizeStageMutation(*decision, "node_only_continuation") {
		t.Fatalf("Recovery Plan was not atomically bound to the Compute Decision: %#v", persisted)
	}
	if len(fixture.fabric.computeClaimCalls) != 0 || len(fixture.fabric.storageIDs) != 0 {
		t.Fatalf("diagnose crossed zero-mutation boundary: claim=%#v storage=%#v", fixture.fabric.computeClaimCalls, fixture.fabric.storageIDs)
	}
}

func TestWorkspaceRecoveryPlanReserveRejectsPersistedDecisionDrift(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunchStorage(operation, "unallocated", "storage_attempt_unknown", "")

	diagnosed := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
	validated := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", map[string]any{
		"planId": diagnosed.PlanID, "planDigest": diagnosed.PlanDigest,
	}))
	drifted := fixture.operation(t)
	if drifted.CurrentDecision == nil {
		t.Fatal("diagnose did not persist CurrentDecision")
	}
	drifted.CurrentDecision.EvidenceDigest = "sha256:" + strings.Repeat("d", 64)
	drifted.CurrentDecision.DecisionVersion++
	mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(drifted)))

	_, won, err := fixture.app.reserveWorkspaceRecoveryExecution(context.Background(), fixture.service, operation.ID, validated.PlanID, validated.PlanDigest, "continue", "usr-admin")
	if !errors.Is(err, errBillingReviewIdentity) || won {
		t.Fatalf("stale Decision authorized Recovery execution: won=%v err=%v operation=%#v", won, err, fixture.operation(t))
	}
	if len(fixture.fabric.computeClaimCalls) != 0 || len(fixture.fabric.storageIDs) != 0 {
		t.Fatalf("Decision drift crossed mutation boundary: claim=%#v storage=%#v", fixture.fabric.computeClaimCalls, fixture.fabric.storageIDs)
	}
}

func TestWorkspaceRecoveryPlanDiagnosePreservesRedactedComputeClaimFailure(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")
	fixture.fabric.computeClaimProof.Eligible = false
	fixture.fabric.computeClaimProof.Reason = "provider_describe"
	fixture.fabric.computeClaimProof.FailureStage = "cvm_tag_readback"
	fixture.fabric.computeClaimProof.ProviderErrorClass = "readback_mismatch"
	fixture.fabric.computeClaimProof.ProviderIdentityFailure = &clients.ComputeClaimProviderIdentityFailure{
		Predicate:      "compute_claim.cvm_ownership.opl_account_id",
		ExpectedDigest: strings.Repeat("a", 64), ActualDigest: strings.Repeat("b", 64),
	}
	fixture.fabric.computeClaimProofErr = errors.New("raw provider response must not escape")

	response := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID})
	if response.Code != http.StatusConflict {
		t.Fatalf("compute claim blocked diagnose status=%d body=%s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"errorCode", "failureStage", "mutationCounts", "providerIdentityFailure", "readbackError", "recoveryEligible", "schemaVersion", "status"}
	actualKeys := make([]string, 0, len(body))
	for key := range body {
		actualKeys = append(actualKeys, key)
	}
	sort.Strings(actualKeys)
	if !equalWorkspaceComputeClaimStrings(actualKeys, wantKeys) || body["schemaVersion"] != float64(1) || body["status"] != "blocked" || body["recoveryEligible"] != false ||
		body["failureStage"] != "cvm_tag_readback" || body["readbackError"] != "readback_mismatch" ||
		body["errorCode"] != "workspace_recovery_plan_fabric_proof_failed" {
		t.Fatalf("compute claim blocked diagnose evidence=%#v", body)
	}
	counts, ok := body["mutationCounts"].(map[string]any)
	if !ok || len(counts) != 3 || counts["sub2api"] != float64(0) || counts["tencent"] != float64(0) || counts["kubernetes"] != float64(0) {
		t.Fatalf("compute claim blocked diagnose mutation counts=%#v", body["mutationCounts"])
	}
	identityFailure, ok := body["providerIdentityFailure"].(map[string]any)
	if !ok || len(identityFailure) != 3 || identityFailure["predicate"] != "compute_claim.cvm_ownership.opl_account_id" ||
		identityFailure["expectedDigest"] != strings.Repeat("a", 64) || identityFailure["actualDigest"] != strings.Repeat("b", 64) {
		t.Fatalf("compute claim provider identity failure=%#v", body["providerIdentityFailure"])
	}
	if strings.Contains(response.Body.String(), "raw provider") || strings.Contains(response.Body.String(), operation.ComputeCVMInstanceID) ||
		strings.Contains(response.Body.String(), operation.AccountID) || strings.Contains(response.Body.String(), operation.ID) {
		t.Fatalf("compute claim blocked diagnose leaked protected evidence: %s", response.Body.String())
	}
	persisted := fixture.operation(t)
	if persisted.RecoveryPlan != nil || persisted.RecoveryExecution != nil || persisted.Status != operation.Status || persisted.Phase != operation.Phase ||
		len(fixture.fabric.computeClaimCalls) != 0 || len(fixture.fabric.storageIDs) != 0 {
		t.Fatalf("blocked diagnose crossed mutation boundary: operation=%#v claims=%#v storage=%#v", persisted, fixture.fabric.computeClaimCalls, fixture.fabric.storageIDs)
	}
}

func TestWorkspaceRecoveryPlanDiagnosePreservesPersistedUnknownCVMClaimEvidence(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")
	fixture.fabric.computeClaimProof.Eligible = false
	fixture.fabric.computeClaimProof.Reason = "provider_describe"
	fixture.fabric.computeClaimProof.FailureStage = "cvm_pre_read"
	fixture.fabric.computeClaimProof.ProviderErrorClass = "readback_mismatch"
	fixture.fabric.computeClaimProof.Evidence = &clients.ComputeClaimEvidence{
		CVM: clients.ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"opl_account_id"}},
	}
	fixture.fabric.computeClaimProofErr = errors.New("provider proof failed")
	evidence := requestHashReconciliationIdentityEvidence()
	evidence.MutationEvidence.CVM.Missing = nil
	useWorkspaceRecoveryPlanIdentityEvidence(t, &fixture, &evidence)

	response := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID})
	if response.Code != http.StatusConflict {
		t.Fatalf("compute claim blocked diagnose status=%d body=%s", response.Code, response.Body.String())
	}
	var rawBody struct {
		ComputeClaimEvidence struct {
			CVM  map[string]json.RawMessage `json:"cvm"`
			Node map[string]json.RawMessage `json:"node"`
		} `json:"computeClaimEvidence"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &rawBody); err != nil {
		t.Fatal(err)
	}
	for name, mutationEvidence := range map[string]map[string]json.RawMessage{
		"cvm":  rawBody.ComputeClaimEvidence.CVM,
		"node": rawBody.ComputeClaimEvidence.Node,
	} {
		var missing []string
		missingJSON, ok := mutationEvidence["missing"]
		if !ok || json.Unmarshal(missingJSON, &missing) != nil || len(missing) != 0 {
			t.Fatalf("compute claim blocked diagnose must preserve explicit empty %s missing evidence: %s", name, response.Body.String())
		}
	}
	var body struct {
		FailureStage         string                                 `json:"failureStage"`
		ReadbackError        string                                 `json:"readbackError"`
		ErrorCode            string                                 `json:"errorCode"`
		MutationCounts       workspaceRecoveryMutationCounts        `json:"mutationCounts"`
		ComputeClaimEvidence *workspaceRecoveryComputeClaimEvidence `json:"computeClaimEvidence"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	got := body.ComputeClaimEvidence
	if body.FailureStage != "cvm_pre_read" || body.ReadbackError != "readback_mismatch" ||
		body.ErrorCode != "workspace_recovery_plan_fabric_proof_failed" || body.MutationCounts != (workspaceRecoveryMutationCounts{}) ||
		got == nil || got.BindingClassification != "request-hash-reconciliation" || got.MismatchField != "binding.requestHash" ||
		got.MutationLedger != "observed" || got.MutationLedgerOutcome != "unknown" ||
		got.CVM.Attempted != 1 || got.CVM.Confirmed != 0 || got.CVM.Unknown != 1 || len(got.CVM.Missing) != 0 ||
		got.Node.Attempted != 0 || got.Node.Confirmed != 0 || got.Node.Unknown != 0 || len(got.Node.Missing) != 0 ||
		got.LedgerFailureStage != "cvm_tag_readback" || got.LedgerProviderErrorClass != "provider_error" ||
		got.FailureStage != "cvm_pre_read" || got.ProviderErrorClass != "readback_mismatch" {
		t.Fatalf("compute claim blocked evidence=%#v body=%s", got, response.Body.String())
	}
	if strings.Contains(response.Body.String(), operation.ComputeCVMInstanceID) || strings.Contains(response.Body.String(), operation.AccountID) ||
		strings.Contains(response.Body.String(), operation.ID) || len(fixture.fabric.computeClaimCalls) != 0 || len(fixture.fabric.storageIDs) != 0 {
		t.Fatalf("blocked diagnose leaked identity or mutated provider: body=%s claims=%d storage=%d", response.Body.String(),
			len(fixture.fabric.computeClaimCalls), len(fixture.fabric.storageIDs))
	}
}

func TestWorkspaceRecoveryPlanValidateReportsComputeIdentityConflictAndKeepsManualReview(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	operation.Status, operation.ErrorCode = "manual_review", "workspace_compute_claim_identity_mismatch"
	mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(operation)))
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")

	diagnosed := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
	driftedCVM := "ins-conflicting-authority"
	fixture.fabric.computeClaimProof.CVMInstanceID = driftedCVM
	response := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", map[string]any{
		"planId": diagnosed.PlanID, "planDigest": diagnosed.PlanDigest,
	})
	if response.Code != http.StatusOK {
		t.Fatalf("identity conflict validate status=%d body=%s", response.Code, response.Body.String())
	}
	blocked := recoveryPlanResponse(t, response)
	if blocked.Status != "blocked" || blocked.ErrorCode != "identity_mismatch" || len(blocked.Mismatches) == 0 {
		t.Fatalf("identity conflict did not produce blocked plan: %#v", blocked)
	}
	found := false
	wantDigests := workspaceComputeClaimIdentityDigestCheck("tencent.cvmInstanceId", operation.ComputeCVMInstanceID, driftedCVM)
	for _, mismatch := range blocked.Mismatches {
		if mismatch.Field == "tencent.cvmInstanceId" && mismatch.Expected == "" && mismatch.Actual == "" &&
			mismatch.ExpectedDigest == wantDigests.ExpectedDigest && mismatch.ActualDigest == wantDigests.ActualDigest {
			found = true
		}
	}
	if !found {
		t.Fatalf("identity conflict omitted exact CVM expected/actual: %#v", blocked.Mismatches)
	}
	persisted := fixture.operation(t)
	if persisted.Status != "manual_review" || persisted.Phase != "compute_claim_pending" || persisted.RecoveryExecution != nil ||
		len(fixture.fabric.computeClaimCalls) != 0 || len(fixture.fabric.storageIDs) != 0 {
		t.Fatalf("identity conflict crossed fail-closed boundary: operation=%#v claims=%#v storage=%#v", persisted, fixture.fabric.computeClaimCalls, fixture.fabric.storageIDs)
	}
	internalExact := false
	for _, mismatch := range persisted.RecoveryPlan.Mismatches {
		internalExact = internalExact || mismatch.Field == "tencent.cvmInstanceId" && mismatch.Expected == operation.ComputeCVMInstanceID && mismatch.Actual == driftedCVM
	}
	if !internalExact {
		t.Fatalf("persisted authority omitted exact CVM expected/actual: %#v", persisted.RecoveryPlan.Mismatches)
	}
}

func TestWorkspaceRecoveryPlanDiagnoseCreatesSuccessorForFailedZeroMutationExecution(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")

	first := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
	failed := fixture.operation(t)
	failed.Status, failed.ErrorCode = "manual_review", "workspace_compute_claim_identity_mismatch"
	failed.RecoveryPlan.Status, failed.RecoveryPlan.ErrorCode = "failed", "workspace_compute_claim_identity_mismatch"
	failed.RecoveryExecution = &workspaceRecoveryExecution{
		ExecutionID: "recovery-exec-failed-zero", RunIdentity: "control-plane-run-failed-zero",
		PlanID: first.PlanID, PlanDigest: first.PlanDigest, ApprovalDigest: strings.Repeat("c", 64), Decision: "continue",
		Status: "failed", StartedAt: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano),
		CompletedAt: time.Now().UTC().Format(time.RFC3339Nano), ErrorCode: failed.ErrorCode,
	}
	mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(failed)))

	successorResponse := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID})
	if successorResponse.Code != http.StatusOK {
		t.Fatalf("successor diagnose status=%d body=%s", successorResponse.Code, successorResponse.Body.String())
	}
	successor := recoveryPlanResponse(t, successorResponse)
	persisted := fixture.operation(t)
	if successor.Status != "diagnosed" || successor.PlanID == first.PlanID || successor.PlanDigest == first.PlanDigest ||
		persisted.RecoveryExecution != nil || len(persisted.RecoveryHistory) != 1 {
		t.Fatalf("failed zero execution did not create successor: first=%#v successor=%#v operation=%#v", first, successor, persisted)
	}
	history := persisted.RecoveryHistory[0]
	if history.Plan.PlanID != first.PlanID || history.Plan.PlanDigest != first.PlanDigest || history.Plan.Status != "failed" ||
		history.Execution.ExecutionID != "recovery-exec-failed-zero" || history.Execution.ErrorCode != failed.ErrorCode ||
		history.Execution.MutationOutcome.Status != "confirmed_zero" || history.Execution.MutationOutcome.Counts != (workspaceRecoveryMutationCounts{}) ||
		persisted.RecoveryPlan.Generation != 1 || persisted.RecoveryPlan.PredecessorPlanDigest != first.PlanDigest ||
		persisted.RecoveryPlan.PredecessorExecutionID != history.Execution.ExecutionID {
		t.Fatalf("successor evidence not preserved: history=%#v current=%#v", history, persisted.RecoveryPlan)
	}
	if len(fixture.fabric.computeClaimCalls) != 0 || len(fixture.fabric.storageIDs) != 0 || len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeIDs) != 1 {
		t.Fatalf("successor diagnose crossed zero-mutation boundary: claims=%d storage=%d charges=%d computes=%d", len(fixture.fabric.computeClaimCalls), len(fixture.fabric.storageIDs), len(fixture.sub2API.charges), len(fixture.fabric.computeIDs))
	}
	replayed := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
	replayedOperation := fixture.operation(t)
	if replayed.PlanID != successor.PlanID || replayed.PlanDigest != successor.PlanDigest || len(replayedOperation.RecoveryHistory) != 1 || replayedOperation.RecoveryExecution != nil {
		t.Fatalf("successor diagnose replay drifted identity: successor=%#v replay=%#v operation=%#v", successor, replayed, replayedOperation)
	}
}

func TestWorkspaceRecoveryPlanValidatePreservesTerminalFailedExecution(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")

	first := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
	failed := fixture.operation(t)
	failed.Status, failed.ErrorCode = "manual_review", "workspace_compute_claim_identity_mismatch"
	failed.RecoveryPlan.Status, failed.RecoveryPlan.ErrorCode = "failed", failed.ErrorCode
	failed.RecoveryExecution = &workspaceRecoveryExecution{
		ExecutionID: "recovery-exec-terminal-failed", RunIdentity: "control-plane-run-terminal-failed",
		PlanID: first.PlanID, PlanDigest: first.PlanDigest, ApprovalDigest: strings.Repeat("c", 64), Decision: "continue",
		Status: "failed", StartedAt: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano),
		CompletedAt: time.Now().UTC().Format(time.RFC3339Nano), ErrorCode: failed.ErrorCode,
	}
	mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(failed)))

	response := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", map[string]any{
		"planId": first.PlanID, "planDigest": first.PlanDigest,
	})
	if response.Code != http.StatusOK {
		t.Fatalf("terminal validate status=%d body=%s", response.Code, response.Body.String())
	}
	terminal := recoveryPlanResponse(t, response)
	persisted := fixture.operation(t)
	if terminal.Status != "failed" || terminal.ErrorCode != failed.ErrorCode || persisted.RecoveryPlan.Status != "failed" ||
		persisted.RecoveryPlan.ValidatedAt != "" || persisted.RecoveryExecution == nil || persisted.RecoveryExecution.Status != "failed" {
		t.Fatalf("terminal validation rewrote failed execution: response=%#v operation=%#v", terminal, persisted)
	}
}

func TestWorkspaceRecoveryPlanDiagnoseCreatesSuccessorAfterTerminalPlanWasBlocked(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")

	first := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
	failed := fixture.operation(t)
	failed.Status, failed.ErrorCode = "manual_review", "workspace_compute_claim_identity_mismatch"
	failed.RecoveryPlan.Status, failed.RecoveryPlan.ErrorCode = "blocked", "identity_mismatch"
	failed.RecoveryPlan.Mismatches = []workspaceRecoveryPlanMismatch{{
		Field: "release.mainSha", Expected: strings.Repeat("d", 40), Actual: strings.Repeat("a", 40),
	}}
	failed.RecoveryExecution = &workspaceRecoveryExecution{
		ExecutionID: "recovery-exec-failed-blocked-plan", RunIdentity: "control-plane-run-failed-blocked-plan",
		PlanID: first.PlanID, PlanDigest: first.PlanDigest, ApprovalDigest: strings.Repeat("c", 64), Decision: "continue",
		Status: "failed", StartedAt: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano),
		CompletedAt: time.Now().UTC().Format(time.RFC3339Nano), ErrorCode: failed.ErrorCode,
	}
	mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(failed)))

	successor := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
	persisted := fixture.operation(t)
	if successor.Status != "diagnosed" || successor.PlanID == first.PlanID || successor.PlanDigest == first.PlanDigest ||
		persisted.RecoveryExecution != nil || len(persisted.RecoveryHistory) != 1 || persisted.RecoveryHistory[0].Plan.Status != "failed" ||
		persisted.RecoveryHistory[0].Execution.Status != "failed" || persisted.RecoveryHistory[0].Execution.MutationOutcome.Status != "confirmed_zero" {
		t.Fatalf("blocked terminal plan did not create successor: first=%#v successor=%#v operation=%#v", first, successor, persisted)
	}
	if len(fixture.fabric.computeClaimCalls) != 0 || len(fixture.fabric.storageIDs) != 0 || len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeIDs) != 1 {
		t.Fatalf("blocked terminal successor crossed zero-mutation boundary: claims=%d storage=%d charges=%d computes=%d", len(fixture.fabric.computeClaimCalls), len(fixture.fabric.storageIDs), len(fixture.sub2API.charges), len(fixture.fabric.computeIDs))
	}
}

func TestWorkspaceRecoveryPlanDiagnoseCreatesSuccessorForAuthoritativeObservedZeroLedger(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")
	useWorkspaceRecoveryPlanIdentityEvidence(t, &fixture, &clients.ComputeClaimIdentityEvidence{
		BindingClassification: "current", BindingDigest: strings.Repeat("b", 64),
		Checks: []clients.ComputeClaimIdentityCheck{{
			Field: "binding.compatibility", Matches: true, Expected: "current_or_historical", Actual: "historical",
		}},
		MutationLedger: "observed", MutationLedgerOutcome: "confirmed_zero", MutationLedgerDigest: strings.Repeat("d", 64),
	})

	first := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
	failed := fixture.operation(t)
	failed.Status, failed.ErrorCode = "manual_review", "workspace_compute_claim_identity_mismatch"
	failed.RecoveryPlan.Status, failed.RecoveryPlan.ErrorCode = "failed", failed.ErrorCode
	failed.RecoveryExecution = &workspaceRecoveryExecution{
		ExecutionID: "recovery-exec-failed-authoritative-zero", RunIdentity: "control-plane-run-failed-authoritative-zero",
		PlanID: first.PlanID, PlanDigest: first.PlanDigest, ApprovalDigest: strings.Repeat("c", 64), Decision: "continue",
		Status: "failed", StartedAt: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano),
		CompletedAt: time.Now().UTC().Format(time.RFC3339Nano), ErrorCode: failed.ErrorCode,
	}
	mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(failed)))

	successor := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
	persisted := fixture.operation(t)
	if successor.Status != "diagnosed" || successor.PlanID == first.PlanID || successor.PlanDigest == first.PlanDigest ||
		persisted.RecoveryExecution != nil || len(persisted.RecoveryHistory) != 1 ||
		persisted.RecoveryHistory[0].Execution.MutationOutcome.Status != "confirmed_zero" ||
		persisted.RecoveryHistory[0].Execution.MutationOutcome.Source != "fabric_mutation_ledger_confirmed_zero" ||
		persisted.RecoveryHistory[0].Execution.MutationOutcome.EvidenceDigest != strings.Repeat("d", 64) {
		t.Fatalf("authoritative zero ledger did not create successor: first=%#v successor=%#v operation=%#v", first, successor, persisted)
	}
	if len(fixture.fabric.computeClaimCalls) != 0 || len(fixture.fabric.storageIDs) != 0 || len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeIDs) != 1 {
		t.Fatalf("authoritative successor crossed zero-mutation boundary: claims=%d storage=%d charges=%d computes=%d", len(fixture.fabric.computeClaimCalls), len(fixture.fabric.storageIDs), len(fixture.sub2API.charges), len(fixture.fabric.computeIDs))
	}
}

func TestWorkspaceRecoveryPlanSuccessorRejectsUnconfirmedFabricLedgerEvidence(t *testing.T) {
	planID, planDigest := "recovery-plan-failed", strings.Repeat("a", 64)
	operation := workspaceLaunchOperation{
		RecoveryPlan: &workspaceRecoveryPlan{
			PlanID: planID, PlanDigest: planDigest, Status: "failed", Action: "compute_claim_continue",
		},
		RecoveryExecution: &workspaceRecoveryExecution{
			ExecutionID: "recovery-exec-failed", PlanID: planID, PlanDigest: planDigest, Status: "failed",
			CompletedAt: time.Now().UTC().Format(time.RFC3339Nano),
		},
	}
	tests := map[string]clients.ComputeClaimIdentityEvidence{
		"nonzero": {
			MutationLedger: "observed", MutationLedgerOutcome: "nonzero", MutationLedgerDigest: strings.Repeat("b", 64),
		},
		"unknown": {
			MutationLedger: "observed", MutationLedgerOutcome: "unknown", MutationLedgerDigest: strings.Repeat("b", 64),
		},
		"missing_digest": {
			MutationLedger: "observed", MutationLedgerOutcome: "confirmed_zero",
		},
		"invalid_digest": {
			MutationLedger: "observed", MutationLedgerOutcome: "confirmed_zero", MutationLedgerDigest: "not-a-digest",
		},
		"contradictory_absent": {
			MutationLedger: "absent", MutationLedgerOutcome: "nonzero", MutationLedgerDigest: strings.Repeat("b", 64),
		},
	}
	for name, evidence := range tests {
		t.Run(name, func(t *testing.T) {
			outcome, gate := workspaceRecoveryExecutionSuccessorGate(operation, &evidence)
			if gate.Allowed {
				t.Fatalf("unconfirmed evidence accepted: outcome=%#v gate=%#v evidence=%#v", outcome, gate, evidence)
			}
			if name == "unknown" && gate.FabricLedgerState != "unknown" {
				t.Fatalf("unknown Fabric evidence was not preserved as unknown: gate=%#v", gate)
			}
		})
	}
}

func TestWorkspaceRecoveryPlanSuccessorAllowsOnlyExactRecoverableCVMOnlyEvidence(t *testing.T) {
	planID, planDigest := "recovery-plan-failed", strings.Repeat("a", 64)
	operation := workspaceLaunchOperation{
		RecoveryPlan: &workspaceRecoveryPlan{
			PlanID: planID, PlanDigest: planDigest, Status: "failed", Action: "compute_claim_continue",
		},
		RecoveryExecution: &workspaceRecoveryExecution{
			ExecutionID: "recovery-exec-failed", PlanID: planID, PlanDigest: planDigest, Status: "failed",
			CompletedAt:     time.Now().UTC().Format(time.RFC3339Nano),
			MutationOutcome: workspaceRecoveryMutationOutcome{Status: "unknown"},
		},
	}
	evidence := recoverableCVMOnlyIdentityEvidence()
	outcome, gate := workspaceRecoveryExecutionSuccessorGate(operation, &evidence)
	if !gate.Allowed || gate.FabricLedgerState != "nonzero" || outcome.Status != "nonzero" ||
		outcome.Counts != (workspaceRecoveryMutationCounts{Tencent: 1}) || outcome.Source != "fabric_mutation_ledger_recoverable_cvm_only" ||
		outcome.EvidenceDigest != evidence.MutationLedgerDigest {
		t.Fatalf("exact recoverable CVM-only evidence was rejected: outcome=%#v gate=%#v", outcome, gate)
	}

	for name, mutate := range map[string]func(*clients.ComputeClaimIdentityEvidence){
		"known legacy": func(candidate *clients.ComputeClaimIdentityEvidence) {
			candidate.BindingClassification = "known-legacy"
		},
		"node attempted": func(candidate *clients.ComputeClaimIdentityEvidence) {
			candidate.MutationEvidence.Node = clients.ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1}
		},
		"cvm unknown": func(candidate *clients.ComputeClaimIdentityEvidence) {
			candidate.MutationEvidence.CVM = clients.ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1}
		},
		"identity mismatch": func(candidate *clients.ComputeClaimIdentityEvidence) { candidate.Checks[0].Matches = false },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := recoverableCVMOnlyIdentityEvidence()
			mutate(&candidate)
			if rejectedOutcome, rejectedGate := workspaceRecoveryExecutionSuccessorGate(operation, &candidate); rejectedGate.Allowed {
				t.Fatalf("unsafe successor evidence accepted: outcome=%#v gate=%#v evidence=%#v", rejectedOutcome, rejectedGate, candidate)
			}
		})
	}
}

func TestWorkspaceRecoveryPlanSuccessorAllowsOnlyExactLegacyKubectlClientRejectedRecordedCall(t *testing.T) {
	planID, planDigest := "recovery-plan-failed", strings.Repeat("a", 64)
	operation := workspaceLaunchOperation{
		RecoveryPlan: &workspaceRecoveryPlan{
			PlanID: planID, PlanDigest: planDigest, Status: "failed", Action: "compute_claim_continue",
		},
		RecoveryExecution: &workspaceRecoveryExecution{
			ExecutionID: "recovery-exec-failed", PlanID: planID, PlanDigest: planDigest, Status: "failed",
			CompletedAt: time.Now().UTC().Format(time.RFC3339Nano),
			MutationOutcome: workspaceRecoveryMutationOutcome{
				Status: "nonzero", Counts: workspaceRecoveryMutationCounts{Kubernetes: 1}, Source: "compute_claim_response",
			},
		},
	}
	evidence := legacyKubectlClientRejectedIdentityEvidence()
	outcome, gate := workspaceRecoveryExecutionSuccessorGate(operation, &evidence)
	if !gate.Allowed || gate.PersistedMutationState != "nonzero" || gate.FabricLedgerState != "absent" ||
		outcome != operation.RecoveryExecution.MutationOutcome {
		t.Fatalf("exact legacy client-rejected call was not admitted: outcome=%#v gate=%#v", outcome, gate)
	}

	for name, mutate := range map[string]func(*workspaceLaunchOperation, *clients.ComputeClaimIdentityEvidence){
		"tencent recorded": func(operation *workspaceLaunchOperation, _ *clients.ComputeClaimIdentityEvidence) {
			operation.RecoveryExecution.MutationOutcome.Counts = workspaceRecoveryMutationCounts{Tencent: 1}
		},
		"fabric operation mutation": func(operation *workspaceLaunchOperation, _ *clients.ComputeClaimIdentityEvidence) {
			operation.RecoveryExecution.MutationOutcome.FabricOperationMutations = 1
		},
		"wrong source": func(operation *workspaceLaunchOperation, _ *clients.ComputeClaimIdentityEvidence) {
			operation.RecoveryExecution.MutationOutcome.Source = "recovery_execution"
		},
		"wrong generation": func(_ *workspaceLaunchOperation, evidence *clients.ComputeClaimIdentityEvidence) {
			evidence.Reconciliation.Generation = "isolated_request_hash_v1"
		},
		"wrong state": func(_ *workspaceLaunchOperation, evidence *clients.ComputeClaimIdentityEvidence) {
			evidence.Reconciliation.State = "node_reserved"
		},
		"wrong provider class": func(_ *workspaceLaunchOperation, evidence *clients.ComputeClaimIdentityEvidence) {
			evidence.Reconciliation.ProviderErrorClass = "transport_error"
		},
		"confirmed node mutation": func(_ *workspaceLaunchOperation, evidence *clients.ComputeClaimIdentityEvidence) {
			evidence.Reconciliation.Node = clients.ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1}
		},
		"unknown node mutation": func(_ *workspaceLaunchOperation, evidence *clients.ComputeClaimIdentityEvidence) {
			evidence.Reconciliation.Node.Unknown = 1
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidateOperation := operation
			candidateExecution := *operation.RecoveryExecution
			candidateOperation.RecoveryExecution = &candidateExecution
			candidateEvidence := legacyKubectlClientRejectedIdentityEvidence()
			mutate(&candidateOperation, &candidateEvidence)
			if rejectedOutcome, rejectedGate := workspaceRecoveryExecutionSuccessorGate(candidateOperation, &candidateEvidence); rejectedGate.Allowed {
				t.Fatalf("drifted legacy client-rejected call was admitted: outcome=%#v gate=%#v", rejectedOutcome, rejectedGate)
			}
		})
	}
}

func TestWorkspaceRecoveryPlanSuccessorClassifiesLegacyKubectlClientRejectedUnknownExecution(t *testing.T) {
	planID, planDigest := "recovery-plan-failed", strings.Repeat("a", 64)
	operation := workspaceLaunchOperation{
		RecoveryPlan: &workspaceRecoveryPlan{
			PlanID: planID, PlanDigest: planDigest, Status: "failed", Action: "compute_claim_continue",
		},
		RecoveryExecution: &workspaceRecoveryExecution{
			ExecutionID: "recovery-exec-failed", PlanID: planID, PlanDigest: planDigest, Status: "failed",
			CompletedAt: time.Now().UTC().Format(time.RFC3339Nano),
			MutationOutcome: workspaceRecoveryMutationOutcome{
				Status: "unknown", Source: "compute_claim_response",
			},
		},
	}
	outcome, gate := workspaceRecoveryExecutionSuccessorGate(operation, func() *clients.ComputeClaimIdentityEvidence {
		evidence := legacyKubectlClientRejectedIdentityEvidence()
		return &evidence
	}())
	if !gate.Allowed || gate.PersistedMutationState != "unknown" || gate.FabricLedgerState != "absent" ||
		outcome.Status != "nonzero" || outcome.Counts != (workspaceRecoveryMutationCounts{Kubernetes: 1}) ||
		outcome.Source != "compute_claim_response" {
		t.Fatalf("legacy client rejection with unclassified execution was not normalized: outcome=%#v gate=%#v", outcome, gate)
	}
}

func TestWorkspaceRecoveryPlanSuccessorRejectsNonterminalPlanStatus(t *testing.T) {
	planID, planDigest := "recovery-plan-failed", strings.Repeat("a", 64)
	operation := workspaceLaunchOperation{
		RecoveryPlan: &workspaceRecoveryPlan{
			PlanID: planID, PlanDigest: planDigest, Status: "validated", Action: "compute_claim_continue",
		},
		RecoveryExecution: &workspaceRecoveryExecution{
			ExecutionID: "recovery-exec-failed", PlanID: planID, PlanDigest: planDigest, Status: "failed",
			CompletedAt:     time.Now().UTC().Format(time.RFC3339Nano),
			MutationOutcome: workspaceRecoveryMutationOutcome{Status: "confirmed_zero"},
		},
	}
	if outcome, ok := workspaceRecoveryExecutionConfirmedZero(operation, nil); ok {
		t.Fatalf("nonterminal plan status accepted: outcome=%#v operation=%#v", outcome, operation)
	}
}

func TestWorkspaceRecoveryPlanDiagnoseProjectsRedactedSuccessorGate(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")

	first := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
	failed := fixture.operation(t)
	failed.Status, failed.ErrorCode = "manual_review", "workspace_compute_claim_identity_mismatch"
	failed.RecoveryPlan.Status, failed.RecoveryPlan.ErrorCode = "blocked", "identity_mismatch"
	failed.RecoveryExecution = &workspaceRecoveryExecution{
		ExecutionID: "recovery-exec-held-lease", RunIdentity: "control-plane-run-held-lease",
		PlanID: first.PlanID, PlanDigest: first.PlanDigest, ApprovalDigest: strings.Repeat("c", 64), Decision: "continue",
		Status: "failed", StartedAt: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano),
		CompletedAt: time.Now().UTC().Format(time.RFC3339Nano), ErrorCode: failed.ErrorCode,
		LeaseToken: strings.Repeat("d", 64), LeaseExpiresAt: time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano),
	}
	mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(failed)))

	response := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID})
	if response.Code != http.StatusOK {
		t.Fatalf("successor gate diagnose status=%d body=%s", response.Code, response.Body.String())
	}
	rawBody := append([]byte(nil), response.Body.Bytes()...)
	var body map[string]any
	if err := json.Unmarshal(rawBody, &body); err != nil {
		t.Fatal(err)
	}
	gate, ok := body["successorGate"].(map[string]any)
	if !ok {
		t.Fatalf("successor gate missing: %#v", body)
	}
	want := map[string]any{
		"applicable": true, "allowed": false, "planState": "terminal",
		"executionState": "failed", "completionState": "completed", "leaseState": "held",
		"identityState": "matches", "persistedMutationState": "missing", "fabricLedgerState": "absent",
	}
	for field, expected := range want {
		if gate[field] != expected {
			t.Fatalf("successor gate %s=%#v, want %#v: %#v", field, gate[field], expected, gate)
		}
	}
	encoded := string(rawBody)
	if strings.Contains(encoded, strings.Repeat("c", 64)) || strings.Contains(encoded, strings.Repeat("d", 64)) ||
		strings.Contains(encoded, failed.ComputeCVMInstanceID) || strings.Contains(encoded, failed.ComputeNodeName) ||
		strings.Contains(encoded, "approvalDigest") || strings.Contains(encoded, "leaseToken") || strings.Contains(encoded, "leaseExpiresAt") ||
		strings.Contains(encoded, "mutationLedgerDigest") {
		t.Fatalf("successor gate leaked protected identity: %s", encoded)
	}
	persisted := fixture.operation(t)
	if persisted.RecoveryPlan.PlanID != first.PlanID || persisted.RecoveryExecution == nil ||
		persisted.RecoveryExecution.ExecutionID != failed.RecoveryExecution.ExecutionID || len(persisted.RecoveryHistory) != 0 {
		t.Fatalf("successor gate diagnosis mutated terminal history: %#v", persisted)
	}
}

func TestWorkspaceRecoveryPlanDiagnoseKeepsFailedExecutionWithNonzeroMutationEvidence(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")
	first := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
	failed := fixture.operation(t)
	failed.Status, failed.ErrorCode = "manual_review", "workspace_compute_claim_identity_mismatch"
	failed.RecoveryPlan.Status, failed.RecoveryPlan.ErrorCode = "failed", failed.ErrorCode
	failed.RecoveryExecution = &workspaceRecoveryExecution{
		ExecutionID: "recovery-exec-failed-nonzero", RunIdentity: "control-plane-run-failed-nonzero",
		PlanID: first.PlanID, PlanDigest: first.PlanDigest, ApprovalDigest: strings.Repeat("c", 64), Decision: "continue",
		Status: "failed", StartedAt: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano),
		CompletedAt: time.Now().UTC().Format(time.RFC3339Nano), ErrorCode: failed.ErrorCode,
		MutationOutcome: workspaceRecoveryMutationOutcome{
			Status: "nonzero", Counts: workspaceRecoveryMutationCounts{Kubernetes: 1}, Source: "compute_claim_response",
		},
	}
	mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(failed)))

	replayed := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
	persisted := fixture.operation(t)
	if replayed.PlanID != first.PlanID || replayed.PlanDigest != first.PlanDigest || replayed.Status != "failed" ||
		persisted.RecoveryExecution == nil || persisted.RecoveryExecution.ExecutionID != failed.RecoveryExecution.ExecutionID || len(persisted.RecoveryHistory) != 0 ||
		len(fixture.fabric.computeClaimCalls) != 0 || len(fixture.fabric.storageIDs) != 0 {
		t.Fatalf("nonzero failed execution was replaced or repeated: replay=%#v operation=%#v", replayed, persisted)
	}
}

func TestWorkspaceRecoveryPlanDiagnoseCreatesSuccessorForExactLegacyKubectlClientRejectedRecordedCall(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")
	evidence := legacyKubectlClientRejectedIdentityEvidence()
	useWorkspaceRecoveryPlanIdentityEvidence(t, &fixture, &evidence)

	first := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
	failed := fixture.operation(t)
	failed.Status, failed.ErrorCode = "manual_review", "workspace_compute_claim_provider_describe"
	failed.RecoveryPlan.Status, failed.RecoveryPlan.ErrorCode = "failed", failed.ErrorCode
	failed.RecoveryExecution = &workspaceRecoveryExecution{
		ExecutionID: "recovery-exec-client-rejected", RunIdentity: "control-plane-run-client-rejected",
		PlanID: first.PlanID, PlanDigest: first.PlanDigest, ApprovalDigest: strings.Repeat("c", 64), Decision: "continue",
		Status: "failed", StartedAt: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano),
		CompletedAt: time.Now().UTC().Format(time.RFC3339Nano), ErrorCode: failed.ErrorCode,
		MutationOutcome: workspaceRecoveryMutationOutcome{
			Status: "nonzero", Counts: workspaceRecoveryMutationCounts{Kubernetes: 1}, Source: "compute_claim_response",
		},
	}
	mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(failed)))

	successor := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
	persisted := fixture.operation(t)
	if successor.Status != "diagnosed" || successor.PlanID == first.PlanID || successor.PlanDigest == first.PlanDigest ||
		successor.SuccessorGate == nil || !successor.SuccessorGate.Allowed || persisted.RecoveryExecution != nil || len(persisted.RecoveryHistory) != 1 ||
		persisted.RecoveryPlan == nil || persisted.RecoveryPlan.Generation != 1 ||
		persisted.RecoveryHistory[0].Execution.MutationOutcome.Status != "nonzero" ||
		persisted.RecoveryHistory[0].Execution.MutationOutcome.Counts != (workspaceRecoveryMutationCounts{Kubernetes: 1}) {
		t.Fatalf("legacy client-rejected execution did not create one successor: first=%#v successor=%#v operation=%#v", first, successor, persisted)
	}
	if len(fixture.fabric.computeClaimCalls) != 0 || len(fixture.fabric.storageIDs) != 0 || len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeIDs) != 1 {
		t.Fatalf("client-rejected successor crossed zero-mutation boundary: claims=%d storage=%d charges=%d computes=%d", len(fixture.fabric.computeClaimCalls), len(fixture.fabric.storageIDs), len(fixture.sub2API.charges), len(fixture.fabric.computeIDs))
	}
}

func TestWorkspaceRecoveryPlanDiagnosePersistsFieldMismatch(t *testing.T) {
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	proof := computeClaimRecoveryProofForLaunch(operation, "unallocated")
	input := workspaceComputeClaimRecoveryRequestForOperation(operation)
	evidence := &clients.ComputeClaimIdentityEvidence{
		BindingClassification: "current", BindingDigest: strings.Repeat("b", 64),
		Checks: []clients.ComputeClaimIdentityCheck{{
			Field: "binding.operationId", Matches: false, Expected: operation.ID + ":compute", Actual: "op-conflict",
		}},
		MutationLedger: "absent",
	}
	plan, err := newWorkspaceComputeClaimRecoveryPlan(operation, input, proof, evidence, workspaceRecoveryReleaseBinding{
		MainSHA: strings.Repeat("a", 40), CloudImageDigest: "sha256:" + strings.Repeat("b", 64), WorkspaceImageDigest: deployedImageDigest(operation.WorkspaceImageDigest),
	})
	if err != nil || plan.Status != "blocked" || len(plan.Mismatches) != 1 || plan.Mismatches[0].Field != "binding.operationId" ||
		plan.Mismatches[0].Expected != operation.ID+":compute" || plan.Mismatches[0].Actual != "op-conflict" || len(fixture.fabric.computeClaimCalls) != 0 {
		t.Fatalf("diagnose mismatch plan=%#v err=%v", plan, err)
	}
}

func TestWorkspaceRecoveryPlanRejectsClassificationOnlyBindingAuthority(t *testing.T) {
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	proof := computeClaimRecoveryProofForLaunch(operation, "unallocated")
	evidence := recoverableCVMOnlyIdentityEvidence()
	evidence.BindingClassification = "known-legacy"
	plan, err := newWorkspaceComputeClaimRecoveryPlan(
		operation, workspaceComputeClaimRecoveryRequestForOperation(operation), proof, &evidence,
		workspaceRecoveryReleaseBinding{
			MainSHA: strings.Repeat("a", 40), CloudImageDigest: "sha256:" + strings.Repeat("b", 64),
			WorkspaceImageDigest: deployedImageDigest(operation.WorkspaceImageDigest),
		},
	)
	if err != nil || plan.Status != "blocked" || len(plan.Mismatches) == 0 || len(fixture.fabric.computeClaimCalls) != 0 {
		t.Fatalf("classification-only binding became recovery authority: plan=%#v err=%v", plan, err)
	}
	found := false
	for _, mismatch := range plan.Mismatches {
		if mismatch.Field == "fabric.bindingRecoveryAuthority" {
			found = true
		}
	}
	if !found {
		t.Fatalf("classification-only binding mismatch was not explicit: %#v", plan.Mismatches)
	}
}

func TestWorkspaceRecoveryPlanExecuteComputeClaimContinuesOriginalLaunchOnce(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")
	claimed := computeClaimRecoveryProofForLaunch(operation, "target_owned")
	fixture.fabric.computeClaimResult = &claimed
	configureWorkspaceLaunchFulfillment(t, fixture)
	configureWorkspaceComputeClaimReadback(fixture, operation)

	diagnosed := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
	validated := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", map[string]any{
		"planId": diagnosed.PlanID, "planDigest": diagnosed.PlanDigest,
	}))
	executeBody := map[string]any{
		"planId": validated.PlanID, "planDigest": validated.PlanDigest, "decision": "continue", "confirmation": "CONTINUE_RECOVERY_PLAN",
	}
	firstResponse := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/execute", executeBody)
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("compute claim execute status=%d body=%s", firstResponse.Code, firstResponse.Body.String())
	}
	first := recoveryPlanResponse(t, firstResponse)
	if first.Status != "completed" || first.ExecutionID == "" || first.RunID == "" || first.URL == "" || first.ReceiptID == "" {
		t.Fatalf("compute claim executed plan=%#v", first)
	}
	claimCalls, storageCreates := len(fixture.fabric.computeClaimCalls), len(fixture.fabric.storageIDs)
	secondResponse := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/execute", executeBody)
	if secondResponse.Code != http.StatusOK {
		t.Fatalf("compute claim replay status=%d body=%s", secondResponse.Code, secondResponse.Body.String())
	}
	second := recoveryPlanResponse(t, secondResponse)
	if second.ExecutionID != first.ExecutionID || second.RunID != first.RunID || len(fixture.fabric.computeClaimCalls) != claimCalls || len(fixture.fabric.storageIDs) != storageCreates ||
		claimCalls != 1 || storageCreates != 1 || len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeIDs) != 1 {
		t.Fatalf("compute claim replay repeated mutation: first=%#v second=%#v claims=%d storage=%d charges=%d computes=%d", first, second, len(fixture.fabric.computeClaimCalls), len(fixture.fabric.storageIDs), len(fixture.sub2API.charges), len(fixture.fabric.computeIDs))
	}
	persisted := fixture.operation(t)
	executionWire := structToMap(*persisted.RecoveryExecution)
	expectedReviewer := sessionUserIDForTest(t, fixture.server, fixture.operator)
	if persisted.Status != "succeeded" || persisted.Phase != "succeeded" || persisted.ComputeClaimApproval == nil || persisted.ComputeClaimProof == nil ||
		persisted.RecoveryExecution == nil || persisted.RecoveryExecution.ExecutionID != first.ExecutionID || stringValue(executionWire["reviewer"]) != expectedReviewer {
		t.Fatalf("compute claim execution not persisted on original launch: %#v", persisted)
	}
}

func TestWorkspaceRecoveryPlanWorkerRetriesCBSReadbackAfterConfirmedNodeClaim(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")
	claimed := computeClaimRecoveryProofForLaunch(operation, "target_owned")
	fixture.fabric.computeClaimResult = &claimed
	configureWorkspaceLaunchFulfillment(t, fixture)
	configureWorkspaceComputeClaimReadback(fixture, operation)
	pendingStorage := fixture.fabric.storageSync
	pendingStorage.Status, pendingStorage.CBSStatus = "provisioning", "CREATING"
	fixture.fabric.storageSync = pendingStorage
	fixture.fabric.mutateStorage = func(created *clients.StorageVolume) { *created = pendingStorage }

	diagnosed := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
	validated := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", map[string]any{
		"planId": diagnosed.PlanID, "planDigest": diagnosed.PlanDigest,
	}))
	execute := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/execute", map[string]any{
		"planId": validated.PlanID, "planDigest": validated.PlanDigest, "decision": "continue", "confirmation": "CONTINUE_RECOVERY_PLAN",
	})
	if execute.Code != http.StatusOK {
		t.Fatalf("CBS waiting execute status=%d body=%s", execute.Code, execute.Body.String())
	}
	waitingPlan := recoveryPlanResponse(t, execute)
	waiting := fixture.operation(t)
	if waitingPlan.Status != "executing" || waiting.Status != "waiting" || waiting.Phase != "storage_fulfilling" || waiting.ComputeClaimProof == nil ||
		waiting.RecoveryExecution == nil || waiting.RecoveryExecution.LeaseToken != "" || waiting.ContinuationAttemptBudgets["storage"] != (workspaceLaunchStageBudget{Attempted: 1, Max: 1}) {
		t.Fatalf("CBS waiting recovery plan=%#v launch=%#v", waitingPlan, waiting)
	}

	fixture.fabric.storageSyncErr = errors.New("transient CBS Describe failure")
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err == nil {
		t.Fatal("transient CBS Describe failure was not returned")
	}
	retryable := fixture.operation(t)
	if retryable.Status != "retryable" || retryable.Phase != "storage_fulfilling" || retryable.RecoveryPlan == nil || retryable.RecoveryPlan.Status != "executing" ||
		retryable.RecoveryExecution == nil || retryable.RecoveryExecution.Status != "running" {
		t.Fatalf("transient CBS readback was not retryable: %#v", retryable)
	}
	fixture.fabric.storageSyncErr = nil
	fixture.fabric.mutateStorage = nil
	readyStorage := pendingStorage
	readyStorage.Status, readyStorage.CBSStatus = "available", "UNATTACHED"
	fixture.fabric.storageSync = readyStorage
	restarted, err := newControlPlaneAppWithStore(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	completed, found, err := restarted.workspaceLaunchOperation(context.Background(), operation.ID)
	if err != nil || !found || completed.Status != "succeeded" || completed.Phase != "succeeded" || completed.URL == "" || completed.ReceiptID == "" ||
		completed.RecoveryPlan == nil || completed.RecoveryPlan.Status != "completed" || completed.RecoveryExecution == nil || completed.RecoveryExecution.Status != "completed" ||
		completed.ContinuationAttemptBudgets["storage"] != (workspaceLaunchStageBudget{Attempted: 1, Confirmed: 1, Max: 1}) {
		t.Fatalf("CBS restart terminal readback launch=%#v found=%v err=%v", completed, found, err)
	}
	if len(fixture.fabric.computeClaimCalls) != 1 || len(fixture.fabric.storageIDs) != 3 || len(fixture.fabric.storageCreateKeys) != len(fixture.fabric.storageIDs) ||
		len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeIDs) != 1 ||
		len(fixture.ledger.receiptInputs) != 1 {
		t.Fatalf("CBS retry repeated mutation: claims=%d storage=%d charges=%d compute=%d receipts=%d", len(fixture.fabric.computeClaimCalls), len(fixture.fabric.storageIDs), len(fixture.sub2API.charges), len(fixture.fabric.computeIDs), len(fixture.ledger.receiptInputs))
	}
	for index, storageID := range fixture.fabric.storageIDs {
		if storageID != operation.StorageID || fixture.fabric.storageCreateKeys[index] != operation.ID+":storage" {
			t.Fatalf("CBS retry changed original operation identity: ids=%#v keys=%#v", fixture.fabric.storageIDs, fixture.fabric.storageCreateKeys)
		}
	}
}
