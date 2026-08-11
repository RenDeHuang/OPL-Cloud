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
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"os/exec"
	"path"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"opl-cloud/services/fabric/internal/protectedresource"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8slabels "k8s.io/apimachinery/pkg/labels"
)

const (
	defaultNamespace         = "opl-cloud"
	gatewayService           = "opl-cloud-control-plane"
	webuiUsername            = "opl"
	workspaceImageRepository = "uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app"
)

type TencentProvider struct {
	provision       func(context.Context, provisionerRequest) (provisionerResponse, error)
	kubectl         func(context.Context, []string, []byte) ([]byte, error)
	convergenceWait func(context.Context, int) error
}

func (p *TencentProvider) callKubectl(ctx context.Context, args []string, stdin []byte, target protectedresource.Target) ([]byte, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("kubectl_action_required")
	}
	switch args[0] {
	case "get", "wait", "logs", "describe", "version":
	default:
		if err := protectedresource.FromEnv().Check(target); err != nil {
			return nil, err
		}
	}
	return p.kubectl(ctx, args, stdin)
}

type monthlyPreflightReportProvider interface {
	MonthlyPreflightReport(context.Context, MonthlyPreflightReportInput) (MonthlyPreflightReport, error)
}

type monthlyPreflightEvaluation struct {
	Result MonthlyPreflight
	Stages []MonthlyPreflightStage
	Err    error
}

func NewTencentProvider() *TencentProvider {
	return &TencentProvider{provision: executeProvisioner, kubectl: executeKubectl, convergenceWait: boundedClaimReadbackWait}
}

func (*TencentProvider) Descriptor() ProviderDescriptor {
	basic, pro := packagePlan("basic"), packagePlan("pro")
	return ProviderDescriptor{
		Name: "tencent-tke", RequiresMonthlyPricing: true,
		Plans: map[string]ComputePlan{"basic": basic, "pro": pro},
		Catalog: Catalog{
			SchemaVersion: 1, Owner: "OPL Fabric",
			WorkspacePackages: []WorkspacePackage{
				{ID: "basic", Name: "Basic Workspace", ComputeProfileID: "cpu-basic", CPU: 2, MemoryGB: 4, DiskGB: 10, Provider: "tencent-tke", Available: true},
				{ID: "pro", Name: "Pro Workspace", ComputeProfileID: "cpu-pro", CPU: 8, MemoryGB: 16, DiskGB: 100, Provider: "tencent-tke", Available: true},
			},
			StorageClasses: []StorageClass{{ID: "workspace-cbs", StorageClassName: "cbs", Provider: "tencent-tke", Available: true}},
			IngressDomains: []IngressDomain{{ID: "workspace", Host: "workspace.medopl.cn", PathPattern: "/w/<workspaceId>/", Available: true}},
		},
	}
}

func (*TencentProvider) ValidateComputeAllocation(allocation ComputeAllocation, prepared ComputeAllocationPreparation) error {
	if allocation.Provider != "tencent-tke" || prepared.NodePoolID == "" || allocation.NodePoolID != prepared.NodePoolID || allocation.PoolID != prepared.PoolID || allocation.PackageID != prepared.PackageID ||
		allocation.InstanceType != prepared.InstanceType || allocation.MachineName == "" || !strings.HasPrefix(firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID), "ins-") ||
		allocation.NodeName == "" || allocation.PrivateIP == "" || allocation.Zone == "" || allocation.ChargeType != "PREPAID" ||
		allocation.RenewFlag != "NOTIFY_AND_MANUAL_RENEW" || allocation.Deadline == "" {
		return fmt.Errorf("compute_provider_readback_mismatch")
	}
	for _, existing := range prepared.BeforeMachineNames {
		if allocation.MachineName == existing {
			return fmt.Errorf("compute_allocation_machine_not_new")
		}
	}
	return nil
}

func (*TencentProvider) ValidateWorkspaceImageReference(value string) bool {
	return validWorkspaceRuntimeImageIdentity(value)
}

func boundedClaimReadbackWait(ctx context.Context, attempt int) error {
	if attempt <= 0 {
		return nil
	}
	delays := [...]time.Duration{0, 500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	if attempt >= len(delays) {
		attempt = len(delays) - 1
	}
	timer := time.NewTimer(delays[attempt])
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (p *TencentProvider) MonthlyPreflight(ctx context.Context, input MonthlyPreflightInput) (MonthlyPreflight, error) {
	if os.Getenv("RUN_TENCENT_CREATE_RELEASE_EXECUTION") != "1" {
		return MonthlyPreflight{}, errors.New("live_mutation_flag_required")
	}
	evaluation := p.evaluateMonthlyPreflight(ctx, input)
	return evaluation.Result, evaluation.Err
}

func (p *TencentProvider) evaluateMonthlyPreflight(ctx context.Context, input MonthlyPreflightInput) monthlyPreflightEvaluation {
	if (input.ResourceType != "compute" && input.ResourceType != "storage") || (input.PackageID != "basic" && input.PackageID != "pro") || strings.TrimSpace(input.Zone) == "" ||
		(input.ResourceType == "compute" && input.SizeGB != 0) || (input.ResourceType == "storage" && input.SizeGB <= 0) {
		return monthlyPreflightEvaluation{Err: ErrInvalidMonthlyPreflight}
	}
	request := provisionerRequest{PackageID: input.PackageID, Zone: input.Zone}
	plan, err := configuredPackagePlan(input.PackageID)
	if err != nil {
		return monthlyPreflightEvaluation{Err: err}
	}
	expectedStages := []string{"node_pool_discovery", "tke_cluster_capacity", "node_pool_contract", "subnet", "zone", "cvm_prepaid_quota", "cvm_sku_price"}
	preflightStages := []MonthlyPreflightStage{}
	if input.ResourceType == "compute" {
		poolConfig, err := configuredPackageNodePool(input.PackageID)
		if err != nil {
			return monthlyPreflightEvaluation{Err: err}
		}
		request.Action = "capacity_preflight"
		request.Pool = provisionerPool{ID: plan.ID, PackageID: input.PackageID, InstanceType: plan.InstanceType, CPU: uint64(plan.CPU), MemoryGB: uint64(plan.MemoryGB), NodePoolID: poolConfig.NodePoolID, DesiredReplicas: 1, MaxReplicas: poolConfig.MaxReplicas}
		iamResponse, iamErr := p.provision(ctx, provisionerRequest{Action: "predebit_iam_gate", PackageID: input.PackageID, Zone: input.Zone})
		iamStage, gateErr := predebitIAMPreflightStage(iamResponse, iamErr)
		preflightStages = append(preflightStages, iamStage)
		if gateErr != nil {
			preflightStages = append(preflightStages, blockedPreflightStages(expectedStages, "tencent_predebit_iam")...)
			return monthlyPreflightEvaluation{Stages: preflightStages, Err: gateErr}
		}
	} else {
		request.Action = "storage_preflight"
		request.Storage = provisionerStorage{SizeGB: uint64(input.SizeGB), Zone: input.Zone, DiskType: firstNonEmpty(os.Getenv("TENCENT_CBS_DISK_TYPE"), "CLOUD_BSSD")}
		expectedStages = []string{"cbs_prepaid_quota", "cbs_price"}
	}
	if input.ResourceType == "compute" {
		if err := p.requireNodePatchRBAC(ctx); err != nil {
			return monthlyPreflightEvaluation{Err: err}
		}
	}
	response, err := p.provision(ctx, request)
	evaluation := monthlyPreflightEvaluation{Stages: append(preflightStages, reportStages(response, err, expectedStages)...)}
	if input.ResourceType == "compute" {
		if rbacErr := p.requireNodePatchRBAC(ctx); rbacErr != nil {
			evaluation.Err = rbacErr
			return evaluation
		}
	}
	if err != nil {
		evaluation.Err = err
		return evaluation
	}
	if !response.OK {
		evaluation.Err = provisionerError(response)
		return evaluation
	}
	validPrice := response.ProviderPriceCNY > 0 && !math.IsNaN(response.ProviderPriceCNY) && !math.IsInf(response.ProviderPriceCNY, 0)
	validFacts := response.Status == "ready" && response.ProviderData["chargeType"] == "PREPAID" && response.ProviderData["periodMonths"] == "1" &&
		response.ProviderData["renewFlag"] == "NOTIFY_AND_MANUAL_RENEW" && response.ProviderData["zone"] == input.Zone
	if input.ResourceType == "compute" {
		validFacts = validFacts && strings.TrimSpace(response.NodePoolID) != "" && response.InstanceType == plan.InstanceType && response.InstanceAvailable && len(response.Zones) == 1 && response.Zones[0] == input.Zone &&
			response.RemainingQuota >= uint64(request.Pool.DesiredReplicas) && strings.TrimSpace(response.ProviderRequestIDs["nodePool"]) != "" && strings.TrimSpace(response.ProviderRequestIDs["subnets"]) != "" && strings.TrimSpace(response.ProviderRequestIDs["availability"]) != "" && strings.TrimSpace(response.ProviderRequestIDs["quota"]) != ""
	} else {
		validFacts = validFacts && response.ProviderData["diskType"] == request.Storage.DiskType && response.ProviderData["sizeGb"] == strconv.Itoa(input.SizeGB) &&
			strings.TrimSpace(response.ProviderRequestIDs["quota"]) != "" && strings.TrimSpace(response.ProviderRequestIDs["price"]) != ""
	}
	if !validPrice || !validFacts {
		evaluation.Err = fmt.Errorf("monthly_preflight_provider_mismatch")
		return evaluation
	}
	evaluation.Result = MonthlyPreflight{
		ResourceType: input.ResourceType, PackageID: input.PackageID, NodePoolID: response.NodePoolID, SizeGB: input.SizeGB, Zone: input.Zone,
		Available: true, ChargeType: "PREPAID", PeriodMonths: 1, RenewFlag: "NOTIFY_AND_MANUAL_RENEW",
		ProviderPriceCNY: response.ProviderPriceCNY, ProviderRequestIDs: response.ProviderRequestIDs,
	}
	return evaluation
}

func predebitIAMPreflightStage(response provisionerResponse, err error) (MonthlyPreflightStage, error) {
	stage := MonthlyPreflightStage{Stage: "tencent_predebit_iam", Status: "failed", BlockedBy: []string{}, SafeFacts: map[string]any{}}
	if err != nil {
		stage.ErrorCode = "predebit_iam_unavailable"
		return stage, err
	}
	if !response.OK {
		stage.ErrorCode = firstNonEmpty(response.ErrorCode, "predebit_iam_unavailable")
		return stage, provisionerError(response)
	}
	if response.Status != "ready" || response.MutationCount != 0 || response.ProviderData["proofMode"] != "production_runner_deployment_attestation" ||
		response.ProviderData["requiredActions"] != "tag:TagResources,tag:ModifyResourcesTagValue" || response.ProviderData["releaseSha"] == "" || response.ProviderData["policyDigest"] == "" {
		stage.ErrorCode = "predebit_iam_provider_mismatch"
		return stage, errors.New(stage.ErrorCode)
	}
	stage.Status = "passed"
	stage.SafeFacts = map[string]any{
		"proofMode": response.ProviderData["proofMode"], "releaseBound": true,
		"requiredActions": []string{"tag:TagResources", "tag:ModifyResourcesTagValue"}, "policyDigest": response.ProviderData["policyDigest"],
	}
	return stage, nil
}

func (p *TencentProvider) requireNodePatchRBAC(ctx context.Context) error {
	if p.kubectl == nil {
		return errors.New("kubernetes_node_patch_rbac_unavailable")
	}
	output, err := p.kubectl(ctx, []string{"auth", "can-i", "patch", "nodes"}, nil)
	if err != nil || strings.TrimSpace(string(output)) != "yes" {
		return errors.New("kubernetes_node_patch_rbac_unavailable")
	}
	return nil
}

func (p *TencentProvider) MonthlyPreflightReport(ctx context.Context, input MonthlyPreflightReportInput) (MonthlyPreflightReport, error) {
	items := []MonthlyPreflightStage{
		monthlyPreflightEnvironmentStage("launch_permission", "RUN_TENCENT_CREATE_RELEASE_EXECUTION", "1"),
		monthlyPreflightCredentialsStage(),
	}
	if strings.TrimSpace(input.Zone) == "" || input.Zone != strings.TrimSpace(input.Zone) {
		return MonthlyPreflightReport{}, ErrInvalidMonthlyPreflight
	}
	packages := make([]MonthlyPreflightPackageReport, 0, 2)
	for _, current := range []struct {
		packageID string
		sizeGB    int
	}{{packageID: "basic", sizeGB: 10}, {packageID: "pro", sizeGB: 100}} {
		packageItems := []MonthlyPreflightStage{}
		if items[1].Status != "passed" {
			packageItems = blockedPreflightStages(monthlyPreflightProviderStageNames(), "credentials")
		} else {
			compute := p.evaluateMonthlyPreflight(ctx, MonthlyPreflightInput{ResourceType: "compute", PackageID: current.packageID, Zone: input.Zone})
			storage := p.evaluateMonthlyPreflight(ctx, MonthlyPreflightInput{ResourceType: "storage", PackageID: current.packageID, SizeGB: current.sizeGB, Zone: input.Zone})
			packageItems = append(packageItems, normalizedPreflightStages(compute, []string{"tencent_predebit_iam", "node_pool_discovery", "tke_cluster_capacity", "node_pool_contract", "subnet", "zone", "cvm_prepaid_quota", "cvm_sku_price"})...)
			packageItems = append(packageItems, normalizedPreflightStages(storage, []string{"cbs_prepaid_quota", "cbs_price"})...)
		}
		packages = append(packages, MonthlyPreflightPackageReport{PackageID: current.packageID, SizeGB: current.sizeGB, Status: preflightStatus(packageItems), Items: packageItems})
	}
	status := preflightStatus(items)
	for _, packageReport := range packages {
		if packageReport.Status != "passed" {
			status = "failed"
		}
	}
	return MonthlyPreflightReport{SchemaVersion: 1, Status: status, Zone: input.Zone, Items: items, Packages: packages}, nil
}

func monthlyPreflightProviderStageNames() []string {
	return []string{"tencent_predebit_iam", "node_pool_discovery", "tke_cluster_capacity", "node_pool_contract", "subnet", "zone", "cvm_prepaid_quota", "cvm_sku_price", "cbs_prepaid_quota", "cbs_price"}
}

func normalizedPreflightStages(evaluation monthlyPreflightEvaluation, expected []string) []MonthlyPreflightStage {
	if len(evaluation.Stages) == len(expected) {
		return evaluation.Stages
	}
	code := "monthly_preflight_unavailable"
	if evaluation.Err != nil {
		code = evaluation.Err.Error()
	}
	items := make([]MonthlyPreflightStage, 0, len(expected))
	for _, stage := range expected {
		items = append(items, MonthlyPreflightStage{Stage: stage, Status: "failed", ErrorCode: code, BlockedBy: []string{}, SafeFacts: map[string]any{}})
	}
	return items
}

func preflightStatus(items []MonthlyPreflightStage) string {
	for _, item := range items {
		if item.Status != "passed" {
			return "failed"
		}
	}
	return "passed"
}

func monthlyPreflightEnvironmentStage(stage, key, expected string) MonthlyPreflightStage {
	status, code := "passed", ""
	if os.Getenv(key) != expected {
		status, code = "failed", "live_mutation_flag_required"
	}
	return MonthlyPreflightStage{Stage: stage, Status: status, ErrorCode: code, BlockedBy: []string{}, SafeFacts: map[string]any{"enabled": status == "passed"}}
}

func monthlyPreflightCredentialsStage() MonthlyPreflightStage {
	missing := []string{}
	for _, key := range []string{"TENCENTCLOUD_SECRET_ID", "TENCENTCLOUD_SECRET_KEY", "TENCENTCLOUD_REGION", "TENCENT_DEPLOY_CLUSTER_ID"} {
		if strings.TrimSpace(os.Getenv(key)) == "" {
			missing = append(missing, key)
		}
	}
	status, code := "passed", ""
	if len(missing) > 0 {
		status, code = "failed", "tencent_env_missing"
	}
	return MonthlyPreflightStage{Stage: "credentials", Status: status, ErrorCode: code, BlockedBy: []string{}, SafeFacts: map[string]any{"available": len(missing) == 0}}
}

func blockedPreflightStages(names []string, dependency string) []MonthlyPreflightStage {
	items := make([]MonthlyPreflightStage, 0, len(names))
	for _, name := range names {
		items = append(items, MonthlyPreflightStage{
			Stage: name, Status: "blocked", ErrorCode: "preflight_dependency_blocked",
			BlockedBy: []string{dependency}, SafeFacts: map[string]any{},
		})
	}
	return items
}

func reportStages(response provisionerResponse, err error, expected []string) []MonthlyPreflightStage {
	byName := map[string]MonthlyPreflightStage{}
	for _, item := range response.PreflightStages {
		byName[item.Stage] = item
	}
	items := make([]MonthlyPreflightStage, 0, len(expected))
	for _, name := range expected {
		item, ok := byName[name]
		if !ok {
			code := response.ErrorCode
			if code == "" && err != nil {
				code = "monthly_preflight_unavailable"
			}
			item = MonthlyPreflightStage{Stage: name, Status: "failed", ErrorCode: code, BlockedBy: []string{}, SafeFacts: map[string]any{}}
		}
		if item.BlockedBy == nil {
			item.BlockedBy = []string{}
		}
		if item.SafeFacts == nil {
			item.SafeFacts = map[string]any{}
		}
		items = append(items, item)
	}
	return items
}

func (p *TencentProvider) MonthlyProviderTruth(ctx context.Context, compute ComputeAllocation, storage StorageVolume) (MonthlyProviderTruth, error) {
	truth := unknownMonthlyProviderTruth(compute, storage)
	clusterID := strings.TrimSpace(os.Getenv("TENCENT_DEPLOY_CLUSTER_ID"))
	if !validMonthlyProviderTruthIdentity(compute, storage) || clusterID == "" {
		return truth, ErrInvalidMonthlyProviderTruth
	}
	instanceID := firstNonEmpty(compute.InstanceID, compute.CVMInstanceID)
	instanceType := firstNonEmpty(compute.InstanceType, compute.ProviderData["instanceType"])
	response, err := p.provision(ctx, provisionerRequest{
		Action: "provider_truth", AccountID: compute.AccountID, PackageID: compute.PackageID, Zone: storage.Zone,
		StorageVolumeID: storage.ProviderResourceID, Tags: maps.Clone(storage.CostTags), ComputeTags: maps.Clone(compute.CostTags),
		Pool: provisionerPool{
			ID: compute.PoolID, ClusterID: clusterID, PackageID: compute.PackageID, InstanceType: instanceType, NodePoolID: compute.NodePoolID,
		},
		Allocation: provisionerAllocation{
			ID: compute.ID, InstanceID: instanceID, MachineName: firstNonEmpty(compute.MachineName, compute.ProviderData["machineName"]),
			NodeName: compute.NodeName, PrivateIP: compute.PrivateIP, PublicIP: compute.PublicIP, Deadline: compute.Deadline,
		},
		Storage: provisionerStorage{
			ID: storage.ProviderResourceID, SizeGB: uint64(storage.SizeGB), Zone: storage.Zone, DiskType: storage.DiskType, Deadline: storage.Deadline,
		},
	})
	if err != nil {
		return truth, err
	}
	truth.ProviderRequestID, truth.ErrorCode = response.ProviderRequestID, response.ErrorCode
	truth.Compute.ProviderRequestID, truth.Storage.ProviderRequestID = firstNonEmpty(response.ProviderRequestID, truth.Compute.ProviderRequestID), firstNonEmpty(response.ProviderRequestID, truth.Storage.ProviderRequestID)
	truth.Compute.CVMStatus = firstNonEmpty(response.CVMStatus, truth.Compute.CVMStatus)
	truth.Compute.ProviderData = maps.Clone(truth.Compute.ProviderData)
	if truth.Compute.ProviderData == nil {
		truth.Compute.ProviderData = map[string]string{}
	}
	for key, value := range response.ProviderData {
		truth.Compute.ProviderData[key] = value
	}
	truth.Compute.InstanceType = firstNonEmpty(response.InstanceType, response.ProviderData["instanceType"], truth.Compute.InstanceType, truth.Compute.ProviderData["instanceType"])
	truth.Compute.Zone = firstNonEmpty(response.ProviderData["zone"], truth.Compute.Zone, truth.Compute.ProviderData["zone"])
	truth.Compute.ChargeType = firstNonEmpty(response.ProviderData["chargeType"], truth.Compute.ChargeType)
	truth.Compute.RenewFlag = firstNonEmpty(response.ProviderData["renewFlag"], truth.Compute.RenewFlag)
	truth.Compute.Deadline = firstNonEmpty(response.ProviderData["deadline"], truth.Compute.Deadline)
	applyMonthlyStorageTruth(&truth.Storage, response)

	if response.MachinePresent != nil {
		if *response.MachinePresent && validMonthlyComputeProviderTruth(response, compute) {
			truth.ComputeState, truth.Compute.Status = "ready", "ready"
		} else if !*response.MachinePresent && response.ProviderRequestID != "" && response.CVMStatus == "NOT_FOUND" && response.TKEStatus == "NOT_FOUND" {
			truth.ComputeState, truth.Compute.Status = "absent", "external_deleted"
		}
	}
	if response.StoragePresent != nil {
		if *response.StoragePresent && validMonthlyStorageProviderTruth(response, storage) {
			truth.StorageState, truth.Storage.Status = "ready", "ready"
		} else if !*response.StoragePresent && response.ProviderRequestID != "" && response.CBSStatus == "NOT_FOUND" {
			truth.StorageState, truth.Storage.Status = "absent", "external_deleted"
		}
	}
	if response.OK && truth.ComputeState == "unknown" && truth.StorageState == "unknown" && response.ErrorCode == "" {
		return unknownMonthlyProviderTruth(compute, storage), fmt.Errorf("provider_truth_response_invalid")
	}
	return truth, nil
}

func validMonthlyComputeProviderTruth(response provisionerResponse, expected ComputeAllocation) bool {
	instanceID := firstNonEmpty(expected.InstanceID, expected.CVMInstanceID)
	instanceType := firstNonEmpty(expected.InstanceType, expected.ProviderData["instanceType"])
	zone := firstNonEmpty(expected.Zone, expected.ProviderData["zone"])
	if response.ProviderRequestID == "" || response.InstanceID != instanceID || response.InstanceType != instanceType || response.PrivateIP != expected.PrivateIP ||
		response.CVMStatus == "" || response.CVMStatus == "NOT_FOUND" || response.TKEStatus == "" || response.TKEStatus == "NOT_FOUND" ||
		response.ProviderData["machineName"] != firstNonEmpty(expected.MachineName, expected.ProviderData["machineName"]) || response.ProviderData["instanceType"] != instanceType ||
		response.ProviderData["zone"] != zone || response.ProviderData["chargeType"] != "PREPAID" || response.ProviderData["renewFlag"] != "NOTIFY_AND_MANUAL_RENEW" || !validProviderTruthDeadline(response.ProviderData["deadline"]) {
		return false
	}
	for key, value := range expected.CostTags {
		if response.ProviderData["computeTag:"+key] != value {
			return false
		}
	}
	return true
}

func validMonthlyStorageProviderTruth(response provisionerResponse, expected StorageVolume) bool {
	if response.ProviderRequestID == "" || !isCBSProviderReady(response.CBSStatus) || response.ProviderData["storageChargeType"] != "PREPAID" ||
		response.ProviderData["storageRenewFlag"] != "NOTIFY_AND_MANUAL_RENEW" || !validProviderTruthDeadline(response.ProviderData["storageDeadline"]) ||
		response.ProviderData["storageDiskType"] != expected.DiskType || response.ProviderData["storageSizeGb"] != strconv.Itoa(expected.SizeGB) || response.ProviderData["storageZone"] != expected.Zone {
		return false
	}
	for key, value := range expected.CostTags {
		if response.ProviderData[key] != value {
			return false
		}
	}
	return true
}

func validProviderTruthDeadline(value string) bool {
	_, err := time.Parse(time.RFC3339, value)
	return err == nil
}

func applyMonthlyStorageTruth(storage *StorageVolume, response provisionerResponse) {
	storage.CBSStatus = firstNonEmpty(response.CBSStatus, storage.CBSStatus)
	storage.ProviderData = maps.Clone(storage.ProviderData)
	if storage.ProviderData == nil {
		storage.ProviderData = map[string]string{}
	}
	for target, source := range map[string]string{
		"chargeType": "storageChargeType", "diskChargeType": "storageChargeType", "renewFlag": "storageRenewFlag", "deadline": "storageDeadline",
		"diskType": "storageDiskType", "sizeGb": "storageSizeGb", "zone": "storageZone",
	} {
		if value := response.ProviderData[source]; value != "" {
			storage.ProviderData[target] = value
		}
	}
	storage.DiskType = firstNonEmpty(response.ProviderData["storageDiskType"], storage.DiskType)
	storage.RenewFlag = firstNonEmpty(response.ProviderData["storageRenewFlag"], storage.RenewFlag)
	storage.Deadline = firstNonEmpty(response.ProviderData["storageDeadline"], storage.Deadline)
	storage.Zone = firstNonEmpty(response.ProviderData["storageZone"], storage.Zone)
}

func (p *TencentProvider) UpsertGatewaySecret(ctx context.Context, input GatewaySecretInput) (GatewaySecret, error) {
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(input.GatewayAPIKey)))
	secret := GatewaySecret{SecretRef: gatewaySecretName(input.WorkspaceID), Version: digest[:16], Fingerprint: "sha256:" + digest}
	if input.Fingerprint != secret.Fingerprint {
		return GatewaySecret{}, fmt.Errorf("gateway_secret_fingerprint_mismatch")
	}
	manifest := mustJSON(map[string]any{
		"apiVersion": "v1", "kind": "Secret", "type": "Opaque",
		"metadata": map[string]any{
			"name":   secret.SecretRef,
			"labels": map[string]any{"app.kubernetes.io/name": "opl-gateway-secret"},
			"annotations": map[string]any{
				"oplcloud.cn/account-id": input.AccountID, "oplcloud.cn/workspace-id": input.WorkspaceID,
				"oplcloud.cn/workspace-api-key-id": strconv.FormatInt(input.WorkspaceAPIKeyID, 10),
				"oplcloud.cn/secret-version":       secret.Version, "oplcloud.cn/secret-fingerprint": secret.Fingerprint,
			},
		},
		"stringData": map[string]any{"opl_gateway_api_key": input.GatewayAPIKey},
	})
	if _, err := p.callKubectl(ctx, []string{"apply", "-f", "-"}, manifest, protectedresource.Target{}); err != nil {
		return GatewaySecret{}, err
	}
	readback, err := p.callKubectl(ctx, []string{"get", "secret/" + secret.SecretRef, "-o", "json"}, nil, protectedresource.Target{})
	if err != nil {
		return GatewaySecret{}, err
	}
	var actual struct {
		Kind     string `json:"kind"`
		Type     string `json:"type"`
		Metadata struct {
			Name        string            `json:"name"`
			Labels      map[string]string `json:"labels"`
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
		Data map[string]string `json:"data"`
	}
	if json.Unmarshal(readback, &actual) != nil {
		return GatewaySecret{}, fmt.Errorf("gateway_secret_readback_mismatch")
	}
	rawKey, err := base64.StdEncoding.DecodeString(actual.Data["opl_gateway_api_key"])
	if err != nil {
		return GatewaySecret{}, fmt.Errorf("gateway_secret_readback_mismatch")
	}
	actualDigest := fmt.Sprintf("%x", sha256.Sum256(rawKey))
	if actual.Kind != "Secret" || actual.Type != "Opaque" || actual.Metadata.Name != secret.SecretRef ||
		actual.Metadata.Labels["app.kubernetes.io/name"] != "opl-gateway-secret" || actual.Metadata.Annotations["oplcloud.cn/account-id"] != input.AccountID ||
		actual.Metadata.Annotations["oplcloud.cn/workspace-id"] != input.WorkspaceID || actual.Metadata.Annotations["oplcloud.cn/workspace-api-key-id"] != strconv.FormatInt(input.WorkspaceAPIKeyID, 10) ||
		actual.Metadata.Annotations["oplcloud.cn/secret-version"] != secret.Version || actual.Metadata.Annotations["oplcloud.cn/secret-fingerprint"] != secret.Fingerprint ||
		"sha256:"+actualDigest != secret.Fingerprint {
		return GatewaySecret{}, fmt.Errorf("gateway_secret_readback_mismatch")
	}
	return secret, nil
}

func (p *TencentProvider) ReadGatewaySecret(ctx context.Context, input GatewaySecretInput) (GatewaySecret, error) {
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(input.GatewayAPIKey)))
	if input.Fingerprint != "sha256:"+digest {
		return GatewaySecret{}, fmt.Errorf("gateway_secret_readback_mismatch")
	}
	return p.ReadGatewaySecretByDigest(ctx, GatewaySecretReadbackInput{
		AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, WorkspaceAPIKeyID: input.WorkspaceAPIKeyID,
		SecretRef: gatewaySecretName(input.WorkspaceID), Fingerprint: input.Fingerprint, KeyDigest: digest,
	})
}

