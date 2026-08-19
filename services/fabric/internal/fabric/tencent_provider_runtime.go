package fabric

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"time"

	"opl-cloud/services/fabric/internal/protectedresource"
)

func (p *TencentProvider) CreateWorkspaceRuntime(ctx context.Context, input WorkspaceRuntimeInput, compute ComputeAllocation, volume StorageVolume) (WorkspaceRuntime, error) {
	return p.createWorkspaceRuntime(ctx, input, compute, volume, tencentWorkspaceRuntimeGatewayBinding{})
}

type tencentWorkspaceRuntimeGatewayBinding struct {
	WorkspaceAPIKeyID int64
	SecretRef         string
	Fingerprint       string
}

func (p *TencentProvider) createWorkspaceRuntime(ctx context.Context, input WorkspaceRuntimeInput, compute ComputeAllocation, volume StorageVolume, gateway tencentWorkspaceRuntimeGatewayBinding) (WorkspaceRuntime, error) {
	if compute.ID == "" || volume.ID == "" {
		return WorkspaceRuntime{}, fmt.Errorf("workspace_runtime_resources_required")
	}
	if !validWorkspaceRuntimeImageIdentity(input.ImageID) {
		return WorkspaceRuntime{}, fmt.Errorf("workspace_image_identity_invalid")
	}
	workspacePlan, planErr := p.workspacePlanForContext(ctx, compute.PackageID)
	if planErr != nil {
		return WorkspaceRuntime{}, planErr
	}
	now := time.Now().UTC()
	serviceName := firstNonEmpty(compute.ServiceName, k8sName(compute.ID))
	credentialSeed := stableID(input.WorkspaceID, input.IdempotencyKey)[:24]
	runtimeOperationID := firstNonEmpty(input.RuntimeOperationID, input.IdempotencyKey)
	input.RuntimeOperationID = runtimeOperationID
	runtimeID := "rt_" + stableSuffix(input.WorkspaceID, runtimeOperationID)[:18]
	tags := oplCostTags(compute.AccountID, input.WorkspaceID, runtimeID, runtimeOperationID)
	runtimeTarget := protectedresource.Target{PackageID: compute.PackageID, NodePoolID: compute.NodePoolID, MachineID: compute.MachineName, NodeName: compute.NodeName, CVMID: firstNonEmpty(compute.InstanceID, compute.CVMInstanceID)}
	mutation, err := beginProviderMutation(ctx, "tencent_workspace_runtime_apply", "workspace_runtime", runtimeID, serviceName)
	if err != nil {
		return WorkspaceRuntime{}, err
	}
	if mutation != nil && !mutation.Fresh {
		runtime, readErr := p.readWorkspaceRuntime(ctx, input, runtimeID, serviceName, tags, gateway)
		if errors.Is(readErr, ErrWorkspaceLaunchResourceAbsent) {
			claimed, claimErr := mutation.claimReplay(ctx)
			if claimErr != nil || !claimed {
				return WorkspaceRuntime{}, firstNonNil(claimErr, ErrWorkspaceLaunchPending)
			}
			runtime, readErr = p.readWorkspaceRuntime(ctx, input, runtimeID, serviceName, tags, gateway)
			if errors.Is(readErr, ErrWorkspaceLaunchResourceAbsent) {
				if dispatchErr := mutation.markReplayDispatch(ctx); dispatchErr != nil {
					return WorkspaceRuntime{}, dispatchErr
				}
				if _, readErr = p.callKubectl(ctx, []string{"apply", "-f", "-"}, workspaceManifestWithGatewayPlan(input, input.WorkspaceID, credentialSeed, runtimeID, serviceName, compute, volume, tags, gateway, workspacePlan.Compute), runtimeTarget); readErr == nil {
					runtime, readErr = p.readWorkspaceRuntime(ctx, input, runtimeID, serviceName, tags, gateway)
				}
			}
		}
		if readErr != nil {
			_ = mutation.complete(ctx, "", WorkspaceRuntime{ID: runtimeID, WorkspaceID: input.WorkspaceID}, readErr)
			return WorkspaceRuntime{}, readErr
		}
		if completeErr := mutation.complete(ctx, runtime.ProviderRequestID, runtime, nil); completeErr != nil {
			return WorkspaceRuntime{}, completeErr
		}
		return runtime, nil
	}
	if _, err := p.callKubectl(ctx, []string{"apply", "-f", "-"}, workspaceManifestWithGatewayPlan(input, input.WorkspaceID, credentialSeed, runtimeID, serviceName, compute, volume, tags, gateway, workspacePlan.Compute), runtimeTarget); err != nil {
		_ = mutation.complete(ctx, "", WorkspaceRuntime{ID: runtimeID, WorkspaceID: input.WorkspaceID}, err)
		return WorkspaceRuntime{}, err
	}
	runtime, err := p.readWorkspaceRuntime(ctx, input, runtimeID, serviceName, tags, gateway)
	if err != nil {
		_ = mutation.complete(ctx, "", WorkspaceRuntime{ID: runtimeID, WorkspaceID: input.WorkspaceID}, err)
		return WorkspaceRuntime{}, err
	}
	runtime.ProviderRequestID = providerRequestID("runtime", input.IdempotencyKey)
	runtime.CreatedAt = now
	if err := mutation.complete(ctx, runtime.ProviderRequestID, runtime, nil); err != nil {
		return WorkspaceRuntime{}, err
	}
	return runtime, nil
}

