package clients

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestLedgerReceiptList(t *testing.T) {
	t.Run("sends scoped pagination and authorization", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/ledger/receipts" || r.URL.RawQuery != "accountId=acct-alpha&cursor=opaque&limit=50" {
				t.Fatalf("request = %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer internal-secret" {
				t.Fatalf("authorization = %q", got)
			}
			_, _ = w.Write([]byte(`{"receipts":[{"receiptId":"receipt-1","type":"billing.resource_purchased.v1","status":"completed","accountId":"acct-alpha","workspaceId":"ws-alpha","createdAt":"2026-07-16T00:00:00Z"}],"nextCursor":"next","hasMore":true}`))
		}))
		defer server.Close()

		client := NewLedgerHTTPClient(server.URL, "internal-secret", server.Client()).(LedgerReceiptListClient)
		page, err := client.ListReceipts(context.Background(), ReceiptQuery{AccountID: "acct-alpha", Cursor: "opaque", Limit: 50})
		if err != nil || len(page.Receipts) != 1 || page.Receipts[0].ReceiptID != "receipt-1" || page.NextCursor != "next" || !page.HasMore {
			t.Fatalf("page = %#v err=%v", page, err)
		}
	})

	t.Run("rejects invalid limit before HTTP", func(t *testing.T) {
		var called atomic.Bool
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			called.Store(true)
		}))
		defer server.Close()

		client := NewLedgerHTTPClient(server.URL, "internal-secret", server.Client()).(LedgerReceiptListClient)
		if _, err := client.ListReceipts(context.Background(), ReceiptQuery{AccountID: "acct-alpha", Limit: 101}); err == nil {
			t.Fatal("expected invalid limit error")
		}
		if called.Load() {
			t.Fatal("invalid query reached Ledger")
		}
	})

	t.Run("rejects response over one MiB without echoing body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"receipts":[],"padding":"` + strings.Repeat("sensitive", 1<<17) + `"}`))
		}))
		defer server.Close()

		client := NewLedgerHTTPClient(server.URL, "internal-secret", server.Client()).(LedgerReceiptListClient)
		_, err := client.ListReceipts(context.Background(), ReceiptQuery{AccountID: "acct-alpha"})
		if err == nil || !strings.Contains(err.Error(), "response too large") || strings.Contains(err.Error(), "sensitive") {
			t.Fatalf("bounded response error = %v", err)
		}
	})
}

func TestLedgerHTTPClientReturnsBoundedErrorForFailedEvidenceWrite(t *testing.T) {
	const secretMarker = "LEDGER_BODY_SECRET_MUST_NOT_LEAK"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer internal-secret" || r.URL.Path != "/ledger/reconciliation" {
			t.Fatalf("request = %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		http.Error(w, secretMarker+strings.Repeat("x", 70<<10), http.StatusConflict)
	}))
	defer server.Close()

	client := NewLedgerHTTPClient(server.URL, "internal-secret", server.Client())
	_, err := client.RecordReconciliation(context.Background(), ReconciliationInput{Report: map[string]any{"id": "report-1"}}, "report-once")
	if err == nil || !strings.Contains(err.Error(), "status 409") || strings.Contains(err.Error(), secretMarker) || len(err.Error()) > 66<<10 {
		t.Fatalf("bounded status error = %v", err)
	}
}

