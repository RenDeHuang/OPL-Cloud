package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

func registerGatewayRoutes(mux *http.ServeMux, app *controlPlaneServer, service *controlplane.Service) {
	mux.HandleFunc("GET /api/gateway/wallet", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		userID, ok := app.gatewaySub2APIUserID(w, r)
		if !ok {
			return
		}
		balance, err := service.Sub2APIBalance(r.Context(), userID)
		if err != nil {
			writeGatewaySourceError(w, err)
			return
		}
		writeSourceEnvelope(w, http.StatusOK, "sub2api", "available", map[string]any{
			"userId": strconv.FormatInt(balance.UserID, 10), "currency": "USD", "usdMicros": balance.USDMicros, "status": balance.Status,
		})
	}))
	mux.HandleFunc("GET /api/gateway/keys", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		_, userID, credential, ok := app.gatewayUserContext(w, r)
		if !ok {
			return
		}
		keys, err := service.GatewayUserKeys(r.Context(), credential, userID)
		if err != nil {
			writeGatewaySourceError(w, err)
			return
		}
		items := make([]any, 0, len(keys))
		for _, key := range keys {
			items = append(items, gatewayKeySummary(key))
		}
		status := "available"
		if len(items) == 0 {
			status = "empty"
		}
		writeSourceEnvelope(w, http.StatusOK, "sub2api", status, map[string]any{"items": items, "total": len(items), "page": 1, "pageSize": len(items)})
	}))
	mux.HandleFunc("GET /api/gateway/keys/{keyId}", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		app.gatewayKey(w, r, service)
	}))
	mux.HandleFunc("POST /api/gateway/keys", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		app.createGatewayKey(w, r, service)
	}))
	mux.HandleFunc("PATCH /api/gateway/keys/{keyId}", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		app.updateGatewayKey(w, r, service)
	}))
	mux.HandleFunc("DELETE /api/gateway/keys/{keyId}", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		app.deleteGatewayKey(w, r, service)
	}))
	mux.HandleFunc("POST /api/gateway/keys/{keyId}/reveal", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		app.revealGatewayKey(w, r, service)
	}))
	mux.HandleFunc("GET /api/gateway/keys/{keyId}/usage", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		app.gatewayKeyUsage(w, r, service)
	}))
	mux.HandleFunc("GET /api/gateway/keys/{keyId}/usage-summary", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		app.gatewayKeyUsageSummary(w, r, service)
	}))
	mux.HandleFunc("GET /api/gateway/usage-summary", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		app.gatewayAccountUsageSummary(w, r, service)
	}))
	mux.HandleFunc("GET /api/gateway/balance-history", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		userID, ok := app.gatewaySub2APIUserID(w, r)
		if !ok {
			return
		}
		history, err := service.Sub2APIBalanceHistory(r.Context(), userID)
		if err != nil {
			writeGatewaySourceError(w, err)
			return
		}
		items := make([]any, 0, len(history))
		for _, entry := range history {
			var usedAt any
			if entry.UsedAt != nil {
				usedAt = entry.UsedAt.UTC().Format(time.RFC3339Nano)
			}
			items = append(items, map[string]any{
				"type": entry.Type, "valueUsdMicros": entry.ValueUSDMicros, "status": entry.Status,
				"usedAt": usedAt, "createdAt": entry.CreatedAt.UTC().Format(time.RFC3339Nano),
			})
		}
		status := "available"
		if len(items) == 0 {
			status = "empty"
		}
		writeSourceEnvelope(w, http.StatusOK, "sub2api", status, map[string]any{"items": items, "total": len(items)})
	}))
}

type createGatewayKeyRequest struct {
	Name           string `json:"name"`
	QuotaUSDMicros int64  `json:"quotaUsdMicros"`
	ExpiresInDays  *int   `json:"expiresInDays,omitempty"`
}

type updateGatewayKeyRequest struct {
	Name           *string `json:"name,omitempty"`
	QuotaUSDMicros *int64  `json:"quotaUsdMicros,omitempty"`
	Enabled        *bool   `json:"enabled,omitempty"`
}

type gatewayKeyCommandEvidence struct {
	ID             int64  `json:"id"`
	Name           string `json:"name,omitempty"`
	Status         string `json:"status"`
	QuotaUSDMicros int64  `json:"quotaUsdMicros,omitempty"`
	ExpiresAt      string `json:"expiresAt,omitempty"`
}

