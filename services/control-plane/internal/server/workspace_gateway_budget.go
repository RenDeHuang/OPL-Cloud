package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

const workspaceGatewayBudgetAction = "workspace.gateway_budget.update"

var (
	errWorkspaceGatewayBudgetInProgress = errors.New("workspace_gateway_budget_update_in_progress")
	errWorkspaceGatewayBudgetState      = errors.New("workspace_gateway_budget_state_failed")
)

type workspaceGatewayBudgetRequest struct {
	QuotaUSDMicros       *int64 `json:"quotaUsdMicros,omitempty"`
	RateLimit5hUSDMicros *int64 `json:"rateLimit5hUsdMicros,omitempty"`
	RateLimit1dUSDMicros *int64 `json:"rateLimit1dUsdMicros,omitempty"`
	RateLimit7dUSDMicros *int64 `json:"rateLimit7dUsdMicros,omitempty"`
	Enabled              *bool  `json:"enabled,omitempty"`
	ResetQuota           *bool  `json:"resetQuota,omitempty"`
	ResetRateLimitUsage  *bool  `json:"resetRateLimitUsage,omitempty"`
}

type workspaceGatewayBudgetOperation struct {
	RequestHash string                        `json:"requestHash"`
	KeyID       int64                         `json:"keyId"`
	Request     workspaceGatewayBudgetRequest `json:"request"`
	AuditEvent  map[string]any                `json:"auditEvent"`
}

func (app *controlPlaneServer) workspaceGatewayBudget(w http.ResponseWriter, r *http.Request, service *controlplane.Service) {
	workspaceID := r.PathValue("workspaceId")
	_, userID, credential, keyID, ok := app.workspaceGatewayBudgetContext(w, r, workspaceID)
	if !ok {
		return
	}
	key, err := service.GatewayUserKey(r.Context(), credential, userID, keyID)
	if err != nil {
		writeWorkspaceGatewayBudgetReadError(w, err)
		return
	}
	if !workspaceGatewayKeyBindingMatches(key, userID, keyID) {
		writeError(w, http.StatusConflict, "workspace_gateway_key_binding_invalid")
		return
	}
	writeWorkspaceGatewayBudget(w, workspaceID, key)
}

func (app *controlPlaneServer) updateWorkspaceGatewayBudget(w http.ResponseWriter, r *http.Request, service *controlplane.Service) {
	input, err := decodeWorkspaceGatewayBudgetRequest(r)
	if err != nil || !validWorkspaceGatewayBudgetRequest(input) {
		writeError(w, http.StatusBadRequest, "invalid_workspace_gateway_budget_request")
		return
	}
	idempotencyKey, ok := requiredMutationKey(w, r)
	if !ok {
		return
	}
	workspaceID := r.PathValue("workspaceId")
	unlock := app.lockWorkspaceGatewayMutation(workspaceID)
	defer unlock()

	user, userID, credential, keyID, ok := app.workspaceGatewayBudgetContext(w, r, workspaceID)
	if !ok {
		return
	}
	accountID := stringValue(user["accountId"])
	operationID := "workspace-gateway-budget-" + stableID(accountID, workspaceID, idempotencyKey)[:18]
	requestHash := gatewayKeyRequestHash("workspace-gateway-budget-v1", accountID, workspaceID+":"+strconv.FormatInt(keyID, 10), input)

	operation, operationStatus, found, err := app.workspaceGatewayBudgetOperation(r.Context(), operationID, accountID, workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "state_read_failed")
		return
	}
	if found {
		if operation.RequestHash != requestHash || operation.KeyID != keyID {
			writeError(w, http.StatusConflict, errIdempotencyConflict.Error())
			return
		}
		app.replayWorkspaceGatewayBudget(w, r, service, credential, userID, operationID, accountID, workspaceID, operationStatus, operation)
		return
	}
	if err := app.admitWorkspaceGatewayBudgetMutation(r.Context(), workspaceID); err != nil {
		writeWorkspaceGatewayBudgetMutationError(w, err)
		return
	}
	current, err := service.GatewayUserKey(r.Context(), credential, userID, keyID)
	if err != nil {
		writeWorkspaceGatewayBudgetReadError(w, err)
		return
	}
	if !workspaceGatewayKeyBindingMatches(current, userID, keyID) {
		writeError(w, http.StatusConflict, "workspace_gateway_key_binding_invalid")
		return
	}
	operation = workspaceGatewayBudgetOperation{RequestHash: requestHash, KeyID: keyID, Request: input}
	operation.AuditEvent = app.newWorkspaceGatewayBudgetAudit(r, operationID, accountID, workspaceID, current, input)
	if err := app.saveWorkspaceGatewayBudgetOperation(r.Context(), operationID, accountID, workspaceID, "started", operation); err != nil {
		writeError(w, http.StatusInternalServerError, "state_persist_failed")
		return
	}
	updated, writeErr := service.UpdateGatewayUserKey(r.Context(), credential, userID, keyID, workspaceGatewayBudgetUpdateInput(input))
	readback, readErr := service.GatewayUserKey(r.Context(), credential, userID, keyID)
	if readErr != nil || !workspaceGatewayBudgetWriteProved(updated, readback, userID, operation, writeErr == nil) {
		_ = app.saveWorkspaceGatewayBudgetOperation(r.Context(), operationID, accountID, workspaceID, "manual_review", operation)
		if writeErr != nil {
			writeGatewaySourceError(w, writeErr)
		} else {
			writeSourceEnvelope(w, http.StatusBadGateway, "sub2api", "unavailable", nil)
		}
		return
	}
	if !app.completeWorkspaceGatewayBudgetOperation(r, operationID, accountID, workspaceID, operation, readback) {
		writeError(w, http.StatusInternalServerError, "state_persist_failed")
		return
	}
	writeWorkspaceGatewayBudget(w, workspaceID, readback)
}

