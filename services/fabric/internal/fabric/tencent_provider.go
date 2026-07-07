package fabric

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

const (
	defaultNamespace = "opl-cloud"
	gatewayService   = "opl-cloud-control-plane"
)

type TencentProvider struct {
	provision func(context.Context, provisionerRequest) (provisionerResponse, error)
	kubectl   func(context.Context, []string, []byte) ([]byte, error)
}

func NewTencentProvider() *TencentProvider {
	return &TencentProvider{provision: executeProvisioner, kubectl: executeKubectl}
}

type provisionerRequest struct {
	Action     string                `json:"action"`
	DryRun     bool                  `json:"dryRun,omitempty"`
	AccountID  string                `json:"accountId,omitempty"`
	UserID     string                `json:"userId,omitempty"`
	PackageID  string                `json:"packageId,omitempty"`
	Pool       provisionerPool       `json:"pool,omitempty"`
	Allocation provisionerAllocation `json:"allocation,omitempty"`
}

type provisionerPool struct {
	ID           string            `json:"id,omitempty"`
	PackageID    string            `json:"packageId,omitempty"`
	InstanceType string            `json:"instanceType,omitempty"`
	NodePoolID   string            `json:"nodePoolId,omitempty"`
	Labels       map[string]string `json:"desiredNodeLabels,omitempty"`
}

type provisionerAllocation struct {
	ID          string `json:"id,omitempty"`
	InstanceID  string `json:"instanceId,omitempty"`
	MachineName string `json:"machineName,omitempty"`
	NodeName    string `json:"nodeName,omitempty"`
	PrivateIP   string `json:"privateIp,omitempty"`
	PublicIP    string `json:"publicIp,omitempty"`
}

type provisionerResponse struct {
	OK                bool              `json:"ok"`
	OperationID       string            `json:"operationId,omitempty"`
	PoolID            string            `json:"poolId,omitempty"`
	NodePoolID        string            `json:"nodePoolId,omitempty"`
	InstanceID        string            `json:"instanceId,omitempty"`
	NodeName          string            `json:"nodeName,omitempty"`
	PrivateIP         string            `json:"privateIp,omitempty"`
	PublicIP          string            `json:"publicIp,omitempty"`
	Status            string            `json:"status,omitempty"`
	ProviderRequestID string            `json:"providerRequestId,omitempty"`
	ProviderData      map[string]string `json:"providerData,omitempty"`
	ErrorCode         string            `json:"errorCode,omitempty"`
	Message           string            `json:"message,omitempty"`
	Retryable         bool              `json:"retryable,omitempty"`
	MissingEnv        []string          `json:"missingEnv,omitempty"`
}

type plan struct {
	ID           string
	Server       string
	CPU          int
	MemoryGB     int
	DiskGB       int
	InstanceType string
	NodePoolID   string
}

func (p *TencentProvider) CreateComputeAllocation(ctx context.Context, input ComputeAllocationInput) (ComputeAllocation, error) {
	now := time.Now().UTC()
	packageID := firstNonEmpty(input.PackageID, "basic")
	id := fabricID("ca", input.WorkspaceID, now)
	plan := packagePlan(packageID)
	pool := provisionerPool{
		ID:           "pool-" + packageID,
		PackageID:    packageID,
		InstanceType: plan.InstanceType,
		NodePoolID:   plan.NodePoolID,
		Labels:       map[string]string{"oplcloud.cn/package-id": packageID, "oplcloud.cn/instance-type": plan.InstanceType},
	}
	response, err := p.provision(ctx, provisionerRequest{Action: "create_compute_allocation", DryRun: input.DryRun, AccountID: input.AccountID, PackageID: packageID, Pool: pool, Allocation: provisionerAllocation{ID: id}})
	if err != nil {
		return ComputeAllocation{}, err
	}
	if !response.OK {
		return ComputeAllocation{}, provisionerError(response)
	}
	nodeName := firstNonEmpty(response.NodeName, response.ProviderData["nodeName"])
	machineName := response.ProviderData["machineName"]
	serviceName := k8sName(id)
	return ComputeAllocation{
		ID:                 id,
		AccountID:          input.AccountID,
		WorkspaceID:        input.WorkspaceID,
		PackageID:          packageID,
		Status:             firstNonEmpty(response.Status, "running"),
		Provider:           "tencent-tke",
		ProviderResourceID: "node/" + nodeName,
		ProviderRequestID:  response.ProviderRequestID,
		PoolID:             firstNonEmpty(response.PoolID, pool.ID),
		NodePoolID:         firstNonEmpty(response.NodePoolID, pool.NodePoolID),
		InstanceID:         response.InstanceID,
		CVMInstanceID:      response.InstanceID,
		NodeName:           nodeName,
		MachineName:        machineName,
		PrivateIP:          response.PrivateIP,
		PublicIP:           response.PublicIP,
		ServiceName:        serviceName,
		NodeSelector:       tkeNodeSelector(response.ProviderData, nodeName),
		ProviderData:       response.ProviderData,
		CreatedAt:          now,
	}, nil
}

