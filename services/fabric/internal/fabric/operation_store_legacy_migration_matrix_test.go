package fabric

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"
)

func TestPostgresOperationStoreLegacyMigrationMatrix(t *testing.T) {
	t.Run("fresh_empty_database", func(t *testing.T) {
		databaseURL := legacyMigrationTestDatabaseURL(t)
		store, err := newTestPostgresOperationStore(databaseURL)
		if err != nil {
			t.Fatalf("fresh Fabric startup: %v", err)
		}
		closeTestOperationStore(t, store)

		db := openLegacyMigrationTestDB(t, databaseURL)
		assertFabricMigrationVersions(t, db, []string{
			"202607080001_fabric_operations_legacy_migration",
			"202607090001_ent_hard_cut",
			"202607110001_workspace_secret_authority_hard_cut",
			"202607110003_runtime_operation_claim",
			"202607120001_machine_ownership",
			"202607260001_compute_pool_admission",
			"202607290001_compute_claim_pending_pool_head",
		})
		assertFabricCurrentSchema(t, db)
	})

	t.Run("already_current_product_database_with_202607090001_journal", func(t *testing.T) {
		databaseURL := legacyMigrationTestDatabaseURL(t)
		db := openLegacyMigrationTestDB(t, databaseURL)
		createCurrentFabricSchema(t, db)
		recordFabricMigration(t, db, "202607090001_ent_hard_cut")

		store, err := newTestPostgresOperationStore(databaseURL)
		if err != nil {
			t.Fatalf("current Fabric startup: %v", err)
		}
		closeTestOperationStore(t, store)

		assertFabricMigrationVersions(t, db, []string{
			"202607080001_fabric_operations_legacy_migration",
			"202607090001_ent_hard_cut",
			"202607110001_workspace_secret_authority_hard_cut",
			"202607110003_runtime_operation_claim",
			"202607120001_machine_ownership",
			"202607260001_compute_pool_admission",
			"202607290001_compute_claim_pending_pool_head",
		})
		assertFabricCurrentSchema(t, db)
	})

	t.Run("completed_legacy_migration_without_journal_recovers", func(t *testing.T) {
		databaseURL := legacyMigrationTestDatabaseURL(t)
		db := openLegacyMigrationTestDB(t, databaseURL)
		createHistoricalFabricOperationsMatrixFixture(t, db, true)
		createEmptyFabricMigrationJournal(t, db)
		runHistoricalOperationMigrationDirectly(t, db)
		assertMigrationCount(t, db, "fabric", "202607080001_fabric_operations_legacy_migration", 0)
		assertLegacyOperationMigrated(t, db)

		store, err := newTestPostgresOperationStore(databaseURL)
		if err != nil {
			t.Fatalf("recover completed unjournaled migration: %v", err)
		}
		defer closeTestOperationStore(t, store)
		assertFabricMigrationVersions(t, db, []string{
			"202607080001_fabric_operations_legacy_migration",
			"202607090001_ent_hard_cut",
			"202607110001_workspace_secret_authority_hard_cut",
			"202607110003_runtime_operation_claim",
			"202607120001_machine_ownership",
			"202607260001_compute_pool_admission",
			"202607290001_compute_claim_pending_pool_head",
		})
		assertFabricCurrentSchema(t, db)
		assertInsertedCurrentFabricOperation(t, store)

		before, err := migrationJournalRows(db)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Install(context.Background()); err != nil {
			t.Fatalf("second recovered Fabric install: %v", err)
		}
		after, err := migrationJournalRows(db)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(after) != fmt.Sprint(before) {
			t.Fatalf("second recovered startup changed migration journal: before=%v after=%v", before, after)
		}
	})

	t.Run("unknown_transformed_hybrid_fails_closed", func(t *testing.T) {
		databaseURL := legacyMigrationTestDatabaseURL(t)
		db := openLegacyMigrationTestDB(t, databaseURL)
		createHistoricalFabricOperationsMatrixFixture(t, db, false)
		createEmptyFabricMigrationJournal(t, db)
		runHistoricalOperationMigrationDirectly(t, db)
		if _, err := db.Exec(`ALTER TABLE fabric_operations ADD COLUMN unknown_hybrid TEXT NOT NULL DEFAULT ''`); err != nil {
			t.Fatal(err)
		}

		if _, err := newTestPostgresOperationStore(databaseURL); err == nil {
			t.Fatal("unknown transformed hybrid unexpectedly recovered")
		}
		assertMigrationCount(t, db, "fabric", "202607080001_fabric_operations_legacy_migration", 0)
		assertMigrationCount(t, db, "fabric", "202607090001_ent_hard_cut", 0)
	})

	for _, testCase := range []struct {
		name   string
		mutate string
	}{
		{
			name:   "operation_id_drift",
			mutate: `UPDATE fabric_operations SET operation_id = 'drifted-operation-id' WHERE id = 'legacy-operation-1'`,
		},
		{
			name:   "caller_service_drift",
			mutate: `UPDATE fabric_operations SET caller_service = 'drifted-caller' WHERE id = 'legacy-operation-1'`,
		},
		{
			name:   "status_drift",
			mutate: `UPDATE fabric_operations SET status = 'drifted-status' WHERE id = 'legacy-operation-1'`,
		},
		{
			name:   "started_at_drift",
			mutate: `UPDATE fabric_operations SET started_at = started_at + INTERVAL '1 second' WHERE id = 'legacy-operation-1'`,
		},
		{
			name: "duplicate_correlation_id",
			mutate: `
				INSERT INTO fabric_operations (
					id, correlation_id, idempotency_key, requested_by, resource_id,
					resource_kind, state, operation_id, caller_service, status,
					created_at, started_at
				) VALUES (
					'legacy-operation-2', 'legacy-correlation-1', 'legacy-idempotency-2',
					'legacy-requester-2', 'legacy-resource-2', 'legacy-resource-kind',
					'pending', 'legacy-correlation-1', 'legacy-requester-2', 'pending',
					'2026-08-14T01:03:03Z', '2026-08-14T01:03:03Z'
				)
			`,
		},
	} {
		t.Run("transformed_hybrid_"+testCase.name+"_fails_closed", func(t *testing.T) {
			databaseURL := legacyMigrationTestDatabaseURL(t)
			db := openLegacyMigrationTestDB(t, databaseURL)
			createHistoricalFabricOperationsMatrixFixture(t, db, false)
			createEmptyFabricMigrationJournal(t, db)
			runHistoricalOperationMigrationDirectly(t, db)
			if _, err := db.Exec(testCase.mutate); err != nil {
				t.Fatal(err)
			}

			if _, err := newTestPostgresOperationStore(databaseURL); err == nil {
				t.Fatal("transformed hybrid row drift unexpectedly recovered")
			} else if !strings.Contains(err.Error(), "202607080001_fabric_operations_legacy_migration") {
				t.Fatalf("transformed hybrid row drift failed after legacy migration: %v", err)
			}
			assertMigrationJournalEmpty(t, db)
			assertNoPostHybridMigrationMutation(t, db)
		})
	}

	for _, uniqueIdempotencyKey := range []bool{false, true} {
		name := "pk_only_legacy"
		if uniqueIdempotencyKey {
			name = "pk_plus_unique_idempotency_key_legacy"
		}
		t.Run(name, func(t *testing.T) {
			databaseURL := legacyMigrationTestDatabaseURL(t)
			db := openLegacyMigrationTestDB(t, databaseURL)
			createHistoricalFabricOperationsMatrixFixture(t, db, uniqueIdempotencyKey)

			store, err := newTestPostgresOperationStore(databaseURL)
			if err != nil {
				t.Fatalf("legacy Fabric startup: %v", err)
			}
			defer closeTestOperationStore(t, store)

			assertLegacyOperationMigrated(t, db)
			assertFabricCurrentSchema(t, db)
			assertInsertedCurrentFabricOperation(t, store)

			before, err := migrationJournalRows(db)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Install(context.Background()); err != nil {
				t.Fatalf("second Fabric startup: %v", err)
			}
			after, err := migrationJournalRows(db)
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(after) != fmt.Sprint(before) {
				t.Fatalf("second startup changed migration journal: before=%v after=%v", before, after)
			}
			assertFabricMigrationVersions(t, db, []string{
				"202607080001_fabric_operations_legacy_migration",
				"202607090001_ent_hard_cut",
				"202607110001_workspace_secret_authority_hard_cut",
				"202607110003_runtime_operation_claim",
				"202607120001_machine_ownership",
				"202607260001_compute_pool_admission",
				"202607290001_compute_claim_pending_pool_head",
			})
		})
	}

	t.Run("unknown_shape_fails_closed", func(t *testing.T) {
		databaseURL := legacyMigrationTestDatabaseURL(t)
		db := openLegacyMigrationTestDB(t, databaseURL)
		createHistoricalFabricOperationsMatrixFixture(t, db, false)
		if _, err := db.Exec(`ALTER TABLE fabric_operations ADD COLUMN unknown_shape TEXT NOT NULL DEFAULT ''`); err != nil {
			t.Fatal(err)
		}

		if _, err := newTestPostgresOperationStore(databaseURL); err == nil {
			t.Fatal("unknown historical Fabric shape unexpectedly migrated")
		}
		assertMigrationJournalEmpty(t, db)
		var currentColumn bool
		if err := db.QueryRow(`
			SELECT EXISTS (
				SELECT 1
				FROM pg_attribute
				WHERE attrelid = 'fabric_operations'::regclass
				  AND attname = 'operation_id'
				  AND attnum > 0
				  AND NOT attisdropped
			)
		`).Scan(&currentColumn); err != nil {
			t.Fatal(err)
		}
		if currentColumn {
			t.Fatal("unknown historical shape was mutated before failure")
		}
	})
}

func createCurrentFabricSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	migration, err := os.ReadFile("ent_migrations/202607090001_ent_hard_cut.sql")
	if err != nil {
		t.Fatalf("read current Fabric migration: %v", err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatalf("create current Fabric schema: %v", err)
	}
}

func createHistoricalFabricOperationsMatrixFixture(t *testing.T, db *sql.DB, uniqueIdempotencyKey bool) {
	t.Helper()
	uniqueClause := ""
	if uniqueIdempotencyKey {
		uniqueClause = " UNIQUE"
	}
	if _, err := db.Exec(fmt.Sprintf(`
		CREATE TABLE fabric_operations (
			id TEXT PRIMARY KEY,
			correlation_id TEXT NOT NULL,
			idempotency_key TEXT NOT NULL%s,
			requested_by TEXT NOT NULL,
			resource_id TEXT NOT NULL,
			resource_kind TEXT NOT NULL,
			state TEXT NOT NULL,
			lease_owner TEXT NOT NULL DEFAULT '',
			lease_expires_at TIMESTAMPTZ,
			attempts INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			provider_refs JSONB NOT NULL DEFAULT '{}'::jsonb,
			evidence_refs JSONB NOT NULL DEFAULT '[]'::jsonb,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`, uniqueClause)); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"workspaces", "fabric_events", "fabric_evidence_refs", "idempotency_keys"} {
		query := fmt.Sprintf(`CREATE TABLE %s (id TEXT PRIMARY KEY, operation_id TEXT NOT NULL REFERENCES fabric_operations(id))`, pq.QuoteIdentifier(source))
		if _, err := db.Exec(query); err != nil {
			t.Fatalf("create inbound FK source %s: %v", source, err)
		}
	}
	if _, err := db.Exec(`
		INSERT INTO fabric_operations (
			id, correlation_id, idempotency_key, requested_by, resource_id, resource_kind,
			state, lease_owner, lease_expires_at, attempts, last_error, provider_refs,
			evidence_refs, created_at, updated_at
		) VALUES (
			'legacy-operation-1', 'legacy-correlation-1', 'legacy-idempotency-1', 'legacy-requester',
			'legacy-resource-1', 'legacy-resource-kind', 'pending', 'legacy-lease-owner',
			'2026-08-14T01:02:04Z', 2, 'legacy-error', '{"provider":"req-1"}',
			'{"receipt":"evidence-1"}', '2026-08-14T01:02:03Z', '2026-08-14T01:02:05Z'
		)
	`); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"workspaces", "fabric_events", "fabric_evidence_refs", "idempotency_keys"} {
		if _, err := db.Exec(`INSERT INTO `+pq.QuoteIdentifier(source)+` (id, operation_id) VALUES ($1, $2)`, source+"-row-1", "legacy-operation-1"); err != nil {
			t.Fatalf("seed inbound FK source %s: %v", source, err)
		}
	}
}

