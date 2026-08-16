package server

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

var (
	errMonthlyInsufficientBalance             = errors.New("monthly_balance_insufficient")
	errMonthlyAccountUnmapped                 = errors.New("sub2api_account_mapping_required")
	errWorkspaceLaunchMonthlyPreflightInvalid = errors.New("fabric_monthly_preflight_invalid")
	errBillingReviewNotFound                  = errors.New("billing_review_not_found")
	errBillingReviewNotPending                = errors.New("billing_review_not_pending")
	errBillingReviewIdentity                  = errors.New("billing_review_identity_mismatch")
	errBillingReviewChargeFact                = errors.New("billing_review_charge_fact_unconfirmed")
	errBillingReviewProviderFact              = errors.New("billing_review_provider_fact_unconfirmed")
	errBillingReviewReceipt                   = errors.New("billing_review_receipt_pending")
	errInvalidBillingReview                   = errors.New("invalid_billing_review_request")
)

const billingReviewActivateCharged = "activate_charged_resource"

type billingReviewResolutionInput struct {
	ResourceType       string
	ResourceID         string
	AccountID          string
	BillingOperationID string
	Decision           string
	EvidenceRef        string
	IdempotencyKey     string
	Reviewer           string
}

func monthlyPreflightConfirmed(input clients.MonthlyPreflightInput, result clients.MonthlyPreflight) bool {
	return result.ResourceType == input.ResourceType && result.PackageID == input.PackageID && result.SizeGB == input.SizeGB &&
		result.Zone == input.Zone && result.Available
}

func (a *controlPlaneWorkspaceLaunchStageAdapter) preflightWorkspaceLaunchMonthly(ctx context.Context, operation workspaceLaunchReconcileOperation) error {
	zone := controlplane.ProviderAcceptanceLaunchZone()
	if zone == "" {
		return errWorkspaceLaunchMonthlyPreflightInvalid
	}
	for _, input := range []clients.MonthlyPreflightInput{
		{ResourceType: "compute", PackageID: operation.stringFact("packageId"), Zone: zone},
		{ResourceType: "storage", PackageID: operation.stringFact("packageId"), SizeGB: operation.intFact("sizeGb"), Zone: zone},
	} {
		result, err := a.service.PreflightMonthlyResource(ctx, input)
		if err != nil {
			return err
		}
		if !monthlyPreflightConfirmed(input, result) {
			return errWorkspaceLaunchMonthlyPreflightInvalid
		}
	}
	return nil
}

func monthlyChargeConfirmationMatches(confirmation map[string]any, code string, userID, chargeUSDMicros int64) bool {
	return len(confirmation) == 4 && code != "" && chargeUSDMicros > 0 && stringValue(confirmation["code"]) == code &&
		numberField(confirmation, "userId", -1) == float64(userID) && numberField(confirmation, "chargeUsdMicros", -1) == float64(chargeUSDMicros) &&
		stringValue(confirmation["status"]) == "used"
}

func (app *controlPlaneServer) sub2APIUserID(ctx context.Context, accountID string) (int64, error) {
	accounts, err := app.tables.ListAccounts(ctx, "")
	if err != nil {
		return 0, err
	}
	for _, account := range accounts {
		if stringValue(account["id"]) == accountID {
			if userID := int64(numberField(account, "sub2apiUserId", 0)); userID > 0 {
				if err := validateSub2APIAccountMapping(accounts, account); err != nil {
					return 0, err
				}
				return userID, nil
			}
			break
		}
	}
	return 0, errMonthlyAccountUnmapped
}

func monthlyResourceType(row map[string]any) string {
	if resourceType := stringValue(row["resourceType"]); resourceType == "compute" || resourceType == "storage" {
		return resourceType
	}
	if numberField(row, "sizeGb", 0) > 0 {
		return "storage"
	}
	return "compute"
}

