package clients

import (
	"bytes"
	"context"
	"encoding/base64"
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

func TestLedgerReceiptListSendsTypePrefix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("typePrefix") != "billing." {
			t.Fatalf("typePrefix = %q", r.URL.Query().Get("typePrefix"))
		}
		_, _ = w.Write([]byte(`{"receipts":[],"nextCursor":"","hasMore":false}`))
	}))
	defer server.Close()

	client := NewLedgerHTTPClient(server.URL, "internal-secret", server.Client()).(LedgerReceiptListClient)
	if _, err := client.ListReceipts(context.Background(), ReceiptQuery{AccountID: "acct-alpha", TypePrefix: "billing."}); err != nil {
		t.Fatal(err)
	}
}

func TestLedgerScopedDetailCapabilityUsesStructuredPathIdentity(t *testing.T) {
	const capabilityKey = "ledger-capability-key-for-client-tests-32-chars"
	for _, test := range []struct {
		name        string
		workspaceID string
	}{
		{name: "account only"},
		{name: "exact workspace", workspaceID: "ws-alpha"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				claims := ledgerCapabilityClaimsForTest(t, r.Header.Get("X-OPL-Ledger-Capability"))
				if r.URL.Path != "/ledger/receipts/receipt-alpha" || claims["resourceId"] != "receipt-alpha" ||
					claims["accountId"] != "acct-alpha" || claims["workspaceId"] != test.workspaceID {
					t.Fatalf("request=%s?%s claims=%#v", r.URL.Path, r.URL.RawQuery, claims)
				}
				_, _ = w.Write([]byte(`{"receiptId":"receipt-alpha","type":"billing.workspace_purchased.v1","status":"completed","accountId":"acct-alpha","workspaceId":"ws-alpha"}`))
			}))
			defer server.Close()

			client := NewLedgerHTTPClientWithCapability(server.URL, "internal-secret", capabilityKey, server.Client()).(LedgerScopedReceiptClient)
			receipt, err := client.ReceiptForAccount(context.Background(), "acct-alpha", test.workspaceID, "receipt-alpha")
			if err != nil || receipt.ReceiptID != "receipt-alpha" {
				t.Fatalf("receipt=%#v err=%v", receipt, err)
			}
		})
	}

	for _, test := range []struct{ path, want string }{
		{path: "/ledger/receipts/receipt-alpha?accountId=acct-alpha", want: "receipt-alpha"},
	} {
		if got := ledgerResourceID(test.path); got != test.want {
			t.Errorf("ledgerResourceID(%q)=%q want %q", test.path, got, test.want)
		}
	}
}

