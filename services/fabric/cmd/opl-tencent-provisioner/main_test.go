package main

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	cbs2017 "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cbs/v20170312"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	tcerrors "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/errors"
	cvm2017 "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cvm/v20170312"
	tke2022 "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/tke/v20220501"
	vpc2017 "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/vpc/v20170312"
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
	createdRequest        Request
	storageRequest        Request
	renewedComputeRequest Request
	syncedRequest         Request
	destroyedRequest      Request
	taggedRequest         Request
	truthRequest          Request
}

func (client *fakeTencentClient) Capacity(request Request, _ map[string]string) Response {
	return Response{Ok: true, Status: "ready", InstanceType: request.Pool.InstanceType, RequiredCapacity: request.Pool.DesiredReplicas}
}

func (client *fakeTencentClient) StoragePreflight(request Request, _ map[string]string) Response {
	return Response{Ok: true, Status: "ready", ProviderPriceCNY: 1, ProviderRequestIDs: map[string]string{"quota": "req-quota", "price": "req-price"}}
}

func (client *fakeTencentClient) ProviderTruth(request Request, _ map[string]string) Response {
	client.truthRequest = request
	return Response{Ok: true, Status: "present", InstanceId: request.Allocation.InstanceId}
}

func (client *fakeTencentClient) CreateStorageVolume(request Request, _ map[string]string) Response {
	client.storageRequest = request
	return Response{Ok: true, StorageVolumeId: "disk-test", Status: "provider_ready"}
}

func (client *fakeTencentClient) SyncStorageVolume(request Request, _ map[string]string) Response {
	client.storageRequest = request
	return Response{Ok: true, StorageVolumeId: request.Storage.Id, Status: "provider_ready"}
}

func (client *fakeTencentClient) RenewStorageVolume(request Request, _ map[string]string) Response {
	client.storageRequest = request
	return Response{Ok: true, StorageVolumeId: request.Storage.Id, Status: "provider_ready"}
}

func (client *fakeTencentClient) RenewComputeAllocation(request Request, _ map[string]string) Response {
	client.renewedComputeRequest = request
	return Response{Ok: true, InstanceId: request.Allocation.InstanceId, Status: "provider_ready"}
}

func TestProviderTruthUsesTencentClientBoundaryWithoutMutationFlag(t *testing.T) {
	client := &fakeTencentClient{}
	request := providerTruthRequest()
	response := handleWithClient(request, map[string]string{
		"TENCENTCLOUD_SECRET_ID": "sid", "TENCENTCLOUD_SECRET_KEY": "skey", "TENCENTCLOUD_REGION": "ap-guangzhou", "TENCENT_DEPLOY_CLUSTER_ID": "cls-123",
	}, client)
	if !response.Ok || response.Status != "present" || client.truthRequest.Allocation.Id != "compute-alpha" {
		t.Fatalf("provider truth response=%#v request=%#v", response, client.truthRequest)
	}
}

func (client *fakeTencentClient) ReconcileComputePool(request Request, _ map[string]string) Response {
	return Response{Ok: true, PoolId: request.Pool.Id, NodePoolId: request.Pool.NodePoolId, Status: "ready"}
}

func (client *fakeTencentClient) TagComputeMachine(request Request, _ map[string]string) Response {
	client.taggedRequest = request
	return Response{Ok: true, InstanceId: request.Allocation.InstanceId, Status: "tagged", ProviderRequestId: "req-tag-machine"}
}

func TestTagComputeMachineLiveUsesTencentClientBoundary(t *testing.T) {
	client := &fakeTencentClient{}
	request := Request{Action: "tag_compute_machine", Tags: map[string]string{"opl_account_id": "acct-alpha", "opl_workspace_id": "ws-alpha", "opl_resource_id": "compute-alpha", "opl_operation_id": "owner-alpha"}, Allocation: ComputeAllocationInput{InstanceId: "ins-alpha"}}
	response := handleWithClient(request, map[string]string{
		"TENCENTCLOUD_SECRET_ID": "sid", "TENCENTCLOUD_SECRET_KEY": "skey", "TENCENTCLOUD_REGION": "ap-guangzhou",
		"TENCENT_DEPLOY_CLUSTER_ID": "cls-123", "RUN_TENCENT_CREATE_RELEASE_EXECUTION": "1",
	}, client)
	if !response.Ok || response.Status != "tagged" || client.taggedRequest.Allocation.InstanceId != "ins-alpha" {
		t.Fatalf("tag response=%#v request=%#v", response, client.taggedRequest)
	}
}

func TestTencentSDKTagComputeMachineWritesAndReadsBackCVMOwnership(t *testing.T) {
	wantTags := map[string]string{"opl_account_id": "acct-alpha", "opl_workspace_id": "ws-alpha", "opl_resource_id": "compute-alpha", "opl_operation_id": "owner-alpha"}
	cvmAPI := &fakeNativeCvmAPI{}
	client := &tencentSDKClient{nativeCvmClient: cvmAPI, nativeTagClient: &fakeNativeTagAPI{cvm: cvmAPI}}
	response := client.TagComputeMachine(Request{Tags: wantTags, Allocation: ComputeAllocationInput{InstanceId: "ins-alpha"}}, nil)
	if !response.Ok || response.ProviderRequestId != "req-verify-cvm" || len(cvmAPI.modifyInstancesRequest) != 1 || stringValue(cvmAPI.modifyInstancesRequest[0].InstanceName) != "compute-alpha" || !reflect.DeepEqual(cvmAPI.tags, wantTags) {
		t.Fatalf("tag response=%#v modify requests=%#v tags=%#v", response, cvmAPI.modifyInstancesRequest, cvmAPI.tags)
	}
}

func TestTencentSDKTagComputeMachineRequiresEveryOwnershipTag(t *testing.T) {
	for _, missing := range cbsOwnershipTagKeys {
		t.Run(missing, func(t *testing.T) {
			tags := map[string]string{"opl_account_id": "acct-alpha", "opl_workspace_id": "ws-alpha", "opl_resource_id": "compute-alpha", "opl_operation_id": "owner-alpha"}
			delete(tags, missing)
			cvmAPI := &fakeNativeCvmAPI{}
			response := (&tencentSDKClient{nativeCvmClient: cvmAPI, nativeTagClient: &fakeNativeTagAPI{cvm: cvmAPI}}).TagComputeMachine(Request{Tags: tags, Allocation: ComputeAllocationInput{InstanceId: "ins-alpha"}}, nil)
			if response.Ok || len(cvmAPI.modifyInstancesRequest) != 0 {
				t.Fatalf("missing %s must fail before CVM mutation: response=%#v", missing, response)
			}
		})
	}
}

