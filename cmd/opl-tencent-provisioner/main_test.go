package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	cvm2017 "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cvm/v20170312"
	tke2022 "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/tke/v20220501"
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
		},
		Allocation: ComputeAllocationInput{Id: "compute-alpha"},
	}, env)
	if !response.Ok {
		t.Fatalf("expected ok response: %#v", response)
	}
	if response.NodePoolId != "np-basic" {
		t.Fatalf("unexpected node pool id: %s", response.NodePoolId)
	}
	if response.InstanceId == "" {
		t.Fatalf("expected dry-run instance id: %#v", response)
	}
	if response.ProviderData["accountId"] != "pi-alpha" {
		t.Fatalf("expected account ownership in provider data: %#v", response.ProviderData)
	}
}

func TestLiveComputeAllocationRequiresSafetyFlag(t *testing.T) {
	env := map[string]string{
		"TENCENTCLOUD_SECRET_ID":    "sid",
		"TENCENTCLOUD_SECRET_KEY":   "skey",
		"TENCENTCLOUD_REGION":       "ap-guangzhou",
		"TENCENT_DEPLOY_CLUSTER_ID": "cls-123",
	}

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
	}, env, unimplementedTencentClient{})

	if response.Ok {
		t.Fatalf("expected live mutation to require safety flag")
	}
	if response.ErrorCode != "live_mutation_flag_required" {
		t.Fatalf("unexpected error code: %s", response.ErrorCode)
	}
}

