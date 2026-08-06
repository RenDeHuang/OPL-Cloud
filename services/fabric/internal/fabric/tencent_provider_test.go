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

	"opl-cloud/services/fabric/internal/protectedresource"
)

func TestMain(m *testing.M) {
	for key, value := range map[string]string{
		"OPL_SYSTEM_COMPUTE_NODE_POOL_ID": "np-system",
		"OPL_SYSTEM_COMPUTE_MACHINE_ID":   "machine-system",
		"OPL_SYSTEM_COMPUTE_NODE_NAME":    "10.66.0.42",
		"OPL_SYSTEM_COMPUTE_MACHINE_TYPE": "NativeCVM",
		"OPL_SYSTEM_COMPUTE_CVM_ID":       "ins-system",
		"OPL_BASIC_COMPUTE_NODE_POOL_ID":  "np-basic",
		"OPL_PRO_COMPUTE_NODE_POOL_ID":    "np-pro",
		"OPL_BASIC_COMPUTE_INSTANCE_TYPE": "SA5.MEDIUM4",
		"OPL_PRO_COMPUTE_INSTANCE_TYPE":   "SA5.2XLARGE16",
	} {
		_ = os.Setenv(key, value)
	}
	os.Exit(m.Run())
}

func setProtectedResourceEnv(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{
		"OPL_SYSTEM_COMPUTE_NODE_POOL_ID": "np-system",
		"OPL_SYSTEM_COMPUTE_MACHINE_ID":   "machine-system",
		"OPL_SYSTEM_COMPUTE_NODE_NAME":    "10.66.0.42",
		"OPL_SYSTEM_COMPUTE_MACHINE_TYPE": "NativeCVM",
		"OPL_SYSTEM_COMPUTE_CVM_ID":       "ins-system",
		"OPL_BASIC_COMPUTE_NODE_POOL_ID":  "np-basic",
		"OPL_PRO_COMPUTE_NODE_POOL_ID":    "np-pro",
	} {
		t.Setenv(key, value)
	}
}

func TestKubernetesMutationRequiresProtectedResourceConfiguration(t *testing.T) {
	for _, key := range []string{
		"OPL_SYSTEM_COMPUTE_NODE_POOL_ID", "OPL_SYSTEM_COMPUTE_MACHINE_ID", "OPL_SYSTEM_COMPUTE_NODE_NAME",
		"OPL_SYSTEM_COMPUTE_MACHINE_TYPE", "OPL_SYSTEM_COMPUTE_CVM_ID", "OPL_BASIC_COMPUTE_NODE_POOL_ID", "OPL_PRO_COMPUTE_NODE_POOL_ID",
	} {
		t.Setenv(key, "")
	}
	provider := NewTencentProvider()
	calls := 0
	provider.kubectl = func(_ context.Context, _ []string, _ []byte) ([]byte, error) {
		calls++
		return []byte(`{"items":[]}`), nil
	}

	if _, err := provider.callKubectl(context.Background(), []string{"apply", "-f", "-"}, []byte(`{}`), protectedresource.Target{}); err == nil || err.Error() != "protected_resource_guard_configuration_invalid" || calls != 0 {
		t.Fatalf("mutation err=%v calls=%d", err, calls)
	}
	if _, err := provider.callKubectl(context.Background(), []string{"get", "pods", "-o", "json"}, nil, protectedresource.Target{}); err != nil || calls != 1 {
		t.Fatalf("read-only err=%v calls=%d", err, calls)
	}
}

func TestKubernetesMutationAcceptsExplicitNonCVMSystemIdentityWithoutCVM(t *testing.T) {
	setProtectedResourceEnv(t)
	t.Setenv("OPL_SYSTEM_COMPUTE_MACHINE_TYPE", "Native")
	t.Setenv("OPL_SYSTEM_COMPUTE_CVM_ID", "")
	provider := NewTencentProvider()
	calls := 0
	provider.kubectl = func(_ context.Context, _ []string, _ []byte) ([]byte, error) {
		calls++
		return []byte(`{"items":[]}`), nil
	}

	if _, err := provider.callKubectl(context.Background(), []string{"apply", "-f", "-"}, []byte(`{}`), protectedresource.Target{}); err != nil || calls != 1 {
		t.Fatalf("non-CVM system mutation guard err=%v calls=%d", err, calls)
	}
}

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

func TestTencentProviderRuntimeHealthSummaryUsesOneAggregateRead(t *testing.T) {
	provider := NewTencentProvider()
	calls := 0
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		calls++
		if !slices.Equal(args, []string{"get", "deployment,pod", "-l", "oplcloud.cn/workspace-id", "-o", "json"}) {
			t.Fatalf("kubectl args = %#v", args)
		}
		return mustJSON(map[string]any{"kind": "List", "items": []any{
			map[string]any{"kind": "Deployment", "metadata": map[string]any{"labels": map[string]any{"oplcloud.cn/workspace-id": "ws-ready"}}, "status": map[string]any{"readyReplicas": 1, "availableReplicas": 1}},
			map[string]any{"kind": "Pod", "metadata": map[string]any{"labels": map[string]any{"oplcloud.cn/workspace-id": "ws-ready"}}, "status": map[string]any{"phase": "Running", "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}},
			map[string]any{"kind": "Deployment", "metadata": map[string]any{"labels": map[string]any{"oplcloud.cn/workspace-id": "ws-unready"}}, "status": map[string]any{"readyReplicas": 0, "availableReplicas": 0}},
			map[string]any{"kind": "Pod", "metadata": map[string]any{"labels": map[string]any{"oplcloud.cn/workspace-id": "ws-unready"}}, "status": map[string]any{"phase": "Pending"}},
		}}), nil
	}

	summary, err := provider.RuntimeHealthSummary(context.Background())
	if err != nil || summary.Total != 2 || summary.Ready != 1 || summary.Unready != 1 || calls != 1 {
		t.Fatalf("summary=%#v err=%v calls=%d", summary, err, calls)
	}
}

func TestTencentProviderRuntimeHealthSummaryFailsClosedOnInvalidList(t *testing.T) {
	for _, payload := range [][]byte{[]byte("not-json"), []byte(`{"kind":"Deployment"}`)} {
		provider := NewTencentProvider()
		provider.kubectl = func(context.Context, []string, []byte) ([]byte, error) { return payload, nil }
		if summary, err := provider.RuntimeHealthSummary(context.Background()); err == nil {
			t.Fatalf("invalid payload returned summary=%#v", summary)
		}
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

func TestTencentProviderMonthlyPreflightUsesExactConfiguredPackagePool(t *testing.T) {
	t.Setenv("OPL_BASIC_COMPUTE_NODE_POOL_ID", "np-basic")
	t.Setenv("OPL_PRO_COMPUTE_NODE_POOL_ID", "np-pro")
	t.Setenv("OPL_BASIC_COMPUTE_NODE_POOL_MAX_REPLICAS", "40")
	t.Setenv("OPL_PRO_COMPUTE_NODE_POOL_MAX_REPLICAS", "12")
	for _, tc := range []struct {
		name  string
		input MonthlyPreflightInput
		check func(*testing.T, provisionerRequest)
		reply provisionerResponse
	}{
		{
			name: "compute", input: MonthlyPreflightInput{ResourceType: "compute", PackageID: "basic", Zone: "na-siliconvalley-1"},
			check: func(t *testing.T, request provisionerRequest) {
				if request.Action != "capacity_preflight" || request.PackageID != "basic" || request.Zone != "na-siliconvalley-1" || request.Pool.ID != "pool-basic-2c4g" || request.Pool.NodePoolID != "np-basic" || request.Pool.InstanceType != "SA5.MEDIUM4" || request.Pool.CPU != 2 || request.Pool.MemoryGB != 4 || request.Pool.DesiredReplicas != 1 || request.Pool.MaxReplicas != 40 {
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
				if request.Action != "capacity_preflight" || request.PackageID != "pro" || request.Zone != "na-siliconvalley-1" || request.Pool.ID != "pool-pro-8c16g" || request.Pool.NodePoolID != "np-pro" || request.Pool.InstanceType != "SA5.2XLARGE16" || request.Pool.CPU != 8 || request.Pool.MemoryGB != 16 || request.Pool.DesiredReplicas != 1 || request.Pool.MaxReplicas != 12 {
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
			kubectlCalls := 0
			provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
				if request.Action == "predebit_iam_gate" {
					return successfulPredebitIAMResponse(), nil
				}
				tc.check(t, request)
				return tc.reply, nil
			}
			provider.kubectl = func(_ context.Context, args []string, stdin []byte) ([]byte, error) {
				kubectlCalls++
				if tc.input.ResourceType == "storage" {
					t.Fatal("storage monthly preflight must not call kubectl")
				}
				if !slices.Equal(args, []string{"auth", "can-i", "patch", "nodes"}) || stdin != nil {
					t.Fatalf("compute preflight kubectl args=%#v stdin=%q", args, stdin)
				}
				return []byte("yes\n"), nil
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
			wantKubectlCalls := 0
			if tc.input.ResourceType == "compute" {
				wantKubectlCalls = 2
			}
			if kubectlCalls != wantKubectlCalls {
				t.Fatalf("kubectl calls=%d want=%d", kubectlCalls, wantKubectlCalls)
			}
		})
	}
}

func TestTencentProviderMonthlyComputePreflightChecksIAMBeforeNodePatchRBACAndCapacity(t *testing.T) {
	t.Setenv("RUN_TENCENT_CREATE_RELEASE_EXECUTION", "1")
	t.Setenv("OPL_BASIC_COMPUTE_NODE_POOL_MAX_REPLICAS", "40")
	t.Setenv("OPL_SYSTEM_COMPUTE_MACHINE_ID", "")
	provider := NewTencentProvider()
	events := []string{}
	provider.kubectl = func(_ context.Context, args []string, stdin []byte) ([]byte, error) {
		events = append(events, "kubectl")
		if !slices.Equal(args, []string{"auth", "can-i", "patch", "nodes"}) || stdin != nil {
			t.Fatalf("kubectl args=%#v stdin=%q", args, stdin)
		}
		return []byte("yes\n"), nil
	}
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Action == "predebit_iam_gate" {
			events = append(events, "iam")
			return successfulPredebitIAMResponse(), nil
		}
		events = append(events, "capacity")
		return provisionerResponse{
			OK: true, Status: "ready", NodePoolID: "np-basic", InstanceType: "SA5.MEDIUM4", InstanceAvailable: true, Zones: []string{"na-siliconvalley-1"},
			ProviderPriceCNY: 142.91, RemainingQuota: 8, ProviderRequestIDs: map[string]string{"nodePool": "req-pool", "subnets": "req-subnets", "availability": "req-capacity", "quota": "req-quota"},
			ProviderData: map[string]string{"chargeType": "PREPAID", "periodMonths": "1", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "zone": "na-siliconvalley-1"},
		}, nil
	}

	result, err := provider.MonthlyPreflight(context.Background(), MonthlyPreflightInput{ResourceType: "compute", PackageID: "basic", Zone: "na-siliconvalley-1"})

	if err != nil || !result.Available {
		t.Fatalf("preflight=%#v err=%v", result, err)
	}
	if !reflect.DeepEqual(events, []string{"iam", "kubectl", "capacity", "kubectl"}) {
		t.Fatalf("events=%#v", events)
	}
}

func successfulPredebitIAMResponse() provisionerResponse {
	return provisionerResponse{
		OK: true, Status: "ready", MutationCount: 0,
		ProviderData: map[string]string{
			"proofMode": "production_runner_deployment_attestation", "releaseSha": strings.Repeat("a", 40),
			"requiredActions": "tag:TagResources,tag:ModifyResourcesTagValue", "policyDigest": "sha256:" + strings.Repeat("b", 64),
		},
	}
}

func TestTencentProviderMonthlyComputePreflightIAMFailureStopsBeforeRBACAndCapacity(t *testing.T) {
	t.Setenv("RUN_TENCENT_CREATE_RELEASE_EXECUTION", "1")
	t.Setenv("OPL_BASIC_COMPUTE_NODE_POOL_MAX_REPLICAS", "40")
	provider := NewTencentProvider()
	kubectlCalls, capacityCalls := 0, 0
	provider.kubectl = func(context.Context, []string, []byte) ([]byte, error) {
		kubectlCalls++
		return []byte("yes\n"), nil
	}
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Action == "predebit_iam_gate" {
			return provisionerResponse{OK: false, ErrorCode: "predebit_iam_identity_mismatch", MutationCount: 0}, nil
		}
		capacityCalls++
		return provisionerResponse{OK: true, Status: "ready"}, nil
	}

	result, err := provider.MonthlyPreflight(context.Background(), MonthlyPreflightInput{ResourceType: "compute", PackageID: "basic", Zone: "na-siliconvalley-1"})

	if err == nil || err.Error() != "predebit_iam_identity_mismatch" || result.Available || kubectlCalls != 0 || capacityCalls != 0 {
		t.Fatalf("preflight=%#v err=%v kubectlCalls=%d capacityCalls=%d", result, err, kubectlCalls, capacityCalls)
	}
}

func TestTencentProviderMonthlyComputePreflightFailsClosedOnNodePatchRBAC(t *testing.T) {
	t.Setenv("RUN_TENCENT_CREATE_RELEASE_EXECUTION", "1")
	t.Setenv("OPL_BASIC_COMPUTE_NODE_POOL_MAX_REPLICAS", "40")
	for _, tc := range []struct {
		name             string
		outputs          [][]byte
		errors           []error
		nilKubectl       bool
		wantKubectlCalls int
		wantCapacity     int
	}{
		{name: "pre nil kubectl", nilKubectl: true},
		{name: "pre no", outputs: [][]byte{[]byte("no\n")}, wantKubectlCalls: 1},
		{name: "pre empty", outputs: [][]byte{[]byte("")}, wantKubectlCalls: 1},
		{name: "pre error", errors: []error{errors.New("forbidden")}, wantKubectlCalls: 1},
		{name: "pre abnormal", outputs: [][]byte{[]byte("yes unexpected\n")}, wantKubectlCalls: 1},
		{name: "post no", outputs: [][]byte{[]byte("yes\n"), []byte("no\n")}, wantKubectlCalls: 2, wantCapacity: 1},
		{name: "post empty", outputs: [][]byte{[]byte("yes\n"), []byte("")}, wantKubectlCalls: 2, wantCapacity: 1},
		{name: "post error", outputs: [][]byte{[]byte("yes\n")}, errors: []error{nil, errors.New("forbidden")}, wantKubectlCalls: 2, wantCapacity: 1},
		{name: "post abnormal", outputs: [][]byte{[]byte("yes\n"), []byte("allowed\n")}, wantKubectlCalls: 2, wantCapacity: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := NewTencentProvider()
			kubectlCalls, iamCalls, capacityCalls := 0, 0, 0
			if tc.nilKubectl {
				provider.kubectl = nil
			} else {
				provider.kubectl = func(_ context.Context, args []string, stdin []byte) ([]byte, error) {
					if !slices.Equal(args, []string{"auth", "can-i", "patch", "nodes"}) || stdin != nil {
						t.Fatalf("kubectl args=%#v stdin=%q", args, stdin)
					}
					index := kubectlCalls
					kubectlCalls++
					var output []byte
					if index < len(tc.outputs) {
						output = tc.outputs[index]
					}
					var err error
					if index < len(tc.errors) {
						err = tc.errors[index]
					}
					return output, err
				}
			}
			provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
				if request.Action == "predebit_iam_gate" {
					iamCalls++
					return successfulPredebitIAMResponse(), nil
				}
				capacityCalls++
				return provisionerResponse{
					OK: true, Status: "ready", NodePoolID: "np-basic", InstanceType: "SA5.MEDIUM4", InstanceAvailable: true, Zones: []string{"na-siliconvalley-1"},
					ProviderPriceCNY: 142.91, RemainingQuota: 8, ProviderRequestIDs: map[string]string{"nodePool": "req-pool", "subnets": "req-subnets", "availability": "req-capacity", "quota": "req-quota"},
					ProviderData: map[string]string{"chargeType": "PREPAID", "periodMonths": "1", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "zone": "na-siliconvalley-1"},
				}, nil
			}

			_, err := provider.MonthlyPreflight(context.Background(), MonthlyPreflightInput{ResourceType: "compute", PackageID: "basic", Zone: "na-siliconvalley-1"})

			if err == nil || err.Error() != "kubernetes_node_patch_rbac_unavailable" || kubectlCalls != tc.wantKubectlCalls || iamCalls != 1 || capacityCalls != tc.wantCapacity {
				t.Fatalf("err=%v kubectl calls=%d iam calls=%d capacity calls=%d", err, kubectlCalls, iamCalls, capacityCalls)
			}
		})
	}
}

func TestTencentProviderMonthlyComputePreflightChecksNodePatchRBACAfterProvisionerFailure(t *testing.T) {
	t.Setenv("RUN_TENCENT_CREATE_RELEASE_EXECUTION", "1")
	t.Setenv("OPL_BASIC_COMPUTE_NODE_POOL_MAX_REPLICAS", "40")
	provider := NewTencentProvider()
	events := []string{}
	provider.kubectl = func(_ context.Context, args []string, stdin []byte) ([]byte, error) {
		events = append(events, "kubectl")
		if !slices.Equal(args, []string{"auth", "can-i", "patch", "nodes"}) || stdin != nil {
			t.Fatalf("kubectl args=%#v stdin=%q", args, stdin)
		}
		return []byte("yes\n"), nil
	}
	providerErr := errors.New("provider unavailable")
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Action == "predebit_iam_gate" {
			events = append(events, "iam")
			return successfulPredebitIAMResponse(), nil
		}
		events = append(events, "capacity")
		return provisionerResponse{}, providerErr
	}

	_, err := provider.MonthlyPreflight(context.Background(), MonthlyPreflightInput{ResourceType: "compute", PackageID: "basic", Zone: "na-siliconvalley-1"})

	if !errors.Is(err, providerErr) || !reflect.DeepEqual(events, []string{"iam", "kubectl", "capacity", "kubectl"}) {
		t.Fatalf("err=%v events=%#v", err, events)
	}
}

