package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
	"opl-cloud/services/control-plane/internal/domain"
)

const (
	providerAcceptanceConfirmation           = "I_UNDERSTAND_THIS_BUYS_ONE_PREPAID_CVM_AND_CBS"
	providerAcceptanceLifetimePurchaseBudget = 2
)

var errProviderAcceptanceStateRead = errors.New("provider_acceptance_state_read_failed")

type providerAcceptanceSlot struct {
	ID           string
	AccountID    string
	OwnerEmail   string
	Key          string
	PackageID    string
	InstanceType string
	StorageGB    int
	OperationID  string
}

var providerAcceptanceSlots = map[string]providerAcceptanceSlot{
	"verification-slot-basic-01": {
		ID: "verification-slot-basic-01", AccountID: "acct-verification-slot-basic-01", OwnerEmail: "verification-slot-basic-01@fenggaolab.org",
		Key: "provider-acceptance:verification-slot-basic-01", PackageID: "basic", InstanceType: "SA5.MEDIUM4", StorageGB: 10,
		OperationID: "provider-acceptance-verification-slot-basic-01",
	},
	"verification-slot-pro-01": {
		ID: "verification-slot-pro-01", AccountID: "acct-verification-slot-pro-01", OwnerEmail: "verification-slot-pro-01@fenggaolab.org",
		Key: "provider-acceptance:verification-slot-pro-01", PackageID: "pro", InstanceType: "SA5.2XLARGE16", StorageGB: 100,
		OperationID: "provider-acceptance-verification-slot-pro-01",
	},
}

