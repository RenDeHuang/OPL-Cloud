package fabric

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestTencentProviderWorkspaceActivationTruthIsStrictAndReadOnly(t *testing.T) {
	t.Setenv("TENCENT_DEPLOY_CLUSTER_ID", "cls-activation")
	t.Setenv("OPL_BASIC_COMPUTE_INSTANCE_TYPE", "SA5.MEDIUM4")
	image := "registry.example/one-person-lab-app@sha256:" + repeatHex("a", 64)
	t.Setenv("OPL_WORKSPACE_IMAGE", image)

	fixture := newWorkspaceActivationTruthFixture(image)
	provider := NewTencentProvider()
	provider.provision = fixture.provision
	provider.kubectl = fixture.kubectl

	truth, err := provider.WorkspaceActivationTruth(context.Background(), fixture.input, fixture.compute, fixture.storage, fixture.attachment)

	if err != nil || !truth.Ready || truth.Reason != "none" || truth.ErrorClass != "" {
		t.Fatalf("truth=%#v err=%v", truth, err)
	}
	if truth.Runtime.ID != fixture.input.RuntimeID || truth.Runtime.ServiceName != fixture.input.ServiceName || truth.Runtime.PodName != fixture.podName ||
		truth.Runtime.PodIP != fixture.podIP || truth.Runtime.NodeName != fixture.compute.NodeName || truth.Runtime.PVName != fixture.pvName ||
		truth.Runtime.PVCName != fixture.pvcName || truth.Runtime.VolumeAttachmentName != fixture.volumeAttachmentName ||
		!reflect.DeepEqual(truth.Runtime.EndpointIPs, []string{fixture.podIP}) {
		t.Fatalf("runtime identity=%#v", truth.Runtime)
	}
	for _, args := range fixture.calls {
		if len(args) == 0 || args[0] != "get" {
			t.Fatalf("activation truth performed a mutation: %#v", fixture.calls)
		}
	}
}

func TestTencentProviderWorkspaceActivationTruthAcceptsApprovedBareDigest(t *testing.T) {
	t.Setenv("TENCENT_DEPLOY_CLUSTER_ID", "cls-activation")
	t.Setenv("OPL_BASIC_COMPUTE_INSTANCE_TYPE", "SA5.MEDIUM4")
	digest := "sha256:" + repeatHex("9", 64)
	image := "registry.example/one-person-lab-app@" + digest
	t.Setenv("OPL_WORKSPACE_IMAGE", image)

	fixture := newWorkspaceActivationTruthFixture(image)
	fixture.input.WorkspaceImageDigest = digest
	provider := NewTencentProvider()
	provider.provision = fixture.provision
	provider.kubectl = fixture.kubectl

	truth, err := provider.WorkspaceActivationTruth(context.Background(), fixture.input, fixture.compute, fixture.storage, fixture.attachment)

	if err != nil || !truth.Ready || truth.Runtime.ImageID == "" {
		t.Fatalf("bare approved digest truth=%#v err=%v", truth, err)
	}
}

func TestTencentProviderWorkspaceActivationTruthRejectsMultipleRuntimeCandidates(t *testing.T) {
	t.Setenv("TENCENT_DEPLOY_CLUSTER_ID", "cls-activation")
	t.Setenv("OPL_BASIC_COMPUTE_INSTANCE_TYPE", "SA5.MEDIUM4")
	image := "registry.example/one-person-lab-app@sha256:" + repeatHex("b", 64)
	t.Setenv("OPL_WORKSPACE_IMAGE", image)

	fixture := newWorkspaceActivationTruthFixture(image)
	duplicate := cloneJSONMap(fixture.deployment)
	duplicate["metadata"].(map[string]any)["name"] = "opl-compute-alpha-duplicate"
	fixture.items = append(fixture.items, duplicate)
	provider := NewTencentProvider()
	provider.provision = fixture.provision
	provider.kubectl = fixture.kubectl

	truth, err := provider.WorkspaceActivationTruth(context.Background(), fixture.input, fixture.compute, fixture.storage, fixture.attachment)

	if err == nil || truth.Ready || truth.Reason != "multiple_candidate" || truth.ErrorClass != "ownership_conflict" {
		t.Fatalf("multiple candidates truth=%#v err=%v", truth, err)
	}
}

