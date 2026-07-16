package server

import (
	"context"
	"errors"
	"sort"
	"strings"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

const maxReconciliationReceiptPages = 100

type billingReconciliationResource struct {
	resourceType string
	row          map[string]any
}

type billingReconciliationException struct {
	resourceType string
	resourceID   string
	code         string
}

type billingReconciliationAccountFacts struct {
	userID       int64
	history      []clients.Sub2APIBalanceHistoryEntry
	historyError bool
	receipts     []clients.Receipt
	receiptError bool
}

func (app *controlPlaneServer) billingReconciliationReport(ctx context.Context, service *controlplane.Service, idempotencyKey string) (map[string]any, error) {
	computes, err := app.tables.ListComputes(ctx, "")
	if err != nil {
		return nil, err
	}
	storages, err := app.tables.ListStorages(ctx, "")
	if err != nil {
		return nil, err
	}
	resources := make([]billingReconciliationResource, 0, len(computes)+len(storages))
	for _, row := range computes {
		if stringValue(row["billingStatus"]) == "active" {
			resources = append(resources, billingReconciliationResource{resourceType: "compute", row: row})
		}
	}
	for _, row := range storages {
		if stringValue(row["billingStatus"]) == "active" {
			resources = append(resources, billingReconciliationResource{resourceType: "storage", row: row})
		}
	}
	sort.Slice(resources, func(i, j int) bool {
		if resources[i].resourceType != resources[j].resourceType {
			return resources[i].resourceType < resources[j].resourceType
		}
		return stringValue(resources[i].row["id"]) < stringValue(resources[j].row["id"])
	})

	reportID := "reconciliation-" + stableID(idempotencyKey)[:18]
	if len(resources) == 0 {
		return reconciliationReport(reportID, 0, 0, nil), nil
	}
	operations, fabricErr := service.FabricOperations(ctx)
	accountFacts := map[string]billingReconciliationAccountFacts{}
	for _, resource := range resources {
		accountID := stringValue(resource.row["accountId"])
		if _, loaded := accountFacts[accountID]; loaded {
			continue
		}
		facts := billingReconciliationAccountFacts{}
		userID, err := app.sub2APIUserID(ctx, accountID)
		if err != nil {
			facts.historyError = true
		} else {
			facts.userID = userID
			facts.history, err = service.Sub2APIBalanceHistory(ctx, userID)
			facts.historyError = err != nil
		}
		facts.receipts, err = reconciliationLedgerReceipts(ctx, service, accountID)
		facts.receiptError = err != nil
		accountFacts[accountID] = facts
	}

	exceptions := make([]billingReconciliationException, 0)
	matched := 0
	for _, resource := range resources {
		before := len(exceptions)
		row := resource.row
		accountID := stringValue(row["accountId"])
		if !validLocalBillingReconciliationFact(resource.resourceType, row) {
			exceptions = append(exceptions, newBillingReconciliationException(resource, "billing_operation_invalid"))
			continue
		}
		facts := accountFacts[accountID]
		if facts.historyError {
			exceptions = append(exceptions, newBillingReconciliationException(resource, "sub2api_balance_history_unavailable"))
		} else if code := sub2APIReconciliationCode(row, facts.userID, facts.history); code != "" {
			exceptions = append(exceptions, newBillingReconciliationException(resource, code))
		}
		if fabricErr != nil {
			exceptions = append(exceptions, newBillingReconciliationException(resource, "fabric_operations_unavailable"))
		} else if code := fabricReconciliationCode(resource.resourceType, row, operations); code != "" {
			exceptions = append(exceptions, newBillingReconciliationException(resource, code))
		}
		if facts.receiptError {
			exceptions = append(exceptions, newBillingReconciliationException(resource, "ledger_receipts_unavailable"))
		} else if code := ledgerReconciliationCode(resource.resourceType, row, facts.receipts); code != "" {
			exceptions = append(exceptions, newBillingReconciliationException(resource, code))
		}
		if len(exceptions) == before {
			matched++
		}
	}
	sort.Slice(exceptions, func(i, j int) bool {
		if exceptions[i].resourceType != exceptions[j].resourceType {
			return exceptions[i].resourceType < exceptions[j].resourceType
		}
		if exceptions[i].resourceID != exceptions[j].resourceID {
			return exceptions[i].resourceID < exceptions[j].resourceID
		}
		return exceptions[i].code < exceptions[j].code
	})
	return reconciliationReport(reportID, len(resources), matched, exceptions), nil
}

func reconciliationReport(id string, checked, matched int, exceptions []billingReconciliationException) map[string]any {
	status := "ok"
	if len(exceptions) > 0 {
		status = "mismatch"
	}
	items := make([]any, 0, len(exceptions))
	for _, exception := range exceptions {
		items = append(items, map[string]any{"resourceType": exception.resourceType, "resourceId": exception.resourceID, "code": exception.code})
	}
	return map[string]any{
		"id": id, "status": status,
		"counts":     map[string]any{"billingOperations": checked, "matched": matched, "exceptions": len(exceptions)},
		"exceptions": items,
	}
}

func newBillingReconciliationException(resource billingReconciliationResource, code string) billingReconciliationException {
	return billingReconciliationException{resourceType: resource.resourceType, resourceID: stringValue(resource.row["id"]), code: code}
}

func validLocalBillingReconciliationFact(resourceType string, row map[string]any) bool {
	charge, validCharge := requiredNonNegativeInteger(row, "chargeUsdMicros")
	return (resourceType == "compute" || resourceType == "storage") && stringValue(row["id"]) != "" && stringValue(row["accountId"]) != "" &&
		stringValue(row["workspaceId"]) != "" && stringValue(row["billingOperationId"]) != "" && stringValue(row["sub2apiRedeemCode"]) != "" &&
		validCharge && charge > 0 && stringValue(row["provider"]) != "" && stringValue(row["providerRequestId"]) != "" && stringValue(row["providerResourceId"]) != "" &&
		stringValue(row["lastReceiptId"]) != ""
}

func sub2APIReconciliationCode(row map[string]any, userID int64, history []clients.Sub2APIBalanceHistoryEntry) string {
	matches := make([]clients.Sub2APIBalanceHistoryEntry, 0, 1)
	for _, entry := range history {
		if entry.Code == stringValue(row["sub2apiRedeemCode"]) {
			matches = append(matches, entry)
		}
	}
	if len(matches) == 0 {
		return "sub2api_charge_missing"
	}
	charge, validCharge := requiredNonNegativeInteger(row, "chargeUsdMicros")
	if len(matches) != 1 || !validCharge || matches[0].Type != "balance" || matches[0].Status != "used" || matches[0].UsedBy == nil || *matches[0].UsedBy != userID || matches[0].ValueUSDMicros != -charge {
		return "sub2api_charge_mismatch"
	}
	return ""
}

func fabricReconciliationCode(resourceType string, row map[string]any, operations []clients.FabricOperation) string {
	action, kind, keySuffix := "create_compute_allocation", "compute_allocation", ":prepare"
	if resourceType == "storage" {
		action, kind = "create_storage_volume", "storage_volume"
	}
	if strings.HasPrefix(stringValue(row["billingOperationId"]), "renewal-") {
		action, keySuffix = "renew_compute_allocation", ":provider-renew"
		if resourceType == "storage" {
			action = "renew_storage_volume"
		}
	}
	matches := make([]clients.FabricOperation, 0, 1)
	for _, operation := range operations {
		if operation.Action == action && operation.ResourceKind == kind && operation.ResourceID == stringValue(row["id"]) && operation.IdempotencyKey == stringValue(row["billingOperationId"])+keySuffix && operation.Status == "succeeded" {
			matches = append(matches, operation)
		}
	}
	if len(matches) == 0 {
		return "fabric_operation_missing"
	}
	operation := matches[0]
	if len(matches) != 1 || operation.CallerService != "control-plane" || operation.AccountID != stringValue(row["accountId"]) || operation.WorkspaceID != stringValue(row["workspaceId"]) ||
		operation.Provider != stringValue(row["provider"]) || operation.ProviderRequestID != stringValue(row["providerRequestId"]) ||
		stringValue(operation.RedactedProviderPayload["providerResourceId"]) != stringValue(row["providerResourceId"]) {
		return "fabric_operation_mismatch"
	}
	return ""
}

func ledgerReconciliationCode(resourceType string, row map[string]any, receipts []clients.Receipt) string {
	matches := make([]clients.Receipt, 0, 1)
	for _, receipt := range receipts {
		if receipt.ReceiptID == stringValue(row["lastReceiptId"]) {
			matches = append(matches, receipt)
		}
	}
	if len(matches) == 0 {
		return "ledger_receipt_missing"
	}
	receipt := matches[0]
	expectedType := "billing.resource_purchased.v1"
	if strings.HasPrefix(stringValue(row["billingOperationId"]), "renewal-") {
		expectedType = "billing.resource_renewed.v1"
	}
	charge, validCharge := requiredNonNegativeInteger(receipt.Cost, "chargeUsdMicros")
	expectedCharge, validExpectedCharge := requiredNonNegativeInteger(row, "chargeUsdMicros")
	if len(matches) != 1 || receipt.Type != expectedType || receipt.Status != "completed" || receipt.AccountID != stringValue(row["accountId"]) ||
		receipt.WorkspaceID != stringValue(row["workspaceId"]) || receipt.RequestID != stringValue(row["billingOperationId"]) ||
		stringValue(receipt.Cost["resourceType"]) != resourceType || stringValue(receipt.Cost["resourceId"]) != stringValue(row["id"]) ||
		!validCharge || !validExpectedCharge || charge != expectedCharge {
		return "ledger_receipt_mismatch"
	}
	return ""
}

func reconciliationLedgerReceipts(ctx context.Context, service *controlplane.Service, accountID string) ([]clients.Receipt, error) {
	// ponytail: 10k rows bound a manual Pilot audit; add a batched receipt-ID API only if this ceiling is reached.
	receipts := make([]clients.Receipt, 0)
	cursor := ""
	seen := map[string]bool{}
	for pageNumber := 0; pageNumber < maxReconciliationReceiptPages; pageNumber++ {
		page, err := service.BillingReceipts(ctx, clients.ReceiptQuery{AccountID: accountID, Cursor: cursor, Limit: 100})
		if err != nil {
			return nil, err
		}
		for _, receipt := range page.Receipts {
			if receipt.AccountID != accountID {
				return nil, errors.New("ledger_receipt_identity_mismatch")
			}
			receipts = append(receipts, receipt)
		}
		if !page.HasMore {
			return receipts, nil
		}
		if page.NextCursor == "" || seen[page.NextCursor] {
			return nil, errors.New("ledger_receipt_pagination_invalid")
		}
		seen[page.NextCursor] = true
		cursor = page.NextCursor
	}
	return nil, errors.New("ledger_receipt_page_limit_exceeded")
}

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
			"cvmInstanceId":     firstNonEmpty(stringValue(compute["cvmInstanceId"]), stringValue(compute["providerResourceId"])),
			"nodeName":          firstNonEmpty(stringValue(compute["nodeName"]), stringValue(compute["machineName"])),
			"providerRequestId": firstNonEmpty(stringValue(compute["providerRequestId"]), stringValue(storage["providerRequestId"]), stringValue(attachment["providerRequestId"])),
			"operationId":       firstNonEmpty(stringValue(operation["operationId"]), stringValue(compute["operationId"]), stringValue(storage["operationId"]), stringValue(attachment["operationId"])),
			"costTags":          costTags,
			"receiptIds":        uniqueStrings([]string{stringValue(compute["lastReceiptId"]), stringValue(storage["lastReceiptId"]), stringValue(workspace["receiptId"])}),
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
