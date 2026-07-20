package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

const billingReviewResolvePath = "/api/operator/billing-reviews/compute/compute-review/resolve"

type billingReviewLedger struct {
	fakeLedgerClient
	inputs []clients.ReceiptInput
	keys   []string
	errors []error
}

func (l *billingReviewLedger) RecordReceipt(_ context.Context, input clients.ReceiptInput, key string) (clients.Receipt, error) {
	l.inputs = append(l.inputs, input)
	l.keys = append(l.keys, key)
	if len(l.errors) > 0 {
		err := l.errors[0]
		l.errors = l.errors[1:]
		if err != nil {
			return clients.Receipt{}, err
		}
	}
	return clients.Receipt{ReceiptInput: input, ReceiptID: "receipt-review-resolution"}, nil
}

type billingReviewHarness struct {
	server   http.Handler
	store    *memoryTableStore
	fabric   *monthlyFabric
	sub2API  *monthlySub2API
	ledger   *billingReviewLedger
	service  *controlplane.Service
	operator *httptest.ResponseRecorder
	events   *[]string
}

func newBillingReviewHarness(t *testing.T, row map[string]any, sync clients.ComputeAllocation) *billingReviewHarness {
	t.Helper()
	t.Setenv("OPL_BASIC_COMPUTE_INSTANCE_TYPE", "S5.MEDIUM4")
	events := &[]string{}
	fabric := &monthlyFabric{events: events, computeSync: sync}
	sub2API := &monthlySub2API{events: events, balances: []int64{1_000_000_000}}
	ledger := &billingReviewLedger{}
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	mustStore(t, store.SaveCompute(context.Background(), row))
	service := controlplane.NewService(ledger, fabric, sub2API)
	server, err := NewPersistentServer(service, store)
	if err != nil {
		t.Fatal(err)
	}
	return &billingReviewHarness{server: server, store: store, fabric: fabric, sub2API: sub2API, ledger: ledger, service: service, operator: reservedOperatorSessionForTest(t, server), events: events}
}

func billingReviewRow() map[string]any {
	return map[string]any{
		"id": "compute-review", "resourceType": "compute", "accountId": "acct-alpha", "workspaceId": "workspace-alpha",
		"packageId": "basic", "status": "provisioning", "desiredStatus": "running", "billingStatus": "manual_review",
		"billingOperationId": "purchase-review-001", "pricingVersion": "opl-monthly-v2", "monthlyPriceCnyCents": int64(35000),
		"chargeUsdMicros": int64(50000000), "periodStart": "2026-07-16T00:00:00Z", "paidThrough": "2026-08-16T00:00:00Z",
		"billingAnchorDay": int64(16), "sub2apiRedeemCode": "opl:purchase-review-charge", "sub2apiRefundCode": "opl:purchase-review-refund",
		"postChargeBalanceKnown": true, "postChargeBalanceUsdMicros": int64(150000000), "manualReviewReason": "fabric_prepare_partial",
		"sub2apiChargeConfirmation": map[string]any{"code": "opl:purchase-review-charge", "userId": int64(41), "chargeUsdMicros": int64(50000000), "status": "used"},
		"lastReceiptId":             "receipt-review-required", "zone": "ap-shanghai-2",
	}
}