func TestTencentProviderWorkspaceActivationTruthRejectsAttachmentDrift(t *testing.T) {
	t.Setenv("TENCENT_DEPLOY_CLUSTER_ID", "cls-activation")
	t.Setenv("OPL_BASIC_COMPUTE_INSTANCE_TYPE", "SA5.MEDIUM4")
	image := "registry.example/one-person-lab-app@sha256:" + repeatHex("c", 64)
	t.Setenv("OPL_WORKSPACE_IMAGE", image)

	fixture := newWorkspaceActivationTruthFixture(image)
	fixture.deployment["metadata"].(map[string]any)["labels"].(map[string]any)["oplcloud.cn/attachment-id"] = "att-other"
	provider := NewTencentProvider()
	provider.provision = fixture.provision
	provider.kubectl = fixture.kubectl

	truth, err := provider.WorkspaceActivationTruth(context.Background(), fixture.input, fixture.compute, fixture.storage, fixture.attachment)

	if err == nil || truth.Ready || truth.Reason != "identity_mismatch" || truth.ErrorClass != "readback_mismatch" {
		t.Fatalf("attachment drift truth=%#v err=%v", truth, err)
	}
}

func TestTencentProviderWorkspaceActivationTruthIgnoresOtherVolumesOnTargetNode(t *testing.T) {
	t.Setenv("TENCENT_DEPLOY_CLUSTER_ID", "cls-activation")
	t.Setenv("OPL_BASIC_COMPUTE_INSTANCE_TYPE", "SA5.MEDIUM4")
	image := "registry.example/one-person-lab-app@sha256:" + repeatHex("1", 64)
	t.Setenv("OPL_WORKSPACE_IMAGE", image)

	fixture := newWorkspaceActivationTruthFixture(image)
	other := cloneJSONMap(fixture.volumeAttachment)
	other["metadata"].(map[string]any)["name"] = "csi-other-volume"
	other["spec"].(map[string]any)["source"].(map[string]any)["persistentVolumeName"] = "unrelated-pv"
	baseKubectl := fixture.kubectl
	fixtureKubectl := func(ctx context.Context, args []string, input []byte) ([]byte, error) {
		if slices.Equal(args, []string{"get", "volumeattachment", "-o", "json"}) {
			fixture.calls = append(fixture.calls, append([]string(nil), args...))
			return mustJSON(map[string]any{"kind": "List", "items": []any{fixture.volumeAttachment, other}}), nil
		}
		return baseKubectl(ctx, args, input)
	}
	provider := NewTencentProvider()
	provider.provision = fixture.provision
	provider.kubectl = fixtureKubectl

	truth, err := provider.WorkspaceActivationTruth(context.Background(), fixture.input, fixture.compute, fixture.storage, fixture.attachment)

	if err != nil || !truth.Ready || truth.Runtime.VolumeAttachmentName != fixture.volumeAttachmentName {
		t.Fatalf("truth=%#v err=%v", truth, err)
	}
}

func TestTencentProviderWorkspaceActivationTruthRejectsDuplicateWorkspaceTaint(t *testing.T) {
	t.Setenv("TENCENT_DEPLOY_CLUSTER_ID", "cls-activation")
	t.Setenv("OPL_BASIC_COMPUTE_INSTANCE_TYPE", "SA5.MEDIUM4")
	image := "registry.example/one-person-lab-app@sha256:" + repeatHex("2", 64)
	t.Setenv("OPL_WORKSPACE_IMAGE", image)

	fixture := newWorkspaceActivationTruthFixture(image)
	taints := fixture.node["spec"].(map[string]any)["taints"].([]any)
	fixture.node["spec"].(map[string]any)["taints"] = append(taints, map[string]any{
		"key": "oplcloud.cn/workspace-id", "value": "ws-other", "effect": "NoSchedule",
	})
	provider := NewTencentProvider()
	provider.provision = fixture.provision
	provider.kubectl = fixture.kubectl

	truth, err := provider.WorkspaceActivationTruth(context.Background(), fixture.input, fixture.compute, fixture.storage, fixture.attachment)

	if err == nil || truth.Ready || truth.Reason != "identity_mismatch" || truth.ErrorClass != "readback_mismatch" {
		t.Fatalf("truth=%#v err=%v", truth, err)
	}
}

