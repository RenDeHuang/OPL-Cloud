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
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

func newGatewayOwnerTestServer(t *testing.T, sub2API clients.Sub2APIClient, store StateStore) (http.Handler, *httptest.ResponseRecorder) {
	t.Helper()
	if store == nil {
		store = newMemoryTableStore()
	}
	seedTenantMember(t, store, "acct-gateway", "org-gateway", "usr-gateway-owner", "gateway-owner@example.com")
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, sub2API), store)
	if err != nil {
		t.Fatal(err)
	}
	return server, loginForTest(t, server, "gateway-owner@example.com", "CorrectHorseBatteryStaple!")
}

func TestGatewayOwnerRevealIsAudited(t *testing.T) {
	server, client, store, session := newGatewayKeyCommandFixture(t, "")
	var logs bytes.Buffer
	previousLogOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousLogOutput) })

	revealed := requestWithSession(t, server, session, http.MethodPost, "/api/gateway/keys/17/reveal", "{}")
	if revealed.Code != http.StatusOK || revealed.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("revealed Gateway response = %d Cache-Control %q: %s", revealed.Code, revealed.Header().Get("Cache-Control"), revealed.Body.String())
	}
	var revealBody map[string]any
	if err := json.NewDecoder(revealed.Body).Decode(&revealBody); err != nil {
		t.Fatal(err)
	}
	revealedKey := mapField(revealBody, "data")
	if stringValue(revealedKey["id"]) != "17" || stringValue(revealedKey["name"]) != "general-key" || stringValue(revealedKey["status"]) != "active" || stringValue(revealedKey["value"]) != "general-key-secret" {
		t.Fatalf("revealed Gateway key = %#v", revealedKey)
	}

	state := requestWithSession(t, server, session, http.MethodGet, "/api/state", "")
	if state.Code != http.StatusOK {
		t.Fatalf("state status = %d: %s", state.Code, state.Body.String())
	}
	if strings.Contains(state.Body.String(), "workspace-key-secret") || strings.Contains(state.Body.String(), "maskedValue") || strings.Contains(state.Body.String(), "usage5hUsdMicros") {
		t.Fatalf("state leaked Gateway Key projection: %s", state.Body.String())
	}
	auditEvents, err := store.ListAuditEvents(context.Background(), "acct-gateway")
	if err != nil || !slices.ContainsFunc(auditEvents, func(event map[string]any) bool {
		return stringValue(event["action"]) == "gateway.key_reveal" && stringValue(event["actorUserId"]) == "usr-gateway-owner" && stringValue(event["targetAccountId"]) == "acct-gateway" && stringValue(event["result"]) == "succeeded"
	}) {
		t.Fatalf("Gateway reveal audit events = %#v err=%v", auditEvents, err)
	}
	auditJSON, _ := json.Marshal(auditEvents)
	if strings.Contains(string(auditJSON), "general-key-secret") || strings.Contains(string(auditJSON), "gene...cret") || strings.Contains(logs.String(), "general-key-secret") {
		t.Fatalf("Gateway reveal leaked Key outside response: audit=%s logs=%s", auditJSON, logs.String())
	}
	if len(client.userKeyReadIDs) != 1 || client.userKeyReadIDs[0] != 17 {
		t.Fatalf("Gateway used caller-supplied Key identity: %#v", client.userKeyReadIDs)
	}
}

