package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/controlplane"
)

const (
	defaultMonthlyBillingInterval = time.Hour
	monthlyRenewalLead            = 24 * time.Hour
)

func monthlyBillingWorkerEnabled() bool {
	value := strings.TrimSpace(os.Getenv("OPL_MONTHLY_BILLING_WORKER_ENABLED"))
	return value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "yes")
}

func monthlyBillingWorkerInterval() time.Duration {
	return durationFromEnv("OPL_MONTHLY_BILLING_INTERVAL_MS", defaultMonthlyBillingInterval)
}

func (app *controlPlaneServer) startMonthlyBillingWorker(ctx context.Context, service *controlplane.Service, interval time.Duration) {
	if interval <= 0 {
		interval = defaultMonthlyBillingInterval
	}
	go func() {
		if err := app.runMonthlyBillingOnce(ctx, service, time.Now().UTC()); err != nil {
			log.Printf("monthly billing failed: %v", err)
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if err := app.runMonthlyBillingOnce(ctx, service, now.UTC()); err != nil {
					log.Printf("monthly billing failed: %v", err)
				}
			}
		}
	}()
}

func (app *controlPlaneServer) runMonthlyBillingOnce(ctx context.Context, service *controlplane.Service, now time.Time) error {
	computes, err := app.tables.ListComputes(ctx, "")
	if err != nil {
		return err
	}
	storages, err := app.tables.ListStorages(ctx, "")
	if err != nil {
		return err
	}
	var errs []error
	for _, row := range computes {
		app.observeMonthlyOperationalAlerts("compute", row)
		if err := app.processMonthlyResource(ctx, service, "compute", row, now.UTC()); err != nil && !monthlyBusinessOutcome(err) {
			errs = append(errs, fmt.Errorf("compute %s: %w", stringValue(row["id"]), err))
		}
	}
	for _, row := range storages {
		app.observeMonthlyOperationalAlerts("storage", row)
		if err := app.processMonthlyResource(ctx, service, "storage", row, now.UTC()); err != nil && !monthlyBusinessOutcome(err) {
			errs = append(errs, fmt.Errorf("storage %s: %w", stringValue(row["id"]), err))
		}
	}
	return errors.Join(errs...)
}

func monthlyBusinessOutcome(err error) bool {
	return errors.Is(err, errMonthlyInsufficientBalance) || errors.Is(err, errMonthlyChargeNeedsReview) || errors.Is(err, errMonthlyAccountUnmapped)
}

func (app *controlPlaneServer) processMonthlyResource(ctx context.Context, service *controlplane.Service, resourceType string, row map[string]any, now time.Time) error {
	id := stringValue(row["id"])
	if id == "" {
		return nil
	}
	unlock := app.lockResource(resourceType, id)
	defer unlock()
	var ok bool
	if row, ok = app.monthlyResource(resourceType, id); !ok {
		return nil
	}
	if stringValue(row["lastBillingError"]) == "ledger_receipt_pending" {
		userID, err := app.sub2APIUserID(ctx, stringValue(row["accountId"]))
		if err != nil {
			return err
		}
		updated, err := app.ensureMonthlyReceipt(ctx, service, row, userID, monthlyReceiptType(row))
		if err != nil {
			return err
		}
		row = updated
	}
	status := stringValue(row["billingStatus"])
	if status == "retained" || status == "stopped" || status == "failed" || status == "manual_review" {
		return nil
	}
	paidThrough, err := time.Parse(time.RFC3339, stringValue(row["paidThrough"]))
	if err != nil {
		return fmt.Errorf("invalid paidThrough: %w", err)
	}
	if monthlyAutoRenew(row) && (status == "active" || status == "past_due" || status == "renewal_pending") && !now.Before(paidThrough.Add(-monthlyRenewalLead)) {
		updated, renewErr := app.renewMonthlyResource(ctx, service, resourceType, row, now)
		row = updated
		if renewErr != nil && !errors.Is(renewErr, errMonthlyInsufficientBalance) && stringValue(row["billingStatus"]) != "manual_review" {
			return renewErr
		}
		if renewedThrough, parseErr := time.Parse(time.RFC3339, stringValue(row["paidThrough"])); parseErr == nil && now.Before(renewedThrough) && stringValue(row["billingStatus"]) == "active" {
			return nil
		}
	}
	if !now.Before(paidThrough) && stringValue(row["billingStatus"]) != "manual_review" {
		_, err := app.expireMonthlyResource(ctx, service, resourceType, row, now)
		return err
	}
	return nil
}