func decodeWorkspaceGatewayBudgetRequest(r *http.Request) (workspaceGatewayBudgetRequest, error) {
	decoder := json.NewDecoder(r.Body)
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return workspaceGatewayBudgetRequest{}, errors.New("invalid workspace gateway budget request")
	}
	input := workspaceGatewayBudgetRequest{}
	seen := map[string]struct{}{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return workspaceGatewayBudgetRequest{}, err
		}
		field, ok := token.(string)
		if !ok {
			return workspaceGatewayBudgetRequest{}, errors.New("invalid workspace gateway budget request")
		}
		if _, duplicate := seen[field]; duplicate {
			return workspaceGatewayBudgetRequest{}, errors.New("duplicate workspace gateway budget field")
		}
		seen[field] = struct{}{}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return workspaceGatewayBudgetRequest{}, errors.New("invalid workspace gateway budget value")
		}
		switch field {
		case "quotaUsdMicros":
			err = json.Unmarshal(raw, &input.QuotaUSDMicros)
		case "rateLimit5hUsdMicros":
			err = json.Unmarshal(raw, &input.RateLimit5hUSDMicros)
		case "rateLimit1dUsdMicros":
			err = json.Unmarshal(raw, &input.RateLimit1dUSDMicros)
		case "rateLimit7dUsdMicros":
			err = json.Unmarshal(raw, &input.RateLimit7dUSDMicros)
		case "enabled":
			err = json.Unmarshal(raw, &input.Enabled)
		case "resetQuota":
			err = json.Unmarshal(raw, &input.ResetQuota)
		case "resetRateLimitUsage":
			err = json.Unmarshal(raw, &input.ResetRateLimitUsage)
		default:
			return workspaceGatewayBudgetRequest{}, errors.New("unknown workspace gateway budget field")
		}
		if err != nil {
			return workspaceGatewayBudgetRequest{}, err
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return workspaceGatewayBudgetRequest{}, errors.New("invalid workspace gateway budget request")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return workspaceGatewayBudgetRequest{}, errors.New("invalid workspace gateway budget request")
	}
	return input, nil
}

func (app *controlPlaneServer) replayWorkspaceGatewayBudget(w http.ResponseWriter, r *http.Request, service *controlplane.Service, credential clients.SessionDelegatedCredential, userID int64, operationID, accountID, workspaceID, operationStatus string, operation workspaceGatewayBudgetOperation) {
	readback, err := service.GatewayUserKey(r.Context(), credential, userID, operation.KeyID)
	if err != nil {
		writeWorkspaceGatewayBudgetReadError(w, err)
		return
	}
	if !workspaceGatewayKeyBindingMatches(readback, userID, operation.KeyID) {
		writeError(w, http.StatusConflict, "workspace_gateway_key_binding_invalid")
		return
	}
	if operationStatus != "succeeded" && !workspaceGatewayBudgetTargetMatches(readback, userID, operation) {
		writeSourceEnvelope(w, http.StatusBadGateway, "sub2api", "unavailable", nil)
		return
	}
	if operationStatus != "succeeded" && !app.completeWorkspaceGatewayBudgetOperation(r, operationID, accountID, workspaceID, operation, readback) {
		writeError(w, http.StatusInternalServerError, "state_persist_failed")
		return
	}
	writeWorkspaceGatewayBudget(w, workspaceID, readback)
}

