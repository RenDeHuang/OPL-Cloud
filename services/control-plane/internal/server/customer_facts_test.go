package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

type customerFactsLedger struct {
	fakeLedgerClient
	page       clients.ReceiptPage
	listErr    error
	query      clients.ReceiptQuery
	receipt    clients.Receipt
	receiptErr error
}

type customerFactsSub2API struct {
	*testSub2APIClient
	usagePage  clients.Sub2APIUsagePage
	usageErr   error
	usageQuery clients.Sub2APIUsageQuery
	usageStats clients.Sub2APIUsageStats
	statsErr   error
	statsQuery clients.Sub2APIUsageStatsQuery
}

func (c *customerFactsSub2API) Usage(_ context.Context, query clients.Sub2APIUsageQuery) (clients.Sub2APIUsagePage, error) {
	c.usageQuery = query
	return c.usagePage, c.usageErr
}

func (c *customerFactsSub2API) UsageStats(_ context.Context, query clients.Sub2APIUsageStatsQuery) (clients.Sub2APIUsageStats, error) {
	c.statsQuery = query
	return c.usageStats, c.statsErr
}

func (*customerFactsSub2API) BalanceHistory(context.Context, int64) ([]clients.Sub2APIBalanceHistoryEntry, error) {
	return nil, nil
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

func TestGatewayUsageAndStatsUseMappedWorkspaceKey(t *testing.T) {
	t.Setenv("OPL_CONSOLE_USERS_JSON", `[{"id":"usr-gateway-member","email":"gateway-member@example.com","password":"correct horse battery staple","role":"member","accountId":"acct-gateway","sub2apiUserId":41}]`)
	createdAt := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	sub2API := &customerFactsSub2API{
		testSub2APIClient: &testSub2APIClient{balance: 123, charges: map[string]int64{}, workspaceKey: clients.Sub2APIWorkspaceKey{ID: 9, UserID: 41, Name: "opl-workspace", Key: "workspace-key-secret", Status: "active"}},
		usagePage: clients.Sub2APIUsagePage{
			Items: []clients.Sub2APIUsageRecord{{
				UserID: 41, APIKeyID: 9, RequestID: "req-1", CreatedAt: createdAt, Model: "gpt-5", InboundEndpoint: "/v1/responses", RequestType: "sync",
				InputTokens: 10, OutputTokens: 20, CacheCreationTokens: 0, CacheReadTokens: 5, ActualCostUSDMicros: 1234,
			}},
			Total: 1, Page: 1, PageSize: 50, Pages: 1,
		},
		usageStats: clients.Sub2APIUsageStats{TotalRequests: 1, TotalInputTokens: 10, TotalOutputTokens: 20, TotalTokens: 35, TotalActualCostUSDMicros: 1234},
	}
	server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, sub2API))
	session := loginForTest(t, server, "gateway-member@example.com", "correct horse battery staple")

	usage := requestWithSession(t, server, session, http.MethodGet, "/api/gateway/usage?page=1&pageSize=50&user_id=999&api_key_id=999&sub2apiUserId=999", "")
	if usage.Code != http.StatusOK || usage.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("usage response = %d cache=%q: %s", usage.Code, usage.Header().Get("Cache-Control"), usage.Body.String())
	}
	if sub2API.usageQuery != (clients.Sub2APIUsageQuery{UserID: 41, APIKeyID: 9, Page: 1, PageSize: 50}) {
		t.Fatalf("usage query = %#v", sub2API.usageQuery)
	}
	var page map[string]any
	if err := json.NewDecoder(usage.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	items, _ := page["items"].([]any)
	if len(page) != 5 || len(items) != 1 || numberField(page, "total", 0) != 1 || numberField(page, "page", 0) != 1 || numberField(page, "pageSize", 0) != 50 || numberField(page, "pages", 0) != 1 {
		t.Fatalf("usage page = %#v", page)
	}
	row := items[0].(map[string]any)
	allowed := map[string]bool{"requestId": true, "createdAt": true, "model": true, "inboundEndpoint": true, "requestType": true, "inputTokens": true, "outputTokens": true, "cacheCreationTokens": true, "cacheReadTokens": true, "actualCostUsdMicros": true}
	if len(row) != len(allowed) || row["requestId"] != "req-1" || numberField(row, "actualCostUsdMicros", 0) != 1234 {
		t.Fatalf("usage row = %#v", row)
	}
	for key := range row {
		if !allowed[key] {
			t.Fatalf("unsafe usage field %q in %#v", key, row)
		}
	}
	stats := requestWithSession(t, server, session, http.MethodGet, "/api/gateway/usage/stats?period=month&user_id=999&api_key_id=999", "")
	if stats.Code != http.StatusOK || stats.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("stats response = %d cache=%q: %s", stats.Code, stats.Header().Get("Cache-Control"), stats.Body.String())
	}
	if sub2API.statsQuery != (clients.Sub2APIUsageStatsQuery{UserID: 41, APIKeyID: 9, Period: "month"}) {
		t.Fatalf("stats query = %#v", sub2API.statsQuery)
	}
	var totals map[string]any
	if err := json.NewDecoder(stats.Body).Decode(&totals); err != nil {
		t.Fatal(err)
	}
	if len(totals) != 5 || numberField(totals, "totalRequests", 0) != 1 || numberField(totals, "totalActualCostUsdMicros", 0) != 1234 {
		t.Fatalf("usage stats = %#v", totals)
	}
}

