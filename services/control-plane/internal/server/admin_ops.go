package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
)

func (app *controlPlaneServer) createOrganization(input map[string]any) (map[string]any, error) {
	name := stringField(input, "name", "Organization")
	accountID := stringField(input, "billingAccountId", "acct-admin")
	accounts, err := app.tables.ListAccounts(context.Background())
	if err != nil {
		return nil, err
	}
	if !recordExists(accounts, accountID) {
		return nil, errAccountNotFound
	}
	id := "org-" + compactID(name+"-"+time.Now().UTC().Format("20060102150405.000000000"))
	org := map[string]any{"id": id, "name": name, "billingAccountId": accountID, "status": "active"}
	if err := app.tables.SaveOrganization(context.Background(), org); err != nil {
		return nil, err
	}
	return cloneMap(org), nil
}

func (app *controlPlaneServer) createMembership(input map[string]any) (map[string]any, error) {
	orgID := stringField(input, "organizationId", "")
	userID := stringField(input, "userId", "")
	role := stringField(input, "role", "member")
	if !validRole(role) {
		return nil, errInvalidRole
	}
	organizations, err := app.tables.ListOrganizations(context.Background())
	if err != nil {
		return nil, err
	}
	organization := findRecord(organizations, orgID)
	if organization == nil {
		return nil, errOrganizationNotFound
	}
	user, err := app.findUserByID(context.Background(), userID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, errMembershipUserNotFound
	}
	accountID := stringField(input, "accountId", stringValue(organization["billingAccountId"]))
	accounts, err := app.tables.ListAccounts(context.Background())
	if err != nil {
		return nil, err
	}
	if !recordExists(accounts, accountID) {
		return nil, errAccountNotFound
	}
	if accountID != stringValue(organization["billingAccountId"]) || accountID != stringValue(user["accountId"]) {
		return nil, errMembershipAccountMismatch
	}
	id := "mem-" + stableID(orgID, userID, time.Now().UTC().String())[:12]
	membership := map[string]any{"id": id, "accountId": accountID, "organizationId": orgID, "userId": userID, "role": role, "status": "active"}
	if err := app.tables.SaveMembership(context.Background(), membership); err != nil {
		return nil, err
	}
	return cloneMap(membership), nil
}

func findRecord(rows []map[string]any, id string) map[string]any {
	for _, row := range rows {
		if stringValue(row["id"]) == id {
			return row
		}
	}
	return nil
}

func recordExists(rows []map[string]any, id string) bool { return findRecord(rows, id) != nil }

func (app *controlPlaneServer) revokeMembership(ctx context.Context, id string) (map[string]any, error) {
	memberships, err := app.tables.ListMemberships(ctx)
	if err != nil {
		return nil, err
	}
	membership := findRecord(memberships, id)
	if membership == nil {
		return nil, errMembershipNotFound
	}
	membership["status"] = "revoked"
	if err := app.tables.SaveMembership(ctx, membership); err != nil {
		return nil, err
	}
	return membership, nil
}

func (app *controlPlaneServer) managementState(includeDeleted bool, computePools []any) map[string]any {
	app.mu.Lock()
	defer app.mu.Unlock()
	return map[string]any{
		"organization":           nil,
		"organizations":          rowsAsAnyFromMaps(app.listOrganizations()),
		"users":                  sanitizedUserValues(app.userRecordSet(includeDeleted), includeDeleted),
		"memberships":            rowsAsAnyFromMaps(app.listMemberships()),
		"supportTickets":         rowsAsAnyFromMaps(app.listSupportMappings("")),
		"accounts":               app.accountsLocked(),
		"packages":               packageList(),
		"computePools":           computePools,
		"workspaces":             rowsAsAnyFromMaps(app.listWorkspaces("")),
		"computeAllocations":     rowsAsAnyFromMaps(app.listComputes("")),
		"storageVolumes":         rowsAsAnyFromMaps(app.listStorages("")),
		"storageAttachments":     rowsAsAnyFromMaps(app.listAttachments("")),
		"resourceLedgerEvidence": app.resourceLedgerEvidenceLocked(),
		"runtimeOperations":      rowsAsAnyFromMaps(app.listRuntimeOperations()),
		"auditEvents":            rowsAsAnyFromMaps(app.listAuditEvents("")),
		"billingReconciliation":  app.reconciliationProjectionLocked(),
		"workspaceAccessCleanup": app.workspaceAccessCleanupSummaryLocked(),
		"archive":                app.archiveStateLocked(),
		"retentionPolicy":        currentRetentionPolicy().dto(),
	}
}

