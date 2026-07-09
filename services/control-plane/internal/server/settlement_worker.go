package server

import (
	"context"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

const defaultSettlementInterval = time.Hour

func settlementWorkerEnabled() bool {
	value := strings.TrimSpace(os.Getenv("OPL_RESOURCE_BILLING_WORKER_ENABLED"))
	return value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "yes")
}

func settlementWorkerInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv("OPL_RESOURCE_BILLING_INTERVAL_MS"))
	if raw == "" {
		return defaultSettlementInterval
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return defaultSettlementInterval
	}
	return time.Duration(ms) * time.Millisecond
}

func (app *controlPlaneServer) startPeriodicSettlementWorker(ctx context.Context, service *controlplane.Service, interval time.Duration) {
	if interval <= 0 {
		interval = defaultSettlementInterval
	}
	go func() {
		if err := app.runPeriodicSettlementOnce(ctx, service, time.Now().UTC()); err != nil {
			log.Printf("periodic settlement failed: %v", err)
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if err := app.runPeriodicSettlementOnce(ctx, service, now.UTC()); err != nil {
					log.Printf("periodic settlement failed: %v", err)
				}
			}
		}
	}()
}

func (app *controlPlaneServer) runPeriodicSettlementOnce(ctx context.Context, service *controlplane.Service, now time.Time) error {
	periodEnd := now.UTC().Truncate(time.Hour)
	if periodEnd.IsZero() {
		periodEnd = now.UTC()
	}
	periodStart := periodEnd.Add(-time.Hour)
	inputs, err := app.periodicSettlementInputs(ctx, periodStart, periodEnd)
	if err != nil {
		return err
	}
	for _, input := range inputs {
		key := periodicSettlementKey(input)
		result, err := service.SettleResource(ctx, input, key)
		if err != nil {
			return err
		}
		result = completeSettlementResult(result, input)
		if err := app.saveResourceSettlementProjection(result); err != nil {
			return err
		}
		if err := app.markResourceSettlement(result); err != nil {
			return err
		}
	}
	return nil
}

type settlementResourceStore interface {
	SettlementResourceRows(ctx context.Context) (controlPlaneRecordSet, controlPlaneRecordSet, error)
}

func (app *controlPlaneServer) periodicSettlementInputs(ctx context.Context, periodStart time.Time, periodEnd time.Time) ([]controlplane.ResourceSettlementInput, error) {
	computes, storages, err := app.settlementResourceRows(ctx)
	if err != nil {
		return nil, err
	}
	inputs := []controlplane.ResourceSettlementInput{}
	for _, row := range computes {
		if !billableCompute(row) || alreadySettledForPeriod(row, periodEnd) {
			continue
		}
		inputs = append(inputs, periodicSettlementInput(row, "compute", periodStart, periodEnd))
	}
	for _, row := range storages {
		if !billableStorage(row) || alreadySettledForPeriod(row, periodEnd) {
			continue
		}
		inputs = append(inputs, periodicSettlementInput(row, "storage", periodStart, periodEnd))
	}
	return inputs, nil
}

func (app *controlPlaneServer) settlementResourceRows(ctx context.Context) (controlPlaneRecordSet, controlPlaneRecordSet, error) {
	if store, ok := app.store.(settlementResourceStore); ok {
		return store.SettlementResourceRows(ctx)
	}
	return app.computeRecordSet(""), app.storageRecordSet(""), nil
}

func billableCompute(row map[string]any) bool {
	status := stringValue(row["status"])
	return providerFreshEnough(row) && billingStatusFor(row) != "stopped" && (status == "running" || status == "ready" || status == "active")
}

func billableStorage(row map[string]any) bool {
	status := stringValue(row["status"])
	return providerFreshEnough(row) && billingStatusFor(row) != "stopped" && (status == "available" || status == "ready" || status == "bound")
}

func providerFreshEnough(row map[string]any) bool {
	switch stringValue(row["providerStatus"]) {
	case "missing", "sync_failed":
		return false
	}
	lastSync, ok := parseTimeString(stringValue(row["lastProviderSyncAt"]))
	if !ok {
		return false
	}
	return time.Since(lastSync) <= providerFreshnessWindow()
}

