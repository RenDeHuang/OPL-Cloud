package server

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/domain"
)

type workspaceLaunchPendingReadGateStore struct {
	workspaceLaunchReconcileStore
	mu      sync.Mutex
	reads   int
	ready   chan struct{}
	release chan struct{}
}

func (s *workspaceLaunchPendingReadGateStore) GetRuntimeOperation(ctx context.Context, operationID string) (map[string]any, bool, error) {
	row, found, err := s.workspaceLaunchReconcileStore.GetRuntimeOperation(ctx, operationID)
	if err != nil || !found {
		return row, found, err
	}
	s.mu.Lock()
	s.reads++
	if s.reads == 2 {
		close(s.ready)
	}
	release := s.release
	s.mu.Unlock()
	<-release
	return row, found, nil
}

func seedPostgresWorkspaceLaunchFreshTypedPending(
	t *testing.T,
	suffix string,
) (workspaceLaunchReconcileStore, *workspaceLaunchUnitAdapter, workspaceLaunchReconcileOperation) {
	t.Helper()
	ctx := context.Background()
	store, _ := newPostgresWorkspaceRenewalStoreWithDB(t)
	accountID, ownerID := "acct-fresh-pending-"+suffix, "usr-fresh-pending-"+suffix
	account, owner := provisionedAccountRowsFor(accountID, ownerID, "fresh-pending-"+suffix+"@example.com", 840)
	mustStore(t, store.CreateProvisionedAccount(ctx, account, owner))
	command := workspaceLaunchUnitCommand()
	command.OperationID, command.AccountID, command.OwnerUserID = "workspace-launch-fresh-pending-"+suffix, accountID, ownerID
	command.WorkspaceID, command.Sub2APIUserID = "ws-fresh-pending-"+suffix, 840
	operation, err := newWorkspaceLaunchReconcileOperation(command)
	if err != nil {
		t.Fatal(err)
	}
	operation.Version, operation.Stage, operation.Status = 4, "runtime", "pending"
	row, err := workspaceLaunchReconcileOperationRow(operation)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ClaimWorkspaceLaunchReconcile(ctx, workspaceLaunchReconcileClaim{AccountID: accountID, DesiredOperation: row}); err != nil {
		t.Fatal(err)
	}
	absent := workspaceLaunchUnitReadResult{observation: workspaceLaunchStageObservation{State: workspaceLaunchStageAbsent}}
	pending := workspaceLaunchUnitReadResult{observation: workspaceLaunchStageObservation{State: workspaceLaunchStagePending}}
	adapter := &workspaceLaunchUnitAdapter{readResultsByStage: map[string][]workspaceLaunchUnitReadResult{"runtime": {absent, pending}}}
	seeded, err := NewWorkspaceLaunchReconciler(store, adapter).Reconcile(ctx, operation.ID)
	if err != nil || seeded.Status != "pending" || seeded.Stage != "runtime" {
		t.Fatalf("seed PostgreSQL fresh pending: operation=%s err=%v", workspaceLaunchReconcileResultSummary(seeded), err)
	}
	return store, adapter, seeded
}

