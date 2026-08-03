package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

var errWorkspaceLaunchProcessRestart = errors.New("workspace_launch_process_restart")

type workspaceLaunchProcessRestartStore struct {
	*postgresEntStateStore
	stopPhase          string
	stopStorageReserve bool
	stopped            bool
}

type postgresWorkspaceLaunchClaimBarrier struct {
	mu      sync.Mutex
	waiting int
	release chan struct{}
	results chan error
}

type postgresWorkspaceLaunchClaimBarrierStore struct {
	*postgresEntStateStore
	barrier *postgresWorkspaceLaunchClaimBarrier
}

func (s *postgresWorkspaceLaunchClaimBarrierStore) ClaimWorkspaceLaunch(ctx context.Context, claim workspaceLaunchClaimCAS) error {
	s.barrier.mu.Lock()
	s.barrier.waiting++
	if s.barrier.waiting == 2 {
		close(s.barrier.release)
	}
	release := s.barrier.release
	s.barrier.mu.Unlock()
	select {
	case <-release:
	case <-ctx.Done():
		return ctx.Err()
	}
	err := s.postgresEntStateStore.ClaimWorkspaceLaunch(ctx, claim)
	s.barrier.results <- err
	return err
}

func (s *workspaceLaunchProcessRestartStore) PersistWorkspaceLaunch(ctx context.Context, update workspaceLaunchPersistCAS) error {
	operation, err := decodeWorkspaceLaunchOperation(update.DesiredOperation)
	if err != nil {
		return err
	}
	storageReserved := operation.Phase == "storage_fulfilling" && operation.ContinuationAttemptBudgets["storage"] == (workspaceLaunchStageBudget{Attempted: 1, Max: workspaceLaunchStageMax})
	stop := !s.stopped && (operation.Phase == s.stopPhase || s.stopStorageReserve && storageReserved)
	if stop && operation.LeaseExpiresAt != "" {
		operation.LeaseExpiresAt = time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano)
		desired := cloneMap(update.DesiredOperation)
		desired["result"] = encodeWorkspaceLaunchOperation(operation)
		update.DesiredOperation = desired
	}
	if err := s.postgresEntStateStore.PersistWorkspaceLaunch(ctx, update); err != nil {
		return err
	}
	if stop {
		s.stopped = true
		return errWorkspaceLaunchProcessRestart
	}
	return nil
}

