package server

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	errUserNotFound              = errors.New("user_not_found")
	errUserExists                = errors.New("user_already_exists")
	errLastActiveAdmin           = errors.New("last_active_admin")
	errUserDeleted               = errors.New("user_deleted")
	errInvalidRole               = errors.New("invalid_role")
	errAccountNotFound           = errors.New("account_not_found")
	errOrganizationNotFound      = errors.New("organization_not_found")
	errMembershipUserNotFound    = errors.New("membership_user_not_found")
	errMembershipAccountMismatch = errors.New("membership_account_mismatch")
)

func newControlPlaneApp() *controlPlaneServer {
	return newControlPlaneAppEmpty()
}

func newControlPlaneAppWithStore(store StateStore) (*controlPlaneServer, error) {
	app := newControlPlaneAppEmpty()
	app.store = store
	if tableStore, ok := store.(controlPlaneTableStore); ok {
		app.tables = tableStore
	}
	if app.tables == nil {
		app.tables = newMemoryTableStore()
	}
	if err := app.importBootstrapUsers(); err != nil {
		return nil, err
	}
	return app, nil
}

func (app *controlPlaneServer) ensureBootstrapAdmin(ctx context.Context, service *controlplane.Service) error {
	accounts, err := app.tables.ListAccounts(ctx, "")
	if err != nil {
		return err
	}
	users, err := app.tables.ListUsers(ctx, true)
	if err != nil {
		return err
	}
	organizations, err := app.tables.ListOrganizations(ctx)
	if err != nil {
		return err
	}
	memberships, err := app.tables.ListMemberships(ctx)
	if err != nil {
		return err
	}
	operatorAccounts := make([]map[string]any, 0, 1)
	for _, account := range accounts {
		if stringValue(account["id"]) == "acct-admin" || stringValue(account["ownerUserId"]) == "usr-admin" {
			operatorAccounts = append(operatorAccounts, account)
		}
	}
	operatorUsers := make([]map[string]any, 0, 1)
	for _, user := range users {
		if stringValue(user["id"]) == "usr-admin" || normalizeEmail(stringValue(user["email"])) == "admin@medopl.cn" ||
			stringValue(user["accountId"]) == "acct-admin" || stringValue(user["role"]) == "admin" {
			operatorUsers = append(operatorUsers, user)
		}
	}
	operatorOrganizations := make([]map[string]any, 0, 1)
	for _, organization := range organizations {
		if stringValue(organization["billingAccountId"]) == "acct-admin" {
			operatorOrganizations = append(operatorOrganizations, organization)
		}
	}
	operatorMemberships := make([]map[string]any, 0, 1)
	for _, membership := range memberships {
		if stringValue(membership["accountId"]) == "acct-admin" || stringValue(membership["userId"]) == "usr-admin" {
			operatorMemberships = append(operatorMemberships, membership)
		}
	}
	operatorPresent := len(operatorAccounts)+len(operatorUsers)+len(operatorOrganizations)+len(operatorMemberships) != 0
	operatorComplete := len(operatorAccounts) == 1 && len(operatorUsers) == 1 && len(operatorOrganizations) == 1 && len(operatorMemberships) == 1
	var localSub2APIUserID int64
	if operatorComplete {
		account, user := operatorAccounts[0], operatorUsers[0]
		organization, membership := operatorOrganizations[0], operatorMemberships[0]
		localSub2APIUserID, _ = positiveIntegerField(account, "sub2apiUserId")
		operatorComplete = stringValue(account["id"]) == "acct-admin" && stringValue(account["ownerUserId"]) == "usr-admin" && localSub2APIUserID > 0 && stringValue(account["status"]) == "active" &&
			stringValue(user["id"]) == "usr-admin" && stringValue(user["email"]) == "admin@medopl.cn" && stringValue(user["accountId"]) == "acct-admin" && stringValue(user["role"]) == "admin" && stringValue(user["status"]) == "active" &&
			stringValue(organization["id"]) != "" && stringValue(organization["status"]) == "active" &&
			stringValue(membership["id"]) != "" && stringValue(membership["organizationId"]) == stringValue(organization["id"]) && stringValue(membership["userId"]) == "usr-admin" &&
			stringValue(membership["accountId"]) == "acct-admin" && stringValue(membership["role"]) == "owner" && stringValue(membership["status"]) == "active"
	}
	if operatorPresent && !operatorComplete {
		return errBootstrapUserIdentityConflict
	}
	identity, err := service.Sub2APIAdminIdentity(ctx)
	if err != nil {
		return err
	}
	if identity.ID <= 0 || normalizeEmail(identity.Email) != "admin@medopl.cn" || identity.Status != "active" {
		return clients.ErrSub2APIIdentityConflict
	}
	if operatorComplete {
		if identity.ID != localSub2APIUserID {
			return clients.ErrSub2APIIdentityConflict
		}
		return nil
	}
	account := map[string]any{"id": "acct-admin", "ownerUserId": "usr-admin", "sub2apiUserId": identity.ID, "status": "active"}
	user := map[string]any{"id": "usr-admin", "email": "admin@medopl.cn", "accountId": "acct-admin", "role": "admin", "status": "active"}
	organization := map[string]any{"id": "org-admin", "name": "OPL Cloud", "billingAccountId": "acct-admin", "status": "active"}
	membership := map[string]any{"id": "mem-admin", "accountId": "acct-admin", "organizationId": "org-admin", "userId": "usr-admin", "role": "owner", "status": "active"}
	return app.tables.CreateInvitedAccount(ctx, account, user, organization, membership)
}