func monthlyAutoRenew(row map[string]any) bool {
	value, ok := row["autoRenew"].(bool)
	return ok && value
}

func (app *controlPlaneServer) renewMonthlyResource(ctx context.Context, service *controlplane.Service, resourceType string, existing map[string]any, now time.Time) (map[string]any, error) {
	paidThrough, err := time.Parse(time.RFC3339, stringValue(existing["paidThrough"]))
	if err != nil {
		return existing, err
	}
	operationID := "renewal-" + stableID(resourceType, stringValue(existing["id"]), paidThrough.Format(time.RFC3339))[:18]
	row := cloneMap(existing)
	row["billingStatus"], row["billingOperationId"] = "renewal_pending", operationID
	row["billingOperationStartedAt"], row["lastRenewalAttemptAt"] = now.Format(time.RFC3339), now.Format(time.RFC3339)
	row["sub2apiRedeemCode"] = monthlyRedeemCode(monthlyEnvironment(), operationID)
	row["lastReceiptId"], row["postChargeBalanceKnown"], row["postChargeBalanceUsdMicros"] = "", false, int64(0)
	delete(row, "lastBillingError")
	row, _, err = app.tables.ClaimResourceBillingOperation(ctx, resourceType, row)
	if err != nil {
		return row, err
	}
	if stringValue(row["billingStatus"]) == "active" {
		return row, nil
	}
	if stringValue(row["billingStatus"]) == "manual_review" {
		return row, errMonthlyChargeNeedsReview
	}
	row["lastReceiptId"] = ""
	userID, err := app.sub2APIUserID(ctx, stringValue(row["accountId"]))
	if err != nil {
		return row, err
	}
	if known, _ := row["postChargeBalanceKnown"].(bool); !known {
		balance, err := service.Sub2APIBalance(ctx, userID)
		if err != nil {
			row["billingStatus"], row["lastBillingError"] = "past_due", "sub2api_balance_unavailable"
			_ = app.saveMonthlyResource(ctx, resourceType, row)
			return row, err
		}
		if balance.USDMicros < int64(numberField(row, "chargeUsdMicros", 0)) {
			row["billingStatus"], row["lastBillingError"] = "past_due", errMonthlyInsufficientBalance.Error()
			_ = app.saveMonthlyResource(ctx, resourceType, row)
			return row, errMonthlyInsufficientBalance
		}
		row, err = app.chargeMonthlyOperation(ctx, service, row, userID, balance.USDMicros)
		if err != nil {
			if stringValue(row["billingStatus"]) != "manual_review" {
				row, _ = app.markMonthlyManualReview(ctx, row, "sub2api_renewal_charge_unconfirmed")
			}
			return row, err
		}
		if err := app.saveMonthlyResource(ctx, resourceType, row); err != nil {
			return row, err
		}
	}
	anchorDay := int(numberField(row, "billingAnchorDay", float64(paidThrough.Day())))
	renewedThrough := nextBillingMonth(paidThrough, anchorDay)
	providerDeadline, err := time.Parse(time.RFC3339, firstNonEmpty(stringValue(row["deadline"]), providerDataValue(row, "deadline")))
	if err != nil {
		return app.markMonthlyManualReview(ctx, row, "fabric_renewal_deadline_unknown")
	}
	row, err = app.renewMonthlyProvider(ctx, service, resourceType, row, providerDeadline, renewedThrough, operationID+":provider-renew")
	if err != nil {
		return app.markMonthlyManualReview(ctx, row, "fabric_renewal_unconfirmed")
	}
	row["periodStart"], row["paidThrough"], row["billingStatus"] = paidThrough.Format(time.RFC3339), renewedThrough.Format(time.RFC3339), "active"
	delete(row, "lastBillingError")
	if err := app.saveMonthlyResource(ctx, resourceType, row); err != nil {
		return row, err
	}
	return app.ensureMonthlyReceipt(ctx, service, row, userID, "billing.resource_renewed.v1")
}

