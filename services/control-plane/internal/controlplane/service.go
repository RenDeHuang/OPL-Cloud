package controlplane

import (
	"context"
	"errors"
	"fmt"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/domain"
)

type Service struct {
	ledger clients.LedgerClient
	fabric clients.FabricClient
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

type ManualTopUpInput struct {
	AccountID      string `json:"accountId"`
	AmountCents    int64  `json:"amountCents"`
	Currency       string `json:"currency"`
	OperatorUserID string `json:"operatorUserId"`
	Reason         string `json:"reason,omitempty"`
}

type ResourceSettlementInput struct {
	AccountID               string         `json:"accountId"`
	WorkspaceID             string         `json:"workspaceId"`
	ResourceType            string         `json:"resourceType"`
	ResourceID              string         `json:"resourceId"`
	AmountCents             int64          `json:"amountCents"`
	Currency                string         `json:"currency"`
	PricingVersion          string         `json:"pricingVersion,omitempty"`
	PriceSnapshot           map[string]any `json:"priceSnapshot,omitempty"`
	UsagePeriodStart        string         `json:"usagePeriodStart,omitempty"`
	UsagePeriodEnd          string         `json:"usagePeriodEnd,omitempty"`
	Quantity                float64        `json:"quantity,omitempty"`
	Unit                    string         `json:"unit,omitempty"`
	ProviderCostEvidenceRef string         `json:"providerCostEvidenceRef,omitempty"`
}

type ReconciliationInput struct {
	Report map[string]any `json:"report"`
}

type ComputeAllocationInput struct {
	ID              string `json:"id,omitempty"`
	AccountID       string `json:"accountId"`
	WorkspaceID     string `json:"workspaceId"`
	PackageID       string `json:"packageId"`
	HoldAmountCents int64  `json:"holdAmountCents"`
}

type StorageVolumeInput struct {
	ID              string `json:"id,omitempty"`
	AccountID       string `json:"accountId"`
	WorkspaceID     string `json:"workspaceId"`
	SizeGB          int    `json:"sizeGb"`
	HoldAmountCents int64  `json:"holdAmountCents"`
}

type StorageAttachmentInput struct {
	WorkspaceID string `json:"workspaceId"`
	ComputeID   string `json:"computeId"`
	VolumeID    string `json:"volumeId"`
}

type DestroyResourceInput struct {
	ID              string `json:"id"`
	AccountID       string `json:"accountId"`
	WorkspaceID     string `json:"workspaceId"`
	HoldID          string `json:"holdId"`
	HoldAmountCents int64  `json:"holdAmountCents"`
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

func NewService(ledger clients.LedgerClient, fabric clients.FabricClient) *Service {
	return &Service{ledger: ledger, fabric: fabric}
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

func (s *Service) ManualTopUp(ctx context.Context, input ManualTopUpInput, idempotencyKey string) (clients.ManualTopUpResult, error) {
	return s.ledger.ManualTopUp(ctx, clients.ManualTopUpInput{
		AccountID:      input.AccountID,
		AmountCents:    input.AmountCents,
		Currency:       input.Currency,
		OperatorUserID: input.OperatorUserID,
		Reason:         input.Reason,
	}, idempotencyKey)
}

func (s *Service) SettleResource(ctx context.Context, input ResourceSettlementInput, idempotencyKey string) (clients.ResourceSettlementResult, error) {
	return s.ledger.SettleResource(ctx, clients.ResourceSettlementInput{
		AccountID:               input.AccountID,
		WorkspaceID:             input.WorkspaceID,
		ResourceType:            input.ResourceType,
		ResourceID:              input.ResourceID,
		AmountCents:             input.AmountCents,
		Currency:                input.Currency,
		PricingVersion:          input.PricingVersion,
		PriceSnapshot:           input.PriceSnapshot,
		UsagePeriodStart:        input.UsagePeriodStart,
		UsagePeriodEnd:          input.UsagePeriodEnd,
		Quantity:                input.Quantity,
		Unit:                    input.Unit,
		ProviderCostEvidenceRef: input.ProviderCostEvidenceRef,
	}, idempotencyKey)
}

func (s *Service) RecordReconciliation(ctx context.Context, input ReconciliationInput, idempotencyKey string) (clients.ReconciliationResult, error) {
	return s.ledger.RecordReconciliation(ctx, clients.ReconciliationInput{Report: input.Report}, idempotencyKey)
}

func (s *Service) Wallet(ctx context.Context, accountID string) (clients.Wallet, error) {
	return s.ledger.Wallet(ctx, accountID)
}

func (s *Service) ListLedgerEntries(ctx context.Context, accountID string) ([]clients.LedgerEntry, error) {
	return s.ledger.ListLedgerEntries(ctx, accountID)
}

func (s *Service) ListWalletTransactions(ctx context.Context, accountID string) ([]clients.WalletTransaction, error) {
	return s.ledger.ListWalletTransactions(ctx, accountID)
}

func (s *Service) ListManualTopUps(ctx context.Context, accountID string) ([]clients.ManualTopUp, error) {
	return s.ledger.ListManualTopUps(ctx, accountID)
}

func (s *Service) ListResourceSettlements(ctx context.Context, accountID string) ([]clients.ResourceSettlementResult, error) {
	return s.ledger.ListResourceSettlements(ctx, accountID)
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

func (s *Service) CreateComputeAllocation(ctx context.Context, input ComputeAllocationInput, idempotencyKey string) (clients.ComputeAllocation, error) {
	if input.HoldAmountCents <= 0 {
		return clients.ComputeAllocation{}, fmt.Errorf("compute_hold_amount_required")
	}
	id := input.ID
	if id == "" {
		id = resourceID("ca")
	}
	hold, err := s.ledger.CreateHold(ctx, clients.HoldInput{AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: "compute", ResourceID: id, AmountCents: input.HoldAmountCents, Currency: "CNY"}, idempotencyKey+":hold")
	if err != nil {
		return clients.ComputeAllocation{}, err
	}
	allocation, err := s.fabric.CreateComputeAllocation(ctx, clients.ComputeAllocationInput{ID: id, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, PackageID: input.PackageID}, idempotencyKey)
	if err != nil {
		_, releaseErr := s.ledger.ReleaseHold(ctx, clients.HoldReleaseInput{AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: "compute", ResourceID: id, HoldID: hold.ID, AmountCents: hold.AmountCents, Currency: "CNY", Reason: "compute_create_failed"}, idempotencyKey+":hold-release")
		return clients.ComputeAllocation{}, errors.Join(err, releaseErr)
	}
	allocation.HoldID = hold.ID
	allocation.HoldAmountCents = hold.AmountCents
	allocation.Wallet = hold.Wallet
	return allocation, nil
}

func (s *Service) GetComputeAllocation(ctx context.Context, id string) (clients.ComputeAllocation, error) {
	return s.fabric.GetComputeAllocation(ctx, id)
}

func (s *Service) SyncComputeAllocation(ctx context.Context, input DestroyResourceInput, idempotencyKey string) (clients.ComputeAllocation, error) {
	allocation, err := s.fabric.SyncComputeAllocation(ctx, input.ID)
	if err != nil {
		return allocation, err
	}
	if isExternallyDeletedResource(allocation.Status) && input.HoldID != "" && input.HoldAmountCents > 0 {
		release, err := s.ledger.ReleaseHold(ctx, clients.HoldReleaseInput{AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: "compute", ResourceID: input.ID, HoldID: input.HoldID, AmountCents: input.HoldAmountCents, Currency: "CNY", Reason: "provider_external_deleted"}, idempotencyKey+":hold-release")
		if err != nil {
			return allocation, err
		}
		allocation.HoldID = input.HoldID
		allocation.HoldAmountCents = input.HoldAmountCents
		allocation.HoldReleaseID = release.ID
		allocation.Wallet = release.Wallet
		allocation.BillingStatus = "stopped"
	}
	return allocation, nil
}

func (s *Service) DestroyComputeAllocation(ctx context.Context, input DestroyResourceInput, idempotencyKey string) (clients.ComputeAllocation, error) {
	allocation, err := s.fabric.DestroyComputeAllocation(ctx, input.ID, idempotencyKey)
	if err != nil {
		return allocation, err
	}
	if input.HoldID != "" && input.HoldAmountCents > 0 {
		release, err := s.ledger.ReleaseHold(ctx, clients.HoldReleaseInput{AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: "compute", ResourceID: input.ID, HoldID: input.HoldID, AmountCents: input.HoldAmountCents, Currency: "CNY", Reason: "destroy_compute"}, idempotencyKey+":hold-release")
		if err != nil {
			return allocation, err
		}
		allocation.HoldID = input.HoldID
		allocation.HoldAmountCents = input.HoldAmountCents
		allocation.HoldReleaseID = release.ID
		allocation.Wallet = release.Wallet
	}
	return allocation, nil
}

func isExternallyDeletedResource(status string) bool {
	return status == "external_deleted" || status == "deleted" || status == "missing"
}

func (s *Service) CreateStorageVolume(ctx context.Context, input StorageVolumeInput, idempotencyKey string) (clients.StorageVolume, error) {
	if input.HoldAmountCents <= 0 {
		return clients.StorageVolume{}, fmt.Errorf("storage_hold_amount_required")
	}
	id := input.ID
	if id == "" {
		id = resourceID("vol")
	}
	hold, err := s.ledger.CreateHold(ctx, clients.HoldInput{AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: "storage", ResourceID: id, AmountCents: input.HoldAmountCents, Currency: "CNY"}, idempotencyKey+":hold")
	if err != nil {
		return clients.StorageVolume{}, err
	}
	volume, err := s.fabric.CreateStorageVolume(ctx, clients.StorageVolumeInput{ID: id, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, SizeGB: input.SizeGB}, idempotencyKey)
	if err != nil {
		_, releaseErr := s.ledger.ReleaseHold(ctx, clients.HoldReleaseInput{AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: "storage", ResourceID: id, HoldID: hold.ID, AmountCents: hold.AmountCents, Currency: "CNY", Reason: "storage_create_failed"}, idempotencyKey+":hold-release")
		return clients.StorageVolume{}, errors.Join(err, releaseErr)
	}
	volume.HoldID = hold.ID
	volume.HoldAmountCents = hold.AmountCents
	volume.Wallet = hold.Wallet
	return volume, nil
}

func (s *Service) SyncStorageVolume(ctx context.Context, input DestroyResourceInput, idempotencyKey string) (clients.StorageVolume, error) {
	volume, err := s.fabric.SyncStorageVolume(ctx, input.ID)
	if err != nil {
		return volume, err
	}
	if isExternallyDeletedResource(volume.Status) && input.HoldID != "" && input.HoldAmountCents > 0 {
		release, err := s.ledger.ReleaseHold(ctx, clients.HoldReleaseInput{AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: "storage", ResourceID: input.ID, HoldID: input.HoldID, AmountCents: input.HoldAmountCents, Currency: "CNY", Reason: "provider_external_deleted"}, idempotencyKey+":hold-release")
		if err != nil {
			return volume, err
		}
		volume.HoldID = input.HoldID
		volume.HoldAmountCents = input.HoldAmountCents
		volume.HoldReleaseID = release.ID
		volume.Wallet = release.Wallet
		volume.BillingStatus = "stopped"
	}
	return volume, nil
}

func (s *Service) DestroyStorageVolume(ctx context.Context, input DestroyResourceInput, idempotencyKey string) (clients.StorageVolume, error) {
	volume, err := s.fabric.DestroyStorageVolume(ctx, input.ID, idempotencyKey)
	if err != nil {
		return volume, err
	}
	if input.HoldID != "" && input.HoldAmountCents > 0 {
		release, err := s.ledger.ReleaseHold(ctx, clients.HoldReleaseInput{AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: "storage", ResourceID: input.ID, HoldID: input.HoldID, AmountCents: input.HoldAmountCents, Currency: "CNY", Reason: "destroy_storage"}, idempotencyKey+":hold-release")
		if err != nil {
			return volume, err
		}
		volume.HoldID = input.HoldID
		volume.HoldAmountCents = input.HoldAmountCents
		volume.HoldReleaseID = release.ID
		volume.Wallet = release.Wallet
	}
	return volume, nil
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
	receipt, err := s.ledger.RecordReceipt(ctx, clients.ReceiptInput{Type: "workspace.created", Status: "completed", Surface: "workspace", WorkspaceID: workspaceID, JobID: runtime.ID, Execution: map[string]any{"providerRequestId": runtime.ID}, OutputRefs: map[string]any{"redactedUrl": runtime.URL}, Continuation: map[string]any{"action": "open_workspace_url", "tokenVersion": "v1", "redactedUrl": runtime.URL}}, idempotencyKey+":receipt")
	if err != nil {
		return domain.WorkspaceProjection{}, err
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
		RuntimePassword:     runtime.Access.Password,
		CredentialStatus:    runtime.Access.CredentialStatus,
		CredentialVersion:   runtime.Access.CredentialVersion,
		CredentialSecretRef: runtime.Access.SecretRef,
		ReceiptID:           receipt.ReceiptID,
	}, nil
}

func resourceID(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UTC().UnixNano())
}
