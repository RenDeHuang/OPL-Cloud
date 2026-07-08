package server

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/domain"
)

type runtimeApp struct {
	mu          sync.Mutex
	store       ReadModelStore
	computes    map[string]map[string]any
	storages    map[string]map[string]any
	attachments map[string]map[string]any
	workspaces  map[string]map[string]any
	users       map[string]map[string]any
	orgs        map[string]map[string]any
	memberships map[string]map[string]any
	support     map[string]map[string]any
	wallets     map[string]map[string]any
	ledger      []map[string]any
	walletTx    []map[string]any
	topups      []map[string]any
	runtimeOps  []map[string]any
	reconcile   map[string]any
	sessions    map[string]sessionRecord
	// ponytail: per-process limiter; move to Redis when login traffic spans multiple replicas.
	loginFailures map[string]loginFailure
}

type loginFailure struct {
	Count   int
	FirstAt time.Time
}

var (
	errUserNotFound    = errors.New("user_not_found")
	errLastActiveAdmin = errors.New("last_active_admin")
	errUserDeleted     = errors.New("user_deleted")
)

func newRuntimeApp() *runtimeApp {
	return newRuntimeAppEmpty()
}

func newRuntimeAppWithStore(store ReadModelStore) (*runtimeApp, error) {
	app := newRuntimeAppEmpty()
	app.store = store
	if store != nil {
		snapshot, err := store.Load(context.Background())
		if err != nil {
			return nil, err
		}
		app.applySnapshot(snapshot)
	}
	if err := app.importBootstrapUsers(); err != nil {
		return nil, err
	}
	return app, nil
}

func newRuntimeAppEmpty() *runtimeApp {
	return &runtimeApp{
		computes:      map[string]map[string]any{},
		storages:      map[string]map[string]any{},
		attachments:   map[string]map[string]any{},
		workspaces:    map[string]map[string]any{},
		users:         map[string]map[string]any{"usr-admin": {"id": "usr-admin", "email": "owner@example.com", "accountId": "acct-admin", "role": "admin", "status": "active"}},
		orgs:          map[string]map[string]any{},
		memberships:   map[string]map[string]any{},
		support:       map[string]map[string]any{},
		wallets:       map[string]map[string]any{},
		sessions:      map[string]sessionRecord{},
		loginFailures: map[string]loginFailure{},
	}
}

func (app *runtimeApp) snapshotLocked() readModelSnapshot {
	return readModelSnapshot{
		Version:     1,
		Computes:    cloneMapMap(app.computes),
		Storages:    cloneMapMap(app.storages),
		Attachments: cloneMapMap(app.attachments),
		Workspaces:  cloneMapMap(app.workspaces),
		Users:       cloneMapMap(app.users),
		Orgs:        cloneMapMap(app.orgs),
		Memberships: cloneMapMap(app.memberships),
		Support:     cloneMapMap(app.support),
		Wallets:     cloneMapMap(app.wallets),
		Ledger:      cloneMapSlice(app.ledger),
		WalletTx:    cloneMapSlice(app.walletTx),
		Topups:      cloneMapSlice(app.topups),
		RuntimeOps:  cloneMapSlice(app.runtimeOps),
		Reconcile:   cloneMap(app.reconcile),
	}
}

func (app *runtimeApp) applySnapshot(snapshot readModelSnapshot) {
	if len(snapshot.Computes) > 0 {
		app.computes = cloneMapMap(snapshot.Computes)
	}
	if len(snapshot.Storages) > 0 {
		app.storages = cloneMapMap(snapshot.Storages)
	}
	if len(snapshot.Attachments) > 0 {
		app.attachments = cloneMapMap(snapshot.Attachments)
	}
	if len(snapshot.Workspaces) > 0 {
		app.workspaces = cloneMapMap(snapshot.Workspaces)
	}
	if len(snapshot.Users) > 0 {
		app.users = cloneMapMap(snapshot.Users)
	}
	if len(snapshot.Orgs) > 0 {
		app.orgs = cloneMapMap(snapshot.Orgs)
	}
	if len(snapshot.Memberships) > 0 {
		app.memberships = cloneMapMap(snapshot.Memberships)
	}
	if len(snapshot.Support) > 0 {
		app.support = cloneMapMap(snapshot.Support)
	}
	if len(snapshot.Wallets) > 0 {
		app.wallets = cloneMapMap(snapshot.Wallets)
	}
	if snapshot.Ledger != nil {
		app.ledger = cloneMapSlice(snapshot.Ledger)
	}
	if snapshot.WalletTx != nil {
		app.walletTx = cloneMapSlice(snapshot.WalletTx)
	}
	if snapshot.Topups != nil {
		app.topups = cloneMapSlice(snapshot.Topups)
	}
	if snapshot.RuntimeOps != nil {
		app.runtimeOps = cloneMapSlice(snapshot.RuntimeOps)
	}
	if snapshot.Reconcile != nil {
		app.reconcile = cloneMap(snapshot.Reconcile)
	}
}

func (app *runtimeApp) persistLocked() error {
	if app.store == nil {
		return nil
	}
	return app.store.Save(context.Background(), app.snapshotLocked())
}

func (app *runtimeApp) state(accountID string) map[string]any {
	app.mu.Lock()
	defer app.mu.Unlock()
	return map[string]any{
		"product":               map[string]any{"name": "OPL Cloud", "console": "OPL Console", "workspace": "OPL Workspace"},
		"billingPolicy":         map[string]any{"holdDays": 7, "priceBasis": "OPL price list"},
		"packages":              packageList(),
		"computePools":          computePools(),
		"wallet":                app.wallet(accountID),
		"account":               app.wallet(accountID),
		"user":                  app.currentUserLocked(),
		"workspaces":            values(app.workspaces),
		"computeAllocations":    values(app.computes),
		"storageVolumes":        values(app.storages),
		"storageAttachments":    values(app.attachments),
		"accounts":              app.accountsLocked(),
		"billingLedger":         copySlice(app.ledger),
		"walletTransactions":    copySlice(app.walletTx),
		"manualTopups":          copySlice(app.topups),
		"supportTickets":        values(app.support),
		"billingReconciliation": app.reconciliationProjectionLocked(),
		"notifications":         []any{},
		"runtimeOperations":     copySlice(app.runtimeOps),
		"generatedAt":           time.Now().UTC().Format(time.RFC3339),
	}
}

func (app *runtimeApp) currentUserLocked() map[string]any {
	if user, ok := app.users["usr-admin"]; ok {
		return sanitizeUser(user)
	}
	return nil
}

func (app *runtimeApp) createOrganization(input map[string]any) (map[string]any, error) {
	app.mu.Lock()
	defer app.mu.Unlock()
	name := stringField(input, "name", "Organization")
	id := "org-" + compactID(name+"-"+time.Now().UTC().Format("20060102150405.000000000"))
	org := map[string]any{"id": id, "name": name, "billingAccountId": stringField(input, "billingAccountId", "acct-admin"), "status": "active"}
	app.orgs[id] = org
	return cloneMap(org), app.persistLocked()
}

