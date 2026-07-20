package controlplane

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/domain"
)

type Service struct {
	ledger  clients.LedgerClient
	fabric  clients.FabricClient
	sub2API clients.Sub2APIClient
}

var (
	ErrWorkspaceRuntimeIdentityMismatch = errors.New("workspace_runtime_identity_mismatch")
	ErrWorkspaceRuntimeReadbackInvalid  = errors.New("workspace_runtime_readback_invalid")
)

func (s *Service) fabricTransfers() (clients.FabricTransferClient, error) {
	client, ok := s.fabric.(clients.FabricTransferClient)
	if !ok {
		return nil, errors.New("fabric_transfer_unavailable")
	}
	return client, nil
}

func (s *Service) fabricRecovery() (clients.FabricRecoveryClient, error) {
	client, ok := s.fabric.(clients.FabricRecoveryClient)
	if !ok {
		return nil, errors.New("fabric_recovery_unavailable")
	}
	return client, nil
}

func (s *Service) CreateStorageSnapshot(ctx context.Context, input clients.StorageSnapshotInput, key string) (clients.StorageSnapshot, error) {
	client, err := s.fabricRecovery()
	if err != nil {
		return clients.StorageSnapshot{}, err
	}
	return client.CreateStorageSnapshot(ctx, input, key)
}

func (s *Service) SyncStorageSnapshot(ctx context.Context, id string) (clients.StorageSnapshot, error) {
	client, err := s.fabricRecovery()
	if err != nil {
		return clients.StorageSnapshot{}, err
	}
	return client.SyncStorageSnapshot(ctx, id)
}

func (s *Service) RestoreStorageSnapshot(ctx context.Context, id string, input clients.StorageRestoreInput, key string) (clients.StorageVolume, error) {
	client, err := s.fabricRecovery()
	if err != nil {
		return clients.StorageVolume{}, err
	}
	return client.RestoreStorageSnapshot(ctx, id, input, key)
}

func (s *Service) DestroyStorageSnapshot(ctx context.Context, id, key string) (clients.StorageSnapshot, error) {
	client, err := s.fabricRecovery()
	if err != nil {
		return clients.StorageSnapshot{}, err
	}
	return client.DestroyStorageSnapshot(ctx, id, key)
}

func (s *Service) CreateContentTransfer(ctx context.Context, input clients.ContentTransferInput, key string) (clients.ContentTransfer, error) {
	client, err := s.fabricTransfers()
	if err != nil {
		return clients.ContentTransfer{}, err
	}
	return client.CreateTransfer(ctx, input, key)
}

func (s *Service) ContentTransfer(ctx context.Context, id string) (clients.ContentTransfer, error) {
	client, err := s.fabricTransfers()
	if err != nil {
		return clients.ContentTransfer{}, err
	}
	return client.Transfer(ctx, id)
}

func (s *Service) PutContentTransferChunk(ctx context.Context, id string, index int, body []byte, digest string) (clients.ContentTransfer, error) {
	client, err := s.fabricTransfers()
	if err != nil {
		return clients.ContentTransfer{}, err
	}
	return client.PutTransferChunk(ctx, id, index, body, digest)
}

func (s *Service) CompleteContentTransfer(ctx context.Context, id string) (clients.ContentTransfer, error) {
	client, err := s.fabricTransfers()
	if err != nil {
		return clients.ContentTransfer{}, err
	}
	return client.CompleteTransfer(ctx, id)
}

func (s *Service) Content(ctx context.Context, workspaceID, digest string) (clients.FabricContent, error) {
	client, err := s.fabricTransfers()
	if err != nil {
		return clients.FabricContent{}, err
	}
	return client.Content(ctx, workspaceID, digest)
}

type CreateWorkspaceInput struct {
	WorkspaceID       string `json:"workspaceId"`
	AccountID         string `json:"accountId"`
	Sub2APIUserID     int64  `json:"-"`
	WorkspaceAPIKeyID int64  `json:"workspaceApiKeyId"`
	OwnerID           string `json:"ownerId"`
	Name              string `json:"name"`
	PackageID         string `json:"packageId"`
	AttachmentID      string `json:"attachmentId"`
	ComputeID         string `json:"computeAllocationId"`
	VolumeID          string `json:"storageId"`
	GatewaySecretRef  string `json:"-"`
}

type ResumeWorkspaceInput struct {
	WorkspaceID   string `json:"workspaceId"`
	AccountID     string `json:"accountId"`
	Sub2APIUserID int64  `json:"-"`
	OwnerID       string `json:"ownerId"`
	Name          string `json:"name"`
	PackageID     string `json:"packageId"`
	URL           string `json:"url"`
	AttachmentID  string `json:"attachmentId"`
	ComputeID     string `json:"computeAllocationId"`
	VolumeID      string `json:"storageId"`
}

type RotateWorkspaceCredentialInput struct {
	WorkspaceID   string
	AccountID     string
	Sub2APIUserID int64
	OwnerID       string
	ComputeID     string
	VolumeID      string
}

type ReconciliationInput struct {
	Report map[string]any `json:"report"`
}

type StorageAttachmentInput struct {
	WorkspaceID string `json:"workspaceId"`
	ComputeID   string `json:"computeId"`
	VolumeID    string `json:"volumeId"`
}

