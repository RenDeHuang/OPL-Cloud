package fabric

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestTKENodeSelectorPrefersClaimedNodeHostname(t *testing.T) {
	withMachine := tkeNodeSelector(map[string]string{"machineName": "np-basic-2"}, "10.0.0.8")
	if withMachine["kubernetes.io/hostname"] != "10.0.0.8" {
		t.Fatalf("selector with machineName = %#v", withMachine)
	}
	if _, ok := withMachine["cloud.tencent.com/node-instance-id"]; ok {
		t.Fatalf("selector must not use TKE machine name as CVM instance id: %#v", withMachine)
	}
	withoutMachine := tkeNodeSelector(map[string]string{}, "10.0.0.8")
	if withoutMachine["kubernetes.io/hostname"] != "10.0.0.8" {
		t.Fatalf("selector without machineName = %#v", withoutMachine)
	}
}

func TestTencentProviderMonthlyPreflightUsesExplicitReadOnlyProviderPaths(t *testing.T) {
	t.Setenv("OPL_BASIC_COMPUTE_NODE_POOL_ID", "np-basic")
	t.Setenv("OPL_PRO_COMPUTE_NODE_POOL_ID", "np-pro")
	for _, tc := range []struct {
		name  string
		input MonthlyPreflightInput
		check func(*testing.T, provisionerRequest)
		reply provisionerResponse
	}{
		{
			name: "compute", input: MonthlyPreflightInput{ResourceType: "compute", PackageID: "basic", Zone: "na-siliconvalley-1"},
			check: func(t *testing.T, request provisionerRequest) {
				if request.Action != "capacity_preflight" || request.PackageID != "basic" || request.Zone != "na-siliconvalley-1" || request.Pool.NodePoolID != "np-basic" || request.Pool.InstanceType != "SA5.MEDIUM4" || request.Pool.DesiredReplicas != 1 {
					t.Fatalf("compute preflight request = %#v", request)
				}
			},
			reply: provisionerResponse{
				OK: true, Status: "ready", NodePoolID: "np-basic", InstanceType: "SA5.MEDIUM4", InstanceAvailable: true, Zones: []string{"na-siliconvalley-1"},
				ProviderPriceCNY: 142.91, RemainingQuota: 8, ProviderRequestIDs: map[string]string{"nodePool": "req-pool", "subnets": "req-subnets", "availability": "req-capacity", "quota": "req-quota"},
				ProviderData: map[string]string{"chargeType": "PREPAID", "periodMonths": "1", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "zone": "na-siliconvalley-1"},
			},
		},
		{
			name: "pro compute", input: MonthlyPreflightInput{ResourceType: "compute", PackageID: "pro", Zone: "na-siliconvalley-1"},
			check: func(t *testing.T, request provisionerRequest) {
				if request.Action != "capacity_preflight" || request.PackageID != "pro" || request.Zone != "na-siliconvalley-1" || request.Pool.ID != "pro" || request.Pool.NodePoolID != "np-pro" || request.Pool.InstanceType != "SA5.2XLARGE16" || request.Pool.DesiredReplicas != 1 {
					t.Fatalf("Pro compute preflight request = %#v", request)
				}
			},
			reply: provisionerResponse{
				OK: true, Status: "ready", NodePoolID: "np-pro", InstanceType: "SA5.2XLARGE16", InstanceAvailable: true, Zones: []string{"na-siliconvalley-1"},
				ProviderPriceCNY: 571.64, RemainingQuota: 8, ProviderRequestIDs: map[string]string{"nodePool": "req-pro-pool", "subnets": "req-pro-subnets", "availability": "req-pro-capacity", "quota": "req-pro-quota"},
				ProviderData: map[string]string{"chargeType": "PREPAID", "periodMonths": "1", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "zone": "na-siliconvalley-1"},
			},
		},
		{
			name: "storage", input: MonthlyPreflightInput{ResourceType: "storage", PackageID: "basic", SizeGB: 10, Zone: "na-siliconvalley-1"},
			check: func(t *testing.T, request provisionerRequest) {
				if request.Action != "storage_preflight" || request.PackageID != "basic" || request.Storage.SizeGB != 10 || request.Storage.Zone != "na-siliconvalley-1" || request.Storage.DiskType != "CLOUD_BSSD" {
					t.Fatalf("storage preflight request = %#v", request)
				}
			},
			reply: provisionerResponse{
				OK: true, Status: "ready", ProviderPriceCNY: 7.5, ProviderRequestIDs: map[string]string{"quota": "req-quota", "price": "req-price"},
				ProviderData: map[string]string{"chargeType": "PREPAID", "periodMonths": "1", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "zone": "na-siliconvalley-1", "diskType": "CLOUD_BSSD", "sizeGb": "10"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := NewTencentProvider()
			provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
				tc.check(t, request)
				return tc.reply, nil
			}
			provider.kubectl = func(context.Context, []string, []byte) ([]byte, error) {
				t.Fatal("monthly preflight must not call kubectl")
				return nil, nil
			}
			result, err := provider.MonthlyPreflight(context.Background(), tc.input)
			if err != nil || result.ResourceType != tc.input.ResourceType || result.PackageID != tc.input.PackageID || result.SizeGB != tc.input.SizeGB || result.Zone != tc.input.Zone || !result.Available || result.ChargeType != "PREPAID" || result.PeriodMonths != 1 || result.RenewFlag != "NOTIFY_AND_MANUAL_RENEW" || result.ProviderPriceCNY != tc.reply.ProviderPriceCNY || len(result.ProviderRequestIDs) == 0 {
				t.Fatalf("monthly preflight = %#v, err=%v", result, err)
			}
		})
	}
}

func TestSyncComputeAllocationRestoresClaimedMachineSelector(t *testing.T) {
	provider := NewTencentProvider()
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Pool.InstanceType != "SA5.MEDIUM4" {
			t.Fatalf("sync request missing exact package SKU: %#v", request.Pool)
		}
		return provisionerResponse{
			OK: true, Status: "running", InstanceID: "np-basic-2", NodeName: "10.0.0.8",
			InstanceType: "SA5.MEDIUM4", ProviderData: map[string]string{"machineName": "np-basic-2", "instanceType": "SA5.MEDIUM4"},
		}, nil
	}

	allocation, err := provider.SyncComputeAllocation(context.Background(), ComputeAllocation{ID: "compute-alpha", PackageID: "basic"})
	if err != nil {
		t.Fatal(err)
	}
	if allocation.NodeSelector["kubernetes.io/hostname"] != "10.0.0.8" || allocation.ProviderData["instanceType"] != "SA5.MEDIUM4" {
		t.Fatalf("synced selector = %#v", allocation.NodeSelector)
	}
}

func TestSyncComputeAllocationPreservesPaidIdentityWhenProviderReadbackFails(t *testing.T) {
	provider := NewTencentProvider()
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Pool.InstanceType != "SA5.MEDIUM4" {
			t.Fatalf("sync request missing exact package SKU: %#v", request.Pool)
		}
		return provisionerResponse{OK: false, ErrorCode: "compute_provider_partial_identity", ProviderRequestID: "req-sync", ProviderData: map[string]string{"instanceType": "SA5.MEDIUM4"}}, nil
	}
	input := ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", PackageID: "basic", NodePoolID: "np-basic", MachineName: "machine-alpha", InstanceID: "ins-alpha", CVMInstanceID: "ins-alpha", NodeName: "node-alpha", Deadline: "2026-08-16T00:00:00Z"}

	allocation, err := provider.SyncComputeAllocation(context.Background(), input)
	if err == nil || allocation.ID != input.ID || allocation.InstanceID != input.InstanceID || allocation.MachineName != input.MachineName || allocation.Deadline != input.Deadline || allocation.ProviderRequestID != "req-sync" {
		t.Fatalf("failed sync lost paid identity: allocation=%#v err=%v", allocation, err)
	}
}

