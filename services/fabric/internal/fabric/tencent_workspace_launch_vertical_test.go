package fabric

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

func newTencentWorkspaceLaunchService(t *testing.T) (*Service, *MemoryOperationStore, *TencentProvider, WorkspaceLaunchPreflight, string, string) {
	t.Helper()
	setProtectedResourceEnv(t)
	store := NewMemoryOperationStore()
	provider := NewTencentProvider()
	service := NewServiceWithOperationStore(provider, store)
	image := "uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app@sha256:" + strings.Repeat("a", 64)
	launchHash := strings.Repeat("b", 64)
	admission := workspaceLaunchPreflightAdmission{
		SchemaVersion: 1,
		Input: WorkspaceLaunchPreflightInput{
			SchemaVersion: 1, LaunchOperationID: "launch-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha",
			PackageID: "basic", SizeGB: 10, WorkspaceImageDigest: image, RequestHash: launchHash,
		},
		ProviderProfileRef: "tencent-tke",
	}
	admission.BindingRef = "fabric-preflight:" + hashInput(admission)
	if err := service.persistWorkspaceLaunchPreflight(context.Background(), admission); err != nil {
		t.Fatal(err)
	}
	return service, store, provider, WorkspaceLaunchPreflight{BindingRef: admission.BindingRef}, image, launchHash
}