func TestPostgresNormalWorkspaceLaunchSurvivesEveryPersistedBoundaryFromOnePost(t *testing.T) {
	t.Setenv("OPL_MONTHLY_BILLING_WORKER_ENABLED", "false")
	t.Setenv("OPL_PROVIDER_RECONCILE_WORKER_ENABLED", "false")
	t.Setenv("OPL_ARCHIVE_RETENTION_WORKER_ENABLED", "false")
	t.Setenv("OPL_WORKSPACE_LAUNCH_WORKER_ENABLED", "false")
	t.Setenv("OPL_WORKSPACE_IMAGE", workspaceImageRepository+"@sha256:"+strings.Repeat("d", 64))

	for _, plan := range []struct {
		packageID string
		storageGB int
		total     int64
	}{
		{packageID: "basic", storageGB: 10, total: 52_580_000},
	} {
		t.Run(plan.packageID, func(t *testing.T) {
			admin := openControlPlaneTestPostgres(t)
			schema := fmt.Sprintf("control_plane_normal_launch_%s_%d", plan.packageID, time.Now().UnixNano())
			if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
				t.Fatal(err)
			}
			databaseURL := controlPlaneTestPostgresURL(t, "postgres", schema)
			t.Cleanup(func() {
				_, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
				_ = admin.Close()
			})

			events := []string{}
			gateway := &durableWorkspaceLaunchSub2API{
				workspaceLaunchSub2API: &workspaceLaunchSub2API{
					monthlySub2API: &monthlySub2API{events: &events},
					keys:           map[int64]clients.Sub2APIWorkspaceKey{},
				},
				balance:        1_000_000_000,
				appliedCharges: map[string]clients.Sub2APIChargeInput{},
			}
			fabric := &monthlyFabric{fakeFabricClient: fakeFabricClient{calls: &events}, events: &events}
			ledger := &workspaceLaunchLedger{events: &events, receipts: map[string]clients.Receipt{}}
			service := controlplane.NewService(ledger, fabric, gateway)

			state, err := newTestPostgresEntStateStore(databaseURL)
			if err != nil {
				t.Fatal(err)
			}
			store := state.(*postgresEntStateStore)
			seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
			handler, err := NewPersistentServer(service, store)
			if err != nil {
				t.Fatal(err)
			}
			session := loginForTest(t, handler, "alpha@example.com", "CorrectHorseBatteryStaple!")
			body := fmt.Sprintf(`{"name":"Normal %s","packageId":%q,"sizeGb":%d,"autoRenew":false}`, plan.packageID, plan.packageID, plan.storageGB)
			created := requestWithMutationKeyForTest(t, handler, session, http.MethodPost, "/api/workspace-launches", body, "normal-launch-post")
			if created.Code != http.StatusAccepted {
				t.Fatalf("single POST status=%d body=%s", created.Code, created.Body.String())
			}
			rows, err := store.ListRuntimeOperations(context.Background())
			if err != nil || len(rows) != 1 {
				t.Fatalf("single POST operations=%#v err=%v", rows, err)
			}
			operation, err := decodeWorkspaceLaunchOperation(rows[0])
			if err != nil || operation.Phase != "debit_pending" || operation.WorkspaceAPIKeyID <= 0 || gateway.createCalls != 1 {
				t.Fatalf("Key boundary operation=%#v keyCreates=%d err=%v", operation, gateway.createCalls, err)
			}
			launchID := operation.ID
			configurePostgresNormalWorkspaceLaunchFabric(fabric, operation)
			if err := store.client.Close(); err != nil {
				t.Fatal(err)
			}

			for _, phase := range []string{"debited", "storage_fulfilling", "attaching", "secret_writing", "runtime_starting", "activating", "receipt_pending", "succeeded"} {
				state, err = newTestPostgresEntStateStore(databaseURL)
				if err != nil {
					t.Fatal(err)
				}
				restarted := &workspaceLaunchProcessRestartStore{postgresEntStateStore: state.(*postgresEntStateStore), stopPhase: phase}
				app, err := newControlPlaneAppWithStore(restarted)
				if err != nil {
					t.Fatal(err)
				}
				if err := app.runWorkspaceLaunchesOnce(context.Background(), service); !errors.Is(err, errWorkspaceLaunchProcessRestart) || !restarted.stopped {
					t.Fatalf("phase %s did not stop after persisted boundary: err=%v stopped=%t", phase, err, restarted.stopped)
				}
				row, found, err := restarted.GetRuntimeOperation(context.Background(), launchID)
				if err != nil || !found {
					t.Fatalf("phase %s launch found=%t err=%v", phase, found, err)
				}
				persisted, err := decodeWorkspaceLaunchOperation(row)
				if err != nil || persisted.ID != launchID || persisted.Phase != phase || persisted.Status == "manual_review" {
					t.Fatalf("phase %s persisted operation=%#v err=%v", phase, persisted, err)
				}
				if err := restarted.client.Close(); err != nil {
					t.Fatal(err)
				}
			}

			state, err = newTestPostgresEntStateStore(databaseURL)
			if err != nil {
				t.Fatal(err)
			}
			finalStore := state.(*postgresEntStateStore)
			t.Cleanup(func() { _ = finalStore.client.Close() })
			row, found, err := finalStore.GetRuntimeOperation(context.Background(), launchID)
			if err != nil || !found {
				t.Fatalf("final launch found=%t err=%v", found, err)
			}
			completed, err := decodeWorkspaceLaunchOperation(row)
			computes, computeErr := finalStore.ListComputes(context.Background(), operation.AccountID)
			storages, storageErr := finalStore.ListStorages(context.Background(), operation.AccountID)
			attachments, attachmentErr := finalStore.ListAttachments(context.Background(), operation.AccountID)
			workspaces, workspaceErr := finalStore.ListWorkspaces(context.Background(), operation.AccountID)
			if err != nil || computeErr != nil || storageErr != nil || attachmentErr != nil || workspaceErr != nil ||
				completed.Status != "succeeded" || completed.Phase != "succeeded" || completed.ID != launchID || completed.URL == "" || completed.ReceiptID == "" ||
				len(computes) != 1 || len(storages) != 1 || len(attachments) != 1 || len(workspaces) != 1 {
				t.Fatalf("terminal launch=%#v compute=%#v storage=%#v attachment=%#v workspace=%#v errors=%v/%v/%v/%v/%v", completed, computes, storages, attachments, workspaces, err, computeErr, storageErr, attachmentErr, workspaceErr)
			}
			if len(gateway.chargeCalls) != 1 || gateway.chargeCalls[0].ChargeUSDMicros != plan.total || len(gateway.refunds) != 0 ||
				len(fabric.computeIDs) != 1 || len(fabric.storageIDs) != 1 || countStrings(events, "fabric.attachment") != 1 ||
				countStrings(events, "fabric.gateway-secret") != 1 || countStrings(events, "fabric.runtime") != 1 || len(ledger.receiptInputs) != 1 ||
				ledger.receiptInputs[0].Type != "billing.workspace_purchased.v1" || ledger.receiptInputs[0].RequestID != launchID || len(ledger.receipts) != 1 {
				t.Fatalf("mutation counts key=%d debit=%d refund=%d compute=%d storage=%d attachment=%d secret=%d runtime=%d receipts=%#v events=%#v", gateway.createCalls, len(gateway.chargeCalls), len(gateway.refunds), len(fabric.computeIDs), len(fabric.storageIDs), countStrings(events, "fabric.attachment"), countStrings(events, "fabric.gateway-secret"), countStrings(events, "fabric.runtime"), ledger.receiptInputs, events)
			}
			assertWorkspaceLaunchRuntimeIdentity(t, fabric.runtimeInputs, completed)
		})
	}
}

