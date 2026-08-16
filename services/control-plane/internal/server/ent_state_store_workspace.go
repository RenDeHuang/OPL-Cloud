package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	controlplaneent "opl-cloud/services/control-plane/ent"
	"opl-cloud/services/control-plane/ent/account"
	"opl-cloud/services/control-plane/ent/adminauditevent"
	"opl-cloud/services/control-plane/ent/runtimeoperation"
	"opl-cloud/services/control-plane/ent/user"
	"opl-cloud/services/control-plane/ent/workspace"
)

var (
	workspaceEntFields = []entRecordField{
		textField("AccountID", "SetAccountID", "accountId"),
		textField("OwnerAccountID", "SetOwnerAccountID", "ownerAccountId"),
		textField("OwnerUserID", "SetOwnerUserID", "ownerUserId"),
		textField("UserID", "SetUserID", "userId"),
		textField("Name", "SetName", "name"),
		textField("URL", "SetURL", "url"),
		textField("State", "SetState", "state"),
		textField("Status", "SetStatus", "status"),
		textField("PurchaseReceiptID", "SetPurchaseReceiptID", "purchaseReceiptId"),
		textField("StorageID", "SetStorageID", "storageId"),
		textField("CurrentComputeAllocationID", "SetCurrentComputeAllocationID", "currentComputeAllocationId"),
		textField("CurrentAttachmentID", "SetCurrentAttachmentID", "currentAttachmentId"),
		textField("RuntimeID", "SetRuntimeID", "runtimeId"),
		textField("RuntimeServiceName", "SetRuntimeServiceName", "runtime", "serviceName"),
		textField("RuntimeServiceNameRoot", "SetRuntimeServiceNameRoot", "runtimeServiceName"),
		textField("ServiceName", "SetServiceName", "serviceName"),
		intField("WorkspaceAPIKeyID", "SetWorkspaceAPIKeyID", "workspaceApiKeyId"),
		textField("AccessAccount", "SetAccessAccount", "access", "account"),
		textField("AccessUsername", "SetAccessUsername", "access", "username"),
		textField("CredentialStatus", "SetCredentialStatus", "access", "credentialStatus"),
		textField("CredentialVersion", "SetCredentialVersion", "access", "credentialVersion"),
		textField("CredentialSecretRef", "SetCredentialSecretRef", "access", "secretRef"),
		textField("VerificationSlotID", "SetVerificationSlotID", "verificationSlotId"),
		boolField("CustomerProduct", "SetCustomerProduct", "customerProduct"),
		entRecordField{EntityField: "BillingStateJSON", Setter: "SetBillingStateJSON", Kind: "workspace_billing_json"},
	}
)

type workspaceBillingState struct {
	AutoRenew           bool   `json:"autoRenew"`
	AuthorizedBy        string `json:"authorizedBy"`
	AuthorizedAt        string `json:"authorizedAt"`
	PackageID           string `json:"packageId"`
	StorageGB           int64  `json:"storageGb"`
	PriceVersion        string `json:"priceVersion"`
	Currency            string `json:"currency"`
	BillingUnit         string `json:"billingUnit"`
	ComputeUSDMicros    int64  `json:"computeUsdMicros"`
	StorageUSDMicros    int64  `json:"storageUsdMicros"`
	TotalUSDMicros      int64  `json:"totalUsdMicros"`
	PeriodStart         string `json:"periodStart"`
	PaidThrough         string `json:"paidThrough"`
	NextRenewalAt       string `json:"nextRenewalAt"`
	BillingAnchorDay    int64  `json:"billingAnchorDay"`
	RenewalStatus       string `json:"renewalStatus"`
	ComputeAllocationID string `json:"computeAllocationId"`
	StorageID           string `json:"storageId"`
	ManualReviewReason  string `json:"-"`
}

var workspaceBillingStateRequiredKeys = []string{
	"autoRenew", "authorizedBy", "authorizedAt", "packageId", "storageGb", "priceVersion", "currency", "billingUnit",
	"computeUsdMicros", "storageUsdMicros", "totalUsdMicros", "periodStart", "paidThrough",
	"nextRenewalAt", "billingAnchorDay", "renewalStatus", "computeAllocationId", "storageId",
}

var workspaceBillingStateExclusiveKeys = []string{
	"authorizedBy", "authorizedAt", "storageGb", "priceVersion", "currency", "billingUnit",
	"computeUsdMicros", "storageUsdMicros", "totalUsdMicros", "periodStart", "paidThrough", "nextRenewalAt", "billingAnchorDay",
}

const workspaceBillingLegacyMismatch = "legacy_billing_state_mismatch"

func validateWorkspaceBillingState(row map[string]any) error {
	_, _, err := normalizeWorkspaceBillingStateForWorkspace(row, row)
	return err
}

func mergeWorkspaceForSave(existing, incoming map[string]any) (map[string]any, error) {
	row := cloneMap(incoming)
	if existing == nil {
		return row, nil
	}
	for _, key := range []string{"accountId", "ownerAccountId", "ownerUserId"} {
		if current := stringValue(existing[key]); current != "" && stringValue(row[key]) != current {
			return nil, errIdempotencyConflict
		}
	}
	existingBilling := workspaceAcceptedBillingState(existing)
	incomingBilling := workspaceAcceptedBillingState(row)
	if existingBilling != nil && incomingBilling != nil {
		for _, key := range []string{"computeAllocationId", "storageId", "packageId", "priceVersion"} {
			if existingBilling[key] != incomingBilling[key] {
				return nil, errIdempotencyConflict
			}
		}
	}
	existingAutoRenew, existingHasAutoRenew := existing["autoRenew"].(bool)
	incomingAutoRenew, incomingHasAutoRenew := row["autoRenew"].(bool)
	if existingHasAutoRenew && !existingAutoRenew && incomingHasAutoRenew && incomingAutoRenew {
		if existingBilling == nil {
			return nil, errIdempotencyConflict
		}
		for key, value := range existingBilling {
			row[key] = value
		}
	}
	if !workspaceLifecycleInactive(existing) || workspaceLifecycleInactive(row) {
		return row, nil
	}
	for _, key := range []string{"state", "status", "currentComputeAllocationId", "currentAttachmentId"} {
		row[key] = existing[key]
	}
	if existingBilling != nil {
		for key, value := range existingBilling {
			row[key] = value
		}
	}
	return row, nil
}

