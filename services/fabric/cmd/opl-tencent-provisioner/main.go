package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"opl-cloud/services/fabric/internal/protectedresource"

	cbs2017 "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cbs/v20170312"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	tcerrors "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/errors"
	tchttp "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/http"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	cvm2017 "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cvm/v20170312"
	tke2018 "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/tke/v20180525"
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

const tkeListPageLimit int64 = 100
const nodePoolBootstrapMutationConfirmation = "CREATE_MISSING_WORKSPACE_NODEPOOLS"
const protectedCheckNotChecked = "not_checked"
const protectedCheckPassed = "passed"
const protectedCheckFailed = "failed"
const protectedCheckNotApplicable = "not_applicable"

type Request struct {
	Action           string                 `json:"action"`
	DryRun           bool                   `json:"dryRun,omitempty"`
	AccountId        string                 `json:"accountId,omitempty"`
	UserId           string                 `json:"userId,omitempty"`
	PackageId        string                 `json:"packageId,omitempty"`
	Zone             string                 `json:"zone,omitempty"`
	StorageVolumeId  string                 `json:"storageVolumeId,omitempty"`
	RequiredCapacity int64                  `json:"requiredCapacity,omitempty"`
	Tags             map[string]string      `json:"tags,omitempty"`
	ComputeTags      map[string]string      `json:"computeTags,omitempty"`
	Pool             ComputePoolInput       `json:"pool,omitempty"`
	Allocation       ComputeAllocationInput `json:"allocation,omitempty"`
	Storage          StorageInput           `json:"storage,omitempty"`
}

type ComputePoolInput struct {
	Id                 string            `json:"id,omitempty"`
	ClusterId          string            `json:"clusterId,omitempty"`
	PackageId          string            `json:"packageId,omitempty"`
	InstanceType       string            `json:"instanceType,omitempty"`
	CPU                uint64            `json:"cpu,omitempty"`
	MemoryGB           uint64            `json:"memoryGb,omitempty"`
	NodePoolId         string            `json:"nodePoolId,omitempty"`
	DesiredNodeLabels  map[string]string `json:"desiredNodeLabels,omitempty"`
	DesiredReplicas    int64             `json:"desiredReplicas,omitempty"`
	MaxReplicas        int64             `json:"maxReplicas,omitempty"`
	BaselineReplicas   int64             `json:"baselineReplicas,omitempty"`
	TargetReplicas     int64             `json:"targetReplicas,omitempty"`
	BeforeMachineNames []string          `json:"beforeMachineNames,omitempty"`
}

type ComputeAllocationInput struct {
	Id          string `json:"id,omitempty"`
	InstanceId  string `json:"instanceId,omitempty"`
	MachineName string `json:"machineName,omitempty"`
	NodeName    string `json:"nodeName,omitempty"`
	PrivateIp   string `json:"privateIp,omitempty"`
	PublicIp    string `json:"publicIp,omitempty"`
	Deadline    string `json:"deadline,omitempty"`
}

type StorageInput struct {
	Id                         string `json:"id,omitempty"`
	SizeGB                     uint64 `json:"sizeGb,omitempty"`
	Zone                       string `json:"zone,omitempty"`
	DiskType                   string `json:"diskType,omitempty"`
	Deadline                   string `json:"deadline,omitempty"`
	ExpectedState              string `json:"expectedState,omitempty"`
	ExpectedProviderResourceId string `json:"expectedProviderResourceId,omitempty"`
	AllowExistingExactReplay   bool   `json:"allowExistingExactReplay,omitempty"`
}

type Response struct {
	Ok                       bool                      `json:"ok"`
	OperationId              string                    `json:"operationId,omitempty"`
	PoolId                   string                    `json:"poolId,omitempty"`
	NodePoolId               string                    `json:"nodePoolId,omitempty"`
	InstanceId               string                    `json:"instanceId,omitempty"`
	NodeName                 string                    `json:"nodeName,omitempty"`
	PrivateIp                string                    `json:"privateIp,omitempty"`
	MachinePresent           *bool                     `json:"machinePresent,omitempty"`
	StoragePresent           *bool                     `json:"storagePresent,omitempty"`
	CVMStatus                string                    `json:"cvmStatus,omitempty"`
	TKEStatus                string                    `json:"tkeStatus,omitempty"`
	CBSStatus                string                    `json:"cbsStatus,omitempty"`
	StorageVolumeId          string                    `json:"storageVolumeId,omitempty"`
	StorageState             string                    `json:"storageState,omitempty"`
	PublicIp                 string                    `json:"publicIp,omitempty"`
	Status                   string                    `json:"status,omitempty"`
	ProviderRequestId        string                    `json:"providerRequestId,omitempty"`
	ProviderRequestIDs       map[string]string         `json:"providerRequestIds,omitempty"`
	ProviderPriceCNY         float64                   `json:"providerPriceCny,omitempty"`
	ProviderData             map[string]string         `json:"providerData,omitempty"`
	ErrorCode                string                    `json:"errorCode,omitempty"`
	Message                  string                    `json:"message,omitempty"`
	Retryable                bool                      `json:"retryable,omitempty"`
	MissingEnv               []string                  `json:"missingEnv,omitempty"`
	Machines                 []MachineOutput           `json:"machines,omitempty"`
	InstanceType             string                    `json:"instanceType,omitempty"`
	InstanceAvailable        bool                      `json:"instanceAvailable,omitempty"`
	RequiredCapacity         int64                     `json:"requiredCapacity,omitempty"`
	RemainingQuota           uint64                    `json:"remainingQuota,omitempty"`
	CurrentReplicas          int64                     `json:"currentReplicas,omitempty"`
	ReadyReplicas            int64                     `json:"readyReplicas,omitempty"`
	MaxReplicas              int64                     `json:"maxReplicas,omitempty"`
	TargetReplicas           int64                     `json:"targetReplicas,omitempty"`
	MachineType              string                    `json:"machineType,omitempty"`
	Zones                    []string                  `json:"zones,omitempty"`
	PreflightStages          []PreflightStage          `json:"preflightStages,omitempty"`
	NodePools                []NodePoolBootstrapResult `json:"nodePools,omitempty"`
	SKUPackages              []WorkspaceSKUPackage     `json:"skuPackages,omitempty"`
	PrepaidQuotaRemaining    uint64                    `json:"prepaidQuotaRemaining,omitempty"`
	Subnets                  []WorkspaceSubnetFact     `json:"subnets,omitempty"`
	TKEClusterNodeLimit      uint64                    `json:"tkeClusterNodeLimit,omitempty"`
	TKECurrentNodeCount      uint64                    `json:"tkeCurrentNodeCount,omitempty"`
	TKEAvailableNodeCapacity uint64                    `json:"tkeAvailableNodeCapacity,omitempty"`
	TKECapacity              *TKEClusterCapacityFacts  `json:"tkeCapacity,omitempty"`
	ProtectedSystem          ProtectedSystemFacts      `json:"protectedSystem,omitempty"`
	NodePoolInventory        []string                  `json:"nodePoolInventoryBeforeMutation,omitempty"`
	MutationCount            int                       `json:"mutationCount"`
	FailureStage             string                    `json:"failureStage,omitempty"`
	ProviderErrorClass       string                    `json:"providerErrorClass,omitempty"`
	MutationEvidence         *MutationEvidence         `json:"mutationEvidence,omitempty"`
}

type MutationEvidence struct {
	Attempted int      `json:"attempted"`
	Confirmed int      `json:"confirmed"`
	Unknown   int      `json:"unknown"`
	Missing   []string `json:"missing,omitempty"`
}

type WorkspaceSKUCandidate struct {
	InstanceType    string  `json:"instanceType"`
	CPU             uint64  `json:"cpu"`
	MemoryGB        uint64  `json:"memoryGb"`
	MonthlyPriceCNY float64 `json:"monthlyPriceCny"`
	StockStatus     string  `json:"stockStatus"`
	StockCategory   string  `json:"stockCategory,omitempty"`
}

type WorkspaceSKUPackage struct {
	PackageID                  string                  `json:"packageId"`
	CPU                        uint64                  `json:"cpu"`
	MemoryGB                   uint64                  `json:"memoryGb"`
	RecommendedInstanceType    string                  `json:"recommendedInstanceType"`
	RecommendedMonthlyPriceCNY float64                 `json:"recommendedMonthlyPriceCny"`
	Candidates                 []WorkspaceSKUCandidate `json:"candidates"`
}

type WorkspaceSubnetFact struct {
	SubnetID             string `json:"subnetId"`
	VPCID                string `json:"vpcId"`
	Zone                 string `json:"zone"`
	AvailableIPAddresses uint64 `json:"availableIpAddresses"`
}

type TKEClusterCapacityFacts struct {
	ClusterLevel     string                `json:"clusterLevel"`
	CurrentNodeCount uint64                `json:"currentNodeCount"`
	LevelAttributes  []TKEClusterLevelFact `json:"levelAttributes"`
}

type TKEClusterLevelFact struct {
	Name      string  `json:"name"`
	Alias     string  `json:"alias,omitempty"`
	NodeCount *uint64 `json:"nodeCount,omitempty"`
	Enable    *bool   `json:"enable,omitempty"`
}

type ProtectedSystemFacts struct {
	NodePoolID         string `json:"nodePoolId,omitempty"`
	PoolCheckStatus    string `json:"poolCheckStatus"`
	MachineID          string `json:"machineId,omitempty"`
	MachineCheckStatus string `json:"machineCheckStatus"`
	NodeName           string `json:"nodeName,omitempty"`
	NodeCheckStatus    string `json:"nodeCheckStatus"`
	MachineType        string `json:"machineType,omitempty"`
	CVMApplicable      bool   `json:"cvmApplicable"`
	CVMID              string `json:"cvmId,omitempty"`
	CVMCheckStatus     string `json:"cvmCheckStatus"`
}

type NodePoolBootstrapResult struct {
	PackageID                 string `json:"packageId"`
	PoolID                    string `json:"poolId"`
	NodePoolID                string `json:"nodePoolId,omitempty"`
	InstanceType              string `json:"instanceType"`
	CPU                       uint64 `json:"cpu"`
	MemoryGB                  uint64 `json:"memoryGb"`
	MaxReplicas               int64  `json:"maxReplicas"`
	MaxReplicasSource         string `json:"maxReplicasSource"`
	MaxReplicasDecision       string `json:"maxReplicasDecision"`
	MaxReplicasConstraint     string `json:"maxReplicasConstraint"`
	MaxReplicasRecommendation string `json:"maxReplicasRecommendation"`
	Status                    string `json:"status"`
	ErrorCode                 string `json:"errorCode,omitempty"`
}

type PreflightStage struct {
	Stage      string         `json:"stage"`
	Status     string         `json:"status"`
	ErrorCode  string         `json:"errorCode,omitempty"`
	BlockedBy  []string       `json:"blockedBy"`
	DurationMS int64          `json:"durationMs"`
	SafeFacts  map[string]any `json:"safeFacts"`
}

type MachineOutput struct {
	MachineId    string `json:"machineId"`
	InstanceId   string `json:"instanceId,omitempty"`
	NodeName     string `json:"nodeName,omitempty"`
	PrivateIp    string `json:"privateIp,omitempty"`
	PublicIp     string `json:"publicIp,omitempty"`
	InstanceType string `json:"instanceType,omitempty"`
	Zone         string `json:"zone,omitempty"`
	ChargeType   string `json:"chargeType,omitempty"`
	RenewFlag    string `json:"renewFlag,omitempty"`
	Deadline     string `json:"deadline,omitempty"`
	Ready        bool   `json:"ready"`
}

type TencentClient interface {
	WorkspaceSKUInventory(request Request, env map[string]string) Response
	Capacity(request Request, env map[string]string) Response
	StoragePreflight(request Request, env map[string]string) Response
	ProviderTruth(request Request, env map[string]string) Response
	ComputeClaimTruth(request Request, env map[string]string) Response
	ClaimComputeMachine(request Request, env map[string]string) Response
	PrepareComputeAllocation(request Request, env map[string]string) Response
	CreateComputeAllocation(request Request, env map[string]string) Response
	TagComputeMachine(request Request, env map[string]string) Response
	SyncComputeAllocation(request Request, env map[string]string) Response
	DestroyComputeAllocation(request Request, env map[string]string) Response
	RenewComputeAllocation(request Request, env map[string]string) Response
	DiscoverStorageVolume(request Request, env map[string]string) Response
	CreateStorageVolume(request Request, env map[string]string) Response
	SyncStorageVolume(request Request, env map[string]string) Response
	RenewStorageVolume(request Request, env map[string]string) Response
	BootstrapComputeNodePools(request Request, env map[string]string) Response
}

type unimplementedTencentClient struct{}

type tencentSDKClient struct {
	region                string
	clusterId             string
	nativeTkeClient       tkeNativeAPI
	nativeLegacyTkeClient tkeLegacyAPI
	nativeCvmClient       cvmNativeAPI
	nativeCbsClient       cbsNativeAPI
	nativeVpcClient       vpcNativeAPI
	nativeTagClient       tagNativeAPI
	convergenceContext    context.Context
	convergenceWait       func(context.Context, int) error
}

type tagNativeAPI interface {
	SetCVMTag(instanceID, key, value string, attached bool) (string, error)
}

type tencentTagClient struct {
	client *common.Client
	region string
}

type tagResourceRequest struct {
	*tchttp.BaseRequest
	ServiceType    *string   `json:"ServiceType,omitempty" name:"ServiceType"`
	ResourceIds    []*string `json:"ResourceIds,omitempty" name:"ResourceIds"`
	TagKey         *string   `json:"TagKey,omitempty" name:"TagKey"`
	TagValue       *string   `json:"TagValue,omitempty" name:"TagValue"`
	ResourceRegion *string   `json:"ResourceRegion,omitempty" name:"ResourceRegion"`
	ResourcePrefix *string   `json:"ResourcePrefix,omitempty" name:"ResourcePrefix"`
}

type tagResourceResponse struct {
	*tchttp.BaseResponse
	Response *struct {
		RequestId *string `json:"RequestId,omitempty" name:"RequestId"`
	} `json:"Response"`
}