func TestPostgresWorkspaceLaunchClaimPersistAndActivate(t *testing.T) {
	ctx := context.Background()
	store, _ := newPostgresWorkspaceRenewalStoreWithDB(t)
	account, owner := provisionedAccountRowsFor("acct-launch-pg", "usr-launch-pg", "launch-pg@example.com", 41)
	mustStore(t, store.CreateProvisionedAccount(ctx, account, owner))

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

func TestPostgresWorkspaceLaunchReceiptProjectionCASIsIdempotentAndFailClosed(t *testing.T) {
	ctx := context.Background()
	store, _ := newPostgresWorkspaceRenewalStoreWithDB(t)
	account, owner := provisionedAccountRowsFor("acct-receipt-pg", "usr-receipt-pg", "receipt-pg@example.com", 41)
	mustStore(t, store.CreateProvisionedAccount(ctx, account, owner))

	workspace := workspaceLaunchUnitActivationProjectionRow(t, "ws-receipt-pg", "acct-receipt-pg", "usr-receipt-pg")
	mustStore(t, store.SaveWorkspace(ctx, workspace))

	command := workspaceLaunchUnitCommand()
	command.OperationID, command.AccountID, command.OwnerUserID = "workspace-launch-receipt-pg", "acct-receipt-pg", "usr-receipt-pg"
	command.WorkspaceID, command.Sub2APIUserID = "ws-receipt-pg", 41
	operation, err := newWorkspaceLaunchReconcileOperation(command)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range workspaceLaunchReconcileStages[:len(workspaceLaunchReconcileStages)-1] {
		operation.Stage = stage
		observation, reduceErr := reduceWorkspaceLaunchStageObservation(&operation, workspaceLaunchStageObservation{State: workspaceLaunchStageReady, Facts: workspaceLaunchReadyFacts(stage)})
		if reduceErr != nil {
			t.Fatalf("reduce %s: %v", stage, reduceErr)
		}
		operation.Observations[stage] = observation
		attempt := operation.Attempts[stage]
		attempt.Attempted, attempt.Confirmed, attempt.Status = 1, 1, "confirmed"
		operation.Attempts[stage] = attempt
		operation.advance()
	}
	if operation.Stage != "succeeded" || operation.Status != "succeeded" || operation.stringFact("receiptId") != "receipt-unit" {
		t.Fatalf("terminal operation=%s", workspaceLaunchReconcileResultSummary(operation))
	}
	claimed, err := workspaceLaunchReconcileOperationRow(operation)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ClaimWorkspaceLaunchReconcile(ctx, workspaceLaunchReconcileClaim{AccountID: command.AccountID, DesiredOperation: claimed}); err != nil {
		t.Fatal(err)
	}

	projection, err := workspaceLaunchReceiptProjectionFor(operation)
	if err != nil {
		t.Fatal(err)
	}
	operation.Version++
	first, err := workspaceLaunchReconcileOperationRow(operation)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PersistWorkspaceLaunchReconcile(ctx, workspaceLaunchReconcileCAS{
		OperationID: command.OperationID, ExpectedOperationResult: stringValue(claimed["result"]), DesiredOperation: first, WorkspaceReceiptProjection: projection,
	}); err != nil {
		t.Fatalf("first projection: %v", err)
	}
	readback, found, err := store.GetWorkspace(ctx, command.WorkspaceID)
	if err != nil || !found || stringValue(readback["purchaseReceiptId"]) != "receipt-unit" {
		t.Fatalf("first projection readback found=%v workspace=%#v err=%v", found, readback, err)
	}

	operation.Version++
	second, err := workspaceLaunchReconcileOperationRow(operation)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PersistWorkspaceLaunchReconcile(ctx, workspaceLaunchReconcileCAS{
		OperationID: command.OperationID, ExpectedOperationResult: stringValue(first["result"]), DesiredOperation: second, WorkspaceReceiptProjection: projection,
	}); err != nil {
		t.Fatalf("same receipt replay: %v", err)
	}

	readback["purchaseReceiptId"] = "receipt-other"
	if err := store.SaveWorkspace(ctx, readback); err != nil {
		t.Fatalf("seed conflicting workspace receipt: %v", err)
	}
	operation.Version++
	conflict, err := workspaceLaunchReconcileOperationRow(operation)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PersistWorkspaceLaunchReconcile(ctx, workspaceLaunchReconcileCAS{
		OperationID: command.OperationID, ExpectedOperationResult: stringValue(second["result"]), DesiredOperation: conflict, WorkspaceReceiptProjection: projection,
	}); !errors.Is(err, errIdempotencyConflict) {
		t.Fatalf("different receipt error=%v", err)
	}
	readback, found, err = store.GetWorkspace(ctx, command.WorkspaceID)
	if err != nil || !found || stringValue(readback["purchaseReceiptId"]) != "receipt-other" {
		t.Fatalf("conflicting projection mutated workspace found=%v workspace=%#v err=%v", found, readback, err)
	}
}

func TestPostgresWorkspaceLaunchCanonicalFactRepairIsAtomicAndSingleWriter(t *testing.T) {
	ctx := context.Background()
	store, _ := newPostgresWorkspaceRenewalStoreWithDB(t)
	row := historicalWorkspaceLaunchMissingSpecDigest(t)
	account, owner := provisionedAccountRowsFor("acct-repair", "usr-repair", "repair-pg@example.com", 41)
	mustStore(t, store.CreateProvisionedAccount(ctx, account, owner))
	mustStore(t, store.SaveRuntimeOperation(ctx, row))
	preview, err := buildWorkspaceLaunchCanonicalFactRepairPreview(row, strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	update := workspaceLaunchCanonicalFactRepairCAS{
		OperationID: preview.Classification.OperationID, ExpectedOperationResult: preview.Classification.PersistedResult,
		DesiredOperation: preview.DesiredOperation, AuditEvent: canonicalFactRepairAudit(preview, "repair-pg"),
	}
	row["providerRequestId"] = "request-after-preview"
	mustStore(t, store.SaveRuntimeOperation(ctx, row))
	results := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			ready.Done()
			<-start
			results <- store.ApplyWorkspaceLaunchCanonicalFactRepair(ctx, update)
		}()
	}
	ready.Wait()
	close(start)
	first, second := <-results, <-results
	if first != nil || second != nil {
		t.Fatalf("idempotent concurrent results=%v/%v", first, second)
	}
	stored, found, readErr := store.GetRuntimeOperation(ctx, preview.Classification.OperationID)
	operation, decodeErr := decodeWorkspaceLaunchReconcileOperation(stored)
	audits, auditErr := store.ListAuditEvents(ctx, preview.Classification.AccountID)
	if !found || readErr != nil || decodeErr != nil || operation.Version != 6 || stringValue(stored["providerRequestId"]) != "request-after-preview" || len(audits) != 1 || auditErr != nil {
		t.Fatalf("operation=%#v audits=%#v errors=%v/%v/%v", operation, audits, readErr, decodeErr, auditErr)
	}
}