func (p *TencentProvider) DestroyComputeAllocation(ctx context.Context, allocation ComputeAllocation) (ComputeAllocation, error) {
	if allocation.ID == "" {
		return ComputeAllocation{}, fmt.Errorf("compute_allocation_id_required")
	}
	if allocation.ProviderRequestID == "" && allocation.NodePoolID == "" && allocation.MachineName == "" && allocation.NodeName == "" {
		allocation.Status = "destroyed"
		allocation.Provider = "tencent-tke"
		return allocation, nil
	}
	response, err := p.provision(ctx, provisionerRequest{
		Action:    "destroy_compute_allocation",
		AccountID: allocation.AccountID,
		PackageID: allocation.PackageID,
		Pool:      provisionerPool{ID: allocation.PoolID, NodePoolID: allocation.NodePoolID},
		Allocation: provisionerAllocation{
			ID:          allocation.ID,
			InstanceID:  firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID),
			MachineName: firstNonEmpty(allocation.MachineName, allocation.ProviderData["machineName"], allocation.NodeName),
			NodeName:    allocation.NodeName,
		},
	})
	if err != nil {
		return ComputeAllocation{}, err
	}
	if !response.OK {
		return ComputeAllocation{}, provisionerError(response)
	}
	if allocation.ServiceName != "" {
		_, _ = p.kubectl(ctx, []string{"delete", "deployment/" + allocation.ServiceName, "service/" + allocation.ServiceName, "secret/" + allocation.ServiceName + "-env", "--ignore-not-found=true"}, nil)
	}
	allocation.Status = "destroyed"
	allocation.ProviderRequestID = response.ProviderRequestID
	if allocation.Provider == "" {
		allocation.Provider = "tencent-tke"
	}
	return allocation, nil
}

func (p *TencentProvider) CreateStorageVolume(ctx context.Context, input StorageVolumeInput) (StorageVolume, error) {
	now := time.Now().UTC()
	sizeGB := int(math.Max(float64(input.SizeGB), 1))
	id := fabricID("vol", input.WorkspaceID, now)
	name := k8sName(id)
	if _, err := p.kubectl(ctx, []string{"apply", "-f", "-"}, pvcManifest(name, id, input.AccountID, sizeGB)); err != nil {
		return StorageVolume{}, err
	}
	return StorageVolume{ID: id, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "ready", Provider: "tencent-tke", ProviderResourceID: "pvc/" + name + "-data", ProviderRequestID: providerRequestID("storage", input.IdempotencyKey), SizeGB: sizeGB, StorageClass: os.Getenv("OPL_WORKSPACE_STORAGE_CLASS"), CreatedAt: now}, nil
}

func (p *TencentProvider) DestroyStorageVolume(ctx context.Context, volume StorageVolume) (StorageVolume, error) {
	if volume.ID == "" {
		return StorageVolume{}, fmt.Errorf("storage_volume_id_required")
	}
	pvc := resourceName(volume.ProviderResourceID)
	if pvc != "" {
		_, _ = p.kubectl(ctx, []string{"delete", "pvc/" + pvc, "--ignore-not-found=true"}, nil)
	}
	volume.Status = "destroyed"
	volume.ProviderRequestID = providerRequestID("storage-destroy", volume.ID)
	if volume.Provider == "" {
		volume.Provider = "tencent-tke"
	}
	return volume, nil
}

