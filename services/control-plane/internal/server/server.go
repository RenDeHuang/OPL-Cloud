package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

func NewServer(service *controlplane.Service) http.Handler {
	handler, err := NewPersistentServer(service, nil)
	if err != nil {
		panic(err)
	}
	return handler
}

type controlPlaneHTTPHandler struct {
	app     *controlPlaneServer
	next    http.Handler
	service *controlplane.Service
}

func (h *controlPlaneHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; frame-ancestors 'none'; object-src 'none'; form-action 'self'")
	w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if retiredConsoleAPI(r.Method, r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	h.next.ServeHTTP(w, r)
}

func NewPersistentServer(service *controlplane.Service, store StateStore) (http.Handler, error) {
	app, err := newControlPlaneAppWithStore(store)
	if err != nil {
		return nil, err
	}
	if err := app.ensureBootstrapAdmin(context.Background(), service); err != nil {
		return nil, err
	}
	if monthlyBillingWorkerEnabled() {
		app.startMonthlyBillingWorker(context.Background(), service, monthlyBillingWorkerInterval())
	}
	if providerReconcileWorkerEnabled() {
		app.startProviderReconcileWorker(context.Background(), service, providerReconcileInterval())
	}
	if archiveRetentionWorkerEnabled() {
		app.startArchiveRetentionWorker(context.Background(), archiveRetentionWorkerInterval())
	}
	mux := http.NewServeMux()
	registerCoreRoutes(mux, app, service)
	registerAuthRoutes(mux, app, service)
	registerStateRoutes(mux, app, service)
	registerGatewayRoutes(mux, app, service)
	registerWorkspaceRoutes(mux, app, service)
	registerWorkspaceLaunchRoutes(mux, app, service)
	registerBillingRoutes(mux, app, service)
	registerAnnouncementRoutes(mux, app)
	registerSupportRoutes(mux, app)
	registerAdminRoutes(mux, app, service)
	registerProviderAcceptanceRoutes(mux, app, service)
	registerExecutionRoutes(mux, app, service)
	return &controlPlaneHTTPHandler{app: app, next: mux, service: service}, nil
}

func retiredConsoleAPI(method, path string) bool {
	if method == http.MethodPost && path == "/api/workspaces" {
		return true
	}
	switch path {
	case "/api/me", "/api/overview", "/api/gateway/summary", "/api/billing/summary",
		"/api/gateway/usage", "/api/gateway/usage/stats", "/api/gateway/keys/opl-workspace/reveal",
		"/api/workspaces/runtime-status", "/api/operator/summary", "/api/operator/accounts/invitations":
		return true
	}
	if path == "/api/workspace-backups" || strings.HasPrefix(path, "/api/workspace-backups/") || strings.HasPrefix(path, "/api/payment") || strings.HasPrefix(path, "/api/orders") ||
		strings.HasPrefix(path, "/api/api-keys") || strings.HasPrefix(path, "/api/keys") ||
		strings.HasPrefix(path, "/api/users") || strings.HasPrefix(path, "/api/compute-allocations") ||
		strings.HasPrefix(path, "/api/storage-volumes") || strings.HasPrefix(path, "/api/storage-attachments") ||
		strings.HasPrefix(path, "/api/compute-pools") {
		return true
	}
	if strings.HasPrefix(path, "/api/workspaces/") && strings.HasSuffix(path, "/gateway-secret/rotate") {
		return true
	}
	if strings.HasPrefix(path, "/api/gateway/keys/") && path != "/api/gateway/keys/opl-workspace/reveal" && !currentGatewayKeyAPI(method, path) {
		return true
	}
	if !strings.HasPrefix(path, "/api/workspaces/") {
		return false
	}
	for _, segment := range strings.Split(strings.Trim(path, "/"), "/")[3:] {
		switch segment {
		case "backups", "recovery", "sync", "transfers", "transfer", "contents":
			return true
		}
	}
	return false
}

func currentGatewayKeyAPI(method, path string) bool {
	segments := strings.Split(strings.TrimPrefix(path, "/api/gateway/keys/"), "/")
	if len(segments) < 1 {
		return false
	}
	keyID, err := strconv.ParseInt(segments[0], 10, 64)
	if err != nil || keyID <= 0 || strconv.FormatInt(keyID, 10) != segments[0] {
		return false
	}
	if len(segments) == 1 {
		return method == http.MethodGet || method == http.MethodPatch || method == http.MethodDelete
	}
	if len(segments) != 2 {
		return false
	}
	return method == http.MethodPost && segments[1] == "reveal" || method == http.MethodGet && (segments[1] == "usage" || segments[1] == "usage-summary")
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
		rel := strings.TrimPrefix(r.URL.Path, "/assets/")
		if !filepath.IsLocal(rel) || !serveConsoleFile(w, r, filepath.Join(dist, "assets", rel), "public,max-age=31536000,immutable") {
			http.NotFound(w, r)
		}
		return
	}
	if r.URL.Path == "/opl-app-icon.png" {
		if !serveConsoleFile(w, r, filepath.Join(dist, "opl-app-icon.png"), "public,max-age=86400") {
			http.NotFound(w, r)
		}
		return
	}
	if serveConsoleFile(w, r, filepath.Join(dist, "index.html"), "no-cache") {
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fallback := []byte(`<!doctype html><html><head><title>OPL Console</title></head><body><div id="root"></div></body></html>`)
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(fallback))
}