func TestPostgresWorkspaceLaunchUnknownRuntimeRecoveryPersistsReadyReadOnly(t *testing.T) {
	ctx := context.Background()
	store, _ := newPostgresWorkspaceRenewalStoreWithDB(t)
	accountID, ownerID := "acct-unit", "usr-unit"
	account, owner := provisionedAccountRowsFor(accountID, ownerID, "runtime-recovery-pg@example.com", 11)
	mustStore(t, store.CreateProvisionedAccount(ctx, account, owner))

	row := workspaceLaunchUnknownRuntimeWithFailedFreshContinuationRow(t)
	operation, err := decodeWorkspaceLaunchReconcileOperation(row)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ClaimWorkspaceLaunchReconcile(ctx, workspaceLaunchReconcileClaim{AccountID: accountID, DesiredOperation: row}); err != nil {
		t.Fatal(err)
	}

	adapter := &workspaceLaunchUnitAdapter{readyStages: map[string]bool{"runtime": true}}
	authorization := workspaceLaunchUnknownRuntimeReadAuthorization(t, row, "resume-postgres-unknown-runtime")
	got, err := NewWorkspaceLaunchReconciler(store, adapter).Resume(ctx, operation.ID, authorization)
	fresh := got.FreshContinuationAuthorizations["runtime"]
	if err != nil || got.Stage != "activation" || got.Status != "pending" || got.Version != operation.Version+1 ||
		got.Attempts["runtime"].Confirmed != 1 || got.ResumeAuthorizationConsumedAt == "" || fresh.Status != "consumed" ||
		adapter.reads != 1 || adapter.mutations != 0 {
		t.Fatalf("PostgreSQL Runtime recovery did not persist: operation=%s version=%d reads=%d mutations=%d err=%v",
			workspaceLaunchReconcileResultSummary(got), got.Version, adapter.reads, adapter.mutations, err)
	}
}

