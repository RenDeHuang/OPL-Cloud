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
		unlock := app.lockResource("workspace-launch", accountID)
		defer unlock()
		ownerUserID := stringValue(user["id"])
		row, found, err := app.tables.GetRuntimeOperation(r.Context(), workspaceLaunchOperationID(accountID, key))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		if found {
			if stringValue(row["action"]) != workspaceLaunchAction {
				writeError(w, http.StatusConflict, errIdempotencyConflict.Error())
				return
			}
			persisted, err := decodeWorkspaceLaunchOperation(row)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "state_read_failed")
				return
			}
			if !workspaceLaunchSubmissionMatches(persisted, accountID, ownerUserID, name, packageID, int(storageGB), autoRenew) {
				writeError(w, http.StatusConflict, errIdempotencyConflict.Error())
				return
			}
			app.respondWorkspaceLaunchContinuation(w, r, service, persisted)
			return
		}
		for _, action := range []string{workspaceLaunchAction, "workspace.launch"} {
			active, err := queryRuntimeOperations(r.Context(), app.tables, runtimeOperationQuery{
				AccountID: accountID, Action: action, ExcludedStatuses: []string{"succeeded", "refunded", "failed"},
			})
			if err != nil {
				writeError(w, http.StatusInternalServerError, "state_read_failed")
				return
			}
			if len(active) > 0 {
				var matching workspaceLaunchOperation
				matchingCount := 0
				for _, candidate := range active {
					persisted, decodeErr := decodeWorkspaceLaunchOperation(candidate)
					if decodeErr != nil || !workspaceLaunchSubmissionMatches(persisted, accountID, ownerUserID, name, packageID, int(storageGB), autoRenew) {
						continue
					}
					matching, matchingCount = persisted, matchingCount+1
				}
				if matchingCount == 1 {
					app.respondWorkspaceLaunchContinuation(w, r, service, matching)
					return
				}
				writeError(w, http.StatusConflict, errWorkspaceLaunchInProgress.Error())
				return
			}
		}
		admission := controlledBasicPilotAdmissionFromEnv()
		code := admission.rejectNewLaunch(accountID, packageID, autoRenew)
		if code == "workspace_launch_admission_disabled" {
			approval, configured := parseProductionAcceptanceBApproval()
			if configured && productionAcceptanceBLaunchApproved(r.Header, approval, accountID, stringValue(user["email"]), name, packageID, int(storageGB), autoRenew, key) {
				code = ""
			}
		}
		if code != "" {
			writeError(w, http.StatusConflict, code)
			return
		}
		if _, blocked := app.reconciliationBlocksNewWorkspaces(); blocked {
			writeError(w, http.StatusConflict, "billing_reconciliation_blocked")
			return
		}
		if currentWorkspaceImageDigest() == "" {
			writeError(w, http.StatusConflict, "workspace_image_digest_invalid")
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
			accountID, ownerUserID, name, packageID, int(storageGB), autoRenew, stringValue(quote["priceVersion"]),
			int64(numberField(quote, "totalChargeUsdMicros", 0)), key,
		)

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
		balance, err := service.Sub2APIBalance(r.Context(), sub2APIUserID)
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		if balance.USDMicros < operation.TotalChargeUSDMicros {
			writeError(w, http.StatusConflict, errMonthlyInsufficientBalance.Error())
			return
		}
		operation.Phase = "key_pending"
		row = workspaceLaunchOperationRow(operation)
		if err := app.tables.ClaimWorkspaceLaunch(r.Context(), workspaceLaunchClaimCAS{AccountID: accountID, DesiredOperation: row}); err != nil {
			if errors.Is(err, errWorkspaceLaunchCapacityReached) {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
			if errors.Is(err, errWorkspaceLaunchCASConflict) || errors.Is(err, errWorkspaceLaunchInProgress) {
				existing, found, readErr := app.tables.GetRuntimeOperation(r.Context(), operation.ID)
				if readErr == nil && found && stringValue(existing["action"]) == workspaceLaunchAction {
					persisted, decodeErr := decodeWorkspaceLaunchOperation(existing)
					if decodeErr == nil && persisted.AccountID == accountID && persisted.RequestHash == operation.RequestHash {
						body, responseErr := workspaceLaunchResponse(existing)
						if responseErr == nil {
							writeJSON(w, http.StatusAccepted, body)
							return
						}
					}
				}
				active, activeErr := queryRuntimeOperations(r.Context(), app.tables, runtimeOperationQuery{
					AccountID: accountID, Action: workspaceLaunchAction, ExcludedStatuses: []string{"succeeded", "refunded", "failed"},
				})
				var matching workspaceLaunchOperation
				matchingCount := 0
				if activeErr == nil {
					for _, candidate := range active {
						persisted, decodeErr := decodeWorkspaceLaunchOperation(candidate)
						if decodeErr == nil && persisted.AccountID == accountID && persisted.RequestHash == operation.RequestHash {
							matching, matchingCount = persisted, matchingCount+1
						}
					}
				}
				if matchingCount == 1 {
					if matching.Phase == "key_pending" {
						if convergeErr := app.convergeAndPersistWorkspaceLaunchKey(r.Context(), service, credential, sub2APIUserID, &matching); convergeErr != nil {
							writeGatewayKeyError(w, convergeErr)
							return
						}
					}
					persistedRow, persistedFound, persistedErr := app.tables.GetRuntimeOperation(r.Context(), matching.ID)
					if persistedErr == nil && persistedFound {
						body, responseErr := workspaceLaunchResponse(persistedRow)
						if responseErr == nil {
							writeJSON(w, http.StatusAccepted, body)
							return
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
		operation.PersistedResult = stringValue(row["result"])
		if err := app.convergeAndPersistWorkspaceLaunchKey(r.Context(), service, credential, sub2APIUserID, &operation); err != nil {
			writeGatewayKeyError(w, err)
			return
		}
		persistedRow, found, err := app.tables.GetRuntimeOperation(r.Context(), operation.ID)
		if err != nil || !found {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		body, err := workspaceLaunchResponse(persistedRow)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		if workspaceLaunchWorkerEnabled() {
			go func() { _ = app.runWorkspaceLaunch(context.Background(), service, operation.ID) }()
		}
		writeJSON(w, http.StatusAccepted, body)
	}))

	mux.HandleFunc("GET /api/workspace-launches", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		accountID, ok := app.scopedAccountID(w, r, nil)
		if !ok {
			return
		}
		operations, err := queryRuntimeOperations(r.Context(), app.tables, runtimeOperationQuery{AccountID: accountID, Action: workspaceLaunchAction})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		rows := make([]any, 0)
		for _, operation := range operations {
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
		operation, found, err := app.tables.GetRuntimeOperation(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		if found && stringValue(operation["accountId"]) == accountID && stringValue(operation["action"]) == workspaceLaunchAction {
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

func (app *controlPlaneServer) respondWorkspaceLaunchContinuation(w http.ResponseWriter, r *http.Request, service *controlplane.Service, operation workspaceLaunchOperation) {
	if operation.Phase == "key_pending" {
		unlockAccount := app.lockResource("account", operation.AccountID)
		defer unlockAccount()
		credentialUser, sub2APIUserID, credential, ok := app.gatewayUserContext(w, r)
		if !ok {
			return
		}
		if stringValue(credentialUser["accountId"]) != operation.AccountID {
			writeError(w, http.StatusForbidden, "account_scope_forbidden")
			return
		}
		if err := app.convergeAndPersistWorkspaceLaunchKey(r.Context(), service, credential, sub2APIUserID, &operation); err != nil {
			writeGatewayKeyError(w, err)
			return
		}
	}
	row, found, err := app.tables.GetRuntimeOperation(r.Context(), operation.ID)
	if err != nil || !found {
		writeError(w, http.StatusInternalServerError, "state_read_failed")
		return
	}
	body, err := workspaceLaunchResponse(row)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "state_read_failed")
		return
	}
	writeJSON(w, http.StatusAccepted, body)
}

func (app *controlPlaneServer) convergeAndPersistWorkspaceLaunchKey(ctx context.Context, service *controlplane.Service, credential clients.SessionDelegatedCredential, userID int64, operation *workspaceLaunchOperation) error {
	workspaceKey, err := convergeWorkspaceAPIKey(ctx, service, credential, userID, operation.WorkspaceID, operation.ID)
	if err != nil {
		return err
	}
	operation.WorkspaceAPIKeyID = workspaceKey.ID
	operation.Status, operation.Phase, operation.ErrorCode = "debit_pending", "debit_pending", ""
	if err := app.persistWorkspaceLaunch(ctx, operation); err != nil {
		if !errors.Is(err, errWorkspaceLaunchCASConflict) {
			return err
		}
		current, found, readErr := app.tables.GetRuntimeOperation(ctx, operation.ID)
		if readErr != nil || !found || !workspaceLaunchClaimIdentityMatches(current, workspaceLaunchOperationRow(*operation)) {
			return err
		}
		persisted, decodeErr := decodeWorkspaceLaunchOperation(current)
		if decodeErr != nil || persisted.WorkspaceAPIKeyID != workspaceKey.ID || persisted.Phase == "key_pending" {
			return err
		}
		*operation = persisted
	}
	return nil
}

func convergeWorkspaceAPIKey(ctx context.Context, service *controlplane.Service, credential clients.SessionDelegatedCredential, userID int64, workspaceID, operationID string) (clients.Sub2APIWorkspaceKey, error) {
	name := workspaceReservedKeyName(workspaceID)
	keys, err := service.GatewayWorkspaceKeysForConvergence(ctx, credential, userID, name)
	if err != nil {
		return clients.Sub2APIWorkspaceKey{}, err
	}
	reserved := workspaceKeysNamed(keys, name)
	if len(reserved) == 1 && reserved[0].UserID == userID && reserved[0].ID > 0 && reserved[0].Status == "active" {
		return reserved[0], nil
	}
	if len(reserved) != 0 {
		return clients.Sub2APIWorkspaceKey{}, clients.ErrSub2APIWorkspaceKeyAmbiguous
	}
	created, createErr := service.CreateGatewayUserKey(ctx, credential, userID, clients.Sub2APICreateKeyInput{Name: name}, operationID+":workspace-key")
	keys, readErr := service.GatewayWorkspaceKeysForConvergence(ctx, credential, userID, name)
	if readErr != nil {
		if createErr != nil {
			return clients.Sub2APIWorkspaceKey{}, createErr
		}
		return clients.Sub2APIWorkspaceKey{}, readErr
	}
	reserved = workspaceKeysNamed(keys, name)
	if len(reserved) != 1 || reserved[0].UserID != userID || reserved[0].ID <= 0 || reserved[0].Status != "active" || created.ID > 0 && created.ID != reserved[0].ID {
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