func (p *TencentProvider) ReadGatewaySecretByDigest(ctx context.Context, input GatewaySecretReadbackInput) (GatewaySecret, error) {
	if input.AccountID == "" || input.WorkspaceID == "" || input.WorkspaceAPIKeyID <= 0 || len(input.KeyDigest) != 64 ||
		input.SecretRef != gatewaySecretName(input.WorkspaceID) || input.Fingerprint != "sha256:"+input.KeyDigest {
		return GatewaySecret{}, fmt.Errorf("gateway_secret_readback_mismatch")
	}
	expected := GatewaySecret{SecretRef: input.SecretRef, Version: input.KeyDigest[:16], Fingerprint: input.Fingerprint}
	readback, err := p.callKubectl(ctx, []string{"get", "secret/" + expected.SecretRef, "-o", "json"}, nil, protectedresource.Target{})
	if err != nil {
		return GatewaySecret{}, err
	}
	var actual struct {
		Kind     string `json:"kind"`
		Type     string `json:"type"`
		Metadata struct {
			Name        string            `json:"name"`
			Labels      map[string]string `json:"labels"`
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
		Data map[string]string `json:"data"`
	}
	if json.Unmarshal(readback, &actual) != nil {
		return GatewaySecret{}, fmt.Errorf("gateway_secret_readback_mismatch")
	}
	rawKey, err := base64.StdEncoding.DecodeString(actual.Data["opl_gateway_api_key"])
	if err != nil {
		return GatewaySecret{}, fmt.Errorf("gateway_secret_readback_mismatch")
	}
	actualDigest := fmt.Sprintf("%x", sha256.Sum256(rawKey))
	if actual.Kind != "Secret" || actual.Type != "Opaque" || actual.Metadata.Name != expected.SecretRef ||
		actual.Metadata.Labels["app.kubernetes.io/name"] != "opl-gateway-secret" || actual.Metadata.Annotations["oplcloud.cn/account-id"] != input.AccountID ||
		actual.Metadata.Annotations["oplcloud.cn/workspace-id"] != input.WorkspaceID || actual.Metadata.Annotations["oplcloud.cn/workspace-api-key-id"] != strconv.FormatInt(input.WorkspaceAPIKeyID, 10) ||
		actual.Metadata.Annotations["oplcloud.cn/secret-version"] != expected.Version || actual.Metadata.Annotations["oplcloud.cn/secret-fingerprint"] != expected.Fingerprint ||
		"sha256:"+actualDigest != expected.Fingerprint {
		return GatewaySecret{}, fmt.Errorf("gateway_secret_readback_mismatch")
	}
	return expected, nil
}

func gatewaySecretName(workspaceID string) string {
	return "opl-gateway-" + stableSuffix(workspaceID)[:16]
}

type provisionerRequest struct {
	Action          string                `json:"action"`
	DryRun          bool                  `json:"dryRun,omitempty"`
	AccountID       string                `json:"accountId,omitempty"`
	UserID          string                `json:"userId,omitempty"`
	PackageID       string                `json:"packageId,omitempty"`
	Zone            string                `json:"zone,omitempty"`
	StorageVolumeID string                `json:"storageVolumeId,omitempty"`
	Tags            map[string]string     `json:"tags,omitempty"`
	ComputeTags     map[string]string     `json:"computeTags,omitempty"`
	Pool            provisionerPool       `json:"pool,omitempty"`
	Allocation      provisionerAllocation `json:"allocation,omitempty"`
	Storage         provisionerStorage    `json:"storage,omitempty"`
}

type provisionerPool struct {
	ID                 string            `json:"id,omitempty"`
	ClusterID          string            `json:"clusterId,omitempty"`
	PackageID          string            `json:"packageId,omitempty"`
	InstanceType       string            `json:"instanceType,omitempty"`
	CPU                uint64            `json:"cpu,omitempty"`
	MemoryGB           uint64            `json:"memoryGb,omitempty"`
	NodePoolID         string            `json:"nodePoolId,omitempty"`
	Labels             map[string]string `json:"desiredNodeLabels,omitempty"`
	DesiredReplicas    int64             `json:"desiredReplicas,omitempty"`
	MaxReplicas        int64             `json:"maxReplicas,omitempty"`
	BaselineReplicas   int64             `json:"baselineReplicas,omitempty"`
	TargetReplicas     int64             `json:"targetReplicas,omitempty"`
	BeforeMachineNames []string          `json:"beforeMachineNames,omitempty"`
}

type packageNodePoolConfig struct {
	NodePoolID  string
	MaxReplicas int64
}

func configuredPackageNodePool(packageID string) (packageNodePoolConfig, error) {
	prefix := ""
	switch packageID {
	case "basic":
		prefix = "OPL_BASIC_COMPUTE_NODE_POOL_"
	case "pro":
		prefix = "OPL_PRO_COMPUTE_NODE_POOL_"
	default:
		return packageNodePoolConfig{}, ErrUnsupportedComputePackage
	}
	config := packageNodePoolConfig{NodePoolID: strings.TrimSpace(os.Getenv(prefix + "ID"))}
	maxRaw := strings.TrimSpace(os.Getenv(prefix + "MAX_REPLICAS"))
	maxReplicas, err := strconv.ParseInt(maxRaw, 10, 64)
	if config.NodePoolID == "" || err != nil || maxReplicas <= 0 {
		return packageNodePoolConfig{}, fmt.Errorf("compute_node_pool_configuration_required")
	}
	config.MaxReplicas = maxReplicas
	otherPrefix := "OPL_PRO_COMPUTE_NODE_POOL_ID"
	if packageID == "pro" {
		otherPrefix = "OPL_BASIC_COMPUTE_NODE_POOL_ID"
	}
	if other := strings.TrimSpace(os.Getenv(otherPrefix)); other != "" && other == config.NodePoolID {
		return packageNodePoolConfig{}, fmt.Errorf("compute_node_pool_configuration_invalid")
	}
	return config, nil
}

type provisionerAllocation struct {
	ID          string `json:"id,omitempty"`
	InstanceID  string `json:"instanceId,omitempty"`
	MachineName string `json:"machineName,omitempty"`
	NodeName    string `json:"nodeName,omitempty"`
	PrivateIP   string `json:"privateIp,omitempty"`
	PublicIP    string `json:"publicIp,omitempty"`
	Deadline    string `json:"deadline,omitempty"`
}

type provisionerStorage struct {
	ID                         string `json:"id,omitempty"`
	SizeGB                     uint64 `json:"sizeGb,omitempty"`
	Zone                       string `json:"zone,omitempty"`
	DiskType                   string `json:"diskType,omitempty"`
	Deadline                   string `json:"deadline,omitempty"`
	ExpectedState              string `json:"expectedState,omitempty"`
	ExpectedProviderResourceID string `json:"expectedProviderResourceId,omitempty"`
	AllowExistingExactReplay   bool   `json:"allowExistingExactReplay,omitempty"`
}

type provisionerResponse struct {
	OK                      bool                                 `json:"ok"`
	OperationID             string                               `json:"operationId,omitempty"`
	PoolID                  string                               `json:"poolId,omitempty"`
	NodePoolID              string                               `json:"nodePoolId,omitempty"`
	InstanceID              string                               `json:"instanceId,omitempty"`
	NodeName                string                               `json:"nodeName,omitempty"`
	PrivateIP               string                               `json:"privateIp,omitempty"`
	PublicIP                string                               `json:"publicIp,omitempty"`
	MachinePresent          *bool                                `json:"machinePresent,omitempty"`
	StoragePresent          *bool                                `json:"storagePresent,omitempty"`
	StorageVolumeID         string                               `json:"storageVolumeId,omitempty"`
	StorageState            string                               `json:"storageState,omitempty"`
	CBSStatus               string                               `json:"cbsStatus,omitempty"`
	CVMStatus               string                               `json:"cvmStatus,omitempty"`
	TKEStatus               string                               `json:"tkeStatus,omitempty"`
	Status                  string                               `json:"status,omitempty"`
	ProviderRequestID       string                               `json:"providerRequestId,omitempty"`
	ProviderRequestIDs      map[string]string                    `json:"providerRequestIds,omitempty"`
	ProviderPriceCNY        float64                              `json:"providerPriceCny,omitempty"`
	ProviderData            map[string]string                    `json:"providerData,omitempty"`
	ErrorCode               string                               `json:"errorCode,omitempty"`
	Message                 string                               `json:"message,omitempty"`
	Retryable               bool                                 `json:"retryable,omitempty"`
	MissingEnv              []string                             `json:"missingEnv,omitempty"`
	Machines                []provisionerMachine                 `json:"machines,omitempty"`
	InstanceType            string                               `json:"instanceType,omitempty"`
	InstanceAvailable       bool                                 `json:"instanceAvailable,omitempty"`
	RemainingQuota          uint64                               `json:"remainingQuota,omitempty"`
	Zones                   []string                             `json:"zones,omitempty"`
	PreflightStages         []MonthlyPreflightStage              `json:"preflightStages,omitempty"`
	CurrentReplicas         int64                                `json:"currentReplicas,omitempty"`
	ReadyReplicas           int64                                `json:"readyReplicas,omitempty"`
	MaxReplicas             int64                                `json:"maxReplicas,omitempty"`
	TargetReplicas          int64                                `json:"targetReplicas,omitempty"`
	MutationCount           int                                  `json:"mutationCount"`
	FailureStage            string                               `json:"failureStage,omitempty"`
	ProviderErrorClass      string                               `json:"providerErrorClass,omitempty"`
	ProviderIdentityFailure *ComputeClaimProviderIdentityFailure `json:"providerIdentityFailure,omitempty"`
	MutationEvidence        *ComputeClaimMutationEvidence        `json:"mutationEvidence,omitempty"`
}

type provisionerMachine struct {
	MachineID    string `json:"machineId"`
	InstanceID   string `json:"instanceId,omitempty"`
	NodeName     string `json:"nodeName,omitempty"`
	PrivateIP    string `json:"privateIp,omitempty"`
	PublicIP     string `json:"publicIp,omitempty"`
	InstanceType string `json:"instanceType,omitempty"`
	Zone         string `json:"zone,omitempty"`
	ChargeType   string `json:"chargeType,omitempty"`
	RenewFlag    string `json:"renewFlag,omitempty"`
	Deadline     string `json:"deadline,omitempty"`
	Ready        bool   `json:"ready"`
}

func (p *TencentProvider) PrepareComputeAllocation(ctx context.Context, input ComputeAllocationInput) (ComputeAllocationPreparation, error) {
	packagePlan, err := configuredPackagePlan(input.PackageID)
	if err != nil {
		return ComputeAllocationPreparation{}, err
	}
	poolConfig, err := configuredPackageNodePool(input.PackageID)
	prepared := ComputeAllocationPreparation{PoolID: packagePlan.ID, PackageID: input.PackageID, NodePoolID: poolConfig.NodePoolID, InstanceType: packagePlan.InstanceType, MaxReplicas: poolConfig.MaxReplicas}
	if err != nil {
		return prepared, err
	}
	if strings.TrimSpace(input.NodePoolID) != prepared.NodePoolID {
		return prepared, protectedresource.ErrPackagePoolMismatch
	}
	response, err := p.provision(ctx, provisionerRequest{
		Action: "prepare_compute_allocation", DryRun: input.DryRun, PackageID: input.PackageID,
		Pool: provisionerPool{
			ID: packagePlan.ID, PackageID: input.PackageID, InstanceType: packagePlan.InstanceType,
			CPU: uint64(packagePlan.CPU), MemoryGB: uint64(packagePlan.MemoryGB), NodePoolID: prepared.NodePoolID, MaxReplicas: prepared.MaxReplicas,
		},
		Allocation: provisionerAllocation{ID: input.ID},
	})
	if err != nil {
		return prepared, err
	}
	if !response.OK {
		return prepared, provisionerError(response)
	}
	prepared.BaselineReplicas = response.CurrentReplicas
	prepared.TargetReplicas = response.TargetReplicas
	prepared.ProviderRequestID = response.ProviderRequestID
	prepared.BeforeMachineNames = make([]string, 0, len(response.Machines))
	for _, machine := range response.Machines {
		prepared.BeforeMachineNames = append(prepared.BeforeMachineNames, machine.MachineID)
	}
	return prepared, nil
}

func (p *TencentProvider) CreateComputeAllocation(ctx context.Context, input ComputeAllocationExecution) (ComputeAllocation, error) {
	allocation, prepared := input.Allocation, input.Plan
	packagePlan := packagePlan(prepared.PackageID)
	response, err := p.provision(ctx, provisionerRequest{
		Action: "create_compute_allocation", DryRun: input.DryRun, AccountID: allocation.AccountID, PackageID: allocation.PackageID,
		Tags: oplCostTags(allocation.AccountID, allocation.WorkspaceID, allocation.ID, allocation.ProviderRequestID),
		Pool: provisionerPool{
			ID: prepared.PoolID, PackageID: prepared.PackageID, InstanceType: prepared.InstanceType, NodePoolID: prepared.NodePoolID,
			CPU: uint64(packagePlan.CPU), MemoryGB: uint64(packagePlan.MemoryGB),
			MaxReplicas: prepared.MaxReplicas, BaselineReplicas: prepared.BaselineReplicas, TargetReplicas: prepared.TargetReplicas, BeforeMachineNames: append([]string(nil), prepared.BeforeMachineNames...),
		},
		Allocation: provisionerAllocation{ID: allocation.ID},
	})
	if err != nil {
		return allocation, err
	}
	allocation.ProviderRequestID = firstNonEmpty(response.ProviderRequestID, allocation.ProviderRequestID)
	allocation.PoolID = prepared.PoolID
	allocation.NodePoolID = prepared.NodePoolID
	allocation.InstanceType = prepared.InstanceType
	allocation.Status = firstNonEmpty(response.Status, allocation.Status)
	allocation.InstanceID = response.InstanceID
	allocation.CVMInstanceID = response.InstanceID
	allocation.MachineName = response.ProviderData["machineName"]
	allocation.NodeName = response.NodeName
	allocation.PrivateIP = response.PrivateIP
	allocation.PublicIP = response.PublicIP
	allocation.Zone = response.ProviderData["zone"]
	allocation.ChargeType = response.ProviderData["chargeType"]
	allocation.RenewFlag = response.ProviderData["renewFlag"]
	allocation.Deadline = response.ProviderData["deadline"]
	allocation.ProviderData = maps.Clone(response.ProviderData)
	allocation.ProviderResourceID = firstNonEmpty(response.InstanceID, allocation.ProviderResourceID)
	if !response.OK {
		if response.Retryable {
			return allocation, ErrComputeAllocationPending
		}
		return allocation, provisionerError(response)
	}
	return allocation, nil
}

func (p *TencentProvider) DiscoverComputeAllocation(ctx context.Context, allocation ComputeAllocation, prepared ComputeAllocationPreparation) (ComputeAllocation, error) {
	packagePlan := packagePlan(prepared.PackageID)
	response, err := p.provision(ctx, provisionerRequest{
		Action: "read_compute_allocation", AccountID: allocation.AccountID, PackageID: allocation.PackageID,
		Pool: provisionerPool{
			ID: prepared.PoolID, PackageID: prepared.PackageID, InstanceType: prepared.InstanceType, NodePoolID: prepared.NodePoolID,
			CPU: uint64(packagePlan.CPU), MemoryGB: uint64(packagePlan.MemoryGB), MaxReplicas: prepared.MaxReplicas,
			BaselineReplicas: prepared.BaselineReplicas, TargetReplicas: prepared.TargetReplicas,
			BeforeMachineNames: append([]string(nil), prepared.BeforeMachineNames...),
		},
		Allocation: provisionerAllocation{ID: allocation.ID},
	})
	if err != nil {
		return allocation, err
	}
	allocation.ProviderRequestID = firstNonEmpty(response.ProviderRequestID, allocation.ProviderRequestID)
	allocation.PoolID = prepared.PoolID
	allocation.NodePoolID = prepared.NodePoolID
	allocation.InstanceType = prepared.InstanceType
	allocation.Status = firstNonEmpty(response.Status, allocation.Status)
	allocation.InstanceID = response.InstanceID
	allocation.CVMInstanceID = response.InstanceID
	allocation.MachineName = response.ProviderData["machineName"]
	allocation.NodeName = response.NodeName
	allocation.PrivateIP = response.PrivateIP
	allocation.PublicIP = response.PublicIP
	allocation.Zone = response.ProviderData["zone"]
	allocation.ChargeType = response.ProviderData["chargeType"]
	allocation.RenewFlag = response.ProviderData["renewFlag"]
	allocation.Deadline = response.ProviderData["deadline"]
	allocation.ProviderData = maps.Clone(response.ProviderData)
	allocation.ProviderResourceID = firstNonEmpty(response.InstanceID, allocation.ProviderResourceID)
	if !response.OK {
		if response.Retryable {
			return allocation, ErrComputeAllocationPending
		}
		return allocation, provisionerError(response)
	}
	return allocation, nil
}

func (p *TencentProvider) ProveComputeClaimRecovery(ctx context.Context, allocation ComputeAllocation, prepared ComputeAllocationPreparation, ownership MachineOwnership) (ComputeClaimProviderProof, error) {
	proof := ComputeClaimProviderProof{Reason: "identity_mismatch"}
	plan := packagePlan(allocation.PackageID)
	instanceID := firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID)
	if allocation.ID == "" || allocation.AccountID == "" || allocation.WorkspaceID == "" || (allocation.PackageID != "basic" && allocation.PackageID != "pro") ||
		allocation.PoolID != prepared.PoolID || allocation.NodePoolID != prepared.NodePoolID || prepared.PackageID != allocation.PackageID ||
		prepared.InstanceType != plan.InstanceType || allocation.InstanceType != prepared.InstanceType || prepared.MaxReplicas <= 0 || prepared.BaselineReplicas < 0 ||
		prepared.TargetReplicas != prepared.BaselineReplicas+1 || int64(len(prepared.BeforeMachineNames)) != prepared.BaselineReplicas ||
		allocation.MachineName == "" || !strings.HasPrefix(instanceID, "ins-") || allocation.NodeName == "" || allocation.PrivateIP == "" || allocation.Zone == "" ||
		ownership.ResourceID != allocation.ID || ownership.AccountID != allocation.AccountID || ownership.WorkspaceID != allocation.WorkspaceID ||
		ownership.PackageID != allocation.PackageID || ownership.NodePoolID != allocation.NodePoolID || ownership.MachineID != allocation.MachineName ||
		ownership.InstanceID != instanceID || ownership.NodeName != allocation.NodeName || ownership.ID == "" {
		return proof, computeClaimProviderError(proof.Reason)
	}
	if err := protectedresource.FromEnv().Check(protectedresource.Target{
		PackageID: ownership.PackageID, NodePoolID: ownership.NodePoolID, MachineID: ownership.MachineID,
		NodeName: ownership.NodeName, CVMID: ownership.InstanceID,
	}); err != nil {
		return proof, computeClaimProviderError(proof.Reason)
	}
	response, err := p.provision(ctx, provisionerRequest{
		Action: "compute_claim_truth", AccountID: allocation.AccountID, PackageID: allocation.PackageID, Zone: allocation.Zone,
		Tags: oplCostTags(allocation.AccountID, allocation.WorkspaceID, allocation.ID, ownership.ID),
		Pool: provisionerPool{
			ID: prepared.PoolID, PackageID: prepared.PackageID, InstanceType: prepared.InstanceType, CPU: uint64(plan.CPU), MemoryGB: uint64(plan.MemoryGB),
			NodePoolID: prepared.NodePoolID, MaxReplicas: prepared.MaxReplicas, BaselineReplicas: prepared.BaselineReplicas,
			TargetReplicas: prepared.TargetReplicas, BeforeMachineNames: append([]string(nil), prepared.BeforeMachineNames...),
		},
		Allocation: provisionerAllocation{
			ID: allocation.ID, InstanceID: instanceID, MachineName: allocation.MachineName, NodeName: allocation.NodeName,
			PrivateIP: allocation.PrivateIP, PublicIP: allocation.PublicIP, Deadline: allocation.Deadline,
		},
	})
	if err != nil {
		proof.Reason = "provider_describe"
		return proof, computeClaimProviderError(proof.Reason)
	}
	if !response.OK {
		proof.Reason = safeComputeClaimRecoveryReason(response.ErrorCode, "provider_describe")
		proof.FailureStage = response.FailureStage
		proof.ProviderErrorClass = response.ProviderErrorClass
		proof.ProviderIdentityFailure = cloneComputeClaimProviderIdentityFailure(response.ProviderIdentityFailure)
		return proof, computeClaimProviderError(proof.Reason)
	}
	periodMonths, periodErr := strconv.Atoi(response.ProviderData["periodMonths"])
	proof = ComputeClaimProviderProof{
		Status: response.Status, MachineName: response.ProviderData["machineName"], NodeName: response.NodeName,
		CVMInstanceID: response.InstanceID, PrivateIP: response.PrivateIP, InstanceType: response.InstanceType,
		Zone: response.ProviderData["zone"], ChargeType: response.ProviderData["chargeType"], PeriodMonths: periodMonths,
		RenewFlag: response.ProviderData["renewFlag"], Deadline: response.ProviderData["deadline"],
		CVMOwnershipState: response.ProviderData["cvmOwnershipState"],
	}
	if periodErr != nil || proof.Status != "proven" || proof.MachineName != allocation.MachineName || proof.NodeName != allocation.NodeName ||
		proof.CVMInstanceID != instanceID || proof.PrivateIP != allocation.PrivateIP || proof.InstanceType != prepared.InstanceType || proof.Zone != allocation.Zone ||
		proof.ChargeType != "PREPAID" || proof.PeriodMonths != 1 || proof.RenewFlag != "NOTIFY_AND_MANUAL_RENEW" || proof.Deadline != allocation.Deadline ||
		(proof.CVMOwnershipState != "recoverable" && proof.CVMOwnershipState != "target_owned") {
		proof.Reason = "identity_mismatch"
		proof.FailureStage, proof.ProviderErrorClass = "cvm_pre_read", "readback_mismatch"
		proof.ProviderIdentityFailure = newComputeClaimProviderIdentityFailure("compute_claim.provider_response_identity", map[string]any{
			"status": "proven", "machineName": allocation.MachineName, "nodeName": allocation.NodeName, "cvmInstanceId": instanceID,
			"privateIp": allocation.PrivateIP, "instanceType": prepared.InstanceType, "zone": allocation.Zone, "chargeType": "PREPAID",
			"periodMonths": 1, "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": allocation.Deadline,
		}, proof)
		return proof, computeClaimProviderError(proof.Reason)
	}
	nodeRaw, err := p.callKubectl(ctx, []string{"get", "node/" + allocation.NodeName, "-o", "json"}, nil, protectedresource.Target{})
	if err != nil {
		proof.Reason = "provider_describe"
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "forbidden") || strings.Contains(message, "unauthorized") || strings.Contains(message, "permission") {
			proof.Reason = "iam_rbac"
		}
		return proof, computeClaimProviderError(proof.Reason)
	}
	nodeState, ok := computeClaimNodeOwnershipState(nodeRaw, allocation, ownership)
	if !ok {
		proof.Reason = "node_ownership_conflict"
		if nodeState == "identity_mismatch" {
			proof.Reason = "identity_mismatch"
			proof.FailureStage, proof.ProviderErrorClass = "node_pre_read", "readback_mismatch"
			proof.ProviderIdentityFailure = newComputeClaimProviderIdentityFailure("compute_claim.kubernetes_node_identity", map[string]any{
				"nodeName": allocation.NodeName, "privateIp": allocation.PrivateIP, "resourceId": allocation.ID,
				"accountId": allocation.AccountID, "workspaceId": allocation.WorkspaceID,
			}, json.RawMessage(nodeRaw))
		}
		return proof, computeClaimProviderError(proof.Reason)
	}
	proof.NodeOwnershipState = nodeState
	proof.Reason = ""
	return proof, nil
}

