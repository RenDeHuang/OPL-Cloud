package fabric

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"opl-cloud/services/fabric/internal/protectedresource"
)

const testTencentProviderProfile = `{"schemaVersion":1,"packages":[{"id":"basic","name":"Basic Workspace","available":true,"compute":{"id":"pool-basic-2c4g","server":"2c4g","cpu":2,"memoryGb":4,"diskGb":10,"instanceType":"SA5.MEDIUM4"},"nodePoolId":"np-basic","maxReplicas":20,"zone":"ap-guangzhou-3","storage":{"sizeGb":10,"diskType":"CLOUD_BSSD"},"billing":{"chargeType":"PREPAID","periodMonths":1,"renewFlag":"NOTIFY_AND_MANUAL_RENEW"}},{"id":"pro","name":"Pro Workspace","available":true,"compute":{"id":"pool-pro-8c16g","server":"8c16g","cpu":8,"memoryGb":16,"diskGb":100,"instanceType":"SA5.2XLARGE16"},"nodePoolId":"np-pro","maxReplicas":20,"zone":"ap-guangzhou-3","storage":{"sizeGb":100,"diskType":"CLOUD_BSSD"},"billing":{"chargeType":"PREPAID","periodMonths":1,"renewFlag":"NOTIFY_AND_MANUAL_RENEW"}}]}`

const workspaceImageRepository = "uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app"

const testDefaultTencentProviderProfile = `{"schemaVersion":1,"packages":[{"id":"basic","name":"Basic Workspace","available":true,"compute":{"id":"pool-basic-2c4g","server":"2c4g","cpu":2,"memoryGb":4,"diskGb":10,"instanceType":"SA5.MEDIUM4"},"nodePoolId":"np-basic","maxReplicas":20,"zone":"na-siliconvalley-1","storage":{"sizeGb":10,"diskType":"CLOUD_BSSD"},"billing":{"chargeType":"PREPAID","periodMonths":1,"renewFlag":"NOTIFY_AND_MANUAL_RENEW"}},{"id":"pro","name":"Pro Workspace","available":true,"compute":{"id":"pool-pro-8c16g","server":"8c16g","cpu":8,"memoryGb":16,"diskGb":100,"instanceType":"SA5.2XLARGE16"},"nodePoolId":"np-pro","maxReplicas":20,"zone":"na-siliconvalley-1","storage":{"sizeGb":100,"diskType":"CLOUD_BSSD"},"billing":{"chargeType":"PREPAID","periodMonths":1,"renewFlag":"NOTIFY_AND_MANUAL_RENEW"}}]}`

func TestMain(m *testing.M) {
	for key, value := range map[string]string{
		tencentProviderProfileEnv:                          testDefaultTencentProviderProfile,
		tencentProviderRegionEnv:                           "ap-guangzhou",
		"OPL_WORKSPACE_DOMAIN":                             "workspace.medopl.cn",
		"OPL_WORKSPACE_IMAGE":                              workspaceImageRepository + "@sha256:" + strings.Repeat("a", 64),
		"OPL_K8S_NAMESPACE":                                "opl-cloud",
		"OPL_TENCENT_PROVISIONER_BIN":                      "/bin/true",
		"OPL_FABRIC_LOCAL_DOCKER_TRUSTED_WORKSPACE_IMAGES": "ghcr.io/gaofeng21cn/one-person-lab-webui@sha256:" + strings.Repeat("a", 64),
		"OPL_SYSTEM_COMPUTE_NODE_POOL_ID":                  "np-system",
		"OPL_SYSTEM_COMPUTE_MACHINE_ID":                    "machine-system",
		"OPL_SYSTEM_COMPUTE_NODE_NAME":                     "10.66.0.42",
		"OPL_SYSTEM_COMPUTE_MACHINE_TYPE":                  "NativeCVM",
		"OPL_SYSTEM_COMPUTE_CVM_ID":                        "ins-system",
		"OPL_BASIC_COMPUTE_NODE_POOL_ID":                   "np-basic",
		"OPL_PRO_COMPUTE_NODE_POOL_ID":                     "np-pro",
		"OPL_BASIC_COMPUTE_INSTANCE_TYPE":                  "SA5.MEDIUM4",
		"OPL_PRO_COMPUTE_INSTANCE_TYPE":                    "SA5.2XLARGE16",
		"TENCENT_CBS_DISK_TYPE":                            "CLOUD_BSSD",
		"OPL_FABRIC_LOCAL_DOCKER_PROVIDER_PROFILE_JSON":    `{"schemaVersion":1,"packages":[{"id":"basic","name":"Basic Workspace","available":true,"compute":{"id":"local-basic-2c4g","server":"2c4g","cpu":2,"memoryGb":4,"diskGb":10,"instanceType":"local-2c4g"},"storage":{"sizeGb":10,"quotaPolicy":"linux-project"}},{"id":"pro","name":"Pro Workspace","available":true,"compute":{"id":"local-pro-8c16g","server":"8c16g","cpu":8,"memoryGb":16,"diskGb":100,"instanceType":"local-8c16g"},"storage":{"sizeGb":100,"quotaPolicy":"linux-project"}}]}`,
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

func setTencentProviderProfileEnv(t *testing.T) {
	t.Helper()
	t.Setenv(tencentProviderProfileEnv, testTencentProviderProfile)
	t.Setenv(tencentProviderRegionEnv, "ap-guangzhou")
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

func TestTencentProviderProfileBindsWorkspaceFactsAndIgnoresLaterProfileDrift(t *testing.T) {
	profile := `{"schemaVersion":1,"packages":[{"id":"basic","name":"Basic Workspace","available":true,"compute":{"id":"pool-basic-2c4g","server":"2c4g","cpu":2,"memoryGb":4,"diskGb":10,"instanceType":"SA5.MEDIUM4"},"nodePoolId":"np-basic","maxReplicas":20,"zone":"ap-guangzhou-3","storage":{"sizeGb":10,"diskType":"CLOUD_BSSD"},"billing":{"chargeType":"PREPAID","periodMonths":1,"renewFlag":"NOTIFY_AND_MANUAL_RENEW"}}]}`
	t.Setenv(tencentProviderProfileEnv, profile)
	t.Setenv(tencentProviderRegionEnv, "ap-guangzhou")
	provider := NewTencentProvider()
	first, err := provider.ResolveWorkspacePlan(context.Background(), WorkspaceLaunchPlanInput{PackageID: "basic", SizeGB: 10})
	if err != nil {
		t.Fatalf("resolve profile plan: %v", err)
	}
	var resolved tencentWorkspacePlan
	if err := json.Unmarshal(first, &resolved); err != nil || resolved.NodePoolID != "np-basic" || resolved.Region != "ap-guangzhou" || resolved.Zone != "ap-guangzhou-3" || resolved.Storage.DiskType != "CLOUD_BSSD" || resolved.Billing.RenewFlag != "NOTIFY_AND_MANUAL_RENEW" {
		t.Fatalf("resolved plan=%#v err=%v", resolved, err)
	}

	rotated := strings.ReplaceAll(profile, "np-basic", "np-basic-rotated")
	rotated = strings.ReplaceAll(rotated, "SA5.MEDIUM4", "SA5.MEDIUM8")
	t.Setenv(tencentProviderProfileEnv, rotated)
	second, err := provider.ResolveWorkspacePlan(context.Background(), WorkspaceLaunchPlanInput{PackageID: "basic", SizeGB: 10})
	if err != nil || string(first) != string(second) {
		t.Fatalf("loaded profile drifted after environment change: first=%s second=%s err=%v", first, second, err)
	}
}

func TestTencentProviderProfileControlsBasicAndProAvailability(t *testing.T) {
	for _, tc := range []struct {
		name           string
		basicAvailable bool
		proAvailable   bool
	}{
		{name: "Basic only", basicAvailable: true},
		{name: "Pro only", proAvailable: true},
		{name: "Basic and Pro", basicAvailable: true, proAvailable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var profile tencentProviderProfile
			if err := json.Unmarshal([]byte(testTencentProviderProfile), &profile); err != nil {
				t.Fatal(err)
			}
			for index := range profile.Packages {
				switch profile.Packages[index].ID {
				case "basic":
					profile.Packages[index].Available = tc.basicAvailable
				case "pro":
					profile.Packages[index].Available = tc.proAvailable
				}
			}
			raw, err := json.Marshal(profile)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv(tencentProviderProfileEnv, string(raw))
			provider := NewTencentProvider()
			descriptor := provider.Descriptor()
			if len(descriptor.Catalog.WorkspacePackages) != 2 {
				t.Fatalf("catalog packages=%#v", descriptor.Catalog.WorkspacePackages)
			}
			for index, packageID := range []string{"basic", "pro"} {
				wantAvailable := map[string]bool{"basic": tc.basicAvailable, "pro": tc.proAvailable}[packageID]
				catalogPackage := descriptor.Catalog.WorkspacePackages[index]
				if catalogPackage.ID != packageID || catalogPackage.Available != wantAvailable {
					t.Fatalf("%s catalog package=%#v wantAvailable=%v", packageID, catalogPackage, wantAvailable)
				}
				_, planAvailable := descriptor.Plans[packageID]
				if planAvailable != wantAvailable {
					t.Fatalf("%s descriptor plan available=%v want=%v", packageID, planAvailable, wantAvailable)
				}
				sizeGB := map[string]int{"basic": 10, "pro": 100}[packageID]
				_, resolveErr := provider.ResolveWorkspacePlan(context.Background(), WorkspaceLaunchPlanInput{PackageID: packageID, SizeGB: sizeGB})
				if wantAvailable && resolveErr != nil || !wantAvailable && !errors.Is(resolveErr, ErrProviderPlanUnavailable) {
					t.Fatalf("%s resolve error=%v wantAvailable=%v", packageID, resolveErr, wantAvailable)
				}
			}
		})
	}
}

func TestTencentProviderProfileRejectsMissingProviderFacts(t *testing.T) {
	for _, raw := range []string{
		`{"schemaVersion":1,"packages":[{"id":"basic","name":"Basic","available":true,"compute":{"id":"pool-basic","server":"2c4g","cpu":2,"memoryGb":4,"diskGb":10,"instanceType":"SA5.MEDIUM4"},"nodePoolId":"","maxReplicas":20,"zone":"ap-guangzhou-3","storage":{"sizeGb":10,"diskType":"CLOUD_BSSD"},"billing":{"chargeType":"PREPAID","periodMonths":1,"renewFlag":"NOTIFY_AND_MANUAL_RENEW"}}]}`,
		`{"schemaVersion":1,"packages":[{"id":"basic","name":"Basic","available":true,"compute":{"id":"pool-basic","server":"2c4g","cpu":2,"memoryGb":4,"diskGb":10,"instanceType":"SA5.MEDIUM4"},"nodePoolId":"np-basic","maxReplicas":20,"zone":"","storage":{"sizeGb":10,"diskType":"CLOUD_BSSD"},"billing":{"chargeType":"PREPAID","periodMonths":1,"renewFlag":"NOTIFY_AND_MANUAL_RENEW"}}]}`,
		`{"schemaVersion":1,"packages":[{"id":"basic","name":"Basic","available":true,"compute":{"id":"pool-basic","server":"2c4g","cpu":2,"memoryGb":4,"diskGb":10,"instanceType":"SA5.MEDIUM4"},"nodePoolId":"np-basic","maxReplicas":20,"zone":"ap-guangzhou-3","storage":{"sizeGb":10,"diskType":""},"billing":{"chargeType":"POSTPAID","periodMonths":1,"renewFlag":"NOTIFY_AND_MANUAL_RENEW"}}]}`,
	} {
		t.Setenv(tencentProviderProfileEnv, raw)
		provider := NewTencentProvider()
		if _, err := provider.ResolveWorkspacePlan(context.Background(), WorkspaceLaunchPlanInput{PackageID: "basic", SizeGB: 10}); !errors.Is(err, ErrProviderPlanUnavailable) {
			t.Fatalf("invalid profile resolve err=%v", err)
		}
	}
}

func TestTencentProviderRequiresProfileAndFailsClosed(t *testing.T) {
	t.Setenv(tencentProviderProfileEnv, "")
	provider := NewTencentProvider()
	descriptor := provider.Descriptor()
	if len(descriptor.Plans) != 0 || len(descriptor.Catalog.WorkspacePackages) != 0 || len(descriptor.DefaultComputePoolIDs) != 0 {
		t.Fatalf("missing profile exposed provider plans: %#v", descriptor)
	}
	if _, err := provider.ResolveWorkspacePlan(context.Background(), WorkspaceLaunchPlanInput{PackageID: "basic", SizeGB: 10}); !errors.Is(err, ErrProviderPlanUnavailable) {
		t.Fatalf("missing profile resolve err=%v", err)
	}
}

func TestTencentProviderRequiresExplicitInstallationInputsBeforeProviderAccess(t *testing.T) {
	for _, key := range []string{"OPL_WORKSPACE_DOMAIN", "OPL_WORKSPACE_IMAGE", "OPL_K8S_NAMESPACE", "OPL_TENCENT_PROVISIONER_BIN"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "")
			provider := NewTencentProvider()
			if provider.installationErr == nil {
				t.Fatalf("missing %s did not invalidate provider configuration", key)
			}
			if _, err := provider.provision(context.Background(), provisionerRequest{Action: "readiness"}); err == nil {
				t.Fatalf("missing %s reached provisioner", key)
			}
			if _, err := provider.callKubectl(context.Background(), []string{"apply", "-f", "-"}, []byte(`{}`), protectedresource.Target{}); err == nil {
				t.Fatalf("missing %s reached kubectl mutation", key)
			}
		})
	}
}

func TestTencentProviderUsesInstallationInputsCapturedAtConstruction(t *testing.T) {
	initialDir := t.TempDir()
	initialProvisioner := filepath.Join(initialDir, "provisioner")
	if err := os.WriteFile(initialProvisioner, []byte("#!/bin/sh\nprintf '%s\\n' '{\"ok\":true,\"status\":\"initial\"}'\n"), 0o700); err != nil {
		t.Fatalf("write initial provisioner: %v", err)
	}
	rotatedDir := t.TempDir()
	rotatedProvisioner := filepath.Join(rotatedDir, "provisioner")
	if err := os.WriteFile(rotatedProvisioner, []byte("#!/bin/sh\nprintf '%s\\n' '{\"ok\":true,\"status\":\"rotated\"}'\n"), 0o700); err != nil {
		t.Fatalf("write rotated provisioner: %v", err)
	}
	t.Setenv("OPL_TENCENT_PROVISIONER_BIN", initialProvisioner)
	t.Setenv("OPL_K8S_NAMESPACE", "initial-namespace")
	provider := NewTencentProvider()
	t.Setenv("OPL_TENCENT_PROVISIONER_BIN", rotatedProvisioner)
	t.Setenv("OPL_K8S_NAMESPACE", "rotated-namespace")

	response, err := provider.provision(context.Background(), provisionerRequest{Action: "readiness"})
	if err != nil || response.Status != "initial" {
		t.Fatalf("provisioner response=%#v err=%v, want initial installation input", response, err)
	}

	kubectlDir := t.TempDir()
	kubectl := filepath.Join(kubectlDir, "kubectl")
	if err := os.WriteFile(kubectl, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\"\n"), 0o700); err != nil {
		t.Fatalf("write kubectl: %v", err)
	}
	t.Setenv("PATH", kubectlDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := provider.kubectl(context.Background(), []string{"get", "pods"}, nil)
	if err != nil {
		t.Fatalf("kubectl: %v", err)
	}
	if got := strings.TrimSpace(string(output)); !strings.Contains(got, "--namespace initial-namespace") || strings.Contains(got, "rotated-namespace") {
		t.Fatalf("kubectl args=%q, want captured namespace", got)
	}
}

