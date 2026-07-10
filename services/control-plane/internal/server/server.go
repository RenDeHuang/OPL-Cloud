package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
	"opl-cloud/services/control-plane/internal/handler"
	controlplaneroutes "opl-cloud/services/control-plane/internal/server/routes"
)

func NewServer(service *controlplane.Service) http.Handler {
	handler, err := NewPersistentServer(service, nil)
	if err != nil {
		panic(err)
	}
	return handler
}

func NewPersistentServer(service *controlplane.Service, store StateStore) (http.Handler, error) {
	app, err := newControlPlaneAppWithStore(store)
	if err != nil {
		return nil, err
	}
	if settlementWorkerEnabled() {
		app.startPeriodicSettlementWorker(context.Background(), service, settlementWorkerInterval())
	}
	if providerReconcileWorkerEnabled() {
		app.startProviderReconcileWorker(context.Background(), service, providerReconcileInterval())
	}
	if archiveRetentionWorkerEnabled() {
		app.startArchiveRetentionWorker(context.Background(), archiveRetentionWorkerInterval())
	}
	mux := http.NewServeMux()
	controlplaneroutes.RegisterCore(mux, handler.CoreHandler{Register: func(mux *http.ServeMux) { registerCoreRoutes(mux, app, service) }})
	controlplaneroutes.RegisterAuth(mux, handler.AuthHandler{Register: func(mux *http.ServeMux) { registerAuthRoutes(mux, app) }})
	controlplaneroutes.RegisterState(mux, handler.StateHandler{Register: func(mux *http.ServeMux) { registerStateRoutes(mux, app, service) }})
	controlplaneroutes.RegisterWorkspace(mux, handler.WorkspaceHandler{Register: func(mux *http.ServeMux) { registerWorkspaceRoutes(mux, app, service) }})
	controlplaneroutes.RegisterBilling(mux, handler.BillingHandler{Register: func(mux *http.ServeMux) { registerBillingRoutes(mux, app, service) }})
	controlplaneroutes.RegisterResource(mux, handler.ResourceHandler{Register: func(mux *http.ServeMux) { registerResourceRoutes(mux, app, service) }})
	controlplaneroutes.RegisterSupport(mux, handler.SupportHandler{Register: func(mux *http.ServeMux) { registerSupportRoutes(mux, app) }})
	controlplaneroutes.RegisterAdmin(mux, handler.AdminHandler{Register: func(mux *http.ServeMux) { registerAdminRoutes(mux, app) }})
	registerExecutionRoutes(mux, app, service)
	registerSyncRoutes(mux, app)
	return mux, nil
}

func (app *controlPlaneServer) consoleStatic(w http.ResponseWriter, r *http.Request) {
	if isWorkspaceRequest(r) {
		app.proxyWorkspaceRoot(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}
	dist := consoleDistDir()
	if strings.HasPrefix(r.URL.Path, "/assets/") {
		http.FileServer(http.Dir(dist)).ServeHTTP(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if data, err := os.ReadFile(filepath.Join(dist, "index.html")); err == nil {
		_, _ = w.Write(data)
		return
	}
	_, _ = w.Write([]byte(`<!doctype html><html><head><title>OPL Console</title></head><body><div id="root"></div></body></html>`))
}

func consoleDistDir() string {
	for _, dir := range []string{strings.TrimSpace(os.Getenv("OPL_CONSOLE_DIST_DIR")), "dist", "../../dist", "../../../../dist"} {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "index.html")); err == nil {
			return dir
		}
	}
	return "dist"
}

func (app *controlPlaneServer) protected(requiresAdmin bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		payload, ok := app.session(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "not_authenticated")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if !limitJSONBody(w, r) {
				return
			}
			if r.Header.Get("x-opl-csrf") != stringValue(payload["csrfToken"]) {
				writeError(w, http.StatusForbidden, "csrf_token_invalid")
				return
			}
		}
		user, _ := payload["user"].(map[string]any)
		if requiresAdmin && stringValue(user["role"]) != "admin" {
			writeError(w, http.StatusForbidden, "admin_required")
			return
		}
		next(w, r)
	}
}

func (app *controlPlaneServer) syncRuntimeOperations(w http.ResponseWriter, r *http.Request, service *controlplane.Service) bool {
	operations, err := service.FabricOperations(r.Context())
	if err != nil {
		writeUpstreamError(w)
		return false
	}
	if err := app.rememberRuntimeOperations(operations); err != nil {
		writeError(w, http.StatusInternalServerError, "state_persist_failed")
		return false
	}
	return true
}

