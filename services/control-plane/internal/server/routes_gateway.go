package server

import (
	"errors"
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
		userID, ok := app.gatewaySub2APIUserID(w, r)
		if !ok {
			return
		}
		keys, err := service.GatewayKeys(r.Context(), userID)
		if err != nil {
			writeGatewaySourceError(w, err)
			return
		}
		items := make([]any, 0, len(keys))
		for _, key := range keys {
			var lastUsedAt any
			if key.LastUsedAt != nil {
				lastUsedAt = key.LastUsedAt.UTC().Format(time.RFC3339Nano)
			}
			items = append(items, map[string]any{
				"id": strconv.FormatInt(key.ID, 10), "name": key.Name, "status": key.Status,
				"quotaUsdMicros": key.QuotaUSDMicros, "quotaUsedUsdMicros": key.QuotaUsedUSDMicros,
				"usage5hUsdMicros": key.Usage5hUSDMicros, "usage1dUsdMicros": key.Usage1dUSDMicros,
				"usage7dUsdMicros": key.Usage7dUSDMicros, "lastUsedAt": lastUsedAt,
			})
		}
		status := "available"
		if len(items) == 0 {
			status = "empty"
		}
		writeSourceEnvelope(w, http.StatusOK, "sub2api", status, map[string]any{"items": items, "total": len(items)})
	}))
	mux.HandleFunc("GET /api/gateway/usage", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		page, pageSize := 1, 50
		if raw := strings.TrimSpace(r.URL.Query().Get("page")); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 1 || value > 1_000_000 {
				writeError(w, http.StatusBadRequest, "invalid_gateway_usage_pagination")
				return
			}
			page = value
		}
		if raw := strings.TrimSpace(r.URL.Query().Get("pageSize")); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 1 || value > 100 {
				writeError(w, http.StatusBadRequest, "invalid_gateway_usage_pagination")
				return
			}
			pageSize = value
		}
		userID, ok := app.gatewaySub2APIUserID(w, r)
		if !ok {
			return
		}
		usage, err := service.GatewayUsage(r.Context(), userID, page, pageSize)
		if err != nil {
			writeGatewaySourceError(w, err)
			return
		}
		items := make([]any, 0, len(usage.Items))
		for _, item := range usage.Items {
			items = append(items, map[string]any{
				"apiKeyId": strconv.FormatInt(item.APIKeyID, 10), "requestId": item.RequestID, "createdAt": item.CreatedAt.UTC().Format(time.RFC3339Nano),
				"model": item.Model, "inboundEndpoint": item.InboundEndpoint, "requestType": item.RequestType,
				"inputTokens": item.InputTokens, "outputTokens": item.OutputTokens,
				"cacheCreationTokens": item.CacheCreationTokens, "cacheReadTokens": item.CacheReadTokens,
				"actualCostUsdMicros": item.ActualCostUSDMicros,
			})
		}
		status := "available"
		if len(items) == 0 {
			status = "empty"
		}
		writeSourceEnvelope(w, http.StatusOK, "sub2api", status, map[string]any{"items": items, "total": usage.Total, "page": usage.Page, "pageSize": usage.PageSize, "pages": usage.Pages})
	}))
	mux.HandleFunc("GET /api/gateway/usage/stats", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		period := strings.TrimSpace(r.URL.Query().Get("period"))
		if period == "" {
			period = "month"
		}
		if period != "today" && period != "week" && period != "month" {
			writeError(w, http.StatusBadRequest, "invalid_gateway_usage_period")
			return
		}
		userID, ok := app.gatewaySub2APIUserID(w, r)
		if !ok {
			return
		}
		stats, err := service.GatewayUsageStats(r.Context(), userID, period)
		if err != nil {
			writeGatewaySourceError(w, err)
			return
		}
		writeSourceEnvelope(w, http.StatusOK, "sub2api", "available", map[string]any{
			"totalRequests": stats.TotalRequests, "totalInputTokens": stats.TotalInputTokens,
			"totalOutputTokens": stats.TotalOutputTokens, "totalTokens": stats.TotalTokens,
			"totalActualCostUsdMicros": stats.TotalActualCostUSDMicros,
		})
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
	mux.HandleFunc("POST /api/gateway/keys/opl-workspace/reveal", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		user, ok := app.sessionUserContext(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "not_authenticated")
			return
		}
		if stringValue(user["role"]) != "owner" {
			writeError(w, http.StatusForbidden, "gateway_key_reveal_forbidden")
			return
		}
		accountID := stringValue(user["accountId"])
		userID, err := app.sub2APIUserID(r.Context(), accountID)
		if err != nil {
			writeSourceEnvelope(w, http.StatusInternalServerError, "sub2api", "unavailable", nil)
			return
		}
		key, err := service.Sub2APIWorkspaceKey(r.Context(), userID)
		if err != nil {
			writeGatewaySourceError(w, err)
			return
		}
		if key.ID <= 0 || key.UserID != userID || key.Name != "opl-workspace" || key.Status != "active" || key.Key == "" {
			writeSourceEnvelope(w, http.StatusBadGateway, "sub2api", "unavailable", nil)
			return
		}
		if err := app.appendAuditEvent(r, "gateway.key_reveal", "gateway_key", strconv.FormatInt(key.ID, 10), accountID, nil, map[string]any{
			"id": key.ID, "name": key.Name, "status": key.Status,
		}, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeSourceEnvelope(w, http.StatusOK, "sub2api", "available", map[string]any{
			"id": strconv.FormatInt(key.ID, 10), "name": key.Name, "status": key.Status, "value": key.Key,
		})
	}))
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
