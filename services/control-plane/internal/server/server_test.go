package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"opl-cloud/services/control-plane/internal/controlplane"
)

func TestCreateWorkspaceHTTPRequiresIdempotencyKey(t *testing.T) {
	server := NewServer(controlplane.NewService(nil, nil))
	body := bytes.NewBufferString(`{"accountId":"acct-alpha","ownerId":"usr-owner","name":"Alpha Lab","packageId":"basic"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/workspaces", body)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestOverviewHTTP(t *testing.T) {
	server := NewServer(controlplane.NewService(nil, nil))
	req := httptest.NewRequest(http.MethodGet, "/api/overview", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode overview: %v", err)
	}
	if body["service"] != "control-plane" {
		t.Fatalf("service = %v, want control-plane", body["service"])
	}
}

func TestActiveConsoleAPIRoutesReachControlPlane(t *testing.T) {
	server := NewServer(controlplane.NewService(nil, nil))
	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/auth/me", ""},
		{http.MethodGet, "/api/state", ""},
		{http.MethodGet, "/api/management/state", ""},
		{http.MethodGet, "/api/operator/summary", ""},
		{http.MethodGet, "/api/runtime/readiness", ""},
		{http.MethodGet, "/api/production/readiness", ""},
		{http.MethodGet, "/api/compute-pools", ""},
		{http.MethodGet, "/api/compute-allocations", ""},
		{http.MethodGet, "/api/compute-allocations/compute-alpha", ""},
		{http.MethodGet, "/api/support/tickets", ""},
		{http.MethodGet, "/api/ledger/task-receipts", ""},
		{http.MethodPost, "/api/auth/logout", `{}`},
		{http.MethodPost, "/api/organizations", `{"name":"Lab","billingAccountId":"acct-lab"}`},
		{http.MethodPost, "/api/organizations/members", `{"organizationId":"org-lab","userId":"usr-owner","role":"member"}`},
		{http.MethodPost, "/api/users", `{"email":"owner@example.com","accountId":"acct-lab","password":"secret"}`},
		{http.MethodPost, "/api/users/disable", `{"userId":"usr-owner"}`},
		{http.MethodPost, "/api/users/delete", `{"userId":"usr-owner"}`},
		{http.MethodPost, "/api/billing/topups", `{"accountId":"acct-lab","amount":100,"idempotencyKey":"topup-test"}`},
		{http.MethodPost, "/api/billing/resource-settlements", `{"accountId":"acct-lab","hours":1}`},
		{http.MethodPost, "/api/billing/reconciliation", `{"report":{"id":"recon-test","generatedAt":"2026-07-06T00:00:00Z"}}`},
		{http.MethodPost, "/api/compute-allocations", `{"packageId":"basic","name":"compute"}`},
		{http.MethodPost, "/api/compute-allocations/compute-alpha/destroy", `{"confirm":true}`},
		{http.MethodPost, "/api/storage-volumes", `{"name":"data","sizeGb":10}`},
		{http.MethodPost, "/api/storage-volumes/destroy", `{"storageId":"storage-alpha"}`},
		{http.MethodPost, "/api/storage-attachments", `{"computeAllocationId":"compute-alpha","storageId":"storage-alpha","mountPath":"/data"}`},
		{http.MethodPost, "/api/storage-attachments/detach", `{"attachmentId":"attach-alpha"}`},
		{http.MethodPost, "/api/workspaces/reset-token", `{"workspaceId":"ws-alpha"}`},
		{http.MethodPost, "/api/workspaces/delete-token", `{"workspaceId":"ws-alpha"}`},
		{http.MethodPost, "/api/workspaces/runtime-status", `{"workspaceId":"ws-alpha"}`},
		{http.MethodPost, "/api/operator/cleanup-workspace-access", `{"reason":"test"}`},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			var body *bytes.Buffer
			if tc.body != "" {
				body = bytes.NewBufferString(tc.body)
			} else {
				body = bytes.NewBuffer(nil)
			}
			req := httptest.NewRequest(tc.method, tc.path, body)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			server.ServeHTTP(rec, req)

			if rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
				t.Fatalf("status = %d for %s %s", rec.Code, tc.method, tc.path)
			}
			if rec.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("content-type = %q, want application/json", rec.Header().Get("Content-Type"))
			}
			var payload any
			if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
				t.Fatalf("decode response: %v", err)
			}
		})
	}
}
