package http

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"opl-cloud/services/fabric/internal/fabric"
)

func TestMain(m *testing.M) {
	for key, value := range map[string]string{
		"OPL_BASIC_COMPUTE_INSTANCE_TYPE": "SA5.MEDIUM4",
		"OPL_PRO_COMPUTE_INSTANCE_TYPE":   "SA5.2XLARGE16",
	} {
		_ = os.Setenv(key, value)
	}
	os.Exit(m.Run())
}

type runtimeHealthSummaryHTTPProvider struct {
	testProvider
	calls int
}

type workspaceLaunchStageReadbackHTTPProvider struct {
	testProvider
	secret fabric.GatewaySecret
	reads  int
}

func (p *workspaceLaunchStageReadbackHTTPProvider) UpsertGatewaySecret(_ context.Context, input fabric.GatewaySecretInput) (fabric.GatewaySecret, error) {
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(input.GatewayAPIKey)))
	p.secret = fabric.GatewaySecret{Version: digest[:16], Fingerprint: "sha256:" + digest}
	return p.secret, nil
}

func (p *workspaceLaunchStageReadbackHTTPProvider) ReadGatewaySecretByDigest(_ context.Context, input fabric.GatewaySecretReadbackInput) (fabric.GatewaySecret, error) {
	p.reads++
	if input.SecretRef != p.secret.SecretRef || input.Fingerprint != p.secret.Fingerprint || "sha256:"+input.KeyDigest != p.secret.Fingerprint {
		return fabric.GatewaySecret{}, errors.New("gateway_secret_readback_mismatch")
	}
	return p.secret, nil
}

type failWorkspaceLaunchStageHTTPStore struct {
	fabric.OperationStore
	failed bool
}

func (s *failWorkspaceLaunchStageHTTPStore) SaveRuntime(ctx context.Context, operation fabric.FabricOperation) error {
	if !s.failed {
		s.failed = true
		return errors.New("injected runtime save failure")
	}
	return s.OperationStore.SaveRuntime(ctx, operation)
}

func (s *failWorkspaceLaunchStageHTTPStore) ConvergeRuntimeReadback(ctx context.Context, expected, next fabric.FabricOperation) error {
	converger, ok := s.OperationStore.(interface {
		ConvergeRuntimeReadback(context.Context, fabric.FabricOperation, fabric.FabricOperation) error
	})
	if !ok {
		return errors.New("runtime readback convergence unavailable")
	}
	return converger.ConvergeRuntimeReadback(ctx, expected, next)
}

func TestWorkspaceLaunchStageReadbackHTTPSeparatesProofAndCAS(t *testing.T) {
	provider := &workspaceLaunchStageReadbackHTTPProvider{}
	store := fabric.NewMemoryOperationStore()
	service := fabric.NewServiceWithOperationStore(provider, &failWorkspaceLaunchStageHTTPStore{OperationStore: store})
	key := "gateway-key-http"
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
	input := fabric.GatewaySecretInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", WorkspaceAPIKeyID: 19, Fingerprint: "sha256:" + digest, GatewayAPIKey: key, IdempotencyKey: "launch-alpha:secret:gateway-secret"}
	if _, err := service.UpsertGatewaySecret(context.Background(), input); err == nil {
		t.Fatal("injected final SaveRuntime failure was not observed")
	}
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 1 || operations[0].Status != "started" {
		t.Fatalf("interrupted operation=%#v err=%v", operations, err)
	}
	provider.secret.SecretRef = operations[0].ResourceID
	request := fabric.WorkspaceLaunchStageReadbackInput{
		Stage: "secret", FabricRecordID: operations[0].ID, FabricOperationID: operations[0].OperationID,
		AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, IdempotencyKey: input.IdempotencyKey,
		RequestHash: operations[0].RequestHash, GatewaySecretRef: provider.secret.SecretRef,
		GatewaySecretFingerprint: provider.secret.Fingerprint, WorkspaceAPIKeyID: input.WorkspaceAPIKeyID,
	}
	server := NewServer(service, "internal-secret")
	proofBody, _ := json.Marshal(request)
	proofResponse := httptest.NewRecorder()
	server.ServeHTTP(proofResponse, testRequest(http.MethodPost, "/fabric/workspace-launch-stage-readback/proof", bytes.NewReader(proofBody)))
	if proofResponse.Code != http.StatusOK {
		t.Fatalf("proof status=%d body=%s", proofResponse.Code, proofResponse.Body.String())
	}
	var proof fabric.WorkspaceLaunchStageReadbackProof
	if json.Unmarshal(proofResponse.Body.Bytes(), &proof) != nil || !proof.Eligible || proof.BindingDigest == "" || proof.FabricOperationMutationCount != 0 || provider.reads != 1 {
		t.Fatalf("proof=%#v reads=%d", proof, provider.reads)
	}
	if strings.Contains(proofResponse.Body.String(), key) || strings.Contains(proofResponse.Body.String(), "keyDigest") {
		t.Fatalf("proof leaked Secret material: %s", proofResponse.Body.String())
	}
	afterProof, _ := store.List(context.Background())
	if !reflect.DeepEqual(afterProof, operations) {
		t.Fatalf("proof mutated operation: before=%#v after=%#v", operations, afterProof)
	}
	request.ExpectedBindingDigest = proof.BindingDigest
	convergeBody, _ := json.Marshal(request)
	convergeResponse := httptest.NewRecorder()
	server.ServeHTTP(convergeResponse, testRequest(http.MethodPost, "/fabric/workspace-launch-stage-readback/converge", bytes.NewReader(convergeBody)))
	if convergeResponse.Code != http.StatusOK {
		t.Fatalf("converge status=%d body=%s", convergeResponse.Code, convergeResponse.Body.String())
	}
	var converged fabric.WorkspaceLaunchStageReadbackProof
	if json.Unmarshal(convergeResponse.Body.Bytes(), &converged) != nil || converged.FabricOperationMutationCount != 1 || provider.reads != 2 {
		t.Fatalf("converged=%#v reads=%d", converged, provider.reads)
	}
}

func (p *runtimeHealthSummaryHTTPProvider) RuntimeHealthSummary(context.Context) (fabric.RuntimeHealthSummary, error) {
	p.calls++
	return fabric.RuntimeHealthSummary{Total: 1000, Ready: 999, Unready: 1}, nil
}

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

