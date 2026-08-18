package server

import (
	"context"
	"errors"
	"math"
	"net"
	"net/http"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

const maxJSONSafeInteger = float64(1<<53 - 1)

var (
	errMissingPassword               = errors.New("missing_password")
	errSub2APIUserMappingUnverified  = errors.New("sub2api_user_mapping_unverified")
	errCallerSuppliedSub2APIUserID   = errors.New("sub2api_user_id_forbidden")
	errBootstrapUserIdentityConflict = errors.New("bootstrap_user_identity_conflict")
	errInvalidLocalCredentials       = errors.New("invalid_local_credentials")
)

func (app *controlPlaneServer) createUser(ctx context.Context, service *controlplane.Service, input map[string]any) (map[string]any, error) {
	if _, exists := input["sub2apiUserId"]; exists {
		return nil, errCallerSuppliedSub2APIUserID
	}
	role := "owner"
	if rawRole, exists := input["role"]; exists {
		value, ok := rawRole.(string)
		if !ok || value != "owner" {
			return nil, errInvalidRole
		}
		role = value
	}
	email, err := canonicalEmail(stringValue(input["email"]))
	if err != nil {
		return nil, err
	}
	accountID := strings.TrimSpace(stringValue(input["accountId"]))
	if !validAccountID(accountID) {
		return nil, errInvalidAccountID
	}
	password := stringField(input, "password", "")
	if err := validatePlaintextPassword(password); err != nil {
		return nil, err
	}
	unlock, err := app.lockResourceContext(ctx, "account", accountID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	id := "usr-" + stableID("customer", email)[:18]
	user := map[string]any{"id": id, "email": email, "accountId": accountID, "role": role, "status": "active"}
	accounts, users := controlPlaneRecordSet{}, controlPlaneRecordSet{}
	existingAccount, accountFound, err := app.tables.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if accountFound {
		accounts[accountID] = existingAccount
	}
	for _, lookup := range []func() (map[string]any, bool, error){
		func() (map[string]any, bool, error) { return app.tables.GetUser(ctx, id) },
		func() (map[string]any, bool, error) { return app.tables.GetUserByEmail(ctx, email, true) },
	} {
		row, found, lookupErr := lookup()
		if lookupErr != nil {
			return nil, lookupErr
		}
		if found {
			users[stringValue(row["id"])] = row
		}
	}
	preflightSub2APIUserID := int64(1)
	if accountFound {
		preflightSub2APIUserID = int64(numberField(existingAccount, "sub2apiUserId", 0))
	}
	workspaceEligibility := true
	if value, supplied := input["workspacePurchaseEnabled"].(bool); supplied {
		workspaceEligibility = value
	}
	account := map[string]any{"id": accountID, "ownerUserId": id, "status": "active", "sub2apiUserId": preflightSub2APIUserID, "workspacePurchaseEnabled": workspaceEligibility}
	if err := stageProvisionedAccount(accounts, users, account, user); err != nil {
		return nil, err
	}
	identity, err := service.ResolveOrCreateSub2APIUser(ctx, email, password)
	if err != nil {
		return nil, errSub2APIUserMappingUnverified
	}
	if identity.ID <= 0 || normalizeEmail(identity.Email) != email || identity.Status != "active" {
		return nil, errSub2APIUserMappingUnverified
	}
	account["sub2apiUserId"] = identity.ID
	if err := app.tables.CreateProvisionedAccount(ctx, account, user); err != nil {
		return nil, err
	}
	return sanitizeUser(user), nil
}

func positiveIntegerField(input map[string]any, key string) (int64, bool) {
	value := numberField(input, key, 0)
	return int64(value), value > 0 && value <= maxJSONSafeInteger && value == math.Trunc(value)
}

func (app *controlPlaneServer) disableUser(input map[string]any) (map[string]any, error) {
	id := stringField(input, "userId", "")
	user, err := app.findUserByID(context.Background(), id)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, errUserNotFound
	}
	accountID := stringValue(user["accountId"])
	unlock := app.lockResource("account", accountID)
	defer unlock()
	user, err = app.findUserByID(context.Background(), id)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, errUserNotFound
	}
	if stringValue(user["accountId"]) != accountID {
		return nil, errAccountIdentityConflict
	}
	if stringValue(user["status"]) == "deleted" {
		return nil, errUserDeleted
	}
	if isOperatorUser(user) && stringValue(user["status"]) == "active" {
		return nil, errLastActiveAdmin
	}
	sessionKeys, err := app.sessionKeysForUser(context.Background(), id)
	if err != nil {
		return nil, err
	}
	user["status"] = "disabled"
	user["disabledAt"] = time.Now().UTC().Format(time.RFC3339)
	user["disabledBy"] = firstNonEmpty(stringField(input, "operatorUserId", ""), stringField(input, "disabledBy", ""), "usr-admin")
	user["disabledReason"] = stringField(input, "reason", "admin_disabled")
	if err := app.tables.ApplyUserLifecycle(context.Background(), user); err != nil {
		return nil, err
	}
	app.sessionCredentials.Delete(sessionKeys...)
	return sanitizeUser(user), nil
}