func TestPostgresWorkspaceLaunchUnknownComputeResumePersistsOriginalAttemptContinuation(t *testing.T) {
	ctx := context.Background()
	store, _ := newPostgresWorkspaceRenewalStoreWithDB(t)
	accountID, ownerID := "acct-unit", "usr-unit"
	account, owner := provisionedAccountRowsFor(accountID, ownerID, "compute-recovery-pg@example.com", 11)
	mustStore(t, store.CreateProvisionedAccount(ctx, account, owner))

	row := workspaceLaunchUnknownStageManualReviewRow(t, "ensure_compute_allocation")
	operation, err := decodeWorkspaceLaunchReconcileOperation(row)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ClaimWorkspaceLaunchReconcile(ctx, workspaceLaunchReconcileClaim{AccountID: accountID, DesiredOperation: row}); err != nil {
		t.Fatal(err)
	}
	ownership := workspaceLaunchUnitReadResult{observation: workspaceLaunchStageObservation{State: workspaceLaunchStageOwnershipPending}}
	adapter := &workspaceLaunchUnitAdapter{
		readResultsByStage: map[string][]workspaceLaunchUnitReadResult{"ensure_compute_allocation": {ownership, ownership, ownership}},
		replayableStages:   map[string]bool{"ensure_compute_allocation": true},
	}
	authorization := workspaceLaunchUnknownComputeContinuationAuthorization(t, row, "resume-postgres-unknown-compute")
	got, err := NewWorkspaceLaunchReconciler(store, adapter).Resume(ctx, operation.ID, authorization)
	if err != nil || got.Stage != "storage" || got.Status != "pending" || got.Attempts["ensure_compute_allocation"].Attempted != 1 ||
		got.Attempts["ensure_compute_allocation"].Confirmed != 1 || got.ResumeAuthorizationConsumedAt == "" ||
		got.IdempotentReplayClaims["ensure_compute_allocation"].Status != "succeeded" || adapter.mutationsByStage["ensure_compute_allocation"] != 1 {
		t.Fatalf("PostgreSQL compute continuation did not persist: operation=%s reads=%d mutations=%#v err=%v",
			workspaceLaunchReconcileResultSummary(got), adapter.reads, adapter.mutationsByStage, err)
	}
	persisted, found, err := store.GetRuntimeOperation(ctx, operation.ID)
	restarted, decodeErr := decodeWorkspaceLaunchReconcileOperation(persisted)
	if err != nil || !found || decodeErr != nil || restarted.Version != got.Version || restarted.Stage != "storage" ||
		restarted.Attempts["ensure_compute_allocation"].IdempotencyKey != workspaceLaunchStageIdempotencyKey(operationWithStage(restarted, "ensure_compute_allocation"), 1) {
		t.Fatalf("PostgreSQL compute continuation restart readback found=%v operation=%s errors=%v/%v", found, workspaceLaunchReconcileResultSummary(restarted), err, decodeErr)
	}
}

func TestPostgresWorkspaceLaunchUnknownComputeResumePersistsFailedReplayReplacement(t *testing.T) {
	ctx := context.Background()
	store, _ := newPostgresWorkspaceRenewalStoreWithDB(t)
	accountID, ownerID := "acct-unit", "usr-unit"
	account, owner := provisionedAccountRowsFor(accountID, ownerID, "compute-replay-replacement-pg@example.com", 11)
	mustStore(t, store.CreateProvisionedAccount(ctx, account, owner))

	row := workspaceLaunchUnknownComputeAfterFailedReplayRow(t)
	operation, err := decodeWorkspaceLaunchReconcileOperation(row)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ClaimWorkspaceLaunchReconcile(ctx, workspaceLaunchReconcileClaim{AccountID: accountID, DesiredOperation: row}); err != nil {
		t.Fatal(err)
	}
	ownership := workspaceLaunchUnitReadResult{observation: workspaceLaunchStageObservation{State: workspaceLaunchStageOwnershipPending}}
	adapter := &workspaceLaunchUnitAdapter{
		readResultsByStage: map[string][]workspaceLaunchUnitReadResult{"ensure_compute_allocation": {ownership, ownership, ownership}},
		replayableStages:   map[string]bool{"ensure_compute_allocation": true},
	}
	authorization := workspaceLaunchUnknownComputeContinuationAuthorization(t, row, "resume-postgres-failed-compute-replacement")
	got, err := NewWorkspaceLaunchReconciler(store, adapter).Resume(ctx, operation.ID, authorization)
	if err != nil || got.Stage != "storage" || got.Status != "pending" || got.Attempts["ensure_compute_allocation"].Attempted != 1 ||
		got.Attempts["ensure_compute_allocation"].Confirmed != 1 || got.ResumeAuthorizationConsumedAt == "" ||
		got.IdempotentReplayClaims["ensure_compute_allocation"].AuthorizationID != authorization.AuthorizationID ||
		got.IdempotentReplayClaims["ensure_compute_allocation"].Status != "succeeded" || len(got.ConsumedResumeAuthorizations) != 1 ||
		got.ConsumedResumeAuthorizations[0].Authorization.AuthorizationID != operation.ResumeAuthorization.AuthorizationID ||
		adapter.mutationsByStage["ensure_compute_allocation"] != 1 {
		t.Fatalf("PostgreSQL failed compute replay replacement did not persist: operation=%s history=%#v reads=%d mutations=%#v err=%v",
			workspaceLaunchReconcileResultSummary(got), got.ConsumedResumeAuthorizations, adapter.reads, adapter.mutationsByStage, err)
	}
	persisted, found, err := store.GetRuntimeOperation(ctx, operation.ID)
	restarted, decodeErr := decodeWorkspaceLaunchReconcileOperation(persisted)
	if err != nil || !found || decodeErr != nil || restarted.Version != got.Version || restarted.Stage != "storage" ||
		restarted.IdempotentReplayClaims["ensure_compute_allocation"].AuthorizationID != authorization.AuthorizationID ||
		len(restarted.ConsumedResumeAuthorizations) != 1 ||
		restarted.Attempts["ensure_compute_allocation"].IdempotencyKey != operation.Attempts["ensure_compute_allocation"].IdempotencyKey {
		t.Fatalf("PostgreSQL failed compute replay replacement restart readback found=%v operation=%s errors=%v/%v",
			found, workspaceLaunchReconcileResultSummary(restarted), err, decodeErr)
	}
}

