package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	cvm "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cvm/v20170312"
	tke "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/tke/v20180525"
)

var requiredTencentEnv = []string{
	"TENCENTCLOUD_SECRET_ID",
	"TENCENTCLOUD_SECRET_KEY",
	"TENCENTCLOUD_REGION",
	"TENCENT_DEPLOY_CLUSTER_ID",
}

type Request struct {
	Action     string                 `json:"action"`
	DryRun     bool                   `json:"dryRun,omitempty"`
	AccountId  string                 `json:"accountId,omitempty"`
	UserId     string                 `json:"userId,omitempty"`
	PackageId  string                 `json:"packageId,omitempty"`
	Pool       ComputePoolInput       `json:"pool,omitempty"`
	Allocation ComputeAllocationInput `json:"allocation,omitempty"`
}

type ComputePoolInput struct {
	Id                string            `json:"id,omitempty"`
	PackageId         string            `json:"packageId,omitempty"`
	InstanceType      string            `json:"instanceType,omitempty"`
	NodePoolId        string            `json:"nodePoolId,omitempty"`
	DesiredNodeLabels map[string]string `json:"desiredNodeLabels,omitempty"`
}

type ComputeAllocationInput struct {
	Id         string `json:"id,omitempty"`
	InstanceId string `json:"instanceId,omitempty"`
	NodeName   string `json:"nodeName,omitempty"`
}

type Response struct {
	Ok                bool              `json:"ok"`
	OperationId       string            `json:"operationId,omitempty"`
	PoolId            string            `json:"poolId,omitempty"`
	NodePoolId        string            `json:"nodePoolId,omitempty"`
	InstanceId        string            `json:"instanceId,omitempty"`
	NodeName          string            `json:"nodeName,omitempty"`
	Status            string            `json:"status,omitempty"`
	ProviderRequestId string            `json:"providerRequestId,omitempty"`
	ProviderData      map[string]string `json:"providerData,omitempty"`
	ErrorCode         string            `json:"errorCode,omitempty"`
	Message           string            `json:"message,omitempty"`
	Retryable         bool              `json:"retryable,omitempty"`
	MissingEnv        []string          `json:"missingEnv,omitempty"`
}

type TencentClient interface {
	CreateComputeAllocation(request Request, env map[string]string) Response
	DestroyComputeAllocation(request Request, env map[string]string) Response
}

type unimplementedTencentClient struct{}

type tencentSDKClient struct {
	region    string
	clusterId string
	tkeClient tkeComputeAPI
	cvmClient cvmComputeAPI
}

type tkeComputeAPI interface {
	CreateClusterNodePool(request *tke.CreateClusterNodePoolRequest) (*tke.CreateClusterNodePoolResponse, error)
	CreateClusterInstances(request *tke.CreateClusterInstancesRequest) (*tke.CreateClusterInstancesResponse, error)
	AddNodeToNodePool(request *tke.AddNodeToNodePoolRequest) (*tke.AddNodeToNodePoolResponse, error)
}

type cvmComputeAPI interface {
	TerminateInstances(request *cvm.TerminateInstancesRequest) (*cvm.TerminateInstancesResponse, error)
}

func (unimplementedTencentClient) CreateComputeAllocation(_ Request, _ map[string]string) Response {
	return Response{
		Ok:        false,
		ErrorCode: "tencent_live_not_implemented",
		Message:   "Tencent live compute allocation is not implemented in this build.",
		Retryable: false,
	}
}

func (unimplementedTencentClient) DestroyComputeAllocation(_ Request, _ map[string]string) Response {
	return Response{
		Ok:        false,
		ErrorCode: "tencent_live_not_implemented",
		Message:   "Tencent live compute allocation destroy is not implemented in this build.",
		Retryable: false,
	}
}