func (app *controlPlaneServer) importBootstrapUsers() error {
	users, err := bootstrapUsersFromEnv()
	if err != nil {
		return err
	}
	if len(users) != 0 {
		return errors.New("OPL_CONSOLE_USERS_JSON is retired")
	}
	return nil
}

func (app *controlPlaneServer) login(ctx context.Context, service *controlplane.Service, input map[string]any) (map[string]any, string, error) {
	email, emailErr := canonicalEmail(stringField(input, "email", ""))
	password := stringField(input, "password", "")
	if emailErr != nil || password == "" {
		return nil, "", errInvalidLocalCredentials
	}
	user, found, err := app.tables.GetUserByEmail(ctx, email, false)
	if err != nil {
		return nil, "", err
	}
	if !found || stringValue(user["status"]) != "active" {
		return nil, "", errInvalidLocalCredentials
	}
	accountID := stringValue(user["accountId"])
	unlock, err := app.lockResourceContext(ctx, "account", accountID)
	if err != nil {
		return nil, "", err
	}
	defer unlock()
	user, err = app.findUserByID(ctx, stringValue(user["id"]))
	if err != nil {
		return nil, "", err
	}
	if user == nil || normalizeEmail(stringValue(user["email"])) != email || stringValue(user["status"]) != "active" {
		return nil, "", errInvalidLocalCredentials
	}
	account, found, err := app.tables.GetAccount(ctx, accountID)
	if err != nil {
		return nil, "", err
	}
	remoteID, mapped := positiveIntegerField(account, "sub2apiUserId")
	operator := isOperatorUser(user)
	if !found || stringValue(account["status"]) != "active" || stringValue(account["ownerUserId"]) != stringValue(user["id"]) || !mapped ||
		(!operator && stringValue(user["role"]) != "owner") || (operator && (stringValue(user["id"]) != "usr-admin" || accountID != "acct-admin")) {
		return nil, "", clients.ErrSub2APIAuthUnavailable
	}
	authentication, err := service.AuthenticateSub2APIUser(ctx, email, password)
	if err != nil {
		return nil, "", err
	}
	identity := authentication.Identity
	if identity.ID != remoteID || normalizeEmail(identity.Email) != email || identity.Status != "active" {
		return nil, "", clients.ErrSub2APIAuthUnavailable
	}
	return app.createSession(user, authentication.AccessToken)
}

func (app *controlPlaneServer) loginRateLimited(r *http.Request, input map[string]any) bool {
	key := loginFailureKey(r, input)
	app.mu.Lock()
	defer app.mu.Unlock()
	failure := app.loginRateLimits[key]
	if !failure.FirstAt.IsZero() && time.Since(failure.FirstAt) > loginFailureWindow {
		delete(app.loginRateLimits, key)
		return false
	}
	return failure.Count >= 5
}

func (app *controlPlaneServer) recordLoginFailure(r *http.Request, input map[string]any) {
	key := loginFailureKey(r, input)
	app.mu.Lock()
	defer app.mu.Unlock()
	failure := app.loginRateLimits[key]
	if failure.FirstAt.IsZero() || time.Since(failure.FirstAt) > loginFailureWindow {
		if _, exists := app.loginRateLimits[key]; !exists && len(app.loginRateLimits) >= maxLoginRateEntries {
			app.expireLoginFailuresLocked(time.Now().UTC())
		}
		if _, exists := app.loginRateLimits[key]; !exists && len(app.loginRateLimits) >= maxLoginRateEntries {
			app.evictOldestLoginFailureLocked()
		}
		failure = loginFailure{FirstAt: time.Now().UTC()}
	}
	failure.Count++
	app.loginRateLimits[key] = failure
}

