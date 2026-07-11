package server

import (
	"errors"
	"net/http"
)

func registerAdminRoutes(mux *http.ServeMux, app *controlPlaneServer) {
	mux.HandleFunc("POST /api/organizations", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		body, err := app.createOrganization(decodeJSON(r))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "organization.create", "organization", stringValue(body["id"]), stringValue(body["billingAccountId"]), nil, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusCreated, body)
	}))
	mux.HandleFunc("POST /api/organizations/members", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		body, err := app.createMembership(decodeJSON(r))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "organization.member_add", "organization_membership", stringValue(body["id"]), "", nil, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusCreated, body)
	}))
	mux.HandleFunc("POST /api/organizations/members/{id}/revoke", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		body, err := app.revokeMembership(r.Context(), r.PathValue("id"))
		if err != nil {
			if errors.Is(err, errMembershipNotFound) {
				writeError(w, http.StatusNotFound, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "organization.member_revoke", "organization_membership", stringValue(body["id"]), stringValue(body["accountId"]), nil, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, body)
	}))
	mux.HandleFunc("POST /api/users", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		body, err := app.createUser(input)
		if err != nil {
			writeCreateUserError(w, err)
			return
		}
		if err := app.appendAuditEvent(r, "user.create", "user", stringValue(body["id"]), stringValue(body["accountId"]), nil, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusCreated, body)
	}))
	mux.HandleFunc("POST /api/users/disable", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		withOperatorUserID(input, app.sessionUserID(r))
		body, err := app.disableUser(input)
		if err != nil {
			writeUserLifecycleError(w, err)
			return
		}
		if err := app.appendAuditEvent(r, "user.disable", "user", stringValue(body["id"]), stringValue(body["accountId"]), nil, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, body)
	}))
	mux.HandleFunc("POST /api/users/delete", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		if !confirmed(input, "confirm") {
			writeError(w, http.StatusBadRequest, "confirmation_required")
			return
		}
		withOperatorUserID(input, app.sessionUserID(r))
		body, err := app.softDeleteUser(input)
		if err != nil {
			writeUserLifecycleError(w, err)
			return
		}
		if err := app.appendAuditEvent(r, "user.delete", "user", stringValue(body["id"]), stringValue(body["accountId"]), nil, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, body)
	}))
	mux.HandleFunc("POST /api/operator/cleanup-workspace-access", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		if !confirmed(input, "confirm") {
			writeError(w, http.StatusBadRequest, "confirmation_required")
			return
		}
		result, err := app.cleanupWorkspaceAccess(input)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "operator.cleanup_workspace_access", "workspace_access_cleanup", "", "", nil, result, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}))
	mux.HandleFunc("GET /api/operator/archive", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		result, err := app.archiveState(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "archive_state_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}))
	mux.HandleFunc("POST /api/operator/archive-terminal-resources", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		if !confirmed(input, "confirm") {
			writeError(w, http.StatusBadRequest, "confirmation_required")
			return
		}
		result, err := app.archiveTerminalResources(r.Context(), input)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "operator.archive_terminal_resources", "archive_job", stringValue(result["id"]), "", nil, result, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}))
}
