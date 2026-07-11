package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"opl-cloud/services/fabric/internal/fabric"
)

func TestContentTransferHTTPResumesAndDownloads(t *testing.T) {
	server := NewServer(fabric.NewService(testProvider{}))
	body := []byte("workspace bytes")
	digest := fmt.Sprintf("%x", sha256.Sum256(body))
	create := httptest.NewRequest(http.MethodPost, "/fabric/transfers", bytes.NewBufferString(fmt.Sprintf(`{"organizationId":"org-alpha","workspaceId":"workspace-alpha","projectId":"project-alpha","path":"inputs/a.txt","digest":"%s","size":%d,"chunkSize":%d}`, digest, len(body), len(body))))
	create.Header.Set("Idempotency-Key", "transfer-http")
	createdRec := httptest.NewRecorder()
	server.ServeHTTP(createdRec, create)
	if createdRec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createdRec.Code, createdRec.Body.String())
	}
	var transfer fabric.Transfer
	if err := json.NewDecoder(createdRec.Body).Decode(&transfer); err != nil {
		t.Fatal(err)
	}

	put := httptest.NewRequest(http.MethodPut, "/fabric/transfers/"+transfer.TransferID+"/chunks/0", bytes.NewReader(body))
	put.Header.Set("X-Chunk-SHA256", digest)
	putRec := httptest.NewRecorder()
	server.ServeHTTP(putRec, put)
	if putRec.Code != http.StatusOK {
		t.Fatalf("put status=%d body=%s", putRec.Code, putRec.Body.String())
	}
	complete := httptest.NewRequest(http.MethodPost, "/fabric/transfers/"+transfer.TransferID+"/complete", nil)
	completeRec := httptest.NewRecorder()
	server.ServeHTTP(completeRec, complete)
	if completeRec.Code != http.StatusOK {
		t.Fatalf("complete status=%d body=%s", completeRec.Code, completeRec.Body.String())
	}
	download := httptest.NewRequest(http.MethodGet, "/fabric/contents/"+digest, nil)
	download.Header.Set("X-Workspace-ID", "workspace-alpha")
	downloadRec := httptest.NewRecorder()
	server.ServeHTTP(downloadRec, download)
	downloaded, _ := io.ReadAll(downloadRec.Body)
	if downloadRec.Code != http.StatusOK || !bytes.Equal(downloaded, body) {
		t.Fatalf("download status=%d body=%q", downloadRec.Code, downloaded)
	}
}