func computeOwnershipTags() map[string]string {
	return map[string]string{"opl_account_id": "acct-alpha", "opl_workspace_id": "ws-alpha", "opl_resource_id": "compute-alpha", "opl_operation_id": "owner-alpha"}
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

func (client *fakeTencentClient) SyncComputeAllocation(request Request, env map[string]string) Response {
	client.syncedRequest = request
	return Response{
		Ok:          true,
		OperationId: "op-live-sync",
		NodePoolId:  request.Pool.NodePoolId,
		NodeName:    request.Allocation.NodeName,
		Status:      "external_deleted",
		ProviderData: map[string]string{
			"client": "fake",
			"region": env["TENCENTCLOUD_REGION"],
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

func TestSyncComputeAllocationLiveUsesTencentClientBoundaryWithoutMutationFlag(t *testing.T) {
	env := map[string]string{
		"TENCENTCLOUD_SECRET_ID":    "sid",
		"TENCENTCLOUD_SECRET_KEY":   "skey",
		"TENCENTCLOUD_REGION":       "ap-guangzhou",
		"TENCENT_DEPLOY_CLUSTER_ID": "cls-123",
	}
	client := &fakeTencentClient{}

	response := handleWithClient(Request{
		Action:    "sync_compute_allocation",
		AccountId: "pi-alpha",
		Pool:      ComputePoolInput{Id: "pool-basic-2c4g", NodePoolId: "np-basic"},
		Allocation: ComputeAllocationInput{
			Id:          "compute-alpha",
			MachineName: "machine-alpha",
			NodeName:    "node-alpha",
			PrivateIp:   "10.0.0.8",
		},
	}, env, client)

	if !response.Ok {
		t.Fatalf("expected ok response: %#v", response)
	}
	if response.Status != "external_deleted" {
		t.Fatalf("expected sync result from client: %#v", response)
	}
	if client.syncedRequest.Allocation.MachineName != "machine-alpha" {
		t.Fatalf("expected request to reach client: %#v", client.syncedRequest)
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
	if client.nativeCbsClient == nil {
		t.Fatalf("expected native CBS SDK client")
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
		Tags: map[string]string{
			"opl_account_id":   "pi-alpha",
			"opl_workspace_id": "ws-alpha",
			"opl_resource_id":  "compute-alpha",
			"opl_operation_id": "op-alpha",
		},
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
	if stringValue(createRequest.Native.InstanceChargeType) != "PREPAID" {
		t.Fatalf("Fabric package pools must use prepaid CVMs: %#v", createRequest.Native.InstanceChargeType)
	}
	prepaid := createRequest.Native.InstanceChargePrepaid
	if prepaid == nil || prepaid.Period == nil || *prepaid.Period != 1 || stringValue(prepaid.RenewFlag) != "NOTIFY_AND_MANUAL_RENEW" {
		t.Fatalf("Fabric package pools must use one-month prepaid CVMs with manual renewal: %#v", prepaid)
	}
	if stringValue(createRequest.Native.MachineType) != "NativeCVM" {
		t.Fatalf("Fabric package pools must provision CVMs, not CXM native machines: %#v", createRequest.Native.MachineType)
	}
	if createRequest.Native.Replicas == nil || *createRequest.Native.Replicas != 0 {
		t.Fatalf("node pool creation must not allocate a CVM immediately: %#v", createRequest.Native.Replicas)
	}
	if createRequest.Native.EnableAutoscaling == nil || *createRequest.Native.EnableAutoscaling {
		t.Fatalf("Fabric-managed package node pools must disable TKE autoscaling: %#v", createRequest.Native.EnableAutoscaling)
	}
	if createRequest.Native.AutoRepair == nil || *createRequest.Native.AutoRepair {
		t.Fatalf("Fabric-managed package node pools must disable TKE autorepair so Console owns every replacement CVM: %#v", createRequest.Native.AutoRepair)
	}
	if createRequest.Native.InternetAccessible != nil {
		t.Fatalf("zero-bandwidth package nodes must omit legacy public network settings: %#v", createRequest.Native.InternetAccessible)
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
	if labels["oplcloud.cn/account-id"] != "pi-alpha" || labels["oplcloud.cn/resource-id"] != "compute-alpha" || labels["oplcloud.cn/operation-id"] != "op-alpha" {
		t.Fatalf("node pool request must carry OPL cost labels: %#v", labels)
	}
	if len(createRequest.Tags) != 1 || len(createRequest.Tags[0].Tags) != 4 {
		t.Fatalf("node pool request must carry Tencent cost tags: %#v", createRequest.Tags)
	}
}

type fakeNativeTkeAPI struct {
	createNodePoolRequest       *tke2022.CreateNodePoolRequest
	describeInstancesRequest    []*tke2022.DescribeClusterInstancesRequest
	describeMachinesRequest     []*tke2022.DescribeClusterMachinesRequest
	describeNodePoolsRequest    []*tke2022.DescribeNodePoolsRequest
	modifyNodePoolRequest       *tke2022.ModifyNodePoolRequest
	scaleNodePoolRequest        *tke2022.ScaleNodePoolRequest
	deleteMachinesRequest       *tke2022.DeleteClusterMachinesRequest
	nodePoolId                  string
	discoverNodePoolId          string
	ambiguousDiscovery          bool
	truncatedDiscovery          bool
	replicas                    int64
	maxReplicas                 int64
	readyReplicas               *int64
	omitNative                  bool
	omitScaling                 bool
	omitReplicas                bool
	omitReadyReplicas           bool
	lifeState                   string
	poolType                    string
	machineType                 string
	instanceChargeType          string
	omitInstanceChargePrepaid   bool
	omitPrepaidPeriod           bool
	prepaidRenewFlag            string
	labelPoolId                 string
	labelPackageId              string
	labelInstanceType           string
	instanceTypes               []string
	subnetIds                   []string
	enableAutoscaling           bool
	autoRepair                  bool
	rejectMachinePoolFilter     bool
	machinePoolIds              []string
	nodeType                    string
	omitInstanceNodePool        bool
	omitMachineLanIP            bool
	machineInstanceIDsMatch     bool
	machineInstanceType         string
	duplicateMachineName        bool
	deletedMachineNames         map[string]bool
	retainDeletedMachines       bool
	callLog                     *[]string
	calls                       []string
	describeNodePoolErr         error
	describeMachineErr          error
	describeClusterInstancesErr error
	omitClusterInstances        bool
	clusterInstanceID           string
}

type fakeNativeCvmAPI struct {
	describeAccountQuotaRequests []*cvm2017.DescribeAccountQuotaRequest
	describeZoneConfigRequests   []*cvm2017.DescribeZoneInstanceConfigInfosRequest
	quotaRemaining               uint64
	zeroQuota                    bool
	omitQuotaRemaining           bool
	ambiguousPrepaidQuota        bool
	omitZoneConfig               bool
	zoneConfigStatus             string
	zoneConfigChargeType         string
	zoneConfigDiscountPrice      float64
	omitZoneConfigPrice          bool
	describeInstancesRequest     []*cvm2017.DescribeInstancesRequest
	modifyInstancesRequest       []*cvm2017.ModifyInstancesAttributeRequest
	renewInstancesRequests       []*cvm2017.RenewInstancesRequest
	instanceName                 string
	instanceType                 string
	returnedInstanceID           string
	instanceChargeType           string
	renewFlag                    string
	expiredTime                  string
	renewedExpiredTime           string
	omitExpiredTime              bool
	empty                        bool
	err                          error
	nilResponse                  bool
	nilEnvelope                  bool
	nilResponseCall              int
	nilEnvelopeCall              int
	nilInstanceCall              int
	callLog                      *[]string
	zone                         string
	tags                         map[string]string
}

type fakeNativeTagAPI struct {
	cvm      *fakeNativeCvmAPI
	calls    []string
	attached map[string]bool
}

func (api *fakeNativeTagAPI) SetCVMTag(_ string, key, value string, attached bool) (string, error) {
	api.calls = append(api.calls, key+"="+value)
	if api.attached == nil {
		api.attached = map[string]bool{}
	}
	api.attached[key] = attached
	if api.cvm.tags == nil {
		api.cvm.tags = map[string]string{}
	}
	api.cvm.tags[key] = value
	return "req-tag-" + key, nil
}

func TestTencentSDKTagComputeMachineModifiesAttachedEmptyOwnershipTag(t *testing.T) {
	wantTags := computeOwnershipTags()
	cvmAPI := &fakeNativeCvmAPI{tags: map[string]string{"opl_account_id": ""}}
	tagAPI := &fakeNativeTagAPI{cvm: cvmAPI}
	response := (&tencentSDKClient{nativeCvmClient: cvmAPI, nativeTagClient: tagAPI}).TagComputeMachine(Request{Tags: wantTags, Allocation: ComputeAllocationInput{InstanceId: "ins-alpha"}}, nil)
	if !response.Ok || !tagAPI.attached["opl_account_id"] {
		t.Fatalf("attached empty tag must be modified: response=%#v attached=%#v", response, tagAPI.attached)
	}
}

func (api *fakeNativeCvmAPI) RenewInstances(request *cvm2017.RenewInstancesRequest) (*cvm2017.RenewInstancesResponse, error) {
	api.renewInstancesRequests = append(api.renewInstancesRequests, request)
	api.expiredTime = firstNonEmpty(api.renewedExpiredTime, "2026-09-16T00:00:00Z")
	return &cvm2017.RenewInstancesResponse{Response: &cvm2017.RenewInstancesResponseParams{RequestId: common.StringPtr("req-renew-cvm")}}, nil
}

type fakeNativeCbsAPI struct {
	createDisksRequests             []*cbs2017.CreateDisksRequest
	describeDisksRequests           []*cbs2017.DescribeDisksRequest
	describeDiskConfigQuotaRequests []*cbs2017.DescribeDiskConfigQuotaRequest
	inquiryPriceCreateDisksRequests []*cbs2017.InquiryPriceCreateDisksRequest
	renewDiskRequests               []*cbs2017.RenewDiskRequest
	quotaUnavailable                bool
	quotaDiskChargeType             string
	quotaDiskType                   string
	quotaZone                       string
	priceDiscount                   float64
	omitPrice                       bool
	diskID                          string
	diskName                        string
	diskUsage                       string
	diskState                       string
	diskType                        string
	diskChargeType                  string
	renewFlag                       string
	diskSize                        uint64
	zone                            string
	deadline                        string
	tags                            map[string]string
	renewedDeadline                 string
	omitDeadline                    bool
	empty                           bool
	duplicate                       bool
	err                             error
}

func (api *fakeNativeCbsAPI) CreateDisks(request *cbs2017.CreateDisksRequest) (*cbs2017.CreateDisksResponse, error) {
	api.createDisksRequests = append(api.createDisksRequests, request)
	return &cbs2017.CreateDisksResponse{Response: &cbs2017.CreateDisksResponseParams{
		DiskIdSet: []*string{common.StringPtr(firstNonEmpty(api.diskID, "disk-storage-alpha"))}, RequestId: common.StringPtr("req-create-cbs"),
	}}, nil
}

func (api *fakeNativeCbsAPI) RenewDisk(request *cbs2017.RenewDiskRequest) (*cbs2017.RenewDiskResponse, error) {
	api.renewDiskRequests = append(api.renewDiskRequests, request)
	api.deadline = firstNonEmpty(api.renewedDeadline, "2026-09-16T00:00:00Z")
	return &cbs2017.RenewDiskResponse{Response: &cbs2017.RenewDiskResponseParams{RequestId: common.StringPtr("req-renew-cbs")}}, nil
}

func (api *fakeNativeCbsAPI) DescribeDisks(request *cbs2017.DescribeDisksRequest) (*cbs2017.DescribeDisksResponse, error) {
	api.describeDisksRequests = append(api.describeDisksRequests, request)
	if api.err != nil {
		return nil, api.err
	}
	diskID := firstNonEmpty(api.diskID, stringValue(request.DiskIds[0]))
	deadline := common.StringPtr(firstNonEmpty(api.deadline, "2026-08-16T00:00:00Z"))
	if api.omitDeadline {
		deadline = nil
	}
	tags := map[string]string{
		"opl_account_id": "acct-alpha", "opl_workspace_id": "ws-alpha", "opl_resource_id": "storage-alpha", "opl_operation_id": "op-storage-alpha",
	}
	for key, value := range api.tags {
		tags[key] = value
	}
	diskTags := make([]*cbs2017.Tag, 0, len(tags))
	for key, value := range tags {
		diskTags = append(diskTags, &cbs2017.Tag{Key: common.StringPtr(key), Value: common.StringPtr(value)})
	}
	disks := []*cbs2017.Disk{{
		DiskId: common.StringPtr(diskID), DiskState: common.StringPtr(firstNonEmpty(api.diskState, "ATTACHED")),
		DiskName: common.StringPtr(firstNonEmpty(api.diskName, "storage-alpha")), DiskUsage: common.StringPtr(firstNonEmpty(api.diskUsage, "DATA_DISK")), Tags: diskTags,
		DiskType: common.StringPtr(firstNonEmpty(api.diskType, "CLOUD_BSSD")), DiskChargeType: common.StringPtr(firstNonEmpty(api.diskChargeType, "PREPAID")),
		RenewFlag: common.StringPtr(firstNonEmpty(api.renewFlag, "NOTIFY_AND_MANUAL_RENEW")), DiskSize: common.Uint64Ptr(firstNonZeroUint(api.diskSize, 10)),
		Placement: &cbs2017.Placement{Zone: common.StringPtr(firstNonEmpty(api.zone, "ap-guangzhou-3"))}, DeadlineTime: deadline,
	}}
	if api.empty {
		disks = nil
	}
	if api.duplicate {
		disks = append(disks, disks[0])
	}
	return &cbs2017.DescribeDisksResponse{Response: &cbs2017.DescribeDisksResponseParams{
		DiskSet: disks, TotalCount: common.Uint64Ptr(uint64(len(disks))), RequestId: common.StringPtr("req-describe-cbs"),
	}}, nil
}

func (api *fakeNativeCbsAPI) DescribeDiskConfigQuota(request *cbs2017.DescribeDiskConfigQuotaRequest) (*cbs2017.DescribeDiskConfigQuotaResponse, error) {
	api.describeDiskConfigQuotaRequests = append(api.describeDiskConfigQuotaRequests, request)
	available := !api.quotaUnavailable
	config := &cbs2017.DiskConfig{
		Available: common.BoolPtr(available), DiskChargeType: common.StringPtr(firstNonEmpty(api.quotaDiskChargeType, "PREPAID")),
		Zone: common.StringPtr(firstNonEmpty(api.quotaZone, "ap-guangzhou-3")), DiskType: common.StringPtr(firstNonEmpty(api.quotaDiskType, "CLOUD_BSSD")),
		DiskUsage: common.StringPtr("DATA_DISK"), MinDiskSize: common.Uint64Ptr(10), MaxDiskSize: common.Uint64Ptr(32000), StepSize: common.Uint64Ptr(1),
	}
	return &cbs2017.DescribeDiskConfigQuotaResponse{Response: &cbs2017.DescribeDiskConfigQuotaResponseParams{DiskConfigSet: []*cbs2017.DiskConfig{config}, RequestId: common.StringPtr("req-cbs-quota")}}, nil
}

func (api *fakeNativeCbsAPI) InquiryPriceCreateDisks(request *cbs2017.InquiryPriceCreateDisksRequest) (*cbs2017.InquiryPriceCreateDisksResponse, error) {
	api.inquiryPriceCreateDisksRequests = append(api.inquiryPriceCreateDisksRequests, request)
	var price *cbs2017.Price
	if !api.omitPrice {
		discount := api.priceDiscount
		if discount == 0 {
			discount = 7.5
		}
		price = &cbs2017.Price{DiscountPrice: common.Float64Ptr(discount), OriginalPrice: common.Float64Ptr(10)}
	}
	return &cbs2017.InquiryPriceCreateDisksResponse{Response: &cbs2017.InquiryPriceCreateDisksResponseParams{DiskPrice: price, RequestId: common.StringPtr("req-cbs-price")}}, nil
}

func TestTencentSDKStoragePreflightUsesExactPrepaidQuotaAndPriceRequests(t *testing.T) {
	api := &fakeNativeCbsAPI{}
	client := &tencentSDKClient{nativeCbsClient: api}
	response := client.StoragePreflight(Request{Action: "storage_preflight", PackageId: "basic", Storage: StorageInput{SizeGB: 10, Zone: "ap-guangzhou-3", DiskType: "CLOUD_BSSD"}}, nil)

	if !response.Ok || response.Status != "ready" || response.ProviderPriceCNY != 7.5 || response.ProviderRequestIDs["quota"] != "req-cbs-quota" || response.ProviderRequestIDs["price"] != "req-cbs-price" {
		t.Fatalf("unexpected storage preflight response: %#v", response)
	}
	if len(api.describeDiskConfigQuotaRequests) != 1 || len(api.inquiryPriceCreateDisksRequests) != 1 || len(api.createDisksRequests) != 0 || len(api.renewDiskRequests) != 0 {
		t.Fatalf("storage preflight calls = %#v", api)
	}
	quota := api.describeDiskConfigQuotaRequests[0]
	if stringValue(quota.InquiryType) != "INQUIRY_CBS_CONFIG" || stringValue(quota.DiskChargeType) != "PREPAID" || stringValue(quota.DiskUsage) != "DATA_DISK" || len(quota.DiskTypes) != 1 || stringValue(quota.DiskTypes[0]) != "CLOUD_BSSD" || len(quota.Zones) != 1 || stringValue(quota.Zones[0]) != "ap-guangzhou-3" {
		t.Fatalf("unexpected CBS quota request: %#v", quota)
	}
	price := api.inquiryPriceCreateDisksRequests[0]
	if stringValue(price.DiskChargeType) != "PREPAID" || stringValue(price.DiskType) != "CLOUD_BSSD" || price.DiskSize == nil || *price.DiskSize != 10 || price.DiskCount == nil || *price.DiskCount != 1 || price.DiskChargePrepaid == nil || price.DiskChargePrepaid.Period == nil || *price.DiskChargePrepaid.Period != 1 || stringValue(price.DiskChargePrepaid.RenewFlag) != "NOTIFY_AND_MANUAL_RENEW" {
		t.Fatalf("unexpected CBS price request: %#v", price)
	}
}

func TestTencentSDKStoragePreflightRejectsUnavailableQuotaOrPriceWithoutMutation(t *testing.T) {
	for _, api := range []*fakeNativeCbsAPI{{quotaUnavailable: true}, {quotaDiskChargeType: "POSTPAID_BY_HOUR"}, {omitPrice: true}} {
		client := &tencentSDKClient{nativeCbsClient: api}
		response := client.StoragePreflight(Request{PackageId: "basic", Storage: StorageInput{SizeGB: 10, Zone: "ap-guangzhou-3", DiskType: "CLOUD_BSSD"}}, nil)
		if response.Ok || len(api.createDisksRequests) != 0 || len(api.renewDiskRequests) != 0 {
			t.Fatalf("failed preflight mutated or succeeded: response=%#v api=%#v", response, api)
		}
	}
}

func TestTencentSDKCreateStorageVolumeUsesOneMonthPrepaidCBSAndStableToken(t *testing.T) {
	api := &fakeNativeCbsAPI{}
	client := &tencentSDKClient{nativeCbsClient: api}
	request := Request{
		AccountId: "acct-alpha",
		Tags: map[string]string{
			"opl_account_id": "acct-alpha", "opl_workspace_id": "ws-alpha", "opl_resource_id": "storage-alpha", "opl_operation_id": "op-storage-alpha",
		},
		Storage: StorageInput{Id: "storage-alpha", SizeGB: 10, Zone: "ap-guangzhou-3"},
	}

	first := client.CreateStorageVolume(request, map[string]string{"TENCENT_CBS_DISK_TYPE": "CLOUD_BSSD"})
	second := client.CreateStorageVolume(request, map[string]string{"TENCENT_CBS_DISK_TYPE": "CLOUD_BSSD"})

	if !first.Ok || first.StorageVolumeId != "disk-storage-alpha" || first.CBSStatus != "ATTACHED" || first.Status != "provider_ready" || first.ProviderData["deadline"] != "2026-08-16T00:00:00Z" {
		t.Fatalf("unexpected create response: %#v", first)
	}
	if !second.Ok || len(api.createDisksRequests) != 2 {
		t.Fatalf("replayed create response=%#v requests=%#v", second, api.createDisksRequests)
	}
	created := api.createDisksRequests[0]
	if created.Placement == nil || stringValue(created.Placement.Zone) != "ap-guangzhou-3" || stringValue(created.DiskChargeType) != "PREPAID" ||
		created.DiskCount == nil || *created.DiskCount != 1 || created.DiskSize == nil || *created.DiskSize != 10 || stringValue(created.DiskType) != "CLOUD_BSSD" {
		t.Fatalf("unexpected prepaid CBS request: %#v", created)
	}
	if created.DiskChargePrepaid == nil || created.DiskChargePrepaid.Period == nil || *created.DiskChargePrepaid.Period != 1 || stringValue(created.DiskChargePrepaid.RenewFlag) != "NOTIFY_AND_MANUAL_RENEW" {
		t.Fatalf("CBS must use one-month manual renewal: %#v", created.DiskChargePrepaid)
	}
	if stringValue(created.ClientToken) == "" || stringValue(created.ClientToken) != stringValue(api.createDisksRequests[1].ClientToken) {
		t.Fatalf("CBS replay must reuse a stable ClientToken: %#v", api.createDisksRequests)
	}
	tags := map[string]string{}
	for _, tag := range created.Tags {
		tags[stringValue(tag.Key)] = stringValue(tag.Value)
	}
	if tags["opl_account_id"] != "acct-alpha" || tags["opl_workspace_id"] != "ws-alpha" || tags["opl_resource_id"] != "storage-alpha" || tags["opl_operation_id"] != "op-storage-alpha" {
		t.Fatalf("CBS request missing ownership tags: %#v", tags)
	}
	if len(api.describeDisksRequests) != 2 || len(api.describeDisksRequests[0].DiskIds) != 1 || stringValue(api.describeDisksRequests[0].DiskIds[0]) != "disk-storage-alpha" {
		t.Fatalf("create must read back the exact CBS disk: %#v", api.describeDisksRequests)
	}
}

func TestTencentSDKCreateStorageVolumeRequiresOwnershipTagsBeforeMutation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(map[string]string)
	}{
		{name: "missing_account", configure: func(tags map[string]string) { delete(tags, "opl_account_id") }},
		{name: "missing_workspace", configure: func(tags map[string]string) { delete(tags, "opl_workspace_id") }},
		{name: "missing_resource", configure: func(tags map[string]string) { delete(tags, "opl_resource_id") }},
		{name: "missing_operation", configure: func(tags map[string]string) { delete(tags, "opl_operation_id") }},
		{name: "mismatched_account", configure: func(tags map[string]string) { tags["opl_account_id"] = "acct-other" }},
		{name: "mismatched_resource", configure: func(tags map[string]string) { tags["opl_resource_id"] = "storage-other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tags := map[string]string{
				"opl_account_id": "acct-alpha", "opl_workspace_id": "ws-alpha", "opl_resource_id": "storage-alpha", "opl_operation_id": "op-storage-alpha",
			}
			tc.configure(tags)
			api := &fakeNativeCbsAPI{}
			response := (&tencentSDKClient{nativeCbsClient: api}).CreateStorageVolume(Request{
				AccountId: "acct-alpha", Tags: tags, Storage: StorageInput{Id: "storage-alpha", SizeGB: 10, Zone: "ap-guangzhou-3", DiskType: "CLOUD_BSSD"},
			}, nil)
			if response.Ok || response.ErrorCode != "tencent_cbs_input_invalid" || len(api.createDisksRequests) != 0 {
				t.Fatalf("invalid ownership mutated CBS: response=%#v requests=%#v", response, api.createDisksRequests)
			}
		})
	}
}

func TestTencentSDKCreateStorageVolumePreservesDiskIdentityWhenReadbackFails(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*fakeNativeCbsAPI)
	}{
		{name: "timeout", configure: func(api *fakeNativeCbsAPI) { api.err = errors.New("describe timed out") }},
		{name: "partial", configure: func(api *fakeNativeCbsAPI) { api.omitDeadline = true }},
		{name: "mismatch", configure: func(api *fakeNativeCbsAPI) { api.duplicate = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeNativeCbsAPI{}
			tc.configure(api)
			response := (&tencentSDKClient{nativeCbsClient: api}).CreateStorageVolume(Request{
				AccountId: "acct-alpha", Tags: map[string]string{
					"opl_account_id": "acct-alpha", "opl_workspace_id": "ws-alpha", "opl_resource_id": "storage-alpha", "opl_operation_id": "op-storage-alpha",
				},
				Storage: StorageInput{Id: "storage-alpha", SizeGB: 10, Zone: "ap-guangzhou-3", DiskType: "CLOUD_BSSD"},
			}, nil)
			if response.Ok || response.StorageVolumeId != "disk-storage-alpha" || response.ProviderRequestId == "" {
				t.Fatalf("post-create readback failure lost CBS identity: %#v", response)
			}
		})
	}
}

func TestTencentSDKStorageVolumeReadbackFailsClosedOnBillingOrIdentityMismatch(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*fakeNativeCbsAPI)
	}{
		{name: "ambiguous", configure: func(api *fakeNativeCbsAPI) { api.duplicate = true }},
		{name: "wrong id", configure: func(api *fakeNativeCbsAPI) { api.diskID = "disk-other" }},
		{name: "wrong name", configure: func(api *fakeNativeCbsAPI) { api.diskName = "storage-other" }},
		{name: "wrong usage", configure: func(api *fakeNativeCbsAPI) { api.diskUsage = "SYSTEM_DISK" }},
		{name: "wrong account tag", configure: func(api *fakeNativeCbsAPI) { api.tags = map[string]string{"opl_account_id": "acct-other"} }},
		{name: "wrong workspace tag", configure: func(api *fakeNativeCbsAPI) { api.tags = map[string]string{"opl_workspace_id": "ws-other"} }},
		{name: "wrong resource tag", configure: func(api *fakeNativeCbsAPI) { api.tags = map[string]string{"opl_resource_id": "storage-other"} }},
		{name: "wrong operation tag", configure: func(api *fakeNativeCbsAPI) { api.tags = map[string]string{"opl_operation_id": "op-other"} }},
		{name: "postpaid", configure: func(api *fakeNativeCbsAPI) { api.diskChargeType = "POSTPAID_BY_HOUR" }},
		{name: "auto renew", configure: func(api *fakeNativeCbsAPI) { api.renewFlag = "NOTIFY_AND_AUTO_RENEW" }},
		{name: "wrong type", configure: func(api *fakeNativeCbsAPI) { api.diskType = "CLOUD_SSD" }},
		{name: "wrong size", configure: func(api *fakeNativeCbsAPI) { api.diskSize = 20 }},
		{name: "wrong zone", configure: func(api *fakeNativeCbsAPI) { api.zone = "ap-guangzhou-4" }},
		{name: "deadline missing", configure: func(api *fakeNativeCbsAPI) { api.omitDeadline = true }},
		{name: "deadline invalid", configure: func(api *fakeNativeCbsAPI) { api.deadline = "not-a-deadline" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeNativeCbsAPI{}
			tc.configure(api)
			client := &tencentSDKClient{nativeCbsClient: api}
			response := client.SyncStorageVolume(Request{
				AccountId: "acct-alpha",
				Tags:      map[string]string{"opl_account_id": "acct-alpha", "opl_workspace_id": "ws-alpha", "opl_resource_id": "storage-alpha", "opl_operation_id": "op-storage-alpha"},
				Storage:   StorageInput{Id: "disk-storage-alpha", SizeGB: 10, Zone: "ap-guangzhou-3", DiskType: "CLOUD_BSSD"},
			}, nil)
			if response.Ok {
				t.Fatalf("mismatched CBS readback must fail closed: %#v", response)
			}
		})
	}
}

func TestNormalizeTencentDeadlineRequiresExplicitTimezone(t *testing.T) {
	if got := normalizeTencentDeadline("2026-08-16 00:00:00"); got != "" {
		t.Fatalf("timezone-less deadline was accepted as %q", got)
	}
	if got := normalizeTencentDeadline("2026-08-16T08:00:00+08:00"); got != "2026-08-16T00:00:00Z" {
		t.Fatalf("RFC3339 deadline normalized to %q", got)
	}
}

func cbsReadbackRequest(storage StorageInput) Request {
	return Request{
		AccountId: "acct-alpha",
		Tags:      map[string]string{"opl_account_id": "acct-alpha", "opl_workspace_id": "ws-alpha", "opl_resource_id": "storage-alpha", "opl_operation_id": "op-storage-alpha"},
		Storage:   storage,
	}
}

