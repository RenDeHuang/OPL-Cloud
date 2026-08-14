package clients

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newFabricHTTPClientForTest(baseURL, token string, client *http.Client) FabricClient {
	if client == nil {
		client = http.DefaultClient
	}
	return &fabricHTTPClient{baseURL: baseURL, token: token, client: client}
}

func TestFabricHTTPClientSignsShortLivedOperationBoundMutationCapability(t *testing.T) {
	const capabilityKey = "test-capability-key"
	before := time.Now().Unix()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.Split(r.Header.Get(FabricCapabilityHeader), ".")
		if len(parts) != 2 {
			t.Fatalf("capability format=%q", r.Header.Get(FabricCapabilityHeader))
		}
		mac := hmac.New(sha256.New, []byte(capabilityKey))
		_, _ = mac.Write([]byte(parts[0]))
		signature, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
			t.Fatalf("capability integrity invalid: %v", err)
		}
		payload, err := base64.RawURLEncoding.DecodeString(parts[0])
		if err != nil {
			t.Fatal(err)
		}
		var claims struct {
			Version      int    `json:"version"`
			Caller       string `json:"caller"`
			AccountID    string `json:"accountId"`
			WorkspaceID  string `json:"workspaceId"`
			ResourceKind string `json:"resourceKind"`
			ResourceID   string `json:"resourceId"`
			Action       string `json:"action"`
			OperationID  string `json:"operationId"`
			ExpiresAt    int64  `json:"expiresAt"`
			BodySHA256   string `json:"bodySha256"`
		}
		if err := json.Unmarshal(payload, &claims); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(body)
		if claims.Version != 1 || claims.Caller != "control-plane" || claims.AccountID != "acct-alpha" || claims.WorkspaceID != "ws-alpha" ||
			claims.ResourceKind != "compute_allocation" || claims.ResourceID != "compute-alpha" || claims.Action != "create_compute_allocation" ||
			claims.OperationID != "operation-alpha" || claims.BodySHA256 != hex.EncodeToString(digest[:]) || claims.ExpiresAt <= before || claims.ExpiresAt > before+120 {
			t.Fatalf("capability claims=%#v", claims)
		}
		_ = json.NewEncoder(w).Encode(ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "provisioning"})
	}))
	defer upstream.Close()

	client := NewFabricHTTPClientWithCapability(upstream.URL, "internal-secret", capabilityKey, upstream.Client())
	_, err := client.CreateComputeAllocation(context.Background(), ComputeAllocationInput{
		ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic",
	}, "operation-alpha")
	if err != nil {
		t.Fatal(err)
	}
}

func TestFabricHTTPClientUsesCapabilityForRuntimeCredentialReveal(t *testing.T) {
	const capabilityKey = "test-capability-key"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/fabric/workspace-runtimes/ws-alpha/credentials/reveal" || r.Header.Get("Idempotency-Key") != "reveal-once" || r.Header.Get(FabricCapabilityHeader) == "" {
			t.Fatalf("unexpected reveal request: %s %s key=%q capability=%q", r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"), r.Header.Get(FabricCapabilityHeader))
		}
		var input map[string]string
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input["accountId"] != "acct-alpha" || input["workspaceId"] != "ws-alpha" {
			t.Fatalf("reveal input=%#v err=%v", input, err)
		}
		_ = json.NewEncoder(w).Encode(WorkspaceRuntime{ID: "runtime-alpha", WorkspaceID: "ws-alpha", Status: "running", Ready: true, Access: WorkspaceRuntimeAccess{Password: "secret"}})
	}))
	defer upstream.Close()

	client, ok := NewFabricHTTPClientWithCapability(upstream.URL, "internal-secret", capabilityKey, upstream.Client()).(FabricWorkspaceRuntimeCredentialClient)
	if !ok {
		t.Fatal("Fabric HTTP client must implement runtime credential reveal")
	}
	runtime, err := client.RevealWorkspaceRuntimeCredentials(context.Background(), "acct-alpha", "ws-alpha", "reveal-once")
	if err != nil || runtime.Access.Password != "secret" {
		t.Fatalf("runtime credentials=%#v err=%v", runtime, err)
	}
}

