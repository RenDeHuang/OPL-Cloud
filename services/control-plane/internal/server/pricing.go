package server

import "context"

const (
	pricingCatalogVersion = "2026-07-06-opl-user-resource-v1"
	pricingCurrency       = "CNY"
	pricingHoldDays       = 7
)

type pricingCatalogData struct {
	Version  string
	Currency string
	HoldDays int
	Packages []pricingPackageData
}

type pricingPackageData struct {
	ID             string
	Name           string
	Available      bool
	CPU            float64
	MemoryGB       float64
	DiskGB         float64
	Server         string
	ComputeHourly  float64
	StorageGBMonth float64
}

type pricingCatalogStore interface {
	PricingCatalog(ctx context.Context) (pricingCatalogData, error)
}

func defaultPricingCatalog() pricingCatalogData {
	return pricingCatalogData{
		Version:  pricingCatalogVersion,
		Currency: pricingCurrency,
		HoldDays: pricingHoldDays,
		Packages: []pricingPackageData{
			{ID: "basic", Name: "Basic", Available: true, CPU: 2, MemoryGB: 4, DiskGB: 10, Server: "2c4g", ComputeHourly: 0.468, StorageGBMonth: 0.432},
			{ID: "pro", Name: "Pro", Available: true, CPU: 8, MemoryGB: 16, DiskGB: 100, Server: "8c16g", ComputeHourly: 1.38, StorageGBMonth: 0.432},
		},
	}
}

func pricingCatalogResponse() map[string]any {
	return pricingCatalogDTO(defaultPricingCatalog())
}

func (app *controlPlaneServer) pricingCatalog(ctx context.Context) (pricingCatalogData, error) {
	if store, ok := app.store.(pricingCatalogStore); ok {
		catalog, err := store.PricingCatalog(ctx)
		if err != nil {
			return pricingCatalogData{}, err
		}
		if len(catalog.Packages) > 0 {
			return catalog, nil
		}
	}
	return defaultPricingCatalog(), nil
}

func (app *controlPlaneServer) pricingCatalogResponse(ctx context.Context) (map[string]any, error) {
	catalog, err := app.pricingCatalog(ctx)
	if err != nil {
		return nil, err
	}
	return pricingCatalogDTO(catalog), nil
}

func pricingCatalogDTO(catalog pricingCatalogData) map[string]any {
	return map[string]any{
		"pricingVersion": catalog.Version,
		"catalogVersion": catalog.Version,
		"currency":       catalog.Currency,
		"holdDays":       catalog.HoldDays,
		"packages":       packageRows(catalog),
	}
}

func pricingPreviewResponse(input map[string]any, wallet map[string]any) map[string]any {
	return pricingPreviewFromCatalog(defaultPricingCatalog(), input, wallet)
}

func (app *controlPlaneServer) pricingPreviewResponse(ctx context.Context, input map[string]any, wallet map[string]any) (map[string]any, error) {
	catalog, err := app.pricingCatalog(ctx)
	if err != nil {
		return nil, err
	}
	return pricingPreviewFromCatalog(catalog, input, wallet), nil
}

