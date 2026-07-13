package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	cbs2017 "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cbs/v20170312"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	tcerrors "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/errors"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	cvm2017 "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cvm/v20170312"
	tke2022 "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/tke/v20220501"
	vpc2017 "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/vpc/v20170312"
)

var requiredTencentEnv = []string{
	"TENCENTCLOUD_SECRET_ID",
	"TENCENTCLOUD_SECRET_KEY",
	"TENCENTCLOUD_REGION",
	"TENCENT_DEPLOY_CLUSTER_ID",
}

var errCVMInstanceNotFound = errors.New("CVM instance not found")
var errTKEInstanceNotFound = errors.New("TKE instance not found")

type Request struct {
	Action          string                 `json:"action"`
	DryRun          bool                   `json:"dryRun,omitempty"`
	AccountId       string                 `json:"accountId,omitempty"`
	UserId          string                 `json:"userId,omitempty"`
	PackageId       string                 `json:"packageId,omitempty"`
	StorageVolumeId string                 `json:"storageVolumeId,omitempty"`
	Tags            map[string]string      `json:"tags,omitempty"`
	Pool            ComputePoolInput       `json:"pool,omitempty"`
	Allocation      ComputeAllocationInput `json:"allocation,omitempty"`
}

type ComputePoolInput struct {
	Id                string            `json:"id,omitempty"`
	ClusterId         string            `json:"clusterId,omitempty"`
	PackageId         string            `json:"packageId,omitempty"`
	InstanceType      string            `json:"instanceType,omitempty"`
	NodePoolId        string            `json:"nodePoolId,omitempty"`
	DesiredNodeLabels map[string]string `json:"desiredNodeLabels,omitempty"`
	DesiredReplicas   int64             `json:"desiredReplicas,omitempty"`
}

type ComputeAllocationInput struct {
	Id          string `json:"id,omitempty"`
	InstanceId  string `json:"instanceId,omitempty"`
	MachineName string `json:"machineName,omitempty"`
	NodeName    string `json:"nodeName,omitempty"`
	PrivateIp   string `json:"privateIp,omitempty"`
	PublicIp    string `json:"publicIp,omitempty"`
}

type Response struct {
	Ok                bool              `json:"ok"`
	OperationId       string            `json:"operationId,omitempty"`
	PoolId            string            `json:"poolId,omitempty"`
	NodePoolId        string            `json:"nodePoolId,omitempty"`
	InstanceId        string            `json:"instanceId,omitempty"`
	NodeName          string            `json:"nodeName,omitempty"`
	PrivateIp         string            `json:"privateIp,omitempty"`
	MachinePresent    *bool             `json:"machinePresent,omitempty"`
	StoragePresent    *bool             `json:"storagePresent,omitempty"`
	CVMStatus         string            `json:"cvmStatus,omitempty"`
	TKEStatus         string            `json:"tkeStatus,omitempty"`
	CBSStatus         string            `json:"cbsStatus,omitempty"`
	PublicIp          string            `json:"publicIp,omitempty"`
	Status            string            `json:"status,omitempty"`
	ProviderRequestId string            `json:"providerRequestId,omitempty"`
	ProviderData      map[string]string `json:"providerData,omitempty"`
	ErrorCode         string            `json:"errorCode,omitempty"`
	Message           string            `json:"message,omitempty"`
	Retryable         bool              `json:"retryable,omitempty"`
	MissingEnv        []string          `json:"missingEnv,omitempty"`
	Machines          []MachineOutput   `json:"machines,omitempty"`
	InstanceType      string            `json:"instanceType,omitempty"`
	InstanceAvailable bool              `json:"instanceAvailable,omitempty"`
	RequiredCapacity  int64             `json:"requiredCapacity,omitempty"`
	RemainingQuota    uint64            `json:"remainingQuota,omitempty"`
	CurrentReplicas   int64             `json:"currentReplicas,omitempty"`
	ReadyReplicas     int64             `json:"readyReplicas,omitempty"`
	MaxReplicas       int64             `json:"maxReplicas,omitempty"`
	TargetReplicas    int64             `json:"targetReplicas,omitempty"`
	MachineType       string            `json:"machineType,omitempty"`
	Zones             []string          `json:"zones,omitempty"`
}

type MachineOutput struct {
	MachineId    string `json:"machineId"`
	InstanceId   string `json:"instanceId,omitempty"`
	NodeName     string `json:"nodeName,omitempty"`
	PrivateIp    string `json:"privateIp,omitempty"`
	PublicIp     string `json:"publicIp,omitempty"`
	InstanceType string `json:"instanceType,omitempty"`
	Ready        bool   `json:"ready"`
}

type TencentClient interface {
	Capacity(request Request, env map[string]string) Response
	ProviderTruth(request Request, env map[string]string) Response
	CreateComputeAllocation(request Request, env map[string]string) Response
	ReconcileComputePool(request Request, env map[string]string) Response
	TagComputeMachine(request Request, env map[string]string) Response
	SyncComputeAllocation(request Request, env map[string]string) Response
	DestroyComputeAllocation(request Request, env map[string]string) Response
}

type unimplementedTencentClient struct{}

type tencentSDKClient struct {
	region          string
	clusterId       string
	nativeTkeClient tkeNativeAPI
	nativeCvmClient cvmNativeAPI
	nativeCbsClient cbsNativeAPI
	nativeVpcClient vpcNativeAPI
}

type tkeNativeAPI interface {
	CreateNodePool(request *tke2022.CreateNodePoolRequest) (*tke2022.CreateNodePoolResponse, error)
	DescribeClusterInstances(request *tke2022.DescribeClusterInstancesRequest) (*tke2022.DescribeClusterInstancesResponse, error)
	DescribeClusterMachines(request *tke2022.DescribeClusterMachinesRequest) (*tke2022.DescribeClusterMachinesResponse, error)
	DescribeNodePools(request *tke2022.DescribeNodePoolsRequest) (*tke2022.DescribeNodePoolsResponse, error)
	ModifyNodePool(request *tke2022.ModifyNodePoolRequest) (*tke2022.ModifyNodePoolResponse, error)
	ScaleNodePool(request *tke2022.ScaleNodePoolRequest) (*tke2022.ScaleNodePoolResponse, error)
	DeleteClusterMachines(request *tke2022.DeleteClusterMachinesRequest) (*tke2022.DeleteClusterMachinesResponse, error)
}

type cvmNativeAPI interface {
	DescribeAccountQuota(request *cvm2017.DescribeAccountQuotaRequest) (*cvm2017.DescribeAccountQuotaResponse, error)
	DescribeInstances(request *cvm2017.DescribeInstancesRequest) (*cvm2017.DescribeInstancesResponse, error)
	DescribeZoneInstanceConfigInfos(request *cvm2017.DescribeZoneInstanceConfigInfosRequest) (*cvm2017.DescribeZoneInstanceConfigInfosResponse, error)
	ModifyInstancesAttribute(request *cvm2017.ModifyInstancesAttributeRequest) (*cvm2017.ModifyInstancesAttributeResponse, error)
}

type cbsNativeAPI interface {
	DescribeDisks(request *cbs2017.DescribeDisksRequest) (*cbs2017.DescribeDisksResponse, error)
}

type vpcNativeAPI interface {
	DescribeSubnets(request *vpc2017.DescribeSubnetsRequest) (*vpc2017.DescribeSubnetsResponse, error)
}

func (unimplementedTencentClient) Capacity(_ Request, _ map[string]string) Response {
	return Response{Ok: false, ErrorCode: "tencent_live_not_implemented", Message: "Tencent live capacity preflight is not implemented in this build.", Retryable: false}
}

func (unimplementedTencentClient) ProviderTruth(_ Request, _ map[string]string) Response {
	return Response{Ok: false, ErrorCode: "tencent_live_not_implemented", Message: "Tencent live provider truth is not implemented in this build.", Retryable: false}
}

func (unimplementedTencentClient) CreateComputeAllocation(_ Request, _ map[string]string) Response {
	return Response{
		Ok:        false,
		ErrorCode: "tencent_live_not_implemented",
		Message:   "Tencent live compute allocation is not implemented in this build.",
		Retryable: false,
	}
}

func (unimplementedTencentClient) ReconcileComputePool(_ Request, _ map[string]string) Response {
	return Response{Ok: false, ErrorCode: "tencent_live_not_implemented", Message: "Tencent live compute pool reconciliation is not implemented in this build.", Retryable: false}
}

func (unimplementedTencentClient) TagComputeMachine(_ Request, _ map[string]string) Response {
	return Response{Ok: false, ErrorCode: "tencent_live_not_implemented", Message: "Tencent live compute machine tagging is not implemented in this build.", Retryable: false}
}

func (unimplementedTencentClient) DestroyComputeAllocation(_ Request, _ map[string]string) Response {
	return Response{
		Ok:        false,
		ErrorCode: "tencent_live_not_implemented",
		Message:   "Tencent live compute allocation destroy is not implemented in this build.",
		Retryable: false,
	}
}

