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

func TestTencentProviderReadinessRequiresExpectedImagesOnEveryReadyPod(t *testing.T) {
	const (
		cloudImage     = "registry.example.com/opl/cloud@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		workspaceImage = "registry.example.com/opl/workspace@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	readyPod := func(component, container, imageID string) any {
		labels := map[string]any{"app.kubernetes.io/component": component}
		if component == "workspace" {
			labels = map[string]any{"oplcloud.cn/workspace-id": "workspace-alpha"}
		}
		return map[string]any{
			"metadata": map[string]any{"labels": labels},
			"status": map[string]any{
				"phase":      "Running",
				"conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
				"containerStatuses": []any{map[string]any{
					"name": container, "ready": true, "imageID": imageID,
				}},
			},
		}
	}
	matchingPods := func() []any {
		return []any{
			readyPod("control-plane", "control-plane", "docker-pullable://"+cloudImage),
			readyPod("ledger", "ledger", "docker-pullable://"+cloudImage),
			readyPod("fabric", "fabric", "docker-pullable://"+cloudImage),
			readyPod("workspace", "workspace", "docker-pullable://"+workspaceImage),
		}
	}
	digestPods := func(prefix string) []any {
		pods := matchingPods()
		for index, item := range pods {
			ref := cloudImage
			if index == len(pods)-1 {
				ref = workspaceImage
			}
			item.(map[string]any)["status"].(map[string]any)["containerStatuses"].([]any)[0].(map[string]any)["imageID"] = prefix + strings.SplitN(ref, "@", 2)[1]
		}
		return pods
	}

	for _, tc := range []struct {
		name           string
		cloudImage     string
		workspaceImage string
		pods           func() []any
		wantReady      bool
		wantCloud      bool
		wantWorkspace  bool
	}{
		{name: "matching immutable image ids", cloudImage: cloudImage, workspaceImage: workspaceImage, pods: matchingPods, wantReady: true, wantCloud: true, wantWorkspace: true},
		{name: "containerd digest image ids", cloudImage: cloudImage, workspaceImage: workspaceImage, pods: func() []any { return digestPods("containerd://") }, wantReady: true, wantCloud: true, wantWorkspace: true},
		{name: "bare digest image ids", cloudImage: cloudImage, workspaceImage: workspaceImage, pods: func() []any { return digestPods("") }, wantReady: true, wantCloud: true, wantWorkspace: true},
		{name: "missing image id", cloudImage: cloudImage, workspaceImage: workspaceImage, pods: func() []any {
			pods := matchingPods()
			pods[0].(map[string]any)["status"].(map[string]any)["containerStatuses"].([]any)[0].(map[string]any)["imageID"] = ""
			return pods
		}, wantWorkspace: true},
		{name: "tag only image id", cloudImage: cloudImage, workspaceImage: workspaceImage, pods: func() []any {
			pods := matchingPods()
			pods[0].(map[string]any)["status"].(map[string]any)["containerStatuses"].([]any)[0].(map[string]any)["imageID"] = "registry.example.com/opl/cloud:latest"
			return pods
		}, wantWorkspace: true},
		{name: "mixed image ids", cloudImage: cloudImage, workspaceImage: workspaceImage, pods: func() []any {
			return append(matchingPods(), readyPod("fabric", "fabric", "docker-pullable://registry.example.com/opl/cloud@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"))
		}, wantWorkspace: true},
		{name: "unknown runtime image id scheme", cloudImage: cloudImage, workspaceImage: workspaceImage, pods: func() []any {
			pods := matchingPods()
			pods[0].(map[string]any)["status"].(map[string]any)["containerStatuses"].([]any)[0].(map[string]any)["imageID"] = "cri-o://" + strings.SplitN(cloudImage, "@", 2)[1]
			return pods
		}, wantWorkspace: true},
		{name: "tag only expected image", cloudImage: "registry.example.com/opl/cloud:latest", workspaceImage: workspaceImage, pods: matchingPods, wantWorkspace: true},
		{name: "workspace pod missing", cloudImage: cloudImage, workspaceImage: workspaceImage, pods: func() []any { return matchingPods()[:3] }, wantCloud: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OPL_CLOUD_IMAGE", tc.cloudImage)
			t.Setenv("OPL_WORKSPACE_IMAGE", tc.workspaceImage)
			for key, value := range map[string]string{
				"OPL_WORKSPACE_DOMAIN": "workspace.medopl.cn", "OPL_K8S_NAMESPACE": "opl-cloud", "OPL_IMAGE_PULL_SECRET_NAME": "pull-secret",
				"OPL_WORKSPACE_STORAGE_CLASS": "cbs", "OPL_TENCENT_PROVISIONER_BIN": "/bin/true", "TENCENT_DEPLOY_KUBECONFIG_REF": "/tmp/kubeconfig",
				"RUN_TENCENT_CREATE_RELEASE_EXECUTION": "1",
			} {
				t.Setenv(key, value)
			}
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte("#!/bin/sh\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			provider := NewTencentProvider()
			provider.provision = func(context.Context, provisionerRequest) (provisionerResponse, error) {
				return provisionerResponse{OK: true}, nil
			}
			provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
				if !slices.Equal(args, []string{"get", "pod", "-o", "json"}) {
					t.Fatalf("kubectl args = %#v", args)
				}
				return json.Marshal(map[string]any{"items": tc.pods()})
			}

			result, err := provider.Readiness(context.Background())
			if err != nil || result["ready"] != tc.wantReady || result["immutableImagesReady"] != tc.wantReady || result["cloudImagesReady"] != tc.wantCloud || result["workspaceImagesReady"] != tc.wantWorkspace {
				t.Fatalf("readiness = %#v, err=%v, want ready=%t", result, err, tc.wantReady)
			}
		})
	}
}

func TestTencentProviderMonthlyPreflightRequiresLiveMutationFlag(t *testing.T) {
	for _, value := range []string{"", "0", "true"} {
		t.Run(fmt.Sprintf("value_%q", value), func(t *testing.T) {
			t.Setenv("RUN_TENCENT_CREATE_RELEASE_EXECUTION", value)
			calls := 0
			provider := NewTencentProvider()
			provider.provision = func(context.Context, provisionerRequest) (provisionerResponse, error) {
				calls++
				return provisionerResponse{}, errors.New("unexpected provisioner call")
			}

			_, err := provider.MonthlyPreflight(context.Background(), MonthlyPreflightInput{
				ResourceType: "compute", PackageID: "basic", Zone: "na-siliconvalley-1",
			})
			if err == nil || !strings.Contains(err.Error(), "live_mutation_flag_required") || calls != 0 {
				t.Fatalf("preflight error=%v provisioner calls=%d", err, calls)
			}
		})
	}
}

