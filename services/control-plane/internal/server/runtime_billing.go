package server

import (
	"time"

	"opl-cloud/services/control-plane/internal/clients"
)

func (app *controlPlaneApp) billingSummaryLocked(accountID string) map[string]any {
	return map[string]any{
		"activeHourlyEstimate":     app.activeHourlyEstimateLocked(accountID),
		"recentResourceDebitTotal": resourceDebitTotal(app.billing.ledger, accountID, ""),
	}
}

func (app *controlPlaneApp) activeHourlyEstimateLocked(accountID string) float64 {
	total := float64(0)
	for _, row := range app.resources.computes {
		if accountID != "" && firstNonEmpty(stringValue(row["accountId"]), stringValue(row["ownerAccountId"])) != accountID {
			continue
		}
		total += activeHourlyForResource(row)
	}
	for _, row := range app.resources.storages {
		if accountID != "" && firstNonEmpty(stringValue(row["accountId"]), stringValue(row["ownerAccountId"])) != accountID {
			continue
		}
		total += activeHourlyForResource(row)
	}
	return total
}

func (app *controlPlaneApp) reconciliationProjectionLocked() map[string]any {
	if app.reconcile == nil {
		return map[string]any{"reports": 0, "guard": map[string]any{"status": "not_required", "blockNewWorkspaces": false, "reason": "billing_reconciliation_not_required"}}
	}
	row := cloneMap(app.reconcile)
	row["reports"] = 1
	return row
}

func (app *controlPlaneApp) reconciliationBlocksNewWorkspaces() (map[string]any, bool) {
	app.mu.Lock()
	defer app.mu.Unlock()
	projection := app.reconciliationProjectionLocked()
	guard, _ := projection["guard"].(map[string]any)
	if guard == nil {
		return projection, false
	}
	blocked, _ := guard["blockNewWorkspaces"].(bool)
	return projection, blocked
}

func (app *controlPlaneApp) rememberReconciliation(result clients.ReconciliationResult) error {
	app.mu.Lock()
	defer app.mu.Unlock()
	app.reconcile = reconciliationResponse(result)
	return app.persistLocked()
}

func (app *controlPlaneApp) addLedgerLocked(accountID string, entryType string, ids map[string]any) map[string]any {
	entry := map[string]any{"id": "ledger-" + stableID(accountID, entryType, time.Now().UTC().String())[:12], "accountId": accountID, "type": entryType}
	for key, value := range ids {
		entry[key] = value
	}
	app.billing.ledger = append(app.billing.ledger, entry)
	return entry
}

func (app *controlPlaneApp) rememberManualTopUp(result clients.ManualTopUpResult) error {
	app.mu.Lock()
	defer app.mu.Unlock()
	app.billing.topups = append(app.billing.topups, structToMap(result.TopUp))
	app.billing.ledger = append(app.billing.ledger, map[string]any{"id": result.LedgerEntry.ID, "accountId": result.LedgerEntry.AccountID, "type": "manual_topup", "amountCents": result.LedgerEntry.AmountCents})
	app.billing.walletTx = append(app.billing.walletTx, map[string]any{"id": result.WalletTransaction.ID, "accountId": result.WalletTransaction.AccountID, "type": "manual_topup", "ledgerEntryId": result.WalletTransaction.LedgerEntryID, "amountCents": result.WalletTransaction.AmountCents})
	app.billing.wallets[result.Wallet.AccountID] = walletProjection(result.Wallet)
	return app.persistLocked()
}

func (app *controlPlaneApp) rememberResourceSettlement(result clients.ResourceSettlementResult) error {
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
	app.billing.ledger = upsertProjectionByID(app.billing.ledger, ledger)

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
	app.billing.walletTx = upsertProjectionByID(app.billing.walletTx, walletTx)
	app.billing.wallets[result.AccountID] = walletProjection(result.Wallet)
	return app.persistLocked()
}