func (unimplementedTencentClient) SyncComputeAllocation(_ Request, _ map[string]string) Response {
	return Response{
		Ok:        false,
		ErrorCode: "tencent_live_not_implemented",
		Message:   "Tencent live compute allocation sync is not implemented in this build.",
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
	tkeClient, err := tke2022.NewClient(credential, env["TENCENTCLOUD_REGION"], tkeProfile)
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
	cvmClient, err := cvm2017.NewClient(credential, env["TENCENTCLOUD_REGION"], cvmProfile)
	if err != nil {
		return nil, &Response{
			Ok:        false,
			ErrorCode: "tencent_sdk_client_failed",
			Message:   err.Error(),
			Retryable: false,
		}
	}
	cbsProfile := profile.NewClientProfile()
	cbsProfile.HttpProfile.Endpoint = "cbs.tencentcloudapi.com"
	cbsClient, err := cbs2017.NewClient(credential, env["TENCENTCLOUD_REGION"], cbsProfile)
	if err != nil {
		return nil, &Response{Ok: false, ErrorCode: "tencent_sdk_client_failed", Message: err.Error(), Retryable: false}
	}
	vpcProfile := profile.NewClientProfile()
	vpcProfile.HttpProfile.Endpoint = "vpc.tencentcloudapi.com"
	vpcClient, err := vpc2017.NewClient(credential, env["TENCENTCLOUD_REGION"], vpcProfile)
	if err != nil {
		return nil, &Response{Ok: false, ErrorCode: "tencent_sdk_client_failed", Message: err.Error(), Retryable: false}
	}

	return &tencentSDKClient{
		region:          env["TENCENTCLOUD_REGION"],
		clusterId:       env["TENCENT_DEPLOY_CLUSTER_ID"],
		nativeTkeClient: tkeClient,
		nativeCvmClient: cvmClient,
		nativeCbsClient: cbsClient,
		nativeVpcClient: vpcClient,
	}, nil
}

func capacityFailure(code string, err error) Response {
	message := code
	if err != nil {
		message = err.Error()
	}
	return Response{Ok: false, ErrorCode: code, Message: message, Retryable: false}
}

func containsString(values []*string, expected string) bool {
	for _, current := range values {
		if stringValue(current) == expected {
			return true
		}
	}
	return false
}

func isCVMNativeNodePool(pool *tke2022.NodePool) bool {
	return pool != nil && stringValue(pool.Type) == "Native" && pool.Native != nil &&
		stringValue(pool.Native.MachineType) == "NativeCVM" && stringValue(pool.Native.InstanceChargeType) == "POSTPAID_BY_HOUR"
}

func mutationNodePoolFailure(pool *tke2022.NodePool, request Request, requestID string) *Response {
	if !isCVMNativeNodePool(pool) {
		return &Response{Ok: false, ErrorCode: "tencent_cvm_node_pool_required", Message: "Fabric compute requires a POSTPAID NativeCVM node pool.", ProviderRequestId: requestID, Retryable: false}
	}
	lifeState := strings.TrimSpace(stringValue(pool.LifeState))
	if strings.EqualFold(lifeState, "Creating") {
		return &Response{Ok: false, ErrorCode: "tencent_node_pool_not_ready", Message: "Tencent node pool is still being created.", ProviderRequestId: requestID, Retryable: true}
	}
	if !strings.EqualFold(lifeState, "Running") || !matchesCapacityNodePool(pool, request) {
		return &Response{Ok: false, ErrorCode: "tencent_node_pool_ownership_mismatch", Message: "Node pool does not match the requested ownership labels.", ProviderRequestId: requestID, Retryable: false}
	}
	requiredReplicas := request.Pool.DesiredReplicas
	if request.Allocation.Id != "" {
		requiredReplicas = nativeReplicas(pool) + 1
	}
	if len(pool.Native.InstanceTypes) != 1 || stringValue(pool.Native.InstanceTypes[0]) != request.Pool.InstanceType ||
		pool.Native.EnableAutoscaling == nil || *pool.Native.EnableAutoscaling ||
		pool.Native.AutoRepair == nil || *pool.Native.AutoRepair || pool.Native.Replicas == nil ||
		pool.Native.Scaling == nil || pool.Native.Scaling.MaxReplicas == nil || *pool.Native.Scaling.MaxReplicas < requiredReplicas {
		return &Response{Ok: false, ErrorCode: "tencent_node_pool_configuration_mismatch", Message: "Node pool runtime configuration does not match the requested Fabric mutation.", ProviderRequestId: requestID, Retryable: false}
	}
	return nil
}

func matchesCapacityNodePool(pool *tke2022.NodePool, request Request) bool {
	labels := nodePoolLabels(pool)
	return request.Pool.Id != "" && request.PackageId != "" && request.Pool.InstanceType != "" &&
		labels["oplcloud.cn/pool-id"] == request.Pool.Id &&
		labels["oplcloud.cn/package-id"] == request.PackageId &&
		labels["oplcloud.cn/instance-type"] == request.Pool.InstanceType
}

func (client *tencentSDKClient) capacityNodePool(request Request) (*tke2022.NodePool, string, error) {
	if strings.TrimSpace(request.Pool.NodePoolId) != "" {
		describe := tke2022.NewDescribeNodePoolsRequest()
		describe.ClusterId = common.StringPtr(client.clusterId)
		describe.Limit = common.Int64Ptr(100)
		describe.Filters = []*tke2022.Filter{{Name: common.StringPtr("NodePoolsId"), Values: []*string{common.StringPtr(request.Pool.NodePoolId)}}}
		response, err := client.nativeTkeClient.DescribeNodePools(describe)
		if err != nil {
			return nil, "", err
		}
		if response == nil || response.Response == nil || response.Response.TotalCount == nil || *response.Response.TotalCount != 1 ||
			len(response.Response.NodePools) != 1 || stringValue(response.Response.NodePools[0].NodePoolId) != request.Pool.NodePoolId ||
			!matchesCapacityNodePool(response.Response.NodePools[0], request) {
			return nil, "", fmt.Errorf("explicit node pool ownership mismatch")
		}
		return response.Response.NodePools[0], stringValue(response.Response.RequestId), nil
	}
	describe := tke2022.NewDescribeNodePoolsRequest()
	describe.ClusterId = common.StringPtr(client.clusterId)
	describe.Limit = common.Int64Ptr(100)
	response, err := client.nativeTkeClient.DescribeNodePools(describe)
	if err != nil {
		return nil, "", err
	}
	if response == nil || response.Response == nil || response.Response.TotalCount == nil || *response.Response.TotalCount != int64(len(response.Response.NodePools)) {
		return nil, "", fmt.Errorf("node pool discovery is incomplete")
	}
	matches := []*tke2022.NodePool{}
	for _, pool := range response.Response.NodePools {
		if matchesCapacityNodePool(pool, request) {
			matches = append(matches, pool)
		}
	}
	requestID := stringValue(response.Response.RequestId)
	if len(matches) != 1 {
		return nil, requestID, fmt.Errorf("capacity preflight requires exactly one matching node pool")
	}
	return matches[0], requestID, nil
}

func (client *tencentSDKClient) Capacity(request Request, _ map[string]string) Response {
	required := request.Pool.DesiredReplicas
	if client == nil || client.nativeTkeClient == nil || client.nativeCvmClient == nil || client.nativeVpcClient == nil {
		return capacityFailure("tencent_capacity_client_missing", nil)
	}
	if required <= 0 || strings.TrimSpace(request.Pool.InstanceType) == "" ||
		(strings.TrimSpace(request.Pool.NodePoolId) == "" && (strings.TrimSpace(request.Pool.Id) == "" || strings.TrimSpace(request.PackageId) == "")) {
		return capacityFailure("tencent_capacity_input_invalid", nil)
	}
	pool, nodePoolRequestId, err := client.capacityNodePool(request)
	if err != nil {
		return capacityFailure("tencent_capacity_node_pool_unavailable", err)
	}
	native := pool.Native
	if !isCVMNativeNodePool(pool) || strings.TrimSpace(stringValue(pool.LifeState)) != "Running" || native.Scaling == nil || native.Scaling.MaxReplicas == nil ||
		native.Replicas == nil || native.ReadyReplicas == nil || native.EnableAutoscaling == nil || native.AutoRepair == nil ||
		*native.EnableAutoscaling || *native.AutoRepair || *native.ReadyReplicas != *native.Replicas ||
		*native.Scaling.MaxReplicas < *native.Replicas+required || !containsString(native.InstanceTypes, request.Pool.InstanceType) || len(native.SubnetIds) == 0 {
		return capacityFailure("tencent_capacity_node_pool_unavailable", nil)
	}
	subnetIds := []string{}
	seenSubnetIds := map[string]bool{}
	for _, raw := range native.SubnetIds {
		subnetId := strings.TrimSpace(stringValue(raw))
		if subnetId == "" || seenSubnetIds[subnetId] {
			return capacityFailure("tencent_capacity_subnet_invalid", nil)
		}
		seenSubnetIds[subnetId] = true
		subnetIds = append(subnetIds, subnetId)
	}
	subnetRequest := vpc2017.NewDescribeSubnetsRequest()
	subnetRequest.SubnetIds = stringsToPtrs(subnetIds)
	subnetResponse, err := client.nativeVpcClient.DescribeSubnets(subnetRequest)
	if err != nil || subnetResponse == nil || subnetResponse.Response == nil {
		return capacityFailure("tencent_capacity_subnet_describe_failed", err)
	}
	zones := []string{}
	seenZones := map[string]bool{}
	foundSubnets := map[string]bool{}
	for _, subnet := range subnetResponse.Response.SubnetSet {
		if subnet == nil || !seenSubnetIds[stringValue(subnet.SubnetId)] || foundSubnets[stringValue(subnet.SubnetId)] || subnet.AvailableIpAddressCount == nil || int64(*subnet.AvailableIpAddressCount) < required {
			return capacityFailure("tencent_capacity_subnet_unavailable", nil)
		}
		zone := strings.TrimSpace(stringValue(subnet.Zone))
		if zone == "" {
			return capacityFailure("tencent_capacity_subnet_unavailable", nil)
		}
		foundSubnets[stringValue(subnet.SubnetId)] = true
		if !seenZones[zone] {
			seenZones[zone] = true
			zones = append(zones, zone)
		}
	}
	if len(foundSubnets) != len(subnetIds) {
		return capacityFailure("tencent_capacity_subnet_unavailable", nil)
	}
	minimumRemainingQuota := ^uint64(0)
	for _, zone := range zones {
		availabilityRequest := cvm2017.NewDescribeZoneInstanceConfigInfosRequest()
		availabilityRequest.Filters = []*cvm2017.Filter{
			{Name: common.StringPtr("zone"), Values: []*string{common.StringPtr(zone)}},
			{Name: common.StringPtr("instance-type"), Values: []*string{common.StringPtr(request.Pool.InstanceType)}},
			{Name: common.StringPtr("instance-charge-type"), Values: []*string{common.StringPtr("POSTPAID_BY_HOUR")}},
		}
		availability, err := client.nativeCvmClient.DescribeZoneInstanceConfigInfos(availabilityRequest)
		if err != nil || availability == nil || availability.Response == nil {
			return capacityFailure("tencent_capacity_instance_describe_failed", err)
		}
		exactAvailability := 0
		sell := false
		for _, item := range availability.Response.InstanceTypeQuotaSet {
			if item != nil && stringValue(item.Zone) == zone && stringValue(item.InstanceType) == request.Pool.InstanceType && stringValue(item.InstanceChargeType) == "POSTPAID_BY_HOUR" {
				exactAvailability++
				sell = stringValue(item.Status) == "SELL"
			}
		}
		if exactAvailability != 1 || !sell {
			return capacityFailure("tencent_capacity_instance_unavailable", nil)
		}
		quotaRequest := cvm2017.NewDescribeAccountQuotaRequest()
		quotaRequest.Filters = []*cvm2017.Filter{
			{Name: common.StringPtr("zone"), Values: []*string{common.StringPtr(zone)}},
			{Name: common.StringPtr("quota-type"), Values: []*string{common.StringPtr("PostPaidQuotaSet")}},
		}
		quota, err := client.nativeCvmClient.DescribeAccountQuota(quotaRequest)
		if err != nil || quota == nil || quota.Response == nil || quota.Response.AccountQuotaOverview == nil || quota.Response.AccountQuotaOverview.AccountQuota == nil {
			return capacityFailure("tencent_capacity_quota_describe_failed", err)
		}
		remaining := (*uint64)(nil)
		exactQuotas := 0
		for _, item := range quota.Response.AccountQuotaOverview.AccountQuota.PostPaidQuotaSet {
			if item != nil && stringValue(item.Zone) == zone {
				exactQuotas++
				remaining = item.RemainingQuota
			}
		}
		if exactQuotas != 1 || remaining == nil || *remaining < uint64(required) {
			return capacityFailure("tencent_capacity_quota_insufficient", nil)
		}
		if *remaining < minimumRemainingQuota {
			minimumRemainingQuota = *remaining
		}
	}
	return Response{
		Ok: true, Status: "ready", ProviderRequestId: nodePoolRequestId, NodePoolId: stringValue(pool.NodePoolId),
		InstanceType: request.Pool.InstanceType, InstanceAvailable: true, RequiredCapacity: required,
		RemainingQuota: minimumRemainingQuota, CurrentReplicas: *native.Replicas, ReadyReplicas: *native.ReadyReplicas,
		MaxReplicas: *native.Scaling.MaxReplicas, TargetReplicas: *native.Replicas + required, MachineType: stringValue(native.MachineType), Zones: zones,
	}
}

func (client *tencentSDKClient) CreateComputeAllocation(request Request, env map[string]string) Response {
	if client == nil || client.nativeTkeClient == nil {
		return Response{Ok: false, ErrorCode: "tencent_sdk_client_missing", Message: "Tencent TKE SDK client is missing.", Retryable: false}
	}
	nodePoolId := request.Pool.NodePoolId
	createNodePoolRequestId := ""
	describeRequestId := ""
	var pool *tke2022.NodePool
	if nodePoolId != "" {
		describedPool, requestId, err := client.describeNativeNodePool(nodePoolId)
		if err != nil {
			return sdkErrorResponse("tencent_describe_node_pool_failed", err)
		}
		if failure := mutationNodePoolFailure(describedPool, request, requestId); failure != nil {
			return *failure
		}
		pool = describedPool
		describeRequestId = requestId
	}
	if nodePoolId == "" {
		discoveredPool, requestId, err := client.discoverNativeNodePool(request)
		if err != nil {
			return sdkErrorResponse("tencent_describe_node_pool_failed", err)
		}
		if discoveredPool != nil {
			pool = discoveredPool
			nodePoolId = stringValue(discoveredPool.NodePoolId)
			describeRequestId = requestId
		}
	}
	if nodePoolId == "" {
		createNodePoolRequest, failure := buildCreateNativeNodePoolRequest(request, env)
		if failure != nil {
			return *failure
		}
		createNodePoolResponse, err := client.nativeTkeClient.CreateNodePool(createNodePoolRequest)
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

	if pool == nil {
		describedPool, requestId, err := client.describeNativeNodePool(nodePoolId)
		if err != nil {
			response := sdkErrorResponse("tencent_describe_node_pool_failed", err)
			response.ProviderRequestId = createNodePoolRequestId
			return response
		}
		pool = describedPool
		describeRequestId = requestId
	}
	if failure := mutationNodePoolFailure(pool, request, describeRequestId); failure != nil {
		return *failure
	}
	modifySelfProvisioningRequestId := ""
	if nativeSelfProvisioningEnabled(pool) {
		requestId, err := client.disableNativeNodePoolSelfProvisioning(nodePoolId)
		if err != nil {
			response := sdkErrorResponse("tencent_disable_node_pool_self_provisioning_failed", err)
			response.ProviderRequestId = describeRequestId
			return response
		}
		modifySelfProvisioningRequestId = requestId
		pool.Native.EnableAutoscaling = common.BoolPtr(false)
		pool.Native.AutoRepair = common.BoolPtr(false)
	}
	currentReplicas := nativeReplicas(pool)
	beforeMachines, beforeMachinesRequestId, err := client.describeClusterMachines(nodePoolId)
	if err != nil {
		response := sdkErrorResponse("tencent_describe_cluster_machines_failed", err)
		response.ProviderRequestId = describeRequestId
		return response
	}
	targetReplicas := currentReplicas + 1
	scaleRequest := tke2022.NewScaleNodePoolRequest()
	scaleRequest.ClusterId = common.StringPtr(client.clusterId)
	scaleRequest.NodePoolId = common.StringPtr(nodePoolId)
	scaleRequest.Replicas = common.Int64Ptr(targetReplicas)
	scaleResponse, err := client.nativeTkeClient.ScaleNodePool(scaleRequest)
	if err != nil {
		response := sdkErrorResponse("tencent_scale_node_pool_failed", err)
		response.ProviderRequestId = describeRequestId
		return response
	}
	scaleRequestId := stringValue(scaleResponse.Response.RequestId)
	machine, machineRequestId, err := client.waitForNewPoolMachine(nodePoolId, beforeMachines, request, env)
	if err != nil {
		return Response{
			Ok:                false,
			ErrorCode:         "compute_allocation_node_identity_required",
			Message:           "Tencent TKE did not return a dedicated node for this compute allocation.",
			ProviderRequestId: firstNonEmpty(machineRequestId, scaleRequestId),
			Retryable:         true,
			ProviderData: map[string]string{
				"clusterId":                   client.clusterId,
				"region":                      client.region,
				"createNodePoolRequestId":     createNodePoolRequestId,
				"describeNodePoolRequestId":   describeRequestId,
				"modifySelfProvisioningReqId": modifySelfProvisioningRequestId,
				"describeMachinesBeforeReqId": beforeMachinesRequestId,
				"describeMachinesLatestReqId": machineRequestId,
				"scaleNodePoolRequestId":      scaleRequestId,
				"instanceType":                request.Pool.InstanceType,
				"replicasBefore":              fmt.Sprintf("%d", currentReplicas),
				"replicasAfter":               fmt.Sprintf("%d", targetReplicas),
			},
		}
	}
	machineName := stringValue(machine.MachineName)
	privateIp := stringValue(machine.LanIP)
	instanceId := ""
	publicIp := ""
	instanceIdentitySource := ""
	cvmInstance, cvmRequestId, err := client.describeCvmInstanceByPrivateIp(privateIp)
	if err == nil {
		instanceId = stringValue(cvmInstance.InstanceId)
		publicIp = firstString(cvmInstance.PublicIpAddresses)
		instanceIdentitySource = "cvm"
	} else if errors.Is(err, errCVMInstanceNotFound) {
		return Response{Ok: false, ErrorCode: "compute_cvm_identity_required", Message: "NativeCVM allocation did not resolve to a Tencent CVM instance.", ProviderRequestId: firstNonEmpty(cvmRequestId, machineRequestId, scaleRequestId), Retryable: true}
	} else {
		response := sdkErrorResponse("tencent_describe_cvm_instance_failed", err)
		response.ProviderRequestId = firstNonEmpty(cvmRequestId, machineRequestId, scaleRequestId)
		return response
	}
	nodeName := kubernetesNodeName(machine)
	if nodeName == "" && machineName == "" {
		return Response{
			Ok:                false,
			ErrorCode:         "compute_allocation_node_identity_required",
			Message:           "Tencent TKE did not return a node identity for this compute allocation.",
			ProviderRequestId: firstNonEmpty(cvmRequestId, machineRequestId, scaleRequestId),
			Retryable:         true,
		}
	}
	return Response{
		Ok:                true,
		OperationId:       "op-create-compute-" + stableSuffix(request.AccountId, request.Allocation.Id, nodePoolId, fmt.Sprintf("%d", targetReplicas))[:12],
		PoolId:            request.Pool.Id,
		NodePoolId:        nodePoolId,
		InstanceId:        instanceId,
		NodeName:          nodeName,
		PrivateIp:         privateIp,
		PublicIp:          publicIp,
		Status:            "running",
		ProviderRequestId: scaleRequestId,
		ProviderData: map[string]string{
			"clusterId":                   client.clusterId,
			"region":                      client.region,
			"createNodePoolRequestId":     createNodePoolRequestId,
			"describeNodePoolRequestId":   describeRequestId,
			"modifySelfProvisioningReqId": modifySelfProvisioningRequestId,
			"describeMachinesBeforeReqId": beforeMachinesRequestId,
			"describeMachinesReadyReqId":  machineRequestId,
			"describeCvmRequestId":        cvmRequestId,
			"scaleNodePoolRequestId":      scaleRequestId,
			"instanceType":                request.Pool.InstanceType,
			"replicasBefore":              fmt.Sprintf("%d", currentReplicas),
			"replicasAfter":               fmt.Sprintf("%d", targetReplicas),
			"instanceId":                  instanceId,
			"instanceIdentitySource":      instanceIdentitySource,
			"machineName":                 machineName,
			"nodeName":                    nodeName,
			"privateIp":                   privateIp,
			"publicIp":                    publicIp,
		},
	}
}

func (client *tencentSDKClient) ReconcileComputePool(request Request, env map[string]string) Response {
	if client == nil || client.nativeTkeClient == nil {
		return Response{Ok: false, ErrorCode: "tencent_sdk_client_missing", Message: "Tencent TKE SDK client is missing.", Retryable: false}
	}
	if request.Pool.DesiredReplicas < 0 {
		return Response{Ok: false, ErrorCode: "desired_replicas_invalid", Message: "Node pool desired replicas cannot be negative.", Retryable: false}
	}
	nodePoolId := request.Pool.NodePoolId
	var pool *tke2022.NodePool
	requestId := ""
	describeNodePoolRequestId := ""
	if nodePoolId != "" {
		described, id, err := client.describeNativeNodePool(nodePoolId)
		if err != nil {
			return sdkErrorResponse("tencent_describe_node_pool_failed", err)
		}
		if failure := mutationNodePoolFailure(described, request, id); failure != nil {
			return *failure
		}
		pool, requestId = described, id
		describeNodePoolRequestId = id
	}
	if pool == nil {
		discovered, id, err := client.discoverNativeNodePool(request)
		if err != nil {
			return sdkErrorResponse("tencent_describe_node_pool_failed", err)
		}
		if discovered != nil {
			pool, requestId = discovered, id
			describeNodePoolRequestId = id
			nodePoolId = stringValue(discovered.NodePoolId)
		}
	}
	if pool == nil && request.Pool.DesiredReplicas == 0 {
		return Response{Ok: true, PoolId: request.Pool.Id, Status: "ready", ProviderRequestId: requestId}
	}
	if pool == nil {
		createRequest, failure := buildCreateNativeNodePoolRequest(request, env)
		if failure != nil {
			return *failure
		}
		created, err := client.nativeTkeClient.CreateNodePool(createRequest)
		if err != nil {
			return sdkErrorResponse("tencent_create_node_pool_failed", err)
		}
		nodePoolId = stringValue(created.Response.NodePoolId)
		requestId = stringValue(created.Response.RequestId)
		if nodePoolId == "" {
			return Response{Ok: false, ErrorCode: "tencent_node_pool_id_missing", Message: "Tencent TKE did not return a node pool id.", ProviderRequestId: requestId, Retryable: true}
		}
		described, id, err := client.describeNativeNodePool(nodePoolId)
		if err != nil {
			response := sdkErrorResponse("tencent_describe_node_pool_failed", err)
			response.ProviderRequestId = requestId
			return response
		}
		pool = described
		requestId = firstNonEmpty(id, requestId)
		describeNodePoolRequestId = id
	}
	if failure := mutationNodePoolFailure(pool, request, describeNodePoolRequestId); failure != nil {
		return *failure
	}
	current := nativeReplicas(pool)
	scaleRequestId := ""
	if current != request.Pool.DesiredReplicas {
		scaleRequest := tke2022.NewScaleNodePoolRequest()
		scaleRequest.ClusterId = common.StringPtr(client.clusterId)
		scaleRequest.NodePoolId = common.StringPtr(nodePoolId)
		scaleRequest.Replicas = common.Int64Ptr(request.Pool.DesiredReplicas)
		scaled, err := client.nativeTkeClient.ScaleNodePool(scaleRequest)
		if err != nil {
			response := sdkErrorResponse("tencent_scale_node_pool_failed", err)
			response.ProviderRequestId = requestId
			response.PoolId = request.Pool.Id
			response.NodePoolId = nodePoolId
			response.ProviderData["nodePoolId"] = nodePoolId
			response.ProviderData["currentReplicas"] = fmt.Sprintf("%d", current)
			response.ProviderData["desiredReplicas"] = fmt.Sprintf("%d", request.Pool.DesiredReplicas)
			response.ProviderData["describeNodePoolRequestId"] = describeNodePoolRequestId
			return response
		}
		scaleRequestId = stringValue(scaled.Response.RequestId)
		requestId = firstNonEmpty(scaleRequestId, requestId)
	}
	machines, describeRequestId, err := client.describeClusterMachines(nodePoolId)
	if err != nil {
		response := sdkErrorResponse("tencent_describe_cluster_machines_failed", err)
		response.ProviderRequestId = requestId
		return response
	}
	output := make([]MachineOutput, 0, len(machines))
	machineStates := make([]string, 0, len(machines))
	for _, machine := range machines {
		if machine == nil || stringValue(machine.MachineName) == "" {
			continue
		}
		state := strings.ToLower(strings.TrimSpace(stringValue(machine.MachineState)))
		machineStates = append(machineStates, stringValue(machine.MachineName)+"="+state)
		ready := state == "" || state == "running" || state == "normal" || state == "ready"
		privateIp := stringValue(machine.LanIP)
		instanceId, publicIp := "", ""
		if privateIp == "" {
			tkeInstance, tkeRequestID, tkeErr := client.describeNativeTkeClusterInstanceByMachineName(stringValue(machine.MachineName), nodePoolId)
			if tkeErr != nil {
				response := sdkErrorResponse("tencent_describe_tke_cluster_instance_failed", tkeErr)
				response.ProviderRequestId = firstNonEmpty(tkeRequestID, describeRequestId, requestId)
				return response
			}
			if tkeInstance != nil {
				privateIp = stringValue(tkeInstance.LanIP)
			}
		}
		if instanceId == "" && privateIp != "" {
			if cvmInstance, _, resolveErr := client.describeCvmInstanceByPrivateIp(privateIp); resolveErr == nil {
				instanceId = stringValue(cvmInstance.InstanceId)
				publicIp = firstString(cvmInstance.PublicIpAddresses)
			} else if errors.Is(resolveErr, errCVMInstanceNotFound) {
				return Response{Ok: false, ErrorCode: "compute_cvm_identity_required", Message: "NativeCVM pool machine did not resolve to a Tencent CVM instance.", ProviderRequestId: firstNonEmpty(describeRequestId, requestId), Retryable: true}
			} else {
				response := sdkErrorResponse("tencent_describe_cvm_instance_failed", resolveErr)
				response.ProviderRequestId = firstNonEmpty(describeRequestId, requestId)
				return response
			}
		}
		output = append(output, MachineOutput{MachineId: stringValue(machine.MachineName), InstanceId: instanceId, NodeName: firstNonEmpty(privateIp, kubernetesNodeName(machine)), PrivateIp: privateIp, PublicIp: publicIp, InstanceType: stringValue(machine.InstanceType), Ready: ready && instanceId != "" && privateIp != ""})
	}
	return Response{Ok: true, OperationId: "op-reconcile-pool-" + stableSuffix(nodePoolId, fmt.Sprintf("%d", request.Pool.DesiredReplicas))[:12], PoolId: request.Pool.Id, NodePoolId: nodePoolId, Status: "reconciling", ProviderRequestId: firstNonEmpty(describeRequestId, requestId), Machines: output, ProviderData: map[string]string{"nodePoolId": nodePoolId, "currentReplicas": fmt.Sprintf("%d", len(machines)), "desiredReplicas": fmt.Sprintf("%d", request.Pool.DesiredReplicas), "describeNodePoolRequestId": describeNodePoolRequestId, "scaleNodePoolRequestId": scaleRequestId, "describeMachinesRequestId": describeRequestId, "machineStates": strings.Join(machineStates, ",")}}
}

func (client *tencentSDKClient) TagComputeMachine(request Request, _ map[string]string) Response {
	if client == nil || client.nativeCvmClient == nil {
		return Response{Ok: false, ErrorCode: "tencent_sdk_client_missing", Message: "Tencent CVM SDK client is missing.", Retryable: false}
	}
	instanceID := strings.TrimSpace(request.Allocation.InstanceId)
	resourceID := strings.TrimSpace(request.Tags["opl_resource_id"])
	if instanceID == "" || resourceID == "" || len(resourceID) > 60 {
		return Response{Ok: false, ErrorCode: "compute_machine_identity_required", Message: "Machine instance id and a resource id of at most 60 characters are required.", Retryable: false}
	}
	if strings.HasPrefix(instanceID, "np-") {
		nativeInstance, requestID, err := client.describeTkeClusterInstanceByPrivateIp(request.Allocation.PrivateIp, request.Pool.NodePoolId)
		if err != nil {
			return sdkErrorResponse("tencent_verify_native_compute_machine_failed", err)
		}
		if stringValue(nativeInstance.InstanceId) != instanceID {
			return Response{Ok: false, ErrorCode: "compute_machine_identity_unverified", Message: "Tencent TKE instance did not match the claimed machine identity.", ProviderRequestId: requestID, Retryable: true}
		}
		return Response{Ok: true, InstanceId: instanceID, Status: "tagged", ProviderRequestId: requestID}
	}
	if !strings.HasPrefix(instanceID, "ins-") {
		return Response{Ok: false, ErrorCode: "compute_machine_identity_unverified", Message: "Tencent machine identity source is unsupported.", Retryable: false}
	}
	describe := cvm2017.NewDescribeInstancesRequest()
	describe.InstanceIds = []*string{common.StringPtr(instanceID)}
	described, err := client.nativeCvmClient.DescribeInstances(describe)
	if err != nil {
		return sdkErrorResponse("tencent_verify_compute_machine_failed", err)
	}
	if described == nil || described.Response == nil {
		return sdkErrorResponse("tencent_verify_compute_machine_failed", fmt.Errorf("Tencent CVM DescribeInstances response is missing"))
	}
	if len(described.Response.InstanceSet) == 0 {
		return Response{Ok: false, ErrorCode: "compute_machine_identity_unverified", Message: "Tencent CVM instance did not match the claimed machine identity.", ProviderRequestId: stringValue(described.Response.RequestId), Retryable: true}
	}
	if len(described.Response.InstanceSet) != 1 || described.Response.InstanceSet[0] == nil || stringValue(described.Response.InstanceSet[0].InstanceId) != instanceID {
		return Response{Ok: false, ErrorCode: "compute_machine_identity_unverified", Message: "Tencent CVM instance did not match the claimed machine identity.", ProviderRequestId: stringValue(described.Response.RequestId), Retryable: true}
	}
	modify := cvm2017.NewModifyInstancesAttributeRequest()
	modify.InstanceIds = []*string{common.StringPtr(instanceID)}
	modify.InstanceName = common.StringPtr(resourceID)
	modified, err := client.nativeCvmClient.ModifyInstancesAttribute(modify)
	if err != nil {
		return sdkErrorResponse("tencent_tag_compute_machine_failed", err)
	}
	describe = cvm2017.NewDescribeInstancesRequest()
	describe.InstanceIds = []*string{common.StringPtr(instanceID)}
	described, err = client.nativeCvmClient.DescribeInstances(describe)
	if err != nil {
		return sdkErrorResponse("tencent_verify_compute_machine_tag_failed", err)
	}
	if described == nil || described.Response == nil {
		return sdkErrorResponse("tencent_verify_compute_machine_tag_failed", fmt.Errorf("Tencent CVM DescribeInstances readback response is missing"))
	}
	if len(described.Response.InstanceSet) != 1 || described.Response.InstanceSet[0] == nil {
		return sdkErrorResponse("tencent_verify_compute_machine_tag_failed", fmt.Errorf("Tencent CVM DescribeInstances readback instance is missing"))
	}
	if stringValue(described.Response.InstanceSet[0].InstanceId) != instanceID || stringValue(described.Response.InstanceSet[0].InstanceName) != resourceID {
		return Response{Ok: false, ErrorCode: "compute_machine_tag_unverified", Message: "Tencent CVM instance name did not match the resource id after update.", Retryable: true}
	}
	modifyRequestID := ""
	if modified.Response != nil {
		modifyRequestID = stringValue(modified.Response.RequestId)
	}
	return Response{Ok: true, InstanceId: instanceID, Status: "tagged", ProviderRequestId: firstNonEmpty(stringValue(described.Response.RequestId), modifyRequestID)}
}

func (client *tencentSDKClient) DestroyComputeAllocation(request Request, env map[string]string) Response {
	if client == nil || client.nativeTkeClient == nil {
		return Response{Ok: false, ErrorCode: "tencent_sdk_client_missing", Message: "Tencent TKE SDK client is missing.", Retryable: false}
	}
	if strings.TrimSpace(request.Pool.NodePoolId) == "" {
		return Response{Ok: false, ErrorCode: "node_pool_id_required", Message: "ComputePool nodePoolId is required.", Retryable: false}
	}
	if strings.TrimSpace(request.Allocation.MachineName) == "" {
		return Response{Ok: false, ErrorCode: "compute_allocation_machine_identity_required", Message: "ComputeAllocation machineName is required to destroy a dedicated Tencent node.", Retryable: false}
	}
	describeRequestId := ""
	modifySelfProvisioningRequestId := ""
	pool, requestId, err := client.describeNativeNodePool(request.Pool.NodePoolId)
	if err != nil {
		response := sdkErrorResponse("tencent_describe_node_pool_failed", err)
		response.ProviderData = map[string]string{
			"clusterId":   client.clusterId,
			"region":      client.region,
			"nodePoolId":  request.Pool.NodePoolId,
			"machineName": request.Allocation.MachineName,
			"nodeName":    request.Allocation.NodeName,
			"instanceId":  request.Allocation.InstanceId,
		}
		return response
	}
	describeRequestId = requestId
	identityRequestId, err := client.verifyDestroyMachineOwnership(pool, request)
	if err != nil {
		return Response{
			Ok: false, ErrorCode: "compute_machine_identity_unverified", Message: err.Error(),
			ProviderRequestId: firstNonEmpty(identityRequestId, describeRequestId), Retryable: false,
			ProviderData: map[string]string{
				"clusterId": client.clusterId, "region": client.region, "nodePoolId": request.Pool.NodePoolId,
				"machineName": request.Allocation.MachineName, "nodeName": request.Allocation.NodeName,
				"privateIp": request.Allocation.PrivateIp, "instanceId": request.Allocation.InstanceId,
			},
		}
	}
	if nativeSelfProvisioningEnabled(pool) {
		requestId, err := client.disableNativeNodePoolSelfProvisioning(request.Pool.NodePoolId)
		if err != nil {
			response := sdkErrorResponse("tencent_disable_node_pool_self_provisioning_failed", err)
			response.ProviderRequestId = describeRequestId
			response.ProviderData = map[string]string{
				"clusterId":                    client.clusterId,
				"region":                       client.region,
				"nodePoolId":                   request.Pool.NodePoolId,
				"machineName":                  request.Allocation.MachineName,
				"nodeName":                     request.Allocation.NodeName,
				"instanceId":                   request.Allocation.InstanceId,
				"describeNodePoolRequestId":    describeRequestId,
				"selfProvisioningDisableError": "true",
			}
			return response
		}
		modifySelfProvisioningRequestId = requestId
	}
	providerRequestId := ""
	deleteRequest := tke2022.NewDeleteClusterMachinesRequest()
	deleteRequest.ClusterId = common.StringPtr(client.clusterId)
	deleteRequest.MachineNames = []*string{common.StringPtr(request.Allocation.MachineName)}
	deleteRequest.EnableScaleDown = common.BoolPtr(true)
	deleteRequest.InstanceDeleteMode = common.StringPtr("terminate")
	deleteResponse, err := client.nativeTkeClient.DeleteClusterMachines(deleteRequest)
	if err != nil {
		response := sdkErrorResponse("tencent_delete_cluster_machine_failed", err)
		response.ProviderData = map[string]string{
			"clusterId":                   client.clusterId,
			"region":                      client.region,
			"nodePoolId":                  request.Pool.NodePoolId,
			"machineName":                 request.Allocation.MachineName,
			"nodeName":                    request.Allocation.NodeName,
			"instanceId":                  request.Allocation.InstanceId,
			"deleteMethod":                "DeleteClusterMachines",
			"scaleDown":                   "true",
			"deleteMode":                  "terminate",
			"describeNodePoolRequestId":   describeRequestId,
			"modifySelfProvisioningReqId": modifySelfProvisioningRequestId,
		}
		return response
	}
	providerRequestId = stringValue(deleteResponse.Response.RequestId)
	deleteAttempts := intFromEnv(env, "TENCENT_TKE_NODE_DELETE_ATTEMPTS", 30)
	if deleteAttempts < 1 {
		deleteAttempts = 1
	}
	deleteDelayMs := intFromEnv(env, "TENCENT_TKE_NODE_DELETE_DELAY_MS", 10000)
	if deleteDelayMs < 0 {
		deleteDelayMs = 0
	}
	deleteVerifiedRequestID := ""
	for attempt := 1; attempt <= deleteAttempts; attempt++ {
		machines, verifyRequestID, verifyErr := client.describeClusterMachines(request.Pool.NodePoolId)
		deleteVerifiedRequestID = verifyRequestID
		if verifyErr != nil {
			response := sdkErrorResponse("tencent_verify_compute_machine_delete_failed", verifyErr)
			response.ProviderRequestId = providerRequestId
			return response
		}
		found := false
		for _, machine := range machines {
			if stringValue(machine.MachineName) == request.Allocation.MachineName {
				found = true
				break
			}
		}
		if !found {
			break
		}
		if attempt == deleteAttempts {
			return Response{Ok: false, ErrorCode: "compute_machine_delete_unverified", Message: "Tencent TKE still reports the deleted Machine.", ProviderRequestId: providerRequestId, Retryable: true}
		}
		if deleteDelayMs > 0 {
			time.Sleep(time.Duration(deleteDelayMs) * time.Millisecond)
		}
	}
	return Response{
		Ok:                true,
		OperationId:       "op-destroy-compute-" + stableSuffix(request.AccountId, request.Allocation.Id, request.Pool.NodePoolId, request.Allocation.NodeName)[:12],
		InstanceId:        request.Allocation.InstanceId,
		NodeName:          request.Allocation.NodeName,
		NodePoolId:        request.Pool.NodePoolId,
		Status:            "destroyed",
		ProviderRequestId: providerRequestId,
		ProviderData: map[string]string{
			"clusterId":                   client.clusterId,
			"region":                      client.region,
			"deleteMethod":                "DeleteClusterMachines",
			"scaleDown":                   "true",
			"deleteMode":                  "terminate",
			"describeNodePoolRequestId":   describeRequestId,
			"modifySelfProvisioningReqId": modifySelfProvisioningRequestId,
			"verifyMachineDeletedReqId":   deleteVerifiedRequestID,
		},
	}
}

func (client *tencentSDKClient) SyncComputeAllocation(request Request, _ map[string]string) Response {
	if client == nil || client.nativeTkeClient == nil {
		return Response{Ok: false, ErrorCode: "tencent_sdk_client_missing", Message: "Tencent TKE SDK client is missing.", Retryable: false}
	}
	machines, requestId, err := client.describeClusterMachines(request.Pool.NodePoolId)
	if err != nil {
		response := sdkErrorResponse("tencent_describe_cluster_machines_failed", err)
		response.ProviderData = map[string]string{"nodePoolId": request.Pool.NodePoolId, "machineName": request.Allocation.MachineName, "nodeName": request.Allocation.NodeName, "privateIp": request.Allocation.PrivateIp}
		return response
	}
	machine := findComputeMachine(machines, request.Allocation)
	if machine == nil {
		return Response{
			Ok:                true,
			OperationId:       "op-sync-compute-" + stableSuffix(request.AccountId, request.Allocation.Id, request.Pool.NodePoolId, request.Allocation.MachineName, request.Allocation.PrivateIp)[:12],
			PoolId:            request.Pool.Id,
			NodePoolId:        request.Pool.NodePoolId,
			InstanceId:        request.Allocation.InstanceId,
			NodeName:          request.Allocation.NodeName,
			PrivateIp:         request.Allocation.PrivateIp,
			PublicIp:          request.Allocation.PublicIp,
			Status:            "external_deleted",
			ProviderRequestId: requestId,
			ProviderData: map[string]string{
				"clusterId":                  client.clusterId,
				"region":                     client.region,
				"nodePoolId":                 request.Pool.NodePoolId,
				"machineName":                request.Allocation.MachineName,
				"nodeName":                   request.Allocation.NodeName,
				"privateIp":                  request.Allocation.PrivateIp,
				"syncResult":                 "missing",
				"describeClusterMachinesReq": requestId,
			},
		}
	}
	privateIP := firstNonEmpty(stringValue(machine.LanIP), request.Allocation.PrivateIp)
	nodeName := firstNonEmpty(kubernetesNodeName(machine), request.Allocation.NodeName)
	machineName := firstNonEmpty(stringValue(machine.MachineName), request.Allocation.MachineName)
	status := firstNonEmpty(strings.ToLower(strings.TrimSpace(stringValue(machine.MachineState))), "running")
	return Response{
		Ok:                true,
		OperationId:       "op-sync-compute-" + stableSuffix(request.AccountId, request.Allocation.Id, request.Pool.NodePoolId, machineName, privateIP)[:12],
		PoolId:            request.Pool.Id,
		NodePoolId:        request.Pool.NodePoolId,
		InstanceId:        request.Allocation.InstanceId,
		NodeName:          nodeName,
		PrivateIp:         privateIP,
		PublicIp:          request.Allocation.PublicIp,
		Status:            status,
		ProviderRequestId: requestId,
		ProviderData: map[string]string{
			"clusterId":                  client.clusterId,
			"region":                     client.region,
			"nodePoolId":                 request.Pool.NodePoolId,
			"machineName":                machineName,
			"nodeName":                   nodeName,
			"privateIp":                  privateIP,
			"syncResult":                 "found",
			"describeClusterMachinesReq": requestId,
		},
	}
}

func (client *tencentSDKClient) ProviderTruth(request Request, _ map[string]string) Response {
	if client == nil || client.nativeTkeClient == nil || client.nativeCvmClient == nil || client.nativeCbsClient == nil {
		return Response{Ok: false, ErrorCode: "tencent_sdk_client_missing", Message: "Tencent TKE, CVM, and CBS SDK clients are required.", Retryable: false}
	}
	for field, value := range map[string]string{
		"accountId": request.AccountId, "resourceId": request.Allocation.Id, "clusterId": request.Pool.ClusterId,
		"nodePoolId": request.Pool.NodePoolId, "machineName": request.Allocation.MachineName,
		"instanceId": request.Allocation.InstanceId, "privateIp": request.Allocation.PrivateIp, "storageVolumeId": request.StorageVolumeId,
	} {
		if strings.TrimSpace(value) == "" {
			return Response{Ok: false, ErrorCode: "provider_truth_identity_required", Message: field + " is required for exact provider truth.", Retryable: false}
		}
	}
	if request.Pool.ClusterId != client.clusterId {
		return Response{Ok: false, ErrorCode: "provider_truth_cluster_mismatch", Message: "The supplied cluster does not match the configured cluster.", Retryable: false}
	}
	if !strings.HasPrefix(request.Allocation.InstanceId, "ins-") {
		return Response{Ok: false, ErrorCode: "provider_truth_cvm_instance_required", Message: "A Tencent CVM ins-* instance ID is required.", Retryable: false}
	}
	if !strings.HasPrefix(request.StorageVolumeId, "disk-") {
		return Response{Ok: false, ErrorCode: "provider_truth_cbs_volume_required", Message: "A Tencent CBS disk-* volume ID is required.", Retryable: false}
	}
	storagePresent, cbsStatus, cbsRequestID, err := client.cbsVolumeTruth(request.StorageVolumeId)
	if err != nil {
		response := sdkErrorResponse("tencent_provider_truth_cbs_probe_failed", err)
		response.ProviderRequestId = firstNonEmpty(response.ProviderRequestId, cbsRequestID)
		return response
	}

	pool, poolRequestID, err := client.describeNativeNodePool(request.Pool.NodePoolId)
	if err != nil {
		response := sdkErrorResponse("tencent_provider_truth_node_pool_failed", err)
		response.ProviderRequestId = firstNonEmpty(response.ProviderRequestId, poolRequestID)
		return response
	}
	if !isCVMNativeNodePool(pool) {
		return Response{Ok: false, ErrorCode: "tencent_cvm_node_pool_required", Message: "Provider truth requires a POSTPAID NativeCVM node pool.", ProviderRequestId: poolRequestID, Retryable: false}
	}

	machines, machineRequestID, err := client.describeClusterMachines(request.Pool.NodePoolId)
	if err != nil {
		response := sdkErrorResponse("tencent_provider_truth_machine_probe_failed", err)
		response.ProviderRequestId = firstNonEmpty(response.ProviderRequestId, machineRequestID)
		return response
	}
	var machine *tke2022.Machine
	for _, candidate := range machines {
		if candidate != nil && stringValue(candidate.MachineName) == request.Allocation.MachineName {
			if machine != nil {
				return Response{Ok: false, ErrorCode: "provider_truth_machine_ambiguous", Message: "The supplied machine name is not unique in the exact node pool.", ProviderRequestId: machineRequestID, Retryable: false}
			}
			machine = candidate
		}
	}
	if machine != nil && stringValue(machine.LanIP) != request.Allocation.PrivateIp {
		return Response{Ok: false, ErrorCode: "provider_truth_machine_ip_mismatch", Message: "The supplied machine and private IP do not identify the same TKE machine.", ProviderRequestId: machineRequestID, Retryable: false}
	}

	tkeInstance, tkeRequestID, tkeErr := client.describeTkeClusterInstanceByPrivateIp(request.Allocation.PrivateIp, request.Pool.NodePoolId)
	if tkeErr != nil && !errors.Is(tkeErr, errTKEInstanceNotFound) {
		response := sdkErrorResponse("tencent_provider_truth_tke_instance_probe_failed", tkeErr)
		response.ProviderRequestId = firstNonEmpty(response.ProviderRequestId, tkeRequestID)
		return response
	}
	cvmInstance, cvmRequestID, err := client.describeCvmInstanceByID(request.Allocation.InstanceId)
	if err != nil {
		response := sdkErrorResponse("tencent_provider_truth_cvm_probe_failed", err)
		response.ProviderRequestId = firstNonEmpty(response.ProviderRequestId, cvmRequestID)
		return response
	}

	machinePresent, tkePresent, cvmPresent := machine != nil, tkeInstance != nil, cvmInstance != nil
	providerData := map[string]string{
		"resourceId": request.Allocation.Id, "clusterId": request.Pool.ClusterId,
		"nodePoolId": request.Pool.NodePoolId, "machineName": request.Allocation.MachineName,
		"storageVolumeId": request.StorageVolumeId, "storagePresent": strconv.FormatBool(storagePresent), "cbsStatus": cbsStatus,
		"machinePresent": strconv.FormatBool(machinePresent), "describeNodePoolRequestId": poolRequestID,
		"describeMachineRequestId": machineRequestID, "describeTkeRequestId": tkeRequestID, "describeCvmRequestId": cvmRequestID,
		"describeCbsRequestId": cbsRequestID,
	}
	if !machinePresent && !tkePresent && !cvmPresent {
		providerData["tkeStatus"] = "NOT_FOUND"
		providerData["cvmStatus"] = "NOT_FOUND"
		return Response{Ok: true, PoolId: request.Pool.Id, NodePoolId: request.Pool.NodePoolId, InstanceId: request.Allocation.InstanceId, PrivateIp: request.Allocation.PrivateIp, MachinePresent: &machinePresent, StoragePresent: &storagePresent, CVMStatus: "NOT_FOUND", TKEStatus: "NOT_FOUND", CBSStatus: cbsStatus, Status: "absent", MachineType: "NativeCVM", ProviderRequestId: firstNonEmpty(cbsRequestID, cvmRequestID, tkeRequestID, machineRequestID), ProviderData: providerData}
	}
	if !machinePresent || !tkePresent || !cvmPresent {
		return Response{Ok: false, ErrorCode: "provider_truth_partial_identity", Message: "Tencent provider identity is only partially present.", ProviderRequestId: firstNonEmpty(cvmRequestID, tkeRequestID, machineRequestID), ProviderData: providerData, Retryable: true}
	}
	if stringValue(tkeInstance.InstanceId) != request.Allocation.MachineName {
		return Response{Ok: false, ErrorCode: "provider_truth_node_mismatch", Message: "The supplied machine does not identify the TKE cluster instance at the private IP.", ProviderRequestId: tkeRequestID, ProviderData: providerData, Retryable: false}
	}
	if stringValue(cvmInstance.InstanceId) != request.Allocation.InstanceId || stringValue(cvmInstance.InstanceName) != request.Allocation.Id ||
		!containsString(cvmInstance.PrivateIpAddresses, request.Allocation.PrivateIp) {
		return Response{Ok: false, ErrorCode: "provider_truth_cvm_identity_mismatch", Message: "The supplied resource, instance, machine, and node do not identify the same CVM.", ProviderRequestId: cvmRequestID, ProviderData: providerData, Retryable: false}
	}
	tkeStatus := strings.ToUpper(strings.TrimSpace(stringValue(tkeInstance.InstanceState)))
	cvmStatus := strings.ToUpper(strings.TrimSpace(stringValue(cvmInstance.InstanceState)))
	if tkeStatus == "" || cvmStatus == "" {
		return Response{Ok: false, ErrorCode: "provider_truth_status_unknown", Message: "Tencent returned a present resource without an exact state.", ProviderRequestId: firstNonEmpty(cvmRequestID, tkeRequestID), ProviderData: providerData, Retryable: true}
	}
	providerData["tkeStatus"] = tkeStatus
	providerData["cvmStatus"] = cvmStatus
	return Response{Ok: true, PoolId: request.Pool.Id, NodePoolId: request.Pool.NodePoolId, InstanceId: request.Allocation.InstanceId, PrivateIp: request.Allocation.PrivateIp, MachinePresent: &machinePresent, StoragePresent: &storagePresent, CVMStatus: cvmStatus, TKEStatus: tkeStatus, CBSStatus: cbsStatus, Status: "present", MachineType: "NativeCVM", ProviderRequestId: firstNonEmpty(cbsRequestID, cvmRequestID), ProviderData: providerData}
}

func (client *tencentSDKClient) cbsVolumeTruth(volumeID string) (bool, string, string, error) {
	request := cbs2017.NewDescribeDisksRequest()
	request.DiskIds = []*string{common.StringPtr(volumeID)}
	response, err := client.nativeCbsClient.DescribeDisks(request)
	if err != nil {
		if sdkErr, ok := err.(*tcerrors.TencentCloudSDKError); ok && sdkErr.Code == cbs2017.INVALIDDISKID_NOTFOUND {
			return false, "NOT_FOUND", sdkErr.RequestId, nil
		}
		return false, "", "", err
	}
	if response == nil || response.Response == nil || response.Response.TotalCount == nil ||
		*response.Response.TotalCount != uint64(len(response.Response.DiskSet)) || len(response.Response.DiskSet) > 1 {
		return false, "", "", fmt.Errorf("Tencent CBS DescribeDisks response is missing or incomplete")
	}
	requestID := stringValue(response.Response.RequestId)
	if len(response.Response.DiskSet) == 0 {
		return false, "NOT_FOUND", requestID, nil
	}
	disk := response.Response.DiskSet[0]
	if disk == nil || stringValue(disk.DiskId) != volumeID || strings.TrimSpace(stringValue(disk.DiskState)) == "" {
		return false, "", requestID, fmt.Errorf("Tencent CBS disk identity or state is missing")
	}
	return true, strings.ToUpper(strings.TrimSpace(stringValue(disk.DiskState))), requestID, nil
}

func (client *tencentSDKClient) describeClusterMachines(nodePoolId string) ([]*tke2022.Machine, string, error) {
	describeRequest := tke2022.NewDescribeClusterMachinesRequest()
	describeRequest.ClusterId = common.StringPtr(client.clusterId)
	describeRequest.Limit = common.Int64Ptr(100)
	if strings.TrimSpace(nodePoolId) != "" {
		describeRequest.Filters = []*tke2022.Filter{
			{Name: common.StringPtr("NodePoolsId"), Values: []*string{common.StringPtr(nodePoolId)}},
		}
	}
	describeResponse, err := client.nativeTkeClient.DescribeClusterMachines(describeRequest)
	if err != nil && strings.TrimSpace(nodePoolId) != "" && isInvalidMachineFilterError(err) {
		fallbackRequest := tke2022.NewDescribeClusterMachinesRequest()
		fallbackRequest.ClusterId = common.StringPtr(client.clusterId)
		fallbackRequest.Limit = common.Int64Ptr(100)
		describeResponse, err = client.nativeTkeClient.DescribeClusterMachines(fallbackRequest)
		if err == nil {
			instanceRequest := tke2022.NewDescribeClusterInstancesRequest()
			instanceRequest.ClusterId = common.StringPtr(client.clusterId)
			instanceRequest.Limit = common.Int64Ptr(100)
			instanceRequest.Filters = []*tke2022.Filter{{Name: common.StringPtr("NodePoolIds"), Values: []*string{common.StringPtr(nodePoolId)}}}
			instances, instanceErr := client.nativeTkeClient.DescribeClusterInstances(instanceRequest)
			if instanceErr != nil {
				err = instanceErr
			} else {
				poolIPs := map[string]bool{}
				for _, instance := range instances.Response.InstanceSet {
					if instance != nil && stringValue(instance.NodePoolId) == nodePoolId {
						poolIPs[stringValue(instance.LanIP)] = true
					}
				}
				machines := describeResponse.Response.Machines[:0]
				for _, machine := range describeResponse.Response.Machines {
					if machine != nil && poolIPs[stringValue(machine.LanIP)] {
						machines = append(machines, machine)
					}
				}
				describeResponse.Response.Machines = machines
				describeResponse.Response.TotalCount = common.Int64Ptr(int64(len(machines)))
			}
		}
	}
	if err != nil {
		return nil, "", err
	}
	if describeResponse == nil || describeResponse.Response == nil || describeResponse.Response.TotalCount == nil ||
		*describeResponse.Response.TotalCount != int64(len(describeResponse.Response.Machines)) {
		return nil, "", fmt.Errorf("Tencent TKE DescribeClusterMachines response is missing or incomplete")
	}
	return describeResponse.Response.Machines, stringValue(describeResponse.Response.RequestId), nil
}

func (client *tencentSDKClient) verifyDestroyMachineOwnership(pool *tke2022.NodePool, request Request) (string, error) {
	poolMachines, requestID, err := client.describeClusterMachines(request.Pool.NodePoolId)
	if err != nil {
		return requestID, err
	}
	machineName := strings.TrimSpace(request.Allocation.MachineName)
	matches := []*tke2022.Machine{}
	for _, machine := range poolMachines {
		if machine != nil && stringValue(machine.MachineName) == machineName {
			matches = append(matches, machine)
		}
	}
	if len(matches) != 1 {
		return requestID, fmt.Errorf("machine name is not unique in the exact node pool")
	}
	allMachines, globalRequestID, err := client.describeClusterMachines("")
	if err != nil {
		return firstNonEmpty(globalRequestID, requestID), err
	}
	globalMatches := 0
	for _, machine := range allMachines {
		if machine != nil && stringValue(machine.MachineName) == machineName {
			globalMatches++
		}
	}
	if globalMatches != 1 {
		return globalRequestID, fmt.Errorf("machine name is not globally unique")
	}
	machine := matches[0]
	privateIP := stringValue(machine.LanIP)
	if privateIP == "" || (request.Allocation.PrivateIp != "" && request.Allocation.PrivateIp != privateIP) ||
		(request.Allocation.NodeName != "" && request.Allocation.NodeName != kubernetesNodeName(machine)) {
		return globalRequestID, fmt.Errorf("machine name, node name, and private IP do not identify the same machine")
	}
	machineType := ""
	if pool != nil && pool.Native != nil {
		machineType = stringValue(pool.Native.MachineType)
	}
	switch {
	case strings.EqualFold(machineType, "NativeCVM"):
		instance, providerRequestID, err := client.describeCvmInstanceByPrivateIp(privateIP)
		if err != nil {
			return firstNonEmpty(providerRequestID, globalRequestID), err
		}
		instanceID := strings.TrimSpace(request.Allocation.InstanceId)
		if !strings.HasPrefix(instanceID, "ins-") || stringValue(instance.InstanceId) != instanceID {
			return providerRequestID, fmt.Errorf("CVM instance does not match the supplied instance ID")
		}
		return providerRequestID, nil
	case strings.EqualFold(machineType, "Native"), strings.EqualFold(machineType, "CXM"):
		instance, providerRequestID, err := client.describeTkeClusterInstanceByPrivateIp(privateIP, request.Pool.NodePoolId)
		if err != nil {
			return firstNonEmpty(providerRequestID, globalRequestID), err
		}
		if request.Allocation.InstanceId == "" || stringValue(instance.InstanceId) != request.Allocation.InstanceId ||
			stringValue(instance.NodePoolId) != request.Pool.NodePoolId || stringValue(instance.LanIP) != privateIP {
			return providerRequestID, fmt.Errorf("TKE instance does not match the supplied machine identity")
		}
		return providerRequestID, nil
	default:
		return globalRequestID, fmt.Errorf("unsupported Tencent machine provider: %s", machineType)
	}
}

func nativeSelfProvisioningEnabled(pool *tke2022.NodePool) bool {
	return pool != nil && pool.Native != nil && ((pool.Native.EnableAutoscaling != nil && *pool.Native.EnableAutoscaling) ||
		(pool.Native.AutoRepair != nil && *pool.Native.AutoRepair))
}

func (client *tencentSDKClient) disableNativeNodePoolSelfProvisioning(nodePoolId string) (string, error) {
	modifyRequest := tke2022.NewModifyNodePoolRequest()
	modifyRequest.ClusterId = common.StringPtr(client.clusterId)
	modifyRequest.NodePoolId = common.StringPtr(nodePoolId)
	modifyRequest.Native = &tke2022.UpdateNativeNodePoolParam{
		EnableAutoscaling: common.BoolPtr(false),
		AutoRepair:        common.BoolPtr(false),
	}
	modifyResponse, err := client.nativeTkeClient.ModifyNodePool(modifyRequest)
	if err != nil {
		return "", err
	}
	return stringValue(modifyResponse.Response.RequestId), nil
}

func isInvalidMachineFilterError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "invalid filter name") || strings.Contains(message, "invalidparameter")
}

