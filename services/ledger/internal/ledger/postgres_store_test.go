package ledger

import (
	"database/sql"
	"strings"
	"testing"
)

func TestPostgresSchemaUsesEntMigrationLedgerTables(t *testing.T) {
	schema := PostgresSchemaSQL()
	required := []string{
		"CREATE TABLE IF NOT EXISTS wallets",
		"CREATE TABLE IF NOT EXISTS ledger_entries",
		"CREATE TABLE IF NOT EXISTS wallet_transactions",
		"CREATE TABLE IF NOT EXISTS manual_topups",
		"CREATE TABLE IF NOT EXISTS holds",
		"CREATE TABLE IF NOT EXISTS hold_releases",
		"CREATE TABLE IF NOT EXISTS evidence_receipts",
		"CREATE TABLE IF NOT EXISTS resource_settlements",
		"price_snapshot_json TEXT NOT NULL DEFAULT '{}'",
		"CREATE TABLE IF NOT EXISTS reconciliation_reports",
		"report_json TEXT NOT NULL DEFAULT '{}'",
		"CREATE TABLE IF NOT EXISTS idempotency_keys",
	}
	for _, marker := range required {
		if !strings.Contains(schema, marker) {
			t.Fatalf("schema missing %q", marker)
		}
	}
	if strings.Contains(schema, "JSONB") {
		t.Fatalf("ledger schema must not keep JSONB fact columns")
	}
}

func TestPostgresStoreImplementsLedgerStore(t *testing.T) {
	var db *sql.DB
	var _ Store = NewPostgresStore(db)
}
