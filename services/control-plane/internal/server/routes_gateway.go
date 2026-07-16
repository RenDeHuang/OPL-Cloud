package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

func registerGatewayRoutes(mux *http.ServeMux, app *controlPlaneServer, service *controlplane.Service) {
	mux.HandleFunc("GET /api/gateway/usage", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		accountID, ok := app.scopedAccountID(w, r, nil)
		if !ok {
			return
		}
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
		userID, ok := app.mappedSub2APIUserID(w, r, accountID)
		if !ok {
			return
		}
		usage, err := service.GatewayUsage(r.Context(), userID, page, pageSize)
		if err != nil {
			writeGatewayUsageError(w, err)
			return
		}
		items := make([]any, 0, len(usage.Items))
		for _, item := range usage.Items {
			items = append(items, map[string]any{
				"requestId": item.RequestID, "createdAt": item.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
				"model": item.Model, "inboundEndpoint": item.InboundEndpoint, "requestType": item.RequestType,
				"inputTokens": item.InputTokens, "outputTokens": item.OutputTokens,
				"cacheCreationTokens": item.CacheCreationTokens, "cacheReadTokens": item.CacheReadTokens,
				"actualCostUsdMicros": item.ActualCostUSDMicros,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": usage.Total, "page": usage.Page, "pageSize": usage.PageSize, "pages": usage.Pages})
	}))
	mux.HandleFunc("GET /api/gateway/usage/stats", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		accountID, ok := app.scopedAccountID(w, r, nil)
		if !ok {
			return
		}
		period := strings.TrimSpace(r.URL.Query().Get("period"))
		if period == "" {
			period = "month"
		}
		if period != "today" && period != "week" && period != "month" {
			writeError(w, http.StatusBadRequest, "invalid_gateway_usage_period")
			return
		}
		userID, ok := app.mappedSub2APIUserID(w, r, accountID)
		if !ok {
			return
		}
		stats, err := service.GatewayUsageStats(r.Context(), userID, period)
		if err != nil {
			writeGatewayUsageError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"totalRequests": stats.TotalRequests, "totalInputTokens": stats.TotalInputTokens,
			"totalOutputTokens": stats.TotalOutputTokens, "totalTokens": stats.TotalTokens,
			"totalActualCostUsdMicros": stats.TotalActualCostUSDMicros,
		})
	}))
	mux.HandleFunc("GET /api/gateway/summary", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		accountID, ok := app.scopedAccountID(w, r, nil)
		if !ok {
			return
		}
		userID, ok := app.mappedSub2APIUserID(w, r, accountID)
		if !ok {
			return
		}
		summary, err := service.GatewaySummary(r.Context(), userID)
		if err != nil {
			writeGatewayKeyError(w, err)
			return
		}
		apiKey := map[string]any{
			"id": summary.Key.ID, "name": summary.Key.Name, "status": summary.Key.Status,
			"maskedValue": maskedGatewayKey(summary.Key.Key), "revealed": false,
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"account": map[string]any{"sub2apiUserId": userID, "status": summary.Balance.Status},
			"balance": map[string]any{
				"source": "sub2api", "currency": "USD", "status": "available", "available": true,
				"userId": summary.Balance.UserID, "usdMicros": summary.Balance.USDMicros,
			},
			"apiKey": apiKey,
			"usage": map[string]any{
				"quotaUsdMicros": summary.Key.QuotaUSDMicros, "quotaUsedUsdMicros": summary.Key.QuotaUsedUSDMicros,
				"usage5hUsdMicros": summary.Key.Usage5hUSDMicros, "usage1dUsdMicros": summary.Key.Usage1dUSDMicros,
				"usage7dUsdMicros": summary.Key.Usage7dUSDMicros, "lastUsedAt": summary.Key.LastUsedAt,
			},
		})
	}))
	mux.HandleFunc("POST /api/gateway/keys/opl-workspace/reveal", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		user, _ := app.sessionUserContext(r)
		if stringValue(user["role"]) != "owner" {
			writeError(w, http.StatusForbidden, "gateway_key_reveal_forbidden")
			return
		}
		accountID, ok := app.scopedAccountID(w, r, nil)
		if !ok {
			return
		}
		userID, ok := app.mappedSub2APIUserID(w, r, accountID)
		if !ok {
			return
		}
		key, err := service.Sub2APIWorkspaceKey(r.Context(), userID)
		if err != nil {
			writeGatewayKeyError(w, err)
			return
		}
		if err := app.appendAuditEvent(r, "gateway.key_reveal", "gateway_key", strconv.FormatInt(key.ID, 10), accountID, nil, map[string]any{
			"id": key.ID, "name": key.Name, "status": key.Status,
		}, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"apiKey": map[string]any{
			"id": key.ID, "name": key.Name, "status": key.Status, "value": key.Key,
		}})
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

func writeGatewayUsageError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, clients.ErrSub2APIWorkspaceKeyMissing):
		writeError(w, http.StatusConflict, "gateway_key_missing")
	case errors.Is(err, clients.ErrSub2APIWorkspaceKeyAmbiguous):
		writeError(w, http.StatusConflict, "gateway_key_ambiguous")
	default:
		writeError(w, http.StatusBadGateway, "sub2api_usage_unavailable")
	}
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

func maskedGatewayKey(value string) string {
	runes := []rune(value)
	if len(runes) <= 8 {
		return "****"
	}
	return string(runes[:4]) + "..." + string(runes[len(runes)-4:])
}
