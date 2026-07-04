package main

import (
	"encoding/json"
	"testing"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	cvm "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cvm/v20170312"
	tke "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/tke/v20180525"
)

func TestReadinessRequiresTencentEnv(t *testing.T) {
	response := handle(Request{Action: "readiness"}, map[string]string{})
	if response.Ok {
		t.Fatalf("expected readiness to fail without Tencent env")
	}
	if response.ErrorCode != "tencent_env_missing" {
		t.Fatalf("unexpected error code: %s", response.ErrorCode)
	}
	if len(response.MissingEnv) == 0 {
		t.Fatalf("expected missing Tencent env keys")
	}
}

func TestCreateComputeAllocationDryRunReturnsOwnership(t *testing.T) {
	env := map[string]string{
		"TENCENTCLOUD_SECRET_ID":     "sid",
		"TENCENTCLOUD_SECRET_KEY":    "skey",
		"TENCENTCLOUD_REGION":        "ap-guangzhou",
		"TENCENT_DEPLOY_CLUSTER_ID":  "cls-123",
		"OPL_TENCENT_DRY_RUN_PREFIX": "test",
	}
	response := handle(Request{
		Action:    "create_compute_allocation",
		DryRun:    true,
		AccountId: "pi-alpha",
		UserId:    "usr-alpha",
		PackageId: "basic",
		Pool: ComputePoolInput{
			Id:           "pool-basic-2c4g",
			InstanceType: "SA5.LARGE4",
			NodePoolId:   "np-basic",
			DesiredNodeLabels: map[string]string{
				"oplcloud.cn/pool-id": "pool-basic-2c4g",
			},
		},
		Allocation: ComputeAllocationInput{Id: "compute-alpha"},
	}, env)
	if !response.Ok {
		t.Fatalf("expected ok response: %#v", response)
	}
	if response.OperationId == "" {
		t.Fatalf("expected operation id: %#v", response)
	}
	if response.PoolId != "pool-basic-2c4g" {
		t.Fatalf("unexpected pool id: %s", response.PoolId)
	}
	if response.NodePoolId != "np-basic" {
		t.Fatalf("unexpected node pool id: %s", response.NodePoolId)
	}
	if response.InstanceId == "" {
		t.Fatalf("expected dry-run instance id: %#v", response)
	}
	if response.NodeName == "" {
		t.Fatalf("expected dry-run node name: %#v", response)
	}
	if response.Status != "provisioning" {
		t.Fatalf("unexpected status: %s", response.Status)
	}
	if response.ProviderData["accountId"] != "pi-alpha" {
		t.Fatalf("expected account ownership in provider data: %#v", response.ProviderData)
	}
}

func TestDestroyComputeAllocationDryRunClosesOwnership(t *testing.T) {
	env := map[string]string{
		"TENCENTCLOUD_SECRET_ID":    "sid",
		"TENCENTCLOUD_SECRET_KEY":   "skey",
		"TENCENTCLOUD_REGION":       "ap-guangzhou",
		"TENCENT_DEPLOY_CLUSTER_ID": "cls-123",
	}
	response := handle(Request{
		Action:    "destroy_compute_allocation",
		DryRun:    true,
		AccountId: "pi-alpha",
		Allocation: ComputeAllocationInput{
			Id:         "compute-alpha",
			InstanceId: "ins-alpha",
			NodeName:   "10.0.0.12",
		},
	}, env)
	if !response.Ok {
		t.Fatalf("expected ok response: %#v", response)
	}
	if response.Status != "destroyed" {
		t.Fatalf("unexpected status: %s", response.Status)
	}
	if response.InstanceId != "ins-alpha" {
		t.Fatalf("unexpected instance id: %s", response.InstanceId)
	}
}

type fakeTencentClient struct {
	createdRequest   Request
	destroyedRequest Request
}

func (client *fakeTencentClient) CreateComputeAllocation(request Request, env map[string]string) Response {
	client.createdRequest = request
	return Response{
		Ok:          true,
		OperationId: "op-live-create",
		PoolId:      request.Pool.Id,
		NodePoolId:  "np-live",
		InstanceId:  "ins-live",
		NodeName:    "node-live",
		Status:      "provisioning",
		ProviderData: map[string]string{
			"client": "fake",
			"region": env["TENCENTCLOUD_REGION"],
		},
	}
}

