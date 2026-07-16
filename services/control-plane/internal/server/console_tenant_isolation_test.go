package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/lib/pq"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

func TestGatewaySummaryIsAlwaysMaskedAndOwnerRevealIsAudited(t *testing.T) {
	t.Setenv("OPL_CONSOLE_USERS_JSON", `[{"id":"usr-gateway-owner","email":"gateway-owner@example.com","password":"correct horse battery staple","role":"owner","accountId":"acct-gateway","sub2apiUserId":41}]`)
	lastUsedAt := time.Date(2026, 7, 16, 8, 9, 10, 0, time.UTC)
	sub2API := &testSub2APIClient{
		balance: 123,
		charges: map[string]int64{},
		workspaceKey: clients.Sub2APIWorkspaceKey{
			ID: 9, UserID: 41, Name: "opl-workspace", Key: "workspace-key-secret", Status: "active",
			QuotaUSDMicros: 50_000_000, QuotaUsedUSDMicros: 7_000_000, Usage5hUSDMicros: 1_000_000,
			Usage1dUSDMicros: 2_000_000, Usage7dUSDMicros: 3_000_000, LastUsedAt: &lastUsedAt,
		},
	}
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, sub2API))
	session := loginForTest(t, server, "gateway-owner@example.com", "correct horse battery staple")
	var logs bytes.Buffer
	previousLogOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousLogOutput) })

	masked := requestWithSession(t, server, session, http.MethodGet, "/api/gateway/summary?sub2apiUserId=999", "")
	if masked.Code != http.StatusOK {
		t.Fatalf("masked Gateway summary status = %d, want 200: %s", masked.Code, masked.Body.String())
	}
	if got := masked.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("masked Gateway Cache-Control = %q", got)
	}
	var summary map[string]any
	if err := json.NewDecoder(masked.Body).Decode(&summary); err != nil {
		t.Fatal(err)
	}
	account, balance := mapField(summary, "account"), mapField(summary, "balance")
	key, usage := mapField(summary, "apiKey"), mapField(summary, "usage")
	if numberField(account, "sub2apiUserId", 0) != 41 || stringValue(account["status"]) != "active" {
		t.Fatalf("Gateway account = %#v", account)
	}
	if stringValue(balance["source"]) != "sub2api" || stringValue(balance["status"]) != "available" || balance["available"] != true || numberField(balance, "usdMicros", 0) != 123 {
		t.Fatalf("Gateway balance = %#v", balance)
	}
	if numberField(key, "id", 0) != 9 || stringValue(key["maskedValue"]) != "work...cret" || key["revealed"] != false {
		t.Fatalf("masked Gateway key = %#v", key)
	}
	if _, ok := key["value"]; ok || strings.Contains(masked.Body.String(), "workspace-key-secret") {
		t.Fatalf("masked Gateway response leaked Key: %s", masked.Body.String())
	}
	if numberField(usage, "quotaUsdMicros", 0) != 50_000_000 || numberField(usage, "quotaUsedUsdMicros", 0) != 7_000_000 ||
		numberField(usage, "usage5hUsdMicros", 0) != 1_000_000 || numberField(usage, "usage1dUsdMicros", 0) != 2_000_000 ||
		numberField(usage, "usage7dUsdMicros", 0) != 3_000_000 || stringValue(usage["lastUsedAt"]) != lastUsedAt.Format(time.RFC3339) {
		t.Fatalf("Gateway usage = %#v", usage)
	}

	ignoredReveal := requestWithSession(t, server, session, http.MethodGet, "/api/gateway/summary?reveal=true&sub2apiUserId=999", "")
	if ignoredReveal.Code != http.StatusOK || ignoredReveal.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("masked reveal query response = %d Cache-Control %q: %s", ignoredReveal.Code, ignoredReveal.Header().Get("Cache-Control"), ignoredReveal.Body.String())
	}
	if err := json.NewDecoder(ignoredReveal.Body).Decode(&summary); err != nil {
		t.Fatal(err)
	}
	key = mapField(summary, "apiKey")
	if key["revealed"] != false || stringValue(key["value"]) != "" {
		t.Fatalf("GET reveal query exposed Gateway key = %#v", key)
	}
	if encoded, _ := json.Marshal(summary); strings.Contains(string(encoded), "workspace-key-secret") {
		t.Fatalf("GET reveal query leaked Gateway key: %s", encoded)
	}

	revealed := requestWithSession(t, server, session, http.MethodPost, "/api/gateway/keys/opl-workspace/reveal", "{}")
	if revealed.Code != http.StatusOK || revealed.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("revealed Gateway response = %d Cache-Control %q: %s", revealed.Code, revealed.Header().Get("Cache-Control"), revealed.Body.String())
	}
	var revealBody map[string]any
	if err := json.NewDecoder(revealed.Body).Decode(&revealBody); err != nil {
		t.Fatal(err)
	}
	revealedKey := mapField(revealBody, "apiKey")
	if numberField(revealedKey, "id", 0) != 9 || stringValue(revealedKey["name"]) != "opl-workspace" || stringValue(revealedKey["status"]) != "active" || stringValue(revealedKey["value"]) != "workspace-key-secret" {
		t.Fatalf("revealed Gateway key = %#v", revealedKey)
	}

	crossAccount := requestWithSession(t, server, session, http.MethodGet, "/api/gateway/summary?accountId=acct-other", "")
	if crossAccount.Code != http.StatusForbidden || !strings.Contains(crossAccount.Body.String(), "account_scope_forbidden") {
		t.Fatalf("cross-account Gateway status = %d, want 403: %s", crossAccount.Code, crossAccount.Body.String())
	}
	state := requestWithSession(t, server, session, http.MethodGet, "/api/state", "")
	if state.Code != http.StatusOK {
		t.Fatalf("state status = %d: %s", state.Code, state.Body.String())
	}
	if strings.Contains(state.Body.String(), "workspace-key-secret") || strings.Contains(state.Body.String(), "maskedValue") || strings.Contains(state.Body.String(), "usage5hUsdMicros") {
		t.Fatalf("state leaked Gateway Key projection: %s", state.Body.String())
	}
	handler := server.(*controlPlaneHTTPHandler)
	auditEvents, err := handler.app.tables.ListAuditEvents(context.Background(), "acct-gateway")
	if err != nil || !slices.ContainsFunc(auditEvents, func(event map[string]any) bool {
		return stringValue(event["action"]) == "gateway.key_reveal" && stringValue(event["actorUserId"]) == "usr-gateway-owner" && stringValue(event["targetAccountId"]) == "acct-gateway" && stringValue(event["result"]) == "succeeded"
	}) {
		t.Fatalf("Gateway reveal audit events = %#v err=%v", auditEvents, err)
	}
	auditJSON, _ := json.Marshal(auditEvents)
	if strings.Contains(string(auditJSON), "workspace-key-secret") || strings.Contains(string(auditJSON), "work...cret") || strings.Contains(logs.String(), "workspace-key-secret") {
		t.Fatalf("Gateway reveal leaked Key outside response: audit=%s logs=%s", auditJSON, logs.String())
	}
	if len(sub2API.workspaceKeyUserIDs) != 3 || slices.ContainsFunc(sub2API.workspaceKeyUserIDs, func(id int64) bool { return id != 41 }) {
		t.Fatalf("Gateway used caller-supplied Sub2API identity: %#v", sub2API.workspaceKeyUserIDs)
	}
}

