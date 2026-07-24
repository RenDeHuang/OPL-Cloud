package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/lib/pq"
)

type recordingDriver struct {
	dialect.Driver
	query string
}

func TestPilotAnnouncementsMigration(t *testing.T) {
	raw, err := os.ReadFile("202607190002_pilot_announcements.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS control_plane_announcements",
		"CREATE TABLE IF NOT EXISTS control_plane_announcement_reads",
		"CHECK (status IN ('draft', 'scheduled', 'published', 'withdrawn'))",
		"CHECK (ends_at = '' OR starts_at = '' OR ends_at::timestamptz > starts_at::timestamptz)",
		"UNIQUE (announcement_id, user_id)",
		"ADD CONSTRAINT control_plane_announcements_status_check",
		"ADD CONSTRAINT control_plane_announcements_schedule_check",
		"ADD CONSTRAINT control_plane_announcement_reads_announcement_fk",
		"ADD CONSTRAINT control_plane_announcement_reads_user_unique",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("Pilot announcement migration missing %q", required)
		}
	}
	driver := &recordingDriver{}
	if err := ApplyPilotAnnouncements(context.Background(), driver); err != nil {
		t.Fatal(err)
	}
	if driver.query != sql {
		t.Fatal("ApplyPilotAnnouncements did not execute the embedded migration")
	}
}

func TestWorkspaceAPIKeyIDMigration(t *testing.T) {
	raw, err := os.ReadFile("202607190001_workspace_api_key_id.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for _, required := range []string{
		"ADD COLUMN IF NOT EXISTS workspace_api_key_id BIGINT",
		"workspace_api_key_id IS NULL OR workspace_api_key_id > 0",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("Workspace Key migration missing %q", required)
		}
	}
	driver := &recordingDriver{}
	if err := ApplyWorkspaceAPIKeyID(context.Background(), driver); err != nil {
		t.Fatal(err)
	}
	if driver.query != sql {
		t.Fatal("ApplyWorkspaceAPIKeyID did not execute the embedded migration")
	}
}

func TestWorkspacePurchaseReceiptIDMigrationIsAdditiveAndIdempotent(t *testing.T) {
	raw, err := os.ReadFile("202607230001_workspace_purchase_receipt_id.sql")
	if err != nil {
		t.Fatal(err)
	}
	const want = `ALTER TABLE control_plane_workspaces
  ADD COLUMN IF NOT EXISTS purchase_receipt_id TEXT NOT NULL DEFAULT '';`
	if strings.TrimSpace(string(raw)) != want {
		t.Fatalf("Workspace purchase Receipt migration=%q, want exact additive statement", strings.TrimSpace(string(raw)))
	}

	databaseURL := requiredIdentityTestDatabaseURL(t)
	admin, err := sql.Open("postgres", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	if err := admin.Ping(); err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("control_plane_workspace_purchase_receipt_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`) })
	db, err := sql.Open("postgres", postgresURLWithSearchPath(databaseURL, schema))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE control_plane_workspaces (id TEXT PRIMARY KEY); INSERT INTO control_plane_workspaces VALUES ('ws-existing')`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := db.Exec(string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	var purchaseReceiptID string
	if err := db.QueryRow(`SELECT purchase_receipt_id FROM control_plane_workspaces WHERE id = 'ws-existing'`).Scan(&purchaseReceiptID); err != nil || purchaseReceiptID != "" {
		t.Fatalf("existing Workspace purchase Receipt ID=%q err=%v", purchaseReceiptID, err)
	}
}

func TestMultiWorkspacePaginationMigrationIsAdditiveAndIdempotent(t *testing.T) {
	raw, err := os.ReadFile("202607240001_multi_workspace_pagination.sql")
	if err != nil {
		t.Fatal(err)
	}
	sqlText := string(raw)
	for _, required := range []string{
		"DROP INDEX IF EXISTS control_plane_workspaces_primary_account_key",
		"CREATE INDEX IF NOT EXISTS control_plane_workspaces_account_page_key",
		"COALESCE(NULLIF(account_id, ''), owner_account_id)",
		"customer_product",
		"id",
	} {
		if !strings.Contains(sqlText, required) {
			t.Fatalf("multi-Workspace migration missing %q", required)
		}
	}

	databaseURL := requiredIdentityTestDatabaseURL(t)
	admin, err := sql.Open("postgres", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("control_plane_multi_workspace_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`) })
	db, err := sql.Open("postgres", postgresURLWithSearchPath(databaseURL, schema))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE control_plane_workspaces (
			id TEXT PRIMARY KEY,
			account_id TEXT NOT NULL DEFAULT '',
			owner_account_id TEXT NOT NULL DEFAULT '',
			customer_product BOOLEAN NOT NULL DEFAULT TRUE
		);
		INSERT INTO control_plane_workspaces (id, account_id, owner_account_id)
		VALUES ('ws-existing', 'acct-alpha', 'acct-alpha');
		CREATE UNIQUE INDEX control_plane_workspaces_primary_account_key
			ON control_plane_workspaces ((COALESCE(NULLIF(account_id, ''), owner_account_id)))
			WHERE COALESCE(NULLIF(account_id, ''), owner_account_id) <> '';
	`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := db.Exec(sqlText); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO control_plane_workspaces (id, account_id, owner_account_id) VALUES ('ws-second', 'acct-alpha', 'acct-alpha')`); err != nil {
		t.Fatalf("same account second Workspace rejected after migration: %v", err)
	}
	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM control_plane_workspaces WHERE account_id = 'acct-alpha'`).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("same-account Workspaces=%d err=%v", rows, err)
	}
	var legacyUnique, pageIndex bool
	if err := db.QueryRow(`SELECT to_regclass(current_schema() || '.control_plane_workspaces_primary_account_key') IS NOT NULL`).Scan(&legacyUnique); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT to_regclass(current_schema() || '.control_plane_workspaces_account_page_key') IS NOT NULL`).Scan(&pageIndex); err != nil {
		t.Fatal(err)
	}
	if legacyUnique || !pageIndex {
		t.Fatalf("legacyUnique=%v pageIndex=%v", legacyUnique, pageIndex)
	}
}

