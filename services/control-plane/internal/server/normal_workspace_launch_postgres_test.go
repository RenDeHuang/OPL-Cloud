package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

var errWorkspaceLaunchProcessRestart = errors.New("workspace_launch_process_restart")

type workspaceLaunchProcessRestartStore struct {
	*postgresEntStateStore
	stopPhase string
	stopped   bool
}

func (s *workspaceLaunchProcessRestartStore) PersistWorkspaceLaunch(ctx context.Context, update workspaceLaunchPersistCAS) error {
	operation, err := decodeWorkspaceLaunchOperation(update.DesiredOperation)
	if err != nil {
		return err
	}
	stop := !s.stopped && operation.Phase == s.stopPhase
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
		{packageID: "pro", storageGB: 100, total: 240_080_000},
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