func (app *controlPlaneServer) operatorSummary() map[string]any {
	app.mu.Lock()
	defer app.mu.Unlock()
	computes := app.computeRecordSet("")
	workspaces := app.workspaceRecordSet("")
	evidence := app.resourceLedgerEvidenceLocked()
	runtimeOperations := app.listRuntimeOperations()
	running := countStatus(computes, "running")
	accounts := app.accountsLocked()
	return map[string]any{
		"product":                "OPL Console",
		"generatedAt":            time.Now().UTC().Format(time.RFC3339),
		"accountScope":           "all",
		"accounts":               map[string]any{"total": len(accounts)},
		"workspaces":             map[string]any{"total": len(workspaces), "running": countStatus(workspaces, "running"), "urlActive": countActiveURLs(workspaces), "destroyed": countStatus(workspaces, "destroyed"), "needsAttention": 0},
		"computeAllocations":     map[string]any{"total": len(computes), "running": running, "failed": countStatus(computes, "failed")},
		"notifications":          map[string]any{"total": 0, "error": 0, "warning": 0, "recent": []any{}},
		"runtimeOperations":      runtimeOperationSummary(runtimeOperations),
		"failedOperations":       failedRuntimeOperations(runtimeOperations),
		"resourceAnomalies":      app.resourceAnomaliesLocked(),
		"resourceLedgerEvidence": map[string]any{"total": len(evidence), "recent": evidence},
		"productionE2E":          productionE2ESummary(nil),
		"billingReconciliation":  app.reconciliationProjectionLocked(),
		"retentionPolicy":        currentRetentionPolicy().dto(),
	}
}

func (app *controlPlaneServer) appendAuditEvent(r *http.Request, action string, resourceKind string, resourceID string, targetAccountID string, before any, after any, result string) error {
	return app.tables.SaveAuditEvent(r.Context(), app.auditEvent(r, action, resourceKind, resourceID, targetAccountID, before, after, result))
}

func (app *controlPlaneServer) auditEvent(r *http.Request, action string, resourceKind string, resourceID string, targetAccountID string, before any, after any, result string) map[string]any {
	user, _ := app.sessionUserContext(r)
	now := time.Now().UTC().Format(time.RFC3339)
	event := map[string]any{
		"id":              "audit-" + stableID(action, resourceKind, resourceID, now)[:12],
		"actorUserId":     stringValue(user["id"]),
		"actorRole":       stringValue(user["role"]),
		"actorAccountId":  stringValue(user["accountId"]),
		"targetAccountId": targetAccountID,
		"action":          action,
		"resourceKind":    resourceKind,
		"resourceId":      resourceID,
		"ipAddress":       requestIP(r),
		"userAgent":       r.UserAgent(),
		"before":          before,
		"after":           after,
		"result":          result,
		"createdAt":       now,
	}
	return event
}

func (app *controlPlaneServer) rememberRuntimeOperations(operations []clients.FabricOperation) error {
	for _, operation := range operations {
		row := structToMap(operation)
		result := cloneMap(operation.RedactedProviderPayload)
		if operation.ErrorCode != "" {
			result["_fabricErrorCode"] = operation.ErrorCode
		}
		payload, err := json.Marshal(result)
		if err != nil {
			return err
		}
		row["result"] = string(payload)
		if err := app.tables.SaveRuntimeOperation(context.Background(), row); err != nil {
			return err
		}
		if err := app.rememberRuntimeOperationResource(row); err != nil {
			return err
		}
	}
	return nil
}