func TestTencentProviderDescriptorUsesConfiguredPackageIdentity(t *testing.T) {
	t.Setenv(tencentProviderProfileEnv, `{"schemaVersion":1,"packages":[{"id":"starter","name":"Starter","available":true,"compute":{"id":"pool-starter","server":"configured","cpu":3,"memoryGb":6,"diskGb":30,"instanceType":"S6.MEDIUM6"},"nodePoolId":"np-starter","maxReplicas":7,"zone":"ap-guangzhou-3","storage":{"sizeGb":30,"diskType":"CLOUD_PREMIUM"},"billing":{"chargeType":"PREPAID","periodMonths":1,"renewFlag":"NOTIFY_AND_MANUAL_RENEW"}}]}`)
	provider := NewTencentProvider()
	descriptor := provider.Descriptor()
	plan, ok := descriptor.Plans["starter"]
	if !ok || plan.ID != "pool-starter" || plan.CPU != 3 || plan.MemoryGB != 6 || descriptor.DefaultComputePoolIDs["starter"] != "np-starter" {
		t.Fatalf("configured starter plan not exposed: %#v", descriptor)
	}
	if _, err := provider.ResolveWorkspacePlan(context.Background(), WorkspaceLaunchPlanInput{PackageID: "starter", SizeGB: 30}); err != nil {
		t.Fatalf("starter plan resolve err=%v", err)
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

func TestComputeClaimNodeOwnershipUsesPackageTaintAndWorkspaceLabels(t *testing.T) {
	allocation, _, ownership := computeClaimProviderFixture()
	for _, tc := range []struct {
		name      string
		raw       string
		packageID string
		wantState string
		wantOK    bool
	}{
		{
			name:      "basic unallocated",
			raw:       `{"metadata":{"name":"10.0.0.8","labels":{}},"spec":{"taints":[{"key":"oplcloud.cn/package-id","value":"basic","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`,
			packageID: "basic", wantState: "unallocated", wantOK: true,
		},
		{
			name:      "pro unallocated",
			raw:       `{"metadata":{"name":"10.0.0.8","labels":{}},"spec":{"taints":[{"key":"oplcloud.cn/package-id","value":"pro","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`,
			packageID: "pro", wantState: "unallocated", wantOK: true,
		},
		{
			name:      "target owned labels preserve package taint",
			raw:       `{"metadata":{"name":"10.0.0.8","labels":{"medopl.cn/workload":"workspace","oplcloud.cn/resource-id":"compute-alpha","oplcloud.cn/account-id":"acct-alpha","oplcloud.cn/workspace-id":"ws-alpha"}},"spec":{"taints":[{"key":"oplcloud.cn/package-id","value":"basic","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`,
			packageID: "basic", wantState: "target_owned", wantOK: true,
		},
		{
			name:      "preinstalled pool workload label is not ownership",
			raw:       `{"metadata":{"name":"10.0.0.8","labels":{"medopl.cn/workload":"workspace"}},"spec":{"taints":[{"key":"oplcloud.cn/package-id","value":"basic","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`,
			packageID: "basic", wantState: "unallocated", wantOK: true,
		},
		{
			name:      "preinstalled pool labels are not ownership",
			raw:       `{"metadata":{"name":"10.0.0.8","labels":{"medopl.cn/workload":"workspace","oplcloud.cn/package-id":"basic"}},"spec":{"taints":[{"key":"oplcloud.cn/package-id","value":"basic","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`,
			packageID: "basic", wantState: "unallocated", wantOK: true,
		},
		{
			name:      "partial ownership labels conflict",
			raw:       `{"metadata":{"name":"10.0.0.8","labels":{"medopl.cn/workload":"workspace","oplcloud.cn/package-id":"basic","oplcloud.cn/resource-id":"compute-alpha"}},"spec":{"taints":[{"key":"oplcloud.cn/package-id","value":"basic","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`,
			packageID: "basic", wantState: "node_ownership_conflict", wantOK: false,
		},
		{
			name:      "other workspace label conflict",
			raw:       `{"metadata":{"name":"10.0.0.8","labels":{"medopl.cn/workload":"workspace","oplcloud.cn/resource-id":"compute-alpha","oplcloud.cn/account-id":"acct-alpha","oplcloud.cn/workspace-id":"ws-other"}},"spec":{"taints":[{"key":"oplcloud.cn/package-id","value":"basic","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`,
			packageID: "basic", wantState: "node_ownership_conflict", wantOK: false,
		},
		{
			name:      "other account label conflict",
			raw:       `{"metadata":{"name":"10.0.0.8","labels":{"medopl.cn/workload":"workspace","oplcloud.cn/resource-id":"compute-alpha","oplcloud.cn/account-id":"acct-other","oplcloud.cn/workspace-id":"ws-alpha"}},"spec":{"taints":[{"key":"oplcloud.cn/package-id","value":"basic","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`,
			packageID: "basic", wantState: "node_ownership_conflict", wantOK: false,
		},
		{
			name:      "other resource label conflict",
			raw:       `{"metadata":{"name":"10.0.0.8","labels":{"medopl.cn/workload":"workspace","oplcloud.cn/resource-id":"compute-other","oplcloud.cn/account-id":"acct-alpha","oplcloud.cn/workspace-id":"ws-alpha"}},"spec":{"taints":[{"key":"oplcloud.cn/package-id","value":"basic","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`,
			packageID: "basic", wantState: "node_ownership_conflict", wantOK: false,
		},
		{
			name:      "duplicate package taint conflict",
			raw:       `{"metadata":{"name":"10.0.0.8","labels":{}},"spec":{"taints":[{"key":"oplcloud.cn/package-id","value":"basic","effect":"NoSchedule"},{"key":"oplcloud.cn/package-id","value":"basic","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`,
			packageID: "basic", wantState: "node_ownership_conflict", wantOK: false,
		},
		{
			name:      "wrong package taint conflict",
			raw:       `{"metadata":{"name":"10.0.0.8","labels":{}},"spec":{"taints":[{"key":"oplcloud.cn/package-id","value":"pro","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`,
			packageID: "basic", wantState: "node_ownership_conflict", wantOK: false,
		},
		{
			name:      "exact legacy pool unallocated",
			raw:       `{"metadata":{"name":"10.0.0.8","labels":{"medopl.cn/workload":"workspace","oplcloud.cn/package-id":"basic"}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"unallocated","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`,
			packageID: "basic", wantState: "unallocated", wantOK: true,
		},
		{
			name:      "legacy taint without pool identity conflicts",
			raw:       `{"metadata":{"name":"10.0.0.8","labels":{}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"unallocated","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`,
			packageID: "basic", wantState: "node_ownership_conflict", wantOK: false,
		},
		{
			name:      "legacy wrong package label conflicts",
			raw:       `{"metadata":{"name":"10.0.0.8","labels":{"medopl.cn/workload":"workspace","oplcloud.cn/package-id":"pro"}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"unallocated","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`,
			packageID: "basic", wantState: "node_ownership_conflict", wantOK: false,
		},
		{
			name:      "legacy partial ownership conflicts",
			raw:       `{"metadata":{"name":"10.0.0.8","labels":{"medopl.cn/workload":"workspace","oplcloud.cn/package-id":"basic","oplcloud.cn/account-id":"acct-alpha"}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"unallocated","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`,
			packageID: "basic", wantState: "node_ownership_conflict", wantOK: false,
		},
		{
			name:      "real workspace taint conflicts",
			raw:       `{"metadata":{"name":"10.0.0.8","labels":{"medopl.cn/workload":"workspace","oplcloud.cn/package-id":"basic"}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"ws-other","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`,
			packageID: "basic", wantState: "node_ownership_conflict", wantOK: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			allocation.PackageID, ownership.PackageID = tc.packageID, tc.packageID
			state, ok := computeClaimNodeOwnershipState([]byte(tc.raw), allocation, ownership)
			if state != tc.wantState || ok != tc.wantOK {
				t.Fatalf("state=%q ok=%v want state=%q ok=%v", state, ok, tc.wantState, tc.wantOK)
			}
		})
	}
}

func TestComputeClaimNodePatchPreservesPackageTaint(t *testing.T) {
	allocation, _, ownership := computeClaimProviderFixture()
	raw := []byte(`{"metadata":{"name":"10.0.0.8","resourceVersion":"7","labels":{}},"spec":{"taints":[{"key":"oplcloud.cn/package-id","value":"basic","effect":"NoSchedule"}]}}`)
	patch, err := computeClaimNodePatch(raw, allocation, ownership)
	if err != nil {
		t.Fatal(err)
	}
	var operations []map[string]any
	if err := json.Unmarshal(patch, &operations); err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{
		{"op": "test", "path": "/metadata/resourceVersion", "value": "7"},
		{"op": "add", "path": "/metadata/labels/medopl.cn~1workload", "value": "workspace"},
		{"op": "add", "path": "/metadata/labels/oplcloud.cn~1resource-id", "value": "compute-alpha"},
		{"op": "add", "path": "/metadata/labels/oplcloud.cn~1account-id", "value": "acct-alpha"},
		{"op": "add", "path": "/metadata/labels/oplcloud.cn~1workspace-id", "value": "ws-alpha"},
	}
	if !reflect.DeepEqual(operations, want) {
		t.Fatalf("claim patch=%#v want=%#v", operations, want)
	}
	for _, operation := range operations {
		path, _ := operation["path"].(string)
		if strings.HasPrefix(path, "/spec/taints") {
			t.Fatalf("claim patch must not write the package taint, patch=%s", patch)
		}
	}
}

func TestComputeClaimNodePatchConvertsExactLegacyPoolTaintAtomically(t *testing.T) {
	allocation, _, ownership := computeClaimProviderFixture()
	raw := []byte(`{"metadata":{"name":"10.0.0.8","resourceVersion":"7","labels":{"medopl.cn/workload":"workspace","oplcloud.cn/package-id":"basic"}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"unallocated","effect":"NoSchedule"}]}}`)
	patch, err := computeClaimNodePatch(raw, allocation, ownership)
	if err != nil {
		t.Fatal(err)
	}
	var operations []map[string]any
	if err := json.Unmarshal(patch, &operations); err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{
		{"op": "test", "path": "/metadata/resourceVersion", "value": "7"},
		{"op": "test", "path": "/spec/taints", "value": []any{map[string]any{"key": "oplcloud.cn/workspace-id", "value": "unallocated", "effect": "NoSchedule"}}},
		{"op": "replace", "path": "/spec/taints", "value": []any{map[string]any{"key": "oplcloud.cn/package-id", "value": "basic", "effect": "NoSchedule"}}},
		{"op": "add", "path": "/metadata/labels/oplcloud.cn~1resource-id", "value": "compute-alpha"},
		{"op": "add", "path": "/metadata/labels/oplcloud.cn~1account-id", "value": "acct-alpha"},
		{"op": "add", "path": "/metadata/labels/oplcloud.cn~1workspace-id", "value": "ws-alpha"},
	}
	if !reflect.DeepEqual(operations, want) {
		t.Fatalf("legacy claim patch=%#v want=%#v", operations, want)
	}
}

func TestTencentClaimComputeNodeLegacyConversionConvergesOrFailsClosedOnTemplateDrift(t *testing.T) {
	for _, tc := range []struct {
		name            string
		controllerDrift bool
		wantErr         bool
		wantGets        int
	}{
		{name: "converted target ownership replays without mutation", wantGets: 3},
		{name: "controller restores legacy taint", controllerDrift: true, wantErr: true, wantGets: 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setProtectedResourceEnv(t)
			allocation, _, ownership := computeClaimProviderFixture()
			provider := NewTencentProvider()
			provider.convergenceWait = func(context.Context, int) error { return nil }
			legacy := []byte(`{"metadata":{"name":"10.0.0.8","resourceVersion":"7","labels":{"medopl.cn/workload":"workspace","oplcloud.cn/package-id":"basic"}},"spec":{"taints":[{"key":"oplcloud.cn/workspace-id","value":"unallocated","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`)
			target := []byte(`{"metadata":{"name":"10.0.0.8","resourceVersion":"8","labels":{"medopl.cn/workload":"workspace","oplcloud.cn/package-id":"basic","oplcloud.cn/resource-id":"compute-alpha","oplcloud.cn/account-id":"acct-alpha","oplcloud.cn/workspace-id":"ws-alpha"}},"spec":{"taints":[{"key":"oplcloud.cn/package-id","value":"basic","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`)
			owned, gets, patches := false, 0, 0
			provider.kubectl = func(_ context.Context, args []string, stdin []byte) ([]byte, error) {
				switch args[0] {
				case "get":
					gets++
					if owned && !tc.controllerDrift {
						return target, nil
					}
					return legacy, nil
				case "patch":
					patches++
					var operations []map[string]any
					if json.Unmarshal(stdin, &operations) != nil || len(operations) < 3 || operations[1]["op"] != "test" || operations[2]["op"] != "replace" {
						t.Fatalf("legacy patch is not an atomic tested conversion: %s", stdin)
					}
					owned = true
					return nil, nil
				default:
					t.Fatalf("unexpected kubectl args=%#v", args)
					return nil, nil
				}
			}

			err := provider.ClaimComputeNode(context.Background(), allocation, ownership)
			if tc.wantErr {
				if err == nil {
					t.Fatal("controller drift unexpectedly converged")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if err := provider.ClaimComputeNode(context.Background(), allocation, ownership); err != nil {
					t.Fatalf("target-owned replay: %v", err)
				}
			}
			if patches != 1 || gets != tc.wantGets {
				t.Fatalf("gets=%d patches=%d want gets=%d patches=1 err=%v", gets, patches, tc.wantGets, err)
			}
		})
	}
}

func TestWorkspaceManifestUsesPackageToleration(t *testing.T) {
	input := WorkspaceRuntimeInput{WorkspaceID: "ws-alpha", RuntimeOperationID: "workspace-launch-alpha:workspace:runtime", ImageID: "registry.example/one-person-lab-app@sha256:" + repeatHex("a", 64)}
	compute := ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", NodeName: "10.0.0.8", NodeSelector: map[string]any{"kubernetes.io/hostname": "10.0.0.8"}}
	volume := StorageVolume{ID: "storage-alpha"}
	var manifest map[string]any
	if err := json.Unmarshal(workspaceManifest(input, input.WorkspaceID, "seed", "rt-alpha", "opl-compute-alpha", compute, volume, map[string]string{}), &manifest); err != nil {
		t.Fatal(err)
	}
	items := manifest["items"].([]any)
	deployment := items[1].(map[string]any)
	tolerations := nested(deployment, "spec", "template", "spec", "tolerations")
	want := []any{
		map[string]any{"key": "oplcloud.cn/package-id", "operator": "Equal", "value": "basic", "effect": "NoSchedule"},
	}
	if !reflect.DeepEqual(tolerations, want) {
		t.Fatalf("runtime tolerations=%#v want=%#v", tolerations, want)
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
	for _, tc := range []struct {
		name  string
		input MonthlyPreflightInput
		check func(*testing.T, provisionerRequest)
		reply provisionerResponse
	}{
		{
			name: "compute", input: MonthlyPreflightInput{ResourceType: "compute", PackageID: "basic", Zone: "na-siliconvalley-1"},
			check: func(t *testing.T, request provisionerRequest) {
				if request.Action != "capacity_preflight" || request.PackageID != "basic" || request.Zone != "na-siliconvalley-1" || request.Pool.ID != "pool-basic-2c4g" || request.Pool.NodePoolID != "np-basic" || request.Pool.InstanceType != "SA5.MEDIUM4" || request.Pool.CPU != 2 || request.Pool.MemoryGB != 4 || request.Pool.DesiredReplicas != 1 || request.Pool.MaxReplicas != 20 {
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
				if request.Action != "capacity_preflight" || request.PackageID != "pro" || request.Zone != "na-siliconvalley-1" || request.Pool.ID != "pool-pro-8c16g" || request.Pool.NodePoolID != "np-pro" || request.Pool.InstanceType != "SA5.2XLARGE16" || request.Pool.CPU != 8 || request.Pool.MemoryGB != 16 || request.Pool.DesiredReplicas != 1 || request.Pool.MaxReplicas != 20 {
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
	t.Setenv(tencentProviderProfileEnv, strings.Replace(testDefaultTencentProviderProfile, `"nodePoolId":"np-basic"`, `"nodePoolId":""`, 1))
	provider := NewTencentProvider()
	provider.provision = func(context.Context, provisionerRequest) (provisionerResponse, error) {
		t.Fatal("missing exact pool configuration reached provisioner")
		return provisionerResponse{}, nil
	}
	if _, err := provider.MonthlyPreflight(context.Background(), MonthlyPreflightInput{ResourceType: "compute", PackageID: "basic", Zone: "na-siliconvalley-1"}); err == nil || !errors.Is(err, ErrProviderPlanUnavailable) {
		t.Fatalf("error=%v", err)
	}
}

func TestTencentProviderCustomerComputeRequiresReleaseOwnerResolvedInstanceType(t *testing.T) {
	t.Setenv("RUN_TENCENT_CREATE_RELEASE_EXECUTION", "1")
	t.Setenv(tencentProviderProfileEnv, strings.Replace(testDefaultTencentProviderProfile, `"instanceType":"SA5.2XLARGE16"`, `"instanceType":""`, 1))
	provider := NewTencentProvider()
	provider.provision = func(context.Context, provisionerRequest) (provisionerResponse, error) {
		t.Fatal("missing release-owner resolved SKU reached provisioner")
		return provisionerResponse{}, nil
	}
	if _, err := provider.MonthlyPreflight(context.Background(), MonthlyPreflightInput{ResourceType: "compute", PackageID: "pro", Zone: "na-siliconvalley-1"}); err == nil || !errors.Is(err, ErrProviderPlanUnavailable) {
		t.Fatalf("error=%v", err)
	}
}

func TestTencentProviderPrepareComputeAllocationPersistsConfiguredPoolLimit(t *testing.T) {
	t.Setenv(tencentProviderProfileEnv, strings.Replace(testDefaultTencentProviderProfile, `"maxReplicas":20`, `"maxReplicas":40`, 1))
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

func TestTencentProviderComputeClaimRecoveryProofCombinesTencentAndNodeReadOnlyTruth(t *testing.T) {
	setProtectedResourceEnv(t)
	setTencentProviderProfileEnv(t)
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
		return []byte(`{"metadata":{"name":"10.0.0.8","labels":{}},"spec":{"taints":[{"key":"oplcloud.cn/package-id","value":"basic","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`), nil
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
		{name: "node target owned", wantReason: "", kubectlRaw: []byte(`{"metadata":{"name":"10.0.0.8","labels":{"medopl.cn/workload":"workspace","oplcloud.cn/resource-id":"compute-alpha","oplcloud.cn/account-id":"acct-alpha","oplcloud.cn/workspace-id":"ws-alpha"}},"spec":{"taints":[{"key":"oplcloud.cn/package-id","value":"basic","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`)},
		{name: "controller resync preserves target ownership", wantReason: "", kubectlRaw: []byte(`{"metadata":{"name":"10.0.0.8","labels":{"medopl.cn/workload":"workspace","oplcloud.cn/resource-id":"compute-alpha","oplcloud.cn/account-id":"acct-alpha","oplcloud.cn/workspace-id":"ws-alpha"}},"spec":{"taints":[{"key":"oplcloud.cn/package-id","value":"basic","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`)},
		{name: "node ownership conflict", wantReason: "node_ownership_conflict", kubectlRaw: []byte(`{"metadata":{"name":"10.0.0.8","labels":{"oplcloud.cn/workspace-id":"ws-other"}},"spec":{"taints":[{"key":"oplcloud.cn/package-id","value":"basic","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`)},
		{name: "rbac", wantReason: "iam_rbac", kubectlErr: errors.New("Error from server (Forbidden): nodes is forbidden")},
		{name: "malformed", wantReason: "identity_mismatch", kubectlRaw: []byte(`{"metadata":{"name":"10.0.0.8"}}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setTencentProviderProfileEnv(t)
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
				if err != nil || proof.NodeOwnershipState != "target_owned" {
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
	setTencentProviderProfileEnv(t)
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

func TestTencentProviderComputeClaimRecoveryProofRejectsProtectedSystemIdentityBeforeRead(t *testing.T) {
	setProtectedResourceEnv(t)
	setTencentProviderProfileEnv(t)
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

	allocation, err := provider.SyncComputeAllocation(context.Background(), ComputeAllocation{ID: "compute-alpha", PackageID: "basic", PoolID: "pool-basic-2c4g", InstanceType: "SA5.MEDIUM4", ProviderData: map[string]string{"instanceType": "SA5.MEDIUM4", "cpu": "2", "memoryGb": "4"}})
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
	input := ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", PackageID: "basic", PoolID: "pool-basic-2c4g", NodePoolID: "np-basic", InstanceType: "SA5.MEDIUM4", ProviderData: map[string]string{"instanceType": "SA5.MEDIUM4", "cpu": "2", "memoryGb": "4"}, MachineName: "machine-alpha", InstanceID: "ins-alpha", CVMInstanceID: "ins-alpha", NodeName: "node-alpha", Deadline: "2026-08-16T00:00:00Z"}

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
				return []byte(`{"metadata":{"name":"node-alpha","resourceVersion":"8","labels":{"medopl.cn/workload":"workspace","oplcloud.cn/resource-id":"compute-alpha","oplcloud.cn/account-id":"acct-alpha","oplcloud.cn/workspace-id":"ws-alpha"}},"spec":{"taints":[{"key":"oplcloud.cn/package-id","value":"basic","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`), nil
			}
			return []byte(`{"metadata":{"name":"node-alpha","resourceVersion":"7","labels":{}},"spec":{"taints":[{"key":"oplcloud.cn/package-id","value":"basic","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`), nil
		case "patch":
			events = append(events, "patch")
			if !slices.Equal(args, []string{"patch", "node/node-alpha", "--type=json", "--patch-file=/dev/stdin"}) {
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
				return []byte(`{"metadata":{"name":"node-alpha","resourceVersion":"8","labels":{"medopl.cn/workload":"workspace","oplcloud.cn/resource-id":"compute-alpha","oplcloud.cn/account-id":"acct-alpha","oplcloud.cn/workspace-id":"ws-alpha"}},"spec":{"taints":[{"key":"oplcloud.cn/package-id","value":"basic","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`), nil
			}
			return []byte(`{"metadata":{"name":"node-alpha","resourceVersion":"7","labels":{}},"spec":{"taints":[{"key":"oplcloud.cn/package-id","value":"basic","effect":"NoSchedule"}]},"status":{"addresses":[{"type":"InternalIP","address":"10.0.0.8"}]}}`), nil
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

func TestDestroyComputeAllocationWithoutClaimedMachineFailsClosedWithoutProviderMutation(t *testing.T) {
	provider := NewTencentProvider()
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		t.Fatalf("unexpected provider mutation: %#v", request)
		return provisionerResponse{}, nil
	}

	allocation, err := provider.DestroyComputeAllocation(context.Background(), ComputeAllocation{ID: "compute-alpha", NodePoolID: "np-basic", ProviderRequestID: "local-request-only", Status: "provisioning"})
	if err == nil || err.Error() != "compute_allocation_destroy_identity_required" || allocation.Status != "provisioning" || allocation.Provider != "" {
		t.Fatalf("destroy unclaimed compute = %#v err=%v", allocation, err)
	}
}

func TestTencentProviderDestroyComputeRequiresDurableStableIdentityBeforeProvisioner(t *testing.T) {
	base := canonicalTencentComputeDestroyFixture()
	tests := []struct {
		name   string
		mutate func(*ComputeAllocation)
	}{
		{name: "account missing", mutate: func(allocation *ComputeAllocation) { allocation.AccountID = "" }},
		{name: "workspace missing", mutate: func(allocation *ComputeAllocation) { allocation.WorkspaceID = "" }},
		{name: "package missing", mutate: func(allocation *ComputeAllocation) { allocation.PackageID = "" }},
		{name: "provider missing", mutate: func(allocation *ComputeAllocation) { allocation.Provider = "" }},
		{name: "provider mismatch", mutate: func(allocation *ComputeAllocation) { allocation.Provider = "local-docker" }},
		{name: "machine name only in ProviderData", mutate: func(allocation *ComputeAllocation) {
			allocation.MachineName = ""
			allocation.ProviderData["machineName"] = "machine-alpha"
		}},
		{name: "pool missing", mutate: func(allocation *ComputeAllocation) { allocation.PoolID = "" }},
		{name: "machine name missing", mutate: func(allocation *ComputeAllocation) { allocation.MachineName = "" }},
		{name: "node name missing", mutate: func(allocation *ComputeAllocation) { allocation.NodeName = "" }},
		{name: "node pool missing", mutate: func(allocation *ComputeAllocation) { allocation.NodePoolID = "" }},
		{name: "private IP missing", mutate: func(allocation *ComputeAllocation) { allocation.PrivateIP = "" }},
		{name: "instance identity missing", mutate: func(allocation *ComputeAllocation) { allocation.InstanceID, allocation.CVMInstanceID = "", "" }},
		{name: "instance identity conflict", mutate: func(allocation *ComputeAllocation) { allocation.CVMInstanceID = "ins-other" }},
		{name: "zone missing", mutate: func(allocation *ComputeAllocation) { allocation.Zone = "" }},
		{name: "provider zone missing", mutate: func(allocation *ComputeAllocation) { delete(allocation.ProviderData, "zone") }},
		{name: "zone conflict", mutate: func(allocation *ComputeAllocation) { allocation.ProviderData["zone"] = "na-siliconvalley-2" }},
		{name: "instance type missing", mutate: func(allocation *ComputeAllocation) { allocation.InstanceType = "" }},
		{name: "provider instance type missing", mutate: func(allocation *ComputeAllocation) { delete(allocation.ProviderData, "instanceType") }},
		{name: "instance type conflict", mutate: func(allocation *ComputeAllocation) { allocation.ProviderData["instanceType"] = "SA5.LARGE8" }},
		{name: "CPU malformed", mutate: func(allocation *ComputeAllocation) { allocation.ProviderData["cpu"] = "two" }},
		{name: "CPU nonpositive", mutate: func(allocation *ComputeAllocation) { allocation.ProviderData["cpu"] = "0" }},
		{name: "CPU missing", mutate: func(allocation *ComputeAllocation) { delete(allocation.ProviderData, "cpu") }},
		{name: "memory malformed", mutate: func(allocation *ComputeAllocation) { allocation.ProviderData["memoryGb"] = "four" }},
		{name: "memory nonpositive", mutate: func(allocation *ComputeAllocation) { allocation.ProviderData["memoryGb"] = "0" }},
		{name: "memory missing", mutate: func(allocation *ComputeAllocation) { delete(allocation.ProviderData, "memoryGb") }},
		{name: "missing shape conflicts with provider plan", mutate: func(allocation *ComputeAllocation) {
			delete(allocation.ProviderData, "cpu")
			delete(allocation.ProviderData, "memoryGb")
		}},
		{name: "cluster missing", mutate: func(allocation *ComputeAllocation) { delete(allocation.ProviderData, "clusterId") }},
		{name: "region missing", mutate: func(allocation *ComputeAllocation) { delete(allocation.ProviderData, "region") }},
		{name: "account tag missing", mutate: func(allocation *ComputeAllocation) { delete(allocation.CostTags, "opl_account_id") }},
		{name: "account tag conflict", mutate: func(allocation *ComputeAllocation) { allocation.CostTags["opl_account_id"] = "acct-other" }},
		{name: "workspace tag missing", mutate: func(allocation *ComputeAllocation) { delete(allocation.CostTags, "opl_workspace_id") }},
		{name: "workspace tag conflict", mutate: func(allocation *ComputeAllocation) { allocation.CostTags["opl_workspace_id"] = "ws-other" }},
		{name: "resource tag missing", mutate: func(allocation *ComputeAllocation) { delete(allocation.CostTags, "opl_resource_id") }},
		{name: "resource tag conflict", mutate: func(allocation *ComputeAllocation) { allocation.CostTags["opl_resource_id"] = "compute-other" }},
		{name: "operation tag missing", mutate: func(allocation *ComputeAllocation) { delete(allocation.CostTags, "opl_operation_id") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			allocation := cloneComputeAllocation(base)
			test.mutate(&allocation)
			provider := NewTencentProvider()
			calls := 0
			provider.provision = func(context.Context, provisionerRequest) (provisionerResponse, error) {
				calls++
				return provisionerResponse{}, nil
			}
			if _, err := provider.DestroyComputeAllocation(context.Background(), allocation); err == nil || calls != 0 {
				t.Fatalf("incomplete durable identity reached provisioner: allocation=%#v calls=%d err=%v", allocation, calls, err)
			}
		})
	}
}

func canonicalTencentComputeDestroyFixture() ComputeAllocation {
	return ComputeAllocation{
		ID: "compute-alpha", OperationID: "op-compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", Status: "running", Provider: "tencent-tke",
		PoolID: "pool-basic", NodePoolID: "np-basic", MachineName: "machine-alpha", NodeName: "node-alpha", PrivateIP: "10.0.0.8",
		InstanceID: "ins-alpha", CVMInstanceID: "ins-alpha", InstanceType: "SA5.MEDIUM4", Zone: "na-siliconvalley-1",
		CostTags: map[string]string{
			"opl_account_id": "acct-alpha", "opl_workspace_id": "ws-alpha", "opl_resource_id": "compute-alpha", "opl_operation_id": "owner-compute-alpha",
		},
		ProviderData: map[string]string{
			"clusterId": "cls-alpha", "region": "na-siliconvalley", "instanceType": "SA5.MEDIUM4", "cpu": "2", "memoryGb": "4", "zone": "na-siliconvalley-1",
			"machineType": "NativeCVM", "cvmApplicable": "true",
		},
	}
}

func TestTencentProviderDestroyComputeRejectsMissingShapeWithoutProvisionerCall(t *testing.T) {
	allocation := canonicalTencentComputeDestroyFixture()
	allocation.Status = "destroying"
	allocation.PoolID = "pool-basic-2c4g"
	delete(allocation.ProviderData, "cpu")
	delete(allocation.ProviderData, "memoryGb")
	provider := NewTencentProvider()
	calls := 0
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		calls++
		return provisionerResponse{}, nil
	}
	result, err := provider.DestroyComputeAllocation(context.Background(), allocation)
	if err == nil || err.Error() != "compute_allocation_destroy_identity_required" || calls != 0 || !reflect.DeepEqual(result, allocation) {
		t.Fatalf("missing shape result=%#v calls=%d err=%v", result, calls, err)
	}
}

func TestTencentProviderDestroyComputeRejectsUntrustedResponseWithoutProviderDataPollution(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		data          map[string]string
		wantAttempted bool
	}{
		{name: "cluster drift", data: map[string]string{"clusterId": "cls-other"}},
		{name: "region drift", data: map[string]string{"region": "ap-shanghai"}},
		{name: "shape drift", data: map[string]string{"instanceType": "SA5.LARGE8", "cpu": "4", "memoryGb": "8"}},
		{name: "provider error fields are discarded", data: map[string]string{"providerErrorClass": "unknown", "providerErrorMessage": "untrusted"}, wantAttempted: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			input := canonicalTencentComputeDestroyFixture()
			if testCase.wantAttempted {
				input.Status = "destroying"
			}
			provider := NewTencentProvider()
			provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
				data := map[string]string{
					"deleteMethod": "DeleteClusterMachines", "scaleDown": "true", "deleteMode": "terminate",
					"describeNodePoolRequestId": "req-node-pool", "machineType": "NativeCVM", "cvmApplicable": "true",
				}
				for key, value := range testCase.data {
					data[key] = value
				}
				return provisionerResponse{
					OK: false, ErrorCode: "compute_delete_untrusted", ProviderRequestID: "req-untrusted", CVMStatus: "UNKNOWN",
					MutationCount: 1, InstanceID: request.Allocation.InstanceID, NodePoolID: request.Pool.NodePoolID, NodeName: request.Allocation.NodeName,
					ProviderData: data,
				}, nil
			}
			result, err := provider.DestroyComputeAllocation(context.Background(), input)
			if testCase.wantAttempted {
				_, hasProviderClass := result.ProviderData["providerErrorClass"]
				_, hasProviderMessage := result.ProviderData["providerErrorMessage"]
				if err == nil || err.Error() != "compute_delete_untrusted" || !validTencentComputeDestroyAttemptEvidence(result) ||
					hasProviderClass || hasProviderMessage || !sameComputeDestroyStableIdentity(input, result) {
					t.Fatalf("provider diagnostics entered durable evidence: result=%#v input=%#v err=%v", result, input, err)
				}
				return
			}
			if err == nil || err.Error() != "compute_delete_untrusted" || !reflect.DeepEqual(result, input) {
				t.Fatalf("untrusted response changed canonical resource: result=%#v input=%#v err=%v", result, input, err)
			}
		})
	}
}

func TestTencentProviderDestroyComputePersistsMutationMarkerWhenErrorEvidenceIsIncomplete(t *testing.T) {
	input := canonicalTencentComputeDestroyFixture()
	input.Status = "destroying"
	provider := NewTencentProvider()
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		return provisionerResponse{
			OK: false, ErrorCode: "missing", ProviderRequestID: "req-delete-missing", MutationCount: 1,
			InstanceID: request.Allocation.InstanceID, NodePoolID: request.Pool.NodePoolID, NodeName: request.Allocation.NodeName,
		}, nil
	}

	result, err := provider.DestroyComputeAllocation(context.Background(), input)
	if err == nil || err.Error() != "missing" || !validTencentComputeDestroyAttemptEvidence(result) ||
		result.ProviderData[tencentComputeDestroyMutationCountKey] != "1" ||
		result.ProviderData[tencentComputeDestroyPhaseKey] != tencentComputeDestroyPhaseAttempted ||
		!sameComputeDestroyStableIdentity(input, result) {
		t.Fatalf("incomplete mutation evidence was not persisted safely: result=%#v input=%#v err=%v", result, input, err)
	}
}

func TestTencentServiceRestartKeepsCanonicalResourceAfterUntrustedDestroyResponse(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryOperationStore()
	resource := canonicalTencentComputeDestroyFixture()
	resource.ProviderRequestID = "req-create-machine"
	seedTencentComputeCreateOperation(t, store, resource)
	provider := NewTencentProvider()
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		return provisionerResponse{
			OK: false, ErrorCode: "compute_delete_untrusted", ProviderRequestID: "req-untrusted", MutationCount: 2,
			InstanceID: request.Allocation.InstanceID, NodePoolID: request.Pool.NodePoolID, NodeName: request.Allocation.NodeName,
			ProviderData: map[string]string{
				"clusterId": "cls-other", "region": "ap-shanghai", "instanceType": "SA5.LARGE8", "cpu": "4", "memoryGb": "8",
				"machineType": "NativeCVM", "cvmApplicable": "true", "deleteMethod": "DeleteClusterMachines", "scaleDown": "true", "deleteMode": "terminate",
				"describeNodePoolRequestId": "req-node-pool", "providerErrorMessage": "untrusted",
			},
		}, nil
	}
	first := NewServiceWithOperationStore(provider, store)
	if _, err := first.DestroyComputeAllocation(ctx, resource.ID); err != nil {
		t.Fatal(err)
	}
	waitForOperation(t, first, "destroy_compute_allocation", "compute_allocation", resource.ID, "failed")
	failed, ok := first.GetComputeAllocation(ctx, resource.ID)
	if !ok || !sameComputeDestroyStableIdentity(resource, failed) || failed.ProviderData["clusterId"] != resource.ProviderData["clusterId"] ||
		failed.ProviderData[tencentComputeDestroyPhaseKey] != tencentComputeDestroyPhaseDispatchAuthorized {
		t.Fatalf("same-process canonical resource changed: failed=%#v ok=%v", failed, ok)
	}

	restored, ok := NewServiceWithOperationStore(provider, store).GetComputeAllocation(ctx, resource.ID)
	if !ok || !reflect.DeepEqual(restored, failed) {
		t.Fatalf("restart changed canonical resource: restored=%#v failed=%#v ok=%v", restored, failed, ok)
	}
}

func TestTencentProviderDestroyComputePreservesAuthoritativeAbsenceEvidence(t *testing.T) {
	provider := NewTencentProvider()
	kubectlCalled := false
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Action != "destroy_compute_allocation" || request.Region != "na-siliconvalley" || request.Pool.ClusterID != "cls-alpha" ||
			request.Allocation.InstanceID != "ins-alpha" || request.Allocation.MachineName != "machine-alpha" {
			t.Fatalf("compute destroy request=%#v", request)
		}
		machinePresent := false
		return provisionerResponse{
			OK: true, NodePoolID: request.Pool.NodePoolID, InstanceID: "ins-alpha", NodeName: request.Allocation.NodeName,
			MachinePresent: &machinePresent, CVMStatus: "NOT_FOUND", TKEStatus: "NOT_FOUND", Status: "external_deleted", ProviderRequestID: "req-delete-machine",
			ProviderData: map[string]string{"describeNodePoolRequestId": "req-node-pool", "verifyMachineDeletedReqId": "req-machine-absent", "describeCvmRequestId": "req-cvm-absent", "machinePresent": "false", "tkeStatus": "NOT_FOUND", "cvmStatus": "NOT_FOUND", "machineType": "NativeCVM", "cvmApplicable": "true"},
		}, nil
	}
	provider.kubectl = func(context.Context, []string, []byte) ([]byte, error) {
		kubectlCalled = true
		return nil, nil
	}
	allocation, err := provider.DestroyComputeAllocation(context.Background(), canonicalTencentComputeDestroyFixture())
	if err != nil || !kubectlCalled || allocation.Status != "external_deleted" || allocation.CVMStatus != "NOT_FOUND" || allocation.ProviderRequestID != "req-delete-machine" || allocation.ProviderData["verifyMachineDeletedReqId"] != "req-machine-absent" || allocation.ProviderData["describeCvmRequestId"] != "req-cvm-absent" {
		t.Fatalf("destroyed allocation=%#v err=%v", allocation, err)
	}
}

func TestTencentProviderDestroyComputeConfigDriftDoesNotCleanupRuntime(t *testing.T) {
	provider := NewTencentProvider()
	input := canonicalTencentComputeDestroyFixture()
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Region != input.ProviderData["region"] || request.Pool.ClusterID != input.ProviderData["clusterId"] {
			t.Fatalf("persisted provider identity missing from request: %#v", request)
		}
		return provisionerResponse{OK: false, ErrorCode: "compute_provider_config_identity_mismatch", MutationCount: 0}, nil
	}
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		t.Fatalf("provider config drift reached runtime cleanup: %#v", args)
		return nil, nil
	}
	result, err := provider.DestroyComputeAllocation(context.Background(), input)
	if err == nil || err.Error() != "compute_provider_config_identity_mismatch" || !reflect.DeepEqual(result, input) {
		t.Fatalf("config drift result=%#v err=%v", result, err)
	}
}

func TestTencentProviderDestroyComputeUsesAuthoritativeMachineApplicabilityFacts(t *testing.T) {
	for _, test := range []struct {
		name              string
		persistedData     map[string]string
		responseData      map[string]string
		legacyIdentity    bool
		cvmStatus         string
		wantOK            bool
		wantProvisionCall bool
		wantError         string
	}{
		{
			name: "legacy Native ins identity", persistedData: map[string]string{"machineType": "Native", "cvmApplicable": "false"}, legacyIdentity: true,
			responseData: map[string]string{"machineType": "Native", "cvmApplicable": "false", "machinePresent": "false", "tkeStatus": "NOT_FOUND", "describeNodePoolRequestId": "req-node-pool", "verifyMachineDeletedReqId": "req-machine-absent"},
			wantOK:       true, wantProvisionCall: true,
		},
		{name: "missing machine type", persistedData: map[string]string{"machineType": "", "cvmApplicable": "true"}, wantError: "compute_allocation_destroy_identity_required"},
		{name: "missing applicability", persistedData: map[string]string{"machineType": "NativeCVM", "cvmApplicable": ""}, wantError: "compute_allocation_destroy_identity_required"},
		{name: "malformed applicability", persistedData: map[string]string{"machineType": "NativeCVM", "cvmApplicable": "yes"}, wantError: "compute_allocation_destroy_identity_required"},
		{
			name: "NativeCVM response conflict", persistedData: map[string]string{"machineType": "NativeCVM", "cvmApplicable": "true"}, cvmStatus: "NOT_FOUND",
			responseData:      map[string]string{"machineType": "NativeCVM", "cvmApplicable": "false", "machinePresent": "false", "tkeStatus": "NOT_FOUND", "describeNodePoolRequestId": "req-node-pool", "verifyMachineDeletedReqId": "req-machine-absent"},
			wantProvisionCall: true, wantError: "compute_allocation_destroy_readback_mismatch",
		},
		{
			name: "legacy response conflict", persistedData: map[string]string{"machineType": "CXM", "cvmApplicable": "false"}, legacyIdentity: true,
			responseData:      map[string]string{"machineType": "CXM", "cvmApplicable": "true", "machinePresent": "false", "tkeStatus": "NOT_FOUND", "describeNodePoolRequestId": "req-node-pool", "verifyMachineDeletedReqId": "req-machine-absent"},
			wantProvisionCall: true, wantError: "compute_allocation_destroy_readback_mismatch",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := NewTencentProvider()
			provisionCalls := 0
			provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
				provisionCalls++
				machinePresent := false
				return provisionerResponse{OK: true, NodePoolID: request.Pool.NodePoolID, InstanceID: "ins-legacy-1", NodeName: request.Allocation.NodeName,
					MachinePresent: &machinePresent, CVMStatus: test.cvmStatus, TKEStatus: "NOT_FOUND", Status: "external_deleted", ProviderRequestID: "req-delete-machine", ProviderData: test.responseData}, nil
			}
			provider.kubectl = func(context.Context, []string, []byte) ([]byte, error) { return nil, nil }
			input := canonicalTencentComputeDestroyFixture()
			input.InstanceID, input.CVMInstanceID = "ins-legacy-1", "ins-legacy-1"
			if test.legacyIdentity {
				input.CVMInstanceID = ""
			}
			for key, value := range test.persistedData {
				if value == "" {
					delete(input.ProviderData, key)
				} else {
					input.ProviderData[key] = value
				}
			}
			allocation, err := provider.DestroyComputeAllocation(context.Background(), input)
			if test.wantOK {
				if err != nil || allocation.Status != "external_deleted" || allocation.CVMInstanceID != "" || allocation.ProviderData["machineType"] != "Native" || provisionCalls != 1 {
					t.Fatalf("legacy allocation=%#v err=%v", allocation, err)
				}
				return
			}
			wantCalls := 0
			if test.wantProvisionCall {
				wantCalls = 1
			}
			if err == nil || err.Error() != test.wantError || provisionCalls != wantCalls || !reflect.DeepEqual(allocation, input) {
				t.Fatalf("invalid facts allocation=%#v err=%v calls=%d", allocation, err, provisionCalls)
			}
		})
	}
}

func TestTencentProviderDestroyComputeRetriesOnlyRuntimeCleanupAfterCloudAbsence(t *testing.T) {
	provider := NewTencentProvider()
	provisionCalls := 0
	kubectlCalls := 0
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		provisionCalls++
		machinePresent := false
		return provisionerResponse{
			OK: true, NodePoolID: request.Pool.NodePoolID, InstanceID: "ins-alpha", NodeName: request.Allocation.NodeName,
			MachinePresent: &machinePresent, CVMStatus: "NOT_FOUND", TKEStatus: "NOT_FOUND", Status: "external_deleted", ProviderRequestID: "req-delete-machine",
			ProviderData: map[string]string{"machineType": "NativeCVM", "cvmApplicable": "true", "machinePresent": "false", "tkeStatus": "NOT_FOUND", "cvmStatus": "NOT_FOUND", "describeNodePoolRequestId": "req-node-pool", "verifyMachineDeletedReqId": "req-machine-absent", "describeCvmRequestId": "req-cvm-absent"},
		}, nil
	}
	provider.kubectl = func(context.Context, []string, []byte) ([]byte, error) {
		kubectlCalls++
		if kubectlCalls == 1 {
			return nil, errors.New("runtime cleanup failed")
		}
		return nil, nil
	}
	input := canonicalTencentComputeDestroyFixture()
	first, firstErr := provider.DestroyComputeAllocation(context.Background(), input)
	if firstErr == nil || first.ID != input.ID || first.Status != "external_deleted" || first.ProviderRequestID != "req-delete-machine" || first.ProviderData["describeCvmRequestId"] != "req-cvm-absent" ||
		!sameComputeDestroyStableIdentity(input, first) {
		t.Fatalf("first cleanup result=%#v err=%v", first, firstErr)
	}
	second, secondErr := provider.DestroyComputeAllocation(context.Background(), first)
	if secondErr != nil || second.Status != "external_deleted" || provisionCalls != 1 || kubectlCalls != 2 || !sameComputeDestroyStableIdentity(input, second) {
		t.Fatalf("second cleanup result=%#v err=%v provision=%d kubectl=%d", second, secondErr, provisionCalls, kubectlCalls)
	}
}

func TestServiceRetriesTencentRuntimeCleanupFromPersistedCloudAbsence(t *testing.T) {
	provider := NewTencentProvider()
	var callsMu sync.Mutex
	provisionCalls := 0
	kubectlCalls := 0
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		callsMu.Lock()
		provisionCalls++
		callsMu.Unlock()
		machinePresent := false
		return provisionerResponse{
			OK: true, NodePoolID: request.Pool.NodePoolID, InstanceID: "ins-alpha", NodeName: request.Allocation.NodeName,
			MachinePresent: &machinePresent, CVMStatus: "NOT_FOUND", TKEStatus: "NOT_FOUND", Status: "external_deleted", ProviderRequestID: "req-delete-machine",
			ProviderData: map[string]string{"machineType": "NativeCVM", "cvmApplicable": "true", "machinePresent": "false", "tkeStatus": "NOT_FOUND", "cvmStatus": "NOT_FOUND", "describeNodePoolRequestId": "req-node-pool", "verifyMachineDeletedReqId": "req-machine-absent", "describeCvmRequestId": "req-cvm-absent"},
		}, nil
	}
	provider.kubectl = func(context.Context, []string, []byte) ([]byte, error) {
		callsMu.Lock()
		defer callsMu.Unlock()
		kubectlCalls++
		if kubectlCalls == 1 {
			return nil, errors.New("runtime cleanup failed")
		}
		return nil, nil
	}
	store := NewMemoryOperationStore()
	resource := canonicalTencentComputeDestroyFixture()
	resource.ProviderRequestID = "req-create-machine"
	seedTencentComputeCreateOperation(t, store, resource)
	service := NewServiceWithOperationStore(provider, store)
	if _, _, err := store.ClaimMachine(context.Background(), MachineOwnership{
		ID: "owner-alpha", ResourceID: resource.ID, AccountID: resource.AccountID, WorkspaceID: resource.WorkspaceID, PackageID: resource.PackageID,
		NodePoolID: resource.NodePoolID, MachineID: resource.MachineName, InstanceID: resource.InstanceID, NodeName: resource.NodeName, Status: "active",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := service.DestroyComputeAllocation(context.Background(), resource.ID); err != nil {
		t.Fatal(err)
	}
	waitForOperation(t, service, "destroy_compute_allocation", "compute_allocation", resource.ID, "failed")
	current, _ := service.GetComputeAllocation(context.Background(), resource.ID)
	ownership, ownershipErr := store.MachineOwnership(context.Background(), resource.ID)
	if current.Status != "external_deleted" || current.ProviderData["describeCvmRequestId"] != "req-cvm-absent" || ownershipErr != nil || ownership.Status == "released" || ownership.ReleasedAt != nil {
		t.Fatalf("failed cleanup current=%#v ownership=%#v ownershipErr=%v", current, ownership, ownershipErr)
	}
	operations, err := service.ListOperations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	failedEvidence := ComputeAllocation{}
	for _, operation := range operations {
		if operation.Action == "destroy_compute_allocation" && operation.ResourceID == resource.ID && operation.Status == "failed" {
			_ = decodeOperationResource(operation, &failedEvidence)
		}
	}
	if failedEvidence.Status != "external_deleted" || failedEvidence.ProviderRequestID != "req-delete-machine" {
		t.Fatalf("failed operation lost provider absence evidence: %#v", failedEvidence)
	}

	restarted := NewServiceWithOperationStore(provider, store)
	restored, ok := restarted.GetComputeAllocation(context.Background(), resource.ID)
	if !ok || restored.Status != "external_deleted" || restored.ProviderRequestID != "req-delete-machine" || restored.ProviderData["describeCvmRequestId"] != "req-cvm-absent" {
		t.Fatalf("restart lost authoritative cloud absence: restored=%#v ok=%v", restored, ok)
	}
	ownership, ownershipErr = store.MachineOwnership(context.Background(), resource.ID)
	if ownershipErr != nil || ownership.Status == "released" || ownership.ReleasedAt != nil {
		t.Fatalf("restart released ownership before runtime cleanup: ownership=%#v err=%v", ownership, ownershipErr)
	}
	if _, err := restarted.DestroyComputeAllocation(context.Background(), resource.ID); err != nil {
		t.Fatal(err)
	}
	waitForOperation(t, restarted, "destroy_compute_allocation", "compute_allocation", resource.ID, "succeeded")
	final, _ := restarted.GetComputeAllocation(context.Background(), resource.ID)
	ownership, ownershipErr = store.MachineOwnership(context.Background(), resource.ID)
	callsMu.Lock()
	gotProvisionCalls, gotKubectlCalls := provisionCalls, kubectlCalls
	callsMu.Unlock()
	if final.Status != "external_deleted" || ownershipErr != nil || ownership.Status != "released" || ownership.ReleasedAt == nil || gotProvisionCalls != 1 || gotKubectlCalls != 2 {
		t.Fatalf("final=%#v ownership=%#v ownershipErr=%v provision=%d kubectl=%d", final, ownership, ownershipErr, gotProvisionCalls, gotKubectlCalls)
	}
}

func TestTencentFailedDestroyReplayRequiresExactAuthoritativeAbsenceEvidence(t *testing.T) {
	type replayMutation struct {
		previous  func(*ComputeAllocation)
		failed    func(*ComputeAllocation)
		operation func(*FabricOperation)
	}
	tests := []struct {
		name         string
		mutation     replayMutation
		noPrevious   bool
		wantExternal bool
	}{
		{name: "valid NativeCVM", wantExternal: true},
		{name: "no previous", noPrevious: true},
		{name: "missing machine presence", mutation: replayMutation{failed: func(resource *ComputeAllocation) { delete(resource.ProviderData, "machinePresent") }}},
		{name: "malformed applicability", mutation: replayMutation{failed: func(resource *ComputeAllocation) { resource.ProviderData["cvmApplicable"] = "yes" }}},
		{name: "operation account drift", mutation: replayMutation{operation: func(operation *FabricOperation) { operation.AccountID = "acct-other" }}},
		{name: "resource workspace drift", mutation: replayMutation{failed: func(resource *ComputeAllocation) { resource.WorkspaceID = "ws-other" }}},
		{name: "package drift", mutation: replayMutation{failed: func(resource *ComputeAllocation) { resource.PackageID = "pro" }}},
		{name: "provider drift", mutation: replayMutation{failed: func(resource *ComputeAllocation) { resource.Provider = "local-docker" }}},
		{name: "provider resource drift", mutation: replayMutation{failed: func(resource *ComputeAllocation) { resource.ProviderResourceID = "ins-other" }}},
		{name: "pool drift", mutation: replayMutation{failed: func(resource *ComputeAllocation) { resource.PoolID = "pool-other" }}},
		{name: "node pool drift", mutation: replayMutation{failed: func(resource *ComputeAllocation) { resource.NodePoolID = "np-other" }}},
		{name: "machine drift", mutation: replayMutation{failed: func(resource *ComputeAllocation) { resource.MachineName = "machine-other" }}},
		{name: "instance drift", mutation: replayMutation{failed: func(resource *ComputeAllocation) { resource.InstanceID = "ins-other" }}},
		{name: "CVM instance drift", mutation: replayMutation{failed: func(resource *ComputeAllocation) { resource.CVMInstanceID = "ins-other" }}},
		{name: "node drift", mutation: replayMutation{failed: func(resource *ComputeAllocation) { resource.NodeName = "node-other" }}},
		{name: "private IP drift", mutation: replayMutation{failed: func(resource *ComputeAllocation) { resource.PrivateIP = "10.0.0.99" }}},
		{name: "public IP drift", mutation: replayMutation{failed: func(resource *ComputeAllocation) { resource.PublicIP = "203.0.113.99" }}},
		{name: "service name drift", mutation: replayMutation{failed: func(resource *ComputeAllocation) { resource.ServiceName = "opl-compute-other" }}},
		{name: "operation ID drift", mutation: replayMutation{failed: func(resource *ComputeAllocation) { resource.OperationID = "op-create-other" }}},
		{name: "cost tags drift", mutation: replayMutation{failed: func(resource *ComputeAllocation) {
			resource.CostTags = maps.Clone(resource.CostTags)
			resource.CostTags["opl_workspace_id"] = "ws-other"
		}}},
		{name: "node selector drift", mutation: replayMutation{failed: func(resource *ComputeAllocation) {
			resource.NodeSelector = map[string]any{"kubernetes.io/hostname": "node-other"}
		}}},
		{name: "created at drift", mutation: replayMutation{failed: func(resource *ComputeAllocation) { resource.CreatedAt = resource.CreatedAt.Add(time.Second) }}},
		{name: "claim terminal evidence drift", mutation: replayMutation{failed: func(resource *ComputeAllocation) {
			resource.ClaimTerminalEvidence = &ComputeClaimTerminalEvidence{SchemaVersion: 2}
		}}},
		{name: "stable ProviderData drift", mutation: replayMutation{failed: func(resource *ComputeAllocation) { resource.ProviderData["clusterId"] = "cls-other" }}},
		{name: "stable ProviderData removed", mutation: replayMutation{failed: func(resource *ComputeAllocation) { delete(resource.ProviderData, "region") }}},
		{name: "source and failed missing durable region", mutation: replayMutation{previous: func(resource *ComputeAllocation) { delete(resource.ProviderData, "region") }}},
		{name: "source and failed missing durable CPU", mutation: replayMutation{previous: func(resource *ComputeAllocation) { delete(resource.ProviderData, "cpu") }}},
		{name: "unknown ProviderData added", mutation: replayMutation{failed: func(resource *ComputeAllocation) { resource.ProviderData["deleteReadbackMaybe"] = "absent" }}},
		{name: "missing delete request", mutation: replayMutation{failed: func(resource *ComputeAllocation) { resource.ProviderRequestID = "" }}},
		{name: "valid legacy Native", mutation: replayMutation{
			previous: func(resource *ComputeAllocation) {
				resource.CVMInstanceID, resource.CVMStatus = "", ""
				resource.ProviderData["machineType"] = "Native"
				resource.ProviderData["cvmApplicable"] = "false"
			},
			failed: func(resource *ComputeAllocation) {
				resource.CVMStatus = ""
				resource.ProviderData["machineType"] = "Native"
				resource.ProviderData["cvmApplicable"] = "false"
				resource.ProviderData["describeCvmRequestId"] = "req-create-cvm"
				delete(resource.ProviderData, "cvmStatus")
			},
		}, wantExternal: true},
		{name: "legacy forged CVM absence", mutation: replayMutation{
			previous: func(resource *ComputeAllocation) { resource.CVMInstanceID, resource.CVMStatus = "", "" },
			failed: func(resource *ComputeAllocation) {
				resource.CVMStatus = "NOT_FOUND"
				resource.ProviderData["machineType"] = "CXM"
				resource.ProviderData["cvmApplicable"] = "false"
				resource.ProviderData["describeCvmRequestId"] = "req-create-cvm"
				resource.ProviderData["cvmStatus"] = "NOT_FOUND"
			},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemoryOperationStore()
			created := ComputeAllocation{
				ID: "compute-alpha", OperationID: "op-create-compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", Status: "running", Provider: "tencent-tke",
				ProviderResourceID: "ins-alpha", ProviderRequestID: "req-create-machine", PoolID: "pool-basic", NodePoolID: "np-basic", InstanceID: "ins-alpha", CVMInstanceID: "ins-alpha",
				MachineName: "machine-alpha", NodeName: "node-alpha", PrivateIP: "10.0.0.8", PublicIP: "203.0.113.8", InstanceType: "SA5.MEDIUM4", Zone: "ap-guangzhou-3",
				CVMStatus: "RUNNING", ChargeType: "PREPAID", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2026-09-20T00:00:00Z", ServiceName: "opl-compute-alpha",
				NodeSelector: map[string]any{"kubernetes.io/hostname": "node-alpha"},
				ProviderData: map[string]string{
					"clusterId": "cls-alpha", "region": "ap-guangzhou", "describeNodePoolRequestId": "req-create-node-pool", "describeCvmRequestId": "req-create-cvm",
					"instanceType": "SA5.MEDIUM4", "cpu": "2", "memoryGb": "4", "machineName": "machine-alpha", "zone": "ap-guangzhou-3",
					"machineType": "NativeCVM", "cvmApplicable": "true",
				},
				CostTags:              map[string]string{"opl_account_id": "acct-alpha", "opl_workspace_id": "ws-alpha", "opl_resource_id": "compute-alpha", "opl_operation_id": "owner-compute-alpha"},
				ClaimTerminalEvidence: &ComputeClaimTerminalEvidence{SchemaVersion: 1}, CreatedAt: time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC),
			}
			if test.mutation.previous != nil {
				test.mutation.previous(&created)
			}
			if !test.noPrevious {
				seedTencentComputeCreateOperation(t, store, created)
			}
			deleted := created
			deleted.Status = "external_deleted"
			deleted.ProviderRequestID = "req-delete-machine"
			deleted.CVMStatus = "NOT_FOUND"
			deleted.ProviderData = maps.Clone(created.ProviderData)
			for key, value := range map[string]string{
				"machineType": "NativeCVM", "cvmApplicable": "true", "machinePresent": "false", "tkeStatus": "NOT_FOUND", "cvmStatus": "NOT_FOUND", "deleteMethod": "DeleteClusterMachines",
				"scaleDown": "true", "deleteMode": "terminate", "describeNodePoolRequestId": "req-node-pool", "modifySelfProvisioningReqId": "req-disable-self-provisioning",
				"verifyMachineDeletedReqId": "req-machine-absent", "describeCvmRequestId": "req-cvm-absent",
			} {
				deleted.ProviderData[key] = value
			}
			if test.mutation.failed != nil {
				test.mutation.failed(&deleted)
			}
			now := time.Date(2026, 8, 20, 2, 0, 0, 0, time.UTC)
			failed := newOperation("destroy_compute_allocation", "compute_allocation", created.ID, created.AccountID, created.WorkspaceID, "", hashInput(map[string]string{"id": created.ID}), now)
			failed.ID, failed.Status, failed.CreatedAt, failed.FinishedAt = "fop-destroy-failed", "failed", now, now
			fillOperationResource(&failed, deleted)
			if test.mutation.operation != nil {
				test.mutation.operation(&failed)
			}
			if err := store.Append(context.Background(), failed); err != nil {
				t.Fatal(err)
			}

			replayed, ok := NewServiceWithOperationStore(NewTencentProvider(), store).GetComputeAllocation(context.Background(), created.ID)
			if test.noPrevious {
				if ok {
					t.Fatalf("failed destroy without previous create gained replay authority: %#v", replayed)
				}
				return
			}
			if test.wantExternal {
				if !ok || replayed.Status != "external_deleted" {
					t.Fatalf("replayed=%#v ok=%v want external_deleted", replayed, ok)
				}
				return
			}
			if !ok || !reflect.DeepEqual(replayed, created) {
				t.Fatalf("invalid failed destroy overrode previous create: replayed=%#v previous=%#v ok=%v", replayed, created, ok)
			}
		})
	}
}

func seedTencentComputeCreateOperation(t *testing.T, store OperationStore, resource ComputeAllocation) {
	t.Helper()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	operation := newOperation("create_compute_allocation", "compute_allocation", resource.ID, resource.AccountID, resource.WorkspaceID, "seed-create-"+resource.ID, hashInput(resource), now)
	operation.ID, operation.Status, operation.CreatedAt, operation.FinishedAt = "fop-create-"+stableSuffix(resource.ID)[:12], "succeeded", now, now
	fillOperationResource(&operation, resource)
	if err := store.Append(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
}

func TestServiceKeepsOrdinaryTencentDestroyFailureInDestroyingState(t *testing.T) {
	provider := NewTencentProvider()
	provider.provision = func(context.Context, provisionerRequest) (provisionerResponse, error) {
		return provisionerResponse{}, errors.New("provider unavailable")
	}
	provider.kubectl = func(context.Context, []string, []byte) ([]byte, error) {
		t.Fatal("ordinary provider failure must not reach runtime cleanup")
		return nil, nil
	}
	service := NewServiceWithOperationStore(provider, NewMemoryOperationStore())
	resource := ComputeAllocation{
		ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", Status: "running", ProviderRequestID: "req-existing",
		MachineName: "machine-alpha", InstanceID: "ins-alpha", NodeName: "node-alpha",
	}
	service.computes[resource.ID] = resource
	if _, err := service.DestroyComputeAllocation(context.Background(), resource.ID); err != nil {
		t.Fatal(err)
	}
	waitForOperation(t, service, "destroy_compute_allocation", "compute_allocation", resource.ID, "failed")
	current, _ := service.GetComputeAllocation(context.Background(), resource.ID)
	if current.Status != "destroying" || current.ProviderData["machineType"] != "" {
		t.Fatalf("ordinary provider failure current=%#v", current)
	}
}

func TestDestroyExternallyDeletedComputeRequiresAuthoritativeReadbackBeforeCleanup(t *testing.T) {
	provider := NewTencentProvider()
	provisionCalls := 0
	kubectlCalled := false
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		provisionCalls++
		if request.Action != "read_compute_destroy_status" || request.Region != "na-siliconvalley" || request.Pool.ClusterID != "cls-alpha" || request.Allocation.MachineType != "NativeCVM" {
			t.Fatalf("unexpected provider request: %#v", request)
		}
		machinePresent := false
		return provisionerResponse{
			OK: true, MutationCount: 0, InstanceID: request.Allocation.InstanceID, MachinePresent: &machinePresent,
			TKEStatus: "NOT_FOUND", CVMStatus: "NOT_FOUND", Status: "absent", ProviderRequestID: "req-destroy-readback",
			ProviderData: map[string]string{
				"clusterId": "cls-alpha", "region": "na-siliconvalley", "nodePoolId": "np-basic", "machineName": "machine-alpha",
				"nodeName": "node-alpha", "privateIp": "10.0.0.8", "machineType": "NativeCVM", "cvmApplicable": "true",
				"syncResult": "missing", "machinePresent": "false", "tkeStatus": "NOT_FOUND", "cvmStatus": "NOT_FOUND",
				"describeClusterMachinesReq": "req-tke-absence", "describeCvmRequestId": "req-cvm-absence",
			},
		}, nil
	}
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		kubectlCalled = true
		if !slices.Equal(args, []string{"delete", "deployment/opl-compute-alpha", "service/opl-compute-alpha", "secret/opl-compute-alpha-env", "--ignore-not-found=true", "--wait=true"}) {
			t.Fatalf("unexpected runtime cleanup: %#v", args)
		}
		return nil, nil
	}

	input := canonicalTencentComputeDestroyFixture()
	input.Status = "external_deleted"
	allocation, err := provider.DestroyComputeAllocation(context.Background(), input)
	if err != nil || allocation.Status != "external_deleted" || !validTencentComputeAbsenceEvidence(allocation) || provisionCalls != 1 || !kubectlCalled {
		t.Fatalf("destroy externally deleted compute = %#v err=%v", allocation, err)
	}
}