func TestPostgresWorkspaceLaunchCanonicalOperatorActivationPersistsAuthoritativeReadback(t *testing.T) {
	ctx := context.Background()
	store, _ := newPostgresWorkspaceRenewalStoreWithDB(t)
	account := map[string]any{"id": "acct-admin", "ownerUserId": "usr-admin", "status": "active", "sub2apiUserId": int64(1)}
	owner := map[string]any{"id": "usr-admin", "email": "admin@opl.local", "accountId": "acct-admin", "role": "admin", "status": "active"}
	mustStore(t, store.CreateProvisionedAccount(ctx, account, owner))

	row := workspaceLaunchUnitActivationProjectionRow(t, "ws-admin-pg", "acct-admin", "usr-admin")
	activated, err := store.ActivateWorkspaceLaunchProjection(ctx, row)
	if err != nil {
		t.Fatalf("activate canonical operator projection: %v", err)
	}
	readback, found, err := store.GetWorkspace(ctx, "ws-admin-pg")
	if err != nil || !found || stringValue(activated["accountId"]) != "acct-admin" || stringValue(activated["ownerUserId"]) != "usr-admin" || activated["customerProduct"] != true ||
		stringValue(readback["accountId"]) != "acct-admin" || stringValue(readback["ownerUserId"]) != "usr-admin" || readback["customerProduct"] != true {
		t.Fatalf("canonical activation readback found=%v activated=%#v readback=%#v err=%v", found, activated, readback, err)
	}
}

