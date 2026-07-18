package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

type customerFactsLedger struct {
	fakeLedgerClient
	page               clients.ReceiptPage
	listErr            error
	query              clients.ReceiptQuery
	queries            []clients.ReceiptQuery
	receipt            clients.Receipt
	receiptErr         error
	reconciliationErr  error
	reconciliationKeys []string
	reports            []map[string]any
	receiptWrites      int
}

type customerFactsSub2API struct {
	*testSub2APIClient
	usagePage  clients.Sub2APIUsagePage
	usageErr   error
	usageQuery clients.Sub2APIUsageQuery
	usageStats clients.Sub2APIUsageStats
	statsErr   error
	statsQuery clients.Sub2APIUsageStatsQuery
	history    map[int64][]clients.Sub2APIBalanceHistoryEntry
	historyErr error
	historyIDs []int64
}

type customerFactsFabric struct {
	fakeFabricClient
	operations    []clients.FabricOperation
	operationsErr error
}

func (c *customerFactsSub2API) Usage(_ context.Context, query clients.Sub2APIUsageQuery) (clients.Sub2APIUsagePage, error) {
	c.usageQuery = query
	return c.usagePage, c.usageErr
}

func (c *customerFactsSub2API) UsageStats(_ context.Context, query clients.Sub2APIUsageStatsQuery) (clients.Sub2APIUsageStats, error) {
	c.statsQuery = query
	return c.usageStats, c.statsErr
}

func (c *customerFactsSub2API) BalanceHistory(_ context.Context, userID int64) ([]clients.Sub2APIBalanceHistoryEntry, error) {
	c.historyIDs = append(c.historyIDs, userID)
	return append([]clients.Sub2APIBalanceHistoryEntry(nil), c.history[userID]...), c.historyErr
}

func (l *customerFactsLedger) ListReceipts(_ context.Context, query clients.ReceiptQuery) (clients.ReceiptPage, error) {
	l.query = query
	l.queries = append(l.queries, query)
	return l.page, l.listErr
}

func (l *customerFactsLedger) Receipt(_ context.Context, _ string) (clients.Receipt, error) {
	return l.receipt, l.receiptErr
}

func (l *customerFactsLedger) RecordReceipt(ctx context.Context, input clients.ReceiptInput, key string) (clients.Receipt, error) {
	l.receiptWrites++
	return l.fakeLedgerClient.RecordReceipt(ctx, input, key)
}

func (l *customerFactsLedger) RecordReconciliation(_ context.Context, input clients.ReconciliationInput, key string) (clients.ReconciliationResult, error) {
	report := cloneMap(input.Report)
	l.reconciliationKeys = append(l.reconciliationKeys, key)
	l.reports = append(l.reports, report)
	if l.reconciliationErr != nil {
		return clients.ReconciliationResult{}, l.reconciliationErr
	}
	status := stringValue(report["status"])
	return clients.ReconciliationResult{
		ID: stringValue(report["id"]), Status: status, Report: report,
		BlockNewWorkspaces: status != "ok", Reason: "operator_reconciliation",
	}, nil
}

func (f *customerFactsFabric) ListOperations(_ context.Context) ([]clients.FabricOperation, error) {
	f.record("fabric.operations")
	return append([]clients.FabricOperation(nil), f.operations...), f.operationsErr
}