func TestTencentTagComputeMachineWritesProviderIdentityBeforeNodeLabel(t *testing.T) {
	provider := NewTencentProvider()
	var events []string
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		events = append(events, "provider")
		if request.Action != "tag_compute_machine" || request.Pool.NodePoolID != "np-basic" || request.Allocation.InstanceID != "ins-alpha" || request.Allocation.PrivateIP != "10.0.0.8" || request.Tags["opl_resource_id"] != "compute-alpha" {
			t.Fatalf("provider request = %#v", request)
		}
		return provisionerResponse{OK: true, Status: "tagged"}, nil
	}
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		events = append(events, "node")
		if !slices.Equal(args, []string{"label", "node/node-alpha", "oplcloud.cn/resource-id=compute-alpha", "oplcloud.cn/account-id=acct-alpha", "oplcloud.cn/workspace-id=ws-alpha", "--overwrite"}) {
			t.Fatalf("kubectl args = %#v", args)
		}
		return nil, nil
	}

	err := provider.TagComputeMachine(context.Background(), ProviderMachine{MachineID: "machine-alpha", InstanceID: "ins-alpha", NodeName: "node-alpha", PrivateIP: "10.0.0.8"}, MachineOwnership{ResourceID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", NodePoolID: "np-basic"})
	if err != nil || !slices.Equal(events, []string{"provider", "node"}) {
		t.Fatalf("tag machine err=%v events=%#v", err, events)
	}
}

func TestDestroyComputeAllocationWithoutClaimedMachineSkipsProviderMutation(t *testing.T) {
	provider := NewTencentProvider()
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		t.Fatalf("unexpected provider mutation: %#v", request)
		return provisionerResponse{}, nil
	}

	allocation, err := provider.DestroyComputeAllocation(context.Background(), ComputeAllocation{ID: "compute-alpha", NodePoolID: "np-basic", ProviderRequestID: "local-request-only", Status: "provisioning"})
	if err != nil || allocation.Status != "destroyed" {
		t.Fatalf("destroy unclaimed compute = %#v err=%v", allocation, err)
	}
}

func TestDestroyExternallyDeletedComputeSkipsProviderMutation(t *testing.T) {
	provider := NewTencentProvider()
	kubectlCalled := false
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		t.Fatalf("unexpected provider mutation: %#v", request)
		return provisionerResponse{}, nil
	}
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		kubectlCalled = true
		if !slices.Equal(args, []string{"delete", "deployment/opl-compute-alpha", "service/opl-compute-alpha", "secret/opl-compute-alpha-env", "--ignore-not-found=true", "--wait=true"}) {
			t.Fatalf("unexpected runtime cleanup: %#v", args)
		}
		return nil, nil
	}

	allocation, err := provider.DestroyComputeAllocation(context.Background(), ComputeAllocation{
		ID: "compute-alpha", Status: "external_deleted", NodePoolID: "np-basic",
		MachineName: "machine-alpha", InstanceID: "ins-alpha", NodeName: "node-alpha", PrivateIP: "10.0.0.8",
	})
	if err != nil || allocation.Status != "destroyed" || !kubectlCalled {
		t.Fatalf("destroy externally deleted compute = %#v err=%v", allocation, err)
	}
}