func computeRenewalRequest() Request {
	return Request{
		AccountId: "acct-alpha", Zone: "ap-guangzhou-3", Tags: computeOwnershipTags(), Pool: ComputePoolInput{InstanceType: "SA5.LARGE4"},
		Allocation: ComputeAllocationInput{Id: "compute-alpha", InstanceId: "ins-basic-1", Deadline: "2026-08-16T00:00:00Z"},
	}
}

func TestTencentSDKSyncStorageVolumeRejectsTimezoneLessDeadline(t *testing.T) {
	response := (&tencentSDKClient{nativeCbsClient: &fakeNativeCbsAPI{deadline: "2026-08-16 00:00:00"}}).SyncStorageVolume(cbsReadbackRequest(
		StorageInput{Id: "disk-storage-alpha", SizeGB: 10, Zone: "ap-guangzhou-3", DiskType: "CLOUD_BSSD"}), nil)
	if response.Ok || response.ErrorCode != "tencent_cbs_readback_mismatch" || response.ProviderData["deadline"] != "" {
		t.Fatalf("timezone-less CBS deadline did not fail closed: %#v", response)
	}
}

func TestTencentSDKSyncStorageVolumeReturnsOnlyExactConfirmedAbsence(t *testing.T) {
	for _, api := range []*fakeNativeCbsAPI{
		{empty: true},
		{err: tcerrors.NewTencentCloudSDKError(cbs2017.INVALIDDISKID_NOTFOUND, "disk missing", "req-cbs-not-found")},
	} {
		client := &tencentSDKClient{nativeCbsClient: api}
		response := client.SyncStorageVolume(cbsReadbackRequest(StorageInput{Id: "disk-storage-alpha", SizeGB: 10, Zone: "ap-guangzhou-3", DiskType: "CLOUD_BSSD"}), nil)
		if !response.Ok || response.Status != "external_deleted" || response.CBSStatus != "NOT_FOUND" || response.StorageVolumeId != "disk-storage-alpha" {
			t.Fatalf("confirmed CBS absence = %#v", response)
		}
	}
	createAPI := &fakeNativeCbsAPI{empty: true}
	created := (&tencentSDKClient{nativeCbsClient: createAPI}).CreateStorageVolume(Request{AccountId: "acct-alpha", Storage: StorageInput{Id: "storage-alpha", SizeGB: 10, Zone: "ap-guangzhou-3"}}, nil)
	if created.Ok || created.Status == "external_deleted" {
		t.Fatalf("post-create missing readback is unknown, not confirmed absence: %#v", created)
	}
}

func TestTencentSDKRenewStorageVolumeIsManualAndIdempotentAcrossLostResponse(t *testing.T) {
	api := &fakeNativeCbsAPI{deadline: "2026-08-16T00:00:00Z"}
	client := &tencentSDKClient{nativeCbsClient: api}
	request := cbsReadbackRequest(StorageInput{Id: "disk-storage-alpha", SizeGB: 10, Zone: "ap-guangzhou-3", DiskType: "CLOUD_BSSD", Deadline: "2026-08-16T00:00:00Z"})

	first := client.RenewStorageVolume(request, nil)
	second := client.RenewStorageVolume(request, nil)

	if !first.Ok || !second.Ok || len(api.renewDiskRequests) != 1 {
		t.Fatalf("renewal replay must not renew twice: first=%#v second=%#v requests=%#v", first, second, api.renewDiskRequests)
	}
	renewed := api.renewDiskRequests[0]
	if stringValue(renewed.DiskId) != "disk-storage-alpha" || renewed.DiskChargePrepaid == nil || renewed.DiskChargePrepaid.Period == nil || *renewed.DiskChargePrepaid.Period != 1 || stringValue(renewed.DiskChargePrepaid.RenewFlag) != "NOTIFY_AND_MANUAL_RENEW" {
		t.Fatalf("unexpected CBS renewal request: %#v", renewed)
	}
	if second.ProviderData["renewalResult"] != "already_renewed" || second.ProviderData["deadline"] != "2026-09-16T00:00:00Z" {
		t.Fatalf("renew replay must return exact current deadline: %#v", second)
	}
}

func TestTencentSDKRenewComputeAllocationIsManualAndIdempotentAcrossLostResponse(t *testing.T) {
	api := &fakeNativeCvmAPI{instanceName: "compute-alpha", expiredTime: "2026-08-16T08:00:00+08:00", renewedExpiredTime: "2026-09-16T08:00:00+08:00", tags: computeOwnershipTags()}
	client := &tencentSDKClient{nativeCvmClient: api}
	request := computeRenewalRequest()

	first := client.RenewComputeAllocation(request, nil)
	second := client.RenewComputeAllocation(request, nil)

	if !first.Ok || !second.Ok || len(api.renewInstancesRequests) != 1 {
		t.Fatalf("compute renewal replay must not renew twice: first=%#v second=%#v requests=%#v", first, second, api.renewInstancesRequests)
	}
	renewed := api.renewInstancesRequests[0]
	if len(renewed.InstanceIds) != 1 || stringValue(renewed.InstanceIds[0]) != "ins-basic-1" || renewed.InstanceChargePrepaid == nil || renewed.InstanceChargePrepaid.Period == nil || *renewed.InstanceChargePrepaid.Period != 1 || stringValue(renewed.InstanceChargePrepaid.RenewFlag) != "NOTIFY_AND_MANUAL_RENEW" {
		t.Fatalf("unexpected CVM renewal request: %#v", renewed)
	}
	if renewed.RenewPortableDataDisk == nil || *renewed.RenewPortableDataDisk {
		t.Fatalf("CVM renewal must leave independently billed CBS untouched: %#v", renewed.RenewPortableDataDisk)
	}
	if first.ProviderData["deadline"] != "2026-09-16T00:00:00Z" || first.ProviderData["renewalResult"] != "renewed" || second.ProviderData["renewalResult"] != "already_renewed" {
		t.Fatalf("unexpected renewal facts: first=%#v second=%#v", first.ProviderData, second.ProviderData)
	}
}

func TestTencentSDKRenewComputeAllocationReadbackFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*fakeNativeCvmAPI)
	}{
		{name: "wrong instance", configure: func(api *fakeNativeCvmAPI) { api.returnedInstanceID = "ins-other" }},
		{name: "wrong ownership", configure: func(api *fakeNativeCvmAPI) { api.instanceName = "compute-other" }},
		{name: "postpaid", configure: func(api *fakeNativeCvmAPI) { api.instanceChargeType = "POSTPAID_BY_HOUR" }},
		{name: "auto renew", configure: func(api *fakeNativeCvmAPI) { api.renewFlag = "NOTIFY_AND_AUTO_RENEW" }},
		{name: "deadline missing", configure: func(api *fakeNativeCvmAPI) { api.omitExpiredTime = true }},
		{name: "deadline invalid", configure: func(api *fakeNativeCvmAPI) { api.expiredTime = "not-a-deadline" }},
		{name: "malformed", configure: func(api *fakeNativeCvmAPI) { api.nilEnvelope = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeNativeCvmAPI{tags: computeOwnershipTags()}
			tc.configure(api)
			client := &tencentSDKClient{nativeCvmClient: api}
			response := client.RenewComputeAllocation(computeRenewalRequest(), nil)
			if response.Ok || len(api.renewInstancesRequests) != 0 {
				t.Fatalf("invalid CVM readback must fail before renewal: response=%#v requests=%#v", response, api.renewInstancesRequests)
			}
		})
	}
}

func TestTencentSDKRenewComputeAllocationRequiresExactSKUZoneAndOwnership(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*Request, *fakeNativeCvmAPI)
	}{
		{name: "missing SKU", configure: func(request *Request, _ *fakeNativeCvmAPI) { request.Pool.InstanceType = "" }},
		{name: "wrong SKU", configure: func(_ *Request, api *fakeNativeCvmAPI) { api.instanceType = "SA5.2XLARGE16" }},
		{name: "missing Zone", configure: func(request *Request, _ *fakeNativeCvmAPI) { request.Zone = "" }},
		{name: "wrong Zone", configure: func(_ *Request, api *fakeNativeCvmAPI) { api.zone = "ap-guangzhou-4" }},
		{name: "missing tags", configure: func(request *Request, _ *fakeNativeCvmAPI) { request.Tags = nil }},
		{name: "wrong tags", configure: func(_ *Request, api *fakeNativeCvmAPI) { api.tags["opl_workspace_id"] = "ws-other" }},
		{name: "wrong requested account tag", configure: func(request *Request, api *fakeNativeCvmAPI) {
			request.Tags["opl_account_id"] = "acct-other"
			api.tags["opl_account_id"] = "acct-other"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := computeRenewalRequest()
			api := &fakeNativeCvmAPI{instanceName: "compute-alpha", tags: computeOwnershipTags()}
			tc.configure(&request, api)
			response := (&tencentSDKClient{nativeCvmClient: api}).RenewComputeAllocation(request, nil)
			if response.Ok || len(api.renewInstancesRequests) != 0 {
				t.Fatalf("mismatched renewal identity must fail before mutation: response=%#v renew=%#v", response, api.renewInstancesRequests)
			}
		})
	}
}

func TestTencentSDKSyncComputeAllocationRequiresExpectedIdentityBeforeConfirmedAbsence(t *testing.T) {
	for _, missing := range []string{"zone", "tags"} {
		t.Run(missing, func(t *testing.T) {
			request := computeRenewalRequest()
			request.Pool.NodePoolId = "np-basic"
			request.Allocation.MachineName = "node-basic-1"
			request.Allocation.PrivateIp = "10.0.0.11"
			if missing == "zone" {
				request.Zone = ""
			} else {
				request.Tags = nil
			}
			client := newFakeTencentSDKClient(&fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 0})
			api := &fakeNativeCvmAPI{empty: true}
			client.nativeCvmClient = api
			response := client.SyncComputeAllocation(request, nil)
			if response.Ok || len(api.describeInstancesRequest) != 0 {
				t.Fatalf("missing %s returned confirmed absence: %#v", missing, response)
			}
		})
	}
}

func TestTencentSDKRenewComputeAllocationReturnsConfirmedAbsenceWithoutMutation(t *testing.T) {
	api := &fakeNativeCvmAPI{empty: true}
	client := &tencentSDKClient{nativeCvmClient: api}
	response := client.RenewComputeAllocation(computeRenewalRequest(), nil)
	if !response.Ok || response.Status != "external_deleted" || response.CVMStatus != "NOT_FOUND" || len(api.renewInstancesRequests) != 0 {
		t.Fatalf("confirmed CVM absence response=%#v requests=%#v", response, api.renewInstancesRequests)
	}
}

func TestTencentSDKRenewalsRejectNonIncreasingProviderDeadline(t *testing.T) {
	t.Run("CVM", func(t *testing.T) {
		api := &fakeNativeCvmAPI{expiredTime: "2026-08-16T00:00:00Z", renewedExpiredTime: "2026-07-16T00:00:00Z", tags: computeOwnershipTags()}
		client := &tencentSDKClient{nativeCvmClient: api}
		response := client.RenewComputeAllocation(computeRenewalRequest(), nil)
		if response.Ok {
			t.Fatalf("CVM renewal must reject a non-increasing deadline: %#v", response)
		}
	})
	t.Run("CBS", func(t *testing.T) {
		api := &fakeNativeCbsAPI{deadline: "2026-08-16T00:00:00Z", renewedDeadline: "2026-07-16T00:00:00Z"}
		client := &tencentSDKClient{nativeCbsClient: api}
		response := client.RenewStorageVolume(cbsReadbackRequest(StorageInput{Id: "disk-storage-alpha", SizeGB: 10, Zone: "ap-guangzhou-3", DiskType: "CLOUD_BSSD", Deadline: "2026-08-16T00:00:00Z"}), nil)
		if response.Ok {
			t.Fatalf("CBS renewal must reject a non-increasing deadline: %#v", response)
		}
	})
}

func (api *fakeNativeTkeAPI) record(call string) {
	api.calls = append(api.calls, call)
	if api.callLog != nil {
		*api.callLog = append(*api.callLog, call)
	}
}

type fakeNativeVpcAPI struct {
	describeSubnetsRequests []*vpc2017.DescribeSubnetsRequest
	omitSubnet              bool
	omitSubnetZone          bool
	availableIpCount        uint64
	zone                    string
	zones                   []string
}

func (api *fakeNativeVpcAPI) DescribeSubnets(request *vpc2017.DescribeSubnetsRequest) (*vpc2017.DescribeSubnetsResponse, error) {
	api.describeSubnetsRequests = append(api.describeSubnetsRequests, request)
	subnets := make([]*vpc2017.Subnet, 0, len(request.SubnetIds))
	for index, rawID := range request.SubnetIds {
		zoneName := firstNonEmpty(api.zone, "na-siliconvalley-1")
		if index < len(api.zones) {
			zoneName = api.zones[index]
		}
		zone := common.StringPtr(zoneName)
		if api.omitSubnetZone {
			zone = nil
		}
		subnets = append(subnets, &vpc2017.Subnet{SubnetId: rawID, Zone: zone, AvailableIpAddressCount: common.Uint64Ptr(firstNonZeroUint(api.availableIpCount, 8))})
	}
	if api.omitSubnet {
		subnets = nil
	}
	return &vpc2017.DescribeSubnetsResponse{Response: &vpc2017.DescribeSubnetsResponseParams{
		SubnetSet: subnets, TotalCount: common.Uint64Ptr(uint64(len(subnets))), RequestId: common.StringPtr("req-describe-subnets"),
	}}, nil
}

func (api *fakeNativeCvmAPI) DescribeAccountQuota(request *cvm2017.DescribeAccountQuotaRequest) (*cvm2017.DescribeAccountQuotaResponse, error) {
	api.describeAccountQuotaRequests = append(api.describeAccountQuotaRequests, request)
	remaining := common.Uint64Ptr(firstNonZeroUint(api.quotaRemaining, 8))
	if api.zeroQuota {
		remaining = common.Uint64Ptr(0)
	}
	if api.omitQuotaRemaining {
		remaining = nil
	}
	quotas := []*cvm2017.PrePaidQuota{{
		Zone: common.StringPtr("na-siliconvalley-1"), RemainingQuota: remaining, TotalQuota: common.Uint64Ptr(10), UsedQuota: common.Uint64Ptr(2),
	}}
	if api.ambiguousPrepaidQuota {
		quotas = append(quotas, &cvm2017.PrePaidQuota{Zone: common.StringPtr("na-siliconvalley-1"), RemainingQuota: common.Uint64Ptr(8)})
	}
	return &cvm2017.DescribeAccountQuotaResponse{Response: &cvm2017.DescribeAccountQuotaResponseParams{
		AccountQuotaOverview: &cvm2017.AccountQuotaOverview{AccountQuota: &cvm2017.AccountQuota{PrePaidQuotaSet: quotas}},
		RequestId:            common.StringPtr("req-account-quota"),
	}}, nil
}

func (api *fakeNativeCvmAPI) DescribeZoneInstanceConfigInfos(request *cvm2017.DescribeZoneInstanceConfigInfosRequest) (*cvm2017.DescribeZoneInstanceConfigInfosResponse, error) {
	api.describeZoneConfigRequests = append(api.describeZoneConfigRequests, request)
	var price *cvm2017.ItemPrice
	if !api.omitZoneConfigPrice {
		discount := api.zoneConfigDiscountPrice
		if discount == 0 {
			discount = 142.91
		}
		price = &cvm2017.ItemPrice{DiscountPrice: common.Float64Ptr(discount), OriginalPrice: common.Float64Ptr(150)}
	}
	items := []*cvm2017.InstanceTypeQuotaItem{{
		Zone: common.StringPtr(firstNonEmpty(api.zone, "na-siliconvalley-1")), InstanceType: common.StringPtr("SA5.LARGE4"), InstanceChargeType: common.StringPtr(firstNonEmpty(api.zoneConfigChargeType, "PREPAID")), Status: common.StringPtr(firstNonEmpty(api.zoneConfigStatus, "SELL")), Price: price,
	}}
	if api.omitZoneConfig {
		items = nil
	}
	return &cvm2017.DescribeZoneInstanceConfigInfosResponse{Response: &cvm2017.DescribeZoneInstanceConfigInfosResponseParams{
		InstanceTypeQuotaSet: items,
		RequestId:            common.StringPtr("req-zone-capacity"),
	}}, nil
}

func (api *fakeNativeCvmAPI) ModifyInstancesAttribute(request *cvm2017.ModifyInstancesAttributeRequest) (*cvm2017.ModifyInstancesAttributeResponse, error) {
	api.modifyInstancesRequest = append(api.modifyInstancesRequest, request)
	api.instanceName = stringValue(request.InstanceName)
	return &cvm2017.ModifyInstancesAttributeResponse{Response: &cvm2017.ModifyInstancesAttributeResponseParams{RequestId: common.StringPtr("req-modify-cvm")}}, nil
}