func (client *fakeTencentClient) DestroyComputeAllocation(request Request, env map[string]string) Response {
	client.destroyedRequest = request
	return Response{
		Ok:          true,
		OperationId: "op-live-destroy",
		InstanceId:  request.Allocation.InstanceId,
		NodeName:    request.Allocation.NodeName,
		Status:      "destroyed",
		ProviderData: map[string]string{
			"client": "fake",
		},
	}
}

func TestCreateComputeAllocationLiveUsesTencentClientBoundary(t *testing.T) {
	env := map[string]string{
		"TENCENTCLOUD_SECRET_ID":    "sid",
		"TENCENTCLOUD_SECRET_KEY":   "skey",
		"TENCENTCLOUD_REGION":       "ap-guangzhou",
		"TENCENT_DEPLOY_CLUSTER_ID": "cls-123",
	}
	client := &fakeTencentClient{}

	response := handleWithClient(Request{
		Action:    "create_compute_allocation",
		AccountId: "pi-alpha",
		UserId:    "usr-alpha",
		PackageId: "basic",
		Pool: ComputePoolInput{
			Id:           "pool-basic-2c4g",
			InstanceType: "SA5.LARGE4",
		},
		Allocation: ComputeAllocationInput{Id: "compute-alpha"},
	}, env, client)

	if !response.Ok {
		t.Fatalf("expected ok response: %#v", response)
	}
	if response.InstanceId != "ins-live" {
		t.Fatalf("expected live client result: %#v", response)
	}
	if client.createdRequest.Allocation.Id != "compute-alpha" {
		t.Fatalf("expected request to reach client: %#v", client.createdRequest)
	}
}

func TestDestroyComputeAllocationLiveUsesTencentClientBoundary(t *testing.T) {
	env := map[string]string{
		"TENCENTCLOUD_SECRET_ID":    "sid",
		"TENCENTCLOUD_SECRET_KEY":   "skey",
		"TENCENTCLOUD_REGION":       "ap-guangzhou",
		"TENCENT_DEPLOY_CLUSTER_ID": "cls-123",
	}
	client := &fakeTencentClient{}

	response := handleWithClient(Request{
		Action:    "destroy_compute_allocation",
		AccountId: "pi-alpha",
		Allocation: ComputeAllocationInput{
			Id:         "compute-alpha",
			InstanceId: "ins-alpha",
			NodeName:   "node-alpha",
		},
	}, env, client)

	if !response.Ok {
		t.Fatalf("expected ok response: %#v", response)
	}
	if response.Status != "destroyed" {
		t.Fatalf("expected destroy result: %#v", response)
	}
	if client.destroyedRequest.Allocation.InstanceId != "ins-alpha" {
		t.Fatalf("expected request to reach client: %#v", client.destroyedRequest)
	}
}

func TestNewTencentSDKClientBuildsTkeAndCvmClients(t *testing.T) {
	env := map[string]string{
		"TENCENTCLOUD_SECRET_ID":    "sid",
		"TENCENTCLOUD_SECRET_KEY":   "skey",
		"TENCENTCLOUD_REGION":       "ap-guangzhou",
		"TENCENT_DEPLOY_CLUSTER_ID": "cls-123",
	}

	client, response := newTencentSDKClient(env)

	if response != nil {
		t.Fatalf("expected SDK client, got response: %#v", response)
	}
	if client == nil {
		t.Fatalf("expected SDK client")
	}
	if client.region != "ap-guangzhou" {
		t.Fatalf("unexpected region: %s", client.region)
	}
	if client.clusterId != "cls-123" {
		t.Fatalf("unexpected cluster id: %s", client.clusterId)
	}
	if client.tkeClient == nil {
		t.Fatalf("expected TKE SDK client")
	}
	if client.cvmClient == nil {
		t.Fatalf("expected CVM SDK client")
	}
}