func newTencentSDKClient(env map[string]string) (*tencentSDKClient, *Response) {
	missing := missingEnv(env)
	if len(missing) > 0 {
		return nil, &Response{
			Ok:         false,
			ErrorCode:  "tencent_env_missing",
			Message:    "Tencent Cloud provisioner environment is incomplete.",
			MissingEnv: missing,
			Retryable:  false,
		}
	}
	credential := common.NewCredential(env["TENCENTCLOUD_SECRET_ID"], env["TENCENTCLOUD_SECRET_KEY"])

	tkeProfile := profile.NewClientProfile()
	tkeProfile.HttpProfile.Endpoint = "tke.tencentcloudapi.com"
	tkeClient, err := tke.NewClient(credential, env["TENCENTCLOUD_REGION"], tkeProfile)
	if err != nil {
		return nil, &Response{
			Ok:        false,
			ErrorCode: "tencent_sdk_client_failed",
			Message:   err.Error(),
			Retryable: false,
		}
	}

	cvmProfile := profile.NewClientProfile()
	cvmProfile.HttpProfile.Endpoint = "cvm.tencentcloudapi.com"
	cvmClient, err := cvm.NewClient(credential, env["TENCENTCLOUD_REGION"], cvmProfile)
	if err != nil {
		return nil, &Response{
			Ok:        false,
			ErrorCode: "tencent_sdk_client_failed",
			Message:   err.Error(),
			Retryable: false,
		}
	}

	return &tencentSDKClient{
		region:    env["TENCENTCLOUD_REGION"],
		clusterId: env["TENCENT_DEPLOY_CLUSTER_ID"],
		tkeClient: tkeClient,
		cvmClient: cvmClient,
	}, nil
}

func (client *tencentSDKClient) CreateComputeAllocation(request Request, env map[string]string) Response {
	if client == nil || client.tkeClient == nil {
		return Response{Ok: false, ErrorCode: "tencent_sdk_client_missing", Message: "Tencent TKE SDK client is missing.", Retryable: false}
	}
	nodePoolId := request.Pool.NodePoolId
	createNodePoolRequestId := ""
	if nodePoolId == "" {
		createNodePoolRequest, failure := buildCreateClusterNodePoolRequest(request, env)
		if failure != nil {
			return *failure
		}
		createNodePoolResponse, err := client.tkeClient.CreateClusterNodePool(createNodePoolRequest)
		if err != nil {
			return sdkErrorResponse("tencent_create_node_pool_failed", err)
		}
		nodePoolId = stringValue(createNodePoolResponse.Response.NodePoolId)
		createNodePoolRequestId = stringValue(createNodePoolResponse.Response.RequestId)
		if nodePoolId == "" {
			return Response{
				Ok:                false,
				ErrorCode:         "tencent_node_pool_id_missing",
				Message:           "Tencent TKE did not return a node pool id.",
				ProviderRequestId: createNodePoolRequestId,
				Retryable:         true,
			}
		}
	}
	request.Pool.NodePoolId = nodePoolId
	createRequest, failure := buildCreateClusterInstancesRequest(request, env)
	if failure != nil {
		failure.ProviderRequestId = createNodePoolRequestId
		return *failure
	}
	createResponse, err := client.tkeClient.CreateClusterInstances(createRequest)
	if err != nil {
		response := sdkErrorResponse("tencent_create_cluster_instances_failed", err)
		response.ProviderRequestId = createNodePoolRequestId
		return response
	}
	instanceId := firstString(createResponse.Response.InstanceIdSet)
	if instanceId == "" {
		return Response{Ok: false, ErrorCode: "tencent_instance_id_missing", Message: "Tencent TKE did not return a CVM instance id.", ProviderRequestId: stringValue(createResponse.Response.RequestId), Retryable: true}
	}
	addRequest, failure := buildAddNodeToPoolRequest(request, client.clusterId, instanceId)
	if failure != nil {
		failure.ProviderRequestId = stringValue(createResponse.Response.RequestId)
		failure.ProviderData = map[string]string{"createdInstanceId": instanceId}
		return *failure
	}
	addResponse, err := client.tkeClient.AddNodeToNodePool(addRequest)
	if err != nil {
		response := sdkErrorResponse("tencent_add_node_to_pool_failed", err)
		response.ProviderRequestId = stringValue(createResponse.Response.RequestId)
		response.ProviderData = map[string]string{"createdInstanceId": instanceId}
		return response
	}
	createRequestId := stringValue(createResponse.Response.RequestId)
	addRequestId := stringValue(addResponse.Response.RequestId)
	return Response{
		Ok:                true,
		OperationId:       "op-create-compute-" + stableSuffix(request.AccountId, request.Allocation.Id, instanceId)[:12],
		PoolId:            request.Pool.Id,
		NodePoolId:        nodePoolId,
		InstanceId:        instanceId,
		NodeName:          request.Allocation.NodeName,
		Status:            "provisioning",
		ProviderRequestId: createRequestId,
		ProviderData: map[string]string{
			"clusterId":               client.clusterId,
			"region":                  client.region,
			"createNodePoolRequestId": createNodePoolRequestId,
			"createRequestId":         createRequestId,
			"addNodeRequestId":        addRequestId,
			"instanceType":            request.Pool.InstanceType,
		},
	}
}