func (app *controlPlaneServer) syncLedgerFacts(w http.ResponseWriter, r *http.Request, service *controlplane.Service, accountID string) bool {
	entries, err := service.ListLedgerEntries(r.Context(), accountID)
	if err != nil {
		writeUpstreamError(w)
		return false
	}
	transactions, err := service.ListWalletTransactions(r.Context(), accountID)
	if err != nil {
		writeUpstreamError(w)
		return false
	}
	topups, err := service.ListManualTopUps(r.Context(), accountID)
	if err != nil {
		writeUpstreamError(w)
		return false
	}
	settlements, err := service.ListResourceSettlements(r.Context(), accountID)
	if err != nil {
		writeUpstreamError(w)
		return false
	}
	var wallet clients.Wallet
	if accountID != "" {
		wallet, err = service.Wallet(r.Context(), accountID)
		if err != nil {
			writeUpstreamError(w)
			return false
		}
	}
	if err := app.applyLedgerFacts(accountID, wallet, entries, transactions, topups, settlements); err != nil {
		writeError(w, http.StatusInternalServerError, "state_persist_failed")
		return false
	}
	return true
}

func fabricComputePools(w http.ResponseWriter, r *http.Request, service *controlplane.Service) ([]any, bool) {
	catalog, err := service.FabricCatalog(r.Context())
	if err != nil {
		writeUpstreamError(w)
		return nil, false
	}
	return computePoolsFromFabricCatalog(catalog), true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeUpstreamError(w http.ResponseWriter, causes ...error) {
	for _, cause := range causes {
		if cause != nil {
			log.Printf("upstream request failed: %v", cause)
		}
	}
	writeError(w, http.StatusBadGateway, "upstream_unavailable")
}

const maxJSONBodyBytes int64 = 1 << 20

func limitJSONBody(w http.ResponseWriter, r *http.Request) bool {
	if r.Body == nil {
		return true
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBodyBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json_body")
		return false
	}
	if int64(len(data)) > maxJSONBodyBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "request_body_too_large")
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	return true
}

func writeUserLifecycleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errUserNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, errLastActiveAdmin), errors.Is(err, errUserDeleted):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "state_persist_failed")
	}
}

func writeCreateUserError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errUserExists):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}

func requestIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}

func withOperatorUserID(input map[string]any, userID string) {
	if userID != "" && stringValue(input["operatorUserId"]) == "" {
		input["operatorUserId"] = userID
	}
}

func withSessionUserContext(input map[string]any, user map[string]any, ok bool) {
	if !ok {
		return
	}
	if stringValue(input["userId"]) == "" {
		input["userId"] = stringValue(user["id"])
	}
	if stringValue(input["accountId"]) == "" {
		input["accountId"] = stringValue(user["accountId"])
	}
}

func (app *controlPlaneServer) scopedAccountID(w http.ResponseWriter, r *http.Request, input map[string]any) (string, bool) {
	user, ok := app.sessionUserContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "not_authenticated")
		return "", false
	}
	requested := r.URL.Query().Get("accountId")
	if input != nil {
		requested = firstNonEmpty(stringField(input, "accountId", ""), requested)
	}
	sessionAccount := stringValue(user["accountId"])
	if stringValue(user["role"]) == "admin" {
		return firstNonEmpty(requested, sessionAccount), true
	}
	if sessionAccount == "" || (requested != "" && requested != sessionAccount) {
		writeError(w, http.StatusForbidden, "account_scope_forbidden")
		return "", false
	}
	return sessionAccount, true
}

func decodeJSON(r *http.Request) map[string]any {
	var input map[string]any
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		return map[string]any{}
	}
	return input
}