func newComputeClaimProviderIdentityFailure(predicate string, expected, actual any) *ComputeClaimProviderIdentityFailure {
	digest := func(value any) (string, bool) {
		raw, err := json.Marshal(value)
		if err != nil {
			return "", false
		}
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:]), true
	}
	expectedDigest, expectedOK := digest(expected)
	actualDigest, actualOK := digest(actual)
	if !expectedOK || !actualOK || expectedDigest == actualDigest {
		return nil
	}
	value := &ComputeClaimProviderIdentityFailure{
		Predicate: predicate, ExpectedDigest: expectedDigest, ActualDigest: actualDigest,
	}
	if !validComputeClaimProviderIdentityFailure(value) {
		return nil
	}
	return value
}

func (p *TencentProvider) ClaimComputeRecovery(ctx context.Context, allocation ComputeAllocation, prepared ComputeAllocationPreparation, ownership MachineOwnership) (ComputeClaimProviderClaim, error) {
	result := ComputeClaimProviderClaim{Evidence: &ComputeClaimEvidence{}}
	proof, err := p.ProveComputeClaimRecovery(ctx, allocation, prepared, ownership)
	result.Proof = proof
	if err != nil {
		return result, err
	}
	target := protectedresource.Target{
		PackageID: ownership.PackageID, NodePoolID: ownership.NodePoolID, MachineID: ownership.MachineID,
		NodeName: ownership.NodeName, CVMID: ownership.InstanceID,
	}
	if err := protectedresource.FromEnv().Check(target); err != nil {
		result.Proof.Reason = "identity_mismatch"
		return result, err
	}
	if proof.CVMOwnershipState == "recoverable" {
		nodeRaw, nodeReadErr := p.callKubectl(ctx, []string{"get", "node/" + allocation.NodeName, "-o", "json"}, nil, protectedresource.Target{})
		if nodeReadErr != nil {
			result.Proof.Reason = computeClaimKubectlReason(nodeReadErr)
			result.FailureStage, result.ProviderErrorClass = "node_pre_cvm_read", computeClaimKubectlErrorClass(nodeReadErr)
			return result, computeClaimProviderError(result.Proof.Reason)
		}
		nodeState, nodeOK := computeClaimNodeOwnershipState(nodeRaw, allocation, ownership)
		if !nodeOK {
			result.Proof.Reason = "node_ownership_conflict"
			if nodeState == "identity_mismatch" {
				result.Proof.Reason = "identity_mismatch"
			}
			result.FailureStage, result.ProviderErrorClass = "node_pre_cvm_read", "ownership_conflict"
			return result, computeClaimProviderError(result.Proof.Reason)
		}
		result.Proof.NodeOwnershipState = nodeState
		response, provisionErr := p.provision(ctx, computeClaimProvisionerRequest("claim_compute_machine", allocation, prepared, ownership))
		result.TencentMutationCount = max(0, response.MutationCount)
		if response.MutationEvidence != nil {
			result.Evidence.CVM = cloneComputeClaimMutationEvidence(*response.MutationEvidence)
		}
		if provisionErr != nil {
			result.Proof.Reason = "provider_describe"
			result.FailureStage, result.ProviderErrorClass = "cvm_provisioner_transport", "transport_error"
			if response.MutationEvidence == nil {
				result.TencentMutationCount = 5
				result.Evidence.CVM = ComputeClaimMutationEvidence{
					Attempted: 5,
					Unknown:   5,
					Missing:   []string{"instance_name", "opl_account_id", "opl_workspace_id", "opl_resource_id", "opl_operation_id"},
				}
			}
			return result, computeClaimProviderError(result.Proof.Reason)
		}
		if response.FailureStage != "" || response.ProviderErrorClass != "" {
			result.FailureStage, result.ProviderErrorClass = response.FailureStage, response.ProviderErrorClass
		}
		if !response.OK || response.Status != "claimed" || response.InstanceID != firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID) ||
			response.ProviderData["cvmOwnershipState"] != "target_owned" || !validConfirmedComputeClaimMutation(response.MutationEvidence, response.MutationCount, 5) {
			result.Proof.Reason = safeComputeClaimRecoveryReason(response.ErrorCode, "identity_mismatch")
			if result.FailureStage == "" {
				result.FailureStage, result.ProviderErrorClass = "cvm_mutation_evidence", "evidence_incomplete"
			}
			return result, computeClaimProviderError(result.Proof.Reason)
		}
	}
	if proof.NodeOwnershipState != "target_owned" {
		nodeEvidence, nodeErr := p.convergeComputeClaimNode(ctx, allocation, ownership, target)
		result.KubernetesMutationCount = nodeEvidence.Attempted
		result.Evidence.Node = cloneComputeClaimMutationEvidence(nodeEvidence)
		if nodeErr != nil {
			result.Proof.Reason = safeComputeClaimRecoveryReason(nodeErr.Reason, "provider_describe")
			result.FailureStage, result.ProviderErrorClass = nodeErr.Stage, nodeErr.ProviderClass
			return result, computeClaimProviderError(result.Proof.Reason)
		}
	}
	readback, err := p.ProveComputeClaimRecovery(ctx, allocation, prepared, ownership)
	result.Proof = readback
	if err != nil || readback.CVMOwnershipState != "target_owned" || readback.NodeOwnershipState != "target_owned" {
		if result.Proof.Reason == "" {
			result.Proof.Reason = "identity_mismatch"
		}
		if result.FailureStage == "" {
			result.FailureStage, result.ProviderErrorClass = "claim_final_readback", "readback_mismatch"
		}
		return result, computeClaimProviderError(result.Proof.Reason)
	}
	return result, nil
}

