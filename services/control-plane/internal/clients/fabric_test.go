package clients

import (
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

func TestFabricHTTPClientReadsMonthlyProviderTruthWithoutMutation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/fabric/monthly-provider-truth" || r.Header.Get("Authorization") != "Bearer internal-secret" ||
			r.URL.Query().Get("computeAllocationId") != "compute alpha" || r.URL.Query().Get("storageVolumeId") != "storage/alpha" {
			t.Fatalf("unexpected request: %s %s?%s auth=%q", r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"))
		}
		if _, ok := r.Header["Idempotency-Key"]; ok {
			t.Fatalf("read-only provider truth sent Idempotency-Key: %#v", r.Header.Values("Idempotency-Key"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"computeState": "ready", "storageState": "absent", "providerRequestId": "req-truth",
			"compute": map[string]any{"id": "compute alpha", "accountId": "acct-alpha", "workspaceId": "ws-alpha"},
			"storage": map[string]any{"id": "storage/alpha", "accountId": "acct-alpha", "workspaceId": "ws-alpha"},
		})
	}))
	defer upstream.Close()

	client := NewFabricHTTPClient(upstream.URL, "internal-secret", upstream.Client()).(FabricMonthlyProviderTruthClient)
	truth, err := client.MonthlyProviderTruth(context.Background(), "compute alpha", "storage/alpha")
	if err != nil || truth.ComputeState != "ready" || truth.StorageState != "absent" || truth.Compute.ID != "compute alpha" || truth.Storage.ID != "storage/alpha" || truth.ProviderRequestID != "req-truth" {
		t.Fatalf("monthly provider truth = %#v err=%v", truth, err)
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
