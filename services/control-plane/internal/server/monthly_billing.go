package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

var (
	errMonthlyInsufficientBalance = errors.New("monthly_balance_insufficient")
	errMonthlyChargeNeedsReview   = errors.New("monthly_charge_needs_review")
	errMonthlyAccountUnmapped     = errors.New("sub2api_account_mapping_required")
)

type monthlyPurchaseInput struct {
	ResourceType       string
	ResourceID         string
	BillingOperationID string
	AccountID          string
	OwnerUserID        string
	WorkspaceID        string
	Name               string
	PackageID          string
	SizeGB             int
	Environment        string
	AutoRenew          *bool
	Now                time.Time
}

func (app *controlPlaneServer) purchaseMonthlyResource(ctx context.Context, service *controlplane.Service, input monthlyPurchaseInput) (map[string]any, error) {
	if input.ResourceID == "" || input.BillingOperationID == "" || input.AccountID == "" {
		return nil, errors.New("monthly_purchase_identity_required")
	}
	if input.Now.IsZero() {
		input.Now = time.Now().UTC()
	} else {
		input.Now = input.Now.UTC()
	}
	if existing, ok := app.monthlyResource(input.ResourceType, input.ResourceID); ok {
		if stringValue(existing["accountId"]) != input.AccountID {
			return existing, errIdempotencyConflict
		}
		if stringValue(existing["billingOperationId"]) != input.BillingOperationID {
			if input.ResourceType != "storage" || stringValue(existing["billingStatus"]) != "retained" {
				return existing, errIdempotencyConflict
			}
			input.OwnerUserID = stringValue(existing["ownerUserId"])
			input.WorkspaceID = stringValue(existing["workspaceId"])
			input.Name = stringValue(existing["name"])
			input.PackageID = stringValue(existing["packageId"])
			input.SizeGB = int(numberField(existing, "sizeGb", 0))
		}
	}
	quote, err := pricingPreviewResponse(map[string]any{"resourceType": input.ResourceType, "packageId": input.PackageID, "sizeGb": input.SizeGB})
	if err != nil {
		return nil, err
	}
	sub2APIUserID, err := app.sub2APIUserID(ctx, input.AccountID)
	if err != nil {
		return nil, err
	}
	periodStart, paidThrough, anchorDay := input.Now, nextBillingMonth(input.Now, input.Now.Day()), input.Now.Day()
	if existing, ok := app.monthlyResource(input.ResourceType, input.ResourceID); ok && stringValue(existing["billingOperationId"]) == input.BillingOperationID {
		periodStart = parseMonthlyTime(existing["periodStart"], periodStart)
		paidThrough = parseMonthlyTime(existing["paidThrough"], paidThrough)
		anchorDay = int(numberField(existing, "billingAnchorDay", float64(anchorDay)))
	}
	autoRenew := true
	if input.AutoRenew != nil {
		autoRenew = *input.AutoRenew
	}
	chargeUSDMicros := int64(numberField(quote, "chargeUsdMicros", 0))
	row := map[string]any{
		"id": input.ResourceID, "accountId": input.AccountID, "ownerUserId": input.OwnerUserID, "workspaceId": input.WorkspaceID,
		"name": input.Name, "packageId": input.PackageID, "billingStatus": "preparing", "billingOperationId": input.BillingOperationID,
		"billingOperationStartedAt": input.Now.Format(time.RFC3339), "sub2apiRedeemCode": monthlyRedeemCode(input.Environment, input.BillingOperationID),
		"pricingVersion": stringValue(quote["pricingVersion"]), "monthlyPriceCnyCents": int64(numberField(quote, "monthlyPriceCnyCents", 0)),
		"chargeUsdMicros": chargeUSDMicros, "billingAnchorDay": int64(anchorDay), "periodStart": periodStart.Format(time.RFC3339),
		"paidThrough": paidThrough.Format(time.RFC3339), "autoRenew": autoRenew, "postChargeBalanceKnown": false,
		"status": "provisioning", "desiredStatus": monthlyDesiredStatus(input.ResourceType), "providerStatus": "pending",
	}
	if input.ResourceType == "storage" {
		row["sizeGb"] = input.SizeGB
	}
	claimed, _, err := app.tables.ClaimResourceBillingOperation(ctx, input.ResourceType, row)
	if err != nil {
		return nil, err
	}
	row = claimed
	switch stringValue(row["billingStatus"]) {
	case "active":
		return app.ensureMonthlyPurchaseReceipt(ctx, service, row, sub2APIUserID)
	case "charge_pending", "manual_review":
		return app.confirmMonthlyCharge(ctx, service, row, sub2APIUserID, 0)
	case "failed":
		return row, errors.New("monthly_purchase_failed")
	}

	balance, err := service.Sub2APIBalance(ctx, sub2APIUserID)
	if err != nil {
		return row, err
	}
	if balance.USDMicros < chargeUSDMicros {
		row["billingStatus"], row["lastBillingError"] = "failed", errMonthlyInsufficientBalance.Error()
		_ = app.saveMonthlyResource(ctx, input.ResourceType, row)
		return row, errMonthlyInsufficientBalance
	}
	row, err = app.prepareMonthlyResource(ctx, service, row)
	if err != nil {
		row["billingStatus"], row["lastBillingError"] = "failed", "fabric_prepare_failed"
		var cleanupErr error
		row, cleanupErr = app.cleanupMonthlyResource(ctx, service, row)
		if cleanupErr != nil {
			row["lastBillingError"] = "fabric_prepare_cleanup_failed"
		}
		_ = app.saveMonthlyResource(ctx, input.ResourceType, row)
		return row, err
	}
	if !monthlyResourcePrepared(input.ResourceType, row) {
		if err := app.saveMonthlyResource(ctx, input.ResourceType, row); err != nil {
			return row, err
		}
		return row, nil
	}
	balance, err = service.Sub2APIBalance(ctx, sub2APIUserID)
	if err != nil {
		return row, err
	}
	if balance.USDMicros < chargeUSDMicros {
		row["billingStatus"], row["lastBillingError"] = "failed", errMonthlyInsufficientBalance.Error()
		var cleanupErr error
		row, cleanupErr = app.cleanupMonthlyResource(ctx, service, row)
		if cleanupErr != nil {
			row["lastBillingError"] = "insufficient_balance_cleanup_failed"
		}
		_ = app.saveMonthlyResource(ctx, input.ResourceType, row)
		return row, errMonthlyInsufficientBalance
	}
	row["billingStatus"] = "charge_pending"
	if err := app.saveMonthlyResource(ctx, input.ResourceType, row); err != nil {
		return row, err
	}
	return app.confirmMonthlyCharge(ctx, service, row, sub2APIUserID, balance.USDMicros)
}

