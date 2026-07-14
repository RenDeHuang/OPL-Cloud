package server

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var errWorkspaceResumeInProgress = errors.New("workspace_resume_in_progress")
var errWorkspaceNotSuspended = errors.New("workspace_not_suspended")
var errBillingOperationInProgress = errors.New("billing_operation_in_progress")

type workspaceResumeOperationResult struct {
	RequestHash    string         `json:"requestHash"`
	LeaseExpiresAt *time.Time     `json:"leaseExpiresAt,omitempty"`
	Response       map[string]any `json:"response,omitempty"`
	ErrorCode      string         `json:"errorCode,omitempty"`
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
	ListAccounts(ctx context.Context) ([]map[string]any, error)
	SaveAccount(ctx context.Context, row map[string]any) error
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
	ClaimResourceBillingOperation(ctx context.Context, resourceType string, row map[string]any) (map[string]any, bool, error)
	DeleteStorage(ctx context.Context, id string) error
	ListAttachments(ctx context.Context, accountID string) ([]map[string]any, error)
	SaveAttachment(ctx context.Context, row map[string]any) error
	DeleteAttachment(ctx context.Context, id string) error
	ListWorkspaces(ctx context.Context, accountID string) ([]map[string]any, error)
	SaveWorkspace(ctx context.Context, row map[string]any) error
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

func billingOperationIdentityMatches(existing, requested map[string]any) bool {
	for _, field := range []string{"accountId", "billingOperationId", "pricingVersion", "packageId", "periodStart", "paidThrough"} {
		if stringValue(existing[field]) != stringValue(requested[field]) {
			return false
		}
	}
	for _, field := range []string{"monthlyPriceCnyCents", "chargeUsdMicros", "sizeGb"} {
		if numberField(existing, field, 0) != numberField(requested, field, 0) {
			return false
		}
	}
	return true
}

func billingOperationInProgress(status string) bool {
	switch status {
	case "preparing", "charge_pending", "renewal_pending", "manual_review":
		return true
	default:
		return false
	}
}
