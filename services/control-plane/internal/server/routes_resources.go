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
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, computePools)
	}))
	mux.HandleFunc("GET /api/compute-allocations", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		accountID, ok := app.scopedAccountID(w, r, nil)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, app.state(accountID, nil)["computeAllocations"])
	}))
	mux.HandleFunc("POST /api/compute-allocations", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		accountID, ok := app.scopedAccountID(w, r, input)
		if !ok {
			return
		}
		if _, blocked := app.reconciliationBlocksNewWorkspaces(); blocked {
			writeError(w, http.StatusConflict, "billing_reconciliation_blocked")
			return
		}
		key := mutationKey(r, input)
		resourceID := firstNonEmpty(stringField(input, "id", ""), newResourceID("ca"))
		preview, err := app.pricingPreviewResponse(r.Context(), map[string]any{
			"accountId":      accountID,
			"resourceType":   "compute",
			"packageId":      stringField(input, "packageId", "basic"),
			"computeId":      resourceID,
			"idempotencyKey": key,
		}, map[string]any{})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "pricing_catalog_unavailable")
			return
		}
		pending := computeResponse(map[string]any{
			"id":              resourceID,
			"accountId":       accountID,
			"ownerUserId":     app.sessionUserID(r),
			"workspaceId":     stringField(input, "workspaceId", ""),
			"name":            stringField(input, "name", ""),
			"packageId":       stringField(input, "packageId", "basic"),
			"status":          "provisioning",
			"desiredStatus":   "running",
			"providerStatus":  "pending",
			"billingStatus":   "pending",
			"pricingVersion":  stringValue(preview["pricingVersion"]),
			"priceSnapshot":   mapField(preview, "priceSnapshot"),
			"holdAmountCents": int64(numberField(preview, "holdAmountCents", 0)),
			"createdAt":       time.Now().UTC().Format(time.RFC3339Nano),
		})
		if err := app.saveComputeFact(pending); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		compute, err := service.CreateComputeAllocation(r.Context(), controlplane.ComputeAllocationInput{
			ID:              resourceID,
			AccountID:       accountID,
			WorkspaceID:     stringField(input, "workspaceId", ""),
			PackageID:       stringField(input, "packageId", "basic"),
			HoldAmountCents: int64(numberField(preview, "holdAmountCents", 0)),
		}, key)
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		body := providerSyncFacts(computeResponse(mergeMaps(pending, structToMap(compute))), nil)
		if err := app.saveComputeFact(body); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "compute.create", "compute_allocation", stringValue(body["id"]), accountID, nil, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusAccepted, body)
	}))
	mux.HandleFunc("GET /api/compute-allocations/{id}", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.PathValue("id"))
		compute, ok := app.getCompute(id)
		if ok && stringValue(compute["status"]) != "provisioning" {
			if !app.canAccessResource(r, compute) {
				writeError(w, http.StatusForbidden, "account_scope_forbidden")
				return
			}
			writeJSON(w, http.StatusOK, compute)
			return
		}
		fresh, err := service.GetComputeAllocation(r.Context(), id)
		if err == nil && fresh.ID != "" {
			body := computeResponse(structToMap(fresh))
			if !app.canAccessResource(r, body) {
				writeError(w, http.StatusForbidden, "account_scope_forbidden")
				return
			}
			if err := app.saveComputeFact(body); err != nil {
				writeError(w, http.StatusInternalServerError, "state_persist_failed")
				return
			}
			writeJSON(w, http.StatusOK, body)
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, "compute_allocation_not_found")
			return
		}
		if !app.canAccessResource(r, compute) {
			writeError(w, http.StatusForbidden, "account_scope_forbidden")
			return
		}
		writeJSON(w, http.StatusOK, compute)
	}))
	mux.HandleFunc("POST /api/compute-allocations/{id}/sync", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
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
		releaseInput := destroyResourceInput(id, existing)
		if stringValue(existing["holdReleaseId"]) != "" || billingStatusFor(existing) == "stopped" {
			releaseInput.HoldID = ""
			releaseInput.HoldAmountCents = 0
		}
		compute, err := service.SyncComputeAllocation(r.Context(), releaseInput, mutationKey(r, input))
		if err != nil {
			_ = app.saveComputeFact(providerSyncFacts(existing, err))
			writeUpstreamError(w, err)
			return
		}
		body := providerSyncFacts(computeResponse(mergeMaps(existing, structToMap(compute))), nil)
		if err := app.saveComputeFact(body); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "compute.sync", "compute_allocation", id, firstNonEmpty(stringValue(existing["accountId"]), stringValue(existing["ownerAccountId"]), stringValue(body["accountId"])), existing, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, body)
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
		compute, err := service.DestroyComputeAllocation(r.Context(), destroyResourceInput(id, existing), mutationKey(r, input))
		if err != nil {
			_ = app.saveComputeFact(providerSyncFacts(existing, err))
			writeUpstreamError(w, err)
			return
		}
		body := providerSyncFacts(computeResponse(mergeMaps(existing, structToMap(compute))), nil)
		if err := app.saveComputeFact(body); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "compute.destroy", "compute_allocation", id, firstNonEmpty(stringValue(existing["accountId"]), stringValue(existing["ownerAccountId"]), stringValue(body["accountId"])), existing, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, body)
	}))
	mux.HandleFunc("POST /api/storage-volumes", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		accountID, ok := app.scopedAccountID(w, r, input)
		if !ok {
			return
		}
		if _, blocked := app.reconciliationBlocksNewWorkspaces(); blocked {
			writeError(w, http.StatusConflict, "billing_reconciliation_blocked")
			return
		}
		key := mutationKey(r, input)
		resourceID := firstNonEmpty(stringField(input, "id", ""), newResourceID("vol"))
		packageID := stringField(input, "packageId", "basic")
		preview, err := app.pricingPreviewResponse(r.Context(), map[string]any{
			"accountId":      accountID,
			"resourceType":   "storage",
			"packageId":      packageID,
			"sizeGb":         numberField(input, "sizeGb", 10),
			"storageId":      resourceID,
			"idempotencyKey": key,
		}, map[string]any{})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "pricing_catalog_unavailable")
			return
		}
		pending := storageResponse(map[string]any{
			"id":              resourceID,
			"accountId":       accountID,
			"ownerUserId":     app.sessionUserID(r),
			"workspaceId":     stringField(input, "workspaceId", ""),
			"name":            stringField(input, "name", ""),
			"packageId":       packageID,
			"sizeGb":          numberField(input, "sizeGb", 10),
			"status":          "provisioning",
			"desiredStatus":   "available",
			"providerStatus":  "pending",
			"billingStatus":   "pending",
			"pricingVersion":  stringValue(preview["pricingVersion"]),
			"priceSnapshot":   mapField(preview, "priceSnapshot"),
			"holdAmountCents": int64(numberField(preview, "holdAmountCents", 0)),
			"createdAt":       time.Now().UTC().Format(time.RFC3339Nano),
		})
		if err := app.saveStorageFact(pending); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		storage, err := service.CreateStorageVolume(r.Context(), controlplane.StorageVolumeInput{
			ID:              resourceID,
			AccountID:       accountID,
			WorkspaceID:     stringField(input, "workspaceId", ""),
			SizeGB:          int(numberField(input, "sizeGb", 10)),
			HoldAmountCents: int64(numberField(preview, "holdAmountCents", 0)),
		}, key)
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		body := providerSyncFacts(storageResponse(mergeMaps(pending, structToMap(storage))), nil)
		if err := app.saveStorageFact(body); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "storage.create", "storage_volume", stringValue(body["id"]), accountID, nil, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusAccepted, body)
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
		storage, err := service.DestroyStorageVolume(r.Context(), destroyResourceInput(id, existing), mutationKey(r, input))
		if err != nil {
			_ = app.saveStorageFact(providerSyncFacts(existing, err))
			writeUpstreamError(w, err)
			return
		}
		body := providerSyncFacts(storageResponse(mergeMaps(existing, structToMap(storage))), nil)
		if err := app.saveStorageFact(body); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "storage.destroy", "storage_volume", id, firstNonEmpty(stringValue(existing["accountId"]), stringValue(existing["ownerAccountId"]), stringValue(body["accountId"])), existing, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, body)
	}))
	mux.HandleFunc("POST /api/storage-volumes/{id}/sync", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		id := strings.TrimSpace(r.PathValue("id"))
		existing, ok := app.getStorage(id)
		if !ok {
			writeError(w, http.StatusNotFound, "storage_volume_not_found")
			return
		}
		if !app.canAccessResource(r, existing) {
			writeError(w, http.StatusForbidden, "account_scope_forbidden")
			return
		}
		releaseInput := destroyResourceInput(id, existing)
		if stringValue(existing["holdReleaseId"]) != "" || billingStatusFor(existing) == "stopped" {
			releaseInput.HoldID = ""
			releaseInput.HoldAmountCents = 0
		}
		storage, err := service.SyncStorageVolume(r.Context(), releaseInput, mutationKey(r, input))
		if err != nil {
			_ = app.saveStorageFact(providerSyncFacts(existing, err))
			writeUpstreamError(w, err)
			return
		}
		body := providerSyncFacts(storageResponse(mergeMaps(existing, structToMap(storage))), nil)
		if err := app.saveStorageFact(body); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "storage.sync", "storage_volume", id, firstNonEmpty(stringValue(existing["accountId"]), stringValue(existing["ownerAccountId"]), stringValue(body["accountId"])), existing, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, body)
	}))
	mux.HandleFunc("POST /api/storage-attachments", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		accountID, ok := app.scopedAccountID(w, r, input)
		if !ok {
			return
		}
		compute, computeOK := app.getCompute(stringField(input, "computeAllocationId", ""))
		storage, storageOK := app.getStorage(stringField(input, "storageId", ""))
		if !computeOK || !storageOK {
			writeError(w, http.StatusBadRequest, "compute_storage_not_found")
			return
		}
		if !app.resourceBelongsToAccount(compute, accountID) || !app.resourceBelongsToAccount(storage, accountID) || !app.canAccessResource(r, compute) || !app.canAccessResource(r, storage) {
			writeError(w, http.StatusForbidden, "account_scope_forbidden")
			return
		}
		workspaceID := stringField(input, "workspaceId", "")
		if (stringValue(compute["workspaceId"]) != "" && stringValue(compute["workspaceId"]) != workspaceID) || (stringValue(storage["workspaceId"]) != "" && stringValue(storage["workspaceId"]) != workspaceID) {
			writeError(w, http.StatusForbidden, "workspace_scope_forbidden")
			return
		}
		accountID = firstNonEmpty(stringValue(compute["accountId"]), stringValue(compute["ownerAccountId"]), stringValue(storage["accountId"]), stringValue(storage["ownerAccountId"]), accountID)
		attachment, err := service.CreateStorageAttachment(r.Context(), controlplane.StorageAttachmentInput{
			WorkspaceID: stringField(input, "workspaceId", ""),
			ComputeID:   stringField(input, "computeAllocationId", ""),
			VolumeID:    stringField(input, "storageId", ""),
		}, mutationKey(r, input))
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		body := attachmentResponse(structToMap(attachment), input)
		body["accountId"] = accountID
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
		if err := app.appendAuditEvent(r, "attachment.detach", "storage_attachment", attachmentID, firstNonEmpty(stringValue(existing["accountId"]), stringValue(existing["ownerAccountId"])), existing, body, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, body)
	}))
}