func workspaceLifecycleInactive(row map[string]any) bool {
	switch firstNonEmpty(stringValue(row["state"]), stringValue(row["status"])) {
	case "suspended", "stopped", "data_deleted", "unrecoverable", "storage_missing", "destroyed":
		return true
	default:
		return false
	}
}

func normalizeWorkspaceBillingStateForWorkspace(row, workspace map[string]any) (workspaceBillingState, bool, error) {
	currentComputeID := stringValue(workspace["currentComputeAllocationId"])
	state, present, err := normalizeWorkspaceBillingState(row, currentComputeID, stringValue(workspace["storageId"]), stringValue(workspace["ownerUserId"]))
	if err != nil || !present || state.RenewalStatus == "manual_review" || currentComputeID != "" {
		return state, present, err
	}
	if state.AutoRenew {
		return workspaceBillingState{}, false, errInvalidWorkspaceBillingState
	}
	switch firstNonEmpty(stringValue(workspace["state"]), stringValue(workspace["status"])) {
	case "suspended", "data_deleted", "unrecoverable":
		return state, present, nil
	default:
		return workspaceBillingState{}, false, errInvalidWorkspaceBillingState
	}
}

func normalizeWorkspaceBillingState(row map[string]any, expectedComputeID, expectedStorageID, expectedOwnerID string) (workspaceBillingState, bool, error) {
	_, hasRenewalStatus := row["renewalStatus"]
	_, hasAutoRenew := row["autoRenew"]
	_, hasPriceVersion := row["priceVersion"]
	if !hasRenewalStatus && !hasAutoRenew && !hasPriceVersion {
		return workspaceBillingState{}, false, nil
	}
	if row["renewalStatus"] == "manual_review" {
		for _, key := range workspaceBillingStateExclusiveKeys {
			if _, ok := row[key]; ok {
				return workspaceBillingState{}, false, errInvalidWorkspaceBillingState
			}
		}
		autoRenew, validAutoRenew := row["autoRenew"].(bool)
		reason, validReason := row["manualReviewReason"].(string)
		if !validAutoRenew || autoRenew || !validReason || reason != workspaceBillingLegacyMismatch {
			return workspaceBillingState{}, false, errInvalidWorkspaceBillingState
		}
		return workspaceBillingState{RenewalStatus: "manual_review", ManualReviewReason: reason}, true, nil
	}
	for _, key := range workspaceBillingStateRequiredKeys {
		if _, ok := row[key]; !ok {
			return workspaceBillingState{}, false, fmt.Errorf("%w: missing %s", errInvalidWorkspaceBillingState, key)
		}
	}
	autoRenew, validAutoRenew := row["autoRenew"].(bool)
	authorizedBy, validAuthorizedBy := row["authorizedBy"].(string)
	authorizedAt, validAuthorizedAt := row["authorizedAt"].(string)
	packageID, validPackageID := row["packageId"].(string)
	priceVersion, validPriceVersion := row["priceVersion"].(string)
	currency, validCurrency := row["currency"].(string)
	billingUnit, validBillingUnit := row["billingUnit"].(string)
	periodStartText, validPeriodStart := row["periodStart"].(string)
	paidThroughText, validPaidThrough := row["paidThrough"].(string)
	nextRenewalText, validNextRenewal := row["nextRenewalAt"].(string)
	renewalStatus, validRenewalStatus := row["renewalStatus"].(string)
	computeID, validComputeID := row["computeAllocationId"].(string)
	storageID, validStorageID := row["storageId"].(string)
	if !validAutoRenew || !validAuthorizedBy || !validAuthorizedAt || !validPackageID || !validPriceVersion || !validCurrency || !validBillingUnit ||
		!validPeriodStart || !validPaidThrough || !validNextRenewal || !validRenewalStatus || !validComputeID || !validStorageID {
		return workspaceBillingState{}, false, errInvalidWorkspaceBillingState
	}
	storageGB, validStorageGB := requiredPositiveInteger(row, "storageGb")
	computeUSDMicros, validComputePrice := requiredPositiveInteger(row, "computeUsdMicros")
	storageUSDMicros, validStoragePrice := requiredPositiveInteger(row, "storageUsdMicros")
	totalUSDMicros, validTotal := requiredPositiveInteger(row, "totalUsdMicros")
	billingAnchorDay, validAnchor := requiredPositiveInteger(row, "billingAnchorDay")
	if !validStorageGB || !validComputePrice || !validStoragePrice || !validTotal || !validAnchor || billingAnchorDay > 31 {
		return workspaceBillingState{}, false, errInvalidWorkspaceBillingState
	}
	quote, err := workspacePricingPreview(defaultPricingCatalog(), map[string]any{"packageId": packageID, "sizeGb": storageGB})
	if err != nil {
		return workspaceBillingState{}, false, errInvalidWorkspaceBillingState
	}
	expectedCompute, computeOK := requiredPositiveInteger(mapField(quote, "compute"), "chargeUsdMicros")
	expectedStorage, storageOK := requiredPositiveInteger(mapField(quote, "storage"), "chargeUsdMicros")
	expectedTotal, totalOK := requiredPositiveInteger(quote, "totalChargeUsdMicros")
	checkedTotal, sumOK := checkedAddInt64(computeUSDMicros, storageUSDMicros)
	if !computeOK || !storageOK || !totalOK || !sumOK || computeUSDMicros != expectedCompute || storageUSDMicros != expectedStorage || totalUSDMicros != expectedTotal || totalUSDMicros != checkedTotal ||
		priceVersion != pricingCatalogVersion || currency != pricingCurrency || billingUnit != pricingBillingUnit {
		return workspaceBillingState{}, false, errInvalidWorkspaceBillingState
	}
	periodStart, startErr := time.Parse(time.RFC3339, periodStartText)
	paidThrough, paidErr := time.Parse(time.RFC3339, paidThroughText)
	nextRenewal, nextErr := time.Parse(time.RFC3339, nextRenewalText)
	if startErr != nil || paidErr != nil || nextErr != nil || !paidThrough.After(periodStart) || !nextRenewal.Equal(paidThrough.Add(-24*time.Hour)) {
		return workspaceBillingState{}, false, errInvalidWorkspaceBillingState
	}
	if (renewalStatus != "active" && renewalStatus != "expired_unpaid") || strings.TrimSpace(computeID) == "" || strings.TrimSpace(storageID) == "" ||
		expectedComputeID != "" && computeID != expectedComputeID || expectedStorageID == "" || storageID != expectedStorageID {
		return workspaceBillingState{}, false, errInvalidWorkspaceBillingState
	}
	if renewalStatus == "expired_unpaid" && autoRenew {
		return workspaceBillingState{}, false, errInvalidWorkspaceBillingState
	}
	if autoRenew && (authorizedBy == "" || authorizedAt == "") || authorizedBy != "" && authorizedBy != expectedOwnerID || (authorizedBy == "") != (authorizedAt == "") {
		return workspaceBillingState{}, false, errInvalidWorkspaceBillingState
	}
	if authorizedAt != "" {
		parsed, err := time.Parse(time.RFC3339, authorizedAt)
		if err != nil {
			return workspaceBillingState{}, false, errInvalidWorkspaceBillingState
		}
		authorizedAt = parsed.UTC().Format(time.RFC3339Nano)
	}
	if _, ok := row["manualReviewReason"]; ok {
		return workspaceBillingState{}, false, errInvalidWorkspaceBillingState
	}
	return workspaceBillingState{
		AutoRenew: autoRenew, AuthorizedBy: authorizedBy, AuthorizedAt: authorizedAt,
		PackageID: packageID, StorageGB: storageGB, PriceVersion: priceVersion, Currency: currency, BillingUnit: billingUnit,
		ComputeUSDMicros: computeUSDMicros, StorageUSDMicros: storageUSDMicros, TotalUSDMicros: totalUSDMicros,
		PeriodStart: periodStart.UTC().Format(time.RFC3339Nano), PaidThrough: paidThrough.UTC().Format(time.RFC3339Nano), NextRenewalAt: nextRenewal.UTC().Format(time.RFC3339Nano),
		BillingAnchorDay: billingAnchorDay, RenewalStatus: renewalStatus, ComputeAllocationID: computeID, StorageID: storageID,
	}, true, nil
}

