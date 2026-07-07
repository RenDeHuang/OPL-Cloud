package ledger

import (
	"database/sql"
	"strings"
	"testing"
)

func TestPostgresSchemaUsesAppendFirstLedgerTables(t *testing.T) {
	schema := PostgresSchemaSQL()
	required := []string{
		"CREATE TABLE IF NOT EXISTS wallets",
		"CREATE TABLE IF NOT EXISTS ledger_entries",
		"CREATE TABLE IF NOT EXISTS wallet_transactions",
		"CREATE TABLE IF NOT EXISTS manual_topups",
		"ALTER TABLE manual_topups ADD COLUMN IF NOT EXISTS idempotency_key TEXT",
		"UPDATE manual_topups SET idempotency_key = 'migrated:' || ctid::text WHERE idempotency_key IS NULL",
		"ALTER TABLE manual_topups ALTER COLUMN idempotency_key SET NOT NULL",
		"ALTER TABLE holds ADD COLUMN IF NOT EXISTS idempotency_key TEXT",
		"ALTER TABLE holds ADD COLUMN IF NOT EXISTS resource_type TEXT",
		"ALTER TABLE holds ALTER COLUMN request_hash SET NOT NULL",
		"ALTER TABLE holds ALTER COLUMN resource_type SET NOT NULL",
		"CREATE TABLE IF NOT EXISTS hold_releases",
		"CREATE UNIQUE INDEX IF NOT EXISTS hold_releases_idempotency_key_idx",
		"CREATE TABLE IF NOT EXISTS idempotency_keys",
		"CREATE UNIQUE INDEX IF NOT EXISTS manual_topups_idempotency_key_idx",
	}
	for _, marker := range required {
		if !strings.Contains(schema, marker) {
			t.Fatalf("schema missing %q", marker)
		}
	}
}

func TestPostgresStoreImplementsLedgerStore(t *testing.T) {
	var db *sql.DB
	var _ Store = NewPostgresStore(db)
}