func (app *controlPlaneServer) renewMonthlyProvider(ctx context.Context, service *controlplane.Service, resourceType string, row map[string]any, oldDeadline, paidThrough time.Time, key string) (map[string]any, error) {
	expected := cloneMap(row)
	var result map[string]any
	if resourceType == "storage" {
		volume, err := service.RenewMonthlyStorage(ctx, stringValue(row["id"]), key)
		result = structToMap(volume)
		row = mergeMaps(row, result)
		if err != nil || !monthlyRenewalReadbackConfirmed(resourceType, expected, row, result, oldDeadline, paidThrough) {
			return row, errMonthlyChargeNeedsReview
		}
		return row, nil
	}
	allocation, err := service.RenewMonthlyCompute(ctx, stringValue(row["id"]), key)
	result = structToMap(allocation)
	row = mergeMaps(row, result)
	if err != nil || !monthlyRenewalReadbackConfirmed(resourceType, expected, row, result, oldDeadline, paidThrough) {
		return row, errMonthlyChargeNeedsReview
	}
	return row, nil
}

func monthlyRenewalReadbackConfirmed(resourceType string, expected, row, result map[string]any, oldDeadline, paidThrough time.Time) bool {
	if !monthlyReadbackIdentityMatches(expected, stringValue(result["id"]), stringValue(result["accountId"]), stringValue(result["workspaceId"])) || stringValue(result["providerRequestId"]) == "" {
		return false
	}
	chargeType := firstNonEmpty(stringValue(result["chargeType"]), providerDataValue(result, "chargeType"))
	renewFlag := firstNonEmpty(stringValue(result["renewFlag"]), providerDataValue(result, "renewFlag"))
	deadlineText := firstNonEmpty(stringValue(result["deadline"]), providerDataValue(result, "deadline"))
	renewalResult := providerDataValue(result, "renewalResult")
	deadline, err := time.Parse(time.RFC3339, deadlineText)
	if err != nil || !deadline.After(oldDeadline) || deadline.Before(paidThrough) || chargeType != "PREPAID" || renewFlag != "NOTIFY_AND_MANUAL_RENEW" || (renewalResult != "renewed" && renewalResult != "already_renewed") {
		return false
	}
	if !monthlyResourcePrepared(resourceType, row) {
		return false
	}
	return resourceType != "storage" || stringValue(result["cbsStatus"]) == "UNATTACHED" || stringValue(result["cbsStatus"]) == "ATTACHED"
}

func providerDataValue(row map[string]any, key string) string {
	switch data := row["providerData"].(type) {
	case map[string]any:
		return stringValue(data[key])
	case map[string]string:
		return data[key]
	default:
		return ""
	}
}

func (app *controlPlaneServer) expireMonthlyResource(ctx context.Context, service *controlplane.Service, resourceType string, existing map[string]any, now time.Time) (map[string]any, error) {
	row := cloneMap(existing)
	operationID := "expiry-" + stableID(resourceType, stringValue(row["id"]), stringValue(row["paidThrough"]))[:18]
	if resourceType == "compute" {
		result, err := app.cleanupComputeResource(ctx, service, stringValue(row["id"]), operationID+":destroy")
		if err != nil {
			row["billingStatus"], row["lastBillingError"] = "past_due", "fabric_expiry_destroy_failed"
			_ = app.saveMonthlyResource(ctx, resourceType, row)
			return row, err
		}
		row = mergeMaps(row, structToMap(result))
		row["status"], row["desiredStatus"], row["billingStatus"] = "destroyed", "destroyed", "stopped"
		if err := app.saveComputeFact(row); err != nil {
			return row, err
		}
	} else {
		row["desiredStatus"], row["billingStatus"] = "retained", "retained"
		if err := app.saveStorageFact(row); err != nil {
			return row, err
		}
	}
	row["billingOperationId"], row["billingOperationStartedAt"] = operationID, now.Format(time.RFC3339)
	row["sub2apiRedeemCode"], row["lastReceiptId"], row["postChargeBalanceKnown"], row["postChargeBalanceUsdMicros"] = "", "", false, int64(0)
	delete(row, "lastBillingError")
	if err := app.saveMonthlyResource(ctx, resourceType, row); err != nil {
		return row, err
	}
	userID, err := app.sub2APIUserID(ctx, stringValue(row["accountId"]))
	if err != nil {
		return row, err
	}
	return app.ensureMonthlyReceipt(ctx, service, row, userID, "billing.resource_expired.v1")
}