func (state workspaceBillingState) record() map[string]any {
	if state.RenewalStatus == "manual_review" {
		return map[string]any{"autoRenew": false, "renewalStatus": state.RenewalStatus, "manualReviewReason": state.ManualReviewReason}
	}
	row := map[string]any{
		"autoRenew": state.AutoRenew, "authorizedBy": state.AuthorizedBy, "authorizedAt": state.AuthorizedAt,
		"packageId": state.PackageID, "storageGb": state.StorageGB,
		"priceVersion": state.PriceVersion, "currency": state.Currency, "billingUnit": state.BillingUnit,
		"computeUsdMicros": state.ComputeUSDMicros, "storageUsdMicros": state.StorageUSDMicros, "totalUsdMicros": state.TotalUSDMicros,
		"periodStart": state.PeriodStart, "paidThrough": state.PaidThrough, "nextRenewalAt": state.NextRenewalAt,
		"billingAnchorDay": state.BillingAnchorDay, "renewalStatus": state.RenewalStatus,
		"computeAllocationId": state.ComputeAllocationID, "storageId": state.StorageID,
	}
	return row
}

func encodeWorkspaceBillingState(row map[string]any) (string, error) {
	state, present, err := normalizeWorkspaceBillingStateForWorkspace(row, row)
	if err != nil {
		return "", err
	}
	if !present {
		return "{}", nil
	}
	if state.RenewalStatus == "manual_review" {
		encoded, err := json.Marshal(state.record())
		return string(encoded), err
	}
	encoded, err := json.Marshal(state)
	return string(encoded), err
}