func registerProviderAcceptanceRoutes(mux *http.ServeMux, app *controlPlaneServer, service *controlplane.Service) {
	mux.HandleFunc("POST /api/operator/provider-acceptance", app.providerAcceptanceProtected(func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		key, ok := requiredMutationKey(w, r)
		if !ok {
			return
		}
		if stringField(input, "confirmation", "") != providerAcceptanceConfirmation {
			writeError(w, http.StatusBadRequest, "provider_acceptance_confirmation_required")
			return
		}
		slot, exists := providerAcceptanceSlots[stringField(input, "slotId", "")]
		if !exists {
			writeError(w, http.StatusBadRequest, "provider_acceptance_slot_fixed")
			return
		}
		if stringField(input, "accountId", "") != slot.AccountID {
			writeError(w, http.StatusBadRequest, "provider_acceptance_account_fixed")
			return
		}
		if key != slot.Key {
			writeError(w, http.StatusConflict, "provider_acceptance_idempotency_key_fixed")
			return
		}

		unlock := app.lockResource("provider-acceptance", slot.ID)
		defer unlock()

		ownerID, sub2APIUserID, code := app.providerAcceptanceIdentity(r.Context(), slot)
		if code != "" {
			writeError(w, http.StatusConflict, code)
			return
		}
		workspaces, err := app.providerAcceptanceWorkspaceCandidates(r.Context(), slot)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		workspace, conflict := providerAcceptanceWorkspace(workspaces, slot)
		if conflict {
			writeError(w, http.StatusConflict, errPrimaryWorkspaceExists.Error())
			return
		}
		operation, operationExists, err := app.providerAcceptanceOperation(r.Context(), slot)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		computes, err := app.providerAcceptanceComputeCandidates(r.Context(), slot)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		storages, err := app.providerAcceptanceStorageCandidates(r.Context(), slot)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		identitiesValid := providerAcceptanceResourceInventoryValid(computes, slot, providerAcceptanceComputeID(slot), ownerID) &&
			providerAcceptanceResourceInventoryValid(storages, slot, providerAcceptanceStorageID(slot), ownerID)
		workspaceIdentityValid := workspace == nil || providerAcceptanceWorkspaceCandidateValid(workspace, slot, ownerID)
		emptyInventory := workspace == nil && len(computes) == 0 && len(storages) == 0
		now := time.Now().UTC()
		completeInventory := providerAcceptanceWorkspaceCandidateValid(workspace, slot, ownerID) && len(computes) == 1 && len(storages) == 1 &&
			providerAcceptanceComputeValid(computes[0], slot, ownerID, now) && providerAcceptanceStorageValid(storages[0], slot, ownerID, now)
		invalidOperation := operationExists && !providerAcceptanceOperationValid(operation, slot)
		unclaimedAmbiguousInventory := !operationExists && !emptyInventory && !completeInventory
		if !workspaceIdentityValid || !identitiesValid || invalidOperation || unclaimedAmbiguousInventory {
			writeError(w, http.StatusConflict, "provider_acceptance_inventory_ambiguous")
			return
		}
		if operationExists && stringValue(operation["status"]) == "manual_review" {
			summary, err := app.providerAcceptanceSlotSummary(r.Context(), slot)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "state_read_failed")
				return
			}
			writeJSON(w, http.StatusOK, providerAcceptanceResponse("manual_review", stringValue(operation["errorCode"]), summary))
			return
		}
		summary, ready, err := app.providerAcceptanceReadySlot(r.Context(), slot, ownerID, time.Now().UTC())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		if ready {
			if !operationExists {
				operation = providerAcceptanceOperationRow("succeeded", slot)
			}
			if !operationExists || stringValue(operation["status"]) == "started" {
				operation["status"] = "succeeded"
				delete(operation, "errorCode")
				operation["result"] = string(mustJSON(providerAcceptanceResponse("reused", "", summary)))
				if err := app.tables.SaveRuntimeOperation(r.Context(), operation); err != nil {
					writeError(w, http.StatusInternalServerError, "state_persist_failed")
					return
				}
			}
			writeJSON(w, http.StatusOK, providerAcceptanceResponse("reused", "", summary))
			return
		}
		if operationExists && stringValue(operation["status"]) == "succeeded" {
			app.writeProviderAcceptanceManualReview(w, r, operation, slot, "provider_acceptance_state_ambiguous")
			return
		}
		approved, _ := input["environmentApproved"].(bool)
		if !approved {
			writeError(w, http.StatusConflict, "provider_acceptance_environment_approval_required")
			return
		}
		if numberField(input, "purchaseBudget", 0) != 1 {
			writeError(w, http.StatusConflict, "provider_acceptance_purchase_budget_invalid")
			return
		}
		maxApprovedProviderCost := numberField(input, "maxApprovedProviderCost", 0)
		if maxApprovedProviderCost <= 0 {
			writeError(w, http.StatusConflict, "provider_acceptance_provider_cost_approval_required")
			return
		}

		workspaceKey, err := service.Sub2APIWorkspaceKey(r.Context(), sub2APIUserID)
		if err != nil || workspaceKey.UserID != sub2APIUserID || workspaceKey.Name != "opl-workspace" || workspaceKey.Status != "active" || workspaceKey.Key == "" {
			writeError(w, http.StatusConflict, "provider_acceptance_gateway_key_required")
			return
		}
		computePreflight, storagePreflight, ok := providerAcceptancePreflight(r.Context(), service, slot)
		if !ok {
			writeError(w, http.StatusConflict, "provider_acceptance_preflight_failed")
			return
		}
		if computePreflight.ProviderPriceCNY+storagePreflight.ProviderPriceCNY > maxApprovedProviderCost {
			writeError(w, http.StatusConflict, "provider_acceptance_provider_cost_exceeds_approval")
			return
		}

		if !operationExists {
			operation = providerAcceptanceOperationRow("started", slot)
			if workspace == nil {
				workspace = providerAcceptanceWorkspaceClaim(ownerID, slot)
				if err := app.tables.ClaimWorkspaceCreate(r.Context(), workspace, operation); err != nil {
					if errors.Is(err, errPrimaryWorkspaceExists) {
						writeError(w, http.StatusConflict, errPrimaryWorkspaceExists.Error())
					} else {
						writeError(w, http.StatusInternalServerError, "state_persist_failed")
					}
					return
				}
			} else if err := app.tables.SaveRuntimeOperation(r.Context(), operation); err != nil {
				writeError(w, http.StatusInternalServerError, "state_persist_failed")
				return
			}
		}

		status, reason, err := app.advanceProviderAcceptance(r.Context(), service, slot, ownerID, sub2APIUserID, computePreflight, storagePreflight)
		if err != nil {
			if errors.Is(err, errProviderAcceptanceStateRead) {
				writeError(w, http.StatusInternalServerError, "state_read_failed")
			} else {
				writeError(w, http.StatusInternalServerError, "state_persist_failed")
			}
			return
		}
		if reason != "" {
			app.writeProviderAcceptanceManualReview(w, r, operation, slot, reason)
			return
		}
		summary, err = app.providerAcceptanceSlotSummary(r.Context(), slot)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		if status == "ready" {
			operation["status"] = "succeeded"
			operation["result"] = string(mustJSON(providerAcceptanceResponse("ready", "", summary)))
			if err := app.appendAuditEvent(r, "operator.provider_acceptance", "verification_slot", slot.ID, slot.AccountID, nil, summary, "succeeded"); err != nil {
				app.writeProviderAcceptanceManualReview(w, r, operation, slot, "provider_acceptance_audit_failed")
				return
			}
			if err := app.tables.SaveRuntimeOperation(r.Context(), operation); err != nil {
				writeError(w, http.StatusInternalServerError, "state_persist_failed")
				return
			}
		}
		writeJSON(w, http.StatusOK, providerAcceptanceResponse(status, "", summary))
	}))
}

func (app *controlPlaneServer) providerAcceptanceProtected(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		expected := strings.TrimSpace(os.Getenv("OPL_PROVIDER_ACCEPTANCE_TOKEN"))
		want := sha256.Sum256([]byte(expected))
		got := sha256.Sum256([]byte(r.Header.Get("x-opl-provider-acceptance-token")))
		if expected == "" || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			writeError(w, http.StatusUnauthorized, "provider_acceptance_token_invalid")
			return
		}
		if !limitJSONBody(w, r) {
			return
		}
		actor := auditActor{UserID: "system:provider-acceptance", Role: "system"}
		next(w, r.WithContext(context.WithValue(r.Context(), auditActorContextKey{}, actor)))
	}
}