func TestDestroyExternallyDeletedComputeRefusesCleanupWhileMachineIsPresent(t *testing.T) {
	provider := NewTencentProvider()
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		machinePresent := true
		return provisionerResponse{
			OK: true, MutationCount: 0, InstanceID: request.Allocation.InstanceID, MachinePresent: &machinePresent, Status: "running",
			ProviderData: map[string]string{
				"clusterId": "cls-alpha", "region": "na-siliconvalley", "nodePoolId": "np-basic", "machineName": "machine-alpha",
				"nodeName": "node-alpha", "privateIp": "10.0.0.8", "machineType": "NativeCVM", "cvmApplicable": "true",
			},
		}, nil
	}
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		t.Fatalf("unconfirmed external deletion reached runtime cleanup: %#v", args)
		return nil, nil
	}
	input := canonicalTencentComputeDestroyFixture()
	input.Status = "external_deleted"
	allocation, err := provider.DestroyComputeAllocation(context.Background(), input)
	if err == nil || err.Error() != "compute_destroy_absence_evidence_invalid" || !reflect.DeepEqual(allocation, input) {
		t.Fatalf("unconfirmed external deletion = %#v err=%v", allocation, err)
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
	if len(tolerations) != 1 {
		t.Fatalf("workspace pod must carry only the exact package toleration: %#v", tolerations)
	}
	workspaceToleration := tolerations[0].(map[string]any)
	if workspaceToleration["key"] != "oplcloud.cn/package-id" || workspaceToleration["operator"] != "Equal" || workspaceToleration["value"] != "basic" || workspaceToleration["effect"] != "NoSchedule" {
		t.Fatalf("workspace pod must tolerate only its exact package node taint: %#v", workspaceToleration)
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
		case len(args) == 5 && args[0] == "get" && strings.HasPrefix(args[1], "secret/") && args[2] == "--ignore-not-found" && args[3] == "-o" && args[4] == "json":
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
	setTencentProviderProfileEnv(t)
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
		if slices.Equal(args, []string{"get", "deployment,service,networkpolicy,secret", "-l", "oplcloud.cn/workspace-id=ws-alpha", "-o", "json"}) {
			return mustJSON(map[string]any{"kind": "List", "items": []any{deployment, service, networkPolicy, secret}}), nil
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
	runtime, err := provider.CreateWorkspaceRuntime(context.Background(), input, ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", PackageID: "basic", ServiceName: "opl-compute-alpha"}, StorageVolume{ID: "storage-alpha", ProviderResourceID: "pvc/opl-storage-alpha-data"})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	replayed, replayErr := provider.CreateWorkspaceRuntime(context.Background(), input, ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", PackageID: "basic", ServiceName: "opl-compute-alpha"}, StorageVolume{ID: "storage-alpha", ProviderResourceID: "pvc/opl-storage-alpha-data"})
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
		if len(args) == 6 && args[0] == "get" && args[1] == "deployment,service,networkpolicy,secret" && args[2] == "-l" && args[3] == "oplcloud.cn/workspace-id=ws-alpha" {
			return mustJSON(map[string]any{"kind": "List", "items": []any{deployment, service, networkPolicy,
				map[string]any{"kind": "Secret", "metadata": map[string]any{"name": "opl-compute-alpha-env", "labels": map[string]any{"oplcloud.cn/workspace-id": "ws-alpha"}}},
			}}), nil
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
				if len(args) >= 2 && args[0] == "get" && args[1] == "deployment,service,networkpolicy,secret" {
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

func TestTencentProviderCreatesStaticRetainedCBSVolumeInComputeZone(t *testing.T) {
	provider := NewTencentProvider()
	var provisioned provisionerRequest
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		provisioned = request
		return provisionerResponse{
			OK: true, StorageVolumeID: "disk-storage-alpha", CBSStatus: "UNATTACHED", Status: "provider_ready", ProviderRequestID: "req-create-cbs",
			ProviderData: map[string]string{"diskType": "CLOUD_BSSD", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": "2026-08-16 00:00:00", "zone": "ap-guangzhou-3", "sizeGb": "10", "region": "ap-guangzhou"},
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

	if err != nil || volume.ProviderResourceID != "disk-storage-alpha" || volume.Status != "pending" || volume.Zone != "ap-guangzhou-3" || volume.Deadline != "2026-08-16 00:00:00" || volume.ProviderData["region"] != "ap-guangzhou" {
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
			ProviderData: map[string]string{"diskType": "CLOUD_BSSD", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": "2026-08-16T00:00:00Z", "zone": "ap-guangzhou-3", "sizeGb": "10", "diskChargeType": "PREPAID", "region": "ap-guangzhou"},
		}, nil
	}
	volume := StorageVolume{
		ID: "storage-staged-alpha", OperationID: "launch-staged:storage", AccountID: "acct-staged", WorkspaceID: "workspace-staged", Status: "provider_ready",
		Provider: "tencent-tke", ProviderResourceID: "disk-staged-alpha", SizeGB: 10, DiskType: "CLOUD_BSSD", Zone: "ap-guangzhou-3",
		RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2026-08-16T00:00:00Z", CostTags: oplCostTags("acct-staged", "workspace-staged", "storage-staged-alpha", "launch-staged:storage"),
		ProviderData: map[string]string{"pvName": "opl-storage-staged-alpha-pv", "pvcName": "opl-storage-staged-alpha-data", "region": "ap-guangzhou"},
	}
	input := StorageVolumeInput{
		ID: volume.ID, AccountID: volume.AccountID, WorkspaceID: volume.WorkspaceID, ComputeID: "compute-staged-alpha", Zone: volume.Zone, SizeGB: volume.SizeGB,
		IdempotencyKey: volume.OperationID, OperationID: volume.OperationID,
	}

	created, err := provider.CreateCBSVolume(context.Background(), input)
	if err != nil || created.ProviderResourceID != "disk-staged-alpha" {
		t.Fatalf("CBS create=%#v err=%v", created, err)
	}
	if len(actions) != 1 || actions[0] != "create_storage_volume" {
		t.Fatalf("CBS stage actions=%v", actions)
	}

	read, err := provider.ReadCBSVolume(context.Background(), input, created)
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
	bound, err := provider.ApplyStaticStorageBinding(context.Background(), read)
	if err != nil || bound.Status != "ready" || applyCalls != 1 {
		t.Fatalf("static binding=%#v err=%v applyCalls=%d getCalls=%d", bound, err, applyCalls, getCalls)
	}
	readBound, err := provider.ReadStaticStorageBinding(context.Background(), bound)
	if err != nil || readBound.Status != "ready" || applyCalls != 1 || getCalls == 0 {
		t.Fatalf("static readback=%#v err=%v applyCalls=%d getCalls=%d", readBound, err, applyCalls, getCalls)
	}
}

func TestTencentProviderCBSResponseLossRecoversFromDurablePredispatchRegion(t *testing.T) {
	input := StorageVolumeInput{
		ID: "storage-response-loss", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ComputeID: "compute-alpha",
		Zone: "ap-guangzhou-3", SizeGB: 10, IdempotencyKey: "launch-alpha:storage", OperationID: "op_create_storage_volume_fixture",
	}
	plan := tencentWorkspacePlan{
		PackageID: "basic", Compute: ComputePlan{ID: "pool-basic"}, NodePoolID: "np-basic", MaxReplicas: 20,
		Region: "ap-guangzhou", Zone: input.Zone, Storage: tencentStoragePlan{SizeGB: input.SizeGB, DiskType: "CLOUD_BSSD"},
	}
	parent := testWorkspaceLaunchBinding("storage", "ensure_storage", input.OperationID)
	operation := newOperation(parent.Action, "workspace_launch_stage", parent.FabricOperationID, parent.AccountID, parent.WorkspaceID, parent.IdempotencyKey, parent.RequestHash, time.Now().UTC())
	operation.ID, operation.OperationID, operation.Status = parent.FabricOperationID, parent.FabricOperationID, "started"
	if err := bindLaunchStageOperation(&operation, &parent); err != nil {
		t.Fatal(err)
	}
	store := NewMemoryOperationStore()
	childID := providerMutationOperationID(parent, "tencent_cbs_create", "storage_volume", input.ID, "")
	createCalls := 0
	firstProvider := NewTencentProvider()
	firstService := NewServiceWithOperationStore(firstProvider, store)
	firstProvider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Action != "create_storage_volume" || request.Region != plan.Region {
			t.Fatalf("first create request=%#v", request)
		}
		createCalls++
		child, err := store.Get(context.Background(), childID)
		var state tencentCBSCreateMutationState
		if err != nil || child.Status != "started" || !decodeProviderMutationState(child, &state) || !state.matches(input) || state.Region != plan.Region {
			t.Fatalf("pre-dispatch child=%#v state=%#v err=%v", child, state, err)
		}
		return provisionerResponse{}, errors.New("simulated CreateDisks response loss")
	}
	firstCtx := withTencentWorkspacePlan(firstService.providerMutationContext(context.Background(), operation), plan)
	failed, err := firstProvider.CreateStorageVolume(firstCtx, input)
	if err == nil || createCalls != 1 || failed.ProviderData["region"] != plan.Region || failed.ProviderResourceID != "" {
		t.Fatalf("response-loss result=%#v err=%v createCalls=%d", failed, err, createCalls)
	}

	t.Setenv(tencentProviderRegionEnv, "ap-shanghai")
	restartedProvider := NewTencentProvider()
	if restartedProvider.region != "ap-shanghai" {
		t.Fatalf("restart region proposal=%q", restartedProvider.region)
	}
	restartedService := NewServiceWithOperationStore(restartedProvider, store)
	var replayActions []string
	restartedProvider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		replayActions = append(replayActions, request.Action)
		if request.Region != plan.Region {
			t.Fatalf("replay used non-durable region: %#v", request)
		}
		providerData := map[string]string{
			"diskType": "CLOUD_BSSD", "diskChargeType": "PREPAID", "renewFlag": "NOTIFY_AND_MANUAL_RENEW",
			"deadline": "2026-09-01T00:00:00Z", "zone": input.Zone, "sizeGb": "10", "region": plan.Region,
		}
		switch request.Action {
		case "discover_storage_volume":
			return provisionerResponse{OK: true, StorageState: "storage_existing_exact", StorageVolumeID: "disk-response-loss", Status: "provider_ready", ProviderRequestID: "req-discover", ProviderData: providerData}, nil
		case "sync_storage_volume":
			if request.Storage.ID != "disk-response-loss" {
				t.Fatalf("sync disk=%q", request.Storage.ID)
			}
			return provisionerResponse{OK: true, StorageVolumeID: "disk-response-loss", Status: "provider_ready", CBSStatus: "UNATTACHED", ProviderRequestID: "req-sync", ProviderData: providerData}, nil
		case "create_storage_volume":
			createCalls++
			return provisionerResponse{}, errors.New("second CreateDisks dispatch")
		default:
			return provisionerResponse{}, fmt.Errorf("unexpected action %q", request.Action)
		}
	}
	expected := StorageVolume{
		ID: input.ID, OperationID: input.IdempotencyKey, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID,
		Provider: "tencent-tke", ProviderResourceID: "disk-response-loss", SizeGB: input.SizeGB, Zone: input.Zone, DiskType: "CLOUD_BSSD",
		CostTags:     oplCostTags(input.AccountID, input.WorkspaceID, input.ID, input.OperationID),
		ProviderData: map[string]string{"pvName": k8sName(input.ID) + "-pv", "pvcName": k8sName(input.ID) + "-data", "region": plan.Region},
	}
	manifest := mustJSON(canonicalTencentStorageBindingObjects(t, expected))
	restartedProvider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		if len(args) > 0 && args[0] == "apply" {
			return nil, nil
		}
		if len(args) > 0 && args[0] == "get" {
			return manifest, nil
		}
		return nil, fmt.Errorf("unexpected kubectl args=%#v", args)
	}
	restartedCtx := withTencentWorkspacePlan(restartedService.providerMutationContext(context.Background(), operation), plan)
	recovered, err := restartedProvider.CreateStorageVolume(restartedCtx, input)
	if err != nil || recovered.ProviderResourceID != "disk-response-loss" || recovered.Status != "ready" || recovered.ProviderData["region"] != plan.Region || createCalls != 1 {
		t.Fatalf("recovered=%#v err=%v createCalls=%d", recovered, err, createCalls)
	}
	if !slices.Equal(replayActions, []string{"discover_storage_volume", "sync_storage_volume"}) {
		t.Fatalf("replay actions=%v", replayActions)
	}
	child, err := store.Get(context.Background(), childID)
	var persisted StorageVolume
	if err != nil || child.Status != "succeeded" || !decodeOperationResource(child, &persisted) || persisted.ProviderData["region"] != plan.Region {
		t.Fatalf("converged child=%#v resource=%#v err=%v", child, persisted, err)
	}
}

func TestTencentServiceStorageCreatePersistsImmutableStateBeforeProviderDispatch(t *testing.T) {
	input := StorageVolumeInput{
		ID: "storage-provider-acceptance", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ComputeID: "compute-provider-acceptance",
		Zone: "ap-guangzhou-3", SizeGB: 10, IdempotencyKey: "provider-acceptance:storage",
	}
	store := NewMemoryOperationStore()
	provider := NewTencentProvider()
	service := NewServiceWithOperationStore(provider, store)
	service.computes[input.ComputeID] = ComputeAllocation{
		ID: input.ComputeID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "ready", Zone: input.Zone,
	}
	providerData := map[string]string{
		"diskType": "CLOUD_BSSD", "diskChargeType": "PREPAID", "renewFlag": "NOTIFY_AND_MANUAL_RENEW",
		"deadline": "2026-09-01T00:00:00Z", "zone": input.Zone, "sizeGb": "10", "region": "ap-guangzhou",
	}
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		switch request.Action {
		case "create_storage_volume":
			operations, err := store.List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			var state tencentCBSCreateMutationState
			found := false
			for _, operation := range operations {
				if operation.Action == "tencent_cbs_create" {
					found = decodeProviderMutationState(operation, &state)
				}
			}
			if !found || state.Region != "ap-guangzhou" || state.DiskType != "CLOUD_BSSD" || state.Zone != input.Zone || state.SizeGB != input.SizeGB ||
				state.AccountID != input.AccountID || state.WorkspaceID != input.WorkspaceID || state.StorageID != input.ID {
				t.Fatalf("provider dispatch preceded durable child state: operations=%#v state=%#v", operations, state)
			}
			return provisionerResponse{OK: true, StorageState: "storage_existing_exact", StorageVolumeID: "disk-provider-acceptance", Status: "provider_ready", CBSStatus: "UNATTACHED", ProviderRequestID: "req-create", ProviderData: providerData}, nil
		case "sync_storage_volume":
			return provisionerResponse{OK: true, StorageVolumeID: "disk-provider-acceptance", Status: "provider_ready", CBSStatus: "UNATTACHED", ProviderRequestID: "req-sync", ProviderData: providerData}, nil
		default:
			return provisionerResponse{}, fmt.Errorf("unexpected action %q", request.Action)
		}
	}
	expected := StorageVolume{
		ID: input.ID, OperationID: input.IdempotencyKey, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID,
		Provider: "tencent-tke", ProviderResourceID: "disk-provider-acceptance", SizeGB: input.SizeGB, Zone: input.Zone, DiskType: "CLOUD_BSSD",
		CostTags:     oplCostTags(input.AccountID, input.WorkspaceID, input.ID, newOperation("create_storage_volume", "storage_volume", input.ID, input.AccountID, input.WorkspaceID, input.IdempotencyKey, hashInput(input), time.Now()).OperationID),
		ProviderData: map[string]string{"pvName": k8sName(input.ID) + "-pv", "pvcName": k8sName(input.ID) + "-data", "region": "ap-guangzhou"},
	}
	manifest := mustJSON(canonicalTencentStorageBindingObjects(t, expected))
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		if len(args) > 0 && args[0] == "apply" {
			return nil, nil
		}
		if len(args) > 0 && args[0] == "get" {
			return manifest, nil
		}
		return nil, fmt.Errorf("unexpected kubectl args=%#v", args)
	}

	created, err := service.CreateStorageVolume(context.Background(), input)
	if err != nil || created.ProviderResourceID != "disk-provider-acceptance" || created.ProviderData["region"] != "ap-guangzhou" {
		t.Fatalf("service storage create=%#v err=%v", created, err)
	}
}

func TestTencentServiceStorageResponseLossReopensFromPersistedCreateIdentity(t *testing.T) {
	input := StorageVolumeInput{
		ID: "storage-provider-reopen", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ComputeID: "compute-provider-reopen",
		Zone: "ap-guangzhou-3", SizeGB: 10, IdempotencyKey: "provider-acceptance:storage-reopen",
	}
	store := NewMemoryOperationStore()
	seedCompute := func(service *Service) {
		service.computes[input.ComputeID] = ComputeAllocation{
			ID: input.ComputeID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "ready", Zone: input.Zone,
		}
	}
	createCalls := 0
	firstProvider := NewTencentProvider()
	firstService := NewServiceWithOperationStore(firstProvider, store)
	seedCompute(firstService)
	firstProvider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Action != "create_storage_volume" || request.Region != "ap-guangzhou" || request.Storage.DiskType != "CLOUD_BSSD" ||
			request.Storage.Zone != input.Zone || request.Storage.SizeGB != uint64(input.SizeGB) {
			t.Fatalf("first create request=%#v", request)
		}
		createCalls++
		return provisionerResponse{}, errors.New("simulated CreateDisks response loss")
	}
	failed, err := firstService.CreateStorageVolume(context.Background(), input)
	if err == nil || createCalls != 1 || failed.ProviderResourceID != "" || failed.ProviderData["region"] != "ap-guangzhou" {
		t.Fatalf("first response-loss result=%#v err=%v createCalls=%d", failed, err, createCalls)
	}

	parentOperation := newOperation("create_storage_volume", "storage_volume", input.ID, input.AccountID, input.WorkspaceID, input.IdempotencyKey, hashInput(input), time.Now())
	parentBinding, ok := directStorageProviderMutationBinding(parentOperation)
	if !ok {
		t.Fatal("direct storage parent binding unavailable")
	}
	childID := providerMutationOperationID(parentBinding, "tencent_cbs_create", "storage_volume", input.ID, "")
	child, err := store.Get(context.Background(), childID)
	var state tencentCBSCreateMutationState
	if err != nil || child.Status != "failed" || !decodeProviderMutationState(child, &state) || state.Region != "ap-guangzhou" ||
		state.DiskType != "CLOUD_BSSD" || state.Zone != input.Zone || state.SizeGB != input.SizeGB {
		t.Fatalf("response-loss child=%#v state=%#v err=%v", child, state, err)
	}

	t.Setenv(tencentProviderRegionEnv, "ap-shanghai")
	t.Setenv("TENCENT_CBS_DISK_TYPE", "CLOUD_PREMIUM")
	restartedProvider := NewTencentProvider()
	if restartedProvider.region != "ap-shanghai" {
		t.Fatalf("restart region proposal=%q", restartedProvider.region)
	}
	restartedService := NewServiceWithOperationStore(restartedProvider, store)
	seedCompute(restartedService)
	var replayActions []string
	providerData := map[string]string{
		"diskType": "CLOUD_BSSD", "diskChargeType": "PREPAID", "renewFlag": "NOTIFY_AND_MANUAL_RENEW",
		"deadline": "2026-09-01T00:00:00Z", "zone": input.Zone, "sizeGb": "10", "region": "ap-guangzhou",
	}
	restartedProvider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		replayActions = append(replayActions, request.Action)
		if request.Region != state.Region || request.Storage.DiskType != state.DiskType || request.Storage.Zone != state.Zone || request.Storage.SizeGB != uint64(state.SizeGB) {
			t.Fatalf("replay request=%#v state=%#v", request, state)
		}
		switch request.Action {
		case "discover_storage_volume":
			return provisionerResponse{OK: true, StorageState: "storage_existing_exact", StorageVolumeID: "disk-provider-reopen", Status: "provider_ready", ProviderRequestID: "req-discover", ProviderData: providerData}, nil
		case "sync_storage_volume":
			return provisionerResponse{OK: true, StorageVolumeID: "disk-provider-reopen", Status: "provider_ready", CBSStatus: "UNATTACHED", ProviderRequestID: "req-sync", ProviderData: providerData}, nil
		case "create_storage_volume":
			createCalls++
			return provisionerResponse{}, errors.New("second CreateDisks dispatch")
		default:
			return provisionerResponse{}, fmt.Errorf("unexpected replay action %q", request.Action)
		}
	}
	expected := StorageVolume{
		ID: input.ID, OperationID: input.IdempotencyKey, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID,
		Provider: "tencent-tke", ProviderResourceID: "disk-provider-reopen", SizeGB: input.SizeGB, Zone: input.Zone, DiskType: "CLOUD_BSSD",
		CostTags:     oplCostTags(input.AccountID, input.WorkspaceID, input.ID, parentOperation.OperationID),
		ProviderData: map[string]string{"pvName": k8sName(input.ID) + "-pv", "pvcName": k8sName(input.ID) + "-data", "region": state.Region},
	}
	manifest := mustJSON(canonicalTencentStorageBindingObjects(t, expected))
	restartedProvider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		if len(args) > 0 && args[0] == "apply" {
			return nil, nil
		}
		if len(args) > 0 && args[0] == "get" {
			return manifest, nil
		}
		return nil, fmt.Errorf("unexpected kubectl args=%#v", args)
	}

	recovered, err := restartedService.CreateStorageVolume(context.Background(), input)
	if err != nil || recovered.ProviderResourceID != "disk-provider-reopen" || recovered.ProviderData["region"] != state.Region ||
		recovered.DiskType != state.DiskType || recovered.Zone != state.Zone || recovered.SizeGB != state.SizeGB || createCalls != 1 {
		t.Fatalf("reopened storage=%#v err=%v createCalls=%d", recovered, err, createCalls)
	}
	if !slices.Equal(replayActions, []string{"discover_storage_volume", "sync_storage_volume"}) {
		t.Fatalf("replay actions=%v", replayActions)
	}
}

