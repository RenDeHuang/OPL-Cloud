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
	raw := stringValue(data["usdMicros"])
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 || strconv.FormatInt(value, 10) != raw {
		t.Fatal("local E2E wallet returned an invalid amount")
	}
	return value
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
