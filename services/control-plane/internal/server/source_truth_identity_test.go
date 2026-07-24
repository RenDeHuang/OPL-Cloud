package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

type sourceTruthIdentityClient struct {
	*testSub2APIClient
	mu              sync.Mutex
	users           map[int64]clients.Sub2APIIdentity
	balances        map[int64]int64
	userErrs        map[int64]error
	userIDs         []int64
	adminUserIDs    []int64
	batchUsageCalls int
}

func (c *sourceTruthIdentityClient) User(_ context.Context, userID int64) (clients.Sub2APIIdentity, error) {
	c.userIDs = append(c.userIDs, userID)
	if err := c.userErrs[userID]; err != nil {
		return clients.Sub2APIIdentity{}, err
	}
	identity, ok := c.users[userID]
	if !ok {
		return clients.Sub2APIIdentity{}, errors.New("identity unavailable")
	}
	return identity, nil
}

func (c *sourceTruthIdentityClient) AdminUser(_ context.Context, userID int64) (clients.Sub2APIUser, error) {
	c.mu.Lock()
	c.adminUserIDs = append(c.adminUserIDs, userID)
	c.mu.Unlock()
	if err := c.userErrs[userID]; err != nil {
		return clients.Sub2APIUser{}, err
	}
	identity, ok := c.users[userID]
	if !ok {
		return clients.Sub2APIUser{}, errors.New("identity unavailable")
	}
	return clients.Sub2APIUser{
		ID: identity.ID, Email: identity.Email, BalanceUSDMicros: c.balances[userID], Status: identity.Status,
		CreatedAt: time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC),
	}, nil
}

func (c *sourceTruthIdentityClient) BatchUsersUsage(_ context.Context, userIDs []int64) (map[int64]clients.Sub2APIBatchUserUsage, error) {
	c.batchUsageCalls++
	result := make(map[int64]clients.Sub2APIBatchUserUsage, len(userIDs))
	for _, id := range userIDs {
		result[id] = clients.Sub2APIBatchUserUsage{UserID: id}
	}
	return result, nil
}

func TestAuthMeUsesOnlySessionIdentityAndLiveSub2APIUser(t *testing.T) {
	client := &sourceTruthIdentityClient{
		testSub2APIClient: &testSub2APIClient{charges: map[string]int64{}},
		users:             map[int64]clients.Sub2APIIdentity{41: {ID: 41, Email: "gateway-owner@example.com", Status: "disabled"}},
		userErrs:          map[int64]error{},
	}
	server, session := newGatewayOwnerTestServer(t, client, nil)
	response := requestWithSession(t, server, session, http.MethodGet, "/api/auth/me?accountId=acct-other&sub2apiUserId=999", "")
	if response.Code != http.StatusOK {
		t.Fatalf("auth me = %d: %s", response.Code, response.Body.String())
	}
	if got, want := response.Header().Get("x-opl-csrf-token"), session.Header().Get("x-opl-csrf-token"); got == "" || got != want {
		t.Fatalf("auth me csrf recovery header = %q, want login token", got)
	}
	var envelope map[string]any
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	data := mapField(envelope, "data")
	if envelope["source"] != "sub2api" || envelope["status"] != "available" || envelope["available"] != true || len(data) != 6 {
		t.Fatalf("auth me envelope = %#v", envelope)
	}
	if data["consoleUserId"] != "usr-gateway-owner" || data["accountId"] != "acct-gateway" || data["role"] != "owner" || data["sub2apiUserId"] != "41" || data["email"] != "gateway-owner@example.com" || data["status"] != "disabled" {
		t.Fatalf("auth me data = %#v", data)
	}
	if len(client.userIDs) != 1 || client.userIDs[0] != 41 {
		t.Fatalf("auth me readback IDs = %#v", client.userIDs)
	}
	if _, err := time.Parse(time.RFC3339Nano, stringValue(envelope["fetchedAt"])); err != nil {
		t.Fatalf("auth me fetchedAt = %#v", envelope["fetchedAt"])
	}

	legacy := requestWithSession(t, server, session, http.MethodGet, "/api/me", "")
	if legacy.Code != http.StatusNotFound {
		t.Fatalf("legacy /api/me = %d: %s", legacy.Code, legacy.Body.String())
	}

	client.users[41] = clients.Sub2APIIdentity{ID: 41, Email: "mismatch@example.com", Status: "active"}
	mismatch := requestWithSession(t, server, session, http.MethodGet, "/api/auth/me", "")
	assertUnavailableIdentityEnvelope(t, mismatch, http.StatusBadGateway, "sub2api")
	client.users[41] = clients.Sub2APIIdentity{ID: 99, Email: "gateway-owner@example.com", Status: "active"}
	mismatch = requestWithSession(t, server, session, http.MethodGet, "/api/auth/me", "")
	assertUnavailableIdentityEnvelope(t, mismatch, http.StatusBadGateway, "sub2api")
	client.users[41] = clients.Sub2APIIdentity{ID: 41, Email: "gateway-owner@example.com", Status: "active"}
	client.userErrs[41] = errors.New("Sub2API unavailable")
	unavailable := requestWithSession(t, server, session, http.MethodGet, "/api/auth/me", "")
	assertUnavailableIdentityEnvelope(t, unavailable, http.StatusBadGateway, "sub2api")
}

