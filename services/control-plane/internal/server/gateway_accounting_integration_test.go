package server

import (
	"bufio"
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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

const (
	gatewayAccountingAdminEmail    = "admin-gateway@example.test"
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
		fixture := newGatewayAccountingSub2API(t, "owner-success@example.test", false)
		process := newGatewayAccountingControlPlane(t, ledger, fixture, "acct-gateway-success", "usr-gateway-success")
		api := process.login(t, fixture.ownerEmail, gatewayAccountingOwnerPassword)

		launch := api.mustRequest(t, http.MethodPost, "/api/workspace-launches", map[string]any{
			"name": "Gateway accounting success", "packageId": "basic", "autoRenew": false,
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
		fabricCallsBeforeTerminalReadback := len(*process.fabric.calls)
		terminal := api.mustRequest(t, http.MethodGet, "/api/workspace-launches/"+launchID, nil, "", http.StatusOK)
		if stringValue(terminal["operationId"]) != operation.ID || stringValue(terminal["accountId"]) != operation.stringFact("accountId") ||
			stringValue(terminal["workspaceId"]) != operation.stringFact("workspaceId") || stringValue(terminal["runtimeServiceName"]) != operation.stringFact("runtimeServiceName") ||
			stringValue(terminal["receiptId"]) != operation.stringFact("receiptId") || stringValue(terminal["status"]) != "succeeded" || stringValue(terminal["stage"]) != "succeeded" {
			t.Fatalf("terminal Workspace launch HTTP readback = %#v operation=%s", terminal, workspaceLaunchReconcileResultSummary(operation))
		}
		runtimeStatus := gatewayAccountingEnvelopeData(t, api.mustRequest(t, http.MethodGet, "/api/workspaces/"+operation.stringFact("workspaceId")+"/runtime-status", nil, "", http.StatusOK))
		if stringValue(runtimeStatus["workspaceId"]) != operation.stringFact("workspaceId") || stringValue(runtimeStatus["runtimeId"]) != operation.stringFact("runtimeId") ||
			stringValue(runtimeStatus["url"]) != operation.stringFact("url") || stringValue(runtimeStatus["serviceName"]) != operation.stringFact("runtimeServiceName") || runtimeStatus["ready"] != true {
			t.Fatalf("terminal Fabric runtime readback = %#v operation=%s", runtimeStatus, workspaceLaunchReconcileResultSummary(operation))
		}
		if process.fabric.calls == nil || len(*process.fabric.calls) != fabricCallsBeforeTerminalReadback+1 || (*process.fabric.calls)[fabricCallsBeforeTerminalReadback] != "fabric.runtime-status" {
			t.Fatalf("terminal Fabric runtime calls = %#v", process.fabric.calls)
		}
		assertGatewayAccountingCanonicalWorkspaceOpen(t, api, operation)
		if len(*process.fabric.calls) != fabricCallsBeforeTerminalReadback+1 {
			t.Fatalf("canonical Workspace open used legacy or extra Fabric reads: %#v", *process.fabric.calls)
		}

		beforeReplay := fixture.writeCounts(t)
		replay := api.mustRequest(t, http.MethodPost, "/api/workspace-launches", map[string]any{
			"name": "Gateway accounting success", "packageId": "basic", "autoRenew": false,
		}, "gateway-accounting-success", http.StatusAccepted)
		if stringValue(replay["operationId"]) != launchID {
			t.Fatalf("replay operation = %#v, want %s", replay, launchID)
		}
		if err := process.handler.app.runWorkspaceLaunchesOnce(context.Background(), process.handler.service); err != nil {
			t.Fatalf("replay worker: %v", err)
		}
		if afterReplay := fixture.writeCounts(t); afterReplay != beforeReplay || afterReplay.charges != 1 || afterReplay.refunds != 0 {
			t.Fatalf("Sub2API writes after replay = %#v, before %#v", afterReplay, beforeReplay)
		}

		assertGatewayAccountingReadback(t, api, fixture.client, ledgerClient, operation, gatewayAccountingInitialMicros-gatewayAccountingChargeMicros)
	})

	t.Run("response loss parks without false success", func(t *testing.T) {
		fixture := newGatewayAccountingSub2API(t, "owner-unknown@example.test", true)
		process := newGatewayAccountingControlPlane(t, ledger, fixture, "acct-gateway-unknown", "usr-gateway-unknown")
		api := process.login(t, fixture.ownerEmail, gatewayAccountingOwnerPassword)

		launch := api.mustRequest(t, http.MethodPost, "/api/workspace-launches", map[string]any{
			"name": "Gateway accounting unknown", "packageId": "basic", "autoRenew": false,
		}, "gateway-accounting-unknown", http.StatusAccepted)
		launchID := stringValue(launch["operationId"])
		operation := runGatewayAccountingLaunch(t, process, launchID, true)
		if operation.Status != "manual_review" || operation.Stage != "debit" {
			t.Fatalf("unknown debit terminal state = %s/%s", operation.Status, operation.Stage)
		}
		if counts := fixture.writeCounts(t); counts.charges != 1 || counts.refunds != 0 {
			t.Fatalf("unknown debit Sub2API writes = %#v", counts)
		}

		replay := api.mustRequest(t, http.MethodPost, "/api/workspace-launches", map[string]any{
			"name": "Gateway accounting unknown", "packageId": "basic", "autoRenew": false,
		}, "gateway-accounting-unknown", http.StatusAccepted)
		if stringValue(replay["operationId"]) != launchID || stringValue(replay["status"]) != "manual_review" {
			t.Fatalf("unknown replay response = %#v", replay)
		}
		if err := process.handler.app.runWorkspaceLaunchesOnce(context.Background(), process.handler.service); err != nil {
			t.Fatalf("manual-review worker replay: %v", err)
		}
		if counts := fixture.writeCounts(t); counts.charges != 1 || counts.refunds != 0 {
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

type gatewayAccountingSub2API struct {
	client         *clients.Sub2APIHTTPClient
	baseURL        string
	authorityToken string
	ownerEmail     string
	responseLoss   bool
	process        *exec.Cmd
	lossInjected   bool
	historyLoss    bool
	mu             sync.Mutex
}

func newGatewayAccountingSub2API(t *testing.T, ownerEmail string, responseLoss bool) *gatewayAccountingSub2API {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve integration test location")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../../../.."))
	statePath := filepath.Join(t.TempDir(), "authority-state.json")
	password := gatewayAccountingOwnerPassword
	userToken := "gateway-accounting-user-token-32-chars"
	authorityToken := "gateway-accounting-authority-token-32-chars"
	command := exec.Command("node", "--experimental-strip-types", filepath.Join(repositoryRoot, "tools", "local-sub2api-authority-fixture.ts"))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve Sub2API authority port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	command.Env = gatewayAccountingProcessEnv(os.Environ(), map[string]string{
		"OPL_QUALIFICATION_PORT":               strconv.Itoa(port),
		"OPL_QUALIFICATION_USER_EMAIL":         ownerEmail,
		"OPL_QUALIFICATION_USER_PASSWORD":      password,
		"OPL_QUALIFICATION_USER_TOKEN":         userToken,
		"OPL_QUALIFICATION_ADMIN_EMAIL":        gatewayAccountingAdminEmail,
		"OPL_QUALIFICATION_ADMIN_PASSWORD":     gatewayAccountingAdminPassword,
		"OPL_QUALIFICATION_ADMIN_TOKEN":        "gateway-accounting-admin-token-32-chars",
		"OPL_QUALIFICATION_AUTHORITY_TOKEN":    authorityToken,
		"OPL_QUALIFICATION_INITIAL_USD_MICROS": strconv.FormatInt(gatewayAccountingInitialMicros, 10),
		"OPL_QUALIFICATION_STATE_PATH":         statePath,
	})
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("open Sub2API authority stdout: %v", err)
	}
	var logs bytes.Buffer
	command.Stderr = &logs
	if err := command.Start(); err != nil {
		t.Fatalf("start Sub2API authority: %v", err)
	}
	ready := make(chan struct {
		origin string
		err    error
	}, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			var payload struct {
				Status string `json:"status"`
				Origin string `json:"origin"`
			}
			if json.Unmarshal(scanner.Bytes(), &payload) == nil && payload.Status == "READY" && payload.Origin != "" {
				ready <- struct {
					origin string
					err    error
				}{origin: payload.Origin}
				return
			}
		}
		ready <- struct {
			origin string
			err    error
		}{err: fmt.Errorf("Sub2API authority exited before READY: %s", strings.TrimSpace(logs.String()))}
	}()
	var origin string
	select {
	case result := <-ready:
		if result.err != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
			t.Fatal(result.err)
		}
		origin = result.origin
	case <-time.After(10 * time.Second):
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("Sub2API authority did not become ready: %s", strings.TrimSpace(logs.String()))
	}
	fixture := &gatewayAccountingSub2API{
		baseURL: origin, authorityToken: authorityToken, ownerEmail: ownerEmail, responseLoss: responseLoss, process: command,
	}
	t.Cleanup(func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
	})
	baseClient := &http.Client{Timeout: 5 * time.Second}
	baseClient.Transport = &gatewayAccountingFaultTransport{base: http.DefaultTransport, fixture: fixture}
	client, err := clients.NewSub2APIHTTPClient(clients.Sub2APIConfig{
		BaseURL: origin, AdminEmail: gatewayAccountingAdminEmail, AdminPassword: gatewayAccountingAdminPassword, Timeout: 3 * time.Second,
	}, baseClient)
	if err != nil {
		t.Fatal(err)
	}
	fixture.client = client
	return fixture
}