func TestTencentServiceStorageReplayRejectsMissingOrDriftedChildCreateIdentityWithoutBackfill(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(*tencentCBSCreateMutationState)
	}{
		{name: "region missing", mutate: func(state *tencentCBSCreateMutationState) { state.Region = "" }},
		{name: "region resource drift", mutate: func(state *tencentCBSCreateMutationState) { state.Region = "ap-shanghai" }},
		{name: "disk type drift", mutate: func(state *tencentCBSCreateMutationState) { state.DiskType = "CLOUD_PREMIUM" }},
		{name: "zone drift", mutate: func(state *tencentCBSCreateMutationState) { state.Zone = "ap-guangzhou-4" }},
		{name: "size drift", mutate: func(state *tencentCBSCreateMutationState) { state.SizeGB = 20 }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			input := StorageVolumeInput{
				ID: "storage-direct-state-" + strings.ReplaceAll(testCase.name, " ", "-"), AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ComputeID: "compute-direct-state",
				Zone: "ap-guangzhou-3", SizeGB: 10, IdempotencyKey: "provider-acceptance:state-" + testCase.name,
			}
			store := NewMemoryOperationStore()
			firstProvider := NewTencentProvider()
			firstService := NewServiceWithOperationStore(firstProvider, store)
			firstService.computes[input.ComputeID] = ComputeAllocation{ID: input.ComputeID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "ready", Zone: input.Zone}
			firstProvider.provision = func(context.Context, provisionerRequest) (provisionerResponse, error) {
				return provisionerResponse{}, errors.New("simulated CreateDisks response loss")
			}
			if _, err := firstService.CreateStorageVolume(context.Background(), input); err == nil {
				t.Fatal("response loss fixture unexpectedly succeeded")
			}

			store.mu.Lock()
			found := false
			for index := range store.operation {
				if store.operation[index].Action != "tencent_cbs_create" {
					continue
				}
				var state tencentCBSCreateMutationState
				if !decodeProviderMutationState(store.operation[index], &state) {
					store.mu.Unlock()
					t.Fatal("missing direct CBS child state")
				}
				testCase.mutate(&state)
				body, err := json.Marshal(state)
				if err != nil {
					store.mu.Unlock()
					t.Fatal(err)
				}
				store.operation[index].RedactedProviderPayload[providerMutationStatePayloadKey] = persistedProviderMutationState{Value: body, Digest: hashInput(json.RawMessage(body))}
				found = true
			}
			store.mu.Unlock()
			if !found {
				t.Fatal("direct CBS child not found")
			}

			t.Setenv(tencentProviderRegionEnv, "ap-shanghai")
			t.Setenv("TENCENT_CBS_DISK_TYPE", "CLOUD_PREMIUM")
			restartedProvider := NewTencentProvider()
			restartedService := NewServiceWithOperationStore(restartedProvider, store)
			restartedService.computes[input.ComputeID] = ComputeAllocation{ID: input.ComputeID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "ready", Zone: input.Zone}
			providerCalls := 0
			restartedProvider.provision = func(context.Context, provisionerRequest) (provisionerResponse, error) {
				providerCalls++
				return provisionerResponse{}, errors.New("invalid child state reached provisioner")
			}
			if _, err := restartedService.CreateStorageVolume(context.Background(), input); !errors.Is(err, ErrLaunchStageBindingConflict) || providerCalls != 0 {
				t.Fatalf("invalid direct child replay err=%v providerCalls=%d", err, providerCalls)
			}
		})
	}
}

