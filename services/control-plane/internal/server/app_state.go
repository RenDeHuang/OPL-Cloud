package server

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

type controlPlaneServer struct {
	mu            sync.Mutex
	resourceLocks sync.Map
	// ponytail: process-local dedup matches one replica; CLS remains the durable alert history.
	operationalAlertStates sync.Map
	store                  StateStore
	tables                 controlPlaneTableStore
	sessionCredentials     *SessionCredentialVault
	// ponytail: per-process limiter; move to Redis when login traffic spans multiple replicas.
	loginRateLimits map[string]loginFailure
	deployment      deploymentProfile
}

func (app *controlPlaneServer) lockResource(resourceType, id string) func() {
	unlock, _ := app.lockResourceContext(context.Background(), resourceType, id)
	return unlock
}

func (app *controlPlaneServer) lockResourceContext(ctx context.Context, resourceType, id string) (func(), error) {
	value, _ := app.resourceLocks.LoadOrStore(resourceType+":"+id, make(chan struct{}, 1))
	lock := value.(chan struct{})
	select {
	case lock <- struct{}{}:
		// ponytail: process-local locks match one replica; use DB advisory locks before scaling replicas.
		return func() { <-lock }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (app *controlPlaneServer) lockEntitlementResources(computeID, storageID, attachmentID string) func() {
	var unlocks []func()
	for _, resource := range [][2]string{{"compute", computeID}, {"storage", storageID}, {"attachment", attachmentID}} {
		if resource[1] != "" {
			unlocks = append(unlocks, app.lockResource(resource[0], resource[1]))
		}
	}
	return func() {
		for index := len(unlocks) - 1; index >= 0; index-- {
			unlocks[index]()
		}
	}
}

type loginFailure struct {
	Count   int
	FirstAt time.Time
}

var (
	errUserNotFound    = errors.New("user_not_found")
	errUserExists      = errors.New("user_already_exists")
	errLastActiveAdmin = errors.New("last_active_admin")
	errUserDeleted     = errors.New("user_deleted")
	errInvalidRole     = errors.New("invalid_role")
	errAccountNotFound = errors.New("account_not_found")
)

func newControlPlaneAppWithStore(store StateStore) (*controlPlaneServer, error) {
	tableStore, ok := store.(controlPlaneTableStore)
	if !ok {
		return nil, errors.New("control_plane_store_must_implement_table_store")
	}
	app := &controlPlaneServer{
		store:              store,
		tables:             tableStore,
		sessionCredentials: newSessionCredentialVault(time.Now),
		loginRateLimits:    map[string]loginFailure{},
	}
	if err := app.importBootstrapUsers(); err != nil {
		return nil, err
	}
	return app, nil
}

func (app *controlPlaneServer) ensureBootstrapAdmin(ctx context.Context, service *controlplane.Service) error {
	account, accountFound, err := app.tables.GetAccount(ctx, "acct-admin")
	if err != nil {
		return err
	}
	user, userFound, err := app.tables.GetUser(ctx, "usr-admin")
	if err != nil {
		return err
	}
	var userByEmail map[string]any
	var userEmail string
	emailFound := false
	if userFound {
		userEmail, err = canonicalEmail(stringValue(user["email"]))
		if err == nil {
			userByEmail, emailFound, err = app.tables.GetUserByEmail(ctx, userEmail, true)
			if err != nil {
				return err
			}
		}
	}
	operatorPresent := accountFound || userFound
	operatorComplete := accountFound && userFound && emailFound && stringValue(userByEmail["id"]) == "usr-admin"
	var localSub2APIUserID int64
	if operatorComplete {
		localSub2APIUserID, _ = positiveIntegerField(account, "sub2apiUserId")
		operatorComplete = stringValue(account["id"]) == "acct-admin" && stringValue(account["ownerUserId"]) == "usr-admin" && localSub2APIUserID > 0 && stringValue(account["status"]) == "active" &&
			isOperatorUser(user) && stringValue(user["email"]) == userEmail
	}
	if operatorPresent && !operatorComplete {
		return errBootstrapUserIdentityConflict
	}
	identity, err := service.Sub2APIAdminIdentity(ctx)
	if err != nil {
		return err
	}
	identityEmail, identityEmailErr := canonicalEmail(identity.Email)
	if identity.ID <= 0 || identityEmailErr != nil || identity.Status != "active" {
		return clients.ErrSub2APIIdentityConflict
	}
	if operatorComplete {
		if identity.ID != localSub2APIUserID || identityEmail != userEmail {
			return clients.ErrSub2APIIdentityConflict
		}
		return nil
	}
	bootstrapAccount := map[string]any{"id": "acct-admin", "ownerUserId": "usr-admin", "sub2apiUserId": identity.ID, "status": "active"}
	bootstrapUser := map[string]any{"id": "usr-admin", "email": identityEmail, "accountId": "acct-admin", "role": "admin", "status": "active"}
	return app.tables.CreateProvisionedAccount(ctx, bootstrapAccount, bootstrapUser)
}

func (app *controlPlaneServer) ensureDeploymentIdentity(ctx context.Context, service *controlplane.Service) error {
	if !app.deployment.customerOwned() {
		return app.ensureBootstrapAdmin(ctx, service)
	}
	identity, err := service.ConfiguredSub2APIIdentity(ctx)
	if err != nil {
		return err
	}
	identityEmail, emailErr := canonicalEmail(identity.Email)
	if identity.ID <= 0 || emailErr != nil || identity.Status != "active" {
		return clients.ErrSub2APIIdentityConflict
	}
	accountID := "acct-customer-" + stableID("customer-account", identityEmail)[:18]
	userID := "usr-customer-" + stableID("customer-user", identityEmail)[:18]
	account, accountFound, err := app.tables.GetAccount(ctx, accountID)
	if err != nil {
		return err
	}
	user, userFound, err := app.tables.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	if accountFound || userFound {
		remoteID, _ := positiveIntegerField(account, "sub2apiUserId")
		if !accountFound || !userFound || stringValue(account["ownerUserId"]) != userID || remoteID != identity.ID || stringValue(account["status"]) != "active" || stringValue(user["email"]) != identityEmail || stringValue(user["accountId"]) != accountID || stringValue(user["role"]) != "owner" || stringValue(user["status"]) != "active" {
			return clients.ErrSub2APIIdentityConflict
		}
		return nil
	}
	return app.tables.CreateProvisionedAccount(ctx,
		map[string]any{"id": accountID, "ownerUserId": userID, "sub2apiUserId": identity.ID, "status": "active", "workspacePurchaseEnabled": true},
		map[string]any{"id": userID, "email": identityEmail, "accountId": accountID, "role": "owner", "status": "active"})
}

func (app *controlPlaneServer) state(accountID string, computePools []any) map[string]any {
	app.mu.Lock()
	defer app.mu.Unlock()
	workspaces := app.workspaceStateRowsLocked(accountID)
	accounts := []any{}
	for _, account := range app.accountsLocked(accountID) {
		if stringValue(account.(map[string]any)["accountId"]) == accountID {
			accounts = append(accounts, account)
		}
	}
	return map[string]any{
		"product": map[string]any{
			"name": "OPL Cloud", "console": "OPL Console", "workspace": "OPL Workspace",
			"deploymentMode": string(app.deployment.Mode), "fabricProvider": string(app.deployment.FabricProvider),
		},
		"packages":               packageList(computePools),
		"computePools":           computePools,
		"workspaces":             workspaces,
		"computeAllocations":     rowsAsAnyFromMaps(app.listComputes(accountID)),
		"storageVolumes":         rowsAsAnyFromMaps(app.listStorages(accountID)),
		"storageAttachments":     rowsAsAnyFromMaps(app.listAttachments(accountID)),
		"accounts":               accounts,
		"supportTickets":         rowsAsAnyFromMaps(app.listSupportMappings(accountID)),
		"resourceLedgerEvidence": app.resourceLedgerEvidenceLocked(accountID),
		"notifications":          []any{},
		"runtimeOperations":      rowsAsAnyFromMaps(app.runtimeOperationRows(runtimeOperationQuery{AccountID: accountID})),
		"generatedAt":            time.Now().UTC().Format(time.RFC3339),
	}
}

func (app *controlPlaneServer) userRecordSet(includeDeleted bool) controlPlaneRecordSet {
	users, err := app.tables.ListUsers(context.Background(), includeDeleted)
	if err != nil {
		return controlPlaneRecordSet{}
	}
	out := controlPlaneRecordSet{}
	for _, user := range users {
		out[stringValue(user["id"])] = cloneMap(user)
	}
	return out
}

func (app *controlPlaneServer) listComputes(accountID string) []map[string]any {
	rows, err := app.tables.ListComputes(context.Background(), accountID)
	if err != nil {
		return nil
	}
	return rows
}

func (app *controlPlaneServer) computeRecordSet(accountID string) controlPlaneRecordSet {
	return recordSetFromRows(app.listComputes(accountID))
}

func (app *controlPlaneServer) listStorages(accountID string) []map[string]any {
	rows, err := app.tables.ListStorages(context.Background(), accountID)
	if err != nil {
		return nil
	}
	return rows
}

func (app *controlPlaneServer) storageRecordSet(accountID string) controlPlaneRecordSet {
	return recordSetFromRows(app.listStorages(accountID))
}

func (app *controlPlaneServer) listAttachments(accountID string) []map[string]any {
	rows, err := app.tables.ListAttachments(context.Background(), accountID)
	if err != nil {
		return nil
	}
	return rows
}

func (app *controlPlaneServer) listWorkspaces(accountID string) []map[string]any {
	rows, err := app.tables.ListWorkspaces(context.Background(), accountID)
	if err != nil {
		return nil
	}
	return rows
}

func (app *controlPlaneServer) workspaceByID(ctx context.Context, id string) (map[string]any, bool, error) {
	return app.tables.GetWorkspace(ctx, id)
}

func (app *controlPlaneServer) workspaceRecordSet(accountID string) controlPlaneRecordSet {
	return recordSetFromRows(app.listWorkspaces(accountID))
}

func recordSetFromRows(rows []map[string]any) controlPlaneRecordSet {
	out := controlPlaneRecordSet{}
	for _, row := range rows {
		out[stringValue(row["id"])] = cloneMap(row)
	}
	return out
}

func rowsAsAnyFromMaps(rows []map[string]any) []any {
	out := make([]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, cloneMap(row))
	}
	return out
}

func (app *controlPlaneServer) listAuditEvents(accountID string) []map[string]any {
	rows, err := app.tables.ListAuditEvents(context.Background(), accountID)
	if err != nil {
		return nil
	}
	return rows
}

func (app *controlPlaneServer) listSupportMappings(accountID string) []map[string]any {
	rows, err := app.tables.ListSupportMappings(context.Background(), accountID)
	if err != nil {
		return nil
	}
	return rows
}

func (app *controlPlaneServer) runtimeOperationRows(query runtimeOperationQuery) []map[string]any {
	query.Offset, query.Limit = 0, runtimeOperationPageSize
	page, err := app.tables.PageRuntimeOperations(context.Background(), query)
	if err != nil {
		return nil
	}
	rows := page.Items
	for _, row := range rows {
		if result := stringValue(row["result"]); result != "" {
			var payload map[string]any
			if json.Unmarshal([]byte(result), &payload) == nil {
				if errorCode := stringValue(payload["_fabricErrorCode"]); errorCode != "" {
					row["errorCode"] = errorCode
					delete(payload, "_fabricErrorCode")
				}
				row["redactedProviderPayload"] = payload
			}
		}
	}
	return rows
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

func workspaceServiceTarget(serviceName string) (*url.URL, error) {
	value := strings.TrimSpace(serviceName)
	if value == "" || len(value) > 63 || strings.ContainsAny(value, ".:/@\\\r\n") || !strings.HasPrefix(value, "opl-") {
		return nil, errors.New("invalid_workspace_runtime_destination")
	}
	if net.ParseIP(value) != nil || strings.EqualFold(value, "localhost") || strings.HasSuffix(strings.ToLower(value), ".localhost") {
		return nil, errors.New("invalid_workspace_runtime_destination")
	}
	if value[len(value)-1] == '-' {
		return nil, errors.New("invalid_workspace_runtime_destination")
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
			return nil, errors.New("invalid_workspace_runtime_destination")
		}
	}
	return url.Parse("http://" + value + ":3000")
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

func setWorkspaceGatewayRouteCookie(w http.ResponseWriter, workspaceID string) {
	http.SetCookie(w, &http.Cookie{Name: "opl_ws_active", Value: workspaceID, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
}

func isWorkspaceRequest(r *http.Request) bool {
	return isWorkspaceHost(r.Host)
}

func isWorkspaceHost(host string) bool {
	return strings.Trim(strings.Split(host, ":")[0], " ") == workspaceDomain()
}

func upsertProjectionByID(rows []controlPlaneRecord, row controlPlaneRecord) []controlPlaneRecord {
	key := projectionReplayKey(row)
	if key == "" {
		return append(rows, row)
	}
	for index := range rows {
		if projectionReplayKey(rows[index]) == key {
			rows[index] = row
			return rows
		}
	}
	return append(rows, row)
}

func projectionReplayKey(row controlPlaneRecord) string {
	id := stringValue(row["id"])
	if id == "" {
		return ""
	}
	resourceID := stringValue(row["resourceId"])
	if resourceID == "" {
		if metadata, _ := row["metadata"].(map[string]any); metadata != nil {
			resourceID = stringValue(metadata["resourceId"])
		}
	}
	return strings.Join([]string{id, stringValue(row["type"]), resourceID}, "\x00")
}

func packageList(computePools []any) []any {
	return packageRowsForComputePools(defaultPricingCatalog(), computePools)
}

func computePoolsFromFabricCatalog(catalog clients.FabricCatalog) []any {
	pools := make([]any, 0, len(catalog.WorkspacePackages))
	for _, pkg := range catalog.WorkspacePackages {
		pools = append(pools, map[string]any{
			"id":        firstNonEmpty(pkg.ComputeProfileID, "pool-"+pkg.ID),
			"packageId": pkg.ID,
			"name":      firstNonEmpty(pkg.Name, pkg.ID),
			"available": pkg.Available,
			"provider":  pkg.Provider,
			"cpu":       pkg.CPU,
			"memoryGb":  pkg.MemoryGB,
			"diskGb":    pkg.DiskGB,
		})
	}
	return pools
}

func workspaceDomain() string {
	return strings.Trim(strings.TrimPrefix(strings.TrimPrefix(firstNonEmpty(os.Getenv("OPL_WORKSPACE_DOMAIN"), "workspace.medopl.cn"), "https://"), "http://"), "/")
}

func mustJSON(value any) []byte {
	data, _ := json.Marshal(value)
	return data
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func cloneMap(input map[string]any) controlPlaneRecord {
	if input == nil {
		return controlPlaneRecord{}
	}
	output := controlPlaneRecord{}
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneStateTable(input controlPlaneRecordSet) controlPlaneRecordSet {
	output := controlPlaneRecordSet{}
	for key, value := range input {
		output[key] = cloneMap(value)
	}
	return output
}

func sanitizedUserValues(input controlPlaneRecordSet, includeDeleted bool) []any {
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

func countStatus(input controlPlaneRecordSet, status string) int {
	count := 0
	for _, item := range input {
		if item["status"] == status || item["state"] == status {
			count++
		}
	}
	return count
}
