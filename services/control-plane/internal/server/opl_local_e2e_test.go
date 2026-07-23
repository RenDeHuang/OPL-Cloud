//go:build local_e2e

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

const localE2ESub2APIBaseURL = "http://127.0.0.1:8080"

type localE2ETraffic struct {
	mu                 sync.Mutex
	localRequests      int
	localWrites        int
	productionRequests int
	productionWrites   int
}

func (c *localE2ETraffic) record(request *http.Request) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	write := request.Method != http.MethodGet && request.Method != http.MethodHead && request.Method != http.MethodOptions
	if strings.EqualFold(request.URL.Hostname(), "gflabtoken.cn") {
		c.productionRequests++
		if write {
			c.productionWrites++
		}
		return errors.New("production Sub2API is forbidden in local E2E")
	}
	if request.URL.Scheme != "http" || request.URL.Hostname() != "127.0.0.1" || request.URL.Port() != "8080" {
		return errors.New("local E2E Sub2API request escaped the localhost boundary")
	}
	c.localRequests++
	if write {
		c.localWrites++
	}
	return nil
}

func (c *localE2ETraffic) snapshot() (int, int, int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.localRequests, c.localWrites, c.productionRequests, c.productionWrites
}

type localE2EFaultTransport struct {
	base    http.RoundTripper
	traffic *localE2ETraffic
	fault   string

	mu                   sync.Mutex
	redeemFaulted        bool
	failNextHistoryRead  bool
	redeemIdempotencyIDs []string
}

func (t *localE2EFaultTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := t.traffic.record(request); err != nil {
		return nil, err
	}
	isRedeem := request.Method == http.MethodPost && request.URL.Path == "/api/v1/admin/redeem-codes/create-and-redeem"
	isHistory := request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/balance-history")

	t.mu.Lock()
	if isRedeem {
		t.redeemIdempotencyIDs = append(t.redeemIdempotencyIDs, request.Header.Get("Idempotency-Key"))
	}
	fault := ""
	if isRedeem && !t.redeemFaulted && t.fault != "" {
		t.redeemFaulted = true
		fault = t.fault
		if fault == "response_loss" {
			t.failNextHistoryRead = true
		}
	} else if isHistory && t.failNextHistoryRead {
		t.failNextHistoryRead = false
		fault = "history_read_loss"
	}
	t.mu.Unlock()

	switch fault {
	case "409":
		return localE2EHTTPFailure(request, http.StatusConflict, "redeem_conflict", "req-local-409"), nil
	case "503":
		return localE2EHTTPFailure(request, http.StatusServiceUnavailable, "gateway_busy", "req-local-503"), nil
	case "timeout":
		return nil, context.DeadlineExceeded
	case "history_read_loss":
		return nil, io.ErrUnexpectedEOF
	case "response_loss":
		response, err := t.base.RoundTrip(request)
		if err != nil {
			return nil, err
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		return nil, io.ErrUnexpectedEOF
	default:
		return t.base.RoundTrip(request)
	}
}

func (t *localE2EFaultTransport) redeemIdentities() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.redeemIdempotencyIDs...)
}

func (t *localE2EFaultTransport) configureFault(fault string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.fault = fault
	t.redeemFaulted = false
	t.failNextHistoryRead = false
	t.redeemIdempotencyIDs = nil
}

func TestLocalE2EFaultTransportCanSwitchScenariosWithoutNewClient(t *testing.T) {
	traffic := &localE2ETraffic{}
	transport := &localE2EFaultTransport{base: http.DefaultTransport, traffic: traffic}
	for _, scenario := range []struct {
		fault  string
		status int
	}{{"409", http.StatusConflict}, {"503", http.StatusServiceUnavailable}} {
		transport.configureFault(scenario.fault)
		request := httptest.NewRequest(http.MethodPost, localE2ESub2APIBaseURL+"/api/v1/admin/redeem-codes/create-and-redeem", nil)
		request.Header.Set("Idempotency-Key", "wallet-recovery-test")
		response, err := transport.RoundTrip(request)
		if err != nil || response.StatusCode != scenario.status {
			t.Fatalf("fault=%s status=%v err=%v", scenario.fault, response, err)
		}
		_ = response.Body.Close()
		if identities := transport.redeemIdentities(); len(identities) != 1 || identities[0] != "wallet-recovery-test" {
			t.Fatalf("fault=%s identities=%#v", scenario.fault, identities)
		}
	}
}

