package server

import (
	"context"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
)

func (app *controlPlaneServer) accountBillingSummary(accountID string) map[string]any {
	return map[string]any{
		"activeHourlyEstimate":     app.activeHourlyEstimate(accountID),
		"recentResourceDebitTotal": resourceDebitTotal(rowsToRecords(app.listLedger(accountID)), accountID, ""),
	}
}

func (app *controlPlaneServer) activeHourlyEstimate(accountID string) float64 {
	total := float64(0)
	for _, row := range app.listComputes(accountID) {
		if accountID != "" && firstNonEmpty(stringValue(row["accountId"]), stringValue(row["ownerAccountId"])) != accountID {
			continue
		}
		total += activeHourlyForResource(row)
	}
	for _, row := range app.listStorages(accountID) {
		if accountID != "" && firstNonEmpty(stringValue(row["accountId"]), stringValue(row["ownerAccountId"])) != accountID {
			continue
		}
		total += activeHourlyForResource(row)
	}
	return total
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
	if guard == nil {
		return projection, false
	}
	blocked, _ := guard["blockNewWorkspaces"].(bool)
	return projection, blocked
}

func (app *controlPlaneServer) rememberReconciliation(result clients.ReconciliationResult) error {
	return app.tables.SaveBillingReconciliation(context.Background(), reconciliationResponse(result))
}

func (app *controlPlaneServer) addLedgerLocked(accountID string, entryType string, ids map[string]any) map[string]any {
	entry := map[string]any{"id": "ledger-" + stableID(accountID, entryType, time.Now().UTC().String())[:12], "accountId": accountID, "type": entryType}
	for key, value := range ids {
		entry[key] = value
	}
	_ = app.tables.SaveLedgerEntry(context.Background(), entry)
	return entry
}

func (app *controlPlaneServer) saveManualTopUpProjection(result clients.ManualTopUpResult) error {
	if err := app.tables.SaveManualTopup(context.Background(), structToMap(result.TopUp)); err != nil {
		return err
	}
	if err := app.tables.SaveLedgerEntry(context.Background(), map[string]any{"id": result.LedgerEntry.ID, "accountId": result.LedgerEntry.AccountID, "type": "manual_topup", "amountCents": result.LedgerEntry.AmountCents}); err != nil {
		return err
	}
	if err := app.tables.SaveWalletTransaction(context.Background(), map[string]any{"id": result.WalletTransaction.ID, "accountId": result.WalletTransaction.AccountID, "type": "manual_topup", "ledgerEntryId": result.WalletTransaction.LedgerEntryID, "amountCents": result.WalletTransaction.AmountCents}); err != nil {
		return err
	}
	return app.tables.SaveWallet(context.Background(), walletProjection(result.Wallet))
}

func (app *controlPlaneServer) saveResourceSettlementProjection(result clients.ResourceSettlementResult) error {
	app.mu.Lock()
	defer app.mu.Unlock()

	resourceType := firstNonEmpty(result.ResourceType, "compute")
	debitType := resourceType + "_debit"
	ids := map[string]any{"workspaceId": result.WorkspaceID, "resourceId": result.ResourceID}
	switch resourceType {
	case "storage":
		ids["storageId"] = result.ResourceID
	default:
		ids["computeAllocationId"] = result.ResourceID
	}

	ledger := map[string]any{"id": result.LedgerEntryID, "accountId": result.AccountID, "type": debitType, "amountCents": -result.AmountCents}
	for key, value := range ids {
		ledger[key] = value
	}
	ledger["settlementId"] = result.ID
	ledger["pricingVersion"] = result.PricingVersion
	ledger["priceSnapshot"] = cloneMap(result.PriceSnapshot)
	ledger["usagePeriodStart"] = result.UsagePeriodStart
	ledger["usagePeriodEnd"] = result.UsagePeriodEnd
	ledger["quantity"] = result.Quantity
	ledger["unit"] = result.Unit
	ledger["providerCostEvidenceRef"] = result.ProviderCostEvidenceRef
	if err := app.tables.SaveLedgerEntry(context.Background(), ledger); err != nil {
		return err
	}

	walletTx := map[string]any{
		"id":              result.WalletTransactionID,
		"accountId":       result.AccountID,
		"ledgerEntryId":   result.LedgerEntryID,
		"type":            debitType,
		"metadata":        settlementMetadata(result),
		"amountCents":     -result.AmountCents,
		"balanceCents":    result.Wallet.BalanceCents,
		"frozenCents":     result.Wallet.FrozenCents,
		"availableCents":  result.Wallet.AvailableCents,
		"totalSpentCents": result.Wallet.TotalSpentCents,
		"currency":        result.Wallet.Currency,
	}
	if err := app.tables.SaveWalletTransaction(context.Background(), walletTx); err != nil {
		return err
	}
	return app.tables.SaveWallet(context.Background(), walletProjection(result.Wallet))
}