func TestDestroyComputeAllocationDryRunClosesOwnership(t *testing.T) {
	env := map[string]string{
		"TENCENTCLOUD_SECRET_ID":               "sid",
		"TENCENTCLOUD_SECRET_KEY":              "skey",
		"TENCENTCLOUD_REGION":                  "ap-guangzhou",
		"TENCENT_DEPLOY_CLUSTER_ID":            "cls-123",
		"RUN_TENCENT_CREATE_RELEASE_EXECUTION": "1",
	}
	response := handle(Request{
		Action:    "destroy_compute_allocation",
		DryRun:    true,
		AccountId: "pi-alpha",
		Pool:      ComputePoolInput{Id: "pool-basic-2c4g", NodePoolId: "np-basic"},
		Allocation: ComputeAllocationInput{
			Id:         "compute-alpha",
			InstanceId: "ins-alpha",
			NodeName:   "node-alpha",
		},
	}, env)
	if !response.Ok {
		t.Fatalf("expected ok response: %#v", response)
	}
	if response.Status != "destroyed" {
		t.Fatalf("unexpected status: %s", response.Status)
	}
	if response.NodePoolId != "np-basic" {
		t.Fatalf("unexpected node pool id: %s", response.NodePoolId)
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
		NodePoolId:  request.Pool.NodePoolId,
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
		"TENCENTCLOUD_SECRET_ID":               "sid",
		"TENCENTCLOUD_SECRET_KEY":              "skey",
		"TENCENTCLOUD_REGION":                  "ap-guangzhou",
		"TENCENT_DEPLOY_CLUSTER_ID":            "cls-123",
		"RUN_TENCENT_CREATE_RELEASE_EXECUTION": "1",
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
	if response.NodePoolId != "np-live" {
		t.Fatalf("expected live client result: %#v", response)
	}
	if client.createdRequest.Allocation.Id != "compute-alpha" {
		t.Fatalf("expected request to reach client: %#v", client.createdRequest)
	}
}

func TestDestroyComputeAllocationLiveUsesTencentClientBoundary(t *testing.T) {
	env := map[string]string{
		"TENCENTCLOUD_SECRET_ID":               "sid",
		"TENCENTCLOUD_SECRET_KEY":              "skey",
		"TENCENTCLOUD_REGION":                  "ap-guangzhou",
		"TENCENT_DEPLOY_CLUSTER_ID":            "cls-123",
		"RUN_TENCENT_CREATE_RELEASE_EXECUTION": "1",
	}
	client := &fakeTencentClient{}

	response := handleWithClient(Request{
		Action:    "destroy_compute_allocation",
		AccountId: "pi-alpha",
		Pool:      ComputePoolInput{Id: "pool-basic-2c4g", NodePoolId: "np-basic"},
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
	if client.destroyedRequest.Allocation.NodeName != "node-alpha" {
		t.Fatalf("expected request to reach client: %#v", client.destroyedRequest)
	}
}

func TestNewTencentSDKClientBuildsNativeTkeClient(t *testing.T) {
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
	if client.nativeTkeClient == nil {
		t.Fatalf("expected native TKE SDK client")
	}
}

func TestBuildCreateNativeNodePoolRequestUsesCurrentPackageShape(t *testing.T) {
	env := map[string]string{
		"TENCENT_DEPLOY_CLUSTER_ID":       "cls-123",
		"TENCENT_CVM_SUBNET_ID":           "subnet-123",
		"TENCENT_CVM_SECURITY_GROUP_IDS":  "sg-123",
		"TENCENT_CVM_SYSTEM_DISK_TYPE":    "CLOUD_BSSD",
		"TENCENT_CVM_SYSTEM_DISK_SIZE_GB": "50",
	}
	request := Request{
		AccountId: "pi-alpha",
		UserId:    "usr-alpha",
		PackageId: "basic",
		Pool: ComputePoolInput{
			Id:           "pool-basic-2c4g",
			InstanceType: "SA5.LARGE4",
		},
		Allocation: ComputeAllocationInput{Id: "compute-alpha"},
	}

	createRequest, response := buildCreateNativeNodePoolRequest(request, env)

	if response != nil {
		t.Fatalf("expected request, got response: %#v", response)
	}
	if createRequest.ClusterId == nil || *createRequest.ClusterId != "cls-123" {
		t.Fatalf("unexpected cluster id: %#v", createRequest.ClusterId)
	}
	if createRequest.Type == nil || *createRequest.Type != "Native" {
		t.Fatalf("expected native node pool: %#v", createRequest.Type)
	}
	if createRequest.Name == nil || *createRequest.Name != "pool-basic-2c4g" {
		t.Fatalf("unexpected name: %#v", createRequest.Name)
	}
	if createRequest.Native == nil {
		t.Fatalf("expected native config")
	}
	if createRequest.Native.Replicas == nil || *createRequest.Native.Replicas != 0 {
		t.Fatalf("node pool creation must not allocate a CVM immediately: %#v", createRequest.Native.Replicas)
	}
	if createRequest.Native.EnableAutoscaling == nil || *createRequest.Native.EnableAutoscaling {
		t.Fatalf("Fabric-managed package node pools must disable TKE autoscaling: %#v", createRequest.Native.EnableAutoscaling)
	}
	if len(createRequest.Native.InstanceTypes) != 1 || *createRequest.Native.InstanceTypes[0] != "SA5.LARGE4" {
		t.Fatalf("unexpected instance types: %#v", createRequest.Native.InstanceTypes)
	}
	if len(createRequest.Native.SecurityGroupIds) != 1 || *createRequest.Native.SecurityGroupIds[0] != "sg-123" {
		t.Fatalf("unexpected security groups: %#v", createRequest.Native.SecurityGroupIds)
	}
	labels := map[string]string{}
	for _, label := range createRequest.Labels {
		if label.Name != nil && label.Value != nil {
			labels[*label.Name] = *label.Value
		}
	}
	if labels["oplcloud.cn/pool-id"] != "pool-basic-2c4g" || labels["oplcloud.cn/package-id"] != "basic" || labels["oplcloud.cn/instance-type"] != "SA5.LARGE4" {
		t.Fatalf("unexpected labels: %#v", labels)
	}
}

type fakeNativeTkeAPI struct {
	createNodePoolRequest    *tke2022.CreateNodePoolRequest
	describeInstancesRequest []*tke2022.DescribeClusterInstancesRequest
	describeMachinesRequest  []*tke2022.DescribeClusterMachinesRequest
	describeNodePoolsRequest []*tke2022.DescribeNodePoolsRequest
	modifyNodePoolRequest    *tke2022.ModifyNodePoolRequest
	scaleNodePoolRequest     *tke2022.ScaleNodePoolRequest
	deleteMachinesRequest    *tke2022.DeleteClusterMachinesRequest
	nodePoolId               string
	discoverNodePoolId       string
	replicas                 int64
	enableAutoscaling        bool
	rejectMachinePoolFilter  bool
	calls                    []string
}

type fakeNativeCvmAPI struct {
	describeInstancesRequest []*cvm2017.DescribeInstancesRequest
	empty                    bool
}

func (api *fakeNativeCvmAPI) DescribeInstances(request *cvm2017.DescribeInstancesRequest) (*cvm2017.DescribeInstancesResponse, error) {
	api.describeInstancesRequest = append(api.describeInstancesRequest, request)
	if api.empty {
		return &cvm2017.DescribeInstancesResponse{
			Response: &cvm2017.DescribeInstancesResponseParams{
				InstanceSet: []*cvm2017.Instance{},
				TotalCount:  common.Int64Ptr(0),
				RequestId:   common.StringPtr("req-describe-cvm-empty"),
			},
		}, nil
	}
	privateIp := cvmPrivateIpFilterValue(request)
	instanceIndex := 1
	parts := strings.Split(privateIp, ".")
	if len(parts) == 4 {
		if last, err := strconv.Atoi(parts[3]); err == nil && last > 10 {
			instanceIndex = last - 10
		}
	}
	publicIp := fmt.Sprintf("203.0.113.%d", instanceIndex)
	return &cvm2017.DescribeInstancesResponse{
		Response: &cvm2017.DescribeInstancesResponseParams{
			InstanceSet: []*cvm2017.Instance{{
				InstanceId:         common.StringPtr(fmt.Sprintf("ins-basic-%d", instanceIndex)),
				InstanceName:       common.StringPtr(fmt.Sprintf("node-basic-%d", instanceIndex)),
				PrivateIpAddresses: []*string{common.StringPtr(privateIp)},
				PublicIpAddresses:  []*string{common.StringPtr(publicIp)},
			}},
			TotalCount: common.Int64Ptr(1),
			RequestId:  common.StringPtr("req-describe-cvm"),
		},
	}, nil
}

func newFakeTencentSDKClient(tkeAPI *fakeNativeTkeAPI) *tencentSDKClient {
	return &tencentSDKClient{
		region:          "ap-guangzhou",
		clusterId:       "cls-123",
		nativeTkeClient: tkeAPI,
		nativeCvmClient: &fakeNativeCvmAPI{},
	}
}

func (api *fakeNativeTkeAPI) CreateNodePool(request *tke2022.CreateNodePoolRequest) (*tke2022.CreateNodePoolResponse, error) {
	api.calls = append(api.calls, "CreateNodePool")
	api.createNodePoolRequest = request
	api.nodePoolId = "np-created"
	api.replicas = 0
	return &tke2022.CreateNodePoolResponse{
		Response: &tke2022.CreateNodePoolResponseParams{
			NodePoolId: common.StringPtr(api.nodePoolId),
			RequestId:  common.StringPtr("req-create-pool"),
		},
	}, nil
}

func (api *fakeNativeTkeAPI) DescribeNodePools(request *tke2022.DescribeNodePoolsRequest) (*tke2022.DescribeNodePoolsResponse, error) {
	api.calls = append(api.calls, "DescribeNodePools")
	api.describeNodePoolsRequest = append(api.describeNodePoolsRequest, request)
	nodePoolId := api.nodePoolId
	if nodePoolId == "" {
		nodePoolId = "np-basic"
	}
	if filterValue := nodePoolIdFilterValue(request); filterValue != "" && filterValue != nodePoolId {
		return &tke2022.DescribeNodePoolsResponse{
			Response: &tke2022.DescribeNodePoolsResponseParams{
				NodePools:  []*tke2022.NodePool{},
				TotalCount: common.Int64Ptr(0),
				RequestId:  common.StringPtr("req-describe-missing"),
			},
		}, nil
	}
	if nodePoolIdFilterValue(request) == "" {
		if api.discoverNodePoolId == "" {
			return &tke2022.DescribeNodePoolsResponse{
				Response: &tke2022.DescribeNodePoolsResponseParams{
					NodePools:  []*tke2022.NodePool{},
					TotalCount: common.Int64Ptr(0),
					RequestId:  common.StringPtr("req-discover-pool"),
				},
			}, nil
		}
		nodePoolId = api.discoverNodePoolId
	}
	return &tke2022.DescribeNodePoolsResponse{
		Response: &tke2022.DescribeNodePoolsResponseParams{
			NodePools: []*tke2022.NodePool{{
				NodePoolId: common.StringPtr(nodePoolId),
				Name:       common.StringPtr("pool-basic-2c4g"),
				Type:       common.StringPtr("Native"),
				LifeState:  common.StringPtr("Running"),
				Labels: []*tke2022.Label{
					{Name: common.StringPtr("oplcloud.cn/pool-id"), Value: common.StringPtr("pool-basic-2c4g")},
					{Name: common.StringPtr("oplcloud.cn/package-id"), Value: common.StringPtr("basic")},
					{Name: common.StringPtr("oplcloud.cn/instance-type"), Value: common.StringPtr("SA5.LARGE4")},
				},
				Native: &tke2022.NativeNodePoolInfo{
					Replicas:          common.Int64Ptr(api.replicas),
					EnableAutoscaling: common.BoolPtr(api.enableAutoscaling),
				},
			}},
			TotalCount: common.Int64Ptr(1),
			RequestId:  common.StringPtr("req-describe-pool"),
		},
	}, nil
}

func (api *fakeNativeTkeAPI) DescribeClusterInstances(request *tke2022.DescribeClusterInstancesRequest) (*tke2022.DescribeClusterInstancesResponse, error) {
	api.calls = append(api.calls, "DescribeClusterInstances")
	api.describeInstancesRequest = append(api.describeInstancesRequest, request)
	privateIp := clusterInstanceFilterValue(request, "VagueIpAddress")
	nodePoolId := clusterInstanceFilterValue(request, "NodePoolIds")
	instances := []*tke2022.Instance{}
	for index := int64(1); index <= api.replicas; index++ {
		lanIp := fmt.Sprintf("10.0.0.%d", index+10)
		if privateIp != "" && privateIp != lanIp {
			continue
		}
		currentNodePoolId := api.nodePoolId
		if currentNodePoolId == "" {
			currentNodePoolId = "np-basic"
		}
		if nodePoolId != "" && nodePoolId != currentNodePoolId {
			continue
		}
		instances = append(instances, &tke2022.Instance{
			InstanceId:    common.StringPtr(fmt.Sprintf("np-native-%d", index)),
			InstanceState: common.StringPtr("running"),
			LanIP:         common.StringPtr(lanIp),
			NodePoolId:    common.StringPtr(currentNodePoolId),
		})
	}
	return &tke2022.DescribeClusterInstancesResponse{
		Response: &tke2022.DescribeClusterInstancesResponseParams{
			InstanceSet: instances,
			TotalCount:  common.Uint64Ptr(uint64(len(instances))),
			RequestId:   common.StringPtr("req-describe-tke-instances"),
		},
	}, nil
}

func (api *fakeNativeTkeAPI) DescribeClusterMachines(request *tke2022.DescribeClusterMachinesRequest) (*tke2022.DescribeClusterMachinesResponse, error) {
	api.calls = append(api.calls, "DescribeClusterMachines")
	api.describeMachinesRequest = append(api.describeMachinesRequest, request)
	if api.rejectMachinePoolFilter && clusterMachineNodePoolIdFilterValue(request) != "" {
		return nil, errors.New("[TencentCloudSDKError] Code=InvalidParameter, Message=invalid filter name NodePoolsId")
	}
	machines := []*tke2022.Machine{}
	for index := int64(1); index <= api.replicas; index++ {
		machines = append(machines, &tke2022.Machine{
			MachineName:  common.StringPtr(fmt.Sprintf("node-basic-%d", index)),
			MachineState: common.StringPtr("Running"),
			LanIP:        common.StringPtr(fmt.Sprintf("10.0.0.%d", index+10)),
			InstanceType: common.StringPtr("SA5.LARGE4"),
		})
	}
	return &tke2022.DescribeClusterMachinesResponse{
		Response: &tke2022.DescribeClusterMachinesResponseParams{
			Machines:   machines,
			TotalCount: common.Int64Ptr(int64(len(machines))),
			RequestId:  common.StringPtr("req-describe-machines"),
		},
	}, nil
}

func nodePoolIdFilterValue(request *tke2022.DescribeNodePoolsRequest) string {
	for _, filter := range request.Filters {
		if filter.Name != nil && *filter.Name == "NodePoolsId" && len(filter.Values) > 0 && filter.Values[0] != nil {
			return *filter.Values[0]
		}
	}
	return ""
}

func clusterMachineNodePoolIdFilterValue(request *tke2022.DescribeClusterMachinesRequest) string {
	for _, filter := range request.Filters {
		if filter.Name != nil && *filter.Name == "NodePoolsId" && len(filter.Values) > 0 && filter.Values[0] != nil {
			return *filter.Values[0]
		}
	}
	return ""
}

func clusterInstanceFilterValue(request *tke2022.DescribeClusterInstancesRequest, name string) string {
	for _, filter := range request.Filters {
		if filter.Name != nil && *filter.Name == name && len(filter.Values) > 0 && filter.Values[0] != nil {
			return *filter.Values[0]
		}
	}
	return ""
}

func cvmPrivateIpFilterValue(request *cvm2017.DescribeInstancesRequest) string {
	for _, filter := range request.Filters {
		if filter.Name != nil && *filter.Name == "private-ip-address" && len(filter.Values) > 0 && filter.Values[0] != nil {
			return *filter.Values[0]
		}
	}
	return ""
}

func (api *fakeNativeTkeAPI) ScaleNodePool(request *tke2022.ScaleNodePoolRequest) (*tke2022.ScaleNodePoolResponse, error) {
	api.calls = append(api.calls, "ScaleNodePool")
	api.scaleNodePoolRequest = request
	if request.Replicas != nil {
		api.replicas = *request.Replicas
	}
	return &tke2022.ScaleNodePoolResponse{
		Response: &tke2022.ScaleNodePoolResponseParams{
			RequestId: common.StringPtr("req-scale-pool"),
		},
	}, nil
}

func (api *fakeNativeTkeAPI) ModifyNodePool(request *tke2022.ModifyNodePoolRequest) (*tke2022.ModifyNodePoolResponse, error) {
	api.calls = append(api.calls, "ModifyNodePool")
	api.modifyNodePoolRequest = request
	if request.Native != nil && request.Native.EnableAutoscaling != nil {
		api.enableAutoscaling = *request.Native.EnableAutoscaling
	}
	return &tke2022.ModifyNodePoolResponse{
		Response: &tke2022.ModifyNodePoolResponseParams{
			RequestId: common.StringPtr("req-modify-pool"),
		},
	}, nil
}

func (api *fakeNativeTkeAPI) DeleteClusterMachines(request *tke2022.DeleteClusterMachinesRequest) (*tke2022.DeleteClusterMachinesResponse, error) {
	api.calls = append(api.calls, "DeleteClusterMachines")
	api.deleteMachinesRequest = request
	return &tke2022.DeleteClusterMachinesResponse{
		Response: &tke2022.DeleteClusterMachinesResponseParams{
			RequestId: common.StringPtr("req-delete-machine"),
		},
	}, nil
}

func TestTencentSDKClientCreateAllocationDisablesNodePoolAutoscalingBeforeExplicitScale(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1, enableAutoscaling: true}
	client := newFakeTencentSDKClient(tkeAPI)

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
	}, map[string]string{})

	if !response.Ok {
		t.Fatalf("expected ok response: %#v", response)
	}
	if tkeAPI.modifyNodePoolRequest == nil {
		t.Fatalf("expected ModifyNodePool before explicit ScaleNodePool")
	}
	if tkeAPI.modifyNodePoolRequest.Native == nil ||
		tkeAPI.modifyNodePoolRequest.Native.EnableAutoscaling == nil ||
		*tkeAPI.modifyNodePoolRequest.Native.EnableAutoscaling {
		t.Fatalf("explicit Fabric allocation must disable TKE node pool autoscaling: %#v", tkeAPI.modifyNodePoolRequest)
	}
	expectedCalls := []string{"DescribeNodePools", "ModifyNodePool", "DescribeClusterMachines", "ScaleNodePool", "DescribeClusterMachines"}
	if len(tkeAPI.calls) != len(expectedCalls) {
		t.Fatalf("unexpected call order: %#v", tkeAPI.calls)
	}
	for index, expected := range expectedCalls {
		if tkeAPI.calls[index] != expected {
			t.Fatalf("unexpected call order: %#v", tkeAPI.calls)
		}
	}
}

