package server

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
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultNamespace = "opl-cloud"
	gatewayService   = "opl-cloud-control-plane"
)

type runtimeApp struct {
	mu          sync.Mutex
	computes    map[string]map[string]any
	storages    map[string]map[string]any
	attachments map[string]map[string]any
	workspaces  map[string]map[string]any
	wallets     map[string]map[string]any
	ledger      []map[string]any
	usage       []map[string]any
	walletTx    []map[string]any
	topups      []map[string]any
	provision   func(context.Context, provisionerRequest) (provisionerResponse, error)
	kubectl     func(context.Context, []string, []byte) ([]byte, error)
}

type provisionerRequest struct {
	Action     string                `json:"action"`
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

func newRuntimeApp() *runtimeApp {
	return &runtimeApp{
		computes:    map[string]map[string]any{},
		storages:    map[string]map[string]any{},
		attachments: map[string]map[string]any{},
		workspaces:  map[string]map[string]any{},
		wallets:     map[string]map[string]any{},
		provision:   executeProvisioner,
		kubectl:     executeKubectl,
	}
}

func (app *runtimeApp) state(accountID string) map[string]any {
	app.mu.Lock()
	defer app.mu.Unlock()
	return map[string]any{
		"product":               map[string]any{"name": "OPL Cloud", "console": "OPL Console", "workspace": "OPL Workspace"},
		"billingPolicy":         map[string]any{"holdDays": 7, "priceBasis": "OPL price list"},
		"packages":              packageList(),
		"computePools":          computePools(),
		"wallet":                app.wallet(accountID),
		"account":               app.wallet(accountID),
		"user":                  sessionPayload()["user"],
		"workspaces":            values(app.workspaces),
		"computeAllocations":    values(app.computes),
		"storageVolumes":        values(app.storages),
		"storageAttachments":    values(app.attachments),
		"billingLedger":         copySlice(app.ledger),
		"resourceUsageLogs":     copySlice(app.usage),
		"walletTransactions":    copySlice(app.walletTx),
		"manualTopups":          copySlice(app.topups),
		"billingReconciliation": map[string]any{"guard": map[string]any{"status": "not_required", "blockNewWorkspaces": false, "reason": "billing_reconciliation_not_required"}},
		"evidenceLedger":        []any{},
		"audit":                 []any{},
		"notifications":         []any{},
		"runtimeOperations":     []any{},
		"generatedAt":           time.Now().UTC().Format(time.RFC3339),
	}
}

func (app *runtimeApp) managementState() map[string]any {
	app.mu.Lock()
	defer app.mu.Unlock()
	return map[string]any{
		"organization":           nil,
		"organizations":          []any{},
		"users":                  []any{map[string]any{"id": "usr-local", "email": "owner@example.com", "accountId": "acct-local", "role": "admin", "status": "active"}},
		"memberships":            []any{},
		"accounts":               []any{app.wallet("acct-local")},
		"packages":               packageList(),
		"computePools":           computePools(),
		"workspaces":             values(app.workspaces),
		"computeAllocations":     values(app.computes),
		"storageVolumes":         values(app.storages),
		"storageAttachments":     values(app.attachments),
		"resourceLedgerEvidence": []any{},
		"walletTransactions":     copySlice(app.walletTx),
		"manualTopups":           copySlice(app.topups),
	}
}

func (app *runtimeApp) operatorSummary() map[string]any {
	app.mu.Lock()
	defer app.mu.Unlock()
	running := countStatus(app.computes, "running")
	return map[string]any{
		"product":                "OPL Console",
		"generatedAt":            time.Now().UTC().Format(time.RFC3339),
		"accountScope":           "all",
		"accounts":               map[string]any{"total": len(app.wallets), "frozen": 0, "balance": totalWallet(app.wallets, "balance"), "totalSpent": totalDebits(app.walletTx)},
		"workspaces":             map[string]any{"total": len(app.workspaces), "running": countStatus(app.workspaces, "running"), "urlActive": countActiveURLs(app.workspaces), "destroyed": countStatus(app.workspaces, "destroyed"), "needsAttention": 0},
		"computeAllocations":     map[string]any{"total": len(app.computes), "running": running, "failed": countStatus(app.computes, "failed")},
		"notifications":          map[string]any{"total": 0, "error": 0, "warning": 0, "recent": []any{}},
		"runtimeOperations":      map[string]any{"total": 0, "failed": 0, "recentFailed": []any{}},
		"failedOperations":       []any{},
		"resourceAnomalies":      []any{},
		"resourceLedgerEvidence": map[string]any{"total": len(app.ledger), "recent": copySlice(app.ledger)},
		"productionE2E":          map[string]any{},
		"billingReconciliation":  map[string]any{"reports": 0, "guard": map[string]any{"status": "not_required", "blockNewWorkspaces": false, "reason": "billing_reconciliation_not_required"}},
	}
}

func (app *runtimeApp) readiness(ctx context.Context) map[string]any {
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
	provisioner, err := app.provision(ctx, provisionerRequest{Action: "readiness"})
	if err != nil || !provisioner.OK {
		missing = append(missing, provisioner.MissingEnv...)
		if provisioner.ErrorCode != "" {
			missing = append(missing, provisioner.ErrorCode)
		} else if err != nil {
			missing = append(missing, "provisioner_failed")
		}
	}
	uniqueMissing := uniqueStrings(missing)
	return map[string]any{"provider": "tencent-tke", "ready": len(uniqueMissing) == 0 && len(missingTools) == 0, "missingEnv": uniqueMissing, "missingTools": missingTools, "failedChecks": []any{}}
}

func (app *runtimeApp) topUp(input map[string]any) map[string]any {
	app.mu.Lock()
	defer app.mu.Unlock()
	accountID := stringField(input, "accountId", "acct-local")
	amount := numberField(input, "amount", 0)
	wallet := app.wallet(accountID)
	before := number(wallet["balance"])
	after := before + amount
	wallet["balance"] = after
	wallet["available"] = after - number(wallet["frozen"])
	wallet["totalRecharged"] = number(wallet["totalRecharged"]) + amount
	id := "manual-topup-" + stableID(accountID, fmt.Sprintf("%f", amount), time.Now().UTC().String())[:12]
	entry := map[string]any{
		"id":                  id,
		"idempotencyKey":      stringField(input, "idempotencyKey", id),
		"targetAccountId":     accountID,
		"amount":              amount,
		"balanceBefore":       before,
		"balanceAfter":        after,
		"operatorUserId":      stringField(input, "operatorUserId", "operator"),
		"operatorAccountId":   stringField(input, "operatorAccountId", "operator"),
		"ledgerEntryId":       "ledger-" + id,
		"walletTransactionId": "wallet-" + id,
		"status":              "completed",
	}
	app.topups = append(app.topups, entry)
	return cloneMap(entry)
}

func (app *runtimeApp) createCompute(ctx context.Context, input map[string]any) (map[string]any, error) {
	accountID := stringField(input, "accountId", "acct-local")
	packageID := stringField(input, "packageId", "basic")
	name := stringField(input, "name", "compute")
	id := "compute-" + compactID(name+"-"+time.Now().UTC().Format("20060102150405.000000000"))
	plan := packagePlan(packageID)
	pool := provisionerPool{
		ID:           "pool-" + packageID,
		PackageID:    packageID,
		InstanceType: plan.InstanceType,
		NodePoolID:   plan.NodePoolID,
		Labels:       map[string]string{"oplcloud.cn/package-id": packageID, "oplcloud.cn/instance-type": plan.InstanceType},
	}
	serviceName := k8sName(id)
	compute := map[string]any{
		"id":                 id,
		"name":               name,
		"ownerAccountId":     accountID,
		"accountId":          accountID,
		"packageId":          packageID,
		"provider":           "tencent-tke",
		"providerResourceId": "",
		"poolId":             pool.ID,
		"nodePoolId":         pool.NodePoolID,
		"instanceId":         "",
		"cvmInstanceId":      "",
		"nodeName":           "",
		"privateIp":          "",
		"publicIp":           "",
		"status":             "provisioning",
		"billingStatus":      "active",
		"spec":               plan.Server,
		"cpu":                plan.CPU,
		"memoryGb":           plan.MemoryGB,
		"image":              os.Getenv("OPL_WORKSPACE_IMAGE"),
		"operationId":        "",
		"providerRequestId":  "",
		"providerData":       map[string]string{},
		"runtime":            map[string]any{"service": "service/" + serviceName, "serviceName": serviceName, "nodeName": "", "nodeSelector": map[string]any{}},
		"nodeSelector":       map[string]any{},
	}
	app.mu.Lock()
	app.computes[id] = compute
	app.mu.Unlock()
	go app.finishComputeProvision(provisionerRequest{Action: "create_compute_allocation", AccountID: accountID, PackageID: packageID, Pool: pool, Allocation: provisionerAllocation{ID: id}}, id)
	return cloneMap(compute), nil
}

func (app *runtimeApp) finishComputeProvision(request provisionerRequest, id string) {
	response, err := app.provision(context.Background(), request)
	app.mu.Lock()
	defer app.mu.Unlock()
	compute := app.computes[id]
	if compute == nil {
		return
	}
	if err != nil || !response.OK {
		compute["status"] = "failed"
		compute["error"] = firstNonEmpty(provisionerError(response).Error(), errString(err))
		app.computes[id] = compute
		return
	}
	nodeName := firstNonEmpty(response.NodeName, response.ProviderData["nodeName"])
	nodeSelector := tkeNodeSelector(response.ProviderData, nodeName)
	compute["providerResourceId"] = "node/" + nodeName
	compute["poolId"] = firstNonEmpty(response.PoolID, stringValue(compute["poolId"]))
	compute["nodePoolId"] = firstNonEmpty(response.NodePoolID, stringValue(compute["nodePoolId"]))
	compute["instanceId"] = response.InstanceID
	compute["cvmInstanceId"] = response.InstanceID
	compute["nodeName"] = nodeName
	compute["privateIp"] = response.PrivateIP
	compute["publicIp"] = response.PublicIP
	compute["status"] = firstNonEmpty(response.Status, "running")
	compute["operationId"] = response.OperationID
	compute["providerRequestId"] = response.ProviderRequestID
	compute["providerData"] = response.ProviderData
	compute["runtime"] = map[string]any{"service": "service/" + stringValue(nested(compute, "runtime", "serviceName")), "serviceName": nested(compute, "runtime", "serviceName"), "nodeName": nodeName, "nodeSelector": nodeSelector}
	compute["nodeSelector"] = nodeSelector
	app.computes[id] = compute
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

func (app *runtimeApp) getCompute(id string) (map[string]any, bool) {
	app.mu.Lock()
	defer app.mu.Unlock()
	compute, ok := app.computes[id]
	return cloneMap(compute), ok
}

func (app *runtimeApp) destroyCompute(ctx context.Context, id string) (map[string]any, error) {
	app.mu.Lock()
	compute, ok := app.computes[id]
	app.mu.Unlock()
	if !ok {
		return map[string]any{"id": id, "status": "destroyed", "billingStatus": "stopped"}, nil
	}
	pool := provisionerPool{ID: stringValue(compute["poolId"]), NodePoolID: stringValue(compute["nodePoolId"])}
	allocation := provisionerAllocation{
		ID:          id,
		InstanceID:  stringValue(compute["instanceId"]),
		MachineName: firstNonEmpty(stringValue(nested(compute, "providerData", "machineName")), stringValue(compute["nodeName"])),
		NodeName:    stringValue(compute["nodeName"]),
	}
	response, err := app.provision(ctx, provisionerRequest{Action: "destroy_compute_allocation", AccountID: stringValue(compute["ownerAccountId"]), PackageID: stringValue(compute["packageId"]), Pool: pool, Allocation: allocation})
	if err != nil {
		return nil, err
	}
	if !response.OK {
		return nil, provisionerError(response)
	}
	name := stringValue(nested(compute, "runtime", "serviceName"))
	_, _ = app.kubectl(ctx, []string{"delete", "deployment/" + name, "service/" + name, "secret/" + name + "-env", "--ignore-not-found=true"}, nil)
	app.mu.Lock()
	compute["status"] = "destroyed"
	compute["billingStatus"] = "stopped"
	app.computes[id] = compute
	app.mu.Unlock()
	return cloneMap(compute), nil
}

func (app *runtimeApp) createStorage(ctx context.Context, input map[string]any) (map[string]any, error) {
	accountID := stringField(input, "accountId", "acct-local")
	packageID := stringField(input, "packageId", "basic")
	name := stringField(input, "name", "storage")
	sizeGB := int(math.Max(numberField(input, "sizeGb", float64(packagePlan(packageID).DiskGB)), 1))
	id := "storage-" + compactID(name+"-"+time.Now().UTC().Format("20060102150405.000000000"))
	k8s := k8sName(id)
	manifest := pvcManifest(k8s, id, accountID, sizeGB)
	if _, err := app.kubectl(ctx, []string{"apply", "-f", "-"}, manifest); err != nil {
		return nil, err
	}
	storage := map[string]any{
		"id":                 id,
		"name":               name,
		"ownerAccountId":     accountID,
		"accountId":          accountID,
		"packageId":          packageID,
		"provider":           "tencent-tke",
		"providerResourceId": "pvc/" + k8s + "-data",
		"status":             "available",
		"billingStatus":      "active",
		"sizeGb":             sizeGB,
		"storageClass":       os.Getenv("OPL_WORKSPACE_STORAGE_CLASS"),
	}
	app.mu.Lock()
	app.storages[id] = storage
	app.mu.Unlock()
	return cloneMap(storage), nil
}

func (app *runtimeApp) destroyStorage(ctx context.Context, input map[string]any) (map[string]any, error) {
	id := stringField(input, "storageId", "")
	app.mu.Lock()
	storage, ok := app.storages[id]
	app.mu.Unlock()
	if !ok {
		return map[string]any{"id": id, "status": "destroyed", "billingStatus": "stopped"}, nil
	}
	pvc := resourceName(stringValue(storage["providerResourceId"]))
	_, _ = app.kubectl(ctx, []string{"delete", "pvc/" + pvc, "--ignore-not-found=true"}, nil)
	app.mu.Lock()
	storage["status"] = "destroyed"
	storage["billingStatus"] = "stopped"
	app.storages[id] = storage
	app.mu.Unlock()
	return cloneMap(storage), nil
}

func (app *runtimeApp) attachStorage(input map[string]any) (map[string]any, error) {
	accountID := stringField(input, "accountId", "acct-local")
	computeID := stringField(input, "computeAllocationId", "")
	storageID := stringField(input, "storageId", "")
	mountPath := stringField(input, "mountPath", "/data")
	id := "attach-" + compactID(computeID+"-"+storageID+"-"+time.Now().UTC().Format("20060102150405.000000000"))
	app.mu.Lock()
	compute, computeOK := app.computes[computeID]
	storage, storageOK := app.storages[storageID]
	app.mu.Unlock()
	if !computeOK || !storageOK {
		return nil, fmt.Errorf("resource_not_found")
	}
	attachment := map[string]any{
		"id":                   id,
		"ownerAccountId":       accountID,
		"accountId":            accountID,
		"computeAllocationId":  computeID,
		"storageId":            storageID,
		"provider":             "tencent-tke",
		"providerAttachmentId": "deployment/" + stringValue(nested(compute, "runtime", "serviceName")) + ":" + stringValue(storage["providerResourceId"]) + ":" + mountPath,
		"status":               "attached",
		"mountPath":            mountPath,
	}
	app.mu.Lock()
	app.attachments[id] = attachment
	app.addLedgerLocked(accountID, "storage_attached", map[string]any{"attachmentId": id})
	app.addUsageLocked(accountID, "attachment", map[string]any{"attachmentId": id})
	app.mu.Unlock()
	return cloneMap(attachment), nil
}

func (app *runtimeApp) detachStorage(input map[string]any) map[string]any {
	id := stringField(input, "attachmentId", "")
	app.mu.Lock()
	defer app.mu.Unlock()
	attachment, ok := app.attachments[id]
	if !ok {
		return map[string]any{"id": id, "status": "detached"}
	}
	attachment["status"] = "detached"
	app.attachments[id] = attachment
	return cloneMap(attachment)
}

func (app *runtimeApp) createWorkspace(ctx context.Context, input map[string]any) (map[string]any, error) {
	accountID := stringField(input, "accountId", "acct-local")
	attachmentID := stringField(input, "attachmentId", "")
	name := firstNonEmpty(stringField(input, "workspaceName", ""), stringField(input, "name", "Workspace"))
	id := "ws-" + compactID(name+"-"+time.Now().UTC().Format("20060102150405.000000000"))
	token := "share_" + stableID(id, accountID)[:16]
	app.mu.Lock()
	attachment, attachmentOK := app.attachments[attachmentID]
	compute := app.computes[stringValue(attachment["computeAllocationId"])]
	storage := app.storages[stringValue(attachment["storageId"])]
	app.mu.Unlock()
	if !attachmentOK || compute == nil || storage == nil {
		return nil, fmt.Errorf("workspace_attachment_not_found")
	}
	serviceName := stringValue(nested(compute, "runtime", "serviceName"))
	manifest := workspaceManifest(id, name, token, serviceName, compute, storage)
	if _, err := app.kubectl(ctx, []string{"apply", "-f", "-"}, manifest); err != nil {
		return nil, err
	}
	workspace := map[string]any{
		"id":                  id,
		"ownerAccountId":      accountID,
		"accountId":           accountID,
		"name":                name,
		"packageId":           stringValue(compute["packageId"]),
		"state":               "running",
		"status":              "running",
		"provider":            "tencent-tke",
		"computeAllocationId": stringValue(compute["id"]),
		"storageId":           stringValue(storage["id"]),
		"attachmentId":        attachmentID,
		"slug":                compactID(name),
		"url":                 fmt.Sprintf("https://%s/w/%s/?token=%s", workspaceDomain(), id, token),
		"access":              map[string]any{"token": token, "tokenStatus": "active", "requiresLogin": false},
		"server":              map[string]any{"id": compute["providerResourceId"], "status": "running", "billingStatus": "active", "namespace": namespace(), "spec": compute["spec"]},
		"docker":              map[string]any{"id": compute["providerResourceId"], "image": compute["image"], "status": "running", "service": "service/" + serviceName},
		"disk":                map[string]any{"id": storage["providerResourceId"], "status": "attached_retained", "billingStatus": "active", "sizeGb": storage["sizeGb"], "mountPath": attachment["mountPath"], "storageClass": storage["storageClass"]},
		"runtime":             map[string]any{"serviceName": serviceName},
	}
	app.mu.Lock()
	app.workspaces[id] = workspace
	app.mu.Unlock()
	return cloneMap(workspace), nil
}

func (app *runtimeApp) runtimeStatus(ctx context.Context, workspaceID string) map[string]any {
	app.mu.Lock()
	workspace := cloneMap(app.workspaces[workspaceID])
	app.mu.Unlock()
	serviceName := stringValue(nested(workspace, "runtime", "serviceName"))
	pvcName := resourceName(stringValue(nested(workspace, "disk", "id")))
	if serviceName == "" || pvcName == "" {
		serviceName, pvcName = app.workspaceRuntimeResources(ctx, workspaceID)
	}
	if serviceName == "" || pvcName == "" {
		return map[string]any{"provider": "tencent-tke", "workspaceId": workspaceID, "ready": false, "checks": []map[string]any{{"name": "workspace_resources_found", "ok": false}}}
	}
	raw, err := app.kubectl(ctx, []string{"get", "deployment/" + serviceName, "pvc/" + pvcName, "service/" + serviceName, "ingress/opl-cloud", "endpoints/" + serviceName, "-o", "json"}, nil)
	if err != nil {
		return map[string]any{"provider": "tencent-tke", "workspaceId": workspaceID, "ready": false, "checks": []map[string]any{{"name": "kubectl_get", "ok": false}}}
	}
	items := kubectlItems(raw)
	deployment := findK8s(items, "Deployment", serviceName)
	pvc := findK8s(items, "PersistentVolumeClaim", pvcName)
	service := findK8s(items, "Service", serviceName)
	ingress := findK8s(items, "Ingress", "opl-cloud")
	endpoints := findK8s(items, "Endpoints", serviceName)
	readyReplicas := number(nested(deployment, "status", "readyReplicas"))
	availableReplicas := number(nested(deployment, "status", "availableReplicas"))
	readyAddresses := endpointReadyAddresses(endpoints)
	image := stringValue(firstContainerField(deployment, "image"))
	checks := []map[string]any{
		{"name": "deployment_ready", "ok": readyReplicas > 0 && availableReplicas > 0},
		{"name": "workspace_image_pulled", "ok": image == os.Getenv("OPL_WORKSPACE_IMAGE")},
		{"name": "pvc_bound", "ok": stringValue(nested(pvc, "status", "phase")) == "Bound"},
		{"name": "deployment_uses_retained_pvc", "ok": deploymentUsesPVC(deployment, pvcName)},
		{"name": "service_targets_workspace", "ok": selectorMatches(service, deployment)},
		{"name": "service_endpoints_ready", "ok": readyAddresses > 0},
		{"name": "ingress_routes_workspace_gateway", "ok": ingressRoutesGateway(ingress)},
	}
	ready := true
	for _, check := range checks {
		if check["ok"] != true {
			ready = false
		}
	}
	return map[string]any{"provider": "tencent-tke", "workspaceId": workspaceID, "ready": ready, "checks": checks}
}

func (app *runtimeApp) workspaceRuntimeResources(ctx context.Context, workspaceID string) (string, string) {
	if strings.TrimSpace(workspaceID) == "" {
		return "", ""
	}
	raw, err := app.kubectl(ctx, []string{"get", "deployment,service", "-l", "oplcloud.cn/workspace-id=" + workspaceID, "-o", "json"}, nil)
	if err != nil {
		return "", ""
	}
	items := kubectlItems(raw)
	deployment := findK8sByLabel(items, "Deployment", "oplcloud.cn/workspace-id", workspaceID)
	service := findK8sByLabel(items, "Service", "oplcloud.cn/workspace-id", workspaceID)
	serviceName := firstNonEmpty(stringValue(nested(deployment, "metadata", "name")), stringValue(nested(service, "metadata", "name")))
	return serviceName, firstPVCClaimName(deployment)
}

func (app *runtimeApp) settleResources(input map[string]any) map[string]any {
	accountID := stringField(input, "accountId", "acct-local")
	app.mu.Lock()
	defer app.mu.Unlock()
	var compute map[string]any
	for _, candidate := range app.computes {
		if candidate["ownerAccountId"] == accountID && candidate["status"] == "running" {
			compute = candidate
		}
	}
	var storage map[string]any
	for _, candidate := range app.storages {
		if candidate["ownerAccountId"] == accountID && candidate["status"] != "destroyed" {
			storage = candidate
		}
	}
	entries := []map[string]any{}
	if compute != nil {
		entry := app.addLedgerLocked(accountID, "compute_debit", map[string]any{"computeAllocationId": compute["id"]})
		app.addUsageLocked(accountID, "compute", map[string]any{"computeAllocationId": compute["id"]})
		app.addWalletTxLocked(accountID, "compute_debit", map[string]any{"computeAllocationId": compute["id"]})
		entries = append(entries, entry)
	}
	if storage != nil {
		entry := app.addLedgerLocked(accountID, "storage_debit", map[string]any{"storageId": storage["id"]})
		app.addUsageLocked(accountID, "storage", map[string]any{"storageId": storage["id"]})
		app.addWalletTxLocked(accountID, "storage_debit", map[string]any{"storageId": storage["id"]})
		entries = append(entries, entry)
	}
	return map[string]any{"entries": entries, "account": app.wallet(accountID)}
}

func (app *runtimeApp) proxyWorkspace(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/w/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	workspaceID := parts[0]
	app.mu.Lock()
	workspace := cloneMap(app.workspaces[workspaceID])
	app.mu.Unlock()
	serviceName := stringValue(nested(workspace, "runtime", "serviceName"))
	if serviceName == "" {
		http.NotFound(w, r)
		return
	}
	target, _ := url.Parse("http://" + serviceName + ":3000")
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		suffix := strings.TrimPrefix(r.URL.Path, "/w/"+workspaceID)
		if suffix == "" {
			suffix = "/"
		}
		req.URL.Path = suffix
		req.URL.RawPath = ""
		req.Host = target.Host
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		writeError(w, http.StatusBadGateway, err.Error())
	}
	proxy.ServeHTTP(w, r)
}

func (app *runtimeApp) addLedgerLocked(accountID string, entryType string, ids map[string]any) map[string]any {
	entry := map[string]any{"id": "ledger-" + stableID(accountID, entryType, time.Now().UTC().String())[:12], "accountId": accountID, "type": entryType}
	for key, value := range ids {
		entry[key] = value
	}
	app.ledger = append(app.ledger, entry)
	return entry
}

func (app *runtimeApp) addUsageLocked(accountID string, resourceType string, ids map[string]any) {
	entry := map[string]any{"id": "usage-" + stableID(accountID, resourceType, time.Now().UTC().String())[:12], "accountId": accountID, "resourceType": resourceType}
	for key, value := range ids {
		entry[key] = value
	}
	app.usage = append(app.usage, entry)
}

func (app *runtimeApp) addWalletTxLocked(accountID string, txType string, metadata map[string]any) {
	app.walletTx = append(app.walletTx, map[string]any{"id": "wallet-" + stableID(accountID, txType, time.Now().UTC().String())[:12], "accountId": accountID, "type": txType, "metadata": metadata})
}

func (app *runtimeApp) wallet(accountID string) map[string]any {
	if accountID == "" {
		accountID = "acct-local"
	}
	if wallet, ok := app.wallets[accountID]; ok {
		return wallet
	}
	wallet := map[string]any{"id": accountID, "accountId": accountID, "balance": float64(0), "frozen": float64(0), "available": float64(0), "totalRecharged": float64(0)}
	app.wallets[accountID] = wallet
	return wallet
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

func packagePlan(packageID string) plan {
	if packageID == "pro" {
		return plan{ID: "pro", Server: "8c16g", CPU: 8, MemoryGB: 16, DiskGB: 100, InstanceType: firstNonEmpty(os.Getenv("OPL_PRO_COMPUTE_INSTANCE_TYPE"), "SA5.2XLARGE16"), NodePoolID: os.Getenv("OPL_PRO_COMPUTE_NODE_POOL_ID")}
	}
	return plan{ID: "basic", Server: "2c4g", CPU: 2, MemoryGB: 4, DiskGB: 10, InstanceType: firstNonEmpty(os.Getenv("OPL_BASIC_COMPUTE_INSTANCE_TYPE"), "SA5.MEDIUM4"), NodePoolID: os.Getenv("OPL_BASIC_COMPUTE_NODE_POOL_ID")}
}

func packageList() []any {
	return []any{
		map[string]any{"id": "basic", "name": "Basic", "available": true, "cpu": 2, "memoryGb": 4, "diskGb": 10, "server": "2c4g", "price": map[string]any{"computeHourly": 0.468, "storageGbMonth": 0.432}},
		map[string]any{"id": "pro", "name": "Pro", "available": true, "cpu": 8, "memoryGb": 16, "diskGb": 100, "server": "8c16g", "price": map[string]any{"computeHourly": 1.38, "storageGbMonth": 0.432}},
	}
}

func computePools() []any {
	return []any{
		map[string]any{"id": "pool-basic", "name": "Basic", "available": true, "provider": "tencent-tke"},
		map[string]any{"id": "pool-pro", "name": "Pro", "available": true, "provider": "tencent-tke"},
	}
}

func pvcManifest(name string, storageID string, accountID string, sizeGB int) []byte {
	return mustJSON(map[string]any{
		"apiVersion": "v1",
		"kind":       "PersistentVolumeClaim",
		"metadata":   map[string]any{"name": name + "-data", "labels": map[string]any{"app.kubernetes.io/name": "opl-storage-volume", "app.kubernetes.io/instance": name, "oplcloud.cn/storage-id": storageID, "oplcloud.cn/account-id": accountID}},
		"spec":       map[string]any{"accessModes": []string{"ReadWriteOnce"}, "storageClassName": os.Getenv("OPL_WORKSPACE_STORAGE_CLASS"), "resources": map[string]any{"requests": map[string]any{"storage": fmt.Sprintf("%dGi", sizeGB)}}},
	})
}

func workspaceManifest(workspaceID string, workspaceName string, token string, serviceName string, compute map[string]any, storage map[string]any) []byte {
	labels := map[string]any{"app.kubernetes.io/name": "opl-compute-allocation", "app.kubernetes.io/instance": serviceName, "oplcloud.cn/compute-allocation-id": compute["id"], "oplcloud.cn/account-id": compute["ownerAccountId"], "oplcloud.cn/workspace-id": workspaceID}
	pvcName := resourceName(stringValue(storage["providerResourceId"]))
	plan := packagePlan(stringValue(compute["packageId"]))
	secretData := map[string]any{
		"OPL_SHARE_TOKEN":            b64(token),
		"OPL_WORKSPACE_ID":           b64(workspaceID),
		"OPL_WORKSPACE_NAME":         b64(workspaceName),
		"OPL_OWNER_ACCOUNT_ID":       b64(stringValue(compute["ownerAccountId"])),
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
		map[string]any{"name": "OPL_COMPUTE_ALLOCATION_ID", "value": compute["id"]},
		map[string]any{"name": "OPL_OWNER_ACCOUNT_ID", "value": compute["ownerAccountId"]},
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
	deployment := map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": serviceName, "labels": labels}, "spec": map[string]any{"replicas": 1, "selector": map[string]any{"matchLabels": labels}, "template": map[string]any{"metadata": map[string]any{"labels": labels}, "spec": map[string]any{"automountServiceAccountToken": false, "hostNetwork": true, "dnsPolicy": "ClusterFirstWithHostNet", "imagePullSecrets": []any{map[string]any{"name": os.Getenv("OPL_IMAGE_PULL_SECRET_NAME")}}, "nodeSelector": nested(compute, "runtime", "nodeSelector"), "tolerations": []any{map[string]any{"key": "tke.cloud.tencent.com/eni-ip-unavailable", "operator": "Exists", "effect": "NoSchedule"}}, "initContainers": []any{initContainer}, "containers": []any{workspaceContainer}, "volumes": []any{map[string]any{"name": "workspace-data", "persistentVolumeClaim": map[string]any{"claimName": pvcName}}}}}}}
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
	return map[string]any{
		"requests": map[string]any{"cpu": fmt.Sprint(requestCPU), "memory": fmt.Sprintf("%dGi", requestMemoryGB)},
		"limits":   map[string]any{"cpu": fmt.Sprint(plan.CPU), "memory": fmt.Sprintf("%dGi", plan.MemoryGB)},
	}
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

func mustJSON(value any) []byte {
	body, _ := json.Marshal(value)
	return body
}

func provisionerError(response provisionerResponse) error {
	if response.Message != "" {
		return fmt.Errorf("%s:%s", response.ErrorCode, response.Message)
	}
	return fmt.Errorf("%s", response.ErrorCode)
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

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	output := map[string]any{}
	for key, value := range input {
		output[key] = value
	}
	return output
}

func copySlice(input []map[string]any) []any {
	output := make([]any, 0, len(input))
	for _, item := range input {
		output = append(output, cloneMap(item))
	}
	return output
}

func values(input map[string]map[string]any) []any {
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	output := make([]any, 0, len(keys))
	for _, key := range keys {
		output = append(output, cloneMap(input[key]))
	}
	return output
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
	return output
}

func countStatus(input map[string]map[string]any, status string) int {
	count := 0
	for _, item := range input {
		if item["status"] == status || item["state"] == status {
			count++
		}
	}
	return count
}

func countActiveURLs(input map[string]map[string]any) int {
	count := 0
	for _, item := range input {
		if nested(item, "access", "tokenStatus") == "active" {
			count++
		}
	}
	return count
}

func totalWallet(wallets map[string]map[string]any, key string) float64 {
	total := float64(0)
	for _, wallet := range wallets {
		total += number(wallet[key])
	}
	return total
}

func totalDebits(transactions []map[string]any) float64 {
	return float64(len(transactions))
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
