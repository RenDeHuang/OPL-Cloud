package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"opl-cloud/services/control-plane/internal/clients"
)

type customerFactsLedger struct {
	fakeLedgerClient
	page       clients.ReceiptPage
	listErr    error
	query      clients.ReceiptQuery
	receipt    clients.Receipt
	receiptErr error
}

func (l *customerFactsLedger) ListReceipts(_ context.Context, query clients.ReceiptQuery) (clients.ReceiptPage, error) {
	l.query = query
	return l.page, l.listErr
}

func (l *customerFactsLedger) Receipt(_ context.Context, receiptID string) (clients.Receipt, error) {
	result := l.receipt
	result.ReceiptID = receiptID
	return result, l.receiptErr
}

func TestBillingReceiptListTenantProjection(t *testing.T) {
	billing := customerBillingReceipt()
	ledger := &customerFactsLedger{page: clients.ReceiptPage{
		Receipts: []clients.Receipt{
			billing,
			{ReceiptInput: clients.ReceiptInput{Type: "execution.receipt.v1", AccountID: "acct-alpha"}, ReceiptID: "receipt-not-billing"},
		},
		NextCursor: "next-page",
		HasMore:    true,
	}}
	server := NewServer(newTestService(ledger, &fakeFabricClient{}))
	session := tenantAdminSessionForTest(t, server)

	response := requestWithSession(t, server, session, http.MethodGet, "/api/billing/receipts?cursor=opaque&limit=50", "")
	if response.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", response.Code, response.Body.String())
	}
	if ledger.query != (clients.ReceiptQuery{AccountID: "acct-alpha", Cursor: "opaque", Limit: 50}) {
		t.Fatalf("Ledger query = %#v", ledger.query)
	}
	var page map[string]any
	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	items, _ := page["receipts"].([]any)
	if len(items) != 1 || page["nextCursor"] != "next-page" || page["hasMore"] != true {
		t.Fatalf("projected page = %#v", page)
	}
	assertCustomerBillingReceipt(t, items[0].(map[string]any))
}

func TestBillingReceiptListRejectsTenantMismatch(t *testing.T) {
	receipt := customerBillingReceipt()
	receipt.AccountID = "acct-beta"
	ledger := &customerFactsLedger{page: clients.ReceiptPage{Receipts: []clients.Receipt{receipt}}}
	server := NewServer(newTestService(ledger, &fakeFabricClient{}))

	response := requestWithSession(t, server, tenantAdminSessionForTest(t, server), http.MethodGet, "/api/billing/receipts", "")
	assertErrorResponse(t, response.Code, response.Body.String(), http.StatusBadGateway, "billing_receipt_identity_mismatch")
}

func TestBillingReceiptDetailProjection(t *testing.T) {
	ledger := &customerFactsLedger{receipt: customerBillingReceipt()}
	server := NewServer(newTestService(ledger, &fakeFabricClient{}))

	response := requestWithSession(t, server, tenantAdminSessionForTest(t, server), http.MethodGet, "/api/billing/receipts/receipt-1", "")
	if response.Code != http.StatusOK {
		t.Fatalf("detail status = %d: %s", response.Code, response.Body.String())
	}
	var receipt map[string]any
	if err := json.NewDecoder(response.Body).Decode(&receipt); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	assertCustomerBillingReceipt(t, receipt)
}

func TestBillingReceiptProjectionRejectsMalformedMoney(t *testing.T) {
	receipt := customerBillingReceipt()
	receipt.Cost["chargeUsdMicros"] = 1.5
	ledger := &customerFactsLedger{page: clients.ReceiptPage{Receipts: []clients.Receipt{receipt}}}
	server := NewServer(newTestService(ledger, &fakeFabricClient{}))

	response := requestWithSession(t, server, tenantAdminSessionForTest(t, server), http.MethodGet, "/api/billing/receipts", "")
	assertErrorResponse(t, response.Code, response.Body.String(), http.StatusBadGateway, "billing_receipt_source_unavailable")
}

func TestBillingReceiptListUnavailableDoesNotAffectSummary(t *testing.T) {
	ledger := &customerFactsLedger{listErr: errors.New("Ledger unavailable")}
	server := NewServer(newTestService(ledger, &fakeFabricClient{}))
	session := tenantAdminSessionForTest(t, server)

	list := requestWithSession(t, server, session, http.MethodGet, "/api/billing/receipts", "")
	assertErrorResponse(t, list.Code, list.Body.String(), http.StatusBadGateway, "upstream_unavailable")
	summary := requestWithSession(t, server, session, http.MethodGet, "/api/billing/summary", "")
	if summary.Code != http.StatusOK {
		t.Fatalf("summary status after Ledger failure = %d: %s", summary.Code, summary.Body.String())
	}
}

func customerBillingReceipt() clients.Receipt {
	return clients.Receipt{
		ReceiptInput: clients.ReceiptInput{
			Type:        "billing.resource_purchased.v1",
			Status:      "completed",
			AccountID:   "acct-alpha",
			WorkspaceID: "ws-alpha",
			Plan:        map[string]any{"secret": "plan-secret"},
			Execution:   map[string]any{"providerPayload": "provider-secret"},
			Environment: map[string]any{"credential": "runtime-secret"},
			InputRefs:   map[string]any{"sub2apiResponse": "sub2api-secret"},
			Cost: map[string]any{
				"resourceType": "compute", "resourceId": "compute-alpha", "pricingVersion": "pricing-v1",
				"monthlyPriceCnyCents": int64(35000), "chargeUsdMicros": int64(50_000_000),
				"periodStart": "2026-07-16T00:00:00Z", "paidThrough": "2026-08-16T00:00:00Z",
				"sub2apiRedeemCode": "redeem-secret", "rawProviderPayload": "provider-secret",
			},
			Owner: map[string]any{"credential": "owner-secret"},
		},
		ReceiptID: "receipt-1",
		CreatedAt: "2026-07-16T00:00:00Z",
	}
}

func assertCustomerBillingReceipt(t *testing.T, receipt map[string]any) {
	t.Helper()
	allowed := map[string]bool{
		"receiptId": true, "type": true, "status": true, "workspaceId": true, "createdAt": true,
		"resourceType": true, "resourceId": true, "pricingVersion": true, "monthlyPriceCnyCents": true,
		"chargeUsdMicros": true, "periodStart": true, "paidThrough": true,
	}
	if len(receipt) != len(allowed) || receipt["receiptId"] != "receipt-1" || receipt["chargeUsdMicros"] != float64(50_000_000) {
		t.Fatalf("billing receipt = %#v", receipt)
	}
	for key := range receipt {
		if !allowed[key] {
			t.Fatalf("unsafe billing field %q in %#v", key, receipt)
		}
	}
}

func assertErrorResponse(t *testing.T, status int, body string, wantStatus int, wantCode string) {
	t.Helper()
	if status != wantStatus {
		t.Fatalf("status = %d, want %d: %s", status, wantStatus, body)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(body), &payload); err != nil || payload["error"] != wantCode {
		t.Fatalf("error body = %s, want %q", body, wantCode)
	}
}
