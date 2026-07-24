package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

type sourceTruthGatewayClient struct {
	*customerFactsSub2API
	balanceErr error
	keys       []clients.Sub2APIWorkspaceKey
	keysErr    error
	userKeyErr error
	keyUserIDs []int64
}

func (*sourceTruthGatewayClient) PublicEndpoint() string { return "https://gateway.example.test/v1" }

func (c *sourceTruthGatewayClient) UserGroups(_ context.Context, credential clients.SessionDelegatedCredential, userID int64) ([]clients.Sub2APIGroup, error) {
	if credential.Bearer != "test-user-delegated-token" || userID != 41 {
		return nil, errors.New("wrong delegated credential")
	}
	return []clients.Sub2APIGroup{{ID: 7, Name: "Basic", Platform: "openai", RateMultiplier: 1, Status: "active"}}, nil
}

func (c *sourceTruthGatewayClient) Balance(ctx context.Context, userID int64) (clients.Sub2APIBalance, error) {
	if c.balanceErr != nil {
		return clients.Sub2APIBalance{}, c.balanceErr
	}
	return c.testSub2APIClient.Balance(ctx, userID)
}

func (c *sourceTruthGatewayClient) Keys(_ context.Context, userID int64) ([]clients.Sub2APIWorkspaceKey, error) {
	c.keyUserIDs = append(c.keyUserIDs, userID)
	return append([]clients.Sub2APIWorkspaceKey(nil), c.keys...), c.keysErr
}

func (c *sourceTruthGatewayClient) UserKeys(ctx context.Context, credential clients.SessionDelegatedCredential, userID int64) ([]clients.Sub2APIWorkspaceKey, error) {
	if credential.Bearer != "test-user-delegated-token" {
		return nil, errors.New("wrong delegated credential")
	}
	return c.Keys(ctx, userID)
}

func (c *sourceTruthGatewayClient) UserKeyPage(ctx context.Context, credential clients.SessionDelegatedCredential, userID int64, query clients.Sub2APIKeyPageQuery) (clients.Sub2APIKeyPage, error) {
	keys, err := c.UserKeys(ctx, credential, userID)
	if err != nil {
		return clients.Sub2APIKeyPage{}, err
	}
	return clients.Sub2APIKeyPage{Items: keys, Total: len(keys), Page: query.Page, PageSize: query.PageSize, Pages: 1}, nil
}

func (c *sourceTruthGatewayClient) UserKey(_ context.Context, credential clients.SessionDelegatedCredential, userID, keyID int64) (clients.Sub2APIWorkspaceKey, error) {
	if credential.Bearer != "test-user-delegated-token" {
		return clients.Sub2APIWorkspaceKey{}, errors.New("wrong delegated credential")
	}
	c.keyUserIDs = append(c.keyUserIDs, userID)
	if c.userKeyErr != nil {
		return clients.Sub2APIWorkspaceKey{}, c.userKeyErr
	}
	for _, key := range c.keys {
		if key.ID == keyID && key.UserID == userID {
			return key, nil
		}
	}
	return clients.Sub2APIWorkspaceKey{}, clients.ErrSub2APIKeyNotFound
}

func decodeSourceEnvelope(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope map[string]any
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode source envelope: %v: %s", err, response.Body.String())
	}
	if envelope["source"] != "sub2api" || envelope["available"] != true {
		t.Fatalf("source envelope = %#v", envelope)
	}
	if _, err := time.Parse(time.RFC3339Nano, stringValue(envelope["fetchedAt"])); err != nil {
		t.Fatalf("fetchedAt = %#v: %v", envelope["fetchedAt"], err)
	}
	if _, exists := envelope["sourceUpdatedAt"]; exists {
		t.Fatalf("sourceUpdatedAt was fabricated: %#v", envelope)
	}
	return envelope
}

func assertUnavailableSourceEnvelope(t *testing.T, response *httptest.ResponseRecorder, wantStatus int) {
	t.Helper()
	if response.Code != wantStatus {
		t.Fatalf("unavailable status = %d, want %d: %s", response.Code, wantStatus, response.Body.String())
	}
	var envelope map[string]any
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope) != 4 || envelope["source"] != "sub2api" || envelope["status"] != "unavailable" || envelope["available"] != false || envelope["data"] != nil {
		t.Fatalf("unavailable envelope = %#v", envelope)
	}
}