func TestPostgresWorkspaceLaunchReservedStorageAttemptResumesStagedPrepare(t *testing.T) {
	t.Setenv("OPL_MONTHLY_BILLING_WORKER_ENABLED", "false")
	t.Setenv("OPL_PROVIDER_RECONCILE_WORKER_ENABLED", "false")
	t.Setenv("OPL_ARCHIVE_RETENTION_WORKER_ENABLED", "false")
	t.Setenv("OPL_WORKSPACE_LAUNCH_WORKER_ENABLED", "false")
	t.Setenv("OPL_WORKSPACE_IMAGE", workspaceImageRepository+"@sha256:"+strings.Repeat("d", 64))

	admin := openControlPlaneTestPostgres(t)
	schema := fmt.Sprintf("control_plane_storage_resume_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	databaseURL := controlPlaneTestPostgresURL(t, "postgres", schema)
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		_ = admin.Close()
	})

	events := []string{}
	gateway := &durableWorkspaceLaunchSub2API{
		workspaceLaunchSub2API: &workspaceLaunchSub2API{
			monthlySub2API: &monthlySub2API{events: &events},
			keys:           map[int64]clients.Sub2APIWorkspaceKey{},
		},
		balance:        1_000_000_000,
		appliedCharges: map[string]clients.Sub2APIChargeInput{},
	}
	fabric := &monthlyFabric{fakeFabricClient: fakeFabricClient{calls: &events}, events: &events}
	ledger := &workspaceLaunchLedger{events: &events, receipts: map[string]clients.Receipt{}}
	service := controlplane.NewService(ledger, fabric, gateway)

	state, err := newTestPostgresEntStateStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	store := state.(*postgresEntStateStore)
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	handler, err := NewPersistentServer(service, store)
	if err != nil {
		t.Fatal(err)
	}
	session := loginForTest(t, handler, "alpha@example.com", "CorrectHorseBatteryStaple!")
	created := requestWithMutationKeyForTest(t, handler, session, http.MethodPost, "/api/workspace-launches", `{"name":"Storage Resume","packageId":"basic","sizeGb":10,"autoRenew":false}`, "storage-resume-post")
	if created.Code != http.StatusAccepted {
		t.Fatalf("single POST status=%d body=%s", created.Code, created.Body.String())
	}
	rows, err := store.ListRuntimeOperations(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("single POST operations=%#v err=%v", rows, err)
	}
	operation, err := decodeWorkspaceLaunchOperation(rows[0])
	if err != nil {
		t.Fatal(err)
	}
	configurePostgresNormalWorkspaceLaunchFabric(fabric, operation)
	if err := store.client.Close(); err != nil {
		t.Fatal(err)
	}

	state, err = newTestPostgresEntStateStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	first := &workspaceLaunchProcessRestartStore{postgresEntStateStore: state.(*postgresEntStateStore), stopStorageReserve: true}
	app, err := newControlPlaneAppWithStore(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.runWorkspaceLaunchesOnce(context.Background(), service); !errors.Is(err, errWorkspaceLaunchProcessRestart) || !first.stopped {
		t.Fatalf("storage reserve did not stop after persisted boundary: err=%v stopped=%t", err, first.stopped)
	}
	row, found, err := first.GetRuntimeOperation(context.Background(), operation.ID)
	if err != nil || !found {
		t.Fatalf("reserved launch found=%t err=%v", found, err)
	}
	reserved, err := decodeWorkspaceLaunchOperation(row)
	if err != nil {
		t.Fatal(err)
	}
	if reserved.Status == "manual_review" || reserved.Phase != "storage_fulfilling" || reserved.ContinuationAttemptBudgets["storage"] != (workspaceLaunchStageBudget{Attempted: 1, Max: workspaceLaunchStageMax}) {
		t.Fatalf("reserved PostgreSQL launch=%#v", reserved)
	}
	if err := first.client.Close(); err != nil {
		t.Fatal(err)
	}

	state, err = newTestPostgresEntStateStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	reopened := state.(*postgresEntStateStore)
	t.Cleanup(func() { _ = reopened.client.Close() })
	restarted, err := newControlPlaneAppWithStore(reopened)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.runWorkspaceLaunchesOnce(context.Background(), service); err != nil {
		t.Fatal(err)
	}
	row, found, err = reopened.GetRuntimeOperation(context.Background(), operation.ID)
	if err != nil || !found {
		t.Fatalf("completed launch found=%t err=%v", found, err)
	}
	completed, err := decodeWorkspaceLaunchOperation(row)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != "succeeded" || completed.Phase != "succeeded" || completed.ContinuationAttemptBudgets["storage"] != (workspaceLaunchStageBudget{Attempted: 1, Confirmed: 1, Max: workspaceLaunchStageMax}) {
		t.Fatalf("reopened Storage launch=%#v", completed)
	}
	if len(gateway.chargeCalls) != 1 || len(fabric.computeIDs) != 1 || len(fabric.storageIDs) != 1 || len(fabric.storageCreateKeys) != 1 || fabric.storageCreateKeys[0] != operation.ID+":storage" {
		t.Fatalf("reopened Storage repeated mutation or changed identity: charges=%#v compute=%#v storage=%#v keys=%#v events=%#v", gateway.chargeCalls, fabric.computeIDs, fabric.storageIDs, fabric.storageCreateKeys, events)
	}
}