func pricingPreviewFromCatalog(catalog pricingCatalogData, input map[string]any, wallet map[string]any) map[string]any {
	resourceType := firstNonEmpty(stringField(input, "resourceType", ""), "compute")
	packageID := firstNonEmpty(stringField(input, "packageId", ""), "basic")
	sizeGB := numberField(input, "sizeGb", numberField(input, "sizeGB", 10))
	plan := packageByIDFromCatalog(catalog, packageID)
	priceSnapshot := map[string]any{
		"pricingVersion": catalog.Version,
		"packageId":      plan.ID,
		"currency":       catalog.Currency,
		"holdDays":       catalog.HoldDays,
		"resourceType":   resourceType,
		"computeHourly":  plan.ComputeHourly,
		"storageGbMonth": plan.StorageGBMonth,
		"unitPriceCents": cents(plan.ComputeHourly),
	}
	unit := "hour"
	unitPrice := plan.ComputeHourly
	guaranteeAmountCents := computeHoldAmountCentsFromCatalog(catalog, packageID)
	activationAmountCents := cents(plan.ComputeHourly)
	if resourceType == "storage" {
		unit = "gb_month"
		unitPrice = plan.StorageGBMonth
		guaranteeAmountCents = storageHoldAmountCentsFromCatalog(catalog, packageID, sizeGB)
		activationAmountCents = cents(plan.StorageGBMonth * sizeGB / 30 / 24)
		priceSnapshot["sizeGb"] = sizeGB
		priceSnapshot["unitPriceCents"] = activationAmountCents
		priceSnapshot["monthlyUnitPriceCents"] = cents(plan.StorageGBMonth)
	}
	holdAmountCents := guaranteeAmountCents + activationAmountCents
	return map[string]any{
		"pricingVersion":        catalog.Version,
		"resourceType":          resourceType,
		"packageId":             plan.ID,
		"currency":              catalog.Currency,
		"unit":                  unit,
		"unitPrice":             unitPrice,
		"holdDays":              catalog.HoldDays,
		"holdAmountCents":       holdAmountCents,
		"guaranteeAmountCents":  guaranteeAmountCents,
		"activationAmountCents": activationAmountCents,
		"priceSnapshot":         priceSnapshot,
		"walletAfterPreview":    walletAfterHoldPreview(wallet, holdAmountCents),
	}
}

func packageRows(catalog pricingCatalogData) []any {
	rows := make([]any, 0, len(catalog.Packages))
	for _, plan := range catalog.Packages {
		rows = append(rows, packageRow(plan))
	}
	return rows
}

func packageRow(plan pricingPackageData) map[string]any {
	return map[string]any{
		"id":        plan.ID,
		"name":      plan.Name,
		"available": plan.Available,
		"cpu":       plan.CPU,
		"memoryGb":  plan.MemoryGB,
		"diskGb":    plan.DiskGB,
		"server":    plan.Server,
		"price": map[string]any{
			"computeHourly":  plan.ComputeHourly,
			"storageGbMonth": plan.StorageGBMonth,
		},
	}
}

func packageByIDFromCatalog(catalog pricingCatalogData, packageID string) pricingPackageData {
	for _, plan := range catalog.Packages {
		if plan.ID == packageID {
			return plan
		}
	}
	if len(catalog.Packages) > 0 {
		return catalog.Packages[0]
	}
	return defaultPricingCatalog().Packages[0]
}

func computeHoldAmountCentsFromCatalog(catalog pricingCatalogData, packageID string) int64 {
	plan := packageByIDFromCatalog(catalog, packageID)
	return cents(plan.ComputeHourly * 24 * float64(catalog.HoldDays))
}

func storageHoldAmountCentsFromCatalog(catalog pricingCatalogData, packageID string, sizeGB float64) int64 {
	plan := packageByIDFromCatalog(catalog, packageID)
	return cents(plan.StorageGBMonth * sizeGB / 30 * float64(catalog.HoldDays))
}

func walletAfterHoldPreview(wallet map[string]any, holdAmountCents int64) map[string]any {
	balanceCents := int64(numberField(wallet, "balanceCents", numberField(wallet, "balance", 0)*100))
	frozenCents := int64(numberField(wallet, "frozenCents", numberField(wallet, "frozen", 0)*100)) + holdAmountCents
	availableCents := balanceCents - frozenCents
	if availableCents < 0 {
		availableCents = 0
	}
	return map[string]any{
		"balanceCents":   balanceCents,
		"frozenCents":    frozenCents,
		"availableCents": availableCents,
		"balance":        float64(balanceCents) / 100,
		"frozen":         float64(frozenCents) / 100,
		"available":      float64(availableCents) / 100,
		"currency":       firstNonEmpty(stringValue(wallet["currency"]), pricingCurrency),
	}
}