func TestBillingReceiptListTenantProjection(t *testing.T) {
	billing := customerBillingReceipt()
	ledger := &customerFactsLedger{page: clients.ReceiptPage{
		Receipts:   []clients.Receipt{billing},
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
	if page["source"] != "ledger" || page["status"] != "available" {
		t.Fatalf("source envelope = %#v", page)
	}
	page = mapField(page, "data")
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
	assertUnavailableWorkspaceEnvelope(t, response, http.StatusBadGateway, "ledger")
}

func TestBillingReceiptDetailProjection(t *testing.T) {
	receipt := customerBillingReceipt()
	receipt.Cost["priceVersion"], receipt.Cost["currency"] = "pricing-v1", "USD"
	ledger := &customerFactsLedger{receipt: receipt}
	server := NewServer(newTestService(ledger, &fakeFabricClient{}))

	response := requestWithSession(t, server, tenantAdminSessionForTest(t, server), http.MethodGet, "/api/billing/receipts/receipt-1", "")
	if response.Code != http.StatusOK {
		t.Fatalf("detail status = %d: %s", response.Code, response.Body.String())
	}
	var projected map[string]any
	if err := json.NewDecoder(response.Body).Decode(&projected); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	assertCustomerBillingReceipt(t, mapField(projected, "data"))
}

func TestBillingReceiptProjectionRejectsMalformedMoney(t *testing.T) {
	receipt := customerBillingReceipt()
	receipt.Cost["chargeUsdMicros"] = 1.5
	ledger := &customerFactsLedger{page: clients.ReceiptPage{Receipts: []clients.Receipt{receipt}}}
	server := NewServer(newTestService(ledger, &fakeFabricClient{}))

	response := requestWithSession(t, server, tenantAdminSessionForTest(t, server), http.MethodGet, "/api/billing/receipts", "")
	assertUnavailableWorkspaceEnvelope(t, response, http.StatusBadGateway, "ledger")
}

func TestBillingReceiptProjectionRejectsMalformedPricingIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "canonical missing currency", mutate: func(cost map[string]any) { delete(cost, "currency") }},
		{name: "canonical non USD currency", mutate: func(cost map[string]any) { cost["priceVersion"], cost["currency"] = "pricing-v1", "CNY" }},
		{name: "canonical wrong currency type", mutate: func(cost map[string]any) { cost["priceVersion"], cost["currency"] = "pricing-v1", 42 }},
		{name: "canonical legacy version mismatch", mutate: func(cost map[string]any) { cost["pricingVersion"] = "pricing-v2" }},
		{name: "legacy CNY fallback", mutate: func(cost map[string]any) {
			delete(cost, "priceVersion")
			delete(cost, "currency")
			cost["pricingVersion"], cost["monthlyPriceCnyCents"] = "pricing-v1", int64(35000)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receipt := customerBillingReceipt()
			tc.mutate(receipt.Cost)
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

func TestBillingReceiptListUnavailableIsStrictEnvelope(t *testing.T) {
	ledger := &customerFactsLedger{listErr: errors.New("Ledger unavailable")}
	server := NewServer(newTestService(ledger, &fakeFabricClient{}))
	session := tenantAdminSessionForTest(t, server)

	list := requestWithSession(t, server, session, http.MethodGet, "/api/billing/receipts", "")
	assertUnavailableWorkspaceEnvelope(t, list, http.StatusBadGateway, "ledger")
}

func TestGatewayUsageAndStatsUseMappedWorkspaceKey(t *testing.T) {
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
	server, session := newGatewayOwnerTestServer(t, sub2API, nil)

	usage := requestWithSession(t, server, session, http.MethodGet, "/api/gateway/usage?page=1&pageSize=50&user_id=999&api_key_id=999&sub2apiUserId=999", "")
	if usage.Code != http.StatusOK || usage.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("usage response = %d cache=%q: %s", usage.Code, usage.Header().Get("Cache-Control"), usage.Body.String())
	}
	if sub2API.usageQuery != (clients.Sub2APIUsageQuery{UserID: 41, APIKeyID: 9, Page: 1, PageSize: 50}) {
		t.Fatalf("usage query = %#v", sub2API.usageQuery)
	}
	var envelope map[string]any
	if err := json.NewDecoder(usage.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	page := mapField(envelope, "data")
	items, _ := page["items"].([]any)
	if envelope["source"] != "sub2api" || envelope["status"] != "available" || envelope["available"] != true || len(page) != 5 || len(items) != 1 || numberField(page, "total", 0) != 1 || numberField(page, "page", 0) != 1 || numberField(page, "pageSize", 0) != 50 || numberField(page, "pages", 0) != 1 {
		t.Fatalf("usage envelope = %#v", envelope)
	}
	row := items[0].(map[string]any)
	allowed := map[string]bool{"apiKeyId": true, "requestId": true, "createdAt": true, "model": true, "inboundEndpoint": true, "requestType": true, "inputTokens": true, "outputTokens": true, "cacheCreationTokens": true, "cacheReadTokens": true, "actualCostUsdMicros": true}
	if len(row) != len(allowed) || row["apiKeyId"] != "9" || row["requestId"] != "req-1" || numberField(row, "actualCostUsdMicros", 0) != 1234 {
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
	if err := json.NewDecoder(stats.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	totals := mapField(envelope, "data")
	if len(totals) != 5 || numberField(totals, "totalRequests", 0) != 1 || numberField(totals, "totalActualCostUsdMicros", 0) != 1234 {
		t.Fatalf("usage stats = %#v", envelope)
	}
}

func TestGatewayUsageAndStatsFailClosedWithoutFacts(t *testing.T) {
	for _, path := range []string{"/api/gateway/usage", "/api/gateway/usage/stats?period=month"} {
		for _, tc := range []struct {
			name       string
			client     clients.Sub2APIClient
			wantStatus int
		}{
			{
				name: "missing key", wantStatus: http.StatusConflict,
				client: &customerFactsSub2API{testSub2APIClient: &testSub2APIClient{charges: map[string]int64{}, workspaceKeyErr: clients.ErrSub2APIWorkspaceKeyMissing}},
			},
			{
				name: "ambiguous key", wantStatus: http.StatusConflict,
				client: &customerFactsSub2API{testSub2APIClient: &testSub2APIClient{charges: map[string]int64{}, workspaceKeyErr: clients.ErrSub2APIWorkspaceKeyAmbiguous}},
			},
			{
				name: "missing usage capability", wantStatus: http.StatusBadGateway,
				client: &testSub2APIClient{charges: map[string]int64{}},
			},
			{
				name: "upstream unavailable", wantStatus: http.StatusBadGateway,
				client: &customerFactsSub2API{testSub2APIClient: &testSub2APIClient{charges: map[string]int64{}}, usageErr: errors.New("usage unavailable"), statsErr: errors.New("stats unavailable")},
			},
		} {
			t.Run(path+" "+tc.name, func(t *testing.T) {
				server, session := newGatewayOwnerTestServer(t, tc.client, nil)
				response := requestWithSession(t, server, session, http.MethodGet, path, "")
				assertUnavailableSourceEnvelope(t, response, tc.wantStatus)
				if strings.Contains(response.Body.String(), `:0`) {
					t.Fatalf("unavailable response substituted zero: %s", response.Body.String())
				}
			})
		}
	}
}

func TestBillingReconciliationCompleteMatchIsDeterministic(t *testing.T) {
	fixture := newBillingReconciliationFixture(t)
	operator := operatorSessionForTest(t, fixture.server)

	first := requestWithMutationKeyForTest(t, fixture.server, operator, http.MethodPost, "/api/billing/reconciliation", `{"confirm":true}`, "reconcile-match")
	if first.Code != http.StatusCreated {
		t.Fatalf("first reconciliation status = %d: %s", first.Code, first.Body.String())
	}
	firstBody := decodeReconciliationResponse(t, first)
	assertReconciliationReport(t, firstBody, "ok", 2, 2, 0)
	report := firstBody["report"].(map[string]any)
	if report["id"] != "reconciliation-"+stableID("reconcile-match")[:18] {
		t.Fatalf("report id = %#v", report["id"])
	}
	if _, exists := report["checkedAt"]; exists {
		t.Fatalf("deterministic report contains checkedAt: %#v", report)
	}

	replay := requestWithMutationKeyForTest(t, fixture.server, operator, http.MethodPost, "/api/billing/reconciliation", `{"confirm":true}`, "reconcile-match")
	if replay.Code != http.StatusCreated {
		t.Fatalf("replayed reconciliation status = %d: %s", replay.Code, replay.Body.String())
	}
	replayBody := decodeReconciliationResponse(t, replay)
	if !reflect.DeepEqual(firstBody["report"], replayBody["report"]) || len(fixture.ledger.reports) != 2 || !reflect.DeepEqual(fixture.ledger.reports[0], fixture.ledger.reports[1]) {
		t.Fatalf("same key changed report: first=%#v replay=%#v recorded=%#v", firstBody["report"], replayBody["report"], fixture.ledger.reports)
	}
	assertReconciliationReadOnly(t, fixture)
}

func TestBillingReconciliationTreatsWorkspaceRenewalAsOneCombinedOperation(t *testing.T) {
	renewal := newWorkspaceRenewalWorkerFixture(t, []int64{100_000_000, 47_420_000})
	if err := renewal.app.runMonthlyBillingOnce(context.Background(), renewal.service, renewal.paidThrough.Add(-monthlyRenewalLead)); err != nil {
		t.Fatal(err)
	}
	operation, err := decodeWorkspaceRenewalOperation(renewal.operation(t))
	if err != nil {
		t.Fatal(err)
	}
	compute, _ := renewal.app.getCompute(operation.ComputeID)
	storage, _ := renewal.app.getStorage(operation.StorageID)
	usedBy := int64(41)
	history := []clients.Sub2APIBalanceHistoryEntry{{
		Code: operation.RedeemCode, Type: "balance", ValueUSDMicros: -operation.TotalUSDMicros, Status: "used", UsedBy: &usedBy,
		UsedAt: &renewal.paidThrough, CreatedAt: renewal.paidThrough.Add(-time.Minute),
	}}
	ledger := &customerFactsLedger{page: clients.ReceiptPage{Receipts: []clients.Receipt{{
		ReceiptInput: renewal.ledger.receipts[0], ReceiptID: operation.ReceiptID, CreatedAt: renewal.paidThrough.Format(time.RFC3339),
	}}}}
	sub2API := &customerFactsSub2API{
		testSub2APIClient: &testSub2APIClient{balance: 1_000_000_000, charges: map[string]int64{}},
		history:           map[int64][]clients.Sub2APIBalanceHistoryEntry{41: history},
	}
	calls := &[]string{}
	fabric := &customerFactsFabric{fakeFabricClient: fakeFabricClient{calls: calls}, operations: []clients.FabricOperation{
		workspaceRenewalReconciliationFabricOperation(operation, "compute", compute),
		workspaceRenewalReconciliationFabricOperation(operation, "storage", storage),
	}}
	server, err := NewPersistentServer(controlplane.NewService(ledger, fabric, sub2API), renewal.app.tables)
	if err != nil {
		t.Fatal(err)
	}
	response := requestWithMutationKeyForTest(t, server, operatorSessionForTest(t, server), http.MethodPost, "/api/billing/reconciliation", `{"confirm":true}`, "reconcile-workspace-renewal")
	if response.Code != http.StatusCreated {
		t.Fatalf("reconciliation status=%d body=%s", response.Code, response.Body.String())
	}
	assertReconciliationReport(t, decodeReconciliationResponse(t, response), "ok", 1, 1, 0)
	if len(sub2API.history[41]) != 1 || len(ledger.page.Receipts) != 1 || len(fabric.operations) != 2 {
		t.Fatalf("combined facts history=%#v receipts=%#v operations=%#v", sub2API.history[41], ledger.page.Receipts, fabric.operations)
	}
	originalCost := structToMap(ledger.page.Receipts[0].Cost)
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "total", mutate: func(cost map[string]any) { cost["totalUsdMicros"] = int64(1) }},
		{name: "Sub2API user", mutate: func(cost map[string]any) { cost["sub2apiUserId"] = int64(42) }},
		{name: "redeem code", mutate: func(cost map[string]any) { cost["sub2apiRedeemCode"] = "opl:other" }},
		{name: "post-charge balance", mutate: func(cost map[string]any) { cost["postChargeBalanceUsdMicros"] = int64(1) }},
		{name: "compute resource type", mutate: func(cost map[string]any) {
			cost["components"].(map[string]any)["compute"].(map[string]any)["resourceType"] = "storage"
		}},
		{name: "storage resource type", mutate: func(cost map[string]any) {
			cost["components"].(map[string]any)["storage"].(map[string]any)["resourceType"] = "compute"
		}},
		{name: "billing unit", mutate: func(cost map[string]any) { cost["billingUnit"] = "rolling_month" }},
		{name: "period start", mutate: func(cost map[string]any) {
			cost["periodStart"] = renewal.paidThrough.Add(-time.Hour).Format(time.RFC3339)
		}},
		{name: "paid through", mutate: func(cost map[string]any) {
			cost["paidThrough"] = renewal.renewedThrough.Add(time.Hour).Format(time.RFC3339)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledger.page.Receipts[0].Cost = structToMap(originalCost)
			tc.mutate(ledger.page.Receipts[0].Cost)
			key := "reconcile-workspace-renewal-" + strings.ReplaceAll(tc.name, " ", "-")
			mismatch := requestWithMutationKeyForTest(t, server, operatorSessionForTest(t, server), http.MethodPost, "/api/billing/reconciliation", `{"confirm":true}`, key)
			if mismatch.Code != http.StatusCreated {
				t.Fatalf("mismatch status=%d body=%s", mismatch.Code, mismatch.Body.String())
			}
			mismatchBody := decodeReconciliationResponse(t, mismatch)
			assertReconciliationReport(t, mismatchBody, "mismatch", 1, 0, 1)
			assertReconciliationException(t, mismatchBody["report"].(map[string]any), "workspace", operation.WorkspaceID, "ledger_receipt_mismatch")
		})
	}
	t.Run("current account mapping", func(t *testing.T) {
		operation.ChargeConfirmation["userId"] = int64(42)
		mustStore(t, renewal.app.tables.SaveRuntimeOperation(context.Background(), workspaceRenewalOperationRow(operation)))
		ledger.page.Receipts[0].Cost = structToMap(originalCost)
		ledger.page.Receipts[0].Cost["sub2apiUserId"] = int64(42)
		mismatch := requestWithMutationKeyForTest(t, server, operatorSessionForTest(t, server), http.MethodPost, "/api/billing/reconciliation", `{"confirm":true}`, "reconcile-workspace-renewal-current-account")
		if mismatch.Code != http.StatusCreated {
			t.Fatalf("mismatch status=%d body=%s", mismatch.Code, mismatch.Body.String())
		}
		mismatchBody := decodeReconciliationResponse(t, mismatch)
		assertReconciliationReport(t, mismatchBody, "mismatch", 1, 0, 1)
		assertReconciliationException(t, mismatchBody["report"].(map[string]any), "workspace", operation.WorkspaceID, "ledger_receipt_mismatch")
	})
}

func workspaceRenewalReconciliationFabricOperation(operation workspaceRenewalOperation, resourceType string, row map[string]any) clients.FabricOperation {
	action, kind := "renew_compute_allocation", "compute_allocation"
	if resourceType == "storage" {
		action, kind = "renew_storage_volume", "storage_volume"
	}
	return clients.FabricOperation{
		ID: "fop-" + resourceType, OperationID: operation.ID + ":" + resourceType, CallerService: "control-plane", Action: action, ResourceKind: kind,
		ResourceID: stringValue(row["id"]), AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, Provider: stringValue(row["provider"]),
		ProviderRequestID: stringValue(row["providerRequestId"]), IdempotencyKey: operation.ID + ":" + resourceType, Status: "succeeded",
		RedactedProviderPayload: map[string]any{"providerResourceId": stringValue(row["providerResourceId"])},
	}
}

func TestBillingReconciliationMismatchBlocksPurchasesWithoutMutation(t *testing.T) {
	tests := []struct {
		name   string
		code   string
		mutate func(*billingReconciliationFixture)
	}{
		{name: "Sub2API charge missing", code: "sub2api_charge_missing", mutate: func(f *billingReconciliationFixture) {
			f.sub2API.history[41] = f.sub2API.history[41][1:]
		}},
		{name: "Sub2API charge changed", code: "sub2api_charge_mismatch", mutate: func(f *billingReconciliationFixture) {
			f.sub2API.history[41][0].ValueUSDMicros = -1
		}},
		{name: "Fabric operation missing", code: "fabric_operation_missing", mutate: func(f *billingReconciliationFixture) {
			f.fabric.operations = f.fabric.operations[1:]
		}},
		{name: "Fabric provider fact changed", code: "fabric_operation_mismatch", mutate: func(f *billingReconciliationFixture) {
			f.fabric.operations[0].RedactedProviderPayload["providerResourceId"] = "ins-other"
		}},
		{name: "Ledger receipt missing", code: "ledger_receipt_missing", mutate: func(f *billingReconciliationFixture) {
			f.ledger.page.Receipts = f.ledger.page.Receipts[1:]
		}},
		{name: "Ledger receipt changed", code: "ledger_receipt_mismatch", mutate: func(f *billingReconciliationFixture) {
			f.ledger.page.Receipts[0].Cost["chargeUsdMicros"] = int64(1)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newBillingReconciliationFixture(t)
			tc.mutate(fixture)
			response := requestWithMutationKeyForTest(t, fixture.server, operatorSessionForTest(t, fixture.server), http.MethodPost, "/api/billing/reconciliation", `{"confirm":true}`, "reconcile-mismatch")
			if response.Code != http.StatusCreated {
				t.Fatalf("reconciliation status = %d: %s", response.Code, response.Body.String())
			}
			body := decodeReconciliationResponse(t, response)
			assertReconciliationReport(t, body, "mismatch", 2, 1, 1)
			assertReconciliationException(t, body["report"].(map[string]any), "compute", "compute-reconcile", tc.code)

			blocked := requestWithMutationKeyForTest(t, fixture.server, fixture.member, http.MethodPost, "/api/workspace-launches", `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`, "blocked-after-reconciliation")
			assertErrorResponse(t, blocked.Code, blocked.Body.String(), http.StatusConflict, "billing_reconciliation_blocked")
			assertReconciliationReadOnly(t, fixture)
		})
	}
}

func TestBillingReconciliationUnavailableFactsMismatch(t *testing.T) {
	tests := []struct {
		name   string
		code   string
		mutate func(*billingReconciliationFixture)
	}{
		{name: "Sub2API", code: "sub2api_balance_history_unavailable", mutate: func(f *billingReconciliationFixture) { f.sub2API.historyErr = errors.New("Sub2API unavailable") }},
		{name: "Fabric", code: "fabric_operations_unavailable", mutate: func(f *billingReconciliationFixture) { f.fabric.operationsErr = errors.New("Fabric unavailable") }},
		{name: "Ledger", code: "ledger_receipts_unavailable", mutate: func(f *billingReconciliationFixture) { f.ledger.listErr = errors.New("Ledger unavailable") }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newBillingReconciliationFixture(t)
			tc.mutate(fixture)
			response := requestWithMutationKeyForTest(t, fixture.server, operatorSessionForTest(t, fixture.server), http.MethodPost, "/api/billing/reconciliation", `{"confirm":true}`, "reconcile-unavailable")
			if response.Code != http.StatusCreated {
				t.Fatalf("reconciliation status = %d: %s", response.Code, response.Body.String())
			}
			body := decodeReconciliationResponse(t, response)
			assertReconciliationReport(t, body, "mismatch", 2, 0, 2)
			assertReconciliationException(t, body["report"].(map[string]any), "compute", "compute-reconcile", tc.code)
			assertReconciliationReadOnly(t, fixture)
		})
	}
}

func TestBillingReconciliationRejectsCallerReportAndRequiresHeader(t *testing.T) {
	fixture := newBillingReconciliationFixture(t)
	operator := operatorSessionForTest(t, fixture.server)
	callerReport := requestWithMutationKeyForTest(t, fixture.server, operator, http.MethodPost, "/api/billing/reconciliation", `{"confirm":true,"report":{"id":"caller","status":"ok"}}`, "caller-report")
	assertErrorResponse(t, callerReport.Code, callerReport.Body.String(), http.StatusBadRequest, "reconciliation_report_server_computed")
	missingKey := requestWithMutationKeyForTest(t, fixture.server, operator, http.MethodPost, "/api/billing/reconciliation", `{"confirm":true}`, "")
	assertErrorResponse(t, missingKey.Code, missingKey.Body.String(), http.StatusBadRequest, "missing Idempotency-Key")
	if len(fixture.ledger.reports) != 0 {
		t.Fatalf("invalid requests reached Ledger: %#v", fixture.ledger.reports)
	}
}

func TestBillingReconciliationMalformedLedgerResponseKeepsLastGuard(t *testing.T) {
	fixture := newBillingReconciliationFixture(t)
	previous := reconciliationResponse(clients.ReconciliationResult{
		ID: "recon-previous", Status: "mismatch", BlockNewWorkspaces: true, Reason: "operator_reconciliation",
		Report: reconciliationReport("recon-previous", 1, 0, []billingReconciliationException{{resourceType: "compute", resourceID: "compute-alpha", code: "ledger_receipt_missing"}}),
	})
	mustStore(t, fixture.store.SaveBillingReconciliation(context.Background(), previous))
	fixture.ledger.reconciliationErr = errors.New("invalid ledger reconciliation response")

	response := requestWithMutationKeyForTest(t, fixture.server, operatorSessionForTest(t, fixture.server), http.MethodPost, "/api/billing/reconciliation", `{"confirm":true}`, "reconcile-malformed-response")
	if response.Code != http.StatusBadGateway {
		t.Fatalf("malformed Ledger response status = %d: %s", response.Code, response.Body.String())
	}
	stored, ok, err := fixture.store.BillingReconciliation(context.Background())
	if err != nil || !ok || !reflect.DeepEqual(stored, previous) {
		t.Fatalf("last valid guard replaced: stored=%#v previous=%#v err=%v", stored, previous, err)
	}
}

type billingReconciliationFixture struct {
	server  http.Handler
	member  *httptest.ResponseRecorder
	store   *memoryTableStore
	ledger  *customerFactsLedger
	sub2API *customerFactsSub2API
	fabric  *customerFactsFabric
	calls   *[]string
}

func newBillingReconciliationFixture(t *testing.T) *billingReconciliationFixture {
	t.Helper()
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-monthly", "org-monthly", "usr-monthly", "monthly@example.com")
	paidThrough := time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)
	compute := monthlyActiveResource("compute", "compute-reconcile", paidThrough)
	storage := monthlyActiveResource("storage", "storage-reconcile", paidThrough)
	compute["resourceType"], storage["resourceType"] = "compute", "storage"
	mustStore(t, store.SaveCompute(context.Background(), compute))
	mustStore(t, store.SaveStorage(context.Background(), storage))
	usedBy := int64(41)
	history := []clients.Sub2APIBalanceHistoryEntry{
		{Code: stringValue(compute["sub2apiRedeemCode"]), Type: "balance", ValueUSDMicros: -int64(numberField(compute, "chargeUsdMicros", 0)), Status: "used", UsedBy: &usedBy, UsedAt: &paidThrough, CreatedAt: paidThrough.Add(-time.Minute)},
		{Code: stringValue(storage["sub2apiRedeemCode"]), Type: "balance", ValueUSDMicros: -int64(numberField(storage, "chargeUsdMicros", 0)), Status: "used", UsedBy: &usedBy, UsedAt: &paidThrough, CreatedAt: paidThrough.Add(-time.Minute)},
	}
	receipts := []clients.Receipt{reconciliationReceipt(compute), reconciliationReceipt(storage)}
	operations := []clients.FabricOperation{reconciliationFabricOperation(compute), reconciliationFabricOperation(storage)}
	ledger := &customerFactsLedger{page: clients.ReceiptPage{Receipts: receipts}}
	sub2API := &customerFactsSub2API{
		testSub2APIClient: &testSub2APIClient{balance: 1_000_000_000, charges: map[string]int64{}},
		history:           map[int64][]clients.Sub2APIBalanceHistoryEntry{41: history},
	}
	calls := &[]string{}
	fabric := &customerFactsFabric{fakeFabricClient: fakeFabricClient{calls: calls}, operations: operations}
	server, err := NewPersistentServer(controlplane.NewService(ledger, fabric, sub2API), store)
	if err != nil {
		t.Fatal(err)
	}
	member := loginForTest(t, server, "monthly@example.com", "CorrectHorseBatteryStaple!")
	return &billingReconciliationFixture{server: server, member: member, store: store, ledger: ledger, sub2API: sub2API, fabric: fabric, calls: calls}
}

func reconciliationReceipt(row map[string]any) clients.Receipt {
	return clients.Receipt{
		ReceiptInput: clients.ReceiptInput{
			Type: "billing.resource_purchased.v1", Status: "completed", AccountID: stringValue(row["accountId"]), WorkspaceID: stringValue(row["workspaceId"]), RequestID: stringValue(row["billingOperationId"]),
			Cost: map[string]any{"resourceType": stringValue(row["resourceType"]), "resourceId": stringValue(row["id"]), "chargeUsdMicros": int64(numberField(row, "chargeUsdMicros", 0))},
		},
		ReceiptID: stringValue(row["lastReceiptId"]), CreatedAt: "2026-07-16T00:00:00Z",
	}
}

func reconciliationFabricOperation(row map[string]any) clients.FabricOperation {
	resourceType := stringValue(row["resourceType"])
	action, kind := "create_compute_allocation", "compute_allocation"
	if resourceType == "storage" {
		action, kind = "create_storage_volume", "storage_volume"
	}
	return clients.FabricOperation{
		ID: "fop-" + resourceType, OperationID: "op-" + resourceType, CallerService: "control-plane", Action: action, ResourceKind: kind,
		ResourceID: stringValue(row["id"]), AccountID: stringValue(row["accountId"]), WorkspaceID: stringValue(row["workspaceId"]), Provider: "tencent-tke",
		ProviderRequestID: stringValue(row["providerRequestId"]), IdempotencyKey: stringValue(row["billingOperationId"]) + ":prepare", Status: "succeeded", RedactedProviderPayload: map[string]any{"providerResourceId": stringValue(row["providerResourceId"])},
	}
}

func decodeReconciliationResponse(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode reconciliation: %v", err)
	}
	return body
}