func TestFabricHTTPClientWritesWorkspaceScopedGatewaySecret(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/fabric/gateway-secrets" || r.Header.Get("Idempotency-Key") != "workspace-once:gateway-secret" || r.Header.Get("Authorization") != "Bearer internal-secret" {
			t.Fatalf("unexpected request: %s %s key=%q auth=%q", r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"), r.Header.Get("Authorization"))
		}
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if len(input) != 5 || input["accountId"] != "acct-alpha" || input["workspaceId"] != "ws-alpha" || input["workspaceApiKeyId"] != float64(19) ||
			input["fingerprint"] != "sha256:workspace-key" || input["gatewayApiKey"] != "workspace-key-secret" {
			t.Fatalf("gateway secret input = %#v", input)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"secretRef": "opl-gateway-ws-alpha", "version": "v2", "fingerprint": "sha256:workspace-key"})
	}))
	defer upstream.Close()

	client := newFabricHTTPClientForTest(upstream.URL, "internal-secret", upstream.Client())
	var input GatewaySecretWriteInput
	if err := json.Unmarshal([]byte(`{"accountId":"acct-alpha","workspaceId":"ws-alpha","workspaceApiKeyId":19,"fingerprint":"sha256:workspace-key","gatewayApiKey":"workspace-key-secret"}`), &input); err != nil {
		t.Fatal(err)
	}
	result, err := client.WriteGatewaySecret(context.Background(), input, "workspace-once:gateway-secret")
	if err != nil || result.SecretRef != "opl-gateway-ws-alpha" || result.Version != "v2" || result.Fingerprint != "sha256:workspace-key" {
		t.Fatalf("gateway secret result = %#v err=%v", result, err)
	}
}

func TestFabricHTTPClientGatewaySecretErrorDoesNotLeakKey(t *testing.T) {
	const secret = "workspace-key-secret"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, secret, http.StatusInternalServerError)
	}))
	defer upstream.Close()

	client := newFabricHTTPClientForTest(upstream.URL, "internal-secret", upstream.Client())
	_, err := client.WriteGatewaySecret(context.Background(), GatewaySecretWriteInput{
		AccountID: "acct-alpha", WorkspaceID: "ws-alpha", WorkspaceAPIKeyID: 19,
		Fingerprint: "sha256:20ad99c323ffc5eeac19c3a9b148f5911acb6b12826eaa089e09204e15ead7d5", GatewayAPIKey: secret,
	}, "workspace-once:gateway-secret")
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("gateway secret error = %v", err)
	}
}

func TestFabricHTTPClientBoundsResponseBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"padding":"` + strings.Repeat("sensitive", 1<<17) + `"}`))
	}))
	defer upstream.Close()

	client := newFabricHTTPClientForTest(upstream.URL, "internal-secret", upstream.Client()).(*fabricHTTPClient)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, upstream.URL+"/fabric/readiness", nil)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	err = client.doJSON(req, &result)
	if err == nil || !strings.Contains(err.Error(), "fabric response too large") || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("bounded response error = %v", err)
	}
}

func TestFabricHTTPClientRejectsMultipleJSONValues(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}{"ok":true}`))
	}))
	defer upstream.Close()

	client := newFabricHTTPClientForTest(upstream.URL, "internal-secret", upstream.Client()).(*fabricHTTPClient)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, upstream.URL+"/fabric/readiness", nil)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	err = client.doJSON(req, &result)
	if err == nil || !strings.Contains(err.Error(), "multiple JSON values") {
		t.Fatalf("multiple JSON value error = %v", err)
	}
}

