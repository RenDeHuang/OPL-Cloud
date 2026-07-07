package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

func TestCreateWorkspaceHTTPUsesMutationKeyWhenHeaderIsAbsent(t *testing.T) {
	calls := []string{}
	ledger := &fakeLedgerClientWithKeys{fakeLedgerClient{}, []string{}}
	server := NewServer(controlplane.NewService(ledger, &fakeFabricClient{calls: &calls}))

	createResource(t, server, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-alpha","packageId":"basic"}`)
	createResource(t, server, http.MethodPost, "/api/storage-volumes", `{"accountId":"acct-alpha","sizeGb":10}`)
	createResource(t, server, http.MethodPost, "/api/storage-attachments", `{"workspaceId":"ws-alpha","computeAllocationId":"compute-from-fabric","storageId":"volume-from-fabric","mountPath":"/data"}`)

	body := bytes.NewBufferString(`{"accountId":"acct-alpha","ownerId":"usr-owner","workspaceName":"Alpha Lab","attachmentId":"attachment-from-fabric"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/workspaces", body)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	if len(ledger.keys) == 0 || ledger.keys[0] == "" {
		t.Fatalf("expected generated workspace idempotency key, got %#v", ledger.keys)
	}
}

func TestCreateWorkspaceHTTPUsesControlPlaneService(t *testing.T) {
	calls := []string{}
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{calls: &calls}))

	createResource(t, server, http.MethodPost, "/api/compute-allocations", `{"accountId":"acct-alpha","packageId":"basic"}`)
	createResource(t, server, http.MethodPost, "/api/storage-volumes", `{"accountId":"acct-alpha","sizeGb":10}`)
	createResource(t, server, http.MethodPost, "/api/storage-attachments", `{"workspaceId":"ws-alpha","computeAllocationId":"compute-from-fabric","storageId":"volume-from-fabric","mountPath":"/data"}`)

	body := bytes.NewBufferString(`{"accountId":"acct-alpha","ownerId":"usr-owner","workspaceName":"Alpha Lab","attachmentId":"attachment-from-fabric"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/workspaces", body)
	req.Header.Set("Idempotency-Key", "workspace-once")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	var workspace map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&workspace); err != nil {
		t.Fatalf("decode workspace: %v", err)
	}
	if workspace["accountId"] != "acct-alpha" || workspace["ownerId"] != "usr-owner" || workspace["url"] != "https://workspace.medopl.cn/w/ws-from-fabric/" {
		t.Fatalf("workspace did not come from service boundary: %#v", workspace)
	}
	if workspace["computeAllocationId"] != "compute-from-fabric" || workspace["storageId"] != "volume-from-fabric" || workspace["attachmentId"] != "attachment-from-fabric" || workspace["evidenceId"] != "evidence-from-ledger" {
		t.Fatalf("workspace missing ledger/fabric evidence: %#v", workspace)
	}
	if access, ok := workspace["access"].(map[string]any); !ok || access["tokenStatus"] != "active" {
		t.Fatalf("workspace response must include active URL access state: %#v", workspace)
	}
	if slices.Contains(calls[3:], "fabric.compute") || slices.Contains(calls[3:], "fabric.storage") {
		t.Fatalf("workspace create must not allocate replacement resources: %#v", calls)
	}
}

func TestWorkspaceRuntimeStatusPassesFabricChecks(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	req := httptest.NewRequest(http.MethodPost, "/api/workspaces/runtime-status", bytes.NewBufferString(`{"workspaceId":"ws-alpha"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["ready"] != false {
		t.Fatalf("ready must come from Fabric runtime state: %#v", body)
	}
	checks := body["checks"].([]any)
	if len(checks) != 2 || checks[0].(map[string]any)["name"] != "deployment_ready" || checks[1].(map[string]any)["name"] != "service_endpoints_ready" {
		t.Fatalf("runtime checks must pass through Fabric details: %#v", body["checks"])
	}
}

type fakeLedgerClient struct{}

type fakeLedgerClientWithKeys struct {
	fakeLedgerClient
	keys []string
}

func (f *fakeLedgerClientWithKeys) CreateHold(ctx context.Context, input clients.HoldInput, idempotencyKey string) (clients.HoldResult, error) {
	f.keys = append(f.keys, idempotencyKey)
	return f.fakeLedgerClient.CreateHold(ctx, input, idempotencyKey)
}

func (fakeLedgerClient) ManualTopUp(_ context.Context, input clients.ManualTopUpInput, _ string) (clients.ManualTopUpResult, error) {
	return clients.ManualTopUpResult{
		TopUp:             clients.ManualTopUp{ID: "topup-from-ledger", AccountID: input.AccountID, AmountCents: input.AmountCents, OperatorUserID: input.OperatorUserID},
		LedgerEntry:       clients.LedgerEntry{ID: "ledger-from-ledger", AccountID: input.AccountID, AmountCents: input.AmountCents},
		WalletTransaction: clients.WalletTransaction{ID: "wallet-tx-from-ledger", AccountID: input.AccountID, AmountCents: input.AmountCents},
		Wallet:            clients.Wallet{AccountID: input.AccountID, BalanceCents: input.AmountCents, AvailableCents: input.AmountCents, Currency: "CNY"},
	}, nil
}

func (fakeLedgerClient) CreateHold(_ context.Context, input clients.HoldInput, _ string) (clients.HoldResult, error) {
	return clients.HoldResult{ID: "hold-from-ledger", AccountID: input.AccountID, ResourceType: input.ResourceType, ResourceID: input.ResourceID, AmountCents: input.AmountCents, Wallet: clients.Wallet{AccountID: input.AccountID, BalanceCents: 20000, FrozenCents: input.AmountCents, AvailableCents: 20000 - input.AmountCents, Currency: "CNY"}}, nil
}

func (fakeLedgerClient) ReleaseHold(_ context.Context, input clients.HoldReleaseInput, _ string) (clients.HoldReleaseResult, error) {
	return clients.HoldReleaseResult{ID: "release-from-ledger", AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: input.ResourceType, ResourceID: input.ResourceID, HoldID: input.HoldID, AmountCents: input.AmountCents, Status: "released", Wallet: clients.Wallet{AccountID: input.AccountID, BalanceCents: 8800, FrozenCents: 0, AvailableCents: 8800, Currency: "CNY"}}, nil
}

func (fakeLedgerClient) RecordEvidence(_ context.Context, input clients.EvidenceInput, _ string) (clients.EvidenceReceipt, error) {
	return clients.EvidenceReceipt{ID: "evidence-from-ledger", WorkspaceID: input.WorkspaceID}, nil
}

func (fakeLedgerClient) SettleResource(_ context.Context, input clients.ResourceSettlementInput, _ string) (clients.ResourceSettlementResult, error) {
	return clients.ResourceSettlementResult{ID: "settlement-from-ledger", AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: input.ResourceType, ResourceID: input.ResourceID, AmountCents: input.AmountCents, Status: "settled", LedgerEntryID: "ledger-settlement-from-ledger", WalletTransactionID: "wallet-settlement-from-ledger", Wallet: clients.Wallet{AccountID: input.AccountID, BalanceCents: 8800, AvailableCents: 8800, Currency: "CNY"}}, nil
}

type fakeLedgerClientWithoutSettlementIdentity struct {
	fakeLedgerClient
}

func (fakeLedgerClientWithoutSettlementIdentity) SettleResource(_ context.Context, _ clients.ResourceSettlementInput, _ string) (clients.ResourceSettlementResult, error) {
	return clients.ResourceSettlementResult{ID: "settlement-from-ledger", Status: "settled", LedgerEntryID: "ledger-settlement-from-ledger", WalletTransactionID: "wallet-settlement-from-ledger"}, nil
}

func (fakeLedgerClient) RecordReconciliation(_ context.Context, input clients.ReconciliationInput, _ string) (clients.ReconciliationResult, error) {
	return clients.ReconciliationResult{ID: stringField(input.Report, "id", "reconciliation-from-ledger"), Status: "ok", Report: input.Report, BlockNewWorkspaces: false, Reason: "operator_reconciliation"}, nil
}

type fakeFabricClient struct {
	calls *[]string
}

func (f *fakeFabricClient) record(call string) {
	if f != nil && f.calls != nil {
		*f.calls = append(*f.calls, call)
	}
}

func (f *fakeFabricClient) CreateComputeAllocation(_ context.Context, input clients.ComputeAllocationInput, _ string) (clients.ComputeAllocation, error) {
	f.record("fabric.compute")
	return clients.ComputeAllocation{ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, PackageID: input.PackageID, Status: "running", Provider: "tencent-tke", ProviderResourceID: "node/node-from-fabric", ProviderRequestID: "compute-request-from-fabric", InstanceID: "ins-from-fabric", NodeName: "node-from-fabric", BillingStatus: "active", ServiceName: "opl-compute-from-fabric"}, nil
}

func (f *fakeFabricClient) GetComputeAllocation(_ context.Context, id string) (clients.ComputeAllocation, error) {
	f.record("fabric.compute-get")
	return clients.ComputeAllocation{ID: id, Status: "running", Provider: "tencent-tke", ProviderResourceID: "node/node-from-fabric", ProviderRequestID: "compute-request-from-fabric", InstanceID: "ins-from-fabric", NodeName: "node-from-fabric", BillingStatus: "active", ServiceName: "opl-compute-from-fabric"}, nil
}

func (f *fakeFabricClient) DestroyComputeAllocation(_ context.Context, id string, _ string) (clients.ComputeAllocation, error) {
	f.record("fabric.compute-destroy")
	return clients.ComputeAllocation{ID: id, Status: "destroyed", Provider: "tencent-tke", ProviderRequestID: "compute-destroy-from-fabric", BillingStatus: "stopped"}, nil
}

func (f *fakeFabricClient) CreateStorageVolume(_ context.Context, input clients.StorageVolumeInput, _ string) (clients.StorageVolume, error) {
	f.record("fabric.storage")
	return clients.StorageVolume{ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "available", Provider: "tencent-tke", ProviderResourceID: "pvc/volume-from-fabric-data", ProviderRequestID: "storage-request-from-fabric", SizeGB: input.SizeGB, StorageClass: "cbs", BillingStatus: "active"}, nil
}

func (f *fakeFabricClient) DestroyStorageVolume(_ context.Context, id string, _ string) (clients.StorageVolume, error) {
	f.record("fabric.storage-destroy")
	return clients.StorageVolume{ID: id, Status: "destroyed", Provider: "tencent-tke", ProviderRequestID: "storage-destroy-from-fabric", BillingStatus: "stopped"}, nil
}

func (f *fakeFabricClient) CreateStorageAttachment(_ context.Context, input clients.StorageAttachmentInput, _ string) (clients.StorageAttachment, error) {
	f.record("fabric.attachment")
	return clients.StorageAttachment{ID: "attachment-from-fabric", WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID, Status: "attached", Provider: "tencent-tke", ProviderAttachmentID: "deployment/opl-compute-from-fabric:pvc/volume-from-fabric-data:/data", ProviderRequestID: "attachment-request-from-fabric", MountPath: "/data"}, nil
}

func (f *fakeFabricClient) DetachStorageAttachment(_ context.Context, id string, _ string) (clients.StorageAttachment, error) {
	f.record("fabric.attachment-detach")
	return clients.StorageAttachment{ID: id, Status: "detached", ProviderRequestID: "attachment-detach-from-fabric"}, nil
}

func (f *fakeFabricClient) CreateWorkspaceRuntime(_ context.Context, input clients.WorkspaceRuntimeInput, _ string) (clients.WorkspaceRuntime, error) {
	f.record("fabric.runtime")
	return clients.WorkspaceRuntime{ID: "runtime-from-fabric", WorkspaceID: input.WorkspaceID, URL: "https://workspace.medopl.cn/w/ws-from-fabric/", ServiceName: "opl-compute-from-fabric"}, nil
}

func (f *fakeFabricClient) WorkspaceRuntimeStatus(_ context.Context, workspaceID string) (clients.WorkspaceRuntime, error) {
	f.record("fabric.runtime-status")
	return clients.WorkspaceRuntime{
		ID:          "runtime-from-fabric",
		WorkspaceID: workspaceID,
		URL:         "https://workspace.medopl.cn/w/" + workspaceID + "/",
		Status:      "unready",
		ServiceName: "opl-compute-from-fabric",
		Ready:       false,
		Checks: []any{
			map[string]any{"name": "deployment_ready", "ok": true},
			map[string]any{"name": "service_endpoints_ready", "ok": false},
		},
	}, nil
}

func (f *fakeFabricClient) Readiness(_ context.Context) (map[string]any, error) {
	f.record("fabric.readiness")
	return map[string]any{"provider": "tencent-tke", "ready": true, "missingEnv": []string{}, "missingTools": []string{}}, nil
}

func createResource(t *testing.T, server http.Handler, method string, path string, body string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "test-"+path)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code < 200 || rec.Code >= 300 {
		t.Fatalf("%s %s status = %d: %s", method, path, rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode %s %s: %v", method, path, err)
	}
	return payload
}

func TestCreateComputeAllocationUsesFabricService(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	req := httptest.NewRequest(http.MethodPost, "/api/compute-allocations", bytes.NewBufferString(`{"accountId":"acct-alpha","packageId":"basic","name":"Production Compute"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "compute-once")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if stringValue(body["id"]) == "" || body["providerRequestId"] != "compute-request-from-fabric" || body["holdId"] != "hold-from-ledger" {
		t.Fatalf("compute allocation did not come from Fabric: %#v", body)
	}
	if body["provider"] != "tencent-tke" || body["billingStatus"] != "active" || body["nodeName"] != "node-from-fabric" || body["instanceId"] != "ins-from-fabric" {
		t.Fatalf("compute allocation missing route contract fields: %#v", body)
	}
	getReq := httptest.NewRequest(http.MethodGet, "/api/compute-allocations/"+stringValue(body["id"]), nil)
	getRec := httptest.NewRecorder()
	server.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want %d: %s", getRec.Code, http.StatusOK, getRec.Body.String())
	}
}

func TestManagementStateIncludesResourceLedgerEvidenceChain(t *testing.T) {
	app := newRuntimeApp()
	app.mu.Lock()
	app.workspaces["ws-alpha"] = map[string]any{
		"id":                         "ws-alpha",
		"ownerAccountId":             "acct-alpha",
		"ownerUserId":                "usr-alpha",
		"currentComputeAllocationId": "compute-alpha",
		"currentAttachmentId":        "attach-alpha",
		"storageId":                  "storage-alpha",
	}
	app.computes["compute-alpha"] = map[string]any{
		"id":             "compute-alpha",
		"ownerAccountId": "acct-alpha",
		"ownerUserId":    "usr-alpha",
		"cvmInstanceId":  "ins-alpha",
		"nodeName":       "node-alpha",
	}
	app.storages["storage-alpha"] = map[string]any{
		"id":             "storage-alpha",
		"ownerAccountId": "acct-alpha",
	}
	ledger := app.addLedgerLocked("acct-alpha", "compute_debit", map[string]any{"workspaceId": "ws-alpha", "computeAllocationId": "compute-alpha"})
	app.addWalletTxLocked("acct-alpha", "compute_debit", map[string]any{"workspaceId": "ws-alpha", "computeAllocationId": "compute-alpha"})
	wallet := app.walletTx[len(app.walletTx)-1]
	app.mu.Unlock()

	state := app.managementState()
	rows := state["resourceLedgerEvidence"].([]any)
	if len(rows) != 1 {
		t.Fatalf("resourceLedgerEvidence rows = %d, want 1: %#v", len(rows), rows)
	}
	row := rows[0].(map[string]any)
	if row["ownerAccountId"] != "acct-alpha" || row["ownerUserId"] != "usr-alpha" || row["cvmInstanceId"] != "ins-alpha" || row["nodeName"] != "node-alpha" || row["storageId"] != "storage-alpha" {
		t.Fatalf("unexpected ownership evidence: %#v", row)
	}
	if !slices.Contains(row["workspaceIds"].([]string), "ws-alpha") {
		t.Fatalf("workspaceIds missing ws-alpha: %#v", row["workspaceIds"])
	}
	if !slices.Contains(row["ledgerEntryIds"].([]string), ledger["id"].(string)) {
		t.Fatalf("ledgerEntryIds missing ledger id: %#v", row["ledgerEntryIds"])
	}
	if !slices.Contains(row["walletTransactionIds"].([]string), wallet["id"].(string)) {
		t.Fatalf("walletTransactionIds missing wallet id: %#v", row["walletTransactionIds"])
	}
}

func TestResourceSettlementProjectsLedgerEvidenceIntoConsoleState(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))

	createResource(t, server, http.MethodPost, "/api/billing/resource-settlements", `{"accountId":"acct-alpha","workspaceId":"ws-alpha","resourceType":"compute","resourceId":"compute-alpha","amountCents":123}`)
	createResource(t, server, http.MethodPost, "/api/billing/resource-settlements", `{"accountId":"acct-alpha","workspaceId":"ws-alpha","resourceType":"storage","resourceId":"storage-alpha","amountCents":45}`)
	req := httptest.NewRequest(http.MethodGet, "/api/state?accountId=acct-alpha", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("state status = %d: %s", rec.Code, rec.Body.String())
	}
	var state map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&state); err != nil {
		t.Fatalf("decode state: %v", err)
	}

	ledger := state["billingLedger"].([]any)
	if !slices.ContainsFunc(ledger, func(row any) bool {
		entry := row.(map[string]any)
		return entry["type"] == "compute_debit" && entry["computeAllocationId"] == "compute-alpha" && entry["workspaceId"] == "ws-alpha"
	}) {
		t.Fatalf("missing compute debit ledger projection: %#v", ledger)
	}
	if !slices.ContainsFunc(ledger, func(row any) bool {
		entry := row.(map[string]any)
		return entry["type"] == "storage_debit" && entry["storageId"] == "storage-alpha" && entry["workspaceId"] == "ws-alpha"
	}) {
		t.Fatalf("missing storage debit ledger projection: %#v", ledger)
	}
	usage := state["resourceUsageLogs"].([]any)
	if len(usage) != 2 {
		t.Fatalf("usage rows = %d, want 2: %#v", len(usage), usage)
	}
	walletTx := state["walletTransactions"].([]any)
	if len(walletTx) != 2 {
		t.Fatalf("wallet transaction rows = %d, want 2: %#v", len(walletTx), walletTx)
	}
}

