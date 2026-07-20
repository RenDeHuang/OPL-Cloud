ALTER TABLE control_plane_workspaces
  ADD COLUMN IF NOT EXISTS workspace_api_key_id BIGINT;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conname = 'control_plane_workspaces_workspace_api_key_id_positive'
      AND conrelid = 'control_plane_workspaces'::regclass
  ) THEN
    ALTER TABLE control_plane_workspaces
      ADD CONSTRAINT control_plane_workspaces_workspace_api_key_id_positive
      CHECK (workspace_api_key_id IS NULL OR workspace_api_key_id > 0);
  END IF;
END $$;
