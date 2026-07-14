package server

import (
	"context"
	"errors"
	"fmt"
	"math"
)

const (
	pricingCatalogVersion        = "2026-07-14-opl-monthly-v1"
	pricingCurrency              = "CNY"
	pricingWalletCurrency        = "USD"
	pricingBillingUnit           = "calendar_month"
	pricingExchangeRateCNYPerUSD = int64(7)
	storageBlockGB               = int64(10)
	storageBlockPriceCNYCents    = int64(1800)
	maxStorageBlocks             = (math.MaxInt64 - pricingExchangeRateCNYPerUSD + 1) / (storageBlockPriceCNYCents * 10000)
	maxStorageGB                 = maxStorageBlocks * storageBlockGB
)

var errInvalidPricingInput = errors.New("invalid pricing input")

type pricingCatalogData struct {
	Version        string
	Currency       string
	WalletCurrency string
	BillingUnit    string
	ExchangeRate   int64
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
		BillingUnit: pricingBillingUnit, ExchangeRate: pricingExchangeRateCNYPerUSD,
		Packages: []pricingPackageData{
			{ID: "basic", Name: "Basic", Available: true, CPU: 2, MemoryGB: 4, DiskGB: 10, Server: "2c4g", MonthlyPriceCNYCents: 35000, ChargeUSDMicros: 50000000},
			{ID: "pro", Name: "Pro", Available: true, CPU: 8, MemoryGB: 16, DiskGB: 100, Server: "8c16g", MonthlyPriceCNYCents: 150000, ChargeUSDMicros: 214285715},
		},
	}
}

func pricingCatalogResponse() map[string]any { return pricingCatalogDTO(defaultPricingCatalog()) }

func (app *controlPlaneServer) pricingCatalogResponse(context.Context) (map[string]any, error) {
	return pricingCatalogResponse(), nil
}

func pricingCatalogDTO(catalog pricingCatalogData) map[string]any {
	return map[string]any{
		"pricingVersion": catalog.Version, "catalogVersion": catalog.Version, "billingUnit": catalog.BillingUnit,
		"displayCurrency": catalog.Currency, "walletCurrency": catalog.WalletCurrency,
		"exchangeRateCnyPerUsd": catalog.ExchangeRate, "currency": catalog.Currency,
		"storageSize":           map[string]any{"minimumGb": storageBlockGB, "stepGb": storageBlockGB},
		"storagePer10GbMonthly": map[string]any{"cnyCents": storageBlockPriceCNYCents, "usdMicros": cnyCentsToUSDMicros(storageBlockPriceCNYCents)},
		"packages":              packageRows(catalog),
	}
}

func pricingPreviewResponse(input map[string]any) (map[string]any, error) {
	return pricingPreviewFromCatalog(defaultPricingCatalog(), input)
}

func (app *controlPlaneServer) pricingPreviewResponse(_ context.Context, input map[string]any) (map[string]any, error) {
	return pricingPreviewResponse(input)
}

func pricingPreviewFromCatalog(catalog pricingCatalogData, input map[string]any) (map[string]any, error) {
	resourceType := firstNonEmpty(stringField(input, "resourceType", ""), "compute")
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
		"pricingVersion": catalog.Version, "packageId": plan.ID, "currency": catalog.Currency,
		"billingUnit": catalog.BillingUnit, "monthlyPriceCnyCents": monthlyPriceCNYCents,
		"chargeUsdMicros": chargeUSDMicros, "resourceType": resourceType,
	}
	if resourceType == "storage" {
		sizeGB := numberField(input, "sizeGb", numberField(input, "sizeGB", 10))
		if sizeGB < float64(storageBlockGB) || sizeGB > float64(maxStorageGB) || sizeGB != math.Trunc(sizeGB) || int64(sizeGB)%storageBlockGB != 0 {
			return nil, fmt.Errorf("%w: storage size must be a positive multiple of %dGB", errInvalidPricingInput, storageBlockGB)
		}
		blocks := int64(sizeGB) / storageBlockGB
		monthlyPriceCNYCents = blocks * storageBlockPriceCNYCents
		chargeUSDMicros = cnyCentsToUSDMicros(monthlyPriceCNYCents)
		snapshot["sizeGb"], snapshot["monthlyPriceCnyCents"], snapshot["chargeUsdMicros"] = sizeGB, monthlyPriceCNYCents, chargeUSDMicros
	}
	return map[string]any{
		"pricingVersion": catalog.Version, "resourceType": resourceType, "packageId": plan.ID,
		"currency": catalog.Currency, "billingUnit": catalog.BillingUnit,
		"monthlyPriceCnyCents": monthlyPriceCNYCents, "chargeUsdMicros": chargeUSDMicros,
		"priceSnapshot": snapshot,
	}, nil
}

func packageRows(catalog pricingCatalogData) []any {
	rows := make([]any, 0, len(catalog.Packages))
	for _, plan := range catalog.Packages {
		rows = append(rows, map[string]any{
			"id": plan.ID, "name": plan.Name, "available": plan.Available, "cpu": plan.CPU,
			"memoryGb": plan.MemoryGB, "diskGb": plan.DiskGB, "server": plan.Server,
			"price": map[string]any{"monthlyPriceCnyCents": plan.MonthlyPriceCNYCents, "chargeUsdMicros": plan.ChargeUSDMicros},
		})
	}
	return rows
}

func cnyCentsToUSDMicros(cnyCents int64) int64 {
	return (cnyCents*10000 + pricingExchangeRateCNYPerUSD - 1) / pricingExchangeRateCNYPerUSD
}

func packageByIDFromCatalog(catalog pricingCatalogData, packageID string) (pricingPackageData, bool) {
	for _, plan := range catalog.Packages {
		if plan.ID == packageID {
			return plan, true
		}
	}
	return pricingPackageData{}, false
}
