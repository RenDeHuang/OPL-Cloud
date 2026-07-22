package server

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

var billingReviewEvidenceRefPattern = regexp.MustCompile(`^case-[0-9]{8}-[a-z0-9]{3,16}$`)

func registerAdminRoutes(mux *http.ServeMux, app *controlPlaneServer, service *controlplane.Service) {
	mux.HandleFunc("POST /api/operator/accounts/{accountId}/wallet-adjustments", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		app.createWalletAdjustment(w, r, service)
	}))
	mux.HandleFunc("GET /api/operator/wallet-adjustments/{operationId}", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		app.getWalletAdjustment(w, r)
	}))
	mux.HandleFunc("GET /api/operator/accounts", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		page, pageSize, ok := operatorPagination(w, r)
		if !ok {
			return
		}
		data, status, err := app.operatorAccountPage(r.Context(), service, page, pageSize)
		if err != nil {
			writeSourceEnvelope(w, http.StatusBadGateway, "control-plane+sub2api", "unavailable", nil)
			return
		}
		writeSourceEnvelope(w, http.StatusOK, "control-plane+sub2api", status, data)
	}))
	mux.HandleFunc("GET /api/operator/overview", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		data, err := app.operatorOverview(r.Context(), service)
		if err != nil {
			writeSourceEnvelope(w, http.StatusBadGateway, "control-plane", "unavailable", nil)
			return
		}
		writeSourceEnvelope(w, http.StatusOK, "control-plane", "available", data)
	}))
	mux.HandleFunc("GET /api/operator/workspaces", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		page, pageSize, ok := operatorPagination(w, r)
		if !ok {
			return
		}
		data, status, err := app.operatorWorkspacePage(r.Context(), service, page, pageSize)
		if err != nil {
			writeSourceEnvelope(w, http.StatusInternalServerError, "control-plane+fabric+sub2api", "unavailable", nil)
			return
		}
		writeSourceEnvelope(w, http.StatusOK, "control-plane+fabric+sub2api", status, data)
	}))
	mux.HandleFunc("GET /api/operator/workspaces/{workspaceId}", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		data, found, err := app.operatorWorkspaceDetail(r.Context(), service, strings.TrimSpace(r.PathValue("workspaceId")))
		if err != nil {
			writeSourceEnvelope(w, http.StatusInternalServerError, "control-plane+fabric+ledger", "unavailable", nil)
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, "workspace_not_found")
			return
		}
		writeSourceEnvelope(w, http.StatusOK, "control-plane+fabric+ledger", "available", data)
	}))
	mux.HandleFunc("GET /api/operator/reconciliation", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		page, pageSize, ok := operatorPagination(w, r)
		if !ok {
			return
		}
		data, status, err := app.operatorReconciliationPage(r.Context(), page, pageSize)
		if err != nil {
			writeSourceEnvelope(w, http.StatusInternalServerError, "control-plane", "unavailable", nil)
			return
		}
		writeSourceEnvelope(w, http.StatusOK, "control-plane", status, data)
	}))
	mux.HandleFunc("GET /api/operator/health", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		writeSourceEnvelope(w, http.StatusOK, "control-plane", "available", app.operatorHealth(r.Context(), service))
	}))
	mux.HandleFunc("POST /api/operator/accounts", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		key, ok := requiredMutationKey(w, r)
		if !ok {
			return
		}
		input := decodeJSON(r)
		if !operatorProvisionShapeValid(input) {
			writeError(w, http.StatusBadRequest, "invalid_provision")
			return
		}
		email, err := canonicalEmail(stringValue(input["email"]))
		if err != nil {
			writeCreateUserError(w, err)
			return
		}
		accountID := "acct-" + stableID("account", email)[:18]
		user, err := app.createUser(r.Context(), service, map[string]any{"email": email, "password": input["password"], "accountId": accountID, "role": "owner"})
		if err != nil {
			writeCreateUserError(w, err)
			return
		}
		result := map[string]any{"operationId": "account-provision-" + stableID(key, email)[:18], "accountId": accountID, "status": "succeeded"}
		if err := app.appendAuditEvent(r, "account.provision", "account", accountID, accountID, nil, map[string]any{"userId": user["id"], "email": email}, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusCreated, result)
	}))
	mux.HandleFunc("POST /api/operator/accounts/{accountId}/disable", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		key, ok := requiredMutationKey(w, r)
		if !ok {
			return
		}
		accountID := strings.TrimSpace(r.PathValue("accountId"))
		input := decodeJSON(r)
		if !validAccountID(accountID) || !operatorDisableShapeValid(input, accountID) {
			writeError(w, http.StatusBadRequest, "invalid_account_disable")
			return
		}
		accounts, err := app.tables.ListAccounts(r.Context(), accountID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		account := findRecord(accounts, accountID)
		if account == nil {
			writeError(w, http.StatusNotFound, "account_not_found")
			return
		}
		withOperatorUserID(input, app.sessionUserID(r))
		input["userId"] = stringValue(account["ownerUserId"])
		user, err := app.disableUser(input)
		if err != nil {
			writeUserLifecycleError(w, err)
			return
		}
		result := map[string]any{"operationId": "account-disable-" + stableID(key, accountID)[:18], "accountId": accountID, "status": "succeeded"}
		if err := app.appendAuditEvent(r, "account.disable", "account", accountID, accountID, nil, map[string]any{"userId": user["id"], "reason": input["reason"]}, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}))
	mux.HandleFunc("GET /api/operator/archive", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		result, err := app.archiveState(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "archive_state_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}))
	mux.HandleFunc("POST /api/operator/archive-terminal-resources", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		if !confirmed(input, "confirm") {
			writeError(w, http.StatusBadRequest, "confirmation_required")
			return
		}
		result, err := app.archiveTerminalResources(r.Context(), input)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "operator.archive_terminal_resources", "archive_job", stringValue(result["id"]), "", nil, result, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}))
	mux.HandleFunc("POST /api/operator/workspace-launches/{operationId}/recover", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		key, ok := requiredMutationKey(w, r)
		if !ok {
			return
		}
		input := decodeJSON(r)
		operationID := strings.TrimSpace(r.PathValue("operationId"))
		if !workspaceLaunchRecoveryShapeValid(input) || operationID == "" || stringValue(input["billingOperationId"]) != operationID || !validBillingReviewOpaqueID(key) {
			writeError(w, http.StatusBadRequest, errInvalidBillingReview.Error())
			return
		}
		evidenceRef := stringValue(input["evidenceRef"])
		if !validBillingReviewEvidenceRef(evidenceRef) {
			writeError(w, http.StatusBadRequest, "invalid_evidence_ref")
			return
		}
		resolution := billingReviewResolutionInput{
			ResourceType: "workspace_launch", ResourceID: operationID, AccountID: stringValue(input["accountId"]), BillingOperationID: operationID,
			EvidenceRef: evidenceRef, IdempotencyKey: key, Reviewer: app.sessionUserID(r),
		}
		result, err := app.recoverWorkspaceLaunchReview(r.Context(), service, resolution)
		if err != nil {
			writeBillingReviewResolutionError(w, err)
			return
		}
		audit := app.auditEvent(r, "workspace.launch.recover", "workspace", stringValue(result["workspaceId"]), resolution.AccountID, nil, mergeMaps(result, map[string]any{"evidenceRef": evidenceRef}), stringValue(result["status"]))
		audit["id"] = "audit-" + stableID("workspace.launch.recover", operationID, key)[:12]
		if err := app.tables.SaveAuditEvent(r.Context(), audit); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}))
	mux.HandleFunc("POST /api/operator/billing-reviews/{resourceType}/{id}/resolve", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		if !billingReviewRequestShapeValid(input) {
			writeError(w, http.StatusBadRequest, errInvalidBillingReview.Error())
			return
		}
		key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if key == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		if !validBillingReviewOpaqueID(key) {
			writeError(w, http.StatusBadRequest, "invalid_idempotency_key")
			return
		}
		evidenceRef := strings.TrimSpace(stringValue(input["evidenceRef"]))
		if !validBillingReviewEvidenceRef(evidenceRef) {
			writeError(w, http.StatusBadRequest, "invalid_evidence_ref")
			return
		}
		resolution := billingReviewResolutionInput{
			ResourceType: strings.TrimSpace(r.PathValue("resourceType")), ResourceID: strings.TrimSpace(r.PathValue("id")),
			AccountID: strings.TrimSpace(stringValue(input["accountId"])), BillingOperationID: strings.TrimSpace(stringValue(input["billingOperationId"])),
			Decision: strings.TrimSpace(stringValue(input["decision"])), EvidenceRef: evidenceRef, IdempotencyKey: key, Reviewer: app.sessionUserID(r),
		}
		var result map[string]any
		var err error
		if resolution.ResourceType == "workspace" {
			result, err = app.resolveWorkspaceRenewalReview(r.Context(), service, resolution)
		} else {
			result, err = app.resolveMonthlyBillingReview(r.Context(), service, resolution)
		}
		if err != nil {
			writeBillingReviewResolutionError(w, err)
			return
		}
		if err := app.appendBillingReviewResolutionAudit(r, key, result); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}))
}