func TestTencentProviderMonthlyStoragePreflightNeverChecksNodePatchRBAC(t *testing.T) {
	t.Setenv("RUN_TENCENT_CREATE_RELEASE_EXECUTION", "1")
	provider := NewTencentProvider()
	provider.kubectl = func(context.Context, []string, []byte) ([]byte, error) {
		t.Fatal("storage preflight reached kubectl")
		return nil, nil
	}
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Action != "storage_preflight" {
			t.Fatalf("request=%#v", request)
		}
		return provisionerResponse{
			OK: true, Status: "ready", ProviderPriceCNY: 7.5, ProviderRequestIDs: map[string]string{"quota": "req-quota", "price": "req-price"},
			ProviderData: map[string]string{"chargeType": "PREPAID", "periodMonths": "1", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "zone": "na-siliconvalley-1", "diskType": "CLOUD_BSSD", "sizeGb": "10"},
		}, nil
	}

	result, err := provider.MonthlyPreflight(context.Background(), MonthlyPreflightInput{ResourceType: "storage", PackageID: "basic", SizeGB: 10, Zone: "na-siliconvalley-1"})
	if err != nil || !result.Available {
		t.Fatalf("preflight=%#v err=%v", result, err)
	}
}

func TestTencentProviderPreservesProvisionerTKECapacityDependencyStages(t *testing.T) {
	stages := reportStages(provisionerResponse{
		ErrorCode: "tencent_capacity_cluster_headroom_unavailable",
		PreflightStages: []MonthlyPreflightStage{
			{Stage: "node_pool_discovery", Status: "passed", BlockedBy: []string{}, SafeFacts: map[string]any{}},
			{Stage: "tke_cluster_capacity", Status: "failed", ErrorCode: "tencent_capacity_cluster_headroom_unavailable", BlockedBy: []string{}, SafeFacts: map[string]any{}},
			{Stage: "node_pool_contract", Status: "blocked", ErrorCode: "preflight_dependency_blocked", BlockedBy: []string{"tke_cluster_capacity"}, SafeFacts: map[string]any{}},
		},
	}, nil, []string{"node_pool_discovery", "tke_cluster_capacity", "node_pool_contract", "subnet", "zone", "cvm_prepaid_quota", "cvm_sku_price"})
	byName := map[string]MonthlyPreflightStage{}
	for _, stage := range stages {
		byName[stage.Stage] = stage
	}
	if byName["tke_cluster_capacity"].Status != "failed" || byName["node_pool_contract"].Status != "blocked" ||
		!reflect.DeepEqual(byName["node_pool_contract"].BlockedBy, []string{"tke_cluster_capacity"}) {
		t.Fatalf("provisioner dependency stage was not preserved: %#v", byName)
	}
}

func TestTencentProviderMonthlyComputePreflightFailsBeforeProvisionerWithoutPoolConfiguration(t *testing.T) {
	t.Setenv("RUN_TENCENT_CREATE_RELEASE_EXECUTION", "1")
	t.Setenv("OPL_BASIC_COMPUTE_NODE_POOL_ID", "")
	t.Setenv("OPL_BASIC_COMPUTE_NODE_POOL_MAX_REPLICAS", "")
	provider := NewTencentProvider()
	provider.provision = func(context.Context, provisionerRequest) (provisionerResponse, error) {
		t.Fatal("missing exact pool configuration reached provisioner")
		return provisionerResponse{}, nil
	}
	if _, err := provider.MonthlyPreflight(context.Background(), MonthlyPreflightInput{ResourceType: "compute", PackageID: "basic", Zone: "na-siliconvalley-1"}); err == nil || err.Error() != "compute_node_pool_configuration_required" {
		t.Fatalf("error=%v", err)
	}
}

func TestTencentProviderCustomerComputeRequiresReleaseOwnerResolvedInstanceType(t *testing.T) {
	t.Setenv("RUN_TENCENT_CREATE_RELEASE_EXECUTION", "1")
	t.Setenv("OPL_PRO_COMPUTE_NODE_POOL_ID", "np-pro")
	t.Setenv("OPL_PRO_COMPUTE_NODE_POOL_MAX_REPLICAS", "12")
	t.Setenv("OPL_PRO_COMPUTE_INSTANCE_TYPE", "")
	provider := NewTencentProvider()
	provider.provision = func(context.Context, provisionerRequest) (provisionerResponse, error) {
		t.Fatal("missing release-owner resolved SKU reached provisioner")
		return provisionerResponse{}, nil
	}
	if _, err := provider.MonthlyPreflight(context.Background(), MonthlyPreflightInput{ResourceType: "compute", PackageID: "pro", Zone: "na-siliconvalley-1"}); err == nil || err.Error() != "compute_instance_type_configuration_required" {
		t.Fatalf("error=%v", err)
	}
}

func TestTencentProviderPrepareComputeAllocationPersistsConfiguredPoolLimit(t *testing.T) {
	t.Setenv("OPL_BASIC_COMPUTE_NODE_POOL_ID", "np-basic")
	t.Setenv("OPL_PRO_COMPUTE_NODE_POOL_ID", "np-pro")
	t.Setenv("OPL_BASIC_COMPUTE_NODE_POOL_MAX_REPLICAS", "40")
	provider := NewTencentProvider()
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Action != "prepare_compute_allocation" || request.Pool.NodePoolID != "np-basic" || request.Pool.MaxReplicas != 40 {
			t.Fatalf("prepare request=%#v", request)
		}
		return provisionerResponse{OK: true, NodePoolID: "np-basic", CurrentReplicas: 2, TargetReplicas: 3, MaxReplicas: 40, Machines: []provisionerMachine{{MachineID: "machine-1"}, {MachineID: "machine-2"}}}, nil
	}

	prepared, err := provider.PrepareComputeAllocation(context.Background(), ComputeAllocationInput{ID: "compute-alpha", PackageID: "basic", NodePoolID: "np-basic"})
	if err != nil || prepared.MaxReplicas != 40 || prepared.BaselineReplicas != 2 || prepared.TargetReplicas != 3 {
		t.Fatalf("prepared=%#v err=%v", prepared, err)
	}

	if _, err := provider.PrepareComputeAllocation(context.Background(), ComputeAllocationInput{ID: "compute-alpha", PackageID: "basic", NodePoolID: "np-pro"}); err == nil || err.Error() != "compute_package_node_pool_mismatch" {
		t.Fatalf("mismatched pool error=%v", err)
	}
}