func TestTencentProviderWorkspaceActivationTruthRejectsRuntimeReplicaDrift(t *testing.T) {
	t.Setenv("TENCENT_DEPLOY_CLUSTER_ID", "cls-activation")
	t.Setenv("OPL_BASIC_COMPUTE_INSTANCE_TYPE", "SA5.MEDIUM4")
	image := "registry.example/one-person-lab-app@sha256:" + repeatHex("3", 64)
	t.Setenv("OPL_WORKSPACE_IMAGE", image)

	fixture := newWorkspaceActivationTruthFixture(image)
	fixture.deployment["spec"].(map[string]any)["replicas"] = 2
	fixture.deployment["status"].(map[string]any)["updatedReplicas"] = 2
	fixture.deployment["status"].(map[string]any)["readyReplicas"] = 2
	fixture.deployment["status"].(map[string]any)["availableReplicas"] = 2
	provider := NewTencentProvider()
	provider.provision = fixture.provision
	provider.kubectl = fixture.kubectl

	truth, err := provider.WorkspaceActivationTruth(context.Background(), fixture.input, fixture.compute, fixture.storage, fixture.attachment)

	if err == nil || truth.Ready || truth.Reason != "identity_mismatch" || truth.ErrorClass != "readback_mismatch" {
		t.Fatalf("truth=%#v err=%v", truth, err)
	}
}

func TestTencentProviderWorkspaceActivationTruthPropagatesKubernetesReadErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		raw       []byte
		err       error
		wantClass string
	}{
		{name: "rbac", err: errors.New("Error from server (Forbidden): deployments is forbidden"), wantClass: "iam_rbac"},
		{name: "timeout", err: context.DeadlineExceeded, wantClass: "timeout"},
		{name: "malformed response", raw: []byte(`{"kind":"List","items":`), wantClass: "provider_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TENCENT_DEPLOY_CLUSTER_ID", "cls-activation")
			t.Setenv("OPL_BASIC_COMPUTE_INSTANCE_TYPE", "SA5.MEDIUM4")
			image := "registry.example/one-person-lab-app@sha256:" + repeatHex("d", 64)
			t.Setenv("OPL_WORKSPACE_IMAGE", image)
			fixture := newWorkspaceActivationTruthFixture(image)
			provider := NewTencentProvider()
			provider.provision = fixture.provision
			provider.kubectl = func(context.Context, []string, []byte) ([]byte, error) { return tc.raw, tc.err }

			truth, err := provider.WorkspaceActivationTruth(context.Background(), fixture.input, fixture.compute, fixture.storage, fixture.attachment)

			if err == nil || truth.Ready || truth.Reason != "provider_unavailable" || truth.ErrorClass != tc.wantClass {
				t.Fatalf("truth=%#v err=%v", truth, err)
			}
			if containsAny(err.Error(), "Forbidden", "deployments is forbidden") {
				t.Fatalf("raw kubernetes error leaked: %v", err)
			}
		})
	}
}

type recordingWorkspaceActivationTruthProvider struct {
	testProvider
	calls      int
	input      WorkspaceActivationTruthInput
	compute    ComputeAllocation
	storage    StorageVolume
	attachment StorageAttachment
	result     WorkspaceActivationTruth
	err        error
}

func (p *recordingWorkspaceActivationTruthProvider) WorkspaceActivationTruth(_ context.Context, input WorkspaceActivationTruthInput, compute ComputeAllocation, storage StorageVolume, attachment StorageAttachment) (WorkspaceActivationTruth, error) {
	p.calls++
	p.input, p.compute, p.storage, p.attachment = input, compute, storage, attachment
	result := p.result
	if result.SchemaVersion == 0 {
		result = WorkspaceActivationTruth{
			SchemaVersion: 1, Ready: true, Reason: "none", ComputeState: "ready", StorageState: "ready",
			Compute: compute, Storage: storage, Attachment: attachment,
			Runtime: WorkspaceActivationRuntimeTruth{
				ID: input.RuntimeID, OperationID: input.RuntimeOperationID, ServiceName: input.ServiceName,
				DeploymentName: input.ServiceName, GatewaySecretRef: input.GatewaySecretRef,
			},
			Checks: []Check{},
		}
	}
	return result, p.err
}