func TestDeleteComputeMachineForwardsOwnershipAndExactMachineIdentity(t *testing.T) {
	provider := NewTencentProvider()
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Action != "destroy_compute_allocation" || request.AccountID != "acct-alpha" || request.Pool.NodePoolID != "np-basic" ||
			request.Allocation.ID != "compute-alpha" || request.Allocation.MachineName != "machine-alpha" ||
			request.Allocation.InstanceID != "ins-alpha" || request.Allocation.NodeName != "node-alpha" || request.Allocation.PrivateIP != "10.0.0.8" {
			t.Fatalf("destroy request lost ownership or machine identity: %#v", request)
		}
		return provisionerResponse{OK: true, Status: "destroyed"}, nil
	}
	err := provider.DeleteComputeMachine(context.Background(), ProviderMachine{
		MachineID: "machine-alpha", InstanceID: "ins-alpha", NodeName: "node-alpha", PrivateIP: "10.0.0.8",
	}, MachineOwnership{ResourceID: "compute-alpha", AccountID: "acct-alpha", NodePoolID: "np-basic"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReconcileComputePoolPreservesRawMachineCount(t *testing.T) {
	provider := NewTencentProvider()
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Action != "reconcile_compute_pool" {
			t.Fatalf("provider request = %#v", request)
		}
		return provisionerResponse{OK: true, ProviderData: map[string]string{"currentReplicas": "1"}}, nil
	}

	state, err := provider.ReconcileComputePool(context.Background(), ComputePoolDemand{PoolID: "basic", PackageID: "basic", DesiredReplicas: 0})
	if err != nil || state.CurrentReplicas != 1 {
		t.Fatalf("pool state = %#v err=%v", state, err)
	}
}

func TestReconcileComputePoolPreservesDemandWhenProvisionerProcessFails(t *testing.T) {
	provider := NewTencentProvider()
	provider.provision = func(_ context.Context, _ provisionerRequest) (provisionerResponse, error) {
		return provisionerResponse{}, errors.New("provisioner unavailable")
	}

	state, err := provider.ReconcileComputePool(context.Background(), ComputePoolDemand{PoolID: "basic", PackageID: "basic", NodePoolID: "np-basic", DesiredReplicas: 2})
	if err == nil || state.PoolID != "basic" || state.NodePoolID != "np-basic" || state.DesiredReplicas != 2 {
		t.Fatalf("pool state = %#v err=%v", state, err)
	}
}

func TestWorkspaceManifestUsesHostNetworkOnDedicatedTKENode(t *testing.T) {
	t.Setenv("OPL_WORKSPACE_IMAGE", "workspace-image:test")
	t.Setenv("OPL_IMAGE_PULL_SECRET_NAME", "pull-secret")
	t.Setenv("OPL_AIONUI_ADMIN_PASSWORD_SEED", "workspace-secret-2026-very-long")
	t.Setenv("OPL_CODEX_API_KEY", "forbidden-global-key")
	compute := ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", PackageID: "basic", NodeSelector: map[string]any{"cloud.tencent.com/node-instance-id": "np-basic-2"}}
	storage := StorageVolume{ProviderResourceID: "disk-storage-alpha", ProviderData: map[string]string{"pvcName": "opl-storage-alpha-data"}}
	tags := map[string]string{"opl_account_id": "acct-alpha", "opl_workspace_id": "ws-alpha", "opl_resource_id": "compute-alpha", "opl_operation_id": "op-alpha"}
	var manifest map[string]any
	if err := json.Unmarshal(workspaceManifest("ws-alpha", "Alpha", "token", "opl-compute-alpha", compute, storage, "opl-gateway-acct-alpha", tags), &manifest); err != nil {
		t.Fatalf("decode workspace manifest: %v", err)
	}
	var deployment map[string]any
	var service map[string]any
	var secret map[string]any
	for _, item := range manifest["items"].([]any) {
		candidate := item.(map[string]any)
		if candidate["kind"] == "Deployment" {
			deployment = candidate
		}
		if candidate["kind"] == "Service" {
			service = candidate
		}
		if candidate["kind"] == "Secret" {
			secret = candidate
		}
	}
	secretData := secret["data"].(map[string]any)
	passwordBytes := decodeSecretValue(t, secretData, "webui_password")
	if string(passwordBytes) != "opl_jngdohVMGgp2Kdvpg4f-OLuNAa1!" {
		t.Fatalf("workspace must derive a per-workspace WebUI password, got %q", string(passwordBytes))
	}
	sessionSecretBytes := decodeSecretValue(t, secretData, "webui_session_secret")
	if len(sessionSecretBytes) < 32 || string(sessionSecretBytes) == string(passwordBytes) {
		t.Fatalf("workspace must derive an independent WebUI session secret")
	}
	if _, ok := secretData["opl_gateway_api_key"]; ok {
		t.Fatalf("workspace Secret must not copy the account Gateway key: %#v", secretData)
	}
	if _, ok := secretData["OPL_AIONUI_ADMIN_PASSWORD"]; ok {
		t.Fatalf("workspace must not expose retired AionUI password env secret: %#v", secretData)
	}
	if _, ok := secretData["OPL_CODEX_API_KEY"]; ok {
		t.Fatalf("workspace must not expose gateway key through env-style OPL_CODEX_API_KEY: %#v", secretData)
	}
	podSpec := nested(deployment, "spec", "template", "spec").(map[string]any)
	if nested(deployment, "metadata", "labels", "oplcloud.cn/workspace-id") != "ws-alpha" {
		t.Fatalf("deployment must carry workspace label for stateless runtime lookup: %#v", nested(deployment, "metadata", "labels"))
	}
	if nested(deployment, "metadata", "annotations", "opl_operation_id") != "op-alpha" {
		t.Fatalf("deployment must carry OPL cost tag annotations: %#v", nested(deployment, "metadata", "annotations"))
	}
	if nested(deployment, "metadata", "labels", "oplcloud.cn/resource-id") != "compute-alpha" {
		t.Fatalf("deployment must carry OPL cost labels: %#v", nested(deployment, "metadata", "labels"))
	}
	selector := nested(service, "spec", "selector").(map[string]any)
	if selector["oplcloud.cn/workspace-id"] != nil || selector["oplcloud.cn/operation-id"] != nil || selector["oplcloud.cn/resource-id"] != nil {
		t.Fatalf("service selector must not include mutable workspace cost labels: %#v", selector)
	}
	if !selectorMatches(service, deployment) {
		t.Fatalf("service selector must match deployment pod labels: selector=%#v labels=%#v", selector, nested(deployment, "spec", "template", "metadata", "labels"))
	}
	if podSpec["hostNetwork"] != true || podSpec["dnsPolicy"] != "ClusterFirstWithHostNet" {
		t.Fatalf("workspace pod must use host networking on dedicated TKE nodes: %#v", podSpec)
	}
	toleration := podSpec["tolerations"].([]any)[0].(map[string]any)
	if toleration["key"] != "tke.cloud.tencent.com/eni-ip-unavailable" || toleration["effect"] != "NoSchedule" {
		t.Fatalf("workspace pod must tolerate TKE ENI readiness taint: %#v", toleration)
	}
	container := podSpec["containers"].([]any)[0].(map[string]any)
	if _, ok := podSpec["initContainers"]; ok {
		t.Fatalf("workspace must let one-person-lab-app cloud mode configure gateway access, not run retired bootstrap init containers: %#v", podSpec["initContainers"])
	}
	resources := container["resources"].(map[string]any)
	requests := resources["requests"].(map[string]any)
	limits := resources["limits"].(map[string]any)
	if requests["cpu"] != "1" || requests["memory"] != "2Gi" {
		t.Fatalf("workspace requests must leave room for node overhead: %#v", requests)
	}
	if limits["cpu"] != "2" || limits["memory"] != "4Gi" {
		t.Fatalf("workspace limits must preserve the package shape: %#v", limits)
	}
	env := envMap(container["env"].([]any))
	if _, ok := env["OPL_SHARE_TOKEN"]; ok {
		t.Fatalf("workspace must not receive a fake URL authentication token: %#v", env)
	}
	if env["AIONUI_ALLOW_REMOTE"] != "true" {
		t.Fatalf("workspace must allow remote AionUI access: %#v", env)
	}
	if env["OPL_WEBUI_DEPLOYMENT_MODE"] != "cloud" || env["OPL_WEBUI_AUTH_MODE"] != "password" {
		t.Fatalf("workspace must start one-person-lab-app in explicit cloud password mode: %#v", env)
	}
	if env["OPL_WEBUI_USERNAME"] != "opl" ||
		env["OPL_WEBUI_PASSWORD_FILE"] != "/run/secrets/opl_webui_password" ||
		env["OPL_WEBUI_SESSION_SECRET_FILE"] != "/run/secrets/webui_session_secret" ||
		env["OPL_GATEWAY_API_KEY_FILE"] != "/run/secrets/opl_gateway_api_key" {
		t.Fatalf("workspace must point one-person-lab-app at mounted secret files: %#v", env)
	}
	if _, ok := container["envFrom"]; ok {
		t.Fatalf("workspace must not import cloud secrets as environment variables: %#v", container["envFrom"])
	}
	if _, ok := container["lifecycle"]; ok {
		t.Fatalf("workspace must not use retired postStart password bootstrap: %#v", container["lifecycle"])
	}
	mounts := volumeMountMap(container["volumeMounts"].([]any))
	if mounts["workspace-secrets"] != "/run/secrets" {
		t.Fatalf("workspace must mount cloud secrets at /run/secrets: %#v", mounts)
	}
	secretVolume := findVolume(podSpec["volumes"].([]any), "workspace-secrets")
	if secretVolume == nil || nested(secretVolume, "projected", "sources") == nil {
		t.Fatalf("workspace must source cloud secret files from the workspace Secret: %#v", podSpec["volumes"])
	}
	sources := nested(secretVolume, "projected", "sources").([]any)
	if nested(sources[0].(map[string]any), "secret", "name") != "opl-compute-alpha-env" || nested(sources[1].(map[string]any), "secret", "name") != "opl-gateway-acct-alpha" {
		t.Fatalf("workspace must project its runtime Secret and account Gateway Secret: %#v", sources)
	}
	if nested(sources[0].(map[string]any), "secret", "items").([]any)[0].(map[string]any)["path"] != "opl_webui_password" ||
		nested(sources[1].(map[string]any), "secret", "items").([]any)[0].(map[string]any)["path"] != "opl_gateway_api_key" {
		t.Fatalf("workspace password secret path must match one-person-lab-app cloud compose: %#v", secretVolume)
	}
}

func TestTencentProviderWritesAccountGatewaySecretWithoutReturningRawKey(t *testing.T) {
	provider := NewTencentProvider()
	var applied []byte
	provider.kubectl = func(_ context.Context, args []string, stdin []byte) ([]byte, error) {
		if !slices.Equal(args, []string{"apply", "-f", "-"}) {
			t.Fatalf("kubectl args = %#v", args)
		}
		applied = append([]byte(nil), stdin...)
		return nil, nil
	}

	secret, err := provider.UpsertGatewaySecret(context.Background(), GatewaySecretInput{AccountID: "acct-alpha", GatewayAPIKey: "raw-gateway-key", IdempotencyKey: "gateway-once"})

	if err != nil || secret.SecretRef == "" || secret.Version == "" || !strings.HasPrefix(secret.Fingerprint, "sha256:") {
		t.Fatalf("gateway secret=%#v err=%v", secret, err)
	}
	if strings.Contains(fmt.Sprintf("%#v", secret), "raw-gateway-key") {
		t.Fatalf("gateway secret response leaked raw key: %#v", secret)
	}
	var manifest map[string]any
	if err := json.Unmarshal(applied, &manifest); err != nil {
		t.Fatalf("decode Gateway Secret: %v", err)
	}
	if manifest["kind"] != "Secret" || nested(manifest, "metadata", "name") != secret.SecretRef || nested(manifest, "stringData", "opl_gateway_api_key") != "raw-gateway-key" {
		t.Fatalf("account Gateway Secret manifest = %#v", manifest)
	}
	rotated, err := provider.UpsertGatewaySecret(context.Background(), GatewaySecretInput{AccountID: "acct-alpha", GatewayAPIKey: "rotated-gateway-key", IdempotencyKey: "gateway-rotate"})
	if err != nil || rotated.SecretRef != secret.SecretRef || rotated.Version == secret.Version || rotated.Fingerprint == secret.Fingerprint {
		t.Fatalf("rotated Gateway Secret=%#v original=%#v err=%v", rotated, secret, err)
	}
}

func TestWorkspaceManifestSkipsGatewaySecretWhenCodexKeyMissing(t *testing.T) {
	t.Setenv("OPL_WORKSPACE_IMAGE", "workspace-image:test")
	t.Setenv("OPL_IMAGE_PULL_SECRET_NAME", "pull-secret")
	t.Setenv("OPL_AIONUI_ADMIN_PASSWORD_SEED", "workspace-secret-2026-very-long")
	compute := ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", PackageID: "basic", NodeSelector: map[string]any{"cloud.tencent.com/node-instance-id": "np-basic-2"}}
	storage := StorageVolume{ProviderResourceID: "pvc/opl-storage-alpha-data"}
	var manifest map[string]any
	if err := json.Unmarshal(workspaceManifest("ws-alpha", "Alpha", "token", "opl-compute-alpha", compute, storage, "", nil), &manifest); err != nil {
		t.Fatalf("decode workspace manifest: %v", err)
	}
	var deployment map[string]any
	var secret map[string]any
	for _, item := range manifest["items"].([]any) {
		candidate := item.(map[string]any)
		if candidate["kind"] == "Deployment" {
			deployment = candidate
		}
		if candidate["kind"] == "Secret" {
			secret = candidate
		}
	}
	if _, ok := secret["data"].(map[string]any)["opl_gateway_api_key"]; ok {
		t.Fatalf("workspace secret must not contain empty gateway key: %#v", secret["data"])
	}
	container := nested(deployment, "spec", "template", "spec", "containers").([]any)[0].(map[string]any)
	if _, ok := envMap(container["env"].([]any))["OPL_GATEWAY_API_KEY_FILE"]; ok {
		t.Fatalf("workspace must not point at a missing gateway key file: %#v", container["env"])
	}
	secretVolume := findVolume(nested(deployment, "spec", "template", "spec", "volumes").([]any), "workspace-secrets")
	if len(nested(secretVolume, "projected", "sources").([]any)) != 1 {
		t.Fatalf("workspace volume must not reference a missing gateway key: %#v", secretVolume)
	}
}

func TestTencentRuntimeCreationUsesActualReadinessAfterApply(t *testing.T) {
	t.Setenv("OPL_AIONUI_ADMIN_PASSWORD_SEED", "workspace-secret-2026-very-long")
	provider := NewTencentProvider()
	var calls [][]string
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if slices.Equal(args, []string{"apply", "-f", "-"}) {
			return nil, nil
		}
		if slices.Equal(args, []string{"get", "deployment,service", "-l", "oplcloud.cn/workspace-id=ws-alpha", "-o", "json"}) {
			return mustJSON(map[string]any{"kind": "List", "items": []any{}}), nil
		}
		t.Fatalf("unexpected kubectl args: %#v", args)
		return nil, nil
	}
	runtime, err := provider.CreateWorkspaceRuntime(context.Background(), WorkspaceRuntimeInput{WorkspaceID: "ws-alpha", GatewaySecretRef: "opl-gateway-acct-alpha", IdempotencyKey: "runtime-unready"}, ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", ServiceName: "opl-compute-alpha"}, StorageVolume{ID: "storage-alpha", ProviderResourceID: "pvc/opl-storage-alpha-data"})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	if runtime.Ready || runtime.Status != "not_found" || runtime.Access.CredentialStatus == "configured" || len(calls) != 2 {
		t.Fatalf("apply must be followed by actual readiness: runtime=%#v calls=%#v", runtime, calls)
	}
}

