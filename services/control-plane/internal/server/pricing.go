package server

const (
	pricingCatalogVersion = "2026-07-06-opl-user-resource-v1"
	pricingCurrency       = "CNY"
	pricingHoldDays       = 7
)

func pricingCatalogResponse() map[string]any {
	return map[string]any{
		"pricingVersion": pricingCatalogVersion,
		"catalogVersion": pricingCatalogVersion,
		"currency":       pricingCurrency,
		"holdDays":       pricingHoldDays,
		"packages":       packageList(),
	}
}

func pricingPreviewResponse(input map[string]any, wallet map[string]any) map[string]any {
	resourceType := firstNonEmpty(stringField(input, "resourceType", ""), "compute")
	packageID := firstNonEmpty(stringField(input, "packageId", ""), "basic")
	sizeGB := numberField(input, "sizeGb", numberField(input, "sizeGB", 10))
	plan := packageByID(packageID)
	priceSnapshot := map[string]any{
		"pricingVersion": pricingCatalogVersion,
		"packageId":      stringValue(plan["id"]),
		"currency":       pricingCurrency,
		"holdDays":       pricingHoldDays,
		"computeHourly":  priceField(plan, "computeHourly"),
		"storageGbMonth": priceField(plan, "storageGbMonth"),
	}
	unit := "hour"
	unitPrice := priceField(plan, "computeHourly")
	holdAmountCents := computeHoldAmountCents(packageID)
	if resourceType == "storage" {
		unit = "gb_month"
		unitPrice = priceField(plan, "storageGbMonth")
		holdAmountCents = storageHoldAmountCents(packageID, sizeGB)
		priceSnapshot["sizeGb"] = sizeGB
	}
	return map[string]any{
		"pricingVersion":     pricingCatalogVersion,
		"resourceType":       resourceType,
		"packageId":          stringValue(plan["id"]),
		"currency":           pricingCurrency,
		"unit":               unit,
		"unitPrice":          unitPrice,
		"holdDays":           pricingHoldDays,
		"holdAmountCents":    holdAmountCents,
		"priceSnapshot":      priceSnapshot,
		"walletAfterPreview": walletAfterHoldPreview(wallet, holdAmountCents),
	}
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
