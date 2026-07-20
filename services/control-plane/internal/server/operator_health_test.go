package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

type boundedRuntimeProbeFabric struct {
	fakeFabricClient
	mu        sync.Mutex
	active    int
	maxActive int
	cancelled int
}

func (f *boundedRuntimeProbeFabric) WorkspaceRuntimeStatus(ctx context.Context, workspaceID string) (clients.WorkspaceRuntime, error) {
	f.mu.Lock()
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()
	if workspaceID == "ws-slow" {
		<-ctx.Done()
		f.mu.Lock()
		f.cancelled++
		f.mu.Unlock()
		return clients.WorkspaceRuntime{}, ctx.Err()
	}
	select {
	case <-time.After(20 * time.Millisecond):
		return clients.WorkspaceRuntime{
			ID: "runtime-" + workspaceID, WorkspaceID: workspaceID, Status: "running", Ready: true,
			Access: clients.WorkspaceRuntimeAccess{Username: "opl", Password: "runtime-password-must-not-leak", SecretRef: "runtime-secret-ref"},
		}, nil
	case <-ctx.Done():
		return clients.WorkspaceRuntime{}, ctx.Err()
	}
}

func TestOperatorHealthBoundsRuntimeProbesAndDropsSecrets(t *testing.T) {
	store := newMemoryTableStore()
	for _, workspaceID := range []string{"ws-a", "ws-b", "ws-c", "ws-d", "ws-slow"} {
		mustStore(t, store.SaveWorkspace(context.Background(), map[string]any{
			"id": workspaceID, "ownerAccountId": "acct-alpha", "ownerUserId": "usr-alpha", "accountId": "acct-alpha", "state": "active",
			"createdAt": "2026-07-18T00:00:00Z", "updatedAt": "2026-07-19T00:00:00Z",
		}))
	}
	fabric := &boundedRuntimeProbeFabric{}
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, fabric, newOperatorProjectionClient()), store)
	if err != nil {
		t.Fatal(err)
	}
	operator := reservedOperatorSessionForTest(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/operator/health", nil).WithContext(ctx)
	addAuth(req, operator)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("operator health = %d: %s", response.Code, response.Body.String())
	}
	if fabric.maxActive != 3 || fabric.cancelled != 1 {
		t.Fatalf("runtime probe concurrency max=%d cancelled=%d", fabric.maxActive, fabric.cancelled)
	}
	if strings.Contains(response.Body.String(), "runtime-password-must-not-leak") || strings.Contains(response.Body.String(), "runtime-secret-ref") {
		t.Fatalf("operator health leaked Runtime secret: %s", response.Body.String())
	}
	envelope := decodeOperatorEnvelope(t, response)
	health := mapField(envelope, "data")
	runtimeEnvelope := mapField(health, "runtime")
	if runtimeEnvelope["available"] != true {
		t.Fatalf("partial Runtime health = %#v", runtimeEnvelope)
	}
	runtimeData := mapField(runtimeEnvelope, "data")
	if runtimeData["ready"] != false || runtimeData["total"] != float64(5) || runtimeData["available"] != float64(4) {
		t.Fatalf("Runtime health data = %#v", runtimeData)
	}
}

func TestOperatorHealthMarksRuntimeUnavailableWithoutRealProbe(t *testing.T) {
	store := newMemoryTableStore()
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, newOperatorProjectionClient()), store)
	if err != nil {
		t.Fatal(err)
	}
	response := requestWithSession(t, server, reservedOperatorSessionForTest(t, server), http.MethodGet, "/api/operator/health", "")
	if response.Code != http.StatusOK {
		t.Fatalf("operator health without runtime = %d: %s", response.Code, response.Body.String())
	}
	var envelope map[string]any
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	runtimeEnvelope := mapField(mapField(envelope, "data"), "runtime")
	if runtimeEnvelope["available"] != false || runtimeEnvelope["status"] != "unavailable" {
		t.Fatalf("Runtime without probe = %#v", runtimeEnvelope)
	}
}
