package server

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
)

const (
	pricingCatalogVersion      = "pilot-usd-2026-07-v1"
	pricingCurrency            = "USD"
	pricingWalletCurrency      = "USD"
	pricingBillingUnit         = "calendar_month"
	storageBlockGB             = int64(10)
	storageBlockPriceCNYCents  = int64(1800)
	storageBlockPriceUSDMicros = int64(2_580_000)
	maxStorageBlocks           = math.MaxInt64 / storageBlockPriceUSDMicros
	maxStorageGB               = maxStorageBlocks * storageBlockGB
)

var errInvalidPricingInput = errors.New("invalid pricing input")

type pricingCatalogData struct {
	Version        string
	Currency       string
	WalletCurrency string
	BillingUnit    string
	Packages       []pricingPackageData
}

type pricingPackageData struct {
	ID                   string
	Name                 string
	Available            bool
	CPU                  float64
	MemoryGB             float64
	DiskGB               float64
	Server               string
	MonthlyPriceCNYCents int64
	ChargeUSDMicros      int64
}

func defaultPricingCatalog() pricingCatalogData {
	return pricingCatalogData{
		Version: pricingCatalogVersion, Currency: pricingCurrency, WalletCurrency: pricingWalletCurrency,
		BillingUnit: pricingBillingUnit,
		Packages: []pricingPackageData{
			{ID: "basic", Name: "Basic", Available: true, CPU: 2, MemoryGB: 4, DiskGB: 10, Server: "2c4g", MonthlyPriceCNYCents: 35000, ChargeUSDMicros: 50000000},
			{ID: "pro", Name: "Pro", Available: true, CPU: 8, MemoryGB: 16, DiskGB: 100, Server: "8c16g", MonthlyPriceCNYCents: 150000, ChargeUSDMicros: 214280000},
		},
	}
}

func pricingCatalogResponse() map[string]any { return pricingCatalogDTO(defaultPricingCatalog()) }

func (app *controlPlaneServer) pricingCatalogResponse(_ context.Context, computePools []any) (map[string]any, error) {
	response := pricingCatalogResponse()
	response["packages"] = packageRowsForComputePools(defaultPricingCatalog(), computePools)
	return response, nil
}

func pricingCatalogDTO(catalog pricingCatalogData) map[string]any {
	return map[string]any{
		"priceVersion": catalog.Version, "billingUnit": catalog.BillingUnit,
		"displayCurrency": catalog.Currency, "walletCurrency": catalog.WalletCurrency,
		"currency":              catalog.Currency,
		"storageSize":           map[string]any{"minimumGb": storageBlockGB, "stepGb": storageBlockGB},
		"storagePer10GbMonthly": map[string]any{"priceVersion": catalog.Version, "currency": catalog.Currency, "displayCurrency": catalog.Currency, "usdMicros": storageBlockPriceUSDMicros},
		"packages":              packageRows(catalog),
	}
}

func pricingPreviewResponse(input map[string]any) (map[string]any, error) {
	return pricingPreviewFromCatalog(defaultPricingCatalog(), input)
}

func (app *controlPlaneServer) pricingPreviewResponse(_ context.Context, input map[string]any) (map[string]any, error) {
	resourceType, validResourceType := input["resourceType"].(string)
	packageID, validPackage := input["packageId"].(string)
	if !validResourceType || !validPackage || strings.TrimSpace(packageID) == "" {
		return nil, errInvalidPricingInput
	}
	switch resourceType {
	case "compute":
	case "storage", "workspace":
		if _, validSize := positiveIntegerField(input, "sizeGb"); !validSize {
			return nil, errInvalidPricingInput
		}
	default:
		return nil, errInvalidPricingInput
	}
	preview, err := pricingPreviewResponse(input)
	if err != nil {
		return nil, err
	}
	return customerPricingPreviewDTO(preview), nil
}

