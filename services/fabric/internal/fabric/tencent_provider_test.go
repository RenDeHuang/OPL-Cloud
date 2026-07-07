package fabric

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestTKENodeSelectorUsesTencentInstanceLabel(t *testing.T) {
	withMachine := tkeNodeSelector(map[string]string{"machineName": "np-basic-2"}, "10.0.0.8")
	if withMachine["cloud.tencent.com/node-instance-id"] != "np-basic-2" {
		t.Fatalf("selector with machineName = %#v", withMachine)
	}
	if _, ok := withMachine["kubernetes.io/hostname"]; ok {
		t.Fatalf("selector must not use machineName as hostname: %#v", withMachine)
	}
	withoutMachine := tkeNodeSelector(map[string]string{}, "10.0.0.8")
	if withoutMachine["kubernetes.io/hostname"] != "10.0.0.8" {
		t.Fatalf("selector without machineName = %#v", withoutMachine)
	}
}

func TestWorkspaceManifestUsesHostNetworkOnDedicatedTKENode(t *testing.T) {
	t.Setenv("OPL_WORKSPACE_IMAGE", "workspace-image:test")
	t.Setenv("OPL_IMAGE_PULL_SECRET_NAME", "pull-secret")
	t.Setenv("OPL_AIONUI_ADMIN_PASSWORD_SEED", "workspace-secret-2026-very-long")
	compute := ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", PackageID: "basic", NodeSelector: map[string]any{"cloud.tencent.com/node-instance-id": "np-basic-2"}}
	storage := StorageVolume{ProviderResourceID: "pvc/opl-storage-alpha-data"}
	var manifest map[string]any
	if err := json.Unmarshal(workspaceManifest("ws-alpha", "Alpha", "token", "opl-compute-alpha", compute, storage), &manifest); err != nil {
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
	secretData := secret["data"].(map[string]any)
	passwordBytes, err := base64.StdEncoding.DecodeString(secretData["OPL_AIONUI_ADMIN_PASSWORD"].(string))
	if err != nil {
		t.Fatalf("decode webui password: %v", err)
	}
	if string(passwordBytes) != "opl_jngdohVMGgp2Kdvpg4f-OLuNAa1!" {
		t.Fatalf("workspace must derive a per-workspace WebUI password, got %q", string(passwordBytes))
	}
	podSpec := nested(deployment, "spec", "template", "spec").(map[string]any)
	if nested(deployment, "metadata", "labels", "oplcloud.cn/workspace-id") != "ws-alpha" {
		t.Fatalf("deployment must carry workspace label for stateless runtime lookup: %#v", nested(deployment, "metadata", "labels"))
	}
	if podSpec["hostNetwork"] != true || podSpec["dnsPolicy"] != "ClusterFirstWithHostNet" {
		t.Fatalf("workspace pod must use host networking on dedicated TKE nodes: %#v", podSpec)
	}
	toleration := podSpec["tolerations"].([]any)[0].(map[string]any)
	if toleration["key"] != "tke.cloud.tencent.com/eni-ip-unavailable" || toleration["effect"] != "NoSchedule" {
		t.Fatalf("workspace pod must tolerate TKE ENI readiness taint: %#v", toleration)
	}
	container := podSpec["containers"].([]any)[0].(map[string]any)
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
	for key := range env {
		if strings.Contains(key, "AUTH_MODE") || strings.Contains(key, "PERSISTENT_CONFIG") || key == "WEBUI"+"_"+"AUTH" {
			t.Fatalf("workspace must use AionUI login with managed credentials, not retired auth bypass env: %#v", env)
		}
	}
	if env["AIONUI_ALLOW_REMOTE"] != "true" {
		t.Fatalf("workspace must allow remote AionUI access: %#v", env)
	}
	lifecycle := container["lifecycle"].(map[string]any)
	postStart := nested(lifecycle, "postStart", "exec").(map[string]any)
	command := postStart["command"].([]any)
	if !strings.Contains(strings.Join([]string{command[0].(string), command[1].(string), command[2].(string)}, " "), "/api/webui/change-password") {
		t.Fatalf("workspace must set the managed WebUI password after startup: %#v", command)
	}
}

func TestRuntimeStatusRecoversWorkspaceResourcesFromKubernetesLabels(t *testing.T) {
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
		want := []string{"get", "deployment/opl-compute-alpha", "pvc/opl-storage-alpha-data", "service/opl-compute-alpha", "ingress/opl-cloud", "endpoints/opl-compute-alpha", "-o", "json"}
		if !slices.Equal(args, want) {
			t.Fatalf("kubectl args = %#v, want %#v", args, want)
		}
		return mustJSON(map[string]any{"kind": "List", "items": []any{
			deployment,
			map[string]any{"kind": "PersistentVolumeClaim", "metadata": map[string]any{"name": "opl-storage-alpha-data"}, "status": map[string]any{"phase": "Bound"}},
			service,
			map[string]any{"kind": "Ingress", "metadata": map[string]any{"name": "opl-cloud"}, "spec": map[string]any{"rules": []any{map[string]any{"http": map[string]any{"paths": []any{map[string]any{"path": "/", "backend": map[string]any{"service": map[string]any{"name": gatewayService, "port": map[string]any{"number": 8787}}}}}}}}}},
			map[string]any{"kind": "Endpoints", "metadata": map[string]any{"name": "opl-compute-alpha"}, "subsets": []any{map[string]any{"addresses": []any{map[string]any{"ip": "10.0.0.8"}}}}},
		}}), nil
	}

	status, err := provider.WorkspaceRuntimeStatus(context.Background(), "ws-alpha")

	if err != nil {
		t.Fatalf("runtime status: %v", err)
	}
	if !status.Ready {
		t.Fatalf("status = %#v, want ready", status)
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

func envMap(entries []any) map[string]string {
	values := map[string]string{}
	for _, entry := range entries {
		asMap, _ := entry.(map[string]any)
		values[stringValue(asMap["name"])] = stringValue(asMap["value"])
	}
	return values
}