func (client *tencentSDKClient) waitForNewPoolMachine(nodePoolId string, before []*tke2022.Machine, request Request, env map[string]string) (*tke2022.Machine, string, error) {
	beforeNames := map[string]bool{}
	for _, machine := range before {
		if name := stringValue(machine.MachineName); name != "" {
			beforeNames[name] = true
		}
	}
	attempts := intFromEnv(env, "TENCENT_TKE_NODE_READY_ATTEMPTS", 30)
	if attempts < 1 {
		attempts = 1
	}
	delayMs := intFromEnv(env, "TENCENT_TKE_NODE_READY_DELAY_MS", 10000)
	if delayMs < 0 {
		delayMs = 0
	}
	var lastRequestId string
	for attempt := 1; attempt <= attempts; attempt++ {
		machines, requestId, err := client.describeClusterMachines(nodePoolId)
		lastRequestId = requestId
		if err != nil {
			return nil, requestId, err
		}
		if machine := selectNewReadyMachine(machines, beforeNames, request.Pool.InstanceType); machine != nil {
			return machine, requestId, nil
		}
		if attempt < attempts && delayMs > 0 {
			time.Sleep(time.Duration(delayMs) * time.Millisecond)
		}
	}
	return nil, lastRequestId, fmt.Errorf("new node in pool %s not ready", nodePoolId)
}

