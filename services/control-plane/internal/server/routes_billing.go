package server

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

func registerBillingRoutes(mux *http.ServeMux, app *controlPlaneServer, service *controlplane.Service) {
	mux.HandleFunc("GET /api/billing/receipts", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		user, ok := app.sessionUserContext(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "not_authenticated")
			return
		}
		accountID := stringValue(user["accountId"])
		limit := 50
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 100 {
				writeError(w, http.StatusBadRequest, "invalid_billing_receipt_limit")
				return
			}
			limit = parsed
		}
		page, err := service.BillingReceipts(r.Context(), clients.ReceiptQuery{AccountID: accountID, Cursor: r.URL.Query().Get("cursor"), Limit: limit})
		if err != nil {
			writeSourceEnvelope(w, http.StatusBadGateway, "ledger", "unavailable", nil)
			return
		}
		receipts := make([]any, 0, len(page.Receipts))
		for _, receipt := range page.Receipts {
			if receipt.AccountID != accountID {
				writeSourceEnvelope(w, http.StatusBadGateway, "ledger", "unavailable", nil)
				return
			}
			if !strings.HasPrefix(receipt.Type, "billing.") {
				continue
			}
			projected, ok := projectCustomerBillingReceipt(receipt)
			if !ok {
				writeSourceEnvelope(w, http.StatusBadGateway, "ledger", "unavailable", nil)
				return
			}
			receipts = append(receipts, projected)
		}
		status := "available"
		if len(receipts) == 0 {
			status = "empty"
		}
		writeSourceEnvelope(w, http.StatusOK, "ledger", status, map[string]any{"receipts": receipts, "nextCursor": page.NextCursor, "hasMore": page.HasMore})
	}))
	mux.HandleFunc("GET /api/billing/receipts/{id}", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		user, ok := app.sessionUserContext(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "not_authenticated")
			return
		}
		accountID := stringValue(user["accountId"])
		receiptID := strings.TrimSpace(r.PathValue("id"))
		receipt, err := service.BillingReceipt(r.Context(), receiptID)
		if err != nil {
			writeSourceEnvelope(w, http.StatusBadGateway, "ledger", "unavailable", nil)
			return
		}
		if receipt.ReceiptID != receiptID {
			writeSourceEnvelope(w, http.StatusBadGateway, "ledger", "unavailable", nil)
			return
		}
		if receipt.AccountID != accountID {
			writeError(w, http.StatusNotFound, "billing_receipt_not_found")
			return
		}
		var projected map[string]any
		var projectedOK bool
		if receipt.Type == "workspace.created" {
			projected, projectedOK = projectWorkspaceCreatedReceipt(receipt)
		} else if strings.HasPrefix(receipt.Type, "billing.") {
			projected, projectedOK = projectCustomerBillingReceipt(receipt)
		}
		if !projectedOK {
			writeSourceEnvelope(w, http.StatusBadGateway, "ledger", "unavailable", nil)
			return
		}
		writeSourceEnvelope(w, http.StatusOK, "ledger", "available", projected)
	}))
	mux.HandleFunc("POST /api/billing/reconciliation", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		if !confirmed(input, "confirm") {
			writeError(w, http.StatusBadRequest, "confirmation_required")
			return
		}
		if _, supplied := input["report"]; supplied {
			writeError(w, http.StatusBadRequest, "reconciliation_report_server_computed")
			return
		}
		idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if idempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		report, err := app.billingReconciliationReport(r.Context(), service, idempotencyKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		result, err := service.RecordReconciliation(r.Context(), controlplane.ReconciliationInput{Report: report}, idempotencyKey)
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		if err := app.rememberReconciliation(result); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "billing.reconciliation", "billing_reconciliation", result.ID, "", nil, result, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusCreated, reconciliationResponse(result))
	}))
}

func projectWorkspaceCreatedReceipt(receipt clients.Receipt) (map[string]any, bool) {
	if strings.TrimSpace(receipt.ReceiptID) == "" || receipt.Type != "workspace.created" || receipt.Status != "completed" || receipt.Surface != "workspace" ||
		strings.TrimSpace(receipt.AccountID) == "" || strings.TrimSpace(receipt.WorkspaceID) == "" {
		return nil, false
	}
	if _, err := time.Parse(time.RFC3339, receipt.CreatedAt); err != nil {
		return nil, false
	}
	return map[string]any{
		"receiptId": receipt.ReceiptID, "type": receipt.Type, "status": receipt.Status,
		"workspaceId": receipt.WorkspaceID, "createdAt": receipt.CreatedAt,
	}, true
}