func (client *tencentTagClient) SetCVMTag(instanceID, key, value string, attached bool) (string, error) {
	action := "AttachResourcesTag"
	if attached {
		action = "ModifyResourcesTagValue"
	}
	request := &tagResourceRequest{
		BaseRequest: &tchttp.BaseRequest{}, ServiceType: common.StringPtr("cvm"), ResourceIds: []*string{common.StringPtr(instanceID)},
		TagKey: common.StringPtr(key), TagValue: common.StringPtr(value), ResourceRegion: common.StringPtr(client.region), ResourcePrefix: common.StringPtr("instance"),
	}
	request.Init().WithApiInfo("tag", "2018-08-13", action)
	response := &tagResourceResponse{BaseResponse: &tchttp.BaseResponse{}}
	if err := client.client.Send(request, response); err != nil {
		return "", err
	}
	if response.Response == nil || strings.TrimSpace(stringValue(response.Response.RequestId)) == "" {
		return "", fmt.Errorf("Tencent Tag %s response is missing", action)
	}
	return stringValue(response.Response.RequestId), nil
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

type tkeLegacyAPI interface {
	DescribeClusters(request *tke2018.DescribeClustersRequest) (*tke2018.DescribeClustersResponse, error)
	DescribeClusterLevelAttribute(request *tke2018.DescribeClusterLevelAttributeRequest) (*tke2018.DescribeClusterLevelAttributeResponse, error)
}

type cvmNativeAPI interface {
	DescribeAccountQuota(request *cvm2017.DescribeAccountQuotaRequest) (*cvm2017.DescribeAccountQuotaResponse, error)
	DescribeInstances(request *cvm2017.DescribeInstancesRequest) (*cvm2017.DescribeInstancesResponse, error)
	DescribeZoneInstanceConfigInfos(request *cvm2017.DescribeZoneInstanceConfigInfosRequest) (*cvm2017.DescribeZoneInstanceConfigInfosResponse, error)
	ModifyInstancesAttribute(request *cvm2017.ModifyInstancesAttributeRequest) (*cvm2017.ModifyInstancesAttributeResponse, error)
	RenewInstances(request *cvm2017.RenewInstancesRequest) (*cvm2017.RenewInstancesResponse, error)
}

type cbsNativeAPI interface {
	CreateDisks(request *cbs2017.CreateDisksRequest) (*cbs2017.CreateDisksResponse, error)
	DescribeDiskConfigQuota(request *cbs2017.DescribeDiskConfigQuotaRequest) (*cbs2017.DescribeDiskConfigQuotaResponse, error)
	DescribeDisks(request *cbs2017.DescribeDisksRequest) (*cbs2017.DescribeDisksResponse, error)
	InquiryPriceCreateDisks(request *cbs2017.InquiryPriceCreateDisksRequest) (*cbs2017.InquiryPriceCreateDisksResponse, error)
	RenewDisk(request *cbs2017.RenewDiskRequest) (*cbs2017.RenewDiskResponse, error)
}

type vpcNativeAPI interface {
	DescribeSubnets(request *vpc2017.DescribeSubnetsRequest) (*vpc2017.DescribeSubnetsResponse, error)
}

func (unimplementedTencentClient) WorkspaceSKUInventory(_ Request, _ map[string]string) Response {
	return Response{Ok: false, ErrorCode: "tencent_live_not_implemented", Message: "Tencent live Workspace SKU inventory is not implemented in this build.", Retryable: false}
}

func (unimplementedTencentClient) Capacity(_ Request, _ map[string]string) Response {
	return Response{Ok: false, ErrorCode: "tencent_live_not_implemented", Message: "Tencent live capacity preflight is not implemented in this build.", Retryable: false}
}

func (unimplementedTencentClient) StoragePreflight(_ Request, _ map[string]string) Response {
	return Response{Ok: false, ErrorCode: "tencent_live_not_implemented", Message: "Tencent live storage preflight is not implemented in this build.", Retryable: false}
}

func (unimplementedTencentClient) ProviderTruth(_ Request, _ map[string]string) Response {
	return Response{Ok: false, ErrorCode: "tencent_live_not_implemented", Message: "Tencent live provider truth is not implemented in this build.", Retryable: false}
}

func (unimplementedTencentClient) ComputeClaimTruth(_ Request, _ map[string]string) Response {
	return Response{Ok: false, ErrorCode: "provider_describe", Message: "Compute claim truth is unavailable.", Retryable: false}
}

func (unimplementedTencentClient) ClaimComputeMachine(_ Request, _ map[string]string) Response {
	return Response{Ok: false, ErrorCode: "provider_describe", Message: "Compute claim convergence is unavailable.", Retryable: false}
}

func (unimplementedTencentClient) PrepareComputeAllocation(_ Request, _ map[string]string) Response {
	return Response{Ok: false, ErrorCode: "tencent_live_not_implemented", Message: "Tencent live compute allocation preparation is not implemented in this build.", Retryable: false}
}

func (unimplementedTencentClient) CreateComputeAllocation(_ Request, _ map[string]string) Response {
	return Response{
		Ok:        false,
		ErrorCode: "tencent_live_not_implemented",
		Message:   "Tencent live compute allocation is not implemented in this build.",
		Retryable: false,
	}
}

func (unimplementedTencentClient) ReadComputeAllocation(_ Request, _ map[string]string) Response {
	return Response{Ok: false, ErrorCode: "tencent_live_not_implemented", Message: "Tencent live compute allocation readback is not implemented in this build.", Retryable: false}
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

func (unimplementedTencentClient) CreateStorageVolume(_ Request, _ map[string]string) Response {
	return Response{Ok: false, ErrorCode: "tencent_live_not_implemented", Message: "Tencent live CBS creation is not implemented in this build.", Retryable: false}
}

func (unimplementedTencentClient) DiscoverStorageVolume(_ Request, _ map[string]string) Response {
	return Response{Ok: false, StorageState: "unknown", ErrorCode: "tencent_live_not_implemented", Message: "Tencent live CBS discovery is not implemented in this build.", Retryable: false}
}

func (unimplementedTencentClient) SyncStorageVolume(_ Request, _ map[string]string) Response {
	return Response{Ok: false, ErrorCode: "tencent_live_not_implemented", Message: "Tencent live CBS sync is not implemented in this build.", Retryable: false}
}

func (unimplementedTencentClient) RenewStorageVolume(_ Request, _ map[string]string) Response {
	return Response{Ok: false, ErrorCode: "tencent_live_not_implemented", Message: "Tencent live CBS renewal is not implemented in this build.", Retryable: false}
}

func (unimplementedTencentClient) RenewComputeAllocation(_ Request, _ map[string]string) Response {
	return Response{Ok: false, ErrorCode: "tencent_live_not_implemented", Message: "Tencent live CVM renewal is not implemented in this build.", Retryable: false}
}

func (unimplementedTencentClient) BootstrapComputeNodePools(_ Request, _ map[string]string) Response {
	return Response{Ok: false, ErrorCode: "tencent_live_not_implemented", Message: "Tencent live node pool bootstrap is not implemented in this build.", Retryable: false}
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
	legacyTkeProfile := profile.NewClientProfile()
	legacyTkeProfile.HttpProfile.Endpoint = "tke.tencentcloudapi.com"
	legacyTkeClient, err := tke2018.NewClient(credential, env["TENCENTCLOUD_REGION"], legacyTkeProfile)
	if err != nil {
		return nil, &Response{Ok: false, ErrorCode: "tencent_sdk_client_failed", Message: err.Error(), Retryable: false}
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
	tagProfile := profile.NewClientProfile()
	tagProfile.HttpProfile.Endpoint = "tag.tencentcloudapi.com"
	tagClient := &common.Client{}
	tagClient.Init(env["TENCENTCLOUD_REGION"]).WithCredential(credential).WithProfile(tagProfile)

	return &tencentSDKClient{
		region:                env["TENCENTCLOUD_REGION"],
		clusterId:             env["TENCENT_DEPLOY_CLUSTER_ID"],
		nativeTkeClient:       tkeClient,
		nativeLegacyTkeClient: legacyTkeClient,
		nativeCvmClient:       cvmClient,
		nativeCbsClient:       cbsClient,
		nativeVpcClient:       vpcClient,
		nativeTagClient:       &tencentTagClient{client: tagClient, region: env["TENCENTCLOUD_REGION"]},
		convergenceContext:    context.Background(),
		convergenceWait:       boundedConvergenceWait,
	}, nil
}

func boundedConvergenceWait(ctx context.Context, attempt int) error {
	if attempt <= 0 {
		return nil
	}
	timer := time.NewTimer(time.Duration(attempt) * 200 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func workspaceSKUInventoryFailure(code string, err error) Response {
	message := code
	if err != nil {
		message = err.Error()
	}
	return Response{Ok: false, ErrorCode: code, Message: message, Retryable: false, MutationCount: 0}
}

func workspaceSKUShape(packageID string) (uint64, uint64) {
	cpu, memoryGB, _ := customerPackageResourceShape(packageID)
	return cpu, memoryGB
}

func workspaceSKUPackage(items []*cvm2017.InstanceTypeQuotaItem, packageID, zone string) (WorkspaceSKUPackage, error) {
	cpu, memoryGB := workspaceSKUShape(packageID)
	result := WorkspaceSKUPackage{PackageID: packageID, CPU: cpu, MemoryGB: memoryGB, Candidates: []WorkspaceSKUCandidate{}}
	seen := map[string]bool{}
	for _, item := range items {
		if item == nil || stringValue(item.Zone) != zone || stringValue(item.InstanceChargeType) != "PREPAID" || stringValue(item.Status) != "SELL" ||
			!exactInt64(item.Cpu, cpu) || !exactInt64(item.Memory, memoryGB) || item.Price == nil || item.Price.OriginalPrice == nil || *item.Price.OriginalPrice <= 0 ||
			item.Price.DiscountPrice == nil || *item.Price.DiscountPrice <= 0 {
			continue
		}
		instanceType := strings.TrimSpace(stringValue(item.InstanceType))
		if instanceType == "" || len(instanceType) > 64 || strings.ContainsAny(instanceType, " \t\r\n") || seen[instanceType] {
			return result, fmt.Errorf("eligible %s SKU inventory contains an empty or duplicate instance type", packageID)
		}
		seen[instanceType] = true
		result.Candidates = append(result.Candidates, WorkspaceSKUCandidate{
			InstanceType: instanceType, CPU: cpu, MemoryGB: memoryGB, MonthlyPriceCNY: *item.Price.DiscountPrice,
			StockStatus: stringValue(item.Status), StockCategory: stringValue(item.StatusCategory),
		})
	}
	sort.Slice(result.Candidates, func(i, j int) bool {
		if result.Candidates[i].MonthlyPriceCNY != result.Candidates[j].MonthlyPriceCNY {
			return result.Candidates[i].MonthlyPriceCNY < result.Candidates[j].MonthlyPriceCNY
		}
		return result.Candidates[i].InstanceType < result.Candidates[j].InstanceType
	})
	if len(result.Candidates) == 0 {
		return result, fmt.Errorf("no eligible %s PREPAID SKU matches the package shape in %s", packageID, zone)
	}
	result.RecommendedInstanceType = result.Candidates[0].InstanceType
	result.RecommendedMonthlyPriceCNY = result.Candidates[0].MonthlyPriceCNY
	return result, nil
}

func (client *tencentSDKClient) workspacePrepaidQuota(zone string) (uint64, error) {
	response, err := client.nativeCvmClient.DescribeAccountQuota(cvm2017.NewDescribeAccountQuotaRequest())
	if err != nil || response == nil || response.Response == nil || strings.TrimSpace(stringValue(response.Response.RequestId)) == "" ||
		response.Response.AccountQuotaOverview == nil || response.Response.AccountQuotaOverview.AccountQuota == nil {
		return 0, fmt.Errorf("Tencent PREPAID quota inventory is unavailable")
	}
	var matched *cvm2017.PrePaidQuota
	for _, quota := range response.Response.AccountQuotaOverview.AccountQuota.PrePaidQuotaSet {
		if quota == nil || strings.TrimSpace(stringValue(quota.Zone)) == "" {
			return 0, fmt.Errorf("Tencent PREPAID quota inventory is incomplete")
		}
		if stringValue(quota.Zone) == zone {
			if matched != nil {
				return 0, fmt.Errorf("Tencent PREPAID quota inventory is ambiguous")
			}
			matched = quota
		}
	}
	if matched == nil || matched.RemainingQuota == nil {
		return 0, fmt.Errorf("Tencent PREPAID quota inventory is unavailable for %s", zone)
	}
	return *matched.RemainingQuota, nil
}

func (client *tencentSDKClient) workspaceSubnetFacts(env map[string]string, zone string) ([]WorkspaceSubnetFact, uint64, error) {
	configured := splitCsv(env["TENCENT_CVM_SUBNET_ID"])
	seen := map[string]bool{}
	for _, subnetID := range configured {
		if seen[subnetID] {
			return nil, 0, fmt.Errorf("Workspace subnet configuration contains duplicate identities")
		}
		seen[subnetID] = true
	}
	if len(configured) == 0 {
		return nil, 0, fmt.Errorf("Workspace subnet configuration is missing")
	}
	request := vpc2017.NewDescribeSubnetsRequest()
	request.SubnetIds = stringsToPtrs(configured)
	response, err := client.nativeVpcClient.DescribeSubnets(request)
	if err != nil || response == nil || response.Response == nil || response.Response.TotalCount == nil ||
		strings.TrimSpace(stringValue(response.Response.RequestId)) == "" || *response.Response.TotalCount != uint64(len(configured)) || len(response.Response.SubnetSet) != len(configured) {
		return nil, 0, fmt.Errorf("Tencent Workspace subnet inventory is unavailable")
	}
	facts := make([]WorkspaceSubnetFact, 0, len(configured))
	matched := map[string]bool{}
	vpcID := ""
	available := uint64(0)
	for _, subnet := range response.Response.SubnetSet {
		if subnet == nil || subnet.AvailableIpAddressCount == nil || !seen[stringValue(subnet.SubnetId)] || matched[stringValue(subnet.SubnetId)] ||
			stringValue(subnet.Zone) != zone || strings.TrimSpace(stringValue(subnet.VpcId)) == "" {
			return nil, 0, fmt.Errorf("Tencent Workspace subnet inventory does not match the configured Zone and VPC")
		}
		if vpcID == "" {
			vpcID = stringValue(subnet.VpcId)
		} else if vpcID != stringValue(subnet.VpcId) {
			return nil, 0, fmt.Errorf("Workspace subnets span multiple VPCs")
		}
		if ^uint64(0)-available < *subnet.AvailableIpAddressCount {
			return nil, 0, fmt.Errorf("Workspace subnet capacity overflow")
		}
		available += *subnet.AvailableIpAddressCount
		matched[stringValue(subnet.SubnetId)] = true
		facts = append(facts, WorkspaceSubnetFact{SubnetID: stringValue(subnet.SubnetId), VPCID: vpcID, Zone: zone, AvailableIPAddresses: *subnet.AvailableIpAddressCount})
	}
	sort.Slice(facts, func(i, j int) bool { return facts[i].SubnetID < facts[j].SubnetID })
	return facts, available, nil
}

func (client *tencentSDKClient) workspaceTKECapacity() (uint64, uint64, uint64, TKEClusterCapacityFacts, error) {
	if client == nil || client.nativeLegacyTkeClient == nil || strings.TrimSpace(client.clusterId) == "" {
		return 0, 0, 0, TKEClusterCapacityFacts{}, fmt.Errorf("Tencent TKE cluster inventory is unavailable")
	}
	clustersRequest := tke2018.NewDescribeClustersRequest()
	clustersRequest.ClusterIds = []*string{common.StringPtr(client.clusterId)}
	clustersRequest.Limit = common.Int64Ptr(1)
	clusters, err := client.nativeLegacyTkeClient.DescribeClusters(clustersRequest)
	if err != nil || clusters == nil || clusters.Response == nil || clusters.Response.TotalCount == nil || *clusters.Response.TotalCount != 1 ||
		len(clusters.Response.Clusters) != 1 || clusters.Response.Clusters[0] == nil || strings.TrimSpace(stringValue(clusters.Response.RequestId)) == "" {
		return 0, 0, 0, TKEClusterCapacityFacts{}, fmt.Errorf("Tencent TKE cluster inventory is unavailable")
	}
	cluster := clusters.Response.Clusters[0]
	level := strings.TrimSpace(stringValue(cluster.ClusterLevel))
	if stringValue(cluster.ClusterId) != client.clusterId || !strings.EqualFold(stringValue(cluster.ClusterStatus), "Running") || level == "" || cluster.ClusterNodeNum == nil {
		return 0, 0, 0, TKEClusterCapacityFacts{}, fmt.Errorf("Tencent TKE cluster identity or level is unavailable")
	}
	facts := TKEClusterCapacityFacts{ClusterLevel: level, CurrentNodeCount: *cluster.ClusterNodeNum, LevelAttributes: []TKEClusterLevelFact{}}
	levelRequest := tke2018.NewDescribeClusterLevelAttributeRequest()
	levelRequest.ClusterID = common.StringPtr(client.clusterId)
	levels, err := client.nativeLegacyTkeClient.DescribeClusterLevelAttribute(levelRequest)
	if err != nil || levels == nil || levels.Response == nil || levels.Response.TotalCount == nil ||
		*levels.Response.TotalCount != int64(len(levels.Response.Items)) || strings.TrimSpace(stringValue(levels.Response.RequestId)) == "" {
		return 0, 0, 0, facts, fmt.Errorf("Tencent TKE cluster level inventory is unavailable")
	}
	for _, item := range levels.Response.Items {
		if item == nil {
			continue
		}
		attribute := TKEClusterLevelFact{Name: strings.TrimSpace(stringValue(item.Name)), Alias: strings.TrimSpace(stringValue(item.Alias))}
		if item.NodeCount != nil {
			nodeCount := *item.NodeCount
			attribute.NodeCount = &nodeCount
		}
		if item.Enable != nil {
			enable := *item.Enable
			attribute.Enable = &enable
		}
		facts.LevelAttributes = append(facts.LevelAttributes, attribute)
	}
	sort.Slice(facts.LevelAttributes, func(i, j int) bool {
		return facts.LevelAttributes[i].Name < facts.LevelAttributes[j].Name
	})
	matchCount := 0
	var nodeLimit *uint64
	for _, item := range levels.Response.Items {
		// The running cluster's matching NodeCount is the capacity fact; Enable is unrelated metadata.
		if item == nil || (strings.TrimSpace(stringValue(item.Name)) != level && strings.TrimSpace(stringValue(item.Alias)) != level) {
			continue
		}
		matchCount++
		if matchCount > 1 {
			return 0, 0, 0, facts, fmt.Errorf("Tencent TKE cluster level inventory is ambiguous")
		}
		if item.NodeCount != nil {
			nodeLimit = item.NodeCount
		}
	}
	if matchCount != 1 || nodeLimit == nil || *cluster.ClusterNodeNum > *nodeLimit {
		return 0, 0, 0, facts, fmt.Errorf("Tencent TKE cluster node limit is unavailable")
	}
	return *nodeLimit, *cluster.ClusterNodeNum, *nodeLimit - *cluster.ClusterNodeNum, facts, nil
}

func (client *tencentSDKClient) bootstrapSystemFacts(env map[string]string) (ProtectedSystemFacts, error) {
	facts := newProtectedSystemFacts()
	pools, err := client.bootstrapNodePoolInventory()
	if err != nil {
		facts.PoolCheckStatus = protectedCheckFailed
		return facts, err
	}
	return client.verifyBootstrapSystemIdentity(pools, env)
}

func (client *tencentSDKClient) WorkspaceSKUInventory(request Request, env map[string]string) Response {
	zone := strings.TrimSpace(request.Zone)
	configuredZone := strings.TrimSpace(env["OPL_TENCENT_ZONE"])
	if zone == "" {
		zone = configuredZone
	}
	if zone == "" || (configuredZone != "" && configuredZone != zone) || request.RequiredCapacity <= 0 {
		return workspaceSKUInventoryFailure("workspace_sku_inventory_invalid", nil)
	}
	if client == nil || client.nativeTkeClient == nil || client.nativeLegacyTkeClient == nil || client.nativeCvmClient == nil || client.nativeVpcClient == nil {
		return workspaceSKUInventoryFailure("workspace_sku_inventory_unavailable", fmt.Errorf("Tencent inventory clients are incomplete"))
	}
	availabilityRequest := cvm2017.NewDescribeZoneInstanceConfigInfosRequest()
	availabilityRequest.Filters = []*cvm2017.Filter{
		{Name: common.StringPtr("zone"), Values: []*string{common.StringPtr(zone)}},
		{Name: common.StringPtr("instance-charge-type"), Values: []*string{common.StringPtr("PREPAID")}},
	}
	availability, err := client.nativeCvmClient.DescribeZoneInstanceConfigInfos(availabilityRequest)
	if err != nil || availability == nil || availability.Response == nil || strings.TrimSpace(stringValue(availability.Response.RequestId)) == "" {
		return workspaceSKUInventoryFailure("workspace_sku_inventory_unavailable", fmt.Errorf("Tencent Workspace SKU inventory is unavailable"))
	}
	packages := make([]WorkspaceSKUPackage, 0, 2)
	for _, packageID := range []string{"basic", "pro"} {
		current, packageErr := workspaceSKUPackage(availability.Response.InstanceTypeQuotaSet, packageID, zone)
		if packageErr != nil {
			response := workspaceSKUInventoryFailure("workspace_sku_unavailable", packageErr)
			response.SKUPackages = packages
			return response
		}
		packages = append(packages, current)
	}
	quota, err := client.workspacePrepaidQuota(zone)
	if err != nil {
		response := workspaceSKUInventoryFailure("workspace_sku_inventory_unavailable", err)
		response.SKUPackages = packages
		return response
	}
	subnets, subnetCapacity, err := client.workspaceSubnetFacts(env, zone)
	if err != nil {
		response := workspaceSKUInventoryFailure("workspace_sku_inventory_unavailable", err)
		response.SKUPackages, response.PrepaidQuotaRemaining = packages, quota
		return response
	}
	nodeLimit, currentNodes, availableNodes, tkeCapacity, err := client.workspaceTKECapacity()
	if err != nil {
		response := workspaceSKUInventoryFailure("workspace_sku_inventory_unavailable", err)
		response.SKUPackages, response.PrepaidQuotaRemaining, response.Subnets = packages, quota, subnets
		if tkeCapacity.ClusterLevel != "" {
			response.TKECapacity = &tkeCapacity
		}
		return response
	}
	protectedSystem, err := client.bootstrapSystemFacts(env)
	if err != nil {
		response := workspaceSKUInventoryFailure("protected_system_identity_mismatch", fmt.Errorf("Protected system identity inventory is unavailable or inconsistent"))
		response.SKUPackages, response.PrepaidQuotaRemaining, response.Subnets = packages, quota, subnets
		response.TKEClusterNodeLimit, response.TKECurrentNodeCount, response.TKEAvailableNodeCapacity = nodeLimit, currentNodes, availableNodes
		response.ProtectedSystem = protectedSystem
		return response
	}
	response := Response{
		Ok: true, Status: "ready", RequiredCapacity: request.RequiredCapacity, SKUPackages: packages,
		PrepaidQuotaRemaining: quota, Subnets: subnets, TKEClusterNodeLimit: nodeLimit,
		TKECurrentNodeCount: currentNodes, TKEAvailableNodeCapacity: availableNodes, TKECapacity: &tkeCapacity,
		ProtectedSystem: protectedSystem, MutationCount: 0,
	}
	if quota < uint64(request.RequiredCapacity) || subnetCapacity < uint64(request.RequiredCapacity) || availableNodes < uint64(request.RequiredCapacity) {
		response.Ok = false
		response.Status = "blocked"
		response.ErrorCode = "workspace_capacity_insufficient"
		response.Message = "Approved Workspace NodePool capacity exceeds current PREPAID quota, subnet IP, or TKE node limits."
	}
	return response
}

func capacityFailure(code string, err error) Response {
	message := code
	if err != nil {
		message = err.Error()
	}
	return Response{Ok: false, ErrorCode: code, Message: message, Retryable: false}
}

func completedPreflightStage(stage, status, code string, started time.Time, blockedBy []string, facts map[string]any) PreflightStage {
	if blockedBy == nil {
		blockedBy = []string{}
	}
	if facts == nil {
		facts = map[string]any{}
	}
	return PreflightStage{Stage: stage, Status: status, ErrorCode: code, BlockedBy: blockedBy, DurationMS: time.Since(started).Milliseconds(), SafeFacts: facts}
}

func failedPreflightResponse(stages []PreflightStage) Response {
	code := "monthly_preflight_unavailable"
	for _, stage := range stages {
		if stage.Status == "failed" && stage.ErrorCode != "" {
			code = stage.ErrorCode
			break
		}
	}
	return Response{Ok: false, ErrorCode: code, Message: code, Retryable: false, PreflightStages: stages}
}

func allPreflightStagesPassed(stages []PreflightStage) bool {
	for _, stage := range stages {
		if stage.Status != "passed" {
			return false
		}
	}
	return true
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
	if pool == nil || pool.Native == nil || pool.Native.InstanceChargePrepaid == nil {
		return false
	}
	prepaid := pool.Native.InstanceChargePrepaid
	return stringValue(pool.Type) == "Native" && stringValue(pool.Native.MachineType) == "NativeCVM" &&
		stringValue(pool.Native.InstanceChargeType) == "PREPAID" &&
		prepaid.Period != nil && *prepaid.Period == 1 && stringValue(prepaid.RenewFlag) == "NOTIFY_AND_MANUAL_RENEW"
}

func mutationNodePoolFailure(pool *tke2022.NodePool, request Request, requestID string) *Response {
	if !isCVMNativeNodePool(pool) {
		return &Response{Ok: false, ErrorCode: "tencent_cvm_node_pool_required", Message: "Fabric compute requires a one-month PREPAID NativeCVM node pool with manual renewal.", ProviderRequestId: requestID, Retryable: false}
	}
	lifeState := strings.TrimSpace(stringValue(pool.LifeState))
	if strings.EqualFold(lifeState, "Creating") {
		return &Response{Ok: false, ErrorCode: "tencent_node_pool_not_ready", Message: "Tencent node pool is still being created.", ProviderRequestId: requestID, Retryable: true}
	}
	if !strings.EqualFold(lifeState, "Running") || !matchesCapacityNodePool(pool, request) {
		return &Response{Ok: false, ErrorCode: "tencent_node_pool_ownership_mismatch", Message: "Node pool does not match the requested ownership labels.", ProviderRequestId: requestID, Retryable: false}
	}
	requiredReplicas := request.Pool.DesiredReplicas
	if request.Pool.TargetReplicas > 0 {
		requiredReplicas = request.Pool.TargetReplicas
	} else if request.Allocation.Id != "" {
		requiredReplicas = nativeReplicas(pool) + 1
	}
	if len(pool.Native.InstanceTypes) != 1 || stringValue(pool.Native.InstanceTypes[0]) != request.Pool.InstanceType ||
		pool.Native.EnableAutoscaling == nil || *pool.Native.EnableAutoscaling ||
		pool.Native.AutoRepair == nil || *pool.Native.AutoRepair || pool.Native.Replicas == nil ||
		pool.Native.Scaling == nil || pool.Native.Scaling.MinReplicas == nil || *pool.Native.Scaling.MinReplicas != 0 || pool.Native.Scaling.MaxReplicas == nil ||
		request.Pool.MaxReplicas <= 0 || *pool.Native.Scaling.MaxReplicas != request.Pool.MaxReplicas || *pool.Native.Scaling.MaxReplicas < requiredReplicas {
		return &Response{Ok: false, ErrorCode: "tencent_node_pool_configuration_mismatch", Message: "Node pool runtime configuration does not match the requested Fabric mutation.", ProviderRequestId: requestID, Retryable: false}
	}
	return nil
}

func readbackNodePoolFailure(pool *tke2022.NodePool, request Request, requestID string) *Response {
	if !isCVMNativeNodePool(pool) || !strings.EqualFold(strings.TrimSpace(stringValue(pool.LifeState)), "running") ||
		!matchesCapacityNodePool(pool, request) || pool.Native == nil || len(pool.Native.InstanceTypes) != 1 ||
		stringValue(pool.Native.InstanceTypes[0]) != request.Pool.InstanceType {
		return &Response{Ok: false, ErrorCode: "compute_instance_type_mismatch", Message: "Tencent NodePool instance type does not match the requested package.", ProviderRequestId: requestID, Retryable: true}
	}
	return nil
}

func exactUint64(value *uint64, expected uint64) bool {
	return expected > 0 && value != nil && *value == expected
}

func exactInt64(value *int64, expected uint64) bool {
	return expected > 0 && value != nil && *value > 0 && uint64(*value) == expected
}

func machineResourceShapeMatches(machine *tke2022.Machine, pool ComputePoolInput) bool {
	return machine != nil && exactUint64(machine.CPU, pool.CPU) && exactUint64(machine.Memory, pool.MemoryGB)
}

func nativeResourceShapeMatches(native *tke2022.NativeNodeInfo, pool ComputePoolInput) bool {
	return native != nil && exactUint64(native.CPU, pool.CPU) && exactUint64(native.Memory, pool.MemoryGB)
}

func cvmResourceShapeMatches(instance *cvm2017.Instance, pool ComputePoolInput) bool {
	return instance != nil && exactInt64(instance.CPU, pool.CPU) && exactInt64(instance.Memory, pool.MemoryGB)
}

func customerPackageResourceShape(packageID string) (uint64, uint64, bool) {
	switch packageID {
	case "basic":
		return 2, 4, true
	case "pro":
		return 8, 16, true
	default:
		return 0, 0, false
	}
}

func computeResourceShapeFailure(requestID string) Response {
	return Response{Ok: false, ErrorCode: "compute_resource_shape_mismatch", Message: "Tencent Machine, Native instance, and CVM CPU and memory must exactly match the package resource contract.", ProviderRequestId: requestID, Retryable: true}
}

func matchesCapacityNodePool(pool *tke2022.NodePool, request Request) bool {
	labels := nodePoolLabels(pool)
	return request.Pool.Id != "" && request.PackageId != "" && request.Pool.InstanceType != "" &&
		labels["oplcloud.cn/pool-id"] == request.Pool.Id &&
		labels["oplcloud.cn/package-id"] == request.PackageId &&
		labels["oplcloud.cn/instance-type"] == request.Pool.InstanceType
}

func (client *tencentSDKClient) capacityNodePool(request Request) (*tke2022.NodePool, string, error) {
	if strings.TrimSpace(request.Pool.NodePoolId) == "" {
		return nil, "", fmt.Errorf("explicit node pool id required")
	}
	describe := tke2022.NewDescribeNodePoolsRequest()
	describe.ClusterId = common.StringPtr(client.clusterId)
	describe.Limit = common.Int64Ptr(1)
	describe.Filters = []*tke2022.Filter{{Name: common.StringPtr("NodePoolsId"), Values: []*string{common.StringPtr(request.Pool.NodePoolId)}}}
	response, err := client.nativeTkeClient.DescribeNodePools(describe)
	if err != nil {
		return nil, "", err
	}
	if response == nil || response.Response == nil {
		return nil, "", fmt.Errorf("explicit node pool ownership mismatch")
	}
	requestID := stringValue(response.Response.RequestId)
	if response.Response.TotalCount == nil || *response.Response.TotalCount != 1 ||
		len(response.Response.NodePools) != 1 || response.Response.NodePools[0] == nil ||
		stringValue(response.Response.NodePools[0].NodePoolId) != request.Pool.NodePoolId || !matchesCapacityNodePool(response.Response.NodePools[0], request) {
		return nil, requestID, fmt.Errorf("explicit node pool ownership mismatch")
	}
	return response.Response.NodePools[0], requestID, nil
}

func (client *tencentSDKClient) Capacity(request Request, _ map[string]string) Response {
	required := request.Pool.DesiredReplicas
	expectedCPU, expectedMemoryGB, packageShapeKnown := customerPackageResourceShape(request.PackageId)
	if required <= 0 || request.Pool.MaxReplicas <= 0 || strings.TrimSpace(request.Pool.InstanceType) == "" || strings.TrimSpace(request.Pool.NodePoolId) == "" || strings.TrimSpace(request.Pool.Id) == "" ||
		!packageShapeKnown || request.Pool.CPU != expectedCPU || request.Pool.MemoryGB != expectedMemoryGB {
		stages := []PreflightStage{}
		for _, stage := range []string{"node_pool_discovery", "tke_cluster_capacity", "node_pool_contract", "subnet", "zone", "cvm_prepaid_quota", "cvm_sku_price"} {
			stages = append(stages, completedPreflightStage(stage, "failed", "tencent_capacity_input_invalid", time.Now(), nil, nil))
		}
		return failedPreflightResponse(stages)
	}
	stages := make([]PreflightStage, 0, 7)
	clientReady := client != nil && client.nativeTkeClient != nil && client.nativeLegacyTkeClient != nil && client.nativeCvmClient != nil && client.nativeVpcClient != nil
	if !clientReady {
		for _, stage := range []string{"node_pool_discovery", "tke_cluster_capacity", "node_pool_contract", "subnet", "zone", "cvm_prepaid_quota", "cvm_sku_price"} {
			stages = append(stages, completedPreflightStage(stage, "failed", "tencent_capacity_client_missing", time.Now(), nil, nil))
		}
		return failedPreflightResponse(stages)
	}

	discoveryStarted := time.Now()
	pool, nodePoolRequestId, err := client.capacityNodePool(request)
	if err != nil {
		stages = append(stages, completedPreflightStage("node_pool_discovery", "failed", "tencent_capacity_node_pool_unavailable", discoveryStarted, nil, map[string]any{"matchCount": 0}))
	} else {
		stages = append(stages, completedPreflightStage("node_pool_discovery", "passed", "", discoveryStarted, nil, map[string]any{"matchCount": 1}))
	}

	clusterCapacityStarted := time.Now()
	clusterLimit, currentNodes, availableNodes, clusterFacts, clusterCapacityErr := client.workspaceTKECapacity()
	clusterCapacityPassed := clusterCapacityErr == nil && availableNodes >= uint64(required)
	clusterCapacityStatus, clusterCapacityCode := "passed", ""
	if clusterCapacityErr != nil {
		clusterCapacityStatus, clusterCapacityCode = "failed", "tencent_capacity_cluster_inventory_unavailable"
	} else if !clusterCapacityPassed {
		clusterCapacityStatus, clusterCapacityCode = "failed", "tencent_capacity_cluster_headroom_unavailable"
	}
	clusterSafeFacts := map[string]any{"clusterLevel": clusterFacts.ClusterLevel, "nodeLimit": clusterLimit, "currentNodes": currentNodes, "availableNodes": availableNodes, "requiredReplicas": required}
	stages = append(stages, completedPreflightStage("tke_cluster_capacity", clusterCapacityStatus, clusterCapacityCode, clusterCapacityStarted, nil, clusterSafeFacts))

	var native *tke2022.NativeNodePoolInfo
	contractPassed := false
	contractStarted := time.Now()
	if !clusterCapacityPassed {
		stages = append(stages, completedPreflightStage("node_pool_contract", "blocked", "preflight_dependency_blocked", contractStarted, []string{"tke_cluster_capacity"}, nil))
	} else if pool == nil {
		stages = append(stages, completedPreflightStage("node_pool_contract", "blocked", "preflight_dependency_blocked", contractStarted, []string{"node_pool_discovery"}, nil))
	} else {
		native = pool.Native
		contractPassed = isCVMNativeNodePool(pool) && strings.TrimSpace(stringValue(pool.LifeState)) == "Running" && native.Scaling != nil && native.Scaling.MinReplicas != nil && *native.Scaling.MinReplicas == 0 && native.Scaling.MaxReplicas != nil && *native.Scaling.MaxReplicas == request.Pool.MaxReplicas &&
			native.Replicas != nil && native.ReadyReplicas != nil && native.EnableAutoscaling != nil && native.AutoRepair != nil &&
			!*native.EnableAutoscaling && !*native.AutoRepair && *native.ReadyReplicas == *native.Replicas &&
			*native.Scaling.MaxReplicas >= *native.Replicas+required && len(native.InstanceTypes) == 1 && stringValue(native.InstanceTypes[0]) == request.Pool.InstanceType && len(native.SubnetIds) > 0
		status, code := "passed", ""
		if !contractPassed {
			status, code = "failed", "tencent_capacity_node_pool_unavailable"
		}
		facts := nodePoolSafeFacts(pool, request, required)
		if native != nil && native.Replicas != nil && native.ReadyReplicas != nil && native.Scaling != nil && native.Scaling.MaxReplicas != nil {
			facts["currentReplicas"], facts["readyReplicas"], facts["maxReplicas"] = *native.Replicas, *native.ReadyReplicas, *native.Scaling.MaxReplicas
		}
		stages = append(stages, completedPreflightStage("node_pool_contract", status, code, contractStarted, nil, facts))
	}

	subnetIds := []string{}
	zones := []string{}
	subnetRequestID := ""
	subnetPassed := false
	subnetStarted := time.Now()
	if !contractPassed {
		stages = append(stages, completedPreflightStage("subnet", "blocked", "preflight_dependency_blocked", subnetStarted, []string{"node_pool_contract"}, nil))
	} else {
		seenSubnetIds := map[string]bool{}
		validSubnetIDs := true
		for _, raw := range native.SubnetIds {
			subnetID := strings.TrimSpace(stringValue(raw))
			if subnetID == "" || seenSubnetIds[subnetID] {
				validSubnetIDs = false
				break
			}
			seenSubnetIds[subnetID] = true
			subnetIds = append(subnetIds, subnetID)
		}
		if validSubnetIDs {
			subnetRequest := vpc2017.NewDescribeSubnetsRequest()
			subnetRequest.SubnetIds = stringsToPtrs(subnetIds)
			subnetResponse, describeErr := client.nativeVpcClient.DescribeSubnets(subnetRequest)
			if describeErr == nil && subnetResponse != nil && subnetResponse.Response != nil {
				subnetRequestID = stringValue(subnetResponse.Response.RequestId)
				seenZones, foundSubnets := map[string]bool{}, map[string]bool{}
				for _, subnet := range subnetResponse.Response.SubnetSet {
					if subnet == nil || !seenSubnetIds[stringValue(subnet.SubnetId)] || foundSubnets[stringValue(subnet.SubnetId)] || subnet.AvailableIpAddressCount == nil || int64(*subnet.AvailableIpAddressCount) < required || strings.TrimSpace(stringValue(subnet.Zone)) == "" {
						validSubnetIDs = false
						break
					}
					foundSubnets[stringValue(subnet.SubnetId)] = true
					zone := strings.TrimSpace(stringValue(subnet.Zone))
					if !seenZones[zone] {
						seenZones[zone], zones = true, append(zones, zone)
					}
				}
				subnetPassed = validSubnetIDs && len(foundSubnets) == len(subnetIds) && subnetRequestID != "" && nodePoolRequestId != ""
			}
		}
		status, code := "passed", ""
		if !subnetPassed {
			status, code = "failed", "tencent_capacity_subnet_unavailable"
		}
		stages = append(stages, completedPreflightStage("subnet", status, code, subnetStarted, nil, map[string]any{"subnetCount": len(subnetIds)}))
	}
	zoneStarted := time.Now()
	zonePassed := subnetPassed && len(zones) == 1 && (strings.TrimSpace(request.Zone) == "" || zones[0] == request.Zone)
	if !subnetPassed {
		stages = append(stages, completedPreflightStage("zone", "blocked", "preflight_dependency_blocked", zoneStarted, []string{"subnet"}, nil))
	} else {
		status, code := "passed", ""
		if !zonePassed {
			status, code = "failed", "tencent_capacity_zone_unavailable"
		}
		stages = append(stages, completedPreflightStage("zone", status, code, zoneStarted, nil, map[string]any{"zone": firstNonEmpty(request.Zone, firstString(stringsToPtrs(zones)))}))
	}
	targetZone := strings.TrimSpace(request.Zone)
	if targetZone == "" && len(zones) == 1 {
		targetZone = zones[0]
	}

	remainingQuota := uint64(0)
	quotaRequestID := ""
	quotaStarted := time.Now()
	if targetZone == "" {
		stages = append(stages, completedPreflightStage("cvm_prepaid_quota", "blocked", "preflight_dependency_blocked", quotaStarted, []string{"zone"}, nil))
	} else {
		quotaRequest := cvm2017.NewDescribeAccountQuotaRequest()
		quotaResponse, err := client.nativeCvmClient.DescribeAccountQuota(quotaRequest)
		quotaPassed := false
		code := "tencent_capacity_prepaid_quota_describe_failed"
		if err == nil && quotaResponse != nil && quotaResponse.Response != nil && quotaResponse.Response.AccountQuotaOverview != nil && quotaResponse.Response.AccountQuotaOverview.AccountQuota != nil {
			quotaRequestID = stringValue(quotaResponse.Response.RequestId)
			matchingQuotas, ambiguous := []*cvm2017.PrePaidQuota{}, false
			for _, quota := range quotaResponse.Response.AccountQuotaOverview.AccountQuota.PrePaidQuotaSet {
				if quota == nil || strings.TrimSpace(stringValue(quota.Zone)) == "" {
					ambiguous = true
					continue
				}
				if stringValue(quota.Zone) == targetZone {
					matchingQuotas = append(matchingQuotas, quota)
				}
			}
			if !ambiguous && len(matchingQuotas) == 1 && matchingQuotas[0].RemainingQuota != nil {
				remainingQuota = *matchingQuotas[0].RemainingQuota
				quotaPassed = remainingQuota >= uint64(required) && quotaRequestID != ""
				code = "tencent_capacity_prepaid_quota_unavailable"
			}
		}
		status := "passed"
		if !quotaPassed {
			status = "failed"
		}
		stages = append(stages, completedPreflightStage("cvm_prepaid_quota", status, map[bool]string{true: "", false: code}[quotaPassed], quotaStarted, nil, map[string]any{"zone": targetZone, "remainingQuota": remainingQuota, "requiredCapacity": required}))
	}

	providerPriceCNY := 0.0
	availabilityRequestID := ""
	priceStarted := time.Now()
	if targetZone == "" {
		stages = append(stages, completedPreflightStage("cvm_sku_price", "blocked", "preflight_dependency_blocked", priceStarted, []string{"zone"}, nil))
	} else {
		zone := targetZone
		availabilityRequest := cvm2017.NewDescribeZoneInstanceConfigInfosRequest()
		availabilityRequest.Filters = []*cvm2017.Filter{
			{Name: common.StringPtr("zone"), Values: []*string{common.StringPtr(zone)}},
			{Name: common.StringPtr("instance-type"), Values: []*string{common.StringPtr(request.Pool.InstanceType)}},
			{Name: common.StringPtr("instance-charge-type"), Values: []*string{common.StringPtr("PREPAID")}},
		}
		availability, err := client.nativeCvmClient.DescribeZoneInstanceConfigInfos(availabilityRequest)
		exactAvailability := 0
		sell := false
		zonePriceCNY := 0.0
		shapeMatches := false
		var actualCPU any
		var actualMemoryGB any
		if err == nil && availability != nil && availability.Response != nil {
			for _, item := range availability.Response.InstanceTypeQuotaSet {
				if item != nil && stringValue(item.Zone) == zone && stringValue(item.InstanceType) == request.Pool.InstanceType && stringValue(item.InstanceChargeType) == "PREPAID" {
					exactAvailability++
					sell = stringValue(item.Status) == "SELL"
					if item.Cpu != nil {
						actualCPU = *item.Cpu
					}
					if item.Memory != nil {
						actualMemoryGB = *item.Memory
					}
					shapeMatches = exactInt64(item.Cpu, expectedCPU) && exactInt64(item.Memory, expectedMemoryGB)
					if item.Price != nil && item.Price.DiscountPrice != nil {
						zonePriceCNY = *item.Price.DiscountPrice
					}
				}
			}
			availabilityRequestID = stringValue(availability.Response.RequestId)
		}
		pricePassed := exactAvailability == 1 && shapeMatches && sell && zonePriceCNY > 0 && availabilityRequestID != ""
		status, code := "passed", ""
		if !pricePassed {
			status, code = "failed", "tencent_capacity_instance_unavailable"
			if exactAvailability == 1 && !shapeMatches {
				code = "tencent_capacity_instance_shape_mismatch"
			}
		}
		providerPriceCNY = zonePriceCNY
		stages = append(stages, completedPreflightStage("cvm_sku_price", status, code, priceStarted, nil, map[string]any{
			"zone": targetZone, "instanceType": request.Pool.InstanceType, "cpu": actualCPU, "memoryGb": actualMemoryGB,
			"available": sell, "providerPriceCny": zonePriceCNY,
		}))
	}
	if !allPreflightStagesPassed(stages) {
		return failedPreflightResponse(stages)
	}
	providerRequestIDs := map[string]string{"nodePool": nodePoolRequestId, "subnets": subnetRequestID, "quota": quotaRequestID, "availability": availabilityRequestID}
	return Response{
		Ok: true, Status: "ready", ProviderRequestId: nodePoolRequestId, NodePoolId: stringValue(pool.NodePoolId),
		ProviderRequestIDs: providerRequestIDs, ProviderPriceCNY: providerPriceCNY,
		ProviderData: map[string]string{"chargeType": "PREPAID", "periodMonths": "1", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "zone": targetZone},
		InstanceType: request.Pool.InstanceType, InstanceAvailable: true, RequiredCapacity: required,
		RemainingQuota:  remainingQuota,
		CurrentReplicas: *native.Replicas, ReadyReplicas: *native.ReadyReplicas,
		MaxReplicas: *native.Scaling.MaxReplicas, TargetReplicas: *native.Replicas + required, MachineType: stringValue(native.MachineType), Zones: zones, PreflightStages: stages,
		TKEClusterNodeLimit: clusterLimit, TKECurrentNodeCount: currentNodes, TKEAvailableNodeCapacity: availableNodes, TKECapacity: &clusterFacts,
	}
}

func nodePoolSafeFacts(pool *tke2022.NodePool, request Request, required int64) map[string]any {
	facts := map[string]any{
		"nodePoolId": request.Pool.NodePoolId, "poolId": request.Pool.Id, "packageId": request.PackageId,
		"expectedInstanceType": request.Pool.InstanceType, "requiredCapacity": required,
	}
	if pool == nil {
		return facts
	}
	facts["labels"] = nodePoolLabels(pool)
	facts["type"] = stringValue(pool.Type)
	facts["lifeState"] = stringValue(pool.LifeState)
	taints := make([]map[string]string, 0, len(pool.Taints))
	for _, taint := range pool.Taints {
		if taint != nil {
			taints = append(taints, map[string]string{"key": stringValue(taint.Key), "value": stringValue(taint.Value), "effect": stringValue(taint.Effect)})
		}
	}
	facts["taints"] = taints
	if pool.Native == nil {
		return facts
	}
	native := pool.Native
	facts["machineType"] = stringValue(native.MachineType)
	facts["chargeType"] = stringValue(native.InstanceChargeType)
	facts["autoscaling"] = boolValue(native.EnableAutoscaling)
	facts["autoRepair"] = boolValue(native.AutoRepair)
	facts["actualInstanceTypes"] = ptrsToStrings(native.InstanceTypes)
	facts["zones"] = []string{}
	if native.Replicas != nil {
		facts["currentReplicas"] = *native.Replicas
	}
	if native.ReadyReplicas != nil {
		facts["readyReplicas"] = *native.ReadyReplicas
	}
	if native.Scaling != nil {
		if native.Scaling.MinReplicas != nil {
			facts["minReplicas"] = *native.Scaling.MinReplicas
		}
		if native.Scaling.MaxReplicas != nil {
			facts["maxReplicas"] = *native.Scaling.MaxReplicas
		}
	}
	if native.InstanceChargePrepaid != nil {
		if native.InstanceChargePrepaid.Period != nil {
			facts["periodMonths"] = *native.InstanceChargePrepaid.Period
		}
		facts["renewFlag"] = stringValue(native.InstanceChargePrepaid.RenewFlag)
	}
	return facts
}

func ptrsToStrings(values []*string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, stringValue(value))
	}
	return result
}

func boolValue(value *bool) bool {
	return value != nil && *value
}

func (client *tencentSDKClient) StoragePreflight(request Request, env map[string]string) Response {
	storage := request.Storage
	storage.DiskType = firstNonEmpty(storage.DiskType, env["TENCENT_CBS_DISK_TYPE"], "CLOUD_BSSD")
	if storage.SizeGB == 0 || strings.TrimSpace(storage.Zone) == "" || strings.TrimSpace(storage.DiskType) == "" {
		return failedPreflightResponse([]PreflightStage{
			completedPreflightStage("cbs_prepaid_quota", "failed", "tencent_storage_preflight_input_invalid", time.Now(), nil, nil),
			completedPreflightStage("cbs_price", "failed", "tencent_storage_preflight_input_invalid", time.Now(), nil, nil),
		})
	}
	if client == nil || client.nativeCbsClient == nil {
		return failedPreflightResponse([]PreflightStage{
			completedPreflightStage("cbs_prepaid_quota", "failed", "tencent_storage_preflight_client_missing", time.Now(), nil, nil),
			completedPreflightStage("cbs_price", "failed", "tencent_storage_preflight_client_missing", time.Now(), nil, nil),
		})
	}
	stages := make([]PreflightStage, 0, 2)
	quotaStarted := time.Now()
	quotaRequest := cbs2017.NewDescribeDiskConfigQuotaRequest()
	quotaRequest.InquiryType = common.StringPtr("INQUIRY_CBS_CONFIG")
	quotaRequest.DiskChargeType = common.StringPtr("PREPAID")
	quotaRequest.DiskTypes = []*string{common.StringPtr(storage.DiskType)}
	quotaRequest.Zones = []*string{common.StringPtr(storage.Zone)}
	quotaRequest.DiskUsage = common.StringPtr("DATA_DISK")
	quota, err := client.nativeCbsClient.DescribeDiskConfigQuota(quotaRequest)
	quotaRequestID := ""
	matches := 0
	available := false
	if err == nil && quota != nil && quota.Response != nil {
		quotaRequestID = stringValue(quota.Response.RequestId)
		for _, config := range quota.Response.DiskConfigSet {
			if config == nil || stringValue(config.DiskChargeType) != "PREPAID" || stringValue(config.Zone) != storage.Zone || stringValue(config.DiskType) != storage.DiskType || stringValue(config.DiskUsage) != "DATA_DISK" {
				continue
			}
			matches++
			available = config.Available != nil && *config.Available && config.MinDiskSize != nil && config.MaxDiskSize != nil && config.StepSize != nil && *config.StepSize > 0 &&
				storage.SizeGB >= *config.MinDiskSize && storage.SizeGB <= *config.MaxDiskSize && (storage.SizeGB-*config.MinDiskSize)%*config.StepSize == 0
		}
	}
	quotaPassed := quotaRequestID != "" && matches == 1 && available
	quotaStatus, quotaCode := "passed", ""
	if !quotaPassed {
		quotaStatus, quotaCode = "failed", "tencent_storage_quota_unavailable"
	}
	stages = append(stages, completedPreflightStage("cbs_prepaid_quota", quotaStatus, quotaCode, quotaStarted, nil, map[string]any{"zone": storage.Zone, "diskType": storage.DiskType, "sizeGb": storage.SizeGB, "available": available}))

	priceStarted := time.Now()
	priceRequest := cbs2017.NewInquiryPriceCreateDisksRequest()
	priceRequest.DiskChargeType = common.StringPtr("PREPAID")
	priceRequest.DiskType = common.StringPtr(storage.DiskType)
	priceRequest.DiskSize = common.Uint64Ptr(storage.SizeGB)
	priceRequest.DiskCount = common.Uint64Ptr(1)
	priceRequest.DiskChargePrepaid = &cbs2017.DiskChargePrepaid{Period: common.Uint64Ptr(1), RenewFlag: common.StringPtr("NOTIFY_AND_MANUAL_RENEW")}
	price, err := client.nativeCbsClient.InquiryPriceCreateDisks(priceRequest)
	providerPriceCNY := 0.0
	priceRequestID := ""
	if err == nil && price != nil && price.Response != nil && price.Response.DiskPrice != nil && price.Response.DiskPrice.DiscountPrice != nil {
		providerPriceCNY = *price.Response.DiskPrice.DiscountPrice
		priceRequestID = stringValue(price.Response.RequestId)
	}
	pricePassed := providerPriceCNY > 0 && priceRequestID != ""
	priceStatus, priceCode := "passed", ""
	if !pricePassed {
		priceStatus, priceCode = "failed", "tencent_storage_price_unavailable"
	}
	stages = append(stages, completedPreflightStage("cbs_price", priceStatus, priceCode, priceStarted, nil, map[string]any{"zone": storage.Zone, "diskType": storage.DiskType, "sizeGb": storage.SizeGB, "providerPriceCny": providerPriceCNY}))
	if !allPreflightStagesPassed(stages) {
		return failedPreflightResponse(stages)
	}
	return Response{
		Ok: true, Status: "ready", ProviderRequestId: priceRequestID,
		ProviderRequestIDs: map[string]string{"quota": quotaRequestID, "price": priceRequestID}, ProviderPriceCNY: providerPriceCNY,
		ProviderData:    map[string]string{"chargeType": "PREPAID", "periodMonths": "1", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "zone": storage.Zone, "diskType": storage.DiskType, "sizeGb": strconv.FormatUint(storage.SizeGB, 10)},
		PreflightStages: stages,
	}
}

func (client *tencentSDKClient) CreateStorageVolume(request Request, env map[string]string) Response {
	if client == nil || client.nativeCbsClient == nil {
		return Response{Ok: false, ErrorCode: "tencent_sdk_client_missing", Message: "Tencent CBS SDK client is missing.", Retryable: false}
	}
	storage := request.Storage
	storage.DiskType = firstNonEmpty(storage.DiskType, env["TENCENT_CBS_DISK_TYPE"], "CLOUD_BSSD")
	if strings.TrimSpace(request.AccountId) == "" || strings.TrimSpace(storage.Id) == "" || storage.SizeGB == 0 || strings.TrimSpace(storage.Zone) == "" ||
		!validCBSOwnershipTags(request.Tags) || request.Tags["opl_account_id"] != request.AccountId || request.Tags["opl_resource_id"] != storage.Id {
		return Response{Ok: false, ErrorCode: "tencent_cbs_input_invalid", Message: "Exact CBS account, resource, size, compute Zone, and ownership tags are required.", Retryable: false}
	}
	discoveryRequest := request
	discoveryRequest.Storage = storage
	discovery := client.DiscoverStorageVolume(discoveryRequest, env)
	if !discovery.Ok {
		return discovery
	}
	switch storage.ExpectedState {
	case "":
		if discovery.StorageState == "storage_existing_exact" {
			return discovery
		}
		if storage.AllowExistingExactReplay {
			return Response{Ok: false, StorageState: "unknown", ErrorCode: "tencent_cbs_replay_unconfirmed", Message: "A reserved CBS create attempt cannot be repeated without exact disk readback.", ProviderRequestId: discovery.ProviderRequestId, Retryable: false}
		}
	case "storage_not_started":
		if storage.ExpectedProviderResourceId != "" {
			return Response{Ok: false, StorageState: "unknown", ErrorCode: "tencent_cbs_approval_invalid", Message: "Approved absent CBS state cannot bind a disk identity.", ProviderRequestId: discovery.ProviderRequestId, Retryable: false}
		}
		if storage.AllowExistingExactReplay {
			if discovery.StorageState == "storage_existing_exact" {
				return discovery
			}
			return Response{Ok: false, StorageState: "unknown", ErrorCode: "tencent_cbs_replay_unconfirmed", Message: "A reserved CBS create attempt cannot be repeated without exact disk readback.", ProviderRequestId: discovery.ProviderRequestId, Retryable: false}
		}
		if discovery.StorageState != "storage_not_started" {
			return Response{Ok: false, StorageState: "unknown", ErrorCode: "tencent_cbs_approval_drift", Message: "Tencent CBS state changed after approval.", ProviderRequestId: discovery.ProviderRequestId, Retryable: false}
		}
	case "storage_existing_exact":
		if discovery.StorageState != "storage_existing_exact" || !strings.HasPrefix(storage.ExpectedProviderResourceId, "disk-") ||
			discovery.StorageVolumeId != storage.ExpectedProviderResourceId {
			return Response{Ok: false, StorageState: "unknown", ErrorCode: "tencent_cbs_approval_drift", Message: "Tencent CBS identity changed after approval.", ProviderRequestId: discovery.ProviderRequestId, Retryable: false}
		}
		return discovery
	default:
		return Response{Ok: false, StorageState: "unknown", ErrorCode: "tencent_cbs_approval_invalid", Message: "Approved CBS state is invalid.", Retryable: false}
	}
	create := cbs2017.NewCreateDisksRequest()
	create.Placement = &cbs2017.Placement{Zone: common.StringPtr(storage.Zone)}
	create.DiskChargeType = common.StringPtr("PREPAID")
	create.DiskType = common.StringPtr(storage.DiskType)
	create.DiskName = common.StringPtr(storage.Id)
	create.Tags = cbsTags(request.Tags)
	create.DiskCount = common.Uint64Ptr(1)
	create.DiskSize = common.Uint64Ptr(storage.SizeGB)
	create.ClientToken = common.StringPtr("opl-cbs-" + stableSuffix(request.AccountId, storage.Id, request.Tags["opl_operation_id"]))
	create.DiskChargePrepaid = &cbs2017.DiskChargePrepaid{Period: common.Uint64Ptr(1), RenewFlag: common.StringPtr("NOTIFY_AND_MANUAL_RENEW")}
	created, err := client.nativeCbsClient.CreateDisks(create)
	if err != nil {
		response := sdkErrorResponse("tencent_create_cbs_failed", err)
		response.StorageState = "unknown"
		response.MutationCount = 1
		return response
	}
	if created == nil || created.Response == nil || len(created.Response.DiskIdSet) != 1 || !strings.HasPrefix(stringValue(created.Response.DiskIdSet[0]), "disk-") {
		return Response{Ok: false, StorageState: "unknown", ErrorCode: "tencent_create_cbs_identity_missing", Message: "Tencent CBS did not return exactly one disk identity.", Retryable: true, MutationCount: 1}
	}
	storage.Id = stringValue(created.Response.DiskIdSet[0])
	request.Storage = storage
	response := client.storageVolumeReadback(request, false)
	response.StorageVolumeId = storage.Id
	response.StorageState = "storage_existing_exact"
	if response.ProviderData == nil {
		response.ProviderData = map[string]string{}
	}
	response.ProviderData["createCbsRequestId"] = stringValue(created.Response.RequestId)
	response.ProviderRequestId = firstNonEmpty(stringValue(created.Response.RequestId), response.ProviderRequestId)
	response.MutationCount = 1
	return response
}

func (client *tencentSDKClient) DiscoverStorageVolume(request Request, _ map[string]string) Response {
	if client == nil || client.nativeCbsClient == nil {
		return Response{Ok: false, StorageState: "unknown", ErrorCode: "tencent_sdk_client_missing", Message: "Tencent CBS SDK client is missing.", Retryable: false}
	}
	storage := request.Storage
	storage.DiskType = firstNonEmpty(storage.DiskType, "CLOUD_BSSD")
	if strings.TrimSpace(request.AccountId) == "" || storage.Id == "" || storage.SizeGB == 0 || storage.Zone == "" ||
		!validCBSOwnershipTags(request.Tags) || request.Tags["opl_account_id"] != request.AccountId || request.Tags["opl_resource_id"] != storage.Id {
		return Response{Ok: false, StorageState: "unknown", ErrorCode: "tencent_cbs_input_invalid", Message: "Exact CBS account, resource, size, compute Zone, and ownership tags are required.", Retryable: false}
	}
	filters := make([]*cbs2017.Filter, 0, len(cbsOwnershipTagKeys))
	for _, key := range cbsOwnershipTagKeys {
		filters = append(filters, &cbs2017.Filter{Name: common.StringPtr("tag:" + key), Values: []*string{common.StringPtr(request.Tags[key])}})
	}
	exactDisks, exactRequestID, failure := client.describeRecoveryDisks(filters)
	if failure != nil {
		return *failure
	}
	if len(exactDisks) > 1 {
		return Response{Ok: false, StorageState: "unknown", ErrorCode: "tencent_cbs_multiple_candidate", Message: "Tencent CBS discovery must return at most one exact disk.", ProviderRequestId: exactRequestID, Retryable: false}
	}
	nameDisks, nameRequestID, failure := client.describeRecoveryDisks([]*cbs2017.Filter{{Name: common.StringPtr("disk-name"), Values: []*string{common.StringPtr(storage.Id)}}})
	if failure != nil {
		return *failure
	}
	requestID := firstNonEmpty(nameRequestID, exactRequestID)
	if len(exactDisks) == 0 && len(nameDisks) == 0 {
		return Response{Ok: true, StorageState: "storage_not_started", Status: "absent", ProviderRequestId: requestID, MutationCount: 0}
	}
	if len(nameDisks) > 1 {
		return Response{Ok: false, StorageState: "unknown", ErrorCode: "tencent_cbs_multiple_candidate", Message: "Tencent CBS discovery must return at most one logical disk.", ProviderRequestId: requestID, Retryable: false}
	}
	if len(exactDisks) != 1 || exactDisks[0] == nil || len(nameDisks) != 1 || nameDisks[0] == nil ||
		stringValue(exactDisks[0].DiskId) != stringValue(nameDisks[0].DiskId) {
		return Response{Ok: false, StorageState: "unknown", ErrorCode: "tencent_cbs_identity_mismatch", Message: "Tencent CBS name and ownership tags do not identify the same unique disk.", ProviderRequestId: requestID, Retryable: false}
	}
	disk := nameDisks[0]
	diskID := stringValue(disk.DiskId)
	exactStorage := storage
	exactStorage.Id = diskID
	facts, err := validateCBSVolume(disk, exactStorage, request.Tags)
	if err != nil || !oneMonthCBSPeriod(stringValue(disk.CreateTime), stringValue(disk.DeadlineTime)) {
		return Response{Ok: false, StorageState: "unknown", ErrorCode: "tencent_cbs_identity_mismatch", Message: "Tencent CBS billing or identity facts do not match the original launch.", ProviderRequestId: requestID, Retryable: false}
	}
	facts["periodMonths"] = "1"
	facts["describeCbsRequestId"] = requestID
	status := "pending"
	if state := strings.ToUpper(strings.TrimSpace(stringValue(disk.DiskState))); state == "UNATTACHED" || state == "ATTACHED" {
		status = "provider_ready"
	}
	return Response{
		Ok: true, StorageState: "storage_existing_exact", StorageVolumeId: diskID, CBSStatus: stringValue(disk.DiskState), Status: status,
		ProviderRequestId: requestID, ProviderData: facts, MutationCount: 0,
	}
}

func (client *tencentSDKClient) describeRecoveryDisks(filters []*cbs2017.Filter) ([]*cbs2017.Disk, string, *Response) {
	const pageLimit uint64 = 100
	disks := []*cbs2017.Disk{}
	requestID := ""
	totalCount := uint64(0)
	for offset := uint64(0); ; {
		describe := cbs2017.NewDescribeDisksRequest()
		describe.Filters = filters
		describe.Limit = common.Uint64Ptr(pageLimit)
		describe.Offset = common.Uint64Ptr(offset)
		result, err := client.nativeCbsClient.DescribeDisks(describe)
		if err != nil {
			response := sdkErrorResponse("tencent_describe_cbs_failed", err)
			response.StorageState = "unknown"
			response.MutationCount = 0
			return nil, requestID, &response
		}
		if result == nil || result.Response == nil || result.Response.TotalCount == nil || len(result.Response.DiskSet) > int(pageLimit) {
			response := Response{Ok: false, StorageState: "unknown", ErrorCode: "tencent_cbs_describe_incomplete", Message: "Tencent CBS DescribeDisks response is missing or incomplete.", ProviderRequestId: requestID, Retryable: true}
			return nil, requestID, &response
		}
		requestID = firstNonEmpty(stringValue(result.Response.RequestId), requestID)
		if offset == 0 {
			totalCount = *result.Response.TotalCount
		} else if *result.Response.TotalCount != totalCount {
			response := Response{Ok: false, StorageState: "unknown", ErrorCode: "tencent_cbs_describe_incomplete", Message: "Tencent CBS DescribeDisks total changed during pagination.", ProviderRequestId: requestID, Retryable: true}
			return nil, requestID, &response
		}
		disks = append(disks, result.Response.DiskSet...)
		if uint64(len(disks)) >= totalCount {
			break
		}
		if len(result.Response.DiskSet) == 0 {
			response := Response{Ok: false, StorageState: "unknown", ErrorCode: "tencent_cbs_describe_incomplete", Message: "Tencent CBS DescribeDisks pagination ended early.", ProviderRequestId: requestID, Retryable: true}
			return nil, requestID, &response
		}
		offset += uint64(len(result.Response.DiskSet))
	}
	if uint64(len(disks)) != totalCount {
		response := Response{Ok: false, StorageState: "unknown", ErrorCode: "tencent_cbs_describe_incomplete", Message: "Tencent CBS DescribeDisks result count is inconsistent.", ProviderRequestId: requestID, Retryable: true}
		return nil, requestID, &response
	}
	return disks, requestID, nil
}

func oneMonthCBSPeriod(created, deadline string) bool {
	createdAt, createdErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(created))
	deadlineAt, deadlineErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(deadline))
	return createdErr == nil && deadlineErr == nil && deadlineAt.Equal(createdAt.AddDate(0, 1, 0))
}

func (client *tencentSDKClient) SyncStorageVolume(request Request, _ map[string]string) Response {
	if client == nil || client.nativeCbsClient == nil {
		return Response{Ok: false, ErrorCode: "tencent_sdk_client_missing", Message: "Tencent CBS SDK client is missing.", Retryable: false}
	}
	return client.storageVolumeReadback(request, true)
}

func (client *tencentSDKClient) RenewStorageVolume(request Request, _ map[string]string) Response {
	if client == nil || client.nativeCbsClient == nil {
		return Response{Ok: false, ErrorCode: "tencent_sdk_client_missing", Message: "Tencent CBS SDK client is missing.", Retryable: false}
	}
	request.Storage.Deadline = normalizeTencentDeadline(request.Storage.Deadline)
	if request.Storage.Deadline == "" {
		return Response{Ok: false, ErrorCode: "tencent_cbs_deadline_required", Message: "The current CBS deadline is required for idempotent renewal.", Retryable: false}
	}
	current := client.storageVolumeReadback(request, true)
	if !current.Ok || current.Status == "external_deleted" {
		return current
	}
	if current.ProviderData["deadline"] != request.Storage.Deadline {
		if !deadlineAfter(current.ProviderData["deadline"], request.Storage.Deadline) {
			return Response{Ok: false, ErrorCode: "tencent_cbs_renewal_deadline_invalid", Message: "Tencent CBS renewal deadline did not increase.", ProviderRequestId: current.ProviderRequestId, ProviderData: current.ProviderData, Retryable: true}
		}
		current.ProviderData["renewalResult"] = "already_renewed"
		return current
	}
	renew := cbs2017.NewRenewDiskRequest()
	renew.DiskId = common.StringPtr(request.Storage.Id)
	renew.DiskChargePrepaid = &cbs2017.DiskChargePrepaid{Period: common.Uint64Ptr(1), RenewFlag: common.StringPtr("NOTIFY_AND_MANUAL_RENEW")}
	renewed, err := client.nativeCbsClient.RenewDisk(renew)
	if err != nil {
		return sdkErrorResponse("tencent_renew_cbs_failed", err)
	}
	if renewed == nil || renewed.Response == nil {
		return Response{Ok: false, ErrorCode: "tencent_renew_cbs_response_missing", Message: "Tencent CBS renewal response is missing.", Retryable: true}
	}
	response := client.storageVolumeReadback(request, false)
	if response.Ok && !deadlineAfter(response.ProviderData["deadline"], request.Storage.Deadline) {
		return Response{Ok: false, ErrorCode: "tencent_cbs_renewal_unconfirmed", Message: "Tencent CBS renewal deadline did not increase.", ProviderRequestId: stringValue(renewed.Response.RequestId), ProviderData: response.ProviderData, Retryable: true}
	}
	if response.ProviderData == nil {
		response.ProviderData = map[string]string{}
	}
	response.ProviderData["renewCbsRequestId"] = stringValue(renewed.Response.RequestId)
	response.ProviderData["renewalResult"] = "renewed"
	response.ProviderRequestId = firstNonEmpty(stringValue(renewed.Response.RequestId), response.ProviderRequestId)
	return response
}

func (client *tencentSDKClient) storageVolumeReadback(request Request, allowAbsent bool) Response {
	storage := request.Storage
	storage.DiskType = firstNonEmpty(storage.DiskType, "CLOUD_BSSD")
	if !validCBSReadbackInput(storage, request.Tags) {
		return Response{Ok: false, ErrorCode: "tencent_cbs_input_invalid", Message: "Exact CBS disk identity, size, compute Zone, and ownership tags are required.", Retryable: false}
	}
	describe := cbs2017.NewDescribeDisksRequest()
	describe.DiskIds = []*string{common.StringPtr(storage.Id)}
	result, err := client.nativeCbsClient.DescribeDisks(describe)
	if err != nil {
		if sdkErr, ok := err.(*tcerrors.TencentCloudSDKError); allowAbsent && ok && sdkErr.Code == cbs2017.INVALIDDISKID_NOTFOUND {
			return Response{Ok: true, StorageVolumeId: storage.Id, CBSStatus: "NOT_FOUND", Status: "external_deleted", ProviderRequestId: sdkErr.RequestId, ProviderData: map[string]string{"storageVolumeId": storage.Id, "cbsStatus": "NOT_FOUND", "describeCbsRequestId": sdkErr.RequestId}}
		}
		return sdkErrorResponse("tencent_describe_cbs_failed", err)
	}
	requestID := ""
	if result != nil && result.Response != nil {
		requestID = stringValue(result.Response.RequestId)
	}
	if allowAbsent && result != nil && result.Response != nil && result.Response.TotalCount != nil && *result.Response.TotalCount == 0 && len(result.Response.DiskSet) == 0 {
		return Response{Ok: true, StorageVolumeId: storage.Id, CBSStatus: "NOT_FOUND", Status: "external_deleted", ProviderRequestId: requestID, ProviderData: map[string]string{"storageVolumeId": storage.Id, "cbsStatus": "NOT_FOUND", "describeCbsRequestId": requestID}}
	}
	if result == nil || result.Response == nil || result.Response.TotalCount == nil || *result.Response.TotalCount != 1 || len(result.Response.DiskSet) != 1 || result.Response.DiskSet[0] == nil {
		return Response{Ok: false, ErrorCode: "tencent_cbs_readback_mismatch", Message: "Tencent CBS readback must return exactly one disk.", ProviderRequestId: requestID, Retryable: true}
	}
	disk := result.Response.DiskSet[0]
	facts, err := validateCBSVolume(disk, storage, request.Tags)
	if err != nil {
		return Response{Ok: false, ErrorCode: "tencent_cbs_readback_mismatch", Message: "Tencent CBS billing or identity facts do not match the requested volume.", ProviderRequestId: requestID, Retryable: true}
	}
	state := stringValue(disk.DiskState)
	facts["describeCbsRequestId"] = requestID
	status := "pending"
	if state == "UNATTACHED" || state == "ATTACHED" {
		status = "provider_ready"
	}
	return Response{
		Ok: true, StorageVolumeId: storage.Id, CBSStatus: state, Status: status, ProviderRequestId: requestID,
		ProviderData: facts,
	}
}

func validCBSReadbackInput(storage StorageInput, tags map[string]string) bool {
	if !strings.HasPrefix(storage.Id, "disk-") || storage.SizeGB == 0 || strings.TrimSpace(storage.Zone) == "" || strings.TrimSpace(storage.DiskType) == "" {
		return false
	}
	return validCBSOwnershipTags(tags)
}

func validCBSOwnershipTags(tags map[string]string) bool {
	for _, key := range cbsOwnershipTagKeys {
		if strings.TrimSpace(tags[key]) == "" {
			return false
		}
	}
	return true
}

func validateCBSVolume(disk *cbs2017.Disk, storage StorageInput, expectedTags map[string]string) (map[string]string, error) {
	if disk == nil || !validCBSReadbackInput(storage, expectedTags) {
		return nil, fmt.Errorf("CBS identity is missing")
	}
	zone := ""
	if disk.Placement != nil {
		zone = stringValue(disk.Placement.Zone)
	}
	deadline := normalizeTencentDeadline(stringValue(disk.DeadlineTime))
	actualTags := map[string]string{}
	for _, tag := range disk.Tags {
		if tag == nil || strings.TrimSpace(stringValue(tag.Key)) == "" {
			continue
		}
		key := stringValue(tag.Key)
		if _, duplicate := actualTags[key]; duplicate {
			return nil, fmt.Errorf("CBS ownership tag is duplicated")
		}
		actualTags[key] = stringValue(tag.Value)
	}
	if stringValue(disk.DiskId) != storage.Id || stringValue(disk.DiskName) != expectedTags["opl_resource_id"] || stringValue(disk.DiskUsage) != "DATA_DISK" ||
		strings.TrimSpace(stringValue(disk.DiskState)) == "" || stringValue(disk.DiskChargeType) != "PREPAID" ||
		stringValue(disk.RenewFlag) != "NOTIFY_AND_MANUAL_RENEW" || stringValue(disk.DiskType) != storage.DiskType ||
		disk.DiskSize == nil || *disk.DiskSize != storage.SizeGB || zone != storage.Zone || deadline == "" {
		return nil, fmt.Errorf("CBS billing or identity facts mismatch")
	}
	for _, key := range cbsOwnershipTagKeys {
		if actualTags[key] != expectedTags[key] {
			return nil, fmt.Errorf("CBS ownership tag mismatch")
		}
	}
	facts := map[string]string{
		"storageVolumeId": storage.Id, "cbsStatus": stringValue(disk.DiskState), "diskName": stringValue(disk.DiskName), "diskUsage": stringValue(disk.DiskUsage),
		"diskChargeType": stringValue(disk.DiskChargeType), "diskType": stringValue(disk.DiskType), "renewFlag": stringValue(disk.RenewFlag),
		"sizeGb": strconv.FormatUint(*disk.DiskSize, 10), "zone": zone, "deadline": deadline,
	}
	for _, key := range cbsOwnershipTagKeys {
		facts[key] = actualTags[key]
	}
	return facts, nil
}

func (client *tencentSDKClient) RenewComputeAllocation(request Request, _ map[string]string) Response {
	if client == nil || client.nativeCvmClient == nil {
		return Response{Ok: false, ErrorCode: "tencent_sdk_client_missing", Message: "Tencent CVM SDK client is missing.", Retryable: false}
	}
	allocation := request.Allocation
	allocation.Deadline = normalizeTencentDeadline(allocation.Deadline)
	request.Allocation = allocation
	if strings.TrimSpace(request.AccountId) == "" || strings.TrimSpace(allocation.Id) == "" || !strings.HasPrefix(allocation.InstanceId, "ins-") || allocation.Deadline == "" || strings.TrimSpace(request.Pool.InstanceType) == "" || strings.TrimSpace(request.Zone) == "" || !validOwnershipTags(request.Tags) || request.Tags["opl_account_id"] != request.AccountId || request.Tags["opl_resource_id"] != allocation.Id {
		return Response{Ok: false, ErrorCode: "tencent_cvm_renew_input_invalid", Message: "Exact compute resource, CVM instance, and previous deadline are required.", Retryable: false}
	}
	current := client.computeRenewalReadback(request)
	if !current.Ok || current.Status == "external_deleted" {
		return current
	}
	if current.ProviderData["deadline"] != allocation.Deadline {
		if !deadlineAfter(current.ProviderData["deadline"], allocation.Deadline) {
			return Response{Ok: false, ErrorCode: "tencent_cvm_renewal_deadline_invalid", Message: "Tencent CVM renewal deadline did not increase.", InstanceId: allocation.InstanceId, ProviderRequestId: current.ProviderRequestId, ProviderData: current.ProviderData, Retryable: true}
		}
		current.ProviderData["renewalResult"] = "already_renewed"
		return current
	}
	renew := cvm2017.NewRenewInstancesRequest()
	renew.InstanceIds = []*string{common.StringPtr(allocation.InstanceId)}
	renew.InstanceChargePrepaid = &cvm2017.InstanceChargePrepaid{Period: common.Int64Ptr(1), RenewFlag: common.StringPtr("NOTIFY_AND_MANUAL_RENEW")}
	renew.RenewPortableDataDisk = common.BoolPtr(false)
	renewed, err := client.nativeCvmClient.RenewInstances(renew)
	if err != nil {
		return sdkErrorResponse("tencent_renew_cvm_failed", err)
	}
	if renewed == nil || renewed.Response == nil {
		return Response{Ok: false, ErrorCode: "tencent_renew_cvm_response_missing", Message: "Tencent CVM renewal response is missing.", Retryable: true}
	}
	response := client.computeRenewalReadback(request)
	if !response.Ok || response.Status == "external_deleted" || !deadlineAfter(response.ProviderData["deadline"], allocation.Deadline) {
		return Response{
			Ok: false, ErrorCode: "tencent_cvm_renewal_unconfirmed", Message: "Tencent CVM renewal could not be confirmed by exact readback.",
			InstanceId: allocation.InstanceId, ProviderRequestId: stringValue(renewed.Response.RequestId), ProviderData: response.ProviderData, Retryable: true,
		}
	}
	response.ProviderData["renewCvmRequestId"] = stringValue(renewed.Response.RequestId)
	response.ProviderData["renewalResult"] = "renewed"
	response.ProviderRequestId = firstNonEmpty(stringValue(renewed.Response.RequestId), response.ProviderRequestId)
	return response
}

func (client *tencentSDKClient) computeRenewalReadback(request Request) Response {
	allocation := request.Allocation
	instance, requestID, err := client.describeCvmInstanceByID(allocation.InstanceId)
	if err != nil {
		response := sdkErrorResponse("tencent_describe_cvm_renewal_failed", err)
		response.ProviderRequestId = firstNonEmpty(response.ProviderRequestId, requestID)
		return response
	}
	if instance == nil {
		return Response{
			Ok: true, InstanceId: allocation.InstanceId, Status: "external_deleted", CVMStatus: "NOT_FOUND", ProviderRequestId: requestID,
			ProviderData: map[string]string{"instanceId": allocation.InstanceId, "cvmStatus": "NOT_FOUND", "describeCvmRequestId": requestID},
		}
	}
	zone := ""
	if instance.Placement != nil {
		zone = stringValue(instance.Placement.Zone)
	}
	deadline := normalizeTencentDeadline(stringValue(instance.ExpiredTime))
	actualTags, tagErr := cvmOwnershipTags(instance)
	if stringValue(instance.InstanceType) != request.Pool.InstanceType {
		return Response{
			Ok: false, ErrorCode: "compute_instance_type_mismatch", Message: "Tencent CVM instance type does not match the requested package.",
			InstanceId: allocation.InstanceId, ProviderRequestId: requestID, ProviderData: map[string]string{"instanceType": stringValue(instance.InstanceType)}, Retryable: true,
		}
	}
	if (request.Pool.CPU > 0 || request.Pool.MemoryGB > 0) && !cvmResourceShapeMatches(instance, request.Pool) {
		response := computeResourceShapeFailure(requestID)
		response.InstanceId = allocation.InstanceId
		return response
	}
	if stringValue(instance.InstanceId) != allocation.InstanceId || stringValue(instance.InstanceName) != allocation.Id ||
		stringValue(instance.InstanceChargeType) != "PREPAID" || stringValue(instance.RenewFlag) != "NOTIFY_AND_MANUAL_RENEW" ||
		deadline == "" || zone != request.Zone || tagErr != nil ||
		(strings.TrimSpace(allocation.PrivateIp) != "" && !containsString(instance.PrivateIpAddresses, allocation.PrivateIp)) {
		return Response{
			Ok: false, ErrorCode: "tencent_cvm_renewal_readback_mismatch", Message: "Tencent CVM billing, ownership, or deadline facts do not match the requested allocation.",
			InstanceId: allocation.InstanceId, ProviderRequestId: requestID, Retryable: true,
		}
	}
	for _, key := range cbsOwnershipTagKeys {
		if actualTags[key] != request.Tags[key] {
			return Response{Ok: false, ErrorCode: "tencent_cvm_renewal_readback_mismatch", Message: "Tencent CVM ownership tags do not match the requested allocation.", InstanceId: allocation.InstanceId, ProviderRequestId: requestID, Retryable: true}
		}
	}
	state := stringValue(instance.InstanceState)
	response := Response{
		Ok: true, InstanceId: allocation.InstanceId, Status: "provider_ready", CVMStatus: state, ProviderRequestId: requestID,
		ProviderData: map[string]string{
			"instanceId": allocation.InstanceId, "resourceId": allocation.Id, "cvmStatus": state,
			"instanceType": stringValue(instance.InstanceType),
			"chargeType":   stringValue(instance.InstanceChargeType), "renewFlag": stringValue(instance.RenewFlag),
			"deadline": deadline, "zone": zone, "privateIp": firstString(instance.PrivateIpAddresses), "describeCvmRequestId": requestID,
		},
	}
	if instance.CPU != nil && instance.Memory != nil {
		response.ProviderData["cpu"] = strconv.FormatInt(*instance.CPU, 10)
		response.ProviderData["memoryGb"] = strconv.FormatInt(*instance.Memory, 10)
	}
	for _, key := range cbsOwnershipTagKeys {
		response.ProviderData[key] = actualTags[key]
	}
	return response
}

func (client *tencentSDKClient) PrepareComputeAllocation(request Request, _ map[string]string) Response {
	if client == nil || client.nativeTkeClient == nil {
		return Response{Ok: false, ErrorCode: "tencent_sdk_client_missing", Message: "Tencent TKE SDK client is missing.", Retryable: false}
	}
	nodePoolId := strings.TrimSpace(request.Pool.NodePoolId)
	if nodePoolId == "" {
		return Response{Ok: false, ErrorCode: "compute_node_pool_id_required", Message: "An exact registered NodePool ID is required.", Retryable: false}
	}
	if request.Pool.MaxReplicas <= 0 {
		return Response{Ok: false, ErrorCode: "compute_allocation_plan_invalid", Message: "A configured NodePool maximum is required.", Retryable: false}
	}
	pool, describeRequestId, err := client.describeNativeNodePool(nodePoolId)
	if err != nil {
		return sdkErrorResponse("tencent_describe_node_pool_failed", err)
	}
	if failure := mutationNodePoolFailure(pool, request, describeRequestId); failure != nil {
		return *failure
	}
	currentReplicas := nativeReplicas(pool)
	beforeMachines, beforeMachinesRequestId, err := client.describeClusterMachines(nodePoolId)
	if err != nil {
		response := sdkErrorResponse("tencent_describe_cluster_machines_failed", err)
		response.ProviderRequestId = describeRequestId
		return response
	}
	if int64(len(beforeMachines)) != currentReplicas {
		return Response{Ok: false, ErrorCode: "compute_allocation_machine_inventory_incomplete", Message: "NodePool machine inventory is incomplete.", ProviderRequestId: beforeMachinesRequestId, Retryable: false}
	}
	machines := make([]MachineOutput, 0, len(beforeMachines))
	for _, machine := range beforeMachines {
		if machine == nil || strings.TrimSpace(stringValue(machine.MachineName)) == "" {
			return Response{Ok: false, ErrorCode: "compute_allocation_machine_inventory_incomplete", Message: "NodePool machine inventory contains an unknown machine.", ProviderRequestId: beforeMachinesRequestId, Retryable: false}
		}
		machines = append(machines, MachineOutput{MachineId: stringValue(machine.MachineName)})
	}
	return Response{Ok: true, PoolId: request.Pool.Id, NodePoolId: nodePoolId, Status: "prepared", ProviderRequestId: firstNonEmpty(beforeMachinesRequestId, describeRequestId), CurrentReplicas: currentReplicas, MaxReplicas: request.Pool.MaxReplicas, TargetReplicas: currentReplicas + 1, Machines: machines}
}

func (client *tencentSDKClient) CreateComputeAllocation(request Request, _ map[string]string) Response {
	return client.resolveComputeAllocation(request, true)
}

func (client *tencentSDKClient) ReadComputeAllocation(request Request, _ map[string]string) Response {
	return client.resolveComputeAllocation(request, false)
}

func (client *tencentSDKClient) resolveComputeAllocation(request Request, allowScale bool) Response {
	if client == nil || client.nativeTkeClient == nil || client.nativeCvmClient == nil || client.nativeVpcClient == nil {
		return Response{Ok: false, ErrorCode: "tencent_sdk_client_missing", Message: "Tencent TKE, CVM, and VPC SDK clients are required.", Retryable: false}
	}
	nodePoolId := strings.TrimSpace(request.Pool.NodePoolId)
	if nodePoolId == "" || request.Pool.CPU == 0 || request.Pool.MemoryGB == 0 || request.Pool.MaxReplicas <= 0 || request.Pool.BaselineReplicas < 0 || request.Pool.TargetReplicas != request.Pool.BaselineReplicas+1 || request.Pool.TargetReplicas > request.Pool.MaxReplicas || int64(len(request.Pool.BeforeMachineNames)) != request.Pool.BaselineReplicas {
		return Response{Ok: false, ErrorCode: "compute_allocation_plan_invalid", Message: "A persisted allocation plan with an exact NodePool ID is required.", Retryable: false}
	}
	pool, describeRequestId, err := client.describeNativeNodePool(nodePoolId)
	if err != nil {
		return sdkErrorResponse("tencent_describe_node_pool_failed", err)
	}
	if failure := mutationNodePoolFailure(pool, request, describeRequestId); failure != nil {
		return *failure
	}
	currentReplicas := nativeReplicas(pool)
	if currentReplicas < request.Pool.BaselineReplicas || currentReplicas > request.Pool.TargetReplicas {
		return Response{Ok: false, ErrorCode: "compute_allocation_replica_state_ambiguous", Message: "NodePool replicas do not match the persisted allocation plan.", ProviderRequestId: describeRequestId, Retryable: false}
	}
	scaleRequestId := ""
	if currentReplicas == request.Pool.BaselineReplicas {
		if !allowScale {
			return Response{Ok: false, ErrorCode: "compute_allocation_pending", Message: "The persisted absolute target has not been reached.", ProviderRequestId: describeRequestId, Retryable: true}
		}
		scaleRequest := tke2022.NewScaleNodePoolRequest()
		scaleRequest.ClusterId = common.StringPtr(client.clusterId)
		scaleRequest.NodePoolId = common.StringPtr(nodePoolId)
		scaleRequest.Replicas = common.Int64Ptr(request.Pool.TargetReplicas)
		scaleResponse, scaleErr := client.nativeTkeClient.ScaleNodePool(scaleRequest)
		if scaleErr != nil {
			return Response{Ok: false, ErrorCode: "tencent_scale_node_pool_result_unknown", Message: "Tencent ScaleNodePool result is unknown; read back the persisted absolute target.", ProviderRequestId: describeRequestId, Retryable: true}
		}
		if scaleResponse != nil && scaleResponse.Response != nil {
			scaleRequestId = stringValue(scaleResponse.Response.RequestId)
		}
	}
	afterMachines, machineRequestId, err := client.describeClusterMachines(nodePoolId)
	if err != nil {
		return sdkErrorResponse("tencent_describe_cluster_machines_failed", err)
	}
	machine, differenceCode := exactNewReadyMachine(afterMachines, request.Pool.BeforeMachineNames, request.Pool.InstanceType)
	if differenceCode != "" {
		return Response{Ok: false, ErrorCode: differenceCode, Message: differenceCode, ProviderRequestId: firstNonEmpty(machineRequestId, scaleRequestId, describeRequestId), Retryable: differenceCode == "compute_allocation_pending"}
	}
	machineName := stringValue(machine.MachineName)
	privateIp := stringValue(machine.LanIP)
	if !machineResourceShapeMatches(machine, request.Pool) {
		response := computeResourceShapeFailure(firstNonEmpty(machineRequestId, scaleRequestId, describeRequestId))
		return response
	}
	tkeInstance, tkeRequestID, err := client.describeNativeTkeClusterInstanceByMachineName(machineName, nodePoolId)
	if err != nil {
		response := sdkErrorResponse("tencent_describe_tke_instance_failed", err)
		response.ProviderRequestId = firstNonEmpty(tkeRequestID, machineRequestId, scaleRequestId, describeRequestId)
		response.Retryable = true
		return response
	}
	if failure := validateNativeTkeAllocationIdentity(tkeInstance, machine, request); failure != nil {
		failure.ProviderRequestId = firstNonEmpty(tkeRequestID, machineRequestId, scaleRequestId, describeRequestId)
		return *failure
	}
	nativeIdentity := tkeInstance.Native
	if !nativeResourceShapeMatches(nativeIdentity, request.Pool) {
		response := computeResourceShapeFailure(firstNonEmpty(tkeRequestID, machineRequestId, scaleRequestId, describeRequestId))
		return response
	}
	targetVpcID, subnetRequestID, err := client.describeNodePoolSubnetVpc(pool, stringValue(nativeIdentity.SubnetId))
	if err != nil {
		response := sdkErrorResponse("tencent_describe_allocation_subnet_failed", err)
		response.ProviderRequestId = firstNonEmpty(subnetRequestID, tkeRequestID, machineRequestId, scaleRequestId, describeRequestId)
		response.Retryable = true
		return response
	}
	if stringValue(nativeIdentity.VpcId) != targetVpcID {
		return Response{Ok: false, ErrorCode: "compute_tke_network_identity_mismatch", Message: "Tencent TKE native identity does not match the exact NodePool VPC and subnet.", ProviderRequestId: firstNonEmpty(subnetRequestID, tkeRequestID, machineRequestId, scaleRequestId, describeRequestId), Retryable: true}
	}
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
	if cvmInstance.VirtualPrivateCloud == nil || instanceId != stringValue(nativeIdentity.InstanceId) ||
		!containsString(cvmInstance.PrivateIpAddresses, privateIp) ||
		stringValue(cvmInstance.VirtualPrivateCloud.VpcId) != targetVpcID ||
		stringValue(cvmInstance.VirtualPrivateCloud.SubnetId) != stringValue(nativeIdentity.SubnetId) {
		return Response{Ok: false, ErrorCode: "compute_cvm_identity_mismatch", Message: "Tencent CVM identity does not match the exact TKE Machine, VPC, and subnet.", ProviderRequestId: firstNonEmpty(cvmRequestId, subnetRequestID, tkeRequestID, machineRequestId, scaleRequestId), Retryable: true}
	}
	if stringValue(cvmInstance.InstanceType) != request.Pool.InstanceType {
		return Response{Ok: false, ErrorCode: "compute_instance_type_mismatch", Message: "Tencent NodePool, Machine, Native instance, and CVM instance types must exactly match.", ProviderRequestId: firstNonEmpty(cvmRequestId, tkeRequestID, machineRequestId, scaleRequestId, describeRequestId), Retryable: true}
	}
	if !cvmResourceShapeMatches(cvmInstance, request.Pool) {
		response := computeResourceShapeFailure(firstNonEmpty(cvmRequestId, tkeRequestID, machineRequestId, scaleRequestId, describeRequestId))
		return response
	}
	if !strings.EqualFold(strings.TrimSpace(stringValue(cvmInstance.InstanceState)), "running") {
		return Response{Ok: false, ErrorCode: "compute_cvm_not_ready", Message: "Tencent CVM has not explicitly reported RUNNING.", ProviderRequestId: firstNonEmpty(cvmRequestId, subnetRequestID, tkeRequestID, machineRequestId, scaleRequestId), Retryable: true}
	}
	zone := ""
	if cvmInstance.Placement != nil {
		zone = stringValue(cvmInstance.Placement.Zone)
	}
	chargeType, renewFlag, deadline := stringValue(cvmInstance.InstanceChargeType), stringValue(cvmInstance.RenewFlag), normalizeTencentDeadline(stringValue(cvmInstance.ExpiredTime))
	if strings.TrimSpace(zone) == "" || chargeType != "PREPAID" || renewFlag != "NOTIFY_AND_MANUAL_RENEW" || strings.TrimSpace(deadline) == "" {
		return Response{Ok: false, ErrorCode: "compute_cvm_billing_facts_required", Message: "NativeCVM allocation did not return exact PREPAID manual-renew billing facts.", ProviderRequestId: firstNonEmpty(cvmRequestId, machineRequestId, scaleRequestId), Retryable: true}
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
		OperationId:       "op-create-compute-" + stableSuffix(request.AccountId, request.Allocation.Id, nodePoolId, fmt.Sprintf("%d", request.Pool.TargetReplicas))[:12],
		PoolId:            request.Pool.Id,
		NodePoolId:        nodePoolId,
		InstanceId:        instanceId,
		NodeName:          nodeName,
		PrivateIp:         privateIp,
		PublicIp:          publicIp,
		Status:            "running",
		ProviderRequestId: firstNonEmpty(scaleRequestId, machineRequestId, describeRequestId),
		ProviderData: map[string]string{
			"clusterId":                  client.clusterId,
			"region":                     client.region,
			"describeNodePoolRequestId":  describeRequestId,
			"describeMachinesReadyReqId": machineRequestId,
			"describeCvmRequestId":       cvmRequestId,
			"describeTkeInstanceReqId":   tkeRequestID,
			"describeSubnetRequestId":    subnetRequestID,
			"scaleNodePoolRequestId":     scaleRequestId,
			"instanceType":               request.Pool.InstanceType,
			"cpu":                        strconv.FormatUint(request.Pool.CPU, 10),
			"memoryGb":                   strconv.FormatUint(request.Pool.MemoryGB, 10),
			"replicasBefore":             fmt.Sprintf("%d", request.Pool.BaselineReplicas),
			"replicasAfter":              fmt.Sprintf("%d", request.Pool.TargetReplicas),
			"instanceId":                 instanceId,
			"instanceIdentitySource":     instanceIdentitySource,
			"machineName":                machineName,
			"machineState":               strings.ToLower(strings.TrimSpace(stringValue(machine.MachineState))),
			"tkeInstanceState":           strings.ToLower(strings.TrimSpace(stringValue(tkeInstance.InstanceState))),
			"nodeName":                   nodeName,
			"privateIp":                  privateIp,
			"publicIp":                   publicIp,
			"zone":                       zone,
			"vpcId":                      targetVpcID,
			"subnetId":                   stringValue(nativeIdentity.SubnetId),
			"chargeType":                 chargeType,
			"periodMonths":               "1",
			"renewFlag":                  renewFlag,
			"deadline":                   deadline,
		},
	}
}

func (client *tencentSDKClient) ComputeClaimTruth(request Request, _ map[string]string) Response {
	if client == nil || client.nativeTkeClient == nil || client.nativeCvmClient == nil || client.nativeVpcClient == nil {
		return computeClaimTruthFailure("provider_describe")
	}
	expectedCPU, expectedMemoryGB, knownPackage := customerPackageResourceShape(request.PackageId)
	if !knownPackage || request.Pool.CPU != expectedCPU || request.Pool.MemoryGB != expectedMemoryGB ||
		strings.TrimSpace(request.AccountId) == "" || strings.TrimSpace(request.Zone) == "" || strings.TrimSpace(request.Pool.Id) == "" ||
		strings.TrimSpace(request.Pool.NodePoolId) == "" || strings.TrimSpace(request.Pool.InstanceType) == "" || request.Pool.MaxReplicas <= 0 ||
		request.Pool.BaselineReplicas < 0 || request.Pool.TargetReplicas != request.Pool.BaselineReplicas+1 ||
		int64(len(request.Pool.BeforeMachineNames)) != request.Pool.BaselineReplicas || strings.TrimSpace(request.Allocation.Id) == "" ||
		!strings.HasPrefix(request.Allocation.InstanceId, "ins-") || strings.TrimSpace(request.Allocation.MachineName) == "" ||
		strings.TrimSpace(request.Allocation.NodeName) == "" || strings.TrimSpace(request.Allocation.PrivateIp) == "" ||
		strings.TrimSpace(request.Allocation.Deadline) == "" || !validOwnershipTags(request.Tags) ||
		request.Tags["opl_account_id"] != request.AccountId || request.Tags["opl_resource_id"] != request.Allocation.Id {
		return computeClaimTruthFailure("identity_mismatch")
	}
	pool, _, err := client.describeNativeNodePool(request.Pool.NodePoolId)
	if err != nil {
		return computeClaimTruthFailure(computeClaimDescribeReason(err))
	}
	machines, _, err := client.describeClusterMachines(request.Pool.NodePoolId)
	if err != nil {
		return computeClaimTruthFailure(computeClaimDescribeReason(err))
	}
	machine, differenceCode := exactNewReadyMachine(machines, request.Pool.BeforeMachineNames, request.Pool.InstanceType)
	if differenceCode != "" {
		if differenceCode == "compute_allocation_machine_difference_ambiguous" {
			return computeClaimTruthFailure("multiple_candidate")
		}
		return computeClaimTruthFailure("identity_mismatch")
	}
	if !computeClaimNodePoolMatches(pool, request) {
		return computeClaimTruthFailure("identity_mismatch")
	}
	machineName, privateIP := stringValue(machine.MachineName), stringValue(machine.LanIP)
	if machineName != request.Allocation.MachineName || kubernetesNodeName(machine) != request.Allocation.NodeName || privateIP != request.Allocation.PrivateIp ||
		!machineResourceShapeMatches(machine, request.Pool) {
		return computeClaimTruthFailure("identity_mismatch")
	}
	tkeInstance, _, err := client.describeNativeTkeClusterInstanceByMachineName(machineName, request.Pool.NodePoolId)
	if err != nil {
		return computeClaimTruthFailure(computeClaimDescribeReason(err))
	}
	if validateNativeTkeAllocationIdentity(tkeInstance, machine, request) != nil || !nativeResourceShapeMatches(tkeInstance.Native, request.Pool) {
		return computeClaimTruthFailure("identity_mismatch")
	}
	targetVPCID, _, err := client.describeNodePoolSubnetVpc(pool, stringValue(tkeInstance.Native.SubnetId))
	if err != nil {
		return computeClaimTruthFailure(computeClaimDescribeReason(err))
	}
	if stringValue(tkeInstance.Native.VpcId) != targetVPCID {
		return computeClaimTruthFailure("identity_mismatch")
	}
	cvmInstance, _, err := client.describeCvmInstanceByPrivateIp(privateIP)
	if err != nil {
		return computeClaimTruthFailure(computeClaimDescribeReason(err))
	}
	if cvmInstance == nil || cvmInstance.VirtualPrivateCloud == nil || stringValue(cvmInstance.InstanceId) != request.Allocation.InstanceId ||
		!containsString(cvmInstance.PrivateIpAddresses, privateIP) || stringValue(cvmInstance.VirtualPrivateCloud.VpcId) != targetVPCID ||
		stringValue(cvmInstance.VirtualPrivateCloud.SubnetId) != stringValue(tkeInstance.Native.SubnetId) ||
		stringValue(cvmInstance.InstanceType) != request.Pool.InstanceType || !cvmResourceShapeMatches(cvmInstance, request.Pool) ||
		!strings.EqualFold(strings.TrimSpace(stringValue(cvmInstance.InstanceState)), "running") {
		return computeClaimTruthFailure("identity_mismatch")
	}
	zone := ""
	if cvmInstance.Placement != nil {
		zone = stringValue(cvmInstance.Placement.Zone)
	}
	deadline := normalizeTencentDeadline(stringValue(cvmInstance.ExpiredTime))
	if zone != request.Zone || stringValue(cvmInstance.InstanceChargeType) != "PREPAID" ||
		stringValue(cvmInstance.RenewFlag) != "NOTIFY_AND_MANUAL_RENEW" || deadline == "" || deadline != request.Allocation.Deadline {
		return computeClaimTruthFailure("identity_mismatch")
	}
	ownershipState, ok := computeClaimCVMOwnershipState(cvmInstance, machineName, request)
	if !ok {
		return computeClaimTruthFailure("identity_mismatch")
	}
	return Response{
		Ok: true, Status: "proven", PoolId: request.Pool.Id, NodePoolId: request.Pool.NodePoolId,
		InstanceId: request.Allocation.InstanceId, NodeName: request.Allocation.NodeName, PrivateIp: privateIP,
		PublicIp: firstString(cvmInstance.PublicIpAddresses), InstanceType: request.Pool.InstanceType, CVMStatus: "RUNNING", TKEStatus: "RUNNING",
		ProviderData: map[string]string{
			"machineName": machineName, "zone": zone, "chargeType": "PREPAID", "periodMonths": "1",
			"renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": deadline, "cvmOwnershipState": ownershipState,
		},
	}
}

func computeClaimNodePoolMatches(pool *tke2022.NodePool, request Request) bool {
	return pool != nil && pool.Native != nil && pool.Native.Scaling != nil && isCVMNativeNodePool(pool) &&
		strings.EqualFold(strings.TrimSpace(stringValue(pool.LifeState)), "running") && matchesCapacityNodePool(pool, request) &&
		pool.Native.Replicas != nil && *pool.Native.Replicas == request.Pool.TargetReplicas &&
		pool.Native.ReadyReplicas != nil && *pool.Native.ReadyReplicas == request.Pool.TargetReplicas &&
		pool.Native.Scaling.MinReplicas != nil && *pool.Native.Scaling.MinReplicas == 0 && pool.Native.Scaling.MaxReplicas != nil &&
		*pool.Native.Scaling.MaxReplicas == request.Pool.MaxReplicas && pool.Native.EnableAutoscaling != nil && !*pool.Native.EnableAutoscaling &&
		pool.Native.AutoRepair != nil && !*pool.Native.AutoRepair && len(pool.Native.InstanceTypes) == 1 &&
		stringValue(pool.Native.InstanceTypes[0]) == request.Pool.InstanceType && len(pool.Native.SubnetIds) > 0
}

func computeClaimCVMOwnershipState(instance *cvm2017.Instance, machineName string, request Request) (string, bool) {
	tags, err := cvmOwnershipTags(instance)
	if err != nil {
		return "", false
	}
	targetCount := 0
	for _, key := range cbsOwnershipTagKeys {
		actual := strings.TrimSpace(tags[key])
		if actual == "" {
			continue
		}
		if actual != request.Tags[key] {
			return "", false
		}
		targetCount++
	}
	instanceName := strings.TrimSpace(stringValue(instance.InstanceName))
	if instanceName != "" && instanceName != machineName && instanceName != request.Allocation.Id {
		return "", false
	}
	if instanceName == request.Allocation.Id && targetCount == len(cbsOwnershipTagKeys) {
		return "target_owned", true
	}
	return "recoverable", true
}

type cvmOwnershipTarget struct {
	InstanceID  string
	CurrentName string
	TargetName  string
	Tags        map[string]string
}

type cvmOwnershipConvergence struct {
	Attempted       int
	Confirmed       int
	Unknown         int
	ProviderData    map[string]string
	ProviderRequest string
}

const cvmOwnershipSnapshotField = "cvm_ownership_snapshot"

func (client *tencentSDKClient) convergeCVMOwnership(target cvmOwnershipTarget) (cvmOwnershipConvergence, *Response) {
	result := cvmOwnershipConvergence{ProviderData: map[string]string{}}
	if client == nil || client.nativeCvmClient == nil {
		failure := computeClaimConvergenceFailure("provider_describe", "cvm_pre_read", "client_unavailable", result, nil)
		return result, &failure
	}
	instance, requestID, err := client.describeCvmInstanceByID(target.InstanceID)
	result.ProviderRequest = requestID
	if err != nil || instance == nil || stringValue(instance.InstanceId) != target.InstanceID {
		missing := []string(nil)
		if err == nil {
			missing = []string{"instance"}
		}
		failure := computeClaimConvergenceFailure(computeClaimDescribeReason(err), "cvm_pre_read", computeClaimProviderErrorClass(err), result, missing)
		return result, &failure
	}
	states, tags, conflict, err := classifyCVMOwnership(instance, target)
	if err != nil {
		failure := computeClaimConvergenceFailure("identity_mismatch", "cvm_pre_read", "malformed_readback", result, nil)
		return result, &failure
	}
	currentName := strings.TrimSpace(stringValue(instance.InstanceName))
	if target.CurrentName == "" {
		target.CurrentName = currentName
		states, tags, conflict, err = classifyCVMOwnership(instance, target)
		if err != nil {
			failure := computeClaimConvergenceFailure("identity_mismatch", "cvm_pre_read", "malformed_readback", result, nil)
			return result, &failure
		}
	}
	if conflict != "" {
		failure := computeClaimConvergenceFailure("identity_mismatch", "cvm_conflict_check", "ownership_conflict", result, []string{conflict})
		return result, &failure
	}
	missingTags := []string{}
	for _, key := range cbsOwnershipTagKeys {
		if states[key] == "missing" {
			missingTags = append(missingTags, key)
		}
	}
	if len(missingTags) > 0 && client.nativeTagClient == nil {
		failure := computeClaimConvergenceFailure("provider_describe", "cvm_mutation_precondition", "client_unavailable", result, missingTags)
		return result, &failure
	}
	fields := append([]string{"instance_name"}, cbsOwnershipTagKeys[:]...)
	for _, field := range fields {
		if states[field] == "target_owned" {
			continue
		}
		var mutationErr error
		result.Attempted++
		stage := "cvm_tag_readback"
		if field == "instance_name" {
			stage = "cvm_rename_readback"
			modify := cvm2017.NewModifyInstancesAttributeRequest()
			modify.InstanceIds = []*string{common.StringPtr(target.InstanceID)}
			modify.InstanceName = common.StringPtr(target.TargetName)
			modified, modifyErr := client.nativeCvmClient.ModifyInstancesAttribute(modify)
			mutationErr = modifyErr
			if modified != nil && modified.Response != nil {
				result.ProviderData["modifyInstanceRequestId"] = stringValue(modified.Response.RequestId)
			}
		} else {
			_, attached := tags[field]
			tagRequestID, tagErr := client.nativeTagClient.SetCVMTag(target.InstanceID, field, target.Tags[field], attached)
			mutationErr = tagErr
			if tagRequestID != "" {
				result.ProviderData["tagRequestId:"+field] = tagRequestID
			}
		}
		state, readbackStates, readbackTags, readbackRequestID, readbackErr := client.readCVMOwnershipField(target, field)
		result.ProviderRequest = firstNonEmpty(readbackRequestID, result.ProviderRequest)
		if state == "target_owned" {
			result.Confirmed++
			states, tags = readbackStates, readbackTags
			continue
		}
		if state == "unknown" {
			result.Unknown++
		}
		reason, providerClass := "provider_describe", "readback_mismatch"
		failureErr := readbackErr
		if mutationErr != nil {
			failureErr = mutationErr
		}
		if state == "conflict" {
			reason, providerClass = "identity_mismatch", "ownership_conflict"
		} else if failureErr != nil {
			reason, providerClass = computeClaimDescribeReason(failureErr), computeClaimProviderErrorClass(failureErr)
		}
		failure := computeClaimConvergenceFailure(reason, stage, providerClass, result, []string{field})
		return result, &failure
	}
	state, finalStates, finalTags, readbackRequestID, readbackErr := client.readCVMOwnershipField(target, cvmOwnershipSnapshotField)
	result.ProviderRequest = firstNonEmpty(readbackRequestID, result.ProviderRequest)
	if state != "target_owned" {
		reason, providerClass := "provider_describe", "readback_mismatch"
		if state == "unknown" {
			result.Unknown, result.Confirmed = result.Attempted, 0
		}
		if state == "conflict" {
			reason, providerClass = "identity_mismatch", "ownership_conflict"
		} else if readbackErr != nil {
			reason, providerClass = computeClaimDescribeReason(readbackErr), computeClaimProviderErrorClass(readbackErr)
		}
		failure := computeClaimConvergenceFailure(reason, "cvm_final_readback", providerClass, result, missingCVMOwnershipFields(finalStates))
		return result, &failure
	}
	tags = finalTags
	for _, key := range cbsOwnershipTagKeys {
		result.ProviderData[key] = tags[key]
	}
	return result, nil
}

func (client *tencentSDKClient) readCVMOwnershipField(target cvmOwnershipTarget, field string) (string, map[string]string, map[string]string, string, error) {
	ctx := client.convergenceContext
	if ctx == nil {
		ctx = context.Background()
	}
	var requestID string
	lastState := "unknown"
	var lastErr error
	var lastStates map[string]string
	var lastTags map[string]string
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 && client.convergenceWait != nil {
			if err := client.convergenceWait(ctx, attempt); err != nil {
				return "unknown", nil, nil, requestID, err
			}
		}
		instance, currentRequestID, err := client.describeCvmInstanceByID(target.InstanceID)
		requestID = firstNonEmpty(currentRequestID, requestID)
		if err != nil || instance == nil || stringValue(instance.InstanceId) != target.InstanceID {
			lastState, lastErr = "unknown", err
			if lastErr == nil {
				lastErr = fmt.Errorf("Tencent CVM readback instance is missing")
			}
			continue
		}
		states, tags, conflict, classifyErr := classifyCVMOwnership(instance, target)
		if classifyErr != nil {
			return "conflict", nil, nil, requestID, classifyErr
		}
		if conflict != "" {
			return "conflict", states, tags, requestID, nil
		}
		lastStates, lastTags, lastErr = states, tags, nil
		lastState = cvmOwnershipReadbackState(states, field)
		if lastState == "target_owned" {
			return lastState, states, tags, requestID, nil
		}
	}
	return lastState, lastStates, lastTags, requestID, lastErr
}