func assertLegacyOperationMigrated(t *testing.T, db *sql.DB) {
	t.Helper()
	var operationID, callerService, status string
	var startedAt time.Time
	if err := db.QueryRow(`
		SELECT operation_id, caller_service, status, started_at
		FROM fabric_operations WHERE id = 'legacy-operation-1'
	`).Scan(&operationID, &callerService, &status, &startedAt); err != nil {
		t.Fatal(err)
	}
	if operationID != "legacy-correlation-1" || callerService != "legacy-requester" || status != "pending" {
		t.Fatalf("legacy identity/state = (%q, %q, %q)", operationID, callerService, status)
	}
	if !startedAt.Equal(time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)) {
		t.Fatalf("legacy started_at = %s, want created_at", startedAt)
	}
	var inboundFKCount int
	if err := db.QueryRow(`
		SELECT count(*)
		FROM pg_constraint
		WHERE contype = 'f' AND confrelid = 'fabric_operations'::regclass
	`).Scan(&inboundFKCount); err != nil {
		t.Fatal(err)
	}
	if inboundFKCount != 4 {
		t.Fatalf("inbound foreign key count = %d, want 4", inboundFKCount)
	}
	var providerRefs, evidenceRefs string
	if err := db.QueryRow(`
		SELECT provider_refs::text, evidence_refs::text
		FROM fabric_operations WHERE id = 'legacy-operation-1'
	`).Scan(&providerRefs, &evidenceRefs); err != nil {
		t.Fatal(err)
	}
	if providerRefs != `{"provider": "req-1"}` || evidenceRefs != `{"receipt": "evidence-1"}` {
		t.Fatalf("legacy references changed: provider_refs=%q evidence_refs=%q", providerRefs, evidenceRefs)
	}
}

