package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

var billingReviewEvidenceRefPattern = regexp.MustCompile(`^case-[0-9]{8}-[a-z0-9]{3,16}$`)
var computeClaimMergedSHAPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)
var computeClaimCloudDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var computeClaimApprovalDigestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

const (
	operatorPageReadTimeout      = 5 * time.Second
	operatorPageLaneConcurrency  = 4
	operatorPageTotalConcurrency = 8
)

func registerAdminRoutes(mux *http.ServeMux, app *controlPlaneServer, service *controlplane.Service) {
	mux.HandleFunc("POST /api/operator/accounts/{accountId}/wallet-adjustments", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		app.createWalletAdjustment(w, r, service)
	}))
	mux.HandleFunc("GET /api/operator/wallet-adjustments/{operationId}", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		app.getWalletAdjustment(w, r)
	}))
	mux.HandleFunc("POST /api/operator/wallet-adjustments/{operationId}/recover", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		app.recoverWalletAdjustment(w, r, service)
	}))
	mux.HandleFunc("GET /api/operator/accounts", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		page, pageSize, ok := operatorPagination(w, r)
		if !ok {
			return
		}
		data, status, err := app.operatorAccountPage(r.Context(), service, page, pageSize)
		if err != nil {
			writeSourceEnvelope(w, http.StatusBadGateway, "control-plane+sub2api", "unavailable", nil)
			return
		}
		writeSourceEnvelope(w, http.StatusOK, "control-plane+sub2api", status, data)
	}))
	mux.HandleFunc("GET /api/operator/overview", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		data, err := app.operatorOverview(r.Context(), service)
		if err != nil {
			writeSourceEnvelope(w, http.StatusBadGateway, "control-plane", "unavailable", nil)
			return
		}
		writeSourceEnvelope(w, http.StatusOK, "control-plane", "available", data)
	}))
	mux.HandleFunc("GET /api/operator/workspaces", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		page, pageSize, ok := operatorPagination(w, r)
		if !ok {
			return
		}
		data, status, err := app.operatorWorkspacePage(r.Context(), service, page, pageSize)
		if err != nil {
			writeSourceEnvelope(w, http.StatusInternalServerError, "control-plane+fabric+sub2api", "unavailable", nil)
			return
		}
		writeSourceEnvelope(w, http.StatusOK, "control-plane+fabric+sub2api", status, data)
	}))
	mux.HandleFunc("GET /api/operator/workspaces/{workspaceId}", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		data, found, err := app.operatorWorkspaceDetail(r.Context(), service, strings.TrimSpace(r.PathValue("workspaceId")))
		if err != nil {
			writeSourceEnvelope(w, http.StatusInternalServerError, "control-plane+fabric+ledger", "unavailable", nil)
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, "workspace_not_found")
			return
		}
		writeSourceEnvelope(w, http.StatusOK, "control-plane+fabric+ledger", "available", data)
	}))
	mux.HandleFunc("GET /api/operator/reconciliation", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		page, pageSize, ok := operatorPagination(w, r)
		if !ok {
			return
		}
		data, status, err := app.operatorReconciliationPage(r.Context(), page, pageSize)
		if err != nil {
			writeSourceEnvelope(w, http.StatusInternalServerError, "control-plane", "unavailable", nil)
			return
		}
		writeSourceEnvelope(w, http.StatusOK, "control-plane", status, data)
	}))
	mux.HandleFunc("GET /api/operator/health", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		writeSourceEnvelope(w, http.StatusOK, "control-plane", "available", app.operatorHealth(r.Context(), service))
	}))
	mux.HandleFunc("POST /api/operator/accounts", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		key, ok := requiredMutationKey(w, r)
		if !ok {
			return
		}
		input := decodeJSON(r)
		if !operatorProvisionShapeValid(input) {
			writeError(w, http.StatusBadRequest, "invalid_provision")
			return
		}
		email, err := canonicalEmail(stringValue(input["email"]))
		if err != nil {
			writeCreateUserError(w, err)
			return
		}
		accountID := "acct-" + stableID("account", email)[:18]
		user, err := app.createUser(r.Context(), service, map[string]any{"email": email, "password": input["password"], "accountId": accountID, "role": "owner"})
		if err != nil {
			writeCreateUserError(w, err)
			return
		}
		result := map[string]any{"operationId": "account-provision-" + stableID(key, email)[:18], "accountId": accountID, "status": "succeeded"}
		if err := app.appendAuditEvent(r, "account.provision", "account", accountID, accountID, nil, map[string]any{"userId": user["id"], "email": email}, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusCreated, result)
	}))
	mux.HandleFunc("POST /api/operator/accounts/{accountId}/disable", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		key, ok := requiredMutationKey(w, r)
		if !ok {
			return
		}
		accountID := strings.TrimSpace(r.PathValue("accountId"))
		input := decodeJSON(r)
		if !validAccountID(accountID) || !operatorDisableShapeValid(input, accountID) {
			writeError(w, http.StatusBadRequest, "invalid_account_disable")
			return
		}
		accounts, err := app.tables.ListAccounts(r.Context(), accountID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		account := findRecord(accounts, accountID)
		if account == nil {
			writeError(w, http.StatusNotFound, "account_not_found")
			return
		}
		withOperatorUserID(input, app.sessionUserID(r))
		input["userId"] = stringValue(account["ownerUserId"])
		user, err := app.disableUser(input)
		if err != nil {
			writeUserLifecycleError(w, err)
			return
		}
		result := map[string]any{"operationId": "account-disable-" + stableID(key, accountID)[:18], "accountId": accountID, "status": "succeeded"}
		if err := app.appendAuditEvent(r, "account.disable", "account", accountID, accountID, nil, map[string]any{"userId": user["id"], "reason": input["reason"]}, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}))
	mux.HandleFunc("GET /api/operator/archive", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		result, err := app.archiveState(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "archive_state_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}))
	mux.HandleFunc("POST /api/operator/archive-terminal-resources", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		if !confirmed(input, "confirm") {
			writeError(w, http.StatusBadRequest, "confirmation_required")
			return
		}
		result, err := app.archiveTerminalResources(r.Context(), input)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		if err := app.appendAuditEvent(r, "operator.archive_terminal_resources", "archive_job", stringValue(result["id"]), "", nil, result, "succeeded"); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}))
	mux.HandleFunc("POST /api/operator/workspace-launches/{operationId}/recover", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		key, ok := requiredMutationKey(w, r)
		if !ok {
			return
		}
		input := decodeJSON(r)
		operationID := strings.TrimSpace(r.PathValue("operationId"))
		if !workspaceLaunchRecoveryShapeValid(input) || operationID == "" || stringValue(input["billingOperationId"]) != operationID || !validBillingReviewOpaqueID(key) {
			writeError(w, http.StatusBadRequest, errInvalidBillingReview.Error())
			return
		}
		var approval *workspaceLaunchReadbackRecoveryApproval
		if raw, ok := input["approval"]; ok {
			approvalKey := key
			if approvalMap, ok := raw.(map[string]any); ok {
				approvalKey = stringValue(approvalMap["idempotencyKey"])
			}
			parsed, valid := workspaceLaunchReadbackRecoveryApprovalFromMap(raw, approvalKey)
			if !valid || !app.computeClaimCapabilityValid(r) {
				writeError(w, http.StatusBadRequest, errInvalidBillingReview.Error())
				return
			}
			approval = &parsed
		}
		evidenceRef := stringValue(input["evidenceRef"])
		if !validBillingReviewEvidenceRef(evidenceRef) {
			writeError(w, http.StatusBadRequest, "invalid_evidence_ref")
			return
		}
		resolution := billingReviewResolutionInput{
			ResourceType: "workspace_launch", ResourceID: operationID, AccountID: stringValue(input["accountId"]), BillingOperationID: operationID,
			EvidenceRef: evidenceRef, IdempotencyKey: key, Reviewer: app.sessionUserID(r), ReadbackApproval: approval,
		}
		result, replayed, err := app.recoverWorkspaceLaunchReviewWithReplay(r.Context(), service, resolution)
		if err != nil {
			writeBillingReviewResolutionError(w, err)
			return
		}
		if !replayed {
			audit := app.auditEvent(r, "workspace.launch.recover", "workspace", stringValue(result["workspaceId"]), resolution.AccountID, nil, mergeMaps(result, map[string]any{"evidenceRef": evidenceRef}), stringValue(result["status"]))
			audit["id"] = "audit-" + stableID("workspace.launch.recover", operationID, key)[:12]
			if err := app.tables.SaveAuditEvent(r.Context(), audit); err != nil {
				writeError(w, http.StatusInternalServerError, "state_persist_failed")
				return
			}
		}
		writeJSON(w, http.StatusOK, result)
	}))
	mux.HandleFunc("GET /api/operator/workspace-launches/{operationId}/readback-recovery-proof", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		operationID := strings.TrimSpace(r.PathValue("operationId"))
		if operationID == "" {
			writeError(w, http.StatusBadRequest, errInvalidBillingReview.Error())
			return
		}
		proof, err := app.diagnoseWorkspaceLaunchReadbackRecovery(r.Context(), service, operationID)
		if err != nil {
			writeError(w, http.StatusConflict, "workspace_launch_readback_unconfirmed")
			return
		}
		writeJSON(w, http.StatusOK, proof)
	}))
	mux.HandleFunc("POST /api/operator/workspace-launches/{operationId}/compute-claim-recovery/proof", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		operationID := strings.TrimSpace(r.PathValue("operationId"))
		input, ok := workspaceComputeClaimRecoveryRequestFromMap(operationID, decodeJSON(r), false)
		if !ok {
			writeError(w, http.StatusBadRequest, errWorkspaceComputeClaimInvalid.Error())
			return
		}
		proof, err := app.diagnoseWorkspaceComputeClaim(r.Context(), service, input)
		if err == nil {
			writeJSON(w, http.StatusOK, proof)
			return
		}
		if workspaceComputeClaimSafeFailure(proof) {
			writeJSON(w, http.StatusConflict, proof)
			return
		}
		writeWorkspaceComputeClaimError(w, err)
	}))
	mux.HandleFunc("POST /api/operator/workspace-launches/{operationId}/compute-claim-recovery/claim", app.protected(true, app.computeClaimCapabilityProtected(func(w http.ResponseWriter, r *http.Request) {
		key, ok := requiredMutationKey(w, r)
		if !ok {
			return
		}
		operationID := strings.TrimSpace(r.PathValue("operationId"))
		input, ok := workspaceComputeClaimRecoveryRequestFromMap(operationID, decodeJSON(r), true)
		if !ok || !validBillingReviewOpaqueID(key) {
			writeError(w, http.StatusBadRequest, errWorkspaceComputeClaimInvalid.Error())
			return
		}
		proof, err := app.claimWorkspaceCompute(r.Context(), service, input, key)
		if err == nil {
			writeJSON(w, http.StatusOK, proof)
			return
		}
		if workspaceComputeClaimSafeFailure(proof) {
			writeJSON(w, http.StatusConflict, proof)
			return
		}
		writeWorkspaceComputeClaimError(w, err)
	})))
	mux.HandleFunc("POST /api/operator/billing-reviews/{resourceType}/{id}/resolve", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		input := decodeJSON(r)
		if !billingReviewRequestShapeValid(input) {
			writeError(w, http.StatusBadRequest, errInvalidBillingReview.Error())
			return
		}
		key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if key == "" {
			writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
			return
		}
		if !validBillingReviewOpaqueID(key) {
			writeError(w, http.StatusBadRequest, "invalid_idempotency_key")
			return
		}
		evidenceRef := strings.TrimSpace(stringValue(input["evidenceRef"]))
		if !validBillingReviewEvidenceRef(evidenceRef) {
			writeError(w, http.StatusBadRequest, "invalid_evidence_ref")
			return
		}
		resolution := billingReviewResolutionInput{
			ResourceType: strings.TrimSpace(r.PathValue("resourceType")), ResourceID: strings.TrimSpace(r.PathValue("id")),
			AccountID: strings.TrimSpace(stringValue(input["accountId"])), BillingOperationID: strings.TrimSpace(stringValue(input["billingOperationId"])),
			Decision: strings.TrimSpace(stringValue(input["decision"])), EvidenceRef: evidenceRef, IdempotencyKey: key, Reviewer: app.sessionUserID(r),
		}
		if resolution.ResourceType != "workspace" {
			writeError(w, http.StatusBadRequest, errInvalidBillingReview.Error())
			return
		}
		result, err := app.resolveWorkspaceRenewalReview(r.Context(), service, resolution)
		if err != nil {
			writeBillingReviewResolutionError(w, err)
			return
		}
		if err := app.appendBillingReviewResolutionAudit(r, key, result); err != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}))
}

