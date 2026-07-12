package fabric

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestContentTransferResumesAndPublishesOnlyVerifiedBytes(t *testing.T) {
	const chunkSize = 4 << 20
	body := bytes.Repeat([]byte("x"), 2*chunkSize+17)
	digest := fmt.Sprintf("%x", sha256.Sum256(body))
	provider := &contentTestProvider{}
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(provider, store)
	ctx := context.Background()

	transfer, err := service.CreateTransfer(ctx, TransferInput{
		OrganizationID: "org-alpha", WorkspaceID: "workspace-alpha", ProjectID: "project-alpha",
		Path: "inputs/paper.txt", Digest: digest, Size: int64(len(body)),
		IdempotencyKey: "transfer-once",
	})
	if err != nil {
		t.Fatalf("create transfer: %v", err)
	}
	if _, err := service.PutTransferChunk(ctx, transfer.TransferID, 1, body[chunkSize:2*chunkSize], fmt.Sprintf("%x", sha256.Sum256(body[chunkSize:2*chunkSize]))); err != nil {
		t.Fatalf("put middle chunk: %v", err)
	}

	restarted := NewServiceWithOperationStore(provider, store)
	resumed, err := restarted.Transfer(ctx, transfer.TransferID)
	if err != nil || len(resumed.ReceivedChunks) != 1 || resumed.ReceivedChunks[0] != 1 {
		t.Fatalf("resumed transfer = %#v err=%v", resumed, err)
	}
	changed := bytes.Repeat([]byte("y"), chunkSize)
	if _, err := restarted.PutTransferChunk(ctx, transfer.TransferID, 1, changed, fmt.Sprintf("%x", sha256.Sum256(changed))); !errors.Is(err, ErrTransferChunkConflict) {
		t.Fatalf("changed replay error = %v, want chunk conflict", err)
	}
	if _, err := restarted.CompleteTransfer(ctx, transfer.TransferID); !errors.Is(err, ErrTransferIncomplete) {
		t.Fatalf("incomplete error = %v", err)
	}
	for index, chunk := range [][]byte{body[:chunkSize], body[2*chunkSize:]} {
		actualIndex := index
		if index == 1 {
			actualIndex = 2
		}
		if _, err := restarted.PutTransferChunk(ctx, transfer.TransferID, actualIndex, chunk, fmt.Sprintf("%x", sha256.Sum256(chunk))); err != nil {
			t.Fatalf("put chunk %d: %v", actualIndex, err)
		}
	}
	completed, err := restarted.CompleteTransfer(ctx, transfer.TransferID)
	if err != nil || completed.Status != "completed" {
		t.Fatalf("complete transfer = %#v err=%v", completed, err)
	}
	if string(provider.published) != string(body) || provider.path != "inputs/paper.txt" {
		t.Fatalf("published path=%q body=%q", provider.path, provider.published)
	}
	downloaded, err := restarted.Content(ctx, "workspace-alpha", digest)
	if err != nil || string(downloaded.Body) != string(body) {
		t.Fatalf("downloaded = %#v err=%v", downloaded, err)
	}
}

func TestJobLifecycleUsesDurableOperationStore(t *testing.T) {
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(testProvider{}, store)
	ctx := context.Background()
	input := JobInput{
		OrganizationID: "org-alpha",
		WorkspaceID:    "workspace-alpha",
		ProjectID:      "project-alpha",
		TaskID:         "task-alpha",
		RequestID:      "request-alpha",
		ApprovalID:     "approval-alpha",
		EnvironmentRef: "environment-alpha",
		IdempotencyKey: "job-once",
	}

	created, err := service.CreateJob(ctx, input)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if created.JobID == "" || created.Status != "queued" || created.RequestID != "request-alpha" {
		t.Fatalf("unexpected created job: %#v", created)
	}
	replayed, err := service.CreateJob(ctx, input)
	if err != nil {
		t.Fatalf("replay job: %v", err)
	}
	if !replayed.Replayed || replayed.JobID != created.JobID {
		t.Fatalf("unexpected replayed job: %#v", replayed)
	}

	restarted := NewServiceWithOperationStore(testProvider{}, store)
	queried, err := restarted.Job(ctx, created.JobID)
	if err != nil || queried.Status != "queued" {
		t.Fatalf("query durable job: %#v, %v", queried, err)
	}
	cancelled, err := restarted.CancelJob(ctx, created.JobID, "cancel-once")
	if err != nil || cancelled.Status != "cancelled" {
		t.Fatalf("cancel job: %#v, %v", cancelled, err)
	}
	queried, err = restarted.Job(ctx, created.JobID)
	if err != nil || queried.Status != "cancelled" {
		t.Fatalf("query cancelled job: %#v, %v", queried, err)
	}

	input.EnvironmentRef = "environment-beta"
	if _, err := restarted.CreateJob(ctx, input); !errors.Is(err, ErrJobIdempotencyConflict) {
		t.Fatalf("idempotency conflict = %v, want ErrJobIdempotencyConflict", err)
	}
}