func TestTencentStorageAttachmentVerifiesBoundStaticVolumeBeforeRuntime(t *testing.T) {
	type fixture struct {
		input   StorageAttachmentInput
		compute ComputeAllocation
		volume  StorageVolume
		items   []any
	}
	newFixture := func() fixture {
		labels := map[string]any{"oplcloud.cn/account-id": "acct-alpha", "oplcloud.cn/workspace-id": "ws-alpha", "oplcloud.cn/storage-id": "storage-alpha"}
		pv := map[string]any{
			"kind": "PersistentVolume", "metadata": map[string]any{"name": "opl-storage-alpha-pv", "labels": labels},
			"spec": map[string]any{"persistentVolumeReclaimPolicy": "Retain", "storageClassName": "", "csi": map[string]any{"driver": "com.tencent.cloud.csi.cbs", "volumeHandle": "disk-storage-alpha"}},
		}
		pvc := map[string]any{
			"kind": "PersistentVolumeClaim", "metadata": map[string]any{"name": "opl-storage-alpha-data", "labels": labels},
			"spec": map[string]any{"storageClassName": "", "volumeName": "opl-storage-alpha-pv"}, "status": map[string]any{"phase": "Bound"},
		}
		return fixture{
			input:   StorageAttachmentInput{WorkspaceID: "ws-alpha", ComputeID: "compute-alpha", VolumeID: "storage-alpha", IdempotencyKey: "attach-alpha", OperationID: "op-attach-alpha"},
			compute: ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "running"},
			volume: StorageVolume{
				ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "ready", ProviderResourceID: "disk-storage-alpha",
				ProviderData: map[string]string{"pvName": "opl-storage-alpha-pv", "pvcName": "opl-storage-alpha-data"},
			},
			items: []any{pv, pvc},
		}
	}
	create := func(current fixture) (StorageAttachment, [][]string, error) {
		provider := NewTencentProvider()
		calls := [][]string{}
		provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
			calls = append(calls, append([]string(nil), args...))
			if slices.Equal(args, []string{"get", "pv/opl-storage-alpha-pv", "pvc/opl-storage-alpha-data", "--ignore-not-found", "-o", "json"}) {
				return mustJSON(map[string]any{"kind": "List", "items": current.items}), nil
			}
			return mustJSON(map[string]any{"kind": "List", "items": []any{}}), nil
		}
		attachment, err := provider.CreateStorageAttachment(context.Background(), current.input, current.compute, current.volume)
		return attachment, calls, err
	}

	t.Run("pre-runtime exact binding", func(t *testing.T) {
		attachment, calls, err := create(newFixture())
		if err != nil || attachment.Status != "attached" || attachment.ProviderAttachmentID != "pv/opl-storage-alpha-pv:pvc/opl-storage-alpha-data" || len(calls) != 1 {
			t.Fatalf("attachment=%#v err=%v calls=%#v", attachment, err, calls)
		}
	})
	t.Run("PV omitted empty storage class", func(t *testing.T) {
		current := newFixture()
		delete(current.items[0].(map[string]any)["spec"].(map[string]any), "storageClassName")
		if attachment, _, err := create(current); err != nil || attachment.Status != "attached" {
			t.Fatalf("omitted empty PV storage class attachment=%#v err=%v", attachment, err)
		}
	})

	for _, tc := range []struct {
		name      string
		configure func(*fixture)
	}{
		{name: "compute identity", configure: func(current *fixture) { current.compute.ID = "compute-other" }},
		{name: "volume identity", configure: func(current *fixture) { current.volume.ID = "storage-other" }},
		{name: "account ownership", configure: func(current *fixture) { current.volume.AccountID = "acct-other" }},
		{name: "workspace ownership", configure: func(current *fixture) { current.volume.WorkspaceID = "ws-other" }},
		{name: "PVC pending", configure: func(current *fixture) {
			current.items[1].(map[string]any)["status"] = map[string]any{"phase": "Pending"}
		}},
		{name: "PVC wrong PV", configure: func(current *fixture) {
			current.items[1].(map[string]any)["spec"].(map[string]any)["volumeName"] = "pv-other"
		}},
		{name: "PV wrong disk", configure: func(current *fixture) {
			current.items[0].(map[string]any)["spec"].(map[string]any)["csi"].(map[string]any)["volumeHandle"] = "disk-other"
		}},
		{name: "PVC wrong owner", configure: func(current *fixture) {
			current.items[1].(map[string]any)["metadata"].(map[string]any)["labels"].(map[string]any)["oplcloud.cn/workspace-id"] = "ws-other"
		}},
		{name: "PV missing", configure: func(current *fixture) { current.items = current.items[1:] }},
		{name: "PV ambiguous", configure: func(current *fixture) { current.items = append(current.items, current.items[0]) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := newFixture()
			tc.configure(&current)
			if attachment, _, err := create(current); err == nil || attachment.Status == "attached" {
				t.Fatalf("invalid static binding attached storage: attachment=%#v err=%v", attachment, err)
			}
		})
	}
}

