package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

var (
	errWorkspaceReactivationRequired = errors.New("workspace_reactivation_required")
	errWorkspaceRenewalCASConflict   = errors.New("workspace_renewal_cas_conflict")
	errInvalidWorkspaceRenewalPatch  = errors.New("invalid_workspace_renewal_patch")
	errInvalidWorkspaceRenewalAudit  = errors.New("invalid_workspace_renewal_audit")
)

type workspaceRenewalIntentCAS struct {
	WorkspaceID               string
	AccountID                 string
	OwnerUserID               string
	ExpectedPaidThrough       string
	ExpectedAutoRenew         bool
	ExpectedOperationsVersion string
	WorkspacePatch            workspaceRenewalIntentPatch
	CommandOperation          map[string]any
	AuditEvent                map[string]any
}

type workspaceRenewalIntentPatch struct {
	AutoRenew    bool
	AuthorizedBy string
	AuthorizedAt string
}

type workspaceAutoRenewCommandResult struct {
	RequestHash string         `json:"requestHash"`
	Response    map[string]any `json:"response"`
}

func workspaceAutoRenewCommandID(workspaceID, key string) string {
	return "workspace-renewal-intent-" + stableID(workspaceID, key)[:18]
}

func workspaceAutoRenewAuditID(commandID string) string {
	return "audit-" + stableID("workspace.auto_renew", commandID)[:12]
}

func workspaceRenewalIntentState(autoRenew bool, authorizedBy, authorizedAt string) map[string]any {
	return map[string]any{"autoRenew": autoRenew, "authorizedBy": authorizedBy, "authorizedAt": authorizedAt}
}

func bindWorkspaceAutoRenewAudit(command, event map[string]any) map[string]any {
	event = cloneMap(event)
	event["id"] = workspaceAutoRenewAuditID(stringValue(command["id"]))
	event["createdAt"] = command["createdAt"]
	return event
}

func validateWorkspaceRenewalIntentAudit(update workspaceRenewalIntentCAS, current map[string]any) error {
	event, command := update.AuditEvent, update.CommandOperation
	before := workspaceRenewalIntentState(current["autoRenew"] == true, stringValue(current["authorizedBy"]), stringValue(current["authorizedAt"]))
	after := workspaceRenewalIntentState(update.WorkspacePatch.AutoRenew, update.WorkspacePatch.AuthorizedBy, update.WorkspacePatch.AuthorizedAt)
	if stringValue(event["id"]) != workspaceAutoRenewAuditID(stringValue(command["id"])) ||
		stringValue(event["createdAt"]) == "" || stringValue(event["createdAt"]) != stringValue(command["createdAt"]) ||
		stringValue(event["actorUserId"]) != update.OwnerUserID || !validRole(stringValue(event["actorRole"])) ||
		stringValue(event["actorAccountId"]) != update.AccountID || stringValue(event["targetAccountId"]) != update.AccountID ||
		stringValue(event["action"]) != "workspace.auto_renew" || stringValue(event["resourceKind"]) != "workspace" ||
		stringValue(event["resourceId"]) != update.WorkspaceID || stringValue(event["result"]) != "succeeded" ||
		string(mustJSON(event["before"])) != string(mustJSON(before)) || string(mustJSON(event["after"])) != string(mustJSON(after)) {
		return errInvalidWorkspaceRenewalAudit
	}
	return nil
}

func workspaceRenewalIntentAuditIdentityMatches(current, desired map[string]any) bool {
	identity := func(row map[string]any) map[string]any {
		return map[string]any{
			"id": stringValue(row["id"]), "actorUserId": stringValue(row["actorUserId"]), "actorRole": stringValue(row["actorRole"]),
			"actorAccountId": stringValue(row["actorAccountId"]), "targetAccountId": stringValue(row["targetAccountId"]),
			"action": stringValue(row["action"]), "resourceKind": stringValue(row["resourceKind"]), "resourceId": stringValue(row["resourceId"]),
			"ipAddress": stringValue(row["ipAddress"]), "userAgent": stringValue(row["userAgent"]), "result": stringValue(row["result"]),
			"createdAt": stringValue(row["createdAt"]), "before": row["before"], "after": row["after"],
		}
	}
	return string(mustJSON(identity(current))) == string(mustJSON(identity(desired)))
}

func workspaceAutoRenewRequestHash(workspaceID string, autoRenew bool) string {
	return stableID("workspace-auto-renew-v1", workspaceID, strconv.FormatBool(autoRenew))
}

func workspaceAutoRenewCommandRow(workspace, user map[string]any, key, requestHash string, response map[string]any, now time.Time) map[string]any {
	result, _ := json.Marshal(workspaceAutoRenewCommandResult{RequestHash: requestHash, Response: response})
	id := workspaceAutoRenewCommandID(stringValue(workspace["id"]), key)
	return map[string]any{
		"id": id, "operationId": id, "accountId": stringValue(workspace["accountId"]), "workspaceId": stringValue(workspace["id"]),
		"resourceId": stringValue(workspace["id"]), "resourceKind": "workspace_renewal_intent", "action": "workspace.auto_renew",
		"status": "succeeded", "result": string(result), "createdAt": now.UTC().Format(time.RFC3339Nano),
	}
}

func decodeWorkspaceAutoRenewCommand(row map[string]any) (workspaceAutoRenewCommandResult, error) {
	var result workspaceAutoRenewCommandResult
	if stringValue(row["action"]) != "workspace.auto_renew" || json.Unmarshal([]byte(stringValue(row["result"])), &result) != nil || result.RequestHash == "" || len(result.Response) != 5 {
		return workspaceAutoRenewCommandResult{}, errors.New("invalid_workspace_auto_renew_operation")
	}
	return result, nil
}

func runtimeOperationsVersion(rows []map[string]any, workspaceID string) string {
	values := make([]string, 0)
	for _, row := range rows {
		if stringValue(row["workspaceId"]) != workspaceID {
			continue
		}
		values = append(values, string(mustJSON(map[string]any{
			"id": row["id"], "action": row["action"], "status": row["status"], "result": row["result"],
		})))
	}
	sort.Strings(values)
	return stableID(values...)
}

func currentWorkspaceRenewalOperation(rows []map[string]any, workspaceID, paidThrough string) map[string]any {
	for _, row := range rows {
		if stringValue(row["action"]) != "workspace.renewal" || stringValue(row["workspaceId"]) != workspaceID {
			continue
		}
		var result struct {
			PaidThrough string `json:"paidThrough"`
		}
		if json.Unmarshal([]byte(stringValue(row["result"])), &result) != nil {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, result.PaidThrough)
		if err != nil || parsed.UTC().Format(time.RFC3339Nano) != paidThrough {
			continue
		}
		switch stringValue(row["status"]) {
		case "active", "cancelled", "expired_unpaid", "refunded":
			continue
		default:
			return row
		}
	}
	return nil
}

func planWorkspaceRenewalIntent(workspace, user map[string]any, operations []map[string]any, autoRenew bool, key string, now time.Time) (workspaceRenewalIntentCAS, map[string]any, error) {
	paidThrough, err := time.Parse(time.RFC3339, stringValue(workspace["paidThrough"]))
	if err != nil {
		return workspaceRenewalIntentCAS{}, nil, errInvalidWorkspaceBillingState
	}
	paidThrough = paidThrough.UTC()
	if !now.UTC().Before(paidThrough) || stringValue(workspace["renewalStatus"]) == "expired_unpaid" {
		return workspaceRenewalIntentCAS{}, nil, errWorkspaceReactivationRequired
	}
	nextRenewal, err := time.Parse(time.RFC3339, stringValue(workspace["nextRenewalAt"]))
	if err != nil {
		return workspaceRenewalIntentCAS{}, nil, errInvalidWorkspaceBillingState
	}
	patch := workspaceRenewalIntentPatch{AutoRenew: autoRenew}
	renewalStatus := "scheduled"
	effectiveAfter := nextRenewal.UTC()
	operation := currentWorkspaceRenewalOperation(operations, stringValue(workspace["id"]), paidThrough.Format(time.RFC3339Nano))
	if autoRenew {
		patch.AuthorizedBy, patch.AuthorizedAt = stringValue(user["id"]), now.UTC().Format(time.RFC3339Nano)
		if operation != nil {
			renewalStatus = stringValue(operation["status"])
		}
	} else {
		effectiveAfter, renewalStatus = paidThrough, "cancelled"
		if operation != nil {
			anchor := int(numberField(workspace, "billingAnchorDay", float64(paidThrough.Day())))
			effectiveAfter, renewalStatus = nextBillingMonth(paidThrough, anchor), stringValue(operation["status"])
		}
	}
	response := map[string]any{
		"autoRenew": autoRenew, "effectiveAfter": effectiveAfter.Format(time.RFC3339Nano),
		"nextRenewalAt": nextRenewal.UTC().Format(time.RFC3339Nano), "paidThrough": paidThrough.Format(time.RFC3339Nano), "renewalStatus": renewalStatus,
	}
	requestHash := workspaceAutoRenewRequestHash(stringValue(workspace["id"]), autoRenew)
	return workspaceRenewalIntentCAS{
		WorkspaceID: stringValue(workspace["id"]), AccountID: stringValue(workspace["accountId"]), OwnerUserID: stringValue(user["id"]),
		ExpectedPaidThrough: stringValue(workspace["paidThrough"]), ExpectedAutoRenew: workspace["autoRenew"] == true,
		ExpectedOperationsVersion: runtimeOperationsVersion(operations, stringValue(workspace["id"])), WorkspacePatch: patch,
		CommandOperation: workspaceAutoRenewCommandRow(workspace, user, key, requestHash, response, now),
	}, response, nil
}

