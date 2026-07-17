package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/mail"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/domain"
)

var errWorkspaceResumeInProgress = errors.New("workspace_resume_in_progress")
var errWorkspaceNotSuspended = errors.New("workspace_not_suspended")
var errBillingOperationInProgress = errors.New("billing_operation_in_progress")
var errSub2APIAccountMappingConflict = errors.New("sub2api_account_mapping_conflict")
var errPrimaryWorkspaceExists = errors.New("primary_workspace_already_exists")
var errWorkspaceActivationConflict = errors.New("workspace_activation_conflict")
var errInvalidAccountID = errors.New("invalid_account_id")
var errInvalidEmail = errors.New("invalid_email")
var errMembershipExists = errors.New("membership_already_exists")

type workspaceResumeOperationResult struct {
	RequestHash    string                      `json:"requestHash"`
	LeaseExpiresAt *time.Time                  `json:"leaseExpiresAt,omitempty"`
	Response       map[string]any              `json:"response,omitempty"`
	Workspace      *domain.WorkspaceProjection `json:"workspace,omitempty"`
	ErrorCode      string                      `json:"errorCode,omitempty"`
}

type workspaceCreateOperationResult struct {
	RequestHash          string                     `json:"requestHash"`
	LeaseExpiresAt       *time.Time                 `json:"leaseExpiresAt,omitempty"`
	Workspace            domain.WorkspaceProjection `json:"workspace"`
	AcceptedBillingState map[string]any             `json:"acceptedBillingState,omitempty"`
}

type workspaceGatewaySecretOperationResult struct {
	RequestHash string `json:"requestHash"`
	SecretRef   string `json:"secretRef"`
	Fingerprint string `json:"fingerprint"`
}

func decodeWorkspaceCreateOperation(operation map[string]any) (workspaceCreateOperationResult, error) {
	var result workspaceCreateOperationResult
	if err := json.Unmarshal([]byte(stringValue(operation["result"])), &result); err != nil || result.RequestHash == "" || result.Workspace.ID == "" {
		return workspaceCreateOperationResult{}, errors.New("invalid_workspace_create_operation")
	}
	return result, nil
}

func encodeWorkspaceCreateOperation(result workspaceCreateOperationResult) string {
	payload, _ := json.Marshal(result)
	return string(payload)
}

func workspaceCreateClaimIdentity(result workspaceCreateOperationResult) string {
	billingState, _ := json.Marshal(result.AcceptedBillingState)
	return stableID(result.RequestHash, string(billingState))
}

func decodeWorkspaceGatewaySecretOperation(operation map[string]any) (workspaceGatewaySecretOperationResult, error) {
	var result workspaceGatewaySecretOperationResult
	if err := json.Unmarshal([]byte(stringValue(operation["result"])), &result); err != nil || result.RequestHash == "" || result.SecretRef == "" || result.Fingerprint == "" {
		return workspaceGatewaySecretOperationResult{}, errors.New("invalid_workspace_gateway_secret_operation")
	}
	return result, nil
}

func encodeWorkspaceGatewaySecretOperation(result workspaceGatewaySecretOperationResult) string {
	payload, _ := json.Marshal(result)
	return string(payload)
}

func decodeWorkspaceResumeOperation(operation map[string]any) (workspaceResumeOperationResult, error) {
	var result workspaceResumeOperationResult
	if err := json.Unmarshal([]byte(stringValue(operation["result"])), &result); err != nil || result.RequestHash == "" {
		return workspaceResumeOperationResult{}, errors.New("invalid_workspace_resume_operation")
	}
	return result, nil
}

func encodeWorkspaceResumeOperation(result workspaceResumeOperationResult) string {
	payload, _ := json.Marshal(result)
	return string(payload)
}