func (app *controlPlaneServer) computeClaimCapabilityProtected(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !app.computeClaimCapabilityValid(r) {
			writeError(w, http.StatusForbidden, "workspace_compute_claim_capability_invalid")
			return
		}
		next(w, r)
	}
}

func (app *controlPlaneServer) computeClaimCapabilityValid(r *http.Request) bool {
	expected := os.Getenv("OPL_INTERNAL_SERVICE_TOKEN")
	want := sha256.Sum256([]byte(expected))
	got := sha256.Sum256([]byte(r.Header.Get("x-opl-compute-claim-capability")))
	return expected != "" && subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

func workspaceComputeClaimRecoveryRequestFromMap(operationID string, input map[string]any, approved bool) (workspaceComputeClaimRecoveryRequest, bool) {
	want := []string{"accountId", "workspaceId", "computeAllocationId", "storageId", "packageId", "poolId", "nodePoolId", "machineName", "nodeName", "cvmInstanceId", "privateIp", "instanceType", "zone"}
	if approved {
		want = append(want, "approvalId", "approvalDigest", "expiresAt", "mergedMainSha", "cloudImageDigest", "workspaceImageDigest", "customerEmail", "recoveryKey", "resources", "attemptLimits", "allowedWrites", "forbiddenWrites", "confirm")
	}
	if operationID == "" || !exactWorkspaceComputeClaimKeys(input, want) {
		return workspaceComputeClaimRecoveryRequest{}, false
	}
	stringFields := append([]string(nil), want...)
	if approved {
		stringFields = append([]string(nil), want[:21]...)
		stringFields = append(stringFields, "confirm")
	}
	values := make(map[string]string, len(stringFields))
	for _, field := range stringFields {
		value, ok := input[field].(string)
		if !ok || value == "" || value != strings.TrimSpace(value) {
			return workspaceComputeClaimRecoveryRequest{}, false
		}
		values[field] = value
	}
	request := workspaceComputeClaimRecoveryRequest{
		LaunchOperationID: operationID, AccountID: values["accountId"], WorkspaceID: values["workspaceId"],
		ComputeID: values["computeAllocationId"], StorageID: values["storageId"], PackageID: values["packageId"],
		PoolID: values["poolId"], NodePoolID: values["nodePoolId"], MachineName: values["machineName"], NodeName: values["nodeName"],
		CVMInstanceID: values["cvmInstanceId"], PrivateIP: values["privateIp"], InstanceType: values["instanceType"], Zone: values["zone"],
		ApprovalID: values["approvalId"], ApprovalDigest: values["approvalDigest"], ExpiresAt: values["expiresAt"],
		MergedMainSHA: values["mergedMainSha"], CloudImageDigest: values["cloudImageDigest"], WorkspaceImageDigest: values["workspaceImageDigest"],
		CustomerEmail: values["customerEmail"], RecoveryKey: values["recoveryKey"], Confirmation: values["confirm"],
	}
	if request.PackageID != "basic" && request.PackageID != "pro" || !strings.HasPrefix(request.CVMInstanceID, "ins-") {
		return workspaceComputeClaimRecoveryRequest{}, false
	}
	if approved {
		expiresAt, expiresErr := time.Parse(time.RFC3339, request.ExpiresAt)
		customerEmail, emailErr := canonicalEmail(request.CustomerEmail)
		resources, resourcesOK := workspaceComputeClaimApprovalResourcesFromMap(input["resources"])
		limits, limitsOK := workspaceComputeClaimAttemptLimitsFromMap(input["attemptLimits"])
		allowedWrites, allowedOK := workspaceComputeClaimStringList(input["allowedWrites"])
		forbiddenWrites, forbiddenOK := workspaceComputeClaimStringList(input["forbiddenWrites"])
		if !validBillingReviewOpaqueID(request.ApprovalID) || !validBillingReviewOpaqueID(request.RecoveryKey) ||
			!computeClaimApprovalDigestPattern.MatchString(request.ApprovalDigest) || !computeClaimMergedSHAPattern.MatchString(request.MergedMainSHA) ||
			!computeClaimCloudDigestPattern.MatchString(request.CloudImageDigest) || !computeClaimCloudDigestPattern.MatchString(request.WorkspaceImageDigest) ||
			expiresErr != nil || !expiresAt.After(time.Now().UTC()) || emailErr != nil || customerEmail != request.CustomerEmail ||
			request.Confirmation != "RECOVER_PROVEN_COMPUTE_AND_CONTINUE_ORIGINAL_LAUNCH" || !resourcesOK || !limitsOK || !allowedOK || !forbiddenOK ||
			!workspaceComputeClaimStorageBindingValid(resources.StorageState, resources.StorageProviderResourceID) ||
			!workspaceComputeClaimAttemptLimitsExact(limits) || !equalWorkspaceComputeClaimStrings(allowedWrites, workspaceComputeClaimAllowedWritesForStorage(resources.StorageState)) ||
			!equalWorkspaceComputeClaimStrings(forbiddenWrites, workspaceComputeClaimForbiddenWrites) {
			return workspaceComputeClaimRecoveryRequest{}, false
		}
		request.Resources, request.AttemptLimits = resources, limits
		request.AllowedWrites, request.ForbiddenWrites = allowedWrites, forbiddenWrites
	}
	return request, true
}

func exactWorkspaceComputeClaimKeys(input map[string]any, want []string) bool {
	if len(input) != len(want) {
		return false
	}
	for _, key := range want {
		if _, ok := input[key]; !ok {
			return false
		}
	}
	return true
}

func workspaceComputeClaimApprovalResourcesFromMap(value any) (workspaceComputeClaimApprovalResources, bool) {
	raw, ok := value.(map[string]any)
	want := []string{"computeOperationId", "storageOperationId", "storageState", "storageProviderResourceId", "attachmentId", "attachmentOperationId", "workspaceApiKeyId", "gatewaySecretRef", "secretOperationId", "runtimeId", "runtimeOperationId", "receiptOperationId"}
	if !ok || !exactWorkspaceComputeClaimKeys(raw, want) {
		return workspaceComputeClaimApprovalResources{}, false
	}
	values := make(map[string]string, len(want))
	for _, field := range want {
		item, ok := raw[field].(string)
		if !ok || field != "storageProviderResourceId" && item == "" || item != strings.TrimSpace(item) {
			return workspaceComputeClaimApprovalResources{}, false
		}
		values[field] = item
	}
	workspaceAPIKeyID, err := strconv.ParseInt(values["workspaceApiKeyId"], 10, 64)
	if err != nil || workspaceAPIKeyID <= 0 || strings.HasPrefix(values["workspaceApiKeyId"], "0") {
		return workspaceComputeClaimApprovalResources{}, false
	}
	return workspaceComputeClaimApprovalResources{
		ComputeOperationID: values["computeOperationId"], StorageOperationID: values["storageOperationId"],
		StorageState: values["storageState"], StorageProviderResourceID: values["storageProviderResourceId"],
		AttachmentID: values["attachmentId"], AttachmentOperationID: values["attachmentOperationId"], WorkspaceAPIKeyID: values["workspaceApiKeyId"],
		GatewaySecretRef: values["gatewaySecretRef"], SecretOperationID: values["secretOperationId"], RuntimeID: values["runtimeId"],
		RuntimeOperationID: values["runtimeOperationId"], ReceiptOperationID: values["receiptOperationId"],
	}, true
}

func workspaceComputeClaimAttemptLimitsFromMap(value any) (workspaceComputeClaimAttemptLimits, bool) {
	raw, ok := value.(map[string]any)
	want := []string{"claim", "storage", "attachment", "secret", "runtime", "activation", "receipt"}
	if !ok || !exactWorkspaceComputeClaimKeys(raw, want) {
		return workspaceComputeClaimAttemptLimits{}, false
	}
	claim, ok := raw["claim"].(map[string]any)
	if !ok || !exactWorkspaceComputeClaimKeys(claim, []string{"sub2api", "tencent", "kubernetes"}) {
		return workspaceComputeClaimAttemptLimits{}, false
	}
	integer := func(source map[string]any, field string) (int, bool) {
		value, ok := source[field].(float64)
		if !ok || value < 0 || value != float64(int(value)) {
			return 0, false
		}
		return int(value), true
	}
	sub2API, sub2APIOK := integer(claim, "sub2api")
	tencent, tencentOK := integer(claim, "tencent")
	kubernetes, kubernetesOK := integer(claim, "kubernetes")
	storage, storageOK := integer(raw, "storage")
	attachment, attachmentOK := integer(raw, "attachment")
	secret, secretOK := integer(raw, "secret")
	runtime, runtimeOK := integer(raw, "runtime")
	activation, activationOK := integer(raw, "activation")
	receipt, receiptOK := integer(raw, "receipt")
	if !sub2APIOK || !tencentOK || !kubernetesOK || !storageOK || !attachmentOK || !secretOK || !runtimeOK || !activationOK || !receiptOK {
		return workspaceComputeClaimAttemptLimits{}, false
	}
	return workspaceComputeClaimAttemptLimits{
		Claim:   workspaceComputeClaimProviderAttemptLimits{Sub2API: sub2API, Tencent: tencent, Kubernetes: kubernetes},
		Storage: storage, Attachment: attachment, Secret: secret, Runtime: runtime, Activation: activation, Receipt: receipt,
	}, true
}

func workspaceComputeClaimStringList(value any) ([]string, bool) {
	raw, ok := value.([]any)
	if !ok {
		return nil, false
	}
	items := make([]string, len(raw))
	for index, value := range raw {
		item, ok := value.(string)
		if !ok || item == "" || item != strings.TrimSpace(item) {
			return nil, false
		}
		items[index] = item
	}
	return items, true
}

func workspaceLaunchReadbackRecoveryApprovalFromMap(value any, key string) (workspaceLaunchReadbackRecoveryApproval, bool) {
	raw, ok := value.(map[string]any)
	want := []string{
		"schemaVersion", "approvalId", "approvalDigest", "expiresAt", "mergedMainSha", "cloudImageDigest", "workspaceImageDigest",
		"confirmation", "idempotencyKey", "recoveryKey", "stage", "customer", "target", "resources", "operationIds", "attemptBudget",
		"allowedWrites", "forbiddenWrites",
	}
	if !ok || !exactWorkspaceComputeClaimKeys(raw, want) || numberField(raw, "schemaVersion", 0) != 1 {
		return workspaceLaunchReadbackRecoveryApproval{}, false
	}
	for _, field := range []string{"approvalId", "approvalDigest", "expiresAt", "mergedMainSha", "cloudImageDigest", "workspaceImageDigest", "confirmation", "idempotencyKey", "recoveryKey", "stage"} {
		item, ok := raw[field].(string)
		if !ok || item == "" || item != strings.TrimSpace(item) {
			return workspaceLaunchReadbackRecoveryApproval{}, false
		}
	}
	nestedKeys := map[string][]string{
		"customer": {"email", "accountId", "ownerUserId"},
		"target": {
			"launchOperationId", "accountId", "workspaceId", "computeAllocationId", "storageId", "packageId", "poolId", "nodePoolId",
			"machineName", "nodeName", "cvmInstanceId", "privateIp", "instanceType", "zone", "chargeType", "periodMonths", "renewFlag",
			"deadline", "storageGb", "autoRenew", "priceVersion", "totalChargeUsdMicros", "periodStart", "paidThrough", "billingAnchorDay",
		},
		"resources": {
			"computeAllocationId", "computeProviderResourceId", "storageVolumeId", "storageProviderResourceId", "storageZone", "storageSizeGb",
			"storageChargeType", "storageRenewFlag", "storageDeadline", "attachmentId", "attachmentProviderId", "gatewaySecretRef",
			"gatewaySecretFingerprint", "workspaceApiKeyId", "runtimeId", "runtimeServiceName", "receiptId",
		},
		"operationIds": {
			"launchOperationId", "launchRequestHash", "machineOwnershipId", "compute", "storage", "attachment", "secret", "runtime",
			"activationOperationId", "receiptOperationId",
		},
		"attemptBudget": {"attempted", "confirmed", "unknown", "max"},
	}
	for field, keys := range nestedKeys {
		item, ok := raw[field].(map[string]any)
		if !ok || !exactWorkspaceComputeClaimKeys(item, keys) {
			return workspaceLaunchReadbackRecoveryApproval{}, false
		}
	}
	for _, field := range []string{"customer"} {
		for _, value := range raw[field].(map[string]any) {
			item, ok := value.(string)
			if !ok || item == "" || item != strings.TrimSpace(item) {
				return workspaceLaunchReadbackRecoveryApproval{}, false
			}
		}
	}
	operationFields := []string{"compute", "storage", "attachment", "secret", "runtime"}
	operationIdentityKeys := []string{"idempotencyKey", "fabricRecordId", "fabricOperationId", "requestHash", "resourceOperationId", "providerOperationId"}
	for _, field := range operationFields {
		item, ok := raw["operationIds"].(map[string]any)[field].(map[string]any)
		if !ok || !exactWorkspaceComputeClaimKeys(item, operationIdentityKeys) {
			return workspaceLaunchReadbackRecoveryApproval{}, false
		}
		for _, value := range item {
			text, ok := value.(string)
			if !ok || text != strings.TrimSpace(text) {
				return workspaceLaunchReadbackRecoveryApproval{}, false
			}
		}
	}
	budget := raw["attemptBudget"].(map[string]any)
	if numberField(budget, "attempted", -1) != 1 || numberField(budget, "confirmed", -1) != 0 || numberField(budget, "unknown", -1) != 1 || numberField(budget, "max", -1) != 1 {
		return workspaceLaunchReadbackRecoveryApproval{}, false
	}
	allowedWrites, allowedOK := workspaceComputeClaimStringList(raw["allowedWrites"])
	forbiddenWrites, forbiddenOK := workspaceComputeClaimStringList(raw["forbiddenWrites"])
	var approval workspaceLaunchReadbackRecoveryApproval
	if !allowedOK || !forbiddenOK || jsonRoundTrip(raw, &approval) != nil {
		return workspaceLaunchReadbackRecoveryApproval{}, false
	}
	_, expiresErr := time.Parse(time.RFC3339, approval.ExpiresAt)
	email, emailErr := canonicalEmail(approval.Customer.Email)
	if approval.SchemaVersion != 1 || !validBillingReviewOpaqueID(approval.ApprovalID) || !validBillingReviewOpaqueID(approval.RecoveryKey) ||
		!computeClaimApprovalDigestPattern.MatchString(approval.ApprovalDigest) || !computeClaimMergedSHAPattern.MatchString(approval.MergedMainSHA) ||
		!computeClaimCloudDigestPattern.MatchString(approval.CloudImageDigest) || !computeClaimCloudDigestPattern.MatchString(approval.WorkspaceImageDigest) ||
		expiresErr != nil || emailErr != nil || email != approval.Customer.Email || approval.IdempotencyKey != key ||
		approval.Confirmation != workspaceLaunchReadbackRecoveryConfirmation || !workspaceLaunchReadbackRecoveryStageValid(approval.Stage) ||
		approval.Target.LaunchOperationID == "" || approval.Target.AccountID == "" || approval.Target.WorkspaceID == "" || approval.Target.ComputeAllocationID == "" ||
		approval.Target.StorageID == "" || approval.Target.PoolID == "" || approval.Target.NodePoolID == "" || approval.Target.MachineName == "" ||
		approval.Target.NodeName == "" || approval.Target.CVMInstanceID == "" || approval.Target.PrivateIP == "" || approval.Target.InstanceType == "" ||
		approval.Target.Zone == "" || approval.Target.ChargeType != "PREPAID" || approval.Target.PeriodMonths != 1 ||
		approval.Target.RenewFlag != "NOTIFY_AND_MANUAL_RENEW" || approval.Target.StorageGB <= 0 || approval.Target.PriceVersion == "" ||
		approval.Target.TotalChargeUSDMicros <= 0 || approval.Target.BillingAnchorDay < 1 || approval.Target.BillingAnchorDay > 31 ||
		!strings.HasPrefix(approval.Resources.ComputeProviderResourceID, "ins-") || !strings.HasPrefix(approval.Resources.StorageProviderResourceID, "disk-") ||
		approval.Resources.StorageZone == "" || approval.Resources.StorageSizeGB <= 0 || approval.Resources.StorageChargeType != "PREPAID" ||
		approval.Resources.StorageRenewFlag != "NOTIFY_AND_MANUAL_RENEW" || approval.Resources.WorkspaceAPIKeyID <= 0 ||
		!equalWorkspaceComputeClaimStrings(allowedWrites, workspaceLaunchReadbackRecoveryAllowedWrites(approval.Stage)) ||
		!equalWorkspaceComputeClaimStrings(forbiddenWrites, workspaceLaunchReadbackRecoveryForbiddenWrites) {
		return workspaceLaunchReadbackRecoveryApproval{}, false
	}
	approval.AllowedWrites = allowedWrites
	approval.ForbiddenWrites = forbiddenWrites
	return approval, true
}

func workspaceComputeClaimSafeFailure(proof clients.ComputeClaimRecoveryProof) bool {
	return proof.SchemaVersion == 1 && proof.Reason != "" && proof.Reason != "none" && safeWorkspaceComputeClaimReason(proof.Reason) &&
		proof.Sub2APIMutationCount == 0 && workspaceComputeClaimEvidenceMatches(proof, false) &&
		(proof.FailureStage == "" && proof.ProviderErrorClass == "" || proof.FailureStage != "" && proof.ProviderErrorClass != "")
}

func writeWorkspaceComputeClaimError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errWorkspaceComputeClaimInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, errBillingReviewNotFound):
		writeError(w, http.StatusNotFound, "workspace_launch_not_found")
	case errors.Is(err, errWorkspaceComputeClaimIdentity), errors.Is(err, errWorkspaceComputeClaimNotPending), errors.Is(err, errWorkspaceComputeClaimProof),
		errors.Is(err, errWorkspaceLaunchCASConflict):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusBadGateway, "workspace_compute_claim_unavailable")
	}
}