func (p *TencentProvider) ClaimComputeRecoveryNodeOnly(ctx context.Context, allocation ComputeAllocation, prepared ComputeAllocationPreparation, ownership MachineOwnership) (ComputeClaimProviderClaim, error) {
	result := ComputeClaimProviderClaim{Evidence: &ComputeClaimEvidence{}}
	proof, err := p.ProveComputeClaimRecovery(ctx, allocation, prepared, ownership)
	result.Proof = proof
	if err != nil {
		return result, err
	}
	target := protectedresource.Target{
		PackageID: ownership.PackageID, NodePoolID: ownership.NodePoolID, MachineID: ownership.MachineID,
		NodeName: ownership.NodeName, CVMID: ownership.InstanceID,
	}
	if err := protectedresource.FromEnv().Check(target); err != nil {
		result.Proof.Reason = "identity_mismatch"
		return result, err
	}
	if proof.CVMOwnershipState != "recoverable" && proof.CVMOwnershipState != "target_owned" ||
		proof.NodeOwnershipState != "unallocated" && proof.NodeOwnershipState != "target_owned" {
		result.Proof.Reason = "identity_mismatch"
		return result, computeClaimProviderError(result.Proof.Reason)
	}
	initialCVMOwnershipState := proof.CVMOwnershipState
	if proof.NodeOwnershipState != "target_owned" {
		nodeEvidence, nodeErr := p.convergeComputeClaimNode(ctx, allocation, ownership, target)
		result.KubernetesMutationCount = nodeEvidence.Attempted
		result.Evidence.Node = cloneComputeClaimMutationEvidence(nodeEvidence)
		if nodeErr != nil {
			result.Proof.Reason = safeComputeClaimRecoveryReason(nodeErr.Reason, "provider_describe")
			result.FailureStage, result.ProviderErrorClass = nodeErr.Stage, nodeErr.ProviderClass
			return result, computeClaimProviderError(result.Proof.Reason)
		}
	}
	readback, err := p.ProveComputeClaimRecovery(ctx, allocation, prepared, ownership)
	result.Proof = readback
	if err != nil || readback.CVMOwnershipState != initialCVMOwnershipState || readback.NodeOwnershipState != "target_owned" {
		if result.Proof.Reason == "" {
			result.Proof.Reason = "identity_mismatch"
		}
		if result.FailureStage == "" {
			result.FailureStage, result.ProviderErrorClass = "claim_final_readback", "readback_mismatch"
		}
		return result, computeClaimProviderError(result.Proof.Reason)
	}
	return result, nil
}

func cloneComputeClaimMutationEvidence(value ComputeClaimMutationEvidence) ComputeClaimMutationEvidence {
	value.Missing = append([]string(nil), value.Missing...)
	return value
}

func validConfirmedComputeClaimMutation(evidence *ComputeClaimMutationEvidence, count, maximum int) bool {
	return evidence != nil && count >= 0 && count <= maximum && evidence.Attempted == count && evidence.Attempted == evidence.Confirmed &&
		evidence.Unknown == 0 && len(evidence.Missing) == 0
}

type computeClaimNodeConvergenceError struct {
	Reason        string
	Stage         string
	ProviderClass string
}

func (p *TencentProvider) convergeComputeClaimNode(ctx context.Context, allocation ComputeAllocation, ownership MachineOwnership, target protectedresource.Target) (ComputeClaimMutationEvidence, *computeClaimNodeConvergenceError) {
	evidence := ComputeClaimMutationEvidence{}
	nodeRaw, err := p.callKubectl(ctx, []string{"get", "node/" + allocation.NodeName, "-o", "json"}, nil, protectedresource.Target{})
	if err != nil {
		reason := computeClaimKubectlReason(err)
		return evidence, &computeClaimNodeConvergenceError{Reason: reason, Stage: "node_pre_read", ProviderClass: computeClaimKubectlErrorClass(err)}
	}
	nodeState, ok := computeClaimNodeOwnershipState(nodeRaw, allocation, ownership)
	if !ok {
		reason := "node_ownership_conflict"
		if nodeState == "identity_mismatch" {
			reason = "identity_mismatch"
		}
		return evidence, &computeClaimNodeConvergenceError{Reason: reason, Stage: "node_conflict_check", ProviderClass: "ownership_conflict"}
	}
	if nodeState == "target_owned" {
		return evidence, nil
	}
	patch, patchErr := computeClaimNodePatch(nodeRaw, allocation, ownership)
	if patchErr != nil {
		return evidence, &computeClaimNodeConvergenceError{Reason: "node_ownership_conflict", Stage: "node_patch_build", ProviderClass: "ownership_conflict"}
	}
	_, patchErr = p.callKubectl(ctx, []string{"patch", "node/" + allocation.NodeName, "--type=json", "--patch-file=/dev/stdin"}, patch, target)
	if !computeClaimKubectlClientRejectedBeforeAPI(patchErr) {
		evidence.Attempted = 1
	}
	readbackState, readbackOK, readbackErr, readbackClass := p.readNodeOwnershipAfterMutation(ctx, allocation, ownership)
	if readbackOK && readbackState == "target_owned" {
		evidence.Confirmed = evidence.Attempted
		return evidence, nil
	}
	evidence.Missing = []string{"node_ownership"}
	if readbackState == "identity_mismatch" {
		return evidence, &computeClaimNodeConvergenceError{Reason: "identity_mismatch", Stage: "node_final_readback", ProviderClass: "ownership_conflict"}
	}
	if readbackState == "node_ownership_conflict" {
		return evidence, &computeClaimNodeConvergenceError{Reason: "node_ownership_conflict", Stage: "node_final_readback", ProviderClass: "ownership_conflict"}
	}
	if readbackErr != nil {
		evidence.Unknown = 1
		reason := computeClaimKubectlReason(readbackErr)
		providerClass := readbackClass
		if patchErr != nil && reason == "provider_describe" {
			reason = computeClaimKubectlReason(patchErr)
			providerClass = computeClaimKubectlErrorClass(patchErr)
		}
		return evidence, &computeClaimNodeConvergenceError{Reason: reason, Stage: "node_patch_readback", ProviderClass: providerClass}
	}
	if patchErr != nil {
		return evidence, &computeClaimNodeConvergenceError{Reason: computeClaimKubectlReason(patchErr), Stage: "node_patch_readback", ProviderClass: computeClaimKubectlErrorClass(patchErr)}
	}
	return evidence, &computeClaimNodeConvergenceError{Reason: "node_ownership_conflict", Stage: "node_final_readback", ProviderClass: "readback_mismatch"}
}

// readNodeOwnershipAfterMutation performs bounded authoritative reads after a
// patch. It never retries the patch itself: a target-owned readback is the only
// success proof, while an explicit ownership conflict stops immediately.
func (p *TencentProvider) readNodeOwnershipAfterMutation(ctx context.Context, allocation ComputeAllocation, ownership MachineOwnership) (string, bool, error, string) {
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		if attempt > 0 && p.convergenceWait != nil {
			if err := p.convergenceWait(ctx, attempt); err != nil {
				return "unknown", false, err, computeClaimKubectlErrorClass(err)
			}
		}
		raw, err := p.callKubectl(ctx, []string{"get", "node/" + allocation.NodeName, "-o", "json"}, nil, protectedresource.Target{})
		if err != nil {
			lastErr = err
			continue
		}
		state, ok := computeClaimNodeOwnershipState(raw, allocation, ownership)
		if ok && state == "target_owned" {
			return state, true, nil, "readback_mismatch"
		}
		if !ok && (state == "identity_mismatch" || state == "node_ownership_conflict") {
			return state, false, nil, "ownership_conflict"
		}
	}
	if lastErr != nil {
		return "unknown", false, lastErr, computeClaimKubectlErrorClass(lastErr)
	}
	return "unallocated", true, nil, "readback_mismatch"
}

func computeClaimKubectlErrorClass(err error) string {
	if err == nil {
		return "readback_mismatch"
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "timeout"
	}
	if computeClaimKubectlReason(err) == "iam_rbac" {
		return "iam_rbac"
	}
	if computeClaimKubectlReason(err) == "node_ownership_conflict" {
		return "ownership_conflict"
	}
	return "provider_error"
}

func computeClaimKubectlClientRejectedBeforeAPI(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "must specify --patch or --patch-file containing the contents of the patch")
}

func computeClaimProvisionerRequest(action string, allocation ComputeAllocation, prepared ComputeAllocationPreparation, ownership MachineOwnership) provisionerRequest {
	plan := packagePlan(allocation.PackageID)
	return provisionerRequest{
		Action: action, AccountID: allocation.AccountID, PackageID: allocation.PackageID, Zone: allocation.Zone,
		Tags: oplCostTags(allocation.AccountID, allocation.WorkspaceID, allocation.ID, ownership.ID),
		Pool: provisionerPool{
			ID: prepared.PoolID, PackageID: prepared.PackageID, InstanceType: prepared.InstanceType, CPU: uint64(plan.CPU), MemoryGB: uint64(plan.MemoryGB),
			NodePoolID: prepared.NodePoolID, MaxReplicas: prepared.MaxReplicas, BaselineReplicas: prepared.BaselineReplicas,
			TargetReplicas: prepared.TargetReplicas, BeforeMachineNames: append([]string(nil), prepared.BeforeMachineNames...),
		},
		Allocation: provisionerAllocation{
			ID: allocation.ID, InstanceID: firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID), MachineName: allocation.MachineName,
			NodeName: allocation.NodeName, PrivateIP: allocation.PrivateIP, PublicIP: allocation.PublicIP, Deadline: allocation.Deadline,
		},
	}
}

func computeClaimKubectlReason(err error) string {
	if err == nil {
		return "provider_describe"
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "forbidden") || strings.Contains(message, "unauthorized") || strings.Contains(message, "permission") {
		return "iam_rbac"
	}
	if strings.Contains(message, "test failed") || strings.Contains(message, "conflict") || strings.Contains(message, "resourceversion") {
		return "node_ownership_conflict"
	}
	return "provider_describe"
}

func computeClaimNodePatch(raw []byte, allocation ComputeAllocation, ownership MachineOwnership) ([]byte, error) {
	var node struct {
		Metadata struct {
			Name            string            `json:"name"`
			ResourceVersion string            `json:"resourceVersion"`
			Labels          map[string]string `json:"labels"`
		} `json:"metadata"`
		Spec struct {
			Taints []struct {
				Key, Value, Effect string
			} `json:"taints"`
		} `json:"spec"`
	}
	if json.Unmarshal(raw, &node) != nil || node.Metadata.Name != allocation.NodeName || node.Metadata.ResourceVersion == "" {
		return nil, fmt.Errorf("node_identity_mismatch")
	}
	packageTaints := 0
	for _, taint := range node.Spec.Taints {
		if taint.Key == "oplcloud.cn/workspace-id" {
			return nil, fmt.Errorf("node_ownership_conflict")
		}
		if taint.Key == "oplcloud.cn/package-id" {
			packageTaints++
			if taint.Value != allocation.PackageID || taint.Effect != "NoSchedule" {
				return nil, fmt.Errorf("node_ownership_conflict")
			}
		}
	}
	if packageTaints != 1 {
		return nil, fmt.Errorf("node_ownership_conflict")
	}
	expected := []struct{ key, value string }{
		{key: "medopl.cn/workload", value: "workspace"},
		{key: "oplcloud.cn/resource-id", value: ownership.ResourceID},
		{key: "oplcloud.cn/account-id", value: ownership.AccountID},
		{key: "oplcloud.cn/workspace-id", value: ownership.WorkspaceID},
	}
	for _, label := range expected {
		if _, present := node.Metadata.Labels[label.key]; present {
			return nil, fmt.Errorf("node_ownership_conflict")
		}
	}
	patch := []map[string]any{{"op": "test", "path": "/metadata/resourceVersion", "value": node.Metadata.ResourceVersion}}
	if node.Metadata.Labels == nil {
		patch = append(patch, map[string]any{"op": "add", "path": "/metadata/labels", "value": map[string]string{}})
	}
	for _, label := range expected {
		patch = append(patch, map[string]any{"op": "add", "path": "/metadata/labels/" + strings.ReplaceAll(label.key, "/", "~1"), "value": label.value})
	}
	return json.Marshal(patch)
}

func computeClaimProviderError(reason string) error {
	return fmt.Errorf("compute_claim_recovery_%s", safeComputeClaimRecoveryReason(reason, "provider_describe"))
}

func computeClaimNodeOwnershipState(raw []byte, allocation ComputeAllocation, ownership MachineOwnership) (string, bool) {
	var node struct {
		Metadata struct {
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
		Spec struct {
			Taints []struct {
				Key, Value, Effect string
			} `json:"taints"`
		} `json:"spec"`
		Status struct {
			Addresses []struct {
				Type, Address string
			} `json:"addresses"`
		} `json:"status"`
	}
	if json.Unmarshal(raw, &node) != nil || node.Metadata.Name != allocation.NodeName {
		return "identity_mismatch", false
	}
	internalIPCount := 0
	for _, address := range node.Status.Addresses {
		if address.Type == "InternalIP" && address.Address == allocation.PrivateIP {
			internalIPCount++
		}
	}
	if internalIPCount != 1 {
		return "identity_mismatch", false
	}
	packageTaintCount := 0
	for _, taint := range node.Spec.Taints {
		if taint.Key == "oplcloud.cn/workspace-id" {
			return "node_ownership_conflict", false
		}
		if taint.Key == "oplcloud.cn/package-id" {
			packageTaintCount++
			if taint.Value != allocation.PackageID || taint.Effect != "NoSchedule" {
				return "node_ownership_conflict", false
			}
		}
	}
	if packageTaintCount != 1 || allocation.PackageID != ownership.PackageID {
		return "node_ownership_conflict", false
	}
	expected := map[string]string{
		"medopl.cn/workload": "workspace", "oplcloud.cn/resource-id": ownership.ResourceID,
		"oplcloud.cn/account-id": ownership.AccountID, "oplcloud.cn/workspace-id": ownership.WorkspaceID,
	}
	present := 0
	for key, value := range expected {
		actual, exists := node.Metadata.Labels[key]
		if !exists {
			continue
		}
		present++
		if actual != value {
			return "node_ownership_conflict", false
		}
	}
	if present == len(expected) {
		return "target_owned", true
	}
	if present == 0 {
		return "unallocated", true
	}
	return "node_ownership_conflict", false
}

func (p *TencentProvider) TagComputeMachine(ctx context.Context, machine ProviderMachine, ownership MachineOwnership) error {
	if err := p.TagComputeMachineCVM(ctx, machine, ownership); err != nil {
		return err
	}
	return p.ClaimComputeNode(ctx, ComputeAllocation{
		ID: ownership.ResourceID, AccountID: ownership.AccountID, WorkspaceID: ownership.WorkspaceID,
		PackageID: ownership.PackageID, NodePoolID: ownership.NodePoolID, MachineName: machine.MachineID,
		InstanceID: machine.InstanceID, CVMInstanceID: machine.InstanceID, NodeName: machine.NodeName, PrivateIP: machine.PrivateIP,
	}, ownership)
}

func (p *TencentProvider) TagComputeMachineCVM(ctx context.Context, machine ProviderMachine, ownership MachineOwnership) error {
	if machine.InstanceID == "" || machine.NodeName == "" {
		return fmt.Errorf("compute_machine_identity_required")
	}
	if err := protectedresource.FromEnv().Check(protectedresource.Target{
		PackageID: ownership.PackageID, NodePoolID: ownership.NodePoolID,
		MachineID: machine.MachineID, NodeName: machine.NodeName, CVMID: machine.InstanceID,
	}); err != nil {
		return err
	}
	response, err := p.provision(ctx, provisionerRequest{
		Action:    "tag_compute_machine",
		PackageID: ownership.PackageID,
		Tags:      oplCostTags(ownership.AccountID, ownership.WorkspaceID, ownership.ResourceID, ownership.ID),
		Pool:      provisionerPool{NodePoolID: ownership.NodePoolID},
		Allocation: provisionerAllocation{
			ID: ownership.ResourceID, InstanceID: machine.InstanceID, MachineName: machine.MachineID, NodeName: machine.NodeName, PrivateIP: machine.PrivateIP,
		},
	})
	if err != nil {
		return err
	}
	if !response.OK || !validConfirmedComputeClaimMutation(response.MutationEvidence, response.MutationCount, 5) {
		return provisionerError(response)
	}
	return nil
}

func (p *TencentProvider) ClaimComputeNode(ctx context.Context, allocation ComputeAllocation, ownership MachineOwnership) error {
	target := protectedresource.Target{PackageID: ownership.PackageID, NodePoolID: ownership.NodePoolID, MachineID: allocation.MachineName, NodeName: allocation.NodeName, CVMID: firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID)}
	_, nodeErr := p.convergeComputeClaimNode(ctx, allocation, ownership, target)
	if nodeErr != nil {
		return fmt.Errorf("compute_machine_node_claim_%s", safeComputeClaimRecoveryReason(nodeErr.Reason, "provider_describe"))
	}
	return nil
}

