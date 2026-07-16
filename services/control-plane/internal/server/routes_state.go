package server

import (
	"errors"
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
		balance, ok := app.liveBalance(w, r, service, accountID, true)
		if !ok {
			return
		}
		computePools, ok := fabricComputePools(w, r, service)
		if !ok {
			return
		}
		state := app.state(accountID, computePools)
		state["balance"] = balance
		if user, ok := app.sessionUserContext(r); ok {
			state["user"] = sanitizeUser(user)
		}
		writeJSON(w, http.StatusOK, state)
	}))
	mux.HandleFunc("GET /api/pricing/catalog", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		computePools, ok := fabricComputePools(w, r, service)
		if !ok {
			return
		}
		catalog, err := app.pricingCatalogResponse(r.Context(), computePools)
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
		_, ok := app.scopedAccountID(w, r, input)
		if !ok {
			return
		}
		preview, err := app.pricingPreviewResponse(r.Context(), input)
		if err != nil {
			writePricingError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, preview)
	}))
	mux.HandleFunc("GET /api/management/state", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		if !app.syncRuntimeOperations(w, r, service) {
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
		writeJSON(w, http.StatusOK, app.operatorSummary())
	}))
}

func (app *controlPlaneServer) liveBalance(w http.ResponseWriter, r *http.Request, service *controlplane.Service, accountID string, allowUnavailable bool) (map[string]any, bool) {
	userID, err := app.sub2APIUserID(r.Context(), accountID)
	if err != nil {
		writeError(w, http.StatusConflict, errMonthlyAccountUnmapped.Error())
		return nil, false
	}
	balance, err := service.Sub2APIBalance(r.Context(), userID)
	if err != nil {
		if allowUnavailable {
			return map[string]any{"source": "sub2api", "currency": "USD", "status": "unavailable", "available": false}, true
		}
		writeUpstreamError(w, err)
		return nil, false
	}
	return map[string]any{"source": "sub2api", "currency": "USD", "status": "available", "available": true, "userId": balance.UserID, "usdMicros": balance.USDMicros}, true
}

func writePricingError(w http.ResponseWriter, err error) {
	if errors.Is(err, errInvalidPricingInput) {
		writeError(w, http.StatusBadRequest, "invalid_pricing_input")
		return
	}
	writeError(w, http.StatusInternalServerError, "pricing_catalog_unavailable")
}