func (app *controlPlaneServer) providerAcceptanceIdentity(ctx context.Context, slot providerAcceptanceSlot) (string, int64, string) {
	accounts, err := app.tables.ListAccounts(ctx, slot.AccountID)
	if err != nil || len(accounts) != 1 || stringValue(accounts[0]["id"]) != slot.AccountID || stringValue(accounts[0]["status"]) != "active" {
		return "", 0, "provider_acceptance_account_required"
	}
	sub2APIUserID, err := app.sub2APIUserID(ctx, slot.AccountID)
	if err != nil {
		return "", 0, "provider_acceptance_account_mapping_required"
	}
	users, err := app.tables.ListUsers(ctx, false)
	if err != nil {
		return "", 0, "provider_acceptance_owner_required"
	}
	ownerID := ""
	for _, user := range users {
		if stringValue(user["accountId"]) != slot.AccountID || stringValue(user["role"]) != "owner" || stringValue(user["status"]) != "active" || !strings.EqualFold(stringValue(user["email"]), slot.OwnerEmail) {
			continue
		}
		if ownerID != "" {
			return "", 0, "provider_acceptance_owner_ambiguous"
		}
		ownerID = stringValue(user["id"])
	}
	if ownerID == "" {
		return "", 0, "provider_acceptance_owner_required"
	}
	return ownerID, sub2APIUserID, ""
}

func providerAcceptanceWorkspace(rows []map[string]any, slot providerAcceptanceSlot) (map[string]any, bool) {
	if len(rows) == 0 {
		return nil, false
	}
	if len(rows) != 1 {
		return nil, true
	}
	row := rows[0]
	if stringValue(row["id"]) != primaryWorkspaceID(slot.AccountID) || stringValue(row["verificationSlotId"]) != slot.ID || row["customerProduct"] != false {
		return nil, true
	}
	return row, false
}

func providerAcceptanceWorkspaceCandidateValid(row map[string]any, slot providerAcceptanceSlot, ownerID string) bool {
	return row != nil && stringValue(row["id"]) == primaryWorkspaceID(slot.AccountID) && stringValue(row["accountId"]) == slot.AccountID &&
		stringValue(row["ownerAccountId"]) == slot.AccountID && stringValue(row["ownerUserId"]) == ownerID && stringValue(row["name"]) == slot.ID &&
		stringValue(row["packageId"]) == slot.PackageID && stringValue(row["verificationSlotId"]) == slot.ID && row["customerProduct"] == false &&
		stringValue(row["computeAllocationId"]) == providerAcceptanceComputeID(slot) && stringValue(row["currentComputeAllocationId"]) == providerAcceptanceComputeID(slot) &&
		stringValue(row["storageId"]) == providerAcceptanceStorageID(slot)
}

func providerAcceptanceResourceInventoryValid(rows []map[string]any, slot providerAcceptanceSlot, resourceID, ownerID string) bool {
	if len(rows) == 0 {
		return true
	}
	if len(rows) != 1 {
		return false
	}
	row := rows[0]
	return stringValue(row["id"]) == resourceID && stringValue(row["accountId"]) == slot.AccountID && stringValue(row["ownerUserId"]) == ownerID &&
		stringValue(row["workspaceId"]) == primaryWorkspaceID(slot.AccountID) && stringValue(row["verificationSlotId"]) == slot.ID && row["customerProduct"] == false
}

func providerAcceptanceWorkspaceClaim(ownerID string, slot providerAcceptanceSlot) map[string]any {
	workspaceID := primaryWorkspaceID(slot.AccountID)
	return map[string]any{
		"id": workspaceID, "accountId": slot.AccountID, "ownerAccountId": slot.AccountID, "ownerUserId": ownerID,
		"name": slot.ID, "packageId": slot.PackageID, "provider": "tencent-tke", "state": "provisioning", "status": "provisioning",
		"computeAllocationId": providerAcceptanceComputeID(slot), "currentComputeAllocationId": providerAcceptanceComputeID(slot), "storageId": providerAcceptanceStorageID(slot),
		"verificationSlotId": slot.ID, "customerProduct": false,
	}
}

func providerAcceptanceOperationRow(status string, slot providerAcceptanceSlot) map[string]any {
	workspaceID := primaryWorkspaceID(slot.AccountID)
	return map[string]any{
		"id": slot.OperationID, "operationId": slot.OperationID, "accountId": slot.AccountID, "workspaceId": workspaceID,
		"resourceId": slot.ID, "resourceKind": "verification_slot", "action": "provider_acceptance", "provider": "tencent-tke",
		"status": status, "result": "{}", "computeAllocationId": providerAcceptanceComputeID(slot), "storageId": providerAcceptanceStorageID(slot),
	}
}

func (app *controlPlaneServer) providerAcceptanceOperation(ctx context.Context, slot providerAcceptanceSlot) (map[string]any, bool, error) {
	operations, err := app.tables.ListRuntimeOperations(ctx)
	if err != nil {
		return nil, false, err
	}
	for _, operation := range operations {
		if stringValue(operation["id"]) == slot.OperationID {
			return operation, true, nil
		}
	}
	return nil, false, nil
}

func providerAcceptanceOperationValid(operation map[string]any, slot providerAcceptanceSlot) bool {
	status := stringValue(operation["status"])
	return (status == "started" || status == "manual_review" || status == "succeeded") &&
		stringValue(operation["id"]) == slot.OperationID && stringValue(operation["operationId"]) == slot.OperationID &&
		stringValue(operation["accountId"]) == slot.AccountID && stringValue(operation["workspaceId"]) == primaryWorkspaceID(slot.AccountID) &&
		stringValue(operation["resourceId"]) == slot.ID && stringValue(operation["resourceKind"]) == "verification_slot" &&
		stringValue(operation["action"]) == "provider_acceptance" && stringValue(operation["computeAllocationId"]) == providerAcceptanceComputeID(slot) &&
		stringValue(operation["storageId"]) == providerAcceptanceStorageID(slot)
}