func (app *controlPlaneServer) workspaceGatewayBudgetContext(w http.ResponseWriter, r *http.Request, workspaceID string) (map[string]any, int64, clients.SessionDelegatedCredential, int64, bool) {
	user, userID, credential, ok := app.gatewayUserContext(w, r)
	if !ok {
		return nil, 0, clients.SessionDelegatedCredential{}, 0, false
	}
	workspace, found, err := app.workspaceForSource(r.Context(), stringValue(user["accountId"]), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "state_read_failed")
		return nil, 0, clients.SessionDelegatedCredential{}, 0, false
	}
	if !found {
		writeError(w, http.StatusNotFound, "workspace_not_found")
		return nil, 0, clients.SessionDelegatedCredential{}, 0, false
	}
	keyID, ok := requiredPositiveInteger(workspace, "workspaceApiKeyId")
	if !ok {
		writeError(w, http.StatusConflict, "workspace_gateway_key_not_provisioned")
		return nil, 0, clients.SessionDelegatedCredential{}, 0, false
	}
	return user, userID, credential, keyID, true
}

func validWorkspaceGatewayBudgetRequest(input workspaceGatewayBudgetRequest) bool {
	if input.QuotaUSDMicros == nil && input.RateLimit5hUSDMicros == nil && input.RateLimit1dUSDMicros == nil && input.RateLimit7dUSDMicros == nil && input.Enabled == nil && input.ResetQuota == nil && input.ResetRateLimitUsage == nil {
		return false
	}
	for _, value := range []*int64{input.QuotaUSDMicros, input.RateLimit5hUSDMicros, input.RateLimit1dUSDMicros, input.RateLimit7dUSDMicros} {
		if value != nil && *value < 0 {
			return false
		}
	}
	return true
}

func workspaceGatewayBudgetUpdateInput(input workspaceGatewayBudgetRequest) clients.Sub2APIUpdateKeyInput {
	return clients.Sub2APIUpdateKeyInput{
		QuotaUSDMicros: input.QuotaUSDMicros, RateLimit5hUSDMicros: input.RateLimit5hUSDMicros,
		RateLimit1dUSDMicros: input.RateLimit1dUSDMicros, RateLimit7dUSDMicros: input.RateLimit7dUSDMicros,
		Enabled: input.Enabled, ResetQuota: input.ResetQuota, ResetRateLimitUsage: input.ResetRateLimitUsage,
	}
}

func workspaceGatewayKeyBindingMatches(key clients.Sub2APIWorkspaceKey, userID, keyID int64) bool {
	return key.ID == keyID && key.UserID == userID
}

func workspaceGatewayBudgetTargetMatches(key clients.Sub2APIWorkspaceKey, userID int64, operation workspaceGatewayBudgetOperation) bool {
	return workspaceGatewayBudgetPolicyMatches(key, userID, operation) && workspaceGatewayBudgetResetMatches(key, operation.Request)
}

func workspaceGatewayBudgetPolicyMatches(key clients.Sub2APIWorkspaceKey, userID int64, operation workspaceGatewayBudgetOperation) bool {
	if !workspaceGatewayKeyBindingMatches(key, userID, operation.KeyID) {
		return false
	}
	input := operation.Request
	if input.QuotaUSDMicros != nil && key.QuotaUSDMicros != *input.QuotaUSDMicros ||
		input.RateLimit5hUSDMicros != nil && key.RateLimit5hUSDMicros != *input.RateLimit5hUSDMicros ||
		input.RateLimit1dUSDMicros != nil && key.RateLimit1dUSDMicros != *input.RateLimit1dUSDMicros ||
		input.RateLimit7dUSDMicros != nil && key.RateLimit7dUSDMicros != *input.RateLimit7dUSDMicros {
		return false
	}
	if input.Enabled != nil && (*input.Enabled && key.Status != "active" || !*input.Enabled && key.Status != "disabled") {
		return false
	}
	return true
}

func workspaceGatewayBudgetResetMatches(key clients.Sub2APIWorkspaceKey, input workspaceGatewayBudgetRequest) bool {
	return (input.ResetQuota == nil || !*input.ResetQuota || key.QuotaUsedUSDMicros == 0) &&
		(input.ResetRateLimitUsage == nil || !*input.ResetRateLimitUsage || key.Usage5hUSDMicros == 0 && key.Usage1dUSDMicros == 0 && key.Usage7dUSDMicros == 0)
}