func periodicSettlementInput(row map[string]any, resourceType string, periodStart time.Time, periodEnd time.Time) controlplane.ResourceSettlementInput {
	packageID := firstNonEmpty(stringValue(row["packageId"]), "basic")
	amountCents := periodicSettlementAmountCents(row, resourceType)
	unitPriceCents := amountCents
	return controlplane.ResourceSettlementInput{
		AccountID:               firstNonEmpty(stringValue(row["accountId"]), stringValue(row["ownerAccountId"]), "acct-local"),
		WorkspaceID:             stringValue(row["workspaceId"]),
		ResourceType:            resourceType,
		ResourceID:              stringValue(row["id"]),
		AmountCents:             amountCents,
		Currency:                firstNonEmpty(stringValue(valueOrNil(row, "priceSnapshot", "currency")), pricingCurrency),
		PricingVersion:          firstNonEmpty(stringValue(row["pricingVersion"]), pricingCatalogVersion),
		PriceSnapshot:           settlementPriceSnapshot(row, packageID, resourceType, unitPriceCents),
		UsagePeriodStart:        periodStart.UTC().Format(time.RFC3339),
		UsagePeriodEnd:          periodEnd.UTC().Format(time.RFC3339),
		Quantity:                1,
		Unit:                    "hour",
		ProviderCostEvidenceRef: firstNonEmpty(stringValue(row["operationId"]), stringValue(row["providerRequestId"]), "control-plane:"+resourceType+":"+stringValue(row["id"])),
	}
}

func alreadySettledForPeriod(row map[string]any, periodEnd time.Time) bool {
	return stringValue(row["settlementId"]) != "" && stringValue(row["usagePeriodEnd"]) == periodEnd.UTC().Format(time.RFC3339)
}

func periodicSettlementAmountCents(row map[string]any, resourceType string) int64 {
	if snapshot, _ := row["priceSnapshot"].(map[string]any); snapshot != nil {
		if unitPriceCents := int64(numberField(snapshot, "unitPriceCents", 0)); unitPriceCents > 0 {
			return unitPriceCents
		}
		if resourceType == "storage" {
			sizeGB := numberField(row, "sizeGb", numberField(snapshot, "sizeGb", 10))
			return cents(numberField(snapshot, "storageGbMonth", 0) * sizeGB / 30 / 24)
		}
		if hourly := numberField(snapshot, "computeHourly", 0); hourly > 0 {
			return cents(hourly)
		}
	}
	plan := packageByID(packageIDFromRow(row))
	if resourceType == "storage" {
		sizeGB := numberField(row, "sizeGb", 10)
		return cents(priceField(plan, "storageGbMonth") * sizeGB / 30 / 24)
	}
	return cents(priceField(plan, "computeHourly"))
}

func settlementPriceSnapshot(row map[string]any, packageID string, resourceType string, unitPriceCents int64) map[string]any {
	if snapshot, _ := row["priceSnapshot"].(map[string]any); snapshot != nil {
		out := cloneMap(snapshot)
		out["unitPriceCents"] = unitPriceCents
		out["source"] = firstNonEmpty(stringValue(out["source"]), "resource_price_snapshot")
		return out
	}
	return map[string]any{"packageId": packageID, "resourceType": resourceType, "unitPriceCents": unitPriceCents, "currency": pricingCurrency, "source": "periodic_settlement_worker"}
}

func packageIDFromRow(row map[string]any) string {
	return firstNonEmpty(stringValue(row["packageId"]), stringValue(valueOrNil(row, "priceSnapshot", "packageId")), "basic")
}

func valueOrNil(row map[string]any, path ...string) any {
	var current any = row
	for _, part := range path {
		asMap, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = asMap[part]
	}
	return current
}

func periodicSettlementKey(input controlplane.ResourceSettlementInput) string {
	return strings.Join([]string{"periodic-settlement", input.AccountID, input.ResourceType, input.ResourceID, input.UsagePeriodEnd}, ":")
}

func (app *controlPlaneServer) markResourceSettlement(result clients.ResourceSettlementResult) error {
	var row map[string]any
	switch result.ResourceType {
	case "storage":
		row, _ = app.getStorage(result.ResourceID)
	default:
		row, _ = app.getCompute(result.ResourceID)
	}
	if row == nil {
		return nil
	}
	row["settlementId"] = result.ID
	row["ledgerEntryId"] = result.LedgerEntryID
	row["walletTransactionId"] = result.WalletTransactionID
	row["usagePeriodEnd"] = result.UsagePeriodEnd
	if result.ResourceType == "storage" {
		return app.tables.SaveStorage(context.Background(), row)
	}
	return app.tables.SaveCompute(context.Background(), row)
}