func (p *TencentProvider) SyncComputeAllocation(ctx context.Context, allocation ComputeAllocation) (ComputeAllocation, error) {
	if allocation.ID == "" {
		return ComputeAllocation{}, fmt.Errorf("compute_allocation_id_required")
	}
	plan, err := configuredPackagePlan(firstNonEmpty(allocation.PackageID, "basic"))
	if err != nil {
		return allocation, err
	}
	response, err := p.provision(ctx, provisionerRequest{
		Action:    "sync_compute_allocation",
		AccountID: allocation.AccountID,
		PackageID: allocation.PackageID,
		Zone:      allocation.ProviderData["zone"],
		Tags:      allocation.CostTags,
		Pool: provisionerPool{
			ID: allocation.PoolID, PackageID: allocation.PackageID, NodePoolID: allocation.NodePoolID,
			InstanceType: plan.InstanceType, CPU: uint64(plan.CPU), MemoryGB: uint64(plan.MemoryGB),
		},
		Allocation: provisionerAllocation{
			ID:          allocation.ID,
			InstanceID:  firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID),
			MachineName: firstNonEmpty(allocation.MachineName, allocation.ProviderData["machineName"], allocation.NodeName),
			NodeName:    allocation.NodeName,
			PrivateIP:   allocation.PrivateIP,
			PublicIP:    allocation.PublicIP,
		},
	})
	if err != nil {
		return allocation, err
	}
	allocation.Status = firstNonEmpty(response.Status, allocation.Status)
	allocation.Provider = firstNonEmpty(allocation.Provider, "tencent-tke")
	allocation.ProviderRequestID = firstNonEmpty(response.ProviderRequestID, allocation.ProviderRequestID)
	allocation.NodePoolID = firstNonEmpty(response.NodePoolID, allocation.NodePoolID)
	allocation.InstanceID = firstNonEmpty(response.InstanceID, allocation.InstanceID)
	allocation.CVMInstanceID = firstNonEmpty(response.InstanceID, allocation.CVMInstanceID)
	allocation.NodeName = firstNonEmpty(response.NodeName, allocation.NodeName)
	allocation.PrivateIP = firstNonEmpty(response.PrivateIP, allocation.PrivateIP)
	allocation.PublicIP = firstNonEmpty(response.PublicIP, allocation.PublicIP)
	allocation.CVMStatus = firstNonEmpty(response.CVMStatus, allocation.CVMStatus)
	if allocation.ProviderData == nil {
		allocation.ProviderData = map[string]string{}
	}
	for key, value := range response.ProviderData {
		allocation.ProviderData[key] = value
	}
	allocation.ProviderData["instanceType"] = firstNonEmpty(response.InstanceType, allocation.ProviderData["instanceType"])
	allocation.ChargeType = firstNonEmpty(response.ProviderData["chargeType"], allocation.ChargeType)
	allocation.RenewFlag = firstNonEmpty(response.ProviderData["renewFlag"], allocation.RenewFlag)
	allocation.Deadline = firstNonEmpty(response.ProviderData["deadline"], allocation.Deadline)
	allocation.NodeSelector = tkeNodeSelector(allocation.ProviderData, allocation.NodeName)
	if !response.OK {
		return allocation, provisionerError(response)
	}
	if response.InstanceType != plan.InstanceType || response.ProviderData["instanceType"] != plan.InstanceType {
		return allocation, fmt.Errorf("compute_instance_type_mismatch")
	}
	if response.ProviderData["cpu"] != strconv.Itoa(plan.CPU) || response.ProviderData["memoryGb"] != strconv.Itoa(plan.MemoryGB) {
		return allocation, fmt.Errorf("compute_resource_shape_mismatch")
	}
	return allocation, nil
}

func (p *TencentProvider) ReadComputeAllocation(ctx context.Context, allocation ComputeAllocation) (ComputeAllocation, error) {
	return p.SyncComputeAllocation(ctx, allocation)
}

func (p *TencentProvider) RenewComputeAllocation(ctx context.Context, allocation ComputeAllocation) (ComputeAllocation, error) {
	if !validComputeRenewalIdentity(allocation) {
		return ComputeAllocation{}, fmt.Errorf("compute_allocation_renew_identity_required")
	}
	expectedInstanceID := firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID)
	expectedInstanceType := allocation.ProviderData["instanceType"]
	expectedZone := allocation.ProviderData["zone"]
	expectedTags := allocation.CostTags
	response, err := p.provision(ctx, provisionerRequest{
		Action: "renew_compute_allocation", AccountID: allocation.AccountID, Zone: allocation.ProviderData["zone"], Tags: allocation.CostTags,
		Pool:       provisionerPool{InstanceType: allocation.ProviderData["instanceType"]},
		Allocation: provisionerAllocation{ID: allocation.ID, InstanceID: expectedInstanceID, PrivateIP: allocation.PrivateIP, Deadline: allocation.Deadline},
	})
	if err != nil {
		return ComputeAllocation{}, err
	}
	allocation.ProviderRequestID = firstNonEmpty(response.ProviderRequestID, allocation.ProviderRequestID)
	allocation.InstanceID = firstNonEmpty(response.InstanceID, allocation.InstanceID)
	allocation.CVMInstanceID = firstNonEmpty(response.InstanceID, allocation.CVMInstanceID)
	allocation.CVMStatus = response.CVMStatus
	if response.Status == "external_deleted" {
		allocation.Status = "external_deleted"
	}
	if allocation.ProviderData == nil {
		allocation.ProviderData = map[string]string{}
	}
	for key, value := range response.ProviderData {
		allocation.ProviderData[key] = value
	}
	allocation.ChargeType = firstNonEmpty(response.ProviderData["chargeType"], allocation.ChargeType)
	allocation.RenewFlag = firstNonEmpty(response.ProviderData["renewFlag"], allocation.RenewFlag)
	allocation.Deadline = firstNonEmpty(response.ProviderData["deadline"], allocation.Deadline)
	if !response.OK {
		return allocation, provisionerError(response)
	}
	if response.InstanceID != expectedInstanceID || response.ProviderData["instanceType"] != expectedInstanceType || response.ProviderData["zone"] != expectedZone {
		return allocation, fmt.Errorf("compute_renewal_readback_mismatch")
	}
	for _, key := range []string{"opl_account_id", "opl_workspace_id", "opl_resource_id", "opl_operation_id"} {
		if response.ProviderData[key] != expectedTags[key] {
			return allocation, fmt.Errorf("compute_renewal_readback_mismatch")
		}
	}
	return allocation, nil
}

func (p *TencentProvider) DestroyComputeAllocation(ctx context.Context, allocation ComputeAllocation) (ComputeAllocation, error) {
	if allocation.ID == "" {
		return ComputeAllocation{}, fmt.Errorf("compute_allocation_id_required")
	}
	externallyDeleted := isExternallyDeletedComputeStatus(allocation.Status)
	if !externallyDeleted && firstNonEmpty(allocation.MachineName, allocation.ProviderData["machineName"]) == "" && allocation.NodeName == "" && firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID) == "" {
		allocation.Status = "destroyed"
		allocation.Provider = "tencent-tke"
		return allocation, nil
	}
	response := provisionerResponse{}
	if !externallyDeleted {
		var err error
		response, err = p.provision(ctx, provisionerRequest{
			Action:    "destroy_compute_allocation",
			AccountID: allocation.AccountID,
			PackageID: allocation.PackageID,
			Pool:      provisionerPool{ID: allocation.PoolID, NodePoolID: allocation.NodePoolID},
			Allocation: provisionerAllocation{
				ID:          allocation.ID,
				InstanceID:  firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID),
				MachineName: firstNonEmpty(allocation.MachineName, allocation.ProviderData["machineName"], allocation.NodeName),
				NodeName:    allocation.NodeName,
				PrivateIP:   allocation.PrivateIP,
			},
		})
		if err != nil {
			return ComputeAllocation{}, err
		}
		if !response.OK {
			return ComputeAllocation{}, provisionerError(response)
		}
	}
	serviceName := allocation.ServiceName
	if serviceName == "" && (externallyDeleted || allocation.Status == "running" || allocation.Status == "ready" || allocation.Status == "active" || allocation.Status == "destroying") {
		serviceName = k8sName(allocation.ID)
	}
	if serviceName != "" {
		if _, err := p.callKubectl(ctx, []string{"delete", "deployment/" + serviceName, "service/" + serviceName, "secret/" + serviceName + "-env", "--ignore-not-found=true", "--wait=true"}, nil, protectedresource.Target{PackageID: allocation.PackageID, NodePoolID: allocation.NodePoolID, MachineID: allocation.MachineName, NodeName: allocation.NodeName, CVMID: firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID)}); err != nil {
			return ComputeAllocation{}, err
		}
		allocation.ServiceName = serviceName
	}
	allocation.Status = "destroyed"
	allocation.ProviderRequestID = firstNonEmpty(response.ProviderRequestID, allocation.ProviderRequestID)
	if allocation.Provider == "" {
		allocation.Provider = "tencent-tke"
	}
	return allocation, nil
}

func (p *TencentProvider) CreateStorageVolume(ctx context.Context, input StorageVolumeInput) (StorageVolume, error) {
	now := time.Now().UTC()
	id := firstNonEmpty(input.ID, fabricID("vol", input.WorkspaceID, now))
	name := k8sName(id)
	tags := oplCostTags(input.AccountID, input.WorkspaceID, id, input.OperationID)
	diskType := firstNonEmpty(os.Getenv("TENCENT_CBS_DISK_TYPE"), "CLOUD_BSSD")
	volume := StorageVolume{
		ID: id, OperationID: input.IdempotencyKey, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "pending", Provider: "tencent-tke",
		SizeGB: input.SizeGB, DiskType: diskType, Zone: input.Zone, CostTags: tags, CreatedAt: now,
		ProviderData: map[string]string{"pvName": name + "-pv", "pvcName": name + "-data"},
	}
	response, err := p.provision(ctx, provisionerRequest{
		Action: "create_storage_volume", AccountID: input.AccountID, Tags: tags,
		Storage: provisionerStorage{
			ID: id, SizeGB: uint64(input.SizeGB), Zone: input.Zone, DiskType: diskType,
			ExpectedState: input.ExpectedRecoveryState, ExpectedProviderResourceID: input.ExpectedProviderResourceID,
			AllowExistingExactReplay: input.AllowExistingExactReplay,
		},
	})
	if err != nil {
		return volume, err
	}
	volume.ProviderRequestID = response.ProviderRequestID
	if strings.HasPrefix(response.StorageVolumeID, "disk-") {
		volume.ProviderResourceID = response.StorageVolumeID
	}
	applyStorageReadback(&volume, response)
	if !response.OK {
		return volume, provisionerError(response)
	}
	if volume.ProviderResourceID == "" {
		return volume, fmt.Errorf("storage_cbs_identity_required")
	}
	if _, err := p.callKubectl(ctx, []string{"apply", "-f", "-"}, staticCBSManifest(volume), protectedresource.Target{}); err != nil {
		return volume, err
	}
	return volume, nil
}

// CreateCBSVolume is the first normal-launch storage stage. It deliberately
// stops after the provider has returned a disk identity; Kubernetes binding is
// handled by ApplyStaticStorageBinding so a lost response can be recovered by
// a Describe-only readback without reapplying either side.
func (p *TencentProvider) CreateCBSVolume(ctx context.Context, input StorageVolumeInput) (StorageVolume, error) {
	now := time.Now().UTC()
	id := firstNonEmpty(input.ID, fabricID("vol", input.WorkspaceID, now))
	diskType := firstNonEmpty(os.Getenv("TENCENT_CBS_DISK_TYPE"), "CLOUD_BSSD")
	tags := oplCostTags(input.AccountID, input.WorkspaceID, id, input.OperationID)
	volume := StorageVolume{
		ID: id, OperationID: input.IdempotencyKey, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "pending", Provider: "tencent-tke",
		SizeGB: input.SizeGB, DiskType: diskType, Zone: input.Zone, CostTags: tags, CreatedAt: now,
		ProviderData: map[string]string{"pvName": k8sName(id) + "-pv", "pvcName": k8sName(id) + "-data"},
	}
	response, err := p.provision(ctx, provisionerRequest{
		Action: "create_storage_volume", AccountID: input.AccountID, Tags: tags,
		Storage: provisionerStorage{
			ID: id, SizeGB: uint64(input.SizeGB), Zone: input.Zone, DiskType: diskType,
			ExpectedState: input.ExpectedRecoveryState, ExpectedProviderResourceID: input.ExpectedProviderResourceID,
			AllowExistingExactReplay: input.AllowExistingExactReplay,
		},
	})
	if err != nil {
		return volume, err
	}
	volume.ProviderRequestID = response.ProviderRequestID
	if strings.HasPrefix(response.StorageVolumeID, "disk-") {
		volume.ProviderResourceID = response.StorageVolumeID
	}
	applyStorageReadback(&volume, response)
	if !response.OK {
		return volume, provisionerError(response)
	}
	if volume.ProviderResourceID == "" {
		return volume, fmt.Errorf("storage_cbs_identity_required")
	}
	return volume, nil
}

// ReadCBSVolume is intentionally Describe-only. The persisted disk identity
// is authoritative; a response naming another disk is an identity failure.
func (p *TencentProvider) ReadCBSVolume(ctx context.Context, input StorageVolumeInput, persisted StorageVolume) (StorageVolume, error) {
	if persisted.ID == "" {
		persisted.ID = input.ID
	}
	if persisted.AccountID == "" {
		persisted.AccountID = input.AccountID
	}
	if persisted.WorkspaceID == "" {
		persisted.WorkspaceID = input.WorkspaceID
	}
	if persisted.SizeGB == 0 {
		persisted.SizeGB = input.SizeGB
	}
	if persisted.Zone == "" {
		persisted.Zone = input.Zone
	}
	if persisted.Provider == "" {
		persisted.Provider = "tencent-tke"
	}
	if len(persisted.CostTags) == 0 {
		persisted.CostTags = oplCostTags(input.AccountID, input.WorkspaceID, input.ID, input.OperationID)
	}
	if persisted.ID == "" || persisted.AccountID != input.AccountID || persisted.WorkspaceID != input.WorkspaceID || persisted.SizeGB != input.SizeGB || persisted.Zone != input.Zone {
		return persisted, fmt.Errorf("storage_cbs_readback_identity_required")
	}
	if !strings.HasPrefix(persisted.ProviderResourceID, "disk-") {
		discovery, discoverErr := p.DiscoverStorageRecovery(ctx, input)
		if discoverErr != nil || discovery.State != "storage_existing_exact" || !strings.HasPrefix(discovery.ProviderResourceID, "disk-") {
			if discoverErr != nil {
				return persisted, discoverErr
			}
			return persisted, fmt.Errorf("storage_cbs_readback_identity_required")
		}
		persisted.ProviderResourceID = discovery.ProviderResourceID
		persisted.ProviderRequestID = firstNonEmpty(discovery.ProviderRequestID, persisted.ProviderRequestID)
	}
	readback, err := p.ReadStorageVolume(ctx, persisted)
	if err != nil {
		return readback, err
	}
	if readback.ProviderResourceID != persisted.ProviderResourceID ||
		readback.ID != persisted.ID || readback.AccountID != persisted.AccountID || readback.WorkspaceID != persisted.WorkspaceID ||
		readback.SizeGB != persisted.SizeGB || readback.Zone != persisted.Zone ||
		(persisted.DiskType != "" && readback.DiskType != persisted.DiskType) ||
		(persisted.RenewFlag != "" && readback.RenewFlag != persisted.RenewFlag) ||
		(persisted.Deadline != "" && readback.Deadline != persisted.Deadline) ||
		(readback.ProviderData["zone"] != "" && readback.ProviderData["zone"] != persisted.Zone) {
		return readback, fmt.Errorf("storage_cbs_readback_identity_mismatch")
	}
	return readback, nil
}

// ApplyStaticStorageBinding is the sole Kubernetes write in the staged
// storage path. It always follows the apply with the same strict GET proof.
func (p *TencentProvider) ApplyStaticStorageBinding(ctx context.Context, volume StorageVolume) (StorageVolume, error) {
	if err := validateStaticStorageBindingInput(volume); err != nil {
		return volume, err
	}
	if _, err := p.callKubectl(ctx, []string{"apply", "-f", "-"}, staticCBSManifest(volume), protectedresource.Target{}); err != nil {
		return volume, err
	}
	return p.ReadStaticStorageBinding(ctx, volume)
}

