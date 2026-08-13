package fabric

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"time"
)

func (s *Service) CreateJob(ctx context.Context, input JobInput) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if input.OrganizationID == "" || input.WorkspaceID == "" || input.ProjectID == "" || input.TaskID == "" || input.RequestID == "" || input.ApprovalID == "" || input.IdempotencyKey == "" {
		return Job{}, ErrInvalidJobInput
	}
	requestHash := hashInput(input)
	operations, err := s.operations.List(ctx)
	if err != nil {
		return Job{}, err
	}
	// ponytail: linear scan is enough for the initial job volume; add an indexed store query when measured throughput requires it.
	for _, operation := range operations {
		if operation.ResourceKind != "job" || operation.Action != "create_job" || operation.IdempotencyKey != input.IdempotencyKey {
			continue
		}
		if operation.RequestHash != requestHash {
			return Job{}, ErrJobIdempotencyConflict
		}
		var job Job
		if decodeOperationResource(operation, &job) {
			job.Replayed = true
			return job, nil
		}
	}
	now := s.now()
	job := Job{
		JobID:          "job-" + stableSuffix(input.IdempotencyKey, input.RequestID, input.TaskID)[:16],
		OrganizationID: input.OrganizationID,
		WorkspaceID:    input.WorkspaceID,
		ProjectID:      input.ProjectID,
		TaskID:         input.TaskID,
		RequestID:      input.RequestID,
		ApprovalID:     input.ApprovalID,
		EnvironmentRef: input.EnvironmentRef,
		Status:         "queued",
		Attempt:        1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	operation := newOperation("create_job", "job", job.JobID, "", input.WorkspaceID, input.IdempotencyKey, requestHash, now)
	operation.ProviderRequestID = job.JobID
	if err := s.recordOperation(ctx, operation, job.Status, job, nil); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) Job(ctx context.Context, jobID string) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	return s.jobLocked(ctx, jobID, true)
}

func (s *Service) jobLocked(ctx context.Context, jobID string, expire bool) (Job, error) {
	operations, err := s.operations.List(ctx)
	if err != nil {
		return Job{}, err
	}
	var job Job
	leaseTokenHash := ""
	found := false
	for _, operation := range operations {
		if operation.ResourceKind == "job" && operation.ResourceID == jobID && decodeOperationResource(operation, &job) {
			found = true
			leaseTokenHash, _ = operation.RedactedProviderPayload["leaseTokenHash"].(string)
		}
	}
	if !found {
		return Job{}, ErrJobNotFound
	}
	job.leaseTokenHash = leaseTokenHash
	if expire && job.Status == "running" && job.LeaseExpiresAt != nil && !s.now().Before(*job.LeaseExpiresAt) {
		job.Status = "timed_out"
		job.ErrorCode = "lease_expired"
		job.UpdatedAt = s.now()
		if err := s.appendJobTransition(ctx, "timeout_job", "timeout-"+job.JobID+fmt.Sprintf("-%d", job.Attempt), hashInput(map[string]any{"jobId": job.JobID, "attempt": job.Attempt}), job, "runner"); err != nil {
			return Job{}, err
		}
	}
	return job, nil
}

