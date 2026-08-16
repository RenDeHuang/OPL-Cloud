package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

const (
	gatewayAccountingAdminEmail    = "admin@opl.local"
	gatewayAccountingAdminPassword = "gateway-accounting-admin-password"
	gatewayAccountingOwnerPassword = "gateway-accounting-owner-password"
	gatewayAccountingInitialMicros = int64(100_000_000)
	gatewayAccountingChargeMicros  = int64(52_580_000)
	gatewayAccountingSub2APIUserID = int64(41)
)

func TestGatewayAccountingAuthoritativeLocalChain(t *testing.T) {
	if controlPlaneTestPostgresBaseURL() == "" {
		t.Skip("PostgreSQL test gate is not configured")
	}
	t.Setenv("OPL_TENCENT_ZONE", "na-siliconvalley-1")
	ledger, ledgerClient := startGatewayAccountingLedger(t)

	t.Run("stable debit and replay", func(t *testing.T) {
		fixture := newGatewayAccountingSub2API(t, "owner-success@example.com", false)
		process := newGatewayAccountingControlPlane(t, ledger, fixture, "acct-gateway-success", "usr-gateway-success")
		api := process.login(t, fixture.ownerEmail, gatewayAccountingOwnerPassword)

		launch := api.mustRequest(t, http.MethodPost, "/api/workspace-launches", map[string]any{
			"name": "Gateway accounting success", "packageId": "basic", "sizeGb": 10, "autoRenew": false,
		}, "gateway-accounting-success", http.StatusAccepted)
		launchID := stringValue(launch["operationId"])
		if launchID == "" {
			t.Fatalf("launch response has no operation identity: %#v", launch)
		}
		operation := runGatewayAccountingLaunch(t, process, launchID, false)
		if operation.Status != "succeeded" || operation.Stage != "succeeded" {
			t.Fatalf("launch terminal state = %s/%s", operation.Status, operation.Stage)
		}
		for resource, read := range map[string]func(context.Context, string) ([]map[string]any, error){
			"compute":    process.store.ListComputes,
			"storage":    process.store.ListStorages,
			"attachment": process.store.ListAttachments,
		} {
			rows, err := read(context.Background(), operation.stringFact("accountId"))
			if err != nil || len(rows) != 0 {
				t.Fatalf("canonical launch copied %s truth: rows=%#v err=%v", resource, rows, err)
			}
		}
		runtimeStatus := gatewayAccountingEnvelopeData(t, api.mustRequest(t, http.MethodGet, "/api/workspaces/"+operation.stringFact("workspaceId")+"/runtime-status", nil, "", http.StatusOK))
		if stringValue(runtimeStatus["workspaceId"]) != operation.stringFact("workspaceId") || stringValue(runtimeStatus["runtimeId"]) != operation.stringFact("runtimeId") ||
			stringValue(runtimeStatus["url"]) != operation.stringFact("url") || stringValue(runtimeStatus["serviceName"]) != operation.stringFact("runtimeServiceName") || runtimeStatus["ready"] != true {
			t.Fatalf("terminal Fabric runtime readback = %#v operation=%s", runtimeStatus, workspaceLaunchReconcileResultSummary(operation))
		}
		if process.fabric.calls == nil || len(*process.fabric.calls) != 1 || (*process.fabric.calls)[0] != "fabric.runtime-status" {
			t.Fatalf("terminal Fabric runtime calls = %#v", process.fabric.calls)
		}

		beforeReplay := fixture.writeCounts()
		replay := api.mustRequest(t, http.MethodPost, "/api/workspace-launches", map[string]any{
			"name": "Gateway accounting success", "packageId": "basic", "sizeGb": 10, "autoRenew": false,
		}, "gateway-accounting-success", http.StatusAccepted)
		if stringValue(replay["operationId"]) != launchID {
			t.Fatalf("replay operation = %#v, want %s", replay, launchID)
		}
		if err := process.handler.app.runWorkspaceLaunchesOnce(context.Background(), process.handler.service); err != nil {
			t.Fatalf("replay worker: %v", err)
		}
		if afterReplay := fixture.writeCounts(); afterReplay != beforeReplay || afterReplay.charges != 1 || afterReplay.refunds != 0 {
			t.Fatalf("Sub2API writes after replay = %#v, before %#v", afterReplay, beforeReplay)
		}

		assertGatewayAccountingReadback(t, api, fixture.client, ledgerClient, operation, gatewayAccountingInitialMicros-gatewayAccountingChargeMicros)
	})

	t.Run("response loss parks without false success", func(t *testing.T) {
		fixture := newGatewayAccountingSub2API(t, "owner-unknown@example.com", true)
		process := newGatewayAccountingControlPlane(t, ledger, fixture, "acct-gateway-unknown", "usr-gateway-unknown")
		api := process.login(t, fixture.ownerEmail, gatewayAccountingOwnerPassword)

		launch := api.mustRequest(t, http.MethodPost, "/api/workspace-launches", map[string]any{
			"name": "Gateway accounting unknown", "packageId": "basic", "sizeGb": 10, "autoRenew": false,
		}, "gateway-accounting-unknown", http.StatusAccepted)
		launchID := stringValue(launch["operationId"])
		operation := runGatewayAccountingLaunch(t, process, launchID, true)
		if operation.Status != "manual_review" || operation.Stage != "debit" {
			t.Fatalf("unknown debit terminal state = %s/%s", operation.Status, operation.Stage)
		}
		if counts := fixture.writeCounts(); counts.charges != 1 || counts.refunds != 0 {
			t.Fatalf("unknown debit Sub2API writes = %#v", counts)
		}

		replay := api.mustRequest(t, http.MethodPost, "/api/workspace-launches", map[string]any{
			"name": "Gateway accounting unknown", "packageId": "basic", "sizeGb": 10, "autoRenew": false,
		}, "gateway-accounting-unknown", http.StatusAccepted)
		if stringValue(replay["operationId"]) != launchID || stringValue(replay["status"]) != "manual_review" {
			t.Fatalf("unknown replay response = %#v", replay)
		}
		if err := process.handler.app.runWorkspaceLaunchesOnce(context.Background(), process.handler.service); err != nil {
			t.Fatalf("manual-review worker replay: %v", err)
		}
		if counts := fixture.writeCounts(); counts.charges != 1 || counts.refunds != 0 {
			t.Fatalf("manual-review replay Sub2API writes = %#v", counts)
		}

		balance, err := fixture.client.Balance(context.Background(), gatewayAccountingSub2APIUserID)
		if err != nil || balance.USDMicros != gatewayAccountingInitialMicros-gatewayAccountingChargeMicros {
			t.Fatalf("authoritative unknown balance = %#v, err=%v", balance, err)
		}
		history, err := fixture.client.FinancialBalanceHistoryByCodes(context.Background(), gatewayAccountingSub2APIUserID, []string{operation.stringFact("sub2apiRedeemCode")})
		if err != nil || !gatewayAccountingHistoryMatches(history, operation.stringFact("sub2apiRedeemCode"), -gatewayAccountingChargeMicros) {
			t.Fatalf("authoritative unknown history = %#v, err=%v", history, err)
		}
		workspacePage := gatewayAccountingEnvelopeData(t, api.mustRequest(t, http.MethodGet, "/api/workspaces", nil, "", http.StatusOK))
		if gatewayAccountingInt64(workspacePage["total"]) != 0 {
			t.Fatalf("unknown debit created Workspace projection: %#v", workspacePage)
		}
		receipts, err := ledgerClient.ListReceipts(context.Background(), clients.ReceiptQuery{AccountID: operation.stringFact("accountId"), Limit: 50})
		if err != nil || len(receipts.Receipts) != 0 {
			t.Fatalf("unknown debit Ledger receipts = %#v, err=%v", receipts, err)
		}
	})
}