func (app *runtimeApp) createMembership(input map[string]any) (map[string]any, error) {
	app.mu.Lock()
	defer app.mu.Unlock()
	orgID := stringField(input, "organizationId", "")
	userID := stringField(input, "userId", "")
	id := "mem-" + stableID(orgID, userID, time.Now().UTC().String())[:12]
	membership := map[string]any{"id": id, "organizationId": orgID, "userId": userID, "role": stringField(input, "role", "member"), "status": "active"}
	app.memberships[id] = membership
	return cloneMap(membership), app.persistLocked()
}

func (app *runtimeApp) supportTickets(scopeAll bool, accountID string) []any {
	app.mu.Lock()
	defer app.mu.Unlock()
	if scopeAll || accountID == "" {
		return values(app.support)
	}
	return filteredValues(app.support, func(item map[string]any) bool {
		return stringValue(item["accountId"]) == accountID
	})
}

func (app *runtimeApp) createSupportMapping(input map[string]any) (map[string]any, error) {
	app.mu.Lock()
	defer app.mu.Unlock()
	externalTicketID := stringField(input, "externalTicketId", "")
	if externalTicketID == "" {
		return nil, errors.New("missing_external_ticket_id")
	}
	accountID := stringField(input, "accountId", "")
	if accountID == "" {
		return nil, errors.New("missing_account_id")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	id := "support-" + stableID(accountID, externalTicketID)[:12]
	message := strings.TrimSpace(stringField(input, "description", ""))
	row := map[string]any{
		"id":               id,
		"externalSystem":   stringField(input, "externalSystem", "external-helpdesk"),
		"externalTicketId": externalTicketID,
		"externalUrl":      stringField(input, "externalUrl", ""),
		"accountId":        accountID,
		"userId":           stringField(input, "userId", ""),
		"workspaceId":      stringField(input, "workspaceId", ""),
		"resourceIds":      stringSliceField(input, "resourceIds"),
		"operationId":      stringField(input, "operationId", ""),
		"title":            stringField(input, "title", externalTicketID),
		"category":         stringField(input, "category", "Workspace"),
		"priority":         stringField(input, "priority", "normal"),
		"status":           stringField(input, "status", "external_open"),
		"createdAt":        now,
		"updatedAt":        now,
		"messages":         []any{},
	}
	if message != "" {
		row["messages"] = []any{map[string]any{"author": "requester", "text": message, "createdAt": now}}
	}
	app.support[id] = row
	return cloneMap(row), app.persistLocked()
}

func (app *runtimeApp) createUser(input map[string]any) (map[string]any, error) {
	app.mu.Lock()
	defer app.mu.Unlock()
	email := stringField(input, "email", "owner@example.com")
	id := "usr-" + compactID(email+"-"+time.Now().UTC().Format("20060102150405.000000000"))
	password := stringField(input, "password", "")
	if password == "" {
		return nil, errors.New("missing_password")
	}
	passwordHash, err := hashPassword(password)
	if err != nil {
		return nil, err
	}
	user := map[string]any{"id": id, "email": email, "accountId": stringField(input, "accountId", "acct-admin"), "role": stringField(input, "role", "owner"), "status": "active", "passwordHash": passwordHash}
	app.users[id] = user
	return sanitizeUser(user), app.persistLocked()
}

func (app *runtimeApp) disableUser(input map[string]any) (map[string]any, error) {
	app.mu.Lock()
	defer app.mu.Unlock()
	id := stringField(input, "userId", "")
	user := app.users[id]
	if user == nil {
		return nil, errUserNotFound
	}
	if stringValue(user["status"]) == "deleted" {
		return nil, errUserDeleted
	}
	if stringValue(user["role"]) == "admin" && stringValue(user["status"]) == "active" && app.activeAdminCountLocked() <= 1 {
		return nil, errLastActiveAdmin
	}
	user["status"] = "disabled"
	user["disabledAt"] = time.Now().UTC().Format(time.RFC3339)
	user["disabledBy"] = firstNonEmpty(stringField(input, "operatorUserId", ""), stringField(input, "disabledBy", ""), "usr-admin")
	user["disabledReason"] = stringField(input, "reason", "admin_disabled")
	app.revokeUserSessionsLocked(id)
	return sanitizeUser(user), app.persistLocked()
}

func (app *runtimeApp) softDeleteUser(input map[string]any) (map[string]any, error) {
	app.mu.Lock()
	defer app.mu.Unlock()
	id := stringField(input, "userId", "")
	user := app.users[id]
	if user == nil {
		return nil, errUserNotFound
	}
	if stringValue(user["status"]) == "deleted" {
		return sanitizeUser(user), nil
	}
	if stringValue(user["role"]) == "admin" && stringValue(user["status"]) == "active" && app.activeAdminCountLocked() <= 1 {
		return nil, errLastActiveAdmin
	}
	user["status"] = "deleted"
	user["deletedAt"] = time.Now().UTC().Format(time.RFC3339)
	user["deletedBy"] = firstNonEmpty(stringField(input, "operatorUserId", ""), stringField(input, "deletedBy", ""), "usr-admin")
	user["deleteReason"] = stringField(input, "reason", "admin_deleted")
	app.revokeUserSessionsLocked(id)
	return sanitizeUser(user), app.persistLocked()
}

func (app *runtimeApp) activeAdminCountLocked() int {
	count := 0
	for _, user := range app.users {
		if stringValue(user["role"]) == "admin" && stringValue(user["status"]) == "active" {
			count++
		}
	}
	return count
}

func (app *runtimeApp) revokeUserSessionsLocked(userID string) {
	for sessionID, session := range app.sessions {
		if session.UserID == userID {
			delete(app.sessions, sessionID)
		}
	}
}

func (app *runtimeApp) importBootstrapUsers() error {
	users, err := bootstrapUsersFromEnv()
	if err != nil {
		return err
	}
	if len(users) == 0 {
		return nil
	}
	app.mu.Lock()
	defer app.mu.Unlock()
	if len(app.users) > 0 {
		return nil
	}
	for _, user := range users {
		app.users[stringValue(user["id"])] = user
	}
	return app.persistLocked()
}

func (app *runtimeApp) login(input map[string]any) (map[string]any, string, error) {
	email := strings.ToLower(strings.TrimSpace(stringField(input, "email", "")))
	password := stringField(input, "password", "")
	app.mu.Lock()
	defer app.mu.Unlock()
	for _, user := range app.users {
		if strings.ToLower(stringValue(user["email"])) != email {
			continue
		}
		if stringValue(user["status"]) != "active" || !verifyPassword(password, stringValue(user["passwordHash"])) {
			return nil, "", errors.New("invalid_credentials")
		}
		return app.createSessionLocked(user)
	}
	return nil, "", errors.New("invalid_credentials")
}

func (app *runtimeApp) loginRateLimited(r *http.Request, input map[string]any) bool {
	key := loginFailureKey(r, input)
	app.mu.Lock()
	defer app.mu.Unlock()
	failure := app.loginFailures[key]
	if !failure.FirstAt.IsZero() && time.Since(failure.FirstAt) > 15*time.Minute {
		delete(app.loginFailures, key)
		return false
	}
	return failure.Count >= 5
}

func (app *runtimeApp) recordLoginFailure(r *http.Request, input map[string]any) {
	key := loginFailureKey(r, input)
	app.mu.Lock()
	defer app.mu.Unlock()
	failure := app.loginFailures[key]
	if failure.FirstAt.IsZero() || time.Since(failure.FirstAt) > 15*time.Minute {
		failure = loginFailure{FirstAt: time.Now().UTC()}
	}
	failure.Count++
	app.loginFailures[key] = failure
}

func (app *runtimeApp) clearLoginFailures(r *http.Request, input map[string]any) {
	key := loginFailureKey(r, input)
	app.mu.Lock()
	defer app.mu.Unlock()
	delete(app.loginFailures, key)
}

func loginFailureKey(r *http.Request, input map[string]any) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || host == "" {
		host = r.RemoteAddr
	}
	email := strings.ToLower(strings.TrimSpace(stringField(input, "email", "")))
	return email + "|" + host
}

