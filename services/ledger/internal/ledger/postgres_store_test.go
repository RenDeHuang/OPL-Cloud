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
		"ALTER TABLE ledger_entries ADD COLUMN IF NOT EXISTS reason TEXT",
		"CREATE TABLE IF NOT EXISTS wallet_transactions",
		"ALTER TABLE wallet_transactions ADD COLUMN IF NOT EXISTS ledger_entry_id TEXT",
		"ALTER TABLE wallet_transactions ADD COLUMN IF NOT EXISTS frozen_cents BIGINT",
		"ALTER TABLE wallet_transactions ADD COLUMN IF NOT EXISTS available_cents BIGINT",
		"ALTER TABLE wallet_transactions ADD COLUMN IF NOT EXISTS total_spent_cents BIGINT",
		"ALTER TABLE wallet_transactions ALTER COLUMN ledger_entry_id SET NOT NULL",
		"ALTER TABLE wallet_transactions DROP COLUMN IF EXISTS user_id",
		"ALTER TABLE wallet_transactions DROP COLUMN IF EXISTS workspace_id",
		"ALTER TABLE wallet_transactions DROP COLUMN IF EXISTS transaction_type",
		"ALTER TABLE wallet_transactions DROP COLUMN IF EXISTS source_event_id",
		"ALTER TABLE wallet_transactions DROP COLUMN IF EXISTS state",
		"CREATE TABLE IF NOT EXISTS manual_topups",
		"ALTER TABLE manual_topups ADD COLUMN IF NOT EXISTS account_id TEXT",
		"UPDATE manual_topups SET account_id = target_account_id",
		"ALTER TABLE manual_topups ALTER COLUMN account_id SET NOT NULL",
		"ALTER TABLE manual_topups ADD COLUMN IF NOT EXISTS wallet_transaction_id TEXT",
		"ALTER TABLE manual_topups DROP COLUMN IF EXISTS target_user_id",
		"ALTER TABLE manual_topups DROP COLUMN IF EXISTS target_account_id",
		"ALTER TABLE manual_topups DROP COLUMN IF EXISTS state",
		"ALTER TABLE manual_topups ADD COLUMN IF NOT EXISTS idempotency_key TEXT",
		"UPDATE manual_topups SET idempotency_key = 'migrated:' || ctid::text WHERE idempotency_key IS NULL",
		"ALTER TABLE manual_topups ALTER COLUMN idempotency_key SET NOT NULL",
		"ALTER TABLE holds ADD COLUMN IF NOT EXISTS idempotency_key TEXT",
		"ALTER TABLE holds ADD COLUMN IF NOT EXISTS resource_type TEXT",
		"ALTER TABLE holds ALTER COLUMN request_hash SET NOT NULL",
		"ALTER TABLE holds ALTER COLUMN resource_type SET NOT NULL",
		"CREATE TABLE IF NOT EXISTS hold_releases",
		"ALTER TABLE resource_settlements ADD COLUMN IF NOT EXISTS pricing_version TEXT",
		"ALTER TABLE resource_settlements ADD COLUMN IF NOT EXISTS price_snapshot_json JSONB",
		"ALTER TABLE resource_settlements ADD COLUMN IF NOT EXISTS provider_cost_evidence_ref TEXT",
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
