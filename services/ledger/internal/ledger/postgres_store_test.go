package ledger

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lib/pq"
)

func TestPostgresSchemaKeepsEvidenceAndDropsRetiredCommercialTables(t *testing.T) {
	schema := PostgresSchemaSQL()
	for _, marker := range []string{
		"CREATE TABLE IF NOT EXISTS evidence_receipts",
		"account_id TEXT NOT NULL DEFAULT ''",
		"CREATE INDEX IF NOT EXISTS evidence_receipts_account_created",
		"CREATE TABLE IF NOT EXISTS review_policies",
		"CREATE TABLE IF NOT EXISTS reconciliation_reports",
		"CREATE TABLE IF NOT EXISTS idempotency_keys",
		"DROP TABLE IF EXISTS hold_activations",
		"DROP TABLE IF EXISTS resource_settlements",
		"DROP TABLE IF EXISTS wallets",
	} {
		if !strings.Contains(schema, marker) {
			t.Fatalf("schema missing %q", marker)
		}
	}
	for _, retiredCreate := range []string{"wallets", "ledger_entries", "wallet_transactions", "manual_topups", "holds", "hold_activations", "hold_releases", "resource_settlements"} {
		if strings.Contains(schema, "CREATE TABLE IF NOT EXISTS "+retiredCreate) {
			t.Fatalf("schema recreates retired table %q", retiredCreate)
		}
	}
	if strings.Contains(schema, "DROP TABLE IF EXISTS idempotency_keys") {
		t.Fatal("schema drops receipt mutation idempotency table")
	}
}

