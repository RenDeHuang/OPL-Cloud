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

func TestCreateWorkspaceHTTPUsesControlPlaneService(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, fakeFabricClient{}))
	body := bytes.NewBufferString(`{"accountId":"acct-alpha","ownerId":"usr-owner","name":"Alpha Lab","packageId":"basic"}`)
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
	if workspace["holdId"] != "hold-from-ledger" || workspace["computeId"] != "compute-from-fabric" || workspace["evidenceId"] != "evidence-from-ledger" {
		t.Fatalf("workspace missing ledger/fabric evidence: %#v", workspace)
	}
}

type fakeLedgerClient struct{}

func (fakeLedgerClient) ManualTopUp(_ context.Context, input clients.ManualTopUpInput, _ string) (clients.ManualTopUpResult, error) {
	return clients.ManualTopUpResult{
		TopUp:             clients.ManualTopUp{ID: "topup-from-ledger", AccountID: input.AccountID, AmountCents: input.AmountCents, OperatorUserID: input.OperatorUserID},
		LedgerEntry:       clients.LedgerEntry{ID: "ledger-from-ledger", AccountID: input.AccountID, AmountCents: input.AmountCents},
		WalletTransaction: clients.WalletTransaction{ID: "wallet-tx-from-ledger", AccountID: input.AccountID, AmountCents: input.AmountCents},
		Wallet:            clients.Wallet{AccountID: input.AccountID, BalanceCents: input.AmountCents, AvailableCents: input.AmountCents, Currency: "CNY"},
	}, nil
}

func (fakeLedgerClient) CreateHold(_ context.Context, input clients.HoldInput, _ string) (clients.HoldResult, error) {
	return clients.HoldResult{ID: "hold-from-ledger", AccountID: input.AccountID, AmountCents: input.AmountCents}, nil
}

func (fakeLedgerClient) RecordEvidence(_ context.Context, input clients.EvidenceInput, _ string) (clients.EvidenceReceipt, error) {
	return clients.EvidenceReceipt{ID: "evidence-from-ledger", WorkspaceID: input.WorkspaceID}, nil
}

func (fakeLedgerClient) SettleResource(_ context.Context, input clients.ResourceSettlementInput, _ string) (clients.ResourceSettlementResult, error) {
	return clients.ResourceSettlementResult{ID: "settlement-from-ledger", AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: input.ResourceType, ResourceID: input.ResourceID, AmountCents: input.AmountCents, Status: "settled", LedgerEntryID: "ledger-settlement-from-ledger", WalletTransactionID: "wallet-settlement-from-ledger", Wallet: clients.Wallet{AccountID: input.AccountID, BalanceCents: 8800, AvailableCents: 8800, Currency: "CNY"}}, nil
}

func (fakeLedgerClient) RecordReconciliation(_ context.Context, input clients.ReconciliationInput, _ string) (clients.ReconciliationResult, error) {
	return clients.ReconciliationResult{ID: stringField(input.Report, "id", "reconciliation-from-ledger"), Status: "ok", Report: input.Report, BlockNewWorkspaces: false, Reason: "operator_reconciliation"}, nil
}

type fakeFabricClient struct{}

func (fakeFabricClient) CreateComputeAllocation(_ context.Context, input clients.ComputeAllocationInput, _ string) (clients.ComputeAllocation, error) {
	return clients.ComputeAllocation{ID: "compute-from-fabric", ProviderRequestID: "compute-request-from-fabric"}, nil
}

func (fakeFabricClient) DestroyComputeAllocation(_ context.Context, id string, _ string) (clients.ComputeAllocation, error) {
	return clients.ComputeAllocation{ID: id, ProviderRequestID: "compute-destroy-from-fabric"}, nil
}

func (fakeFabricClient) CreateStorageVolume(_ context.Context, input clients.StorageVolumeInput, _ string) (clients.StorageVolume, error) {
	return clients.StorageVolume{ID: "volume-from-fabric", WorkspaceID: input.WorkspaceID, Status: "ready", ProviderRequestID: "storage-request-from-fabric"}, nil
}

func (fakeFabricClient) DestroyStorageVolume(_ context.Context, id string, _ string) (clients.StorageVolume, error) {
	return clients.StorageVolume{ID: id, Status: "destroyed", ProviderRequestID: "storage-destroy-from-fabric"}, nil
}

func (fakeFabricClient) CreateStorageAttachment(_ context.Context, input clients.StorageAttachmentInput, _ string) (clients.StorageAttachment, error) {
	return clients.StorageAttachment{ID: "attachment-from-fabric", WorkspaceID: input.WorkspaceID, VolumeID: input.VolumeID, Status: "attached", ProviderRequestID: "attachment-request-from-fabric"}, nil
}

func (fakeFabricClient) DetachStorageAttachment(_ context.Context, id string, _ string) (clients.StorageAttachment, error) {
	return clients.StorageAttachment{ID: id, Status: "detached", ProviderRequestID: "attachment-detach-from-fabric"}, nil
}

func (fakeFabricClient) CreateWorkspaceRuntime(_ context.Context, input clients.WorkspaceRuntimeInput, _ string) (clients.WorkspaceRuntime, error) {
	return clients.WorkspaceRuntime{ID: "runtime-from-fabric", WorkspaceID: input.WorkspaceID, URL: "https://workspace.medopl.cn/w/ws-from-fabric/", ServiceName: "opl-compute-from-fabric"}, nil
}

func (fakeFabricClient) WorkspaceRuntimeStatus(_ context.Context, workspaceID string) (clients.WorkspaceRuntime, error) {
	return clients.WorkspaceRuntime{ID: "runtime-from-fabric", WorkspaceID: workspaceID, URL: "https://workspace.medopl.cn/w/" + workspaceID + "/", Status: "running"}, nil
}

func (fakeFabricClient) Readiness(_ context.Context) (map[string]any, error) {
	return map[string]any{"provider": "tencent-tke", "ready": true, "missingEnv": []string{}, "missingTools": []string{}}, nil
}

func TestCreateComputeAllocationUsesFabricService(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, fakeFabricClient{}))
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
	if body["id"] != "compute-from-fabric" || body["providerRequestId"] != "compute-request-from-fabric" {
		t.Fatalf("compute allocation did not come from Fabric: %#v", body)
	}
	getReq := httptest.NewRequest(http.MethodGet, "/api/compute-allocations/compute-from-fabric", nil)
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
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, fakeFabricClient{}))
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
