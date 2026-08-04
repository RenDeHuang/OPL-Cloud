package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"opl-cloud/services/control-plane/internal/clients"
)

func recoveryAcceptanceCanaryTestApproval(operation workspaceLaunchOperation, nonce string) recoveryAcceptanceCanaryApproval {
	approval := recoveryAcceptanceCanaryApproval{
		AccountID:         operation.AccountID,
		LaunchOperationID: operation.ID,
		MergedMainSHA:     strings.Repeat("a", 40),
		CloudImageDigest:  "sha256:" + strings.Repeat("b", 64),
		Nonce:             nonce,
	}
	approval.ApprovalDigest = recoveryAcceptanceCanaryDigest(approval)
	return approval
}

func recoveryAcceptanceCanaryEligibleOperation(t *testing.T) (workspaceLaunchWorkerFixture, workspaceLaunchOperation) {
	t.Helper()
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	proof := computeClaimRecoveryProofForLaunch(operation, "target_owned")
	proof.Evidence = &clients.ComputeClaimEvidence{
		CVM:  clients.ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1},
		Node: clients.ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1},
	}
	proof.TencentMutationCount, proof.KubernetesMutationCount = 1, 1
	operation.Status, operation.Phase, operation.ErrorCode = "preparing", "storage_fulfilling", ""
	operation.ComputeClaimProof = &proof
	releaseWorkspaceLaunchLease(&operation)
	mustStore(t, fixture.store.memoryTableStore.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(operation)))
	return fixture, operation
}

func TestRecoveryAcceptanceCanaryApprovalDigestBindsLaunchAndNonce(t *testing.T) {
	_, operation := recoveryAcceptanceCanaryEligibleOperation(t)
	approval := recoveryAcceptanceCanaryTestApproval(operation, strings.Repeat("c", 32))
	input := map[string]any{
		"accountId": operation.AccountID, "launchOperationId": operation.ID,
		"mergedMainSha": approval.MergedMainSHA, "cloudImageDigest": approval.CloudImageDigest,
		"approvalDigest": approval.ApprovalDigest, "nonce": approval.Nonce,
	}
	parsed, err := parseRecoveryAcceptanceCanaryApproval(input, operation.ID)
	if err != nil || parsed.ApprovalDigest != approval.ApprovalDigest {
		t.Fatalf("valid approval rejected: approval=%#v err=%v", approval, err)
	}
	input["launchOperationId"] = operation.ID + "-other"
	if _, err := parseRecoveryAcceptanceCanaryApproval(input, operation.ID); err == nil {
		t.Fatal("launch identity drift was accepted")
	}
}

func TestRecoveryAcceptanceCanaryIsDefaultOff(t *testing.T) {
	fixture, operation := recoveryAcceptanceCanaryEligibleOperation(t)
	t.Setenv("OPL_RECOVERY_ACCEPTANCE_CANARY_ENABLED", "")
	t.Setenv("OPL_RECOVERY_ACCEPTANCE_CANARY_ACCOUNT_IDS", operation.AccountID)
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "registry.example/opl-cloud@sha256:"+strings.Repeat("b", 64))
	approval := recoveryAcceptanceCanaryTestApproval(operation, strings.Repeat("d", 32))
	if _, err := fixture.app.executeRecoveryAcceptanceCanary(context.Background(), operation.ID, approval); err != errRecoveryAcceptanceCanaryDisabled {
		t.Fatalf("default-off gate err=%v", err)
	}
}