func TestTencentProviderMonthlyPreflightDiscoversExactlyOneLabeledPool(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input MonthlyPreflightInput
		check func(*testing.T, provisionerRequest)
		reply provisionerResponse
	}{
		{
			name: "compute", input: MonthlyPreflightInput{ResourceType: "compute", PackageID: "basic", Zone: "na-siliconvalley-1"},
			check: func(t *testing.T, request provisionerRequest) {
				if request.Action != "capacity_preflight" || request.PackageID != "basic" || request.Zone != "na-siliconvalley-1" || request.Pool.ID != "pool-basic-2c4g" || request.Pool.NodePoolID != "" || request.Pool.InstanceType != "SA5.MEDIUM4" || request.Pool.DesiredReplicas != 1 {
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
				if request.Action != "capacity_preflight" || request.PackageID != "pro" || request.Zone != "na-siliconvalley-1" || request.Pool.ID != "pool-pro-8c16g" || request.Pool.NodePoolID != "" || request.Pool.InstanceType != "SA5.2XLARGE16" || request.Pool.DesiredReplicas != 1 {
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
			t.Setenv("RUN_TENCENT_CREATE_RELEASE_EXECUTION", "1")
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
			resultJSON, marshalErr := json.Marshal(result)
			var resultFields map[string]any
			if marshalErr != nil || json.Unmarshal(resultJSON, &resultFields) != nil {
				t.Fatal(marshalErr)
			}
			if err != nil || result.ResourceType != tc.input.ResourceType || result.PackageID != tc.input.PackageID || result.SizeGB != tc.input.SizeGB || result.Zone != tc.input.Zone || !result.Available || result.ChargeType != "PREPAID" || result.PeriodMonths != 1 || result.RenewFlag != "NOTIFY_AND_MANUAL_RENEW" || result.ProviderPriceCNY != tc.reply.ProviderPriceCNY || len(result.ProviderRequestIDs) == 0 || (tc.input.ResourceType == "compute" && resultFields["nodePoolId"] != tc.reply.NodePoolID) {
				t.Fatalf("monthly preflight = %#v, err=%v", result, err)
			}
		})
	}
}

func boolPointer(value bool) *bool { return &value }

func TestTencentProviderMonthlyProviderTruthReusesDescribeOnlyProvisionerAction(t *testing.T) {
	t.Setenv("TENCENT_DEPLOY_CLUSTER_ID", "cls-123")
	compute, storage := monthlyTruthResources()
	compute.ProviderData, storage.ProviderData = nil, nil
	provider := NewTencentProvider()
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Action != "provider_truth" || request.AccountID != compute.AccountID || request.StorageVolumeID != storage.ProviderResourceID || request.PackageID != compute.PackageID ||
			request.Pool.ClusterID != "cls-123" || request.Pool.NodePoolID != compute.NodePoolID || request.Pool.InstanceType != compute.InstanceType ||
			request.Allocation.ID != compute.ID || request.Allocation.InstanceID != compute.InstanceID || request.Allocation.MachineName != compute.MachineName || request.Allocation.PrivateIP != compute.PrivateIP ||
			request.Storage.ID != storage.ProviderResourceID || request.Storage.SizeGB != uint64(storage.SizeGB) || request.Storage.Zone != storage.Zone || request.Storage.DiskType != storage.DiskType ||
			!reflect.DeepEqual(request.ComputeTags, compute.CostTags) || !reflect.DeepEqual(request.Tags, storage.CostTags) {
			t.Fatalf("provider truth request = %#v", request)
		}
		providerData := map[string]string{
			"instanceType": compute.InstanceType, "zone": compute.Zone, "chargeType": "PREPAID", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": compute.Deadline,
			"machineName": compute.MachineName, "privateIp": compute.PrivateIP, "storagePresent": "false", "cbsStatus": "NOT_FOUND",
		}
		for key, value := range compute.CostTags {
			providerData["computeTag:"+key] = value
		}
		return provisionerResponse{
			OK: false, ErrorCode: "provider_truth_partial_identity", ProviderRequestID: "req-truth", MachinePresent: boolPointer(true), StoragePresent: boolPointer(false),
			InstanceID: compute.InstanceID, PrivateIP: compute.PrivateIP, CVMStatus: "RUNNING", TKEStatus: "RUNNING", CBSStatus: "NOT_FOUND", Status: "", InstanceType: compute.InstanceType,
			ProviderData: providerData,
		}, nil
	}
	provider.kubectl = func(context.Context, []string, []byte) ([]byte, error) {
		t.Fatal("monthly provider truth must not call kubectl")
		return nil, nil
	}

	truth, err := provider.MonthlyProviderTruth(context.Background(), compute, storage)

	if err != nil || truth.ComputeState != "ready" || truth.StorageState != "absent" || truth.ProviderRequestID != "req-truth" || truth.ErrorCode != "provider_truth_partial_identity" {
		t.Fatalf("provider truth=%#v err=%v", truth, err)
	}
	if truth.Compute.Status != "ready" || truth.Compute.InstanceType != compute.InstanceType || truth.Compute.Zone != compute.Zone || truth.Compute.ChargeType != "PREPAID" ||
		truth.Compute.ProviderRequestID != "req-truth" || truth.Storage.Status != "external_deleted" || truth.Storage.CBSStatus != "NOT_FOUND" || truth.Storage.ProviderRequestID != "req-truth" {
		t.Fatalf("provider truth lost authoritative facts: %#v", truth)
	}
}

func TestTencentProviderMonthlyProviderTruthMapsKnownAndUnknownComponentsIndependently(t *testing.T) {
	t.Setenv("TENCENT_DEPLOY_CLUSTER_ID", "cls-123")
	compute, storage := monthlyTruthResources()
	for _, tc := range []struct {
		name                       string
		response                   provisionerResponse
		wantCompute, wantStorage   string
		wantComputeStatus, wantCBS string
	}{
		{
			name: "both absent", wantCompute: "absent", wantStorage: "absent", wantComputeStatus: "external_deleted", wantCBS: "NOT_FOUND",
			response: provisionerResponse{OK: true, Status: "absent", MachinePresent: boolPointer(false), StoragePresent: boolPointer(false), CVMStatus: "NOT_FOUND", TKEStatus: "NOT_FOUND", CBSStatus: "NOT_FOUND", ProviderRequestID: "req-absent"},
		},
		{
			name: "compute absent storage ready", wantCompute: "absent", wantStorage: "ready", wantComputeStatus: "external_deleted", wantCBS: "ATTACHED",
			response: provisionerResponse{
				OK: false, ErrorCode: "provider_truth_partial_identity", MachinePresent: boolPointer(false), StoragePresent: boolPointer(true), CVMStatus: "NOT_FOUND", CBSStatus: "ATTACHED", ProviderRequestID: "req-storage",
				TKEStatus: "NOT_FOUND", ProviderData: map[string]string{
					"storageChargeType": "PREPAID", "storageRenewFlag": "NOTIFY_AND_MANUAL_RENEW", "storageDeadline": storage.Deadline, "storageDiskType": storage.DiskType, "storageSizeGb": "10", "storageZone": storage.Zone,
					"opl_account_id": storage.CostTags["opl_account_id"], "opl_workspace_id": storage.CostTags["opl_workspace_id"], "opl_resource_id": storage.CostTags["opl_resource_id"], "opl_operation_id": storage.CostTags["opl_operation_id"],
				},
			},
		},
		{
			name: "compute unknown storage ready", wantCompute: "unknown", wantStorage: "ready", wantComputeStatus: compute.Status, wantCBS: "ATTACHED",
			response: provisionerResponse{
				OK: false, ErrorCode: "provider_truth_compute_sku_mismatch", MachinePresent: nil, StoragePresent: boolPointer(true), CBSStatus: "ATTACHED", ProviderRequestID: "req-mismatch",
				ProviderData: map[string]string{
					"storageChargeType": "PREPAID", "storageRenewFlag": "NOTIFY_AND_MANUAL_RENEW", "storageDeadline": storage.Deadline, "storageDiskType": storage.DiskType, "storageSizeGb": "10", "storageZone": storage.Zone,
					"opl_account_id": storage.CostTags["opl_account_id"], "opl_workspace_id": storage.CostTags["opl_workspace_id"], "opl_resource_id": storage.CostTags["opl_resource_id"], "opl_operation_id": storage.CostTags["opl_operation_id"],
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := NewTencentProvider()
			provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
				if request.Action != "provider_truth" {
					t.Fatalf("action=%q", request.Action)
				}
				return tc.response, nil
			}
			provider.kubectl = func(context.Context, []string, []byte) ([]byte, error) {
				t.Fatal("monthly provider truth must not call kubectl")
				return nil, nil
			}

			truth, err := provider.MonthlyProviderTruth(context.Background(), compute, storage)

			if err != nil || truth.ComputeState != tc.wantCompute || truth.StorageState != tc.wantStorage || truth.Compute.Status != tc.wantComputeStatus || truth.Storage.CBSStatus != tc.wantCBS {
				t.Fatalf("truth=%#v err=%v", truth, err)
			}
			if tc.wantStorage == "ready" && (truth.Storage.Status != "ready" || truth.Storage.ProviderData["chargeType"] != "PREPAID" || truth.Storage.Zone != storage.Zone || truth.Storage.DiskType != storage.DiskType) {
				t.Fatalf("storage authoritative facts=%#v", truth.Storage)
			}
		})
	}
}

