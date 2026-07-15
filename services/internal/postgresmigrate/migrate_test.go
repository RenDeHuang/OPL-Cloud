package postgresmigrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lib/pq"
)

func TestApplyRunsMigrationOnlyOnce(t *testing.T) {
	db := openIsolatedPostgres(t)
	var calls atomic.Int32
	migration := Migration{
		Version: "001_create_probe",
		Run: func(ctx context.Context) error {
			calls.Add(1)
			_, err := db.ExecContext(ctx, `CREATE TABLE migration_probe (id integer PRIMARY KEY); INSERT INTO migration_probe VALUES (1)`)
			return err
		},
	}

	for range 2 {
		if err := Apply(context.Background(), db, "test-service", []Migration{migration}); err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("migration calls = %d, want 1", got)
	}
	assertMigrationCount(t, db, "test-service", "001_create_probe", 1)
}

func TestApplySerializesConcurrentStartup(t *testing.T) {
	databaseURL := isolatedPostgresURL(t)
	first := openPostgres(t, databaseURL)
	second := openPostgres(t, databaseURL)
	var calls atomic.Int32
	start := make(chan struct{})
	run := func(db *sql.DB) error {
		<-start
		return Apply(context.Background(), db, "concurrent-service", []Migration{{
			Version: "001_concurrent",
			Run: func(ctx context.Context) error {
				calls.Add(1)
				time.Sleep(100 * time.Millisecond)
				_, err := db.ExecContext(ctx, `CREATE TABLE concurrent_probe (id integer PRIMARY KEY)`)
				return err
			},
		}})
	}

	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for _, db := range []*sql.DB{first, second} {
		wait.Add(1)
		go func(db *sql.DB) {
			defer wait.Done()
			errs <- run(db)
		}(db)
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("concurrent migration calls = %d, want 1", got)
	}
	assertMigrationCount(t, first, "concurrent-service", "001_concurrent", 1)
}

func TestApplyDoesNotRecordFailedMigration(t *testing.T) {
	db := openIsolatedPostgres(t)
	wantErr := errors.New("migration failed")
	err := Apply(context.Background(), db, "failure-service", []Migration{{
		Version: "001_failure",
		Run: func(context.Context) error {
			return wantErr
		},
	}})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Apply error = %v, want %v", err, wantErr)
	}
	assertMigrationCount(t, db, "failure-service", "001_failure", 0)

	if err := Apply(context.Background(), db, "failure-service", []Migration{{
		Version: "001_failure",
		Run: func(context.Context) error {
			return nil
		},
	}}); err != nil {
		t.Fatalf("retry failed migration: %v", err)
	}
	assertMigrationCount(t, db, "failure-service", "001_failure", 1)
}

func assertMigrationCount(t *testing.T, db *sql.DB, service, version string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`SELECT count(*) FROM opl_schema_migrations WHERE service = $1 AND version = $2`, service, version).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("migration count = %d, want %d", got, want)
	}
}

func openIsolatedPostgres(t *testing.T) *sql.DB {
	t.Helper()
	return openPostgres(t, isolatedPostgresURL(t))
}

func openPostgres(t *testing.T, databaseURL string) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func isolatedPostgresURL(t *testing.T) string {
	t.Helper()
	databaseURL := os.Getenv("POSTGRES_MIGRATION_TEST_DATABASE_URL")
	optional := false
	if databaseURL == "" {
		if os.Getenv("OPL_POSTGRES_TESTS") == "1" {
			databaseURL = "connect_timeout=10"
		} else {
			databaseURL = "host=/var/run/postgresql dbname=postgres sslmode=disable"
			optional = true
		}
	}
	admin, err := sql.Open("postgres", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := admin.Ping(); err != nil {
		_ = admin.Close()
		if optional {
			t.Skipf("local PostgreSQL unavailable: %v", err)
		}
		t.Fatal(err)
	}
	schema := fmt.Sprintf("opl_migration_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + pq.QuoteIdentifier(schema)); err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + pq.QuoteIdentifier(schema) + ` CASCADE`)
		_ = admin.Close()
	})
	if parsed, err := url.Parse(databaseURL); err == nil && parsed.Scheme != "" {
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return databaseURL + " search_path=" + pq.QuoteLiteral(schema)
}