func selectNewReadyMachine(machines []*tke2022.Machine, beforeNames map[string]bool, instanceType string) *tke2022.Machine {
	for _, machine := range machines {
		if machine == nil {
			continue
		}
		name := stringValue(machine.MachineName)
		if name == "" || beforeNames[name] {
			continue
		}
		if instanceType != "" && stringValue(machine.InstanceType) != "" && stringValue(machine.InstanceType) != instanceType {
			continue
		}
		state := strings.ToLower(strings.TrimSpace(stringValue(machine.MachineState)))
		if state != "" && state != "running" && state != "normal" && state != "ready" {
			continue
		}
		return machine
	}
	return nil
}

func kubernetesNodeName(machine *tke2022.Machine) string {
	if machine == nil {
		return ""
	}
	if lanIp := stringValue(machine.LanIP); lanIp != "" {
		return lanIp
	}
	return stringValue(machine.MachineName)
}

func (client *tencentSDKClient) describeCvmInstanceByPrivateIp(privateIp string) (*cvm2017.Instance, string, error) {
	if strings.TrimSpace(privateIp) == "" {
		return nil, "", fmt.Errorf("private IP is required to resolve CVM instance identity")
	}
	if client == nil || client.nativeCvmClient == nil {
		return nil, "", fmt.Errorf("Tencent CVM SDK client is missing")
	}
	describeRequest := cvm2017.NewDescribeInstancesRequest()
	describeRequest.Filters = []*cvm2017.Filter{{
		Name:   common.StringPtr("private-ip-address"),
		Values: []*string{common.StringPtr(privateIp)},
	}}
	describeRequest.Limit = common.Int64Ptr(1)
	describeResponse, err := client.nativeCvmClient.DescribeInstances(describeRequest)
	if err != nil {
		return nil, "", err
	}
	if describeResponse == nil || describeResponse.Response == nil {
		return nil, "", fmt.Errorf("Tencent CVM DescribeInstances response is missing")
	}
	requestId := stringValue(describeResponse.Response.RequestId)
	if len(describeResponse.Response.InstanceSet) > 0 {
		if describeResponse.Response.InstanceSet[0] == nil {
			return nil, requestId, fmt.Errorf("Tencent CVM DescribeInstances returned an empty instance")
		}
		return describeResponse.Response.InstanceSet[0], requestId, nil
	}
	return nil, requestId, fmt.Errorf("%w for private IP %s", errCVMInstanceNotFound, privateIp)
}