func (p *TencentProvider) readWorkspaceRuntime(ctx context.Context, input WorkspaceRuntimeInput, runtimeID, serviceName string, tags map[string]string, gateway tencentWorkspaceRuntimeGatewayBinding) (WorkspaceRuntime, error) {
	runtime, err := p.WorkspaceRuntimeStatus(ctx, input.WorkspaceID)
	if err != nil {
		return runtime, err
	}
	if runtime.ID != runtimeID || runtime.OperationID != input.RuntimeOperationID || runtime.WorkspaceID != input.WorkspaceID ||
		runtime.ServiceName != serviceName || runtime.ImageID != input.ImageID || runtime.URL == "" || !reflect.DeepEqual(runtime.CostTags, tags) {
		return runtime, fmt.Errorf("workspace_runtime_readback_mismatch")
	}
	if gateway == (tencentWorkspaceRuntimeGatewayBinding{}) {
		runtime.Access.Password = ""
		return runtime, nil
	}
	binding, err := p.WorkspaceRuntimeGatewaySecret(ctx, input.WorkspaceID)
	if err != nil || !binding.Bound || binding.WorkspaceID != input.WorkspaceID || binding.WorkspaceAPIKeyID != gateway.WorkspaceAPIKeyID ||
		binding.SecretRef != gateway.SecretRef || binding.Fingerprint != gateway.Fingerprint {
		return runtime, firstNonNil(err, fmt.Errorf("workspace_runtime_gateway_secret_readback_mismatch"))
	}
	runtime.Access.Password = ""
	return runtime, nil
}

func (p *TencentProvider) DestroyWorkspaceRuntime(ctx context.Context, workspaceID string) (WorkspaceRuntime, error) {
	serviceName, err := p.workspaceRuntimeResourceNameForDestroy(ctx, workspaceID)
	if err != nil {
		return WorkspaceRuntime{}, err
	}
	if serviceName != "" {
		if _, err := p.callKubectl(ctx, []string{"delete", "deployment/" + serviceName, "service/" + serviceName, "networkpolicy/" + serviceName, "secret/" + serviceName + "-env", "--ignore-not-found=true"}, nil, protectedresource.Target{}); err != nil {
			return WorkspaceRuntime{}, err
		}
	}
	return WorkspaceRuntime{WorkspaceID: workspaceID, Status: "destroyed", ServiceName: serviceName}, nil
}

func (p *TencentProvider) workspaceRuntimeResourceNameForDestroy(ctx context.Context, workspaceID string) (string, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return "", nil
	}
	raw, err := p.callKubectl(ctx, []string{"get", "deployment,service,networkpolicy,secret", "-l", "oplcloud.cn/workspace-id=" + workspaceID, "-o", "json"}, nil, protectedresource.Target{})
	if err != nil {
		return "", err
	}
	names := map[string]bool{}
	for _, item := range kubectlItems(raw) {
		resource, ok := item.(map[string]any)
		if !ok || stringValue(nested(resource, "metadata", "labels", "oplcloud.cn/workspace-id")) != workspaceID {
			continue
		}
		name := stringValue(nested(resource, "metadata", "name"))
		if stringValue(resource["kind"]) == "Secret" && strings.HasSuffix(name, "-env") {
			name = strings.TrimSuffix(name, "-env")
		}
		if name != "" {
			names[name] = true
		}
	}
	if len(names) > 1 {
		return "", workspaceRuntimeStatusError("ownership_conflict")
	}
	for name := range names {
		return name, nil
	}
	return "", nil
}