func operatorPagination(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	page, pageSize := 1, 20
	var err error
	if raw := strings.TrimSpace(r.URL.Query().Get("page")); raw != "" {
		page, err = strconv.Atoi(raw)
	}
	if err == nil {
		if raw := strings.TrimSpace(r.URL.Query().Get("pageSize")); raw != "" {
			pageSize, err = strconv.Atoi(raw)
		}
	}
	if err != nil || page <= 0 || pageSize <= 0 || pageSize > 50 {
		writeError(w, http.StatusBadRequest, "invalid_pagination")
		return 0, 0, false
	}
	return page, pageSize, true
}

func operatorProvisionShapeValid(input map[string]any) bool {
	if len(input) < 2 || len(input) > 3 {
		return false
	}
	for key := range input {
		if key != "email" && key != "password" && key != "name" {
			return false
		}
	}
	password, passwordOK := input["password"].(string)
	if _, emailOK := input["email"].(string); !emailOK || !passwordOK || password == "" {
		return false
	}
	if raw, exists := input["name"]; exists {
		name, ok := raw.(string)
		if !ok || name != strings.TrimSpace(name) || name == "" || len([]rune(name)) > 100 {
			return false
		}
	}
	return true
}

func operatorDisableShapeValid(input map[string]any, accountID string) bool {
	if len(input) != 2 || stringValue(input["confirmationAccountId"]) != accountID {
		return false
	}
	for key := range input {
		if key != "confirmationAccountId" && key != "reason" {
			return false
		}
	}
	reason, ok := input["reason"].(string)
	return ok && reason == strings.TrimSpace(reason) && reason != "" && len([]rune(reason)) <= 200
}