func localE2EHTTPFailure(request *http.Request, status int, code, requestID string) *http.Response {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	header.Set("X-Request-ID", requestID)
	body := `{"code":"` + code + `","message":"local-e2e-redacted"}`
	return &http.Response{
		StatusCode: status,
		Status:     strconv.Itoa(status) + " " + http.StatusText(status),
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

type localE2ELedger struct {
	fakeLedgerClient
	mu      sync.Mutex
	byKey   map[string]clients.Receipt
	byID    map[string]clients.Receipt
	ordered []string
}

func newLocalE2ELedger() *localE2ELedger {
	return &localE2ELedger{byKey: map[string]clients.Receipt{}, byID: map[string]clients.Receipt{}}
}

func (l *localE2ELedger) RecordReceipt(_ context.Context, input clients.ReceiptInput, key string) (clients.Receipt, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if receipt, ok := l.byKey[key]; ok {
		receipt.Replayed = true
		return receipt, nil
	}
	receipt := clients.Receipt{
		ReceiptInput: input,
		ReceiptID:    "receipt-local-" + stableID(key)[:16],
		CreatedAt:    time.Now().UTC().Format(time.RFC3339Nano),
	}
	l.byKey[key], l.byID[receipt.ReceiptID] = receipt, receipt
	l.ordered = append(l.ordered, receipt.ReceiptID)
	return receipt, nil
}

func (l *localE2ELedger) Receipt(_ context.Context, receiptID string) (clients.Receipt, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	receipt, ok := l.byID[receiptID]
	if !ok {
		return clients.Receipt{}, errors.New("receipt not found")
	}
	return receipt, nil
}

func (l *localE2ELedger) ListReceipts(_ context.Context, query clients.ReceiptQuery) (clients.ReceiptPage, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	receipts := make([]clients.Receipt, 0, len(l.ordered))
	for i := len(l.ordered) - 1; i >= 0; i-- {
		receipt := l.byID[l.ordered[i]]
		if query.AccountID == "" || receipt.AccountID == query.AccountID {
			receipts = append(receipts, receipt)
		}
	}
	return clients.ReceiptPage{Receipts: receipts}, nil
}

func (l *localE2ELedger) receiptsFor(accountID string) []clients.Receipt {
	page, _ := l.ListReceipts(context.Background(), clients.ReceiptQuery{AccountID: accountID})
	return page.Receipts
}

type localE2EProcess struct {
	server  *httptest.Server
	handler *controlPlaneHTTPHandler
	store   StateStore
	close   sync.Once
}

func startLocalE2EProcess(t *testing.T, databaseURL string, service *controlplane.Service) *localE2EProcess {
	t.Helper()
	store, err := newTestPostgresEntStateStore(databaseURL)
	if err != nil {
		t.Fatalf("start local E2E PostgreSQL store: %v", err)
	}
	handler, err := NewPersistentServer(service, store)
	if err != nil {
		_ = store.(*postgresEntStateStore).client.Close()
		t.Fatalf("start local E2E Control Plane: %v", err)
	}
	typed, ok := handler.(*controlPlaneHTTPHandler)
	if !ok {
		t.Fatal("local E2E Control Plane handler type mismatch")
	}
	process := &localE2EProcess{server: httptest.NewTLSServer(handler), handler: typed, store: store}
	t.Cleanup(process.Close)
	return process
}

func (p *localE2EProcess) Close() {
	p.close.Do(func() {
		p.server.Close()
		_ = p.store.(*postgresEntStateStore).client.Close()
	})
}

type localE2EAPI struct {
	baseURL string
	client  *http.Client
	csrf    string
}

type localE2EResponse struct {
	status int
	header http.Header
	body   any
}

func (p *localE2EProcess) newAPI(t *testing.T) *localE2EAPI {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := *p.server.Client()
	client.Jar = jar
	client.Timeout = 15 * time.Second
	return &localE2EAPI{baseURL: p.server.URL, client: &client}
}

func (a *localE2EAPI) request(ctx context.Context, method, path string, input any, idempotencyKey string) (localE2EResponse, error) {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return localE2EResponse{}, err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, a.baseURL+path, body)
	if err != nil {
		return localE2EResponse{}, err
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if a.csrf != "" && method != http.MethodGet && method != http.MethodHead && path != "/api/auth/login" {
		request.Header.Set("x-opl-csrf", a.csrf)
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response, err := a.client.Do(request)
	if err != nil {
		return localE2EResponse{}, err
	}
	defer response.Body.Close()
	limited, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return localE2EResponse{}, err
	}
	var payload any
	if len(bytes.TrimSpace(limited)) != 0 {
		if err := json.Unmarshal(limited, &payload); err != nil {
			return localE2EResponse{}, errors.New("invalid local E2E JSON response")
		}
	}
	return localE2EResponse{status: response.StatusCode, header: response.Header.Clone(), body: payload}, nil
}

func (a *localE2EAPI) login(t *testing.T, email, password string) {
	t.Helper()
	response := localE2EMustRequest(t, a, http.MethodPost, "/api/auth/login", map[string]any{"email": email, "password": password}, "", http.StatusOK)
	a.csrf = response.header.Get("x-opl-csrf-token")
	if a.csrf == "" || stringValue(localE2EMap(t, response.body, "login")["csrfToken"]) != a.csrf {
		t.Fatal("local E2E login did not return a consistent CSRF token")
	}
}

func localE2EMustRequest(t *testing.T, api *localE2EAPI, method, path string, input any, key string, statuses ...int) localE2EResponse {
	t.Helper()
	response, err := api.request(context.Background(), method, path, input, key)
	if err != nil {
		t.Fatalf("local E2E request %s %s: %v", method, path, err)
	}
	for _, status := range statuses {
		if response.status == status {
			return response
		}
	}
	if body, ok := response.body.(map[string]any); ok {
		t.Fatalf("local E2E request %s %s status=%d code=%q error=%q message=%q", method, path, response.status, stringValue(body["code"]), stringValue(body["error"]), stringValue(body["message"]))
	}
	t.Fatalf("local E2E request %s %s status=%d", method, path, response.status)
	return localE2EResponse{}
}

func localE2EMap(t *testing.T, value any, label string) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("local E2E %s is not an object", label)
	}
	return result
}

func localE2EData(t *testing.T, response localE2EResponse, label string) map[string]any {
	t.Helper()
	body := localE2EMap(t, response.body, label)
	if body["available"] != true || body["status"] == "unavailable" {
		t.Fatalf("local E2E %s source is unavailable", label)
	}
	return localE2EMap(t, body["data"], label+" data")
}

func localE2EItems(t *testing.T, data map[string]any, label string) []any {
	t.Helper()
	items, ok := data["items"].([]any)
	if !ok {
		t.Fatalf("local E2E %s items are invalid", label)
	}
	return items
}

func localE2EItemBy(t *testing.T, items []any, field, value, label string) map[string]any {
	t.Helper()
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if ok && stringValue(item[field]) == value {
			return item
		}
	}
	t.Fatalf("local E2E %s item %s=%s not found", label, field, value)
	return nil
}

func localE2EDatabase(t *testing.T) string {
	t.Helper()
	admin := openControlPlaneTestPostgres(t)
	database := "opl_local_e2e_" + strconv.FormatInt(time.Now().UTC().UnixNano(), 10)
	if _, err := admin.Exec(`CREATE DATABASE "` + database + `"`); err != nil {
		_ = admin.Close()
		t.Fatalf("create local E2E database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1`, database)
		_, _ = admin.Exec(`DROP DATABASE "` + database + `"`)
		_ = admin.Close()
	})
	return controlPlaneTestPostgresURL(t, database, "")
}