type gatewayKeyCommandResult struct {
	RequestHash  string                     `json:"requestHash"`
	KeyID        int64                      `json:"keyId,omitempty"`
	TargetStatus string                     `json:"targetStatus"`
	Readback     *gatewayKeyCommandEvidence `json:"readback,omitempty"`
}

func (app *controlPlaneServer) gatewayKey(w http.ResponseWriter, r *http.Request, service *controlplane.Service) {
	_, userID, credential, ok := app.gatewayUserContext(w, r)
	if !ok {
		return
	}
	keyID, ok := gatewayKeyID(w, r)
	if !ok {
		return
	}
	key, err := service.GatewayUserKey(r.Context(), credential, userID, keyID)
	if err != nil {
		writeGatewayUserKeyError(w, err)
		return
	}
	writeSourceEnvelope(w, http.StatusOK, "sub2api", "available", gatewayKeySummary(key))
}

func (app *controlPlaneServer) createGatewayKey(w http.ResponseWriter, r *http.Request, service *controlplane.Service) {
	user, userID, credential, ok := app.gatewayUserContext(w, r)
	if !ok {
		return
	}
	idempotencyKey, ok := requiredMutationKey(w, r)
	if !ok {
		return
	}
	var input createGatewayKeyRequest
	if decodeStrictGatewayRequest(r, &input) != nil {
		writeError(w, http.StatusBadRequest, "invalid_gateway_key_request")
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || reservedWorkspaceKeyName(input.Name) || input.QuotaUSDMicros < 0 || input.ExpiresInDays != nil && *input.ExpiresInDays <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_gateway_key_request")
		return
	}
	accountID := stringValue(user["accountId"])
	operationID := "gateway-key-create-" + stableID(accountID, idempotencyKey)[:18]
	requestHash := stableID("gateway-key-create-v1", accountID, input.Name, strconv.FormatInt(input.QuotaUSDMicros, 10), optionalInt(input.ExpiresInDays))
	result := gatewayKeyCommandResult{RequestHash: requestHash, TargetStatus: "active"}
	unlock := app.lockResource("gateway_key_command", operationID)
	defer unlock()

	existing, found, err := app.gatewayKeyCommand(r.Context(), operationID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "state_read_failed")
		return
	}
	if found {
		if existing.RequestHash != requestHash {
			writeError(w, http.StatusConflict, "idempotency_conflict")
			return
		}
		result = existing
		if result.KeyID > 0 {
			if key, readErr := service.GatewayUserKey(r.Context(), credential, userID, result.KeyID); readErr == nil && key.Name == input.Name && key.QuotaUSDMicros == input.QuotaUSDMicros && key.Status == "active" {
				result.Readback = gatewayKeyEvidence(key)
				if !app.completeGatewayKeyCommand(r, operationID, accountID, operationID, "gateway.key_create", result) {
					writeError(w, http.StatusInternalServerError, "state_persist_failed")
					return
				}
				writeSourceEnvelope(w, http.StatusOK, "sub2api", "available", gatewayKeySummary(key))
				return
			}
		}
	}
	if err := app.saveGatewayKeyCommand(r.Context(), operationID, accountID, operationID, "gateway.key_create", "started", result); err != nil {
		writeError(w, http.StatusInternalServerError, "state_persist_failed")
		return
	}
	key, err := service.CreateGatewayUserKey(r.Context(), credential, userID, clients.Sub2APICreateKeyInput{
		Name: input.Name, QuotaUSDMicros: input.QuotaUSDMicros, ExpiresInDays: input.ExpiresInDays,
	}, idempotencyKey)
	if err != nil {
		_ = app.saveGatewayKeyCommand(r.Context(), operationID, accountID, operationID, "gateway.key_create", "manual_review", result)
		writeGatewaySourceError(w, err)
		return
	}
	result.KeyID = key.ID
	key, err = service.GatewayUserKey(r.Context(), credential, userID, key.ID)
	if err != nil || key.Name != input.Name || key.QuotaUSDMicros != input.QuotaUSDMicros || key.Status != "active" {
		_ = app.saveGatewayKeyCommand(r.Context(), operationID, accountID, operationID, "gateway.key_create", "manual_review", result)
		writeSourceEnvelope(w, http.StatusBadGateway, "sub2api", "unavailable", nil)
		return
	}
	result.Readback = gatewayKeyEvidence(key)
	if !app.completeGatewayKeyCommand(r, operationID, accountID, operationID, "gateway.key_create", result) {
		writeError(w, http.StatusInternalServerError, "state_persist_failed")
		return
	}
	writeSourceEnvelope(w, http.StatusCreated, "sub2api", "available", gatewayKeySummary(key))
}

