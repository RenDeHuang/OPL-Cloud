package server

import (
	"crypto/sha1"
	"encoding/hex"
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
	computes    map[string]map[string]any
	storages    map[string]map[string]any
	attachments map[string]map[string]any
	workspaces  map[string]map[string]any
	users       map[string]map[string]any
	orgs        map[string]map[string]any
	memberships map[string]map[string]any
	wallets     map[string]map[string]any
	ledger      []map[string]any
	usage       []map[string]any
	walletTx    []map[string]any
	topups      []map[string]any
}

func newRuntimeApp() *runtimeApp {
	return &runtimeApp{
		computes:    map[string]map[string]any{},
		storages:    map[string]map[string]any{},
		attachments: map[string]map[string]any{},
		workspaces:  map[string]map[string]any{},
		users:       map[string]map[string]any{"usr-admin": {"id": "usr-admin", "email": "owner@example.com", "accountId": "acct-admin", "role": "admin", "status": "active"}},
		orgs:        map[string]map[string]any{},
		memberships: map[string]map[string]any{},
		wallets:     map[string]map[string]any{},
	}
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
		"billingLedger":         copySlice(app.ledger),
		"resourceUsageLogs":     copySlice(app.usage),
		"walletTransactions":    copySlice(app.walletTx),
		"manualTopups":          copySlice(app.topups),
		"billingReconciliation": map[string]any{"guard": map[string]any{"status": "not_required", "blockNewWorkspaces": false, "reason": "billing_reconciliation_not_required"}},
		"evidenceLedger":        []any{},
		"audit":                 []any{},
		"notifications":         []any{},
		"runtimeOperations":     []any{},
		"generatedAt":           time.Now().UTC().Format(time.RFC3339),
	}
}

func (app *runtimeApp) currentUserLocked() map[string]any {
	if user, ok := app.users["usr-admin"]; ok {
		return cloneMap(user)
	}
	return map[string]any{"id": "usr-admin", "email": "owner@example.com", "accountId": "acct-admin", "role": "admin", "status": "active"}
}

func (app *runtimeApp) createOrganization(input map[string]any) map[string]any {
	app.mu.Lock()
	defer app.mu.Unlock()
	name := stringField(input, "name", "Organization")
	id := "org-" + compactID(name+"-"+time.Now().UTC().Format("20060102150405.000000000"))
	org := map[string]any{"id": id, "name": name, "billingAccountId": stringField(input, "billingAccountId", "acct-admin"), "status": "active"}
	app.orgs[id] = org
	return cloneMap(org)
}

func (app *runtimeApp) createMembership(input map[string]any) map[string]any {
	app.mu.Lock()
	defer app.mu.Unlock()
	orgID := stringField(input, "organizationId", "")
	userID := stringField(input, "userId", "")
	id := "mem-" + stableID(orgID, userID, time.Now().UTC().String())[:12]
	membership := map[string]any{"id": id, "organizationId": orgID, "userId": userID, "role": stringField(input, "role", "member"), "status": "active"}
	app.memberships[id] = membership
	return cloneMap(membership)
}

func (app *runtimeApp) createUser(input map[string]any) map[string]any {
	app.mu.Lock()
	defer app.mu.Unlock()
	email := stringField(input, "email", "owner@example.com")
	id := "usr-" + compactID(email+"-"+time.Now().UTC().Format("20060102150405.000000000"))
	user := map[string]any{"id": id, "email": email, "accountId": stringField(input, "accountId", "acct-admin"), "role": stringField(input, "role", "owner"), "status": "active"}
	app.users[id] = user
	return cloneMap(user)
}

func (app *runtimeApp) setUserStatus(input map[string]any, status string) map[string]any {
	app.mu.Lock()
	defer app.mu.Unlock()
	id := stringField(input, "userId", "")
	user := app.users[id]
	if user == nil {
		user = map[string]any{"id": id}
	}
	user["status"] = status
	app.users[id] = user
	return cloneMap(user)
}

func (app *runtimeApp) rememberWorkspaceProjection(workspace domain.WorkspaceProjection) {
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
}

func (app *runtimeApp) rememberCompute(allocation any) {
	if row, ok := allocation.(map[string]any); ok {
		app.mu.Lock()
		defer app.mu.Unlock()
		accountID := stringValue(row["accountId"])
		app.computes[stringValue(row["id"])] = row
		if stringValue(row["status"]) == "destroyed" {
			app.rememberReleaseLocked(accountID, "compute", stringValue(row["id"]), row)
			app.suspendWorkspacesForComputeLocked(stringValue(row["id"]))
			return
		}
		app.rememberHoldLocked(accountID, "compute", stringValue(row["id"]), row)
	}
}