func TestGatewayRevealRejectsUnauthorizedWithoutFetchingKey(t *testing.T) {
	t.Run("operator", func(t *testing.T) {
		server, client, _, _ := newGatewayKeyCommandFixture(t, "")
		rec := requestWithSession(t, server, reservedOperatorSessionForTest(t, server), http.MethodPost, "/api/gateway/keys/17/reveal", "{}")
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "gateway_key_reveal_forbidden") || len(client.userKeyReadIDs) != 0 {
			t.Fatalf("operator reveal = %d calls=%#v: %s", rec.Code, client.userKeyReadIDs, rec.Body.String())
		}
	})

	t.Run("owner mismatch", func(t *testing.T) {
		server, client, store, session := newGatewayKeyCommandFixture(t, "")
		store.mu.Lock()
		store.accounts["acct-gateway"]["ownerUserId"] = "usr-other"
		store.mu.Unlock()
		rec := requestWithSession(t, server, session, http.MethodPost, "/api/gateway/keys/17/reveal", "{}")
		if rec.Code != http.StatusUnauthorized || len(client.userKeyReadIDs) != 0 {
			t.Fatalf("owner mismatch reveal = %d calls=%#v: %s", rec.Code, client.userKeyReadIDs, rec.Body.String())
		}
	})

	for _, tc := range []struct {
		name string
		path string
		csrf bool
		code string
	}{
		{name: "missing csrf", path: "/api/gateway/keys/17/reveal", code: "csrf_token_invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, client, _, session := newGatewayKeyCommandFixture(t, "")
			req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewBufferString("{}"))
			req.Header.Set("Content-Type", "application/json")
			addSessionCookies(req, session)
			if tc.csrf {
				req.Header.Set("x-opl-csrf", session.Header().Get("x-opl-csrf-token"))
			}
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), tc.code) || len(client.userKeyReadIDs) != 0 {
				t.Fatalf("%s reveal = %d calls=%#v: %s", tc.name, rec.Code, client.userKeyReadIDs, rec.Body.String())
			}
		})
	}
}

func TestGatewayRevealAuditFailureDoesNotReturnKey(t *testing.T) {
	store := &failingResumeCommitStore{memoryTableStore: newMemoryTableStore()}
	client := &sourceTruthGatewayClient{
		customerFactsSub2API: &customerFactsSub2API{testSub2APIClient: &testSub2APIClient{balance: 123, charges: map[string]int64{}}},
		keys:                 []clients.Sub2APIWorkspaceKey{{ID: 17, UserID: 41, Name: "general-key", Key: "general-key-secret", Status: "active"}},
	}
	server, session := newGatewayOwnerTestServer(t, client, store)
	var logs bytes.Buffer
	previousLogOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousLogOutput) })
	rec := requestWithSession(t, server, session, http.MethodPost, "/api/gateway/keys/17/reveal", "{}")
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "state_persist_failed") {
		t.Fatalf("audit failure reveal = %d: %s", rec.Code, rec.Body.String())
	}
	state := requestWithSession(t, server, session, http.MethodGet, "/api/state", "")
	auditEvents, auditErr := store.ListAuditEvents(context.Background(), "acct-gateway")
	joined := rec.Body.String() + state.Body.String() + logs.String()
	if strings.Contains(joined, "general-key-secret") || strings.Contains(joined, "gene...cret") || auditErr != nil || len(auditEvents) != 0 || len(client.keyUserIDs) != 1 {
		t.Fatalf("audit failure leaked or persisted Key: calls=%#v audit=%#v err=%v output=%s", client.keyUserIDs, auditEvents, auditErr, joined)
	}
}

func TestGatewayRevealFailsClosed(t *testing.T) {
	client := &sourceTruthGatewayClient{
		customerFactsSub2API: &customerFactsSub2API{testSub2APIClient: &testSub2APIClient{balance: 123, charges: map[string]int64{}}},
		keys:                 []clients.Sub2APIWorkspaceKey{{ID: 17, UserID: 41, Name: "general-key", Key: "general-key-secret", Status: "active"}},
		userKeyErr:           errors.New("Sub2API unavailable"),
	}
	server, session := newGatewayOwnerTestServer(t, client, nil)
	rec := requestWithSession(t, server, session, http.MethodPost, "/api/gateway/keys/17/reveal", "{}")
	assertUnavailableSourceEnvelope(t, rec, http.StatusBadGateway)
	if strings.Contains(rec.Body.String(), "general-key-secret") || len(client.keyUserIDs) != 1 {
		t.Fatalf("Gateway reveal error = %d calls=%#v: %s", rec.Code, client.keyUserIDs, rec.Body.String())
	}
}