func (app *controlPlaneServer) rememberRuntimeOperationResource(operation map[string]any) error {
	status := stringValue(operation["status"])
	if status != "succeeded" && status != "failed" {
		return nil
	}
	payload, _ := operation["redactedProviderPayload"].(map[string]any)
	resource, _ := payload["resource"].(map[string]any)
	if len(resource) == 0 {
		return nil
	}
	switch stringValue(operation["resourceKind"]) {
	case "compute_allocation":
		row := cloneMap(resource)
		row["id"] = firstNonEmpty(stringValue(row["id"]), stringValue(operation["resourceId"]))
		row["ownerAccountId"] = firstNonEmpty(stringValue(row["ownerAccountId"]), stringValue(row["accountId"]), stringValue(operation["accountId"]))
		row["accountId"] = firstNonEmpty(stringValue(row["accountId"]), stringValue(row["ownerAccountId"]))
		row["workspaceId"] = firstNonEmpty(stringValue(row["workspaceId"]), stringValue(operation["workspaceId"]))
		if id := stringValue(row["id"]); id != "" {
			if existing, ok := app.getCompute(id); ok {
				row = mergeMaps(existing, row)
			}
			row = computeResponse(row)
			if stringValue(row["accountId"]) == "" {
				return nil
			}
			return app.tables.SaveCompute(context.Background(), row)
		}
	case "storage_volume":
		row := cloneMap(resource)
		row["id"] = firstNonEmpty(stringValue(row["id"]), stringValue(operation["resourceId"]))
		row["ownerAccountId"] = firstNonEmpty(stringValue(row["ownerAccountId"]), stringValue(row["accountId"]), stringValue(operation["accountId"]))
		row["accountId"] = firstNonEmpty(stringValue(row["accountId"]), stringValue(row["ownerAccountId"]))
		row["workspaceId"] = firstNonEmpty(stringValue(row["workspaceId"]), stringValue(operation["workspaceId"]))
		if id := stringValue(row["id"]); id != "" {
			if existing, ok := app.getStorage(id); ok {
				row = mergeMaps(existing, row)
			}
			row = storageResponse(row)
			if stringValue(row["accountId"]) == "" {
				return nil
			}
			return app.tables.SaveStorage(context.Background(), row)
		}
	case "storage_attachment":
		row := attachmentResponse(cloneMap(resource), nil)
		row["id"] = firstNonEmpty(stringValue(row["id"]), stringValue(operation["resourceId"]))
		row["ownerAccountId"] = firstNonEmpty(stringValue(row["ownerAccountId"]), stringValue(row["accountId"]), stringValue(operation["accountId"]))
		row["accountId"] = firstNonEmpty(stringValue(row["accountId"]), stringValue(row["ownerAccountId"]))
		row["workspaceId"] = firstNonEmpty(stringValue(row["workspaceId"]), stringValue(operation["workspaceId"]))
		if id := stringValue(row["id"]); id != "" {
			if existing, ok := app.getAttachment(id); ok {
				row = attachmentResponse(mergeMaps(existing, row), nil)
			}
			row["accountId"] = firstNonEmpty(stringValue(row["accountId"]), app.attachmentAccountID(row))
			if stringValue(row["accountId"]) == "" {
				return nil
			}
			return app.tables.SaveAttachment(context.Background(), row)
		}
	}
	return nil
}

func runtimeOperationSummary(operations []map[string]any) map[string]any {
	failed := failedRuntimeOperations(operations)
	return map[string]any{"total": len(operations), "failed": len(failed), "recentFailed": failed}
}