func (app *controlPlaneServer) updateGatewayKey(w http.ResponseWriter, r *http.Request, service *controlplane.Service) {
	user, userID, credential, ok := app.gatewayUserContext(w, r)
	if !ok {
		return
	}
	keyID, ok := gatewayKeyID(w, r)
	if !ok {
		return
	}
	current, err := service.GatewayUserKey(r.Context(), credential, userID, keyID)
	if err != nil {
		writeGatewayUserKeyError(w, err)
		return
	}
	if reservedWorkspaceKeyName(current.Name) {
		writeError(w, http.StatusConflict, "gateway_key_reserved")
		return
	}
	idempotencyKey, ok := requiredMutationKey(w, r)
	if !ok {
		return
	}
	var input updateGatewayKeyRequest
	if decodeStrictGatewayRequest(r, &input) != nil || input.Name == nil && input.QuotaUSDMicros == nil && input.Enabled == nil {
		writeError(w, http.StatusBadRequest, "invalid_gateway_key_request")
		return
	}
	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if name == "" || reservedWorkspaceKeyName(name) {
			writeError(w, http.StatusBadRequest, "invalid_gateway_key_request")
			return
		}
		input.Name = &name
	}
	if input.QuotaUSDMicros != nil && *input.QuotaUSDMicros < 0 {
		writeError(w, http.StatusBadRequest, "invalid_gateway_key_request")
		return
	}
	target := current
	if input.Name != nil {
		target.Name = *input.Name
	}
	if input.QuotaUSDMicros != nil {
		target.QuotaUSDMicros = *input.QuotaUSDMicros
	}
	if input.Enabled != nil {
		target.Status = "disabled"
		if *input.Enabled {
			target.Status = "active"
		}
	}
	accountID := stringValue(user["accountId"])
	resourceID := strconv.FormatInt(keyID, 10)
	operationID := "gateway-key-update-" + stableID(accountID, idempotencyKey)[:18]
	requestHash := stableID("gateway-key-update-v1", accountID, resourceID, optionalString(input.Name), optionalInt64(input.QuotaUSDMicros), optionalBool(input.Enabled))
	result := gatewayKeyCommandResult{RequestHash: requestHash, KeyID: keyID, TargetStatus: target.Status}
	unlock := app.lockResource("gateway_key_command", operationID)
	defer unlock()

	if existing, found, readErr := app.gatewayKeyCommand(r.Context(), operationID); readErr != nil {
		writeError(w, http.StatusInternalServerError, "state_read_failed")
		return
	} else if found {
		if existing.RequestHash != requestHash {
			writeError(w, http.StatusConflict, "idempotency_conflict")
			return
		}
		readback, readErr := service.GatewayUserKey(r.Context(), credential, userID, keyID)
		if readErr != nil || !gatewayKeyTargetMatches(readback, target) {
			writeSourceEnvelope(w, http.StatusBadGateway, "sub2api", "unavailable", nil)
			return
		}
		existing.Readback = gatewayKeyEvidence(readback)
		if !app.completeGatewayKeyCommand(r, operationID, accountID, resourceID, "gateway.key_update", existing) {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeSourceEnvelope(w, http.StatusOK, "sub2api", "available", gatewayKeySummary(readback))
		return
	}
	if err := app.saveGatewayKeyCommand(r.Context(), operationID, accountID, resourceID, "gateway.key_update", "started", result); err != nil {
		writeError(w, http.StatusInternalServerError, "state_persist_failed")
		return
	}
	_, writeErr := service.UpdateGatewayUserKey(r.Context(), credential, userID, keyID, clients.Sub2APIUpdateKeyInput(input))
	readback, readErr := service.GatewayUserKey(r.Context(), credential, userID, keyID)
	if readErr != nil || !gatewayKeyTargetMatches(readback, target) {
		_ = app.saveGatewayKeyCommand(r.Context(), operationID, accountID, resourceID, "gateway.key_update", "manual_review", result)
		if writeErr != nil {
			writeGatewaySourceError(w, writeErr)
		} else {
			writeSourceEnvelope(w, http.StatusBadGateway, "sub2api", "unavailable", nil)
		}
		return
	}
	result.Readback = gatewayKeyEvidence(readback)
	if !app.completeGatewayKeyCommand(r, operationID, accountID, resourceID, "gateway.key_update", result) {
		writeError(w, http.StatusInternalServerError, "state_persist_failed")
		return
	}
	writeSourceEnvelope(w, http.StatusOK, "sub2api", "available", gatewayKeySummary(readback))
}