func serveConsoleFile(w http.ResponseWriter, r *http.Request, path string, cacheControl string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	w.Header().Set("Cache-Control", cacheControl)
	if contentType := mime.TypeByExtension(filepath.Ext(path)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if !compressibleConsoleAsset(path) {
		http.ServeContent(w, r, info.Name(), info.ModTime(), file)
		return true
	}
	w.Header().Add("Vary", "Accept-Encoding")
	if r.Header.Get("Range") != "" || !acceptsGzip(r.Header.Get("Accept-Encoding")) {
		http.ServeContent(w, r, info.Name(), info.ModTime(), file)
		return true
	}
	var compressed bytes.Buffer
	zipper := gzip.NewWriter(&compressed)
	if _, err := io.Copy(zipper, file); err != nil {
		_ = zipper.Close()
		http.Error(w, "static asset read failed", http.StatusInternalServerError)
		return true
	}
	if err := zipper.Close(); err != nil {
		http.Error(w, "static asset compression failed", http.StatusInternalServerError)
		return true
	}
	w.Header().Set("Content-Encoding", "gzip")
	http.ServeContent(w, r, info.Name(), info.ModTime(), bytes.NewReader(compressed.Bytes()))
	return true
}

func compressibleConsoleAsset(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".css", ".js", ".json", ".map", ".mjs", ".svg", ".txt", ".webmanifest", ".xml":
		return true
	default:
		return false
	}
}

func acceptsGzip(value string) bool {
	for _, item := range strings.Split(value, ",") {
		parts := strings.Split(item, ";")
		if !strings.EqualFold(strings.TrimSpace(parts[0]), "gzip") {
			continue
		}
		for _, part := range parts[1:] {
			name, raw, ok := strings.Cut(part, "=")
			if !ok || !strings.EqualFold(strings.TrimSpace(name), "q") {
				continue
			}
			quality, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
			return err == nil && quality > 0
		}
		return true
	}
	return false
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
		payload, state := app.session(r)
		if state != sessionAuthenticated {
			if state == sessionReauthenticationRequired {
				http.SetCookie(w, sessionCookie("", -1))
				writeError(w, http.StatusUnauthorized, "reauthentication_required")
				return
			}
			writeError(w, http.StatusUnauthorized, "not_authenticated")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if r.Header.Get("Content-Type") != "application/octet-stream" && !limitJSONBody(w, r) {
				return
			}
			if r.Header.Get("x-opl-csrf") != stringValue(payload["csrfToken"]) {
				writeError(w, http.StatusForbidden, "csrf_token_invalid")
				return
			}
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/auth/me" {
			w.Header().Set("x-opl-csrf-token", stringValue(payload["csrfToken"]))
		}
		user, _ := payload["user"].(map[string]any)
		if requiresAdmin && !isOperatorUser(user) {
			writeError(w, http.StatusForbidden, "admin_required")
			return
		}
		if !requiresAdmin && !isOperatorUser(user) {
			active, err := app.hasActiveCustomerMembership(r.Context(), user)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "state_read_failed")
				return
			}
			if !active {
				writeError(w, http.StatusForbidden, "organization_membership_required")
				return
			}
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
		var fabricErr *clients.FabricHTTPError
		if errors.As(cause, &fabricErr) && fabricErr.StatusCode == http.StatusConflict {
			writeError(w, http.StatusConflict, "upstream_conflict")
			return
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
	case errors.Is(err, errUserExists), errors.Is(err, errAccountIdentityConflict), errors.Is(err, errSub2APIAccountMappingConflict), errors.Is(err, errMembershipExists), errors.Is(err, errMembershipAccountMismatch):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, errSub2APIUserMappingUnverified):
		writeError(w, http.StatusBadGateway, err.Error())
	case errors.Is(err, errCallerSuppliedSub2APIUserID), errors.Is(err, errInvalidRole), errors.Is(err, errInvalidEmail), errors.Is(err, errInvalidAccountID), errors.Is(err, errMissingPassword), errors.Is(err, errWeakPassword):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "state_persist_failed")
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

func requiredMutationKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
		return "", false
	}
	return key, true
}

func newResourceID(prefix string) string {
	return prefix + "_" + stableID(prefix, time.Now().UTC().Format(time.RFC3339Nano))[:18]
}

func resourceIDForMutation(prefix, accountID, key string) string {
	return prefix + "_" + stableID(prefix, accountID, key)[:18]
}

func primaryWorkspaceID(accountID string) string {
	return resourceIDForMutation("ws", accountID, "primary")
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
	response := map[string]any{}
	for _, key := range []string{
		"id", "accountId", "name", "url", "state", "status", "packageId", "storageGb", "storageId",
		"computeAllocationId", "attachmentId", "runtimeId", "createdAt", "updatedAt",
		"autoRenew", "authorizedBy", "authorizedAt", "priceVersion", "currency", "billingUnit",
		"computeUsdMicros", "storageUsdMicros", "totalUsdMicros", "periodStart", "paidThrough",
		"nextRenewalAt", "billingAnchorDay", "renewalStatus", "manualReviewReason",
	} {
		if value, ok := row[key]; ok {
			response[key] = value
		}
	}
	response["ownerAccountId"] = firstNonEmpty(stringValue(row["ownerAccountId"]), stringValue(row["accountId"]))
	response["ownerUserId"] = firstNonEmpty(stringValue(row["ownerUserId"]), stringValue(row["ownerId"]))
	response["ownerId"] = response["ownerUserId"]
	response["state"] = firstNonEmpty(stringValue(row["state"]), stringValue(row["status"]))
	response["status"] = firstNonEmpty(stringValue(row["status"]), stringValue(response["state"]))
	response["currentComputeAllocationId"] = stringValue(row["currentComputeAllocationId"])
	response["currentAttachmentId"] = stringValue(row["currentAttachmentId"])
	runtime := cloneMap(mapField(row, "runtime"))
	serviceName := firstNonEmpty(stringValue(runtime["serviceName"]), stringValue(row["runtimeServiceName"]))
	runtimeResponse := map[string]any{}
	if serviceName != "" {
		runtimeResponse["serviceName"] = serviceName
	}
	runtimeStatus := firstNonEmpty(stringValue(runtime["status"]), stringValue(row["runtimeStatus"]), stringValue(response["state"]))
	runtimeResponse["status"] = runtimeStatus
	if ready, ok := row["runtimeReady"].(bool); ok {
		runtimeResponse["ready"] = ready
	} else if ready, ok := runtime["ready"].(bool); ok {
		runtimeResponse["ready"] = ready
	}
	response["runtime"] = runtimeResponse
	response["runtimeStatus"] = runtimeStatus
	access, _ := row["access"].(map[string]any)
	access = cloneMap(access)
	accessResponse := map[string]any{}
	if username := stringValue(row["runtimeUsername"]); username != "" {
		accessResponse["account"], accessResponse["username"] = username, username
	} else if username := firstNonEmpty(stringValue(access["username"]), stringValue(access["account"])); username != "" {
		accessResponse["account"], accessResponse["username"] = username, username
	}
	if status := firstNonEmpty(stringValue(row["credentialStatus"]), stringValue(access["credentialStatus"])); status != "" {
		accessResponse["credentialStatus"] = status
	}
	if version := firstNonEmpty(stringValue(row["credentialVersion"]), stringValue(access["credentialVersion"])); version != "" {
		accessResponse["credentialVersion"] = version
	}
	state := stringValue(response["state"])
	openable := state == "running" || state == "ready" || state == "available" || state == "active"
	if ready, ok := runtimeResponse["ready"].(bool); ok && !ready {
		openable = false
	}
	if state == "suspended" || state == "data_deleted" || state == "unrecoverable" || state == "storage_missing" || state == "destroyed" {
		openable = false
	}
	response["openable"] = openable
	switch {
	case openable:
		response["accessState"] = "available"
	case state == "suspended" || state == "data_deleted" || state == "unrecoverable" || state == "destroyed":
		response["accessState"] = "disabled"
	default:
		response["accessState"] = "distributing"
	}
	response["access"] = accessResponse
	return response
}