type ExecuteInput struct {
	OrganizationID string `json:"organizationId"`
	WorkspaceID    string `json:"workspaceId"`
	ProjectID      string `json:"projectId"`
	TaskID         string `json:"taskId"`
	RequestID      string `json:"requestId"`
	ApprovalID     string `json:"approvalId"`
	EnvironmentRef string `json:"environmentRef,omitempty"`
}

type ExecutionResult struct {
	RequestID      string   `json:"requestId"`
	ApprovalID     string   `json:"approvalId"`
	JobID          string   `json:"jobId"`
	ReceiptID      string   `json:"receiptId"`
	ContinuationID string   `json:"continuationId"`
	Status         string   `json:"status"`
	Attempt        int      `json:"attempt"`
	ArtifactIDs    []string `json:"artifactIds,omitempty"`
	ReviewIDs      []string `json:"reviewIds,omitempty"`
	ErrorCode      string   `json:"errorCode,omitempty"`
}

type ExecutionSyncInput struct {
	OrganizationID string
	WorkspaceID    string
	ProjectID      string
	TaskID         string
	RequestID      string
	ApprovalID     string
	JobID          string
	ReceiptID      string
	ContinuationID string
	Status         string
	EnvironmentRef string
}

func NewService(ledger clients.LedgerClient, fabric clients.FabricClient, sub2API ...clients.Sub2APIClient) *Service {
	service := &Service{ledger: ledger, fabric: fabric}
	if len(sub2API) > 0 {
		service.sub2API = sub2API[0]
	}
	return service
}

func (s *Service) Sub2APIWorkspaceKey(ctx context.Context, userID int64) (clients.Sub2APIWorkspaceKey, error) {
	client, ok := s.sub2API.(clients.Sub2APIWorkspaceKeyClient)
	if !ok {
		return clients.Sub2APIWorkspaceKey{}, errors.New("sub2api_workspace_key_unavailable")
	}
	return client.WorkspaceKey(ctx, userID)
}

func (s *Service) GatewayKeys(ctx context.Context, userID int64) ([]clients.Sub2APIWorkspaceKey, error) {
	client, ok := s.sub2API.(clients.Sub2APIKeyListClient)
	if !ok {
		return nil, errors.New("sub2api_key_list_unavailable")
	}
	return client.Keys(ctx, userID)
}

func (s *Service) Sub2APIWorkspaceKeyByID(ctx context.Context, userID, keyID int64) (clients.Sub2APIWorkspaceKey, error) {
	if userID <= 0 || keyID <= 0 {
		return clients.Sub2APIWorkspaceKey{}, clients.ErrSub2APIWorkspaceKeyMissing
	}
	keys, err := s.GatewayKeys(ctx, userID)
	if err != nil {
		return clients.Sub2APIWorkspaceKey{}, err
	}
	for _, key := range keys {
		if key.ID == keyID && key.UserID == userID {
			return key, nil
		}
	}
	return clients.Sub2APIWorkspaceKey{}, clients.ErrSub2APIWorkspaceKeyMissing
}

func (s *Service) GatewayUserKeys(ctx context.Context, credential clients.SessionDelegatedCredential, userID int64) ([]clients.Sub2APIWorkspaceKey, error) {
	client, ok := s.sub2API.(clients.Sub2APIUserKeyReadClient)
	if !ok {
		return nil, errors.New("sub2api_user_key_unavailable")
	}
	return client.UserKeys(ctx, credential, userID)
}

func (s *Service) GatewayUserKey(ctx context.Context, credential clients.SessionDelegatedCredential, userID, keyID int64) (clients.Sub2APIWorkspaceKey, error) {
	client, ok := s.sub2API.(clients.Sub2APIUserKeyReadClient)
	if !ok {
		return clients.Sub2APIWorkspaceKey{}, errors.New("sub2api_user_key_unavailable")
	}
	return client.UserKey(ctx, credential, userID, keyID)
}

func (s *Service) CreateGatewayUserKey(ctx context.Context, credential clients.SessionDelegatedCredential, userID int64, input clients.Sub2APICreateKeyInput, idempotencyKey string) (clients.Sub2APIWorkspaceKey, error) {
	client, ok := s.sub2API.(clients.Sub2APIUserKeyMutationClient)
	if !ok {
		return clients.Sub2APIWorkspaceKey{}, errors.New("sub2api_user_key_unavailable")
	}
	return client.CreateUserKey(ctx, credential, userID, input, idempotencyKey)
}

func (s *Service) UpdateGatewayUserKey(ctx context.Context, credential clients.SessionDelegatedCredential, userID, keyID int64, input clients.Sub2APIUpdateKeyInput) (clients.Sub2APIWorkspaceKey, error) {
	client, ok := s.sub2API.(clients.Sub2APIUserKeyMutationClient)
	if !ok {
		return clients.Sub2APIWorkspaceKey{}, errors.New("sub2api_user_key_unavailable")
	}
	return client.UpdateUserKey(ctx, credential, userID, keyID, input)
}

func (s *Service) DeleteGatewayUserKey(ctx context.Context, credential clients.SessionDelegatedCredential, userID, keyID int64) error {
	client, ok := s.sub2API.(clients.Sub2APIUserKeyMutationClient)
	if !ok {
		return errors.New("sub2api_user_key_unavailable")
	}
	return client.DeleteUserKey(ctx, credential, userID, keyID)
}

