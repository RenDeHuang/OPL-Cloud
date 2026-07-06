package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/controlplane"
)

func TestCreateWorkspaceHTTPRequiresAttachment(t *testing.T) {
	server := NewServer(controlplane.NewService(nil, nil))
	body := bytes.NewBufferString(`{"accountId":"acct-alpha","ownerId":"usr-owner","name":"Alpha Lab","packageId":"basic"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/workspaces", body)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
}

func TestCreateComputeAllocationUsesProvisionerShape(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "provisioner")
	script := `#!/bin/sh
cat >/dev/null
printf '{"ok":true,"operationId":"op-alpha","poolId":"pool-basic","nodePoolId":"np-basic","instanceId":"ins-alpha","nodeName":"10.0.0.8","privateIp":"10.0.0.8","status":"running","providerRequestId":"req-alpha","providerData":{"machineName":"machine-alpha"}}\n'
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake provisioner: %v", err)
	}
	t.Setenv("OPL_TENCENT_PROVISIONER_BIN", bin)
	t.Setenv("OPL_WORKSPACE_IMAGE", "workspace-image:test")
	server := NewServer(controlplane.NewService(nil, nil))
	req := httptest.NewRequest(http.MethodPost, "/api/compute-allocations", bytes.NewBufferString(`{"accountId":"acct-alpha","packageId":"basic","name":"Production Compute"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["id"] == "compute-local" {
		t.Fatalf("compute allocation still uses local stub id")
	}
	for attempt := 0; attempt < 20 && body["status"] != "running"; attempt++ {
		time.Sleep(10 * time.Millisecond)
		getReq := httptest.NewRequest(http.MethodGet, "/api/compute-allocations/"+body["id"].(string), nil)
		getRec := httptest.NewRecorder()
		server.ServeHTTP(getRec, getReq)
		if getRec.Code != http.StatusOK {
			t.Fatalf("get status = %d, want %d: %s", getRec.Code, http.StatusOK, getRec.Body.String())
		}
		if err := json.NewDecoder(getRec.Body).Decode(&body); err != nil {
			t.Fatalf("decode get response: %v", err)
		}
	}
	if body["provider"] != "tencent-tke" || body["nodeName"] == "" || body["instanceId"] == "" || body["billingStatus"] != "active" {
		t.Fatalf("unexpected compute shape: %#v", body)
	}
	nodeSelector, _ := body["nodeSelector"].(map[string]any)
	if nodeSelector["cloud.tencent.com/node-instance-id"] != "machine-alpha" {
		t.Fatalf("node selector = %#v, want Tencent instance id label machine-alpha", nodeSelector)
	}
}

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
	compute := map[string]any{"id": "compute-alpha", "ownerAccountId": "acct-alpha", "packageId": "basic", "runtime": map[string]any{"nodeSelector": map[string]any{"cloud.tencent.com/node-instance-id": "np-basic-2"}}}
	storage := map[string]any{"providerResourceId": "pvc/opl-storage-alpha-data"}
	var manifest map[string]any
	if err := json.Unmarshal(workspaceManifest("ws-alpha", "Alpha", "token", "opl-compute-alpha", compute, storage), &manifest); err != nil {
		t.Fatalf("decode workspace manifest: %v", err)
	}
	var deployment map[string]any
	for _, item := range manifest["items"].([]any) {
		candidate := item.(map[string]any)
		if candidate["kind"] == "Deployment" {
			deployment = candidate
		}
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
	if env["WEBUI_AUTH"] != "False" || env["ENABLE_PERSISTENT_CONFIG"] != "False" {
		t.Fatalf("workspace must disable app login through the runtime auth contract: %#v", env)
	}
}

func TestRuntimeStatusRecoversWorkspaceResourcesFromKubernetesLabels(t *testing.T) {
	t.Setenv("OPL_WORKSPACE_IMAGE", "workspace-image:test")
	app := newRuntimeApp()
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
	app.kubectl = func(_ context.Context, args []string, _ []byte) ([]byte, error) {
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

	status := app.runtimeStatus(context.Background(), "ws-alpha")

	if status["ready"] != true {
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

func TestOverviewHTTP(t *testing.T) {
	server := NewServer(controlplane.NewService(nil, nil))
	req := httptest.NewRequest(http.MethodGet, "/api/overview", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode overview: %v", err)
	}
	if body["service"] != "control-plane" {
		t.Fatalf("service = %v, want control-plane", body["service"])
	}
}

func TestOperatorLoginUsesConfiguredToken(t *testing.T) {
	t.Setenv("OPL_OPERATOR_SUMMARY_TOKEN", "operator-secret")
	server := NewServer(controlplane.NewService(nil, nil))
	req := httptest.NewRequest(http.MethodPost, "/api/auth/operator-login", bytes.NewBufferString(`{"operatorToken":"operator-secret"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Header().Get("x-opl-csrf-token") == "" {
		t.Fatalf("expected csrf response header")
	}
	if rec.Header().Get("Set-Cookie") == "" {
		t.Fatalf("expected session cookie")
	}
}

func TestOperatorLoginRejectsInvalidToken(t *testing.T) {
	t.Setenv("OPL_OPERATOR_SUMMARY_TOKEN", "operator-secret")
	server := NewServer(controlplane.NewService(nil, nil))
	req := httptest.NewRequest(http.MethodPost, "/api/auth/operator-login", bytes.NewBufferString(`{"operatorToken":"wrong"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestActiveConsoleAPIRoutesReachControlPlane(t *testing.T) {
	server := NewServer(controlplane.NewService(nil, nil))
	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/auth/me", ""},
		{http.MethodGet, "/api/healthz", ""},
		{http.MethodGet, "/api/state", ""},
		{http.MethodGet, "/api/management/state", ""},
		{http.MethodGet, "/api/operator/summary", ""},
		{http.MethodGet, "/api/runtime/readiness", ""},
		{http.MethodGet, "/api/production/readiness", ""},
		{http.MethodGet, "/api/compute-pools", ""},
		{http.MethodGet, "/api/compute-allocations", ""},
		{http.MethodGet, "/api/compute-allocations/compute-alpha", ""},
		{http.MethodGet, "/api/support/tickets", ""},
		{http.MethodGet, "/api/ledger/task-receipts", ""},
		{http.MethodPost, "/api/auth/logout", `{}`},
		{http.MethodPost, "/api/organizations", `{"name":"Lab","billingAccountId":"acct-lab"}`},
		{http.MethodPost, "/api/organizations/members", `{"organizationId":"org-lab","userId":"usr-owner","role":"member"}`},
		{http.MethodPost, "/api/users", `{"email":"owner@example.com","accountId":"acct-lab","password":"secret"}`},
		{http.MethodPost, "/api/users/disable", `{"userId":"usr-owner"}`},
		{http.MethodPost, "/api/users/delete", `{"userId":"usr-owner"}`},
		{http.MethodPost, "/api/billing/topups", `{"accountId":"acct-lab","amount":100,"idempotencyKey":"topup-test"}`},
		{http.MethodPost, "/api/billing/resource-settlements", `{"accountId":"acct-lab","hours":1}`},
		{http.MethodPost, "/api/billing/reconciliation", `{"report":{"id":"recon-test","generatedAt":"2026-07-06T00:00:00Z"}}`},
		{http.MethodPost, "/api/compute-allocations", `{"packageId":"basic","name":"compute"}`},
		{http.MethodPost, "/api/compute-allocations/compute-alpha/destroy", `{"confirm":true}`},
		{http.MethodPost, "/api/storage-volumes", `{"name":"data","sizeGb":10}`},
		{http.MethodPost, "/api/storage-volumes/destroy", `{"storageId":"storage-alpha"}`},
		{http.MethodPost, "/api/storage-attachments", `{"computeAllocationId":"compute-alpha","storageId":"storage-alpha","mountPath":"/data"}`},
		{http.MethodPost, "/api/storage-attachments/detach", `{"attachmentId":"attach-alpha"}`},
		{http.MethodPost, "/api/workspaces/reset-token", `{"workspaceId":"ws-alpha"}`},
		{http.MethodPost, "/api/workspaces/delete-token", `{"workspaceId":"ws-alpha"}`},
		{http.MethodPost, "/api/workspaces/runtime-status", `{"workspaceId":"ws-alpha"}`},
		{http.MethodPost, "/api/operator/cleanup-workspace-access", `{"reason":"test"}`},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			var body *bytes.Buffer
			if tc.body != "" {
				body = bytes.NewBufferString(tc.body)
			} else {
				body = bytes.NewBuffer(nil)
			}
			req := httptest.NewRequest(tc.method, tc.path, body)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			server.ServeHTTP(rec, req)

			if rec.Code == http.StatusMethodNotAllowed {
				t.Fatalf("status = %d for %s %s", rec.Code, tc.method, tc.path)
			}
			if rec.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("content-type = %q, want application/json", rec.Header().Get("Content-Type"))
			}
			var payload any
			if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
				t.Fatalf("decode response: %v", err)
			}
		})
	}
}