func TestRuntimeHealthSummaryHTTPIsAuthenticatedAndReadOnly(t *testing.T) {
	provider := &runtimeHealthSummaryHTTPProvider{}
	service := fabric.NewService(provider)
	server := NewServer(service, "internal-secret")

	unauthorized := httptest.NewRecorder()
	server.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/fabric/runtime-health-summary", nil))
	if unauthorized.Code != http.StatusUnauthorized || provider.calls != 0 {
		t.Fatalf("unauthorized status=%d calls=%d", unauthorized.Code, provider.calls)
	}

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, testRequest(http.MethodGet, "/fabric/runtime-health-summary", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("summary status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var summary fabric.RuntimeHealthSummary
	if err := json.NewDecoder(recorder.Body).Decode(&summary); err != nil || summary.Total != 1000 || summary.Ready != 999 || summary.Unready != 1 || provider.calls != 1 {
		t.Fatalf("summary=%#v err=%v calls=%d", summary, err, provider.calls)
	}
	operations, err := service.ListOperations(context.Background())
	if err != nil || len(operations) != 0 {
		t.Fatalf("read-only summary operations=%#v err=%v", operations, err)
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
	request := testRequest(http.MethodPost, "/fabric/compute-allocations", bytes.NewBufferString(fmt.Sprintf(`{"accountId":%q,"workspaceId":%q,"packageId":"basic","nodePoolId":"np-basic"}`, accountID, workspaceID)))
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
	req := testRequest(http.MethodPost, "/fabric/gateway-secrets", bytes.NewBufferString(`{"accountId":"acct-alpha","workspaceId":"ws-alpha","workspaceApiKeyId":19,"fingerprint":"sha256:12982dcaf26b60cde5b6b68b01556e591badb2768ac9b71525619cb4ebc646f0","gatewayApiKey":"raw-gateway-key"}`))
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

type monthlyPreflightReportHTTPProvider struct{ testProvider }

func (monthlyPreflightReportHTTPProvider) MonthlyPreflightReport(context.Context, fabric.MonthlyPreflightReportInput) (fabric.MonthlyPreflightReport, error) {
	items := make([]fabric.MonthlyPreflightStage, 0, 2)
	for _, stage := range []string{"launch_permission", "credentials"} {
		items = append(items, fabric.MonthlyPreflightStage{Stage: stage, Status: "passed", BlockedBy: []string{}, SafeFacts: map[string]any{}, DurationMS: 1})
	}
	return fabric.MonthlyPreflightReport{
		SchemaVersion: 1, Status: "passed", Zone: "na-siliconvalley-1", Items: items,
		Sub2APIMutationCount: 0, TencentMutationCount: 0, KubernetesMutationCount: 0,
	}, nil
}

func TestServerMonthlyPreflightReportIsInternalReadOnlyAndStrictJSON(t *testing.T) {
	server := NewServer(fabric.NewService(monthlyPreflightReportHTTPProvider{}), "internal-secret")
	unauthorized := httptest.NewRecorder()
	server.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/fabric/monthly-preflight-report?zone=na-siliconvalley-1", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, testRequest(http.MethodGet, "/fabric/monthly-preflight-report?zone=na-siliconvalley-1", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("report status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var report fabric.MonthlyPreflightReport
	decoder := json.NewDecoder(recorder.Body)
	if err := decoder.Decode(&report); err != nil {
		t.Fatal(err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatalf("report has trailing JSON: err=%v body=%s", err, recorder.Body.String())
	}
	if report.Status != "passed" || len(report.Items) != 2 || report.Sub2APIMutationCount != 0 || report.TencentMutationCount != 0 || report.KubernetesMutationCount != 0 {
		t.Fatalf("report=%#v", report)
	}

	legacy := httptest.NewRecorder()
	server.ServeHTTP(legacy, testRequest(http.MethodGet, "/fabric/monthly-preflight-report?packageId=basic&sizeGb=10&zone=na-siliconvalley-1", nil))
	if legacy.Code != http.StatusBadRequest {
		t.Fatalf("legacy diagnostics query status=%d body=%s", legacy.Code, legacy.Body.String())
	}
}

type monthlyTruthHTTPProvider struct {
	testProvider
	calls  int
	result fabric.MonthlyProviderTruth
}

func (p *monthlyTruthHTTPProvider) MonthlyProviderTruth(_ context.Context, compute fabric.ComputeAllocation, storage fabric.StorageVolume) (fabric.MonthlyProviderTruth, error) {
	p.calls++
	result := p.result
	result.Compute.ID, result.Compute.AccountID, result.Compute.WorkspaceID = compute.ID, compute.AccountID, compute.WorkspaceID
	result.Storage.ID, result.Storage.AccountID, result.Storage.WorkspaceID = storage.ID, storage.AccountID, storage.WorkspaceID
	return result, nil
}

func monthlyTruthHTTPFixture(t *testing.T, provider *monthlyTruthHTTPProvider) (*fabric.Service, *fabric.MemoryOperationStore) {
	t.Helper()
	tags := func(accountID, workspaceID, resourceID, operationID string) map[string]string {
		return map[string]string{"opl_account_id": accountID, "opl_workspace_id": workspaceID, "opl_resource_id": resourceID, "opl_operation_id": operationID}
	}
	compute := fabric.ComputeAllocation{
		ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", Status: "running",
		Provider: "tencent-tke", ProviderResourceID: "machine/node-basic-1", ProviderRequestID: "req-compute",
		NodePoolID: "np-basic", InstanceID: "ins-basic-1", CVMInstanceID: "ins-basic-1", MachineName: "node-basic-1", PrivateIP: "10.0.0.11",
		InstanceType: "SA5.MEDIUM4", Zone: "ap-guangzhou-3", ChargeType: "PREPAID", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2026-08-16T00:00:00Z",
		ProviderData: map[string]string{"instanceType": "SA5.MEDIUM4", "zone": "ap-guangzhou-3"}, CostTags: tags("acct-alpha", "ws-alpha", "compute-alpha", "owner-compute-alpha"),
	}
	storage := fabric.StorageVolume{
		ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "ready", Provider: "tencent-tke",
		ProviderResourceID: "disk-storage-alpha", ProviderRequestID: "req-storage", SizeGB: 10, CBSStatus: "ATTACHED", DiskType: "CLOUD_BSSD",
		RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2026-08-16T00:00:00Z", Zone: "ap-guangzhou-3", CostTags: tags("acct-alpha", "ws-alpha", "storage-alpha", "owner-storage-alpha"),
	}
	provider.result = fabric.MonthlyProviderTruth{
		ComputeState: "ready", StorageState: "absent", ProviderRequestID: "req-provider-truth",
		Compute: compute,
		Storage: fabric.StorageVolume{
			ID: storage.ID, AccountID: storage.AccountID, WorkspaceID: storage.WorkspaceID, Status: "external_deleted", Provider: storage.Provider,
			ProviderResourceID: storage.ProviderResourceID, ProviderRequestID: "req-provider-truth", SizeGB: storage.SizeGB, CBSStatus: "NOT_FOUND",
			DiskType: storage.DiskType, RenewFlag: storage.RenewFlag, Deadline: storage.Deadline, Zone: storage.Zone, CostTags: storage.CostTags,
		},
	}
	store := fabric.NewMemoryOperationStore()
	now := time.Now().UTC()
	for _, operation := range []fabric.FabricOperation{
		{ID: "fop-compute", Action: "create_compute_allocation", ResourceKind: "compute_allocation", ResourceID: compute.ID, Status: "succeeded", RedactedProviderPayload: map[string]any{"resource": compute}, CreatedAt: now},
		{ID: "fop-storage", Action: "create_storage_volume", ResourceKind: "storage_volume", ResourceID: storage.ID, Status: "succeeded", RedactedProviderPayload: map[string]any{"resource": storage}, CreatedAt: now},
	} {
		if err := store.Append(context.Background(), operation); err != nil {
			t.Fatal(err)
		}
	}
	return fabric.NewServiceWithOperationStore(provider, store), store
}

func TestMonthlyProviderTruthHTTPIsAuthenticatedReadOnlyAndValidatesQuery(t *testing.T) {
	provider := &monthlyTruthHTTPProvider{}
	service, store := monthlyTruthHTTPFixture(t, provider)
	server := NewServer(service, "internal-secret")
	path := "/fabric/monthly-provider-truth?computeAllocationId=compute-alpha&storageVolumeId=storage-alpha"

	unauthorized := httptest.NewRecorder()
	server.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, path, nil))
	if unauthorized.Code != http.StatusUnauthorized || provider.calls != 0 {
		t.Fatalf("unauthorized status=%d calls=%d", unauthorized.Code, provider.calls)
	}

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, testRequest(http.MethodGet, path, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("truth status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var truth fabric.MonthlyProviderTruth
	if err := json.NewDecoder(recorder.Body).Decode(&truth); err != nil || truth.ComputeState != "ready" || truth.StorageState != "absent" || truth.Compute.ID != "compute-alpha" || truth.Storage.ID != "storage-alpha" || provider.calls != 1 {
		t.Fatalf("truth=%#v err=%v calls=%d", truth, err, provider.calls)
	}
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 2 {
		t.Fatalf("read-only truth operations=%#v err=%v", operations, err)
	}

	for _, invalidPath := range []string{
		"/fabric/monthly-provider-truth?storageVolumeId=storage-alpha",
		"/fabric/monthly-provider-truth?computeAllocationId=compute-alpha",
		"/fabric/monthly-provider-truth?computeAllocationId=compute-alpha&computeAllocationId=compute-other&storageVolumeId=storage-alpha",
		"/fabric/monthly-provider-truth?computeAllocationId=compute-alpha&storageVolumeId=storage-missing",
	} {
		t.Run(invalidPath, func(t *testing.T) {
			invalid := httptest.NewRecorder()
			server.ServeHTTP(invalid, testRequest(http.MethodGet, invalidPath, nil))
			if (strings.Contains(invalidPath, "storage-missing") && invalid.Code != http.StatusServiceUnavailable) || (!strings.Contains(invalidPath, "storage-missing") && invalid.Code != http.StatusBadRequest) {
				t.Fatalf("invalid query status=%d body=%s", invalid.Code, invalid.Body.String())
			}
		})
	}
	if provider.calls != 1 {
		t.Fatalf("invalid query or local identity reached provider: calls=%d", provider.calls)
	}
}

type workspaceActivationTruthHTTPProvider struct {
	testProvider
	calls int
}

func (p *workspaceActivationTruthHTTPProvider) WorkspaceActivationTruth(_ context.Context, input fabric.WorkspaceActivationTruthInput, compute fabric.ComputeAllocation, storage fabric.StorageVolume, attachment fabric.StorageAttachment) (fabric.WorkspaceActivationTruth, error) {
	p.calls++
	return fabric.WorkspaceActivationTruth{
		SchemaVersion: 1, Ready: true, Reason: "none", ComputeState: "ready", StorageState: "ready",
		Compute: compute, Storage: storage, Attachment: attachment,
		Runtime: fabric.WorkspaceActivationRuntimeTruth{
			ID: input.RuntimeID, OperationID: input.RuntimeOperationID, ServiceName: input.ServiceName,
			DeploymentName: input.ServiceName, GatewaySecretRef: input.GatewaySecretRef,
		},
		Checks: []fabric.Check{},
	}, nil
}

func workspaceActivationTruthHTTPFixture(t *testing.T, provider *workspaceActivationTruthHTTPProvider) (*fabric.Service, *fabric.MemoryOperationStore, fabric.WorkspaceActivationTruthInput) {
	t.Helper()
	launchID := "workspace-launch-alpha"
	compute := fabric.ComputeAllocation{
		ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", Status: "running", Provider: "tencent-tke",
		ProviderResourceID: "machine/node-alpha", ProviderRequestID: "req-compute", NodePoolID: "np-basic", InstanceID: "ins-alpha", CVMInstanceID: "ins-alpha",
		MachineName: "node-alpha", NodeName: "10.0.0.8", PrivateIP: "10.0.0.8", InstanceType: "SA5.MEDIUM4", Zone: "ap-guangzhou-3",
		CostTags: map[string]string{"opl_operation_id": "owner-alpha"},
	}
	storage := fabric.StorageVolume{
		ID: "storage-alpha", AccountID: compute.AccountID, WorkspaceID: compute.WorkspaceID, Status: "ready", Provider: "tencent-tke",
		ProviderResourceID: "disk-alpha", ProviderRequestID: "req-storage", SizeGB: 10, DiskType: "CLOUD_BSSD", Zone: compute.Zone,
	}
	attachment := fabric.StorageAttachment{
		ID: "att-alpha", WorkspaceID: compute.WorkspaceID, ComputeID: compute.ID, VolumeID: storage.ID, Status: "attached", Provider: "tencent-tke",
		ProviderAttachmentID: "pv/opl-storage-alpha-pv:pvc/opl-storage-alpha-data", ProviderRequestID: "req-attachment",
	}
	runtime := fabric.WorkspaceRuntime{ID: "rt-alpha", WorkspaceID: compute.WorkspaceID, Status: "running", ServiceName: "opl-compute-alpha", Ready: true, ProviderRequestID: "req-runtime"}
	store := fabric.NewMemoryOperationStore()
	now := time.Now().UTC()
	for _, operation := range []fabric.FabricOperation{
		{ID: "fop-compute", OperationID: "op-internal-compute", Action: "create_compute_allocation", ResourceKind: "compute_allocation", ResourceID: compute.ID, AccountID: compute.AccountID, WorkspaceID: compute.WorkspaceID, IdempotencyKey: launchID + ":compute", Status: "succeeded", RedactedProviderPayload: map[string]any{"resource": compute}, CreatedAt: now},
		{ID: "fop-storage", OperationID: "op-internal-storage", Action: "create_storage_volume", ResourceKind: "storage_volume", ResourceID: storage.ID, AccountID: storage.AccountID, WorkspaceID: storage.WorkspaceID, IdempotencyKey: launchID + ":storage", Status: "succeeded", RedactedProviderPayload: map[string]any{"resource": storage}, CreatedAt: now.Add(time.Second)},
		{ID: "fop-attachment", OperationID: "op-internal-attachment", Action: "create_storage_attachment", ResourceKind: "storage_attachment", ResourceID: attachment.ID, AccountID: compute.AccountID, WorkspaceID: compute.WorkspaceID, IdempotencyKey: launchID + ":attachment", Status: "succeeded", RedactedProviderPayload: map[string]any{"resource": attachment}, CreatedAt: now.Add(2 * time.Second)},
		{ID: "fop-runtime", OperationID: "op-internal-runtime", Action: "create_workspace_runtime", ResourceKind: "workspace_runtime", ResourceID: compute.WorkspaceID, AccountID: compute.AccountID, WorkspaceID: compute.WorkspaceID, IdempotencyKey: launchID + ":workspace:runtime", Status: "succeeded", RedactedProviderPayload: map[string]any{"resource": runtime}, CreatedAt: now.Add(3 * time.Second)},
	} {
		if err := store.Append(context.Background(), operation); err != nil {
			t.Fatal(err)
		}
	}
	if _, created, err := store.ClaimMachine(context.Background(), fabric.MachineOwnership{
		ID: "owner-alpha", ResourceID: compute.ID, AccountID: compute.AccountID, WorkspaceID: compute.WorkspaceID,
		PackageID: compute.PackageID, NodePoolID: compute.NodePoolID, MachineID: compute.MachineName,
		InstanceID: compute.CVMInstanceID, NodeName: compute.NodeName, Status: "active", ClaimedAt: now,
	}); err != nil || !created {
		t.Fatalf("seed machine ownership: created=%v err=%v", created, err)
	}
	input := fabric.WorkspaceActivationTruthInput{
		LaunchOperationID: launchID, AccountID: compute.AccountID, WorkspaceID: compute.WorkspaceID,
		ComputeAllocationID: compute.ID, ComputeOperationID: launchID + ":compute",
		StorageVolumeID: storage.ID, StorageOperationID: launchID + ":storage",
		AttachmentID: attachment.ID, AttachmentOperationID: launchID + ":attachment",
		RuntimeID: runtime.ID, RuntimeOperationID: launchID + ":workspace:runtime", ServiceName: runtime.ServiceName,
		WorkspaceImageDigest: "registry.example/one-person-lab-app@sha256:" + strings.Repeat("f", 64),
		GatewaySecretRef:     "opl-gateway-alpha", WorkspaceAPIKeyID: 42, GatewaySecretFingerprint: "sha256:" + strings.Repeat("e", 64),
	}
	return fabric.NewServiceWithOperationStore(provider, store), store, input
}

func TestWorkspaceActivationTruthHTTPIsAuthenticatedStructuredAndReadOnly(t *testing.T) {
	provider := &workspaceActivationTruthHTTPProvider{}
	service, store, input := workspaceActivationTruthHTTPFixture(t, provider)
	server := NewServer(service, "internal-secret")
	body, _ := json.Marshal(input)

	unauthorized := httptest.NewRecorder()
	server.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/fabric/workspace-activation-truth", bytes.NewReader(body)))
	if unauthorized.Code != http.StatusUnauthorized || provider.calls != 0 {
		t.Fatalf("unauthorized status=%d calls=%d", unauthorized.Code, provider.calls)
	}

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, testRequest(http.MethodPost, "/fabric/workspace-activation-truth", bytes.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("truth status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var truth fabric.WorkspaceActivationTruth
	if err := json.NewDecoder(recorder.Body).Decode(&truth); err != nil || !truth.Ready || truth.Runtime.ID != input.RuntimeID || provider.calls != 1 {
		t.Fatalf("truth=%#v err=%v calls=%d", truth, err, provider.calls)
	}
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 4 {
		t.Fatalf("read-only truth operations=%#v err=%v", operations, err)
	}

	input.RuntimeOperationID = input.LaunchOperationID + ":workspace:runtime-other"
	invalidBody, _ := json.Marshal(input)
	invalid := httptest.NewRecorder()
	server.ServeHTTP(invalid, testRequest(http.MethodPost, "/fabric/workspace-activation-truth", bytes.NewReader(invalidBody)))
	if invalid.Code != http.StatusBadRequest || provider.calls != 1 {
		t.Fatalf("invalid status=%d body=%s calls=%d", invalid.Code, invalid.Body.String(), provider.calls)
	}
	var blocked fabric.WorkspaceActivationTruth
	if err := json.NewDecoder(invalid.Body).Decode(&blocked); err != nil || blocked.Ready || blocked.Reason == "" || blocked.ErrorClass == "" {
		t.Fatalf("blocked=%#v err=%v", blocked, err)
	}
}

type computeClaimHTTPProvider struct {
	testProvider
	proof      fabric.ComputeClaimProviderProof
	claim      fabric.ComputeClaimProviderClaim
	proofCalls int
	claimCalls int
}

func (p *computeClaimHTTPProvider) ProveComputeClaimRecovery(_ context.Context, _ fabric.ComputeAllocation, _ fabric.ComputeAllocationPreparation, _ fabric.MachineOwnership) (fabric.ComputeClaimProviderProof, error) {
	p.proofCalls++
	return p.proof, nil
}

func (p *computeClaimHTTPProvider) ClaimComputeRecovery(_ context.Context, _ fabric.ComputeAllocation, _ fabric.ComputeAllocationPreparation, _ fabric.MachineOwnership) (fabric.ComputeClaimProviderClaim, error) {
	p.claimCalls++
	return p.claim, nil
}

func (p *computeClaimHTTPProvider) DiscoverStorageRecovery(_ context.Context, _ fabric.StorageVolumeInput) (fabric.StorageRecoveryDiscovery, error) {
	return fabric.StorageRecoveryDiscovery{State: "storage_not_started", ProviderRequestID: "req-storage-discovery"}, nil
}

func computeClaimHTTPFixture(t *testing.T) (*fabric.Service, *fabric.MemoryOperationStore, *computeClaimHTTPProvider, fabric.ComputeClaimRecoveryInput) {
	t.Helper()
	input := fabric.ComputeClaimRecoveryInput{
		LaunchOperationID: "launch-fixture", AccountID: "acct-fixture", WorkspaceID: "ws-fixture",
		ComputeAllocationID: "ca-fixture", PackageID: "basic",
		PoolID: "pool-basic-2c4g", NodePoolID: "np-workspace-basic",
	}
	storageIdentity := sha1.New()
	for _, part := range []string{"vol", input.AccountID, input.LaunchOperationID + ":storage"} {
		_, _ = storageIdentity.Write([]byte(part))
		_, _ = storageIdentity.Write([]byte{0})
	}
	input.StorageVolumeID = "vol_" + hex.EncodeToString(storageIdentity.Sum(nil))[:18]
	plan := fabric.ComputeAllocationPreparation{
		PoolID: input.PoolID, PackageID: input.PackageID, NodePoolID: input.NodePoolID, InstanceType: "SA5.MEDIUM4",
		MaxReplicas: 10, BaselineReplicas: 1, TargetReplicas: 2, BeforeMachineNames: []string{"machine-before"},
	}
	allocation := fabric.ComputeAllocation{
		ID: input.ComputeAllocationID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, PackageID: input.PackageID,
		Status: "quarantined", Provider: "tencent-tke", ProviderResourceID: "ins-fixture", PoolID: input.PoolID, NodePoolID: input.NodePoolID,
		MachineName: "machine-after", InstanceID: "ins-fixture", CVMInstanceID: "ins-fixture", NodeName: "10.0.0.18", PrivateIP: "10.0.0.18",
		InstanceType: plan.InstanceType, Zone: "ap-guangzhou-3", ChargeType: "PREPAID", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2026-08-28T00:00:00Z",
		ProviderData: map[string]string{
			"instanceType": plan.InstanceType, "zone": "ap-guangzhou-3", "chargeType": "PREPAID", "periodMonths": "1",
			"renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": "2026-08-28T00:00:00Z", "machineName": "machine-after",
		},
	}
	ownership := fabric.MachineOwnership{
		ID: "owner-fixture", ResourceID: allocation.ID, AccountID: allocation.AccountID, WorkspaceID: allocation.WorkspaceID,
		PackageID: allocation.PackageID, NodePoolID: allocation.NodePoolID, MachineID: allocation.MachineName,
		InstanceID: allocation.InstanceID, NodeName: allocation.NodeName, Status: "quarantined", ClaimedAt: time.Now().UTC(),
	}
	operation := fabric.FabricOperation{
		ID: "fop-compute-fixture", OperationID: "op-compute-fixture", CallerService: "control-plane", Action: "create_compute_allocation",
		ResourceKind: "compute_allocation", ResourceID: allocation.ID, AccountID: allocation.AccountID, WorkspaceID: allocation.WorkspaceID,
		Provider: "tencent-tke", IdempotencyKey: input.LaunchOperationID + ":compute", RequestHash: "fixture-hash", Status: "failed", ComputePoolKey: input.NodePoolID,
		RedactedProviderPayload: map[string]any{"resource": allocation, "allocationPlan": plan}, CreatedAt: time.Now().UTC(), FinishedAt: time.Now().UTC(),
	}
	store := fabric.NewMemoryOperationStore()
	if err := store.Append(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := store.ClaimMachine(context.Background(), ownership); err != nil || !claimed {
		t.Fatalf("seed ownership: claimed=%v err=%v", claimed, err)
	}
	provider := &computeClaimHTTPProvider{proof: fabric.ComputeClaimProviderProof{
		Status: "proven", NodeOwnershipState: "unallocated", CVMOwnershipState: "recoverable", MachineName: allocation.MachineName,
		NodeName: allocation.NodeName, CVMInstanceID: allocation.InstanceID, PrivateIP: allocation.PrivateIP, InstanceType: allocation.InstanceType,
		Zone: allocation.Zone, ChargeType: "PREPAID", PeriodMonths: 1, RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: allocation.Deadline,
	}}
	provider.claim = fabric.ComputeClaimProviderClaim{
		Proof: provider.proof, TencentMutationCount: 1, KubernetesMutationCount: 1,
		Evidence: &fabric.ComputeClaimEvidence{
			CVM:  fabric.ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1},
			Node: fabric.ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1},
		},
	}
	provider.claim.Proof.NodeOwnershipState = "target_owned"
	provider.claim.Proof.CVMOwnershipState = "target_owned"
	return fabric.NewServiceWithOperationStore(provider, store), store, provider, input
}

func TestComputeClaimRecoveryHTTPSeparatesReadOnlyProofAndIdempotentClaim(t *testing.T) {
	service, store, provider, input := computeClaimHTTPFixture(t)
	server := NewServer(service, "internal-secret")
	proofBody, _ := json.Marshal(input)

	unauthorized := httptest.NewRecorder()
	server.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/fabric/compute-claim-recovery/proof", bytes.NewReader(proofBody)))
	if unauthorized.Code != http.StatusUnauthorized || provider.proofCalls != 0 || provider.claimCalls != 0 {
		t.Fatalf("unauthorized status=%d proofCalls=%d claimCalls=%d", unauthorized.Code, provider.proofCalls, provider.claimCalls)
	}

	proofRecorder := httptest.NewRecorder()
	server.ServeHTTP(proofRecorder, testRequest(http.MethodPost, "/fabric/compute-claim-recovery/proof", bytes.NewReader(proofBody)))
	var proof fabric.ComputeClaimRecoveryProof
	if proofRecorder.Code != http.StatusOK || json.NewDecoder(proofRecorder.Body).Decode(&proof) != nil || !proof.Eligible || proof.Reason != "none" ||
		proof.StorageState != "storage_not_started" || proof.Sub2APIMutationCount != 0 || proof.TencentMutationCount != 0 || proof.KubernetesMutationCount != 0 ||
		provider.proofCalls != 1 || provider.claimCalls != 0 {
		t.Fatalf("proof status=%d proof=%#v calls=%d/%d body=%s", proofRecorder.Code, proof, provider.proofCalls, provider.claimCalls, proofRecorder.Body.String())
	}
	operations, _ := store.List(context.Background())
	ownership, _ := store.MachineOwnership(context.Background(), input.ComputeAllocationID)
	if len(operations) != 1 || operations[0].Status != "failed" || ownership.Status != "quarantined" {
		t.Fatalf("read-only proof mutated state: operations=%#v ownership=%#v", operations, ownership)
	}

	claimInput := fabric.ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: proof.MachineName, NodeName: proof.NodeName, CVMInstanceID: proof.CVMInstanceID,
		PrivateIP: proof.PrivateIP, InstanceType: proof.InstanceType, Zone: proof.Zone,
	}
	claimBody, _ := json.Marshal(claimInput)
	missingKey := httptest.NewRecorder()
	server.ServeHTTP(missingKey, testRequest(http.MethodPost, "/fabric/compute-claim-recovery/claim", bytes.NewReader(claimBody)))
	if missingKey.Code != http.StatusBadRequest || provider.proofCalls != 1 || provider.claimCalls != 0 {
		t.Fatalf("missing key status=%d calls=%d/%d body=%s", missingKey.Code, provider.proofCalls, provider.claimCalls, missingKey.Body.String())
	}

	claimRequest := testRequest(http.MethodPost, "/fabric/compute-claim-recovery/claim", bytes.NewReader(claimBody))
	claimRequest.Header.Set("Idempotency-Key", "launch-fixture:compute")
	claimRecorder := httptest.NewRecorder()
	server.ServeHTTP(claimRecorder, claimRequest)
	claimPayload := append([]byte(nil), claimRecorder.Body.Bytes()...)
	var claimed fabric.ComputeClaimRecoveryProof
	if claimRecorder.Code != http.StatusAccepted || json.NewDecoder(claimRecorder.Body).Decode(&claimed) != nil || !claimed.Eligible ||
		claimed.NodeOwnershipState != "target_owned" || claimed.TencentMutationCount != 1 || claimed.KubernetesMutationCount != 1 ||
		provider.proofCalls != 2 || provider.claimCalls != 1 {
		t.Fatalf("claim status=%d claim=%#v calls=%d/%d body=%s", claimRecorder.Code, claimed, provider.proofCalls, provider.claimCalls, claimRecorder.Body.String())
	}
	var wire map[string]any
	if err := json.Unmarshal(claimPayload, &wire); err != nil {
		t.Fatal(err)
	}
	evidence, _ := wire["evidence"].(map[string]any)
	for _, kind := range []string{"cvm", "node"} {
		item, _ := evidence[kind].(map[string]any)
		if _, present := item["missing"]; present {
			t.Fatalf("successful Go HTTP evidence must exercise the omitempty wire shape: %s", claimPayload)
		}
	}
	operations, _ = store.List(context.Background())
	binding, _ := operations[0].RedactedProviderPayload["computeClaimRecovery"].(map[string]any)
	if len(operations) != 1 || binding["launchOperationId"] != input.LaunchOperationID ||
		binding["idempotencyKey"] != "launch-fixture:compute" || binding["targetHash"] == "" || binding["requestHash"] == "" {
		t.Fatalf("claim binding was not persisted on original operation: operations=%#v binding=%#v", operations, binding)
	}

	provider.proof.NodeOwnershipState = "target_owned"
	provider.proof.CVMOwnershipState = "target_owned"
	replayRequest := testRequest(http.MethodPost, "/fabric/compute-claim-recovery/claim", bytes.NewReader(claimBody))
	replayRequest.Header.Set("Idempotency-Key", "launch-fixture:compute")
	replayRecorder := httptest.NewRecorder()
	server.ServeHTTP(replayRecorder, replayRequest)
	var replayed fabric.ComputeClaimRecoveryProof
	if replayRecorder.Code != http.StatusAccepted || json.NewDecoder(replayRecorder.Body).Decode(&replayed) != nil || !replayed.Eligible ||
		replayed.TencentMutationCount != 0 || replayed.KubernetesMutationCount != 0 || provider.claimCalls != 1 {
		t.Fatalf("claim replay status=%d proof=%#v calls=%d/%d body=%s", replayRecorder.Code, replayed, provider.proofCalls, provider.claimCalls, replayRecorder.Body.String())
	}

	conflictRequest := testRequest(http.MethodPost, "/fabric/compute-claim-recovery/claim", bytes.NewReader(claimBody))
	conflictRequest.Header.Set("Idempotency-Key", "launch-fixture:recovery-key")
	conflictRecorder := httptest.NewRecorder()
	proofCallsBeforeConflict := provider.proofCalls
	claimCallsBeforeConflict := provider.claimCalls
	server.ServeHTTP(conflictRecorder, conflictRequest)
	if conflictRecorder.Code != http.StatusBadRequest || provider.proofCalls != proofCallsBeforeConflict || provider.claimCalls != claimCallsBeforeConflict {
		t.Fatalf("different claim key status=%d calls=%d/%d body=%s", conflictRecorder.Code, provider.proofCalls, provider.claimCalls, conflictRecorder.Body.String())
	}

	driftedInput := claimInput
	driftedInput.PrivateIP = "10.0.0.99"
	driftedBody, _ := json.Marshal(driftedInput)
	driftedRequest := testRequest(http.MethodPost, "/fabric/compute-claim-recovery/claim", bytes.NewReader(driftedBody))
	driftedRequest.Header.Set("Idempotency-Key", "launch-fixture:compute")
	driftedRecorder := httptest.NewRecorder()
	server.ServeHTTP(driftedRecorder, driftedRequest)
	if driftedRecorder.Code != http.StatusConflict || provider.claimCalls != 1 {
		t.Fatalf("claim target drift status=%d calls=%d/%d body=%s", driftedRecorder.Code, provider.proofCalls, provider.claimCalls, driftedRecorder.Body.String())
	}
}

func TestComputeProviderTruthHTTPIsAuthenticatedStrictAndReadOnly(t *testing.T) {
	service, store, provider, input := computeClaimHTTPFixture(t)
	server := NewServer(service, "internal-secret")
	path := "/fabric/compute-provider-truth?launchOperationId=" + input.LaunchOperationID +
		"&accountId=" + input.AccountID + "&workspaceId=" + input.WorkspaceID +
		"&computeAllocationId=" + input.ComputeAllocationID + "&storageVolumeId=" + input.StorageVolumeID +
		"&packageId=" + input.PackageID + "&poolId=" + input.PoolID + "&nodePoolId=" + input.NodePoolID

	unauthorized := httptest.NewRecorder()
	server.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, path, nil))
	if unauthorized.Code != http.StatusUnauthorized || provider.proofCalls != 0 || provider.claimCalls != 0 {
		t.Fatalf("unauthorized status=%d calls=%d/%d", unauthorized.Code, provider.proofCalls, provider.claimCalls)
	}

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, testRequest(http.MethodGet, path, nil))
	var truth fabric.ComputeProviderTruth
	if recorder.Code != http.StatusOK || json.NewDecoder(recorder.Body).Decode(&truth) != nil || truth.State != "ready" ||
		truth.ComputeState != "ready" || truth.NodeOwnershipState != "unallocated" || truth.CVMOwnershipState != "recoverable" ||
		provider.proofCalls != 1 || provider.claimCalls != 0 {
		t.Fatalf("truth status=%d truth=%#v calls=%d/%d body=%s", recorder.Code, truth, provider.proofCalls, provider.claimCalls, recorder.Body.String())
	}
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 1 || operations[0].Status != "failed" {
		t.Fatalf("GET-only truth mutated operations=%#v err=%v", operations, err)
	}

	for _, invalidPath := range []string{
		path + "&extra=value",
		path + "&nodePoolId=duplicate",
		strings.Replace(path, "packageId=basic", "packageId=%20basic", 1),
	} {
		invalid := httptest.NewRecorder()
		server.ServeHTTP(invalid, testRequest(http.MethodGet, invalidPath, nil))
		if invalid.Code != http.StatusBadRequest {
			t.Fatalf("invalid query status=%d body=%s path=%s", invalid.Code, invalid.Body.String(), invalidPath)
		}
	}
	if provider.proofCalls != 1 || provider.claimCalls != 0 {
		t.Fatalf("invalid query reached provider calls=%d/%d", provider.proofCalls, provider.claimCalls)
	}
}

func TestComputePoolHeadTerminalizationHTTPUsesOneExactCASAndReadOnlyReplay(t *testing.T) {
	service, store, provider, input := computeClaimHTTPFixture(t)
	provider.claim = fabric.ComputeClaimProviderClaim{
		Proof: fabric.ComputeClaimProviderProof{
			Status: "blocked", Reason: "provider_describe", NodeOwnershipState: "unallocated", CVMOwnershipState: "recoverable",
			MachineName: provider.proof.MachineName, NodeName: provider.proof.NodeName, CVMInstanceID: provider.proof.CVMInstanceID,
			PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
			ChargeType: "PREPAID", PeriodMonths: 1, RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: provider.proof.Deadline,
		},
		TencentMutationCount: 1, KubernetesMutationCount: 0, FailureStage: "cvm_tag_readback", ProviderErrorClass: "readback_mismatch",
		Evidence: &fabric.ComputeClaimEvidence{
			CVM:  fabric.ComputeClaimMutationEvidence{Attempted: 1, Missing: []string{"opl_account_id"}},
			Node: fabric.ComputeClaimMutationEvidence{},
		},
	}
	claimInput := fabric.ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: provider.proof.MachineName, NodeName: provider.proof.NodeName,
		CVMInstanceID: provider.proof.CVMInstanceID, PrivateIP: provider.proof.PrivateIP,
		InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone, IdempotencyKey: input.LaunchOperationID + ":compute",
	}
	if _, err := service.ClaimComputeRecovery(context.Background(), claimInput); err == nil {
		t.Fatal("manual recovery fixture unexpectedly completed")
	}
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 1 || operations[0].Status != "claim_pending" || provider.proofCalls != 1 || provider.claimCalls != 1 {
		t.Fatalf("manual recovery fixture operations=%#v calls=%d/%d err=%v", operations, provider.proofCalls, provider.claimCalls, err)
	}

	server := NewServer(service, "internal-secret")
	path := "/fabric/compute-pool-head/terminalization?nodePoolId=" + input.NodePoolID
	unauthorized := httptest.NewRecorder()
	server.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, path, nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}

	candidateRecorder := httptest.NewRecorder()
	server.ServeHTTP(candidateRecorder, testRequest(http.MethodGet, path, nil))
	var candidate fabric.ComputePoolHeadTerminalizationReadback
	if candidateRecorder.Code != http.StatusOK || json.NewDecoder(candidateRecorder.Body).Decode(&candidate) != nil ||
		candidate.Status != "candidate" || candidate.HeadStatus != "claim_pending" || candidate.AllocationStatus != "quarantined" ||
		candidate.OwnershipStatus != "quarantined" || len(candidate.ApprovalDigest) != 64 || len(candidate.BindingDigest) != 64 ||
		len(candidate.ManualRecoveryLedgerDigest) != 64 || candidate.Sub2APIMutationCount != 0 || candidate.TencentMutationCount != 0 || candidate.KubernetesMutationCount != 0 {
		t.Fatalf("candidate status=%d candidate=%#v body=%s", candidateRecorder.Code, candidate, candidateRecorder.Body.String())
	}
	if provider.proofCalls != 1 || provider.claimCalls != 1 {
		t.Fatalf("candidate readback called provider: %d/%d", provider.proofCalls, provider.claimCalls)
	}

	const approvalID = "operator-terminalize-30970000005"
	invalidBody := fmt.Sprintf(`{"nodePoolId":%q,"approvalId":%q,"approvalDigest":%q,"unexpected":true}`, input.NodePoolID, approvalID, candidate.ApprovalDigest)
	invalidRequest := testRequest(http.MethodPost, "/fabric/compute-pool-head/terminalization", strings.NewReader(invalidBody))
	invalidRequest.Header.Set("Idempotency-Key", approvalID)
	invalidRecorder := httptest.NewRecorder()
	server.ServeHTTP(invalidRecorder, invalidRequest)
	operations, _ = store.List(context.Background())
	if invalidRecorder.Code != http.StatusBadRequest || len(operations) != 1 || operations[0].Status != "claim_pending" {
		t.Fatalf("invalid request status=%d body=%s operations=%#v", invalidRecorder.Code, invalidRecorder.Body.String(), operations)
	}

	body, _ := json.Marshal(fabric.ComputePoolHeadTerminalizationInput{NodePoolID: input.NodePoolID, ApprovalID: approvalID, ApprovalDigest: candidate.ApprovalDigest})
	request := testRequest(http.MethodPost, "/fabric/compute-pool-head/terminalization", bytes.NewReader(body))
	request.Header.Set("Idempotency-Key", approvalID)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	var terminalized fabric.ComputePoolHeadTerminalizationReadback
	if recorder.Code != http.StatusOK || json.NewDecoder(recorder.Body).Decode(&terminalized) != nil || terminalized.Status != "succeeded" ||
		terminalized.HeadStatus != "failed" || terminalized.TerminalStatus != "terminal_unprovable" || terminalized.Replayed ||
		terminalized.Sub2APIMutationCount != 0 || terminalized.TencentMutationCount != 0 || terminalized.KubernetesMutationCount != 0 {
		t.Fatalf("terminalized status=%d result=%#v body=%s", recorder.Code, terminalized, recorder.Body.String())
	}
	if provider.proofCalls != 1 || provider.claimCalls != 1 {
		t.Fatalf("terminalization called provider: %d/%d", provider.proofCalls, provider.claimCalls)
	}

	replayPath := path + "&approvalId=" + approvalID + "&approvalDigest=" + candidate.ApprovalDigest
	replayRecorder := httptest.NewRecorder()
	server.ServeHTTP(replayRecorder, testRequest(http.MethodGet, replayPath, nil))
	var replayed fabric.ComputePoolHeadTerminalizationReadback
	if replayRecorder.Code != http.StatusOK || json.NewDecoder(replayRecorder.Body).Decode(&replayed) != nil || replayed.Status != "succeeded" || !replayed.Replayed ||
		replayed.ApprovalDigest != candidate.ApprovalDigest || replayed.BindingDigest != candidate.BindingDigest || replayed.ManualRecoveryLedgerDigest != candidate.ManualRecoveryLedgerDigest {
		t.Fatalf("replay status=%d result=%#v body=%s", replayRecorder.Code, replayed, replayRecorder.Body.String())
	}

	conflictBody, _ := json.Marshal(fabric.ComputePoolHeadTerminalizationInput{NodePoolID: input.NodePoolID, ApprovalID: approvalID, ApprovalDigest: strings.Repeat("0", 64)})
	conflictRequest := testRequest(http.MethodPost, "/fabric/compute-pool-head/terminalization", bytes.NewReader(conflictBody))
	conflictRequest.Header.Set("Idempotency-Key", approvalID)
	conflictRecorder := httptest.NewRecorder()
	server.ServeHTTP(conflictRecorder, conflictRequest)
	if conflictRecorder.Code != http.StatusConflict || provider.proofCalls != 1 || provider.claimCalls != 1 {
		t.Fatalf("conflict status=%d body=%s calls=%d/%d", conflictRecorder.Code, conflictRecorder.Body.String(), provider.proofCalls, provider.claimCalls)
	}
}

