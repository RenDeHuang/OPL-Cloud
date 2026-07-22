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
	errWeakPassword                  = errors.New("weak_password")
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
	organizationID := "org-" + stableID("account", accountID)[:18]
	user := map[string]any{"id": id, "email": email, "accountId": accountID, "role": role, "status": "active"}
	organization := map[string]any{"id": organizationID, "name": "Organization " + accountID, "billingAccountId": accountID, "status": "active"}
	membership := map[string]any{"id": "mem-" + stableID(organizationID, id)[:18], "accountId": accountID, "organizationId": organizationID, "userId": id, "role": role, "status": "active"}
	accounts, err := app.tables.ListAccounts(ctx, "")
	if err != nil {
		return nil, err
	}
	users, err := app.tables.ListUsers(ctx, true)
	if err != nil {
		return nil, err
	}
	organizations, err := app.tables.ListOrganizations(ctx)
	if err != nil {
		return nil, err
	}
	memberships, err := app.tables.ListMemberships(ctx)
	if err != nil {
		return nil, err
	}
	preflightSub2APIUserID := int64(1)
	if existing := findRecord(accounts, accountID); existing != nil {
		preflightSub2APIUserID = int64(numberField(existing, "sub2apiUserId", 0))
	} else {
		used := map[int64]struct{}{}
		for _, existing := range accounts {
			used[int64(numberField(existing, "sub2apiUserId", 0))] = struct{}{}
		}
		for {
			if _, exists := used[preflightSub2APIUserID]; !exists {
				break
			}
			preflightSub2APIUserID++
		}
	}
	account := map[string]any{"id": accountID, "ownerUserId": id, "status": "active", "sub2apiUserId": preflightSub2APIUserID}
	if err := stageInvitedAccount(recordSetFromRows(accounts), recordSetFromRows(users), recordSetFromRows(organizations), recordSetFromRows(memberships), account, user, organization, membership); err != nil {
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
	if err := app.tables.CreateInvitedAccount(ctx, account, user, organization, membership); err != nil {
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
	email := normalizeEmail(stringField(input, "email", ""))
	password := stringField(input, "password", "")
	users, err := app.tables.ListUsers(ctx, false)
	if err != nil {
		return nil, "", err
	}
	matches := make([]map[string]any, 0, 1)
	for _, user := range users {
		if normalizeEmail(stringValue(user["email"])) == email && stringValue(user["status"]) == "active" {
			matches = append(matches, user)
		}
	}
	if len(matches) != 1 || password == "" {
		return nil, "", errInvalidLocalCredentials
	}
	user := matches[0]
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
	accounts, err := app.tables.ListAccounts(ctx, accountID)
	if err != nil {
		return nil, "", err
	}
	account := findRecord(accounts, accountID)
	remoteID, mapped := positiveIntegerField(account, "sub2apiUserId")
	operator := isOperatorUser(user)
	if account == nil || stringValue(account["status"]) != "active" || stringValue(account["ownerUserId"]) != stringValue(user["id"]) || !mapped ||
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
	if !failure.FirstAt.IsZero() && time.Since(failure.FirstAt) > 15*time.Minute {
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
	if failure.FirstAt.IsZero() || time.Since(failure.FirstAt) > 15*time.Minute {
		failure = loginFailure{FirstAt: time.Now().UTC()}
	}
	failure.Count++
	app.loginRateLimits[key] = failure
}

func (app *controlPlaneServer) clearLoginFailures(r *http.Request, input map[string]any) {
	key := loginFailureKey(r, input)
	app.mu.Lock()
	defer app.mu.Unlock()
	delete(app.loginRateLimits, key)
}

func loginFailureKey(r *http.Request, input map[string]any) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || host == "" {
		host = r.RemoteAddr
	}
	email := strings.ToLower(strings.TrimSpace(stringField(input, "email", "")))
	return email + "|" + host
}

func (app *controlPlaneServer) createSession(user map[string]any, bearer string) (map[string]any, string, error) {
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
	if err := app.tables.SaveSession(context.Background(), map[string]any{"id": sessionKey, "userId": stringValue(user["id"]), "csrf": csrf, "expiresAt": expiresAt.Format(time.RFC3339)}); err != nil {
		app.sessionCredentials.Delete(sessionKey)
		return nil, "", err
	}
	return map[string]any{"user": sanitizeUser(user), "isOperator": isOperatorUser(user), "csrfToken": csrf, "expiresAt": expiresAt.Format(time.RFC3339)}, sessionID, nil
}

type sessionAuthenticationState uint8

const (
	sessionNotAuthenticated sessionAuthenticationState = iota
	sessionAuthenticated
	sessionReauthenticationRequired
)

func (app *controlPlaneServer) session(r *http.Request) (map[string]any, sessionAuthenticationState) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return nil, sessionNotAuthenticated
	}
	sessionKey := sessionLookupKey(cookie.Value)
	sessions, err := app.tables.ListSessions(r.Context())
	if err != nil {
		return nil, sessionNotAuthenticated
	}
	session, ok := sessions[sessionKey]
	expiresAt, parseErr := time.Parse(time.RFC3339, stringValue(session["expiresAt"]))
	if !ok || parseErr != nil || !expiresAt.After(time.Now().UTC()) {
		app.invalidateSession(r.Context(), sessionKey)
		return nil, sessionReauthenticationRequired
	}
	if _, ok := app.sessionCredentials.Get(sessionKey); !ok {
		app.invalidateSession(r.Context(), sessionKey)
		return nil, sessionReauthenticationRequired
	}
	user, err := app.findUserByID(r.Context(), stringValue(session["userId"]))
	if err != nil {
		return nil, sessionNotAuthenticated
	}
	if user == nil || stringValue(user["status"]) != "active" || !validRole(stringValue(user["role"])) {
		app.invalidateSession(r.Context(), sessionKey)
		return nil, sessionReauthenticationRequired
	}
	if !isOperatorUser(user) {
		active, err := app.hasActiveCustomerMembership(r.Context(), user)
		if err != nil {
			return nil, sessionNotAuthenticated
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
	return stringValue(user["id"]) == "usr-admin" && stringValue(user["accountId"]) == "acct-admin" && stringValue(user["email"]) == "admin@medopl.cn" && stringValue(user["role"]) == "admin" && stringValue(user["status"]) == "active"
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

func (app *controlPlaneServer) hasActiveCustomerMembership(ctx context.Context, user map[string]any) (bool, error) {
	accountID := stringValue(user["accountId"])
	accounts, err := app.tables.ListAccounts(ctx, accountID)
	if err != nil {
		return false, err
	}
	account := findRecord(accounts, accountID)
	if !ownsActiveAccount(account, user) {
		return false, nil
	}
	organizations, err := app.tables.ListOrganizations(ctx)
	if err != nil {
		return false, err
	}
	memberships, err := app.tables.ListMemberships(ctx)
	if err != nil {
		return false, err
	}
	ownedOrganizations := make([]map[string]any, 0, 1)
	for _, organization := range organizations {
		if stringValue(organization["billingAccountId"]) == accountID {
			ownedOrganizations = append(ownedOrganizations, organization)
		}
	}
	ownedMemberships := make([]map[string]any, 0, 1)
	for _, membership := range memberships {
		if stringValue(membership["accountId"]) == accountID || stringValue(membership["userId"]) == stringValue(user["id"]) {
			ownedMemberships = append(ownedMemberships, membership)
		}
	}
	if len(ownedOrganizations) != 1 || len(ownedMemberships) != 1 {
		return false, nil
	}
	organization, membership := ownedOrganizations[0], ownedMemberships[0]
	return stringValue(organization["status"]) == "active" && stringValue(membership["status"]) == "active" &&
		stringValue(membership["organizationId"]) == stringValue(organization["id"]) && stringValue(membership["accountId"]) == accountID &&
		stringValue(membership["userId"]) == stringValue(user["id"]) && stringValue(membership["role"]) == "owner", nil
}

func (app *controlPlaneServer) sessionUserID(r *http.Request) string {
	user, ok := app.sessionUserContext(r)
	if !ok {
		return ""
	}
	return stringValue(user["id"])
}

func (app *controlPlaneServer) sessionUserContext(r *http.Request) (map[string]any, bool) {
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
	sessions, err := app.tables.ListSessions(ctx)
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

func (app *controlPlaneServer) sessionFactsLocked() controlPlaneRecordSet {
	sessions, err := app.tables.ListSessions(context.Background())
	if err != nil {
		return controlPlaneRecordSet{}
	}
	return sessions
}

func (app *controlPlaneServer) findUserByID(ctx context.Context, id string) (map[string]any, error) {
	users, err := app.tables.ListUsers(ctx, true)
	if err != nil {
		return nil, err
	}
	for _, user := range users {
		if stringValue(user["id"]) == id {
			return cloneMap(user), nil
		}
	}
	return nil, nil
}
