package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

type retiredRouteDownstreamProbe struct {
	runtimeProxyCalls int
	providerCalls     int
	storeWrites       int
}

func (p *retiredRouteDownstreamProbe) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	p.runtimeProxyCalls++
	p.providerCalls++
	p.storeWrites++
	w.WriteHeader(http.StatusNoContent)
}

func TestRetiredConsoleAPIRoutesAreMethodlessTombstones(t *testing.T) {
	paths := []string{
		"/api/workspaces/ws-alpha/backups",
		"/api/workspace-backups",
		"/api/workspace-backups/backup-alpha/export",
		"/api/workspaces/ws-alpha/recovery",
		"/api/workspaces/ws-alpha/sync",
		"/api/workspaces/ws-alpha/sync/mutations",
		"/api/workspaces/ws-alpha/transfer",
		"/api/workspaces/ws-alpha/transfers",
		"/api/workspaces/ws-alpha/transfers/transfer-alpha",
		"/api/workspaces/ws-alpha/contents/digest-alpha",
		"/api/gateway/summary",
		"/api/billing/summary",
		"/api/me",
		"/api/overview",
		"/api/payment/orders",
		"/api/payment-orders",
		"/api/api-keys",
		"/api/api-keys/key-alpha/revoke",
		"/api/gateway/keys/key-alpha/revoke",
	}
	contexts := []struct {
		name      string
		configure func(*http.Request)
	}{
		{name: "console host", configure: func(r *http.Request) { r.Host = "console.medopl.cn" }},
		{name: "workspace host", configure: func(r *http.Request) { r.Host = "workspace.medopl.cn" }},
		{name: "routing cookie", configure: func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "opl_ws_active", Value: "ws-alpha"}) }},
		{name: "workspace referer", configure: func(r *http.Request) { r.Header.Set("Referer", "https://workspace.medopl.cn/w/ws-alpha/") }},
	}
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut} {
		for _, path := range paths {
			for _, requestContext := range contexts {
				t.Run(method+" "+path+" "+requestContext.name, func(t *testing.T) {
					probe := &retiredRouteDownstreamProbe{}
					handler := &controlPlaneHTTPHandler{next: probe}
					req := httptest.NewRequest(method, path, nil)
					requestContext.configure(req)
					rec := httptest.NewRecorder()
					handler.ServeHTTP(rec, req)
					if rec.Code != http.StatusNotFound {
						t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
					}
					if rec.Header().Get("Strict-Transport-Security") == "" || rec.Header().Get("Content-Security-Policy") == "" {
						t.Fatalf("retired route missing security headers: %#v", rec.Header())
					}
					if probe.runtimeProxyCalls != 0 || probe.providerCalls != 0 || probe.storeWrites != 0 {
						t.Fatalf("retired route reached downstream: %#v", probe)
					}
				})
			}
		}
	}
}

func TestRetiredCustomerWriteRoutesAreMethodAwareTombstones(t *testing.T) {
	routes := map[string]string{
		"/api/compute-allocations": `{"packageId":"basic"}`,
		"/api/storage-volumes":     `{"packageId":"basic","sizeGb":10}`,
		"/api/storage-attachments": `{"computeAllocationId":"compute-alpha","storageId":"storage-alpha"}`,
		"/api/workspaces":          `{"attachmentId":"attachment-alpha"}`,
	}
	contexts := []struct {
		name      string
		configure func(*http.Request)
	}{
		{name: "console host", configure: func(r *http.Request) { r.Host = "console.medopl.cn" }},
		{name: "workspace host", configure: func(r *http.Request) { r.Host = "workspace.medopl.cn" }},
		{name: "routing cookie", configure: func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "opl_ws_active", Value: "ws-alpha"}) }},
		{name: "workspace referer", configure: func(r *http.Request) { r.Header.Set("Referer", "https://workspace.medopl.cn/w/ws-alpha/") }},
	}
	for path, body := range routes {
		for _, requestContext := range contexts {
			t.Run(path+" "+requestContext.name, func(t *testing.T) {
				store := newMemoryTableStore()
				seedTenantMember(t, store, "acct-gateway", "org-gateway", "usr-gateway-owner", "gateway-owner@example.com")
				fabricCalls := []string{}
				fabric := &fakeFabricClient{calls: &fabricCalls}
				sub2API := &testSub2APIClient{balance: 1_000_000_000_000, charges: map[string]int64{}}
				server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, fabric, sub2API), store)
				if err != nil {
					t.Fatal(err)
				}
				session := loginForTest(t, server, "gateway-owner@example.com", "CorrectHorseBatteryStaple!")
				beforeSessions := len(store.sessions)

				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Idempotency-Key", "retired-write")
				addAuth(req, session)
				requestContext.configure(req)
				rec := httptest.NewRecorder()
				server.ServeHTTP(rec, req)

				if rec.Code != http.StatusNotFound {
					t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
				}
				if len(sub2API.charges) != 0 || len(fabricCalls) != 0 || len(store.computes) != 0 || len(store.storages) != 0 || len(store.attachments) != 0 || len(store.workspaces) != 0 || len(store.runtimeOps) != 0 || len(store.auditEvents) != 0 || len(store.sessions) != beforeSessions {
					t.Fatalf("retired write reached downstream: charges=%d fabric=%v computes=%d storages=%d attachments=%d workspaces=%d operations=%d audits=%d sessions=%d/%d", len(sub2API.charges), fabricCalls, len(store.computes), len(store.storages), len(store.attachments), len(store.workspaces), len(store.runtimeOps), len(store.auditEvents), len(store.sessions), beforeSessions)
				}
			})
		}
	}

	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	session := tenantAdminSessionForTest(t, server)
	for _, path := range []string{"/api/compute-allocations", "/api/workspaces"} {
		response := requestWithSession(t, server, session, http.MethodGet, path, "")
		if response.Code == http.StatusNotFound {
			t.Fatalf("allowed read route %s was tombstoned", path)
		}
	}
}

type stateBalanceProbe struct {
	testSub2APIClient
	balanceCalls int
}

func (p *stateBalanceProbe) Balance(ctx context.Context, userID int64) (clients.Sub2APIBalance, error) {
	p.balanceCalls++
	return p.testSub2APIClient.Balance(ctx, userID)
}

func TestConsoleStateDoesNotReadOrReturnGatewayIdentity(t *testing.T) {
	sub2API := &stateBalanceProbe{testSub2APIClient: testSub2APIClient{balance: 123, charges: map[string]int64{}}}
	server, session := newGatewayOwnerTestServer(t, sub2API, nil)
	response := requestWithSession(t, server, session, http.MethodGet, "/api/state", "")
	if response.Code != http.StatusOK {
		t.Fatalf("state = %d: %s", response.Code, response.Body.String())
	}
	var state map[string]any
	if err := json.NewDecoder(response.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	if _, exists := state["user"]; exists {
		t.Fatalf("state returned user: %#v", state["user"])
	}
	if _, exists := state["balance"]; exists {
		t.Fatalf("state returned balance: %#v", state["balance"])
	}
	if sub2API.balanceCalls != 0 {
		t.Fatalf("state read Sub2API balance %d times", sub2API.balanceCalls)
	}
}