func TestWorkspaceActivationTruthServiceBindsPersistedOperationIdentityWithoutWriting(t *testing.T) {
	provider := &recordingWorkspaceActivationTruthProvider{}
	service, store, input := workspaceActivationTruthServiceFixture(t, provider)

	truth, err := service.WorkspaceActivationTruth(context.Background(), input)
	operations, listErr := store.List(context.Background())

	if err != nil || !truth.Ready || provider.calls != 1 || listErr != nil || len(operations) != 4 {
		t.Fatalf("truth=%#v err=%v calls=%d operations=%#v listErr=%v", truth, err, provider.calls, operations, listErr)
	}
	if provider.compute.OperationID != input.ComputeOperationID || provider.storage.OperationID != input.StorageOperationID ||
		provider.attachment.OperationID != input.AttachmentOperationID || provider.input != input {
		t.Fatalf("persisted identity input=%#v compute=%#v storage=%#v attachment=%#v", provider.input, provider.compute, provider.storage, provider.attachment)
	}
}

func TestWorkspaceActivationTruthServiceAllowsExactHistoricalStartedOperation(t *testing.T) {
	provider := &recordingWorkspaceActivationTruthProvider{}
	service, store, input := workspaceActivationTruthServiceFixture(t, provider)
	operations, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	storage := operations[1]
	storage.ID = "fop-storage-started-history"
	storage.OperationID = "op-internal-storage-started-history"
	storage.Status = "started"
	storage.FinishedAt = time.Time{}
	storage.CreatedAt = storage.CreatedAt.Add(-time.Second)
	if err := store.Append(context.Background(), storage); err != nil {
		t.Fatal(err)
	}

	truth, err := service.WorkspaceActivationTruth(context.Background(), input)

	if err != nil || !truth.Ready || provider.calls != 1 {
		t.Fatalf("truth=%#v err=%v calls=%d", truth, err, provider.calls)
	}
}

func TestWorkspaceActivationTruthServiceRejectsLocalIdentityDriftBeforeProvider(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*WorkspaceActivationTruthInput)
	}{
		{name: "compute operation", mutate: func(input *WorkspaceActivationTruthInput) {
			input.ComputeOperationID = "workspace-launch-alpha:compute-other"
		}},
		{name: "storage operation", mutate: func(input *WorkspaceActivationTruthInput) {
			input.StorageOperationID = "workspace-launch-alpha:storage-other"
		}},
		{name: "attachment operation", mutate: func(input *WorkspaceActivationTruthInput) {
			input.AttachmentOperationID = "workspace-launch-alpha:attachment-other"
		}},
		{name: "runtime operation", mutate: func(input *WorkspaceActivationTruthInput) {
			input.RuntimeOperationID = "workspace-launch-alpha:workspace:runtime-other"
		}},
		{name: "runtime id", mutate: func(input *WorkspaceActivationTruthInput) { input.RuntimeID = "rt-other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &recordingWorkspaceActivationTruthProvider{}
			service, store, input := workspaceActivationTruthServiceFixture(t, provider)
			tc.mutate(&input)

			truth, err := service.WorkspaceActivationTruth(context.Background(), input)
			operations, listErr := store.List(context.Background())

			if err == nil || !errors.Is(err, ErrInvalidWorkspaceActivationTruth) || truth.Ready || provider.calls != 0 || listErr != nil || len(operations) != 4 {
				t.Fatalf("truth=%#v err=%v calls=%d operations=%#v listErr=%v", truth, err, provider.calls, operations, listErr)
			}
		})
	}
}