type gatewayAccountingWriteCounts struct {
	charges int
	refunds int
	keys    int
}

type gatewayAccountingHistoryRow struct {
	code      string
	value     int64
	usedAt    time.Time
	createdAt time.Time
}

type gatewayAccountingKey struct {
	id        int64
	name      string
	value     string
	groupID   int64
	createdAt time.Time
}

type gatewayAccountingSub2API struct {
	testingServer *httptest.Server
	client        *clients.Sub2APIHTTPClient
	ownerEmail    string
	responseLoss  bool

	mu           sync.Mutex
	balance      int64
	history      []gatewayAccountingHistoryRow
	keys         map[int64]gatewayAccountingKey
	writes       gatewayAccountingWriteCounts
	nextKeyID    int64
	lossInjected bool
	historyLoss  bool
}

func newGatewayAccountingSub2API(t *testing.T, ownerEmail string, responseLoss bool) *gatewayAccountingSub2API {
	t.Helper()
	fixture := &gatewayAccountingSub2API{
		ownerEmail: ownerEmail, responseLoss: responseLoss, balance: gatewayAccountingInitialMicros,
		keys: map[int64]gatewayAccountingKey{}, nextKeyID: 700,
	}
	fixture.testingServer = httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(fixture.testingServer.Close)
	baseClient := fixture.testingServer.Client()
	baseTransport := baseClient.Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	baseClient.Transport = &gatewayAccountingFaultTransport{base: baseTransport, fixture: fixture}
	baseClient.Timeout = 5 * time.Second
	client, err := clients.NewSub2APIHTTPClient(clients.Sub2APIConfig{
		BaseURL: fixture.testingServer.URL, AdminEmail: gatewayAccountingAdminEmail,
		AdminPassword: gatewayAccountingAdminPassword, Timeout: 3 * time.Second,
	}, baseClient)
	if err != nil {
		t.Fatal(err)
	}
	fixture.client = client
	return fixture
}