func operatorPagination(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	page, pageSize := 1, 20
	var err error
	if raw := strings.TrimSpace(r.URL.Query().Get("page")); raw != "" {
		page, err = strconv.Atoi(raw)
	}
	if err == nil {
		if raw := strings.TrimSpace(r.URL.Query().Get("pageSize")); raw != "" {
			pageSize, err = strconv.Atoi(raw)
		}
	}
	if err != nil || page <= 0 || pageSize <= 0 || pageSize > 50 {
		writeError(w, http.StatusBadRequest, "invalid_pagination")
		return 0, 0, false
	}
	return page, pageSize, true
}

func operatorProvisionShapeValid(input map[string]any) bool {
	if len(input) < 2 || len(input) > 3 {
		return false
	}
	for key := range input {
		if key != "email" && key != "password" && key != "name" {
			return false
		}
	}
	password, passwordOK := input["password"].(string)
	if _, emailOK := input["email"].(string); !emailOK || !passwordOK || password == "" {
		return false
	}
	if raw, exists := input["name"]; exists {
		name, ok := raw.(string)
		if !ok || name != strings.TrimSpace(name) || name == "" || len([]rune(name)) > 100 {
			return false
		}
	}
	return true
}

func operatorDisableShapeValid(input map[string]any, accountID string) bool {
	if len(input) != 2 || stringValue(input["confirmationAccountId"]) != accountID {
		return false
	}
	for key := range input {
		if key != "confirmationAccountId" && key != "reason" {
			return false
		}
	}
	reason, ok := input["reason"].(string)
	return ok && reason == strings.TrimSpace(reason) && reason != "" && len([]rune(reason)) <= 200
}