func (app *controlPlaneServer) operatorAccountPage(ctx context.Context, service *controlplane.Service, page, pageSize int) (map[string]any, string, error) {
	accountPage, err := app.tables.PageAccounts(ctx, tablePageQuery{Offset: (page - 1) * pageSize, Limit: pageSize})
	if err != nil {
		return nil, "", err
	}
	local := make([]map[string]any, 0, len(accountPage.Items))
	remoteIDs := make([]int64, 0, len(accountPage.Items))
	accountIDs := make([]string, 0, len(accountPage.Items))
	for _, account := range accountPage.Items {
		remoteID, ok := positiveIntegerField(account, "sub2apiUserId")
		owner, ownerOK, ownerErr := app.tables.GetUser(ctx, stringValue(account["ownerUserId"]))
		if ownerErr != nil {
			return nil, "", ownerErr
		}
		if !ok || !ownerOK || !ownsAccount(account, owner) {
			return nil, "", errAccountIdentityConflict
		}
		local = append(local, map[string]any{"account": account, "owner": owner, "remoteId": remoteID})
		remoteIDs = append(remoteIDs, remoteID)
		accountIDs = append(accountIDs, stringValue(account["id"]))
	}
	if len(local) == 0 {
		return map[string]any{"items": []any{}, "total": accountPage.Total, "page": page, "pageSize": pageSize}, "empty", nil
	}
	workspaceCounts, err := app.tables.CountWorkspacesByAccount(ctx, accountIDs)
	if err != nil {
		return nil, "", err
	}
	items := make([]any, 0, len(local))
	for _, joined := range local {
		account := joined["account"].(map[string]any)
		owner := joined["owner"].(map[string]any)
		remoteID := joined["remoteId"].(int64)
		ownerStatus := stringValue(owner["status"])
		if ownerStatus != "active" {
			ownerStatus = "disabled"
		}
		item := map[string]any{
			"accountId": stringValue(account["id"]), "consoleUserId": stringValue(owner["id"]), "role": stringValue(owner["role"]),
			"sub2apiUserId": strconv.FormatInt(remoteID, 10), "email": normalizeEmail(stringValue(owner["email"])), "status": ownerStatus,
			"gatewayIdentity": sourceEnvelope("sub2api", "unavailable", nil, ""),
			"wallet":          sourceEnvelope("sub2api", "unavailable", nil, ""),
			"usage":           sourceEnvelope("sub2api", "unavailable", nil, ""),
			"keyCount":        sourceEnvelope("sub2api", "unavailable", nil, ""),
		}
		item["workspaceCount"] = sourceEnvelope("control-plane", "available", workspaceCounts[stringValue(account["id"])], "")
		items = append(items, item)
	}

	pageCtx, cancel := context.WithTimeout(ctx, operatorPageReadTimeout)
	defer cancel()
	totalGate := make(chan struct{}, operatorPageTotalConcurrency)
	var remoteByID map[int64]clients.Sub2APIUser
	var usageByID map[int64]clients.Sub2APIBatchUserUsage
	var usageErr error
	var remoteReads sync.WaitGroup
	remoteReads.Add(3)
	go func() {
		defer remoteReads.Done()
		remoteByID = app.operatorCurrentPageUsers(pageCtx, service, local, totalGate)
	}()
	go func() {
		defer remoteReads.Done()
		release, ok := operatorPageReadPermit(pageCtx, totalGate)
		if !ok {
			usageErr = pageCtx.Err()
			return
		}
		defer release()
		usageByID, usageErr = service.Sub2APIBatchUsersUsage(pageCtx, remoteIDs)
	}()
	go func() {
		defer remoteReads.Done()
		app.populateOperatorKeyCounts(pageCtx, service, items, totalGate)
	}()
	remoteReads.Wait()

	for index, joined := range local {
		owner := joined["owner"].(map[string]any)
		remoteID := joined["remoteId"].(int64)
		item := items[index].(map[string]any)
		remote, remoteOK := remoteByID[remoteID]
		remoteOK = remoteOK && remote.ID == remoteID && remote.Email == normalizeEmail(stringValue(owner["email"])) && (remote.Status == "active" || remote.Status == "disabled")
		if !remoteOK {
			continue
		}
		updatedAt := remote.UpdatedAt.UTC().Format(time.RFC3339Nano)
		item["gatewayIdentity"] = sourceEnvelope("sub2api", "available", map[string]any{"userId": strconv.FormatInt(remote.ID, 10), "email": remote.Email, "status": remote.Status}, updatedAt)
		if remote.BalanceUnavailable {
			item["wallet"] = sourceEnvelope("sub2api", "unavailable", nil, "")
		} else {
			item["wallet"] = sourceEnvelope("sub2api", "available", map[string]any{"userId": strconv.FormatInt(remote.ID, 10), "currency": "USD", "usdMicros": strconv.FormatInt(remote.BalanceUSDMicros, 10), "status": remote.Status}, updatedAt)
		}
		usage, usageOK := usageByID[remoteID]
		if usageErr != nil || !usageOK || usage.UserID != remoteID {
			item["usage"] = sourceEnvelope("sub2api", "unavailable", nil, "")
		} else {
			platforms := make([]any, 0, len(usage.ByPlatform))
			for _, platform := range usage.ByPlatform {
				platforms = append(platforms, map[string]any{"platform": platform.Platform, "todayActualCostUsdMicros": platform.TodayActualCostUSDMicros, "totalActualCostUsdMicros": platform.TotalActualCostUSDMicros})
			}
			item["usage"] = sourceEnvelope("sub2api", "available", map[string]any{"todayActualCostUsdMicros": usage.TodayActualCostUSDMicros, "totalActualCostUsdMicros": usage.TotalActualCostUSDMicros, "byPlatform": platforms}, "")
		}
	}
	status := "available"
	if len(items) == 0 {
		status = "empty"
	}
	return map[string]any{"items": items, "total": accountPage.Total, "page": page, "pageSize": pageSize}, status, nil
}

