package server

import (
	"net/http"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/controlplane"
)

func registerResourceRoutes(mux *http.ServeMux, app *controlPlaneServer, service *controlplane.Service) {
	mux.HandleFunc("GET /api/compute-pools", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		computePools, ok := fabricComputePools(w, r, service)
		if ok {
			writeJSON(w, http.StatusOK, computePools)
		}
	}))
	mux.HandleFunc("GET /api/compute-allocations", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		accountID, ok := app.scopedAccountID(w, r, nil)
		if ok {
			writeJSON(w, http.StatusOK, app.state(accountID, nil)["computeAllocations"])
		}
	}))
	mux.HandleFunc("POST /api/compute-allocations", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		key, ok := requiredMutationKey(w, r)
		if !ok {
			return
		}
		accountID, ok := app.scopedAccountID(w, r, input)
		if !ok {
			return
		}
		packageID, validPackage := input["packageId"].(string)
		if !validPackage || strings.TrimSpace(packageID) == "" {
			writeError(w, http.StatusBadRequest, "invalid_pricing_input")
			return
		}
		if strings.TrimSpace(stringField(input, "id", "")) != "" {
			writeError(w, http.StatusBadRequest, "resource_id_not_allowed")
			return
		}
		if _, blocked := app.reconciliationBlocksNewWorkspaces(); blocked {
			writeError(w, http.StatusConflict, "billing_reconciliation_blocked")
			return
		}
		resourceID := resourceIDForMutation("ca", accountID, key)
		workspaceID := resourceIDForMutation("ws", accountID, key)
		unlock := app.lockResource("compute", resourceID)
		defer unlock()
		body, err := app.purchaseMonthlyResource(r.Context(), service, monthlyPurchaseInput{
			ResourceType: "compute", ResourceID: resourceID, BillingOperationID: "billing-" + stableID("compute", accountID, key)[:18],
			AccountID: accountID, OwnerUserID: app.sessionUserID(r), WorkspaceID: workspaceID,
			Name: stringField(input, "name", ""), PackageID: packageID, Environment: monthlyEnvironment(),
		})
		if err != nil {
			writeMonthlyPurchaseError(w, err)
			return
		}
		body = computeResponse(body)
		if err := app.appendAuditEvent(r, "compute.create", "compute_allocation", resourceID, accountID, nil, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusAccepted, body)
	}))
	mux.HandleFunc("GET /api/compute-allocations/{id}", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		app.serveMonthlyCompute(w, r, service, strings.TrimSpace(r.PathValue("id")), false, nil)
	}))
	mux.HandleFunc("POST /api/compute-allocations/{id}/sync", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		app.serveMonthlyCompute(w, r, service, strings.TrimSpace(r.PathValue("id")), true, decodeJSON(r))
	}))
	mux.HandleFunc("POST /api/compute-allocations/{id}/destroy", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		if !confirmed(input, "confirm") {
			writeError(w, http.StatusBadRequest, "confirmation_required")
			return
		}
		id := strings.TrimSpace(r.PathValue("id"))
		existing, ok := app.getCompute(id)
		if !ok {
			writeError(w, http.StatusNotFound, "compute_allocation_not_found")
			return
		}
		if !app.canAccessResource(r, existing) {
			writeError(w, http.StatusForbidden, "account_scope_forbidden")
			return
		}
		unlock := app.lockResource("compute", id)
		defer unlock()
		existing, _ = app.getCompute(id)
		result, err := app.cleanupComputeResource(r.Context(), service, id, mutationKey(r, input))
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		body := computeResponse(mergeMaps(existing, structToMap(result)))
		body["status"], body["desiredStatus"], body["billingStatus"] = "destroyed", "destroyed", "stopped"
		if err := app.saveComputeFact(body); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "compute.destroy", "compute_allocation", id, stringValue(existing["accountId"]), existing, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, body)
	}))
	mux.HandleFunc("POST /api/storage-volumes", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		key, ok := requiredMutationKey(w, r)
		if !ok {
			return
		}
		accountID, ok := app.scopedAccountID(w, r, input)
		if !ok {
			return
		}
		sizeGB, validSize := positiveIntegerField(input, "sizeGb")
		if !validSize {
			writeError(w, http.StatusBadRequest, "invalid_pricing_input")
			return
		}
		if _, blocked := app.reconciliationBlocksNewWorkspaces(); blocked {
			writeError(w, http.StatusConflict, "billing_reconciliation_blocked")
			return
		}
		computeID := strings.TrimSpace(stringField(input, "computeAllocationId", ""))
		if computeID == "" {
			writeError(w, http.StatusBadRequest, "compute_allocation_required")
			return
		}
		resourceID := resourceIDForMutation("vol", accountID, key)
		var retained map[string]any
		if requestedID := strings.TrimSpace(stringField(input, "id", "")); requestedID != "" {
			existing, exists := app.getStorage(requestedID)
			if !exists {
				writeError(w, http.StatusBadRequest, "retained_storage_required")
				return
			}
			if !app.canAccessResource(r, existing) {
				writeError(w, http.StatusForbidden, "account_scope_forbidden")
				return
			}
			if stringValue(existing["billingStatus"]) != "retained" {
				writeError(w, http.StatusConflict, "retained_storage_required")
				return
			}
			resourceID = requestedID
			retained = existing
		}
		unlock := app.lockEntitlementResources(computeID, resourceID, "")
		defer unlock()
		compute, exists := app.getCompute(computeID)
		if !exists {
			writeError(w, http.StatusBadRequest, "compute_allocation_required")
			return
		}
		if !app.resourceBelongsToAccount(compute, accountID) || !app.canAccessResource(r, compute) {
			writeError(w, http.StatusForbidden, "account_scope_forbidden")
			return
		}
		workspaceID := stringValue(compute["workspaceId"])
		if workspaceID == "" || (retained != nil && stringValue(retained["workspaceId"]) != workspaceID) {
			writeError(w, http.StatusConflict, "compute_storage_workspace_mismatch")
			return
		}
		if !ensureMonthlyEntitlements(w, time.Now(), compute) {
			return
		}
		zone := stringValue(mapField(compute, "providerData")["zone"])
		if zone == "" {
			writeError(w, http.StatusConflict, "compute_zone_unavailable")
			return
		}
		body, err := app.purchaseMonthlyResource(r.Context(), service, monthlyPurchaseInput{
			ResourceType: "storage", ResourceID: resourceID, BillingOperationID: "billing-" + stableID("storage", accountID, key)[:18],
			AccountID: accountID, OwnerUserID: app.sessionUserID(r), WorkspaceID: workspaceID,
			Name: stringField(input, "name", ""), PackageID: stringField(input, "packageId", "basic"),
			SizeGB: int(sizeGB), ComputeID: computeID, Zone: zone, Environment: monthlyEnvironment(),
		})
		if err != nil {
			writeMonthlyPurchaseError(w, err)
			return
		}
		body = storageResponse(body)
		if err := app.appendAuditEvent(r, "storage.create", "storage_volume", resourceID, accountID, nil, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusAccepted, body)
	}))
	mux.HandleFunc("POST /api/storage-volumes/{id}/sync", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		app.serveMonthlyStorage(w, r, service, strings.TrimSpace(r.PathValue("id")), decodeJSON(r))
	}))
	mux.HandleFunc("POST /api/storage-volumes/destroy", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		if !confirmed(input, "confirmDataLoss") {
			writeError(w, http.StatusBadRequest, "confirmation_required")
			return
		}
		id := stringField(input, "storageId", "")
		existing, ok := app.getStorage(id)
		if !ok {
			writeError(w, http.StatusNotFound, "storage_volume_not_found")
			return
		}
		if !app.canAccessResource(r, existing) {
			writeError(w, http.StatusForbidden, "account_scope_forbidden")
			return
		}
		unlock := app.lockResource("storage", id)
		defer unlock()
		existing, _ = app.getStorage(id)
		result, err := service.CleanupMonthlyStorage(r.Context(), id, mutationKey(r, input))
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		body := storageResponse(mergeMaps(existing, structToMap(result)))
		body["status"], body["desiredStatus"], body["billingStatus"] = "destroyed", "destroyed", "stopped"
		if err := app.saveStorageFact(body); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "storage.destroy", "storage_volume", id, stringValue(existing["accountId"]), existing, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, body)
	}))
	mux.HandleFunc("POST /api/resources/{id}/auto-renew", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		if _, ok := requiredMutationKey(w, r); !ok {
			return
		}
		autoRenew, ok := input["autoRenew"].(bool)
		if !ok {
			writeError(w, http.StatusBadRequest, "autoRenew_required")
			return
		}
		id := strings.TrimSpace(r.PathValue("id"))
		resourceType := "compute"
		existing, ok := app.getCompute(id)
		if !ok {
			resourceType = "storage"
			existing, ok = app.getStorage(id)
		}
		if !ok {
			writeError(w, http.StatusNotFound, "resource_not_found")
			return
		}
		if !app.canAccessResource(r, existing) {
			writeError(w, http.StatusForbidden, "account_scope_forbidden")
			return
		}
		unlock := app.lockResource(resourceType, id)
		defer unlock()
		existing, _ = app.monthlyResource(resourceType, id)
		before := cloneMap(existing)
		existing["autoRenew"] = autoRenew
		if err := app.saveMonthlyResource(r.Context(), resourceType, existing); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "resource.auto_renew", resourceType, id, stringValue(existing["accountId"]), before, existing, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if resourceType == "storage" {
			writeJSON(w, http.StatusOK, storageResponse(existing))
			return
		}
		writeJSON(w, http.StatusOK, computeResponse(existing))
	}))
	mux.HandleFunc("POST /api/storage-attachments", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		accountID, ok := app.scopedAccountID(w, r, input)
		if !ok {
			return
		}
		computeID, storageID := stringField(input, "computeAllocationId", ""), stringField(input, "storageId", "")
		unlock := app.lockEntitlementResources(computeID, storageID, "")
		defer unlock()
		compute, computeOK := app.getCompute(computeID)
		storage, storageOK := app.getStorage(storageID)
		if !computeOK || !storageOK {
			writeError(w, http.StatusBadRequest, "compute_storage_not_found")
			return
		}
		if !app.resourceBelongsToAccount(compute, accountID) || !app.resourceBelongsToAccount(storage, accountID) || !app.canAccessResource(r, compute) || !app.canAccessResource(r, storage) {
			writeError(w, http.StatusForbidden, "account_scope_forbidden")
			return
		}
		workspaceID := stringValue(compute["workspaceId"])
		packageID := stringValue(compute["packageId"])
		if workspaceID == "" || stringValue(storage["workspaceId"]) != workspaceID || packageID == "" || stringValue(storage["packageId"]) != packageID {
			writeError(w, http.StatusConflict, "compute_storage_workspace_mismatch")
			return
		}
		if !ensureMonthlyEntitlements(w, time.Now(), compute, storage) {
			return
		}
		attachment, err := service.CreateStorageAttachment(r.Context(), controlplane.StorageAttachmentInput{
			WorkspaceID: workspaceID, ComputeID: stringField(input, "computeAllocationId", ""), VolumeID: stringField(input, "storageId", ""),
		}, mutationKey(r, input))
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		if attachment.WorkspaceID != workspaceID || attachment.ComputeID != computeID || attachment.VolumeID != storageID {
			writeError(w, http.StatusBadGateway, "fabric_attachment_identity_mismatch")
			return
		}
		input["workspaceId"], input["packageId"] = workspaceID, packageID
		body := attachmentResponse(structToMap(attachment), input)
		body["accountId"], body["packageId"] = accountID, packageID
		if err := app.saveAttachmentFact(body, input); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "attachment.create", "storage_attachment", stringValue(body["id"]), accountID, nil, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusAccepted, body)
	}))
	mux.HandleFunc("POST /api/storage-attachments/detach", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		attachmentID := stringField(input, "attachmentId", "")
		unlock := app.lockResource("attachment", attachmentID)
		defer unlock()
		existing, ok := app.getAttachment(attachmentID)
		if !ok {
			writeError(w, http.StatusNotFound, "storage_attachment_not_found")
			return
		}
		if !app.canAccessResource(r, existing) {
			writeError(w, http.StatusForbidden, "account_scope_forbidden")
			return
		}
		attachment, err := service.DetachStorageAttachment(r.Context(), attachmentID, mutationKey(r, input))
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		body := attachmentResponse(mergeMaps(existing, structToMap(attachment)), input)
		if err := app.saveAttachmentFact(body, input); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "attachment.detach", "storage_attachment", attachmentID, stringValue(existing["accountId"]), existing, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, body)
	}))
}