func (app *controlPlaneServer) expireLoginFailuresLocked(now time.Time) {
	for key, failure := range app.loginRateLimits {
		if !failure.FirstAt.IsZero() && now.Sub(failure.FirstAt) > loginFailureWindow {
			delete(app.loginRateLimits, key)
		}
	}
}

func (app *controlPlaneServer) evictOldestLoginFailureLocked() {
	var oldestKey string
	var oldest time.Time
	for key, failure := range app.loginRateLimits {
		if oldestKey == "" || failure.FirstAt.Before(oldest) {
			oldestKey, oldest = key, failure.FirstAt
		}
	}
	if oldestKey != "" {
		delete(app.loginRateLimits, oldestKey)
	}
}

func (app *controlPlaneServer) clearLoginFailures(r *http.Request, input map[string]any) {
	key := loginFailureKey(r, input)
	app.mu.Lock()
	defer app.mu.Unlock()
	delete(app.loginRateLimits, key)
}

const (
	loginFailureWindow        = 15 * time.Minute
	maxLoginRateEntries       = 10000
	maxLoginRateKeyEmailBytes = 256
)

func loginFailureKey(r *http.Request, input map[string]any) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || host == "" {
		host = r.RemoteAddr
	}
	email := strings.ToLower(strings.TrimSpace(stringField(input, "email", "")))
	if len(email) > maxLoginRateKeyEmailBytes {
		email = email[:maxLoginRateKeyEmailBytes]
	}
	return email + "|" + host
}

func (app *controlPlaneServer) createSession(user map[string]any, bearer string) (map[string]any, string, error) {
	const maxSessionsPerUser = 8
	userID := stringValue(user["id"])
	if existing, err := app.tables.ListSessionsByUser(context.Background(), userID); err != nil {
		return nil, "", err
	} else {
		now := time.Now().UTC()
		for key, session := range existing {
			expiresAt, parseErr := time.Parse(time.RFC3339, stringValue(session["expiresAt"]))
			if parseErr != nil || !expiresAt.After(now) {
				_ = app.tables.DeleteSession(context.Background(), key)
				app.sessionCredentials.Delete(key)
				delete(existing, key)
			}
		}
		for len(existing) >= maxSessionsPerUser {
			var oldestKey string
			var oldestExpiry time.Time
			for key, session := range existing {
				expiresAt, _ := time.Parse(time.RFC3339, stringValue(session["expiresAt"]))
				if oldestKey == "" || expiresAt.Before(oldestExpiry) || expiresAt.Equal(oldestExpiry) && key < oldestKey {
					oldestKey, oldestExpiry = key, expiresAt
				}
			}
			if oldestKey == "" {
				break
			}
			if err := app.tables.DeleteSession(context.Background(), oldestKey); err != nil {
				return nil, "", err
			}
			app.sessionCredentials.Delete(oldestKey)
			delete(existing, oldestKey)
		}
	}
	sessionID, err := randomToken(32)
	if err != nil {
		return nil, "", err
	}
	csrf, err := randomToken(24)
	if err != nil {
		return nil, "", err
	}
	sessionKey := sessionLookupKey(sessionID)
	expiresAt := time.Now().UTC().Add(12 * time.Hour)
	if err := app.sessionCredentials.Put(sessionKey, SessionDelegatedCredential{Bearer: bearer, ExpiresAt: expiresAt}); err != nil {
		return nil, "", err
	}
	if err := app.tables.SaveSession(context.Background(), map[string]any{"id": sessionKey, "userId": userID, "csrf": csrf, "expiresAt": expiresAt.Format(time.RFC3339)}); err != nil {
		app.sessionCredentials.Delete(sessionKey)
		return nil, "", err
	}
	return map[string]any{"user": sanitizeUser(user), "isOperator": isOperatorUser(user), "csrfToken": csrf, "expiresAt": expiresAt.Format(time.RFC3339)}, sessionID, nil
}

type sessionAuthenticationState uint8

type authenticatedSessionContextKey struct{}

const (
	sessionNotAuthenticated sessionAuthenticationState = iota
	sessionAuthenticated
	sessionReauthenticationRequired
	sessionAuthenticationUnavailable
)