func (d *recordingDriver) Tx(context.Context) (dialect.Tx, error) {
	return dialect.NopTx(d), nil
}

func (d *recordingDriver) Exec(_ context.Context, query string, _ any, _ any) error {
	d.query = query
	return nil
}

func TestApplyExecutesEmbeddedMonthlyHardCut(t *testing.T) {
	driver := &recordingDriver{}
	if err := Apply(context.Background(), driver); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"DROP TABLE IF EXISTS control_plane_wallet_projections",
		"ADD COLUMN IF NOT EXISTS sub2api_user_id",
		"DROP COLUMN IF EXISTS hold_id",
	} {
		if !strings.Contains(driver.query, required) {
			t.Fatalf("embedded migration missing %q", required)
		}
	}
}

func TestApplySub2APIUserUniquenessFailsClosedAndAddsPartialIndex(t *testing.T) {
	driver := &recordingDriver{}
	if err := ApplySub2APIUserUniqueness(context.Background(), driver); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"GROUP BY sub2api_user_id",
		"RAISE EXCEPTION 'duplicate sub2api_user_id mappings'",
		"CREATE UNIQUE INDEX",
		"WHERE sub2api_user_id > 0",
	} {
		if !strings.Contains(driver.query, required) {
			t.Fatalf("embedded mapping migration missing %q", required)
		}
	}
}

func TestApplyPrimaryWorkspaceAddsClassificationAndFailsClosedOnDuplicates(t *testing.T) {
	driver := &recordingDriver{}
	if err := ApplyPrimaryWorkspace(context.Background(), driver); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"ADD COLUMN IF NOT EXISTS verification_slot_id",
		"ADD COLUMN IF NOT EXISTS customer_product",
		"duplicate primary Workspaces",
		"COALESCE(NULLIF(account_id, ''), owner_account_id)",
		"CREATE UNIQUE INDEX",
	} {
		if !strings.Contains(driver.query, required) {
			t.Fatalf("embedded primary Workspace migration missing %q", required)
		}
	}
}

