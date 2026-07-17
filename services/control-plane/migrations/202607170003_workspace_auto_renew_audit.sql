ALTER TABLE IF EXISTS control_plane_admin_audit_events
    ADD COLUMN IF NOT EXISTS before_json TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS after_json TEXT NOT NULL DEFAULT '';

ALTER TABLE IF EXISTS control_plane_archived_admin_audit_events
    ADD COLUMN IF NOT EXISTS before_json TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS after_json TEXT NOT NULL DEFAULT '';