func TestPostgresWorkspaceLaunchReplayClaimSurvivesReconcilerRestartWithoutSkip(t *testing.T) {
	for stageIndex, stage := range workspaceLaunchReconcileStages[:len(workspaceLaunchReconcileStages)-1] {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			store, _ := newPostgresWorkspaceRenewalStoreWithDB(t)
			suffix := strconv.Itoa(stageIndex)
			accountID, ownerID := "acct-launch-restart-"+suffix, "usr-launch-restart-"+suffix
			account, owner := provisionedAccountRowsFor(accountID, ownerID, "launch-restart-"+suffix+"@example.com", int64(100+stageIndex))
			mustStore(t, store.CreateProvisionedAccount(ctx, account, owner))

			command := workspaceLaunchUnitCommand()
			command.OperationID = "workspace-launch-restart-" + suffix
			command.AccountID, command.OwnerUserID, command.Sub2APIUserID = accountID, ownerID, int64(100+stageIndex)
			command.WorkspaceID = "ws-launch-restart-" + suffix
			operation, err := newWorkspaceLaunchReconcileOperation(command)
			if err != nil {
				t.Fatal(err)
			}
			operation.Version, operation.Stage, operation.Status = 5, stage, "manual_review"
			attempt := operation.Attempts[stage]
			attempt.Attempted, attempt.Status = 1, "reserved"
			attempt.IdempotencyKey = workspaceLaunchStageIdempotencyKey(operation, 1)
			operation.Attempts[stage] = attempt
			operation.Observations[stage] = workspaceLaunchStageObservation{State: workspaceLaunchStageAbsent}
			row, err := workspaceLaunchReconcileOperationRow(operation)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.ClaimWorkspaceLaunchReconcile(ctx, workspaceLaunchReconcileClaim{AccountID: command.AccountID, DesiredOperation: row}); err != nil {
				t.Fatal(err)
			}
			if stage == "receipt" {
				mustStore(t, store.SaveWorkspace(ctx, workspaceLaunchUnitActivationProjectionRow(t, command.WorkspaceID, command.AccountID, command.OwnerUserID)))
			}

			adapter := &workspaceLaunchUnitAdapter{
				replayableStages:     map[string]bool{stage: true},
				panicBeforeMutations: map[string]int{stage: 1},
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
				_, _ = firstProcess.Resume(ctx, operation.ID, workspaceLaunchReservedStageAuthorization(t, row, "resume-postgres-crash-"+stage))
			}()

			durableRow, found, err := store.GetRuntimeOperation(ctx, operation.ID)
			if err != nil || !found {
				t.Fatalf("durable replay claim missing found=%v err=%v", found, err)
			}
			durable, err := decodeWorkspaceLaunchReconcileOperation(durableRow)
			if err != nil || durable.IdempotentReplayClaims[stage].Status != "claimed" || durable.ResumeAuthorizationConsumedAt != "" || adapter.mutations != 0 {
				t.Fatalf("durable crash cut invalid: operation=%s claim=%#v mutations=%d err=%v", workspaceLaunchReconcileResultSummary(durable), durable.IdempotentReplayClaims[stage], adapter.mutations, err)
			}

			restartedProcess := NewWorkspaceLaunchReconciler(store, adapter)
			restartedProcess.now = func() time.Time { return startedAt.Add(workspaceLaunchIdempotentReplayLease + time.Second) }
			recovered, err := restartedProcess.Reconcile(ctx, operation.ID)
			recoveredAttempt := recovered.Attempts[stage]
			if err != nil || recovered.Stage != workspaceLaunchReconcileStages[stageIndex+1] || recoveredAttempt.Attempted != 1 || recoveredAttempt.Max != 1 ||
				recoveredAttempt.Confirmed != 1 || recovered.IdempotentReplayClaims[stage].Status != "succeeded" || adapter.mutationsByStage[stage] != 1 ||
				adapter.mutationIdempotencyKey != attempt.IdempotencyKey {
				t.Fatalf("PostgreSQL restart skipped or duplicated replay: operation=%s attempt=%#v claim=%#v mutations=%#v err=%v", workspaceLaunchReconcileResultSummary(recovered), recoveredAttempt, recovered.IdempotentReplayClaims[stage], adapter.mutationsByStage, err)
			}
		})
	}
}

func TestPostgresWorkspaceLaunchConcurrentReplayResumeAllowsOneWriter(t *testing.T) {
	for stageIndex, stage := range workspaceLaunchReconcileStages[:len(workspaceLaunchReconcileStages)-1] {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			store, _ := newPostgresWorkspaceRenewalStoreWithDB(t)
			suffix := strconv.Itoa(stageIndex)
			accountID, ownerID := "acct-launch-cas-"+suffix, "usr-launch-cas-"+suffix
			account, owner := provisionedAccountRowsFor(accountID, ownerID, "launch-cas-"+suffix+"@example.com", int64(200+stageIndex))
			mustStore(t, store.CreateProvisionedAccount(ctx, account, owner))

			command := workspaceLaunchUnitCommand()
			command.OperationID = "workspace-launch-cas-" + suffix
			command.AccountID, command.OwnerUserID, command.Sub2APIUserID = accountID, ownerID, int64(200+stageIndex)
			command.WorkspaceID = "ws-launch-cas-" + suffix
			operation, err := newWorkspaceLaunchReconcileOperation(command)
			if err != nil {
				t.Fatal(err)
			}
			operation.Version, operation.Stage, operation.Status = 5, stage, "manual_review"
			attempt := operation.Attempts[stage]
			attempt.Attempted, attempt.Status = 1, "reserved"
			attempt.IdempotencyKey = workspaceLaunchStageIdempotencyKey(operation, 1)
			operation.Attempts[stage] = attempt
			operation.Observations[stage] = workspaceLaunchStageObservation{State: workspaceLaunchStageAbsent}
			row, err := workspaceLaunchReconcileOperationRow(operation)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.ClaimWorkspaceLaunchReconcile(ctx, workspaceLaunchReconcileClaim{AccountID: command.AccountID, DesiredOperation: row}); err != nil {
				t.Fatal(err)
			}
			if stage == "receipt" {
				mustStore(t, store.SaveWorkspace(ctx, workspaceLaunchUnitActivationProjectionRow(t, command.WorkspaceID, command.AccountID, command.OwnerUserID)))
			}

			authorization := workspaceLaunchReservedStageAuthorization(t, row, "resume-postgres-concurrent-"+stage)
			adapter := &workspaceLaunchUnitAdapter{barrier: make(chan struct{}), replayableStages: map[string]bool{stage: true}}
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
			durableAttempt := durable.Attempts[stage]
			if err != nil || successes != 1 || conflicts != 1 || durable.Stage != workspaceLaunchReconcileStages[stageIndex+1] || durableAttempt.Attempted != 1 || durableAttempt.Max != 1 ||
				durableAttempt.Confirmed != 1 || durable.IdempotentReplayClaims[stage].Status != "succeeded" || durable.ResumeAuthorizationConsumedAt == "" ||
				adapter.mutationsByStage[stage] != 1 || adapter.mutationIdempotencyKey != attempt.IdempotencyKey {
				t.Fatalf("PostgreSQL replay CAS mismatch: operation=%s successes=%d conflicts=%d mutations=%#v err=%v", workspaceLaunchReconcileResultSummary(durable), successes, conflicts, adapter.mutationsByStage, err)
			}
		})
	}
}

