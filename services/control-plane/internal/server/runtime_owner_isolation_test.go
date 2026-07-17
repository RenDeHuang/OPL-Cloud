package server

import (
	"context"
	"encoding/json"
	"errors"
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
	seedRuntimeAccessWorkspaceForTest(t, store, sessionUserIDForTest(t, server, owner), nil)

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
		name, path string
		mutation   bool
	}{
		{name: "runtime status", path: "/api/workspaces/runtime-status"},
		{name: "credential reveal", path: "/api/workspaces/ws-alpha/runtime-credentials/reveal"},
		{name: "credential rotate", path: "/api/workspaces/ws-alpha/runtime-credentials/rotate", mutation: true},
		{name: "gateway secret rotate", path: "/api/workspaces/ws-alpha/gateway-secret/rotate", mutation: true},
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
				if command.name == "runtime status" {
					body = `{"workspaceId":"ws-alpha"}`
				}
				var response *httptest.ResponseRecorder
				if command.mutation {
					response = requestWithMutationKeyForTest(t, server, owner, http.MethodPost, command.path, body, "blocked-command")
				} else {
					response = requestWithSession(t, server, owner, http.MethodPost, command.path, body)
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

type rotatingCredentialFabricClient struct {
	fakeFabricClient
	current        clients.WorkspaceRuntime
	runtimes       map[string]clients.WorkspaceRuntime
	gatewayKeys    []string
	runtimeKeys    []string
	runtimeApplies int
}

func (f *rotatingCredentialFabricClient) WriteGatewaySecret(_ context.Context, input clients.GatewaySecretWriteInput, key string) (clients.GatewaySecretWriteResult, error) {
	f.record("fabric.gateway-secret")
	f.gatewayKeys = append(f.gatewayKeys, key)
	return clients.GatewaySecretWriteResult{SecretRef: "opl-gateway-" + input.AccountID, Version: "v1", Fingerprint: "sha256:redacted"}, nil
}

func (f *rotatingCredentialFabricClient) CreateWorkspaceRuntime(_ context.Context, input clients.WorkspaceRuntimeInput, key string) (clients.WorkspaceRuntime, error) {
	f.record("fabric.runtime")
	f.runtimeKeys = append(f.runtimeKeys, key)
	runtime, ok := f.runtimes[key]
	if !ok {
		f.runtimeApplies++
		revision := stableID("runtime-credential", key)[:12]
		runtime = clients.WorkspaceRuntime{
			ID: "runtime-alpha", WorkspaceID: input.WorkspaceID, Status: "running", Ready: true,
			ServiceName: "opl-compute-alpha", Access: clients.WorkspaceRuntimeAccess{
				Username: "opl", Password: "runtime-password-" + revision,
				CredentialStatus: "configured", CredentialVersion: "v-" + revision, SecretRef: "opl-compute-alpha-env",
			},
		}
		f.runtimes[key] = runtime
	}
	f.current = runtime
	runtime.Access.Password = ""
	return runtime, nil
}

func (f *rotatingCredentialFabricClient) WorkspaceRuntimeStatus(_ context.Context, workspaceID string) (clients.WorkspaceRuntime, error) {
	f.record("fabric.runtime-status")
	runtime := f.current
	runtime.WorkspaceID = workspaceID
	return runtime, nil
}

type credentialRotationLedger struct {
	fakeLedgerClient
	receipts map[string]clients.Receipt
	inputs   []clients.ReceiptInput
	keys     []string
	failNext bool
}

func (l *credentialRotationLedger) RecordReceipt(_ context.Context, input clients.ReceiptInput, key string) (clients.Receipt, error) {
	if l.failNext {
		l.failNext = false
		return clients.Receipt{}, errors.New("ledger unavailable")
	}
	if receipt, ok := l.receipts[key]; ok {
		receipt.Replayed = true
		return receipt, nil
	}
	receipt := clients.Receipt{ReceiptInput: input, ReceiptID: "receipt-" + stableID(key)[:12]}
	l.receipts[key] = receipt
	l.inputs = append(l.inputs, input)
	l.keys = append(l.keys, key)
	return receipt, nil
}

func TestRuntimeCredentialRotateOwnerIdempotentAndNoLeak(t *testing.T) {
	store := newMemoryTableStore()
	calls := []string{}
	fabric := &rotatingCredentialFabricClient{
		fakeFabricClient: fakeFabricClient{calls: &calls},
		current: clients.WorkspaceRuntime{
			ID: "runtime-alpha", WorkspaceID: "ws-alpha", Status: "running", Ready: true,
			ServiceName: "opl-compute-alpha", Access: clients.WorkspaceRuntimeAccess{
				Username: "opl", Password: "runtime-password-before", CredentialStatus: "configured",
				CredentialVersion: "v-before", SecretRef: "opl-compute-alpha-env",
			},
		},
		runtimes: map[string]clients.WorkspaceRuntime{},
	}
	ledger := &credentialRotationLedger{receipts: map[string]clients.Receipt{}, failNext: true}
	sub2API := &testSub2APIClient{balance: 1_000_000_000_000, charges: map[string]int64{}}
	server, err := NewPersistentServer(controlplane.NewService(ledger, fabric, sub2API), store)
	if err != nil {
		t.Fatalf("create server: %v", err)
	}
	owner := tenantOwnerSessionForTest(t, server)
	member := tenantSessionForTest(t, server, "member")
	ownerID := sessionUserIDForTest(t, server, owner)
	seedRuntimeAccessWorkspaceForTest(t, store, ownerID, map[string]any{
		"state": "running", "status": "running", "url": "https://workspace.medopl.cn/w/ws-alpha/",
		"computeAllocationId": "compute-alpha", "currentComputeAllocationId": "compute-alpha",
		"storageId": "storage-alpha", "attachmentId": "attachment-alpha", "currentAttachmentId": "attachment-alpha",
		"runtimeId": "runtime-alpha", "runtime": map[string]any{"serviceName": "opl-compute-alpha", "status": "running", "ready": true},
		"access": map[string]any{"username": "opl", "credentialStatus": "configured", "credentialVersion": "v-before", "secretRef": "opl-compute-alpha-env"},
	})

	unauthorized := requestWithMutationKeyForTest(t, server, member, http.MethodPost, "/api/workspaces/ws-alpha/runtime-credentials/rotate", `{}`, "rotate-member")
	if unauthorized.Code != http.StatusForbidden || len(sub2API.workspaceKeyUserIDs) != 0 || len(calls) != 0 || len(ledger.inputs) != 0 {
		t.Fatalf("unauthorized rotate crossed trust boundary: status=%d sub2api=%#v fabric=%#v receipts=%#v", unauthorized.Code, sub2API.workspaceKeyUserIDs, calls, ledger.inputs)
	}

	rotate := func(key string) (map[string]any, string) {
		t.Helper()
		response := requestWithMutationKeyForTest(t, server, owner, http.MethodPost, "/api/workspaces/ws-alpha/runtime-credentials/rotate", `{}`, key)
		if response.Code != http.StatusOK {
			t.Fatalf("rotate %s = %d: %s", key, response.Code, response.Body.String())
		}
		if got := response.Header().Get("Cache-Control"); got != "private, no-store" {
			t.Fatalf("Cache-Control = %q, want private, no-store", got)
		}
		var body map[string]any
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatalf("decode rotate: %v", err)
		}
		password := stringValue(mapField(body, "access")["password"])
		if password == "" || password == "runtime-password-before" || strings.Contains(response.Body.String(), "workspace-key-secret") {
			t.Fatalf("rotate response = %#v", body)
		}
		return body, password
	}

	failed := requestWithMutationKeyForTest(t, server, owner, http.MethodPost, "/api/workspaces/ws-alpha/runtime-credentials/rotate", `{}`, "rotate-20260716-1")
	if failed.Code != http.StatusBadGateway || strings.Contains(failed.Body.String(), "password") || fabric.runtimeApplies != 1 || len(ledger.inputs) != 0 {
		t.Fatalf("receipt failure recovery point = %d body=%s applies=%d receipts=%d", failed.Code, failed.Body.String(), fabric.runtimeApplies, len(ledger.inputs))
	}
	first, firstPassword := rotate("rotate-20260716-1")
	replay, replayPassword := rotate("rotate-20260716-1")
	if replayPassword != firstPassword || replay["receiptId"] != first["receiptId"] || fabric.runtimeApplies != 1 || len(ledger.inputs) != 1 {
		t.Fatalf("same-key replay rotated twice: first=%#v replay=%#v applies=%d receipts=%d", first, replay, fabric.runtimeApplies, len(ledger.inputs))
	}
	second, secondPassword := rotate("rotate-20260716-2")
	if secondPassword == firstPassword || second["receiptId"] == first["receiptId"] || fabric.runtimeApplies != 2 || len(ledger.inputs) != 2 {
		t.Fatalf("new key did not rotate once: first=%#v second=%#v applies=%d receipts=%d", first, second, fabric.runtimeApplies, len(ledger.inputs))
	}

	firstOperationKey := "runtime-credential-rotate:ws-alpha:rotate-20260716-1"
	if fabric.gatewayKeys[0] != firstOperationKey+":gateway:gateway-secret" || fabric.runtimeKeys[0] != firstOperationKey+":runtime" || ledger.keys[0] != firstOperationKey {
		t.Fatalf("unstable child keys: gateway=%#v runtime=%#v ledger=%#v", fabric.gatewayKeys, fabric.runtimeKeys, ledger.keys)
	}
	for _, input := range ledger.inputs {
		raw := string(mustJSON(input))
		if input.Type != "workspace.access_token_reset" || strings.Contains(raw, "password") || strings.Contains(raw, firstPassword) || strings.Contains(raw, secondPassword) || strings.Contains(raw, "workspace-key-secret") {
			t.Fatalf("unsafe credential receipt: %s", raw)
		}
	}
	stored, err := store.ListWorkspaces(context.Background(), "acct-alpha")
	if err != nil || len(stored) != 1 || nested(stored[0], "access", "password") != nil || nested(stored[0], "access", "credentialVersion") == "v-before" {
		t.Fatalf("rotated credential persistence = %#v err=%v", stored, err)
	}
	operations, operationErr := store.ListRuntimeOperations(context.Background())
	if operationErr != nil || strings.Contains(string(mustJSON(operations)), firstPassword) || strings.Contains(string(mustJSON(operations)), secondPassword) {
		t.Fatalf("Control Plane operation leaked password: %#v err=%v", operations, operationErr)
	}
}
