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
		autoRenew, validAutoRenew := input["autoRenew"].(bool)
		if !validName || !validPackage || name == "" || packageID == "" || !validAutoRenew {
			writeError(w, http.StatusBadRequest, "invalid_pricing_input")
			return
		}
		if _, supplied := input["sizeGb"]; supplied {
			writeError(w, http.StatusBadRequest, "invalid_pricing_input")
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
		ownerUserID := stringValue(user["id"])
		account, found, err := app.tables.GetAccount(r.Context(), accountID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		if !found || stringValue(account["status"]) != "active" || stringValue(account["ownerUserId"]) != ownerUserID {
			writeError(w, http.StatusForbidden, "account_scope_forbidden")
			return
		}
		if !workspacePurchaseEnabled(account) {
			writeError(w, http.StatusConflict, "workspace_purchase_not_enabled")
			return
		}

		unlock := app.lockResource("workspace-launch", accountID)
		defer unlock()
		operationID := workspaceLaunchOperationID(accountID, key)
		row, found, err := app.tables.GetRuntimeOperation(r.Context(), operationID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		if found {
			persisted, decodeErr := decodeWorkspaceLaunchReconcileOperation(row)
			if decodeErr != nil || !workspaceLaunchReconcileRequestMatches(persisted, accountID, ownerUserID, name, packageID, autoRenew) {
				writeError(w, http.StatusConflict, errIdempotencyConflict.Error())
				return
			}
			if (persisted.Status == "pending" && persisted.Stage == "key") || (app.deployment.customerOwned() && persisted.Status == "manual_review" && (persisted.Stage == "key" || persisted.Stage == "secret") && persisted.Observations[persisted.Stage].State == workspaceLaunchStageUnknown) {
				credentialUser, sub2APIUserID, credential, credentialOK := app.gatewayUserContext(w, r)
				if !credentialOK {
					return
				}
				if stringValue(credentialUser["accountId"]) != accountID || sub2APIUserID != persisted.int64Fact("sub2apiUserId") {
					writeError(w, http.StatusForbidden, "account_scope_forbidden")
					return
				}
				continued, reconcileErr := app.workspaceLaunchReconciler(service, credential, sub2APIUserID).Reconcile(r.Context(), persisted.ID)
				if reconcileErr != nil {
					writeError(w, http.StatusInternalServerError, "state_persist_failed")
					return
				}
				persisted = continued
			}
			app.respondWorkspaceLaunchContinuation(w, r, persisted)
			return
		}
		if autoRenew && !app.deployment.resourceBillingEnabled() {
			writeError(w, http.StatusConflict, "autoRenew_unavailable")
			return
		}
		active, err := queryRuntimeOperations(r.Context(), app.tables, runtimeOperationQuery{
			AccountID: accountID, ExcludedStatuses: []string{"succeeded", "refunded", "failed"},
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		for _, candidate := range active {
			if isWorkspaceLaunchAction(stringValue(candidate["action"])) {
				writeError(w, http.StatusConflict, errWorkspaceLaunchInProgress.Error())
				return
			}
		}
		if _, blocked := app.reconciliationBlocksNewWorkspaces(); blocked {
			writeError(w, http.StatusConflict, "billing_reconciliation_blocked")
			return
		}
		computePools, ok := fabricComputePools(w, r, service)
		if !ok {
			return
		}
		storageGB, packageAvailable := providerPackageStorageGB(computePools, packageID)
		if !packageAvailable {
			writePricingError(w, errPackageUnavailable)
			return
		}

		admission := controlledBasicPilotAdmissionFromEnv()
		code := ""
		if !app.deployment.customerOwned() {
			// The route admits renewal intent against the active billing mode;
			// pilot admission continues to own account, package, and capacity policy.
			code = admission.rejectNewLaunch(accountID, packageID, false)
		}
		acceptanceBApproved := false
		if code == "workspace_launch_admission_disabled" {
			approval, configured := parseProductionAcceptanceBApproval()
			if configured && productionAcceptanceBLaunchApproved(r.Header, approval, accountID, stringValue(user["email"]), name, packageID, storageGB, autoRenew, key) {
				code, acceptanceBApproved = "", true
			}
		}
		if code != "" {
			writeError(w, http.StatusConflict, code)
			return
		}
		quote, err := pricingPreviewResponse(map[string]any{"resourceType": "workspace", "packageId": packageID, "sizeGb": storageGB})
		if err != nil {
			writePricingError(w, err)
			return
		}
		quote = customerPricingPreviewDTO(quote)
		quote = app.applyResourceBillingQuote(quote)
		descriptor, err := newWorkspaceLaunchDescriptor(accountID, ownerUserID, name, packageID, storageGB, autoRenew, stringValue(quote["priceVersion"]), key)
		if err != nil {
			writeError(w, http.StatusConflict, "workspace_image_digest_invalid")
			return
		}
		preflightInput := clients.WorkspaceLaunchPreflightInput{
			SchemaVersion: clients.WorkspaceLaunchFabricSchemaVersion, LaunchOperationID: descriptor.OperationID,
			AccountID: accountID, WorkspaceID: descriptor.WorkspaceID, PackageID: packageID, SizeGB: storageGB,
			WorkspaceImageDigest: descriptor.WorkspaceImageDigest, RequestHash: descriptor.RequestHash,
		}
		preflight, err := service.PreflightWorkspaceLaunch(r.Context(), preflightInput)
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		if !workspaceLaunchPreflightConfirmed(preflightInput, preflight) {
			writeError(w, http.StatusBadGateway, "fabric_workspace_launch_preflight_invalid")
			return
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
		workspaceKeyGroupID, err := workspaceCodexGroupID(r.Context(), service, credential, sub2APIUserID)
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		totalCharge := int64(numberField(quote, "totalChargeUsdMicros", 0))
		preChargeBalance := int64(0)
		if app.deployment.resourceBillingEnabled() {
			balance, balanceErr := service.Sub2APIBalance(r.Context(), sub2APIUserID)
			if balanceErr != nil {
				writeUpstreamError(w, balanceErr)
				return
			}
			if balance.USDMicros < totalCharge {
				writeError(w, http.StatusConflict, errMonthlyInsufficientBalance.Error())
				return
			}
			preChargeBalance = balance.USDMicros
		}
		created, err := app.createWorkspaceLaunch(r.Context(), service, credential, sub2APIUserID, workspaceLaunchReconcileCreate{
			OperationID: descriptor.OperationID, RequestHash: descriptor.RequestHash, AccountID: accountID, OwnerUserID: ownerUserID,
			Sub2APIUserID: sub2APIUserID, WorkspaceKeyGroupID: workspaceKeyGroupID, WorkspaceID: descriptor.WorkspaceID,
			Name: name, PackageID: packageID, StorageGB: storageGB, AutoRenew: autoRenew,
			PriceVersion: stringValue(quote["priceVersion"]), TotalChargeUSDMicros: totalCharge,
			ProviderProfileRef: preflight.ProviderProfileRef, PreflightBindingRef: preflight.BindingRef, SpecDigest: preflight.SpecDigest,
			WorkspaceImageDigest: descriptor.WorkspaceImageDigest, PreChargeBalanceMicros: preChargeBalance, ResourceBillingEnabled: boolPtr(app.deployment.resourceBillingEnabled()),
			AcceptanceBCapacitySlot: acceptanceBApproved,
		})
		if err != nil {
			switch {
			case errors.Is(err, errWorkspaceLaunchCapacityReached):
				writeError(w, http.StatusConflict, err.Error())
			case errors.Is(err, errWorkspaceLaunchCASConflict), errors.Is(err, errWorkspaceLaunchInProgress):
				writeError(w, http.StatusConflict, errIdempotencyConflict.Error())
			default:
				writeError(w, http.StatusInternalServerError, "state_persist_failed")
			}
			return
		}
		persistedRow, found, err := app.tables.GetRuntimeOperation(r.Context(), created.ID)
		if err != nil || !found {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		body, err := workspaceLaunchResponse(persistedRow)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		if workspaceLaunchWorkerEnabled() && created.Status == "pending" {
			go func() {
				unlock := app.lockResource("workspace-launch", accountID)
				defer unlock()
				_ = app.runWorkspaceLaunch(context.Background(), service, created.ID)
			}()
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
		rows := make([]any, 0, len(operations))
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

	mux.HandleFunc("POST /api/workspace-launches/{id}/resume", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requiredMutationKey(w, r); !ok {
			return
		}
		if !app.deployment.customerOwned() {
			writeError(w, http.StatusNotFound, "workspace_launch_not_found")
			return
		}
		accountID, ok := app.scopedAccountID(w, r, nil)
		if !ok {
			return
		}
		unlock := app.lockResource("workspace-launch", accountID)
		defer unlock()
		row, found, err := app.tables.GetRuntimeOperation(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_read_failed")
			return
		}
		if !found || stringValue(row["accountId"]) != accountID || stringValue(row["action"]) != workspaceLaunchAction {
			writeError(w, http.StatusNotFound, "workspace_launch_not_found")
			return
		}
		operation, err := decodeWorkspaceLaunchReconcileOperation(row)
		recoverableManualReview := operation.Status == "manual_review" && (operation.Stage == "key" || operation.Stage == "storage" || operation.Stage == "attachment" || operation.Stage == "secret" || operation.Stage == "runtime" || operation.Stage == "activation") && operation.Observations[operation.Stage].State == workspaceLaunchStageUnknown
		recoverablePendingStage := operation.Status == "pending" && (operation.Stage == "key" || operation.Stage == "storage" || operation.Stage == "attachment" || operation.Stage == "secret" || operation.Stage == "runtime")
		if err != nil || !recoverableManualReview && !recoverablePendingStage {
			writeError(w, http.StatusConflict, "workspace_launch_not_recoverable")
			return
		}
		_, sub2APIUserID, credential, ok := app.gatewayUserContext(w, r)
		if !ok {
			return
		}
		if sub2APIUserID != operation.int64Fact("sub2apiUserId") {
			writeError(w, http.StatusForbidden, "account_scope_forbidden")
			return
		}
		continued, err := app.workspaceLaunchReconciler(service, credential, sub2APIUserID).Reconcile(r.Context(), operation.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		app.respondWorkspaceLaunchContinuation(w, r, continued)
	}))
}

func (app *controlPlaneServer) respondWorkspaceLaunchContinuation(w http.ResponseWriter, r *http.Request, operation workspaceLaunchReconcileOperation) {
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
