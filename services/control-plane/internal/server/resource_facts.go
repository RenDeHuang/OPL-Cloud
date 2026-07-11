package server

import "context"
import "net/http"
import "time"

func (app *controlPlaneServer) saveComputeFact(allocation any) error {
	if row, ok := allocation.(map[string]any); ok {
		accountID := stringValue(row["accountId"])
		if isTerminalResourceStatus(stringValue(row["status"])) {
			if err := app.saveReleaseProjection(accountID, "compute", stringValue(row["id"]), row); err != nil {
				return err
			}
			if err := app.suspendWorkspacesForCompute(stringValue(row["id"])); err != nil {
				return err
			}
			return app.tables.SaveCompute(context.Background(), row)
		}
		if err := app.saveHoldProjection(accountID, "compute", stringValue(row["id"]), row); err != nil {
			return err
		}
		return app.tables.SaveCompute(context.Background(), row)
	}
	return nil
}

func (app *controlPlaneServer) saveStorageFact(volume any) error {
	if row, ok := volume.(map[string]any); ok {
		accountID := stringValue(row["accountId"])
		if isTerminalResourceStatus(stringValue(row["status"])) {
			if err := app.saveReleaseProjection(accountID, "storage", stringValue(row["id"]), row); err != nil {
				return err
			}
			if err := app.markWorkspacesStorageDestroyed(stringValue(row["id"])); err != nil {
				return err
			}
			return app.tables.SaveStorage(context.Background(), row)
		}
		if err := app.saveHoldProjection(accountID, "storage", stringValue(row["id"]), row); err != nil {
			return err
		}
		return app.tables.SaveStorage(context.Background(), row)
	}
	return nil
}

func (app *controlPlaneServer) saveHoldProjection(accountID string, resourceType string, resourceID string, row map[string]any) error {
	holdID := stringValue(row["holdId"])
	if accountID == "" || holdID == "" {
		return nil
	}
	if wallet, ok := row["wallet"].(map[string]any); ok {
		if err := app.tables.SaveWallet(context.Background(), walletProjection(walletFromMap(wallet))); err != nil {
			return err
		}
	}
	ledger := map[string]any{"id": holdID, "accountId": accountID, "type": resourceType + "_hold", "resourceId": resourceID, "amountCents": int64(numberField(row, "holdAmountCents", 0))}
	if resourceType == "storage" {
		ledger["storageId"] = resourceID
	} else {
		ledger["computeAllocationId"] = resourceID
	}
	return app.tables.SaveLedgerEntry(context.Background(), ledger)
}

func (app *controlPlaneServer) saveReleaseProjection(accountID string, resourceType string, resourceID string, row map[string]any) error {
	releaseID := stringValue(row["holdReleaseId"])
	if accountID == "" || releaseID == "" {
		return nil
	}
	if wallet, ok := row["wallet"].(map[string]any); ok {
		if err := app.tables.SaveWallet(context.Background(), walletProjection(walletFromMap(wallet))); err != nil {
			return err
		}
	}
	ledger := map[string]any{"id": releaseID, "accountId": accountID, "type": resourceType + "_hold_released", "resourceId": resourceID, "amountCents": int64(numberField(row, "holdAmountCents", 0))}
	if resourceType == "storage" {
		ledger["storageId"] = resourceID
	} else {
		ledger["computeAllocationId"] = resourceID
	}
	return app.tables.SaveLedgerEntry(context.Background(), ledger)
}

func (app *controlPlaneServer) saveAttachmentFact(attachment any, input map[string]any) error {
	if row, ok := attachment.(map[string]any); ok {
		row["computeAllocationId"] = firstNonEmpty(stringValue(row["computeAllocationId"]), stringValue(row["computeId"]), stringField(input, "computeAllocationId", ""))
		row["storageId"] = firstNonEmpty(stringValue(row["storageId"]), stringValue(row["volumeId"]), stringField(input, "storageId", ""))
		row["mountPath"] = firstNonEmpty(stringValue(row["mountPath"]), stringField(input, "mountPath", "/data"))
		ownerAccountID := app.attachmentAccountID(row)
		if ownerAccountID != "" {
			row["ownerAccountId"] = ownerAccountID
			row["accountId"] = firstNonEmpty(stringValue(row["accountId"]), ownerAccountID)
		}
		if stringValue(row["status"]) == "detached" {
			if err := app.clearWorkspacesForAttachment(stringValue(row["id"])); err != nil {
				return err
			}
		}
		return app.tables.SaveAttachment(context.Background(), row)
	}
	return nil
}

func (app *controlPlaneServer) attachmentFactsLocked() controlPlaneRecordSet {
	rows := app.attachmentRecordSet("")
	for _, row := range rows {
		if accountID := app.attachmentAccountID(row); accountID != "" {
			row["accountId"] = firstNonEmpty(stringValue(row["accountId"]), accountID)
			row["ownerAccountId"] = firstNonEmpty(stringValue(row["ownerAccountId"]), accountID)
		}
	}
	return rows
}

func (app *controlPlaneServer) attachmentAccountID(row map[string]any) string {
	if row == nil {
		return ""
	}
	if accountID := firstNonEmpty(stringValue(row["accountId"]), stringValue(row["ownerAccountId"])); accountID != "" {
		return accountID
	}
	compute, _ := app.getCompute(firstNonEmpty(stringValue(row["computeAllocationId"]), stringValue(row["computeId"])))
	storage, _ := app.getStorage(firstNonEmpty(stringValue(row["storageId"]), stringValue(row["volumeId"])))
	workspace, _ := app.getWorkspace(stringValue(row["workspaceId"]))
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

func (app *controlPlaneServer) resourceBelongsToAccount(row map[string]any, accountID string) bool {
	if accountID == "" {
		return false
	}
	return firstNonEmpty(stringValue(row["accountId"]), stringValue(row["ownerAccountId"])) == accountID
}

func (app *controlPlaneServer) getCompute(id string) (map[string]any, bool) {
	for _, compute := range app.listComputes("") {
		if stringValue(compute["id"]) == id {
			return cloneMap(compute), true
		}
	}
	return nil, false
}

func (app *controlPlaneServer) getStorage(id string) (map[string]any, bool) {
	for _, storage := range app.listStorages("") {
		if stringValue(storage["id"]) == id {
			return cloneMap(storage), true
		}
	}
	return nil, false
}

func (app *controlPlaneServer) getAttachment(id string) (map[string]any, bool) {
	for _, attachment := range app.listAttachments("") {
		if stringValue(attachment["id"]) == id {
			return cloneMap(attachment), true
		}
	}
	return nil, false
}

func (app *controlPlaneServer) canAccessResource(r *http.Request, row map[string]any) bool {
	user, ok := app.sessionUserContext(r)
	if !ok {
		return false
	}
	return app.resourceBelongsToAccount(row, stringValue(user["accountId"]))
}