func TestPostgresWorkspaceLaunchFreshPendingReadClaimSurvivesCrashAndRestart(t *testing.T) {
	ctx := context.Background()
	store, adapter, seeded := seedPostgresWorkspaceLaunchFreshTypedPending(t, "restart")
	startedAt := time.Date(2026, 8, 16, 2, 0, 0, 0, time.UTC)
	adapter.panicOnReadNumber = 3
	first := NewWorkspaceLaunchReconciler(store, adapter)
	first.now = func() time.Time { return startedAt }
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected simulated process crash")
			}
		}()
		_, _ = first.Reconcile(ctx, seeded.ID)
	}()
	durableRow, found, err := store.GetRuntimeOperation(ctx, seeded.ID)
	if err != nil || !found {
		t.Fatalf("read durable fresh claim found=%v err=%v", found, err)
	}
	durable, err := decodeWorkspaceLaunchReconcileOperation(durableRow)
	authorization := durable.FreshContinuationAuthorizations["runtime"]
	claim2 := durable.ContinuationReadClaims[workspaceLaunchFreshContinuationClaimKey(authorization.AuthorizationID, 2)]
	if err != nil || durable.Attempts["runtime"].PendingReadbacks != 2 || claim2.Status != "claimed" || adapter.mutationsByStage["runtime"] != 1 {
		t.Fatalf("PostgreSQL crash claim not durable: operation=%s claim=%#v mutations=%#v err=%v", workspaceLaunchReconcileResultSummary(durable), claim2, adapter.mutationsByStage, err)
	}

	adapter.panicOnReadNumber = 0
	adapter.stageObservations = map[string]workspaceLaunchStageObservation{"runtime": {State: workspaceLaunchStageReady, Facts: workspaceLaunchReadyFacts("runtime")}}
	restarted := NewWorkspaceLaunchReconciler(store, adapter)
	restarted.now = func() time.Time { return startedAt.Add(workspaceLaunchFreshContinuationReadClaimLease + time.Second) }
	got, err := restarted.Reconcile(ctx, seeded.ID)
	if err != nil || got.Stage != "activation" || got.Attempts["runtime"].PendingReadbacks != 3 ||
		got.ContinuationReadClaims[workspaceLaunchFreshContinuationClaimKey(authorization.AuthorizationID, 2)].Status != "expired" ||
		got.ContinuationReadClaims[workspaceLaunchFreshContinuationClaimKey(authorization.AuthorizationID, 3)].Status != "ready" ||
		got.FreshContinuationAuthorizations["runtime"].Status != "consumed" || adapter.mutationsByStage["runtime"] != 1 {
		t.Fatalf("PostgreSQL restart skipped or refunded claim: operation=%s claims=%#v mutations=%#v err=%v",
			workspaceLaunchReconcileResultSummary(got), got.ContinuationReadClaims, adapter.mutationsByStage, err)
	}
}