func TestCatalogHTTP(t *testing.T) {
	server := NewServer(fabric.NewService(testProvider{}))
	req := httptest.NewRequest(http.MethodGet, "/fabric/catalog", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var catalog fabric.Catalog
	if err := json.NewDecoder(rec.Body).Decode(&catalog); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	if len(catalog.WorkspacePackages) == 0 {
		t.Fatalf("expected workspace packages")
	}
}

func TestCreateComputeAllocationHTTPRequiresIdempotencyKey(t *testing.T) {
	server := NewServer(fabric.NewService(testProvider{}))
	body := bytes.NewBufferString(`{"accountId":"acct-alpha","workspaceId":"ws-alpha","packageId":"basic","dryRun":true}`)
	req := httptest.NewRequest(http.MethodPost, "/fabric/compute-allocations", body)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestSyncComputeAllocationHTTPRefreshesProviderState(t *testing.T) {
	service := fabric.NewService(testProvider{})
	server := NewServer(service)
	create := httptest.NewRequest(http.MethodPost, "/fabric/compute-allocations", bytes.NewBufferString(`{"accountId":"acct-alpha","workspaceId":"ws-alpha","packageId":"basic"}`))
	create.Header.Set("Idempotency-Key", "sync-http-create")
	createRec := httptest.NewRecorder()
	server.ServeHTTP(createRec, create)
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want %d: %s", createRec.Code, http.StatusAccepted, createRec.Body.String())
	}
	var created fabric.ComputeAllocation
	if err := json.NewDecoder(createRec.Body).Decode(&created); err != nil {
		t.Fatalf("decode create: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/fabric/compute-allocations/"+created.ID+"/sync", bytes.NewBufferString(`{}`))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("sync status = %d, want %d: %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var allocation fabric.ComputeAllocation
	if err := json.NewDecoder(rec.Body).Decode(&allocation); err != nil {
		t.Fatalf("decode sync: %v", err)
	}
	if allocation.Status != "external_deleted" {
		t.Fatalf("sync must return provider state, got %#v", allocation)
	}
}

func TestSyncStorageVolumeHTTPRefreshesProviderState(t *testing.T) {
	service := fabric.NewService(testProvider{})
	server := NewServer(service)
	create := httptest.NewRequest(http.MethodPost, "/fabric/storage-volumes", bytes.NewBufferString(`{"accountId":"acct-alpha","workspaceId":"ws-alpha","sizeGb":10}`))
	create.Header.Set("Idempotency-Key", "sync-http-storage")
	createRec := httptest.NewRecorder()
	server.ServeHTTP(createRec, create)
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want %d: %s", createRec.Code, http.StatusAccepted, createRec.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/fabric/storage-volumes/vol-test/sync", bytes.NewBufferString(`{}`))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("sync status = %d, want %d: %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var volume fabric.StorageVolume
	if err := json.NewDecoder(rec.Body).Decode(&volume); err != nil {
		t.Fatalf("decode sync: %v", err)
	}
	if volume.Status != "external_deleted" {
		t.Fatalf("sync must return provider state, got %#v", volume)
	}
}

func TestOperationsHTTPReturnsFabricAuditFacts(t *testing.T) {
	service := fabric.NewService(testProvider{})
	server := NewServer(service)

	create := httptest.NewRequest(http.MethodPost, "/fabric/storage-volumes", bytes.NewBufferString(`{"accountId":"acct-alpha","workspaceId":"ws-alpha","sizeGb":10}`))
	create.Header.Set("Idempotency-Key", "http-ops-storage")
	createRec := httptest.NewRecorder()
	server.ServeHTTP(createRec, create)
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want %d: %s", createRec.Code, http.StatusAccepted, createRec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/fabric/operations", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("operations status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var operations []fabric.FabricOperation
	if err := json.NewDecoder(rec.Body).Decode(&operations); err != nil {
		t.Fatalf("decode operations: %v", err)
	}
	for _, operation := range operations {
		if operation.Action == "create_storage_volume" && operation.ResourceKind == "storage_volume" && operation.Status == "succeeded" {
			if operation.OperationID == "" || operation.ProviderRequestID != "storage-test" || operation.RequestHash == "" {
				t.Fatalf("operation missing audit identity: %#v", operation)
			}
			return
		}
	}
	t.Fatalf("missing storage operation in %#v", operations)
}

func TestJobHTTPLifecycle(t *testing.T) {
	server := NewServer(fabric.NewService(testProvider{}))
	create := httptest.NewRequest(http.MethodPost, "/fabric/jobs", bytes.NewBufferString(`{"organizationId":"org-alpha","workspaceId":"workspace-alpha","projectId":"project-alpha","taskId":"task-alpha","requestId":"request-alpha","approvalId":"approval-alpha","environmentRef":"environment-alpha"}`))
	create.Header.Set("Idempotency-Key", "http-job-once")
	createRec := httptest.NewRecorder()
	server.ServeHTTP(createRec, create)
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want %d: %s", createRec.Code, http.StatusAccepted, createRec.Body.String())
	}
	var created fabric.Job
	if err := json.NewDecoder(createRec.Body).Decode(&created); err != nil {
		t.Fatalf("decode job: %v", err)
	}

	get := httptest.NewRequest(http.MethodGet, "/fabric/jobs/"+created.JobID, nil)
	getRec := httptest.NewRecorder()
	server.ServeHTTP(getRec, get)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want %d: %s", getRec.Code, http.StatusOK, getRec.Body.String())
	}

	cancel := httptest.NewRequest(http.MethodPost, "/fabric/jobs/"+created.JobID+"/cancel", bytes.NewBufferString(`{}`))
	cancel.Header.Set("Idempotency-Key", "http-job-cancel")
	cancelRec := httptest.NewRecorder()
	server.ServeHTTP(cancelRec, cancel)
	if cancelRec.Code != http.StatusAccepted {
		t.Fatalf("cancel status = %d, want %d: %s", cancelRec.Code, http.StatusAccepted, cancelRec.Body.String())
	}
	var cancelled fabric.Job
	if err := json.NewDecoder(cancelRec.Body).Decode(&cancelled); err != nil {
		t.Fatalf("decode cancelled job: %v", err)
	}
	if cancelled.JobID != created.JobID || cancelled.Status != "cancelled" {
		t.Fatalf("unexpected cancelled job: %#v", cancelled)
	}
}

func TestJobHTTPReturnsNotFound(t *testing.T) {
	server := NewServer(fabric.NewService(testProvider{}))
	req := httptest.NewRequest(http.MethodGet, "/fabric/jobs/job-missing", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestJobHTTPRequiresCanonicalIdentity(t *testing.T) {
	server := NewServer(fabric.NewService(testProvider{}))
	req := httptest.NewRequest(http.MethodPost, "/fabric/jobs", bytes.NewBufferString(`{}`))
	req.Header.Set("Idempotency-Key", "invalid-job")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestRunnerJobHTTPCompletionLifecycle(t *testing.T) {
	server := NewServer(fabric.NewService(testProvider{}))
	create := httptest.NewRequest(http.MethodPost, "/fabric/jobs", bytes.NewBufferString(`{"organizationId":"org-alpha","workspaceId":"workspace-alpha","projectId":"project-alpha","taskId":"task-alpha","requestId":"request-alpha","approvalId":"approval-alpha"}`))
	create.Header.Set("Idempotency-Key", "http-runner-job")
	createRec := httptest.NewRecorder()
	server.ServeHTTP(createRec, create)
	var job fabric.Job
	if err := json.NewDecoder(createRec.Body).Decode(&job); err != nil {
		t.Fatalf("decode job: %v", err)
	}

	claim := httptest.NewRequest(http.MethodPost, "/fabric/jobs/"+job.JobID+"/claim", bytes.NewBufferString(`{"runnerId":"runner-alpha"}`))
	claim.Header.Set("Idempotency-Key", "http-claim")
	claimRec := httptest.NewRecorder()
	server.ServeHTTP(claimRec, claim)
	if claimRec.Code != http.StatusAccepted {
		t.Fatalf("claim status = %d: %s", claimRec.Code, claimRec.Body.String())
	}
	var claimed fabric.Job
	if err := json.NewDecoder(claimRec.Body).Decode(&claimed); err != nil || claimed.LeaseToken == "" {
		t.Fatalf("decode claim: %#v, %v", claimed, err)
	}

	heartbeat := httptest.NewRequest(http.MethodPost, "/fabric/jobs/"+job.JobID+"/heartbeat", bytes.NewBufferString(`{"runnerId":"runner-alpha","leaseToken":"`+claimed.LeaseToken+`"}`))
	heartbeat.Header.Set("Idempotency-Key", "http-heartbeat")
	heartbeatRec := httptest.NewRecorder()
	server.ServeHTTP(heartbeatRec, heartbeat)
	if heartbeatRec.Code != http.StatusAccepted {
		t.Fatalf("heartbeat status = %d: %s", heartbeatRec.Code, heartbeatRec.Body.String())
	}

	complete := httptest.NewRequest(http.MethodPost, "/fabric/jobs/"+job.JobID+"/complete", bytes.NewBufferString(`{"runnerId":"runner-alpha","leaseToken":"`+claimed.LeaseToken+`","artifactIds":["artifact-alpha"],"reviewIds":["review-alpha"]}`))
	complete.Header.Set("Idempotency-Key", "http-complete")
	completeRec := httptest.NewRecorder()
	server.ServeHTTP(completeRec, complete)
	if completeRec.Code != http.StatusAccepted {
		t.Fatalf("complete status = %d: %s", completeRec.Code, completeRec.Body.String())
	}
	var completed fabric.Job
	if err := json.NewDecoder(completeRec.Body).Decode(&completed); err != nil || completed.Status != "succeeded" {
		t.Fatalf("decode complete: %#v, %v", completed, err)
	}
}

func TestRunnerJobHTTPFailRetryAndConflict(t *testing.T) {
	server := NewServer(fabric.NewService(testProvider{}))
	create := httptest.NewRequest(http.MethodPost, "/fabric/jobs", bytes.NewBufferString(`{"organizationId":"org-alpha","workspaceId":"workspace-alpha","projectId":"project-alpha","taskId":"task-alpha","requestId":"request-alpha","approvalId":"approval-alpha"}`))
	create.Header.Set("Idempotency-Key", "http-fail-job")
	createRec := httptest.NewRecorder()
	server.ServeHTTP(createRec, create)
	var job fabric.Job
	_ = json.NewDecoder(createRec.Body).Decode(&job)
	claim := httptest.NewRequest(http.MethodPost, "/fabric/jobs/"+job.JobID+"/claim", bytes.NewBufferString(`{"runnerId":"runner-alpha"}`))
	claim.Header.Set("Idempotency-Key", "http-fail-claim")
	claimRec := httptest.NewRecorder()
	server.ServeHTTP(claimRec, claim)
	var claimed fabric.Job
	_ = json.NewDecoder(claimRec.Body).Decode(&claimed)

	conflict := httptest.NewRequest(http.MethodPost, "/fabric/jobs/"+job.JobID+"/heartbeat", bytes.NewBufferString(`{"runnerId":"runner-beta","leaseToken":"`+claimed.LeaseToken+`"}`))
	conflict.Header.Set("Idempotency-Key", "http-wrong-runner")
	conflictRec := httptest.NewRecorder()
	server.ServeHTTP(conflictRec, conflict)
	if conflictRec.Code != http.StatusConflict {
		t.Fatalf("lease conflict status = %d, want %d: %s", conflictRec.Code, http.StatusConflict, conflictRec.Body.String())
	}

	fail := httptest.NewRequest(http.MethodPost, "/fabric/jobs/"+job.JobID+"/fail", bytes.NewBufferString(`{"runnerId":"runner-alpha","leaseToken":"`+claimed.LeaseToken+`","errorCode":"runner_failed"}`))
	fail.Header.Set("Idempotency-Key", "http-fail")
	failRec := httptest.NewRecorder()
	server.ServeHTTP(failRec, fail)
	if failRec.Code != http.StatusAccepted {
		t.Fatalf("fail status = %d: %s", failRec.Code, failRec.Body.String())
	}

	retry := httptest.NewRequest(http.MethodPost, "/fabric/jobs/"+job.JobID+"/retry", nil)
	retry.Header.Set("Idempotency-Key", "http-retry")
	retryRec := httptest.NewRecorder()
	server.ServeHTTP(retryRec, retry)
	if retryRec.Code != http.StatusAccepted {
		t.Fatalf("retry status = %d: %s", retryRec.Code, retryRec.Body.String())
	}
	var retried fabric.Job
	if err := json.NewDecoder(retryRec.Body).Decode(&retried); err != nil || retried.Status != "queued" || retried.Attempt != 2 {
		t.Fatalf("decode retry: %#v, %v", retried, err)
	}
}

type testProvider struct{}

func (testProvider) PublishWorkspaceContent(_ context.Context, _, _ string, _ []byte) error {
	return nil
}

func (testProvider) CreateComputeAllocation(_ context.Context, input fabric.ComputeAllocationInput) (fabric.ComputeAllocation, error) {
	return fabric.ComputeAllocation{ID: "ca-test", AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, PackageID: input.PackageID, Status: "allocated", ProviderRequestID: "compute-test"}, nil
}

func (testProvider) SyncComputeAllocation(_ context.Context, allocation fabric.ComputeAllocation) (fabric.ComputeAllocation, error) {
	allocation.Status = "external_deleted"
	return allocation, nil
}

func (testProvider) DestroyComputeAllocation(_ context.Context, allocation fabric.ComputeAllocation) (fabric.ComputeAllocation, error) {
	allocation.Status = "destroyed"
	return allocation, nil
}

func (testProvider) CreateStorageVolume(_ context.Context, input fabric.StorageVolumeInput) (fabric.StorageVolume, error) {
	return fabric.StorageVolume{ID: "vol-test", AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "ready", ProviderRequestID: "storage-test"}, nil
}

func (testProvider) SyncStorageVolume(_ context.Context, volume fabric.StorageVolume) (fabric.StorageVolume, error) {
	volume.Status = "external_deleted"
	return volume, nil
}

func (testProvider) DestroyStorageVolume(_ context.Context, volume fabric.StorageVolume) (fabric.StorageVolume, error) {
	volume.Status = "destroyed"
	return volume, nil
}

func (testProvider) CreateStorageAttachment(_ context.Context, input fabric.StorageAttachmentInput, _ fabric.ComputeAllocation, _ fabric.StorageVolume) (fabric.StorageAttachment, error) {
	return fabric.StorageAttachment{ID: "att-test", WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID, Status: "attached", ProviderRequestID: "attachment-test"}, nil
}

func (testProvider) DetachStorageAttachment(_ context.Context, attachment fabric.StorageAttachment) (fabric.StorageAttachment, error) {
	attachment.Status = "detached"
	return attachment, nil
}

func (testProvider) CreateWorkspaceRuntime(_ context.Context, input fabric.WorkspaceRuntimeInput, _ fabric.ComputeAllocation, _ fabric.StorageVolume) (fabric.WorkspaceRuntime, error) {
	return fabric.WorkspaceRuntime{ID: "rt-test", WorkspaceID: input.WorkspaceID, Status: "running", ProviderRequestID: "runtime-test"}, nil
}

func (testProvider) WorkspaceRuntimeStatus(_ context.Context, workspaceID string) (fabric.WorkspaceRuntime, error) {
	return fabric.WorkspaceRuntime{WorkspaceID: workspaceID, Status: "not_found"}, nil
}

func (testProvider) Readiness(_ context.Context) (map[string]any, error) {
	return map[string]any{"provider": "test", "ready": true}, nil
}
