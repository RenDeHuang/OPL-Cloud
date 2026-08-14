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

func seedRuntimeAccessWorkspaceForTest(t *testing.T, store controlPlaneTableStore, ownerID string, overrides map[string]any) {
	t.Helper()
	mustStore(t, store.SaveCompute(context.Background(), map[string]any{
		"id": "compute-alpha", "accountId": "acct-alpha", "ownerUserId": ownerID, "workspaceId": "ws-alpha",
		"status": "running", "billingStatus": "active", "paidThrough": "2099-01-01T00:00:00Z",
	}))
	mustStore(t, store.SaveStorage(context.Background(), map[string]any{
		"id": "storage-alpha", "accountId": "acct-alpha", "ownerUserId": ownerID, "workspaceId": "ws-alpha",
		"status": "available", "billingStatus": "active", "paidThrough": "2099-01-01T00:00:00Z",
	}))
	mustStore(t, store.SaveAttachment(context.Background(), map[string]any{
		"id": "attachment-alpha", "accountId": "acct-alpha", "workspaceId": "ws-alpha",
		"computeAllocationId": "compute-alpha", "storageId": "storage-alpha", "status": "attached",
	}))
	row := workspaceGatewayTestRow(map[string]any{
		"id": "ws-alpha", "accountId": "acct-alpha", "ownerAccountId": "acct-alpha", "ownerUserId": ownerID,
		"state": "running", "status": "running", "computeAllocationId": "compute-alpha", "currentComputeAllocationId": "compute-alpha",
		"storageId": "storage-alpha", "attachmentId": "attachment-alpha", "currentAttachmentId": "attachment-alpha",
		"runtimeId": "runtime-alpha", "runtime": map[string]any{"serviceName": "opl-compute-alpha", "status": "running", "ready": true},
	})
	for key, value := range overrides {
		row[key] = value
	}
	mustStore(t, store.SaveWorkspace(context.Background(), row))
}