func providerAcceptancePreflight(ctx context.Context, service *controlplane.Service, slot providerAcceptanceSlot) (clients.MonthlyPreflight, clients.MonthlyPreflight, bool) {
	zone := monthlyComputeLaunchZone()
	if zone == "" || monthlyComputeInstanceType(slot.PackageID) != slot.InstanceType {
		return clients.MonthlyPreflight{}, clients.MonthlyPreflight{}, false
	}
	compute, err := service.PreflightMonthlyResource(ctx, clients.MonthlyPreflightInput{ResourceType: "compute", PackageID: slot.PackageID, Zone: zone})
	if err != nil || !providerAcceptancePreflightValid(compute, slot, "compute", 0, zone) {
		return clients.MonthlyPreflight{}, clients.MonthlyPreflight{}, false
	}
	storage, err := service.PreflightMonthlyResource(ctx, clients.MonthlyPreflightInput{ResourceType: "storage", PackageID: slot.PackageID, SizeGB: slot.StorageGB, Zone: zone})
	if err != nil || !providerAcceptancePreflightValid(storage, slot, "storage", slot.StorageGB, zone) {
		return clients.MonthlyPreflight{}, clients.MonthlyPreflight{}, false
	}
	return compute, storage, true
}

func providerAcceptancePreflightValid(preflight clients.MonthlyPreflight, slot providerAcceptanceSlot, resourceType string, sizeGB int, zone string) bool {
	return preflight.ResourceType == resourceType && preflight.PackageID == slot.PackageID && preflight.SizeGB == sizeGB && preflight.Zone == zone && preflight.Available &&
		preflight.ChargeType == "PREPAID" && preflight.PeriodMonths == 1 && preflight.RenewFlag == "NOTIFY_AND_MANUAL_RENEW" && preflight.ProviderPriceCNY > 0
}

func providerAcceptanceComputeID(slot providerAcceptanceSlot) string {
	return resourceIDForMutation("ca", slot.AccountID, slot.Key+":compute")
}

func providerAcceptanceStorageID(slot providerAcceptanceSlot) string {
	return resourceIDForMutation("vol", slot.AccountID, slot.Key+":storage")
}

func providerAcceptanceCandidates(rows []map[string]any, identities map[string]string) []map[string]any {
	// ponytail: Acceptance inventory is tiny; add indexed multi-key store queries before this operator-only scan needs to scale.
	candidates := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		for field, expected := range identities {
			if expected != "" && stringValue(row[field]) == expected {
				candidates = append(candidates, cloneMap(row))
				break
			}
		}
	}
	return candidates
}

func (app *controlPlaneServer) providerAcceptanceWorkspaceCandidates(ctx context.Context, slot providerAcceptanceSlot) ([]map[string]any, error) {
	rows, err := app.tables.ListWorkspaces(ctx, "")
	if err != nil {
		return nil, errProviderAcceptanceStateRead
	}
	return providerAcceptanceCandidates(rows, map[string]string{
		"id": primaryWorkspaceID(slot.AccountID), "accountId": slot.AccountID, "ownerAccountId": slot.AccountID, "verificationSlotId": slot.ID,
	}), nil
}

func (app *controlPlaneServer) providerAcceptanceComputeCandidates(ctx context.Context, slot providerAcceptanceSlot) ([]map[string]any, error) {
	rows, err := app.tables.ListComputes(ctx, "")
	if err != nil {
		return nil, errProviderAcceptanceStateRead
	}
	return providerAcceptanceCandidates(rows, map[string]string{
		"id": providerAcceptanceComputeID(slot), "accountId": slot.AccountID, "ownerAccountId": slot.AccountID,
		"workspaceId": primaryWorkspaceID(slot.AccountID), "verificationSlotId": slot.ID,
	}), nil
}

func (app *controlPlaneServer) providerAcceptanceStorageCandidates(ctx context.Context, slot providerAcceptanceSlot) ([]map[string]any, error) {
	rows, err := app.tables.ListStorages(ctx, "")
	if err != nil {
		return nil, errProviderAcceptanceStateRead
	}
	return providerAcceptanceCandidates(rows, map[string]string{
		"id": providerAcceptanceStorageID(slot), "accountId": slot.AccountID, "ownerAccountId": slot.AccountID,
		"workspaceId": primaryWorkspaceID(slot.AccountID), "verificationSlotId": slot.ID,
	}), nil
}

func (app *controlPlaneServer) providerAcceptanceWorkspaceExact(ctx context.Context, slot providerAcceptanceSlot) (map[string]any, bool, error) {
	rows, err := app.providerAcceptanceWorkspaceCandidates(ctx, slot)
	if err != nil {
		return nil, false, errProviderAcceptanceStateRead
	}
	workspace, conflict := providerAcceptanceWorkspace(rows, slot)
	return workspace, conflict, nil
}

func (app *controlPlaneServer) providerAcceptanceComputeExact(ctx context.Context, slot providerAcceptanceSlot) (map[string]any, bool, bool, error) {
	rows, err := app.providerAcceptanceComputeCandidates(ctx, slot)
	if err != nil {
		return nil, false, false, errProviderAcceptanceStateRead
	}
	if len(rows) == 0 {
		return nil, false, false, nil
	}
	if len(rows) != 1 || stringValue(rows[0]["id"]) != providerAcceptanceComputeID(slot) {
		return nil, false, true, nil
	}
	return cloneMap(rows[0]), true, false, nil
}