func TestTencentProviderMonthlyPreflightReportEvaluatesBasicAndPro(t *testing.T) {
	t.Setenv("RUN_TENCENT_CREATE_RELEASE_EXECUTION", "1")
	t.Setenv("TENCENTCLOUD_SECRET_ID", "configured")
	t.Setenv("TENCENTCLOUD_SECRET_KEY", "configured")
	t.Setenv("TENCENTCLOUD_REGION", "na-siliconvalley")
	t.Setenv("TENCENT_DEPLOY_CLUSTER_ID", "cls-production")
	t.Setenv("OPL_BASIC_COMPUTE_NODE_POOL_MAX_REPLICAS", "40")
	t.Setenv("OPL_PRO_COMPUTE_NODE_POOL_MAX_REPLICAS", "20")
	provider := NewTencentProvider()
	calls := []string{}
	kubectlCalls := 0
	provider.kubectl = func(_ context.Context, args []string, stdin []byte) ([]byte, error) {
		kubectlCalls++
		if !slices.Equal(args, []string{"auth", "can-i", "patch", "nodes"}) || stdin != nil {
			t.Fatalf("kubectl args=%#v stdin=%q", args, stdin)
		}
		return []byte("yes\n"), nil
	}
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		calls = append(calls, request.PackageID+":"+request.Action)
		switch request.Action {
		case "predebit_iam_gate":
			return successfulPredebitIAMResponse(), nil
		case "capacity_preflight":
			instanceType := packagePlan(request.PackageID).InstanceType
			return provisionerResponse{OK: false, ErrorCode: "tencent_capacity_node_pool_unavailable", PreflightStages: []MonthlyPreflightStage{
				{Stage: "node_pool_discovery", Status: "failed", ErrorCode: "tencent_capacity_node_pool_unavailable", BlockedBy: []string{}, DurationMS: 2, SafeFacts: map[string]any{"nodePoolId": request.Pool.NodePoolID, "matchCount": 0}},
				{Stage: "tke_cluster_capacity", Status: "failed", ErrorCode: "tencent_capacity_cluster_headroom_unavailable", BlockedBy: []string{}, DurationMS: 1, SafeFacts: map[string]any{"requiredReplicas": 1, "availableNodes": 0}},
				{Stage: "node_pool_contract", Status: "blocked", ErrorCode: "preflight_dependency_blocked", BlockedBy: []string{"tke_cluster_capacity"}, DurationMS: 0, SafeFacts: map[string]any{}},
				{Stage: "subnet", Status: "blocked", ErrorCode: "preflight_dependency_blocked", BlockedBy: []string{"node_pool_contract"}, DurationMS: 0, SafeFacts: map[string]any{}},
				{Stage: "zone", Status: "blocked", ErrorCode: "preflight_dependency_blocked", BlockedBy: []string{"subnet"}, DurationMS: 0, SafeFacts: map[string]any{}},
				{Stage: "cvm_prepaid_quota", Status: "failed", ErrorCode: "tencent_capacity_prepaid_quota_unavailable", BlockedBy: []string{}, DurationMS: 3, SafeFacts: map[string]any{"remainingQuota": 0}},
				{Stage: "cvm_sku_price", Status: "passed", ErrorCode: "", BlockedBy: []string{}, DurationMS: 4, SafeFacts: map[string]any{"instanceType": instanceType, "providerPriceCny": 142.91}},
			}}, nil
		case "storage_preflight":
			return provisionerResponse{OK: false, ErrorCode: "tencent_storage_price_unavailable", PreflightStages: []MonthlyPreflightStage{
				{Stage: "cbs_prepaid_quota", Status: "passed", ErrorCode: "", BlockedBy: []string{}, DurationMS: 5, SafeFacts: map[string]any{"sizeGb": request.Storage.SizeGB, "diskType": "CLOUD_BSSD"}},
				{Stage: "cbs_price", Status: "failed", ErrorCode: "tencent_storage_price_unavailable", BlockedBy: []string{}, DurationMS: 6, SafeFacts: map[string]any{"sizeGb": request.Storage.SizeGB}},
			}}, nil
		default:
			t.Fatalf("unexpected provisioner action %q", request.Action)
			return provisionerResponse{}, nil
		}
	}

	report, err := provider.MonthlyPreflightReport(context.Background(), MonthlyPreflightReportInput{Zone: "na-siliconvalley-1"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "failed" || report.Zone != "na-siliconvalley-1" || report.Sub2APIMutationCount != 0 || report.TencentMutationCount != 0 || report.KubernetesMutationCount != 0 {
		t.Fatalf("report=%#v", report)
	}
	var payload struct {
		Items    []MonthlyPreflightStage `json:"items"`
		Packages []struct {
			PackageID string                  `json:"packageId"`
			SizeGB    int                     `json:"sizeGb"`
			Status    string                  `json:"status"`
			Items     []MonthlyPreflightStage `json:"items"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(mustJSON(report), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 2 || payload.Items[0].Stage != "launch_permission" || payload.Items[1].Stage != "credentials" || len(payload.Packages) != 2 {
		t.Fatalf("payload=%#v", payload)
	}
	for index, packageReport := range payload.Packages {
		wantPackage, wantSize := []string{"basic", "pro"}[index], []int{10, 100}[index]
		if packageReport.PackageID != wantPackage || packageReport.SizeGB != wantSize || packageReport.Status != "failed" || len(packageReport.Items) != 10 || packageReport.Items[0].Stage != "tencent_predebit_iam" || packageReport.Items[0].Status != "passed" {
			t.Fatalf("package[%d]=%#v", index, packageReport)
		}
		for itemIndex, item := range packageReport.Items {
			if item.Status == "" || item.BlockedBy == nil || item.SafeFacts == nil || item.DurationMS < 0 {
				t.Fatalf("package[%d].item[%d]=%#v", index, itemIndex, item)
			}
		}
	}
	wantCalls := []string{"basic:predebit_iam_gate", "basic:capacity_preflight", "basic:storage_preflight", "pro:predebit_iam_gate", "pro:capacity_preflight", "pro:storage_preflight"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls=%#v want=%#v", calls, wantCalls)
	}
	if kubectlCalls != 4 {
		t.Fatalf("kubectl calls=%d want=4", kubectlCalls)
	}
	encoded := string(mustJSON(report))
	for _, forbidden := range []string{"configured", "cls-production", "providerRequestId", "providerRequestIds", "rawResponse", "wallet", "userData"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("report leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestTencentProviderMonthlyPreflightReportBlocksTencentChecksWithoutCredentials(t *testing.T) {
	t.Setenv("RUN_TENCENT_CREATE_RELEASE_EXECUTION", "1")
	t.Setenv("TENCENTCLOUD_SECRET_ID", "")
	t.Setenv("TENCENTCLOUD_SECRET_KEY", "")
	t.Setenv("TENCENTCLOUD_REGION", "")
	t.Setenv("TENCENT_DEPLOY_CLUSTER_ID", "")
	provider := NewTencentProvider()
	calls := 0
	provider.provision = func(context.Context, provisionerRequest) (provisionerResponse, error) {
		calls++
		return provisionerResponse{}, errors.New("must not call provisioner without credentials")
	}

	report, err := provider.MonthlyPreflightReport(context.Background(), MonthlyPreflightReportInput{Zone: "na-siliconvalley-1"})
	if err != nil || calls != 0 || report.Status != "failed" || len(report.Items) != 2 {
		t.Fatalf("report=%#v err=%v calls=%d", report, err, calls)
	}
	if report.Items[0].Status != "passed" || report.Items[1].Stage != "credentials" || report.Items[1].Status != "failed" {
		t.Fatalf("environment stages=%#v", report.Items[:2])
	}
	encoded := string(mustJSON(report))
	if !strings.Contains(encoded, `"packageId":"basic"`) || !strings.Contains(encoded, `"packageId":"pro"`) || strings.Count(encoded, `"status":"blocked"`) != 20 {
		t.Fatalf("blocked package reports=%s", encoded)
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

func computeClaimProviderFixture() (ComputeAllocation, ComputeAllocationPreparation, MachineOwnership) {
	allocation := ComputeAllocation{
		ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", Provider: "tencent-tke",
		ProviderResourceID: "ins-alpha", PoolID: "pool-basic-2c4g", NodePoolID: "np-basic", MachineName: "machine-alpha",
		InstanceID: "ins-alpha", CVMInstanceID: "ins-alpha", NodeName: "10.0.0.8", PrivateIP: "10.0.0.8", InstanceType: "SA5.MEDIUM4",
		Zone: "ap-guangzhou-3", ChargeType: "PREPAID", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2026-08-28T00:00:00Z",
	}
	plan := ComputeAllocationPreparation{
		PoolID: "pool-basic-2c4g", PackageID: "basic", NodePoolID: "np-basic", InstanceType: "SA5.MEDIUM4",
		MaxReplicas: 20, BaselineReplicas: 1, TargetReplicas: 2, BeforeMachineNames: []string{"machine-before"},
	}
	ownership := MachineOwnership{
		ID: "owner-alpha", ResourceID: allocation.ID, AccountID: allocation.AccountID, WorkspaceID: allocation.WorkspaceID,
		PackageID: allocation.PackageID, NodePoolID: allocation.NodePoolID, MachineID: allocation.MachineName, InstanceID: allocation.InstanceID,
		NodeName: allocation.NodeName, Status: "quarantined",
	}
	return allocation, plan, ownership
}

func TestTencentProviderDiscoversStorageRecoveryThroughProvisionerWithoutMutation(t *testing.T) {
	provider := NewTencentProvider()
	input := StorageVolumeInput{
		ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ComputeID: "compute-alpha",
		Zone: "ap-guangzhou-3", SizeGB: 10, IdempotencyKey: "launch-alpha:storage", OperationID: "op-storage-alpha",
	}
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Action != "discover_storage_volume" || request.AccountID != input.AccountID || request.Storage.ID != input.ID ||
			request.Storage.Zone != input.Zone || request.Storage.SizeGB != uint64(input.SizeGB) || request.Storage.DiskType != "CLOUD_BSSD" ||
			!reflect.DeepEqual(request.Tags, oplCostTags(input.AccountID, input.WorkspaceID, input.ID, input.OperationID)) {
			t.Fatalf("storage recovery discovery request=%#v", request)
		}
		return provisionerResponse{
			OK: true, StorageState: "storage_existing_exact", StorageVolumeID: "disk-existing-alpha", ProviderRequestID: "req-discover-alpha", MutationCount: 0,
		}, nil
	}
	provider.kubectl = func(context.Context, []string, []byte) ([]byte, error) {
		t.Fatal("storage recovery discovery must not call kubectl")
		return nil, nil
	}

	discovery, err := provider.DiscoverStorageRecovery(context.Background(), input)

	if err != nil || discovery.State != "storage_existing_exact" || discovery.ProviderResourceID != "disk-existing-alpha" ||
		discovery.ProviderRequestID != "req-discover-alpha" || discovery.MutationCount != 0 || discovery.Reason != "" {
		t.Fatalf("storage recovery discovery=%#v err=%v", discovery, err)
	}
}

func TestTencentProviderComputeClaimRecoveryProofCombinesTencentAndNodeReadOnlyTruth(t *testing.T) {
	setProtectedResourceEnv(t)
	allocation, plan, ownership := computeClaimProviderFixture()
	provider := NewTencentProvider()
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Action != "compute_claim_truth" || request.AccountID != allocation.AccountID || request.PackageID != allocation.PackageID ||
			request.Pool.ID != plan.PoolID || request.Pool.NodePoolID != plan.NodePoolID || !slices.Equal(request.Pool.BeforeMachineNames, plan.BeforeMachineNames) ||
			request.Allocation.ID != allocation.ID || request.Allocation.InstanceID != allocation.InstanceID || request.Tags["opl_operation_id"] != ownership.ID {
			t.Fatalf("proof request=%#v", request)
		}
		return provisionerResponse{
			OK: true, Status: "proven", PoolID: plan.PoolID, NodePoolID: plan.NodePoolID, InstanceID: allocation.InstanceID,
			NodeName: allocation.NodeName, PrivateIP: allocation.PrivateIP, InstanceType: plan.InstanceType,
			ProviderData: map[string]string{
				"machineName": allocation.MachineName, "zone": allocation.Zone, "chargeType": "PREPAID", "periodMonths": "1",
				"renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": allocation.Deadline, "cvmOwnershipState": "recoverable",
			},
		}, nil
	}
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		if !slices.Equal(args, []string{"get", "node/" + allocation.NodeName, "-o", "json"}) {
			t.Fatalf("kubectl args=%#v", args)
		}
		return []byte(`{"metadata":{"name":"10.0.0.8","labels":{}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"unallocated","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`), nil
	}

	proof, err := provider.ProveComputeClaimRecovery(context.Background(), allocation, plan, ownership)

	if err != nil || proof.Status != "proven" || proof.Reason != "" || proof.NodeOwnershipState != "unallocated" || proof.CVMOwnershipState != "recoverable" ||
		proof.MachineName != allocation.MachineName || proof.NodeName != allocation.NodeName || proof.CVMInstanceID != allocation.InstanceID ||
		proof.PeriodMonths != 1 || proof.ChargeType != "PREPAID" || proof.RenewFlag != "NOTIFY_AND_MANUAL_RENEW" {
		t.Fatalf("proof=%#v err=%v", proof, err)
	}
}

func TestTencentProviderComputeClaimRecoveryProofClassifiesNodeAndDependencyFailures(t *testing.T) {
	for _, tc := range []struct {
		name       string
		kubectlRaw []byte
		kubectlErr error
		wantReason string
	}{
		{name: "node target owned", wantReason: "", kubectlRaw: []byte(`{"metadata":{"name":"10.0.0.8","labels":{"medopl.cn/workload":"workspace","oplcloud.cn/resource-id":"compute-alpha","oplcloud.cn/account-id":"acct-alpha","oplcloud.cn/workspace-id":"ws-alpha"}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"ws-alpha","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`)},
		{name: "target labels written before taint", wantReason: "", kubectlRaw: []byte(`{"metadata":{"name":"10.0.0.8","labels":{"medopl.cn/workload":"workspace","oplcloud.cn/resource-id":"compute-alpha","oplcloud.cn/account-id":"acct-alpha","oplcloud.cn/workspace-id":"ws-alpha"}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"unallocated","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`)},
		{name: "node ownership conflict", wantReason: "node_ownership_conflict", kubectlRaw: []byte(`{"metadata":{"name":"10.0.0.8","labels":{"oplcloud.cn/workspace-id":"ws-other"}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"unallocated","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`)},
		{name: "rbac", wantReason: "iam_rbac", kubectlErr: errors.New("Error from server (Forbidden): nodes is forbidden")},
		{name: "malformed", wantReason: "identity_mismatch", kubectlRaw: []byte(`{"metadata":{"name":"10.0.0.8"}}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			allocation, plan, ownership := computeClaimProviderFixture()
			provider := NewTencentProvider()
			provider.provision = func(context.Context, provisionerRequest) (provisionerResponse, error) {
				return provisionerResponse{OK: true, Status: "proven", PoolID: plan.PoolID, NodePoolID: plan.NodePoolID, InstanceID: allocation.InstanceID, NodeName: allocation.NodeName, PrivateIP: allocation.PrivateIP, InstanceType: plan.InstanceType, ProviderData: map[string]string{
					"machineName": allocation.MachineName, "zone": allocation.Zone, "chargeType": "PREPAID", "periodMonths": "1", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": allocation.Deadline, "cvmOwnershipState": "target_owned",
				}}, nil
			}
			provider.kubectl = func(context.Context, []string, []byte) ([]byte, error) { return tc.kubectlRaw, tc.kubectlErr }

			proof, err := provider.ProveComputeClaimRecovery(context.Background(), allocation, plan, ownership)

			if tc.wantReason == "" {
				wantState := "target_owned"
				if tc.name == "target labels written before taint" {
					wantState = "unallocated"
				}
				if err != nil || proof.NodeOwnershipState != wantState {
					t.Fatalf("proof=%#v err=%v", proof, err)
				}
			} else if err == nil || proof.Reason != tc.wantReason {
				t.Fatalf("proof=%#v err=%v", proof, err)
			}
		})
	}
}

func TestTencentProviderComputeClaimRecoveryProofPreservesRedactedProviderIdentityFailure(t *testing.T) {
	setProtectedResourceEnv(t)
	allocation, plan, ownership := computeClaimProviderFixture()
	provider := NewTencentProvider()
	provider.provision = func(context.Context, provisionerRequest) (provisionerResponse, error) {
		return provisionerResponse{
			OK: false, ErrorCode: "identity_mismatch", MutationCount: 0,
			FailureStage: "cvm_pre_read", ProviderErrorClass: "readback_mismatch",
			ProviderIdentityFailure: &ComputeClaimProviderIdentityFailure{
				Predicate:      "compute_claim.cvm_ownership.opl_account_id",
				ExpectedDigest: strings.Repeat("a", 64), ActualDigest: strings.Repeat("b", 64),
			},
		}, nil
	}
	provider.kubectl = func(context.Context, []string, []byte) ([]byte, error) {
		t.Fatal("failed Tencent proof must not reach Kubernetes")
		return nil, nil
	}

	proof, err := provider.ProveComputeClaimRecovery(context.Background(), allocation, plan, ownership)

	if err == nil || proof.Reason != "identity_mismatch" || proof.FailureStage != "cvm_pre_read" ||
		proof.ProviderErrorClass != "readback_mismatch" || proof.ProviderIdentityFailure == nil ||
		proof.ProviderIdentityFailure.Predicate != "compute_claim.cvm_ownership.opl_account_id" ||
		proof.ProviderIdentityFailure.ExpectedDigest != strings.Repeat("a", 64) ||
		proof.ProviderIdentityFailure.ActualDigest != strings.Repeat("b", 64) {
		t.Fatalf("proof=%#v err=%v", proof, err)
	}
}

func TestComputeClaimProviderIdentityFailureDoesNotSynthesizeUnknownDigest(t *testing.T) {
	evidence := newComputeClaimProviderIdentityFailure(
		"compute_claim.provider_response_identity",
		map[string]any{"identity": "expected"},
		map[string]any{"identity": func() {}},
	)

	if evidence != nil {
		t.Fatalf("evidence=%#v", evidence)
	}
}

func TestTencentProviderClaimComputeRecoveryConvergesExactCVMAndNodeWithStrictReadback(t *testing.T) {
	setProtectedResourceEnv(t)
	allocation, plan, ownership := computeClaimProviderFixture()
	provider := NewTencentProvider()
	cvmOwned, nodeOwned := false, false
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Action == "claim_compute_machine" {
			if cvmOwned {
				t.Fatal("target-owned CVM was mutated twice")
			}
			cvmOwned = true
			return provisionerResponse{
				OK: true, Status: "claimed", InstanceID: allocation.InstanceID,
				ProviderData: map[string]string{"cvmOwnershipState": "target_owned"}, MutationCount: 1,
				MutationEvidence: &ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1},
			}, nil
		}
		if request.Action != "compute_claim_truth" {
			t.Fatalf("action=%q", request.Action)
		}
		cvmState := "recoverable"
		if cvmOwned {
			cvmState = "target_owned"
		}
		return provisionerResponse{OK: true, Status: "proven", PoolID: plan.PoolID, NodePoolID: plan.NodePoolID, InstanceID: allocation.InstanceID, NodeName: allocation.NodeName, PrivateIP: allocation.PrivateIP, InstanceType: plan.InstanceType, ProviderData: map[string]string{
			"machineName": allocation.MachineName, "zone": allocation.Zone, "chargeType": "PREPAID", "periodMonths": "1", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": allocation.Deadline, "cvmOwnershipState": cvmState,
		}}, nil
	}
	patchCalls := 0
	provider.kubectl = func(_ context.Context, args []string, stdin []byte) ([]byte, error) {
		switch args[0] {
		case "get":
			if nodeOwned {
				return []byte(`{"metadata":{"name":"10.0.0.8","resourceVersion":"8","labels":{"medopl.cn/workload":"workspace","oplcloud.cn/resource-id":"compute-alpha","oplcloud.cn/account-id":"acct-alpha","oplcloud.cn/workspace-id":"ws-alpha"}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"ws-alpha","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`), nil
			}
			return []byte(`{"metadata":{"name":"10.0.0.8","resourceVersion":"7","labels":{}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"unallocated","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`), nil
		case "patch":
			patchCalls++
			if !slices.Equal(args, []string{"patch", "node/10.0.0.8", "--type=json", "-f", "-"}) {
				t.Fatalf("patch args=%#v", args)
			}
			var patch []map[string]any
			if json.Unmarshal(stdin, &patch) != nil || len(patch) < 7 || patch[0]["op"] != "test" || patch[0]["path"] != "/metadata/resourceVersion" || patch[0]["value"] != "7" {
				t.Fatalf("patch=%s", stdin)
			}
			nodeOwned = true
			return nil, nil
		default:
			t.Fatalf("kubectl args=%#v", args)
			return nil, nil
		}
	}

	claim, err := provider.ClaimComputeRecovery(context.Background(), allocation, plan, ownership)

	if err != nil || claim.Proof.CVMOwnershipState != "target_owned" || claim.Proof.NodeOwnershipState != "target_owned" ||
		claim.TencentMutationCount != 1 || claim.KubernetesMutationCount != 1 ||
		claim.Evidence == nil || claim.Evidence.CVM.Attempted != 1 || claim.Evidence.CVM.Confirmed != 1 || claim.Evidence.CVM.Unknown != 0 || len(claim.Evidence.CVM.Missing) != 0 ||
		claim.Evidence.Node.Attempted != 1 || claim.Evidence.Node.Confirmed != 1 || claim.Evidence.Node.Unknown != 0 || len(claim.Evidence.Node.Missing) != 0 || patchCalls != 1 {
		t.Fatalf("claim=%#v err=%v patchCalls=%d", claim, err, patchCalls)
	}

	replayed, err := provider.ClaimComputeRecovery(context.Background(), allocation, plan, ownership)
	if err != nil || replayed.TencentMutationCount != 0 || replayed.KubernetesMutationCount != 0 || patchCalls != 1 {
		t.Fatalf("replayed=%#v err=%v patchCalls=%d", replayed, err, patchCalls)
	}
}

func TestTencentProviderClaimComputeRecoveryNodeOnlyPreservesRecoverableCVM(t *testing.T) {
	setProtectedResourceEnv(t)
	allocation, plan, ownership := computeClaimProviderFixture()
	provider := NewTencentProvider()
	nodeOwned := false
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Action != "compute_claim_truth" {
			t.Fatalf("node-only reconciliation attempted Tencent mutation: action=%q", request.Action)
		}
		return provisionerResponse{
			OK: true, Status: "proven", PoolID: plan.PoolID, NodePoolID: plan.NodePoolID,
			InstanceID: allocation.InstanceID, NodeName: allocation.NodeName, PrivateIP: allocation.PrivateIP, InstanceType: plan.InstanceType,
			ProviderData: map[string]string{
				"machineName": allocation.MachineName, "zone": allocation.Zone, "chargeType": "PREPAID", "periodMonths": "1",
				"renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": allocation.Deadline, "cvmOwnershipState": "recoverable",
			},
		}, nil
	}
	patchCalls := 0
	provider.kubectl = func(_ context.Context, args []string, stdin []byte) ([]byte, error) {
		switch args[0] {
		case "get":
			if nodeOwned {
				return []byte(`{"metadata":{"name":"10.0.0.8","resourceVersion":"8","labels":{"medopl.cn/workload":"workspace","oplcloud.cn/resource-id":"compute-alpha","oplcloud.cn/account-id":"acct-alpha","oplcloud.cn/workspace-id":"ws-alpha"}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"ws-alpha","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`), nil
			}
			return []byte(`{"metadata":{"name":"10.0.0.8","resourceVersion":"7","labels":{}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"unallocated","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`), nil
		case "patch":
			patchCalls++
			if !slices.Equal(args, []string{"patch", "node/10.0.0.8", "--type=json", "-f", "-"}) || len(stdin) == 0 {
				t.Fatalf("patch args=%#v stdin=%s", args, stdin)
			}
			nodeOwned = true
			return nil, nil
		default:
			t.Fatalf("kubectl args=%#v", args)
			return nil, nil
		}
	}

	claim, err := provider.ClaimComputeRecoveryNodeOnly(context.Background(), allocation, plan, ownership)

	if err != nil || claim.Proof.CVMOwnershipState != "recoverable" || claim.Proof.NodeOwnershipState != "target_owned" ||
		claim.TencentMutationCount != 0 || claim.KubernetesMutationCount != 1 || claim.Evidence == nil ||
		!reflect.DeepEqual(claim.Evidence.CVM, ComputeClaimMutationEvidence{}) ||
		!reflect.DeepEqual(claim.Evidence.Node, ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1}) || patchCalls != 1 {
		t.Fatalf("claim=%#v err=%v patchCalls=%d", claim, err, patchCalls)
	}
}

func TestTencentProviderClaimComputeRecoveryNodeOnlyRejectsCVMOwnershipReadbackDrift(t *testing.T) {
	setProtectedResourceEnv(t)
	allocation, plan, ownership := computeClaimProviderFixture()
	provider := NewTencentProvider()
	proofCalls := 0
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Action != "compute_claim_truth" {
			t.Fatalf("node-only reconciliation attempted Tencent mutation: action=%q", request.Action)
		}
		proofCalls++
		cvmOwnershipState := "target_owned"
		if proofCalls > 1 {
			cvmOwnershipState = "recoverable"
		}
		return provisionerResponse{
			OK: true, Status: "proven", PoolID: plan.PoolID, NodePoolID: plan.NodePoolID,
			InstanceID: allocation.InstanceID, NodeName: allocation.NodeName, PrivateIP: allocation.PrivateIP, InstanceType: plan.InstanceType,
			ProviderData: map[string]string{
				"machineName": allocation.MachineName, "zone": allocation.Zone, "chargeType": "PREPAID", "periodMonths": "1",
				"renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": allocation.Deadline, "cvmOwnershipState": cvmOwnershipState,
			},
		}, nil
	}
	provider.kubectl = func(context.Context, []string, []byte) ([]byte, error) {
		return []byte(`{"metadata":{"name":"10.0.0.8","resourceVersion":"8","labels":{"medopl.cn/workload":"workspace","oplcloud.cn/resource-id":"compute-alpha","oplcloud.cn/account-id":"acct-alpha","oplcloud.cn/workspace-id":"ws-alpha"}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"ws-alpha","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`), nil
	}

	claim, err := provider.ClaimComputeRecoveryNodeOnly(context.Background(), allocation, plan, ownership)

	if err == nil || claim.Proof.Reason != "identity_mismatch" || claim.FailureStage != "claim_final_readback" ||
		claim.ProviderErrorClass != "readback_mismatch" || claim.TencentMutationCount != 0 || claim.KubernetesMutationCount != 0 || proofCalls != 2 {
		t.Fatalf("claim=%#v err=%v proofCalls=%d", claim, err, proofCalls)
	}
}

func TestTencentProviderClaimComputeRecoveryRejectsNodeConflictBeforeMutation(t *testing.T) {
	allocation, plan, ownership := computeClaimProviderFixture()
	provider := NewTencentProvider()
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Action != "compute_claim_truth" {
			t.Fatalf("node conflict must stop before Tencent mutation: action=%q", request.Action)
		}
		return provisionerResponse{OK: true, Status: "proven", PoolID: plan.PoolID, NodePoolID: plan.NodePoolID, InstanceID: allocation.InstanceID, NodeName: allocation.NodeName, PrivateIP: allocation.PrivateIP, InstanceType: plan.InstanceType, ProviderData: map[string]string{
			"machineName": allocation.MachineName, "zone": allocation.Zone, "chargeType": "PREPAID", "periodMonths": "1", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": allocation.Deadline, "cvmOwnershipState": "recoverable",
		}}, nil
	}
	provider.kubectl = func(context.Context, []string, []byte) ([]byte, error) {
		return []byte(`{"metadata":{"name":"10.0.0.8","resourceVersion":"7","labels":{"oplcloud.cn/workspace-id":"ws-other"}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"unallocated","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`), nil
	}

	claim, err := provider.ClaimComputeRecovery(context.Background(), allocation, plan, ownership)

	if err == nil || claim.Proof.Reason != "node_ownership_conflict" || claim.TencentMutationCount != 0 || claim.KubernetesMutationCount != 0 {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
}

func TestTencentProviderClaimComputeRecoveryRejectsNodeConflictAfterProofBeforeTencentMutation(t *testing.T) {
	setProtectedResourceEnv(t)
	allocation, plan, ownership := computeClaimProviderFixture()
	provider := NewTencentProvider()
	tencentMutationCalls := 0
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Action == "claim_compute_machine" {
			tencentMutationCalls++
			return provisionerResponse{
				OK: true, Status: "claimed", InstanceID: allocation.InstanceID, MutationCount: 1,
				MutationEvidence: &ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1},
				ProviderData:     map[string]string{"cvmOwnershipState": "target_owned"},
			}, nil
		}
		return provisionerResponse{
			OK: true, Status: "proven", PoolID: plan.PoolID, NodePoolID: plan.NodePoolID,
			InstanceID: allocation.InstanceID, NodeName: allocation.NodeName, PrivateIP: allocation.PrivateIP, InstanceType: plan.InstanceType,
			ProviderData: map[string]string{
				"machineName": allocation.MachineName, "zone": allocation.Zone, "chargeType": "PREPAID", "periodMonths": "1",
				"renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": allocation.Deadline, "cvmOwnershipState": "recoverable",
			},
		}, nil
	}
	getCalls, patchCalls := 0, 0
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		switch args[0] {
		case "get":
			getCalls++
			if getCalls == 1 {
				return []byte(`{"metadata":{"name":"10.0.0.8","resourceVersion":"7","labels":{}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"unallocated","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`), nil
			}
			return []byte(`{"metadata":{"name":"10.0.0.8","resourceVersion":"8","labels":{"oplcloud.cn/workspace-id":"ws-other"}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"ws-other","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`), nil
		case "patch":
			patchCalls++
			return nil, nil
		default:
			t.Fatalf("unexpected kubectl args=%#v", args)
			return nil, nil
		}
	}

	claim, err := provider.ClaimComputeRecovery(context.Background(), allocation, plan, ownership)

	if err == nil || claim.Proof.Reason != "node_ownership_conflict" || claim.TencentMutationCount != 0 || claim.KubernetesMutationCount != 0 ||
		claim.FailureStage != "node_pre_cvm_read" || claim.ProviderErrorClass != "ownership_conflict" || getCalls != 2 || tencentMutationCalls != 0 || patchCalls != 0 {
		t.Fatalf("claim=%#v err=%v getCalls=%d tencentMutationCalls=%d patchCalls=%d", claim, err, getCalls, tencentMutationCalls, patchCalls)
	}
}

