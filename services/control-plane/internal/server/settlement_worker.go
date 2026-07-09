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

func (app *controlPlaneApp) startPeriodicSettlementWorker(ctx context.Context, service *controlplane.Service, interval time.Duration) {
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

func (app *controlPlaneApp) runPeriodicSettlementOnce(ctx context.Context, service *controlplane.Service, now time.Time) error {
	periodEnd := now.UTC().Truncate(time.Hour)
	if periodEnd.IsZero() {
		periodEnd = now.UTC()
	}
	periodStart := periodEnd.Add(-time.Hour)
	for _, input := range app.periodicSettlementInputs(periodStart, periodEnd) {
		key := periodicSettlementKey(input)
		result, err := service.SettleResource(ctx, input, key)
		if err != nil {
			return err
		}
		result = completeSettlementResult(result, input)
		if err := app.rememberResourceSettlement(result); err != nil {
			return err
		}
		if err := app.markResourceSettlement(result); err != nil {
			return err
		}
	}
	return nil
}

func (app *controlPlaneApp) periodicSettlementInputs(periodStart time.Time, periodEnd time.Time) []controlplane.ResourceSettlementInput {
	app.mu.Lock()
	defer app.mu.Unlock()
	inputs := []controlplane.ResourceSettlementInput{}
	for _, row := range app.computes {
		if !billableCompute(row) || alreadySettledForPeriod(row, periodEnd) {
			continue
		}
		inputs = append(inputs, periodicSettlementInput(row, "compute", periodStart, periodEnd))
	}
	for _, row := range app.storages {
		if !billableStorage(row) || alreadySettledForPeriod(row, periodEnd) {
			continue
		}
		inputs = append(inputs, periodicSettlementInput(row, "storage", periodStart, periodEnd))
	}
	return inputs
}

func billableCompute(row map[string]any) bool {
	status := stringValue(row["status"])
	return billingStatusFor(row) != "stopped" && (status == "running" || status == "ready" || status == "active")
}

func billableStorage(row map[string]any) bool {
	status := stringValue(row["status"])
	return billingStatusFor(row) != "stopped" && (status == "available" || status == "ready" || status == "bound")
}

func periodicSettlementInput(row map[string]any, resourceType string, periodStart time.Time, periodEnd time.Time) controlplane.ResourceSettlementInput {
	packageID := firstNonEmpty(stringValue(row["packageId"]), "basic")
	plan := packageByID(packageID)
	amountCents := periodicSettlementAmountCents(row, resourceType, plan)
	unitPriceCents := amountCents
	return controlplane.ResourceSettlementInput{
		AccountID:               firstNonEmpty(stringValue(row["accountId"]), stringValue(row["ownerAccountId"]), "acct-local"),
		WorkspaceID:             stringValue(row["workspaceId"]),
		ResourceType:            resourceType,
		ResourceID:              stringValue(row["id"]),
		AmountCents:             amountCents,
		Currency:                pricingCurrency,
		PricingVersion:          pricingCatalogVersion,
		PriceSnapshot:           map[string]any{"packageId": packageID, "resourceType": resourceType, "unitPriceCents": unitPriceCents, "currency": pricingCurrency, "source": "periodic_settlement_worker"},
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

func periodicSettlementAmountCents(row map[string]any, resourceType string, plan map[string]any) int64 {
	if resourceType == "storage" {
		sizeGB := numberField(row, "sizeGb", 10)
		return cents(priceField(plan, "storageGbMonth") * sizeGB / 30 / 24)
	}
	return cents(priceField(plan, "computeHourly"))
}

func periodicSettlementKey(input controlplane.ResourceSettlementInput) string {
	return strings.Join([]string{"periodic-settlement", input.AccountID, input.ResourceType, input.ResourceID, input.UsagePeriodEnd}, ":")
}

func (app *controlPlaneApp) markResourceSettlement(result clients.ResourceSettlementResult) error {
	app.mu.Lock()
	defer app.mu.Unlock()
	var table stateTable
	switch result.ResourceType {
	case "storage":
		table = app.storages
	default:
		table = app.computes
	}
	row := table[result.ResourceID]
	if row == nil {
		return nil
	}
	row["settlementId"] = result.ID
	row["ledgerEntryId"] = result.LedgerEntryID
	row["walletTransactionId"] = result.WalletTransactionID
	row["usagePeriodEnd"] = result.UsagePeriodEnd
	return app.persistLocked()
}