func (app *controlPlaneServer) providerAcceptanceStorageExact(ctx context.Context, slot providerAcceptanceSlot) (map[string]any, bool, bool, error) {
	rows, err := app.providerAcceptanceStorageCandidates(ctx, slot)
	if err != nil {
		return nil, false, false, errProviderAcceptanceStateRead
	}
	if len(rows) == 0 {
		return nil, false, false, nil
	}
	if len(rows) != 1 || stringValue(rows[0]["id"]) != providerAcceptanceStorageID(slot) {
		return nil, false, true, nil
	}
	return cloneMap(rows[0]), true, false, nil
}

func (app *controlPlaneServer) advanceProviderAcceptance(ctx context.Context, service *controlplane.Service, slot providerAcceptanceSlot, ownerID string, sub2APIUserID int64, computePreflight, storagePreflight clients.MonthlyPreflight) (string, string, error) {
	workspaceID := primaryWorkspaceID(slot.AccountID)
	computeID := providerAcceptanceComputeID(slot)
	compute, exists, conflict, err := app.providerAcceptanceComputeExact(ctx, slot)
	if err != nil {
		return "", "", err
	}
	if conflict {
		return "", "provider_acceptance_compute_state_ambiguous", nil
	}
	if !exists {
		created, createErr := service.PrepareMonthlyCompute(ctx, clients.ComputeAllocationInput{ID: computeID, AccountID: slot.AccountID, WorkspaceID: workspaceID, PackageID: slot.PackageID}, slot.Key+":compute")
		compute = providerAcceptanceComputeRow(structToMap(created), slot, ownerID, computePreflight)
		if err := app.saveComputeFact(compute); err != nil {
			return "", "", err
		}
		if createErr != nil {
			return "", "provider_acceptance_compute_result_unknown", nil
		}
	} else if monthlyResourceInProgress(compute) {
		synced, syncErr := service.SyncMonthlyCompute(ctx, computeID)
		compute = providerAcceptanceComputeRow(mergeMaps(compute, structToMap(synced)), slot, ownerID, computePreflight)
		if err := app.saveComputeFact(compute); err != nil {
			return "", "", err
		}
		if syncErr != nil {
			return "", "provider_acceptance_compute_result_unknown", nil
		}
	}
	if monthlyResourceInProgress(compute) {
		return "in_progress", "", nil
	}
	if !providerAcceptanceComputeValid(compute, slot, ownerID, time.Now().UTC()) {
		return "", "provider_acceptance_compute_state_ambiguous", nil
	}

	storageID := providerAcceptanceStorageID(slot)
	storage, exists, conflict, err := app.providerAcceptanceStorageExact(ctx, slot)
	if err != nil {
		return "", "", err
	}
	if conflict {
		return "", "provider_acceptance_storage_state_ambiguous", nil
	}
	if !exists {
		created, createErr := service.PrepareMonthlyStorage(ctx, clients.StorageVolumeInput{ID: storageID, AccountID: slot.AccountID, WorkspaceID: workspaceID, ComputeID: computeID, Zone: storagePreflight.Zone, SizeGB: slot.StorageGB}, slot.Key+":storage")
		storage = providerAcceptanceStorageRow(structToMap(created), slot, ownerID, storagePreflight)
		if err := app.saveStorageFact(storage); err != nil {
			return "", "", err
		}
		if createErr != nil {
			return "", "provider_acceptance_storage_result_unknown", nil
		}
	} else if monthlyResourceInProgress(storage) {
		synced, syncErr := service.SyncMonthlyStorage(ctx, storageID)
		storage = providerAcceptanceStorageRow(mergeMaps(storage, structToMap(synced)), slot, ownerID, storagePreflight)
		if err := app.saveStorageFact(storage); err != nil {
			return "", "", err
		}
		if syncErr != nil {
			return "", "provider_acceptance_storage_result_unknown", nil
		}
	}
	if monthlyResourceInProgress(storage) {
		return "in_progress", "", nil
	}
	if !providerAcceptanceStorageValid(storage, slot, ownerID, time.Now().UTC()) {
		return "", "provider_acceptance_storage_state_ambiguous", nil
	}

	attachment, attachmentCount, err := app.providerAcceptanceAttachment(ctx, slot)
	if err != nil {
		return "", "", err
	}
	if attachmentCount > 1 {
		return "", "provider_acceptance_attachment_state_ambiguous", nil
	}
	if attachmentCount == 0 {
		created, createErr := service.CreateStorageAttachment(ctx, controlplane.StorageAttachmentInput{WorkspaceID: workspaceID, ComputeID: computeID, VolumeID: storageID}, slot.Key+":attachment")
		attachment = attachmentResponse(structToMap(created), map[string]any{"computeAllocationId": computeID, "storageId": storageID, "mountPath": "/data"})
		attachment["accountId"], attachment["ownerAccountId"] = slot.AccountID, slot.AccountID
		if err := app.saveAttachmentFact(attachment, attachment); err != nil {
			return "", "", err
		}
		if createErr != nil {
			return "", "provider_acceptance_attachment_result_unknown", nil
		}
	}
	if stringValue(attachment["status"]) != "attached" || stringValue(attachment["workspaceId"]) != workspaceID || stringValue(attachment["computeAllocationId"]) != computeID || stringValue(attachment["storageId"]) != storageID {
		return "", "provider_acceptance_attachment_state_ambiguous", nil
	}

	workspace, workspaceConflict, err := app.providerAcceptanceWorkspaceExact(ctx, slot)
	if err != nil {
		return "", "", err
	}
	if workspaceConflict || workspace == nil {
		return "", "provider_acceptance_workspace_state_ambiguous", nil
	}
	var projection domain.WorkspaceProjection
	if stringValue(workspace["runtimeId"]) == "" {
		prepared, prepareErr := service.PrepareWorkspace(ctx, controlplane.CreateWorkspaceInput{
			WorkspaceID: workspaceID, AccountID: slot.AccountID, Sub2APIUserID: sub2APIUserID, OwnerID: ownerID,
			Name: slot.ID, PackageID: slot.PackageID, AttachmentID: stringValue(attachment["id"]), ComputeID: computeID, VolumeID: storageID,
		}, slot.Key+":workspace")
		if prepareErr != nil {
			return "", "provider_acceptance_runtime_result_unknown", nil
		}
		projection = prepared
	} else {
		runtime, runtimeErr := service.WorkspaceRuntimeStatus(ctx, workspaceID)
		if runtimeErr != nil {
			return "", "provider_acceptance_runtime_result_unknown", nil
		}
		projection = providerAcceptanceWorkspaceProjection(workspace, runtime, slot)
	}
	workspace = providerAcceptanceWorkspaceRow(projection, slot)
	if err := app.tables.SaveWorkspace(ctx, workspace); err != nil {
		return "", "", err
	}
	if !projection.RuntimeReady {
		return "in_progress", "", nil
	}
	if projection.ReceiptID == "" {
		withReceipt, receiptErr := service.RecordWorkspaceCreatedReceipt(ctx, projection, slot.Key+":workspace")
		if receiptErr != nil {
			return "", "provider_acceptance_receipt_failed", nil
		}
		projection = withReceipt
		if err := app.tables.SaveWorkspace(ctx, providerAcceptanceWorkspaceRow(projection, slot)); err != nil {
			return "", "", err
		}
	}
	if _, ready, err := app.providerAcceptanceReadySlot(ctx, slot, ownerID, time.Now().UTC()); err != nil {
		return "", "", err
	} else if !ready {
		return "", "provider_acceptance_state_ambiguous", nil
	}
	return "ready", "", nil
}