func (api *fakeNativeCvmAPI) DescribeInstances(request *cvm2017.DescribeInstancesRequest) (*cvm2017.DescribeInstancesResponse, error) {
	if api.callLog != nil {
		*api.callLog = append(*api.callLog, "DescribeCVMInstances")
	}
	api.describeInstancesRequest = append(api.describeInstancesRequest, request)
	call := len(api.describeInstancesRequest)
	if api.err != nil {
		return nil, api.err
	}
	if api.nilResponse || api.nilResponseCall == call {
		return nil, nil
	}
	if api.nilEnvelope || api.nilEnvelopeCall == call {
		return &cvm2017.DescribeInstancesResponse{}, nil
	}
	if api.nilInstanceCall == call {
		return &cvm2017.DescribeInstancesResponse{Response: &cvm2017.DescribeInstancesResponseParams{InstanceSet: []*cvm2017.Instance{nil}, TotalCount: common.Int64Ptr(1), RequestId: common.StringPtr("req-malformed-cvm")}}, nil
	}
	if api.empty {
		return &cvm2017.DescribeInstancesResponse{
			Response: &cvm2017.DescribeInstancesResponseParams{
				InstanceSet: []*cvm2017.Instance{},
				TotalCount:  common.Int64Ptr(0),
				RequestId:   common.StringPtr("req-describe-cvm-empty"),
			},
		}, nil
	}
	if len(request.InstanceIds) == 1 {
		expiredTime := common.StringPtr(firstNonEmpty(api.expiredTime, "2026-08-16T00:00:00Z"))
		if api.omitExpiredTime {
			expiredTime = nil
		}
		tags := []*cvm2017.Tag{}
		for key, value := range api.tags {
			tags = append(tags, &cvm2017.Tag{Key: common.StringPtr(key), Value: common.StringPtr(value)})
		}
		return &cvm2017.DescribeInstancesResponse{Response: &cvm2017.DescribeInstancesResponseParams{InstanceSet: []*cvm2017.Instance{{
			InstanceId: common.StringPtr(firstNonEmpty(api.returnedInstanceID, stringValue(request.InstanceIds[0]))), InstanceName: common.StringPtr(firstNonEmpty(api.instanceName, "compute-alpha")), InstanceType: common.StringPtr(firstNonEmpty(api.instanceType, "SA5.LARGE4")),
			PrivateIpAddresses: []*string{common.StringPtr("10.0.0.11")}, InstanceState: common.StringPtr("RUNNING"), Placement: &cvm2017.Placement{Zone: common.StringPtr(firstNonEmpty(api.zone, "ap-guangzhou-3"))},
			InstanceChargeType: common.StringPtr(firstNonEmpty(api.instanceChargeType, "PREPAID")), RenewFlag: common.StringPtr(firstNonEmpty(api.renewFlag, "NOTIFY_AND_MANUAL_RENEW")), ExpiredTime: expiredTime, Tags: tags,
		}}, TotalCount: common.Int64Ptr(1), RequestId: common.StringPtr("req-verify-cvm")}}, nil
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
	expiredTime := common.StringPtr(firstNonEmpty(api.expiredTime, "2026-08-16T00:00:00Z"))
	if api.omitExpiredTime {
		expiredTime = nil
	}
	return &cvm2017.DescribeInstancesResponse{
		Response: &cvm2017.DescribeInstancesResponseParams{
			InstanceSet: []*cvm2017.Instance{{
				InstanceId:         common.StringPtr(fmt.Sprintf("ins-basic-%d", instanceIndex)),
				InstanceName:       common.StringPtr(fmt.Sprintf("node-basic-%d", instanceIndex)),
				InstanceType:       common.StringPtr(firstNonEmpty(api.instanceType, "SA5.LARGE4")),
				PrivateIpAddresses: []*string{common.StringPtr(privateIp)},
				PublicIpAddresses:  []*string{common.StringPtr(publicIp)},
				Placement:          &cvm2017.Placement{Zone: common.StringPtr(firstNonEmpty(api.zone, "ap-guangzhou-3"))},
				InstanceChargeType: common.StringPtr(firstNonEmpty(api.instanceChargeType, "PREPAID")),
				RenewFlag:          common.StringPtr(firstNonEmpty(api.renewFlag, "NOTIFY_AND_MANUAL_RENEW")),
				ExpiredTime:        expiredTime,
			}},
			TotalCount: common.Int64Ptr(1),
			RequestId:  common.StringPtr("req-describe-cvm"),
		},
	}, nil
}

func newFakeTencentSDKClient(tkeAPI *fakeNativeTkeAPI) *tencentSDKClient {
	cvmAPI := &fakeNativeCvmAPI{}
	return &tencentSDKClient{
		region:          "ap-guangzhou",
		clusterId:       "cls-123",
		nativeTkeClient: tkeAPI,
		nativeCvmClient: cvmAPI,
		nativeTagClient: &fakeNativeTagAPI{cvm: cvmAPI},
	}
}

func (api *fakeNativeTkeAPI) CreateNodePool(request *tke2022.CreateNodePoolRequest) (*tke2022.CreateNodePoolResponse, error) {
	api.record("CreateNodePool")
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
	api.record("DescribeNodePools")
	api.describeNodePoolsRequest = append(api.describeNodePoolsRequest, request)
	if api.describeNodePoolErr != nil {
		return nil, api.describeNodePoolErr
	}
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
	pools := []*tke2022.NodePool{{
		NodePoolId: common.StringPtr(nodePoolId),
		Name:       common.StringPtr("pool-basic-2c4g"),
		Type:       common.StringPtr(firstNonEmpty(api.poolType, "Native")),
		LifeState:  common.StringPtr(firstNonEmpty(api.lifeState, "Running")),
		Labels: []*tke2022.Label{
			{Name: common.StringPtr("oplcloud.cn/pool-id"), Value: common.StringPtr(firstNonEmpty(api.labelPoolId, "pool-basic-2c4g"))},
			{Name: common.StringPtr("oplcloud.cn/package-id"), Value: common.StringPtr(firstNonEmpty(api.labelPackageId, "basic"))},
			{Name: common.StringPtr("oplcloud.cn/instance-type"), Value: common.StringPtr(firstNonEmpty(api.labelInstanceType, "SA5.LARGE4"))},
		},
		Native: fakeNativeNodePoolInfo(api),
	}}
	if api.ambiguousDiscovery && nodePoolIdFilterValue(request) == "" {
		duplicate := *pools[0]
		duplicate.NodePoolId = common.StringPtr("np-basic-duplicate")
		pools = append(pools, &duplicate)
	}
	totalCount := int64(len(pools))
	if api.truncatedDiscovery && nodePoolIdFilterValue(request) == "" {
		totalCount++
	}
	return &tke2022.DescribeNodePoolsResponse{
		Response: &tke2022.DescribeNodePoolsResponseParams{
			NodePools:  pools,
			TotalCount: common.Int64Ptr(totalCount),
			RequestId:  common.StringPtr("req-describe-pool"),
		},
	}, nil
}

func firstNonZero(value int64, fallback int64) int64 {
	if value != 0 {
		return value
	}
	return fallback
}

func firstNonZeroUint(value uint64, fallback uint64) uint64 {
	if value != 0 {
		return value
	}
	return fallback
}

func fakeNativeNodePoolInfo(api *fakeNativeTkeAPI) *tke2022.NativeNodePoolInfo {
	if api.omitNative {
		return nil
	}
	scaling := &tke2022.MachineSetScaling{MinReplicas: common.Int64Ptr(0), MaxReplicas: common.Int64Ptr(firstNonZero(api.maxReplicas, 10))}
	if api.omitScaling {
		scaling = nil
	}
	replicas := common.Int64Ptr(api.replicas)
	if api.omitReplicas {
		replicas = nil
	}
	readyReplicas := api.readyReplicas
	if readyReplicas == nil && !api.omitReplicas {
		readyReplicas = common.Int64Ptr(api.replicas)
	}
	if api.omitReadyReplicas {
		readyReplicas = nil
	}
	instanceTypes := api.instanceTypes
	if len(instanceTypes) == 0 {
		instanceTypes = []string{"SA5.LARGE4"}
	}
	subnetIds := api.subnetIds
	if len(subnetIds) == 0 {
		subnetIds = []string{"subnet-basic"}
	}
	prepaid := &tke2022.InstanceChargePrepaid{
		Period: common.Uint64Ptr(1), RenewFlag: common.StringPtr(firstNonEmpty(api.prepaidRenewFlag, "NOTIFY_AND_MANUAL_RENEW")),
	}
	if api.omitPrepaidPeriod {
		prepaid.Period = nil
	}
	if api.omitInstanceChargePrepaid {
		prepaid = nil
	}
	return &tke2022.NativeNodePoolInfo{
		Scaling: scaling, SubnetIds: stringsToPtrs(subnetIds), InstanceTypes: stringsToPtrs(instanceTypes), Replicas: replicas, ReadyReplicas: readyReplicas,
		EnableAutoscaling: common.BoolPtr(api.enableAutoscaling), AutoRepair: common.BoolPtr(api.autoRepair),
		MachineType: common.StringPtr(firstNonEmpty(api.machineType, "NativeCVM")), InstanceChargeType: common.StringPtr(firstNonEmpty(api.instanceChargeType, "PREPAID")),
		InstanceChargePrepaid: prepaid,
	}
}

func TestTencentSDKCapacityIsReadOnlyAndRequiresPrepaidQuota(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", discoverNodePoolId: "np-basic", replicas: 2, maxReplicas: 10, labelPoolId: "basic"}
	cvmAPI := &fakeNativeCvmAPI{}
	vpcAPI := &fakeNativeVpcAPI{}
	client := &tencentSDKClient{region: "na-siliconvalley", clusterId: "cls-123", nativeTkeClient: tkeAPI, nativeCvmClient: cvmAPI, nativeVpcClient: vpcAPI}

	response := client.Capacity(Request{
		Action:    "capacity_preflight",
		PackageId: "basic",
		Zone:      "na-siliconvalley-1",
		Pool:      ComputePoolInput{Id: "basic", InstanceType: "SA5.LARGE4", NodePoolId: "np-basic", DesiredReplicas: 5},
	}, map[string]string{})

	if !response.Ok || response.Status != "ready" || !response.InstanceAvailable || response.RemainingQuota != 8 || response.RequiredCapacity != 5 {
		t.Fatalf("unexpected capacity response: %#v", response)
	}
	if response.NodePoolId != "np-basic" || response.CurrentReplicas != 2 || response.MaxReplicas != 10 || response.MachineType != "NativeCVM" || response.TargetReplicas != 7 {
		t.Fatalf("unexpected node pool capacity: %#v", response)
	}
	if response.ProviderPriceCNY != 142.91 || response.ProviderRequestIDs["nodePool"] != "req-describe-pool" || response.ProviderRequestIDs["subnets"] != "req-describe-subnets" || response.ProviderRequestIDs["availability"] != "req-zone-capacity" || response.ProviderRequestIDs["quota"] != "req-account-quota" {
		t.Fatalf("capacity preflight price evidence = %#v", response)
	}
	if len(cvmAPI.describeAccountQuotaRequests) != 1 || len(cvmAPI.describeZoneConfigRequests) != 1 {
		t.Fatalf("prepaid capacity must query exact account quota and availability once: %#v", cvmAPI)
	}
	if len(vpcAPI.describeSubnetsRequests) != 1 {
		t.Fatalf("capacity must resolve the exact node pool subnets once: %#v", vpcAPI)
	}
	if got := tkeAPI.calls; len(got) != 1 || got[0] != "DescribeNodePools" {
		t.Fatalf("capacity must only describe the exact node pool: %#v", got)
	}
	if tkeAPI.scaleNodePoolRequest != nil || tkeAPI.createNodePoolRequest != nil || tkeAPI.modifyNodePoolRequest != nil {
		t.Fatalf("capacity probe must never mutate Tencent resources: %#v", tkeAPI)
	}
}

func TestTencentSDKCapacityFailsClosedOnMissingZeroOrAmbiguousPrepaidQuota(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*fakeNativeCvmAPI)
	}{
		{name: "missing remaining", configure: func(api *fakeNativeCvmAPI) { api.omitQuotaRemaining = true }},
		{name: "zero remaining", configure: func(api *fakeNativeCvmAPI) { api.zeroQuota = true }},
		{name: "ambiguous zone", configure: func(api *fakeNativeCvmAPI) { api.ambiguousPrepaidQuota = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 2, maxReplicas: 10, labelPoolId: "basic"}
			cvmAPI := &fakeNativeCvmAPI{}
			tc.configure(cvmAPI)
			client := &tencentSDKClient{region: "na-siliconvalley", clusterId: "cls-123", nativeTkeClient: tkeAPI, nativeCvmClient: cvmAPI, nativeVpcClient: &fakeNativeVpcAPI{}}
			response := client.Capacity(Request{Action: "capacity_preflight", PackageId: "basic", Zone: "na-siliconvalley-1", Pool: ComputePoolInput{Id: "basic", InstanceType: "SA5.LARGE4", NodePoolId: "np-basic", DesiredReplicas: 5}}, nil)
			if response.Ok || len(cvmAPI.describeAccountQuotaRequests) != 1 {
				t.Fatalf("invalid prepaid quota must fail closed: response=%#v quotaRequests=%d", response, len(cvmAPI.describeAccountQuotaRequests))
			}
			if tkeAPI.createNodePoolRequest != nil || tkeAPI.modifyNodePoolRequest != nil || tkeAPI.scaleNodePoolRequest != nil {
				t.Fatalf("quota preflight must remain read-only: %#v", tkeAPI)
			}
		})
	}
}

func TestTencentSDKCapacityRequiresExactSingleSKUAndZone(t *testing.T) {
	ready := int64(2)
	for _, tc := range []struct {
		name string
		tke  *fakeNativeTkeAPI
		vpc  *fakeNativeVpcAPI
	}{
		{
			name: "multiple SKUs",
			tke:  &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 2, maxReplicas: 10, readyReplicas: &ready, instanceTypes: []string{"SA5.LARGE4", "SA5.2XLARGE16"}},
			vpc:  &fakeNativeVpcAPI{},
		},
		{
			name: "multiple Zones",
			tke:  &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 2, maxReplicas: 10, readyReplicas: &ready, subnetIds: []string{"subnet-a", "subnet-b"}},
			vpc:  &fakeNativeVpcAPI{zones: []string{"na-siliconvalley-1", "na-siliconvalley-2"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &tencentSDKClient{region: "na-siliconvalley", clusterId: "cls-123", nativeTkeClient: tc.tke, nativeCvmClient: &fakeNativeCvmAPI{}, nativeVpcClient: tc.vpc}
			response := client.Capacity(Request{
				Action: "capacity_preflight", PackageId: "basic", Zone: "na-siliconvalley-1",
				Pool: ComputePoolInput{Id: "pool-basic-2c4g", InstanceType: "SA5.LARGE4", NodePoolId: "np-basic", DesiredReplicas: 5},
			}, nil)
			if response.Ok {
				t.Fatalf("non-exact pool must fail closed: %#v", response)
			}
		})
	}
}

func TestTencentSDKCapacityDiscoveryRequiresOneUntruncatedMatchingPool(t *testing.T) {
	for _, tc := range []struct {
		name string
		tke  *fakeNativeTkeAPI
	}{
		{name: "missing", tke: &fakeNativeTkeAPI{}},
		{name: "ambiguous", tke: &fakeNativeTkeAPI{discoverNodePoolId: "np-basic", ambiguousDiscovery: true}},
		{name: "truncated", tke: &fakeNativeTkeAPI{discoverNodePoolId: "np-basic", truncatedDiscovery: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &tencentSDKClient{region: "na-siliconvalley", clusterId: "cls-123", nativeTkeClient: tc.tke, nativeCvmClient: &fakeNativeCvmAPI{}, nativeVpcClient: &fakeNativeVpcAPI{}}
			response := client.Capacity(Request{Action: "capacity_preflight", PackageId: "basic", Pool: ComputePoolInput{
				Id: "pool-basic-2c4g", InstanceType: "SA5.LARGE4", DesiredReplicas: 5,
			}}, map[string]string{})
			if response.Ok || tc.tke.scaleNodePoolRequest != nil || tc.tke.createNodePoolRequest != nil || tc.tke.modifyNodePoolRequest != nil {
				t.Fatalf("discovery must fail closed without mutation: %#v", response)
			}
		})
	}
}