func billingStatusFor(row map[string]any) string {
	status := stringValue(row["status"])
	if isTerminalResourceStatus(status) {
		return "stopped"
	}
	if billingStatus := stringValue(row["billingStatus"]); billingStatus != "" {
		return billingStatus
	}
	switch status {
	case "detached", "failed":
		return "stopped"
	default:
		return "pending"
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

func workspaceRuntimeStatusResponse(runtime clients.WorkspaceRuntime, workspaceID string) (map[string]any, bool) {
	if runtime.WorkspaceID != workspaceID || runtime.Status == "" || runtime.Checks == nil {
		return nil, false
	}
	switch runtime.Status {
	case "running", "unready":
		if runtime.ID == "" || runtime.URL == "" || runtime.ServiceName == "" {
			return nil, false
		}
	case "not_found", "destroyed":
	default:
		return nil, false
	}
	checks := make([]any, 0, len(runtime.Checks))
	for _, raw := range runtime.Checks {
		check, ok := raw.(map[string]any)
		name, nameOK := check["name"].(string)
		ready, readyOK := check["ok"].(bool)
		if !ok || !nameOK || strings.TrimSpace(name) == "" || !readyOK {
			return nil, false
		}
		checks = append(checks, map[string]any{"name": name, "ok": ready})
	}
	body := map[string]any{
		"workspaceId": runtime.WorkspaceID, "status": runtime.Status, "ready": runtime.Ready, "checks": checks,
	}
	if runtime.ID != "" {
		body["runtimeId"] = runtime.ID
	}
	if runtime.URL != "" {
		body["url"] = runtime.URL
	}
	if runtime.ServiceName != "" {
		body["serviceName"] = runtime.ServiceName
	}
	if runtime.Access.Username != "" || runtime.Access.CredentialStatus != "" || runtime.Access.CredentialVersion != "" {
		access := map[string]any{}
		if runtime.Access.Username != "" {
			access["username"] = runtime.Access.Username
		}
		if runtime.Access.CredentialStatus != "" {
			access["credentialStatus"] = runtime.Access.CredentialStatus
		}
		if runtime.Access.CredentialVersion != "" {
			access["credentialVersion"] = runtime.Access.CredentialVersion
		}
		body["access"] = access
	}
	return body, true
}

func workspaceRuntimeCredentialResponse(runtime clients.WorkspaceRuntime) map[string]any {
	return map[string]any{
		"workspaceId": runtime.WorkspaceID,
		"access": map[string]any{
			"account":           runtime.Access.Username,
			"username":          runtime.Access.Username,
			"password":          runtime.Access.Password,
			"credentialStatus":  runtime.Access.CredentialStatus,
			"credentialVersion": runtime.Access.CredentialVersion,
		},
	}
}