func newLocalE2ESub2API(t *testing.T, traffic *localE2ETraffic, fault string) (*clients.Sub2APIHTTPClient, *localE2EFaultTransport) {
	t.Helper()
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("OPL_SUB2API_BASE_URL")), "/")
	if baseURL != localE2ESub2APIBaseURL {
		t.Fatalf("OPL_SUB2API_BASE_URL must be %s for local E2E", localE2ESub2APIBaseURL)
	}
	adminEmail := strings.TrimSpace(os.Getenv("OPL_SUB2API_ADMIN_EMAIL"))
	adminPassword := os.Getenv("OPL_SUB2API_ADMIN_PASSWORD")
	if adminEmail != "admin@medopl.cn" || adminPassword == "" {
		t.Fatal("local E2E Sub2API admin credentials are missing or incompatible with the reserved admin mapping")
	}
	transport := &localE2EFaultTransport{base: http.DefaultTransport, traffic: traffic, fault: fault}
	client, err := clients.NewSub2APIHTTPClient(clients.Sub2APIConfig{
		BaseURL: baseURL, AdminEmail: adminEmail, AdminPassword: adminPassword, Timeout: 5 * time.Second,
	}, &http.Client{Transport: transport, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return client, transport
}

type localE2EUser struct {
	accountID    string
	email        string
	password     string
	sub2APIUser  int64
	operationID  string
	recoveryKey  string
	adjustmentID string
}

func localE2EUserIdentity(prefix string) (string, string) {
	suffix := strconv.FormatInt(time.Now().UTC().UnixNano(), 36)
	return prefix + "-" + suffix + "@example.com", "Local-E2E-" + suffix + "-Aa1!"
}

func provisionLocalE2EUser(t *testing.T, admin *localE2EAPI, prefix string) localE2EUser {
	t.Helper()
	email, password := localE2EUserIdentity(prefix)
	response := localE2EMustRequest(t, admin, http.MethodPost, "/api/operator/accounts", map[string]any{
		"email": email, "password": password, "name": "Local E2E",
	}, "account-provision-"+prefix+"-"+strconv.FormatInt(time.Now().UnixNano(), 10), http.StatusCreated)
	body := localE2EMap(t, response.body, "account provision")
	accountID := stringValue(body["accountId"])
	if accountID == "" || stringValue(body["status"]) != "succeeded" {
		t.Fatal("local E2E account provisioning did not return a mapped account")
	}
	return localE2EUser{accountID: accountID, email: email, password: password}
}

func loginLocalE2EUser(t *testing.T, process *localE2EProcess, user *localE2EUser) *localE2EAPI {
	t.Helper()
	api := process.newAPI(t)
	api.login(t, user.email, user.password)
	auth := localE2EData(t, localE2EMustRequest(t, api, http.MethodGet, "/api/auth/me", nil, "", http.StatusOK), "auth me")
	parsed, err := strconv.ParseInt(stringValue(auth["sub2apiUserId"]), 10, 64)
	if err != nil || parsed <= 0 || stringValue(auth["accountId"]) != user.accountID || normalizeEmail(stringValue(auth["email"])) != normalizeEmail(user.email) {
		t.Fatal("local E2E user mapping is not one-to-one")
	}
	user.sub2APIUser = parsed
	return api
}

func localE2EWalletMicros(t *testing.T, api *localE2EAPI) int64 {
	t.Helper()
	data := localE2EData(t, localE2EMustRequest(t, api, http.MethodGet, "/api/gateway/wallet", nil, "", http.StatusOK), "wallet")
	value := numberField(data, "usdMicros", -1)
	if value < 0 || value != float64(int64(value)) {
		t.Fatal("local E2E wallet returned an invalid amount")
	}
	return int64(value)
}

func localE2EStringItems(t *testing.T, value any, label string) []string {
	t.Helper()
	raw, ok := value.([]any)
	if !ok {
		t.Fatalf("local E2E %s is not a list", label)
	}
	items := make([]string, 0, len(raw))
	for _, item := range raw {
		text, ok := item.(string)
		if !ok {
			t.Fatalf("local E2E %s contains a non-string", label)
		}
		items = append(items, text)
	}
	return items
}

type localE2EFullEvidence struct {
	user            localE2EUser
	walletOperation string
	generalKeyID    string
	generalKeyGroup string
	workspaceID     string
	launchOperation string
	receiptID       string
	computeID       string
	computeProvider string
	storageID       string
	storageProvider string
	attachmentID    string
	runtimeID       string
}

func runLocalE2EFullFlow(t *testing.T, process *localE2EProcess, adminEmail, adminPassword string, sub2API *clients.Sub2APIHTTPClient, fabric *monthlyFabric, ledger *localE2ELedger) localE2EFullEvidence {
	t.Helper()
	admin := process.newAPI(t)
	admin.login(t, adminEmail, adminPassword)
	user := provisionLocalE2EUser(t, admin, "opl-full")
	owner := loginLocalE2EUser(t, process, &user)
	if balance := localE2EWalletMicros(t, owner); balance != 0 {
		t.Fatalf("local E2E starting balance=%d, want 0", balance)
	}
	endpoint := localE2EData(t, localE2EMustRequest(t, owner, http.MethodGet, "/api/gateway/endpoint", nil, "", http.StatusOK), "gateway endpoint")
	if stringValue(endpoint["baseUrl"]) != localE2ESub2APIBaseURL+"/v1" {
		t.Fatal("local E2E Gateway endpoint did not use the local Sub2API origin")
	}
	groups := localE2EData(t, localE2EMustRequest(t, owner, http.MethodGet, "/api/gateway/groups", nil, "", http.StatusOK), "gateway groups")
	groupItems := localE2EItems(t, groups, "gateway groups")
	if len(groupItems) == 0 {
		t.Fatal("local E2E Sub2API returned no available Key groups")
	}
	groupID := stringValue(localE2EMap(t, groupItems[0], "gateway group")["id"])
	if parsed, err := strconv.ParseInt(groupID, 10, 64); err != nil || parsed <= 0 {
		t.Fatal("local E2E Gateway group ID is invalid")
	}

	adjustmentKey := "wallet-local-full-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	adjustmentPath := "/api/operator/accounts/" + user.accountID + "/wallet-adjustments"
	adjustmentBody := map[string]any{
		"kind": "recharge", "amountUsd": "60.00", "reason": "local Stage B credit", "confirmationAccountId": user.accountID,
	}
	start := make(chan struct{})
	responses := make(chan localE2EResponse, 8)
	errorsOut := make(chan error, 8)
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			response, err := admin.request(context.Background(), http.MethodPost, adjustmentPath, adjustmentBody, adjustmentKey)
			if err != nil {
				errorsOut <- err
				return
			}
			responses <- response
		}()
	}
	close(start)
	workers.Wait()
	close(responses)
	close(errorsOut)
	for err := range errorsOut {
		if err != nil {
			t.Fatalf("concurrent wallet adjustment: %v", err)
		}
	}
	operationID := ""
	for response := range responses {
		if response.status != http.StatusCreated && response.status != http.StatusOK {
			t.Fatalf("concurrent wallet adjustment status=%d", response.status)
		}
		candidate := stringValue(localE2EMap(t, response.body, "wallet adjustment")["operationId"])
		if operationID == "" {
			operationID = candidate
		}
		if candidate == "" || candidate != operationID {
			t.Fatal("concurrent wallet adjustment returned different operations")
		}
	}
	if balance := localE2EWalletMicros(t, owner); balance != 60_000_000 {
		t.Fatalf("local E2E recharged balance=%d, want 60000000", balance)
	}
	localE2EMustRequest(t, admin, http.MethodPost, adjustmentPath, adjustmentBody, adjustmentKey, http.StatusOK)
	if balance := localE2EWalletMicros(t, owner); balance != 60_000_000 {
		t.Fatalf("local E2E replay balance=%d, want 60000000", balance)
	}
	history, err := sub2API.BalanceHistory(context.Background(), user.sub2APIUser)
	if err != nil {
		t.Fatal(err)
	}
	if countLocalE2EHistory(history, walletAdjustmentRedeemCode(operationID), 60_000_000) != 1 {
		t.Fatal("local E2E concurrent recharge did not produce exactly one authoritative funds effect")
	}

	createKeyIntent := "gateway-key-local-full-" + user.accountID
	createKeyBody := map[string]any{
		"name": "local-stage-b", "groupId": groupID,
		"ipWhitelist": []string{"127.0.0.1"}, "ipBlacklist": []string{"198.51.100.0/24"},
		"quotaUsdMicros": int64(1_250_000), "expiresInDays": 30,
		"rateLimit5hUsdMicros": int64(5_000_000), "rateLimit1dUsdMicros": int64(10_000_000), "rateLimit7dUsdMicros": int64(20_000_000),
	}
	created := localE2EMustRequest(t, owner, http.MethodPost, "/api/gateway/keys", createKeyBody, createKeyIntent, http.StatusCreated)
	createdJSON, _ := json.Marshal(created.body)
	if bytes.Contains(createdJSON, []byte(`"value"`)) || bytes.Contains(createdJSON, []byte(`"key"`)) {
		t.Fatal("gateway Key create response exposed plaintext")
	}
	keyData := localE2EData(t, created, "gateway key create")
	keyID := stringValue(keyData["id"])
	if keyID == "" || stringValue(keyData["groupId"]) != groupID || stringValue(keyData["status"]) != "active" ||
		int64(numberField(keyData, "quotaUsdMicros", -1)) != 1_250_000 || int64(numberField(keyData, "rateLimit7dUsdMicros", -1)) != 20_000_000 ||
		!slices.Equal(localE2EStringItems(t, keyData["ipWhitelist"], "created Key allowlist"), []string{"127.0.0.1"}) ||
		!slices.Equal(localE2EStringItems(t, keyData["ipBlacklist"], "created Key denylist"), []string{"198.51.100.0/24"}) {
		t.Fatal("gateway Key create did not return the authoritative parity fields")
	}
	replayedCreate := localE2EMustRequest(t, owner, http.MethodPost, "/api/gateway/keys", createKeyBody, createKeyIntent, http.StatusOK)
	if stringValue(localE2EData(t, replayedCreate, "gateway key create replay")["id"]) != keyID {
		t.Fatal("gateway Key create replay produced a different identity")
	}
	updated := localE2EMustRequest(t, owner, http.MethodPatch, "/api/gateway/keys/"+keyID, map[string]any{
		"name": "local-stage-b-edited", "groupId": groupID, "enabled": false,
		"ipWhitelist": []string{"127.0.0.1", "192.0.2.10"}, "ipBlacklist": []string{"203.0.113.0/24"},
		"quotaUsdMicros": int64(2_500_000), "expiresAt": "2099-01-01T00:00:00Z",
		"rateLimit5hUsdMicros": int64(6_000_000), "rateLimit1dUsdMicros": int64(12_000_000), "rateLimit7dUsdMicros": int64(24_000_000),
		"resetQuota": true, "resetRateLimitUsage": true,
	}, "gateway-key-update-local-full-"+user.accountID, http.StatusOK)
	updatedData := localE2EData(t, updated, "gateway key update")
	if stringValue(updatedData["id"]) != keyID || stringValue(updatedData["name"]) != "local-stage-b-edited" || stringValue(updatedData["status"]) != "disabled" ||
		int64(numberField(updatedData, "quotaUsdMicros", -1)) != 2_500_000 || int64(numberField(updatedData, "rateLimit1dUsdMicros", -1)) != 12_000_000 ||
		int64(numberField(updatedData, "quotaUsedUsdMicros", -1)) != 0 || int64(numberField(updatedData, "usage7dUsdMicros", -1)) != 0 {
		t.Fatal("gateway Key update did not return the authoritative parity fields")
	}
	enabled := localE2EMustRequest(t, owner, http.MethodPatch, "/api/gateway/keys/"+keyID, map[string]any{"enabled": true}, "gateway-key-enable-local-full-"+user.accountID, http.StatusOK)
	if stringValue(localE2EData(t, enabled, "gateway key enable")["status"]) != "active" {
		t.Fatal("gateway Key enable did not converge")
	}
	revealed := localE2EMustRequest(t, owner, http.MethodPost, "/api/gateway/keys/"+keyID+"/reveal", map[string]any{}, "", http.StatusOK)
	cacheControl := strings.ToLower(revealed.header.Get("Cache-Control"))
	if !strings.Contains(cacheControl, "private") || !strings.Contains(cacheControl, "no-store") {
		t.Fatal("gateway Key reveal response is cacheable")
	}
	revealData := localE2EData(t, revealed, "gateway key reveal")
	plaintext := stringValue(revealData["value"])
	if plaintext == "" {
		t.Fatal("gateway Key reveal did not return a copyable value")
	}
	listed := localE2EMustRequest(t, owner, http.MethodGet, "/api/gateway/keys?page=1&pageSize=10&search=local-stage-b-edited&status=active&groupId="+groupID+"&sortBy=name&sortOrder=asc", nil, "", http.StatusOK)
	listedJSON, _ := json.Marshal(listed.body)
	if bytes.Contains(listedJSON, []byte(plaintext)) || bytes.Contains(listedJSON, []byte(`"value"`)) {
		t.Fatal("gateway Key list leaked plaintext")
	}
	listedData := localE2EData(t, listed, "gateway keys")
	if int64(numberField(listedData, "total", -1)) != 1 || int64(numberField(listedData, "pages", -1)) != 1 {
		t.Fatal("gateway Key filtered pagination is not authoritative")
	}
	localE2EItemBy(t, localE2EItems(t, listedData, "gateway keys"), "id", keyID, "gateway key")
	delete(revealData, "value")
	plaintext = ""

	catalog := localE2EMap(t, localE2EMustRequest(t, owner, http.MethodGet, "/api/pricing/catalog", nil, "", http.StatusOK).body, "pricing catalog")
	packages, ok := catalog["packages"].([]any)
	if !ok {
		t.Fatal("pricing catalog packages are invalid")
	}
	for _, packageID := range []string{"basic", "pro"} {
		item := localE2EItemBy(t, packages, "id", packageID, "pricing package")
		if item["available"] != true {
			t.Fatalf("pricing package %s is unavailable", packageID)
		}
	}
	for _, preview := range []struct {
		packageID string
		sizeGB    int
		total     int64
	}{{"basic", 10, 52_580_000}, {"pro", 100, 240_080_000}} {
		body := localE2EMap(t, localE2EMustRequest(t, owner, http.MethodPost, "/api/pricing/preview", map[string]any{
			"resourceType": "workspace", "packageId": preview.packageID, "sizeGb": preview.sizeGB,
		}, "", http.StatusOK).body, "pricing preview")
		if int64(numberField(body, "totalChargeUsdMicros", -1)) != preview.total {
			t.Fatalf("pricing preview %s total is wrong", preview.packageID)
		}
	}

	launch := localE2EMustRequest(t, owner, http.MethodPost, "/api/workspace-launches", map[string]any{
		"name": "Local Basic", "packageId": "basic", "sizeGb": 10, "autoRenew": false,
	}, "workspace-launch-local-full", http.StatusAccepted)
	launchBody := localE2EMap(t, launch.body, "workspace launch")
	launchID := stringValue(launchBody["operationId"])
	if launchID == "" || stringValue(launchBody["packageId"]) != "basic" {
		t.Fatal("local E2E Basic launch was not accepted")
	}
	operation := localE2EWorkspaceLaunch(t, process.store, launchID)
	configureLocalE2EFabric(fabric, operation)
	for range 8 {
		if err := process.handler.app.runWorkspaceLaunchesOnce(context.Background(), process.handler.service); err != nil {
			t.Fatalf("run local E2E Workspace worker: %v", err)
		}
		operation = localE2EWorkspaceLaunch(t, process.store, launchID)
		if operation.Status == "succeeded" {
			break
		}
	}
	if operation.Status != "succeeded" || operation.Phase != "succeeded" {
		t.Fatalf("local E2E Workspace launch ended in %s/%s", operation.Status, operation.Phase)
	}
	if balance := localE2EWalletMicros(t, owner); balance != 7_420_000 {
		t.Fatalf("local E2E post-Basic balance=%d, want 7420000", balance)
	}
	history, err = sub2API.BalanceHistory(context.Background(), user.sub2APIUser)
	if err != nil {
		t.Fatal(err)
	}
	if countLocalE2EHistory(history, operation.RedeemCode, -52_580_000) != 1 {
		t.Fatal("local E2E Basic did not produce exactly one authoritative debit")
	}

	workspaceData := localE2EData(t, localE2EMustRequest(t, owner, http.MethodGet, "/api/workspaces", nil, "", http.StatusOK), "workspaces")
	workspace := localE2EItemBy(t, localE2EItems(t, workspaceData, "workspaces"), "id", operation.WorkspaceID, "workspace")
	if stringValue(workspace["state"]) != "running" || stringValue(workspace["renewalStatus"]) != "active" || int64(numberField(workspace, "totalUsdMicros", -1)) != 52_580_000 || stringValue(workspace["paidThrough"]) == "" {
		t.Fatal("local E2E owner Workspace projection is incomplete")
	}
	runtime := localE2EData(t, localE2EMustRequest(t, owner, http.MethodGet, "/api/workspaces/"+operation.WorkspaceID+"/runtime-status", nil, "", http.StatusOK), "runtime status")
	if runtime["ready"] != true || stringValue(runtime["status"]) != "running" {
		t.Fatal("local E2E owner Runtime projection is not ready")
	}
	receiptPage := localE2EData(t, localE2EMustRequest(t, owner, http.MethodGet, "/api/billing/receipts", nil, "", http.StatusOK), "billing receipts")
	receipts, ok := receiptPage["receipts"].([]any)
	if !ok {
		t.Fatal("local E2E billing receipt list is invalid")
	}
	receipt := localE2EItemBy(t, receipts, "receiptId", operation.ReceiptID, "billing receipt")
	if stringValue(receipt["type"]) != "billing.workspace_purchased.v1" || int64(numberField(receipt, "totalUsdMicros", -1)) != 52_580_000 || stringValue(receipt["periodStart"]) == "" || stringValue(receipt["paidThrough"]) == "" {
		t.Fatal("local E2E billing receipt is incomplete")
	}

	accounts := localE2EData(t, localE2EMustRequest(t, admin, http.MethodGet, "/api/operator/accounts?page=1&pageSize=50", nil, "", http.StatusOK), "operator accounts")
	account := localE2EItemBy(t, localE2EItems(t, accounts, "operator accounts"), "accountId", user.accountID, "operator account")
	if stringValue(account["email"]) != normalizeEmail(user.email) {
		t.Fatal("operator account projection does not show the provisioned user")
	}
	ownerKeys := localE2EData(t, localE2EMustRequest(t, owner, http.MethodGet, "/api/gateway/keys?page=1&pageSize=100", nil, "", http.StatusOK), "operator Key count readback")
	keyCount := localE2EMap(t, account["keyCount"], "operator Key count")
	if keyCount["available"] != true || stringValue(keyCount["source"]) != "sub2api" ||
		int64(numberField(keyCount, "data", -1)) != int64(numberField(ownerKeys, "total", -2)) {
		t.Fatal("operator account Key count is not the authoritative Sub2API readback")
	}
	adjustment := localE2EMap(t, localE2EMustRequest(t, admin, http.MethodGet, "/api/operator/wallet-adjustments/"+operationID, nil, "", http.StatusOK).body, "operator wallet adjustment")
	if stringValue(adjustment["status"]) != "succeeded" || stringValue(adjustment["receiptId"]) == "" {
		t.Fatal("operator wallet adjustment projection is incomplete")
	}
	operatorWorkspace := localE2EData(t, localE2EMustRequest(t, admin, http.MethodGet, "/api/operator/workspaces/"+operation.WorkspaceID, nil, "", http.StatusOK), "operator workspace")
	operatorReceipt := localE2EMap(t, operatorWorkspace["receipt"], "operator workspace receipt")
	if operatorReceipt["available"] != true || stringValue(localE2EMap(t, operatorReceipt["data"], "operator receipt data")["receiptId"]) != operation.ReceiptID {
		t.Fatal("operator Workspace detail does not expose the authoritative purchase receipt")
	}
	resources, ok := operatorWorkspace["resources"].([]any)
	if !ok || len(resources) != 3 {
		t.Fatal("operator Workspace detail does not expose Fabric resources")
	}
	health := localE2EData(t, localE2EMustRequest(t, admin, http.MethodGet, "/api/operator/health", nil, "", http.StatusOK), "operator health")
	for _, source := range []string{"gateway", "fabric", "ledger"} {
		envelope := localE2EMap(t, health[source], "operator health "+source)
		if envelope["available"] != true {
			t.Fatalf("operator health source %s is unavailable", source)
		}
	}

	rows, err := process.store.ListRuntimeOperations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	persisted, _ := json.Marshal(rows)
	for _, secret := range fabric.gatewaySecretInputs {
		if secret.GatewayAPIKey == "" || bytes.Contains(persisted, []byte(secret.GatewayAPIKey)) {
			t.Fatal("Workspace Key crossed the authorized fake Fabric Secret boundary")
		}
	}
	if len(fabric.computeIDs) != 1 || len(fabric.storageIDs) != 1 || countStrings(*fabric.events, "fabric.attachment") != 1 || countStrings(*fabric.events, "fabric.runtime") != 1 {
		t.Fatal("local E2E fake Fabric mutation cardinality is invalid")
	}
	localE2EAssertLedger(t, ledger.receiptsFor(user.accountID), operation.ReceiptID)

	return localE2EFullEvidence{
		user: user, walletOperation: operationID, generalKeyID: keyID, generalKeyGroup: groupID, workspaceID: operation.WorkspaceID,
		launchOperation: launchID, receiptID: operation.ReceiptID, computeID: operation.ComputeID,
		computeProvider: fabric.computeSync.ProviderResourceID, storageID: operation.StorageID,
		storageProvider: fabric.storageSync.ProviderResourceID, attachmentID: operation.AttachmentID, runtimeID: operation.RuntimeID,
	}
}