func workspaceActivationTruthServiceFixture(t *testing.T, provider *recordingWorkspaceActivationTruthProvider) (*Service, *MemoryOperationStore, WorkspaceActivationTruthInput) {
	t.Helper()
	compute, storage := monthlyTruthResources()
	compute.OperationID = ""
	compute.NodeName = "10.0.0.8"
	compute.PrivateIP = "10.0.0.8"
	storage.OperationID = ""
	attachment := StorageAttachment{
		ID: "att-alpha", WorkspaceID: compute.WorkspaceID, ComputeID: compute.ID, VolumeID: storage.ID,
		Status: "attached", Provider: "tencent-tke", ProviderAttachmentID: "pv/opl-storage-alpha-pv:pvc/opl-storage-alpha-data", ProviderRequestID: "req-attachment",
	}
	runtime := WorkspaceRuntime{
		ID: "rt-alpha", WorkspaceID: compute.WorkspaceID, Status: "running", ServiceName: "opl-compute-alpha", Ready: true,
		ProviderRequestID: "req-runtime",
	}
	launchID := "workspace-launch-alpha"
	store := NewMemoryOperationStore()
	now := time.Now().UTC()
	for _, operation := range []FabricOperation{
		{ID: "fop-compute", OperationID: "op-internal-compute", Action: "create_compute_allocation", ResourceKind: "compute_allocation", ResourceID: compute.ID, AccountID: compute.AccountID, WorkspaceID: compute.WorkspaceID, IdempotencyKey: launchID + ":compute", Status: "succeeded", RedactedProviderPayload: map[string]any{"resource": compute}, CreatedAt: now},
		{ID: "fop-storage", OperationID: "op-internal-storage", Action: "create_storage_volume", ResourceKind: "storage_volume", ResourceID: storage.ID, AccountID: storage.AccountID, WorkspaceID: storage.WorkspaceID, IdempotencyKey: launchID + ":storage", Status: "succeeded", RedactedProviderPayload: map[string]any{"resource": storage}, CreatedAt: now.Add(time.Second)},
		{ID: "fop-attachment", OperationID: "op-internal-attachment", Action: "create_storage_attachment", ResourceKind: "storage_attachment", ResourceID: attachment.ID, AccountID: compute.AccountID, WorkspaceID: compute.WorkspaceID, IdempotencyKey: launchID + ":attachment", Status: "succeeded", RedactedProviderPayload: map[string]any{"resource": attachment}, CreatedAt: now.Add(2 * time.Second)},
		{ID: "fop-runtime", OperationID: "op-internal-runtime", Action: "create_workspace_runtime", ResourceKind: "workspace_runtime", ResourceID: compute.WorkspaceID, AccountID: compute.AccountID, WorkspaceID: compute.WorkspaceID, IdempotencyKey: launchID + ":workspace:runtime", Status: "succeeded", RedactedProviderPayload: map[string]any{"resource": runtime}, CreatedAt: now.Add(3 * time.Second)},
	} {
		if err := store.Append(context.Background(), operation); err != nil {
			t.Fatal(err)
		}
	}
	input := WorkspaceActivationTruthInput{
		LaunchOperationID: launchID, AccountID: compute.AccountID, WorkspaceID: compute.WorkspaceID,
		ComputeAllocationID: compute.ID, ComputeOperationID: launchID + ":compute",
		StorageVolumeID: storage.ID, StorageOperationID: launchID + ":storage",
		AttachmentID: attachment.ID, AttachmentOperationID: launchID + ":attachment",
		RuntimeID: runtime.ID, RuntimeOperationID: launchID + ":workspace:runtime", ServiceName: runtime.ServiceName,
		WorkspaceImageDigest: "registry.example/one-person-lab-app@sha256:" + repeatHex("f", 64),
		GatewaySecretRef:     "opl-gateway-alpha", WorkspaceAPIKeyID: 42, GatewaySecretFingerprint: "sha256:" + repeatHex("e", 64),
	}
	return NewServiceWithOperationStore(provider, store), store, input
}

type workspaceActivationTruthFixture struct {
	input                WorkspaceActivationTruthInput
	compute              ComputeAllocation
	storage              StorageVolume
	attachment           StorageAttachment
	items                []any
	deployment           map[string]any
	endpoint             map[string]any
	gatewaySecret        map[string]any
	volumeAttachment     map[string]any
	node                 map[string]any
	calls                [][]string
	pvName               string
	pvcName              string
	podName              string
	podIP                string
	volumeAttachmentName string
}