func cvmOwnershipReadbackState(states map[string]string, field string) string {
	if field != cvmOwnershipSnapshotField {
		return states[field]
	}
	if len(missingCVMOwnershipFields(states)) == 0 {
		return "target_owned"
	}
	return "missing"
}

func missingCVMOwnershipFields(states map[string]string) []string {
	fields := append([]string{"instance_name"}, cbsOwnershipTagKeys[:]...)
	missing := make([]string, 0, len(fields))
	for _, field := range fields {
		if states[field] != "target_owned" {
			missing = append(missing, field)
		}
	}
	return missing
}

func classifyCVMOwnership(instance *cvm2017.Instance, target cvmOwnershipTarget) (map[string]string, map[string]string, string, error) {
	states := map[string]string{}
	if instance == nil || stringValue(instance.InstanceId) != target.InstanceID {
		return states, nil, "instance", fmt.Errorf("Tencent CVM instance identity is missing")
	}
	tags, err := cvmOwnershipTags(instance)
	if err != nil {
		return states, nil, "ownership_tags", err
	}
	actualName := strings.TrimSpace(stringValue(instance.InstanceName))
	switch {
	case actualName == target.TargetName:
		states["instance_name"] = "target_owned"
	case actualName == "" || actualName == target.CurrentName:
		states["instance_name"] = "missing"
	default:
		return states, tags, "instance_name", nil
	}
	for legacyKey, canonicalKey := range cvmOwnershipTagAliases {
		actual := strings.TrimSpace(tags[legacyKey])
		if actual != "" && actual != target.Tags[canonicalKey] {
			return states, tags, legacyKey, nil
		}
	}
	for _, key := range cbsOwnershipTagKeys {
		actual := strings.TrimSpace(tags[key])
		switch {
		case actual == target.Tags[key]:
			states[key] = "target_owned"
		case actual == "":
			states[key] = "missing"
		default:
			return states, tags, key, nil
		}
	}
	return states, tags, "", nil
}