func confirmedBillingReviewCompute(status string) clients.ComputeAllocation {
	return clients.ComputeAllocation{
		ID: "compute-review", AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", PackageID: "basic", Status: status,
		Provider: "tencent-tke", ProviderResourceID: "ins-compute-review", ProviderRequestID: "req-compute-review",
		InstanceID: "ins-compute-review", CVMInstanceID: "ins-compute-review", InstanceType: "S5.MEDIUM4", Zone: "ap-shanghai-2",
		ChargeType: "PREPAID", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2026-08-16T00:00:00Z",
		ProviderData: map[string]string{"chargeType": "PREPAID", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": "2026-08-16T00:00:00Z", "zone": "ap-shanghai-2", "instanceType": "S5.MEDIUM4"},
	}
}

func billingReviewRequest(t *testing.T, h *billingReviewHarness, session *httptest.ResponseRecorder, key, decision, evidenceRef string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"accountId":"acct-alpha","billingOperationId":"purchase-review-001","decision":"` + decision + `","evidenceRef":"` + evidenceRef + `"}`
	return requestWithMutationKeyForTest(t, h.server, session, http.MethodPost, billingReviewResolvePath, body, key)
}

func billingReviewStoredRow(t *testing.T, store *memoryTableStore) map[string]any {
	t.Helper()
	rows, err := store.ListComputes(context.Background(), "acct-alpha")
	if err != nil || len(rows) != 1 {
		t.Fatalf("stored review resource = %#v, err=%v", rows, err)
	}
	return rows[0]
}

func TestBillingReviewResolutionRequiresReservedOperator(t *testing.T) {
	h := newBillingReviewHarness(t, billingReviewRow(), confirmedBillingReviewCompute("running"))
	tenant := tenantAdminSessionForTest(t, h.server)
	*h.events = nil
	rec := billingReviewRequest(t, h, tenant, "review-resolution-001", "activate_charged_resource", "case-20260716-001")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"error":"admin_required"`) {
		t.Fatalf("tenant resolution = %d %s", rec.Code, rec.Body.String())
	}
	if len(h.ledger.inputs) != 0 || len(h.sub2API.refunds) != 0 || len(*h.events) != 0 {
		t.Fatalf("forbidden request had side effects: events=%#v receipts=%#v refunds=%#v", *h.events, h.ledger.inputs, h.sub2API.refunds)
	}
}

func TestBillingReviewResolutionActivatesConfirmedChargedResourceIdempotently(t *testing.T) {
	h := newBillingReviewHarness(t, billingReviewRow(), confirmedBillingReviewCompute("running"))
	first := billingReviewRequest(t, h, h.operator, "review-resolution-001", "activate_charged_resource", "case-20260716-001")
	if first.Code != http.StatusOK {
		t.Fatalf("first resolution = %d %s", first.Code, first.Body.String())
	}
	row := billingReviewStoredRow(t, h.store)
	if row["billingStatus"] != "active" || row["status"] != "running" || row["lastReceiptId"] != "receipt-review-resolution" {
		t.Fatalf("resolved resource = %#v", row)
	}
	if len(h.ledger.inputs) != 1 || len(h.ledger.keys) != 1 {
		t.Fatalf("closing receipts = %#v keys=%#v", h.ledger.inputs, h.ledger.keys)
	}
	receipt := h.ledger.inputs[0]
	if receipt.Type != "billing.resource_purchased.v1" || receipt.SupersedesReceiptID != "receipt-review-required" || receipt.RequestID != "purchase-review-001" ||
		receipt.Cost["priceVersion"] != "opl-monthly-v2" || receipt.Cost["currency"] != "USD" ||
		stringValue(receipt.ReviewerChecks["decision"]) != "activate_charged_resource" || stringValue(receipt.ReviewerChecks["reviewer"]) != "usr-admin" || stringValue(receipt.InputRefs["evidenceRef"]) != "case-20260716-001" {
		t.Fatalf("closing receipt = %#v", receipt)
	}
	firstBody := first.Body.String()
	restarted, err := NewPersistentServer(h.service, h.store)
	if err != nil {
		t.Fatal(err)
	}
	h.server = restarted
	h.operator = operatorSessionForTest(t, restarted)
	replayed := billingReviewRequest(t, h, h.operator, "review-resolution-001", "activate_charged_resource", "case-20260716-001")
	if replayed.Code != http.StatusOK || replayed.Body.String() != firstBody || len(h.ledger.inputs) != 1 || strings.Count(strings.Join(*h.events, ","), "fabric.compute.sync") != 1 {
		t.Fatalf("replay = %d %s events=%#v receipts=%d", replayed.Code, replayed.Body.String(), *h.events, len(h.ledger.inputs))
	}
	conflict := billingReviewRequest(t, h, h.operator, "review-resolution-001", "activate_charged_resource", "case-20260716-002")
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), `"error":"idempotency_conflict"`) {
		t.Fatalf("conflicting replay = %d %s", conflict.Code, conflict.Body.String())
	}
}