func TestAutoRenewAuditMigrationUpgradesExistingTablesIdempotently(t *testing.T) {
	databaseURL := requiredIdentityTestDatabaseURL(t)
	admin, err := sql.Open("postgres", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	if err := admin.Ping(); err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("control_plane_auto_renew_audit_migration_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`) })
	db, err := sql.Open("postgres", postgresURLWithSearchPath(databaseURL, schema))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE control_plane_admin_audit_events (id TEXT PRIMARY KEY);
		CREATE TABLE control_plane_archived_admin_audit_events (id TEXT PRIMARY KEY);
		INSERT INTO control_plane_admin_audit_events VALUES ('audit-active');
		INSERT INTO control_plane_archived_admin_audit_events VALUES ('audit-archived');
	`); err != nil {
		t.Fatal(err)
	}
	driver := entsql.OpenDB(dialect.Postgres, db)
	for range 2 {
		if err := ApplyAutoRenewAudit(context.Background(), driver); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"control_plane_admin_audit_events", "control_plane_archived_admin_audit_events"} {
		var before, after string
		if err := db.QueryRow(`SELECT before_json, after_json FROM `+table+` LIMIT 1`).Scan(&before, &after); err != nil {
			t.Fatal(err)
		}
		if before != "" || after != "" {
			t.Fatalf("%s existing row snapshots = %q/%q", table, before, after)
		}
		var columns int
		if err := db.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1 AND column_name IN ('before_json', 'after_json')`, table).Scan(&columns); err != nil {
			t.Fatal(err)
		}
		if columns != 2 {
			t.Fatalf("%s snapshot column count=%d", table, columns)
		}
	}
}

