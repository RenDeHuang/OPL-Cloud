package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"
)

func (app *controlPlaneServer) createUser(input map[string]any) (map[string]any, error) {
	role := stringField(input, "role", "owner")
	if !validRole(role) {
		return nil, errInvalidRole
	}
	email := stringField(input, "email", "admin@medopl.cn")
	users, err := app.tables.ListUsers(context.Background(), true)
	if err != nil {
		return nil, err
	}
	for _, existing := range users {
		if strings.EqualFold(stringValue(existing["email"]), email) {
			return nil, errUserExists
		}
	}
	id := "usr-" + compactID(email+"-"+time.Now().UTC().Format("20060102150405.000000000"))
	password := stringField(input, "password", "")
	if password == "" {
		return nil, errors.New("missing_password")
	}
	passwordHash, err := hashPassword(password)
	if err != nil {
		return nil, err
	}
	accountID := stringField(input, "accountId", "acct-admin")
	if err := app.ensureAccount(context.Background(), accountID); err != nil {
		return nil, err
	}
	user := map[string]any{"id": id, "email": email, "accountId": accountID, "role": role, "status": "active", "passwordHash": passwordHash}
	return sanitizeUser(user), app.tables.SaveUser(context.Background(), user)
}

func (app *controlPlaneServer) ensureAccount(ctx context.Context, accountID string) error {
	accounts, err := app.tables.ListAccounts(ctx)
	if err != nil {
		return err
	}
	for _, account := range accounts {
		if stringValue(account["id"]) == accountID {
			return nil
		}
	}
	return app.tables.SaveAccount(ctx, map[string]any{"id": accountID, "status": "active"})
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
	if stringValue(user["status"]) == "deleted" {
		return nil, errUserDeleted
	}
	if isOperatorUser(user) && stringValue(user["status"]) == "active" {
		return nil, errLastActiveAdmin
	}
	user["status"] = "disabled"
	user["disabledAt"] = time.Now().UTC().Format(time.RFC3339)
	user["disabledBy"] = firstNonEmpty(stringField(input, "operatorUserId", ""), stringField(input, "disabledBy", ""), "usr-admin")
	user["disabledReason"] = stringField(input, "reason", "admin_disabled")
	if err := app.revokeUserSessions(id); err != nil {
		return nil, err
	}
	return sanitizeUser(user), app.tables.SaveUser(context.Background(), user)
}

func (app *controlPlaneServer) softDeleteUser(input map[string]any) (map[string]any, error) {
	id := stringField(input, "userId", "")
	user, err := app.findUserByID(context.Background(), id)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, errUserNotFound
	}
	if stringValue(user["status"]) == "deleted" {
		return sanitizeUser(user), nil
	}
	if isOperatorUser(user) && stringValue(user["status"]) == "active" {
		return nil, errLastActiveAdmin
	}
	user["status"] = "deleted"
	user["deletedAt"] = time.Now().UTC().Format(time.RFC3339)
	user["deletedBy"] = firstNonEmpty(stringField(input, "operatorUserId", ""), stringField(input, "deletedBy", ""), "usr-admin")
	user["deleteReason"] = stringField(input, "reason", "admin_deleted")
	if err := app.revokeUserSessions(id); err != nil {
		return nil, err
	}
	return sanitizeUser(user), app.tables.SaveUser(context.Background(), user)
}