func (app *controlPlaneServer) deleteGatewayKey(w http.ResponseWriter, r *http.Request, service *controlplane.Service) {
	user, userID, credential, ok := app.gatewayUserContext(w, r)
	if !ok {
		return
	}
	keyID, ok := gatewayKeyID(w, r)
	if !ok {
		return
	}
	idempotencyKey, ok := requiredMutationKey(w, r)
	if !ok {
		return
	}
	accountID, resourceID := stringValue(user["accountId"]), strconv.FormatInt(keyID, 10)
	operationID := "gateway-key-delete-" + stableID(accountID, idempotencyKey)[:18]
	result := gatewayKeyCommandResult{RequestHash: stableID("gateway-key-delete-v1", accountID, resourceID), KeyID: keyID, TargetStatus: "deleted"}
	unlock := app.lockResource("gateway_key_command", operationID)
	defer unlock()
	if existing, found, readErr := app.gatewayKeyCommand(r.Context(), operationID); readErr != nil {
		writeError(w, http.StatusInternalServerError, "state_read_failed")
		return
	} else if found {
		if existing.RequestHash != result.RequestHash {
			writeError(w, http.StatusConflict, "idempotency_conflict")
			return
		}
		_, readErr := service.GatewayUserKey(r.Context(), credential, userID, keyID)
		if !errors.Is(readErr, clients.ErrSub2APIKeyNotFound) {
			writeSourceEnvelope(w, http.StatusBadGateway, "sub2api", "unavailable", nil)
			return
		}
		if !app.completeGatewayKeyCommand(r, operationID, accountID, resourceID, "gateway.key_delete", existing) {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeSourceEnvelope(w, http.StatusOK, "sub2api", "available", gatewayKeyDeletedResponse(operationID, keyID))
		return
	}
	current, err := service.GatewayUserKey(r.Context(), credential, userID, keyID)
	if err != nil {
		writeGatewayUserKeyError(w, err)
		return
	}
	if reservedWorkspaceKeyName(current.Name) {
		writeError(w, http.StatusConflict, "gateway_key_reserved")
		return
	}
	if err := app.saveGatewayKeyCommand(r.Context(), operationID, accountID, resourceID, "gateway.key_delete", "started", result); err != nil {
		writeError(w, http.StatusInternalServerError, "state_persist_failed")
		return
	}
	writeErr := service.DeleteGatewayUserKey(r.Context(), credential, userID, keyID)
	_, readErr := service.GatewayUserKey(r.Context(), credential, userID, keyID)
	if !errors.Is(readErr, clients.ErrSub2APIKeyNotFound) {
		_ = app.saveGatewayKeyCommand(r.Context(), operationID, accountID, resourceID, "gateway.key_delete", "manual_review", result)
		if writeErr != nil {
			writeGatewaySourceError(w, writeErr)
		} else {
			writeSourceEnvelope(w, http.StatusBadGateway, "sub2api", "unavailable", nil)
		}
		return
	}
	if !app.completeGatewayKeyCommand(r, operationID, accountID, resourceID, "gateway.key_delete", result) {
		writeError(w, http.StatusInternalServerError, "state_persist_failed")
		return
	}
	writeSourceEnvelope(w, http.StatusOK, "sub2api", "available", gatewayKeyDeletedResponse(operationID, keyID))
}

func (app *controlPlaneServer) revealGatewayKey(w http.ResponseWriter, r *http.Request, service *controlplane.Service) {
	user, userID, credential, ok := app.gatewayUserContext(w, r)
	if !ok {
		return
	}
	if stringValue(user["role"]) != "owner" {
		writeError(w, http.StatusForbidden, "gateway_key_reveal_forbidden")
		return
	}
	keyID, ok := gatewayKeyID(w, r)
	if !ok {
		return
	}
	key, err := service.GatewayUserKey(r.Context(), credential, userID, keyID)
	if err != nil {
		writeGatewayUserKeyError(w, err)
		return
	}
	if key.Key == "" {
		writeSourceEnvelope(w, http.StatusBadGateway, "sub2api", "unavailable", nil)
		return
	}
	if err := app.appendAuditEvent(r, "gateway.key_reveal", "gateway_key", strconv.FormatInt(key.ID, 10), stringValue(user["accountId"]), nil, gatewayKeyEvidence(key), "succeeded"); err != nil {
		writeError(w, http.StatusInternalServerError, "state_persist_failed")
		return
	}
	writeSourceEnvelope(w, http.StatusOK, "sub2api", "available", map[string]any{
		"id": strconv.FormatInt(key.ID, 10), "name": key.Name, "status": key.Status, "value": key.Key,
	})
}

func (app *controlPlaneServer) gatewayKeyUsage(w http.ResponseWriter, r *http.Request, service *controlplane.Service) {
	_, userID, credential, ok := app.gatewayUserContext(w, r)
	if !ok {
		return
	}
	keyID, ok := gatewayKeyID(w, r)
	if !ok {
		return
	}
	page, pageSize, ok := gatewayUsagePagination(w, r)
	if !ok {
		return
	}
	usage, err := service.GatewayKeyUsage(r.Context(), credential, userID, keyID, page, pageSize)
	if err != nil {
		writeGatewayUserKeyError(w, err)
		return
	}
	writeGatewayUsagePage(w, usage)
}

func (app *controlPlaneServer) gatewayKeyUsageSummary(w http.ResponseWriter, r *http.Request, service *controlplane.Service) {
	_, userID, credential, ok := app.gatewayUserContext(w, r)
	if !ok {
		return
	}
	keyID, ok := gatewayKeyID(w, r)
	if !ok {
		return
	}
	period, ok := gatewayUsagePeriod(w, r)
	if !ok {
		return
	}
	stats, err := service.GatewayKeyUsageStats(r.Context(), credential, userID, keyID, period)
	if err != nil {
		writeGatewayUserKeyError(w, err)
		return
	}
	writeGatewayUsageStats(w, stats)
}

func (app *controlPlaneServer) gatewayAccountUsageSummary(w http.ResponseWriter, r *http.Request, service *controlplane.Service) {
	userID, ok := app.gatewaySub2APIUserID(w, r)
	if !ok {
		return
	}
	period, ok := gatewayUsagePeriod(w, r)
	if !ok {
		return
	}
	stats, err := service.GatewayAccountUsageStats(r.Context(), userID, period)
	if err != nil {
		writeGatewaySourceError(w, err)
		return
	}
	writeGatewayUsageStats(w, stats)
}

func gatewayKeySummary(key clients.Sub2APIWorkspaceKey) map[string]any {
	var lastUsedAt, expiresAt any
	if key.LastUsedAt != nil {
		lastUsedAt = key.LastUsedAt.UTC().Format(time.RFC3339Nano)
	}
	if key.ExpiresAt != nil {
		expiresAt = key.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	workspace := reservedWorkspaceKeyName(key.Name)
	kind := "general"
	if workspace {
		kind = "workspace"
	}
	return map[string]any{
		"id": strconv.FormatInt(key.ID, 10), "name": key.Name, "kind": kind, "status": key.Status,
		"quotaUsdMicros": key.QuotaUSDMicros, "quotaUsedUsdMicros": key.QuotaUsedUSDMicros,
		"usage5hUsdMicros": key.Usage5hUSDMicros, "usage1dUsdMicros": key.Usage1dUSDMicros,
		"usage7dUsdMicros": key.Usage7dUSDMicros, "lastUsedAt": lastUsedAt, "expiresAt": expiresAt,
		"manageable": !workspace, "deletable": !workspace,
	}
}

func gatewayKeyEvidence(key clients.Sub2APIWorkspaceKey) *gatewayKeyCommandEvidence {
	evidence := &gatewayKeyCommandEvidence{ID: key.ID, Name: key.Name, Status: key.Status, QuotaUSDMicros: key.QuotaUSDMicros}
	if key.ExpiresAt != nil {
		evidence.ExpiresAt = key.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	return evidence
}

func gatewayKeyTargetMatches(got, want clients.Sub2APIWorkspaceKey) bool {
	return got.ID == want.ID && got.UserID == want.UserID && got.Name == want.Name && got.Status == want.Status && got.QuotaUSDMicros == want.QuotaUSDMicros
}

func gatewayKeyDeletedResponse(operationID string, keyID int64) map[string]any {
	return map[string]any{"operationId": operationID, "keyId": strconv.FormatInt(keyID, 10), "status": "deleted"}
}

func reservedWorkspaceKeyName(name string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(name)), "opl-workspace")
}

func gatewayKeyID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := strings.TrimSpace(r.PathValue("keyId"))
	keyID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || keyID <= 0 || strconv.FormatInt(keyID, 10) != raw {
		writeError(w, http.StatusNotFound, "gateway_key_not_found")
		return 0, false
	}
	return keyID, true
}

