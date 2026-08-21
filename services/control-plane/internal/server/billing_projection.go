package server

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

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
	history      map[string]clients.Sub2APIBalanceHistoryEntry
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
	workspaces, err := app.tables.ListWorkspaces(ctx, "")
	if err != nil {
		return nil, err
	}
	runtimeOperations, err := queryRuntimeOperations(ctx, app.tables, runtimeOperationQuery{Action: "workspace.renewal"})
	if err != nil {
		return nil, err
	}
	resources := make([]billingReconciliationResource, 0, len(computes)+len(storages)+len(workspaces))
	workspaceRenewals := map[string]workspaceRenewalOperation{}
	for _, row := range runtimeOperations {
		operation, decodeErr := decodeWorkspaceRenewalOperation(row)
		workspace := findRecord(workspaces, operation.WorkspaceID)
		workspacePaidThrough, workspaceTimeErr := time.Parse(time.RFC3339, stringValue(workspace["paidThrough"]))
		renewedThrough, renewedTimeErr := time.Parse(time.RFC3339, operation.RenewedThrough)
		if decodeErr != nil || workspace == nil || (operation.Status != "active" && !(operation.Status == "verifying" && operation.EntitlementCommitted)) ||
			workspaceTimeErr != nil || renewedTimeErr != nil || !workspacePaidThrough.Equal(renewedThrough) {
			continue
		}
		if current, ok := workspaceRenewals[operation.WorkspaceID]; !ok || current.PaidThrough < operation.PaidThrough {
			workspaceRenewals[operation.WorkspaceID] = operation
		}
	}
	workspaceChildren := map[string]bool{}
	for workspaceID, operation := range workspaceRenewals {
		workspace := findRecord(workspaces, workspaceID)
		resources = append(resources, billingReconciliationResource{resourceType: "workspace", row: workspaceRenewalBillingReconciliationRow(workspace, operation)})
		workspaceChildren["compute\x00"+operation.ComputeID] = true
		workspaceChildren["storage\x00"+operation.StorageID] = true
	}
	for _, row := range computes {
		if stringValue(row["billingStatus"]) == "active" && !workspaceChildren["compute\x00"+stringValue(row["id"])] {
			resources = append(resources, billingReconciliationResource{resourceType: "compute", row: row})
		}
	}
	for _, row := range storages {
		if stringValue(row["billingStatus"]) == "active" && !workspaceChildren["storage\x00"+stringValue(row["id"])] {
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
	providerInputs := make([]clients.ProviderFactInput, 0, len(resources)*2)
	for _, resource := range resources {
		providerInputs = append(providerInputs, billingReconciliationProviderInputs(resource)...)
	}
	providerFacts, fabricErr := readProviderFacts(ctx, service, uniqueProviderFactInputs(providerInputs))
	accountCodes := make(map[string][]string)
	seenAccountCodes := make(map[string]map[string]struct{})
	for _, resource := range resources {
		accountID := stringValue(resource.row["accountId"])
		code := stringValue(resource.row["sub2apiRedeemCode"])
		if accountID == "" || code == "" {
			continue
		}
		if seenAccountCodes[accountID] == nil {
			seenAccountCodes[accountID] = make(map[string]struct{})
		}
		if _, seen := seenAccountCodes[accountID][code]; seen {
			continue
		}
		seenAccountCodes[accountID][code] = struct{}{}
		accountCodes[accountID] = append(accountCodes[accountID], code)
	}
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
			facts.history, err = service.FinancialBalanceHistoryByCodes(ctx, userID, accountCodes[accountID])
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
			exceptions = append(exceptions, newBillingReconciliationException(resource, "fabric_provider_facts_unavailable"))
		} else if code := fabricReconciliationCode(resource, providerFacts); code != "" {
			exceptions = append(exceptions, newBillingReconciliationException(resource, code))
		}
		if facts.receiptError {
			exceptions = append(exceptions, newBillingReconciliationException(resource, "ledger_receipts_unavailable"))
		} else if code := ledgerReconciliationCode(resource.resourceType, row, facts.userID, facts.receipts); code != "" {
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

func workspaceRenewalBillingReconciliationRow(workspace map[string]any, operation workspaceRenewalOperation) map[string]any {
	return map[string]any{
		"id": operation.WorkspaceID, "accountId": operation.AccountID, "workspaceId": operation.WorkspaceID,
		"billingOperationId": operation.ID, "sub2apiRedeemCode": operation.RedeemCode, "chargeUsdMicros": operation.TotalUSDMicros,
		"sub2apiUserId": int64(numberField(operation.ChargeConfirmation, "userId", 0)), "postChargeBalanceUsdMicros": operation.PostChargeBalanceUSDMicros,
		"lastReceiptId": operation.ReceiptID, "priceVersion": operation.PriceVersion, "periodStart": operation.PaidThrough, "paidThrough": operation.RenewedThrough,
		"computeAllocationId": operation.ComputeID, "storageId": operation.StorageID, "storageGb": operation.StorageGB,
		"computeChargeUsdMicros": operation.ComputeUSDMicros, "storageChargeUsdMicros": operation.StorageUSDMicros,
		"workspaceRenewalStatus": operation.Status, "workspaceState": stringValue(workspace["state"]),
	}
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
	if resourceType == "workspace" {
		computeCharge, validComputeCharge := requiredNonNegativeInteger(row, "computeChargeUsdMicros")
		storageCharge, validStorageCharge := requiredNonNegativeInteger(row, "storageChargeUsdMicros")
		sub2APIUserID, validSub2APIUserID := requiredNonNegativeInteger(row, "sub2apiUserId")
		_, validPostChargeBalance := requiredNonNegativeInteger(row, "postChargeBalanceUsdMicros")
		return stringValue(row["id"]) != "" && stringValue(row["accountId"]) != "" && stringValue(row["workspaceId"]) == stringValue(row["id"]) &&
			stringValue(row["billingOperationId"]) != "" && stringValue(row["sub2apiRedeemCode"]) != "" && validCharge && validComputeCharge && validStorageCharge &&
			validSub2APIUserID && validPostChargeBalance && sub2APIUserID > 0 && charge > 0 && computeCharge > 0 && storageCharge > 0 && charge == computeCharge+storageCharge && stringValue(row["priceVersion"]) != "" &&
			stringValue(row["computeAllocationId"]) != "" && stringValue(row["storageId"]) != "" && numberField(row, "storageGb", 0) > 0
	}
	return (resourceType == "compute" || resourceType == "storage") && stringValue(row["id"]) != "" && stringValue(row["accountId"]) != "" &&
		stringValue(row["workspaceId"]) != "" && stringValue(row["billingOperationId"]) != "" && stringValue(row["sub2apiRedeemCode"]) != "" &&
		validCharge && charge > 0 && stringValue(row["providerResourceId"]) != "" &&
		stringValue(row["lastReceiptId"]) != ""
}

func sub2APIReconciliationCode(row map[string]any, userID int64, history map[string]clients.Sub2APIBalanceHistoryEntry) string {
	entry, found := history[stringValue(row["sub2apiRedeemCode"])]
	if !found {
		return "sub2api_charge_missing"
	}
	charge, validCharge := requiredNonNegativeInteger(row, "chargeUsdMicros")
	if !validCharge || entry.Type != "balance" || entry.Status != "used" || entry.UsedBy == nil || *entry.UsedBy != userID || entry.ValueUSDMicros != -charge {
		return "sub2api_charge_mismatch"
	}
	return ""
}

func billingReconciliationProviderInputs(resource billingReconciliationResource) []clients.ProviderFactInput {
	row := resource.row
	accountID, workspaceID := stringValue(row["accountId"]), stringValue(row["workspaceId"])
	if resource.resourceType == "workspace" {
		return []clients.ProviderFactInput{
			{AccountID: accountID, WorkspaceID: workspaceID, ResourceType: "compute", ResourceID: stringValue(row["computeAllocationId"])},
			{AccountID: accountID, WorkspaceID: workspaceID, ResourceType: "storage", ResourceID: stringValue(row["storageId"])},
		}
	}
	return []clients.ProviderFactInput{{
		AccountID: accountID, WorkspaceID: workspaceID, ResourceType: resource.resourceType, ResourceID: stringValue(row["id"]),
	}}
}

func fabricReconciliationCode(resource billingReconciliationResource, facts map[string]clients.ProviderFact) string {
	paidThrough, err := time.Parse(time.RFC3339, stringValue(resource.row["paidThrough"]))
	if err != nil {
		return "fabric_provider_fact_mismatch"
	}
	for _, input := range billingReconciliationProviderInputs(resource) {
		fact, ok := facts[providerFactKey(input)]
		if !ok {
			return "fabric_provider_fact_missing"
		}
		if providerFactConfirmedAbsent(input, fact) {
			return "fabric_provider_fact_missing"
		}
		expectedProviderID := ""
		if resource.resourceType != "workspace" {
			expectedProviderID = stringValue(resource.row["providerResourceId"])
		}
		if !providerFactCovers(input, fact, expectedProviderID, paidThrough) {
			return "fabric_provider_fact_mismatch"
		}
	}
	return ""
}

func ledgerReconciliationCode(resourceType string, row map[string]any, currentUserID int64, receipts []clients.Receipt) string {
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
	if resourceType == "workspace" {
		total, validTotal := requiredNonNegativeInteger(receipt.Cost, "totalUsdMicros")
		expectedTotal, validExpectedTotal := requiredNonNegativeInteger(row, "chargeUsdMicros")
		components := mapField(receipt.Cost, "components")
		compute := mapField(components, "compute")
		storage := mapField(components, "storage")
		computeCharge, validComputeCharge := requiredNonNegativeInteger(compute, "chargeUsdMicros")
		storageCharge, validStorageCharge := requiredNonNegativeInteger(storage, "chargeUsdMicros")
		expectedComputeCharge, validExpectedComputeCharge := requiredNonNegativeInteger(row, "computeChargeUsdMicros")
		expectedStorageCharge, validExpectedStorageCharge := requiredNonNegativeInteger(row, "storageChargeUsdMicros")
		sub2APIUserID, validSub2APIUserID := requiredNonNegativeInteger(receipt.Cost, "sub2apiUserId")
		expectedSub2APIUserID, validExpectedSub2APIUserID := requiredNonNegativeInteger(row, "sub2apiUserId")
		postChargeBalance, validPostChargeBalance := requiredNonNegativeInteger(receipt.Cost, "postChargeBalanceUsdMicros")
		expectedPostChargeBalance, validExpectedPostChargeBalance := requiredNonNegativeInteger(row, "postChargeBalanceUsdMicros")
		if len(matches) != 1 || receipt.Type != "billing.workspace_renewed.v1" || receipt.Status != "completed" ||
			receipt.AccountID != stringValue(row["accountId"]) || receipt.WorkspaceID != stringValue(row["workspaceId"]) || receipt.RequestID != stringValue(row["billingOperationId"]) ||
			stringValue(receipt.Cost["resourceType"]) != "workspace" || stringValue(receipt.Cost["resourceId"]) != stringValue(row["id"]) ||
			stringValue(receipt.Cost["priceVersion"]) != stringValue(row["priceVersion"]) || stringValue(receipt.Cost["currency"]) != pricingCurrency || stringValue(receipt.Cost["billingUnit"]) != pricingBillingUnit ||
			stringValue(receipt.Cost["sub2apiRedeemCode"]) != stringValue(row["sub2apiRedeemCode"]) || !validSub2APIUserID || !validExpectedSub2APIUserID ||
			sub2APIUserID != expectedSub2APIUserID || sub2APIUserID != currentUserID ||
			!validPostChargeBalance || !validExpectedPostChargeBalance || postChargeBalance != expectedPostChargeBalance ||
			stringValue(receipt.Cost["periodStart"]) != stringValue(row["periodStart"]) || stringValue(receipt.Cost["paidThrough"]) != stringValue(row["paidThrough"]) ||
			!validTotal || !validExpectedTotal || total != expectedTotal || !validComputeCharge || !validExpectedComputeCharge || computeCharge != expectedComputeCharge ||
			!validStorageCharge || !validExpectedStorageCharge || storageCharge != expectedStorageCharge || stringValue(compute["resourceType"]) != "compute" || stringValue(storage["resourceType"]) != "storage" ||
			stringValue(compute["resourceId"]) != stringValue(row["computeAllocationId"]) || stringValue(storage["resourceId"]) != stringValue(row["storageId"]) ||
			int64(numberField(storage, "sizeGb", -1)) != int64(numberField(row, "storageGb", -2)) {
			return "ledger_receipt_mismatch"
		}
		return ""
	}
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
	if err != nil {
		return map[string]any{"reports": 0, "guard": map[string]any{"status": "unavailable", "blockNewWorkspaces": true, "reason": "billing_reconciliation_unavailable"}}
	}
	if !ok {
		return map[string]any{"reports": 0, "guard": map[string]any{"status": "not_required", "blockNewWorkspaces": false, "reason": "billing_reconciliation_not_required"}}
	}
	if _, err := billingReconciliationBlockState(row); err != nil {
		return map[string]any{"reports": 0, "guard": map[string]any{"status": "unavailable", "blockNewWorkspaces": true, "reason": "billing_reconciliation_invalid"}}
	}
	row["reports"] = 1
	return row
}

func (app *controlPlaneServer) reconciliationBlocksNewWorkspaces(ctx context.Context) (map[string]any, bool, error) {
	row, ok, err := app.tables.BillingReconciliation(ctx)
	if err != nil {
		return nil, true, err
	}
	if !ok {
		projection := map[string]any{"reports": 0, "guard": map[string]any{"status": "not_required", "blockNewWorkspaces": false, "reason": "billing_reconciliation_not_required"}}
		return projection, false, nil
	}
	blocked, err := billingReconciliationBlockState(row)
	if err != nil {
		return nil, true, err
	}
	row["reports"] = 1
	return row, blocked, nil
}

func billingReconciliationBlockState(row map[string]any) (bool, error) {
	status := stringValue(row["status"])
	guard := mapField(row, "guard")
	guardStatus, reason := stringValue(guard["status"]), stringValue(guard["reason"])
	blocked, hasBlocked := guard["blockNewWorkspaces"].(bool)
	if stringValue(row["id"]) == "" || (status != "ok" && status != "mismatch") || guardStatus != status || reason == "" || !hasBlocked || blocked != (status == "mismatch") {
		return true, errors.New("billing_reconciliation_guard_invalid")
	}
	return blocked, nil
}

func validateBillingReconciliationMutation(mutation billingReconciliationMutation) error {
	row, audit := mutation.Row, mutation.AuditEvent
	if _, err := billingReconciliationBlockState(row); err != nil {
		return err
	}
	report := mapField(row, "report")
	if stringValue(report["id"]) != stringValue(row["id"]) {
		return errors.New("billing_reconciliation_report_invalid")
	}
	if !billingReconciliationAuditIdentityMatches(audit, row) {
		return errors.New("billing_reconciliation_audit_invalid")
	}
	if _, err := time.Parse(time.RFC3339, stringValue(audit["createdAt"])); err != nil {
		return errors.New("billing_reconciliation_audit_invalid")
	}
	return nil
}

func billingReconciliationIdentityMatches(current, desired map[string]any) bool {
	currentBlocked, currentErr := billingReconciliationBlockState(current)
	desiredBlocked, desiredErr := billingReconciliationBlockState(desired)
	return currentErr == nil && desiredErr == nil && stringValue(current["id"]) == stringValue(desired["id"]) &&
		stringValue(current["status"]) == stringValue(desired["status"]) && currentBlocked == desiredBlocked &&
		stringValue(mapField(current, "guard")["reason"]) == stringValue(mapField(desired, "guard")["reason"])
}

func billingReconciliationContentMatches(current, desired map[string]any) bool {
	if !billingReconciliationIdentityMatches(current, desired) {
		return false
	}
	currentReport, currentOK := current["report"]
	desiredReport, desiredOK := desired["report"]
	if !currentOK || !desiredOK {
		return false
	}
	currentJSON, currentErr := json.Marshal(currentReport)
	desiredJSON, desiredErr := json.Marshal(desiredReport)
	return currentErr == nil && desiredErr == nil && string(currentJSON) == string(desiredJSON)
}

func billingReconciliationAuditID(resultID string) string {
	return "audit-" + stableID("billing.reconciliation", "billing_reconciliation", resultID)[:12]
}

func billingReconciliationAuditIdentityMatches(row, desired map[string]any) bool {
	resultID := stringValue(desired["id"])
	after, ok := row["after"].(map[string]any)
	return stringValue(row["id"]) == billingReconciliationAuditID(resultID) && stringValue(row["actorUserId"]) != "" &&
		stringValue(row["action"]) == "billing.reconciliation" && stringValue(row["resourceKind"]) == "billing_reconciliation" &&
		stringValue(row["resourceId"]) == resultID && stringValue(row["result"]) == "succeeded" && ok &&
		billingReconciliationContentMatches(after, desired)
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
		rows = append(rows, map[string]any{
			"id": firstNonEmpty(workspaceID, computeID, storageID, attachmentID), "accountId": ownerAccountID,
			"ownerAccountId": ownerAccountID, "ownerUserId": firstNonEmpty(stringValue(workspace["ownerUserId"]), stringValue(compute["ownerUserId"]), stringValue(storage["ownerUserId"])),
			"workspaceId": workspaceID, "workspaceIds": uniqueStrings([]string{workspaceID}),
			"computeAllocationId": computeID, "storageId": storageID, "attachmentId": attachmentID,
			"providerRequestId": firstNonEmpty(stringValue(compute["providerRequestId"]), stringValue(storage["providerRequestId"]), stringValue(attachment["providerRequestId"])),
			"operationId":       firstNonEmpty(stringValue(operation["operationId"]), stringValue(compute["operationId"]), stringValue(storage["operationId"]), stringValue(attachment["operationId"])),
			"receiptIds":        uniqueStrings([]string{stringValue(compute["lastReceiptId"]), stringValue(storage["lastReceiptId"]), stringValue(workspace["purchaseReceiptId"])}),
		})
	}
	return rows
}

func (app *controlPlaneServer) operationEvidenceForResourceLocked(ids ...string) map[string]any {
	operations := app.runtimeOperationRows(runtimeOperationQuery{})
	for index := len(operations) - 1; index >= 0; index-- {
		operation := operations[index]
		if mapContainsAnyID(operation, ids...) {
			return map[string]any{"operationId": operation["operationId"], "resourceId": operation["resourceId"]}
		}
	}
	return map[string]any{}
}