type controlPlaneTableStore interface {
	ListAccounts(ctx context.Context, accountID string) ([]map[string]any, error)
	SaveAccount(ctx context.Context, row map[string]any) error
	CreateInvitedAccount(ctx context.Context, account, user, organization, membership map[string]any) error
	ApplyUserLifecycle(ctx context.Context, user map[string]any) error
	ListUsers(ctx context.Context, includeDeleted bool) ([]map[string]any, error)
	SaveUser(ctx context.Context, row map[string]any) error
	DeleteUser(ctx context.Context, id string) error
	ListSessions(ctx context.Context) (controlPlaneRecordSet, error)
	SaveSession(ctx context.Context, row map[string]any) error
	DeleteSession(ctx context.Context, id string) error
	ListOrganizations(ctx context.Context) ([]map[string]any, error)
	SaveOrganization(ctx context.Context, row map[string]any) error
	ListMemberships(ctx context.Context) ([]map[string]any, error)
	SaveMembership(ctx context.Context, row map[string]any) error

	ListComputes(ctx context.Context, accountID string) ([]map[string]any, error)
	SaveCompute(ctx context.Context, row map[string]any) error
	DeleteCompute(ctx context.Context, id string) error
	ListStorages(ctx context.Context, accountID string) ([]map[string]any, error)
	SaveStorage(ctx context.Context, row map[string]any) error
	SetResourceAutoRenew(ctx context.Context, resourceType, id, accountID string, autoRenew bool) error
	ClaimResourceBillingOperation(ctx context.Context, resourceType string, row map[string]any) (map[string]any, bool, error)
	DeleteStorage(ctx context.Context, id string) error
	ListAttachments(ctx context.Context, accountID string) ([]map[string]any, error)
	SaveAttachment(ctx context.Context, row map[string]any) error
	DeleteAttachment(ctx context.Context, id string) error
	ListWorkspaces(ctx context.Context, accountID string) ([]map[string]any, error)
	SaveWorkspace(ctx context.Context, row map[string]any) error
	ActivateWorkspace(ctx context.Context, row map[string]any) (map[string]any, error)
	ClaimWorkspaceCreate(ctx context.Context, workspace map[string]any, operation map[string]any) error
	ClaimWorkspaceResume(ctx context.Context, workspaceID string, operation map[string]any) (map[string]any, bool, error)
	FailWorkspaceResume(ctx context.Context, workspaceID string, operationID string, errorCode string) error
	CommitWorkspaceResume(ctx context.Context, workspace map[string]any, audit map[string]any, operation map[string]any) error
	DeleteWorkspace(ctx context.Context, id string) error
	ListWorkspaceBackups(ctx context.Context, workspaceID string) ([]map[string]any, error)
	SaveWorkspaceBackup(ctx context.Context, row map[string]any) error

	ListAuditEvents(ctx context.Context, accountID string) ([]map[string]any, error)
	SaveAuditEvent(ctx context.Context, row map[string]any) error
	ListSupportMappings(ctx context.Context, accountID string) ([]map[string]any, error)
	SaveSupportMapping(ctx context.Context, row map[string]any) error
	ListRuntimeOperations(ctx context.Context) ([]map[string]any, error)
	SaveRuntimeOperation(ctx context.Context, row map[string]any) error
	ListProjectTaskSyncHeads(ctx context.Context) ([]map[string]any, error)
	SaveProjectTaskSyncHead(ctx context.Context, row map[string]any) error
	ListWorkspaceSyncEvents(ctx context.Context, workspaceID string, after int64, limit int) ([]map[string]any, error)
	SaveWorkspaceSyncEvent(ctx context.Context, row map[string]any) error
	ListExecutionRequests(ctx context.Context) ([]map[string]any, error)
	SaveExecutionRequest(ctx context.Context, row map[string]any) error
	BillingReconciliation(ctx context.Context) (map[string]any, bool, error)
	SaveBillingReconciliation(ctx context.Context, row map[string]any) error
}

