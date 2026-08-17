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
	server, client, store, session := newGatewayKeyCommandFixture(t)
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

	keys := requestWithSession(t, server, session, http.MethodGet, "/api/gateway/keys", "")
	if keys.Code != http.StatusOK {
		t.Fatalf("key list status = %d: %s", keys.Code, keys.Body.String())
	}
	if strings.Contains(keys.Body.String(), "workspace-key-secret") || strings.Contains(keys.Body.String(), "maskedValue") {
		t.Fatalf("key list leaked Gateway Key projection: %s", keys.Body.String())
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

func TestGatewayRevealAuditGrowthAndRequestMetadataAreBounded(t *testing.T) {
	store := NewTestEntStateStore(t, t.TempDir()+"/gateway-reveal-audit.sqlite").(*postgresEntStateStore)
	client := &sourceTruthGatewayClient{
		customerFactsSub2API: &customerFactsSub2API{testSub2APIClient: &testSub2APIClient{balance: 123, charges: map[string]int64{}}},
		keys:                 []clients.Sub2APIWorkspaceKey{{ID: 17, UserID: 41, Name: "general-key", Key: "general-key-secret", Status: "active"}},
	}
	server, session := newGatewayOwnerTestServer(t, client, store)
	for attempt := range 2 {
		if attempt > 0 {
			time.Sleep(1100 * time.Millisecond)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/gateway/keys/17/reveal", strings.NewReader("{}"))
		req.RemoteAddr = strings.Repeat("1", 4096)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", strings.Repeat("u", 4096))
		addAuth(req, session)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("reveal status = %d: %s", rec.Code, rec.Body.String())
		}
	}

	audits, err := store.ListAuditEvents(context.Background(), "acct-gateway")
	if err != nil {
		t.Fatal(err)
	}
	reveals := make([]map[string]any, 0, len(audits))
	for _, event := range audits {
		if stringValue(event["action"]) == "gateway.key_reveal" {
			reveals = append(reveals, event)
		}
	}
	if len(reveals) != 1 {
		t.Fatalf("reveal audit rows = %d, want one coalesced row: %#v", len(reveals), reveals)
	}
	if ip := stringValue(reveals[0]["ipAddress"]); len(ip) > 64 {
		t.Fatalf("audit IP length = %d, want <= 64", len(ip))
	}
	if userAgent := stringValue(reveals[0]["userAgent"]); len(userAgent) > 512 {
		t.Fatalf("audit user agent length = %d, want <= 512", len(userAgent))
	}
}

func TestGatewayRevealDoesNotLeakAcrossAccounts(t *testing.T) {
	t.Run("operator other account", func(t *testing.T) {
		server, client, _, _ := newGatewayKeyCommandFixture(t)
		rec := requestWithSession(t, server, reservedOperatorSessionForTest(t, server), http.MethodPost, "/api/gateway/keys/17/reveal", "{}")
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "gateway_key_not_found") || strings.Contains(rec.Body.String(), "general-key-secret") || len(client.userKeyReadIDs) != 1 {
			t.Fatalf("operator reveal = %d calls=%#v: %s", rec.Code, client.userKeyReadIDs, rec.Body.String())
		}
	})

	t.Run("owner mismatch", func(t *testing.T) {
		server, client, store, session := newGatewayKeyCommandFixture(t)
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
			server, client, _, session := newGatewayKeyCommandFixture(t)
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
	store := &failingAuditStore{memoryTableStore: newMemoryTableStore()}
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
	keys := requestWithSession(t, server, session, http.MethodGet, "/api/gateway/keys", "")
	auditEvents, auditErr := store.ListAuditEvents(context.Background(), "acct-gateway")
	joined := rec.Body.String() + keys.Body.String() + logs.String()
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

func TestCustomerOwnerCannotUseEndpointsAcrossAccounts(t *testing.T) {
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
}

func TestCustomerWorkspaceListContainsOnlySessionTenant(t *testing.T) {
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
	rec := requestWithSession(t, server, member, http.MethodGet, "/api/workspaces", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("workspace list status = %d: %s", rec.Code, rec.Body.String())
	}
	for _, secret := range []string{"acct-beta", "beta-secret@example.com", "workspace-beta", "compute-beta", "storage-beta", "operation-beta"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("workspace list leaked %q: %s", secret, rec.Body.String())
		}
	}
	if strings.Contains(rec.Body.String(), "alpha@example.com") {
		t.Fatalf("workspace list leaked current user: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "billingReconciliation") || strings.Contains(rec.Body.String(), "global-secret") {
		t.Fatalf("workspace list leaked global reconciliation: %s", rec.Body.String())
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

func seedTenantMember(t *testing.T, store controlPlaneTableStore, accountID, _ string, userID, email string) {
	t.Helper()
	account := map[string]any{"id": accountID, "ownerUserId": userID, "sub2apiUserId": testSub2APIUserID(email), "status": "active"}
	user := map[string]any{"id": userID, "email": email, "accountId": accountID, "role": "owner", "status": "active"}
	mustStore(t, store.CreateProvisionedAccount(context.Background(), account, user))
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

func TestCloudAdminOwnsReservedCustomerAccount(t *testing.T) {
	server := NewServer(newTestService(fakeLedgerClient{}, &fakeFabricClient{}))
	handler := server.(*controlPlaneHTTPHandler)
	users, err := handler.app.tables.ListUsers(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	active, err := handler.app.hasActiveCustomerAccount(context.Background(), findRecord(users, "usr-admin"))
	if err != nil || !active {
		t.Fatalf("reserved admin owner active=%v err=%v", active, err)
	}
}

func TestReservedCloudAdminAuthorityDoesNotDependOnInstanceEmail(t *testing.T) {
	for _, email := range []string{"admin@opl.local", "operator@example.test"} {
		if !isOperatorUser(map[string]any{"id": "usr-admin", "email": email, "accountId": "acct-admin", "role": "admin", "status": "active"}) {
			t.Fatalf("reserved OPL Cloud administrator email %q was rejected", email)
		}
	}
	for _, user := range []map[string]any{
		{"id": "usr-tenant-admin", "email": "tenant-admin@example.com", "accountId": "acct-alpha", "role": "admin", "status": "active"},
		{"id": "usr-operator", "email": "operator@opl.local", "accountId": "acct-operator", "role": "admin", "status": "active"},
		{"id": "usr-other", "email": "operator@example.test", "accountId": "acct-admin", "role": "admin", "status": "active"},
		{"id": "usr-admin", "email": "operator@example.test", "accountId": "acct-other", "role": "admin", "status": "active"},
		{"id": "usr-admin", "email": "operator@example.test", "accountId": "acct-admin", "role": "owner", "status": "active"},
		{"id": "usr-admin", "email": "not-an-email", "accountId": "acct-admin", "role": "admin", "status": "active"},
	} {
		if isOperatorUser(user) {
			t.Fatalf("non-cloud-admin received operator authority: %#v", user)
		}
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
	if migrationCount != 18 {
		t.Fatalf("control-plane migration count = %d, want 18", migrationCount)
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
	var workspacePurchaseReceiptMigration bool
	if err := check.QueryRow(`SELECT EXISTS (SELECT 1 FROM opl_schema_migrations WHERE service = 'control-plane' AND version = '202607230001_workspace_purchase_receipt_id')`).Scan(&workspacePurchaseReceiptMigration); err != nil || !workspacePurchaseReceiptMigration {
		t.Fatalf("Workspace purchase Receipt migration missing: applied=%v err=%v", workspacePurchaseReceiptMigration, err)
	}
	var multiWorkspaceMigration bool
	if err := check.QueryRow(`SELECT EXISTS (SELECT 1 FROM opl_schema_migrations WHERE service = 'control-plane' AND version = '202607240001_multi_workspace_pagination')`).Scan(&multiWorkspaceMigration); err != nil || !multiWorkspaceMigration {
		t.Fatalf("multi-Workspace pagination migration missing: applied=%v err=%v", multiWorkspaceMigration, err)
	}
	var capacityIndexesMigration bool
	if err := check.QueryRow(`SELECT EXISTS (SELECT 1 FROM opl_schema_migrations WHERE service = 'control-plane' AND version = '202607250001_control_plane_capacity_indexes')`).Scan(&capacityIndexesMigration); err != nil || !capacityIndexesMigration {
		t.Fatalf("capacity indexes migration missing: applied=%v err=%v", capacityIndexesMigration, err)
	}
	var remoteCompanionMigration bool
	if err := check.QueryRow(`SELECT EXISTS (SELECT 1 FROM opl_schema_migrations WHERE service = 'control-plane' AND version = '202608170001_remote_companion_broker')`).Scan(&remoteCompanionMigration); err != nil || !remoteCompanionMigration {
		t.Fatalf("remote companion broker migration missing: applied=%v err=%v", remoteCompanionMigration, err)
	}
	var legacyIdentityCustodyMigration bool
	if err := check.QueryRow(`SELECT EXISTS (SELECT 1 FROM opl_schema_migrations WHERE service = 'control-plane' AND version = '202608170002_legacy_identity_table_custody')`).Scan(&legacyIdentityCustodyMigration); err != nil || !legacyIdentityCustodyMigration {
		t.Fatalf("legacy identity table custody migration missing: applied=%v err=%v", legacyIdentityCustodyMigration, err)
	}
	for _, table := range []string{"control_plane_organizations", "control_plane_memberships"} {
		var exists bool
		if err := check.QueryRow(`SELECT to_regclass($1) IS NOT NULL`, "public."+table).Scan(&exists); err != nil || !exists {
			t.Fatalf("legacy custody table %s missing: exists=%v err=%v", table, exists, err)
		}
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
