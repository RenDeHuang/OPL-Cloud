package server

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func newPostgresBillingReconciliationStore(t *testing.T, suffix string) (*postgresEntStateStore, string) {
	t.Helper()
	admin := openControlPlaneTestPostgres(t)
	schema := fmt.Sprintf("control_plane_billing_guard_%s_%d", suffix, time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		_ = admin.Close()
	})
	databaseURL := controlPlaneTestPostgresURL(t, "postgres", schema)
	stateStore, err := newTestPostgresEntStateStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	store := stateStore.(*postgresEntStateStore)
	t.Cleanup(func() { _ = store.client.Close() })
	return store, databaseURL
}

func seedPostgresBillingReconciliationAccount(t *testing.T, store controlPlaneTableStore, suffix string, userID int64) string {
	t.Helper()
	accountID, ownerID := "acct-billing-guard-"+suffix, "usr-billing-guard-"+suffix
	account, owner := provisionedAccountRowsFor(accountID, ownerID, suffix+"@example.com", userID)
	if err := store.CreateProvisionedAccount(context.Background(), account, owner); err != nil {
		t.Fatal(err)
	}
	return accountID
}

func billingReconciliationLaunchClaimForTest(t *testing.T, accountID, ownerID, suffix string, userID int64) (workspaceLaunchReconcileClaim, workspaceLaunchReconcileOperation) {
	t.Helper()
	command := workspaceLaunchUnitCommand()
	command.OperationID, command.AccountID, command.OwnerUserID = "billing-guard-operation-"+suffix, accountID, ownerID
	command.WorkspaceID, command.Sub2APIUserID = "billing-guard-workspace-"+suffix, userID
	operation, err := newWorkspaceLaunchReconcileOperation(command)
	if err != nil {
		t.Fatal(err)
	}
	row, err := workspaceLaunchReconcileOperationRow(operation)
	if err != nil {
		t.Fatal(err)
	}
	return workspaceLaunchReconcileClaim{AccountID: accountID, DesiredOperation: row}, operation
}

func TestPostgresBillingReconciliationReplayAndCleanSurviveRestart(t *testing.T) {
	ctx := context.Background()
	store, databaseURL := newPostgresBillingReconciliationStore(t, "restart")
	seedPostgresBillingReconciliationAccount(t, store, "restart", 901)
	clean := billingReconciliationRowForTest("reconcile-pg-clean", "ok", "operator_reconciliation")
	mismatch := billingReconciliationRowForTest("reconcile-pg-mismatch", "mismatch", "operator_reconciliation")
	applyBillingReconciliationForTest(t, store, clean, "")
	applyBillingReconciliationForTest(t, store, mismatch, "reconcile-pg-clean")
	reportConflict := billingReconciliationRowForTest("reconcile-pg-clean", "ok", "operator_reconciliation")
	reportConflict["report"].(map[string]any)["changed"] = true
	if err := store.ApplyBillingReconciliation(ctx, billingReconciliationMutationForTest(reportConflict, "reconcile-pg-mismatch")); !errors.Is(err, errIdempotencyConflict) {
		t.Fatalf("same ID report conflict error=%v", err)
	}
	if err := store.ApplyBillingReconciliation(ctx, billingReconciliationMutationForTest(clean, "reconcile-pg-mismatch")); err != nil {
		t.Fatalf("old clean replay error=%v", err)
	}
	if err := store.client.Close(); err != nil {
		t.Fatal(err)
	}

	restartedState, err := newTestPostgresEntStateStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	restarted := restartedState.(*postgresEntStateStore)
	current, found, err := restarted.BillingReconciliation(ctx)
	if err != nil || !found || stringValue(current["id"]) != "reconcile-pg-mismatch" {
		t.Fatalf("blocked restart readback=%#v found=%v err=%v", current, found, err)
	}
	audits, err := restarted.ListAuditEvents(ctx, "")
	if err != nil || len(audits) != 2 {
		t.Fatalf("blocked restart audits=%#v err=%v", audits, err)
	}
	resume := billingReconciliationRowForTest("reconcile-pg-resume", "ok", "operator_reconciliation")
	applyBillingReconciliationForTest(t, restarted, resume, "reconcile-pg-mismatch")
	if err := restarted.client.Close(); err != nil {
		t.Fatal(err)
	}

	finalState, err := newTestPostgresEntStateStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	finalStore := finalState.(*postgresEntStateStore)
	t.Cleanup(func() { _ = finalStore.client.Close() })
	current, found, err = finalStore.BillingReconciliation(ctx)
	blocked, guardErr := billingReconciliationBlockState(current)
	if err != nil || guardErr != nil || !found || blocked || stringValue(current["id"]) != "reconcile-pg-resume" {
		t.Fatalf("clean restart readback=%#v blocked=%v found=%v err=%v guardErr=%v", current, blocked, found, err, guardErr)
	}
	audits, err = finalStore.ListAuditEvents(ctx, "")
	if err != nil || len(audits) != 3 {
		t.Fatalf("clean restart audits=%#v err=%v", audits, err)
	}
}