func (app *controlPlaneServer) operatorAccountPage(ctx context.Context, service *controlplane.Service, page, pageSize int) (map[string]any, string, error) {
	accounts, err := app.tables.ListAccounts(ctx, "")
	if err != nil {
		return nil, "", err
	}
	users, err := app.tables.ListUsers(ctx, true)
	if err != nil {
		return nil, "", err
	}
	workspaces, err := app.tables.ListWorkspaces(ctx, "")
	if err != nil {
		return nil, "", err
	}
	sort.Slice(accounts, func(i, j int) bool { return stringValue(accounts[i]["id"]) < stringValue(accounts[j]["id"]) })
	local := make([]map[string]any, 0, len(accounts))
	remoteIDs := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		remoteID, ok := positiveIntegerField(account, "sub2apiUserId")
		owner := findRecord(users, stringValue(account["ownerUserId"]))
		if !ok || !ownsAccount(account, owner) {
			return nil, "", errAccountIdentityConflict
		}
		local = append(local, map[string]any{"account": account, "owner": owner, "remoteId": remoteID})
		remoteIDs = append(remoteIDs, remoteID)
	}
	if len(local) == 0 {
		return map[string]any{"items": []any{}, "total": 0, "page": page, "pageSize": pageSize}, "empty", nil
	}
	if len(local) > 50 {
		return nil, "", errors.New("operator account projection exceeds Pilot limit")
	}
	remoteByID := make(map[int64]clients.Sub2APIUser)
	remoteTotal, remotePages := int64(-1), -1
	for remotePageNumber := 1; remotePageNumber <= remotePages || remotePages == -1; remotePageNumber++ {
		remotePage, err := service.Sub2APIAdminUsers(ctx, clients.Sub2APIUserPageQuery{Page: remotePageNumber, PageSize: 50, SortBy: "id", SortOrder: "asc"})
		if err != nil || remotePage.Page != remotePageNumber || remotePage.PageSize != 50 || remotePage.Pages < 1 {
			return nil, "", errors.New("sub2api user projection unavailable")
		}
		if remotePageNumber == 1 {
			remoteTotal, remotePages = remotePage.Total, remotePage.Pages
		} else if remotePage.Total != remoteTotal || remotePage.Pages != remotePages {
			return nil, "", errors.New("sub2api user projection unavailable")
		}
		for _, user := range remotePage.Items {
			if _, duplicate := remoteByID[user.ID]; duplicate {
				return nil, "", errors.New("sub2api user projection unavailable")
			}
			remoteByID[user.ID] = user
		}
	}
	if int64(len(remoteByID)) != remoteTotal {
		return nil, "", errors.New("sub2api user projection unavailable")
	}
	usageByID, usageErr := service.Sub2APIBatchUsersUsage(ctx, remoteIDs)
	items := make([]any, 0, len(local))
	for _, joined := range local {
		account := joined["account"].(map[string]any)
		owner := joined["owner"].(map[string]any)
		remoteID := joined["remoteId"].(int64)
		ownerStatus := stringValue(owner["status"])
		if ownerStatus != "active" {
			ownerStatus = "disabled"
		}
		item := map[string]any{
			"accountId": stringValue(account["id"]), "consoleUserId": stringValue(owner["id"]), "role": stringValue(owner["role"]),
			"sub2apiUserId": strconv.FormatInt(remoteID, 10), "email": normalizeEmail(stringValue(owner["email"])), "status": ownerStatus,
			"keyCount": sourceEnvelope("sub2api", "unavailable", nil, ""),
		}
		workspaceCount := 0
		for _, workspace := range workspaces {
			if firstNonEmpty(stringValue(workspace["ownerAccountId"]), stringValue(workspace["accountId"])) == stringValue(account["id"]) {
				workspaceCount++
			}
		}
		item["workspaceCount"] = sourceEnvelope("control-plane", "available", workspaceCount, "")
		remote, remoteOK := remoteByID[remoteID]
		remoteOK = remoteOK && remote.ID == remoteID && remote.Email == normalizeEmail(stringValue(owner["email"])) && (remote.Status == "active" || remote.Status == "disabled")
		if !remoteOK {
			item["gatewayIdentity"] = sourceEnvelope("sub2api", "unavailable", nil, "")
			item["wallet"] = sourceEnvelope("sub2api", "unavailable", nil, "")
			item["usage"] = sourceEnvelope("sub2api", "unavailable", nil, "")
			items = append(items, item)
			continue
		}
		updatedAt := remote.UpdatedAt.UTC().Format(time.RFC3339Nano)
		item["gatewayIdentity"] = sourceEnvelope("sub2api", "available", map[string]any{"userId": strconv.FormatInt(remote.ID, 10), "email": remote.Email, "status": remote.Status}, updatedAt)
		item["wallet"] = sourceEnvelope("sub2api", "available", map[string]any{"userId": strconv.FormatInt(remote.ID, 10), "currency": "USD", "usdMicros": remote.BalanceUSDMicros, "status": remote.Status}, updatedAt)
		usage, usageOK := usageByID[remoteID]
		if usageErr != nil || !usageOK || usage.UserID != remoteID {
			item["usage"] = sourceEnvelope("sub2api", "unavailable", nil, "")
		} else {
			platforms := make([]any, 0, len(usage.ByPlatform))
			for _, platform := range usage.ByPlatform {
				platforms = append(platforms, map[string]any{"platform": platform.Platform, "todayActualCostUsdMicros": platform.TodayActualCostUSDMicros, "totalActualCostUsdMicros": platform.TotalActualCostUSDMicros})
			}
			item["usage"] = sourceEnvelope("sub2api", "available", map[string]any{"todayActualCostUsdMicros": usage.TodayActualCostUSDMicros, "totalActualCostUsdMicros": usage.TotalActualCostUSDMicros, "byPlatform": platforms}, "")
		}
		items = append(items, item)
	}
	total := len(items)
	start := (page - 1) * pageSize
	if start >= total {
		items = []any{}
	} else {
		end := start + pageSize
		if end > total {
			end = total
		}
		items = items[start:end]
	}
	status := "available"
	if len(items) == 0 {
		status = "empty"
	}
	return map[string]any{"items": items, "total": total, "page": page, "pageSize": pageSize}, status, nil
}

