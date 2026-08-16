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
	if allocation.Provider != "tencent-tke" {
		return fmt.Errorf("compute_provider_readback_mismatch")
	}
	return validateTencentComputeAllocationIdentity(allocation, prepared)
}

func validateTencentComputeAllocationIdentity(allocation ComputeAllocation, prepared ComputeAllocationPreparation) error {
	if prepared.NodePoolID == "" || allocation.NodePoolID != prepared.NodePoolID || allocation.PoolID != prepared.PoolID || allocation.PackageID != prepared.PackageID ||
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
	mutation, err := beginProviderMutation(ctx, "tencent_gateway_secret_apply", "gateway_secret", secret.SecretRef, secret.Version)
	if err != nil {
		return GatewaySecret{}, err
	}
	if mutation != nil && !mutation.Fresh {
		readback, readErr := p.ReadGatewaySecret(ctx, input)
		if readErr != nil {
			_ = mutation.complete(ctx, "", secret, readErr)
			return GatewaySecret{}, readErr
		}
		if completeErr := mutation.complete(ctx, providerRequestID("gateway-secret", input.IdempotencyKey), readback, nil); completeErr != nil {
			return GatewaySecret{}, completeErr
		}
		return readback, nil
	}
	if _, err := p.callKubectl(ctx, []string{"apply", "-f", "-"}, manifest, protectedresource.Target{}); err != nil {
		_ = mutation.complete(ctx, "", secret, err)
		return GatewaySecret{}, err
	}
	readback, err := p.ReadGatewaySecret(ctx, input)
	if err != nil {
		_ = mutation.complete(ctx, "", secret, err)
		return GatewaySecret{}, err
	}
	if err := mutation.complete(ctx, providerRequestID("gateway-secret", input.IdempotencyKey), readback, nil); err != nil {
		return GatewaySecret{}, err
	}
	return readback, nil
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

func (p *TencentProvider) ReadStorageAttachmentProviderFacts(ctx context.Context, attachment StorageAttachment, compute ComputeAllocation, volume StorageVolume) (ProviderResourceFacts, error) {
	readback, err := p.ReadStorageAttachment(ctx, attachment, compute, volume)
	if err != nil {
		return ProviderResourceFacts{}, err
	}
	return ProviderResourceFacts{PackageOrSpec: "/data", ProviderID: readback.ProviderAttachmentID, Status: readback.Status}, nil
}

func (p *TencentProvider) DetachStorageAttachment(_ context.Context, attachment StorageAttachment) (StorageAttachment, error) {
	attachment.Status = "detached"
	attachment.ProviderRequestID = providerRequestID("storage-detach", attachment.ID)
	return attachment, nil
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
	return workspaceManifestWithGatewayBinding(input, workspaceName, credentialSeed, runtimeID, serviceName, compute, storage, tags, tencentWorkspaceRuntimeGatewayBinding{})
}

func workspaceManifestWithGatewayBinding(input WorkspaceRuntimeInput, workspaceName string, credentialSeed string, runtimeID string, serviceName string, compute ComputeAllocation, storage StorageVolume, tags map[string]string, gateway tencentWorkspaceRuntimeGatewayBinding) []byte {
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
	if gateway != (tencentWorkspaceRuntimeGatewayBinding{}) {
		podAnnotations["opl.medopl.cn/gateway-secret-ref"] = gateway.SecretRef
		podAnnotations["opl.medopl.cn/gateway-key-id"] = strconv.FormatInt(gateway.WorkspaceAPIKeyID, 10)
		podAnnotations["opl.medopl.cn/gateway-fingerprint"] = gateway.Fingerprint
	}
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