func TestRuntimeStatusVerifiesFinalMountAfterPreRuntimeAttachment(t *testing.T) {
	t.Setenv("OPL_WORKSPACE_IMAGE", "workspace-image:test")
	provider := NewTencentProvider()
	deployment := map[string]any{
		"kind":     "Deployment",
		"metadata": map[string]any{"name": "opl-compute-alpha", "labels": map[string]any{"oplcloud.cn/workspace-id": "ws-alpha"}},
		"spec": map[string]any{"template": map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": "workspace"}}, "spec": map[string]any{
			"containers": []any{map[string]any{"name": "workspace", "image": "workspace-image:test"}},
			"volumes":    []any{map[string]any{"persistentVolumeClaim": map[string]any{"claimName": "opl-storage-alpha-data"}}},
		}}},
		"status": map[string]any{"readyReplicas": 1, "availableReplicas": 1},
	}
	service := map[string]any{
		"kind":     "Service",
		"metadata": map[string]any{"name": "opl-compute-alpha", "labels": map[string]any{"oplcloud.cn/workspace-id": "ws-alpha"}},
		"spec":     map[string]any{"selector": map[string]any{"app": "workspace"}},
	}
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		if len(args) == 6 && args[0] == "get" && args[1] == "deployment,service" && args[2] == "-l" && args[3] == "oplcloud.cn/workspace-id=ws-alpha" {
			return mustJSON(map[string]any{"kind": "List", "items": []any{deployment, service}}), nil
		}
		if slices.Equal(args, []string{"get", "pod", "-l", "oplcloud.cn/workspace-id=ws-alpha", "-o", "json"}) {
			return mustJSON(map[string]any{"kind": "List", "items": []any{map[string]any{
				"kind": "Pod",
				"metadata": map[string]any{"name": "opl-compute-alpha-7d6c", "labels": map[string]any{
					"oplcloud.cn/workspace-id": "ws-alpha",
				}},
				"spec": map[string]any{"nodeName": "10.0.0.8"},
				"status": map[string]any{
					"phase": "Running",
					"conditions": []any{
						map[string]any{"type": "PodScheduled", "status": "True"},
						map[string]any{"type": "Ready", "status": "True"},
					},
					"containerStatuses": []any{map[string]any{"name": "workspace", "ready": true, "restartCount": 0, "state": map[string]any{"running": map[string]any{}}}},
				},
			}}}), nil
		}
		want := []string{"get", "deployment/opl-compute-alpha", "pvc/opl-storage-alpha-data", "service/opl-compute-alpha", "ingress/opl-cloud", "endpoints/opl-compute-alpha", "secret/opl-compute-alpha-env", "--ignore-not-found", "-o", "json"}
		if !slices.Equal(args, want) {
			t.Fatalf("kubectl args = %#v, want %#v", args, want)
		}
		return mustJSON(map[string]any{"kind": "List", "items": []any{
			deployment,
			map[string]any{"kind": "PersistentVolumeClaim", "metadata": map[string]any{"name": "opl-storage-alpha-data"}, "status": map[string]any{"phase": "Bound"}},
			service,
			map[string]any{"kind": "Ingress", "metadata": map[string]any{"name": "opl-cloud"}, "spec": map[string]any{"rules": []any{map[string]any{"http": map[string]any{"paths": []any{map[string]any{"path": "/", "backend": map[string]any{"service": map[string]any{"name": gatewayService, "port": map[string]any{"number": 8787}}}}}}}}}},
			map[string]any{"kind": "Endpoints", "metadata": map[string]any{"name": "opl-compute-alpha"}, "subsets": []any{map[string]any{"addresses": []any{map[string]any{"ip": "10.0.0.8"}}}}},
			map[string]any{"kind": "Secret", "metadata": map[string]any{"name": "opl-compute-alpha-env"}, "data": map[string]any{"webui_password": base64.StdEncoding.EncodeToString([]byte("secret-password"))}},
		}}), nil
	}

	status, err := provider.WorkspaceRuntimeStatus(context.Background(), "ws-alpha")

	if err != nil {
		t.Fatalf("runtime status: %v", err)
	}
	if !status.Ready {
		t.Fatalf("status = %#v, want ready", status)
	}
	verified := map[string]bool{}
	for _, check := range status.Checks {
		verified[check.Name] = check.OK
	}
	for _, name := range []string{"pvc_bound", "deployment_uses_retained_pvc", "deployment_ready"} {
		if !verified[name] {
			t.Fatalf("runtime must own final mount/readiness proof %q: %#v", name, status.Checks)
		}
	}
	if status.Access.Password != "secret-password" || status.Access.Username != webuiUsername || status.Access.CredentialStatus != "configured" || status.Access.SecretRef != "opl-compute-alpha-env" {
		t.Fatalf("runtime access must come transiently from Workspace Secret: %#v", status.Access)
	}
}

func TestDestroyWorkspaceRuntimeDeletesOnlyWorkspaceResources(t *testing.T) {
	provider := NewTencentProvider()
	var calls [][]string
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if args[0] == "get" {
			return []byte(`{"items":[{"kind":"Deployment","metadata":{"name":"opl-compute-alpha","labels":{"oplcloud.cn/workspace-id":"ws-alpha"}},"spec":{"template":{"spec":{"volumes":[{"persistentVolumeClaim":{"claimName":"opl-storage-alpha-data"}}]}}}},{"kind":"Service","metadata":{"name":"opl-compute-alpha","labels":{"oplcloud.cn/workspace-id":"ws-alpha"}}}]}`), nil
		}
		return nil, nil
	}

	runtime, err := provider.DestroyWorkspaceRuntime(context.Background(), "ws-alpha")
	if err != nil || runtime.Status != "destroyed" || runtime.WorkspaceID != "ws-alpha" || runtime.Access.Password != "" {
		t.Fatalf("destroy runtime = %#v err=%v", runtime, err)
	}
	if len(calls) != 2 || calls[1][0] != "delete" || !slices.Contains(calls[1], "deployment/opl-compute-alpha") || !slices.Contains(calls[1], "service/opl-compute-alpha") || !slices.Contains(calls[1], "secret/opl-compute-alpha-env") || slices.Contains(calls[1], "ingress/opl-cloud") {
		t.Fatalf("kubectl calls = %#v", calls)
	}
}

func TestDestroyWorkspaceRuntimeReturnsDiscoveryFailure(t *testing.T) {
	provider := NewTencentProvider()
	provider.kubectl = func(context.Context, []string, []byte) ([]byte, error) {
		return nil, errors.New("cluster unavailable")
	}

	if _, err := provider.DestroyWorkspaceRuntime(context.Background(), "ws-alpha"); err == nil || !strings.Contains(err.Error(), "cluster unavailable") {
		t.Fatalf("destroy error = %v", err)
	}
}

func TestDestroyWorkspaceRuntimeDeletesSecretOnlyRemnant(t *testing.T) {
	provider := NewTencentProvider()
	var calls [][]string
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if args[0] == "get" {
			return []byte(`{"items":[{"kind":"Secret","metadata":{"name":"opl-compute-alpha-env","labels":{"oplcloud.cn/workspace-id":"ws-alpha"}}}]}`), nil
		}
		return nil, nil
	}

	if _, err := provider.DestroyWorkspaceRuntime(context.Background(), "ws-alpha"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0][1] != "deployment,service,secret" || !slices.Contains(calls[1], "secret/opl-compute-alpha-env") || slices.Contains(calls[1], "ingress/opl-cloud") {
		t.Fatalf("kubectl calls = %#v", calls)
	}
}

func TestRuntimeAccessFromMissingWorkspaceSecret(t *testing.T) {
	access, check := runtimeAccessFromSecret(nil, "opl-compute-alpha-env")
	if access.Password != "" || access.CredentialStatus != "missing" || access.SecretRef != "opl-compute-alpha-env" || check.OK {
		t.Fatalf("missing Secret access = %#v check = %#v", access, check)
	}
}

func TestPodRuntimeDetailsReportsWaitingReason(t *testing.T) {
	details := podRuntimeDetails([]any{map[string]any{
		"kind":     "Pod",
		"metadata": map[string]any{"name": "opl-compute-alpha-7d6c"},
		"spec":     map[string]any{"nodeName": "10.0.0.8"},
		"status": map[string]any{
			"phase": "Pending",
			"conditions": []any{
				map[string]any{"type": "PodScheduled", "status": "True"},
				map[string]any{"type": "Ready", "status": "False"},
			},
			"containerStatuses": []any{map[string]any{
				"name":         "workspace",
				"ready":        false,
				"restartCount": 3,
				"state":        map[string]any{"waiting": map[string]any{"reason": "CrashLoopBackOff"}},
			}},
		},
	}})

	if details["phase"] != "Pending" || details["podReady"] != false {
		t.Fatalf("unexpected pod details: %#v", details)
	}
	containers := details["containers"].([]map[string]any)
	if containers[0]["state"] != "waiting" || containers[0]["reason"] != "CrashLoopBackOff" {
		t.Fatalf("container waiting reason missing: %#v", containers)
	}
}