func TestFormalAndEmbeddedMigrationTreesMatch(t *testing.T) {
	formal, err := os.ReadDir("../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := os.ReadDir("ent_migrations")
	if err != nil {
		t.Fatal(err)
	}
	if len(formal) != len(embedded) {
		t.Fatalf("migration file count differs: formal=%d embedded=%d", len(formal), len(embedded))
	}
	for i := range formal {
		if formal[i].Name() != embedded[i].Name() {
			t.Fatalf("migration names differ: %q != %q", formal[i].Name(), embedded[i].Name())
		}
		formalSQL, err := os.ReadFile(filepath.Join("../../migrations", formal[i].Name()))
		if err != nil {
			t.Fatal(err)
		}
		embeddedSQL, err := os.ReadFile(filepath.Join("ent_migrations", embedded[i].Name()))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(formalSQL, embeddedSQL) {
			t.Fatalf("migration %s differs", formal[i].Name())
		}
	}
}

func TestPostgresStoreRunsEmbeddedMigrationsOnce(t *testing.T) {
	db := openLedgerTestPostgres(t)
	first := NewPostgresStore(db)
	if err := first.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	var migrationCount int
	if err := db.QueryRow(`SELECT count(*) FROM opl_schema_migrations WHERE service = 'ledger'`).Scan(&migrationCount); err != nil {
		t.Fatalf("read Ledger migration journal: %v", err)
	}
	if migrationCount != 1 {
		t.Fatalf("Ledger migration count = %d, want 1", migrationCount)
	}
	if _, err := db.Exec(`DROP TABLE evidence_receipts`); err != nil {
		t.Fatal(err)
	}
	second := NewPostgresStore(db)
	if err := second.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	var table sql.NullString
	if err := db.QueryRow(`SELECT to_regclass('evidence_receipts')`).Scan(&table); err != nil {
		t.Fatal(err)
	}
	if table.Valid {
		t.Fatal("second Ledger startup repeated embedded DDL")
	}
}

func TestPostgresStoreImplementsLedgerStore(t *testing.T) {
	var db *sql.DB
	var _ Store = NewPostgresStore(db)
}

func TestPostgresConcurrentReceiptIdempotency(t *testing.T) {
	db := openLedgerTestPostgres(t)
	store := NewPostgresStore(db)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := store.Install(ctx); err != nil {
		t.Fatalf("install ledger schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE FUNCTION await_receipt_barrier() RETURNS trigger LANGUAGE plpgsql AS $$
		DECLARE
			phase INTEGER;
		BEGIN
			phase := CASE NEW.account_id
				WHEN 'acct-concurrent-same-receipt' THEN 1
				WHEN 'acct-concurrent-payload-receipt' THEN 2
			END;
			IF phase IS NULL THEN
				RETURN NEW;
			END IF;
			PERFORM pg_advisory_xact_lock_shared(741101, phase);
			PERFORM pg_advisory_xact_lock_shared(741102, phase);
			RETURN NEW;
		END;
		$$;
		CREATE TRIGGER await_receipt_insert BEFORE INSERT ON evidence_receipts FOR EACH ROW EXECUTE FUNCTION await_receipt_barrier();
	`); err != nil {
		t.Fatalf("install receipt race trigger: %v", err)
	}
	type outcome struct {
		receipt Receipt
		err     error
	}
	run := func(phase int, inputs []ReceiptInput) []outcome {
		barrier, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer barrier.Close()
		if _, err := barrier.ExecContext(ctx, "SELECT pg_advisory_lock(741102, $1)", phase); err != nil {
			t.Fatalf("lock receipt barrier: %v", err)
		}
		gateHeld := true
		defer func() {
			if gateHeld {
				_, _ = barrier.ExecContext(context.Background(), "SELECT pg_advisory_unlock(741102, $1)", phase)
			}
		}()
		start := make(chan struct{})
		outcomes := make(chan outcome, len(inputs))
		var wg sync.WaitGroup
		for _, input := range inputs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				receipt, err := store.RecordReceipt(ctx, input)
				outcomes <- outcome{receipt: receipt, err: err}
			}()
		}
		close(start)
		deadline := time.Now().Add(5 * time.Second)
		for {
			var arrivals int
			if err := barrier.QueryRowContext(ctx, "SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND mode = 'ShareLock' AND granted AND classid = 741101::oid AND objid = $1::oid", phase).Scan(&arrivals); err != nil {
				t.Fatalf("observe receipt barrier: %v", err)
			}
			if arrivals == len(inputs) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("receipt operations did not overlap: arrivals=%d want=%d", arrivals, len(inputs))
			}
			time.Sleep(5 * time.Millisecond)
		}
		var unlocked bool
		if err := barrier.QueryRowContext(ctx, "SELECT pg_advisory_unlock(741102, $1)", phase).Scan(&unlocked); err != nil || !unlocked {
			t.Fatalf("unlock receipt barrier: unlocked=%v err=%v", unlocked, err)
		}
		gateHeld = false
		wg.Wait()
		close(outcomes)
		results := make([]outcome, 0, len(inputs))
		for result := range outcomes {
			results = append(results, result)
		}
		return results
	}

	same := ReceiptInput{Type: "billing.resource_purchased.v1", Status: "completed", Surface: "control_plane", AccountID: "acct-concurrent-same-receipt", WorkspaceID: "workspace-concurrent-receipt", RequestID: "billing-operation-concurrent", Cost: map[string]any{"chargeUsdMicros": int64(50_000_000)}, IdempotencyKey: "receipt-concurrent-same"}
	results := run(1, []ReceiptInput{same, same, same, same})
	created, replayed := 0, 0
	receiptIDs := map[string]struct{}{}
	for _, result := range results {
		if result.err != nil {
			t.Fatalf("same receipt concurrent create: %v", result.err)
		}
		receiptIDs[result.receipt.ReceiptID] = struct{}{}
		if result.receipt.Replayed {
			replayed++
		} else {
			created++
		}
	}
	if created != 1 || replayed != 3 || len(receiptIDs) != 1 {
		t.Fatalf("same receipt outcomes = %#v", results)
	}

	different := same
	different.AccountID = "acct-concurrent-payload-receipt"
	different.IdempotencyKey = "receipt-concurrent-payload"
	changed := different
	changed.Cost = map[string]any{"chargeUsdMicros": int64(49_999_999)}
	results = run(2, []ReceiptInput{different, changed, different, changed})
	created, replayed, conflicts := 0, 0, 0
	for _, result := range results {
		switch {
		case errors.Is(result.err, ErrIdempotencyConflict):
			conflicts++
		case result.err != nil:
			t.Fatalf("different receipt payload raw error: %v", result.err)
		case result.receipt.Replayed:
			replayed++
		default:
			created++
		}
	}
	if created != 1 || replayed != 1 || conflicts != 2 {
		t.Fatalf("different receipt payload outcomes = %#v", results)
	}
}

func TestPostgresConcurrentReviewPolicyIdempotency(t *testing.T) {
	db := openLedgerTestPostgres(t)
	store := NewPostgresStore(db)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := store.Install(ctx); err != nil {
		t.Fatalf("install ledger schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE FUNCTION await_review_policy_barrier() RETURNS trigger LANGUAGE plpgsql AS $$
		DECLARE
			phase INTEGER;
		BEGIN
			phase := CASE NEW.organization_id
				WHEN 'org-concurrent-same-key' THEN 1
				WHEN 'org-concurrent-payload' THEN 2
				WHEN 'org-concurrent-scope' THEN 3
			END;
			IF phase IS NULL THEN
				RETURN NEW;
			END IF;
			PERFORM pg_advisory_xact_lock_shared(741001, phase);
			PERFORM pg_advisory_xact_lock_shared(741002, phase);
			RETURN NEW;
		END;
		$$;
		CREATE TRIGGER await_review_policy_insert BEFORE INSERT ON review_policies FOR EACH ROW EXECUTE FUNCTION await_review_policy_barrier();
	`); err != nil {
		t.Fatalf("install review policy race trigger: %v", err)
	}
	type outcome struct {
		policy ReviewPolicy
		err    error
	}
	run := func(phase int, inputs []ReviewPolicyInput) []outcome {
		barrier, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer barrier.Close()
		if _, err := barrier.ExecContext(ctx, "SELECT pg_advisory_lock(741002, $1)", phase); err != nil {
			t.Fatalf("lock review policy barrier: %v", err)
		}
		gateHeld := true
		defer func() {
			if gateHeld {
				_, _ = barrier.ExecContext(context.Background(), "SELECT pg_advisory_unlock(741002, $1)", phase)
			}
		}()
		start := make(chan struct{})
		outcomes := make(chan outcome, len(inputs))
		var wg sync.WaitGroup
		for _, input := range inputs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				policy, err := store.CreateReviewPolicy(ctx, input)
				outcomes <- outcome{policy: policy, err: err}
			}()
		}
		close(start)
		deadline := time.Now().Add(5 * time.Second)
		for {
			var arrivals int
			if err := barrier.QueryRowContext(ctx, "SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND mode = 'ShareLock' AND granted AND classid = 741001::oid AND objid = $1::oid", phase).Scan(&arrivals); err != nil {
				t.Fatalf("observe review policy barrier: %v", err)
			}
			if arrivals == len(inputs) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("review policy operations did not overlap: arrivals=%d want=%d", arrivals, len(inputs))
			}
			time.Sleep(5 * time.Millisecond)
		}
		var unlocked bool
		if err := barrier.QueryRowContext(ctx, "SELECT pg_advisory_unlock(741002, $1)", phase).Scan(&unlocked); err != nil || !unlocked {
			t.Fatalf("unlock review policy barrier: unlocked=%v err=%v", unlocked, err)
		}
		gateHeld = false
		wg.Wait()
		close(outcomes)
		results := make([]outcome, 0, len(inputs))
		for result := range outcomes {
			results = append(results, result)
		}
		return results
	}
	input := ReviewPolicyInput{
		ExecutionIdentity: ExecutionIdentity{OrganizationID: "org-concurrent-same-key", WorkspaceID: "workspace-concurrent", ProjectID: "project-concurrent", TaskID: "task-concurrent", JobID: "job-concurrent"},
		Version:           "1", RequiredReviewers: []RequiredReviewer{{ReviewerRef: "reviewer-rca", ReviewerVersion: "1.0.0"}}, IdempotencyKey: "policy-concurrent",
	}
	results := run(1, []ReviewPolicyInput{input, input, input, input})
	created, replayed := 0, 0
	for _, result := range results {
		if result.err != nil {
			t.Fatalf("same-key concurrent create: %v", result.err)
		}
		if result.policy.Replayed {
			replayed++
		} else {
			created++
		}
	}
	if created != 1 || replayed != 3 {
		t.Fatalf("same-key outcomes = %#v", results)
	}

	differentPayload := input
	differentPayload.ExecutionIdentity.OrganizationID = "org-concurrent-payload"
	differentPayload.ExecutionIdentity.JobID = "job-different-payload"
	differentPayload.IdempotencyKey = "policy-different-payload"
	versionTwo := differentPayload
	versionTwo.Version = "2"
	results = run(2, []ReviewPolicyInput{differentPayload, versionTwo, differentPayload, versionTwo})
	conflicts := 0
	for _, result := range results {
		if errors.Is(result.err, ErrIdempotencyConflict) {
			conflicts++
		} else if result.err != nil {
			t.Fatalf("different-payload raw error: %v", result.err)
		}
	}
	if conflicts != 2 {
		t.Fatalf("different-payload outcomes = %#v", results)
	}

	differentKey := input
	differentKey.ExecutionIdentity.OrganizationID = "org-concurrent-scope"
	differentKey.ExecutionIdentity.JobID = "job-different-key"
	inputs := make([]ReviewPolicyInput, 4)
	for i := range inputs {
		inputs[i] = differentKey
		inputs[i].IdempotencyKey = fmt.Sprintf("policy-different-key-%d", i)
	}
	results = run(3, inputs)
	created, conflicts = 0, 0
	for _, result := range results {
		switch {
		case result.err == nil:
			created++
		case errors.Is(result.err, ErrInvalidReviewPolicyInput):
			conflicts++
		default:
			t.Fatalf("different-key raw error: %v", result.err)
		}
	}
	if created != 1 || conflicts != 3 {
		t.Fatalf("different-key outcomes = %#v", results)
	}
}