func countLocalE2EHistory(history []clients.Sub2APIBalanceHistoryEntry, code string, amount int64) int {
	count := 0
	for _, entry := range history {
		if entry.Code == code && entry.Type == "balance" && entry.Status == "used" && entry.ValueUSDMicros == amount && entry.UsedAt != nil {
			count++
		}
	}
	return count
}

func localE2EWorkspaceLaunch(t *testing.T, store StateStore, operationID string) workspaceLaunchOperation {
	t.Helper()
	rows, err := store.ListRuntimeOperations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	operation, err := decodeWorkspaceLaunchOperation(findRecord(rows, operationID))
	if err != nil {
		t.Fatalf("decode local E2E Workspace launch: %v", err)
	}
	return operation
}

func configureLocalE2EFabric(fabric *monthlyFabric, operation workspaceLaunchOperation) {
	fabric.computeSync = clients.ComputeAllocation{
		ID: operation.ComputeID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, PackageID: operation.PackageID,
		Status: "running", Provider: "local-fake-tencent", ProviderResourceID: "ins-" + operation.ComputeID,
		ProviderRequestID: "req-" + operation.ComputeID, InstanceID: "ins-" + operation.ComputeID,
		InstanceType: "S5.MEDIUM4", Zone: "ap-shanghai-2", ChargeType: "PREPAID",
		RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2099-01-01T00:00:00Z",
		ProviderData: map[string]string{"zone": "ap-shanghai-2", "instanceType": "S5.MEDIUM4"},
	}
	fabric.storageSync = clients.StorageVolume{
		ID: operation.StorageID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID,
		Status: "available", Provider: "local-fake-tencent", ProviderResourceID: "disk-" + operation.StorageID,
		ProviderRequestID: "req-" + operation.StorageID, SizeGB: operation.StorageGB, CBSStatus: "UNATTACHED",
		DiskType: "CLOUD_PREMIUM", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2099-01-01T00:00:00Z",
		Zone: "ap-shanghai-2", ProviderData: map[string]string{"chargeType": "PREPAID"},
	}
}

