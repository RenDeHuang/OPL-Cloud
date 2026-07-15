package server

import "testing"

func TestMonthlyPricingCatalogUsesIntegerCharges(t *testing.T) {
	catalog := pricingCatalogResponse()
	if catalog["pricingVersion"] != "2026-07-14-opl-monthly-v1" || catalog["billingUnit"] != "calendar_month" {
		t.Fatalf("monthly catalog identity = %#v", catalog)
	}
	if catalog["displayCurrency"] != "CNY" || catalog["walletCurrency"] != "USD" || catalog["exchangeRateCnyPerUsd"] != int64(7) {
		t.Fatalf("monthly catalog currencies = %#v", catalog)
	}

	packages, _ := catalog["packages"].([]any)
	if len(packages) != 1 {
		t.Fatalf("packages = %#v", packages)
	}
	assertCharge := func(index int, packageID string, cnyCents, usdMicros int64) {
		t.Helper()
		row, _ := packages[index].(map[string]any)
		price, _ := row["price"].(map[string]any)
		if row["id"] != packageID || price["monthlyPriceCnyCents"] != cnyCents || price["chargeUsdMicros"] != usdMicros {
			t.Fatalf("%s price = %#v", packageID, row)
		}
	}
	assertCharge(0, "basic", 35000, 50000000)
}

func TestMonthlyStoragePriceUsesWholeTenGigabyteBlocks(t *testing.T) {
	preview, err := pricingPreviewResponse(map[string]any{"resourceType": "storage", "packageId": "basic", "sizeGb": 30})
	if err != nil {
		t.Fatalf("price 30GB storage: %v", err)
	}
	snapshot := mapField(preview, "priceSnapshot")
	if preview["monthlyPriceCnyCents"] != int64(5400) || preview["chargeUsdMicros"] != int64(7714286) {
		t.Fatalf("storage preview = %#v", preview)
	}
	if snapshot["sizeGb"] != float64(30) || snapshot["monthlyPriceCnyCents"] != int64(5400) || snapshot["chargeUsdMicros"] != int64(7714286) {
		t.Fatalf("storage snapshot = %#v", snapshot)
	}
}

func TestMonthlyPricingRejectsInvalidProducts(t *testing.T) {
	for name, input := range map[string]map[string]any{
		"disabled pro package":   {"resourceType": "compute", "packageId": "pro"},
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