func (app *runtimeApp) operatorLogin() (map[string]any, string, error) {
	app.mu.Lock()
	defer app.mu.Unlock()
	for _, user := range app.users {
		if stringValue(user["role"]) == "admin" && stringValue(user["status"]) == "active" {
			return app.createSessionLocked(user)
		}
	}
	operator := map[string]any{"id": "usr-operator", "email": "operator@opl.local", "accountId": "acct-operator", "role": "admin", "status": "active"}
	return app.createSessionLocked(operator)
}

func (app *runtimeApp) createSessionLocked(user map[string]any) (map[string]any, string, error) {
	sessionID, err := randomToken(32)
	if err != nil {
		return nil, "", err
	}
	csrf, err := randomToken(24)
	if err != nil {
		return nil, "", err
	}
	app.sessions[sessionID] = sessionRecord{ID: sessionID, UserID: stringValue(user["id"]), CSRF: csrf, ExpiresAt: time.Now().UTC().Add(12 * time.Hour)}
	return map[string]any{"user": sanitizeUser(user), "csrfToken": csrf, "expiresAt": app.sessions[sessionID].ExpiresAt.Format(time.RFC3339)}, sessionID, nil
}

func (app *runtimeApp) session(r *http.Request) (map[string]any, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return nil, false
	}
	app.mu.Lock()
	defer app.mu.Unlock()
	session, ok := app.sessions[cookie.Value]
	if !ok || time.Now().UTC().After(session.ExpiresAt) {
		delete(app.sessions, cookie.Value)
		return nil, false
	}
	user := app.users[session.UserID]
	if user == nil || stringValue(user["status"]) != "active" {
		return nil, false
	}
	return map[string]any{"user": sanitizeUser(user), "csrfToken": session.CSRF, "expiresAt": session.ExpiresAt.Format(time.RFC3339)}, true
}

func (app *runtimeApp) sessionUserID(r *http.Request) string {
	user, ok := app.sessionUserContext(r)
	if !ok {
		return ""
	}
	return stringValue(user["id"])
}

func (app *runtimeApp) sessionUserContext(r *http.Request) (map[string]any, bool) {
	payload, ok := app.session(r)
	if !ok {
		return nil, false
	}
	user, _ := payload["user"].(map[string]any)
	return user, user != nil
}

func (app *runtimeApp) logout(r *http.Request) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return
	}
	app.mu.Lock()
	defer app.mu.Unlock()
	delete(app.sessions, cookie.Value)
}

func (app *runtimeApp) setWorkspaceAccess(workspaceID string, tokenStatus string) (map[string]any, bool, error) {
	app.mu.Lock()
	defer app.mu.Unlock()
	workspace := app.workspaces[workspaceID]
	if workspace == nil {
		return nil, false, nil
	}
	access, _ := workspace["access"].(map[string]any)
	access = cloneMap(access)
	access["tokenStatus"] = tokenStatus
	access["requiresLogin"] = false
	workspace["access"] = access
	return cloneMap(workspace), true, app.persistLocked()
}

func (app *runtimeApp) rememberWorkspaceProjection(workspace domain.WorkspaceProjection) error {
	app.mu.Lock()
	defer app.mu.Unlock()

	app.workspaces[workspace.ID] = map[string]any{
		"id":                         workspace.ID,
		"ownerAccountId":             workspace.AccountID,
		"ownerUserId":                workspace.OwnerID,
		"accountId":                  workspace.AccountID,
		"name":                       workspace.Name,
		"packageId":                  workspace.PackageID,
		"provider":                   workspace.Provider,
		"state":                      workspace.Status,
		"status":                     workspace.Status,
		"url":                        workspace.URL,
		"computeAllocationId":        workspace.ComputeID,
		"currentComputeAllocationId": workspace.ComputeID,
		"storageId":                  workspace.VolumeID,
		"attachmentId":               workspace.AttachmentID,
		"currentAttachmentId":        workspace.AttachmentID,
		"runtimeId":                  workspace.RuntimeID,
		"runtime":                    map[string]any{"serviceName": workspace.RuntimeServiceName},
		"evidenceId":                 workspace.EvidenceID,
		"access":                     map[string]any{"tokenStatus": "active", "requiresLogin": false},
	}
	return app.persistLocked()
}

func (app *runtimeApp) rememberCompute(allocation any) error {
	if row, ok := allocation.(map[string]any); ok {
		app.mu.Lock()
		defer app.mu.Unlock()
		accountID := stringValue(row["accountId"])
		app.computes[stringValue(row["id"])] = row
		if stringValue(row["status"]) == "destroyed" {
			app.rememberReleaseLocked(accountID, "compute", stringValue(row["id"]), row)
			app.suspendWorkspacesForComputeLocked(stringValue(row["id"]))
			return app.persistLocked()
		}
		app.rememberHoldLocked(accountID, "compute", stringValue(row["id"]), row)
		return app.persistLocked()
	}
	return nil
}

func (app *runtimeApp) rememberStorage(volume any) error {
	if row, ok := volume.(map[string]any); ok {
		app.mu.Lock()
		defer app.mu.Unlock()
		accountID := stringValue(row["accountId"])
		app.storages[stringValue(row["id"])] = row
		if stringValue(row["status"]) == "destroyed" {
			app.rememberReleaseLocked(accountID, "storage", stringValue(row["id"]), row)
			app.markWorkspacesStorageDestroyedLocked(stringValue(row["id"]))
			return app.persistLocked()
		}
		app.rememberHoldLocked(accountID, "storage", stringValue(row["id"]), row)
		return app.persistLocked()
	}
	return nil
}