func seedTencentWorkspaceLaunchStage(t *testing.T, store OperationStore, preflight WorkspaceLaunchPreflight, image, launchHash, stage, action string, requestResources, resultResources WorkspaceLaunchResources, state tencentWorkspaceLaunchState, gatewayKeyID int64) {
	t.Helper()
	input := workspaceLaunchStageFixtureInput(preflight, image, launchHash, stage, action, requestResources)
	if gatewayKeyID > 0 {
		input.GatewayCredential = &WorkspaceLaunchGatewayCredential{KeyID: gatewayKeyID, Value: "not-persisted"}
	}
	operation, record, err := newWorkspaceLaunchStageOperation(input, "tencent-tke", func() time.Time { return time.Now().UTC() })
	if err != nil {
		t.Fatal(err)
	}
	providerState, err := encodeTencentWorkspaceLaunchState(state)
	if err != nil {
		t.Fatal(err)
	}
	operation.Status, operation.FinishedAt = "succeeded", time.Now().UTC()
	record.Resources, record.ProviderState = resultResources, providerState
	setWorkspaceLaunchStageRecord(&operation, record)
	if err := store.Append(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
}

func tencentStorageBindingReadback(t *testing.T, manifest []byte, drift bool) []byte {
	t.Helper()
	var list map[string]any
	if err := json.Unmarshal(manifest, &list); err != nil {
		t.Fatal(err)
	}
	items, ok := list["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("static storage manifest=%#v", list)
	}
	for _, item := range items {
		resource := item.(map[string]any)
		if resource["kind"] == "PersistentVolumeClaim" {
			resource["status"] = map[string]any{"phase": "Bound"}
		}
	}
	if drift {
		nested(items[0].(map[string]any), "spec", "csi").(map[string]any)["volumeHandle"] = "disk-drift"
	}
	return mustJSON(map[string]any{"kind": "List", "items": items})
}

func TestTencentWorkspaceLaunchStorageReplayRequiresExactCBSAndStaticBindingReadback(t *testing.T) {
	service, store, provider, preflight, image, launchHash := newTencentWorkspaceLaunchService(t)
	compute := ComputeAllocation{
		ID: "ca-compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", NodePoolID: "np-basic",
		MachineName: "machine-alpha", NodeName: "node-alpha", InstanceID: "ins-alpha", Zone: "ap-guangzhou-3", Provider: "tencent-tke",
	}
	computeResources := WorkspaceLaunchResources{ComputeAllocationID: compute.ID, ComputeBindingRef: "launch-alpha:ensure_compute_allocation"}
	seedTencentWorkspaceLaunchStage(t, store, preflight, image, launchHash,
		"ensure_compute_allocation", "ensure_compute_allocation", WorkspaceLaunchResources{}, computeResources,
		tencentWorkspaceLaunchState{Compute: &compute}, 0)

	input := workspaceLaunchStageFixtureInput(preflight, image, launchHash, "storage", "ensure_storage", computeResources)
	storageID := workspaceLaunchStorageID(input.Binding)
	providerCalls, applyCalls, bindingGETs := 0, 0, 0
	staticManifest := []byte(nil)
	staticDrift := false
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		providerCalls++
		switch request.Action {
		case "create_storage_volume":
			return provisionerResponse{
				OK: true, Status: "created", StorageVolumeID: "disk-alpha", CBSStatus: "UNATTACHED", ProviderRequestID: "req-cbs-create",
				ProviderData: map[string]string{"diskType": "CLOUD_BSSD", "zone": compute.Zone, "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": "2026-09-12T00:00:00Z"},
			}, nil
		case "sync_storage_volume":
			return provisionerResponse{
				OK: true, Status: "ready", StorageVolumeID: "disk-alpha", CBSStatus: "UNATTACHED", ProviderRequestID: "req-cbs-read",
				ProviderData: map[string]string{"diskType": "CLOUD_BSSD", "zone": compute.Zone, "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": "2026-09-12T00:00:00Z"},
			}, nil
		default:
			t.Fatalf("unexpected provisioner action %q", request.Action)
			return provisionerResponse{}, nil
		}
	}
	provider.kubectl = func(_ context.Context, args []string, stdin []byte) ([]byte, error) {
		switch {
		case slices.Equal(args, []string{"apply", "-f", "-"}):
			applyCalls++
			staticManifest = append([]byte(nil), stdin...)
			return nil, nil
		case len(args) == 6 && args[0] == "get" && strings.HasPrefix(args[1], "pv/") && strings.HasPrefix(args[2], "pvc/"):
			bindingGETs++
			return tencentStorageBindingReadback(t, staticManifest, staticDrift), nil
		default:
			t.Fatalf("unexpected kubectl args=%#v", args)
			return nil, nil
		}
	}

	result, err := service.EnsureWorkspaceLaunchStage(context.Background(), input)
	if err != nil || result.State != "ready" || result.Resources.StorageID != storageID {
		t.Fatalf("storage result=%#v err=%v", result, err)
	}
	if providerCalls != 3 || applyCalls != 1 || bindingGETs != 2 {
		t.Fatalf("first ensure providerCalls=%d applyCalls=%d bindingGETs=%d", providerCalls, applyCalls, bindingGETs)
	}

	result, err = service.EnsureWorkspaceLaunchStage(context.Background(), input)
	if err != nil || result.State != "ready" || providerCalls != 4 || applyCalls != 1 || bindingGETs != 3 {
		t.Fatalf("GET-only replay result=%#v err=%v providerCalls=%d applyCalls=%d bindingGETs=%d", result, err, providerCalls, applyCalls, bindingGETs)
	}

	staticDrift = true
	result, err = service.ReadWorkspaceLaunchStage(context.Background(), input)
	if err == nil || result.State != "" || providerCalls != 5 || applyCalls != 1 || bindingGETs != 4 {
		t.Fatalf("drift readback result=%#v err=%v providerCalls=%d applyCalls=%d bindingGETs=%d", result, err, providerCalls, applyCalls, bindingGETs)
	}
}

type tencentRuntimeReadbackFixture struct {
	t            *testing.T
	applied      []byte
	applyCalls   int
	getCalls     int
	drift        func(map[string]map[string]any)
	workspaceID  string
	storage      StorageVolume
	gatewayRef   string
	gatewayKeyID int64
	fingerprint  string
}

