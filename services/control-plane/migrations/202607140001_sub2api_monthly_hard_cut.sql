DROP TABLE IF EXISTS control_plane_wallet_projections;
DROP TABLE IF EXISTS control_plane_wallet_transaction_projections;
DROP TABLE IF EXISTS control_plane_manual_topup_projections;
DROP TABLE IF EXISTS control_plane_ledger_projections;
DROP TABLE IF EXISTS control_plane_pricing_items;
DROP TABLE IF EXISTS control_plane_pricing_catalogs;

ALTER TABLE IF EXISTS control_plane_accounts
  ADD COLUMN IF NOT EXISTS sub2api_user_id BIGINT NOT NULL DEFAULT 0;

ALTER TABLE IF EXISTS control_plane_compute_allocations
  DROP COLUMN IF EXISTS hold_id,
  DROP COLUMN IF EXISTS hold_release_id,
  DROP COLUMN IF EXISTS settlement_id,
  DROP COLUMN IF EXISTS ledger_entry_id,
  DROP COLUMN IF EXISTS wallet_transaction_id,
  DROP COLUMN IF EXISTS usage_period_end,
  DROP COLUMN IF EXISTS hold_amount_cents,
  DROP COLUMN IF EXISTS hold_amount,
  DROP COLUMN IF EXISTS price_snapshot_package_id,
  DROP COLUMN IF EXISTS price_snapshot_resource_type,
  DROP COLUMN IF EXISTS price_snapshot_currency,
  DROP COLUMN IF EXISTS price_snapshot_source,
  DROP COLUMN IF EXISTS price_snapshot_sku,
  DROP COLUMN IF EXISTS price_snapshot_unit_price_cents,
  DROP COLUMN IF EXISTS price_snapshot_compute_hourly,
  ADD COLUMN IF NOT EXISTS billing_operation_id TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS billing_state_json TEXT NOT NULL DEFAULT '{}';

ALTER TABLE IF EXISTS control_plane_storage_volumes
  DROP COLUMN IF EXISTS hold_id,
  DROP COLUMN IF EXISTS hold_release_id,
  DROP COLUMN IF EXISTS settlement_id,
  DROP COLUMN IF EXISTS ledger_entry_id,
  DROP COLUMN IF EXISTS wallet_transaction_id,
  DROP COLUMN IF EXISTS usage_period_end,
  DROP COLUMN IF EXISTS hold_amount_cents,
  DROP COLUMN IF EXISTS hold_amount,
  DROP COLUMN IF EXISTS price_snapshot_resource_type,
  DROP COLUMN IF EXISTS price_snapshot_currency,
  DROP COLUMN IF EXISTS price_snapshot_source,
  DROP COLUMN IF EXISTS price_snapshot_unit_price_cents,
  DROP COLUMN IF EXISTS price_snapshot_storage_gb_month,
  DROP COLUMN IF EXISTS price_snapshot_size_gb,
  ADD COLUMN IF NOT EXISTS billing_operation_id TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS billing_state_json TEXT NOT NULL DEFAULT '{}';

ALTER TABLE IF EXISTS control_plane_workspaces
  DROP COLUMN IF EXISTS access_password;