func providerAcceptanceComputeRow(row map[string]any, slot providerAcceptanceSlot, ownerID string, preflight clients.MonthlyPreflight) map[string]any {
	row = computeResponse(row)
	row["id"], row["accountId"], row["ownerAccountId"], row["ownerUserId"] = providerAcceptanceComputeID(slot), slot.AccountID, slot.AccountID, ownerID
	row["workspaceId"], row["packageId"], row["name"] = primaryWorkspaceID(slot.AccountID), slot.PackageID, slot.ID
	row["verificationSlotId"], row["customerProduct"] = slot.ID, false
	row["requestedPeriodMonths"], row["periodMonths"], row["chargeType"], row["renewFlag"] = preflight.PeriodMonths, preflight.PeriodMonths, preflight.ChargeType, preflight.RenewFlag
	row["billingOperationId"], row["monthlyPriceCnyCents"], row["chargeUsdMicros"] = slot.OperationID, int64(0), int64(0)
	row["zone"] = firstNonEmpty(stringValue(row["zone"]), providerDataValue(row, "zone"), preflight.Zone)
	row["instanceType"] = firstNonEmpty(stringValue(row["instanceType"]), providerDataValue(row, "instanceType"))
	providerAcceptanceEntitlement(row)
	return row
}

func providerAcceptanceStorageRow(row map[string]any, slot providerAcceptanceSlot, ownerID string, preflight clients.MonthlyPreflight) map[string]any {
	row = storageResponse(row)
	row["id"], row["accountId"], row["ownerAccountId"], row["ownerUserId"] = providerAcceptanceStorageID(slot), slot.AccountID, slot.AccountID, ownerID
	row["workspaceId"], row["packageId"], row["name"] = primaryWorkspaceID(slot.AccountID), slot.PackageID, slot.ID
	row["computeAllocationId"], row["sizeGb"] = providerAcceptanceComputeID(slot), slot.StorageGB
	row["verificationSlotId"], row["customerProduct"] = slot.ID, false
	row["requestedPeriodMonths"], row["periodMonths"], row["chargeType"], row["renewFlag"] = preflight.PeriodMonths, preflight.PeriodMonths, preflight.ChargeType, preflight.RenewFlag
	row["billingOperationId"], row["monthlyPriceCnyCents"], row["chargeUsdMicros"] = slot.OperationID, int64(0), int64(0)
	row["zone"] = firstNonEmpty(stringValue(row["zone"]), providerDataValue(row, "zone"), preflight.Zone)
	row["pvName"] = firstNonEmpty(stringValue(row["pvName"]), providerDataValue(row, "pvName"))
	row["persistentVolumeName"] = firstNonEmpty(stringValue(row["persistentVolumeName"]), stringValue(row["pvName"]))
	providerAcceptanceEntitlement(row)
	return row
}

