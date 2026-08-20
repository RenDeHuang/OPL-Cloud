package fabric

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type providerFactsSpy struct {
	testProvider
	computeFacts ProviderResourceFacts
	computeErr   error
	computeReads atomic.Int32
}

func (p *providerFactsSpy) ReadComputeProviderFacts(context.Context, ComputeAllocation) (ProviderResourceFacts, error) {
	p.computeReads.Add(1)
	return p.computeFacts, p.computeErr
}

type providerWithoutFacts struct{ Provider }

func TestProviderFactsBatchDelegatesMappingAndPreservesWireShape(t *testing.T) {
	now := time.Date(2026, 8, 12, 4, 5, 6, 7, time.UTC)
	provider := &providerFactsSpy{computeFacts: ProviderResourceFacts{
		PackageOrSpec: "adapter-spec", ProviderID: "adapter-provider-id", Zone: "adapter-zone", Status: "adapter-status", ExpiresAt: "2026-09-12T00:00:00Z",
	}}
	service := NewService(provider)
	service.now = func() time.Time { return now }
	service.computes["compute-alpha"] = ComputeAllocation{
		ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "workspace-alpha",
		InstanceType: "service-must-not-read", CVMInstanceID: "service-must-not-read", CVMStatus: "service-must-not-read",
		ProviderData: map[string]string{"instanceType": "service-must-not-read", "zone": "service-must-not-read"},
		CostTags:     map[string]string{"opl_account_id": "service-must-not-read"},
	}

	batch, err := service.ProviderFactsBatch(context.Background(), ProviderFactsBatchInput{Items: []ProviderFactInput{{
		AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", ResourceType: "compute", ResourceID: "compute-alpha",
	}}})
	if err != nil || len(batch.Items) != 1 || !batch.Items[0].Available || batch.Items[0].ErrorCode != "" || provider.computeReads.Load() != 1 {
		t.Fatalf("batch=%#v err=%v reads=%d", batch, err, provider.computeReads.Load())
	}
	payload, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"items":[{"accountId":"acct-alpha","workspaceId":"workspace-alpha","resourceType":"compute","resourceId":"compute-alpha","available":true,"facts":{"packageOrSpec":"adapter-spec","providerId":"adapter-provider-id","zone":"adapter-zone","status":"adapter-status","expiresAt":"2026-09-12T00:00:00Z","lastReadAt":"2026-08-12T04:05:06.000000007Z"}}]}`
	if string(payload) != want {
		t.Fatalf("provider facts wire=%s want=%s", payload, want)
	}
	operations, listErr := service.ListOperations(context.Background())
	if listErr != nil || len(operations) != 0 {
		t.Fatalf("provider facts mutated operation store: operations=%#v err=%v", operations, listErr)
	}
}

func TestProviderFactsBatchFailsClosedBeforeAdapterRead(t *testing.T) {
	provider := &providerFactsSpy{}
	service := NewService(provider)
	service.computes["compute-alpha"] = ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "workspace-alpha"}

	identityMismatch, err := service.ProviderFactsBatch(context.Background(), ProviderFactsBatchInput{Items: []ProviderFactInput{{
		AccountID: "acct-other", WorkspaceID: "workspace-alpha", ResourceType: "compute", ResourceID: "compute-alpha",
	}}})
	if err != nil || identityMismatch.Items[0].Available || identityMismatch.Items[0].ErrorCode != "provider_fact_identity_mismatch" || provider.computeReads.Load() != 0 {
		t.Fatalf("identity mismatch=%#v err=%v reads=%d", identityMismatch, err, provider.computeReads.Load())
	}

	unavailable := NewService(providerWithoutFacts{Provider: testProvider{}})
	unavailable.computes["compute-alpha"] = service.computes["compute-alpha"]
	unavailableBatch, err := unavailable.ProviderFactsBatch(context.Background(), ProviderFactsBatchInput{Items: []ProviderFactInput{{
		AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", ResourceType: "compute", ResourceID: "compute-alpha",
	}}})
	if err != nil || unavailableBatch.Items[0].Available || unavailableBatch.Items[0].ErrorCode != "provider_facts_unavailable" {
		t.Fatalf("unavailable=%#v err=%v", unavailableBatch, err)
	}

	provider.computeErr = errors.New("provider_readback_unavailable")
	readFailure, err := service.ProviderFactsBatch(context.Background(), ProviderFactsBatchInput{Items: []ProviderFactInput{{
		AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", ResourceType: "compute", ResourceID: "compute-alpha",
	}}})
	if err != nil || readFailure.Items[0].Available || readFailure.Items[0].ErrorCode != "provider_readback_unavailable" || provider.computeReads.Load() != 1 {
		t.Fatalf("read failure=%#v err=%v reads=%d", readFailure, err, provider.computeReads.Load())
	}
	for _, current := range []*Service{service, unavailable} {
		operations, listErr := current.ListOperations(context.Background())
		if listErr != nil || len(operations) != 0 {
			t.Fatalf("failed provider facts read mutated operation store: operations=%#v err=%v", operations, listErr)
		}
	}
}

type providerFactsDockerRunner struct {
	networkID     string
	networkName   string
	volumeName    string
	networkLabels map[string]string
	volumeLabels  map[string]string
	calls         [][]string
}

func (r *providerFactsDockerRunner) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	switch {
	case len(args) == 7 && args[0] == "network" && args[1] == "ls":
		r.networkName = strings.TrimSuffix(strings.TrimPrefix(args[4], "name=^"), "$")
		return json.Marshal(dockerObjectInventoryRow{ID: r.networkID, Name: r.networkName})
	case len(args) == 3 && args[0] == "network" && args[1] == "inspect":
		return json.Marshal([]dockerNetworkInspect{{ID: r.networkID, Name: r.networkName, Labels: r.networkLabels}})
	case len(args) == 6 && args[0] == "volume" && args[1] == "ls":
		return json.Marshal(dockerObjectInventoryRow{Name: r.volumeName})
	case len(args) == 3 && args[0] == "volume" && args[1] == "inspect":
		return json.Marshal([]dockerVolumeInspect{{Name: r.volumeName, Labels: r.volumeLabels}})
	default:
		return nil, fmt.Errorf("unexpected docker action: %v", args)
	}
}

func TestDockerObjectInventoryRequiresExactUniqueStructuredIdentity(t *testing.T) {
	for _, tc := range []struct {
		name    string
		output  string
		exists  bool
		wantErr bool
	}{
		{name: "empty is authoritative absent", output: "", exists: false},
		{name: "exact network", output: `{"ID":"full-id","Name":"opl-runtime-alpha"}`, exists: true},
		{name: "exact container", output: `{"ID":"full-id","Names":"opl-runtime-alpha"}`, exists: true},
		{name: "foreign row conflicts", output: `{"ID":"full-id","Name":"opl-runtime-alphabet"}`, wantErr: true},
		{name: "duplicate exact rows conflict", output: "{\"ID\":\"one\",\"Name\":\"opl-runtime-alpha\"}\n{\"ID\":\"two\",\"Name\":\"opl-runtime-alpha\"}", wantErr: true},
		{name: "name fields disagree", output: `{"ID":"full-id","Name":"opl-runtime-alpha","Names":"opl-runtime-beta"}`, wantErr: true},
		{name: "malformed row", output: `{`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, exists, err := decodeDockerObjectInventory([]byte(tc.output), "opl-runtime-alpha")
			if exists != tc.exists || (err != nil) != tc.wantErr {
				t.Fatalf("exists=%v err=%v", exists, err)
			}
		})
	}
}

func TestLocalDockerProviderFactsParityAndReadOnly(t *testing.T) {
	compute := ComputeAllocation{
		ID: "compute-local", OperationID: "op-compute-local", AccountID: "acct-local", WorkspaceID: "workspace-local",
		InstanceType: "local-2c4g", Deadline: "2026-09-12T00:00:00Z",
	}
	volume := StorageVolume{
		ID: "storage-local", OperationID: "op-storage-local", AccountID: "acct-local", WorkspaceID: "workspace-local",
		DiskType: "local-directory", StorageClass: "host-directory", Deadline: "2026-09-12T00:00:00Z", Zone: "local", SizeGB: 10,
	}
	attachment := StorageAttachment{ID: "attachment-local", OperationID: "op-attachment-local", WorkspaceID: "workspace-local", ComputeID: compute.ID, VolumeID: volume.ID}
	runner := &providerFactsDockerRunner{
		networkID: "network-live", volumeName: localDockerName("opl-storage", volume.ID),
		networkLabels: localDockerLabels(compute.AccountID, compute.WorkspaceID, compute.ID, "", "compute"),
		volumeLabels:  localDockerLabels(volume.AccountID, volume.WorkspaceID, volume.ID, "", "storage"),
	}
	storageRoot := localDockerStorageTestRoot(t)
	provider := newLocalDockerProvider(localDockerStorageTestConfig(storageRoot), runner)
	paths, err := provider.ensureStorageDirectories(context.Background(), localDockerStorageMetadata{
		SchemaVersion: localDockerStorageMetadataSchemaVersion, StorageID: volume.ID, AccountID: volume.AccountID, WorkspaceID: volume.WorkspaceID, SizeGB: volume.SizeGB,
	}, 10)
	if err != nil {
		t.Fatal(err)
	}

	computeFacts, computeErr := provider.ReadComputeProviderFacts(context.Background(), compute)
	storageFacts, storageErr := provider.ReadStorageProviderFacts(context.Background(), volume)
	attachmentFacts, attachmentErr := provider.ReadStorageAttachmentProviderFacts(context.Background(), attachment, compute, volume)
	runtimeFacts := provider.WorkspaceRuntimeProviderFacts(WorkspaceRuntime{ServiceName: "opl-local-runtime", Status: "running"})
	if computeErr != nil || storageErr != nil || attachmentErr != nil {
		t.Fatalf("local facts errors: compute=%v storage=%v attachment=%v", computeErr, storageErr, attachmentErr)
	}
	wantCompute := ProviderResourceFacts{PackageOrSpec: "local-2c4g", ProviderID: "network/network-live", Zone: "local", Status: "running", ExpiresAt: compute.Deadline}
	wantStorage := ProviderResourceFacts{PackageOrSpec: "local-directory", ProviderID: "directory/" + paths.WorkspaceName, Zone: "local", Status: "ready", ExpiresAt: volume.Deadline}
	wantAttachment := ProviderResourceFacts{PackageOrSpec: "/data", ProviderID: "docker/" + localDockerName("opl-compute", compute.ID) + "/" + paths.WorkspaceName, Status: "attached"}
	wantRuntime := ProviderResourceFacts{ProviderID: "opl-local-runtime", Status: "running"}
	if !reflect.DeepEqual(computeFacts, wantCompute) || !reflect.DeepEqual(storageFacts, wantStorage) || !reflect.DeepEqual(attachmentFacts, wantAttachment) || !reflect.DeepEqual(runtimeFacts, wantRuntime) {
		t.Fatalf("local facts: compute=%#v storage=%#v attachment=%#v runtime=%#v", computeFacts, storageFacts, attachmentFacts, runtimeFacts)
	}
	for _, call := range runner.calls {
		if len(call) < 2 || (call[1] != "ls" && call[1] != "inspect") || call[0] != "network" {
			t.Fatalf("local provider facts issued mutation: %#v", runner.calls)
		}
	}
}

func TestTencentProviderFactsOwnTencentMappingAndStayReadOnly(t *testing.T) {
	provider := NewTencentProvider()
	computeTags := oplCostTags("acct-tencent", "workspace-tencent", "compute-tencent", "op-compute-tencent")
	storageTags := oplCostTags("acct-tencent", "workspace-tencent", "storage-tencent", "op-storage-tencent")
	var provisionActions []string
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		provisionActions = append(provisionActions, request.Action)
		switch request.Action {
		case "sync_compute_allocation":
			if request.Pool.NodePoolID != "np-basic" || !reflect.DeepEqual(request.Tags, computeTags) {
				t.Fatalf("compute provider input=%#v", request)
			}
			return provisionerResponse{
				OK: true, Status: "running", CVMStatus: "RUNNING", NodePoolID: "np-basic", InstanceID: "ins-tencent", InstanceType: "SA5.MEDIUM4", ProviderRequestID: "req-compute-read",
				ProviderData: map[string]string{"instanceType": "SA5.MEDIUM4", "cpu": "2", "memoryGb": "4", "zone": "ap-guangzhou-3", "deadline": "2026-09-12T00:00:00Z"},
			}, nil
		case "sync_storage_volume":
			if request.Storage.ID != "disk-tencent" || request.Region != "ap-guangzhou" || !reflect.DeepEqual(request.Tags, storageTags) {
				t.Fatalf("storage provider input=%#v", request)
			}
			return provisionerResponse{
				OK: true, Status: "provider_ready", CBSStatus: "ATTACHED", StorageVolumeID: "disk-tencent", ProviderRequestID: "req-storage-read",
				ProviderData: map[string]string{"diskType": "CLOUD_BSSD", "zone": "ap-guangzhou-3", "deadline": "2026-09-12T00:00:00Z", "region": "ap-guangzhou"},
			}, nil
		default:
			return provisionerResponse{}, fmt.Errorf("unexpected provisioner action %q", request.Action)
		}
	}
	compute := ComputeAllocation{
		ID: "compute-tencent", AccountID: "acct-tencent", WorkspaceID: "workspace-tencent", PackageID: "basic", Status: "running",
		Provider: "tencent-tke", ProviderResourceID: "machine/tke-node", PoolID: "pool-basic-2c4g", NodePoolID: "np-basic",
		InstanceID: "ins-tencent", CVMInstanceID: "ins-tencent", InstanceType: "SA5.MEDIUM4", Deadline: "2026-09-12T00:00:00Z",
		ProviderData: map[string]string{"instanceType": "SA5.MEDIUM4", "zone": "ap-guangzhou-3"}, CostTags: computeTags,
	}
	volume := StorageVolume{
		ID: "storage-tencent", OperationID: "op-storage-tencent", AccountID: "acct-tencent", WorkspaceID: "workspace-tencent", Status: "ready",
		Provider: "tencent-tke", ProviderResourceID: "disk-tencent", SizeGB: 10, DiskType: "CLOUD_BSSD", Zone: "ap-guangzhou-3", Deadline: "2026-09-12T00:00:00Z",
		ProviderData: map[string]string{"pvName": "opl-storage-tencent-pv", "pvcName": "opl-storage-tencent-data", "region": "ap-guangzhou"}, CostTags: storageTags,
	}
	computeFacts, computeErr := provider.ReadComputeProviderFacts(context.Background(), compute)
	storageFacts, storageErr := provider.ReadStorageProviderFacts(context.Background(), volume)
	if computeErr != nil || storageErr != nil {
		t.Fatalf("Tencent facts errors: compute=%v storage=%v", computeErr, storageErr)
	}
	wantCompute := ProviderResourceFacts{PackageOrSpec: "SA5.MEDIUM4", ProviderID: "machine/tke-node", Zone: "ap-guangzhou-3", Status: "RUNNING", ExpiresAt: compute.Deadline}
	wantStorage := ProviderResourceFacts{PackageOrSpec: "CLOUD_BSSD", ProviderID: "disk-tencent", Zone: "ap-guangzhou-3", Status: "ATTACHED", ExpiresAt: volume.Deadline}
	if !reflect.DeepEqual(computeFacts, wantCompute) || !reflect.DeepEqual(storageFacts, wantStorage) || !reflect.DeepEqual(provisionActions, []string{"sync_compute_allocation", "sync_storage_volume"}) {
		t.Fatalf("Tencent facts: compute=%#v storage=%#v actions=%#v", computeFacts, storageFacts, provisionActions)
	}

	manifest := map[string]any{}
	if err := json.Unmarshal(staticCBSManifest(volume), &manifest); err != nil {
		t.Fatal(err)
	}
	items := manifest["items"].([]any)
	items[1].(map[string]any)["status"] = map[string]any{"phase": "Bound"}
	var kubectlCalls [][]string
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		kubectlCalls = append(kubectlCalls, append([]string(nil), args...))
		return mustJSON(manifest), nil
	}
	attachment := StorageAttachment{
		ID: "att_" + stableSuffix("op-attachment-tencent")[:18], OperationID: "op-attachment-tencent", WorkspaceID: "workspace-tencent",
		ComputeID: compute.ID, VolumeID: volume.ID, Status: "attached", ProviderAttachmentID: "stale-provider-id",
	}
	attachmentFacts, attachmentErr := provider.ReadStorageAttachmentProviderFacts(context.Background(), attachment, compute, volume)
	runtimeFacts := provider.WorkspaceRuntimeProviderFacts(WorkspaceRuntime{ServiceName: "opl-tencent-runtime", Status: "running"})
	wantAttachment := ProviderResourceFacts{PackageOrSpec: "/data", ProviderID: "pv/opl-storage-tencent-pv:pvc/opl-storage-tencent-data", Status: "attached"}
	wantRuntime := ProviderResourceFacts{ProviderID: "opl-tencent-runtime", Status: "running"}
	if attachmentErr != nil || !reflect.DeepEqual(attachmentFacts, wantAttachment) || !reflect.DeepEqual(runtimeFacts, wantRuntime) {
		t.Fatalf("Tencent attachment/runtime facts: attachment=%#v err=%v runtime=%#v", attachmentFacts, attachmentErr, runtimeFacts)
	}
	for _, call := range kubectlCalls {
		if len(call) == 0 || call[0] != "get" {
			t.Fatalf("Tencent provider facts issued Kubernetes mutation: %#v", kubectlCalls)
		}
	}
	for _, action := range provisionActions {
		if !strings.HasPrefix(action, "sync_") {
			t.Fatalf("Tencent provider facts issued provider mutation: %#v", provisionActions)
		}
	}
}
