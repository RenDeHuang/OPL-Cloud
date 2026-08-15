package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/domain"
)

func TestPostgresWorkspaceLaunchClaimPersistAndActivate(t *testing.T) {
	ctx := context.Background()
	store, _ := newPostgresWorkspaceRenewalStoreWithDB(t)
	account, owner, organization, membership := provisionedAccountRowsFor("acct-launch-pg", "usr-launch-pg", "org-launch-pg", "launch-pg@example.com", 41)
	mustStore(t, store.CreateProvisionedAccount(ctx, account, owner, organization, membership))

	command := workspaceLaunchUnitCommand()
	command.OperationID = "workspace-launch-pg"
	command.AccountID = "acct-launch-pg"
	command.OwnerUserID = "usr-launch-pg"
	command.Sub2APIUserID = 41
	command.WorkspaceID = "ws-launch-pg"
	operation, err := newWorkspaceLaunchReconcileOperation(command)
	if err != nil {
		t.Fatal(err)
	}
	claimedRow, err := workspaceLaunchReconcileOperationRow(operation)
	if err != nil {
		t.Fatal(err)
	}
	claim := workspaceLaunchReconcileClaim{AccountID: command.AccountID, DesiredOperation: claimedRow}
	if err := store.ClaimWorkspaceLaunchReconcile(ctx, claim); err != nil {
		t.Fatalf("claim launch: %v", err)
	}
	persisted, found, err := store.GetRuntimeOperation(ctx, command.OperationID)
	if err != nil || !found || stringValue(persisted["result"]) != stringValue(claimedRow["result"]) {
		t.Fatalf("claim readback found=%v operation=%#v err=%v", found, persisted, err)
	}
	if err := store.ClaimWorkspaceLaunchReconcile(ctx, claim); !errors.Is(err, errWorkspaceLaunchCASConflict) {
		t.Fatalf("duplicate claim error=%v", err)
	}

	desired, err := decodeWorkspaceLaunchReconcileOperation(persisted)
	if err != nil {
		t.Fatal(err)
	}
	desired.Version++
	desiredRow, err := workspaceLaunchReconcileOperationRow(desired)
	if err != nil {
		t.Fatal(err)
	}
	update := workspaceLaunchReconcileCAS{
		OperationID: command.OperationID, ExpectedOperationResult: stringValue(persisted["result"]), DesiredOperation: desiredRow,
	}
	if err := store.PersistWorkspaceLaunchReconcile(ctx, update); err != nil {
		t.Fatalf("persist exact result CAS: %v", err)
	}
	updated, found, err := store.GetRuntimeOperation(ctx, command.OperationID)
	if err != nil || !found || stringValue(updated["result"]) != stringValue(desiredRow["result"]) {
		t.Fatalf("persist readback found=%v operation=%#v err=%v", found, updated, err)
	}
	if err := store.PersistWorkspaceLaunchReconcile(ctx, update); !errors.Is(err, errWorkspaceLaunchCASConflict) {
		t.Fatalf("stale result CAS error=%v", err)
	}

	row := workspaceProjectionBillingRow(domain.WorkspaceProjection{
		ID: "ws-launch-pg", AccountID: "acct-launch-pg", OwnerID: "usr-launch-pg", Name: "Postgres Launch", PackageID: "basic", Provider: "fabric",
		Status: "running", ComputeID: "compute-fabric-pg", VolumeID: "storage-fabric-pg", AttachmentID: "attachment-fabric-pg",
		RuntimeID: "runtime-fabric-pg", RuntimeServiceName: "runtime-service-pg", RuntimeReady: true, URL: "https://workspace-pg.example",
	}, map[string]any{
		"autoRenew": false, "authorizedBy": "", "authorizedAt": "", "packageId": "basic", "storageGb": 10,
		"priceVersion": pricingCatalogVersion, "currency": pricingCurrency, "billingUnit": pricingBillingUnit,
		"computeUsdMicros": int64(50_000_000), "storageUsdMicros": int64(2_580_000), "totalUsdMicros": int64(52_580_000),
		"periodStart": "2026-08-12T00:00:00Z", "paidThrough": "2026-09-12T00:00:00Z", "nextRenewalAt": "2026-09-11T00:00:00Z",
		"billingAnchorDay": 12, "renewalStatus": "active", "computeAllocationId": "compute-fabric-pg", "storageId": "storage-fabric-pg",
	})
	row["activatedAt"] = "2026-08-12T00:01:00Z"
	activated, err := store.ActivateWorkspaceLaunchProjection(ctx, row)
	if err != nil {
		t.Fatalf("activate launch projection: %v", err)
	}
	readback, found, err := store.GetWorkspace(ctx, "ws-launch-pg")
	if err != nil || !found || stringValue(activated["accountId"]) != "acct-launch-pg" || stringValue(activated["ownerUserId"]) != "usr-launch-pg" || activated["customerProduct"] != true ||
		stringValue(readback["accountId"]) != "acct-launch-pg" || stringValue(readback["ownerUserId"]) != "usr-launch-pg" || readback["customerProduct"] != true {
		t.Fatalf("activation readback found=%v activated=%#v readback=%#v err=%v", found, activated, readback, err)
	}
	drifted := cloneMap(row)
	drifted["ownerAccountId"] = "acct-other"
	if _, err := store.ActivateWorkspaceLaunchProjection(ctx, drifted); !errors.Is(err, errWorkspaceActivationConflict) {
		t.Fatalf("owner drift activation error=%v", err)
	}
	computes, err := store.ListComputes(ctx, "acct-launch-pg")
	if err != nil {
		t.Fatal(err)
	}
	storages, err := store.ListStorages(ctx, "acct-launch-pg")
	if err != nil {
		t.Fatal(err)
	}
	attachments, err := store.ListAttachments(ctx, "acct-launch-pg")
	if err != nil {
		t.Fatal(err)
	}
	if len(computes) != 0 || len(storages) != 0 || len(attachments) != 0 {
		t.Fatalf("activation copied Fabric truth: computes=%#v storages=%#v attachments=%#v", computes, storages, attachments)
	}
}

