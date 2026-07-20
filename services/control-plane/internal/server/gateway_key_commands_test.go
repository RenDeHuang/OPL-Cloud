package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

type gatewayKeyCommandClient struct {
	*customerFactsSub2API
	keys                   map[int64]clients.Sub2APIWorkspaceKey
	credentials            []clients.SessionDelegatedCredential
	createInputs           []clients.Sub2APICreateKeyInput
	createKeys             []string
	createCalls            int
	updateInputs           []clients.Sub2APIUpdateKeyInput
	updateCalls            int
	updateErr              error
	updateFailsBeforeWrite bool
	deleted                []int64
	deleteCalls            int
	deleteErr              error
	deleteFailsBeforeWrite bool
	userKeyReadIDs         []int64
}

func (c *gatewayKeyCommandClient) rememberCredential(credential clients.SessionDelegatedCredential) error {
	c.credentials = append(c.credentials, credential)
	if credential.Bearer != "test-user-delegated-token" {
		return errors.New("wrong delegated credential")
	}
	return nil
}

func (c *gatewayKeyCommandClient) UserKeys(_ context.Context, credential clients.SessionDelegatedCredential, userID int64) ([]clients.Sub2APIWorkspaceKey, error) {
	if err := c.rememberCredential(credential); err != nil {
		return nil, err
	}
	keys := make([]clients.Sub2APIWorkspaceKey, 0, len(c.keys))
	for _, key := range c.keys {
		if key.UserID == userID {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func (c *gatewayKeyCommandClient) UserKey(_ context.Context, credential clients.SessionDelegatedCredential, userID, keyID int64) (clients.Sub2APIWorkspaceKey, error) {
	if err := c.rememberCredential(credential); err != nil {
		return clients.Sub2APIWorkspaceKey{}, err
	}
	c.userKeyReadIDs = append(c.userKeyReadIDs, keyID)
	key, ok := c.keys[keyID]
	if !ok || key.UserID != userID {
		return clients.Sub2APIWorkspaceKey{}, clients.ErrSub2APIKeyNotFound
	}
	return key, nil
}

func (c *gatewayKeyCommandClient) CreateUserKey(_ context.Context, credential clients.SessionDelegatedCredential, userID int64, input clients.Sub2APICreateKeyInput, idempotencyKey string) (clients.Sub2APIWorkspaceKey, error) {
	if err := c.rememberCredential(credential); err != nil {
		return clients.Sub2APIWorkspaceKey{}, err
	}
	c.createInputs = append(c.createInputs, input)
	c.createKeys = append(c.createKeys, idempotencyKey)
	c.createCalls++
	expiresAt := time.Date(2026, 8, 18, 1, 2, 3, 0, time.UTC)
	key := clients.Sub2APIWorkspaceKey{ID: 19, UserID: userID, Name: input.Name, Key: "created-key-secret", Status: "active", QuotaUSDMicros: input.QuotaUSDMicros, ExpiresAt: &expiresAt}
	c.keys[key.ID] = key
	return key, nil
}

func (c *gatewayKeyCommandClient) UpdateUserKey(_ context.Context, credential clients.SessionDelegatedCredential, userID, keyID int64, input clients.Sub2APIUpdateKeyInput) (clients.Sub2APIWorkspaceKey, error) {
	if err := c.rememberCredential(credential); err != nil {
		return clients.Sub2APIWorkspaceKey{}, err
	}
	key, ok := c.keys[keyID]
	if !ok || key.UserID != userID {
		return clients.Sub2APIWorkspaceKey{}, clients.ErrSub2APIKeyNotFound
	}
	c.updateCalls++
	if c.updateErr != nil && c.updateFailsBeforeWrite {
		return clients.Sub2APIWorkspaceKey{}, c.updateErr
	}
	c.updateInputs = append(c.updateInputs, input)
	if input.Name != nil {
		key.Name = *input.Name
	}
	if input.QuotaUSDMicros != nil {
		key.QuotaUSDMicros = *input.QuotaUSDMicros
	}
	if input.Enabled != nil {
		key.Status = "disabled"
		if *input.Enabled {
			key.Status = "active"
		}
	}
	c.keys[keyID] = key
	if c.updateErr != nil {
		return clients.Sub2APIWorkspaceKey{}, c.updateErr
	}
	return key, nil
}

func (c *gatewayKeyCommandClient) DeleteUserKey(_ context.Context, credential clients.SessionDelegatedCredential, userID, keyID int64) error {
	if err := c.rememberCredential(credential); err != nil {
		return err
	}
	key, ok := c.keys[keyID]
	if !ok || key.UserID != userID {
		return clients.ErrSub2APIKeyNotFound
	}
	c.deleteCalls++
	if c.deleteErr != nil && c.deleteFailsBeforeWrite {
		return c.deleteErr
	}
	c.deleted = append(c.deleted, keyID)
	delete(c.keys, keyID)
	return c.deleteErr
}

func newGatewayKeyCommandFixture(t *testing.T) (http.Handler, *gatewayKeyCommandClient, *memoryTableStore, *httptest.ResponseRecorder) {
	t.Helper()
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-gateway", "org-gateway", "usr-gateway-owner", "gateway-owner@example.com")
	base := &customerFactsSub2API{
		testSub2APIClient: &testSub2APIClient{balance: 100_000_000, charges: map[string]int64{}},
		usagePage: clients.Sub2APIUsagePage{Items: []clients.Sub2APIUsageRecord{{
			UserID: 41, APIKeyID: 17, RequestID: "req-17", CreatedAt: time.Date(2026, 7, 18, 1, 2, 3, 0, time.UTC),
			Model: "gpt-5", InboundEndpoint: "/v1/responses", RequestType: "sync", InputTokens: 1, OutputTokens: 2, ActualCostUSDMicros: 3,
		}}, Total: 1, Page: 1, PageSize: 20, Pages: 1},
		usageStats: clients.Sub2APIUsageStats{TotalRequests: 4, TotalInputTokens: 5, TotalOutputTokens: 6, TotalTokens: 11, TotalActualCostUSDMicros: 12},
		history:    map[int64][]clients.Sub2APIBalanceHistoryEntry{},
	}
	expiresAt := time.Date(2026, 8, 18, 1, 2, 3, 0, time.UTC)
	client := &gatewayKeyCommandClient{customerFactsSub2API: base, keys: map[int64]clients.Sub2APIWorkspaceKey{
		9:  {ID: 9, UserID: 41, Name: "opl-workspace", Key: "workspace-key-secret", Status: "active", QuotaUSDMicros: 50_000_000},
		17: {ID: 17, UserID: 41, Name: "general-key", Key: "general-key-secret", Status: "active", QuotaUSDMicros: 10_000_000, ExpiresAt: &expiresAt},
		18: {ID: 18, UserID: 42, Name: "other-user-key", Key: "other-user-secret", Status: "active"},
	}}
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, client), store)
	if err != nil {
		t.Fatal(err)
	}
	session := loginForTest(t, server, "gateway-owner@example.com", "CorrectHorseBatteryStaple!")
	return server, client, store, session
}

func TestGatewayPublicEndpointIsHardCut(t *testing.T) {
	server, _, _, session := newGatewayKeyCommandFixture(t)
	response := requestWithSession(t, server, session, http.MethodGet, "/api/gateway/endpoint", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("endpoint status = %d, want 404: %s", response.Code, response.Body.String())
	}
}

func TestGatewayGeneralKey(t *testing.T) {
	server, client, store, session := newGatewayKeyCommandFixture(t)
	listed := requestWithSession(t, server, session, http.MethodGet, "/api/gateway/keys", "")
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), "key-secret") {
		t.Fatalf("list status = %d: %s", listed.Code, listed.Body.String())
	}
	items := mapField(decodeSourceEnvelope(t, listed), "data")["items"].([]any)
	for _, raw := range items {
		key := raw.(map[string]any)
		if key["id"] == "9" && (key["kind"] != "workspace" || key["manageable"] != false || key["deletable"] != false) {
			t.Fatalf("workspace key protection = %#v", key)
		}
		if key["id"] == "17" && (key["kind"] != "general" || key["manageable"] != true || key["deletable"] != true || key["expiresAt"] != "2026-08-18T01:02:03Z") {
			t.Fatalf("general key projection = %#v", key)
		}
	}

	created := requestWithMutationKeyForTest(t, server, session, http.MethodPost, "/api/gateway/keys", `{"name":"new-general","quotaUsdMicros":1250000,"expiresInDays":30}`, "create-general-once")
	if created.Code != http.StatusCreated || strings.Contains(created.Body.String(), "created-key-secret") {
		t.Fatalf("create status = %d: %s", created.Code, created.Body.String())
	}
	createdData := mapField(decodeSourceEnvelope(t, created), "data")
	if createdData["id"] != "19" || createdData["expiresAt"] != "2026-08-18T01:02:03Z" || len(client.createInputs) != 1 || client.createKeys[0] != "create-general-once" {
		t.Fatalf("created data=%#v inputs=%#v keys=%#v", createdData, client.createInputs, client.createKeys)
	}

	updated := requestWithMutationKeyForTest(t, server, session, http.MethodPatch, "/api/gateway/keys/19", `{"enabled":false}`, "disable-general-once")
	if updated.Code != http.StatusOK || mapField(decodeSourceEnvelope(t, updated), "data")["status"] != "disabled" || len(client.updateInputs) != 1 {
		t.Fatalf("update status = %d inputs=%#v: %s", updated.Code, client.updateInputs, updated.Body.String())
	}
	deleted := requestWithMutationKeyForTest(t, server, session, http.MethodDelete, "/api/gateway/keys/19", `{}`, "delete-general-once")
	if deleted.Code != http.StatusOK || len(client.deleted) != 1 || client.deleted[0] != 19 {
		t.Fatalf("delete status = %d deleted=%#v: %s", deleted.Code, client.deleted, deleted.Body.String())
	}

	for _, operation := range store.runtimeOps {
		encoded, _ := json.Marshal(operation)
		if strings.Contains(string(encoded), "key-secret") || strings.Contains(string(encoded), "delegated-user-token") {
			t.Fatalf("operation leaked secret: %s", encoded)
		}
	}
	for _, audit := range store.auditEvents {
		encoded, _ := json.Marshal(audit)
		if strings.Contains(string(encoded), "key-secret") || strings.Contains(string(encoded), "delegated-user-token") {
			t.Fatalf("audit leaked secret: %s", encoded)
		}
	}
	for _, credential := range client.credentials {
		if credential.Bearer != "test-user-delegated-token" {
			t.Fatalf("wrong delegated credential: %#v", credential)
		}
	}
}