func TestResourceSettlementProjectionKeepsRequestIdentityWhenLedgerOmitsIt(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClientWithoutSettlementIdentity{}, &fakeFabricClient{}))

	createResource(t, server, http.MethodPost, "/api/billing/resource-settlements", `{"accountId":"acct-alpha","workspaceId":"ws-alpha","resourceType":"compute","resourceId":"compute-alpha","amountCents":123}`)
	req := httptest.NewRequest(http.MethodGet, "/api/state?accountId=acct-alpha", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("state status = %d: %s", rec.Code, rec.Body.String())
	}
	var state map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&state); err != nil {
		t.Fatalf("decode state: %v", err)
	}

	ledger := state["billingLedger"].([]any)
	if !slices.ContainsFunc(ledger, func(row any) bool {
		entry := row.(map[string]any)
		return entry["accountId"] == "acct-alpha" && entry["type"] == "compute_debit" && entry["computeAllocationId"] == "compute-alpha" && entry["workspaceId"] == "ws-alpha"
	}) {
		t.Fatalf("missing settlement request identity in ledger projection: %#v", ledger)
	}
	wallet := state["wallet"].(map[string]any)
	if wallet["accountId"] != "acct-alpha" {
		t.Fatalf("wallet lost settlement request account: %#v", wallet)
	}
}