func TestTencentProviderCBSReplayRejectsDurableRegionOrIdentityDriftBeforeReadback(t *testing.T) {
	input := StorageVolumeInput{
		ID: "storage-state-drift", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ComputeID: "compute-alpha",
		Zone: "ap-guangzhou-3", SizeGB: 10, IdempotencyKey: "launch-alpha:storage", OperationID: "op_storage_state_drift",
	}
	plan := tencentWorkspacePlan{
		PackageID: "basic", Compute: ComputePlan{ID: "pool-basic"}, NodePoolID: "np-basic", MaxReplicas: 20,
		Region: "ap-guangzhou", Zone: input.Zone, Storage: tencentStoragePlan{SizeGB: input.SizeGB, DiskType: "CLOUD_BSSD"},
	}
	parent := testWorkspaceLaunchBinding("storage", "ensure_storage", input.OperationID)
	operation := newOperation(parent.Action, "workspace_launch_stage", parent.FabricOperationID, parent.AccountID, parent.WorkspaceID, parent.IdempotencyKey, parent.RequestHash, time.Now().UTC())
	operation.ID, operation.OperationID, operation.Status = parent.FabricOperationID, parent.FabricOperationID, "started"
	if err := bindLaunchStageOperation(&operation, &parent); err != nil {
		t.Fatal(err)
	}

	for _, testCase := range []struct {
		name   string
		mutate func(*tencentCBSCreateMutationState)
	}{
		{name: "region missing", mutate: func(state *tencentCBSCreateMutationState) { state.Region = "" }},
		{name: "region drift", mutate: func(state *tencentCBSCreateMutationState) { state.Region = "ap-shanghai" }},
		{name: "disk type drift", mutate: func(state *tencentCBSCreateMutationState) { state.DiskType = "CLOUD_PREMIUM" }},
		{name: "zone drift", mutate: func(state *tencentCBSCreateMutationState) { state.Zone = "ap-guangzhou-4" }},
		{name: "size drift", mutate: func(state *tencentCBSCreateMutationState) { state.SizeGB = 20 }},
		{name: "operation drift", mutate: func(state *tencentCBSCreateMutationState) { state.OperationID = "op-other" }},
		{name: "account drift", mutate: func(state *tencentCBSCreateMutationState) { state.AccountID = "acct-other" }},
		{name: "workspace drift", mutate: func(state *tencentCBSCreateMutationState) { state.WorkspaceID = "ws-other" }},
		{name: "storage drift", mutate: func(state *tencentCBSCreateMutationState) { state.StorageID = "storage-other" }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			store := NewMemoryOperationStore()
			provider := NewTencentProvider()
			service := NewServiceWithOperationStore(provider, store)
			provider.provision = func(context.Context, provisionerRequest) (provisionerResponse, error) {
				return provisionerResponse{}, errors.New("simulated CreateDisks response loss")
			}
			ctx := withTencentWorkspacePlan(service.providerMutationContext(context.Background(), operation), plan)
			if _, err := provider.CreateStorageVolume(ctx, input); err == nil {
				t.Fatal("response loss fixture unexpectedly succeeded")
			}

			childID := providerMutationOperationID(parent, "tencent_cbs_create", "storage_volume", input.ID, "")
			store.mu.Lock()
			for index := range store.operation {
				if store.operation[index].ID != childID {
					continue
				}
				var state tencentCBSCreateMutationState
				if !decodeProviderMutationState(store.operation[index], &state) {
					store.mu.Unlock()
					t.Fatal("missing CBS mutation state")
				}
				testCase.mutate(&state)
				body, marshalErr := json.Marshal(state)
				if marshalErr != nil {
					store.mu.Unlock()
					t.Fatal(marshalErr)
				}
				store.operation[index].RedactedProviderPayload[providerMutationStatePayloadKey] = persistedProviderMutationState{Value: body, Digest: hashInput(json.RawMessage(body))}
			}
			store.mu.Unlock()

			providerCalls := 0
			provider.provision = func(context.Context, provisionerRequest) (provisionerResponse, error) {
				providerCalls++
				return provisionerResponse{}, errors.New("drift reached provider")
			}
			if _, err := provider.CreateStorageVolume(ctx, input); !errors.Is(err, ErrLaunchStageBindingConflict) || providerCalls != 0 {
				t.Fatalf("drift replay err=%v providerCalls=%d", err, providerCalls)
			}
		})
	}
}