func providerAcceptanceEntitlement(row map[string]any) {
	if monthlyResourcePrepared("compute", row) || monthlyResourcePrepared("storage", row) {
		row["billingStatus"], row["providerClaimStatus"], row["desiredStatus"] = "active", "claimed", stringValue(row["status"])
		row["periodStart"] = firstNonEmpty(stringValue(row["periodStart"]), time.Now().UTC().Format(time.RFC3339))
		if deadline, err := monthlyProviderDeadline(row); err == nil {
			row["paidThrough"] = deadline.Format(time.RFC3339)
		}
		return
	}
	row["billingStatus"], row["providerClaimStatus"], row["desiredStatus"] = "preparing", "pending", stringValue(row["status"])
}

func providerAcceptanceCostTagsValid(row map[string]any, slot providerAcceptanceSlot) bool {
	tags := mapField(row, "costTags")
	return stringValue(tags["opl_account_id"]) == slot.AccountID && stringValue(tags["opl_workspace_id"]) == primaryWorkspaceID(slot.AccountID) &&
		stringValue(tags["opl_resource_id"]) == stringValue(row["id"]) && strings.TrimSpace(stringValue(tags["opl_operation_id"])) != ""
}

func providerAcceptanceComputeValid(row map[string]any, slot providerAcceptanceSlot, ownerID string, now time.Time) bool {
	deadline, err := monthlyProviderDeadline(row)
	return err == nil && deadline.After(now) && monthlyResourcePrepared("compute", row) && stringValue(row["accountId"]) == slot.AccountID &&
		stringValue(row["ownerUserId"]) == ownerID && stringValue(row["workspaceId"]) == primaryWorkspaceID(slot.AccountID) && stringValue(row["packageId"]) == slot.PackageID &&
		stringValue(row["instanceType"]) == slot.InstanceType && stringValue(row["zone"]) == monthlyComputeLaunchZone() &&
		stringValue(row["chargeType"]) == "PREPAID" && numberField(row, "periodMonths", 0) == 1 && stringValue(row["renewFlag"]) == "NOTIFY_AND_MANUAL_RENEW" &&
		strings.HasPrefix(firstNonEmpty(stringValue(row["cvmInstanceId"]), stringValue(row["instanceId"])), "ins-") && strings.HasPrefix(stringValue(row["nodePoolId"]), "np-") && providerAcceptanceCostTagsValid(row, slot)
}

func providerAcceptanceStorageValid(row map[string]any, slot providerAcceptanceSlot, ownerID string, now time.Time) bool {
	deadline, err := monthlyProviderDeadline(row)
	return err == nil && deadline.After(now) && monthlyResourcePrepared("storage", row) && stringValue(row["accountId"]) == slot.AccountID &&
		stringValue(row["ownerUserId"]) == ownerID && stringValue(row["workspaceId"]) == primaryWorkspaceID(slot.AccountID) && numberField(row, "sizeGb", 0) == float64(slot.StorageGB) &&
		stringValue(row["zone"]) == monthlyComputeLaunchZone() && stringValue(row["chargeType"]) == "PREPAID" && numberField(row, "periodMonths", 0) == 1 &&
		stringValue(row["renewFlag"]) == "NOTIFY_AND_MANUAL_RENEW" && strings.HasPrefix(stringValue(row["providerResourceId"]), "disk-") &&
		firstNonEmpty(stringValue(row["pvName"]), stringValue(row["persistentVolumeName"]), providerDataValue(row, "pvName")) != "" && providerAcceptanceCostTagsValid(row, slot)
}

func (app *controlPlaneServer) providerAcceptanceAttachment(ctx context.Context, slot providerAcceptanceSlot) (map[string]any, int, error) {
	attachments, err := app.tables.ListAttachments(ctx, "")
	if err != nil {
		return nil, 0, errProviderAcceptanceStateRead
	}
	var found map[string]any
	count := 0
	for _, attachment := range attachments {
		if stringValue(attachment["workspaceId"]) == primaryWorkspaceID(slot.AccountID) {
			found, count = attachment, count+1
		}
	}
	return found, count, nil
}

func providerAcceptanceWorkspaceProjection(workspace map[string]any, runtime clients.WorkspaceRuntime, slot providerAcceptanceSlot) domain.WorkspaceProjection {
	status := firstNonEmpty(runtime.Status, stringValue(workspace["status"]), "provisioning")
	if runtime.Ready {
		status = "running"
	}
	access := mapField(workspace, "access")
	return domain.WorkspaceProjection{
		ID: stringValue(workspace["id"]), AccountID: slot.AccountID, OwnerID: stringValue(workspace["ownerUserId"]), Name: slot.ID,
		PackageID: slot.PackageID, Provider: "tencent-tke", URL: firstNonEmpty(runtime.URL, stringValue(workspace["url"])), Status: status,
		ComputeID: providerAcceptanceComputeID(slot), VolumeID: providerAcceptanceStorageID(slot), AttachmentID: firstNonEmpty(stringValue(workspace["attachmentId"]), stringValue(workspace["currentAttachmentId"])),
		RuntimeID: firstNonEmpty(runtime.ID, stringValue(workspace["runtimeId"])), RuntimeServiceName: firstNonEmpty(runtime.ServiceName, stringValue(mapField(workspace, "runtime")["serviceName"])), RuntimeReady: runtime.Ready,
		RuntimeUsername: firstNonEmpty(runtime.Access.Username, stringValue(access["username"])), CredentialStatus: firstNonEmpty(runtime.Access.CredentialStatus, stringValue(access["credentialStatus"])),
		CredentialVersion: firstNonEmpty(runtime.Access.CredentialVersion, stringValue(access["credentialVersion"])), CredentialSecretRef: firstNonEmpty(runtime.Access.SecretRef, stringValue(access["secretRef"])), ReceiptID: stringValue(workspace["receiptId"]),
	}
}