func TestLedgerRecordReconciliationValidatesResponse(t *testing.T) {
	report := map[string]any{
		"id": "report-1", "status": "ok",
		"counts":     map[string]any{"billingOperations": 1, "matched": 1, "exceptions": 0},
		"exceptions": []any{},
	}
	validReport := `{"id":"report-1","status":"ok","counts":{"billingOperations":1,"matched":1,"exceptions":0},"exceptions":[]}`
	for _, test := range []struct {
		name      string
		response  string
		wantError bool
	}{
		{name: "valid", response: `{"id":"report-1","status":"ok","report":` + validReport + `,"blockNewWorkspaces":false,"reason":"operator_reconciliation"}`},
		{name: "changed report", response: `{"id":"report-1","status":"ok","report":{"id":"report-1","status":"ok","counts":{"billingOperations":1,"matched":0,"exceptions":0},"exceptions":[]},"blockNewWorkspaces":false,"reason":"operator_reconciliation"}`, wantError: true},
		{name: "id mismatch", response: `{"id":"report-other","status":"ok","report":` + validReport + `,"blockNewWorkspaces":false,"reason":"operator_reconciliation"}`, wantError: true},
		{name: "status mismatch", response: `{"id":"report-1","status":"mismatch","report":` + validReport + `,"blockNewWorkspaces":false,"reason":"operator_reconciliation"}`, wantError: true},
		{name: "wrong block guard", response: `{"id":"report-1","status":"ok","report":` + validReport + `,"blockNewWorkspaces":true,"reason":"operator_reconciliation"}`, wantError: true},
		{name: "missing block guard", response: `{"id":"report-1","status":"ok","report":` + validReport + `,"reason":"operator_reconciliation"}`, wantError: true},
		{name: "wrong reason", response: `{"id":"report-1","status":"ok","report":` + validReport + `,"blockNewWorkspaces":false,"reason":"automatic_repair"}`, wantError: true},
		{name: "missing report", response: `{"id":"report-1","status":"ok","blockNewWorkspaces":false,"reason":"operator_reconciliation"}`, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/ledger/reconciliation" || r.Header.Get("Idempotency-Key") != "report-once" {
					t.Fatalf("request = %s key=%q", r.URL.Path, r.Header.Get("Idempotency-Key"))
				}
				_, _ = w.Write([]byte(test.response))
			}))
			defer server.Close()

			client := NewLedgerHTTPClient(server.URL, "internal-secret", server.Client())
			result, err := client.RecordReconciliation(context.Background(), ReconciliationInput{Report: report}, "report-once")
			if (err != nil) != test.wantError {
				t.Fatalf("result = %#v, error = %v", result, err)
			}
		})
	}
}

func TestLedgerRecordReconciliationPreservesLargeReportIntegers(t *testing.T) {
	for _, count := range []int64{1<<53 + 1, math.MaxInt64} {
		t.Run(strconv.FormatInt(count, 10), func(t *testing.T) {
			report := map[string]any{
				"id": "report-large-integer", "status": "ok",
				"counts": map[string]any{"billingOperations": count},
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id": "report-large-integer", "status": "ok", "report": report,
					"blockNewWorkspaces": false, "reason": "operator_reconciliation",
				})
			}))
			defer server.Close()

			client := NewLedgerHTTPClient(server.URL, "internal-secret", server.Client())
			result, err := client.RecordReconciliation(context.Background(), ReconciliationInput{Report: report}, "report-once")
			if err != nil {
				t.Fatalf("RecordReconciliation: %v", err)
			}
			assertExactLedgerNumber(t, result.Report["counts"].(map[string]any)["billingOperations"], count)
		})
	}
}