func decodeWorkspaceBillingState(encoded string, workspace map[string]any) (map[string]any, error) {
	if strings.TrimSpace(encoded) == "" {
		return nil, errInvalidWorkspaceBillingState
	}
	if strings.TrimSpace(encoded) == "{}" {
		return nil, nil
	}
	var shape map[string]json.RawMessage
	shapeDecoder := json.NewDecoder(strings.NewReader(encoded))
	if err := shapeDecoder.Decode(&shape); err != nil {
		return nil, err
	}
	if err := ensureJSONEOF(shapeDecoder); err != nil {
		return nil, err
	}
	if len(shape) == 3 && shape["autoRenew"] != nil && shape["renewalStatus"] != nil && shape["manualReviewReason"] != nil {
		var marker map[string]any
		if err := json.Unmarshal([]byte(encoded), &marker); err != nil {
			return nil, err
		}
		normalized, _, err := normalizeWorkspaceBillingState(marker, "", "", "")
		if err != nil {
			return nil, err
		}
		return normalized.record(), nil
	}
	allowed := map[string]bool{}
	for _, key := range workspaceBillingStateRequiredKeys {
		allowed[key] = true
		if _, ok := shape[key]; !ok {
			return nil, fmt.Errorf("%w: missing %s", errInvalidWorkspaceBillingState, key)
		}
	}
	for key := range shape {
		if !allowed[key] {
			return nil, fmt.Errorf("%w: unknown %s", errInvalidWorkspaceBillingState, key)
		}
	}
	var state workspaceBillingState
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return nil, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	normalized, _, err := normalizeWorkspaceBillingStateForWorkspace(state.record(), workspace)
	if err != nil {
		return nil, err
	}
	return normalized.record(), nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errInvalidWorkspaceBillingState
	}
	return nil
}

func (s *postgresEntStateStore) ListWorkspaces(ctx context.Context, accountID string) ([]map[string]any, error) {
	query := s.client.Workspace.Query()
	if accountID != "" {
		query.Where(workspace.Or(workspace.AccountID(accountID), workspace.And(workspace.AccountID(""), workspace.OwnerAccountID(accountID))))
	}
	rows, err := loadRecordSet(ctx, query.All, workspaceEntFields)
	if err != nil {
		return nil, err
	}
	return filteredRecords(rows, accountID)
}

func (s *postgresEntStateStore) GetWorkspace(ctx context.Context, id string) (map[string]any, bool, error) {
	entity, err := s.client.Workspace.Get(ctx, id)
	if controlplaneent.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return recordFromEnt(entity, workspaceEntFields), true, nil
}

func (s *postgresEntStateStore) PageWorkspaces(ctx context.Context, accountID string, page tablePageQuery) (tablePage, error) {
	query := s.client.Workspace.Query()
	if accountID != "" {
		query.Where(workspace.Or(workspace.AccountID(accountID), workspace.And(workspace.AccountID(""), workspace.OwnerAccountID(accountID))))
	}
	total, err := query.Clone().Count(ctx)
	if err != nil {
		return tablePage{}, err
	}
	rows, err := loadRecordSet(ctx, query.Order(workspace.ByID()).Offset(page.Offset).Limit(page.Limit).All, workspaceEntFields)
	if err != nil {
		return tablePage{}, err
	}
	items, err := filteredRecords(rows, accountID)
	sort.Slice(items, func(i, j int) bool { return stringValue(items[i]["id"]) < stringValue(items[j]["id"]) })
	return tablePage{Items: items, Total: total}, err
}

func (s *postgresEntStateStore) CountWorkspaces(ctx context.Context) (int, error) {
	return s.client.Workspace.Query().Count(ctx)
}

func (s *postgresEntStateStore) CountWorkspacesByAccount(ctx context.Context, accountIDs []string) (map[string]int, error) {
	counts := make(map[string]int, len(accountIDs))
	for _, accountID := range accountIDs {
		counts[accountID] = 0
	}
	if len(counts) == 0 {
		return counts, nil
	}
	ids := make([]string, 0, len(counts))
	for accountID := range counts {
		ids = append(ids, accountID)
	}
	var groups []struct {
		AccountID      string `json:"account_id"`
		OwnerAccountID string `json:"owner_account_id"`
		Count          int    `json:"count"`
	}
	err := s.client.Workspace.Query().
		Where(workspace.Or(workspace.AccountIDIn(ids...), workspace.And(workspace.AccountID(""), workspace.OwnerAccountIDIn(ids...)))).
		GroupBy(workspace.FieldAccountID, workspace.FieldOwnerAccountID).
		Aggregate(controlplaneent.As(controlplaneent.Count(), "count")).
		Scan(ctx, &groups)
	if err != nil {
		return nil, err
	}
	for _, group := range groups {
		accountID := firstNonEmpty(group.AccountID, group.OwnerAccountID)
		if _, ok := counts[accountID]; ok {
			counts[accountID] += group.Count
		}
	}
	return counts, nil
}

