package postgresmigrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
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

func ValidateTLS(databaseURL string) error {
	mode, host, err := postgresTLSSettings(strings.TrimSpace(databaseURL))
	if err != nil || mode != "verify-full" || !postgresTCPHost(host) {
		return errors.New("PostgreSQL DATABASE_URL must set sslmode=verify-full and an explicit TCP host")
	}
	return nil
}

func postgresTLSSettings(databaseURL string) (string, string, error) {
	if databaseURL == "" {
		return "", "", errors.New("empty PostgreSQL DSN")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		return "", "", err
	}
	if parsed.Scheme != "" {
		if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
			return "", "", errors.New("invalid PostgreSQL URL scheme")
		}
		query, err := url.ParseQuery(parsed.RawQuery)
		if err != nil || len(query["sslmode"]) != 1 {
			return "", "", errors.New("PostgreSQL URL requires one sslmode")
		}
		host := parsed.Hostname()
		if queryHosts, ok := query["host"]; ok {
			if len(queryHosts) != 1 {
				return "", "", errors.New("PostgreSQL URL allows one host setting")
			}
			host = queryHosts[0]
		}
		return query["sslmode"][0], host, nil
	}
	values, err := parseKeywordDSN(databaseURL)
	if err != nil || len(values["sslmode"]) != 1 {
		return "", "", errors.New("PostgreSQL DSN requires one sslmode")
	}
	if len(values["host"]) != 1 {
		return "", "", errors.New("PostgreSQL DSN requires one host setting")
	}
	return values["sslmode"][0], values["host"][0], nil
}

func postgresTCPHost(value string) bool {
	for _, host := range strings.Split(value, ",") {
		host = strings.TrimSpace(host)
		if host == "" || strings.ContainsAny(host, "/@") {
			return false
		}
	}
	return true
}

func parseKeywordDSN(databaseURL string) (map[string][]string, error) {
	values := map[string][]string{}
	for index := 0; index < len(databaseURL); {
		for index < len(databaseURL) && dsnSpace(databaseURL[index]) {
			index++
		}
		if index == len(databaseURL) {
			break
		}
		start := index
		for index < len(databaseURL) && dsnKeyByte(databaseURL[index]) {
			index++
		}
		if start == index {
			return nil, errors.New("invalid PostgreSQL DSN key")
		}
		key := databaseURL[start:index]
		for index < len(databaseURL) && dsnSpace(databaseURL[index]) {
			index++
		}
		if index == len(databaseURL) || databaseURL[index] != '=' {
			return nil, errors.New("invalid PostgreSQL DSN assignment")
		}
		index++
		for index < len(databaseURL) && dsnSpace(databaseURL[index]) {
			index++
		}
		var value strings.Builder
		if index < len(databaseURL) && databaseURL[index] == '\'' {
			index++
			closed := false
			for index < len(databaseURL) {
				if databaseURL[index] == '\\' {
					index++
					if index == len(databaseURL) {
						return nil, errors.New("invalid PostgreSQL DSN escape")
					}
					value.WriteByte(databaseURL[index])
					index++
					continue
				}
				if databaseURL[index] == '\'' {
					index++
					closed = true
					break
				}
				value.WriteByte(databaseURL[index])
				index++
			}
			if !closed || (index < len(databaseURL) && !dsnSpace(databaseURL[index])) {
				return nil, errors.New("invalid PostgreSQL DSN quoted value")
			}
		} else {
			for index < len(databaseURL) && !dsnSpace(databaseURL[index]) {
				if databaseURL[index] == '\\' {
					index++
					if index == len(databaseURL) {
						return nil, errors.New("invalid PostgreSQL DSN escape")
					}
				}
				value.WriteByte(databaseURL[index])
				index++
			}
		}
		values[key] = append(values[key], value.String())
	}
	return values, nil
}

func dsnSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n'
}

func dsnKeyByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || value == '_'
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