func TestTencentProviderCBSFreshPlanDriftFailsBeforeProviderDispatch(t *testing.T) {
	baseInput := StorageVolumeInput{
		ID: "storage-fresh-plan-drift", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ComputeID: "compute-alpha",
		Zone: "ap-guangzhou-3", SizeGB: 10, IdempotencyKey: "launch-fresh-plan:storage", OperationID: "op_fresh_plan_storage",
	}
	basePlan := tencentWorkspacePlan{
		PackageID: "basic", Compute: ComputePlan{ID: "pool-basic"}, NodePoolID: "np-basic", MaxReplicas: 20,
		Region: "ap-guangzhou", Zone: baseInput.Zone, Storage: tencentStoragePlan{SizeGB: baseInput.SizeGB, DiskType: "CLOUD_BSSD"},
	}
	parent := testWorkspaceLaunchBinding("storage", "ensure_storage", baseInput.OperationID)
	operation := newOperation(parent.Action, "workspace_launch_stage", parent.FabricOperationID, parent.AccountID, parent.WorkspaceID, parent.IdempotencyKey, parent.RequestHash, time.Now().UTC())
	operation.ID, operation.OperationID, operation.Status = parent.FabricOperationID, parent.FabricOperationID, "started"
	if err := bindLaunchStageOperation(&operation, &parent); err != nil {
		t.Fatal(err)
	}

	for _, testCase := range []struct {
		name   string
		mutate func(*StorageVolumeInput, *tencentWorkspacePlan)
	}{
		{name: "region drift", mutate: func(_ *StorageVolumeInput, plan *tencentWorkspacePlan) { plan.Region = "ap-shanghai" }},
		{name: "disk type missing", mutate: func(_ *StorageVolumeInput, plan *tencentWorkspacePlan) { plan.Storage.DiskType = "" }},
		{name: "zone drift", mutate: func(input *StorageVolumeInput, _ *tencentWorkspacePlan) { input.Zone = "ap-guangzhou-4" }},
		{name: "size drift", mutate: func(input *StorageVolumeInput, _ *tencentWorkspacePlan) { input.SizeGB = 20 }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			input, plan := baseInput, basePlan
			testCase.mutate(&input, &plan)
			store := NewMemoryOperationStore()
			provider := NewTencentProvider()
			service := NewServiceWithOperationStore(provider, store)
			providerCalls := 0
			provider.provision = func(context.Context, provisionerRequest) (provisionerResponse, error) {
				providerCalls++
				return provisionerResponse{}, errors.New("fresh plan drift reached provider")
			}
			ctx := withTencentWorkspacePlan(service.providerMutationContext(context.Background(), operation), plan)
			if _, err := provider.CreateStorageVolume(ctx, input); !errors.Is(err, ErrLaunchStageBindingConflict) || providerCalls != 0 {
				t.Fatalf("fresh plan drift err=%v providerCalls=%d", err, providerCalls)
			}
			operations, err := store.List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range operations {
				if item.Action == "tencent_cbs_create" {
					t.Fatalf("fresh plan drift appended child: %#v", item)
				}
			}
		})
	}
}