func (app *controlPlaneServer) resumeMonthlyPurchase(ctx context.Context, service *controlplane.Service, row map[string]any) (map[string]any, error) {
	return app.purchaseMonthlyResource(ctx, service, monthlyPurchaseInput{
		ResourceType: monthlyResourceType(row), ResourceID: stringValue(row["id"]), BillingOperationID: stringValue(row["billingOperationId"]),
		AccountID: stringValue(row["accountId"]), OwnerUserID: stringValue(row["ownerUserId"]), WorkspaceID: stringValue(row["workspaceId"]),
		Name: stringValue(row["name"]), PackageID: stringValue(row["packageId"]), SizeGB: int(numberField(row, "sizeGb", 0)), Environment: monthlyEnvironment(),
	})
}

func (app *controlPlaneServer) confirmMonthlyCharge(ctx context.Context, service *controlplane.Service, row map[string]any, sub2APIUserID, preChargeBalance int64) (map[string]any, error) {
	row, err := app.chargeMonthlyOperation(ctx, service, row, sub2APIUserID, preChargeBalance)
	if err != nil {
		return row, err
	}
	row["billingStatus"] = "active"
	delete(row, "lastBillingError")
	if err := app.saveMonthlyResource(ctx, monthlyResourceType(row), row); err != nil {
		return row, err
	}
	return app.ensureMonthlyReceipt(ctx, service, row, sub2APIUserID, "billing.resource_purchased.v1")
}