func TestGatewayRevealRejectsUnauthorizedWithoutFetchingKey(t *testing.T) {
	for _, role := range []string{"admin", "member"} {
		t.Run(role, func(t *testing.T) {
			t.Setenv("OPL_CONSOLE_USERS_JSON", `[{"id":"usr-gateway-`+role+`","email":"gateway-`+role+`@example.com","password":"correct horse battery staple","role":"`+role+`","accountId":"acct-gateway","sub2apiUserId":41}]`)
			sub2API := &testSub2APIClient{balance: 123, charges: map[string]int64{}}
			server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, sub2API))
			session := loginForTest(t, server, "gateway-"+role+"@example.com", "correct horse battery staple")
			rec := requestWithSession(t, server, session, http.MethodPost, "/api/gateway/keys/opl-workspace/reveal", "{}")
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "gateway_key_reveal_forbidden") || len(sub2API.workspaceKeyUserIDs) != 0 {
				t.Fatalf("%s reveal = %d calls=%#v: %s", role, rec.Code, sub2API.workspaceKeyUserIDs, rec.Body.String())
			}
		})
	}

	t.Run("operator", func(t *testing.T) {
		t.Setenv("OPL_CONSOLE_USERS_JSON", "")
		sub2API := &testSub2APIClient{balance: 123, charges: map[string]int64{}}
		server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, sub2API))
		rec := requestWithSession(t, server, reservedOperatorSessionForTest(t, server), http.MethodPost, "/api/gateway/keys/opl-workspace/reveal", "{}")
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "gateway_key_reveal_forbidden") || len(sub2API.workspaceKeyUserIDs) != 0 {
			t.Fatalf("operator reveal = %d calls=%#v: %s", rec.Code, sub2API.workspaceKeyUserIDs, rec.Body.String())
		}
	})

	for _, tc := range []struct {
		name string
		path string
		csrf bool
		code string
	}{
		{name: "cross account", path: "/api/gateway/keys/opl-workspace/reveal?accountId=acct-other", csrf: true, code: "account_scope_forbidden"},
		{name: "missing csrf", path: "/api/gateway/keys/opl-workspace/reveal", code: "csrf_token_invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OPL_CONSOLE_USERS_JSON", `[{"id":"usr-gateway-owner","email":"gateway-owner@example.com","password":"correct horse battery staple","role":"owner","accountId":"acct-gateway","sub2apiUserId":41}]`)
			sub2API := &testSub2APIClient{balance: 123, charges: map[string]int64{}}
			server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, sub2API))
			session := loginForTest(t, server, "gateway-owner@example.com", "correct horse battery staple")
			req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewBufferString("{}"))
			req.Header.Set("Content-Type", "application/json")
			addSessionCookies(req, session)
			if tc.csrf {
				req.Header.Set("x-opl-csrf", session.Header().Get("x-opl-csrf-token"))
			}
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), tc.code) || len(sub2API.workspaceKeyUserIDs) != 0 {
				t.Fatalf("%s reveal = %d calls=%#v: %s", tc.name, rec.Code, sub2API.workspaceKeyUserIDs, rec.Body.String())
			}
		})
	}
}

func TestGatewayRevealAuditFailureDoesNotReturnKey(t *testing.T) {
	t.Setenv("OPL_CONSOLE_USERS_JSON", `[{"id":"usr-gateway-owner","email":"gateway-owner@example.com","password":"correct horse battery staple","role":"owner","accountId":"acct-gateway","sub2apiUserId":41}]`)
	store := &failingResumeCommitStore{memoryTableStore: newMemoryTableStore()}
	sub2API := &testSub2APIClient{balance: 123, charges: map[string]int64{}}
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, sub2API), store)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	previousLogOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousLogOutput) })
	session := loginForTest(t, server, "gateway-owner@example.com", "correct horse battery staple")
	rec := requestWithSession(t, server, session, http.MethodPost, "/api/gateway/keys/opl-workspace/reveal", "{}")
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "state_persist_failed") {
		t.Fatalf("audit failure reveal = %d: %s", rec.Code, rec.Body.String())
	}
	state := requestWithSession(t, server, session, http.MethodGet, "/api/state", "")
	auditEvents, auditErr := store.ListAuditEvents(context.Background(), "acct-gateway")
	joined := rec.Body.String() + state.Body.String() + logs.String()
	if strings.Contains(joined, "workspace-key-secret") || strings.Contains(joined, "work...cret") || auditErr != nil || len(auditEvents) != 0 || len(sub2API.workspaceKeyUserIDs) != 1 {
		t.Fatalf("audit failure leaked or persisted Key: calls=%#v audit=%#v err=%v output=%s", sub2API.workspaceKeyUserIDs, auditEvents, auditErr, joined)
	}
}

func TestGatewayRevealFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "missing Key", err: clients.ErrSub2APIWorkspaceKeyMissing, wantStatus: http.StatusConflict, wantCode: "gateway_key_missing"},
		{name: "ambiguous Key", err: clients.ErrSub2APIWorkspaceKeyAmbiguous, wantStatus: http.StatusConflict, wantCode: "gateway_key_ambiguous"},
		{name: "upstream unavailable", err: errors.New("Sub2API unavailable"), wantStatus: http.StatusBadGateway, wantCode: "upstream_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OPL_CONSOLE_USERS_JSON", `[{"id":"usr-gateway-owner","email":"gateway-owner@example.com","password":"correct horse battery staple","role":"owner","accountId":"acct-gateway","sub2apiUserId":41}]`)
			sub2API := &testSub2APIClient{balance: 123, charges: map[string]int64{}, workspaceKeyErr: tc.err}
			server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, sub2API))
			session := loginForTest(t, server, "gateway-owner@example.com", "correct horse battery staple")
			rec := requestWithSession(t, server, session, http.MethodPost, "/api/gateway/keys/opl-workspace/reveal", "{}")
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantCode) || strings.Contains(rec.Body.String(), "workspace-key-secret") || len(sub2API.workspaceKeyUserIDs) != 1 {
				t.Fatalf("Gateway reveal error = %d calls=%#v, want %d %s: %s", rec.Code, sub2API.workspaceKeyUserIDs, tc.wantStatus, tc.wantCode, rec.Body.String())
			}
		})
	}
}