func (p *TencentProvider) CreateStorageAttachment(_ context.Context, input StorageAttachmentInput, compute ComputeAllocation, volume StorageVolume) (StorageAttachment, error) {
	if input.VolumeID == "" {
		return StorageAttachment{}, fmt.Errorf("storage_volume_id_required")
	}
	now := time.Now().UTC()
	id := fabricID("att", input.WorkspaceID, now)
	return StorageAttachment{
		ID:                   id,
		WorkspaceID:          input.WorkspaceID,
		ComputeID:            input.ComputeID,
		VolumeID:             input.VolumeID,
		Status:               "attached",
		Provider:             "tencent-tke",
		ProviderAttachmentID: "deployment/" + compute.ServiceName + ":" + volume.ProviderResourceID,
		ProviderRequestID:    providerRequestID("storage-attach", input.IdempotencyKey),
		CreatedAt:            now,
	}, nil
}

func (p *TencentProvider) DetachStorageAttachment(_ context.Context, attachment StorageAttachment) (StorageAttachment, error) {
	attachment.Status = "detached"
	attachment.ProviderRequestID = providerRequestID("storage-detach", attachment.ID)
	return attachment, nil
}

func (p *TencentProvider) CreateWorkspaceRuntime(ctx context.Context, input WorkspaceRuntimeInput, compute ComputeAllocation, volume StorageVolume) (WorkspaceRuntime, error) {
	if compute.ID == "" || volume.ID == "" {
		return WorkspaceRuntime{}, fmt.Errorf("workspace_runtime_resources_required")
	}
	now := time.Now().UTC()
	serviceName := firstNonEmpty(compute.ServiceName, k8sName(compute.ID))
	token := stableID(input.WorkspaceID, input.IdempotencyKey, now.String())[:24]
	if _, err := p.kubectl(ctx, []string{"apply", "-f", "-"}, workspaceManifest(input.WorkspaceID, input.WorkspaceID, token, serviceName, compute, volume)); err != nil {
		return WorkspaceRuntime{}, err
	}
	return WorkspaceRuntime{ID: fabricID("rt", input.WorkspaceID, now), WorkspaceID: input.WorkspaceID, URL: fmt.Sprintf("https://%s/w/%s/", workspaceDomain(), input.WorkspaceID), Status: "running", ServiceName: serviceName, ProviderRequestID: providerRequestID("runtime", input.IdempotencyKey), Ready: true, CreatedAt: now}, nil
}