func projectCustomerBillingReceipt(receipt clients.Receipt) (map[string]any, bool) {
	priceVersion, validVersion := receipt.Cost["priceVersion"].(string)
	currency, validCurrency := receipt.Cost["currency"].(string)
	resourceType := stringValue(receipt.Cost["resourceType"])
	resourceID := stringValue(receipt.Cost["resourceId"])
	periodStart := stringValue(receipt.Cost["periodStart"])
	paidThrough := stringValue(receipt.Cost["paidThrough"])
	if strings.TrimSpace(receipt.ReceiptID) == "" || strings.TrimSpace(receipt.Status) == "" || strings.TrimSpace(receipt.WorkspaceID) == "" || strings.TrimSpace(resourceType) == "" || strings.TrimSpace(resourceID) == "" || !validVersion || strings.TrimSpace(priceVersion) == "" || !validCurrency || currency != pricingCurrency {
		return nil, false
	}
	for _, timestamp := range []string{receipt.CreatedAt, periodStart, paidThrough} {
		if _, err := time.Parse(time.RFC3339, timestamp); err != nil {
			return nil, false
		}
	}
	if legacyVersion, present := receipt.Cost["pricingVersion"]; present && legacyVersion != priceVersion {
		return nil, false
	}
	body := map[string]any{
		"receiptId": receipt.ReceiptID, "type": receipt.Type, "status": receipt.Status,
		"workspaceId": receipt.WorkspaceID, "createdAt": receipt.CreatedAt,
		"resourceType": resourceType, "resourceId": resourceID,
		"priceVersion": priceVersion, "currency": currency,
		"periodStart": periodStart, "paidThrough": paidThrough,
	}
	switch receipt.Type {
	case "billing.workspace_purchased.v1", "billing.workspace_renewed.v1", "billing.workspace_expired.v1", "billing.workspace_refunded.v1":
		total, ok := requiredPositiveInteger(receipt.Cost, "totalUsdMicros")
		components, validComponents := receipt.Cost["components"].(map[string]any)
		compute, validCompute := components["compute"].(map[string]any)
		storage, validStorage := components["storage"].(map[string]any)
		computeID, validComputeID := compute["resourceId"].(string)
		storageID, validStorageID := storage["resourceId"].(string)
		computeCharge, validComputeCharge := requiredPositiveInteger(compute, "chargeUsdMicros")
		storageCharge, validStorageCharge := requiredPositiveInteger(storage, "chargeUsdMicros")
		storageGB, validStorageGB := requiredPositiveInteger(storage, "sizeGb")
		if !ok || body["resourceType"] != "workspace" || body["resourceId"] != receipt.WorkspaceID || receipt.Cost["billingUnit"] != pricingBillingUnit ||
			!validComponents || !validCompute || !validStorage || !validComputeID || strings.TrimSpace(computeID) == "" || !validStorageID || strings.TrimSpace(storageID) == "" ||
			compute["resourceType"] != "compute" || storage["resourceType"] != "storage" || !validComputeCharge || !validStorageCharge || !validStorageGB ||
			computeCharge > total || storageCharge != total-computeCharge {
			return nil, false
		}
		body["totalUsdMicros"] = total
		body["components"] = map[string]any{
			"compute": map[string]any{"resourceType": "compute", "resourceId": computeID, "chargeUsdMicros": computeCharge},
			"storage": map[string]any{"resourceType": "storage", "resourceId": storageID, "sizeGb": storageGB, "chargeUsdMicros": storageCharge},
		}
		if receipt.Type == "billing.workspace_purchased.v1" || receipt.Type == "billing.workspace_renewed.v1" {
			computeRef, validComputeRef := receipt.Execution["computeAllocationId"].(string)
			storageRef, validStorageRef := receipt.Execution["storageId"].(string)
			if !validComputeRef || !validStorageRef || computeRef != computeID || storageRef != storageID {
				return nil, false
			}
			fulfillment := map[string]any{"computeAllocationId": computeID, "storageId": storageID}
			for _, key := range []string{"attachmentId", "runtimeId"} {
				if value, present := receipt.Execution[key]; present {
					text, valid := value.(string)
					if !valid || strings.TrimSpace(text) == "" {
						return nil, false
					}
					fulfillment[key] = text
				}
			}
			if _, present := receipt.Execution["workspaceApiKeyId"]; present {
				keyID, valid := requiredPositiveInteger(receipt.Execution, "workspaceApiKeyId")
				if !valid {
					return nil, false
				}
				fulfillment["workspaceApiKeyId"] = strconv.FormatInt(keyID, 10)
			}
			body["fulfillment"] = fulfillment
		}
		if receipt.Type == "billing.workspace_purchased.v1" || receipt.Type == "billing.workspace_renewed.v1" || receipt.Type == "billing.workspace_refunded.v1" {
			chargeReference, valid := receipt.Cost["sub2apiRedeemCode"].(string)
			if !valid || strings.TrimSpace(chargeReference) == "" {
				return nil, false
			}
			body["chargeReference"] = chargeReference
		}
		if receipt.Type == "billing.workspace_refunded.v1" {
			refund, ok := requiredNonNegativeInteger(receipt.Cost, "refundUsdMicros")
			if !ok || refund != total {
				return nil, false
			}
			body["refundUsdMicros"] = refund
		}
	case "billing.resource_purchased.v1", "billing.resource_renewed.v1", "billing.resource_expired.v1", "billing.resource_refunded.v1", "billing.charge_review_required.v1", "billing.reconciliation.v1":
		charge, ok := requiredNonNegativeInteger(receipt.Cost, "chargeUsdMicros")
		if !ok {
			return nil, false
		}
		body["chargeUsdMicros"] = charge
	default:
		return nil, false
	}
	return body, true
}

func requiredNonNegativeInteger(input map[string]any, key string) (int64, bool) {
	var result int64
	switch value := input[key].(type) {
	case int:
		result = int64(value)
	case int64:
		result = value
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) || value < 0 || value >= math.Exp2(63) {
			return 0, false
		}
		result = int64(value)
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0, false
		}
		result = parsed
	default:
		return 0, false
	}
	return result, result >= 0
}
