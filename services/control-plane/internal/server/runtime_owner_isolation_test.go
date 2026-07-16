package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"opl-cloud/services/control-plane/internal/clients"
)

func TestRuntimeStatusNeverReturnsCredential(t *testing.T) {
	store := newMemoryTableStore()
	fabric := &fakeFabricClient{runtimeStatus: clients.WorkspaceRuntime{
		ID: "runtime-alpha", WorkspaceID: "ws-alpha", Status: "running", Ready: true,
		Access: clients.WorkspaceRuntimeAccess{
			Username: "opl", Password: "runtime-password-alpha", CredentialStatus: "configured",
			CredentialVersion: "v1", SecretRef: "runtime-secret-alpha",
		},
	}}
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, fabric), store)
	if err != nil {
		t.Fatalf("create server: %v", err)
	}
	owner := tenantOwnerSessionForTest(t, server)
	member := tenantSessionForTest(t, server, "member")
	mustStore(t, store.SaveWorkspace(context.Background(), map[string]any{
		"id": "ws-alpha", "accountId": "acct-alpha", "ownerAccountId": "acct-alpha",
		"ownerUserId": sessionUserIDForTest(t, server, owner), "state": "running", "status": "running",
	}))

	response := requestWithSession(t, server, member, http.MethodPost, "/api/workspaces/runtime-status", `{"workspaceId":"ws-alpha"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("runtime status = %d: %s", response.Code, response.Body.String())
	}
	for _, secret := range []string{"runtime-password-alpha", `"password"`, `"secretRef"`} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("runtime status leaked %q: %s", secret, response.Body.String())
		}
	}
	if got := response.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control = %q, want private, no-store", got)
	}
	stored, err := store.ListWorkspaces(context.Background(), "acct-alpha")
	if err != nil || len(stored) != 1 || nested(stored[0], "access", "password") != nil {
		t.Fatalf("stored Workspace leaked password: rows=%#v err=%v", stored, err)
	}
}

func TestRuntimeCredentialRevealOwnerOnly(t *testing.T) {
	store := newMemoryTableStore()
	calls := []string{}
	fabric := &fakeFabricClient{calls: &calls, runtimeStatus: clients.WorkspaceRuntime{
		ID: "runtime-alpha", WorkspaceID: "ws-alpha", Status: "running", Ready: true,
		Access: clients.WorkspaceRuntimeAccess{
			Username: "opl", Password: "runtime-password-alpha", CredentialStatus: "configured",
			CredentialVersion: "v1", SecretRef: "runtime-secret-alpha",
		},
	}}
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, fabric), store)
	if err != nil {
		t.Fatalf("create server: %v", err)
	}
	owner := tenantOwnerSessionForTest(t, server)
	member := tenantSessionForTest(t, server, "member")
	ownerID := sessionUserIDForTest(t, server, owner)
	mustStore(t, store.SaveWorkspace(context.Background(), map[string]any{
		"id": "ws-alpha", "accountId": "acct-alpha", "ownerAccountId": "acct-alpha",
		"ownerUserId": ownerID, "state": "running", "status": "running",
	}))
	mustStore(t, store.SaveWorkspace(context.Background(), map[string]any{
		"id": "ws-beta", "accountId": "acct-beta", "ownerAccountId": "acct-beta",
		"ownerUserId": "usr-beta", "state": "running", "status": "running",
	}))

	for _, test := range []struct {
		name      string
		login     *httptest.ResponseRecorder
		workspace string
	}{
		{name: "member", login: member, workspace: "ws-alpha"},
		{name: "cross-account", login: owner, workspace: "ws-beta"},
		{name: "unknown", login: owner, workspace: "ws-unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := len(calls)
			response := requestWithSession(t, server, test.login, http.MethodPost, "/api/workspaces/"+test.workspace+"/runtime-credentials/reveal", `{}`)
			if response.Code != http.StatusForbidden {
				t.Fatalf("reveal status = %d, want 403: %s", response.Code, response.Body.String())
			}
			if len(calls) != before {
				t.Fatalf("unauthorized reveal reached Fabric: %#v", calls[before:])
			}
		})
	}

	fabric.runtimeStatus.Ready = false
	unavailable := requestWithSession(t, server, owner, http.MethodPost, "/api/workspaces/ws-alpha/runtime-credentials/reveal", `{}`)
	if unavailable.Code != http.StatusConflict || strings.Contains(unavailable.Body.String(), "runtime-password-alpha") {
		t.Fatalf("unready credential reveal = %d: %s", unavailable.Code, unavailable.Body.String())
	}
	fabric.runtimeStatus.Ready = true
	calls = calls[:0]

	response := requestWithSession(t, server, owner, http.MethodPost, "/api/workspaces/ws-alpha/runtime-credentials/reveal", `{}`)
	if response.Code != http.StatusOK {
		t.Fatalf("owner reveal status = %d: %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control = %q, want private, no-store", got)
	}
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode reveal: %v", err)
	}
	if body["workspaceId"] != "ws-alpha" || nested(body, "access", "password") != "runtime-password-alpha" || nested(body, "access", "secretRef") != nil {
		t.Fatalf("owner reveal response = %#v", body)
	}
	if len(calls) != 1 || calls[0] != "fabric.runtime-status" {
		t.Fatalf("owner reveal calls = %#v", calls)
	}

	for _, path := range []string{"/api/state", "/api/workspaces"} {
		listed := requestWithSession(t, server, owner, http.MethodGet, path, "")
		if strings.Contains(listed.Body.String(), "runtime-password-alpha") {
			t.Fatalf("%s leaked revealed password: %s", path, listed.Body.String())
		}
	}
	stored, err := store.ListWorkspaces(context.Background(), "acct-alpha")
	if err != nil || len(stored) != 1 || nested(stored[0], "access", "password") != nil {
		t.Fatalf("reveal persisted password: rows=%#v err=%v", stored, err)
	}
	operations, operationErr := store.ListRuntimeOperations(context.Background())
	audits, auditErr := store.ListAuditEvents(context.Background(), "acct-alpha")
	if operationErr != nil || auditErr != nil || strings.Contains(string(mustJSON(operations)), "runtime-password-alpha") || strings.Contains(string(mustJSON(audits)), "runtime-password-alpha") {
		t.Fatalf("reveal leaked into operations/audit: operations=%#v audits=%#v errors=%v/%v", operations, audits, operationErr, auditErr)
	}
}