func (s *Service) Sub2APIUser(ctx context.Context, userID int64) (clients.Sub2APIIdentity, error) {
	client, ok := s.sub2API.(clients.Sub2APIUserReadClient)
	if !ok {
		return clients.Sub2APIIdentity{}, errors.New("sub2api_user_read_unavailable")
	}
	identity, err := client.User(ctx, userID)
	if err != nil {
		return clients.Sub2APIIdentity{}, err
	}
	identity.Email = strings.ToLower(strings.TrimSpace(identity.Email))
	if identity.ID != userID || identity.Email == "" || (identity.Status != "active" && identity.Status != "disabled") {
		return clients.Sub2APIIdentity{}, errors.New("sub2api_user_read_invalid")
	}
	return identity, nil
}

func (s *Service) Sub2APIAdminUsers(ctx context.Context, query clients.Sub2APIUserPageQuery) (clients.Sub2APIUserPage, error) {
	client, ok := s.sub2API.(clients.Sub2APIAdminUsersClient)
	if !ok {
		return clients.Sub2APIUserPage{}, errors.New("sub2api_admin_users_unavailable")
	}
	return client.AdminUsers(ctx, query)
}

func (s *Service) Sub2APIBatchUsersUsage(ctx context.Context, userIDs []int64) (map[int64]clients.Sub2APIBatchUserUsage, error) {
	client, ok := s.sub2API.(clients.Sub2APIBatchUsersUsageClient)
	if !ok {
		return nil, errors.New("sub2api_batch_users_usage_unavailable")
	}
	return client.BatchUsersUsage(ctx, userIDs)
}

func (s *Service) Sub2APIBatchKeysUsage(ctx context.Context, keyIDs []int64) (map[int64]clients.Sub2APIBatchKeyUsage, error) {
	client, ok := s.sub2API.(clients.Sub2APIBatchKeysUsageClient)
	if !ok {
		return nil, errors.New("sub2api_batch_keys_usage_unavailable")
	}
	return client.BatchKeysUsage(ctx, keyIDs)
}

func (s *Service) Sub2APIVersion(ctx context.Context) (string, error) {
	if s.sub2API == nil {
		return "", errors.New("sub2api_unavailable")
	}
	return s.sub2API.Version(ctx)
}

func (s *Service) ResolveOrCreateSub2APIUser(ctx context.Context, email, password string) (clients.Sub2APIIdentity, error) {
	client, ok := s.sub2API.(clients.Sub2APIIdentityClient)
	if !ok {
		return clients.Sub2APIIdentity{}, errors.New("sub2api_identity_unavailable")
	}
	return client.ResolveOrCreateUser(ctx, email, password)
}

func (s *Service) AuthenticateSub2APIUser(ctx context.Context, email, password string) (clients.Sub2APIUserAuthentication, error) {
	client, ok := s.sub2API.(clients.Sub2APIIdentityClient)
	if !ok {
		return clients.Sub2APIUserAuthentication{}, clients.ErrSub2APIAuthUnavailable
	}
	return client.AuthenticateUser(ctx, email, password)
}

func (s *Service) Sub2APIAdminIdentity(ctx context.Context) (clients.Sub2APIIdentity, error) {
	client, ok := s.sub2API.(clients.Sub2APIAdminIdentityClient)
	if !ok {
		return clients.Sub2APIIdentity{}, clients.ErrSub2APIAuthUnavailable
	}
	return client.AdminIdentity(ctx)
}

func (s *Service) GatewayKeyUsage(ctx context.Context, credential clients.SessionDelegatedCredential, userID, keyID int64, page, pageSize int) (clients.Sub2APIUsagePage, error) {
	if _, err := s.GatewayUserKey(ctx, credential, userID, keyID); err != nil {
		return clients.Sub2APIUsagePage{}, err
	}
	client, ok := s.sub2API.(clients.Sub2APIUsageClient)
	if !ok {
		return clients.Sub2APIUsagePage{}, errors.New("sub2api_usage_unavailable")
	}
	return client.Usage(ctx, clients.Sub2APIUsageQuery{UserID: userID, APIKeyID: keyID, Page: page, PageSize: pageSize})
}

func (s *Service) GatewayKeyUsageStats(ctx context.Context, credential clients.SessionDelegatedCredential, userID, keyID int64, period string) (clients.Sub2APIUsageStats, error) {
	if _, err := s.GatewayUserKey(ctx, credential, userID, keyID); err != nil {
		return clients.Sub2APIUsageStats{}, err
	}
	client, ok := s.sub2API.(clients.Sub2APIUsageClient)
	if !ok {
		return clients.Sub2APIUsageStats{}, errors.New("sub2api_usage_unavailable")
	}
	return client.UsageStats(ctx, clients.Sub2APIUsageStatsQuery{UserID: userID, APIKeyID: keyID, Period: period})
}

func (s *Service) GatewayAccountUsageStats(ctx context.Context, userID int64, period string) (clients.Sub2APIUsageStats, error) {
	client, ok := s.sub2API.(clients.Sub2APIUsageClient)
	if !ok {
		return clients.Sub2APIUsageStats{}, errors.New("sub2api_usage_unavailable")
	}
	return client.UsageStats(ctx, clients.Sub2APIUsageStatsQuery{UserID: userID, Period: period})
}

func (s *Service) Sub2APIBalanceHistory(ctx context.Context, userID int64) ([]clients.Sub2APIBalanceHistoryEntry, error) {
	client, ok := s.sub2API.(clients.Sub2APIUsageClient)
	if !ok {
		return nil, errors.New("sub2api_balance_history_unavailable")
	}
	return client.BalanceHistory(ctx, userID)
}

