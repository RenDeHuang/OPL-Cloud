package server

import (
	"context"
	"net/http"
	"strings"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

func registerWorkspaceLaunchRoutes(mux *http.ServeMux, app *controlPlaneServer, service *controlplane.Service) {
	mux.HandleFunc("POST /api/workspace-launches", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		key, ok := requiredMutationKey(w, r)
		if !ok {
			return
		}
		accountID, ok := app.scopedAccountID(w, r, input)
		if !ok {
			return
		}
		user, ok := app.sessionUserContext(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "not_authenticated")
			return
		}
		if stringValue(user["role"]) != "owner" {
			writeError(w, http.StatusForbidden, "workspace_owner_required")
			return
		}
		name, validName := input["name"].(string)
		packageID, validPackage := input["packageId"].(string)
		name, packageID = strings.TrimSpace(name), strings.TrimSpace(packageID)
		if !validName || !validPackage || name == "" || packageID == "" {
			writeError(w, http.StatusBadRequest, "invalid_pricing_input")
			return
		}
		storageGB, validSize := positiveIntegerField(input, "sizeGb")
		if !validSize {
			writeError(w, http.StatusBadRequest, "invalid_pricing_input")
			return
		}
		autoRenew, validAutoRenew := input["autoRenew"].(bool)
		if !validAutoRenew {
			writeError(w, http.StatusBadRequest, "autoRenew_required")
			return
		}
		quote, err := app.pricingPreviewResponse(r.Context(), map[string]any{"resourceType": "workspace", "packageId": packageID, "sizeGb": storageGB})
		if err != nil {
			writePricingError(w, err)
			return
		}
		operation := newWorkspaceLaunchOperation(
			accountID, stringValue(user["id"]), name, packageID, int(storageGB), autoRenew, stringValue(quote["priceVersion"]),
			int64(numberField(quote, "totalChargeUsdMicros", 0)), key,
		)

		unlock := app.lockResource("workspace-launch", accountID)
		defer unlock()
		operations, err := app.tables.ListRuntimeOperations(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		for _, row := range operations {
			if stringValue(row["id"]) != operation.ID {
				continue
			}
			persisted, err := decodeWorkspaceLaunchOperation(row)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "state_read_failed")
				return
			}
			if persisted.AccountID != accountID || persisted.RequestHash != operation.RequestHash {
				writeError(w, http.StatusConflict, errIdempotencyConflict.Error())
				return
			}
			body, err := workspaceLaunchResponse(row)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "state_read_failed")
				return
			}
			writeJSON(w, http.StatusAccepted, body)
			return
		}
		if _, blocked := app.reconciliationBlocksNewWorkspaces(); blocked {
			writeError(w, http.StatusConflict, "billing_reconciliation_blocked")
			return
		}
		workspaces, err := app.tables.ListWorkspaces(r.Context(), accountID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		if len(workspaces) != 0 {
			writeError(w, http.StatusConflict, errPrimaryWorkspaceExists.Error())
			return
		}
		for _, row := range operations {
			if stringValue(row["accountId"]) == accountID && stringValue(row["action"]) == "workspace.launch" {
				writeError(w, http.StatusConflict, errWorkspaceLaunchInProgress.Error())
				return
			}
		}

		zone := monthlyComputeLaunchZone()
		for _, preflightInput := range []clients.MonthlyPreflightInput{
			{ResourceType: "compute", PackageID: packageID, Zone: zone},
			{ResourceType: "storage", PackageID: packageID, SizeGB: int(storageGB), Zone: zone},
		} {
			preflight, err := service.PreflightMonthlyResource(r.Context(), preflightInput)
			if err != nil {
				writeUpstreamError(w, err)
				return
			}
			if !monthlyPreflightConfirmed(preflightInput, preflight) {
				writeError(w, http.StatusBadGateway, "fabric_monthly_preflight_invalid")
				return
			}
		}
		sub2APIUserID, err := app.sub2APIUserID(r.Context(), accountID)
		if err != nil {
			writeError(w, http.StatusConflict, errMonthlyAccountUnmapped.Error())
			return
		}
		summary, err := service.GatewaySummary(r.Context(), sub2APIUserID)
		if err != nil {
			writeGatewayKeyError(w, err)
			return
		}
		if summary.Balance.USDMicros < operation.TotalChargeUSDMicros {
			writeError(w, http.StatusConflict, errMonthlyInsufficientBalance.Error())
			return
		}
		row := workspaceLaunchOperationRow(operation)
		if err := app.tables.SaveRuntimeOperation(r.Context(), row); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		body, err := workspaceLaunchResponse(row)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		if providerReconcileWorkerEnabled() {
			go func() { _ = app.runWorkspaceLaunch(context.Background(), service, operation.ID) }()
		}
		writeJSON(w, http.StatusAccepted, body)
	}))

	mux.HandleFunc("GET /api/workspace-launches", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		accountID, ok := app.scopedAccountID(w, r, nil)
		if !ok {
			return
		}
		operations, err := app.tables.ListRuntimeOperations(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		rows := make([]any, 0)
		for _, operation := range operations {
			if stringValue(operation["accountId"]) != accountID || stringValue(operation["action"]) != "workspace.launch" {
				continue
			}
			body, err := workspaceLaunchResponse(operation)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "state_read_failed")
				return
			}
			rows = append(rows, body)
		}
		writeJSON(w, http.StatusOK, rows)
	}))

	mux.HandleFunc("GET /api/workspace-launches/{id}", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		accountID, ok := app.scopedAccountID(w, r, nil)
		if !ok {
			return
		}
		operations, err := app.tables.ListRuntimeOperations(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		for _, operation := range operations {
			if stringValue(operation["id"]) != r.PathValue("id") || stringValue(operation["accountId"]) != accountID || stringValue(operation["action"]) != "workspace.launch" {
				continue
			}
			body, err := workspaceLaunchResponse(operation)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "state_read_failed")
				return
			}
			writeJSON(w, http.StatusOK, body)
			return
		}
		writeError(w, http.StatusNotFound, "workspace_launch_not_found")
	}))
}