func newControlPlaneAppEmpty() *controlPlaneServer {
	tables := newMemoryTableStore()
	return &controlPlaneServer{
		tables:             tables,
		sessionCredentials: newSessionCredentialVault(time.Now),
		loginRateLimits:    map[string]loginFailure{},
	}
}

func (app *controlPlaneServer) userFacts() controlPlaneRecordSet {
	return app.userRecordSet(true)
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
		"product":                map[string]any{"name": "OPL Cloud", "console": "OPL Console", "workspace": "OPL Workspace"},
		"packages":               packageList(computePools),
		"computePools":           computePools,
		"workspaces":             workspaces,
		"computeAllocations":     rowsAsAnyFromMaps(app.listComputes(accountID)),
		"storageVolumes":         rowsAsAnyFromMaps(app.listStorages(accountID)),
		"storageAttachments":     rowsAsAnyFromMaps(app.listAttachments(accountID)),
		"accounts":               accounts,
		"supportTickets":         rowsAsAnyFromMaps(app.listSupportMappings(accountID)),
		"auditEvents":            rowsAsAnyFromMaps(app.listAuditEvents(accountID)),
		"resourceLedgerEvidence": app.resourceLedgerEvidenceLocked(accountID),
		"notifications":          []any{},
		"runtimeOperations":      rowsAsAnyFromMaps(rowsForAccount(app.listRuntimeOperations(), accountID)),
		"generatedAt":            time.Now().UTC().Format(time.RFC3339),
	}
}

func rowsForAccount(rows []map[string]any, accountID string) []map[string]any {
	out := make([]map[string]any, 0)
	for _, row := range rows {
		if stringValue(row["accountId"]) == accountID {
			out = append(out, row)
		}
	}
	return out
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

func (app *controlPlaneServer) attachmentRecordSet(accountID string) controlPlaneRecordSet {
	return recordSetFromRows(app.listAttachments(accountID))
}

func (app *controlPlaneServer) listWorkspaces(accountID string) []map[string]any {
	rows, err := app.tables.ListWorkspaces(context.Background(), accountID)
	if err != nil {
		return nil
	}
	return rows
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

func rowsToRecords(rows []map[string]any) []controlPlaneRecord {
	out := make([]controlPlaneRecord, 0, len(rows))
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

func (app *controlPlaneServer) listOrganizations() []map[string]any {
	rows, err := app.tables.ListOrganizations(context.Background())
	if err != nil {
		return nil
	}
	return rows
}

func (app *controlPlaneServer) listMemberships() []map[string]any {
	rows, err := app.tables.ListMemberships(context.Background())
	if err != nil {
		return nil
	}
	return rows
}

func (app *controlPlaneServer) listRuntimeOperations() []map[string]any {
	rows, err := app.tables.ListRuntimeOperations(context.Background())
	if err != nil {
		return nil
	}
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

func terminalComputeStatus(status string) bool {
	return status == "destroyed" || status == "deleted"
}

func terminalStorageStatus(status string) bool {
	return status == "destroyed" || status == "deleted"
}

func terminalAttachmentStatus(status string) bool {
	return status == "detached" || status == "deleted"
}

func terminalWorkspaceStatus(status string) bool {
	return status == "data_deleted" || status == "unrecoverable" || status == "deleted" || status == "destroyed"
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

func cloneStateRows(input []controlPlaneRecord) []controlPlaneRecord {
	output := make([]controlPlaneRecord, 0, len(input))
	for _, item := range input {
		output = append(output, cloneMap(item))
	}
	return output
}

func sessionsFromFacts(input controlPlaneRecordSet) map[string]sessionRecord {
	output := map[string]sessionRecord{}
	now := time.Now().UTC()
	for id, row := range input {
		expiresAt, err := time.Parse(time.RFC3339, stringValue(row["expiresAt"]))
		if err != nil || now.After(expiresAt) {
			continue
		}
		sessionID := firstNonEmpty(stringValue(row["id"]), id)
		output[sessionID] = sessionRecord{
			ID:        sessionID,
			UserID:    stringValue(row["userId"]),
			CSRF:      stringValue(row["csrf"]),
			ExpiresAt: expiresAt,
		}
	}
	return output
}

func copySlice(input []controlPlaneRecord) []any {
	output := make([]any, 0, len(input))
	for _, item := range input {
		output = append(output, cloneMap(item))
	}
	return output
}

func values(input controlPlaneRecordSet) []any {
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

func accountValues(input controlPlaneRecordSet, accountID string) []any {
	if accountID == "" {
		return values(input)
	}
	return filteredValues(input, func(item map[string]any) bool {
		return firstNonEmpty(stringValue(item["accountId"]), stringValue(item["ownerAccountId"])) == accountID
	})
}

func auditEventsForAccount(events []controlPlaneRecord, accountID string) []any {
	output := []any{}
	for _, event := range events {
		if accountID == "" || stringValue(event["targetAccountId"]) == accountID || stringValue(event["actorAccountId"]) == accountID {
			output = append(output, cloneMap(event))
		}
	}
	return output
}

func filteredValues(input controlPlaneRecordSet, include func(map[string]any) bool) []any {
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

func countStatus(input controlPlaneRecordSet, status string) int {
	count := 0
	for _, item := range input {
		if item["status"] == status || item["state"] == status {
			count++
		}
	}
	return count
}