func (fixture *tencentRuntimeReadbackFixture) resources() map[string]map[string]any {
	fixture.t.Helper()
	var list map[string]any
	if err := json.Unmarshal(fixture.applied, &list); err != nil {
		fixture.t.Fatal(err)
	}
	resources := map[string]map[string]any{}
	for _, raw := range list["items"].([]any) {
		resource := raw.(map[string]any)
		resources[stringValue(resource["kind"])] = resource
	}
	deployment := resources["Deployment"]
	deployment["status"] = map[string]any{"observedGeneration": 1, "updatedReplicas": 1, "readyReplicas": 1, "availableReplicas": 1}
	deployment["metadata"].(map[string]any)["generation"] = 1
	serviceName := stringValue(nested(deployment, "metadata", "name"))
	secret := resources["Secret"]
	secret["data"] = map[string]any{"webui_password": b64("runtime-password"), "webui_session_secret": b64("runtime-session")}
	resources["PersistentVolumeClaim"] = map[string]any{
		"kind": "PersistentVolumeClaim", "metadata": map[string]any{"name": storagePVCName(fixture.storage)}, "status": map[string]any{"phase": "Bound"},
	}
	resources["Ingress"] = map[string]any{
		"kind": "Ingress", "metadata": map[string]any{"name": "opl-cloud"},
		"spec": map[string]any{"rules": []any{map[string]any{"http": map[string]any{"paths": []any{map[string]any{"path": "/", "backend": map[string]any{"service": map[string]any{"name": gatewayService, "port": map[string]any{"number": 8787}}}}}}}}},
	}
	resources["Endpoints"] = map[string]any{
		"kind": "Endpoints", "metadata": map[string]any{"name": serviceName}, "subsets": []any{map[string]any{"addresses": []any{map[string]any{"ip": "10.0.0.8"}}}},
	}
	pod := map[string]any{
		"kind": "Pod",
		"metadata": map[string]any{
			"name": serviceName + "-pod", "labels": cloneJSONMap(nested(deployment, "spec", "template", "metadata", "labels").(map[string]any)),
		},
		"spec": cloneJSONMap(nested(deployment, "spec", "template", "spec").(map[string]any)),
		"status": map[string]any{
			"phase": "Running", "conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
			"containerStatuses": []any{map[string]any{"name": "workspace", "ready": true, "restartCount": 0, "state": map[string]any{"running": map[string]any{}}}},
		},
	}
	resources["Pod"] = pod
	if fixture.drift != nil {
		fixture.drift(resources)
	}
	return resources
}

func (fixture *tencentRuntimeReadbackFixture) kubectl(_ context.Context, args []string, stdin []byte) ([]byte, error) {
	fixture.t.Helper()
	if slices.Equal(args, []string{"apply", "-f", "-"}) {
		fixture.applyCalls++
		fixture.applied = append([]byte(nil), stdin...)
		return nil, nil
	}
	fixture.getCalls++
	resources := fixture.resources()
	deployment, service, policy := resources["Deployment"], resources["Service"], resources["NetworkPolicy"]
	switch {
	case len(args) >= 2 && args[0] == "get" && args[1] == "deployment,service,networkpolicy":
		return mustJSON(map[string]any{"kind": "List", "items": []any{deployment, service, policy}}), nil
	case slices.Equal(args, []string{"get", "networkpolicy", "-o", "json"}):
		return mustJSON(map[string]any{"kind": "List", "items": []any{policy}}), nil
	case len(args) == 6 && args[0] == "get" && args[1] == "pod":
		return mustJSON(map[string]any{"kind": "List", "items": []any{resources["Pod"]}}), nil
	case len(args) == 4 && args[0] == "get" && strings.HasPrefix(args[1], "deployment/"):
		return mustJSON(deployment), nil
	case len(args) == 10 && args[0] == "get" && strings.HasPrefix(args[1], "deployment/"):
		return mustJSON(map[string]any{"kind": "List", "items": []any{
			deployment, resources["PersistentVolumeClaim"], service, resources["Ingress"], resources["Endpoints"], resources["Secret"],
		}}), nil
	default:
		fixture.t.Fatalf("unexpected kubectl args=%#v", args)
		return nil, nil
	}
}

