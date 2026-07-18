package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"opl-cloud/services/control-plane/internal/clients"
)

func canonicalCustomerBillingReceipt() clients.Receipt {
	receipt := customerBillingReceipt()
	receipt.Cost["priceVersion"], receipt.Cost["currency"] = "pricing-v1", "USD"
	return receipt
}

func workspaceBillingReceipt(receiptType string) clients.Receipt {
	return clients.Receipt{
		ReceiptInput: clients.ReceiptInput{
			Type: receiptType, Status: "completed", AccountID: "acct-alpha", WorkspaceID: "ws-alpha",
			Cost: map[string]any{
				"resourceType": "workspace", "resourceId": "ws-alpha", "priceVersion": "pricing-v1", "currency": "USD",
				"billingUnit": "calendar_month", "totalUsdMicros": int64(52_580_000),
				"periodStart": "2026-07-16T00:00:00Z", "paidThrough": "2026-08-16T00:00:00Z",
			},
		},
		ReceiptID: "receipt-workspace", CreatedAt: "2026-07-16T00:00:00Z",
	}
}

func TestBillingReceiptListIsStrictLedgerSource(t *testing.T) {
	receipt := canonicalCustomerBillingReceipt()
	workspace := workspaceBillingReceipt("billing.workspace_renewed.v1")
	ledger := &customerFactsLedger{page: clients.ReceiptPage{Receipts: []clients.Receipt{
		receipt,
		{ReceiptInput: clients.ReceiptInput{Type: "execution.receipt.v1", AccountID: "acct-alpha"}, ReceiptID: "receipt-execution"},
		workspace,
	}, NextCursor: "next-page", HasMore: true}}
	server := NewServer(newTestService(ledger, &fakeFabricClient{}))
	session := tenantAdminSessionForTest(t, server)

	response := requestWithSession(t, server, session, http.MethodGet, "/api/billing/receipts?accountId=acct-beta&cursor=opaque&limit=50", "")
	if response.Code != http.StatusOK {
		t.Fatalf("receipt list = %d: %s", response.Code, response.Body.String())
	}
	if ledger.query != (clients.ReceiptQuery{AccountID: "acct-alpha", Cursor: "opaque", Limit: 50}) {
		t.Fatalf("Ledger query = %#v", ledger.query)
	}
	var envelope map[string]any
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	data := mapField(envelope, "data")
	items, _ := data["receipts"].([]any)
	if envelope["source"] != "ledger" || envelope["status"] != "available" || envelope["available"] != true || len(items) != 2 || data["nextCursor"] != "next-page" || data["hasMore"] != true {
		t.Fatalf("receipt envelope = %#v", envelope)
	}
	assertCustomerBillingReceipt(t, items[0].(map[string]any))
	if projected := items[1].(map[string]any); projected["type"] != "billing.workspace_renewed.v1" || projected["totalUsdMicros"] != float64(52_580_000) || projected["chargeUsdMicros"] != nil {
		t.Fatalf("Workspace receipt = %#v", projected)
	}

	emptyLedger := &customerFactsLedger{}
	emptyServer := NewServer(newTestService(emptyLedger, &fakeFabricClient{}))
	empty := requestWithSession(t, emptyServer, tenantAdminSessionForTest(t, emptyServer), http.MethodGet, "/api/billing/receipts", "")
	var emptyEnvelope map[string]any
	_ = json.NewDecoder(empty.Body).Decode(&emptyEnvelope)
	if empty.Code != http.StatusOK || emptyEnvelope["source"] != "ledger" || emptyEnvelope["status"] != "empty" || emptyEnvelope["available"] != true || len(mapField(emptyEnvelope, "data")["receipts"].([]any)) != 0 {
		t.Fatalf("empty receipts = %d: %#v", empty.Code, emptyEnvelope)
	}
}

func TestBillingReceiptListInvalidSourceIsUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		receipt func() clients.Receipt
	}{
		{name: "cross tenant", receipt: func() clients.Receipt {
			receipt := canonicalCustomerBillingReceipt()
			receipt.AccountID = "acct-beta"
			return receipt
		}},
		{name: "legacy cny fallback", receipt: func() clients.Receipt {
			receipt := customerBillingReceipt()
			delete(receipt.Cost, "priceVersion")
			delete(receipt.Cost, "currency")
			receipt.Cost["pricingVersion"], receipt.Cost["monthlyPriceCnyCents"] = "pricing-v1", int64(35000)
			return receipt
		}},
		{name: "fractional money", receipt: func() clients.Receipt {
			receipt := canonicalCustomerBillingReceipt()
			receipt.Cost["chargeUsdMicros"] = 1.5
			return receipt
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledger := &customerFactsLedger{page: clients.ReceiptPage{Receipts: []clients.Receipt{tc.receipt()}}}
			server := NewServer(newTestService(ledger, &fakeFabricClient{}))
			response := requestWithSession(t, server, tenantAdminSessionForTest(t, server), http.MethodGet, "/api/billing/receipts", "")
			assertUnavailableWorkspaceEnvelope(t, response, http.StatusBadGateway, "ledger")
		})
	}

	ledger := &customerFactsLedger{listErr: errors.New("Ledger unavailable")}
	server := NewServer(newTestService(ledger, &fakeFabricClient{}))
	response := requestWithSession(t, server, tenantAdminSessionForTest(t, server), http.MethodGet, "/api/billing/receipts", "")
	assertUnavailableWorkspaceEnvelope(t, response, http.StatusBadGateway, "ledger")
}