func configurePostgresNormalWorkspaceLaunchFabric(fabric *monthlyFabric, operation workspaceLaunchOperation) {
	instanceType := "S5.MEDIUM4"
	if operation.PackageID == "pro" {
		instanceType = "SA5.2XLARGE16"
	}
	fabric.computeSync = clients.ComputeAllocation{
		ID: operation.ComputeID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, PackageID: operation.PackageID,
		Status: "running", Provider: "tencent-tke", ProviderResourceID: "ins-" + operation.ComputeID, ProviderRequestID: "req-" + operation.ComputeID,
		InstanceID: "ins-" + operation.ComputeID, InstanceType: instanceType, Zone: "ap-shanghai-2", ChargeType: "PREPAID",
		RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2099-01-01T00:00:00Z", ProviderData: map[string]string{"zone": "ap-shanghai-2", "instanceType": instanceType},
	}
	fabric.storageSync = clients.StorageVolume{
		ID: operation.StorageID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, Status: "available",
		Provider: "tencent-tke", ProviderResourceID: "disk-" + operation.StorageID, ProviderRequestID: "req-" + operation.StorageID,
		SizeGB: operation.StorageGB, CBSStatus: "UNATTACHED", DiskType: "CLOUD_PREMIUM", RenewFlag: "NOTIFY_AND_MANUAL_RENEW",
		Deadline: "2099-01-01T00:00:00Z", Zone: "ap-shanghai-2", ProviderData: map[string]string{"chargeType": "PREPAID"},
	}
}