func TestBuildCreateClusterInstancesRequestUsesPackagePoolAndAllocationOwnership(t *testing.T) {
	env := map[string]string{
		"TENCENT_DEPLOY_CLUSTER_ID":       "cls-123",
		"TENCENT_CVM_ZONE":                "ap-guangzhou-6",
		"TENCENT_CVM_VPC_ID":              "vpc-123",
		"TENCENT_CVM_SUBNET_ID":           "subnet-123",
		"TENCENT_CVM_SECURITY_GROUP_IDS":  "sg-123,sg-456",
		"TENCENT_CVM_IMAGE_ID":            "img-123",
		"TENCENT_CVM_SYSTEM_DISK_TYPE":    "CLOUD_BSSD",
		"TENCENT_CVM_SYSTEM_DISK_SIZE_GB": "80",
	}
	request := Request{
		AccountId: "pi-alpha",
		UserId:    "usr-alpha",
		PackageId: "basic",
		Pool: ComputePoolInput{
			Id:           "pool-basic-2c4g",
			InstanceType: "SA5.LARGE4",
			NodePoolId:   "np-basic",
		},
		Allocation: ComputeAllocationInput{Id: "compute-alpha"},
	}

	createRequest, response := buildCreateClusterInstancesRequest(request, env)

	if response != nil {
		t.Fatalf("expected request, got response: %#v", response)
	}
	if createRequest.ClusterId == nil || *createRequest.ClusterId != "cls-123" {
		t.Fatalf("unexpected cluster id: %#v", createRequest.ClusterId)
	}
	var runInstances map[string]any
	if err := json.Unmarshal([]byte(*createRequest.RunInstancePara), &runInstances); err != nil {
		t.Fatalf("invalid RunInstancePara: %v", err)
	}
	if runInstances["InstanceType"] != "SA5.LARGE4" {
		t.Fatalf("unexpected instance type: %#v", runInstances)
	}
	if runInstances["ClientToken"] != "opl-compute-alpha" {
		t.Fatalf("unexpected client token: %#v", runInstances)
	}
	if runInstances["InstanceName"] != "opl-compute-alpha" {
		t.Fatalf("unexpected instance name: %#v", runInstances)
	}
	if runInstances["ImageId"] != "img-123" {
		t.Fatalf("unexpected image id: %#v", runInstances)
	}
	tags := runInstances["Tags"].([]any)
	if len(tags) < 4 {
		t.Fatalf("expected ownership tags: %#v", tags)
	}
	settings := createRequest.InstanceAdvancedSettings
	if settings == nil || settings.Labels == nil || settings.Labels[0].Name == nil || *settings.Labels[0].Name != "oplcloud.cn/compute-allocation-id" {
		t.Fatalf("expected allocation label in advanced settings: %#v", settings)
	}
}

func TestBuildAddNodeToPoolRequestTargetsExistingPackagePool(t *testing.T) {
	request := Request{
		Pool: ComputePoolInput{
			Id:         "pool-basic-2c4g",
			NodePoolId: "np-basic",
		},
		Allocation: ComputeAllocationInput{Id: "compute-alpha"},
	}

	addRequest, response := buildAddNodeToPoolRequest(request, "cls-123", "ins-alpha")

	if response != nil {
		t.Fatalf("expected request, got response: %#v", response)
	}
	if addRequest.ClusterId == nil || *addRequest.ClusterId != "cls-123" {
		t.Fatalf("unexpected cluster id: %#v", addRequest.ClusterId)
	}
	if addRequest.NodePoolId == nil || *addRequest.NodePoolId != "np-basic" {
		t.Fatalf("unexpected node pool id: %#v", addRequest.NodePoolId)
	}
	if len(addRequest.InstanceIds) != 1 || *addRequest.InstanceIds[0] != "ins-alpha" {
		t.Fatalf("unexpected instance ids: %#v", addRequest.InstanceIds)
	}
}

type fakeTkeAPI struct {
	createNodePoolRequest *tke.CreateClusterNodePoolRequest
	createRequest         *tke.CreateClusterInstancesRequest
	addRequest            *tke.AddNodeToNodePoolRequest
	calls                 []string
}

func (api *fakeTkeAPI) CreateClusterNodePool(request *tke.CreateClusterNodePoolRequest) (*tke.CreateClusterNodePoolResponse, error) {
	api.calls = append(api.calls, "CreateClusterNodePool")
	api.createNodePoolRequest = request
	return &tke.CreateClusterNodePoolResponse{
		Response: &tke.CreateClusterNodePoolResponseParams{
			NodePoolId: common.StringPtr("np-created"),
			RequestId:  common.StringPtr("req-create-pool"),
		},
	}, nil
}