func TestComputeClaimRecoveryHTTPNeverReturnsUnallowlistedMutationEvidence(t *testing.T) {
	service, _, provider, input := computeClaimHTTPFixture(t)
	const marker = "ghp_secret"
	provider.claim.Proof.Reason = "provider_describe"
	provider.claim.TencentMutationCount = 1
	provider.claim.KubernetesMutationCount = 0
	provider.claim.FailureStage = "cvm_final_readback"
	provider.claim.ProviderErrorClass = "readback_mismatch"
	provider.claim.Evidence = &fabric.ComputeClaimEvidence{
		CVM:  fabric.ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{marker}},
		Node: fabric.ComputeClaimMutationEvidence{},
	}
	claimInput := fabric.ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: provider.proof.MachineName, NodeName: provider.proof.NodeName,
		CVMInstanceID: provider.proof.CVMInstanceID, PrivateIP: provider.proof.PrivateIP,
		InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
	}
	body, err := json.Marshal(claimInput)
	if err != nil {
		t.Fatal(err)
	}
	request := testRequest(http.MethodPost, "/fabric/compute-claim-recovery/claim", bytes.NewReader(body))
	request.Header.Set("Idempotency-Key", "launch-fixture:compute")
	recorder := httptest.NewRecorder()

	NewServer(service, "internal-secret").ServeHTTP(recorder, request)

	if recorder.Code < 400 || provider.proofCalls != 1 || provider.claimCalls != 1 || strings.Contains(recorder.Body.String(), marker) {
		t.Fatalf("status=%d calls=%d/%d body=%s", recorder.Code, provider.proofCalls, provider.claimCalls, recorder.Body.String())
	}
}