func (app *runtimeApp) rememberHoldLocked(accountID string, resourceType string, resourceID string, row map[string]any) {
	holdID := stringValue(row["holdId"])
	if accountID == "" || holdID == "" {
		return
	}
	if wallet, ok := row["wallet"].(map[string]any); ok {
		app.wallets[accountID] = walletProjection(walletFromMap(wallet))
	}
	ledger := map[string]any{"id": holdID, "accountId": accountID, "type": resourceType + "_hold", "resourceId": resourceID, "amountCents": int64(numberField(row, "holdAmountCents", 0))}
	if resourceType == "storage" {
		ledger["storageId"] = resourceID
	} else {
		ledger["computeAllocationId"] = resourceID
	}
	app.ledger = append(app.ledger, ledger)
}

func (app *runtimeApp) rememberReleaseLocked(accountID string, resourceType string, resourceID string, row map[string]any) {
	releaseID := stringValue(row["holdReleaseId"])
	if accountID == "" || releaseID == "" {
		return
	}
	if wallet, ok := row["wallet"].(map[string]any); ok {
		app.wallets[accountID] = walletProjection(walletFromMap(wallet))
	}
	ledger := map[string]any{"id": releaseID, "accountId": accountID, "type": resourceType + "_hold_released", "resourceId": resourceID, "amountCents": int64(numberField(row, "holdAmountCents", 0))}
	if resourceType == "storage" {
		ledger["storageId"] = resourceID
	} else {
		ledger["computeAllocationId"] = resourceID
	}
	app.ledger = append(app.ledger, ledger)
}

func (app *runtimeApp) rememberAttachment(attachment any, input map[string]any) error {
	if row, ok := attachment.(map[string]any); ok {
		row["computeAllocationId"] = stringField(input, "computeAllocationId", "")
		row["storageId"] = firstNonEmpty(stringValue(row["volumeId"]), stringField(input, "storageId", ""))
		row["mountPath"] = firstNonEmpty(stringValue(row["mountPath"]), stringField(input, "mountPath", "/data"))
		app.mu.Lock()
		defer app.mu.Unlock()
		app.attachments[stringValue(row["id"])] = row
		if stringValue(row["status"]) == "detached" {
			app.clearWorkspacesForAttachmentLocked(stringValue(row["id"]))
		}
		return app.persistLocked()
	}
	return nil
}

func (app *runtimeApp) suspendWorkspacesForComputeLocked(computeID string) {
	for _, workspace := range app.workspaces {
		if stringValue(workspace["currentComputeAllocationId"]) == computeID || stringValue(workspace["computeAllocationId"]) == computeID {
			workspace["currentComputeAllocationId"] = ""
			workspace["computeAllocationId"] = ""
			workspace["state"] = "suspended"
			workspace["status"] = "suspended"
			workspace["access"] = map[string]any{"tokenStatus": "suspended", "requiresLogin": false}
		}
	}
}

func (app *runtimeApp) clearWorkspacesForAttachmentLocked(attachmentID string) {
	for _, workspace := range app.workspaces {
		if stringValue(workspace["currentAttachmentId"]) == attachmentID || stringValue(workspace["attachmentId"]) == attachmentID {
			workspace["currentAttachmentId"] = ""
			workspace["attachmentId"] = ""
			if stringValue(workspace["state"]) != "data_deleted" {
				workspace["state"] = "suspended"
				workspace["status"] = "suspended"
			}
		}
	}
}

func (app *runtimeApp) markWorkspacesStorageDestroyedLocked(storageID string) {
	for _, workspace := range app.workspaces {
		if stringValue(workspace["storageId"]) == storageID {
			workspace["state"] = "data_deleted"
			workspace["status"] = "unrecoverable"
			workspace["currentComputeAllocationId"] = ""
			workspace["computeAllocationId"] = ""
			workspace["currentAttachmentId"] = ""
			workspace["attachmentId"] = ""
			workspace["access"] = map[string]any{"tokenStatus": "disabled", "requiresLogin": false}
		}
	}
}

func (app *runtimeApp) managementState(includeDeleted bool) map[string]any {
	app.mu.Lock()
	defer app.mu.Unlock()
	return map[string]any{
		"organization":           nil,
		"organizations":          values(app.orgs),
		"users":                  sanitizedUserValues(app.users, includeDeleted),
		"memberships":            values(app.memberships),
		"supportTickets":         values(app.support),
		"accounts":               app.accountsLocked(),
		"packages":               packageList(),
		"computePools":           computePools(),
		"workspaces":             values(app.workspaces),
		"computeAllocations":     values(app.computes),
		"storageVolumes":         values(app.storages),
		"storageAttachments":     values(app.attachments),
		"resourceLedgerEvidence": app.resourceLedgerEvidenceLocked(),
		"billingLedger":          copySlice(app.ledger),
		"walletTransactions":     copySlice(app.walletTx),
		"manualTopups":           copySlice(app.topups),
		"runtimeOperations":      copySlice(app.runtimeOps),
		"billingReconciliation":  app.reconciliationProjectionLocked(),
	}
}

func (app *runtimeApp) operatorSummary() map[string]any {
	app.mu.Lock()
	defer app.mu.Unlock()
	running := countStatus(app.computes, "running")
	accounts := app.accountsLocked()
	return map[string]any{
		"product":                "OPL Console",
		"generatedAt":            time.Now().UTC().Format(time.RFC3339),
		"accountScope":           "all",
		"accounts":               map[string]any{"total": len(accounts), "frozen": totalAccountField(accounts, "frozen"), "balance": totalAccountField(accounts, "balance"), "totalSpent": totalDebits(app.walletTx, app.ledger)},
		"workspaces":             map[string]any{"total": len(app.workspaces), "running": countStatus(app.workspaces, "running"), "urlActive": countActiveURLs(app.workspaces), "destroyed": countStatus(app.workspaces, "destroyed"), "needsAttention": 0},
		"computeAllocations":     map[string]any{"total": len(app.computes), "running": running, "failed": countStatus(app.computes, "failed")},
		"notifications":          map[string]any{"total": 0, "error": 0, "warning": 0, "recent": []any{}},
		"runtimeOperations":      app.runtimeOperationSummaryLocked(),
		"failedOperations":       failedRuntimeOperations(app.runtimeOps),
		"resourceAnomalies":      []any{},
		"resourceLedgerEvidence": map[string]any{"total": len(app.ledger), "recent": copySlice(app.ledger)},
		"productionE2E":          map[string]any{},
		"billingReconciliation":  app.reconciliationProjectionLocked(),
	}
}

func (app *runtimeApp) reconciliationProjectionLocked() map[string]any {
	if app.reconcile == nil {
		return map[string]any{"reports": 0, "guard": map[string]any{"status": "not_required", "blockNewWorkspaces": false, "reason": "billing_reconciliation_not_required"}}
	}
	row := cloneMap(app.reconcile)
	row["reports"] = 1
	return row
}

func (app *runtimeApp) reconciliationBlocksNewWorkspaces() (map[string]any, bool) {
	app.mu.Lock()
	defer app.mu.Unlock()
	projection := app.reconciliationProjectionLocked()
	guard, _ := projection["guard"].(map[string]any)
	if guard == nil {
		return projection, false
	}
	blocked, _ := guard["blockNewWorkspaces"].(bool)
	return projection, blocked
}