func TestComputeClaimKubectlErrorClassUsesAllowlistedOwnershipConflict(t *testing.T) {
	if got := computeClaimKubectlErrorClass(errors.New("resourceVersion conflict")); got != "ownership_conflict" {
		t.Fatalf("provider error class=%q", got)
	}
}

func TestTencentProviderClaimComputeRecoveryRejectsTencentMutationCountAboveBoundBeforeNodePatch(t *testing.T) {
	setProtectedResourceEnv(t)
	allocation, plan, ownership := computeClaimProviderFixture()
	provider := NewTencentProvider()
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Action == "claim_compute_machine" {
			return provisionerResponse{
				OK: true, Status: "claimed", InstanceID: allocation.InstanceID, MutationCount: 6,
				MutationEvidence: &ComputeClaimMutationEvidence{Attempted: 6, Confirmed: 6},
				ProviderData:     map[string]string{"cvmOwnershipState": "target_owned"},
			}, nil
		}
		return provisionerResponse{
			OK: true, Status: "proven", PoolID: plan.PoolID, NodePoolID: plan.NodePoolID,
			InstanceID: allocation.InstanceID, NodeName: allocation.NodeName, PrivateIP: allocation.PrivateIP, InstanceType: plan.InstanceType,
			ProviderData: map[string]string{
				"machineName": allocation.MachineName, "zone": allocation.Zone, "chargeType": "PREPAID", "periodMonths": "1",
				"renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": allocation.Deadline, "cvmOwnershipState": "recoverable",
			},
		}, nil
	}
	patchCalls := 0
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		if args[0] == "patch" {
			patchCalls++
			return nil, nil
		}
		return []byte(`{"metadata":{"name":"10.0.0.8","resourceVersion":"7","labels":{}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"unallocated","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`), nil
	}

	claim, err := provider.ClaimComputeRecovery(context.Background(), allocation, plan, ownership)

	if err == nil || claim.TencentMutationCount != 6 || claim.KubernetesMutationCount != 0 || patchCalls != 0 {
		t.Fatalf("over-bound claim=%#v err=%v patchCalls=%d", claim, err, patchCalls)
	}
}