func TestGatewayGeneralKeyReplayConvergence(t *testing.T) {
	server, client, store, session := newGatewayKeyCommandFixture(t)
	create := func(body string) *httptest.ResponseRecorder {
		return requestWithMutationKeyForTest(t, server, session, http.MethodPost, "/api/gateway/keys", body, "create-replay")
	}
	if first := create(`{"name":"replay-key","quotaUsdMicros":1250000}`); first.Code != http.StatusCreated {
		t.Fatalf("first create = %d: %s", first.Code, first.Body.String())
	}
	if replay := create(`{"name":"replay-key","quotaUsdMicros":1250000}`); replay.Code != http.StatusOK || client.createCalls != 1 {
		t.Fatalf("create replay = %d calls=%d: %s", replay.Code, client.createCalls, replay.Body.String())
	}
	if conflict := create(`{"name":"different-key","quotaUsdMicros":1250000}`); conflict.Code != http.StatusConflict || client.createCalls != 1 {
		t.Fatalf("create conflict = %d calls=%d: %s", conflict.Code, client.createCalls, conflict.Body.String())
	}

	client.updateErr = errors.New("response lost after update")
	updated := requestWithMutationKeyForTest(t, server, session, http.MethodPatch, "/api/gateway/keys/19", `{"enabled":false}`, "update-response-loss")
	if updated.Code != http.StatusOK || client.updateCalls != 1 || client.keys[19].Status != "disabled" {
		t.Fatalf("lost update response = %d calls=%d key=%#v: %s", updated.Code, client.updateCalls, client.keys[19], updated.Body.String())
	}
	client.updateErr, client.updateFailsBeforeWrite = errors.New("response lost before update"), true
	unknown := requestWithMutationKeyForTest(t, server, session, http.MethodPatch, "/api/gateway/keys/19", `{"enabled":true}`, "update-unknown")
	if unknown.Code != http.StatusBadGateway || client.updateCalls != 2 {
		t.Fatalf("unknown update = %d calls=%d: %s", unknown.Code, client.updateCalls, unknown.Body.String())
	}
	replayedUnknown := requestWithMutationKeyForTest(t, server, session, http.MethodPatch, "/api/gateway/keys/19", `{"enabled":true}`, "update-unknown")
	if replayedUnknown.Code != http.StatusBadGateway || client.updateCalls != 2 {
		t.Fatalf("unknown update replay rewrote upstream = %d calls=%d: %s", replayedUnknown.Code, client.updateCalls, replayedUnknown.Body.String())
	}

	client.deleteErr, client.deleteFailsBeforeWrite = errors.New("response lost after delete"), false
	deleted := requestWithMutationKeyForTest(t, server, session, http.MethodDelete, "/api/gateway/keys/19", `{}`, "delete-response-loss")
	if deleted.Code != http.StatusOK || client.deleteCalls != 1 {
		t.Fatalf("lost delete response = %d calls=%d: %s", deleted.Code, client.deleteCalls, deleted.Body.String())
	}
	restarted, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, client), store)
	if err != nil {
		t.Fatal(err)
	}
	restartedSession := loginForTest(t, restarted, "gateway-owner@example.com", "CorrectHorseBatteryStaple!")
	replayDelete := requestWithMutationKeyForTest(t, restarted, restartedSession, http.MethodDelete, "/api/gateway/keys/19", `{}`, "delete-response-loss")
	if replayDelete.Code != http.StatusOK || client.deleteCalls != 1 {
		t.Fatalf("delete restart replay = %d calls=%d: %s", replayDelete.Code, client.deleteCalls, replayDelete.Body.String())
	}
}