func computeClaimProviderErrorClass(err error) string {
	if err == nil {
		return "readback_mismatch"
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "timeout"
	}
	if computeClaimDescribeReason(err) == "iam_rbac" {
		return "iam_rbac"
	}
	return "provider_error"
}

func computeClaimConvergenceFailure(reason, stage, providerClass string, evidence cvmOwnershipConvergence, missing []string) Response {
	response := computeClaimTruthFailure(reason)
	response.MutationCount = evidence.Attempted
	response.FailureStage = stage
	response.ProviderErrorClass = providerClass
	response.ProviderRequestId = evidence.ProviderRequest
	response.MutationEvidence = &MutationEvidence{
		Attempted: evidence.Attempted,
		Confirmed: evidence.Confirmed,
		Unknown:   evidence.Unknown,
		Missing:   append([]string(nil), missing...),
	}
	return response
}

func computeClaimDescribeReason(err error) string {
	if err == nil {
		return "provider_describe"
	}
	value := strings.ToLower(err.Error())
	if sdkErr, ok := err.(*tcerrors.TencentCloudSDKError); ok {
		value += " " + strings.ToLower(sdkErr.Code)
	}
	for _, marker := range []string{"authfailure", "unauthorized", "forbidden", "permission", "accessdenied", "invalidcredential", "secretid"} {
		if strings.Contains(value, marker) {
			return "iam_rbac"
		}
	}
	return "provider_describe"
}

