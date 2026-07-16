DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM control_plane_accounts
    WHERE sub2api_user_id > 0
    GROUP BY sub2api_user_id
    HAVING COUNT(*) > 1
  ) THEN
    RAISE EXCEPTION 'duplicate sub2api_user_id mappings';
  END IF;
END
$$;

CREATE UNIQUE INDEX IF NOT EXISTS control_plane_accounts_sub2api_user_id_unique
  ON control_plane_accounts (sub2api_user_id)
  WHERE sub2api_user_id > 0;