func (api *fakeTkeAPI) CreateClusterInstances(request *tke.CreateClusterInstancesRequest) (*tke.CreateClusterInstancesResponse, error) {
	api.calls = append(api.calls, "CreateClusterInstances")
	api.createRequest = request
	return &tke.CreateClusterInstancesResponse{
		Response: &tke.CreateClusterInstancesResponseParams{
			InstanceIdSet: []*string{common.StringPtr("ins-created")},
			RequestId:     common.StringPtr("req-create"),
		},
	}, nil
}

func (api *fakeTkeAPI) AddNodeToNodePool(request *tke.AddNodeToNodePoolRequest) (*tke.AddNodeToNodePoolResponse, error) {
	api.calls = append(api.calls, "AddNodeToNodePool")
	api.addRequest = request
	return &tke.AddNodeToNodePoolResponse{
		Response: &tke.AddNodeToNodePoolResponseParams{
			RequestId: common.StringPtr("req-add-node"),
		},
	}, nil
}

type fakeCvmAPI struct {
	terminateRequest *cvm.TerminateInstancesRequest
}

func (api *fakeCvmAPI) TerminateInstances(request *cvm.TerminateInstancesRequest) (*cvm.TerminateInstancesResponse, error) {
	api.terminateRequest = request
	return &cvm.TerminateInstancesResponse{
		Response: &cvm.TerminateInstancesResponseParams{
			RequestId: common.StringPtr("req-terminate"),
		},
	}, nil
}

func TestTencentSDKClientCreateAllocationCreatesClusterInstanceAndAddsItToNodePool(t *testing.T) {
	tkeAPI := &fakeTkeAPI{}
	client := &tencentSDKClient{
		region:    "ap-guangzhou",
		clusterId: "cls-123",
		tkeClient: tkeAPI,
	}
	env := map[string]string{
		"TENCENT_DEPLOY_CLUSTER_ID":       "cls-123",
		"TENCENT_CVM_ZONE":                "ap-guangzhou-6",
		"TENCENT_CVM_VPC_ID":              "vpc-123",
		"TENCENT_CVM_SUBNET_ID":           "subnet-123",
		"TENCENT_CVM_SECURITY_GROUP_IDS":  "sg-123",
		"TENCENT_CVM_IMAGE_ID":            "img-123",
		"TENCENT_CVM_SYSTEM_DISK_TYPE":    "CLOUD_BSSD",
		"TENCENT_CVM_SYSTEM_DISK_SIZE_GB": "80",
	}

	response := client.CreateComputeAllocation(Request{
		AccountId: "pi-alpha",
		UserId:    "usr-alpha",
		PackageId: "basic",
		Pool: ComputePoolInput{
			Id:           "pool-basic-2c4g",
			InstanceType: "SA5.LARGE4",
			NodePoolId:   "np-basic",
		},
		Allocation: ComputeAllocationInput{Id: "compute-alpha"},
	}, env)

	if !response.Ok {
		t.Fatalf("expected ok response: %#v", response)
	}
	if response.InstanceId != "ins-created" {
		t.Fatalf("unexpected instance id: %#v", response)
	}
	if response.NodePoolId != "np-basic" {
		t.Fatalf("unexpected node pool id: %#v", response)
	}
	if response.ProviderRequestId != "req-create" {
		t.Fatalf("unexpected request id: %#v", response)
	}
	if tkeAPI.createRequest == nil {
		t.Fatalf("expected CreateClusterInstances call")
	}
	if tkeAPI.addRequest == nil || *tkeAPI.addRequest.InstanceIds[0] != "ins-created" {
		t.Fatalf("expected AddNodeToNodePool call: %#v", tkeAPI.addRequest)
	}
	if response.ProviderData["addNodeRequestId"] != "req-add-node" {
		t.Fatalf("expected add node request id: %#v", response.ProviderData)
	}
}

