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
	for _, err := range []error{fabric.ErrRuntimeIdempotencyConflict, fabric.ErrRuntimeOperationInProgress, fabric.ErrRuntimeOperationFailed} {
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

func TestVersionedCatalogAndPubMedHTTP(t *testing.T) {
	ncbi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/esearch.fcgi":
			_, _ = w.Write([]byte(`{"esearchresult":{"count":"1","idlist":["123"]}}`))
		case "/efetch.fcgi":
			_, _ = w.Write([]byte(`<PubmedArticleSet><PubmedArticle><MedlineCitation><PMID>123</PMID><Article><ArticleTitle>HTTP result</ArticleTitle><Journal><Title>Journal</Title><JournalIssue><PubDate><Year>2026</Year></PubDate></JournalIssue></Journal></Article></MedlineCitation></PubmedArticle></PubmedArticleSet>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ncbi.Close()
	service := fabric.NewServiceWithPubMed(testProvider{}, fabric.NewMemoryOperationStore(), ncbi.Client(), ncbi.URL)
	server := NewServer(service, "internal-secret")

	for _, tt := range []struct {
		path string
		body any
	}{
		{path: "/fabric/catalog/connectors", body: &[]fabric.Connector{}},
		{path: "/fabric/catalog/connectors/pubmed/versions/1.0.0", body: &fabric.Connector{}},
		{path: "/fabric/catalog/environment-templates", body: &[]fabric.EnvironmentTemplate{}},
		{path: "/fabric/catalog/environment-templates/python-minimal/versions/1.0.0", body: &fabric.EnvironmentTemplate{}},
		{path: "/fabric/catalog/connectors/pubmed/versions/1.0.0/query?q=cancer&page=1&pageSize=10", body: &fabric.PubMedResult{}},
	} {
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, testRequest(http.MethodGet, tt.path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", tt.path, rec.Code, rec.Body.String())
		}
		if err := json.NewDecoder(rec.Body).Decode(tt.body); err != nil {
			t.Fatalf("GET %s decode: %v", tt.path, err)
		}
	}

	bad := httptest.NewRecorder()
	server.ServeHTTP(bad, testRequest(http.MethodGet, "/fabric/catalog/connectors/pubmed/versions/1.0.0/query?q=x&pageSize=101", nil))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("invalid query status=%d body=%s", bad.Code, bad.Body.String())
	}
	missing := httptest.NewRecorder()
	server.ServeHTTP(missing, testRequest(http.MethodGet, "/fabric/catalog/connectors/missing/versions/1.0.0", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing connector status=%d body=%s", missing.Code, missing.Body.String())
	}
}

func TestStorageSnapshotHTTPCreateRestoreAndDestroy(t *testing.T) {
	service := fabric.NewService(testProvider{})
	server := NewServer(service, "internal-secret")
	createVolume := testRequest(http.MethodPost, "/fabric/storage-volumes", bytes.NewBufferString(`{"id":"vol-source","accountId":"acct-alpha","workspaceId":"ws-alpha","sizeGb":10}`))
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
	create := testRequest(http.MethodPost, "/fabric/storage-volumes", bytes.NewBufferString(`{"accountId":"acct-alpha","workspaceId":"ws-alpha","sizeGb":10}`))
	create.Header.Set("Idempotency-Key", "sync-http-storage")
	createRec := httptest.NewRecorder()
	server.ServeHTTP(createRec, create)
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want %d: %s", createRec.Code, http.StatusAccepted, createRec.Body.String())
	}

	req := testRequest(http.MethodPost, "/fabric/storage-volumes/vol-test/sync", bytes.NewBufferString(`{}`))
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

	create := testRequest(http.MethodPost, "/fabric/storage-volumes", bytes.NewBufferString(`{"accountId":"acct-alpha","workspaceId":"ws-alpha","sizeGb":10}`))
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
		machines = append(machines, fabric.ProviderMachine{MachineID: id, InstanceID: "ins-" + id, NodeName: id, InstanceType: input.InstanceType, Ready: true})
	}
	return fabric.ComputePoolState{PoolID: input.PoolID, NodePoolID: "np-" + input.PoolID, DesiredReplicas: input.DesiredReplicas, CurrentReplicas: input.DesiredReplicas, ProviderRequestID: "pool-test", Machines: machines}, nil
}

func (testProvider) TagComputeMachine(_ context.Context, _ fabric.ProviderMachine, _ fabric.MachineOwnership) error {
	return nil
}

func (testProvider) DeleteComputeMachine(_ context.Context, _ fabric.ProviderMachine, _ fabric.MachineOwnership) error {
	return nil
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

func (testProvider) Readiness(_ context.Context) (map[string]any, error) {
	return map[string]any{"provider": "test", "ready": true}, nil
}