const workspaceRenewalLeaseDuration = 5 * time.Minute

type workspaceRenewalOperation struct {
	ID                          string         `json:"-"`
	Status                      string         `json:"-"`
	CreatedAt                   string         `json:"-"`
	PersistedResult             string         `json:"-"`
	RequestHash                 string         `json:"requestHash"`
	Phase                       string         `json:"phase"`
	AccountID                   string         `json:"accountId"`
	OwnerUserID                 string         `json:"ownerUserId"`
	WorkspaceID                 string         `json:"workspaceId"`
	PackageID                   string         `json:"packageId"`
	StorageGB                   int64          `json:"storageGb"`
	ComputeID                   string         `json:"computeAllocationId"`
	StorageID                   string         `json:"storageId"`
	PriceVersion                string         `json:"priceVersion"`
	ComputeUSDMicros            int64          `json:"computeUsdMicros"`
	StorageUSDMicros            int64          `json:"storageUsdMicros"`
	TotalUSDMicros              int64          `json:"totalUsdMicros"`
	PeriodStart                 string         `json:"periodStart"`
	PaidThrough                 string         `json:"paidThrough"`
	RenewedThrough              string         `json:"renewedThrough"`
	RedeemCode                  string         `json:"sub2apiRedeemCode"`
	RefundCode                  string         `json:"sub2apiRefundCode"`
	ComputePreflightConfirmed   bool           `json:"computePreflightConfirmed,omitempty"`
	StoragePreflightConfirmed   bool           `json:"storagePreflightConfirmed,omitempty"`
	ChargeAttempted             bool           `json:"chargeAttempted,omitempty"`
	ChargeConfirmation          map[string]any `json:"chargeConfirmation,omitempty"`
	PreChargeBalanceUSDMicros   int64          `json:"preChargeBalanceUsdMicros,omitempty"`
	PostChargeBalanceUSDMicros  int64          `json:"postChargeBalanceUsdMicros,omitempty"`
	PostChargeBalanceKnown      bool           `json:"postChargeBalanceKnown,omitempty"`
	RefundAttempted             bool           `json:"refundAttempted,omitempty"`
	RefundConfirmation          map[string]any `json:"refundConfirmation,omitempty"`
	RefundReason                string         `json:"refundReason,omitempty"`
	RefundReceiptID             string         `json:"refundReceiptId,omitempty"`
	ComputeRenewal              map[string]any `json:"computeRenewal,omitempty"`
	StorageRenewal              map[string]any `json:"storageRenewal,omitempty"`
	ComputeReadback             map[string]any `json:"computeReadback,omitempty"`
	StorageReadback             map[string]any `json:"storageReadback,omitempty"`
	EntitlementCommitted        bool           `json:"entitlementCommitted,omitempty"`
	ReceiptID                   string         `json:"receiptId,omitempty"`
	ErrorCode                   string         `json:"errorCode,omitempty"`
	PriorStatus                 string         `json:"priorStatus,omitempty"`
	PriorErrorCode              string         `json:"priorErrorCode,omitempty"`
	ExpiryStatus                string         `json:"expiryStatus,omitempty"`
	ExpiryPhase                 string         `json:"expiryPhase,omitempty"`
	ExpiryErrorCode             string         `json:"expiryErrorCode,omitempty"`
	ExpiryReceiptID             string         `json:"expiryReceiptId,omitempty"`
	ExpiryPeriodStart           string         `json:"expiryPeriodStart,omitempty"`
	ExpiryPaidThrough           string         `json:"expiryPaidThrough,omitempty"`
	LeaseToken                  string         `json:"leaseToken,omitempty"`
	LeaseExpiresAt              string         `json:"leaseExpiresAt,omitempty"`
	ReviewResolutionKey         string         `json:"reviewResolutionKey,omitempty"`
	ReviewResolutionFingerprint string         `json:"reviewResolutionFingerprint,omitempty"`
	ReviewResolutionDecision    string         `json:"reviewResolutionDecision,omitempty"`
	ReviewResolutionEvidenceRef string         `json:"reviewResolutionEvidenceRef,omitempty"`
	ReviewResolutionReviewer    string         `json:"reviewResolutionReviewer,omitempty"`
	ReviewResolutionPhase       string         `json:"reviewResolutionPhase,omitempty"`
	ReviewResolutionResolvedAt  string         `json:"reviewResolutionResolvedAt,omitempty"`
	ReviewResolutionResult      map[string]any `json:"reviewResolutionResult,omitempty"`
}

type workspaceRenewalClaimCAS struct {
	WorkspaceID               string
	AccountID                 string
	ExpectedPaidThrough       string
	ExpectedAutoRenew         bool
	ExpectedOperationsVersion string
	ExpectedOperationResult   string
	DesiredOperation          map[string]any
}

type workspaceRenewalPersistCAS struct {
	OperationID                  string
	ExpectedOperationResult      string
	DesiredOperation             map[string]any
	WorkspaceID                  string
	ExpectedWorkspacePaidThrough string
	ExpectedWorkspaceFields      map[string]string
	WorkspacePatch               map[string]any
}

var workspaceRenewalPatchFields = map[string]bool{
	"periodStart": true, "paidThrough": true, "nextRenewalAt": true, "renewalStatus": true,
	"autoRenew": true, "authorizedBy": true, "authorizedAt": true, "state": true, "status": true, "currentComputeAllocationId": true,
}

func mergeWorkspaceRenewalPatch(current, patch map[string]any) (map[string]any, error) {
	if current == nil || len(patch) == 0 {
		return nil, errInvalidWorkspaceRenewalPatch
	}
	merged := cloneMap(current)
	for key, value := range patch {
		if !workspaceRenewalPatchFields[key] {
			return nil, errInvalidWorkspaceRenewalPatch
		}
		merged[key] = value
	}
	if err := validateWorkspaceBillingState(merged); err != nil {
		return nil, err
	}
	return merged, nil
}

type workspaceRenewalWorkspacePatch struct {
	ExpectedPaidThrough string
	ExpectedFields      map[string]string
	Fields              map[string]any
}

func workspaceRenewalExpectedFieldsMatch(current map[string]any, expected map[string]string) bool {
	for field, value := range expected {
		if stringValue(current[field]) != value {
			return false
		}
	}
	return true
}

func workspaceRenewalOperationID(workspaceID string, paidThrough time.Time) string {
	return "workspace-renewal-" + stableID(workspaceID, paidThrough.UTC().Format(time.RFC3339Nano))[:18]
}