func TestTencentSDKClientCreateAllocationScalesExistingPackageNodePool(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1}
	client := newFakeTencentSDKClient(tkeAPI)

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
	}, map[string]string{})

	if !response.Ok {
		t.Fatalf("expected ok response: %#v", response)
	}
	if response.NodePoolId != "np-basic" {
		t.Fatalf("unexpected node pool id: %#v", response)
	}
	if response.InstanceId != "ins-basic-2" {
		t.Fatalf("native scale allocation must return the dedicated CVM instance id: %#v", response)
	}
	if response.NodeName != "10.0.0.12" {
		t.Fatalf("native scale allocation must return the Kubernetes node hostname from LanIP: %#v", response)
	}
	if response.ProviderData["machineName"] != "node-basic-2" {
		t.Fatalf("native scale allocation must preserve Tencent machine name as provider evidence: %#v", response.ProviderData)
	}
	if response.ProviderData["instanceId"] != "ins-basic-2" {
		t.Fatalf("native scale allocation must preserve Tencent instance id as provider evidence: %#v", response.ProviderData)
	}
	cvmAPI := client.nativeCvmClient.(*fakeNativeCvmAPI)
	if cvmPrivateIpFilterValue(cvmAPI.describeInstancesRequest[0]) != "10.0.0.12" {
		t.Fatalf("native scale allocation must resolve CVM identity by node private IP: %#v", cvmAPI.describeInstancesRequest[0])
	}
	if response.Status != "running" {
		t.Fatalf("allocation must complete only after node is running: %#v", response)
	}
	if tkeAPI.scaleNodePoolRequest == nil || tkeAPI.scaleNodePoolRequest.Replicas == nil || *tkeAPI.scaleNodePoolRequest.Replicas != 2 {
		t.Fatalf("expected scale to 2 replicas: %#v", tkeAPI.scaleNodePoolRequest)
	}
	if response.ProviderData["replicasBefore"] != "1" || response.ProviderData["replicasAfter"] != "2" {
		t.Fatalf("expected replica evidence: %#v", response.ProviderData)
	}
}

