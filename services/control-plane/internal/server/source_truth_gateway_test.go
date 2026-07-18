package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
)

type sourceTruthGatewayClient struct {
	*customerFactsSub2API
	balanceErr error
	keys       []clients.Sub2APIWorkspaceKey
	keysErr    error
	keyUserIDs []int64
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
	if len(activeKey) != 9 || activeKey["id"] != "9" || activeKey["status"] != "active" || activeKey["quotaUsdMicros"] != float64(10) {
		t.Fatalf("active key = %#v", activeKey)
	}

	usage := requestWithSession(t, server, session, http.MethodGet, "/api/gateway/usage"+spoofed+"&page=1&pageSize=50", "")
	if usage.Code != http.StatusOK {
		t.Fatalf("usage = %d: %s", usage.Code, usage.Body.String())
	}
	usageEnvelope := decodeSourceEnvelope(t, usage)
	usageItems, _ := mapField(usageEnvelope, "data")["items"].([]any)
	if len(usageItems) != 1 || usageItems[0].(map[string]any)["apiKeyId"] != "9" {
		t.Fatalf("usage envelope = %#v", usageEnvelope)
	}

	stats := requestWithSession(t, server, session, http.MethodGet, "/api/gateway/usage/stats"+spoofed+"&period=month", "")
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

	if len(client.keyUserIDs) != 1 || client.keyUserIDs[0] != 41 || base.usageQuery.UserID != 41 || base.usageQuery.APIKeyID != 9 || base.statsQuery.UserID != 41 || base.statsQuery.APIKeyID != 9 || len(base.historyIDs) != 1 || base.historyIDs[0] != 41 {
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

	for _, path := range []string{"/api/gateway/keys", "/api/gateway/usage", "/api/gateway/balance-history"} {
		t.Run("empty "+path, func(t *testing.T) {
			server, session := newGatewayOwnerTestServer(t, baseClient(), nil)
			response := requestWithSession(t, server, session, http.MethodGet, path, "")
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
		{name: "usage", path: "/api/gateway/usage", mutate: func(c *sourceTruthGatewayClient) { c.usageErr = errors.New("usage unavailable") }},
		{name: "usage pagination", path: "/api/gateway/usage", mutate: func(c *sourceTruthGatewayClient) { c.usageErr = errors.New("invalid sub2api usage pagination") }},
		{name: "stats", path: "/api/gateway/usage/stats", mutate: func(c *sourceTruthGatewayClient) { c.statsErr = errors.New("stats unavailable") }},
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
	client := &testSub2APIClient{charges: map[string]int64{}, workspaceKey: clients.Sub2APIWorkspaceKey{
		ID: 9, UserID: 41, Name: "opl-workspace", Key: "workspace-secret", Status: "active",
	}}
	server, session := newGatewayOwnerTestServer(t, client, nil)
	response := requestWithSession(t, server, session, http.MethodPost, "/api/gateway/keys/opl-workspace/reveal?accountId=acct-other&sub2apiUserId=999", "{}")
	if response.Code != http.StatusOK {
		t.Fatalf("reveal = %d: %s", response.Code, response.Body.String())
	}
	envelope := decodeSourceEnvelope(t, response)
	data := mapField(envelope, "data")
	if envelope["status"] != "available" || len(data) != 4 || data["id"] != "9" || data["name"] != "opl-workspace" || data["status"] != "active" || data["value"] != "workspace-secret" {
		t.Fatalf("reveal envelope = %#v", envelope)
	}
	if response.Header().Get("Cache-Control") != "private, no-store" || len(client.workspaceKeyUserIDs) != 1 || client.workspaceKeyUserIDs[0] != 41 {
		t.Fatalf("reveal boundary cache=%q users=%#v", response.Header().Get("Cache-Control"), client.workspaceKeyUserIDs)
	}
}