func TestRecoveryAcceptanceCanaryConvergesOriginalLaunchOnce(t *testing.T) {
	fixture, operation := recoveryAcceptanceCanaryEligibleOperation(t)
	t.Setenv("OPL_RECOVERY_ACCEPTANCE_CANARY_ENABLED", "1")
	t.Setenv("OPL_RECOVERY_ACCEPTANCE_CANARY_ACCOUNT_IDS", operation.AccountID)
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "registry.example/opl-cloud@sha256:"+strings.Repeat("b", 64))
	approval := recoveryAcceptanceCanaryTestApproval(operation, strings.Repeat("e", 32))
	beforeEvents := len(*fixture.events)
	first, err := fixture.app.executeRecoveryAcceptanceCanary(context.Background(), operation.ID, approval)
	if err != nil || first["status"] != "manual_review" || first["phase"] != "storage_fulfilling" || first["approvalDigest"] != approval.ApprovalDigest {
		t.Fatalf("canary convergence failed: result=%#v err=%v", first, err)
	}
	current := fixture.operation(t)
	if current.Status != "manual_review" || current.Phase != "storage_fulfilling" || current.ErrorCode != recoveryAcceptanceCanaryErrorCode ||
		current.RecoveryCanaryDigest != approval.ApprovalDigest || len(*fixture.events) != beforeEvents {
		t.Fatalf("canary mutated unexpected state: current=%#v events=%#v", current, *fixture.events)
	}
	second, err := fixture.app.executeRecoveryAcceptanceCanary(context.Background(), operation.ID, approval)
	if err != nil || second["approvalDigest"] != approval.ApprovalDigest || len(*fixture.events) != beforeEvents {
		t.Fatalf("same approval was not idempotent: result=%#v err=%v", second, err)
	}
	drifted := recoveryAcceptanceCanaryTestApproval(operation, strings.Repeat("f", 32))
	if _, err := fixture.app.executeRecoveryAcceptanceCanary(context.Background(), operation.ID, drifted); err != errRecoveryAcceptanceCanaryReplayConflict {
		t.Fatalf("nonce replay drift was accepted: err=%v", err)
	}
}

func TestRecoveryAcceptanceCanaryRejectsStorageStarted(t *testing.T) {
	fixture, operation := recoveryAcceptanceCanaryEligibleOperation(t)
	operation.ContinuationAttemptBudgets["storage"] = workspaceLaunchStageBudget{Attempted: 1, Max: 1}
	mustStore(t, fixture.store.memoryTableStore.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(operation)))
	t.Setenv("OPL_RECOVERY_ACCEPTANCE_CANARY_ENABLED", "1")
	t.Setenv("OPL_RECOVERY_ACCEPTANCE_CANARY_ACCOUNT_IDS", operation.AccountID)
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "registry.example/opl-cloud@sha256:"+strings.Repeat("b", 64))
	approval := recoveryAcceptanceCanaryTestApproval(operation, strings.Repeat("1", 32))
	if _, err := fixture.app.executeRecoveryAcceptanceCanary(context.Background(), operation.ID, approval); err != errRecoveryAcceptanceCanaryLaunchInvalid {
		t.Fatalf("storage-started launch was accepted: err=%v", err)
	}
}

func TestRecoveryAcceptanceCanaryRejectsStorageResourceInUnstartedProof(t *testing.T) {
	fixture, operation := recoveryAcceptanceCanaryEligibleOperation(t)
	operation.ComputeClaimProof.StorageProviderResourceID = "disk-existing"
	mustStore(t, fixture.store.memoryTableStore.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(operation)))
	t.Setenv("OPL_RECOVERY_ACCEPTANCE_CANARY_ENABLED", "1")
	t.Setenv("OPL_RECOVERY_ACCEPTANCE_CANARY_ACCOUNT_IDS", operation.AccountID)
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "registry.example/opl-cloud@sha256:"+strings.Repeat("b", 64))
	approval := recoveryAcceptanceCanaryTestApproval(operation, strings.Repeat("2", 32))
	if _, err := fixture.app.executeRecoveryAcceptanceCanary(context.Background(), operation.ID, approval); err != errRecoveryAcceptanceCanaryLaunchInvalid {
		t.Fatalf("unstarted proof with a storage resource was accepted: err=%v", err)
	}
}