func TestTencentProviderAuthoritativeAbsentReadbacksAreTypedAndMutationFree(t *testing.T) {
	t.Run("compute exact persisted baseline", func(t *testing.T) {
		setTencentProviderProfileEnv(t)
		provider := NewTencentProvider()
		calls := 0
		present := false
		provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
			calls++
			if request.Action != "read_compute_allocation" {
				t.Fatalf("action=%q", request.Action)
			}
			return provisionerResponse{
				OK: true, Status: "absent", MachinePresent: &present, MutationCount: 0,
				PoolID: request.Pool.ID, NodePoolID: request.Pool.NodePoolID,
				CurrentReplicas: request.Pool.BaselineReplicas, TargetReplicas: request.Pool.TargetReplicas,
			}, nil
		}
		plan := ComputeAllocationPreparation{
			PoolID: "pool-basic-2c4g", PackageID: "basic", NodePoolID: "np-basic", InstanceType: "SA5.MEDIUM4",
			MaxReplicas: 20, BaselineReplicas: 1, TargetReplicas: 2, BeforeMachineNames: []string{"machine-before"},
		}
		allocation := ComputeAllocation{ID: "compute-absent", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", NodePoolID: plan.NodePoolID}
		if _, err := provider.DiscoverComputeAllocation(context.Background(), allocation, plan); !errors.Is(err, ErrWorkspaceLaunchResourceAbsent) || calls != 1 {
			t.Fatalf("compute absent err=%v calls=%d", err, calls)
		}
	})

	t.Run("CBS exact tags and logical name absent", func(t *testing.T) {
		provider := NewTencentProvider()
		calls := 0
		provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
			calls++
			if request.Action != "discover_storage_volume" {
				t.Fatalf("action=%q", request.Action)
			}
			return provisionerResponse{OK: true, Status: "absent", StorageState: "storage_not_started", MutationCount: 0}, nil
		}
		input := StorageVolumeInput{
			ID: "storage-absent", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Zone: "ap-guangzhou-3", SizeGB: 10,
			IdempotencyKey: "launch-alpha:storage", OperationID: "launch-alpha:storage",
		}
		persisted := StorageVolume{ID: input.ID, OperationID: input.IdempotencyKey, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, SizeGB: input.SizeGB, DiskType: "CLOUD_BSSD", Zone: input.Zone, ProviderData: map[string]string{"region": "ap-guangzhou"}}
		if _, err := provider.ReadCBSVolume(context.Background(), input, persisted); !errors.Is(err, ErrWorkspaceLaunchResourceAbsent) || calls != 1 {
			t.Fatalf("CBS absent err=%v calls=%d", err, calls)
		}
	})

	t.Run("static PV and PVC both absent", func(t *testing.T) {
		provider := NewTencentProvider()
		provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
			if len(args) == 6 && args[0] == "get" && args[3] == "--ignore-not-found" {
				return mustJSON(map[string]any{"kind": "List", "items": []any{}}), nil
			}
			return nil, fmt.Errorf("unexpected kubectl args=%#v", args)
		}
		volume := StorageVolume{
			ID: "storage-absent", OperationID: "launch-alpha:storage", AccountID: "acct-alpha", WorkspaceID: "ws-alpha",
			ProviderResourceID: "disk-absent", SizeGB: 10, Zone: "ap-guangzhou-3",
		}
		if _, err := provider.ReadStaticStorageBinding(context.Background(), volume); !errors.Is(err, ErrWorkspaceLaunchResourceAbsent) {
			t.Fatalf("static binding absent err=%v", err)
		}
	})
}

func TestTencentGatewaySecretReadbackDistinguishesAbsentConflictAndOwnerError(t *testing.T) {
	input := GatewaySecretReadbackInput{
		AccountID: "acct-alpha", WorkspaceID: "ws-alpha", WorkspaceAPIKeyID: 7,
		SecretRef: gatewaySecretName("ws-alpha"), Fingerprint: "sha256:" + strings.Repeat("a", 64), KeyDigest: strings.Repeat("a", 64),
	}
	for _, tc := range []struct {
		name     string
		readback []byte
		readErr  error
		want     error
	}{
		{name: "absent", readback: nil, want: ErrWorkspaceLaunchResourceAbsent},
		{name: "identity drift", readback: mustJSON(map[string]any{"kind": "Secret", "metadata": map[string]any{"name": input.SecretRef}}), want: ErrLaunchStageBindingConflict},
		{name: "owner error", readErr: errors.New("kubectl unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := NewTencentProvider()
			provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
				if !slices.Equal(args, []string{"get", "secret/" + input.SecretRef, "--ignore-not-found", "-o", "json"}) {
					t.Fatalf("args=%#v", args)
				}
				return tc.readback, tc.readErr
			}
			_, err := provider.ReadGatewaySecretByDigest(context.Background(), input)
			switch {
			case tc.want != nil && !errors.Is(err, tc.want):
				t.Fatalf("err=%v want=%v", err, tc.want)
			case tc.want == nil && tc.readErr != nil && !errors.Is(err, tc.readErr):
				t.Fatalf("err=%v want=%v", err, tc.readErr)
			}
		})
	}
}

func TestTencentProviderCBSResponseLossRequiresOperationIdentityBeforeDiscovery(t *testing.T) {
	provider := NewTencentProvider()
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		t.Fatalf("missing operation identity reached provider: %#v", request)
		return provisionerResponse{}, nil
	}
	input := StorageVolumeInput{
		ID: "storage-response-loss", AccountID: "acct-alpha", WorkspaceID: "workspace-alpha",
		Zone: "ap-guangzhou-3", SizeGB: 10,
	}
	persisted := StorageVolume{
		ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID,
		Provider: "tencent-tke", SizeGB: input.SizeGB, Zone: input.Zone,
	}

	if _, err := provider.ReadCBSVolume(context.Background(), input, persisted); err == nil {
		t.Fatal("missing operation identity was accepted")
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
			ProviderData: map[string]string{"diskType": "CLOUD_BSSD", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": "2026-08-16T00:00:00Z", "zone": "ap-guangzhou-4", "sizeGb": "10", "diskChargeType": "PREPAID", "region": "ap-guangzhou"},
		}, nil
	}
	input := StorageVolumeInput{ID: "storage-staged-drift", AccountID: "acct-staged", WorkspaceID: "workspace-staged", Zone: "ap-guangzhou-3", SizeGB: 10, OperationID: "launch-staged:storage"}
	persisted := StorageVolume{ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Provider: "tencent-tke", ProviderResourceID: "disk-staged-drift", SizeGB: input.SizeGB, Zone: input.Zone, DiskType: "CLOUD_BSSD", CostTags: oplCostTags(input.AccountID, input.WorkspaceID, input.ID, input.OperationID), ProviderData: map[string]string{"region": "ap-guangzhou"}}
	readback, err := provider.ReadCBSVolume(context.Background(), input, persisted)
	if err == nil || readback.Zone != "ap-guangzhou-4" {
		t.Fatalf("zone drift must fail closed: readback=%#v err=%v", readback, err)
	}
}

func TestTencentProviderStagedStorageRejectsStaticBindingLabelDriftWithoutApply(t *testing.T) {
	provider := NewTencentProvider()
	volume := StorageVolume{
		ID: "storage-label-drift", AccountID: "acct-staged", WorkspaceID: "workspace-staged", Provider: "tencent-tke", ProviderResourceID: "disk-label-drift", SizeGB: 10, Zone: "ap-guangzhou-3",
		CostTags: oplCostTags("acct-staged", "workspace-staged", "storage-label-drift", "launch-staged:storage"), ProviderData: map[string]string{"pvName": "opl-storage-label-drift-pv", "pvcName": "opl-storage-label-drift-data", "region": "ap-guangzhou"},
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
				"deadline": "2026-08-16T00:00:00Z", "zone": "ap-guangzhou-3", "sizeGb": "10", "periodMonths": "1", "region": "ap-guangzhou",
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
			ProviderData: map[string]string{"diskChargeType": "PREPAID", "diskType": "CLOUD_BSSD", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": "2026-08-16T00:00:00Z", "zone": "ap-guangzhou-3", "sizeGb": "10", "region": "ap-guangzhou"},
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
			ProviderData: map[string]string{"diskChargeType": "PREPAID", "diskType": "CLOUD_BSSD", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": "2026-08-16 00:00:00", "zone": "ap-guangzhou-3", "sizeGb": "10", "region": "ap-guangzhou"},
		}, nil
	}
	provider.kubectl = func(context.Context, []string, []byte) ([]byte, error) { return nil, errors.New("cluster unavailable") }
	volume, err := provider.CreateStorageVolume(context.Background(), StorageVolumeInput{
		ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Zone: "ap-guangzhou-3", SizeGB: 10,
		IdempotencyKey: "storage-alpha:create", OperationID: "op-storage-alpha",
	})
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
		IdempotencyKey: "storage-alpha:create", OperationID: "op-storage-alpha",
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
				return provisionerResponse{OK: true, StorageVolumeID: "disk-storage-alpha", CBSStatus: tc.cbsStatus, Status: "provider_ready", ProviderRequestID: "req-sync-cbs", ProviderData: map[string]string{"zone": "ap-guangzhou-3", "diskType": "CLOUD_BSSD", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": "2026-08-16 00:00:00", "sizeGb": "10", "region": "ap-guangzhou"}}, nil
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
				ProviderData: map[string]string{"pvName": "opl-storage-alpha-pv", "pvcName": "opl-storage-alpha-data", "region": "ap-guangzhou"},
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
			ProviderData: map[string]string{"storageVolumeId": "disk-storage-alpha", "cbsStatus": "NOT_FOUND", "region": "ap-guangzhou"},
		}, nil
	}
	provider.kubectl = func(context.Context, []string, []byte) ([]byte, error) {
		t.Fatal("confirmed CBS absence must not apply a PV or PVC")
		return nil, nil
	}
	volume, err := provider.SyncStorageVolume(context.Background(), StorageVolume{
		ID: "storage-alpha", ProviderResourceID: "disk-storage-alpha", SizeGB: 10, Zone: "ap-guangzhou-3", DiskType: "CLOUD_BSSD",
		ProviderData: map[string]string{"region": "ap-guangzhou"},
	})
	if err != nil || volume.Status != "external_deleted" || volume.CBSStatus != "NOT_FOUND" || volume.ProviderResourceID != "disk-storage-alpha" || volume.ProviderRequestID != "req-cbs-not-found" {
		t.Fatalf("confirmed CBS absence = %#v, err=%v", volume, err)
	}
}

func canonicalTencentStorageDestroyFixture() StorageVolume {
	return StorageVolume{
		ID: "storage-alpha", OperationID: "op-storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "ready", Provider: "tencent-tke",
		ProviderResourceID: "disk-storage-alpha", ProviderRequestID: "req-create-cbs", SizeGB: 10, CBSStatus: "ATTACHED", Zone: "ap-guangzhou-3", DiskType: "CLOUD_BSSD",
		CostTags:     oplCostTags("acct-alpha", "ws-alpha", "storage-alpha", "op-storage-alpha"),
		ProviderData: map[string]string{"pvName": "opl-storage-alpha-pv", "pvcName": "opl-storage-alpha-data", "region": "ap-guangzhou"},
	}
}

func canonicalTencentStorageStatusResponse(request provisionerRequest) provisionerResponse {
	return provisionerResponse{
		OK: true, StorageVolumeID: request.Storage.ID, CBSStatus: "ATTACHED", Status: "provider_ready", ProviderRequestID: "req-storage-predelete",
		ProviderData: map[string]string{"region": request.Region},
	}
}

func canonicalTencentStorageBindingObjects(t *testing.T, volume StorageVolume) map[string]any {
	t.Helper()
	manifest := map[string]any{}
	if err := json.Unmarshal(staticCBSManifest(volume), &manifest); err != nil {
		t.Fatal(err)
	}
	items := manifest["items"].([]any)
	items[1].(map[string]any)["status"] = map[string]any{"phase": "Bound"}
	return manifest
}

func exactTencentStorageBindingKubectl(t *testing.T, volume StorageVolume, deletedArgs *[]string) func(context.Context, []string, []byte) ([]byte, error) {
	t.Helper()
	readback := mustJSON(canonicalTencentStorageBindingObjects(t, volume))
	return func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		switch {
		case len(args) > 0 && args[0] == "get":
			return readback, nil
		case len(args) > 0 && args[0] == "delete":
			if deletedArgs != nil {
				*deletedArgs = append([]string(nil), args...)
			}
			return nil, nil
		default:
			t.Fatalf("unexpected kubectl request: %#v", args)
			return nil, nil
		}
	}
}

func TestTencentProviderDestroyStorageRequiresCanonicalOwnerIdentityBeforeMutation(t *testing.T) {
	base := canonicalTencentStorageDestroyFixture()
	for _, testCase := range []struct {
		name   string
		mutate func(*StorageVolume)
	}{
		{name: "provider missing", mutate: func(volume *StorageVolume) { volume.Provider = "" }},
		{name: "provider drift", mutate: func(volume *StorageVolume) { volume.Provider = "local-docker" }},
		{name: "workspace missing", mutate: func(volume *StorageVolume) { volume.WorkspaceID = "" }},
		{name: "workspace drift", mutate: func(volume *StorageVolume) { volume.WorkspaceID = "ws-other" }},
		{name: "operation missing", mutate: func(volume *StorageVolume) { volume.OperationID = "" }},
		{name: "operation drift", mutate: func(volume *StorageVolume) { volume.OperationID = "op-other" }},
		{name: "cost tags missing", mutate: func(volume *StorageVolume) { volume.CostTags = nil }},
		{name: "cost tags drift", mutate: func(volume *StorageVolume) { volume.CostTags["opl_workspace_id"] = "ws-other" }},
		{name: "cost tags contain extra fact", mutate: func(volume *StorageVolume) { volume.CostTags["extra"] = "untrusted" }},
		{name: "PV name missing", mutate: func(volume *StorageVolume) { delete(volume.ProviderData, "pvName") }},
		{name: "PV name drift", mutate: func(volume *StorageVolume) { volume.ProviderData["pvName"] = "foreign-pv" }},
		{name: "PVC name missing", mutate: func(volume *StorageVolume) { delete(volume.ProviderData, "pvcName") }},
		{name: "PVC name drift", mutate: func(volume *StorageVolume) { volume.ProviderData["pvcName"] = "foreign-pvc" }},
		{name: "region missing", mutate: func(volume *StorageVolume) { delete(volume.ProviderData, "region") }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			volume := cloneStorageVolume(base)
			testCase.mutate(&volume)
			provider := NewTencentProvider()
			kubectlCalls, provisionCalls := 0, 0
			provider.kubectl = func(context.Context, []string, []byte) ([]byte, error) {
				kubectlCalls++
				return nil, nil
			}
			provider.provision = func(context.Context, provisionerRequest) (provisionerResponse, error) {
				provisionCalls++
				return provisionerResponse{}, nil
			}
			result, err := provider.DestroyStorageVolume(context.Background(), volume)
			if err == nil || err.Error() != "storage_volume_destroy_identity_required" || kubectlCalls != 0 || provisionCalls != 0 || !reflect.DeepEqual(result, volume) {
				t.Fatalf("identity drift mutated provider: result=%#v err=%v kubectl=%d provision=%d", result, err, kubectlCalls, provisionCalls)
			}
		})
	}
}

