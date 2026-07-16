package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
	"opl-cloud/services/control-plane/internal/domain"
)

const (
	providerAcceptanceSlotID       = "verification-slot-01"
	providerAcceptanceAccountID    = "acct-verification-slot-01"
	providerAcceptanceOwnerEmail   = "verification-slot-01@fenggaolab.org"
	providerAcceptanceKey          = "provider-acceptance:" + providerAcceptanceSlotID
	providerAcceptanceConfirmation = "I_UNDERSTAND_THIS_BUYS_ONE_PREPAID_CVM_AND_CBS"
	providerAcceptanceInstanceType = "SA5.MEDIUM4"
	providerAcceptancePackageID    = "basic"
	providerAcceptanceStorageGB    = 10
	providerAcceptanceOperationID  = "provider-acceptance-verification-slot-01"
)

func registerProviderAcceptanceRoutes(mux *http.ServeMux, app *controlPlaneServer, service *controlplane.Service) {
	mux.HandleFunc("POST /api/operator/provider-acceptance", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		key, ok := requiredMutationKey(w, r)
		if !ok {
			return
		}
		if stringField(input, "confirmation", "") != providerAcceptanceConfirmation || stringField(input, "slotId", "") != providerAcceptanceSlotID {
			writeError(w, http.StatusBadRequest, "provider_acceptance_confirmation_required")
			return
		}
		if stringField(input, "accountId", "") != providerAcceptanceAccountID {
			writeError(w, http.StatusBadRequest, "provider_acceptance_account_fixed")
			return
		}
		if key != providerAcceptanceKey {
			writeError(w, http.StatusConflict, "provider_acceptance_idempotency_key_fixed")
			return
		}

		unlock := app.lockResource("provider-acceptance", providerAcceptanceSlotID)
		defer unlock()

		ownerID, sub2APIUserID, code := app.providerAcceptanceIdentity(r.Context())
		if code != "" {
			writeError(w, http.StatusConflict, code)
			return
		}
		workspaces, err := app.tables.ListWorkspaces(r.Context(), providerAcceptanceAccountID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		workspace, conflict := providerAcceptanceWorkspace(workspaces)
		if conflict {
			writeError(w, http.StatusConflict, errPrimaryWorkspaceExists.Error())
			return
		}

		operation, operationExists, err := app.providerAcceptanceOperation(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		if operationExists && stringValue(operation["status"]) == "manual_review" {
			writeJSON(w, http.StatusOK, providerAcceptanceResponse("manual_review", stringValue(operation["errorCode"]), app.providerAcceptanceSlotSummary()))
			return
		}
		if slot, ready := app.providerAcceptanceReadySlot(time.Now().UTC()); ready {
			if !operationExists {
				operation = providerAcceptanceOperationRow("succeeded")
			}
			if !operationExists || stringValue(operation["status"]) == "started" {
				operation["status"] = "succeeded"
				delete(operation, "errorCode")
				operation["result"] = string(mustJSON(providerAcceptanceResponse("reused", "", slot)))
				if err := app.tables.SaveRuntimeOperation(r.Context(), operation); err != nil {
					writeError(w, http.StatusInternalServerError, "state_persist_failed")
					return
				}
			}
			writeJSON(w, http.StatusOK, providerAcceptanceResponse("reused", "", slot))
			return
		}
		if operationExists && stringValue(operation["status"]) == "succeeded" {
			app.writeProviderAcceptanceManualReview(w, r, operation, "provider_acceptance_state_ambiguous")
			return
		}

		workspaceKey, err := service.Sub2APIWorkspaceKey(r.Context(), sub2APIUserID)
		if err != nil || workspaceKey.UserID != sub2APIUserID || workspaceKey.Name != "opl-workspace" || workspaceKey.Status != "active" || workspaceKey.Key == "" {
			writeError(w, http.StatusConflict, "provider_acceptance_gateway_key_required")
			return
		}
		computePreflight, storagePreflight, ok := providerAcceptancePreflight(r.Context(), service)
		if !ok {
			writeError(w, http.StatusConflict, "provider_acceptance_preflight_failed")
			return
		}

		if !operationExists {
			operation = providerAcceptanceOperationRow("started")
			if workspace == nil {
				workspace = providerAcceptanceWorkspaceClaim(ownerID)
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

		status, reason, err := app.advanceProviderAcceptance(r.Context(), service, ownerID, sub2APIUserID, computePreflight, storagePreflight)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if reason != "" {
			app.writeProviderAcceptanceManualReview(w, r, operation, reason)
			return
		}
		slot := app.providerAcceptanceSlotSummary()
		if status == "ready" {
			operation["status"] = "succeeded"
			operation["result"] = string(mustJSON(providerAcceptanceResponse("ready", "", slot)))
			if err := app.appendAuditEvent(r, "operator.provider_acceptance", "verification_slot", providerAcceptanceSlotID, providerAcceptanceAccountID, nil, slot, "succeeded"); err != nil {
				app.writeProviderAcceptanceManualReview(w, r, operation, "provider_acceptance_audit_failed")
				return
			}
			if err := app.tables.SaveRuntimeOperation(r.Context(), operation); err != nil {
				writeError(w, http.StatusInternalServerError, "state_persist_failed")
				return
			}
		}
		writeJSON(w, http.StatusOK, providerAcceptanceResponse(status, "", slot))
	}))
}

func (app *controlPlaneServer) providerAcceptanceIdentity(ctx context.Context) (string, int64, string) {
	accounts, err := app.tables.ListAccounts(ctx, providerAcceptanceAccountID)
	if err != nil || len(accounts) != 1 || stringValue(accounts[0]["id"]) != providerAcceptanceAccountID || stringValue(accounts[0]["status"]) != "active" {
		return "", 0, "provider_acceptance_account_required"
	}
	sub2APIUserID, err := app.sub2APIUserID(ctx, providerAcceptanceAccountID)
	if err != nil {
		return "", 0, "provider_acceptance_account_mapping_required"
	}
	users, err := app.tables.ListUsers(ctx, false)
	if err != nil {
		return "", 0, "provider_acceptance_owner_required"
	}
	ownerID := ""
	for _, user := range users {
		if stringValue(user["accountId"]) != providerAcceptanceAccountID || stringValue(user["role"]) != "owner" || stringValue(user["status"]) != "active" || !strings.EqualFold(stringValue(user["email"]), providerAcceptanceOwnerEmail) {
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

func providerAcceptanceWorkspace(rows []map[string]any) (map[string]any, bool) {
	if len(rows) == 0 {
		return nil, false
	}
	if len(rows) != 1 {
		return nil, true
	}
	row := rows[0]
	if stringValue(row["id"]) != primaryWorkspaceID(providerAcceptanceAccountID) || stringValue(row["verificationSlotId"]) != providerAcceptanceSlotID || row["customerProduct"] != false {
		return nil, true
	}
	return row, false
}

func providerAcceptanceWorkspaceClaim(ownerID string) map[string]any {
	workspaceID := primaryWorkspaceID(providerAcceptanceAccountID)
	return map[string]any{
		"id": workspaceID, "accountId": providerAcceptanceAccountID, "ownerAccountId": providerAcceptanceAccountID, "ownerUserId": ownerID,
		"name": providerAcceptanceSlotID, "packageId": providerAcceptancePackageID, "provider": "tencent-tke", "state": "provisioning", "status": "provisioning",
		"computeAllocationId": providerAcceptanceComputeID(), "currentComputeAllocationId": providerAcceptanceComputeID(), "storageId": providerAcceptanceStorageID(),
		"verificationSlotId": providerAcceptanceSlotID, "customerProduct": false,
	}
}

func providerAcceptanceOperationRow(status string) map[string]any {
	workspaceID := primaryWorkspaceID(providerAcceptanceAccountID)
	return map[string]any{
		"id": providerAcceptanceOperationID, "operationId": providerAcceptanceOperationID, "accountId": providerAcceptanceAccountID, "workspaceId": workspaceID,
		"resourceId": providerAcceptanceSlotID, "resourceKind": "verification_slot", "action": "provider_acceptance", "provider": "tencent-tke",
		"status": status, "result": "{}", "computeAllocationId": providerAcceptanceComputeID(), "storageId": providerAcceptanceStorageID(),
	}
}

func (app *controlPlaneServer) providerAcceptanceOperation(ctx context.Context) (map[string]any, bool, error) {
	operations, err := app.tables.ListRuntimeOperations(ctx)
	if err != nil {
		return nil, false, err
	}
	for _, operation := range operations {
		if stringValue(operation["id"]) == providerAcceptanceOperationID {
			return operation, true, nil
		}
	}
	return nil, false, nil
}

func providerAcceptancePreflight(ctx context.Context, service *controlplane.Service) (clients.MonthlyPreflight, clients.MonthlyPreflight, bool) {
	zone := monthlyComputeLaunchZone()
	if zone == "" || monthlyComputeInstanceType(providerAcceptancePackageID) != providerAcceptanceInstanceType {
		return clients.MonthlyPreflight{}, clients.MonthlyPreflight{}, false
	}
	compute, err := service.PreflightMonthlyResource(ctx, clients.MonthlyPreflightInput{ResourceType: "compute", PackageID: providerAcceptancePackageID, Zone: zone})
	if err != nil || !providerAcceptancePreflightValid(compute, "compute", 0, zone) {
		return clients.MonthlyPreflight{}, clients.MonthlyPreflight{}, false
	}
	storage, err := service.PreflightMonthlyResource(ctx, clients.MonthlyPreflightInput{ResourceType: "storage", PackageID: providerAcceptancePackageID, SizeGB: providerAcceptanceStorageGB, Zone: zone})
	if err != nil || !providerAcceptancePreflightValid(storage, "storage", providerAcceptanceStorageGB, zone) {
		return clients.MonthlyPreflight{}, clients.MonthlyPreflight{}, false
	}
	return compute, storage, true
}

func providerAcceptancePreflightValid(preflight clients.MonthlyPreflight, resourceType string, sizeGB int, zone string) bool {
	return preflight.ResourceType == resourceType && preflight.PackageID == providerAcceptancePackageID && preflight.SizeGB == sizeGB && preflight.Zone == zone && preflight.Available &&
		preflight.ChargeType == "PREPAID" && preflight.PeriodMonths == 1 && preflight.RenewFlag == "NOTIFY_AND_MANUAL_RENEW" && preflight.ProviderPriceCNY > 0
}

func providerAcceptanceComputeID() string {
	return resourceIDForMutation("ca", providerAcceptanceAccountID, providerAcceptanceKey+":compute")
}

func providerAcceptanceStorageID() string {
	return resourceIDForMutation("vol", providerAcceptanceAccountID, providerAcceptanceKey+":storage")
}

func (app *controlPlaneServer) advanceProviderAcceptance(ctx context.Context, service *controlplane.Service, ownerID string, sub2APIUserID int64, computePreflight, storagePreflight clients.MonthlyPreflight) (string, string, error) {
	workspaceID := primaryWorkspaceID(providerAcceptanceAccountID)
	computeID := providerAcceptanceComputeID()
	compute, exists := app.getCompute(computeID)
	if !exists {
		created, createErr := service.PrepareMonthlyCompute(ctx, clients.ComputeAllocationInput{ID: computeID, AccountID: providerAcceptanceAccountID, WorkspaceID: workspaceID, PackageID: providerAcceptancePackageID}, providerAcceptanceKey+":compute")
		compute = providerAcceptanceComputeRow(structToMap(created), ownerID, computePreflight)
		if err := app.saveComputeFact(compute); err != nil {
			return "", "", err
		}
		if createErr != nil {
			return "", "provider_acceptance_compute_result_unknown", nil
		}
	} else if monthlyResourceInProgress(compute) {
		synced, syncErr := service.SyncMonthlyCompute(ctx, computeID)
		compute = providerAcceptanceComputeRow(mergeMaps(compute, structToMap(synced)), ownerID, computePreflight)
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
	if !providerAcceptanceComputeValid(compute, time.Now().UTC()) {
		return "", "provider_acceptance_compute_state_ambiguous", nil
	}

	storageID := providerAcceptanceStorageID()
	storage, exists := app.getStorage(storageID)
	if !exists {
		created, createErr := service.PrepareMonthlyStorage(ctx, clients.StorageVolumeInput{ID: storageID, AccountID: providerAcceptanceAccountID, WorkspaceID: workspaceID, ComputeID: computeID, Zone: storagePreflight.Zone, SizeGB: providerAcceptanceStorageGB}, providerAcceptanceKey+":storage")
		storage = providerAcceptanceStorageRow(structToMap(created), ownerID, storagePreflight)
		if err := app.saveStorageFact(storage); err != nil {
			return "", "", err
		}
		if createErr != nil {
			return "", "provider_acceptance_storage_result_unknown", nil
		}
	} else if monthlyResourceInProgress(storage) {
		synced, syncErr := service.SyncMonthlyStorage(ctx, storageID)
		storage = providerAcceptanceStorageRow(mergeMaps(storage, structToMap(synced)), ownerID, storagePreflight)
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
	if !providerAcceptanceStorageValid(storage, time.Now().UTC()) {
		return "", "provider_acceptance_storage_state_ambiguous", nil
	}

	attachment, attachmentCount := app.providerAcceptanceAttachment()
	if attachmentCount > 1 {
		return "", "provider_acceptance_attachment_state_ambiguous", nil
	}
	if attachmentCount == 0 {
		created, createErr := service.CreateStorageAttachment(ctx, controlplane.StorageAttachmentInput{WorkspaceID: workspaceID, ComputeID: computeID, VolumeID: storageID}, providerAcceptanceKey+":attachment")
		attachment = attachmentResponse(structToMap(created), map[string]any{"computeAllocationId": computeID, "storageId": storageID, "mountPath": "/data"})
		attachment["accountId"], attachment["ownerAccountId"] = providerAcceptanceAccountID, providerAcceptanceAccountID
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

	workspace, _ := app.getWorkspace(workspaceID)
	var projection domain.WorkspaceProjection
	if stringValue(workspace["runtimeId"]) == "" {
		prepared, prepareErr := service.PrepareWorkspace(ctx, controlplane.CreateWorkspaceInput{
			WorkspaceID: workspaceID, AccountID: providerAcceptanceAccountID, Sub2APIUserID: sub2APIUserID, OwnerID: ownerID,
			Name: providerAcceptanceSlotID, PackageID: providerAcceptancePackageID, AttachmentID: stringValue(attachment["id"]), ComputeID: computeID, VolumeID: storageID,
		}, providerAcceptanceKey+":workspace")
		if prepareErr != nil {
			return "", "provider_acceptance_runtime_result_unknown", nil
		}
		projection = prepared
	} else {
		runtime, runtimeErr := service.WorkspaceRuntimeStatus(ctx, workspaceID)
		if runtimeErr != nil {
			return "", "provider_acceptance_runtime_result_unknown", nil
		}
		projection = providerAcceptanceWorkspaceProjection(workspace, runtime)
	}
	workspace = providerAcceptanceWorkspaceRow(projection)
	if err := app.tables.SaveWorkspace(ctx, workspace); err != nil {
		return "", "", err
	}
	if !projection.RuntimeReady {
		return "in_progress", "", nil
	}
	if projection.ReceiptID == "" {
		withReceipt, receiptErr := service.RecordWorkspaceCreatedReceipt(ctx, projection, providerAcceptanceKey+":workspace")
		if receiptErr != nil {
			return "", "provider_acceptance_receipt_failed", nil
		}
		projection = withReceipt
		if err := app.tables.SaveWorkspace(ctx, providerAcceptanceWorkspaceRow(projection)); err != nil {
			return "", "", err
		}
	}
	if _, ready := app.providerAcceptanceReadySlot(time.Now().UTC()); !ready {
		return "", "provider_acceptance_state_ambiguous", nil
	}
	return "ready", "", nil
}

func providerAcceptanceComputeRow(row map[string]any, ownerID string, preflight clients.MonthlyPreflight) map[string]any {
	row = computeResponse(row)
	row["id"], row["accountId"], row["ownerAccountId"], row["ownerUserId"] = providerAcceptanceComputeID(), providerAcceptanceAccountID, providerAcceptanceAccountID, ownerID
	row["workspaceId"], row["packageId"], row["name"] = primaryWorkspaceID(providerAcceptanceAccountID), providerAcceptancePackageID, providerAcceptanceSlotID
	row["verificationSlotId"], row["customerProduct"] = providerAcceptanceSlotID, false
	row["requestedPeriodMonths"], row["periodMonths"], row["chargeType"], row["renewFlag"] = preflight.PeriodMonths, preflight.PeriodMonths, preflight.ChargeType, preflight.RenewFlag
	row["billingOperationId"], row["monthlyPriceCnyCents"], row["chargeUsdMicros"] = providerAcceptanceOperationID, int64(0), int64(0)
	row["zone"] = firstNonEmpty(stringValue(row["zone"]), providerDataValue(row, "zone"), preflight.Zone)
	row["instanceType"] = firstNonEmpty(stringValue(row["instanceType"]), providerDataValue(row, "instanceType"))
	providerAcceptanceEntitlement(row)
	return row
}

func providerAcceptanceStorageRow(row map[string]any, ownerID string, preflight clients.MonthlyPreflight) map[string]any {
	row = storageResponse(row)
	row["id"], row["accountId"], row["ownerAccountId"], row["ownerUserId"] = providerAcceptanceStorageID(), providerAcceptanceAccountID, providerAcceptanceAccountID, ownerID
	row["workspaceId"], row["packageId"], row["name"] = primaryWorkspaceID(providerAcceptanceAccountID), providerAcceptancePackageID, providerAcceptanceSlotID
	row["computeAllocationId"], row["sizeGb"] = providerAcceptanceComputeID(), providerAcceptanceStorageGB
	row["verificationSlotId"], row["customerProduct"] = providerAcceptanceSlotID, false
	row["requestedPeriodMonths"], row["periodMonths"], row["chargeType"], row["renewFlag"] = preflight.PeriodMonths, preflight.PeriodMonths, preflight.ChargeType, preflight.RenewFlag
	row["billingOperationId"], row["monthlyPriceCnyCents"], row["chargeUsdMicros"] = providerAcceptanceOperationID, int64(0), int64(0)
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

func providerAcceptanceCostTagsValid(row map[string]any) bool {
	tags := mapField(row, "costTags")
	return stringValue(tags["opl_account_id"]) == providerAcceptanceAccountID && stringValue(tags["opl_workspace_id"]) == primaryWorkspaceID(providerAcceptanceAccountID) &&
		stringValue(tags["opl_resource_id"]) == stringValue(row["id"]) && strings.TrimSpace(stringValue(tags["opl_operation_id"])) != ""
}

func providerAcceptanceComputeValid(row map[string]any, now time.Time) bool {
	deadline, err := monthlyProviderDeadline(row)
	return err == nil && deadline.After(now) && monthlyResourcePrepared("compute", row) && stringValue(row["accountId"]) == providerAcceptanceAccountID &&
		stringValue(row["workspaceId"]) == primaryWorkspaceID(providerAcceptanceAccountID) && stringValue(row["packageId"]) == providerAcceptancePackageID &&
		stringValue(row["instanceType"]) == providerAcceptanceInstanceType && stringValue(row["zone"]) == monthlyComputeLaunchZone() &&
		stringValue(row["chargeType"]) == "PREPAID" && numberField(row, "periodMonths", 0) == 1 && stringValue(row["renewFlag"]) == "NOTIFY_AND_MANUAL_RENEW" &&
		strings.HasPrefix(firstNonEmpty(stringValue(row["cvmInstanceId"]), stringValue(row["instanceId"])), "ins-") && strings.HasPrefix(stringValue(row["nodePoolId"]), "np-") && providerAcceptanceCostTagsValid(row)
}

func providerAcceptanceStorageValid(row map[string]any, now time.Time) bool {
	deadline, err := monthlyProviderDeadline(row)
	return err == nil && deadline.After(now) && monthlyResourcePrepared("storage", row) && stringValue(row["accountId"]) == providerAcceptanceAccountID &&
		stringValue(row["workspaceId"]) == primaryWorkspaceID(providerAcceptanceAccountID) && numberField(row, "sizeGb", 0) == providerAcceptanceStorageGB &&
		stringValue(row["zone"]) == monthlyComputeLaunchZone() && stringValue(row["chargeType"]) == "PREPAID" && numberField(row, "periodMonths", 0) == 1 &&
		stringValue(row["renewFlag"]) == "NOTIFY_AND_MANUAL_RENEW" && strings.HasPrefix(stringValue(row["providerResourceId"]), "disk-") &&
		firstNonEmpty(stringValue(row["pvName"]), stringValue(row["persistentVolumeName"]), providerDataValue(row, "pvName")) != "" && providerAcceptanceCostTagsValid(row)
}

func (app *controlPlaneServer) providerAcceptanceAttachment() (map[string]any, int) {
	var found map[string]any
	count := 0
	for _, attachment := range app.listAttachments(providerAcceptanceAccountID) {
		if stringValue(attachment["workspaceId"]) == primaryWorkspaceID(providerAcceptanceAccountID) {
			found, count = attachment, count+1
		}
	}
	return found, count
}

func providerAcceptanceWorkspaceProjection(workspace map[string]any, runtime clients.WorkspaceRuntime) domain.WorkspaceProjection {
	status := firstNonEmpty(runtime.Status, stringValue(workspace["status"]), "provisioning")
	if runtime.Ready {
		status = "running"
	}
	access := mapField(workspace, "access")
	return domain.WorkspaceProjection{
		ID: stringValue(workspace["id"]), AccountID: providerAcceptanceAccountID, OwnerID: stringValue(workspace["ownerUserId"]), Name: providerAcceptanceSlotID,
		PackageID: providerAcceptancePackageID, Provider: "tencent-tke", URL: firstNonEmpty(runtime.URL, stringValue(workspace["url"])), Status: status,
		ComputeID: providerAcceptanceComputeID(), VolumeID: providerAcceptanceStorageID(), AttachmentID: firstNonEmpty(stringValue(workspace["attachmentId"]), stringValue(workspace["currentAttachmentId"])),
		RuntimeID: firstNonEmpty(runtime.ID, stringValue(workspace["runtimeId"])), RuntimeServiceName: firstNonEmpty(runtime.ServiceName, stringValue(mapField(workspace, "runtime")["serviceName"])), RuntimeReady: runtime.Ready,
		RuntimeUsername: firstNonEmpty(runtime.Access.Username, stringValue(access["username"])), CredentialStatus: firstNonEmpty(runtime.Access.CredentialStatus, stringValue(access["credentialStatus"])),
		CredentialVersion: firstNonEmpty(runtime.Access.CredentialVersion, stringValue(access["credentialVersion"])), CredentialSecretRef: firstNonEmpty(runtime.Access.SecretRef, stringValue(access["secretRef"])), ReceiptID: stringValue(workspace["receiptId"]),
	}
}

func providerAcceptanceWorkspaceRow(projection domain.WorkspaceProjection) map[string]any {
	row := workspaceProjectionRow(projection)
	row["verificationSlotId"], row["customerProduct"] = providerAcceptanceSlotID, false
	row["runtimeServiceName"], row["serviceName"] = projection.RuntimeServiceName, projection.RuntimeServiceName
	return row
}

func (app *controlPlaneServer) providerAcceptanceReadySlot(now time.Time) (map[string]any, bool) {
	workspaces, err := app.tables.ListWorkspaces(context.Background(), providerAcceptanceAccountID)
	workspace, conflict := providerAcceptanceWorkspace(workspaces)
	if err != nil || conflict || workspace == nil || stringValue(workspace["url"]) == "" {
		return nil, false
	}
	compute, computeOK := app.getCompute(providerAcceptanceComputeID())
	storage, storageOK := app.getStorage(providerAcceptanceStorageID())
	attachment, attachmentCount := app.providerAcceptanceAttachment()
	if !computeOK || !storageOK || attachmentCount != 1 || !providerAcceptanceComputeValid(compute, now) || !providerAcceptanceStorageValid(storage, now) ||
		stringValue(attachment["status"]) != "attached" || app.workspaceResponse(cloneMap(workspace))["openable"] != true {
		return nil, false
	}
	return providerAcceptanceSlotResponse(workspace, compute, storage, attachment), true
}

func (app *controlPlaneServer) providerAcceptanceSlotSummary() map[string]any {
	workspace, _ := app.getWorkspace(primaryWorkspaceID(providerAcceptanceAccountID))
	compute, _ := app.getCompute(providerAcceptanceComputeID())
	storage, _ := app.getStorage(providerAcceptanceStorageID())
	attachment, _ := app.providerAcceptanceAttachment()
	return providerAcceptanceSlotResponse(workspace, compute, storage, attachment)
}

func providerAcceptanceSlotResponse(workspace, compute, storage, attachment map[string]any) map[string]any {
	return map[string]any{
		"id": providerAcceptanceSlotID, "accountId": providerAcceptanceAccountID, "workspaceId": stringValue(workspace["id"]), "workspaceUrl": stringValue(workspace["url"]),
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

func (app *controlPlaneServer) writeProviderAcceptanceManualReview(w http.ResponseWriter, r *http.Request, operation map[string]any, reason string) {
	response := providerAcceptanceResponse("manual_review", reason, app.providerAcceptanceSlotSummary())
	operation = mergeMaps(operation, map[string]any{"status": "manual_review", "errorCode": reason, "result": string(mustJSON(response))})
	if err := app.tables.SaveRuntimeOperation(r.Context(), operation); err != nil {
		writeError(w, http.StatusInternalServerError, "state_persist_failed")
		return
	}
	writeJSON(w, http.StatusOK, response)
}