func newWorkspaceActivationTruthFixture(image string) *workspaceActivationTruthFixture {
	compute, storage := monthlyTruthResources()
	compute.OperationID = "workspace-launch-alpha:compute"
	compute.NodeName = "10.0.0.8"
	compute.PrivateIP = "10.0.0.8"
	compute.NodeSelector = map[string]any{"kubernetes.io/hostname": compute.NodeName}
	storage.OperationID = "workspace-launch-alpha:storage"
	storage.ProviderData["pvName"], storage.ProviderData["pvcName"] = "opl-storage-alpha-pv", "opl-storage-alpha-data"
	attachment := StorageAttachment{
		ID: "att-alpha", OperationID: "workspace-launch-alpha:attachment", WorkspaceID: compute.WorkspaceID,
		ComputeID: compute.ID, VolumeID: storage.ID, Status: "attached", Provider: "tencent-tke",
		ProviderAttachmentID: "pv/opl-storage-alpha-pv:pvc/opl-storage-alpha-data", ProviderRequestID: "req-attachment",
	}
	input := WorkspaceActivationTruthInput{
		LaunchOperationID: "workspace-launch-alpha", AccountID: compute.AccountID, WorkspaceID: compute.WorkspaceID,
		ComputeAllocationID: compute.ID, ComputeOperationID: "workspace-launch-alpha:compute",
		StorageVolumeID: storage.ID, StorageOperationID: "workspace-launch-alpha:storage",
		AttachmentID: attachment.ID, AttachmentOperationID: attachment.OperationID,
		RuntimeID: "rt-alpha", RuntimeOperationID: "workspace-launch-alpha:workspace:runtime", ServiceName: "opl-compute-alpha", WorkspaceImageDigest: image,
		GatewaySecretRef: "opl-gateway-alpha", WorkspaceAPIKeyID: 42, GatewaySecretFingerprint: "sha256:" + repeatHex("e", 64),
	}
	runtimeLabels := map[string]any{
		"app.kubernetes.io/name": "opl-compute-allocation", "app.kubernetes.io/instance": input.ServiceName,
		"oplcloud.cn/account-id": input.AccountID, "oplcloud.cn/workspace-id": input.WorkspaceID,
		"oplcloud.cn/compute-allocation-id": input.ComputeAllocationID, "oplcloud.cn/storage-id": input.StorageVolumeID,
		"oplcloud.cn/attachment-id": input.AttachmentID, "oplcloud.cn/attachment-operation-id": input.AttachmentOperationID,
		"oplcloud.cn/runtime-id": input.RuntimeID, "oplcloud.cn/runtime-operation-id": input.RuntimeOperationID,
	}
	selector := map[string]any{
		"app.kubernetes.io/name": "opl-compute-allocation", "app.kubernetes.io/instance": input.ServiceName,
		"oplcloud.cn/compute-allocation-id": input.ComputeAllocationID,
	}
	volumes := []any{
		map[string]any{"name": "workspace-data", "persistentVolumeClaim": map[string]any{"claimName": "opl-storage-alpha-data"}},
		map[string]any{"name": "workspace-secrets", "projected": map[string]any{"sources": []any{
			map[string]any{"secret": map[string]any{"name": input.ServiceName + "-env"}},
			map[string]any{"secret": map[string]any{"name": input.GatewaySecretRef}},
		}}},
	}
	container := map[string]any{
		"name": "workspace", "image": image,
		"securityContext": map[string]any{"allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []any{"ALL"}}},
		"volumeMounts":    workspaceDataMounts(),
	}
	podSpec := map[string]any{
		"nodeName": compute.NodeName, "automountServiceAccountToken": false, "dnsPolicy": "ClusterFirst",
		"securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 10001, "runAsGroup": 10001, "fsGroup": 10001, "seccompProfile": map[string]any{"type": "RuntimeDefault"}},
		"containers":      []any{container}, "volumes": volumes,
	}
	deployment := map[string]any{
		"kind": "Deployment", "metadata": map[string]any{"name": input.ServiceName, "generation": 2, "labels": cloneJSONMap(runtimeLabels)},
		"spec": map[string]any{"replicas": 1, "selector": map[string]any{"matchLabels": selector}, "template": map[string]any{
			"metadata": map[string]any{"labels": cloneJSONMap(runtimeLabels), "annotations": map[string]any{"opl.medopl.cn/credential-revision": "revision-alpha"}},
			"spec":     podSpec,
		}},
		"status": map[string]any{"observedGeneration": 2, "updatedReplicas": 1, "readyReplicas": 1, "availableReplicas": 1},
	}
	service := map[string]any{"kind": "Service", "metadata": map[string]any{"name": input.ServiceName, "labels": cloneJSONMap(runtimeLabels)}, "spec": map[string]any{"selector": selector}}
	policy := map[string]any{
		"kind": "NetworkPolicy", "metadata": map[string]any{"name": input.ServiceName, "labels": cloneJSONMap(runtimeLabels)},
		"spec": map[string]any{
			"podSelector": map[string]any{"matchLabels": selector}, "policyTypes": []any{"Ingress", "Egress"},
			"ingress": []any{map[string]any{
				"from":  []any{map[string]any{"podSelector": map[string]any{"matchLabels": map[string]any{"app.kubernetes.io/name": "opl-cloud", "app.kubernetes.io/component": "control-plane"}}}},
				"ports": []any{map[string]any{"protocol": "TCP", "port": 3000}},
			}},
			"egress": workspaceEgressFixture(),
		},
	}
	runtimeSecret := map[string]any{
		"kind": "Secret", "metadata": map[string]any{"name": input.ServiceName + "-env", "labels": cloneJSONMap(runtimeLabels)}, "type": "Opaque",
		"data": map[string]any{"webui_password": b64("runtime-password"), "webui_session_secret": b64("runtime-session")},
	}
	pv := map[string]any{
		"kind": "PersistentVolume", "metadata": map[string]any{"name": "opl-storage-alpha-pv", "labels": map[string]any{"oplcloud.cn/account-id": input.AccountID, "oplcloud.cn/workspace-id": input.WorkspaceID, "oplcloud.cn/storage-id": input.StorageVolumeID}},
		"spec": map[string]any{"csi": map[string]any{"driver": "com.tencent.cloud.csi.cbs", "volumeHandle": storage.ProviderResourceID}, "persistentVolumeReclaimPolicy": "Retain", "storageClassName": "", "accessModes": []any{"ReadWriteOnce"}, "capacity": map[string]any{"storage": "10Gi"}},
	}
	pvc := map[string]any{
		"kind": "PersistentVolumeClaim", "metadata": map[string]any{"name": "opl-storage-alpha-data", "labels": map[string]any{"oplcloud.cn/account-id": input.AccountID, "oplcloud.cn/workspace-id": input.WorkspaceID, "oplcloud.cn/storage-id": input.StorageVolumeID}},
		"spec":   map[string]any{"volumeName": "opl-storage-alpha-pv", "storageClassName": "", "accessModes": []any{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]any{"storage": "10Gi"}}},
		"status": map[string]any{"phase": "Bound"},
	}
	podName, podIP := "opl-compute-alpha-7d6c", "10.244.0.8"
	pod := map[string]any{
		"kind": "Pod", "metadata": map[string]any{"name": podName, "uid": "pod-uid-alpha", "labels": cloneJSONMap(runtimeLabels)}, "spec": podSpec,
		"status": map[string]any{
			"phase": "Running", "podIP": podIP,
			"conditions":        []any{map[string]any{"type": "PodScheduled", "status": "True"}, map[string]any{"type": "Ready", "status": "True"}},
			"containerStatuses": []any{map[string]any{"name": "workspace", "ready": true, "imageID": "containerd://" + image}},
		},
	}
	endpoint := map[string]any{
		"kind": "Endpoints", "metadata": map[string]any{"name": input.ServiceName},
		"subsets": []any{map[string]any{"addresses": []any{map[string]any{"ip": podIP, "targetRef": map[string]any{"kind": "Pod", "name": podName, "uid": "pod-uid-alpha"}}}}},
	}
	gatewaySecret := map[string]any{
		"kind": "Secret", "type": "Opaque", "metadata": map[string]any{
			"name": input.GatewaySecretRef, "labels": map[string]any{"app.kubernetes.io/name": "opl-gateway-secret"},
			"annotations": map[string]any{"oplcloud.cn/account-id": input.AccountID, "oplcloud.cn/workspace-id": input.WorkspaceID, "oplcloud.cn/workspace-api-key-id": "42", "oplcloud.cn/secret-fingerprint": input.GatewaySecretFingerprint},
		},
		"data": map[string]any{"opl_gateway_api_key": b64("gateway-key")},
	}
	volumeAttachmentName := "csi-att-alpha"
	volumeAttachment := map[string]any{
		"kind": "VolumeAttachment", "metadata": map[string]any{"name": volumeAttachmentName},
		"spec":   map[string]any{"attacher": "com.tencent.cloud.csi.cbs", "nodeName": compute.NodeName, "source": map[string]any{"persistentVolumeName": "opl-storage-alpha-pv"}},
		"status": map[string]any{"attached": true},
	}
	node := map[string]any{
		"kind": "Node", "metadata": map[string]any{"name": compute.NodeName, "labels": map[string]any{
			"medopl.cn/workload": "workspace", "oplcloud.cn/resource-id": compute.ID, "oplcloud.cn/account-id": input.AccountID, "oplcloud.cn/workspace-id": input.WorkspaceID,
		}},
		"spec":   map[string]any{"taints": []any{map[string]any{"key": "oplcloud.cn/workspace-id", "value": input.WorkspaceID, "effect": "NoSchedule"}}},
		"status": map[string]any{"addresses": []any{map[string]any{"type": "InternalIP", "address": compute.PrivateIP}}},
	}
	return &workspaceActivationTruthFixture{
		input: input, compute: compute, storage: storage, attachment: attachment,
		items: []any{deployment, service, policy, runtimeSecret, pv, pvc, pod}, deployment: deployment,
		endpoint: endpoint, gatewaySecret: gatewaySecret, volumeAttachment: volumeAttachment, node: node,
		pvName: "opl-storage-alpha-pv", pvcName: "opl-storage-alpha-data", podName: podName, podIP: podIP, volumeAttachmentName: volumeAttachmentName,
	}
}

