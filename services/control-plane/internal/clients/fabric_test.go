package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFabricTransferClientStreamsChunksAndContent(t *testing.T) {
	body := []byte("content")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer internal-secret" {
			t.Fatalf("Authorization = %q", got)
		}
		switch {
		case r.Method == http.MethodPut:
			if r.URL.Path != "/fabric/transfers/transfer-alpha/chunks/2" || r.Header.Get("X-Chunk-SHA256") != "chunk-digest" {
				t.Fatalf("unexpected chunk request: %s %s", r.Method, r.URL.Path)
			}
			_ = json.NewEncoder(w).Encode(ContentTransfer{TransferID: "transfer-alpha", ReceivedChunks: []int{2}})
		case r.Method == http.MethodGet:
			w.Header().Set("X-Content-SHA256", "content-digest")
			w.Header().Set("X-Workspace-Path", "inputs/a.txt")
			_, _ = w.Write(body)
		}
	}))
	defer upstream.Close()
	client := NewFabricHTTPClient(upstream.URL, "internal-secret", upstream.Client()).(FabricTransferClient)
	transfer, err := client.PutTransferChunk(context.Background(), "transfer-alpha", 2, []byte("chunk"), "chunk-digest")
	if err != nil || len(transfer.ReceivedChunks) != 1 {
		t.Fatalf("put = %#v err=%v", transfer, err)
	}
	content, err := client.Content(context.Background(), "workspace-alpha", "content-digest")
	if err != nil || !bytes.Equal(content.Body, body) || content.Path != "inputs/a.txt" {
		t.Fatalf("content=%#v err=%v", content, err)
	}
}

func TestFabricClientCreatesJob(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/fabric/jobs" || r.Header.Get("Idempotency-Key") != "job-once" {
			t.Fatalf("unexpected request: %s %s key=%s", r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"))
		}
		_ = json.NewEncoder(w).Encode(Job{JobID: "job-alpha", RequestID: "request-alpha", Status: "queued"})
	}))
	defer upstream.Close()

	client := NewFabricHTTPClient(upstream.URL, "internal-secret", upstream.Client())
	job, err := client.CreateJob(context.Background(), JobInput{OrganizationID: "org-alpha", WorkspaceID: "workspace-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", RequestID: "request-alpha", ApprovalID: "approval-alpha"}, "job-once")
	if err != nil || job.JobID != "job-alpha" || job.Status != "queued" {
		t.Fatalf("job = %#v err=%v", job, err)
	}
}

func TestFabricHTTPClientDestroysWorkspaceRuntime(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/fabric/workspace-runtimes/workspace-alpha/destroy" || r.Header.Get("Idempotency-Key") != "runtime-destroy-once" {
			t.Fatalf("unexpected request: %s %s key=%s", r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"))
		}
		_ = json.NewEncoder(w).Encode(WorkspaceRuntime{WorkspaceID: "workspace-alpha", Status: "destroyed"})
	}))
	defer upstream.Close()

	client := NewFabricHTTPClient(upstream.URL, "internal-secret", upstream.Client())
	runtime, err := client.DestroyWorkspaceRuntime(context.Background(), "workspace-alpha", "runtime-destroy-once")
	if err != nil || runtime.Status != "destroyed" {
		t.Fatalf("runtime = %#v err=%v", runtime, err)
	}
}

func TestFabricClientReturnsErrorOnUpstreamFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "fabric unavailable", http.StatusServiceUnavailable)
	}))
	defer upstream.Close()

	client := NewFabricHTTPClient(upstream.URL, "internal-secret", upstream.Client())
	if _, err := client.Catalog(context.Background()); err == nil || !strings.Contains(err.Error(), "status 503") {
		t.Fatalf("expected upstream status error, got %v", err)
	}
}

func TestFabricClientReadsCompletedJobEvidence(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/fabric/jobs/job-alpha" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"jobId":"job-alpha","status":"succeeded","attempt":2,"artifactIds":["artifact-alpha"],"reviewIds":["review-alpha"]}`))
	}))
	defer upstream.Close()

	client := NewFabricHTTPClient(upstream.URL, "internal-secret", upstream.Client())
	job, err := client.GetJob(context.Background(), "job-alpha")
	if err != nil || job.Status != "succeeded" || job.Attempt != 2 || len(job.ArtifactIDs) != 1 || len(job.ReviewIDs) != 1 {
		t.Fatalf("job = %#v err=%v", job, err)
	}
}

func TestFabricRecoveryClientUsesSnapshotContract(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Idempotency-Key") == "" {
			t.Fatalf("missing idempotency key for %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/fabric/storage-snapshots":
			_ = json.NewEncoder(w).Encode(StorageSnapshot{ID: "snap-alpha", WorkspaceID: "ws-alpha", VolumeID: "vol-alpha", Status: "ready"})
		case "/fabric/storage-snapshots/snap-alpha/restore":
			_ = json.NewEncoder(w).Encode(StorageVolume{ID: "vol-restored", WorkspaceID: "ws-restored", Status: "restoring"})
		default:
			t.Fatalf("unexpected recovery path %s", r.URL.Path)
		}
	}))
	defer upstream.Close()
	client := NewFabricHTTPClient(upstream.URL, "internal-secret", upstream.Client()).(FabricRecoveryClient)
	snapshot, err := client.CreateStorageSnapshot(context.Background(), StorageSnapshotInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", VolumeID: "vol-alpha"}, "snapshot-once")
	if err != nil || snapshot.ID != "snap-alpha" {
		t.Fatalf("snapshot=%#v err=%v", snapshot, err)
	}
	volume, err := client.RestoreStorageSnapshot(context.Background(), "snap-alpha", StorageRestoreInput{AccountID: "acct-alpha", WorkspaceID: "ws-restored", TargetVolumeID: "vol-restored"}, "restore-once")
	if err != nil || volume.ID != "vol-restored" {
		t.Fatalf("volume=%#v err=%v", volume, err)
	}
}