func TestTencentProviderDestroyStorageRequiresExactLiveBindingBeforeMutation(t *testing.T) {
	base := canonicalTencentStorageDestroyFixture()
	for _, testCase := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "objects absent", mutate: func(manifest map[string]any) { manifest["items"] = []any{} }},
		{name: "PV labels drift", mutate: func(manifest map[string]any) {
			pv := manifest["items"].([]any)[0].(map[string]any)
			pv["metadata"].(map[string]any)["labels"].(map[string]any)["oplcloud.cn/workspace-id"] = "ws-other"
		}},
		{name: "PV CBS handle drift", mutate: func(manifest map[string]any) {
			pv := manifest["items"].([]any)[0].(map[string]any)
			pv["spec"].(map[string]any)["csi"].(map[string]any)["volumeHandle"] = "disk-other"
		}},
		{name: "PV capacity drift", mutate: func(manifest map[string]any) {
			pv := manifest["items"].([]any)[0].(map[string]any)
			pv["spec"].(map[string]any)["capacity"].(map[string]any)["storage"] = "100Gi"
		}},
		{name: "PV zone drift", mutate: func(manifest map[string]any) {
			pv := manifest["items"].([]any)[0].(map[string]any)
			affinity := pv["spec"].(map[string]any)["nodeAffinity"].(map[string]any)
			terms := affinity["required"].(map[string]any)["nodeSelectorTerms"].([]any)
			expressions := terms[0].(map[string]any)["matchExpressions"].([]any)
			expressions[0].(map[string]any)["values"] = []any{"ap-guangzhou-4"}
		}},
		{name: "PVC binding drift", mutate: func(manifest map[string]any) {
			pvc := manifest["items"].([]any)[1].(map[string]any)
			pvc["spec"].(map[string]any)["volumeName"] = "foreign-pv"
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			manifest := canonicalTencentStorageBindingObjects(t, base)
			testCase.mutate(manifest)
			provider := NewTencentProvider()
			readCalls, mutationCalls := 0, 0
			provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
				if len(args) > 0 && args[0] == "get" {
					readCalls++
					return mustJSON(manifest), nil
				}
				mutationCalls++
				return nil, nil
			}
			provider.provision = func(context.Context, provisionerRequest) (provisionerResponse, error) {
				mutationCalls++
				return provisionerResponse{}, nil
			}
			result, err := provider.DestroyStorageVolume(context.Background(), base)
			if err == nil || readCalls != 1 || mutationCalls != 0 || !reflect.DeepEqual(result, base) {
				t.Fatalf("live binding drift reached mutation: result=%#v err=%v reads=%d mutations=%d", result, err, readCalls, mutationCalls)
			}
		})
	}
}

func TestTencentProviderStorageReadbackRejectsRegionDriftWithoutRewritingIdentity(t *testing.T) {
	provider := NewTencentProvider()
	input := canonicalTencentStorageDestroyFixture()
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Action != "sync_storage_volume" || request.Region != "ap-guangzhou" {
			t.Fatalf("storage readback request=%#v", request)
		}
		return provisionerResponse{
			OK: true, StorageVolumeID: input.ProviderResourceID, CBSStatus: "NOT_FOUND", Status: "external_deleted", ProviderRequestID: "req-wrong-region",
			ProviderData: map[string]string{"region": "ap-shanghai", "storageVolumeId": input.ProviderResourceID, "cbsStatus": "NOT_FOUND", "describeCbsRequestId": "req-wrong-region"},
		}, nil
	}
	result, err := provider.ReadStorageVolume(context.Background(), input)
	if err == nil || err.Error() != "storage_cbs_region_readback_mismatch" || !reflect.DeepEqual(result, input) {
		t.Fatalf("region drift rewrote storage identity: result=%#v err=%v", result, err)
	}
}

func TestTencentProviderStorageConfigDriftDoesNotDeleteKubernetesBinding(t *testing.T) {
	provider := NewTencentProvider()
	input := canonicalTencentStorageDestroyFixture()
	deleteCalls, provisionCalls := 0, 0
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		if len(args) > 0 && args[0] == "delete" {
			deleteCalls++
			return nil, nil
		}
		return mustJSON(canonicalTencentStorageBindingObjects(t, input)), nil
	}
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		provisionCalls++
		if request.Action != "sync_storage_volume" || request.Region != input.ProviderData["region"] {
			t.Fatalf("unexpected pre-delete request: %#v", request)
		}
		return provisionerResponse{OK: false, ErrorCode: "storage_provider_config_identity_mismatch", MutationCount: 0}, nil
	}
	result, err := provider.DestroyStorageVolume(context.Background(), input)
	if err == nil || err.Error() != "storage_provider_config_identity_mismatch" || provisionCalls != 1 || deleteCalls != 0 || !reflect.DeepEqual(result, input) {
		t.Fatalf("config drift released binding: result=%#v err=%v provision=%d delete=%d", result, err, provisionCalls, deleteCalls)
	}
}

func TestTencentProviderDestroyStorageDeletesBindingAndRequiresConfirmedCBSAbsence(t *testing.T) {
	provider := NewTencentProvider()
	resource := canonicalTencentStorageDestroyFixture()
	var args []string
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Region != "ap-guangzhou" || request.AccountID != "acct-alpha" || request.Storage.ID != "disk-storage-alpha" || request.Storage.SizeGB != 10 || request.Storage.Zone != "ap-guangzhou-3" || request.Storage.DiskType != "CLOUD_BSSD" || !reflect.DeepEqual(request.Tags, oplCostTags("acct-alpha", "ws-alpha", "storage-alpha", "op-storage-alpha")) {
			t.Fatalf("storage destroy request=%#v", request)
		}
		if request.Action == "sync_storage_volume" {
			return canonicalTencentStorageStatusResponse(request), nil
		}
		if request.Action != "destroy_storage_volume" {
			t.Fatalf("storage destroy action=%#v", request)
		}
		return provisionerResponse{
			OK: true, StorageVolumeID: "disk-storage-alpha", CBSStatus: "NOT_FOUND", Status: "external_deleted", ProviderRequestID: "req-terminate-cbs", MutationCount: 1,
			ProviderData: map[string]string{"storageVolumeId": "disk-storage-alpha", "cbsStatus": "NOT_FOUND", "status": "external_deleted", "storageDestroyPhase": "absence_confirmed", "storageDestroyMutationCount": "1", "terminateCbsRequestId": "req-terminate-cbs", "describeCbsRequestId": "req-describe-cbs-absent", "region": "ap-guangzhou"},
		}, nil
	}
	provider.kubectl = exactTencentStorageBindingKubectl(t, resource, &args)
	volume, err := provider.DestroyStorageVolume(context.Background(), resource)
	if err != nil || volume.Status != "external_deleted" || volume.CBSStatus != "NOT_FOUND" || volume.ProviderResourceID != "disk-storage-alpha" || volume.ProviderRequestID != "req-terminate-cbs" || volume.ProviderData["terminateCbsRequestId"] != "req-terminate-cbs" {
		t.Fatalf("destroyed volume=%#v err=%v", volume, err)
	}
	if !slices.Contains(args, "pvc/opl-storage-alpha-data") || !slices.Contains(args, "pv/opl-storage-alpha-pv") {
		t.Fatalf("static binding delete args = %#v", args)
	}
}

func TestTencentProviderDestroyStorageRejectsNonAbsentProvisionerResult(t *testing.T) {
	for _, response := range []provisionerResponse{
		{OK: true, StorageVolumeID: "disk-storage-alpha", CBSStatus: "UNATTACHED", Status: "retained"},
		{OK: true, StorageVolumeID: "disk-other", CBSStatus: "NOT_FOUND", Status: "external_deleted"},
		{OK: true, StorageVolumeID: "disk-storage-alpha", CBSStatus: "NOT_FOUND", Status: "external_deleted", ProviderData: map[string]string{"storageVolumeId": "disk-storage-alpha", "cbsStatus": "NOT_FOUND", "status": "external_deleted"}},
		{OK: true, StorageVolumeID: "disk-storage-alpha", CBSStatus: "NOT_FOUND", Status: "external_deleted", ProviderData: map[string]string{"storageVolumeId": "disk-other", "cbsStatus": "NOT_FOUND", "status": "external_deleted", "describeCbsRequestId": "req-describe-cbs-absent"}},
		{OK: true, StorageVolumeID: "disk-storage-alpha", CBSStatus: "NOT_FOUND", Status: "external_deleted", ProviderData: map[string]string{"storageVolumeId": "disk-storage-alpha", "cbsStatus": "UNATTACHED", "status": "external_deleted", "describeCbsRequestId": "req-describe-cbs-absent"}},
		{OK: true, StorageVolumeID: "disk-storage-alpha", CBSStatus: "NOT_FOUND", Status: "external_deleted", ProviderData: map[string]string{"storageVolumeId": "disk-storage-alpha", "cbsStatus": "NOT_FOUND", "status": "retained", "describeCbsRequestId": "req-describe-cbs-absent"}},
	} {
		provider := NewTencentProvider()
		resource := canonicalTencentStorageDestroyFixture()
		provider.kubectl = exactTencentStorageBindingKubectl(t, resource, nil)
		provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
			if request.Action == "sync_storage_volume" {
				return canonicalTencentStorageStatusResponse(request), nil
			}
			return response, nil
		}
		_, err := provider.DestroyStorageVolume(context.Background(), resource)
		if err == nil || err.Error() != "storage_volume_destroy_readback_mismatch" {
			t.Fatalf("invalid destroy result=%#v error=%v", response, err)
		}
	}
}

func TestTencentProviderDestroyStorageRejectsMutationPhaseCountMismatch(t *testing.T) {
	for _, response := range []provisionerResponse{
		{OK: false, ErrorCode: "storage_volume_detach_unverified", MutationCount: 1, ProviderData: map[string]string{"storageDestroyPhase": "terminate_not_attempted", "storageDestroyMutationCount": "0"}},
		{OK: false, ErrorCode: "storage_volume_delete_unverified", MutationCount: 0, ProviderData: map[string]string{"storageDestroyPhase": "terminate_attempted", "storageDestroyMutationCount": "1"}},
		{OK: true, StorageVolumeID: "disk-storage-alpha", CBSStatus: "NOT_FOUND", Status: "external_deleted", MutationCount: 1, ProviderData: map[string]string{
			"storageVolumeId": "disk-storage-alpha", "cbsStatus": "NOT_FOUND", "status": "external_deleted", "describeCbsRequestId": "req-describe-cbs-absent",
			"storageDestroyPhase": "absence_confirmed", "storageDestroyMutationCount": "0",
		}},
	} {
		provider := NewTencentProvider()
		resource := canonicalTencentStorageDestroyFixture()
		provider.kubectl = exactTencentStorageBindingKubectl(t, resource, nil)
		provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
			if request.Action == "sync_storage_volume" {
				return canonicalTencentStorageStatusResponse(request), nil
			}
			return response, nil
		}
		volume, err := provider.DestroyStorageVolume(context.Background(), resource)
		if err == nil || volume.ProviderData["storageDestroyPhase"] != "" || volume.ProviderData["storageDestroyMutationCount"] != "" {
			t.Fatalf("mismatched mutation evidence persisted: response=%#v volume=%#v err=%v", response, volume, err)
		}
	}
}

func TestTencentStorageDestroyFailureDoesNotPolluteRestartReplay(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryOperationStore()
	resource := canonicalTencentStorageDestroyFixture()
	appendSucceededStorageCreate(t, store, resource)
	provider := NewTencentProvider()
	destroyCalls := 0
	provider.kubectl = exactTencentStorageBindingKubectl(t, resource, nil)
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		switch request.Action {
		case "destroy_storage_volume":
			destroyCalls++
			return provisionerResponse{
				OK: false, ErrorCode: "storage_volume_delete_unconfirmed", StorageVolumeID: resource.ProviderResourceID,
				ProviderRequestID: "req-terminate-cbs", CBSStatus: "UNATTACHED", MutationCount: 1,
				ProviderData: map[string]string{
					"storageDestroyPhase": "terminate_attempted", "storageDestroyMutationCount": "1", "terminateCbsRequestId": "req-terminate-cbs",
					"pvName": "foreign-pv", "pvcName": "foreign-pvc", "zone": "ap-shanghai-1", "diskType": "CLOUD_SSD", "untrusted": "pollution",
				},
			}, nil
		case "sync_storage_volume":
			return provisionerResponse{
				OK: true, StorageVolumeID: resource.ProviderResourceID, ProviderRequestID: "req-read-cbs-absence", CBSStatus: "NOT_FOUND", Status: "external_deleted",
				ProviderData: map[string]string{
					"storageVolumeId": resource.ProviderResourceID, "cbsStatus": "NOT_FOUND", "status": "external_deleted",
					"storageDestroyPhase": "absence_confirmed", "storageDestroyMutationCount": "1", "describeCbsRequestId": "req-read-cbs-absence", "region": "ap-guangzhou",
				},
			}, nil
		default:
			t.Fatalf("unexpected provisioner request: %#v", request)
			return provisionerResponse{}, nil
		}
	}

	first := NewServiceWithOperationStore(provider, store)
	failed, err := first.DestroyStorageVolume(ctx, resource.ID)
	if err == nil || !sameStorageDestroyStableIdentity(resource, failed) || failed.ProviderData["storageDestroyPhase"] != "terminate_attempted" || failed.ProviderData["untrusted"] != "" {
		t.Fatalf("failed destroy polluted canonical result: failed=%#v err=%v", failed, err)
	}

	restarted := NewServiceWithOperationStore(provider, store)
	converged, err := restarted.DestroyStorageVolume(ctx, resource.ID)
	if err != nil || !storageDestroyReadbackConfirmsAbsence(converged) || destroyCalls != 1 || converged.ProviderData["untrusted"] != "" {
		t.Fatalf("restart replay=%#v err=%v destroyCalls=%d", converged, err, destroyCalls)
	}
}

func TestTencentStorageDestroyOKResponsePollutionDoesNotSurviveRestart(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryOperationStore()
	resource := canonicalTencentStorageDestroyFixture()
	appendSucceededStorageCreate(t, store, resource)
	provider := NewTencentProvider()
	destroyCalls := 0
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		if len(args) > 0 && args[0] == "get" {
			return mustJSON(canonicalTencentStorageBindingObjects(t, resource)), nil
		}
		return nil, nil
	}
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		switch request.Action {
		case "destroy_storage_volume":
			destroyCalls++
			return provisionerResponse{
				OK: true, StorageVolumeID: resource.ProviderResourceID, ProviderRequestID: "req-terminate-cbs", CBSStatus: "NOT_FOUND", Status: "external_deleted", MutationCount: 1,
				ProviderData: map[string]string{
					"storageVolumeId": resource.ProviderResourceID, "cbsStatus": "NOT_FOUND", "status": "external_deleted", "storageDestroyPhase": "absence_confirmed",
					"storageDestroyMutationCount": "1", "terminateCbsRequestId": "req-terminate-cbs", "describeCbsRequestId": "req-describe-cbs-absence",
					"pvName": "foreign-pv", "zone": "ap-shanghai-1", "diskType": "CLOUD_SSD", "unknown": "pollution",
				},
			}, nil
		case "sync_storage_volume":
			return provisionerResponse{
				OK: true, StorageVolumeID: resource.ProviderResourceID, ProviderRequestID: "req-read-cbs-absence", CBSStatus: "NOT_FOUND", Status: "external_deleted",
				ProviderData: map[string]string{
					"storageVolumeId": resource.ProviderResourceID, "cbsStatus": "NOT_FOUND", "status": "external_deleted",
					"storageDestroyPhase": "absence_confirmed", "storageDestroyMutationCount": "1", "describeCbsRequestId": "req-read-cbs-absence", "region": "ap-guangzhou",
				},
			}, nil
		default:
			t.Fatalf("unexpected provisioner request: %#v", request)
			return provisionerResponse{}, nil
		}
	}

	first := NewServiceWithOperationStore(provider, store)
	failed, err := first.DestroyStorageVolume(ctx, resource.ID)
	if err == nil || err.Error() != "storage_volume_destroy_readback_mismatch" || !sameStorageDestroyStableIdentity(resource, failed) ||
		failed.Zone != resource.Zone || failed.DiskType != resource.DiskType || failed.ProviderData["pvName"] != resource.ProviderData["pvName"] || failed.ProviderData["unknown"] != "" {
		t.Fatalf("OK response pollution entered canonical result: failed=%#v err=%v", failed, err)
	}

	restarted := NewServiceWithOperationStore(provider, store)
	converged, err := restarted.DestroyStorageVolume(ctx, resource.ID)
	if err != nil || !storageDestroyReadbackConfirmsAbsence(converged) || destroyCalls != 1 || converged.Zone != resource.Zone || converged.DiskType != resource.DiskType || converged.ProviderData["unknown"] != "" {
		t.Fatalf("restart replay=%#v err=%v destroyCalls=%d", converged, err, destroyCalls)
	}
}

func TestTencentProviderRenewsCBSAndPersistsDeadlineReadback(t *testing.T) {
	provider := NewTencentProvider()
	provider.provision = func(_ context.Context, request provisionerRequest) (provisionerResponse, error) {
		if request.Action != "renew_storage_volume" || request.AccountID != "acct-alpha" || request.Region != "ap-guangzhou" || request.Storage.Deadline != "2026-08-16T00:00:00Z" ||
			!reflect.DeepEqual(request.Tags, oplCostTags("acct-alpha", "ws-alpha", "storage-alpha", "op-storage-alpha")) {
			t.Fatalf("renew request = %#v", request)
		}
		return provisionerResponse{OK: true, StorageVolumeID: "disk-storage-alpha", CBSStatus: "UNATTACHED", Status: "provider_ready", ProviderRequestID: "req-renew-cbs", ProviderData: map[string]string{"deadline": "2026-09-16T00:00:00Z", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "diskChargeType": "PREPAID", "zone": "ap-guangzhou-3", "diskType": "CLOUD_BSSD", "sizeGb": "10", "region": "ap-guangzhou"}}, nil
	}
	volume, err := provider.RenewStorageVolume(context.Background(), StorageVolume{
		ID: "storage-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ProviderResourceID: "disk-storage-alpha", SizeGB: 10, Zone: "ap-guangzhou-3", DiskType: "CLOUD_BSSD", Deadline: "2026-08-16T00:00:00Z",
		CostTags: oplCostTags("acct-alpha", "ws-alpha", "storage-alpha", "op-storage-alpha"), ProviderData: map[string]string{"region": "ap-guangzhou"},
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
		volume, err := provider.RenewStorageVolume(context.Background(), StorageVolume{ID: "storage-alpha", ProviderResourceID: "disk-storage-alpha", SizeGB: 10, Zone: "ap-guangzhou-3", DiskType: "CLOUD_BSSD", Deadline: "2026-08-16 00:00:00", ProviderData: map[string]string{"region": "ap-guangzhou"}})
		if err == nil || volume.ID != "storage-alpha" || volume.ProviderResourceID != "disk-storage-alpha" || volume.ProviderRequestID != "req-renew-cbs" || volume.ProviderData["describeCbsRequestId"] != "req-read-cbs" {
			t.Fatalf("failed CBS renewal lost evidence: volume=%#v err=%v", volume, err)
		}
	})
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