func TestTencentSDKClientCreateAllocationUsesTkeInstanceWhenCvmPrivateIpLookupIsEmpty(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1}
	client := newFakeTencentSDKClient(tkeAPI)
	client.nativeCvmClient = &fakeNativeCvmAPI{empty: true}

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
	}, map[string]string{})

	if !response.Ok {
		t.Fatalf("native TKE allocation should not fail only because CVM private IP lookup is empty: %#v", response)
	}
	if response.InstanceId != "" {
		t.Fatalf("TKE cluster instance id must not be reported as a CVM instance id: %#v", response)
	}
	if response.NodeName != "10.0.0.12" {
		t.Fatalf("expected Kubernetes node hostname from LanIP: %#v", response)
	}
	if response.ProviderData["machineName"] != "node-basic-2" {
		t.Fatalf("expected machineName deletion handle: %#v", response.ProviderData)
	}
	if response.ProviderData["instanceIdentitySource"] != "tke_cluster_instance" {
		t.Fatalf("expected identity source evidence: %#v", response.ProviderData)
	}
	if response.ProviderData["tkeClusterInstanceId"] != "np-native-2" {
		t.Fatalf("expected TKE cluster instance evidence: %#v", response.ProviderData)
	}
}

func TestTencentSDKClientCreateAllocationFallsBackWhenClusterMachinesRejectsNodePoolFilter(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1, rejectMachinePoolFilter: true}
	client := newFakeTencentSDKClient(tkeAPI)

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
	}, map[string]string{})

	if !response.Ok {
		t.Fatalf("expected fallback without machine filter to still provision node identity: %#v", response)
	}
	if response.NodeName == "" || response.PrivateIp == "" {
		t.Fatalf("expected node identity after fallback: %#v", response)
	}
	if len(tkeAPI.describeMachinesRequest) < 3 {
		t.Fatalf("expected filtered attempt and unfiltered fallback calls: %#v", tkeAPI.describeMachinesRequest)
	}
	if clusterMachineNodePoolIdFilterValue(tkeAPI.describeMachinesRequest[0]) != "np-basic" {
		t.Fatalf("first machine describe should try node pool filter: %#v", tkeAPI.describeMachinesRequest[0])
	}
	if clusterMachineNodePoolIdFilterValue(tkeAPI.describeMachinesRequest[1]) != "" {
		t.Fatalf("second machine describe should fallback without filter: %#v", tkeAPI.describeMachinesRequest[1])
	}
}