func stringField(input map[string]any, key string, fallback string) string {
	if value, ok := input[key].(string); ok && strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func numberField(input map[string]any, key string, fallback float64) float64 {
	switch value := input[key].(type) {
	case float64:
		return value
	case int:
		return float64(value)
	case int64:
		return float64(value)
	case int32:
		return float64(value)
	default:
		return fallback
	}
}

func mapField(input map[string]any, key string) map[string]any {
	value, _ := input[key].(map[string]any)
	return cloneMap(value)
}

func confirmed(input map[string]any, key string) bool {
	value, ok := input[key].(bool)
	return ok && value
}

func moneyToCents(input map[string]any) int64 {
	if cents := numberField(input, "amountCents", -1); cents >= 0 {
		return int64(cents)
	}
	return int64(numberField(input, "amount", 0) * 100)
}

func mutationKey(r *http.Request, input map[string]any) string {
	return firstNonEmpty(r.Header.Get("Idempotency-Key"), stringField(input, "idempotencyKey", ""), stringField(input, "sourceEventId", ""), stableID(r.Method, r.URL.Path, time.Now().UTC().String()))
}

func newResourceID(prefix string) string {
	return prefix + "_" + stableID(prefix, time.Now().UTC().Format(time.RFC3339Nano))[:18]
}

func structToMap(value any) map[string]any {
	data, err := json.Marshal(value)
	if err != nil {
		return map[string]any{}
	}
	var output map[string]any
	if err := json.Unmarshal(data, &output); err != nil {
		return map[string]any{}
	}
	return output
}

func computeResponse(row map[string]any) map[string]any {
	if row == nil {
		row = map[string]any{}
	}
	row["ownerAccountId"] = firstNonEmpty(stringValue(row["ownerAccountId"]), stringValue(row["accountId"]))
	row["provider"] = firstNonEmpty(stringValue(row["provider"]), "tencent-tke")
	row["status"] = firstNonEmpty(stringValue(row["status"]), "running")
	row["billingStatus"] = billingStatusFor(row)
	row["cvmInstanceId"] = firstNonEmpty(stringValue(row["cvmInstanceId"]), stringValue(row["instanceId"]))
	if holdCents := numberField(row, "holdAmountCents", 0); holdCents > 0 {
		row["holdAmount"] = holdCents / 100
	}
	if serviceName := stringValue(row["serviceName"]); serviceName != "" {
		row["runtime"] = map[string]any{"serviceName": serviceName, "service": "service/" + serviceName}
	}
	return row
}

func storageResponse(row map[string]any) map[string]any {
	if row == nil {
		row = map[string]any{}
	}
	row["ownerAccountId"] = firstNonEmpty(stringValue(row["ownerAccountId"]), stringValue(row["accountId"]))
	row["provider"] = firstNonEmpty(stringValue(row["provider"]), "tencent-tke")
	if stringValue(row["status"]) == "ready" {
		row["status"] = "available"
	}
	row["status"] = firstNonEmpty(stringValue(row["status"]), "available")
	row["billingStatus"] = billingStatusFor(row)
	if holdCents := numberField(row, "holdAmountCents", 0); holdCents > 0 {
		row["holdAmount"] = holdCents / 100
	}
	if numberField(row, "sizeGb", 0) == 0 {
		row["sizeGb"] = 10
	}
	return row
}

func attachmentResponse(row map[string]any, input map[string]any) map[string]any {
	if row == nil {
		row = map[string]any{}
	}
	row["computeAllocationId"] = firstNonEmpty(stringValue(row["computeAllocationId"]), stringValue(row["computeId"]), stringField(input, "computeAllocationId", ""))
	row["storageId"] = firstNonEmpty(stringValue(row["storageId"]), stringValue(row["volumeId"]), stringField(input, "storageId", ""))
	row["mountPath"] = firstNonEmpty(stringValue(row["mountPath"]), stringField(input, "mountPath", "/data"))
	row["provider"] = firstNonEmpty(stringValue(row["provider"]), "tencent-tke")
	row["status"] = firstNonEmpty(stringValue(row["status"]), "attached")
	return row
}

func workspaceResponse(row map[string]any) map[string]any {
	if row == nil {
		row = map[string]any{}
	}
	row["ownerAccountId"] = firstNonEmpty(stringValue(row["ownerAccountId"]), stringValue(row["accountId"]))
	row["ownerUserId"] = firstNonEmpty(stringValue(row["ownerUserId"]), stringValue(row["ownerId"]))
	row["state"] = firstNonEmpty(stringValue(row["state"]), stringValue(row["status"]))
	row["currentComputeAllocationId"] = firstNonEmpty(stringValue(row["currentComputeAllocationId"]), stringValue(row["computeAllocationId"]))
	row["currentAttachmentId"] = firstNonEmpty(stringValue(row["currentAttachmentId"]), stringValue(row["attachmentId"]))
	if serviceName := stringValue(row["runtimeServiceName"]); serviceName != "" {
		row["runtime"] = map[string]any{"serviceName": serviceName}
	}
	runtimeStatus := firstNonEmpty(stringValue(valueOrNil(row, "runtime", "status")), stringValue(row["runtimeStatus"]), stringValue(row["state"]))
	row["runtimeStatus"] = runtimeStatus
	access, _ := row["access"].(map[string]any)
	access = cloneMap(access)
	access["tokenStatus"] = firstNonEmpty(stringValue(access["tokenStatus"]), "active")
	access["requiresLogin"] = false
	if username := stringValue(row["runtimeUsername"]); username != "" {
		access["account"] = username
		access["username"] = username
	}
	if password := stringValue(row["runtimePassword"]); password != "" {
		access["password"] = password
	}
	if status := stringValue(row["credentialStatus"]); status != "" {
		access["credentialStatus"] = status
	}
	if version := stringValue(row["credentialVersion"]); version != "" {
		access["credentialVersion"] = version
	}
	if secretRef := stringValue(row["credentialSecretRef"]); secretRef != "" {
		access["secretRef"] = secretRef
	}
	openable := access["tokenStatus"] == "active" && (runtimeStatus == "running" || runtimeStatus == "ready" || runtimeStatus == "available" || runtimeStatus == "active")
	if state := stringValue(row["state"]); state == "suspended" || state == "data_deleted" || state == "storage_missing" || state == "destroyed" {
		openable = false
	}
	row["openable"] = openable
	switch {
	case openable:
		row["accessState"] = "available"
	case access["tokenStatus"] == "active" && stringValue(row["state"]) != "data_deleted":
		row["accessState"] = "distributing"
	default:
		row["accessState"] = "disabled"
	}
	row["access"] = access
	return row
}

func billingStatusFor(row map[string]any) string {
	status := stringValue(row["status"])
	if isTerminalResourceStatus(status) {
		return "stopped"
	}
	if billingStatus := stringValue(row["billingStatus"]); billingStatus != "" && !(billingStatus == "pending" && status != "pending" && status != "provisioning") {
		return billingStatus
	}
	switch status {
	case "detached", "failed":
		return "stopped"
	default:
		return "active"
	}
}

func isTerminalResourceStatus(status string) bool {
	switch status {
	case "destroyed", "external_deleted", "deleted", "missing":
		return true
	default:
		return false
	}
}

func mergeMaps(base map[string]any, updates map[string]any) map[string]any {
	output := cloneMap(base)
	for key, value := range updates {
		if !emptyMergeValue(value) {
			output[key] = value
		}
	}
	return output
}

func emptyMergeValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return typed == ""
	default:
		return false
	}
}