func authoritativeSourceTimestamp(value any) string {
	raw := stringValue(value)
	if raw == "" {
		return ""
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return ""
	}
	return parsed.UTC().Format(time.RFC3339Nano)
}

type operatorWorkspaceFacts struct {
	accounts            []map[string]any
	users               []map[string]any
	computes            []map[string]any
	storages            []map[string]any
	attachments         []map[string]any
	operations          []clients.FabricOperation
	operationsAvailable bool
	keyUsage            map[int64]clients.Sub2APIBatchKeyUsage
	keyUsageAvailable   bool
}

func (app *controlPlaneServer) operatorWorkspacePage(ctx context.Context, service *controlplane.Service, page, pageSize int) (map[string]any, string, error) {
	workspaces, err := app.tables.ListWorkspaces(ctx, "")
	if err != nil {
		return nil, "", err
	}
	sort.Slice(workspaces, func(i, j int) bool { return stringValue(workspaces[i]["id"]) < stringValue(workspaces[j]["id"]) })
	facts, err := app.loadOperatorWorkspaceFacts(ctx, service, workspaces)
	if err != nil {
		return nil, "", err
	}
	total := len(workspaces)
	start := (page - 1) * pageSize
	selected := []map[string]any{}
	if start < total {
		end := start + pageSize
		if end > total {
			end = total
		}
		selected = workspaces[start:end]
	}
	items := make([]any, 0, len(selected))
	for _, workspace := range selected {
		items = append(items, app.operatorWorkspaceDTO(ctx, service, workspace, facts, false))
	}
	status := "available"
	if len(items) == 0 {
		status = "empty"
	}
	return map[string]any{"items": items, "total": total, "page": page, "pageSize": pageSize}, status, nil
}

func (app *controlPlaneServer) operatorWorkspaceDetail(ctx context.Context, service *controlplane.Service, workspaceID string) (map[string]any, bool, error) {
	if workspaceID == "" {
		return nil, false, nil
	}
	workspaces, err := app.tables.ListWorkspaces(ctx, "")
	if err != nil {
		return nil, false, err
	}
	workspace := findRecord(workspaces, workspaceID)
	if workspace == nil {
		return nil, false, nil
	}
	facts, err := app.loadOperatorWorkspaceFacts(ctx, service, []map[string]any{workspace})
	if err != nil {
		return nil, false, err
	}
	return app.operatorWorkspaceDTO(ctx, service, workspace, facts, true), true, nil
}

func (app *controlPlaneServer) loadOperatorWorkspaceFacts(ctx context.Context, service *controlplane.Service, workspaces []map[string]any) (operatorWorkspaceFacts, error) {
	var facts operatorWorkspaceFacts
	var err error
	if facts.accounts, err = app.tables.ListAccounts(ctx, ""); err != nil {
		return facts, err
	}
	if facts.users, err = app.tables.ListUsers(ctx, true); err != nil {
		return facts, err
	}
	if facts.computes, err = app.tables.ListComputes(ctx, ""); err != nil {
		return facts, err
	}
	if facts.storages, err = app.tables.ListStorages(ctx, ""); err != nil {
		return facts, err
	}
	if facts.attachments, err = app.tables.ListAttachments(ctx, ""); err != nil {
		return facts, err
	}
	facts.operations, err = service.FabricOperations(ctx)
	facts.operationsAvailable = err == nil
	keyIDs := make([]int64, 0, len(workspaces))
	seen := map[int64]struct{}{}
	for _, workspace := range workspaces {
		keyID, ok := positiveIntegerField(workspace, "workspaceApiKeyId")
		if !ok {
			continue
		}
		if _, exists := seen[keyID]; exists {
			continue
		}
		seen[keyID] = struct{}{}
		keyIDs = append(keyIDs, keyID)
	}
	if len(keyIDs) > 0 && len(keyIDs) <= 50 {
		facts.keyUsage, err = service.Sub2APIBatchKeysUsage(ctx, keyIDs)
		facts.keyUsageAvailable = err == nil
	}
	return facts, nil
}

func (app *controlPlaneServer) operatorWorkspaceDTO(ctx context.Context, service *controlplane.Service, workspace map[string]any, facts operatorWorkspaceFacts, liveLedger bool) map[string]any {
	workspaceID := stringValue(workspace["id"])
	accountID := firstNonEmpty(stringValue(workspace["ownerAccountId"]), stringValue(workspace["accountId"]))
	ownerID := stringValue(workspace["ownerUserId"])
	account := findRecord(facts.accounts, accountID)
	owner := findRecord(facts.users, ownerID)
	result := map[string]any{
		"ownerAccount": operatorOwnerAccountEnvelope(account),
		"ownerUser":    operatorOwnerUserEnvelope(owner, accountID),
		"receipt":      sourceEnvelope("ledger", "unavailable", nil, ""),
	}
	if projected, ok := workspaceSourceProjection(workspace); ok {
		result["workspace"] = sourceEnvelope("control-plane", "available", projected, authoritativeSourceTimestamp(workspace["updatedAt"]))
	} else {
		result["workspace"] = sourceEnvelope("control-plane", "unavailable", nil, "")
	}
	keyID, hasKey := positiveIntegerField(workspace, "workspaceApiKeyId")
	keyUsage, hasUsage := facts.keyUsage[keyID]
	if hasKey && facts.keyUsageAvailable && hasUsage && keyUsage.APIKeyID == keyID {
		result["workspaceKeyUsage"] = sourceEnvelope("sub2api", "available", map[string]any{
			"keyId": strconv.FormatInt(keyID, 10), "todayActualCostUsdMicros": keyUsage.TodayActualCostUSDMicros, "totalActualCostUsdMicros": keyUsage.TotalActualCostUSDMicros,
		}, "")
	} else {
		result["workspaceKeyUsage"] = sourceEnvelope("sub2api", "unavailable", nil, "")
	}
	type resourceRow struct {
		kind string
		row  map[string]any
	}
	rows := make([]resourceRow, 0)
	for _, row := range facts.computes {
		if stringValue(row["workspaceId"]) == workspaceID {
			rows = append(rows, resourceRow{kind: "compute", row: row})
		}
	}
	for _, row := range facts.storages {
		if stringValue(row["workspaceId"]) == workspaceID {
			rows = append(rows, resourceRow{kind: "storage", row: row})
		}
	}
	for _, row := range facts.attachments {
		if stringValue(row["workspaceId"]) == workspaceID {
			rows = append(rows, resourceRow{kind: "attachment", row: row})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		left, right := rows[i].kind+":"+stringValue(rows[i].row["id"]), rows[j].kind+":"+stringValue(rows[j].row["id"])
		return left < right
	})
	resources := make([]any, 0, len(rows))
	for _, resource := range rows {
		resources = append(resources, app.operatorResourceDTO(ctx, service, resource.kind, resource.row, account, owner, workspace, facts, liveLedger))
	}
	result["resources"] = resources
	if liveLedger {
		receiptID := stringValue(workspace["receiptId"])
		if receipt, err := service.BillingReceipt(ctx, receiptID); err == nil && receipt.ReceiptID == receiptID && receipt.AccountID == accountID && receipt.WorkspaceID == workspaceID {
			if projected, ok := projectWorkspaceCreatedReceipt(receipt); ok {
				result["receipt"] = sourceEnvelope("ledger", "available", projected, authoritativeSourceTimestamp(receipt.CreatedAt))
			}
		}
	}
	return result
}

