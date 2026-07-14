package controlplane

import (
	"context"
	"errors"
	"fmt"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/domain"
)

const workspaceCompensationTimeout = 30 * time.Second

type Service struct {
	ledger  clients.LedgerClient
	fabric  clients.FabricClient
	sub2API clients.Sub2APIClient
}

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
	AccountID    string `json:"accountId"`
	OwnerID      string `json:"ownerId"`
	Name         string `json:"name"`
	PackageID    string `json:"packageId"`
	AttachmentID string `json:"attachmentId"`
	ComputeID    string `json:"computeAllocationId"`
	VolumeID     string `json:"storageId"`
}

type ResumeWorkspaceInput struct {
	WorkspaceID  string `json:"workspaceId"`
	AccountID    string `json:"accountId"`
	OwnerID      string `json:"ownerId"`
	Name         string `json:"name"`
	PackageID    string `json:"packageId"`
	URL          string `json:"url"`
	AttachmentID string `json:"attachmentId"`
	ComputeID    string `json:"computeAllocationId"`
	VolumeID     string `json:"storageId"`
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

func (s *Service) BillingReceipt(ctx context.Context, receiptID string) (clients.Receipt, error) {
	if receiptID == "" {
		return clients.Receipt{}, fmt.Errorf("receipt_id_required")
	}
	return s.ledger.Receipt(ctx, receiptID)
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

func (s *Service) CreateWorkspace(ctx context.Context, input CreateWorkspaceInput, idempotencyKey string) (domain.WorkspaceProjection, error) {
	if input.ComputeID == "" || input.VolumeID == "" || input.AttachmentID == "" {
		return domain.WorkspaceProjection{}, fmt.Errorf("attached_compute_storage_required")
	}
	workspaceID := fmt.Sprintf("ws_%d", time.Now().UTC().UnixNano())
	runtime, err := s.fabric.CreateWorkspaceRuntime(ctx, clients.WorkspaceRuntimeInput{WorkspaceID: workspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID, ImageID: "one-person-lab-app"}, idempotencyKey+":runtime")
	if err != nil {
		return domain.WorkspaceProjection{}, err
	}
	receipt, err := s.ledger.RecordReceipt(ctx, clients.ReceiptInput{Type: "workspace.created", Status: "completed", Surface: "workspace", WorkspaceID: workspaceID, JobID: runtime.ID, Execution: map[string]any{"providerRequestId": runtime.ID}, OutputRefs: map[string]any{"redactedUrl": runtime.URL}}, idempotencyKey+":receipt")
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), workspaceCompensationTimeout)
		defer cancel()
		_, cleanupErr := s.fabric.DestroyWorkspaceRuntime(cleanupCtx, workspaceID, idempotencyKey+":runtime-compensation")
		return domain.WorkspaceProjection{}, errors.Join(err, cleanupErr)
	}

	return domain.WorkspaceProjection{
		ID:                  workspaceID,
		AccountID:           input.AccountID,
		OwnerID:             input.OwnerID,
		Name:                input.Name,
		PackageID:           input.PackageID,
		Provider:            "tencent-tke",
		URL:                 runtime.URL,
		Status:              "running",
		ComputeID:           input.ComputeID,
		VolumeID:            input.VolumeID,
		AttachmentID:        input.AttachmentID,
		RuntimeID:           runtime.ID,
		RuntimeServiceName:  runtime.ServiceName,
		RuntimeUsername:     runtime.Access.Username,
		CredentialStatus:    runtime.Access.CredentialStatus,
		CredentialVersion:   runtime.Access.CredentialVersion,
		CredentialSecretRef: runtime.Access.SecretRef,
		ReceiptID:           receipt.ReceiptID,
	}, nil
}

func (s *Service) ResumeWorkspace(ctx context.Context, input ResumeWorkspaceInput, idempotencyKey string) (domain.WorkspaceProjection, error) {
	runtime, err := s.fabric.CreateWorkspaceRuntime(ctx, clients.WorkspaceRuntimeInput{WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID, ImageID: "one-person-lab-app"}, idempotencyKey+":runtime")
	if err != nil {
		return domain.WorkspaceProjection{}, err
	}
	url := input.URL
	if url == "" {
		url = runtime.URL
	}
	receipt, err := s.ledger.RecordReceipt(ctx, clients.ReceiptInput{Type: "workspace.compute_restarted", Status: "completed", Surface: "workspace", WorkspaceID: input.WorkspaceID, JobID: runtime.ID, Execution: map[string]any{"providerRequestId": runtime.ID, "computeAllocationId": input.ComputeID, "storageAttachmentId": input.AttachmentID}, OutputRefs: map[string]any{"redactedUrl": url}}, idempotencyKey+":receipt")
	if err != nil {
		return domain.WorkspaceProjection{}, err
	}
	status := runtime.Status
	if status == "" {
		status = "provisioning"
	}
	return domain.WorkspaceProjection{ID: input.WorkspaceID, AccountID: input.AccountID, OwnerID: input.OwnerID, Name: input.Name, PackageID: input.PackageID, Provider: "tencent-tke", URL: url, Status: status, ComputeID: input.ComputeID, VolumeID: input.VolumeID, AttachmentID: input.AttachmentID, RuntimeID: runtime.ID, RuntimeServiceName: runtime.ServiceName, RuntimeReady: runtime.Ready, RuntimeUsername: runtime.Access.Username, CredentialStatus: runtime.Access.CredentialStatus, CredentialVersion: runtime.Access.CredentialVersion, CredentialSecretRef: runtime.Access.SecretRef, ReceiptID: receipt.ReceiptID}, nil
}