func (p *TencentProvider) WorkspaceRuntimeStatus(ctx context.Context, workspaceID string) (WorkspaceRuntime, error) {
	serviceName, pvcName, err := p.workspaceRuntimeResourcesStrict(ctx, workspaceID, true)
	if err != nil {
		return WorkspaceRuntime{WorkspaceID: workspaceID}, err
	}
	if serviceName == "" || pvcName == "" {
		return WorkspaceRuntime{WorkspaceID: workspaceID}, ErrWorkspaceLaunchResourceAbsent
	}
	secretRef := serviceName + "-env"
	raw, err := p.callKubectl(ctx, []string{"get", "deployment/" + serviceName, "pvc/" + pvcName, "service/" + serviceName, "ingress/opl-cloud", "endpoints/" + serviceName, "secret/" + secretRef, "--ignore-not-found", "-o", "json"}, nil, protectedresource.Target{})
	if err != nil {
		return WorkspaceRuntime{WorkspaceID: workspaceID, ServiceName: serviceName}, workspaceRuntimeStatusError(computeClaimKubectlErrorClass(err))
	}
	policyRaw, err := p.callKubectl(ctx, []string{"get", "networkpolicy", "-o", "json"}, nil, protectedresource.Target{})
	if err != nil {
		return WorkspaceRuntime{WorkspaceID: workspaceID, ServiceName: serviceName}, workspaceRuntimeStatusError(computeClaimKubectlErrorClass(err))
	}
	items, err := strictKubectlItems(raw)
	if err != nil {
		return WorkspaceRuntime{WorkspaceID: workspaceID, ServiceName: serviceName}, workspaceRuntimeStatusError("provider_error")
	}
	networkPolicies, err := strictKubectlItems(policyRaw)
	if err != nil {
		return WorkspaceRuntime{WorkspaceID: workspaceID, ServiceName: serviceName}, workspaceRuntimeStatusError("provider_error")
	}
	deployment, err := exactWorkspaceRuntimeStatusResource(items, "Deployment", serviceName)
	if err != nil {
		return WorkspaceRuntime{WorkspaceID: workspaceID, ServiceName: serviceName}, err
	}
	pvc, err := exactWorkspaceRuntimeStatusResource(items, "PersistentVolumeClaim", pvcName)
	if err != nil {
		return WorkspaceRuntime{WorkspaceID: workspaceID, ServiceName: serviceName}, err
	}
	service, err := exactWorkspaceRuntimeStatusResource(items, "Service", serviceName)
	if err != nil {
		return WorkspaceRuntime{WorkspaceID: workspaceID, ServiceName: serviceName}, err
	}
	ingress, err := exactWorkspaceRuntimeStatusResource(items, "Ingress", "opl-cloud")
	if err != nil {
		return WorkspaceRuntime{WorkspaceID: workspaceID, ServiceName: serviceName}, err
	}
	endpoints, err := exactWorkspaceRuntimeStatusResource(items, "Endpoints", serviceName)
	if err != nil {
		return WorkspaceRuntime{WorkspaceID: workspaceID, ServiceName: serviceName}, err
	}
	secret, err := exactWorkspaceRuntimeStatusResource(items, "Secret", secretRef)
	if err != nil {
		return WorkspaceRuntime{WorkspaceID: workspaceID, ServiceName: serviceName}, err
	}
	access, credentialCheck := runtimeAccessFromSecret(secret, secretRef)
	access.CredentialVersion = firstNonEmpty(stringValue(nested(deployment, "spec", "template", "metadata", "annotations", "opl.medopl.cn/credential-revision")), access.CredentialVersion)
	pods, err := p.workspacePods(ctx, workspaceID)
	if err != nil {
		return WorkspaceRuntime{WorkspaceID: workspaceID, ServiceName: serviceName}, workspaceRuntimeStatusError(computeClaimKubectlErrorClass(err))
	}
	podDetails := podRuntimeDetails(pods)
	readyPodUsesPVC := false
	for _, item := range pods {
		pod, _ := item.(map[string]any)
		if conditionStatuses(nested(pod, "status", "conditions"))["Ready"] == "True" && workloadUsesPVC(pod, pvcName) {
			readyPodUsesPVC = true
			break
		}
	}
	readyReplicas := number(nested(deployment, "status", "readyReplicas"))
	availableReplicas := number(nested(deployment, "status", "availableReplicas"))
	image := stringValue(firstContainerField(deployment, "image"))
	readyAddresses := endpointReadyAddresses(endpoints)
	checks := []Check{
		{Name: "deployment_ready", OK: readyReplicas > 0 && availableReplicas > 0, Details: mergeDetails(map[string]any{"readyReplicas": readyReplicas, "availableReplicas": availableReplicas}, podDetails)},
		{Name: "workspace_image_pulled", OK: validWorkspaceRuntimeImageIdentity(image), Details: map[string]any{"imageId": image}},
		{Name: "pvc_bound", OK: stringValue(nested(pvc, "status", "phase")) == "Bound"},
		{Name: "deployment_uses_retained_pvc", OK: workloadUsesPVC(deployment, pvcName)},
		{Name: "ready_pod_uses_retained_pvc", OK: readyPodUsesPVC, Details: podDetails},
		{Name: "service_targets_workspace", OK: selectorMatches(service, deployment)},
		{Name: "workspace_network_policy", OK: workspaceNetworkPoliciesReady(networkPolicies, deployment, pods)},
		{Name: "workspace_runtime_isolation", OK: workspaceRuntimeIsolationReady(deployment, pods)},
		{Name: "service_endpoints_ready", OK: readyAddresses > 0, Details: mergeDetails(map[string]any{"readyAddresses": readyAddresses}, podDetails)},
		{Name: "ingress_routes_workspace_gateway", OK: ingressRoutesGateway(ingress)},
		credentialCheck,
	}
	ready := true
	for _, check := range checks {
		if !check.OK {
			ready = false
		}
	}
	status := "running"
	if !ready {
		status = "unready"
	}
	runtimeID := stringValue(nested(deployment, "metadata", "labels", "oplcloud.cn/runtime-id"))
	runtimeOperationID := stringValue(nested(deployment, "metadata", "labels", "oplcloud.cn/runtime-operation-id"))
	costTags := map[string]string{
		"opl_account_id":   stringValue(nested(deployment, "metadata", "annotations", "opl_account_id")),
		"opl_workspace_id": stringValue(nested(deployment, "metadata", "annotations", "opl_workspace_id")),
		"opl_resource_id":  stringValue(nested(deployment, "metadata", "annotations", "opl_resource_id")),
		"opl_operation_id": stringValue(nested(deployment, "metadata", "annotations", "opl_operation_id")),
	}
	if runtimeID == "" || runtimeOperationID == "" || costTags["opl_workspace_id"] != workspaceID || costTags["opl_resource_id"] != runtimeID || costTags["opl_operation_id"] != runtimeOperationID || costTags["opl_account_id"] == "" {
		return WorkspaceRuntime{WorkspaceID: workspaceID, ServiceName: serviceName}, workspaceRuntimeStatusError("readback_mismatch")
	}
	return WorkspaceRuntime{ID: runtimeID, OperationID: runtimeOperationID, WorkspaceID: workspaceID, URL: fmt.Sprintf("https://%s/w/%s/", workspaceDomain(), workspaceID), Status: status, ServiceName: serviceName, ImageID: image, Access: access, Ready: ready, Checks: checks, CostTags: costTags}, nil
}

