package http

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"opl-cloud/services/fabric/internal/fabric"
)

const fabricCapabilityHeader = "X-OPL-Fabric-Capability"

const maxJSONBodyBytes int64 = 1 << 20

var errRequestBodyTooLarge = errors.New("request body too large")

type ServerAuthConfig struct {
	ControlPlaneToken string
	RunnerToken       string
	CapabilityKey     string
	Now               func() time.Time
}

func NewServerWithAuth(service *fabric.Service, config ServerAuthConfig) http.Handler {
	if config.Now == nil {
		config.Now = time.Now
	}
	return authorizeFabricRequests(newFabricMux(service), config)
}

func newFabricMux(service *fabric.Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /fabric/workspace-launches/preflight", func(w http.ResponseWriter, r *http.Request) {
		var input fabric.WorkspaceLaunchPreflightInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		result, err := service.PreflightWorkspaceLaunch(r.Context(), input)
		writeWorkspaceLaunchResult(w, result, err)
	})
	mux.HandleFunc("POST /fabric/workspace-launches/stages/read", func(w http.ResponseWriter, r *http.Request) {
		var input fabric.WorkspaceLaunchStageInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		result, err := service.ReadWorkspaceLaunchStage(r.Context(), input)
		writeWorkspaceLaunchResult(w, result, err)
	})
	mux.HandleFunc("POST /fabric/workspace-launches/stages/ensure", func(w http.ResponseWriter, r *http.Request) {
		var input fabric.WorkspaceLaunchStageInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if input.Binding.IdempotencyKey == "" || r.Header.Get("Idempotency-Key") != input.Binding.IdempotencyKey {
			writeError(w, http.StatusBadRequest, "workspace launch stage idempotency key mismatch")
			return
		}
		result, err := service.EnsureWorkspaceLaunchStage(r.Context(), input)
		writeWorkspaceLaunchResult(w, result, err)
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
		decoder := json.NewDecoder(r.Body)
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
	return limitRequestBody(mux)
}

type fabricCapabilityClaims struct {
	Version      int    `json:"version"`
	Caller       string `json:"caller"`
	AccountID    string `json:"accountId"`
	WorkspaceID  string `json:"workspaceId"`
	ResourceKind string `json:"resourceKind"`
	ResourceID   string `json:"resourceId"`
	Action       string `json:"action"`
	OperationID  string `json:"operationId"`
	ExpiresAt    int64  `json:"expiresAt"`
	BodySHA256   string `json:"bodySha256"`
}

type fabricMutationScope struct {
	AccountID    string
	WorkspaceID  string
	ResourceKind string
	ResourceID   string
	Action       string
	OperationID  string
}

func authorizeFabricRequests(next http.Handler, config ServerAuthConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		identity := fabricTransportIdentity(r.Header.Get("Authorization"), config)
		if identity == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if identity == "runner" {
			if !runnerJobLeaseRoute(r) {
				writeError(w, http.StatusForbidden, "forbidden")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if !isFabricMutation(r) {
			next.ServeHTTP(w, r)
			return
		}
		body, err := readBoundedBody(r)
		if err != nil {
			if errors.Is(err, errRequestBodyTooLarge) {
				writeError(w, http.StatusRequestEntityTooLarge, "request_body_too_large")
				return
			}
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		scope, ok := fabricMutationScopeForRequest(r, body)
		if !ok {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		if !verifyFabricCapability(r.Header.Get(fabricCapabilityHeader), config.CapabilityKey, scope, body, config.Now()) {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func fabricTransportIdentity(header string, config ServerAuthConfig) string {
	if config.ControlPlaneToken != "" && constantTimeBearerMatch(header, config.ControlPlaneToken) {
		return "control-plane"
	}
	if config.RunnerToken != "" && constantTimeBearerMatch(header, config.RunnerToken) {
		return "runner"
	}
	return ""
}

func constantTimeBearerMatch(header, token string) bool {
	want := sha256.Sum256([]byte("Bearer " + token))
	got := sha256.Sum256([]byte(header))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

func runnerJobLeaseRoute(r *http.Request) bool {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 3 && parts[0] == "fabric" && parts[1] == "jobs" && parts[2] != "" {
		return r.Method == http.MethodGet
	}
	if len(parts) != 4 || parts[0] != "fabric" || parts[1] != "jobs" || parts[2] == "" || r.Method != http.MethodPost {
		return false
	}
	switch parts[3] {
	case "claim", "heartbeat", "complete", "fail":
		return true
	default:
		return false
	}
}

func isFabricMutation(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	if r.URL.Path == "/fabric/compute-allocations" || r.URL.Path == "/fabric/storage-volumes" || r.URL.Path == "/fabric/workspace-runtimes" ||
		r.URL.Path == "/fabric/gateway-secrets" || r.URL.Path == "/fabric/workspace-launches/stages/ensure" {
		return true
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 4 || parts[0] != "fabric" || parts[2] == "" {
		return false
	}
	switch parts[1] + "/" + parts[3] {
	case "compute-allocations/renew", "compute-allocations/destroy", "storage-volumes/renew", "storage-volumes/destroy", "storage-attachments/detach", "workspace-runtimes/destroy", "workspace-runtimes/gateway-secret":
		return true
	default:
		return false
	}
}

func fabricMutationScopeForRequest(r *http.Request, body []byte) (fabricMutationScope, bool) {
	var input map[string]any
	if len(body) == 0 || json.Unmarshal(body, &input) != nil {
		return fabricMutationScope{}, false
	}
	value := func(name string) string {
		result, _ := input[name].(string)
		return strings.TrimSpace(result)
	}
	scope := fabricMutationScope{AccountID: value("accountId"), WorkspaceID: value("workspaceId"), OperationID: strings.TrimSpace(r.Header.Get("Idempotency-Key"))}
	path := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(path, "/")
	switch {
	case r.URL.Path == "/fabric/compute-allocations":
		scope.ResourceKind, scope.ResourceID, scope.Action = "compute_allocation", value("id"), "create_compute_allocation"
	case r.URL.Path == "/fabric/storage-volumes":
		scope.ResourceKind, scope.ResourceID, scope.Action = "storage_volume", value("id"), "create_storage_volume"
	case r.URL.Path == "/fabric/workspace-runtimes":
		scope.ResourceKind, scope.ResourceID, scope.Action = "workspace_runtime", scope.WorkspaceID, "create_workspace_runtime"
		if value("runtimeOperationId") != scope.OperationID {
			scope.Action = "update_workspace_runtime"
		}
	case r.URL.Path == "/fabric/gateway-secrets":
		scope.ResourceKind, scope.ResourceID, scope.Action = "gateway_secret", scope.WorkspaceID, "upsert_gateway_secret"
	case r.URL.Path == "/fabric/workspace-launches/stages/ensure":
		binding, _ := input["binding"].(map[string]any)
		bindingValue := func(name string) string {
			result, _ := binding[name].(string)
			return strings.TrimSpace(result)
		}
		scope.AccountID, scope.WorkspaceID = bindingValue("accountId"), bindingValue("workspaceId")
		scope.ResourceKind, scope.ResourceID, scope.Action = "workspace_launch_stage", bindingValue("expectedResourceBinding"), bindingValue("action")
		scope.OperationID = bindingValue("idempotencyKey")
	case len(parts) == 4 && parts[0] == "fabric" && parts[1] == "compute-allocations" && parts[2] != "":
		scope.ResourceKind, scope.ResourceID = "compute_allocation", parts[2]
		switch parts[3] {
		case "renew":
			scope.Action = "renew_compute_allocation"
		case "destroy":
			scope.Action = "destroy_compute_allocation"
		}
	case len(parts) == 4 && parts[0] == "fabric" && parts[1] == "storage-volumes" && parts[2] != "":
		scope.ResourceKind, scope.ResourceID = "storage_volume", parts[2]
		switch parts[3] {
		case "renew":
			scope.Action = "renew_storage_volume"
		case "destroy":
			scope.Action = "destroy_storage_volume"
		}
	case len(parts) == 4 && parts[0] == "fabric" && parts[1] == "storage-attachments" && parts[2] != "" && parts[3] == "detach":
		scope.ResourceKind, scope.ResourceID, scope.Action = "storage_attachment", parts[2], "detach_storage_attachment"
	case len(parts) == 4 && parts[0] == "fabric" && parts[1] == "workspace-runtimes" && parts[2] != "" && parts[3] == "destroy":
		scope.ResourceKind, scope.ResourceID, scope.Action = "workspace_runtime", parts[2], "destroy_workspace_runtime"
	case len(parts) == 4 && parts[0] == "fabric" && parts[1] == "workspace-runtimes" && parts[2] != "" && parts[3] == "gateway-secret":
		scope.ResourceKind, scope.ResourceID, scope.Action = "workspace_runtime_gateway_secret", parts[2], "bind_workspace_runtime_gateway_secret"
	}
	return scope, scope.AccountID != "" && scope.WorkspaceID != "" && scope.ResourceKind != "" && scope.ResourceID != "" && scope.Action != "" && scope.OperationID != ""
}

func verifyFabricCapability(raw, key string, expected fabricMutationScope, body []byte, now time.Time) bool {
	parts := strings.Split(raw, ".")
	if key == "" || len(parts) != 2 {
		return false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	var claims fabricCapabilityClaims
	if json.Unmarshal(payload, &claims) != nil {
		return false
	}
	digest := sha256.Sum256(body)
	return claims.Version == 1 && claims.Caller == "control-plane" && claims.AccountID == expected.AccountID &&
		claims.WorkspaceID == expected.WorkspaceID && claims.ResourceKind == expected.ResourceKind && claims.ResourceID == expected.ResourceID &&
		claims.Action == expected.Action && claims.OperationID == expected.OperationID && claims.ExpiresAt > now.Unix() &&
		claims.ExpiresAt <= now.Add(2*time.Minute).Unix() && claims.BodySHA256 == hex.EncodeToString(digest[:])
}

func writeWorkspaceLaunchResult(w http.ResponseWriter, result any, err error) {
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, result)
	case errors.Is(err, fabric.ErrLaunchStageBindingNotFound), errors.Is(err, fabric.ErrOperationNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, fabric.ErrWorkspaceLaunchInputInvalid), errors.Is(err, fabric.ErrLaunchStageBindingInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, fabric.ErrLaunchStageBindingConflict):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusServiceUnavailable, err.Error())
	}
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

func limitRequestBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := readBoundedBody(r); err != nil {
			if errors.Is(err, errRequestBodyTooLarge) {
				writeError(w, http.StatusRequestEntityTooLarge, "request_body_too_large")
				return
			}
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func readBoundedBody(r *http.Request) ([]byte, error) {
	reader := r.Body
	if reader == nil {
		reader = http.NoBody
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxJSONBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxJSONBodyBytes {
		return nil, errRequestBodyTooLarge
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	return data, nil
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