func localE2EAssertLedger(t *testing.T, receipts []clients.Receipt, workspaceReceiptID string) {
	t.Helper()
	wallet, purchase := 0, 0
	for _, receipt := range receipts {
		switch receipt.Type {
		case "gateway.wallet_adjustment.v1":
			wallet++
		case "billing.workspace_purchased.v1":
			if receipt.ReceiptID == workspaceReceiptID {
				purchase++
			}
		}
	}
	if wallet != 1 || purchase != 1 {
		t.Fatalf("local E2E Ledger cardinality wallet=%d purchase=%d", wallet, purchase)
	}
}

func verifyLocalE2ERestartPersistence(t *testing.T, process *localE2EProcess, evidence localE2EFullEvidence, adminEmail, adminPassword string) *localE2EAPI {
	t.Helper()
	owner := loginLocalE2EUser(t, process, &evidence.user)
	if balance := localE2EWalletMicros(t, owner); balance != 7_420_000 {
		t.Fatalf("local E2E restarted balance=%d, want 7420000", balance)
	}
	workspaces := localE2EData(t, localE2EMustRequest(t, owner, http.MethodGet, "/api/workspaces", nil, "", http.StatusOK), "restarted workspaces")
	localE2EItemBy(t, localE2EItems(t, workspaces, "restarted workspaces"), "id", evidence.workspaceID, "restarted workspace")
	key := localE2EData(t, localE2EMustRequest(t, owner, http.MethodGet, "/api/gateway/keys/"+evidence.generalKeyID, nil, "", http.StatusOK), "restarted key")
	if stringValue(key["name"]) != "local-stage-b-edited" || stringValue(key["groupId"]) != evidence.generalKeyGroup || stringValue(key["status"]) != "active" ||
		int64(numberField(key, "quotaUsdMicros", -1)) != 2_500_000 || int64(numberField(key, "rateLimit5hUsdMicros", -1)) != 6_000_000 ||
		int64(numberField(key, "rateLimit1dUsdMicros", -1)) != 12_000_000 || int64(numberField(key, "rateLimit7dUsdMicros", -1)) != 24_000_000 ||
		!slices.Equal(localE2EStringItems(t, key["ipWhitelist"], "restarted Key allowlist"), []string{"127.0.0.1", "192.0.2.10"}) ||
		!slices.Equal(localE2EStringItems(t, key["ipBlacklist"], "restarted Key denylist"), []string{"203.0.113.0/24"}) {
		t.Fatal("restarted Gateway Key lost authoritative parity fields")
	}
	receipts := localE2EData(t, localE2EMustRequest(t, owner, http.MethodGet, "/api/billing/receipts", nil, "", http.StatusOK), "restarted receipts")
	items, ok := receipts["receipts"].([]any)
	if !ok {
		t.Fatal("restarted receipt list is invalid")
	}
	localE2EItemBy(t, items, "receiptId", evidence.receiptID, "restarted receipt")
	admin := process.newAPI(t)
	admin.login(t, adminEmail, adminPassword)
	operatorWorkspace := localE2EData(t, localE2EMustRequest(t, admin, http.MethodGet, "/api/operator/workspaces/"+evidence.workspaceID, nil, "", http.StatusOK), "restarted operator workspace")
	operatorReceipt := localE2EMap(t, operatorWorkspace["receipt"], "restarted operator workspace receipt")
	operatorReceiptData := localE2EMap(t, operatorReceipt["data"], "restarted operator receipt data")
	if operatorReceipt["available"] != true || stringValue(operatorReceiptData["receiptId"]) != evidence.receiptID || stringValue(operatorReceiptData["type"]) != "billing.workspace_purchased.v1" {
		t.Fatal("restarted Operator Workspace detail changed the canonical purchase Receipt")
	}
	localE2EMustRequest(t, admin, http.MethodGet, "/api/operator/wallet-adjustments/"+evidence.walletOperation, nil, "", http.StatusOK)
	deletePath := "/api/gateway/keys/" + evidence.generalKeyID
	deleteIntent := "gateway-key-delete-local-full-" + evidence.user.accountID
	localE2EMustRequest(t, owner, http.MethodDelete, deletePath, nil, deleteIntent, http.StatusOK)
	localE2EMustRequest(t, owner, http.MethodDelete, deletePath, nil, deleteIntent, http.StatusOK)
	localE2EMustRequest(t, owner, http.MethodGet, deletePath, nil, "", http.StatusNotFound)
	empty := localE2EData(t, localE2EMustRequest(t, owner, http.MethodGet, "/api/gateway/keys?page=1&pageSize=10&search=local-stage-b-edited", nil, "", http.StatusOK), "deleted Key search")
	if int64(numberField(empty, "total", -1)) != 0 || len(localE2EItems(t, empty, "deleted Key search")) != 0 {
		t.Fatal("deleted Gateway Key remains in authoritative Sub2API results")
	}
	return admin
}