func TestExecuteKubectlKeepsStderrWarningsOutOfJSON(t *testing.T) {
	binDir := t.TempDir()
	kubectl := filepath.Join(binDir, "kubectl")
	script := `#!/bin/sh
printf 'Warning: endpoints is deprecated\n' >&2
printf '{"kind":"List","items":[]}\n'
`
	if err := os.WriteFile(kubectl, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake kubectl: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OPL_K8S_NAMESPACE", "opl-cloud")

	raw, err := executeKubectl(context.Background(), []string{"get", "endpoints/opl-compute-alpha", "-o", "json"}, nil)

	if err != nil {
		t.Fatalf("execute kubectl: %v", err)
	}
	if !json.Valid(raw) {
		t.Fatalf("kubectl output must stay valid JSON, got %q", string(raw))
	}
}

func TestTencentProviderPublishesWorkspaceContentAtomically(t *testing.T) {
	provider := NewTencentProvider()
	var calls [][]string
	var uploaded []byte
	var uploadSizes []int
	stdinBytes := 0
	provider.kubectl = func(_ context.Context, args []string, stdin []byte) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if args[0] == "get" {
			return mustJSON(map[string]any{"items": []any{map[string]any{
				"kind": "Deployment", "metadata": map[string]any{"name": "opl-workspace-alpha", "labels": map[string]any{"oplcloud.cn/workspace-id": "workspace-alpha"}},
				"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"volumes": []any{map[string]any{"name": "workspace-data", "persistentVolumeClaim": map[string]any{"claimName": "pvc-alpha"}}}}}},
			}}}), nil
		}
		if stdin != nil {
			stdinBytes += len(stdin)
		}
		if len(args) > 7 && args[3] == "sh" {
			chunk, err := base64.StdEncoding.DecodeString(args[7])
			if err != nil {
				return nil, err
			}
			uploaded = append(uploaded, chunk...)
			uploadSizes = append(uploadSizes, len(chunk))
		}
		if len(args) > 3 && args[3] == "sha256sum" {
			return []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256(uploaded), args[4])), nil
		}
		return nil, nil
	}
	body := bytes.Repeat([]byte("v"), (32<<10)+1)
	if err := provider.PublishWorkspaceContent(context.Background(), "workspace-alpha", "inputs/paper.txt", body); err != nil {
		t.Fatalf("publish: %v", err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(body))
	temporary := "/projects/inputs/paper.txt.opl-upload-" + digest[:12]
	if !bytes.Equal(uploaded, body) || stdinBytes != 0 || !slices.Equal(uploadSizes, []int{32 << 10, 1}) || len(calls) != 7 || !slices.Equal(calls[1], []string{"exec", "deployment/opl-workspace-alpha", "--", "mkdir", "-p", "/projects/inputs"}) || !slices.Equal(calls[2], []string{"exec", "deployment/opl-workspace-alpha", "--", "rm", "-f", temporary}) || calls[3][0] != "exec" || calls[3][3] != "sh" || calls[3][8] != temporary || calls[4][0] != "exec" || calls[4][3] != "sh" || calls[4][8] != temporary || !slices.Equal(calls[5], []string{"exec", "deployment/opl-workspace-alpha", "--", "mv", temporary, "/projects/inputs/paper.txt"}) || !slices.Equal(calls[6], []string{"exec", "deployment/opl-workspace-alpha", "--", "sha256sum", "/projects/inputs/paper.txt"}) {
		t.Fatalf("calls=%#v uploadSizes=%#v stdinBytes=%d", calls, uploadSizes, stdinBytes)
	}
}

func TestTencentProviderReportsWorkspaceContentMismatchWithoutBody(t *testing.T) {
	provider := NewTencentProvider()
	actualDigest := fmt.Sprintf("%x", sha256.Sum256([]byte("different-secret-body")))
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		if args[0] == "get" {
			return mustJSON(map[string]any{"items": []any{map[string]any{
				"kind": "Deployment", "metadata": map[string]any{"name": "opl-workspace-alpha", "labels": map[string]any{"oplcloud.cn/workspace-id": "workspace-alpha"}},
			}}}), nil
		}
		if len(args) > 3 && args[3] == "sha256sum" {
			return []byte(actualDigest + "  /projects/inputs/paper.txt\n"), nil
		}
		return nil, nil
	}
	body := []byte("expected-secret-body")
	err := provider.PublishWorkspaceContent(context.Background(), "workspace-alpha", "inputs/paper.txt", body)
	expectedDigest := fmt.Sprintf("%x", sha256.Sum256(body))
	if err == nil || !strings.Contains(err.Error(), expectedDigest) || !strings.Contains(err.Error(), actualDigest) || strings.Contains(err.Error(), string(body)) {
		t.Fatalf("safe mismatch diagnostics = %v", err)
	}
}

func TestTencentProviderReportsWorkspaceContentDigestCommandFailure(t *testing.T) {
	provider := NewTencentProvider()
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		if args[0] == "get" {
			return mustJSON(map[string]any{"items": []any{map[string]any{
				"kind": "Deployment", "metadata": map[string]any{"name": "opl-workspace-alpha", "labels": map[string]any{"oplcloud.cn/workspace-id": "workspace-alpha"}},
			}}}), nil
		}
		if len(args) > 3 && args[3] == "sha256sum" {
			return nil, fmt.Errorf("exit status 1: forbidden")
		}
		return nil, nil
	}
	err := provider.PublishWorkspaceContent(context.Background(), "workspace-alpha", "inputs/paper.txt", []byte("expected"))
	if err == nil || !strings.Contains(err.Error(), "workspace_content_digest_command_failed") || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("readback diagnostics = %v", err)
	}
}

func TestTencentProviderRejectsInvalidWorkspaceContentDigestOutput(t *testing.T) {
	provider := NewTencentProvider()
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		if args[0] == "get" {
			return mustJSON(map[string]any{"items": []any{map[string]any{
				"kind": "Deployment", "metadata": map[string]any{"name": "opl-workspace-alpha", "labels": map[string]any{"oplcloud.cn/workspace-id": "workspace-alpha"}},
			}}}), nil
		}
		if len(args) > 3 && args[3] == "sha256sum" {
			return []byte("not-a-digest\n"), nil
		}
		return nil, nil
	}
	err := provider.PublishWorkspaceContent(context.Background(), "workspace-alpha", "inputs/paper.txt", []byte("expected"))
	if err == nil || err.Error() != "workspace_content_digest_invalid" {
		t.Fatalf("invalid digest diagnostics = %v", err)
	}
}

func TestTencentProviderCreatesStaticRetainedCBSVolumeInComputeZone(t *testing.T) {
	provider := NewTencentProvider()
	var provisioned provisionerRequest
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		provisioned = request
		return provisionerResponse{
			OK: true, StorageVolumeID: "disk-storage-alpha", CBSStatus: "UNATTACHED", Status: "provider_ready", ProviderRequestID: "req-create-cbs",
			ProviderData: map[string]string{"diskType": "CLOUD_BSSD", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": "2026-08-16 00:00:00", "zone": "ap-guangzhou-3", "sizeGb": "10"},
		}, nil
	}
	var applied []byte
	provider.kubectl = func(_ context.Context, args []string, stdin []byte) ([]byte, error) {
		if !slices.Equal(args, []string{"apply", "-f", "-"}) {
			t.Fatalf("kubectl args = %#v", args)
		}
		applied = append([]byte(nil), stdin...)
		return nil, nil
	}

	volume, err := provider.CreateStorageVolume(context.Background(), StorageVolumeInput{
		ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ComputeID: "compute-alpha", Zone: "ap-guangzhou-3", SizeGB: 10,
		IdempotencyKey: "storage-once", OperationID: "op-storage-alpha",
	})

	if err != nil || volume.ProviderResourceID != "disk-storage-alpha" || volume.Status != "pending" || volume.Zone != "ap-guangzhou-3" || volume.Deadline != "2026-08-16 00:00:00" {
		t.Fatalf("created volume=%#v err=%v", volume, err)
	}
	if provisioned.Action != "create_storage_volume" || provisioned.Storage.ID != "storage-alpha" || provisioned.Storage.Zone != "ap-guangzhou-3" || provisioned.Storage.SizeGB != 10 {
		t.Fatalf("provisioner request = %#v", provisioned)
	}
	var manifest map[string]any
	if err := json.Unmarshal(applied, &manifest); err != nil {
		t.Fatalf("decode static volume manifest: %v", err)
	}
	items := manifest["items"].([]any)
	pv, pvc := items[0].(map[string]any), items[1].(map[string]any)
	if pv["kind"] != "PersistentVolume" || nested(pv, "spec", "csi", "driver") != "com.tencent.cloud.csi.cbs" || nested(pv, "spec", "csi", "volumeHandle") != "disk-storage-alpha" {
		t.Fatalf("static PV must bind the exact CBS disk: %#v", pv)
	}
	if nested(pv, "spec", "persistentVolumeReclaimPolicy") != "Retain" || nested(pv, "spec", "storageClassName") != "" || nested(pv, "spec", "accessModes", "0") != nil {
		// AccessModes is asserted below because nested intentionally handles maps only.
		t.Fatalf("static PV retention/class mismatch: %#v", pv["spec"])
	}
	if pv["spec"].(map[string]any)["accessModes"].([]any)[0] != "ReadWriteOnce" || nested(pv, "spec", "nodeAffinity", "required", "nodeSelectorTerms") == nil {
		t.Fatalf("static PV must be RWO with Zone affinity: %#v", pv["spec"])
	}
	if pvc["kind"] != "PersistentVolumeClaim" || nested(pvc, "spec", "storageClassName") != "" || nested(pvc, "spec", "volumeName") != nested(pv, "metadata", "name") {
		t.Fatalf("static PVC must prebind the retained PV: pv=%#v pvc=%#v", pv, pvc)
	}
}