func (f *workspaceActivationTruthFixture) provision(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
	if request.Action != "provider_truth" {
		return provisionerResponse{}, errors.New("unexpected provisioner action")
	}
	providerData := map[string]string{
		"instanceType": f.compute.InstanceType, "zone": f.compute.Zone, "chargeType": "PREPAID", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": f.compute.Deadline,
		"machineName": f.compute.MachineName, "storageChargeType": "PREPAID", "storageRenewFlag": "NOTIFY_AND_MANUAL_RENEW", "storageDeadline": f.storage.Deadline,
		"storageDiskType": f.storage.DiskType, "storageSizeGb": "10", "storageZone": f.storage.Zone,
	}
	for key, value := range f.compute.CostTags {
		providerData["computeTag:"+key] = value
	}
	for key, value := range f.storage.CostTags {
		providerData[key] = value
	}
	return provisionerResponse{
		OK: true, ProviderRequestID: "req-activation-truth", MachinePresent: boolPointer(true), StoragePresent: boolPointer(true),
		InstanceID: f.compute.InstanceID, PrivateIP: f.compute.PrivateIP, InstanceType: f.compute.InstanceType,
		CVMStatus: "RUNNING", TKEStatus: "RUNNING", CBSStatus: "ATTACHED", ProviderData: providerData,
	}, nil
}