type localE2EFaultEvidence struct {
	user        localE2EUser
	operationID string
	code        string
}

func startLocalE2EWalletFault(t *testing.T, process *localE2EProcess, admin *localE2EAPI, fault, expectedCode string, expectedStatus int) localE2EFaultEvidence {
	t.Helper()
	user := provisionLocalE2EUser(t, admin, "opl-"+strings.ReplaceAll(fault, "_", "-"))
	accounts := localE2EData(t, localE2EMustRequest(t, admin, http.MethodGet, "/api/operator/accounts?page=1&pageSize=50", nil, "", http.StatusOK), "fault operator accounts")
	account := localE2EItemBy(t, localE2EItems(t, accounts, "fault operator accounts"), "accountId", user.accountID, "fault operator account")
	remoteUserID, err := strconv.ParseInt(stringValue(account["sub2apiUserId"]), 10, 64)
	if err != nil || remoteUserID <= 0 {
		t.Fatalf("local E2E %s mapped Sub2API user is invalid", fault)
	}
	user.sub2APIUser = remoteUserID
	balance, err := process.handler.service.Sub2APIBalance(context.Background(), remoteUserID)
	if err != nil || balance.USDMicros != 0 {
		t.Fatalf("local E2E %s starting balance=%d err=%v", fault, balance.USDMicros, err)
	}
	key := "wallet-fault-" + fault + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	path := "/api/operator/accounts/" + user.accountID + "/wallet-adjustments"
	response := localE2EMustRequest(t, admin, http.MethodPost, path, map[string]any{
		"kind": "recharge", "amountUsd": "60.00", "reason": "local fault recovery", "confirmationAccountId": user.accountID,
	}, key, http.StatusAccepted)
	body := localE2EMap(t, response.body, "fault wallet adjustment")
	failure := localE2EMap(t, body["upstreamFailure"], "fault upstream failure")
	if stringValue(body["status"]) != "manual_review" || stringValue(body["errorCode"]) == "" || stringValue(failure["errorCode"]) != expectedCode {
		t.Fatalf("local E2E %s did not persist structured manual-review diagnostics", fault)
	}
	if expectedStatus != 0 && int(numberField(failure, "httpStatus", 0)) != expectedStatus {
		t.Fatalf("local E2E %s HTTP status diagnostic is missing", fault)
	}
	if expectedStatus == 0 && numberField(failure, "httpStatus", 0) != 0 {
		t.Fatalf("local E2E %s unexpectedly persisted an HTTP status", fault)
	}
	operationID := stringValue(body["operationId"])
	if operationID == "" {
		t.Fatalf("local E2E %s operation ID is missing", fault)
	}
	operationSuffix := strings.TrimPrefix(operationID, "wallet-adjustment-")
	if len(operationSuffix) != 18 || strings.Trim(operationSuffix, "0123456789abcdef") != "" {
		t.Fatalf("local E2E %s operation ID is not canonical", fault)
	}
	user.operationID = operationID
	user.recoveryKey = "wallet-recovery-" + operationSuffix[:16]
	return localE2EFaultEvidence{user: user, operationID: operationID, code: walletAdjustmentRedeemCode(operationID)}
}

