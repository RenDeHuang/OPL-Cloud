package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opl-cloud/services/fabric/internal/fabric"
)

func TestServerAuthenticatesEverythingExceptGetHealthz(t *testing.T) {
	server := NewServer(fabric.NewService(testProvider{}), "internal-secret")
	tests := []struct {
		name          string
		method        string
		path          string
		authorization string
		want          int
	}{
		{name: "health", method: http.MethodGet, path: "/healthz", want: http.StatusOK},
		{name: "health wrong method", method: http.MethodPost, path: "/healthz", want: http.StatusUnauthorized},
		{name: "readiness anonymous", method: http.MethodGet, path: "/fabric/readiness", want: http.StatusUnauthorized},
		{name: "unknown anonymous", method: http.MethodGet, path: "/missing", want: http.StatusUnauthorized},
		{name: "wrong scheme", method: http.MethodGet, path: "/fabric/catalog", authorization: "Basic internal-secret", want: http.StatusUnauthorized},
		{name: "wrong token", method: http.MethodGet, path: "/fabric/catalog", authorization: "Bearer wrong", want: http.StatusUnauthorized},
		{name: "authenticated", method: http.MethodGet, path: "/fabric/catalog", authorization: "Bearer internal-secret", want: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req.Header.Set("Authorization", tt.authorization)
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestMachineOwnershipHTTPIsAuthenticatedExactAndNotFound(t *testing.T) {
	store := fabric.NewMemoryOperationStore()
	releasedAt := time.Now().UTC().Truncate(time.Second)
	ownership := fabric.MachineOwnership{
		ID: "owner-alpha", ResourceID: "compute-alpha", AccountID: "acct-alpha", PackageID: "basic",
		NodePoolID: "np-basic", MachineID: "machine-alpha", InstanceID: "ins-alpha", NodeName: "node-alpha",
		Status: "released", ClaimedAt: releasedAt.Add(-time.Minute), ReleasedAt: &releasedAt,
	}
	if _, _, err := store.ClaimMachine(context.Background(), ownership); err != nil {
		t.Fatal(err)
	}
	active := fabric.MachineOwnership{
		ID: "owner-active", ResourceID: "compute-active", AccountID: "acct-alpha", PackageID: "basic",
		NodePoolID: "np-basic", MachineID: "machine-active", InstanceID: "ins-active", NodeName: "node-active",
		Status: "active", ClaimedAt: releasedAt,
	}
	if _, _, err := store.ClaimMachine(context.Background(), active); err != nil {
		t.Fatal(err)
	}
	server := NewServer(fabric.NewServiceWithOperationStore(testProvider{}, store), "internal-secret")

	unauthorized := httptest.NewRecorder()
	server.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/fabric/machine-ownerships/compute-alpha", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, testRequest(http.MethodGet, "/fabric/machine-ownerships/compute-alpha", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got fabric.MachineOwnership
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.ResourceID != ownership.ResourceID || got.AccountID != ownership.AccountID || got.MachineID != ownership.MachineID ||
		got.InstanceID != ownership.InstanceID || got.NodeName != ownership.NodeName || got.Status != "released" ||
		got.ReleasedAt == nil || !got.ReleasedAt.Equal(releasedAt) {
		t.Fatalf("ownership = %#v", got)
	}
	activeRec := httptest.NewRecorder()
	server.ServeHTTP(activeRec, testRequest(http.MethodGet, "/fabric/machine-ownerships/compute-active", nil))
	if activeRec.Code != http.StatusOK || !strings.Contains(activeRec.Body.String(), `"status":"active"`) || strings.Contains(activeRec.Body.String(), `"releasedAt"`) {
		t.Fatalf("active status=%d body=%s", activeRec.Code, activeRec.Body.String())
	}

	missing := httptest.NewRecorder()
	server.ServeHTTP(missing, testRequest(http.MethodGet, "/fabric/machine-ownerships/compute-missing", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status=%d body=%s", missing.Code, missing.Body.String())
	}
}

func testRequest(method, path string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Authorization", "Bearer internal-secret")
	return req
}

func createReadyCompute(t *testing.T, service *fabric.Service, server http.Handler, accountID, workspaceID, key string) fabric.ComputeAllocation {
	t.Helper()
	request := testRequest(http.MethodPost, "/fabric/compute-allocations", bytes.NewBufferString(fmt.Sprintf(`{"accountId":%q,"workspaceId":%q,"packageId":"basic"}`, accountID, workspaceID)))
	request.Header.Set("Idempotency-Key", key)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("create compute status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var created fabric.ComputeAllocation
	if err := json.NewDecoder(recorder.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if current, ok := service.GetComputeAllocation(context.Background(), created.ID); ok && current.Status == "running" {
			return current
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("compute %s did not become ready", created.ID)
	return fabric.ComputeAllocation{}
}

func TestServerDestroysWorkspaceRuntime(t *testing.T) {
	server := NewServer(fabric.NewService(testProvider{}), "internal-secret")
	req := httptest.NewRequest(http.MethodPost, "/fabric/workspace-runtimes/workspace-alpha/destroy", nil)
	req.Header.Set("Authorization", "Bearer internal-secret")
	req.Header.Set("Idempotency-Key", "runtime-destroy-once")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"status":"destroyed"`) || !strings.Contains(rec.Body.String(), `"workspaceId":"workspace-alpha"`) {
		t.Fatalf("destroy status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestServerWritesGatewaySecretWithoutReturningRawKey(t *testing.T) {
	server := NewServer(fabric.NewService(testProvider{}), "internal-secret")
	req := testRequest(http.MethodPost, "/fabric/gateway-secrets", bytes.NewBufferString(`{"accountId":"acct-alpha","gatewayApiKey":"raw-gateway-key"}`))
	req.Header.Set("Idempotency-Key", "gateway-once")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted || strings.Contains(rec.Body.String(), "raw-gateway-key") {
		t.Fatalf("gateway secret status=%d body=%s", rec.Code, rec.Body.String())
	}
	var secret fabric.GatewaySecret
	if err := json.NewDecoder(rec.Body).Decode(&secret); err != nil || secret.SecretRef == "" || secret.Version == "" || secret.Fingerprint == "" {
		t.Fatalf("gateway secret=%#v err=%v", secret, err)
	}
}

func TestServerMonthlyPreflightNeedsNoIdempotencyKeyAndRecordsNoOperation(t *testing.T) {
	store := fabric.NewMemoryOperationStore()
	server := NewServer(fabric.NewServiceWithOperationStore(testProvider{}, store), "internal-secret")
	req := testRequest(http.MethodPost, "/fabric/monthly-preflight", bytes.NewBufferString(`{"resourceType":"storage","packageId":"basic","sizeGb":10,"zone":"na-siliconvalley-1"}`))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("preflight status=%d body=%s", rec.Code, rec.Body.String())
	}
	var result fabric.MonthlyPreflight
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil || result.ResourceType != "storage" || result.PackageID != "basic" || result.SizeGB != 10 || result.Zone != "na-siliconvalley-1" || !result.Available || result.ChargeType != "PREPAID" || result.PeriodMonths != 1 || result.RenewFlag != "NOTIFY_AND_MANUAL_RENEW" || result.ProviderPriceCNY <= 0 || len(result.ProviderRequestIDs) == 0 {
		t.Fatalf("preflight=%#v err=%v", result, err)
	}
	operations := httptest.NewRecorder()
	server.ServeHTTP(operations, testRequest(http.MethodGet, "/fabric/operations", nil))
	if operations.Code != http.StatusOK || strings.TrimSpace(operations.Body.String()) != "[]" {
		t.Fatalf("operations status=%d body=%s", operations.Code, operations.Body.String())
	}
}

type unavailablePreflightProvider struct{ testProvider }

func (unavailablePreflightProvider) MonthlyPreflight(context.Context, fabric.MonthlyPreflightInput) (fabric.MonthlyPreflight, error) {
	return fabric.MonthlyPreflight{}, errors.New("private Tencent response")
}

func TestServerMonthlyPreflightFailsClosedWithStableErrors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider fabric.Provider
		body     string
		want     int
		message  string
	}{
		{name: "invalid compute size", provider: testProvider{}, body: `{"resourceType":"compute","packageId":"basic","sizeGb":10,"zone":"na-siliconvalley-1"}`, want: http.StatusBadRequest, message: "invalid_monthly_preflight"},
		{name: "provider unavailable", provider: unavailablePreflightProvider{}, body: `{"resourceType":"storage","packageId":"basic","sizeGb":10,"zone":"na-siliconvalley-1"}`, want: http.StatusServiceUnavailable, message: "monthly_preflight_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := NewServer(fabric.NewService(tc.provider), "internal-secret")
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, testRequest(http.MethodPost, "/fabric/monthly-preflight", bytes.NewBufferString(tc.body)))
			if recorder.Code != tc.want || !strings.Contains(recorder.Body.String(), tc.message) || strings.Contains(recorder.Body.String(), "private Tencent response") {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestServerRenewsComputeAllocation(t *testing.T) {
	service := fabric.NewService(testProvider{})
	allocation, err := service.CreateComputeAllocation(context.Background(), fabric.ComputeAllocationInput{ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", IdempotencyKey: "compute-create"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if current, ok := service.GetComputeAllocation(context.Background(), allocation.ID); ok && current.Status == "running" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	server := NewServer(service, "internal-secret")
	req := testRequest(http.MethodPost, "/fabric/compute-allocations/compute-alpha/renew", bytes.NewBufferString(`{}`))
	req.Header.Set("Idempotency-Key", "compute-renew-once")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("renew status=%d body=%s", rec.Code, rec.Body.String())
	}
	var renewed fabric.ComputeAllocation
	if err := json.NewDecoder(rec.Body).Decode(&renewed); err != nil || renewed.Deadline != "2026-09-16T00:00:00Z" || renewed.ProviderData["renewalResult"] != "renewed" {
		t.Fatalf("renewed allocation=%#v err=%v", renewed, err)
	}
}

func TestTransferServiceFailureIsLogged(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })

	recorder := httptest.NewRecorder()
	writeTransferResult(recorder, http.StatusOK, fabric.Transfer{}, errors.New("workspace_content_digest_mismatch expected_sha256=abc actual_sha256=def"))

	if !strings.Contains(output.String(), "workspace_content_digest_mismatch expected_sha256=abc actual_sha256=def") {
		t.Fatalf("transfer failure log = %q", output.String())
	}
	output.Reset()
	writeTransferResult(httptest.NewRecorder(), http.StatusOK, fabric.Transfer{}, errors.New("database failed with private value"))
	if output.Len() != 0 {
		t.Fatalf("non-content failure log = %q", output.String())
	}
}

func TestRuntimeOperationConflictsAreHTTPConflict(t *testing.T) {
	for _, err := range []error{fabric.ErrRuntimeIdempotencyConflict, fabric.ErrRuntimeOperationInProgress, fabric.ErrRuntimeOperationFailed, fabric.ErrGatewaySecretIdempotencyConflict} {
		recorder := httptest.NewRecorder()
		writeResult(recorder, fabric.WorkspaceRuntime{}, err)
		if recorder.Code != http.StatusConflict {
			t.Fatalf("error %v status = %d, want %d", err, recorder.Code, http.StatusConflict)
		}
	}
}

func TestContentTransferHTTPResumesAndDownloads(t *testing.T) {
	server := NewServer(fabric.NewService(testProvider{}), "internal-secret")
	body := []byte("workspace bytes")
	digest := fmt.Sprintf("%x", sha256.Sum256(body))
	create := testRequest(http.MethodPost, "/fabric/transfers", bytes.NewBufferString(fmt.Sprintf(`{"organizationId":"org-alpha","workspaceId":"workspace-alpha","projectId":"project-alpha","path":"inputs/a.txt","digest":"%s","size":%d,"chunkSize":%d}`, digest, len(body), len(body))))
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

	put := testRequest(http.MethodPut, "/fabric/transfers/"+transfer.TransferID+"/chunks/0", bytes.NewReader(body))
	put.Header.Set("X-Chunk-SHA256", digest)
	putRec := httptest.NewRecorder()
	server.ServeHTTP(putRec, put)
	if putRec.Code != http.StatusOK {
		t.Fatalf("put status=%d body=%s", putRec.Code, putRec.Body.String())
	}
	complete := testRequest(http.MethodPost, "/fabric/transfers/"+transfer.TransferID+"/complete", nil)
	completeRec := httptest.NewRecorder()
	server.ServeHTTP(completeRec, complete)
	if completeRec.Code != http.StatusOK {
		t.Fatalf("complete status=%d body=%s", completeRec.Code, completeRec.Body.String())
	}
	download := testRequest(http.MethodGet, "/fabric/contents/"+digest, nil)
	download.Header.Set("X-Workspace-ID", "workspace-alpha")
	downloadRec := httptest.NewRecorder()
	server.ServeHTTP(downloadRec, download)
	downloaded, _ := io.ReadAll(downloadRec.Body)
	if downloadRec.Code != http.StatusOK || !bytes.Equal(downloaded, body) {
		t.Fatalf("download status=%d body=%q", downloadRec.Code, downloaded)
	}
}

func TestCatalogHTTP(t *testing.T) {
	server := NewServer(fabric.NewService(testProvider{}), "internal-secret")
	req := testRequest(http.MethodGet, "/fabric/catalog", nil)
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

func TestStorageSnapshotHTTPCreateRestoreAndDestroy(t *testing.T) {
	service := fabric.NewService(testProvider{})
	server := NewServer(service, "internal-secret")
	compute := createReadyCompute(t, service, server, "acct-alpha", "ws-alpha", "snapshot-compute")
	createVolume := testRequest(http.MethodPost, "/fabric/storage-volumes", bytes.NewBufferString(fmt.Sprintf(`{"id":"vol-source","accountId":"acct-alpha","workspaceId":"ws-alpha","computeId":%q,"zone":"ap-guangzhou-3","sizeGb":10}`, compute.ID)))
	createVolume.Header.Set("Idempotency-Key", "volume-once")
	volumeRec := httptest.NewRecorder()
	server.ServeHTTP(volumeRec, createVolume)

	create := testRequest(http.MethodPost, "/fabric/storage-snapshots", bytes.NewBufferString(`{"accountId":"acct-alpha","workspaceId":"ws-alpha","volumeId":"vol-source"}`))
	create.Header.Set("Idempotency-Key", "snapshot-once")
	createdRec := httptest.NewRecorder()
	server.ServeHTTP(createdRec, create)
	if createdRec.Code != http.StatusAccepted {
		t.Fatalf("create status=%d body=%s", createdRec.Code, createdRec.Body.String())
	}
	var snapshot fabric.StorageSnapshot
	if err := json.NewDecoder(createdRec.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	getRec := httptest.NewRecorder()
	server.ServeHTTP(getRec, testRequest(http.MethodGet, "/fabric/storage-snapshots/"+snapshot.ID, nil))
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", getRec.Code, getRec.Body.String())
	}
	restore := testRequest(http.MethodPost, "/fabric/storage-snapshots/"+snapshot.ID+"/restore", bytes.NewBufferString(`{"accountId":"acct-alpha","workspaceId":"ws-restored","targetVolumeId":"vol-restored"}`))
	restore.Header.Set("Idempotency-Key", "restore-once")
	restoreRec := httptest.NewRecorder()
	server.ServeHTTP(restoreRec, restore)
	if restoreRec.Code != http.StatusAccepted {
		t.Fatalf("restore status=%d body=%s", restoreRec.Code, restoreRec.Body.String())
	}
	destroy := testRequest(http.MethodPost, "/fabric/storage-snapshots/"+snapshot.ID+"/destroy", nil)
	destroy.Header.Set("Idempotency-Key", "destroy-once")
	destroyRec := httptest.NewRecorder()
	server.ServeHTTP(destroyRec, destroy)
	if destroyRec.Code != http.StatusAccepted {
		t.Fatalf("destroy status=%d body=%s", destroyRec.Code, destroyRec.Body.String())
	}
}

func TestCreateComputeAllocationHTTPRequiresIdempotencyKey(t *testing.T) {
	server := NewServer(fabric.NewService(testProvider{}), "internal-secret")
	body := bytes.NewBufferString(`{"accountId":"acct-alpha","workspaceId":"ws-alpha","packageId":"basic","dryRun":true}`)
	req := testRequest(http.MethodPost, "/fabric/compute-allocations", body)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestResourceBoundaryHTTPReturnsBadRequest(t *testing.T) {
	server := NewServer(fabric.NewService(testProvider{}), "internal-secret")
	for _, tc := range []struct{ name, path, body string }{
		{name: "package", path: "/fabric/compute-allocations", body: `{"accountId":"acct-alpha","workspaceId":"ws-alpha","packageId":"enterprise"}`},
		{name: "storage", path: "/fabric/storage-volumes", body: `{"accountId":"acct-alpha","workspaceId":"ws-alpha","sizeGb":15}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := testRequest(http.MethodPost, tc.path, bytes.NewBufferString(tc.body))
			req.Header.Set("Idempotency-Key", "invalid-"+tc.name)
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestSyncComputeAllocationHTTPWaitsForMachineOwnership(t *testing.T) {
	service := fabric.NewService(testProvider{})
	server := NewServer(service, "internal-secret")
	create := testRequest(http.MethodPost, "/fabric/compute-allocations", bytes.NewBufferString(`{"accountId":"acct-alpha","workspaceId":"ws-alpha","packageId":"basic"}`))
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

	req := testRequest(http.MethodPost, "/fabric/compute-allocations/"+created.ID+"/sync", bytes.NewBufferString(`{}`))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("sync status = %d, want %d: %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var allocation fabric.ComputeAllocation
	if err := json.NewDecoder(rec.Body).Decode(&allocation); err != nil {
		t.Fatalf("decode sync: %v", err)
	}
	if allocation.Status != "provisioning" {
		t.Fatalf("sync before machine ownership = %#v", allocation)
	}
}

func TestSyncStorageVolumeHTTPRefreshesProviderState(t *testing.T) {
	service := fabric.NewService(testProvider{})
	server := NewServer(service, "internal-secret")
	compute := createReadyCompute(t, service, server, "acct-alpha", "ws-alpha", "sync-storage-compute")
	create := testRequest(http.MethodPost, "/fabric/storage-volumes", bytes.NewBufferString(fmt.Sprintf(`{"accountId":"acct-alpha","workspaceId":"ws-alpha","computeId":%q,"zone":"ap-guangzhou-3","sizeGb":10}`, compute.ID)))
	create.Header.Set("Idempotency-Key", "sync-http-storage")
	createRec := httptest.NewRecorder()
	server.ServeHTTP(createRec, create)
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want %d: %s", createRec.Code, http.StatusAccepted, createRec.Body.String())
	}
	var created fabric.StorageVolume
	if err := json.NewDecoder(createRec.Body).Decode(&created); err != nil || created.ID == "" {
		t.Fatalf("decode created storage: volume=%#v err=%v", created, err)
	}

	req := testRequest(http.MethodPost, "/fabric/storage-volumes/"+created.ID+"/sync", bytes.NewBufferString(`{}`))
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
	server := NewServer(service, "internal-secret")
	compute := createReadyCompute(t, service, server, "acct-alpha", "ws-alpha", "ops-storage-compute")

	create := testRequest(http.MethodPost, "/fabric/storage-volumes", bytes.NewBufferString(fmt.Sprintf(`{"accountId":"acct-alpha","workspaceId":"ws-alpha","computeId":%q,"zone":"ap-guangzhou-3","sizeGb":10}`, compute.ID)))
	create.Header.Set("Idempotency-Key", "http-ops-storage")
	createRec := httptest.NewRecorder()
	server.ServeHTTP(createRec, create)
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want %d: %s", createRec.Code, http.StatusAccepted, createRec.Body.String())
	}

	req := testRequest(http.MethodGet, "/fabric/operations", nil)
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
	server := NewServer(fabric.NewService(testProvider{}), "internal-secret")
	create := testRequest(http.MethodPost, "/fabric/jobs", bytes.NewBufferString(`{"organizationId":"org-alpha","workspaceId":"workspace-alpha","projectId":"project-alpha","taskId":"task-alpha","requestId":"request-alpha","approvalId":"approval-alpha","environmentRef":"environment-alpha"}`))
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

	get := testRequest(http.MethodGet, "/fabric/jobs/"+created.JobID, nil)
	getRec := httptest.NewRecorder()
	server.ServeHTTP(getRec, get)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want %d: %s", getRec.Code, http.StatusOK, getRec.Body.String())
	}

	cancel := testRequest(http.MethodPost, "/fabric/jobs/"+created.JobID+"/cancel", bytes.NewBufferString(`{}`))
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
	server := NewServer(fabric.NewService(testProvider{}), "internal-secret")
	req := testRequest(http.MethodGet, "/fabric/jobs/job-missing", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestJobHTTPRequiresCanonicalIdentity(t *testing.T) {
	server := NewServer(fabric.NewService(testProvider{}), "internal-secret")
	req := testRequest(http.MethodPost, "/fabric/jobs", bytes.NewBufferString(`{}`))
	req.Header.Set("Idempotency-Key", "invalid-job")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestRunnerJobHTTPCompletionLifecycle(t *testing.T) {
	server := NewServer(fabric.NewService(testProvider{}), "internal-secret")
	create := testRequest(http.MethodPost, "/fabric/jobs", bytes.NewBufferString(`{"organizationId":"org-alpha","workspaceId":"workspace-alpha","projectId":"project-alpha","taskId":"task-alpha","requestId":"request-alpha","approvalId":"approval-alpha"}`))
	create.Header.Set("Idempotency-Key", "http-runner-job")
	createRec := httptest.NewRecorder()
	server.ServeHTTP(createRec, create)
	var job fabric.Job
	if err := json.NewDecoder(createRec.Body).Decode(&job); err != nil {
		t.Fatalf("decode job: %v", err)
	}

	claim := testRequest(http.MethodPost, "/fabric/jobs/"+job.JobID+"/claim", bytes.NewBufferString(`{"runnerId":"runner-alpha"}`))
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

	heartbeat := testRequest(http.MethodPost, "/fabric/jobs/"+job.JobID+"/heartbeat", bytes.NewBufferString(`{"runnerId":"runner-alpha","leaseToken":"`+claimed.LeaseToken+`"}`))
	heartbeat.Header.Set("Idempotency-Key", "http-heartbeat")
	heartbeatRec := httptest.NewRecorder()
	server.ServeHTTP(heartbeatRec, heartbeat)
	if heartbeatRec.Code != http.StatusAccepted {
		t.Fatalf("heartbeat status = %d: %s", heartbeatRec.Code, heartbeatRec.Body.String())
	}

	complete := testRequest(http.MethodPost, "/fabric/jobs/"+job.JobID+"/complete", bytes.NewBufferString(`{"runnerId":"runner-alpha","leaseToken":"`+claimed.LeaseToken+`","artifactIds":["artifact-alpha"],"reviewIds":["review-alpha"]}`))
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
	server := NewServer(fabric.NewService(testProvider{}), "internal-secret")
	create := testRequest(http.MethodPost, "/fabric/jobs", bytes.NewBufferString(`{"organizationId":"org-alpha","workspaceId":"workspace-alpha","projectId":"project-alpha","taskId":"task-alpha","requestId":"request-alpha","approvalId":"approval-alpha"}`))
	create.Header.Set("Idempotency-Key", "http-fail-job")
	createRec := httptest.NewRecorder()
	server.ServeHTTP(createRec, create)
	var job fabric.Job
	_ = json.NewDecoder(createRec.Body).Decode(&job)
	claim := testRequest(http.MethodPost, "/fabric/jobs/"+job.JobID+"/claim", bytes.NewBufferString(`{"runnerId":"runner-alpha"}`))
	claim.Header.Set("Idempotency-Key", "http-fail-claim")
	claimRec := httptest.NewRecorder()
	server.ServeHTTP(claimRec, claim)
	var claimed fabric.Job
	_ = json.NewDecoder(claimRec.Body).Decode(&claimed)

	conflict := testRequest(http.MethodPost, "/fabric/jobs/"+job.JobID+"/heartbeat", bytes.NewBufferString(`{"runnerId":"runner-beta","leaseToken":"`+claimed.LeaseToken+`"}`))
	conflict.Header.Set("Idempotency-Key", "http-wrong-runner")
	conflictRec := httptest.NewRecorder()
	server.ServeHTTP(conflictRec, conflict)
	if conflictRec.Code != http.StatusConflict {
		t.Fatalf("lease conflict status = %d, want %d: %s", conflictRec.Code, http.StatusConflict, conflictRec.Body.String())
	}

	fail := testRequest(http.MethodPost, "/fabric/jobs/"+job.JobID+"/fail", bytes.NewBufferString(`{"runnerId":"runner-alpha","leaseToken":"`+claimed.LeaseToken+`","errorCode":"runner_failed"}`))
	fail.Header.Set("Idempotency-Key", "http-fail")
	failRec := httptest.NewRecorder()
	server.ServeHTTP(failRec, fail)
	if failRec.Code != http.StatusAccepted {
		t.Fatalf("fail status = %d: %s", failRec.Code, failRec.Body.String())
	}

	retry := testRequest(http.MethodPost, "/fabric/jobs/"+job.JobID+"/retry", nil)
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

func (testProvider) ReconcileComputePool(_ context.Context, input fabric.ComputePoolDemand) (fabric.ComputePoolState, error) {
	machines := make([]fabric.ProviderMachine, 0, input.DesiredReplicas)
	for index := int64(0); index < input.DesiredReplicas; index++ {
		id := fmt.Sprintf("%s-%03d", input.PoolID, index+1)
		machines = append(machines, fabric.ProviderMachine{MachineID: id, InstanceID: "ins-" + id, NodeName: id, InstanceType: input.InstanceType, Zone: "ap-guangzhou-3", ChargeType: "PREPAID", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2026-08-16T00:00:00Z", Ready: true})
	}
	return fabric.ComputePoolState{PoolID: input.PoolID, NodePoolID: "np-" + input.PoolID, DesiredReplicas: input.DesiredReplicas, CurrentReplicas: input.DesiredReplicas, ProviderRequestID: "pool-test", Machines: machines}, nil
}

func (testProvider) MonthlyPreflight(_ context.Context, input fabric.MonthlyPreflightInput) (fabric.MonthlyPreflight, error) {
	requestIDs := map[string]string{"nodePool": "req-pool", "subnets": "req-subnets", "availability": "req-capacity"}
	price := 142.91
	if input.ResourceType == "storage" {
		requestIDs = map[string]string{"quota": "req-quota", "price": "req-price"}
		price = 7.5
	}
	return fabric.MonthlyPreflight{
		ResourceType: input.ResourceType, PackageID: input.PackageID, SizeGB: input.SizeGB, Zone: input.Zone,
		Available: true, ChargeType: "PREPAID", PeriodMonths: 1, RenewFlag: "NOTIFY_AND_MANUAL_RENEW", ProviderPriceCNY: price, ProviderRequestIDs: requestIDs,
	}, nil
}

func (testProvider) TagComputeMachine(_ context.Context, _ fabric.ProviderMachine, _ fabric.MachineOwnership) error {
	return nil
}

func (testProvider) DeleteComputeMachine(_ context.Context, _ fabric.ProviderMachine, _ fabric.MachineOwnership) error {
	return nil
}

func (testProvider) SyncComputeAllocation(_ context.Context, allocation fabric.ComputeAllocation) (fabric.ComputeAllocation, error) {
	allocation.Status = "running"
	return allocation, nil
}

func (testProvider) RenewComputeAllocation(_ context.Context, allocation fabric.ComputeAllocation) (fabric.ComputeAllocation, error) {
	allocation.Deadline = "2026-09-16T00:00:00Z"
	allocation.RenewFlag = "NOTIFY_AND_MANUAL_RENEW"
	allocation.ChargeType = "PREPAID"
	if allocation.ProviderData == nil {
		allocation.ProviderData = map[string]string{}
	}
	allocation.ProviderData["deadline"] = allocation.Deadline
	allocation.ProviderData["renewFlag"] = allocation.RenewFlag
	allocation.ProviderData["renewalResult"] = "renewed"
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

func (testProvider) RenewStorageVolume(_ context.Context, volume fabric.StorageVolume) (fabric.StorageVolume, error) {
	volume.Deadline = "2026-09-16T00:00:00Z"
	volume.RenewFlag = "NOTIFY_AND_MANUAL_RENEW"
	if volume.ProviderData == nil {
		volume.ProviderData = map[string]string{}
	}
	volume.ProviderData["diskChargeType"] = "PREPAID"
	volume.ProviderData["deadline"] = volume.Deadline
	volume.ProviderData["renewFlag"] = volume.RenewFlag
	volume.ProviderData["renewalResult"] = "renewed"
	return volume, nil
}

func (testProvider) DestroyStorageVolume(_ context.Context, volume fabric.StorageVolume) (fabric.StorageVolume, error) {
	volume.Status = "destroyed"
	return volume, nil
}

func (testProvider) CreateStorageSnapshot(_ context.Context, input fabric.StorageSnapshotInput, volume fabric.StorageVolume) (fabric.StorageSnapshot, error) {
	return fabric.StorageSnapshot{ID: "snap-http", AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, VolumeID: volume.ID, Status: "ready", Provider: "test", ProviderSnapshotRef: "volumesnapshot/snap-http", ProviderRequestID: "snapshot-request", SizeGB: volume.SizeGB, CreatedAt: time.Now().UTC()}, nil
}

func (testProvider) SyncStorageSnapshot(_ context.Context, snapshot fabric.StorageSnapshot) (fabric.StorageSnapshot, error) {
	return snapshot, nil
}

func (testProvider) RestoreStorageSnapshot(_ context.Context, input fabric.StorageRestoreInput, snapshot fabric.StorageSnapshot) (fabric.StorageVolume, error) {
	return fabric.StorageVolume{ID: input.TargetVolumeID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "ready", Provider: "test", ProviderResourceID: "pvc/" + input.TargetVolumeID, ProviderRequestID: "restore-request", SizeGB: snapshot.SizeGB, CreatedAt: time.Now().UTC()}, nil
}

func (testProvider) DestroyStorageSnapshot(_ context.Context, snapshot fabric.StorageSnapshot) (fabric.StorageSnapshot, error) {
	snapshot.Status = "destroyed"
	return snapshot, nil
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

func (testProvider) DestroyWorkspaceRuntime(_ context.Context, workspaceID string) (fabric.WorkspaceRuntime, error) {
	return fabric.WorkspaceRuntime{WorkspaceID: workspaceID, Status: "destroyed"}, nil
}

func (testProvider) WorkspaceRuntimeStatus(_ context.Context, workspaceID string) (fabric.WorkspaceRuntime, error) {
	return fabric.WorkspaceRuntime{WorkspaceID: workspaceID, Status: "not_found"}, nil
}

func (testProvider) UpsertGatewaySecret(_ context.Context, input fabric.GatewaySecretInput) (fabric.GatewaySecret, error) {
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(input.GatewayAPIKey)))
	return fabric.GatewaySecret{SecretRef: "opl-gateway-acct-alpha", Version: digest[:16], Fingerprint: "sha256:" + digest}, nil
}

func (testProvider) Readiness(_ context.Context) (map[string]any, error) {
	return map[string]any{"provider": "test", "ready": true}, nil
}