func (s *postgresEntStateStore) SaveWorkspace(ctx context.Context, row map[string]any) error {
	if err := validateWorkspaceBillingState(row); err != nil {
		return err
	}
	row = cloneMap(row)
	if _, ok := row["customerProduct"]; !ok {
		row["customerProduct"] = true
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := saveWorkspaceRecord(ctx, tx.Client(), row); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *postgresEntStateStore) CompareAndSwapWorkspaceAPIKey(ctx context.Context, workspaceID string, expectedID, newID int64) error {
	if workspaceID == "" || expectedID <= 0 || newID <= 0 {
		return errWorkspaceAPIKeyCASConflict
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	entity, err := tx.Workspace.Query().Where(workspace.IDEQ(workspaceID), lockRowForUpdate).Only(ctx)
	if err != nil {
		if controlplaneent.IsNotFound(err) {
			return errWorkspaceAPIKeyCASConflict
		}
		return err
	}
	if entity.WorkspaceAPIKeyID == newID {
		return tx.Commit()
	}
	if entity.WorkspaceAPIKeyID != expectedID {
		return errWorkspaceAPIKeyCASConflict
	}
	if err := tx.Workspace.UpdateOneID(workspaceID).SetWorkspaceAPIKeyID(newID).Exec(ctx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *postgresEntStateStore) ApplyWorkspaceRenewalIntent(ctx context.Context, update workspaceRenewalIntentCAS) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	entity, err := client.Workspace.Query().Where(workspace.IDEQ(update.WorkspaceID), lockRowForUpdate).Only(ctx)
	if err != nil {
		if controlplaneent.IsNotFound(err) {
			return errWorkspaceRenewalCASConflict
		}
		return err
	}
	current := recordFromEnt(entity, workspaceEntFields)
	currentAutoRenew, validAutoRenew := current["autoRenew"].(bool)
	if stringValue(current["accountId"]) != update.AccountID || stringValue(current["ownerUserId"]) != update.OwnerUserID ||
		stringValue(current["paidThrough"]) != update.ExpectedPaidThrough || !validAutoRenew || currentAutoRenew != update.ExpectedAutoRenew {
		return errWorkspaceRenewalCASConflict
	}
	operationEntities, err := client.RuntimeOperation.Query().Where(runtimeoperation.WorkspaceIDEQ(update.WorkspaceID), lockRowForUpdate).All(ctx)
	if err != nil {
		return err
	}
	operations := make([]map[string]any, 0, len(operationEntities))
	for _, operation := range operationEntities {
		row := recordFromEnt(operation, runtimeOpEntFields)
		if stringValue(row["id"]) == stringValue(update.CommandOperation["id"]) {
			return errWorkspaceRenewalCASConflict
		}
		operations = append(operations, row)
	}
	if runtimeOperationsVersion(operations, update.WorkspaceID) != update.ExpectedOperationsVersion {
		return errWorkspaceRenewalCASConflict
	}
	desired := cloneMap(current)
	desired["autoRenew"], desired["authorizedBy"], desired["authorizedAt"] = update.WorkspacePatch.AutoRenew, update.WorkspacePatch.AuthorizedBy, update.WorkspacePatch.AuthorizedAt
	if err := validateWorkspaceBillingState(desired); err != nil {
		return err
	}
	if err := validateWorkspaceRenewalIntentAudit(update, current); err != nil {
		return err
	}
	auditID := stringValue(update.AuditEvent["id"])
	auditExists := false
	auditEntity, err := client.AdminAuditEvent.Query().Where(adminauditevent.IDEQ(auditID), lockRowForUpdate).Only(ctx)
	if err == nil {
		auditExists = true
		if !workspaceRenewalIntentAuditIdentityMatches(recordFromEnt(auditEntity, auditEntFields), update.AuditEvent) {
			return errIdempotencyConflict
		}
	} else if !controlplaneent.IsNotFound(err) {
		return err
	}
	builder := client.Workspace.UpdateOneID(update.WorkspaceID)
	setRecordFieldsWithEmptyText(builder, desired, workspaceEntFields, true)
	if err := execCreate(ctx, builder); err != nil {
		return err
	}
	command := controlPlaneRecord(update.CommandOperation)
	if err := saveRecord(ctx, stringValue(command["id"]), command, client.RuntimeOperation.Create(), runtimeOpEntFields); err != nil {
		if controlplaneent.IsConstraintError(err) {
			return errWorkspaceRenewalCASConflict
		}
		return err
	}
	if !auditExists {
		audit := controlPlaneRecord(update.AuditEvent)
		if err := saveRecord(ctx, auditID, audit, client.AdminAuditEvent.Create(), auditEntFields); err != nil {
			if controlplaneent.IsConstraintError(err) {
				return errIdempotencyConflict
			}
			return err
		}
	}
	return tx.Commit()
}

func (s *postgresEntStateStore) ClaimWorkspaceLaunchReconcile(ctx context.Context, claim workspaceLaunchReconcileClaim) error {
	desired, err := decodeWorkspaceLaunchReconcileOperation(claim.DesiredOperation)
	if err != nil || desired.stringFact("accountId") != claim.AccountID || desired.boolFact("acceptanceBCapacitySlot") != claim.AcceptanceBCapacitySlot {
		return errWorkspaceLaunchCASConflict
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	if _, err := client.Account.Query().Where(lockRowForUpdate).Order(controlplaneent.Asc(account.FieldID)).First(ctx); err != nil {
		if controlplaneent.IsNotFound(err) {
			return errWorkspaceLaunchCASConflict
		}
		return err
	}
	if _, err := client.Account.Query().Where(account.IDEQ(claim.AccountID), lockRowForUpdate).Only(ctx); err != nil {
		if controlplaneent.IsNotFound(err) {
			return errWorkspaceLaunchCASConflict
		}
		return err
	}
	accountOperations, err := client.RuntimeOperation.Query().Where(runtimeoperation.AccountIDEQ(claim.AccountID), lockRowForUpdate).All(ctx)
	if err != nil {
		return err
	}
	for _, entity := range accountOperations {
		row := recordFromEnt(entity, runtimeOpEntFields)
		if stringValue(row["id"]) == desired.ID {
			return errWorkspaceLaunchCASConflict
		}
		if isWorkspaceLaunchAction(stringValue(row["action"])) && !terminalWorkspaceLaunchStatus(stringValue(row["status"])) {
			return errWorkspaceLaunchInProgress
		}
	}
	launchEntities, err := client.RuntimeOperation.Query().Where(
		runtimeoperation.ActionIn(workspaceLaunchAction, "workspace.launch"),
	).All(ctx)
	if err != nil {
		return err
	}
	inFlight, acceptanceClaims := 0, 0
	for _, entity := range launchEntities {
		row := recordFromEnt(entity, runtimeOpEntFields)
		if workspaceLaunchReconcileAcceptanceSlot(row) || workspaceLaunchHasAcceptanceBCapacitySlot(row) {
			acceptanceClaims++
		}
		if !terminalWorkspaceLaunchStatus(stringValue(row["status"])) {
			inFlight++
		}
	}
	if claim.AcceptanceBCapacitySlot {
		if acceptanceClaims >= 1 {
			return errWorkspaceLaunchCapacityReached
		}
	} else if inFlight >= controlledBasicPilotGlobalInFlightLimit() {
		return errWorkspaceLaunchCapacityReached
	}
	if err := saveRecord(ctx, desired.ID, controlPlaneRecord(claim.DesiredOperation), client.RuntimeOperation.Create(), runtimeOpEntFields); err != nil {
		if controlplaneent.IsConstraintError(err) {
			return errWorkspaceLaunchCASConflict
		}
		return err
	}
	return tx.Commit()
}

func (s *postgresEntStateStore) PersistWorkspaceLaunchReconcile(ctx context.Context, update workspaceLaunchReconcileCAS) error {
	if _, err := decodeWorkspaceLaunchReconcileOperation(update.DesiredOperation); err != nil {
		return errWorkspaceLaunchCASConflict
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	entity, err := client.RuntimeOperation.Query().Where(runtimeoperation.IDEQ(update.OperationID), lockRowForUpdate).Only(ctx)
	if err != nil {
		if controlplaneent.IsNotFound(err) {
			return errWorkspaceLaunchCASConflict
		}
		return err
	}
	current := recordFromEnt(entity, runtimeOpEntFields)
	if stringValue(current["result"]) != update.ExpectedOperationResult || !workspaceLaunchReconcileIdentityMatches(current, update.DesiredOperation) {
		return errWorkspaceLaunchCASConflict
	}
	builder := client.RuntimeOperation.UpdateOneID(update.OperationID)
	setRecordFieldsWithEmptyText(builder, update.DesiredOperation, runtimeOpEntFields, true)
	if err := execCreate(ctx, builder); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *postgresEntStateStore) ClaimWorkspaceRenewal(ctx context.Context, claim workspaceRenewalClaimCAS) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	entity, err := client.Workspace.Query().Where(workspace.IDEQ(claim.WorkspaceID), lockRowForUpdate).Only(ctx)
	if err != nil {
		if controlplaneent.IsNotFound(err) {
			return errWorkspaceRenewalCASConflict
		}
		return err
	}
	current := recordFromEnt(entity, workspaceEntFields)
	autoRenew, validAutoRenew := current["autoRenew"].(bool)
	if stringValue(current["accountId"]) != claim.AccountID || stringValue(current["paidThrough"]) != claim.ExpectedPaidThrough || !validAutoRenew || autoRenew != claim.ExpectedAutoRenew {
		return errWorkspaceRenewalCASConflict
	}
	operationEntities, err := client.RuntimeOperation.Query().Where(runtimeoperation.WorkspaceIDEQ(claim.WorkspaceID), lockRowForUpdate).All(ctx)
	if err != nil {
		return err
	}
	operations := make([]map[string]any, 0, len(operationEntities))
	desiredID := stringValue(claim.DesiredOperation["id"])
	var existing map[string]any
	for _, operation := range operationEntities {
		row := recordFromEnt(operation, runtimeOpEntFields)
		operations = append(operations, row)
		if stringValue(row["id"]) == desiredID {
			existing = row
		}
	}
	if runtimeOperationsVersion(operations, claim.WorkspaceID) != claim.ExpectedOperationsVersion {
		return errWorkspaceRenewalCASConflict
	}
	if claim.ExpectedOperationResult == "" {
		if existing != nil {
			return errWorkspaceRenewalCASConflict
		}
		if err := saveRecord(ctx, desiredID, controlPlaneRecord(claim.DesiredOperation), client.RuntimeOperation.Create(), runtimeOpEntFields); err != nil {
			if controlplaneent.IsConstraintError(err) {
				return errWorkspaceRenewalCASConflict
			}
			return err
		}
	} else {
		if existing == nil || stringValue(existing["result"]) != claim.ExpectedOperationResult {
			return errWorkspaceRenewalCASConflict
		}
		if !workspaceRenewalClaimIdentityMatches(existing, claim.DesiredOperation) {
			return errIdempotencyConflict
		}
		builder := client.RuntimeOperation.UpdateOneID(desiredID)
		setRecordFieldsWithEmptyText(builder, claim.DesiredOperation, runtimeOpEntFields, true)
		if err := execCreate(ctx, builder); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *postgresEntStateStore) PersistWorkspaceRenewal(ctx context.Context, update workspaceRenewalPersistCAS) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	var mergedWorkspace map[string]any
	if update.WorkspacePatch != nil {
		if update.WorkspaceID == "" || update.ExpectedWorkspacePaidThrough == "" || update.WorkspaceID != stringValue(update.DesiredOperation["workspaceId"]) {
			return errWorkspaceRenewalCASConflict
		}
		entity, err := client.Workspace.Query().Where(workspace.IDEQ(update.WorkspaceID), lockRowForUpdate).Only(ctx)
		if err != nil {
			return err
		}
		currentWorkspace := recordFromEnt(entity, workspaceEntFields)
		if stringValue(currentWorkspace["paidThrough"]) != update.ExpectedWorkspacePaidThrough ||
			!workspaceRenewalExpectedFieldsMatch(currentWorkspace, update.ExpectedWorkspaceFields) {
			return errWorkspaceRenewalCASConflict
		}
		mergedWorkspace, err = mergeWorkspaceRenewalPatch(currentWorkspace, update.WorkspacePatch)
		if err != nil {
			return err
		}
	} else if update.WorkspaceID != "" || update.ExpectedWorkspacePaidThrough != "" || len(update.ExpectedWorkspaceFields) != 0 {
		return errInvalidWorkspaceRenewalPatch
	}
	entity, err := client.RuntimeOperation.Query().Where(runtimeoperation.IDEQ(update.OperationID), lockRowForUpdate).Only(ctx)
	if err != nil {
		if controlplaneent.IsNotFound(err) {
			return errWorkspaceRenewalCASConflict
		}
		return err
	}
	current := recordFromEnt(entity, runtimeOpEntFields)
	if stringValue(current["result"]) != update.ExpectedOperationResult || stringValue(current["workspaceId"]) != stringValue(update.DesiredOperation["workspaceId"]) ||
		stringValue(current["action"]) != stringValue(update.DesiredOperation["action"]) {
		return errWorkspaceRenewalCASConflict
	}
	if mergedWorkspace != nil {
		if update.WorkspaceID != stringValue(current["workspaceId"]) {
			return errWorkspaceRenewalCASConflict
		}
		builder := client.Workspace.UpdateOneID(update.WorkspaceID)
		setRecordFieldsWithEmptyText(builder, mergedWorkspace, workspaceEntFields, true)
		if err := execCreate(ctx, builder); err != nil {
			return err
		}
	}
	builder := client.RuntimeOperation.UpdateOneID(update.OperationID)
	setRecordFieldsWithEmptyText(builder, update.DesiredOperation, runtimeOpEntFields, true)
	if err := execCreate(ctx, builder); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *postgresEntStateStore) ActivateWorkspaceLaunchProjection(ctx context.Context, row map[string]any) (map[string]any, error) {
	if err := validateWorkspaceBillingState(row); err != nil {
		return nil, err
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	ownerEntity, ownerErr := client.User.Query().Where(user.IDEQ(stringValue(row["ownerUserId"])), lockRowForUpdate).Only(ctx)
	if ownerErr != nil && !controlplaneent.IsNotFound(ownerErr) {
		return nil, ownerErr
	}
	var owner map[string]any
	if ownerErr == nil {
		owner = recordFromEnt(ownerEntity, userEntFields)
	}
	existingEntity, existingErr := client.Workspace.Query().Where(workspace.IDEQ(stringValue(row["id"])), lockRowForUpdate).Only(ctx)
	if existingErr != nil && !controlplaneent.IsNotFound(existingErr) {
		return nil, existingErr
	}
	var existing map[string]any
	if existingErr == nil {
		existing = recordFromEnt(existingEntity, workspaceEntFields)
	}
	prepared, err := prepareWorkspaceLaunchProjection(row, owner, existing)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		builder := client.Workspace.UpdateOneID(stringValue(prepared["id"]))
		setRecordFieldsWithEmptyText(builder, prepared, workspaceEntFields, true)
		if err := execCreate(ctx, builder); err != nil {
			return nil, err
		}
	} else if err := saveRecord(ctx, stringValue(prepared["id"]), prepared, client.Workspace.Create(), workspaceEntFields); err != nil {
		if controlplaneent.IsConstraintError(err) {
			return nil, errIdempotencyConflict
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return prepared, nil
}

func saveWorkspaceRecord(ctx context.Context, client *controlplaneent.Client, row map[string]any) error {
	id := stringValue(row["id"])
	if id == "" {
		return errors.New("missing_record_id")
	}
	entity, err := client.Workspace.Query().Where(workspace.IDEQ(id), lockRowForUpdate).Only(ctx)
	if err == nil {
		row, err = mergeWorkspaceForSave(recordFromEnt(entity, workspaceEntFields), row)
		if err != nil {
			return err
		}
		if err := validateWorkspaceBillingState(row); err != nil {
			return err
		}
		builder := client.Workspace.UpdateOneID(id)
		setRecordFieldsWithEmptyText(builder, row, workspaceEntFields, true)
		return execCreate(ctx, builder)
	}
	if !controlplaneent.IsNotFound(err) {
		return err
	}
	if err := saveRecord(ctx, id, row, client.Workspace.Create(), workspaceEntFields); controlplaneent.IsConstraintError(err) {
		return errIdempotencyConflict
	} else {
		return err
	}
}

func (s *postgresEntStateStore) ClaimWorkspaceCreate(ctx context.Context, workspaceRow map[string]any, operation map[string]any) error {
	accountID := firstNonEmpty(stringValue(workspaceRow["accountId"]), stringValue(workspaceRow["ownerAccountId"]))
	workspaceID, operationID := stringValue(workspaceRow["id"]), stringValue(operation["id"])
	if accountID == "" || workspaceID == "" || operationID == "" {
		return errors.New("invalid_workspace_create_claim")
	}
	if stringValue(operation["accountId"]) != accountID || stringValue(operation["workspaceId"]) != workspaceID {
		return errPrimaryWorkspaceExists
	}
	var claim workspaceCreateOperationResult
	if stringValue(operation["action"]) == "workspace.create" {
		var claimErr error
		claim, claimErr = decodeWorkspaceCreateOperation(operation)
		if claimErr != nil || claim.Workspace.ID != workspaceID || claim.Workspace.AccountID != accountID {
			return errPrimaryWorkspaceExists
		}
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	existing, err := tx.RuntimeOperation.Get(ctx, operationID)
	if err == nil {
		if stringValue(operation["action"]) != "workspace.create" || existing.Action != "workspace.create" {
			return errPrimaryWorkspaceExists
		}
		current, currentErr := decodeWorkspaceCreateOperation(recordFromEnt(existing, runtimeOpEntFields))
		persistedEntity, persistedErr := tx.Workspace.Query().Where(workspace.IDEQ(workspaceID), lockRowForUpdate).Only(ctx)
		if controlplaneent.IsNotFound(persistedErr) {
			return errPrimaryWorkspaceExists
		}
		if persistedErr != nil {
			return persistedErr
		}
		persisted := recordFromEnt(persistedEntity, workspaceEntFields)
		if currentErr != nil || !workspaceCreateClaimCompatible(current, claim, persisted) || existing.AccountID != accountID || existing.WorkspaceID != workspaceID {
			return errPrimaryWorkspaceExists
		}
		if existing.Status != "retryable" && (existing.Status != "started" || current.LeaseExpiresAt != nil && current.LeaseExpiresAt.After(time.Now().UTC())) {
			return errPrimaryWorkspaceExists
		}
		_, err := tx.RuntimeOperation.UpdateOneID(existing.ID).
			Where(runtimeoperation.StatusEQ(existing.Status), runtimeoperation.ResultEQ(existing.Result)).
			SetStatus("started").
			SetResult(stringValue(operation["result"])).
			Save(ctx)
		if err != nil {
			if controlplaneent.IsNotFound(err) {
				return errPrimaryWorkspaceExists
			}
			return err
		}
		return tx.Commit()
	}
	if !controlplaneent.IsNotFound(err) {
		return err
	}
	if _, err := tx.Workspace.Query().Where(workspace.Or(workspace.AccountID(accountID), workspace.And(workspace.AccountID(""), workspace.OwnerAccountID(accountID)))).First(ctx); err == nil {
		return errPrimaryWorkspaceExists
	} else if !controlplaneent.IsNotFound(err) {
		return err
	}
	workspaceRow = cloneMap(workspaceRow)
	if _, ok := workspaceRow["customerProduct"]; !ok {
		workspaceRow["customerProduct"] = true
	}
	if err := saveRecord(ctx, stringValue(workspaceRow["id"]), workspaceRow, tx.Workspace.Create(), workspaceEntFields); err != nil {
		if controlplaneent.IsConstraintError(err) {
			return errPrimaryWorkspaceExists
		}
		return err
	}
	if err := saveRecord(ctx, stringValue(operation["id"]), operation, tx.RuntimeOperation.Create(), runtimeOpEntFields); err != nil {
		if controlplaneent.IsConstraintError(err) {
			return errPrimaryWorkspaceExists
		}
		return err
	}
	return tx.Commit()
}

func (s *postgresEntStateStore) DeleteWorkspace(ctx context.Context, id string) error {
	err := s.client.Workspace.DeleteOneID(id).Exec(ctx)
	if controlplaneent.IsNotFound(err) {
		return nil
	}
	return err
}

func (s *postgresEntStateStore) ApplyWorkspaceDelete(ctx context.Context, mutation workspaceDeleteStoreMutation) error {
	desired, ok := validWorkspaceDeleteStoreMutation(mutation)
	if !ok {
		return errWorkspaceDeleteCASConflict
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()

	workspaceEntity, workspaceErr := client.Workspace.Query().Where(workspace.IDEQ(desired.WorkspaceID), lockRowForUpdate).Only(ctx)
	if mutation.RequireWorkspaceAbsent {
		if workspaceErr == nil || !controlplaneent.IsNotFound(workspaceErr) {
			return errWorkspaceDeleteCASConflict
		}
	} else {
		if workspaceErr != nil {
			if controlplaneent.IsNotFound(workspaceErr) {
				return errWorkspaceDeleteCASConflict
			}
			return workspaceErr
		}
		workspaceRow := recordFromEnt(workspaceEntity, workspaceEntFields)
		if firstNonEmpty(stringValue(workspaceRow["accountId"]), stringValue(workspaceRow["ownerAccountId"])) != desired.AccountID ||
			firstNonEmpty(stringValue(workspaceRow["ownerUserId"]), stringValue(workspaceRow["ownerId"])) != desired.OwnerUserID {
			return errWorkspaceDeleteCASConflict
		}
	}

	operationEntity, operationErr := client.RuntimeOperation.Query().Where(runtimeoperation.IDEQ(desired.OperationID), lockRowForUpdate).Only(ctx)
	if mutation.Create {
		if operationErr == nil {
			return errWorkspaceDeleteCASConflict
		}
		if !controlplaneent.IsNotFound(operationErr) {
			return operationErr
		}
		if err := saveRecord(ctx, desired.OperationID, mutation.DesiredOperation, client.RuntimeOperation.Create(), runtimeOpEntFields); err != nil {
			if controlplaneent.IsConstraintError(err) {
				return errWorkspaceDeleteCASConflict
			}
			return err
		}
	} else {
		if operationErr != nil {
			if controlplaneent.IsNotFound(operationErr) {
				return errWorkspaceDeleteCASConflict
			}
			return operationErr
		}
		current := recordFromEnt(operationEntity, runtimeOpEntFields)
		if stringValue(current["result"]) != mutation.ExpectedResult || !workspaceDeleteOperationIdentityMatches(current, desired) {
			return errWorkspaceDeleteCASConflict
		}
		builder := client.RuntimeOperation.UpdateOneID(desired.OperationID)
		setRecordFieldsWithEmptyText(builder, mutation.DesiredOperation, runtimeOpEntFields, true)
		if err := execCreate(ctx, builder); err != nil {
			return err
		}
	}
	if mutation.DeleteWorkspace {
		if err := client.Workspace.DeleteOneID(desired.WorkspaceID).Exec(ctx); err != nil {
			return err
		}
	}
	return tx.Commit()
}
