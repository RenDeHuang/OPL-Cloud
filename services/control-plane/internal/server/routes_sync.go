package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxSyncPageSize = 100

func registerSyncRoutes(mux *http.ServeMux, app *controlPlaneServer) {
	mux.HandleFunc("POST /api/workspaces/{workspaceId}/sync/mutations", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		workspaceID := strings.TrimSpace(r.PathValue("workspaceId"))
		input := decodeJSON(r)
		identity, ok := app.syncIdentity(r, workspaceID, input)
		if !ok {
			writeError(w, http.StatusBadRequest, "sync_identity_invalid")
			return
		}
		if !app.authorizeOrganization(w, r, stringValue(identity["organizationId"])) {
			return
		}
		if stringField(input, "organizationId", "") != stringValue(identity["organizationId"]) {
			writeError(w, http.StatusBadRequest, "sync_organization_invalid")
			return
		}
		key, ok := executionMutationKey(w, r)
		if !ok {
			return
		}
		operationID := stringField(input, "operationId", "")
		clientID := stringField(input, "clientId", "")
		operation := stringField(input, "operation", "")
		occurredAt := stringField(input, "occurredAt", "")
		baseVersion := int64(numberField(input, "baseVersion", 0))
		payload, payloadOK := input["payload"].(map[string]any)
		if operationID == "" || clientID == "" || occurredAt == "" || baseVersion < 1 || !payloadOK || (operation != "append" && operation != "replace") {
			writeError(w, http.StatusBadRequest, "sync_mutation_invalid")
			return
		}
		if _, err := time.Parse(time.RFC3339, occurredAt); err != nil {
			writeError(w, http.StatusBadRequest, "sync_occurred_at_invalid")
			return
		}
		requestBody, _ := json.Marshal(input)
		requestHash := stableID(string(requestBody))

		app.mu.Lock()
		defer app.mu.Unlock()
		events, err := app.tables.ListWorkspaceSyncEvents(r.Context(), workspaceID, 0, 0)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		for _, event := range events {
			if stringValue(event["idempotencyKey"]) != key {
				continue
			}
			if stringValue(event["requestHash"]) != requestHash {
				writeError(w, http.StatusConflict, errIdempotencyConflict.Error())
				return
			}
			status := http.StatusOK
			if stringValue(event["status"]) == "conflict" {
				status = http.StatusConflict
			}
			writeJSON(w, status, event)
			return
		}
		currentVersion, currentPayload := latestSyncEntityState(events, input)
		if operation == "replace" && baseVersion != currentVersion {
			event := map[string]any{
				"id":             "mutation-" + stableID(workspaceID, key)[:18],
				"operationId":    operationID,
				"workspaceId":    workspaceID,
				"cursor":         nextSyncCursor(events),
				"entityKind":     stringValue(identity["kind"]),
				"projectId":      stringValue(identity["projectId"]),
				"taskId":         stringValue(identity["taskId"]),
				"clientId":       clientID,
				"actorUserId":    app.sessionUserID(r),
				"baseVersion":    baseVersion,
				"serverVersion":  currentVersion,
				"operation":      operation,
				"status":         "conflict",
				"payload":        map[string]any{"current": currentPayload, "incoming": cloneMap(payload)},
				"contentDigest":  stringField(input, "contentDigest", ""),
				"idempotencyKey": key,
				"requestHash":    requestHash,
				"conflictId":     "conflict-" + stableID(workspaceID, operationID)[:18],
				"occurredAt":     occurredAt,
			}
			if err := app.tables.SaveWorkspaceSyncEvent(r.Context(), event); err != nil {
				if errors.Is(err, errIdempotencyConflict) {
					writeError(w, http.StatusConflict, err.Error())
					return
				}
				writeError(w, http.StatusInternalServerError, "state_persist_failed")
				return
			}
			writeJSON(w, http.StatusConflict, event)
			return
		}
		cursor := nextSyncCursor(events)
		event := map[string]any{
			"id":             "mutation-" + stableID(workspaceID, key)[:18],
			"workspaceId":    workspaceID,
			"cursor":         cursor,
			"entityKind":     stringValue(identity["kind"]),
			"projectId":      stringValue(identity["projectId"]),
			"taskId":         stringValue(identity["taskId"]),
			"clientId":       clientID,
			"actorUserId":    app.sessionUserID(r),
			"baseVersion":    baseVersion,
			"serverVersion":  currentVersion + 1,
			"operation":      operation,
			"status":         "accepted",
			"payload":        cloneMap(payload),
			"contentDigest":  stringField(input, "contentDigest", ""),
			"idempotencyKey": key,
			"requestHash":    requestHash,
			"occurredAt":     occurredAt,
			"operationId":    operationID,
		}
		if err := app.tables.SaveWorkspaceSyncEvent(r.Context(), event); err != nil {
			if errors.Is(err, errIdempotencyConflict) {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusCreated, event)
	}))

	mux.HandleFunc("GET /api/workspaces/{workspaceId}/sync/changes", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		workspaceID := strings.TrimSpace(r.PathValue("workspaceId"))
		organizationID, ok := app.syncWorkspaceOrganization(r, workspaceID)
		if !ok {
			writeError(w, http.StatusNotFound, "sync_workspace_not_found")
			return
		}
		if !app.authorizeOrganization(w, r, organizationID) {
			return
		}
		after, err := strconv.ParseInt(firstNonEmpty(r.URL.Query().Get("after"), "0"), 10, 64)
		if err != nil || after < 0 {
			writeError(w, http.StatusBadRequest, "sync_cursor_invalid")
			return
		}
		limit, err := strconv.Atoi(firstNonEmpty(r.URL.Query().Get("limit"), "50"))
		if err != nil || limit < 1 || limit > maxSyncPageSize {
			writeError(w, http.StatusBadRequest, "sync_limit_invalid")
			return
		}
		changes, err := app.tables.ListWorkspaceSyncEvents(r.Context(), workspaceID, after, limit+1)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		hasMore := len(changes) > limit
		if hasMore {
			changes = changes[:limit]
		}
		nextCursor := after
		if len(changes) > 0 {
			nextCursor = int64(numberField(changes[len(changes)-1], "cursor", float64(after)))
		}
		writeJSON(w, http.StatusOK, map[string]any{"changes": changes, "nextCursor": nextCursor, "hasMore": hasMore})
	}))

	mux.HandleFunc("POST /api/workspaces/{workspaceId}/sync/conflicts/{conflictId}/resolve", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		workspaceID := strings.TrimSpace(r.PathValue("workspaceId"))
		conflictID := strings.TrimSpace(r.PathValue("conflictId"))
		organizationID, ok := app.syncWorkspaceOrganization(r, workspaceID)
		if !ok {
			writeError(w, http.StatusNotFound, "sync_workspace_not_found")
			return
		}
		if !app.authorizeOrganization(w, r, organizationID) {
			return
		}
		key, ok := executionMutationKey(w, r)
		if !ok {
			return
		}
		input := decodeJSON(r)
		operationID := stringField(input, "operationId", "")
		clientID := stringField(input, "clientId", "")
		resolution := stringField(input, "resolution", "")
		occurredAt := stringField(input, "occurredAt", "")
		baseVersion := int64(numberField(input, "baseVersion", 0))
		if stringField(input, "organizationId", "") != organizationID || conflictID == "" || operationID == "" || clientID == "" || baseVersion < 1 || (resolution != "accept_current" && resolution != "accept_incoming") {
			writeError(w, http.StatusBadRequest, "sync_conflict_resolution_invalid")
			return
		}
		if _, err := time.Parse(time.RFC3339, occurredAt); err != nil {
			writeError(w, http.StatusBadRequest, "sync_occurred_at_invalid")
			return
		}
		requestBody, _ := json.Marshal(input)
		requestHash := stableID(string(requestBody))

		app.mu.Lock()
		defer app.mu.Unlock()
		events, err := app.tables.ListWorkspaceSyncEvents(r.Context(), workspaceID, 0, 0)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		var conflict map[string]any
		for _, event := range events {
			if stringValue(event["idempotencyKey"]) == key {
				if stringValue(event["requestHash"]) != requestHash {
					writeError(w, http.StatusConflict, errIdempotencyConflict.Error())
					return
				}
				writeJSON(w, http.StatusOK, event)
				return
			}
			if stringValue(event["conflictId"]) != conflictID {
				continue
			}
			if stringValue(event["status"]) == "resolved" {
				writeError(w, http.StatusConflict, "sync_conflict_already_resolved")
				return
			}
			if stringValue(event["status"]) == "conflict" {
				conflict = event
			}
		}
		if conflict == nil {
			writeError(w, http.StatusNotFound, "sync_conflict_not_found")
			return
		}
		identity := map[string]any{
			"entityKind": stringValue(conflict["entityKind"]),
			"projectId":  stringValue(conflict["projectId"]),
			"taskId":     stringValue(conflict["taskId"]),
		}
		currentVersion, _ := latestSyncEntityState(events, identity)
		if baseVersion != currentVersion {
			writeError(w, http.StatusConflict, "sync_resolution_version_conflict")
			return
		}
		conflictPayload := mapField(conflict, "payload")
		resolvedPayload := mapField(conflictPayload, "current")
		if resolution == "accept_incoming" {
			resolvedPayload = mapField(conflictPayload, "incoming")
		}
		event := map[string]any{
			"id":             "mutation-" + stableID(workspaceID, key)[:18],
			"operationId":    operationID,
			"workspaceId":    workspaceID,
			"cursor":         nextSyncCursor(events),
			"entityKind":     stringValue(conflict["entityKind"]),
			"projectId":      stringValue(conflict["projectId"]),
			"taskId":         stringValue(conflict["taskId"]),
			"clientId":       clientID,
			"actorUserId":    app.sessionUserID(r),
			"baseVersion":    baseVersion,
			"serverVersion":  currentVersion + 1,
			"operation":      "resolve_conflict",
			"status":         "resolved",
			"payload":        resolvedPayload,
			"contentDigest":  stringValue(conflict["contentDigest"]),
			"idempotencyKey": key,
			"requestHash":    requestHash,
			"conflictId":     conflictID,
			"occurredAt":     occurredAt,
		}
		if err := app.tables.SaveWorkspaceSyncEvent(r.Context(), event); err != nil {
			if errors.Is(err, errIdempotencyConflict) {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusCreated, event)
	}))
}