func TestWorkspaceBillingReceiptProjectionUsesAuthoritativeMoney(t *testing.T) {
	for _, tc := range []struct {
		typeName string
		refund   bool
	}{
		{typeName: "billing.workspace_renewed.v1"},
		{typeName: "billing.workspace_expired.v1"},
		{typeName: "billing.workspace_refunded.v1", refund: true},
	} {
		t.Run(tc.typeName, func(t *testing.T) {
			receipt := workspaceBillingReceipt(tc.typeName)
			if tc.refund {
				receipt.Cost["refundUsdMicros"] = int64(52_580_000)
			}
			projected, ok := projectCustomerBillingReceipt(receipt)
			if !ok || projected["totalUsdMicros"] != int64(52_580_000) || projected["chargeUsdMicros"] != nil {
				t.Fatalf("Workspace projection = %#v ok=%v", projected, ok)
			}
			if tc.refund && projected["refundUsdMicros"] != int64(52_580_000) {
				t.Fatalf("refund projection = %#v", projected)
			}
		})
	}
}

func TestBillingReceiptDetailIsStrictLedgerSource(t *testing.T) {
	receipt := canonicalCustomerBillingReceipt()
	ledger := &customerFactsLedger{receipt: receipt}
	server := NewServer(newTestService(ledger, &fakeFabricClient{}))
	response := requestWithSession(t, server, tenantAdminSessionForTest(t, server), http.MethodGet, "/api/billing/receipts/receipt-1?accountId=acct-beta", "")
	if response.Code != http.StatusOK {
		t.Fatalf("receipt detail = %d: %s", response.Code, response.Body.String())
	}
	var envelope map[string]any
	_ = json.NewDecoder(response.Body).Decode(&envelope)
	if envelope["source"] != "ledger" || envelope["status"] != "available" || envelope["available"] != true {
		t.Fatalf("receipt detail envelope = %#v", envelope)
	}
	assertCustomerBillingReceipt(t, mapField(envelope, "data"))
}

func TestBillingReceiptDetailRejectsMismatchedLedgerIdentity(t *testing.T) {
	receipt := canonicalCustomerBillingReceipt()
	receipt.ReceiptID = "receipt-other"
	ledger := &customerFactsLedger{receipt: receipt}
	server := NewServer(newTestService(ledger, &fakeFabricClient{}))

	response := requestWithSession(t, server, tenantAdminSessionForTest(t, server), http.MethodGet, "/api/billing/receipts/receipt-requested", "")
	assertUnavailableWorkspaceEnvelope(t, response, http.StatusBadGateway, "ledger")
}

func TestBillingReceiptSourcesRejectMalformedCustomerDTO(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*clients.Receipt)
	}{
		{name: "empty receipt ID", mutate: func(receipt *clients.Receipt) { receipt.ReceiptID = "" }},
		{name: "unknown billing type", mutate: func(receipt *clients.Receipt) { receipt.Type = "billing.future.v1" }},
		{name: "empty status", mutate: func(receipt *clients.Receipt) { receipt.Status = "" }},
		{name: "empty workspace ID", mutate: func(receipt *clients.Receipt) { receipt.WorkspaceID = "" }},
		{name: "empty resource type", mutate: func(receipt *clients.Receipt) { receipt.Cost["resourceType"] = "" }},
		{name: "empty resource ID", mutate: func(receipt *clients.Receipt) { receipt.Cost["resourceId"] = "" }},
		{name: "invalid createdAt", mutate: func(receipt *clients.Receipt) { receipt.CreatedAt = "not-rfc3339" }},
		{name: "invalid periodStart", mutate: func(receipt *clients.Receipt) { receipt.Cost["periodStart"] = "not-rfc3339" }},
		{name: "invalid paidThrough", mutate: func(receipt *clients.Receipt) { receipt.Cost["paidThrough"] = "not-rfc3339" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receipt := canonicalCustomerBillingReceipt()
			tc.mutate(&receipt)
			ledger := &customerFactsLedger{page: clients.ReceiptPage{Receipts: []clients.Receipt{receipt}}, receipt: receipt}
			server := NewServer(newTestService(ledger, &fakeFabricClient{}))
			session := tenantAdminSessionForTest(t, server)
			for name, path := range map[string]string{"list": "/api/billing/receipts", "detail": "/api/billing/receipts/receipt-1"} {
				t.Run(name, func(t *testing.T) {
					response := requestWithSession(t, server, session, http.MethodGet, path, "")
					assertUnavailableWorkspaceEnvelope(t, response, http.StatusBadGateway, "ledger")
				})
			}
		})
	}
}