func (app *controlPlaneServer) serveMonthlyCompute(w http.ResponseWriter, r *http.Request, service *controlplane.Service, id string, forceSync bool, input map[string]any) {
	existing, ok := app.getCompute(id)
	if !ok {
		writeError(w, http.StatusNotFound, "compute_allocation_not_found")
		return
	}
	if !app.canAccessResource(r, existing) {
		writeError(w, http.StatusForbidden, "account_scope_forbidden")
		return
	}
	unlock := app.lockResource("compute", id)
	defer unlock()
	existing, _ = app.getCompute(id)
	if stringValue(existing["billingStatus"]) == "preparing" {
		body, err := app.resumeMonthlyPurchase(r.Context(), service, existing)
		if err != nil {
			writeMonthlyPurchaseError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, computeResponse(body))
		return
	}
	if !forceSync && stringValue(existing["status"]) != "provisioning" {
		writeJSON(w, http.StatusOK, existing)
		return
	}
	fresh, err := service.SyncMonthlyCompute(r.Context(), id)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	body := providerSyncFacts(computeResponse(mergeMaps(existing, structToMap(fresh))), nil)
	if err := app.saveComputeFact(body); err != nil {
		writeError(w, http.StatusInternalServerError, "state_persist_failed")
		return
	}
	if forceSync {
		_ = app.appendAuditEvent(r, "compute.sync", "compute_allocation", id, stringValue(existing["accountId"]), existing, body, "succeeded")
	}
	writeJSON(w, http.StatusOK, body)
}

