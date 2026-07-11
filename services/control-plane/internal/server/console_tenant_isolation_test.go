package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/lib/pq"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

func TestBootstrapUsersUseOnlyFixedRoles(t *testing.T) {
	t.Setenv("OPL_CONSOLE_USERS_JSON", `[{"email":"owner@example.com","password":"correct horse battery staple"}]`)
	users, err := bootstrapUsersFromEnv()
	if err != nil {
		t.Fatalf("bootstrap users: %v", err)
	}
	if got := stringValue(users[0]["role"]); got != "owner" {
		t.Fatalf("default role = %q, want owner", got)
	}

	t.Setenv("OPL_CONSOLE_USERS_JSON", `[{"email":"pi@example.com","password":"correct horse battery staple","role":"pi"}]`)
	if _, err := bootstrapUsersFromEnv(); err == nil || !strings.Contains(err.Error(), "invalid_role") {
		t.Fatalf("invalid explicit role error = %v, want invalid_role", err)
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
		if _, err := app.createUser(map[string]any{"email": role + "@example.com", "accountId": "acct-alpha", "role": role, "password": "correct horse battery staple"}); err == nil || err.Error() != "invalid_role" {
			t.Fatalf("user role %q error = %v, want invalid_role", role, err)
		}
		if _, err := app.createMembership(map[string]any{"organizationId": "org-alpha", "userId": "usr-member", "accountId": "acct-alpha", "role": role}); err == nil || err.Error() != "invalid_role" {
			t.Fatalf("membership role %q error = %v, want invalid_role", role, err)
		}
	}
}

func TestAdminCannotUseCustomerEndpointsAcrossAccountsOrOrganizations(t *testing.T) {
	app := newControlPlaneApp()
	req := requestForStoredUser(t, app, map[string]any{"id": "usr-admin", "email": "admin@example.com", "accountId": "acct-admin", "role": "admin", "status": "active"})

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

type crossTenantComputeFabric struct{ fakeFabricClient }

func (crossTenantComputeFabric) GetComputeAllocation(_ context.Context, id string) (clients.ComputeAllocation, error) {
	return clients.ComputeAllocation{ID: id, AccountID: "acct-beta", Status: "running"}, nil
}

func TestFreshComputeReadRejectsCrossTenantProjection(t *testing.T) {
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &crossTenantComputeFabric{}))
	req := httptest.NewRequest(http.MethodGet, "/api/compute-allocations/compute-beta", nil)
	addSessionCookies(req, tenantAdminSessionForTest(t, server))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("fresh cross-tenant compute status = %d, want 403: %s", rec.Code, rec.Body.String())
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
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}), store)
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
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{calls: &calls}), store)
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

func TestCustomerSupportScopeAllRemainsTenantScoped(t *testing.T) {
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	mustStore(t, store.SaveSupportMapping(context.Background(), map[string]any{"id": "support-alpha", "accountId": "acct-alpha", "externalTicketId": "ALPHA-1"}))
	mustStore(t, store.SaveSupportMapping(context.Background(), map[string]any{"id": "support-beta", "accountId": "acct-beta", "externalTicketId": "BETA-1"}))
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}), store)
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
	mustStore(t, store.SaveAccount(context.Background(), map[string]any{"id": accountID, "status": "active"}))
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
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}), store)
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
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}), store)
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
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}), store)
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

func TestOperatorLoginNeverAdoptsTenantAdmin(t *testing.T) {
	store := newMemoryTableStore()
	mustStore(t, store.SaveUser(context.Background(), map[string]any{"id": "usr-tenant-admin", "email": "tenant-admin@example.com", "accountId": "acct-alpha", "role": "admin", "status": "active"}))
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}), store)
	if err != nil {
		t.Fatal(err)
	}
	operator := reservedOperatorSessionForTest(t, server)
	var payload map[string]any
	if err := json.NewDecoder(operator.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	user := payload["user"].(map[string]any)
	if user["id"] != "usr-operator" || user["accountId"] != "acct-operator" {
		t.Fatalf("operator login adopted non-reserved admin: %#v", user)
	}
	if rec := requestWithSession(t, server, operator, http.MethodGet, "/api/management/state", ""); rec.Code != http.StatusOK {
		t.Fatalf("explicit operator management status = %d: %s", rec.Code, rec.Body.String())
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
	admin, err := sql.Open("postgres", "host=/var/run/postgresql dbname=postgres sslmode=disable")
	if err != nil {
		t.Skipf("open local postgres: %v", err)
	}
	defer admin.Close()
	if err := admin.Ping(); err != nil {
		t.Skipf("local postgres unavailable: %v", err)
	}
	schema := fmt.Sprintf("control_plane_membership_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`) })

	db, err := sql.Open("postgres", "host=/var/run/postgresql dbname=postgres sslmode=disable search_path="+schema)
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
	admin, err := sql.Open("postgres", "host=/var/run/postgresql dbname=postgres sslmode=disable")
	if err != nil {
		t.Skipf("open local postgres: %v", err)
	}
	defer admin.Close()
	if err := admin.Ping(); err != nil {
		t.Skipf("local postgres unavailable: %v", err)
	}
	database := fmt.Sprintf("control_plane_fresh_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE DATABASE ` + database); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1`, database)
		_, _ = admin.Exec(`DROP DATABASE ` + database)
	})

	store, err := NewPostgresEntStateStore("host=/var/run/postgresql dbname=" + database + " sslmode=disable")
	if err != nil {
		t.Fatalf("start store on fresh database: %v", err)
	}
	defer store.(*postgresEntStateStore).client.Close()
	accounts, err := store.ListAccounts(context.Background())
	if err != nil || len(accounts) != 0 {
		t.Fatalf("fresh account table = %v, err=%v", accounts, err)
	}
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