func assertInsertedCurrentFabricOperation(t *testing.T, store *PostgresOperationStore) {
	t.Helper()
	operation := FabricOperation{
		ID:             "fop-current-after-legacy",
		OperationID:    "launch-current-after-legacy",
		CallerService:  "control-plane",
		Action:         "create_compute_allocation",
		ResourceKind:   "compute_allocation",
		ResourceID:     "compute-current-after-legacy",
		AccountID:      "account-current-after-legacy",
		WorkspaceID:    "workspace-current-after-legacy",
		Provider:       "tencent",
		IdempotencyKey: "launch-current-after-legacy:compute",
		RequestHash:    "request-hash-current-after-legacy",
		Status:         "started",
		StartedAt:      time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC),
		CreatedAt:      time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC),
	}
	if err := store.Append(context.Background(), operation); err != nil {
		t.Fatalf("append current Fabric operation: %v", err)
	}
	got, err := store.Get(context.Background(), operation.ID)
	if err != nil {
		t.Fatalf("read current Fabric operation: %v", err)
	}
	if got.ID != operation.ID || got.OperationID != operation.OperationID || got.Status != operation.Status {
		t.Fatalf("current operation = %#v, want %#v", got, operation)
	}
}

func assertFabricCurrentSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	var runtimeAccessExists bool
	if err := db.QueryRow(`SELECT to_regclass('fabric_workspace_runtime_access') IS NOT NULL`).Scan(&runtimeAccessExists); err != nil {
		t.Fatal(err)
	}
	if runtimeAccessExists {
		t.Fatal("retired workspace runtime access table remains")
	}
	var machineOwnershipsExists bool
	if err := db.QueryRow(`SELECT to_regclass('machine_ownerships') IS NOT NULL`).Scan(&machineOwnershipsExists); err != nil {
		t.Fatal(err)
	}
	if !machineOwnershipsExists {
		t.Fatal("machine ownership migration did not run")
	}
	for _, column := range []string{"compute_pool_key", "compute_pool_lease_owner", "compute_pool_lease_expires_at"} {
		var exists bool
		if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'fabric_operations' AND column_name = $1)`, column).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("fabric_operations missing %s", column)
		}
	}
	var runtimeClaimIndex, computeClaimIndex, poolHeadIndex bool
	if err := db.QueryRow(`
		SELECT
			EXISTS (SELECT 1 FROM pg_class WHERE relname = 'fabric_operations_runtime_claim_idx'),
			EXISTS (SELECT 1 FROM pg_class WHERE relname = 'fabric_operations_compute_claim_idx'),
			EXISTS (SELECT 1 FROM pg_class WHERE relname = 'fabric_operations_compute_pool_head_idx')
	`).Scan(&runtimeClaimIndex, &computeClaimIndex, &poolHeadIndex); err != nil {
		t.Fatal(err)
	}
	if !runtimeClaimIndex || !computeClaimIndex || !poolHeadIndex {
		t.Fatalf("later migration indexes = runtime=%t compute=%t pool_head=%t", runtimeClaimIndex, computeClaimIndex, poolHeadIndex)
	}
	var poolHeadPredicate string
	if err := db.QueryRow(`
		SELECT pg_get_expr(i.indpred, i.indrelid)
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid
		WHERE c.relname = 'fabric_operations_compute_pool_head_idx'
	`).Scan(&poolHeadPredicate); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(poolHeadPredicate, "claim_pending") {
		t.Fatalf("pool head predicate = %q, want claim_pending", poolHeadPredicate)
	}
}

func assertFabricMigrationVersions(t *testing.T, db *sql.DB, want []string) {
	t.Helper()
	rows, err := db.Query(`
		SELECT version
		FROM opl_schema_migrations
		WHERE service = 'fabric'
		ORDER BY version
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			t.Fatal(err)
		}
		got = append(got, version)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("Fabric migration versions = %v, want %v", got, want)
	}
}

