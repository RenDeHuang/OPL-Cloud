package server

import (
	"context"

	"opl-cloud/services/control-plane/internal/clients"
)

func (app *controlPlaneServer) reconciliationProjectionLocked() map[string]any {
	row, ok, err := app.tables.BillingReconciliation(context.Background())
	if err != nil || !ok {
		return map[string]any{"reports": 0, "guard": map[string]any{"status": "not_required", "blockNewWorkspaces": false, "reason": "billing_reconciliation_not_required"}}
	}
	row["reports"] = 1
	return row
}

func (app *controlPlaneServer) reconciliationBlocksNewWorkspaces() (map[string]any, bool) {
	projection := app.reconciliationProjectionLocked()
	guard, _ := projection["guard"].(map[string]any)
	blocked, _ := guard["blockNewWorkspaces"].(bool)
	return projection, blocked
}

func (app *controlPlaneServer) rememberReconciliation(result clients.ReconciliationResult) error {
	return app.tables.SaveBillingReconciliation(context.Background(), reconciliationResponse(result))
}

func (app *controlPlaneServer) resourceLedgerEvidenceLocked(accountIDs ...string) []any {
	rows := []any{}
	for _, workspace := range app.listWorkspaces("") {
		if len(accountIDs) > 0 && !app.resourceBelongsToAccount(workspace, accountIDs[0]) {
			continue
		}
		workspaceID := stringValue(workspace["id"])
		computeID := stringValue(workspace["currentComputeAllocationId"])
		storageID := stringValue(workspace["storageId"])
		attachmentID := stringValue(workspace["currentAttachmentId"])
		compute, _ := app.getCompute(computeID)
		storage, _ := app.getStorage(storageID)
		attachment, _ := app.getAttachment(attachmentID)
		operation := app.operationEvidenceForResourceLocked(workspaceID, computeID, storageID, attachmentID)
		ownerAccountID := firstNonEmpty(stringValue(workspace["ownerAccountId"]), stringValue(compute["ownerAccountId"]), stringValue(storage["ownerAccountId"]), stringValue(attachment["ownerAccountId"]))
		costTags := firstNonNil(operation["costTags"], compute["costTags"], storage["costTags"], attachment["costTags"])
		if !hasProviderCostTags(costTags) {
			costTags = providerCostTags(ownerAccountID, workspaceID, firstNonEmpty(stringValue(operation["resourceId"]), workspaceID), stringValue(operation["operationId"]))
		}
		rows = append(rows, map[string]any{
			"id": firstNonEmpty(workspaceID, computeID, storageID, attachmentID), "accountId": ownerAccountID,
			"ownerAccountId": ownerAccountID, "ownerUserId": firstNonEmpty(stringValue(workspace["ownerUserId"]), stringValue(compute["ownerUserId"]), stringValue(storage["ownerUserId"])),
			"workspaceId": workspaceID, "workspaceIds": uniqueStrings([]string{workspaceID}),
			"computeAllocationId": computeID, "storageId": storageID, "attachmentId": attachmentID,
			"cvmInstanceId": firstNonEmpty(stringValue(compute["cvmInstanceId"]), stringValue(compute["providerResourceId"])),
			"nodeName": firstNonEmpty(stringValue(compute["nodeName"]), stringValue(compute["machineName"])),
			"providerRequestId": firstNonEmpty(stringValue(compute["providerRequestId"]), stringValue(storage["providerRequestId"]), stringValue(attachment["providerRequestId"])),
			"operationId": firstNonEmpty(stringValue(operation["operationId"]), stringValue(compute["operationId"]), stringValue(storage["operationId"]), stringValue(attachment["operationId"])),
			"costTags": costTags,
			"receiptIds": uniqueStrings([]string{stringValue(compute["lastReceiptId"]), stringValue(storage["lastReceiptId"]), stringValue(workspace["receiptId"])}),
		})
	}
	return rows
}

func hasProviderCostTags(tags any) bool {
	return costTagValue(tags, "opl_account_id") != "" && costTagValue(tags, "opl_workspace_id") != "" && costTagValue(tags, "opl_resource_id") != "" && costTagValue(tags, "opl_operation_id") != ""
}

func costTagValue(tags any, key string) string {
	switch typed := tags.(type) {
	case map[string]any:
		return stringValue(typed[key])
	case map[string]string:
		return typed[key]
	default:
		return ""
	}
}

func providerCostTags(accountID, workspaceID, resourceID, operationID string) map[string]any {
	return map[string]any{"opl_account_id": accountID, "opl_workspace_id": workspaceID, "opl_resource_id": resourceID, "opl_operation_id": operationID}
}

func (app *controlPlaneServer) operationEvidenceForResourceLocked(ids ...string) map[string]any {
	operations := app.listRuntimeOperations()
	for index := len(operations) - 1; index >= 0; index-- {
		operation := operations[index]
		if mapContainsAnyID(operation, ids...) {
			payload, _ := operation["redactedProviderPayload"].(map[string]any)
			return map[string]any{"operationId": operation["operationId"], "resourceId": operation["resourceId"], "costTags": firstNonNil(operation["costTags"], payload["costTags"])}
		}
	}
	return map[string]any{}
}
