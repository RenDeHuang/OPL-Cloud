package clients

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLedgerHTTPClientReturnsErrorForFailedMutation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ledger/holds/release" {
			t.Fatalf("path = %s, want /ledger/holds/release", r.URL.Path)
		}
		http.Error(w, "insufficient frozen balance", http.StatusConflict)
	}))
	defer server.Close()

	client := NewLedgerHTTPClient(server.URL, server.Client())
	_, err := client.ReleaseHold(context.Background(), HoldReleaseInput{AccountID: "acct-alpha", AmountCents: 1000, Currency: "CNY"}, "release-once")
	if err == nil || !strings.Contains(err.Error(), "status 409") {
		t.Fatalf("expected status error, got %v", err)
	}
}

func TestLedgerHTTPClientReadsWalletAndSettlementFacts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ledger/accounts/acct-alpha/wallet":
			_ = json.NewEncoder(w).Encode(Wallet{AccountID: "acct-alpha", BalanceCents: 9900, Currency: "CNY"})
		case "/ledger/accounts/acct-alpha/resource-settlements":
			_ = json.NewEncoder(w).Encode([]ResourceSettlementResult{{ID: "settlement-alpha", AccountID: "acct-alpha", PriceSnapshot: map[string]any{"unitPriceCents": 1200}}})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewLedgerHTTPClient(server.URL, server.Client())
	wallet, err := client.Wallet(context.Background(), "acct-alpha")
	if err != nil || wallet.BalanceCents != 9900 {
		t.Fatalf("wallet = %#v err=%v", wallet, err)
	}
	settlements, err := client.ListResourceSettlements(context.Background(), "acct-alpha")
	if err != nil || len(settlements) != 1 || settlements[0].PriceSnapshot["unitPriceCents"] != float64(1200) {
		t.Fatalf("settlements = %#v err=%v", settlements, err)
	}
}

func TestLedgerHTTPClientReadsReceiptContinuationIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Receipt{ReceiptID: "receipt-alpha", Status: "completed", WorkspaceID: "workspace-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", JobID: "job-alpha", ContinuationID: "continuation-alpha", Execution: map[string]any{"jobStatus": "succeeded", "attempt": float64(1)}})
	}))
	defer server.Close()

	client := NewLedgerHTTPClient(server.URL, server.Client())
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
			_ = json.NewEncoder(w).Encode(map[string]any{"continuationId": "continuation-alpha", "receiptId": "receipt-alpha"})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewLedgerHTTPClient(server.URL, server.Client())
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