func workspaceGatewayBudgetWriteProved(updated, readback clients.Sub2APIWorkspaceKey, userID int64, operation workspaceGatewayBudgetOperation, writeSucceeded bool) bool {
	if !workspaceGatewayBudgetPolicyMatches(readback, userID, operation) {
		return false
	}
	input := operation.Request
	resetRequested := input.ResetQuota != nil && *input.ResetQuota || input.ResetRateLimitUsage != nil && *input.ResetRateLimitUsage
	if !resetRequested || workspaceGatewayBudgetResetMatches(readback, input) {
		return true
	}
	return writeSucceeded && workspaceGatewayBudgetPolicyMatches(updated, userID, operation) && workspaceGatewayBudgetResetMatches(updated, input)
}

func workspaceGatewayBudgetProjection(workspaceID string, key clients.Sub2APIWorkspaceKey) map[string]any {
	var updatedAt any
	if !key.UpdatedAt.IsZero() {
		updatedAt = key.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	return map[string]any{
		"workspaceId": workspaceID, "keyId": strconv.FormatInt(key.ID, 10), "status": key.Status,
		"quotaUsdMicros": strconv.FormatInt(key.QuotaUSDMicros, 10), "quotaUsedUsdMicros": strconv.FormatInt(key.QuotaUsedUSDMicros, 10),
		"rateLimit5hUsdMicros": strconv.FormatInt(key.RateLimit5hUSDMicros, 10), "rateLimit1dUsdMicros": strconv.FormatInt(key.RateLimit1dUSDMicros, 10),
		"rateLimit7dUsdMicros": strconv.FormatInt(key.RateLimit7dUSDMicros, 10), "usage5hUsdMicros": strconv.FormatInt(key.Usage5hUSDMicros, 10),
		"usage1dUsdMicros": strconv.FormatInt(key.Usage1dUSDMicros, 10), "usage7dUsdMicros": strconv.FormatInt(key.Usage7dUSDMicros, 10),
		"enabled": key.Status == "active", "updatedAt": updatedAt,
	}
}

func writeWorkspaceGatewayBudget(w http.ResponseWriter, workspaceID string, key clients.Sub2APIWorkspaceKey) {
	writeSourceEnvelope(w, http.StatusOK, "sub2api", "available", workspaceGatewayBudgetProjection(workspaceID, key))
}

func writeWorkspaceGatewayBudgetReadError(w http.ResponseWriter, err error) {
	if errors.Is(err, clients.ErrSub2APIKeyNotFound) {
		writeError(w, http.StatusConflict, "workspace_gateway_key_binding_invalid")
		return
	}
	writeGatewaySourceError(w, err)
}

func (app *controlPlaneServer) admitWorkspaceGatewayBudgetMutation(ctx context.Context, workspaceID string) error {
	rotations, err := queryRuntimeOperations(ctx, app.tables, runtimeOperationQuery{WorkspaceID: workspaceID, Action: "workspace.gateway_key.rotate", ExcludedStatuses: []string{"succeeded"}})
	if err != nil {
		return errWorkspaceGatewayBudgetState
	}
	if len(rotations) != 0 {
		return errWorkspaceKeyRotationInProgress
	}
	operations, err := queryRuntimeOperations(ctx, app.tables, runtimeOperationQuery{WorkspaceID: workspaceID, Action: workspaceGatewayBudgetAction, ExcludedStatuses: []string{"succeeded"}})
	if err != nil {
		return errWorkspaceGatewayBudgetState
	}
	if len(operations) != 0 {
		return errWorkspaceGatewayBudgetInProgress
	}
	return nil
}

func writeWorkspaceGatewayBudgetMutationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errWorkspaceKeyRotationInProgress), errors.Is(err, errWorkspaceGatewayBudgetInProgress):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "state_read_failed")
	}
}

func (app *controlPlaneServer) workspaceGatewayBudgetOperation(ctx context.Context, operationID, accountID, workspaceID string) (workspaceGatewayBudgetOperation, string, bool, error) {
	row, found, err := app.tables.GetRuntimeOperation(ctx, operationID)
	if err != nil || !found {
		return workspaceGatewayBudgetOperation{}, "", false, err
	}
	if stringValue(row["accountId"]) != accountID || stringValue(row["workspaceId"]) != workspaceID || stringValue(row["action"]) != workspaceGatewayBudgetAction {
		return workspaceGatewayBudgetOperation{}, "", false, errWorkspaceGatewayBudgetState
	}
	status := stringValue(row["status"])
	if status != "started" && status != "manual_review" && status != "succeeded" {
		return workspaceGatewayBudgetOperation{}, "", false, errWorkspaceGatewayBudgetState
	}
	var operation workspaceGatewayBudgetOperation
	if json.Unmarshal([]byte(stringValue(row["result"])), &operation) != nil || operation.RequestHash == "" || operation.KeyID <= 0 || !validWorkspaceGatewayBudgetRequest(operation.Request) || !validWorkspaceGatewayBudgetAudit(operation.AuditEvent, operationID, accountID, workspaceID) {
		return workspaceGatewayBudgetOperation{}, "", false, errWorkspaceGatewayBudgetState
	}
	return operation, status, true, nil
}

