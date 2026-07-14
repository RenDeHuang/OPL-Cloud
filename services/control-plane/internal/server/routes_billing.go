package server

import (
	"net/http"
	"strings"

	"opl-cloud/services/control-plane/internal/controlplane"
)

func registerBillingRoutes(mux *http.ServeMux, app *controlPlaneServer, service *controlplane.Service) {
	mux.HandleFunc("GET /api/billing/summary", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		accountID, ok := app.scopedAccountID(w, r, nil)
		if !ok {
			return
		}
		balance, ok := app.liveBalance(w, r, service, accountID)
		if ok {
			writeJSON(w, http.StatusOK, balance)
		}
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
		writeJSON(w, http.StatusOK, receipt)
	}))
	mux.HandleFunc("POST /api/billing/reconciliation", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		if !confirmed(input, "confirm") {
			writeError(w, http.StatusBadRequest, "confirmation_required")
			return
		}
		report, _ := input["report"].(map[string]any)
		if report == nil {
			report = map[string]any{}
		}
		idempotencyKey := firstNonEmpty(r.Header.Get("Idempotency-Key"), stringField(input, "idempotencyKey", ""), stringField(report, "id", ""))
		if idempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
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
		if err := app.appendAuditEvent(r, "billing.reconciliation", "billing_reconciliation", stringField(report, "id", ""), "", nil, result, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusCreated, reconciliationResponse(result))
	}))
}