func (client *tencentSDKClient) DestroyComputeAllocation(request Request, _ map[string]string) Response {
	if client == nil || client.cvmClient == nil {
		return Response{Ok: false, ErrorCode: "tencent_sdk_client_missing", Message: "Tencent CVM SDK client is missing.", Retryable: false}
	}
	if strings.TrimSpace(request.Allocation.InstanceId) == "" {
		return Response{Ok: false, ErrorCode: "instance_id_required", Message: "Tencent CVM instance id is required.", Retryable: false}
	}
	terminateRequest := cvm.NewTerminateInstancesRequest()
	terminateRequest.InstanceIds = []*string{common.StringPtr(request.Allocation.InstanceId)}
	terminateRequest.ReleaseAddress = common.BoolPtr(false)
	terminateRequest.ReleasePrepaidDataDisks = common.BoolPtr(false)
	terminateResponse, err := client.cvmClient.TerminateInstances(terminateRequest)
	if err != nil {
		return sdkErrorResponse("tencent_terminate_instance_failed", err)
	}
	return Response{
		Ok:                true,
		OperationId:       "op-destroy-compute-" + stableSuffix(request.AccountId, request.Allocation.Id, request.Allocation.InstanceId)[:12],
		InstanceId:        request.Allocation.InstanceId,
		NodeName:          request.Allocation.NodeName,
		Status:            "destroyed",
		ProviderRequestId: stringValue(terminateResponse.Response.RequestId),
		ProviderData: map[string]string{
			"clusterId": client.clusterId,
			"region":    client.region,
		},
	}
}

