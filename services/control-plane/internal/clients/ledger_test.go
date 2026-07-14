package clients

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLedgerHTTPClientReturnsBoundedErrorForFailedEvidenceWrite(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer internal-secret" || r.URL.Path != "/ledger/reconciliation" {
			t.Fatalf("request = %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		http.Error(w, strings.Repeat("x", 70<<10), http.StatusConflict)
	}))
	defer server.Close()

	client := NewLedgerHTTPClient(server.URL, "internal-secret", server.Client())
	_, err := client.RecordReconciliation(context.Background(), ReconciliationInput{Report: map[string]any{"id": "report-1"}}, "report-once")
	if err == nil || !strings.Contains(err.Error(), "status 409") || len(err.Error()) > 66<<10 {
		t.Fatalf("bounded status error = %v", err)
	}
}

func TestLedgerHTTPClientReadsReceiptContinuationIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(Receipt{
			ReceiptInput: ReceiptInput{Status: "completed", WorkspaceID: "workspace-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", JobID: "job-alpha", Execution: map[string]any{"jobStatus": "succeeded"}},
			ReceiptID: "receipt-alpha", ContinuationID: "continuation-alpha",
		})
	}))
	defer server.Close()

	client := NewLedgerHTTPClient(server.URL, "internal-secret", server.Client())
	receipt, err := client.RecordReceipt(context.Background(), ReceiptInput{Type: "execution.receipt.v1", Status: "running", Surface: "workspace", WorkspaceID: "workspace-alpha"}, "receipt-once")
	if err != nil || receipt.ReceiptID != "receipt-alpha" || receipt.ContinuationID != "continuation-alpha" {
		t.Fatalf("receipt = %#v err=%v", receipt, err)
	}
	loaded, err := client.Receipt(context.Background(), "receipt-alpha")
	if err != nil || loaded.Status != "completed" || loaded.Execution["jobStatus"] != "succeeded" {
		t.Fatalf("loaded receipt = %#v err=%v", loaded, err)
	}
}

func TestLedgerHTTPClientReadsReviewAndContinuation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ledger/artifacts/artifact-alpha":
			_ = json.NewEncoder(w).Encode(Artifact{ArtifactID: "artifact-alpha", JobID: "job-alpha", Digest: "sha256:alpha"})
		case "/ledger/reviews/review-alpha":
			_ = json.NewEncoder(w).Encode(Review{ReviewID: "review-alpha", Decision: "accepted", InputArtifactDigests: []string{"sha256:alpha"}})
		case "/ledger/receipts/receipt-alpha/continuation":
			_ = json.NewEncoder(w).Encode(map[string]any{"continuationId": "continuation-alpha"})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewLedgerHTTPClient(server.URL, "internal-secret", server.Client())
	artifact, err := client.Artifact(context.Background(), "artifact-alpha")
	if err != nil || artifact.JobID != "job-alpha" || artifact.Digest != "sha256:alpha" {
		t.Fatalf("artifact = %#v err=%v", artifact, err)
	}
	review, err := client.Review(context.Background(), "review-alpha")
	if err != nil || review.Decision != "accepted" || len(review.InputArtifactDigests) != 1 {
		t.Fatalf("review = %#v err=%v", review, err)
	}
	continuation, err := client.Continuation(context.Background(), "receipt-alpha")
	if err != nil || continuation["continuationId"] != "continuation-alpha" {
		t.Fatalf("continuation = %#v err=%v", continuation, err)
	}
}
