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

func TestFabricHTTPClientWritesAccountGatewaySecret(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/fabric/gateway-secrets" || r.Header.Get("Idempotency-Key") != "workspace-once:gateway-secret" || r.Header.Get("Authorization") != "Bearer internal-secret" {
			t.Fatalf("unexpected request: %s %s key=%q auth=%q", r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"), r.Header.Get("Authorization"))
		}
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if len(input) != 2 || input["accountId"] != "acct-alpha" || input["gatewayApiKey"] != "workspace-key-secret" {
			t.Fatalf("gateway secret input = %#v", input)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"secretRef": "opl-gateway-acct-alpha", "version": "v2", "fingerprint": "sha256:redacted"})
	}))
	defer upstream.Close()

	client := NewFabricHTTPClient(upstream.URL, "internal-secret", upstream.Client())
	result, err := client.WriteGatewaySecret(context.Background(), GatewaySecretWriteInput{AccountID: "acct-alpha", GatewayAPIKey: "workspace-key-secret"}, "workspace-once:gateway-secret")
	if err != nil || result.SecretRef != "opl-gateway-acct-alpha" || result.Version != "v2" || result.Fingerprint != "sha256:redacted" {
		t.Fatalf("gateway secret result = %#v err=%v", result, err)
	}
}

func TestFabricHTTPClientGatewaySecretErrorDoesNotLeakKey(t *testing.T) {
	const secret = "workspace-key-secret"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, secret, http.StatusInternalServerError)
	}))
	defer upstream.Close()

	client := NewFabricHTTPClient(upstream.URL, "internal-secret", upstream.Client())
	_, err := client.WriteGatewaySecret(context.Background(), GatewaySecretWriteInput{AccountID: "acct-alpha", GatewayAPIKey: secret}, "workspace-once:gateway-secret")
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("gateway secret error = %v", err)
	}
}

func TestFabricHTTPClientPreflightsMonthlyResourceWithoutIdempotencyKey(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/fabric/monthly-preflight" || r.Header.Get("Authorization") != "Bearer internal-secret" {
			t.Fatalf("unexpected request: %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		if _, ok := r.Header["Idempotency-Key"]; ok {
			t.Fatalf("read-only preflight sent Idempotency-Key: %#v", r.Header.Values("Idempotency-Key"))
		}
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if len(input) != 4 || input["resourceType"] != "storage" || input["packageId"] != "pro" || input["sizeGb"] != float64(100) || input["zone"] != "ap-guangzhou-3" {
			t.Fatalf("monthly preflight input = %#v", input)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resourceType": "storage", "packageId": "pro", "sizeGb": 100, "zone": "ap-guangzhou-3",
			"available": true, "chargeType": "PREPAID", "periodMonths": 1, "renewFlag": "NOTIFY_AND_MANUAL_RENEW",
			"providerPriceCny": 12.34, "providerRequestIds": map[string]string{"quota": "quota-request", "price": "price-request"},
		})
	}))
	defer upstream.Close()

	client := NewFabricHTTPClient(upstream.URL, "internal-secret", upstream.Client()).(FabricMonthlyPreflightClient)
	result, err := client.MonthlyPreflight(context.Background(), MonthlyPreflightInput{ResourceType: "storage", PackageID: "pro", SizeGB: 100, Zone: "ap-guangzhou-3"})
	if err != nil || !result.Available || result.ProviderPriceCNY != 12.34 || result.ProviderRequestIDs["quota"] != "quota-request" {
		t.Fatalf("monthly preflight = %#v err=%v", result, err)
	}
}

func TestFabricHTTPClientCreatesZonedPrepaidStorage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/fabric/storage-volumes" || r.Header.Get("Idempotency-Key") != "storage-once" {
			t.Fatalf("unexpected request: %s %s key=%q", r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"))
		}
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input["computeId"] != "compute-alpha" || input["zone"] != "ap-shanghai-2" {
			t.Fatalf("storage placement input = %#v", input)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "storage-alpha", "status": "available", "providerResourceId": "disk-alpha",
			"zone": "ap-shanghai-2", "diskType": "CLOUD_PREMIUM", "renewFlag": "NOTIFY_AND_MANUAL_RENEW",
			"deadline": "2026-08-16T00:00:00Z", "cbsStatus": "UNATTACHED", "providerData": map[string]any{"chargeType": "PREPAID"},
		})
	}))
	defer upstream.Close()

	client := NewFabricHTTPClient(upstream.URL, "internal-secret", upstream.Client())
	volume, err := client.CreateStorageVolume(context.Background(), StorageVolumeInput{ID: "storage-alpha", AccountID: "acct-alpha", ComputeID: "compute-alpha", Zone: "ap-shanghai-2", SizeGB: 10}, "storage-once")
	if err != nil || volume.Zone != "ap-shanghai-2" || volume.DiskType != "CLOUD_PREMIUM" || volume.RenewFlag != "NOTIFY_AND_MANUAL_RENEW" || volume.Deadline != "2026-08-16T00:00:00Z" || volume.CBSStatus != "UNATTACHED" || volume.ProviderData["chargeType"] != "PREPAID" {
		t.Fatalf("storage readback = %#v err=%v", volume, err)
	}
}

func TestFabricHTTPClientRenewsMonthlyResourcesWithReadback(t *testing.T) {
	paths := []string{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Method != http.MethodPost || r.Header.Get("Idempotency-Key") != "renew-once" {
			t.Fatalf("unexpected renewal request: %s %s key=%q", r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"))
		}
		switch r.URL.Path {
		case "/fabric/compute-allocations/compute-alpha/renew":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "compute-alpha", "status": "running", "providerRequestId": "compute-renew", "chargeType": "PREPAID", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": "2026-09-16T00:00:00Z", "providerData": map[string]any{"renewalResult": "renewed"}, "costTags": map[string]string{"opl_account_id": "acct-alpha"}})
		case "/fabric/storage-volumes/storage-alpha/renew":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "storage-alpha", "status": "available", "providerRequestId": "storage-renew", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": "2026-09-16T00:00:00Z", "cbsStatus": "UNATTACHED", "providerData": map[string]any{"chargeType": "PREPAID", "renewalResult": "already_renewed"}, "costTags": map[string]string{"opl_account_id": "acct-alpha"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	client := NewFabricHTTPClient(upstream.URL, "internal-secret", upstream.Client()).(FabricRenewalClient)
	compute, err := client.RenewComputeAllocation(context.Background(), "compute-alpha", "renew-once")
	if err != nil || compute.ProviderRequestID != "compute-renew" || compute.ChargeType != "PREPAID" || compute.RenewFlag != "NOTIFY_AND_MANUAL_RENEW" || compute.Deadline != "2026-09-16T00:00:00Z" || compute.ProviderData["renewalResult"] != "renewed" || compute.CostTags["opl_account_id"] != "acct-alpha" {
		t.Fatalf("compute renewal = %#v err=%v", compute, err)
	}
	storage, err := client.RenewStorageVolume(context.Background(), "storage-alpha", "renew-once")
	if err != nil || storage.ProviderRequestID != "storage-renew" || storage.Deadline != "2026-09-16T00:00:00Z" || storage.ProviderData["renewalResult"] != "already_renewed" || storage.CostTags["opl_account_id"] != "acct-alpha" {
		t.Fatalf("storage renewal = %#v err=%v", storage, err)
	}
	if strings.Join(paths, ",") != "/fabric/compute-allocations/compute-alpha/renew,/fabric/storage-volumes/storage-alpha/renew" {
		t.Fatalf("renewal paths = %#v", paths)
	}
}

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