func TestGatewaySourceTruthRoutesUseSessionIdentityAndStrictEnvelopes(t *testing.T) {
	createdAt := time.Date(2026, 7, 18, 1, 2, 3, 0, time.UTC)
	lastUsedAt := createdAt.Add(-time.Hour)
	base := &customerFactsSub2API{
		testSub2APIClient: &testSub2APIClient{
			balance: 0, charges: map[string]int64{},
			workspaceKey: clients.Sub2APIWorkspaceKey{ID: 9, UserID: 41, Name: "opl-workspace", Key: "workspace-secret", Status: "active"},
		},
		usagePage: clients.Sub2APIUsagePage{
			Items: []clients.Sub2APIUsageRecord{{
				UserID: 41, APIKeyID: 9, RequestID: "request-1", CreatedAt: createdAt, Model: "gpt-5",
				InboundEndpoint: "/v1/responses", RequestType: "sync", InputTokens: 1, OutputTokens: 2,
				CacheCreationTokens: 3, CacheReadTokens: 4, ActualCostUSDMicros: 5,
			}},
			Total: 1, Page: 1, PageSize: 50, Pages: 1,
		},
		usageStats: clients.Sub2APIUsageStats{},
		history: map[int64][]clients.Sub2APIBalanceHistoryEntry{41: {{
			Code: "adjustment-1", Type: "balance", ValueUSDMicros: -5, Status: "used", UsedAt: &createdAt, CreatedAt: createdAt,
		}}},
	}
	client := &sourceTruthGatewayClient{
		customerFactsSub2API: base,
		keys: []clients.Sub2APIWorkspaceKey{
			{ID: 8, UserID: 41, Name: "retired", Key: "must-not-leak-retired", Status: "disabled"},
			{ID: 9, UserID: 41, Name: "opl-workspace", Key: "must-not-leak-active", Status: "active", QuotaUSDMicros: 10, QuotaUsedUSDMicros: 2, Usage5hUSDMicros: 1, Usage1dUSDMicros: 2, Usage7dUSDMicros: 3, LastUsedAt: &lastUsedAt},
		},
	}
	server, session := newGatewayOwnerTestServer(t, client, nil)
	spoofed := "?accountId=acct-other&user_id=999&api_key_id=999&sub2apiUserId=999"

	wallet := requestWithSession(t, server, session, http.MethodGet, "/api/gateway/wallet"+spoofed, "")
	if wallet.Code != http.StatusOK {
		t.Fatalf("wallet = %d: %s", wallet.Code, wallet.Body.String())
	}
	walletEnvelope := decodeSourceEnvelope(t, wallet)
	if walletEnvelope["status"] != "available" {
		t.Fatalf("zero wallet is not available: %#v", walletEnvelope)
	}
	walletData := mapField(walletEnvelope, "data")
	if len(walletData) != 4 || walletData["userId"] != "41" || walletData["currency"] != "USD" || walletData["usdMicros"] != float64(0) || walletData["status"] != "active" {
		t.Fatalf("wallet data = %#v", walletData)
	}

	keysResponse := requestWithSession(t, server, session, http.MethodGet, "/api/gateway/keys"+spoofed, "")
	if keysResponse.Code != http.StatusOK || strings.Contains(keysResponse.Body.String(), "must-not-leak") {
		t.Fatalf("keys = %d: %s", keysResponse.Code, keysResponse.Body.String())
	}
	keysEnvelope := decodeSourceEnvelope(t, keysResponse)
	keysData := mapField(keysEnvelope, "data")
	keyItems, _ := keysData["items"].([]any)
	if keysEnvelope["status"] != "available" || len(keyItems) != 2 || keysData["total"] != float64(2) {
		t.Fatalf("keys envelope = %#v", keysEnvelope)
	}
	activeKey := keyItems[1].(map[string]any)
	if len(activeKey) != 23 || activeKey["id"] != "9" || activeKey["status"] != "active" || activeKey["quotaUsdMicros"] != float64(10) ||
		activeKey["kind"] != "workspace" || activeKey["manageable"] != false || activeKey["deletable"] != false || activeKey["expiresAt"] != nil {
		t.Fatalf("active key = %#v", activeKey)
	}

	usage := requestWithSession(t, server, session, http.MethodGet, "/api/gateway/keys/9/usage"+spoofed+"&page=1&pageSize=50", "")
	if usage.Code != http.StatusOK {
		t.Fatalf("usage = %d: %s", usage.Code, usage.Body.String())
	}
	usageEnvelope := decodeSourceEnvelope(t, usage)
	usageItems, _ := mapField(usageEnvelope, "data")["items"].([]any)
	if len(usageItems) != 1 || usageItems[0].(map[string]any)["apiKeyId"] != "9" {
		t.Fatalf("usage envelope = %#v", usageEnvelope)
	}

	stats := requestWithSession(t, server, session, http.MethodGet, "/api/gateway/keys/9/usage-summary"+spoofed+"&period=month", "")
	if stats.Code != http.StatusOK {
		t.Fatalf("stats = %d: %s", stats.Code, stats.Body.String())
	}
	statsEnvelope := decodeSourceEnvelope(t, stats)
	if statsEnvelope["status"] != "available" || numberField(mapField(statsEnvelope, "data"), "totalRequests", -1) != 0 {
		t.Fatalf("zero stats = %#v", statsEnvelope)
	}

	history := requestWithSession(t, server, session, http.MethodGet, "/api/gateway/balance-history"+spoofed, "")
	if history.Code != http.StatusOK || strings.Contains(history.Body.String(), "adjustment-1") || strings.Contains(history.Body.String(), "usedBy") {
		t.Fatalf("history = %d: %s", history.Code, history.Body.String())
	}
	historyEnvelope := decodeSourceEnvelope(t, history)
	historyItems, _ := mapField(historyEnvelope, "data")["items"].([]any)
	if len(historyItems) != 1 || len(historyItems[0].(map[string]any)) != 5 || historyItems[0].(map[string]any)["valueUsdMicros"] != float64(-5) {
		t.Fatalf("history envelope = %#v", historyEnvelope)
	}

	if len(client.keyUserIDs) != 3 || client.keyUserIDs[0] != 41 || client.keyUserIDs[1] != 41 || client.keyUserIDs[2] != 41 || base.usageQuery.UserID != 41 || base.usageQuery.APIKeyID != 9 || base.statsQuery.UserID != 41 || base.statsQuery.APIKeyID != 9 || len(base.historyIDs) != 1 || base.historyIDs[0] != 41 {
		t.Fatalf("session identity was not authoritative: keys=%#v usage=%#v stats=%#v history=%#v", client.keyUserIDs, base.usageQuery, base.statsQuery, base.historyIDs)
	}
}