func computeClaimTruthFailure(reason string) Response {
	switch reason {
	case "provider_describe", "iam_rbac", "multiple_candidate", "identity_mismatch":
	default:
		reason = "provider_describe"
	}
	return Response{Ok: false, ErrorCode: reason, Message: "Compute claim truth could not be proven.", Retryable: false}
}

func (client *tencentSDKClient) ClaimComputeMachine(request Request, _ map[string]string) Response {
	truth := client.ComputeClaimTruth(request, nil)
	if !truth.Ok {
		return truth
	}
	converged, failure := client.convergeCVMOwnership(cvmOwnershipTarget{
		InstanceID: request.Allocation.InstanceId, CurrentName: request.Allocation.MachineName,
		TargetName: request.Allocation.Id, Tags: request.Tags,
	})
	if failure != nil {
		return *failure
	}
	converged.ProviderData["cvmOwnershipState"] = "target_owned"
	return Response{
		Ok: true, Status: "claimed", InstanceId: request.Allocation.InstanceId, ProviderRequestId: converged.ProviderRequest,
		MutationCount: converged.Attempted, ProviderData: converged.ProviderData,
		MutationEvidence: &MutationEvidence{Attempted: converged.Attempted, Confirmed: converged.Confirmed},
	}
}

func exactNewReadyMachine(after []*tke2022.Machine, before []string, instanceType string) (*tke2022.Machine, string) {
	beforeSet := map[string]bool{}
	for _, name := range before {
		name = strings.TrimSpace(name)
		if name == "" || beforeSet[name] {
			return nil, "compute_allocation_plan_invalid"
		}
		beforeSet[name] = true
	}
	seenBefore := map[string]bool{}
	newMachines := []*tke2022.Machine{}
	for _, machine := range after {
		if machine == nil || strings.TrimSpace(stringValue(machine.MachineName)) == "" {
			return nil, "compute_allocation_machine_difference_incomplete"
		}
		name := stringValue(machine.MachineName)
		if beforeSet[name] {
			seenBefore[name] = true
			continue
		}
		newMachines = append(newMachines, machine)
	}
	if len(seenBefore) != len(beforeSet) {
		return nil, "compute_allocation_machine_difference_incomplete"
	}
	if len(newMachines) == 0 {
		return nil, "compute_allocation_pending"
	}
	if len(newMachines) != 1 {
		return nil, "compute_allocation_machine_difference_ambiguous"
	}
	machine := newMachines[0]
	state := strings.ToLower(strings.TrimSpace(stringValue(machine.MachineState)))
	if !explicitReadyState(state) {
		return nil, "compute_allocation_pending"
	}
	if stringValue(machine.InstanceType) != instanceType {
		return nil, "compute_allocation_machine_identity_mismatch"
	}
	return machine, ""
}