func TestRunnerCompletesJobAcrossServiceRestart(t *testing.T) {
	store := NewMemoryOperationStore()
	now := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)
	service := NewServiceWithOperationStore(testProvider{}, store)
	service.now = func() time.Time { return now }
	created, err := service.CreateJob(context.Background(), JobInput{OrganizationID: "org-alpha", WorkspaceID: "workspace-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", RequestID: "request-alpha", ApprovalID: "approval-alpha", IdempotencyKey: "runner-job"})
	if err != nil || created.Attempt != 1 {
		t.Fatalf("create job: %#v, %v", created, err)
	}
	claimed, err := service.ClaimJob(context.Background(), created.JobID, JobClaimInput{RunnerID: "runner-alpha", IdempotencyKey: "claim-once"})
	if err != nil || claimed.Status != "running" || claimed.LeaseToken == "" || claimed.LeaseOwner != "runner-alpha" || claimed.LeaseExpiresAt == nil {
		t.Fatalf("claim job: %#v, %v", claimed, err)
	}

	restarted := NewServiceWithOperationStore(testProvider{}, store)
	restarted.now = func() time.Time { return now.Add(10 * time.Second) }
	heartbeat, err := restarted.HeartbeatJob(context.Background(), created.JobID, JobHeartbeatInput{RunnerID: "runner-alpha", LeaseToken: claimed.LeaseToken, IdempotencyKey: "heartbeat-once"})
	if err != nil || heartbeat.Status != "running" || !heartbeat.LeaseExpiresAt.After(*claimed.LeaseExpiresAt) {
		t.Fatalf("heartbeat job: %#v, %v", heartbeat, err)
	}
	completed, err := restarted.CompleteJob(context.Background(), created.JobID, JobCompleteInput{RunnerID: "runner-alpha", LeaseToken: claimed.LeaseToken, ArtifactIDs: []string{"artifact-alpha"}, ReviewIDs: []string{"review-alpha"}, IdempotencyKey: "complete-once"})
	if err != nil || completed.Status != "succeeded" || len(completed.ArtifactIDs) != 1 || len(completed.ReviewIDs) != 1 {
		t.Fatalf("complete job: %#v, %v", completed, err)
	}
	loaded, err := NewServiceWithOperationStore(testProvider{}, store).Job(context.Background(), created.JobID)
	if err != nil || loaded.Status != "succeeded" {
		t.Fatalf("load completed job: %#v, %v", loaded, err)
	}
	operations, _ := store.List(context.Background())
	payload, _ := json.Marshal(operations)
	if strings.Contains(string(payload), claimed.LeaseToken) {
		t.Fatalf("operation log leaked lease token")
	}
	operationIDs := map[string]bool{}
	for _, operation := range operations {
		if operationIDs[operation.ID] {
			t.Fatalf("duplicate operation id %q", operation.ID)
		}
		operationIDs[operation.ID] = true
	}
}

func TestRunnerLeaseMismatchAndEvidenceValidation(t *testing.T) {
	service := NewService(testProvider{})
	created, err := service.CreateJob(context.Background(), JobInput{OrganizationID: "org-alpha", WorkspaceID: "workspace-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", RequestID: "request-alpha", ApprovalID: "approval-alpha", IdempotencyKey: "lease-job"})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	claimed, err := service.ClaimJob(context.Background(), created.JobID, JobClaimInput{RunnerID: "runner-alpha", IdempotencyKey: "lease-claim"})
	if err != nil {
		t.Fatalf("claim job: %v", err)
	}
	if _, err := service.HeartbeatJob(context.Background(), created.JobID, JobHeartbeatInput{RunnerID: "runner-beta", LeaseToken: claimed.LeaseToken, IdempotencyKey: "wrong-owner"}); !errors.Is(err, ErrJobLeaseMismatch) {
		t.Fatalf("owner mismatch = %v, want ErrJobLeaseMismatch", err)
	}
	if _, err := service.CompleteJob(context.Background(), created.JobID, JobCompleteInput{RunnerID: "runner-alpha", LeaseToken: "wrong-token", ArtifactIDs: []string{"artifact-alpha"}, ReviewIDs: []string{"review-alpha"}, IdempotencyKey: "wrong-token"}); !errors.Is(err, ErrJobLeaseMismatch) {
		t.Fatalf("token mismatch = %v, want ErrJobLeaseMismatch", err)
	}
	if _, err := service.CompleteJob(context.Background(), created.JobID, JobCompleteInput{RunnerID: "runner-alpha", LeaseToken: claimed.LeaseToken, IdempotencyKey: "missing-evidence"}); !errors.Is(err, ErrInvalidJobInput) {
		t.Fatalf("missing evidence = %v, want ErrInvalidJobInput", err)
	}
}

