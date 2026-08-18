package server

import (
	"errors"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

func registerAuthRoutes(mux *http.ServeMux, app *controlPlaneServer, service *controlplane.Service) {
	mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		if !validLoginRequest(w, r) {
			return
		}
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
		var identity clients.Sub2APIIdentity
		if app.deployment.customerOwned() {
			cookie, cookieErr := r.Cookie(sessionCookieName)
			if cookieErr != nil {
				writeError(w, http.StatusUnauthorized, "reauthentication_required")
				return
			}
			credential, credentialOK := app.sessionCredentials.Get(sessionLookupKey(cookie.Value))
			if !credentialOK {
				writeError(w, http.StatusUnauthorized, "reauthentication_required")
				return
			}
			identity, err = service.Sub2APIUserWithCredential(r.Context(), credential, userID, stringValue(user["email"]))
		} else {
			identity, err = service.Sub2APIUser(r.Context(), userID)
		}
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

func validLoginRequest(w http.ResponseWriter, r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "json_content_type_required")
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	referer := strings.TrimSpace(r.Header.Get("Referer"))
	if origin == "" && referer == "" {
		return true
	}
	expected, ok := loginRequestOrigin(r)
	if !ok || origin != "" && !sameWebOrigin(origin, expected) || referer != "" && !sameWebOrigin(referer, expected) {
		writeError(w, http.StatusForbidden, "login_origin_forbidden")
		return false
	}
	return true
}

func loginRequestOrigin(r *http.Request) (*url.URL, bool) {
	if configured := strings.TrimSpace(os.Getenv("OPL_PUBLIC_URL")); configured != "" {
		parsed, err := url.Parse(configured)
		if err != nil || !validWebOrigin(parsed) {
			return nil, false
		}
		return parsed, true
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	parsed := &url.URL{Scheme: scheme, Host: r.Host}
	return parsed, validWebOrigin(parsed)
}

func sameWebOrigin(raw string, expected *url.URL) bool {
	actual, err := url.Parse(raw)
	if err != nil || !validWebOrigin(actual) || !validWebOrigin(expected) {
		return false
	}
	return strings.EqualFold(actual.Scheme, expected.Scheme) &&
		strings.EqualFold(actual.Hostname(), expected.Hostname()) &&
		webOriginPort(actual) == webOriginPort(expected)
}

func validWebOrigin(value *url.URL) bool {
	return value != nil && value.User == nil && value.Hostname() != "" && (strings.EqualFold(value.Scheme, "http") || strings.EqualFold(value.Scheme, "https"))
}

func webOriginPort(value *url.URL) string {
	if port := value.Port(); port != "" {
		return port
	}
	if strings.EqualFold(value.Scheme, "https") {
		return "443"
	}
	return "80"
}
