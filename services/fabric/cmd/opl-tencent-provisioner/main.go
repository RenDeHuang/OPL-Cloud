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
	"time"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	cvm2017 "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cvm/v20170312"
	tke2022 "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/tke/v20220501"
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
	Tags       map[string]string      `json:"tags,omitempty"`
	Pool       ComputePoolInput       `json:"pool,omitempty"`
	Allocation ComputeAllocationInput `json:"allocation,omitempty"`
}

type ComputePoolInput struct {
	Id                string            `json:"id,omitempty"`
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
	PublicIp          string            `json:"publicIp,omitempty"`
	Status            string            `json:"status,omitempty"`
	ProviderRequestId string            `json:"providerRequestId,omitempty"`
	ProviderData      map[string]string `json:"providerData,omitempty"`
	ErrorCode         string            `json:"errorCode,omitempty"`
	Message           string            `json:"message,omitempty"`
	Retryable         bool              `json:"retryable,omitempty"`
	MissingEnv        []string          `json:"missingEnv,omitempty"`
	Machines          []MachineOutput   `json:"machines,omitempty"`
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
	DescribeInstances(request *cvm2017.DescribeInstancesRequest) (*cvm2017.DescribeInstancesResponse, error)
	ModifyInstancesAttribute(request *cvm2017.ModifyInstancesAttributeRequest) (*cvm2017.ModifyInstancesAttributeResponse, error)
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

	return &tencentSDKClient{
		region:          env["TENCENTCLOUD_REGION"],
		clusterId:       env["TENCENT_DEPLOY_CLUSTER_ID"],
		nativeTkeClient: tkeClient,
		nativeCvmClient: cvmClient,
	}, nil
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
		if err != nil && !isNodePoolNotFound(err) {
			response := sdkErrorResponse("tencent_describe_node_pool_failed", err)
			return response
		}
		if err == nil {
			pool = describedPool
			describeRequestId = requestId
		} else {
			nodePoolId = ""
		}
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
	tkeClusterInstanceId := ""
	cvmInstance, cvmRequestId, err := client.describeCvmInstanceByPrivateIp(privateIp)
	if err == nil {
		instanceId = stringValue(cvmInstance.InstanceId)
		publicIp = firstString(cvmInstance.PublicIpAddresses)
		instanceIdentitySource = "cvm"
	} else {
		tkeInstance, tkeInstanceRequestId, tkeErr := client.describeTkeClusterInstanceByPrivateIp(privateIp, nodePoolId)
		if tkeErr != nil {
			response := sdkErrorResponse("tencent_describe_cvm_instance_failed", err)
			response.ProviderRequestId = firstNonEmpty(cvmRequestId, tkeInstanceRequestId, machineRequestId, scaleRequestId)
			return response
		}
		cvmRequestId = firstNonEmpty(cvmRequestId, tkeInstanceRequestId)
		tkeClusterInstanceId = stringValue(tkeInstance.InstanceId)
		instanceIdentitySource = "tke_cluster_instance"
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
			"tkeClusterInstanceId":        tkeClusterInstanceId,
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
	if nodePoolId != "" {
		described, id, err := client.describeNativeNodePool(nodePoolId)
		if err == nil {
			pool, requestId = described, id
		} else if !isNodePoolNotFound(err) {
			return sdkErrorResponse("tencent_describe_node_pool_failed", err)
		}
	}
	if pool == nil {
		discovered, id, err := client.discoverNativeNodePool(request)
		if err != nil {
			return sdkErrorResponse("tencent_describe_node_pool_failed", err)
		}
		if discovered != nil {
			pool, requestId = discovered, id
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
	}
	current := nativeReplicas(pool)
	if current != request.Pool.DesiredReplicas {
		scaleRequest := tke2022.NewScaleNodePoolRequest()
		scaleRequest.ClusterId = common.StringPtr(client.clusterId)
		scaleRequest.NodePoolId = common.StringPtr(nodePoolId)
		scaleRequest.Replicas = common.Int64Ptr(request.Pool.DesiredReplicas)
		scaled, err := client.nativeTkeClient.ScaleNodePool(scaleRequest)
		if err != nil {
			response := sdkErrorResponse("tencent_scale_node_pool_failed", err)
			response.ProviderRequestId = requestId
			return response
		}
		requestId = firstNonEmpty(stringValue(scaled.Response.RequestId), requestId)
	}
	machines, describeRequestId, err := client.describeClusterMachines(nodePoolId)
	if err != nil {
		response := sdkErrorResponse("tencent_describe_cluster_machines_failed", err)
		response.ProviderRequestId = requestId
		return response
	}
	output := make([]MachineOutput, 0, len(machines))
	for _, machine := range machines {
		if machine == nil || stringValue(machine.MachineName) == "" {
			continue
		}
		state := strings.ToLower(strings.TrimSpace(stringValue(machine.MachineState)))
		ready := state == "" || state == "running" || state == "normal" || state == "ready"
		privateIp := stringValue(machine.LanIP)
		instanceId, publicIp := "", ""
		if cvmInstance, _, resolveErr := client.describeCvmInstanceByPrivateIp(privateIp); resolveErr == nil {
			instanceId = stringValue(cvmInstance.InstanceId)
			publicIp = firstString(cvmInstance.PublicIpAddresses)
		} else {
			ready = false
		}
		output = append(output, MachineOutput{MachineId: stringValue(machine.MachineName), InstanceId: instanceId, NodeName: kubernetesNodeName(machine), PrivateIp: privateIp, PublicIp: publicIp, InstanceType: stringValue(machine.InstanceType), Ready: ready && instanceId != ""})
	}
	return Response{Ok: true, OperationId: "op-reconcile-pool-" + stableSuffix(nodePoolId, fmt.Sprintf("%d", request.Pool.DesiredReplicas))[:12], PoolId: request.Pool.Id, NodePoolId: nodePoolId, Status: "reconciling", ProviderRequestId: firstNonEmpty(describeRequestId, requestId), Machines: output, ProviderData: map[string]string{"currentReplicas": fmt.Sprintf("%d", len(machines)), "desiredReplicas": fmt.Sprintf("%d", request.Pool.DesiredReplicas)}}
}

func (client *tencentSDKClient) TagComputeMachine(request Request, _ map[string]string) Response {
	if client == nil || client.nativeCvmClient == nil {
		return Response{Ok: false, ErrorCode: "tencent_sdk_client_missing", Message: "Tencent CVM SDK client is missing.", Retryable: false}
	}
	instanceID := strings.TrimSpace(request.Allocation.InstanceId)
	resourceID := strings.TrimSpace(request.Tags["opl_resource_id"])
	if instanceID == "" || resourceID == "" || len(resourceID) > 60 {
		return Response{Ok: false, ErrorCode: "compute_machine_identity_required", Message: "CVM instance id and a resource id of at most 60 characters are required.", Retryable: false}
	}
	modify := cvm2017.NewModifyInstancesAttributeRequest()
	modify.InstanceIds = []*string{common.StringPtr(instanceID)}
	modify.InstanceName = common.StringPtr(resourceID)
	modified, err := client.nativeCvmClient.ModifyInstancesAttribute(modify)
	if err != nil {
		return sdkErrorResponse("tencent_tag_compute_machine_failed", err)
	}
	describe := cvm2017.NewDescribeInstancesRequest()
	describe.InstanceIds = []*string{common.StringPtr(instanceID)}
	described, err := client.nativeCvmClient.DescribeInstances(describe)
	if err != nil {
		return sdkErrorResponse("tencent_verify_compute_machine_tag_failed", err)
	}
	if described.Response == nil || len(described.Response.InstanceSet) != 1 || stringValue(described.Response.InstanceSet[0].InstanceId) != instanceID || stringValue(described.Response.InstanceSet[0].InstanceName) != resourceID {
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
	}
	if err != nil {
		return nil, "", err
	}
	return describeResponse.Response.Machines, stringValue(describeResponse.Response.RequestId), nil
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
	requestId := ""
	if describeResponse != nil && describeResponse.Response != nil {
		requestId = stringValue(describeResponse.Response.RequestId)
		if len(describeResponse.Response.InstanceSet) > 0 {
			return describeResponse.Response.InstanceSet[0], requestId, nil
		}
	}
	return nil, requestId, fmt.Errorf("CVM instance not found for private IP %s", privateIp)
}

func (client *tencentSDKClient) describeTkeClusterInstanceByPrivateIp(privateIp string, nodePoolId string) (*tke2022.Instance, string, error) {
	if strings.TrimSpace(privateIp) == "" {
		return nil, "", fmt.Errorf("private IP is required to resolve TKE instance identity")
	}
	if client == nil || client.nativeTkeClient == nil {
		return nil, "", fmt.Errorf("Tencent TKE SDK client is missing")
	}
	describeRequest := tke2022.NewDescribeClusterInstancesRequest()
	describeRequest.ClusterId = common.StringPtr(client.clusterId)
	describeRequest.Limit = common.Int64Ptr(100)
	describeRequest.Filters = []*tke2022.Filter{
		{Name: common.StringPtr("VagueIpAddress"), Values: []*string{common.StringPtr(privateIp)}},
	}
	if strings.TrimSpace(nodePoolId) != "" {
		describeRequest.Filters = append(describeRequest.Filters,
			&tke2022.Filter{Name: common.StringPtr("NodePoolIds"), Values: []*string{common.StringPtr(nodePoolId)}})
	}
	describeResponse, err := client.nativeTkeClient.DescribeClusterInstances(describeRequest)
	if err != nil {
		return nil, "", err
	}
	requestId := ""
	if describeResponse != nil && describeResponse.Response != nil {
		requestId = stringValue(describeResponse.Response.RequestId)
		for _, instance := range describeResponse.Response.InstanceSet {
			if instance == nil {
				continue
			}
			if stringValue(instance.LanIP) == privateIp && (nodePoolId == "" || stringValue(instance.NodePoolId) == "" || stringValue(instance.NodePoolId) == nodePoolId) {
				return instance, requestId, nil
			}
		}
	}
	return nil, requestId, fmt.Errorf("TKE instance not found for private IP %s", privateIp)
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
	if len(describeResponse.Response.NodePools) == 0 {
		return nil, stringValue(describeResponse.Response.RequestId), fmt.Errorf("node pool not found: %s", nodePoolId)
	}
	return describeResponse.Response.NodePools[0], stringValue(describeResponse.Response.RequestId), nil
}

func (client *tencentSDKClient) discoverNativeNodePool(request Request) (*tke2022.NodePool, string, error) {
	describeRequest := tke2022.NewDescribeNodePoolsRequest()
	describeRequest.ClusterId = common.StringPtr(client.clusterId)
	describeRequest.Limit = common.Int64Ptr(100)
	describeResponse, err := client.nativeTkeClient.DescribeNodePools(describeRequest)
	if err != nil {
		return nil, "", err
	}
	requestId := stringValue(describeResponse.Response.RequestId)
	for _, pool := range describeResponse.Response.NodePools {
		if matchesPackageNodePool(pool, request) {
			return pool, requestId, nil
		}
	}
	return nil, requestId, nil
}

func matchesPackageNodePool(pool *tke2022.NodePool, request Request) bool {
	if pool == nil || isDeletingNodePool(pool) {
		return false
	}
	labels := nodePoolLabels(pool)
	if request.Pool.Id != "" && labels["oplcloud.cn/pool-id"] == request.Pool.Id {
		return true
	}
	if request.PackageId != "" && request.Pool.InstanceType != "" {
		return labels["oplcloud.cn/package-id"] == request.PackageId &&
			labels["oplcloud.cn/instance-type"] == request.Pool.InstanceType
	}
	return request.Pool.Id != "" && stringValue(pool.Name) == request.Pool.Id
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

func isNodePoolNotFound(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "node pool not found")
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
		MachineType:        common.StringPtr("Native"),
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
	return Response{
		Ok:        false,
		ErrorCode: code,
		Message:   err.Error(),
		Retryable: true,
	}
}