func TestWorkspaceGatewayRoutesRootRuntimeApiByReferer(t *testing.T) {
	t.Setenv("OPL_WORKSPACE_DOMAIN", "workspace.medopl.cn")
	var gotPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		writeJSON(w, http.StatusOK, map[string]string{"proxied": r.URL.Path})
	}))
	defer backend.Close()
	app := newRuntimeApp()
	app.workspaces["ws-alpha"] = map[string]any{
		"runtime": map[string]any{"serviceName": strings.TrimPrefix(backend.URL, "http://")},
	}
	req := httptest.NewRequest(http.MethodPost, "https://workspace.medopl.cn/login", bytes.NewBufferString(`{"username":"admin"}`))
	req.Header.Set("Referer", "https://workspace.medopl.cn/w/ws-alpha/")
	rec := httptest.NewRecorder()

	app.proxyWorkspaceRoot(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if gotPath != "/login" {
		t.Fatalf("proxied path = %q, want /login", gotPath)
	}
}

func TestWorkspaceGatewaySetsActiveCookieForRootRuntimeApi(t *testing.T) {
	t.Setenv("OPL_WORKSPACE_DOMAIN", "workspace.medopl.cn")
	var gotPaths []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		writeJSON(w, http.StatusOK, map[string]string{"proxied": r.URL.Path})
	}))
	defer backend.Close()
	app := newRuntimeApp()
	app.workspaces["ws-alpha"] = map[string]any{
		"runtime": map[string]any{"serviceName": strings.TrimPrefix(backend.URL, "http://")},
	}
	entryReq := httptest.NewRequest(http.MethodGet, "https://workspace.medopl.cn/w/ws-alpha/?token=share_alpha", nil)
	entryRec := httptest.NewRecorder()

	app.proxyWorkspace(entryRec, entryReq)

	if entryRec.Code != http.StatusOK {
		t.Fatalf("entry status = %d, want %d: %s", entryRec.Code, http.StatusOK, entryRec.Body.String())
	}
	cookies := entryRec.Result().Cookies()
	if !slices.ContainsFunc(cookies, func(cookie *http.Cookie) bool {
		return cookie.Name == "opl_ws_active" && cookie.Value == "ws-alpha"
	}) {
		t.Fatalf("entry response must set active workspace cookie, got %#v", cookies)
	}
	apiReq := httptest.NewRequest(http.MethodGet, "https://workspace.medopl.cn/api/auth/user", nil)
	for _, cookie := range cookies {
		apiReq.AddCookie(cookie)
	}
	apiRec := httptest.NewRecorder()

	app.proxyWorkspaceRoot(apiRec, apiReq)

	if apiRec.Code != http.StatusOK {
		t.Fatalf("api status = %d, want %d: %s", apiRec.Code, http.StatusOK, apiRec.Body.String())
	}
	if !slices.Equal(gotPaths, []string{"/", "/api/auth/user"}) {
		t.Fatalf("proxied paths = %#v, want entry and root API paths", gotPaths)
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

func TestOperatorLoginUsesConfiguredToken(t *testing.T) {
	t.Setenv("OPL_OPERATOR_SUMMARY_TOKEN", "operator-secret")
	server := NewServer(controlplane.NewService(nil, nil))
	req := httptest.NewRequest(http.MethodPost, "/api/auth/operator-login", bytes.NewBufferString(`{"operatorToken":"operator-secret"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Header().Get("x-opl-csrf-token") == "" {
		t.Fatalf("expected csrf response header")
	}
	if rec.Header().Get("Set-Cookie") == "" {
		t.Fatalf("expected session cookie")
	}
}

func TestOperatorLoginRejectsInvalidToken(t *testing.T) {
	t.Setenv("OPL_OPERATOR_SUMMARY_TOKEN", "operator-secret")
	server := NewServer(controlplane.NewService(nil, nil))
	req := httptest.NewRequest(http.MethodPost, "/api/auth/operator-login", bytes.NewBufferString(`{"operatorToken":"wrong"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestActiveConsoleAPIRoutesReachControlPlane(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}))
	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/auth/me", ""},
		{http.MethodGet, "/api/healthz", ""},
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
			req.Header.Set("Idempotency-Key", "route-contract-test")
			rec := httptest.NewRecorder()

			server.ServeHTTP(rec, req)

			if rec.Code == http.StatusMethodNotAllowed {
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
