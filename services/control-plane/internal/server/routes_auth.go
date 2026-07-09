package server

import (
	"net/http"
	"os"
	"strings"
)

func registerAuthRoutes(mux *http.ServeMux, app *controlPlaneServer) {
	mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		if !limitJSONBody(w, r) {
			return
		}
		input := decodeJSON(r)
		if app.loginRateLimited(r, input) {
			writeError(w, http.StatusTooManyRequests, "login_rate_limited")
			return
		}
		payload, sessionID, err := app.login(input)
		if err != nil {
			app.recordLoginFailure(r, input)
			writeError(w, http.StatusUnauthorized, "invalid_credentials")
			return
		}
		app.clearLoginFailures(r, input)
		http.SetCookie(w, sessionCookie(sessionID, 12*60*60))
		w.Header().Set("x-opl-csrf-token", stringValue(payload["csrfToken"]))
		writeJSON(w, http.StatusOK, payload)
	})
	mux.HandleFunc("POST /api/auth/operator-login", func(w http.ResponseWriter, r *http.Request) {
		if !limitJSONBody(w, r) {
			return
		}
		input := map[string]any{"email": "operator"}
		if app.loginRateLimited(r, input) {
			writeError(w, http.StatusTooManyRequests, "login_rate_limited")
			return
		}
		expectedToken := strings.TrimSpace(os.Getenv("OPL_OPERATOR_SUMMARY_TOKEN"))
		if expectedToken == "" || r.Header.Get("x-opl-operator-token") != expectedToken {
			app.recordLoginFailure(r, input)
			writeError(w, http.StatusUnauthorized, "operator_token_invalid")
			return
		}
		payload, sessionID, err := app.operatorLogin()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "operator_session_failed")
			return
		}
		app.clearLoginFailures(r, input)
		http.SetCookie(w, sessionCookie(sessionID, 12*60*60))
		w.Header().Set("x-opl-csrf-token", stringValue(payload["csrfToken"]))
		writeJSON(w, http.StatusOK, payload)
	})
	mux.HandleFunc("GET /api/auth/me", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		payload, ok := app.session(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "not_authenticated")
			return
		}
		writeJSON(w, http.StatusOK, payload)
	}))
	mux.HandleFunc("POST /api/auth/logout", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		if err := app.logout(r); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		http.SetCookie(w, sessionCookie("", -1))
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("GET /api/me", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		payload, ok := app.session(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "not_authenticated")
			return
		}
		writeJSON(w, http.StatusOK, payload["user"])
	}))
}