func (app *controlPlaneServer) gatewayUserContext(w http.ResponseWriter, r *http.Request) (map[string]any, int64, clients.SessionDelegatedCredential, bool) {
	user, ok := app.sessionUserContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "not_authenticated")
		return nil, 0, clients.SessionDelegatedCredential{}, false
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "reauthentication_required")
		return nil, 0, clients.SessionDelegatedCredential{}, false
	}
	credential, ok := app.sessionCredentials.Get(sessionLookupKey(cookie.Value))
	if !ok {
		http.SetCookie(w, sessionCookie("", -1))
		writeError(w, http.StatusUnauthorized, "reauthentication_required")
		return nil, 0, clients.SessionDelegatedCredential{}, false
	}
	userID, err := app.sub2APIUserID(r.Context(), stringValue(user["accountId"]))
	if err != nil {
		writeSourceEnvelope(w, http.StatusInternalServerError, "sub2api", "unavailable", nil)
		return nil, 0, clients.SessionDelegatedCredential{}, false
	}
	return user, userID, credential, true
}

func decodeStrictGatewayRequest(r *http.Request, output any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("invalid gateway key request")
	}
	return nil
}

func gatewayUsagePagination(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	page, pageSize := 1, 20
	if raw := strings.TrimSpace(r.URL.Query().Get("page")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 1_000_000 {
			writeError(w, http.StatusBadRequest, "invalid_gateway_usage_pagination")
			return 0, 0, false
		}
		page = value
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("pageSize")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			writeError(w, http.StatusBadRequest, "invalid_gateway_usage_pagination")
			return 0, 0, false
		}
		pageSize = value
	}
	return page, pageSize, true
}

