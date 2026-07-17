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

	ledgerent "opl-cloud/services/ledger/ent"
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

func TestPostgresReceiptRowPreservesLargeInteger(t *testing.T) {
	receipt, err := receiptFromEnt(&ledgerent.EvidenceReceipt{
		ID: "receipt-large", ReceiptType: "billing.resource_purchased.v1", Status: "completed",
		PayloadJSON: `{"cost":{"monthlyPriceCnyCents":9007199254740993}}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(receipt.Cost["monthlyPriceCnyCents"]); got != "9007199254740993" {
		t.Fatalf("persisted receipt integer = %s", got)
	}
}

func TestPostgresReceiptRowRejectsTrailingJSON(t *testing.T) {
	if _, err := receiptFromEnt(&ledgerent.EvidenceReceipt{PayloadJSON: `{"cost":{}} {}`}); err == nil {
		t.Fatal("persisted receipt payload with trailing JSON must fail closed")
	}
}

func TestPostgresReconciliationRowPreservesMaxInt64(t *testing.T) {
	result, err := reconciliationFromEnt(&ledgerent.ReconciliationReport{
		ID: "recon-max-int64", Status: "ok", BlockNewWorkspaces: false, Reason: "operator_reconciliation",
		ReportJSON: `{"id":"recon-max-int64","status":"ok","counts":{"billingOperations":9223372036854775807,"matched":9223372036854775807,"exceptions":0},"exceptions":[]}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(result.Report["counts"].(map[string]any)["billingOperations"]); got != "9223372036854775807" {
		t.Fatalf("persisted reconciliation integer = %s", got)
	}
}

func TestPostgresReconciliationRowRejectsTrailingJSON(t *testing.T) {
	_, err := reconciliationFromEnt(&ledgerent.ReconciliationReport{
		ID: "recon-trailing", Status: "ok", BlockNewWorkspaces: false, Reason: "operator_reconciliation",
		ReportJSON: `{"id":"recon-trailing","status":"ok","counts":{"billingOperations":0,"matched":0,"exceptions":0},"exceptions":[]} {}`,
	})
	if err == nil {
		t.Fatal("persisted reconciliation report with trailing JSON must fail closed")
	}
}

func TestPostgresEvidenceNumberReadback(t *testing.T) {
	store, db := installedLedgerTestPostgres(t)
	ctx := context.Background()
	receiptInput := validBillingReceiptInput()
	receiptInput.IdempotencyKey = "postgres-large-receipt"
	receiptInput.Cost["monthlyPriceCnyCents"] = int64(9_007_199_254_740_993)
	created, err := store.RecordReceipt(ctx, receiptInput)
	if err != nil {
		t.Fatal(err)
	}
	read, err := store.Receipt(ctx, created.ReceiptID)
	if err != nil || fmt.Sprint(read.Cost["monthlyPriceCnyCents"]) != "9007199254740993" {
		t.Fatalf("persisted receipt = %#v, %v", read.Cost, err)
	}
	if _, err := store.UpdateReceiptRetention(ctx, ReceiptRetentionInput{
		ReceiptID: created.ReceiptID, RetainUntil: time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC), IdempotencyKey: "postgres-large-receipt-retention",
	}); err != nil {
		t.Fatal(err)
	}
	read, err = store.Receipt(ctx, created.ReceiptID)
	if err != nil || fmt.Sprint(read.Cost["monthlyPriceCnyCents"]) != "9007199254740993" {
		t.Fatalf("mutated receipt = %#v, %v", read.Cost, err)
	}

	report := validReconciliationReport("ok")
	report["id"] = "postgres-max-int64"
	report["counts"].(map[string]any)["billingOperations"] = int64(9_223_372_036_854_775_807)
	report["counts"].(map[string]any)["matched"] = int64(9_223_372_036_854_775_807)
	reconciliationInput := ReconciliationInput{Report: report, IdempotencyKey: "postgres-max-int64"}
	if _, err := store.RecordReconciliation(ctx, reconciliationInput); err != nil {
		t.Fatal(err)
	}
	replayed, err := store.RecordReconciliation(ctx, reconciliationInput)
	if err != nil || !replayed.Replayed || fmt.Sprint(replayed.Report["counts"].(map[string]any)["billingOperations"]) != "9223372036854775807" {
		t.Fatalf("persisted reconciliation = %#v, %v", replayed, err)
	}

	if _, err := db.Exec(`UPDATE evidence_receipts SET payload_json = '{' WHERE id = $1`, created.ReceiptID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Receipt(ctx, created.ReceiptID); err == nil {
		t.Fatal("invalid persisted receipt payload must fail closed")
	}
}

func TestBillingReceiptSchemaPostgres(t *testing.T) {
	store, _ := installedLedgerTestPostgres(t)
	testBillingReceiptSchema(t, store)
}

func TestReceiptRejectsSensitiveContentPostgres(t *testing.T) {
	store, _ := installedLedgerTestPostgres(t)
	testReceiptRejectsSensitiveContent(t, store)
}

func TestReconciliationSchemaPostgres(t *testing.T) {
	store, db := installedLedgerTestPostgres(t)
	testReconciliationSchema(t, store)

	input := ReconciliationInput{Report: validReconciliationReport("ok"), IdempotencyKey: "reconciliation-schema"}
	result, err := store.RecordReconciliation(context.Background(), input)
	if err != nil || !result.Replayed {
		t.Fatalf("valid replay = %#v, %v", result, err)
	}
	if _, err := db.Exec(`UPDATE reconciliation_reports SET reason = 'invalid' WHERE id = $1`, result.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordReconciliation(context.Background(), input); !errors.Is(err, ErrInvalidReconciliationInput) {
		t.Fatalf("invalid persisted guard error = %v, want ErrInvalidReconciliationInput", err)
	}
	if _, err := db.Exec(`UPDATE reconciliation_reports SET reason = 'operator_reconciliation', report_json = '{"id":"recon-alpha"}' WHERE id = $1`, result.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordReconciliation(context.Background(), input); !errors.Is(err, ErrInvalidReconciliationInput) {
		t.Fatalf("invalid persisted report error = %v, want ErrInvalidReconciliationInput", err)
	}
}

func installedLedgerTestPostgres(t *testing.T) (*PostgresStore, *sql.DB) {
	t.Helper()
	db := openLedgerTestPostgres(t)
	store := NewPostgresStore(db)
	if err := store.Install(context.Background()); err != nil {
		t.Fatalf("install ledger schema: %v", err)
	}
	return store, db
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

	same := validBillingReceiptInput()
	same.AccountID, same.WorkspaceID, same.RequestID, same.IdempotencyKey = "acct-concurrent-same-receipt", "workspace-concurrent-receipt", "billing-operation-concurrent", "receipt-concurrent-same"
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
	changed.Cost = validBillingReceiptInput().Cost
	changed.Cost["chargeUsdMicros"] = int64(49_999_999)
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

func TestPostgresConcurrentReconciliationIdempotency(t *testing.T) {
	db := openLedgerTestPostgres(t)
	store := NewPostgresStore(db)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := store.Install(ctx); err != nil {
		t.Fatalf("install ledger schema: %v", err)
	}
	type outcome struct {
		result ReconciliationResult
		err    error
	}
	run := func(inputs []ReconciliationInput) []outcome {
		t.Helper()
		barrier, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := barrier.ExecContext(ctx, "LOCK TABLE reconciliation_reports IN SHARE MODE"); err != nil {
			_ = barrier.Rollback()
			t.Fatalf("lock reconciliation barrier: %v", err)
		}
		start := make(chan struct{})
		outcomes := make(chan outcome, len(inputs))
		var wg sync.WaitGroup
		for _, input := range inputs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				result, err := store.RecordReconciliation(ctx, input)
				outcomes <- outcome{result: result, err: err}
			}()
		}
		close(start)
		deadline := time.Now().Add(5 * time.Second)
		for {
			var arrivals int
			if err := db.QueryRowContext(ctx, "SELECT count(*) FROM pg_locks WHERE relation = 'reconciliation_reports'::regclass AND mode = 'RowExclusiveLock' AND NOT granted").Scan(&arrivals); err != nil {
				_ = barrier.Rollback()
				t.Fatalf("observe reconciliation barrier: %v", err)
			}
			if arrivals == len(inputs) {
				break
			}
			if time.Now().After(deadline) {
				_ = barrier.Rollback()
				t.Fatalf("reconciliation operations did not overlap: arrivals=%d want=%d", arrivals, len(inputs))
			}
			time.Sleep(5 * time.Millisecond)
		}
		if err := barrier.Commit(); err != nil {
			t.Fatalf("unlock reconciliation barrier: %v", err)
		}
		wg.Wait()
		close(outcomes)
		results := make([]outcome, 0, len(inputs))
		for result := range outcomes {
			results = append(results, result)
		}
		return results
	}

	sameReport := validReconciliationReport("ok")
	sameReport["id"] = "recon-concurrent-same"
	same := ReconciliationInput{Report: sameReport, IdempotencyKey: "reconciliation-concurrent-same"}
	results := run([]ReconciliationInput{same, same})
	created, replayed := 0, 0
	for _, result := range results {
		if result.err != nil {
			t.Fatalf("same reconciliation concurrent create: %v", result.err)
		}
		if result.result.Replayed {
			replayed++
		} else {
			created++
		}
	}
	if created != 1 || replayed != 1 {
		t.Fatalf("same reconciliation outcomes = %#v", results)
	}
	var persisted int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM reconciliation_reports WHERE id = 'recon-concurrent-same'").Scan(&persisted); err != nil || persisted != 1 {
		t.Fatalf("same reconciliation persisted rows=%d err=%v", persisted, err)
	}

	firstReport := validReconciliationReport("ok")
	firstReport["id"] = "recon-concurrent-payload"
	first := ReconciliationInput{Report: firstReport, IdempotencyKey: "reconciliation-concurrent-payload"}
	secondReport := validReconciliationReport("ok")
	secondReport["id"] = "recon-concurrent-payload-changed"
	secondReport["counts"].(map[string]any)["observed"] = 1
	second := ReconciliationInput{Report: secondReport, IdempotencyKey: first.IdempotencyKey}
	results = run([]ReconciliationInput{first, second})
	created, conflicts := 0, 0
	for _, result := range results {
		switch {
		case errors.Is(result.err, ErrIdempotencyConflict):
			conflicts++
		case result.err != nil:
			t.Fatalf("different reconciliation payload raw error: %v", result.err)
		default:
			created++
		}
	}
	if created != 1 || conflicts != 1 {
		t.Fatalf("different reconciliation payload outcomes = %#v", results)
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
