package server

import (
	"errors"
	"net/http"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

func registerGatewayRoutes(mux *http.ServeMux, app *controlPlaneServer, service *controlplane.Service) {
	mux.HandleFunc("GET /api/gateway/summary", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		accountID, ok := app.scopedAccountID(w, r, nil)
		if !ok {
			return
		}
		reveal := r.URL.Query().Get("reveal") == "true"
		user, _ := app.sessionUserContext(r)
		if reveal && stringValue(user["role"]) != "owner" {
			writeError(w, http.StatusForbidden, "gateway_key_reveal_forbidden")
			return
		}
		userID, ok := app.mappedSub2APIUserID(w, r, accountID)
		if !ok {
			return
		}
		summary, err := service.GatewaySummary(r.Context(), userID)
		if err != nil {
			switch {
			case errors.Is(err, clients.ErrSub2APIWorkspaceKeyMissing):
				writeError(w, http.StatusConflict, "gateway_key_missing")
			case errors.Is(err, clients.ErrSub2APIWorkspaceKeyAmbiguous):
				writeError(w, http.StatusConflict, "gateway_key_ambiguous")
			default:
				writeUpstreamError(w, err)
			}
			return
		}
		apiKey := map[string]any{
			"id": summary.Key.ID, "name": summary.Key.Name, "status": summary.Key.Status,
			"maskedValue": maskedGatewayKey(summary.Key.Key), "revealed": reveal,
		}
		if reveal {
			apiKey["value"] = summary.Key.Key
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
