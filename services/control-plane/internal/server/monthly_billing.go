package server

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
)

var (
	errMonthlyInsufficientBalance = errors.New("monthly_balance_insufficient")
	errMonthlyAccountUnmapped     = errors.New("sub2api_account_mapping_required")
	errBillingReviewNotFound      = errors.New("billing_review_not_found")
	errBillingReviewNotPending    = errors.New("billing_review_not_pending")
	errBillingReviewIdentity      = errors.New("billing_review_identity_mismatch")
	errBillingReviewChargeFact    = errors.New("billing_review_charge_fact_unconfirmed")
	errBillingReviewProviderFact  = errors.New("billing_review_provider_fact_unconfirmed")
	errBillingReviewReceipt       = errors.New("billing_review_receipt_pending")
	errInvalidBillingReview       = errors.New("invalid_billing_review_request")
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
	if result.ResourceType != input.ResourceType || result.PackageID != input.PackageID || result.SizeGB != input.SizeGB ||
		!result.Available || result.ChargeType != "PREPAID" || result.PeriodMonths != 1 ||
		result.RenewFlag != "NOTIFY_AND_MANUAL_RENEW" || result.ProviderPriceCNY <= 0 ||
		strings.TrimSpace(input.Zone) == "" || result.Zone != input.Zone ||
		(input.ResourceType == "compute" && strings.TrimSpace(result.NodePoolID) == "") {
		return false
	}
	requestIDs := []string{"nodePool", "subnets", "availability"}
	if input.ResourceType == "storage" {
		requestIDs = []string{"quota", "price"}
	}
	for _, key := range requestIDs {
		if strings.TrimSpace(result.ProviderRequestIDs[key]) == "" {
			return false
		}
	}
	return true
}

func monthlyChargeConfirmationMatches(confirmation map[string]any, code string, userID, chargeUSDMicros int64) bool {
	return len(confirmation) == 4 && code != "" && chargeUSDMicros > 0 && stringValue(confirmation["code"]) == code &&
		numberField(confirmation, "userId", -1) == float64(userID) && numberField(confirmation, "chargeUsdMicros", -1) == float64(chargeUSDMicros) &&
		stringValue(confirmation["status"]) == "used"
}

func monthlyReadbackIdentityMatches(row map[string]any, id, accountID, workspaceID string) bool {
	return id == stringValue(row["id"]) && accountID == stringValue(row["accountId"]) && workspaceID == stringValue(row["workspaceId"])
}

func monthlyPurchaseReadbackConfirmed(resourceType string, row, facts map[string]any) bool {
	if !monthlyResourcePrepared(resourceType, facts) || stringValue(facts["providerRequestId"]) == "" {
		return false
	}
	deadline, err := monthlyProviderDeadline(facts)
	periodStart, startErr := time.Parse(time.RFC3339, stringValue(row["periodStart"]))
	paidThrough, paidErr := time.Parse(time.RFC3339, stringValue(row["paidThrough"]))
	minimumDeadline := time.Date(paidThrough.Year(), paidThrough.Month(), paidThrough.Day(), 0, 0, 0, 0, time.UTC)
	zone := firstNonEmpty(stringValue(facts["zone"]), providerDataValue(facts, "zone"))
	chargeType := firstNonEmpty(stringValue(facts["chargeType"]), providerDataValue(facts, "chargeType"))
	renewFlag := firstNonEmpty(stringValue(facts["renewFlag"]), providerDataValue(facts, "renewFlag"))
	if err != nil || startErr != nil || paidErr != nil || !deadline.After(periodStart) || deadline.Before(minimumDeadline) || zone == "" || zone != stringValue(row["zone"]) || chargeType != "PREPAID" || renewFlag != "NOTIFY_AND_MANUAL_RENEW" {
		return false
	}
	if resourceType == "compute" {
		instanceType, providerInstanceType := stringValue(facts["instanceType"]), providerDataValue(facts, "instanceType")
		expectedInstanceType := monthlyComputeInstanceType(stringValue(row["packageId"]))
		return stringValue(facts["providerResourceId"]) != "" && stringValue(facts["packageId"]) == stringValue(row["packageId"]) &&
			firstNonEmpty(stringValue(facts["instanceId"]), stringValue(facts["cvmInstanceId"])) != "" &&
			expectedInstanceType != "" && instanceType == expectedInstanceType && providerInstanceType == expectedInstanceType
	}
	return strings.HasPrefix(stringValue(facts["providerResourceId"]), "disk-") && stringValue(facts["diskType"]) != "" &&
		(stringValue(facts["cbsStatus"]) == "UNATTACHED" || stringValue(facts["cbsStatus"]) == "ATTACHED") &&
		int(numberField(facts, "sizeGb", 0)) == int(numberField(row, "sizeGb", 0))
}

func monthlyProviderDeadline(row map[string]any) (time.Time, error) {
	value := strings.TrimSpace(firstNonEmpty(stringValue(row["deadline"]), providerDataValue(row, "deadline")))
	deadline, err := time.Parse(time.RFC3339, value)
	return deadline.UTC(), err
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

func monthlyResourcePrepared(resourceType string, row map[string]any) bool {
	status := stringValue(row["status"])
	if resourceType == "storage" {
		return (status == "available" || status == "ready") && stringValue(row["providerResourceId"]) != ""
	}
	return (status == "running" || status == "ready") && firstNonEmpty(stringValue(row["providerResourceId"]), stringValue(row["instanceId"]), stringValue(row["cvmInstanceId"])) != ""
}

func monthlyResourceInProgress(row map[string]any) bool {
	switch stringValue(row["status"]) {
	case "provisioning", "pending", "creating":
		return stringValue(row["providerRequestId"]) != ""
	default:
		return false
	}
}

func monthlyResourceConfirmedAbsent(resourceType string, row map[string]any) bool {
	if stringValue(row["status"]) != "external_deleted" {
		return false
	}
	return resourceType == "compute" || stringValue(row["cbsStatus"]) == "NOT_FOUND"
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

func monthlyComputeLaunchZone() string { return strings.TrimSpace(os.Getenv("OPL_TENCENT_ZONE")) }

func monthlyComputeInstanceType(packageID string) string {
	if packageID == "pro" {
		return strings.TrimSpace(os.Getenv("OPL_PRO_COMPUTE_INSTANCE_TYPE"))
	}
	if packageID == "basic" {
		return strings.TrimSpace(os.Getenv("OPL_BASIC_COMPUTE_INSTANCE_TYPE"))
	}
	return ""
}

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

func monthlyDesiredStatus(resourceType string) string {
	if resourceType == "storage" {
		return "available"
	}
	return "running"
}
