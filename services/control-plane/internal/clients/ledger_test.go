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
		_ = json.NewEncoder(w).Encode(Receipt{ReceiptID: "receipt-alpha", WorkspaceID: "workspace-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", JobID: "job-alpha", ContinuationID: "continuation-alpha"})
	}))
	defer server.Close()

	client := NewLedgerHTTPClient(server.URL, server.Client())
	receipt, err := client.RecordReceipt(context.Background(), ReceiptInput{Type: "execution.receipt.v1", Status: "running", Surface: "workspace", WorkspaceID: "workspace-alpha"}, "receipt-once")
	if err != nil || receipt.ReceiptID != "receipt-alpha" || receipt.ContinuationID != "continuation-alpha" {
		t.Fatalf("receipt = %#v err=%v", receipt, err)
	}
}