func (*TencentProvider) WorkspaceRuntimeProviderFacts(runtime WorkspaceRuntime) ProviderResourceFacts {
	return ProviderResourceFacts{ProviderID: runtime.ServiceName, Status: runtime.Status}
}

func (p *TencentProvider) BindWorkspaceRuntimeGatewaySecret(ctx context.Context, input WorkspaceRuntimeGatewaySecretInput) (WorkspaceRuntimeGatewaySecretBinding, error) {
	serviceName, _, err := p.workspaceRuntimeResourcesStrict(ctx, input.WorkspaceID, false)
	if err != nil || serviceName == "" {
		return WorkspaceRuntimeGatewaySecretBinding{}, fmt.Errorf("workspace_runtime_not_found")
	}
	patch := mustJSON(map[string]any{"spec": map[string]any{"template": map[string]any{
		"metadata": map[string]any{"annotations": map[string]any{
			"opl.medopl.cn/gateway-secret-ref":  input.SecretRef,
			"opl.medopl.cn/gateway-key-id":      strconv.FormatInt(input.WorkspaceAPIKeyID, 10),
			"opl.medopl.cn/gateway-fingerprint": input.Fingerprint,
		}},
		"spec": map[string]any{"volumes": []any{map[string]any{
			"name": "workspace-secrets",
			"projected": map[string]any{"sources": []any{
				map[string]any{"secret": map[string]any{"name": serviceName + "-env", "items": []any{
					map[string]any{"key": "webui_password", "path": "opl_webui_password"},
					map[string]any{"key": "webui_session_secret", "path": "webui_session_secret"},
				}}},
				map[string]any{"secret": map[string]any{"name": input.SecretRef, "items": []any{
					map[string]any{"key": "opl_gateway_api_key", "path": "opl_gateway_api_key"},
				}}},
			}},
		}}},
	}}})
	if _, err := p.callKubectl(ctx, []string{"patch", "deployment/" + serviceName, "--type=strategic", "-p", string(patch)}, nil, protectedresource.Target{}); err != nil {
		return WorkspaceRuntimeGatewaySecretBinding{}, err
	}
	return p.WorkspaceRuntimeGatewaySecret(ctx, input.WorkspaceID)
}