func newWorkspaceRenewalOperation(workspace map[string]any, now time.Time) (workspaceRenewalOperation, error) {
	paidThrough, err := time.Parse(time.RFC3339, stringValue(workspace["paidThrough"]))
	if err != nil {
		return workspaceRenewalOperation{}, errInvalidWorkspaceBillingState
	}
	anchor := int(numberField(workspace, "billingAnchorDay", float64(paidThrough.Day())))
	renewedThrough := nextBillingMonth(paidThrough, anchor)
	id := workspaceRenewalOperationID(stringValue(workspace["id"]), paidThrough)
	operation := workspaceRenewalOperation{
		ID: id, Status: "claimed", CreatedAt: now.UTC().Format(time.RFC3339Nano), Phase: "preflight_compute",
		AccountID: stringValue(workspace["accountId"]), OwnerUserID: stringValue(workspace["ownerUserId"]), WorkspaceID: stringValue(workspace["id"]),
		PackageID: stringValue(workspace["packageId"]), StorageGB: int64(numberField(workspace, "storageGb", 0)),
		ComputeID: stringValue(workspace["computeAllocationId"]), StorageID: stringValue(workspace["storageId"]), PriceVersion: stringValue(workspace["priceVersion"]),
		ComputeUSDMicros: int64(numberField(workspace, "computeUsdMicros", 0)), StorageUSDMicros: int64(numberField(workspace, "storageUsdMicros", 0)), TotalUSDMicros: int64(numberField(workspace, "totalUsdMicros", 0)),
		PeriodStart: stringValue(workspace["periodStart"]), PaidThrough: paidThrough.UTC().Format(time.RFC3339Nano), RenewedThrough: renewedThrough.UTC().Format(time.RFC3339Nano),
		RedeemCode: monthlyRedeemCode(monthlyEnvironment(), id), RefundCode: monthlyRefundCode(monthlyEnvironment(), id),
	}
	operation.RequestHash = stableID(
		"workspace-renewal-v1", operation.AccountID, operation.OwnerUserID, operation.WorkspaceID, operation.PackageID,
		operation.ComputeID, operation.StorageID, operation.PriceVersion, strconv.FormatInt(operation.StorageGB, 10),
		strconv.FormatInt(operation.ComputeUSDMicros, 10), strconv.FormatInt(operation.StorageUSDMicros, 10), strconv.FormatInt(operation.TotalUSDMicros, 10), operation.PeriodStart, operation.PaidThrough,
	)
	if operation.AccountID == "" || operation.OwnerUserID == "" || operation.WorkspaceID == "" || operation.PackageID == "" || operation.ComputeID == "" || operation.StorageID == "" ||
		operation.StorageGB <= 0 || operation.ComputeUSDMicros <= 0 || operation.StorageUSDMicros <= 0 || operation.TotalUSDMicros <= 0 {
		return workspaceRenewalOperation{}, errInvalidWorkspaceBillingState
	}
	return operation, nil
}

func encodeWorkspaceRenewalOperation(operation workspaceRenewalOperation) string {
	payload, _ := json.Marshal(operation)
	return string(payload)
}

func decodeWorkspaceRenewalOperation(row map[string]any) (workspaceRenewalOperation, error) {
	var operation workspaceRenewalOperation
	result := stringValue(row["result"])
	if json.Unmarshal([]byte(result), &operation) != nil {
		return workspaceRenewalOperation{}, errors.New("invalid_workspace_renewal_operation")
	}
	operation.ID = firstNonEmpty(stringValue(row["operationId"]), stringValue(row["id"]))
	operation.Status, operation.CreatedAt, operation.PersistedResult = stringValue(row["status"]), stringValue(row["createdAt"]), result
	if operation.ID == "" || operation.Status == "" || operation.RequestHash == "" || operation.AccountID == "" || operation.WorkspaceID == "" || operation.PaidThrough == "" {
		return workspaceRenewalOperation{}, errors.New("invalid_workspace_renewal_operation")
	}
	for field, want := range map[string]string{
		"accountId": operation.AccountID, "workspaceId": operation.WorkspaceID, "resourceId": operation.WorkspaceID,
		"resourceKind": "workspace_renewal", "action": "workspace.renewal",
	} {
		if got := stringValue(row[field]); got != "" && got != want {
			return workspaceRenewalOperation{}, errors.New("invalid_workspace_renewal_operation")
		}
	}
	return operation, nil
}

func workspaceRenewalOperationRow(operation workspaceRenewalOperation) map[string]any {
	return map[string]any{
		"id": operation.ID, "operationId": operation.ID, "accountId": operation.AccountID, "workspaceId": operation.WorkspaceID,
		"resourceId": operation.WorkspaceID, "resourceKind": "workspace_renewal", "action": "workspace.renewal", "status": operation.Status,
		"result": encodeWorkspaceRenewalOperation(operation), "computeAllocationId": operation.ComputeID, "storageId": operation.StorageID, "createdAt": operation.CreatedAt,
	}
}

func workspaceRenewalClaimIdentityMatches(current, desired map[string]any) bool {
	existing, existingErr := decodeWorkspaceRenewalOperation(current)
	next, nextErr := decodeWorkspaceRenewalOperation(desired)
	return existingErr == nil && nextErr == nil && existing.ID == next.ID && existing.AccountID == next.AccountID && existing.WorkspaceID == next.WorkspaceID &&
		existing.PaidThrough == next.PaidThrough && existing.RequestHash == next.RequestHash
}

func terminalWorkspaceRenewal(operation workspaceRenewalOperation) bool {
	financialTerminal := false
	switch operation.Status {
	case "active", "cancelled", "manual_review":
		financialTerminal = true
	case "refunded":
		financialTerminal = operation.Phase == "complete"
	case "expired_unpaid":
		financialTerminal = operation.ExpiryStatus == "expired_unpaid" || operation.Phase == "complete"
	}
	return financialTerminal && (operation.ExpiryStatus == "" || operation.ExpiryPhase == "complete")
}

func workspaceRenewalFinancialRecoveryRequired(operation workspaceRenewalOperation) bool {
	return operation.Status == "manual_review" || operation.ChargeAttempted || operation.ChargeConfirmation != nil ||
		operation.RefundAttempted || operation.RefundConfirmation != nil || operation.EntitlementCommitted
}

