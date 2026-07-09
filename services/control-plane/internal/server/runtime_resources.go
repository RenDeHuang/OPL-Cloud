package server

import "net/http"
import "time"

func (app *controlPlaneApp) rememberCompute(allocation any) error {
	if row, ok := allocation.(map[string]any); ok {
		app.mu.Lock()
		defer app.mu.Unlock()
		accountID := stringValue(row["accountId"])
		app.computes[stringValue(row["id"])] = row
		if isTerminalResourceStatus(stringValue(row["status"])) {
			app.rememberReleaseLocked(accountID, "compute", stringValue(row["id"]), row)
			app.suspendWorkspacesForComputeLocked(stringValue(row["id"]))
			return app.persistLocked()
		}
		app.rememberHoldLocked(accountID, "compute", stringValue(row["id"]), row)
		return app.persistLocked()
	}
	return nil
}

func (app *controlPlaneApp) rememberStorage(volume any) error {
	if row, ok := volume.(map[string]any); ok {
		app.mu.Lock()
		defer app.mu.Unlock()
		accountID := stringValue(row["accountId"])
		app.storages[stringValue(row["id"])] = row
		if isTerminalResourceStatus(stringValue(row["status"])) {
			app.rememberReleaseLocked(accountID, "storage", stringValue(row["id"]), row)
			app.markWorkspacesStorageDestroyedLocked(stringValue(row["id"]))
			return app.persistLocked()
		}
		app.rememberHoldLocked(accountID, "storage", stringValue(row["id"]), row)
		return app.persistLocked()
	}
	return nil
}

func (app *controlPlaneApp) rememberHoldLocked(accountID string, resourceType string, resourceID string, row map[string]any) {
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

func (app *controlPlaneApp) rememberReleaseLocked(accountID string, resourceType string, resourceID string, row map[string]any) {
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

func (app *controlPlaneApp) rememberAttachment(attachment any, input map[string]any) error {
	if row, ok := attachment.(map[string]any); ok {
		row["computeAllocationId"] = firstNonEmpty(stringValue(row["computeAllocationId"]), stringValue(row["computeId"]), stringField(input, "computeAllocationId", ""))
		row["storageId"] = firstNonEmpty(stringValue(row["storageId"]), stringValue(row["volumeId"]), stringField(input, "storageId", ""))
		row["mountPath"] = firstNonEmpty(stringValue(row["mountPath"]), stringField(input, "mountPath", "/data"))
		app.mu.Lock()
		defer app.mu.Unlock()
		ownerAccountID := app.attachmentAccountIDLocked(row)
		if ownerAccountID != "" {
			row["ownerAccountId"] = ownerAccountID
			row["accountId"] = firstNonEmpty(stringValue(row["accountId"]), ownerAccountID)
		}
		app.attachments[stringValue(row["id"])] = row
		if stringValue(row["status"]) == "detached" {
			app.clearWorkspacesForAttachmentLocked(stringValue(row["id"]))
		}
		return app.persistLocked()
	}
	return nil
}

func (app *controlPlaneApp) attachmentFactsLocked() controlPlaneRecordSet {
	rows := cloneStateTable(app.attachments)
	for _, row := range rows {
		if accountID := app.attachmentAccountIDLocked(row); accountID != "" {
			row["accountId"] = firstNonEmpty(stringValue(row["accountId"]), accountID)
			row["ownerAccountId"] = firstNonEmpty(stringValue(row["ownerAccountId"]), accountID)
		}
	}
	return rows
}

func (app *controlPlaneApp) attachmentAccountIDLocked(row map[string]any) string {
	if row == nil {
		return ""
	}
	if accountID := firstNonEmpty(stringValue(row["accountId"]), stringValue(row["ownerAccountId"])); accountID != "" {
		return accountID
	}
	compute := app.computes[firstNonEmpty(stringValue(row["computeAllocationId"]), stringValue(row["computeId"]))]
	storage := app.storages[firstNonEmpty(stringValue(row["storageId"]), stringValue(row["volumeId"]))]
	workspace := app.workspaces[stringValue(row["workspaceId"])]
	return firstNonEmpty(
		stringValue(compute["accountId"]),
		stringValue(compute["ownerAccountId"]),
		stringValue(storage["accountId"]),
		stringValue(storage["ownerAccountId"]),
		stringValue(workspace["accountId"]),
		stringValue(workspace["ownerAccountId"]),
	)
}

func providerSyncFacts(row map[string]any, err error) map[string]any {
	out := cloneMap(row)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	out["lastProviderSyncAt"] = now
	if err != nil {
		out["providerStatus"] = "sync_failed"
		out["lastProviderSyncError"] = customerSafeProviderError(err)
		return out
	}
	status := stringValue(out["status"])
	out["lastProviderSyncError"] = ""
	if isExternallyDeletedStatus(status) {
		out["providerStatus"] = "missing"
		out["externalDeletedAt"] = firstNonEmpty(stringValue(out["externalDeletedAt"]), now)
		out["billingStatus"] = "stopped"
		return out
	}
	out["providerStatus"] = firstNonEmpty(status, "running")
	return out
}

func isExternallyDeletedStatus(status string) bool {
	return status == "external_deleted" || status == "deleted" || status == "missing"
}

func customerSafeProviderError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (app *controlPlaneApp) resourceBelongsToAccount(row map[string]any, accountID string) bool {
	if accountID == "" {
		return false
	}
	return firstNonEmpty(stringValue(row["accountId"]), stringValue(row["ownerAccountId"])) == accountID
}

func (app *controlPlaneApp) getCompute(id string) (map[string]any, bool) {
	app.mu.Lock()
	defer app.mu.Unlock()
	compute, ok := app.computes[id]
	return cloneMap(compute), ok
}

func (app *controlPlaneApp) getStorage(id string) (map[string]any, bool) {
	app.mu.Lock()
	defer app.mu.Unlock()
	storage, ok := app.storages[id]
	return cloneMap(storage), ok
}

func (app *controlPlaneApp) getAttachment(id string) (map[string]any, bool) {
	app.mu.Lock()
	defer app.mu.Unlock()
	attachment, ok := app.attachments[id]
	return cloneMap(attachment), ok
}

func (app *controlPlaneApp) canAccessResource(r *http.Request, row map[string]any) bool {
	user, ok := app.sessionUserContext(r)
	if !ok {
		return false
	}
	if stringValue(user["role"]) == "admin" {
		return true
	}
	return app.resourceBelongsToAccount(row, stringValue(user["accountId"]))
}
