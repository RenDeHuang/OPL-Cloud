DROP INDEX IF EXISTS control_plane_workspaces_primary_account_key;

CREATE INDEX IF NOT EXISTS control_plane_workspaces_account_page_key
  ON control_plane_workspaces (
    (COALESCE(NULLIF(account_id, ''), owner_account_id)),
    customer_product,
    id
  );
