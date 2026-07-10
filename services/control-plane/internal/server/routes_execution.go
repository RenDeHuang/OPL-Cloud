package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/controlplane"
)

var errExecutionNotFound = errors.New("execution_request_not_found")

func registerExecutionRoutes(mux *http.ServeMux, app *controlPlaneServer, service *controlplane.Service) {
	// ponytail: admin-only until organization membership authorization is enforced for this execution boundary.
	mux.HandleFunc("POST /api/projects", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		organizationID := stringField(input, "organizationId", "")
		workspaceID := stringField(input, "workspaceId", "")
		if organizationID == "" || workspaceID == "" {
			writeError(w, http.StatusBadRequest, "project_identity_required")
			return
		}
		key, ok := executionMutationKey(w, r)
		if !ok {
			return
		}
		row := map[string]any{
			"id":             "project-" + stableID("project", key)[:18],
			"projectId":      "project-" + stableID("project", key)[:18],
			"kind":           "project",
			"organizationId": organizationID,
			"workspaceId":    workspaceID,
			"localAliasId":   stringField(input, "localAliasId", ""),
			"version":        int64(1),
			"status":         "active",
		}
		if existing, ok := app.projectTaskSyncHead(r.Context(), stringValue(row["id"])); ok {
			if !sameIdentity(existing, row) {
				writeError(w, http.StatusConflict, "idempotency_conflict")
				return
			}
			writeJSON(w, http.StatusOK, existing)
			return
		}
		if err := app.tables.SaveProjectTaskSyncHead(r.Context(), row); err != nil {
			if errors.Is(err, errIdempotencyConflict) {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusCreated, row)
	}))

	mux.HandleFunc("POST /api/projects/{projectId}/tasks", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		projectID := strings.TrimSpace(r.PathValue("projectId"))
		project, ok := app.projectTaskSyncHead(r.Context(), projectID)
		if !ok || stringValue(project["kind"]) != "project" {
			writeError(w, http.StatusNotFound, "project_not_found")
			return
		}
		organizationID := stringField(input, "organizationId", "")
		workspaceID := stringField(input, "workspaceId", "")
		if organizationID == "" || workspaceID == "" || organizationID != stringValue(project["organizationId"]) || workspaceID != stringValue(project["workspaceId"]) {
			writeError(w, http.StatusBadRequest, "task_identity_required")
			return
		}
		key, ok := executionMutationKey(w, r)
		if !ok {
			return
		}
		taskID := "task-" + stableID("task", projectID, key)[:18]
		row := map[string]any{
			"id":             taskID,
			"taskId":         taskID,
			"kind":           "task",
			"organizationId": organizationID,
			"workspaceId":    workspaceID,
			"projectId":      projectID,
			"localAliasId":   stringField(input, "localAliasId", ""),
			"version":        int64(1),
			"status":         "draft",
		}
		if existing, ok := app.projectTaskSyncHead(r.Context(), taskID); ok {
			if !sameIdentity(existing, row) {
				writeError(w, http.StatusConflict, "idempotency_conflict")
				return
			}
			writeJSON(w, http.StatusOK, existing)
			return
		}
		if err := app.tables.SaveProjectTaskSyncHead(r.Context(), row); err != nil {
			if errors.Is(err, errIdempotencyConflict) {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusCreated, row)
	}))

	mux.HandleFunc("POST /api/execution-requests", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		projectID := stringField(input, "projectId", "")
		taskID := stringField(input, "taskId", "")
		project, projectOK := app.projectTaskSyncHead(r.Context(), projectID)
		task, taskOK := app.projectTaskSyncHead(r.Context(), taskID)
		if !projectOK || !taskOK || stringValue(task["projectId"]) != projectID {
			writeError(w, http.StatusBadRequest, "project_task_identity_invalid")
			return
		}
		organizationID := stringField(input, "organizationId", "")
		workspaceID := stringField(input, "workspaceId", "")
		if organizationID == "" || workspaceID == "" || organizationID != stringValue(project["organizationId"]) || workspaceID != stringValue(project["workspaceId"]) {
			writeError(w, http.StatusBadRequest, "execution_identity_required")
			return
		}
		key, ok := executionMutationKey(w, r)
		if !ok {
			return
		}
		requestID := "request-" + stableID("request", key)[:18]
		if existing, ok := app.executionRequest(r.Context(), requestID); ok {
			if stringValue(existing["organizationId"]) != organizationID || stringValue(existing["workspaceId"]) != workspaceID || stringValue(existing["projectId"]) != projectID || stringValue(existing["taskId"]) != taskID || stringValue(existing["environmentRef"]) != stringField(input, "environmentRef", "") {
				writeError(w, http.StatusConflict, "idempotency_conflict")
				return
			}
			writeJSON(w, http.StatusOK, existing)
			return
		}
		row := map[string]any{
			"id":             requestID,
			"requestId":      requestID,
			"organizationId": organizationID,
			"workspaceId":    workspaceID,
			"projectId":      projectID,
			"taskId":         taskID,
			"actorUserId":    app.sessionUserID(r),
			"approvalId":     "",
			"approvalStatus": "pending",
			"status":         "awaiting_approval",
			"environmentRef": stringField(input, "environmentRef", ""),
			"idempotencyKey": key,
			"version":        int64(1),
		}
		if err := app.tables.SaveExecutionRequest(r.Context(), row); err != nil {
			if errors.Is(err, errIdempotencyConflict) {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusCreated, row)
	}))

	mux.HandleFunc("POST /api/execution-requests/{requestId}/approve", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := executionMutationKey(w, r); !ok {
			return
		}
		requestID := strings.TrimSpace(r.PathValue("requestId"))
		row, ok := app.executionRequest(r.Context(), requestID)
		if !ok {
			writeError(w, http.StatusNotFound, errExecutionNotFound.Error())
			return
		}
		if stringValue(row["approvalStatus"]) == "approved" {
			writeJSON(w, http.StatusOK, row)
			return
		}
		row["approvalId"] = "approval-" + stableID("approval", requestID)[:18]
		row["approvalStatus"] = "approved"
		row["approvedBy"] = app.sessionUserID(r)
		row["approvedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
		row["status"] = "approved"
		row["version"] = int64(numberField(row, "version", 1)) + 1
		if err := app.tables.SaveExecutionRequest(r.Context(), row); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, row)
	}))

	mux.HandleFunc("POST /api/execution-requests/{requestId}/execute", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		key, ok := executionMutationKey(w, r)
		if !ok {
			return
		}
		requestID := strings.TrimSpace(r.PathValue("requestId"))
		row, ok := app.executionRequest(r.Context(), requestID)
		if !ok {
			writeError(w, http.StatusNotFound, errExecutionNotFound.Error())
			return
		}
		if stringValue(row["approvalStatus"]) != "approved" {
			writeError(w, http.StatusConflict, "approval_required")
			return
		}
		if stringValue(row["jobId"]) != "" {
			writeJSON(w, http.StatusOK, row)
			return
		}
		result, err := service.Execute(r.Context(), controlplane.ExecuteInput{
			OrganizationID: stringValue(row["organizationId"]),
			WorkspaceID:    stringValue(row["workspaceId"]),
			ProjectID:      stringValue(row["projectId"]),
			TaskID:         stringValue(row["taskId"]),
			RequestID:      requestID,
			ApprovalID:     stringValue(row["approvalId"]),
			EnvironmentRef: stringValue(row["environmentRef"]),
		}, key)
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		row["jobId"] = result.JobID
		row["receiptId"] = result.ReceiptID
		row["continuationId"] = result.ContinuationID
		row["status"] = result.Status
		row["version"] = int64(numberField(row, "version", 1)) + 1
		if err := app.tables.SaveExecutionRequest(r.Context(), row); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusAccepted, row)
	}))

	mux.HandleFunc("GET /api/execution-requests/{requestId}", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		row, ok := app.executionRequest(r.Context(), strings.TrimSpace(r.PathValue("requestId")))
		if !ok {
			writeError(w, http.StatusNotFound, errExecutionNotFound.Error())
			return
		}
		writeJSON(w, http.StatusOK, row)
	}))
}

func (app *controlPlaneServer) projectTaskSyncHead(ctx context.Context, id string) (map[string]any, bool) {
	rows, err := app.tables.ListProjectTaskSyncHeads(ctx)
	if err != nil {
		return nil, false
	}
	for _, row := range rows {
		if stringValue(row["id"]) == id {
			return row, true
		}
	}
	return nil, false
}

func (app *controlPlaneServer) executionRequest(ctx context.Context, id string) (map[string]any, bool) {
	rows, err := app.tables.ListExecutionRequests(ctx)
	if err != nil {
		return nil, false
	}
	for _, row := range rows {
		if stringValue(row["id"]) == id {
			return row, true
		}
	}
	return nil, false
}

func sameIdentity(left map[string]any, right map[string]any) bool {
	for _, key := range []string{"kind", "organizationId", "workspaceId", "projectId", "localAliasId"} {
		if stringValue(left[key]) != stringValue(right[key]) {
			return false
		}
	}
	return true
}

func executionMutationKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
		return "", false
	}
	return key, true
}