func pricingPreviewFromCatalog(catalog pricingCatalogData, input map[string]any) (map[string]any, error) {
	resourceType := firstNonEmpty(stringField(input, "resourceType", ""), "compute")
	if resourceType == "workspace" {
		return workspacePricingPreview(catalog, input)
	}
	if resourceType != "compute" && resourceType != "storage" {
		return nil, fmt.Errorf("%w: unknown resource type %q", errInvalidPricingInput, resourceType)
	}
	packageID := firstNonEmpty(stringField(input, "packageId", ""), "basic")
	plan, ok := packageByIDFromCatalog(catalog, packageID)
	if !ok {
		return nil, fmt.Errorf("%w: unknown package %q", errInvalidPricingInput, packageID)
	}
	monthlyPriceCNYCents, chargeUSDMicros := plan.MonthlyPriceCNYCents, plan.ChargeUSDMicros
	snapshot := map[string]any{
		"priceVersion": catalog.Version, "pricingVersion": catalog.Version, "packageId": plan.ID, "currency": catalog.Currency,
		"displayCurrency": catalog.Currency,
		"billingUnit":     catalog.BillingUnit, "monthlyPriceCnyCents": monthlyPriceCNYCents,
		"chargeUsdMicros": chargeUSDMicros, "resourceType": resourceType,
	}
	if resourceType == "storage" {
		sizeGB := numberField(input, "sizeGb", numberField(input, "sizeGB", 10))
		if sizeGB < float64(storageBlockGB) || sizeGB > float64(maxStorageGB) || sizeGB != math.Trunc(sizeGB) || int64(sizeGB)%storageBlockGB != 0 {
			return nil, fmt.Errorf("%w: storage size must be a positive multiple of %dGB", errInvalidPricingInput, storageBlockGB)
		}
		blocks := int64(sizeGB) / storageBlockGB
		monthlyPriceCNYCents = blocks * storageBlockPriceCNYCents
		chargeUSDMicros = blocks * storageBlockPriceUSDMicros
		snapshot["sizeGb"], snapshot["monthlyPriceCnyCents"], snapshot["chargeUsdMicros"] = sizeGB, monthlyPriceCNYCents, chargeUSDMicros
	}
	return map[string]any{
		"priceVersion": catalog.Version, "pricingVersion": catalog.Version, "resourceType": resourceType, "packageId": plan.ID,
		"currency": catalog.Currency, "displayCurrency": catalog.Currency, "billingUnit": catalog.BillingUnit,
		"monthlyPriceCnyCents": monthlyPriceCNYCents, "chargeUsdMicros": chargeUSDMicros,
		"priceSnapshot": snapshot,
	}, nil
}

func workspacePricingPreview(catalog pricingCatalogData, input map[string]any) (map[string]any, error) {
	packageID, validPackage := input["packageId"].(string)
	packageID = strings.TrimSpace(packageID)
	sizeGB, validSize := positiveIntegerField(input, "sizeGb")
	if !validPackage || packageID == "" || !validSize || packageID == "basic" && sizeGB != 10 || packageID == "pro" && sizeGB != 100 {
		return nil, fmt.Errorf("%w: invalid workspace package or storage size", errInvalidPricingInput)
	}
	computeInput := cloneMap(input)
	computeInput["resourceType"], computeInput["packageId"] = "compute", packageID
	storageInput := cloneMap(input)
	storageInput["resourceType"], storageInput["packageId"], storageInput["sizeGb"] = "storage", packageID, sizeGB
	compute, err := pricingPreviewFromCatalog(catalog, computeInput)
	if err != nil {
		return nil, err
	}
	storage, err := pricingPreviewFromCatalog(catalog, storageInput)
	if err != nil {
		return nil, err
	}
	cnyCents, ok := checkedAddInt64(int64(numberField(compute, "monthlyPriceCnyCents", 0)), int64(numberField(storage, "monthlyPriceCnyCents", 0)))
	if !ok {
		return nil, errInvalidPricingInput
	}
	usdMicros, ok := checkedAddInt64(int64(numberField(compute, "chargeUsdMicros", 0)), int64(numberField(storage, "chargeUsdMicros", 0)))
	if !ok {
		return nil, errInvalidPricingInput
	}
	return map[string]any{
		"resourceType": "workspace", "priceVersion": catalog.Version, "pricingVersion": catalog.Version,
		"packageId": stringValue(compute["packageId"]), "currency": catalog.Currency, "displayCurrency": catalog.Currency,
		"billingUnit": catalog.BillingUnit, "compute": compute, "storage": storage,
		"totalMonthlyPriceCnyCents": cnyCents, "totalChargeUsdMicros": usdMicros,
	}, nil
}

