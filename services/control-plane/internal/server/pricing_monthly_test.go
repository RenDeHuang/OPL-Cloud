package server

import (
	"context"
	"errors"
	"testing"
)

const pilotPriceVersion = "pilot-usd-2026-07-v1"

func customerPricingPreview(t *testing.T, input map[string]any) map[string]any {
	t.Helper()
	preview, err := newControlPlaneAppEmpty().pricingPreviewResponse(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	return preview
}

func assertCustomerUSDPrice(t *testing.T, dto map[string]any, usdMicros int64) {
	t.Helper()
	if dto["priceVersion"] != pilotPriceVersion || dto["currency"] != "USD" || dto["chargeUsdMicros"] != usdMicros {
		t.Fatalf("customer USD price = %#v", dto)
	}
	if _, ok := dto["pricingVersion"]; ok {
		t.Fatalf("legacy pricingVersion leaked to customer DTO: %#v", dto)
	}
	if _, ok := dto["monthlyPriceCnyCents"]; ok {
		t.Fatalf("internal CNY cost leaked to customer DTO: %#v", dto)
	}
}

func TestMonthlyPricingCatalogUsesFixedPilotUSDPrices(t *testing.T) {
	catalog := pricingCatalogResponse()
	if catalog["priceVersion"] != pilotPriceVersion || catalog["billingUnit"] != "calendar_month" {
		t.Fatalf("monthly catalog identity = %#v", catalog)
	}
	if catalog["displayCurrency"] != "USD" || catalog["currency"] != "USD" || catalog["walletCurrency"] != "USD" {
		t.Fatalf("monthly catalog currencies = %#v", catalog)
	}
	if _, ok := catalog["pricingVersion"]; ok {
		t.Fatalf("legacy pricingVersion leaked to catalog: %#v", catalog)
	}
	if _, ok := catalog["exchangeRateCnyPerUsd"]; ok {
		t.Fatalf("exchange-rate pricing leaked to catalog: %#v", catalog)
	}

	packages, _ := catalog["packages"].([]any)
	if len(packages) != 2 {
		t.Fatalf("packages = %#v", packages)
	}
	assertCharge := func(index int, packageID string, usdMicros int64) {
		t.Helper()
		row, _ := packages[index].(map[string]any)
		price, _ := row["price"].(map[string]any)
		if row["id"] != packageID || row["available"] != true || price["priceVersion"] != pilotPriceVersion || price["currency"] != "USD" || price["chargeUsdMicros"] != usdMicros {
			t.Fatalf("%s price = %#v", packageID, row)
		}
		if _, ok := price["monthlyPriceCnyCents"]; ok {
			t.Fatalf("internal CNY cost leaked to %s: %#v", packageID, row)
		}
	}
	assertCharge(0, "basic", 50_000_000)
	assertCharge(1, "pro", 214_280_000)
	storagePrice := mapField(catalog, "storagePer10GbMonthly")
	if storagePrice["priceVersion"] != pilotPriceVersion || storagePrice["currency"] != "USD" || storagePrice["usdMicros"] != int64(2_580_000) {
		t.Fatalf("default storage price = %#v", storagePrice)
	}
	if _, ok := storagePrice["cnyCents"]; ok {
		t.Fatalf("internal CNY cost leaked to storage price: %#v", storagePrice)
	}

	statePackages, _ := newControlPlaneAppEmpty().state("", nil)["packages"].([]any)
	if len(statePackages) != 2 || mapField(statePackages[0].(map[string]any), "price")["chargeUsdMicros"] != int64(50_000_000) || mapField(statePackages[1].(map[string]any), "price")["chargeUsdMicros"] != int64(214_280_000) {
		t.Fatalf("state packages = %#v", statePackages)
	}
}

func TestMonthlyStoragePriceUsesFixedUSDComponents(t *testing.T) {
	for _, tc := range []struct {
		sizeGB    int
		usdMicros int64
	}{{10, 2_580_000}, {100, 25_800_000}} {
		preview := customerPricingPreview(t, map[string]any{"resourceType": "storage", "packageId": "basic", "sizeGb": tc.sizeGB})
		assertCustomerUSDPrice(t, preview, tc.usdMicros)
		snapshot := mapField(preview, "priceSnapshot")
		assertCustomerUSDPrice(t, snapshot, tc.usdMicros)
		if snapshot["sizeGb"] != float64(tc.sizeGB) {
			t.Fatalf("%dGB storage preview = %#v", tc.sizeGB, preview)
		}
	}
}

func TestMonthlyProComputeUsesFixedUSDPrice(t *testing.T) {
	preview := customerPricingPreview(t, map[string]any{"resourceType": "compute", "packageId": "pro"})
	assertCustomerUSDPrice(t, preview, 214_280_000)
	assertCustomerUSDPrice(t, mapField(preview, "priceSnapshot"), 214_280_000)
}

func TestWorkspacePricingPreviewAllowsOnlyFrozenPackageStoragePairs(t *testing.T) {
	for _, tc := range []struct {
		packageID, name       string
		sizeGB                int
		compute, storage, sum int64
	}{
		{packageID: "basic", name: "Basic", sizeGB: 10, compute: 50_000_000, storage: 2_580_000, sum: 52_580_000},
		{packageID: "pro", name: "Pro", sizeGB: 100, compute: 214_280_000, storage: 25_800_000, sum: 240_080_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			preview := customerPricingPreview(t, map[string]any{"resourceType": "workspace", "packageId": tc.packageID, "sizeGb": tc.sizeGB})
			if preview["priceVersion"] != pilotPriceVersion || preview["currency"] != "USD" || preview["totalChargeUsdMicros"] != tc.sum {
				t.Fatalf("workspace preview = %#v", preview)
			}
			if _, ok := preview["pricingVersion"]; ok {
				t.Fatalf("legacy pricingVersion leaked to workspace preview: %#v", preview)
			}
			assertCustomerUSDPrice(t, mapField(preview, "compute"), tc.compute)
			assertCustomerUSDPrice(t, mapField(preview, "storage"), tc.storage)
		})
	}

	for _, input := range []map[string]any{
		{"resourceType": "workspace", "packageId": "basic", "sizeGb": 100},
		{"resourceType": "workspace", "packageId": "pro", "sizeGb": 10},
	} {
		if _, err := newControlPlaneAppEmpty().pricingPreviewResponse(context.Background(), input); !errors.Is(err, errInvalidPricingInput) {
			t.Fatalf("cross-package input %#v error = %v", input, err)
		}
	}
}