func (f *gatewayAccountingSub2API) writeCounts(t *testing.T) gatewayAccountingWriteCounts {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, f.baseURL+"/qualification/state", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+f.authorityToken)
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("read Sub2API authority state: %v", err)
	}
	defer response.Body.Close()
	var payload struct {
		Code json.Number `json:"code"`
		Data struct {
			WriteCounts struct {
				KeyCreates int `json:"keyCreates"`
				Debits     int `json:"debits"`
				Refunds    int `json:"refunds"`
			} `json:"writeCounts"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		t.Fatalf("decode Sub2API authority state: %v", err)
	}
	if response.StatusCode != http.StatusOK || payload.Code != json.Number("0") {
		t.Fatalf("Sub2API authority state status=%d code=%q", response.StatusCode, payload.Code)
	}
	return gatewayAccountingWriteCounts{charges: payload.Data.WriteCounts.Debits, refunds: payload.Data.WriteCounts.Refunds, keys: payload.Data.WriteCounts.KeyCreates}
}

type gatewayAccountingFaultTransport struct {
	base    http.RoundTripper
	fixture *gatewayAccountingSub2API
}

type gatewayAccountingRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip gatewayAccountingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
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
		ProviderProfileRef: "local-provider-profile", BindingRef: "local-preflight-" + stableID(input.LaunchOperationID)[:12], SpecDigest: strings.Repeat("a", 64),
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
		resources.RuntimeID, resources.RuntimeServiceName = "runtime-"+suffix, "opl-runtime-"+suffix
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
	account, user := provisionedAccountRowsFor(accountID, userID, sub2API.ownerEmail, gatewayAccountingSub2APIUserID)
	if err := store.CreateProvisionedAccount(context.Background(), account, user); err != nil {
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
	scopedLedger, ok := ledger.(clients.LedgerScopedReceiptClient)
	if !ok {
		t.Fatal("typed Ledger HTTP client does not support scoped receipt readback")
	}
	scopedReceipt, err := scopedLedger.ReceiptForAccount(context.Background(), accountID, workspaceID, receipt.ReceiptID)
	if err != nil || scopedReceipt.ReceiptID != receipt.ReceiptID || scopedReceipt.AccountID != accountID || scopedReceipt.WorkspaceID != workspaceID {
		t.Fatalf("Owner Delete scoped purchase receipt=%#v err=%v", scopedReceipt, err)
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
	projectedDetail := gatewayAccountingEnvelopeData(t, api.mustRequest(t, http.MethodGet, "/api/billing/receipts/"+receipt.ReceiptID, nil, "", http.StatusOK))
	if stringValue(projectedDetail["receiptId"]) != receipt.ReceiptID || stringValue(projectedDetail["workspaceId"]) != workspaceID || stringValue(projectedDetail["chargeReference"]) != code {
		t.Fatalf("Control Plane purchase receipt detail = %#v", projectedDetail)
	}
}

func assertGatewayAccountingCanonicalWorkspaceOpen(t *testing.T, api *gatewayAccountingAPI, operation workspaceLaunchReconcileOperation) {
	t.Helper()
	originalTransport := http.DefaultTransport
	upstreamCalls := 0
	upstreamMismatch := ""
	http.DefaultTransport = gatewayAccountingRoundTripper(func(request *http.Request) (*http.Response, error) {
		upstreamCalls++
		if request.URL.Host != operation.stringFact("runtimeServiceName")+":3000" || request.URL.Path != "/" || request.Host != operation.stringFact("runtimeServiceName")+":3000" {
			upstreamMismatch = fmt.Sprintf("Workspace open upstream request = %s host=%q", request.URL.String(), request.Host)
		}
		for _, header := range []string{"Authorization", "Cookie", "X-OPL-CSRF", "X-OPL-CSRF-Token"} {
			if value := request.Header.Get(header); value != "" {
				upstreamMismatch = fmt.Sprintf("Workspace open forwarded %s=%q", header, value)
			}
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/plain"}, "Set-Cookie": []string{"upstream=forbidden"}},
			Body:       io.NopCloser(strings.NewReader("workspace-open")),
			Request:    request,
		}, nil
	})
	defer func() { http.DefaultTransport = originalTransport }()

	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, api.baseURL+"/w/"+operation.stringFact("workspaceId")+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer platform-session")
	request.Header.Set("X-OPL-CSRF", api.csrf)
	response, err := api.client.Do(request)
	if err != nil {
		t.Fatalf("canonical Workspace open: %v", err)
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 1024))
	if readErr != nil || response.StatusCode != http.StatusOK || string(body) != "workspace-open" || upstreamCalls != 1 || upstreamMismatch != "" {
		t.Fatalf("canonical Workspace open status=%d body=%q calls=%d mismatch=%q err=%v", response.StatusCode, body, upstreamCalls, upstreamMismatch, readErr)
	}
	for _, cookie := range response.Cookies() {
		if cookie.Name == "upstream" {
			t.Fatalf("Workspace open forwarded upstream cookie: %#v", cookie)
		}
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