func TestGatewayKeyOwnership(t *testing.T) {
	server, client, _, session := newGatewayKeyCommandFixture(t)
	for _, request := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/gateway/keys/18", ""},
		{http.MethodPost, "/api/gateway/keys/18/reveal", `{}`},
		{http.MethodGet, "/api/gateway/keys/18/usage", ""},
		{http.MethodPatch, "/api/gateway/keys/18", `{"enabled":false}`},
		{http.MethodDelete, "/api/gateway/keys/18", `{}`},
	} {
		response := requestWithMutationKeyForTest(t, server, session, request.method, request.path+"?accountId=acct-other&user_id=42&api_key_id=18", request.body, "ownership-"+strconv.Itoa(len(client.userKeyReadIDs)))
		if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "other-user") {
			t.Fatalf("cross-account %s %s = %d: %s", request.method, request.path, response.Code, response.Body.String())
		}
	}
	if len(client.updateInputs) != 0 || len(client.deleted) != 0 || client.usageQuery.UserID != 0 {
		t.Fatalf("cross-account request reached mutation/usage: updates=%#v deletes=%#v usage=%#v", client.updateInputs, client.deleted, client.usageQuery)
	}
}

func TestGatewayKeySecret(t *testing.T) {
	server, _, store, session := newGatewayKeyCommandFixture(t)
	revealed := requestWithSession(t, server, session, http.MethodPost, "/api/gateway/keys/17/reveal", `{}`)
	if revealed.Code != http.StatusOK || revealed.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("reveal status = %d cache=%q: %s", revealed.Code, revealed.Header().Get("Cache-Control"), revealed.Body.String())
	}
	data := mapField(decodeSourceEnvelope(t, revealed), "data")
	if data["id"] != "17" || data["value"] != "general-key-secret" {
		t.Fatalf("reveal data = %#v", data)
	}
	state := requestWithSession(t, server, session, http.MethodGet, "/api/state", "")
	audits, _ := store.ListAuditEvents(context.Background(), "acct-gateway")
	encoded, _ := json.Marshal(audits)
	if strings.Contains(state.Body.String(), "general-key-secret") || strings.Contains(string(encoded), "general-key-secret") {
		t.Fatalf("secret escaped dedicated response: state=%s audits=%s", state.Body.String(), encoded)
	}
}