func explicitReadyState(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "ready", "running":
		return true
	default:
		return false
	}
}

func validateNativeTkeAllocationIdentity(instance *tke2022.Instance, machine *tke2022.Machine, request Request) *Response {
	machineName := stringValue(machine.MachineName)
	privateIP := stringValue(machine.LanIP)
	if instance == nil {
		return &Response{Ok: false, ErrorCode: "compute_tke_identity_mismatch", Message: "Tencent TKE instance does not match the exact Ready NativeCVM Machine identity.", Retryable: true}
	}
	native := instance.Native
	valid := instance != nil && native != nil && stringValue(instance.InstanceId) == machineName &&
		stringValue(instance.NodePoolId) == request.Pool.NodePoolId && strings.EqualFold(stringValue(instance.NodeType), "Native") &&
		strings.EqualFold(strings.TrimSpace(stringValue(instance.InstanceState)), "running") && stringValue(instance.LanIP) == privateIP &&
		stringValue(native.MachineName) == machineName && explicitReadyState(stringValue(native.MachineState)) &&
		stringValue(native.LanIp) == privateIP && stringValue(native.InstanceType) == request.Pool.InstanceType &&
		strings.EqualFold(stringValue(native.MachineType), "NativeCVM") && strings.HasPrefix(stringValue(native.InstanceId), "ins-") &&
		strings.TrimSpace(stringValue(native.VpcId)) != "" && strings.TrimSpace(stringValue(native.SubnetId)) != ""
	if valid {
		return nil
	}
	return &Response{Ok: false, ErrorCode: "compute_tke_identity_mismatch", Message: "Tencent TKE instance does not match the exact Ready NativeCVM Machine identity.", Retryable: true}
}

func (client *tencentSDKClient) describeNodePoolSubnetVpc(pool *tke2022.NodePool, subnetID string) (string, string, error) {
	if client == nil || client.nativeVpcClient == nil || pool == nil || pool.Native == nil || strings.TrimSpace(subnetID) == "" {
		return "", "", fmt.Errorf("exact NodePool subnet identity is required")
	}
	allowed := false
	seen := map[string]bool{}
	for _, raw := range pool.Native.SubnetIds {
		candidate := strings.TrimSpace(stringValue(raw))
		if candidate == "" || seen[candidate] {
			return "", "", fmt.Errorf("NodePool subnet inventory is invalid")
		}
		seen[candidate] = true
		allowed = allowed || candidate == subnetID
	}
	if !allowed {
		return "", "", fmt.Errorf("native instance subnet is not registered on the exact NodePool")
	}
	request := vpc2017.NewDescribeSubnetsRequest()
	request.SubnetIds = []*string{common.StringPtr(subnetID)}
	response, err := client.nativeVpcClient.DescribeSubnets(request)
	if err != nil {
		return "", "", err
	}
	if response == nil || response.Response == nil {
		return "", "", fmt.Errorf("Tencent VPC DescribeSubnets response is missing")
	}
	requestID := stringValue(response.Response.RequestId)
	if response.Response.TotalCount == nil || *response.Response.TotalCount != 1 || len(response.Response.SubnetSet) != 1 || response.Response.SubnetSet[0] == nil ||
		stringValue(response.Response.SubnetSet[0].SubnetId) != subnetID || strings.TrimSpace(stringValue(response.Response.SubnetSet[0].VpcId)) == "" {
		return "", requestID, fmt.Errorf("Tencent VPC subnet identity is missing or ambiguous")
	}
	return stringValue(response.Response.SubnetSet[0].VpcId), requestID, nil
}

func (client *tencentSDKClient) TagComputeMachine(request Request, _ map[string]string) Response {
	if client == nil || client.nativeCvmClient == nil {
		return Response{Ok: false, ErrorCode: "tencent_sdk_client_missing", Message: "Tencent CVM SDK client is missing.", Retryable: false}
	}
	instanceID := strings.TrimSpace(request.Allocation.InstanceId)
	resourceID := strings.TrimSpace(request.Tags["opl_resource_id"])
	if instanceID == "" || resourceID == "" || len(resourceID) > 60 || !validOwnershipTags(request.Tags) {
		return Response{Ok: false, ErrorCode: "compute_machine_identity_required", Message: "Machine instance id and a resource id of at most 60 characters are required.", Retryable: false}
	}
	if !strings.HasPrefix(instanceID, "ins-") {
		return Response{Ok: false, ErrorCode: "compute_machine_identity_unverified", Message: "Tencent machine identity source is unsupported.", Retryable: false}
	}
	converged, failure := client.convergeCVMOwnership(cvmOwnershipTarget{
		InstanceID: instanceID, CurrentName: request.Allocation.MachineName,
		TargetName: resourceID, Tags: request.Tags,
	})
	if failure != nil {
		return tagComputeMachineFailure(*failure)
	}
	return Response{
		Ok: true, InstanceId: instanceID, Status: "tagged", ProviderRequestId: converged.ProviderRequest,
		ProviderData: converged.ProviderData, MutationCount: converged.Attempted,
		MutationEvidence: &MutationEvidence{Attempted: converged.Attempted, Confirmed: converged.Confirmed},
	}
}

func tagComputeMachineFailure(response Response) Response {
	switch response.FailureStage {
	case "cvm_pre_read":
		if response.MutationEvidence != nil && len(response.MutationEvidence.Missing) == 1 && response.MutationEvidence.Missing[0] == "instance" {
			response.ErrorCode = "compute_machine_identity_unverified"
		} else {
			response.ErrorCode = "tencent_verify_compute_machine_failed"
		}
	case "cvm_conflict_check":
		response.ErrorCode = "compute_machine_tag_unverified"
	case "cvm_mutation_precondition":
		response.ErrorCode = "tencent_sdk_client_missing"
	case "cvm_final_readback":
		response.ErrorCode = "tencent_verify_compute_machine_tag_failed"
	case "cvm_rename_readback", "cvm_tag_readback":
		if response.MutationEvidence != nil && response.MutationEvidence.Unknown > 0 {
			response.ErrorCode = "tencent_verify_compute_machine_tag_failed"
		} else {
			response.ErrorCode = "tencent_tag_compute_machine_failed"
		}
	default:
		response.ErrorCode = "tencent_tag_compute_machine_failed"
	}
	response.Message = "Tencent CVM ownership convergence could not be confirmed."
	response.Retryable = response.ErrorCode != "tencent_sdk_client_missing"
	return response
}

func validOwnershipTags(tags map[string]string) bool {
	for _, key := range cbsOwnershipTagKeys {
		if strings.TrimSpace(tags[key]) == "" {
			return false
		}
	}
	return true
}

func cvmOwnershipTags(instance *cvm2017.Instance) (map[string]string, error) {
	tags := map[string]string{}
	if instance == nil {
		return tags, fmt.Errorf("Tencent CVM instance is missing")
	}
	for _, tag := range instance.Tags {
		if tag == nil {
			continue
		}
		key := strings.TrimSpace(stringValue(tag.Key))
		if key == "" {
			continue
		}
		if _, duplicate := tags[key]; duplicate {
			return nil, fmt.Errorf("Tencent CVM ownership tag is duplicated")
		}
		tags[key] = stringValue(tag.Value)
	}
	return tags, nil
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
	if client == nil || client.nativeTkeClient == nil || client.nativeCvmClient == nil {
		return Response{Ok: false, ErrorCode: "tencent_sdk_client_missing", Message: "Tencent TKE and CVM SDK clients are required.", Retryable: false}
	}
	if strings.TrimSpace(request.Pool.InstanceType) == "" || request.Pool.CPU == 0 || request.Pool.MemoryGB == 0 {
		return Response{Ok: false, ErrorCode: "compute_resource_shape_required", Message: "The exact package instance type, CPU, and memory are required for compute sync.", Retryable: false}
	}
	if strings.TrimSpace(request.AccountId) == "" || strings.TrimSpace(request.PackageId) == "" || strings.TrimSpace(request.Pool.Id) == "" || strings.TrimSpace(request.Pool.NodePoolId) == "" || strings.TrimSpace(request.Allocation.Id) == "" || !strings.HasPrefix(request.Allocation.InstanceId, "ins-") || strings.TrimSpace(request.Zone) == "" || !validOwnershipTags(request.Tags) || request.Tags["opl_account_id"] != request.AccountId || request.Tags["opl_resource_id"] != request.Allocation.Id {
		return Response{Ok: false, ErrorCode: "compute_sync_identity_required", Message: "Exact compute account, resource, CVM instance, Zone, and ownership tags are required for compute sync.", Retryable: false}
	}
	pool, poolRequestID, err := client.describeNativeNodePool(request.Pool.NodePoolId)
	if err != nil {
		response := sdkErrorResponse("tencent_describe_node_pool_failed", err)
		response.ProviderRequestId = poolRequestID
		return response
	}
	if failure := readbackNodePoolFailure(pool, request, poolRequestID); failure != nil {
		return *failure
	}
	machines, requestId, err := client.describeClusterMachines(request.Pool.NodePoolId)
	if err != nil {
		response := sdkErrorResponse("tencent_describe_cluster_machines_failed", err)
		response.ProviderData = map[string]string{"nodePoolId": request.Pool.NodePoolId, "machineName": request.Allocation.MachineName, "nodeName": request.Allocation.NodeName, "privateIp": request.Allocation.PrivateIp}
		return response
	}
	machine := findComputeMachine(machines, request.Allocation)
	cvm := client.computeRenewalReadback(request)
	if !cvm.Ok {
		if cvm.ErrorCode != "compute_instance_type_mismatch" && cvm.ErrorCode != "compute_resource_shape_mismatch" {
			cvm.ErrorCode = "tencent_sync_cvm_readback_failed"
		}
		return cvm
	}
	if machine == nil && cvm.Status == "external_deleted" {
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
			CVMStatus:         "NOT_FOUND",
			TKEStatus:         "NOT_FOUND",
			ProviderRequestId: firstNonEmpty(cvm.ProviderRequestId, requestId),
			ProviderData: map[string]string{
				"clusterId":                  client.clusterId,
				"region":                     client.region,
				"nodePoolId":                 request.Pool.NodePoolId,
				"machineName":                request.Allocation.MachineName,
				"nodeName":                   request.Allocation.NodeName,
				"privateIp":                  request.Allocation.PrivateIp,
				"syncResult":                 "missing",
				"cvmStatus":                  "NOT_FOUND",
				"tkeStatus":                  "NOT_FOUND",
				"describeClusterMachinesReq": requestId,
				"describeCvmRequestId":       cvm.ProviderData["describeCvmRequestId"],
			},
		}
	}
	if machine == nil || cvm.Status == "external_deleted" {
		return Response{Ok: false, ErrorCode: "compute_provider_partial_identity", Message: "Tencent compute identity is only partially present.", ProviderRequestId: firstNonEmpty(cvm.ProviderRequestId, requestId, poolRequestID), Retryable: true}
	}
	privateIP := firstNonEmpty(stringValue(machine.LanIP), request.Allocation.PrivateIp)
	nodeName := firstNonEmpty(kubernetesNodeName(machine), request.Allocation.NodeName)
	machineName := firstNonEmpty(stringValue(machine.MachineName), request.Allocation.MachineName)
	status := strings.ToLower(strings.TrimSpace(stringValue(machine.MachineState)))
	if !explicitReadyState(status) {
		return Response{Ok: false, ErrorCode: "compute_machine_state_unready", Message: "Tencent Machine has not explicitly reported Ready or Running.", ProviderRequestId: firstNonEmpty(cvm.ProviderRequestId, requestId, poolRequestID), Retryable: true}
	}
	if !machineResourceShapeMatches(machine, request.Pool) {
		response := computeResourceShapeFailure(firstNonEmpty(cvm.ProviderRequestId, requestId, poolRequestID))
		return response
	}
	tkeInstance, tkeRequestID, err := client.describeNativeTkeClusterInstanceByMachineName(machineName, request.Pool.NodePoolId)
	if err != nil {
		response := sdkErrorResponse("tencent_describe_tke_instance_failed", err)
		response.ProviderRequestId = firstNonEmpty(tkeRequestID, cvm.ProviderRequestId, requestId, poolRequestID)
		response.Retryable = true
		return response
	}
	if failure := validateNativeTkeAllocationIdentity(tkeInstance, machine, request); failure != nil {
		failure.ProviderRequestId = firstNonEmpty(tkeRequestID, cvm.ProviderRequestId, requestId, poolRequestID)
		return *failure
	}
	if !nativeResourceShapeMatches(tkeInstance.Native, request.Pool) {
		response := computeResourceShapeFailure(firstNonEmpty(tkeRequestID, cvm.ProviderRequestId, requestId, poolRequestID))
		return response
	}
	if !strings.EqualFold(strings.TrimSpace(cvm.CVMStatus), "running") {
		return Response{Ok: false, ErrorCode: "compute_cvm_not_ready", Message: "Tencent CVM has not explicitly reported RUNNING.", ProviderRequestId: firstNonEmpty(cvm.ProviderRequestId, requestId), Retryable: true}
	}
	if stringValue(machine.InstanceType) != request.Pool.InstanceType || cvm.ProviderData["instanceType"] != request.Pool.InstanceType {
		cvm.ProviderData["tkeInstanceType"] = stringValue(machine.InstanceType)
		return Response{Ok: false, ErrorCode: "compute_instance_type_mismatch", Message: "Tencent TKE and CVM instance types must exactly match the requested package.", InstanceId: request.Allocation.InstanceId, NodeName: nodeName, PrivateIp: privateIP, InstanceType: firstNonEmpty(cvm.ProviderData["instanceType"], stringValue(machine.InstanceType)), ProviderRequestId: firstNonEmpty(cvm.ProviderRequestId, requestId), ProviderData: cvm.ProviderData, Retryable: true}
	}
	if cvm.ProviderData["privateIp"] != privateIP {
		return Response{Ok: false, ErrorCode: "compute_cvm_identity_mismatch", Message: "Tencent CVM and TKE machine do not share the requested private IP.", ProviderRequestId: firstNonEmpty(cvm.ProviderRequestId, requestId), Retryable: true}
	}
	providerData := cvm.ProviderData
	providerData["clusterId"] = client.clusterId
	providerData["region"] = client.region
	providerData["nodePoolId"] = request.Pool.NodePoolId
	providerData["machineName"] = machineName
	providerData["nodeName"] = nodeName
	providerData["privateIp"] = privateIP
	providerData["syncResult"] = "found"
	providerData["describeClusterMachinesReq"] = requestId
	providerData["describeNodePoolRequestId"] = poolRequestID
	providerData["describeTkeInstanceReqId"] = tkeRequestID
	providerData["cpu"] = strconv.FormatUint(request.Pool.CPU, 10)
	providerData["memoryGb"] = strconv.FormatUint(request.Pool.MemoryGB, 10)
	return Response{
		Ok:                true,
		OperationId:       "op-sync-compute-" + stableSuffix(request.AccountId, request.Allocation.Id, request.Pool.NodePoolId, machineName, privateIP)[:12],
		PoolId:            request.Pool.Id,
		NodePoolId:        request.Pool.NodePoolId,
		InstanceId:        request.Allocation.InstanceId,
		NodeName:          nodeName,
		PrivateIp:         privateIP,
		PublicIp:          request.Allocation.PublicIp,
		InstanceType:      request.Pool.InstanceType,
		Status:            status,
		CVMStatus:         cvm.CVMStatus,
		TKEStatus:         strings.ToUpper(status),
		ProviderRequestId: firstNonEmpty(cvm.ProviderRequestId, requestId),
		ProviderData:      providerData,
	}
}

