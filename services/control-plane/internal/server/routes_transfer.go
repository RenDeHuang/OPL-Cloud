package server

import (
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

const maxTransferChunkBytes int64 = 4 << 20

func registerTransferRoutes(mux *http.ServeMux, app *controlPlaneServer, service *controlplane.Service) {
	mux.HandleFunc("POST /api/workspaces/{workspaceId}/transfers", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		workspaceID := strings.TrimSpace(r.PathValue("workspaceId"))
		input := decodeJSON(r)
		projectID := stringField(input, "projectId", "")
		identity, ok := app.syncIdentity(r, workspaceID, map[string]any{"entityKind": "project", "projectId": projectID})
		if !ok {
			writeError(w, http.StatusNotFound, "transfer_project_not_found")
			return
		}
		organizationID := stringValue(identity["organizationId"])
		if !app.authorizeOrganization(w, r, organizationID) {
			return
		}
		key, ok := executionMutationKey(w, r)
		if !ok {
			return
		}
		if stringField(input, "organizationId", "") != organizationID {
			writeError(w, http.StatusBadRequest, "transfer_organization_invalid")
			return
		}
		size := numberField(input, "size", -1)
		if size < 0 || size > 1<<40 || math.Trunc(size) != size {
			writeError(w, http.StatusBadRequest, "transfer_size_invalid")
			return
		}
		transfer, err := service.CreateContentTransfer(r.Context(), clients.ContentTransferInput{
			OrganizationID: organizationID, WorkspaceID: workspaceID, ProjectID: projectID,
			Path: stringField(input, "path", ""), Digest: stringField(input, "digest", ""), Size: int64(size),
		}, key)
		if err != nil {
			writeTransferUpstreamError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, transfer)
	}))
	mux.HandleFunc("GET /api/workspaces/{workspaceId}/transfers/{transferId}", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		transfer, ok := authorizedTransfer(w, r, app, service)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, transfer)
	}))
	mux.HandleFunc("PUT /api/workspaces/{workspaceId}/transfers/{transferId}/chunks/{index}", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		transfer, ok := authorizedTransfer(w, r, app, service)
		if !ok {
			return
		}
		index, err := strconv.Atoi(r.PathValue("index"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "transfer_chunk_invalid")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxTransferChunkBytes+1))
		if err != nil || int64(len(body)) > maxTransferChunkBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "transfer_chunk_too_large")
			return
		}
		updated, err := service.PutContentTransferChunk(r.Context(), transfer.TransferID, index, body, r.Header.Get("X-Chunk-SHA256"))
		if err != nil {
			writeTransferUpstreamError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, updated)
	}))
	mux.HandleFunc("POST /api/workspaces/{workspaceId}/transfers/{transferId}/complete", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		transfer, ok := authorizedTransfer(w, r, app, service)
		if !ok {
			return
		}
		completed, err := service.CompleteContentTransfer(r.Context(), transfer.TransferID)
		if err != nil {
			writeTransferUpstreamError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, completed)
	}))
	mux.HandleFunc("GET /api/workspaces/{workspaceId}/contents/{digest}", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		workspaceID := strings.TrimSpace(r.PathValue("workspaceId"))
		organizationID, ok := app.syncWorkspaceOrganization(r, workspaceID)
		if !ok {
			writeError(w, http.StatusNotFound, "sync_workspace_not_found")
			return
		}
		if !app.authorizeOrganization(w, r, organizationID) {
			return
		}
		content, err := service.Content(r.Context(), workspaceID, strings.TrimSpace(r.PathValue("digest")))
		if err != nil {
			writeTransferUpstreamError(w, err)
			return
		}
		if content.WorkspaceID != workspaceID {
			writeError(w, http.StatusNotFound, "content_not_found")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Content-SHA256", content.Digest)
		w.Header().Set("X-Workspace-Path", content.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content.Body)
	}))
}

func authorizedTransfer(w http.ResponseWriter, r *http.Request, app *controlPlaneServer, service *controlplane.Service) (clients.ContentTransfer, bool) {
	workspaceID := strings.TrimSpace(r.PathValue("workspaceId"))
	organizationID, ok := app.syncWorkspaceOrganization(r, workspaceID)
	if !ok {
		writeError(w, http.StatusNotFound, "sync_workspace_not_found")
		return clients.ContentTransfer{}, false
	}
	if !app.authorizeOrganization(w, r, organizationID) {
		return clients.ContentTransfer{}, false
	}
	transfer, err := service.ContentTransfer(r.Context(), strings.TrimSpace(r.PathValue("transferId")))
	if err != nil {
		writeTransferUpstreamError(w, err)
		return clients.ContentTransfer{}, false
	}
	if transfer.WorkspaceID != workspaceID || transfer.OrganizationID != organizationID {
		writeError(w, http.StatusNotFound, "transfer_not_found")
		return clients.ContentTransfer{}, false
	}
	return transfer, true
}

func writeTransferUpstreamError(w http.ResponseWriter, err error) {
	var upstream *clients.FabricHTTPError
	if errors.As(err, &upstream) {
		switch upstream.StatusCode {
		case http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusServiceUnavailable:
			writeError(w, upstream.StatusCode, "fabric_transfer_failed")
			return
		}
	}
	writeUpstreamError(w, err)
}
