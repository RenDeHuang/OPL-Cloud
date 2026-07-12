ALTER TABLE holds ADD COLUMN IF NOT EXISTS activation_amount_cents BIGINT NOT NULL DEFAULT 0;
ALTER TABLE holds ADD COLUMN IF NOT EXISTS original_cents BIGINT NOT NULL DEFAULT 0;
ALTER TABLE holds ADD COLUMN IF NOT EXISTS remaining_cents BIGINT NOT NULL DEFAULT 0;
ALTER TABLE holds ADD COLUMN IF NOT EXISTS consumed_cents BIGINT NOT NULL DEFAULT 0;
ALTER TABLE holds ADD COLUMN IF NOT EXISTS released_cents BIGINT NOT NULL DEFAULT 0;
ALTER TABLE holds ADD COLUMN IF NOT EXISTS provider_evidence_ref TEXT NOT NULL DEFAULT '';
UPDATE holds SET original_cents = amount_cents WHERE original_cents = 0;
UPDATE holds SET remaining_cents = amount_cents WHERE remaining_cents = 0 AND consumed_cents = 0 AND released_cents = 0;

ALTER TABLE resource_settlements ADD COLUMN IF NOT EXISTS hold_id TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS hold_activations (
    id TEXT PRIMARY KEY,
    account_id TEXT NOT NULL,
    workspace_id TEXT NOT NULL DEFAULT '',
    resource_type TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    hold_id TEXT NOT NULL,
    amount_cents BIGINT NOT NULL,
    currency TEXT NOT NULL DEFAULT 'CNY',
    status TEXT NOT NULL,
    provider_evidence_ref TEXT NOT NULL,
    ledger_entry_id TEXT NOT NULL,
    wallet_transaction_id TEXT NOT NULL,
    idempotency_key TEXT NOT NULL UNIQUE,
    request_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS hold_activations_hold_idx ON hold_activations(hold_id);
CREATE INDEX IF NOT EXISTS resource_settlements_hold_idx ON resource_settlements(hold_id);