func TestTencentSDKClientCreateAllocationDiscoversExistingPackageNodePool(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{discoverNodePoolId: "np-discovered", replicas: 2}
	client := newFakeTencentSDKClient(tkeAPI)

	response := client.CreateComputeAllocation(Request{
		AccountId: "pi-alpha",
		UserId:    "usr-alpha",
		PackageId: "basic",
		Pool: ComputePoolInput{
			Id:           "pool-basic-2c4g",
			InstanceType: "SA5.LARGE4",
		},
		Allocation: ComputeAllocationInput{Id: "compute-alpha"},
	}, map[string]string{})

	if !response.Ok {
		t.Fatalf("expected ok response: %#v", response)
	}
	if response.NodePoolId != "np-discovered" {
		t.Fatalf("expected discovered node pool id: %#v", response)
	}
	if response.NodeName == "" {
		t.Fatalf("expected discovered pool allocation to return a node identity: %#v", response)
	}
	if tkeAPI.createNodePoolRequest != nil {
		t.Fatalf("must reuse discovered package pool before creating: %#v", tkeAPI.createNodePoolRequest)
	}
	if tkeAPI.scaleNodePoolRequest == nil || tkeAPI.scaleNodePoolRequest.Replicas == nil || *tkeAPI.scaleNodePoolRequest.Replicas != 3 {
		t.Fatalf("expected scale to 3 replicas: %#v", tkeAPI.scaleNodePoolRequest)
	}
	expectedCalls := []string{"DescribeNodePools", "DescribeClusterMachines", "ScaleNodePool", "DescribeClusterMachines"}
	if len(tkeAPI.calls) != len(expectedCalls) {
		t.Fatalf("unexpected call order: %#v", tkeAPI.calls)
	}
	for index, expected := range expectedCalls {
		if tkeAPI.calls[index] != expected {
			t.Fatalf("unexpected call order: %#v", tkeAPI.calls)
		}
	}
}