// ReadStaticStorageBinding performs only Kubernetes GETs and verifies the
// original PV/PVC/CBS identity. Missing, duplicate, or drifted objects fail
// closed; a not-yet-Bound PVC is returned as pending for later readback.
func (p *TencentProvider) ReadStaticStorageBinding(ctx context.Context, volume StorageVolume) (StorageVolume, error) {
	if err := validateStaticStorageBindingInput(volume); err != nil {
		return volume, err
	}
	pvName, pvcName := storageBindingNames(volume)
	raw, err := p.callKubectl(ctx, []string{"get", "pv/" + pvName, "pvc/" + pvcName, "--ignore-not-found", "-o", "json"}, nil, protectedresource.Target{})
	if err != nil {
		return volume, err
	}
	items, err := strictKubectlItems(raw)
	if err != nil {
		return volume, err
	}
	var pv, pvc map[string]any
	pvMatches, pvcMatches := 0, 0
	for _, item := range items {
		resource, ok := item.(map[string]any)
		if !ok {
			return volume, fmt.Errorf("storage_static_binding_response_invalid")
		}
		switch {
		case stringValue(resource["kind"]) == "PersistentVolume" && stringValue(nested(resource, "metadata", "name")) == pvName:
			pv, pvMatches = resource, pvMatches+1
		case stringValue(resource["kind"]) == "PersistentVolumeClaim" && stringValue(nested(resource, "metadata", "name")) == pvcName:
			pvc, pvcMatches = resource, pvcMatches+1
		}
	}
	if pvMatches != 1 || pvcMatches != 1 {
		return volume, fmt.Errorf("storage_static_binding_unverified")
	}
	expectedTags := volume.CostTags
	if len(expectedTags) == 0 {
		expectedTags = oplCostTags(volume.AccountID, volume.WorkspaceID, volume.ID, volume.OperationID)
	}
	for _, resource := range []map[string]any{pv, pvc} {
		for key, expected := range k8sCostLabels(expectedTags) {
			if expected != "" && stringValue(nested(resource, "metadata", "labels", key)) != expected {
				return volume, fmt.Errorf("storage_static_binding_identity_mismatch")
			}
		}
	}
	pvSpec, _ := pv["spec"].(map[string]any)
	pvcSpec, _ := pvc["spec"].(map[string]any)
	expectedCapacity := fmt.Sprintf("%dGi", volume.SizeGB)
	expectedNodeAffinity := map[string]any{"required": map[string]any{"nodeSelectorTerms": []any{map[string]any{"matchExpressions": []any{map[string]any{"key": "topology.kubernetes.io/zone", "operator": "In", "values": []any{volume.Zone}}}}}}}
	pvAccessModes, _ := pvSpec["accessModes"].([]any)
	pvcAccessModes, _ := pvcSpec["accessModes"].([]any)
	if stringValue(nested(pv, "spec", "csi", "driver")) != "com.tencent.cloud.csi.cbs" ||
		stringValue(nested(pv, "spec", "csi", "volumeHandle")) != volume.ProviderResourceID ||
		stringValue(pvSpec["persistentVolumeReclaimPolicy"]) != "Retain" || stringValue(pvSpec["storageClassName"]) != "" ||
		stringValue(pvcSpec["storageClassName"]) != "" || stringValue(pvcSpec["volumeName"]) != pvName ||
		len(pvAccessModes) != 1 || stringValue(pvAccessModes[0]) != "ReadWriteOnce" || len(pvcAccessModes) != 1 || stringValue(pvcAccessModes[0]) != "ReadWriteOnce" ||
		stringValue(nested(pv, "spec", "capacity", "storage")) != expectedCapacity || stringValue(nested(pvc, "spec", "resources", "requests", "storage")) != expectedCapacity ||
		!reflect.DeepEqual(pvSpec["nodeAffinity"], expectedNodeAffinity) {
		return volume, fmt.Errorf("storage_static_binding_identity_mismatch")
	}
	volume.ProviderData = firstStringMap(volume.ProviderData, map[string]string{})
	volume.ProviderData["pvName"], volume.ProviderData["pvcName"] = pvName, pvcName
	if stringValue(nested(pvc, "status", "phase")) == "Bound" {
		volume.Status = "ready"
	} else {
		volume.Status = "pending"
	}
	return volume, nil
}

func validateStaticStorageBindingInput(volume StorageVolume) error {
	if volume.ID == "" || volume.AccountID == "" || volume.WorkspaceID == "" || !strings.HasPrefix(volume.ProviderResourceID, "disk-") ||
		volume.SizeGB <= 0 || strings.TrimSpace(volume.Zone) == "" {
		return fmt.Errorf("storage_static_binding_identity_required")
	}
	pvName, pvcName := storageBindingNames(volume)
	if pvName == "" || pvcName == "" {
		return fmt.Errorf("storage_static_binding_names_required")
	}
	return nil
}

func (p *TencentProvider) DiscoverStorageRecovery(ctx context.Context, input StorageVolumeInput) (StorageRecoveryDiscovery, error) {
	discovery := StorageRecoveryDiscovery{State: "unknown"}
	if input.ID == "" || input.AccountID == "" || input.WorkspaceID == "" || input.OperationID == "" || input.Zone == "" || input.SizeGB <= 0 {
		discovery.Reason = "identity_mismatch"
		return discovery, fmt.Errorf("storage_recovery_identity_mismatch")
	}
	diskType := firstNonEmpty(os.Getenv("TENCENT_CBS_DISK_TYPE"), "CLOUD_BSSD")
	response, err := p.provision(ctx, provisionerRequest{
		Action: "discover_storage_volume", AccountID: input.AccountID,
		Tags:    oplCostTags(input.AccountID, input.WorkspaceID, input.ID, input.OperationID),
		Storage: provisionerStorage{ID: input.ID, SizeGB: uint64(input.SizeGB), Zone: input.Zone, DiskType: diskType},
	})
	discovery.ProviderRequestID = response.ProviderRequestID
	discovery.MutationCount = response.MutationCount
	if err != nil {
		discovery.Reason = "provider_describe"
		return discovery, fmt.Errorf("storage_recovery_provider_describe")
	}
	if response.MutationCount != 0 {
		discovery.Reason = "identity_mismatch"
		return discovery, fmt.Errorf("storage_recovery_identity_mismatch")
	}
	if !response.OK {
		discovery.Reason = storageRecoveryReason(response.ErrorCode)
		return discovery, fmt.Errorf("storage_recovery_%s", discovery.Reason)
	}
	discovery.State = response.StorageState
	discovery.ProviderResourceID = response.StorageVolumeID
	switch discovery.State {
	case "storage_not_started":
		if discovery.ProviderResourceID != "" {
			discovery.State, discovery.Reason = "unknown", "identity_mismatch"
			return discovery, fmt.Errorf("storage_recovery_identity_mismatch")
		}
	case "storage_existing_exact":
		if !strings.HasPrefix(discovery.ProviderResourceID, "disk-") {
			discovery.State, discovery.Reason = "unknown", "identity_mismatch"
			return discovery, fmt.Errorf("storage_recovery_identity_mismatch")
		}
	default:
		discovery.State, discovery.Reason = "unknown", "identity_mismatch"
		return discovery, fmt.Errorf("storage_recovery_identity_mismatch")
	}
	return discovery, nil
}

func storageRecoveryReason(errorCode string) string {
	switch errorCode {
	case "tencent_cbs_multiple_candidate":
		return "multiple_candidate"
	case "tencent_cbs_identity_mismatch", "tencent_cbs_input_invalid", "tencent_cbs_approval_drift", "tencent_cbs_approval_invalid":
		return "identity_mismatch"
	default:
		return "provider_describe"
	}
}

func (p *TencentProvider) SyncStorageVolume(ctx context.Context, volume StorageVolume) (StorageVolume, error) {
	return p.ReadStorageVolumeStatus(ctx, volume)
}

func (p *TencentProvider) ReadStorageVolumeStatus(ctx context.Context, volume StorageVolume) (StorageVolume, error) {
	volume, err := p.ReadStorageVolume(ctx, volume)
	if err != nil || volume.Status == "external_deleted" || volume.Status == "pending" {
		return volume, err
	}
	pvc := storagePVCName(volume)
	raw, err := p.callKubectl(ctx, []string{"get", "pvc/" + pvc, "-o", "json"}, nil, protectedresource.Target{})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "notfound") || strings.Contains(strings.ToLower(err.Error()), "not found") {
			volume.Status = "pending"
			return volume, nil
		}
		return volume, err
	}
	pvcResource := findK8s(kubectlItems(raw), "PersistentVolumeClaim", pvc)
	if pvcResource != nil && stringValue(nested(pvcResource, "status", "phase")) == "Bound" {
		volume.Status = "ready"
	} else {
		volume.Status = "pending"
	}
	return volume, nil
}

func (p *TencentProvider) ReadStorageVolume(ctx context.Context, volume StorageVolume) (StorageVolume, error) {
	if volume.ID == "" || !strings.HasPrefix(volume.ProviderResourceID, "disk-") {
		return StorageVolume{}, fmt.Errorf("storage_volume_cbs_identity_required")
	}
	response, err := p.provision(ctx, provisionerRequest{Action: "sync_storage_volume", AccountID: volume.AccountID, Tags: volume.CostTags, Storage: provisionerStorage{
		ID: volume.ProviderResourceID, SizeGB: uint64(volume.SizeGB), Zone: volume.Zone, DiskType: volume.DiskType, Deadline: volume.Deadline,
	}})
	if err != nil {
		return volume, err
	}
	if strings.HasPrefix(response.StorageVolumeID, "disk-") {
		volume.ProviderResourceID = response.StorageVolumeID
	}
	applyStorageReadback(&volume, response)
	if !response.OK {
		return volume, provisionerError(response)
	}
	if response.Status == "external_deleted" {
		volume.Status = "external_deleted"
		return volume, nil
	}
	if !isCBSProviderReady(volume.CBSStatus) {
		volume.Status = "pending"
		return volume, nil
	}
	volume.Status = "ready"
	if volume.Provider == "" {
		volume.Provider = "tencent-tke"
	}
	return volume, nil
}

func (p *TencentProvider) DestroyStorageVolume(ctx context.Context, volume StorageVolume) (StorageVolume, error) {
	if volume.ID == "" {
		return StorageVolume{}, fmt.Errorf("storage_volume_id_required")
	}
	pv, pvc := storageBindingNames(volume)
	resources := []string{}
	if pvc != "" {
		resources = append(resources, "pvc/"+pvc)
	}
	if pv != "" {
		resources = append(resources, "pv/"+pv)
	}
	if len(resources) > 0 {
		if _, err := p.callKubectl(ctx, append([]string{"delete"}, append(resources, "--ignore-not-found=true", "--wait=true")...), nil, protectedresource.Target{}); err != nil {
			return StorageVolume{}, err
		}
	}
	volume.Status = "released"
	if strings.HasPrefix(volume.ProviderResourceID, "disk-") {
		volume.Status = "retained"
	}
	volume.ProviderRequestID = providerRequestID("storage-destroy", volume.ID)
	if volume.Provider == "" {
		volume.Provider = "tencent-tke"
	}
	return volume, nil
}

func (p *TencentProvider) RenewStorageVolume(ctx context.Context, volume StorageVolume) (StorageVolume, error) {
	if volume.ID == "" || !strings.HasPrefix(volume.ProviderResourceID, "disk-") || strings.TrimSpace(volume.Deadline) == "" {
		return StorageVolume{}, fmt.Errorf("storage_volume_renew_identity_required")
	}
	response, err := p.provision(ctx, provisionerRequest{Action: "renew_storage_volume", AccountID: volume.AccountID, Tags: volume.CostTags, Storage: provisionerStorage{
		ID: volume.ProviderResourceID, SizeGB: uint64(volume.SizeGB), Zone: volume.Zone, DiskType: volume.DiskType, Deadline: volume.Deadline,
	}})
	if err != nil {
		return StorageVolume{}, err
	}
	if response.StorageVolumeID != "" {
		volume.ProviderResourceID = response.StorageVolumeID
	}
	applyStorageReadback(&volume, response)
	if !response.OK {
		return volume, provisionerError(response)
	}
	return volume, nil
}

func applyStorageReadback(volume *StorageVolume, response provisionerResponse) {
	volume.ProviderRequestID = firstNonEmpty(response.ProviderRequestID, volume.ProviderRequestID)
	volume.CBSStatus = response.CBSStatus
	if volume.ProviderData == nil {
		volume.ProviderData = map[string]string{}
	}
	for key, value := range response.ProviderData {
		volume.ProviderData[key] = value
	}
	volume.DiskType = firstNonEmpty(response.ProviderData["diskType"], volume.DiskType)
	volume.RenewFlag = firstNonEmpty(response.ProviderData["renewFlag"], volume.RenewFlag)
	volume.Deadline = firstNonEmpty(response.ProviderData["deadline"], volume.Deadline)
	volume.Zone = firstNonEmpty(response.ProviderData["zone"], volume.Zone)
}

func isCBSProviderReady(status string) bool {
	return status == "UNATTACHED" || status == "ATTACHED"
}

func (p *TencentProvider) CreateStorageSnapshot(ctx context.Context, input StorageSnapshotInput, volume StorageVolume) (StorageSnapshot, error) {
	pvcName := storagePVCName(volume)
	if volume.ID == "" || pvcName == "" {
		return StorageSnapshot{}, fmt.Errorf("storage_volume_provider_ref_required")
	}
	now := time.Now().UTC()
	id := "snap-" + stableSuffix(input.WorkspaceID, input.VolumeID, input.IdempotencyKey)[:16]
	name := k8sName(id)
	snapshotClass := os.Getenv("OPL_WORKSPACE_VOLUME_SNAPSHOT_CLASS")
	if snapshotClass == "" {
		return StorageSnapshot{}, fmt.Errorf("storage_snapshot_class_required")
	}
	if _, err := p.callKubectl(ctx, []string{"apply", "-f", "-"}, volumeSnapshotManifest(name, pvcName, snapshotClass, input), protectedresource.Target{}); err != nil {
		return StorageSnapshot{}, err
	}
	if _, err := p.callKubectl(ctx, []string{"wait", "--for=jsonpath={.status.readyToUse}=true", "volumesnapshot/" + name, "--timeout=300s"}, nil, protectedresource.Target{}); err != nil {
		return StorageSnapshot{}, err
	}
	return StorageSnapshot{ID: id, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, VolumeID: input.VolumeID, Status: "ready", Provider: "tencent-tke", ProviderSnapshotRef: "volumesnapshot/" + name, ProviderRequestID: providerRequestID("snapshot", input.IdempotencyKey), SnapshotClass: snapshotClass, SizeGB: volume.SizeGB, CreatedAt: now}, nil
}

func (p *TencentProvider) SyncStorageSnapshot(ctx context.Context, snapshot StorageSnapshot) (StorageSnapshot, error) {
	name := resourceName(snapshot.ProviderSnapshotRef)
	if name == "" {
		return StorageSnapshot{}, fmt.Errorf("storage_snapshot_provider_ref_required")
	}
	raw, err := p.callKubectl(ctx, []string{"get", "volumesnapshot/" + name, "-o", "json"}, nil, protectedresource.Target{})
	if err != nil {
		return StorageSnapshot{}, err
	}
	var item map[string]any
	if err := json.Unmarshal(raw, &item); err != nil {
		return StorageSnapshot{}, err
	}
	if ready, _ := nested(item, "status", "readyToUse").(bool); ready {
		snapshot.Status = "ready"
	} else {
		snapshot.Status = "creating"
	}
	snapshot.ProviderRequestID = providerRequestID("snapshot-sync", snapshot.ID)
	return snapshot, nil
}

func (p *TencentProvider) RestoreStorageSnapshot(ctx context.Context, input StorageRestoreInput, snapshot StorageSnapshot) (StorageVolume, error) {
	snapshotName := resourceName(snapshot.ProviderSnapshotRef)
	if snapshotName == "" {
		return StorageVolume{}, fmt.Errorf("storage_snapshot_provider_ref_required")
	}
	sizeGB := snapshot.SizeGB
	if sizeGB < 1 {
		return StorageVolume{}, fmt.Errorf("storage_snapshot_size_required")
	}
	name := k8sName(input.TargetVolumeID)
	if _, err := p.callKubectl(ctx, []string{"apply", "-f", "-"}, restoredPVCManifest(name, input.TargetVolumeID, input.AccountID, sizeGB, snapshotName), protectedresource.Target{}); err != nil {
		return StorageVolume{}, err
	}
	if _, err := p.callKubectl(ctx, []string{"wait", "--for=jsonpath={.status.phase}=Bound", "pvc/" + name + "-data", "--timeout=300s"}, nil, protectedresource.Target{}); err != nil {
		return StorageVolume{}, err
	}
	return StorageVolume{ID: input.TargetVolumeID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "ready", Provider: "tencent-tke", ProviderResourceID: "pvc/" + name + "-data", ProviderRequestID: providerRequestID("restore", input.IdempotencyKey), SizeGB: sizeGB, StorageClass: os.Getenv("OPL_WORKSPACE_STORAGE_CLASS"), CreatedAt: time.Now().UTC()}, nil
}

func (p *TencentProvider) DestroyStorageSnapshot(ctx context.Context, snapshot StorageSnapshot) (StorageSnapshot, error) {
	name := resourceName(snapshot.ProviderSnapshotRef)
	if name == "" {
		return StorageSnapshot{}, fmt.Errorf("storage_snapshot_provider_ref_required")
	}
	if _, err := p.callKubectl(ctx, []string{"delete", "volumesnapshot/" + name, "--ignore-not-found=true"}, nil, protectedresource.Target{}); err != nil {
		return StorageSnapshot{}, err
	}
	snapshot.Status = "destroyed"
	snapshot.ProviderRequestID = providerRequestID("snapshot-destroy", snapshot.ID)
	return snapshot, nil
}