func TestWorkspaceRenewalMigrationPostgres(t *testing.T) {
	databaseURL := requiredIdentityTestDatabaseURL(t)
	admin, err := sql.Open("postgres", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	if err := admin.Ping(); err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("control_plane_workspace_renewal_migration_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`) })
	db, err := sql.Open("postgres", postgresURLWithSearchPath(databaseURL, schema))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE control_plane_workspaces (
			id text PRIMARY KEY, account_id text NOT NULL, owner_account_id text NOT NULL,
			owner_user_id text NOT NULL, current_compute_allocation_id text NOT NULL, storage_id text NOT NULL
		);
		CREATE TABLE control_plane_users (
			id text PRIMARY KEY, account_id text NOT NULL, status text NOT NULL, role text NOT NULL
		);
		CREATE TABLE control_plane_compute_allocations (
			id text PRIMARY KEY, account_id text NOT NULL, owner_user_id text NOT NULL, workspace_id text NOT NULL,
			package_id text NOT NULL, billing_status text NOT NULL, billing_state_json text NOT NULL
		);
		CREATE TABLE control_plane_storage_volumes (
			id text PRIMARY KEY, account_id text NOT NULL, owner_user_id text NOT NULL, workspace_id text NOT NULL,
			package_id text NOT NULL, billing_status text NOT NULL, size_gb double precision NOT NULL, billing_state_json text NOT NULL
		);
		INSERT INTO control_plane_users VALUES
			('usr-basic', 'acct-basic', 'active', 'owner'),
			('usr-true', 'acct-true', 'active', 'owner'),
			('usr-pro', 'acct-pro', 'active', 'owner');
		INSERT INTO control_plane_workspaces VALUES ('ws-basic', 'acct-basic', 'acct-basic', 'usr-basic', 'compute-basic', 'storage-basic');
		INSERT INTO control_plane_compute_allocations VALUES (
			'compute-basic', 'acct-basic', 'usr-basic', 'ws-basic', 'basic', 'active',
			'{"autoRenew":false,"priceVersion":"pilot-usd-2026-07-v1","currency":"USD","priceSnapshot":{"resourceType":"compute","priceVersion":"pilot-usd-2026-07-v1","packageId":"basic","currency":"USD","billingUnit":"calendar_month","chargeUsdMicros":50000000},"chargeUsdMicros":50000000,"billingAnchorDay":17,"periodStart":"2026-07-17T01:02:03Z","paidThrough":"2026-08-17T01:02:03Z","deadline":"2026-08-17T01:02:03Z","providerData":{"deadline":"2026-08-17T01:02:03Z"}}'
		);
		INSERT INTO control_plane_storage_volumes VALUES (
			'storage-basic', 'acct-basic', 'usr-basic', 'ws-basic', 'basic', 'active', 10,
			'{"autoRenew":false,"priceVersion":"pilot-usd-2026-07-v1","currency":"USD","priceSnapshot":{"resourceType":"storage","priceVersion":"pilot-usd-2026-07-v1","packageId":"basic","sizeGb":10,"currency":"USD","billingUnit":"calendar_month","chargeUsdMicros":2580000},"chargeUsdMicros":2580000,"billingAnchorDay":17,"periodStart":"2026-07-17T01:02:03Z","paidThrough":"2026-08-17T01:02:03Z","computeAllocationId":"compute-basic","deadline":"2026-08-17T01:02:03Z","providerData":{"deadline":"2026-08-17T01:02:03Z"}}'
		);
		INSERT INTO control_plane_workspaces VALUES ('ws-true', 'acct-true', 'acct-true', 'usr-true', 'compute-true', 'storage-true');
		INSERT INTO control_plane_compute_allocations
		SELECT 'compute-true', 'acct-true', 'usr-true', 'ws-true', package_id, billing_status,
			jsonb_set(billing_state_json::jsonb, '{autoRenew}', 'true')::text
		FROM control_plane_compute_allocations WHERE id = 'compute-basic';
		INSERT INTO control_plane_storage_volumes
		SELECT 'storage-true', 'acct-true', 'usr-true', 'ws-true', package_id, billing_status, size_gb,
			jsonb_set(jsonb_set(billing_state_json::jsonb, '{autoRenew}', 'true'), '{computeAllocationId}', '"compute-true"')::text
		FROM control_plane_storage_volumes WHERE id = 'storage-basic';
		INSERT INTO control_plane_workspaces VALUES ('ws-pro', 'acct-pro', 'acct-pro', 'usr-pro', 'compute-pro', 'storage-pro');
		INSERT INTO control_plane_compute_allocations VALUES (
			'compute-pro', 'acct-pro', 'usr-pro', 'ws-pro', 'pro', 'active',
			'{"autoRenew":false,"priceVersion":"pilot-usd-2026-07-v1","currency":"USD","priceSnapshot":{"resourceType":"compute","priceVersion":"pilot-usd-2026-07-v1","packageId":"pro","currency":"USD","billingUnit":"calendar_month","chargeUsdMicros":214280000},"chargeUsdMicros":214280000,"billingAnchorDay":17,"periodStart":"2026-07-17T01:02:03.123456Z","paidThrough":"2026-08-17T01:02:03.123456Z","deadline":"2026-08-17T01:02:03.123456Z","providerData":{"deadline":"2026-08-17T01:02:03.123456Z"}}'
		);
		INSERT INTO control_plane_storage_volumes VALUES (
			'storage-pro', 'acct-pro', 'usr-pro', 'ws-pro', 'pro', 'active', 100,
			'{"autoRenew":false,"priceVersion":"pilot-usd-2026-07-v1","currency":"USD","priceSnapshot":{"resourceType":"storage","priceVersion":"pilot-usd-2026-07-v1","packageId":"pro","sizeGb":100,"currency":"USD","billingUnit":"calendar_month","chargeUsdMicros":25800000},"chargeUsdMicros":25800000,"billingAnchorDay":17,"periodStart":"2026-07-17T01:02:03.123456Z","paidThrough":"2026-08-17T01:02:03.123456Z","computeAllocationId":"compute-pro","deadline":"2026-08-17T01:02:03.123456Z","providerData":{"deadline":"2026-08-17T01:02:03.123456Z"}}'
		);
	`); err != nil {
		t.Fatal(err)
	}
	if err := ApplyWorkspaceRenewal(context.Background(), entsql.OpenDB(dialect.Postgres, db)); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.QueryRow(`SELECT billing_state_json FROM control_plane_workspaces WHERE id = 'ws-basic'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		t.Fatal(err)
	}
	if state["autoRenew"] != false || state["renewalStatus"] != "active" || state["packageId"] != "basic" ||
		state["computeUsdMicros"] != float64(50_000_000) || state["storageUsdMicros"] != float64(2_580_000) || state["totalUsdMicros"] != float64(52_580_000) ||
		state["authorizedBy"] != "" || state["authorizedAt"] != "" || state["nextRenewalAt"] != "2026-08-16T01:02:03Z" {
		t.Fatalf("Workspace renewal backfill=%s", raw)
	}
	basicRaw := raw
	if err := db.QueryRow(`SELECT billing_state_json FROM control_plane_workspaces WHERE id = 'ws-pro'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	state = nil
	if err := json.Unmarshal([]byte(raw), &state); err != nil || state["renewalStatus"] != "active" || state["packageId"] != "pro" ||
		state["storageGb"] != float64(100) || state["computeUsdMicros"] != float64(214_280_000) ||
		state["storageUsdMicros"] != float64(25_800_000) || state["totalUsdMicros"] != float64(240_080_000) ||
		state["periodStart"] != "2026-07-17T01:02:03.123456Z" || state["paidThrough"] != "2026-08-17T01:02:03.123456Z" ||
		state["nextRenewalAt"] != "2026-08-16T01:02:03.123456Z" {
		t.Fatalf("Workspace Pro renewal backfill=%s err=%v", raw, err)
	}
	if err := db.QueryRow(`SELECT billing_state_json FROM control_plane_workspaces WHERE id = 'ws-true'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	state = nil
	if err := json.Unmarshal([]byte(raw), &state); err != nil || len(state) != 3 || state["autoRenew"] != false ||
		state["renewalStatus"] != "manual_review" || state["manualReviewReason"] != "legacy_billing_state_mismatch" {
		t.Fatalf("Workspace true-switch marker=%s err=%v", raw, err)
	}
	trueRaw := raw

	seedMismatch := func(name string) {
		t.Helper()
		workspaceID, accountID, ownerID := "ws-"+name, "acct-"+name, "usr-"+name
		computeID, storageID := "compute-"+name, "storage-"+name
		if name != "owner-missing" {
			status, role, userAccountID := "active", "owner", accountID
			switch name {
			case "owner-disabled":
				status = "disabled"
			case "owner-deleted":
				status = "deleted"
			case "owner-member":
				role = "member"
			case "owner-admin":
				role = "admin"
			case "owner-account-mismatch":
				userAccountID = "acct-other"
			}
			if _, err := db.Exec(`INSERT INTO control_plane_users (id, account_id, status, role) VALUES ($1, $2, $3, $4)`, ownerID, userAccountID, status, role); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Exec(`INSERT INTO control_plane_workspaces
			(id, account_id, owner_account_id, owner_user_id, current_compute_allocation_id, storage_id, billing_state_json)
			VALUES ($1, $2, $2, $3, $4, $5, '{}')`, workspaceID, accountID, ownerID, computeID, storageID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO control_plane_compute_allocations
			SELECT $4, $2, $3, $1, package_id, billing_status, billing_state_json
			FROM control_plane_compute_allocations WHERE id = 'compute-basic'`, workspaceID, accountID, ownerID, computeID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO control_plane_storage_volumes
			SELECT $5, $2, $3, $1, package_id, billing_status, size_gb,
				jsonb_set(billing_state_json::jsonb, '{computeAllocationId}', to_jsonb($4::text))::text
			FROM control_plane_storage_volumes WHERE id = 'storage-basic'`, workspaceID, accountID, ownerID, computeID, storageID); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{
		"switch", "period", "price", "pointer", "owner", "child-corrupt", "anchor", "time", "deadline-missing", "deadline-invalid", "deadline-early",
		"owner-missing", "owner-disabled", "owner-deleted", "owner-member", "owner-admin", "owner-account-mismatch",
	} {
		seedMismatch(name)
	}
	if _, err := db.Exec(`
		UPDATE control_plane_storage_volumes SET billing_state_json = jsonb_set(billing_state_json::jsonb, '{autoRenew}', 'true')::text WHERE id = 'storage-switch';
		UPDATE control_plane_storage_volumes SET billing_state_json = jsonb_set(billing_state_json::jsonb, '{paidThrough}', '"2026-08-18T01:02:03Z"')::text WHERE id = 'storage-period';
		UPDATE control_plane_compute_allocations SET billing_state_json = jsonb_set(billing_state_json::jsonb, '{chargeUsdMicros}', '1')::text WHERE id = 'compute-price';
		UPDATE control_plane_storage_volumes SET billing_state_json = jsonb_set(billing_state_json::jsonb, '{computeAllocationId}', '"compute-other"')::text WHERE id = 'storage-pointer';
		UPDATE control_plane_storage_volumes SET owner_user_id = 'usr-other' WHERE id = 'storage-owner';
		UPDATE control_plane_compute_allocations SET billing_state_json = '{not-json' WHERE id = 'compute-child-corrupt';
		UPDATE control_plane_compute_allocations SET billing_state_json = jsonb_set(billing_state_json::jsonb, '{billingAnchorDay}', '"x"')::text WHERE id = 'compute-anchor';
		UPDATE control_plane_storage_volumes SET billing_state_json = jsonb_set(billing_state_json::jsonb, '{billingAnchorDay}', '"x"')::text WHERE id = 'storage-anchor';
		UPDATE control_plane_compute_allocations SET billing_state_json = jsonb_set(billing_state_json::jsonb, '{periodStart}', '"2026-07-17"')::text WHERE id = 'compute-time';
		UPDATE control_plane_storage_volumes SET billing_state_json = jsonb_set(billing_state_json::jsonb, '{periodStart}', '"2026-07-17"')::text WHERE id = 'storage-time';
		UPDATE control_plane_compute_allocations SET billing_state_json = ((billing_state_json::jsonb - 'deadline') #- '{providerData,deadline}')::text WHERE id = 'compute-deadline-missing';
		UPDATE control_plane_storage_volumes SET billing_state_json = ((billing_state_json::jsonb - 'deadline') #- '{providerData,deadline}')::text WHERE id = 'storage-deadline-missing';
		UPDATE control_plane_compute_allocations SET billing_state_json = jsonb_set(billing_state_json::jsonb, '{deadline}', '"not-a-time"')::text WHERE id = 'compute-deadline-invalid';
		UPDATE control_plane_storage_volumes SET billing_state_json = jsonb_set(billing_state_json::jsonb, '{deadline}', '"not-a-time"')::text WHERE id = 'storage-deadline-invalid';
		UPDATE control_plane_compute_allocations SET billing_state_json = jsonb_set(jsonb_set(billing_state_json::jsonb, '{deadline}', '"2026-08-17T01:02:02Z"'), '{providerData,deadline}', '"2026-08-17T01:02:02Z"')::text WHERE id = 'compute-deadline-early';
		UPDATE control_plane_storage_volumes SET billing_state_json = jsonb_set(jsonb_set(billing_state_json::jsonb, '{deadline}', '"2026-08-17T01:02:02Z"'), '{providerData,deadline}', '"2026-08-17T01:02:02Z"')::text WHERE id = 'storage-deadline-early';
		INSERT INTO control_plane_workspaces
			(id, account_id, owner_account_id, owner_user_id, current_compute_allocation_id, storage_id, billing_state_json)
		VALUES
			('ws-blank', 'acct-blank', 'acct-blank', 'usr-blank', 'compute-missing', 'storage-missing', '   '),
			('ws-corrupt', 'acct-corrupt', 'acct-corrupt', 'usr-corrupt', 'compute-missing', 'storage-missing', ' {not-json} ');
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO control_plane_workspaces
		(id, account_id, owner_account_id, owner_user_id, current_compute_allocation_id, storage_id, billing_state_json)
		VALUES ('ws-existing', 'acct-existing', 'acct-existing', 'usr-existing', 'compute-missing', 'storage-missing', $1)`, basicRaw); err != nil {
		t.Fatal(err)
	}
	if err := ApplyWorkspaceRenewal(context.Background(), entsql.OpenDB(dialect.Postgres, db)); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"ws-basic": basicRaw, "ws-true": trueRaw, "ws-corrupt": " {not-json} ", "ws-existing": basicRaw} {
		if err := db.QueryRow(`SELECT billing_state_json FROM control_plane_workspaces WHERE id = $1`, id).Scan(&raw); err != nil || raw != want {
			t.Fatalf("preserved Workspace %s billing bytes=%q want=%q err=%v", id, raw, want, err)
		}
	}
	for _, id := range []string{
		"ws-blank", "ws-switch", "ws-period", "ws-price", "ws-pointer", "ws-owner", "ws-child-corrupt", "ws-anchor", "ws-time", "ws-deadline-missing", "ws-deadline-invalid", "ws-deadline-early",
		"ws-owner-missing", "ws-owner-disabled", "ws-owner-deleted", "ws-owner-member", "ws-owner-admin", "ws-owner-account-mismatch",
	} {
		if err := db.QueryRow(`SELECT billing_state_json FROM control_plane_workspaces WHERE id = $1`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		state = nil
		if err := json.Unmarshal([]byte(raw), &state); err != nil || len(state) != 3 || state["autoRenew"] != false ||
			state["renewalStatus"] != "manual_review" || state["manualReviewReason"] != "legacy_billing_state_mismatch" {
			t.Fatalf("Workspace %s marker=%s err=%v", id, raw, err)
		}
	}
}