func (app *controlPlaneServer) applyLedgerFacts(accountID string, wallet clients.Wallet, entries []clients.LedgerEntry, transactions []clients.WalletTransaction, topups []clients.ManualTopUp, settlements []clients.ResourceSettlementResult) error {
	app.mu.Lock()
	defer app.mu.Unlock()

	if accountID != "" && wallet.AccountID != "" && walletHasMoneyFacts(wallet) {
		if err := app.tables.SaveWallet(context.Background(), walletProjection(wallet)); err != nil {
			return err
		}
	}
	for _, tx := range transactions {
		if tx.AccountID != "" {
			if err := app.tables.SaveWallet(context.Background(), walletProjection(clients.Wallet{
				AccountID:       tx.AccountID,
				BalanceCents:    tx.BalanceCents,
				FrozenCents:     tx.FrozenCents,
				AvailableCents:  tx.AvailableCents,
				TotalSpentCents: tx.TotalSpentCents,
				Currency:        tx.Currency,
			})); err != nil {
				return err
			}
		}
	}

	settlementsByEntry := map[string]clients.ResourceSettlementResult{}
	settlementsByWalletTx := map[string]clients.ResourceSettlementResult{}
	for _, settlement := range settlements {
		settlementsByEntry[settlement.LedgerEntryID] = settlement
		settlementsByWalletTx[settlement.WalletTransactionID] = settlement
	}
	if entries != nil {
		for _, row := range ledgerEntryProjections(entries, settlementsByEntry) {
			if err := app.tables.SaveLedgerEntry(context.Background(), row); err != nil {
				return err
			}
		}
	}
	if transactions != nil {
		for _, row := range walletTransactionProjections(transactions, settlementsByWalletTx) {
			if err := app.tables.SaveWalletTransaction(context.Background(), row); err != nil {
				return err
			}
		}
	}
	if topups != nil {
		for _, row := range manualTopUpProjections(topups) {
			if err := app.tables.SaveManualTopup(context.Background(), row); err != nil {
				return err
			}
		}
	}
	return nil
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
		ownerUserID := firstNonEmpty(stringValue(workspace["ownerUserId"]), stringValue(compute["ownerUserId"]), stringValue(storage["ownerUserId"]), stringValue(attachment["ownerUserId"]))
		costTags := firstNonNil(operation["costTags"], compute["costTags"], storage["costTags"], attachment["costTags"])
		if !hasProviderCostTags(costTags) {
			costTags = providerCostTags(ownerAccountID, workspaceID, firstNonEmpty(stringValue(operation["resourceId"]), workspaceID, computeID, storageID, attachmentID), stringValue(operation["operationId"]))
		}
		rows = append(rows, map[string]any{
			"id":                   firstNonEmpty(workspaceID, computeID, storageID, attachmentID),
			"accountId":            ownerAccountID,
			"ownerAccountId":       ownerAccountID,
			"ownerUserId":          ownerUserID,
			"workspaceId":          workspaceID,
			"workspaceIds":         uniqueStrings([]string{workspaceID}),
			"computeAllocationId":  computeID,
			"storageId":            storageID,
			"attachmentId":         attachmentID,
			"cvmInstanceId":        firstNonEmpty(stringValue(compute["cvmInstanceId"]), stringValue(compute["providerResourceId"])),
			"nodeName":             firstNonEmpty(stringValue(compute["nodeName"]), stringValue(compute["machineName"])),
			"providerRequestId":    firstNonEmpty(stringValue(compute["providerRequestId"]), stringValue(storage["providerRequestId"]), stringValue(attachment["providerRequestId"])),
			"operationId":          firstNonEmpty(stringValue(operation["operationId"]), stringValue(compute["operationId"]), stringValue(storage["operationId"]), stringValue(attachment["operationId"])),
			"costTags":             costTags,
			"ledgerEntryIds":       app.entryIDsForLedger(workspaceID, computeID, storageID, attachmentID),
			"walletTransactionIds": app.transactionIDsForWallet(workspaceID, computeID, storageID, attachmentID),
		})
	}
	return rows
}

func hasProviderCostTags(tags any) bool {
	return costTagValue(tags, "opl_account_id") != "" &&
		costTagValue(tags, "opl_workspace_id") != "" &&
		costTagValue(tags, "opl_resource_id") != "" &&
		costTagValue(tags, "opl_operation_id") != ""
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

func providerCostTags(accountID string, workspaceID string, resourceID string, operationID string) map[string]any {
	return map[string]any{
		"opl_account_id":   accountID,
		"opl_workspace_id": workspaceID,
		"opl_resource_id":  resourceID,
		"opl_operation_id": operationID,
	}
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

func (app *controlPlaneServer) entryIDsForLedger(ids ...string) []string {
	output := []string{}
	for _, entry := range app.listLedger("") {
		if mapContainsAnyID(entry, ids...) {
			output = append(output, stringValue(entry["id"]))
		}
	}
	return uniqueStrings(output)
}

func (app *controlPlaneServer) transactionIDsForWallet(ids ...string) []string {
	output := []string{}
	for _, tx := range app.listWalletTransactions("") {
		metadata, _ := tx["metadata"].(map[string]any)
		if mapContainsAnyID(metadata, ids...) {
			output = append(output, stringValue(tx["id"]))
		}
	}
	return uniqueStrings(output)
}

func (app *controlPlaneServer) addWalletTxLocked(accountID string, txType string, metadata map[string]any) {
	_ = app.tables.SaveWalletTransaction(context.Background(), map[string]any{"id": "wallet-" + stableID(accountID, txType, time.Now().UTC().String())[:12], "accountId": accountID, "type": txType, "metadata": metadata})
}

func (app *controlPlaneServer) wallet(accountID string) map[string]any {
	if accountID == "" {
		accountID = "acct-local"
	}
	wallets, err := app.tables.ListWallets(context.Background(), accountID)
	if err == nil && len(wallets) > 0 {
		return cloneMap(wallets[0])
	}
	return map[string]any{"id": accountID, "accountId": accountID, "balance": float64(0), "frozen": float64(0), "available": float64(0), "totalRecharged": float64(0)}
}
