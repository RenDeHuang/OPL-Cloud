package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

type sourceTruthIdentityClient struct {
	*testSub2APIClient
	users    map[int64]clients.Sub2APIIdentity
	userErrs map[int64]error
	userIDs  []int64
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

func TestOperatorAccountsJoinsControlPlaneMappingWithSequentialSub2APIReadback(t *testing.T) {
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-beta", "org-beta", "usr-beta", "beta@example.com")
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	client := &sourceTruthIdentityClient{
		testSub2APIClient: &testSub2APIClient{charges: map[string]int64{}},
		users: map[int64]clients.Sub2APIIdentity{
			41: {ID: 41, Email: "alpha@example.com", Status: "active"},
			42: {ID: 42, Email: "beta@example.com", Status: "disabled"},
		},
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
	if envelope["source"] != "control-plane+sub2api" || envelope["status"] != "available" || len(items) != 2 || data["total"] != float64(2) {
		t.Fatalf("operator accounts envelope = %#v", envelope)
	}
	alpha, beta := items[0].(map[string]any), items[1].(map[string]any)
	if len(alpha) != 6 || alpha["accountId"] != "acct-alpha" || alpha["consoleUserId"] != "usr-alpha" || alpha["role"] != "owner" || alpha["sub2apiUserId"] != "41" || alpha["email"] != "alpha@example.com" || alpha["status"] != "active" {
		t.Fatalf("alpha mapping = %#v", alpha)
	}
	if beta["accountId"] != "acct-beta" || beta["sub2apiUserId"] != "42" || beta["email"] != "beta@example.com" || beta["status"] != "disabled" {
		t.Fatalf("beta mapping = %#v", beta)
	}
	if len(client.userIDs) != 2 || client.userIDs[0] != 41 || client.userIDs[1] != 42 {
		t.Fatalf("operator sequential readback = %#v", client.userIDs)
	}

	client.userIDs = nil
	customer := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	forbidden := requestWithSession(t, server, customer, http.MethodGet, "/api/operator/accounts", "")
	if forbidden.Code != http.StatusForbidden || len(client.userIDs) != 0 {
		t.Fatalf("customer operator accounts = %d calls=%#v: %s", forbidden.Code, client.userIDs, forbidden.Body.String())
	}

	client.users[41] = clients.Sub2APIIdentity{ID: 41, Email: "mismatch@example.com", Status: "active"}
	mismatch := requestWithSession(t, server, operator, http.MethodGet, "/api/operator/accounts", "")
	assertUnavailableIdentityEnvelope(t, mismatch, http.StatusBadGateway, "control-plane+sub2api")
	client.users[41] = clients.Sub2APIIdentity{ID: 41, Email: "alpha@example.com", Status: "active"}
	client.userErrs[42] = errors.New("Sub2API unavailable")
	unavailable := requestWithSession(t, server, operator, http.MethodGet, "/api/operator/accounts", "")
	assertUnavailableIdentityEnvelope(t, unavailable, http.StatusBadGateway, "control-plane+sub2api")
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