func TestPostgresBillingReconciliationAuditConflictRollsBackGuard(t *testing.T) {
	ctx := context.Background()
	store, _ := newPostgresBillingReconciliationStore(t, "audit")
	seedPostgresBillingReconciliationAccount(t, store, "audit", 902)
	clean := billingReconciliationRowForTest("reconcile-pg-audit-clean", "ok", "operator_reconciliation")
	applyBillingReconciliationForTest(t, store, clean, "")
	mismatch := billingReconciliationRowForTest("reconcile-pg-audit-mismatch", "mismatch", "operator_reconciliation")
	mutation := billingReconciliationMutationForTest(mismatch, "reconcile-pg-audit-clean")
	if err := store.SaveAuditEvent(ctx, map[string]any{
		"id": mutation.AuditEvent["id"], "actorUserId": "usr-other", "action": "other.action", "resourceKind": "other", "resourceId": "other", "result": "succeeded",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyBillingReconciliation(ctx, mutation); !errors.Is(err, errIdempotencyConflict) {
		t.Fatalf("conflicting audit error=%v", err)
	}
	current, found, err := store.BillingReconciliation(ctx)
	if err != nil || !found || stringValue(current["id"]) != "reconcile-pg-audit-clean" {
		t.Fatalf("guard changed despite audit conflict: current=%#v found=%v err=%v", current, found, err)
	}
}

func TestPostgresBillingReconciliationAndLaunchClaimLinearize(t *testing.T) {
	t.Run("mismatch commits before claim", func(t *testing.T) {
		ctx := context.Background()
		store, _ := newPostgresBillingReconciliationStore(t, "mismatch_first")
		accountID := seedPostgresBillingReconciliationAccount(t, store, "mismatch-first", 903)
		claim, _ := billingReconciliationLaunchClaimForTest(t, accountID, "usr-billing-guard-mismatch-first", "mismatch-first", 903)
		mutation := billingReconciliationMutationForTest(billingReconciliationRowForTest("reconcile-pg-linearized-mismatch", "mismatch", "operator_reconciliation"), "")

		tx, err := store.client.Tx(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if err := lockWorkspacePurchaseAdmission(ctx, tx.Client()); err != nil {
			t.Fatal(err)
		}
		if err := applyBillingReconciliationLocked(ctx, tx.Client(), mutation); err != nil {
			t.Fatal(err)
		}
		claimResult := make(chan error, 1)
		go func() { claimResult <- store.ClaimWorkspaceLaunchReconcile(ctx, claim) }()
		assertPostgresOperationWaitsForAdmissionLock(t, claimResult)
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if err := <-claimResult; !errors.Is(err, errBillingReconciliationBlocked) {
			t.Fatalf("claim after committed mismatch error=%v", err)
		}
	})

	t.Run("claim commits before mismatch", func(t *testing.T) {
		ctx := context.Background()
		store, _ := newPostgresBillingReconciliationStore(t, "claim_first")
		firstAccountID := seedPostgresBillingReconciliationAccount(t, store, "claim-first", 904)
		secondAccountID := seedPostgresBillingReconciliationAccount(t, store, "claim-second", 905)
		firstClaim, firstOperation := billingReconciliationLaunchClaimForTest(t, firstAccountID, "usr-billing-guard-claim-first", "claim-first", 904)
		secondClaim, _ := billingReconciliationLaunchClaimForTest(t, secondAccountID, "usr-billing-guard-claim-second", "claim-second", 905)
		mutation := billingReconciliationMutationForTest(billingReconciliationRowForTest("reconcile-pg-after-claim", "mismatch", "operator_reconciliation"), "")

		tx, err := store.client.Tx(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if err := lockWorkspacePurchaseAdmission(ctx, tx.Client()); err != nil {
			t.Fatal(err)
		}
		desired, err := decodeWorkspaceLaunchReconcileOperation(firstClaim.DesiredOperation)
		if err != nil {
			t.Fatal(err)
		}
		if err := claimWorkspaceLaunchReconcileLocked(ctx, tx.Client(), firstClaim, desired); err != nil {
			t.Fatal(err)
		}
		applyResult := make(chan error, 1)
		go func() { applyResult <- store.ApplyBillingReconciliation(ctx, mutation) }()
		assertPostgresOperationWaitsForAdmissionLock(t, applyResult)
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if err := <-applyResult; err != nil {
			t.Fatalf("mismatch after committed claim error=%v", err)
		}
		if row, found, err := store.GetRuntimeOperation(ctx, firstOperation.ID); err != nil || !found || stringValue(row["id"]) != firstOperation.ID {
			t.Fatalf("first claim did not remain authoritative: row=%#v found=%v err=%v", row, found, err)
		}
		if err := store.ClaimWorkspaceLaunchReconcile(ctx, secondClaim); !errors.Is(err, errBillingReconciliationBlocked) {
			t.Fatalf("new claim after mismatch error=%v", err)
		}
	})
}

func assertPostgresOperationWaitsForAdmissionLock(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		t.Fatalf("operation bypassed admission lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
}
