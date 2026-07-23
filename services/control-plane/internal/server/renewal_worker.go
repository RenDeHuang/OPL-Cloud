package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
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
	workspaces, err := app.tables.ListWorkspaces(ctx, "")
	if err != nil {
		return err
	}
	var errs []error
	for _, workspace := range workspaces {
		_, present, stateErr := normalizeWorkspaceBillingStateForWorkspace(workspace, workspace)
		if stateErr != nil {
			errs = append(errs, fmt.Errorf("workspace %s: %w", stringValue(workspace["id"]), stateErr))
			continue
		}
		if !present {
			continue
		}
		if err := app.processWorkspaceRenewal(ctx, service, stringValue(workspace["id"]), now.UTC()); err != nil && !monthlyBusinessOutcome(err) {
			errs = append(errs, fmt.Errorf("workspace %s: %w", stringValue(workspace["id"]), err))
		}
	}
	return errors.Join(errs...)
}

func monthlyBusinessOutcome(err error) bool {
	return errors.Is(err, errMonthlyInsufficientBalance) || errors.Is(err, errMonthlyAccountUnmapped)
}

func monthlyRenewalProviderTruth(resourceType string, row map[string]any) (time.Time, bool) {
	deadline, deadlineErr := monthlyProviderDeadline(row)
	paidThrough, paidErr := time.Parse(time.RFC3339, stringValue(row["paidThrough"]))
	if deadlineErr != nil || paidErr != nil || deadline.Before(paidThrough) || !monthlyPurchaseReadbackConfirmed(resourceType, row, row) {
		return time.Time{}, false
	}
	if providerDeadline := strings.TrimSpace(providerDataValue(row, "deadline")); providerDeadline != "" {
		parsed, err := time.Parse(time.RFC3339, providerDeadline)
		if err != nil || !parsed.Equal(deadline) {
			return time.Time{}, false
		}
	}
	zone := strings.TrimSpace(stringValue(row["zone"]))
	if providerZone := strings.TrimSpace(providerDataValue(row, "zone")); zone == "" || (providerZone != "" && providerZone != zone) {
		return time.Time{}, false
	}
	if !monthlyProviderFactMatches(row, "chargeType", "PREPAID") || !monthlyProviderFactMatches(row, "renewFlag", "NOTIFY_AND_MANUAL_RENEW") {
		return time.Time{}, false
	}
	if resourceType == "storage" {
		sizeGB := numberField(row, "sizeGb", 0)
		return deadline, sizeGB > 0 && sizeGB == math.Trunc(sizeGB)
	}
	providerID := stringValue(row["providerResourceId"])
	instanceID, cvmInstanceID := stringValue(row["instanceId"]), stringValue(row["cvmInstanceId"])
	if providerID == "" || firstNonEmpty(instanceID, cvmInstanceID) != providerID || (instanceID != "" && instanceID != providerID) || (cvmInstanceID != "" && cvmInstanceID != providerID) {
		return time.Time{}, false
	}
	return deadline, true
}

func monthlyProviderFactMatches(row map[string]any, key, expected string) bool {
	value, providerValue := strings.TrimSpace(stringValue(row[key])), strings.TrimSpace(providerDataValue(row, key))
	return (value == "" || value == expected) && (providerValue == "" || providerValue == expected) && (value == expected || providerValue == expected)
}

func monthlyRenewalReadbackConfirmed(resourceType string, expected, row, result map[string]any, oldDeadline, paidThrough time.Time) bool {
	if !monthlyReadbackIdentityMatches(expected, stringValue(result["id"]), stringValue(result["accountId"]), stringValue(result["workspaceId"])) || stringValue(result["providerRequestId"]) == "" {
		return false
	}
	providerResourceID := stringValue(result["providerResourceId"])
	zone := firstNonEmpty(stringValue(result["zone"]), providerDataValue(result, "zone"))
	if providerResourceID == "" || providerResourceID != stringValue(expected["providerResourceId"]) || zone == "" || zone != stringValue(expected["zone"]) {
		return false
	}
	chargeType := firstNonEmpty(stringValue(result["chargeType"]), providerDataValue(result, "chargeType"))
	renewFlag := firstNonEmpty(stringValue(result["renewFlag"]), providerDataValue(result, "renewFlag"))
	renewalResult := providerDataValue(result, "renewalResult")
	deadline, err := monthlyProviderDeadline(result)
	if err != nil || !deadline.After(oldDeadline) || deadline.Before(paidThrough) || chargeType != "PREPAID" || renewFlag != "NOTIFY_AND_MANUAL_RENEW" || (renewalResult != "renewed" && renewalResult != "already_renewed") {
		return false
	}
	if !monthlyResourcePrepared(resourceType, row) {
		return false
	}
	if resourceType == "storage" {
		return int(numberField(result, "sizeGb", 0)) == int(numberField(expected, "sizeGb", 0)) &&
			(stringValue(result["cbsStatus"]) == "UNATTACHED" || stringValue(result["cbsStatus"]) == "ATTACHED")
	}
	expectedInstanceType := monthlyComputeInstanceType(stringValue(expected["packageId"]))
	instanceType, providerInstanceType := stringValue(result["instanceType"]), providerDataValue(result, "instanceType")
	instanceID := firstNonEmpty(stringValue(result["instanceId"]), stringValue(result["cvmInstanceId"]))
	expectedInstanceID := firstNonEmpty(stringValue(expected["instanceId"]), stringValue(expected["cvmInstanceId"]))
	return stringValue(result["packageId"]) == stringValue(expected["packageId"]) && expectedInstanceType != "" &&
		instanceType == expectedInstanceType && providerInstanceType == expectedInstanceType && instanceID != "" && instanceID == expectedInstanceID
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