func TestTencentSDKCapacityPreflightFailsClosedWithoutMutation(t *testing.T) {
	ready := int64(2)
	cases := []struct {
		name string
		tke  *fakeNativeTkeAPI
		cvm  *fakeNativeCvmAPI
		vpc  *fakeNativeVpcAPI
	}{
		{name: "sold out", tke: &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 2, maxReplicas: 10, readyReplicas: &ready}, cvm: &fakeNativeCvmAPI{zoneConfigStatus: "SOLD_OUT"}},
		{name: "missing exact zone type charge", tke: &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 2, maxReplicas: 10, readyReplicas: &ready}, cvm: &fakeNativeCvmAPI{zoneConfigChargeType: "POSTPAID_BY_HOUR"}},
		{name: "price missing", tke: &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 2, maxReplicas: 10, readyReplicas: &ready}, cvm: &fakeNativeCvmAPI{omitZoneConfigPrice: true}},
		{name: "node pool missing", tke: &fakeNativeTkeAPI{nodePoolId: "np-other", replicas: 2, maxReplicas: 10, readyReplicas: &ready}, cvm: &fakeNativeCvmAPI{}},
		{name: "autoscaling enabled", tke: &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 2, maxReplicas: 10, readyReplicas: &ready, enableAutoscaling: true}, cvm: &fakeNativeCvmAPI{}},
		{name: "auto repair enabled", tke: &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 2, maxReplicas: 10, readyReplicas: &ready, autoRepair: true}, cvm: &fakeNativeCvmAPI{}},
		{name: "max below current plus five", tke: &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 2, maxReplicas: 6, readyReplicas: &ready}, cvm: &fakeNativeCvmAPI{}},
		{name: "replicas missing", tke: &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 2, maxReplicas: 10, omitReplicas: true}, cvm: &fakeNativeCvmAPI{}},
		{name: "ready replicas missing", tke: &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 2, maxReplicas: 10, omitReadyReplicas: true}, cvm: &fakeNativeCvmAPI{}},
		{name: "scaling missing", tke: &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 2, omitScaling: true, readyReplicas: &ready}, cvm: &fakeNativeCvmAPI{}},
		{name: "wrong instance type", tke: &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 2, maxReplicas: 10, readyReplicas: &ready, instanceTypes: []string{"SA5.2XLARGE16"}}, cvm: &fakeNativeCvmAPI{}},
		{name: "pool not running", tke: &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 2, maxReplicas: 10, readyReplicas: &ready, lifeState: "Creating"}, cvm: &fakeNativeCvmAPI{}},
		{name: "pool type is not native", tke: &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 2, maxReplicas: 10, readyReplicas: &ready, poolType: "Managed"}, cvm: &fakeNativeCvmAPI{}},
		{name: "pool machine type is CXM native", tke: &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 2, maxReplicas: 10, readyReplicas: &ready, machineType: "Native"}, cvm: &fakeNativeCvmAPI{}},
		{name: "pool is postpaid", tke: &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 2, maxReplicas: 10, readyReplicas: &ready, instanceChargeType: "POSTPAID_BY_HOUR"}, cvm: &fakeNativeCvmAPI{}},
		{name: "prepaid config missing", tke: &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 2, maxReplicas: 10, readyReplicas: &ready, omitInstanceChargePrepaid: true}, cvm: &fakeNativeCvmAPI{}},
		{name: "prepaid period missing", tke: &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 2, maxReplicas: 10, readyReplicas: &ready, omitPrepaidPeriod: true}, cvm: &fakeNativeCvmAPI{}},
		{name: "prepaid auto renew", tke: &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 2, maxReplicas: 10, readyReplicas: &ready, prepaidRenewFlag: "NOTIFY_AND_AUTO_RENEW"}, cvm: &fakeNativeCvmAPI{}},
		{name: "pool ownership labels mismatch", tke: &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 2, maxReplicas: 10, readyReplicas: &ready, labelPackageId: "pro"}, cvm: &fakeNativeCvmAPI{}},
		{name: "pool subnet missing", tke: &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 2, maxReplicas: 10, readyReplicas: &ready}, cvm: &fakeNativeCvmAPI{}, vpc: &fakeNativeVpcAPI{omitSubnet: true}},
		{name: "pool subnet lacks five ips", tke: &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 2, maxReplicas: 10, readyReplicas: &ready}, cvm: &fakeNativeCvmAPI{}, vpc: &fakeNativeVpcAPI{availableIpCount: 4}},
		{name: "pool subnet zone missing", tke: &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 2, maxReplicas: 10, readyReplicas: &ready}, cvm: &fakeNativeCvmAPI{}, vpc: &fakeNativeVpcAPI{omitSubnetZone: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vpcAPI := tc.vpc
			if vpcAPI == nil {
				vpcAPI = &fakeNativeVpcAPI{}
			}
			client := &tencentSDKClient{region: "na-siliconvalley", clusterId: "cls-123", nativeTkeClient: tc.tke, nativeCvmClient: tc.cvm, nativeVpcClient: vpcAPI}
			response := client.Capacity(Request{Action: "capacity_preflight", PackageId: "basic", Pool: ComputePoolInput{
				Id: "pool-basic-2c4g", InstanceType: "SA5.LARGE4", NodePoolId: "np-basic", DesiredReplicas: 5,
			}}, map[string]string{})
			if response.Ok {
				t.Fatalf("capacity preflight must fail closed: %#v", response)
			}
			if tc.tke.scaleNodePoolRequest != nil || tc.tke.createNodePoolRequest != nil || tc.tke.modifyNodePoolRequest != nil {
				t.Fatalf("failed preflight must remain read-only: %#v", tc.tke)
			}
		})
	}
}

func (api *fakeNativeTkeAPI) DescribeClusterInstances(request *tke2022.DescribeClusterInstancesRequest) (*tke2022.DescribeClusterInstancesResponse, error) {
	api.record("DescribeClusterInstances")
	api.describeInstancesRequest = append(api.describeInstancesRequest, request)
	if api.describeClusterInstancesErr != nil {
		return nil, api.describeClusterInstancesErr
	}
	privateIp := clusterInstanceFilterValue(request, "VagueIpAddress")
	nodePoolId := clusterInstanceFilterValue(request, "NodePoolIds")
	instances := []*tke2022.Instance{}
	for index := int64(1); index <= api.replicas && !api.omitClusterInstances; index++ {
		lanIp := fmt.Sprintf("10.0.0.%d", index+10)
		if privateIp != "" && privateIp != lanIp {
			continue
		}
		currentNodePoolId := api.nodePoolId
		if int(index) <= len(api.machinePoolIds) {
			currentNodePoolId = api.machinePoolIds[index-1]
		}
		if currentNodePoolId == "" {
			currentNodePoolId = "np-basic"
		}
		if nodePoolId != "" && nodePoolId != currentNodePoolId {
			continue
		}
		instanceNodePoolId := currentNodePoolId
		if api.omitInstanceNodePool {
			instanceNodePoolId = ""
		}
		instanceID := firstNonEmpty(api.clusterInstanceID, fmt.Sprintf("np-native-%d", index))
		if api.machineInstanceIDsMatch {
			instanceID = fmt.Sprintf("node-basic-%d", index)
		}
		instances = append(instances, &tke2022.Instance{
			InstanceId:    common.StringPtr(instanceID),
			InstanceState: common.StringPtr("running"),
			LanIP:         common.StringPtr(lanIp),
			NodePoolId:    common.StringPtr(instanceNodePoolId),
			NodeType:      common.StringPtr(firstNonEmpty(api.nodeType, "Native")),
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
	api.record("DescribeClusterMachines")
	api.describeMachinesRequest = append(api.describeMachinesRequest, request)
	if api.describeMachineErr != nil {
		return nil, api.describeMachineErr
	}
	if api.rejectMachinePoolFilter && clusterMachineNodePoolIdFilterValue(request) != "" {
		return nil, errors.New("[TencentCloudSDKError] Code=InvalidParameter, Message=invalid filter name NodePoolsId")
	}
	machines := []*tke2022.Machine{}
	for index := int64(1); index <= api.replicas; index++ {
		machineName := fmt.Sprintf("node-basic-%d", index)
		if api.deletedMachineNames[machineName] {
			continue
		}
		lanIP := fmt.Sprintf("10.0.0.%d", index+10)
		if api.omitMachineLanIP {
			lanIP = ""
		}
		machines = append(machines, &tke2022.Machine{
			MachineName:  common.StringPtr(machineName),
			MachineState: common.StringPtr("Running"),
			LanIP:        common.StringPtr(lanIP),
			InstanceType: common.StringPtr(firstNonEmpty(api.machineInstanceType, "SA5.LARGE4")),
		})
	}
	if api.duplicateMachineName && clusterMachineNodePoolIdFilterValue(request) == "" && len(machines) > 0 {
		duplicate := *machines[0]
		machines = append(machines, &duplicate)
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
	api.record("ScaleNodePool")
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
	api.record("ModifyNodePool")
	api.modifyNodePoolRequest = request
	if request.Native != nil && request.Native.EnableAutoscaling != nil {
		api.enableAutoscaling = *request.Native.EnableAutoscaling
	}
	if request.Native != nil && request.Native.AutoRepair != nil {
		api.autoRepair = *request.Native.AutoRepair
	}
	return &tke2022.ModifyNodePoolResponse{
		Response: &tke2022.ModifyNodePoolResponseParams{
			RequestId: common.StringPtr("req-modify-pool"),
		},
	}, nil
}

func (api *fakeNativeTkeAPI) DeleteClusterMachines(request *tke2022.DeleteClusterMachinesRequest) (*tke2022.DeleteClusterMachinesResponse, error) {
	api.record("DeleteClusterMachines")
	api.deleteMachinesRequest = request
	if api.deletedMachineNames == nil {
		api.deletedMachineNames = map[string]bool{}
	}
	if !api.retainDeletedMachines {
		for _, name := range request.MachineNames {
			api.deletedMachineNames[stringValue(name)] = true
		}
	}
	return &tke2022.DeleteClusterMachinesResponse{
		Response: &tke2022.DeleteClusterMachinesResponseParams{
			RequestId: common.StringPtr("req-delete-machine"),
		},
	}, nil
}

func TestTencentSDKClientCreateAllocationRejectsSelfProvisioningBeforeExplicitScale(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1, enableAutoscaling: true, autoRepair: true}
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

	if response.Ok || response.ErrorCode != "tencent_node_pool_configuration_mismatch" {
		t.Fatalf("self-provisioning pool must fail closed: %#v", response)
	}
	if tkeAPI.modifyNodePoolRequest != nil || tkeAPI.scaleNodePoolRequest != nil {
		t.Fatalf("conflicting pool readback must remain mutation-free: %#v", tkeAPI.calls)
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
	if response.ProviderData["zone"] != "ap-guangzhou-3" {
		t.Fatalf("native allocation must preserve the exact CVM Zone for CBS binding: %#v", response.ProviderData)
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

func TestTencentSDKClientCreateAllocationUsesExactProPoolAndSKU(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{
		nodePoolId: "np-pro", replicas: 1, labelPoolId: "pool-pro-8c16g", labelPackageId: "pro", labelInstanceType: "SA5.2XLARGE16",
		instanceTypes: []string{"SA5.2XLARGE16"}, machineInstanceType: "SA5.2XLARGE16",
	}
	client := newFakeTencentSDKClient(tkeAPI)
	client.nativeCvmClient = &fakeNativeCvmAPI{instanceType: "SA5.2XLARGE16", expiredTime: "2026-08-16T08:00:00+08:00"}

	response := client.CreateComputeAllocation(Request{
		AccountId: "pi-pro", UserId: "usr-pro", PackageId: "pro",
		Pool:       ComputePoolInput{Id: "pool-pro-8c16g", InstanceType: "SA5.2XLARGE16", NodePoolId: "np-pro"},
		Allocation: ComputeAllocationInput{Id: "compute-pro"},
	}, map[string]string{})

	if !response.Ok || response.NodePoolId != "np-pro" || response.ProviderData["instanceType"] != "SA5.2XLARGE16" || response.ProviderData["deadline"] != "2026-08-16T00:00:00Z" || tkeAPI.scaleNodePoolRequest == nil || stringValue(tkeAPI.scaleNodePoolRequest.NodePoolId) != "np-pro" {
		t.Fatalf("Pro create did not preserve exact pool/SKU: response=%#v scale=%#v", response, tkeAPI.scaleNodePoolRequest)
	}
}

func TestTencentSDKClientCreateAllocationRequiresCvmIdentityWithoutTkeFallback(t *testing.T) {
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

	if response.Ok || response.ErrorCode != "compute_cvm_identity_required" || len(tkeAPI.describeInstancesRequest) != 0 {
		t.Fatalf("CVM allocation must fail without a real CVM identity and must not fall back to TKE instance IDs: %#v", response)
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

func TestTencentSDKClientPoolFallbackExcludesMachinesFromOtherPools(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{
		nodePoolId:              "np-basic",
		replicas:                2,
		rejectMachinePoolFilter: true,
		machinePoolIds:          []string{"np-pro", "np-basic"},
	}
	client := newFakeTencentSDKClient(tkeAPI)

	response := client.ReconcileComputePool(Request{
		PackageId: "basic",
		Pool:      ComputePoolInput{Id: "pool-basic-2c4g", InstanceType: "SA5.LARGE4", NodePoolId: "np-basic", DesiredReplicas: 2},
	}, map[string]string{})

	if !response.Ok || len(response.Machines) != 1 || response.Machines[0].MachineId != "node-basic-2" || response.Machines[0].Zone != "ap-guangzhou-3" {
		t.Fatalf("pool fallback leaked another pool's machine: %#v", response)
	}
}

func TestTencentSDKClientReconcileRequiresCvmIdentityWhenCVMIsAbsent(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1}
	client := newFakeTencentSDKClient(tkeAPI)
	client.nativeCvmClient = &fakeNativeCvmAPI{empty: true}

	response := client.ReconcileComputePool(Request{
		PackageId: "basic",
		Pool:      ComputePoolInput{Id: "pool-basic-2c4g", InstanceType: "SA5.LARGE4", NodePoolId: "np-basic", DesiredReplicas: 1},
	}, map[string]string{})

	if response.Ok || response.ErrorCode != "compute_cvm_identity_required" || len(tkeAPI.describeInstancesRequest) != 0 {
		t.Fatalf("CVM pool reconcile must fail instead of using TKE-native identity: %#v", response)
	}
}

func TestTencentSDKClientReconcileRejectsIncompleteCvmBillingFacts(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*fakeNativeCvmAPI)
	}{
		{name: "postpaid", configure: func(api *fakeNativeCvmAPI) { api.instanceChargeType = "POSTPAID_BY_HOUR" }},
		{name: "auto renew", configure: func(api *fakeNativeCvmAPI) { api.renewFlag = "NOTIFY_AND_AUTO_RENEW" }},
		{name: "deadline missing", configure: func(api *fakeNativeCvmAPI) { api.omitExpiredTime = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1}
			cvmAPI := &fakeNativeCvmAPI{}
			tc.configure(cvmAPI)
			client := newFakeTencentSDKClient(tkeAPI)
			client.nativeCvmClient = cvmAPI
			response := client.ReconcileComputePool(Request{PackageId: "basic", Pool: ComputePoolInput{Id: "pool-basic-2c4g", InstanceType: "SA5.LARGE4", NodePoolId: "np-basic", DesiredReplicas: 1}}, nil)
			if response.Ok || len(response.Machines) != 0 {
				t.Fatalf("incomplete per-CVM billing facts must block claim: %#v", response)
			}
		})
	}
}

func TestTencentSDKClientSyncRequiresOwnedPrepaidCvmBillingFacts(t *testing.T) {
	request := Request{AccountId: "acct-alpha", Zone: "ap-guangzhou-3", Tags: computeOwnershipTags(), Pool: ComputePoolInput{Id: "basic", NodePoolId: "np-basic", InstanceType: "SA5.LARGE4"}, Allocation: ComputeAllocationInput{
		Id: "compute-alpha", InstanceId: "ins-basic-1", MachineName: "node-basic-1", NodeName: "10.0.0.11", PrivateIp: "10.0.0.11",
	}}
	client := newFakeTencentSDKClient(&fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1})
	client.nativeCvmClient = &fakeNativeCvmAPI{instanceName: "compute-alpha", expiredTime: "2026-08-16T08:00:00+08:00", tags: computeOwnershipTags()}
	response := client.SyncComputeAllocation(request, nil)
	if !response.Ok || response.InstanceType != "SA5.LARGE4" || response.ProviderData["instanceType"] != "SA5.LARGE4" || response.ProviderData["chargeType"] != "PREPAID" || response.ProviderData["renewFlag"] != "NOTIFY_AND_MANUAL_RENEW" || response.ProviderData["deadline"] != "2026-08-16T00:00:00Z" || response.ProviderData["zone"] != "ap-guangzhou-3" {
		t.Fatalf("sync must return exact owned CVM billing facts: %#v", response)
	}
	for _, tc := range []struct {
		name      string
		configure func(*fakeNativeCvmAPI)
	}{
		{name: "ownership mismatch", configure: func(api *fakeNativeCvmAPI) { api.instanceName = "compute-other" }},
		{name: "postpaid", configure: func(api *fakeNativeCvmAPI) { api.instanceChargeType = "POSTPAID_BY_HOUR" }},
		{name: "auto renew", configure: func(api *fakeNativeCvmAPI) { api.renewFlag = "NOTIFY_AND_AUTO_RENEW" }},
		{name: "deadline missing", configure: func(api *fakeNativeCvmAPI) { api.omitExpiredTime = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newFakeTencentSDKClient(&fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1})
			api := &fakeNativeCvmAPI{tags: computeOwnershipTags()}
			tc.configure(api)
			client.nativeCvmClient = api
			if got := client.SyncComputeAllocation(request, nil); got.Ok {
				t.Fatalf("invalid CVM sync must fail closed: %#v", got)
			}
		})
	}
}

func TestTencentSDKClientSyncRejectsWrongSelfConsistentInstanceType(t *testing.T) {
	request := Request{AccountId: "acct-alpha", Zone: "ap-guangzhou-3", Tags: computeOwnershipTags(), Pool: ComputePoolInput{Id: "basic", NodePoolId: "np-basic", InstanceType: "SA5.LARGE4"}, Allocation: ComputeAllocationInput{
		Id: "compute-alpha", InstanceId: "ins-basic-1", MachineName: "node-basic-1", NodeName: "10.0.0.11", PrivateIp: "10.0.0.11",
	}}
	client := newFakeTencentSDKClient(&fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1, machineInstanceType: "SA5.2XLARGE16"})
	client.nativeCvmClient = &fakeNativeCvmAPI{instanceName: "compute-alpha", instanceType: "SA5.2XLARGE16", tags: computeOwnershipTags()}

	response := client.SyncComputeAllocation(request, nil)
	if response.Ok || response.ErrorCode != "compute_instance_type_mismatch" {
		t.Fatalf("wrong but self-consistent SKU must fail exact package claim: %#v", response)
	}
}

func TestTencentSDKClientReconcileCompletesNativeIdentityWhenMachineLanIPIsMissing(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1, omitMachineLanIP: true, machineInstanceIDsMatch: true}
	client := newFakeTencentSDKClient(tkeAPI)
	client.nativeCvmClient = &fakeNativeCvmAPI{empty: true}

	response := client.ReconcileComputePool(Request{
		PackageId: "basic",
		Pool:      ComputePoolInput{Id: "pool-basic-2c4g", InstanceType: "SA5.LARGE4", NodePoolId: "np-basic", DesiredReplicas: 1},
	}, map[string]string{})

	if response.Ok || response.ErrorCode != "compute_cvm_identity_required" {
		t.Fatalf("TKE-discovered private IP still requires a real CVM identity: %#v", response)
	}
}