func (app *controlPlaneApp) applyLedgerFacts(accountID string, wallet clients.Wallet, entries []clients.LedgerEntry, transactions []clients.WalletTransaction, topups []clients.ManualTopUp, settlements []clients.ResourceSettlementResult) error {
	app.mu.Lock()
	defer app.mu.Unlock()

	if accountID != "" && wallet.AccountID != "" && (walletHasMoneyFacts(wallet) || app.billing.wallets[wallet.AccountID] == nil) {
		app.billing.wallets[wallet.AccountID] = walletProjection(wallet)
	}
	for _, tx := range transactions {
		if tx.AccountID != "" {
			app.billing.wallets[tx.AccountID] = walletProjection(clients.Wallet{
				AccountID:       tx.AccountID,
				BalanceCents:    tx.BalanceCents,
				FrozenCents:     tx.FrozenCents,
				AvailableCents:  tx.AvailableCents,
				TotalSpentCents: tx.TotalSpentCents,
				Currency:        tx.Currency,
			})
		}
	}

	settlementsByEntry := map[string]clients.ResourceSettlementResult{}
	settlementsByWalletTx := map[string]clients.ResourceSettlementResult{}
	for _, settlement := range settlements {
		settlementsByEntry[settlement.LedgerEntryID] = settlement
		settlementsByWalletTx[settlement.WalletTransactionID] = settlement
	}
	if entries != nil {
		app.billing.ledger = ledgerEntryProjections(entries, settlementsByEntry)
	}
	if transactions != nil {
		app.billing.walletTx = walletTransactionProjections(transactions, settlementsByWalletTx)
	}
	if topups != nil {
		app.billing.topups = manualTopUpProjections(topups)
	}
	return app.persistLocked()
}

func (app *controlPlaneApp) resourceLedgerEvidenceLocked() []any {
	rows := []any{}
	for _, workspace := range app.resources.workspaces {
		workspaceID := stringValue(workspace["id"])
		computeID := stringValue(workspace["currentComputeAllocationId"])
		storageID := stringValue(workspace["storageId"])
		attachmentID := stringValue(workspace["currentAttachmentId"])
		compute := app.resources.computes[computeID]
		storage := app.resources.storages[storageID]
		attachment := app.resources.attachments[attachmentID]
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
			"ledgerEntryIds":       app.ledgerEntryIDsLocked(workspaceID, computeID, storageID, attachmentID),
			"walletTransactionIds": app.walletTransactionIDsLocked(workspaceID, computeID, storageID, attachmentID),
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

func (app *controlPlaneApp) operationEvidenceForResourceLocked(ids ...string) map[string]any {
	for index := len(app.runtimeOps) - 1; index >= 0; index-- {
		operation := app.runtimeOps[index]
		if mapContainsAnyID(operation, ids...) {
			payload, _ := operation["redactedProviderPayload"].(map[string]any)
			return map[string]any{"operationId": operation["operationId"], "resourceId": operation["resourceId"], "costTags": firstNonNil(operation["costTags"], payload["costTags"])}
		}
	}
	return map[string]any{}
}

func (app *controlPlaneApp) ledgerEntryIDsLocked(ids ...string) []string {
	output := []string{}
	for _, entry := range app.billing.ledger {
		if mapContainsAnyID(entry, ids...) {
			output = append(output, stringValue(entry["id"]))
		}
	}
	return uniqueStrings(output)
}

func (app *controlPlaneApp) walletTransactionIDsLocked(ids ...string) []string {
	output := []string{}
	for _, tx := range app.billing.walletTx {
		metadata, _ := tx["metadata"].(map[string]any)
		if mapContainsAnyID(metadata, ids...) {
			output = append(output, stringValue(tx["id"]))
		}
	}
	return uniqueStrings(output)
}

func (app *controlPlaneApp) addWalletTxLocked(accountID string, txType string, metadata map[string]any) {
	app.billing.walletTx = append(app.billing.walletTx, map[string]any{"id": "wallet-" + stableID(accountID, txType, time.Now().UTC().String())[:12], "accountId": accountID, "type": txType, "metadata": metadata})
}

func (app *controlPlaneApp) wallet(accountID string) map[string]any {
	if accountID == "" {
		accountID = "acct-local"
	}
	if wallet, ok := app.billing.wallets[accountID]; ok {
		return wallet
	}
	return map[string]any{"id": accountID, "accountId": accountID, "balance": float64(0), "frozen": float64(0), "available": float64(0), "totalRecharged": float64(0)}
}
