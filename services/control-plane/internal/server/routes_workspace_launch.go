package server

import (
	"context"
	"errors"
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
		if autoRenew {
			writeError(w, http.StatusConflict, "autoRenew_unavailable")
			return
		}
		if _, supplied := input["priceVersion"]; supplied {
			writeError(w, http.StatusBadRequest, "client_pricing_forbidden")
			return
		}
		if _, supplied := input["totalChargeUsdMicros"]; supplied {
			writeError(w, http.StatusBadRequest, "client_pricing_forbidden")
			return
		}
		computePools, ok := fabricComputePools(w, r, service)
		if !ok {
			return
		}
		quote, err := app.pricingPreviewResponse(r.Context(), map[string]any{"resourceType": "workspace", "packageId": packageID, "sizeGb": storageGB}, computePools)
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
			if stringValue(row["id"]) != operation.ID || stringValue(row["action"]) != workspaceLaunchAction {
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
			if stringValue(row["accountId"]) == accountID && isWorkspaceLaunchAction(stringValue(row["action"])) && !terminalWorkspaceLaunchStatus(stringValue(row["status"])) {
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
			if preflightInput.ResourceType == "compute" {
				operation.ComputeNodePoolID = preflight.NodePoolID
			}
		}
		unlockAccount := app.lockResource("account", accountID)
		defer unlockAccount()
		credentialUser, sub2APIUserID, credential, ok := app.gatewayUserContext(w, r)
		if !ok {
			return
		}
		if stringValue(credentialUser["accountId"]) != accountID {
			writeError(w, http.StatusForbidden, "account_scope_forbidden")
			return
		}
		workspaceKey, err := convergeWorkspaceAPIKey(r.Context(), service, credential, sub2APIUserID, operation.ID)
		if err != nil {
			writeGatewayKeyError(w, err)
			return
		}
		operation.WorkspaceAPIKeyID = workspaceKey.ID
		balance, err := service.Sub2APIBalance(r.Context(), sub2APIUserID)
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		if balance.USDMicros <= operation.TotalChargeUSDMicros {
			writeError(w, http.StatusConflict, errMonthlyInsufficientBalance.Error())
			return
		}
		row := workspaceLaunchOperationRow(operation)
		if err := app.tables.ClaimWorkspaceLaunch(r.Context(), workspaceLaunchClaimCAS{AccountID: accountID, DesiredOperation: row}); err != nil {
			if errors.Is(err, errWorkspaceLaunchCASConflict) || errors.Is(err, errWorkspaceLaunchInProgress) {
				operations, readErr := app.tables.ListRuntimeOperations(r.Context())
				if readErr == nil {
					for _, existing := range operations {
						if stringValue(existing["id"]) != operation.ID || stringValue(existing["action"]) != workspaceLaunchAction {
							continue
						}
						persisted, decodeErr := decodeWorkspaceLaunchOperation(existing)
						if decodeErr == nil && persisted.AccountID == accountID && persisted.RequestHash == operation.RequestHash {
							body, responseErr := workspaceLaunchResponse(existing)
							if responseErr == nil {
								writeJSON(w, http.StatusAccepted, body)
								return
							}
						}
					}
				}
				if errors.Is(err, errWorkspaceLaunchInProgress) {
					writeError(w, http.StatusConflict, errWorkspaceLaunchInProgress.Error())
				} else {
					writeError(w, http.StatusConflict, errIdempotencyConflict.Error())
				}
				return
			}
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		operations, err = app.tables.ListRuntimeOperations(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		persistedRow := findRecord(operations, operation.ID)
		body, err := workspaceLaunchResponse(persistedRow)
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
			if stringValue(operation["accountId"]) != accountID || stringValue(operation["action"]) != workspaceLaunchAction {
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
			if stringValue(operation["id"]) != r.PathValue("id") || stringValue(operation["accountId"]) != accountID || stringValue(operation["action"]) != workspaceLaunchAction {
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

func convergeWorkspaceAPIKey(ctx context.Context, service *controlplane.Service, credential clients.SessionDelegatedCredential, userID int64, operationID string) (clients.Sub2APIWorkspaceKey, error) {
	keys, err := service.GatewayUserKeys(ctx, credential, userID)
	if err != nil {
		return clients.Sub2APIWorkspaceKey{}, err
	}
	reserved := workspaceReservedKeys(keys, userID)
	if len(reserved) == 1 && reserved[0].Name == "opl-workspace" && reserved[0].Status == "active" && reserved[0].ID > 0 {
		return reserved[0], nil
	}
	if len(reserved) != 0 {
		return clients.Sub2APIWorkspaceKey{}, clients.ErrSub2APIWorkspaceKeyAmbiguous
	}
	created, createErr := service.CreateGatewayUserKey(ctx, credential, userID, clients.Sub2APICreateKeyInput{Name: "opl-workspace"}, operationID+":workspace-key")
	keys, readErr := service.GatewayUserKeys(ctx, credential, userID)
	if readErr != nil {
		if createErr != nil {
			return clients.Sub2APIWorkspaceKey{}, createErr
		}
		return clients.Sub2APIWorkspaceKey{}, readErr
	}
	reserved = workspaceReservedKeys(keys, userID)
	if len(reserved) != 1 || reserved[0].Name != "opl-workspace" || reserved[0].Status != "active" || reserved[0].ID <= 0 || created.ID > 0 && created.ID != reserved[0].ID {
		return clients.Sub2APIWorkspaceKey{}, clients.ErrSub2APIWorkspaceKeyAmbiguous
	}
	return reserved[0], nil
}

func workspaceReservedKeys(keys []clients.Sub2APIWorkspaceKey, userID int64) []clients.Sub2APIWorkspaceKey {
	reserved := make([]clients.Sub2APIWorkspaceKey, 0, 1)
	for _, key := range keys {
		if key.UserID != userID || key.ID <= 0 {
			return append(reserved, clients.Sub2APIWorkspaceKey{})
		}
		if reservedWorkspaceKeyName(key.Name) {
			reserved = append(reserved, key)
		}
	}
	return reserved
}