func (client *tencentSDKClient) ProviderTruth(request Request, _ map[string]string) Response {
	if client == nil || client.nativeTkeClient == nil || client.nativeCvmClient == nil || client.nativeCbsClient == nil {
		return Response{Ok: false, ErrorCode: "tencent_sdk_client_missing", Message: "Tencent TKE, CVM, and CBS SDK clients are required.", Retryable: false}
	}
	for field, value := range map[string]string{
		"accountId": request.AccountId, "resourceId": request.Allocation.Id, "clusterId": request.Pool.ClusterId,
		"nodePoolId": request.Pool.NodePoolId, "instanceType": request.Pool.InstanceType, "machineName": request.Allocation.MachineName,
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
	request.Storage.Id = request.StorageVolumeId
	if request.Tags["opl_account_id"] != request.AccountId || !validCBSReadbackInput(request.Storage, request.Tags) {
		return Response{Ok: false, ErrorCode: "provider_truth_cbs_ownership_required", Message: "Exact CBS ownership tags and storage facts are required for provider truth.", Retryable: false}
	}
	if !validOwnershipTags(request.ComputeTags) || request.ComputeTags["opl_account_id"] != request.AccountId || request.ComputeTags["opl_workspace_id"] != request.Tags["opl_workspace_id"] || request.ComputeTags["opl_resource_id"] != request.Allocation.Id {
		return Response{Ok: false, ErrorCode: "provider_truth_compute_ownership_required", Message: "Exact CVM ownership tags are required for provider truth.", Retryable: false}
	}
	storagePresent, cbsStatus, cbsRequestID, cbsFacts, err := client.cbsVolumeTruth(request.Storage, request.Tags)
	if err != nil {
		response := sdkErrorResponse("tencent_provider_truth_cbs_probe_failed", err)
		response.ProviderRequestId = firstNonEmpty(response.ProviderRequestId, cbsRequestID)
		return response
	}
	providerData := map[string]string{
		"resourceId": request.Allocation.Id, "clusterId": request.Pool.ClusterId,
		"nodePoolId": request.Pool.NodePoolId, "machineName": request.Allocation.MachineName,
		"storageVolumeId": request.StorageVolumeId, "storagePresent": strconv.FormatBool(storagePresent), "cbsStatus": cbsStatus,
		"describeCbsRequestId": cbsRequestID,
	}
	for key, value := range cbsFacts {
		providerData[key] = value
	}
	withKnownStorage := func(response Response) Response {
		response.StoragePresent = &storagePresent
		response.CBSStatus = cbsStatus
		response.ProviderRequestId = firstNonEmpty(response.ProviderRequestId, cbsRequestID)
		for key, value := range response.ProviderData {
			providerData[key] = value
		}
		response.ProviderData = providerData
		return response
	}

	pool, poolRequestID, err := client.describeNativeNodePool(request.Pool.NodePoolId)
	if err != nil {
		response := sdkErrorResponse("tencent_provider_truth_node_pool_failed", err)
		response.ProviderRequestId = firstNonEmpty(response.ProviderRequestId, poolRequestID)
		return withKnownStorage(response)
	}
	if !isCVMNativeNodePool(pool) {
		return withKnownStorage(Response{Ok: false, ErrorCode: "tencent_cvm_node_pool_required", Message: "Provider truth requires a one-month PREPAID NativeCVM node pool with manual renewal.", ProviderRequestId: poolRequestID, Retryable: false})
	}

	machines, machineRequestID, err := client.describeClusterMachines(request.Pool.NodePoolId)
	if err != nil {
		response := sdkErrorResponse("tencent_provider_truth_machine_probe_failed", err)
		response.ProviderRequestId = firstNonEmpty(response.ProviderRequestId, machineRequestID)
		return withKnownStorage(response)
	}
	var machine *tke2022.Machine
	for _, candidate := range machines {
		if candidate != nil && stringValue(candidate.MachineName) == request.Allocation.MachineName {
			if machine != nil {
				return withKnownStorage(Response{Ok: false, ErrorCode: "provider_truth_machine_ambiguous", Message: "The supplied machine name is not unique in the exact node pool.", ProviderRequestId: machineRequestID, Retryable: false})
			}
			machine = candidate
		}
	}
	if machine != nil && stringValue(machine.LanIP) != request.Allocation.PrivateIp {
		return withKnownStorage(Response{Ok: false, ErrorCode: "provider_truth_machine_ip_mismatch", Message: "The supplied machine and private IP do not identify the same TKE machine.", ProviderRequestId: machineRequestID, Retryable: false})
	}

	tkeInstance, tkeRequestID, tkeErr := client.describeTkeClusterInstanceByPrivateIp(request.Allocation.PrivateIp, request.Pool.NodePoolId)
	if tkeErr != nil && !errors.Is(tkeErr, errTKEInstanceNotFound) {
		response := sdkErrorResponse("tencent_provider_truth_tke_instance_probe_failed", tkeErr)
		response.ProviderRequestId = firstNonEmpty(response.ProviderRequestId, tkeRequestID)
		return withKnownStorage(response)
	}
	cvmInstance, cvmRequestID, err := client.describeCvmInstanceByID(request.Allocation.InstanceId)
	if err != nil {
		response := sdkErrorResponse("tencent_provider_truth_cvm_probe_failed", err)
		response.ProviderRequestId = firstNonEmpty(response.ProviderRequestId, cvmRequestID)
		return withKnownStorage(response)
	}

	machinePresent, tkePresent, cvmPresent := machine != nil, tkeInstance != nil, cvmInstance != nil
	providerData["machinePresent"] = strconv.FormatBool(machinePresent)
	providerData["describeNodePoolRequestId"] = poolRequestID
	providerData["describeMachineRequestId"] = machineRequestID
	providerData["describeTkeRequestId"] = tkeRequestID
	providerData["describeCvmRequestId"] = cvmRequestID
	if !storagePresent && !machinePresent && !tkePresent && !cvmPresent {
		providerData["tkeStatus"] = "NOT_FOUND"
		providerData["cvmStatus"] = "NOT_FOUND"
		return Response{Ok: true, PoolId: request.Pool.Id, NodePoolId: request.Pool.NodePoolId, InstanceId: request.Allocation.InstanceId, PrivateIp: request.Allocation.PrivateIp, MachinePresent: &machinePresent, StoragePresent: &storagePresent, CVMStatus: "NOT_FOUND", TKEStatus: "NOT_FOUND", CBSStatus: cbsStatus, Status: "absent", MachineType: "NativeCVM", ProviderRequestId: firstNonEmpty(cbsRequestID, cvmRequestID, tkeRequestID, machineRequestID), ProviderData: providerData}
	}
	if !machinePresent && !tkePresent && !cvmPresent {
		providerData["tkeStatus"] = "NOT_FOUND"
		providerData["cvmStatus"] = "NOT_FOUND"
		return withKnownStorage(Response{Ok: false, ErrorCode: "provider_truth_partial_identity", Message: "Tencent provider identity is only partially present.", MachinePresent: &machinePresent, CVMStatus: "NOT_FOUND", TKEStatus: "NOT_FOUND", ProviderRequestId: firstNonEmpty(cvmRequestID, tkeRequestID, machineRequestID), Retryable: true})
	}
	if !machinePresent || !tkePresent || !cvmPresent {
		return withKnownStorage(Response{Ok: false, ErrorCode: "provider_truth_partial_identity", Message: "Tencent provider identity is only partially present.", ProviderRequestId: firstNonEmpty(cvmRequestID, tkeRequestID, machineRequestID), Retryable: true})
	}
	if stringValue(tkeInstance.InstanceId) != request.Allocation.MachineName {
		return withKnownStorage(Response{Ok: false, ErrorCode: "provider_truth_node_mismatch", Message: "The supplied machine does not identify the TKE cluster instance at the private IP.", ProviderRequestId: tkeRequestID, Retryable: false})
	}
	if stringValue(machine.InstanceType) != request.Pool.InstanceType || stringValue(cvmInstance.InstanceType) != request.Pool.InstanceType {
		return withKnownStorage(Response{Ok: false, ErrorCode: "provider_truth_compute_sku_mismatch", Message: "The TKE machine and CVM instance type must exactly match the requested package.", ProviderRequestId: firstNonEmpty(cvmRequestID, machineRequestID), Retryable: false})
	}
	if stringValue(cvmInstance.InstanceId) != request.Allocation.InstanceId || stringValue(cvmInstance.InstanceName) != request.Allocation.Id ||
		!containsString(cvmInstance.PrivateIpAddresses, request.Allocation.PrivateIp) {
		return withKnownStorage(Response{Ok: false, ErrorCode: "provider_truth_cvm_identity_mismatch", Message: "The supplied resource, instance, machine, and node do not identify the same CVM.", ProviderRequestId: cvmRequestID, Retryable: false})
	}
	zone := ""
	if cvmInstance.Placement != nil {
		zone = stringValue(cvmInstance.Placement.Zone)
	}
	deadline := normalizeTencentDeadline(stringValue(cvmInstance.ExpiredTime))
	cvmTags, tagErr := cvmOwnershipTags(cvmInstance)
	if tagErr != nil {
		return withKnownStorage(Response{Ok: false, ErrorCode: "provider_truth_cvm_ownership_mismatch", Message: tagErr.Error(), ProviderRequestId: cvmRequestID, Retryable: false})
	}
	for _, key := range cbsOwnershipTagKeys {
		if cvmTags[key] != request.ComputeTags[key] {
			return withKnownStorage(Response{Ok: false, ErrorCode: "provider_truth_cvm_ownership_mismatch", Message: "The exact CVM ownership tags do not match the compute allocation.", ProviderRequestId: cvmRequestID, Retryable: false})
		}
	}
	if stringValue(cvmInstance.InstanceChargeType) != "PREPAID" || stringValue(cvmInstance.RenewFlag) != "NOTIFY_AND_MANUAL_RENEW" || deadline == "" || strings.TrimSpace(zone) == "" || zone != request.Storage.Zone {
		return withKnownStorage(Response{Ok: false, ErrorCode: "provider_truth_cvm_billing_mismatch", Message: "The exact CVM does not have required PREPAID manual-renew billing facts.", ProviderRequestId: cvmRequestID, Retryable: false})
	}
	tkeStatus := strings.ToUpper(strings.TrimSpace(stringValue(tkeInstance.InstanceState)))
	cvmStatus := strings.ToUpper(strings.TrimSpace(stringValue(cvmInstance.InstanceState)))
	if tkeStatus == "" || cvmStatus == "" {
		return withKnownStorage(Response{Ok: false, ErrorCode: "provider_truth_status_unknown", Message: "Tencent returned a present resource without an exact state.", ProviderRequestId: firstNonEmpty(cvmRequestID, tkeRequestID), Retryable: true})
	}
	providerData["tkeStatus"] = tkeStatus
	providerData["cvmStatus"] = cvmStatus
	providerData["chargeType"] = stringValue(cvmInstance.InstanceChargeType)
	providerData["renewFlag"] = stringValue(cvmInstance.RenewFlag)
	providerData["deadline"] = deadline
	providerData["zone"] = zone
	providerData["instanceType"] = request.Pool.InstanceType
	for _, key := range cbsOwnershipTagKeys {
		providerData["computeTag:"+key] = cvmTags[key]
	}
	if !storagePresent {
		return withKnownStorage(Response{
			Ok: false, ErrorCode: "provider_truth_partial_identity", Message: "Tencent provider identity is only partially present.", Retryable: true,
			PoolId: request.Pool.Id, NodePoolId: request.Pool.NodePoolId, InstanceId: request.Allocation.InstanceId, PrivateIp: request.Allocation.PrivateIp,
			MachinePresent: &machinePresent, CVMStatus: cvmStatus, TKEStatus: tkeStatus, Status: "present", MachineType: "NativeCVM", InstanceType: request.Pool.InstanceType,
			ProviderRequestId: firstNonEmpty(cvmRequestID, tkeRequestID, machineRequestID),
		})
	}
	return Response{Ok: true, PoolId: request.Pool.Id, NodePoolId: request.Pool.NodePoolId, InstanceId: request.Allocation.InstanceId, PrivateIp: request.Allocation.PrivateIp, MachinePresent: &machinePresent, StoragePresent: &storagePresent, CVMStatus: cvmStatus, TKEStatus: tkeStatus, CBSStatus: cbsStatus, Status: "present", MachineType: "NativeCVM", InstanceType: request.Pool.InstanceType, ProviderRequestId: firstNonEmpty(cbsRequestID, cvmRequestID), ProviderData: providerData}
}

func (client *tencentSDKClient) cbsVolumeTruth(storage StorageInput, tags map[string]string) (bool, string, string, map[string]string, error) {
	request := cbs2017.NewDescribeDisksRequest()
	request.DiskIds = []*string{common.StringPtr(storage.Id)}
	response, err := client.nativeCbsClient.DescribeDisks(request)
	if err != nil {
		if sdkErr, ok := err.(*tcerrors.TencentCloudSDKError); ok && sdkErr.Code == cbs2017.INVALIDDISKID_NOTFOUND {
			return false, "NOT_FOUND", sdkErr.RequestId, nil, nil
		}
		return false, "", "", nil, err
	}
	if response == nil || response.Response == nil || response.Response.TotalCount == nil ||
		*response.Response.TotalCount != uint64(len(response.Response.DiskSet)) || len(response.Response.DiskSet) > 1 {
		return false, "", "", nil, fmt.Errorf("Tencent CBS DescribeDisks response is missing or incomplete")
	}
	requestID := stringValue(response.Response.RequestId)
	if len(response.Response.DiskSet) == 0 {
		return false, "NOT_FOUND", requestID, nil, nil
	}
	disk := response.Response.DiskSet[0]
	facts, err := validateCBSVolume(disk, storage, tags)
	if err != nil {
		return false, "", requestID, nil, err
	}
	truthFacts := map[string]string{
		"storageChargeType": facts["diskChargeType"], "storageRenewFlag": facts["renewFlag"], "storageDeadline": facts["deadline"],
		"storageDiskType": facts["diskType"], "storageDiskName": facts["diskName"], "storageDiskUsage": facts["diskUsage"],
		"storageSizeGb": facts["sizeGb"], "storageZone": facts["zone"],
	}
	for _, key := range cbsOwnershipTagKeys {
		truthFacts[key] = facts[key]
	}
	return true, strings.ToUpper(strings.TrimSpace(stringValue(disk.DiskState))), requestID, truthFacts, nil
}

func (client *tencentSDKClient) describeClusterMachinePages(filters []*tke2022.Filter) ([]*tke2022.Machine, string, error) {
	machines := []*tke2022.Machine{}
	requestID := ""
	totalCount := int64(-1)
	for offset := int64(0); ; {
		request := tke2022.NewDescribeClusterMachinesRequest()
		request.ClusterId = common.StringPtr(client.clusterId)
		request.Limit = common.Int64Ptr(tkeListPageLimit)
		request.Offset = common.Int64Ptr(offset)
		request.Filters = filters
		response, err := client.nativeTkeClient.DescribeClusterMachines(request)
		if err != nil {
			return nil, requestID, err
		}
		if response == nil || response.Response == nil || response.Response.TotalCount == nil {
			return nil, requestID, fmt.Errorf("Tencent TKE DescribeClusterMachines response is missing or incomplete")
		}
		pageTotal := *response.Response.TotalCount
		if pageTotal < 0 || (totalCount >= 0 && pageTotal != totalCount) || int64(len(response.Response.Machines)) > tkeListPageLimit {
			return nil, requestID, fmt.Errorf("Tencent TKE DescribeClusterMachines response is missing or incomplete")
		}
		if totalCount < 0 {
			totalCount = pageTotal
		}
		requestID = firstNonEmpty(requestID, stringValue(response.Response.RequestId))
		machines = append(machines, response.Response.Machines...)
		if int64(len(machines)) == totalCount {
			return machines, requestID, nil
		}
		if len(response.Response.Machines) == 0 || int64(len(machines)) > totalCount {
			return nil, requestID, fmt.Errorf("Tencent TKE DescribeClusterMachines response is missing or incomplete")
		}
		offset += int64(len(response.Response.Machines))
	}
}

func (client *tencentSDKClient) describeClusterInstancePages(filters []*tke2022.Filter) ([]*tke2022.Instance, string, error) {
	instances := []*tke2022.Instance{}
	requestID := ""
	var totalCount *uint64
	for offset := int64(0); ; {
		request := tke2022.NewDescribeClusterInstancesRequest()
		request.ClusterId = common.StringPtr(client.clusterId)
		request.Limit = common.Int64Ptr(tkeListPageLimit)
		request.Offset = common.Int64Ptr(offset)
		request.Filters = filters
		response, err := client.nativeTkeClient.DescribeClusterInstances(request)
		if err != nil {
			return nil, requestID, err
		}
		if response == nil || response.Response == nil || response.Response.TotalCount == nil || len(response.Response.InstanceSet) > int(tkeListPageLimit) {
			return nil, requestID, fmt.Errorf("Tencent TKE DescribeClusterInstances response is missing or incomplete")
		}
		pageTotal := *response.Response.TotalCount
		if totalCount != nil && pageTotal != *totalCount {
			return nil, requestID, fmt.Errorf("Tencent TKE DescribeClusterInstances response is missing or incomplete")
		}
		if totalCount == nil {
			totalCount = common.Uint64Ptr(pageTotal)
		}
		requestID = firstNonEmpty(requestID, stringValue(response.Response.RequestId))
		instances = append(instances, response.Response.InstanceSet...)
		if uint64(len(instances)) == pageTotal {
			return instances, requestID, nil
		}
		if len(response.Response.InstanceSet) == 0 || uint64(len(instances)) > pageTotal {
			return nil, requestID, fmt.Errorf("Tencent TKE DescribeClusterInstances response is missing or incomplete")
		}
		offset += int64(len(response.Response.InstanceSet))
	}
}

func (client *tencentSDKClient) describeClusterMachines(nodePoolId string) ([]*tke2022.Machine, string, error) {
	var filters []*tke2022.Filter
	if strings.TrimSpace(nodePoolId) != "" {
		filters = []*tke2022.Filter{{Name: common.StringPtr("NodePoolsId"), Values: []*string{common.StringPtr(nodePoolId)}}}
	}
	machines, requestID, err := client.describeClusterMachinePages(filters)
	if err == nil || strings.TrimSpace(nodePoolId) == "" || !isInvalidMachineFilterError(err) {
		return machines, requestID, err
	}
	machines, requestID, err = client.describeClusterMachinePages(nil)
	if err != nil {
		return nil, requestID, err
	}
	instances, _, err := client.describeClusterInstancePages([]*tke2022.Filter{{Name: common.StringPtr("NodePoolIds"), Values: []*string{common.StringPtr(nodePoolId)}}})
	if err != nil {
		return nil, requestID, err
	}
	poolIPs := map[string]bool{}
	for _, instance := range instances {
		if instance != nil && stringValue(instance.NodePoolId) == nodePoolId {
			poolIPs[stringValue(instance.LanIP)] = true
		}
	}
	filtered := machines[:0]
	for _, machine := range machines {
		if machine != nil && poolIPs[stringValue(machine.LanIP)] {
			filtered = append(filtered, machine)
		}
	}
	return filtered, requestID, nil
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
	describeRequest.Limit = common.Int64Ptr(100)
	describeResponse, err := client.nativeCvmClient.DescribeInstances(describeRequest)
	if err != nil {
		return nil, "", err
	}
	if describeResponse == nil || describeResponse.Response == nil {
		return nil, "", fmt.Errorf("Tencent CVM DescribeInstances response is missing")
	}
	requestId := stringValue(describeResponse.Response.RequestId)
	if describeResponse.Response.TotalCount == nil || *describeResponse.Response.TotalCount != int64(len(describeResponse.Response.InstanceSet)) {
		return nil, requestId, fmt.Errorf("Tencent CVM DescribeInstances response is incomplete")
	}
	if *describeResponse.Response.TotalCount > 1 {
		return nil, requestId, fmt.Errorf("Tencent CVM private IP identity is ambiguous")
	}
	if *describeResponse.Response.TotalCount == 1 {
		instance := describeResponse.Response.InstanceSet[0]
		if instance == nil || !containsString(instance.PrivateIpAddresses, privateIp) {
			return nil, requestId, fmt.Errorf("Tencent CVM DescribeInstances returned a mismatched instance")
		}
		return instance, requestId, nil
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
		{Name: common.StringPtr("InstanceIds"), Values: []*string{common.StringPtr(machineName)}},
		{Name: common.StringPtr("NodePoolIds"), Values: []*string{common.StringPtr(nodePoolId)}},
	}
	describeResponse, err := client.nativeTkeClient.DescribeClusterInstances(describeRequest)
	if err != nil {
		return nil, "", err
	}
	if describeResponse == nil || describeResponse.Response == nil || describeResponse.Response.TotalCount == nil ||
		*describeResponse.Response.TotalCount != uint64(len(describeResponse.Response.InstanceSet)) {
		return nil, "", fmt.Errorf("Tencent TKE DescribeClusterInstances response is missing")
	}
	requestID := stringValue(describeResponse.Response.RequestId)
	var matched *tke2022.Instance
	for _, instance := range describeResponse.Response.InstanceSet {
		if instance == nil {
			return nil, requestID, fmt.Errorf("Tencent TKE DescribeClusterInstances returned an empty instance")
		}
		if stringValue(instance.InstanceId) == machineName && stringValue(instance.NodePoolId) == nodePoolId && strings.EqualFold(stringValue(instance.NodeType), "Native") {
			if matched != nil {
				return nil, requestID, fmt.Errorf("Tencent TKE Machine identity is ambiguous")
			}
			matched = instance
		}
	}
	if matched == nil {
		return nil, requestID, fmt.Errorf("%w for Machine %s", errTKEInstanceNotFound, machineName)
	}
	return matched, requestID, nil
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

func nodePoolLabels(pool *tke2022.NodePool) map[string]string {
	labels := map[string]string{}
	for _, label := range pool.Labels {
		if label != nil && label.Name != nil && label.Value != nil {
			labels[*label.Name] = *label.Value
		}
	}
	return labels
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func deadlineAfter(current, previous string) bool {
	currentTime, currentErr := time.Parse(time.RFC3339, normalizeTencentDeadline(current))
	previousTime, previousErr := time.Parse(time.RFC3339, normalizeTencentDeadline(previous))
	return currentErr == nil && previousErr == nil && currentTime.After(previousTime)
}

func normalizeTencentDeadline(value string) string {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return parsed.UTC().Format(time.RFC3339)
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
	if request.Pool.MaxReplicas <= 0 {
		return nil, &Response{Ok: false, ErrorCode: "max_replicas_required", Message: "ComputePool maxReplicas must be explicitly approved.", Retryable: false}
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
		{Name: common.StringPtr("medopl.cn/workload"), Value: common.StringPtr("workspace")},
	}
	createRequest.Labels = nodePoolLabels
	createRequest.Taints = []*tke2022.Taint{{Key: common.StringPtr("oplcloud.cn/workspace-id"), Value: common.StringPtr("unallocated"), Effect: common.StringPtr("NoSchedule")}}
	createRequest.Native = &tke2022.CreateNativeNodePoolParam{
		Scaling: &tke2022.MachineSetScaling{
			MinReplicas:  common.Int64Ptr(0),
			MaxReplicas:  common.Int64Ptr(request.Pool.MaxReplicas),
			CreatePolicy: common.StringPtr("ZonePriority"),
		},
		SubnetIds:          stringsToPtrs(splitCsv(env["TENCENT_CVM_SUBNET_ID"])),
		InstanceChargeType: common.StringPtr("PREPAID"),
		InstanceChargePrepaid: &tke2022.InstanceChargePrepaid{
			Period: common.Uint64Ptr(1), RenewFlag: common.StringPtr("NOTIFY_AND_MANUAL_RENEW"),
		},
		SystemDisk: &tke2022.Disk{
			DiskType: common.StringPtr(defaultString(env["TENCENT_CVM_SYSTEM_DISK_TYPE"], "CLOUD_BSSD")),
			DiskSize: common.Int64Ptr(int64(intFromEnv(env, "TENCENT_CVM_SYSTEM_DISK_SIZE_GB", 50))),
		},
		InstanceTypes:     []*string{common.StringPtr(request.Pool.InstanceType)},
		SecurityGroupIds:  stringsToPtrs(splitCsv(env["TENCENT_CVM_SECURITY_GROUP_IDS"])),
		AutoRepair:        common.BoolPtr(false),
		EnableAutoscaling: common.BoolPtr(false),
		Replicas:          common.Int64Ptr(0),
		MachineType:       common.StringPtr("NativeCVM"),
		AutomationService: common.BoolPtr(true),
		RuntimeRootDir:    common.StringPtr("/var/lib/containerd"),
	}
	return createRequest, nil
}

type bootstrapPackageSpec struct {
	PackageID          string
	PoolID             string
	InstanceType       string
	CPU                uint64
	MemoryGB           uint64
	MaxReplicas        int64
	ExpectedNodePoolID string
}

func bootstrapPackageSpecs(env map[string]string, requireMaxReplicas bool) ([]bootstrapPackageSpec, *Response) {
	basicInstanceType := strings.TrimSpace(env["OPL_BASIC_COMPUTE_INSTANCE_TYPE"])
	if basicInstanceType == "" || len(basicInstanceType) > 64 || strings.ContainsAny(basicInstanceType, " \t\r\n") {
		return nil, &Response{Ok: false, ErrorCode: "instance_type_required", Message: "OPL_BASIC_COMPUTE_INSTANCE_TYPE must be the release-owner-approved resolved Basic instance type.", Retryable: false}
	}
	proInstanceType := strings.TrimSpace(env["OPL_PRO_COMPUTE_INSTANCE_TYPE"])
	if proInstanceType == "" || len(proInstanceType) > 64 || strings.ContainsAny(proInstanceType, " \t\r\n") {
		return nil, &Response{Ok: false, ErrorCode: "instance_type_required", Message: "OPL_PRO_COMPUTE_INSTANCE_TYPE must be the release-owner-approved resolved Pro instance type.", Retryable: false}
	}
	basicMax, err := bootstrapMaxReplicas(env, "OPL_BASIC_COMPUTE_NODE_POOL_MAX_REPLICAS", requireMaxReplicas)
	if err != nil {
		return nil, &Response{Ok: false, ErrorCode: "max_replicas_required", Message: err.Error(), Retryable: false}
	}
	proMax, err := bootstrapMaxReplicas(env, "OPL_PRO_COMPUTE_NODE_POOL_MAX_REPLICAS", requireMaxReplicas)
	if err != nil {
		return nil, &Response{Ok: false, ErrorCode: "max_replicas_required", Message: err.Error(), Retryable: false}
	}
	return []bootstrapPackageSpec{
		{PackageID: "basic", PoolID: "pool-basic-2c4g", InstanceType: basicInstanceType, CPU: 2, MemoryGB: 4, MaxReplicas: basicMax, ExpectedNodePoolID: strings.TrimSpace(env["OPL_BASIC_COMPUTE_NODE_POOL_ID"])},
		{PackageID: "pro", PoolID: "pool-pro-8c16g", InstanceType: proInstanceType, CPU: 8, MemoryGB: 16, MaxReplicas: proMax, ExpectedNodePoolID: strings.TrimSpace(env["OPL_PRO_COMPUTE_NODE_POOL_ID"])},
	}, nil
}

func bootstrapMaxReplicas(env map[string]string, key string, required bool) (int64, error) {
	raw := strings.TrimSpace(env[key])
	if raw == "" && !required {
		return 0, nil
	}
	return requiredPositiveInt64(env, key)
}

func requiredPositiveInt64(env map[string]string, key string) (int64, error) {
	raw := strings.TrimSpace(env[key])
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be an explicitly approved positive integer", key)
	}
	return value, nil
}

func canonicalNativeMachineType(value string) (string, bool, bool) {
	switch {
	case strings.EqualFold(strings.TrimSpace(value), "NativeCVM"):
		return "NativeCVM", true, true
	case strings.EqualFold(strings.TrimSpace(value), "Native"):
		return "Native", false, true
	case strings.EqualFold(strings.TrimSpace(value), "CXM"):
		return "CXM", false, true
	default:
		return strings.TrimSpace(value), false, false
	}
}

func validTencentCVMID(value string) bool {
	value = strings.TrimSpace(value)
	return len(value) > len("ins-") && strings.HasPrefix(value, "ins-")
}

func validateBootstrapSystemConfig(env map[string]string, specs []bootstrapPackageSpec) *Response {
	for _, key := range []string{"OPL_SYSTEM_COMPUTE_NODE_POOL_ID", "OPL_SYSTEM_COMPUTE_MACHINE_ID", "OPL_SYSTEM_COMPUTE_NODE_NAME", "OPL_SYSTEM_COMPUTE_MACHINE_TYPE"} {
		if strings.TrimSpace(env[key]) == "" {
			return &Response{Ok: false, ErrorCode: "protected_resource_guard_configuration_invalid", Message: key + " is required for node pool bootstrap.", Retryable: false}
		}
	}
	machineType, cvmApplicable, supported := canonicalNativeMachineType(env["OPL_SYSTEM_COMPUTE_MACHINE_TYPE"])
	if !supported {
		return &Response{Ok: false, ErrorCode: "protected_resource_guard_configuration_invalid", Message: "System MachineType must be NativeCVM, Native, or CXM.", Retryable: false}
	}
	cvmID := strings.TrimSpace(env["OPL_SYSTEM_COMPUTE_CVM_ID"])
	if cvmApplicable && !validTencentCVMID(cvmID) {
		return &Response{Ok: false, ErrorCode: "protected_resource_guard_configuration_invalid", Message: "NativeCVM system identity requires a verified ins-* value.", Retryable: false}
	}
	if !cvmApplicable && cvmID != "" {
		return &Response{Ok: false, ErrorCode: "protected_resource_guard_configuration_invalid", Message: machineType + " system identity must not configure a CVM identity.", Retryable: false}
	}
	systemPoolID := strings.TrimSpace(env["OPL_SYSTEM_COMPUTE_NODE_POOL_ID"])
	seen := map[string]bool{systemPoolID: true}
	for _, spec := range specs {
		if spec.ExpectedNodePoolID == "" {
			continue
		}
		if seen[spec.ExpectedNodePoolID] {
			return &Response{Ok: false, ErrorCode: "protected_resource_guard_configuration_invalid", Message: "System, Basic, and Pro NodePool identities must be distinct.", Retryable: false}
		}
		seen[spec.ExpectedNodePoolID] = true
	}
	return nil
}

func (client *tencentSDKClient) bootstrapNodePoolInventory() ([]*tke2022.NodePool, error) {
	if client == nil || client.nativeTkeClient == nil || client.nativeCvmClient == nil {
		return nil, fmt.Errorf("Tencent TKE and CVM SDK clients are required")
	}
	pools := []*tke2022.NodePool{}
	totalCount := int64(-1)
	for offset := int64(0); ; {
		request := tke2022.NewDescribeNodePoolsRequest()
		request.ClusterId = common.StringPtr(client.clusterId)
		request.Offset = common.Int64Ptr(offset)
		request.Limit = common.Int64Ptr(tkeListPageLimit)
		response, err := client.nativeTkeClient.DescribeNodePools(request)
		if err != nil {
			return nil, err
		}
		if response == nil || response.Response == nil || response.Response.TotalCount == nil || len(response.Response.NodePools) > int(tkeListPageLimit) {
			return nil, fmt.Errorf("Tencent TKE NodePool inventory is missing or incomplete")
		}
		pageTotal := *response.Response.TotalCount
		if pageTotal < 0 || (totalCount >= 0 && pageTotal != totalCount) {
			return nil, fmt.Errorf("Tencent TKE NodePool inventory is missing or incomplete")
		}
		if totalCount < 0 {
			totalCount = pageTotal
		}
		pools = append(pools, response.Response.NodePools...)
		if int64(len(pools)) == totalCount {
			break
		}
		if len(response.Response.NodePools) == 0 || int64(len(pools)) > totalCount {
			return nil, fmt.Errorf("Tencent TKE NodePool inventory is missing or incomplete")
		}
		offset += int64(len(response.Response.NodePools))
	}
	ids := map[string]bool{}
	for _, pool := range pools {
		if pool == nil {
			return nil, fmt.Errorf("Tencent TKE NodePool inventory contains an empty identity")
		}
		id := strings.TrimSpace(stringValue(pool.NodePoolId))
		if len(id) <= len("np-") || !strings.HasPrefix(id, "np-") || ids[id] {
			return nil, fmt.Errorf("Tencent TKE NodePool inventory contains an empty or duplicate identity")
		}
		ids[id] = true
	}
	return pools, nil
}

func poolTouchesBootstrapSpec(pool *tke2022.NodePool, spec bootstrapPackageSpec) bool {
	if pool == nil {
		return false
	}
	labels := nodePoolLabels(pool)
	return stringValue(pool.Name) == spec.PoolID || labels["oplcloud.cn/pool-id"] == spec.PoolID ||
		labels["oplcloud.cn/package-id"] == spec.PackageID ||
		(spec.ExpectedNodePoolID != "" && stringValue(pool.NodePoolId) == spec.ExpectedNodePoolID)
}

func bootstrapNodePoolIDs(pools []*tke2022.NodePool) []string {
	ids := make([]string, 0, len(pools))
	for _, pool := range pools {
		ids = append(ids, strings.TrimSpace(stringValue(pool.NodePoolId)))
	}
	sort.Strings(ids)
	return ids
}

func bootstrapPackagePoolStatus(pool *tke2022.NodePool, spec bootstrapPackageSpec) string {
	if pool == nil || !isCVMNativeNodePool(pool) {
		return ""
	}
	lifeState := strings.ToLower(strings.TrimSpace(stringValue(pool.LifeState)))
	if lifeState != "running" && lifeState != "creating" {
		return ""
	}
	if spec.ExpectedNodePoolID != "" && stringValue(pool.NodePoolId) != spec.ExpectedNodePoolID {
		return ""
	}
	labels := nodePoolLabels(pool)
	if stringValue(pool.Name) != spec.PoolID || labels["oplcloud.cn/pool-id"] != spec.PoolID || labels["oplcloud.cn/package-id"] != spec.PackageID ||
		labels["oplcloud.cn/instance-type"] != spec.InstanceType || labels["medopl.cn/workload"] != "workspace" {
		return ""
	}
	native := pool.Native
	if native == nil || native.Scaling == nil || native.Scaling.MinReplicas == nil || *native.Scaling.MinReplicas != 0 ||
		native.Scaling.MaxReplicas == nil || *native.Scaling.MaxReplicas <= 0 || (spec.MaxReplicas > 0 && *native.Scaling.MaxReplicas != spec.MaxReplicas) || native.Replicas == nil || native.ReadyReplicas == nil ||
		*native.Replicas != *native.ReadyReplicas || native.EnableAutoscaling == nil || *native.EnableAutoscaling || native.AutoRepair == nil || *native.AutoRepair ||
		len(native.InstanceTypes) != 1 || stringValue(native.InstanceTypes[0]) != spec.InstanceType || len(native.SubnetIds) == 0 {
		return ""
	}
	if pool.DeletionProtection == nil || !*pool.DeletionProtection || len(pool.Taints) != 1 {
		return ""
	}
	taint := pool.Taints[0]
	if taint == nil || stringValue(taint.Key) != "oplcloud.cn/workspace-id" || stringValue(taint.Value) != "unallocated" || stringValue(taint.Effect) != "NoSchedule" {
		return ""
	}
	if lifeState == "creating" {
		return "pending"
	}
	return "registered"
}

func bootstrapInventoryMatches(pools []*tke2022.NodePool, env map[string]string, specs []bootstrapPackageSpec) (map[string]*tke2022.NodePool, *Response) {
	systemPoolID := strings.TrimSpace(env["OPL_SYSTEM_COMPUTE_NODE_POOL_ID"])
	var systemPool *tke2022.NodePool
	for _, pool := range pools {
		if stringValue(pool.NodePoolId) == systemPoolID {
			if systemPool != nil {
				return nil, &Response{Ok: false, ErrorCode: "node_pool_bootstrap_inventory_conflict", Message: "System NodePool identity is duplicated.", Retryable: false}
			}
			systemPool = pool
		}
	}
	if systemPool == nil {
		return nil, &Response{Ok: false, ErrorCode: "node_pool_bootstrap_inventory_conflict", Message: "Protected system NodePool was not found.", Retryable: false}
	}
	for _, spec := range specs {
		if poolTouchesBootstrapSpec(systemPool, spec) {
			return nil, &Response{Ok: false, ErrorCode: "node_pool_bootstrap_inventory_conflict", Message: "Protected system NodePool conflicts with a customer package identity.", Retryable: false}
		}
	}
	for _, pool := range pools {
		if pool == systemPool {
			continue
		}
		matchCount := 0
		for _, spec := range specs {
			if poolTouchesBootstrapSpec(pool, spec) {
				matchCount++
			}
		}
		if matchCount != 1 {
			return nil, &Response{Ok: false, ErrorCode: "node_pool_bootstrap_inventory_conflict", Message: "NodePool inventory contains an unknown or ambiguous package identity.", Retryable: false}
		}
	}
	matches := map[string]*tke2022.NodePool{}
	for _, spec := range specs {
		for _, pool := range pools {
			if pool == systemPool || !poolTouchesBootstrapSpec(pool, spec) {
				continue
			}
			if matches[spec.PackageID] != nil || bootstrapPackagePoolStatus(pool, spec) == "" {
				return nil, &Response{Ok: false, ErrorCode: "node_pool_bootstrap_inventory_conflict", Message: "Package NodePool inventory is duplicated or does not match the fixed contract.", Retryable: false}
			}
			matches[spec.PackageID] = pool
		}
	}
	if matches["basic"] != nil && matches["pro"] != nil && stringValue(matches["basic"].NodePoolId) == stringValue(matches["pro"].NodePoolId) {
		return nil, &Response{Ok: false, ErrorCode: "node_pool_bootstrap_inventory_conflict", Message: "Basic and Pro cannot share a NodePool.", Retryable: false}
	}
	return matches, nil
}

func newProtectedSystemFacts() ProtectedSystemFacts {
	return ProtectedSystemFacts{
		PoolCheckStatus:    protectedCheckNotChecked,
		MachineCheckStatus: protectedCheckNotChecked,
		NodeCheckStatus:    protectedCheckNotChecked,
		CVMCheckStatus:     protectedCheckNotChecked,
	}
}

func (client *tencentSDKClient) verifyBootstrapSystemIdentity(pools []*tke2022.NodePool, env map[string]string) (ProtectedSystemFacts, error) {
	facts := newProtectedSystemFacts()
	systemPoolID := strings.TrimSpace(env["OPL_SYSTEM_COMPUTE_NODE_POOL_ID"])
	wantMachine := strings.TrimSpace(env["OPL_SYSTEM_COMPUTE_MACHINE_ID"])
	wantNode := strings.TrimSpace(env["OPL_SYSTEM_COMPUTE_NODE_NAME"])
	if systemPoolID == "" {
		facts.PoolCheckStatus = protectedCheckFailed
		return facts, fmt.Errorf("protected system NodePool identity is required")
	}
	var systemPool *tke2022.NodePool
	for _, pool := range pools {
		if pool != nil && stringValue(pool.NodePoolId) == systemPoolID {
			if systemPool != nil {
				facts.PoolCheckStatus = protectedCheckFailed
				return facts, fmt.Errorf("protected system NodePool identity is ambiguous")
			}
			systemPool = pool
		}
	}
	if systemPool == nil {
		facts.PoolCheckStatus = protectedCheckFailed
		return facts, fmt.Errorf("protected system NodePool identity is unavailable")
	}
	facts.NodePoolID = systemPoolID
	facts.PoolCheckStatus = protectedCheckPassed
	rawMachineType := ""
	if systemPool.Native != nil {
		rawMachineType = stringValue(systemPool.Native.MachineType)
	}
	machineType, cvmApplicable, supportedMachineType := canonicalNativeMachineType(rawMachineType)
	facts.MachineType = machineType
	facts.CVMApplicable = cvmApplicable
	if wantMachine == "" {
		facts.MachineCheckStatus = protectedCheckFailed
		return facts, fmt.Errorf("protected system Machine identity is required")
	}
	machines, _, err := client.describeClusterMachines(systemPoolID)
	if err != nil {
		facts.MachineCheckStatus = protectedCheckFailed
		return facts, err
	}
	var matched *tke2022.Machine
	for _, machine := range machines {
		if machine != nil && stringValue(machine.MachineName) == wantMachine {
			if matched != nil {
				facts.MachineCheckStatus = protectedCheckFailed
				return facts, fmt.Errorf("protected system Machine identity is ambiguous")
			}
			matched = machine
		}
	}
	if matched == nil {
		facts.MachineCheckStatus = protectedCheckFailed
		return facts, fmt.Errorf("protected system Machine identity does not match")
	}
	facts.MachineID = wantMachine
	facts.MachineCheckStatus = protectedCheckPassed
	if wantNode == "" {
		facts.NodeCheckStatus = protectedCheckFailed
		return facts, fmt.Errorf("protected system Node identity is required")
	}
	nodeMatches := 0
	for _, machine := range machines {
		if machine != nil && kubernetesNodeName(machine) == wantNode {
			nodeMatches++
		}
	}
	if kubernetesNodeName(matched) != wantNode || nodeMatches != 1 {
		facts.NodeCheckStatus = protectedCheckFailed
		return facts, fmt.Errorf("protected system Machine and Node identities do not match uniquely")
	}
	facts.NodeName = wantNode
	facts.NodeCheckStatus = protectedCheckPassed
	if !supportedMachineType {
		facts.CVMCheckStatus = protectedCheckFailed
		return facts, fmt.Errorf("protected system MachineType is unsupported")
	}
	configuredMachineType := strings.TrimSpace(env["OPL_SYSTEM_COMPUTE_MACHINE_TYPE"])
	if configuredMachineType != "" {
		expectedMachineType, _, configuredMachineTypeSupported := canonicalNativeMachineType(configuredMachineType)
		if !configuredMachineTypeSupported || expectedMachineType != machineType {
			facts.CVMCheckStatus = protectedCheckFailed
			return facts, fmt.Errorf("protected system MachineType does not match configured identity")
		}
	}
	configuredCVMID := strings.TrimSpace(env["OPL_SYSTEM_COMPUTE_CVM_ID"])
	if !cvmApplicable {
		if configuredCVMID != "" {
			facts.CVMCheckStatus = protectedCheckFailed
			return facts, fmt.Errorf("protected non-CVM system identity configured an unexpected CVM")
		}
		facts.CVMCheckStatus = protectedCheckNotApplicable
		return facts, nil
	}
	cvm, _, err := client.describeCvmInstanceByPrivateIp(stringValue(matched.LanIP))
	if err != nil || cvm == nil {
		facts.CVMCheckStatus = protectedCheckFailed
		return facts, fmt.Errorf("protected system CVM identity does not match")
	}
	resolvedCVMID := strings.TrimSpace(stringValue(cvm.InstanceId))
	if !validTencentCVMID(resolvedCVMID) || (configuredCVMID != "" && configuredCVMID != resolvedCVMID) {
		facts.CVMCheckStatus = protectedCheckFailed
		return facts, fmt.Errorf("protected system CVM identity does not match")
	}
	facts.CVMID = resolvedCVMID
	facts.CVMCheckStatus = protectedCheckPassed
	return facts, nil
}

func bootstrapInventoryCapacity(request Request, env map[string]string) (int64, *Response) {
	for _, key := range []string{"OPL_BASIC_COMPUTE_NODE_POOL_MAX_REPLICAS", "OPL_PRO_COMPUTE_NODE_POOL_MAX_REPLICAS"} {
		if _, err := requiredPositiveInt64(env, key); err != nil {
			return 0, &Response{Ok: false, ErrorCode: "max_replicas_required", Message: err.Error(), Retryable: false}
		}
	}
	const immediateHeadroom int64 = 1
	if request.RequiredCapacity > 0 && request.RequiredCapacity != immediateHeadroom {
		return 0, &Response{Ok: false, ErrorCode: "workspace_capacity_input_mismatch", Message: "bootstrap requiredCapacity must be exactly one immediate launch headroom.", Retryable: false}
	}
	return immediateHeadroom, nil
}

func withBootstrapInventoryFacts(response Response, inventory Response, requiredCapacity int64) Response {
	response.RequiredCapacity = requiredCapacity
	response.PrepaidQuotaRemaining = inventory.PrepaidQuotaRemaining
	response.Subnets = inventory.Subnets
	response.TKEClusterNodeLimit = inventory.TKEClusterNodeLimit
	response.TKECurrentNodeCount = inventory.TKECurrentNodeCount
	response.TKEAvailableNodeCapacity = inventory.TKEAvailableNodeCapacity
	response.TKECapacity = inventory.TKECapacity
	response.ProtectedSystem = inventory.ProtectedSystem
	response.NodePoolInventory = inventory.NodePoolInventory
	return response
}

func copyStringMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source)+2)
	for key, value := range source {
		result[key] = value
	}
	return result
}