func TestServerRenewsComputeAllocation(t *testing.T) {
	service := fabric.NewService(testProvider{})
	allocation, err := service.CreateComputeAllocation(context.Background(), fabric.ComputeAllocationInput{ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", NodePoolID: "np-basic", IdempotencyKey: "compute-create"})
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

func TestRuntimeOperationConflictsAreHTTPConflict(t *testing.T) {
	for _, err := range []error{fabric.ErrRuntimeIdempotencyConflict, fabric.ErrRuntimeOperationInProgress, fabric.ErrRuntimeOperationFailed, fabric.ErrGatewaySecretIdempotencyConflict} {
		recorder := httptest.NewRecorder()
		writeResult(recorder, fabric.WorkspaceRuntime{}, err)
		if recorder.Code != http.StatusConflict {
			t.Fatalf("error %v status = %d, want %d", err, recorder.Code, http.StatusConflict)
		}
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

func TestWriteComputeAllocationResultPreservesTerminalEvidence(t *testing.T) {
	allocation := fabric.ComputeAllocation{
		ID: "compute-fixture", AccountID: "acct-fixture", WorkspaceID: "ws-fixture", PackageID: "basic", Status: "quarantined",
		ClaimTerminalEvidence: &fabric.ComputeClaimTerminalEvidence{Stage: "compute_claim_node", Status: "terminal_unprovable", ErrorCode: "compute_claim_node_unprovable"},
	}
	recorder := httptest.NewRecorder()
	writeComputeAllocationResult(recorder, allocation, fabric.ErrComputeOperationFailed)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var got fabric.ComputeAllocation
	if err := json.NewDecoder(recorder.Body).Decode(&got); err != nil || got.ID != allocation.ID || got.ClaimTerminalEvidence == nil || got.ClaimTerminalEvidence.Status != "terminal_unprovable" {
		t.Fatalf("allocation=%#v err=%v", got, err)
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

type blockingComputeCreateHTTPProvider struct {
	testProvider
	entered chan struct{}
	release chan struct{}
}

func (p *blockingComputeCreateHTTPProvider) CreateComputeAllocation(ctx context.Context, input fabric.ComputeAllocationExecution) (fabric.ComputeAllocation, error) {
	p.entered <- struct{}{}
	select {
	case <-p.release:
		return p.testProvider.CreateComputeAllocation(ctx, input)
	case <-ctx.Done():
		return input.Allocation, ctx.Err()
	}
}

func TestSyncComputeAllocationHTTPWaitsForMachineOwnership(t *testing.T) {
	provider := &blockingComputeCreateHTTPProvider{entered: make(chan struct{}), release: make(chan struct{})}
	defer close(provider.release)
	service := fabric.NewService(provider)
	server := NewServer(service, "internal-secret")
	create := testRequest(http.MethodPost, "/fabric/compute-allocations", bytes.NewBufferString(`{"accountId":"acct-alpha","workspaceId":"ws-alpha","packageId":"basic","nodePoolId":"np-basic"}`))
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
	<-provider.entered
	if _, err := service.MachineOwnership(context.Background(), created.ID); !errors.Is(err, fabric.ErrMachineOwnershipNotFound) {
		t.Fatalf("machine ownership before provider completion: %v", err)
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

func TestGetStorageVolumeHTTPIsReadOnly(t *testing.T) {
	service := fabric.NewService(testProvider{})
	server := NewServer(service, "internal-secret")
	compute := createReadyCompute(t, service, server, "acct-alpha", "ws-alpha", "get-storage-compute")
	create := testRequest(http.MethodPost, "/fabric/storage-volumes", bytes.NewBufferString(fmt.Sprintf(`{"accountId":"acct-alpha","workspaceId":"ws-alpha","computeId":%q,"zone":"ap-guangzhou-3","sizeGb":10}`, compute.ID)))
	create.Header.Set("Idempotency-Key", "get-http-storage")
	createRec := httptest.NewRecorder()
	server.ServeHTTP(createRec, create)
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("create status = %d: %s", createRec.Code, createRec.Body.String())
	}
	var created fabric.StorageVolume
	if err := json.NewDecoder(createRec.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	before, err := service.ListOperations(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	req := testRequest(http.MethodGet, "/fabric/storage-volumes/"+created.ID, nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d: %s", rec.Code, rec.Body.String())
	}
	var volume fabric.StorageVolume
	if err := json.NewDecoder(rec.Body).Decode(&volume); err != nil || volume.ID != created.ID || volume.Status != "ready" {
		t.Fatalf("get volume=%#v err=%v", volume, err)
	}
	after, err := service.ListOperations(context.Background())
	if err != nil || len(after) != len(before) {
		t.Fatalf("read-only GET operations before=%#v after=%#v err=%v", before, after, err)
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

func (testProvider) PrepareComputeAllocation(_ context.Context, input fabric.ComputeAllocationInput) (fabric.ComputeAllocationPreparation, error) {
	instanceType := "SA5.MEDIUM4"
	poolID := "pool-basic-2c4g"
	if input.PackageID == "pro" {
		instanceType = "SA5.2XLARGE16"
		poolID = "pool-pro-8c16g"
	}
	return fabric.ComputeAllocationPreparation{
		PoolID: poolID, PackageID: input.PackageID, NodePoolID: input.NodePoolID, InstanceType: instanceType,
		MaxReplicas: 10, BaselineReplicas: 0, TargetReplicas: 1, BeforeMachineNames: []string{},
	}, nil
}

func (testProvider) CreateComputeAllocation(_ context.Context, input fabric.ComputeAllocationExecution) (fabric.ComputeAllocation, error) {
	id := input.Allocation.ID
	return fabric.ComputeAllocation{
		ID: id, AccountID: input.Allocation.AccountID, WorkspaceID: input.Allocation.WorkspaceID, PackageID: input.Allocation.PackageID,
		Status: "running", Provider: "tencent-tke", ProviderResourceID: "machine/" + id, ProviderRequestID: "compute-test",
		PoolID: input.Plan.PoolID, NodePoolID: input.Plan.NodePoolID, MachineName: id, InstanceID: "ins-" + id, CVMInstanceID: "ins-" + id,
		NodeName: "10.0.0.11", PrivateIP: "10.0.0.11", InstanceType: input.Plan.InstanceType, Zone: "ap-guangzhou-3",
		ChargeType: "PREPAID", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2026-08-16T00:00:00Z",
		ProviderData: map[string]string{"instanceType": input.Plan.InstanceType, "zone": "ap-guangzhou-3", "chargeType": "PREPAID", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": "2026-08-16T00:00:00Z"},
		CostTags: map[string]string{
			"opl_account_id": input.Allocation.AccountID, "opl_workspace_id": input.Allocation.WorkspaceID,
			"opl_resource_id": id, "opl_operation_id": "owner-" + id,
		},
	}, nil
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
	return fabric.GatewaySecret{SecretRef: "opl-gateway-ws-alpha", Version: digest[:16], Fingerprint: "sha256:" + digest}, nil
}

func (testProvider) Readiness(_ context.Context) (map[string]any, error) {
	return map[string]any{"provider": "test", "ready": true}, nil
}
