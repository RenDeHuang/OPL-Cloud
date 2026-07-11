package ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestMemoryReceiptRetentionContract(t *testing.T) {
	testReceiptRetentionContract(t, NewMemoryStore())
}

func TestPostgresReceiptRetentionContract(t *testing.T) {
	db := openLedgerTestPostgres(t)
	store := NewPostgresStore(db)
	if err := store.Install(context.Background()); err != nil {
		t.Fatalf("install ledger schema: %v", err)
	}
	testReceiptRetentionContract(t, store)
}

func testReceiptRetentionContract(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	receipt := recordRetentionTestReceipt(t, store, "contract")
	firstUntil := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Microsecond)
	first, err := store.UpdateReceiptRetention(ctx, ReceiptRetentionInput{
		ReceiptID: receipt.ReceiptID, RetainUntil: firstUntil, IdempotencyKey: "retention-first",
	})
	if err != nil {
		t.Fatalf("set receipt retention: %v", err)
	}
	if !first.Retention.RetainUntil.Equal(firstUntil) || first.Retention.LegalHold {
		t.Fatalf("first retention = %#v", first.Retention)
	}
	if _, err := store.UpdateReceiptRetention(ctx, ReceiptRetentionInput{
		ReceiptID: receipt.ReceiptID, RetainUntil: firstUntil.Add(-time.Hour), IdempotencyKey: "retention-shorter",
	}); !errors.Is(err, ErrReceiptRetentionShortening) {
		t.Fatalf("shorten retention error = %v, want %v", err, ErrReceiptRetentionShortening)
	}
	longest := firstUntil.Add(48 * time.Hour)
	held, err := store.UpdateReceiptRetention(ctx, ReceiptRetentionInput{
		ReceiptID: receipt.ReceiptID, RetainUntil: longest, LegalHold: true, IdempotencyKey: "retention-hold",
	})
	if err != nil {
		t.Fatalf("place legal hold: %v", err)
	}
	if !held.Retention.LegalHold || !held.Retention.RetainUntil.Equal(longest) {
		t.Fatalf("held retention = %#v", held.Retention)
	}
	stillHeld, err := store.UpdateReceiptRetention(ctx, ReceiptRetentionInput{
		ReceiptID: receipt.ReceiptID, RetainUntil: longest.Add(time.Hour), LegalHold: false, IdempotencyKey: "retention-no-clear",
	})
	if err != nil {
		t.Fatalf("extend held receipt: %v", err)
	}
	if !stillHeld.Retention.LegalHold {
		t.Fatal("normal retention update cleared legal hold")
	}
	if _, err := store.PrivacyDeleteReceipt(ctx, ReceiptPrivacyDeleteInput{
		ReceiptID: receipt.ReceiptID, Reason: "account deletion", IdempotencyKey: "privacy-held",
	}); !errors.Is(err, ErrReceiptLegalHold) {
		t.Fatalf("privacy delete under legal hold error = %v, want %v", err, ErrReceiptLegalHold)
	}

	privacyReceipt := recordRetentionTestReceipt(t, store, "privacy")
	past := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	if _, err := store.UpdateReceiptRetention(ctx, ReceiptRetentionInput{
		ReceiptID: privacyReceipt.ReceiptID, RetainUntil: past, IdempotencyKey: "privacy-expired",
	}); err != nil {
		t.Fatalf("set expired retention: %v", err)
	}
	redacted, err := store.PrivacyDeleteReceipt(ctx, ReceiptPrivacyDeleteInput{
		ReceiptID: privacyReceipt.ReceiptID, Reason: "verified account deletion", IdempotencyKey: "privacy-delete",
	})
	if err != nil {
		t.Fatalf("privacy delete: %v", err)
	}
	redactedReceipt := canonicalReceipt(t, store, privacyReceipt.ReceiptID)
	if redactedReceipt.Actor != nil || redactedReceipt.Owner != nil || redactedReceipt.Continuation != nil {
		t.Fatalf("personal fields survived redaction: actor=%#v owner=%#v continuation=%#v", redactedReceipt.Actor, redactedReceipt.Owner, redactedReceipt.Continuation)
	}
	if redactedReceipt.OrganizationID != privacyReceipt.OrganizationID || redactedReceipt.WorkspaceID != privacyReceipt.WorkspaceID || redactedReceipt.ProjectID != privacyReceipt.ProjectID || redactedReceipt.TaskID != privacyReceipt.TaskID || redactedReceipt.JobID != privacyReceipt.JobID || redactedReceipt.Type != privacyReceipt.Type || redactedReceipt.Status != privacyReceipt.Status || redactedReceipt.ContinuationID != privacyReceipt.ContinuationID {
		t.Fatalf("audit identity changed: %#v", redactedReceipt)
	}
	if !reflect.DeepEqual(redactedReceipt.Plan, privacyReceipt.Plan) || !reflect.DeepEqual(redactedReceipt.Execution, privacyReceipt.Execution) || !reflect.DeepEqual(redactedReceipt.Environment, privacyReceipt.Environment) || !reflect.DeepEqual(redactedReceipt.InputRefs, privacyReceipt.InputRefs) || !reflect.DeepEqual(redactedReceipt.OutputRefs, privacyReceipt.OutputRefs) || !reflect.DeepEqual(redactedReceipt.ReviewerChecks, privacyReceipt.ReviewerChecks) || !reflect.DeepEqual(redactedReceipt.Cost, privacyReceipt.Cost) {
		t.Fatalf("required provenance changed: %#v", redactedReceipt)
	}
	if redacted.Retention.PrivacyRedaction == nil || redacted.Retention.PrivacyRedaction.AppliedAt.IsZero() || redacted.Retention.PrivacyRedaction.Reason != "verified account deletion" || !redacted.Retention.PrivacyRedaction.Eligible {
		t.Fatalf("privacy evidence = %#v", redacted.Retention.PrivacyRedaction)
	}
	replayed, err := store.PrivacyDeleteReceipt(ctx, ReceiptPrivacyDeleteInput{
		ReceiptID: privacyReceipt.ReceiptID, Reason: "verified account deletion", IdempotencyKey: "privacy-delete",
	})
	if err != nil {
		t.Fatalf("replay privacy delete: %v", err)
	}
	wantReplay := redacted
	wantReplay.Replayed = true
	replayedJSON, _ := json.Marshal(replayed)
	wantReplayJSON, _ := json.Marshal(wantReplay)
	if string(replayedJSON) != string(wantReplayJSON) {
		t.Fatalf("privacy replay = %#v, want %#v", replayed, wantReplay)
	}
	if _, err := store.PrivacyDeleteReceipt(ctx, ReceiptPrivacyDeleteInput{
		ReceiptID: privacyReceipt.ReceiptID, Reason: "changed reason", IdempotencyKey: "privacy-delete",
	}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed privacy replay error = %v, want %v", err, ErrIdempotencyConflict)
	}
}