func (s *Service) BillingReceipt(ctx context.Context, receiptID string) (clients.Receipt, error) {
	if receiptID == "" {
		return clients.Receipt{}, fmt.Errorf("receipt_id_required")
	}
	return s.ledger.Receipt(ctx, receiptID)
}

func (s *Service) BillingReceipts(ctx context.Context, query clients.ReceiptQuery) (clients.ReceiptPage, error) {
	client, ok := s.ledger.(clients.LedgerReceiptListClient)
	if !ok {
		return clients.ReceiptPage{}, errors.New("ledger_receipt_list_unavailable")
	}
	if query.AccountID == "" {
		return clients.ReceiptPage{}, errors.New("billing_account_id_required")
	}
	return client.ListReceipts(ctx, query)
}

func (s *Service) Execute(ctx context.Context, input ExecuteInput, idempotencyKey string) (ExecutionResult, error) {
	if input.OrganizationID == "" || input.WorkspaceID == "" || input.ProjectID == "" || input.TaskID == "" || input.RequestID == "" || input.ApprovalID == "" || idempotencyKey == "" {
		return ExecutionResult{}, fmt.Errorf("execution_identity_required")
	}
	job, err := s.fabric.CreateJob(ctx, clients.JobInput{
		OrganizationID: input.OrganizationID,
		WorkspaceID:    input.WorkspaceID,
		ProjectID:      input.ProjectID,
		TaskID:         input.TaskID,
		RequestID:      input.RequestID,
		ApprovalID:     input.ApprovalID,
		EnvironmentRef: input.EnvironmentRef,
	}, idempotencyKey+":job")
	if err != nil {
		return ExecutionResult{}, err
	}
	receipt, err := s.ledger.RecordReceipt(ctx, clients.ReceiptInput{
		Type:           "execution.receipt.v1",
		Status:         "running",
		Surface:        "workspace",
		OrganizationID: input.OrganizationID,
		WorkspaceID:    input.WorkspaceID,
		ProjectID:      input.ProjectID,
		TaskID:         input.TaskID,
		RequestID:      input.RequestID,
		ApprovalID:     input.ApprovalID,
		JobID:          job.JobID,
		Execution:      map[string]any{"jobStatus": job.Status},
		Continuation:   map[string]any{"taskVersion": 1, "environmentRef": input.EnvironmentRef},
	}, idempotencyKey+":receipt")
	if err != nil {
		return ExecutionResult{}, err
	}
	return ExecutionResult{RequestID: input.RequestID, ApprovalID: input.ApprovalID, JobID: job.JobID, ReceiptID: receipt.ReceiptID, ContinuationID: receipt.ContinuationID, Status: job.Status}, nil
}

func (s *Service) SyncExecution(ctx context.Context, input ExecutionSyncInput) (ExecutionResult, error) {
	if input.RequestID == "" || input.JobID == "" || input.ReceiptID == "" {
		return ExecutionResult{}, fmt.Errorf("execution_identity_required")
	}
	job, err := s.fabric.GetJob(ctx, input.JobID)
	if err != nil {
		return ExecutionResult{}, err
	}
	result := ExecutionResult{RequestID: input.RequestID, ApprovalID: input.ApprovalID, JobID: input.JobID, ReceiptID: input.ReceiptID, ContinuationID: input.ContinuationID, Status: job.Status, Attempt: job.Attempt, ArtifactIDs: job.ArtifactIDs, ReviewIDs: job.ReviewIDs, ErrorCode: job.ErrorCode}
	if job.Status == "queued" || job.Status == "running" {
		return result, nil
	}
	if input.OrganizationID == "" || input.WorkspaceID == "" || input.ProjectID == "" || input.TaskID == "" {
		return ExecutionResult{}, fmt.Errorf("execution_identity_required")
	}
	status := job.Status
	reviewerChecks := map[string]any{}
	if job.Status == "succeeded" {
		status, reviewerChecks, err = s.verifyExecutionEvidence(ctx, input, job)
		if err != nil {
			return ExecutionResult{}, err
		}
	} else if job.Status != "failed" && job.Status != "timed_out" && job.Status != "cancelled" {
		return ExecutionResult{}, fmt.Errorf("job_status_invalid")
	}
	if input.Status == status {
		current, err := s.ledger.Receipt(ctx, input.ReceiptID)
		if err != nil {
			return ExecutionResult{}, err
		}
		if current.Status == status && stringFromAny(current.Execution["jobStatus"]) == job.Status && intFromAny(current.Execution["attempt"]) == job.Attempt {
			result.Status = status
			return result, nil
		}
	}
	continuation := map[string]any(nil)
	if status == "completed" {
		continuation = map[string]any{"taskVersion": 1, "environmentRef": input.EnvironmentRef, "artifactIds": job.ArtifactIDs, "reviewIds": job.ReviewIDs}
	}
	receipt, err := s.ledger.RecordReceipt(ctx, clients.ReceiptInput{
		Type: "execution.receipt.v1", Status: status, Surface: "workspace",
		OrganizationID: input.OrganizationID, WorkspaceID: input.WorkspaceID, ProjectID: input.ProjectID, TaskID: input.TaskID,
		RequestID: input.RequestID, ApprovalID: input.ApprovalID, JobID: input.JobID,
		ArtifactID: firstString(job.ArtifactIDs), ReviewID: firstString(job.ReviewIDs),
		Execution:  map[string]any{"jobStatus": job.Status, "attempt": job.Attempt, "errorCode": job.ErrorCode},
		OutputRefs: map[string]any{"artifactIds": job.ArtifactIDs, "reviewIds": job.ReviewIDs}, ReviewerChecks: reviewerChecks,
		Continuation: continuation, SupersedesReceiptID: input.ReceiptID,
	}, fmt.Sprintf("execution-final:%s:%s:%d", input.RequestID, job.Status, job.Attempt))
	if err != nil {
		return ExecutionResult{}, err
	}
	result.Status = status
	result.ReceiptID = receipt.ReceiptID
	if status == "completed" {
		result.ContinuationID = receipt.ContinuationID
	}
	return result, nil
}