func (app *controlPlaneServer) chargeMonthlyOperation(ctx context.Context, service *controlplane.Service, row map[string]any, sub2APIUserID, preChargeBalance int64) (map[string]any, error) {
	chargeUSDMicros := int64(numberField(row, "chargeUsdMicros", 0))
	_, err := service.ChargeSub2API(ctx, clients.Sub2APIChargeInput{
		UserID: sub2APIUserID, Code: stringValue(row["sub2apiRedeemCode"]), ChargeUSDMicros: chargeUSDMicros,
		Notes: "OPL monthly " + stringValue(row["id"]),
	})
	if err != nil {
		row["billingStatus"], row["lastBillingError"] = "manual_review", "sub2api_charge_unconfirmed"
		_ = app.saveMonthlyResource(ctx, monthlyResourceType(row), row)
		return row, err
	}
	postCharge, err := service.Sub2APIBalance(ctx, sub2APIUserID)
	if err != nil {
		row["billingStatus"], row["lastBillingError"] = "manual_review", "post_charge_balance_unavailable"
		_ = app.saveMonthlyResource(ctx, monthlyResourceType(row), row)
		return row, errMonthlyChargeNeedsReview
	}
	row["postChargeBalanceKnown"], row["postChargeBalanceUsdMicros"] = true, postCharge.USDMicros
	if postCharge.USDMicros < 0 || (preChargeBalance > 0 && postCharge.USDMicros > preChargeBalance-chargeUSDMicros) {
		row["billingStatus"], row["lastBillingError"] = "manual_review", errMonthlyChargeNeedsReview.Error()
		_ = app.saveMonthlyResource(ctx, monthlyResourceType(row), row)
		return row, errMonthlyChargeNeedsReview
	}
	return row, nil
}

func (app *controlPlaneServer) ensureMonthlyPurchaseReceipt(ctx context.Context, service *controlplane.Service, row map[string]any, sub2APIUserID int64) (map[string]any, error) {
	return app.ensureMonthlyReceipt(ctx, service, row, sub2APIUserID, "billing.resource_purchased.v1")
}

func (app *controlPlaneServer) ensureMonthlyReceipt(ctx context.Context, service *controlplane.Service, row map[string]any, sub2APIUserID int64, receiptType string) (map[string]any, error) {
	if stringValue(row["lastReceiptId"]) != "" {
		return row, nil
	}
	resourceType := monthlyResourceType(row)
	receipt, err := service.RecordMonthlyReceipt(ctx, clients.ReceiptInput{
		Type: receiptType, Status: "completed", Surface: "control_plane", AccountID: stringValue(row["accountId"]),
		WorkspaceID: firstNonEmpty(stringValue(row["workspaceId"]), "account-"+stringValue(row["accountId"])), RequestID: stringValue(row["billingOperationId"]),
		Execution: map[string]any{"resourceType": resourceType, "resourceId": row["id"]},
		Cost: map[string]any{
			"pricingVersion": row["pricingVersion"], "monthlyPriceCnyCents": row["monthlyPriceCnyCents"], "chargeUsdMicros": row["chargeUsdMicros"],
			"sub2apiUserId": sub2APIUserID, "sub2apiRedeemCode": row["sub2apiRedeemCode"], "periodStart": row["periodStart"], "paidThrough": row["paidThrough"],
			"resourceType": resourceType, "resourceId": row["id"], "postChargeBalanceUsdMicros": row["postChargeBalanceUsdMicros"],
		},
		Owner: map[string]any{"accountId": row["accountId"], "workspaceId": row["workspaceId"]},
	}, stringValue(row["billingOperationId"])+":receipt")
	if err != nil {
		row["lastBillingError"] = "ledger_receipt_pending"
		_ = app.saveMonthlyResource(ctx, resourceType, row)
		return row, nil
	}
	row["lastReceiptId"] = receipt.ReceiptID
	delete(row, "lastBillingError")
	if err := app.saveMonthlyResource(ctx, resourceType, row); err != nil {
		return row, err
	}
	return row, nil
}

func monthlyReceiptType(row map[string]any) string {
	switch operationID := stringValue(row["billingOperationId"]); {
	case strings.HasPrefix(operationID, "renewal-"):
		return "billing.resource_renewed.v1"
	case strings.HasPrefix(operationID, "expiry-"):
		return "billing.resource_expired.v1"
	default:
		return "billing.resource_purchased.v1"
	}
}

func (app *controlPlaneServer) prepareMonthlyResource(ctx context.Context, service *controlplane.Service, row map[string]any) (map[string]any, error) {
	resourceType, id := monthlyResourceType(row), stringValue(row["id"])
	if resourceType == "storage" {
		var volume clients.StorageVolume
		var err error
		if stringValue(row["providerRequestId"]) != "" {
			volume, err = service.SyncMonthlyStorage(ctx, id)
		} else {
			volume, err = service.PrepareMonthlyStorage(ctx, clients.StorageVolumeInput{ID: id, AccountID: stringValue(row["accountId"]), WorkspaceID: stringValue(row["workspaceId"]), SizeGB: int(numberField(row, "sizeGb", 0))}, stringValue(row["billingOperationId"])+":prepare")
		}
		row = mergeMaps(row, structToMap(volume))
		row["billingStatus"] = "preparing"
		return row, err
	}
	var allocation clients.ComputeAllocation
	var err error
	if stringValue(row["providerRequestId"]) != "" {
		allocation, err = service.SyncMonthlyCompute(ctx, id)
	} else {
		allocation, err = service.PrepareMonthlyCompute(ctx, clients.ComputeAllocationInput{ID: id, AccountID: stringValue(row["accountId"]), WorkspaceID: stringValue(row["workspaceId"]), PackageID: stringValue(row["packageId"])}, stringValue(row["billingOperationId"])+":prepare")
	}
	row = mergeMaps(row, structToMap(allocation))
	row["billingStatus"] = "preparing"
	return row, err
}