func (app *controlPlaneServer) accountsLocked() []any {
	accounts, err := app.tables.ListAccounts(context.Background())
	if err != nil {
		return []any{}
	}
	sort.Slice(accounts, func(i, j int) bool { return stringValue(accounts[i]["id"]) < stringValue(accounts[j]["id"]) })
	rows := make([]any, 0, len(accounts))
	for _, account := range accounts {
		row := cloneMap(account)
		accountID := stringValue(row["id"])
		for _, user := range app.userRecordSet(true) {
			if stringValue(user["accountId"]) == accountID && stringValue(user["status"]) != "deleted" {
				row["userId"] = firstNonEmpty(stringValue(row["userId"]), stringValue(user["id"]))
				row["email"] = firstNonEmpty(stringValue(row["email"]), stringValue(user["email"]))
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func (app *controlPlaneServer) cleanupWorkspaceAccess(input map[string]any) (map[string]any, error) {
	requested := stringSet(stringSliceField(input, "workspaceIds"))
	cleaned := []any{}
	skipped := []any{}
	for _, workspace := range app.listWorkspaces("") {
		id := stringValue(workspace["id"])
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
		if err := app.tables.SaveWorkspace(context.Background(), workspace); err != nil {
			return nil, err
		}
	}
	return map[string]any{"cleaned": cleaned, "skipped": skipped}, nil
}

type terminalArchiveStore interface {
	ArchiveTerminalResources(ctx context.Context, reason string) (map[string]any, error)
}

type archiveStateStore interface {
	ArchiveState(ctx context.Context) (map[string]any, error)
}

type retentionStore interface {
	ApplyRetention(ctx context.Context, policy retentionPolicy) (map[string]any, error)
}

func (app *controlPlaneServer) archiveState(ctx context.Context) (map[string]any, error) {
	if store, ok := app.store.(archiveStateStore); ok {
		return store.ArchiveState(ctx)
	}
	app.mu.Lock()
	defer app.mu.Unlock()
	return app.archiveStateLocked(), nil
}

func (app *controlPlaneServer) archiveStateLocked() map[string]any {
	return map[string]any{
		"jobs":             []any{},
		"resources":        []any{},
		"adminAuditEvents": []any{},
		"productionE2E":    productionE2ESummary(nil),
		"retentionPolicy":  currentRetentionPolicy().dto(),
	}
}

func (app *controlPlaneServer) applyRetention(ctx context.Context) (map[string]any, error) {
	if store, ok := app.store.(retentionStore); ok {
		return store.ApplyRetention(ctx, currentRetentionPolicy())
	}
	return map[string]any{"retentionPolicy": currentRetentionPolicy().dto()}, nil
}

func (app *controlPlaneServer) archiveTerminalResources(ctx context.Context, input map[string]any) (map[string]any, error) {
	reason := stringField(input, "reason", "operator_archive_terminal_resources")
	result := map[string]any{"reason": reason}
	if store, ok := app.store.(terminalArchiveStore); ok {
		archived, err := store.ArchiveTerminalResources(ctx, reason)
		if err != nil {
			return nil, err
		}
		result = archived
	}

	result["currentStateRemoved"] = app.removeTerminalResourcesLocked()
	return result, nil
}

func (app *controlPlaneServer) removeTerminalResourcesLocked() int {
	removed := 0
	for _, row := range app.listComputes("") {
		if terminalComputeStatus(stringValue(row["status"])) {
			_ = app.tables.DeleteCompute(context.Background(), stringValue(row["id"]))
			removed++
		}
	}
	for _, row := range app.listStorages("") {
		if terminalStorageStatus(stringValue(row["status"])) {
			_ = app.tables.DeleteStorage(context.Background(), stringValue(row["id"]))
			removed++
		}
	}
	for _, row := range app.listAttachments("") {
		if terminalAttachmentStatus(stringValue(row["status"])) {
			_ = app.tables.DeleteAttachment(context.Background(), stringValue(row["id"]))
			removed++
		}
	}
	for _, row := range app.listWorkspaces("") {
		if terminalWorkspaceStatus(firstNonEmpty(stringValue(row["state"]), stringValue(row["status"]))) {
			_ = app.tables.DeleteWorkspace(context.Background(), stringValue(row["id"]))
			removed++
		}
	}
	return removed
}

func (app *controlPlaneServer) workspaceCleanupReasonLocked(workspace map[string]any) string {
	if stringValue(workspace["ownerAccountId"]) == "" && stringValue(workspace["accountId"]) == "" {
		return "missing_owner"
	}
	storageID := stringValue(workspace["storageId"])
	storage, _ := app.getStorage(storageID)
	if storageID == "" || storage == nil {
		return "missing_storage"
	}
	if stringValue(storage["status"]) == "destroyed" || stringValue(storage["billingStatus"]) == "stopped" {
		return "storage_destroyed"
	}
	computeID := stringValue(workspace["currentComputeAllocationId"])
	compute, _ := app.getCompute(computeID)
	if computeID != "" && (compute == nil || stringValue(compute["status"]) == "destroyed") {
		return "compute_unavailable"
	}
	attachmentID := stringValue(workspace["currentAttachmentId"])
	attachment, _ := app.getAttachment(attachmentID)
	if attachmentID != "" && (attachment == nil || stringValue(attachment["status"]) == "detached") {
		return "attachment_unavailable"
	}
	return ""
}

func (app *controlPlaneServer) workspaceAccessCleanupSummaryLocked() map[string]any {
	active := 0
	candidates := []any{}
	for _, workspace := range app.listWorkspaces("") {
		id := stringValue(workspace["id"])
		if nested(workspace, "access", "tokenStatus") != "active" {
			continue
		}
		active++
		if reason := app.workspaceCleanupReasonLocked(workspace); reason != "" {
			candidates = append(candidates, map[string]any{"id": id, "workspaceId": id, "accountId": firstNonEmpty(stringValue(workspace["ownerAccountId"]), stringValue(workspace["accountId"])), "reason": reason})
		}
	}
	return map[string]any{
		"activeUrlCount":          active,
		"cleanupCandidateCount":   len(candidates),
		"destroyedComputeCount":   countStatus(app.computeRecordSet(""), "destroyed"),
		"destroyedStorageCount":   countStatus(app.storageRecordSet(""), "destroyed"),
		"detachedAttachmentCount": countStatus(app.attachmentRecordSet(""), "detached"),
		"candidates":              candidates,
	}
}

func (app *controlPlaneServer) resourceAnomaliesLocked() []any {
	rows := []any{}
	for _, candidate := range app.workspaceAccessCleanupSummaryLocked()["candidates"].([]any) {
		row := cloneMap(candidate.(map[string]any))
		row["type"] = "workspace_access"
		row["status"] = row["reason"]
		rows = append(rows, row)
	}
	for _, compute := range app.listComputes("") {
		if stringValue(compute["status"]) == "failed" {
			rows = append(rows, map[string]any{
				"type":        "compute",
				"accountId":   firstNonEmpty(stringValue(compute["ownerAccountId"]), stringValue(compute["accountId"])),
				"workspaceId": compute["workspaceId"],
				"resourceId":  compute["id"],
				"status":      "failed",
			})
		}
	}
	for _, storage := range app.listStorages("") {
		if stringValue(storage["status"]) == "failed" {
			rows = append(rows, map[string]any{
				"type":        "storage",
				"accountId":   firstNonEmpty(stringValue(storage["ownerAccountId"]), stringValue(storage["accountId"])),
				"workspaceId": storage["workspaceId"],
				"resourceId":  storage["id"],
				"status":      "failed",
			})
		}
	}
	for _, attachment := range app.listAttachments("") {
		if stringValue(attachment["status"]) == "failed" {
			rows = append(rows, map[string]any{
				"type":        "attachment",
				"accountId":   firstNonEmpty(stringValue(attachment["ownerAccountId"]), stringValue(attachment["accountId"])),
				"workspaceId": attachment["workspaceId"],
				"resourceId":  attachment["id"],
				"status":      "failed",
			})
		}
	}
	return rows
}
