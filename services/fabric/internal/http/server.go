package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"opl-cloud/services/fabric/internal/fabric"
)

func NewServer(service *fabric.Service) http.Handler {
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
	mux.HandleFunc("GET /fabric/operations", func(w http.ResponseWriter, r *http.Request) {
		operations, err := service.ListOperations(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, operations)
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
	mux.HandleFunc("POST /fabric/storage-volumes", func(w http.ResponseWriter, r *http.Request) {
		var input fabric.StorageVolumeInput
		if !decodeWrite(w, r, &input.IdempotencyKey, &input) {
			return
		}
		volume, err := service.CreateStorageVolume(r.Context(), input)
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
	mux.HandleFunc("GET /fabric/workspace-runtimes/{workspaceId}/status", func(w http.ResponseWriter, r *http.Request) {
		runtime, err := service.WorkspaceRuntimeStatus(r.Context(), strings.TrimSpace(r.PathValue("workspaceId")))
		writeResult(w, runtime, err)
	})
	return mux
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