func (client *tencentSDKClient) describeCvmInstanceByID(instanceID string) (*cvm2017.Instance, string, error) {
	describeRequest := cvm2017.NewDescribeInstancesRequest()
	describeRequest.InstanceIds = []*string{common.StringPtr(instanceID)}
	describeRequest.Limit = common.Int64Ptr(1)
	describeResponse, err := client.nativeCvmClient.DescribeInstances(describeRequest)
	if err != nil {
		return nil, "", err
	}
	if describeResponse == nil || describeResponse.Response == nil || describeResponse.Response.TotalCount == nil ||
		*describeResponse.Response.TotalCount != int64(len(describeResponse.Response.InstanceSet)) || len(describeResponse.Response.InstanceSet) > 1 {
		return nil, "", fmt.Errorf("Tencent CVM DescribeInstances response is missing or incomplete")
	}
	requestID := stringValue(describeResponse.Response.RequestId)
	if len(describeResponse.Response.InstanceSet) == 0 {
		return nil, requestID, nil
	}
	if describeResponse.Response.InstanceSet[0] == nil {
		return nil, requestID, fmt.Errorf("Tencent CVM DescribeInstances returned an empty instance")
	}
	return describeResponse.Response.InstanceSet[0], requestID, nil
}

func (client *tencentSDKClient) describeTkeClusterInstanceByPrivateIp(privateIp string, nodePoolId string) (*tke2022.Instance, string, error) {
	if strings.TrimSpace(privateIp) == "" {
		return nil, "", fmt.Errorf("private IP is required to resolve TKE instance identity")
	}
	if client == nil || client.nativeTkeClient == nil {
		return nil, "", fmt.Errorf("Tencent TKE SDK client is missing")
	}
	if strings.TrimSpace(nodePoolId) == "" {
		return nil, "", fmt.Errorf("node pool ID is required to resolve native TKE instance identity")
	}
	describeRequest := tke2022.NewDescribeClusterInstancesRequest()
	describeRequest.ClusterId = common.StringPtr(client.clusterId)
	describeRequest.Limit = common.Int64Ptr(100)
	describeRequest.Filters = []*tke2022.Filter{
		{Name: common.StringPtr("VagueIpAddress"), Values: []*string{common.StringPtr(privateIp)}},
	}
	describeRequest.Filters = append(describeRequest.Filters,
		&tke2022.Filter{Name: common.StringPtr("NodePoolIds"), Values: []*string{common.StringPtr(nodePoolId)}})
	describeResponse, err := client.nativeTkeClient.DescribeClusterInstances(describeRequest)
	if err != nil {
		return nil, "", err
	}
	if describeResponse == nil || describeResponse.Response == nil || describeResponse.Response.TotalCount == nil ||
		*describeResponse.Response.TotalCount != uint64(len(describeResponse.Response.InstanceSet)) {
		return nil, "", fmt.Errorf("Tencent TKE DescribeClusterInstances response is missing or incomplete")
	}
	requestId := stringValue(describeResponse.Response.RequestId)
	var matched *tke2022.Instance
	for _, instance := range describeResponse.Response.InstanceSet {
		if instance == nil {
			return nil, requestId, fmt.Errorf("Tencent TKE DescribeClusterInstances returned an empty instance")
		}
		if stringValue(instance.LanIP) == privateIp && stringValue(instance.NodePoolId) == nodePoolId && strings.EqualFold(stringValue(instance.NodeType), "Native") {
			if matched != nil {
				return nil, requestId, fmt.Errorf("Tencent TKE instance identity is ambiguous")
			}
			matched = instance
		}
	}
	if matched != nil {
		return matched, requestId, nil
	}
	return nil, requestId, fmt.Errorf("%w for private IP %s", errTKEInstanceNotFound, privateIp)
}