func operatorOwnerAccountEnvelope(account map[string]any) map[string]any {
	if account == nil || stringValue(account["id"]) == "" {
		return sourceEnvelope("control-plane", "unavailable", nil, "")
	}
	return sourceEnvelope("control-plane", "available", map[string]any{"id": stringValue(account["id"])}, authoritativeSourceTimestamp(account["updatedAt"]))
}

func operatorOwnerUserEnvelope(owner map[string]any, accountID string) map[string]any {
	if owner == nil || stringValue(owner["id"]) == "" || normalizeEmail(stringValue(owner["email"])) == "" || stringValue(owner["accountId"]) != accountID {
		return sourceEnvelope("control-plane", "unavailable", nil, "")
	}
	return sourceEnvelope("control-plane", "available", map[string]any{"id": stringValue(owner["id"]), "email": normalizeEmail(stringValue(owner["email"]))}, authoritativeSourceTimestamp(owner["updatedAt"]))
}

func (app *controlPlaneServer) operatorResourceDTO(ctx context.Context, service *controlplane.Service, kind string, row, account, owner, workspace map[string]any, facts operatorWorkspaceFacts, liveLedger bool) map[string]any {
	accountID := firstNonEmpty(stringValue(workspace["ownerAccountId"]), stringValue(workspace["accountId"]))
	workspaceID := stringValue(workspace["id"])
	workspaceData := map[string]any{"id": workspaceID}
	if name := stringValue(workspace["name"]); name != "" {
		workspaceData["name"] = name
	}
	result := map[string]any{
		"ownerAccount": operatorOwnerAccountEnvelope(account),
		"ownerUser":    operatorOwnerUserEnvelope(owner, accountID),
		"workspace":    operatorFactEnvelope("control-plane", workspaceData, workspaceID != ""),
		"resourceType": operatorFactEnvelope("fabric", kind, kind != "" && stringValue(row["id"]) != ""),
	}
	spec := ""
	switch kind {
	case "compute":
		spec = firstNonEmpty(stringValue(row["instanceType"]), providerDataValue(row, "instanceType"))
	case "storage":
		spec = firstNonEmpty(stringValue(row["diskType"]), stringValue(row["storageClass"]))
		if spec == "" {
			if size := int64(numberField(row, "sizeGb", 0)); size > 0 {
				spec = strconv.FormatInt(size, 10) + " GB"
			}
		}
	case "attachment":
		spec = stringValue(row["mountPath"])
	}
	result["packageOrSpec"] = operatorStringFactEnvelope("fabric", spec)
	providerID := firstNonEmpty(stringValue(row["providerResourceId"]), stringValue(row["providerAttachmentId"]))
	result["providerId"] = operatorStringFactEnvelope("fabric", providerID)
	result["zone"] = operatorStringFactEnvelope("fabric", stringValue(row["zone"]))
	result["status"] = operatorStringFactEnvelope("fabric", firstNonEmpty(stringValue(row["providerStatus"]), stringValue(row["status"])))
	result["createdAt"] = operatorTimestampFactEnvelope("fabric", stringValue(row["providerCreatedAt"]))
	result["expiresAt"] = operatorTimestampFactEnvelope("fabric", stringValue(row["deadline"]))
	result["lastReadAt"] = operatorTimestampFactEnvelope("fabric", stringValue(row["lastProviderSyncAt"]))
	operationID, operationSource := stringValue(row["operationId"]), "control-plane"
	if operationID == "" && facts.operationsAvailable {
		for _, operation := range facts.operations {
			if operation.ResourceID != stringValue(row["id"]) || (operation.AccountID != "" && operation.AccountID != accountID) || (operation.WorkspaceID != "" && operation.WorkspaceID != workspaceID) {
				continue
			}
			candidate := firstNonEmpty(operation.OperationID, operation.ID)
			if candidate != "" {
				operationID, operationSource = candidate, "fabric"
			}
		}
	}
	result["operationRef"] = operatorStringFactEnvelope(operationSource, operationID)
	result["receiptRef"] = sourceEnvelope("ledger", "unavailable", nil, "")
	if liveLedger {
		receiptID := firstNonEmpty(stringValue(row["lastReceiptId"]), stringValue(row["receiptId"]))
		if receipt, ok := operatorResourceReceipt(ctx, service, receiptID, accountID, workspaceID, kind, stringValue(row["id"])); ok {
			result["receiptRef"] = sourceEnvelope("ledger", "available", receipt.ReceiptID, authoritativeSourceTimestamp(receipt.CreatedAt))
		}
	}
	return result
}