func TestTencentProviderClaimComputeRecoveryReadsNodeAfterPatchTimeout(t *testing.T) {
	setProtectedResourceEnv(t)
	allocation, plan, ownership := computeClaimProviderFixture()
	provider := NewTencentProvider()
	nodeOwned := false
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Action == "claim_compute_machine" {
			return provisionerResponse{
				OK: true, Status: "claimed", InstanceID: allocation.InstanceID,
				ProviderData: map[string]string{"cvmOwnershipState": "target_owned"}, MutationCount: 0,
				MutationEvidence: &ComputeClaimMutationEvidence{},
			}, nil
		}
		return provisionerResponse{OK: true, Status: "proven", PoolID: plan.PoolID, NodePoolID: plan.NodePoolID, InstanceID: allocation.InstanceID, NodeName: allocation.NodeName, PrivateIP: allocation.PrivateIP, InstanceType: plan.InstanceType, ProviderData: map[string]string{
			"machineName": allocation.MachineName, "zone": allocation.Zone, "chargeType": "PREPAID", "periodMonths": "1", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": allocation.Deadline, "cvmOwnershipState": "target_owned",
		}}, nil
	}
	patchCalls := 0
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		switch args[0] {
		case "get":
			if nodeOwned {
				return []byte(`{"metadata":{"name":"10.0.0.8","labels":{"medopl.cn/workload":"workspace","oplcloud.cn/resource-id":"compute-alpha","oplcloud.cn/account-id":"acct-alpha","oplcloud.cn/workspace-id":"ws-alpha"}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"ws-alpha","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`), nil
			}
			return []byte(`{"metadata":{"name":"10.0.0.8","resourceVersion":"7","labels":{}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"unallocated","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`), nil
		case "patch":
			patchCalls++
			nodeOwned = true
			return nil, context.DeadlineExceeded
		default:
			t.Fatalf("unexpected kubectl args=%#v", args)
			return nil, nil
		}
	}

	claim, err := provider.ClaimComputeRecovery(context.Background(), allocation, plan, ownership)

	if err != nil || claim.Proof.NodeOwnershipState != "target_owned" || claim.KubernetesMutationCount != 1 || patchCalls != 1 {
		t.Fatalf("claim=%#v err=%v patchCalls=%d", claim, err, patchCalls)
	}
}

func TestTencentProviderClaimComputeRecoveryUsesSixthNodeReadbackWithoutRepeatingPatch(t *testing.T) {
	setProtectedResourceEnv(t)
	allocation, plan, ownership := computeClaimProviderFixture()
	provider := NewTencentProvider()
	waitAttempts := []int{}
	provider.convergenceWait = func(_ context.Context, attempt int) error {
		waitAttempts = append(waitAttempts, attempt)
		return nil
	}
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Action == "claim_compute_machine" {
			return provisionerResponse{OK: true, Status: "claimed", InstanceID: allocation.InstanceID, ProviderData: map[string]string{"cvmOwnershipState": "target_owned"}, MutationEvidence: &ComputeClaimMutationEvidence{}}, nil
		}
		state := "target_owned"
		return provisionerResponse{OK: true, Status: "proven", PoolID: plan.PoolID, NodePoolID: plan.NodePoolID, InstanceID: allocation.InstanceID, NodeName: allocation.NodeName, PrivateIP: allocation.PrivateIP, InstanceType: plan.InstanceType, ProviderData: map[string]string{
			"machineName": allocation.MachineName, "zone": allocation.Zone, "chargeType": "PREPAID", "periodMonths": "1", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": allocation.Deadline, "cvmOwnershipState": state,
		}}, nil
	}
	getCalls, postPatchGetCalls, patchCalls := 0, 0, 0
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		switch args[0] {
		case "get":
			getCalls++
			if patchCalls == 0 {
				return []byte(`{"metadata":{"name":"10.0.0.8","resourceVersion":"7","labels":{}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"unallocated","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`), nil
			}
			postPatchGetCalls++
			if postPatchGetCalls < 6 {
				return []byte(`{"metadata":{"name":"10.0.0.8","resourceVersion":"7","labels":{}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"unallocated","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`), nil
			}
			return []byte(`{"metadata":{"name":"10.0.0.8","resourceVersion":"8","labels":{"medopl.cn/workload":"workspace","oplcloud.cn/resource-id":"compute-alpha","oplcloud.cn/account-id":"acct-alpha","oplcloud.cn/workspace-id":"ws-alpha"}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"ws-alpha","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`), nil
		case "patch":
			patchCalls++
			return nil, context.DeadlineExceeded
		default:
			t.Fatalf("unexpected kubectl args=%#v", args)
			return nil, nil
		}
	}

	claim, err := provider.ClaimComputeRecovery(context.Background(), allocation, plan, ownership)
	if err != nil || claim.Proof.NodeOwnershipState != "target_owned" || claim.KubernetesMutationCount != 1 || claim.Evidence == nil || claim.Evidence.Node.Attempted != 1 || claim.Evidence.Node.Confirmed != 1 ||
		getCalls != 9 || postPatchGetCalls != 7 || !slices.Equal(waitAttempts, []int{1, 2, 3, 4, 5}) || patchCalls != 1 {
		t.Fatalf("claim=%#v err=%v getCalls=%d postPatchGetCalls=%d waitAttempts=%#v patchCalls=%d", claim, err, getCalls, postPatchGetCalls, waitAttempts, patchCalls)
	}
}

func TestTencentProviderClaimComputeRecoveryFailsClosedAfterPersistentOldNodeReadback(t *testing.T) {
	setProtectedResourceEnv(t)
	allocation, plan, ownership := computeClaimProviderFixture()
	provider := NewTencentProvider()
	waitAttempts := []int{}
	provider.convergenceWait = func(_ context.Context, attempt int) error {
		waitAttempts = append(waitAttempts, attempt)
		return nil
	}
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Action == "claim_compute_machine" {
			return provisionerResponse{OK: true, Status: "claimed", InstanceID: allocation.InstanceID, ProviderData: map[string]string{"cvmOwnershipState": "target_owned"}, MutationEvidence: &ComputeClaimMutationEvidence{}}, nil
		}
		return provisionerResponse{OK: true, Status: "proven", PoolID: plan.PoolID, NodePoolID: plan.NodePoolID, InstanceID: allocation.InstanceID, NodeName: allocation.NodeName, PrivateIP: allocation.PrivateIP, InstanceType: plan.InstanceType, ProviderData: map[string]string{
			"machineName": allocation.MachineName, "zone": allocation.Zone, "chargeType": "PREPAID", "periodMonths": "1", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": allocation.Deadline, "cvmOwnershipState": "target_owned",
		}}, nil
	}
	getCalls, patchCalls := 0, 0
	old := []byte(`{"metadata":{"name":"10.0.0.8","resourceVersion":"7","labels":{}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"unallocated","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`)
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		if args[0] == "get" {
			getCalls++
			return old, nil
		}
		if args[0] == "patch" {
			patchCalls++
			return nil, nil
		}
		t.Fatalf("unexpected kubectl args=%#v", args)
		return nil, nil
	}

	claim, err := provider.ClaimComputeRecovery(context.Background(), allocation, plan, ownership)
	if err == nil || claim.Proof.NodeOwnershipState == "target_owned" || claim.KubernetesMutationCount != 1 || claim.Evidence == nil || claim.Evidence.Node.Attempted != 1 || claim.Evidence.Node.Confirmed != 0 ||
		getCalls != 8 || !slices.Equal(waitAttempts, []int{1, 2, 3, 4, 5}) || patchCalls != 1 {
		t.Fatalf("claim=%#v err=%v getCalls=%d waitAttempts=%#v patchCalls=%d", claim, err, getCalls, waitAttempts, patchCalls)
	}
}

func TestTencentProviderClaimComputeRecoveryFailsClosedAfterUnreadableNodeReadback(t *testing.T) {
	setProtectedResourceEnv(t)
	allocation, plan, ownership := computeClaimProviderFixture()
	provider := NewTencentProvider()
	waitAttempts := []int{}
	provider.convergenceWait = func(_ context.Context, attempt int) error {
		waitAttempts = append(waitAttempts, attempt)
		return nil
	}
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Action == "claim_compute_machine" {
			return provisionerResponse{OK: true, Status: "claimed", InstanceID: allocation.InstanceID, ProviderData: map[string]string{"cvmOwnershipState": "target_owned"}, MutationEvidence: &ComputeClaimMutationEvidence{}}, nil
		}
		return provisionerResponse{OK: true, Status: "proven", PoolID: plan.PoolID, NodePoolID: plan.NodePoolID, InstanceID: allocation.InstanceID, NodeName: allocation.NodeName, PrivateIP: allocation.PrivateIP, InstanceType: plan.InstanceType, ProviderData: map[string]string{
			"machineName": allocation.MachineName, "zone": allocation.Zone, "chargeType": "PREPAID", "periodMonths": "1", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": allocation.Deadline, "cvmOwnershipState": "target_owned",
		}}, nil
	}
	getCalls, patchCalls := 0, 0
	old := []byte(`{"metadata":{"name":"10.0.0.8","resourceVersion":"7","labels":{}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"unallocated","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`)
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		if args[0] == "get" {
			getCalls++
			if patchCalls == 0 {
				return old, nil
			}
			return nil, errors.New("node readback unavailable")
		}
		if args[0] == "patch" {
			patchCalls++
			return nil, context.DeadlineExceeded
		}
		t.Fatalf("unexpected kubectl args=%#v", args)
		return nil, nil
	}

	claim, err := provider.ClaimComputeRecovery(context.Background(), allocation, plan, ownership)
	if err == nil || claim.KubernetesMutationCount != 1 || claim.Evidence == nil || claim.Evidence.Node.Attempted != 1 || claim.Evidence.Node.Confirmed != 0 || claim.Evidence.Node.Unknown != 1 ||
		getCalls != 8 || !slices.Equal(waitAttempts, []int{1, 2, 3, 4, 5}) || patchCalls != 1 {
		t.Fatalf("claim=%#v err=%v getCalls=%d waitAttempts=%#v patchCalls=%d", claim, err, getCalls, waitAttempts, patchCalls)
	}
}

func TestTencentProviderClaimComputeRecoveryDoesNotPatchNodeWhenCVMEvidenceIsUnconfirmed(t *testing.T) {
	setProtectedResourceEnv(t)
	allocation, plan, ownership := computeClaimProviderFixture()
	provider := NewTencentProvider()
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Action == "claim_compute_machine" {
			return provisionerResponse{OK: true, Status: "claimed", InstanceID: allocation.InstanceID, ProviderData: map[string]string{"cvmOwnershipState": "target_owned"}, MutationCount: 1, MutationEvidence: &ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"instance_name"}}}, nil
		}
		return provisionerResponse{OK: true, Status: "proven", PoolID: plan.PoolID, NodePoolID: plan.NodePoolID, InstanceID: allocation.InstanceID, NodeName: allocation.NodeName, PrivateIP: allocation.PrivateIP, InstanceType: plan.InstanceType, ProviderData: map[string]string{
			"machineName": allocation.MachineName, "zone": allocation.Zone, "chargeType": "PREPAID", "periodMonths": "1", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": allocation.Deadline, "cvmOwnershipState": "recoverable",
		}}, nil
	}
	patchCalls := 0
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		if args[0] == "get" {
			return []byte(`{"metadata":{"name":"10.0.0.8","labels":{}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"unallocated","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`), nil
		}
		if args[0] == "patch" {
			patchCalls++
		}
		return nil, nil
	}

	claim, err := provider.ClaimComputeRecovery(context.Background(), allocation, plan, ownership)
	if err == nil || claim.KubernetesMutationCount != 0 || patchCalls != 0 || claim.Proof.NodeOwnershipState == "target_owned" {
		t.Fatalf("unconfirmed CVM must stop before node patch: claim=%#v err=%v patchCalls=%d", claim, err, patchCalls)
	}
}

func TestTencentProviderClaimComputeRecoveryUsesConsistentConservativeEvidenceWhenProvisionerTransportIsLost(t *testing.T) {
	setProtectedResourceEnv(t)
	allocation, plan, ownership := computeClaimProviderFixture()
	provider := NewTencentProvider()
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Action == "claim_compute_machine" {
			return provisionerResponse{}, errors.New("provisioner transport unavailable")
		}
		return provisionerResponse{
			OK: true, Status: "proven", PoolID: plan.PoolID, NodePoolID: plan.NodePoolID,
			InstanceID: allocation.InstanceID, NodeName: allocation.NodeName, PrivateIP: allocation.PrivateIP, InstanceType: plan.InstanceType,
			ProviderData: map[string]string{
				"machineName": allocation.MachineName, "zone": allocation.Zone, "chargeType": "PREPAID", "periodMonths": "1",
				"renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": allocation.Deadline, "cvmOwnershipState": "recoverable",
			},
		}, nil
	}
	patchCalls := 0
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		if args[0] == "patch" {
			patchCalls++
		}
		return []byte(`{"metadata":{"name":"10.0.0.8","resourceVersion":"7","labels":{}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"unallocated","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`), nil
	}

	claim, err := provider.ClaimComputeRecovery(context.Background(), allocation, plan, ownership)

	if err == nil || claim.TencentMutationCount != 5 || claim.KubernetesMutationCount != 0 || patchCalls != 0 || claim.Evidence == nil ||
		claim.Evidence.CVM.Attempted != 5 || claim.Evidence.CVM.Confirmed != 0 || claim.Evidence.CVM.Unknown != 5 || len(claim.Evidence.CVM.Missing) != 5 {
		t.Fatalf("transport evidence must be bounded and internally consistent: claim=%#v err=%v patchCalls=%d", claim, err, patchCalls)
	}
}

