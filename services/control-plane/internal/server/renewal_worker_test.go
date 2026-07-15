package server

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

func monthlyActiveResource(resourceType, id string, paidThrough time.Time) map[string]any {
	status, providerID := "running", "ins-"+id
	if resourceType == "storage" {
		status, providerID = "available", "disk-"+id
	}
	row := map[string]any{
		"id": id, "accountId": "acct-monthly", "workspaceId": "workspace-monthly", "packageId": "basic", "status": status,
		"provider": "tencent-tke", "providerResourceId": providerID, "providerRequestId": "req-" + id,
		"billingStatus": "active", "billingOperationId": "purchase-" + id, "billingOperationStartedAt": paidThrough.AddDate(0, -1, 0).Format(time.RFC3339),
		"sub2apiRedeemCode": "opl:test:purchase-" + id + ":charge:v1", "pricingVersion": pricingCatalogVersion,
		"monthlyPriceCnyCents": int64(35000), "chargeUsdMicros": int64(50_000_000), "billingAnchorDay": int64(paidThrough.Day()),
		"periodStart": paidThrough.AddDate(0, -1, 0).Format(time.RFC3339), "paidThrough": paidThrough.Format(time.RFC3339),
		"autoRenew": true, "lastReceiptId": "receipt-purchase-" + id, "postChargeBalanceKnown": true, "postChargeBalanceUsdMicros": int64(100_000_000),
	}
	if resourceType == "storage" {
		row["sizeGb"] = 10
		row["monthlyPriceCnyCents"] = int64(1800)
		row["chargeUsdMicros"] = int64(2_571_429)
	}
	return row
}

func TestBillingMonthClampsWithoutChangingAnchor(t *testing.T) {
	jan31 := time.Date(2025, 1, 31, 9, 30, 0, 0, time.UTC)
	feb := nextBillingMonth(jan31, 31)
	if feb != time.Date(2025, 2, 28, 9, 30, 0, 0, time.UTC) || nextBillingMonth(feb, 31) != time.Date(2025, 3, 31, 9, 30, 0, 0, time.UTC) {
		t.Fatalf("month clamp failed: feb=%s march=%s", feb, nextBillingMonth(feb, 31))
	}
	if leap := nextBillingMonth(time.Date(2024, 1, 31, 9, 30, 0, 0, time.UTC), 31); leap.Day() != 29 || leap.Month() != time.February {
		t.Fatalf("leap renewal = %s", leap)
	}
}