func (f *workspaceActivationTruthFixture) kubectl(_ context.Context, args []string, _ []byte) ([]byte, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	switch {
	case slices.Equal(args, []string{"get", "deployment,service,networkpolicy,secret,pod,pv,pvc", "-l", "oplcloud.cn/workspace-id=" + f.input.WorkspaceID, "-o", "json"}):
		return mustJSON(map[string]any{"kind": "List", "items": f.items}), nil
	case slices.Equal(args, []string{"get", "endpoints/" + f.input.ServiceName, "secret/" + f.input.GatewaySecretRef, "node/" + f.compute.NodeName, "-o", "json"}):
		return mustJSON(map[string]any{"kind": "List", "items": []any{f.endpoint, f.gatewaySecret, f.node}}), nil
	case slices.Equal(args, []string{"get", "volumeattachment", "-o", "json"}):
		return mustJSON(map[string]any{"kind": "List", "items": []any{f.volumeAttachment}}), nil
	default:
		return nil, errors.New("unexpected kubectl read")
	}
}

func cloneJSONMap(input map[string]any) map[string]any {
	encoded := mustJSON(input)
	var output map[string]any
	if err := json.Unmarshal(encoded, &output); err != nil {
		panic(err)
	}
	return output
}

func repeatHex(value string, count int) string {
	result := ""
	for range count {
		result += value
	}
	return result
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