func (app *controlPlaneServer) syncIdentity(r *http.Request, workspaceID string, input map[string]any) (map[string]any, bool) {
	entityKind := stringField(input, "entityKind", "")
	projectID := stringField(input, "projectId", "")
	taskID := stringField(input, "taskId", "")
	if entityKind != "project" && entityKind != "task" {
		return nil, false
	}
	id := projectID
	if entityKind == "task" {
		id = taskID
	}
	identity, ok := app.projectTaskSyncHead(r.Context(), id)
	if !ok || stringValue(identity["kind"]) != entityKind || stringValue(identity["workspaceId"]) != workspaceID {
		return nil, false
	}
	if entityKind == "task" && stringValue(identity["projectId"]) != projectID {
		return nil, false
	}
	identity = cloneMap(identity)
	identity["projectId"] = projectID
	identity["taskId"] = taskID
	return identity, true
}

func (app *controlPlaneServer) syncWorkspaceOrganization(r *http.Request, workspaceID string) (string, bool) {
	heads, err := app.tables.ListProjectTaskSyncHeads(r.Context())
	if err != nil {
		return "", false
	}
	for _, head := range heads {
		if stringValue(head["workspaceId"]) == workspaceID && stringValue(head["organizationId"]) != "" {
			return stringValue(head["organizationId"]), true
		}
	}
	return "", false
}

func latestSyncEntityState(events []map[string]any, input map[string]any) (int64, map[string]any) {
	version := int64(1)
	payload := map[string]any{}
	for _, event := range events {
		if stringValue(event["status"]) != "accepted" && stringValue(event["status"]) != "resolved" {
			continue
		}
		if stringValue(event["entityKind"]) != stringField(input, "entityKind", "") || stringValue(event["projectId"]) != stringField(input, "projectId", "") || stringValue(event["taskId"]) != stringField(input, "taskId", "") {
			continue
		}
		if candidate := int64(numberField(event, "serverVersion", 1)); candidate > version {
			version = candidate
			payload = mapField(event, "payload")
		}
	}
	return version, payload
}

func nextSyncCursor(events []map[string]any) int64 {
	latest := int64(0)
	for _, event := range events {
		if cursor := int64(numberField(event, "cursor", 0)); cursor > latest {
			latest = cursor
		}
	}
	// ponytail: one writer replica today; use a database sequence before scaling Control Plane writers.
	cursor := time.Now().UTC().UnixMilli() * 1000
	if cursor <= latest {
		return latest + 1
	}
	return cursor
}