func (s *Service) verifyExecutionEvidence(ctx context.Context, input ExecutionSyncInput, job clients.Job) (string, map[string]any, error) {
	if len(job.ArtifactIDs) == 0 || len(job.ReviewIDs) == 0 {
		return "", nil, fmt.Errorf("execution_evidence_required")
	}
	digests := make([]string, 0, len(job.ArtifactIDs))
	for _, artifactID := range job.ArtifactIDs {
		artifact, err := s.ledger.Artifact(ctx, artifactID)
		if err != nil {
			return "", nil, err
		}
		if artifact.ArtifactID != artifactID || artifact.JobID != job.JobID || artifact.Digest == "" || evidenceIdentityMismatch(input, artifact.OrganizationID, artifact.WorkspaceID, artifact.ProjectID, artifact.TaskID) {
			return "", nil, fmt.Errorf("artifact_evidence_mismatch")
		}
		digests = append(digests, artifact.Digest)
	}
	decisions := map[string]any{}
	reviewedDigests := map[string]bool{}
	blocked := false
	for _, reviewID := range job.ReviewIDs {
		review, err := s.ledger.Review(ctx, reviewID)
		if err != nil {
			return "", nil, err
		}
		if review.ReviewID != reviewID || review.JobID != job.JobID || evidenceIdentityMismatch(input, review.OrganizationID, review.WorkspaceID, review.ProjectID, review.TaskID) || (review.Decision != "accepted" && review.Decision != "rejected") {
			return "", nil, fmt.Errorf("review_evidence_mismatch")
		}
		decisions[reviewID] = review.Decision
		blocked = blocked || review.Decision == "rejected"
		for _, digest := range review.InputArtifactDigests {
			reviewedDigests[digest] = true
		}
	}
	for _, digest := range digests {
		if !reviewedDigests[digest] {
			return "", nil, fmt.Errorf("review_artifact_mismatch")
		}
	}
	if blocked {
		return "review_blocked", map[string]any{"decisions": decisions}, nil
	}
	return "completed", map[string]any{"decisions": decisions}, nil
}

func evidenceIdentityMismatch(input ExecutionSyncInput, organizationID, workspaceID, projectID, taskID string) bool {
	return (organizationID != "" && organizationID != input.OrganizationID) || (workspaceID != "" && workspaceID != input.WorkspaceID) || (projectID != "" && projectID != input.ProjectID) || (taskID != "" && taskID != input.TaskID)
}

func firstString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func stringFromAny(value any) string {
	text, _ := value.(string)
	return text
}

func intFromAny(value any) int {
	switch number := value.(type) {
	case int:
		return number
	case int64:
		return int(number)
	case float64:
		return int(number)
	default:
		return 0
	}
}

func (s *Service) Continuation(ctx context.Context, receiptID string) (map[string]any, error) {
	return s.ledger.Continuation(ctx, receiptID)
}

func (s *Service) RecordReconciliation(ctx context.Context, input ReconciliationInput, idempotencyKey string) (clients.ReconciliationResult, error) {
	return s.ledger.RecordReconciliation(ctx, clients.ReconciliationInput{Report: input.Report}, idempotencyKey)
}

func (s *Service) WorkspaceRuntimeStatus(ctx context.Context, workspaceID string) (clients.WorkspaceRuntime, error) {
	return s.fabric.WorkspaceRuntimeStatus(ctx, workspaceID)
}

func (s *Service) RuntimeReadiness(ctx context.Context) (map[string]any, error) {
	return s.fabric.Readiness(ctx)
}

func (s *Service) FabricOperations(ctx context.Context) ([]clients.FabricOperation, error) {
	return s.fabric.ListOperations(ctx)
}

func (s *Service) FabricCatalog(ctx context.Context) (clients.FabricCatalog, error) {
	return s.fabric.Catalog(ctx)
}

func (s *Service) CreateStorageAttachment(ctx context.Context, input StorageAttachmentInput, idempotencyKey string) (clients.StorageAttachment, error) {
	return s.fabric.CreateStorageAttachment(ctx, clients.StorageAttachmentInput{WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID}, idempotencyKey)
}

func (s *Service) DetachStorageAttachment(ctx context.Context, id string, idempotencyKey string) (clients.StorageAttachment, error) {
	return s.fabric.DetachStorageAttachment(ctx, id, idempotencyKey)
}