func (p *TencentProvider) WorkspaceRuntimeStatus(ctx context.Context, workspaceID string) (WorkspaceRuntime, error) {
	serviceName, pvcName := p.workspaceRuntimeResources(ctx, workspaceID)
	if serviceName == "" || pvcName == "" {
		return WorkspaceRuntime{WorkspaceID: workspaceID, Status: "not_found", Ready: false, Checks: []Check{{Name: "workspace_resources_found", OK: false}}}, nil
	}
	raw, err := p.kubectl(ctx, []string{"get", "deployment/" + serviceName, "pvc/" + pvcName, "service/" + serviceName, "ingress/opl-cloud", "endpoints/" + serviceName, "-o", "json"}, nil)
	if err != nil {
		return WorkspaceRuntime{WorkspaceID: workspaceID, Status: "unready", ServiceName: serviceName, Ready: false, Checks: []Check{{Name: "kubectl_get", OK: false}}}, nil
	}
	items := kubectlItems(raw)
	deployment := findK8s(items, "Deployment", serviceName)
	pvc := findK8s(items, "PersistentVolumeClaim", pvcName)
	service := findK8s(items, "Service", serviceName)
	ingress := findK8s(items, "Ingress", "opl-cloud")
	endpoints := findK8s(items, "Endpoints", serviceName)
	readyReplicas := number(nested(deployment, "status", "readyReplicas"))
	availableReplicas := number(nested(deployment, "status", "availableReplicas"))
	image := stringValue(firstContainerField(deployment, "image"))
	checks := []Check{
		{Name: "deployment_ready", OK: readyReplicas > 0 && availableReplicas > 0},
		{Name: "workspace_image_pulled", OK: image == os.Getenv("OPL_WORKSPACE_IMAGE")},
		{Name: "pvc_bound", OK: stringValue(nested(pvc, "status", "phase")) == "Bound"},
		{Name: "deployment_uses_retained_pvc", OK: deploymentUsesPVC(deployment, pvcName)},
		{Name: "service_targets_workspace", OK: selectorMatches(service, deployment)},
		{Name: "service_endpoints_ready", OK: endpointReadyAddresses(endpoints) > 0},
		{Name: "ingress_routes_workspace_gateway", OK: ingressRoutesGateway(ingress)},
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
	return WorkspaceRuntime{WorkspaceID: workspaceID, URL: fmt.Sprintf("https://%s/w/%s/", workspaceDomain(), workspaceID), Status: status, ServiceName: serviceName, Ready: ready, Checks: checks}, nil
}

func (p *TencentProvider) Readiness(ctx context.Context) (map[string]any, error) {
	required := []string{"OPL_WORKSPACE_DOMAIN", "OPL_WORKSPACE_IMAGE", "OPL_K8S_NAMESPACE", "OPL_IMAGE_PULL_SECRET_NAME", "OPL_WORKSPACE_STORAGE_CLASS", "OPL_TENCENT_PROVISIONER_BIN", "TENCENT_DEPLOY_KUBECONFIG_REF", "RUN_TENCENT_CREATE_RELEASE_EXECUTION"}
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
	uniqueMissing := uniqueStrings(missing)
	return map[string]any{"provider": "tencent-tke", "ready": len(uniqueMissing) == 0 && len(missingTools) == 0, "missingEnv": uniqueMissing, "missingTools": missingTools, "failedChecks": []any{}}, nil
}

func (p *TencentProvider) workspaceRuntimeResources(ctx context.Context, workspaceID string) (string, string) {
	if strings.TrimSpace(workspaceID) == "" {
		return "", ""
	}
	raw, err := p.kubectl(ctx, []string{"get", "deployment,service", "-l", "oplcloud.cn/workspace-id=" + workspaceID, "-o", "json"}, nil)
	if err != nil {
		return "", ""
	}
	items := kubectlItems(raw)
	deployment := findK8sByLabel(items, "Deployment", "oplcloud.cn/workspace-id", workspaceID)
	service := findK8sByLabel(items, "Service", "oplcloud.cn/workspace-id", workspaceID)
	serviceName := firstNonEmpty(stringValue(nested(deployment, "metadata", "name")), stringValue(nested(service, "metadata", "name")))
	return serviceName, firstPVCClaimName(deployment)
}

func packagePlan(packageID string) plan {
	if packageID == "pro" {
		return plan{ID: "pro", Server: "8c16g", CPU: 8, MemoryGB: 16, DiskGB: 100, InstanceType: firstNonEmpty(os.Getenv("OPL_PRO_COMPUTE_INSTANCE_TYPE"), "SA5.2XLARGE16"), NodePoolID: os.Getenv("OPL_PRO_COMPUTE_NODE_POOL_ID")}
	}
	return plan{ID: "basic", Server: "2c4g", CPU: 2, MemoryGB: 4, DiskGB: 10, InstanceType: firstNonEmpty(os.Getenv("OPL_BASIC_COMPUTE_INSTANCE_TYPE"), "SA5.MEDIUM4"), NodePoolID: os.Getenv("OPL_BASIC_COMPUTE_NODE_POOL_ID")}
}

func pvcManifest(name string, storageID string, accountID string, sizeGB int) []byte {
	return mustJSON(map[string]any{
		"apiVersion": "v1",
		"kind":       "PersistentVolumeClaim",
		"metadata":   map[string]any{"name": name + "-data", "labels": map[string]any{"app.kubernetes.io/name": "opl-storage-volume", "app.kubernetes.io/instance": name, "oplcloud.cn/storage-id": storageID, "oplcloud.cn/account-id": accountID}},
		"spec":       map[string]any{"accessModes": []string{"ReadWriteOnce"}, "storageClassName": os.Getenv("OPL_WORKSPACE_STORAGE_CLASS"), "resources": map[string]any{"requests": map[string]any{"storage": fmt.Sprintf("%dGi", sizeGB)}}},
	})
}

func workspaceManifest(workspaceID string, workspaceName string, token string, serviceName string, compute ComputeAllocation, storage StorageVolume) []byte {
	labels := map[string]any{"app.kubernetes.io/name": "opl-compute-allocation", "app.kubernetes.io/instance": serviceName, "oplcloud.cn/compute-allocation-id": compute.ID, "oplcloud.cn/account-id": compute.AccountID, "oplcloud.cn/workspace-id": workspaceID}
	pvcName := resourceName(storage.ProviderResourceID)
	plan := packagePlan(compute.PackageID)
	secretData := map[string]any{
		"OPL_SHARE_TOKEN":            b64(token),
		"OPL_WORKSPACE_ID":           b64(workspaceID),
		"OPL_WORKSPACE_NAME":         b64(workspaceName),
		"OPL_OWNER_ACCOUNT_ID":       b64(compute.AccountID),
		"OPL_PACKAGE_ID":             b64(plan.ID),
		"OPL_CODEX_MODEL":            b64(os.Getenv("OPL_CODEX_MODEL")),
		"OPL_CODEX_REASONING_EFFORT": b64(os.Getenv("OPL_CODEX_REASONING_EFFORT")),
		"OPL_CODEX_BASE_URL":         b64(os.Getenv("OPL_CODEX_BASE_URL")),
		"OPL_CODEX_API_KEY":          b64(os.Getenv("OPL_CODEX_API_KEY")),
		"OPL_CODEX_PROVIDER_NAME":    b64(os.Getenv("OPL_CODEX_PROVIDER_NAME")),
	}
	if password := deriveAionUIAdminPassword(os.Getenv("OPL_AIONUI_ADMIN_PASSWORD_SEED"), workspaceID, token); password != "" {
		secretData["OPL_AIONUI_ADMIN_USERNAME"] = b64("admin")
		secretData["OPL_AIONUI_ADMIN_PASSWORD"] = b64(password)
	}
	for key, value := range secretData {
		if value == "" {
			delete(secretData, key)
		}
	}
	workspaceEnv := []any{
		map[string]any{"name": "OPL_COMPUTE_ALLOCATION_ID", "value": compute.ID},
		map[string]any{"name": "OPL_OWNER_ACCOUNT_ID", "value": compute.AccountID},
		map[string]any{"name": "OPL_PACKAGE_ID", "value": plan.ID},
		map[string]any{"name": "DATA_DIR", "value": "/data"},
		map[string]any{"name": "AIONUI_DATA_DIR", "value": "/data"},
		map[string]any{"name": "OPL_PROJECTS_DIR", "value": "/projects"},
		map[string]any{"name": "AIONUI_ALLOW_REMOTE", "value": "true"},
		map[string]any{"name": "ALLOW_REMOTE", "value": "true"},
		map[string]any{"name": "HOME", "value": "/data"},
		map[string]any{"name": "OPL_WORKSPACE_ROOT", "value": "/projects"},
		map[string]any{"name": "CODEX_HOME", "value": "/data/codex"},
	}
	initContainer := map[string]any{"name": "bootstrap-codex-config", "image": os.Getenv("OPL_WORKSPACE_IMAGE"), "imagePullPolicy": "IfNotPresent", "envFrom": []any{map[string]any{"secretRef": map[string]any{"name": serviceName + "-env"}}}, "env": []any{map[string]any{"name": "CODEX_HOME", "value": "/data/codex"}}, "command": []string{"node", "-e"}, "args": []string{codexBootstrapScript()}, "volumeMounts": []any{map[string]any{"name": "workspace-data", "mountPath": "/data", "subPath": "data"}}, "securityContext": map[string]any{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": false, "capabilities": map[string]any{"drop": []string{"ALL"}}}}
	workspaceContainer := map[string]any{"name": "workspace", "image": os.Getenv("OPL_WORKSPACE_IMAGE"), "imagePullPolicy": "IfNotPresent", "ports": []any{map[string]any{"name": "http", "containerPort": 3000}}, "envFrom": []any{map[string]any{"secretRef": map[string]any{"name": serviceName + "-env"}}}, "env": workspaceEnv, "lifecycle": map[string]any{"postStart": map[string]any{"exec": map[string]any{"command": []string{"node", "-e", aionUIPasswordBootstrapScript()}}}}, "volumeMounts": []any{map[string]any{"name": "workspace-data", "mountPath": "/data", "subPath": "data"}, map[string]any{"name": "workspace-data", "mountPath": "/projects", "subPath": "projects"}}, "resources": workspaceResources(plan), "readinessProbe": map[string]any{"httpGet": map[string]any{"path": "/", "port": 3000}, "initialDelaySeconds": 10, "periodSeconds": 10}}
	secret := map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": serviceName + "-env", "labels": map[string]any{"app.kubernetes.io/name": "opl-workspace-entry", "app.kubernetes.io/instance": serviceName, "oplcloud.cn/workspace-id": workspaceID}}, "type": "Opaque", "data": secretData}
	deployment := map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": serviceName, "labels": labels}, "spec": map[string]any{"replicas": 1, "selector": map[string]any{"matchLabels": labels}, "template": map[string]any{"metadata": map[string]any{"labels": labels}, "spec": map[string]any{"automountServiceAccountToken": false, "hostNetwork": true, "dnsPolicy": "ClusterFirstWithHostNet", "imagePullSecrets": []any{map[string]any{"name": os.Getenv("OPL_IMAGE_PULL_SECRET_NAME")}}, "nodeSelector": compute.NodeSelector, "tolerations": []any{map[string]any{"key": "tke.cloud.tencent.com/eni-ip-unavailable", "operator": "Exists", "effect": "NoSchedule"}}, "initContainers": []any{initContainer}, "containers": []any{workspaceContainer}, "volumes": []any{map[string]any{"name": "workspace-data", "persistentVolumeClaim": map[string]any{"claimName": pvcName}}}}}}}
	service := map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": serviceName, "labels": labels}, "spec": map[string]any{"type": "ClusterIP", "selector": labels, "ports": []any{map[string]any{"name": "http", "port": 3000, "targetPort": "http"}}}}
	return mustJSON(map[string]any{"apiVersion": "v1", "kind": "List", "items": []any{secret, deployment, service}})
}

func workspaceResources(plan plan) map[string]any {
	requestCPU := plan.CPU / 2
	if requestCPU < 1 {
		requestCPU = 1
	}
	requestMemoryGB := plan.MemoryGB / 2
	if requestMemoryGB < 1 {
		requestMemoryGB = 1
	}
	return map[string]any{"requests": map[string]any{"cpu": fmt.Sprint(requestCPU), "memory": fmt.Sprintf("%dGi", requestMemoryGB)}, "limits": map[string]any{"cpu": fmt.Sprint(plan.CPU), "memory": fmt.Sprintf("%dGi", plan.MemoryGB)}}
}

func executeProvisioner(ctx context.Context, request provisionerRequest) (provisionerResponse, error) {
	path := firstNonEmpty(os.Getenv("OPL_TENCENT_PROVISIONER_BIN"), "/usr/local/bin/opl-tencent-provisioner")
	body, _ := json.Marshal(request)
	cmd := exec.CommandContext(ctx, path)
	cmd.Stdin = bytes.NewReader(body)
	output, err := cmd.CombinedOutput()
	var response provisionerResponse
	_ = json.Unmarshal(output, &response)
	if err != nil && response.ErrorCode == "" {
		return response, fmt.Errorf("%s: %s", err, strings.TrimSpace(string(output)))
	}
	return response, nil
}

func executeKubectl(ctx context.Context, args []string, stdin []byte) ([]byte, error) {
	kubeconfig := os.Getenv("TENCENT_DEPLOY_KUBECONFIG_REF")
	base := []string{}
	if kubeconfig != "" {
		base = append(base, "--kubeconfig", kubeconfig)
	}
	base = append(base, "--namespace", namespace())
	base = append(base, args...)
	cmd := exec.CommandContext(ctx, "kubectl", base...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = strings.TrimSpace(string(output))
		}
		return output, fmt.Errorf("%s: %s", err, message)
	}
	return output, nil
}

func tkeNodeSelector(providerData map[string]string, nodeName string) map[string]any {
	if machineName := strings.TrimSpace(providerData["machineName"]); machineName != "" {
		return map[string]any{"cloud.tencent.com/node-instance-id": machineName}
	}
	if nodeName := strings.TrimSpace(nodeName); nodeName != "" {
		return map[string]any{"kubernetes.io/hostname": nodeName}
	}
	return map[string]any{}
}

func provisionerError(response provisionerResponse) error {
	if response.Message != "" {
		return fmt.Errorf("%s:%s", response.ErrorCode, response.Message)
	}
	return fmt.Errorf("%s", response.ErrorCode)
}

func mustJSON(value any) []byte {
	body, _ := json.Marshal(value)
	return body
}

func namespace() string {
	return firstNonEmpty(os.Getenv("OPL_K8S_NAMESPACE"), defaultNamespace)
}

func workspaceDomain() string {
	return strings.Trim(strings.TrimPrefix(strings.TrimPrefix(firstNonEmpty(os.Getenv("OPL_WORKSPACE_DOMAIN"), "workspace.medopl.cn"), "https://"), "http://"), "/")
}

func b64(value string) string {
	if value == "" {
		return ""
	}
	return base64.StdEncoding.EncodeToString([]byte(value))
}

func deriveAionUIAdminPassword(seed string, workspaceID string, token string) string {
	secret := strings.TrimSpace(seed)
	if secret == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(workspaceID + ":" + token))
	digest := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if len(digest) > 24 {
		digest = digest[:24]
	}
	return "opl_" + digest + "Aa1!"
}

func stableID(parts ...string) string {
	hash := sha1.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func compactID(value string) string {
	cleaned := strings.Builder{}
	lastDash := false
	for _, r := range strings.ToLower(value) {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			cleaned.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			cleaned.WriteByte('-')
			lastDash = true
		}
	}
	result := strings.Trim(cleaned.String(), "-")
	if len(result) > 48 {
		result = strings.Trim(result[:48], "-")
	}
	if result == "" {
		return "resource"
	}
	return result
}

func k8sName(id string) string {
	name := compactID(id)
	if len(name) > 54 {
		name = name[:54]
	}
	return "opl-" + strings.Trim(name, "-")
}

func resourceName(value string) string {
	parts := strings.Split(value, "/")
	return parts[len(parts)-1]
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func nested(root map[string]any, keys ...string) any {
	var current any = root
	for _, key := range keys {
		asMap, ok := current.(map[string]any)
		if !ok {
			if raw, ok := current.(map[string]string); ok {
				return raw[key]
			}
			return nil
		}
		current = asMap[key]
	}
	return current
}

func number(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	default:
		return 0
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func uniqueStrings(input []string) []string {
	seen := map[string]bool{}
	output := []string{}
	for _, value := range input {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		output = append(output, value)
	}
	sort.Strings(output)
	return output
}

func kubectlItems(raw []byte) []any {
	var list map[string]any
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil
	}
	if items, ok := list["items"].([]any); ok {
		return items
	}
	return []any{list}
}

func findK8s(items []any, kind string, name string) map[string]any {
	for _, item := range items {
		asMap, ok := item.(map[string]any)
		if ok && asMap["kind"] == kind && nested(asMap, "metadata", "name") == name {
			return asMap
		}
	}
	return map[string]any{}
}

func findK8sByLabel(items []any, kind string, key string, value string) map[string]any {
	for _, item := range items {
		asMap, ok := item.(map[string]any)
		if ok && asMap["kind"] == kind && nested(asMap, "metadata", "labels", key) == value {
			return asMap
		}
	}
	return map[string]any{}
}

func firstPVCClaimName(deployment map[string]any) string {
	volumes, _ := nested(deployment, "spec", "template", "spec", "volumes").([]any)
	for _, volume := range volumes {
		asMap, _ := volume.(map[string]any)
		if name := stringValue(nested(asMap, "persistentVolumeClaim", "claimName")); name != "" {
			return name
		}
	}
	return ""
}

func firstContainerField(deployment map[string]any, key string) any {
	containers, _ := nested(deployment, "spec", "template", "spec", "containers").([]any)
	if len(containers) == 0 {
		return nil
	}
	container, _ := containers[0].(map[string]any)
	return container[key]
}

func deploymentUsesPVC(deployment map[string]any, pvcName string) bool {
	volumes, _ := nested(deployment, "spec", "template", "spec", "volumes").([]any)
	for _, volume := range volumes {
		asMap, _ := volume.(map[string]any)
		if nested(asMap, "persistentVolumeClaim", "claimName") == pvcName {
			return true
		}
	}
	return false
}

func selectorMatches(service map[string]any, deployment map[string]any) bool {
	selector, _ := nested(service, "spec", "selector").(map[string]any)
	labels, _ := nested(deployment, "spec", "template", "metadata", "labels").(map[string]any)
	if len(selector) == 0 || len(labels) == 0 {
		return false
	}
	for key, value := range selector {
		if labels[key] != value {
			return false
		}
	}
	return true
}

func endpointReadyAddresses(endpoints map[string]any) int {
	subsets, _ := endpoints["subsets"].([]any)
	count := 0
	for _, subset := range subsets {
		asMap, _ := subset.(map[string]any)
		addresses, _ := asMap["addresses"].([]any)
		count += len(addresses)
	}
	return count
}

func ingressRoutesGateway(ingress map[string]any) bool {
	rules, _ := nested(ingress, "spec", "rules").([]any)
	for _, rawRule := range rules {
		rule, _ := rawRule.(map[string]any)
		paths, _ := nested(rule, "http", "paths").([]any)
		for _, rawPath := range paths {
			path, _ := rawPath.(map[string]any)
			if path["path"] == "/" && nested(path, "backend", "service", "name") == gatewayService && number(nested(path, "backend", "service", "port", "number")) == 8787 {
				return true
			}
		}
	}
	return false
}

func codexBootstrapScript() string {
	return `const fs=require("node:fs");
const path=require("node:path");
const home=process.env.CODEX_HOME||"/data/codex";
const config=path.join(home,"config.toml");
const apiKey=String(process.env.OPL_CODEX_API_KEY||process.env.CODEX_API_KEY||process.env.OPENAI_API_KEY||"").trim();
const model=String(process.env.OPL_CODEX_MODEL||process.env.CODEX_MODEL||"gpt-5.5").trim();
const baseUrl=String(process.env.OPL_CODEX_BASE_URL||process.env.CODEX_BASE_URL||process.env.OPENAI_BASE_URL||"").trim();
if(!apiKey||!model||!baseUrl)process.exit(0);
const existing=fs.existsSync(config)?fs.readFileSync(config,"utf8"):"";
if(/experimental_bearer_token\s*=/.test(existing))process.exit(0);
const provider=String(process.env.OPL_CODEX_MODEL_PROVIDER||process.env.CODEX_MODEL_PROVIDER||"gflabtoken").trim();
const effort=String(process.env.OPL_CODEX_REASONING_EFFORT||process.env.CODEX_REASONING_EFFORT||"").trim();
const q=(value)=>JSON.stringify(String(value));
const lines=["model_provider = "+q(provider),"model = "+q(model),...(effort?["model_reasoning_effort = "+q(effort)]:[]),"","[model_providers."+provider+"]","name = "+q(provider),"base_url = "+q(baseUrl),"experimental_bearer_token = "+q(apiKey),""];
fs.mkdirSync(home,{recursive:true});
fs.writeFileSync(config,lines.join("\n"),{mode:0o600});
fs.chmodSync(config,0o600);`
}

func aionUIPasswordBootstrapScript() string {
	return `const password = String(process.env.OPL_AIONUI_ADMIN_PASSWORD || "").trim();
if (!password) process.exit(1);
const body = JSON.stringify({ new_password: password });
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
let last = "";
for (let attempt = 0; attempt < 90; attempt += 1) {
  try {
    const response = await fetch("http://127.0.0.1:3000/api/webui/change-password", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body
    });
    if (response.ok) process.exit(0);
    last = response.status + ":" + await response.text();
  } catch (error) {
    last = error && error.message ? error.message : String(error);
  }
  await sleep(1000);
}
console.error("[opl] failed to set AionUI admin password: " + last);
process.exit(1);`
}