func gatewayUsagePeriod(w http.ResponseWriter, r *http.Request) (string, bool) {
	period := strings.TrimSpace(r.URL.Query().Get("period"))
	if period == "" {
		period = "month"
	}
	if period != "today" && period != "week" && period != "month" {
		writeError(w, http.StatusBadRequest, "invalid_gateway_usage_period")
		return "", false
	}
	return period, true
}

func writeGatewayUsagePage(w http.ResponseWriter, usage clients.Sub2APIUsagePage) {
	items := make([]any, 0, len(usage.Items))
	for _, item := range usage.Items {
		items = append(items, map[string]any{
			"apiKeyId": strconv.FormatInt(item.APIKeyID, 10), "requestId": item.RequestID, "createdAt": item.CreatedAt.UTC().Format(time.RFC3339Nano),
			"model": item.Model, "inboundEndpoint": item.InboundEndpoint, "requestType": item.RequestType,
			"inputTokens": item.InputTokens, "outputTokens": item.OutputTokens, "cacheCreationTokens": item.CacheCreationTokens,
			"cacheReadTokens": item.CacheReadTokens, "actualCostUsdMicros": item.ActualCostUSDMicros,
		})
	}
	status := "available"
	if len(items) == 0 {
		status = "empty"
	}
	writeSourceEnvelope(w, http.StatusOK, "sub2api", status, map[string]any{"items": items, "total": usage.Total, "page": usage.Page, "pageSize": usage.PageSize, "pages": usage.Pages})
}

func writeGatewayUsageStats(w http.ResponseWriter, stats clients.Sub2APIUsageStats) {
	writeSourceEnvelope(w, http.StatusOK, "sub2api", "available", map[string]any{
		"totalRequests": stats.TotalRequests, "totalInputTokens": stats.TotalInputTokens,
		"totalOutputTokens": stats.TotalOutputTokens, "totalTokens": stats.TotalTokens,
		"totalActualCostUsdMicros": stats.TotalActualCostUSDMicros,
	})
}