func assertReconciliationReport(t *testing.T, body map[string]any, status string, checked, matched, exceptions int) {
	t.Helper()
	report, ok := body["report"].(map[string]any)
	if !ok || body["status"] != status || report["status"] != status {
		t.Fatalf("reconciliation status = %#v", body)
	}
	counts, ok := report["counts"].(map[string]any)
	items, itemsOK := report["exceptions"].([]any)
	if !ok || !itemsOK || numberField(counts, "billingOperations", -1) != float64(checked) || numberField(counts, "matched", -1) != float64(matched) || numberField(counts, "exceptions", -1) != float64(exceptions) || len(items) != exceptions {
		t.Fatalf("reconciliation report = %#v", report)
	}
	guard, _ := body["guard"].(map[string]any)
	if guard["blockNewWorkspaces"] != (status != "ok") {
		t.Fatalf("reconciliation guard = %#v", guard)
	}
}

func assertReconciliationException(t *testing.T, report map[string]any, resourceType, resourceID, code string) {
	t.Helper()
	items, _ := report["exceptions"].([]any)
	for _, item := range items {
		exception, _ := item.(map[string]any)
		if exception["resourceType"] == resourceType && exception["resourceId"] == resourceID && exception["code"] == code && len(exception) == 3 {
			return
		}
	}
	t.Fatalf("missing safe exception %s/%s/%s in %#v", resourceType, resourceID, code, items)
}