func (app *controlPlaneServer) session(r *http.Request) (map[string]any, sessionAuthenticationState) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return nil, sessionNotAuthenticated
	}
	sessionKey := sessionLookupKey(cookie.Value)
	session, found, err := app.tables.GetSession(r.Context(), sessionKey)
	if err != nil {
		return nil, sessionAuthenticationUnavailable
	}
	expiresAt, parseErr := time.Parse(time.RFC3339, stringValue(session["expiresAt"]))
	if !found || parseErr != nil || !expiresAt.After(time.Now().UTC()) {
		app.invalidateSession(r.Context(), sessionKey)
		return nil, sessionReauthenticationRequired
	}
	if _, ok := app.sessionCredentials.Get(sessionKey); !ok {
		app.invalidateSession(r.Context(), sessionKey)
		return nil, sessionReauthenticationRequired
	}
	user, err := app.findUserByID(r.Context(), stringValue(session["userId"]))
	if err != nil {
		return nil, sessionAuthenticationUnavailable
	}
	if user == nil || stringValue(user["status"]) != "active" || !validRole(stringValue(user["role"])) {
		app.invalidateSession(r.Context(), sessionKey)
		return nil, sessionReauthenticationRequired
	}
	if !isOperatorUser(user) {
		active, err := app.hasActiveCustomerAccount(r.Context(), user)
		if err != nil {
			return nil, sessionAuthenticationUnavailable
		}
		if !active {
			app.invalidateSession(r.Context(), sessionKey)
			return nil, sessionReauthenticationRequired
		}
	}
	return map[string]any{"user": sanitizeUser(user), "isOperator": isOperatorUser(user), "csrfToken": stringValue(session["csrf"]), "expiresAt": expiresAt.Format(time.RFC3339)}, sessionAuthenticated
}

func (app *controlPlaneServer) invalidateSession(ctx context.Context, sessionKey string) {
	app.sessionCredentials.Delete(sessionKey)
	_ = app.tables.DeleteSession(ctx, sessionKey)
}

func isOperatorUser(user map[string]any) bool {
	return isReservedOperatorIdentity(user) && stringValue(user["status"]) == "active"
}

func isReservedOperatorIdentity(user map[string]any) bool {
	_, emailErr := canonicalEmail(stringValue(user["email"]))
	return emailErr == nil && stringValue(user["id"]) == "usr-admin" && stringValue(user["accountId"]) == "acct-admin" && stringValue(user["role"]) == "admin"
}

func ownsAccount(account, user map[string]any) bool {
	accountID := stringValue(account["id"])
	return account != nil && accountID != "" && stringValue(user["accountId"]) == accountID &&
		stringValue(account["ownerUserId"]) == stringValue(user["id"]) &&
		(stringValue(user["role"]) == "owner" || isOperatorUser(user))
}

func ownsActiveAccount(account, user map[string]any) bool {
	return ownsAccount(account, user) && stringValue(account["status"]) == "active" && stringValue(user["status"]) == "active"
}

func (app *controlPlaneServer) hasActiveCustomerAccount(ctx context.Context, user map[string]any) (bool, error) {
	accountID := stringValue(user["accountId"])
	account, found, err := app.tables.GetAccount(ctx, accountID)
	if err != nil {
		return false, err
	}
	if !found || !ownsActiveAccount(account, user) {
		return false, nil
	}
	return stringValue(user["status"]) == "active", nil
}

func (app *controlPlaneServer) sessionUserID(r *http.Request) string {
	user, ok := app.sessionUserContext(r)
	if !ok {
		return ""
	}
	return stringValue(user["id"])
}

func (app *controlPlaneServer) sessionUserContext(r *http.Request) (map[string]any, bool) {
	if payload, ok := r.Context().Value(authenticatedSessionContextKey{}).(map[string]any); ok {
		user, _ := payload["user"].(map[string]any)
		return user, user != nil
	}
	payload, state := app.session(r)
	if state != sessionAuthenticated {
		return nil, false
	}
	user, _ := payload["user"].(map[string]any)
	return user, user != nil
}

func (app *controlPlaneServer) logout(r *http.Request) error {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil
	}
	sessionKey := sessionLookupKey(cookie.Value)
	app.sessionCredentials.Delete(sessionKey)
	return app.tables.DeleteSession(r.Context(), sessionKey)
}

func (app *controlPlaneServer) sessionKeysForUser(ctx context.Context, userID string) ([]string, error) {
	sessions, err := app.tables.ListSessionsByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0)
	for key, session := range sessions {
		if stringValue(session["userId"]) == userID {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func (app *controlPlaneServer) findUserByID(ctx context.Context, id string) (map[string]any, error) {
	user, found, err := app.tables.GetUser(ctx, id)
	if err != nil || !found {
		return nil, err
	}
	return cloneMap(user), nil
}