func ledgerCapabilityClaimsForTest(t *testing.T, raw string) map[string]any {
	t.Helper()
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		t.Fatalf("capability parts=%d", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

func TestLedgerReadinessUsesPublicReadyz(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/readyz" || r.Header.Get("Authorization") != "Bearer internal-secret" {
			t.Fatalf("request = %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	}))
	defer server.Close()

	client := NewLedgerHTTPClient(server.URL, "internal-secret", server.Client()).(LedgerReadinessClient)
	if err := client.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
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
	baseInput := ReceiptInput{
		Status: "completed", Surface: "control_plane", AccountID: "acct-alpha",
		WorkspaceID: "workspace-alpha", RequestID: "workspace-renewal-alpha",
		Execution:      map[string]any{"resourceType": "workspace", "resourceId": "workspace-alpha"},
		InputRefs:      map[string]any{"evidenceRef": "case-alpha"},
		ReviewerChecks: map[string]any{"decision": "activate_charged_resource", "reviewer": "operator-alpha"},
		Cost: map[string]any{
			"priceVersion": "pilot-usd-2026-07-v1", "currency": "USD", "billingUnit": "calendar_month", "totalUsdMicros": int64(52_580_000),
			"periodStart": "2026-08-31T09:30:00Z", "paidThrough": "2026-09-30T09:30:00Z", "resourceType": "workspace", "resourceId": "workspace-alpha",
		},
		Owner: map[string]any{"accountId": "acct-alpha", "workspaceId": "workspace-alpha"},
	}
	tests := []struct {
		name     string
		mutate   func(*Receipt)
		trailing string
		wantErr  bool
	}{
		{name: "valid"},
		{name: "missing receipt ID", mutate: func(receipt *Receipt) { receipt.ReceiptID = "" }, wantErr: true},
		{name: "type mismatch", mutate: func(receipt *Receipt) { receipt.Type = "billing.resource_purchased.v1" }, wantErr: true},
		{name: "account mismatch", mutate: func(receipt *Receipt) { receipt.AccountID = "acct-other" }, wantErr: true},
		{name: "Workspace mismatch", mutate: func(receipt *Receipt) { receipt.WorkspaceID = "workspace-other" }, wantErr: true},
		{name: "request mismatch", mutate: func(receipt *Receipt) { receipt.RequestID = "workspace-renewal-other" }, wantErr: true},
		{name: "cost mismatch", mutate: func(receipt *Receipt) { receipt.Cost["paidThrough"] = "2026-10-31T09:30:00Z" }, wantErr: true},
		{name: "execution mismatch", mutate: func(receipt *Receipt) { receipt.Execution["resourceId"] = "workspace-other" }, wantErr: true},
		{name: "owner mismatch", mutate: func(receipt *Receipt) { receipt.Owner["accountId"] = "acct-other" }, wantErr: true},
		{name: "input refs mismatch", mutate: func(receipt *Receipt) { receipt.InputRefs["evidenceRef"] = "case-other" }, wantErr: true},
		{name: "reviewer checks mismatch", mutate: func(receipt *Receipt) { receipt.ReviewerChecks["reviewer"] = "operator-other" }, wantErr: true},
		{name: "trailing JSON", trailing: ` {"receiptId":"receipt-other"}`, wantErr: true},
	}
	for _, receiptType := range []string{"billing.workspace_renewed.v1", "billing.workspace_expired.v1", "billing.workspace_refunded.v1"} {
		t.Run(receiptType, func(t *testing.T) {
			for _, tc := range tests {
				t.Run(tc.name, func(t *testing.T) {
					input := baseInput
					input.Type = receiptType
					payload, err := json.Marshal(input)
					if err != nil {
						t.Fatal(err)
					}
					response := Receipt{ReceiptID: "receipt-workspace-billing", CreatedAt: "2026-09-01T00:00:00Z"}
					decoder := json.NewDecoder(bytes.NewReader(payload))
					decoder.UseNumber()
					if err := decoder.Decode(&response.ReceiptInput); err != nil {
						t.Fatal(err)
					}
					if tc.mutate != nil {
						tc.mutate(&response)
					}
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.Method != http.MethodPost || r.URL.Path != "/ledger/receipts" || r.Header.Get("Idempotency-Key") != "workspace-receipt-once" {
							t.Fatalf("request=%s %s key=%q", r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"))
						}
						responsePayload, err := json.Marshal(response)
						if err != nil {
							t.Fatal(err)
						}
						_, _ = w.Write(append(responsePayload, tc.trailing...))
					}))
					defer server.Close()

					client := NewLedgerHTTPClient(server.URL, "internal-secret", server.Client())
					result, err := client.RecordReceipt(context.Background(), input, "workspace-receipt-once")
					if (err != nil) != tc.wantErr {
						t.Fatalf("result=%#v err=%v", result, err)
					}
				})
			}
		})
	}
}

func TestLedgerHTTPClientValidatesWorkspaceLifecycleReceiptResponse(t *testing.T) {
	baseExecution := map[string]any{
		"operationId": "workspace-operation-alpha", "resourceType": "workspace", "resourceId": "workspace-alpha",
		"computeAllocationId": "compute-alpha", "storageId": "storage-alpha", "attachmentId": "attachment-alpha", "runtimeId": "runtime-alpha",
		"workspaceApiKeyId": int64(9), "workspaceKeyFingerprint": "sha256:alpha", "runtimeServiceName": "runtime-alpha", "gatewaySecretRef": "secret-alpha",
	}
	inputs := []ReceiptInput{
		{
			Type: "workspace.created", Status: "completed", Surface: "control_plane", AccountID: "acct-alpha", WorkspaceID: "workspace-alpha",
			RequestID: "workspace-operation-alpha", Execution: baseExecution,
			Owner: map[string]any{"accountId": "acct-alpha", "workspaceId": "workspace-alpha", "ownerUserId": "usr-alpha"},
		},
		{
			Type: "workspace.deleted.v1", Status: "completed", Surface: "control_plane", AccountID: "acct-alpha", WorkspaceID: "workspace-alpha",
			RequestID: "workspace-operation-alpha", InputRefs: map[string]any{"launchReceiptId": "receipt-launch-alpha"}, Execution: baseExecution,
			OutputRefs: map[string]any{
				"runtimeStatus": "absent", "gatewaySecretStatus": "absent", "attachmentStatus": "absent", "storageStatus": "absent",
				"computeStatus": "absent", "workspaceKeyStatus": "absent", "workspaceStatus": "absent",
			},
			Owner: map[string]any{"accountId": "acct-alpha", "workspaceId": "workspace-alpha", "ownerUserId": "usr-alpha"},
		},
	}
	for _, input := range inputs {
		for _, test := range []struct {
			name    string
			mutate  func(*Receipt)
			wantErr bool
		}{
			{name: "valid"},
			{name: "type", mutate: func(receipt *Receipt) { receipt.Type = "execution.receipt.v1" }, wantErr: true},
			{name: "request", mutate: func(receipt *Receipt) { receipt.RequestID = "workspace-operation-other" }, wantErr: true},
			{name: "input refs", mutate: func(receipt *Receipt) { receipt.InputRefs = map[string]any{"launchReceiptId": "receipt-other"} }, wantErr: true},
			{name: "execution", mutate: func(receipt *Receipt) { receipt.Execution["runtimeId"] = "runtime-other" }, wantErr: true},
			{name: "owner", mutate: func(receipt *Receipt) { receipt.Owner["ownerUserId"] = "usr-other" }, wantErr: true},
			{name: "output", mutate: func(receipt *Receipt) { receipt.OutputRefs = map[string]any{"workspaceStatus": "present"} }, wantErr: true},
			{name: "cost", mutate: func(receipt *Receipt) { receipt.Cost = map[string]any{"totalUsdMicros": int64(1)} }, wantErr: true},
			{name: "supersedes", mutate: func(receipt *Receipt) { receipt.SupersedesReceiptID = "receipt-other" }, wantErr: true},
		} {
			t.Run(input.Type+"/"+test.name, func(t *testing.T) {
				payload, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				response := Receipt{ReceiptID: "receipt-workspace-lifecycle", CreatedAt: "2026-08-19T00:00:00Z"}
				decoder := json.NewDecoder(bytes.NewReader(payload))
				decoder.UseNumber()
				if err := decoder.Decode(&response.ReceiptInput); err != nil {
					t.Fatal(err)
				}
				if test.mutate != nil {
					test.mutate(&response)
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_ = json.NewEncoder(w).Encode(response)
				}))
				defer server.Close()
				client := NewLedgerHTTPClient(server.URL, "internal-secret", server.Client())
				result, err := client.RecordReceipt(context.Background(), input, "workspace-operation-alpha:receipt")
				if (err != nil) != test.wantErr {
					t.Fatalf("result=%#v err=%v", result, err)
				}
			})
		}
	}
}