func TestPostgresWorkspaceLaunchReplayClaimSurvivesReconcilerRestartWithoutSkip(t *testing.T) {
	ctx := context.Background()
	store, _ := newPostgresWorkspaceRenewalStoreWithDB(t)
	account, owner, organization, membership := provisionedAccountRowsFor("acct-launch-replay-pg", "usr-launch-replay-pg", "org-launch-replay-pg", "launch-replay-pg@example.com", 42)
	mustStore(t, store.CreateProvisionedAccount(ctx, account, owner, organization, membership))

	command := workspaceLaunchUnitCommand()
	command.OperationID = "workspace-launch-replay-pg"
	command.AccountID = "acct-launch-replay-pg"
	command.OwnerUserID = "usr-launch-replay-pg"
	command.Sub2APIUserID = 42
	command.WorkspaceID = "ws-launch-replay-pg"
	operation, err := newWorkspaceLaunchReconcileOperation(command)
	if err != nil {
		t.Fatal(err)
	}
	operation.Version, operation.Stage, operation.Status = 5, "debit", "manual_review"
	attempt := operation.Attempts[operation.Stage]
	attempt.Attempted, attempt.Status = 1, "reserved"
	attempt.IdempotencyKey = workspaceLaunchStageIdempotencyKey(operation, 1)
	operation.Attempts[operation.Stage] = attempt
	row, err := workspaceLaunchReconcileOperationRow(operation)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ClaimWorkspaceLaunchReconcile(ctx, workspaceLaunchReconcileClaim{AccountID: command.AccountID, DesiredOperation: row}); err != nil {
		t.Fatal(err)
	}

	adapter := &workspaceLaunchUnitAdapter{
		replayableStages:     map[string]bool{"debit": true},
		panicBeforeMutations: map[string]int{"debit": 1},
	}
	startedAt := time.Date(2026, 8, 15, 3, 0, 0, 0, time.UTC)
	firstProcess := NewWorkspaceLaunchReconciler(store, adapter)
	firstProcess.now = func() time.Time { return startedAt }
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected simulated process crash")
			}
		}()
		_, _ = firstProcess.Resume(ctx, operation.ID, workspaceLaunchReservedStageAuthorization(t, row, "resume-postgres-crash"))
	}()

	durableRow, found, err := store.GetRuntimeOperation(ctx, operation.ID)
	if err != nil || !found {
		t.Fatalf("durable replay claim missing found=%v err=%v", found, err)
	}
	durable, err := decodeWorkspaceLaunchReconcileOperation(durableRow)
	if err != nil || durable.IdempotentReplayClaims["debit"].Status != "claimed" || durable.ResumeAuthorizationConsumedAt != "" || adapter.mutations != 0 {
		t.Fatalf("durable crash cut invalid: operation=%s claim=%#v mutations=%d err=%v", workspaceLaunchReconcileResultSummary(durable), durable.IdempotentReplayClaims["debit"], adapter.mutations, err)
	}

	restartedProcess := NewWorkspaceLaunchReconciler(store, adapter)
	restartedProcess.now = func() time.Time { return startedAt.Add(workspaceLaunchIdempotentReplayLease + time.Second) }
	recovered, err := restartedProcess.Reconcile(ctx, operation.ID)
	if err != nil || recovered.Stage != "ensure_compute_allocation" || recovered.Attempts["debit"].Attempted != 1 || recovered.Attempts["debit"].Max != 1 ||
		recovered.Attempts["debit"].Confirmed != 1 || recovered.IdempotentReplayClaims["debit"].Status != "succeeded" || adapter.mutationsByStage["debit"] != 1 ||
		adapter.mutationIdempotencyKey != attempt.IdempotencyKey {
		t.Fatalf("PostgreSQL restart skipped or duplicated replay: operation=%s attempt=%#v claim=%#v mutations=%#v err=%v", workspaceLaunchReconcileResultSummary(recovered), recovered.Attempts["debit"], recovered.IdempotentReplayClaims["debit"], adapter.mutationsByStage, err)
	}
}

