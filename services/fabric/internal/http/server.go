package http

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
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
	mux.HandleFunc("POST /fabric/transfers", func(w http.ResponseWriter, r *http.Request) {
		var input fabric.TransferInput
		if !decodeWrite(w, r, &input.IdempotencyKey, &input) {
			return
		}
		transfer, err := service.CreateTransfer(r.Context(), input)
		writeTransferResult(w, http.StatusCreated, transfer, err)
	})
	mux.HandleFunc("GET /fabric/transfers/{id}", func(w http.ResponseWriter, r *http.Request) {
		transfer, err := service.Transfer(r.Context(), strings.TrimSpace(r.PathValue("id")))
		writeTransferResult(w, http.StatusOK, transfer, err)
	})
	mux.HandleFunc("PUT /fabric/transfers/{id}/chunks/{index}", func(w http.ResponseWriter, r *http.Request) {
		index, err := strconv.Atoi(r.PathValue("index"))
		if err != nil {
			writeError(w, http.StatusBadRequest, fabric.ErrTransferInvalid.Error())
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, (4<<20)+1))
		if err != nil {
			writeError(w, http.StatusBadRequest, fabric.ErrTransferInvalid.Error())
			return
		}
		transfer, err := service.PutTransferChunk(r.Context(), strings.TrimSpace(r.PathValue("id")), index, body, r.Header.Get("X-Chunk-SHA256"))
		writeTransferResult(w, http.StatusOK, transfer, err)
	})
	mux.HandleFunc("POST /fabric/transfers/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
		transfer, err := service.CompleteTransfer(r.Context(), strings.TrimSpace(r.PathValue("id")))
		writeTransferResult(w, http.StatusOK, transfer, err)
	})
	mux.HandleFunc("GET /fabric/contents/{digest}", func(w http.ResponseWriter, r *http.Request) {
		content, err := service.Content(r.Context(), r.Header.Get("X-Workspace-ID"), strings.TrimSpace(r.PathValue("digest")))
		if err != nil {
			writeTransferResult(w, http.StatusOK, fabric.Transfer{}, err)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Content-SHA256", content.Digest)
		w.Header().Set("X-Workspace-ID", content.WorkspaceID)
		w.Header().Set("X-Workspace-Path", content.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content.Body)
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
		writeResult(w, allocation, err)
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

func writeResult(w http.ResponseWriter, body any, err error) {
	if errors.Is(err, fabric.ErrUnsupportedComputePackage) || errors.Is(err, fabric.ErrInvalidStorageSize) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if errors.Is(err, fabric.ErrRuntimeIdempotencyConflict) || errors.Is(err, fabric.ErrRuntimeOperationInProgress) || errors.Is(err, fabric.ErrRuntimeOperationFailed) || errors.Is(err, fabric.ErrGatewaySecretIdempotencyConflict) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, body)
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

func writeTransferResult(w http.ResponseWriter, status int, body fabric.Transfer, err error) {
	switch {
	case errors.Is(err, fabric.ErrTransferInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, fabric.ErrTransferNotFound), errors.Is(err, fabric.ErrContentNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, fabric.ErrTransferChunkConflict), errors.Is(err, fabric.ErrTransferIncomplete), errors.Is(err, fabric.ErrTransferDigestMismatch):
		writeError(w, http.StatusConflict, err.Error())
	case err != nil:
		if strings.HasPrefix(err.Error(), "workspace_content_") {
			log.Printf("workspace transfer failed: %v", err)
		}
		writeError(w, http.StatusServiceUnavailable, err.Error())
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