func assertMigrationJournalEmpty(t *testing.T, db *sql.DB) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM opl_schema_migrations WHERE service = 'fabric'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed migration left %d Fabric journal rows", count)
	}
}

func assertNoPostHybridMigrationMutation(t *testing.T, db *sql.DB) {
	t.Helper()
	var computePoolColumn, machineOwnershipTable bool
	if err := db.QueryRow(`
		SELECT
			EXISTS (
				SELECT 1
				FROM pg_attribute
				WHERE attrelid = 'fabric_operations'::regclass
				  AND attname = 'compute_pool_key'
				  AND attnum > 0
				  AND NOT attisdropped
			),
			to_regclass('machine_ownerships') IS NOT NULL
	`).Scan(&computePoolColumn, &machineOwnershipTable); err != nil {
		t.Fatal(err)
	}
	if computePoolColumn || machineOwnershipTable {
		t.Fatalf("later Fabric schema mutation observed: compute_pool_key=%t machine_ownerships=%t", computePoolColumn, machineOwnershipTable)
	}
}

func migrationJournalRows(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT service || '/' || version FROM opl_schema_migrations ORDER BY service, version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func recordFabricMigration(t *testing.T, db *sql.DB, version string) {
	t.Helper()
	createEmptyFabricMigrationJournal(t, db)
	if _, err := db.Exec(`INSERT INTO opl_schema_migrations (service, version) VALUES ('fabric', $1)`, version); err != nil {
		t.Fatal(err)
	}
}

func createEmptyFabricMigrationJournal(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE opl_schema_migrations (service TEXT NOT NULL, version TEXT NOT NULL, applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY (service, version))`); err != nil {
		t.Fatal(err)
	}
}

func runHistoricalOperationMigrationDirectly(t *testing.T, db *sql.DB) {
	t.Helper()
	migration, err := os.ReadFile("ent_migrations/202607080001_fabric_operations_legacy_migration.sql")
	if err != nil {
		t.Fatalf("read historical operation migration: %v", err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatalf("execute historical operation migration directly: %v", err)
	}
}

func openLegacyMigrationTestDB(t *testing.T, databaseURL string) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func closeTestOperationStore(t *testing.T, store *PostgresOperationStore) {
	t.Helper()
	if store == nil {
		return
	}
	if err := store.client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.db.Close(); err != nil {
		t.Fatal(err)
	}
}

func legacyMigrationTestDatabaseURL(t *testing.T) string {
	t.Helper()
	databaseURL := os.Getenv("FABRIC_TEST_DATABASE_URL")
	optional := false
	if databaseURL == "" {
		if os.Getenv("OPL_POSTGRES_TESTS") == "1" {
			databaseURL = "connect_timeout=10"
		} else {
			databaseURL = "host=/var/run/postgresql dbname=postgres sslmode=disable"
			optional = true
		}
	}
	admin, err := sql.Open("postgres", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := admin.Ping(); err != nil {
		_ = admin.Close()
		if optional {
			t.Skipf("local PostgreSQL unavailable: %v", err)
		}
		t.Fatal(err)
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	schema := "fabric_legacy_matrix_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(`CREATE SCHEMA ` + pq.QuoteIdentifier(schema)); err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + pq.QuoteIdentifier(schema) + ` CASCADE`)
		_ = admin.Close()
	})
	if parsed, err := url.Parse(databaseURL); err == nil && parsed.Scheme != "" {
		query := parsed.Query()
		query.Set("search_path", schema)
		query.Set("connect_timeout", "5")
		query.Set("statement_timeout", "10000")
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return databaseURL + " search_path=" + schema + " connect_timeout=5 statement_timeout=10000"
}