func prepareWorkspaceActivation(row, owner, compute, storage, attachment, existing map[string]any) (map[string]any, error) {
	row = cloneMap(row)
	state := workspaceAcceptedBillingState(row)
	accountID, ownerID, workspaceID := firstNonEmpty(stringValue(row["accountId"]), stringValue(row["ownerAccountId"])), stringValue(row["ownerUserId"]), stringValue(row["id"])
	computeID, storageID, attachmentID := stringValue(row["currentComputeAllocationId"]), stringValue(row["storageId"]), stringValue(row["currentAttachmentId"])
	paidThrough, paidErr := time.Parse(time.RFC3339, stringValue(state["paidThrough"]))
	computePaidThrough, computePaidErr := time.Parse(time.RFC3339, stringValue(compute["paidThrough"]))
	storagePaidThrough, storagePaidErr := time.Parse(time.RFC3339, stringValue(storage["paidThrough"]))
	if state == nil || accountID == "" || ownerID == "" || workspaceID == "" || computeID == "" || storageID == "" || attachmentID == "" ||
		stringValue(compute["id"]) != computeID || stringValue(storage["id"]) != storageID || stringValue(attachment["id"]) != attachmentID ||
		stringValue(compute["accountId"]) != accountID || stringValue(storage["accountId"]) != accountID || stringValue(attachment["accountId"]) != accountID ||
		stringValue(compute["ownerUserId"]) != ownerID || stringValue(storage["ownerUserId"]) != ownerID ||
		stringValue(compute["workspaceId"]) != workspaceID || stringValue(storage["workspaceId"]) != workspaceID || stringValue(attachment["workspaceId"]) != workspaceID ||
		stringValue(compute["billingStatus"]) != "active" || stringValue(storage["billingStatus"]) != "active" ||
		!workspaceActivationStatus(stringValue(compute["status"]), "compute") || !workspaceActivationStatus(stringValue(storage["status"]), "storage") || !workspaceActivationStatus(stringValue(attachment["status"]), "attachment") ||
		firstNonEmpty(stringValue(attachment["computeAllocationId"]), stringValue(attachment["computeId"])) != computeID ||
		firstNonEmpty(stringValue(attachment["storageId"]), stringValue(attachment["volumeId"])) != storageID ||
		paidErr != nil || computePaidErr != nil || storagePaidErr != nil || computePaidThrough.Before(paidThrough) || storagePaidThrough.Before(paidThrough) {
		return nil, errWorkspaceActivationConflict
	}
	if stringValue(owner["id"]) != ownerID || stringValue(owner["accountId"]) != accountID || stringValue(owner["status"]) != "active" || stringValue(owner["role"]) != "owner" {
		row["autoRenew"] = false
	}
	row, err := mergeWorkspaceForSave(existing, row)
	if err != nil || validateWorkspaceBillingState(row) != nil {
		return nil, errWorkspaceActivationConflict
	}
	return row, nil
}

func workspaceActivationStatus(status, kind string) bool {
	switch kind {
	case "compute":
		return status == "running" || status == "ready" || status == "available" || status == "active"
	case "storage":
		return status == "available" || status == "ready" || status == "bound" || status == "attached"
	case "attachment":
		return status == "attached" || status == "ready"
	default:
		return false
	}
}

func validateSub2APIAccountMapping(accounts []map[string]any, row map[string]any) error {
	userID := int64(numberField(row, "sub2apiUserId", 0))
	if userID <= 0 {
		return nil
	}
	accountID := stringValue(row["id"])
	for _, existing := range accounts {
		if stringValue(existing["id"]) != accountID && int64(numberField(existing, "sub2apiUserId", 0)) == userID {
			return errSub2APIAccountMappingConflict
		}
	}
	return nil
}