func TestTencentSDKClientCreateAllocationFallsBackFromStaleConfiguredNodePool(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-live", discoverNodePoolId: "np-live", replicas: 4}
	client := newFakeTencentSDKClient(tkeAPI)

	response := client.CreateComputeAllocation(Request{
		AccountId: "pi-alpha",
		UserId:    "usr-alpha",
		PackageId: "basic",
		Pool: ComputePoolInput{
			Id:           "pool-basic-2c4g",
			InstanceType: "SA5.LARGE4",
			NodePoolId:   "np-stale",
		},
		Allocation: ComputeAllocationInput{Id: "compute-alpha"},
	}, map[string]string{})

	if !response.Ok {
		t.Fatalf("expected ok response: %#v", response)
	}
	if response.NodePoolId != "np-live" {
		t.Fatalf("expected discovered live node pool id: %#v", response)
	}
	if response.NodeName == "" {
		t.Fatalf("expected fallback allocation to return a node identity: %#v", response)
	}
	if tkeAPI.createNodePoolRequest != nil {
		t.Fatalf("must not create when matching live package pool exists: %#v", tkeAPI.createNodePoolRequest)
	}
	if tkeAPI.scaleNodePoolRequest == nil || tkeAPI.scaleNodePoolRequest.Replicas == nil || *tkeAPI.scaleNodePoolRequest.Replicas != 5 {
		t.Fatalf("expected scale to 5 replicas: %#v", tkeAPI.scaleNodePoolRequest)
	}
	expectedCalls := []string{"DescribeNodePools", "DescribeNodePools", "DescribeClusterMachines", "ScaleNodePool", "DescribeClusterMachines"}
	if len(tkeAPI.calls) != len(expectedCalls) {
		t.Fatalf("unexpected call order: %#v", tkeAPI.calls)
	}
	for index, expected := range expectedCalls {
		if tkeAPI.calls[index] != expected {
			t.Fatalf("unexpected call order: %#v", tkeAPI.calls)
		}
	}
}