func operatorPageReadPermit(ctx context.Context, gates ...chan struct{}) (func(), bool) {
	acquired := make([]chan struct{}, 0, len(gates))
	for _, gate := range gates {
		select {
		case gate <- struct{}{}:
			acquired = append(acquired, gate)
		case <-ctx.Done():
			for index := len(acquired) - 1; index >= 0; index-- {
				<-acquired[index]
			}
			return nil, false
		}
	}
	return func() {
		for index := len(acquired) - 1; index >= 0; index-- {
			<-acquired[index]
		}
	}, true
}

func (app *controlPlaneServer) operatorCurrentPageUsers(ctx context.Context, service *controlplane.Service, local []map[string]any, totalGate chan struct{}) map[int64]clients.Sub2APIUser {
	type result struct {
		user clients.Sub2APIUser
	}
	results := make(chan result, len(local))
	laneGate := make(chan struct{}, operatorPageLaneConcurrency)
	var wait sync.WaitGroup
	for _, joined := range local {
		remoteID := joined["remoteId"].(int64)
		wait.Add(1)
		go func() {
			defer wait.Done()
			release, ok := operatorPageReadPermit(ctx, laneGate, totalGate)
			if !ok {
				return
			}
			defer release()
			user, err := service.Sub2APIAdminUser(ctx, remoteID)
			if err != nil {
				return
			}
			results <- result{user: user}
		}()
	}
	wait.Wait()
	close(results)
	remoteByID := make(map[int64]clients.Sub2APIUser, len(local))
	for result := range results {
		remoteByID[result.user.ID] = result.user
	}
	return remoteByID
}

func (app *controlPlaneServer) populateOperatorKeyCounts(ctx context.Context, service *controlplane.Service, items []any, totalGate chan struct{}) {
	type keyCountResult struct {
		index int
		count int
		err   error
	}
	results := make(chan keyCountResult, len(items))
	laneGate := make(chan struct{}, operatorPageLaneConcurrency)
	var wait sync.WaitGroup
	for index, raw := range items {
		item, ok := raw.(map[string]any)
		remoteID, err := strconv.ParseInt(stringValue(item["sub2apiUserId"]), 10, 64)
		if !ok || err != nil || remoteID <= 0 {
			continue
		}
		wait.Add(1)
		go func(index int, userID int64) {
			defer wait.Done()
			release, ok := operatorPageReadPermit(ctx, laneGate, totalGate)
			if !ok {
				results <- keyCountResult{index: index, err: ctx.Err()}
				return
			}
			defer release()
			count, err := service.AdminUserKeyCount(ctx, userID)
			results <- keyCountResult{index: index, count: count, err: err}
		}(index, remoteID)
	}
	wait.Wait()
	close(results)
	for result := range results {
		if result.err != nil {
			continue
		}
		items[result.index].(map[string]any)["keyCount"] = sourceEnvelope("sub2api", "available", result.count, "")
	}
}

func authoritativeSourceTimestamp(value any) string {
	raw := stringValue(value)
	if raw == "" {
		return ""
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return ""
	}
	return parsed.UTC().Format(time.RFC3339Nano)
}

type operatorWorkspaceFacts struct {
	accounts          []map[string]any
	users             []map[string]any
	computes          []map[string]any
	storages          []map[string]any
	attachments       []map[string]any
	providerFacts     map[string]clients.ProviderFact
	keyUsage          map[int64]clients.Sub2APIBatchKeyUsage
	keyUsageAvailable bool
}