func (app *controlPlaneServer) serveMonthlyStorage(w http.ResponseWriter, r *http.Request, service *controlplane.Service, id string, input map[string]any) {
	existing, ok := app.getStorage(id)
	if !ok {
		writeError(w, http.StatusNotFound, "storage_volume_not_found")
		return
	}
	if !app.canAccessResource(r, existing) {
		writeError(w, http.StatusForbidden, "account_scope_forbidden")
		return
	}
	unlock := app.lockResource("storage", id)
	defer unlock()
	existing, _ = app.getStorage(id)
	if stringValue(existing["billingStatus"]) == "preparing" {
		body, err := app.resumeMonthlyPurchase(r.Context(), service, existing)
		if err != nil {
			writeMonthlyPurchaseError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, storageResponse(body))
		return
	}
	fresh, err := service.SyncMonthlyStorage(r.Context(), id)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	body := providerSyncFacts(storageResponse(mergeMaps(existing, structToMap(fresh))), nil)
	if err := app.saveStorageFact(body); err != nil {
		writeError(w, http.StatusInternalServerError, "state_persist_failed")
		return
	}
	_ = app.appendAuditEvent(r, "storage.sync", "storage_volume", id, stringValue(existing["accountId"]), existing, body, "succeeded")
	writeJSON(w, http.StatusOK, body)
}
