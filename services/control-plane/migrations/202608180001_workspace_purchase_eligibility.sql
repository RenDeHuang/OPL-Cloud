ALTER TABLE control_plane_accounts
  ADD COLUMN IF NOT EXISTS workspace_purchase_enabled BOOLEAN NOT NULL DEFAULT FALSE;