func TestGatewayPerKeyUsage(t *testing.T) {
	server, client, _, session := newGatewayKeyCommandFixture(t)
	response := requestWithSession(t, server, session, http.MethodGet, "/api/gateway/keys/17/usage?page=1&pageSize=20&user_id=42&api_key_id=18", "")
	if response.Code != http.StatusOK {
		t.Fatalf("usage status = %d: %s", response.Code, response.Body.String())
	}
	items := mapField(decodeSourceEnvelope(t, response), "data")["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["apiKeyId"] != "17" || client.usageQuery != (clients.Sub2APIUsageQuery{UserID: 41, APIKeyID: 17, Page: 1, PageSize: 20}) {
		t.Fatalf("usage items=%#v query=%#v", items, client.usageQuery)
	}

	stats := requestWithSession(t, server, session, http.MethodGet, "/api/gateway/keys/17/usage-summary?period=month&user_id=42&api_key_id=18", "")
	if stats.Code != http.StatusOK || client.statsQuery != (clients.Sub2APIUsageStatsQuery{UserID: 41, APIKeyID: 17, Period: "month"}) {
		t.Fatalf("stats status=%d query=%#v: %s", stats.Code, client.statsQuery, stats.Body.String())
	}
}

func TestGatewayAccountUsageSummary(t *testing.T) {
	server, client, _, session := newGatewayKeyCommandFixture(t)
	response := requestWithSession(t, server, session, http.MethodGet, "/api/gateway/usage-summary?period=month&user_id=42&api_key_id=18", "")
	if response.Code != http.StatusOK {
		t.Fatalf("account usage status = %d: %s", response.Code, response.Body.String())
	}
	data := mapField(decodeSourceEnvelope(t, response), "data")
	if data["totalRequests"] != float64(4) || client.statsQuery != (clients.Sub2APIUsageStatsQuery{UserID: 41, Period: "month"}) {
		t.Fatalf("account usage data=%#v query=%#v", data, client.statsQuery)
	}
}