func (p *TencentProvider) CreateStorageAttachment(ctx context.Context, input StorageAttachmentInput, compute ComputeAllocation, volume StorageVolume) (StorageAttachment, error) {
	pvName, pvcName := storageBindingNames(volume)
	if input.ComputeID == "" || input.ComputeID != compute.ID || input.VolumeID == "" || input.VolumeID != volume.ID ||
		compute.AccountID == "" || compute.AccountID != volume.AccountID || strings.TrimSpace(input.WorkspaceID) == "" ||
		input.WorkspaceID != compute.WorkspaceID || input.WorkspaceID != volume.WorkspaceID || !strings.HasPrefix(volume.ProviderResourceID, "disk-") ||
		volume.SizeGB <= 0 || strings.TrimSpace(volume.Zone) == "" || pvName == "" || pvcName == "" {
		return StorageAttachment{}, fmt.Errorf("storage_attachment_provider_identity_required")
	}
	if !isReadyResourceStatus(compute.Status) || volume.Status != "ready" {
		return StorageAttachment{}, fmt.Errorf("resource_status_invalid")
	}
	raw, err := p.callKubectl(ctx, []string{"get", "pv/" + pvName, "pvc/" + pvcName, "--ignore-not-found", "-o", "json"}, nil, protectedresource.Target{})
	if err != nil {
		return StorageAttachment{}, err
	}
	var pv, pvc map[string]any
	pvMatches, pvcMatches := 0, 0
	for _, item := range kubectlItems(raw) {
		resource, _ := item.(map[string]any)
		switch {
		case resource["kind"] == "PersistentVolume" && nested(resource, "metadata", "name") == pvName:
			pv, pvMatches = resource, pvMatches+1
		case resource["kind"] == "PersistentVolumeClaim" && nested(resource, "metadata", "name") == pvcName:
			pvc, pvcMatches = resource, pvcMatches+1
		}
	}
	if pvMatches != 1 || pvcMatches != 1 {
		return StorageAttachment{}, fmt.Errorf("storage_attachment_static_binding_unverified")
	}
	for _, resource := range []map[string]any{pv, pvc} {
		if nested(resource, "metadata", "labels", "oplcloud.cn/account-id") != compute.AccountID ||
			nested(resource, "metadata", "labels", "oplcloud.cn/workspace-id") != input.WorkspaceID ||
			nested(resource, "metadata", "labels", "oplcloud.cn/storage-id") != volume.ID {
			return StorageAttachment{}, fmt.Errorf("storage_attachment_static_binding_unverified")
		}
	}
	pvSpec, _ := pv["spec"].(map[string]any)
	pvcSpec, _ := pvc["spec"].(map[string]any)
	pvStorageClass := pvSpec["storageClassName"]
	pvcStorageClass, pvcStorageClassSet := pvcSpec["storageClassName"]
	pvAccessModes, _ := pvSpec["accessModes"].([]any)
	pvcAccessModes, _ := pvcSpec["accessModes"].([]any)
	expectedCapacity := fmt.Sprintf("%dGi", volume.SizeGB)
	expectedNodeAffinity := map[string]any{"required": map[string]any{"nodeSelectorTerms": []any{map[string]any{"matchExpressions": []any{map[string]any{"key": "topology.kubernetes.io/zone", "operator": "In", "values": []any{volume.Zone}}}}}}}
	if stringValue(nested(pvc, "status", "phase")) != "Bound" || stringValue(pvcSpec["volumeName"]) != pvName ||
		stringValue(nested(pv, "spec", "csi", "driver")) != "com.tencent.cloud.csi.cbs" || stringValue(nested(pv, "spec", "csi", "volumeHandle")) != volume.ProviderResourceID ||
		stringValue(pvSpec["persistentVolumeReclaimPolicy"]) != "Retain" || stringValue(pvStorageClass) != "" || !pvcStorageClassSet || stringValue(pvcStorageClass) != "" ||
		len(pvAccessModes) != 1 || stringValue(pvAccessModes[0]) != "ReadWriteOnce" || len(pvcAccessModes) != 1 || stringValue(pvcAccessModes[0]) != "ReadWriteOnce" ||
		stringValue(nested(pv, "spec", "capacity", "storage")) != expectedCapacity || stringValue(nested(pvc, "spec", "resources", "requests", "storage")) != expectedCapacity ||
		!reflect.DeepEqual(pvSpec["nodeAffinity"], expectedNodeAffinity) {
		return StorageAttachment{}, fmt.Errorf("storage_attachment_static_binding_unverified")
	}
	now := time.Now().UTC()
	id := "att_" + stableSuffix(firstNonEmpty(input.OperationID, input.IdempotencyKey))[:18]
	tags := oplCostTags(compute.AccountID, input.WorkspaceID, id, input.OperationID)
	return StorageAttachment{
		ID:                   id,
		OperationID:          input.OperationID,
		WorkspaceID:          input.WorkspaceID,
		ComputeID:            input.ComputeID,
		VolumeID:             input.VolumeID,
		Status:               "attached",
		Provider:             "tencent-tke",
		ProviderAttachmentID: "pv/" + pvName + ":pvc/" + pvcName,
		ProviderRequestID:    providerRequestID("storage-attach", input.IdempotencyKey),
		CostTags:             tags,
		CreatedAt:            now,
	}, nil
}