func TestPostgresBlockedTerminalRecoveryPlanCreatesPersistedSuccessorAfterReopen(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")
	useWorkspaceRecoveryPlanIdentityEvidence(t, &fixture, &clients.ComputeClaimIdentityEvidence{
		Checks: []clients.ComputeClaimIdentityCheck{{
			Field: "binding.compatibility", Matches: true, Expected: "current_or_historical", Actual: "historical",
		}},
		MutationLedger: "observed", MutationLedgerOutcome: "confirmed_zero", MutationLedgerDigest: strings.Repeat("d", 64),
	})
	first := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
	failed := fixture.operation(t)
	failed.Status, failed.ErrorCode = "manual_review", "workspace_compute_claim_identity_mismatch"
	failed.RecoveryPlan.Status, failed.RecoveryPlan.ErrorCode = "blocked", "identity_mismatch"
	failed.RecoveryPlan.Mismatches = []workspaceRecoveryPlanMismatch{{
		Field: "release.mainSha", Expected: strings.Repeat("d", 40), Actual: strings.Repeat("a", 40),
	}}
	failed.RecoveryExecution = &workspaceRecoveryExecution{
		ExecutionID: "recovery-exec-postgres-failed-zero", RunIdentity: "control-plane-run-postgres-failed-zero",
		PlanID: first.PlanID, PlanDigest: first.PlanDigest, ApprovalDigest: strings.Repeat("c", 64), Decision: "continue",
		Status: "failed", StartedAt: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano),
		CompletedAt: time.Now().UTC().Format(time.RFC3339Nano), ErrorCode: failed.ErrorCode,
	}

	admin := openControlPlaneTestPostgres(t)
	schema := fmt.Sprintf("control_plane_recovery_successor_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		_ = admin.Close()
	})
	state, err := newTestPostgresEntStateStore(controlPlaneTestPostgresURL(t, "postgres", schema))
	if err != nil {
		t.Fatal(err)
	}
	store := state.(*postgresEntStateStore)
	t.Cleanup(func() { _ = store.client.Close() })
	seedTenantMember(t, store, failed.AccountID, "org-alpha", failed.OwnerUserID, "alpha@example.com")
	if err := store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(failed)); err != nil {
		t.Fatal(err)
	}
	server, err := NewPersistentServer(fixture.service, store)
	if err != nil {
		t.Fatal(err)
	}
	pgFixture := fixture
	pgFixture.server, pgFixture.operator = server, reservedOperatorSessionForTest(t, server)
	response := requestWorkspaceRecoveryPlan(t, pgFixture, http.MethodPost, "/diagnose", map[string]any{"accountId": failed.AccountID})
	if response.Code != http.StatusOK {
		t.Fatalf("PostgreSQL successor diagnose status=%d body=%s", response.Code, response.Body.String())
	}
	successor := recoveryPlanResponse(t, response)
	app, err := newControlPlaneAppWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	persisted, found, err := app.workspaceLaunchOperation(context.Background(), failed.ID)
	if err != nil || !found || persisted.RecoveryPlan == nil || persisted.RecoveryExecution != nil || len(persisted.RecoveryHistory) != 1 ||
		successor.PlanID == first.PlanID || successor.PlanDigest == first.PlanDigest || persisted.RecoveryHistory[0].Execution.MutationOutcome.Status != "confirmed_zero" ||
		persisted.RecoveryHistory[0].Execution.MutationOutcome.EvidenceDigest != strings.Repeat("d", 64) || persisted.RecoveryHistory[0].Plan.Status != "failed" {
		t.Fatalf("PostgreSQL successor=%#v operation=%#v found=%v err=%v", successor, persisted, found, err)
	}
}