func monthlyPriceSnapshotAvailable(row map[string]any) bool {
	_, hasPriceVersion := row["priceVersion"]
	_, hasCurrency := row["currency"]
	_, hasSnapshot := row["priceSnapshot"]
	if hasPriceVersion || hasCurrency || hasSnapshot {
		return canonicalMonthlyPriceSnapshotValid(row)
	}
	priceVersion := strings.TrimSpace(stringValue(row["pricingVersion"]))
	monthlyPriceCNYCents, validCNY := requiredNonNegativeInteger(row, "monthlyPriceCnyCents")
	chargeUSDMicros, validCharge := requiredNonNegativeInteger(row, "chargeUsdMicros")
	resourceType, packageID := monthlyResourceType(row), strings.TrimSpace(stringValue(row["packageId"]))
	if priceVersion == "" || packageID == "" || !validCNY || monthlyPriceCNYCents <= 0 || !validCharge || chargeUSDMicros <= 0 {
		return false
	}
	snapshot := map[string]any{
		"resourceType": resourceType, "priceVersion": priceVersion, "packageId": packageID,
		"currency": pricingCurrency, "billingUnit": pricingBillingUnit, "chargeUsdMicros": chargeUSDMicros,
	}
	if resourceType == "storage" {
		sizeGB, ok := requiredPositiveInteger(row, "sizeGb")
		if !ok {
			return false
		}
		snapshot["sizeGb"] = sizeGB
	}
	row["resourceType"], row["priceVersion"], row["currency"] = resourceType, priceVersion, pricingCurrency
	row["priceSnapshot"] = snapshot
	return true
}

func canonicalMonthlyPriceSnapshotValid(row map[string]any) bool {
	priceVersion, validVersion := row["priceVersion"].(string)
	snapshot, validSnapshot := row["priceSnapshot"].(map[string]any)
	chargeUSDMicros, validCharge := requiredPositiveInteger(row, "chargeUsdMicros")
	snapshotCharge, validSnapshotCharge := requiredPositiveInteger(snapshot, "chargeUsdMicros")
	resourceType, packageID := stringValue(row["resourceType"]), strings.TrimSpace(stringValue(row["packageId"]))
	if !validVersion || strings.TrimSpace(priceVersion) == "" || row["currency"] != pricingCurrency || !validSnapshot ||
		(resourceType != "compute" && resourceType != "storage") || packageID == "" || !validCharge || !validSnapshotCharge || chargeUSDMicros != snapshotCharge ||
		snapshot["priceVersion"] != priceVersion || snapshot["currency"] != pricingCurrency || snapshot["billingUnit"] != pricingBillingUnit ||
		snapshot["resourceType"] != resourceType || snapshot["packageId"] != packageID {
		return false
	}
	if resourceType == "storage" {
		sizeGB, validSize := requiredPositiveInteger(row, "sizeGb")
		snapshotSizeGB, validSnapshotSize := requiredPositiveInteger(snapshot, "sizeGb")
		return validSize && validSnapshotSize && sizeGB == snapshotSizeGB
	}
	_, hasSize := snapshot["sizeGb"]
	return !hasSize
}

func requiredPositiveInteger(input map[string]any, key string) (int64, bool) {
	value, ok := requiredNonNegativeInteger(input, key)
	return value, ok && value > 0
}

func monthlyEnvironment() string { return os.Getenv("NODE_ENV") }

func nextBillingMonth(current time.Time, anchorDay int) time.Time {
	current = current.UTC()
	year, month := current.Year(), current.Month()+1
	if month > 12 {
		year, month = year+1, 1
	}
	lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if anchorDay > lastDay {
		anchorDay = lastDay
	}
	return time.Date(year, month, anchorDay, current.Hour(), current.Minute(), current.Second(), current.Nanosecond(), time.UTC)
}

func monthlyRedeemCode(environment, operationID string) string {
	if environment == "" {
		environment = "local"
	}
	return "opl:" + stableID("sub2api-monthly-charge-v1", environment, operationID)[:28]
}

func monthlyRefundCode(environment, operationID string) string {
	if environment == "" {
		environment = "local"
	}
	return "opl:" + stableID("sub2api-monthly-refund-v1", environment, operationID)[:28]
}