func TestPostgresWorkspaceLaunchFreshPendingContinuationCASAllowsOneOwnerRead(t *testing.T) {
	ctx := context.Background()
	store, adapter, seeded := seedPostgresWorkspaceLaunchFreshTypedPending(t, "concurrent")
	adapter.stageObservations = map[string]workspaceLaunchStageObservation{"runtime": {State: workspaceLaunchStageReady, Facts: workspaceLaunchReadyFacts("runtime")}}
	gate := &workspaceLaunchPendingReadGateStore{
		workspaceLaunchReconcileStore: store,
		ready:                         make(chan struct{}), release: make(chan struct{}),
	}
	reconciler := NewWorkspaceLaunchReconciler(gate, adapter)
	results := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := reconciler.Reconcile(ctx, seeded.ID)
			results <- err
		}()
	}
	<-gate.ready
	close(gate.release)
	var successes, conflicts int
	for range 2 {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, errWorkspaceLaunchCASConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent fresh continuation error: %v", err)
		}
	}
	durableRow, found, err := store.GetRuntimeOperation(ctx, seeded.ID)
	if err != nil || !found {
		t.Fatalf("read durable concurrent continuation found=%v err=%v", found, err)
	}
	durable, err := decodeWorkspaceLaunchReconcileOperation(durableRow)
	if err != nil || successes != 1 || conflicts != 1 || durable.Stage != "activation" || durable.Attempts["runtime"].PendingReadbacks != 2 ||
		adapter.reads != 3 || adapter.mutationsByStage["runtime"] != 1 || durable.FreshContinuationAuthorizations["runtime"].Status != "consumed" {
		t.Fatalf("PostgreSQL fresh continuation CAS mismatch: operation=%s successes=%d conflicts=%d reads=%d mutations=%#v err=%v",
			workspaceLaunchReconcileResultSummary(durable), successes, conflicts, adapter.reads, adapter.mutationsByStage, err)
	}
}

func TestPostgresWorkspaceLaunchLegacyV3MissingFreshContinuationFieldsHasZeroBudget(t *testing.T) {
	ctx := context.Background()
	store, _ := newPostgresWorkspaceRenewalStoreWithDB(t)
	account, owner := provisionedAccountRowsFor(
		"acct-fresh-legacy", "usr-fresh-legacy", "fresh-legacy@example.com", 841,
	)
	mustStore(t, store.CreateProvisionedAccount(ctx, account, owner))
	command := workspaceLaunchUnitCommand()
	command.OperationID, command.AccountID, command.OwnerUserID = "workspace-launch-fresh-legacy", "acct-fresh-legacy", "usr-fresh-legacy"
	command.WorkspaceID, command.Sub2APIUserID = "ws-fresh-legacy", 841
	operation, err := newWorkspaceLaunchReconcileOperation(command)
	if err != nil {
		t.Fatal(err)
	}
	operation.Version, operation.Stage, operation.Status = 4, "runtime", "pending"
	attempt := operation.Attempts["runtime"]
	attempt.Attempted, attempt.Status = 1, "reserved"
	attempt.IdempotencyKey = workspaceLaunchStageIdempotencyKey(operation, 1)
	operation.Attempts["runtime"] = attempt
	operation.Observations["runtime"] = workspaceLaunchStageObservation{State: workspaceLaunchStagePending}
	row, err := workspaceLaunchReconcileOperationRow(operation)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ClaimWorkspaceLaunchReconcile(ctx, workspaceLaunchReconcileClaim{AccountID: command.AccountID, DesiredOperation: row}); err != nil {
		t.Fatal(err)
	}
	adapter := &workspaceLaunchUnitAdapter{
		stageObservations: map[string]workspaceLaunchStageObservation{"runtime": {State: workspaceLaunchStageReady, Facts: workspaceLaunchReadyFacts("runtime")}},
		replayableStages:  map[string]bool{"runtime": true},
	}
	got, err := NewWorkspaceLaunchReconciler(store, adapter).Reconcile(ctx, operation.ID)
	if err != nil || got.Status != "manual_review" || got.Stage != "runtime" || adapter.reads != 0 || adapter.mutations != 0 ||
		got.Attempts["runtime"].PendingReadbacks != 0 || got.Attempts["runtime"].MaxPendingReadbacks != workspaceLaunchLegacyV3AuthoritativeReadBudget ||
		len(got.FreshContinuationAuthorizations) != 0 || len(got.ContinuationReadClaims) != 0 {
		t.Fatalf("PostgreSQL legacy v3 row invented continuation authority: operation=%s reads=%d mutations=%d err=%v",
			workspaceLaunchReconcileResultSummary(got), adapter.reads, adapter.mutations, err)
	}
}