func (app *controlPlaneServer) saveWorkspaceGatewayBudgetOperation(ctx context.Context, operationID, accountID, workspaceID, status string, operation workspaceGatewayBudgetOperation) error {
	payload, err := json.Marshal(operation)
	if err != nil {
		return err
	}
	return app.tables.SaveRuntimeOperation(ctx, map[string]any{
		"id": operationID, "operationId": operationID, "accountId": accountID, "workspaceId": workspaceID,
		"resourceId": workspaceID, "resourceKind": "workspace_gateway_budget", "action": workspaceGatewayBudgetAction,
		"provider": "sub2api", "status": status, "result": string(payload),
	})
}

func (app *controlPlaneServer) completeWorkspaceGatewayBudgetOperation(r *http.Request, operationID, accountID, workspaceID string, operation workspaceGatewayBudgetOperation, readback clients.Sub2APIWorkspaceKey) bool {
	event := cloneMap(operation.AuditEvent)
	event["after"] = workspaceGatewayBudgetProjection(workspaceID, readback)
	event["result"] = "succeeded"
	exists, err := app.auditIdentityExists(r.Context(), accountID, event)
	if err != nil || !exists && app.tables.SaveAuditEvent(r.Context(), event) != nil {
		return false
	}
	return app.saveWorkspaceGatewayBudgetOperation(r.Context(), operationID, accountID, workspaceID, "succeeded", operation) == nil
}

func (app *controlPlaneServer) newWorkspaceGatewayBudgetAudit(r *http.Request, operationID, accountID, workspaceID string, current clients.Sub2APIWorkspaceKey, input workspaceGatewayBudgetRequest) map[string]any {
	event := app.auditEvent(r, workspaceGatewayBudgetAction, "workspace_gateway_budget", workspaceID, accountID,
		workspaceGatewayBudgetProjection(workspaceID, current), map[string]any{"request": workspaceGatewayBudgetRequestProjection(input)}, "started")
	event["id"] = "audit-" + stableID(workspaceGatewayBudgetAction, operationID)[:12]
	return event
}

func workspaceGatewayBudgetRequestProjection(input workspaceGatewayBudgetRequest) map[string]any {
	request := map[string]any{}
	for field, value := range map[string]*int64{
		"quotaUsdMicros": input.QuotaUSDMicros, "rateLimit5hUsdMicros": input.RateLimit5hUSDMicros,
		"rateLimit1dUsdMicros": input.RateLimit1dUSDMicros, "rateLimit7dUsdMicros": input.RateLimit7dUSDMicros,
	} {
		if value != nil {
			request[field] = strconv.FormatInt(*value, 10)
		}
	}
	for field, value := range map[string]*bool{
		"enabled": input.Enabled, "resetQuota": input.ResetQuota, "resetRateLimitUsage": input.ResetRateLimitUsage,
	} {
		if value != nil {
			request[field] = *value
		}
	}
	return request
}

func validWorkspaceGatewayBudgetAudit(event map[string]any, operationID, accountID, workspaceID string) bool {
	return stringValue(event["id"]) == "audit-"+stableID(workspaceGatewayBudgetAction, operationID)[:12] &&
		stringValue(event["action"]) == workspaceGatewayBudgetAction && stringValue(event["resourceKind"]) == "workspace_gateway_budget" &&
		stringValue(event["resourceId"]) == workspaceID && stringValue(event["actorAccountId"]) == accountID &&
		stringValue(event["targetAccountId"]) == accountID && stringValue(event["actorUserId"]) != "" && stringValue(event["createdAt"]) != ""
}

func (app *controlPlaneServer) auditIdentityExists(ctx context.Context, accountID string, expected map[string]any) (bool, error) {
	events, err := app.tables.ListAuditEvents(ctx, accountID)
	if err != nil {
		return false, err
	}
	for _, event := range events {
		if stringValue(event["id"]) != stringValue(expected["id"]) {
			continue
		}
		for _, field := range []string{"action", "actorAccountId", "actorRole", "actorUserId", "createdAt", "id", "ipAddress", "resourceId", "resourceKind", "targetAccountId", "userAgent"} {
			if stringValue(event[field]) != stringValue(expected[field]) {
				return false, errWorkspaceGatewayBudgetState
			}
		}
		return true, nil
	}
	return false, nil
}
