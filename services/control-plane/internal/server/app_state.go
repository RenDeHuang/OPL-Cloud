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
)

type controlPlaneServer struct {
	mu     sync.Mutex
	store  StateStore
	tables controlPlaneTableStore
	// ponytail: per-process limiter; move to Redis when login traffic spans multiple replicas.
	loginRateLimits map[string]loginFailure
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
	errMembershipNotFound        = errors.New("membership_not_found")
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
	if err := app.ensureBootstrapAdmin(); err != nil {
		return nil, err
	}
	if err := app.importBootstrapUsers(); err != nil {
		return nil, err
	}
	return app, nil
}

func (app *controlPlaneServer) ensureBootstrapAdmin() error {
	users, err := app.tables.ListUsers(context.Background(), true)
	if err != nil {
		return err
	}
	if len(users) > 0 {
		return nil
	}
	if err := app.tables.SaveAccount(context.Background(), map[string]any{"id": "acct-admin", "status": "active"}); err != nil {
		return err
	}
	return app.tables.SaveUser(context.Background(), map[string]any{"id": "usr-admin", "email": "admin@medopl.cn", "accountId": "acct-admin", "role": "admin", "status": "active"})
}

func newControlPlaneAppEmpty() *controlPlaneServer {
	tables := newMemoryTableStore()
	return &controlPlaneServer{
		tables:          tables,
		loginRateLimits: map[string]loginFailure{},
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
	for _, account := range app.accountsLocked() {
		if stringValue(account.(map[string]any)["accountId"]) == accountID {
			accounts = append(accounts, account)
		}
	}
	return map[string]any{
		"product":                map[string]any{"name": "OPL Cloud", "console": "OPL Console", "workspace": "OPL Workspace"},
		"billingPolicy":          map[string]any{"holdDays": 7, "priceBasis": "OPL price list"},
		"packages":               packageList(),
		"computePools":           computePools,
		"wallet":                 app.wallet(accountID),
		"account":                app.wallet(accountID),
		"user":                   nil,
		"workspaces":             workspaces,
		"computeAllocations":     rowsAsAnyFromMaps(app.listComputes(accountID)),
		"storageVolumes":         rowsAsAnyFromMaps(app.listStorages(accountID)),
		"storageAttachments":     rowsAsAnyFromMaps(app.listAttachments(accountID)),
		"accounts":               accounts,
		"billingSummary":         app.accountBillingSummary(accountID),
		"billingLedger":          rowsAsAnyFromMaps(app.listLedger(accountID)),
		"walletTransactions":     rowsAsAnyFromMaps(app.listWalletTransactions(accountID)),
		"manualTopups":           rowsAsAnyFromMaps(app.listManualTopups(accountID)),
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

func (app *controlPlaneServer) listWallets(accountID string) []map[string]any {
	rows, err := app.tables.ListWallets(context.Background(), accountID)
	if err != nil {
		return nil
	}
	return rows
}

func (app *controlPlaneServer) walletRecordSet(accountID string) controlPlaneRecordSet {
	rows := app.listWallets(accountID)
	out := controlPlaneRecordSet{}
	for _, row := range rows {
		out[firstNonEmpty(stringValue(row["id"]), stringValue(row["accountId"]))] = cloneMap(row)
	}
	return out
}

func (app *controlPlaneServer) listLedger(accountID string) []map[string]any {
	rows, err := app.tables.ListLedger(context.Background(), accountID)
	if err != nil {
		return nil
	}
	return rows
}

func (app *controlPlaneServer) listWalletTransactions(accountID string) []map[string]any {
	rows, err := app.tables.ListWalletTransactions(context.Background(), accountID)
	if err != nil {
		return nil
	}
	return rows
}

func (app *controlPlaneServer) listManualTopups(accountID string) []map[string]any {
	rows, err := app.tables.ListManualTopups(context.Background(), accountID)
	if err != nil {
		return nil
	}
	return rows
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

func walletTransactionProjections(transactions []clients.WalletTransaction, settlements map[string]clients.ResourceSettlementResult) []map[string]any {
	rows := make([]map[string]any, 0, len(transactions))
	for _, tx := range transactions {
		row := map[string]any{
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
		}
		if settlement, ok := settlements[tx.ID]; ok {
			row["type"] = settlement.ResourceType + "_debit"
			row["metadata"] = settlementMetadata(settlement)
		}
		rows = append(rows, row)
	}
	return rows
}

func settlementMetadata(settlement clients.ResourceSettlementResult) map[string]any {
	metadata := map[string]any{
		"workspaceId":   settlement.WorkspaceID,
		"resourceId":    settlement.ResourceID,
		"settlementId":  settlement.ID,
		"ledgerEntryId": settlement.LedgerEntryID,
	}
	if settlement.ResourceType == "storage" {
		metadata["storageId"] = settlement.ResourceID
	} else {
		metadata["computeAllocationId"] = settlement.ResourceID
	}
	return metadata
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
	return packageRows(defaultPricingCatalog())
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

func countActiveURLs(input controlPlaneRecordSet) int {
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

func resourceDebitTotal(ledger []map[string]any, accountID string, workspaceID string) float64 {
	total := float64(0)
	for _, entry := range ledger {
		if !isResourceDebit(entry) {
			continue
		}
		if accountID != "" && stringValue(entry["accountId"]) != accountID {
			continue
		}
		if workspaceID != "" && stringValue(entry["workspaceId"]) != workspaceID {
			continue
		}
		if amount := amountValue(entry); amount < 0 {
			total += -amount
		}
	}
	return total
}

func isResourceDebit(row map[string]any) bool {
	switch stringValue(row["type"]) {
	case "compute_debit", "storage_debit":
		return true
	default:
		return false
	}
}

func activeHourlyForResource(row map[string]any) float64 {
	if row == nil || billingStatusFor(row) != "active" {
		return 0
	}
	switch stringValue(row["status"]) {
	case "destroyed", "failed", "detached":
		return 0
	}
	return firstPositive(number(row["hourlyPrice"]), number(row["hourlyEstimate"]))
}

func firstPositive(values ...float64) float64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func amountValue(row map[string]any) float64 {
	if amount := number(row["amount"]); amount != 0 {
		return amount
	}
	return float64(number(row["amountCents"])) / 100
}