func TestTencentSDKTagComputeMachineRejectsNonCVMIdentity(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1}
	cvmAPI := &fakeNativeCvmAPI{err: errors.New("native identity must not call CVM")}
	client := &tencentSDKClient{clusterId: "cls-123", nativeTkeClient: tkeAPI, nativeCvmClient: cvmAPI}

	response := client.TagComputeMachine(Request{
		Tags: computeOwnershipTags(),
		Pool: ComputePoolInput{NodePoolId: "np-basic"},
		Allocation: ComputeAllocationInput{
			InstanceId: "np-native-1", MachineName: "node-basic-1", NodeName: "10.0.0.11", PrivateIp: "10.0.0.11",
		},
	}, nil)

	if response.Ok || response.ErrorCode != "compute_machine_identity_unverified" || len(cvmAPI.describeInstancesRequest) != 0 || len(cvmAPI.modifyInstancesRequest) != 0 {
		t.Fatalf("native tag response=%#v describe requests=%#v modify requests=%#v", response, cvmAPI.describeInstancesRequest, cvmAPI.modifyInstancesRequest)
	}
}

func TestTencentSDKClientRejectsCxmPoolBeforeAnyMutation(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1, machineType: "Native"}
	client := newFakeTencentSDKClient(tkeAPI)
	response := client.CreateComputeAllocation(Request{
		AccountId: "pi-alpha", PackageId: "basic",
		Pool:       ComputePoolInput{Id: "pool-basic-2c4g", InstanceType: "SA5.LARGE4", NodePoolId: "np-basic"},
		Allocation: ComputeAllocationInput{Id: "compute-alpha"},
	}, map[string]string{})
	if response.Ok || response.ErrorCode != "tencent_cvm_node_pool_required" {
		t.Fatalf("existing CXM pool must fail closed: %#v", response)
	}
	if tkeAPI.modifyNodePoolRequest != nil || tkeAPI.scaleNodePoolRequest != nil || tkeAPI.createNodePoolRequest != nil {
		t.Fatalf("CXM rejection must happen before any mutation: %#v", tkeAPI)
	}
}

func TestTencentSDKTagComputeMachineDoesNotFallbackFromCVMToTKE(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1}
	cvmAPI := &fakeNativeCvmAPI{empty: true}
	client := &tencentSDKClient{clusterId: "cls-123", nativeTkeClient: tkeAPI, nativeCvmClient: cvmAPI}

	response := client.TagComputeMachine(Request{
		Tags:       computeOwnershipTags(),
		Pool:       ComputePoolInput{NodePoolId: "np-basic"},
		Allocation: ComputeAllocationInput{InstanceId: "ins-alpha", PrivateIp: "10.0.0.11"},
	}, nil)

	if response.Ok || response.ErrorCode != "compute_machine_identity_unverified" || len(tkeAPI.describeInstancesRequest) != 0 || len(cvmAPI.modifyInstancesRequest) != 0 {
		t.Fatalf("CVM-to-TKE fallback response=%#v TKE requests=%#v modify requests=%#v", response, tkeAPI.describeInstancesRequest, cvmAPI.modifyInstancesRequest)
	}
}

func TestTencentSDKTagComputeMachineRejectsUnknownIdentitySource(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1}
	cvmAPI := &fakeNativeCvmAPI{}
	client := &tencentSDKClient{clusterId: "cls-123", nativeTkeClient: tkeAPI, nativeCvmClient: cvmAPI}

	response := client.TagComputeMachine(Request{
		Tags:       computeOwnershipTags(),
		Allocation: ComputeAllocationInput{InstanceId: "machine-alpha"},
	}, nil)

	if response.Ok || response.ErrorCode != "compute_machine_identity_unverified" || len(tkeAPI.describeInstancesRequest) != 0 || len(cvmAPI.describeInstancesRequest) != 0 || len(cvmAPI.modifyInstancesRequest) != 0 {
		t.Fatalf("unknown identity response=%#v TKE requests=%#v CVM requests=%#v modify requests=%#v", response, tkeAPI.describeInstancesRequest, cvmAPI.describeInstancesRequest, cvmAPI.modifyInstancesRequest)
	}
}

func TestTencentSDKClientRejectsRegularTKEIdentityWhenCVMIsAbsent(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1, nodeType: "Regular"}
	client := newFakeTencentSDKClient(tkeAPI)
	client.nativeCvmClient = &fakeNativeCvmAPI{empty: true}

	reconcile := client.ReconcileComputePool(Request{
		PackageId: "basic",
		Pool:      ComputePoolInput{Id: "pool-basic-2c4g", InstanceType: "SA5.LARGE4", NodePoolId: "np-basic", DesiredReplicas: 1},
	}, map[string]string{})
	if reconcile.Ok || reconcile.ErrorCode != "compute_cvm_identity_required" {
		t.Fatalf("regular machine without CVM identity = %#v", reconcile)
	}

	tagged := client.TagComputeMachine(Request{
		Tags:       computeOwnershipTags(),
		Pool:       ComputePoolInput{NodePoolId: "np-basic"},
		Allocation: ComputeAllocationInput{InstanceId: "np-native-1", MachineName: "node-basic-1", NodeName: "10.0.0.11", PrivateIp: "10.0.0.11"},
	}, nil)
	if tagged.Ok || tagged.ErrorCode != "compute_machine_identity_unverified" {
		t.Fatalf("regular machine tag = %#v", tagged)
	}
}

func TestTencentSDKClientDoesNotTreatCVMAPIErrorAsNativeIdentity(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1}
	client := newFakeTencentSDKClient(tkeAPI)
	client.nativeCvmClient = &fakeNativeCvmAPI{err: errors.New("cvm unavailable")}

	reconcile := client.ReconcileComputePool(Request{
		PackageId: "basic",
		Pool:      ComputePoolInput{Id: "pool-basic-2c4g", InstanceType: "SA5.LARGE4", NodePoolId: "np-basic", DesiredReplicas: 1},
	}, map[string]string{})
	if reconcile.Ok || reconcile.ErrorCode != "tencent_describe_cvm_instance_failed" {
		t.Fatalf("reconcile after CVM API error = %#v", reconcile)
	}

	created := client.CreateComputeAllocation(Request{
		AccountId: "acct-alpha", PackageId: "basic",
		Pool:       ComputePoolInput{Id: "pool-basic-2c4g", InstanceType: "SA5.LARGE4", NodePoolId: "np-basic"},
		Allocation: ComputeAllocationInput{Id: "compute-alpha"},
	}, map[string]string{})
	if created.Ok || created.ErrorCode != "tencent_describe_cvm_instance_failed" {
		t.Fatalf("create after CVM API error = %#v", created)
	}
}

func TestTencentSDKTagComputeMachineRequiresExactNativeNodePoolIdentity(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1, omitInstanceNodePool: true}
	client := newFakeTencentSDKClient(tkeAPI)
	client.nativeCvmClient = &fakeNativeCvmAPI{empty: true}

	response := client.TagComputeMachine(Request{
		Tags:       computeOwnershipTags(),
		Pool:       ComputePoolInput{NodePoolId: "np-basic"},
		Allocation: ComputeAllocationInput{InstanceId: "np-native-1", MachineName: "node-basic-1", NodeName: "10.0.0.11", PrivateIp: "10.0.0.11"},
	}, nil)
	if response.Ok || response.ErrorCode != "compute_machine_identity_unverified" {
		t.Fatalf("native tag without exact pool identity = %#v", response)
	}

	response = client.TagComputeMachine(Request{
		Tags:       computeOwnershipTags(),
		Allocation: ComputeAllocationInput{InstanceId: "np-native-1", MachineName: "node-basic-1", NodeName: "10.0.0.11", PrivateIp: "10.0.0.11"},
	}, nil)
	if response.Ok || response.ErrorCode != "compute_machine_identity_unverified" {
		t.Fatalf("native tag without requested pool identity = %#v", response)
	}
}

func TestTencentSDKClientRejectsMalformedCVMResponses(t *testing.T) {
	for _, test := range []struct {
		name string
		api  *fakeNativeCvmAPI
	}{
		{name: "nil response", api: &fakeNativeCvmAPI{nilResponse: true}},
		{name: "nil envelope", api: &fakeNativeCvmAPI{nilEnvelope: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1}
			client := newFakeTencentSDKClient(tkeAPI)
			client.nativeCvmClient = test.api

			reconcile := client.ReconcileComputePool(Request{
				PackageId: "basic",
				Pool:      ComputePoolInput{Id: "pool-basic-2c4g", InstanceType: "SA5.LARGE4", NodePoolId: "np-basic", DesiredReplicas: 1},
			}, map[string]string{})
			if reconcile.Ok || reconcile.ErrorCode != "tencent_describe_cvm_instance_failed" {
				t.Fatalf("reconcile malformed CVM response = %#v", reconcile)
			}

			tagged := client.TagComputeMachine(Request{
				Tags:       computeOwnershipTags(),
				Allocation: ComputeAllocationInput{InstanceId: "ins-alpha"},
			}, nil)
			if tagged.Ok || tagged.ErrorCode != "tencent_verify_compute_machine_failed" {
				t.Fatalf("tag malformed CVM response = %#v", tagged)
			}
		})
	}
}

func TestTencentSDKTagComputeMachineRejectsMalformedCVMReadback(t *testing.T) {
	for _, test := range []struct {
		name string
		api  *fakeNativeCvmAPI
	}{
		{name: "nil response", api: &fakeNativeCvmAPI{nilResponseCall: 2}},
		{name: "nil envelope", api: &fakeNativeCvmAPI{nilEnvelopeCall: 2}},
		{name: "nil instance", api: &fakeNativeCvmAPI{nilInstanceCall: 2}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &tencentSDKClient{nativeCvmClient: test.api, nativeTagClient: &fakeNativeTagAPI{cvm: test.api}}
			response := client.TagComputeMachine(Request{
				Tags:       computeOwnershipTags(),
				Allocation: ComputeAllocationInput{InstanceId: "ins-alpha"},
			}, nil)
			if response.Ok || response.ErrorCode != "tencent_verify_compute_machine_tag_failed" {
				t.Fatalf("malformed CVM readback = %#v", response)
			}
		})
	}
}

func TestTencentSDKClientReconcileEvidence(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", labelPoolId: "basic"}
	client := newFakeTencentSDKClient(tkeAPI)
	client.nativeCvmClient = &fakeNativeCvmAPI{expiredTime: "2026-08-16T08:00:00+08:00"}

	response := client.ReconcileComputePool(Request{
		PackageId: "basic",
		Pool:      ComputePoolInput{Id: "basic", InstanceType: "SA5.LARGE4", NodePoolId: "np-basic", DesiredReplicas: 1},
	}, map[string]string{})

	want := map[string]string{
		"nodePoolId":                "np-basic",
		"currentReplicas":           "1",
		"desiredReplicas":           "1",
		"scaleNodePoolRequestId":    "req-scale-pool",
		"describeMachinesRequestId": "req-describe-machines",
		"machineStates":             "node-basic-1=running",
	}
	if !response.Ok {
		t.Fatalf("reconcile failed: %#v", response)
	}
	for key, value := range want {
		if response.ProviderData[key] != value {
			t.Fatalf("providerData[%q] = %q, want %q: %#v", key, response.ProviderData[key], value, response.ProviderData)
		}
	}
	if len(response.Machines) != 1 || response.Machines[0].Deadline != "2026-08-16T00:00:00Z" {
		t.Fatalf("reconcile deadline was not normalized: %#v", response.Machines)
	}
}

