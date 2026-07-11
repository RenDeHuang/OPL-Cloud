package fabric

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
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
	t.Setenv("OPL_CODEX_API_KEY", "gateway-key-secret")
	compute := ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", PackageID: "basic", NodeSelector: map[string]any{"cloud.tencent.com/node-instance-id": "np-basic-2"}}
	storage := StorageVolume{ProviderResourceID: "pvc/opl-storage-alpha-data"}
	tags := map[string]string{"opl_account_id": "acct-alpha", "opl_workspace_id": "ws-alpha", "opl_resource_id": "compute-alpha", "opl_operation_id": "op-alpha"}
	var manifest map[string]any
	if err := json.Unmarshal(workspaceManifest("ws-alpha", "Alpha", "token", "opl-compute-alpha", compute, storage, tags), &manifest); err != nil {
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
	gatewayKeyBytes := decodeSecretValue(t, secretData, "gateway_api_key")
	if string(gatewayKeyBytes) != "gateway-key-secret" {
		t.Fatalf("gateway API key must be kept as model access credential secret, got %q", string(gatewayKeyBytes))
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
	if env["AIONUI_ALLOW_REMOTE"] != "true" {
		t.Fatalf("workspace must allow remote AionUI access: %#v", env)
	}
	if env["OPL_WEBUI_DEPLOYMENT_MODE"] != "cloud" || env["OPL_WEBUI_AUTH_MODE"] != "password" {
		t.Fatalf("workspace must start one-person-lab-app in explicit cloud password mode: %#v", env)
	}
	if env["OPL_WEBUI_USERNAME"] != "opl" ||
		env["OPL_WEBUI_PASSWORD_FILE"] != "/run/secrets/opl_webui_password" ||
		env["OPL_WEBUI_SESSION_SECRET_FILE"] != "/run/secrets/webui_session_secret" ||
		env["OPL_GATEWAY_API_KEY_FILE"] != "/run/secrets/gateway_api_key" {
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
	if secretVolume == nil || nested(secretVolume, "secret", "secretName") != "opl-compute-alpha-env" {
		t.Fatalf("workspace must source cloud secret files from the workspace Secret: %#v", podSpec["volumes"])
	}
	if nested(secretVolume, "secret", "items").([]any)[0].(map[string]any)["path"] != "opl_webui_password" {
		t.Fatalf("workspace password secret path must match one-person-lab-app cloud compose: %#v", secretVolume)
	}
}

func TestWorkspaceManifestSkipsGatewaySecretWhenCodexKeyMissing(t *testing.T) {
	t.Setenv("OPL_WORKSPACE_IMAGE", "workspace-image:test")
	t.Setenv("OPL_IMAGE_PULL_SECRET_NAME", "pull-secret")
	t.Setenv("OPL_AIONUI_ADMIN_PASSWORD_SEED", "workspace-secret-2026-very-long")
	compute := ComputeAllocation{ID: "compute-alpha", AccountID: "acct-alpha", PackageID: "basic", NodeSelector: map[string]any{"cloud.tencent.com/node-instance-id": "np-basic-2"}}
	storage := StorageVolume{ProviderResourceID: "pvc/opl-storage-alpha-data"}
	var manifest map[string]any
	if err := json.Unmarshal(workspaceManifest("ws-alpha", "Alpha", "token", "opl-compute-alpha", compute, storage, nil), &manifest); err != nil {
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
	if _, ok := secret["data"].(map[string]any)["gateway_api_key"]; ok {
		t.Fatalf("workspace secret must not contain empty gateway key: %#v", secret["data"])
	}
	container := nested(deployment, "spec", "template", "spec", "containers").([]any)[0].(map[string]any)
	if _, ok := envMap(container["env"].([]any))["OPL_GATEWAY_API_KEY_FILE"]; ok {
		t.Fatalf("workspace must not point at a missing gateway key file: %#v", container["env"])
	}
	secretVolume := findVolume(nested(deployment, "spec", "template", "spec", "volumes").([]any), "workspace-secrets")
	for _, item := range nested(secretVolume, "secret", "items").([]any) {
		if item.(map[string]any)["key"] == "gateway_api_key" {
			t.Fatalf("workspace volume must not reference a missing gateway key: %#v", secretVolume)
		}
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
	provider.kubectl = func(_ context.Context, args []string, stdin []byte) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if args[0] == "get" {
			return mustJSON(map[string]any{"items": []any{map[string]any{
				"kind": "Deployment", "metadata": map[string]any{"name": "opl-workspace-alpha", "labels": map[string]any{"oplcloud.cn/workspace-id": "workspace-alpha"}},
				"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"volumes": []any{map[string]any{"name": "workspace-data", "persistentVolumeClaim": map[string]any{"claimName": "pvc-alpha"}}}}}},
			}}}), nil
		}
		if stdin != nil {
			uploaded = append([]byte(nil), stdin...)
		}
		if len(args) > 3 && args[3] == "cat" {
			return append([]byte(nil), uploaded...), nil
		}
		return nil, nil
	}
	if err := provider.PublishWorkspaceContent(context.Background(), "workspace-alpha", "inputs/paper.txt", []byte("verified")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if string(uploaded) != "verified" || len(calls) != 5 || !slices.Equal(calls[1], []string{"exec", "deployment/opl-workspace-alpha", "--", "mkdir", "-p", "/projects/inputs"}) || calls[2][0] != "exec" || calls[2][4] != "dd" || !slices.Equal(calls[3][:4], []string{"exec", "deployment/opl-workspace-alpha", "--", "mv"}) || !slices.Equal(calls[4], []string{"exec", "deployment/opl-workspace-alpha", "--", "cat", "/projects/inputs/paper.txt"}) {
		t.Fatalf("calls=%#v uploaded=%q", calls, uploaded)
	}
}

func TestTencentProviderReportsWorkspaceContentMismatchWithoutBody(t *testing.T) {
	provider := NewTencentProvider()
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		if args[0] == "get" {
			return mustJSON(map[string]any{"items": []any{map[string]any{
				"kind": "Deployment", "metadata": map[string]any{"name": "opl-workspace-alpha", "labels": map[string]any{"oplcloud.cn/workspace-id": "workspace-alpha"}},
			}}}), nil
		}
		if len(args) > 3 && args[3] == "cat" {
			return []byte("different-secret-body"), nil
		}
		return nil, nil
	}
	body := []byte("expected-secret-body")
	err := provider.PublishWorkspaceContent(context.Background(), "workspace-alpha", "inputs/paper.txt", body)
	expectedDigest := fmt.Sprintf("%x", sha256.Sum256(body))
	actualDigest := fmt.Sprintf("%x", sha256.Sum256([]byte("different-secret-body")))
	if err == nil || !strings.Contains(err.Error(), expectedDigest) || !strings.Contains(err.Error(), actualDigest) || strings.Contains(err.Error(), string(body)) {
		t.Fatalf("safe mismatch diagnostics = %v", err)
	}
}

func TestTencentProviderReportsWorkspaceContentReadbackCommandFailure(t *testing.T) {
	provider := NewTencentProvider()
	provider.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
		if args[0] == "get" {
			return mustJSON(map[string]any{"items": []any{map[string]any{
				"kind": "Deployment", "metadata": map[string]any{"name": "opl-workspace-alpha", "labels": map[string]any{"oplcloud.cn/workspace-id": "workspace-alpha"}},
			}}}), nil
		}
		if len(args) > 3 && args[3] == "cat" {
			return nil, fmt.Errorf("exit status 1: forbidden")
		}
		return nil, nil
	}
	err := provider.PublishWorkspaceContent(context.Background(), "workspace-alpha", "inputs/paper.txt", []byte("expected"))
	if err == nil || !strings.Contains(err.Error(), "workspace_content_readback_command_failed") || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("readback diagnostics = %v", err)
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