func TestOperatorAccountsJoinsControlPlaneMappingWithPaginatedBatchSub2APIReadback(t *testing.T) {
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-beta", "org-beta", "usr-beta", "beta@example.com")
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	client := &sourceTruthIdentityClient{
		testSub2APIClient: &testSub2APIClient{charges: map[string]int64{}},
		users: map[int64]clients.Sub2APIIdentity{
			41: {ID: 41, Email: "alpha@example.com", Status: "active"},
			42: {ID: 42, Email: "beta@example.com", Status: "disabled"},
		},
		balances: map[int64]int64{41: 12_340_000, 42: 5_670_000},
		userErrs: map[int64]error{},
	}
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, client), store)
	if err != nil {
		t.Fatal(err)
	}
	operator := reservedOperatorSessionForTest(t, server)
	response := requestWithSession(t, server, operator, http.MethodGet, "/api/operator/accounts", "")
	if response.Code != http.StatusOK {
		t.Fatalf("operator accounts = %d: %s", response.Code, response.Body.String())
	}
	var envelope map[string]any
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	data := mapField(envelope, "data")
	items, _ := data["items"].([]any)
	if envelope["source"] != "control-plane+sub2api" || envelope["status"] != "available" || len(items) != 3 || data["total"] != float64(3) || data["page"] != float64(1) || data["pageSize"] != float64(20) {
		t.Fatalf("operator accounts envelope = %#v", envelope)
	}
	alpha, beta := operatorAccountItem(items, "acct-alpha"), operatorAccountItem(items, "acct-beta")
	alphaWallet := mapField(alpha, "wallet")
	if alpha["accountId"] != "acct-alpha" || alpha["consoleUserId"] != "usr-alpha" || alpha["role"] != "owner" || alpha["sub2apiUserId"] != "41" || alpha["email"] != "alpha@example.com" || alpha["status"] != "active" || alphaWallet["available"] != true || mapField(alphaWallet, "data")["usdMicros"] != float64(12_340_000) || alphaWallet["sourceUpdatedAt"] != "2026-07-19T00:00:00Z" {
		t.Fatalf("alpha mapping = %#v", alpha)
	}
	if beta["accountId"] != "acct-beta" || beta["sub2apiUserId"] != "42" || beta["email"] != "beta@example.com" || beta["status"] != "active" || mapField(mapField(beta, "gatewayIdentity"), "data")["status"] != "disabled" {
		t.Fatalf("beta mapping = %#v", beta)
	}
	exactIDs := slices.Clone(client.adminUserIDs)
	slices.Sort(exactIDs)
	if !slices.Equal(exactIDs, []int64{1, 41, 42}) || client.batchUsageCalls != 1 || len(client.userIDs) != 0 {
		t.Fatalf("operator exact readback users=%#v usage=%d identityReads=%#v", exactIDs, client.batchUsageCalls, client.userIDs)
	}

	client.userIDs, client.adminUserIDs, client.batchUsageCalls = nil, nil, 0
	customer := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	forbidden := requestWithSession(t, server, customer, http.MethodGet, "/api/operator/accounts", "")
	if forbidden.Code != http.StatusForbidden || len(client.adminUserIDs) != 0 || client.batchUsageCalls != 0 {
		t.Fatalf("customer operator accounts = %d users=%#v usage=%d: %s", forbidden.Code, client.adminUserIDs, client.batchUsageCalls, forbidden.Body.String())
	}

	client.users[41] = clients.Sub2APIIdentity{ID: 41, Email: "mismatch@example.com", Status: "active"}
	mismatch := requestWithSession(t, server, operator, http.MethodGet, "/api/operator/accounts", "")
	if mismatch.Code != http.StatusOK {
		t.Fatalf("one-account mismatch = %d: %s", mismatch.Code, mismatch.Body.String())
	}
	var mismatchEnvelope map[string]any
	if err := json.NewDecoder(mismatch.Body).Decode(&mismatchEnvelope); err != nil {
		t.Fatal(err)
	}
	mismatchAlpha := operatorAccountItem(mapField(mismatchEnvelope, "data")["items"].([]any), "acct-alpha")
	if mapField(mismatchAlpha, "gatewayIdentity")["available"] != false || mapField(mismatchAlpha, "wallet")["available"] != false || mapField(mismatchAlpha, "usage")["available"] != false {
		t.Fatalf("mismatched account sources = %#v", mismatchAlpha)
	}
	client.users[41] = clients.Sub2APIIdentity{ID: 41, Email: "alpha@example.com", Status: "active"}
	delete(client.users, 42)
	unavailable := requestWithSession(t, server, operator, http.MethodGet, "/api/operator/accounts", "")
	if unavailable.Code != http.StatusOK {
		t.Fatalf("one-account unavailable = %d: %s", unavailable.Code, unavailable.Body.String())
	}
	var unavailableEnvelope map[string]any
	if err := json.NewDecoder(unavailable.Body).Decode(&unavailableEnvelope); err != nil {
		t.Fatal(err)
	}
	unavailableBeta := operatorAccountItem(mapField(unavailableEnvelope, "data")["items"].([]any), "acct-beta")
	if mapField(unavailableBeta, "gatewayIdentity")["available"] != false || mapField(unavailableBeta, "wallet")["available"] != false {
		t.Fatalf("unavailable account sources = %#v", unavailableBeta)
	}
}

func assertUnavailableIdentityEnvelope(t *testing.T, response *httptest.ResponseRecorder, wantStatus int, source string) {
	t.Helper()
	if response.Code != wantStatus {
		t.Fatalf("unavailable status = %d, want %d: %s", response.Code, wantStatus, response.Body.String())
	}
	var envelope map[string]any
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope) != 4 || envelope["source"] != source || envelope["status"] != "unavailable" || envelope["available"] != false || envelope["data"] != nil {
		t.Fatalf("unavailable identity envelope = %#v", envelope)
	}
}
