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
	errMonthlyInsufficientBalance      = errors.New("monthly_balance_insufficient")
	errMonthlyChargeNeedsReview        = errors.New("monthly_charge_needs_review")
	errMonthlyPreDebitGatewayKey       = errors.New("monthly_pre_debit_gateway_key_unavailable")
	errMonthlyAccountUnmapped          = errors.New("sub2api_account_mapping_required")
	errMonthlyPurchaseRefunded         = errors.New("monthly_purchase_refunded")
	errMonthlyPriceSnapshotUnavailable = errors.New("price_snapshot_unavailable")
	errBillingReviewNotFound           = errors.New("billing_review_not_found")
	errBillingReviewNotPending         = errors.New("billing_review_not_pending")
	errBillingReviewIdentity           = errors.New("billing_review_identity_mismatch")
	errBillingReviewChargeFact         = errors.New("billing_review_charge_fact_unconfirmed")
	errBillingReviewProviderFact       = errors.New("billing_review_provider_fact_unconfirmed")
	errBillingReviewReceipt            = errors.New("billing_review_receipt_pending")
	errBillingReviewRefund             = errors.New("billing_review_refund_pending")
	errInvalidBillingReview            = errors.New("invalid_billing_review_request")
)

const (
	billingReviewActivateCharged = "activate_charged_resource"
	billingReviewTerminateFree   = "terminate_uncharged_absent"
	billingReviewRefundCharged   = "refund_charged_absent"
)

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
	var existing map[string]any
	replayingExisting := false
	if current, ok := app.monthlyResource(input.ResourceType, input.ResourceID); ok {
		existing = current
		if stringValue(existing["accountId"]) != input.AccountID {
			return existing, errIdempotencyConflict
		}
		if stringValue(existing["billingOperationId"]) == input.BillingOperationID {
			replayingExisting = true
			if !monthlyPriceSnapshotAvailable(existing) {
				existing["billingStatus"], existing["lastBillingError"], existing["manualReviewReason"] = "manual_review", errMonthlyPriceSnapshotUnavailable.Error(), errMonthlyPriceSnapshotUnavailable.Error()
				if err := app.saveMonthlyResource(ctx, input.ResourceType, existing); err != nil {
					return existing, err
				}
				return existing, errMonthlyPriceSnapshotUnavailable
			}
		} else {
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
	var quote map[string]any
	if replayingExisting {
		quote = map[string]any{
			"priceVersion": existing["priceVersion"], "currency": existing["currency"], "priceSnapshot": mapField(existing, "priceSnapshot"),
			"pricingVersion": existing["pricingVersion"], "monthlyPriceCnyCents": existing["monthlyPriceCnyCents"], "chargeUsdMicros": existing["chargeUsdMicros"],
		}
	} else {
		var err error
		quote, err = pricingPreviewResponse(map[string]any{"resourceType": input.ResourceType, "packageId": input.PackageID, "sizeGb": input.SizeGB})
		if err != nil {
			return nil, err
		}
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
		"name": input.Name, "packageId": input.PackageID, "resourceType": input.ResourceType, "billingStatus": "charge_pending", "billingOperationId": input.BillingOperationID,
		"billingOperationStartedAt": input.Now.Format(time.RFC3339), "sub2apiRedeemCode": monthlyRedeemCode(input.Environment, input.BillingOperationID),
		"sub2apiRefundCode": monthlyRefundCode(input.Environment, input.BillingOperationID),
		"priceVersion":      stringValue(quote["priceVersion"]), "currency": stringValue(quote["currency"]),
		"priceSnapshot": customerPricingSnapshotDTO(mapField(quote, "priceSnapshot")), "chargeUsdMicros": chargeUSDMicros,
		"billingAnchorDay": int64(anchorDay), "periodStart": periodStart.Format(time.RFC3339),
		"paidThrough": paidThrough.Format(time.RFC3339), "autoRenew": autoRenew, "lastReceiptId": "", "postChargeBalanceKnown": false,
		"status": "provisioning", "desiredStatus": monthlyDesiredStatus(input.ResourceType), "providerStatus": "pending", "zone": input.Zone,
	}
	if pricingVersion := stringValue(quote["pricingVersion"]); pricingVersion != "" {
		row["pricingVersion"] = pricingVersion
	}
	if monthlyPriceCNYCents, ok := requiredNonNegativeInteger(quote, "monthlyPriceCnyCents"); ok {
		row["monthlyPriceCnyCents"] = monthlyPriceCNYCents
	}
	if input.ResourceType == "storage" {
		row["sizeGb"], row["computeAllocationId"] = input.SizeGB, input.ComputeID
	}
	claimed, _, err := app.tables.ClaimResourceBillingOperation(ctx, input.ResourceType, row)
	if err != nil {
		return nil, err
	}
	row = claimed
	projectCanonicalMonthlyPrice(row)
	chargeUSDMicros = int64(numberField(row, "chargeUsdMicros", 0))
	switch stringValue(row["billingStatus"]) {
	case "active":
		return app.ensureMonthlyPurchaseReceipt(ctx, service, row, sub2APIUserID)
	case "manual_review":
		row, err = app.ensureMonthlyReceipt(ctx, service, row, sub2APIUserID, "billing.charge_review_required.v1")
		if err != nil {
			return row, err
		}
		return row, errMonthlyChargeNeedsReview
	case "refund_pending":
		return app.refundMonthlyOperation(ctx, service, row, sub2APIUserID)
	case "refunded":
		row, err = app.ensureMonthlyReceipt(ctx, service, row, sub2APIUserID, "billing.resource_refunded.v1")
		if err != nil {
			return row, err
		}
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
		if stringValue(row["lastBillingError"]) != "sub2api_charge_unconfirmed" {
			delete(row, "lastBillingError")
		}
		preChargeBalance := int64(0)
		if monthlyChargeNeedsBalancePreflight(row) {
			balance, err := service.Sub2APIBalance(ctx, sub2APIUserID)
			if err != nil {
				return row, err
			}
			if balance.USDMicros < chargeUSDMicros {
				row["billingStatus"], row["lastBillingError"] = "failed", errMonthlyInsufficientBalance.Error()
				_ = app.saveMonthlyResource(ctx, input.ResourceType, row)
				return row, errMonthlyInsufficientBalance
			}
			preChargeBalance = balance.USDMicros
		}
		row, err = app.chargeMonthlyOperation(ctx, service, row, sub2APIUserID, preChargeBalance)
		if err != nil {
			if stringValue(row["billingStatus"]) == "manual_review" {
				reviewed, reviewErr := app.markMonthlyManualReview(ctx, service, row, sub2APIUserID, firstNonEmpty(stringValue(row["lastBillingError"]), "sub2api_charge_unconfirmed"))
				return reviewed, errors.Join(err, reviewErr)
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
	if _, err := service.Sub2APIWorkspaceKey(ctx, sub2APIUserID); err != nil {
		return row, errors.Join(errMonthlyPreDebitGatewayKey, err)
	}
	chargeUSDMicros := int64(numberField(row, "chargeUsdMicros", 0))
	verifyDelta := stringValue(row["lastBillingError"]) != "sub2api_charge_unconfirmed"
	_, resumingConfirmedCharge := row["sub2apiChargeConfirmation"]
	if resumingConfirmedCharge {
		confirmation, ok := row["sub2apiChargeConfirmation"].(map[string]any)
		if !ok || !monthlyChargeConfirmationMatches(confirmation, stringValue(row["sub2apiRedeemCode"]), sub2APIUserID, chargeUSDMicros) {
			row["billingStatus"], row["lastBillingError"] = "manual_review", "sub2api_charge_confirmation_invalid"
			if err := app.saveMonthlyResource(ctx, monthlyResourceType(row), row); err != nil {
				return row, err
			}
			return row, errMonthlyChargeNeedsReview
		}
	} else {
		var charge clients.Sub2APICharge
		var err error
		if stringValue(row["lastBillingError"]) == "sub2api_charge_unconfirmed" {
			history, historyErr := service.Sub2APIBalanceHistory(ctx, sub2APIUserID)
			switch code := sub2APIReconciliationCode(row, sub2APIUserID, history); {
			case historyErr != nil || code == "sub2api_charge_missing":
				err = clients.ErrSub2APIChargeUnknown
			case code != "":
				err = clients.ErrSub2APIChargeConflict
			default:
				charge = clients.Sub2APICharge{Code: stringValue(row["sub2apiRedeemCode"]), UserID: sub2APIUserID, ChargeUSDMicros: chargeUSDMicros, Status: "used"}
			}
		} else {
			charge, err = service.ChargeSub2API(ctx, clients.Sub2APIChargeInput{
				UserID: sub2APIUserID, Code: stringValue(row["sub2apiRedeemCode"]), ChargeUSDMicros: chargeUSDMicros,
				Notes: "OPL monthly " + stringValue(row["id"]),
			})
		}
		if err != nil {
			row["lastBillingError"] = "sub2api_charge_unconfirmed"
			if errors.Is(err, clients.ErrSub2APIChargeConflict) {
				row["billingStatus"] = "manual_review"
			}
			if saveErr := app.saveMonthlyResource(ctx, monthlyResourceType(row), row); saveErr != nil {
				return row, saveErr
			}
			return row, err
		}
		confirmation := map[string]any{"code": charge.Code, "userId": charge.UserID, "chargeUsdMicros": charge.ChargeUSDMicros, "status": charge.Status}
		if !monthlyChargeConfirmationMatches(confirmation, stringValue(row["sub2apiRedeemCode"]), sub2APIUserID, chargeUSDMicros) {
			row["billingStatus"], row["lastBillingError"] = "manual_review", "sub2api_charge_confirmation_invalid"
			delete(row, "sub2apiChargeConfirmation")
			if err := app.saveMonthlyResource(ctx, monthlyResourceType(row), row); err != nil {
				return row, err
			}
			return row, errMonthlyChargeNeedsReview
		}
		row["sub2apiChargeConfirmation"] = confirmation
		if err := app.saveMonthlyResource(ctx, monthlyResourceType(row), row); err != nil {
			return row, err
		}
	}
	postCharge, err := service.Sub2APIBalance(ctx, sub2APIUserID)
	if err != nil {
		row["billingStatus"], row["lastBillingError"] = "manual_review", "post_charge_balance_unavailable"
		if saveErr := app.saveMonthlyResource(ctx, monthlyResourceType(row), row); saveErr != nil {
			return row, saveErr
		}
		return row, errMonthlyChargeNeedsReview
	}
	row["postChargeBalanceKnown"], row["postChargeBalanceUsdMicros"] = true, postCharge.USDMicros
	if postCharge.USDMicros < 0 || (!resumingConfirmedCharge && verifyDelta && preChargeBalance > 0 && postCharge.USDMicros > preChargeBalance-chargeUSDMicros) {
		row["billingStatus"], row["lastBillingError"] = "manual_review", errMonthlyChargeNeedsReview.Error()
		if err := app.saveMonthlyResource(ctx, monthlyResourceType(row), row); err != nil {
			return row, err
		}
		return row, errMonthlyChargeNeedsReview
	}
	delete(row, "lastBillingError")
	return row, nil
}

func monthlyChargeNeedsBalancePreflight(row map[string]any) bool {
	_, confirmed := row["sub2apiChargeConfirmation"]
	return !confirmed && stringValue(row["lastBillingError"]) != "sub2api_charge_unconfirmed"
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
	resourceType := monthlyResourceType(row)
	if err := app.tables.SetResourceAutoRenew(ctx, resourceType, stringValue(row["id"]), stringValue(row["accountId"]), false); err != nil {
		return row, err
	}
	row["autoRenew"] = false
	row["billingStatus"], row["lastBillingError"], row["providerStatus"] = "refunded", "provider_absent_refunded", "missing"
	row["status"], row["desiredStatus"] = "external_deleted", "destroyed"
	if err := app.saveMonthlyResource(ctx, resourceType, row); err != nil {
		return row, err
	}
	row, err = app.ensureMonthlyReceipt(ctx, service, row, sub2APIUserID, "billing.resource_refunded.v1")
	if err != nil {
		return row, err
	}
	return row, errMonthlyPurchaseRefunded
}

func (app *controlPlaneServer) markMonthlyManualReview(ctx context.Context, service *controlplane.Service, row map[string]any, sub2APIUserID int64, reason string) (map[string]any, error) {
	row["billingStatus"], row["lastBillingError"] = "manual_review", reason
	row["manualReviewReason"] = reason
	if err := app.saveMonthlyResource(ctx, monthlyResourceType(row), row); err != nil {
		return row, err
	}
	var err error
	row, err = app.ensureMonthlyReceipt(ctx, service, row, sub2APIUserID, "billing.charge_review_required.v1")
	if err != nil {
		return row, err
	}
	return row, errMonthlyChargeNeedsReview
}

func (app *controlPlaneServer) resolveMonthlyBillingReview(ctx context.Context, service *controlplane.Service, input billingReviewResolutionInput) (map[string]any, error) {
	if (input.ResourceType != "compute" && input.ResourceType != "storage") || !validBillingReviewDecision(input.Decision) || input.ResourceID == "" || input.AccountID == "" || input.BillingOperationID == "" || input.IdempotencyKey == "" || input.Reviewer == "" {
		return nil, errInvalidBillingReview
	}
	unlock := app.lockResource(input.ResourceType, input.ResourceID)
	defer unlock()

	row, ok := app.monthlyResource(input.ResourceType, input.ResourceID)
	if !ok {
		return nil, errBillingReviewNotFound
	}
	projectCanonicalMonthlyPrice(row)
	fingerprint := stableID(input.ResourceType, input.ResourceID, input.AccountID, input.BillingOperationID, input.Decision, input.EvidenceRef, input.Reviewer)
	if key := stringValue(row["reviewResolutionKey"]); key != "" {
		if key != input.IdempotencyKey || stringValue(row["reviewResolutionFingerprint"]) != fingerprint {
			return nil, errIdempotencyConflict
		}
		if stringValue(row["reviewResolutionPhase"]) == "completed" {
			result := mapField(row, "reviewResolutionResult")
			if len(result) == 0 {
				return nil, errBillingReviewNotPending
			}
			return result, nil
		}
	}
	if monthlyResourceType(row) != input.ResourceType || stringValue(row["accountId"]) != input.AccountID || stringValue(row["billingOperationId"]) != input.BillingOperationID {
		return nil, errBillingReviewIdentity
	}
	if stringValue(row["billingStatus"]) != "manual_review" {
		return nil, errBillingReviewNotPending
	}
	userID, err := app.sub2APIUserID(ctx, input.AccountID)
	if err != nil {
		return nil, err
	}
	charged := monthlyReviewChargeConfirmed(row, userID)
	uncharged := monthlyReviewChargeNotAttempted(row)
	if (input.Decision == billingReviewActivateCharged || input.Decision == billingReviewRefundCharged) && !charged || input.Decision == billingReviewTerminateFree && !uncharged {
		return nil, errBillingReviewChargeFact
	}
	phase := stringValue(row["reviewResolutionPhase"])
	if phase != "receipt_recorded" {
		synced, err := app.syncMonthlyResource(ctx, service, row)
		if err != nil {
			return nil, errBillingReviewProviderFact
		}
		row = synced
	}
	present := monthlyPurchaseReadbackConfirmed(input.ResourceType, row, row)
	if input.Decision == billingReviewActivateCharged && strings.HasPrefix(input.BillingOperationID, "renewal-") {
		periodStart, paidThrough, ok := monthlyReviewRenewalPeriod(row)
		expected := cloneMap(row)
		expected["periodStart"], expected["paidThrough"] = periodStart, paidThrough
		deadline, deadlineErr := monthlyProviderDeadline(row)
		target, targetErr := time.Parse(time.RFC3339, paidThrough)
		present = ok && deadlineErr == nil && targetErr == nil && !deadline.Before(target) && monthlyPurchaseReadbackConfirmed(input.ResourceType, expected, row)
	}
	absent := monthlyResourceConfirmedAbsent(input.ResourceType, row)
	if input.Decision == billingReviewActivateCharged && !present || (input.Decision == billingReviewTerminateFree || input.Decision == billingReviewRefundCharged) && !absent {
		return nil, errBillingReviewProviderFact
	}

	if stringValue(row["reviewResolutionKey"]) == "" {
		row, err = app.ensureMonthlyReceipt(ctx, service, row, userID, "billing.charge_review_required.v1")
		if err != nil || stringValue(row["lastReceiptId"]) == "" {
			return nil, errBillingReviewReceipt
		}
		row["reviewResolutionKey"] = input.IdempotencyKey
		row["reviewResolutionFingerprint"] = fingerprint
		row["reviewResolutionDecision"] = input.Decision
		row["reviewResolutionEvidenceRef"] = input.EvidenceRef
		row["reviewResolutionReviewer"] = input.Reviewer
		row["reviewResolutionPhase"] = "claimed"
		row["reviewResolutionResolvedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
		row["reviewOriginalReceiptId"] = row["lastReceiptId"]
		delete(row, "providerCommercialReady")
		if err := app.saveMonthlyResource(ctx, input.ResourceType, row); err != nil {
			return nil, err
		}
	}

	if input.Decision == billingReviewRefundCharged && stringValue(row["reviewResolutionPhase"]) != "receipt_pending" && stringValue(row["reviewResolutionPhase"]) != "receipt_recorded" {
		row["reviewResolutionPhase"] = "refund_pending"
		if err := app.saveMonthlyResource(ctx, input.ResourceType, row); err != nil {
			return nil, err
		}
		_, err := service.RefundSub2API(ctx, clients.Sub2APIRefundInput{
			UserID: userID, Code: stringValue(row["sub2apiRefundCode"]), RefundUSDMicros: int64(numberField(row, "chargeUsdMicros", 0)),
			Notes: "OPL monthly review resolution " + input.ResourceID,
		})
		if err != nil {
			row["lastBillingError"] = errBillingReviewRefund.Error()
			_ = app.saveMonthlyResource(ctx, input.ResourceType, row)
			return nil, errBillingReviewRefund
		}
		row["reviewResolutionPhase"] = "receipt_pending"
		delete(row, "lastBillingError")
		if err := app.saveMonthlyResource(ctx, input.ResourceType, row); err != nil {
			return nil, err
		}
	}

	if stringValue(row["reviewResolutionReceiptId"]) == "" {
		receipt, err := service.RecordMonthlyReceipt(ctx, billingReviewClosingReceipt(row, userID), billingReviewReceiptKey(row))
		if err != nil || receipt.ReceiptID == "" {
			row["reviewResolutionPhase"], row["lastBillingError"] = "receipt_pending", errBillingReviewReceipt.Error()
			if saveErr := app.saveMonthlyResource(ctx, input.ResourceType, row); saveErr != nil {
				return nil, saveErr
			}
			return nil, errBillingReviewReceipt
		}
		row["reviewResolutionReceiptId"] = receipt.ReceiptID
		row["reviewResolutionPhase"] = "receipt_recorded"
		delete(row, "lastBillingError")
		if err := app.saveMonthlyResource(ctx, input.ResourceType, row); err != nil {
			return nil, err
		}
	}

	return app.finalizeMonthlyBillingReview(ctx, row)
}

func validBillingReviewDecision(decision string) bool {
	return decision == billingReviewActivateCharged || decision == billingReviewTerminateFree || decision == billingReviewRefundCharged
}

func monthlyReviewChargeConfirmed(row map[string]any, sub2APIUserID int64) bool {
	known, _ := row["postChargeBalanceKnown"].(bool)
	chargeUSDMicros := int64(numberField(row, "chargeUsdMicros", 0))
	return known && numberField(row, "postChargeBalanceUsdMicros", -1) >= 0 && stringValue(row["billingOperationId"]) != "" &&
		monthlyChargeConfirmationMatches(mapField(row, "sub2apiChargeConfirmation"), stringValue(row["sub2apiRedeemCode"]), sub2APIUserID, chargeUSDMicros)
}

func monthlyChargeConfirmationMatches(confirmation map[string]any, code string, userID, chargeUSDMicros int64) bool {
	return len(confirmation) == 4 && code != "" && chargeUSDMicros > 0 && stringValue(confirmation["code"]) == code &&
		numberField(confirmation, "userId", -1) == float64(userID) && numberField(confirmation, "chargeUsdMicros", -1) == float64(chargeUSDMicros) &&
		stringValue(confirmation["status"]) == "used"
}

func monthlyReviewChargeNotAttempted(row map[string]any) bool {
	known, _ := row["postChargeBalanceKnown"].(bool)
	return !known && strings.HasPrefix(stringValue(row["billingOperationId"]), "renewal-") && stringValue(row["manualReviewReason"]) == "fabric_renewal_provider_truth_invalid"
}

func monthlyReviewRenewalPeriod(row map[string]any) (string, string, bool) {
	paidThrough, err := time.Parse(time.RFC3339, stringValue(row["paidThrough"]))
	if err != nil {
		return "", "", false
	}
	anchorDay := int(numberField(row, "billingAnchorDay", float64(paidThrough.Day())))
	return paidThrough.UTC().Format(time.RFC3339), nextBillingMonth(paidThrough, anchorDay).Format(time.RFC3339), true
}

func billingReviewClosingReceipt(row map[string]any, userID int64) clients.ReceiptInput {
	decision := stringValue(row["reviewResolutionDecision"])
	receiptType := "billing.reconciliation.v1"
	if decision == billingReviewActivateCharged {
		receiptType = "billing.resource_purchased.v1"
		if strings.HasPrefix(stringValue(row["billingOperationId"]), "renewal-") {
			receiptType = "billing.resource_renewed.v1"
		}
	} else if decision == billingReviewRefundCharged {
		receiptType = "billing.resource_refunded.v1"
	}
	periodStart, paidThrough := row["periodStart"], row["paidThrough"]
	if start, end, ok := monthlyReviewRenewalPeriod(row); decision == billingReviewActivateCharged && strings.HasPrefix(stringValue(row["billingOperationId"]), "renewal-") && ok {
		periodStart, paidThrough = start, end
	}
	return clients.ReceiptInput{
		Type: receiptType, Status: "completed", Surface: "control_plane", AccountID: stringValue(row["accountId"]),
		WorkspaceID: firstNonEmpty(stringValue(row["workspaceId"]), "account-"+stringValue(row["accountId"])), RequestID: stringValue(row["billingOperationId"]),
		Execution: map[string]any{"resourceType": monthlyResourceType(row), "resourceId": row["id"], "billingOperationId": row["billingOperationId"], "decision": decision},
		InputRefs: map[string]any{"evidenceRef": row["reviewResolutionEvidenceRef"], "reviewReceiptId": row["reviewOriginalReceiptId"]},
		ReviewerChecks: map[string]any{
			"decision": decision, "reviewer": row["reviewResolutionReviewer"], "evidenceRef": row["reviewResolutionEvidenceRef"],
			"chargeFact": billingReviewChargeFact(decision), "providerFact": billingReviewProviderFact(decision),
		},
		Cost: map[string]any{
			"priceVersion": row["priceVersion"], "currency": row["currency"], "pricingVersion": row["pricingVersion"], "monthlyPriceCnyCents": row["monthlyPriceCnyCents"], "chargeUsdMicros": row["chargeUsdMicros"],
			"sub2apiUserId": userID, "sub2apiRedeemCode": row["sub2apiRedeemCode"], "periodStart": periodStart, "paidThrough": paidThrough,
			"resourceType": monthlyResourceType(row), "resourceId": row["id"],
		},
		Owner: map[string]any{"accountId": row["accountId"], "workspaceId": row["workspaceId"]}, SupersedesReceiptID: stringValue(row["reviewOriginalReceiptId"]),
	}
}

func billingReviewChargeFact(decision string) string {
	if decision == billingReviewTerminateFree {
		return "confirmed_not_attempted"
	}
	return "confirmed_charged"
}

func billingReviewProviderFact(decision string) string {
	if decision == billingReviewActivateCharged {
		return "confirmed_present"
	}
	return "confirmed_absent"
}

func billingReviewReceiptKey(row map[string]any) string {
	return "billing-review-resolution:" + stableID(stringValue(row["billingOperationId"]), stringValue(row["reviewResolutionKey"]))
}

func (app *controlPlaneServer) finalizeMonthlyBillingReview(ctx context.Context, row map[string]any) (map[string]any, error) {
	decision := stringValue(row["reviewResolutionDecision"])
	switch decision {
	case billingReviewActivateCharged:
		if strings.HasPrefix(stringValue(row["billingOperationId"]), "renewal-") {
			periodStart, paidThrough, ok := monthlyReviewRenewalPeriod(row)
			if !ok {
				return nil, errBillingReviewProviderFact
			}
			row["periodStart"], row["paidThrough"] = periodStart, paidThrough
		}
		row["billingStatus"] = "active"
		row["desiredStatus"] = monthlyDesiredStatus(monthlyResourceType(row))
		row["providerStatus"] = stringValue(row["status"])
	case billingReviewTerminateFree:
		row["billingStatus"], row["status"], row["desiredStatus"], row["providerStatus"], row["autoRenew"] = "failed", "external_deleted", "destroyed", "missing", false
	case billingReviewRefundCharged:
		row["billingStatus"], row["status"], row["desiredStatus"], row["providerStatus"], row["autoRenew"] = "refunded", "external_deleted", "destroyed", "missing", false
	default:
		return nil, errInvalidBillingReview
	}
	row["lastReceiptId"] = row["reviewResolutionReceiptId"]
	row["reviewResolutionPhase"] = "completed"
	delete(row, "lastBillingError")
	if decision == billingReviewTerminateFree || decision == billingReviewRefundCharged {
		if err := app.tables.SetResourceAutoRenew(ctx, monthlyResourceType(row), stringValue(row["id"]), stringValue(row["accountId"]), false); err != nil {
			return nil, err
		}
	}
	result := map[string]any{
		"resourceType": monthlyResourceType(row), "resourceId": row["id"], "accountId": row["accountId"],
		"billingOperationId": row["billingOperationId"], "decision": decision, "evidenceRef": row["reviewResolutionEvidenceRef"],
		"reviewer": row["reviewResolutionReviewer"], "billingStatus": row["billingStatus"], "status": row["status"],
		"providerStatus": row["providerStatus"], "receiptId": row["reviewResolutionReceiptId"], "resolvedAt": row["reviewResolutionResolvedAt"],
	}
	row["reviewResolutionResult"] = result
	if err := app.saveMonthlyResource(ctx, monthlyResourceType(row), row); err != nil {
		return nil, err
	}
	return cloneMap(result), nil
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
			"priceVersion": row["priceVersion"], "currency": row["currency"], "pricingVersion": row["pricingVersion"], "monthlyPriceCnyCents": row["monthlyPriceCnyCents"], "chargeUsdMicros": row["chargeUsdMicros"],
			"sub2apiUserId": sub2APIUserID, "sub2apiRedeemCode": row["sub2apiRedeemCode"], "periodStart": row["periodStart"], "paidThrough": row["paidThrough"],
			"resourceType": resourceType, "resourceId": row["id"], "postChargeBalanceUsdMicros": row["postChargeBalanceUsdMicros"],
		},
		Owner: map[string]any{"accountId": row["accountId"], "workspaceId": row["workspaceId"]},
	}, monthlyReceiptKey(row, receiptType))
	if err != nil {
		row["lastBillingError"] = "ledger_receipt_pending"
		if err := app.saveMonthlyResource(ctx, resourceType, row); err != nil {
			return row, err
		}
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
		(stringValue(facts["cbsStatus"]) == "UNATTACHED" || stringValue(facts["cbsStatus"]) == "ATTACHED") &&
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

func projectCanonicalMonthlyPrice(row map[string]any) {
	_ = monthlyPriceSnapshotAvailable(row)
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

func monthlyPriceIdentity(row map[string]any) (string, int64, bool) {
	normalized := cloneMap(row)
	if !monthlyPriceSnapshotAvailable(normalized) {
		return "", 0, false
	}
	chargeUSDMicros, ok := requiredPositiveInteger(normalized, "chargeUsdMicros")
	return stringValue(normalized["priceVersion"]), chargeUSDMicros, ok
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
	case errors.Is(err, errMonthlyPriceSnapshotUnavailable):
		writeError(w, http.StatusConflict, errMonthlyPriceSnapshotUnavailable.Error())
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