func (app *controlPlaneServer) operatorWorkspacePage(ctx context.Context, service *controlplane.Service, page, pageSize int) (map[string]any, string, error) {
	workspacePage, err := app.tables.PageWorkspaces(ctx, "", tablePageQuery{Offset: (page - 1) * pageSize, Limit: pageSize})
	if err != nil {
		return nil, "", err
	}
	selected := workspacePage.Items
	facts, err := app.loadOperatorWorkspaceFacts(ctx, service, selected)
	if err != nil {
		return nil, "", err
	}
	items := make([]any, 0, len(selected))
	for _, workspace := range selected {
		items = append(items, app.operatorWorkspaceDTO(ctx, service, workspace, facts, false))
	}
	status := "available"
	if len(items) == 0 {
		status = "empty"
	}
	return map[string]any{"items": items, "total": workspacePage.Total, "page": page, "pageSize": pageSize}, status, nil
}

func (app *controlPlaneServer) operatorWorkspaceDetail(ctx context.Context, service *controlplane.Service, workspaceID string) (map[string]any, bool, error) {
	if workspaceID == "" {
		return nil, false, nil
	}
	workspace, ok, err := app.tables.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	facts, err := app.loadOperatorWorkspaceFacts(ctx, service, []map[string]any{workspace})
	if err != nil {
		return nil, false, err
	}
	return app.operatorWorkspaceDTO(ctx, service, workspace, facts, true), true, nil
}

func (app *controlPlaneServer) loadOperatorWorkspaceFacts(ctx context.Context, service *controlplane.Service, workspaces []map[string]any) (operatorWorkspaceFacts, error) {
	var facts operatorWorkspaceFacts
	seenRecords := map[string]bool{}
	appendRecord := func(kind, id string, load func(context.Context, string) (map[string]any, bool, error), target *[]map[string]any) error {
		if id == "" || seenRecords[kind+":"+id] {
			return nil
		}
		seenRecords[kind+":"+id] = true
		row, ok, err := load(ctx, id)
		if err != nil {
			return err
		}
		if ok {
			*target = append(*target, row)
		}
		return nil
	}
	for _, workspace := range workspaces {
		for _, item := range []struct {
			kind   string
			id     string
			load   func(context.Context, string) (map[string]any, bool, error)
			target *[]map[string]any
		}{
			{"account", firstNonEmpty(stringValue(workspace["ownerAccountId"]), stringValue(workspace["accountId"])), app.tables.GetAccount, &facts.accounts},
			{"user", stringValue(workspace["ownerUserId"]), app.tables.GetUser, &facts.users},
			{"compute", firstNonEmpty(stringValue(workspace["currentComputeAllocationId"]), stringValue(workspace["computeAllocationId"])), app.tables.GetCompute, &facts.computes},
			{"storage", stringValue(workspace["storageId"]), app.tables.GetStorage, &facts.storages},
			{"attachment", firstNonEmpty(stringValue(workspace["currentAttachmentId"]), stringValue(workspace["attachmentId"])), app.tables.GetAttachment, &facts.attachments},
		} {
			if err := appendRecord(item.kind, item.id, item.load, item.target); err != nil {
				return facts, err
			}
		}
	}
	facts.providerFacts = app.loadOperatorProviderFacts(ctx, service, workspaces, facts)
	keyIDs := make([]int64, 0, len(workspaces))
	seen := map[int64]struct{}{}
	for _, workspace := range workspaces {
		keyID, ok := positiveIntegerField(workspace, "workspaceApiKeyId")
		if !ok {
			continue
		}
		if _, exists := seen[keyID]; exists {
			continue
		}
		seen[keyID] = struct{}{}
		keyIDs = append(keyIDs, keyID)
	}
	if len(keyIDs) > 0 && len(keyIDs) <= 50 {
		var err error
		facts.keyUsage, err = service.Sub2APIBatchKeysUsage(ctx, keyIDs)
		facts.keyUsageAvailable = err == nil
	}
	return facts, nil
}

func (app *controlPlaneServer) loadOperatorProviderFacts(ctx context.Context, service *controlplane.Service, workspaces []map[string]any, facts operatorWorkspaceFacts) map[string]clients.ProviderFact {
	inputs := operatorProviderFactInputs(workspaces, facts)
	result := make(map[string]clients.ProviderFact, len(inputs))
	for start := 0; start < len(inputs); start += 50 {
		end := start + 50
		if end > len(inputs) {
			end = len(inputs)
		}
		requested := make(map[string]struct{}, end-start)
		for _, input := range inputs[start:end] {
			requested[operatorProviderFactKey(input.AccountID, input.WorkspaceID, input.ResourceType, input.ResourceID)] = struct{}{}
		}
		batch, err := service.ProviderFactsBatch(ctx, clients.ProviderFactsBatchInput{Items: inputs[start:end]})
		if err != nil {
			continue
		}
		duplicates := map[string]struct{}{}
		for _, fact := range batch.Items {
			key := operatorProviderFactKey(fact.AccountID, fact.WorkspaceID, fact.ResourceType, fact.ResourceID)
			if _, ok := requested[key]; !ok {
				continue
			}
			if _, exists := result[key]; exists {
				delete(result, key)
				duplicates[key] = struct{}{}
				continue
			}
			if _, duplicate := duplicates[key]; !duplicate {
				result[key] = fact
			}
		}
	}
	return result
}

func operatorProviderFactInputs(workspaces []map[string]any, facts operatorWorkspaceFacts) []clients.ProviderFactInput {
	workspaceByID := make(map[string]map[string]any, len(workspaces))
	for _, workspace := range workspaces {
		if workspaceID := stringValue(workspace["id"]); workspaceID != "" {
			workspaceByID[workspaceID] = workspace
		}
	}
	inputs := make([]clients.ProviderFactInput, 0, len(workspaces)*4)
	appendRows := func(resourceType string, rows []map[string]any) {
		for _, row := range rows {
			workspaceID, resourceID := stringValue(row["workspaceId"]), stringValue(row["id"])
			workspace := workspaceByID[workspaceID]
			if workspace == nil || resourceID == "" {
				continue
			}
			inputs = append(inputs, clients.ProviderFactInput{
				AccountID:   firstNonEmpty(stringValue(workspace["ownerAccountId"]), stringValue(workspace["accountId"])),
				WorkspaceID: workspaceID, ResourceType: resourceType, ResourceID: resourceID,
			})
		}
	}
	appendRows("compute", facts.computes)
	appendRows("storage", facts.storages)
	appendRows("attachment", facts.attachments)
	for _, workspace := range workspaces {
		if runtimeID := stringValue(workspace["runtimeId"]); runtimeID != "" {
			inputs = append(inputs, clients.ProviderFactInput{
				AccountID:   firstNonEmpty(stringValue(workspace["ownerAccountId"]), stringValue(workspace["accountId"])),
				WorkspaceID: stringValue(workspace["id"]), ResourceType: "runtime", ResourceID: runtimeID,
			})
		}
	}
	sort.Slice(inputs, func(i, j int) bool {
		left := inputs[i].WorkspaceID + ":" + inputs[i].ResourceType + ":" + inputs[i].ResourceID
		right := inputs[j].WorkspaceID + ":" + inputs[j].ResourceType + ":" + inputs[j].ResourceID
		return left < right
	})
	return inputs
}

func operatorProviderFactKey(accountID, workspaceID, resourceType, resourceID string) string {
	return accountID + "\x00" + workspaceID + "\x00" + resourceType + "\x00" + resourceID
}

