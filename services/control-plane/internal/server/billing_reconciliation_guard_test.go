package server

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"opl-cloud/services/control-plane/internal/clients"
)

func billingReconciliationRowForTest(id, status, reason string) map[string]any {
	return map[string]any{
		"id": id, "status": status,
		"guard":  map[string]any{"status": status, "reason": reason, "blockNewWorkspaces": status == "mismatch"},
		"report": map[string]any{"id": id, "status": status},
	}
}

func billingReconciliationMutationForTest(row map[string]any, expectedCurrentGuardID string) billingReconciliationMutation {
	resultID := stringValue(row["id"])
	return billingReconciliationMutation{
		Row: row, ExpectedCurrentGuardID: expectedCurrentGuardID,
		AuditEvent: map[string]any{
			"id": billingReconciliationAuditID(resultID), "actorUserId": "usr-admin", "actorRole": "admin", "actorAccountId": "acct-admin",
			"action": "billing.reconciliation", "resourceKind": "billing_reconciliation", "resourceId": resultID,
			"before": nil, "after": row, "result": "succeeded", "createdAt": "2026-08-21T08:00:00Z",
		},
	}
}

func applyBillingReconciliationForTest(t *testing.T, store controlPlaneTableStore, row map[string]any, expectedCurrentGuardID string) {
	t.Helper()
	if err := store.ApplyBillingReconciliation(context.Background(), billingReconciliationMutationForTest(row, expectedCurrentGuardID)); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryBillingReconciliationReplayAndCASAreFailClosed(t *testing.T) {
	store := newMemoryTableStore()
	clean := billingReconciliationRowForTest("reconcile-clean", "ok", "operator_reconciliation")
	mismatch := billingReconciliationRowForTest("reconcile-mismatch", "mismatch", "operator_reconciliation")
	applyBillingReconciliationForTest(t, store, clean, "")
	applyBillingReconciliationForTest(t, store, mismatch, "reconcile-clean")

	if err := store.ApplyBillingReconciliation(context.Background(), billingReconciliationMutationForTest(clean, "reconcile-mismatch")); err != nil {
		t.Fatalf("same-result replay error=%v", err)
	}
	current, found, err := store.BillingReconciliation(context.Background())
	if err != nil || !found || stringValue(current["id"]) != "reconcile-mismatch" {
		t.Fatalf("old clean replay changed current guard: current=%#v found=%v err=%v", current, found, err)
	}
	reportConflict := billingReconciliationRowForTest("reconcile-clean", "ok", "operator_reconciliation")
	reportConflict["report"].(map[string]any)["changed"] = true
	if err := store.ApplyBillingReconciliation(context.Background(), billingReconciliationMutationForTest(reportConflict, "reconcile-mismatch")); !errors.Is(err, errIdempotencyConflict) {
		t.Fatalf("same ID report conflict error=%v", err)
	}

	conflict := billingReconciliationRowForTest("reconcile-clean", "mismatch", "operator_reconciliation")
	if err := store.ApplyBillingReconciliation(context.Background(), billingReconciliationMutationForTest(conflict, "reconcile-mismatch")); !errors.Is(err, errIdempotencyConflict) {
		t.Fatalf("same ID conflict error=%v", err)
	}
	stale := billingReconciliationRowForTest("reconcile-stale", "ok", "operator_reconciliation")
	if err := store.ApplyBillingReconciliation(context.Background(), billingReconciliationMutationForTest(stale, "reconcile-clean")); !errors.Is(err, errBillingReconciliationCASConflict) {
		t.Fatalf("stale current CAS error=%v", err)
	}
}

func TestMemoryBillingReconciliationAuditConflictRollsBackGuard(t *testing.T) {
	store := newMemoryTableStore()
	clean := billingReconciliationRowForTest("reconcile-clean", "ok", "operator_reconciliation")
	applyBillingReconciliationForTest(t, store, clean, "")
	mismatch := billingReconciliationRowForTest("reconcile-mismatch", "mismatch", "operator_reconciliation")
	mutation := billingReconciliationMutationForTest(mismatch, "reconcile-clean")
	store.auditEvents = append(store.auditEvents, map[string]any{
		"id": mutation.AuditEvent["id"], "actorUserId": "usr-attacker", "action": "other.action", "resourceKind": "other", "resourceId": "other", "result": "succeeded",
	})
	if err := store.ApplyBillingReconciliation(context.Background(), mutation); !errors.Is(err, errIdempotencyConflict) {
		t.Fatalf("conflicting audit error=%v", err)
	}
	current, _, _ := store.BillingReconciliation(context.Background())
	if stringValue(current["id"]) != "reconcile-clean" {
		t.Fatalf("guard changed despite audit conflict: %#v", current)
	}
}

func TestWorkspaceLaunchFailsClosedWhenReconciliationReadFails(t *testing.T) {
	fixture := newBillingReconciliationFixture(t)
	fixture.store.reconciliationErr = errors.New("reconciliation store unavailable")
	response := requestWithMutationKeyForTest(t, fixture.server, fixture.member, http.MethodPost, "/api/workspace-launches", `{"name":"Alpha","packageId":"basic","autoRenew":false}`, "guard-read-failure")
	assertErrorResponse(t, response.Code, response.Body.String(), http.StatusInternalServerError, "state_read_failed")
	if len(*fixture.calls) != 0 || len(fixture.sub2API.charges) != 0 || len(fixture.store.runtimeOps) != 0 {
		t.Fatalf("guard read failure reached downstream mutation: fabric=%#v charges=%#v operations=%#v", *fixture.calls, fixture.sub2API.charges, fixture.store.runtimeOps)
	}
}

func TestBillingReconciliationMismatchBlocksAndFreshCleanResumesNewClaim(t *testing.T) {
	fixture := newBillingReconciliationFixture(t)
	originalHistory := append([]clients.Sub2APIBalanceHistoryEntry(nil), fixture.sub2API.history[41]...)
	fixture.sub2API.history[41] = fixture.sub2API.history[41][1:]
	operator := operatorSessionForTest(t, fixture.server)
	mismatch := requestWithMutationKeyForTest(t, fixture.server, operator, http.MethodPost, "/api/billing/reconciliation", `{"confirm":true}`, "guard-mismatch")
	if mismatch.Code != http.StatusCreated {
		t.Fatalf("mismatch status=%d body=%s", mismatch.Code, mismatch.Body.String())
	}
	blocked := requestWithMutationKeyForTest(t, fixture.server, fixture.member, http.MethodPost, "/api/workspace-launches", `{"name":"Blocked","packageId":"basic","autoRenew":false}`, "guard-blocked")
	assertErrorResponse(t, blocked.Code, blocked.Body.String(), http.StatusConflict, errBillingReconciliationBlocked.Error())

	fixture.sub2API.history[41] = originalHistory
	clean := requestWithMutationKeyForTest(t, fixture.server, operator, http.MethodPost, "/api/billing/reconciliation", `{"confirm":true}`, "guard-clean")
	if clean.Code != http.StatusCreated {
		t.Fatalf("clean status=%d body=%s", clean.Code, clean.Body.String())
	}
	current, found, err := fixture.store.BillingReconciliation(context.Background())
	if err != nil || !found || stringValue(current["status"]) != "ok" {
		t.Fatalf("clean guard readback=%#v found=%v err=%v", current, found, err)
	}

	command := workspaceLaunchUnitCommand()
	command.OperationID, command.AccountID, command.OwnerUserID = "guard-resumed-operation", "acct-monthly", "usr-monthly"
	command.WorkspaceID, command.Sub2APIUserID = "guard-resumed-workspace", 41
	operation, err := newWorkspaceLaunchReconcileOperation(command)
	if err != nil {
		t.Fatal(err)
	}
	row, err := workspaceLaunchReconcileOperationRow(operation)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.ClaimWorkspaceLaunchReconcile(context.Background(), workspaceLaunchReconcileClaim{AccountID: command.AccountID, DesiredOperation: row}); err != nil {
		t.Fatalf("fresh clean did not resume new claim: %v", err)
	}
}