func TestBillingReviewResolutionActivatesConfirmedChargedStorage(t *testing.T) {
	t.Setenv("OPL_BASIC_COMPUTE_INSTANCE_TYPE", "S5.MEDIUM4")
	events := &[]string{}
	row := billingReviewRow()
	row["id"], row["resourceType"], row["sizeGb"], row["computeAllocationId"] = "storage-review", "storage", int64(10), "compute-review"
	fabric := &monthlyFabric{events: events, storageSync: clients.StorageVolume{
		ID: "storage-review", AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", Status: "available", Provider: "tencent-tke",
		ProviderResourceID: "disk-storage-review", ProviderRequestID: "req-storage-review", SizeGB: 10, CBSStatus: "UNATTACHED", DiskType: "CLOUD_PREMIUM",
		Zone: "ap-shanghai-2", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2026-08-16T00:00:00Z",
		ProviderData: map[string]string{"chargeType": "PREPAID", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": "2026-08-16T00:00:00Z", "zone": "ap-shanghai-2"},
	}}
	sub2API := &monthlySub2API{events: events}
	ledger := &billingReviewLedger{}
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	mustStore(t, store.SaveStorage(context.Background(), row))
	server, err := NewPersistentServer(controlplane.NewService(ledger, fabric, sub2API), store)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"accountId":"acct-alpha","billingOperationId":"purchase-review-001","decision":"activate_charged_resource","evidenceRef":"case-20260716-storage"}`
	rec := requestWithMutationKeyForTest(t, server, reservedOperatorSessionForTest(t, server), http.MethodPost, "/api/operator/billing-reviews/storage/storage-review/resolve", body, "review-resolution-storage")
	rows, listErr := store.ListStorages(context.Background(), "acct-alpha")
	if rec.Code != http.StatusOK || listErr != nil || len(rows) != 1 || rows[0]["billingStatus"] != "active" || rows[0]["status"] != "available" || len(ledger.inputs) != 1 {
		t.Fatalf("storage resolution = %d %s rows=%#v err=%v receipts=%#v", rec.Code, rec.Body.String(), rows, listErr, ledger.inputs)
	}
}

func TestBillingReviewResolutionRefundsOnlyConfirmedChargedAbsentResource(t *testing.T) {
	absent := confirmedBillingReviewCompute("external_deleted")
	absent.ProviderResourceID, absent.InstanceID, absent.CVMInstanceID = "", "", ""
	h := newBillingReviewHarness(t, billingReviewRow(), absent)
	rec := billingReviewRequest(t, h, h.operator, "review-resolution-refund", "refund_charged_absent", "case-20260716-002")
	if rec.Code != http.StatusOK {
		t.Fatalf("refund resolution = %d %s", rec.Code, rec.Body.String())
	}
	row := billingReviewStoredRow(t, h.store)
	if row["billingStatus"] != "refunded" || row["status"] != "external_deleted" || row["autoRenew"] != false || len(h.sub2API.refunds) != 1 || len(h.ledger.inputs) != 1 || h.ledger.inputs[0].Type != "billing.resource_refunded.v1" {
		t.Fatalf("refund result row=%#v refunds=%#v receipts=%#v", row, h.sub2API.refunds, h.ledger.inputs)
	}
	refund := h.sub2API.refunds[0]
	if refund.Code != "opl:purchase-review-refund" || refund.UserID != 41 || refund.RefundUSDMicros != 50000000 {
		t.Fatalf("refund input = %#v", refund)
	}
}

func TestBillingReviewResolutionRetriesRefundWithStableDecisionAndKey(t *testing.T) {
	absent := confirmedBillingReviewCompute("external_deleted")
	absent.ProviderResourceID, absent.InstanceID, absent.CVMInstanceID = "", "", ""
	h := newBillingReviewHarness(t, billingReviewRow(), absent)
	h.sub2API.refundErrors = []error{errors.New("refund unavailable"), nil}

	first := billingReviewRequest(t, h, h.operator, "review-resolution-refund-retry", "refund_charged_absent", "case-20260716-retry")
	row := billingReviewStoredRow(t, h.store)
	if first.Code != http.StatusBadGateway || !strings.Contains(first.Body.String(), `"error":"billing_review_refund_pending"`) || len(h.sub2API.refunds) != 1 ||
		row["reviewResolutionDecision"] != "refund_charged_absent" || row["reviewResolutionPhase"] != "refund_pending" {
		t.Fatalf("first refund = %d %s row=%#v refunds=%#v", first.Code, first.Body.String(), row, h.sub2API.refunds)
	}
	switched := billingReviewRequest(t, h, h.operator, "review-resolution-refund-retry", "activate_charged_resource", "case-20260716-retry")
	if switched.Code != http.StatusConflict || len(h.sub2API.refunds) != 1 || billingReviewStoredRow(t, h.store)["reviewResolutionDecision"] != "refund_charged_absent" {
		t.Fatalf("decision switch = %d %s refunds=%#v", switched.Code, switched.Body.String(), h.sub2API.refunds)
	}
	second := billingReviewRequest(t, h, h.operator, "review-resolution-refund-retry", "refund_charged_absent", "case-20260716-retry")
	row = billingReviewStoredRow(t, h.store)
	if second.Code != http.StatusOK || len(h.sub2API.refunds) != 2 || h.sub2API.refunds[0] != h.sub2API.refunds[1] ||
		h.sub2API.refunds[0].Code != "opl:purchase-review-refund" || row["reviewResolutionDecision"] != "refund_charged_absent" || row["billingStatus"] != "refunded" {
		t.Fatalf("refund retry = %d %s row=%#v refunds=%#v", second.Code, second.Body.String(), row, h.sub2API.refunds)
	}
}

func TestBillingReviewResolutionAuditIDIncludesFullOperationIdentity(t *testing.T) {
	app := newControlPlaneAppEmpty()
	request := httptest.NewRequest(http.MethodPost, billingReviewResolvePath, nil)
	results := []map[string]any{
		{"resourceType": "compute", "resourceId": "compute-one", "accountId": "acct-alpha", "billingOperationId": "purchase-one", "resolvedAt": "2026-07-16T00:00:00Z"},
		{"resourceType": "storage", "resourceId": "storage-two", "accountId": "acct-alpha", "billingOperationId": "purchase-two", "resolvedAt": "2026-07-16T00:00:00Z"},
	}
	for _, result := range results {
		if err := app.appendBillingReviewResolutionAudit(request, "shared-resolution-key", result); err != nil {
			t.Fatal(err)
		}
	}
	audits, err := app.tables.ListAuditEvents(context.Background(), "acct-alpha")
	if err != nil || len(audits) != 2 {
		t.Fatalf("audits=%#v err=%v", audits, err)
	}
	wants := map[string]bool{
		"audit-" + stableID("billing.review.resolve", "compute", "compute-one", "purchase-one", "shared-resolution-key")[:12]: true,
		"audit-" + stableID("billing.review.resolve", "storage", "storage-two", "purchase-two", "shared-resolution-key")[:12]: true,
	}
	for _, audit := range audits {
		if !wants[stringValue(audit["id"])] {
			t.Fatalf("unexpected audit ID: %#v", audits)
		}
	}
}

func TestBillingReviewResolutionRejectsUnknownFactsAndRawEvidence(t *testing.T) {
	t.Run("invalid resource type", func(t *testing.T) {
		h := newBillingReviewHarness(t, billingReviewRow(), confirmedBillingReviewCompute("running"))
		body := `{"accountId":"acct-alpha","billingOperationId":"purchase-review-001","decision":"activate_charged_resource","evidenceRef":"case-20260716-000"}`
		rec := requestWithMutationKeyForTest(t, h.server, h.operator, http.MethodPost, "/api/operator/billing-reviews/network/compute-review/resolve", body, "review-resolution-resource-type")
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"error":"invalid_billing_review_request"`) || len(*h.events) != 0 || len(h.ledger.inputs) != 0 {
			t.Fatalf("invalid resource type = %d %s events=%#v", rec.Code, rec.Body.String(), *h.events)
		}
	})

	t.Run("unknown charge", func(t *testing.T) {
		row := billingReviewRow()
		row["postChargeBalanceKnown"], row["manualReviewReason"] = false, "sub2api_charge_unconfirmed"
		absent := confirmedBillingReviewCompute("external_deleted")
		h := newBillingReviewHarness(t, row, absent)
		rec := billingReviewRequest(t, h, h.operator, "review-resolution-unknown-charge", "refund_charged_absent", "case-20260716-003")
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"error":"billing_review_charge_fact_unconfirmed"`) || billingReviewStoredRow(t, h.store)["billingStatus"] != "manual_review" || len(h.sub2API.refunds) != 0 || len(h.ledger.inputs) != 0 {
			t.Fatalf("unknown charge resolution = %d %s refunds=%#v receipts=%#v", rec.Code, rec.Body.String(), h.sub2API.refunds, h.ledger.inputs)
		}
	})

	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "missing charge confirmation", mutate: func(row map[string]any) { delete(row, "sub2apiChargeConfirmation") }},
		{name: "wrong charge confirmation", mutate: func(row map[string]any) {
			row["sub2apiChargeConfirmation"] = map[string]any{"code": "opl:purchase-review-charge", "userId": int64(42), "chargeUsdMicros": int64(50000000), "status": "used"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := billingReviewRow()
			tc.mutate(row)
			h := newBillingReviewHarness(t, row, confirmedBillingReviewCompute("running"))
			rec := billingReviewRequest(t, h, h.operator, "review-resolution-charge-confirmation", "activate_charged_resource", "case-20260716-fact")
			if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"error":"billing_review_charge_fact_unconfirmed"`) ||
				billingReviewStoredRow(t, h.store)["billingStatus"] != "manual_review" || len(h.ledger.inputs) != 0 {
				t.Fatalf("charge confirmation = %d %s receipts=%#v", rec.Code, rec.Body.String(), h.ledger.inputs)
			}
		})
	}

	t.Run("partial provider", func(t *testing.T) {
		partial := confirmedBillingReviewCompute("provisioning")
		partial.ProviderResourceID, partial.InstanceID, partial.CVMInstanceID = "", "", ""
		h := newBillingReviewHarness(t, billingReviewRow(), partial)
		rec := billingReviewRequest(t, h, h.operator, "review-resolution-partial", "activate_charged_resource", "case-20260716-004")
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"error":"billing_review_provider_fact_unconfirmed"`) || billingReviewStoredRow(t, h.store)["billingStatus"] != "manual_review" || len(h.ledger.inputs) != 0 {
			t.Fatalf("partial provider resolution = %d %s receipts=%#v", rec.Code, rec.Body.String(), h.ledger.inputs)
		}
	})

	for _, evidenceRef := range []string{
		"https://evidence.example/raw",
		"case-20260716-ab",
		"case-20260716-abcdefghijklmnopq",
		"case-20260716-ab-c",
		"case-2026716-abc",
		"case-20260716-Abc",
	} {
		t.Run("invalid evidence "+evidenceRef, func(t *testing.T) {
			h := newBillingReviewHarness(t, billingReviewRow(), confirmedBillingReviewCompute("running"))
			rec := billingReviewRequest(t, h, h.operator, "review-resolution-evidence", "activate_charged_resource", evidenceRef)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"error":"invalid_evidence_ref"`) || len(*h.events) != 0 || len(h.ledger.inputs) != 0 {
				t.Fatalf("invalid evidence %q = %d %s events=%#v", evidenceRef, rec.Code, rec.Body.String(), *h.events)
			}
		})
	}
}