func (p *TencentProvider) ReadStorageAttachment(ctx context.Context, attachment StorageAttachment, compute ComputeAllocation, volume StorageVolume) (StorageAttachment, error) {
	readback, err := p.CreateStorageAttachment(ctx, StorageAttachmentInput{
		WorkspaceID: attachment.WorkspaceID, ComputeID: attachment.ComputeID, VolumeID: attachment.VolumeID,
		IdempotencyKey: attachment.OperationID, OperationID: attachment.OperationID,
	}, compute, volume)
	if err != nil {
		return attachment, err
	}
	if strings.HasPrefix(attachment.ID, "att_") && readback.ID != attachment.ID {
		return attachment, fmt.Errorf("storage_attachment_readback_mismatch")
	}
	readback.CreatedAt = attachment.CreatedAt
	return readback, nil
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
	if !validWorkspaceRuntimeImageIdentity(input.ImageID) {
		return WorkspaceRuntime{}, fmt.Errorf("workspace_image_identity_invalid")
	}
	now := time.Now().UTC()
	serviceName := firstNonEmpty(compute.ServiceName, k8sName(compute.ID))
	credentialSeed := stableID(input.WorkspaceID, input.IdempotencyKey)[:24]
	runtimeOperationID := firstNonEmpty(input.RuntimeOperationID, input.IdempotencyKey)
	runtimeID := "rt_" + stableSuffix(input.WorkspaceID, runtimeOperationID)[:18]
	tags := oplCostTags(compute.AccountID, input.WorkspaceID, runtimeID, runtimeOperationID)
	runtimeTarget := protectedresource.Target{PackageID: compute.PackageID, NodePoolID: compute.NodePoolID, MachineID: compute.MachineName, NodeName: compute.NodeName, CVMID: firstNonEmpty(compute.InstanceID, compute.CVMInstanceID)}
	if _, err := p.callKubectl(ctx, []string{"apply", "-f", "-"}, workspaceManifest(input, input.WorkspaceID, credentialSeed, runtimeID, serviceName, compute, volume, tags), runtimeTarget); err != nil {
		return WorkspaceRuntime{}, err
	}
	runtime, err := p.WorkspaceRuntimeStatus(ctx, input.WorkspaceID)
	if err != nil {
		return WorkspaceRuntime{}, err
	}
	runtime.ID = runtimeID
	runtime.OperationID = runtimeOperationID
	runtime.WorkspaceID = input.WorkspaceID
	runtime.URL = firstNonEmpty(runtime.URL, fmt.Sprintf("https://%s/w/%s/", workspaceDomain(), input.WorkspaceID))
	runtime.ServiceName = firstNonEmpty(runtime.ServiceName, serviceName)
	runtime.ProviderRequestID = providerRequestID("runtime", input.IdempotencyKey)
	runtime.CostTags = tags
	runtime.CreatedAt = now
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
	serviceName, pvcName, err := p.workspaceRuntimeResourcesStrict(ctx, workspaceID, false)
	if err != nil {
		return WorkspaceRuntime{WorkspaceID: workspaceID}, err
	}
	if serviceName == "" || pvcName == "" {
		return WorkspaceRuntime{WorkspaceID: workspaceID}, workspaceRuntimeStatusError("readback_mismatch")
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

func (p *TencentProvider) workspaceRuntimeResources(ctx context.Context, workspaceID string) (string, string) {
	serviceName, pvcName, _ := p.workspaceRuntimeResourcesStrict(ctx, workspaceID, false)
	return serviceName, pvcName
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
	resource, state := exactActivationResource(items, kind, name)
	switch state {
	case "one":
		return resource, nil
	case "multiple":
		return nil, workspaceRuntimeStatusError("ownership_conflict")
	default:
		return nil, workspaceRuntimeStatusError("readback_mismatch")
	}
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

func (p *TencentProvider) PublishWorkspaceContent(ctx context.Context, workspaceID, targetPath string, body []byte) error {
	serviceName, _ := p.workspaceRuntimeResources(ctx, workspaceID)
	if serviceName == "" {
		return fmt.Errorf("workspace_runtime_not_found")
	}
	target := path.Join("/projects", targetPath)
	digest := fmt.Sprintf("%x", sha256.Sum256(body))
	temporary := target + ".opl-upload-" + digest[:12]
	deployment := "deployment/" + serviceName
	if _, err := p.callKubectl(ctx, []string{"exec", deployment, "--", "mkdir", "-p", path.Dir(target)}, nil, protectedresource.Target{}); err != nil {
		return err
	}
	if _, err := p.callKubectl(ctx, []string{"exec", deployment, "--", "rm", "-f", temporary}, nil, protectedresource.Target{}); err != nil {
		return err
	}
	// ponytail: TKE exec stdin corrupts large writes; use bounded command arguments until measured throughput justifies object storage.
	const execChunkSize = 32 << 10
	for offset := 0; offset < len(body); offset += execChunkSize {
		end := min(offset+execChunkSize, len(body))
		encoded := base64.StdEncoding.EncodeToString(body[offset:end])
		args := []string{"exec", deployment, "--", "sh", "-c", `printf %s "$1" | base64 -d >> "$2"`, "--", encoded, temporary}
		if _, err := p.callKubectl(ctx, args, nil, protectedresource.Target{}); err != nil {
			return err
		}
	}
	if _, err := p.callKubectl(ctx, []string{"exec", deployment, "--", "mv", temporary, target}, nil, protectedresource.Target{}); err != nil {
		return err
	}
	digestOutput, err := p.callKubectl(ctx, []string{"exec", deployment, "--", "sha256sum", target}, nil, protectedresource.Target{})
	if err != nil {
		return fmt.Errorf("workspace_content_digest_command_failed: %w", err)
	}
	fields := strings.Fields(string(digestOutput))
	if len(fields) == 0 || !validDigest(fields[0]) {
		return fmt.Errorf("workspace_content_digest_invalid")
	}
	if fields[0] != digest {
		return fmt.Errorf("workspace_content_digest_mismatch expected_sha256=%s actual_sha256=%s", digest, fields[0])
	}
	return nil
}

func packagePlan(packageID string) plan {
	if packageID == "pro" {
		return plan{ID: "pool-pro-8c16g", Server: "8c16g", CPU: 8, MemoryGB: 16, DiskGB: 100, InstanceType: strings.TrimSpace(os.Getenv("OPL_PRO_COMPUTE_INSTANCE_TYPE"))}
	}
	return plan{ID: "pool-basic-2c4g", Server: "2c4g", CPU: 2, MemoryGB: 4, DiskGB: 10, InstanceType: strings.TrimSpace(os.Getenv("OPL_BASIC_COMPUTE_INSTANCE_TYPE"))}
}

func configuredPackagePlan(packageID string) (plan, error) {
	current := packagePlan(packageID)
	key := ""
	switch packageID {
	case "basic":
		key = "OPL_BASIC_COMPUTE_INSTANCE_TYPE"
	case "pro":
		key = "OPL_PRO_COMPUTE_INSTANCE_TYPE"
	default:
		return plan{}, ErrUnsupportedComputePackage
	}
	current.InstanceType = strings.TrimSpace(os.Getenv(key))
	if current.InstanceType == "" || len(current.InstanceType) > 64 || strings.ContainsAny(current.InstanceType, " \t\r\n") {
		return current, fmt.Errorf("compute_instance_type_configuration_required")
	}
	return current, nil
}

func staticCBSManifest(volume StorageVolume) []byte {
	pvName, pvcName := storageBindingNames(volume)
	labels := mergeStringMaps(map[string]string{"app.kubernetes.io/name": "opl-storage-volume", "app.kubernetes.io/instance": k8sName(volume.ID), "oplcloud.cn/storage-id": volume.ID, "oplcloud.cn/account-id": volume.AccountID}, k8sCostLabels(volume.CostTags))
	pv := map[string]any{
		"apiVersion": "v1", "kind": "PersistentVolume", "metadata": map[string]any{"name": pvName, "labels": labels, "annotations": volume.CostTags},
		"spec": map[string]any{
			"capacity": map[string]any{"storage": fmt.Sprintf("%dGi", volume.SizeGB)}, "accessModes": []string{"ReadWriteOnce"},
			"persistentVolumeReclaimPolicy": "Retain", "storageClassName": "",
			"csi":          map[string]any{"driver": "com.tencent.cloud.csi.cbs", "volumeHandle": volume.ProviderResourceID},
			"nodeAffinity": map[string]any{"required": map[string]any{"nodeSelectorTerms": []any{map[string]any{"matchExpressions": []any{map[string]any{"key": "topology.kubernetes.io/zone", "operator": "In", "values": []string{volume.Zone}}}}}}},
		},
	}
	pvc := map[string]any{
		"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": map[string]any{"name": pvcName, "labels": labels, "annotations": volume.CostTags},
		"spec": map[string]any{"accessModes": []string{"ReadWriteOnce"}, "storageClassName": "", "volumeName": pvName, "resources": map[string]any{"requests": map[string]any{"storage": fmt.Sprintf("%dGi", volume.SizeGB)}}},
	}
	return mustJSON(map[string]any{"apiVersion": "v1", "kind": "List", "items": []any{pv, pvc}})
}

func storageBindingNames(volume StorageVolume) (string, string) {
	pv, pvc := volume.ProviderData["pvName"], volume.ProviderData["pvcName"]
	if pvc == "" && strings.HasPrefix(volume.ProviderResourceID, "pvc/") {
		pvc = resourceName(volume.ProviderResourceID)
	}
	if strings.HasPrefix(volume.ProviderResourceID, "disk-") {
		name := k8sName(volume.ID)
		pv, pvc = firstNonEmpty(pv, name+"-pv"), firstNonEmpty(pvc, name+"-data")
	}
	return pv, pvc
}

func storagePVCName(volume StorageVolume) string {
	_, pvc := storageBindingNames(volume)
	return pvc
}

func volumeSnapshotManifest(name, pvcName, snapshotClass string, input StorageSnapshotInput) []byte {
	return mustJSON(map[string]any{
		"apiVersion": "snapshot.storage.k8s.io/v1",
		"kind":       "VolumeSnapshot",
		"metadata":   map[string]any{"name": name, "labels": map[string]string{"app.kubernetes.io/name": "opl-storage-snapshot", "oplcloud.cn/account-id": input.AccountID, "oplcloud.cn/workspace-id": input.WorkspaceID, "oplcloud.cn/storage-id": input.VolumeID}},
		"spec":       map[string]any{"volumeSnapshotClassName": snapshotClass, "source": map[string]any{"persistentVolumeClaimName": pvcName}},
	})
}

func restoredPVCManifest(name, storageID, accountID string, sizeGB int, snapshotName string) []byte {
	return mustJSON(map[string]any{
		"apiVersion": "v1",
		"kind":       "PersistentVolumeClaim",
		"metadata":   map[string]any{"name": name + "-data", "labels": map[string]string{"app.kubernetes.io/name": "opl-storage-volume", "oplcloud.cn/storage-id": storageID, "oplcloud.cn/account-id": accountID}},
		"spec": map[string]any{
			"accessModes":      []string{"ReadWriteOnce"},
			"storageClassName": os.Getenv("OPL_WORKSPACE_STORAGE_CLASS"),
			"resources":        map[string]any{"requests": map[string]any{"storage": fmt.Sprintf("%dGi", sizeGB)}},
			"dataSource":       map[string]any{"apiGroup": "snapshot.storage.k8s.io", "kind": "VolumeSnapshot", "name": snapshotName},
		},
	})
}

func workspaceManifest(input WorkspaceRuntimeInput, workspaceName string, credentialSeed string, runtimeID string, serviceName string, compute ComputeAllocation, storage StorageVolume, tags map[string]string) []byte {
	workspaceID := input.WorkspaceID
	gatewaySecretRef := input.GatewaySecretRef
	selectorLabels := stringAnyMap(runtimeSelectorLabels(serviceName, compute))
	identityLabels := map[string]string{
		"oplcloud.cn/account-id":              compute.AccountID,
		"oplcloud.cn/workspace-id":            workspaceID,
		"oplcloud.cn/compute-allocation-id":   compute.ID,
		"oplcloud.cn/storage-id":              storage.ID,
		"oplcloud.cn/attachment-id":           input.AttachmentID,
		"oplcloud.cn/attachment-operation-id": input.AttachmentOperationID,
		"oplcloud.cn/runtime-id":              runtimeID,
		"oplcloud.cn/runtime-operation-id":    input.RuntimeOperationID,
	}
	labels := stringAnyMap(mergeStringMaps(runtimeSelectorLabels(serviceName, compute), identityLabels, k8sCostLabels(tags)))
	pvcName := storagePVCName(storage)
	plan := packagePlan(compute.PackageID)
	password := deriveAionUIAdminPassword(os.Getenv("OPL_AIONUI_ADMIN_PASSWORD_SEED"), workspaceID, credentialSeed)
	secretData := map[string]any{"webui_password": b64(password), "webui_session_secret": b64(deriveWebUISessionSecret(os.Getenv("OPL_AIONUI_ADMIN_PASSWORD_SEED"), workspaceID, credentialSeed))}
	secretItems := []any{map[string]any{"key": "webui_password", "path": "opl_webui_password"}, map[string]any{"key": "webui_session_secret", "path": "webui_session_secret"}}
	workspaceEnv := []any{
		map[string]any{"name": "OPL_WEBUI_DEPLOYMENT_MODE", "value": "cloud"},
		map[string]any{"name": "OPL_WEBUI_AUTH_MODE", "value": "password"},
		map[string]any{"name": "OPL_WEBUI_USERNAME", "value": webuiUsername},
		map[string]any{"name": "OPL_WEBUI_PASSWORD_FILE", "value": "/run/secrets/opl_webui_password"},
		map[string]any{"name": "OPL_WEBUI_SESSION_SECRET_FILE", "value": "/run/secrets/webui_session_secret"},
		map[string]any{"name": "OPL_CODEX_MODEL", "value": os.Getenv("OPL_CODEX_MODEL")},
		map[string]any{"name": "OPL_CODEX_REASONING_EFFORT", "value": os.Getenv("OPL_CODEX_REASONING_EFFORT")},
		map[string]any{"name": "OPL_CODEX_PROVIDER_NAME", "value": os.Getenv("OPL_CODEX_PROVIDER_NAME")},
		map[string]any{"name": "OPL_WORKSPACE_ID", "value": workspaceID},
		map[string]any{"name": "OPL_WORKSPACE_NAME", "value": workspaceName},
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
	if gatewaySecretRef != "" {
		workspaceEnv = append(workspaceEnv, map[string]any{"name": "OPL_GATEWAY_API_KEY_FILE", "value": "/run/secrets/opl_gateway_api_key"})
	}
	workspaceContainer := map[string]any{"name": "workspace", "image": input.ImageID, "imagePullPolicy": "IfNotPresent", "ports": []any{map[string]any{"name": "http", "containerPort": 3000}}, "env": workspaceEnv, "volumeMounts": []any{map[string]any{"name": "workspace-data", "mountPath": "/data", "subPath": "data"}, map[string]any{"name": "workspace-data", "mountPath": "/projects", "subPath": "projects"}, map[string]any{"name": "workspace-secrets", "mountPath": "/run/secrets", "readOnly": true}}, "resources": workspaceResources(plan), "readinessProbe": map[string]any{"httpGet": map[string]any{"path": "/healthz", "port": 3000}, "initialDelaySeconds": 10, "periodSeconds": 10}, "securityContext": map[string]any{"allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []any{"ALL"}}}}
	secretLabels := stringAnyMap(mergeStringMaps(map[string]string{"app.kubernetes.io/name": "opl-workspace-entry", "app.kubernetes.io/instance": serviceName}, identityLabels, k8sCostLabels(tags)))
	secret := map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": serviceName + "-env", "labels": secretLabels, "annotations": tags}, "type": "Opaque", "data": secretData}
	secretSources := []any{map[string]any{"secret": map[string]any{"name": serviceName + "-env", "items": secretItems}}}
	if gatewaySecretRef != "" {
		secretSources = append(secretSources, map[string]any{"secret": map[string]any{"name": gatewaySecretRef, "items": []any{map[string]any{"key": "opl_gateway_api_key", "path": "opl_gateway_api_key"}}}})
	}
	secretVolume := map[string]any{"name": "workspace-secrets", "projected": map[string]any{"sources": secretSources}}
	podAnnotations := stringAnyMap(mergeStringMaps(tags, map[string]string{"opl.medopl.cn/credential-revision": stableID("workspace-credential", workspaceID, credentialSeed)[:16]}))
	deployment := map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": serviceName, "labels": labels, "annotations": tags}, "spec": map[string]any{"replicas": 1, "selector": map[string]any{"matchLabels": selectorLabels}, "template": map[string]any{"metadata": map[string]any{"labels": labels, "annotations": podAnnotations}, "spec": map[string]any{"automountServiceAccountToken": false, "dnsPolicy": "ClusterFirst", "securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 10001, "runAsGroup": 10001, "fsGroup": 10001, "seccompProfile": map[string]any{"type": "RuntimeDefault"}}, "imagePullSecrets": []any{map[string]any{"name": os.Getenv("OPL_IMAGE_PULL_SECRET_NAME")}}, "nodeSelector": map[string]any{"kubernetes.io/hostname": compute.NodeName}, "tolerations": workspaceNodeTolerations(compute.PackageID), "containers": []any{workspaceContainer}, "volumes": []any{map[string]any{"name": "workspace-data", "persistentVolumeClaim": map[string]any{"claimName": pvcName}}, secretVolume}}}}}
	service := map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": serviceName, "labels": labels, "annotations": tags}, "spec": map[string]any{"type": "ClusterIP", "selector": selectorLabels, "ports": []any{map[string]any{"name": "http", "port": 3000, "targetPort": "http"}}}}
	networkPolicy := map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": map[string]any{"name": serviceName, "labels": labels, "annotations": tags}, "spec": map[string]any{"podSelector": map[string]any{"matchLabels": selectorLabels}, "policyTypes": []any{"Ingress", "Egress"}, "ingress": []any{map[string]any{"from": []any{map[string]any{"podSelector": map[string]any{"matchLabels": map[string]any{"app.kubernetes.io/name": "opl-cloud", "app.kubernetes.io/component": "control-plane"}}}}, "ports": []any{map[string]any{"protocol": "TCP", "port": 3000}}}}, "egress": workspaceEgressRules()}}
	return mustJSON(map[string]any{"apiVersion": "v1", "kind": "List", "items": []any{secret, deployment, service, networkPolicy}})
}

func workspaceNodeTolerations(packageID string) []any {
	return []any{
		map[string]any{"key": "oplcloud.cn/package-id", "operator": "Equal", "value": packageID, "effect": "NoSchedule"},
	}
}

func workspaceEgressRules() []any {
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

func runtimeSelectorLabels(serviceName string, compute ComputeAllocation) map[string]string {
	return map[string]string{"app.kubernetes.io/name": "opl-compute-allocation", "app.kubernetes.io/instance": serviceName, "oplcloud.cn/compute-allocation-id": compute.ID}
}

func oplCostTags(accountID string, workspaceID string, resourceID string, operationID string) map[string]string {
	return map[string]string{
		"opl_account_id":   accountID,
		"opl_workspace_id": workspaceID,
		"opl_resource_id":  resourceID,
		"opl_operation_id": operationID,
	}
}

func k8sCostLabels(tags map[string]string) map[string]string {
	return map[string]string{
		"oplcloud.cn/account-id":   tags["opl_account_id"],
		"oplcloud.cn/workspace-id": tags["opl_workspace_id"],
		"oplcloud.cn/resource-id":  tags["opl_resource_id"],
		"oplcloud.cn/operation-id": tags["opl_operation_id"],
	}
}

func mergeStringMaps(values ...map[string]string) map[string]string {
	merged := map[string]string{}
	for _, value := range values {
		for key, item := range value {
			if strings.TrimSpace(item) != "" {
				merged[key] = item
			}
		}
	}
	return merged
}

func stringAnyMap(values map[string]string) map[string]any {
	result := map[string]any{}
	for key, value := range values {
		result[key] = value
	}
	return result
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

func deriveWebUISessionSecret(seed string, workspaceID string, token string) string {
	secret := strings.TrimSpace(seed)
	if secret == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("webui-session:" + workspaceID + ":" + token))
	return hex.EncodeToString(mac.Sum(nil))
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

func strictKubectlItems(raw []byte) ([]any, error) {
	var list map[string]any
	if err := json.Unmarshal(raw, &list); err != nil || len(list) == 0 {
		return nil, fmt.Errorf("kubernetes_response_invalid")
	}
	if stringValue(list["kind"]) == "List" {
		items, ok := list["items"].([]any)
		if !ok {
			return nil, fmt.Errorf("kubernetes_response_invalid")
		}
		return items, nil
	}
	if stringValue(list["kind"]) == "" {
		return nil, fmt.Errorf("kubernetes_response_invalid")
	}
	return []any{list}, nil
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

func workloadUsesPVC(workload map[string]any, pvcName string) bool {
	volumes, _ := nested(workload, "spec", "template", "spec", "volumes").([]any)
	if len(volumes) == 0 {
		volumes, _ = nested(workload, "spec", "volumes").([]any)
	}
	volumeName := ""
	for _, volume := range volumes {
		asMap, _ := volume.(map[string]any)
		if nested(asMap, "persistentVolumeClaim", "claimName") == pvcName {
			name := stringValue(asMap["name"])
			if name == "" || volumeName != "" {
				return false
			}
			volumeName = name
		}
	}
	if volumeName == "" {
		return false
	}
	containers, _ := nested(workload, "spec", "template", "spec", "containers").([]any)
	if len(containers) == 0 {
		containers, _ = nested(workload, "spec", "containers").([]any)
	}
	workspaceContainers := 0
	validMounts := false
	for _, container := range containers {
		asMap, _ := container.(map[string]any)
		if stringValue(asMap["name"]) != "workspace" {
			continue
		}
		workspaceContainers++
		mounts, _ := asMap["volumeMounts"].([]any)
		dataMounted, projectsMounted, retainedMounts := false, false, 0
		for _, mount := range mounts {
			asMount, _ := mount.(map[string]any)
			name, mountPath, subPath := stringValue(asMount["name"]), stringValue(asMount["mountPath"]), stringValue(asMount["subPath"])
			if name == volumeName {
				retainedMounts++
				switch {
				case mountPath == "/data" && subPath == "data":
					dataMounted = true
				case mountPath == "/projects" && subPath == "projects":
					projectsMounted = true
				default:
					return false
				}
			} else if mountPath == "/data" || mountPath == "/projects" {
				return false
			}
		}
		validMounts = retainedMounts == 2 && dataMounted && projectsMounted
	}
	return workspaceContainers == 1 && validMounts
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

func workspaceNetworkPolicyReady(policy map[string]any, deployment map[string]any) bool {
	podSelector, _ := nested(policy, "spec", "podSelector").(map[string]any)
	selector, _ := podSelector["matchLabels"].(map[string]any)
	deploymentSelector, _ := nested(deployment, "spec", "selector", "matchLabels").(map[string]any)
	policyTypes, _ := nested(policy, "spec", "policyTypes").([]any)
	ingress, _ := nested(policy, "spec", "ingress").([]any)
	egress, _ := nested(policy, "spec", "egress").([]any)
	if len(podSelector) != 1 || len(selector) == 0 || !reflect.DeepEqual(selector, deploymentSelector) || len(policyTypes) != 2 ||
		stringValue(policyTypes[0]) != "Ingress" || stringValue(policyTypes[1]) != "Egress" || len(ingress) != 1 || !bytes.Equal(mustJSON(egress), mustJSON(workspaceEgressRules())) {
		return false
	}
	deploymentName := stringValue(nested(deployment, "metadata", "name"))
	computeID := stringValue(selector["oplcloud.cn/compute-allocation-id"])
	if len(selector) != 3 || stringValue(selector["app.kubernetes.io/name"]) != "opl-compute-allocation" || stringValue(selector["app.kubernetes.io/instance"]) != deploymentName || computeID == "" || stringValue(nested(deployment, "metadata", "labels", "oplcloud.cn/compute-allocation-id")) != computeID {
		return false
	}
	rule, _ := ingress[0].(map[string]any)
	from, _ := rule["from"].([]any)
	ports, _ := rule["ports"].([]any)
	if len(rule) != 2 || len(from) != 1 || len(ports) != 1 {
		return false
	}
	peer, _ := from[0].(map[string]any)
	sourceSelector, _ := peer["podSelector"].(map[string]any)
	sourceLabels, _ := sourceSelector["matchLabels"].(map[string]any)
	wantSourceLabels := map[string]any{"app.kubernetes.io/name": "opl-cloud", "app.kubernetes.io/component": "control-plane"}
	if len(peer) != 1 || len(sourceSelector) != 1 || !reflect.DeepEqual(sourceLabels, wantSourceLabels) {
		return false
	}
	port, _ := ports[0].(map[string]any)
	return len(port) == 2 && stringValue(port["protocol"]) == "TCP" && number(port["port"]) == 3000
}

func workspaceNetworkPoliciesReady(policies []any, deployment map[string]any, pods []any) bool {
	deploymentName := stringValue(nested(deployment, "metadata", "name"))
	canonicalPolicy := findK8s(policies, "NetworkPolicy", deploymentName)
	if !workspaceNetworkPolicyReady(canonicalPolicy, deployment) {
		return false
	}
	podLabelValues := []any{nested(deployment, "spec", "template", "metadata", "labels")}
	for _, item := range pods {
		pod, ok := item.(map[string]any)
		if !ok {
			return false
		}
		podLabelValues = append(podLabelValues, nested(pod, "metadata", "labels"))
	}
	podLabelSets := make([]k8slabels.Set, 0, len(podLabelValues))
	for _, value := range podLabelValues {
		values, ok := value.(map[string]any)
		if !ok {
			return false
		}
		labelSet := k8slabels.Set{}
		for key, value := range values {
			text, ok := value.(string)
			if !ok {
				return false
			}
			labelSet[key] = text
		}
		podLabelSets = append(podLabelSets, labelSet)
	}
	canonicalSelector, ok := networkPolicyPodSelector(canonicalPolicy)
	if !ok {
		return false
	}
	for _, podLabels := range podLabelSets {
		if !canonicalSelector.Matches(podLabels) {
			return false
		}
	}
	for _, item := range policies {
		policy, ok := item.(map[string]any)
		if !ok || stringValue(policy["kind"]) != "NetworkPolicy" {
			continue
		}
		hasEgress := false
		policyTypes, _ := nested(policy, "spec", "policyTypes").([]any)
		for _, policyType := range policyTypes {
			hasEgress = hasEgress || stringValue(policyType) == "Egress"
		}
		if !hasEgress {
			continue
		}
		selector, ok := networkPolicyPodSelector(policy)
		if !ok {
			return false
		}
		egress, _ := nested(policy, "spec", "egress").([]any)
		for _, podLabels := range podLabelSets {
			if selector.Matches(podLabels) && !bytes.Equal(mustJSON(egress), mustJSON(workspaceEgressRules())) {
				return false
			}
		}
	}
	return true
}

func networkPolicyPodSelector(policy map[string]any) (k8slabels.Selector, bool) {
	rawSelector, err := json.Marshal(nested(policy, "spec", "podSelector"))
	if err != nil {
		return nil, false
	}
	var labelSelector metav1.LabelSelector
	if err := json.Unmarshal(rawSelector, &labelSelector); err != nil {
		return nil, false
	}
	selector, err := metav1.LabelSelectorAsSelector(&labelSelector)
	return selector, err == nil
}

func workspaceRuntimeIsolationReady(deployment map[string]any, pods []any) bool {
	generation := number(nested(deployment, "metadata", "generation"))
	desiredReplicas := number(nested(deployment, "spec", "replicas"))
	if generation <= 0 || number(nested(deployment, "status", "observedGeneration")) != generation || desiredReplicas <= 0 ||
		number(nested(deployment, "status", "updatedReplicas")) != desiredReplicas || number(nested(deployment, "status", "readyReplicas")) != desiredReplicas || number(nested(deployment, "status", "availableReplicas")) != desiredReplicas {
		return false
	}
	templateSpec, _ := nested(deployment, "spec", "template", "spec").(map[string]any)
	templateImage, isolated := workspaceRuntimeSpecImage(templateSpec)
	if !isolated {
		return false
	}
	readyPods := 0
	activePods := 0
	for _, item := range pods {
		pod, _ := item.(map[string]any)
		phase := stringValue(nested(pod, "status", "phase"))
		if phase == "Succeeded" || phase == "Failed" {
			continue
		}
		activePods++
		spec, _ := pod["spec"].(map[string]any)
		image, isolated := workspaceRuntimeSpecImage(spec)
		if !isolated || image != templateImage {
			return false
		}
		if conditionStatuses(nested(pod, "status", "conditions"))["Ready"] == "True" {
			readyPods++
		}
	}
	return number(activePods) == desiredReplicas && number(readyPods) == desiredReplicas
}

func workspaceRuntimeSpecImage(spec map[string]any) (string, bool) {
	initContainers, _ := spec["initContainers"].([]any)
	ephemeralContainers, _ := spec["ephemeralContainers"].([]any)
	if len(spec) == 0 || len(initContainers) != 0 || len(ephemeralContainers) != 0 || spec["hostNetwork"] == true || stringValue(spec["dnsPolicy"]) != "ClusterFirst" || spec["automountServiceAccountToken"] != false ||
		nested(spec, "securityContext", "runAsNonRoot") != true || number(nested(spec, "securityContext", "runAsUser")) != 10001 ||
		number(nested(spec, "securityContext", "runAsGroup")) != 10001 || number(nested(spec, "securityContext", "fsGroup")) != 10001 ||
		stringValue(nested(spec, "securityContext", "seccompProfile", "type")) != "RuntimeDefault" {
		return "", false
	}
	containers, _ := spec["containers"].([]any)
	if len(containers) != 1 {
		return "", false
	}
	container, _ := containers[0].(map[string]any)
	workspaceImage := stringValue(container["image"])
	security, _ := container["securityContext"].(map[string]any)
	capabilities, _ := security["capabilities"].(map[string]any)
	containerSeccomp := stringValue(nested(security, "seccompProfile", "type"))
	runAsNonRoot, hasRunAsNonRoot := security["runAsNonRoot"]
	runAsUser, hasRunAsUser := security["runAsUser"]
	runAsGroup, hasRunAsGroup := security["runAsGroup"]
	if stringValue(container["name"]) != "workspace" || workspaceImage == "" || security["allowPrivilegeEscalation"] != false || security["privileged"] == true || len(capabilities) != 1 || !reflect.DeepEqual(capabilities["drop"], []any{"ALL"}) || (containerSeccomp != "" && containerSeccomp != "RuntimeDefault") ||
		(hasRunAsNonRoot && runAsNonRoot != true) || (hasRunAsUser && number(runAsUser) != 10001) || (hasRunAsGroup && number(runAsGroup) != 10001) {
		return "", false
	}
	return workspaceImage, true
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

func podRuntimeDetails(pods []any) map[string]any {
	details := map[string]any{"podCount": len(pods)}
	if len(pods) == 0 {
		return details
	}
	pod, _ := pods[0].(map[string]any)
	conditions := conditionStatuses(nested(pod, "status", "conditions"))
	details["podName"] = stringValue(nested(pod, "metadata", "name"))
	details["phase"] = stringValue(nested(pod, "status", "phase"))
	details["nodeName"] = stringValue(nested(pod, "spec", "nodeName"))
	details["podIP"] = stringValue(nested(pod, "status", "podIP"))
	details["podReady"] = conditions["Ready"] == "True"
	details["podScheduled"] = conditions["PodScheduled"] == "True"
	details["initContainers"] = containerStateSummaries(nested(pod, "status", "initContainerStatuses"))
	details["containers"] = containerStateSummaries(nested(pod, "status", "containerStatuses"))
	return details
}

func conditionStatuses(value any) map[string]string {
	statuses := map[string]string{}
	conditions, _ := value.([]any)
	for _, condition := range conditions {
		asMap, _ := condition.(map[string]any)
		statuses[stringValue(asMap["type"])] = stringValue(asMap["status"])
	}
	return statuses
}

func containerStateSummaries(value any) []map[string]any {
	statuses, _ := value.([]any)
	summaries := []map[string]any{}
	for _, status := range statuses {
		asMap, _ := status.(map[string]any)
		summary := map[string]any{"name": stringValue(asMap["name"]), "ready": asMap["ready"] == true, "restartCount": number(asMap["restartCount"])}
		state, _ := asMap["state"].(map[string]any)
		for _, key := range []string{"waiting", "terminated", "running"} {
			if state[key] == nil {
				continue
			}
			summary["state"] = key
			if stateMap, ok := state[key].(map[string]any); ok {
				if reason := stringValue(stateMap["reason"]); reason != "" {
					summary["reason"] = reason
				}
				if exitCode := number(stateMap["exitCode"]); exitCode != 0 {
					summary["exitCode"] = exitCode
				}
			}
			break
		}
		summaries = append(summaries, summary)
	}
	return summaries
}

func mergeDetails(base map[string]any, extra map[string]any) map[string]any {
	for key, value := range extra {
		base[key] = value
	}
	return base
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