func TestGatewaySummaryFailsClosed(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		operator   bool
		wantStatus int
		wantCode   string
	}{
		{name: "unmapped account", operator: true, wantStatus: http.StatusConflict, wantCode: "sub2api_account_mapping_required"},
		{name: "missing Key", err: clients.ErrSub2APIWorkspaceKeyMissing, wantStatus: http.StatusConflict, wantCode: "gateway_key_missing"},
		{name: "ambiguous Key", err: clients.ErrSub2APIWorkspaceKeyAmbiguous, wantStatus: http.StatusConflict, wantCode: "gateway_key_ambiguous"},
		{name: "upstream unavailable", err: fmt.Errorf("Sub2API unavailable"), wantStatus: http.StatusBadGateway, wantCode: "upstream_unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub2API := &testSub2APIClient{balance: 123, charges: map[string]int64{}, workspaceKeyErr: tt.err}
			server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, sub2API))
			var session *httptest.ResponseRecorder
			if tt.operator {
				session = reservedOperatorSessionForTest(t, server)
			} else {
				t.Setenv("OPL_CONSOLE_USERS_JSON", `[{"id":"usr-gateway-owner","email":"gateway-owner@example.com","password":"correct horse battery staple","role":"owner","accountId":"acct-gateway","sub2apiUserId":41}]`)
				server = NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, sub2API))
				session = loginForTest(t, server, "gateway-owner@example.com", "correct horse battery staple")
			}
			rec := requestWithSession(t, server, session, http.MethodGet, "/api/gateway/summary", "")
			if rec.Code != tt.wantStatus || !strings.Contains(rec.Body.String(), tt.wantCode) {
				t.Fatalf("Gateway error response = %d, want %d %s: %s", rec.Code, tt.wantStatus, tt.wantCode, rec.Body.String())
			}
			if rec.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("Gateway error Cache-Control = %q", rec.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestBootstrapUsersUseOnlyFixedRoles(t *testing.T) {
	t.Setenv("OPL_CONSOLE_USERS_JSON", `[{"email":"owner@example.com","password":"correct horse battery staple","sub2apiUserId":41}]`)
	users, err := bootstrapUsersFromEnv()
	if err != nil {
		t.Fatalf("bootstrap users: %v", err)
	}
	if got := stringValue(users[0]["role"]); got != "owner" {
		t.Fatalf("default role = %q, want owner", got)
	}

	t.Setenv("OPL_CONSOLE_USERS_JSON", `[{"email":"pi@example.com","password":"correct horse battery staple","role":"pi","sub2apiUserId":41}]`)
	if _, err := bootstrapUsersFromEnv(); err == nil || !strings.Contains(err.Error(), "invalid_role") {
		t.Fatalf("invalid explicit role error = %v, want invalid_role", err)
	}
}

func TestBootstrapUsersRejectDuplicateSub2APIUserMapping(t *testing.T) {
	t.Setenv("OPL_CONSOLE_USERS_JSON", `[{"id":"usr-one","email":"one@example.com","password":"correct horse battery staple","role":"owner","accountId":"acct-one","sub2apiUserId":41},{"id":"usr-two","email":"two@example.com","password":"correct horse battery staple","role":"owner","accountId":"acct-two","sub2apiUserId":41}]`)
	if _, err := newControlPlaneAppWithStore(newMemoryTableStore()); err == nil || err.Error() != "sub2api_account_mapping_conflict" {
		t.Fatalf("duplicate bootstrap mapping error = %v", err)
	}
}

func TestBootstrapOwnerGetsAnActiveTenantMembership(t *testing.T) {
	t.Setenv("OPL_CONSOLE_USERS_JSON", `[{"id":"usr-seed-owner","email":"seed-owner@example.com","password":"correct horse battery staple","role":"owner","accountId":"acct-seed","sub2apiUserId":41}]`)
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	session := loginForTest(t, server, "seed-owner@example.com", "correct horse battery staple")
	req := httptest.NewRequest(http.MethodGet, "/api/state?accountId=acct-seed", nil)
	addSessionCookies(req, session)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap owner state status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestBootstrapAdminGetsAnActiveTenantMembership(t *testing.T) {
	t.Setenv("OPL_CONSOLE_USERS_JSON", `[{"id":"usr-seed-admin","email":"seed-admin@example.com","password":"correct horse battery staple","role":"admin","accountId":"acct-seed","sub2apiUserId":41}]`)
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	session := loginForTest(t, server, "seed-admin@example.com", "correct horse battery staple")
	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	addSessionCookies(req, session)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap admin session status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload["isOperator"] != false {
		t.Fatalf("bootstrap admin isOperator = %#v, want false", payload["isOperator"])
	}
	stateReq := httptest.NewRequest(http.MethodGet, "/api/state?accountId=acct-seed", nil)
	addSessionCookies(stateReq, session)
	stateRec := httptest.NewRecorder()
	server.ServeHTTP(stateRec, stateReq)
	if stateRec.Code != http.StatusOK {
		t.Fatalf("bootstrap admin state status = %d, want 200: %s", stateRec.Code, stateRec.Body.String())
	}
}

func TestOperatorPasswordResetRevokesSessions(t *testing.T) {
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}), store)
	if err != nil {
		t.Fatal(err)
	}
	oldSession := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	if forbidden := requestWithSession(t, server, oldSession, http.MethodPost, "/api/users/usr-alpha/reset-password", `{"password":"NewCorrectHorseBatteryStaple!"}`); forbidden.Code != http.StatusForbidden {
		t.Fatalf("customer password reset status = %d, want 403: %s", forbidden.Code, forbidden.Body.String())
	}
	operator := reservedOperatorSessionForTest(t, server)
	if missing := requestWithSession(t, server, operator, http.MethodPost, "/api/users/usr-alpha/reset-password", `{}`); missing.Code != http.StatusBadRequest {
		t.Fatalf("empty password reset status = %d, want 400: %s", missing.Code, missing.Body.String())
	}
	reset := requestWithSession(t, server, operator, http.MethodPost, "/api/users/usr-alpha/reset-password", `{"password":"NewCorrectHorseBatteryStaple!"}`)
	if reset.Code != http.StatusOK {
		t.Fatalf("password reset status = %d, want 200: %s", reset.Code, reset.Body.String())
	}
	for _, secret := range []string{"NewCorrectHorseBatteryStaple!", "password", "passwordHash"} {
		if strings.Contains(reset.Body.String(), secret) {
			t.Fatalf("password reset response leaked %q: %s", secret, reset.Body.String())
		}
	}

	assertSessionUnauthorized(t, server, oldSession)
	if oldLogin := loginAttemptForTest(server, "alpha@example.com", "CorrectHorseBatteryStaple!", ""); oldLogin.Code != http.StatusUnauthorized {
		t.Fatalf("old password login status = %d, want 401: %s", oldLogin.Code, oldLogin.Body.String())
	}
	loginForTest(t, server, "alpha@example.com", "NewCorrectHorseBatteryStaple!")

	audits, err := store.ListAuditEvents(context.Background(), "acct-alpha")
	if err != nil || len(audits) == 0 || stringValue(audits[len(audits)-1]["action"]) != "user.password_reset" {
		t.Fatalf("password reset audit = %#v err=%v", audits, err)
	}
	auditJSON, err := json.Marshal(audits[len(audits)-1])
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"NewCorrectHorseBatteryStaple!", `"password":`, `"passwordHash":`, passwordHashPrefix} {
		if strings.Contains(string(auditJSON), secret) {
			t.Fatalf("password reset audit leaked %q: %s", secret, auditJSON)
		}
	}
}