func TestTencentProviderPreservesCBSFactsWhenStaticBindingFails(t *testing.T) {
	provider := NewTencentProvider()
	provider.provision = func(context.Context, provisionerRequest) (provisionerResponse, error) {
		return provisionerResponse{
			OK: true, StorageVolumeID: "disk-storage-alpha", CBSStatus: "UNATTACHED", Status: "provider_ready", ProviderRequestID: "req-create-cbs",
			ProviderData: map[string]string{"diskChargeType": "PREPAID", "diskType": "CLOUD_BSSD", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": "2026-08-16 00:00:00", "zone": "ap-guangzhou-3", "sizeGb": "10"},
		}, nil
	}
	provider.kubectl = func(context.Context, []string, []byte) ([]byte, error) { return nil, errors.New("cluster unavailable") }
	volume, err := provider.CreateStorageVolume(context.Background(), StorageVolumeInput{ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Zone: "ap-guangzhou-3", SizeGB: 10})
	if err == nil || volume.ProviderResourceID != "disk-storage-alpha" || volume.ProviderData["diskChargeType"] != "PREPAID" || volume.RenewFlag != "NOTIFY_AND_MANUAL_RENEW" || volume.Deadline == "" || volume.Zone != "ap-guangzhou-3" {
		t.Fatalf("partial CBS result lost provider facts: volume=%#v err=%v", volume, err)
	}
}

func TestTencentProviderPreservesCBSIdentityFromFailedCreateReadback(t *testing.T) {
	provider := NewTencentProvider()
	provider.provision = func(context.Context, provisionerRequest) (provisionerResponse, error) {
		return provisionerResponse{OK: false, StorageVolumeID: "disk-storage-alpha", ProviderRequestID: "req-create-cbs", ErrorCode: "tencent_cbs_readback_mismatch"}, nil
	}

	volume, err := provider.CreateStorageVolume(context.Background(), StorageVolumeInput{
		ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Zone: "ap-guangzhou-3", SizeGB: 10,
	})
	if err == nil || volume.ID != "storage-alpha" || volume.ProviderResourceID != "disk-storage-alpha" || volume.ProviderRequestID != "req-create-cbs" {
		t.Fatalf("failed create readback lost CBS identity: volume=%#v err=%v", volume, err)
	}
}

func TestTencentProviderStorageReadinessRequiresCBSAndBoundPVC(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cbsStatus  string
		pvcPhase   string
		wantStatus string
	}{
		{name: "unattached and bound", cbsStatus: "UNATTACHED", pvcPhase: "Bound", wantStatus: "ready"},
		{name: "attached and bound", cbsStatus: "ATTACHED", pvcPhase: "Bound", wantStatus: "ready"},
		{name: "provider pending", cbsStatus: "CREATING", pvcPhase: "Bound", wantStatus: "pending"},
		{name: "runtime pending", cbsStatus: "UNATTACHED", pvcPhase: "Pending", wantStatus: "pending"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := NewTencentProvider()
			provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
				if request.Action != "sync_storage_volume" || request.AccountID != "acct-alpha" || request.Storage.ID != "disk-storage-alpha" ||
					!reflect.DeepEqual(request.Tags, oplCostTags("acct-alpha", "ws-alpha", "storage-alpha", "op-storage-alpha")) {
					t.Fatalf("provisioner request = %#v", request)
				}
				return provisionerResponse{OK: true, StorageVolumeID: "disk-storage-alpha", CBSStatus: tc.cbsStatus, Status: "provider_ready", ProviderRequestID: "req-sync-cbs", ProviderData: map[string]string{"zone": "ap-guangzhou-3", "diskType": "CLOUD_BSSD", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": "2026-08-16 00:00:00", "sizeGb": "10"}}, nil
			}
			provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
				if args[0] == "apply" {
					return nil, nil
				}
				return mustJSON(map[string]any{"kind": "PersistentVolumeClaim", "metadata": map[string]any{"name": "opl-storage-alpha-data"}, "status": map[string]any{"phase": tc.pvcPhase}}), nil
			}
			volume, err := provider.SyncStorageVolume(context.Background(), StorageVolume{
				ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ProviderResourceID: "disk-storage-alpha", SizeGB: 10, Zone: "ap-guangzhou-3", DiskType: "CLOUD_BSSD",
				CostTags:     oplCostTags("acct-alpha", "ws-alpha", "storage-alpha", "op-storage-alpha"),
				ProviderData: map[string]string{"pvName": "opl-storage-alpha-pv", "pvcName": "opl-storage-alpha-data"},
			})
			if err != nil || volume.Status != tc.wantStatus || volume.CBSStatus != tc.cbsStatus {
				t.Fatalf("synced volume=%#v err=%v", volume, err)
			}
		})
	}
}

func TestTencentProviderSyncStorageVolumeStopsOnConfirmedCBSAbsence(t *testing.T) {
	provider := NewTencentProvider()
	provider.provision = func(context.Context, provisionerRequest) (provisionerResponse, error) {
		return provisionerResponse{
			OK: true, StorageVolumeID: "disk-storage-alpha", CBSStatus: "NOT_FOUND", Status: "external_deleted", ProviderRequestID: "req-cbs-not-found",
			ProviderData: map[string]string{"storageVolumeId": "disk-storage-alpha", "cbsStatus": "NOT_FOUND"},
		}, nil
	}
	provider.kubectl = func(context.Context, []string, []byte) ([]byte, error) {
		t.Fatal("confirmed CBS absence must not apply a PV or PVC")
		return nil, nil
	}
	volume, err := provider.SyncStorageVolume(context.Background(), StorageVolume{
		ID: "storage-alpha", ProviderResourceID: "disk-storage-alpha", SizeGB: 10, Zone: "ap-guangzhou-3", DiskType: "CLOUD_BSSD",
	})
	if err != nil || volume.Status != "external_deleted" || volume.CBSStatus != "NOT_FOUND" || volume.ProviderResourceID != "disk-storage-alpha" || volume.ProviderRequestID != "req-cbs-not-found" {
		t.Fatalf("confirmed CBS absence = %#v, err=%v", volume, err)
	}
}

func TestTencentProviderDestroyStorageReleasesKubernetesBindingButRetainsCBS(t *testing.T) {
	provider := NewTencentProvider()
	var args []string
	provider.provision = func(context.Context, provisionerRequest) (provisionerResponse, error) {
		t.Fatal("destroying static binding must not call a CBS destroy action")
		return provisionerResponse{}, nil
	}
	provider.kubectl = func(_ context.Context, current []string, _ []byte) ([]byte, error) {
		args = append([]string(nil), current...)
		return nil, nil
	}
	volume, err := provider.DestroyStorageVolume(context.Background(), StorageVolume{
		ID: "storage-alpha", ProviderResourceID: "disk-storage-alpha", ProviderData: map[string]string{"pvName": "opl-storage-alpha-pv", "pvcName": "opl-storage-alpha-data"},
	})
	if err != nil || volume.Status != "retained" || volume.ProviderResourceID != "disk-storage-alpha" {
		t.Fatalf("destroyed volume=%#v err=%v", volume, err)
	}
	if !slices.Contains(args, "pvc/opl-storage-alpha-data") || !slices.Contains(args, "pv/opl-storage-alpha-pv") {
		t.Fatalf("static binding delete args = %#v", args)
	}
}

func TestTencentProviderRenewsCBSAndPersistsDeadlineReadback(t *testing.T) {
	provider := NewTencentProvider()
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Action != "renew_storage_volume" || request.AccountID != "acct-alpha" || request.Storage.Deadline != "2026-08-16T00:00:00Z" ||
			!reflect.DeepEqual(request.Tags, oplCostTags("acct-alpha", "ws-alpha", "storage-alpha", "op-storage-alpha")) {
			t.Fatalf("renew request = %#v", request)
		}
		return provisionerResponse{OK: true, StorageVolumeID: "disk-storage-alpha", CBSStatus: "UNATTACHED", Status: "provider_ready", ProviderRequestID: "req-renew-cbs", ProviderData: map[string]string{"deadline": "2026-09-16T00:00:00Z", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "diskChargeType": "PREPAID", "zone": "ap-guangzhou-3", "diskType": "CLOUD_BSSD", "sizeGb": "10"}}, nil
	}
	volume, err := provider.RenewStorageVolume(context.Background(), StorageVolume{
		ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ProviderResourceID: "disk-storage-alpha", SizeGB: 10, Zone: "ap-guangzhou-3", DiskType: "CLOUD_BSSD", Deadline: "2026-08-16T00:00:00Z",
		CostTags: oplCostTags("acct-alpha", "ws-alpha", "storage-alpha", "op-storage-alpha"),
	})
	if err != nil || volume.Deadline != "2026-09-16T00:00:00Z" || volume.RenewFlag != "NOTIFY_AND_MANUAL_RENEW" || volume.ProviderRequestID != "req-renew-cbs" {
		t.Fatalf("renewed volume=%#v err=%v", volume, err)
	}
}