func openLedgerTestPostgres(t *testing.T) *sql.DB {
	t.Helper()
	rawURL := os.Getenv("LEDGER_TEST_DATABASE_URL")
	if rawURL == "" && os.Getenv("OPL_POSTGRES_TESTS") == "1" {
		rawURL = "connect_timeout=10"
	}
	if rawURL == "" {
		t.Skip("LEDGER_TEST_DATABASE_URL is not set")
	}
	admin, err := sql.Open("postgres", rawURL)
	if err != nil {
		t.Fatalf("open test postgres: %v", err)
	}
	connectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := admin.PingContext(connectCtx); err != nil {
		_ = admin.Close()
		t.Fatalf("connect test postgres: %v", err)
	}
	schema := fmt.Sprintf("ledger_test_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(connectCtx, "CREATE SCHEMA "+pq.QuoteIdentifier(schema)); err != nil {
		_ = admin.Close()
		t.Fatalf("create test schema: %v", err)
	}
	testURL := rawURL
	if parsed, err := url.Parse(rawURL); err == nil && parsed.Scheme != "" {
		query := parsed.Query()
		query.Set("search_path", schema)
		query.Set("connect_timeout", "10")
		parsed.RawQuery = query.Encode()
		testURL = parsed.String()
	} else {
		testURL += " search_path=" + pq.QuoteLiteral(schema) + " connect_timeout=10"
	}
	db, err := sql.Open("postgres", testURL)
	if err != nil {
		_, _ = admin.Exec("DROP SCHEMA " + pq.QuoteIdentifier(schema) + " CASCADE")
		_ = admin.Close()
		t.Fatalf("open isolated test schema: %v", err)
	}
	db.SetMaxOpenConns(5)
	t.Cleanup(func() {
		_ = db.Close()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = admin.ExecContext(cleanupCtx, "DROP SCHEMA "+pq.QuoteIdentifier(schema)+" CASCADE")
		_ = admin.Close()
	})
	return db
}