func TestOrganizationRejectsMissingBillingAccount(t *testing.T) {
	app := newControlPlaneApp()
	if _, err := app.createOrganization(map[string]any{"name": "Orphan", "billingAccountId": "acct-missing"}); err == nil {
		t.Fatal("organization with missing billing account was accepted")
	}
}

func TestMembershipRejectsMissingReferences(t *testing.T) {
	app := newControlPlaneApp()
	if _, err := app.createMembership(map[string]any{"organizationId": "org-missing", "userId": "usr-missing", "accountId": "acct-missing", "role": "member"}); err == nil {
		t.Fatal("membership with missing references was accepted")
	}
}

func TestBackendRejectsRolesOutsideOwnerAdminMember(t *testing.T) {
	app := newControlPlaneApp()
	mustStore(t, app.tables.SaveAccount(context.Background(), map[string]any{"id": "acct-alpha", "status": "active"}))
	mustStore(t, app.tables.SaveOrganization(context.Background(), map[string]any{"id": "org-alpha", "billingAccountId": "acct-alpha", "status": "active"}))
	mustStore(t, app.tables.SaveUser(context.Background(), map[string]any{"id": "usr-member", "email": "member@example.com", "accountId": "acct-alpha", "role": "member", "status": "active"}))
	for _, role := range []string{"pi", "viewer", "OWNER"} {
		if _, err := app.createUser(context.Background(), nil, map[string]any{"email": role + "@example.com", "accountId": "acct-alpha", "role": role, "password": "correct horse battery staple"}); err == nil || err.Error() != "invalid_role" {
			t.Fatalf("user role %q error = %v, want invalid_role", role, err)
		}
		if _, err := app.createMembership(map[string]any{"organizationId": "org-alpha", "userId": "usr-member", "accountId": "acct-alpha", "role": role}); err == nil || err.Error() != "invalid_role" {
			t.Fatalf("membership role %q error = %v, want invalid_role", role, err)
		}
	}
}

func TestAdminCannotUseCustomerEndpointsAcrossAccountsOrOrganizations(t *testing.T) {
	app := newControlPlaneApp()
	req := requestForStoredUser(t, app, map[string]any{"id": "usr-tenant-admin", "email": "admin@example.com", "accountId": "acct-alpha", "role": "admin", "status": "active"})
	mustStore(t, app.tables.SaveMembership(context.Background(), map[string]any{"id": "mem-tenant-admin", "organizationId": "org-alpha", "userId": "usr-tenant-admin", "accountId": "acct-alpha", "role": "admin", "status": "active"}))

	rec := httptest.NewRecorder()
	if _, ok := app.scopedAccountID(rec, req, map[string]any{"accountId": "acct-other"}); ok || rec.Code != http.StatusForbidden {
		t.Fatalf("cross-account admin scope status=%d ok=%v, want 403 false", rec.Code, ok)
	}
	if app.canAccessResource(req, map[string]any{"id": "ws-other", "accountId": "acct-other"}) {
		t.Fatal("admin accessed another account resource through a customer endpoint")
	}

	rec = httptest.NewRecorder()
	if app.authorizeOrganization(rec, req, "org-other") || rec.Code != http.StatusForbidden {
		t.Fatalf("cross-organization admin status=%d, want 403", rec.Code)
	}
}

func TestMissingComputeReadReturnsNotFoundWithoutFabric(t *testing.T) {
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	calls := []string{}
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{calls: &calls}), store)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/compute-allocations/compute-missing", nil)
	addSessionCookies(req, loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!"))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound || len(calls) != 0 {
		t.Fatalf("missing compute status=%d calls=%#v body=%s", rec.Code, calls, rec.Body.String())
	}
}

func TestOwnedProvisioningComputeMayRefreshFromFabric(t *testing.T) {
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	mustStore(t, store.SaveCompute(context.Background(), map[string]any{"id": "compute-alpha", "accountId": "acct-alpha", "status": "provisioning"}))
	calls := []string{}
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, &provisioningComputeFabricClient{fakeFabricClient{calls: &calls}}), store)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/compute-allocations/compute-alpha", nil)
	addSessionCookies(req, loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!"))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !slices.Contains(calls, "fabric.compute-sync") || !strings.Contains(rec.Body.String(), `"accountId":"acct-alpha"`) {
		t.Fatalf("provisioning compute status=%d calls=%#v body=%s", rec.Code, calls, rec.Body.String())
	}
}

