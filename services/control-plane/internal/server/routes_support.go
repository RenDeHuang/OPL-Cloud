package server

import "net/http"

func registerSupportRoutes(mux *http.ServeMux, app *controlPlaneServer) {
	mux.HandleFunc("GET /api/support/tickets", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		user, _ := app.sessionUserContext(r)
		if r.URL.Query().Get("scope") == "all" && stringValue(user["role"]) != "admin" {
			writeError(w, http.StatusForbidden, "admin_required")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"tickets": app.supportTickets(r.URL.Query().Get("scope") == "all", stringValue(user["accountId"]))})
	}))
	mux.HandleFunc("POST /api/support/tickets", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		accountID, ok := app.scopedAccountID(w, r, input)
		if !ok {
			return
		}
		user, ok := app.sessionUserContext(r)
		withSessionUserContext(input, user, ok)
		input["accountId"] = accountID
		body, err := app.createSupportMapping(input)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := app.appendAuditEvent(r, "support.map_external_ticket", "support_ticket_mapping", stringValue(body["id"]), accountID, nil, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusCreated, body)
	}))
}
