ALTER TABLE fabric_operations
ADD COLUMN IF NOT EXISTS compute_pool_key TEXT NOT NULL DEFAULT '',
ADD COLUMN IF NOT EXISTS compute_pool_lease_owner TEXT NOT NULL DEFAULT '',
ADD COLUMN IF NOT EXISTS compute_pool_lease_expires_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS fabric_operations_compute_pool_head_idx
ON fabric_operations(compute_pool_key, created_at, id)
WHERE action = 'create_compute_allocation' AND status = 'started' AND compute_pool_key <> '';

CREATE UNIQUE INDEX IF NOT EXISTS fabric_operations_compute_claim_idx
ON fabric_operations(action, idempotency_key)
WHERE action = 'create_compute_allocation' AND idempotency_key <> '' AND id LIKE 'fop_compute_claim_%';