func TestTencentSDKClientCreateAllocationCreatesMissingPackageNodePoolThenScales(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{}
	client := newFakeTencentSDKClient(tkeAPI)
	env := map[string]string{
		"TENCENT_DEPLOY_CLUSTER_ID":       "cls-123",
		"TENCENT_CVM_SUBNET_ID":           "subnet-123",
		"TENCENT_CVM_SECURITY_GROUP_IDS":  "sg-123",
		"TENCENT_CVM_SYSTEM_DISK_TYPE":    "CLOUD_BSSD",
		"TENCENT_CVM_SYSTEM_DISK_SIZE_GB": "50",
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
		t.Fatalf("expected CreateNodePool call")
	}
	if response.NodePoolId != "np-created" {
		t.Fatalf("expected created node pool id: %#v", response)
	}
	if response.NodeName == "" {
		t.Fatalf("expected created pool allocation to return a node identity: %#v", response)
	}
	if tkeAPI.scaleNodePoolRequest == nil || tkeAPI.scaleNodePoolRequest.NodePoolId == nil || *tkeAPI.scaleNodePoolRequest.NodePoolId != "np-created" {
		t.Fatalf("expected scale of created node pool: %#v", tkeAPI.scaleNodePoolRequest)
	}
	if tkeAPI.scaleNodePoolRequest.Replicas == nil || *tkeAPI.scaleNodePoolRequest.Replicas != 1 {
		t.Fatalf("expected scale to one replica: %#v", tkeAPI.scaleNodePoolRequest)
	}
	expectedCalls := []string{"DescribeNodePools", "CreateNodePool", "DescribeNodePools", "DescribeClusterMachines", "ScaleNodePool", "DescribeClusterMachines"}
	if len(tkeAPI.calls) != len(expectedCalls) {
		t.Fatalf("unexpected call order: %#v", tkeAPI.calls)
	}
	for index, expected := range expectedCalls {
		if tkeAPI.calls[index] != expected {
			t.Fatalf("unexpected call order: %#v", tkeAPI.calls)
		}
	}
}