func TestMonthlyInternalPricingSnapshotKeepsLegacyPersistenceFields(t *testing.T) {
	preview, err := pricingPreviewResponse(map[string]any{"resourceType": "compute", "packageId": "basic"})
	if err != nil || preview["priceVersion"] != pilotPriceVersion || preview["pricingVersion"] != pilotPriceVersion || preview["monthlyPriceCnyCents"] != int64(35_000) {
		t.Fatalf("internal pricing snapshot = %#v err=%v", preview, err)
	}
}

func TestWorkspacePricingPreviewRejectsInvalidStorage(t *testing.T) {
	if _, err := pricingPreviewResponse(map[string]any{
		"resourceType": "workspace", "packageId": "basic", "sizeGb": 11,
	}); !errors.Is(err, errInvalidPricingInput) {
		t.Fatalf("error = %v, want invalid pricing input", err)
	}
}

func TestMonthlyPricingRejectsInvalidProducts(t *testing.T) {
	for name, input := range map[string]map[string]any{
		"unknown compute package": {"resourceType": "compute", "packageId": "enterprise"},
		"storage below minimum":   {"resourceType": "storage", "packageId": "basic", "sizeGb": 9},
		"storage partial block":   {"resourceType": "storage", "packageId": "basic", "sizeGb": 15},
		"storage fractional size": {"resourceType": "storage", "packageId": "basic", "sizeGb": 10.5},
		"storage charge overflow": {"resourceType": "storage", "packageId": "basic", "sizeGb": 100_000_000_000_000},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := pricingPreviewResponse(input); err == nil {
				t.Fatalf("pricing input should be rejected: %#v", input)
			}
		})
	}
}
