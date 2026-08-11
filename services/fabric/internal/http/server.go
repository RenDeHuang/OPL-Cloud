package http

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"opl-cloud/services/fabric/internal/fabric"
)

func NewServer(service *fabric.Service, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /fabric/readiness", func(w http.ResponseWriter, r *http.Request) {
		readiness, err := service.Readiness(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, readiness)
	})
	mux.HandleFunc("GET /fabric/catalog", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, service.Catalog(r.Context()))
	})
	mux.HandleFunc("POST /fabric/monthly-preflight", func(w http.ResponseWriter, r *http.Request) {
		var input fabric.MonthlyPreflightInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		result, err := service.MonthlyPreflight(r.Context(), input)
		if errors.Is(err, fabric.ErrInvalidMonthlyPreflight) {
			writeError(w, http.StatusBadRequest, fabric.ErrInvalidMonthlyPreflight.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, fabric.ErrMonthlyPreflightUnavailable.Error())
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("GET /fabric/compute-pool-head", func(w http.ResponseWriter, r *http.Request) {
		nodePoolID, ok := exactQueryValue(r, "nodePoolId")
		if !ok {
			writeError(w, http.StatusBadRequest, fabric.ErrInvalidMonthlyPreflight.Error())
			return
		}
		result, err := service.ReadComputePoolHead(r.Context(), nodePoolID)
		if errors.Is(err, fabric.ErrInvalidMonthlyPreflight) {
			writeError(w, http.StatusBadRequest, fabric.ErrInvalidMonthlyPreflight.Error())
			return
		}
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, result)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("GET /fabric/compute-pool-head/terminalization", func(w http.ResponseWriter, r *http.Request) {
		values := r.URL.Query()
		if len(values) == 1 {
			nodePoolID, ok := exactQueryValue(r, "nodePoolId")
			if !ok {
				writeError(w, http.StatusBadRequest, fabric.ErrInvalidComputePoolHeadTerminalization.Error())
				return
			}
			result, err := service.ReadComputePoolHeadTerminalization(r.Context(), nodePoolID)
			writeComputePoolHeadTerminalizationResult(w, result, err)
			return
		}
		if len(values) != 3 || len(values["nodePoolId"]) != 1 || len(values["approvalId"]) != 1 || len(values["approvalDigest"]) != 1 {
			writeError(w, http.StatusBadRequest, fabric.ErrInvalidComputePoolHeadTerminalization.Error())
			return
		}
		input := fabric.ComputePoolHeadTerminalizationInput{
			NodePoolID: values.Get("nodePoolId"), ApprovalID: values.Get("approvalId"), ApprovalDigest: values.Get("approvalDigest"),
			IdempotencyKey: values.Get("approvalId"),
		}
		result, err := service.ReadComputePoolHeadTerminalizationResult(r.Context(), input)
		writeComputePoolHeadTerminalizationResult(w, result, err)
	})
	mux.HandleFunc("POST /fabric/compute-pool-head/terminalization", func(w http.ResponseWriter, r *http.Request) {
		idempotencyKey := r.Header.Get("Idempotency-Key")
		if idempotencyKey == "" || idempotencyKey != strings.TrimSpace(idempotencyKey) {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		var input fabric.ComputePoolHeadTerminalizationInput
		decoder := json.NewDecoder(io.LimitReader(r.Body, 4097))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		input.IdempotencyKey = idempotencyKey
		result, err := service.TerminalizeComputePoolHead(r.Context(), input)
		writeComputePoolHeadTerminalizationResult(w, result, err)
	})
	mux.HandleFunc("GET /fabric/monthly-preflight-report", func(w http.ResponseWriter, r *http.Request) {
		values := r.URL.Query()
		if len(values) != 1 || len(values["zone"]) != 1 {
			writeError(w, http.StatusBadRequest, fabric.ErrInvalidMonthlyPreflight.Error())
			return
		}
		result, err := service.MonthlyPreflightReport(r.Context(), fabric.MonthlyPreflightReportInput{Zone: values.Get("zone")})
		if errors.Is(err, fabric.ErrInvalidMonthlyPreflight) {
			writeError(w, http.StatusBadRequest, fabric.ErrInvalidMonthlyPreflight.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, fabric.ErrMonthlyPreflightUnavailable.Error())
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("GET /fabric/monthly-provider-truth", func(w http.ResponseWriter, r *http.Request) {
		computeIDs, computeOK := r.URL.Query()["computeAllocationId"]
		storageIDs, storageOK := r.URL.Query()["storageVolumeId"]
		if !computeOK || !storageOK || len(computeIDs) != 1 || len(storageIDs) != 1 || strings.TrimSpace(computeIDs[0]) == "" || strings.TrimSpace(storageIDs[0]) == "" ||
			computeIDs[0] != strings.TrimSpace(computeIDs[0]) || storageIDs[0] != strings.TrimSpace(storageIDs[0]) {
			writeError(w, http.StatusBadRequest, fabric.ErrInvalidMonthlyProviderTruth.Error())
			return
		}
		result, err := service.MonthlyProviderTruth(r.Context(), computeIDs[0], storageIDs[0])
		if errors.Is(err, fabric.ErrInvalidMonthlyProviderTruth) {
			writeError(w, http.StatusBadRequest, fabric.ErrInvalidMonthlyProviderTruth.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, fabric.ErrMonthlyProviderTruthUnavailable.Error())
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("GET /fabric/compute-provider-truth", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		keys := []string{"launchOperationId", "accountId", "workspaceId", "computeAllocationId", "storageVolumeId", "packageId", "poolId", "nodePoolId"}
		if len(query) != len(keys) {
			writeError(w, http.StatusBadRequest, fabric.ErrInvalidComputeClaimRecovery.Error())
			return
		}
		values := make(map[string]string, len(keys))
		for _, key := range keys {
			items, ok := query[key]
			if !ok || len(items) != 1 || items[0] == "" || items[0] != strings.TrimSpace(items[0]) {
				writeError(w, http.StatusBadRequest, fabric.ErrInvalidComputeClaimRecovery.Error())
				return
			}
			values[key] = items[0]
		}
		input := fabric.ComputeClaimRecoveryInput{
			LaunchOperationID: values["launchOperationId"], AccountID: values["accountId"], WorkspaceID: values["workspaceId"],
			ComputeAllocationID: values["computeAllocationId"], StorageVolumeID: values["storageVolumeId"], PackageID: values["packageId"],
			PoolID: values["poolId"], NodePoolID: values["nodePoolId"], AllowExistingStorageOperation: true,
		}
		if input.LaunchOperationID == "" || input.AccountID == "" || input.WorkspaceID == "" || input.ComputeAllocationID == "" ||
			input.StorageVolumeID == "" || input.PackageID == "" || input.PoolID == "" || input.NodePoolID == "" {
			writeError(w, http.StatusBadRequest, fabric.ErrInvalidComputeClaimRecovery.Error())
			return
		}
		result, _ := service.ComputeProviderTruth(r.Context(), input)
		// A normalized unavailable result is still evidence. Keep HTTP 200 so
		// callers can retain other successful reads instead of losing the whole
		// Compute snapshot to a later-stage Storage error.
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("POST /fabric/workspace-activation-truth", func(w http.ResponseWriter, r *http.Request) {
		var input fabric.WorkspaceActivationTruthInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		truth, err := service.WorkspaceActivationTruth(r.Context(), input)
		writeWorkspaceActivationTruthResult(w, truth, err)
	})
	mux.HandleFunc("POST /fabric/workspace-launch-stage-readback/proof", func(w http.ResponseWriter, r *http.Request) {
		var input fabric.WorkspaceLaunchStageReadbackInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		proof, err := service.WorkspaceLaunchStageReadbackProof(r.Context(), input)
		writeWorkspaceLaunchStageReadbackResult(w, proof, err)
	})
	mux.HandleFunc("POST /fabric/workspace-launch-stage-readback/converge", func(w http.ResponseWriter, r *http.Request) {
		var input fabric.WorkspaceLaunchStageReadbackInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		proof, err := service.ConvergeWorkspaceLaunchStageReadback(r.Context(), input)
		writeWorkspaceLaunchStageReadbackResult(w, proof, err)
	})
	mux.HandleFunc("POST /fabric/compute-claim-recovery/proof", func(w http.ResponseWriter, r *http.Request) {
		var input fabric.ComputeClaimRecoveryInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		proof, err := service.ComputeClaimRecoveryProof(r.Context(), input)
		writeComputeClaimRecoveryResult(w, http.StatusOK, proof, err)
	})
	mux.HandleFunc("POST /fabric/compute-claim-recovery/identity-evidence", func(w http.ResponseWriter, r *http.Request) {
		var input fabric.ComputeClaimRecoveryClaimInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		input.IdempotencyKey = input.LaunchOperationID + ":compute"
		evidence, err := service.ComputeClaimRecoveryIdentityEvidence(r.Context(), input)
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, evidence)
	})
	mux.HandleFunc("POST /fabric/compute-claim-recovery/claim", func(w http.ResponseWriter, r *http.Request) {
		idempotencyKey := r.Header.Get("Idempotency-Key")
		if strings.TrimSpace(idempotencyKey) == "" || idempotencyKey != strings.TrimSpace(idempotencyKey) {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		var input fabric.ComputeClaimRecoveryClaimInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		input.IdempotencyKey = idempotencyKey
		proof, err := service.ClaimComputeRecovery(r.Context(), input)
		writeComputeClaimRecoveryResult(w, http.StatusAccepted, proof, err)
	})
	mux.HandleFunc("POST /fabric/provider-facts/batch", func(w http.ResponseWriter, r *http.Request) {
		var input fabric.ProviderFactsBatchInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		result, err := service.ProviderFactsBatch(r.Context(), input)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("GET /fabric/runtime-health-summary", func(w http.ResponseWriter, r *http.Request) {
		result, err := service.RuntimeHealthSummary(r.Context())
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, fabric.ErrRuntimeHealthSummaryUnavailable.Error())
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("GET /fabric/operations", func(w http.ResponseWriter, r *http.Request) {
		operations, err := service.ListOperations(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, operations)
	})
	mux.HandleFunc("GET /fabric/machine-ownerships/{resourceId}", func(w http.ResponseWriter, r *http.Request) {
		ownership, err := service.MachineOwnership(r.Context(), r.PathValue("resourceId"))
		switch {
		case errors.Is(err, fabric.ErrMachineOwnershipNotFound):
			writeError(w, http.StatusNotFound, err.Error())
		case err != nil:
			writeError(w, http.StatusServiceUnavailable, "machine ownership query failed")
		default:
			writeJSON(w, http.StatusOK, ownership)
		}
	})
	mux.HandleFunc("POST /fabric/jobs", func(w http.ResponseWriter, r *http.Request) {
		var input fabric.JobInput
		if !decodeWrite(w, r, &input.IdempotencyKey, &input) {
			return
		}
		job, err := service.CreateJob(r.Context(), input)
		writeJobResult(w, http.StatusAccepted, job, err)
	})
	mux.HandleFunc("GET /fabric/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		job, err := service.Job(r.Context(), strings.TrimSpace(r.PathValue("id")))
		writeJobResult(w, http.StatusOK, job, err)
	})
	mux.HandleFunc("POST /fabric/jobs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		idempotencyKey := r.Header.Get("Idempotency-Key")
		if idempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		job, err := service.CancelJob(r.Context(), strings.TrimSpace(r.PathValue("id")), idempotencyKey)
		writeJobResult(w, http.StatusAccepted, job, err)
	})
	mux.HandleFunc("POST /fabric/jobs/{id}/claim", func(w http.ResponseWriter, r *http.Request) {
		var input fabric.JobClaimInput
		if !decodeWrite(w, r, &input.IdempotencyKey, &input) {
			return
		}
		job, err := service.ClaimJob(r.Context(), strings.TrimSpace(r.PathValue("id")), input)
		writeJobResult(w, http.StatusAccepted, job, err)
	})
	mux.HandleFunc("POST /fabric/jobs/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		var input fabric.JobHeartbeatInput
		if !decodeWrite(w, r, &input.IdempotencyKey, &input) {
			return
		}
		job, err := service.HeartbeatJob(r.Context(), strings.TrimSpace(r.PathValue("id")), input)
		writeJobResult(w, http.StatusAccepted, job, err)
	})
	mux.HandleFunc("POST /fabric/jobs/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
		var input fabric.JobCompleteInput
		if !decodeWrite(w, r, &input.IdempotencyKey, &input) {
			return
		}
		job, err := service.CompleteJob(r.Context(), strings.TrimSpace(r.PathValue("id")), input)
		writeJobResult(w, http.StatusAccepted, job, err)
	})
	mux.HandleFunc("POST /fabric/jobs/{id}/fail", func(w http.ResponseWriter, r *http.Request) {
		var input fabric.JobFailInput
		if !decodeWrite(w, r, &input.IdempotencyKey, &input) {
			return
		}
		job, err := service.FailJob(r.Context(), strings.TrimSpace(r.PathValue("id")), input)
		writeJobResult(w, http.StatusAccepted, job, err)
	})
	mux.HandleFunc("POST /fabric/jobs/{id}/retry", func(w http.ResponseWriter, r *http.Request) {
		idempotencyKey := r.Header.Get("Idempotency-Key")
		if idempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		job, err := service.RetryJob(r.Context(), strings.TrimSpace(r.PathValue("id")), idempotencyKey)
		writeJobResult(w, http.StatusAccepted, job, err)
	})
	mux.HandleFunc("POST /fabric/compute-allocations", func(w http.ResponseWriter, r *http.Request) {
		var input fabric.ComputeAllocationInput
		if !decodeWrite(w, r, &input.IdempotencyKey, &input) {
			return
		}
		allocation, err := service.CreateComputeAllocation(r.Context(), input)
		writeComputeAllocationResult(w, allocation, err)
	})
	mux.HandleFunc("GET /fabric/compute-allocations/{id}", func(w http.ResponseWriter, r *http.Request) {
		allocation, ok := service.GetComputeAllocation(r.Context(), strings.TrimSpace(r.PathValue("id")))
		if !ok {
			writeError(w, http.StatusNotFound, "compute_allocation_not_found")
			return
		}
		writeJSON(w, http.StatusOK, allocation)
	})
	mux.HandleFunc("POST /fabric/compute-allocations/{id}/sync", func(w http.ResponseWriter, r *http.Request) {
		allocation, err := service.SyncComputeAllocation(r.Context(), strings.TrimSpace(r.PathValue("id")))
		writeResult(w, allocation, err)
	})
	mux.HandleFunc("POST /fabric/compute-allocations/{id}/destroy", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Idempotency-Key") == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		allocation, err := service.DestroyComputeAllocation(r.Context(), strings.TrimSpace(r.PathValue("id")))
		writeResult(w, allocation, err)
	})
	mux.HandleFunc("POST /fabric/compute-allocations/{id}/renew", func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		allocation, err := service.RenewComputeAllocation(r.Context(), strings.TrimSpace(r.PathValue("id")), key)
		writeResult(w, allocation, err)
	})
	mux.HandleFunc("POST /fabric/storage-volumes", func(w http.ResponseWriter, r *http.Request) {
		var input fabric.StorageVolumeInput
		if !decodeWrite(w, r, &input.IdempotencyKey, &input) {
			return
		}
		volume, err := service.CreateStorageVolume(r.Context(), input)
		writeResult(w, volume, err)
	})
	mux.HandleFunc("GET /fabric/storage-volumes/{id}", func(w http.ResponseWriter, r *http.Request) {
		volume, err := service.ReadStorageVolume(r.Context(), strings.TrimSpace(r.PathValue("id")))
		if err != nil && volume.ID == "" {
			writeError(w, http.StatusNotFound, "storage_volume_not_found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, volume)
	})
	mux.HandleFunc("POST /fabric/storage-volumes/{id}/renew", func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		volume, err := service.RenewStorageVolume(r.Context(), strings.TrimSpace(r.PathValue("id")), key)
		writeResult(w, volume, err)
	})
	mux.HandleFunc("POST /fabric/storage-volumes/{id}/destroy", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Idempotency-Key") == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		volume, err := service.DestroyStorageVolume(r.Context(), strings.TrimSpace(r.PathValue("id")))
		writeResult(w, volume, err)
	})
	mux.HandleFunc("POST /fabric/storage-volumes/{id}/sync", func(w http.ResponseWriter, r *http.Request) {
		volume, err := service.SyncStorageVolume(r.Context(), strings.TrimSpace(r.PathValue("id")))
		writeResult(w, volume, err)
	})
	mux.HandleFunc("POST /fabric/storage-snapshots", func(w http.ResponseWriter, r *http.Request) {
		var input fabric.StorageSnapshotInput
		if !decodeWrite(w, r, &input.IdempotencyKey, &input) {
			return
		}
		snapshot, err := service.CreateStorageSnapshot(r.Context(), input)
		writeResult(w, snapshot, err)
	})
	mux.HandleFunc("GET /fabric/storage-snapshots/{id}", func(w http.ResponseWriter, r *http.Request) {
		snapshot, ok := service.GetStorageSnapshot(r.Context(), strings.TrimSpace(r.PathValue("id")))
		if !ok {
			writeError(w, http.StatusNotFound, "storage_snapshot_not_found")
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
	})
	mux.HandleFunc("POST /fabric/storage-snapshots/{id}/sync", func(w http.ResponseWriter, r *http.Request) {
		snapshot, err := service.SyncStorageSnapshot(r.Context(), strings.TrimSpace(r.PathValue("id")))
		writeResult(w, snapshot, err)
	})
	mux.HandleFunc("POST /fabric/storage-snapshots/{id}/restore", func(w http.ResponseWriter, r *http.Request) {
		var input fabric.StorageRestoreInput
		if !decodeWrite(w, r, &input.IdempotencyKey, &input) {
			return
		}
		input.SnapshotID = strings.TrimSpace(r.PathValue("id"))
		volume, err := service.RestoreStorageSnapshot(r.Context(), input)
		writeResult(w, volume, err)
	})
	mux.HandleFunc("POST /fabric/storage-snapshots/{id}/destroy", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Idempotency-Key") == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		snapshot, err := service.DestroyStorageSnapshot(r.Context(), strings.TrimSpace(r.PathValue("id")))
		writeResult(w, snapshot, err)
	})
	mux.HandleFunc("POST /fabric/storage-attachments", func(w http.ResponseWriter, r *http.Request) {
		var input fabric.StorageAttachmentInput
		if !decodeWrite(w, r, &input.IdempotencyKey, &input) {
			return
		}
		attachment, err := service.CreateStorageAttachment(r.Context(), input)
		writeResult(w, attachment, err)
	})
	mux.HandleFunc("POST /fabric/storage-attachments/{id}/detach", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Idempotency-Key") == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		attachment, err := service.DetachStorageAttachment(r.Context(), strings.TrimSpace(r.PathValue("id")))
		writeResult(w, attachment, err)
	})
	mux.HandleFunc("POST /fabric/workspace-runtimes", func(w http.ResponseWriter, r *http.Request) {
		var input fabric.WorkspaceRuntimeInput
		if !decodeWrite(w, r, &input.IdempotencyKey, &input) {
			return
		}
		runtime, err := service.CreateWorkspaceRuntime(r.Context(), input)
		writeResult(w, runtime, err)
	})
	mux.HandleFunc("POST /fabric/workspace-runtimes/{workspaceId}/destroy", func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		runtime, err := service.DestroyWorkspaceRuntime(r.Context(), strings.TrimSpace(r.PathValue("workspaceId")), key)
		writeResult(w, runtime, err)
	})
	mux.HandleFunc("GET /fabric/workspace-runtimes/{workspaceId}/status", func(w http.ResponseWriter, r *http.Request) {
		runtime, err := service.WorkspaceRuntimeStatus(r.Context(), strings.TrimSpace(r.PathValue("workspaceId")))
		writeResult(w, runtime, err)
	})
	mux.HandleFunc("POST /fabric/workspace-runtimes/{workspaceId}/gateway-secret", func(w http.ResponseWriter, r *http.Request) {
		var input fabric.WorkspaceRuntimeGatewaySecretInput
		if !decodeWrite(w, r, &input.IdempotencyKey, &input) {
			return
		}
		if input.WorkspaceID != strings.TrimSpace(r.PathValue("workspaceId")) {
			writeError(w, http.StatusBadRequest, "workspace_runtime_gateway_secret_input_required")
			return
		}
		binding, err := service.BindWorkspaceRuntimeGatewaySecret(r.Context(), input)
		writeResult(w, binding, err)
	})
	mux.HandleFunc("GET /fabric/workspace-runtimes/{workspaceId}/gateway-secret", func(w http.ResponseWriter, r *http.Request) {
		binding, err := service.WorkspaceRuntimeGatewaySecret(r.Context(), strings.TrimSpace(r.PathValue("workspaceId")))
		writeResult(w, binding, err)
	})
	mux.HandleFunc("POST /fabric/gateway-secrets", func(w http.ResponseWriter, r *http.Request) {
		var input fabric.GatewaySecretInput
		if !decodeWrite(w, r, &input.IdempotencyKey, &input) {
			return
		}
		secret, err := service.UpsertGatewaySecret(r.Context(), input)
		writeResult(w, secret, err)
	})
	return authenticate(mux, token)
}

func authenticate(next http.Handler, token string) http.Handler {
	want := sha256.Sum256([]byte("Bearer " + token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		got := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if token == "" || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func decodeWrite(w http.ResponseWriter, r *http.Request, idempotencyKey *string, body any) bool {
	*idempotencyKey = r.Header.Get("Idempotency-Key")
	if *idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
		return false
	}
	if err := json.NewDecoder(r.Body).Decode(body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func exactQueryValue(r *http.Request, name string) (string, bool) {
	values := r.URL.Query()
	items, ok := values[name]
	if !ok || len(values) != 1 || len(items) != 1 || items[0] == "" || items[0] != strings.TrimSpace(items[0]) {
		return "", false
	}
	return items[0], true
}

func writeResult(w http.ResponseWriter, body any, err error) {
	if errors.Is(err, fabric.ErrUnsupportedComputePackage) || errors.Is(err, fabric.ErrInvalidStorageSize) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if errors.Is(err, fabric.ErrComputeIdempotencyConflict) || errors.Is(err, fabric.ErrRuntimeIdempotencyConflict) || errors.Is(err, fabric.ErrRuntimeOperationInProgress) || errors.Is(err, fabric.ErrRuntimeOperationFailed) || errors.Is(err, fabric.ErrGatewaySecretIdempotencyConflict) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, body)
}

func writeComputeAllocationResult(w http.ResponseWriter, allocation fabric.ComputeAllocation, err error) {
	if errors.Is(err, fabric.ErrComputeOperationFailed) && allocation.ClaimTerminalEvidence != nil {
		writeJSON(w, http.StatusConflict, allocation)
		return
	}
	writeResult(w, allocation, err)
}

func writeComputeClaimRecoveryResult(w http.ResponseWriter, successStatus int, proof fabric.ComputeClaimRecoveryProof, err error) {
	if err == nil {
		writeJSON(w, successStatus, proof)
		return
	}
	status := http.StatusConflict
	if errors.Is(err, fabric.ErrInvalidComputeClaimRecovery) {
		status = http.StatusBadRequest
	} else if proof.Reason == "provider_describe" || proof.Reason == "iam_rbac" {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, proof)
}

func writeComputePoolHeadTerminalizationResult(w http.ResponseWriter, result fabric.ComputePoolHeadTerminalizationReadback, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, result)
		return
	}
	status := http.StatusConflict
	if errors.Is(err, fabric.ErrInvalidComputePoolHeadTerminalization) {
		status = http.StatusBadRequest
	} else if !errors.Is(err, fabric.ErrComputePoolHeadTerminalizationConflict) && !errors.Is(err, fabric.ErrComputePoolHeadTerminalizationUnavailable) {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]any{"schemaVersion": 1, "status": "blocked", "errorCode": stableComputePoolHeadTerminalizationError(err)})
}

func stableComputePoolHeadTerminalizationError(err error) string {
	switch {
	case errors.Is(err, fabric.ErrInvalidComputePoolHeadTerminalization):
		return fabric.ErrInvalidComputePoolHeadTerminalization.Error()
	case errors.Is(err, fabric.ErrComputePoolHeadTerminalizationConflict):
		return fabric.ErrComputePoolHeadTerminalizationConflict.Error()
	default:
		return fabric.ErrComputePoolHeadTerminalizationUnavailable.Error()
	}
}

func writeWorkspaceActivationTruthResult(w http.ResponseWriter, truth fabric.WorkspaceActivationTruth, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, truth)
		return
	}
	status := http.StatusServiceUnavailable
	if errors.Is(err, fabric.ErrInvalidWorkspaceActivationTruth) {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, truth)
}

func writeWorkspaceLaunchStageReadbackResult(w http.ResponseWriter, proof fabric.WorkspaceLaunchStageReadbackProof, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, proof)
		return
	}
	status := http.StatusServiceUnavailable
	if errors.Is(err, fabric.ErrWorkspaceLaunchStageReadbackInvalid) {
		status = http.StatusBadRequest
	} else if errors.Is(err, fabric.ErrRuntimeOperationNotCurrent) {
		status = http.StatusConflict
	}
	writeJSON(w, status, proof)
}

func writeJobResult(w http.ResponseWriter, status int, body fabric.Job, err error) {
	switch {
	case errors.Is(err, fabric.ErrInvalidJobInput):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, fabric.ErrJobNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, fabric.ErrJobIdempotencyConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, fabric.ErrJobStateConflict), errors.Is(err, fabric.ErrJobLeaseMismatch):
		writeError(w, http.StatusConflict, err.Error())
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		writeJSON(w, status, body)
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