func (app *runtimeApp) rememberRuntimeOperations(operations []clients.FabricOperation) error {
	app.mu.Lock()
	defer app.mu.Unlock()
	rows := make([]map[string]any, 0, len(operations))
	for _, operation := range operations {
		rows = append(rows, structToMap(operation))
	}
	app.runtimeOps = rows
	return app.persistLocked()
}

func (app *runtimeApp) rememberReconciliation(result clients.ReconciliationResult) error {
	app.mu.Lock()
	defer app.mu.Unlock()
	app.reconcile = reconciliationResponse(result)
	return app.persistLocked()
}

func (app *runtimeApp) runtimeOperationSummaryLocked() map[string]any {
	failed := failedRuntimeOperations(app.runtimeOps)
	return map[string]any{"total": len(app.runtimeOps), "failed": len(failed), "recentFailed": failed}
}

func failedRuntimeOperations(operations []map[string]any) []any {
	failed := make([]any, 0)
	for i := len(operations) - 1; i >= 0 && len(failed) < 10; i-- {
		if stringValue(operations[i]["status"]) == "failed" {
			failed = append(failed, cloneMap(operations[i]))
		}
	}
	return failed
}

func (app *runtimeApp) accountsLocked() []any {
	accountIDs := map[string]bool{}
	for accountID := range app.wallets {
		accountIDs[accountID] = true
	}
	for _, user := range app.users {
		if accountID := stringValue(user["accountId"]); accountID != "" {
			accountIDs[accountID] = true
		}
	}
	for _, workspace := range app.workspaces {
		if accountID := firstNonEmpty(stringValue(workspace["ownerAccountId"]), stringValue(workspace["accountId"])); accountID != "" {
			accountIDs[accountID] = true
		}
	}
	keys := make([]string, 0, len(accountIDs))
	for accountID := range accountIDs {
		keys = append(keys, accountID)
	}
	sort.Strings(keys)
	rows := make([]any, 0, len(keys))
	for _, accountID := range keys {
		row := app.wallet(accountID)
		row["totalRecharged"] = totalTopupsForAccount(app.topups, accountID)
		row["totalSpent"] = totalDebitsForAccount(accountID, app.walletTx, app.ledger)
		for _, user := range app.users {
			if stringValue(user["accountId"]) == accountID {
				row["userId"] = firstNonEmpty(stringValue(row["userId"]), stringValue(user["id"]))
				row["email"] = firstNonEmpty(stringValue(row["email"]), stringValue(user["email"]))
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func (app *runtimeApp) getCompute(id string) (map[string]any, bool) {
	app.mu.Lock()
	defer app.mu.Unlock()
	compute, ok := app.computes[id]
	return cloneMap(compute), ok
}

func (app *runtimeApp) getStorage(id string) (map[string]any, bool) {
	app.mu.Lock()
	defer app.mu.Unlock()
	storage, ok := app.storages[id]
	return cloneMap(storage), ok
}

func (app *runtimeApp) getAttachment(id string) (map[string]any, bool) {
	app.mu.Lock()
	defer app.mu.Unlock()
	attachment, ok := app.attachments[id]
	return cloneMap(attachment), ok
}

func (app *runtimeApp) cleanupWorkspaceAccess(input map[string]any) (map[string]any, error) {
	app.mu.Lock()
	defer app.mu.Unlock()
	requested := stringSet(stringSliceField(input, "workspaceIds"))
	cleaned := []any{}
	skipped := []any{}
	for id, workspace := range app.workspaces {
		if len(requested) > 0 && !requested[id] {
			continue
		}
		if nested(workspace, "access", "tokenStatus") != "active" {
			skipped = append(skipped, map[string]any{"id": id, "reason": "url_not_active"})
			continue
		}
		reason := app.workspaceCleanupReasonLocked(workspace)
		if reason == "" && len(requested) == 0 {
			skipped = append(skipped, map[string]any{"id": id, "reason": "resource_chain_active"})
			continue
		}
		access, _ := workspace["access"].(map[string]any)
		access = cloneMap(access)
		access["tokenStatus"] = "disabled"
		access["requiresLogin"] = false
		workspace["access"] = access
		cleaned = append(cleaned, map[string]any{"id": id, "reason": firstNonEmpty(reason, "operator_requested")})
	}
	if len(cleaned) > 0 {
		if err := app.persistLocked(); err != nil {
			return nil, err
		}
	}
	return map[string]any{"cleaned": cleaned, "skipped": skipped}, nil
}

func (app *runtimeApp) workspaceCleanupReasonLocked(workspace map[string]any) string {
	if stringValue(workspace["ownerAccountId"]) == "" && stringValue(workspace["accountId"]) == "" {
		return "missing_owner"
	}
	storageID := stringValue(workspace["storageId"])
	storage := app.storages[storageID]
	if storageID == "" || storage == nil {
		return "missing_storage"
	}
	if stringValue(storage["status"]) == "destroyed" || stringValue(storage["billingStatus"]) == "stopped" {
		return "storage_destroyed"
	}
	computeID := stringValue(workspace["currentComputeAllocationId"])
	compute := app.computes[computeID]
	if computeID != "" && (compute == nil || stringValue(compute["status"]) == "destroyed") {
		return "compute_unavailable"
	}
	attachmentID := stringValue(workspace["currentAttachmentId"])
	attachment := app.attachments[attachmentID]
	if attachmentID != "" && (attachment == nil || stringValue(attachment["status"]) == "detached") {
		return "attachment_unavailable"
	}
	return ""
}

func (app *runtimeApp) proxyWorkspace(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromPath(r.URL.Path)
	if workspaceID == "" {
		http.NotFound(w, r)
		return
	}
	if token := r.URL.Query().Get("token"); token != "" {
		setWorkspaceGatewayCookies(w, workspaceID, token)
		cleanURL := *r.URL
		query := cleanURL.Query()
		query.Del("token")
		cleanURL.RawQuery = query.Encode()
		http.Redirect(w, r, cleanURL.String(), http.StatusFound)
		return
	}
	suffix := strings.TrimPrefix(r.URL.Path, "/w/"+workspaceID)
	app.proxyWorkspaceTo(w, r, workspaceID, suffix)
}

func (app *runtimeApp) proxyWorkspaceRoot(w http.ResponseWriter, r *http.Request) {
	if !isWorkspaceRequest(r) {
		http.NotFound(w, r)
		return
	}
	workspaceID := workspaceIDFromGatewayRequest(r)
	if workspaceID == "" {
		http.NotFound(w, r)
		return
	}
	app.proxyWorkspaceTo(w, r, workspaceID, r.URL.Path)
}

func (app *runtimeApp) proxyWorkspaceTo(w http.ResponseWriter, r *http.Request, workspaceID string, proxyPath string) {
	app.mu.Lock()
	workspace := cloneMap(app.workspaces[workspaceID])
	app.mu.Unlock()
	if stringValue(workspace["state"]) == "data_deleted" || stringValue(nested(workspace, "access", "tokenStatus")) == "disabled" {
		writeError(w, http.StatusGone, "workspace_storage_destroyed")
		return
	}
	if stringValue(workspace["state"]) == "suspended" || stringValue(nested(workspace, "access", "tokenStatus")) == "suspended" {
		writeError(w, http.StatusConflict, "workspace_suspended")
		return
	}
	serviceName := stringValue(nested(workspace, "runtime", "serviceName"))
	if serviceName == "" {
		http.NotFound(w, r)
		return
	}
	target, err := workspaceServiceTarget(serviceName)
	if err != nil {
		writeUpstreamError(w)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		if proxyPath == "" {
			proxyPath = "/"
		}
		req.URL.Path = proxyPath
		req.URL.RawPath = ""
		req.Host = target.Host
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		writeUpstreamError(w)
	}
	proxy.ServeHTTP(w, r)
}

func workspaceServiceTarget(serviceName string) (*url.URL, error) {
	if strings.HasPrefix(serviceName, "http://") || strings.HasPrefix(serviceName, "https://") {
		return url.Parse(serviceName)
	}
	if strings.Contains(serviceName, ":") {
		return url.Parse("http://" + serviceName)
	}
	return url.Parse("http://" + serviceName + ":3000")
}

func workspaceIDFromPath(path string) string {
	parts := strings.Split(strings.TrimPrefix(path, "/w/"), "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

func workspaceIDFromGatewayRequest(r *http.Request) string {
	if id := workspaceIDFromPath(r.URL.Path); strings.HasPrefix(r.URL.Path, "/w/") && id != "" {
		return id
	}
	if cookie, err := r.Cookie("opl_ws_active"); err == nil && cookie.Value != "" {
		return cookie.Value
	}
	if ref := r.Referer(); ref != "" {
		parsed, err := url.Parse(ref)
		if err == nil && isWorkspaceHost(parsed.Host) {
			return workspaceIDFromPath(parsed.Path)
		}
	}
	return ""
}

func setWorkspaceGatewayCookies(w http.ResponseWriter, workspaceID string, token string) {
	http.SetCookie(w, &http.Cookie{Name: "opl_ws_active", Value: workspaceID, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: "opl_ws_" + workspaceID, Value: token, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
}

func isWorkspaceRequest(r *http.Request) bool {
	return isWorkspaceHost(r.Host)
}

func isWorkspaceHost(host string) bool {
	return strings.Trim(strings.Split(host, ":")[0], " ") == workspaceDomain()
}

func (app *runtimeApp) addLedgerLocked(accountID string, entryType string, ids map[string]any) map[string]any {
	entry := map[string]any{"id": "ledger-" + stableID(accountID, entryType, time.Now().UTC().String())[:12], "accountId": accountID, "type": entryType}
	for key, value := range ids {
		entry[key] = value
	}
	app.ledger = append(app.ledger, entry)
	return entry
}

func (app *runtimeApp) rememberManualTopUp(result clients.ManualTopUpResult) error {
	app.mu.Lock()
	defer app.mu.Unlock()
	app.topups = append(app.topups, structToMap(result.TopUp))
	app.ledger = append(app.ledger, map[string]any{"id": result.LedgerEntry.ID, "accountId": result.LedgerEntry.AccountID, "type": "manual_topup", "amountCents": result.LedgerEntry.AmountCents})
	app.walletTx = append(app.walletTx, map[string]any{"id": result.WalletTransaction.ID, "accountId": result.WalletTransaction.AccountID, "type": "manual_topup", "ledgerEntryId": result.WalletTransaction.LedgerEntryID, "amountCents": result.WalletTransaction.AmountCents})
	app.wallets[result.Wallet.AccountID] = walletProjection(result.Wallet)
	return app.persistLocked()
}

func (app *runtimeApp) rememberResourceSettlement(result clients.ResourceSettlementResult) error {
	app.mu.Lock()
	defer app.mu.Unlock()

	resourceType := firstNonEmpty(result.ResourceType, "compute")
	debitType := resourceType + "_debit"
	ids := map[string]any{"workspaceId": result.WorkspaceID, "resourceId": result.ResourceID}
	switch resourceType {
	case "storage":
		ids["storageId"] = result.ResourceID
	default:
		ids["computeAllocationId"] = result.ResourceID
	}

	ledger := map[string]any{"id": result.LedgerEntryID, "accountId": result.AccountID, "type": debitType, "amountCents": -result.AmountCents}
	for key, value := range ids {
		ledger[key] = value
	}
	app.ledger = append(app.ledger, ledger)

	app.walletTx = append(app.walletTx, map[string]any{"id": result.WalletTransactionID, "accountId": result.AccountID, "type": debitType, "metadata": ids})
	app.wallets[result.AccountID] = walletProjection(result.Wallet)
	return app.persistLocked()
}

func (app *runtimeApp) applyLedgerFacts(accountID string, wallet clients.Wallet, entries []clients.LedgerEntry, transactions []clients.WalletTransaction, topups []clients.ManualTopUp, settlements []clients.ResourceSettlementResult) error {
	app.mu.Lock()
	defer app.mu.Unlock()

	if accountID != "" && wallet.AccountID != "" && (walletHasMoneyFacts(wallet) || app.wallets[wallet.AccountID] == nil) {
		app.wallets[wallet.AccountID] = walletProjection(wallet)
	}
	for _, tx := range transactions {
		if tx.AccountID != "" {
			app.wallets[tx.AccountID] = walletProjection(clients.Wallet{
				AccountID:       tx.AccountID,
				BalanceCents:    tx.BalanceCents,
				FrozenCents:     tx.FrozenCents,
				AvailableCents:  tx.AvailableCents,
				TotalSpentCents: tx.TotalSpentCents,
				Currency:        tx.Currency,
			})
		}
	}

	settlementsByEntry := map[string]clients.ResourceSettlementResult{}
	for _, settlement := range settlements {
		settlementsByEntry[settlement.LedgerEntryID] = settlement
	}
	if entries != nil {
		app.ledger = ledgerEntryProjections(entries, settlementsByEntry)
	}
	if transactions != nil {
		app.walletTx = walletTransactionProjections(transactions)
	}
	if topups != nil {
		app.topups = manualTopUpProjections(topups)
	}
	return app.persistLocked()
}

func ledgerEntryProjections(entries []clients.LedgerEntry, settlements map[string]clients.ResourceSettlementResult) []map[string]any {
	rows := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		row := map[string]any{
			"id":             entry.ID,
			"accountId":      entry.AccountID,
			"type":           ledgerEntryType(entry),
			"amountCents":    ledgerEntryAmount(entry),
			"currency":       entry.Currency,
			"source":         entry.Source,
			"direction":      entry.Direction,
			"operatorUserId": entry.OperatorUserID,
			"reason":         entry.Reason,
			"createdAt":      entry.CreatedAt,
		}
		if settlement, ok := settlements[entry.ID]; ok {
			row["type"] = settlement.ResourceType + "_debit"
			row["amountCents"] = -settlement.AmountCents
			row["workspaceId"] = settlement.WorkspaceID
			row["resourceId"] = settlement.ResourceID
			row["settlementId"] = settlement.ID
			row["pricingVersion"] = settlement.PricingVersion
			row["priceSnapshot"] = settlement.PriceSnapshot
			row["usagePeriodStart"] = settlement.UsagePeriodStart
			row["usagePeriodEnd"] = settlement.UsagePeriodEnd
			row["quantity"] = settlement.Quantity
			row["unit"] = settlement.Unit
			row["providerCostEvidenceRef"] = settlement.ProviderCostEvidenceRef
			if settlement.ResourceType == "storage" {
				row["storageId"] = settlement.ResourceID
			} else {
				row["computeAllocationId"] = settlement.ResourceID
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func walletTransactionProjections(transactions []clients.WalletTransaction) []map[string]any {
	rows := make([]map[string]any, 0, len(transactions))
	for _, tx := range transactions {
		rows = append(rows, map[string]any{
			"id":              tx.ID,
			"accountId":       tx.AccountID,
			"ledgerEntryId":   tx.LedgerEntryID,
			"amountCents":     tx.AmountCents,
			"balanceCents":    tx.BalanceCents,
			"frozenCents":     tx.FrozenCents,
			"availableCents":  tx.AvailableCents,
			"totalSpentCents": tx.TotalSpentCents,
			"currency":        tx.Currency,
			"createdAt":       tx.CreatedAt,
		})
	}
	return rows
}

func manualTopUpProjections(topups []clients.ManualTopUp) []map[string]any {
	rows := make([]map[string]any, 0, len(topups))
	for _, topup := range topups {
		rows = append(rows, structToMap(topup))
	}
	return rows
}

func walletHasMoneyFacts(wallet clients.Wallet) bool {
	return wallet.BalanceCents != 0 || wallet.FrozenCents != 0 || wallet.AvailableCents != 0 || wallet.TotalSpentCents != 0
}

func ledgerEntryType(entry clients.LedgerEntry) string {
	if entry.Source == "manual_topup" {
		return "manual_topup"
	}
	return entry.Source
}

func ledgerEntryAmount(entry clients.LedgerEntry) int64 {
	if entry.Direction == "debit" {
		return -entry.AmountCents
	}
	return entry.AmountCents
}

func (app *runtimeApp) resourceLedgerEvidenceLocked() []any {
	rows := []any{}
	for _, workspace := range app.workspaces {
		workspaceID := stringValue(workspace["id"])
		computeID := stringValue(workspace["currentComputeAllocationId"])
		storageID := stringValue(workspace["storageId"])
		attachmentID := stringValue(workspace["currentAttachmentId"])
		compute := app.computes[computeID]
		storage := app.storages[storageID]
		attachment := app.attachments[attachmentID]
		operation := app.operationEvidenceForResourceLocked(workspaceID, computeID, storageID, attachmentID)
		ownerAccountID := firstNonEmpty(stringValue(workspace["ownerAccountId"]), stringValue(compute["ownerAccountId"]), stringValue(storage["ownerAccountId"]), stringValue(attachment["ownerAccountId"]))
		ownerUserID := firstNonEmpty(stringValue(workspace["ownerUserId"]), stringValue(compute["ownerUserId"]), stringValue(storage["ownerUserId"]), stringValue(attachment["ownerUserId"]))
		rows = append(rows, map[string]any{
			"id":                   firstNonEmpty(workspaceID, computeID, storageID, attachmentID),
			"accountId":            ownerAccountID,
			"ownerAccountId":       ownerAccountID,
			"ownerUserId":          ownerUserID,
			"workspaceId":          workspaceID,
			"workspaceIds":         uniqueStrings([]string{workspaceID}),
			"computeAllocationId":  computeID,
			"storageId":            storageID,
			"attachmentId":         attachmentID,
			"cvmInstanceId":        firstNonEmpty(stringValue(compute["cvmInstanceId"]), stringValue(compute["providerResourceId"])),
			"nodeName":             firstNonEmpty(stringValue(compute["nodeName"]), stringValue(compute["machineName"])),
			"providerRequestId":    firstNonEmpty(stringValue(compute["providerRequestId"]), stringValue(storage["providerRequestId"]), stringValue(attachment["providerRequestId"])),
			"operationId":          firstNonEmpty(stringValue(operation["operationId"]), stringValue(compute["operationId"]), stringValue(storage["operationId"]), stringValue(attachment["operationId"])),
			"costTags":             firstNonNil(operation["costTags"], compute["costTags"], storage["costTags"], attachment["costTags"]),
			"ledgerEntryIds":       app.ledgerEntryIDsLocked(workspaceID, computeID, storageID, attachmentID),
			"walletTransactionIds": app.walletTransactionIDsLocked(workspaceID, computeID, storageID, attachmentID),
		})
	}
	return rows
}

func (app *runtimeApp) operationEvidenceForResourceLocked(ids ...string) map[string]any {
	for index := len(app.runtimeOps) - 1; index >= 0; index-- {
		operation := app.runtimeOps[index]
		if mapContainsAnyID(operation, ids...) {
			payload, _ := operation["redactedProviderPayload"].(map[string]any)
			return map[string]any{"operationId": operation["operationId"], "costTags": firstNonNil(operation["costTags"], payload["costTags"])}
		}
	}
	return map[string]any{}
}

func (app *runtimeApp) ledgerEntryIDsLocked(ids ...string) []string {
	output := []string{}
	for _, entry := range app.ledger {
		if mapContainsAnyID(entry, ids...) {
			output = append(output, stringValue(entry["id"]))
		}
	}
	return uniqueStrings(output)
}

func (app *runtimeApp) walletTransactionIDsLocked(ids ...string) []string {
	output := []string{}
	for _, tx := range app.walletTx {
		metadata, _ := tx["metadata"].(map[string]any)
		if mapContainsAnyID(metadata, ids...) {
			output = append(output, stringValue(tx["id"]))
		}
	}
	return uniqueStrings(output)
}

func (app *runtimeApp) addWalletTxLocked(accountID string, txType string, metadata map[string]any) {
	app.walletTx = append(app.walletTx, map[string]any{"id": "wallet-" + stableID(accountID, txType, time.Now().UTC().String())[:12], "accountId": accountID, "type": txType, "metadata": metadata})
}

func (app *runtimeApp) wallet(accountID string) map[string]any {
	if accountID == "" {
		accountID = "acct-local"
	}
	if wallet, ok := app.wallets[accountID]; ok {
		return wallet
	}
	return map[string]any{"id": accountID, "accountId": accountID, "balance": float64(0), "frozen": float64(0), "available": float64(0), "totalRecharged": float64(0)}
}

func walletProjection(wallet clients.Wallet) map[string]any {
	return map[string]any{
		"id":              wallet.AccountID,
		"accountId":       wallet.AccountID,
		"balance":         float64(wallet.BalanceCents) / 100,
		"balanceCents":    wallet.BalanceCents,
		"frozen":          float64(wallet.FrozenCents) / 100,
		"frozenCents":     wallet.FrozenCents,
		"available":       float64(wallet.AvailableCents) / 100,
		"availableCents":  wallet.AvailableCents,
		"totalSpent":      float64(wallet.TotalSpentCents) / 100,
		"totalSpentCents": wallet.TotalSpentCents,
		"currency":        wallet.Currency,
	}
}

func walletFromMap(row map[string]any) clients.Wallet {
	return clients.Wallet{
		AccountID:       stringValue(row["accountId"]),
		BalanceCents:    int64(numberField(row, "balanceCents", 0)),
		FrozenCents:     int64(numberField(row, "frozenCents", 0)),
		AvailableCents:  int64(numberField(row, "availableCents", 0)),
		TotalSpentCents: int64(numberField(row, "totalSpentCents", 0)),
		Currency:        firstNonEmpty(stringValue(row["currency"]), "CNY"),
	}
}

func packageList() []any {
	return []any{
		map[string]any{"id": "basic", "name": "Basic", "available": true, "cpu": 2, "memoryGb": 4, "diskGb": 10, "server": "2c4g", "price": map[string]any{"computeHourly": 0.468, "storageGbMonth": 0.432}},
		map[string]any{"id": "pro", "name": "Pro", "available": true, "cpu": 8, "memoryGb": 16, "diskGb": 100, "server": "8c16g", "price": map[string]any{"computeHourly": 1.38, "storageGbMonth": 0.432}},
	}
}

func computePools() []any {
	return []any{
		map[string]any{"id": "pool-basic", "name": "Basic", "available": true, "provider": "tencent-tke"},
		map[string]any{"id": "pool-pro", "name": "Pro", "available": true, "provider": "tencent-tke"},
	}
}

func workspaceDomain() string {
	return strings.Trim(strings.TrimPrefix(strings.TrimPrefix(firstNonEmpty(os.Getenv("OPL_WORKSPACE_DOMAIN"), "workspace.medopl.cn"), "https://"), "http://"), "/")
}

func stableID(parts ...string) string {
	hash := sha1.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func compactID(value string) string {
	cleaned := strings.Builder{}
	lastDash := false
	for _, r := range strings.ToLower(value) {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			cleaned.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			cleaned.WriteByte('-')
			lastDash = true
		}
	}
	result := strings.Trim(cleaned.String(), "-")
	if len(result) > 48 {
		result = strings.Trim(result[:48], "-")
	}
	if result == "" {
		return "resource"
	}
	return result
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func nested(root map[string]any, keys ...string) any {
	var current any = root
	for _, key := range keys {
		asMap, ok := current.(map[string]any)
		if !ok {
			if raw, ok := current.(map[string]string); ok {
				return raw[key]
			}
			return nil
		}
		current = asMap[key]
	}
	return current
}

func number(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	default:
		return 0
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	output := map[string]any{}
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneMapMap(input map[string]map[string]any) map[string]map[string]any {
	output := map[string]map[string]any{}
	for key, value := range input {
		output[key] = cloneMap(value)
	}
	return output
}

func cloneMapSlice(input []map[string]any) []map[string]any {
	output := make([]map[string]any, 0, len(input))
	for _, item := range input {
		output = append(output, cloneMap(item))
	}
	return output
}

func copySlice(input []map[string]any) []any {
	output := make([]any, 0, len(input))
	for _, item := range input {
		output = append(output, cloneMap(item))
	}
	return output
}

func values(input map[string]map[string]any) []any {
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	output := make([]any, 0, len(keys))
	for _, key := range keys {
		output = append(output, cloneMap(input[key]))
	}
	return output
}

func filteredValues(input map[string]map[string]any, include func(map[string]any) bool) []any {
	rows := values(input)
	output := make([]any, 0, len(rows))
	for _, row := range rows {
		item := row.(map[string]any)
		if include(item) {
			output = append(output, item)
		}
	}
	return output
}

func sanitizedUserValues(input map[string]map[string]any, includeDeleted bool) []any {
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	output := make([]any, 0, len(keys))
	for _, key := range keys {
		if !includeDeleted && stringValue(input[key]["status"]) == "deleted" {
			continue
		}
		output = append(output, sanitizeUser(input[key]))
	}
	return output
}

func uniqueStrings(input []string) []string {
	seen := map[string]bool{}
	output := []string{}
	for _, value := range input {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		output = append(output, value)
	}
	return output
}

func stringSliceField(input map[string]any, key string) []string {
	raw, ok := input[key].([]any)
	if !ok {
		return nil
	}
	output := []string{}
	for _, item := range raw {
		if value := stringValue(item); value != "" {
			output = append(output, value)
		}
	}
	return output
}

func stringSet(input []string) map[string]bool {
	output := map[string]bool{}
	for _, value := range input {
		if value != "" {
			output[value] = true
		}
	}
	return output
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func mapContainsAnyID(input map[string]any, ids ...string) bool {
	for _, id := range ids {
		if id == "" {
			continue
		}
		for _, value := range input {
			if stringValue(value) == id {
				return true
			}
		}
	}
	return false
}

func countStatus(input map[string]map[string]any, status string) int {
	count := 0
	for _, item := range input {
		if item["status"] == status || item["state"] == status {
			count++
		}
	}
	return count
}

func countActiveURLs(input map[string]map[string]any) int {
	count := 0
	for _, item := range input {
		if nested(item, "access", "tokenStatus") == "active" {
			count++
		}
	}
	return count
}

func totalAccountField(accounts []any, key string) float64 {
	total := float64(0)
	for _, item := range accounts {
		account, _ := item.(map[string]any)
		total += number(account[key])
	}
	return total
}

func totalTopupsForAccount(topups []map[string]any, accountID string) float64 {
	total := float64(0)
	for _, topup := range topups {
		if firstNonEmpty(stringValue(topup["accountId"]), stringValue(topup["targetAccountId"])) != accountID {
			continue
		}
		total += amountValue(topup)
	}
	return total
}

func totalDebitsForAccount(accountID string, transactions []map[string]any, ledger []map[string]any) float64 {
	total := float64(0)
	for _, tx := range transactions {
		if stringValue(tx["accountId"]) != accountID {
			continue
		}
		amount := amountValue(tx)
		if amount < 0 {
			total += -amount
		}
	}
	for _, entry := range ledger {
		if stringValue(entry["accountId"]) != accountID {
			continue
		}
		amount := amountValue(entry)
		if amount < 0 {
			total += -amount
		}
	}
	return total
}

func totalDebits(transactions []map[string]any, ledger []map[string]any) float64 {
	total := float64(0)
	for _, tx := range transactions {
		if amount := amountValue(tx); amount < 0 {
			total += -amount
		}
	}
	for _, entry := range ledger {
		if amount := amountValue(entry); amount < 0 {
			total += -amount
		}
	}
	return total
}

func amountValue(row map[string]any) float64 {
	if amount := number(row["amount"]); amount != 0 {
		return amount
	}
	return float64(number(row["amountCents"])) / 100
}