func TestBillingReviewResolutionRecoversAfterLedgerFailure(t *testing.T) {
	h := newBillingReviewHarness(t, billingReviewRow(), confirmedBillingReviewCompute("running"))
	h.ledger.errors = []error{errors.New("ledger unavailable"), nil}
	first := billingReviewRequest(t, h, h.operator, "review-resolution-retry", "activate_charged_resource", "case-20260716-005")
	if first.Code != http.StatusBadGateway || !strings.Contains(first.Body.String(), `"error":"billing_review_receipt_pending"`) {
		t.Fatalf("failed closing receipt = %d %s", first.Code, first.Body.String())
	}
	if row := billingReviewStoredRow(t, h.store); row["billingStatus"] != "manual_review" || row["reviewResolutionKey"] != "review-resolution-retry" {
		t.Fatalf("failed receipt state = %#v", row)
	}
	second := billingReviewRequest(t, h, h.operator, "review-resolution-retry", "activate_charged_resource", "case-20260716-005")
	if second.Code != http.StatusOK || billingReviewStoredRow(t, h.store)["billingStatus"] != "active" || len(h.ledger.inputs) != 2 || h.ledger.keys[0] != h.ledger.keys[1] {
		t.Fatalf("receipt recovery = %d %s keys=%#v", second.Code, second.Body.String(), h.ledger.keys)
	}
}

