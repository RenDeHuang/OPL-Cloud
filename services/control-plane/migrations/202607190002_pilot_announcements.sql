CREATE TABLE IF NOT EXISTS control_plane_announcements (
    id TEXT PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    title TEXT NOT NULL,
    body TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'draft',
    starts_at TEXT NOT NULL DEFAULT '',
    ends_at TEXT NOT NULL DEFAULT '',
    published_at TEXT NOT NULL DEFAULT '',
    created_by_user_id TEXT NOT NULL,
    updated_by_user_id TEXT NOT NULL,
    CONSTRAINT control_plane_announcements_status_check
        CHECK (status IN ('draft', 'scheduled', 'published', 'withdrawn')),
    CONSTRAINT control_plane_announcements_starts_at_check
        CHECK (starts_at = '' OR starts_at::timestamptz IS NOT NULL),
    CONSTRAINT control_plane_announcements_ends_at_check
        CHECK (ends_at = '' OR ends_at::timestamptz IS NOT NULL),
    CONSTRAINT control_plane_announcements_schedule_check
        CHECK (ends_at = '' OR starts_at = '' OR ends_at::timestamptz > starts_at::timestamptz)
);

CREATE TABLE IF NOT EXISTS control_plane_announcement_reads (
    id TEXT PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    announcement_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    read_at TEXT NOT NULL,
    CONSTRAINT control_plane_announcement_reads_announcement_fk
        FOREIGN KEY (announcement_id) REFERENCES control_plane_announcements(id),
    CONSTRAINT control_plane_announcement_reads_user_unique UNIQUE (announcement_id, user_id)
);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'control_plane_announcements_status_check'
          AND conrelid = 'control_plane_announcements'::regclass
    ) THEN
        ALTER TABLE control_plane_announcements
            ADD CONSTRAINT control_plane_announcements_status_check
            CHECK (status IN ('draft', 'scheduled', 'published', 'withdrawn'));
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'control_plane_announcements_starts_at_check'
          AND conrelid = 'control_plane_announcements'::regclass
    ) THEN
        ALTER TABLE control_plane_announcements
            ADD CONSTRAINT control_plane_announcements_starts_at_check
            CHECK (starts_at = '' OR starts_at::timestamptz IS NOT NULL);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'control_plane_announcements_ends_at_check'
          AND conrelid = 'control_plane_announcements'::regclass
    ) THEN
        ALTER TABLE control_plane_announcements
            ADD CONSTRAINT control_plane_announcements_ends_at_check
            CHECK (ends_at = '' OR ends_at::timestamptz IS NOT NULL);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'control_plane_announcements_schedule_check'
          AND conrelid = 'control_plane_announcements'::regclass
    ) THEN
        ALTER TABLE control_plane_announcements
            ADD CONSTRAINT control_plane_announcements_schedule_check
            CHECK (ends_at = '' OR starts_at = '' OR ends_at::timestamptz > starts_at::timestamptz);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'control_plane_announcement_reads_announcement_fk'
          AND conrelid = 'control_plane_announcement_reads'::regclass
    ) THEN
        ALTER TABLE control_plane_announcement_reads
            ADD CONSTRAINT control_plane_announcement_reads_announcement_fk
            FOREIGN KEY (announcement_id) REFERENCES control_plane_announcements(id);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'control_plane_announcement_reads_user_unique'
          AND conrelid = 'control_plane_announcement_reads'::regclass
    ) THEN
        ALTER TABLE control_plane_announcement_reads
            ADD CONSTRAINT control_plane_announcement_reads_user_unique UNIQUE (announcement_id, user_id);
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS control_plane_announcements_active_idx
    ON control_plane_announcements (status, starts_at, ends_at);