func (app *runtimeApp) rememberStorage(volume any) {
	if row, ok := volume.(map[string]any); ok {
		app.mu.Lock()
		defer app.mu.Unlock()
		accountID := stringValue(row["accountId"])
		app.storages[stringValue(row["id"])] = row
		if stringValue(row["status"]) == "destroyed" {
			app.rememberReleaseLocked(accountID, "storage", stringValue(row["id"]), row)
			app.markWorkspacesStorageDestroyedLocked(stringValue(row["id"]))
			return
		}
		app.rememberHoldLocked(accountID, "storage", stringValue(row["id"]), row)
	}
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

func (app *runtimeApp) rememberAttachment(attachment any, input map[string]any) {
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
	}
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

func (app *runtimeApp) managementState() map[string]any {
	app.mu.Lock()
	defer app.mu.Unlock()
	return map[string]any{
		"organization":           nil,
		"organizations":          values(app.orgs),
		"users":                  values(app.users),
		"memberships":            values(app.memberships),
		"accounts":               []any{app.wallet("acct-admin")},
		"packages":               packageList(),
		"computePools":           computePools(),
		"workspaces":             values(app.workspaces),
		"computeAllocations":     values(app.computes),
		"storageVolumes":         values(app.storages),
		"storageAttachments":     values(app.attachments),
		"resourceLedgerEvidence": app.resourceLedgerEvidenceLocked(),
		"walletTransactions":     copySlice(app.walletTx),
		"manualTopups":           copySlice(app.topups),
	}
}

func (app *runtimeApp) operatorSummary() map[string]any {
	app.mu.Lock()
	defer app.mu.Unlock()
	running := countStatus(app.computes, "running")
	return map[string]any{
		"product":                "OPL Console",
		"generatedAt":            time.Now().UTC().Format(time.RFC3339),
		"accountScope":           "all",
		"accounts":               map[string]any{"total": len(app.wallets), "frozen": 0, "balance": totalWallet(app.wallets, "balance"), "totalSpent": totalDebits(app.walletTx)},
		"workspaces":             map[string]any{"total": len(app.workspaces), "running": countStatus(app.workspaces, "running"), "urlActive": countActiveURLs(app.workspaces), "destroyed": countStatus(app.workspaces, "destroyed"), "needsAttention": 0},
		"computeAllocations":     map[string]any{"total": len(app.computes), "running": running, "failed": countStatus(app.computes, "failed")},
		"notifications":          map[string]any{"total": 0, "error": 0, "warning": 0, "recent": []any{}},
		"runtimeOperations":      map[string]any{"total": 0, "failed": 0, "recentFailed": []any{}},
		"failedOperations":       []any{},
		"resourceAnomalies":      []any{},
		"resourceLedgerEvidence": map[string]any{"total": len(app.ledger), "recent": copySlice(app.ledger)},
		"productionE2E":          map[string]any{},
		"billingReconciliation":  map[string]any{"reports": 0, "guard": map[string]any{"status": "not_required", "blockNewWorkspaces": false, "reason": "billing_reconciliation_not_required"}},
	}
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

func (app *runtimeApp) proxyWorkspace(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromPath(r.URL.Path)
	if workspaceID == "" {
		http.NotFound(w, r)
		return
	}
	if token := r.URL.Query().Get("token"); token != "" {
		setWorkspaceGatewayCookies(w, workspaceID, token)
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
		writeError(w, http.StatusBadGateway, err.Error())
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
		writeError(w, http.StatusBadGateway, err.Error())
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

func (app *runtimeApp) addUsageLocked(accountID string, resourceType string, ids map[string]any) {
	entry := map[string]any{"id": "usage-" + stableID(accountID, resourceType, time.Now().UTC().String())[:12], "accountId": accountID, "resourceType": resourceType}
	for key, value := range ids {
		entry[key] = value
	}
	app.usage = append(app.usage, entry)
}

func (app *runtimeApp) rememberResourceSettlement(result clients.ResourceSettlementResult) {
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

	usage := map[string]any{"id": "usage-" + result.ID, "accountId": result.AccountID, "resourceType": resourceType, "amountCents": result.AmountCents}
	for key, value := range ids {
		usage[key] = value
	}
	app.usage = append(app.usage, usage)
	app.walletTx = append(app.walletTx, map[string]any{"id": result.WalletTransactionID, "accountId": result.AccountID, "type": debitType, "metadata": ids})
	app.wallets[result.AccountID] = walletProjection(result.Wallet)
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
			"ledgerEntryIds":       app.ledgerEntryIDsLocked(workspaceID, computeID, storageID, attachmentID),
			"walletTransactionIds": app.walletTransactionIDsLocked(workspaceID, computeID, storageID, attachmentID),
		})
	}
	return rows
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
	wallet := map[string]any{"id": accountID, "accountId": accountID, "balance": float64(0), "frozen": float64(0), "available": float64(0), "totalRecharged": float64(0)}
	app.wallets[accountID] = wallet
	return wallet
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

func totalWallet(wallets map[string]map[string]any, key string) float64 {
	total := float64(0)
	for _, wallet := range wallets {
		total += number(wallet[key])
	}
	return total
}

func totalDebits(transactions []map[string]any) float64 {
	return float64(len(transactions))
}