func (p *TencentProvider) WorkspaceRuntimeGatewaySecret(ctx context.Context, workspaceID string) (WorkspaceRuntimeGatewaySecretBinding, error) {
	serviceName, _, err := p.workspaceRuntimeResourcesStrict(ctx, workspaceID, false)
	if err != nil || serviceName == "" {
		return WorkspaceRuntimeGatewaySecretBinding{}, fmt.Errorf("workspace_runtime_not_found")
	}
	raw, err := p.callKubectl(ctx, []string{"get", "deployment/" + serviceName, "-o", "json"}, nil, protectedresource.Target{})
	if err != nil {
		return WorkspaceRuntimeGatewaySecretBinding{}, err
	}
	var deployment map[string]any
	if json.Unmarshal(raw, &deployment) != nil || stringValue(nested(deployment, "metadata", "labels", "oplcloud.cn/workspace-id")) != workspaceID {
		return WorkspaceRuntimeGatewaySecretBinding{}, fmt.Errorf("workspace_runtime_gateway_secret_readback_mismatch")
	}
	secretRef := stringValue(nested(deployment, "spec", "template", "metadata", "annotations", "opl.medopl.cn/gateway-secret-ref"))
	fingerprint := stringValue(nested(deployment, "spec", "template", "metadata", "annotations", "opl.medopl.cn/gateway-fingerprint"))
	keyID, parseErr := strconv.ParseInt(stringValue(nested(deployment, "spec", "template", "metadata", "annotations", "opl.medopl.cn/gateway-key-id")), 10, 64)
	bound := false
	volumes, _ := nested(deployment, "spec", "template", "spec", "volumes").([]any)
	for _, rawVolume := range volumes {
		volume, _ := rawVolume.(map[string]any)
		if stringValue(volume["name"]) != "workspace-secrets" {
			continue
		}
		sources, _ := nested(volume, "projected", "sources").([]any)
		for _, rawSource := range sources {
			source, _ := rawSource.(map[string]any)
			bound = bound || stringValue(nested(source, "secret", "name")) == secretRef
		}
	}
	if parseErr != nil || keyID <= 0 || secretRef == "" || fingerprint == "" || !bound {
		return WorkspaceRuntimeGatewaySecretBinding{}, fmt.Errorf("workspace_runtime_gateway_secret_readback_mismatch")
	}
	return WorkspaceRuntimeGatewaySecretBinding{WorkspaceID: workspaceID, WorkspaceAPIKeyID: keyID, SecretRef: secretRef, Fingerprint: fingerprint, Bound: true}, nil
}