func recoverLocalE2EWalletFault(t *testing.T, admin *localE2EAPI, evidence localE2EFaultEvidence, sub2API *clients.Sub2APIHTTPClient) {
	t.Helper()
	path := "/api/operator/wallet-adjustments/" + evidence.operationID + "/recover"
	operationSuffix := strings.TrimPrefix(evidence.operationID, "wallet-adjustment-")
	if len(operationSuffix) != 18 || strings.Trim(operationSuffix, "0123456789abcdef") != "" {
		t.Fatal("local E2E recovery operation ID is not canonical")
	}
	body := map[string]any{"accountId": evidence.user.accountID, "evidenceRef": "case-20260723-" + operationSuffix[:12]}
	recovered := localE2EMustRequest(t, admin, http.MethodPost, path, body, evidence.user.recoveryKey, http.StatusOK)
	if stringValue(localE2EMap(t, recovered.body, "wallet recovery")["status"]) != "succeeded" {
		t.Fatal("local E2E wallet recovery did not succeed")
	}
	localE2EMustRequest(t, admin, http.MethodPost, path, body, evidence.user.recoveryKey, http.StatusOK)
	conflict, err := admin.request(context.Background(), http.MethodPost, path, body, evidence.user.recoveryKey+"-different")
	if err != nil || conflict.status != http.StatusConflict {
		t.Fatal("local E2E wallet recovery accepted a different idempotency identity")
	}
	balance, err := sub2API.Balance(context.Background(), evidence.user.sub2APIUser)
	if err != nil || balance.USDMicros != 60_000_000 {
		t.Fatalf("local E2E recovered wallet balance=%d err=%v", balance.USDMicros, err)
	}
	history, err := sub2API.BalanceHistory(context.Background(), evidence.user.sub2APIUser)
	if err != nil || countLocalE2EHistory(history, evidence.code, 60_000_000) != 1 {
		t.Fatal("local E2E recovery did not reuse the original stable redeem identity exactly once")
	}
}