func operatorFactEnvelope(source string, value any, available bool) map[string]any {
	if !available {
		return sourceEnvelope(source, "unavailable", nil, "")
	}
	return sourceEnvelope(source, "available", value, "")
}

func operatorStringFactEnvelope(source, value string) map[string]any {
	return operatorFactEnvelope(source, value, strings.TrimSpace(value) != "")
}

func operatorTimestampFactEnvelope(source, value string) map[string]any {
	value = authoritativeSourceTimestamp(value)
	return operatorFactEnvelope(source, value, value != "")
}

func operatorResourceReceipt(ctx context.Context, service *controlplane.Service, receiptID, accountID, workspaceID, resourceType, resourceID string) (clients.Receipt, bool) {
	if receiptID == "" {
		return clients.Receipt{}, false
	}
	receipt, err := service.BillingReceipt(ctx, receiptID)
	if err != nil || receipt.ReceiptID != receiptID || receipt.AccountID != accountID || receipt.WorkspaceID != workspaceID {
		return clients.Receipt{}, false
	}
	if value := stringValue(receipt.Cost["resourceType"]); value != "" && value != resourceType {
		return clients.Receipt{}, false
	}
	if value := stringValue(receipt.Cost["resourceId"]); value != "" && value != resourceID {
		return clients.Receipt{}, false
	}
	return receipt, true
}

func (app *controlPlaneServer) operatorOverview(ctx context.Context, service *controlplane.Service) (map[string]any, error) {
	result := map[string]any{
		"accounts":       sourceEnvelope("control-plane+sub2api", "unavailable", nil, ""),
		"wallet":         sourceEnvelope("sub2api", "unavailable", nil, ""),
		"keys":           sourceEnvelope("sub2api", "unavailable", nil, ""),
		"usage":          sourceEnvelope("sub2api", "unavailable", nil, ""),
		"workspaces":     sourceEnvelope("control-plane", "unavailable", nil, ""),
		"resources":      sourceEnvelope("fabric", "unavailable", nil, ""),
		"reconciliation": sourceEnvelope("control-plane", "unavailable", nil, ""),
	}
	if accounts, _, err := app.operatorAccountPage(ctx, service, 1, 50); err == nil {
		items, _ := accounts["items"].([]any)
		active, disabled := 0, 0
		walletTotal, todayUsage, totalUsage := int64(0), int64(0), int64(0)
		walletAvailable, usageAvailable := true, true
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			if stringValue(item["status"]) == "active" {
				active++
			} else {
				disabled++
			}
			wallet, ok := availableEnvelopeData(item["wallet"])
			if !ok {
				walletAvailable = false
			} else if walletAvailable {
				balance, valid := wallet["usdMicros"].(int64)
				if !valid || balance > 0 && walletTotal > math.MaxInt64-balance || balance < 0 && walletTotal < math.MinInt64-balance {
					walletAvailable = false
				} else {
					walletTotal += balance
				}
			}
			usage, ok := availableEnvelopeData(item["usage"])
			if !ok {
				usageAvailable = false
			} else if usageAvailable {
				today, todayValid := usage["todayActualCostUsdMicros"].(int64)
				total, totalValid := usage["totalActualCostUsdMicros"].(int64)
				var todayOK, totalOK bool
				todayUsage, todayOK = checkedAddInt64(todayUsage, today)
				totalUsage, totalOK = checkedAddInt64(totalUsage, total)
				usageAvailable = todayValid && totalValid && todayOK && totalOK
			}
		}
		result["accounts"] = sourceEnvelope("control-plane", "available", map[string]any{"total": len(items), "active": active, "disabled": disabled}, "")
		if walletAvailable {
			result["wallet"] = sourceEnvelope("sub2api", "available", map[string]any{"currency": "USD", "usdMicros": walletTotal}, "")
		}
		if usageAvailable {
			result["usage"] = sourceEnvelope("sub2api", "available", map[string]any{"todayActualCostUsdMicros": todayUsage, "totalActualCostUsdMicros": totalUsage}, "")
		}
	}
	if workspaces, _, err := app.operatorWorkspacePage(ctx, service, 1, 50); err == nil {
		items, _ := workspaces["items"].([]any)
		resourceCount := 0
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			resources, _ := item["resources"].([]any)
			resourceCount += len(resources)
		}
		result["workspaces"] = sourceEnvelope("control-plane", "available", map[string]any{"total": int(numberField(workspaces, "total", 0))}, "")
		result["resources"] = sourceEnvelope("fabric", "available", map[string]any{"total": resourceCount}, "")
	}
	if reconciliation, _, err := app.operatorReconciliationPage(ctx, 1, 50); err == nil {
		result["reconciliation"] = sourceEnvelope("control-plane", "available", map[string]any{"total": int(numberField(reconciliation, "total", 0))}, "")
	}
	result["health"] = sourceEnvelope("control-plane", "available", app.operatorHealth(ctx, service), "")
	return result, nil
}

func availableEnvelopeData(value any) (map[string]any, bool) {
	envelope, ok := value.(map[string]any)
	if !ok || envelope["available"] != true {
		return nil, false
	}
	data, ok := envelope["data"].(map[string]any)
	return data, ok
}

