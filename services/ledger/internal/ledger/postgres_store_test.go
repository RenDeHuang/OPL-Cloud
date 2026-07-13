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
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lib/pq"
)

func TestPostgresSchemaUsesEntMigrationLedgerTables(t *testing.T) {
	schema := PostgresSchemaSQL()
	required := []string{
		"CREATE TABLE IF NOT EXISTS wallets",
		"ALTER TABLE wallets ADD COLUMN IF NOT EXISTS available_cents",
		"CREATE TABLE IF NOT EXISTS ledger_entries",
		"CREATE TABLE IF NOT EXISTS wallet_transactions",
		"CREATE TABLE IF NOT EXISTS manual_topups",
		"CREATE TABLE IF NOT EXISTS holds",
		"CREATE TABLE IF NOT EXISTS hold_releases",
		"CREATE TABLE IF NOT EXISTS evidence_receipts",
		"ALTER TABLE evidence_receipts ALTER COLUMN provider_request_id SET DEFAULT ''",
		"ALTER TABLE evidence_receipts ALTER COLUMN redacted_url SET DEFAULT ''",
		"ALTER TABLE evidence_receipts ALTER COLUMN token_version SET DEFAULT ''",
		"organization_id TEXT NOT NULL DEFAULT ''",
		"project_id TEXT NOT NULL DEFAULT ''",
		"task_id TEXT NOT NULL DEFAULT ''",
		"job_id TEXT NOT NULL DEFAULT ''",
		"CREATE INDEX IF NOT EXISTS evidence_receipts_organization_created",
		"CREATE INDEX IF NOT EXISTS evidence_receipts_workspace_created",
		"CREATE TABLE IF NOT EXISTS resource_settlements",
		"price_snapshot_json TEXT NOT NULL DEFAULT '{}'",
		"CREATE TABLE IF NOT EXISTS reconciliation_reports",
		"report_json TEXT NOT NULL DEFAULT '{}'",
		"CREATE TABLE IF NOT EXISTS idempotency_keys",
		"CREATE TABLE IF NOT EXISTS review_policies",
		"required_reviewers_json TEXT NOT NULL",
		"CREATE UNIQUE INDEX IF NOT EXISTS review_policies_active_scope",
	}
	for _, marker := range required {
		if !strings.Contains(schema, marker) {
			t.Fatalf("schema missing %q", marker)
		}
	}
	if strings.Contains(schema, "JSONB") {
		t.Fatalf("ledger schema must not keep JSONB fact columns")
	}
	generatedValidators := []string{
		`validator failed for field "Hold.workspace_id"`,
		`validator failed for field "HoldRelease.workspace_id"`,
		`validator failed for field "ResourceSettlement.workspace_id"`,
	}
	for _, marker := range generatedValidators {
		if strings.Contains(readGeneratedLedgerEnt(t), marker) {
			t.Fatalf("ledger resource facts must allow account/resource scoped rows before workspace exists: found %q", marker)
		}
	}
}

func TestFormalMigrationsDeclareReceiptColumnsBeforeQueryBackfill(t *testing.T) {
	entries, err := os.ReadDir("../../migrations")
	if err != nil {
		t.Fatalf("read formal migrations: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	var migrations strings.Builder
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join("../../migrations", name))
		if err != nil {
			t.Fatalf("read formal migration %s: %v", name, err)
		}
		migrations.Write(data)
		migrations.WriteByte('\n')
	}
	sql := migrations.String()
	backfill := strings.Index(sql, "UPDATE evidence_receipts")
	if backfill < 0 {
		t.Fatal("formal migrations missing receipt identity backfill")
	}
	for _, column := range []string{"receipt_type", "status", "payload_json", "supersedes_receipt_id", "organization_id", "project_id", "task_id", "job_id"} {
		declaration := strings.Index(sql, "ALTER TABLE evidence_receipts ADD COLUMN IF NOT EXISTS "+column)
		if declaration < 0 || declaration > backfill {
			t.Fatalf("formal migrations must declare %s before receipt backfill", column)
		}
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

func TestPostgresStoreImplementsLedgerStore(t *testing.T) {
	var db *sql.DB
	var _ Store = NewPostgresStore(db)
}

func TestPostgresHoldReturnsExactLifecycleTruth(t *testing.T) {
	db := openLedgerTestPostgres(t)
	store := NewPostgresStore(db)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := store.Install(ctx); err != nil {
		t.Fatal(err)
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	accountID := "acct-hold-truth-" + suffix
	workspaceID := "ws-hold-truth-" + suffix
	resourceID := "compute-hold-truth-" + suffix
	if _, err := store.ManualTopUp(ctx, ManualTopUpInput{AccountID: accountID, AmountCents: 3000, Currency: "CNY", OperatorUserID: "usr-admin", IdempotencyKey: suffix + "-topup"}); err != nil {
		t.Fatal(err)
	}
	hold, err := store.CreateHold(ctx, HoldInput{AccountID: accountID, WorkspaceID: workspaceID, ResourceType: "compute", ResourceID: resourceID, AmountCents: 2000, ActivationAmountCents: 100, Currency: "CNY", IdempotencyKey: suffix + "-create"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateHold(ctx, HoldActivationInput{AccountID: accountID, WorkspaceID: workspaceID, ResourceType: "compute", ResourceID: resourceID, HoldID: hold.ID, Currency: "CNY", ProviderEvidenceRef: "fabric:hold-truth", IdempotencyKey: suffix + "-activate"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReleaseHold(ctx, HoldReleaseInput{AccountID: accountID, WorkspaceID: workspaceID, ResourceType: "compute", ResourceID: resourceID, HoldID: hold.ID, Currency: "CNY", Reason: "destroy_compute", IdempotencyKey: suffix + "-release"}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Hold(ctx, hold.ID)
	if err != nil || got.AccountID != accountID || got.WorkspaceID != workspaceID || got.ResourceType != "compute" || got.ResourceID != resourceID ||
		got.Status != "released" || got.OriginalCents != 2000 || got.RemainingCents != 0 || got.ConsumedCents != 100 || got.ReleasedCents != 1900 {
		t.Fatalf("hold = %#v err=%v", got, err)
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

func readGeneratedLedgerEnt(t *testing.T) string {
	t.Helper()
	files := []string{
		"../../ent/hold_create.go",
		"../../ent/holdrelease_create.go",
		"../../ent/resourcesettlement_create.go",
	}
	var out strings.Builder
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		out.Write(data)
	}
	return out.String()
}