func TestWalletAdjustmentReceipt(t *testing.T) {
	input := ReceiptInput{
		Type: "gateway.wallet_adjustment.v1", Status: "completed", Surface: "control_plane", AccountID: "acct-alpha",
		RequestID: "wallet-adjustment-alpha",
		Actor:     map[string]any{"userId": "usr-admin"},
		Execution: map[string]any{"operationId": "wallet-adjustment-alpha", "kind": "debit", "amountUsdMicros": int64(2_500_000)},
		InputRefs: map[string]any{"balanceHistoryRef": "sub2api:balance-history:41:history-alpha"},
		Owner:     map[string]any{"accountId": "acct-alpha"},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"receiptId":"receipt-wallet","type":"gateway.wallet_adjustment.v1","status":"completed","surface":"control_plane","accountId":"acct-alpha","requestId":"wallet-adjustment-alpha"}`)
	}))
	defer server.Close()

	client := NewLedgerHTTPClient(server.URL, "internal-secret", server.Client())
	if _, err := client.RecordReceipt(context.Background(), input, "wallet-adjustment-alpha:receipt"); err == nil {
		t.Fatal("incomplete wallet adjustment receipt response was accepted")
	}
}

func TestLedgerHTTPClientValidatesWorkspaceGatewayKeyRotationReceiptResponse(t *testing.T) {
	input := ReceiptInput{
		Type: "workspace.gateway_key_rotated.v1", Status: "completed", Surface: "control_plane",
		AccountID: "acct-alpha", WorkspaceID: "workspace-alpha",
		Execution:  map[string]any{"operationId": "workspace-key-rotate-alpha", "oldKeyId": int64(9), "newKeyId": int64(19)},
		OutputRefs: map[string]any{"secretFingerprint": "sha256:replacement"},
		Owner:      map[string]any{"userId": "usr-alpha"},
	}
	for _, tc := range []struct {
		name    string
		mutate  func(*Receipt)
		wantErr bool
	}{
		{name: "valid"},
		{name: "missing receipt ID", mutate: func(receipt *Receipt) { receipt.ReceiptID = "" }, wantErr: true},
		{name: "changed Key", mutate: func(receipt *Receipt) { receipt.Execution["newKeyId"] = int64(20) }, wantErr: true},
		{name: "changed fingerprint", mutate: func(receipt *Receipt) { receipt.OutputRefs["secretFingerprint"] = "sha256:other" }, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			response := Receipt{ReceiptID: "receipt-workspace-key-rotation", CreatedAt: "2026-07-19T00:00:00Z"}
			decoder := json.NewDecoder(bytes.NewReader(payload))
			decoder.UseNumber()
			if err := decoder.Decode(&response.ReceiptInput); err != nil {
				t.Fatal(err)
			}
			if tc.mutate != nil {
				tc.mutate(&response)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			client := NewLedgerHTTPClient(server.URL, "internal-secret", server.Client())
			result, err := client.RecordReceipt(context.Background(), input, "workspace-key-rotate-alpha:receipt")
			if (err != nil) != tc.wantErr {
				t.Fatalf("result=%#v err=%v", result, err)
			}
		})
	}
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