func TestPostgresWorkspaceLaunchConcurrentReplayResumeAllowsOneWriter(t *testing.T) {
	ctx := context.Background()
	store, _ := newPostgresWorkspaceRenewalStoreWithDB(t)
	account, owner, organization, membership := provisionedAccountRowsFor("acct-launch-replay-cas-pg", "usr-launch-replay-cas-pg", "org-launch-replay-cas-pg", "launch-replay-cas-pg@example.com", 43)
	mustStore(t, store.CreateProvisionedAccount(ctx, account, owner, organization, membership))

	command := workspaceLaunchUnitCommand()
	command.OperationID = "workspace-launch-replay-cas-pg"
	command.AccountID = "acct-launch-replay-cas-pg"
	command.OwnerUserID = "usr-launch-replay-cas-pg"
	command.Sub2APIUserID = 43
	command.WorkspaceID = "ws-launch-replay-cas-pg"
	operation, err := newWorkspaceLaunchReconcileOperation(command)
	if err != nil {
		t.Fatal(err)
	}
	operation.Version, operation.Stage, operation.Status = 5, "debit", "manual_review"
	attempt := operation.Attempts[operation.Stage]
	attempt.Attempted, attempt.Status = 1, "reserved"
	attempt.IdempotencyKey = workspaceLaunchStageIdempotencyKey(operation, 1)
	operation.Attempts[operation.Stage] = attempt
	row, err := workspaceLaunchReconcileOperationRow(operation)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ClaimWorkspaceLaunchReconcile(ctx, workspaceLaunchReconcileClaim{AccountID: command.AccountID, DesiredOperation: row}); err != nil {
		t.Fatal(err)
	}

	authorization := workspaceLaunchReservedStageAuthorization(t, row, "resume-postgres-concurrent")
	adapter := &workspaceLaunchUnitAdapter{barrier: make(chan struct{}), replayableStages: map[string]bool{"debit": true}}
	reconciler := NewWorkspaceLaunchReconciler(store, adapter)
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, resumeErr := reconciler.Resume(ctx, operation.ID, authorization)
			results <- resumeErr
		}()
	}
	close(start)
	var successes, conflicts int
	for range 2 {
		switch resumeErr := <-results; {
		case resumeErr == nil:
			successes++
		case errors.Is(resumeErr, errWorkspaceLaunchCASConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent PostgreSQL replay error: %v", resumeErr)
		}
	}
	durableRow, found, err := store.GetRuntimeOperation(ctx, operation.ID)
	if err != nil || !found {
		t.Fatalf("read durable concurrent replay found=%v err=%v", found, err)
	}
	durable, err := decodeWorkspaceLaunchReconcileOperation(durableRow)
	if err != nil || successes != 1 || conflicts != 1 || durable.Stage != "ensure_compute_allocation" || durable.Attempts["debit"].Attempted != 1 || durable.Attempts["debit"].Max != 1 ||
		durable.Attempts["debit"].Confirmed != 1 || durable.IdempotentReplayClaims["debit"].Status != "succeeded" || durable.ResumeAuthorizationConsumedAt == "" ||
		adapter.mutationsByStage["debit"] != 1 || adapter.mutationIdempotencyKey != attempt.IdempotencyKey {
		t.Fatalf("PostgreSQL replay CAS mismatch: operation=%s successes=%d conflicts=%d mutations=%#v err=%v", workspaceLaunchReconcileResultSummary(durable), successes, conflicts, adapter.mutationsByStage, err)
	}
}