func TestTencentSDKClientCreateAllocationDiscoversExistingPackageNodePool(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{discoverNodePoolId: "np-discovered", replicas: 2, labelPoolId: "basic"}
	client := newFakeTencentSDKClient(tkeAPI)

	response := client.CreateComputeAllocation(Request{
		AccountId: "pi-alpha",
		UserId:    "usr-alpha",
		PackageId: "basic",
		Pool: ComputePoolInput{
			Id:           "basic",
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

func TestTencentSDKClientMutationRejectsStaleConfiguredNodePoolWithoutMutation(t *testing.T) {
	for _, action := range []string{"create", "reconcile"} {
		t.Run(action, func(t *testing.T) {
			tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-live", discoverNodePoolId: "np-live", replicas: 4}
			client := newFakeTencentSDKClient(tkeAPI)
			request := Request{AccountId: "pi-alpha", UserId: "usr-alpha", PackageId: "basic", Pool: ComputePoolInput{
				Id: "pool-basic-2c4g", InstanceType: "SA5.LARGE4", NodePoolId: "np-stale", DesiredReplicas: 4,
			}}
			var response Response
			if action == "create" {
				request.Allocation = ComputeAllocationInput{Id: "compute-alpha"}
				response = client.CreateComputeAllocation(request, map[string]string{})
			} else {
				response = client.ReconcileComputePool(request, map[string]string{})
			}
			if response.Ok {
				t.Fatalf("stale explicit node pool must fail closed: %#v", response)
			}
			if tkeAPI.createNodePoolRequest != nil || tkeAPI.modifyNodePoolRequest != nil || tkeAPI.scaleNodePoolRequest != nil {
				t.Fatalf("stale explicit node pool must not mutate another pool: %#v", tkeAPI)
			}
			if len(tkeAPI.calls) != 1 || tkeAPI.calls[0] != "DescribeNodePools" {
				t.Fatalf("stale explicit node pool must not fall back to discovery: %#v", tkeAPI.calls)
			}
		})
	}
}

func TestTencentSDKClientMutationRejectsConflictingPoolReadbackWithoutMutation(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		configure func(*fakeNativeTkeAPI)
	}{
		{name: "wrong instance type", configure: func(api *fakeNativeTkeAPI) { api.instanceTypes = []string{"SA5.2XLARGE16"} }},
		{name: "autoscaling enabled", configure: func(api *fakeNativeTkeAPI) { api.enableAutoscaling = true }},
		{name: "auto repair enabled", configure: func(api *fakeNativeTkeAPI) { api.autoRepair = true }},
		{name: "scaling missing", configure: func(api *fakeNativeTkeAPI) { api.omitScaling = true }},
		{name: "max replicas insufficient", configure: func(api *fakeNativeTkeAPI) { api.maxReplicas = 1 }},
		{name: "pool stopped", configure: func(api *fakeNativeTkeAPI) { api.lifeState = "Stopped" }},
		{name: "pool is postpaid", configure: func(api *fakeNativeTkeAPI) { api.instanceChargeType = "POSTPAID_BY_HOUR" }},
		{name: "prepaid config missing", configure: func(api *fakeNativeTkeAPI) { api.omitInstanceChargePrepaid = true }},
		{name: "prepaid period missing", configure: func(api *fakeNativeTkeAPI) { api.omitPrepaidPeriod = true }},
		{name: "prepaid auto renew", configure: func(api *fakeNativeTkeAPI) { api.prepaidRenewFlag = "NOTIFY_AND_AUTO_RENEW" }},
	} {
		for _, action := range []string{"create", "reconcile"} {
			t.Run(testCase.name+"/"+action, func(t *testing.T) {
				tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1, maxReplicas: 10}
				testCase.configure(tkeAPI)
				client := newFakeTencentSDKClient(tkeAPI)
				request := Request{PackageId: "basic", Pool: ComputePoolInput{
					Id: "pool-basic-2c4g", NodePoolId: "np-basic", InstanceType: "SA5.LARGE4", DesiredReplicas: 2,
				}}
				var response Response
				if action == "create" {
					request.Allocation = ComputeAllocationInput{Id: "compute-alpha"}
					response = client.CreateComputeAllocation(request, map[string]string{})
				} else {
					response = client.ReconcileComputePool(request, map[string]string{})
				}
				if response.Ok || tkeAPI.modifyNodePoolRequest != nil || tkeAPI.scaleNodePoolRequest != nil {
					t.Fatalf("conflicting pool readback must fail before mutation: response=%#v calls=%#v", response, tkeAPI.calls)
				}
			})
		}
	}
}

func TestTencentSDKClientMutationWaitsForCreatingPoolWithoutMutation(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 0, lifeState: "Creating"}
	client := newFakeTencentSDKClient(tkeAPI)
	response := client.ReconcileComputePool(Request{PackageId: "basic", Pool: ComputePoolInput{
		Id: "pool-basic-2c4g", NodePoolId: "np-basic", InstanceType: "SA5.LARGE4", DesiredReplicas: 1,
	}}, map[string]string{})
	if response.Ok || !response.Retryable || response.ErrorCode != "tencent_node_pool_not_ready" {
		t.Fatalf("creating pool must return retryable not-ready: %#v", response)
	}
	if tkeAPI.modifyNodePoolRequest != nil || tkeAPI.scaleNodePoolRequest != nil {
		t.Fatalf("creating pool must not mutate: %#v", tkeAPI.calls)
	}
}

func TestTencentSDKClientMutationDiscoversCreatingPoolWithoutCreatingDuplicate(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{discoverNodePoolId: "np-basic", replicas: 0, lifeState: "Creating"}
	client := newFakeTencentSDKClient(tkeAPI)
	response := client.ReconcileComputePool(Request{PackageId: "basic", Pool: ComputePoolInput{
		Id: "pool-basic-2c4g", InstanceType: "SA5.LARGE4", DesiredReplicas: 1,
	}}, map[string]string{})
	if response.Ok || !response.Retryable || response.ErrorCode != "tencent_node_pool_not_ready" {
		t.Fatalf("discovered creating pool must return retryable not-ready: %#v", response)
	}
	if tkeAPI.createNodePoolRequest != nil || tkeAPI.modifyNodePoolRequest != nil || tkeAPI.scaleNodePoolRequest != nil {
		t.Fatalf("discovered creating pool must not create a duplicate or mutate: %#v", tkeAPI.calls)
	}
}

func TestTencentSDKClientMutationRejectsUnownedNodePoolWithoutMutation(t *testing.T) {
	cases := []struct {
		name string
		tke  *fakeNativeTkeAPI
	}{
		{name: "wrong pool label", tke: &fakeNativeTkeAPI{nodePoolId: "np-basic", labelPoolId: "pool-other"}},
		{name: "wrong package label", tke: &fakeNativeTkeAPI{nodePoolId: "np-basic", labelPackageId: "pro"}},
		{name: "wrong instance type label", tke: &fakeNativeTkeAPI{nodePoolId: "np-basic", labelInstanceType: "SA5.2XLARGE16"}},
		{name: "managed pool", tke: &fakeNativeTkeAPI{nodePoolId: "np-basic", poolType: "Managed"}},
		{name: "legacy CXM native pool", tke: &fakeNativeTkeAPI{nodePoolId: "np-basic", machineType: "Native"}},
		{name: "postpaid pool", tke: &fakeNativeTkeAPI{nodePoolId: "np-basic", instanceChargeType: "POSTPAID_BY_HOUR"}},
	}
	for _, tc := range cases {
		for _, action := range []string{"create", "reconcile"} {
			t.Run(tc.name+" "+action, func(t *testing.T) {
				copy := *tc.tke
				client := newFakeTencentSDKClient(&copy)
				request := Request{PackageId: "basic", Pool: ComputePoolInput{
					Id: "pool-basic-2c4g", InstanceType: "SA5.LARGE4", NodePoolId: "np-basic", DesiredReplicas: 1,
				}}
				var response Response
				if action == "create" {
					request.Allocation = ComputeAllocationInput{Id: "compute-alpha"}
					response = client.CreateComputeAllocation(request, map[string]string{})
				} else {
					response = client.ReconcileComputePool(request, map[string]string{})
				}
				if response.Ok {
					t.Fatalf("unowned node pool must fail closed: %#v", response)
				}
				if copy.createNodePoolRequest != nil || copy.modifyNodePoolRequest != nil || copy.scaleNodePoolRequest != nil {
					t.Fatalf("unowned node pool must not be mutated: %#v", &copy)
				}
			})
		}
	}
}

func TestTencentSDKClientMutationDiscoveryRejectsDuplicateOrTruncatedResultsWithoutMutation(t *testing.T) {
	for _, tc := range []struct {
		name string
		tke  *fakeNativeTkeAPI
	}{
		{name: "duplicate", tke: &fakeNativeTkeAPI{discoverNodePoolId: "np-basic", ambiguousDiscovery: true}},
		{name: "truncated", tke: &fakeNativeTkeAPI{discoverNodePoolId: "np-basic", truncatedDiscovery: true}},
	} {
		for _, action := range []string{"create", "reconcile"} {
			t.Run(tc.name+" "+action, func(t *testing.T) {
				copy := *tc.tke
				client := newFakeTencentSDKClient(&copy)
				request := Request{PackageId: "basic", Pool: ComputePoolInput{
					Id: "pool-basic-2c4g", InstanceType: "SA5.LARGE4", DesiredReplicas: 1,
				}}
				var response Response
				if action == "create" {
					request.Allocation = ComputeAllocationInput{Id: "compute-alpha"}
					response = client.CreateComputeAllocation(request, map[string]string{})
				} else {
					response = client.ReconcileComputePool(request, map[string]string{})
				}
				if response.Ok {
					t.Fatalf("incomplete discovery must fail closed: %#v", response)
				}
				if copy.createNodePoolRequest != nil || copy.modifyNodePoolRequest != nil || copy.scaleNodePoolRequest != nil {
					t.Fatalf("incomplete discovery must not mutate: %#v", &copy)
				}
			})
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
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 2}
	client := newFakeTencentSDKClient(tkeAPI)

	response := client.DestroyComputeAllocation(Request{
		AccountId: "pi-alpha",
		Pool:      ComputePoolInput{Id: "pool-basic-2c4g", NodePoolId: "np-basic"},
		Allocation: ComputeAllocationInput{
			Id:          "compute-alpha",
			InstanceId:  "ins-basic-2",
			NodeName:    "10.0.0.12",
			MachineName: "node-basic-2",
			PrivateIp:   "10.0.0.12",
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

func TestTencentSDKClientDestroyValidatesMachineOwnershipBeforeMutation(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		configure  func(*fakeNativeTkeAPI)
		allocation ComputeAllocationInput
	}{
		{name: "duplicate machine name", configure: func(api *fakeNativeTkeAPI) { api.duplicateMachineName = true }, allocation: ComputeAllocationInput{MachineName: "node-basic-1", NodeName: "10.0.0.11", PrivateIp: "10.0.0.11", InstanceId: "ins-basic-1"}},
		{name: "node name mismatch", allocation: ComputeAllocationInput{MachineName: "node-basic-1", NodeName: "wrong-node", PrivateIp: "10.0.0.11", InstanceId: "ins-basic-1"}},
		{name: "private IP mismatch", allocation: ComputeAllocationInput{MachineName: "node-basic-1", NodeName: "10.0.0.11", PrivateIp: "10.0.0.99", InstanceId: "ins-basic-1"}},
		{name: "CVM instance mismatch", allocation: ComputeAllocationInput{MachineName: "node-basic-1", NodeName: "10.0.0.11", PrivateIp: "10.0.0.11", InstanceId: "ins-other"}},
		{name: "unknown machine provider", configure: func(api *fakeNativeTkeAPI) { api.machineType = "Unknown" }, allocation: ComputeAllocationInput{MachineName: "node-basic-1", NodeName: "10.0.0.11", PrivateIp: "10.0.0.11", InstanceId: "ins-basic-1"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1, enableAutoscaling: true, autoRepair: true}
			if testCase.configure != nil {
				testCase.configure(tkeAPI)
			}
			client := newFakeTencentSDKClient(tkeAPI)
			response := client.DestroyComputeAllocation(Request{
				Pool:       ComputePoolInput{NodePoolId: "np-basic"},
				Allocation: testCase.allocation,
			}, map[string]string{"TENCENT_TKE_NODE_DELETE_ATTEMPTS": "1"})
			if response.Ok || response.ErrorCode != "compute_machine_identity_unverified" {
				t.Fatalf("destroy must fail closed on an unverified machine triple: %#v", response)
			}
			if tkeAPI.modifyNodePoolRequest != nil || tkeAPI.deleteMachinesRequest != nil {
				t.Fatalf("identity failure must not mutate the node pool: %#v", tkeAPI.calls)
			}
		})
	}
}

func TestTencentSDKClientDestroyValidatesNativeCVMBeforePoolMutation(t *testing.T) {
	callOrder := []string{}
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1, enableAutoscaling: true, autoRepair: true, callLog: &callOrder}
	cvmAPI := &fakeNativeCvmAPI{callLog: &callOrder}
	client := &tencentSDKClient{region: "ap-guangzhou", clusterId: "cls-123", nativeTkeClient: tkeAPI, nativeCvmClient: cvmAPI}

	response := client.DestroyComputeAllocation(Request{
		Pool: ComputePoolInput{NodePoolId: "np-basic"},
		Allocation: ComputeAllocationInput{
			MachineName: "node-basic-1", NodeName: "10.0.0.11", PrivateIp: "10.0.0.11", InstanceId: "ins-basic-1",
		},
	}, map[string]string{"TENCENT_TKE_NODE_DELETE_ATTEMPTS": "1", "TENCENT_TKE_NODE_DELETE_DELAY_MS": "0"})
	if !response.Ok {
		t.Fatalf("expected verified destroy: %#v", response)
	}
	expected := []string{"DescribeNodePools", "DescribeClusterMachines", "DescribeClusterMachines", "DescribeCVMInstances", "ModifyNodePool", "DeleteClusterMachines", "DescribeClusterMachines"}
	if !reflect.DeepEqual(callOrder, expected) {
		t.Fatalf("identity reads must precede every mutation: %#v", callOrder)
	}
}

func TestTencentSDKClientDestroyValidatesLegacyNativeInstanceBeforeDelete(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1, machineType: "Native"}
	client := newFakeTencentSDKClient(tkeAPI)
	response := client.DestroyComputeAllocation(Request{
		Pool: ComputePoolInput{NodePoolId: "np-basic"},
		Allocation: ComputeAllocationInput{
			MachineName: "node-basic-1", NodeName: "10.0.0.11", PrivateIp: "10.0.0.11", InstanceId: "np-native-1",
		},
	}, map[string]string{"TENCENT_TKE_NODE_DELETE_ATTEMPTS": "1", "TENCENT_TKE_NODE_DELETE_DELAY_MS": "0"})
	if !response.Ok || tkeAPI.deleteMachinesRequest == nil {
		t.Fatalf("legacy Native machine with an exact TKE identity must remain deletable: %#v", response)
	}
}

func TestTencentSDKClientDestroyWaitsForMachineAbsence(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 2, retainDeletedMachines: true}
	client := newFakeTencentSDKClient(tkeAPI)
	response := client.DestroyComputeAllocation(Request{
		Pool:       ComputePoolInput{NodePoolId: "np-basic"},
		Allocation: ComputeAllocationInput{Id: "compute-alpha", MachineName: "node-basic-2", NodeName: "10.0.0.12", PrivateIp: "10.0.0.12", InstanceId: "ins-basic-2"},
	}, map[string]string{"TENCENT_TKE_NODE_DELETE_ATTEMPTS": "1", "TENCENT_TKE_NODE_DELETE_DELAY_MS": "0"})
	if response.Ok || response.ErrorCode != "compute_machine_delete_unverified" {
		t.Fatalf("delete returned before machine absence: %#v", response)
	}
}

func TestTencentSDKClientDestroyAllocationDisablesNodePoolSelfProvisioningBeforeDelete(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 2, enableAutoscaling: true, autoRepair: true}
	client := newFakeTencentSDKClient(tkeAPI)

	response := client.DestroyComputeAllocation(Request{
		AccountId: "pi-alpha",
		Pool:      ComputePoolInput{Id: "pool-basic-2c4g", NodePoolId: "np-basic"},
		Allocation: ComputeAllocationInput{
			Id:          "compute-alpha",
			InstanceId:  "ins-basic-2",
			NodeName:    "10.0.0.12",
			MachineName: "node-basic-2",
			PrivateIp:   "10.0.0.12",
		},
	}, map[string]string{})

	if !response.Ok {
		t.Fatalf("expected ok response: %#v", response)
	}
	if tkeAPI.modifyNodePoolRequest == nil {
		t.Fatalf("expected ModifyNodePool before DeleteClusterMachines")
	}
	if tkeAPI.modifyNodePoolRequest.Native == nil ||
		tkeAPI.modifyNodePoolRequest.Native.EnableAutoscaling == nil ||
		*tkeAPI.modifyNodePoolRequest.Native.EnableAutoscaling ||
		tkeAPI.modifyNodePoolRequest.Native.AutoRepair == nil ||
		*tkeAPI.modifyNodePoolRequest.Native.AutoRepair {
		t.Fatalf("destroy must disable TKE self-provisioning paths before scaledown delete: %#v", tkeAPI.modifyNodePoolRequest)
	}
	expectedCalls := []string{"DescribeNodePools", "DescribeClusterMachines", "DescribeClusterMachines", "ModifyNodePool", "DeleteClusterMachines", "DescribeClusterMachines"}
	if len(tkeAPI.calls) != len(expectedCalls) {
		t.Fatalf("unexpected call order: %#v", tkeAPI.calls)
	}
	for index, expected := range expectedCalls {
		if tkeAPI.calls[index] != expected {
			t.Fatalf("unexpected call order: %#v", tkeAPI.calls)
		}
	}
}

func TestTencentSDKClientDestroyMachineAllocationAlwaysScalesDownAndTerminates(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1}
	client := newFakeTencentSDKClient(tkeAPI)

	response := client.DestroyComputeAllocation(Request{
		AccountId: "pi-alpha",
		Pool:      ComputePoolInput{Id: "pool-basic-2c4g", NodePoolId: "np-basic"},
		Allocation: ComputeAllocationInput{
			Id: "compute-alpha", InstanceId: "ins-basic-1", NodeName: "10.0.0.11",
			PrivateIp: "10.0.0.11", MachineName: "node-basic-1",
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

func TestTencentSDKClientDestroyMachineNameOnlyAllocationFailsClosed(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1}
	client := newFakeTencentSDKClient(tkeAPI)

	response := client.DestroyComputeAllocation(Request{
		AccountId: "pi-alpha",
		Pool:      ComputePoolInput{Id: "pool-basic-2c4g", NodePoolId: "np-basic"},
		Allocation: ComputeAllocationInput{
			Id:          "compute-alpha",
			MachineName: "node-basic-1",
		},
	}, map[string]string{})

	if response.Ok || response.ErrorCode != "compute_machine_identity_unverified" {
		t.Fatalf("machineName-only destroy must fail closed: %#v", response)
	}
	if tkeAPI.modifyNodePoolRequest != nil || tkeAPI.deleteMachinesRequest != nil {
		t.Fatalf("machineName-only destroy must not mutate Tencent resources")
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

func providerTruthRequest() Request {
	return Request{
		Action:          "provider_truth",
		AccountId:       "acct-alpha",
		StorageVolumeId: "disk-storage-alpha",
		Tags: map[string]string{
			"opl_account_id": "acct-alpha", "opl_workspace_id": "ws-alpha", "opl_resource_id": "storage-alpha", "opl_operation_id": "op-storage-alpha",
		},
		ComputeTags: computeOwnershipTags(),
		Storage:     StorageInput{SizeGB: 10, Zone: "ap-guangzhou-3", DiskType: "CLOUD_BSSD"},
		Pool:        ComputePoolInput{ClusterId: "cls-123", NodePoolId: "np-basic", InstanceType: "SA5.LARGE4"},
		Allocation: ComputeAllocationInput{
			Id: "compute-alpha", MachineName: "node-basic-1", InstanceId: "ins-basic-1", NodeName: "10.0.0.11", PrivateIp: "10.0.0.11",
		},
	}
}

func TestTencentSDKProviderTruthRejectsMismatchedCBSOwnership(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*fakeNativeCbsAPI)
	}{
		{name: "name", configure: func(api *fakeNativeCbsAPI) { api.diskName = "storage-other" }},
		{name: "usage", configure: func(api *fakeNativeCbsAPI) { api.diskUsage = "SYSTEM_DISK" }},
		{name: "account", configure: func(api *fakeNativeCbsAPI) { api.tags = map[string]string{"opl_account_id": "acct-other"} }},
		{name: "workspace", configure: func(api *fakeNativeCbsAPI) { api.tags = map[string]string{"opl_workspace_id": "ws-other"} }},
		{name: "resource", configure: func(api *fakeNativeCbsAPI) { api.tags = map[string]string{"opl_resource_id": "storage-other"} }},
		{name: "operation", configure: func(api *fakeNativeCbsAPI) { api.tags = map[string]string{"opl_operation_id": "op-other"} }},
		{name: "type", configure: func(api *fakeNativeCbsAPI) { api.diskType = "CLOUD_SSD" }},
		{name: "size", configure: func(api *fakeNativeCbsAPI) { api.diskSize = 20 }},
		{name: "zone", configure: func(api *fakeNativeCbsAPI) { api.zone = "ap-guangzhou-4" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1, clusterInstanceID: "node-basic-1"}
			cvmAPI := &fakeNativeCvmAPI{instanceName: "compute-alpha"}
			cbsAPI := &fakeNativeCbsAPI{}
			tc.configure(cbsAPI)
			client := newProviderTruthClient(tkeAPI, cvmAPI)
			client.nativeCbsClient = cbsAPI
			response := client.ProviderTruth(providerTruthRequest(), nil)
			if response.Ok || response.ErrorCode != "tencent_provider_truth_cbs_probe_failed" {
				t.Fatalf("mismatched CBS ownership must fail closed: %#v", response)
			}
		})
	}
}

func newProviderTruthClient(tkeAPI *fakeNativeTkeAPI, cvmAPI *fakeNativeCvmAPI) *tencentSDKClient {
	if cvmAPI.tags == nil {
		cvmAPI.tags = computeOwnershipTags()
	}
	return &tencentSDKClient{
		region: "ap-guangzhou", clusterId: "cls-123", nativeTkeClient: tkeAPI,
		nativeCvmClient: cvmAPI, nativeCbsClient: &fakeNativeCbsAPI{},
	}
}

func TestTencentSDKProviderTruthRejectsWrongComputeSKUOwnershipOrZone(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*Request, *fakeNativeTkeAPI, *fakeNativeCvmAPI)
	}{
		{name: "missing expected SKU", configure: func(request *Request, _ *fakeNativeTkeAPI, _ *fakeNativeCvmAPI) { request.Pool.InstanceType = "" }},
		{name: "wrong CVM SKU", configure: func(_ *Request, _ *fakeNativeTkeAPI, cvm *fakeNativeCvmAPI) { cvm.instanceType = "SA5.2XLARGE16" }},
		{name: "wrong TKE SKU", configure: func(_ *Request, tke *fakeNativeTkeAPI, _ *fakeNativeCvmAPI) {
			tke.machineInstanceType = "SA5.2XLARGE16"
		}},
		{name: "cross Zone", configure: func(_ *Request, _ *fakeNativeTkeAPI, cvm *fakeNativeCvmAPI) { cvm.zone = "ap-guangzhou-4" }},
		{name: "wrong account tag", configure: func(_ *Request, _ *fakeNativeTkeAPI, cvm *fakeNativeCvmAPI) {
			cvm.tags = computeOwnershipTags()
			cvm.tags["opl_account_id"] = "acct-other"
		}},
		{name: "missing workspace tag", configure: func(_ *Request, _ *fakeNativeTkeAPI, cvm *fakeNativeCvmAPI) {
			cvm.tags = computeOwnershipTags()
			delete(cvm.tags, "opl_workspace_id")
		}},
		{name: "wrong resource tag", configure: func(_ *Request, _ *fakeNativeTkeAPI, cvm *fakeNativeCvmAPI) {
			cvm.tags = computeOwnershipTags()
			cvm.tags["opl_resource_id"] = "compute-other"
		}},
		{name: "wrong operation tag", configure: func(_ *Request, _ *fakeNativeTkeAPI, cvm *fakeNativeCvmAPI) {
			cvm.tags = computeOwnershipTags()
			cvm.tags["opl_operation_id"] = "owner-other"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := providerTruthRequest()
			tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1, clusterInstanceID: "node-basic-1"}
			cvmAPI := &fakeNativeCvmAPI{instanceName: "compute-alpha", tags: computeOwnershipTags()}
			tc.configure(&request, tkeAPI, cvmAPI)
			response := newProviderTruthClient(tkeAPI, cvmAPI).ProviderTruth(request, nil)
			if response.Ok || response.Status == "present" {
				t.Fatalf("mismatched compute truth must fail closed: %#v", response)
			}
		})
	}
}

