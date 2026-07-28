DROP INDEX IF EXISTS fabric_operations_compute_pool_head_idx;

CREATE INDEX fabric_operations_compute_pool_head_idx
ON fabric_operations(compute_pool_key, created_at, id)
WHERE action = 'create_compute_allocation' AND status IN ('started', 'claim_pending') AND compute_pool_key <> '';