func TestTencentWorkspaceLaunchRuntimeReplayRequiresExactRuntimeAndGatewayBindingReadback(t *testing.T) {
	service, store, provider, preflight, image, launchHash := newTencentWorkspaceLaunchService(t)
	compute := ComputeAllocation{
		ID: "ca-compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", NodePoolID: "np-basic",
		MachineName: "machine-alpha", NodeName: "node-alpha", InstanceID: "ins-alpha", Zone: "ap-guangzhou-3", Provider: "tencent-tke",
	}
	storage := StorageVolume{
		ID: "vol-storage-alpha", OperationID: "launch-alpha:storage", AccountID: compute.AccountID, WorkspaceID: compute.WorkspaceID,
		Status: "ready", Provider: "tencent-tke", ProviderResourceID: "disk-alpha", SizeGB: 10, DiskType: "CLOUD_BSSD", Zone: compute.Zone,
		ProviderData: map[string]string{"pvName": "vol-storage-alpha-pv", "pvcName": "vol-storage-alpha-data"},
		CostTags:     oplCostTags(compute.AccountID, compute.WorkspaceID, "vol-storage-alpha", "launch-alpha:storage"),
	}
	attachment := StorageAttachment{
		ID: "att-alpha", OperationID: "launch-alpha:attachment", WorkspaceID: compute.WorkspaceID, ComputeID: compute.ID,
		VolumeID: storage.ID, Status: "attached", Provider: "tencent-tke",
	}
	secret := GatewaySecret{SecretRef: "opl-gateway-ws-alpha", Version: "19", Fingerprint: "sha256:" + strings.Repeat("d", 64)}

	computeResources := WorkspaceLaunchResources{ComputeAllocationID: compute.ID, ComputeBindingRef: "launch-alpha:ensure_compute_allocation"}
	storageResources := computeResources
	storageResources.StorageID, storageResources.StorageBindingRef = storage.ID, "launch-alpha:storage"
	attachmentResources := storageResources
	attachmentResources.AttachmentID, attachmentResources.AttachmentBindingRef = attachment.ID, "launch-alpha:attachment"
	secretRequestResources := attachmentResources
	secretRequestResources.GatewaySecretFingerprint = secret.Fingerprint
	secretResources := secretRequestResources
	secretResources.GatewaySecretRef, secretResources.GatewaySecretVersion, secretResources.SecretBindingRef = secret.SecretRef, secret.Version, "launch-alpha:secret"

	seedTencentWorkspaceLaunchStage(t, store, preflight, image, launchHash,
		"ensure_compute_allocation", "ensure_compute_allocation", WorkspaceLaunchResources{}, computeResources,
		tencentWorkspaceLaunchState{Compute: &compute}, 0)
	seedTencentWorkspaceLaunchStage(t, store, preflight, image, launchHash,
		"storage", "ensure_storage", computeResources, storageResources,
		tencentWorkspaceLaunchState{Storage: &storage}, 0)
	seedTencentWorkspaceLaunchStage(t, store, preflight, image, launchHash,
		"attachment", "ensure_attachment", storageResources, attachmentResources,
		tencentWorkspaceLaunchState{Attachment: &attachment}, 0)
	seedTencentWorkspaceLaunchStage(t, store, preflight, image, launchHash,
		"secret", "ensure_gateway_secret", secretRequestResources, secretResources,
		tencentWorkspaceLaunchState{Secret: &secret}, 19)

	input := workspaceLaunchStageFixtureInput(preflight, image, launchHash, "runtime", "ensure_runtime", secretResources)
	runtimeID := "rt_" + stableSuffix(input.Binding.WorkspaceID, input.Binding.FabricOperationID)[:18]
	serviceName := k8sName(compute.ID)
	fixture := &tencentRuntimeReadbackFixture{
		t: t, workspaceID: input.Binding.WorkspaceID, storage: storage, gatewayRef: secret.SecretRef, gatewayKeyID: 19, fingerprint: secret.Fingerprint,
	}
	provider.kubectl = fixture.kubectl

	result, err := service.EnsureWorkspaceLaunchStage(context.Background(), input)
	if err != nil || result.State != "ready" || result.Resources.RuntimeID != runtimeID || result.Resources.RuntimeServiceName != serviceName || result.Resources.RuntimeURL == "" {
		status, statusErr := provider.WorkspaceRuntimeStatus(context.Background(), input.Binding.WorkspaceID)
		t.Fatalf("runtime result=%#v err=%v status=%#v statusErr=%v", result, err, status, statusErr)
	}
	annotations := nested(fixture.resources()["Deployment"], "spec", "template", "metadata", "annotations").(map[string]any)
	if annotations["opl.medopl.cn/gateway-secret-ref"] != secret.SecretRef || annotations["opl.medopl.cn/gateway-key-id"] != "19" || annotations["opl.medopl.cn/gateway-fingerprint"] != secret.Fingerprint {
		t.Fatalf("runtime manifest gateway binding=%#v", annotations)
	}
	firstGETs := fixture.getCalls
	result, err = service.EnsureWorkspaceLaunchStage(context.Background(), input)
	if err != nil || result.State != "ready" || fixture.applyCalls != 1 || fixture.getCalls <= firstGETs {
		t.Fatalf("GET-only replay result=%#v err=%v applyCalls=%d getCalls=%d", result, err, fixture.applyCalls, fixture.getCalls)
	}

	tests := []struct {
		name  string
		drift func(map[string]map[string]any)
	}{
		{name: "runtime ID", drift: func(resources map[string]map[string]any) {
			metadata := resources["Deployment"]["metadata"].(map[string]any)
			metadata["labels"].(map[string]any)["oplcloud.cn/runtime-id"] = "rt-drift"
			metadata["annotations"].(map[string]any)["opl_resource_id"] = "rt-drift"
		}},
		{name: "operation ID", drift: func(resources map[string]map[string]any) {
			metadata := resources["Deployment"]["metadata"].(map[string]any)
			metadata["labels"].(map[string]any)["oplcloud.cn/runtime-operation-id"] = "operation-drift"
			metadata["annotations"].(map[string]any)["opl_operation_id"] = "operation-drift"
		}},
		{name: "workspace", drift: func(resources map[string]map[string]any) {
			for _, resource := range resources {
				if labels, ok := nested(resource, "metadata", "labels").(map[string]any); ok && labels["oplcloud.cn/workspace-id"] != nil {
					labels["oplcloud.cn/workspace-id"] = "ws-drift"
				}
			}
		}},
		{name: "service", drift: func(resources map[string]map[string]any) {
			for _, kind := range []string{"Deployment", "Service", "NetworkPolicy", "Secret", "Endpoints"} {
				resources[kind]["metadata"].(map[string]any)["name"] = map[string]string{"Secret": "runtime-drift-env"}[kind]
				if stringValue(nested(resources[kind], "metadata", "name")) == "" {
					resources[kind]["metadata"].(map[string]any)["name"] = "runtime-drift"
				}
			}
		}},
		{name: "image", drift: func(resources map[string]map[string]any) {
			nested(resources["Deployment"], "spec", "template", "spec", "containers").([]any)[0].(map[string]any)["image"] = workspaceImageRepository + "@sha256:" + strings.Repeat("e", 64)
		}},
		{name: "account cost tag", drift: func(resources map[string]map[string]any) {
			resources["Deployment"]["metadata"].(map[string]any)["annotations"].(map[string]any)["opl_account_id"] = "acct-drift"
		}},
		{name: "gateway secret ref", drift: func(resources map[string]map[string]any) {
			nested(resources["Deployment"], "spec", "template", "metadata", "annotations").(map[string]any)["opl.medopl.cn/gateway-secret-ref"] = "secret-drift"
		}},
		{name: "gateway key ID", drift: func(resources map[string]map[string]any) {
			nested(resources["Deployment"], "spec", "template", "metadata", "annotations").(map[string]any)["opl.medopl.cn/gateway-key-id"] = "20"
		}},
		{name: "gateway fingerprint", drift: func(resources map[string]map[string]any) {
			nested(resources["Deployment"], "spec", "template", "metadata", "annotations").(map[string]any)["opl.medopl.cn/gateway-fingerprint"] = "sha256:" + strings.Repeat("f", 64)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture.drift = test.drift
			beforeGETs := fixture.getCalls
			result, err := service.ReadWorkspaceLaunchStage(context.Background(), input)
			if err == nil || result.State != "" || fixture.applyCalls != 1 || fixture.getCalls <= beforeGETs {
				t.Fatalf("drift result=%#v err=%v applyCalls=%d getCalls=%d", result, err, fixture.applyCalls, fixture.getCalls)
			}
		})
	}
}