func buildCreateClusterInstancesRequest(request Request, env map[string]string) (*tke.CreateClusterInstancesRequest, *Response) {
	missing := missingSpecificEnv(env, []string{
		"TENCENT_DEPLOY_CLUSTER_ID",
		"TENCENT_CVM_ZONE",
		"TENCENT_CVM_VPC_ID",
		"TENCENT_CVM_SUBNET_ID",
		"TENCENT_CVM_SECURITY_GROUP_IDS",
		"TENCENT_CVM_IMAGE_ID",
	})
	if len(missing) > 0 {
		return nil, &Response{
			Ok:         false,
			ErrorCode:  "tencent_cvm_env_missing",
			Message:    "Tencent CVM creation environment is incomplete.",
			MissingEnv: missing,
			Retryable:  false,
		}
	}
	if strings.TrimSpace(request.Pool.InstanceType) == "" {
		return nil, &Response{
			Ok:        false,
			ErrorCode: "instance_type_required",
			Message:   "ComputePool instanceType is required.",
			Retryable: false,
		}
	}
	if strings.TrimSpace(request.Allocation.Id) == "" {
		return nil, &Response{
			Ok:        false,
			ErrorCode: "allocation_id_required",
			Message:   "ComputeAllocation id is required.",
			Retryable: false,
		}
	}

	instanceName := "opl-" + compactName(request.Allocation.Id)
	runInstances := map[string]any{
		"Placement": map[string]any{
			"Zone": env["TENCENT_CVM_ZONE"],
		},
		"VirtualPrivateCloud": map[string]any{
			"VpcId":    env["TENCENT_CVM_VPC_ID"],
			"SubnetId": env["TENCENT_CVM_SUBNET_ID"],
		},
		"InstanceType":       request.Pool.InstanceType,
		"ImageId":            env["TENCENT_CVM_IMAGE_ID"],
		"InstanceName":       instanceName,
		"InstanceChargeType": "POSTPAID_BY_HOUR",
		"ClientToken":        instanceName,
		"SecurityGroupIds":   splitCsv(env["TENCENT_CVM_SECURITY_GROUP_IDS"]),
		"SystemDisk": map[string]any{
			"DiskType": strings.TrimSpace(defaultString(env["TENCENT_CVM_SYSTEM_DISK_TYPE"], "CLOUD_BSSD")),
			"DiskSize": intFromEnv(env, "TENCENT_CVM_SYSTEM_DISK_SIZE_GB", 80),
		},
		"Tags": ownershipTags(request),
	}
	raw, err := json.Marshal(runInstances)
	if err != nil {
		return nil, &Response{
			Ok:        false,
			ErrorCode: "run_instances_json_failed",
			Message:   err.Error(),
			Retryable: false,
		}
	}

	createRequest := tke.NewCreateClusterInstancesRequest()
	createRequest.ClusterId = common.StringPtr(env["TENCENT_DEPLOY_CLUSTER_ID"])
	createRequest.RunInstancePara = common.StringPtr(string(raw))
	createRequest.InstanceAdvancedSettings = &tke.InstanceAdvancedSettings{
		Labels: []*tke.Label{
			{Name: common.StringPtr("oplcloud.cn/compute-allocation-id"), Value: common.StringPtr(request.Allocation.Id)},
			{Name: common.StringPtr("oplcloud.cn/account-id"), Value: common.StringPtr(request.AccountId)},
			{Name: common.StringPtr("oplcloud.cn/pool-id"), Value: common.StringPtr(request.Pool.Id)},
			{Name: common.StringPtr("oplcloud.cn/package-id"), Value: common.StringPtr(request.PackageId)},
		},
	}
	return createRequest, nil
}

func buildAddNodeToPoolRequest(request Request, clusterId string, instanceId string) (*tke.AddNodeToNodePoolRequest, *Response) {
	if strings.TrimSpace(clusterId) == "" {
		return nil, &Response{Ok: false, ErrorCode: "cluster_id_required", Message: "Tencent TKE cluster id is required.", Retryable: false}
	}
	if strings.TrimSpace(request.Pool.NodePoolId) == "" {
		return nil, &Response{Ok: false, ErrorCode: "node_pool_id_required", Message: "ComputePool nodePoolId is required.", Retryable: false}
	}
	if strings.TrimSpace(instanceId) == "" {
		return nil, &Response{Ok: false, ErrorCode: "instance_id_required", Message: "Tencent CVM instance id is required.", Retryable: false}
	}
	addRequest := tke.NewAddNodeToNodePoolRequest()
	addRequest.ClusterId = common.StringPtr(clusterId)
	addRequest.NodePoolId = common.StringPtr(request.Pool.NodePoolId)
	addRequest.InstanceIds = []*string{common.StringPtr(instanceId)}
	return addRequest, nil
}