func TestMonthlyRenewalStartsAtLeadTimeAndDoesNotDuplicate(t *testing.T) {
	paidThrough := time.Date(2026, 8, 31, 9, 30, 0, 0, time.UTC)
	app, service, sub2API, fabric, ledger, events := newMonthlyBillingTest(t, []int64{50_000_000, 0})
	row := monthlyActiveResource("compute", "compute-renew", paidThrough)
	row["billingAnchorDay"] = int64(31)
	if err := app.tables.SaveCompute(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	if err := app.runMonthlyBillingOnce(context.Background(), service, paidThrough.Add(-24*time.Hour-time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(*events) != 0 {
		t.Fatalf("renewed before lead time: %#v", *events)
	}
	if err := app.runMonthlyBillingOnce(context.Background(), service, paidThrough.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	renewed, _ := app.getCompute("compute-renew")
	if renewed["billingStatus"] != "active" || renewed["postChargeBalanceUsdMicros"] != int64(0) || renewed["periodStart"] != paidThrough.Format(time.RFC3339) || renewed["paidThrough"] != "2026-09-30T09:30:00Z" {
		t.Fatalf("renewed row = %#v", renewed)
	}
	if len(sub2API.charges) != 1 || len(ledger.receipts) != 1 || ledger.receipts[0].Type != "billing.resource_renewed.v1" {
		t.Fatalf("charges=%#v receipts=%#v", sub2API.charges, ledger.receipts)
	}
	if len(fabric.computeIDs) != 0 || len(fabric.storageIDs) != 0 || strings.Contains(strings.Join(*events, ","), "fabric.") {
		t.Fatalf("renewal touched Fabric: %#v", *events)
	}
	if err := app.runMonthlyBillingOnce(context.Background(), service, paidThrough.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(sub2API.charges) != 1 || len(ledger.receipts) != 1 {
		t.Fatalf("duplicate renewal: charges=%d receipts=%d", len(sub2API.charges), len(ledger.receipts))
	}
}

func TestMonthlyRenewalInsufficientBalanceKeepsCurrentEntitlement(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	paidThrough := now.Add(12 * time.Hour)
	app, service, sub2API, _, _, _ := newMonthlyBillingTest(t, []int64{40_000_000})
	if err := app.tables.SaveCompute(context.Background(), monthlyActiveResource("compute", "compute-low-renewal", paidThrough)); err != nil {
		t.Fatal(err)
	}
	if err := app.runMonthlyBillingOnce(context.Background(), service, now); err != nil {
		t.Fatal(err)
	}
	row, _ := app.getCompute("compute-low-renewal")
	if row["billingStatus"] != "past_due" || row["paidThrough"] != paidThrough.Format(time.RFC3339) || !monthlyEntitlementActive(row, now) || len(sub2API.charges) != 0 {
		t.Fatalf("insufficient renewal row=%#v charges=%#v", row, sub2API.charges)
	}
}

func TestMonthlyRenewalUnknownChargeRequiresReview(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	paidThrough := now.Add(12 * time.Hour)
	app, service, sub2API, _, ledger, _ := newMonthlyBillingTest(t, []int64{100_000_000})
	sub2API.chargeErrors = []error{clients.ErrSub2APIChargeUnknown}
	if err := app.tables.SaveCompute(context.Background(), monthlyActiveResource("compute", "compute-review-renewal", paidThrough)); err != nil {
		t.Fatal(err)
	}
	if err := app.runMonthlyBillingOnce(context.Background(), service, now); err != nil {
		t.Fatal(err)
	}
	row, _ := app.getCompute("compute-review-renewal")
	if row["billingStatus"] != "manual_review" || row["paidThrough"] != paidThrough.Format(time.RFC3339) || len(sub2API.charges) != 1 || len(ledger.receipts) != 0 {
		t.Fatalf("unknown renewal row=%#v charges=%#v receipts=%#v", row, sub2API.charges, ledger.receipts)
	}
}

func TestMonthlyAutoRenewDisabledWaitsForExpiry(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	app, service, _, _, _, events := newMonthlyBillingTest(t, nil)
	row := monthlyActiveResource("compute", "compute-no-renew", now.Add(12*time.Hour))
	row["autoRenew"] = false
	if err := app.tables.SaveCompute(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	if err := app.runMonthlyBillingOnce(context.Background(), service, now); err != nil {
		t.Fatal(err)
	}
	current, _ := app.getCompute("compute-no-renew")
	if current["billingStatus"] != "active" || len(*events) != 0 {
		t.Fatalf("disabled renewal changed early: row=%#v events=%#v", current, *events)
	}
}

func TestMonthlyExpiryDestroysComputeAndRetainsStorage(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	app, service, _, _, ledger, events := newMonthlyBillingTest(t, nil)
	compute := monthlyActiveResource("compute", "compute-expired", now)
	storage := monthlyActiveResource("storage", "storage-expired", now)
	compute["autoRenew"], storage["autoRenew"] = false, false
	if err := app.tables.SaveCompute(context.Background(), compute); err != nil {
		t.Fatal(err)
	}
	if err := app.tables.SaveStorage(context.Background(), storage); err != nil {
		t.Fatal(err)
	}
	if err := app.runMonthlyBillingOnce(context.Background(), service, now); err != nil {
		t.Fatal(err)
	}
	expiredCompute, _ := app.getCompute("compute-expired")
	retainedStorage, _ := app.getStorage("storage-expired")
	if expiredCompute["billingStatus"] != "stopped" || expiredCompute["status"] != "destroyed" || retainedStorage["billingStatus"] != "retained" || retainedStorage["status"] != "available" {
		t.Fatalf("compute=%#v storage=%#v", expiredCompute, retainedStorage)
	}
	if monthlyEntitlementActive(expiredCompute, now) || monthlyEntitlementActive(retainedStorage, now) || strings.Count(strings.Join(*events, ","), "fabric.compute.cleanup") != 1 {
		t.Fatalf("expiry events=%#v", *events)
	}
	if len(ledger.receipts) != 2 || ledger.receipts[0].Type != "billing.resource_expired.v1" || ledger.receipts[1].Type != "billing.resource_expired.v1" {
		t.Fatalf("expiry receipts=%#v", ledger.receipts)
	}
	if err := app.runMonthlyBillingOnce(context.Background(), service, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(ledger.receipts) != 2 || strings.Count(strings.Join(*events, ","), "fabric.compute.cleanup") != 1 {
		t.Fatalf("duplicate expiry events=%#v receipts=%#v", *events, ledger.receipts)
	}
}

func TestRetainedStorageReactivatesFromCurrentTimeOnly(t *testing.T) {
	now := time.Date(2026, 8, 3, 10, 15, 0, 0, time.UTC)
	events := &[]string{}
	sub2API := &monthlySub2API{events: events, balances: []int64{100_000_000, 100_000_000, 92_285_714}}
	fabric := &provisioningMonthlyFabric{monthlyFabric: monthlyFabric{events: events}}
	ledger := &monthlyLedger{events: events}
	app := newControlPlaneAppEmpty()
	if err := app.tables.SaveAccount(context.Background(), map[string]any{"id": "acct-monthly", "status": "active", "sub2apiUserId": int64(41)}); err != nil {
		t.Fatal(err)
	}
	retained := monthlyActiveResource("storage", "storage-retained", now.Add(-time.Hour))
	retained["billingStatus"] = "retained"
	retained["sizeGb"], retained["monthlyPriceCnyCents"], retained["chargeUsdMicros"] = 30, int64(5400), int64(7_714_286)
	if err := app.tables.SaveStorage(context.Background(), retained); err != nil {
		t.Fatal(err)
	}
	service := controlplane.NewService(ledger, fabric, sub2API)
	result, err := app.purchaseMonthlyResource(context.Background(), service, monthlyPurchaseInput{ResourceType: "storage", ResourceID: "storage-retained", BillingOperationID: "reactivate-storage-retained", AccountID: "acct-monthly", WorkspaceID: "workspace-monthly", PackageID: "basic", SizeGB: 10, Environment: "test", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if result["billingStatus"] != "active" || result["periodStart"] != now.Format(time.RFC3339) || result["paidThrough"] != "2026-09-03T10:15:00Z" || int64(numberField(result, "sizeGb", 0)) != 30 || int64(numberField(result, "chargeUsdMicros", 0)) != 7_714_286 || len(fabric.storageIDs) != 0 || fabric.syncCalls != 1 || len(sub2API.charges) != 1 || sub2API.charges[0].ChargeUSDMicros != 7_714_286 {
		t.Fatalf("reactivated=%#v creates=%#v syncs=%d charges=%#v", result, fabric.storageIDs, fabric.syncCalls, sub2API.charges)
	}
	beforeEvents := len(*events)
	if _, err := app.purchaseMonthlyResource(context.Background(), service, monthlyPurchaseInput{ResourceType: "storage", ResourceID: "storage-retained", BillingOperationID: "replace-active-storage", AccountID: "acct-monthly", WorkspaceID: "workspace-monthly", PackageID: "basic", SizeGB: 10, Environment: "test", Now: now}); !errors.Is(err, errIdempotencyConflict) {
		t.Fatalf("active resource replacement err=%v", err)
	}
	if len(*events) != beforeEvents {
		t.Fatalf("active replacement made external calls: %#v", *events)
	}
}

func TestRetainedStorageRejectsCrossAccountReactivationBeforeExternalCalls(t *testing.T) {
	now := time.Date(2026, 8, 3, 10, 15, 0, 0, time.UTC)
	events := &[]string{}
	app := newControlPlaneAppEmpty()
	mustStore(t, app.tables.SaveAccount(context.Background(), map[string]any{"id": "acct-other", "status": "active", "sub2apiUserId": int64(42)}))
	retained := monthlyActiveResource("storage", "storage-retained", now.Add(-time.Hour))
	retained["billingStatus"] = "retained"
	mustStore(t, app.tables.SaveStorage(context.Background(), retained))
	service := controlplane.NewService(&monthlyLedger{events: events}, &monthlyFabric{events: events}, &monthlySub2API{events: events, balances: []int64{100_000_000}})

	_, err := app.purchaseMonthlyResource(context.Background(), service, monthlyPurchaseInput{ResourceType: "storage", ResourceID: "storage-retained", BillingOperationID: "cross-account-reactivation", AccountID: "acct-other", PackageID: "basic", SizeGB: 10, Environment: "test", Now: now})
	if !errors.Is(err, errIdempotencyConflict) || len(*events) != 0 {
		t.Fatalf("cross-account reactivation err=%v events=%#v", err, *events)
	}
}