func (s *Service) PrepareWorkspace(ctx context.Context, input CreateWorkspaceInput, idempotencyKey string) (domain.WorkspaceProjection, error) {
	if input.WorkspaceID == "" || input.ComputeID == "" || input.VolumeID == "" || input.AttachmentID == "" {
		return domain.WorkspaceProjection{}, fmt.Errorf("attached_compute_storage_required")
	}
	workspaceID := input.WorkspaceID
	gatewaySecretRef := input.GatewaySecretRef
	if gatewaySecretRef == "" {
		var err error
		if input.WorkspaceAPIKeyID > 0 {
			gatewaySecretRef, err = s.gatewaySecretRefByID(ctx, input.AccountID, input.Sub2APIUserID, input.WorkspaceAPIKeyID, idempotencyKey)
		} else {
			gatewaySecretRef, err = s.gatewaySecretRef(ctx, input.AccountID, input.Sub2APIUserID, idempotencyKey)
		}
		if err != nil {
			return domain.WorkspaceProjection{}, err
		}
	}
	runtime, err := s.fabric.CreateWorkspaceRuntime(ctx, clients.WorkspaceRuntimeInput{WorkspaceID: workspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID, ImageID: "one-person-lab-app", GatewaySecretRef: gatewaySecretRef}, idempotencyKey+":runtime")
	if err != nil {
		return domain.WorkspaceProjection{}, err
	}
	readback, err := s.fabric.WorkspaceRuntimeStatus(ctx, workspaceID)
	if err != nil {
		return domain.WorkspaceProjection{}, err
	}
	runtime, err = mergeWorkspaceRuntimeReadback(runtime, readback, workspaceID)
	if err != nil {
		return domain.WorkspaceProjection{}, err
	}
	status := workspaceRuntimeState(runtime.Status, runtime.Ready)
	workspace := domain.WorkspaceProjection{
		ID:                  workspaceID,
		AccountID:           input.AccountID,
		OwnerID:             input.OwnerID,
		Name:                input.Name,
		PackageID:           input.PackageID,
		Provider:            "tencent-tke",
		URL:                 runtime.URL,
		Status:              status,
		ComputeID:           input.ComputeID,
		VolumeID:            input.VolumeID,
		AttachmentID:        input.AttachmentID,
		RuntimeID:           runtime.ID,
		RuntimeServiceName:  runtime.ServiceName,
		WorkspaceAPIKeyID:   input.WorkspaceAPIKeyID,
		RuntimeReady:        runtime.Ready,
		RuntimeUsername:     runtime.Access.Username,
		CredentialStatus:    runtime.Access.CredentialStatus,
		CredentialVersion:   runtime.Access.CredentialVersion,
		CredentialSecretRef: runtime.Access.SecretRef,
	}
	return workspace, nil
}

func mergeWorkspaceRuntimeReadback(created, readback clients.WorkspaceRuntime, workspaceID string) (clients.WorkspaceRuntime, error) {
	if workspaceID == "" || created.WorkspaceID != "" && created.WorkspaceID != workspaceID || readback.WorkspaceID != workspaceID ||
		created.ID != "" && readback.ID != "" && created.ID != readback.ID ||
		created.ServiceName != "" && readback.ServiceName != "" && created.ServiceName != readback.ServiceName {
		return clients.WorkspaceRuntime{}, ErrWorkspaceRuntimeIdentityMismatch
	}
	if readback.Ready || readback.Status == "running" {
		if !readback.Ready || readback.Status != "running" || readback.ID == "" || readback.URL == "" || readback.ServiceName == "" ||
			readback.Access.Username == "" || readback.Access.CredentialStatus != "configured" || readback.Access.CredentialVersion == "" || readback.Access.SecretRef == "" {
			return clients.WorkspaceRuntime{}, ErrWorkspaceRuntimeReadbackInvalid
		}
	}
	return readback, nil
}

func (s *Service) PrepareWorkspaceResume(ctx context.Context, input ResumeWorkspaceInput, idempotencyKey string) (domain.WorkspaceProjection, error) {
	gatewaySecretRef, err := s.gatewaySecretRef(ctx, input.AccountID, input.Sub2APIUserID, idempotencyKey)
	if err != nil {
		return domain.WorkspaceProjection{}, err
	}
	runtime, err := s.fabric.CreateWorkspaceRuntime(ctx, clients.WorkspaceRuntimeInput{WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID, ImageID: "one-person-lab-app", GatewaySecretRef: gatewaySecretRef}, idempotencyKey+":runtime")
	if err != nil {
		return domain.WorkspaceProjection{}, err
	}
	url := input.URL
	if url == "" {
		url = runtime.URL
	}
	status := workspaceRuntimeState(runtime.Status, runtime.Ready)
	workspace := domain.WorkspaceProjection{ID: input.WorkspaceID, AccountID: input.AccountID, OwnerID: input.OwnerID, Name: input.Name, PackageID: input.PackageID, Provider: "tencent-tke", URL: url, Status: status, ComputeID: input.ComputeID, VolumeID: input.VolumeID, AttachmentID: input.AttachmentID, RuntimeID: runtime.ID, RuntimeServiceName: runtime.ServiceName, RuntimeReady: runtime.Ready, RuntimeUsername: runtime.Access.Username, CredentialStatus: runtime.Access.CredentialStatus, CredentialVersion: runtime.Access.CredentialVersion, CredentialSecretRef: runtime.Access.SecretRef}
	return workspace, nil
}

func (s *Service) RecordWorkspaceCreatedReceipt(ctx context.Context, workspace domain.WorkspaceProjection, idempotencyKey string) (domain.WorkspaceProjection, error) {
	input := clients.ReceiptInput{Type: "workspace.created", Status: "completed", Surface: "workspace", AccountID: workspace.AccountID, WorkspaceID: workspace.ID, JobID: workspace.RuntimeID, Execution: map[string]any{"providerRequestId": workspace.RuntimeID}, OutputRefs: map[string]any{"redactedUrl": workspace.URL}}
	return s.recordWorkspaceReceipt(ctx, workspace, input, idempotencyKey)
}