func TestLedgerHTTPClientPreservesLargeReceiptCostIntegers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/ledger/receipts":
			_, _ = fmt.Fprint(w, `{"receiptId":"receipt-write","workspaceId":"workspace-alpha","cost":{"chargeUsdMicros":9007199254740993}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/ledger/receipts":
			_, _ = fmt.Fprint(w, `{"receipts":[{"receiptId":"receipt-list","workspaceId":"workspace-alpha","cost":{"chargeUsdMicros":9223372036854775807}}],"hasMore":false}`)
		case r.Method == http.MethodGet && r.URL.Path == "/ledger/receipts/receipt-readback":
			_, _ = fmt.Fprint(w, `{"receiptId":"receipt-readback","workspaceId":"workspace-alpha","cost":{"chargeUsdMicros":9007199254740993}}`)
		default:
			t.Fatalf("unexpected Ledger request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := NewLedgerHTTPClient(server.URL, "internal-secret", server.Client())
	t.Run("record", func(t *testing.T) {
		receipt, err := client.RecordReceipt(context.Background(), ReceiptInput{WorkspaceID: "workspace-alpha"}, "receipt-once")
		if err != nil {
			t.Fatal(err)
		}
		assertExactLedgerNumber(t, receipt.Cost["chargeUsdMicros"], 1<<53+1)
	})
	t.Run("list", func(t *testing.T) {
		page, err := client.(LedgerReceiptListClient).ListReceipts(context.Background(), ReceiptQuery{AccountID: "acct-alpha"})
		if err != nil || len(page.Receipts) != 1 {
			t.Fatalf("ListReceipts = %#v err=%v", page, err)
		}
		assertExactLedgerNumber(t, page.Receipts[0].Cost["chargeUsdMicros"], math.MaxInt64)
	})
	t.Run("readback", func(t *testing.T) {
		receipt, err := client.Receipt(context.Background(), "receipt-readback")
		if err != nil {
			t.Fatal(err)
		}
		assertExactLedgerNumber(t, receipt.Cost["chargeUsdMicros"], 1<<53+1)
	})
}

func TestLedgerHTTPClientRejectsTrailingResponseData(t *testing.T) {
	for _, test := range []struct {
		name     string
		trailing string
	}{
		{name: "second JSON document", trailing: ` {"receiptId":"receipt-second"}`},
		{name: "garbage", trailing: ` trailing-garbage`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, `{"receiptId":"receipt-first","workspaceId":"workspace-alpha"}`+test.trailing)
			}))
			defer server.Close()

			client := NewLedgerHTTPClient(server.URL, "internal-secret", server.Client())
			if _, err := client.RecordReceipt(context.Background(), ReceiptInput{WorkspaceID: "workspace-alpha"}, "receipt-once"); err == nil {
				t.Fatal("trailing response data was accepted")
			}
		})
	}
}

func TestLedgerHTTPClientValidatesWorkspaceBillingReceiptResponse(t *testing.T) {
	input := ReceiptInput{
		Type: "billing.workspace_renewed.v1", Status: "completed", Surface: "control_plane", AccountID: "acct-alpha",
		WorkspaceID: "workspace-alpha", RequestID: "workspace-renewal-alpha",
		Cost: map[string]any{
			"priceVersion": "pilot-usd-2026-07-v1", "currency": "USD", "billingUnit": "calendar_month", "totalUsdMicros": int64(52_580_000),
			"periodStart": "2026-08-31T09:30:00Z", "paidThrough": "2026-09-30T09:30:00Z", "resourceType": "workspace", "resourceId": "workspace-alpha",
		},
	}
	for _, tc := range []struct {
		name     string
		mutate   func(*Receipt)
		trailing string
		wantErr  bool
	}{
		{name: "valid"},
		{name: "missing receipt ID", mutate: func(receipt *Receipt) { receipt.ReceiptID = "" }, wantErr: true},
		{name: "type mismatch", mutate: func(receipt *Receipt) { receipt.Type = "billing.workspace_expired.v1" }, wantErr: true},
		{name: "account mismatch", mutate: func(receipt *Receipt) { receipt.AccountID = "acct-other" }, wantErr: true},
		{name: "Workspace mismatch", mutate: func(receipt *Receipt) { receipt.WorkspaceID = "workspace-other" }, wantErr: true},
		{name: "request mismatch", mutate: func(receipt *Receipt) { receipt.RequestID = "workspace-renewal-other" }, wantErr: true},
		{name: "cost mismatch", mutate: func(receipt *Receipt) { receipt.Cost["paidThrough"] = "2026-10-31T09:30:00Z" }, wantErr: true},
		{name: "trailing JSON", trailing: ` {"receiptId":"receipt-other"}`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := Receipt{ReceiptInput: input, ReceiptID: "receipt-workspace-renewal", CreatedAt: "2026-09-01T00:00:00Z"}
			response.Cost = cloneLedgerMap(input.Cost)
			if tc.mutate != nil {
				tc.mutate(&response)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/ledger/receipts" || r.Header.Get("Idempotency-Key") != "workspace-receipt-once" {
					t.Fatalf("request=%s %s key=%q", r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"))
				}
				payload, err := json.Marshal(response)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = w.Write(append(payload, tc.trailing...))
			}))
			defer server.Close()

			client := NewLedgerHTTPClient(server.URL, "internal-secret", server.Client())
			result, err := client.RecordReceipt(context.Background(), input, "workspace-receipt-once")
			if (err != nil) != tc.wantErr {
				t.Fatalf("result=%#v err=%v", result, err)
			}
		})
	}
}

func cloneLedgerMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func assertExactLedgerNumber(t *testing.T, value any, want int64) {
	t.Helper()
	number, ok := value.(json.Number)
	if !ok {
		t.Fatalf("Ledger number type = %T (%v), want json.Number", value, value)
	}
	got, err := number.Int64()
	if err != nil || got != want {
		t.Fatalf("Ledger number = %q parsed=%d err=%v, want %d", number, got, err, want)
	}
}

func TestLedgerHTTPClientReadsReceiptContinuationIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(Receipt{
			ReceiptInput: ReceiptInput{Status: "completed", WorkspaceID: "workspace-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", JobID: "job-alpha", Execution: map[string]any{"jobStatus": "succeeded"}},
			ReceiptID:    "receipt-alpha", ContinuationID: "continuation-alpha",
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