func TestGatewaySourceTruthEmptyAndUnavailableAreNotFabricated(t *testing.T) {
	baseClient := func() *sourceTruthGatewayClient {
		return &sourceTruthGatewayClient{customerFactsSub2API: &customerFactsSub2API{
			testSub2APIClient: &testSub2APIClient{charges: map[string]int64{}, workspaceKey: clients.Sub2APIWorkspaceKey{ID: 9, UserID: 41, Name: "opl-workspace", Key: "workspace-secret", Status: "active"}},
			usagePage:         clients.Sub2APIUsagePage{Page: 1, PageSize: 50, Pages: 1},
			history:           map[int64][]clients.Sub2APIBalanceHistoryEntry{},
		}}
	}

	for _, tc := range []struct {
		path   string
		mutate func(*sourceTruthGatewayClient)
	}{
		{path: "/api/gateway/keys"},
		{path: "/api/gateway/keys/9/usage", mutate: func(c *sourceTruthGatewayClient) {
			c.keys = []clients.Sub2APIWorkspaceKey{{ID: 9, UserID: 41, Name: "general", Status: "active"}}
		}},
		{path: "/api/gateway/balance-history"},
	} {
		t.Run("empty "+tc.path, func(t *testing.T) {
			client := baseClient()
			if tc.mutate != nil {
				tc.mutate(client)
			}
			server, session := newGatewayOwnerTestServer(t, client, nil)
			response := requestWithSession(t, server, session, http.MethodGet, tc.path, "")
			if response.Code != http.StatusOK {
				t.Fatalf("empty status = %d: %s", response.Code, response.Body.String())
			}
			envelope := decodeSourceEnvelope(t, response)
			if envelope["status"] != "empty" {
				t.Fatalf("empty envelope = %#v", envelope)
			}
		})
	}

	for _, tc := range []struct {
		name, path string
		mutate     func(*sourceTruthGatewayClient)
	}{
		{name: "wallet", path: "/api/gateway/wallet", mutate: func(c *sourceTruthGatewayClient) { c.balanceErr = errors.New("wallet unavailable") }},
		{name: "keys", path: "/api/gateway/keys", mutate: func(c *sourceTruthGatewayClient) { c.keysErr = errors.New("keys unavailable") }},
		{name: "usage", path: "/api/gateway/keys/9/usage", mutate: func(c *sourceTruthGatewayClient) {
			c.keys = []clients.Sub2APIWorkspaceKey{{ID: 9, UserID: 41, Name: "general", Status: "active"}}
			c.usageErr = errors.New("usage unavailable")
		}},
		{name: "usage pagination", path: "/api/gateway/keys/9/usage", mutate: func(c *sourceTruthGatewayClient) {
			c.keys = []clients.Sub2APIWorkspaceKey{{ID: 9, UserID: 41, Name: "general", Status: "active"}}
			c.usageErr = errors.New("invalid sub2api usage pagination")
		}},
		{name: "stats", path: "/api/gateway/keys/9/usage-summary", mutate: func(c *sourceTruthGatewayClient) {
			c.keys = []clients.Sub2APIWorkspaceKey{{ID: 9, UserID: 41, Name: "general", Status: "active"}}
			c.statsErr = errors.New("stats unavailable")
		}},
		{name: "history", path: "/api/gateway/balance-history", mutate: func(c *sourceTruthGatewayClient) { c.historyErr = errors.New("history unavailable") }},
	} {
		t.Run("unavailable "+tc.name, func(t *testing.T) {
			client := baseClient()
			tc.mutate(client)
			server, session := newGatewayOwnerTestServer(t, client, nil)
			response := requestWithSession(t, server, session, http.MethodGet, tc.path, "")
			if response.Code != http.StatusBadGateway {
				t.Fatalf("unavailable status = %d: %s", response.Code, response.Body.String())
			}
			var envelope map[string]any
			if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
				t.Fatal(err)
			}
			if len(envelope) != 4 || envelope["source"] != "sub2api" || envelope["status"] != "unavailable" || envelope["available"] != false || envelope["data"] != nil {
				t.Fatalf("unavailable envelope = %#v", envelope)
			}
			if _, err := time.Parse(time.RFC3339Nano, stringValue(envelope["fetchedAt"])); err != nil {
				t.Fatalf("unavailable fetchedAt = %#v", envelope["fetchedAt"])
			}
		})
	}
}

func TestGatewayRevealIsStrictSub2APISource(t *testing.T) {
	client := &sourceTruthGatewayClient{
		customerFactsSub2API: &customerFactsSub2API{testSub2APIClient: &testSub2APIClient{charges: map[string]int64{}}},
		keys:                 []clients.Sub2APIWorkspaceKey{{ID: 9, UserID: 41, Name: "opl-workspace", Key: "workspace-secret", Status: "active"}},
	}
	server, session := newGatewayOwnerTestServer(t, client, nil)
	response := requestWithSession(t, server, session, http.MethodPost, "/api/gateway/keys/9/reveal?accountId=acct-other&sub2apiUserId=999", "{}")
	if response.Code != http.StatusOK {
		t.Fatalf("reveal = %d: %s", response.Code, response.Body.String())
	}
	envelope := decodeSourceEnvelope(t, response)
	data := mapField(envelope, "data")
	if envelope["status"] != "available" || len(data) != 4 || data["id"] != "9" || data["name"] != "opl-workspace" || data["status"] != "active" || data["value"] != "workspace-secret" {
		t.Fatalf("reveal envelope = %#v", envelope)
	}
	if response.Header().Get("Cache-Control") != "private, no-store" || len(client.keyUserIDs) != 1 || client.keyUserIDs[0] != 41 {
		t.Fatalf("reveal boundary cache=%q users=%#v", response.Header().Get("Cache-Control"), client.keyUserIDs)
	}
}

type workspaceKeyRotationClient struct {
	*customerFactsSub2API
	mu              sync.Mutex
	keys            map[int64]clients.Sub2APIWorkspaceKey
	failAfter       string
	failed          bool
	createWrites    int
	updateWrites    int
	deleteWrites    int
	createStarted   chan struct{}
	releaseCreate   chan struct{}
	createStartOnce sync.Once
}

func (c *workspaceKeyRotationClient) keyList(userID int64) []clients.Sub2APIWorkspaceKey {
	keys := make([]clients.Sub2APIWorkspaceKey, 0, len(c.keys))
	for _, key := range c.keys {
		if key.UserID == userID {
			keys = append(keys, key)
		}
	}
	return keys
}