func TestBillingReviewResolutionActivatesConfirmedChargedRenewal(t *testing.T) {
	row := billingReviewRow()
	row["billingOperationId"] = "renewal-compute-review-001"
	row["sub2apiRedeemCode"] = "opl:renewal-review-charge"
	row["sub2apiChargeConfirmation"] = map[string]any{"code": "opl:renewal-review-charge", "userId": int64(41), "chargeUsdMicros": int64(50000000), "status": "used"}
	row["manualReviewReason"] = "fabric_renewal_unconfirmed"
	provider := confirmedBillingReviewCompute("running")
	provider.Deadline = "2026-09-16T00:00:00Z"
	provider.ProviderData["deadline"] = provider.Deadline
	h := newBillingReviewHarness(t, row, provider)
	body := `{"accountId":"acct-alpha","billingOperationId":"renewal-compute-review-001","decision":"activate_charged_resource","evidenceRef":"case-20260716-renewal"}`
	rec := requestWithMutationKeyForTest(t, h.server, h.operator, http.MethodPost, billingReviewResolvePath, body, "review-resolution-renewal")
	resolved := billingReviewStoredRow(t, h.store)
	if rec.Code != http.StatusOK || resolved["billingStatus"] != "active" || resolved["periodStart"] != "2026-08-16T00:00:00Z" || resolved["paidThrough"] != "2026-09-16T00:00:00Z" || len(h.ledger.inputs) != 1 || h.ledger.inputs[0].Type != "billing.resource_renewed.v1" {
		t.Fatalf("renewal resolution = %d %s row=%#v receipts=%#v", rec.Code, rec.Body.String(), resolved, h.ledger.inputs)
	}
}