func (app *controlPlaneServer) processWorkspaceRenewal(ctx context.Context, service *controlplane.Service, workspaceID string, now time.Time) error {
	unlock := app.lockResource("workspace-renewal", workspaceID)
	defer unlock()
	for range 3 {
		workspace, ok := app.getWorkspace(workspaceID)
		if !ok {
			return nil
		}
		paidThrough, err := time.Parse(time.RFC3339, stringValue(workspace["paidThrough"]))
		if err != nil {
			return errInvalidWorkspaceBillingState
		}
		operations, err := app.tables.ListRuntimeOperations(ctx)
		if err != nil {
			return err
		}
		expected, err := newWorkspaceRenewalOperation(workspace, now)
		if err != nil {
			return err
		}
		var operation workspaceRenewalOperation
		var found bool
		for _, row := range operations {
			if stringValue(row["action"]) != "workspace.renewal" || stringValue(row["workspaceId"]) != workspaceID {
				continue
			}
			candidate, err := decodeWorkspaceRenewalOperation(row)
			if err != nil {
				return err
			}
			coversCurrentExpiry := (candidate.ExpiryStatus == "past_due" || candidate.ExpiryStatus == "expired_unpaid") && candidate.ExpiryPaidThrough == stringValue(workspace["paidThrough"])
			if candidate.ID == expected.ID || !terminalWorkspaceRenewal(candidate) || coversCurrentExpiry {
				operation, found = candidate, true
				if !terminalWorkspaceRenewal(candidate) || coversCurrentExpiry {
					break
				}
			}
		}
		expired := !now.UTC().Before(paidThrough.UTC())
		if found && terminalWorkspaceRenewal(operation) && (!expired || operation.ExpiryStatus == "expired_unpaid" && operation.ExpiryPhase == "complete") {
			return nil
		}
		if !found {
			if !expired && (workspace["autoRenew"] != true || now.UTC().Before(paidThrough.UTC().Add(-monthlyRenewalLead))) {
				return nil
			}
			operation = expected
		} else if operation.RequestHash != expected.RequestHash && !operation.EntitlementCommitted {
			return errIdempotencyConflict
		}
		if expired && operation.ExpiryStatus == "" {
			operation.PriorStatus, operation.PriorErrorCode = operation.Status, operation.ErrorCode
			operation.ExpiryStatus, operation.ExpiryPhase = "expired_unpaid", "suspend"
			operation.ExpiryPeriodStart, operation.ExpiryPaidThrough = stringValue(workspace["periodStart"]), stringValue(workspace["paidThrough"])
			if workspaceRenewalFinancialRecoveryRequired(operation) && operation.PaidThrough == operation.ExpiryPaidThrough {
				operation.ExpiryStatus = "past_due"
			} else if !workspaceRenewalFinancialRecoveryRequired(operation) {
				operation.Status = "expired_unpaid"
			}
		}
		if operation.LeaseExpiresAt != "" {
			leaseExpiresAt, err := time.Parse(time.RFC3339, operation.LeaseExpiresAt)
			if err != nil {
				return errors.New("invalid_workspace_renewal_lease")
			}
			if leaseExpiresAt.After(now.UTC()) {
				return nil
			}
		}
		operation.LeaseToken = stableID(operation.ID, now.UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
		operation.LeaseExpiresAt = now.UTC().Add(workspaceRenewalLeaseDuration).Format(time.RFC3339Nano)
		desired := workspaceRenewalOperationRow(operation)
		claim := workspaceRenewalClaimCAS{
			WorkspaceID: workspaceID, AccountID: stringValue(workspace["accountId"]), ExpectedPaidThrough: stringValue(workspace["paidThrough"]),
			ExpectedAutoRenew: workspace["autoRenew"] == true, ExpectedOperationsVersion: runtimeOperationsVersion(operations, workspaceID),
			ExpectedOperationResult: operation.PersistedResult, DesiredOperation: desired,
		}
		if err := app.tables.ClaimWorkspaceRenewal(ctx, claim); errors.Is(err, errWorkspaceRenewalCASConflict) {
			continue
		} else if err != nil {
			return err
		}
		operation.PersistedResult = stringValue(desired["result"])
		financialExpiryPending := expired && operation.ExpiryStatus == "past_due"
		var expiryErr, renewalErr error
		if expired {
			expiryErr = app.progressWorkspaceRenewalExpiry(ctx, service, &operation)
			if current, loadErr := app.loadWorkspaceRenewalOperation(ctx, operation.ID); loadErr == nil {
				operation = current
			}
		}
		if operation.Status != "expired_unpaid" {
			renewalErr = app.runWorkspaceRenewal(ctx, service, operation, now.UTC())
			if current, loadErr := app.loadWorkspaceRenewalOperation(ctx, operation.ID); loadErr == nil {
				operation = current
			}
		}
		if expired {
			if financialExpiryPending && operation.ExpiryStatus != "" {
				expiryErr = errors.Join(expiryErr, app.progressWorkspaceRenewalExpiry(ctx, service, &operation))
				if current, loadErr := app.loadWorkspaceRenewalOperation(ctx, operation.ID); loadErr == nil {
					operation = current
				}
			}
			expiryErr = errors.Join(expiryErr, app.recordWorkspaceRenewalExpiryReceipt(ctx, service, &operation))
		}
		return errors.Join(renewalErr, expiryErr)
	}
	return errWorkspaceRenewalCASConflict
}

func (app *controlPlaneServer) persistWorkspaceRenewal(ctx context.Context, operation *workspaceRenewalOperation, workspace *workspaceRenewalWorkspacePatch) error {
	desired := workspaceRenewalOperationRow(*operation)
	update := workspaceRenewalPersistCAS{OperationID: operation.ID, ExpectedOperationResult: operation.PersistedResult, DesiredOperation: desired}
	if workspace != nil {
		update.WorkspaceID, update.ExpectedWorkspacePaidThrough = operation.WorkspaceID, workspace.ExpectedPaidThrough
		update.ExpectedWorkspaceFields, update.WorkspacePatch = workspace.ExpectedFields, workspace.Fields
	}
	if err := app.tables.PersistWorkspaceRenewal(ctx, update); err != nil {
		return err
	}
	operation.PersistedResult = stringValue(desired["result"])
	return nil
}

func releaseWorkspaceRenewalLease(operation *workspaceRenewalOperation) {
	operation.LeaseToken, operation.LeaseExpiresAt = "", ""
}

func (app *controlPlaneServer) retryWorkspaceRenewal(ctx context.Context, operation *workspaceRenewalOperation, code string, cause error) error {
	if cause == nil {
		cause = errors.New(code)
	}
	operation.ErrorCode = code
	releaseWorkspaceRenewalLease(operation)
	return errors.Join(cause, app.persistWorkspaceRenewal(ctx, operation, nil))
}

func (app *controlPlaneServer) manualReviewWorkspaceRenewal(ctx context.Context, operation *workspaceRenewalOperation, code string) error {
	operation.Status, operation.ErrorCode = "manual_review", code
	releaseWorkspaceRenewalLease(operation)
	return app.persistWorkspaceRenewal(ctx, operation, nil)
}

func (app *controlPlaneServer) loadWorkspaceRenewalOperation(ctx context.Context, operationID string) (workspaceRenewalOperation, error) {
	rows, err := app.tables.ListRuntimeOperations(ctx)
	if err != nil {
		return workspaceRenewalOperation{}, err
	}
	for _, row := range rows {
		if stringValue(row["action"]) == "workspace.renewal" && firstNonEmpty(stringValue(row["operationId"]), stringValue(row["id"])) == operationID {
			return decodeWorkspaceRenewalOperation(row)
		}
	}
	return workspaceRenewalOperation{}, errBillingReviewNotFound
}

func (app *controlPlaneServer) resolveWorkspaceRenewalReview(ctx context.Context, service *controlplane.Service, input billingReviewResolutionInput) (map[string]any, error) {
	if input.ResourceType != "workspace" || input.Decision != billingReviewActivateCharged || input.ResourceID == "" || input.AccountID == "" || input.BillingOperationID == "" || input.IdempotencyKey == "" || input.Reviewer == "" {
		return nil, errInvalidBillingReview
	}
	unlock := app.lockResource("workspace-renewal", input.ResourceID)
	defer unlock()

	operation, err := app.loadWorkspaceRenewalOperation(ctx, input.BillingOperationID)
	if err != nil {
		return nil, err
	}
	fingerprint := stableID(input.ResourceType, input.ResourceID, input.AccountID, input.BillingOperationID, input.Decision, input.EvidenceRef, input.Reviewer)
	if operation.ReviewResolutionKey != "" {
		if operation.ReviewResolutionKey != input.IdempotencyKey || operation.ReviewResolutionFingerprint != fingerprint {
			return nil, errIdempotencyConflict
		}
		if operation.ReviewResolutionPhase == "completed" && len(operation.ReviewResolutionResult) > 0 {
			return cloneMap(operation.ReviewResolutionResult), nil
		}
	}
	if operation.WorkspaceID != input.ResourceID || operation.AccountID != input.AccountID || operation.ID != input.BillingOperationID {
		return nil, errBillingReviewIdentity
	}
	if operation.ReviewResolutionKey == "" && operation.Status != "manual_review" {
		return nil, errBillingReviewNotPending
	}
	if operation.Status != "manual_review" && operation.Status != "verifying" && operation.Status != "active" {
		return nil, errBillingReviewNotPending
	}
	userID, err := app.sub2APIUserID(ctx, input.AccountID)
	if err != nil {
		return nil, err
	}
	if !operation.PostChargeBalanceKnown || operation.PostChargeBalanceUSDMicros < 0 || !monthlyChargeConfirmationMatches(operation.ChargeConfirmation, operation.RedeemCode, userID, operation.TotalUSDMicros) {
		return nil, errBillingReviewChargeFact
	}
	if operation.ReviewResolutionKey == "" {
		operation.ReviewResolutionKey = input.IdempotencyKey
		operation.ReviewResolutionFingerprint = fingerprint
		operation.ReviewResolutionDecision = input.Decision
		operation.ReviewResolutionEvidenceRef = input.EvidenceRef
		operation.ReviewResolutionReviewer = input.Reviewer
		operation.ReviewResolutionPhase = "verify_compute"
		operation.ReviewResolutionResolvedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err := app.persistWorkspaceRenewal(ctx, &operation, nil); err != nil {
			return nil, err
		}
	}

	if operation.Status == "manual_review" && operation.ReviewResolutionPhase == "verify_compute" {
		result, readErr := service.SyncMonthlyCompute(ctx, operation.ComputeID)
		facts := structToMap(result)
		operation.ComputeReadback = facts
		row, absent, valid := app.workspaceRenewalProviderReadback("compute", &operation, facts, readErr)
		if absent || !valid {
			if err := app.persistWorkspaceRenewal(ctx, &operation, nil); err != nil {
				return nil, err
			}
			return nil, errBillingReviewProviderFact
		}
		if err := app.tables.SaveCompute(ctx, row); err != nil {
			return nil, err
		}
		operation.ReviewResolutionPhase = "verify_storage"
		if err := app.persistWorkspaceRenewal(ctx, &operation, nil); err != nil {
			return nil, err
		}
	}
	if operation.Status == "manual_review" && operation.ReviewResolutionPhase == "verify_storage" {
		result, readErr := service.SyncMonthlyStorage(ctx, operation.StorageID)
		facts := structToMap(result)
		operation.StorageReadback = facts
		row, absent, valid := app.workspaceRenewalProviderReadback("storage", &operation, facts, readErr)
		if absent || !valid {
			if err := app.persistWorkspaceRenewal(ctx, &operation, nil); err != nil {
				return nil, err
			}
			return nil, errBillingReviewProviderFact
		}
		if err := app.tables.SaveStorage(ctx, row); err != nil {
			return nil, err
		}
		operation.Status, operation.Phase, operation.ErrorCode = "verifying", "entitlement", ""
		operation.ReviewResolutionPhase = "entitlement"
		if err := app.persistWorkspaceRenewal(ctx, &operation, nil); err != nil {
			return nil, err
		}
	}
	if operation.Status == "verifying" {
		if err := app.runWorkspaceRenewal(ctx, service, operation, time.Now().UTC()); err != nil {
			current, loadErr := app.loadWorkspaceRenewalOperation(ctx, operation.ID)
			if loadErr == nil && current.Phase == "receipt" {
				current.ReviewResolutionPhase = "receipt"
				if persistErr := app.persistWorkspaceRenewal(ctx, &current, nil); persistErr != nil {
					return nil, persistErr
				}
				return nil, errBillingReviewReceipt
			}
			return nil, err
		}
		operation, err = app.loadWorkspaceRenewalOperation(ctx, operation.ID)
		if err != nil {
			return nil, err
		}
	}
	if operation.Status != "active" || operation.ReceiptID == "" {
		return nil, errBillingReviewNotPending
	}
	operation.ReviewResolutionPhase = "completed"
	operation.ReviewResolutionResult = map[string]any{
		"resourceType": "workspace", "resourceId": operation.WorkspaceID, "accountId": operation.AccountID,
		"billingOperationId": operation.ID, "decision": operation.ReviewResolutionDecision, "evidenceRef": operation.ReviewResolutionEvidenceRef,
		"reviewer": operation.ReviewResolutionReviewer, "billingStatus": operation.Status, "status": operation.Status,
		"providerStatus": "renewed", "receiptId": operation.ReceiptID, "resolvedAt": operation.ReviewResolutionResolvedAt,
	}
	if err := app.persistWorkspaceRenewal(ctx, &operation, nil); err != nil {
		return nil, err
	}
	return cloneMap(operation.ReviewResolutionResult), nil
}

func (app *controlPlaneServer) workspaceRenewalResources(operation workspaceRenewalOperation) (map[string]any, map[string]any, error) {
	compute, computeOK := app.getCompute(operation.ComputeID)
	storage, storageOK := app.getStorage(operation.StorageID)
	if !computeOK || !storageOK || stringValue(compute["accountId"]) != operation.AccountID || stringValue(storage["accountId"]) != operation.AccountID ||
		stringValue(compute["workspaceId"]) != operation.WorkspaceID || stringValue(storage["workspaceId"]) != operation.WorkspaceID ||
		stringValue(compute["providerResourceId"]) == "" || stringValue(storage["providerResourceId"]) == "" ||
		stringValue(compute["packageId"]) != operation.PackageID || int64(numberField(storage, "sizeGb", 0)) != operation.StorageGB ||
		stringValue(storage["computeAllocationId"]) != operation.ComputeID {
		return nil, nil, errors.New("workspace_renewal_resource_identity_mismatch")
	}
	computeTruth := cloneMap(compute)
	computeTruth["periodStart"], computeTruth["paidThrough"] = operation.PeriodStart, operation.PaidThrough
	if _, ok := monthlyRenewalProviderTruth("compute", computeTruth); !ok {
		return nil, nil, errors.New("workspace_renewal_compute_provider_truth_invalid")
	}
	storageTruth := cloneMap(storage)
	storageTruth["periodStart"], storageTruth["paidThrough"] = operation.PeriodStart, operation.PaidThrough
	if _, ok := monthlyRenewalProviderTruth("storage", storageTruth); !ok {
		return nil, nil, errors.New("workspace_renewal_storage_provider_truth_invalid")
	}
	return compute, storage, nil
}

func (app *controlPlaneServer) runWorkspaceRenewal(ctx context.Context, service *controlplane.Service, operation workspaceRenewalOperation, now time.Time) error {
	for range 16 {
		switch operation.Status {
		case "expired_unpaid":
			return nil
		case "insufficient":
			operation.Status, operation.Phase, operation.ErrorCode = "debit_pending", "debit", ""
			if err := app.persistWorkspaceRenewal(ctx, &operation, nil); err != nil {
				return err
			}
		case "claimed":
			compute, storage, err := app.workspaceRenewalResources(operation)
			if err != nil {
				return app.manualReviewWorkspaceRenewal(ctx, &operation, err.Error())
			}
			if operation.ChargeAttempted || operation.ChargeConfirmation != nil {
				operation.Status, operation.Phase = "debit_pending", "debit"
				if err := app.persistWorkspaceRenewal(ctx, &operation, nil); err != nil {
					return err
				}
				continue
			}
			switch operation.Phase {
			case "preflight_compute":
				input := clients.MonthlyPreflightInput{ResourceType: "compute", PackageID: operation.PackageID, Zone: stringValue(compute["zone"])}
				preflight, err := service.PreflightMonthlyResource(ctx, input)
				if err != nil || !monthlyPreflightConfirmed(input, preflight) {
					return app.retryWorkspaceRenewal(ctx, &operation, "fabric_compute_preflight_failed", err)
				}
				operation.ComputePreflightConfirmed, operation.Phase = true, "preflight_storage"
				if err := app.persistWorkspaceRenewal(ctx, &operation, nil); err != nil {
					return err
				}
			case "preflight_storage":
				input := clients.MonthlyPreflightInput{ResourceType: "storage", PackageID: operation.PackageID, SizeGB: int(operation.StorageGB), Zone: stringValue(storage["zone"])}
				preflight, err := service.PreflightMonthlyResource(ctx, input)
				if err != nil || !monthlyPreflightConfirmed(input, preflight) {
					return app.retryWorkspaceRenewal(ctx, &operation, "fabric_storage_preflight_failed", err)
				}
				operation.StoragePreflightConfirmed, operation.Status, operation.Phase = true, "debit_pending", "debit"
				if err := app.persistWorkspaceRenewal(ctx, &operation, nil); err != nil {
					return err
				}
			default:
				return app.manualReviewWorkspaceRenewal(ctx, &operation, "workspace_renewal_phase_invalid")
			}
		case "debit_pending":
			if err := app.debitWorkspaceRenewal(ctx, service, &operation); err != nil {
				return err
			}
		case "refund_pending":
			return app.refundWorkspaceRenewal(ctx, service, &operation, operation.RefundReason)
		case "debited":
			operation.Status, operation.Phase = "provider_renewing", "provider_compute"
			if err := app.persistWorkspaceRenewal(ctx, &operation, nil); err != nil {
				return err
			}
		case "provider_renewing":
			if err := app.renewWorkspaceProvider(ctx, service, &operation); err != nil {
				return err
			}
		case "verifying":
			if operation.Phase == "receipt" {
				return app.recordWorkspaceRenewalReceipt(ctx, service, &operation)
			}
			if err := app.verifyWorkspaceRenewalProviderReadback(ctx, service, &operation); err != nil {
				return app.manualReviewWorkspaceRenewal(ctx, &operation, err.Error())
			}
			if err := app.commitWorkspaceRenewalEntitlement(ctx, &operation); err != nil {
				return err
			}
		case "refunded":
			if operation.Phase == "refund_receipt" {
				return app.recordWorkspaceRefundReceipt(ctx, service, &operation)
			}
			return nil
		case "active", "manual_review", "cancelled":
			return nil
		default:
			return app.manualReviewWorkspaceRenewal(ctx, &operation, "workspace_renewal_status_invalid")
		}
	}
	return app.retryWorkspaceRenewal(ctx, &operation, "workspace_renewal_iteration_limit", errors.New("workspace renewal iteration limit"))
}

func (app *controlPlaneServer) debitWorkspaceRenewal(ctx context.Context, service *controlplane.Service, operation *workspaceRenewalOperation) error {
	unlockWallet := app.lockResource("sub2api-wallet", operation.AccountID)
	defer unlockWallet()
	userID, err := app.sub2APIUserID(ctx, operation.AccountID)
	if err != nil {
		return app.retryWorkspaceRenewal(ctx, operation, errMonthlyAccountUnmapped.Error(), err)
	}
	if operation.ChargeConfirmation == nil {
		if _, err := service.Sub2APIWorkspaceKey(ctx, userID); err != nil {
			return app.retryWorkspaceRenewal(ctx, operation, "gateway_key_unavailable", err)
		}
		var charge clients.Sub2APICharge
		if operation.ChargeAttempted || operation.ErrorCode == "sub2api_charge_unconfirmed" {
			history, historyErr := service.Sub2APIBalanceHistory(ctx, userID)
			row := map[string]any{"sub2apiRedeemCode": operation.RedeemCode, "chargeUsdMicros": operation.TotalUSDMicros}
			switch code := sub2APIReconciliationCode(row, userID, history); {
			case historyErr != nil || code == "sub2api_charge_missing":
				charge, err = service.ChargeSub2API(ctx, clients.Sub2APIChargeInput{
					UserID: userID, Code: operation.RedeemCode, ChargeUSDMicros: operation.TotalUSDMicros, Notes: "OPL Workspace monthly " + operation.WorkspaceID,
				})
			case code != "":
				return app.manualReviewWorkspaceRenewal(ctx, operation, code)
			default:
				charge = clients.Sub2APICharge{Code: operation.RedeemCode, UserID: userID, ChargeUSDMicros: operation.TotalUSDMicros, Status: "used"}
			}
		} else {
			balance, balanceErr := service.Sub2APIBalance(ctx, userID)
			if balanceErr != nil {
				return app.retryWorkspaceRenewal(ctx, operation, "sub2api_balance_unavailable", balanceErr)
			}
			if balance.USDMicros <= operation.TotalUSDMicros {
				operation.Status, operation.ErrorCode = "insufficient", errMonthlyInsufficientBalance.Error()
				releaseWorkspaceRenewalLease(operation)
				if err := app.persistWorkspaceRenewal(ctx, operation, nil); err != nil {
					return err
				}
				return errMonthlyInsufficientBalance
			}
			operation.PreChargeBalanceUSDMicros = balance.USDMicros
			operation.ChargeAttempted = true
			if err := app.persistWorkspaceRenewal(ctx, operation, nil); err != nil {
				return err
			}
			charge, err = service.ChargeSub2API(ctx, clients.Sub2APIChargeInput{
				UserID: userID, Code: operation.RedeemCode, ChargeUSDMicros: operation.TotalUSDMicros, Notes: "OPL Workspace monthly " + operation.WorkspaceID,
			})
		}
		if err != nil {
			if errors.Is(err, clients.ErrSub2APIChargeUnknown) {
				return app.retryWorkspaceRenewal(ctx, operation, "sub2api_charge_unconfirmed", err)
			}
			return app.manualReviewWorkspaceRenewal(ctx, operation, "sub2api_charge_unconfirmed")
		}
		confirmation := map[string]any{"code": charge.Code, "userId": charge.UserID, "chargeUsdMicros": charge.ChargeUSDMicros, "status": charge.Status}
		if !monthlyChargeConfirmationMatches(confirmation, operation.RedeemCode, userID, operation.TotalUSDMicros) {
			return app.manualReviewWorkspaceRenewal(ctx, operation, "sub2api_charge_confirmation_invalid")
		}
		operation.ChargeConfirmation, operation.ErrorCode = confirmation, ""
		if err := app.persistWorkspaceRenewal(ctx, operation, nil); err != nil {
			return err
		}
	}
	postCharge, err := service.Sub2APIBalance(ctx, userID)
	if err != nil {
		return app.retryWorkspaceRenewal(ctx, operation, "post_charge_balance_unavailable", err)
	}
	operation.PostChargeBalanceKnown, operation.PostChargeBalanceUSDMicros = true, postCharge.USDMicros
	if operation.PreChargeBalanceUSDMicros <= operation.TotalUSDMicros || postCharge.USDMicros < 0 || postCharge.USDMicros != operation.PreChargeBalanceUSDMicros-operation.TotalUSDMicros {
		return app.manualReviewWorkspaceRenewal(ctx, operation, "post_charge_balance_invalid")
	}
	operation.Status, operation.Phase, operation.ErrorCode = "debited", "provider_compute", ""
	return app.persistWorkspaceRenewal(ctx, operation, nil)
}

func (app *controlPlaneServer) renewWorkspaceProvider(ctx context.Context, service *controlplane.Service, operation *workspaceRenewalOperation) error {
	switch operation.Phase {
	case "provider_compute":
		result, err := service.RenewMonthlyCompute(ctx, operation.ComputeID, operation.ID+":compute")
		operation.ComputeRenewal, operation.Phase = structToMap(result), "verify_compute"
		if err != nil {
			operation.ErrorCode = "fabric_compute_renewal_unconfirmed"
		}
		return app.persistWorkspaceRenewal(ctx, operation, nil)
	case "verify_compute":
		result, err := service.SyncMonthlyCompute(ctx, operation.ComputeID)
		facts := structToMap(result)
		operation.ComputeReadback = facts
		row, absent, valid := app.workspaceRenewalProviderReadback("compute", operation, facts, err)
		if absent {
			return app.refundWorkspaceRenewal(ctx, service, operation, "fabric_compute_confirmed_absent")
		}
		if !valid {
			return app.manualReviewWorkspaceRenewal(ctx, operation, "fabric_compute_renewal_unconfirmed")
		}
		if err := app.tables.SaveCompute(ctx, row); err != nil {
			return err
		}
		operation.Phase, operation.ErrorCode = "provider_storage", ""
		return app.persistWorkspaceRenewal(ctx, operation, nil)
	case "provider_storage":
		result, err := service.RenewMonthlyStorage(ctx, operation.StorageID, operation.ID+":storage")
		operation.StorageRenewal, operation.Phase = structToMap(result), "verify_storage"
		if err != nil {
			operation.ErrorCode = "fabric_storage_renewal_unconfirmed"
		}
		return app.persistWorkspaceRenewal(ctx, operation, nil)
	case "verify_storage":
		result, err := service.SyncMonthlyStorage(ctx, operation.StorageID)
		facts := structToMap(result)
		operation.StorageReadback = facts
		row, absent, valid := app.workspaceRenewalProviderReadback("storage", operation, facts, err)
		if absent {
			return app.manualReviewWorkspaceRenewal(ctx, operation, "fabric_storage_confirmed_absent_after_compute_renewed")
		}
		if !valid {
			return app.manualReviewWorkspaceRenewal(ctx, operation, "fabric_storage_renewal_unconfirmed")
		}
		if err := app.tables.SaveStorage(ctx, row); err != nil {
			return err
		}
		operation.Status, operation.Phase, operation.ErrorCode = "verifying", "entitlement", ""
		return app.persistWorkspaceRenewal(ctx, operation, nil)
	default:
		return app.manualReviewWorkspaceRenewal(ctx, operation, "workspace_renewal_provider_phase_invalid")
	}
}

func (app *controlPlaneServer) workspaceRenewalProviderReadback(resourceType string, operation *workspaceRenewalOperation, facts map[string]any, readErr error) (map[string]any, bool, bool) {
	var existing map[string]any
	var ok bool
	if resourceType == "storage" {
		existing, ok = app.getStorage(operation.StorageID)
	} else {
		existing, ok = app.getCompute(operation.ComputeID)
	}
	if !ok {
		return nil, false, false
	}
	candidate := mergeMaps(existing, facts)
	if monthlyResourceConfirmedAbsent(resourceType, candidate) {
		return candidate, true, true
	}
	oldDeadline, oldErr := time.Parse(time.RFC3339, operation.PaidThrough)
	renewedThrough, renewedErr := time.Parse(time.RFC3339, operation.RenewedThrough)
	if readErr != nil || oldErr != nil || renewedErr != nil || !monthlyRenewalReadbackConfirmed(resourceType, existing, candidate, facts, oldDeadline, renewedThrough) {
		return candidate, false, false
	}
	return candidate, false, true
}

func (app *controlPlaneServer) verifyWorkspaceRenewalProviderReadback(ctx context.Context, service *controlplane.Service, operation *workspaceRenewalOperation) error {
	compute, computeErr := service.SyncMonthlyCompute(ctx, operation.ComputeID)
	operation.ComputeReadback = structToMap(compute)
	computeRow, computeAbsent, computeValid := app.workspaceRenewalProviderReadback("compute", operation, operation.ComputeReadback, computeErr)
	if computeAbsent || !computeValid {
		return errors.New("workspace_renewal_provider_truth_invalid")
	}
	if err := app.tables.SaveCompute(ctx, computeRow); err != nil {
		return errors.New("workspace_renewal_provider_truth_invalid")
	}

	storage, storageErr := service.SyncMonthlyStorage(ctx, operation.StorageID)
	operation.StorageReadback = structToMap(storage)
	storageRow, storageAbsent, storageValid := app.workspaceRenewalProviderReadback("storage", operation, operation.StorageReadback, storageErr)
	if storageAbsent || !storageValid {
		return errors.New("workspace_renewal_provider_truth_invalid")
	}
	if err := app.tables.SaveStorage(ctx, storageRow); err != nil {
		return errors.New("workspace_renewal_provider_truth_invalid")
	}
	return nil
}

func (app *controlPlaneServer) refundWorkspaceRenewal(ctx context.Context, service *controlplane.Service, operation *workspaceRenewalOperation, reason string) error {
	unlockWallet := app.lockResource("sub2api-wallet", operation.AccountID)
	defer unlockWallet()
	userID, err := app.sub2APIUserID(ctx, operation.AccountID)
	if err != nil {
		return app.manualReviewWorkspaceRenewal(ctx, operation, "workspace_renewal_refund_account_unmapped")
	}
	recoverAttempt := operation.RefundAttempted
	if !operation.RefundAttempted {
		operation.Status, operation.Phase, operation.RefundAttempted, operation.RefundReason, operation.ErrorCode = "refund_pending", "refund", true, reason, ""
		if err := app.persistWorkspaceRenewal(ctx, operation, nil); err != nil {
			return err
		}
	}
	var refund clients.Sub2APIRefund
	if recoverAttempt {
		history, historyErr := service.Sub2APIBalanceHistory(ctx, userID)
		matches := make([]clients.Sub2APIBalanceHistoryEntry, 0, 1)
		for _, entry := range history {
			if entry.Code == operation.RefundCode {
				matches = append(matches, entry)
			}
		}
		if historyErr != nil || len(matches) == 0 {
			refund, err = service.RefundSub2API(ctx, clients.Sub2APIRefundInput{
				UserID: userID, Code: operation.RefundCode, RefundUSDMicros: operation.TotalUSDMicros, Notes: "OPL Workspace renewal refund " + operation.WorkspaceID,
			})
		} else {
			entry := matches[0]
			if len(matches) != 1 || entry.Type != "balance" || entry.Status != "used" || entry.UsedBy == nil || *entry.UsedBy != userID || entry.ValueUSDMicros != operation.TotalUSDMicros {
				return app.manualReviewWorkspaceRenewal(ctx, operation, "sub2api_refund_mismatch")
			}
			refund = clients.Sub2APIRefund{Code: operation.RefundCode, UserID: userID, RefundUSDMicros: operation.TotalUSDMicros, Status: "used"}
		}
	} else {
		refund, err = service.RefundSub2API(ctx, clients.Sub2APIRefundInput{
			UserID: userID, Code: operation.RefundCode, RefundUSDMicros: operation.TotalUSDMicros, Notes: "OPL Workspace renewal refund " + operation.WorkspaceID,
		})
	}
	if err != nil || refund.Code != operation.RefundCode || refund.UserID != userID || refund.RefundUSDMicros != operation.TotalUSDMicros || refund.Status != "used" {
		return app.retryWorkspaceRenewal(ctx, operation, "sub2api_refund_unconfirmed", errors.Join(err, clients.ErrSub2APIChargeUnknown))
	}
	operation.RefundConfirmation = map[string]any{"code": refund.Code, "userId": refund.UserID, "refundUsdMicros": refund.RefundUSDMicros, "status": refund.Status}
	operation.Status, operation.Phase, operation.ErrorCode = "refunded", "refund_receipt", ""
	if err := app.persistWorkspaceRenewal(ctx, operation, nil); err != nil {
		return err
	}
	return app.recordWorkspaceRefundReceipt(ctx, service, operation)
}

func (app *controlPlaneServer) recordWorkspaceRefundReceipt(ctx context.Context, service *controlplane.Service, operation *workspaceRenewalOperation) error {
	userID, err := app.sub2APIUserID(ctx, operation.AccountID)
	if err != nil {
		return app.retryWorkspaceRenewal(ctx, operation, errMonthlyAccountUnmapped.Error(), err)
	}
	cost := workspaceRenewalReceiptCost(*operation, false, 0)
	cost["periodStart"], cost["paidThrough"] = operation.PaidThrough, operation.RenewedThrough
	cost["sub2apiUserId"], cost["sub2apiRedeemCode"] = userID, operation.RedeemCode
	cost["sub2apiRefundCode"], cost["refundUsdMicros"] = operation.RefundCode, operation.TotalUSDMicros
	receipt, err := service.RecordMonthlyReceipt(ctx, clients.ReceiptInput{
		Type: "billing.workspace_refunded.v1", Status: "completed", Surface: "control_plane", AccountID: operation.AccountID,
		WorkspaceID: operation.WorkspaceID, RequestID: operation.ID,
		Execution: map[string]any{
			"resourceType": "workspace", "resourceId": operation.WorkspaceID, "reason": operation.RefundReason,
			"computeAllocationId": operation.ComputeID, "storageId": operation.StorageID, "refundConfirmation": operation.RefundConfirmation,
		},
		Cost:  cost,
		Owner: map[string]any{"accountId": operation.AccountID, "workspaceId": operation.WorkspaceID, "ownerUserId": operation.OwnerUserID},
	}, operation.ID+":refund-receipt")
	if err != nil {
		return app.retryWorkspaceRenewal(ctx, operation, "ledger_refund_receipt_pending", err)
	}
	if receipt.ReceiptID == "" {
		return app.retryWorkspaceRenewal(ctx, operation, "ledger_refund_receipt_invalid", errors.New("Ledger refund receipt ID missing"))
	}
	operation.RefundReceiptID, operation.Phase, operation.ErrorCode = receipt.ReceiptID, "complete", ""
	releaseWorkspaceRenewalLease(operation)
	return app.persistWorkspaceRenewal(ctx, operation, nil)
}

func (app *controlPlaneServer) commitWorkspaceRenewalEntitlement(ctx context.Context, operation *workspaceRenewalOperation) error {
	workspace, ok := app.getWorkspace(operation.WorkspaceID)
	if !ok || stringValue(workspace["accountId"]) != operation.AccountID || stringValue(workspace["computeAllocationId"]) != operation.ComputeID || stringValue(workspace["storageId"]) != operation.StorageID {
		return app.manualReviewWorkspaceRenewal(ctx, operation, "workspace_renewal_identity_mismatch")
	}
	if stringValue(workspace["paidThrough"]) != operation.PaidThrough && stringValue(workspace["paidThrough"]) != operation.RenewedThrough {
		return app.manualReviewWorkspaceRenewal(ctx, operation, "workspace_renewal_period_mismatch")
	}
	renewedThrough, err := time.Parse(time.RFC3339, operation.RenewedThrough)
	if err != nil {
		return app.manualReviewWorkspaceRenewal(ctx, operation, "workspace_renewal_period_invalid")
	}
	fields := map[string]any{
		"periodStart": operation.PaidThrough, "paidThrough": operation.RenewedThrough,
		"nextRenewalAt": renewedThrough.Add(-monthlyRenewalLead).Format(time.RFC3339Nano), "renewalStatus": "active",
	}
	var expectedFields map[string]string
	if operation.ExpiryStatus == "past_due" && operation.ExpiryPaidThrough == operation.PaidThrough {
		attachmentID := stringValue(workspace["currentAttachmentId"])
		if stringValue(workspace["state"]) != "suspended" || stringValue(workspace["status"]) != "suspended" ||
			stringValue(workspace["currentComputeAllocationId"]) != operation.ComputeID || attachmentID == "" {
			return app.manualReviewWorkspaceRenewal(ctx, operation, "workspace_renewal_recovery_state_mismatch")
		}
		expectedFields = map[string]string{
			"state": "suspended", "status": "suspended",
			"computeAllocationId": operation.ComputeID, "currentComputeAllocationId": operation.ComputeID,
			"storageId": operation.StorageID, "currentAttachmentId": attachmentID,
		}
		operation.PriorStatus, operation.PriorErrorCode = "", ""
		operation.ExpiryStatus, operation.ExpiryPhase, operation.ExpiryErrorCode = "", "", ""
		operation.ExpiryReceiptID, operation.ExpiryPeriodStart, operation.ExpiryPaidThrough = "", "", ""
		fields["state"], fields["status"], fields["currentComputeAllocationId"] = "running", "running", operation.ComputeID
	}
	operation.Status, operation.Phase, operation.EntitlementCommitted = "verifying", "receipt", true
	return app.persistWorkspaceRenewal(ctx, operation, &workspaceRenewalWorkspacePatch{
		ExpectedPaidThrough: operation.PaidThrough,
		ExpectedFields:      expectedFields,
		Fields:              fields,
	})
}

func workspaceRenewalReceiptCost(operation workspaceRenewalOperation, charged bool, userID int64) map[string]any {
	periodStart, paidThrough := operation.PeriodStart, operation.PaidThrough
	if charged {
		periodStart, paidThrough = operation.PaidThrough, operation.RenewedThrough
	}
	cost := map[string]any{
		"priceVersion": operation.PriceVersion, "currency": pricingCurrency, "billingUnit": pricingBillingUnit,
		"totalUsdMicros": operation.TotalUSDMicros, "periodStart": periodStart, "paidThrough": paidThrough,
		"resourceType": "workspace", "resourceId": operation.WorkspaceID,
		"components": map[string]any{
			"compute": map[string]any{"resourceType": "compute", "resourceId": operation.ComputeID, "chargeUsdMicros": operation.ComputeUSDMicros},
			"storage": map[string]any{"resourceType": "storage", "resourceId": operation.StorageID, "sizeGb": operation.StorageGB, "chargeUsdMicros": operation.StorageUSDMicros},
		},
	}
	if charged {
		cost["sub2apiUserId"], cost["sub2apiRedeemCode"], cost["postChargeBalanceUsdMicros"] = userID, operation.RedeemCode, operation.PostChargeBalanceUSDMicros
	}
	return cost
}

func (app *controlPlaneServer) recordWorkspaceRenewalReceipt(ctx context.Context, service *controlplane.Service, operation *workspaceRenewalOperation) error {
	userID, err := app.sub2APIUserID(ctx, operation.AccountID)
	if err != nil {
		return app.retryWorkspaceRenewal(ctx, operation, errMonthlyAccountUnmapped.Error(), err)
	}
	input := clients.ReceiptInput{
		Type: "billing.workspace_renewed.v1", Status: "completed", Surface: "control_plane", AccountID: operation.AccountID,
		WorkspaceID: operation.WorkspaceID, RequestID: operation.ID,
		Execution: map[string]any{
			"resourceType": "workspace", "resourceId": operation.WorkspaceID, "computeAllocationId": operation.ComputeID, "storageId": operation.StorageID,
			"computeReadback": operation.ComputeReadback, "storageReadback": operation.StorageReadback,
		},
		Cost:  workspaceRenewalReceiptCost(*operation, true, userID),
		Owner: map[string]any{"accountId": operation.AccountID, "workspaceId": operation.WorkspaceID, "ownerUserId": operation.OwnerUserID},
	}
	if operation.ReviewResolutionKey != "" {
		input.InputRefs = map[string]any{"evidenceRef": operation.ReviewResolutionEvidenceRef}
		input.ReviewerChecks = map[string]any{
			"decision": operation.ReviewResolutionDecision, "reviewer": operation.ReviewResolutionReviewer,
			"evidenceRef": operation.ReviewResolutionEvidenceRef, "resolvedAt": operation.ReviewResolutionResolvedAt,
		}
	}
	receipt, err := service.RecordMonthlyReceipt(ctx, input, operation.ID+":receipt")
	if err != nil {
		return app.retryWorkspaceRenewal(ctx, operation, "ledger_receipt_pending", err)
	}
	if receipt.ReceiptID == "" {
		return app.retryWorkspaceRenewal(ctx, operation, "ledger_receipt_invalid", errors.New("ledger receipt ID missing"))
	}
	operation.ReceiptID, operation.Status, operation.Phase, operation.ErrorCode = receipt.ReceiptID, "active", "complete", ""
	releaseWorkspaceRenewalLease(operation)
	return app.persistWorkspaceRenewal(ctx, operation, nil)
}

func (app *controlPlaneServer) progressWorkspaceRenewalExpiry(ctx context.Context, service *controlplane.Service, operation *workspaceRenewalOperation) error {
	for range 2 {
		switch operation.ExpiryPhase {
		case "suspend":
			_, ok := app.getWorkspace(operation.WorkspaceID)
			if !ok {
				return errors.New("workspace_expiry_workspace_missing")
			}
			operation.ExpiryPhase, operation.ExpiryErrorCode = "compute", ""
			fields := map[string]any{
				"renewalStatus": "expired_unpaid", "state": "suspended", "status": "suspended", "currentComputeAllocationId": "",
			}
			if operation.ExpiryStatus == "past_due" {
				operation.ExpiryPhase = "financial"
				fields = map[string]any{"state": "suspended", "status": "suspended"}
			} else {
				fields["autoRenew"], fields["authorizedBy"], fields["authorizedAt"] = false, "", ""
			}
			if err := app.persistWorkspaceRenewal(ctx, operation, &workspaceRenewalWorkspacePatch{
				ExpectedPaidThrough: operation.ExpiryPaidThrough,
				Fields:              fields,
			}); err != nil {
				return err
			}
		case "financial":
			if operation.Status != "refunded" && operation.Status != "expired_unpaid" {
				return nil
			}
			operation.ExpiryStatus, operation.ExpiryPhase, operation.ExpiryErrorCode = "expired_unpaid", "compute", ""
			if err := app.persistWorkspaceRenewal(ctx, operation, &workspaceRenewalWorkspacePatch{
				ExpectedPaidThrough: operation.ExpiryPaidThrough,
				Fields: map[string]any{
					"autoRenew": false, "authorizedBy": "", "authorizedAt": "", "renewalStatus": "expired_unpaid",
					"currentComputeAllocationId": "",
				},
			}); err != nil {
				return err
			}
		case "compute":
			compute, ok := app.getCompute(operation.ComputeID)
			status := stringValue(compute["status"])
			if ok && !isTerminalResourceStatus(status) && status != "stopped" {
				if status != "destroying" {
					result, err := service.CleanupMonthlyCompute(ctx, operation.ComputeID, operation.ID+":compute-expire")
					if err != nil {
						operation.ExpiryErrorCode = "workspace_expiry_compute_cleanup_pending"
						releaseWorkspaceRenewalLease(operation)
						return errors.Join(err, app.persistWorkspaceRenewal(ctx, operation, nil))
					}
					if result.ID != operation.ComputeID {
						operation.ExpiryErrorCode = "workspace_expiry_compute_identity_mismatch"
						releaseWorkspaceRenewalLease(operation)
						return errors.Join(errors.New(operation.ExpiryErrorCode), app.persistWorkspaceRenewal(ctx, operation, nil))
					}
				}
				result, err := service.SyncMonthlyCompute(ctx, operation.ComputeID)
				if err == nil && result.ID != operation.ComputeID {
					operation.ExpiryErrorCode = "workspace_expiry_compute_identity_mismatch"
					releaseWorkspaceRenewalLease(operation)
					return errors.Join(errors.New(operation.ExpiryErrorCode), app.persistWorkspaceRenewal(ctx, operation, nil))
				}
				if err == nil {
					compute = mergeMaps(compute, structToMap(result))
				} else {
					compute["status"] = "destroying"
				}
				compute["autoRenew"] = false
				if err != nil || !isTerminalResourceStatus(stringValue(compute["status"])) && stringValue(compute["status"]) != "stopped" {
					compute["status"] = firstNonEmpty(stringValue(compute["status"]), "destroying")
					operation.ExpiryErrorCode = "workspace_expiry_compute_cleanup_pending"
					releaseWorkspaceRenewalLease(operation)
					return errors.Join(err, app.tables.SaveCompute(ctx, compute), errors.New(operation.ExpiryErrorCode), app.persistWorkspaceRenewal(ctx, operation, nil))
				}
				compute["billingStatus"] = "stopped"
				if err := app.tables.SaveCompute(ctx, compute); err != nil {
					return err
				}
			}
			operation.ExpiryPhase, operation.ExpiryErrorCode = "receipt", ""
			return app.persistWorkspaceRenewal(ctx, operation, nil)
		case "receipt", "complete":
			return nil
		default:
			return fmt.Errorf("invalid Workspace expiry phase %q", operation.ExpiryPhase)
		}
	}
	return nil
}

func (app *controlPlaneServer) recordWorkspaceRenewalExpiryReceipt(ctx context.Context, service *controlplane.Service, operation *workspaceRenewalOperation) error {
	if operation.ExpiryStatus != "expired_unpaid" || operation.ExpiryPhase == "" {
		return nil
	}
	if operation.ExpiryPhase == "complete" {
		return nil
	}
	if operation.ExpiryPhase != "receipt" {
		return nil
	}
	execution := map[string]any{
		"resourceType": "workspace", "resourceId": operation.WorkspaceID, "reason": "expired_unpaid",
		"computeAllocationId": operation.ComputeID, "storageId": operation.StorageID, "storageAction": "retained",
	}
	if operation.PriorStatus != "" {
		execution["priorStatus"] = operation.PriorStatus
	}
	if operation.PriorErrorCode != "" {
		execution["priorErrorCode"] = operation.PriorErrorCode
	}
	cost := workspaceRenewalReceiptCost(*operation, false, 0)
	cost["periodStart"], cost["paidThrough"] = operation.ExpiryPeriodStart, operation.ExpiryPaidThrough
	receipt, err := service.RecordMonthlyReceipt(ctx, clients.ReceiptInput{
		Type: "billing.workspace_expired.v1", Status: "completed", Surface: "control_plane", AccountID: operation.AccountID,
		WorkspaceID: operation.WorkspaceID, RequestID: operation.ID, Execution: execution, Cost: cost,
		Owner: map[string]any{"accountId": operation.AccountID, "workspaceId": operation.WorkspaceID, "ownerUserId": operation.OwnerUserID},
	}, operation.ID+":expiry-receipt")
	if err != nil {
		operation.ExpiryErrorCode = "ledger_expiry_receipt_pending"
		releaseWorkspaceRenewalLease(operation)
		return errors.Join(err, app.persistWorkspaceRenewal(ctx, operation, nil))
	}
	if receipt.ReceiptID == "" {
		operation.ExpiryErrorCode = "ledger_expiry_receipt_invalid"
		releaseWorkspaceRenewalLease(operation)
		return errors.Join(errors.New("Ledger expiry receipt ID missing"), app.persistWorkspaceRenewal(ctx, operation, nil))
	}
	operation.ExpiryReceiptID, operation.ExpiryPhase, operation.ExpiryErrorCode = receipt.ReceiptID, "complete", ""
	if operation.ReceiptID == "" {
		operation.ReceiptID = receipt.ReceiptID
	}
	releaseWorkspaceRenewalLease(operation)
	return app.persistWorkspaceRenewal(ctx, operation, nil)
}