func (c *workspaceKeyRotationClient) Keys(_ context.Context, userID int64) ([]clients.Sub2APIWorkspaceKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.keyList(userID), nil
}

func (c *workspaceKeyRotationClient) UserKeys(_ context.Context, credential clients.SessionDelegatedCredential, userID int64) ([]clients.Sub2APIWorkspaceKey, error) {
	if credential.Bearer != "test-user-delegated-token" {
		return nil, errors.New("wrong delegated credential")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.keyList(userID), nil
}

func (c *workspaceKeyRotationClient) UserKey(_ context.Context, credential clients.SessionDelegatedCredential, userID, keyID int64) (clients.Sub2APIWorkspaceKey, error) {
	if credential.Bearer != "test-user-delegated-token" {
		return clients.Sub2APIWorkspaceKey{}, errors.New("wrong delegated credential")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key, ok := c.keys[keyID]
	if !ok || key.UserID != userID {
		return clients.Sub2APIWorkspaceKey{}, clients.ErrSub2APIKeyNotFound
	}
	return key, nil
}

func (c *workspaceKeyRotationClient) WorkspaceKey(_ context.Context, userID int64) (clients.Sub2APIWorkspaceKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var match clients.Sub2APIWorkspaceKey
	for _, key := range c.keys {
		if key.UserID == userID && key.Name == "opl-workspace" && key.Status == "active" {
			if match.ID != 0 {
				return clients.Sub2APIWorkspaceKey{}, clients.ErrSub2APIWorkspaceKeyAmbiguous
			}
			match = key
		}
	}
	if match.ID == 0 {
		return clients.Sub2APIWorkspaceKey{}, clients.ErrSub2APIWorkspaceKeyMissing
	}
	return match, nil
}

func (c *workspaceKeyRotationClient) fail(stage string) bool {
	if c.failAfter == stage && !c.failed {
		c.failed = true
		return true
	}
	return false
}

func (c *workspaceKeyRotationClient) CreateUserKey(_ context.Context, credential clients.SessionDelegatedCredential, userID int64, input clients.Sub2APICreateKeyInput, idempotencyKey string) (clients.Sub2APIWorkspaceKey, error) {
	if credential.Bearer != "test-user-delegated-token" || userID != 41 || idempotencyKey == "" || !strings.HasPrefix(input.Name, "opl-workspace-replacement-") {
		return clients.Sub2APIWorkspaceKey{}, errors.New("invalid replacement create")
	}
	if c.createStarted != nil {
		c.createStartOnce.Do(func() { close(c.createStarted) })
		<-c.releaseCreate
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, key := range c.keys {
		if key.UserID == userID && key.Name == input.Name {
			return key, nil
		}
	}
	keyID := int64(19)
	for {
		if _, exists := c.keys[keyID]; !exists {
			break
		}
		keyID++
	}
	key := clients.Sub2APIWorkspaceKey{ID: keyID, UserID: userID, Name: input.Name, Key: "replacement-workspace-key-secret", Status: "active"}
	c.keys[key.ID] = key
	c.createWrites++
	if c.fail("create") {
		return clients.Sub2APIWorkspaceKey{}, errors.New("create response lost")
	}
	return key, nil
}

func (c *workspaceKeyRotationClient) UpdateUserKey(_ context.Context, credential clients.SessionDelegatedCredential, userID, keyID int64, input clients.Sub2APIUpdateKeyInput) (clients.Sub2APIWorkspaceKey, error) {
	if credential.Bearer != "test-user-delegated-token" {
		return clients.Sub2APIWorkspaceKey{}, errors.New("wrong delegated credential")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key, ok := c.keys[keyID]
	if !ok || key.UserID != userID || input.Name == nil {
		return clients.Sub2APIWorkspaceKey{}, clients.ErrSub2APIKeyNotFound
	}
	stage := "promote"
	if strings.HasPrefix(*input.Name, "opl-workspace-retired-") {
		stage = "retire"
	}
	key.Name = *input.Name
	if input.Enabled != nil {
		key.Status = "disabled"
		if *input.Enabled {
			key.Status = "active"
		}
	}
	c.keys[keyID] = key
	c.updateWrites++
	if c.fail(stage) {
		return clients.Sub2APIWorkspaceKey{}, errors.New(stage + " response lost")
	}
	return key, nil
}

func (c *workspaceKeyRotationClient) DeleteUserKey(_ context.Context, credential clients.SessionDelegatedCredential, userID, keyID int64) error {
	if credential.Bearer != "test-user-delegated-token" {
		return errors.New("wrong delegated credential")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key, ok := c.keys[keyID]
	if !ok || key.UserID != userID {
		return clients.ErrSub2APIKeyNotFound
	}
	delete(c.keys, keyID)
	c.deleteWrites++
	if c.fail("delete") {
		return errors.New("delete response lost")
	}
	return nil
}

type workspaceKeyRotationFabric struct {
	fakeFabricClient
	failAfter string
	failed    bool
	bindings  []clients.WorkspaceRuntimeGatewaySecretInput
}

func (f *workspaceKeyRotationFabric) WriteGatewaySecret(ctx context.Context, input clients.GatewaySecretWriteInput, key string) (clients.GatewaySecretWriteResult, error) {
	result, err := f.fakeFabricClient.WriteGatewaySecret(ctx, input, key)
	if err == nil && f.failAfter == "secret" && !f.failed {
		f.failed = true
		return clients.GatewaySecretWriteResult{}, errors.New("Secret response lost")
	}
	return result, err
}

func (f *workspaceKeyRotationFabric) BindWorkspaceRuntimeGatewaySecret(_ context.Context, input clients.WorkspaceRuntimeGatewaySecretInput, _ string) (clients.WorkspaceRuntimeGatewaySecretBinding, error) {
	result := clients.WorkspaceRuntimeGatewaySecretBinding{
		WorkspaceID: input.WorkspaceID, WorkspaceAPIKeyID: input.WorkspaceAPIKeyID,
		SecretRef: input.SecretRef, Fingerprint: input.Fingerprint, Bound: true,
	}
	if len(f.bindings) > 0 && f.bindings[len(f.bindings)-1] == input {
		return result, nil
	}
	f.bindings = append(f.bindings, input)
	if f.failAfter == "bind" && !f.failed {
		f.failed = true
		return clients.WorkspaceRuntimeGatewaySecretBinding{}, errors.New("Runtime bind response lost")
	}
	return result, nil
}

func (f *workspaceKeyRotationFabric) WorkspaceRuntimeGatewaySecret(_ context.Context, workspaceID string) (clients.WorkspaceRuntimeGatewaySecretBinding, error) {
	if f.failAfter == "readback" && !f.failed {
		f.failed = true
		return clients.WorkspaceRuntimeGatewaySecretBinding{}, errors.New("Runtime readback unavailable")
	}
	if len(f.bindings) == 0 {
		return clients.WorkspaceRuntimeGatewaySecretBinding{WorkspaceID: workspaceID}, nil
	}
	input := f.bindings[len(f.bindings)-1]
	return clients.WorkspaceRuntimeGatewaySecretBinding{WorkspaceID: workspaceID, WorkspaceAPIKeyID: input.WorkspaceAPIKeyID, SecretRef: input.SecretRef, Fingerprint: input.Fingerprint, Bound: true}, nil
}

type workspaceKeyRotationLedger struct {
	fakeLedgerClient
	mu        sync.Mutex
	receipts  map[string]clients.Receipt
	inputs    []clients.ReceiptInput
	failAfter string
	failed    bool
}

func (l *workspaceKeyRotationLedger) RecordReceipt(_ context.Context, input clients.ReceiptInput, key string) (clients.Receipt, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if receipt, ok := l.receipts[key]; ok {
		return receipt, nil
	}
	receipt := clients.Receipt{ReceiptInput: input, ReceiptID: "receipt-" + stableID(key)[:12]}
	l.receipts[key] = receipt
	l.inputs = append(l.inputs, input)
	if l.failAfter == "receipt" && !l.failed {
		l.failed = true
		return clients.Receipt{}, errors.New("Receipt response lost")
	}
	return receipt, nil
}

type workspaceKeyRotationStore struct {
	*memoryTableStore
	failPhase             string
	failPersistedPhase    string
	failCASAfterCommit    bool
	failed                bool
	persistedResponseLost bool
	casResponseLost       bool
}

func (s *workspaceKeyRotationStore) SaveRuntimeOperation(ctx context.Context, row map[string]any) error {
	var phase string
	if stringValue(row["action"]) == "workspace.gateway_key.rotate" && !s.failed && s.failPhase != "" {
		var result map[string]any
		if json.Unmarshal([]byte(stringValue(row["result"])), &result) == nil {
			phase = stringValue(result["phase"])
		}
		if phase == s.failPhase {
			s.failed = true
			return errors.New("phase persist failed")
		}
	}
	if err := s.memoryTableStore.SaveRuntimeOperation(ctx, row); err != nil {
		return err
	}
	if phase == "" && stringValue(row["action"]) == "workspace.gateway_key.rotate" {
		var result map[string]any
		if json.Unmarshal([]byte(stringValue(row["result"])), &result) == nil {
			phase = stringValue(result["phase"])
		}
	}
	if !s.persistedResponseLost && s.failPersistedPhase != "" && phase == s.failPersistedPhase {
		s.persistedResponseLost = true
		return errors.New("phase persist response lost")
	}
	return nil
}

func (s *workspaceKeyRotationStore) CompareAndSwapWorkspaceAPIKey(ctx context.Context, workspaceID string, expectedID, newID int64) error {
	if err := s.memoryTableStore.CompareAndSwapWorkspaceAPIKey(ctx, workspaceID, expectedID, newID); err != nil {
		return err
	}
	if s.failCASAfterCommit && !s.casResponseLost {
		s.casResponseLost = true
		return errors.New("Workspace Key CAS response lost")
	}
	return nil
}

type workspaceKeyRotationFixture struct {
	server  http.Handler
	store   *workspaceKeyRotationStore
	session *httptest.ResponseRecorder
	email   string
	client  *workspaceKeyRotationClient
	fabric  *workspaceKeyRotationFabric
	ledger  *workspaceKeyRotationLedger
	service *controlplane.Service
}

func newWorkspaceKeyRotationFixture(t *testing.T, failAfter string) workspaceKeyRotationFixture {
	t.Helper()
	t.Setenv("OPL_MONTHLY_BILLING_WORKER_ENABLED", "false")
	t.Setenv("OPL_PROVIDER_RECONCILE_WORKER_ENABLED", "false")
	t.Setenv("OPL_ARCHIVE_RETENTION_WORKER_ENABLED", "false")
	store := &workspaceKeyRotationStore{memoryTableStore: newMemoryTableStore()}
	base := &customerFactsSub2API{testSub2APIClient: &testSub2APIClient{balance: 1_000_000_000, charges: map[string]int64{}}}
	client := &workspaceKeyRotationClient{customerFactsSub2API: base, keys: map[int64]clients.Sub2APIWorkspaceKey{
		9: {ID: 9, UserID: 41, Name: "opl-workspace", Key: "old-workspace-key-secret", Status: "active"},
	}, failAfter: failAfter}
	fabric := &workspaceKeyRotationFabric{fakeFabricClient: fakeFabricClient{
		gatewaySecret: clients.GatewaySecretWriteResult{SecretRef: "opl-gateway-ws-alpha", Version: "v2", Fingerprint: "sha256:f346c41dc52c526411868e85de9cda4bb694fe16f71d8c68823c93bf0bf21654"},
		runtimeStatus: clients.WorkspaceRuntime{ID: "runtime-alpha", WorkspaceID: "ws-alpha", Status: "running", ServiceName: "opl-compute-alpha", Ready: true, Access: clients.WorkspaceRuntimeAccess{
			Username: "opl", Password: "runtime-password-before", CredentialStatus: "configured", CredentialVersion: "v-before", SecretRef: "opl-compute-alpha-env",
		}},
	}, failAfter: failAfter}
	ledger := &workspaceKeyRotationLedger{receipts: map[string]clients.Receipt{}, failAfter: failAfter}
	service := controlplane.NewService(ledger, fabric, client)
	server, err := NewPersistentServer(service, store)
	if err != nil {
		t.Fatal(err)
	}
	session := tenantOwnerSessionForTest(t, server)
	ownerID := sessionUserIDForTest(t, server, session)
	users, _ := store.ListUsers(context.Background(), false)
	email := stringValue(findRecord(users, ownerID)["email"])
	seedRuntimeAccessWorkspaceForTest(t, store, ownerID, map[string]any{"workspaceApiKeyId": int64(9)})
	return workspaceKeyRotationFixture{server: server, store: store, session: session, email: email, client: client, fabric: fabric, ledger: ledger, service: service}
}

func (f workspaceKeyRotationFixture) rotate(t *testing.T, key string) *httptest.ResponseRecorder {
	t.Helper()
	return requestWithMutationKeyForTest(t, f.server, f.session, http.MethodPost, "/api/workspaces/ws-alpha/workspace-key/rotate", `{}`, key)
}

func (f *workspaceKeyRotationFixture) restart(t *testing.T) {
	t.Helper()
	server, err := NewPersistentServer(f.service, f.store)
	if err != nil {
		t.Fatal(err)
	}
	f.server = server
	f.session = loginForTest(t, server, f.email, "CorrectHorseBatteryStaple!")
}

func assertWorkspaceKeyRotationComplete(t *testing.T, fixture workspaceKeyRotationFixture, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("rotation status=%d body=%s", response.Code, response.Body.String())
	}
	fixture.client.mu.Lock()
	keys := fixture.client.keyList(41)
	fixture.client.mu.Unlock()
	workspaces, _ := fixture.store.ListWorkspaces(context.Background(), "acct-alpha")
	if len(keys) != 1 || keys[0].ID != 19 || keys[0].Name != workspaceReservedKeyName("ws-alpha") || keys[0].Status != "active" || len(workspaces) != 1 || int64(numberField(workspaces[0], "workspaceApiKeyId", 0)) != 19 || len(fixture.ledger.receipts) != 1 {
		t.Fatalf("rotation did not converge: keys=%#v Workspaces=%#v receipts=%#v", keys, workspaces, fixture.ledger.receipts)
	}
	if len(fixture.fabric.bindings) != 1 || fixture.fabric.bindings[0].WorkspaceID != "ws-alpha" || fixture.fabric.bindings[0].SecretRef != "opl-gateway-ws-alpha" ||
		fixture.fabric.bindings[0].WorkspaceAPIKeyID != 19 || fixture.fabric.bindings[0].Fingerprint != "sha256:f346c41dc52c526411868e85de9cda4bb694fe16f71d8c68823c93bf0bf21654" || len(fixture.fabric.runtimeInputs) != 0 {
		t.Fatalf("runtime Gateway binding=%#v runtime applies=%#v", fixture.fabric.bindings, fixture.fabric.runtimeInputs)
	}
	if access := fixture.fabric.runtimeStatus.Access; access.Username != "opl" || access.Password != "runtime-password-before" || access.CredentialVersion != "v-before" || access.SecretRef != "opl-compute-alpha-env" {
		t.Fatalf("Key rotation changed Runtime credentials: %#v", access)
	}
	if !strings.Contains(response.Body.String(), `"workspaceApiKeyId":"19"`) || !strings.Contains(response.Body.String(), `"fingerprint":"sha256:f346c41dc52c526411868e85de9cda4bb694fe16f71d8c68823c93bf0bf21654"`) {
		t.Fatalf("rotation DTO=%s", response.Body.String())
	}
}

func TestWorkspaceKeyRotationEveryPhaseResponseLoss(t *testing.T) {
	for _, phase := range []string{"create", "secret", "bind", "readback", "retire", "promote", "delete", "receipt"} {
		t.Run(phase, func(t *testing.T) {
			fixture := newWorkspaceKeyRotationFixture(t, phase)
			first := fixture.rotate(t, "rotate-loss-"+phase)
			if first.Code == http.StatusOK {
				t.Fatalf("%s response loss was not observed: %s", phase, first.Body.String())
			}
			assertWorkspaceKeyRotationComplete(t, fixture, fixture.rotate(t, "rotate-loss-"+phase))
		})
	}
}

func TestWorkspaceKeyRotationEveryPhaseRestart(t *testing.T) {
	for _, phase := range []string{"create", "secret", "bind", "readback", "retire", "promote", "delete", "receipt"} {
		t.Run(phase, func(t *testing.T) {
			fixture := newWorkspaceKeyRotationFixture(t, phase)
			if response := fixture.rotate(t, "rotate-restart-"+phase); response.Code == http.StatusOK {
				t.Fatalf("%s failure did not leave a recovery point", phase)
			}
			fixture.restart(t)
			assertWorkspaceKeyRotationComplete(t, fixture, fixture.rotate(t, "rotate-restart-"+phase))
		})
	}
}

func TestWorkspaceKeyRotationEveryPersistedPhaseResponseLossAndRestart(t *testing.T) {
	for _, phase := range []string{
		"replacement_check", "replacement_create", "secret_write", "runtime_bind", "runtime_readback",
		"workspace_commit", "retire_old", "promote_new", "delete_old", "receipt", "complete",
	} {
		t.Run(phase, func(t *testing.T) {
			fixture := newWorkspaceKeyRotationFixture(t, "")
			fixture.store.failPersistedPhase = phase
			if response := fixture.rotate(t, "rotate-persisted-loss-"+phase); response.Code == http.StatusOK {
				t.Fatalf("%s persisted response loss was not observed: %s", phase, response.Body.String())
			}
			fixture.restart(t)
			assertWorkspaceKeyRotationComplete(t, fixture, fixture.rotate(t, "rotate-persisted-loss-"+phase))
		})
	}
}

func TestWorkspaceKeyRotationWorkspaceCommitResponseLossAndRestart(t *testing.T) {
	fixture := newWorkspaceKeyRotationFixture(t, "")
	fixture.store.failCASAfterCommit = true
	first := fixture.rotate(t, "rotate-workspace-commit-loss")
	workspaces, _ := fixture.store.ListWorkspaces(context.Background(), "acct-alpha")
	if first.Code == http.StatusOK || len(workspaces) != 1 || int64(numberField(workspaces[0], "workspaceApiKeyId", 0)) != 19 {
		t.Fatalf("Workspace commit response loss status=%d body=%s Workspaces=%#v", first.Code, first.Body.String(), workspaces)
	}
	fixture.restart(t)
	assertWorkspaceKeyRotationComplete(t, fixture, fixture.rotate(t, "rotate-workspace-commit-loss"))
}

func TestWorkspaceKeyRotationSameKeyReplay(t *testing.T) {
	fixture := newWorkspaceKeyRotationFixture(t, "")
	first := fixture.rotate(t, "rotate-replay")
	assertWorkspaceKeyRotationComplete(t, fixture, first)
	writes := []int{fixture.client.createWrites, fixture.client.updateWrites, fixture.client.deleteWrites, len(fixture.fabric.gatewaySecretInputs), len(fixture.fabric.runtimeInputs), len(fixture.ledger.receipts)}
	replay := fixture.rotate(t, "rotate-replay")
	assertWorkspaceKeyRotationComplete(t, fixture, replay)
	got := []int{fixture.client.createWrites, fixture.client.updateWrites, fixture.client.deleteWrites, len(fixture.fabric.gatewaySecretInputs), len(fixture.fabric.runtimeInputs), len(fixture.ledger.receipts)}
	for index := range writes {
		if got[index] != writes[index] {
			t.Fatalf("same-key replay repeated side effects: before=%#v after=%#v", writes, got)
		}
	}
}

func TestWorkspaceKeyRotationCanRotateCanonicalKeyAgain(t *testing.T) {
	fixture := newWorkspaceKeyRotationFixture(t, "")
	assertWorkspaceKeyRotationComplete(t, fixture, fixture.rotate(t, "rotate-canonical-first"))
	firstWrites := []int{fixture.client.createWrites, fixture.client.updateWrites, fixture.client.deleteWrites, len(fixture.fabric.gatewaySecretInputs), len(fixture.fabric.bindings), len(fixture.ledger.receipts)}

	second := fixture.rotate(t, "rotate-canonical-second")
	if second.Code != http.StatusOK {
		t.Fatalf("second canonical rotation status=%d body=%s", second.Code, second.Body.String())
	}
	workspaces, _ := fixture.store.ListWorkspaces(context.Background(), "acct-alpha")
	if len(workspaces) != 1 {
		t.Fatalf("canonical rotation Workspaces=%#v", workspaces)
	}
	newKeyID := int64(numberField(workspaces[0], "workspaceApiKeyId", 0))
	fixture.client.mu.Lock()
	keys := fixture.client.keyList(41)
	fixture.client.mu.Unlock()
	if newKeyID <= 0 || newKeyID == 19 || len(keys) != 1 || keys[0].ID != newKeyID || keys[0].Name != workspaceReservedKeyName("ws-alpha") || keys[0].Status != "active" {
		t.Fatalf("second canonical rotation did not converge: keyId=%d keys=%#v Workspaces=%#v", newKeyID, keys, workspaces)
	}
	gotWrites := []int{fixture.client.createWrites, fixture.client.updateWrites, fixture.client.deleteWrites, len(fixture.fabric.gatewaySecretInputs), len(fixture.fabric.bindings), len(fixture.ledger.receipts)}
	for index, before := range firstWrites {
		if gotWrites[index] != before+1 && index != 1 {
			t.Fatalf("second canonical rotation side effects before=%#v after=%#v", firstWrites, gotWrites)
		}
	}
	if gotWrites[1] != firstWrites[1]+2 {
		t.Fatalf("second canonical rotation must retire old and promote replacement: before=%#v after=%#v", firstWrites, gotWrites)
	}
}

func TestWorkspaceKeyRotationDoesNotTouchSiblingWorkspaceKey(t *testing.T) {
	fixture := newWorkspaceKeyRotationFixture(t, "")
	sibling := clients.Sub2APIWorkspaceKey{ID: 29, UserID: 41, Name: workspaceReservedKeyName("ws-beta"), Key: "sibling-workspace-key-secret", Status: "active"}
	fixture.client.mu.Lock()
	fixture.client.keys[sibling.ID] = sibling
	fixture.client.mu.Unlock()

	response := fixture.rotate(t, "rotate-with-sibling")
	if response.Code != http.StatusOK {
		t.Fatalf("rotation with sibling status=%d body=%s", response.Code, response.Body.String())
	}
	fixture.client.mu.Lock()
	readback, ok := fixture.client.keys[sibling.ID]
	fixture.client.mu.Unlock()
	if !ok || !reflect.DeepEqual(readback, sibling) {
		t.Fatalf("sibling Workspace Key changed: before=%#v after=%#v", sibling, readback)
	}
}

func TestWorkspaceKeyRotationReplayReadsAuthoritativeState(t *testing.T) {
	fixture := newWorkspaceKeyRotationFixture(t, "")
	assertWorkspaceKeyRotationComplete(t, fixture, fixture.rotate(t, "rotate-authoritative-replay"))
	fixture.client.mu.Lock()
	delete(fixture.client.keys, 19)
	fixture.client.mu.Unlock()
	response := fixture.rotate(t, "rotate-authoritative-replay")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "workspace_key_rotation_conflict") {
		t.Fatalf("stale completed replay=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWorkspaceKeyRotationConcurrent(t *testing.T) {
	fixture := newWorkspaceKeyRotationFixture(t, "")
	fixture.client.createStarted, fixture.client.releaseCreate = make(chan struct{}), make(chan struct{})
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { firstDone <- fixture.rotate(t, "rotate-concurrent-first") }()
	<-fixture.client.createStarted
	second := fixture.rotate(t, "rotate-concurrent-second")
	if second.Code != http.StatusConflict || !strings.Contains(second.Body.String(), "workspace_key_rotation_in_progress") {
		t.Fatalf("concurrent rotation=%d body=%s", second.Code, second.Body.String())
	}
	close(fixture.client.releaseCreate)
	assertWorkspaceKeyRotationComplete(t, fixture, <-firstDone)
}

func TestWorkspaceKeyRotationTemporaryNameConflict(t *testing.T) {
	fixture := newWorkspaceKeyRotationFixture(t, "")
	name := workspaceReservedKeyName("ws-alpha")
	fixture.client.keys[18] = clients.Sub2APIWorkspaceKey{ID: 18, UserID: 41, Name: name, Key: "conflicting-secret", Status: "active"}
	response := fixture.rotate(t, "rotate-temp-conflict")
	operations, _ := fixture.store.ListRuntimeOperations(context.Background())
	if response.Code != http.StatusConflict || len(fixture.fabric.gatewaySecretInputs) != 0 || fixture.client.createWrites != 0 || len(operations) != 1 || stringValue(operations[0]["status"]) != "manual_review" {
		t.Fatalf("temporary conflict crossed mutation boundary or missed manual review: status=%d body=%s writes=%d fabric=%#v operations=%#v", response.Code, response.Body.String(), fixture.client.createWrites, fixture.fabric.gatewaySecretInputs, operations)
	}
}

func TestWorkspaceKeyRotationSecretSwitchedBeforeDatabaseCommit(t *testing.T) {
	fixture := newWorkspaceKeyRotationFixture(t, "")
	fixture.store.failPhase = "runtime_bind"
	first := fixture.rotate(t, "rotate-secret-db-loss")
	if first.Code == http.StatusOK || len(fixture.fabric.gatewaySecretInputs) != 1 {
		t.Fatalf("Secret/database loss point=%d body=%s writes=%#v", first.Code, first.Body.String(), fixture.fabric.gatewaySecretInputs)
	}
	assertWorkspaceKeyRotationComplete(t, fixture, fixture.rotate(t, "rotate-secret-db-loss"))
}

func TestWorkspaceKeyRotationKeepsOldKeyActiveUntilRuntimeReadbackAndWorkspaceCommit(t *testing.T) {
	fixture := newWorkspaceKeyRotationFixture(t, "")
	fixture.store.failPhase = "retire_old"
	first := fixture.rotate(t, "rotate-old-key-gate")
	fixture.client.mu.Lock()
	oldKey := fixture.client.keys[9]
	fixture.client.mu.Unlock()
	workspaces, _ := fixture.store.ListWorkspaces(context.Background(), "acct-alpha")
	if first.Code == http.StatusOK || oldKey.Status != "active" || int64(numberField(workspaces[0], "workspaceApiKeyId", 0)) != 19 || len(fixture.fabric.bindings) != 1 {
		t.Fatalf("old Key retired before runtime readback and Workspace commit: status=%d old=%#v Workspaces=%#v bindings=%#v", first.Code, oldKey, workspaces, fixture.fabric.bindings)
	}
	assertWorkspaceKeyRotationComplete(t, fixture, fixture.rotate(t, "rotate-old-key-gate"))
}

func TestWorkspaceKeySecretAndNoRawPersistence(t *testing.T) {
	fixture := newWorkspaceKeyRotationFixture(t, "")
	response := fixture.rotate(t, "rotate-no-raw")
	assertWorkspaceKeyRotationComplete(t, fixture, response)
	operations, _ := fixture.store.ListRuntimeOperations(context.Background())
	audits, _ := fixture.store.ListAuditEvents(context.Background(), "acct-alpha")
	raw := string(mustJSON(map[string]any{"response": response.Body.String(), "operations": operations, "audits": audits, "ledger": fixture.ledger.inputs}))
	for _, secret := range []string{"old-workspace-key-secret", "replacement-workspace-key-secret", "test-user-delegated-token"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("rotation persisted %q: %s", secret, raw)
		}
	}
	if len(fixture.fabric.gatewaySecretInputs) != 1 || fixture.fabric.gatewaySecretInputs[0].GatewayAPIKey != "replacement-workspace-key-secret" ||
		fixture.fabric.gatewaySecretInputs[0].AccountID != "acct-alpha" || fixture.fabric.gatewaySecretInputs[0].WorkspaceID != "ws-alpha" ||
		fixture.fabric.gatewaySecretInputs[0].WorkspaceAPIKeyID != 19 || fixture.fabric.gatewaySecretInputs[0].Fingerprint == "" {
		t.Fatalf("replacement raw Key did not stay on the transient Fabric write: %#v", fixture.fabric.gatewaySecretInputs)
	}
}
