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
	ID          string
	AccountID   string
	OwnerEmail  string
	Key         string
	PackageID   string
	StorageGB   int
	OperationID string
}

var providerAcceptanceSlots = map[string]providerAcceptanceSlot{
	"verification-slot-basic-01": {
		ID: "verification-slot-basic-01", AccountID: "acct-verification-slot-basic-01", OwnerEmail: "verification-slot-basic-01@fenggaolab.org",
		Key: "provider-acceptance:verification-slot-basic-01", PackageID: "basic", StorageGB: 10,
		OperationID: "provider-acceptance-verification-slot-basic-01",
	},
	"verification-slot-pro-01": {
		ID: "verification-slot-pro-01", AccountID: "acct-verification-slot-pro-01", OwnerEmail: "verification-slot-pro-01@fenggaolab.org",
		Key: "provider-acceptance:verification-slot-pro-01", PackageID: "pro", StorageGB: 100,
		OperationID: "provider-acceptance-verification-slot-pro-01",
	},
}

func providerAcceptanceAttachmentRow(row map[string]any, input map[string]any) map[string]any {
	if row == nil {
		row = map[string]any{}
	}
	row["computeAllocationId"] = firstNonEmpty(stringValue(row["computeAllocationId"]), stringValue(row["computeId"]), stringField(input, "computeAllocationId", ""))
	row["storageId"] = firstNonEmpty(stringValue(row["storageId"]), stringValue(row["volumeId"]), stringField(input, "storageId", ""))
	row["mountPath"] = firstNonEmpty(stringValue(row["mountPath"]), stringField(input, "mountPath", "/data"))
	row["provider"] = "fabric"
	return row
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
		attachment, attachmentCount, err := app.providerAcceptanceAttachment(r.Context(), slot)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		identitiesValid := providerAcceptanceResourceInventoryValid(computes, slot, providerAcceptanceComputeID(slot), ownerID) &&
			providerAcceptanceResourceInventoryValid(storages, slot, providerAcceptanceStorageID(slot), ownerID)
		workspaceIdentityValid := workspace == nil || providerAcceptanceWorkspaceCandidateValid(workspace, slot, ownerID)
		attachmentInventoryValid := attachmentCount == 0 || (attachmentCount == 1 && providerAcceptanceAttachmentIdentityValid(attachment, slot))
		emptyInventory := workspace == nil && len(computes) == 0 && len(storages) == 0 && attachmentCount == 0
		completeInventory := providerAcceptanceWorkspaceCandidateValid(workspace, slot, ownerID) && len(computes) == 1 && len(storages) == 1 &&
			providerAcceptanceComputeIdentityValid(computes[0], slot, ownerID) && providerAcceptanceStorageIdentityValid(storages[0], slot, ownerID) &&
			attachmentCount == 1 && providerAcceptanceAttachmentIdentityValid(attachment, slot)
		invalidOperation := operationExists && !providerAcceptanceOperationValid(operation, slot)
		unclaimedAmbiguousInventory := !operationExists && !emptyInventory && !completeInventory
		if !workspaceIdentityValid || !identitiesValid || !attachmentInventoryValid || invalidOperation || unclaimedAmbiguousInventory {
			writeError(w, http.StatusConflict, "provider_acceptance_inventory_ambiguous")
			return
		}
		providerFacts, err := providerAcceptanceReadFacts(r.Context(), service, slot, workspace, computes, storages, attachment)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		if operationExists && stringValue(operation["status"]) == "manual_review" {
			summary, err := app.providerAcceptanceSlotSummary(r.Context(), slot, providerFacts)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "state_read_failed")
				return
			}
			writeJSON(w, http.StatusOK, providerAcceptanceResponse("manual_review", stringValue(operation["errorCode"]), summary))
			return
		}
		summary, ready, err := app.providerAcceptanceReadySlot(r.Context(), slot, ownerID, providerFacts, time.Now().UTC())
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
			app.writeProviderAcceptanceManualReview(w, r, operation, slot, providerFacts, "provider_acceptance_state_ambiguous")
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

		status, reason, err := app.advanceProviderAcceptance(r.Context(), service, slot, ownerID, sub2APIUserID, computePreflight, storagePreflight, providerFacts)
		if err != nil {
			if errors.Is(err, errProviderAcceptanceStateRead) {
				writeError(w, http.StatusInternalServerError, "state_read_failed")
			} else {
				writeError(w, http.StatusInternalServerError, "state_persist_failed")
			}
			return
		}
		if reason != "" {
			app.writeProviderAcceptanceManualReview(w, r, operation, slot, providerFacts, reason)
			return
		}
		summary, err = app.providerAcceptanceSlotSummary(r.Context(), slot, providerFacts)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		if status == "ready" {
			operation["status"] = "succeeded"
			operation["result"] = string(mustJSON(providerAcceptanceResponse("ready", "", summary)))
			if err := app.appendAuditEvent(r, "operator.provider_acceptance", "verification_slot", slot.ID, slot.AccountID, nil, summary, "succeeded"); err != nil {
				app.writeProviderAcceptanceManualReview(w, r, operation, slot, providerFacts, "provider_acceptance_audit_failed")
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
	account, found, err := app.tables.GetAccount(ctx, slot.AccountID)
	if err != nil || !found || stringValue(account["id"]) != slot.AccountID || stringValue(account["status"]) != "active" {
		return "", 0, "provider_acceptance_account_required"
	}
	sub2APIUserID, err := app.sub2APIUserID(ctx, slot.AccountID)
	if err != nil {
		return "", 0, "provider_acceptance_account_mapping_required"
	}
	owner, found, err := app.tables.GetUser(ctx, stringValue(account["ownerUserId"]))
	if err != nil || !found {
		return "", 0, "provider_acceptance_owner_required"
	}
	ownerID := stringValue(owner["id"])
	if stringValue(owner["accountId"]) != slot.AccountID || stringValue(owner["role"]) != "owner" || stringValue(owner["status"]) != "active" || !strings.EqualFold(stringValue(owner["email"]), slot.OwnerEmail) {
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
	packageID, computeID := stringValue(row["packageId"]), stringValue(row["computeAllocationId"])
	return row != nil && stringValue(row["id"]) == primaryWorkspaceID(slot.AccountID) && stringValue(row["accountId"]) == slot.AccountID &&
		stringValue(row["ownerAccountId"]) == slot.AccountID && stringValue(row["ownerUserId"]) == ownerID && stringValue(row["name"]) == slot.ID &&
		(packageID == "" || packageID == slot.PackageID) && stringValue(row["verificationSlotId"]) == slot.ID && row["customerProduct"] == false &&
		(computeID == "" || computeID == providerAcceptanceComputeID(slot)) && stringValue(row["currentComputeAllocationId"]) == providerAcceptanceComputeID(slot) &&
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
		"name": slot.ID, "packageId": slot.PackageID, "provider": "fabric", "state": "provisioning", "status": "provisioning",
		"computeAllocationId": providerAcceptanceComputeID(slot), "currentComputeAllocationId": providerAcceptanceComputeID(slot), "storageId": providerAcceptanceStorageID(slot),
		"verificationSlotId": slot.ID, "customerProduct": false,
	}
}

func providerAcceptanceOperationRow(status string, slot providerAcceptanceSlot) map[string]any {
	workspaceID := primaryWorkspaceID(slot.AccountID)
	return map[string]any{
		"id": slot.OperationID, "operationId": slot.OperationID, "accountId": slot.AccountID, "workspaceId": workspaceID,
		"resourceId": slot.ID, "resourceKind": "verification_slot", "action": "provider_acceptance", "provider": "fabric",
		"status": status, "result": "{}", "computeAllocationId": providerAcceptanceComputeID(slot), "storageId": providerAcceptanceStorageID(slot),
	}
}

func (app *controlPlaneServer) providerAcceptanceOperation(ctx context.Context, slot providerAcceptanceSlot) (map[string]any, bool, error) {
	operation, found, err := app.tables.GetRuntimeOperation(ctx, slot.OperationID)
	if err != nil {
		return nil, false, err
	}
	return operation, found, nil
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
	zone := controlplane.ProviderAcceptanceLaunchZone()
	if zone == "" {
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

func (app *controlPlaneServer) providerAcceptanceAttachmentCandidates(ctx context.Context, slot providerAcceptanceSlot) ([]map[string]any, error) {
	rows, err := app.tables.ListAttachments(ctx, "")
	if err != nil {
		return nil, errProviderAcceptanceStateRead
	}
	return providerAcceptanceCandidates(rows, map[string]string{
		"accountId": slot.AccountID, "workspaceId": primaryWorkspaceID(slot.AccountID), "computeAllocationId": providerAcceptanceComputeID(slot),
		"storageId": providerAcceptanceStorageID(slot), "verificationSlotId": slot.ID,
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

func (app *controlPlaneServer) advanceProviderAcceptance(ctx context.Context, service *controlplane.Service, slot providerAcceptanceSlot, ownerID string, sub2APIUserID int64, computePreflight, storagePreflight clients.MonthlyPreflight, facts map[string]clients.ProviderFact) (string, string, error) {
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
		compute = providerAcceptanceComputeRow(map[string]any{
			"id": created.ID, "accountId": created.AccountID, "workspaceId": created.WorkspaceID, "packageId": created.PackageID,
			"status": created.Status, "providerRequestId": created.ProviderRequestID,
		}, slot, ownerID)
		if err := app.saveComputeFact(compute); err != nil {
			return "", "", err
		}
		if createErr != nil {
			return "", "provider_acceptance_compute_result_unknown", nil
		}
		return "in_progress", "", nil
	}
	if !providerAcceptanceComputeIdentityValid(compute, slot, ownerID) {
		return "", "provider_acceptance_compute_state_ambiguous", nil
	}
	computeInput := providerAcceptanceFactInput(slot, "compute", computeID)
	computeFact := facts[providerFactKey(computeInput)]
	if !providerAcceptanceFactReady(computeInput, computeFact, time.Now().UTC()) {
		if providerFactConfirmedAbsent(computeInput, computeFact) {
			return "", "provider_acceptance_compute_state_ambiguous", nil
		}
		return "in_progress", "", nil
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
		storage = providerAcceptanceStorageRow(map[string]any{
			"id": created.ID, "accountId": created.AccountID, "workspaceId": created.WorkspaceID, "status": created.Status,
			"providerRequestId": created.ProviderRequestID, "sizeGb": created.SizeGB,
		}, slot, ownerID)
		if err := app.saveStorageFact(storage); err != nil {
			return "", "", err
		}
		if createErr != nil {
			return "", "provider_acceptance_storage_result_unknown", nil
		}
		return "in_progress", "", nil
	}
	if !providerAcceptanceStorageIdentityValid(storage, slot, ownerID) {
		return "", "provider_acceptance_storage_state_ambiguous", nil
	}
	storageInput := providerAcceptanceFactInput(slot, "storage", storageID)
	storageFact := facts[providerFactKey(storageInput)]
	if !providerAcceptanceFactReady(storageInput, storageFact, time.Now().UTC()) {
		if providerFactConfirmedAbsent(storageInput, storageFact) {
			return "", "provider_acceptance_storage_state_ambiguous", nil
		}
		return "in_progress", "", nil
	}

	attachment, attachmentCount, err := app.providerAcceptanceAttachment(ctx, slot)
	if err != nil {
		return "", "", err
	}
	if attachmentCount > 1 || (attachmentCount == 1 && !providerAcceptanceAttachmentIdentityValid(attachment, slot)) {
		return "", "provider_acceptance_attachment_state_ambiguous", nil
	}
	if attachmentCount == 0 {
		created, createErr := service.CreateStorageAttachment(ctx, controlplane.StorageAttachmentInput{WorkspaceID: workspaceID, ComputeID: computeID, VolumeID: storageID}, slot.Key+":attachment")
		attachment = providerAcceptanceAttachmentRow(structToMap(created), map[string]any{"computeAllocationId": computeID, "storageId": storageID, "mountPath": "/data"})
		attachment["accountId"], attachment["ownerAccountId"] = slot.AccountID, slot.AccountID
		if err := app.saveAttachmentFact(attachment, attachment); err != nil {
			return "", "", err
		}
		if createErr != nil {
			return "", "provider_acceptance_attachment_result_unknown", nil
		}
		return "in_progress", "", nil
	}
	attachmentInput := providerAcceptanceFactInput(slot, "attachment", stringValue(attachment["id"]))
	attachmentFact := facts[providerFactKey(attachmentInput)]
	if !providerAcceptanceFactReady(attachmentInput, attachmentFact, time.Now().UTC()) {
		if providerFactConfirmedAbsent(attachmentInput, attachmentFact) {
			return "", "provider_acceptance_attachment_state_ambiguous", nil
		}
		return "in_progress", "", nil
	}
	if !providerAcceptanceAttachmentIdentityValid(attachment, slot) {
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
		created, prepareErr := service.PrepareProviderAcceptanceRuntime(ctx, controlplane.CreateWorkspaceInput{
			WorkspaceID: workspaceID, AccountID: slot.AccountID, Sub2APIUserID: sub2APIUserID, OwnerID: ownerID,
			Name: slot.ID, PackageID: slot.PackageID, AttachmentID: stringValue(attachment["id"]),
			AttachmentOperationID: slot.Key + ":attachment", RuntimeOperationID: slot.Key + ":workspace:runtime",
			ComputeID: computeID, VolumeID: storageID,
		}, slot.Key+":workspace")
		if prepareErr != nil {
			return "", "provider_acceptance_runtime_result_unknown", nil
		}
		projection = providerAcceptanceCreatedWorkspaceProjection(workspace, created, slot)
		workspace = providerAcceptanceWorkspaceRow(projection, slot)
		if err := app.tables.SaveWorkspace(ctx, workspace); err != nil {
			return "", "", err
		}
		return "in_progress", "", nil
	} else {
		runtimeInput := providerAcceptanceFactInput(slot, "runtime", stringValue(workspace["runtimeId"]))
		runtimeFact := facts[providerFactKey(runtimeInput)]
		if !providerAcceptanceFactReady(runtimeInput, runtimeFact, time.Now().UTC()) {
			if providerFactConfirmedAbsent(runtimeInput, runtimeFact) {
				return "", "provider_acceptance_workspace_state_ambiguous", nil
			}
			return "in_progress", "", nil
		}
		projection = providerAcceptanceWorkspaceProjection(workspace, runtimeFact, slot)
	}
	workspace = providerAcceptanceWorkspaceRow(projection, slot)
	if err := app.tables.SaveWorkspace(ctx, workspace); err != nil {
		return "", "", err
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
	if _, ready, err := app.providerAcceptanceReadySlot(ctx, slot, ownerID, facts, time.Now().UTC()); err != nil {
		return "", "", err
	} else if !ready {
		return "", "provider_acceptance_state_ambiguous", nil
	}
	return "ready", "", nil
}

func providerAcceptanceComputeRow(row map[string]any, slot providerAcceptanceSlot, ownerID string) map[string]any {
	if row == nil {
		row = map[string]any{}
	}
	row["id"], row["accountId"], row["ownerAccountId"], row["ownerUserId"] = providerAcceptanceComputeID(slot), slot.AccountID, slot.AccountID, ownerID
	row["workspaceId"], row["packageId"], row["name"] = primaryWorkspaceID(slot.AccountID), slot.PackageID, slot.ID
	row["verificationSlotId"], row["customerProduct"] = slot.ID, false
	row["billingOperationId"], row["monthlyPriceCnyCents"], row["chargeUsdMicros"] = slot.OperationID, int64(0), int64(0)
	row["provider"] = "fabric"
	return row
}

func providerAcceptanceStorageRow(row map[string]any, slot providerAcceptanceSlot, ownerID string) map[string]any {
	if row == nil {
		row = map[string]any{}
	}
	row["id"], row["accountId"], row["ownerAccountId"], row["ownerUserId"] = providerAcceptanceStorageID(slot), slot.AccountID, slot.AccountID, ownerID
	row["workspaceId"], row["packageId"], row["name"] = primaryWorkspaceID(slot.AccountID), slot.PackageID, slot.ID
	row["computeAllocationId"], row["sizeGb"] = providerAcceptanceComputeID(slot), slot.StorageGB
	row["verificationSlotId"], row["customerProduct"] = slot.ID, false
	row["billingOperationId"], row["monthlyPriceCnyCents"], row["chargeUsdMicros"] = slot.OperationID, int64(0), int64(0)
	row["provider"] = "fabric"
	return row
}

func providerAcceptanceComputeIdentityValid(row map[string]any, slot providerAcceptanceSlot, ownerID string) bool {
	return row != nil && stringValue(row["id"]) == providerAcceptanceComputeID(slot) && stringValue(row["accountId"]) == slot.AccountID &&
		stringValue(row["ownerUserId"]) == ownerID && stringValue(row["workspaceId"]) == primaryWorkspaceID(slot.AccountID) &&
		stringValue(row["packageId"]) == slot.PackageID && stringValue(row["verificationSlotId"]) == slot.ID && row["customerProduct"] == false
}

func providerAcceptanceStorageIdentityValid(row map[string]any, slot providerAcceptanceSlot, ownerID string) bool {
	return row != nil && stringValue(row["id"]) == providerAcceptanceStorageID(slot) && stringValue(row["accountId"]) == slot.AccountID &&
		stringValue(row["ownerUserId"]) == ownerID && stringValue(row["workspaceId"]) == primaryWorkspaceID(slot.AccountID) &&
		stringValue(row["packageId"]) == slot.PackageID && numberField(row, "sizeGb", 0) == float64(slot.StorageGB) &&
		stringValue(row["computeAllocationId"]) == providerAcceptanceComputeID(slot) && stringValue(row["verificationSlotId"]) == slot.ID && row["customerProduct"] == false
}

func providerAcceptanceFactInput(slot providerAcceptanceSlot, resourceType, resourceID string) clients.ProviderFactInput {
	return clients.ProviderFactInput{
		AccountID: slot.AccountID, WorkspaceID: primaryWorkspaceID(slot.AccountID), ResourceType: resourceType, ResourceID: resourceID,
	}
}

func providerAcceptanceReadFacts(ctx context.Context, service *controlplane.Service, slot providerAcceptanceSlot, workspace map[string]any, computes, storages []map[string]any, attachment map[string]any) (map[string]clients.ProviderFact, error) {
	inputs := make([]clients.ProviderFactInput, 0, 4)
	if len(computes) == 1 && stringValue(computes[0]["id"]) == providerAcceptanceComputeID(slot) {
		inputs = append(inputs, providerAcceptanceFactInput(slot, "compute", providerAcceptanceComputeID(slot)))
	}
	if len(storages) == 1 && stringValue(storages[0]["id"]) == providerAcceptanceStorageID(slot) {
		inputs = append(inputs, providerAcceptanceFactInput(slot, "storage", providerAcceptanceStorageID(slot)))
	}
	if attachmentID := stringValue(attachment["id"]); attachmentID != "" {
		inputs = append(inputs, providerAcceptanceFactInput(slot, "attachment", attachmentID))
	}
	if runtimeID := stringValue(workspace["runtimeId"]); runtimeID != "" {
		inputs = append(inputs, providerAcceptanceFactInput(slot, "runtime", runtimeID))
	}
	if len(inputs) == 0 {
		return map[string]clients.ProviderFact{}, nil
	}
	facts, err := readProviderFacts(ctx, service, inputs)
	if err != nil {
		return nil, errProviderAcceptanceStateRead
	}
	return facts, nil
}

func providerAcceptanceFactReady(input clients.ProviderFactInput, fact clients.ProviderFact, now time.Time) bool {
	if providerFactResultKey(fact) != providerFactKey(input) || !fact.Available || strings.TrimSpace(fact.ErrorCode) != "" ||
		strings.TrimSpace(fact.Facts.ProviderID) == "" || strings.TrimSpace(fact.Facts.LastReadAt) == "" ||
		!providerAcceptanceFactStatusReady(input.ResourceType, fact.Facts.Status) {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, fact.Facts.LastReadAt); err != nil {
		return false
	}
	if input.ResourceType == "attachment" || input.ResourceType == "runtime" {
		return true
	}
	if strings.TrimSpace(fact.Facts.PackageOrSpec) == "" || strings.TrimSpace(fact.Facts.Zone) == "" {
		return false
	}
	expiresAt, err := time.Parse(time.RFC3339, fact.Facts.ExpiresAt)
	return err == nil && expiresAt.UTC().After(now.UTC())
}

func providerAcceptanceFactStatusReady(resourceType, status string) bool {
	switch resourceType {
	case "compute":
		return strings.EqualFold(status, "running") || strings.EqualFold(status, "ready")
	case "storage":
		return strings.EqualFold(status, "ready") || strings.EqualFold(status, "available") || strings.EqualFold(status, "attached") || strings.EqualFold(status, "unattached")
	case "attachment":
		return strings.EqualFold(status, "attached") || strings.EqualFold(status, "ready")
	case "runtime":
		return strings.EqualFold(status, "running") || strings.EqualFold(status, "ready")
	default:
		return false
	}
}

func (app *controlPlaneServer) providerAcceptanceAttachment(ctx context.Context, slot providerAcceptanceSlot) (map[string]any, int, error) {
	attachments, err := app.providerAcceptanceAttachmentCandidates(ctx, slot)
	if err != nil {
		return nil, 0, errProviderAcceptanceStateRead
	}
	if len(attachments) == 0 {
		return nil, 0, nil
	}
	return attachments[len(attachments)-1], len(attachments), nil
}

func providerAcceptanceAttachmentIdentityValid(attachment map[string]any, slot providerAcceptanceSlot) bool {
	return attachment != nil && stringValue(attachment["accountId"]) == slot.AccountID && stringValue(attachment["workspaceId"]) == primaryWorkspaceID(slot.AccountID) &&
		stringValue(attachment["computeAllocationId"]) == providerAcceptanceComputeID(slot) && stringValue(attachment["storageId"]) == providerAcceptanceStorageID(slot)
}

func providerAcceptanceWorkspaceProjection(workspace map[string]any, runtimeFact clients.ProviderFact, slot providerAcceptanceSlot) domain.WorkspaceProjection {
	access := mapField(workspace, "access")
	return domain.WorkspaceProjection{
		ID: stringValue(workspace["id"]), AccountID: slot.AccountID, OwnerID: stringValue(workspace["ownerUserId"]), Name: slot.ID,
		PackageID: slot.PackageID, Provider: "fabric", URL: stringValue(workspace["url"]), Status: "running",
		ComputeID: providerAcceptanceComputeID(slot), VolumeID: providerAcceptanceStorageID(slot), AttachmentID: firstNonEmpty(stringValue(workspace["attachmentId"]), stringValue(workspace["currentAttachmentId"])),
		RuntimeID: stringValue(workspace["runtimeId"]), RuntimeServiceName: runtimeFact.Facts.ProviderID, RuntimeReady: true,
		RuntimeUsername: stringValue(access["username"]), CredentialStatus: stringValue(access["credentialStatus"]),
		CredentialVersion: stringValue(access["credentialVersion"]), CredentialSecretRef: stringValue(access["secretRef"]), ReceiptID: stringValue(workspace["receiptId"]),
	}
}

func providerAcceptanceCreatedWorkspaceProjection(workspace map[string]any, runtime clients.WorkspaceRuntime, slot providerAcceptanceSlot) domain.WorkspaceProjection {
	access := mapField(workspace, "access")
	return domain.WorkspaceProjection{
		ID: stringValue(workspace["id"]), AccountID: slot.AccountID, OwnerID: stringValue(workspace["ownerUserId"]), Name: slot.ID,
		PackageID: slot.PackageID, Provider: "fabric", URL: runtime.URL, Status: "provisioning",
		ComputeID: providerAcceptanceComputeID(slot), VolumeID: providerAcceptanceStorageID(slot), AttachmentID: firstNonEmpty(stringValue(workspace["attachmentId"]), stringValue(workspace["currentAttachmentId"])),
		RuntimeID: runtime.ID, RuntimeServiceName: runtime.ServiceName,
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

func (app *controlPlaneServer) providerAcceptanceReadySlot(ctx context.Context, slot providerAcceptanceSlot, ownerID string, facts map[string]clients.ProviderFact, now time.Time) (map[string]any, bool, error) {
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
	if workspaceConflict || computeConflict || storageConflict || attachmentCount > 1 || (attachmentCount == 1 && !providerAcceptanceAttachmentIdentityValid(attachment, slot)) {
		return nil, false, errProviderAcceptanceStateRead
	}
	computeInput := providerAcceptanceFactInput(slot, "compute", providerAcceptanceComputeID(slot))
	storageInput := providerAcceptanceFactInput(slot, "storage", providerAcceptanceStorageID(slot))
	attachmentInput := providerAcceptanceFactInput(slot, "attachment", stringValue(attachment["id"]))
	runtimeInput := providerAcceptanceFactInput(slot, "runtime", stringValue(workspace["runtimeId"]))
	if !providerAcceptanceWorkspaceCandidateValid(workspace, slot, ownerID) || stringValue(workspace["url"]) == "" || stringValue(workspace["receiptId"]) == "" ||
		!computeOK || !storageOK || attachmentCount != 1 || !providerAcceptanceComputeIdentityValid(compute, slot, ownerID) || !providerAcceptanceStorageIdentityValid(storage, slot, ownerID) ||
		!providerAcceptanceAttachmentIdentityValid(attachment, slot) || !providerAcceptanceFactReady(computeInput, facts[providerFactKey(computeInput)], now) ||
		!providerAcceptanceFactReady(storageInput, facts[providerFactKey(storageInput)], now) || !providerAcceptanceFactReady(attachmentInput, facts[providerFactKey(attachmentInput)], now) ||
		!providerAcceptanceFactReady(runtimeInput, facts[providerFactKey(runtimeInput)], now) {
		return nil, false, nil
	}
	return providerAcceptanceSlotResponse(slot, workspace, compute, storage, attachment, facts), true, nil
}

func (app *controlPlaneServer) providerAcceptanceSlotSummary(ctx context.Context, slot providerAcceptanceSlot, facts map[string]clients.ProviderFact) (map[string]any, error) {
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
	attachment, attachmentCount, err := app.providerAcceptanceAttachment(ctx, slot)
	if err != nil {
		return nil, err
	}
	if workspaceConflict || computeConflict || storageConflict || attachmentCount > 1 || (attachmentCount == 1 && !providerAcceptanceAttachmentIdentityValid(attachment, slot)) {
		return nil, errProviderAcceptanceStateRead
	}
	return providerAcceptanceSlotResponse(slot, workspace, compute, storage, attachment, facts), nil
}

func providerAcceptanceSlotResponse(slot providerAcceptanceSlot, workspace, compute, storage, attachment map[string]any, facts map[string]clients.ProviderFact) map[string]any {
	computeInput := providerAcceptanceFactInput(slot, "compute", providerAcceptanceComputeID(slot))
	storageInput := providerAcceptanceFactInput(slot, "storage", providerAcceptanceStorageID(slot))
	computeFact, storageFact := facts[providerFactKey(computeInput)], facts[providerFactKey(storageInput)]
	return map[string]any{
		"id": slot.ID, "accountId": slot.AccountID, "workspaceId": stringValue(workspace["id"]), "workspaceUrl": stringValue(workspace["url"]),
		"computeAllocationId": stringValue(compute["id"]), "computeProviderId": computeFact.Facts.ProviderID,
		// Deprecated response-only compatibility projection for tools/provider-acceptance.ts.
		// These persisted fields never participate in ProviderFactsBatch validation.
		"nodePoolId": stringValue(compute["nodePoolId"]),
		"storageId":  stringValue(storage["id"]), "storageProviderId": storageFact.Facts.ProviderID,
		"persistentVolumeId": stringValue(storage["persistentVolumeName"]),
		"attachmentId":       stringValue(attachment["id"]),
	}
}

func providerAcceptanceResponse(status, reason string, slot map[string]any) map[string]any {
	response := map[string]any{"ok": status == "ready" || status == "reused", "status": status, "slot": slot}
	if reason != "" {
		response["reason"] = reason
	}
	return response
}

func (app *controlPlaneServer) writeProviderAcceptanceManualReview(w http.ResponseWriter, r *http.Request, operation map[string]any, slot providerAcceptanceSlot, facts map[string]clients.ProviderFact, reason string) {
	summary, readErr := app.providerAcceptanceSlotSummary(r.Context(), slot, facts)
	if readErr != nil {
		summary = providerAcceptanceSlotResponse(slot, nil, nil, nil, nil, facts)
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
