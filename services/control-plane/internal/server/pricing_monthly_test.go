package server

import (
	"errors"
	"testing"
)

func TestMonthlyPricingCatalogUsesIntegerCharges(t *testing.T) {
	catalog := pricingCatalogResponse()
	if catalog["pricingVersion"] != "2026-07-16-opl-monthly-v2" || catalog["billingUnit"] != "calendar_month" {
		t.Fatalf("monthly catalog identity = %#v", catalog)
	}
	if catalog["displayCurrency"] != "CNY" || catalog["walletCurrency"] != "USD" || catalog["exchangeRateCnyPerUsd"] != int64(7) {
		t.Fatalf("monthly catalog currencies = %#v", catalog)
	}

	packages, _ := catalog["packages"].([]any)
	if len(packages) != 2 {
		t.Fatalf("packages = %#v", packages)
	}
	assertCharge := func(index int, packageID string, cnyCents, usdMicros int64) {
		t.Helper()
		row, _ := packages[index].(map[string]any)
		price, _ := row["price"].(map[string]any)
		if row["id"] != packageID || row["available"] != true || price["monthlyPriceCnyCents"] != cnyCents || price["chargeUsdMicros"] != usdMicros {
			t.Fatalf("%s price = %#v", packageID, row)
		}
	}
	assertCharge(0, "basic", 35000, 50000000)
	assertCharge(1, "pro", 150000, 214285715)
	storagePrice := mapField(catalog, "storagePer10GbMonthly")
	if storagePrice["cnyCents"] != int64(1800) || storagePrice["usdMicros"] != int64(2_571_429) {
		t.Fatalf("default storage price = %#v", storagePrice)
	}

	statePackages, _ := newControlPlaneAppEmpty().state("", nil)["packages"].([]any)
	if len(statePackages) != 2 || mapField(statePackages[0].(map[string]any), "price")["chargeUsdMicros"] != int64(50_000_000) || mapField(statePackages[1].(map[string]any), "price")["chargeUsdMicros"] != int64(214_285_715) {
		t.Fatalf("state packages = %#v", statePackages)
	}
}

func TestMonthlyStoragePriceUsesWholeTenGigabyteBlocks(t *testing.T) {
	for _, tc := range []struct {
		sizeGB    int
		cnyCents  int64
		usdMicros int64
	}{{10, 1800, 2_571_429}, {100, 18000, 25_714_286}} {
		preview, err := pricingPreviewResponse(map[string]any{"resourceType": "storage", "packageId": "basic", "sizeGb": tc.sizeGB})
		if err != nil {
			t.Fatalf("price %dGB storage: %v", tc.sizeGB, err)
		}
		snapshot := mapField(preview, "priceSnapshot")
		if preview["monthlyPriceCnyCents"] != tc.cnyCents || preview["chargeUsdMicros"] != tc.usdMicros || snapshot["sizeGb"] != float64(tc.sizeGB) || snapshot["monthlyPriceCnyCents"] != tc.cnyCents || snapshot["chargeUsdMicros"] != tc.usdMicros {
			t.Fatalf("%dGB storage preview = %#v", tc.sizeGB, preview)
		}
	}
}

func TestMonthlyProComputePrice(t *testing.T) {
	preview, err := pricingPreviewResponse(map[string]any{"resourceType": "compute", "packageId": "pro"})
	if err != nil || preview["monthlyPriceCnyCents"] != int64(150000) || preview["chargeUsdMicros"] != int64(214_285_715) {
		t.Fatalf("Pro preview = %#v err=%v", preview, err)
	}
}

func TestWorkspacePricingPreviewAddsComputeAndStorageOnce(t *testing.T) {
	preview, err := pricingPreviewResponse(map[string]any{
		"resourceType": "workspace", "packageId": "basic", "sizeGb": 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if preview["totalMonthlyPriceCnyCents"] != int64(36_800) || preview["totalChargeUsdMicros"] != int64(52_571_429) {
		t.Fatalf("workspace preview = %#v", preview)
	}
	if mapField(preview, "compute")["chargeUsdMicros"] != int64(50_000_000) || mapField(preview, "storage")["chargeUsdMicros"] != int64(2_571_429) {
		t.Fatalf("workspace components = %#v", preview)
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
		"storage charge overflow": {"resourceType": "storage", "packageId": "basic", "sizeGb": 10_000_000_000_000},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := pricingPreviewResponse(input); err == nil {
				t.Fatalf("pricing input should be rejected: %#v", input)
			}
		})
	}
}