func assertReconciliationReadOnly(t *testing.T, fixture *billingReconciliationFixture) {
	t.Helper()
	for _, call := range *fixture.calls {
		if call != "fabric.operations" {
			t.Fatalf("reconciliation mutated Fabric: %#v", *fixture.calls)
		}
	}
	if len(fixture.sub2API.charges) != 0 {
		t.Fatalf("reconciliation mutated Sub2API: %#v", fixture.sub2API.charges)
	}
	if fixture.ledger.receiptWrites != 0 {
		t.Fatalf("reconciliation corrected receipts: writes=%d", fixture.ledger.receiptWrites)
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
				"resourceType": "compute", "resourceId": "compute-alpha", "priceVersion": "pricing-v1", "currency": "USD",
				"chargeUsdMicros": int64(50_000_000),
				"periodStart":     "2026-07-16T00:00:00Z", "paidThrough": "2026-08-16T00:00:00Z",
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
		"resourceType": true, "resourceId": true, "priceVersion": true, "currency": true,
		"chargeUsdMicros": true, "periodStart": true, "paidThrough": true,
	}
	if len(receipt) != len(allowed) || receipt["receiptId"] != "receipt-1" || receipt["priceVersion"] != "pricing-v1" || receipt["currency"] != "USD" || receipt["chargeUsdMicros"] != float64(50_000_000) {
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