func checkedAddInt64(left, right int64) (int64, bool) {
	if left < 0 || right < 0 || left > math.MaxInt64-right {
		return 0, false
	}
	return left + right, true
}

func packageRows(catalog pricingCatalogData) []any {
	rows := make([]any, 0, len(catalog.Packages))
	for _, plan := range catalog.Packages {
		rows = append(rows, map[string]any{
			"id": plan.ID, "name": plan.Name, "available": plan.Available, "cpu": plan.CPU,
			"memoryGb": plan.MemoryGB, "diskGb": plan.DiskGB, "server": plan.Server,
			"price": map[string]any{"priceVersion": catalog.Version, "currency": catalog.Currency, "displayCurrency": catalog.Currency, "chargeUsdMicros": plan.ChargeUSDMicros},
		})
	}
	return rows
}

func packageRowsForComputePools(catalog pricingCatalogData, computePools []any) []any {
	available := map[string]bool{}
	for _, raw := range computePools {
		pool, _ := raw.(map[string]any)
		packageID := stringValue(pool["packageId"])
		available[packageID] = available[packageID] || pool["available"] == true
	}
	rows := packageRows(catalog)
	for _, raw := range rows {
		row := raw.(map[string]any)
		row["available"] = row["available"] == true && available[stringValue(row["id"])]
	}
	return rows
}

func packageByIDFromCatalog(catalog pricingCatalogData, packageID string) (pricingPackageData, bool) {
	for _, plan := range catalog.Packages {
		if plan.ID == packageID {
			return plan, true
		}
	}
	return pricingPackageData{}, false
}

func customerPricingPreviewDTO(preview map[string]any) map[string]any {
	result := map[string]any{
		"resourceType": preview["resourceType"], "priceVersion": preview["priceVersion"], "packageId": preview["packageId"],
		"currency": preview["currency"], "displayCurrency": preview["displayCurrency"], "billingUnit": preview["billingUnit"],
	}
	if stringValue(preview["resourceType"]) == "workspace" {
		result["compute"] = customerPricingPreviewDTO(mapField(preview, "compute"))
		result["storage"] = customerPricingPreviewDTO(mapField(preview, "storage"))
		result["totalChargeUsdMicros"] = preview["totalChargeUsdMicros"]
		return result
	}
	result["chargeUsdMicros"] = preview["chargeUsdMicros"]
	result["priceSnapshot"] = customerPricingSnapshotDTO(mapField(preview, "priceSnapshot"))
	return result
}

func customerPricingSnapshotDTO(snapshot map[string]any) map[string]any {
	result := map[string]any{
		"resourceType": snapshot["resourceType"], "priceVersion": snapshot["priceVersion"], "packageId": snapshot["packageId"],
		"currency": snapshot["currency"], "displayCurrency": snapshot["displayCurrency"], "billingUnit": snapshot["billingUnit"],
		"chargeUsdMicros": snapshot["chargeUsdMicros"],
	}
	if sizeGB, ok := snapshot["sizeGb"]; ok {
		result["sizeGb"] = sizeGB
	}
	return result
}
