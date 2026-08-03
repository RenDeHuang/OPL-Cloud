package clients

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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

	client := NewFabricHTTPClient(upstream.URL, "internal-secret", upstream.Client())
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

	client := NewFabricHTTPClient(upstream.URL, "internal-secret", upstream.Client())
	_, err := client.WriteGatewaySecret(context.Background(), GatewaySecretWriteInput{
		AccountID: "acct-alpha", WorkspaceID: "ws-alpha", WorkspaceAPIKeyID: 19,
		Fingerprint: "sha256:20ad99c323ffc5eeac19c3a9b148f5911acb6b12826eaa089e09204e15ead7d5", GatewayAPIKey: secret,
	}, "workspace-once:gateway-secret")
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

func TestFabricHTTPClientReadsExactMachineOwnershipWithoutMutation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.EscapedPath() != "/fabric/machine-ownerships/compute%20alpha" || r.Header.Get("Authorization") != "Bearer internal-secret" {
			t.Fatalf("unexpected request: %s %s auth=%q", r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization"))
		}
		if _, ok := r.Header["Idempotency-Key"]; ok {
			t.Fatalf("read-only ownership GET sent Idempotency-Key: %#v", r.Header.Values("Idempotency-Key"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "owner-alpha", "resourceId": "compute alpha", "accountId": "acct-alpha", "workspaceId": "ws-alpha",
			"packageId": "basic", "nodePoolId": "np-alpha", "machineId": "machine-alpha", "instanceId": "ins-alpha",
			"nodeName": "node-alpha", "status": "active", "providerRequestId": "req-alpha",
		})
	}))
	defer upstream.Close()

	client := NewFabricHTTPClient(upstream.URL, "internal-secret", upstream.Client()).(FabricMachineOwnershipClient)
	ownership, err := client.MachineOwnership(context.Background(), "compute alpha")
	if err != nil || ownership.ID != "owner-alpha" || ownership.ResourceID != "compute alpha" || ownership.Status != "active" || ownership.InstanceID != "ins-alpha" {
		t.Fatalf("machine ownership = %#v err=%v", ownership, err)
	}
}

func TestFabricHTTPClientReadsStorageVolumeWithoutMutation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.EscapedPath() != "/fabric/storage-volumes/storage%20alpha" || r.Header.Get("Authorization") != "Bearer internal-secret" {
			t.Fatalf("unexpected request: %s %s auth=%q", r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization"))
		}
		if _, ok := r.Header["Idempotency-Key"]; ok {
			t.Fatalf("read-only storage GET sent Idempotency-Key: %#v", r.Header.Values("Idempotency-Key"))
		}
		_ = json.NewEncoder(w).Encode(StorageVolume{ID: "storage alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "ready", ProviderResourceID: "disk-alpha"})
	}))
	defer upstream.Close()

	client := NewFabricHTTPClient(upstream.URL, "internal-secret", upstream.Client())
	volume, err := client.GetStorageVolume(context.Background(), "storage alpha")
	if err != nil || volume.ID != "storage alpha" || volume.Status != "ready" || volume.ProviderResourceID != "disk-alpha" {
		t.Fatalf("storage volume = %#v err=%v", volume, err)
	}
}