func TestTencentSDKClientDestroyAllocationDeletesNamedMachine(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1}
	client := newFakeTencentSDKClient(tkeAPI)

	response := client.DestroyComputeAllocation(Request{
		AccountId: "pi-alpha",
		Pool:      ComputePoolInput{Id: "pool-basic-2c4g", NodePoolId: "np-basic"},
		Allocation: ComputeAllocationInput{
			Id:          "compute-alpha",
			InstanceId:  "ins-created",
			NodeName:    "10.0.0.12",
			MachineName: "node-basic-2",
		},
	}, map[string]string{})

	if !response.Ok {
		t.Fatalf("expected ok response: %#v", response)
	}
	if response.Status != "destroyed" {
		t.Fatalf("unexpected status: %#v", response)
	}
	if tkeAPI.deleteMachinesRequest == nil || len(tkeAPI.deleteMachinesRequest.MachineNames) != 1 || *tkeAPI.deleteMachinesRequest.MachineNames[0] != "node-basic-2" {
		t.Fatalf("expected DeleteClusterMachines call: %#v", tkeAPI.deleteMachinesRequest)
	}
	if tkeAPI.deleteMachinesRequest.EnableScaleDown == nil || !*tkeAPI.deleteMachinesRequest.EnableScaleDown {
		t.Fatalf("delete must scale down the node pool")
	}
	if tkeAPI.deleteMachinesRequest.InstanceDeleteMode == nil || *tkeAPI.deleteMachinesRequest.InstanceDeleteMode != "terminate" {
		t.Fatalf("compute destroy must terminate the cloud machine")
	}
}

func TestTencentSDKClientDestroyMachineAllocationAlwaysScalesDownAndTerminates(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1}
	client := newFakeTencentSDKClient(tkeAPI)

	response := client.DestroyComputeAllocation(Request{
		AccountId: "pi-alpha",
		Pool:      ComputePoolInput{Id: "pool-basic-2c4g", NodePoolId: "np-basic"},
		Allocation: ComputeAllocationInput{
			Id:          "compute-alpha",
			NodeName:    "10.0.0.12",
			MachineName: "np-basic-native",
		},
	}, map[string]string{})

	if !response.Ok {
		t.Fatalf("expected ok response: %#v", response)
	}
	if tkeAPI.deleteMachinesRequest == nil {
		t.Fatalf("expected DeleteClusterMachines call")
	}
	if tkeAPI.deleteMachinesRequest.EnableScaleDown == nil || !*tkeAPI.deleteMachinesRequest.EnableScaleDown {
		t.Fatalf("compute destroy must scale down the node pool")
	}
	if tkeAPI.deleteMachinesRequest.InstanceDeleteMode == nil || *tkeAPI.deleteMachinesRequest.InstanceDeleteMode != "terminate" {
		t.Fatalf("compute destroy must terminate the cloud machine")
	}
}

func TestTencentSDKClientDestroyMachineNameOnlyAllocationScalesDownAndTerminates(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1}
	client := newFakeTencentSDKClient(tkeAPI)

	response := client.DestroyComputeAllocation(Request{
		AccountId: "pi-alpha",
		Pool:      ComputePoolInput{Id: "pool-basic-2c4g", NodePoolId: "np-basic"},
		Allocation: ComputeAllocationInput{
			Id:          "compute-alpha",
			NodeName:    "10.0.0.12",
			MachineName: "np-basic-native",
		},
	}, map[string]string{})

	if !response.Ok {
		t.Fatalf("expected ok response: %#v", response)
	}
	if tkeAPI.deleteMachinesRequest == nil {
		t.Fatalf("expected DeleteClusterMachines call")
	}
	if tkeAPI.deleteMachinesRequest.EnableScaleDown == nil || !*tkeAPI.deleteMachinesRequest.EnableScaleDown {
		t.Fatalf("machineName-only compute destroy must scale down the node pool")
	}
	if tkeAPI.deleteMachinesRequest.InstanceDeleteMode == nil || *tkeAPI.deleteMachinesRequest.InstanceDeleteMode != "terminate" {
		t.Fatalf("machineName-only compute destroy must terminate the cloud machine")
	}
}

func TestTencentSDKClientDestroyAllocationWithoutMachineNameFailsClosed(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 2}
	client := newFakeTencentSDKClient(tkeAPI)

	response := client.DestroyComputeAllocation(Request{
		AccountId:  "pi-alpha",
		Pool:       ComputePoolInput{Id: "pool-basic-2c4g", NodePoolId: "np-basic"},
		Allocation: ComputeAllocationInput{Id: "compute-alpha"},
	}, map[string]string{})

	if response.Ok {
		t.Fatalf("expected destroy without node identity to fail closed: %#v", response)
	}
	if response.ErrorCode != "compute_allocation_machine_identity_required" {
		t.Fatalf("unexpected error: %#v", response)
	}
	if tkeAPI.scaleNodePoolRequest != nil {
		t.Fatalf("destroy must not scale down a pool without a node identity: %#v", tkeAPI.scaleNodePoolRequest)
	}
}