func TestCustomerStateContainsOnlySessionTenant(t *testing.T) {
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	mustStore(t, store.SaveAccount(context.Background(), map[string]any{"id": "acct-beta", "status": "active"}))
	mustStore(t, store.SaveUser(context.Background(), map[string]any{"id": "usr-beta", "email": "beta-secret@example.com", "accountId": "acct-beta", "role": "member", "status": "active"}))
	mustStore(t, store.SaveWorkspace(context.Background(), map[string]any{"id": "workspace-beta", "accountId": "acct-beta", "ownerAccountId": "acct-beta", "status": "running"}))
	mustStore(t, store.SaveCompute(context.Background(), map[string]any{"id": "compute-beta", "accountId": "acct-beta", "status": "running"}))
	mustStore(t, store.SaveStorage(context.Background(), map[string]any{"id": "storage-beta", "accountId": "acct-beta", "status": "available"}))
	mustStore(t, store.SaveRuntimeOperation(context.Background(), map[string]any{"id": "operation-beta", "operationId": "operation-beta", "accountId": "acct-beta", "workspaceId": "workspace-beta", "status": "succeeded"}))
	mustStore(t, store.SaveBillingReconciliation(context.Background(), map[string]any{"id": "global", "status": "mismatch", "guardStatus": "blocked", "guardReason": "global-secret", "guardBlockNewWorkspaces": true}))
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}), store)
	if err != nil {
		t.Fatal(err)
	}
	member := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	rec := requestWithSession(t, server, member, http.MethodGet, "/api/state", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("state status = %d: %s", rec.Code, rec.Body.String())
	}
	for _, secret := range []string{"acct-beta", "beta-secret@example.com", "workspace-beta", "compute-beta", "storage-beta", "operation-beta"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("state leaked %q: %s", secret, rec.Body.String())
		}
	}
	if !strings.Contains(rec.Body.String(), "alpha@example.com") {
		t.Fatalf("state omitted current user: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "billingReconciliation") || strings.Contains(rec.Body.String(), "global-secret") {
		t.Fatalf("state leaked global reconciliation: %s", rec.Body.String())
	}
}

func TestUnknownCustomerResourceMutationsNeverReachFabric(t *testing.T) {
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	calls := []string{}
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{calls: &calls}), store)
	if err != nil {
		t.Fatal(err)
	}
	member := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	for _, tc := range []struct{ path, body string }{
		{"/api/compute-allocations/compute-missing/destroy", `{"confirm":true}`},
		{"/api/storage-volumes/destroy", `{"storageId":"storage-missing","confirmDataLoss":true}`},
		{"/api/storage-attachments/detach", `{"attachmentId":"attachment-missing"}`},
		{"/api/storage-attachments", `{"computeAllocationId":"compute-missing","storageId":"storage-missing","workspaceId":"workspace-alpha"}`},
	} {
		before := len(calls)
		rec := requestWithSession(t, server, member, http.MethodPost, tc.path, tc.body)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusBadRequest {
			t.Fatalf("unknown mutation %s status = %d: %s", tc.path, rec.Code, rec.Body.String())
		}
		if len(calls) != before {
			t.Fatalf("unknown mutation %s reached Fabric: %#v", tc.path, calls[before:])
		}
	}
}