func TestFabricHTTPClientReadsWorkspaceActivationTruthWithoutMutation(t *testing.T) {
	input := WorkspaceActivationTruthInput{
		LaunchOperationID: "workspace-launch-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha",
		ComputeAllocationID: "ca-alpha", ComputeOperationID: "workspace-launch-alpha:compute",
		StorageVolumeID: "vol-alpha", StorageOperationID: "workspace-launch-alpha:storage",
		AttachmentID: "attachment-alpha", AttachmentOperationID: "workspace-launch-alpha:attachment",
		RuntimeID: "runtime-alpha", RuntimeOperationID: "workspace-launch-alpha:workspace:runtime",
		ServiceName: "opl-compute-alpha", WorkspaceImageDigest: "sha256:" + strings.Repeat("a", 64),
		GatewaySecretRef: "opl-gateway-ws-alpha", WorkspaceAPIKeyID: 19,
		GatewaySecretFingerprint: "sha256:" + strings.Repeat("b", 64),
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/fabric/workspace-activation-truth" || r.Header.Get("Authorization") != "Bearer internal-secret" {
			t.Fatalf("unexpected request: %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		if _, ok := r.Header["Idempotency-Key"]; ok {
			t.Fatalf("read-only activation truth sent Idempotency-Key: %#v", r.Header.Values("Idempotency-Key"))
		}
		var actual WorkspaceActivationTruthInput
		if err := json.NewDecoder(r.Body).Decode(&actual); err != nil {
			t.Fatal(err)
		}
		if actual != input {
			t.Fatalf("activation truth input = %#v, want %#v", actual, input)
		}
		_ = json.NewEncoder(w).Encode(WorkspaceActivationTruth{
			SchemaVersion: 1, Ready: true, Reason: "none", ComputeState: "ready", StorageState: "ready",
			Compute:    ComputeAllocation{ID: input.ComputeAllocationID, OperationID: input.ComputeOperationID},
			Storage:    StorageVolume{ID: input.StorageVolumeID, OperationID: input.StorageOperationID},
			Attachment: StorageAttachment{ID: input.AttachmentID, OperationID: input.AttachmentOperationID},
			Runtime:    WorkspaceActivationRuntimeTruth{ID: input.RuntimeID, OperationID: input.RuntimeOperationID, ServiceName: input.ServiceName},
			Checks:     []any{map[string]any{"name": "runtime_ready", "ok": true}},
		})
	}))
	defer upstream.Close()

	client, ok := NewFabricHTTPClient(upstream.URL, "internal-secret", upstream.Client()).(FabricWorkspaceActivationTruthClient)
	if !ok {
		t.Fatal("Fabric HTTP client must implement Workspace activation truth capability")
	}
	truth, err := client.WorkspaceActivationTruth(context.Background(), input)
	if err != nil || !truth.Ready || truth.Runtime.ID != input.RuntimeID || truth.Attachment.OperationID != input.AttachmentOperationID {
		t.Fatalf("activation truth = %#v err=%v", truth, err)
	}
}

func TestFabricHTTPClientSeparatesWorkspaceLaunchStageProofAndCAS(t *testing.T) {
	input := WorkspaceLaunchStageReadbackInput{
		Stage: "runtime", FabricRecordID: "fop-runtime", FabricOperationID: "op-runtime",
		AccountID: "acct-alpha", WorkspaceID: "ws-alpha", IdempotencyKey: "launch-alpha:runtime",
		RequestHash: strings.Repeat("a", 64), ComputeID: "compute-alpha", StorageID: "storage-alpha",
		AttachmentID: "att_alpha", AttachmentOperationID: "launch-alpha:attachment",
		RuntimeID: "rt_alpha", RuntimeOperationID: "launch-alpha:runtime", ImageID: "sha256:workspace",
		GatewaySecretRef: "opl-gateway-alpha",
	}
	requests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer internal-secret" || r.Header.Get("Idempotency-Key") != "" {
			t.Fatalf("unexpected request: %s %s headers=%#v", r.Method, r.URL.Path, r.Header)
		}
		var actual WorkspaceLaunchStageReadbackInput
		if err := json.NewDecoder(r.Body).Decode(&actual); err != nil || actual.Stage != input.Stage || actual.FabricRecordID != input.FabricRecordID {
			t.Fatalf("input=%#v err=%v", actual, err)
		}
		wantPath := "/fabric/workspace-launch-stage-readback/proof"
		mutationCount := 0
		if requests == 2 {
			wantPath = "/fabric/workspace-launch-stage-readback/converge"
			mutationCount = 1
			if actual.ExpectedBindingDigest != strings.Repeat("b", 64) {
				t.Fatalf("convergence binding=%q", actual.ExpectedBindingDigest)
			}
		}
		if r.URL.Path != wantPath {
			t.Fatalf("path=%s want=%s", r.URL.Path, wantPath)
		}
		_ = json.NewEncoder(w).Encode(WorkspaceLaunchStageReadbackProof{
			SchemaVersion: 1, Eligible: true, Reason: "none", Stage: input.Stage, PriorStatus: "started",
			BindingDigest: strings.Repeat("b", 64), FabricOperationMutationCount: mutationCount,
			Operation: FabricOperation{ID: input.FabricRecordID, OperationID: input.FabricOperationID, Status: "succeeded"},
		})
	}))
	defer upstream.Close()

	client, ok := NewFabricHTTPClient(upstream.URL, "internal-secret", upstream.Client()).(FabricWorkspaceLaunchStageReadbackClient)
	if !ok {
		t.Fatal("Fabric client lacks Workspace launch stage readback boundary")
	}
	proof, err := client.WorkspaceLaunchStageReadbackProof(context.Background(), input)
	if err != nil || !proof.Eligible || proof.FabricOperationMutationCount != 0 {
		t.Fatalf("proof=%#v err=%v", proof, err)
	}
	input.ExpectedBindingDigest = proof.BindingDigest
	converged, err := client.ConvergeWorkspaceLaunchStageReadback(context.Background(), input)
	if err != nil || converged.FabricOperationMutationCount != 1 || requests != 2 {
		t.Fatalf("converged=%#v requests=%d err=%v", converged, requests, err)
	}
}

