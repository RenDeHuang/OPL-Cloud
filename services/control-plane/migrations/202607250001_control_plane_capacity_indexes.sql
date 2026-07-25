ALTER TABLE control_plane_runtime_operations
  ADD COLUMN IF NOT EXISTS period_start TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS control_plane_sessions_user_id_key
  ON control_plane_sessions (user_id, id);

CREATE INDEX IF NOT EXISTS control_plane_runtime_operations_action_status_key
  ON control_plane_runtime_operations (action, status, created_at, id);

CREATE INDEX IF NOT EXISTS control_plane_runtime_operations_account_action_status_key
  ON control_plane_runtime_operations (account_id, action, status, created_at, id);

CREATE INDEX IF NOT EXISTS control_plane_runtime_operations_workspace_action_status_period_key
  ON control_plane_runtime_operations (workspace_id, action, status, period_start, created_at, id);

CREATE INDEX IF NOT EXISTS control_plane_workspaces_renewal_candidates_key
  ON control_plane_workspaces (customer_product, id);
