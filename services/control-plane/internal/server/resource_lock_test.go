package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
)

type signalingReconcileFabric struct {
	fakeFabricClient
	computeEntered chan struct{}
	storageEntered chan struct{}
}

func TestEntitlementConsumersWaitForResourceMutation(t *testing.T) {
	app, err := newControlPlaneAppWithStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	service := newTestService(fakeLedgerClient{}, &fakeFabricClient{})
	mux := http.NewServeMux()
	registerAuthRoutes(mux, app)
	registerAdminRoutes(mux, app)
	registerResourceRoutes(mux, app, service)
	registerWorkspaceRoutes(mux, app, service)
	session := tenantAdminSessionForTest(t, mux)
	compute := createResourceWithSession(t, mux, session, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-alpha","packageId":"basic"}`)
	storage := createResourceWithSession(t, mux, session, http.MethodPost, "/api/storage-volumes", `{"accountId":"acct-alpha","sizeGb":10}`)

	assertWaits := func(path, body string) map[string]any {
		t.Helper()
		unlock := app.lockResource("compute", stringValue(compute["id"]))
		done := make(chan *httptest.ResponseRecorder, 1)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "resource-lock-"+path)
		addAuth(req, session)
		go func() {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			done <- rec
		}()
		select {
		case rec := <-done:
			unlock()
			t.Fatalf("%s crossed compute lock with status %d: %s", path, rec.Code, rec.Body.String())
		case <-time.After(50 * time.Millisecond):
		}
		unlock()
		rec := <-done
		if rec.Code < 200 || rec.Code >= 300 {
			t.Fatalf("%s status = %d: %s", path, rec.Code, rec.Body.String())
		}
		var result map[string]any
		if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}

	attachment := assertWaits("/api/storage-attachments", `{"accountId":"acct-alpha","computeAllocationId":"`+stringValue(compute["id"])+`","storageId":"`+stringValue(storage["id"])+`"}`)
	assertWaits("/api/workspaces", `{"accountId":"acct-alpha","attachmentId":"`+stringValue(attachment["id"])+`","workspaceName":"Locked Workspace"}`)
}

func (f *signalingReconcileFabric) SyncComputeAllocation(_ context.Context, id string) (clients.ComputeAllocation, error) {
	f.computeEntered <- struct{}{}
	return clients.ComputeAllocation{ID: id, Status: "running"}, nil
}

func (f *signalingReconcileFabric) SyncStorageVolume(_ context.Context, id string) (clients.StorageVolume, error) {
	f.storageEntered <- struct{}{}
	return clients.StorageVolume{ID: id, Status: "available"}, nil
}

func TestProviderReconcileSerializesResourceMutation(t *testing.T) {
	for _, resourceType := range []string{"compute", "storage"} {
		t.Run(resourceType, func(t *testing.T) {
			app := newControlPlaneAppEmpty()
			fabric := &signalingReconcileFabric{computeEntered: make(chan struct{}, 1), storageEntered: make(chan struct{}, 1)}
			service := newTestService(fakeLedgerClient{}, fabric)
			row := map[string]any{"id": resourceType + "-alpha", "accountId": "acct-alpha", "status": "running", "billingStatus": "active"}
			if resourceType == "storage" {
				row["status"] = "available"
			}
			if err := app.saveMonthlyResource(context.Background(), resourceType, row); err != nil {
				t.Fatal(err)
			}

			unlock := app.lockResource(resourceType, stringValue(row["id"]))
			done := make(chan error, 1)
			entered := fabric.computeEntered
			if resourceType == "storage" {
				entered = fabric.storageEntered
				go func() { done <- app.reconcileMonthlyStorage(context.Background(), service, row, time.Now().UTC()) }()
			} else {
				go func() { done <- app.reconcileMonthlyCompute(context.Background(), service, row, time.Now().UTC()) }()
			}
			select {
			case <-entered:
				unlock()
				t.Fatal("provider reconcile crossed the resource lock")
			case <-time.After(50 * time.Millisecond):
			}
			unlock()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("provider reconcile did not resume after resource unlock")
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