func TestTencentProviderMonthlyProviderTruthRejectsIncompleteLocalIdentityWithoutProvisionerOrKubectl(t *testing.T) {
	t.Setenv("TENCENT_DEPLOY_CLUSTER_ID", "cls-123")
	compute, storage := monthlyTruthResources()
	compute.InstanceID, compute.CVMInstanceID = "", ""
	provider := NewTencentProvider()
	provider.provision = func(context.Context, provisionerRequest) (provisionerResponse, error) {
		t.Fatal("incomplete local identity must not reach provisioner")
		return provisionerResponse{}, nil
	}
	provider.kubectl = func(context.Context, []string, []byte) ([]byte, error) {
		t.Fatal("incomplete local identity must not reach kubectl")
		return nil, nil
	}

	truth, err := provider.MonthlyProviderTruth(context.Background(), compute, storage)

	if err == nil || truth.ComputeState != "unknown" || truth.StorageState != "unknown" || truth.ProviderRequestID != "" || truth.ErrorCode != "" {
		t.Fatalf("incomplete local identity truth=%#v err=%v", truth, err)
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

func TestWorkspaceManifestIsolatesTenantRuntime(t *testing.T) {
	t.Setenv("OPL_WORKSPACE_IMAGE", "workspace-image:test")
	t.Setenv("OPL_IMAGE_PULL_SECRET_NAME", "pull-secret")
	t.Setenv("OPL_AIONUI_ADMIN_PASSWORD_SEED", "workspace-secret-2026-very-long")
	t.Setenv("OPL_CODEX_API_KEY", "forbidden-global-key")
	t.Setenv("OPL_CODEX_BASE_URL", "https://gflabtoken.cn/v1")
	compute := ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", PackageID: "basic", NodeSelector: map[string]any{"cloud.tencent.com/node-instance-id": "np-basic-2"}}
	storage := StorageVolume{ProviderResourceID: "disk-storage-alpha", ProviderData: map[string]string{"pvcName": "opl-storage-alpha-data"}}
	tags := map[string]string{"opl_account_id": "acct-alpha", "opl_workspace_id": "ws-alpha", "opl_resource_id": "compute-alpha", "opl_operation_id": "op-alpha"}
	var manifest map[string]any
	if err := json.Unmarshal(workspaceManifest("ws-alpha", "Alpha", "token", "opl-compute-alpha", compute, storage, "opl-gateway-acct-alpha", tags), &manifest); err != nil {
		t.Fatalf("decode workspace manifest: %v", err)
	}
	var deployment map[string]any
	var networkPolicy map[string]any
	var service map[string]any
	var secret map[string]any
	for _, item := range manifest["items"].([]any) {
		candidate := item.(map[string]any)
		if candidate["kind"] == "Deployment" {
			deployment = candidate
		}
		if candidate["kind"] == "NetworkPolicy" {
			networkPolicy = candidate
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
	if hostNetwork, ok := podSpec["hostNetwork"]; ok && hostNetwork != false {
		t.Fatalf("workspace pod must not share the node network namespace: %#v", podSpec)
	}
	if podSpec["dnsPolicy"] != "ClusterFirst" || podSpec["automountServiceAccountToken"] != false {
		t.Fatalf("workspace pod must use cluster DNS without a service account token: %#v", podSpec)
	}
	if nested(podSpec, "securityContext", "runAsNonRoot") != true || number(nested(podSpec, "securityContext", "runAsUser")) != 10001 ||
		number(nested(podSpec, "securityContext", "runAsGroup")) != 10001 || number(nested(podSpec, "securityContext", "fsGroup")) != 10001 ||
		nested(podSpec, "securityContext", "seccompProfile", "type") != "RuntimeDefault" {
		t.Fatalf("workspace pod must use the RuntimeDefault seccomp profile: %#v", podSpec["securityContext"])
	}
	toleration := podSpec["tolerations"].([]any)[0].(map[string]any)
	if toleration["key"] != "tke.cloud.tencent.com/eni-ip-unavailable" || toleration["effect"] != "NoSchedule" {
		t.Fatalf("workspace pod must tolerate TKE ENI readiness taint: %#v", toleration)
	}
	container := podSpec["containers"].([]any)[0].(map[string]any)
	containerSecurity, ok := container["securityContext"].(map[string]any)
	if !ok {
		t.Fatalf("workspace container securityContext missing: %#v", container)
	}
	if containerSecurity["allowPrivilegeEscalation"] != false || !reflect.DeepEqual(nested(containerSecurity, "capabilities", "drop"), []any{"ALL"}) {
		t.Fatalf("workspace container must prevent privilege escalation and drop all capabilities: %#v", containerSecurity)
	}
	policySpec, ok := networkPolicy["spec"].(map[string]any)
	if !ok {
		t.Fatalf("workspace NetworkPolicy missing: %#v", manifest["items"])
	}
	if nested(networkPolicy, "metadata", "name") != "opl-compute-alpha" ||
		nested(networkPolicy, "metadata", "labels", "oplcloud.cn/workspace-id") != "ws-alpha" ||
		nested(networkPolicy, "metadata", "annotations", "opl_operation_id") != "op-alpha" ||
		!reflect.DeepEqual(nested(policySpec, "podSelector", "matchLabels"), selector) ||
		!reflect.DeepEqual(policySpec["policyTypes"], []any{"Ingress", "Egress"}) {
		t.Fatalf("workspace NetworkPolicy must select only its immutable runtime labels: %#v", networkPolicy)
	}
	ingress := policySpec["ingress"].([]any)
	ingressRule := ingress[0].(map[string]any)
	ports := ingressRule["ports"].([]any)
	if len(ingress) != 1 || len(ports) != 1 || nested(ports[0].(map[string]any), "protocol") != "TCP" || number(nested(ports[0].(map[string]any), "port")) != 3000 {
		t.Fatalf("workspace NetworkPolicy must allow only the Runtime Service port: %#v", policySpec)
	}
	wantFrom := []any{map[string]any{"podSelector": map[string]any{"matchLabels": map[string]any{"app.kubernetes.io/name": "opl-cloud", "app.kubernetes.io/component": "control-plane"}}}}
	if !reflect.DeepEqual(ingressRule["from"], wantFrom) {
		t.Fatalf("workspace NetworkPolicy must allow only same-namespace Control Plane pods: %#v", ingressRule["from"])
	}
	if !bytes.Equal(mustJSON(policySpec["egress"]), mustJSON(workspaceEgressFixture())) {
		t.Fatalf("workspace NetworkPolicy must allow only DNS and public HTTPS outside private ranges: %#v", policySpec)
	}
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
	if _, ok := env["OPL_CODEX_BASE_URL"]; ok {
		t.Fatalf("Cloud must not inject a second Gateway base URL into Runtime: %#v", env)
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

func workspaceEgressFixture() []any {
	return []any{
		map[string]any{
			"to": []any{map[string]any{
				"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": "kube-system"}},
				"podSelector":       map[string]any{"matchLabels": map[string]any{"k8s-app": "kube-dns"}},
			}},
			"ports": []any{map[string]any{"protocol": "UDP", "port": 53}, map[string]any{"protocol": "TCP", "port": 53}},
		},
		map[string]any{
			"to": []any{map[string]any{"ipBlock": map[string]any{
				"cidr": "0.0.0.0/0", "except": []any{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16"},
			}}},
			"ports": []any{map[string]any{"protocol": "TCP", "port": 443}},
		},
		map[string]any{
			"to": []any{map[string]any{"ipBlock": map[string]any{
				"cidr": "::/0", "except": []any{"::1/128", "fc00::/7", "fe80::/10"},
			}}},
			"ports": []any{map[string]any{"protocol": "TCP", "port": 443}},
		},
	}
}

func TestWorkspaceCredentialRevisionRollsRuntime(t *testing.T) {
	t.Setenv("OPL_AIONUI_ADMIN_PASSWORD_SEED", "workspace-secret-2026-very-long")
	compute := ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", PackageID: "basic"}
	storage := StorageVolume{ProviderData: map[string]string{"pvcName": "opl-storage-alpha-data"}}
	tags := map[string]string{"opl_account_id": "acct-alpha", "opl_workspace_id": "ws-alpha", "opl_resource_id": "ws-alpha", "opl_operation_id": "op-alpha"}

	manifest := func(seed string) ([]byte, map[string]any, map[string]any) {
		t.Helper()
		raw := workspaceManifest("ws-alpha", "Alpha", seed, "opl-compute-alpha", compute, storage, "opl-gateway-acct-alpha", tags)
		var list map[string]any
		if err := json.Unmarshal(raw, &list); err != nil {
			t.Fatalf("decode manifest: %v", err)
		}
		var secret, deployment map[string]any
		for _, item := range list["items"].([]any) {
			candidate := item.(map[string]any)
			switch candidate["kind"] {
			case "Secret":
				secret = candidate
			case "Deployment":
				deployment = candidate
			}
		}
		return raw, secret["data"].(map[string]any), nested(deployment, "spec", "template", "metadata", "annotations").(map[string]any)
	}

	firstRaw, firstSecret, firstAnnotations := manifest("credential-seed-one")
	replayRaw, replaySecret, replayAnnotations := manifest("credential-seed-one")
	rotatedRaw, rotatedSecret, rotatedAnnotations := manifest("credential-seed-two")
	if !bytes.Equal(firstRaw, replayRaw) || !reflect.DeepEqual(firstSecret, replaySecret) || !reflect.DeepEqual(firstAnnotations, replayAnnotations) {
		t.Fatal("same credential seed must produce a byte-identical manifest")
	}
	if firstSecret["webui_password"] == rotatedSecret["webui_password"] || firstSecret["webui_session_secret"] == rotatedSecret["webui_session_secret"] {
		t.Fatalf("rotated credential Secret did not change: before=%#v after=%#v", firstSecret, rotatedSecret)
	}
	if bytes.Equal(firstRaw, rotatedRaw) {
		t.Fatal("new credential seed must change the manifest")
	}
	const revisionKey = "opl.medopl.cn/credential-revision"
	if firstAnnotations[revisionKey] != stableID("workspace-credential", "ws-alpha", "credential-seed-one")[:16] {
		t.Fatalf("credential revision annotation = %#v", firstAnnotations)
	}
	changed := 0
	for key, value := range rotatedAnnotations {
		if firstAnnotations[key] != value {
			changed++
			if key != revisionKey {
				t.Fatalf("rotation changed unrelated pod annotation %q", key)
			}
		}
	}
	if changed != 1 || len(firstAnnotations) != len(rotatedAnnotations) {
		t.Fatalf("rotation annotations changed by %d: before=%#v after=%#v", changed, firstAnnotations, rotatedAnnotations)
	}
	password := string(decodeSecretValue(t, firstSecret, "webui_password"))
	if bytes.Contains(firstRaw, []byte(password)) || bytes.Contains(firstRaw, []byte("credential-seed-one")) {
		t.Fatal("manifest metadata or payload leaked raw credential material")
	}
}

func TestWorkspaceNetworkPolicyReadinessRejectsBroaderSelectors(t *testing.T) {
	runtimeLabels := func() map[string]any {
		return map[string]any{"app.kubernetes.io/name": "opl-compute-allocation", "app.kubernetes.io/instance": "opl-compute-alpha", "oplcloud.cn/compute-allocation-id": "compute-alpha"}
	}
	newDeployment := func(labels map[string]any) map[string]any {
		return map[string]any{
			"metadata": map[string]any{"name": "opl-compute-alpha", "labels": map[string]any{"oplcloud.cn/compute-allocation-id": "compute-alpha"}},
			"spec":     map[string]any{"selector": map[string]any{"matchLabels": labels}},
		}
	}
	newPolicy := func(labels map[string]any) map[string]any {
		return map[string]any{"spec": map[string]any{
			"podSelector": map[string]any{"matchLabels": labels},
			"policyTypes": []any{"Ingress", "Egress"},
			"ingress": []any{map[string]any{
				"from":  []any{map[string]any{"podSelector": map[string]any{"matchLabels": map[string]any{"app.kubernetes.io/name": "opl-cloud", "app.kubernetes.io/component": "control-plane"}}}},
				"ports": []any{map[string]any{"protocol": "TCP", "port": 3000}},
			}},
			"egress": workspaceEgressFixture(),
		}}
	}
	labels := runtimeLabels()
	if !workspaceNetworkPolicyReady(newPolicy(labels), newDeployment(labels)) {
		t.Fatal("strict Workspace NetworkPolicy rejected")
	}
	for _, tc := range []struct {
		name      string
		configure func(map[string]any)
	}{
		{name: "workload matchExpressions", configure: func(policy map[string]any) {
			policy["spec"].(map[string]any)["podSelector"].(map[string]any)["matchExpressions"] = []any{map[string]any{"key": "tenant", "operator": "Exists"}}
		}},
		{name: "workload selector extra field", configure: func(policy map[string]any) {
			policy["spec"].(map[string]any)["podSelector"].(map[string]any)["unexpected"] = map[string]any{}
		}},
		{name: "source namespace selector", configure: func(policy map[string]any) {
			policy["spec"].(map[string]any)["ingress"].([]any)[0].(map[string]any)["from"].([]any)[0].(map[string]any)["namespaceSelector"] = map[string]any{}
		}},
		{name: "public HTTPS without private exceptions", configure: func(policy map[string]any) {
			delete(policy["spec"].(map[string]any)["egress"].([]any)[1].(map[string]any)["to"].([]any)[0].(map[string]any)["ipBlock"].(map[string]any), "except")
		}},
		{name: "second wide HTTPS rule", configure: func(policy map[string]any) {
			policy["spec"].(map[string]any)["egress"] = append(policy["spec"].(map[string]any)["egress"].([]any), map[string]any{
				"to": []any{map[string]any{"ipBlock": map[string]any{"cidr": "0.0.0.0/0"}}}, "ports": []any{map[string]any{"protocol": "TCP", "port": 443}},
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			labels := runtimeLabels()
			deployment := newDeployment(labels)
			policy := newPolicy(labels)
			tc.configure(policy)
			if workspaceNetworkPolicyReady(policy, deployment) {
				t.Fatalf("broader NetworkPolicy accepted: %#v", policy)
			}
		})
	}
	for _, tc := range []struct {
		name      string
		labels    map[string]any
		configure func(map[string]any)
	}{
		{name: "wide workload selector", labels: map[string]any{"app": "workspace"}},
		{name: "empty compute allocation", labels: map[string]any{"app.kubernetes.io/name": "opl-compute-allocation", "app.kubernetes.io/instance": "opl-compute-alpha", "oplcloud.cn/compute-allocation-id": ""}},
		{name: "deployment compute label mismatch", labels: runtimeLabels(), configure: func(deployment map[string]any) {
			deployment["metadata"].(map[string]any)["labels"].(map[string]any)["oplcloud.cn/compute-allocation-id"] = "compute-other"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deployment := newDeployment(tc.labels)
			if tc.configure != nil {
				tc.configure(deployment)
			}
			if workspaceNetworkPolicyReady(newPolicy(tc.labels), deployment) {
				t.Fatalf("invalid runtime selector accepted: deployment=%#v labels=%#v", deployment, tc.labels)
			}
		})
	}
}

func TestWorkspaceRuntimeIsolationRequiresCompleteCurrentReplicaSet(t *testing.T) {
	isolatedSpec := func(image string) map[string]any {
		return map[string]any{
			"automountServiceAccountToken": false,
			"dnsPolicy":                    "ClusterFirst",
			"securityContext":              map[string]any{"runAsNonRoot": true, "runAsUser": 10001, "runAsGroup": 10001, "fsGroup": 10001, "seccompProfile": map[string]any{"type": "RuntimeDefault"}},
			"containers": []any{map[string]any{
				"name": "workspace", "image": image,
				"securityContext": map[string]any{"allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []any{"ALL"}}},
			}},
		}
	}
	deployment := func(image string) map[string]any {
		return map[string]any{
			"metadata": map[string]any{"generation": 2},
			"spec":     map[string]any{"replicas": 1, "template": map[string]any{"spec": isolatedSpec(image)}},
			"status":   map[string]any{"observedGeneration": 2, "updatedReplicas": 1, "readyReplicas": 1, "availableReplicas": 1},
		}
	}
	pod := func(name string, image string, ready bool) map[string]any {
		containerState := map[string]any{"running": map[string]any{}}
		if !ready {
			containerState = map[string]any{"waiting": map[string]any{"reason": "ImagePullBackOff"}}
		}
		return map[string]any{
			"metadata": map[string]any{"name": name},
			"spec":     isolatedSpec(image),
			"status": map[string]any{
				"conditions":        []any{map[string]any{"type": "Ready", "status": map[bool]string{true: "True", false: "False"}[ready]}},
				"containerStatuses": []any{map[string]any{"name": "workspace", "ready": ready, "state": containerState}},
			},
		}
	}

	t.Run("old image remains Ready while new image cannot start", func(t *testing.T) {
		if workspaceRuntimeIsolationReady(deployment("workspace-image:new"), []any{
			pod("workspace-old", "workspace-image:old", true),
			pod("workspace-new", "workspace-image:new", false),
		}) {
			t.Fatal("old Ready Pod must not prove the new Workspace image rollout")
		}
	})
	t.Run("Ready Pods exceed desired replicas", func(t *testing.T) {
		if workspaceRuntimeIsolationReady(deployment("workspace-image:new"), []any{
			pod("workspace-a", "workspace-image:new", true),
			pod("workspace-b", "workspace-image:new", true),
		}) {
			t.Fatal("extra Ready Workspace Pods must keep runtime unready")
		}
	})
	for _, field := range []string{"updatedReplicas", "readyReplicas", "availableReplicas"} {
		t.Run(field+" drift", func(t *testing.T) {
			current := deployment("workspace-image:new")
			current["status"].(map[string]any)[field] = 2
			if workspaceRuntimeIsolationReady(current, []any{pod("workspace-new", "workspace-image:new", true)}) {
				t.Fatalf("%s must exactly equal desired replicas", field)
			}
		})
	}
}

func TestTencentProviderWritesAccountGatewaySecretWithoutReturningRawKey(t *testing.T) {
	provider := NewTencentProvider()
	var applied []byte
	var calls [][]string
	provider.kubectl = func(_ context.Context, args []string, stdin []byte) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		switch {
		case slices.Equal(args, []string{"apply", "-f", "-"}):
			applied = append([]byte(nil), stdin...)
			return nil, nil
		case len(args) == 4 && args[0] == "get" && strings.HasPrefix(args[1], "secret/") && args[2] == "-o" && args[3] == "json":
			var manifest map[string]any
			if err := json.Unmarshal(applied, &manifest); err != nil {
				t.Fatal(err)
			}
			return json.Marshal(map[string]any{
				"apiVersion": "v1", "kind": "Secret", "type": manifest["type"], "metadata": manifest["metadata"],
				"data": map[string]any{"opl_gateway_api_key": base64.StdEncoding.EncodeToString([]byte(nested(manifest, "stringData", "opl_gateway_api_key").(string)))},
			})
		default:
			t.Fatalf("kubectl args = %#v", args)
			return nil, nil
		}
	}

	secret, err := provider.UpsertGatewaySecret(context.Background(), GatewaySecretInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", WorkspaceAPIKeyID: 19, Fingerprint: "sha256:12982dcaf26b60cde5b6b68b01556e591badb2768ac9b71525619cb4ebc646f0", GatewayAPIKey: "raw-gateway-key", IdempotencyKey: "gateway-once"})

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
	replayed, err := provider.UpsertGatewaySecret(context.Background(), GatewaySecretInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", WorkspaceAPIKeyID: 19, Fingerprint: "sha256:12982dcaf26b60cde5b6b68b01556e591badb2768ac9b71525619cb4ebc646f0", GatewayAPIKey: "raw-gateway-key", IdempotencyKey: "gateway-once"})
	if err != nil || replayed != secret {
		t.Fatalf("replayed Gateway Secret=%#v original=%#v err=%v", replayed, secret, err)
	}
	rotated, err := provider.UpsertGatewaySecret(context.Background(), GatewaySecretInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", WorkspaceAPIKeyID: 20, Fingerprint: "sha256:46b91e2f7bc95555effd550e0dd92346b5a4548d9f644a18b11602c5f1c07c68", GatewayAPIKey: "rotated-gateway-key", IdempotencyKey: "gateway-rotate"})
	if err != nil || rotated.SecretRef != secret.SecretRef || rotated.Version == secret.Version || rotated.Fingerprint == secret.Fingerprint {
		t.Fatalf("rotated Gateway Secret=%#v original=%#v err=%v", rotated, secret, err)
	}
	if len(calls) != 6 {
		t.Fatalf("Gateway Secret writes must each perform apply then authoritative get: %#v", calls)
	}
}

func TestTencentProviderGatewaySecretReadbackFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "missing key data", mutate: func(secret map[string]any) { secret["data"] = map[string]any{} }},
		{name: "malformed key data", mutate: func(secret map[string]any) { secret["data"].(map[string]any)["opl_gateway_api_key"] = "%%%" }},
		{name: "different key", mutate: func(secret map[string]any) {
			secret["data"].(map[string]any)["opl_gateway_api_key"] = base64.StdEncoding.EncodeToString([]byte("different-secret"))
		}},
		{name: "wrong kind", mutate: func(secret map[string]any) { secret["kind"] = "ConfigMap" }},
		{name: "wrong type", mutate: func(secret map[string]any) { secret["type"] = "kubernetes.io/tls" }},
		{name: "wrong name", mutate: func(secret map[string]any) { secret["metadata"].(map[string]any)["name"] = "wrong-secret" }},
		{name: "wrong label", mutate: func(secret map[string]any) {
			secret["metadata"].(map[string]any)["labels"].(map[string]any)["app.kubernetes.io/name"] = "wrong-label"
		}},
		{name: "wrong account annotation", mutate: func(secret map[string]any) {
			secret["metadata"].(map[string]any)["annotations"].(map[string]any)["oplcloud.cn/account-id"] = "acct-other"
		}},
		{name: "wrong version annotation", mutate: func(secret map[string]any) {
			secret["metadata"].(map[string]any)["annotations"].(map[string]any)["oplcloud.cn/secret-version"] = "wrong-version"
		}},
		{name: "wrong fingerprint annotation", mutate: func(secret map[string]any) {
			secret["metadata"].(map[string]any)["annotations"].(map[string]any)["oplcloud.cn/secret-fingerprint"] = "sha256:wrong"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := NewTencentProvider()
			var applied map[string]any
			provider.kubectl = func(_ context.Context, args []string, stdin []byte) ([]byte, error) {
				if slices.Equal(args, []string{"apply", "-f", "-"}) {
					if err := json.Unmarshal(stdin, &applied); err != nil {
						t.Fatal(err)
					}
					return nil, nil
				}
				secret := map[string]any{
					"apiVersion": "v1", "kind": "Secret", "type": applied["type"], "metadata": applied["metadata"],
					"data": map[string]any{"opl_gateway_api_key": base64.StdEncoding.EncodeToString([]byte("raw-gateway-key"))},
				}
				tc.mutate(secret)
				return json.Marshal(secret)
			}

			_, err := provider.UpsertGatewaySecret(context.Background(), GatewaySecretInput{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", WorkspaceAPIKeyID: 19, Fingerprint: "sha256:12982dcaf26b60cde5b6b68b01556e591badb2768ac9b71525619cb4ebc646f0", GatewayAPIKey: "raw-gateway-key", IdempotencyKey: "gateway-once"})
			if err == nil || strings.Contains(err.Error(), "raw-gateway-key") || strings.Contains(err.Error(), "different-secret") {
				t.Fatalf("Gateway Secret readback must fail closed without leaking secrets: %v", err)
			}
		})
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

func TestTencentRuntimeCreationIsDeterministicAndUsesActualReadinessAfterApply(t *testing.T) {
	t.Setenv("OPL_AIONUI_ADMIN_PASSWORD_SEED", "workspace-secret-2026-very-long")
	provider := NewTencentProvider()
	var calls [][]string
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if slices.Equal(args, []string{"apply", "-f", "-"}) {
			return nil, nil
		}
		if slices.Equal(args, []string{"get", "deployment,service,networkpolicy", "-l", "oplcloud.cn/workspace-id=ws-alpha", "-o", "json"}) {
			return mustJSON(map[string]any{"kind": "List", "items": []any{}}), nil
		}
		t.Fatalf("unexpected kubectl args: %#v", args)
		return nil, nil
	}
	runtime, err := provider.CreateWorkspaceRuntime(context.Background(), WorkspaceRuntimeInput{WorkspaceID: "ws-alpha", GatewaySecretRef: "opl-gateway-acct-alpha", IdempotencyKey: "runtime-unready"}, ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", ServiceName: "opl-compute-alpha"}, StorageVolume{ID: "storage-alpha", ProviderResourceID: "pvc/opl-storage-alpha-data"})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	replayed, replayErr := provider.CreateWorkspaceRuntime(context.Background(), WorkspaceRuntimeInput{WorkspaceID: "ws-alpha", GatewaySecretRef: "opl-gateway-acct-alpha", IdempotencyKey: "runtime-unready"}, ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", ServiceName: "opl-compute-alpha"}, StorageVolume{ID: "storage-alpha", ProviderResourceID: "pvc/opl-storage-alpha-data"})
	if runtime.Ready || runtime.Status != "not_found" || runtime.Access.CredentialStatus == "configured" || replayErr != nil || replayed.ID != runtime.ID || len(calls) != 4 {
		t.Fatalf("apply must be deterministic and followed by actual readiness: runtime=%#v replayed=%#v replayErr=%v calls=%#v", runtime, replayed, replayErr, calls)
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
			"spec": map[string]any{
				"capacity": map[string]any{"storage": "10Gi"}, "accessModes": []any{"ReadWriteOnce"}, "persistentVolumeReclaimPolicy": "Retain", "storageClassName": "",
				"csi":          map[string]any{"driver": "com.tencent.cloud.csi.cbs", "volumeHandle": "disk-storage-alpha"},
				"nodeAffinity": map[string]any{"required": map[string]any{"nodeSelectorTerms": []any{map[string]any{"matchExpressions": []any{map[string]any{"key": "topology.kubernetes.io/zone", "operator": "In", "values": []any{"ap-guangzhou-3"}}}}}}},
			},
		}
		pvc := map[string]any{
			"kind": "PersistentVolumeClaim", "metadata": map[string]any{"name": "opl-storage-alpha-data", "labels": labels},
			"spec": map[string]any{"accessModes": []any{"ReadWriteOnce"}, "storageClassName": "", "volumeName": "opl-storage-alpha-pv", "resources": map[string]any{"requests": map[string]any{"storage": "10Gi"}}}, "status": map[string]any{"phase": "Bound"},
		}
		return fixture{
			input:   StorageAttachmentInput{WorkspaceID: "ws-alpha", ComputeID: "compute-alpha", VolumeID: "storage-alpha", IdempotencyKey: "attach-alpha", OperationID: "op-attach-alpha"},
			compute: ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "running"},
			volume: StorageVolume{
				ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "ready", ProviderResourceID: "disk-storage-alpha", SizeGB: 10, Zone: "ap-guangzhou-3",
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
		current := newFixture()
		attachment, calls, err := create(current)
		replayed, replayCalls, replayErr := create(current)
		expectedID := "att_" + stableSuffix(current.input.OperationID)[:18]
		if err != nil || replayErr != nil || attachment.ID != expectedID || replayed.ID != expectedID || attachment.Status != "attached" ||
			attachment.ProviderAttachmentID != "pv/opl-storage-alpha-pv:pvc/opl-storage-alpha-data" || replayed.ProviderAttachmentID != attachment.ProviderAttachmentID || len(calls) != 1 || len(replayCalls) != 1 {
			t.Fatalf("attachment=%#v err=%v replayed=%#v replayErr=%v calls=%#v replayCalls=%#v", attachment, err, replayed, replayErr, calls, replayCalls)
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
		{name: "PV wrong zone", configure: func(current *fixture) {
			current.items[0].(map[string]any)["spec"].(map[string]any)["nodeAffinity"].(map[string]any)["required"].(map[string]any)["nodeSelectorTerms"].([]any)[0].(map[string]any)["matchExpressions"].([]any)[0].(map[string]any)["values"] = []any{"ap-guangzhou-4"}
		}},
		{name: "PV not RWO", configure: func(current *fixture) {
			current.items[0].(map[string]any)["spec"].(map[string]any)["accessModes"] = []any{"ReadWriteMany"}
		}},
		{name: "PVC not RWO", configure: func(current *fixture) {
			current.items[1].(map[string]any)["spec"].(map[string]any)["accessModes"] = []any{"ReadWriteMany"}
		}},
		{name: "PV wrong capacity", configure: func(current *fixture) {
			current.items[0].(map[string]any)["spec"].(map[string]any)["capacity"] = map[string]any{"storage": "20Gi"}
		}},
		{name: "PVC wrong capacity", configure: func(current *fixture) {
			current.items[1].(map[string]any)["spec"].(map[string]any)["resources"].(map[string]any)["requests"] = map[string]any{"storage": "20Gi"}
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
	runtimeSelector := map[string]any{"app.kubernetes.io/name": "opl-compute-allocation", "app.kubernetes.io/instance": "opl-compute-alpha", "oplcloud.cn/compute-allocation-id": "compute-alpha"}
	deployment := map[string]any{
		"kind":     "Deployment",
		"metadata": map[string]any{"name": "opl-compute-alpha", "generation": 2, "labels": map[string]any{"app.kubernetes.io/name": "opl-compute-allocation", "app.kubernetes.io/instance": "opl-compute-alpha", "oplcloud.cn/compute-allocation-id": "compute-alpha", "oplcloud.cn/workspace-id": "ws-alpha"}},
		"spec": map[string]any{"replicas": 1, "selector": map[string]any{"matchLabels": runtimeSelector}, "template": map[string]any{"metadata": map[string]any{"labels": runtimeSelector, "annotations": map[string]any{"opl.medopl.cn/credential-revision": "revision-alpha"}}, "spec": map[string]any{
			"automountServiceAccountToken": false, "dnsPolicy": "ClusterFirst", "securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 10001, "runAsGroup": 10001, "fsGroup": 10001, "seccompProfile": map[string]any{"type": "RuntimeDefault"}},
			"containers": []any{map[string]any{"name": "workspace", "image": "workspace-image:test", "securityContext": map[string]any{"allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []any{"ALL"}}}, "volumeMounts": workspaceDataMounts()}},
			"volumes":    []any{map[string]any{"name": "workspace-data", "persistentVolumeClaim": map[string]any{"claimName": "opl-storage-alpha-data"}}},
		}}},
		"status": map[string]any{"observedGeneration": 2, "updatedReplicas": 1, "readyReplicas": 1, "availableReplicas": 1},
	}
	service := map[string]any{
		"kind":     "Service",
		"metadata": map[string]any{"name": "opl-compute-alpha", "labels": map[string]any{"oplcloud.cn/workspace-id": "ws-alpha"}},
		"spec":     map[string]any{"selector": runtimeSelector},
	}
	networkPolicy := map[string]any{
		"kind":     "NetworkPolicy",
		"metadata": map[string]any{"name": "opl-compute-alpha", "labels": map[string]any{"oplcloud.cn/workspace-id": "ws-alpha"}},
		"spec": map[string]any{
			"podSelector": map[string]any{"matchLabels": runtimeSelector},
			"policyTypes": []any{"Ingress", "Egress"},
			"ingress": []any{map[string]any{
				"from":  []any{map[string]any{"podSelector": map[string]any{"matchLabels": map[string]any{"app.kubernetes.io/name": "opl-cloud", "app.kubernetes.io/component": "control-plane"}}}},
				"ports": []any{map[string]any{"protocol": "TCP", "port": 3000}},
			}},
			"egress": workspaceEgressFixture(),
		},
	}
	networkPolicies := []any{networkPolicy}
	pod := map[string]any{
		"kind": "Pod",
		"metadata": map[string]any{"name": "opl-compute-alpha-7d6c", "labels": map[string]any{
			"app.kubernetes.io/name": "opl-compute-allocation", "app.kubernetes.io/instance": "opl-compute-alpha", "oplcloud.cn/compute-allocation-id": "compute-alpha", "oplcloud.cn/workspace-id": "ws-alpha",
		}},
		"spec": map[string]any{
			"nodeName": "10.0.0.8", "automountServiceAccountToken": false, "dnsPolicy": "ClusterFirst", "securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 10001, "runAsGroup": 10001, "fsGroup": 10001, "seccompProfile": map[string]any{"type": "RuntimeDefault"}},
			"containers": []any{map[string]any{"name": "workspace", "image": "workspace-image:test", "securityContext": map[string]any{"allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []any{"ALL"}}}, "volumeMounts": workspaceDataMounts()}},
			"volumes":    []any{map[string]any{"name": "workspace-data", "persistentVolumeClaim": map[string]any{"claimName": "opl-storage-alpha-data"}}},
		},
		"status": map[string]any{
			"phase": "Running",
			"conditions": []any{
				map[string]any{"type": "PodScheduled", "status": "True"},
				map[string]any{"type": "Ready", "status": "True"},
			},
			"containerStatuses": []any{map[string]any{"name": "workspace", "ready": true, "restartCount": 0, "state": map[string]any{"running": map[string]any{}}}},
		},
	}
	pods := []any{pod}
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		if len(args) == 6 && args[0] == "get" && args[1] == "deployment,service,networkpolicy" && args[2] == "-l" && args[3] == "oplcloud.cn/workspace-id=ws-alpha" {
			return mustJSON(map[string]any{"kind": "List", "items": []any{deployment, service}}), nil
		}
		if slices.Equal(args, []string{"get", "networkpolicy", "-o", "json"}) {
			return mustJSON(map[string]any{"kind": "List", "items": networkPolicies}), nil
		}
		if slices.Equal(args, []string{"get", "pod", "-l", "oplcloud.cn/workspace-id=ws-alpha", "-o", "json"}) {
			return mustJSON(map[string]any{"kind": "List", "items": pods}), nil
		}
		want := []string{"get", "deployment/opl-compute-alpha", "pvc/opl-storage-alpha-data", "service/opl-compute-alpha", "ingress/opl-cloud", "endpoints/opl-compute-alpha", "secret/opl-compute-alpha-env", "--ignore-not-found", "-o", "json"}
		if !slices.Equal(args, want) {
			t.Fatalf("kubectl args = %#v, want %#v", args, want)
		}
		return mustJSON(map[string]any{"kind": "List", "items": []any{
			deployment,
			map[string]any{"kind": "PersistentVolumeClaim", "metadata": map[string]any{"name": "opl-storage-alpha-data"}, "status": map[string]any{"phase": "Bound"}},
			service,
			networkPolicy,
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
	for _, name := range []string{"pvc_bound", "deployment_uses_retained_pvc", "deployment_ready", "workspace_network_policy", "workspace_runtime_isolation"} {
		if !verified[name] {
			t.Fatalf("runtime must own final mount/readiness proof %q: %#v", name, status.Checks)
		}
	}
	if status.Access.Password != "secret-password" || status.Access.Username != webuiUsername || status.Access.CredentialStatus != "configured" || status.Access.CredentialVersion != "revision-alpha" || status.Access.SecretRef != "opl-compute-alpha-env" {
		t.Fatalf("runtime access must come transiently from Workspace Secret: %#v", status.Access)
	}
	assertUnready := func(name string) {
		t.Helper()
		status, err := provider.WorkspaceRuntimeStatus(context.Background(), "ws-alpha")
		if err != nil || status.Ready || status.Status != "unready" {
			t.Fatalf("%s runtime status=%#v err=%v", name, status, err)
		}
	}
	networkPolicies = append(networkPolicies, map[string]any{
		"kind":     "NetworkPolicy",
		"metadata": map[string]any{"name": "workspace-egress-open"},
		"spec": map[string]any{
			"podSelector": map[string]any{"matchExpressions": []any{
				map[string]any{"key": "app.kubernetes.io/name", "operator": "In", "values": []any{"opl-compute-allocation"}},
				map[string]any{"key": "oplcloud.cn/compute-allocation-id", "operator": "Exists"},
			}},
			"policyTypes": []any{"Egress"},
			"egress":      []any{map[string]any{}},
		},
	})
	assertUnready("additional NetworkPolicy allows unrestricted egress")
	networkPolicies = networkPolicies[:1]
	podLabels := pod["metadata"].(map[string]any)["labels"].(map[string]any)
	podLabels["app.kubernetes.io/instance"] = "opl-compute-other"
	assertUnready("Ready Pod NetworkPolicy selector labels drift")
	podLabels["app.kubernetes.io/instance"] = "opl-compute-alpha"
	podLabels["oplcloud.cn/runtime-marker"] = "live"
	networkPolicies = append(networkPolicies, map[string]any{
		"kind":     "NetworkPolicy",
		"metadata": map[string]any{"name": "live-pod-egress-open"},
		"spec": map[string]any{
			"podSelector": map[string]any{"matchLabels": map[string]any{"oplcloud.cn/runtime-marker": "live"}},
			"policyTypes": []any{"Egress"},
			"egress":      []any{map[string]any{}},
		},
	})
	assertUnready("additional NetworkPolicy selects only the actual Pod")
	networkPolicies = networkPolicies[:1]
	delete(podLabels, "oplcloud.cn/runtime-marker")
	podSpec := pod["spec"].(map[string]any)
	podContainers := podSpec["containers"].([]any)
	podSpec["containers"] = append(podContainers, map[string]any{
		"name": "debug", "image": "debug:test", "securityContext": map[string]any{"privileged": true},
	})
	assertUnready("Ready Pod privileged sidecar")
	podSpec["containers"] = podContainers
	podSpec["initContainers"] = []any{map[string]any{
		"name": "bootstrap", "image": "bootstrap:test", "securityContext": map[string]any{"privileged": true},
	}}
	assertUnready("Ready Pod privileged initContainer")
	delete(podSpec, "initContainers")
	podSpec["ephemeralContainers"] = []any{map[string]any{
		"name": "debug", "image": "debug:test", "securityContext": map[string]any{"privileged": true},
	}}
	assertUnready("Ready Pod privileged ephemeral container")
	delete(podSpec, "ephemeralContainers")
	extraPod := map[string]any{
		"kind":     "Pod",
		"metadata": map[string]any{"name": "opl-compute-alpha-old", "labels": podLabels},
		"spec":     podSpec,
		"status":   map[string]any{"phase": "Running", "conditions": []any{map[string]any{"type": "Ready", "status": "False"}}},
	}
	pods = append(pods, extraPod)
	assertUnready("additional Running NotReady Workspace Pod")
	extraPod["status"].(map[string]any)["phase"] = "Succeeded"
	status, err = provider.WorkspaceRuntimeStatus(context.Background(), "ws-alpha")
	if err != nil || !status.Ready {
		t.Fatalf("terminal Workspace Pod must not count against active replicas: status=%#v err=%v", status, err)
	}
	pods = pods[:1]
	deploymentContainer := deployment["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	podContainer := podSpec["containers"].([]any)[0].(map[string]any)
	podContainerSecurity := podContainer["securityContext"].(map[string]any)
	podContainerSecurity["runAsNonRoot"] = false
	podContainerSecurity["runAsUser"] = 0
	podContainerSecurity["runAsGroup"] = 0
	assertUnready("Ready Pod workspace container overrides identity as root")
	delete(podContainerSecurity, "runAsNonRoot")
	delete(podContainerSecurity, "runAsUser")
	delete(podContainerSecurity, "runAsGroup")
	delete(deploymentContainer, "volumeMounts")
	assertUnready("deployment mounts missing")
	deploymentContainer["volumeMounts"] = workspaceDataMounts()
	delete(podContainer, "volumeMounts")
	assertUnready("pod mounts missing")
	podContainer["volumeMounts"] = workspaceDataMounts()
	deploymentContainer["volumeMounts"].([]any)[0].(map[string]any)["subPath"] = "projects"
	assertUnready("deployment data subPath mismatch")
	deploymentContainer["volumeMounts"] = workspaceDataMounts()
	podContainer["volumeMounts"].([]any)[1].(map[string]any)["subPath"] = "data"
	assertUnready("pod projects subPath mismatch")
	podContainer["volumeMounts"] = workspaceDataMounts()
	pod["spec"].(map[string]any)["volumes"].([]any)[0].(map[string]any)["persistentVolumeClaim"].(map[string]any)["claimName"] = "other-pvc"
	assertUnready("pod PVC mismatch")
	pod["spec"].(map[string]any)["volumes"].([]any)[0].(map[string]any)["persistentVolumeClaim"].(map[string]any)["claimName"] = "opl-storage-alpha-data"
	pod["status"].(map[string]any)["conditions"].([]any)[1].(map[string]any)["status"] = "False"
	assertUnready("pod not Ready")
	pod["status"].(map[string]any)["conditions"].([]any)[1].(map[string]any)["status"] = "True"
	networkPolicy["spec"].(map[string]any)["ingress"].([]any)[0].(map[string]any)["ports"].([]any)[0].(map[string]any)["port"] = 3001
	assertUnready("NetworkPolicy port mismatch")
	networkPolicy["spec"].(map[string]any)["ingress"].([]any)[0].(map[string]any)["ports"].([]any)[0].(map[string]any)["port"] = 3000
	networkPolicy["spec"].(map[string]any)["ingress"].([]any)[0].(map[string]any)["from"].([]any)[0].(map[string]any)["podSelector"].(map[string]any)["matchLabels"].(map[string]any)["app.kubernetes.io/component"] = "fabric"
	assertUnready("NetworkPolicy source mismatch")
	networkPolicy["spec"].(map[string]any)["ingress"].([]any)[0].(map[string]any)["from"].([]any)[0].(map[string]any)["podSelector"].(map[string]any)["matchLabels"].(map[string]any)["app.kubernetes.io/component"] = "control-plane"
	pod["spec"].(map[string]any)["hostNetwork"] = true
	assertUnready("old host-network Ready Pod")
	delete(pod["spec"].(map[string]any), "hostNetwork")
	deployment["status"].(map[string]any)["observedGeneration"] = 1
	assertUnready("Deployment generation not observed")
	deployment["status"].(map[string]any)["observedGeneration"] = 2
	deployment["status"].(map[string]any)["updatedReplicas"] = 0
	assertUnready("Deployment update incomplete")
	if !verified["ready_pod_uses_retained_pvc"] {
		t.Fatalf("runtime must verify Ready Pod retained mount: %#v", status.Checks)
	}
}

func workspaceDataMounts() []any {
	return []any{
		map[string]any{"name": "workspace-data", "mountPath": "/data", "subPath": "data"},
		map[string]any{"name": "workspace-data", "mountPath": "/projects", "subPath": "projects"},
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
	if len(calls) != 2 || calls[1][0] != "delete" || !slices.Contains(calls[1], "deployment/opl-compute-alpha") || !slices.Contains(calls[1], "service/opl-compute-alpha") || !slices.Contains(calls[1], "networkpolicy/opl-compute-alpha") || !slices.Contains(calls[1], "secret/opl-compute-alpha-env") || slices.Contains(calls[1], "ingress/opl-cloud") {
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
	if len(calls) != 2 || calls[0][1] != "deployment,service,networkpolicy,secret" || !slices.Contains(calls[1], "networkpolicy/opl-compute-alpha") || !slices.Contains(calls[1], "secret/opl-compute-alpha-env") || slices.Contains(calls[1], "ingress/opl-cloud") {
		t.Fatalf("kubectl calls = %#v", calls)
	}
}

func TestDestroyWorkspaceRuntimeDeletesNetworkPolicyOnlyRemnant(t *testing.T) {
	provider := NewTencentProvider()
	var calls [][]string
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if args[0] == "get" {
			return []byte(`{"items":[{"kind":"NetworkPolicy","metadata":{"name":"opl-compute-alpha","labels":{"oplcloud.cn/workspace-id":"ws-alpha"}}}]}`), nil
		}
		return nil, nil
	}

	runtime, err := provider.DestroyWorkspaceRuntime(context.Background(), "ws-alpha")
	if err != nil || runtime.Status != "destroyed" || runtime.ServiceName != "opl-compute-alpha" {
		t.Fatalf("destroy policy-only runtime = %#v err=%v", runtime, err)
	}
	if len(calls) != 2 || calls[0][1] != "deployment,service,networkpolicy,secret" || !slices.Contains(calls[1], "networkpolicy/opl-compute-alpha") {
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