func TestExpiredJobCanRetryAndFail(t *testing.T) {
	now := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)
	service := NewService(testProvider{})
	service.now = func() time.Time { return now }
	created, _ := service.CreateJob(context.Background(), JobInput{OrganizationID: "org-alpha", WorkspaceID: "workspace-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", RequestID: "request-alpha", ApprovalID: "approval-alpha", IdempotencyKey: "retry-job"})
	claimed, err := service.ClaimJob(context.Background(), created.JobID, JobClaimInput{RunnerID: "runner-alpha", IdempotencyKey: "retry-claim"})
	if err != nil {
		t.Fatalf("claim job: %v", err)
	}
	service.now = func() time.Time { return now.Add(31 * time.Second) }
	timedOut, err := service.Job(context.Background(), created.JobID)
	if err != nil || timedOut.Status != "timed_out" {
		t.Fatalf("timeout job: %#v, %v", timedOut, err)
	}
	retried, err := service.RetryJob(context.Background(), created.JobID, "retry-once")
	if err != nil || retried.Status != "queued" || retried.Attempt != 2 || retried.LeaseOwner != "" {
		t.Fatalf("retry job: %#v, %v", retried, err)
	}
	claimed, err = service.ClaimJob(context.Background(), created.JobID, JobClaimInput{RunnerID: "runner-alpha", IdempotencyKey: "retry-claim-2"})
	if err != nil {
		t.Fatalf("claim retry: %v", err)
	}
	failed, err := service.FailJob(context.Background(), created.JobID, JobFailInput{RunnerID: "runner-alpha", LeaseToken: claimed.LeaseToken, ErrorCode: "runner_failed", IdempotencyKey: "fail-once"})
	if err != nil || failed.Status != "failed" || failed.ErrorCode != "runner_failed" {
		t.Fatalf("fail job: %#v, %v", failed, err)
	}
}

func TestCatalogExposesWorkspacePackages(t *testing.T) {
	service := NewService(testProvider{})
	catalog := service.Catalog(context.Background())

	if len(catalog.WorkspacePackages) == 0 {
		t.Fatalf("expected workspace packages")
	}
	if catalog.WorkspacePackages[0].ID != "basic" {
		t.Fatalf("first package = %q, want basic", catalog.WorkspacePackages[0].ID)
	}
}

func TestDryRunComputeAllocationRecordsProviderRequestIDWithoutLedgerTypes(t *testing.T) {
	service := NewService(testProvider{})
	allocation, err := service.CreateComputeAllocation(context.Background(), ComputeAllocationInput{
		AccountID:      "acct-alpha",
		WorkspaceID:    "ws-alpha",
		PackageID:      "basic",
		IdempotencyKey: "fabric-compute-once",
		DryRun:         true,
	})
	if err != nil {
		t.Fatalf("create allocation: %v", err)
	}
	if allocation.ProviderRequestID == "" {
		t.Fatalf("expected provider request id")
	}
	if strings.Contains(strings.ToLower(allocation.ProviderRequestID), "ledger") {
		t.Fatalf("provider request id must not reference ledger: %s", allocation.ProviderRequestID)
	}
}

