package server

import (
	"net/http"

	"opl-cloud/services/control-plane/internal/controlplane"
)

func registerBillingRoutes(mux *http.ServeMux, app *controlPlaneApp, service *controlplane.Service) {
	mux.HandleFunc("GET /api/billing/summary", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		accountID, ok := app.scopedAccountID(w, r, nil)
		if !ok {
			return
		}
		wallet, err := service.Wallet(r.Context(), accountID)
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		if err := app.applyLedgerFacts(accountID, wallet, nil, nil, nil, nil); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, walletProjection(wallet))
	}))
	mux.HandleFunc("POST /api/billing/topups", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		if !confirmed(input, "confirm") {
			writeError(w, http.StatusBadRequest, "confirmation_required")
			return
		}
		idempotencyKey := firstNonEmpty(r.Header.Get("Idempotency-Key"), stringField(input, "idempotencyKey", ""))
		if idempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		result, err := service.ManualTopUp(r.Context(), controlplane.ManualTopUpInput{
			AccountID:      stringField(input, "accountId", "acct-local"),
			AmountCents:    moneyToCents(input),
			Currency:       stringField(input, "currency", "CNY"),
			OperatorUserID: firstNonEmpty(app.sessionUserID(r), stringField(input, "operatorUserId", "operator")),
			Reason:         stringField(input, "reason", ""),
		}, idempotencyKey)
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		if err := app.rememberManualTopUp(result); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "billing.topup", "account", result.TopUp.AccountID, result.TopUp.AccountID, nil, manualTopUpResponse(result), "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusCreated, manualTopUpResponse(result))
	}))
	mux.HandleFunc("POST /api/billing/resource-settlements", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		if !confirmed(input, "confirm") {
			writeError(w, http.StatusBadRequest, "confirmation_required")
			return
		}
		idempotencyKey := firstNonEmpty(r.Header.Get("Idempotency-Key"), stringField(input, "idempotencyKey", ""), stringField(input, "sourceEventId", ""))
		if idempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		settlement := controlplane.ResourceSettlementInput{
			AccountID:               stringField(input, "accountId", "acct-local"),
			WorkspaceID:             stringField(input, "workspaceId", ""),
			ResourceType:            stringField(input, "resourceType", "compute"),
			ResourceID:              firstNonEmpty(stringField(input, "resourceId", ""), stringField(input, "computeAllocationId", ""), stringField(input, "storageId", "")),
			AmountCents:             settlementAmountCents(input),
			Currency:                stringField(input, "currency", "CNY"),
			PricingVersion:          stringField(input, "pricingVersion", ""),
			PriceSnapshot:           mapField(input, "priceSnapshot"),
			UsagePeriodStart:        stringField(input, "usagePeriodStart", ""),
			UsagePeriodEnd:          stringField(input, "usagePeriodEnd", ""),
			Quantity:                numberField(input, "quantity", 0),
			Unit:                    stringField(input, "unit", ""),
			ProviderCostEvidenceRef: stringField(input, "providerCostEvidenceRef", ""),
		}
		result, err := service.SettleResource(r.Context(), settlement, idempotencyKey)
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		result = completeSettlementResult(result, settlement)
		if err := app.rememberResourceSettlement(result); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		body := settlementResponse(result)
		if err := app.appendAuditEvent(r, "billing.settle_resource", "ledger_settlement", stringValue(body["id"]), result.AccountID, nil, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusCreated, body)
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