func TestRuntimeStatusNeverReturnsCredential(t *testing.T) {
	store := newMemoryTableStore()
	fabric := &fakeFabricClient{runtimeStatus: clients.WorkspaceRuntime{
		ID: "runtime-alpha", WorkspaceID: "ws-alpha", URL: "https://workspace.medopl.cn/w/ws-alpha/", ServiceName: "opl-compute-alpha", Status: "running", Ready: true,
		Checks: []any{map[string]any{"name": "service_endpoints_ready", "ok": true}},
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
	seedRuntimeAccessWorkspaceForTest(t, store, sessionUserIDForTest(t, server, owner), nil)

	response := requestWithSession(t, server, owner, http.MethodGet, "/api/workspaces/ws-alpha/runtime-status", "")
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
	ownerID := sessionUserIDForTest(t, server, owner)
	seedRuntimeAccessWorkspaceForTest(t, store, ownerID, nil)
	mustStore(t, store.SaveWorkspace(context.Background(), map[string]any{
		"id": "ws-beta", "accountId": "acct-beta", "ownerAccountId": "acct-beta",
		"ownerUserId": "usr-beta", "state": "running", "status": "running",
	}))

	for _, test := range []struct {
		name      string
		login     *httptest.ResponseRecorder
		workspace string
	}{
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
	if len(calls) != 1 || calls[0] != "fabric.runtime-credentials" {
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

func TestWorkspaceRuntimeAndSecretCommandsRequireCanonicalAccess(t *testing.T) {
	states := []struct {
		name   string
		mutate func(map[string]any, map[string]any, map[string]any, map[string]any)
	}{
		{name: "missing billing", mutate: func(workspace, _, _, _ map[string]any) {
			for _, key := range workspaceBillingStateRequiredKeys {
				delete(workspace, key)
			}
		}},
		{name: "manual review", mutate: func(workspace, _, _, _ map[string]any) {
			for _, key := range workspaceBillingStateExclusiveKeys {
				delete(workspace, key)
			}
			workspace["autoRenew"], workspace["renewalStatus"], workspace["manualReviewReason"] = false, "manual_review", workspaceBillingLegacyMismatch
		}},
		{name: "expired", mutate: func(workspace, _, _, _ map[string]any) {
			workspace["periodStart"], workspace["paidThrough"], workspace["nextRenewalAt"] = "2000-01-01T00:00:00Z", "2000-02-01T00:00:00Z", "2000-01-31T00:00:00Z"
		}},
		{name: "attachment not ready", mutate: func(_, _, _, attachment map[string]any) {
			attachment["status"] = "detached"
		}},
	}
	commands := []struct {
		name, method, path string
		mutation           bool
	}{
		{name: "runtime status", method: http.MethodGet, path: "/api/workspaces/ws-alpha/runtime-status"},
		{name: "credential reveal", method: http.MethodPost, path: "/api/workspaces/ws-alpha/runtime-credentials/reveal"},
		{name: "credential rotate", method: http.MethodPost, path: "/api/workspaces/ws-alpha/runtime-credentials/rotate", mutation: true},
	}

	for _, state := range states {
		for _, command := range commands {
			t.Run(state.name+"/"+command.name, func(t *testing.T) {
				store := newMemoryTableStore()
				calls := []string{}
				fabric := &fakeFabricClient{calls: &calls, runtimeStatus: clients.WorkspaceRuntime{
					ID: "runtime-alpha", WorkspaceID: "ws-alpha", Status: "running", Ready: true,
					Access: clients.WorkspaceRuntimeAccess{Username: "opl", Password: "must-not-reveal"},
				}}
				sub2API := &testSub2APIClient{balance: 1_000_000_000_000, charges: map[string]int64{}}
				server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, fabric, sub2API), store)
				if err != nil {
					t.Fatal(err)
				}
				owner := tenantOwnerSessionForTest(t, server)
				ownerID := sessionUserIDForTest(t, server, owner)
				compute := map[string]any{
					"id": "compute-alpha", "accountId": "acct-alpha", "workspaceId": "ws-alpha",
					"status": "running", "billingStatus": "active", "paidThrough": "2099-01-01T00:00:00Z",
				}
				storage := map[string]any{
					"id": "storage-alpha", "accountId": "acct-alpha", "workspaceId": "ws-alpha",
					"status": "available", "billingStatus": "active", "paidThrough": "2099-01-01T00:00:00Z",
				}
				attachment := map[string]any{
					"id": "attachment-alpha", "accountId": "acct-alpha", "workspaceId": "ws-alpha",
					"computeAllocationId": "compute-alpha", "storageId": "storage-alpha", "status": "attached",
				}
				workspace := workspaceGatewayTestRow(map[string]any{
					"id": "ws-alpha", "accountId": "acct-alpha", "ownerAccountId": "acct-alpha", "ownerUserId": ownerID,
					"state": "running", "status": "running", "computeAllocationId": "compute-alpha", "currentComputeAllocationId": "compute-alpha",
					"storageId": "storage-alpha", "attachmentId": "attachment-alpha", "currentAttachmentId": "attachment-alpha",
					"runtimeId": "runtime-alpha", "runtime": map[string]any{"serviceName": "opl-compute-alpha", "status": "running", "ready": true},
				})
				state.mutate(workspace, compute, storage, attachment)
				mustStore(t, store.SaveCompute(context.Background(), compute))
				mustStore(t, store.SaveStorage(context.Background(), storage))
				mustStore(t, store.SaveAttachment(context.Background(), attachment))
				mustStore(t, store.SaveWorkspace(context.Background(), workspace))
				beforeWorkspaces, _ := store.ListWorkspaces(context.Background(), "acct-alpha")
				beforeOperations, _ := store.ListRuntimeOperations(context.Background())

				body := `{}`
				var response *httptest.ResponseRecorder
				if command.mutation {
					response = requestWithMutationKeyForTest(t, server, owner, command.method, command.path, body, "blocked-command")
				} else {
					response = requestWithSession(t, server, owner, command.method, command.path, body)
				}
				if response.Code >= 200 && response.Code < 300 {
					t.Fatalf("blocked command status=%d body=%s", response.Code, response.Body.String())
				}
				afterWorkspaces, _ := store.ListWorkspaces(context.Background(), "acct-alpha")
				afterOperations, _ := store.ListRuntimeOperations(context.Background())
				if len(calls) != 0 || len(sub2API.workspaceKeyUserIDs) != 0 || string(mustJSON(afterWorkspaces)) != string(mustJSON(beforeWorkspaces)) || string(mustJSON(afterOperations)) != string(mustJSON(beforeOperations)) {
					t.Fatalf("blocked command crossed boundary: status=%d calls=%#v sub2api=%#v before=%#v after=%#v operations=%#v", response.Code, calls, sub2API.workspaceKeyUserIDs, beforeWorkspaces, afterWorkspaces, afterOperations)
				}
			})
		}
	}
}