func (app *controlPlaneServer) revokeUserSessions(userID string) error {
	sessions, err := app.tables.ListSessions(context.Background())
	if err != nil {
		return err
	}
	for sessionID, session := range sessions {
		if stringValue(session["userId"]) == userID {
			if err := app.tables.DeleteSession(context.Background(), sessionID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (app *controlPlaneServer) importBootstrapUsers() error {
	users, err := bootstrapUsersFromEnv()
	if err != nil {
		return err
	}
	if err := app.dropLegacyOwnerUser(); err != nil {
		return err
	}
	for _, user := range users {
		if err := app.upsertBootstrapUser(user); err != nil {
			return err
		}
	}
	return nil
}

func (app *controlPlaneServer) dropLegacyOwnerUser() error {
	users, err := app.tables.ListUsers(context.Background(), true)
	if err != nil {
		return err
	}
	for _, user := range users {
		if strings.EqualFold(stringValue(user["email"]), "owner@example.com") {
			if err := app.tables.DeleteUser(context.Background(), stringValue(user["id"])); err != nil {
				return err
			}
		}
	}
	return nil
}

func (app *controlPlaneServer) upsertBootstrapUser(seed map[string]any) error {
	if err := app.ensureAccount(context.Background(), stringValue(seed["accountId"])); err != nil {
		return err
	}
	id := stringValue(seed["id"])
	users, err := app.tables.ListUsers(context.Background(), true)
	if err != nil {
		return err
	}
	for _, existing := range users {
		if stringValue(existing["id"]) == id || strings.EqualFold(stringValue(existing["email"]), stringValue(seed["email"])) {
			for key, value := range seed {
				if key == "passwordHash" && stringValue(existing["passwordHash"]) != "" {
					continue
				}
				if key == "id" {
					continue
				}
				existing[key] = value
			}
			return app.tables.SaveUser(context.Background(), existing)
		}
	}
	return app.tables.SaveUser(context.Background(), seed)
}

func (app *controlPlaneServer) login(input map[string]any) (map[string]any, string, error) {
	email := strings.ToLower(strings.TrimSpace(stringField(input, "email", "")))
	password := stringField(input, "password", "")
	users, err := app.tables.ListUsers(context.Background(), false)
	if err != nil {
		return nil, "", err
	}
	for _, user := range users {
		if strings.ToLower(stringValue(user["email"])) != email {
			continue
		}
		if stringValue(user["status"]) != "active" || !verifyPassword(password, stringValue(user["passwordHash"])) {
			return nil, "", errors.New("invalid_credentials")
		}
		return app.createSession(user)
	}
	return nil, "", errors.New("invalid_credentials")
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

func (app *controlPlaneServer) operatorLogin() (map[string]any, string, error) {
	users, err := app.tables.ListUsers(context.Background(), false)
	if err != nil {
		return nil, "", err
	}
	for _, user := range users {
		if stringValue(user["id"]) == "usr-operator" && stringValue(user["accountId"]) == "acct-operator" && stringValue(user["status"]) == "active" {
			return app.createSession(user)
		}
	}
	if err := app.ensureAccount(context.Background(), "acct-operator"); err != nil {
		return nil, "", err
	}
	operator := map[string]any{"id": "usr-operator", "email": "operator@opl.local", "accountId": "acct-operator", "role": "admin", "status": "active"}
	if err := app.tables.SaveUser(context.Background(), operator); err != nil {
		return nil, "", err
	}
	return app.createSession(operator)
}

func (app *controlPlaneServer) createSession(user map[string]any) (map[string]any, string, error) {
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
	if err := app.tables.SaveSession(context.Background(), map[string]any{"id": sessionKey, "userId": stringValue(user["id"]), "csrf": csrf, "expiresAt": expiresAt.Format(time.RFC3339)}); err != nil {
		return nil, "", err
	}
	return map[string]any{"user": sanitizeUser(user), "csrfToken": csrf, "expiresAt": expiresAt.Format(time.RFC3339)}, sessionID, nil
}

func (app *controlPlaneServer) session(r *http.Request) (map[string]any, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return nil, false
	}
	sessionKey := sessionLookupKey(cookie.Value)
	sessions, err := app.tables.ListSessions(r.Context())
	if err != nil {
		return nil, false
	}
	session, ok := sessions[sessionKey]
	expiresAt, _ := time.Parse(time.RFC3339, stringValue(session["expiresAt"]))
	if !ok || time.Now().UTC().After(expiresAt) {
		_ = app.tables.DeleteSession(r.Context(), sessionKey)
		return nil, false
	}
	user, err := app.findUserByID(r.Context(), stringValue(session["userId"]))
	if err != nil {
		return nil, false
	}
	if user == nil || stringValue(user["status"]) != "active" || !validRole(stringValue(user["role"])) {
		return nil, false
	}
	if !isOperatorUser(user) {
		active, err := app.hasActiveCustomerMembership(r.Context(), user)
		if err != nil || !active {
			return nil, false
		}
	}
	return map[string]any{"user": sanitizeUser(user), "csrfToken": stringValue(session["csrf"]), "expiresAt": expiresAt.Format(time.RFC3339)}, true
}

func isOperatorUser(user map[string]any) bool {
	id, accountID := stringValue(user["id"]), stringValue(user["accountId"])
	return stringValue(user["role"]) == "admin" && id == "usr-operator" && accountID == "acct-operator"
}

func (app *controlPlaneServer) hasActiveCustomerMembership(ctx context.Context, user map[string]any) (bool, error) {
	accountID := stringValue(user["accountId"])
	accounts, err := app.tables.ListAccounts(ctx)
	if err != nil {
		return false, err
	}
	account := findRecord(accounts, accountID)
	if account == nil || stringValue(account["status"]) != "active" {
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
	for _, membership := range memberships {
		organization := findRecord(organizations, stringValue(membership["organizationId"]))
		if stringValue(membership["userId"]) == stringValue(user["id"]) && stringValue(membership["accountId"]) == accountID && stringValue(membership["status"]) == "active" && validRole(stringValue(membership["role"])) && organization != nil && stringValue(organization["status"]) == "active" && stringValue(organization["billingAccountId"]) == accountID {
			return true, nil
		}
	}
	return false, nil
}

func (app *controlPlaneServer) sessionUserID(r *http.Request) string {
	user, ok := app.sessionUserContext(r)
	if !ok {
		return ""
	}
	return stringValue(user["id"])
}

func (app *controlPlaneServer) sessionUserContext(r *http.Request) (map[string]any, bool) {
	payload, ok := app.session(r)
	if !ok {
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
	return app.tables.DeleteSession(r.Context(), sessionLookupKey(cookie.Value))
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
