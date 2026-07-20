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
		computePools, ok := fabricComputePools(w, r, service)
		if !ok {
			return
		}
		state := app.state(accountID, computePools)
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
}

func writePricingError(w http.ResponseWriter, err error) {
	if errors.Is(err, errInvalidPricingInput) {
		writeError(w, http.StatusBadRequest, "invalid_pricing_input")
		return
	}
	writeError(w, http.StatusInternalServerError, "pricing_catalog_unavailable")
}