func manualTopUpResponse(result clients.ManualTopUpResult) map[string]any {
	return map[string]any{
		"id":                  result.TopUp.ID,
		"idempotent":          result.Replayed,
		"targetAccountId":     result.TopUp.AccountID,
		"amount":              float64(result.TopUp.AmountCents) / 100,
		"amountCents":         result.TopUp.AmountCents,
		"operatorUserId":      result.TopUp.OperatorUserID,
		"ledgerEntryId":       result.LedgerEntry.ID,
		"walletTransactionId": result.WalletTransaction.ID,
		"balance":             float64(result.Wallet.BalanceCents) / 100,
		"frozen":              float64(result.Wallet.FrozenCents) / 100,
		"available":           float64(result.Wallet.AvailableCents) / 100,
		"wallet":              result.Wallet,
		"status":              "completed",
	}
}

func settlementAmountCents(input map[string]any) int64 {
	if cents := numberField(input, "amountCents", -1); cents >= 0 {
		return int64(cents)
	}
	if amount := numberField(input, "amount", -1); amount >= 0 {
		return int64(amount * 100)
	}
	hours := numberField(input, "hours", 1)
	return int64(hours * 100)
}

func destroyResourceInput(id string, row map[string]any) controlplane.DestroyResourceInput {
	return controlplane.DestroyResourceInput{
		ID:              id,
		AccountID:       firstNonEmpty(stringValue(row["accountId"]), stringValue(row["ownerAccountId"]), "acct-local"),
		WorkspaceID:     stringValue(row["workspaceId"]),
		HoldID:          stringValue(row["holdId"]),
		HoldAmountCents: int64(numberField(row, "holdAmountCents", 0)),
	}
}

func computeHoldAmountCents(packageID string) int64 {
	return computeHoldAmountCentsFromCatalog(defaultPricingCatalog(), packageID)
}

func storageHoldAmountCents(packageID string, sizeGB float64) int64 {
	return storageHoldAmountCentsFromCatalog(defaultPricingCatalog(), packageID, sizeGB)
}

