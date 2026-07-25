package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

type noRuntimeFanoutFabric struct {
	fakeFabricClient
	mu    sync.Mutex
	calls int
}

func (f *noRuntimeFanoutFabric) WorkspaceRuntimeStatus(_ context.Context, workspaceID string) (clients.WorkspaceRuntime, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return clients.WorkspaceRuntime{ID: "runtime-" + workspaceID, WorkspaceID: workspaceID, Status: "running", Ready: true}, nil
}

type runtimeHealthSummaryFabric struct {
	noRuntimeFanoutFabric
	summary      clients.RuntimeHealthSummary
	summaryCalls int
}

type boundedOperatorHealthStore struct {
	*memoryTableStore
	listWorkspaceCalls int
	pageWorkspaceCalls int
}

func (s *boundedOperatorHealthStore) ListWorkspaces(ctx context.Context, accountID string) ([]map[string]any, error) {
	s.listWorkspaceCalls++
	return s.memoryTableStore.ListWorkspaces(ctx, accountID)
}

func (s *boundedOperatorHealthStore) PageWorkspaces(ctx context.Context, accountID string, query tablePageQuery) (tablePage, error) {
	s.pageWorkspaceCalls++
	return s.memoryTableStore.PageWorkspaces(ctx, accountID, query)
}

func (f *runtimeHealthSummaryFabric) RuntimeHealthSummary(context.Context) (clients.RuntimeHealthSummary, error) {
	f.mu.Lock()
	f.summaryCalls++
	f.mu.Unlock()
	return f.summary, nil
}

func TestOperatorHealthUsesSingleFabricRuntimeSummary(t *testing.T) {
	store := &boundedOperatorHealthStore{memoryTableStore: newMemoryTableStore()}
	for _, workspaceID := range []string{"ws-a", "ws-b", "ws-c", "ws-d", "ws-e"} {
		mustStore(t, store.SaveWorkspace(context.Background(), map[string]any{
			"id": workspaceID, "ownerAccountId": "acct-alpha", "ownerUserId": "usr-alpha", "accountId": "acct-alpha", "state": "active",
			"createdAt": "2026-07-18T00:00:00Z", "updatedAt": "2026-07-19T00:00:00Z",
		}))
	}
	fabric := &runtimeHealthSummaryFabric{summary: clients.RuntimeHealthSummary{Total: 5, Ready: 4, Unready: 1}}
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, fabric, newOperatorProjectionClient()), store)
	if err != nil {
		t.Fatal(err)
	}
	operator := reservedOperatorSessionForTest(t, server)
	req := httptest.NewRequest(http.MethodGet, "/api/operator/health", nil)
	addAuth(req, operator)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("operator health = %d: %s", response.Code, response.Body.String())
	}
	fabric.mu.Lock()
	statusCalls, summaryCalls := fabric.calls, fabric.summaryCalls
	fabric.mu.Unlock()
	if statusCalls != 0 || summaryCalls != 1 {
		t.Fatalf("runtime health calls status=%d summary=%d", statusCalls, summaryCalls)
	}
	if store.listWorkspaceCalls != 0 || store.pageWorkspaceCalls != 1 {
		t.Fatalf("Workspace health reads list=%d page=%d", store.listWorkspaceCalls, store.pageWorkspaceCalls)
	}
	envelope := decodeOperatorEnvelope(t, response)
	health := mapField(envelope, "data")
	runtimeEnvelope := mapField(health, "runtime")
	if runtimeEnvelope["available"] != true {
		t.Fatalf("Runtime health = %#v", runtimeEnvelope)
	}
	runtimeData := mapField(runtimeEnvelope, "data")
	if runtimeData["ready"] != false || runtimeData["total"] != float64(5) || runtimeData["available"] != float64(5) || runtimeData["unready"] != float64(1) {
		t.Fatalf("Runtime health data = %#v", runtimeData)
	}
	if _, ok := runtimeData["items"]; ok {
		t.Fatalf("Runtime health must not return per-Workspace items: %#v", runtimeData)
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

func TestOperatorHealthNeverFansOutAcrossWorkspaceRuntimes(t *testing.T) {
	store := newMemoryTableStore()
	for index := 0; index < 1000; index++ {
		workspaceID := fmt.Sprintf("ws-%04d", index)
		mustStore(t, store.SaveWorkspace(context.Background(), map[string]any{
			"id": workspaceID, "ownerAccountId": "acct-alpha", "ownerUserId": "usr-alpha", "accountId": "acct-alpha", "state": "active",
			"createdAt": "2026-07-18T00:00:00Z", "updatedAt": "2026-07-19T00:00:00Z",
		}))
	}
	fabric := &noRuntimeFanoutFabric{}
	runtime := (&controlPlaneServer{tables: store}).operatorRuntimeHealth(context.Background(), controlplane.NewService(fakeLedgerClient{}, fabric, newOperatorProjectionClient()))
	fabric.mu.Lock()
	calls := fabric.calls
	fabric.mu.Unlock()
	if calls != 0 || runtime["available"] != false || runtime["status"] != "unavailable" {
		t.Fatalf("runtime health must fail closed without a Fabric summary: calls=%d runtime=%#v", calls, runtime)
	}
}