func TestTencentProviderComputeClaimRecoveryProofRejectsProtectedSystemIdentityBeforeRead(t *testing.T) {
	setProtectedResourceEnv(t)
	for _, test := range []struct {
		name   string
		mutate func(*ComputeAllocation, *ComputeAllocationPreparation, *MachineOwnership)
	}{
		{name: "pool", mutate: func(allocation *ComputeAllocation, plan *ComputeAllocationPreparation, ownership *MachineOwnership) {
			allocation.NodePoolID, plan.NodePoolID, ownership.NodePoolID = "np-system", "np-system", "np-system"
		}},
		{name: "machine", mutate: func(allocation *ComputeAllocation, _ *ComputeAllocationPreparation, ownership *MachineOwnership) {
			allocation.MachineName, ownership.MachineID = "machine-system", "machine-system"
		}},
		{name: "node", mutate: func(allocation *ComputeAllocation, _ *ComputeAllocationPreparation, ownership *MachineOwnership) {
			allocation.NodeName = os.Getenv("OPL_SYSTEM_COMPUTE_NODE_NAME")
			ownership.NodeName = allocation.NodeName
		}},
		{name: "CVM", mutate: func(allocation *ComputeAllocation, _ *ComputeAllocationPreparation, ownership *MachineOwnership) {
			allocation.InstanceID, allocation.CVMInstanceID, ownership.InstanceID = "ins-system", "ins-system", "ins-system"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			allocation, plan, ownership := computeClaimProviderFixture()
			test.mutate(&allocation, &plan, &ownership)
			provider := NewTencentProvider()
			provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
				t.Fatalf("protected target reached Tencent: %#v", request)
				return provisionerResponse{}, nil
			}
			provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
				t.Fatalf("protected target reached Kubernetes: %#v", args)
				return nil, nil
			}

			proof, err := provider.ProveComputeClaimRecovery(context.Background(), allocation, plan, ownership)
			if err == nil || proof.Reason != "identity_mismatch" {
				t.Fatalf("proof=%#v err=%v", proof, err)
			}
		})
	}
}