func TestFabricHTTPClientBoundsErrorBody(t *testing.T) {
	const secret = "never-leak-after-truncation"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"` + strings.Repeat("x", 96<<10) + `","detail":"` + secret + `"}`))
	}))
	defer upstream.Close()

	client := newFabricHTTPClientForTest(upstream.URL, "internal-secret", upstream.Client()).(*fabricHTTPClient)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, upstream.URL+"/fabric/readiness", nil)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	err = client.doJSON(req, &result)
	var httpErr *FabricHTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %v, want FabricHTTPError", err)
	}
	if httpErr.StatusCode != http.StatusInternalServerError || len(httpErr.Body) > maxFabricErrorBodyBytes || strings.Contains(httpErr.Body, secret) {
		t.Fatalf("bounded error body = %#v", httpErr)
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

	client := newFabricHTTPClientForTest(upstream.URL, "internal-secret", upstream.Client()).(FabricMonthlyPreflightClient)
	result, err := client.MonthlyPreflight(context.Background(), MonthlyPreflightInput{ResourceType: "storage", PackageID: "pro", SizeGB: 100, Zone: "ap-guangzhou-3"})
	if err != nil || !result.Available || result.ProviderPriceCNY != 12.34 {
		t.Fatalf("monthly preflight = %#v err=%v", result, err)
	}
}

func TestFabricHTTPClientReadsRuntimeHealthSummaryWithoutMutation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/fabric/runtime-health-summary" || r.Header.Get("Authorization") != "Bearer internal-secret" {
			t.Fatalf("unexpected request: %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		if _, ok := r.Header["Idempotency-Key"]; ok {
			t.Fatalf("read-only Runtime summary sent Idempotency-Key: %#v", r.Header.Values("Idempotency-Key"))
		}
		_ = json.NewEncoder(w).Encode(RuntimeHealthSummary{Total: 1000, Ready: 999, Unready: 1})
	}))
	defer upstream.Close()

	client, ok := newFabricHTTPClientForTest(upstream.URL, "internal-secret", upstream.Client()).(FabricRuntimeHealthClient)
	if !ok {
		t.Fatal("Fabric HTTP client must implement Runtime health summary capability")
	}
	summary, err := client.RuntimeHealthSummary(context.Background())
	if err != nil || summary.Total != 1000 || summary.Ready != 999 || summary.Unready != 1 {
		t.Fatalf("Runtime health summary = %#v err=%v", summary, err)
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
		if len(input) != 6 || input["computeId"] != "compute-alpha" || input["zone"] != "ap-shanghai-2" {
			t.Fatalf("storage placement input = %#v", input)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "storage-alpha", "status": "available", "providerResourceId": "disk-alpha",
			"zone": "ap-shanghai-2", "diskType": "CLOUD_PREMIUM", "renewFlag": "NOTIFY_AND_MANUAL_RENEW",
			"deadline": "2026-08-16T00:00:00Z", "cbsStatus": "UNATTACHED", "providerData": map[string]any{"chargeType": "PREPAID"},
		})
	}))
	defer upstream.Close()

	client := newFabricHTTPClientForTest(upstream.URL, "internal-secret", upstream.Client())
	volume, err := client.CreateStorageVolume(context.Background(), StorageVolumeInput{
		ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ComputeID: "compute-alpha", Zone: "ap-shanghai-2", SizeGB: 10,
	}, "storage-once")
	if err != nil || volume.Zone != "ap-shanghai-2" || volume.DiskType != "CLOUD_PREMIUM" || volume.RenewFlag != "NOTIFY_AND_MANUAL_RENEW" || volume.Deadline != "2026-08-16T00:00:00Z" || volume.CBSStatus != "UNATTACHED" || volume.ProviderData["chargeType"] != "PREPAID" {
		t.Fatalf("storage readback = %#v err=%v", volume, err)
	}
}

func TestFabricHTTPClientPreservesComputeAllocationOnConflict(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/fabric/compute-allocations" || r.Header.Get("Idempotency-Key") != "launch-fixture:compute" {
			t.Fatalf("unexpected request: %s %s key=%q", r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"))
		}
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(ComputeAllocation{
			ID: "compute-fixture", AccountID: "acct-fixture", WorkspaceID: "ws-fixture", PackageID: "basic",
			Status: "quarantined", Provider: "tencent-tke", NodePoolID: "np-basic",
		})
	}))
	defer upstream.Close()

	client := newFabricHTTPClientForTest(upstream.URL, "internal-secret", upstream.Client())
	allocation, err := client.CreateComputeAllocation(context.Background(), ComputeAllocationInput{
		ID: "compute-fixture", AccountID: "acct-fixture", WorkspaceID: "ws-fixture", PackageID: "basic",
	}, "launch-fixture:compute")
	var httpErr *FabricHTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusConflict || allocation.ID != "compute-fixture" || allocation.Status != "quarantined" {
		t.Fatalf("allocation=%#v err=%v", allocation, err)
	}
}

func TestFabricHTTPClientRenewsMonthlyResourcesWithNeutralMutationReceipt(t *testing.T) {
	const capabilityKey = "test-capability-key"
	paths := []string{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Method != http.MethodPost || r.Header.Get("Idempotency-Key") != "renew-once" {
			t.Fatalf("unexpected renewal request: %s %s key=%q", r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"))
		}
		var input map[string]string
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input["accountId"] != "acct-alpha" || input["workspaceId"] != "workspace-alpha" {
			t.Fatalf("renewal identity=%#v err=%v", input, err)
		}
		if r.Header.Get(FabricCapabilityHeader) == "" {
			t.Fatal("renewal capability is missing")
		}
		switch r.URL.Path {
		case "/fabric/compute-allocations/compute-alpha/renew":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "compute-alpha", "operationId": "operation-compute-renew", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "status": "running", "providerRequestId": "compute-renew", "providerData": map[string]any{"ignored": true}})
		case "/fabric/storage-volumes/storage-alpha/renew":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "storage-alpha", "operationId": "operation-storage-renew", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "status": "available", "providerRequestId": "storage-renew", "providerData": map[string]any{"ignored": true}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	client := NewFabricHTTPClientWithCapability(upstream.URL, "internal-secret", capabilityKey, upstream.Client()).(FabricRenewalClient)
	compute, err := client.RenewComputeAllocation(context.Background(), "acct-alpha", "workspace-alpha", "compute-alpha", "renew-once")
	if err != nil || compute.ID != "compute-alpha" || compute.OperationID != "operation-compute-renew" || compute.AccountID != "acct-alpha" || compute.WorkspaceID != "workspace-alpha" || compute.Status != "running" || compute.ProviderRequestID != "compute-renew" {
		t.Fatalf("compute renewal = %#v err=%v", compute, err)
	}
	storage, err := client.RenewStorageVolume(context.Background(), "acct-alpha", "workspace-alpha", "storage-alpha", "renew-once")
	if err != nil || storage.ID != "storage-alpha" || storage.OperationID != "operation-storage-renew" || storage.AccountID != "acct-alpha" || storage.WorkspaceID != "workspace-alpha" || storage.Status != "available" || storage.ProviderRequestID != "storage-renew" {
		t.Fatalf("storage renewal = %#v err=%v", storage, err)
	}
	if strings.Join(paths, ",") != "/fabric/compute-allocations/compute-alpha/renew,/fabric/storage-volumes/storage-alpha/renew" {
		t.Fatalf("renewal paths = %#v", paths)
	}
}

func TestFabricHTTPClientUsesTypedWorkspaceDeleteRoutes(t *testing.T) {
	const capabilityKey = "test-capability-key"
	requests := []string{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path+" "+r.Header.Get("Idempotency-Key"))
		if r.Header.Get("Authorization") != "Bearer internal-secret" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Method == http.MethodPost {
			var input map[string]string
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input["accountId"] != "acct-alpha" || input["workspaceId"] != "ws-alpha" {
				t.Fatalf("delete identity=%#v err=%v", input, err)
			}
			if r.Header.Get(FabricCapabilityHeader) == "" {
				t.Fatal("delete capability is missing")
			}
		}
		switch r.Method + " " + r.URL.Path {
		case "POST /fabric/workspace-runtimes/ws-alpha/destroy":
			_ = json.NewEncoder(w).Encode(WorkspaceRuntime{ID: "runtime-alpha", WorkspaceID: "ws-alpha", Status: "destroyed"})
		case "POST /fabric/storage-attachments/attachment-alpha/detach":
			_ = json.NewEncoder(w).Encode(StorageAttachment{ID: "attachment-alpha", WorkspaceID: "ws-alpha", ComputeID: "compute-alpha", VolumeID: "storage-alpha", Status: "detached"})
		case "POST /fabric/storage-volumes/storage-alpha/destroy":
			_ = json.NewEncoder(w).Encode(StorageVolume{ID: "storage-alpha", WorkspaceID: "ws-alpha", Status: "destroyed"})
		case "POST /fabric/compute-allocations/compute-alpha/destroy":
			_ = json.NewEncoder(w).Encode(ComputeAllocation{ID: "compute-alpha", WorkspaceID: "ws-alpha", Status: "destroying"})
		case "GET /fabric/compute-allocations/compute-alpha":
			if r.Header.Get("Idempotency-Key") != "" {
				t.Fatalf("readback sent Idempotency-Key = %q", r.Header.Get("Idempotency-Key"))
			}
			_ = json.NewEncoder(w).Encode(ComputeAllocation{ID: "compute-alpha", WorkspaceID: "ws-alpha", Status: "destroyed"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	client, ok := NewFabricHTTPClientWithCapability(upstream.URL, "internal-secret", capabilityKey, upstream.Client()).(FabricWorkspaceDeleteClient)
	if !ok {
		t.Fatal("Fabric HTTP client must implement Workspace delete capability")
	}
	ctx := context.Background()
	if _, err := client.DestroyWorkspaceRuntime(ctx, "acct-alpha", "ws-alpha", "delete-intent:runtime"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.DetachStorageAttachment(ctx, "acct-alpha", "ws-alpha", "attachment-alpha", "delete-intent:attachment"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.DestroyStorageVolume(ctx, "acct-alpha", "ws-alpha", "storage-alpha", "delete-intent:storage"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.DestroyComputeAllocation(ctx, "acct-alpha", "ws-alpha", "compute-alpha", "delete-intent:compute"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReadComputeAllocation(ctx, "compute-alpha"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"POST /fabric/workspace-runtimes/ws-alpha/destroy delete-intent:runtime",
		"POST /fabric/storage-attachments/attachment-alpha/detach delete-intent:attachment",
		"POST /fabric/storage-volumes/storage-alpha/destroy delete-intent:storage",
		"POST /fabric/compute-allocations/compute-alpha/destroy delete-intent:compute",
		"GET /fabric/compute-allocations/compute-alpha ",
	}
	if strings.Join(requests, "\n") != strings.Join(want, "\n") {
		t.Fatalf("Workspace delete requests = %#v, want %#v", requests, want)
	}
}

func TestFabricClientReturnsErrorOnUpstreamFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "fabric unavailable", http.StatusServiceUnavailable)
	}))
	defer upstream.Close()

	client := newFabricHTTPClientForTest(upstream.URL, "internal-secret", upstream.Client())
	if _, err := client.Catalog(context.Background()); err == nil || !strings.Contains(err.Error(), "status 503") {
		t.Fatalf("expected upstream status error, got %v", err)
	}
}