func TestCustomerOwnerCannotUseEndpointsAcrossAccountsOrOrganizations(t *testing.T) {
	app := newControlPlaneApp()
	seedTenantMember(t, app.tables, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	users, err := app.tables.ListUsers(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	owner := findRecord(users, "usr-alpha")
	_, sessionID, err := app.createSession(owner, "test-owner-delegated-token")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/test", bytes.NewBufferString(`{}`))
	req.AddCookie(sessionCookie(sessionID, 3600))

	rec := httptest.NewRecorder()
	if _, ok := app.scopedAccountID(rec, req, map[string]any{"accountId": "acct-other"}); ok || rec.Code != http.StatusForbidden {
		t.Fatalf("cross-account owner scope status=%d ok=%v, want 403 false", rec.Code, ok)
	}
	if app.canAccessResource(req, map[string]any{"id": "ws-other", "accountId": "acct-other"}) {
		t.Fatal("owner accessed another account resource through a customer endpoint")
	}

	rec = httptest.NewRecorder()
	if app.authorizeOrganization(rec, req, "org-other") || rec.Code != http.StatusForbidden {
		t.Fatalf("cross-organization owner status=%d, want 403", rec.Code)
	}
}

func TestNonOwnerMembershipCannotAuthorizeOrganization(t *testing.T) {
	for _, role := range []string{"member", "admin"} {
		t.Run(role, func(t *testing.T) {
			store := newMemoryTableStore()
			app, err := newControlPlaneAppWithStore(store)
			if err != nil {
				t.Fatal(err)
			}
			users, err := store.ListUsers(context.Background(), false)
			if err != nil {
				t.Fatal(err)
			}
			_, sessionID, err := app.createSession(findRecord(users, "usr-admin"), "test-operator-delegated-token")
			if err != nil {
				t.Fatal(err)
			}
			store.mu.Lock()
			store.memberships["mem-admin"]["role"] = role
			store.mu.Unlock()
			req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
			req.AddCookie(sessionCookie(sessionID, 3600))
			rec := httptest.NewRecorder()
			if app.authorizeOrganization(rec, req, "org-admin") || rec.Code != http.StatusForbidden {
				t.Fatalf("%s membership authorization status=%d, want 403", role, rec.Code)
			}
		})
	}
}

func TestCustomerStateContainsOnlySessionTenant(t *testing.T) {
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	seedTenantMember(t, store, "acct-beta", "org-beta", "usr-beta", "beta-secret@example.com")
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
	if strings.Contains(rec.Body.String(), "alpha@example.com") {
		t.Fatalf("state leaked current user: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "billingReconciliation") || strings.Contains(rec.Body.String(), "global-secret") {
		t.Fatalf("state leaked global reconciliation: %s", rec.Body.String())
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
	rec := requestWithSession(t, server, member, http.MethodPost, "/api/workspace-launches", `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"error":"billing_reconciliation_blocked"`) {
		t.Fatalf("blocked launch status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, secret := range []string{"billingReconciliation", "private-operator-reason", "operator-secret", "cross-tenant-private-report", "messageText", "messageAuthor"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("blocked launch leaked %q: %s", secret, rec.Body.String())
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
	account := map[string]any{"id": accountID, "ownerUserId": userID, "sub2apiUserId": testSub2APIUserID(email), "status": "active"}
	user := map[string]any{"id": userID, "email": email, "accountId": accountID, "role": "owner", "status": "active"}
	organization := map[string]any{"id": organizationID, "name": "Organization " + accountID, "billingAccountId": accountID, "status": "active"}
	membership := map[string]any{"id": "mem-" + userID, "organizationId": organizationID, "userId": userID, "accountId": accountID, "role": "owner", "status": "active"}
	mustStore(t, store.CreateInvitedAccount(context.Background(), account, user, organization, membership))
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
	data := mapField(payload, "data")
	if payload["source"] != "sub2api" || data["consoleUserId"] != "usr-admin" || data["accountId"] != "acct-admin" || data["role"] != "admin" || data["sub2apiUserId"] != "1" {
		t.Fatalf("operator auth source = %#v", payload)
	}
}

func TestOnlyCloudAdminEmailHasOperatorAuthority(t *testing.T) {
	if !isOperatorUser(map[string]any{"id": "usr-admin", "email": "admin@medopl.cn", "accountId": "acct-admin", "role": "admin", "status": "active"}) {
		t.Fatal("admin@medopl.cn was not treated as OPL Cloud administrator")
	}
	for _, user := range []map[string]any{
		{"id": "usr-tenant-admin", "email": "tenant-admin@example.com", "accountId": "acct-alpha", "role": "admin", "status": "active"},
		{"id": "usr-operator", "email": "operator@opl.local", "accountId": "acct-operator", "role": "admin", "status": "active"},
		{"id": "usr-other", "email": "admin@medopl.cn", "accountId": "acct-admin", "role": "admin", "status": "active"},
		{"id": "usr-admin", "email": "admin@medopl.cn", "accountId": "acct-other", "role": "admin", "status": "active"},
		{"id": "usr-admin", "email": "admin@medopl.cn", "accountId": "acct-admin", "role": "owner", "status": "active"},
	} {
		if isOperatorUser(user) {
			t.Fatalf("non-cloud-admin received operator authority: %#v", user)
		}
	}
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
			seedTenantMember(t, tc.store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
			if err := tc.store.SaveMembership(ctx, map[string]any{"id": "mem-orphan", "organizationId": "org-missing", "userId": "usr-alpha", "accountId": "acct-alpha", "role": "owner", "status": "active"}); err == nil {
				t.Fatal("membership with missing organization succeeded")
			}
			if err := tc.store.SaveMembership(ctx, map[string]any{"id": "mem-mismatch", "organizationId": "org-alpha", "userId": "usr-alpha", "accountId": "acct-other", "role": "owner", "status": "active"}); err == nil {
				t.Fatal("membership with mismatched account succeeded")
			}
		})
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
	databaseURL := controlPlaneTestPostgresURL(t, database, "")
	legacy, err := sql.Open("postgres", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`CREATE TABLE control_plane_wallet_projections (id text PRIMARY KEY)`); err != nil {
		_ = legacy.Close()
		t.Fatal(err)
	}
	_ = legacy.Close()

	store, err := newTestPostgresEntStateStore(databaseURL)
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
	if migrationCount != 13 {
		t.Fatalf("control-plane migration count = %d, want 13", migrationCount)
	}
	var autoRenewAuditMigration bool
	if err := check.QueryRow(`SELECT EXISTS (SELECT 1 FROM opl_schema_migrations WHERE service = 'control-plane' AND version = '202607170003_workspace_auto_renew_audit')`).Scan(&autoRenewAuditMigration); err != nil || !autoRenewAuditMigration {
		t.Fatalf("workspace auto-renew audit migration missing: applied=%v err=%v", autoRenewAuditMigration, err)
	}
	var identityHardCutMigration bool
	if err := check.QueryRow(`SELECT EXISTS (SELECT 1 FROM opl_schema_migrations WHERE service = 'control-plane' AND version = '202607180001_customer_identity_hard_cut')`).Scan(&identityHardCutMigration); err != nil || !identityHardCutMigration {
		t.Fatalf("customer identity hard cut migration missing: applied=%v err=%v", identityHardCutMigration, err)
	}
	var announcementMigration bool
	if err := check.QueryRow(`SELECT EXISTS (SELECT 1 FROM opl_schema_migrations WHERE service = 'control-plane' AND version = '202607190002_pilot_announcements')`).Scan(&announcementMigration); err != nil || !announcementMigration {
		t.Fatalf("Pilot announcement migration missing: applied=%v err=%v", announcementMigration, err)
	}
	var announcementConstraints int
	if err := check.QueryRow(`SELECT count(*) FROM pg_constraint WHERE conrelid IN ('control_plane_announcements'::regclass, 'control_plane_announcement_reads'::regclass) AND conname IN ('control_plane_announcements_status_check', 'control_plane_announcements_schedule_check', 'control_plane_announcement_reads_announcement_fk', 'control_plane_announcement_reads_user_unique')`).Scan(&announcementConstraints); err != nil || announcementConstraints != 4 {
		t.Fatalf("Pilot announcement constraints = %d, want 4: %v", announcementConstraints, err)
	}
	if _, err := check.Exec(`CREATE TABLE control_plane_startup_probe (id text PRIMARY KEY, probe text); INSERT INTO control_plane_startup_probe VALUES ('probe', NULL)`); err != nil {
		t.Fatal(err)
	}

	second, err := newTestPostgresEntStateStore(databaseURL)
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
	stateStore, err := newTestPostgresEntStateStore(controlPlaneTestPostgresURL(t, "postgres", schema))
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
	db, err := sql.Open("postgres", controlPlaneTestPostgresURL(t, "postgres", ""))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	return db
}

func TestControlPlanePostgresTestURLUsesExplicitEnvironment(t *testing.T) {
	t.Run("keyword DSN", func(t *testing.T) {
		t.Setenv("CONTROL_PLANE_TEST_DATABASE_URL", "host=/explicit-test-socket user=explicit dbname=template1 sslmode=disable")
		got := controlPlaneTestPostgresURL(t, "postgres", "schema_explicit")
		if !strings.Contains(got, "host=/explicit-test-socket") || !strings.Contains(got, "user=explicit") || !strings.Contains(got, "dbname=postgres") || !strings.Contains(got, "search_path=schema_explicit") {
			t.Fatalf("explicit PostgreSQL test DSN ignored: %q", got)
		}
	})
	t.Run("URL", func(t *testing.T) {
		t.Setenv("CONTROL_PLANE_TEST_DATABASE_URL", "postgresql://explicit:secret@db.example/template1?sslmode=disable")
		got := controlPlaneTestPostgresURL(t, "postgres", "schema_explicit")
		want := "postgresql://explicit:secret@db.example/postgres?search_path=schema_explicit&sslmode=disable"
		if got != want {
			t.Fatalf("explicit PostgreSQL test URL = %q, want %q", got, want)
		}
	})
}

func TestControlPlanePostgresTestBaseURLRequiresSafetyGate(t *testing.T) {
	for _, key := range []string{"CONTROL_PLANE_TEST_DATABASE_URL", "OPL_POSTGRES_TESTS", "PGHOST", "PGPORT", "PGUSER", "PGDATABASE", "PGSSLMODE"} {
		t.Setenv(key, "")
	}
	for key, value := range map[string]string{
		"PGHOST": "/tmp/isolated-postgres", "PGPORT": "55432", "PGUSER": "postgres", "PGDATABASE": "postgres", "PGSSLMODE": "disable",
	} {
		t.Setenv(key, value)
	}
	if got := controlPlaneTestPostgresBaseURL(); got != "" {
		t.Fatalf("PG environment bypassed OPL_POSTGRES_TESTS gate: %q", got)
	}
	t.Setenv("OPL_POSTGRES_TESTS", "1")
	if got := controlPlaneTestPostgresBaseURL(); got != "connect_timeout=10" {
		t.Fatalf("isolated PG environment URL = %q", got)
	}
	t.Setenv("PGPORT", "")
	if got := controlPlaneTestPostgresBaseURL(); got != "" {
		t.Fatalf("incomplete PG environment accepted: %q", got)
	}
}

func controlPlaneTestPostgresURL(t *testing.T, database, searchPath string) string {
	t.Helper()
	databaseURL := controlPlaneTestPostgresBaseURL()
	if databaseURL == "" {
		t.Fatal("CONTROL_PLANE_TEST_DATABASE_URL or OPL_POSTGRES_TESTS=1 with PGHOST, PGPORT, PGUSER, PGDATABASE, and PGSSLMODE is required for PostgreSQL tests")
	}
	if parsed, err := url.Parse(databaseURL); err == nil && parsed.Scheme != "" {
		parsed.Path = "/" + database
		query := parsed.Query()
		if searchPath != "" {
			query.Set("search_path", searchPath)
		}
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	databaseURL += " dbname=" + database
	if searchPath != "" {
		databaseURL += " search_path=" + searchPath
	}
	return databaseURL
}

func controlPlaneTestPostgresBaseURL() string {
	if databaseURL := strings.TrimSpace(os.Getenv("CONTROL_PLANE_TEST_DATABASE_URL")); databaseURL != "" {
		return databaseURL
	}
	if os.Getenv("OPL_POSTGRES_TESTS") != "1" {
		return ""
	}
	for _, key := range []string{"PGHOST", "PGPORT", "PGUSER", "PGDATABASE", "PGSSLMODE"} {
		if strings.TrimSpace(os.Getenv(key)) == "" {
			return ""
		}
	}
	return "connect_timeout=10"
}