func providerAcceptanceWorkspaceRow(projection domain.WorkspaceProjection, slot providerAcceptanceSlot) map[string]any {
	row := workspaceProjectionRow(projection)
	row["verificationSlotId"], row["customerProduct"] = slot.ID, false
	row["runtimeServiceName"], row["serviceName"] = projection.RuntimeServiceName, projection.RuntimeServiceName
	return row
}

func (app *controlPlaneServer) providerAcceptanceReadySlot(ctx context.Context, slot providerAcceptanceSlot, ownerID string, now time.Time) (map[string]any, bool, error) {
	workspace, workspaceConflict, err := app.providerAcceptanceWorkspaceExact(ctx, slot)
	if err != nil {
		return nil, false, err
	}
	compute, computeOK, computeConflict, err := app.providerAcceptanceComputeExact(ctx, slot)
	if err != nil {
		return nil, false, err
	}
	storage, storageOK, storageConflict, err := app.providerAcceptanceStorageExact(ctx, slot)
	if err != nil {
		return nil, false, err
	}
	attachment, attachmentCount, err := app.providerAcceptanceAttachment(ctx, slot)
	if err != nil {
		return nil, false, err
	}
	if workspaceConflict || computeConflict || storageConflict {
		return nil, false, errProviderAcceptanceStateRead
	}
	if !providerAcceptanceWorkspaceCandidateValid(workspace, slot, ownerID) || stringValue(workspace["url"]) == "" ||
		!computeOK || !storageOK || attachmentCount != 1 || !providerAcceptanceComputeValid(compute, slot, ownerID, now) || !providerAcceptanceStorageValid(storage, slot, ownerID, now) ||
		stringValue(attachment["status"]) != "attached" || app.workspaceResponse(cloneMap(workspace))["openable"] != true {
		return nil, false, nil
	}
	return providerAcceptanceSlotResponse(slot, workspace, compute, storage, attachment), true, nil
}

func (app *controlPlaneServer) providerAcceptanceSlotSummary(ctx context.Context, slot providerAcceptanceSlot) (map[string]any, error) {
	workspace, workspaceConflict, err := app.providerAcceptanceWorkspaceExact(ctx, slot)
	if err != nil {
		return nil, err
	}
	compute, _, computeConflict, err := app.providerAcceptanceComputeExact(ctx, slot)
	if err != nil {
		return nil, err
	}
	storage, _, storageConflict, err := app.providerAcceptanceStorageExact(ctx, slot)
	if err != nil {
		return nil, err
	}
	attachment, _, err := app.providerAcceptanceAttachment(ctx, slot)
	if err != nil {
		return nil, err
	}
	if workspaceConflict || computeConflict || storageConflict {
		return nil, errProviderAcceptanceStateRead
	}
	return providerAcceptanceSlotResponse(slot, workspace, compute, storage, attachment), nil
}

func providerAcceptanceSlotResponse(slot providerAcceptanceSlot, workspace, compute, storage, attachment map[string]any) map[string]any {
	return map[string]any{
		"id": slot.ID, "accountId": slot.AccountID, "workspaceId": stringValue(workspace["id"]), "workspaceUrl": stringValue(workspace["url"]),
		"computeAllocationId": stringValue(compute["id"]), "computeProviderId": firstNonEmpty(stringValue(compute["cvmInstanceId"]), stringValue(compute["instanceId"])), "nodePoolId": stringValue(compute["nodePoolId"]),
		"storageId": stringValue(storage["id"]), "storageProviderId": stringValue(storage["providerResourceId"]), "persistentVolumeId": firstNonEmpty(stringValue(storage["pvName"]), stringValue(storage["persistentVolumeName"]), providerDataValue(storage, "pvName")),
		"attachmentId": stringValue(attachment["id"]),
	}
}

func providerAcceptanceResponse(status, reason string, slot map[string]any) map[string]any {
	response := map[string]any{"ok": status == "ready" || status == "reused", "status": status, "slot": slot}
	if reason != "" {
		response["reason"] = reason
	}
	return response
}

func (app *controlPlaneServer) writeProviderAcceptanceManualReview(w http.ResponseWriter, r *http.Request, operation map[string]any, slot providerAcceptanceSlot, reason string) {
	summary, readErr := app.providerAcceptanceSlotSummary(r.Context(), slot)
	if readErr != nil {
		summary = providerAcceptanceSlotResponse(slot, nil, nil, nil, nil)
	}
	response := providerAcceptanceResponse("manual_review", reason, summary)
	operation = mergeMaps(operation, map[string]any{"status": "manual_review", "errorCode": reason, "result": string(mustJSON(response))})
	if err := app.tables.SaveRuntimeOperation(r.Context(), operation); err != nil {
		writeError(w, http.StatusInternalServerError, "state_persist_failed")
		return
	}
	if readErr != nil {
		writeError(w, http.StatusInternalServerError, "state_read_failed")
		return
	}
	writeJSON(w, http.StatusOK, response)
}