func (client *tencentSDKClient) describeNativeTkeClusterInstanceByMachineName(machineName string, nodePoolId string) (*tke2022.Instance, string, error) {
	if strings.TrimSpace(machineName) == "" || strings.TrimSpace(nodePoolId) == "" {
		return nil, "", fmt.Errorf("machine name and node pool ID are required to resolve native TKE instance identity")
	}
	if client == nil || client.nativeTkeClient == nil {
		return nil, "", fmt.Errorf("Tencent TKE SDK client is missing")
	}
	describeRequest := tke2022.NewDescribeClusterInstancesRequest()
	describeRequest.ClusterId = common.StringPtr(client.clusterId)
	describeRequest.Limit = common.Int64Ptr(100)
	describeRequest.Filters = []*tke2022.Filter{
		{Name: common.StringPtr("NodePoolIds"), Values: []*string{common.StringPtr(nodePoolId)}},
	}
	describeResponse, err := client.nativeTkeClient.DescribeClusterInstances(describeRequest)
	if err != nil {
		return nil, "", err
	}
	if describeResponse == nil || describeResponse.Response == nil {
		return nil, "", fmt.Errorf("Tencent TKE DescribeClusterInstances response is missing")
	}
	requestID := stringValue(describeResponse.Response.RequestId)
	for _, instance := range describeResponse.Response.InstanceSet {
		if instance != nil && stringValue(instance.InstanceId) == machineName && stringValue(instance.NodePoolId) == nodePoolId && strings.EqualFold(stringValue(instance.NodeType), "Native") && stringValue(instance.LanIP) != "" {
			return instance, requestID, nil
		}
	}
	return nil, requestID, nil
}

