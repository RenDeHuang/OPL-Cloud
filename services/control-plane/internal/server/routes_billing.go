package server

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

func registerBillingRoutes(mux *http.ServeMux, app *controlPlaneServer, service *controlplane.Service) {
	mux.HandleFunc("GET /api/billing/summary", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		accountID, ok := app.scopedAccountID(w, r, nil)
		if !ok {
			return
		}
		balance, ok := app.liveBalance(w, r, service, accountID, false)
		if ok {
			writeJSON(w, http.StatusOK, balance)
		}
	}))
	mux.HandleFunc("GET /api/billing/receipts", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		accountID, ok := app.scopedAccountID(w, r, nil)
		if !ok {
			return
		}
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
			writeUpstreamError(w, err)
			return
		}
		receipts := make([]any, 0, len(page.Receipts))
		for _, receipt := range page.Receipts {
			if receipt.AccountID != accountID {
				writeError(w, http.StatusBadGateway, "billing_receipt_identity_mismatch")
				return
			}
			if !strings.HasPrefix(receipt.Type, "billing.") {
				continue
			}
			projected, ok := projectCustomerBillingReceipt(receipt)
			if !ok {
				writeError(w, http.StatusBadGateway, "billing_receipt_source_unavailable")
				return
			}
			receipts = append(receipts, projected)
		}
		writeJSON(w, http.StatusOK, map[string]any{"receipts": receipts, "nextCursor": page.NextCursor, "hasMore": page.HasMore})
	}))
	mux.HandleFunc("GET /api/billing/receipts/{id}", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		accountID, ok := app.scopedAccountID(w, r, nil)
		if !ok {
			return
		}
		receipt, err := service.BillingReceipt(r.Context(), strings.TrimSpace(r.PathValue("id")))
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		if receipt.AccountID != accountID || !strings.HasPrefix(receipt.Type, "billing.") {
			writeError(w, http.StatusNotFound, "billing_receipt_not_found")
			return
		}
		projected, ok := projectCustomerBillingReceipt(receipt)
		if !ok {
			writeError(w, http.StatusBadGateway, "billing_receipt_source_unavailable")
			return
		}
		writeJSON(w, http.StatusOK, projected)
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

func projectCustomerBillingReceipt(receipt clients.Receipt) (map[string]any, bool) {
	priceVersion := firstNonEmpty(stringValue(receipt.Cost["priceVersion"]), stringValue(receipt.Cost["pricingVersion"]))
	currency := firstNonEmpty(stringValue(receipt.Cost["currency"]), pricingCurrency)
	if priceVersion == "" || currency != pricingCurrency {
		return nil, false
	}
	chargeUSDMicros, ok := requiredNonNegativeInteger(receipt.Cost, "chargeUsdMicros")
	if !ok {
		return nil, false
	}
	return map[string]any{
		"receiptId": receipt.ReceiptID, "type": receipt.Type, "status": receipt.Status,
		"workspaceId": receipt.WorkspaceID, "createdAt": receipt.CreatedAt,
		"resourceType": stringValue(receipt.Cost["resourceType"]), "resourceId": stringValue(receipt.Cost["resourceId"]),
		"priceVersion": priceVersion, "currency": currency,
		"chargeUsdMicros": chargeUSDMicros, "periodStart": stringValue(receipt.Cost["periodStart"]), "paidThrough": stringValue(receipt.Cost["paidThrough"]),
	}, true
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
