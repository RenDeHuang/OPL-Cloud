ALTER TABLE control_plane_workspaces
  ADD COLUMN IF NOT EXISTS verification_slot_id TEXT NOT NULL DEFAULT '';

ALTER TABLE control_plane_workspaces
  ADD COLUMN IF NOT EXISTS customer_product BOOLEAN NOT NULL DEFAULT TRUE;

DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM control_plane_workspaces
    WHERE COALESCE(NULLIF(account_id, ''), owner_account_id) <> ''
    GROUP BY COALESCE(NULLIF(account_id, ''), owner_account_id)
    HAVING count(*) > 1
  ) THEN
    RAISE EXCEPTION 'duplicate primary Workspaces';
  END IF;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS control_plane_workspaces_primary_account_key
  ON control_plane_workspaces ((COALESCE(NULLIF(account_id, ''), owner_account_id)))
  WHERE COALESCE(NULLIF(account_id, ''), owner_account_id) <> '';