func (client *tencentSDKClient) describeNativeNodePool(nodePoolId string) (*tke2022.NodePool, string, error) {
	describeRequest := tke2022.NewDescribeNodePoolsRequest()
	describeRequest.ClusterId = common.StringPtr(client.clusterId)
	describeRequest.Limit = common.Int64Ptr(100)
	describeRequest.Filters = []*tke2022.Filter{
		{Name: common.StringPtr("NodePoolsId"), Values: []*string{common.StringPtr(nodePoolId)}},
	}
	describeResponse, err := client.nativeTkeClient.DescribeNodePools(describeRequest)
	if err != nil {
		return nil, "", err
	}
	if describeResponse == nil || describeResponse.Response == nil {
		return nil, "", fmt.Errorf("Tencent TKE DescribeNodePools response is missing")
	}
	requestID := stringValue(describeResponse.Response.RequestId)
	if describeResponse.Response.TotalCount == nil || *describeResponse.Response.TotalCount != 1 ||
		len(describeResponse.Response.NodePools) != 1 || describeResponse.Response.NodePools[0] == nil ||
		stringValue(describeResponse.Response.NodePools[0].NodePoolId) != nodePoolId {
		return nil, requestID, fmt.Errorf("node pool not found or ambiguous: %s", nodePoolId)
	}
	return describeResponse.Response.NodePools[0], requestID, nil
}

func (client *tencentSDKClient) discoverNativeNodePool(request Request) (*tke2022.NodePool, string, error) {
	describeRequest := tke2022.NewDescribeNodePoolsRequest()
	describeRequest.ClusterId = common.StringPtr(client.clusterId)
	describeRequest.Limit = common.Int64Ptr(100)
	describeResponse, err := client.nativeTkeClient.DescribeNodePools(describeRequest)
	if err != nil {
		return nil, "", err
	}
	if describeResponse == nil || describeResponse.Response == nil {
		return nil, "", fmt.Errorf("Tencent TKE DescribeNodePools response is missing")
	}
	requestId := stringValue(describeResponse.Response.RequestId)
	if describeResponse.Response.TotalCount == nil || *describeResponse.Response.TotalCount != int64(len(describeResponse.Response.NodePools)) {
		return nil, requestId, fmt.Errorf("node pool discovery is incomplete")
	}
	matches := []*tke2022.NodePool{}
	for _, pool := range describeResponse.Response.NodePools {
		if matchesPackageNodePool(pool, request) {
			matches = append(matches, pool)
		}
	}
	if len(matches) > 1 {
		return nil, requestId, fmt.Errorf("node pool discovery is ambiguous")
	}
	if len(matches) == 1 {
		return matches[0], requestId, nil
	}
	return nil, requestId, nil
}

func matchesPackageNodePool(pool *tke2022.NodePool, request Request) bool {
	return isCVMNativeNodePool(pool) && !isDeletingNodePool(pool) && matchesCapacityNodePool(pool, request)
}