func (app *controlPlaneServer) cleanupMonthlyResource(ctx context.Context, service *controlplane.Service, row map[string]any) (map[string]any, error) {
	row = cloneMap(row)
	row["desiredStatus"] = "destroyed"
	id, key := stringValue(row["id"]), stringValue(row["billingOperationId"])+":cleanup"
	if monthlyResourceType(row) == "storage" {
		result, err := service.CleanupMonthlyStorage(ctx, id, key)
		row = mergeMaps(row, structToMap(result))
		if err == nil {
			row["status"] = "destroyed"
		}
		return row, err
	}
	result, err := app.cleanupComputeResource(ctx, service, id, key)
	row = mergeMaps(row, structToMap(result))
	if err == nil {
		row["status"] = "destroyed"
	}
	return row, err
}

func (app *controlPlaneServer) saveMonthlyResource(ctx context.Context, resourceType string, row map[string]any) error {
	var err error
	if resourceType == "storage" {
		err = app.tables.SaveStorage(ctx, row)
	} else {
		err = app.tables.SaveCompute(ctx, row)
	}
	if err == nil {
		app.observeMonthlyOperationalAlerts(resourceType, row)
	}
	return err
}

func (app *controlPlaneServer) monthlyResource(resourceType, id string) (map[string]any, bool) {
	if resourceType == "storage" {
		return app.getStorage(id)
	}
	return app.getCompute(id)
}

func (app *controlPlaneServer) sub2APIUserID(ctx context.Context, accountID string) (int64, error) {
	accounts, err := app.tables.ListAccounts(ctx, accountID)
	if err != nil {
		return 0, err
	}
	for _, account := range accounts {
		if stringValue(account["id"]) == accountID {
			if userID := int64(numberField(account, "sub2apiUserId", 0)); userID > 0 {
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

func monthlyResourceType(row map[string]any) string {
	if numberField(row, "sizeGb", 0) > 0 {
		return "storage"
	}
	return "compute"
}

func monthlyEntitlementActive(row map[string]any, now time.Time) bool {
	status := stringValue(row["billingStatus"])
	if status != "active" && status != "past_due" {
		return false
	}
	paidThrough, err := time.Parse(time.RFC3339, stringValue(row["paidThrough"]))
	return err == nil && now.UTC().Before(paidThrough)
}

func ensureMonthlyEntitlements(w http.ResponseWriter, now time.Time, resources ...map[string]any) bool {
	for _, resource := range resources {
		if !monthlyEntitlementActive(resource, now) {
			writeError(w, http.StatusConflict, "monthly_entitlement_inactive")
			return false
		}
	}
	return true
}

func writeMonthlyPurchaseError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errInvalidPricingInput):
		writeError(w, http.StatusBadRequest, "invalid_pricing_input")
	case errors.Is(err, errMonthlyInsufficientBalance):
		writeError(w, http.StatusPaymentRequired, errMonthlyInsufficientBalance.Error())
	case errors.Is(err, errMonthlyAccountUnmapped), errors.Is(err, errMonthlyChargeNeedsReview), errors.Is(err, errIdempotencyConflict), errors.Is(err, errBillingOperationInProgress):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeUpstreamError(w, err)
	}
}

func monthlyEnvironment() string {
	return os.Getenv("NODE_ENV")
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

func parseMonthlyTime(value any, fallback time.Time) time.Time {
	parsed, err := time.Parse(time.RFC3339, stringValue(value))
	if err != nil {
		return fallback
	}
	return parsed.UTC()
}

func monthlyRedeemCode(environment, operationID string) string {
	if environment == "" {
		environment = "local"
	}
	return "opl:" + stableID("sub2api-monthly-charge-v1", environment, operationID)[:28]
}

func monthlyDesiredStatus(resourceType string) string {
	if resourceType == "storage" {
		return "available"
	}
	return "running"
}
