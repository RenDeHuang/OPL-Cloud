package server

import (
	"errors"
	"net/http"
	"strconv"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

func registerAuthRoutes(mux *http.ServeMux, app *controlPlaneServer, service *controlplane.Service) {
	mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		if !limitJSONBody(w, r) {
			return
		}
		input := decodeJSON(r)
		if app.loginRateLimited(r, input) {
			writeError(w, http.StatusTooManyRequests, "login_rate_limited")
			return
		}
		payload, sessionID, err := app.login(r.Context(), service, input)
		if err != nil {
			switch {
			case errors.Is(err, clients.ErrSub2APIInvalidCredentials), errors.Is(err, errInvalidLocalCredentials):
				app.recordLoginFailure(r, input)
				writeError(w, http.StatusUnauthorized, "invalid_credentials")
			case errors.Is(err, clients.ErrSub2APIAuthRateLimited):
				writeError(w, http.StatusTooManyRequests, "login_rate_limited")
			default:
				writeError(w, http.StatusServiceUnavailable, "authentication_unavailable")
			}
			return
		}
		app.clearLoginFailures(r, input)
		http.SetCookie(w, sessionCookie(sessionID, 12*60*60))
		w.Header().Set("x-opl-csrf-token", stringValue(payload["csrfToken"]))
		writeJSON(w, http.StatusOK, payload)
	})
	mux.HandleFunc("GET /api/auth/me", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		user, ok := app.sessionUserContext(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "not_authenticated")
			return
		}
		userID, err := app.sub2APIUserID(r.Context(), stringValue(user["accountId"]))
		if err != nil {
			writeSourceEnvelope(w, http.StatusInternalServerError, "sub2api", "unavailable", nil)
			return
		}
		identity, err := service.Sub2APIUser(r.Context(), userID)
		if err != nil {
			writeSourceEnvelope(w, http.StatusBadGateway, "sub2api", "unavailable", nil)
			return
		}
		mappedUser, err := app.findUserByID(r.Context(), stringValue(user["id"]))
		if err != nil {
			writeSourceEnvelope(w, http.StatusInternalServerError, "sub2api", "unavailable", nil)
			return
		}
		if mappedUser == nil || stringValue(mappedUser["accountId"]) != stringValue(user["accountId"]) || normalizeEmail(stringValue(mappedUser["email"])) != identity.Email {
			writeSourceEnvelope(w, http.StatusBadGateway, "sub2api", "unavailable", nil)
			return
		}
		writeSourceEnvelope(w, http.StatusOK, "sub2api", "available", map[string]any{
			"consoleUserId": stringValue(user["id"]), "accountId": stringValue(user["accountId"]), "role": stringValue(user["role"]),
			"sub2apiUserId": strconv.FormatInt(identity.ID, 10), "email": identity.Email, "status": identity.Status,
		})
	}))
	mux.HandleFunc("POST /api/auth/logout", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		if err := app.logout(r); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		http.SetCookie(w, sessionCookie("", -1))
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))
}