func TestTencentProviderRenewsCVMAndPersistsBillingReadback(t *testing.T) {
	provider := NewTencentProvider()
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Action != "renew_compute_allocation" || request.Allocation.ID != "compute-alpha" || request.Allocation.InstanceID != "ins-basic-1" || request.Allocation.Deadline != "2026-08-16T00:00:00Z" || request.Pool.InstanceType != "SA5.MEDIUM4" || request.Zone != "ap-guangzhou-3" || !reflect.DeepEqual(request.Tags, oplCostTags("acct-alpha", "ws-alpha", "compute-alpha", "owner-alpha")) {
			t.Fatalf("renew request = %#v", request)
		}
		return provisionerResponse{
			OK: true, InstanceID: "ins-basic-1", CVMStatus: "RUNNING", Status: "provider_ready", ProviderRequestID: "req-renew-cvm",
			ProviderData: map[string]string{"deadline": "2026-09-16T00:00:00Z", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "chargeType": "PREPAID", "renewalResult": "renewed", "zone": "ap-guangzhou-3", "instanceType": "SA5.MEDIUM4", "opl_account_id": "acct-alpha", "opl_workspace_id": "ws-alpha", "opl_resource_id": "compute-alpha", "opl_operation_id": "owner-alpha"},
		}, nil
	}
	allocation, err := provider.RenewComputeAllocation(context.Background(), ComputeAllocation{
		ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", InstanceID: "ins-basic-1", Status: "running", Deadline: "2026-08-16T00:00:00Z", ProviderData: map[string]string{"zone": "ap-guangzhou-3", "instanceType": "SA5.MEDIUM4"}, CostTags: oplCostTags("acct-alpha", "ws-alpha", "compute-alpha", "owner-alpha"),
	})
	if err != nil || allocation.Deadline != "2026-09-16T00:00:00Z" || allocation.RenewFlag != "NOTIFY_AND_MANUAL_RENEW" || allocation.ChargeType != "PREPAID" || allocation.ProviderData["renewalResult"] != "renewed" || allocation.ProviderRequestID != "req-renew-cvm" {
		t.Fatalf("renewed allocation=%#v err=%v", allocation, err)
	}
}

func TestTencentProviderRenewFailuresPreserveProviderIdentityAndReadback(t *testing.T) {
	t.Run("CVM", func(t *testing.T) {
		provider := NewTencentProvider()
		provider.provision = func(context.Context, provisionerRequest) (provisionerResponse, error) {
			return provisionerResponse{OK: false, InstanceID: "ins-basic-1", ProviderRequestID: "req-renew-cvm", ErrorCode: "tencent_cvm_renewal_unconfirmed", ProviderData: map[string]string{"deadline": "2026-08-16T00:00:00Z", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "chargeType": "PREPAID", "describeCvmRequestId": "req-read-cvm"}}, nil
		}
		allocation, err := provider.RenewComputeAllocation(context.Background(), ComputeAllocation{
			ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", InstanceID: "ins-basic-1", Deadline: "2026-08-16T00:00:00Z",
			ProviderData: map[string]string{"instanceType": "SA5.MEDIUM4", "zone": "ap-guangzhou-3"}, CostTags: oplCostTags("acct-alpha", "ws-alpha", "compute-alpha", "owner-alpha"),
		})
		if err == nil || allocation.ID != "compute-alpha" || allocation.InstanceID != "ins-basic-1" || allocation.ProviderRequestID != "req-renew-cvm" || allocation.ProviderData["describeCvmRequestId"] != "req-read-cvm" {
			t.Fatalf("failed CVM renewal lost evidence: allocation=%#v err=%v", allocation, err)
		}
	})
	t.Run("CBS", func(t *testing.T) {
		provider := NewTencentProvider()
		provider.provision = func(context.Context, provisionerRequest) (provisionerResponse, error) {
			return provisionerResponse{OK: false, StorageVolumeID: "disk-storage-alpha", ProviderRequestID: "req-renew-cbs", ErrorCode: "tencent_cbs_renewal_unconfirmed", CBSStatus: "UNATTACHED", ProviderData: map[string]string{"deadline": "2026-08-16 00:00:00", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "diskChargeType": "PREPAID", "describeCbsRequestId": "req-read-cbs"}}, nil
		}
		volume, err := provider.RenewStorageVolume(context.Background(), StorageVolume{ID: "storage-alpha", ProviderResourceID: "disk-storage-alpha", SizeGB: 10, Zone: "ap-guangzhou-3", DiskType: "CLOUD_BSSD", Deadline: "2026-08-16 00:00:00"})
		if err == nil || volume.ID != "storage-alpha" || volume.ProviderResourceID != "disk-storage-alpha" || volume.ProviderRequestID != "req-renew-cbs" || volume.ProviderData["describeCbsRequestId"] != "req-read-cbs" {
			t.Fatalf("failed CBS renewal lost evidence: volume=%#v err=%v", volume, err)
		}
	})
}

func TestTencentProviderSnapshotsAndRestoresStorageWithoutMutatingSource(t *testing.T) {
	t.Setenv("OPL_WORKSPACE_VOLUME_SNAPSHOT_CLASS", "cbs-snapshot")
	t.Setenv("OPL_WORKSPACE_STORAGE_CLASS", "cbs")
	provider := NewTencentProvider()
	var manifests [][]byte
	var waits [][]string
	provider.kubectl = func(_ context.Context, args []string, stdin []byte) ([]byte, error) {
		if len(args) >= 2 && args[0] == "apply" {
			manifests = append(manifests, append([]byte(nil), stdin...))
		}
		if len(args) >= 2 && args[0] == "wait" {
			waits = append(waits, append([]string(nil), args...))
		}
		return nil, nil
	}
	source := StorageVolume{ID: "vol-source", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "ready", ProviderResourceID: "pvc/opl-storage-source-data", SizeGB: 10}
	snapshot, err := provider.CreateStorageSnapshot(context.Background(), StorageSnapshotInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", VolumeID: source.ID, IdempotencyKey: "snapshot-once"}, source)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifests) != 1 || !bytes.Contains(manifests[0], []byte(`"kind":"VolumeSnapshot"`)) || !bytes.Contains(manifests[0], []byte(`"persistentVolumeClaimName":"opl-storage-source-data"`)) {
		t.Fatalf("snapshot manifest = %s", manifests)
	}
	restored, err := provider.RestoreStorageSnapshot(context.Background(), StorageRestoreInput{SnapshotID: snapshot.ID, AccountID: "acct-alpha", WorkspaceID: "ws-restored", TargetVolumeID: "vol-restored", IdempotencyKey: "restore-once"}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if restored.ID != "vol-restored" || restored.SizeGB != 10 || len(manifests) != 2 || !bytes.Contains(manifests[1], []byte(`"kind":"PersistentVolumeClaim"`)) || !bytes.Contains(manifests[1], []byte(`"name":"`+resourceName(snapshot.ProviderSnapshotRef)+`"`)) {
		t.Fatalf("restored=%#v manifest=%s", restored, manifests[1])
	}
	if bytes.Contains(manifests[1], []byte("opl-storage-source-data")) {
		t.Fatalf("restore manifest must reference snapshot, not source pvc: %s", manifests[1])
	}
	if snapshot.Status != "ready" || restored.Status != "ready" || len(waits) != 2 {
		t.Fatalf("snapshot=%#v restored=%#v waits=%#v", snapshot, restored, waits)
	}
}

func envMap(entries []any) map[string]string {
	values := map[string]string{}
	for _, entry := range entries {
		asMap, _ := entry.(map[string]any)
		values[stringValue(asMap["name"])] = stringValue(asMap["value"])
	}
	return values
}

func decodeSecretValue(t *testing.T, data map[string]any, key string) []byte {
	t.Helper()
	encoded, ok := data[key].(string)
	if !ok || encoded == "" {
		t.Fatalf("secret missing %s: %#v", key, data)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode %s: %v", key, err)
	}
	return decoded
}

func volumeMountMap(entries []any) map[string]string {
	values := map[string]string{}
	for _, entry := range entries {
		asMap, _ := entry.(map[string]any)
		values[stringValue(asMap["name"])] = stringValue(asMap["mountPath"])
	}
	return values
}

func findVolume(entries []any, name string) map[string]any {
	for _, entry := range entries {
		asMap, _ := entry.(map[string]any)
		if stringValue(asMap["name"]) == name {
			return asMap
		}
	}
	return nil
}