func TestBillingReviewResolutionTerminatesKnownUnchargedAbsentRenewal(t *testing.T) {
	row := billingReviewRow()
	row["billingOperationId"] = "renewal-compute-review-001"
	row["postChargeBalanceKnown"] = false
	row["manualReviewReason"] = "fabric_renewal_provider_truth_invalid"
	delete(row, "sub2apiChargeConfirmation")
	absent := confirmedBillingReviewCompute("external_deleted")
	h := newBillingReviewHarness(t, row, absent)
	body := `{"accountId":"acct-alpha","billingOperationId":"renewal-compute-review-001","decision":"terminate_uncharged_absent","evidenceRef":"case-20260716-006"}`
	rec := requestWithMutationKeyForTest(t, h.server, h.operator, http.MethodPost, billingReviewResolvePath, body, "review-resolution-terminate")
	if rec.Code != http.StatusOK {
		t.Fatalf("terminate resolution = %d %s", rec.Code, rec.Body.String())
	}
	resolved := billingReviewStoredRow(t, h.store)
	if resolved["billingStatus"] != "failed" || resolved["status"] != "external_deleted" || resolved["autoRenew"] != false || len(h.sub2API.refunds) != 0 || len(h.ledger.inputs) != 1 || h.ledger.inputs[0].Type != "billing.reconciliation.v1" {
		t.Fatalf("terminate result row=%#v refunds=%#v receipts=%#v", resolved, h.sub2API.refunds, h.ledger.inputs)
	}
}