func (s *Service) RecordWorkspaceResumedReceipt(ctx context.Context, workspace domain.WorkspaceProjection, idempotencyKey string) (domain.WorkspaceProjection, error) {
	input := clients.ReceiptInput{Type: "workspace.compute_restarted", Status: "completed", Surface: "workspace", AccountID: workspace.AccountID, WorkspaceID: workspace.ID, JobID: workspace.RuntimeID, Execution: map[string]any{"providerRequestId": workspace.RuntimeID, "computeAllocationId": workspace.ComputeID, "storageAttachmentId": workspace.AttachmentID}, OutputRefs: map[string]any{"redactedUrl": workspace.URL}}
	return s.recordWorkspaceReceipt(ctx, workspace, input, idempotencyKey)
}

func (s *Service) recordWorkspaceReceipt(ctx context.Context, workspace domain.WorkspaceProjection, input clients.ReceiptInput, idempotencyKey string) (domain.WorkspaceProjection, error) {
	receipt, err := s.ledger.RecordReceipt(ctx, input, idempotencyKey+":receipt")
	if err != nil {
		return workspace, err
	}
	workspace.ReceiptID = receipt.ReceiptID
	return workspace, nil
}

func (s *Service) gatewaySecretRef(ctx context.Context, accountID string, userID int64, idempotencyKey string) (string, error) {
	secret, err := s.SyncWorkspaceGatewaySecret(ctx, accountID, userID, idempotencyKey)
	return secret.SecretRef, err
}

func (s *Service) gatewaySecretRefByID(ctx context.Context, accountID string, userID, keyID int64, idempotencyKey string) (string, error) {
	secret, err := s.SyncWorkspaceGatewaySecretByID(ctx, accountID, userID, keyID, idempotencyKey)
	return secret.SecretRef, err
}

func (s *Service) SyncWorkspaceGatewaySecretByID(ctx context.Context, accountID string, userID, keyID int64, idempotencyKey string) (clients.GatewaySecretWriteResult, error) {
	key, err := s.Sub2APIWorkspaceKeyByID(ctx, userID, keyID)
	if err != nil {
		return clients.GatewaySecretWriteResult{}, err
	}
	return s.writeWorkspaceGatewaySecret(ctx, accountID, userID, key, idempotencyKey)
}

func (s *Service) SyncWorkspaceGatewayReplacementSecret(ctx context.Context, credential clients.SessionDelegatedCredential, accountID string, userID, keyID int64, replacementName, idempotencyKey string) (clients.GatewaySecretWriteResult, error) {
	key, err := s.GatewayUserKey(ctx, credential, userID, keyID)
	if err != nil {
		return clients.GatewaySecretWriteResult{}, err
	}
	if replacementName == "" || key.Name != replacementName || !strings.HasPrefix(replacementName, "opl-workspace-replacement-") {
		return clients.GatewaySecretWriteResult{}, errors.New("invalid_sub2api_workspace_key")
	}
	return s.writeGatewaySecretValue(ctx, accountID, userID, key, idempotencyKey)
}

func (s *Service) SyncWorkspaceGatewaySecret(ctx context.Context, accountID string, userID int64, idempotencyKey string) (clients.GatewaySecretWriteResult, error) {
	if accountID == "" || userID <= 0 || idempotencyKey == "" {
		return clients.GatewaySecretWriteResult{}, errors.New("gateway_secret_write_failed")
	}
	key, err := s.Sub2APIWorkspaceKey(ctx, userID)
	if err != nil {
		return clients.GatewaySecretWriteResult{}, err
	}
	return s.writeWorkspaceGatewaySecret(ctx, accountID, userID, key, idempotencyKey)
}

func (s *Service) writeWorkspaceGatewaySecret(ctx context.Context, accountID string, userID int64, key clients.Sub2APIWorkspaceKey, idempotencyKey string) (clients.GatewaySecretWriteResult, error) {
	if key.Name != "opl-workspace" {
		return clients.GatewaySecretWriteResult{}, errors.New("invalid_sub2api_workspace_key")
	}
	return s.writeGatewaySecretValue(ctx, accountID, userID, key, idempotencyKey)
}

func (s *Service) writeGatewaySecretValue(ctx context.Context, accountID string, userID int64, key clients.Sub2APIWorkspaceKey, idempotencyKey string) (clients.GatewaySecretWriteResult, error) {
	if accountID == "" || userID <= 0 || idempotencyKey == "" || key.ID <= 0 || key.UserID != userID || key.Status != "active" || key.Key == "" {
		return clients.GatewaySecretWriteResult{}, errors.New("invalid_sub2api_workspace_key")
	}
	secret, err := s.fabric.WriteGatewaySecret(ctx, clients.GatewaySecretWriteInput{AccountID: accountID, GatewayAPIKey: key.Key}, idempotencyKey+":gateway-secret")
	if err != nil {
		return clients.GatewaySecretWriteResult{}, fmt.Errorf("gateway_secret_write_failed: %w", err)
	}
	if secret.SecretRef == "" || secret.Fingerprint == "" {
		return clients.GatewaySecretWriteResult{}, errors.New("gateway_secret_write_failed")
	}
	return secret, nil
}

