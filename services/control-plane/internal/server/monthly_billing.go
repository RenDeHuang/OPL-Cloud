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
	errMonthlyPurchaseRefunded    = errors.New("monthly_purchase_refunded")
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
	ComputeID          string
	Zone               string
	Environment        string
	AutoRenew          *bool
	Now                time.Time
}

func (app *controlPlaneServer) purchaseMonthlyResource(ctx context.Context, service *controlplane.Service, input monthlyPurchaseInput) (map[string]any, error) {
	if input.ResourceID == "" || input.BillingOperationID == "" || input.AccountID == "" {
		return nil, errors.New("monthly_purchase_identity_required")
	}
	if input.ResourceType == "compute" && input.Zone == "" {
		input.Zone = monthlyComputeLaunchZone()
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
			input.ComputeID = stringValue(existing["computeAllocationId"])
			input.Zone = stringValue(existing["zone"])
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
	autoRenew := false
	if input.AutoRenew != nil {
		autoRenew = *input.AutoRenew
	}
	chargeUSDMicros := int64(numberField(quote, "chargeUsdMicros", 0))
	row := map[string]any{
		"id": input.ResourceID, "accountId": input.AccountID, "ownerUserId": input.OwnerUserID, "workspaceId": input.WorkspaceID,
		"name": input.Name, "packageId": input.PackageID, "billingStatus": "charge_pending", "billingOperationId": input.BillingOperationID,
		"billingOperationStartedAt": input.Now.Format(time.RFC3339), "sub2apiRedeemCode": monthlyRedeemCode(input.Environment, input.BillingOperationID),
		"sub2apiRefundCode": monthlyRefundCode(input.Environment, input.BillingOperationID),
		"pricingVersion":    stringValue(quote["pricingVersion"]), "monthlyPriceCnyCents": int64(numberField(quote, "monthlyPriceCnyCents", 0)),
		"chargeUsdMicros": chargeUSDMicros, "billingAnchorDay": int64(anchorDay), "periodStart": periodStart.Format(time.RFC3339),
		"paidThrough": paidThrough.Format(time.RFC3339), "autoRenew": autoRenew, "postChargeBalanceKnown": false,
		"status": "provisioning", "desiredStatus": monthlyDesiredStatus(input.ResourceType), "providerStatus": "pending", "zone": input.Zone,
	}
	if input.ResourceType == "storage" {
		row["sizeGb"], row["computeAllocationId"] = input.SizeGB, input.ComputeID
	}
	claimed, _, err := app.tables.ClaimResourceBillingOperation(ctx, input.ResourceType, row)
	if err != nil {
		return nil, err
	}
	row = claimed
	switch stringValue(row["billingStatus"]) {
	case "active":
		return app.ensureMonthlyPurchaseReceipt(ctx, service, row, sub2APIUserID)
	case "manual_review":
		row, _ = app.ensureMonthlyReceipt(ctx, service, row, sub2APIUserID, "billing.charge_review_required.v1")
		return row, errMonthlyChargeNeedsReview
	case "refund_pending":
		return app.refundMonthlyOperation(ctx, service, row, sub2APIUserID)
	case "refunded":
		row, _ = app.ensureMonthlyReceipt(ctx, service, row, sub2APIUserID, "billing.resource_refunded.v1")
		return row, errMonthlyPurchaseRefunded
	case "failed":
		return row, errors.New("monthly_purchase_failed")
	case "preparing":
		// The debit was confirmed and persisted before provider mutation.
	case "charge_pending":
		preflightInput := clients.MonthlyPreflightInput{
			ResourceType: input.ResourceType, PackageID: stringValue(row["packageId"]), SizeGB: int(numberField(row, "sizeGb", 0)), Zone: stringValue(row["zone"]),
		}
		preflight, err := service.PreflightMonthlyResource(ctx, preflightInput)
		if err != nil {
			row["lastBillingError"] = "fabric_monthly_preflight_failed"
			_ = app.saveMonthlyResource(ctx, input.ResourceType, row)
			return row, err
		}
		if !monthlyPreflightConfirmed(preflightInput, preflight) {
			row["lastBillingError"] = "fabric_monthly_preflight_invalid"
			_ = app.saveMonthlyResource(ctx, input.ResourceType, row)
			return row, errors.New("fabric_monthly_preflight_invalid")
		}
		delete(row, "lastBillingError")
		balance, err := service.Sub2APIBalance(ctx, sub2APIUserID)
		if err != nil {
			return row, err
		}
		if balance.USDMicros < chargeUSDMicros {
			row["billingStatus"], row["lastBillingError"] = "failed", errMonthlyInsufficientBalance.Error()
			_ = app.saveMonthlyResource(ctx, input.ResourceType, row)
			return row, errMonthlyInsufficientBalance
		}
		row, err = app.chargeMonthlyOperation(ctx, service, row, sub2APIUserID, balance.USDMicros)
		if err != nil {
			if stringValue(row["billingStatus"]) == "manual_review" {
				return app.markMonthlyManualReview(ctx, service, row, sub2APIUserID, firstNonEmpty(stringValue(row["lastBillingError"]), "sub2api_charge_unconfirmed"))
			}
			return row, err
		}
		row["billingStatus"] = "preparing"
		delete(row, "lastBillingError")
		if err := app.saveMonthlyResource(ctx, input.ResourceType, row); err != nil {
			return row, err
		}
	default:
		return app.markMonthlyManualReview(ctx, service, row, sub2APIUserID, "monthly_purchase_state_unknown")
	}

	creating := stringValue(row["providerRequestId"]) == ""
	row, err = app.prepareMonthlyResource(ctx, service, row)
	if err != nil && creating {
		row, err = app.syncMonthlyResource(ctx, service, row)
	}
	if err != nil {
		return app.markMonthlyManualReview(ctx, service, row, sub2APIUserID, "fabric_prepare_unknown")
	}
	if monthlyResourceConfirmedAbsent(input.ResourceType, row) {
		return app.refundMonthlyOperation(ctx, service, row, sub2APIUserID)
	}
	if monthlyResourceInProgress(row) {
		if err := app.saveMonthlyResource(ctx, input.ResourceType, row); err != nil {
			return row, err
		}
		return row, nil
	}
	if !monthlyResourcePrepared(input.ResourceType, row) || row["providerCommercialReady"] != true {
		return app.markMonthlyManualReview(ctx, service, row, sub2APIUserID, "fabric_prepare_partial")
	}
	delete(row, "providerCommercialReady")
	row["billingStatus"] = "active"
	delete(row, "lastBillingError")
	if err := app.saveMonthlyResource(ctx, input.ResourceType, row); err != nil {
		return row, err
	}
	return app.ensureMonthlyReceipt(ctx, service, row, sub2APIUserID, "billing.resource_purchased.v1")
}

func monthlyPreflightConfirmed(input clients.MonthlyPreflightInput, result clients.MonthlyPreflight) bool {
	if result.ResourceType != input.ResourceType || result.PackageID != input.PackageID || result.SizeGB != input.SizeGB ||
		!result.Available || result.ChargeType != "PREPAID" || result.PeriodMonths != 1 ||
		result.RenewFlag != "NOTIFY_AND_MANUAL_RENEW" || result.ProviderPriceCNY <= 0 ||
		strings.TrimSpace(input.Zone) == "" || result.Zone != input.Zone {
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

func (app *controlPlaneServer) resumeMonthlyPurchase(ctx context.Context, service *controlplane.Service, row map[string]any) (map[string]any, error) {
	return app.purchaseMonthlyResource(ctx, service, monthlyPurchaseInput{
		ResourceType: monthlyResourceType(row), ResourceID: stringValue(row["id"]), BillingOperationID: stringValue(row["billingOperationId"]),
		AccountID: stringValue(row["accountId"]), OwnerUserID: stringValue(row["ownerUserId"]), WorkspaceID: stringValue(row["workspaceId"]),
		Name: stringValue(row["name"]), PackageID: stringValue(row["packageId"]), SizeGB: int(numberField(row, "sizeGb", 0)),
		ComputeID: stringValue(row["computeAllocationId"]), Zone: stringValue(row["zone"]), Environment: monthlyEnvironment(),
	})
}

func (app *controlPlaneServer) chargeMonthlyOperation(ctx context.Context, service *controlplane.Service, row map[string]any, sub2APIUserID, preChargeBalance int64) (map[string]any, error) {
	chargeUSDMicros := int64(numberField(row, "chargeUsdMicros", 0))
	verifyDelta := stringValue(row["lastBillingError"]) != "sub2api_charge_unconfirmed"
	_, err := service.ChargeSub2API(ctx, clients.Sub2APIChargeInput{
		UserID: sub2APIUserID, Code: stringValue(row["sub2apiRedeemCode"]), ChargeUSDMicros: chargeUSDMicros,
		Notes: "OPL monthly " + stringValue(row["id"]),
	})
	if err != nil {
		row["lastBillingError"] = "sub2api_charge_unconfirmed"
		if errors.Is(err, clients.ErrSub2APIChargeConflict) {
			row["billingStatus"] = "manual_review"
		}
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
	if postCharge.USDMicros < 0 || (verifyDelta && preChargeBalance > 0 && postCharge.USDMicros > preChargeBalance-chargeUSDMicros) {
		row["billingStatus"], row["lastBillingError"] = "manual_review", errMonthlyChargeNeedsReview.Error()
		_ = app.saveMonthlyResource(ctx, monthlyResourceType(row), row)
		return row, errMonthlyChargeNeedsReview
	}
	delete(row, "lastBillingError")
	return row, nil
}

func (app *controlPlaneServer) refundMonthlyOperation(ctx context.Context, service *controlplane.Service, row map[string]any, sub2APIUserID int64) (map[string]any, error) {
	row["billingStatus"], row["lastBillingError"] = "refund_pending", "provider_absent_refund_pending"
	if err := app.saveMonthlyResource(ctx, monthlyResourceType(row), row); err != nil {
		return row, err
	}
	_, err := service.RefundSub2API(ctx, clients.Sub2APIRefundInput{
		UserID: sub2APIUserID, Code: stringValue(row["sub2apiRefundCode"]), RefundUSDMicros: int64(numberField(row, "chargeUsdMicros", 0)),
		Notes: "OPL monthly provider absence " + stringValue(row["id"]),
	})
	if err != nil {
		row["lastBillingError"] = "sub2api_refund_unconfirmed"
		_ = app.saveMonthlyResource(ctx, monthlyResourceType(row), row)
		return row, err
	}
	row["billingStatus"], row["lastBillingError"], row["providerStatus"] = "refunded", "provider_absent_refunded", "missing"
	row["status"], row["desiredStatus"] = "external_deleted", "destroyed"
	if err := app.saveMonthlyResource(ctx, monthlyResourceType(row), row); err != nil {
		return row, err
	}
	row, _ = app.ensureMonthlyReceipt(ctx, service, row, sub2APIUserID, "billing.resource_refunded.v1")
	return row, errMonthlyPurchaseRefunded
}

func (app *controlPlaneServer) markMonthlyManualReview(ctx context.Context, service *controlplane.Service, row map[string]any, sub2APIUserID int64, reason string) (map[string]any, error) {
	row["billingStatus"], row["lastBillingError"] = "manual_review", reason
	row["manualReviewReason"] = reason
	_ = app.saveMonthlyResource(ctx, monthlyResourceType(row), row)
	row, _ = app.ensureMonthlyReceipt(ctx, service, row, sub2APIUserID, "billing.charge_review_required.v1")
	return row, errMonthlyChargeNeedsReview
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
		Execution: map[string]any{"resourceType": resourceType, "resourceId": row["id"], "billingStatus": row["billingStatus"], "reason": row["manualReviewReason"]},
		Cost: map[string]any{
			"pricingVersion": row["pricingVersion"], "monthlyPriceCnyCents": row["monthlyPriceCnyCents"], "chargeUsdMicros": row["chargeUsdMicros"],
			"sub2apiUserId": sub2APIUserID, "sub2apiRedeemCode": row["sub2apiRedeemCode"], "periodStart": row["periodStart"], "paidThrough": row["paidThrough"],
			"resourceType": resourceType, "resourceId": row["id"], "postChargeBalanceUsdMicros": row["postChargeBalanceUsdMicros"],
		},
		Owner: map[string]any{"accountId": row["accountId"], "workspaceId": row["workspaceId"]},
	}, monthlyReceiptKey(row, receiptType))
	if err != nil {
		row["lastBillingError"] = "ledger_receipt_pending"
		_ = app.saveMonthlyResource(ctx, resourceType, row)
		return row, nil
	}
	row["lastReceiptId"] = receipt.ReceiptID
	if stringValue(row["lastBillingError"]) == "ledger_receipt_pending" {
		delete(row, "lastBillingError")
	}
	if err := app.saveMonthlyResource(ctx, resourceType, row); err != nil {
		return row, err
	}
	return row, nil
}

func monthlyReceiptType(row map[string]any) string {
	switch stringValue(row["billingStatus"]) {
	case "manual_review":
		return "billing.charge_review_required.v1"
	case "refunded":
		return "billing.resource_refunded.v1"
	}
	switch operationID := stringValue(row["billingOperationId"]); {
	case strings.HasPrefix(operationID, "renewal-"):
		return "billing.resource_renewed.v1"
	case strings.HasPrefix(operationID, "expiry-"):
		return "billing.resource_expired.v1"
	default:
		return "billing.resource_purchased.v1"
	}
}

func monthlyReceiptKey(row map[string]any, receiptType string) string {
	key := stringValue(row["billingOperationId"]) + ":receipt"
	if receiptType == "billing.charge_review_required.v1" || receiptType == "billing.resource_refunded.v1" {
		return key + ":" + receiptType
	}
	return key
}

func (app *controlPlaneServer) prepareMonthlyResource(ctx context.Context, service *controlplane.Service, row map[string]any) (map[string]any, error) {
	resourceType, id := monthlyResourceType(row), stringValue(row["id"])
	if stringValue(row["providerRequestId"]) != "" {
		return app.syncMonthlyResource(ctx, service, row)
	}
	if resourceType == "storage" {
		volume, err := service.PrepareMonthlyStorage(ctx, clients.StorageVolumeInput{
			ID: id, AccountID: stringValue(row["accountId"]), WorkspaceID: stringValue(row["workspaceId"]),
			ComputeID: stringValue(row["computeAllocationId"]), Zone: stringValue(row["zone"]), SizeGB: int(numberField(row, "sizeGb", 0)),
		}, stringValue(row["billingOperationId"])+":prepare")
		if !monthlyReadbackIdentityMatches(row, volume.ID, volume.AccountID, volume.WorkspaceID) {
			return row, errors.New("fabric_storage_identity_mismatch")
		}
		facts := structToMap(volume)
		expected := row
		row = mergeMaps(row, facts)
		row["billingStatus"] = "preparing"
		if monthlyResourcePrepared(resourceType, row) {
			if !monthlyPurchaseReadbackConfirmed(resourceType, expected, facts) {
				return row, errors.New("fabric_storage_commercial_readback_invalid")
			}
			applyMonthlyProviderDeadline(row)
			row["providerCommercialReady"] = true
		}
		return row, err
	}
	allocation, err := service.PrepareMonthlyCompute(ctx, clients.ComputeAllocationInput{ID: id, AccountID: stringValue(row["accountId"]), WorkspaceID: stringValue(row["workspaceId"]), PackageID: stringValue(row["packageId"])}, stringValue(row["billingOperationId"])+":prepare")
	if !monthlyReadbackIdentityMatches(row, allocation.ID, allocation.AccountID, allocation.WorkspaceID) {
		return row, errors.New("fabric_compute_identity_mismatch")
	}
	facts := structToMap(allocation)
	expected := row
	row = mergeMaps(row, facts)
	row["billingStatus"] = "preparing"
	if monthlyResourcePrepared(resourceType, row) {
		if !monthlyPurchaseReadbackConfirmed(resourceType, expected, facts) {
			return row, errors.New("fabric_compute_commercial_readback_invalid")
		}
		applyMonthlyProviderDeadline(row)
		row["providerCommercialReady"] = true
	}
	return row, err
}

func (app *controlPlaneServer) syncMonthlyResource(ctx context.Context, service *controlplane.Service, row map[string]any) (map[string]any, error) {
	id := stringValue(row["id"])
	if monthlyResourceType(row) == "storage" {
		volume, err := service.SyncMonthlyStorage(ctx, id)
		if !monthlyReadbackIdentityMatches(row, volume.ID, volume.AccountID, volume.WorkspaceID) {
			return row, errors.New("fabric_storage_identity_mismatch")
		}
		facts := structToMap(volume)
		expected := row
		row = mergeMaps(row, facts)
		if monthlyResourcePrepared("storage", row) {
			if !monthlyPurchaseReadbackConfirmed("storage", expected, facts) {
				return row, errors.New("fabric_storage_commercial_readback_invalid")
			}
			applyMonthlyProviderDeadline(row)
			row["providerCommercialReady"] = true
		}
		return row, err
	}
	allocation, err := service.SyncMonthlyCompute(ctx, id)
	if !monthlyReadbackIdentityMatches(row, allocation.ID, allocation.AccountID, allocation.WorkspaceID) {
		return row, errors.New("fabric_compute_identity_mismatch")
	}
	facts := structToMap(allocation)
	expected := row
	row = mergeMaps(row, facts)
	if monthlyResourcePrepared("compute", row) {
		if !monthlyPurchaseReadbackConfirmed("compute", expected, facts) {
			return row, errors.New("fabric_compute_commercial_readback_invalid")
		}
		applyMonthlyProviderDeadline(row)
		row["providerCommercialReady"] = true
	}
	return row, err
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
		return stringValue(facts["providerResourceId"]) != "" &&
			stringValue(facts["packageId"]) == stringValue(row["packageId"]) &&
			firstNonEmpty(stringValue(facts["instanceId"]), stringValue(facts["cvmInstanceId"])) != "" &&
			expectedInstanceType != "" && instanceType == expectedInstanceType && providerInstanceType == expectedInstanceType
	}
	return strings.HasPrefix(stringValue(facts["providerResourceId"]), "disk-") &&
		stringValue(facts["diskType"]) != "" &&
		int(numberField(facts, "sizeGb", 0)) == int(numberField(row, "sizeGb", 0))
}

func monthlyProviderDeadline(row map[string]any) (time.Time, error) {
	value := strings.TrimSpace(firstNonEmpty(stringValue(row["deadline"]), providerDataValue(row, "deadline")))
	deadline, err := time.Parse(time.RFC3339, value)
	return deadline.UTC(), err
}

func applyMonthlyProviderDeadline(row map[string]any) {
	deadline, err := monthlyProviderDeadline(row)
	if err != nil {
		return
	}
	canonical := deadline.Format(time.RFC3339)
	row["deadline"] = canonical
	providerData := cloneMap(mapField(row, "providerData"))
	providerData["deadline"] = canonical
	row["providerData"] = providerData
	if paidThrough, err := time.Parse(time.RFC3339, stringValue(row["paidThrough"])); err == nil && deadline.Before(paidThrough) {
		row["paidThrough"] = canonical
	}
}

func (app *controlPlaneServer) cleanupMonthlyResource(ctx context.Context, service *controlplane.Service, row map[string]any) (map[string]any, error) {
	row = cloneMap(row)
	row["desiredStatus"] = "destroyed"
	id, key := stringValue(row["id"]), stringValue(row["billingOperationId"])+":cleanup"
	if monthlyResourceType(row) == "storage" {
		result, err := service.CleanupMonthlyStorage(ctx, id, key)
		row = mergeMaps(row, structToMap(result))
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
	case errors.Is(err, errMonthlyAccountUnmapped), errors.Is(err, errMonthlyChargeNeedsReview), errors.Is(err, errMonthlyPurchaseRefunded), errors.Is(err, errIdempotencyConflict), errors.Is(err, errBillingOperationInProgress):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeUpstreamError(w, err)
	}
}

func monthlyEnvironment() string {
	return os.Getenv("NODE_ENV")
}

func monthlyComputeLaunchZone() string {
	return strings.TrimSpace(os.Getenv("OPL_TENCENT_ZONE"))
}

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