type failFinalBillingReviewStore struct {
	*memoryTableStore
	reviewSaves int
}

func (s *failFinalBillingReviewStore) SaveCompute(ctx context.Context, row map[string]any) error {
	if stringValue(row["reviewResolutionKey"]) != "" {
		s.reviewSaves++
		if s.reviewSaves == 3 {
			return errors.New("final review save failed")
		}
	}
	return s.memoryTableStore.SaveCompute(ctx, row)
}

func TestBillingReviewResolutionRecoversAfterFinalStateSaveFailure(t *testing.T) {
	t.Setenv("OPL_BASIC_COMPUTE_INSTANCE_TYPE", "S5.MEDIUM4")
	events := &[]string{}
	fabric := &monthlyFabric{events: events, computeSync: confirmedBillingReviewCompute("running")}
	sub2API := &monthlySub2API{events: events}
	ledger := &billingReviewLedger{}
	store := &failFinalBillingReviewStore{memoryTableStore: newMemoryTableStore()}
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	mustStore(t, store.SaveCompute(context.Background(), billingReviewRow()))
	server, err := NewPersistentServer(controlplane.NewService(ledger, fabric, sub2API), store)
	if err != nil {
		t.Fatal(err)
	}
	operator := reservedOperatorSessionForTest(t, server)
	body := `{"accountId":"acct-alpha","billingOperationId":"purchase-review-001","decision":"activate_charged_resource","evidenceRef":"case-20260716-save"}`
	first := requestWithMutationKeyForTest(t, server, operator, http.MethodPost, billingReviewResolvePath, body, "review-resolution-save")
	rows, _ := store.ListComputes(context.Background(), "acct-alpha")
	if first.Code != http.StatusInternalServerError || len(rows) != 1 || rows[0]["billingStatus"] != "manual_review" || rows[0]["reviewResolutionPhase"] != "receipt_recorded" || len(ledger.inputs) != 1 {
		t.Fatalf("failed final save = %d %s rows=%#v receipts=%#v", first.Code, first.Body.String(), rows, ledger.inputs)
	}
	second := requestWithMutationKeyForTest(t, server, operator, http.MethodPost, billingReviewResolvePath, body, "review-resolution-save")
	rows, _ = store.ListComputes(context.Background(), "acct-alpha")
	if second.Code != http.StatusOK || rows[0]["billingStatus"] != "active" || len(ledger.inputs) != 1 || strings.Count(strings.Join(*events, ","), "fabric.compute.sync") != 1 {
		t.Fatalf("final save recovery = %d %s rows=%#v events=%#v receipts=%#v", second.Code, second.Body.String(), rows, *events, ledger.inputs)
	}
}