func packageByID(packageID string) map[string]any {
	for _, plan := range packageList() {
		row, _ := plan.(map[string]any)
		if stringValue(row["id"]) == packageID {
			return row
		}
	}
	first, _ := packageList()[0].(map[string]any)
	return first
}

func cents(amount float64) int64 {
	return int64(math.Round(amount * 100))
}

func priceField(plan map[string]any, key string) float64 {
	price, _ := plan["price"].(map[string]any)
	return numberField(price, key, 0)
}

func settlementResponse(result clients.ResourceSettlementResult) map[string]any {
	return map[string]any{
		"id":                      result.ID,
		"accountId":               result.AccountID,
		"workspaceId":             result.WorkspaceID,
		"resourceType":            result.ResourceType,
		"resourceId":              result.ResourceID,
		"amount":                  float64(result.AmountCents) / 100,
		"amountCents":             result.AmountCents,
		"status":                  result.Status,
		"ledgerEntryId":           result.LedgerEntryID,
		"walletTransactionId":     result.WalletTransactionID,
		"pricingVersion":          result.PricingVersion,
		"priceSnapshot":           result.PriceSnapshot,
		"usagePeriodStart":        result.UsagePeriodStart,
		"usagePeriodEnd":          result.UsagePeriodEnd,
		"quantity":                result.Quantity,
		"unit":                    result.Unit,
		"providerCostEvidenceRef": result.ProviderCostEvidenceRef,
		"wallet":                  result.Wallet,
	}
}

func completeSettlementResult(result clients.ResourceSettlementResult, input controlplane.ResourceSettlementInput) clients.ResourceSettlementResult {
	result.AccountID = firstNonEmpty(result.AccountID, input.AccountID)
	result.WorkspaceID = firstNonEmpty(result.WorkspaceID, input.WorkspaceID)
	result.ResourceType = firstNonEmpty(result.ResourceType, input.ResourceType)
	result.ResourceID = firstNonEmpty(result.ResourceID, input.ResourceID)
	if result.AmountCents == 0 {
		result.AmountCents = input.AmountCents
	}
	result.Currency = firstNonEmpty(result.Currency, input.Currency)
	result.PricingVersion = firstNonEmpty(result.PricingVersion, input.PricingVersion)
	if len(result.PriceSnapshot) == 0 {
		result.PriceSnapshot = cloneMap(input.PriceSnapshot)
	}
	result.UsagePeriodStart = firstNonEmpty(result.UsagePeriodStart, input.UsagePeriodStart)
	result.UsagePeriodEnd = firstNonEmpty(result.UsagePeriodEnd, input.UsagePeriodEnd)
	if result.Quantity == 0 {
		result.Quantity = input.Quantity
	}
	result.Unit = firstNonEmpty(result.Unit, input.Unit)
	result.ProviderCostEvidenceRef = firstNonEmpty(result.ProviderCostEvidenceRef, input.ProviderCostEvidenceRef)
	result.Wallet.AccountID = firstNonEmpty(result.Wallet.AccountID, result.AccountID)
	result.Wallet.Currency = firstNonEmpty(result.Wallet.Currency, result.Currency)
	return result
}

func reconciliationResponse(result clients.ReconciliationResult) map[string]any {
	return map[string]any{
		"id":     result.ID,
		"status": result.Status,
		"guard": map[string]any{
			"status":             result.Status,
			"blockNewWorkspaces": result.BlockNewWorkspaces,
			"reason":             result.Reason,
		},
		"report": result.Report,
	}
}

func workspaceRuntimeStatusResponse(runtime clients.WorkspaceRuntime) map[string]any {
	ready := runtime.Ready
	checks := runtime.Checks
	if len(checks) == 0 {
		ready = runtime.Status == "running"
		checks = []any{map[string]any{"name": "fabric_runtime_running", "ok": ready}}
	}
	body := map[string]any{
		"provider":    "tencent-tke",
		"workspaceId": runtime.WorkspaceID,
		"runtimeId":   runtime.ID,
		"url":         runtime.URL,
		"serviceName": runtime.ServiceName,
		"status":      runtime.Status,
		"ready":       ready,
		"checks":      checks,
	}
	if runtime.Access.Username != "" || runtime.Access.Password != "" {
		body["access"] = map[string]any{
			"account":           runtime.Access.Username,
			"username":          runtime.Access.Username,
			"password":          runtime.Access.Password,
			"credentialStatus":  runtime.Access.CredentialStatus,
			"credentialVersion": runtime.Access.CredentialVersion,
			"secretRef":         runtime.Access.SecretRef,
		}
	}
	return body
}