func runtimeAccessFromSecret(secret map[string]any, secretRef string) (RuntimeAccess, Check) {
	access := RuntimeAccess{Username: webuiUsername, CredentialStatus: "missing", SecretRef: secretRef}
	encoded := stringValue(nested(secret, "data", "webui_password"))
	password, err := base64.StdEncoding.DecodeString(encoded)
	if err == nil && len(password) > 0 {
		access.Password = string(password)
		access.CredentialStatus = "configured"
		access.CredentialVersion = "v1"
	}
	return access, Check{Name: "workspace_credentials_configured", OK: access.CredentialStatus == "configured"}
}

func (p *TencentProvider) workspacePods(ctx context.Context, workspaceID string) ([]any, error) {
	raw, err := p.callKubectl(ctx, []string{"get", "pod", "-l", "oplcloud.cn/workspace-id=" + workspaceID, "-o", "json"}, nil, protectedresource.Target{})
	if err != nil {
		return nil, err
	}
	return strictKubectlItems(raw)
}

func (p *TencentProvider) RuntimeHealthSummary(ctx context.Context) (RuntimeHealthSummary, error) {
	raw, err := p.callKubectl(ctx, []string{"get", "deployment,pod", "-l", "oplcloud.cn/workspace-id", "-o", "json"}, nil, protectedresource.Target{})
	if err != nil {
		return RuntimeHealthSummary{}, err
	}
	var list struct {
		Kind  string `json:"kind"`
		Items []any  `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil || list.Kind != "List" || list.Items == nil {
		return RuntimeHealthSummary{}, fmt.Errorf("workspace_runtime_summary_response_invalid")
	}
	deployments := map[string]map[string]any{}
	readyPods := map[string]bool{}
	for _, item := range list.Items {
		resource, _ := item.(map[string]any)
		workspaceID := stringValue(nested(resource, "metadata", "labels", "oplcloud.cn/workspace-id"))
		if workspaceID == "" {
			continue
		}
		switch stringValue(resource["kind"]) {
		case "Deployment":
			if _, exists := deployments[workspaceID]; exists {
				return RuntimeHealthSummary{}, fmt.Errorf("workspace_runtime_summary_duplicate_deployment")
			}
			deployments[workspaceID] = resource
		case "Pod":
			if stringValue(nested(resource, "status", "phase")) == "Running" && conditionStatuses(nested(resource, "status", "conditions"))["Ready"] == "True" {
				readyPods[workspaceID] = true
			}
		}
	}
	summary := RuntimeHealthSummary{Total: len(deployments)}
	for workspaceID, deployment := range deployments {
		if number(nested(deployment, "status", "readyReplicas")) > 0 && number(nested(deployment, "status", "availableReplicas")) > 0 && readyPods[workspaceID] {
			summary.Ready++
		} else {
			summary.Unready++
		}
	}
	return summary, nil
}

func (p *TencentProvider) Readiness(ctx context.Context) (map[string]any, error) {
	required := []string{"OPL_WORKSPACE_DOMAIN", "OPL_CLOUD_IMAGE", "OPL_WORKSPACE_IMAGE", "OPL_K8S_NAMESPACE", "OPL_IMAGE_PULL_SECRET_NAME", "OPL_WORKSPACE_STORAGE_CLASS", "OPL_TENCENT_PROVISIONER_BIN", "TENCENT_DEPLOY_KUBECONFIG_REF", "RUN_TENCENT_CREATE_RELEASE_EXECUTION"}
	missing := []string{}
	for _, key := range required {
		if strings.TrimSpace(os.Getenv(key)) == "" {
			missing = append(missing, key)
		}
	}
	if os.Getenv("RUN_TENCENT_CREATE_RELEASE_EXECUTION") != "1" {
		missing = append(missing, "RUN_TENCENT_CREATE_RELEASE_EXECUTION=1")
	}
	missingTools := []string{}
	if _, err := exec.LookPath("kubectl"); err != nil {
		missingTools = append(missingTools, "kubectl")
	}
	response, err := p.provision(ctx, provisionerRequest{Action: "readiness"})
	if err != nil || !response.OK {
		missing = append(missing, response.MissingEnv...)
		if response.ErrorCode != "" {
			missing = append(missing, response.ErrorCode)
		} else if err != nil {
			missing = append(missing, "provisioner_failed")
		}
	}
	podRaw, podErr := p.callKubectl(ctx, []string{"get", "pod", "-o", "json"}, nil, protectedresource.Target{})
	pods := kubectlItems(podRaw)
	imageChecks := map[string]bool{
		"control_plane_image_id": podImageIDsMatch(pods, "app.kubernetes.io/component", "control-plane", "control-plane", os.Getenv("OPL_CLOUD_IMAGE")),
		"ledger_image_id":        podImageIDsMatch(pods, "app.kubernetes.io/component", "ledger", "ledger", os.Getenv("OPL_CLOUD_IMAGE")),
		"fabric_image_id":        podImageIDsMatch(pods, "app.kubernetes.io/component", "fabric", "fabric", os.Getenv("OPL_CLOUD_IMAGE")),
		"workspace_image_id":     podImageIDsMatch(pods, "oplcloud.cn/workspace-id", "", "workspace", os.Getenv("OPL_WORKSPACE_IMAGE")),
	}
	failedChecks := []any{}
	if podErr != nil {
		failedChecks = append(failedChecks, "ready_pod_image_ids")
	} else {
		for _, name := range []string{"control_plane_image_id", "ledger_image_id", "fabric_image_id", "workspace_image_id"} {
			if !imageChecks[name] {
				failedChecks = append(failedChecks, name)
			}
		}
	}
	cloudImagesReady := podErr == nil && imageChecks["control_plane_image_id"] && imageChecks["ledger_image_id"] && imageChecks["fabric_image_id"]
	workspaceImagesReady := podErr == nil && imageChecks["workspace_image_id"]
	immutableImagesReady := cloudImagesReady && workspaceImagesReady
	uniqueMissing := uniqueStrings(missing)
	return map[string]any{"provider": "tencent-tke", "ready": len(uniqueMissing) == 0 && len(missingTools) == 0 && immutableImagesReady, "cloudImagesReady": cloudImagesReady, "workspaceImagesReady": workspaceImagesReady, "immutableImagesReady": immutableImagesReady, "missingEnv": uniqueMissing, "missingTools": missingTools, "failedChecks": failedChecks}, nil
}

func podImageIDsMatch(pods []any, labelKey, labelValue, containerName, expected string) bool {
	expectedDigest, ok := immutableImageDigest(expected)
	if !ok {
		return false
	}
	found := false
	for _, item := range pods {
		pod, _ := item.(map[string]any)
		labels, _ := nested(pod, "metadata", "labels").(map[string]any)
		label := stringValue(labels[labelKey])
		if label == "" || (labelValue != "" && label != labelValue) || stringValue(nested(pod, "status", "phase")) != "Running" || conditionStatuses(nested(pod, "status", "conditions"))["Ready"] != "True" {
			continue
		}
		containerFound := false
		statuses, _ := nested(pod, "status", "containerStatuses").([]any)
		for _, item := range statuses {
			status, _ := item.(map[string]any)
			if stringValue(status["name"]) != containerName {
				continue
			}
			containerFound = true
			found = true
			actualDigest, ok := runtimeImageDigest(stringValue(status["imageID"]))
			if status["ready"] != true || !ok || actualDigest != expectedDigest {
				return false
			}
		}
		if !containerFound {
			return false
		}
	}
	return found
}

func immutableImageDigest(value string) (string, bool) {
	repository, digest, ok := strings.Cut(strings.TrimSpace(value), "@")
	if !ok || repository == "" || strings.Contains(digest, "@") || !strings.HasPrefix(digest, "sha256:") || len(digest) != len("sha256:")+64 {
		return "", false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
	return digest, err == nil
}

func runtimeImageDigest(value string) (string, bool) {
	value = strings.TrimSpace(value)
	for _, prefix := range []string{"docker-pullable://", "containerd://"} {
		if strings.HasPrefix(value, prefix) {
			value = strings.TrimPrefix(value, prefix)
			break
		}
	}
	if strings.Contains(value, "://") {
		return "", false
	}
	if strings.Contains(value, "@") {
		return immutableImageDigest(value)
	}
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return "", false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return value, err == nil
}

func (p *TencentProvider) workspaceRuntimeResourcesStrict(ctx context.Context, workspaceID string, includeSecret bool) (string, string, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return "", "", nil
	}
	resourceKinds := "deployment,service,networkpolicy"
	if includeSecret {
		resourceKinds += ",secret"
	}
	raw, err := p.callKubectl(ctx, []string{"get", resourceKinds, "-l", "oplcloud.cn/workspace-id=" + workspaceID, "-o", "json"}, nil, protectedresource.Target{})
	if err != nil {
		return "", "", workspaceRuntimeStatusError(computeClaimKubectlErrorClass(err))
	}
	items, err := strictKubectlItems(raw)
	if err != nil {
		return "", "", workspaceRuntimeStatusError("provider_error")
	}
	if len(items) == 0 {
		return "", "", nil
	}
	deployment, err := exactWorkspaceRuntimeDiscoveryResource(items, "Deployment", workspaceID)
	if err != nil {
		return "", "", err
	}
	service, err := exactWorkspaceRuntimeDiscoveryResource(items, "Service", workspaceID)
	if err != nil {
		return "", "", err
	}
	networkPolicy, err := exactWorkspaceRuntimeDiscoveryResource(items, "NetworkPolicy", workspaceID)
	if err != nil {
		return "", "", err
	}
	serviceName := stringValue(nested(deployment, "metadata", "name"))
	if serviceName == "" || stringValue(nested(service, "metadata", "name")) != serviceName || stringValue(nested(networkPolicy, "metadata", "name")) != serviceName {
		return "", "", workspaceRuntimeStatusError("readback_mismatch")
	}
	if includeSecret {
		secret, secretErr := exactWorkspaceRuntimeDiscoveryResource(items, "Secret", workspaceID)
		if secretErr != nil {
			return "", "", secretErr
		}
		if stringValue(nested(secret, "metadata", "name")) != serviceName+"-env" {
			return "", "", workspaceRuntimeStatusError("readback_mismatch")
		}
	}
	pvcName, ok := singleWorkspaceRuntimePVCClaimName(deployment)
	if !ok {
		return "", "", workspaceRuntimeStatusError("readback_mismatch")
	}
	return serviceName, pvcName, nil
}

func exactWorkspaceRuntimeDiscoveryResource(items []any, kind, workspaceID string) (map[string]any, error) {
	matches := []map[string]any{}
	for _, item := range items {
		resource, ok := item.(map[string]any)
		if ok && stringValue(resource["kind"]) == kind && stringValue(nested(resource, "metadata", "labels", "oplcloud.cn/workspace-id")) == workspaceID {
			matches = append(matches, resource)
		}
	}
	if len(matches) > 1 {
		return nil, workspaceRuntimeStatusError("ownership_conflict")
	}
	if len(matches) != 1 {
		return nil, workspaceRuntimeStatusError("readback_mismatch")
	}
	return matches[0], nil
}

func exactWorkspaceRuntimeStatusResource(items []any, kind, name string) (map[string]any, error) {
	matches := []map[string]any{}
	for _, item := range items {
		resource, ok := item.(map[string]any)
		if ok && stringValue(resource["kind"]) == kind {
			matches = append(matches, resource)
		}
	}
	if len(matches) > 1 || len(matches) == 1 && stringValue(nested(matches[0], "metadata", "name")) != name {
		return nil, workspaceRuntimeStatusError("ownership_conflict")
	}
	if len(matches) != 1 {
		return nil, workspaceRuntimeStatusError("readback_mismatch")
	}
	return matches[0], nil
}

func singleWorkspaceRuntimePVCClaimName(deployment map[string]any) (string, bool) {
	volumes, _ := nested(deployment, "spec", "template", "spec", "volumes").([]any)
	claims := []string{}
	for _, volume := range volumes {
		asMap, _ := volume.(map[string]any)
		if claim := stringValue(nested(asMap, "persistentVolumeClaim", "claimName")); claim != "" {
			claims = append(claims, claim)
		}
	}
	return firstNonEmpty(claims...), len(claims) == 1
}

func workspaceRuntimeStatusError(class string) error {
	switch class {
	case "ownership_conflict", "iam_rbac", "timeout", "provider_error", "readback_mismatch":
	default:
		class = "provider_error"
	}
	return fmt.Errorf("workspace_runtime_status_%s", class)
}