func TestComputeAllocationReturnsProvisioningBeforeProviderCompletes(t *testing.T) {
	provider := &blockingProvider{done: make(chan struct{})}
	service := NewService(provider)

	allocation, err := service.CreateComputeAllocation(context.Background(), ComputeAllocationInput{
		AccountID:      "acct-alpha",
		WorkspaceID:    "ws-alpha",
		PackageID:      "basic",
		IdempotencyKey: "compute-once",
	})
	if err != nil {
		t.Fatalf("create allocation: %v", err)
	}
	if allocation.Status != "provisioning" || allocation.ID == "" {
		t.Fatalf("initial allocation = %#v, want provisioning with id", allocation)
	}
	current, ok := service.GetComputeAllocation(context.Background(), allocation.ID)
	if !ok || current.Status != "provisioning" {
		t.Fatalf("stored allocation = %#v ok=%v, want provisioning", current, ok)
	}

	close(provider.done)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		current, ok = service.GetComputeAllocation(context.Background(), allocation.ID)
		if ok && current.Status == "running" {
			if current.ID != allocation.ID || current.NodeName != "node-alpha" {
				t.Fatalf("completed allocation lost identity: %#v", current)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("allocation did not become running: %#v", current)
}

func TestResourceMutationsAppendFabricOperationFacts(t *testing.T) {
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(testProvider{}, store)
	ctx := context.Background()

	compute, err := service.CreateComputeAllocation(ctx, ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", IdempotencyKey: "ops-compute"})
	if err != nil {
		t.Fatalf("create compute: %v", err)
	}
	waitForOperation(t, service, "create_compute_allocation", "compute_allocation", compute.ID, "succeeded")

	volume, err := service.CreateStorageVolume(ctx, StorageVolumeInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", SizeGB: 10, IdempotencyKey: "ops-storage"})
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	attachment, err := service.CreateStorageAttachment(ctx, StorageAttachmentInput{WorkspaceID: "ws-alpha", ComputeID: compute.ID, VolumeID: volume.ID, IdempotencyKey: "ops-attach"})
	if err != nil {
		t.Fatalf("attach storage: %v", err)
	}
	runtime, err := service.CreateWorkspaceRuntime(ctx, WorkspaceRuntimeInput{WorkspaceID: "ws-alpha", ComputeID: compute.ID, VolumeID: volume.ID, ImageID: "one-person-lab-app", IdempotencyKey: "ops-runtime"})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	if _, err := service.DetachStorageAttachment(ctx, attachment.ID); err != nil {
		t.Fatalf("detach storage: %v", err)
	}
	if _, err := service.DestroyStorageVolume(ctx, volume.ID); err != nil {
		t.Fatalf("destroy storage: %v", err)
	}
	if _, err := service.DestroyComputeAllocation(ctx, compute.ID); err != nil {
		t.Fatalf("destroy compute: %v", err)
	}

	operations, err := service.ListOperations(ctx)
	if err != nil {
		t.Fatalf("list operations: %v", err)
	}
	for _, expected := range []struct {
		action       string
		resourceKind string
		resourceID   string
		status       string
	}{
		{"create_storage_volume", "storage_volume", volume.ID, "succeeded"},
		{"create_storage_attachment", "storage_attachment", attachment.ID, "succeeded"},
		{"create_workspace_runtime", "workspace_runtime", runtime.WorkspaceID, "succeeded"},
		{"detach_storage_attachment", "storage_attachment", attachment.ID, "succeeded"},
		{"destroy_storage_volume", "storage_volume", volume.ID, "succeeded"},
		{"destroy_compute_allocation", "compute_allocation", compute.ID, "succeeded"},
	} {
		assertOperationFact(t, operations, expected.action, expected.resourceKind, expected.resourceID, expected.status)
	}
}

func TestStorageSnapshotRestorePersistsAndKeepsSourceVolume(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(testProvider{}, store)
	volume, err := service.CreateStorageVolume(ctx, StorageVolumeInput{ID: "vol-source", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", SizeGB: 10, IdempotencyKey: "source-volume"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.CreateStorageSnapshot(ctx, StorageSnapshotInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", VolumeID: volume.ID, IdempotencyKey: "snapshot-once"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != "ready" || snapshot.VolumeID != volume.ID {
		t.Fatalf("snapshot = %#v", snapshot)
	}

	restarted := NewServiceWithOperationStore(testProvider{}, store)
	persisted, ok := restarted.GetStorageSnapshot(ctx, snapshot.ID)
	if !ok || persisted.ProviderSnapshotRef == "" {
		t.Fatalf("persisted snapshot = %#v, ok=%v", persisted, ok)
	}
	replayed, err := restarted.CreateStorageSnapshot(ctx, StorageSnapshotInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", VolumeID: volume.ID, IdempotencyKey: "snapshot-once"})
	if err != nil || replayed.ID != snapshot.ID {
		t.Fatalf("replayed snapshot = %#v, err=%v", replayed, err)
	}
	restored, err := restarted.RestoreStorageSnapshot(ctx, StorageRestoreInput{SnapshotID: snapshot.ID, AccountID: "acct-alpha", WorkspaceID: "ws-restored", TargetVolumeID: "vol-restored", IdempotencyKey: "restore-once"})
	if err != nil {
		t.Fatal(err)
	}
	if restored.ID != "vol-restored" || restored.WorkspaceID != "ws-restored" || restored.SizeGB != 10 {
		t.Fatalf("restored volume = %#v", restored)
	}
	if source, ok := restarted.GetStorageVolume(ctx, volume.ID); !ok || source.Status != "ready" {
		t.Fatalf("source volume changed: %#v, ok=%v", source, ok)
	}
	destroyed, err := restarted.DestroyStorageSnapshot(ctx, snapshot.ID)
	if err != nil || destroyed.Status != "destroyed" {
		t.Fatalf("destroyed snapshot = %#v, err=%v", destroyed, err)
	}
}

func TestWorkspaceRuntimeCreationDoesNotReturnCredential(t *testing.T) {
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(testProvider{}, store)
	ctx := context.Background()

	compute, err := service.CreateComputeAllocation(ctx, ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", IdempotencyKey: "access-compute"})
	if err != nil {
		t.Fatalf("create compute: %v", err)
	}
	waitForOperation(t, service, "create_compute_allocation", "compute_allocation", compute.ID, "succeeded")
	volume, err := service.CreateStorageVolume(ctx, StorageVolumeInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", SizeGB: 10, IdempotencyKey: "access-storage"})
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	runtime, err := service.CreateWorkspaceRuntime(ctx, WorkspaceRuntimeInput{WorkspaceID: "ws-alpha", ComputeID: compute.ID, VolumeID: volume.ID, ImageID: "one-person-lab-app", IdempotencyKey: "access-runtime"})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	if runtime.Access.Password != "" || runtime.Access.CredentialStatus != "configured" || runtime.Access.SecretRef != "opl-ca-test-env" {
		t.Fatalf("runtime creation must return credential metadata only: %#v", runtime.Access)
	}

	operations, err := service.ListOperations(ctx)
	if err != nil {
		t.Fatalf("list operations: %v", err)
	}
	for _, operation := range operations {
		payload := operation.RedactedProviderPayload
		if strings.Contains(strings.ToLower(fmt.Sprint(payload)), "runtime-password-alpha") {
			t.Fatalf("fabric operation leaked workspace password: %#v", operation)
		}
	}
}

func TestFabricRejectsIllegalResourceMutationsWithOperationFacts(t *testing.T) {
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(testProvider{}, store)
	ctx := context.Background()

	if _, err := service.DestroyComputeAllocation(ctx, "missing-compute"); err == nil {
		t.Fatalf("destroy missing compute must fail")
	}
	if _, err := service.CreateStorageAttachment(ctx, StorageAttachmentInput{WorkspaceID: "ws-alpha", ComputeID: "missing-compute", VolumeID: "missing-volume", IdempotencyKey: "reject-missing-attach"}); err == nil {
		t.Fatalf("attach missing compute/storage must fail")
	}

	compute, err := service.CreateComputeAllocation(ctx, ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", IdempotencyKey: "reject-compute"})
	if err != nil {
		t.Fatalf("create compute: %v", err)
	}
	waitForOperation(t, service, "create_compute_allocation", "compute_allocation", compute.ID, "succeeded")
	volume, err := service.CreateStorageVolume(ctx, StorageVolumeInput{AccountID: "acct-beta", WorkspaceID: "ws-beta", SizeGB: 10, IdempotencyKey: "reject-storage"})
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	if _, err := service.CreateStorageAttachment(ctx, StorageAttachmentInput{WorkspaceID: "ws-alpha", ComputeID: compute.ID, VolumeID: volume.ID, IdempotencyKey: "reject-cross-account-attach"}); err == nil {
		t.Fatalf("attach cross-account compute/storage must fail")
	}

	operations, err := service.ListOperations(ctx)
	if err != nil {
		t.Fatalf("list operations: %v", err)
	}
	assertOperationFact(t, operations, "destroy_compute_allocation", "compute_allocation", "missing-compute", "rejected")
	assertOperationFact(t, operations, "create_storage_attachment", "storage_attachment", "reject-missing-attach", "rejected")
	assertOperationFact(t, operations, "create_storage_attachment", "storage_attachment", "reject-cross-account-attach", "rejected")
}

func TestServiceReplaysResourceStateFromOperationStore(t *testing.T) {
	store := NewMemoryOperationStore()
	ctx := context.Background()
	original := NewServiceWithOperationStore(testProvider{}, store)

	compute, err := original.CreateComputeAllocation(ctx, ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", IdempotencyKey: "replay-compute"})
	if err != nil {
		t.Fatalf("create compute: %v", err)
	}
	waitForOperation(t, original, "create_compute_allocation", "compute_allocation", compute.ID, "succeeded")
	volume, err := original.CreateStorageVolume(ctx, StorageVolumeInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", SizeGB: 10, IdempotencyKey: "replay-storage"})
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}

	replayed := NewServiceWithOperationStore(testProvider{}, store)
	current, ok := replayed.GetComputeAllocation(ctx, compute.ID)
	if !ok || current.Status == "" || current.AccountID != "acct-alpha" {
		t.Fatalf("replayed compute = %#v ok=%v", current, ok)
	}
	attachment, err := replayed.CreateStorageAttachment(ctx, StorageAttachmentInput{WorkspaceID: "ws-alpha", ComputeID: compute.ID, VolumeID: volume.ID, IdempotencyKey: "replay-attach"})
	if err != nil {
		t.Fatalf("attach after replay: %v", err)
	}
	runtime, err := replayed.CreateWorkspaceRuntime(ctx, WorkspaceRuntimeInput{WorkspaceID: "ws-alpha", ComputeID: compute.ID, VolumeID: volume.ID, ImageID: "one-person-lab-app", IdempotencyKey: "replay-runtime"})
	if err != nil {
		t.Fatalf("runtime after replay: %v", err)
	}

	replayedAgain := NewServiceWithOperationStore(testProvider{}, store)
	if detached, err := replayedAgain.DetachStorageAttachment(ctx, attachment.ID); err != nil || detached.Status != "detached" {
		t.Fatalf("detach replayed attachment = %#v err=%v", detached, err)
	}
	status, err := replayedAgain.WorkspaceRuntimeStatus(ctx, runtime.WorkspaceID)
	if err != nil || status.Status != "not_found" || status.Access.Password != "" {
		t.Fatalf("runtime status must come from provider/Secret, not replayed facts: %#v err=%v", status, err)
	}
}

func TestCreateWorkspaceRuntimeReplaysIdempotentlyBeforeProvider(t *testing.T) {
	provider := &countingRuntimeProvider{}
	service := NewServiceWithOperationStore(provider, NewMemoryOperationStore())
	service.computes["compute-alpha"] = ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", ServiceName: "opl-compute-alpha"}
	service.volumes["storage-alpha"] = StorageVolume{ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", ProviderResourceID: "pvc/storage-alpha"}
	service.volumes["storage-other"] = StorageVolume{ID: "storage-other", AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", ProviderResourceID: "pvc/storage-other"}
	input := WorkspaceRuntimeInput{WorkspaceID: "workspace-alpha", ComputeID: "compute-alpha", VolumeID: "storage-alpha", ImageID: "one-person-lab-app", IdempotencyKey: "runtime-once"}
	first, err := service.CreateWorkspaceRuntime(context.Background(), input)
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	replayed, err := service.CreateWorkspaceRuntime(context.Background(), input)
	if err != nil || replayed.ID != first.ID || provider.calls.Load() != 1 {
		t.Fatalf("runtime replay = %#v err=%v providerCalls=%d", replayed, err, provider.calls.Load())
	}
	changed := input
	changed.VolumeID = "storage-other"
	if _, err := service.CreateWorkspaceRuntime(context.Background(), changed); !errors.Is(err, ErrRuntimeIdempotencyConflict) {
		t.Fatalf("changed replay error = %v, want ErrRuntimeIdempotencyConflict", err)
	}
}

func TestDestroyWorkspaceRuntimeReplaysIdempotentlyBeforeProvider(t *testing.T) {
	provider := &countingRuntimeProvider{}
	service := NewServiceWithOperationStore(provider, NewMemoryOperationStore())

	first, err := service.DestroyWorkspaceRuntime(context.Background(), "workspace-alpha", "runtime-destroy-once")
	if err != nil {
		t.Fatalf("destroy runtime: %v", err)
	}
	replayed, err := service.DestroyWorkspaceRuntime(context.Background(), "workspace-alpha", "runtime-destroy-once")
	if err != nil || replayed.Status != "destroyed" || first.WorkspaceID != "workspace-alpha" || provider.destroyCalls.Load() != 1 {
		t.Fatalf("destroy replay = %#v err=%v providerCalls=%d", replayed, err, provider.destroyCalls.Load())
	}
}

func TestDestroyWorkspaceRuntimeRetriesFailedProviderOperation(t *testing.T) {
	provider := &failOnceDestroyProvider{}
	service := NewServiceWithOperationStore(provider, NewMemoryOperationStore())

	if _, err := service.DestroyWorkspaceRuntime(context.Background(), "workspace-alpha", "runtime-destroy-once"); err == nil {
		t.Fatal("first destroy succeeded, want transient failure")
	}
	runtime, err := service.DestroyWorkspaceRuntime(context.Background(), "workspace-alpha", "runtime-destroy-once")
	if err != nil || runtime.Status != "destroyed" || provider.destroyCalls.Load() != 2 {
		t.Fatalf("retry destroy = %#v err=%v providerCalls=%d", runtime, err, provider.destroyCalls.Load())
	}
	if _, err := service.DestroyWorkspaceRuntime(context.Background(), "workspace-alpha", "runtime-destroy-once"); err != nil || provider.destroyCalls.Load() != 2 {
		t.Fatalf("successful replay err=%v providerCalls=%d", err, provider.destroyCalls.Load())
	}
}

func TestCreateWorkspaceRuntimeClaimsAcrossServiceInstances(t *testing.T) {
	provider := &blockingRuntimeProvider{entered: make(chan struct{}), release: make(chan struct{})}
	store := NewMemoryOperationStore()
	firstService := runtimeTestService(provider, store)
	secondService := runtimeTestService(provider, store)
	input := runtimeTestInput("runtime-shared")

	firstDone := make(chan error, 1)
	go func() {
		_, err := firstService.CreateWorkspaceRuntime(context.Background(), input)
		firstDone <- err
	}()
	select {
	case <-provider.entered:
	case <-time.After(time.Second):
		t.Fatal("first provider call did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := secondService.CreateWorkspaceRuntime(ctx, input); err == nil || err.Error() != "runtime_operation_in_progress" {
		t.Fatalf("concurrent replay error = %v, want runtime_operation_in_progress", err)
	}
	if calls := provider.calls.Load(); calls != 1 {
		t.Fatalf("provider calls = %d, want 1", calls)
	}
	close(provider.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first runtime create: %v", err)
	}

	restarted := NewServiceWithOperationStore(provider, store)
	replayed, err := restarted.CreateWorkspaceRuntime(context.Background(), input)
	if err != nil || replayed.ID != "runtime-alpha" || provider.calls.Load() != 1 {
		t.Fatalf("restart replay = %#v err=%v providerCalls=%d", replayed, err, provider.calls.Load())
	}
	changed := input
	changed.ImageID = "changed-image"
	if _, err := restarted.CreateWorkspaceRuntime(context.Background(), changed); !errors.Is(err, ErrRuntimeIdempotencyConflict) {
		t.Fatalf("changed restart replay error = %v, want ErrRuntimeIdempotencyConflict", err)
	}
}

func TestCreateWorkspaceRuntimeDoesNotReapplyPersistedIncompleteOperation(t *testing.T) {
	for _, status := range []string{"started", "failed"} {
		t.Run(status, func(t *testing.T) {
			provider := &countingRuntimeProvider{}
			store := NewMemoryOperationStore()
			input := runtimeTestInput("runtime-" + status)
			now := time.Now().UTC()
			operation := newOperation("create_workspace_runtime", "workspace_runtime", input.WorkspaceID, "acct-alpha", input.WorkspaceID, input.IdempotencyKey, hashInput(input), now)
			operation.ID = "persisted-" + status
			operation.Status = status
			operation.CreatedAt = now
			if status == "failed" {
				operation.FinishedAt = now
				operation.ErrorCode = "provider_error"
			}
			if err := store.Append(context.Background(), operation); err != nil {
				t.Fatalf("seed operation: %v", err)
			}

			service := runtimeTestService(provider, store)
			_, err := service.CreateWorkspaceRuntime(context.Background(), input)
			want := "runtime_operation_in_progress"
			if status == "failed" {
				want = "runtime_operation_failed"
			}
			if err == nil || err.Error() != want {
				t.Fatalf("persisted %s error = %v, want %s", status, err, want)
			}
			if calls := provider.calls.Load(); calls != 0 {
				t.Fatalf("provider calls = %d, want 0", calls)
			}
		})
	}
}

func runtimeTestService(provider Provider, store OperationStore) *Service {
	service := NewServiceWithOperationStore(provider, store)
	service.computes["compute-alpha"] = ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", ServiceName: "opl-compute-alpha"}
	service.volumes["storage-alpha"] = StorageVolume{ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", ProviderResourceID: "pvc/storage-alpha"}
	return service
}

func runtimeTestInput(key string) WorkspaceRuntimeInput {
	return WorkspaceRuntimeInput{WorkspaceID: "workspace-alpha", ComputeID: "compute-alpha", VolumeID: "storage-alpha", ImageID: "one-person-lab-app", IdempotencyKey: key}
}

func waitForOperation(t *testing.T, service *Service, action string, resourceKind string, resourceID string, status string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		operations, err := service.ListOperations(context.Background())
		if err != nil {
			t.Fatalf("list operations: %v", err)
		}
		for _, operation := range operations {
			if operation.Action == action && operation.ResourceKind == resourceKind && operation.ResourceID == resourceID && operation.Status == status {
				if operation.OperationID == "" || operation.ProviderRequestID == "" || operation.RequestHash == "" || operation.StartedAt.IsZero() || operation.FinishedAt.IsZero() {
					t.Fatalf("operation missing audit fields: %#v", operation)
				}
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("missing operation %s/%s/%s/%s", action, resourceKind, resourceID, status)
}

func assertOperationFact(t *testing.T, operations []FabricOperation, action string, resourceKind string, resourceID string, status string) {
	t.Helper()
	for _, operation := range operations {
		if operation.Action != action || operation.ResourceKind != resourceKind || operation.ResourceID != resourceID || operation.Status != status {
			continue
		}
		if operation.OperationID == "" || operation.ProviderRequestID == "" || operation.RequestHash == "" || operation.StartedAt.IsZero() || operation.FinishedAt.IsZero() {
			t.Fatalf("operation missing audit fields: %#v", operation)
		}
		return
	}
	t.Fatalf("missing operation %s/%s/%s/%s in %#v", action, resourceKind, resourceID, status, operations)
}

type blockingProvider struct {
	testProvider
	done chan struct{}
}

func (p *blockingProvider) CreateComputeAllocation(ctx context.Context, input ComputeAllocationInput) (ComputeAllocation, error) {
	<-p.done
	return ComputeAllocation{ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, PackageID: input.PackageID, Status: "running", Provider: "tencent-tke", ProviderRequestID: providerRequestID("compute", input.IdempotencyKey), NodeName: "node-alpha", CreatedAt: time.Now().UTC()}, nil
}

type testProvider struct{}

type countingRuntimeProvider struct {
	testProvider
	calls        atomic.Int32
	destroyCalls atomic.Int32
}

type failOnceDestroyProvider struct {
	testProvider
	destroyCalls atomic.Int32
}

func (p *failOnceDestroyProvider) DestroyWorkspaceRuntime(_ context.Context, workspaceID string) (WorkspaceRuntime, error) {
	if p.destroyCalls.Add(1) == 1 {
		return WorkspaceRuntime{WorkspaceID: workspaceID, Status: "destroying"}, errors.New("cluster unavailable")
	}
	return WorkspaceRuntime{WorkspaceID: workspaceID, Status: "destroyed"}, nil
}

func (p *countingRuntimeProvider) CreateWorkspaceRuntime(_ context.Context, input WorkspaceRuntimeInput, _ ComputeAllocation, _ StorageVolume) (WorkspaceRuntime, error) {
	p.calls.Add(1)
	return WorkspaceRuntime{ID: "runtime-alpha", WorkspaceID: input.WorkspaceID, Status: "running", Ready: true, ServiceName: "opl-compute-alpha", ProviderRequestID: providerRequestID("runtime", input.IdempotencyKey)}, nil
}

func (p *countingRuntimeProvider) DestroyWorkspaceRuntime(_ context.Context, workspaceID string) (WorkspaceRuntime, error) {
	p.destroyCalls.Add(1)
	return WorkspaceRuntime{WorkspaceID: workspaceID, Status: "destroyed"}, nil
}

type blockingRuntimeProvider struct {
	testProvider
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (p *blockingRuntimeProvider) CreateWorkspaceRuntime(ctx context.Context, input WorkspaceRuntimeInput, _ ComputeAllocation, _ StorageVolume) (WorkspaceRuntime, error) {
	if p.calls.Add(1) == 1 {
		close(p.entered)
	}
	select {
	case <-p.release:
		return WorkspaceRuntime{ID: "runtime-alpha", WorkspaceID: input.WorkspaceID, Status: "running", Ready: true, ProviderRequestID: providerRequestID("runtime", input.IdempotencyKey)}, nil
	case <-ctx.Done():
		return WorkspaceRuntime{}, ctx.Err()
	}
}

type contentTestProvider struct {
	testProvider
	path      string
	published []byte
}

func (p *contentTestProvider) PublishWorkspaceContent(_ context.Context, _ string, targetPath string, body []byte) error {
	p.path = targetPath
	p.published = append([]byte(nil), body...)
	return nil
}

func (testProvider) CreateComputeAllocation(_ context.Context, input ComputeAllocationInput) (ComputeAllocation, error) {
	now := time.Now().UTC()
	return ComputeAllocation{ID: "ca-test", AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, PackageID: input.PackageID, Status: "allocated", Provider: "tencent-tke", ProviderRequestID: providerRequestID("compute", input.IdempotencyKey), ServiceName: "opl-ca-test", CreatedAt: now}, nil
}

func (testProvider) SyncComputeAllocation(_ context.Context, allocation ComputeAllocation) (ComputeAllocation, error) {
	allocation.Status = "running"
	return allocation, nil
}

func (testProvider) DestroyComputeAllocation(_ context.Context, allocation ComputeAllocation) (ComputeAllocation, error) {
	allocation.Status = "destroyed"
	return allocation, nil
}

func (testProvider) CreateStorageVolume(_ context.Context, input StorageVolumeInput) (StorageVolume, error) {
	return StorageVolume{ID: "vol-test", AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "ready", ProviderRequestID: providerRequestID("storage", input.IdempotencyKey), SizeGB: input.SizeGB}, nil
}

func (testProvider) SyncStorageVolume(_ context.Context, volume StorageVolume) (StorageVolume, error) {
	volume.Status = "ready"
	return volume, nil
}

func (testProvider) DestroyStorageVolume(_ context.Context, volume StorageVolume) (StorageVolume, error) {
	volume.Status = "destroyed"
	return volume, nil
}

func (testProvider) CreateStorageSnapshot(_ context.Context, input StorageSnapshotInput, volume StorageVolume) (StorageSnapshot, error) {
	return StorageSnapshot{ID: "snap-test", AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, VolumeID: volume.ID, Status: "ready", Provider: "test", ProviderSnapshotRef: "volumesnapshot/snap-test", ProviderRequestID: "snapshot-request", SizeGB: volume.SizeGB, CreatedAt: time.Now().UTC()}, nil
}

func (testProvider) SyncStorageSnapshot(_ context.Context, snapshot StorageSnapshot) (StorageSnapshot, error) {
	return snapshot, nil
}

func (testProvider) RestoreStorageSnapshot(_ context.Context, input StorageRestoreInput, snapshot StorageSnapshot) (StorageVolume, error) {
	return StorageVolume{ID: input.TargetVolumeID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "ready", Provider: "test", ProviderResourceID: "pvc/" + input.TargetVolumeID, ProviderRequestID: "restore-request", SizeGB: snapshot.SizeGB, CreatedAt: time.Now().UTC()}, nil
}

func (testProvider) DestroyStorageSnapshot(_ context.Context, snapshot StorageSnapshot) (StorageSnapshot, error) {
	snapshot.Status = "destroyed"
	return snapshot, nil
}

func (testProvider) CreateStorageAttachment(_ context.Context, input StorageAttachmentInput, _ ComputeAllocation, _ StorageVolume) (StorageAttachment, error) {
	return StorageAttachment{ID: "att-test", WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID, Status: "attached", ProviderRequestID: providerRequestID("storage-attach", input.IdempotencyKey)}, nil
}

func (testProvider) DetachStorageAttachment(_ context.Context, attachment StorageAttachment) (StorageAttachment, error) {
	attachment.Status = "detached"
	return attachment, nil
}

func (testProvider) CreateWorkspaceRuntime(_ context.Context, input WorkspaceRuntimeInput, _ ComputeAllocation, _ StorageVolume) (WorkspaceRuntime, error) {
	return WorkspaceRuntime{ID: "rt-test", WorkspaceID: input.WorkspaceID, Status: "running", ServiceName: "opl-ca-test", ProviderRequestID: providerRequestID("runtime", input.IdempotencyKey), Access: RuntimeAccess{Username: "admin", Password: "runtime-password-alpha", CredentialStatus: "configured", CredentialVersion: "v1", SecretRef: "opl-ca-test-env"}}, nil
}

func (testProvider) DestroyWorkspaceRuntime(_ context.Context, workspaceID string) (WorkspaceRuntime, error) {
	return WorkspaceRuntime{WorkspaceID: workspaceID, Status: "destroyed"}, nil
}

func (testProvider) WorkspaceRuntimeStatus(_ context.Context, workspaceID string) (WorkspaceRuntime, error) {
	return WorkspaceRuntime{WorkspaceID: workspaceID, Status: "not_found"}, nil
}

func (testProvider) Readiness(_ context.Context) (map[string]any, error) {
	return map[string]any{"provider": "test", "ready": true}, nil
}