func (app *controlPlaneServer) gatewayKeyCommand(ctx context.Context, operationID string) (gatewayKeyCommandResult, bool, error) {
	rows, err := app.tables.ListRuntimeOperations(ctx)
	if err != nil {
		return gatewayKeyCommandResult{}, false, err
	}
	for _, row := range rows {
		if stringValue(row["id"]) != operationID {
			continue
		}
		var result gatewayKeyCommandResult
		if json.Unmarshal([]byte(stringValue(row["result"])), &result) != nil || result.RequestHash == "" || result.TargetStatus == "" {
			return gatewayKeyCommandResult{}, false, errors.New("invalid gateway key operation")
		}
		return result, true, nil
	}
	return gatewayKeyCommandResult{}, false, nil
}

func (app *controlPlaneServer) saveGatewayKeyCommand(ctx context.Context, operationID, accountID, resourceID, action, status string, result gatewayKeyCommandResult) error {
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return app.tables.SaveRuntimeOperation(ctx, map[string]any{
		"id": operationID, "operationId": operationID, "accountId": accountID, "resourceId": resourceID,
		"resourceKind": "gateway_key", "action": action, "provider": "sub2api", "status": status,
		"result": string(encoded), "createdAt": time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func (app *controlPlaneServer) completeGatewayKeyCommand(r *http.Request, operationID, accountID, resourceID, action string, result gatewayKeyCommandResult) bool {
	after := any(map[string]any{"keyId": strconv.FormatInt(result.KeyID, 10), "status": result.TargetStatus})
	if result.Readback != nil {
		after = result.Readback
	}
	event := app.auditEvent(r, action, "gateway_key", resourceID, accountID, nil, after, "succeeded")
	event["id"] = "audit-" + stableID(action, operationID)[:12]
	if err := app.tables.SaveAuditEvent(r.Context(), event); err != nil {
		return false
	}
	return app.saveGatewayKeyCommand(r.Context(), operationID, accountID, resourceID, action, "succeeded", result) == nil
}

func optionalInt(value *int) string {
	if value == nil {
		return "unset"
	}
	return strconv.Itoa(*value)
}

func optionalInt64(value *int64) string {
	if value == nil {
		return "unset"
	}
	return strconv.FormatInt(*value, 10)
}

func optionalString(value *string) string {
	if value == nil {
		return "unset"
	}
	return *value
}

func optionalBool(value *bool) string {
	if value == nil {
		return "unset"
	}
	return strconv.FormatBool(*value)
}

func writeGatewayUserKeyError(w http.ResponseWriter, err error) {
	if errors.Is(err, clients.ErrSub2APIKeyNotFound) {
		writeError(w, http.StatusNotFound, "gateway_key_not_found")
		return
	}
	writeGatewaySourceError(w, err)
}

func writeGatewayKeyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, clients.ErrSub2APIWorkspaceKeyMissing):
		writeError(w, http.StatusConflict, "gateway_key_missing")
	case errors.Is(err, clients.ErrSub2APIWorkspaceKeyAmbiguous):
		writeError(w, http.StatusConflict, "gateway_key_ambiguous")
	default:
		writeUpstreamError(w, err)
	}
}

func writeGatewaySourceError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	if errors.Is(err, clients.ErrSub2APIWorkspaceKeyMissing) || errors.Is(err, clients.ErrSub2APIWorkspaceKeyAmbiguous) {
		status = http.StatusConflict
	}
	writeSourceEnvelope(w, status, "sub2api", "unavailable", nil)
}

func (app *controlPlaneServer) gatewaySub2APIUserID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	user, ok := app.sessionUserContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "not_authenticated")
		return 0, false
	}
	userID, err := app.sub2APIUserID(r.Context(), stringValue(user["accountId"]))
	if err != nil {
		writeSourceEnvelope(w, http.StatusInternalServerError, "sub2api", "unavailable", nil)
		return 0, false
	}
	return userID, true
}

func (app *controlPlaneServer) mappedSub2APIUserID(w http.ResponseWriter, r *http.Request, accountID string) (int64, bool) {
	userID, err := app.sub2APIUserID(r.Context(), accountID)
	if err == nil {
		return userID, true
	}
	if errors.Is(err, errMonthlyAccountUnmapped) {
		writeError(w, http.StatusConflict, errMonthlyAccountUnmapped.Error())
	} else {
		writeError(w, http.StatusInternalServerError, "state_read_failed")
	}
	return 0, false
}