func buildCreateClusterNodePoolRequest(request Request, env map[string]string) (*tke.CreateClusterNodePoolRequest, *Response) {
	missing := missingSpecificEnv(env, []string{
		"TENCENT_DEPLOY_CLUSTER_ID",
		"TENCENT_TKE_NODE_POOL_ASG_PARA_JSON",
		"TENCENT_TKE_NODE_POOL_LAUNCH_CONFIG_JSON",
	})
	if len(missing) > 0 {
		return nil, &Response{
			Ok:         false,
			ErrorCode:  "tencent_node_pool_env_missing",
			Message:    "Tencent TKE node pool creation environment is incomplete.",
			MissingEnv: missing,
			Retryable:  false,
		}
	}
	nodePoolName := request.Pool.Id
	if strings.TrimSpace(nodePoolName) == "" {
		nodePoolName = "pool-" + request.PackageId + "-" + request.Pool.InstanceType
	}
	createRequest := tke.NewCreateClusterNodePoolRequest()
	createRequest.ClusterId = common.StringPtr(env["TENCENT_DEPLOY_CLUSTER_ID"])
	createRequest.AutoScalingGroupPara = common.StringPtr(env["TENCENT_TKE_NODE_POOL_ASG_PARA_JSON"])
	createRequest.LaunchConfigurePara = common.StringPtr(env["TENCENT_TKE_NODE_POOL_LAUNCH_CONFIG_JSON"])
	createRequest.EnableAutoscale = common.BoolPtr(true)
	createRequest.Name = common.StringPtr(nodePoolName)
	createRequest.ContainerRuntime = common.StringPtr(defaultString(env["TENCENT_TKE_NODE_POOL_CONTAINER_RUNTIME"], "containerd"))
	createRequest.RuntimeVersion = common.StringPtr(defaultString(env["TENCENT_TKE_NODE_POOL_RUNTIME_VERSION"], "1.6.9"))
	if strings.TrimSpace(env["TENCENT_TKE_NODE_POOL_OS"]) != "" {
		createRequest.NodePoolOs = common.StringPtr(env["TENCENT_TKE_NODE_POOL_OS"])
	}
	createRequest.Labels = []*tke.Label{
		{Name: common.StringPtr("oplcloud.cn/pool-id"), Value: common.StringPtr(request.Pool.Id)},
		{Name: common.StringPtr("oplcloud.cn/package-id"), Value: common.StringPtr(request.PackageId)},
		{Name: common.StringPtr("oplcloud.cn/instance-type"), Value: common.StringPtr(request.Pool.InstanceType)},
	}
	createRequest.Tags = []*tke.Tag{
		{Key: common.StringPtr("oplcloud:pool-id"), Value: common.StringPtr(request.Pool.Id)},
		{Key: common.StringPtr("oplcloud:package-id"), Value: common.StringPtr(request.PackageId)},
	}
	return createRequest, nil
}

func main() {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		writeResponse(Response{Ok: false, ErrorCode: "stdin_read_failed", Message: err.Error()})
		os.Exit(1)
	}
	var request Request
	if err := json.Unmarshal(raw, &request); err != nil {
		writeResponse(Response{Ok: false, ErrorCode: "invalid_json", Message: err.Error()})
		os.Exit(1)
	}
	env := envMap(os.Environ())
	client, setupFailure := newTencentSDKClient(env)
	if setupFailure != nil && request.Action != "readiness" {
		writeResponse(*setupFailure)
		os.Exit(1)
	}
	var provisioner TencentClient = client
	if provisioner == nil {
		provisioner = unimplementedTencentClient{}
	}
	response := handleWithClient(request, env, provisioner)
	writeResponse(response)
	if !response.Ok {
		os.Exit(1)
	}
}

func writeResponse(response Response) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(response)
}

func envMap(values []string) map[string]string {
	result := map[string]string{}
	for _, item := range values {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			result[key] = value
		}
	}
	return result
}

func handle(request Request, env map[string]string) Response {
	return handleWithClient(request, env, unimplementedTencentClient{})
}

func handleWithClient(request Request, env map[string]string, client TencentClient) Response {
	missing := missingEnv(env)
	if request.Action == "readiness" {
		if len(missing) > 0 {
			return Response{
				Ok:         false,
				ErrorCode:  "tencent_env_missing",
				Message:    "Tencent Cloud provisioner environment is incomplete.",
				MissingEnv: missing,
				Retryable:  false,
			}
		}
		return Response{Ok: true, Status: "ready"}
	}
	if len(missing) > 0 {
		return Response{
			Ok:         false,
			ErrorCode:  "tencent_env_missing",
			Message:    "Tencent Cloud provisioner environment is incomplete.",
			MissingEnv: missing,
			Retryable:  false,
		}
	}

	switch request.Action {
	case "create_compute_allocation":
		if request.DryRun {
			return dryRunCreateComputeAllocation(request, env)
		}
		return client.CreateComputeAllocation(request, env)
	case "destroy_compute_allocation":
		if request.DryRun {
			return dryRunDestroyComputeAllocation(request)
		}
		return client.DestroyComputeAllocation(request, env)
	default:
		return Response{
			Ok:        false,
			ErrorCode: "unknown_action",
			Message:   fmt.Sprintf("Unknown provisioner action: %s", request.Action),
			Retryable: false,
		}
	}
}