func (app *controlPlaneServer) operatorReconciliationPage(ctx context.Context, page, pageSize int) (map[string]any, string, error) {
	operations, err := app.tables.ListRuntimeOperations(ctx)
	if err != nil {
		return nil, "", err
	}
	computes, err := app.tables.ListComputes(ctx, "")
	if err != nil {
		return nil, "", err
	}
	storages, err := app.tables.ListStorages(ctx, "")
	if err != nil {
		return nil, "", err
	}
	items := make([]any, 0)
	appendReview := func(resourceType, resourceID, accountID, operationID, phase, errorCode, action, receiptID string) {
		if resourceID == "" || accountID == "" || operationID == "" {
			return
		}
		item := map[string]any{
			"id": resourceID, "resourceType": resourceType, "status": "manual_review", "accountId": accountID,
			"billingOperationId": operationID, "phase": phase, "errorCode": errorCode, "allowedActions": []string{action},
			"operationRef": operationID,
		}
		if receiptID != "" {
			item["receiptRef"] = receiptID
		}
		items = append(items, item)
	}
	for _, operation := range operations {
		if stringValue(operation["status"]) != "manual_review" {
			continue
		}
		details := map[string]any{}
		_ = json.Unmarshal([]byte(stringValue(operation["result"])), &details)
		operationID := firstNonEmpty(stringValue(operation["operationId"]), stringValue(operation["id"]))
		switch stringValue(operation["action"]) {
		case workspaceLaunchAction:
			appendReview(
				"workspace", operationID, firstNonEmpty(stringValue(operation["accountId"]), stringValue(details["accountId"])), operationID,
				firstNonEmpty(stringValue(details["phase"]), "manual_review"), firstNonEmpty(stringValue(details["errorCode"]), stringValue(details["lastBillingError"])),
				"recover_workspace_launch", firstNonEmpty(stringValue(operation["receiptId"]), stringValue(details["receiptId"])),
			)
		case "workspace.renewal":
			appendReview(
				"workspace", firstNonEmpty(stringValue(operation["workspaceId"]), stringValue(details["workspaceId"])),
				firstNonEmpty(stringValue(operation["accountId"]), stringValue(details["accountId"])), operationID,
				firstNonEmpty(stringValue(details["phase"]), "manual_review"), firstNonEmpty(stringValue(details["errorCode"]), stringValue(details["lastBillingError"])),
				"resolve_billing_review", firstNonEmpty(stringValue(operation["receiptId"]), stringValue(details["receiptId"])),
			)
		}
	}
	for resourceType, rows := range map[string][]map[string]any{"compute": computes, "storage": storages} {
		for _, row := range rows {
			if stringValue(row["billingStatus"]) != "manual_review" {
				continue
			}
			appendReview(
				resourceType, stringValue(row["id"]), stringValue(row["accountId"]), stringValue(row["billingOperationId"]),
				firstNonEmpty(stringValue(row["reviewResolutionPhase"]), "manual_review"), firstNonEmpty(stringValue(row["lastBillingError"]), stringValue(row["manualReviewReason"])),
				"resolve_billing_review", firstNonEmpty(stringValue(row["lastReceiptId"]), stringValue(row["receiptId"])),
			)
		}
	}
	if reconciliation, ok, err := app.tables.BillingReconciliation(ctx); err != nil {
		return nil, "", err
	} else if ok && stringValue(reconciliation["status"]) == "mismatch" {
		items = append(items, map[string]any{
			"id": stringValue(reconciliation["id"]), "resourceType": "workspace", "status": stringValue(reconciliation["status"]),
			"accountId": "", "billingOperationId": stringValue(reconciliation["id"]), "phase": stringValue(reconciliation["status"]),
			"errorCode": firstNonEmpty(stringValue(reconciliation["reason"]), stringValue(reconciliation["status"])), "allowedActions": []string{},
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return stringValue(items[i].(map[string]any)["id"]) < stringValue(items[j].(map[string]any)["id"])
	})
	total := len(items)
	start := (page - 1) * pageSize
	if start >= total {
		items = []any{}
	} else {
		end := start + pageSize
		if end > total {
			end = total
		}
		items = items[start:end]
	}
	status := "available"
	if len(items) == 0 {
		status = "empty"
	}
	return map[string]any{"items": items, "total": total, "page": page, "pageSize": pageSize}, status, nil
}

func (app *controlPlaneServer) operatorHealth(ctx context.Context, service *controlplane.Service) map[string]any {
	result := map[string]any{
		"controlPlane": sourceEnvelope("control-plane", "available", map[string]any{"ready": true}, ""),
		"gateway":      sourceEnvelope("sub2api", "unavailable", nil, ""),
		"fabric":       sourceEnvelope("fabric", "unavailable", nil, ""),
		"runtime":      app.operatorRuntimeHealth(ctx, service),
		"ledger":       sourceEnvelope("ledger", "unavailable", nil, ""),
	}
	if version, err := service.Sub2APIVersion(ctx); err == nil && strings.TrimSpace(version) != "" {
		result["gateway"] = sourceEnvelope("sub2api", "available", map[string]any{"ready": true, "version": version}, "")
	}
	if readiness, err := service.RuntimeReadiness(ctx); err == nil {
		result["fabric"] = sourceEnvelope("fabric", "available", map[string]any{
			"ready": readiness["ready"] == true, "provider": readiness["provider"],
			"cloudImagesReady": readiness["cloudImagesReady"] == true, "workspaceImagesReady": readiness["workspaceImagesReady"] == true,
			"immutableImagesReady": readiness["immutableImagesReady"] == true,
		}, "")
	}
	if workspaces, err := app.tables.ListWorkspaces(ctx, ""); err == nil {
		for _, workspace := range workspaces {
			receiptID := stringValue(workspace["receiptId"])
			if receiptID == "" {
				continue
			}
			receipt, err := service.BillingReceipt(ctx, receiptID)
			if err == nil && receipt.ReceiptID == receiptID && receipt.WorkspaceID == stringValue(workspace["id"]) {
				result["ledger"] = sourceEnvelope("ledger", "available", map[string]any{"ready": true, "receiptId": receiptID}, authoritativeSourceTimestamp(receipt.CreatedAt))
			}
			break
		}
	}
	return result
}

func (app *controlPlaneServer) operatorRuntimeHealth(ctx context.Context, service *controlplane.Service) map[string]any {
	workspaces, err := app.tables.ListWorkspaces(ctx, "")
	if err != nil {
		return sourceEnvelope("runtime", "unavailable", nil, "")
	}
	active := make([]string, 0)
	for _, workspace := range workspaces {
		if state := stringValue(workspace["state"]); state == "active" || state == "running" || state == "provisioning" {
			active = append(active, stringValue(workspace["id"]))
		}
	}
	if len(active) == 0 {
		return sourceEnvelope("runtime", "unavailable", nil, "")
	}
	type probeResult struct {
		workspaceID string
		status      string
		ready       bool
		available   bool
	}
	results := make(chan probeResult, len(active))
	gate := make(chan struct{}, 3)
	var wait sync.WaitGroup
	for _, workspaceID := range active {
		wait.Add(1)
		go func(id string) {
			defer wait.Done()
			select {
			case gate <- struct{}{}:
				defer func() { <-gate }()
			case <-ctx.Done():
				results <- probeResult{workspaceID: id}
				return
			}
			probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			runtime, err := service.WorkspaceRuntimeStatus(probeCtx, id)
			available := err == nil && runtime.WorkspaceID == id && runtime.Status != ""
			results <- probeResult{workspaceID: id, status: runtime.Status, ready: runtime.Ready, available: available}
		}(workspaceID)
	}
	wait.Wait()
	close(results)
	items := make([]any, 0, len(active))
	availableCount, allReady := 0, true
	for result := range results {
		item := map[string]any{"workspaceId": result.workspaceID, "available": result.available}
		if result.available {
			availableCount++
			item["status"], item["ready"] = result.status, result.ready
		}
		if !result.available || !result.ready {
			allReady = false
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		return stringValue(items[i].(map[string]any)["workspaceId"]) < stringValue(items[j].(map[string]any)["workspaceId"])
	})
	if availableCount == 0 {
		return sourceEnvelope("runtime", "unavailable", nil, "")
	}
	return sourceEnvelope("runtime", "available", map[string]any{"ready": allReady, "total": len(active), "available": availableCount, "items": items}, "")
}

func (app *controlPlaneServer) operatorAccountMappings(ctx context.Context, service *controlplane.Service) ([]any, error) {
	accounts, err := app.tables.ListAccounts(ctx, "")
	if err != nil {
		return nil, err
	}
	users, err := app.tables.ListUsers(ctx, true)
	if err != nil {
		return nil, err
	}
	sort.Slice(accounts, func(i, j int) bool { return stringValue(accounts[i]["id"]) < stringValue(accounts[j]["id"]) })
	items := make([]any, 0, len(accounts))
	for _, account := range accounts {
		accountID := stringValue(account["id"])
		remoteID, ok := positiveIntegerField(account, "sub2apiUserId")
		owner := findRecord(users, stringValue(account["ownerUserId"]))
		if !ok || !ownsAccount(account, owner) {
			return nil, errAccountIdentityConflict
		}
		identity, err := service.Sub2APIUser(ctx, remoteID)
		if err != nil {
			return nil, err
		}
		if normalizeEmail(stringValue(owner["email"])) != identity.Email {
			return nil, errAccountIdentityConflict
		}
		items = append(items, map[string]any{
			"accountId": accountID, "consoleUserId": stringValue(owner["id"]), "role": stringValue(owner["role"]),
			"sub2apiUserId": strconv.FormatInt(identity.ID, 10), "email": identity.Email, "status": identity.Status,
		})
	}
	return items, nil
}

func billingReviewRequestShapeValid(input map[string]any) bool {
	if len(input) != 4 {
		return false
	}
	for _, key := range []string{"accountId", "billingOperationId", "decision", "evidenceRef"} {
		value, ok := input[key].(string)
		if !ok || value == "" || value != strings.TrimSpace(value) {
			return false
		}
	}
	return true
}

func workspaceLaunchRecoveryShapeValid(input map[string]any) bool {
	if len(input) != 3 {
		return false
	}
	for _, key := range []string{"accountId", "billingOperationId", "evidenceRef"} {
		value, ok := input[key].(string)
		if !ok || value == "" || value != strings.TrimSpace(value) {
			return false
		}
	}
	return true
}

func validBillingReviewEvidenceRef(value string) bool {
	return billingReviewEvidenceRefPattern.MatchString(value)
}

func validBillingReviewOpaqueID(value string) bool {
	if len(value) < 3 || len(value) > 48 || value != compactID(value) {
		return false
	}
	lower := strings.ToLower(value)
	for _, forbidden := range []string{"api-key", "apikey", "bearer", "credential", "password", "secret", "token"} {
		if strings.Contains(lower, forbidden) {
			return false
		}
	}
	return true
}

func writeBillingReviewResolutionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errInvalidBillingReview):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, errBillingReviewNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, errIdempotencyConflict), errors.Is(err, errBillingReviewNotPending), errors.Is(err, errBillingReviewIdentity), errors.Is(err, errBillingReviewChargeFact), errors.Is(err, errBillingReviewProviderFact):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, errBillingReviewReceipt), errors.Is(err, errBillingReviewRefund):
		writeError(w, http.StatusBadGateway, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "state_persist_failed")
	}
}
