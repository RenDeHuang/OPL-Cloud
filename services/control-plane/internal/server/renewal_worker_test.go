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
		"chargeType": "PREPAID", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": paidThrough.Format(time.RFC3339), "zone": "ap-shanghai-2",
		"providerData": map[string]any{"chargeType": "PREPAID", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": paidThrough.Format(time.RFC3339), "zone": "ap-shanghai-2"},
	}
	if resourceType == "storage" {
		row["sizeGb"] = 10
		row["monthlyPriceCnyCents"] = int64(1800)
		row["chargeUsdMicros"] = int64(2_571_429)
	} else {
		row["instanceId"], row["cvmInstanceId"] = providerID, providerID
		row["instanceType"] = "S5.MEDIUM4"
		row["providerData"].(map[string]any)["instanceType"] = "S5.MEDIUM4"
	}
	return row
}

func TestMonthlyRenewalPreflightStopsBeforeFinancialAndProviderCalls(t *testing.T) {
	now := time.Date(2026, 8, 30, 9, 30, 0, 0, time.UTC)
	paidThrough := now.Add(24 * time.Hour)
	for _, tc := range []struct {
		resourceType string
		configure    func(*monthlyFabric)
	}{
		{
			resourceType: "compute",
			configure: func(fabric *monthlyFabric) {
				fabric.preflightErr = errors.New("capacity unavailable")
			},
		},
		{
			resourceType: "storage",
			configure: func(fabric *monthlyFabric) {
				fabric.preflightResult = &clients.MonthlyPreflight{
					ResourceType: "storage", PackageID: "basic", SizeGB: 10, Zone: "ap-shanghai-2",
					Available: true, ChargeType: "PREPAID", PeriodMonths: 1,
					RenewFlag: "NOTIFY_AND_MANUAL_RENEW", ProviderPriceCNY: 12.34,
				}
			},
		},
	} {
		t.Run(tc.resourceType, func(t *testing.T) {
			app, service, sub2API, fabric, _, events := newMonthlyBillingTest(t, nil)
			tc.configure(fabric)
			row := monthlyActiveResource(tc.resourceType, tc.resourceType+"-preflight", paidThrough)
			if tc.resourceType == "storage" {
				mustStore(t, app.tables.SaveStorage(context.Background(), row))
			} else {
				mustStore(t, app.tables.SaveCompute(context.Background(), row))
			}

			result, err := app.renewMonthlyResource(context.Background(), service, tc.resourceType, row, now)
			if err == nil || result["billingStatus"] != "renewal_pending" {
				t.Fatalf("result=%#v err=%v", result, err)
			}
			if len(fabric.preflightInputs) != 1 {
				t.Fatalf("preflight inputs=%#v", fabric.preflightInputs)
			}
			input := fabric.preflightInputs[0]
			if input.ResourceType != tc.resourceType || input.PackageID != "basic" || input.Zone != "ap-shanghai-2" || input.SizeGB != int(numberField(row, "sizeGb", 0)) {
				t.Fatalf("preflight input=%#v row=%#v", input, row)
			}
			if strings.Join(*events, ",") != "fabric.monthly.preflight" || len(sub2API.charges) != 0 || len(fabric.computeRenewKeys) != 0 || len(fabric.storageRenewKeys) != 0 {
				t.Fatalf("events=%#v charges=%#v compute renews=%#v storage renews=%#v", *events, sub2API.charges, fabric.computeRenewKeys, fabric.storageRenewKeys)
			}
		})
	}
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
	for _, resourceType := range []string{"compute", "storage"} {
		t.Run(resourceType, func(t *testing.T) {
			charge := int64(50_000_000)
			if resourceType == "storage" {
				charge = 2_571_429
			}
			app, service, sub2API, fabric, ledger, events := newMonthlyBillingTest(t, []int64{charge, 0})
			id := resourceType + "-renew"
			row := monthlyActiveResource(resourceType, id, paidThrough)
			row["billingAnchorDay"] = int64(31)
			if resourceType == "storage" {
				mustStore(t, app.tables.SaveStorage(context.Background(), row))
			} else {
				mustStore(t, app.tables.SaveCompute(context.Background(), row))
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
			renewed, _ := app.monthlyResource(resourceType, id)
			if renewed["billingStatus"] != "active" || renewed["postChargeBalanceUsdMicros"] != int64(0) || renewed["periodStart"] != paidThrough.Format(time.RFC3339) || renewed["paidThrough"] != "2026-09-30T09:30:00Z" || stringValue(renewed["deadline"]) != "2026-09-30T09:30:00Z" {
				t.Fatalf("renewed row = %#v", renewed)
			}
			if len(sub2API.charges) != 1 || len(ledger.receipts) != 1 || ledger.receipts[0].Type != "billing.resource_renewed.v1" {
				t.Fatalf("renewed=%#v charges=%#v receipts=%#v events=%#v", renewed, sub2API.charges, ledger.receipts, *events)
			}
			renewKeys, renewEvent := fabric.computeRenewKeys, "fabric.compute.renew"
			if resourceType == "storage" {
				renewKeys, renewEvent = fabric.storageRenewKeys, "fabric.storage.renew"
			}
			operationID := "renewal-" + stableID(resourceType, id, paidThrough.Format(time.RFC3339))[:18]
			wantEvents := []string{"fabric.monthly.preflight", "sub2api.balance", "sub2api.charge", "sub2api.balance", renewEvent, "ledger.receipt"}
			if len(renewKeys) != 1 || renewKeys[0] != operationID+":provider-renew" || strings.Join(*events, ",") != strings.Join(wantEvents, ",") {
				t.Fatalf("renewal keys=%#v events=%#v", renewKeys, *events)
			}
			if err := app.runMonthlyBillingOnce(context.Background(), service, paidThrough.Add(-24*time.Hour)); err != nil {
				t.Fatal(err)
			}
			if len(sub2API.charges) != 1 || len(renewKeys) != 1 || len(ledger.receipts) != 1 {
				t.Fatalf("duplicate renewal: charges=%d renewals=%d receipts=%d", len(sub2API.charges), len(renewKeys), len(ledger.receipts))
			}
		})
	}
}

func TestMonthlyAutoRenewDefaultsOff(t *testing.T) {
	if monthlyAutoRenew(map[string]any{}) {
		t.Fatal("missing autoRenew must default to false")
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
	if row["billingStatus"] != "manual_review" || row["paidThrough"] != paidThrough.Format(time.RFC3339) || len(sub2API.charges) != 1 || len(ledger.receipts) != 1 || ledger.receipts[0].Type != "billing.charge_review_required.v1" {
		t.Fatalf("unknown renewal row=%#v charges=%#v receipts=%#v", row, sub2API.charges, ledger.receipts)
	}
}

func TestMonthlyRenewalUnknownOrPartialProviderResultNeedsReview(t *testing.T) {
	now := time.Date(2026, 8, 30, 9, 30, 0, 0, time.UTC)
	paidThrough := now.Add(24 * time.Hour)
	for _, resourceType := range []string{"compute", "storage"} {
		t.Run(resourceType, func(t *testing.T) {
			charge, postCharge := int64(50_000_000), int64(50_000_000)
			if resourceType == "storage" {
				charge, postCharge = 2_571_429, 97_428_571
			}
			app, service, sub2API, fabric, ledger, events := newMonthlyBillingTest(t, []int64{100_000_000, postCharge})
			id := resourceType + "-provider-review"
			row := monthlyActiveResource(resourceType, id, paidThrough)
			row["chargeUsdMicros"] = charge
			if resourceType == "storage" {
				fabric.storageRenew = clients.StorageVolume{ID: id, Status: "available", ProviderRequestID: "partial-" + id, RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: paidThrough.Format(time.RFC3339), ProviderData: map[string]string{"chargeType": "PREPAID"}}
				mustStore(t, app.tables.SaveStorage(context.Background(), row))
			} else {
				fabric.computeRenewErr = errors.New("renew readback unavailable")
				mustStore(t, app.tables.SaveCompute(context.Background(), row))
			}
			if err := app.runMonthlyBillingOnce(context.Background(), service, now); err != nil {
				t.Fatal(err)
			}
			review, _ := app.monthlyResource(resourceType, id)
			if review["billingStatus"] != "manual_review" || review["paidThrough"] != paidThrough.Format(time.RFC3339) || len(sub2API.charges) != 1 || len(sub2API.refunds) != 0 || len(ledger.receipts) != 1 || ledger.receipts[0].Type != "billing.charge_review_required.v1" {
				t.Fatalf("provider review row=%#v charges=%#v refunds=%#v receipts=%#v", review, sub2API.charges, sub2API.refunds, ledger.receipts)
			}
			before := len(*events)
			if err := app.runMonthlyBillingOnce(context.Background(), service, now); err != nil {
				t.Fatal(err)
			}
			if len(*events) != before || len(sub2API.charges) != 1 {
				t.Fatalf("manual review retried provider: events=%#v charges=%#v", *events, sub2API.charges)
			}
		})
	}
}

func TestMonthlyRenewalRejectsProviderIdentityDrift(t *testing.T) {
	now := time.Date(2026, 8, 30, 9, 30, 0, 0, time.UTC)
	paidThrough := now.Add(24 * time.Hour)
	renewedThrough := nextBillingMonth(paidThrough, paidThrough.Day())
	tests := []struct {
		name         string
		resourceType string
		mutate       func(*monthlyFabric, string)
	}{
		{name: "compute provider resource", resourceType: "compute", mutate: func(f *monthlyFabric, id string) { f.computeRenew.ProviderResourceID = "ins-other" }},
		{name: "compute zone", resourceType: "compute", mutate: func(f *monthlyFabric, _ string) {
			f.computeRenew.Zone, f.computeRenew.ProviderData["zone"] = "ap-shanghai-3", "ap-shanghai-3"
		}},
		{name: "compute package", resourceType: "compute", mutate: func(f *monthlyFabric, _ string) { f.computeRenew.PackageID = "pro" }},
		{name: "compute instance type", resourceType: "compute", mutate: func(f *monthlyFabric, _ string) {
			f.computeRenew.InstanceType, f.computeRenew.ProviderData["instanceType"] = "SA5.2XLARGE16", "SA5.2XLARGE16"
		}},
		{name: "compute instance identity", resourceType: "compute", mutate: func(f *monthlyFabric, _ string) {
			f.computeRenew.InstanceID, f.computeRenew.CVMInstanceID = "ins-other", "ins-other"
		}},
		{name: "storage provider resource", resourceType: "storage", mutate: func(f *monthlyFabric, _ string) { f.storageRenew.ProviderResourceID = "disk-other" }},
		{name: "storage zone", resourceType: "storage", mutate: func(f *monthlyFabric, _ string) {
			f.storageRenew.Zone, f.storageRenew.ProviderData["zone"] = "ap-shanghai-3", "ap-shanghai-3"
		}},
		{name: "storage size", resourceType: "storage", mutate: func(f *monthlyFabric, _ string) { f.storageRenew.SizeGB = 20 }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			charge, postCharge := int64(50_000_000), int64(50_000_000)
			if tc.resourceType == "storage" {
				charge, postCharge = 2_571_429, 97_428_571
			}
			app, service, sub2API, fabric, ledger, _ := newMonthlyBillingTest(t, []int64{100_000_000, postCharge})
			id := tc.resourceType + "-identity-drift"
			row := monthlyActiveResource(tc.resourceType, id, paidThrough)
			row["chargeUsdMicros"] = charge
			if tc.resourceType == "storage" {
				fabric.storageRenew = clients.StorageVolume{ID: id, AccountID: "acct-monthly", WorkspaceID: "workspace-monthly", Status: "available", ProviderResourceID: "disk-" + id, ProviderRequestID: "renew-" + id, CBSStatus: "UNATTACHED", SizeGB: 10, Zone: "ap-shanghai-2", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: renewedThrough.Format(time.RFC3339), ProviderData: map[string]string{"chargeType": "PREPAID", "renewalResult": "renewed", "zone": "ap-shanghai-2"}}
				mustStore(t, app.tables.SaveStorage(context.Background(), row))
			} else {
				fabric.computeRenew = clients.ComputeAllocation{ID: id, AccountID: "acct-monthly", WorkspaceID: "workspace-monthly", PackageID: "basic", Status: "running", ProviderResourceID: "ins-" + id, ProviderRequestID: "renew-" + id, InstanceID: "ins-" + id, CVMInstanceID: "ins-" + id, InstanceType: "S5.MEDIUM4", Zone: "ap-shanghai-2", ChargeType: "PREPAID", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: renewedThrough.Format(time.RFC3339), ProviderData: map[string]string{"chargeType": "PREPAID", "renewalResult": "renewed", "zone": "ap-shanghai-2", "instanceType": "S5.MEDIUM4"}}
				mustStore(t, app.tables.SaveCompute(context.Background(), row))
			}
			tc.mutate(fabric, id)

			result, err := app.renewMonthlyResource(context.Background(), service, tc.resourceType, row, now)
			if !errors.Is(err, errMonthlyChargeNeedsReview) || result["billingStatus"] != "manual_review" || result["paidThrough"] != paidThrough.Format(time.RFC3339) {
				t.Fatalf("identity drift activated renewal: result=%#v err=%v", result, err)
			}
			if stringValue(result["providerResourceId"]) != stringValue(row["providerResourceId"]) || len(sub2API.refunds) != 0 || len(ledger.receipts) != 1 || ledger.receipts[0].Type != "billing.charge_review_required.v1" {
				t.Fatalf("identity drift overwrote truth or evidence: result=%#v refunds=%#v receipts=%#v", result, sub2API.refunds, ledger.receipts)
			}
		})
	}
}

func TestMonthlyRenewalConfirmedAbsenceRefundsOnce(t *testing.T) {
	now := time.Date(2026, 8, 30, 9, 30, 0, 0, time.UTC)
	paidThrough := now.Add(24 * time.Hour)
	for _, resourceType := range []string{"compute", "storage"} {
		t.Run(resourceType, func(t *testing.T) {
			charge, postCharge := int64(50_000_000), int64(50_000_000)
			if resourceType == "storage" {
				charge, postCharge = 2_571_429, 97_428_571
			}
			app, service, sub2API, fabric, ledger, events := newMonthlyBillingTest(t, []int64{100_000_000, postCharge})
			id := resourceType + "-renew-absent"
			row := monthlyActiveResource(resourceType, id, paidThrough)
			row["chargeUsdMicros"] = charge
			row["sub2apiRefundCode"] = "stale-purchase-refund-code"
			if resourceType == "storage" {
				fabric.storageRenewErr = errors.New("renew response unavailable")
				fabric.storageSync = clients.StorageVolume{
					ID: id, AccountID: "acct-monthly", WorkspaceID: "workspace-monthly",
					Status: "external_deleted", CBSStatus: "NOT_FOUND",
				}
				mustStore(t, app.tables.SaveStorage(context.Background(), row))
			} else {
				fabric.computeRenewErr = errors.New("renew response unavailable")
				fabric.computeSync = clients.ComputeAllocation{
					ID: id, AccountID: "acct-monthly", WorkspaceID: "workspace-monthly",
					Status: "external_deleted",
				}
				mustStore(t, app.tables.SaveCompute(context.Background(), row))
			}

			if err := app.runMonthlyBillingOnce(context.Background(), service, now); err != nil {
				t.Fatal(err)
			}
			refunded, _ := app.monthlyResource(resourceType, id)
			operationID := "renewal-" + stableID(resourceType, id, paidThrough.Format(time.RFC3339))[:18]
			if refunded["billingStatus"] != "refunded" || refunded["paidThrough"] != paidThrough.Format(time.RFC3339) || len(sub2API.charges) != 1 || len(sub2API.refunds) != 1 || len(ledger.receipts) != 1 || ledger.receipts[0].Type != "billing.resource_refunded.v1" {
				t.Fatalf("refunded=%#v charges=%#v refunds=%#v receipts=%#v events=%#v", refunded, sub2API.charges, sub2API.refunds, ledger.receipts, *events)
			}
			if sub2API.refunds[0].Code != monthlyRefundCode(monthlyEnvironment(), operationID) || sub2API.refunds[0].RefundUSDMicros != charge {
				t.Fatalf("refund=%#v operation=%s", sub2API.refunds[0], operationID)
			}
			before := len(*events)
			if err := app.runMonthlyBillingOnce(context.Background(), service, now); err != nil {
				t.Fatal(err)
			}
			if len(*events) != before || len(sub2API.charges) != 1 || len(sub2API.refunds) != 1 {
				t.Fatalf("refund replay duplicated work: events=%#v charges=%#v refunds=%#v", *events, sub2API.charges, sub2API.refunds)
			}
		})
	}
}

func TestMonthlyRenewalStorageDoesNotRefundWithoutCBSNotFound(t *testing.T) {
	now := time.Date(2026, 8, 30, 9, 30, 0, 0, time.UTC)
	paidThrough := now.Add(24 * time.Hour)
	app, service, sub2API, fabric, _, events := newMonthlyBillingTest(t, []int64{100_000_000, 97_428_571})
	row := monthlyActiveResource("storage", "storage-renew-attached", paidThrough)
	fabric.storageRenewErr = errors.New("renew response unavailable")
	fabric.storageSync = clients.StorageVolume{
		ID: "storage-renew-attached", AccountID: "acct-monthly", WorkspaceID: "workspace-monthly",
		Status: "external_deleted", CBSStatus: "ATTACHED",
	}
	mustStore(t, app.tables.SaveStorage(context.Background(), row))

	if err := app.runMonthlyBillingOnce(context.Background(), service, now); err != nil {
		t.Fatal(err)
	}
	review, _ := app.getStorage("storage-renew-attached")
	if review["billingStatus"] != "manual_review" || len(sub2API.charges) != 1 || len(sub2API.refunds) != 0 || strings.Count(strings.Join(*events, ","), "fabric.storage.sync") != 1 {
		t.Fatalf("review=%#v charges=%#v refunds=%#v events=%#v", review, sub2API.charges, sub2API.refunds, *events)
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
	sub2API := &monthlySub2API{events: events, balances: []int64{100_000_000, 92_285_714}}
	fabric := &provisioningMonthlyFabric{monthlyFabric: monthlyFabric{events: events}}
	fabric.storageInput = clients.StorageVolumeInput{ID: "storage-retained", AccountID: "acct-monthly", WorkspaceID: "workspace-monthly", ComputeID: "compute-retained", Zone: "ap-shanghai-2", SizeGB: 30}
	ledger := &monthlyLedger{events: events}
	app := newControlPlaneAppEmpty()
	if err := app.tables.SaveAccount(context.Background(), map[string]any{"id": "acct-monthly", "status": "active", "sub2apiUserId": int64(41)}); err != nil {
		t.Fatal(err)
	}
	retained := monthlyActiveResource("storage", "storage-retained", now.Add(-time.Hour))
	retained["billingStatus"] = "retained"
	retained["sizeGb"], retained["monthlyPriceCnyCents"], retained["chargeUsdMicros"] = 30, int64(5400), int64(7_714_286)
	retained["computeAllocationId"], retained["zone"] = "compute-retained", "ap-shanghai-2"
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