func TestMemoryReceiptRetentionConcurrentOperationsSerialize(t *testing.T) {
	store := NewMemoryStore()
	receipt := recordRetentionTestReceipt(t, store, "memory-race")
	past := time.Now().UTC().Add(-time.Hour)
	if _, err := store.UpdateReceiptRetention(context.Background(), ReceiptRetentionInput{ReceiptID: receipt.ReceiptID, RetainUntil: past, IdempotencyKey: "memory-race-past"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PrivacyDeleteReceipt(context.Background(), ReceiptPrivacyDeleteInput{ReceiptID: receipt.ReceiptID, Reason: "verified deletion", IdempotencyKey: "memory-race-redact"}); err != nil {
		t.Fatal(err)
	}

	longest := time.Now().UTC().Add(72 * time.Hour).Truncate(time.Microsecond)
	operations := []func() error{
		func() error {
			_, err := store.UpdateReceiptRetention(context.Background(), ReceiptRetentionInput{ReceiptID: receipt.ReceiptID, RetainUntil: longest.Add(-time.Hour), IdempotencyKey: "memory-race-short"})
			return err
		},
		func() error {
			_, err := store.UpdateReceiptRetention(context.Background(), ReceiptRetentionInput{ReceiptID: receipt.ReceiptID, RetainUntil: longest, LegalHold: true, IdempotencyKey: "memory-race-hold"})
			return err
		},
		func() error {
			_, err := store.PrivacyDeleteReceipt(context.Background(), ReceiptPrivacyDeleteInput{ReceiptID: receipt.ReceiptID, Reason: "verified deletion", IdempotencyKey: "memory-race-redact-again"})
			return err
		},
	}
	store.mu.Lock()
	started := make(chan struct{}, len(operations))
	errs := make(chan error, len(operations))
	var workers sync.WaitGroup
	for _, operation := range operations {
		workers.Add(1)
		go func() {
			defer workers.Done()
			started <- struct{}{}
			errs <- operation()
		}()
	}
	for range operations {
		<-started
	}
	store.mu.Unlock()
	workers.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !errors.Is(err, ErrReceiptRetentionShortening) {
			t.Fatalf("concurrent operation: %v", err)
		}
	}
	got, err := store.Receipt(context.Background(), receipt.ReceiptID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Retention.LegalHold || !got.Retention.RetainUntil.Equal(longest) || got.Retention.PrivacyRedaction == nil || got.Actor != nil || got.Owner != nil || got.Continuation != nil {
		t.Fatalf("concurrent receipt = %#v", got)
	}
}

func TestPostgresReceiptRetentionConcurrentOperationsWaitAndSerialize(t *testing.T) {
	db := openLedgerTestPostgres(t)
	store := NewPostgresStore(db)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := store.Install(ctx); err != nil {
		t.Fatalf("install ledger schema: %v", err)
	}
	receipt := recordRetentionTestReceipt(t, store, "postgres-race")
	past := time.Now().UTC().Add(-time.Hour)
	if _, err := store.UpdateReceiptRetention(ctx, ReceiptRetentionInput{ReceiptID: receipt.ReceiptID, RetainUntil: past, IdempotencyKey: "postgres-race-past"}); err != nil {
		t.Fatal(err)
	}
	redacted, err := store.PrivacyDeleteReceipt(ctx, ReceiptPrivacyDeleteInput{ReceiptID: receipt.ReceiptID, Reason: "verified deletion", IdempotencyKey: "postgres-race-redact"})
	if err != nil {
		t.Fatal(err)
	}

	blocker, err := db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err := blocker.ExecContext(ctx, "SELECT id FROM evidence_receipts WHERE id = $1 FOR UPDATE", receipt.ReceiptID); err != nil {
		t.Fatalf("lock receipt row: %v", err)
	}
	longest := time.Now().UTC().Add(72 * time.Hour).Truncate(time.Microsecond)
	operations := []func() error{
		func() error {
			_, err := store.UpdateReceiptRetention(ctx, ReceiptRetentionInput{ReceiptID: receipt.ReceiptID, RetainUntil: longest.Add(-time.Hour), IdempotencyKey: "postgres-race-short"})
			return err
		},
		func() error {
			_, err := store.UpdateReceiptRetention(ctx, ReceiptRetentionInput{ReceiptID: receipt.ReceiptID, RetainUntil: longest, LegalHold: true, IdempotencyKey: "postgres-race-hold"})
			return err
		},
		func() error {
			_, err := store.PrivacyDeleteReceipt(ctx, ReceiptPrivacyDeleteInput{ReceiptID: receipt.ReceiptID, Reason: "verified deletion", IdempotencyKey: "postgres-race-redact-again"})
			return err
		},
	}
	started := make(chan struct{}, len(operations))
	errs := make(chan error, len(operations))
	var workers sync.WaitGroup
	for _, operation := range operations {
		workers.Add(1)
		go func() {
			defer workers.Done()
			started <- struct{}{}
			errs <- operation()
		}()
	}
	for range operations {
		<-started
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiters int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE '%ledger_receipt_mutation%'`).Scan(&waiters); err != nil {
			t.Fatalf("observe receipt lock waiters: %v", err)
		}
		if waiters == len(operations) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("receipt operations did not overlap at row lock: waiters=%d want=%d", waiters, len(operations))
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := blocker.Commit(); err != nil {
		t.Fatalf("release receipt row: %v", err)
	}
	workers.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !errors.Is(err, ErrReceiptLegalHold) && !errors.Is(err, ErrReceiptRetentionActive) && !errors.Is(err, ErrReceiptRetentionShortening) {
			t.Fatalf("concurrent operation: %v", err)
		}
	}
	got, err := store.Receipt(ctx, receipt.ReceiptID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Retention.LegalHold || !got.Retention.RetainUntil.Equal(longest) || !reflect.DeepEqual(got.Retention.PrivacyRedaction, redacted.Retention.PrivacyRedaction) || got.Actor != nil || got.Owner != nil || got.Continuation != nil {
		t.Fatalf("concurrent receipt = %#v", got)
	}
}

func recordRetentionTestReceipt(t *testing.T, store Store, suffix string) Receipt {
	t.Helper()
	receipt, err := store.RecordReceipt(context.Background(), ReceiptInput{
		Type: "execution.receipt.v1", Status: "completed", Surface: "workspace",
		OrganizationID: "org-" + suffix, WorkspaceID: "workspace-" + suffix, ProjectID: "project-" + suffix, TaskID: "task-" + suffix, RequestID: "request-" + suffix, ApprovalID: "approval-" + suffix, JobID: "job-" + suffix, ArtifactID: "artifact-" + suffix, ReviewID: "review-" + suffix, ContinuationID: "continuation-" + suffix,
		Actor: map[string]any{"userId": "user-" + suffix, "email": suffix + "@example.test"}, Owner: map[string]any{"name": "Owner " + suffix},
		Plan: map[string]any{"version": "1"}, Execution: map[string]any{"providerRequestId": "provider-" + suffix}, Environment: map[string]any{"environmentRef": "env-" + suffix}, InputRefs: map[string]any{"digest": "sha256:input-" + suffix}, OutputRefs: map[string]any{"digest": "sha256:output-" + suffix}, ReviewerChecks: map[string]any{"decision": "accepted"}, Cost: map[string]any{"settlementId": "settlement-" + suffix, "amountCents": float64(100)}, Continuation: map[string]any{"continuationId": "continuation-" + suffix, "freeForm": "personal note"},
		IdempotencyKey: "receipt-" + suffix,
	})
	if err != nil {
		t.Fatalf("record receipt: %v", err)
	}
	return receipt
}

func canonicalReceipt(t *testing.T, store Store, receiptID string) Receipt {
	t.Helper()
	switch typed := store.(type) {
	case *MemoryStore:
		typed.mu.Lock()
		defer typed.mu.Unlock()
		return typed.receipts[receiptID]
	case *PostgresStore:
		var payloadJSON string
		if err := typed.db.QueryRow("SELECT payload_json FROM evidence_receipts WHERE id = $1", receiptID).Scan(&payloadJSON); err != nil {
			t.Fatal(err)
		}
		var stored receiptPayload
		if err := json.Unmarshal([]byte(payloadJSON), &stored); err != nil {
			t.Fatal(err)
		}
		return Receipt{ReceiptInput: stored.ReceiptInput, ReceiptID: receiptID, Retention: stored.Retention}
	default:
		t.Fatalf("unsupported store %T", store)
		return Receipt{}
	}
}