func TestFabricHTTPClientSeparatesComputeClaimProofAndMutation(t *testing.T) {
	requests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer internal-secret" {
			t.Fatalf("unexpected request: %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		var input ComputeClaimRecoveryClaimInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input.LaunchOperationID != "launch-fixture" || input.AccountID != "acct-fixture" || input.WorkspaceID != "ws-fixture" ||
			input.ComputeAllocationID != "ca-fixture" || input.StorageVolumeID != "vol-fixture" || input.PackageID != "pro" ||
			input.PoolID != "pool-pro-8c16g" || input.NodePoolID != "np-workspace-pro" {
			t.Fatalf("identity input=%#v", input)
		}
		response := ComputeClaimRecoveryProof{
			SchemaVersion: 1, Eligible: true, Reason: "none", StorageState: "storage_existing_exact", StorageProviderResourceID: "disk-existing-fixture",
			LaunchOperationID: input.LaunchOperationID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID,
			ComputeAllocationID: input.ComputeAllocationID, StorageVolumeID: input.StorageVolumeID, PackageID: input.PackageID,
			PoolID: input.PoolID, NodePoolID: input.NodePoolID, MachineName: "machine-fixture", NodeName: "10.0.0.18",
			CVMInstanceID: "ins-fixture", PrivateIP: "10.0.0.18", InstanceType: "SA5.2XLARGE16", Zone: "ap-guangzhou-3",
			ChargeType: "PREPAID", PeriodMonths: 1, RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2026-08-28T00:00:00Z",
			NodeOwnershipState: "unallocated", CVMOwnershipState: "recoverable", Evidence: &ComputeClaimEvidence{},
		}
		switch r.URL.Path {
		case "/fabric/compute-claim-recovery/proof":
			if _, ok := r.Header["Idempotency-Key"]; ok {
				t.Fatalf("read-only proof sent Idempotency-Key: %#v", r.Header.Values("Idempotency-Key"))
			}
		case "/fabric/compute-claim-recovery/claim":
			if r.Header.Get("Idempotency-Key") != "launch-fixture:compute" || input.MachineName != response.MachineName ||
				input.NodeName != response.NodeName || input.CVMInstanceID != response.CVMInstanceID || input.PrivateIP != response.PrivateIP ||
				input.InstanceType != response.InstanceType || input.Zone != response.Zone {
				t.Fatalf("claim input=%#v key=%q", input, r.Header.Get("Idempotency-Key"))
			}
			response.NodeOwnershipState = "target_owned"
			response.CVMOwnershipState = "target_owned"
			response.TencentMutationCount = 1
			response.KubernetesMutationCount = 1
			response.Evidence = &ComputeClaimEvidence{
				CVM:  ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1},
				Node: ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1},
			}
		case "/fabric/compute-claim-recovery/identity-evidence":
			_ = json.NewEncoder(w).Encode(ComputeClaimIdentityEvidence{
				Checks:                []ComputeClaimIdentityCheck{{Field: "binding.compatibility", Matches: true, Expected: "current_or_historical", Actual: "historical"}},
				MutationLedger:        "observed",
				MutationLedgerOutcome: "confirmed_zero",
				MutationLedgerDigest:  strings.Repeat("d", 64),
			})
			return
		default:
			t.Fatalf("unexpected path=%q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer upstream.Close()

	client, ok := NewFabricHTTPClient(upstream.URL, "internal-secret", upstream.Client()).(FabricComputeClaimRecoveryClient)
	if !ok {
		t.Fatal("Fabric HTTP client must implement compute claim recovery capability")
	}
	input := ComputeClaimRecoveryInput{
		LaunchOperationID: "launch-fixture", AccountID: "acct-fixture", WorkspaceID: "ws-fixture",
		ComputeAllocationID: "ca-fixture", StorageVolumeID: "vol-fixture", PackageID: "pro",
		PoolID: "pool-pro-8c16g", NodePoolID: "np-workspace-pro",
	}
	proof, err := client.ComputeClaimRecoveryProof(context.Background(), input)
	if err != nil || !proof.Eligible || proof.StorageState != "storage_existing_exact" || proof.StorageProviderResourceID != "disk-existing-fixture" || proof.TencentMutationCount != 0 || proof.KubernetesMutationCount != 0 {
		t.Fatalf("proof=%#v err=%v", proof, err)
	}
	identityClient, ok := NewFabricHTTPClient(upstream.URL, "internal-secret", upstream.Client()).(FabricComputeClaimRecoveryIdentityClient)
	if !ok {
		t.Fatal("Fabric HTTP client must implement compute claim identity evidence capability")
	}
	evidence, err := identityClient.ComputeClaimRecoveryIdentityEvidence(context.Background(), ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: proof.MachineName, NodeName: proof.NodeName, CVMInstanceID: proof.CVMInstanceID,
		PrivateIP: proof.PrivateIP, InstanceType: proof.InstanceType, Zone: proof.Zone,
	})
	if err != nil || evidence == nil || evidence.MutationLedger != "observed" || evidence.MutationLedgerOutcome != "confirmed_zero" ||
		evidence.MutationLedgerDigest != strings.Repeat("d", 64) || len(evidence.Checks) != 1 || !evidence.Checks[0].Matches {
		t.Fatalf("identity evidence=%#v err=%v", evidence, err)
	}
	claim, err := client.ClaimComputeRecovery(context.Background(), ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: proof.MachineName, NodeName: proof.NodeName, CVMInstanceID: proof.CVMInstanceID,
		PrivateIP: proof.PrivateIP, InstanceType: proof.InstanceType, Zone: proof.Zone,
	}, "launch-fixture:compute")
	if err != nil || !claim.Eligible || claim.NodeOwnershipState != "target_owned" || claim.CVMOwnershipState != "target_owned" || claim.TencentMutationCount != 1 || claim.KubernetesMutationCount != 1 || requests != 3 {
		t.Fatalf("claim=%#v err=%v requests=%d", claim, err, requests)
	}
}