func TestPostgresReleasedRecoveryExecutionConvergesOnceAcrossReopenedWorkers(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	t.Setenv("OPL_WORKSPACE_LAUNCH_WORKER_ENABLED", "false")
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")
	claimed := computeClaimRecoveryProofForLaunch(operation, "target_owned")
	fixture.fabric.computeClaimResult = &claimed
	configureWorkspaceLaunchFulfillment(t, fixture)
	configureWorkspaceComputeClaimReadback(fixture, operation)
	readyRuntime := clients.WorkspaceRuntime{
		ID: "runtime-from-fabric", OperationID: operation.WorkspaceOperationID + ":runtime", WorkspaceID: operation.WorkspaceID,
		URL: "https://workspace.medopl.cn/w/" + operation.WorkspaceID + "/", Status: "running", Ready: true, ServiceName: "opl-compute-from-fabric",
		Access: clients.WorkspaceRuntimeAccess{Username: "admin", Password: "runtime-password-alpha", CredentialStatus: "configured", CredentialVersion: "v1", SecretRef: "opl-compute-from-fabric-env"},
	}
	unreadyRuntime := readyRuntime
	unreadyRuntime.Status, unreadyRuntime.Ready = "unready", false
	fixture.fabric.runtime = unreadyRuntime
	fixture.fabric.runtimeStatusResults = []clients.WorkspaceRuntime{unreadyRuntime}

	admin := openControlPlaneTestPostgres(t)
	schema := fmt.Sprintf("control_plane_recovery_worker_cas_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	databaseURL := controlPlaneTestPostgresURL(t, "postgres", schema)
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		_ = admin.Close()
	})
	state, err := newTestPostgresEntStateStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	firstStore := state.(*postgresEntStateStore)
	seedTenantMember(t, firstStore, operation.AccountID, "org-alpha", operation.OwnerUserID, "alpha@example.com")
	computes, err := fixture.store.ListComputes(context.Background(), operation.AccountID)
	if err != nil || len(computes) != 1 {
		t.Fatalf("compute claim fixture rows=%#v err=%v", computes, err)
	}
	if err := firstStore.SaveCompute(context.Background(), computes[0]); err != nil {
		t.Fatal(err)
	}
	if err := firstStore.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(operation)); err != nil {
		t.Fatal(err)
	}
	server, err := NewPersistentServer(fixture.service, firstStore)
	if err != nil {
		t.Fatal(err)
	}
	pgFixture := fixture
	pgFixture.server, pgFixture.operator = server, reservedOperatorSessionForTest(t, server)
	diagnosed := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, pgFixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
	validated := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, pgFixture, http.MethodPost, "/validate", map[string]any{
		"planId": diagnosed.PlanID, "planDigest": diagnosed.PlanDigest,
	}))
	execute := requestWorkspaceRecoveryPlan(t, pgFixture, http.MethodPost, "/execute", map[string]any{
		"planId": validated.PlanID, "planDigest": validated.PlanDigest, "decision": "continue", "confirmation": "CONTINUE_RECOVERY_PLAN",
	})
	if execute.Code != http.StatusOK {
		t.Fatalf("PostgreSQL waiting execute status=%d body=%s", execute.Code, execute.Body.String())
	}
	waitingRow, found, err := firstStore.GetRuntimeOperation(context.Background(), operation.ID)
	if err != nil || !found {
		t.Fatalf("PostgreSQL waiting launch found=%t err=%v", found, err)
	}
	waiting, err := decodeWorkspaceLaunchOperation(waitingRow)
	if err != nil || waiting.Status != "waiting" || waiting.Phase != "runtime_starting" || waiting.LeaseToken != "" || waiting.LeaseExpiresAt != "" ||
		waiting.RecoveryPlan == nil || waiting.RecoveryPlan.Status != "executing" || waiting.RecoveryExecution == nil || waiting.RecoveryExecution.Status != "running" ||
		waiting.RecoveryExecution.LeaseToken != "" || waiting.RecoveryExecution.LeaseExpiresAt != "" {
		t.Fatalf("PostgreSQL waiting recovery launch=%#v err=%v", waiting, err)
	}
	if err := firstStore.client.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.fabric.runtime = readyRuntime

	barrier := &postgresWorkspaceLaunchClaimBarrier{release: make(chan struct{}), results: make(chan error, 2)}
	apps := make([]*controlPlaneServer, 0, 2)
	stores := make([]*postgresEntStateStore, 0, 2)
	for range 2 {
		state, err := newTestPostgresEntStateStore(databaseURL)
		if err != nil {
			t.Fatal(err)
		}
		store := state.(*postgresEntStateStore)
		stores = append(stores, store)
		app, err := newControlPlaneAppWithStore(&postgresWorkspaceLaunchClaimBarrierStore{postgresEntStateStore: store, barrier: barrier})
		if err != nil {
			t.Fatal(err)
		}
		apps = append(apps, app)
	}
	workerCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	workerResults := make(chan error, 2)
	start := make(chan struct{})
	for _, app := range apps {
		go func(app *controlPlaneServer) {
			<-start
			workerResults <- app.runWorkspaceLaunchesOnce(workerCtx, fixture.service)
		}(app)
	}
	close(start)
	for range 2 {
		if err := <-workerResults; err != nil {
			t.Fatalf("reopened PostgreSQL worker failed: %v", err)
		}
	}
	casWinners, casConflicts := 0, 0
	for range 2 {
		switch err := <-barrier.results; {
		case err == nil:
			casWinners++
		case errors.Is(err, errWorkspaceLaunchCASConflict):
			casConflicts++
		default:
			t.Fatalf("unexpected PostgreSQL workspace launch CAS result: %v", err)
		}
	}
	if casWinners != 1 || casConflicts != 1 {
		t.Fatalf("PostgreSQL workspace launch CAS winners=%d conflicts=%d", casWinners, casConflicts)
	}
	for _, store := range stores {
		if err := store.client.Close(); err != nil {
			t.Fatal(err)
		}
	}

	state, err = newTestPostgresEntStateStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	finalStore := state.(*postgresEntStateStore)
	t.Cleanup(func() { _ = finalStore.client.Close() })
	finalRow, found, err := finalStore.GetRuntimeOperation(context.Background(), operation.ID)
	if err != nil || !found {
		t.Fatalf("final PostgreSQL recovery launch found=%t err=%v", found, err)
	}
	completed, err := decodeWorkspaceLaunchOperation(finalRow)
	if err != nil || completed.Status != "succeeded" || completed.Phase != "succeeded" || completed.URL == "" || completed.ReceiptID == "" ||
		completed.RecoveryPlan == nil || completed.RecoveryPlan.Status != "completed" || completed.RecoveryPlan.URL != completed.URL || completed.RecoveryPlan.ReceiptID != completed.ReceiptID ||
		completed.RecoveryExecution == nil || completed.RecoveryExecution.Status != "completed" || completed.RecoveryExecution.CompletedAt == "" {
		t.Fatalf("final PostgreSQL recovery launch=%#v err=%v", completed, err)
	}
	if len(fixture.fabric.computeClaimCalls) != 1 || len(fixture.fabric.storageIDs) != 1 || len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeIDs) != 1 ||
		len(fixture.ledger.receiptInputs) != 1 {
		t.Fatalf("PostgreSQL recovery repeated mutation: claims=%d storage=%d charges=%d compute=%d receipts=%d", len(fixture.fabric.computeClaimCalls), len(fixture.fabric.storageIDs), len(fixture.sub2API.charges), len(fixture.fabric.computeIDs), len(fixture.ledger.receiptInputs))
	}
}