func TestReconciliationBlockedCustomerMutationsDoNotLeakGlobalProjection(t *testing.T) {
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	mustStore(t, store.SaveBillingReconciliation(context.Background(), map[string]any{
		"id": "global", "status": "mismatch",
		"guard":   map[string]any{"status": "blocked", "reason": "private-operator-reason", "blockNewWorkspaces": true},
		"message": map[string]any{"author": "operator-secret", "text": "cross-tenant-private-report"},
	}))
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}), store)
	if err != nil {
		t.Fatal(err)
	}
	member := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	for _, tc := range []struct{ path, body string }{
		{"/api/compute-allocations", `{"packageId":"basic"}`},
		{"/api/storage-volumes", `{"sizeGb":10}`},
	} {
		rec := requestWithSession(t, server, member, http.MethodPost, tc.path, tc.body)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"error":"billing_reconciliation_blocked"`) {
			t.Fatalf("blocked %s status=%d body=%s", tc.path, rec.Code, rec.Body.String())
		}
		for _, secret := range []string{"billingReconciliation", "private-operator-reason", "operator-secret", "cross-tenant-private-report", "messageText", "messageAuthor"} {
			if strings.Contains(rec.Body.String(), secret) {
				t.Fatalf("blocked %s leaked %q: %s", tc.path, secret, rec.Body.String())
			}
		}
	}
}

func TestCustomerSupportScopeAllRemainsTenantScoped(t *testing.T) {
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	mustStore(t, store.SaveSupportMapping(context.Background(), map[string]any{"id": "support-alpha", "accountId": "acct-alpha", "externalTicketId": "ALPHA-1"}))
	mustStore(t, store.SaveSupportMapping(context.Background(), map[string]any{"id": "support-beta", "accountId": "acct-beta", "externalTicketId": "BETA-1"}))
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}), store)
	if err != nil {
		t.Fatal(err)
	}
	member := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	rec := requestWithSession(t, server, member, http.MethodGet, "/api/support/tickets?scope=all", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ALPHA-1") || strings.Contains(rec.Body.String(), "BETA-1") {
		t.Fatalf("tenant support scope status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func seedTenantMember(t *testing.T, store controlPlaneTableStore, accountID, organizationID, userID, email string) {
	t.Helper()
	sub2APIUserID := int64(41)
	if accountID == "acct-beta" {
		sub2APIUserID = 42
	}
	mustStore(t, store.SaveAccount(context.Background(), map[string]any{"id": accountID, "status": "active", "sub2apiUserId": sub2APIUserID}))
	mustStore(t, store.SaveOrganization(context.Background(), map[string]any{"id": organizationID, "billingAccountId": accountID, "status": "active"}))
	hash, err := hashPassword("CorrectHorseBatteryStaple!")
	if err != nil {
		t.Fatal(err)
	}
	mustStore(t, store.SaveUser(context.Background(), map[string]any{"id": userID, "email": email, "accountId": accountID, "role": "member", "status": "active", "passwordHash": hash}))
	mustStore(t, store.SaveMembership(context.Background(), map[string]any{"id": "mem-" + userID, "organizationId": organizationID, "userId": userID, "accountId": accountID, "role": "member", "status": "active"}))
}

func TestMembershipRevocationAndUserDisableTakeEffectImmediately(t *testing.T) {
	app := newControlPlaneApp()
	user := map[string]any{"id": "usr-member", "email": "member@example.com", "accountId": "acct-alpha", "role": "member", "status": "active"}
	mustStore(t, app.tables.SaveAccount(context.Background(), map[string]any{"id": "acct-alpha", "status": "active"}))
	mustStore(t, app.tables.SaveOrganization(context.Background(), map[string]any{"id": "org-alpha", "billingAccountId": "acct-alpha", "status": "active"}))
	req := requestForStoredUser(t, app, user)
	membership := map[string]any{"id": "mem-alpha", "organizationId": "org-alpha", "userId": "usr-member", "accountId": "acct-alpha", "role": "member", "status": "active"}
	mustStore(t, app.tables.SaveMembership(context.Background(), membership))

	if !app.authorizeOrganization(httptest.NewRecorder(), req, "org-alpha") {
		t.Fatal("active membership was denied")
	}
	membership["status"] = "revoked"
	mustStore(t, app.tables.SaveMembership(context.Background(), membership))
	rec := httptest.NewRecorder()
	if app.authorizeOrganization(rec, req, "org-alpha") || rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked membership status=%d, want 401", rec.Code)
	}

	user["status"] = "disabled"
	mustStore(t, app.tables.SaveUser(context.Background(), user))
	if _, ok := app.session(req); ok {
		t.Fatal("disabled user retained an active session")
	}
}

func TestRevokeMembershipImmediatelyDeniesCustomerEndpoints(t *testing.T) {
	store := newMemoryTableStore()
	mustStore(t, store.SaveOrganization(context.Background(), map[string]any{"id": "org-alpha", "billingAccountId": "acct-alpha", "status": "active"}))
	mustStore(t, store.SaveMembership(context.Background(), map[string]any{"id": "mem-admin", "organizationId": "org-alpha", "userId": "usr-admin", "accountId": "acct-alpha", "role": "admin", "status": "active"}))
	hash, err := hashPassword("CorrectHorseBatteryStaple!")
	if err != nil {
		t.Fatal(err)
	}
	mustStore(t, store.SaveUser(context.Background(), map[string]any{"id": "usr-member", "email": "member@alpha.example", "accountId": "acct-alpha", "role": "member", "status": "active", "passwordHash": hash}))
	mustStore(t, store.SaveMembership(context.Background(), map[string]any{"id": "mem-member", "organizationId": "org-alpha", "userId": "usr-member", "accountId": "acct-alpha", "role": "member", "status": "active"}))
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}), store)
	if err != nil {
		t.Fatal(err)
	}
	member := loginForTest(t, server, "member@alpha.example", "CorrectHorseBatteryStaple!")
	if rec := requestWithSession(t, server, member, http.MethodGet, "/api/workspaces", ""); rec.Code != http.StatusOK {
		t.Fatalf("active member workspace status = %d: %s", rec.Code, rec.Body.String())
	}

	revoked := requestWithSession(t, server, reservedOperatorSessionForTest(t, server), http.MethodPost, "/api/organizations/members/mem-member/revoke", `{}`)
	if revoked.Code != http.StatusOK {
		t.Fatalf("revoke status = %d: %s", revoked.Code, revoked.Body.String())
	}
	for _, membership := range mustListMemberships(t, store) {
		if stringValue(membership["id"]) == "mem-member" && stringValue(membership["status"]) != "revoked" {
			t.Fatalf("membership status = %q, want revoked", membership["status"])
		}
	}
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/state", ""},
		{http.MethodGet, "/api/billing/summary", ""},
		{http.MethodGet, "/api/workspaces", ""},
		{http.MethodGet, "/api/compute-allocations", ""},
		{http.MethodPost, "/api/storage-volumes", `{"sizeGb":10}`},
	} {
		rec := requestWithSession(t, server, member, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("revoked member %s %s status = %d, want 401: %s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
	audits, err := store.ListAuditEvents(context.Background(), "acct-alpha")
	if err != nil || len(audits) == 0 || stringValue(audits[len(audits)-1]["action"]) != "organization.member_revoke" {
		t.Fatalf("revoke audit = %#v err=%v", audits, err)
	}
}

func TestRevokeMembershipRequiresGlobalAdminAndExistingMembership(t *testing.T) {
	store := newMemoryTableStore()
	mustStore(t, store.SaveOrganization(context.Background(), map[string]any{"id": "org-alpha", "billingAccountId": "acct-alpha", "status": "active"}))
	mustStore(t, store.SaveMembership(context.Background(), map[string]any{"id": "mem-admin", "organizationId": "org-alpha", "userId": "usr-admin", "accountId": "acct-alpha", "role": "admin", "status": "active"}))
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}), store)
	if err != nil {
		t.Fatal(err)
	}
	missing := requestWithSession(t, server, reservedOperatorSessionForTest(t, server), http.MethodPost, "/api/organizations/members/missing/revoke", `{}`)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing membership status = %d, want 404: %s", missing.Code, missing.Body.String())
	}

	hash, _ := hashPassword("CorrectHorseBatteryStaple!")
	mustStore(t, store.SaveUser(context.Background(), map[string]any{"id": "usr-member", "email": "member@example.com", "accountId": "acct-alpha", "role": "member", "status": "active", "passwordHash": hash}))
	mustStore(t, store.SaveMembership(context.Background(), map[string]any{"id": "mem-member", "organizationId": "org-alpha", "userId": "usr-member", "accountId": "acct-alpha", "role": "member", "status": "active"}))
	member := loginForTest(t, server, "member@example.com", "CorrectHorseBatteryStaple!")
	forbidden := requestWithSession(t, server, member, http.MethodPost, "/api/organizations/members/mem-admin/revoke", `{}`)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("member revoke status = %d, want 403: %s", forbidden.Code, forbidden.Body.String())
	}
}

func TestTenantAdminRequiresMembershipAndCannotUseOperatorRoutes(t *testing.T) {
	store := newMemoryTableStore()
	hash, err := hashPassword("CorrectHorseBatteryStaple!")
	if err != nil {
		t.Fatal(err)
	}
	mustStore(t, store.SaveUser(context.Background(), map[string]any{"id": "usr-tenant-admin", "email": "tenant-admin@example.com", "accountId": "acct-alpha", "role": "admin", "status": "active", "passwordHash": hash}))
	mustStore(t, store.SaveMembership(context.Background(), map[string]any{"id": "mem-tenant-admin", "organizationId": "org-alpha", "userId": "usr-tenant-admin", "accountId": "acct-alpha", "role": "admin", "status": "active"}))
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}), store)
	if err != nil {
		t.Fatal(err)
	}
	tenantAdmin := loginForTest(t, server, "tenant-admin@example.com", "CorrectHorseBatteryStaple!")
	if rec := requestWithSession(t, server, tenantAdmin, http.MethodGet, "/api/workspaces", ""); rec.Code != http.StatusOK {
		t.Fatalf("active tenant admin customer status = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := requestWithSession(t, server, tenantAdmin, http.MethodGet, "/api/management/state", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("tenant admin management status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	membership := findRecord(mustListMemberships(t, store), "mem-tenant-admin")
	membership["status"] = "revoked"
	mustStore(t, store.SaveMembership(context.Background(), membership))
	if rec := requestWithSession(t, server, tenantAdmin, http.MethodGet, "/api/workspaces", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked tenant admin status = %d, want 401: %s", rec.Code, rec.Body.String())
	}
}

func TestCloudAdminSessionNeverAdoptsTenantAdmin(t *testing.T) {
	store := newMemoryTableStore()
	mustStore(t, store.SaveUser(context.Background(), map[string]any{"id": "usr-tenant-admin", "email": "tenant-admin@example.com", "accountId": "acct-alpha", "role": "admin", "status": "active"}))
	server, err := NewPersistentServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}), store)
	if err != nil {
		t.Fatal(err)
	}
	operator := reservedOperatorSessionForTest(t, server)
	var payload map[string]any
	if err := json.NewDecoder(operator.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	user := payload["user"].(map[string]any)
	if user["email"] != "admin@medopl.cn" {
		t.Fatalf("cloud admin session adopted another admin: %#v", user)
	}
	if rec := requestWithSession(t, server, operator, http.MethodGet, "/api/management/state", ""); rec.Code != http.StatusOK {
		t.Fatalf("explicit operator management status = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCloudAdminSessionReportsAuthority(t *testing.T) {
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	session := reservedOperatorSessionForTest(t, server)
	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	addSessionCookies(req, session)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("operator session status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload["isOperator"] != true {
		t.Fatalf("operator isOperator = %#v, want true", payload["isOperator"])
	}
}

func TestOnlyCloudAdminEmailHasOperatorAuthority(t *testing.T) {
	if !isOperatorUser(map[string]any{"id": "usr-admin", "email": "admin@medopl.cn", "accountId": "acct-admin", "role": "admin", "status": "active"}) {
		t.Fatal("admin@medopl.cn was not treated as OPL Cloud administrator")
	}
	for _, user := range []map[string]any{
		{"id": "usr-tenant-admin", "email": "tenant-admin@example.com", "accountId": "acct-alpha", "role": "admin", "status": "active"},
		{"id": "usr-operator", "email": "operator@opl.local", "accountId": "acct-operator", "role": "admin", "status": "active"},
		{"id": "usr-admin", "email": "admin@medopl.cn", "accountId": "acct-admin", "role": "owner", "status": "active"},
	} {
		if isOperatorUser(user) {
			t.Fatalf("non-cloud-admin received operator authority: %#v", user)
		}
	}
}

func mustListMemberships(t *testing.T, store controlPlaneTableStore) []map[string]any {
	t.Helper()
	rows, err := store.ListMemberships(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestStoresRejectOrphanOrganizationAndMembershipWrites(t *testing.T) {
	stores := []struct {
		name  string
		store controlPlaneTableStore
	}{
		{"memory", newMemoryTableStore()},
		{"ent", NewTestEntStateStore(t, t.TempDir()+"/tenant-truth.sqlite")},
	}
	for _, tc := range stores {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if err := tc.store.SaveOrganization(ctx, map[string]any{"id": "org-orphan", "billingAccountId": "acct-missing", "status": "active"}); err == nil {
				t.Fatal("orphan organization write succeeded")
			}
			mustStore(t, tc.store.SaveAccount(ctx, map[string]any{"id": "acct-alpha", "status": "active"}))
			mustStore(t, tc.store.SaveOrganization(ctx, map[string]any{"id": "org-alpha", "billingAccountId": "acct-alpha", "status": "active"}))
			mustStore(t, tc.store.SaveUser(ctx, map[string]any{"id": "usr-alpha", "email": "alpha@example.com", "accountId": "acct-alpha", "role": "member", "status": "active"}))
			if err := tc.store.SaveMembership(ctx, map[string]any{"id": "mem-orphan", "organizationId": "org-missing", "userId": "usr-alpha", "accountId": "acct-alpha", "role": "member", "status": "active"}); err == nil {
				t.Fatal("membership with missing organization succeeded")
			}
			if err := tc.store.SaveMembership(ctx, map[string]any{"id": "mem-mismatch", "organizationId": "org-alpha", "userId": "usr-alpha", "accountId": "acct-other", "role": "member", "status": "active"}); err == nil {
				t.Fatal("membership with mismatched account succeeded")
			}
		})
	}
}

func TestPostgresLegacyMembershipMigrationIsLosslessAndFailClosed(t *testing.T) {
	admin := openControlPlaneTestPostgres(t)
	defer admin.Close()
	schema := fmt.Sprintf("control_plane_membership_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`) })

	db, err := sql.Open("postgres", controlPlaneTestPostgresURL("postgres", schema))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE control_plane_accounts (id text PRIMARY KEY);
		CREATE TABLE control_plane_organizations (id text PRIMARY KEY, billing_account_id text NOT NULL);
		CREATE TABLE control_plane_users (id text PRIMARY KEY, account_id text NOT NULL);
		CREATE TABLE control_plane_memberships (id text PRIMARY KEY, account_id text NOT NULL, organization_id text NOT NULL, user_id text NOT NULL, role text NOT NULL, status text NOT NULL);
		INSERT INTO control_plane_accounts VALUES ('acct-alpha');
		INSERT INTO control_plane_organizations VALUES ('org-alpha', 'acct-alpha');
		INSERT INTO control_plane_users VALUES ('usr-owner', 'acct-alpha');
		INSERT INTO control_plane_memberships VALUES
			('mem-owner', 'acct-alpha', 'org-alpha', 'usr-owner', ' Owner ', 'active'),
			('mem-admin', 'acct-alpha', 'org-alpha', 'usr-missing', 'ADMIN', 'active');
	`); err != nil {
		t.Fatal(err)
	}
	driver := entsql.OpenDB(dialect.Postgres, db)
	if err := validateAndNormalizeLegacyMemberships(context.Background(), driver); err == nil {
		t.Fatal("migration accepted membership with missing user truth")
	}
	assertMembershipRoles(t, db, []string{"ADMIN", " Owner "})

	if _, err := db.Exec(`INSERT INTO control_plane_users VALUES ('usr-missing', 'acct-alpha')`); err != nil {
		t.Fatal(err)
	}
	if err := validateAndNormalizeLegacyMemberships(context.Background(), driver); err != nil {
		t.Fatalf("normalize valid legacy memberships: %v", err)
	}
	assertMembershipRoles(t, db, []string{"admin", "owner"})
	if _, err := db.Exec(`ALTER TABLE control_plane_memberships ALTER COLUMN role DROP NOT NULL; INSERT INTO control_plane_memberships VALUES ('mem-null', 'acct-alpha', 'org-alpha', 'usr-owner', NULL, 'active')`); err != nil {
		t.Fatal(err)
	}
	if err := validateAndNormalizeLegacyMemberships(context.Background(), driver); err == nil {
		t.Fatal("migration accepted NULL role")
	}
	var nullRole sql.NullString
	if err := db.QueryRow(`SELECT role FROM control_plane_memberships WHERE id = 'mem-null'`).Scan(&nullRole); err != nil || nullRole.Valid {
		t.Fatalf("NULL legacy role was not preserved: role=%v err=%v", nullRole, err)
	}
}

func TestPostgresStoreStartsFromFreshDatabase(t *testing.T) {
	admin := openControlPlaneTestPostgres(t)
	database := fmt.Sprintf("control_plane_fresh_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE DATABASE ` + database); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1`, database)
		_, _ = admin.Exec(`DROP DATABASE ` + database)
		_ = admin.Close()
	})
	databaseURL := controlPlaneTestPostgresURL(database, "")
	legacy, err := sql.Open("postgres", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`CREATE TABLE control_plane_wallet_projections (id text PRIMARY KEY)`); err != nil {
		_ = legacy.Close()
		t.Fatal(err)
	}
	_ = legacy.Close()

	store, err := NewPostgresEntStateStore(databaseURL)
	if err != nil {
		t.Fatalf("start store on fresh database: %v", err)
	}
	accounts, err := store.ListAccounts(context.Background(), "")
	if err != nil || len(accounts) != 0 {
		t.Fatalf("fresh account table = %v, err=%v", accounts, err)
	}
	if err := store.(*postgresEntStateStore).client.Close(); err != nil {
		t.Fatal(err)
	}
	check, err := sql.Open("postgres", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var retiredTable sql.NullString
	if err := check.QueryRow(`SELECT to_regclass('public.control_plane_wallet_projections')`).Scan(&retiredTable); err != nil || retiredTable.Valid {
		t.Fatalf("retired wallet projection survived startup: table=%v err=%v", retiredTable, err)
	}
	var migrationCount int
	if err := check.QueryRow(`SELECT count(*) FROM opl_schema_migrations WHERE service = 'control-plane'`).Scan(&migrationCount); err != nil {
		t.Fatalf("read control-plane migration journal: %v", err)
	}
	if migrationCount != 7 {
		t.Fatalf("control-plane migration count = %d, want 7", migrationCount)
	}
	if _, err := check.Exec(`CREATE TABLE control_plane_startup_probe (id text PRIMARY KEY, probe text); INSERT INTO control_plane_startup_probe VALUES ('probe', NULL)`); err != nil {
		t.Fatal(err)
	}

	second, err := NewPostgresEntStateStore(databaseURL)
	if err != nil {
		t.Fatalf("start store a second time: %v", err)
	}
	if err := second.(*postgresEntStateStore).client.Close(); err != nil {
		t.Fatal(err)
	}
	var probe sql.NullString
	if err := check.QueryRow(`SELECT probe FROM control_plane_startup_probe WHERE id = 'probe'`).Scan(&probe); err != nil {
		t.Fatal(err)
	}
	if probe.Valid {
		t.Fatalf("second startup repeated backfill: probe=%q", probe.String)
	}
	var addedColumns int
	if err := check.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'control_plane_startup_probe' AND column_name IN ('created_at', 'updated_at')`).Scan(&addedColumns); err != nil {
		t.Fatal(err)
	}
	if addedColumns != 0 {
		t.Fatalf("second startup repeated DDL: added columns=%d", addedColumns)
	}
}

