package postgresmigrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// One database-wide key keeps the three services from migrating in parallel.
const advisoryLockKey int64 = 0x4f504c4d49475241

const createJournalSQL = `
CREATE TABLE opl_schema_migrations (
	service TEXT NOT NULL,
	version TEXT NOT NULL,
	applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
	PRIMARY KEY (service, version)
)`

type Migration struct {
	Version string
	Run     func(context.Context) error
}

func Apply(ctx context.Context, db *sql.DB, service string, migrations []Migration) error {
	if db == nil {
		return errors.New("migration database is required")
	}
	service = strings.TrimSpace(service)
	if service == "" {
		return errors.New("migration service is required")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("open migration connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", advisoryLockKey)
	}()

	var journalExists bool
	if err := conn.QueryRowContext(ctx, "SELECT to_regclass('opl_schema_migrations') IS NOT NULL").Scan(&journalExists); err != nil {
		return fmt.Errorf("inspect migration journal: %w", err)
	}
	if !journalExists {
		if _, err := conn.ExecContext(ctx, createJournalSQL); err != nil {
			return fmt.Errorf("create migration journal: %w", err)
		}
	}

	for _, migration := range migrations {
		version := strings.TrimSpace(migration.Version)
		if version == "" || migration.Run == nil {
			return errors.New("migration version and runner are required")
		}
		var applied bool
		if err := conn.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM opl_schema_migrations WHERE service = $1 AND version = $2
		)`, service, version).Scan(&applied); err != nil {
			return fmt.Errorf("inspect migration %s/%s: %w", service, version, err)
		}
		if applied {
			continue
		}
		if err := migration.Run(ctx); err != nil {
			return fmt.Errorf("apply migration %s/%s: %w", service, version, err)
		}
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO opl_schema_migrations (service, version) VALUES ($1, $2)
		`, service, version); err != nil {
			return fmt.Errorf("record migration %s/%s: %w", service, version, err)
		}
	}
	return nil
}