func (s *Service) ReapplyWorkspaceRuntime(ctx context.Context, workspaceID, computeID, volumeID, secretRef, idempotencyKey string) (clients.WorkspaceRuntime, error) {
	if workspaceID == "" || computeID == "" || volumeID == "" || secretRef == "" || idempotencyKey == "" {
		return clients.WorkspaceRuntime{}, errors.New("workspace_runtime_apply_input_required")
	}
	applied, err := s.fabric.CreateWorkspaceRuntime(ctx, clients.WorkspaceRuntimeInput{
		WorkspaceID: workspaceID, ComputeID: computeID, VolumeID: volumeID,
		ImageID: "one-person-lab-app", GatewaySecretRef: secretRef,
	}, idempotencyKey+":runtime")
	if err != nil {
		return clients.WorkspaceRuntime{}, err
	}
	runtime, err := s.fabric.WorkspaceRuntimeStatus(ctx, workspaceID)
	if err != nil {
		return clients.WorkspaceRuntime{}, err
	}
	if runtime.ID == "" {
		runtime.ID = applied.ID
	}
	if runtime.WorkspaceID == "" {
		runtime.WorkspaceID = applied.WorkspaceID
	}
	if runtime.ServiceName == "" {
		runtime.ServiceName = applied.ServiceName
	}
	if runtime.WorkspaceID != workspaceID || runtime.ID == "" || runtime.Status == "not_found" || !runtime.Ready {
		return clients.WorkspaceRuntime{}, errors.New("workspace_runtime_readback_invalid")
	}
	return runtime, nil
}

func (s *Service) RecordWorkspaceGatewayKeyRotation(ctx context.Context, accountID, workspaceID, ownerID, operationID string, oldKeyID, newKeyID int64, fingerprint string) (clients.Receipt, error) {
	if accountID == "" || workspaceID == "" || ownerID == "" || operationID == "" || oldKeyID <= 0 || newKeyID <= 0 || oldKeyID == newKeyID || fingerprint == "" {
		return clients.Receipt{}, errors.New("workspace_gateway_key_rotation_evidence_invalid")
	}
	return s.ledger.RecordReceipt(ctx, clients.ReceiptInput{
		Type: "workspace.gateway_key_rotated.v1", Status: "completed", Surface: "control_plane",
		AccountID: accountID, WorkspaceID: workspaceID,
		Execution:  map[string]any{"operationId": operationID, "oldKeyId": oldKeyID, "newKeyId": newKeyID},
		OutputRefs: map[string]any{"secretFingerprint": fingerprint},
		Owner:      map[string]any{"userId": ownerID},
	}, operationID+":receipt")
}

func (s *Service) RotateWorkspaceCredential(ctx context.Context, input RotateWorkspaceCredentialInput, idempotencyKey string) (clients.WorkspaceRuntime, clients.Receipt, error) {
	if input.WorkspaceID == "" || input.AccountID == "" || input.Sub2APIUserID <= 0 || input.OwnerID == "" || input.ComputeID == "" || input.VolumeID == "" || idempotencyKey == "" {
		return clients.WorkspaceRuntime{}, clients.Receipt{}, errors.New("runtime_credential_rotation_input_required")
	}
	operationKey := "runtime-credential-rotate:" + input.WorkspaceID + ":" + idempotencyKey
	secret, err := s.SyncWorkspaceGatewaySecret(ctx, input.AccountID, input.Sub2APIUserID, operationKey+":gateway")
	if err != nil {
		return clients.WorkspaceRuntime{}, clients.Receipt{}, err
	}
	applied, err := s.fabric.CreateWorkspaceRuntime(ctx, clients.WorkspaceRuntimeInput{
		WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID,
		ImageID: "one-person-lab-app", GatewaySecretRef: secret.SecretRef,
	}, operationKey+":runtime")
	if err != nil {
		return clients.WorkspaceRuntime{}, clients.Receipt{}, err
	}
	runtime, err := s.fabric.WorkspaceRuntimeStatus(ctx, input.WorkspaceID)
	if err != nil {
		return clients.WorkspaceRuntime{}, clients.Receipt{}, err
	}
	if runtime.ID == "" {
		runtime.ID = applied.ID
	}
	if runtime.WorkspaceID == "" {
		runtime.WorkspaceID = input.WorkspaceID
	}
	if runtime.ServiceName == "" {
		runtime.ServiceName = applied.ServiceName
	}
	if runtime.Access.Password == "" {
		return clients.WorkspaceRuntime{}, clients.Receipt{}, errors.New("workspace_credentials_unavailable")
	}
	receipt, err := s.ledger.RecordReceipt(ctx, clients.ReceiptInput{
		Type: "workspace.access_token_reset", Status: "completed", Surface: "workspace",
		AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, JobID: runtime.ID,
		Execution: map[string]any{
			"runtimeId": runtime.ID, "computeAllocationId": input.ComputeID, "storageId": input.VolumeID,
		},
		OutputRefs: map[string]any{
			"runtimeId": runtime.ID, "credentialVersion": runtime.Access.CredentialVersion, "credentialSecretRef": runtime.Access.SecretRef,
		},
		Owner: map[string]any{"userId": input.OwnerID},
	}, operationKey)
	if err != nil {
		return runtime, clients.Receipt{}, err
	}
	return runtime, receipt, nil
}

func workspaceRuntimeState(status string, ready bool) string {
	if ready {
		if status == "" {
			return "running"
		}
		return status
	}
	if status == "" || status == "running" {
		return "unready"
	}
	return status
}