func selectedWorkspaceSKU(inventory Response, packageID, configured string) (string, error) {
	for _, current := range inventory.SKUPackages {
		if current.PackageID != packageID {
			continue
		}
		selected := strings.TrimSpace(configured)
		if selected == "" {
			selected = current.RecommendedInstanceType
		}
		for _, candidate := range current.Candidates {
			if candidate.InstanceType == selected {
				return selected, nil
			}
		}
		return "", fmt.Errorf("selected %s instance type is not an eligible current PREPAID candidate", packageID)
	}
	return "", fmt.Errorf("%s SKU inventory is missing", packageID)
}

func existingBootstrapPackageSKU(pool *tke2022.NodePool, spec bootstrapPackageSpec) string {
	if pool == nil || stringValue(pool.Name) != spec.PoolID {
		return ""
	}
	labels := nodePoolLabels(pool)
	if labels["oplcloud.cn/pool-id"] != spec.PoolID || labels["oplcloud.cn/package-id"] != spec.PackageID || pool.Native == nil || len(pool.Native.InstanceTypes) != 1 {
		return ""
	}
	labelSKU := strings.TrimSpace(labels["oplcloud.cn/instance-type"])
	nativeSKU := strings.TrimSpace(stringValue(pool.Native.InstanceTypes[0]))
	if labelSKU == "" || labelSKU != nativeSKU {
		return ""
	}
	return labelSKU
}

func preserveExistingBootstrapSKUs(pools []*tke2022.NodePool, specs []bootstrapPackageSpec, inventory Response, systemPoolID string) *Response {
	for index := range specs {
		var existing *tke2022.NodePool
		for _, pool := range pools {
			if pool == nil || stringValue(pool.NodePoolId) == systemPoolID || !poolTouchesBootstrapSpec(pool, specs[index]) {
				continue
			}
			if existing != nil {
				existing = nil
				break
			}
			existing = pool
		}
		if existing == nil {
			continue
		}
		existingSKU := existingBootstrapPackageSKU(existing, specs[index])
		if existingSKU == "" {
			continue
		}
		if _, err := selectedWorkspaceSKU(inventory, specs[index].PackageID, existingSKU); err != nil {
			return &Response{Ok: false, ErrorCode: "workspace_sku_selection_invalid", Message: err.Error(), Retryable: false, MutationCount: 0}
		}
		specs[index].InstanceType = existingSKU
	}
	return nil
}

func (client *tencentSDKClient) BootstrapComputeNodePools(request Request, env map[string]string) Response {
	requiredCapacity, failure := bootstrapInventoryCapacity(request, env)
	if failure != nil {
		return *failure
	}
	inventory := client.WorkspaceSKUInventory(Request{Action: "workspace_sku_inventory", Zone: request.Zone, RequiredCapacity: requiredCapacity}, env)
	if !inventory.Ok {
		return inventory
	}
	if !request.DryRun && (strings.TrimSpace(env["OPL_BASIC_COMPUTE_INSTANCE_TYPE"]) == "" || strings.TrimSpace(env["OPL_PRO_COMPUTE_INSTANCE_TYPE"]) == "") {
		return Response{Ok: false, ErrorCode: "instance_type_required", Message: "Mutation requires the Basic and Pro instance types selected by the preceding read-only inventory.", Retryable: false, MutationCount: 0}
	}
	effectiveEnv := copyStringMap(env)
	basicSKU, err := selectedWorkspaceSKU(inventory, "basic", env["OPL_BASIC_COMPUTE_INSTANCE_TYPE"])
	if err != nil {
		return Response{Ok: false, ErrorCode: "workspace_sku_selection_invalid", Message: err.Error(), Retryable: false, MutationCount: 0}
	}
	proSKU, err := selectedWorkspaceSKU(inventory, "pro", env["OPL_PRO_COMPUTE_INSTANCE_TYPE"])
	if err != nil {
		return Response{Ok: false, ErrorCode: "workspace_sku_selection_invalid", Message: err.Error(), Retryable: false, MutationCount: 0}
	}
	effectiveEnv["OPL_BASIC_COMPUTE_INSTANCE_TYPE"] = basicSKU
	effectiveEnv["OPL_PRO_COMPUTE_INSTANCE_TYPE"] = proSKU
	specs, failure := bootstrapPackageSpecs(effectiveEnv, true)
	if failure != nil {
		return *failure
	}
	if failure = validateBootstrapSystemConfig(effectiveEnv, specs); failure != nil {
		return *failure
	}
	pools, err := client.bootstrapNodePoolInventory()
	if err != nil {
		return Response{Ok: false, ErrorCode: "node_pool_bootstrap_inventory_unavailable", Message: "Tencent NodePool inventory is unavailable.", Retryable: false}
	}
	inventory.NodePoolInventory = bootstrapNodePoolIDs(pools)
	if failure = preserveExistingBootstrapSKUs(pools, specs, inventory, strings.TrimSpace(effectiveEnv["OPL_SYSTEM_COMPUTE_NODE_POOL_ID"])); failure != nil {
		failure.NodePoolInventory = inventory.NodePoolInventory
		return *failure
	}
	matches, failure := bootstrapInventoryMatches(pools, effectiveEnv, specs)
	if failure != nil {
		failure.NodePoolInventory = inventory.NodePoolInventory
		return *failure
	}
	protectedSystem, err := client.verifyBootstrapSystemIdentity(pools, effectiveEnv)
	if err != nil {
		return Response{Ok: false, ErrorCode: "protected_system_identity_mismatch", Message: "Protected system identity is unavailable or inconsistent.", ProtectedSystem: protectedSystem, NodePoolInventory: inventory.NodePoolInventory, MutationCount: 0, Retryable: false}
	}
	inventory.ProtectedSystem = protectedSystem
	results := make([]NodePoolBootstrapResult, 0, len(specs))
	missing, pendingCount := 0, 0
	for _, spec := range specs {
		result := NodePoolBootstrapResult{
			PackageID: spec.PackageID, PoolID: spec.PoolID, InstanceType: spec.InstanceType, CPU: spec.CPU, MemoryGB: spec.MemoryGB, MaxReplicas: spec.MaxReplicas,
			MaxReplicasSource: "not_configured", MaxReplicasDecision: "release_owner_approval_required",
			MaxReplicasConstraint:     "positive_integer_subject_to_current_tencent_account_region_quota",
			MaxReplicasRecommendation: "release_owner_selects_after_inventory_and_quota_review",
		}
		if spec.MaxReplicas > 0 {
			result.MaxReplicasSource = "workflow_input"
			if !request.DryRun {
				result.MaxReplicasSource = "release_owner_approved_input"
				result.MaxReplicasDecision = "approved_for_mutation"
			}
		}
		if pool := matches[spec.PackageID]; pool != nil {
			result.NodePoolID = stringValue(pool.NodePoolId)
			result.Status = bootstrapPackagePoolStatus(pool, spec)
			if spec.MaxReplicas == 0 && pool.Native != nil && pool.Native.Scaling != nil && pool.Native.Scaling.MaxReplicas != nil {
				result.MaxReplicas = *pool.Native.Scaling.MaxReplicas
				result.MaxReplicasSource = "tencent_node_pool_readback"
				result.MaxReplicasDecision = "existing_pool_inventory"
			}
			if result.Status == "pending" {
				pendingCount++
			}
		} else {
			result.Status = "missing"
			missing++
		}
		results = append(results, result)
	}
	if request.DryRun {
		status := "registered"
		if missing > 0 {
			status = "missing"
		} else if pendingCount > 0 {
			status = "pending"
		}
		return withBootstrapInventoryFacts(Response{Ok: true, Status: status, NodePools: results, MutationCount: 0}, inventory, requiredCapacity)
	}
	mutationCount, createdCount, failedCount := 0, 0, 0
	for index, spec := range specs {
		if results[index].Status != "missing" {
			continue
		}
		createRequest, buildFailure := buildCreateNativeNodePoolRequest(Request{PackageId: spec.PackageID, Pool: ComputePoolInput{Id: spec.PoolID, InstanceType: spec.InstanceType, MaxReplicas: spec.MaxReplicas}}, effectiveEnv)
		if buildFailure != nil {
			results[index].Status = "failed"
			results[index].ErrorCode = buildFailure.ErrorCode
			failedCount++
			continue
		}
		mutationCount++
		created, createErr := client.nativeTkeClient.CreateNodePool(createRequest)
		if createErr != nil || created == nil || created.Response == nil || !strings.HasPrefix(strings.TrimSpace(stringValue(created.Response.NodePoolId)), "np-") {
			results[index].Status = "failed"
			results[index].ErrorCode = "node_pool_create_failed"
			failedCount++
			continue
		}
		createdID := strings.TrimSpace(stringValue(created.Response.NodePoolId))
		if createdID == strings.TrimSpace(effectiveEnv["OPL_SYSTEM_COMPUTE_NODE_POOL_ID"]) || (index > 0 && results[0].NodePoolID == createdID) {
			results[index].Status = "failed"
			results[index].ErrorCode = "node_pool_create_identity_conflict"
			failedCount++
			continue
		}
		readback, _, readbackErr := client.describeNativeNodePool(createdID)
		readbackStatus := bootstrapPackagePoolStatus(readback, bootstrapPackageSpec{
			PackageID: spec.PackageID, PoolID: spec.PoolID, InstanceType: spec.InstanceType, MaxReplicas: spec.MaxReplicas, ExpectedNodePoolID: createdID,
		})
		if readbackErr != nil || readbackStatus == "" {
			results[index].NodePoolID = createdID
			results[index].Status = "failed"
			results[index].ErrorCode = "node_pool_create_readback_failed"
			failedCount++
			continue
		}
		results[index].NodePoolID = createdID
		if readbackStatus == "pending" {
			results[index].Status = "pending"
			pendingCount++
		} else {
			results[index].Status = "created"
			createdCount++
		}
	}
	if failedCount > 0 || pendingCount > 0 {
		return withBootstrapInventoryFacts(Response{Ok: false, Status: "partial", ErrorCode: "node_pool_bootstrap_partial", Message: "One or more package NodePools were not created; existing successful pools were preserved.", NodePools: results, MutationCount: mutationCount, Retryable: false}, inventory, requiredCapacity)
	}
	status := "completed"
	if createdCount == len(specs) {
		status = "created"
	} else if createdCount == 0 {
		status = "registered"
	}
	return withBootstrapInventoryFacts(Response{Ok: true, Status: status, NodePools: results, MutationCount: mutationCount}, inventory, requiredCapacity)
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

var cbsOwnershipTagKeys = [...]string{"opl_account_id", "opl_workspace_id", "opl_resource_id", "opl_operation_id"}

var cvmOwnershipTagAliases = map[string]string{
	"oplcloud.cn/account-id":   "opl_account_id",
	"oplcloud.cn/workspace-id": "opl_workspace_id",
	"oplcloud.cn/resource-id":  "opl_resource_id",
	"oplcloud.cn/operation-id": "opl_operation_id",
}

func cbsTags(tags map[string]string) []*cbs2017.Tag {
	items := []*cbs2017.Tag{}
	for _, key := range cbsOwnershipTagKeys {
		if value := strings.TrimSpace(tags[key]); value != "" {
			items = append(items, &cbs2017.Tag{Key: common.StringPtr(key), Value: common.StringPtr(value)})
		}
	}
	return items
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
	if setupFailure != nil && request.Action != "readiness" && request.Action != "protected_resource_check" {
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
	if request.Action == "protected_resource_check" {
		guard := protectedresource.FromMap(env)
		if err := guard.Check(protectedresource.Target{PackageID: request.PackageId, NodePoolID: request.Pool.NodePoolId, MachineID: request.Allocation.MachineName, NodeName: request.Allocation.NodeName, CVMID: request.Allocation.InstanceId}); err != nil {
			return Response{Ok: false, ErrorCode: err.Error(), Message: "Protected resource guard rejected the target.", Retryable: false}
		}
		return Response{Ok: true, Status: "allowed"}
	}
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
	if request.Action == "workspace_sku_inventory" {
		return client.WorkspaceSKUInventory(request, env)
	}
	if request.Action == "bootstrap_compute_node_pools" {
		if !request.DryRun && strings.TrimSpace(env["RUN_TENCENT_NODE_POOL_BOOTSTRAP_CONFIRMATION"]) != nodePoolBootstrapMutationConfirmation {
			return Response{Ok: false, ErrorCode: "node_pool_bootstrap_confirmation_required", Message: "The exact node pool bootstrap mutation confirmation is required.", Retryable: false}
		}
		if !request.DryRun && strings.TrimSpace(env["RUN_TENCENT_CREATE_RELEASE_EXECUTION"]) != "1" {
			return Response{Ok: false, ErrorCode: "live_mutation_flag_required", Message: "Set RUN_TENCENT_CREATE_RELEASE_EXECUTION=1 to run live Tencent resource mutations.", Retryable: false}
		}
		if !request.DryRun && strings.TrimSpace(env["RUN_TENCENT_NODE_POOL_BOOTSTRAP"]) != "1" {
			return Response{Ok: false, ErrorCode: "node_pool_bootstrap_flag_required", Message: "Set RUN_TENCENT_NODE_POOL_BOOTSTRAP=1 only in the approved manual bootstrap workflow.", Retryable: false}
		}
		return client.BootstrapComputeNodePools(request, env)
	}
	if isLiveMutation(request) && strings.TrimSpace(env["RUN_TENCENT_CREATE_RELEASE_EXECUTION"]) != "1" {
		return Response{
			Ok:        false,
			ErrorCode: "live_mutation_flag_required",
			Message:   "Set RUN_TENCENT_CREATE_RELEASE_EXECUTION=1 to run live Tencent resource mutations.",
			Retryable: false,
		}
	}
	if isLiveMutation(request) {
		guard := protectedresource.FromMap(env)
		if err := guard.Check(protectedresource.Target{
			PackageID: request.PackageId, NodePoolID: request.Pool.NodePoolId,
			MachineID: request.Allocation.MachineName, NodeName: request.Allocation.NodeName, CVMID: request.Allocation.InstanceId,
		}); err != nil {
			return Response{Ok: false, ErrorCode: err.Error(), Message: "Protected resource guard rejected the mutation target.", Retryable: false}
		}
	}

	switch request.Action {
	case "capacity_preflight":
		return client.Capacity(request, env)
	case "storage_preflight":
		return client.StoragePreflight(request, env)
	case "provider_truth":
		return client.ProviderTruth(request, env)
	case "compute_claim_truth":
		return client.ComputeClaimTruth(request, env)
	case "discover_storage_volume":
		return client.DiscoverStorageVolume(request, env)
	case "claim_compute_machine":
		return client.ClaimComputeMachine(request, env)
	case "prepare_compute_allocation":
		if request.DryRun {
			return Response{Ok: true, PoolId: request.Pool.Id, NodePoolId: request.Pool.NodePoolId, Status: "prepared", ProviderRequestId: "dryrun-prepare-" + request.Pool.NodePoolId, CurrentReplicas: 0, TargetReplicas: 1, Machines: []MachineOutput{}}
		}
		return client.PrepareComputeAllocation(request, env)
	case "create_storage_volume":
		return client.CreateStorageVolume(request, env)
	case "sync_storage_volume":
		return client.SyncStorageVolume(request, env)
	case "renew_storage_volume":
		return client.RenewStorageVolume(request, env)
	case "renew_compute_allocation":
		return client.RenewComputeAllocation(request, env)
	case "create_compute_allocation":
		if request.DryRun {
			return dryRunCreateComputeAllocation(request, env)
		}
		return client.CreateComputeAllocation(request, env)
	case "read_compute_allocation":
		reader, ok := client.(interface {
			ReadComputeAllocation(Request, map[string]string) Response
		})
		if !ok {
			return Response{Ok: false, ErrorCode: "tencent_live_not_implemented", Message: "Tencent live compute allocation readback is not implemented in this build.", Retryable: false}
		}
		return reader.ReadComputeAllocation(request, env)
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
	return request.Action == "bootstrap_compute_node_pools" || request.Action == "create_compute_allocation" || request.Action == "tag_compute_machine" || request.Action == "claim_compute_machine" ||
		request.Action == "destroy_compute_allocation" || request.Action == "renew_compute_allocation" || request.Action == "create_storage_volume" || request.Action == "renew_storage_volume"
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
