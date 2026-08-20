package fabric

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestWorkspaceRuntimeAdaptersProjectVersionedABI(t *testing.T) {
	raw, err := os.ReadFile("../../../../packages/contracts/opl-cloud-workspace-runtime-abi-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		SchemaVersion  int `json:"schemaVersion"`
		WorkspaceWebUI struct {
			Protocol string `json:"protocol"`
			Port     int    `json:"port"`
		} `json:"workspaceWebUI"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	if contract.SchemaVersion != 1 || contract.WorkspaceWebUI.Protocol != "http" || contract.WorkspaceWebUI.Port <= 0 {
		t.Fatalf("invalid Workspace Runtime ABI contract: %#v", contract)
	}
	if localDockerWorkspaceRuntimeWebUIPort != strconv.Itoa(contract.WorkspaceWebUI.Port) {
		t.Fatalf("Local-Docker Workspace Runtime port=%s, contract=%d", localDockerWorkspaceRuntimeWebUIPort, contract.WorkspaceWebUI.Port)
	}
	if !strings.Contains(localDockerRuntimeHealthCommand, "http://127.0.0.1:"+localDockerWorkspaceRuntimeWebUIPort+"/") {
		t.Fatalf("Local-Docker health command does not project the Runtime ABI: %s", localDockerRuntimeHealthCommand)
	}
	localProvider, runner, ctx, _, input, compute, volume := localDockerRuntimeReplayFixture(t, 0)
	if _, err := localProvider.CreateWorkspaceRuntime(ctx, input, compute, volume); err != nil {
		t.Fatal(err)
	}
	wantPublish := localProvider.runtimeHost + "::" + localDockerWorkspaceRuntimeWebUIPort
	foundPublish := false
	for _, call := range runner.calls {
		for index := 0; index+1 < len(call); index++ {
			if call[index] == "-p" && call[index+1] == wantPublish {
				foundPublish = true
			}
		}
	}
	if !foundPublish {
		t.Fatalf("Local-Docker create did not publish the Runtime ABI port %q: %#v", wantPublish, runner.calls)
	}
	if tencentWorkspaceRuntimeWebUIPort != contract.WorkspaceWebUI.Port {
		t.Fatalf("Tencent Workspace Runtime port=%d, contract=%d", tencentWorkspaceRuntimeWebUIPort, contract.WorkspaceWebUI.Port)
	}

	t.Setenv("OPL_AIONUI_ADMIN_PASSWORD_SEED", "workspace-runtime-abi-test-seed")
	manifestRaw := workspaceManifestWithGatewayPlan(
		WorkspaceRuntimeInput{WorkspaceID: "ws-abi", ImageID: "workspace-image", RuntimeOperationID: "runtime-op"},
		"Workspace ABI", "credential-seed", "rt-abi", "opl-compute-abi",
		ComputeAllocation{ID: "compute-abi", AccountID: "acct-abi", WorkspaceID: "ws-abi", PackageID: "basic"},
		StorageVolume{ID: "storage-abi", ProviderData: map[string]string{"pvcName": "storage-abi-data"}},
		nil, tencentWorkspaceRuntimeGatewayBinding{}, ComputePlan{ID: "basic", CPU: 2, MemoryGB: 4},
	)
	var manifest struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	var deployment, service, networkPolicy map[string]any
	for _, item := range manifest.Items {
		switch item["kind"] {
		case "Deployment":
			deployment = item
		case "Service":
			service = item
		case "NetworkPolicy":
			networkPolicy = item
		}
	}
	container := nested(deployment, "spec", "template", "spec", "containers").([]any)[0].(map[string]any)
	projectedPorts := []any{
		nested(container, "ports").([]any)[0].(map[string]any)["containerPort"],
		nested(container, "readinessProbe", "httpGet", "port"),
		nested(service, "spec", "ports").([]any)[0].(map[string]any)["port"],
		nested(networkPolicy, "spec", "ingress").([]any)[0].(map[string]any)["ports"].([]any)[0].(map[string]any)["port"],
	}
	for index, projected := range projectedPorts {
		if int(projected.(float64)) != contract.WorkspaceWebUI.Port {
			t.Fatalf("Tencent Runtime ABI projection %d=%v, contract=%d", index, projected, contract.WorkspaceWebUI.Port)
		}
	}
}