func TestTencentSDKClientCreateAllocationCreatesMissingPackageNodePool(t *testing.T) {
	tkeAPI := &fakeTkeAPI{}
	client := &tencentSDKClient{
		region:    "ap-guangzhou",
		clusterId: "cls-123",
		tkeClient: tkeAPI,
	}
	env := map[string]string{
		"TENCENT_DEPLOY_CLUSTER_ID":                "cls-123",
		"TENCENT_CVM_ZONE":                         "ap-guangzhou-6",
		"TENCENT_CVM_VPC_ID":                       "vpc-123",
		"TENCENT_CVM_SUBNET_ID":                    "subnet-123",
		"TENCENT_CVM_SECURITY_GROUP_IDS":           "sg-123",
		"TENCENT_CVM_IMAGE_ID":                     "img-123",
		"TENCENT_TKE_NODE_POOL_ASG_PARA_JSON":      `{"MinSize":0,"MaxSize":10,"DesiredCapacity":0}`,
		"TENCENT_TKE_NODE_POOL_LAUNCH_CONFIG_JSON": `{"InstanceType":"SA5.LARGE4"}`,
		"TENCENT_TKE_NODE_POOL_CONTAINER_RUNTIME":  "containerd",
		"TENCENT_TKE_NODE_POOL_RUNTIME_VERSION":    "1.6.9",
		"TENCENT_TKE_NODE_POOL_OS":                 "tlinux3.1x86_64",
	}

	response := client.CreateComputeAllocation(Request{
		AccountId: "pi-alpha",
		UserId:    "usr-alpha",
		PackageId: "basic",
		Pool: ComputePoolInput{
			Id:           "pool-basic-2c4g",
			InstanceType: "SA5.LARGE4",
		},
		Allocation: ComputeAllocationInput{Id: "compute-alpha"},
	}, env)

	if !response.Ok {
		t.Fatalf("expected ok response: %#v", response)
	}
	if tkeAPI.createNodePoolRequest == nil {
		t.Fatalf("expected CreateClusterNodePool call")
	}
	if tkeAPI.createNodePoolRequest.Name == nil || *tkeAPI.createNodePoolRequest.Name != "pool-basic-2c4g" {
		t.Fatalf("unexpected node pool name: %#v", tkeAPI.createNodePoolRequest.Name)
	}
	if response.NodePoolId != "np-created" {
		t.Fatalf("expected created node pool id: %#v", response)
	}
	if tkeAPI.addRequest == nil || *tkeAPI.addRequest.NodePoolId != "np-created" {
		t.Fatalf("expected add node to created node pool: %#v", tkeAPI.addRequest)
	}
	if response.ProviderData["createNodePoolRequestId"] != "req-create-pool" {
		t.Fatalf("expected node pool request id: %#v", response.ProviderData)
	}
	expectedCalls := []string{"CreateClusterNodePool", "CreateClusterInstances", "AddNodeToNodePool"}
	if len(tkeAPI.calls) != len(expectedCalls) {
		t.Fatalf("unexpected call order: %#v", tkeAPI.calls)
	}
	for index, expected := range expectedCalls {
		if tkeAPI.calls[index] != expected {
			t.Fatalf("unexpected call order: %#v", tkeAPI.calls)
		}
	}
}

func TestTencentSDKClientDestroyAllocationTerminatesOwnedInstance(t *testing.T) {
	cvmAPI := &fakeCvmAPI{}
	client := &tencentSDKClient{
		region:    "ap-guangzhou",
		clusterId: "cls-123",
		cvmClient: cvmAPI,
	}

	response := client.DestroyComputeAllocation(Request{
		AccountId: "pi-alpha",
		Allocation: ComputeAllocationInput{
			Id:         "compute-alpha",
			InstanceId: "ins-created",
			NodeName:   "node-created",
		},
	}, map[string]string{})

	if !response.Ok {
		t.Fatalf("expected ok response: %#v", response)
	}
	if response.Status != "destroyed" {
		t.Fatalf("unexpected status: %#v", response)
	}
	if cvmAPI.terminateRequest == nil || len(cvmAPI.terminateRequest.InstanceIds) != 1 || *cvmAPI.terminateRequest.InstanceIds[0] != "ins-created" {
		t.Fatalf("expected TerminateInstances call: %#v", cvmAPI.terminateRequest)
	}
	if cvmAPI.terminateRequest.ReleasePrepaidDataDisks == nil || *cvmAPI.terminateRequest.ReleasePrepaidDataDisks {
		t.Fatalf("compute destroy must not release retained data disks by default")
	}
}