func (f *gatewayAccountingSub2API) serveHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/login":
		f.login(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups/available":
		if !gatewayAccountingBearer(r, "gateway-owner-token") {
			gatewayAccountingJSON(w, http.StatusUnauthorized, map[string]any{"code": "unauthorized"})
			return
		}
		gatewayAccountingEnvelope(w, []any{map[string]any{
			"id": 7, "name": "Codex", "description": "Codex", "platform": "openai",
			"rate_multiplier": 1, "subscription_type": "standard", "status": "active",
		}})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/admin/usage/search-api-keys":
		f.searchKeys(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/admin/users/") && strings.HasSuffix(r.URL.Path, "/api-keys"):
		f.userKeys(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/admin/users/") && strings.HasSuffix(r.URL.Path, "/balance-history"):
		f.balanceHistory(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/admin/users/"):
		f.user(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/keys":
		f.createKey(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/admin/redeem-codes/create-and-redeem":
		f.redeem(w, r)
	default:
		gatewayAccountingJSON(w, http.StatusNotFound, map[string]any{"code": "not_found"})
	}
}

func (f *gatewayAccountingSub2API) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil {
		gatewayAccountingJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_login"})
		return
	}
	var id int64
	var token string
	switch {
	case input.Email == gatewayAccountingAdminEmail && input.Password == gatewayAccountingAdminPassword:
		id, token = 1, "gateway-admin-token"
	case input.Email == f.ownerEmail && input.Password == gatewayAccountingOwnerPassword:
		id, token = gatewayAccountingSub2APIUserID, "gateway-owner-token"
	default:
		gatewayAccountingJSON(w, http.StatusUnauthorized, map[string]any{"code": "invalid_credentials"})
		return
	}
	gatewayAccountingEnvelope(w, map[string]any{
		"access_token": token, "refresh_token": token + "-refresh",
		"user": map[string]any{"id": id, "email": input.Email, "status": "active"},
	})
}

func (f *gatewayAccountingSub2API) user(w http.ResponseWriter, r *http.Request) {
	if !gatewayAccountingBearer(r, "gateway-admin-token") {
		gatewayAccountingJSON(w, http.StatusUnauthorized, map[string]any{"code": "unauthorized"})
		return
	}
	rawID := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/users/")
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id != 1 && id != gatewayAccountingSub2APIUserID {
		gatewayAccountingJSON(w, http.StatusNotFound, map[string]any{"code": "user_not_found"})
		return
	}
	f.mu.Lock()
	balance := f.balance
	f.mu.Unlock()
	email := f.ownerEmail
	if id == 1 {
		email, balance = gatewayAccountingAdminEmail, 0
	}
	now := time.Now().UTC()
	gatewayAccountingEnvelope(w, map[string]any{
		"id": id, "email": email, "balance": gatewayAccountingUSD(balance), "status": "active",
		"created_at": now.Add(-time.Hour).Format(time.RFC3339Nano), "updated_at": now.Format(time.RFC3339Nano),
	})
}

func (f *gatewayAccountingSub2API) searchKeys(w http.ResponseWriter, r *http.Request) {
	if !gatewayAccountingBearer(r, "gateway-admin-token") || r.URL.Query().Get("user_id") != strconv.FormatInt(gatewayAccountingSub2APIUserID, 10) {
		gatewayAccountingJSON(w, http.StatusUnauthorized, map[string]any{"code": "unauthorized"})
		return
	}
	name := r.URL.Query().Get("q")
	f.mu.Lock()
	items := make([]any, 0, len(f.keys))
	for _, key := range f.keys {
		if key.name == name {
			items = append(items, map[string]any{"id": key.id, "name": key.name, "user_id": gatewayAccountingSub2APIUserID})
		}
	}
	f.mu.Unlock()
	gatewayAccountingEnvelope(w, items)
}

func (f *gatewayAccountingSub2API) userKeys(w http.ResponseWriter, r *http.Request) {
	if !gatewayAccountingBearer(r, "gateway-admin-token") {
		gatewayAccountingJSON(w, http.StatusUnauthorized, map[string]any{"code": "unauthorized"})
		return
	}
	prefix := "/api/v1/admin/users/"
	rawID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), "/api-keys")
	if rawID != strconv.FormatInt(gatewayAccountingSub2APIUserID, 10) {
		gatewayAccountingJSON(w, http.StatusNotFound, map[string]any{"code": "user_not_found"})
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	f.mu.Lock()
	keys := make([]gatewayAccountingKey, 0, len(f.keys))
	for _, key := range f.keys {
		keys = append(keys, key)
	}
	f.mu.Unlock()
	sort.Slice(keys, func(i, j int) bool { return keys[i].id < keys[j].id })
	pages := 1
	if len(keys) > 0 {
		pages = (len(keys) + pageSize - 1) / pageSize
	}
	items := []any{}
	start := (page - 1) * pageSize
	if page > 0 && pageSize > 0 && start >= 0 && start < len(keys) {
		end := start + pageSize
		if end > len(keys) {
			end = len(keys)
		}
		for _, key := range keys[start:end] {
			items = append(items, gatewayAccountingKeyPayload(key))
		}
	}
	gatewayAccountingEnvelope(w, map[string]any{"items": items, "total": len(keys), "page": page, "page_size": pageSize, "pages": pages})
}

func (f *gatewayAccountingSub2API) createKey(w http.ResponseWriter, r *http.Request) {
	if !gatewayAccountingBearer(r, "gateway-owner-token") || strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
		gatewayAccountingJSON(w, http.StatusUnauthorized, map[string]any{"code": "unauthorized"})
		return
	}
	var input struct {
		Name    string      `json:"name"`
		GroupID int64       `json:"group_id"`
		Quota   json.Number `json:"quota"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	if decoder.Decode(&input) != nil || input.Name == "" || input.GroupID != 7 {
		gatewayAccountingJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_key"})
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes.keys++
	for _, key := range f.keys {
		if key.name == input.Name {
			gatewayAccountingEnvelope(w, gatewayAccountingKeyPayload(key))
			return
		}
	}
	f.nextKeyID++
	key := gatewayAccountingKey{
		id: f.nextKeyID, name: input.Name, value: "sk-gateway-accounting-" + strconv.FormatInt(f.nextKeyID, 10),
		groupID: input.GroupID, createdAt: time.Now().UTC(),
	}
	f.keys[key.id] = key
	gatewayAccountingEnvelope(w, gatewayAccountingKeyPayload(key))
}

func (f *gatewayAccountingSub2API) balanceHistory(w http.ResponseWriter, r *http.Request) {
	if !gatewayAccountingBearer(r, "gateway-admin-token") {
		gatewayAccountingJSON(w, http.StatusUnauthorized, map[string]any{"code": "unauthorized"})
		return
	}
	prefix := "/api/v1/admin/users/"
	rawID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), "/balance-history")
	if rawID != strconv.FormatInt(gatewayAccountingSub2APIUserID, 10) {
		gatewayAccountingJSON(w, http.StatusNotFound, map[string]any{"code": "user_not_found"})
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	f.mu.Lock()
	history := append([]gatewayAccountingHistoryRow(nil), f.history...)
	f.mu.Unlock()
	sort.Slice(history, func(i, j int) bool { return history[i].createdAt.After(history[j].createdAt) })
	pages := 1
	if len(history) > 0 {
		pages = (len(history) + pageSize - 1) / pageSize
	}
	items := []any{}
	start := (page - 1) * pageSize
	if page > 0 && pageSize > 0 && start >= 0 && start < len(history) {
		end := start + pageSize
		if end > len(history) {
			end = len(history)
		}
		for _, row := range history[start:end] {
			items = append(items, map[string]any{
				"code": row.code, "type": "balance", "value": gatewayAccountingUSD(row.value), "status": "used",
				"used_by": gatewayAccountingSub2APIUserID, "used_at": row.usedAt.Format(time.RFC3339Nano), "created_at": row.createdAt.Format(time.RFC3339Nano),
			})
		}
	}
	gatewayAccountingEnvelope(w, map[string]any{"items": items, "total": len(history), "page": page, "page_size": pageSize, "pages": pages})
}

func (f *gatewayAccountingSub2API) redeem(w http.ResponseWriter, r *http.Request) {
	if !gatewayAccountingBearer(r, "gateway-admin-token") || strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
		gatewayAccountingJSON(w, http.StatusUnauthorized, map[string]any{"code": "unauthorized"})
		return
	}
	var input struct {
		Code   string      `json:"code"`
		Type   string      `json:"type"`
		Value  json.Number `json:"value"`
		UserID int64       `json:"user_id"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	if decoder.Decode(&input) != nil || input.Type != "balance" || input.UserID != gatewayAccountingSub2APIUserID || input.Code != r.Header.Get("Idempotency-Key") {
		gatewayAccountingJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_redeem"})
		return
	}
	value, err := clients.ParseUSDDecimalMicros(input.Value.String())
	if err != nil || value == 0 {
		gatewayAccountingJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_redeem_value"})
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.history {
		if row.code == input.Code {
			gatewayAccountingJSON(w, http.StatusConflict, map[string]any{"code": "redeem_conflict"})
			return
		}
	}
	if f.balance+value < 0 {
		gatewayAccountingJSON(w, http.StatusConflict, map[string]any{"code": "insufficient_balance"})
		return
	}
	now := time.Now().UTC()
	f.balance += value
	f.history = append(f.history, gatewayAccountingHistoryRow{code: input.Code, value: value, usedAt: now, createdAt: now})
	if value < 0 {
		f.writes.charges++
	} else {
		f.writes.refunds++
	}
	gatewayAccountingEnvelope(w, map[string]any{"redeem_code": map[string]any{
		"code": input.Code, "type": "balance", "value": gatewayAccountingUSD(value), "status": "used", "used_by": input.UserID,
	}})
}

func (f *gatewayAccountingSub2API) writeCounts() gatewayAccountingWriteCounts {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writes
}

type gatewayAccountingFaultTransport struct {
	base    http.RoundTripper
	fixture *gatewayAccountingSub2API
}

func (t *gatewayAccountingFaultTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return response, err
	}
	isRedeem := request.Method == http.MethodPost && request.URL.Path == "/api/v1/admin/redeem-codes/create-and-redeem"
	isHistory := request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/balance-history")
	t.fixture.mu.Lock()
	loss := false
	if t.fixture.responseLoss && isRedeem && !t.fixture.lossInjected {
		t.fixture.lossInjected, t.fixture.historyLoss, loss = true, true, true
	} else if isHistory && t.fixture.historyLoss {
		t.fixture.historyLoss, loss = false, true
	}
	t.fixture.mu.Unlock()
	if !loss {
		return response, nil
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	return nil, io.ErrUnexpectedEOF
}

type gatewayAccountingFabric struct {
	fakeFabricClient
	mu     sync.Mutex
	stages map[string]clients.WorkspaceLaunchStageResult
}

func newGatewayAccountingFabric() *gatewayAccountingFabric {
	return &gatewayAccountingFabric{stages: map[string]clients.WorkspaceLaunchStageResult{}}
}

func (f *gatewayAccountingFabric) WorkspaceRuntimeStatus(_ context.Context, workspaceID string) (clients.WorkspaceRuntime, error) {
	f.record("fabric.runtime-status")
	f.mu.Lock()
	defer f.mu.Unlock()
	result, ok := f.stages["runtime"]
	if !ok || result.State != "ready" || result.Binding.WorkspaceID != workspaceID {
		return clients.WorkspaceRuntime{WorkspaceID: workspaceID, Status: "not_found"}, nil
	}
	return clients.WorkspaceRuntime{
		ID: result.Resources.RuntimeID, WorkspaceID: workspaceID, URL: result.Resources.RuntimeURL,
		ServiceName: result.Resources.RuntimeServiceName, Status: "running", Ready: true,
		Checks: []any{map[string]any{"name": "service_endpoints_ready", "ok": true}},
	}, nil
}

func (f *gatewayAccountingFabric) PreflightWorkspaceLaunch(_ context.Context, input clients.WorkspaceLaunchPreflightInput) (clients.WorkspaceLaunchPreflight, error) {
	return clients.WorkspaceLaunchPreflight{
		SchemaVersion: clients.WorkspaceLaunchFabricSchemaVersion, Available: true, Reason: "none",
		LaunchOperationID: input.LaunchOperationID, RequestHash: input.RequestHash,
		ProviderProfileRef: "local-provider-profile", BindingRef: "local-preflight-" + stableID(input.LaunchOperationID)[:12],
	}, nil
}

func (*gatewayAccountingFabric) MonthlyPreflight(_ context.Context, input clients.MonthlyPreflightInput) (clients.MonthlyPreflight, error) {
	return clients.MonthlyPreflight{
		ResourceType: input.ResourceType, PackageID: input.PackageID, SizeGB: input.SizeGB, Zone: input.Zone,
		Available: true, ChargeType: "PREPAID", PeriodMonths: 1, RenewFlag: "NOTIFY_AND_MANUAL_RENEW",
		ProviderPriceCNY: 12.34,
	}, nil
}

func (f *gatewayAccountingFabric) ReadWorkspaceLaunchStage(_ context.Context, input clients.WorkspaceLaunchStageInput) (clients.WorkspaceLaunchStageResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result, ok := f.stages[input.Binding.Stage]
	if !ok {
		return clients.WorkspaceLaunchStageResult{
			SchemaVersion: clients.WorkspaceLaunchFabricSchemaVersion, State: "absent", Reason: "no_stage_record", Binding: input.Binding, Resources: input.Resources,
		}, nil
	}
	return result, nil
}

func (f *gatewayAccountingFabric) EnsureWorkspaceLaunchStage(_ context.Context, input clients.WorkspaceLaunchStageInput) (clients.WorkspaceLaunchStageResult, error) {
	resources := input.Resources
	suffix := stableID(input.Binding.LaunchOperationID, input.Binding.Stage)[:12]
	switch input.Binding.Stage {
	case "ensure_compute_allocation":
		resources.ComputeAllocationID, resources.ComputeBindingRef = "compute-"+suffix, "compute-binding-"+suffix
	case "storage":
		resources.StorageID, resources.StorageBindingRef = "storage-"+suffix, "storage-binding-"+suffix
	case "attachment":
		resources.AttachmentID, resources.AttachmentBindingRef = "attachment-"+suffix, "attachment-binding-"+suffix
	case "secret":
		if input.GatewayCredential == nil || input.GatewayCredential.KeyID <= 0 || strings.TrimSpace(input.GatewayCredential.Value) == "" {
			return clients.WorkspaceLaunchStageResult{}, errors.New("gateway credential missing")
		}
		resources.GatewaySecretRef, resources.GatewaySecretVersion = "secret-"+suffix, "v1"
		resources.GatewaySecretFingerprint, resources.SecretBindingRef = workspaceLaunchCredentialFingerprint(input.GatewayCredential.Value), "secret-binding-"+suffix
	case "runtime":
		resources.RuntimeID, resources.RuntimeServiceName = "runtime-"+suffix, "workspace-runtime-"+suffix
		resources.RuntimeUsername, resources.RuntimeURL = "opl", "https://workspace.local/"+input.Binding.WorkspaceID
		resources.RuntimeCredentialStatus, resources.RuntimeCredentialVersion = "configured", "v1"
		resources.RuntimeCredentialSecretRef, resources.RuntimeBindingRef = "runtime-credential-"+suffix, "runtime-binding-"+suffix
	default:
		return clients.WorkspaceLaunchStageResult{}, errors.New("unexpected Fabric stage")
	}
	result := clients.WorkspaceLaunchStageResult{
		SchemaVersion: clients.WorkspaceLaunchFabricSchemaVersion, State: "ready", Reason: "none", Binding: input.Binding, Resources: resources,
	}
	f.mu.Lock()
	f.stages[input.Binding.Stage] = result
	f.mu.Unlock()
	return result, nil
}

type gatewayAccountingControlPlane struct {
	server  *httptest.Server
	handler *controlPlaneHTTPHandler
	store   StateStore
	fabric  *gatewayAccountingFabric
}

func newGatewayAccountingControlPlane(t *testing.T, ledger clients.LedgerClient, sub2API *gatewayAccountingSub2API, accountID, userID string) *gatewayAccountingControlPlane {
	t.Helper()
	t.Setenv("NODE_ENV", "")
	t.Setenv("OPL_WORKSPACE_LAUNCH_WORKER_ENABLED", "0")
	t.Setenv(controlledBasicPilotEnabledEnv, "1")
	t.Setenv(controlledBasicPilotAccountsEnv, accountID)
	t.Setenv(controlledBasicPilotMaxInFlightEnv, "1")
	databaseURL := gatewayAccountingDatabase(t)
	store, err := newTestPostgresEntStateStore(databaseURL)
	if err != nil {
		t.Fatalf("start Control Plane PostgreSQL store: %v", err)
	}
	t.Cleanup(func() { _ = store.(*postgresEntStateStore).client.Close() })
	account, user, organization, membership := provisionedAccountRowsFor(accountID, userID, "org-"+accountID, sub2API.ownerEmail, gatewayAccountingSub2APIUserID)
	if err := store.CreateProvisionedAccount(context.Background(), account, user, organization, membership); err != nil {
		t.Fatalf("seed Control Plane owner: %v", err)
	}
	fabric := newGatewayAccountingFabric()
	fabricCalls := []string{}
	fabric.fakeFabricClient.calls = &fabricCalls
	service := controlplane.NewService(ledger, fabric, sub2API.client)
	handler, err := NewPersistentServer(service, store)
	if err != nil {
		t.Fatalf("start Control Plane HTTP owner: %v", err)
	}
	typed, ok := handler.(*controlPlaneHTTPHandler)
	if !ok {
		t.Fatalf("Control Plane handler type = %T", handler)
	}
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	return &gatewayAccountingControlPlane{server: server, handler: typed, store: store, fabric: fabric}
}

func (p *gatewayAccountingControlPlane) login(t *testing.T, email, password string) *gatewayAccountingAPI {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := p.server.Client()
	client.Jar, client.Timeout = jar, 10*time.Second
	api := &gatewayAccountingAPI{baseURL: p.server.URL, client: client}
	response := api.mustRequest(t, http.MethodPost, "/api/auth/login", map[string]any{"email": email, "password": password}, "", http.StatusOK)
	api.csrf = stringValue(response["csrfToken"])
	if api.csrf == "" {
		t.Fatalf("Control Plane login response = %#v", response)
	}
	return api
}

type gatewayAccountingAPI struct {
	baseURL string
	client  *http.Client
	csrf    string
}

func (a *gatewayAccountingAPI) mustRequest(t *testing.T, method, path string, input any, idempotencyKey string, wantStatus int) map[string]any {
	t.Helper()
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(context.Background(), method, a.baseURL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if a.csrf != "" && method != http.MethodGet && method != http.MethodHead {
		request.Header.Set("x-opl-csrf", a.csrf)
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response, err := a.client.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		t.Fatalf("decode %s %s: %v", method, path, err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("%s %s status=%d want=%d body=%#v", method, path, response.StatusCode, wantStatus, payload)
	}
	return payload
}

func runGatewayAccountingLaunch(t *testing.T, process *gatewayAccountingControlPlane, operationID string, wantUnknown bool) workspaceLaunchReconcileOperation {
	t.Helper()
	if operationID == "" {
		t.Fatal("Workspace launch operation ID is empty")
	}
	var operation workspaceLaunchReconcileOperation
	for range 12 {
		err := process.handler.app.runWorkspaceLaunchesOnce(context.Background(), process.handler.service)
		row, found, readErr := process.store.GetRuntimeOperation(context.Background(), operationID)
		if readErr != nil || !found {
			t.Fatalf("read launch found=%t err=%v", found, readErr)
		}
		operation, readErr = decodeWorkspaceLaunchReconcileOperation(row)
		if readErr != nil {
			t.Fatalf("decode launch: %v", readErr)
		}
		if wantUnknown && operation.Status == "manual_review" {
			if err == nil {
				t.Fatal("response-loss debit parked without surfacing the uncertain mutation")
			}
			return operation
		}
		if err != nil {
			t.Fatalf("run Workspace launch: %v", err)
		}
		if operation.Status == "succeeded" {
			return operation
		}
	}
	workspace, found, err := process.store.GetWorkspace(context.Background(), operation.stringFact("workspaceId"))
	t.Fatalf("Workspace launch did not reach terminal state: %s/%s workspaceFound=%t workspace=%#v readErr=%v operation=%#v", operation.Status, operation.Stage, found, workspace, err, operation.raw)
	return operation
}

func assertGatewayAccountingReadback(t *testing.T, api *gatewayAccountingAPI, sub2API *clients.Sub2APIHTTPClient, ledger clients.LedgerReceiptListClient, operation workspaceLaunchReconcileOperation, wantBalance int64) {
	t.Helper()
	accountID, workspaceID := operation.stringFact("accountId"), operation.stringFact("workspaceId")
	code := operation.stringFact("sub2apiRedeemCode")
	wallet := gatewayAccountingEnvelopeData(t, api.mustRequest(t, http.MethodGet, "/api/gateway/wallet", nil, "", http.StatusOK))
	if stringValue(wallet["userId"]) != strconv.FormatInt(gatewayAccountingSub2APIUserID, 10) || stringValue(wallet["usdMicros"]) != strconv.FormatInt(wantBalance, 10) {
		t.Fatalf("Control Plane authoritative wallet = %#v", wallet)
	}
	history, err := sub2API.FinancialBalanceHistoryByCodes(context.Background(), gatewayAccountingSub2APIUserID, []string{code})
	if err != nil || !gatewayAccountingHistoryMatches(history, code, -gatewayAccountingChargeMicros) {
		t.Fatalf("Sub2API authoritative history = %#v, err=%v", history, err)
	}
	balanceHistory := gatewayAccountingEnvelopeData(t, api.mustRequest(t, http.MethodGet, "/api/gateway/balance-history?page=1&pageSize=20", nil, "", http.StatusOK))
	if gatewayAccountingInt64(balanceHistory["total"]) != 1 {
		t.Fatalf("Control Plane balance history = %#v", balanceHistory)
	}
	workspacePage := gatewayAccountingEnvelopeData(t, api.mustRequest(t, http.MethodGet, "/api/workspaces", nil, "", http.StatusOK))
	items, ok := workspacePage["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("Control Plane Workspace page = %#v", workspacePage)
	}
	workspace, ok := items[0].(map[string]any)
	if !ok || stringValue(workspace["id"]) != workspaceID || stringValue(workspace["state"]) != "running" {
		t.Fatalf("Control Plane Workspace projection = %#v", workspace)
	}

	page, err := ledger.ListReceipts(context.Background(), clients.ReceiptQuery{AccountID: accountID, TypePrefix: "billing.", Limit: 50})
	if err != nil || len(page.Receipts) != 1 {
		t.Fatalf("Ledger receipt page = %#v, err=%v", page, err)
	}
	receipt := page.Receipts[0]
	if receipt.Type != "billing.workspace_purchased.v1" || receipt.AccountID != accountID || receipt.WorkspaceID != workspaceID || receipt.RequestID != operation.ID ||
		stringValue(receipt.Cost["sub2apiRedeemCode"]) != code || gatewayAccountingInt64(receipt.Cost["totalUsdMicros"]) != gatewayAccountingChargeMicros {
		t.Fatalf("Ledger purchase receipt identity = %#v", receipt)
	}
	projected := gatewayAccountingEnvelopeData(t, api.mustRequest(t, http.MethodGet, "/api/billing/receipts", nil, "", http.StatusOK))
	projectedReceipts, ok := projected["receipts"].([]any)
	if !ok || len(projectedReceipts) != 1 {
		t.Fatalf("Control Plane Ledger readback = %#v", projected)
	}
	projectedReceipt, ok := projectedReceipts[0].(map[string]any)
	if !ok || stringValue(projectedReceipt["receiptId"]) != receipt.ReceiptID || stringValue(projectedReceipt["workspaceId"]) != workspaceID || stringValue(projectedReceipt["chargeReference"]) != code {
		t.Fatalf("Control Plane purchase receipt projection = %#v", projectedReceipt)
	}
}

func gatewayAccountingHistoryMatches(history map[string]clients.Sub2APIBalanceHistoryEntry, code string, amount int64) bool {
	entry, found := history[code]
	return len(history) == 1 && found && entry.Code == code && entry.Type == "balance" && entry.Status == "used" &&
		entry.ValueUSDMicros == amount && entry.UsedBy != nil && *entry.UsedBy == gatewayAccountingSub2APIUserID && entry.UsedAt != nil
}

func gatewayAccountingInt64(value any) int64 {
	switch value := value.(type) {
	case json.Number:
		parsed, _ := value.Int64()
		return parsed
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	default:
		return -1
	}
}

func gatewayAccountingEnvelopeData(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	if payload["available"] != true || payload["status"] == "unavailable" {
		t.Fatalf("source envelope unavailable: %#v", payload)
	}
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("source envelope data = %#v", payload["data"])
	}
	return data
}

func startGatewayAccountingLedger(t *testing.T) (clients.LedgerClient, clients.LedgerReceiptListClient) {
	t.Helper()
	databaseURL := gatewayAccountingDatabase(t, "opl_gateway_ledger_")
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve integration test location")
	}
	ledgerDir := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "ledger"))
	binary := filepath.Join(t.TempDir(), "opl-ledger")
	build := exec.Command("go", "build", "-o", binary, "./cmd/ledger")
	build.Dir = ledgerDir
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build real Ledger HTTP owner: %v\n%s", err, output)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	token := "gateway-accounting-ledger-token"
	capabilityKey := "gateway-accounting-ledger-capability-key-32-chars"
	command := exec.Command(binary)
	command.Env = gatewayAccountingProcessEnv(os.Environ(), map[string]string{
		"LEDGER_ADDR": address, "OPL_INTERNAL_SERVICE_TOKEN": token, "DATABASE_URL": databaseURL,
		"OPL_LEDGER_CAPABILITY_KEY": capabilityKey,
		"NODE_ENV":                  "local", "OPL_POSTGRES_TESTS": "1",
	})
	var logs bytes.Buffer
	command.Stdout, command.Stderr = &logs, &logs
	if err := command.Start(); err != nil {
		t.Fatalf("start real Ledger HTTP owner: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	t.Cleanup(func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	})
	baseURL := "http://" + address
	deadline := time.Now().Add(10 * time.Second)
	for {
		request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+"/readyz", nil)
		response, requestErr := (&http.Client{Timeout: time.Second}).Do(request)
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		select {
		case processErr := <-done:
			t.Fatalf("Ledger exited before readiness: %v\n%s", processErr, logs.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("Ledger readiness timed out: %s", logs.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
	ledger := clients.NewLedgerHTTPClientWithCapability(baseURL, token, capabilityKey, &http.Client{Timeout: 5 * time.Second})
	list, ok := ledger.(clients.LedgerReceiptListClient)
	if !ok {
		t.Fatal("typed Ledger HTTP client does not support receipt readback")
	}
	ready, ok := ledger.(clients.LedgerReadinessClient)
	if !ok || ready.Ready(context.Background()) != nil {
		t.Fatal("typed Ledger HTTP client readiness failed")
	}
	return ledger, list
}

func gatewayAccountingDatabase(t *testing.T, prefixes ...string) string {
	t.Helper()
	admin := openControlPlaneTestPostgres(t)
	prefix := "opl_gateway_accounting_"
	if len(prefixes) > 0 {
		prefix = prefixes[0]
	}
	database := prefix + strconv.FormatInt(time.Now().UnixNano(), 10)
	if _, err := admin.Exec(`CREATE DATABASE "` + database + `"`); err != nil {
		_ = admin.Close()
		t.Fatalf("create gateway accounting database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1`, database)
		_, _ = admin.Exec(`DROP DATABASE "` + database + `"`)
		_ = admin.Close()
	})
	databaseURL := controlPlaneTestPostgresURL(t, database, "")
	if parsed, err := url.Parse(databaseURL); err == nil && parsed.Scheme != "" {
		return databaseURL
	}
	host, port, user := os.Getenv("PGHOST"), os.Getenv("PGPORT"), os.Getenv("PGUSER")
	if net.ParseIP(host) == nil || port == "" || user == "" {
		t.Fatalf("keyword PostgreSQL test DSN requires explicit PGHOST, PGPORT, and PGUSER for subprocess use")
	}
	return (&url.URL{
		Scheme: "postgresql", User: url.User(user), Host: net.JoinHostPort(host, port), Path: "/" + database,
		RawQuery: url.Values{"sslmode": {"disable"}}.Encode(),
	}).String()
}

func gatewayAccountingProcessEnv(base []string, overrides map[string]string) []string {
	result := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[key]; !replaced {
			result = append(result, entry)
		}
	}
	for key, value := range overrides {
		result = append(result, key+"="+value)
	}
	return result
}

func gatewayAccountingKeyPayload(key gatewayAccountingKey) map[string]any {
	return map[string]any{
		"id": key.id, "user_id": gatewayAccountingSub2APIUserID, "name": key.name, "key": key.value, "group_id": key.groupID, "status": "active",
		"ip_whitelist": []string{}, "ip_blacklist": []string{}, "quota": 0, "quota_used": 0,
		"rate_limit_5h": 0, "rate_limit_1d": 0, "rate_limit_7d": 0, "usage_5h": 0, "usage_1d": 0, "usage_7d": 0,
		"created_at": key.createdAt.Format(time.RFC3339Nano), "updated_at": key.createdAt.Format(time.RFC3339Nano), "current_concurrency": 0,
	}
}

func gatewayAccountingBearer(r *http.Request, token string) bool {
	return r.Header.Get("Authorization") == "Bearer "+token
}

func gatewayAccountingUSD(micros int64) json.RawMessage {
	sign := ""
	if micros < 0 {
		sign, micros = "-", -micros
	}
	return json.RawMessage(fmt.Sprintf("%s%d.%06d", sign, micros/1_000_000, micros%1_000_000))
}

func gatewayAccountingEnvelope(w http.ResponseWriter, data any) {
	gatewayAccountingJSON(w, http.StatusOK, map[string]any{"data": data})
}

func gatewayAccountingJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
