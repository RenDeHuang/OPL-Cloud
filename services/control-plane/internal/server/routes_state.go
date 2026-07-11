package server

import (
	"net/http"

	"opl-cloud/services/control-plane/internal/controlplane"
)

func registerStateRoutes(mux *http.ServeMux, app *controlPlaneServer, service *controlplane.Service) {
	mux.HandleFunc("GET /api/state", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		accountID, ok := app.scopedAccountID(w, r, nil)
		if !ok {
			return
		}
		if !app.syncRuntimeOperations(w, r, service) {
			return
		}
		if !app.syncLedgerFacts(w, r, service, accountID) {
			return
		}
		computePools, ok := fabricComputePools(w, r, service)
		if !ok {
			return
		}
		state := app.state(accountID, computePools)
		if user, ok := app.sessionUserContext(r); ok {
			state["user"] = sanitizeUser(user)
		}
		writeJSON(w, http.StatusOK, state)
	}))
	mux.HandleFunc("GET /api/pricing/catalog", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		catalog, err := app.pricingCatalogResponse(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "pricing_catalog_unavailable")
			return
		}
		writeJSON(w, http.StatusOK, catalog)
	}))
	mux.HandleFunc("POST /api/pricing/preview", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		if !limitJSONBody(w, r) {
			return
		}
		input := decodeJSON(r)
		accountID, ok := app.scopedAccountID(w, r, input)
		if !ok {
			return
		}
		wallet, err := service.Wallet(r.Context(), accountID)
		if err != nil {
			writeUpstreamError(w)
			return
		}
		preview, err := app.pricingPreviewResponse(r.Context(), input, walletProjection(wallet))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "pricing_catalog_unavailable")
			return
		}
		writeJSON(w, http.StatusOK, preview)
	}))
	mux.HandleFunc("GET /api/management/state", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		if !app.syncRuntimeOperations(w, r, service) {
			return
		}
		if !app.syncLedgerFacts(w, r, service, "") {
			return
		}
		computePools, ok := fabricComputePools(w, r, service)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, app.managementState(r.URL.Query().Get("includeDeleted") == "true", computePools))
	}))
	mux.HandleFunc("GET /api/operator/summary", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		if !app.syncRuntimeOperations(w, r, service) {
			return
		}
		if !app.syncLedgerFacts(w, r, service, "") {
			return
		}
		writeJSON(w, http.StatusOK, app.operatorSummary())
	}))
}