func TestFabricHTTPClientPreservesSafeComputeClaimFailureProof(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(ComputeClaimRecoveryProof{
			SchemaVersion: 1, Eligible: false, Reason: "storage_already_started", StorageState: "unknown",
			LaunchOperationID: "launch-fixture", AccountID: "acct-fixture", WorkspaceID: "ws-fixture",
			ComputeAllocationID: "ca-fixture", StorageVolumeID: "vol-fixture", PackageID: "basic",
			PoolID: "pool-basic-2c4g", NodePoolID: "np-workspace-basic",
			Evidence: &ComputeClaimEvidence{},
		})
	}))
	defer upstream.Close()

	client := NewFabricHTTPClient(upstream.URL, "internal-secret", upstream.Client()).(FabricComputeClaimRecoveryClient)
	proof, err := client.ComputeClaimRecoveryProof(context.Background(), ComputeClaimRecoveryInput{
		LaunchOperationID: "launch-fixture", AccountID: "acct-fixture", WorkspaceID: "ws-fixture", ComputeAllocationID: "ca-fixture",
		StorageVolumeID: "vol-fixture", PackageID: "basic", PoolID: "pool-basic-2c4g", NodePoolID: "np-workspace-basic",
	})
	var upstreamErr *FabricHTTPError
	if !errors.As(err, &upstreamErr) || upstreamErr.StatusCode != http.StatusConflict || proof.Eligible || proof.Reason != "storage_already_started" ||
		proof.Sub2APIMutationCount != 0 || proof.TencentMutationCount != 0 || proof.KubernetesMutationCount != 0 {
		t.Fatalf("proof=%#v err=%v", proof, err)
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

	client, ok := NewFabricHTTPClient(upstream.URL, "internal-secret", upstream.Client()).(FabricRuntimeHealthClient)
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
		if input["computeId"] != "compute-alpha" || input["zone"] != "ap-shanghai-2" || input["expectedRecoveryState"] != "storage_existing_exact" || input["expectedProviderResourceId"] != "disk-existing-alpha" {
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
	volume, err := client.CreateStorageVolume(context.Background(), StorageVolumeInput{
		ID: "storage-alpha", AccountID: "acct-alpha", ComputeID: "compute-alpha", Zone: "ap-shanghai-2", SizeGB: 10,
		ExpectedRecoveryState: "storage_existing_exact", ExpectedProviderResourceID: "disk-existing-alpha",
	}, "storage-once")
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