func TestSyncComputeAllocationRestoresClaimedMachineSelector(t *testing.T) {
	provider := NewTencentProvider()
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Pool.InstanceType != "SA5.MEDIUM4" || request.Pool.CPU != 2 || request.Pool.MemoryGB != 4 {
			t.Fatalf("sync request missing exact package SKU: %#v", request.Pool)
		}
		return provisionerResponse{
			OK: true, Status: "running", InstanceID: "np-basic-2", NodeName: "10.0.0.8",
			InstanceType: "SA5.MEDIUM4", ProviderData: map[string]string{"machineName": "np-basic-2", "instanceType": "SA5.MEDIUM4", "cpu": "2", "memoryGb": "4"},
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

func TestTencentTagComputeMachineConvergesProviderAndNodeWithStrictReadback(t *testing.T) {
	setProtectedResourceEnv(t)
	provider := NewTencentProvider()
	var events []string
	nodeOwned := false
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		events = append(events, "provider")
		if request.Action != "tag_compute_machine" || request.Pool.NodePoolID != "np-basic" || request.Allocation.InstanceID != "ins-alpha" || request.Allocation.PrivateIP != "10.0.0.8" || request.Tags["opl_resource_id"] != "compute-alpha" {
			t.Fatalf("provider request = %#v", request)
		}
		return provisionerResponse{OK: true, Status: "tagged", MutationEvidence: &ComputeClaimMutationEvidence{}}, nil
	}
	provider.kubectl = func(_ context.Context, args []string, stdin []byte) ([]byte, error) {
		switch args[0] {
		case "get":
			events = append(events, "get")
			if nodeOwned {
				return []byte(`{"metadata":{"name":"node-alpha","resourceVersion":"8","labels":{"medopl.cn/workload":"workspace","oplcloud.cn/resource-id":"compute-alpha","oplcloud.cn/account-id":"acct-alpha","oplcloud.cn/workspace-id":"ws-alpha"}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"ws-alpha","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`), nil
			}
			return []byte(`{"metadata":{"name":"node-alpha","resourceVersion":"7","labels":{}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"unallocated","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`), nil
		case "patch":
			events = append(events, "patch")
			if !slices.Equal(args, []string{"patch", "node/node-alpha", "--type=json", "-f", "-"}) {
				t.Fatalf("kubectl patch args = %#v", args)
			}
			var patch []map[string]any
			if json.Unmarshal(stdin, &patch) != nil || patch[0]["path"] != "/metadata/resourceVersion" || patch[0]["value"] != "7" {
				t.Fatalf("kubectl patch = %s", stdin)
			}
			nodeOwned = true
		default:
			t.Fatalf("unexpected kubectl args = %#v", args)
		}
		return nil, nil
	}

	err := provider.TagComputeMachine(context.Background(), ProviderMachine{MachineID: "machine-alpha", InstanceID: "ins-alpha", NodeName: "node-alpha", PrivateIP: "10.0.0.8"}, MachineOwnership{ResourceID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", NodePoolID: "np-basic"})
	if err != nil || !slices.Equal(events, []string{"provider", "get", "patch", "get"}) {
		t.Fatalf("tag machine err=%v events=%#v", err, events)
	}
}

func TestTencentTagComputeMachineReadsNodeAfterPatchTimeout(t *testing.T) {
	setProtectedResourceEnv(t)
	provider := NewTencentProvider()
	provider.provision = func(context.Context, provisionerRequest) (provisionerResponse, error) {
		return provisionerResponse{OK: true, Status: "tagged", MutationEvidence: &ComputeClaimMutationEvidence{}}, nil
	}
	nodeOwned := false
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		switch args[0] {
		case "get":
			if nodeOwned {
				return []byte(`{"metadata":{"name":"node-alpha","resourceVersion":"8","labels":{"medopl.cn/workload":"workspace","oplcloud.cn/resource-id":"compute-alpha","oplcloud.cn/account-id":"acct-alpha","oplcloud.cn/workspace-id":"ws-alpha"}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"ws-alpha","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`), nil
			}
			return []byte(`{"metadata":{"name":"node-alpha","resourceVersion":"7","labels":{}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"unallocated","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`), nil
		case "patch":
			nodeOwned = true
			return nil, context.DeadlineExceeded
		default:
			t.Fatalf("unexpected kubectl args = %#v", args)
			return nil, nil
		}
	}

	err := provider.TagComputeMachine(context.Background(), ProviderMachine{MachineID: "machine-alpha", InstanceID: "ins-alpha", NodeName: "node-alpha", PrivateIP: "10.0.0.8"}, MachineOwnership{ResourceID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", NodePoolID: "np-basic"})
	if err != nil {
		t.Fatalf("tag machine after timeout: %v", err)
	}
}

func TestTencentTagComputeMachineRejectsProtectedSystemIdentityBeforeMutation(t *testing.T) {
	setProtectedResourceEnv(t)
	for _, test := range []struct {
		name      string
		machine   ProviderMachine
		ownership MachineOwnership
	}{
		{name: "pool", machine: ProviderMachine{MachineID: "machine-alpha", InstanceID: "ins-alpha", NodeName: "node-alpha"}, ownership: MachineOwnership{PackageID: "basic", NodePoolID: "np-system"}},
		{name: "machine", machine: ProviderMachine{MachineID: "machine-system", InstanceID: "ins-alpha", NodeName: "node-alpha"}, ownership: MachineOwnership{PackageID: "basic", NodePoolID: "np-basic"}},
		{name: "node", machine: ProviderMachine{MachineID: "machine-alpha", InstanceID: "ins-alpha", NodeName: "10.66.0.42"}, ownership: MachineOwnership{PackageID: "basic", NodePoolID: "np-basic"}},
		{name: "CVM", machine: ProviderMachine{MachineID: "machine-alpha", InstanceID: "ins-system", NodeName: "node-alpha"}, ownership: MachineOwnership{PackageID: "basic", NodePoolID: "np-basic"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := NewTencentProvider()
			provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
				t.Fatalf("protected target reached Tencent: %#v", request)
				return provisionerResponse{}, nil
			}
			provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
				t.Fatalf("protected target reached Kubernetes: %#v", args)
				return nil, nil
			}
			test.ownership.ResourceID, test.ownership.AccountID, test.ownership.WorkspaceID = "compute-alpha", "acct-alpha", "ws-alpha"
			if err := provider.TagComputeMachine(context.Background(), test.machine, test.ownership); err == nil || err.Error() != "protected_system_resource" {
				t.Fatalf("protected target error = %v", err)
			}
		})
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
	input := WorkspaceRuntimeInput{WorkspaceID: "ws-alpha", ComputeID: compute.ID, VolumeID: storage.ID, AttachmentID: "att-alpha", AttachmentOperationID: "workspace-launch-alpha:attachment", RuntimeOperationID: "workspace-launch-alpha:workspace:runtime", GatewaySecretRef: "opl-gateway-acct-alpha"}
	runtimeID := "rt_" + stableSuffix(input.WorkspaceID, input.RuntimeOperationID)[:18]
	if err := json.Unmarshal(workspaceManifest(input, "Alpha", "token", runtimeID, "opl-compute-alpha", compute, storage, tags), &manifest); err != nil {
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
	tolerations := podSpec["tolerations"].([]any)
	if len(tolerations) != 2 {
		t.Fatalf("workspace pod must carry only ENI and exact Workspace tolerations: %#v", tolerations)
	}
	eniToleration := tolerations[0].(map[string]any)
	if eniToleration["key"] != "tke.cloud.tencent.com/eni-ip-unavailable" || eniToleration["effect"] != "NoSchedule" {
		t.Fatalf("workspace pod must tolerate TKE ENI readiness taint: %#v", eniToleration)
	}
	workspaceToleration := tolerations[1].(map[string]any)
	if workspaceToleration["key"] != "oplcloud.cn/workspace-id" || workspaceToleration["operator"] != "Equal" || workspaceToleration["value"] != "ws-alpha" || workspaceToleration["effect"] != "NoSchedule" {
		t.Fatalf("workspace pod must tolerate only its exact Workspace node taint: %#v", workspaceToleration)
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

func TestWorkspaceManifestUsesLaunchPinnedImageWhenFabricEnvironmentDrifts(t *testing.T) {
	pinnedImage := "uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app@sha256:" + strings.Repeat("a", 64)
	t.Setenv("OPL_WORKSPACE_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app@sha256:"+strings.Repeat("b", 64))
	t.Setenv("OPL_AIONUI_ADMIN_PASSWORD_SEED", "workspace-secret-2026-very-long")
	compute := ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", NodeSelector: map[string]any{"kubernetes.io/hostname": "node-alpha"}}
	storage := StorageVolume{ID: "storage-alpha", WorkspaceID: "ws-alpha", ProviderResourceID: "disk-storage-alpha", ProviderData: map[string]string{"pvcName": "opl-storage-alpha-data"}}
	input := WorkspaceRuntimeInput{
		WorkspaceID: "ws-alpha", ComputeID: compute.ID, VolumeID: storage.ID,
		AttachmentID: "att-alpha", AttachmentOperationID: "workspace-launch-alpha:attachment",
		RuntimeOperationID: "workspace-launch-alpha:workspace:runtime", ImageID: pinnedImage,
		GatewaySecretRef: "opl-gateway-ws-alpha",
	}
	runtimeID := "rt_" + stableSuffix(input.WorkspaceID, input.RuntimeOperationID)[:18]
	var manifest map[string]any
	if err := json.Unmarshal(workspaceManifest(input, "Alpha", "token", runtimeID, "opl-compute-alpha", compute, storage, nil), &manifest); err != nil {
		t.Fatal(err)
	}
	for _, raw := range manifest["items"].([]any) {
		resource := raw.(map[string]any)
		if resource["kind"] != "Deployment" {
			continue
		}
		if image := stringValue(firstContainerField(resource, "image")); image != pinnedImage {
			t.Fatalf("workspace image=%q, want launch-pinned %q", image, pinnedImage)
		}
		return
	}
	t.Fatal("workspace Deployment missing")
}

func TestWorkspaceManifestBindsAttachmentAndRuntimeIdentity(t *testing.T) {
	t.Setenv("OPL_WORKSPACE_IMAGE", "registry.example/one-person-lab-app@sha256:"+strings.Repeat("a", 64))
	t.Setenv("OPL_AIONUI_ADMIN_PASSWORD_SEED", "workspace-secret-2026-very-long")
	compute := ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", NodeSelector: map[string]any{"kubernetes.io/hostname": "node-alpha"}}
	storage := StorageVolume{ID: "storage-alpha", WorkspaceID: "ws-alpha", ProviderResourceID: "disk-storage-alpha", ProviderData: map[string]string{"pvcName": "opl-storage-alpha-data"}}
	input := WorkspaceRuntimeInput{
		WorkspaceID: "ws-alpha", ComputeID: compute.ID, VolumeID: storage.ID,
		AttachmentID: "att-alpha", AttachmentOperationID: "workspace-launch-alpha:attachment",
		RuntimeOperationID: "workspace-launch-alpha:workspace:runtime", GatewaySecretRef: "opl-gateway-ws-alpha",
	}
	runtimeID := "rt_" + stableSuffix(input.WorkspaceID, input.RuntimeOperationID)[:18]
	var manifest map[string]any
	if err := json.Unmarshal(workspaceManifest(input, "Alpha", "token", runtimeID, "opl-compute-alpha", compute, storage, map[string]string{
		"opl_account_id": compute.AccountID, "opl_workspace_id": input.WorkspaceID, "opl_resource_id": runtimeID, "opl_operation_id": input.RuntimeOperationID,
	}), &manifest); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"oplcloud.cn/account-id": compute.AccountID, "oplcloud.cn/workspace-id": input.WorkspaceID,
		"oplcloud.cn/compute-allocation-id": compute.ID, "oplcloud.cn/storage-id": storage.ID,
		"oplcloud.cn/attachment-id": input.AttachmentID, "oplcloud.cn/attachment-operation-id": input.AttachmentOperationID,
		"oplcloud.cn/runtime-id": runtimeID, "oplcloud.cn/runtime-operation-id": input.RuntimeOperationID,
	}
	for _, raw := range manifest["items"].([]any) {
		resource := raw.(map[string]any)
		for key, value := range want {
			if nested(resource, "metadata", "labels", key) != value {
				t.Fatalf("%s missing %s=%s: %#v", resource["kind"], key, value, nested(resource, "metadata", "labels"))
			}
		}
		if resource["kind"] == "Deployment" {
			for key, value := range want {
				if nested(resource, "spec", "template", "metadata", "labels", key) != value {
					t.Fatalf("pod template missing %s=%s: %#v", key, value, nested(resource, "spec", "template", "metadata", "labels"))
				}
			}
		}
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
		input := WorkspaceRuntimeInput{WorkspaceID: "ws-alpha", ComputeID: compute.ID, VolumeID: storage.ID, AttachmentID: "att-alpha", AttachmentOperationID: "workspace-launch-alpha:attachment", RuntimeOperationID: "workspace-launch-alpha:workspace:runtime", GatewaySecretRef: "opl-gateway-acct-alpha"}
		runtimeID := "rt_" + stableSuffix(input.WorkspaceID, input.RuntimeOperationID)[:18]
		raw := workspaceManifest(input, "Alpha", seed, runtimeID, "opl-compute-alpha", compute, storage, tags)
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
	input := WorkspaceRuntimeInput{WorkspaceID: "ws-alpha", ComputeID: compute.ID, VolumeID: storage.ID, AttachmentID: "att-alpha", AttachmentOperationID: "workspace-launch-alpha:attachment", RuntimeOperationID: "workspace-launch-alpha:workspace:runtime"}
	runtimeID := "rt_" + stableSuffix(input.WorkspaceID, input.RuntimeOperationID)[:18]
	if err := json.Unmarshal(workspaceManifest(input, "Alpha", "token", runtimeID, "opl-compute-alpha", compute, storage, nil), &manifest); err != nil {
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
	workspaceImage := workspaceImageRepository + "@sha256:" + strings.Repeat("a", 64)
	provider := NewTencentProvider()
	var calls [][]string
	var deployment map[string]any
	var service map[string]any
	var networkPolicy map[string]any
	var secret map[string]any
	provider.kubectl = func(_ context.Context, args []string, stdin []byte) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if slices.Equal(args, []string{"apply", "-f", "-"}) {
			var manifest map[string]any
			if err := json.Unmarshal(stdin, &manifest); err != nil {
				t.Fatalf("decode applied runtime manifest: %v", err)
			}
			for _, raw := range manifest["items"].([]any) {
				resource := raw.(map[string]any)
				switch resource["kind"] {
				case "Deployment":
					deployment = resource
				case "Service":
					service = resource
				case "NetworkPolicy":
					networkPolicy = resource
				case "Secret":
					secret = resource
				}
			}
			return nil, nil
		}
		if slices.Equal(args, []string{"get", "deployment,service,networkpolicy", "-l", "oplcloud.cn/workspace-id=ws-alpha", "-o", "json"}) {
			return mustJSON(map[string]any{"kind": "List", "items": []any{deployment, service, networkPolicy}}), nil
		}
		if slices.Equal(args, []string{"get", "deployment/opl-compute-alpha", "pvc/opl-storage-alpha-data", "service/opl-compute-alpha", "ingress/opl-cloud", "endpoints/opl-compute-alpha", "secret/opl-compute-alpha-env", "--ignore-not-found", "-o", "json"}) {
			return mustJSON(map[string]any{"kind": "List", "items": []any{
				deployment,
				map[string]any{"kind": "PersistentVolumeClaim", "metadata": map[string]any{"name": "opl-storage-alpha-data"}, "status": map[string]any{"phase": "Pending"}},
				service,
				map[string]any{"kind": "Ingress", "metadata": map[string]any{"name": "opl-cloud"}},
				map[string]any{"kind": "Endpoints", "metadata": map[string]any{"name": "opl-compute-alpha"}},
				secret,
			}}), nil
		}
		if slices.Equal(args, []string{"get", "networkpolicy", "-o", "json"}) {
			return mustJSON(map[string]any{"kind": "List", "items": []any{networkPolicy}}), nil
		}
		if slices.Equal(args, []string{"get", "pod", "-l", "oplcloud.cn/workspace-id=ws-alpha", "-o", "json"}) {
			return mustJSON(map[string]any{"kind": "List", "items": []any{}}), nil
		}
		t.Fatalf("unexpected kubectl args: %#v", args)
		return nil, nil
	}
	input := WorkspaceRuntimeInput{
		WorkspaceID: "ws-alpha", ComputeID: "compute-alpha", VolumeID: "storage-alpha",
		AttachmentID: "att-alpha", AttachmentOperationID: "workspace-launch-alpha:attachment",
		RuntimeOperationID: "runtime-unready", ImageID: workspaceImage, GatewaySecretRef: "opl-gateway-acct-alpha", IdempotencyKey: "runtime-unready",
	}
	runtime, err := provider.CreateWorkspaceRuntime(context.Background(), input, ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", ServiceName: "opl-compute-alpha"}, StorageVolume{ID: "storage-alpha", ProviderResourceID: "pvc/opl-storage-alpha-data"})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	replayed, replayErr := provider.CreateWorkspaceRuntime(context.Background(), input, ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", ServiceName: "opl-compute-alpha"}, StorageVolume{ID: "storage-alpha", ProviderResourceID: "pvc/opl-storage-alpha-data"})
	if runtime.Ready || runtime.Status != "unready" || replayErr != nil || replayed.ID != runtime.ID || replayed.Status != "unready" || len(calls) != 10 {
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
	workspaceImage := workspaceImageRepository + "@sha256:" + strings.Repeat("a", 64)
	t.Setenv("OPL_WORKSPACE_IMAGE", workspaceImage)
	provider := NewTencentProvider()
	runtimeSelector := map[string]any{"app.kubernetes.io/name": "opl-compute-allocation", "app.kubernetes.io/instance": "opl-compute-alpha", "oplcloud.cn/compute-allocation-id": "compute-alpha"}
	deployment := map[string]any{
		"kind": "Deployment",
		"metadata": map[string]any{
			"name":       "opl-compute-alpha",
			"generation": 2,
			"labels":     map[string]any{"app.kubernetes.io/name": "opl-compute-allocation", "app.kubernetes.io/instance": "opl-compute-alpha", "oplcloud.cn/compute-allocation-id": "compute-alpha", "oplcloud.cn/workspace-id": "ws-alpha", "oplcloud.cn/runtime-id": "rt-alpha", "oplcloud.cn/runtime-operation-id": "workspace-launch-alpha:workspace:runtime"},
			"annotations": map[string]any{
				"opl_account_id":   "acct-alpha",
				"opl_workspace_id": "ws-alpha",
				"opl_resource_id":  "rt-alpha",
				"opl_operation_id": "workspace-launch-alpha:workspace:runtime",
			},
		},
		"spec": map[string]any{"replicas": 1, "selector": map[string]any{"matchLabels": runtimeSelector}, "template": map[string]any{"metadata": map[string]any{"labels": runtimeSelector, "annotations": map[string]any{"opl.medopl.cn/credential-revision": "revision-alpha"}}, "spec": map[string]any{
			"automountServiceAccountToken": false, "dnsPolicy": "ClusterFirst", "securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 10001, "runAsGroup": 10001, "fsGroup": 10001, "seccompProfile": map[string]any{"type": "RuntimeDefault"}},
			"containers": []any{map[string]any{"name": "workspace", "image": workspaceImage, "securityContext": map[string]any{"allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []any{"ALL"}}}, "volumeMounts": workspaceDataMounts()}},
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
			"containers": []any{map[string]any{"name": "workspace", "image": workspaceImage, "securityContext": map[string]any{"allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []any{"ALL"}}}, "volumeMounts": workspaceDataMounts()}},
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
			return mustJSON(map[string]any{"kind": "List", "items": []any{deployment, service, networkPolicy}}), nil
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

func TestWorkspaceRuntimeStatusFailsClosedOnAmbiguousOrUnreadableResources(t *testing.T) {
	workspaceID := "ws-alpha"
	labels := map[string]any{"oplcloud.cn/workspace-id": workspaceID}
	deployment := map[string]any{
		"kind": "Deployment", "metadata": map[string]any{"name": "opl-compute-alpha", "labels": labels},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"volumes": []any{map[string]any{"persistentVolumeClaim": map[string]any{"claimName": "opl-storage-alpha-data"}}}}}},
	}
	service := map[string]any{"kind": "Service", "metadata": map[string]any{"name": "opl-compute-alpha", "labels": labels}}
	policy := map[string]any{"kind": "NetworkPolicy", "metadata": map[string]any{"name": "opl-compute-alpha", "labels": labels}}

	for _, tc := range []struct {
		name      string
		discovery func() ([]byte, error)
		want      string
	}{
		{
			name: "multiple deployment candidates",
			discovery: func() ([]byte, error) {
				duplicate := cloneJSONMap(deployment)
				duplicate["metadata"].(map[string]any)["name"] = "opl-compute-alpha-duplicate"
				return mustJSON(map[string]any{"kind": "List", "items": []any{deployment, duplicate, service, policy}}), nil
			},
			want: "workspace_runtime_status_ownership_conflict",
		},
		{
			name: "kubernetes forbidden",
			discovery: func() ([]byte, error) {
				return nil, errors.New("Error from server (Forbidden): deployments is forbidden")
			},
			want: "workspace_runtime_status_iam_rbac",
		},
		{
			name: "malformed kubernetes response",
			discovery: func() ([]byte, error) {
				return []byte(`{"kind":"List","items":`), nil
			},
			want: "workspace_runtime_status_provider_error",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := NewTencentProvider()
			provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
				if len(args) >= 2 && args[0] == "get" && args[1] == "deployment,service,networkpolicy" {
					return tc.discovery()
				}
				return mustJSON(map[string]any{"kind": "List", "items": []any{}}), nil
			}

			status, err := provider.WorkspaceRuntimeStatus(context.Background(), workspaceID)

			if err == nil || err.Error() != tc.want || status.Ready {
				t.Fatalf("runtime status=%#v err=%v want=%s", status, err, tc.want)
			}
			if strings.Contains(strings.ToLower(err.Error()), "forbidden") || strings.Contains(strings.ToLower(err.Error()), "deployments is forbidden") {
				t.Fatalf("raw kubernetes error leaked: %v", err)
			}
		})
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

func TestExecuteKubectlNodePatchRBACUsesConfiguredKubeconfig(t *testing.T) {
	binDir := t.TempDir()
	kubectl := filepath.Join(binDir, "kubectl")
	if err := os.WriteFile(kubectl, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o700); err != nil {
		t.Fatalf("write fake kubectl: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TENCENT_DEPLOY_KUBECONFIG_REF", "/run/secrets/tencent-kubeconfig")
	t.Setenv("OPL_K8S_NAMESPACE", "opl-cloud")

	raw, err := executeKubectl(context.Background(), []string{"auth", "can-i", "patch", "nodes"}, nil)

	if err != nil {
		t.Fatalf("execute kubectl: %v", err)
	}
	if got, want := strings.Fields(string(raw)), []string{"--kubeconfig", "/run/secrets/tencent-kubeconfig", "--namespace", "opl-cloud", "auth", "can-i", "patch", "nodes"}; !slices.Equal(got, want) {
		t.Fatalf("kubectl args=%#v want=%#v", got, want)
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
			labels := map[string]any{"oplcloud.cn/workspace-id": "workspace-alpha"}
			return mustJSON(map[string]any{"kind": "List", "items": []any{
				map[string]any{
					"kind": "Deployment", "metadata": map[string]any{"name": "opl-workspace-alpha", "labels": labels},
					"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"volumes": []any{map[string]any{"name": "workspace-data", "persistentVolumeClaim": map[string]any{"claimName": "pvc-alpha"}}}}}},
				},
				map[string]any{"kind": "Service", "metadata": map[string]any{"name": "opl-workspace-alpha", "labels": labels}},
				map[string]any{"kind": "NetworkPolicy", "metadata": map[string]any{"name": "opl-workspace-alpha", "labels": labels}},
			}}), nil
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
			labels := map[string]any{"oplcloud.cn/workspace-id": "workspace-alpha"}
			return mustJSON(map[string]any{"kind": "List", "items": []any{
				map[string]any{
					"kind": "Deployment", "metadata": map[string]any{"name": "opl-workspace-alpha", "labels": labels},
					"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"volumes": []any{map[string]any{"persistentVolumeClaim": map[string]any{"claimName": "pvc-alpha"}}}}}},
				},
				map[string]any{"kind": "Service", "metadata": map[string]any{"name": "opl-workspace-alpha", "labels": labels}},
				map[string]any{"kind": "NetworkPolicy", "metadata": map[string]any{"name": "opl-workspace-alpha", "labels": labels}},
			}}), nil
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
			labels := map[string]any{"oplcloud.cn/workspace-id": "workspace-alpha"}
			return mustJSON(map[string]any{"kind": "List", "items": []any{
				map[string]any{
					"kind": "Deployment", "metadata": map[string]any{"name": "opl-workspace-alpha", "labels": labels},
					"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"volumes": []any{map[string]any{"persistentVolumeClaim": map[string]any{"claimName": "pvc-alpha"}}}}}},
				},
				map[string]any{"kind": "Service", "metadata": map[string]any{"name": "opl-workspace-alpha", "labels": labels}},
				map[string]any{"kind": "NetworkPolicy", "metadata": map[string]any{"name": "opl-workspace-alpha", "labels": labels}},
			}}), nil
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
			labels := map[string]any{"oplcloud.cn/workspace-id": "workspace-alpha"}
			return mustJSON(map[string]any{"kind": "List", "items": []any{
				map[string]any{
					"kind": "Deployment", "metadata": map[string]any{"name": "opl-workspace-alpha", "labels": labels},
					"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"volumes": []any{map[string]any{"persistentVolumeClaim": map[string]any{"claimName": "pvc-alpha"}}}}}},
				},
				map[string]any{"kind": "Service", "metadata": map[string]any{"name": "opl-workspace-alpha", "labels": labels}},
				map[string]any{"kind": "NetworkPolicy", "metadata": map[string]any{"name": "opl-workspace-alpha", "labels": labels}},
			}}), nil
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

func TestTencentProviderStagedStorageSeparatesCBSCreateAndStaticBinding(t *testing.T) {
	provider := NewTencentProvider()
	var actions []string
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		actions = append(actions, request.Action)
		if request.Action != "create_storage_volume" && request.Action != "sync_storage_volume" {
			return provisionerResponse{}, fmt.Errorf("unexpected staged action %q", request.Action)
		}
		return provisionerResponse{
			OK: true, StorageVolumeID: "disk-staged-alpha", CBSStatus: "UNATTACHED", Status: "provider_ready", ProviderRequestID: "req-staged-cbs",
			ProviderData: map[string]string{"diskType": "CLOUD_BSSD", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": "2026-08-16T00:00:00Z", "zone": "ap-guangzhou-3", "sizeGb": "10", "diskChargeType": "PREPAID"},
		}, nil
	}
	volume := StorageVolume{
		ID: "storage-staged-alpha", OperationID: "launch-staged:storage", AccountID: "acct-staged", WorkspaceID: "workspace-staged", Status: "provider_ready",
		Provider: "tencent-tke", ProviderResourceID: "disk-staged-alpha", SizeGB: 10, DiskType: "CLOUD_BSSD", Zone: "ap-guangzhou-3",
		RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2026-08-16T00:00:00Z", CostTags: oplCostTags("acct-staged", "workspace-staged", "storage-staged-alpha", "launch-staged:storage"),
		ProviderData: map[string]string{"pvName": "opl-storage-staged-alpha-pv", "pvcName": "opl-storage-staged-alpha-data"},
	}
	input := StorageVolumeInput{
		ID: volume.ID, AccountID: volume.AccountID, WorkspaceID: volume.WorkspaceID, ComputeID: "compute-staged-alpha", Zone: volume.Zone, SizeGB: volume.SizeGB,
		IdempotencyKey: volume.OperationID, OperationID: volume.OperationID,
	}

	// RED: normal launch must expose the staged provider boundary rather than
	// coupling CBS creation to Kubernetes binding.
	staged, ok := any(provider).(stagedStorageProvider)
	if !ok {
		t.Fatal("TencentProvider must implement stagedStorageProvider")
	}
	created, err := staged.CreateCBSVolume(context.Background(), input)
	if err != nil || created.ProviderResourceID != "disk-staged-alpha" {
		t.Fatalf("CBS create=%#v err=%v", created, err)
	}
	if len(actions) != 1 || actions[0] != "create_storage_volume" {
		t.Fatalf("CBS stage actions=%v", actions)
	}

	read, err := staged.ReadCBSVolume(context.Background(), input, created)
	if err != nil || read.ProviderResourceID != created.ProviderResourceID || len(actions) != 2 || actions[1] != "sync_storage_volume" {
		t.Fatalf("CBS readback=%#v err=%v actions=%v", read, err, actions)
	}

	manifest := map[string]any{}
	if err := json.Unmarshal(staticCBSManifest(volume), &manifest); err != nil {
		t.Fatal(err)
	}
	items := manifest["items"].([]any)
	pvc := items[1].(map[string]any)
	pvc["status"] = map[string]any{"phase": "Bound"}
	var applyCalls, getCalls int
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		switch {
		case slices.Equal(args, []string{"apply", "-f", "-"}):
			applyCalls++
			return nil, nil
		case len(args) > 0 && args[0] == "get":
			getCalls++
			return mustJSON(manifest), nil
		default:
			return nil, fmt.Errorf("unexpected kubectl action %#v", args)
		}
	}
	bound, err := staged.ApplyStaticStorageBinding(context.Background(), read)
	if err != nil || bound.Status != "ready" || applyCalls != 1 {
		t.Fatalf("static binding=%#v err=%v applyCalls=%d getCalls=%d", bound, err, applyCalls, getCalls)
	}
	readBound, err := staged.ReadStaticStorageBinding(context.Background(), bound)
	if err != nil || readBound.Status != "ready" || applyCalls != 1 || getCalls == 0 {
		t.Fatalf("static readback=%#v err=%v applyCalls=%d getCalls=%d", readBound, err, applyCalls, getCalls)
	}
}

func TestTencentProviderCBSResponseLossDiscoversExactDiskWithoutPersistedProviderID(t *testing.T) {
	provider := NewTencentProvider()
	var actions []string
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		actions = append(actions, request.Action)
		switch request.Action {
		case "discover_storage_volume":
			return provisionerResponse{
				OK: true, StorageState: "storage_existing_exact", StorageVolumeID: "disk-response-loss", Status: "provider_ready",
				ProviderRequestID: "req-discover", ProviderData: map[string]string{"diskType": "CLOUD_BSSD", "diskChargeType": "PREPAID", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": "2026-09-01T00:00:00Z", "zone": "ap-guangzhou-3", "sizeGb": "10"},
			}, nil
		case "sync_storage_volume":
			if request.Storage.ID != "disk-response-loss" {
				t.Fatalf("sync disk=%q", request.Storage.ID)
			}
			return provisionerResponse{
				OK: true, StorageVolumeID: "disk-response-loss", Status: "provider_ready", CBSStatus: "UNATTACHED",
				ProviderRequestID: "req-sync", ProviderData: map[string]string{"diskType": "CLOUD_BSSD", "diskChargeType": "PREPAID", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": "2026-09-01T00:00:00Z", "zone": "ap-guangzhou-3", "sizeGb": "10"},
			}, nil
		default:
			return provisionerResponse{}, fmt.Errorf("unexpected action %q", request.Action)
		}
	}
	input := StorageVolumeInput{
		ID: "storage-response-loss", AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", ComputeID: "compute-alpha",
		Zone: "ap-guangzhou-3", SizeGB: 10, IdempotencyKey: "workspace-launch:storage", OperationID: "op_create_storage_volume_fixture",
	}
	persisted := StorageVolume{
		ID: input.ID, OperationID: input.IdempotencyKey, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID,
		Provider: "tencent-tke", Status: "pending", SizeGB: input.SizeGB, Zone: input.Zone,
		CostTags: oplCostTags(input.AccountID, input.WorkspaceID, input.ID, input.OperationID),
	}

	readback, err := provider.ReadCBSVolume(context.Background(), input, persisted)
	if err != nil || readback.ProviderResourceID != "disk-response-loss" || readback.Status != "ready" {
		t.Fatalf("readback=%#v err=%v", readback, err)
	}
	if !slices.Equal(actions, []string{"discover_storage_volume", "sync_storage_volume"}) {
		t.Fatalf("actions=%v", actions)
	}
}

func TestTencentProviderStagedStorageRejectsCBSZoneDrift(t *testing.T) {
	provider := NewTencentProvider()
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Action != "sync_storage_volume" {
			t.Fatalf("unexpected provider action=%q", request.Action)
		}
		return provisionerResponse{
			OK: true, StorageVolumeID: "disk-staged-drift", CBSStatus: "UNATTACHED", Status: "provider_ready", ProviderRequestID: "req-readback",
			ProviderData: map[string]string{"diskType": "CLOUD_BSSD", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": "2026-08-16T00:00:00Z", "zone": "ap-guangzhou-4", "sizeGb": "10", "diskChargeType": "PREPAID"},
		}, nil
	}
	input := StorageVolumeInput{ID: "storage-staged-drift", AccountID: "acct-staged", WorkspaceID: "workspace-staged", Zone: "ap-guangzhou-3", SizeGB: 10, OperationID: "launch-staged:storage"}
	persisted := StorageVolume{ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Provider: "tencent-tke", ProviderResourceID: "disk-staged-drift", SizeGB: input.SizeGB, Zone: input.Zone, DiskType: "CLOUD_BSSD", CostTags: oplCostTags(input.AccountID, input.WorkspaceID, input.ID, input.OperationID)}
	readback, err := provider.ReadCBSVolume(context.Background(), input, persisted)
	if err == nil || readback.Zone != "ap-guangzhou-4" {
		t.Fatalf("zone drift must fail closed: readback=%#v err=%v", readback, err)
	}
}

func TestTencentProviderStagedStorageRejectsStaticBindingLabelDriftWithoutApply(t *testing.T) {
	provider := NewTencentProvider()
	volume := StorageVolume{
		ID: "storage-label-drift", AccountID: "acct-staged", WorkspaceID: "workspace-staged", Provider: "tencent-tke", ProviderResourceID: "disk-label-drift", SizeGB: 10, Zone: "ap-guangzhou-3",
		CostTags: oplCostTags("acct-staged", "workspace-staged", "storage-label-drift", "launch-staged:storage"), ProviderData: map[string]string{"pvName": "opl-storage-label-drift-pv", "pvcName": "opl-storage-label-drift-data"},
	}
	manifest := map[string]any{}
	if err := json.Unmarshal(staticCBSManifest(volume), &manifest); err != nil {
		t.Fatal(err)
	}
	items := manifest["items"].([]any)
	for _, item := range items {
		resource := item.(map[string]any)
		resource["metadata"].(map[string]any)["labels"].(map[string]any)["oplcloud.cn/workspace-id"] = "workspace-other"
	}
	items[1].(map[string]any)["status"] = map[string]any{"phase": "Bound"}
	applyCalls := 0
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		if slices.Equal(args, []string{"apply", "-f", "-"}) {
			applyCalls++
			return nil, nil
		}
		return mustJSON(manifest), nil
	}
	readback, err := provider.ReadStaticStorageBinding(context.Background(), volume)
	if err == nil || readback.Status == "ready" || applyCalls != 0 {
		t.Fatalf("label drift must fail closed without apply: readback=%#v err=%v applyCalls=%d", readback, err, applyCalls)
	}
}

func TestTencentProviderReusesApprovedExactCBSWithOriginalStorageIdentity(t *testing.T) {
	provider := NewTencentProvider()
	var provisioned provisionerRequest
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		provisioned = request
		return provisionerResponse{
			OK: true, StorageState: "storage_existing_exact", StorageVolumeID: "disk-existing-alpha", CBSStatus: "UNATTACHED",
			Status: "provider_ready", ProviderRequestID: "req-discover-cbs", MutationCount: 0,
			ProviderData: map[string]string{
				"diskChargeType": "PREPAID", "diskType": "CLOUD_BSSD", "renewFlag": "NOTIFY_AND_MANUAL_RENEW",
				"deadline": "2026-08-16T00:00:00Z", "zone": "ap-guangzhou-3", "sizeGb": "10", "periodMonths": "1",
			},
		}, nil
	}
	var applied []byte
	provider.kubectl = func(_ context.Context, args []string, stdin []byte) ([]byte, error) {
		if !slices.Equal(args, []string{"apply", "-f", "-"}) {
			t.Fatalf("kubectl args=%#v", args)
		}
		applied = append([]byte(nil), stdin...)
		return nil, nil
	}
	input := StorageVolumeInput{
		ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ComputeID: "compute-alpha",
		Zone: "ap-guangzhou-3", SizeGB: 10, IdempotencyKey: "launch-alpha:storage", OperationID: "op-storage-alpha",
		ExpectedRecoveryState: "storage_existing_exact", ExpectedProviderResourceID: "disk-existing-alpha",
	}

	volume, err := provider.CreateStorageVolume(context.Background(), input)

	if err != nil || volume.ID != input.ID || volume.OperationID != input.IdempotencyKey || volume.ProviderResourceID != input.ExpectedProviderResourceID ||
		volume.CostTags["opl_operation_id"] != input.OperationID || volume.ProviderRequestID != "req-discover-cbs" {
		t.Fatalf("reused volume=%#v err=%v", volume, err)
	}
	if provisioned.Action != "create_storage_volume" || provisioned.Storage.ExpectedState != input.ExpectedRecoveryState ||
		provisioned.Storage.ExpectedProviderResourceID != input.ExpectedProviderResourceID || provisioned.Storage.ID != input.ID ||
		!reflect.DeepEqual(provisioned.Tags, oplCostTags(input.AccountID, input.WorkspaceID, input.ID, input.OperationID)) {
		t.Fatalf("recovery storage request=%#v", provisioned)
	}
	var manifest map[string]any
	if json.Unmarshal(applied, &manifest) != nil {
		t.Fatalf("static binding manifest=%s", applied)
	}
	items := manifest["items"].([]any)
	if len(items) != 2 || nested(items[0].(map[string]any), "spec", "csi", "volumeHandle") != input.ExpectedProviderResourceID ||
		nested(items[1].(map[string]any), "spec", "volumeName") != nested(items[0].(map[string]any), "metadata", "name") {
		t.Fatalf("reused static binding=%#v", manifest)
	}
}

func TestTencentProviderCreatesStaticBindingWhileCBSIsStillConverging(t *testing.T) {
	provider := NewTencentProvider()
	provider.provision = func(context.Context, provisionerRequest) (provisionerResponse, error) {
		return provisionerResponse{
			OK: true, StorageVolumeID: "disk-storage-alpha", CBSStatus: "CREATING", Status: "pending", ProviderRequestID: "req-create-cbs",
			ProviderData: map[string]string{"diskChargeType": "PREPAID", "diskType": "CLOUD_BSSD", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": "2026-08-16T00:00:00Z", "zone": "ap-guangzhou-3", "sizeGb": "10"},
		}, nil
	}
	applies := 0
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		if slices.Equal(args, []string{"apply", "-f", "-"}) {
			applies++
		}
		return nil, nil
	}

	volume, err := provider.CreateStorageVolume(context.Background(), StorageVolumeInput{
		ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ComputeID: "compute-alpha", Zone: "ap-guangzhou-3", SizeGB: 10,
		IdempotencyKey: "storage-once", OperationID: "op-storage-alpha",
	})

	if err != nil || volume.ProviderResourceID != "disk-storage-alpha" || volume.Status != "pending" || applies != 1 {
		t.Fatalf("converging CBS must create one static binding: volume=%#v applies=%d err=%v", volume, applies, err)
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
					t.Fatal("storage Sync must be read-only")
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