func missingEnv(env map[string]string) []string {
	var missing []string
	for _, key := range requiredTencentEnv {
		if strings.TrimSpace(env[key]) == "" {
			missing = append(missing, key)
		}
	}
	return missing
}

func dryRunCreateComputeAllocation(request Request, env map[string]string) Response {
	stable := stableSuffix(request.AccountId, request.UserId, request.PackageId, request.Pool.Id, request.Allocation.Id)
	nodePoolId := request.Pool.NodePoolId
	if nodePoolId == "" {
		nodePoolId = "np-" + stable[:8]
	}
	instanceId := request.Allocation.InstanceId
	if instanceId == "" {
		instanceId = "ins-" + stable[:12]
	}
	nodeName := request.Allocation.NodeName
	if nodeName == "" {
		nodeName = "node-" + stable[:10]
	}
	return Response{
		Ok:          true,
		OperationId: "op-create-compute-" + stable[:12],
		PoolId:      request.Pool.Id,
		NodePoolId:  nodePoolId,
		InstanceId:  instanceId,
		NodeName:    nodeName,
		Status:      "provisioning",
		ProviderData: map[string]string{
			"accountId":       request.AccountId,
			"userId":          request.UserId,
			"packageId":       request.PackageId,
			"clusterId":       env["TENCENT_DEPLOY_CLUSTER_ID"],
			"region":          env["TENCENTCLOUD_REGION"],
			"instanceType":    request.Pool.InstanceType,
			"provisionerMode": "dry-run",
		},
	}
}

func dryRunDestroyComputeAllocation(request Request) Response {
	stable := stableSuffix(request.AccountId, request.Allocation.Id, request.Allocation.InstanceId)
	return Response{
		Ok:          true,
		OperationId: "op-destroy-compute-" + stable[:12],
		PoolId:      request.Pool.Id,
		NodePoolId:  request.Pool.NodePoolId,
		InstanceId:  request.Allocation.InstanceId,
		NodeName:    request.Allocation.NodeName,
		Status:      "destroyed",
		ProviderData: map[string]string{
			"accountId":       request.AccountId,
			"provisionerMode": "dry-run",
		},
	}
}

func stableSuffix(parts ...string) string {
	hash := sha1.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func missingSpecificEnv(env map[string]string, keys []string) []string {
	var missing []string
	for _, key := range keys {
		if strings.TrimSpace(env[key]) == "" {
			missing = append(missing, key)
		}
	}
	return missing
}

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func intFromEnv(env map[string]string, key string, fallback int) int {
	if strings.TrimSpace(env[key]) == "" {
		return fallback
	}
	value, err := strconv.Atoi(env[key])
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func splitCsv(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func compactName(value string) string {
	value = strings.ToLower(value)
	var builder strings.Builder
	lastDash := false
	for _, char := range value {
		isAlphaNum := (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9')
		if isAlphaNum {
			builder.WriteRune(char)
			lastDash = false
			continue
		}
		if !lastDash && builder.Len() > 0 {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(strings.TrimSpace(builder.String()), "-")
}

func ownershipTags(request Request) []map[string]string {
	return []map[string]string{
		{"Key": "oplcloud:account-id", "Value": request.AccountId},
		{"Key": "oplcloud:user-id", "Value": request.UserId},
		{"Key": "oplcloud:package-id", "Value": request.PackageId},
		{"Key": "oplcloud:pool-id", "Value": request.Pool.Id},
		{"Key": "oplcloud:compute-allocation-id", "Value": request.Allocation.Id},
	}
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func firstString(values []*string) string {
	if len(values) == 0 || values[0] == nil {
		return ""
	}
	return *values[0]
}

func sdkErrorResponse(code string, err error) Response {
	return Response{
		Ok:        false,
		ErrorCode: code,
		Message:   err.Error(),
		Retryable: true,
	}
}