func TestRecoveryAcceptanceCanaryRejectsUnboundedMutationEvidence(t *testing.T) {
	fixture, operation := recoveryAcceptanceCanaryEligibleOperation(t)
	operation.ComputeClaimProof.Evidence.CVM.Attempted = 6
	operation.ComputeClaimProof.Evidence.CVM.Confirmed = 6
	operation.ComputeClaimProof.TencentMutationCount = 6
	mustStore(t, fixture.store.memoryTableStore.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(operation)))
	t.Setenv("OPL_RECOVERY_ACCEPTANCE_CANARY_ENABLED", "1")
	t.Setenv("OPL_RECOVERY_ACCEPTANCE_CANARY_ACCOUNT_IDS", operation.AccountID)
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "registry.example/opl-cloud@sha256:"+strings.Repeat("b", 64))
	approval := recoveryAcceptanceCanaryTestApproval(operation, strings.Repeat("3", 32))
	if _, err := fixture.app.executeRecoveryAcceptanceCanary(context.Background(), operation.ID, approval); err != errRecoveryAcceptanceCanaryLaunchInvalid {
		t.Fatalf("unbounded mutation evidence was accepted: err=%v", err)
	}
}

func TestRecoveryAcceptanceCanaryPreStorageHookStopsBeforeStorage(t *testing.T) {
	fixture, operation := recoveryAcceptanceCanaryEligibleOperation(t)
	operation.Status, operation.Phase, operation.ErrorCode = "preparing", "compute_fulfilling", ""
	mustStore(t, fixture.store.memoryTableStore.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(operation)))
	operation.PersistedResult = stringValue(workspaceLaunchOperationRow(operation)["result"])
	approval := recoveryAcceptanceCanaryTestApproval(operation, strings.Repeat("2", 32))
	approvalJSON, err := json.Marshal(map[string]any{
		"schemaVersion": 1, "operationMode": "recovery_acceptance_canary",
		"accountId": approval.AccountID, "launchOperationId": approval.LaunchOperationID,
		"mergedMainSha": approval.MergedMainSHA, "cloudImageDigest": approval.CloudImageDigest,
		"approvalDigest": approval.ApprovalDigest, "nonce": approval.Nonce,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPL_RECOVERY_ACCEPTANCE_CANARY_ENABLED", "1")
	t.Setenv("OPL_RECOVERY_ACCEPTANCE_CANARY_ACCOUNT_IDS", operation.AccountID)
	t.Setenv("OPL_RECOVERY_ACCEPTANCE_CANARY_APPROVAL_JSON", string(approvalJSON))
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "registry.example/opl-cloud@sha256:"+strings.Repeat("b", 64))
	beforeEvents := append([]string(nil), (*fixture.events)...)
	entered, err := fixture.app.enterRecoveryAcceptanceCanaryAtStorageBoundary(context.Background(), &operation)
	if err != nil || !entered {
		t.Fatalf("pre-storage canary did not converge: entered=%v err=%v", entered, err)
	}
	current := fixture.operation(t)
	if current.Status != "manual_review" || current.Phase != "storage_fulfilling" || current.RecoveryCanaryDigest != approval.ApprovalDigest {
		t.Fatalf("pre-storage canary state=%#v", current)
	}
	if current.ContinuationAttemptBudgets["storage"].Attempted != 0 || current.ContinuationAttemptBudgets["storage"].Confirmed != 0 || current.ContinuationAttemptBudgets["storage"].Unknown != 0 {
		t.Fatalf("storage budget was reserved: %#v", current.ContinuationAttemptBudgets["storage"])
	}
	if _, ok := fixture.app.getStorage(operation.StorageID); ok || countStrings(*fixture.events, "fabric.storage") != countStrings(beforeEvents, "fabric.storage") {
		t.Fatalf("pre-storage canary crossed storage boundary: events=%#v", *fixture.events)
	}
}