func stageInvitedAccount(accounts, users, organizations, memberships controlPlaneRecordSet, account, user, organization, membership map[string]any) error {
	accountID := stringValue(account["id"])
	sub2APIUserID, mapped := positiveIntegerField(account, "sub2apiUserId")
	if !validAccountID(accountID) {
		return errInvalidAccountID
	}
	if !mapped {
		return errMonthlyAccountUnmapped
	}
	accountRows, _ := filteredRecords(accounts, "")
	if err := validateSub2APIAccountMapping(accountRows, account); err != nil {
		return err
	}
	accountRow := cloneMap(account)
	if existing := accounts[accountID]; existing != nil {
		existingMapping := int64(numberField(existing, "sub2apiUserId", 0))
		if stringValue(existing["status"]) != "active" || existingMapping > 0 && existingMapping != sub2APIUserID {
			return errSub2APIAccountMappingConflict
		}
		accountRow = cloneMap(existing)
		accountRow["sub2apiUserId"] = sub2APIUserID
	} else if stringValue(accountRow["status"]) != "active" {
		return errInvalidAccountID
	}
	accounts[accountID] = accountRow

	email, err := canonicalEmail(stringValue(user["email"]))
	if err != nil {
		return err
	}
	userID := stringValue(user["id"])
	if userID == "" || stringValue(user["accountId"]) != accountID || stringValue(user["status"]) != "active" {
		return errInvalidEmail
	}
	if !validRole(stringValue(user["role"])) {
		return errInvalidRole
	}
	for _, existing := range users {
		if stringValue(existing["id"]) == userID || normalizeEmail(stringValue(existing["email"])) == email {
			return errUserExists
		}
	}
	userRow := cloneMap(user)
	userRow["email"] = email
	users[userID] = userRow

	organizationID := stringValue(organization["id"])
	if organizationID == "" || stringValue(organization["billingAccountId"]) != accountID || stringValue(organization["status"]) != "active" {
		return errMembershipAccountMismatch
	}
	organizationRow := cloneMap(organization)
	if existing := organizations[organizationID]; existing != nil {
		if stringValue(existing["billingAccountId"]) != accountID || stringValue(existing["status"]) != "active" {
			return errMembershipAccountMismatch
		}
		organizationRow = cloneMap(existing)
	}
	organizations[organizationID] = organizationRow

	membershipID := stringValue(membership["id"])
	if membershipID == "" || stringValue(membership["accountId"]) != accountID || stringValue(membership["organizationId"]) != organizationID {
		return errMembershipAccountMismatch
	}
	if stringValue(membership["userId"]) != userID {
		return errMembershipUserNotFound
	}
	if !validRole(stringValue(membership["role"])) || stringValue(membership["role"]) != stringValue(userRow["role"]) || stringValue(membership["status"]) != "active" {
		return errInvalidRole
	}
	for _, existing := range memberships {
		if stringValue(existing["id"]) == membershipID || stringValue(existing["organizationId"]) == organizationID && stringValue(existing["userId"]) == userID {
			return errMembershipExists
		}
	}
	memberships[membershipID] = cloneMap(membership)
	return nil
}

func normalizeEmail(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func canonicalEmail(value string) (string, error) {
	email := normalizeEmail(value)
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email {
		return "", errInvalidEmail
	}
	return email, nil
}

func validAccountID(value string) bool {
	return len(value) >= 3 && len(value) <= 48 && value == compactID(value)
}

func billingOperationIdentityMatches(existing, requested map[string]any) bool {
	existingPriceVersion, existingChargeUSDMicros, existingPriceOK := monthlyPriceIdentity(existing)
	requestedPriceVersion, requestedChargeUSDMicros, requestedPriceOK := monthlyPriceIdentity(requested)
	if !existingPriceOK || !requestedPriceOK || existingPriceVersion != requestedPriceVersion || existingChargeUSDMicros != requestedChargeUSDMicros {
		return false
	}
	for _, field := range []string{"accountId", "billingOperationId", "packageId", "periodStart", "paidThrough", "zone"} {
		if stringValue(existing[field]) != stringValue(requested[field]) {
			return false
		}
	}
	if stringValue(existing["resourceType"]) == "storage" || stringValue(requested["resourceType"]) == "storage" || numberField(existing, "sizeGb", 0) > 0 || numberField(requested, "sizeGb", 0) > 0 {
		if stringValue(existing["computeAllocationId"]) != stringValue(requested["computeAllocationId"]) {
			return false
		}
	}
	for _, field := range []string{"sizeGb"} {
		if numberField(existing, field, 0) != numberField(requested, field, 0) {
			return false
		}
	}
	return true
}

func billingOperationInProgress(status string) bool {
	switch status {
	case "preparing", "charge_pending", "refund_pending", "renewal_pending", "manual_review":
		return true
	default:
		return false
	}
}

func preserveResourceAutoRenew(current, incoming map[string]any) controlPlaneRecord {
	row := cloneMap(incoming)
	if autoRenew, ok := current["autoRenew"]; ok {
		row["autoRenew"] = autoRenew
	} else {
		delete(row, "autoRenew")
	}
	return row
}