func (app *controlPlaneServer) operatorWorkspaceDTO(ctx context.Context, service *controlplane.Service, workspace map[string]any, facts operatorWorkspaceFacts, liveLedger bool) map[string]any {
	workspaceID := stringValue(workspace["id"])
	accountID := firstNonEmpty(stringValue(workspace["ownerAccountId"]), stringValue(workspace["accountId"]))
	ownerID := stringValue(workspace["ownerUserId"])
	account := findRecord(facts.accounts, accountID)
	owner := findRecord(facts.users, ownerID)
	result := map[string]any{
		"ownerAccount": operatorOwnerAccountEnvelope(account),
		"ownerUser":    operatorOwnerUserEnvelope(owner, accountID),
		"receipt":      sourceEnvelope("ledger", "unavailable", nil, ""),
	}
	if projected, ok := workspaceSourceProjection(workspace); ok {
		result["workspace"] = sourceEnvelope("control-plane", "available", projected, authoritativeSourceTimestamp(workspace["updatedAt"]))
	} else {
		result["workspace"] = sourceEnvelope("control-plane", "unavailable", nil, "")
	}
	keyID, hasKey := positiveIntegerField(workspace, "workspaceApiKeyId")
	keyUsage, hasUsage := facts.keyUsage[keyID]
	if hasKey && facts.keyUsageAvailable && hasUsage && keyUsage.APIKeyID == keyID {
		result["workspaceKeyUsage"] = sourceEnvelope("sub2api", "available", map[string]any{
			"keyId": strconv.FormatInt(keyID, 10), "todayActualCostUsdMicros": keyUsage.TodayActualCostUSDMicros, "totalActualCostUsdMicros": keyUsage.TotalActualCostUSDMicros,
		}, "")
	} else {
		result["workspaceKeyUsage"] = sourceEnvelope("sub2api", "unavailable", nil, "")
	}
	type resourceRow struct {
		kind string
		row  map[string]any
	}
	rows := make([]resourceRow, 0)
	for _, row := range facts.computes {
		if stringValue(row["workspaceId"]) == workspaceID {
			rows = append(rows, resourceRow{kind: "compute", row: row})
		}
	}
	for _, row := range facts.storages {
		if stringValue(row["workspaceId"]) == workspaceID {
			rows = append(rows, resourceRow{kind: "storage", row: row})
		}
	}
	for _, row := range facts.attachments {
		if stringValue(row["workspaceId"]) == workspaceID {
			rows = append(rows, resourceRow{kind: "attachment", row: row})
		}
	}
	if runtimeID := stringValue(workspace["runtimeId"]); runtimeID != "" {
		rows = append(rows, resourceRow{kind: "runtime", row: map[string]any{"id": runtimeID, "workspaceId": workspaceID}})
	}
	sort.Slice(rows, func(i, j int) bool {
		left, right := rows[i].kind+":"+stringValue(rows[i].row["id"]), rows[j].kind+":"+stringValue(rows[j].row["id"])
		return left < right
	})
	resources := make([]any, 0, len(rows))
	for _, resource := range rows {
		resources = append(resources, app.operatorResourceDTO(ctx, service, resource.kind, resource.row, account, owner, workspace, facts, liveLedger))
	}
	result["resources"] = resources
	if liveLedger {
		receiptID := stringValue(workspace["purchaseReceiptId"])
		if receipt, err := service.BillingReceipt(ctx, receiptID); err == nil && receipt.ReceiptID == receiptID && receipt.AccountID == accountID && receipt.WorkspaceID == workspaceID {
			if projected, ok := projectCustomerBillingReceipt(receipt); ok {
				result["receipt"] = sourceEnvelope("ledger", "available", projected, authoritativeSourceTimestamp(receipt.CreatedAt))
			}
		}
	}
	return result
}

func operatorOwnerAccountEnvelope(account map[string]any) map[string]any {
	if account == nil || stringValue(account["id"]) == "" {
		return sourceEnvelope("control-plane", "unavailable", nil, "")
	}
	return sourceEnvelope("control-plane", "available", map[string]any{"id": stringValue(account["id"])}, authoritativeSourceTimestamp(account["updatedAt"]))
}

func operatorOwnerUserEnvelope(owner map[string]any, accountID string) map[string]any {
	if owner == nil || stringValue(owner["id"]) == "" || normalizeEmail(stringValue(owner["email"])) == "" || stringValue(owner["accountId"]) != accountID {
		return sourceEnvelope("control-plane", "unavailable", nil, "")
	}
	return sourceEnvelope("control-plane", "available", map[string]any{"id": stringValue(owner["id"]), "email": normalizeEmail(stringValue(owner["email"]))}, authoritativeSourceTimestamp(owner["updatedAt"]))
}

func (app *controlPlaneServer) operatorResourceDTO(ctx context.Context, service *controlplane.Service, kind string, row, account, owner, workspace map[string]any, facts operatorWorkspaceFacts, liveLedger bool) map[string]any {
	accountID := firstNonEmpty(stringValue(workspace["ownerAccountId"]), stringValue(workspace["accountId"]))
	workspaceID := stringValue(workspace["id"])
	workspaceData := map[string]any{"id": workspaceID}
	if name := stringValue(workspace["name"]); name != "" {
		workspaceData["name"] = name
	}
	result := map[string]any{
		"ownerAccount": operatorOwnerAccountEnvelope(account),
		"ownerUser":    operatorOwnerUserEnvelope(owner, accountID),
		"workspace":    operatorFactEnvelope("control-plane", workspaceData, workspaceID != ""),
	}
	resourceID := stringValue(row["id"])
	fact, factAvailable := facts.providerFacts[operatorProviderFactKey(accountID, workspaceID, kind, resourceID)]
	factAvailable = factAvailable && fact.Available
	result["resourceType"] = operatorFactEnvelope("fabric", kind, factAvailable)
	if !factAvailable {
		fact.Facts = clients.ProviderResourceFacts{}
	}
	result["packageOrSpec"] = operatorStringFactEnvelope("fabric", fact.Facts.PackageOrSpec)
	result["providerId"] = operatorStringFactEnvelope("fabric", fact.Facts.ProviderID)
	result["zone"] = operatorStringFactEnvelope("fabric", fact.Facts.Zone)
	result["status"] = operatorStringFactEnvelope("fabric", fact.Facts.Status)
	result["createdAt"] = operatorTimestampFactEnvelope("fabric", fact.Facts.CreatedAt)
	result["expiresAt"] = operatorTimestampFactEnvelope("fabric", fact.Facts.ExpiresAt)
	result["lastReadAt"] = operatorTimestampFactEnvelope("fabric", fact.Facts.LastReadAt)
	result["operationRef"] = operatorStringFactEnvelope("control-plane", stringValue(row["operationId"]))
	result["receiptRef"] = sourceEnvelope("ledger", "unavailable", nil, "")
	if liveLedger {
		receiptID := firstNonEmpty(stringValue(row["lastReceiptId"]), stringValue(row["receiptId"]))
		if receipt, ok := operatorResourceReceipt(ctx, service, receiptID, accountID, workspaceID, kind, resourceID); ok {
			result["receiptRef"] = sourceEnvelope("ledger", "available", receipt.ReceiptID, authoritativeSourceTimestamp(receipt.CreatedAt))
		}
	}
	return result
}

func operatorFactEnvelope(source string, value any, available bool) map[string]any {
	if !available {
		return sourceEnvelope(source, "unavailable", nil, "")
	}
	return sourceEnvelope(source, "available", value, "")
}

func operatorStringFactEnvelope(source, value string) map[string]any {
	return operatorFactEnvelope(source, value, strings.TrimSpace(value) != "")
}

func operatorTimestampFactEnvelope(source, value string) map[string]any {
	value = authoritativeSourceTimestamp(value)
	return operatorFactEnvelope(source, value, value != "")
}

func operatorResourceReceipt(ctx context.Context, service *controlplane.Service, receiptID, accountID, workspaceID, resourceType, resourceID string) (clients.Receipt, bool) {
	if receiptID == "" {
		return clients.Receipt{}, false
	}
	receipt, err := service.BillingReceipt(ctx, receiptID)
	if err != nil || receipt.ReceiptID != receiptID || receipt.AccountID != accountID || receipt.WorkspaceID != workspaceID {
		return clients.Receipt{}, false
	}
	if value := stringValue(receipt.Cost["resourceType"]); value != "" && value != resourceType {
		return clients.Receipt{}, false
	}
	if value := stringValue(receipt.Cost["resourceId"]); value != "" && value != resourceID {
		return clients.Receipt{}, false
	}
	return receipt, true
}

func (app *controlPlaneServer) operatorOverview(ctx context.Context, service *controlplane.Service) (map[string]any, error) {
	result := map[string]any{
		"accounts":       sourceEnvelope("control-plane", "unavailable", nil, ""),
		"wallet":         sourceEnvelope("sub2api", "unavailable", nil, ""),
		"keys":           sourceEnvelope("sub2api", "unavailable", nil, ""),
		"usage":          sourceEnvelope("sub2api", "unavailable", nil, ""),
		"workspaces":     sourceEnvelope("control-plane", "unavailable", nil, ""),
		"resources":      sourceEnvelope("fabric", "unavailable", nil, ""),
		"reconciliation": sourceEnvelope("control-plane", "unavailable", nil, ""),
	}
	if counts, err := app.tables.CountAccountStatuses(ctx); err == nil {
		total := 0
		for _, count := range counts {
			total += count
		}
		active := counts["active"]
		result["accounts"] = sourceEnvelope("control-plane", "available", map[string]any{"total": total, "active": active, "disabled": total - active}, "")
	}
	if total, err := app.tables.CountWorkspaces(ctx); err == nil {
		result["workspaces"] = sourceEnvelope("control-plane", "available", map[string]any{"total": total}, "")
	}
	if reconciliation, err := app.tables.PageRuntimeOperations(ctx, runtimeOperationQuery{Statuses: []string{"manual_review"}, Limit: 1}); err == nil {
		result["reconciliation"] = sourceEnvelope("control-plane", "available", map[string]any{"total": reconciliation.Total}, "")
	}
	health := app.operatorHealth(ctx, service)
	result["health"] = sourceEnvelope("control-plane", "available", health, "")
	if runtime, ok := availableEnvelopeData(health["runtime"]); ok {
		result["resources"] = sourceEnvelope("fabric", "available", map[string]any{"total": runtime["total"]}, "")
	}
	return result, nil
}