func TestTencentSDKProviderTruthProbesExactCBSVolume(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 0}
	cvmAPI := &fakeNativeCvmAPI{empty: true}
	cbsAPI := &fakeNativeCbsAPI{empty: true}
	client := &tencentSDKClient{
		region: "ap-guangzhou", clusterId: "cls-123", nativeTkeClient: tkeAPI,
		nativeCvmClient: cvmAPI, nativeCbsClient: cbsAPI,
	}

	response := client.ProviderTruth(providerTruthRequest(), nil)

	if !response.Ok || response.StoragePresent == nil || *response.StoragePresent || response.CBSStatus != "NOT_FOUND" {
		t.Fatalf("unexpected CBS truth: %#v", response)
	}
	if len(cbsAPI.describeDisksRequests) != 1 || len(cbsAPI.describeDisksRequests[0].DiskIds) != 1 || stringValue(cbsAPI.describeDisksRequests[0].DiskIds[0]) != "disk-storage-alpha" {
		t.Fatalf("CBS truth must query the exact supplied disk: %#v", cbsAPI.describeDisksRequests)
	}
}

func TestTencentSDKProviderTruthTreatsExactCBSNotFoundAsAbsent(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 0}
	cvmAPI := &fakeNativeCvmAPI{empty: true}
	cbsAPI := &fakeNativeCbsAPI{err: tcerrors.NewTencentCloudSDKError(cbs2017.INVALIDDISKID_NOTFOUND, "disk was deleted", "req-cbs-not-found")}
	client := &tencentSDKClient{
		region: "ap-guangzhou", clusterId: "cls-123", nativeTkeClient: tkeAPI,
		nativeCvmClient: cvmAPI, nativeCbsClient: cbsAPI,
	}

	response := client.ProviderTruth(providerTruthRequest(), nil)

	if !response.Ok || response.StoragePresent == nil || *response.StoragePresent || response.CBSStatus != "NOT_FOUND" || response.ProviderRequestId != "req-cbs-not-found" {
		t.Fatalf("unexpected deleted CBS truth: %#v", response)
	}
}

func TestTencentSDKProviderTruthRejectsGenericCBSNotFound(t *testing.T) {
	for _, code := range []string{cbs2017.RESOURCENOTFOUND, cbs2017.RESOURCENOTFOUND_NOTFOUND} {
		t.Run(code, func(t *testing.T) {
			tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 0}
			cvmAPI := &fakeNativeCvmAPI{empty: true}
			cbsAPI := &fakeNativeCbsAPI{err: tcerrors.NewTencentCloudSDKError(code, "ambiguous resource", "req-cbs-generic")}
			client := &tencentSDKClient{
				region: "ap-guangzhou", clusterId: "cls-123", nativeTkeClient: tkeAPI,
				nativeCvmClient: cvmAPI, nativeCbsClient: cbsAPI,
			}

			response := client.ProviderTruth(providerTruthRequest(), nil)

			if response.Ok || response.ErrorCode != "tencent_provider_truth_cbs_probe_failed" {
				t.Fatalf("generic not-found must fail closed: %#v", response)
			}
		})
	}
}

func assertProviderTruthReadOnly(t *testing.T, tkeAPI *fakeNativeTkeAPI, cvmAPI *fakeNativeCvmAPI) {
	t.Helper()
	if tkeAPI.createNodePoolRequest != nil || tkeAPI.modifyNodePoolRequest != nil || tkeAPI.scaleNodePoolRequest != nil || tkeAPI.deleteMachinesRequest != nil || len(cvmAPI.modifyInstancesRequest) != 0 {
		t.Fatalf("provider truth must not mutate Tencent resources: tke=%#v cvm=%#v", tkeAPI.calls, cvmAPI.modifyInstancesRequest)
	}
}

func TestTencentSDKProviderTruthReturnsExactPresentIdentityWithoutMutation(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1, clusterInstanceID: "node-basic-1"}
	cvmAPI := &fakeNativeCvmAPI{instanceName: "compute-alpha", expiredTime: "2026-08-16T08:00:00+08:00"}
	cbsAPI := &fakeNativeCbsAPI{}
	client := newProviderTruthClient(tkeAPI, cvmAPI)
	client.nativeCbsClient = cbsAPI

	response := client.ProviderTruth(providerTruthRequest(), nil)

	if !response.Ok || response.Status != "present" || response.MachineType != "NativeCVM" || response.InstanceId != "ins-basic-1" || response.NodeName != "" || response.PrivateIp != "10.0.0.11" || response.MachinePresent == nil || !*response.MachinePresent || response.CVMStatus != "RUNNING" || response.TKEStatus != "RUNNING" {
		t.Fatalf("unexpected present truth: %#v", response)
	}
	if response.ProviderData["accountId"] != "" || response.ProviderData["requestedAccountId"] != "" || response.ProviderData["resourceId"] != "compute-alpha" || response.ProviderData["machineName"] != "node-basic-1" {
		t.Fatalf("present truth lost exact identity: %#v", response.ProviderData)
	}
	if response.StoragePresent == nil || !*response.StoragePresent || response.CBSStatus != "ATTACHED" || response.ProviderData["storagePresent"] != "true" || response.ProviderData["cbsStatus"] != "ATTACHED" {
		t.Fatalf("present truth lost exact CBS state: %#v", response)
	}
	if response.ProviderData["chargeType"] != "PREPAID" || response.ProviderData["renewFlag"] != "NOTIFY_AND_MANUAL_RENEW" || response.ProviderData["deadline"] != "2026-08-16T00:00:00Z" || response.ProviderData["zone"] != "ap-guangzhou-3" || response.ProviderData["storageDeadline"] != "2026-08-16T00:00:00Z" {
		t.Fatalf("provider truth lost exact CVM billing facts: %#v", response.ProviderData)
	}
	if len(cbsAPI.describeDisksRequests) != 1 || len(cbsAPI.describeDisksRequests[0].DiskIds) != 1 || stringValue(cbsAPI.describeDisksRequests[0].DiskIds[0]) != "disk-storage-alpha" {
		t.Fatalf("CBS truth must query the exact supplied disk: %#v", cbsAPI.describeDisksRequests)
	}
	if want := []string{"DescribeNodePools", "DescribeClusterMachines", "DescribeClusterInstances"}; !reflect.DeepEqual(tkeAPI.calls, want) {
		t.Fatalf("unexpected read path: got=%#v want=%#v", tkeAPI.calls, want)
	}
	if len(cvmAPI.describeInstancesRequest) != 1 || len(cvmAPI.describeInstancesRequest[0].InstanceIds) != 1 || stringValue(cvmAPI.describeInstancesRequest[0].InstanceIds[0]) != "ins-basic-1" {
		t.Fatalf("CVM truth must query the exact supplied instance ID: %#v", cvmAPI.describeInstancesRequest)
	}
	assertProviderTruthReadOnly(t, tkeAPI, cvmAPI)
}

func TestTencentSDKProviderTruthReturnsAbsentOnlyWhenEveryExactIdentityIsAbsent(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 0}
	cvmAPI := &fakeNativeCvmAPI{empty: true}
	client := newProviderTruthClient(tkeAPI, cvmAPI)
	client.nativeCbsClient = &fakeNativeCbsAPI{empty: true}

	response := client.ProviderTruth(providerTruthRequest(), nil)

	if !response.Ok || response.Status != "absent" || response.MachinePresent == nil || *response.MachinePresent || response.CVMStatus != "NOT_FOUND" || response.TKEStatus != "NOT_FOUND" || response.NodeName != "" || response.PrivateIp != "10.0.0.11" {
		t.Fatalf("unexpected absent truth: %#v", response)
	}
	assertProviderTruthDescribeOnly(t, tkeAPI.calls)
	assertProviderTruthReadOnly(t, tkeAPI, cvmAPI)
}

func TestTencentSDKProviderTruthDoesNotReturnAbsentWhileCBSExists(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 0}
	cvmAPI := &fakeNativeCvmAPI{empty: true}
	client := newProviderTruthClient(tkeAPI, cvmAPI)

	response := client.ProviderTruth(providerTruthRequest(), nil)

	if response.Ok || response.Status == "absent" || response.ProviderData["storagePresent"] != "true" || response.ErrorCode != "provider_truth_partial_identity" {
		t.Fatalf("attached CBS must prevent confirmed absence: %#v", response)
	}
}

func TestTencentSDKProviderTruthRejectsAccountOwnershipMismatch(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1, clusterInstanceID: "node-basic-1"}
	cvmAPI := &fakeNativeCvmAPI{instanceName: "compute-alpha"}
	client := newProviderTruthClient(tkeAPI, cvmAPI)
	request := providerTruthRequest()
	request.AccountId = "acct-other"

	response := client.ProviderTruth(request, nil)

	if response.Ok || response.ErrorCode != "provider_truth_cbs_ownership_required" {
		t.Fatalf("Tencent truth accepted the wrong account owner: %#v", response)
	}
	assertProviderTruthReadOnly(t, tkeAPI, cvmAPI)
}

func TestTencentSDKProviderTruthDoesNotRequireOrVerifyKubernetesNodeName(t *testing.T) {
	tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1, clusterInstanceID: "node-basic-1"}
	cvmAPI := &fakeNativeCvmAPI{instanceName: "compute-alpha"}
	client := newProviderTruthClient(tkeAPI, cvmAPI)
	request := providerTruthRequest()
	request.Allocation.NodeName = ""

	response := client.ProviderTruth(request, nil)

	if !response.Ok || response.Status != "present" || response.NodeName != "" {
		t.Fatalf("Tencent truth must leave Kubernetes node verification to kubectl: %#v", response)
	}
	assertProviderTruthReadOnly(t, tkeAPI, cvmAPI)
}

func TestTencentSDKProviderTruthFailsClosedOnMissingOrMismatchedIdentity(t *testing.T) {
	testCases := []struct {
		name      string
		request   func() Request
		configure func(*fakeNativeTkeAPI, *fakeNativeCvmAPI)
	}{
		{name: "missing account", request: func() Request { request := providerTruthRequest(); request.AccountId = ""; return request }},
		{name: "missing resource", request: func() Request { request := providerTruthRequest(); request.Allocation.Id = ""; return request }},
		{name: "missing machine", request: func() Request { request := providerTruthRequest(); request.Allocation.MachineName = ""; return request }},
		{name: "missing instance", request: func() Request { request := providerTruthRequest(); request.Allocation.InstanceId = ""; return request }},
		{name: "non CVM instance", request: func() Request {
			request := providerTruthRequest()
			request.Allocation.InstanceId = "np-native-1"
			return request
		}},
		{name: "missing private IP", request: func() Request { request := providerTruthRequest(); request.Allocation.PrivateIp = ""; return request }},
		{name: "wrong cluster", request: func() Request {
			request := providerTruthRequest()
			request.Pool.ClusterId = "cls-other"
			return request
		}},
		{name: "missing node pool", request: func() Request { request := providerTruthRequest(); request.Pool.NodePoolId = ""; return request }},
		{name: "legacy CXM pool", request: providerTruthRequest, configure: func(tke *fakeNativeTkeAPI, _ *fakeNativeCvmAPI) { tke.machineType = "Native" }},
		{name: "postpaid pool", request: providerTruthRequest, configure: func(tke *fakeNativeTkeAPI, _ *fakeNativeCvmAPI) { tke.instanceChargeType = "POSTPAID_BY_HOUR" }},
		{name: "prepaid config missing", request: providerTruthRequest, configure: func(tke *fakeNativeTkeAPI, _ *fakeNativeCvmAPI) { tke.omitInstanceChargePrepaid = true }},
		{name: "prepaid period missing", request: providerTruthRequest, configure: func(tke *fakeNativeTkeAPI, _ *fakeNativeCvmAPI) { tke.omitPrepaidPeriod = true }},
		{name: "prepaid auto renew", request: providerTruthRequest, configure: func(tke *fakeNativeTkeAPI, _ *fakeNativeCvmAPI) { tke.prepaidRenewFlag = "NOTIFY_AND_AUTO_RENEW" }},
		{name: "CVM postpaid", request: providerTruthRequest, configure: func(_ *fakeNativeTkeAPI, cvm *fakeNativeCvmAPI) { cvm.instanceChargeType = "POSTPAID_BY_HOUR" }},
		{name: "CVM auto renew", request: providerTruthRequest, configure: func(_ *fakeNativeTkeAPI, cvm *fakeNativeCvmAPI) { cvm.renewFlag = "NOTIFY_AND_AUTO_RENEW" }},
		{name: "CVM deadline missing", request: providerTruthRequest, configure: func(_ *fakeNativeTkeAPI, cvm *fakeNativeCvmAPI) { cvm.omitExpiredTime = true }},
		{name: "machine mismatch", request: func() Request {
			request := providerTruthRequest()
			request.Allocation.MachineName = "node-other"
			return request
		}},
		{name: "private IP mismatch", request: func() Request {
			request := providerTruthRequest()
			request.Allocation.PrivateIp = "10.0.0.99"
			return request
		}},
		{name: "resource mismatch", request: providerTruthRequest, configure: func(_ *fakeNativeTkeAPI, cvm *fakeNativeCvmAPI) { cvm.instanceName = "compute-other" }},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1, clusterInstanceID: "node-basic-1"}
			cvmAPI := &fakeNativeCvmAPI{instanceName: "compute-alpha"}
			if testCase.configure != nil {
				testCase.configure(tkeAPI, cvmAPI)
			}
			client := newProviderTruthClient(tkeAPI, cvmAPI)
			response := client.ProviderTruth(testCase.request(), nil)
			if response.Ok || response.ErrorCode == "" {
				t.Fatalf("provider truth must fail closed: %#v", response)
			}
			assertProviderTruthReadOnly(t, tkeAPI, cvmAPI)
		})
	}
}

func TestTencentSDKProviderTruthFailsClosedOnPartialAbsenceOrProbeError(t *testing.T) {
	testCases := []struct {
		name      string
		configure func(*fakeNativeTkeAPI, *fakeNativeCvmAPI)
	}{
		{name: "machine absent while CVM remains", configure: func(tke *fakeNativeTkeAPI, _ *fakeNativeCvmAPI) { tke.replicas = 0 }},
		{name: "CVM absent while machine remains", configure: func(_ *fakeNativeTkeAPI, cvm *fakeNativeCvmAPI) { cvm.empty = true }},
		{name: "TKE instance absent while machine remains", configure: func(tke *fakeNativeTkeAPI, _ *fakeNativeCvmAPI) { tke.omitClusterInstances = true }},
		{name: "node pool probe error", configure: func(tke *fakeNativeTkeAPI, _ *fakeNativeCvmAPI) {
			tke.describeNodePoolErr = errors.New("node pool unavailable")
		}},
		{name: "machine probe error", configure: func(tke *fakeNativeTkeAPI, _ *fakeNativeCvmAPI) {
			tke.describeMachineErr = errors.New("machine unavailable")
		}},
		{name: "TKE instance probe error", configure: func(tke *fakeNativeTkeAPI, _ *fakeNativeCvmAPI) {
			tke.describeClusterInstancesErr = errors.New("TKE instance unavailable")
		}},
		{name: "CVM probe error", configure: func(_ *fakeNativeTkeAPI, cvm *fakeNativeCvmAPI) { cvm.err = errors.New("CVM unavailable") }},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			tkeAPI := &fakeNativeTkeAPI{nodePoolId: "np-basic", replicas: 1, clusterInstanceID: "node-basic-1"}
			cvmAPI := &fakeNativeCvmAPI{instanceName: "compute-alpha"}
			testCase.configure(tkeAPI, cvmAPI)
			client := newProviderTruthClient(tkeAPI, cvmAPI)
			response := client.ProviderTruth(providerTruthRequest(), nil)
			if response.Ok || response.ErrorCode == "" {
				t.Fatalf("partial or unknown truth must fail closed: %#v", response)
			}
			assertProviderTruthDescribeOnly(t, tkeAPI.calls)
			assertProviderTruthReadOnly(t, tkeAPI, cvmAPI)
		})
	}
}

func assertProviderTruthDescribeOnly(t *testing.T, calls []string) {
	t.Helper()
	allowed := map[string]bool{"DescribeNodePools": true, "DescribeClusterMachines": true, "DescribeClusterInstances": true}
	for _, call := range calls {
		if !allowed[call] {
			t.Fatalf("provider truth used a non-Describe TKE call: %#v", calls)
		}
	}
}