func TestGatewayUsageAndStatsFailClosedWithoutFacts(t *testing.T) {
	for _, path := range []string{"/api/gateway/usage", "/api/gateway/usage/stats?period=month"} {
		for _, tc := range []struct {
			name       string
			client     clients.Sub2APIClient
			wantStatus int
			wantCode   string
		}{
			{
				name: "missing key", wantStatus: http.StatusConflict, wantCode: "gateway_key_missing",
				client: &customerFactsSub2API{testSub2APIClient: &testSub2APIClient{charges: map[string]int64{}, workspaceKeyErr: clients.ErrSub2APIWorkspaceKeyMissing}},
			},
			{
				name: "ambiguous key", wantStatus: http.StatusConflict, wantCode: "gateway_key_ambiguous",
				client: &customerFactsSub2API{testSub2APIClient: &testSub2APIClient{charges: map[string]int64{}, workspaceKeyErr: clients.ErrSub2APIWorkspaceKeyAmbiguous}},
			},
			{
				name: "missing usage capability", wantStatus: http.StatusBadGateway, wantCode: "sub2api_usage_unavailable",
				client: &testSub2APIClient{charges: map[string]int64{}},
			},
			{
				name: "upstream unavailable", wantStatus: http.StatusBadGateway, wantCode: "sub2api_usage_unavailable",
				client: &customerFactsSub2API{testSub2APIClient: &testSub2APIClient{charges: map[string]int64{}}, usageErr: errors.New("usage unavailable"), statsErr: errors.New("stats unavailable")},
			},
		} {
			t.Run(path+" "+tc.name, func(t *testing.T) {
				t.Setenv("OPL_CONSOLE_USERS_JSON", `[{"id":"usr-gateway-member","email":"gateway-member@example.com","password":"correct horse battery staple","role":"member","accountId":"acct-gateway","sub2apiUserId":41}]`)
				server := NewServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, tc.client))
				session := loginForTest(t, server, "gateway-member@example.com", "correct horse battery staple")
				response := requestWithSession(t, server, session, http.MethodGet, path, "")
				assertErrorResponse(t, response.Code, response.Body.String(), tc.wantStatus, tc.wantCode)
				if strings.Contains(response.Body.String(), `:0`) {
					t.Fatalf("unavailable response substituted zero: %s", response.Body.String())
				}
			})
		}
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