func nodePoolLabels(pool *tke2022.NodePool) map[string]string {
	labels := map[string]string{}
	for _, label := range pool.Labels {
		if label != nil && label.Name != nil && label.Value != nil {
			labels[*label.Name] = *label.Value
		}
	}
	return labels
}

func isDeletingNodePool(pool *tke2022.NodePool) bool {
	lifeState := strings.ToLower(strings.TrimSpace(stringValue(pool.LifeState)))
	return strings.Contains(lifeState, "delet")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func buildCreateNativeNodePoolRequest(request Request, env map[string]string) (*tke2022.CreateNodePoolRequest, *Response) {
	missing := missingSpecificEnv(env, []string{
		"TENCENT_DEPLOY_CLUSTER_ID",
		"TENCENT_CVM_SUBNET_ID",
		"TENCENT_CVM_SECURITY_GROUP_IDS",
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
	if strings.TrimSpace(request.Pool.InstanceType) == "" {
		return nil, &Response{Ok: false, ErrorCode: "instance_type_required", Message: "ComputePool instanceType is required.", Retryable: false}
	}
	createRequest := tke2022.NewCreateNodePoolRequest()
	createRequest.ClusterId = common.StringPtr(env["TENCENT_DEPLOY_CLUSTER_ID"])
	createRequest.Name = common.StringPtr(nodePoolName)
	createRequest.Type = common.StringPtr("Native")
	createRequest.DeletionProtection = common.BoolPtr(true)
	nodePoolLabels := []*tke2022.Label{
		{Name: common.StringPtr("oplcloud.cn/pool-id"), Value: common.StringPtr(request.Pool.Id)},
		{Name: common.StringPtr("oplcloud.cn/package-id"), Value: common.StringPtr(request.PackageId)},
		{Name: common.StringPtr("oplcloud.cn/instance-type"), Value: common.StringPtr(request.Pool.InstanceType)},
	}
	for key, value := range nodePoolCostLabels(request.Tags) {
		nodePoolLabels = append(nodePoolLabels, &tke2022.Label{Name: common.StringPtr(key), Value: common.StringPtr(value)})
	}
	createRequest.Labels = nodePoolLabels
	createRequest.Tags = tkeTagSpecifications(request.Tags, "machine")
	createRequest.Native = &tke2022.CreateNativeNodePoolParam{
		Scaling: &tke2022.MachineSetScaling{
			MinReplicas:  common.Int64Ptr(0),
			MaxReplicas:  common.Int64Ptr(10),
			CreatePolicy: common.StringPtr("ZonePriority"),
		},
		SubnetIds:          stringsToPtrs(splitCsv(env["TENCENT_CVM_SUBNET_ID"])),
		InstanceChargeType: common.StringPtr("POSTPAID_BY_HOUR"),
		SystemDisk: &tke2022.Disk{
			DiskType: common.StringPtr(defaultString(env["TENCENT_CVM_SYSTEM_DISK_TYPE"], "CLOUD_BSSD")),
			DiskSize: common.Int64Ptr(int64(intFromEnv(env, "TENCENT_CVM_SYSTEM_DISK_SIZE_GB", 50))),
		},
		InstanceTypes:      []*string{common.StringPtr(request.Pool.InstanceType)},
		SecurityGroupIds:   stringsToPtrs(splitCsv(env["TENCENT_CVM_SECURITY_GROUP_IDS"])),
		AutoRepair:         common.BoolPtr(false),
		EnableAutoscaling:  common.BoolPtr(false),
		Replicas:           common.Int64Ptr(0),
		InternetAccessible: &tke2022.InternetAccessible{MaxBandwidthOut: common.Int64Ptr(0), ChargeType: common.StringPtr("TRAFFIC_POSTPAID_BY_HOUR")},
		MachineType:        common.StringPtr("NativeCVM"),
		AutomationService:  common.BoolPtr(true),
		RuntimeRootDir:     common.StringPtr("/var/lib/containerd"),
	}
	return createRequest, nil
}

func tkeTagSpecifications(tags map[string]string, resourceType string) []*tke2022.TagSpecification {
	if len(tags) == 0 {
		return nil
	}
	items := []*tke2022.Tag{}
	for key, value := range tags {
		if strings.TrimSpace(key) != "" && strings.TrimSpace(value) != "" {
			items = append(items, &tke2022.Tag{Key: common.StringPtr(key), Value: common.StringPtr(value)})
		}
	}
	if len(items) == 0 {
		return nil
	}
	return []*tke2022.TagSpecification{{ResourceType: common.StringPtr(resourceType), Tags: items}}
}

func nodePoolCostLabels(tags map[string]string) map[string]string {
	labels := map[string]string{}
	for key, value := range map[string]string{
		"oplcloud.cn/account-id":   tags["opl_account_id"],
		"oplcloud.cn/workspace-id": tags["opl_workspace_id"],
		"oplcloud.cn/resource-id":  tags["opl_resource_id"],
		"oplcloud.cn/operation-id": tags["opl_operation_id"],
	} {
		if strings.TrimSpace(value) != "" {
			labels[key] = value
		}
	}
	return labels
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
	if isLiveMutation(request) && strings.TrimSpace(env["RUN_TENCENT_CREATE_RELEASE_EXECUTION"]) != "1" {
		return Response{
			Ok:        false,
			ErrorCode: "live_mutation_flag_required",
			Message:   "Set RUN_TENCENT_CREATE_RELEASE_EXECUTION=1 to run live Tencent compute mutations.",
			Retryable: false,
		}
	}

	switch request.Action {
	case "capacity_preflight":
		return client.Capacity(request, env)
	case "provider_truth":
		return client.ProviderTruth(request, env)
	case "reconcile_compute_pool":
		if request.DryRun {
			machines := make([]MachineOutput, 0, request.Pool.DesiredReplicas)
			for index := int64(0); index < request.Pool.DesiredReplicas; index++ {
				id := fmt.Sprintf("%s-%06d", firstNonEmpty(request.Pool.Id, "pool"), index+1)
				machines = append(machines, MachineOutput{MachineId: id, InstanceId: "ins-" + id, NodeName: id, PrivateIp: fmt.Sprintf("10.0.%d.%d", index/250, index%250+1), InstanceType: request.Pool.InstanceType, Ready: true})
			}
			return Response{Ok: true, PoolId: request.Pool.Id, NodePoolId: firstNonEmpty(request.Pool.NodePoolId, "np-"+request.Pool.Id), Status: "ready", ProviderRequestId: "dryrun-reconcile-" + request.Pool.Id, Machines: machines}
		}
		return client.ReconcileComputePool(request, env)
	case "create_compute_allocation":
		if request.DryRun {
			return dryRunCreateComputeAllocation(request, env)
		}
		return client.CreateComputeAllocation(request, env)
	case "tag_compute_machine":
		if request.DryRun {
			return Response{Ok: true, InstanceId: request.Allocation.InstanceId, Status: "tagged", ProviderRequestId: "dryrun-tag-" + request.Allocation.InstanceId}
		}
		return client.TagComputeMachine(request, env)
	case "sync_compute_allocation":
		if request.DryRun {
			return dryRunSyncComputeAllocation(request)
		}
		return client.SyncComputeAllocation(request, env)
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

func isLiveMutation(request Request) bool {
	if request.DryRun {
		return false
	}
	return request.Action == "reconcile_compute_pool" || request.Action == "create_compute_allocation" || request.Action == "tag_compute_machine" || request.Action == "destroy_compute_allocation"
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
	providerData := map[string]string{
		"accountId":       request.AccountId,
		"userId":          request.UserId,
		"packageId":       request.PackageId,
		"clusterId":       env["TENCENT_DEPLOY_CLUSTER_ID"],
		"region":          env["TENCENTCLOUD_REGION"],
		"instanceType":    request.Pool.InstanceType,
		"provisionerMode": "dry-run",
	}
	for key, value := range request.Tags {
		if strings.TrimSpace(value) != "" {
			providerData[key] = value
		}
	}
	return Response{
		Ok:           true,
		OperationId:  "op-create-compute-" + stable[:12],
		PoolId:       request.Pool.Id,
		NodePoolId:   nodePoolId,
		InstanceId:   instanceId,
		NodeName:     nodeName,
		PrivateIp:    "10.0.0." + strconv.Itoa(len(stable)),
		Status:       "running",
		ProviderData: providerData,
	}
}

func dryRunDestroyComputeAllocation(request Request) Response {
	stable := stableSuffix(request.AccountId, request.Allocation.Id, request.Allocation.InstanceId)
	providerData := map[string]string{
		"accountId":       request.AccountId,
		"provisionerMode": "dry-run",
	}
	for key, value := range request.Tags {
		if strings.TrimSpace(value) != "" {
			providerData[key] = value
		}
	}
	return Response{
		Ok:           true,
		OperationId:  "op-destroy-compute-" + stable[:12],
		PoolId:       request.Pool.Id,
		NodePoolId:   request.Pool.NodePoolId,
		InstanceId:   request.Allocation.InstanceId,
		NodeName:     request.Allocation.NodeName,
		Status:       "destroyed",
		ProviderData: providerData,
	}
}

func dryRunSyncComputeAllocation(request Request) Response {
	stable := stableSuffix(request.AccountId, request.Allocation.Id, request.Allocation.MachineName, request.Allocation.PrivateIp)
	status := firstNonEmpty(request.Allocation.NodeName, request.Allocation.MachineName, request.Allocation.PrivateIp)
	if status != "" {
		status = "running"
	} else {
		status = "external_deleted"
	}
	return Response{
		Ok:           true,
		OperationId:  "op-sync-compute-" + stable[:12],
		PoolId:       request.Pool.Id,
		NodePoolId:   request.Pool.NodePoolId,
		InstanceId:   request.Allocation.InstanceId,
		NodeName:     request.Allocation.NodeName,
		PrivateIp:    request.Allocation.PrivateIp,
		PublicIp:     request.Allocation.PublicIp,
		Status:       status,
		ProviderData: map[string]string{"accountId": request.AccountId, "syncResult": status},
	}
}

func findComputeMachine(machines []*tke2022.Machine, allocation ComputeAllocationInput) *tke2022.Machine {
	machineName := strings.TrimSpace(allocation.MachineName)
	nodeName := strings.TrimSpace(allocation.NodeName)
	privateIP := strings.TrimSpace(allocation.PrivateIp)
	for _, machine := range machines {
		if machine == nil {
			continue
		}
		if machineName != "" && stringValue(machine.MachineName) == machineName {
			return machine
		}
		if privateIP != "" && stringValue(machine.LanIP) == privateIP {
			return machine
		}
		if nodeName != "" && kubernetesNodeName(machine) == nodeName {
			return machine
		}
	}
	return nil
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

func stringsToPtrs(values []string) []*string {
	if len(values) == 0 {
		return nil
	}
	result := make([]*string, 0, len(values))
	for _, value := range values {
		result = append(result, common.StringPtr(value))
	}
	return result
}

func nativeReplicas(pool *tke2022.NodePool) int64 {
	if pool == nil || pool.Native == nil || pool.Native.Replicas == nil {
		return 0
	}
	return *pool.Native.Replicas
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
	response := Response{
		Ok:        false,
		ErrorCode: code,
		Message:   err.Error(),
		Retryable: true,
		ProviderData: map[string]string{
			"providerErrorCode":    code,
			"providerErrorMessage": err.Error(),
		},
	}
	if sdkErr, ok := err.(*tcerrors.TencentCloudSDKError); ok {
		response.ProviderRequestId = sdkErr.RequestId
		response.ProviderData["providerErrorRequestId"] = sdkErr.RequestId
	}
	return response
}