func TestPostgresRuntimeOperationConcurrentUpsert(t *testing.T) {
	admin := openControlPlaneTestPostgres(t)
	schema := fmt.Sprintf("control_plane_runtime_operation_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		_ = admin.Close()
	})
	stateStore, err := NewPostgresEntStateStore(controlPlaneTestPostgresURL("postgres", schema))
	if err != nil {
		t.Fatal(err)
	}
	store := stateStore.(*postgresEntStateStore)
	t.Cleanup(func() { _ = store.client.Close() })
	operation := map[string]any{
		"id": "operation-capacity", "operationId": "operation-capacity", "accountId": "acct-capacity",
		"resourceId": "compute-capacity", "resourceKind": "compute_allocation", "action": "create_compute_allocation", "status": "succeeded",
	}
	if err := store.SaveRuntimeOperation(context.Background(), operation); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errors := make(chan error, 20)
	var wait sync.WaitGroup
	wait.Add(20)
	for range 20 {
		go func() {
			defer wait.Done()
			<-start
			errors <- store.SaveRuntimeOperation(context.Background(), operation)
		}()
	}
	close(start)
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent runtime operation upsert: %v", err)
		}
	}
	rows, err := store.ListRuntimeOperations(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("runtime operations=%#v err=%v", rows, err)
	}
}