func availableEnvelopeData(value any) (map[string]any, bool) {
	envelope, ok := value.(map[string]any)
	if !ok || envelope["available"] != true {
		return nil, false
	}
	data, ok := envelope["data"].(map[string]any)
	return data, ok
}

func (app *controlPlaneServer) operatorReconciliationPage(ctx context.Context, page, pageSize int) (map[string]any, string, error) {
	operations, err := queryRuntimeOperations(ctx, app.tables, runtimeOperationQuery{Statuses: []string{"manual_review"}})
	if err != nil {
		return nil, "", err
	}
	items := make([]any, 0)
	appendReview := func(resourceType, resourceID, accountID, operationID, phase, errorCode, action, receiptID string) {
		if resourceID == "" || accountID == "" || operationID == "" {
			return
		}
		item := map[string]any{
			"id": resourceID, "resourceType": resourceType, "status": "manual_review", "accountId": accountID,
			"billingOperationId": operationID, "phase": phase, "errorCode": errorCode, "allowedActions": []string{action},
			"operationRef": operationID,
		}
		if receiptID != "" {
			item["receiptRef"] = receiptID
		}
		items = append(items, item)
	}
	for _, operation := range operations {
		details := map[string]any{}
		_ = json.Unmarshal([]byte(stringValue(operation["result"])), &details)
		operationID := firstNonEmpty(stringValue(operation["operationId"]), stringValue(operation["id"]))
		switch stringValue(operation["action"]) {
		case workspaceLaunchAction:
			appendReview(
				"workspace", operationID, firstNonEmpty(stringValue(operation["accountId"]), stringValue(details["accountId"])), operationID,
				firstNonEmpty(stringValue(details["phase"]), "manual_review"), firstNonEmpty(stringValue(details["errorCode"]), stringValue(details["lastBillingError"])),
				"recover_workspace_launch", firstNonEmpty(stringValue(operation["receiptId"]), stringValue(details["receiptId"])),
			)
		case "workspace.renewal":
			appendReview(
				"workspace", firstNonEmpty(stringValue(operation["workspaceId"]), stringValue(details["workspaceId"])),
				firstNonEmpty(stringValue(operation["accountId"]), stringValue(details["accountId"])), operationID,
				firstNonEmpty(stringValue(details["phase"]), "manual_review"), firstNonEmpty(stringValue(details["errorCode"]), stringValue(details["lastBillingError"])),
				"resolve_billing_review", firstNonEmpty(stringValue(operation["receiptId"]), stringValue(details["receiptId"])),
			)
		}
	}
	if reconciliation, ok, err := app.tables.BillingReconciliation(ctx); err != nil {
		return nil, "", err
	} else if ok && stringValue(reconciliation["status"]) == "mismatch" {
		items = append(items, map[string]any{
			"id": stringValue(reconciliation["id"]), "resourceType": "workspace", "status": stringValue(reconciliation["status"]),
			"accountId": "", "billingOperationId": stringValue(reconciliation["id"]), "phase": stringValue(reconciliation["status"]),
			"errorCode": firstNonEmpty(stringValue(reconciliation["reason"]), stringValue(reconciliation["status"])), "allowedActions": []string{},
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return stringValue(items[i].(map[string]any)["id"]) < stringValue(items[j].(map[string]any)["id"])
	})
	total := len(items)
	start := (page - 1) * pageSize
	if start >= total {
		items = []any{}
	} else {
		end := start + pageSize
		if end > total {
			end = total
		}
		items = items[start:end]
	}
	status := "available"
	if len(items) == 0 {
		status = "empty"
	}
	return map[string]any{"items": items, "total": total, "page": page, "pageSize": pageSize}, status, nil
}

func (app *controlPlaneServer) operatorHealth(ctx context.Context, service *controlplane.Service) map[string]any {
	result := map[string]any{
		"controlPlane": sourceEnvelope("control-plane", "available", map[string]any{"ready": true}, ""),
		"gateway":      sourceEnvelope("sub2api", "unavailable", nil, ""),
		"fabric":       sourceEnvelope("fabric", "unavailable", nil, ""),
		"runtime":      app.operatorRuntimeHealth(ctx, service),
		"ledger":       sourceEnvelope("ledger", "unavailable", nil, ""),
	}
	if version, err := service.Sub2APIVersion(ctx); err == nil && strings.TrimSpace(version) != "" {
		result["gateway"] = sourceEnvelope("sub2api", "available", map[string]any{"ready": true, "version": version}, "")
	}
	if readiness, err := service.RuntimeReadiness(ctx); err == nil {
		result["fabric"] = sourceEnvelope("fabric", "available", map[string]any{
			"ready": readiness["ready"] == true, "provider": readiness["provider"],
			"cloudImagesReady": readiness["cloudImagesReady"] == true, "workspaceImagesReady": readiness["workspaceImagesReady"] == true,
			"immutableImagesReady": readiness["immutableImagesReady"] == true,
		}, "")
	}
	if workspaces, err := app.tables.PageWorkspaces(ctx, "", tablePageQuery{Limit: 1}); err == nil {
		for _, workspace := range workspaces.Items {
			receiptID := stringValue(workspace["purchaseReceiptId"])
			if receiptID == "" {
				continue
			}
			receipt, err := service.BillingReceipt(ctx, receiptID)
			if err == nil && receipt.ReceiptID == receiptID && receipt.WorkspaceID == stringValue(workspace["id"]) {
				result["ledger"] = sourceEnvelope("ledger", "available", map[string]any{"ready": true, "receiptId": receiptID}, authoritativeSourceTimestamp(receipt.CreatedAt))
			}
			break
		}
	}
	return result
}

func (app *controlPlaneServer) operatorRuntimeHealth(ctx context.Context, service *controlplane.Service) map[string]any {
	summary, err := service.RuntimeHealthSummary(ctx)
	if err != nil {
		return sourceEnvelope("runtime", "unavailable", nil, "")
	}
	return sourceEnvelope("runtime", "available", map[string]any{
		"ready": summary.Ready == summary.Total && summary.Unready == 0,
		"total": summary.Total, "available": summary.Total, "readyCount": summary.Ready, "unready": summary.Unready,
	}, "")
}

func billingReviewRequestShapeValid(input map[string]any) bool {
	if len(input) != 4 {
		return false
	}
	for _, key := range []string{"accountId", "billingOperationId", "decision", "evidenceRef"} {
		value, ok := input[key].(string)
		if !ok || value == "" || value != strings.TrimSpace(value) {
			return false
		}
	}
	return true
}

func workspaceLaunchRecoveryShapeValid(input map[string]any) bool {
	if len(input) != 3 && len(input) != 4 {
		return false
	}
	for _, key := range []string{"accountId", "billingOperationId", "evidenceRef"} {
		value, ok := input[key].(string)
		if !ok || value == "" || value != strings.TrimSpace(value) {
			return false
		}
	}
	if len(input) == 4 {
		if _, ok := input["approval"].(map[string]any); !ok {
			return false
		}
	}
	return true
}

func validBillingReviewEvidenceRef(value string) bool {
	return billingReviewEvidenceRefPattern.MatchString(value)
}

func validBillingReviewOpaqueID(value string) bool {
	if len(value) < 3 || len(value) > 48 || value != compactID(value) {
		return false
	}
	lower := strings.ToLower(value)
	for _, forbidden := range []string{"api-key", "apikey", "bearer", "credential", "password", "secret", "token"} {
		if strings.Contains(lower, forbidden) {
			return false
		}
	}
	return true
}

func writeBillingReviewResolutionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errInvalidBillingReview):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, errBillingReviewNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, errIdempotencyConflict), errors.Is(err, errBillingReviewNotPending), errors.Is(err, errBillingReviewIdentity), errors.Is(err, errBillingReviewChargeFact), errors.Is(err, errBillingReviewProviderFact):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, errBillingReviewReceipt):
		writeError(w, http.StatusBadGateway, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "state_persist_failed")
	}
}
