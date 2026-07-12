package server

import (
	"context"
	"errors"
	"fmt"
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
	inputs, err := app.periodicSettlementInputs(ctx, periodStart, periodEnd, now.UTC())
	if err != nil {
		return err
	}
	var errs []error
	for _, input := range inputs {
		key := periodicSettlementKey(input)
		result, err := service.SettleResource(ctx, input, key)
		if err != nil {
			errs = append(errs, fmt.Errorf("settle %s: %w", input.ResourceID, err))
			if strings.Contains(strings.ToLower(err.Error()), "insufficient resource hold") {
				if stopErr := app.stopExhaustedResource(ctx, service, input); stopErr != nil {
					errs = append(errs, fmt.Errorf("stop exhausted %s: %w", input.ResourceID, stopErr))
				}
			}
			continue
		}
		result = completeSettlementResult(result, input)
		if err := app.saveResourceSettlementProjection(result); err != nil {
			errs = append(errs, fmt.Errorf("save settlement %s: %w", input.ResourceID, err))
			continue
		}
		if err := app.markResourceSettlement(result); err != nil {
			errs = append(errs, fmt.Errorf("mark settlement %s: %w", input.ResourceID, err))
		}
	}
	return errors.Join(errs...)
}

func (app *controlPlaneServer) stopExhaustedResource(ctx context.Context, service *controlplane.Service, input controlplane.ResourceSettlementInput) error {
	key := "hold-exhausted:" + input.ResourceType + ":" + input.ResourceID
	if input.ResourceType == "compute" {
		row, ok := app.getCompute(input.ResourceID)
		if !ok {
			return fmt.Errorf("compute_allocation_not_found")
		}
		stopping := cloneMap(row)
		stopping["status"] = "destroying"
		stopping["desiredStatus"] = "destroyed"
		stopping["billingStatus"] = "stopping"
		if err := app.saveComputeFact(stopping); err != nil {
			return err
		}
		result, err := service.DestroyComputeAllocation(ctx, destroyResourceInput(input.ResourceID, stopping), key)
		if err != nil {
			_ = app.saveComputeFact(providerSyncFacts(stopping, err))
			return err
		}
		return app.saveComputeFact(providerSyncFacts(computeResponse(mergeMaps(stopping, structToMap(result))), nil))
	}
	row, ok := app.getStorage(input.ResourceID)
	if !ok {
		return fmt.Errorf("storage_volume_not_found")
	}
	stopping := cloneMap(row)
	stopping["status"] = "destroying"
	stopping["desiredStatus"] = "destroyed"
	stopping["billingStatus"] = "stopping"
	if err := app.saveStorageFact(stopping); err != nil {
		return err
	}
	result, err := service.DestroyStorageVolume(ctx, destroyResourceInput(input.ResourceID, stopping), key)
	if err != nil {
		_ = app.saveStorageFact(providerSyncFacts(stopping, err))
		return err
	}
	return app.saveStorageFact(providerSyncFacts(storageResponse(mergeMaps(stopping, structToMap(result))), nil))
}

type settlementResourceStore interface {
	SettlementResourceRows(ctx context.Context) (controlPlaneRecordSet, controlPlaneRecordSet, error)
}

func (app *controlPlaneServer) periodicSettlementInputs(ctx context.Context, periodStart time.Time, periodEnd time.Time, now time.Time) ([]controlplane.ResourceSettlementInput, error) {
	computes, storages, err := app.settlementResourceRows(ctx)
	if err != nil {
		return nil, err
	}
	inputs := []controlplane.ResourceSettlementInput{}
	for _, row := range computes {
		start, end, due := settlementPeriod(row, periodStart, periodEnd, now)
		if !billableCompute(row) || !due || alreadySettledForPeriod(row, end) {
			continue
		}
		inputs = append(inputs, periodicSettlementInput(row, "compute", start, end))
	}
	for _, row := range storages {
		start, end, due := settlementPeriod(row, periodStart, periodEnd, now)
		if !billableStorage(row) || !due || alreadySettledForPeriod(row, end) {
			continue
		}
		inputs = append(inputs, periodicSettlementInput(row, "storage", start, end))
	}
	return inputs, nil
}

func settlementPeriod(row map[string]any, fallbackStart, fallbackEnd, now time.Time) (time.Time, time.Time, bool) {
	next, ok := parseTimeString(stringValue(row["billingNextSettlementAt"]))
	if !ok {
		return fallbackStart, fallbackEnd, true
	}
	return next, next.Add(time.Hour), !now.Before(next)
}

func (app *controlPlaneServer) settlementResourceRows(ctx context.Context) (controlPlaneRecordSet, controlPlaneRecordSet, error) {
	if store, ok := app.store.(settlementResourceStore); ok {
		return store.SettlementResourceRows(ctx)
	}
	return app.computeRecordSet(""), app.storageRecordSet(""), nil
}

func billableCompute(row map[string]any) bool {
	status := stringValue(row["status"])
	return providerFreshEnough(row) && billingStatusFor(row) == "active" && (status == "running" || status == "ready" || status == "active")
}

func billableStorage(row map[string]any) bool {
	status := stringValue(row["status"])
	return providerFreshEnough(row) && billingStatusFor(row) == "active" && (status == "available" || status == "ready" || status == "bound")
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
		HoldID:                  stringValue(row["holdId"]),
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
	nextSettlementAt := result.UsagePeriodEnd
	if stringValue(row["billingNextSettlementAt"]) == "" {
		if periodEnd, ok := parseTimeString(result.UsagePeriodEnd); ok {
			nextSettlementAt = periodEnd.Add(time.Hour).Format(time.RFC3339)
		}
	}
	row["billingNextSettlementAt"] = nextSettlementAt
	if result.ResourceType == "storage" {
		return app.tables.SaveStorage(context.Background(), row)
	}
	return app.tables.SaveCompute(context.Background(), row)
}