func openControlPlaneTestPostgres(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", controlPlaneTestPostgresURL("postgres", ""))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		if os.Getenv("OPL_POSTGRES_TESTS") != "1" && os.Getenv("OPL_CAPACITY_TESTS") != "1" {
			t.Skipf("local PostgreSQL unavailable: %v", err)
		}
		t.Fatal(err)
	}
	return db
}

func controlPlaneTestPostgresURL(database, searchPath string) string {
	var databaseURL string
	if os.Getenv("OPL_POSTGRES_TESTS") == "1" {
		databaseURL = "connect_timeout=10 dbname=" + database
	} else {
		databaseURL = "host=/var/run/postgresql dbname=" + database + " sslmode=disable"
	}
	if searchPath != "" {
		databaseURL += " search_path=" + searchPath
	}
	return databaseURL
}

func assertMembershipRoles(t *testing.T, db *sql.DB, want []string) {
	t.Helper()
	rows, err := db.Query(`SELECT role FROM control_plane_memberships ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			t.Fatal(err)
		}
		got = append(got, role)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("membership roles = %v, want %v", got, want)
	}
}

func requestForStoredUser(t *testing.T, app *controlPlaneServer, user map[string]any) *http.Request {
	t.Helper()
	mustStore(t, app.tables.SaveUser(context.Background(), user))
	_, sessionID, err := app.createSession(user)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/test", bytes.NewBufferString(`{}`))
	req.AddCookie(sessionCookie(sessionID, 3600))
	return req
}