func assertLocalE2ERedeemIdentities(t *testing.T, transport *localE2EFaultTransport, expected string, attempts int) {
	t.Helper()
	identities := transport.redeemIdentities()
	if len(identities) != attempts {
		t.Fatalf("local E2E redeem attempts=%d, want %d", len(identities), attempts)
	}
	for _, identity := range identities {
		if identity != expected {
			t.Fatal("local E2E recovery changed the upstream idempotency identity")
		}
	}
}

func TestOPLLocalToProdStageB(t *testing.T) {
	if os.Getenv("OPL_LOCAL_E2E") != "1" {
		t.Fatal("OPL_LOCAL_E2E=1 is required with the local_e2e build tag")
	}
	t.Setenv("OPL_MONTHLY_BILLING_WORKER_ENABLED", "false")
	t.Setenv("OPL_PROVIDER_RECONCILE_WORKER_ENABLED", "false")
	t.Setenv("OPL_ARCHIVE_RETENTION_WORKER_ENABLED", "false")

	traffic := &localE2ETraffic{}
	sub2API, faultTransport := newLocalE2ESub2API(t, traffic, "")
	version, err := sub2API.Version(context.Background())
	if err != nil || strings.TrimPrefix(strings.TrimSpace(version), "v") != "0.1.162" {
		t.Fatalf("local Sub2API version=%q err=%v", version, err)
	}
	databaseURL := localE2EDatabase(t)
	events := []string{}
	fabric := &monthlyFabric{fakeFabricClient: fakeFabricClient{calls: &events}, events: &events}
	ledger := newLocalE2ELedger()
	adminEmail, adminPassword := strings.TrimSpace(os.Getenv("OPL_SUB2API_ADMIN_EMAIL")), os.Getenv("OPL_SUB2API_ADMIN_PASSWORD")

	process := startLocalE2EProcess(t, databaseURL, controlplane.NewService(ledger, fabric, sub2API))
	full := runLocalE2EFullFlow(t, process, adminEmail, adminPassword, sub2API, fabric, ledger)
	process.Close()

	process = startLocalE2EProcess(t, databaseURL, controlplane.NewService(ledger, fabric, sub2API))
	admin := verifyLocalE2ERestartPersistence(t, process, full, adminEmail, adminPassword)

	for _, scenario := range []struct {
		fault          string
		errorCode      string
		httpStatus     int
		redeemAttempts int
	}{{"409", "redeem_conflict", http.StatusConflict, 2}, {"503", "gateway_busy", http.StatusServiceUnavailable, 2}, {"timeout", "request_timeout", 0, 2}} {
		faultTransport.configureFault(scenario.fault)
		evidence := startLocalE2EWalletFault(t, process, admin, scenario.fault, scenario.errorCode, scenario.httpStatus)
		recoverLocalE2EWalletFault(t, admin, evidence, sub2API)
		assertLocalE2ERedeemIdentities(t, faultTransport, evidence.code, scenario.redeemAttempts)
	}

	faultTransport.configureFault("response_loss")
	lost := startLocalE2EWalletFault(t, process, admin, "response_loss", "transport_failure", 0)
	process.Close()
	process = startLocalE2EProcess(t, databaseURL, controlplane.NewService(ledger, fabric, sub2API))
	admin = process.newAPI(t)
	admin.login(t, adminEmail, adminPassword)
	recoverLocalE2EWalletFault(t, admin, lost, sub2API)
	assertLocalE2ERedeemIdentities(t, faultTransport, lost.code, 1)

	localRequests, localWrites, productionRequests, productionWrites := traffic.snapshot()
	if productionRequests != 0 || productionWrites != 0 {
		t.Fatalf("production traffic requests=%d writes=%d", productionRequests, productionWrites)
	}
	t.Logf("STAGE_B_E2E balance=0->60000000->60000000->7420000 walletOperation=%s launchOperation=%s receipt=%s workspace=%s keyId=%s", full.walletOperation, full.launchOperation, full.receiptID, full.workspaceID, full.generalKeyID)
	t.Logf("STAGE_B_E2E fabric compute=%s provider=%s storage=%s provider=%s attachment=%s runtime=%s", full.computeID, full.computeProvider, full.storageID, full.storageProvider, full.attachmentID, full.runtimeID)
	t.Logf("STAGE_B_E2E localSub2API requests=%d writes=%d productionRequests=0 productionWrites=0", localRequests, localWrites)
}
