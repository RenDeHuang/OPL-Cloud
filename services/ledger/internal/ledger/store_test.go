package ledger

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestReceiptRejectsMissingIdentityAndSecretContent(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	if _, err := store.RecordReceipt(ctx, ReceiptInput{WorkspaceID: "ws-alpha", IdempotencyKey: "invalid-receipt"}); !errors.Is(err, ErrInvalidReceiptInput) {
		t.Fatalf("missing receipt fields error = %v, want ErrInvalidReceiptInput", err)
	}
	_, err := store.RecordReceipt(ctx, ReceiptInput{Type: "workspace.created", Status: "completed", Surface: "workspace", WorkspaceID: "ws-alpha", Actor: map[string]any{"secret": "must-not-persist"}, IdempotencyKey: "secret-receipt"})
	if !errors.Is(err, ErrInvalidReceiptInput) {
		t.Fatalf("secret receipt error = %v, want ErrInvalidReceiptInput", err)
	}
}

func TestBillingReceiptIdempotencyCorrectionAndAccountWorkspaceQuery(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	input := ReceiptInput{
		Type: "billing.resource_purchased.v1", Status: "completed", Surface: "control_plane", AccountID: "acct-alpha", WorkspaceID: "workspace-alpha",
		RequestID: "billing-operation-alpha", Cost: map[string]any{"chargeUsdMicros": int64(50_000_000)}, IdempotencyKey: "billing-receipt-alpha",
	}
	first, err := store.RecordReceipt(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := store.RecordReceipt(ctx, input)
	if err != nil || !replayed.Replayed || replayed.ReceiptID != first.ReceiptID {
		t.Fatalf("replay=%#v err=%v", replayed, err)
	}
	correctionInput := input
	correctionInput.IdempotencyKey = "billing-receipt-alpha-correction"
	correctionInput.SupersedesReceiptID = first.ReceiptID
	correctionInput.Cost = map[string]any{"chargeUsdMicros": int64(49_999_999)}
	correction, err := store.RecordReceipt(ctx, correctionInput)
	if err != nil || correction.SupersedesReceiptID != first.ReceiptID {
		t.Fatalf("correction=%#v err=%v", correction, err)
	}
	page, err := store.ListReceipts(ctx, ReceiptQuery{AccountID: "acct-alpha", WorkspaceID: "workspace-alpha"})
	if err != nil || len(page.Receipts) != 2 {
		t.Fatalf("account/workspace receipts=%#v err=%v", page, err)
	}
	other, err := store.ListReceipts(ctx, ReceiptQuery{AccountID: "acct-other"})
	if err != nil || len(other.Receipts) != 0 {
		t.Fatalf("other account receipts=%#v err=%v", other, err)
	}
}

func TestReconciliationMismatchBlocksNewWorkspaces(t *testing.T) {
	result, err := NewMemoryStore().RecordReconciliation(context.Background(), ReconciliationInput{Report: map[string]any{"id": "recon-alpha", "status": "mismatch"}, IdempotencyKey: "reconciliation-alpha"})
	if err != nil || !result.BlockNewWorkspaces || result.Status != "mismatch" {
		t.Fatalf("reconciliation=%#v err=%v", result, err)
	}
}

func TestAnyContinuationRequiresFullIdentity(t *testing.T) {
	for _, receiptType := range []string{"execution.receipt.v1", "workspace.created"} {
		store := NewMemoryStore()
		receipt, err := store.RecordReceipt(context.Background(), ReceiptInput{
			Type:           receiptType,
			Status:         "completed",
			Surface:        "workspace",
			WorkspaceID:    "workspace-alpha",
			ProjectID:      "project-alpha",
			TaskID:         "task-alpha",
			JobID:          "job-alpha",
			IdempotencyKey: "receipt-continuation",
			Continuation: map[string]any{
				"continuationId":          "continuation-alpha",
				"taskVersion":             float64(3),
				"requiredArtifactDigests": []any{"sha256:alpha"},
				"environmentRef":          "environment-alpha",
			},
		})
		if !errors.Is(err, ErrInvalidReceiptInput) || receipt.ReceiptID != "" {
			t.Fatalf("%s incomplete continuation = %#v, %v", receiptType, receipt, err)
		}
	}
}

func TestLegacyReceiptWithoutContinuationRemainsReadable(t *testing.T) {
	store := NewMemoryStore()
	receipt, err := store.RecordReceipt(context.Background(), ReceiptInput{Type: "workspace.created", Status: "completed", Surface: "workspace", WorkspaceID: "workspace-alpha", IdempotencyKey: "legacy-no-continuation"})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Receipt(context.Background(), receipt.ReceiptID)
	if err != nil || loaded.ReceiptID != receipt.ReceiptID || loaded.Continuation != nil || loaded.ContinuationID != "" {
		t.Fatalf("legacy receipt = %#v, %v", loaded, err)
	}
}

func TestPersistedIncompleteReceiptNeverExposesContinuation(t *testing.T) {
	store := NewMemoryStore()
	receipt := Receipt{
		ReceiptInput: ReceiptInput{Type: "workspace.created", Status: "completed", WorkspaceID: "workspace-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", JobID: "job-alpha", ContinuationID: "continuation-old", Continuation: map[string]any{"continuationId": "continuation-old"}},
		ReceiptID:    "receipt-old",
		CreatedAt:    time.Now().UTC(),
	}
	store.receipts[receipt.ReceiptID] = receipt
	loaded, err := store.Receipt(context.Background(), receipt.ReceiptID)
	if err != nil || loaded.ContinuationID != "" || loaded.Continuation != nil {
		t.Fatalf("receipt detail leaked continuation: %#v, %v", loaded, err)
	}
	page, err := store.ListReceipts(context.Background(), ReceiptQuery{})
	if err != nil || len(page.Receipts) != 1 || page.Receipts[0].ContinuationID != "" || page.Receipts[0].Continuation != nil {
		t.Fatalf("receipt list leaked continuation: %#v, %v", page, err)
	}
	if _, err := store.Continuation(context.Background(), receipt.ReceiptID); !errors.Is(err, ErrContinuationIneligible) {
		t.Fatalf("continuation error = %v", err)
	}
}

func TestReceiptGeneratesContinuationIdentity(t *testing.T) {
	store := NewMemoryStore()
	receipt, err := store.RecordReceipt(context.Background(), ReceiptInput{
		Type:           "execution.receipt.v1",
		Status:         "running",
		Surface:        "workspace",
		OrganizationID: "org-alpha",
		WorkspaceID:    "workspace-alpha",
		ProjectID:      "project-alpha",
		TaskID:         "task-alpha",
		JobID:          "job-alpha",
		IdempotencyKey: "generated-continuation",
		Continuation:   map[string]any{"taskVersion": float64(1)},
	})
	if err != nil {
		t.Fatalf("record receipt: %v", err)
	}
	if receipt.ContinuationID == "" || receipt.Continuation["continuationId"] != receipt.ContinuationID {
		t.Fatalf("ledger must own continuation identity: %#v", receipt)
	}
}

func TestReceiptAcceptsTimedOutExecutionStatus(t *testing.T) {
	store := NewMemoryStore()
	receipt, err := store.RecordReceipt(context.Background(), ReceiptInput{Type: "execution.receipt.v1", Status: "timed_out", Surface: "workspace", WorkspaceID: "workspace-alpha", IdempotencyKey: "timed-out-receipt"})
	if err != nil || receipt.Status != "timed_out" {
		t.Fatalf("timed out receipt: %#v, %v", receipt, err)
	}
}

func TestArtifactManifestRecordsAndQueriesEvidence(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	input := ArtifactInput{
		OrganizationID: "org-alpha",
		WorkspaceID:    "workspace-alpha",
		ProjectID:      "project-alpha",
		TaskID:         "task-alpha",
		JobID:          "job-alpha",
		Digest:         "sha256:abc123",
		MediaType:      "application/json",
		SizeBytes:      42,
		StorageRef:     "storage-artifact-alpha",
		IdempotencyKey: "artifact-once",
	}
	created, err := store.RecordArtifact(ctx, input)
	if err != nil {
		t.Fatalf("record artifact: %v", err)
	}
	if created.ArtifactID == "" || created.ReceiptID == "" || created.Digest != input.Digest {
		t.Fatalf("unexpected artifact: %#v", created)
	}
	replayed, err := store.RecordArtifact(ctx, input)
	if err != nil || !replayed.Replayed || replayed.ArtifactID != created.ArtifactID {
		t.Fatalf("unexpected replay: %#v, %v", replayed, err)
	}
	loaded, err := store.Artifact(ctx, created.ArtifactID)
	if err != nil || loaded.StorageRef != "storage-artifact-alpha" || loaded.JobID != "job-alpha" {
		t.Fatalf("unexpected loaded artifact: %#v, %v", loaded, err)
	}
}

func TestArtifactManifestRejectsUnsafeStorageReference(t *testing.T) {
	store := NewMemoryStore()
	_, err := store.RecordArtifact(context.Background(), ArtifactInput{WorkspaceID: "workspace-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", JobID: "job-alpha", Digest: "sha256:abc123", MediaType: "application/json", SizeBytes: 42, StorageRef: "https://storage.example/result?signature=secret", IdempotencyKey: "unsafe-artifact"})
	if !errors.Is(err, ErrInvalidArtifactInput) {
		t.Fatalf("error = %v, want ErrInvalidArtifactInput", err)
	}
}

func TestReviewResultRecordsAndQueriesDecision(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	input := ReviewInput{
		OrganizationID:       "org-alpha",
		WorkspaceID:          "workspace-alpha",
		ProjectID:            "project-alpha",
		TaskID:               "task-alpha",
		JobID:                "job-alpha",
		ReviewerRef:          "reviewer-rca",
		ReviewerVersion:      "1.0.0",
		InputArtifactDigests: []string{"sha256:abc123"},
		Checks:               map[string]any{"schema": "passed"},
		Decision:             "accepted",
		IdempotencyKey:       "review-once",
	}
	created, err := store.RecordReview(ctx, input)
	if err != nil {
		t.Fatalf("record review: %v", err)
	}
	if created.ReviewID == "" || created.Decision != "accepted" {
		t.Fatalf("unexpected review: %#v", created)
	}
	loaded, err := store.Review(ctx, created.ReviewID)
	if err != nil || loaded.ReviewerRef != "reviewer-rca" || len(loaded.InputArtifactDigests) != 1 {
		t.Fatalf("unexpected loaded review: %#v, %v", loaded, err)
	}
	input.Decision = "rejected"
	input.IdempotencyKey = "review-rejected"
	rejected, err := store.RecordReview(ctx, input)
	if err != nil || rejected.Decision != "rejected" {
		t.Fatalf("unexpected rejected review: %#v, %v", rejected, err)
	}
}

func TestReviewPolicyIsVersionedIdempotentAndSupersedes(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	firstInput := ReviewPolicyInput{
		ExecutionIdentity: ExecutionIdentity{OrganizationID: "org-alpha", WorkspaceID: "workspace-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", JobID: "job-alpha"},
		Version:           "1", RequiredReviewers: []RequiredReviewer{{ReviewerRef: "reviewer-rca", ReviewerVersion: "1.0.0"}}, IdempotencyKey: "policy-v1",
	}
	first, err := store.CreateReviewPolicy(ctx, firstInput)
	if err != nil || first.PolicyID == "" || first.Status != "active" {
		t.Fatalf("create first policy = %#v, %v", first, err)
	}
	replayed, err := store.CreateReviewPolicy(ctx, firstInput)
	if err != nil || !replayed.Replayed || replayed.PolicyID != first.PolicyID {
		t.Fatalf("replay first policy = %#v, %v", replayed, err)
	}

	secondInput := firstInput
	secondInput.Version = "2"
	secondInput.RequiredReviewers = []RequiredReviewer{{ReviewerRef: "reviewer-rca", ReviewerVersion: "2.0.0"}}
	secondInput.SupersedesPolicyID = first.PolicyID
	secondInput.IdempotencyKey = "policy-v2"
	second, err := store.CreateReviewPolicy(ctx, secondInput)
	if err != nil || second.Status != "active" {
		t.Fatalf("create second policy = %#v, %v", second, err)
	}
	loadedFirst, err := store.ReviewPolicy(ctx, first.PolicyID)
	if err != nil || loadedFirst.Status != "superseded" {
		t.Fatalf("superseded first policy = %#v, %v", loadedFirst, err)
	}
	replayedFirst, err := store.CreateReviewPolicy(ctx, firstInput)
	if err != nil || replayedFirst.Status != "superseded" || !replayedFirst.Replayed {
		t.Fatalf("replay must return current policy status = %#v, %v", replayedFirst, err)
	}
	policies, err := store.ListReviewPolicies(ctx, ReviewPolicyQuery{ExecutionIdentity: ExecutionIdentity{JobID: "job-alpha"}})
	if err != nil || len(policies) != 2 || policies[0].PolicyID != second.PolicyID {
		t.Fatalf("list policies = %#v, %v", policies, err)
	}
}

func TestReviewGateEvaluatesRequiredReviewEvidence(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	scope := ExecutionIdentity{OrganizationID: "org-alpha", WorkspaceID: "workspace-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", JobID: "job-alpha"}
	policy, err := store.CreateReviewPolicy(ctx, ReviewPolicyInput{
		ExecutionIdentity: scope, Version: "1",
		RequiredReviewers: []RequiredReviewer{{ReviewerRef: "reviewer-rca", ReviewerVersion: "1.0.0"}, {ReviewerRef: "reviewer-book", ReviewerVersion: "2.0.0"}},
		IdempotencyKey:    "gate-policy",
	})
	if err != nil {
		t.Fatalf("create policy: %v", err)
	}
	review := func(key, ref, version, decision string) Review {
		result, err := store.RecordReview(ctx, ReviewInput{
			OrganizationID: scope.OrganizationID, WorkspaceID: scope.WorkspaceID, ProjectID: scope.ProjectID, TaskID: scope.TaskID, JobID: scope.JobID,
			ReviewerRef: ref, ReviewerVersion: version, InputArtifactDigests: []string{"sha256:abc123"}, Checks: map[string]any{"schema": "checked"}, Decision: decision, IdempotencyKey: key,
		})
		if err != nil {
			t.Fatalf("record review: %v", err)
		}
		return result
	}
	accepted := review("accepted-review", "reviewer-rca", "1.0.0", "accepted")
	pending := review("pending-review", "reviewer-book", "2.0.0", "pending")

	required, err := store.EvaluateReviewGate(ctx, ReviewGateInput{ExecutionIdentity: scope, ReviewIDs: []string{accepted.ReviewID, pending.ReviewID}})
	if err != nil || required.Status != "review_required" || required.ContinuationEligible || len(required.Pending) != 1 || required.PolicyID != policy.PolicyID {
		t.Fatalf("pending gate = %#v, %v", required, err)
	}
	required, err = store.EvaluateReviewGate(ctx, ReviewGateInput{ExecutionIdentity: scope, ReviewIDs: []string{accepted.ReviewID}})
	if err != nil || required.Status != "review_required" || required.ContinuationEligible || len(required.Missing) != 1 {
		t.Fatalf("missing gate = %#v, %v", required, err)
	}
	wrongVersion := review("wrong-version-review", "reviewer-book", "1.0.0", "accepted")
	blocked, err := store.EvaluateReviewGate(ctx, ReviewGateInput{ExecutionIdentity: scope, ReviewIDs: []string{accepted.ReviewID, wrongVersion.ReviewID}})
	if err != nil || blocked.Status != "review_blocked" || blocked.ContinuationEligible || len(blocked.VersionMismatches) != 1 {
		t.Fatalf("version mismatch gate = %#v, %v", blocked, err)
	}
	rejected := review("rejected-review", "reviewer-book", "2.0.0", "rejected")
	blocked, err = store.EvaluateReviewGate(ctx, ReviewGateInput{ExecutionIdentity: scope, ReviewIDs: []string{accepted.ReviewID, rejected.ReviewID}})
	if err != nil || blocked.Status != "review_blocked" || blocked.ContinuationEligible || len(blocked.Rejected) != 1 {
		t.Fatalf("rejected gate = %#v, %v", blocked, err)
	}
	bookAccepted := review("book-accepted-review", "reviewer-book", "2.0.0", "accepted")
	passed, err := store.EvaluateReviewGate(ctx, ReviewGateInput{ExecutionIdentity: scope, ReviewIDs: []string{accepted.ReviewID, bookAccepted.ReviewID}})
	if err != nil || passed.Status != "accepted" || !passed.ContinuationEligible {
		t.Fatalf("accepted gate = %#v, %v", passed, err)
	}
}

func TestReviewGateUsesRequiredVersionRegardlessOfReviewOrder(t *testing.T) {
	scope := ExecutionIdentity{OrganizationID: "org-alpha", WorkspaceID: "workspace-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", JobID: "job-alpha"}
	policy := ReviewPolicy{
		ReviewPolicyInput: ReviewPolicyInput{ExecutionIdentity: scope, Version: "2", RequiredReviewers: []RequiredReviewer{{ReviewerRef: "reviewer-rca", ReviewerVersion: "2.0.0"}}},
		PolicyID:          "policy-alpha", Status: "active",
	}
	result := evaluateReviewGate(policy, []Review{
		{ReviewInput: ReviewInput{OrganizationID: scope.OrganizationID, WorkspaceID: scope.WorkspaceID, ProjectID: scope.ProjectID, TaskID: scope.TaskID, JobID: scope.JobID, ReviewerRef: "reviewer-rca", ReviewerVersion: "1.0.0", Decision: "accepted"}, ReviewID: "review-old"},
		{ReviewInput: ReviewInput{OrganizationID: scope.OrganizationID, WorkspaceID: scope.WorkspaceID, ProjectID: scope.ProjectID, TaskID: scope.TaskID, JobID: scope.JobID, ReviewerRef: "reviewer-rca", ReviewerVersion: "2.0.0", Decision: "accepted"}, ReviewID: "review-current"},
	})
	if result.Status != "accepted" || !result.ContinuationEligible || len(result.VersionMismatches) != 0 {
		t.Fatalf("gate must select required reviewer version: %#v", result)
	}
}

func TestContinuationIsIneligibleUntilActiveReviewPolicyPasses(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	scope := ExecutionIdentity{OrganizationID: "org-alpha", WorkspaceID: "workspace-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", JobID: "job-alpha"}
	if _, err := store.CreateReviewPolicy(ctx, ReviewPolicyInput{ExecutionIdentity: scope, Version: "1", RequiredReviewers: []RequiredReviewer{{ReviewerRef: "reviewer-rca", ReviewerVersion: "1.0.0"}}, IdempotencyKey: "continuation-policy"}); err != nil {
		t.Fatalf("create policy: %v", err)
	}
	receipt, err := store.RecordReceipt(ctx, ReceiptInput{
		Type: "execution.receipt.v1", Status: "completed", Surface: "workspace", OrganizationID: scope.OrganizationID, WorkspaceID: scope.WorkspaceID, ProjectID: scope.ProjectID, TaskID: scope.TaskID, JobID: scope.JobID,
		Continuation: map[string]any{"continuationId": "continuation-alpha", "reviewIds": []string{}}, IdempotencyKey: "continuation-gated-receipt",
	})
	if err != nil {
		t.Fatalf("record receipt: %v", err)
	}
	if _, err := store.Continuation(ctx, receipt.ReceiptID); !errors.Is(err, ErrContinuationIneligible) {
		t.Fatalf("continuation error = %v, want ErrContinuationIneligible", err)
	}
}

func TestFullExecutionContinuationFailsClosedWithoutPolicy(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	receipt, err := store.RecordReceipt(ctx, ReceiptInput{
		Type: "execution.receipt.v1", Status: "completed", Surface: "workspace", OrganizationID: "org-alpha", WorkspaceID: "workspace-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", JobID: "job-alpha",
		Continuation: map[string]any{"continuationId": "continuation-no-policy"}, IdempotencyKey: "receipt-no-policy",
	})
	if err != nil {
		t.Fatalf("record receipt: %v", err)
	}
	if _, err := store.Continuation(ctx, receipt.ReceiptID); !errors.Is(err, ErrContinuationIneligible) {
		t.Fatalf("continuation error = %v, want ErrContinuationIneligible", err)
	}
}

func TestReceiptReadsHideContinuationUntilGateAccepted(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	scope := ExecutionIdentity{OrganizationID: "org-alpha", WorkspaceID: "workspace-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", JobID: "job-alpha"}
	recordReceipt := func(key, continuationID string, reviewIDs []string) Receipt {
		receipt, err := store.RecordReceipt(ctx, ReceiptInput{
			Type: "execution.receipt.v1", Status: "completed", Surface: "workspace", OrganizationID: scope.OrganizationID, WorkspaceID: scope.WorkspaceID, ProjectID: scope.ProjectID, TaskID: scope.TaskID, JobID: scope.JobID,
			Continuation: map[string]any{"continuationId": continuationID, "reviewIds": reviewIDs}, IdempotencyKey: key,
		})
		if err != nil {
			t.Fatalf("record receipt: %v", err)
		}
		return receipt
	}
	assertHidden := func(receiptID string) {
		t.Helper()
		loaded, err := store.Receipt(ctx, receiptID)
		if err != nil || loaded.ContinuationID != "" || loaded.Continuation != nil {
			t.Fatalf("receipt continuation must be hidden = %#v, %v", loaded, err)
		}
		page, err := store.ListReceipts(ctx, ReceiptQuery{JobID: scope.JobID})
		if err != nil {
			t.Fatalf("list receipts: %v", err)
		}
		for _, listed := range page.Receipts {
			if listed.ReceiptID == receiptID && (listed.ContinuationID != "" || listed.Continuation != nil) {
				t.Fatalf("listed receipt continuation must be hidden: %#v", listed)
			}
		}
	}

	noPolicy := recordReceipt("no-policy-read", "continuation-no-policy-read", nil)
	assertHidden(noPolicy.ReceiptID)
	if _, err := store.CreateReviewPolicy(ctx, ReviewPolicyInput{ExecutionIdentity: scope, Version: "1", RequiredReviewers: []RequiredReviewer{{ReviewerRef: "reviewer-rca", ReviewerVersion: "1.0.0"}}, IdempotencyKey: "read-policy"}); err != nil {
		t.Fatalf("create policy: %v", err)
	}
	rejected, err := store.RecordReview(ctx, ReviewInput{OrganizationID: scope.OrganizationID, WorkspaceID: scope.WorkspaceID, ProjectID: scope.ProjectID, TaskID: scope.TaskID, JobID: scope.JobID, ReviewerRef: "reviewer-rca", ReviewerVersion: "1.0.0", InputArtifactDigests: []string{"sha256:abc123"}, Checks: map[string]any{"schema": "checked"}, Decision: "rejected", IdempotencyKey: "read-rejected"})
	if err != nil {
		t.Fatalf("record rejected review: %v", err)
	}
	blocked := recordReceipt("blocked-read", "continuation-blocked", []string{rejected.ReviewID})
	assertHidden(blocked.ReceiptID)
	if _, err := store.Continuation(ctx, blocked.ReceiptID); !errors.Is(err, ErrContinuationIneligible) {
		t.Fatalf("blocked continuation error = %v, want ErrContinuationIneligible", err)
	}

	accepted, err := store.RecordReview(ctx, ReviewInput{OrganizationID: scope.OrganizationID, WorkspaceID: scope.WorkspaceID, ProjectID: scope.ProjectID, TaskID: scope.TaskID, JobID: scope.JobID, ReviewerRef: "reviewer-rca", ReviewerVersion: "1.0.0", InputArtifactDigests: []string{"sha256:abc123"}, Checks: map[string]any{"schema": "checked"}, Decision: "accepted", IdempotencyKey: "read-accepted"})
	if err != nil {
		t.Fatalf("record accepted review: %v", err)
	}
	allowed := recordReceipt("accepted-read", "continuation-accepted", []string{accepted.ReviewID})
	loaded, err := store.Receipt(ctx, allowed.ReceiptID)
	if err != nil || loaded.ContinuationID != "continuation-accepted" || loaded.Continuation == nil {
		t.Fatalf("accepted receipt must expose continuation = %#v, %v", loaded, err)
	}
	page, err := store.ListReceipts(ctx, ReceiptQuery{JobID: scope.JobID})
	if err != nil {
		t.Fatalf("list accepted receipt: %v", err)
	}
	listedAccepted := false
	for _, listed := range page.Receipts {
		if listed.ReceiptID == allowed.ReceiptID {
			listedAccepted = listed.ContinuationID == "continuation-accepted" && listed.Continuation != nil
		}
	}
	if !listedAccepted {
		t.Fatal("accepted listed receipt must expose continuation")
	}
	if continuation, err := store.Continuation(ctx, allowed.ReceiptID); err != nil || continuation["continuationId"] != "continuation-accepted" {
		t.Fatalf("accepted continuation = %#v, %v", continuation, err)
	}
}

func TestReviewPolicyRequiresOrganizationAndUsesOperationScopedIdempotency(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	if _, err := store.CreateReviewPolicy(ctx, ReviewPolicyInput{ExecutionIdentity: ExecutionIdentity{WorkspaceID: "workspace-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", JobID: "job-alpha"}, Version: "1", RequiredReviewers: []RequiredReviewer{{ReviewerRef: "reviewer-rca", ReviewerVersion: "1.0.0"}}, IdempotencyKey: "missing-org"}); !errors.Is(err, ErrInvalidReviewPolicyInput) {
		t.Fatalf("missing organization error = %v, want ErrInvalidReviewPolicyInput", err)
	}
	sharedKey := "operation-scoped-key"
	if _, err := store.RecordReceipt(ctx, ReceiptInput{Type: "execution.receipt.v1", Status: "completed", Surface: "workspace", WorkspaceID: "workspace-alpha", IdempotencyKey: sharedKey}); err != nil {
		t.Fatalf("record receipt: %v", err)
	}
	if _, err := store.CreateReviewPolicy(ctx, ReviewPolicyInput{ExecutionIdentity: ExecutionIdentity{OrganizationID: "org-alpha", WorkspaceID: "workspace-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", JobID: "job-alpha"}, Version: "1", RequiredReviewers: []RequiredReviewer{{ReviewerRef: "reviewer-rca", ReviewerVersion: "1.0.0"}}, IdempotencyKey: sharedKey}); err != nil {
		t.Fatalf("policy idempotency must be operation scoped: %v", err)
	}
}

func TestArtifactAndReviewRequireOrganization(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	if _, err := store.RecordArtifact(ctx, ArtifactInput{WorkspaceID: "workspace-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", JobID: "job-alpha", Digest: "sha256:abc", MediaType: "application/json", StorageRef: "artifact-ref", IdempotencyKey: "artifact-missing-org"}); !errors.Is(err, ErrInvalidArtifactInput) {
		t.Fatalf("artifact error = %v", err)
	}
	if _, err := store.RecordReview(ctx, ReviewInput{WorkspaceID: "workspace-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", JobID: "job-alpha", ReviewerRef: "reviewer", ReviewerVersion: "1", InputArtifactDigests: []string{"sha256:abc"}, Checks: map[string]any{"ok": true}, Decision: "accepted", IdempotencyKey: "review-missing-org"}); !errors.Is(err, ErrInvalidReviewInput) {
		t.Fatalf("review error = %v", err)
	}
}

func TestListReceiptsFiltersAndPaginatesNewestFirst(t *testing.T) {
	store := NewMemoryStore()
	createdAt := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	store.receipts = map[string]Receipt{
		"receipt-a":     {ReceiptInput: ReceiptInput{Type: "execution.receipt.v1", Status: "completed", OrganizationID: "org-alpha", WorkspaceID: "ws-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", JobID: "job-alpha"}, ReceiptID: "receipt-a", CreatedAt: createdAt.Add(-time.Minute)},
		"receipt-b":     {ReceiptInput: ReceiptInput{Type: "execution.receipt.v1", Status: "completed", OrganizationID: "org-alpha", WorkspaceID: "ws-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", JobID: "job-alpha"}, ReceiptID: "receipt-b", CreatedAt: createdAt},
		"receipt-c":     {ReceiptInput: ReceiptInput{Type: "review.result.v1", Status: "review_blocked", OrganizationID: "org-alpha", WorkspaceID: "ws-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", JobID: "job-alpha"}, ReceiptID: "receipt-c", CreatedAt: createdAt},
		"receipt-other": {ReceiptInput: ReceiptInput{Type: "execution.receipt.v1", Status: "completed", OrganizationID: "org-other", WorkspaceID: "ws-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", JobID: "job-alpha"}, ReceiptID: "receipt-other", CreatedAt: createdAt.Add(time.Minute)},
	}

	first, err := store.ListReceipts(context.Background(), ReceiptQuery{OrganizationID: "org-alpha", WorkspaceID: "ws-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", JobID: "job-alpha", Type: "execution.receipt.v1", Status: "completed", Limit: 1})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first.Receipts) != 1 || first.Receipts[0].ReceiptID != "receipt-b" || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("first page = %#v", first)
	}
	second, err := store.ListReceipts(context.Background(), ReceiptQuery{OrganizationID: "org-alpha", WorkspaceID: "ws-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", JobID: "job-alpha", Type: "execution.receipt.v1", Status: "completed", Limit: 1, Cursor: first.NextCursor})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second.Receipts) != 1 || second.Receipts[0].ReceiptID != "receipt-a" || second.HasMore || second.NextCursor != "" {
		t.Fatalf("second page = %#v", second)
	}
}

func TestListReceiptsRejectsInvalidBoundsAndCursor(t *testing.T) {
	store := NewMemoryStore()
	for _, query := range []ReceiptQuery{{Limit: -1}, {Limit: 101}, {Cursor: "not-a-cursor"}} {
		if _, err := store.ListReceipts(context.Background(), query); !errors.Is(err, ErrInvalidReceiptQuery) {
			t.Fatalf("query %#v error = %v, want ErrInvalidReceiptQuery", query, err)
		}
	}
}