func (s *Service) CancelJob(ctx context.Context, jobID string, idempotencyKey string) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if idempotencyKey == "" {
		return Job{}, ErrInvalidJobInput
	}
	requestHash := hashInput(map[string]string{"jobId": jobID})
	if replayed, ok, err := s.replayedJobTransition(ctx, "cancel_job", jobID, idempotencyKey, requestHash); ok || err != nil {
		return replayed, err
	}
	job, err := s.jobLocked(ctx, jobID, true)
	if err != nil {
		return Job{}, err
	}
	if job.Status == "cancelled" {
		job.Replayed = true
		return job, nil
	}
	if job.Status != "queued" && job.Status != "running" {
		return Job{}, ErrJobStateConflict
	}
	now := s.now()
	job.Status = "cancelled"
	job.UpdatedAt = now
	if err := s.appendJobTransition(ctx, "cancel_job", idempotencyKey, requestHash, job, "control-plane"); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) ClaimJob(ctx context.Context, jobID string, input JobClaimInput) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if jobID == "" || input.RunnerID == "" || input.IdempotencyKey == "" {
		return Job{}, ErrInvalidJobInput
	}
	requestHash := hashInput(map[string]string{"jobId": jobID, "runnerId": input.RunnerID})
	if replayed, ok, err := s.replayedJobTransition(ctx, "claim_job", jobID, input.IdempotencyKey, requestHash); ok || err != nil {
		return replayed, err
	}
	job, err := s.jobLocked(ctx, jobID, true)
	if err != nil {
		return Job{}, err
	}
	if job.Status != "queued" {
		return Job{}, ErrJobStateConflict
	}
	now := s.now()
	token, err := newLeaseToken()
	if err != nil {
		return Job{}, err
	}
	expiresAt := now.Add(30 * time.Second)
	job.Status = "running"
	job.LeaseOwner = input.RunnerID
	job.LeaseExpiresAt = &expiresAt
	job.LeaseToken = token
	job.leaseTokenHash = stableSuffix(token)
	job.UpdatedAt = now
	if err := s.appendJobTransition(ctx, "claim_job", input.IdempotencyKey, requestHash, job, "runner"); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) HeartbeatJob(ctx context.Context, jobID string, input JobHeartbeatInput) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if jobID == "" || input.RunnerID == "" || input.LeaseToken == "" || input.IdempotencyKey == "" {
		return Job{}, ErrInvalidJobInput
	}
	requestHash := hashInput(map[string]string{"jobId": jobID, "runnerId": input.RunnerID, "leaseTokenHash": stableSuffix(input.LeaseToken)})
	if replayed, ok, err := s.replayedJobTransition(ctx, "heartbeat_job", jobID, input.IdempotencyKey, requestHash); ok || err != nil {
		return replayed, err
	}
	job, err := s.activeLeasedJob(ctx, jobID, input.RunnerID, input.LeaseToken)
	if err != nil {
		return Job{}, err
	}
	now := s.now()
	expiresAt := now.Add(30 * time.Second)
	job.LeaseExpiresAt = &expiresAt
	job.UpdatedAt = now
	if err := s.appendJobTransition(ctx, "heartbeat_job", input.IdempotencyKey, requestHash, job, "runner"); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) CompleteJob(ctx context.Context, jobID string, input JobCompleteInput) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if jobID == "" || input.RunnerID == "" || input.LeaseToken == "" || len(input.ArtifactIDs) == 0 || len(input.ReviewIDs) == 0 || input.IdempotencyKey == "" {
		return Job{}, ErrInvalidJobInput
	}
	requestHash := hashInput(struct {
		JobID, RunnerID, LeaseTokenHash string
		ArtifactIDs, ReviewIDs          []string
	}{jobID, input.RunnerID, stableSuffix(input.LeaseToken), input.ArtifactIDs, input.ReviewIDs})
	if replayed, ok, err := s.replayedJobTransition(ctx, "complete_job", jobID, input.IdempotencyKey, requestHash); ok || err != nil {
		return replayed, err
	}
	job, err := s.activeLeasedJob(ctx, jobID, input.RunnerID, input.LeaseToken)
	if err != nil {
		return Job{}, err
	}
	job.Status = "succeeded"
	job.ArtifactIDs = append([]string(nil), input.ArtifactIDs...)
	job.ReviewIDs = append([]string(nil), input.ReviewIDs...)
	job.ErrorCode = ""
	job.UpdatedAt = s.now()
	if err := s.appendJobTransition(ctx, "complete_job", input.IdempotencyKey, requestHash, job, "runner"); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) FailJob(ctx context.Context, jobID string, input JobFailInput) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if jobID == "" || input.RunnerID == "" || input.LeaseToken == "" || input.ErrorCode == "" || input.IdempotencyKey == "" {
		return Job{}, ErrInvalidJobInput
	}
	requestHash := hashInput(map[string]string{"jobId": jobID, "runnerId": input.RunnerID, "leaseTokenHash": stableSuffix(input.LeaseToken), "errorCode": input.ErrorCode})
	if replayed, ok, err := s.replayedJobTransition(ctx, "fail_job", jobID, input.IdempotencyKey, requestHash); ok || err != nil {
		return replayed, err
	}
	job, err := s.activeLeasedJob(ctx, jobID, input.RunnerID, input.LeaseToken)
	if err != nil {
		return Job{}, err
	}
	job.Status = "failed"
	job.ErrorCode = input.ErrorCode
	job.UpdatedAt = s.now()
	if err := s.appendJobTransition(ctx, "fail_job", input.IdempotencyKey, requestHash, job, "runner"); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) RetryJob(ctx context.Context, jobID, idempotencyKey string) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if jobID == "" || idempotencyKey == "" {
		return Job{}, ErrInvalidJobInput
	}
	requestHash := hashInput(map[string]string{"jobId": jobID})
	if replayed, ok, err := s.replayedJobTransition(ctx, "retry_job", jobID, idempotencyKey, requestHash); ok || err != nil {
		return replayed, err
	}
	job, err := s.jobLocked(ctx, jobID, true)
	if err != nil {
		return Job{}, err
	}
	if job.Status != "failed" && job.Status != "timed_out" {
		return Job{}, ErrJobStateConflict
	}
	job.Status = "queued"
	job.Attempt++
	job.LeaseOwner = ""
	job.LeaseExpiresAt = nil
	job.LeaseToken = ""
	job.leaseTokenHash = ""
	job.ArtifactIDs = nil
	job.ReviewIDs = nil
	job.ErrorCode = ""
	job.UpdatedAt = s.now()
	if err := s.appendJobTransition(ctx, "retry_job", idempotencyKey, requestHash, job, "control-plane"); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) activeLeasedJob(ctx context.Context, jobID, runnerID, leaseToken string) (Job, error) {
	job, err := s.jobLocked(ctx, jobID, true)
	if err != nil {
		return Job{}, err
	}
	if job.Status != "running" {
		return Job{}, ErrJobStateConflict
	}
	if job.LeaseOwner != runnerID || subtle.ConstantTimeCompare([]byte(job.leaseTokenHash), []byte(stableSuffix(leaseToken))) != 1 {
		return Job{}, ErrJobLeaseMismatch
	}
	return job, nil
}

func (s *Service) replayedJobTransition(ctx context.Context, action, jobID, idempotencyKey, requestHash string) (Job, bool, error) {
	operations, err := s.operations.List(ctx)
	if err != nil {
		return Job{}, false, err
	}
	for _, operation := range operations {
		if operation.ResourceKind != "job" || operation.ResourceID != jobID || operation.Action != action || operation.IdempotencyKey != idempotencyKey {
			continue
		}
		if operation.RequestHash != requestHash {
			return Job{}, false, ErrJobIdempotencyConflict
		}
		var job Job
		if decodeOperationResource(operation, &job) {
			job.Replayed = true
			return job, true, nil
		}
	}
	return Job{}, false, nil
}

func (s *Service) appendJobTransition(ctx context.Context, action, idempotencyKey, requestHash string, job Job, caller string) error {
	operation := newOperation(action, "job", job.JobID, "", job.WorkspaceID, idempotencyKey, requestHash, s.now())
	operation.ProviderRequestID = job.JobID
	operation.CallerService = caller
	return s.recordOperation(ctx, operation, job.Status, job, nil)
}

func newLeaseToken() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return "lease-" + hex.EncodeToString(data), nil
}
