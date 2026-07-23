package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

type FabricClient interface {
	Catalog(ctx context.Context) (FabricCatalog, error)
	CreateComputeAllocation(ctx context.Context, input ComputeAllocationInput, idempotencyKey string) (ComputeAllocation, error)
	GetComputeAllocation(ctx context.Context, id string) (ComputeAllocation, error)
	SyncComputeAllocation(ctx context.Context, id string) (ComputeAllocation, error)
	DestroyComputeAllocation(ctx context.Context, id string, idempotencyKey string) (ComputeAllocation, error)
	CreateStorageVolume(ctx context.Context, input StorageVolumeInput, idempotencyKey string) (StorageVolume, error)
	SyncStorageVolume(ctx context.Context, id string) (StorageVolume, error)
	DestroyStorageVolume(ctx context.Context, id string, idempotencyKey string) (StorageVolume, error)
	CreateStorageAttachment(ctx context.Context, input StorageAttachmentInput, idempotencyKey string) (StorageAttachment, error)
	DetachStorageAttachment(ctx context.Context, id string, idempotencyKey string) (StorageAttachment, error)
	WriteGatewaySecret(ctx context.Context, input GatewaySecretWriteInput, idempotencyKey string) (GatewaySecretWriteResult, error)
	CreateWorkspaceRuntime(ctx context.Context, input WorkspaceRuntimeInput, idempotencyKey string) (WorkspaceRuntime, error)
	DestroyWorkspaceRuntime(ctx context.Context, workspaceID string, idempotencyKey string) (WorkspaceRuntime, error)
	WorkspaceRuntimeStatus(ctx context.Context, workspaceID string) (WorkspaceRuntime, error)
	Readiness(ctx context.Context) (map[string]any, error)
	ListOperations(ctx context.Context) ([]FabricOperation, error)
}

type FabricRenewalClient interface {
	RenewComputeAllocation(context.Context, string, string) (ComputeAllocation, error)
	RenewStorageVolume(context.Context, string, string) (StorageVolume, error)
}

type FabricMonthlyPreflightClient interface {
	MonthlyPreflight(context.Context, MonthlyPreflightInput) (MonthlyPreflight, error)
}

type FabricMonthlyProviderTruthClient interface {
	MonthlyProviderTruth(context.Context, string, string) (MonthlyProviderTruth, error)
}

type FabricHTTPError struct {
	StatusCode int
	Body       string
}

func (e *FabricHTTPError) Error() string {
	return fmt.Sprintf("fabric request failed: status %d: %s", e.StatusCode, e.Body)
}

type FabricCatalog struct {
	SchemaVersion     int                      `json:"schemaVersion"`
	Owner             string                   `json:"owner"`
	WorkspacePackages []FabricWorkspacePackage `json:"workspacePackages"`
	StorageClasses    []FabricStorageClass     `json:"storageClasses"`
	IngressDomains    []FabricIngressDomain    `json:"ingressDomains"`
}

type FabricWorkspacePackage struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	ComputeProfileID string `json:"computeProfileId"`
	CPU              int    `json:"cpu"`
	MemoryGB         int    `json:"memoryGb"`
	DiskGB           int    `json:"diskGb"`
	Provider         string `json:"provider"`
	Available        bool   `json:"available"`
}

type FabricStorageClass struct {
	ID               string `json:"id"`
	StorageClassName string `json:"storageClassName"`
	Provider         string `json:"provider"`
	Available        bool   `json:"available"`
}

type FabricIngressDomain struct {
	ID          string `json:"id"`
	Host        string `json:"host"`
	PathPattern string `json:"pathPattern"`
	Available   bool   `json:"available"`
}

type ComputeAllocationInput struct {
	ID          string `json:"id,omitempty"`
	AccountID   string `json:"accountId"`
	WorkspaceID string `json:"workspaceId"`
	PackageID   string `json:"packageId"`
	NodePoolID  string `json:"nodePoolId,omitempty"`
}

type MonthlyPreflightInput struct {
	ResourceType string `json:"resourceType"`
	PackageID    string `json:"packageId"`
	SizeGB       int    `json:"sizeGb,omitempty"`
	Zone         string `json:"zone,omitempty"`
}

type MonthlyPreflight struct {
	ResourceType       string            `json:"resourceType"`
	PackageID          string            `json:"packageId"`
	NodePoolID         string            `json:"nodePoolId,omitempty"`
	SizeGB             int               `json:"sizeGb"`
	Zone               string            `json:"zone"`
	Available          bool              `json:"available"`
	ChargeType         string            `json:"chargeType"`
	PeriodMonths       int               `json:"periodMonths"`
	RenewFlag          string            `json:"renewFlag"`
	ProviderPriceCNY   float64           `json:"providerPriceCny"`
	ProviderRequestIDs map[string]string `json:"providerRequestIds"`
}

type MonthlyProviderTruth struct {
	ComputeState      string            `json:"computeState"`
	StorageState      string            `json:"storageState"`
	Compute           ComputeAllocation `json:"compute"`
	Storage           StorageVolume     `json:"storage"`
	ProviderRequestID string            `json:"providerRequestId,omitempty"`
	ErrorCode         string            `json:"errorCode,omitempty"`
}

type ComputeAllocation struct {
	ID                 string            `json:"id"`
	AccountID          string            `json:"accountId"`
	WorkspaceID        string            `json:"workspaceId"`
	PackageID          string            `json:"packageId"`
	Status             string            `json:"status"`
	Provider           string            `json:"provider"`
	ProviderResourceID string            `json:"providerResourceId"`
	ProviderRequestID  string            `json:"providerRequestId"`
	OperationID        string            `json:"operationId,omitempty"`
	ServiceName        string            `json:"serviceName"`
	PoolID             string            `json:"poolId,omitempty"`
	NodePoolID         string            `json:"nodePoolId,omitempty"`
	InstanceID         string            `json:"instanceId,omitempty"`
	CVMInstanceID      string            `json:"cvmInstanceId,omitempty"`
	NodeName           string            `json:"nodeName,omitempty"`
	MachineName        string            `json:"machineName,omitempty"`
	PrivateIP          string            `json:"privateIp,omitempty"`
	PublicIP           string            `json:"publicIp,omitempty"`
	InstanceType       string            `json:"instanceType,omitempty"`
	Zone               string            `json:"zone,omitempty"`
	ChargeType         string            `json:"chargeType,omitempty"`
	RenewFlag          string            `json:"renewFlag,omitempty"`
	Deadline           string            `json:"deadline,omitempty"`
	ProviderData       map[string]string `json:"providerData,omitempty"`
	CostTags           map[string]string `json:"costTags,omitempty"`
}

type StorageVolumeInput struct {
	ID          string `json:"id,omitempty"`
	AccountID   string `json:"accountId"`
	WorkspaceID string `json:"workspaceId"`
	ComputeID   string `json:"computeId"`
	Zone        string `json:"zone"`
	SizeGB      int    `json:"sizeGb"`
}

type StorageVolume struct {
	ID                 string            `json:"id"`
	AccountID          string            `json:"accountId,omitempty"`
	Provider           string            `json:"provider,omitempty"`
	ProviderResourceID string            `json:"providerResourceId,omitempty"`
	ProviderRequestID  string            `json:"providerRequestId"`
	WorkspaceID        string            `json:"workspaceId"`
	Status             string            `json:"status"`
	SizeGB             int               `json:"sizeGb,omitempty"`
	StorageClass       string            `json:"storageClass,omitempty"`
	CBSStatus          string            `json:"cbsStatus,omitempty"`
	DiskType           string            `json:"diskType,omitempty"`
	RenewFlag          string            `json:"renewFlag,omitempty"`
	Deadline           string            `json:"deadline,omitempty"`
	Zone               string            `json:"zone,omitempty"`
	ProviderData       map[string]string `json:"providerData,omitempty"`
	CostTags           map[string]string `json:"costTags,omitempty"`
}

type StorageAttachmentInput struct {
	WorkspaceID string `json:"workspaceId"`
	ComputeID   string `json:"computeId"`
	VolumeID    string `json:"volumeId"`
}

type StorageAttachment struct {
	ID                   string `json:"id"`
	WorkspaceID          string `json:"workspaceId"`
	ComputeID            string `json:"computeId,omitempty"`
	VolumeID             string `json:"volumeId"`
	Status               string `json:"status"`
	Provider             string `json:"provider,omitempty"`
	ProviderAttachmentID string `json:"providerAttachmentId,omitempty"`
	ProviderRequestID    string `json:"providerRequestId"`
	MountPath            string `json:"mountPath,omitempty"`
}

type WorkspaceRuntimeInput struct {
	WorkspaceID      string `json:"workspaceId"`
	ComputeID        string `json:"computeId"`
	VolumeID         string `json:"volumeId"`
	ImageID          string `json:"imageId"`
	GatewaySecretRef string `json:"gatewaySecretRef"`
}

type GatewaySecretWriteInput struct {
	AccountID     string `json:"accountId"`
	GatewayAPIKey string `json:"gatewayApiKey"`
}

type GatewaySecretWriteResult struct {
	SecretRef   string `json:"secretRef"`
	Version     string `json:"version"`
	Fingerprint string `json:"fingerprint"`
}

type WorkspaceRuntime struct {
	ID          string                 `json:"id"`
	WorkspaceID string                 `json:"workspaceId"`
	URL         string                 `json:"url"`
	Status      string                 `json:"status"`
	ServiceName string                 `json:"serviceName"`
	Access      WorkspaceRuntimeAccess `json:"access,omitempty"`
	Ready       bool                   `json:"ready"`
	Checks      []any                  `json:"checks"`
}

type WorkspaceRuntimeAccess struct {
	Username          string `json:"username,omitempty"`
	Password          string `json:"password,omitempty"`
	CredentialStatus  string `json:"credentialStatus,omitempty"`
	CredentialVersion string `json:"credentialVersion,omitempty"`
	SecretRef         string `json:"secretRef,omitempty"`
}

type FabricOperation struct {
	ID                      string         `json:"id"`
	OperationID             string         `json:"operationId"`
	CallerService           string         `json:"callerService"`
	Action                  string         `json:"action"`
	ResourceKind            string         `json:"resourceKind"`
	ResourceID              string         `json:"resourceId"`
	AccountID               string         `json:"accountId,omitempty"`
	WorkspaceID             string         `json:"workspaceId,omitempty"`
	Provider                string         `json:"provider,omitempty"`
	ProviderRequestID       string         `json:"providerRequestId,omitempty"`
	IdempotencyKey          string         `json:"idempotencyKey,omitempty"`
	RequestHash             string         `json:"requestHash,omitempty"`
	RedactedProviderPayload map[string]any `json:"redactedProviderPayload,omitempty"`
	Status                  string         `json:"status"`
	ErrorCode               string         `json:"errorCode,omitempty"`
	Retryable               bool           `json:"retryable,omitempty"`
	StartedAt               string         `json:"startedAt"`
	FinishedAt              string         `json:"finishedAt,omitempty"`
	CreatedAt               string         `json:"createdAt"`
}

type fabricHTTPClient struct {
	baseURL string
	token   string
	client  *http.Client
}

func NewFabricHTTPClient(baseURL, token string, client *http.Client) FabricClient {
	if client == nil {
		client = http.DefaultClient
	}
	return &fabricHTTPClient{baseURL: baseURL, token: token, client: client}
}

func (c *fabricHTTPClient) Catalog(ctx context.Context) (FabricCatalog, error) {
	var result FabricCatalog
	err := c.get(ctx, "/fabric/catalog", &result)
	return result, err
}

func (c *fabricHTTPClient) MonthlyPreflight(ctx context.Context, input MonthlyPreflightInput) (MonthlyPreflight, error) {
	var result MonthlyPreflight
	err := c.post(ctx, "/fabric/monthly-preflight", input, "", &result)
	return result, err
}

func (c *fabricHTTPClient) MonthlyProviderTruth(ctx context.Context, computeID, storageID string) (MonthlyProviderTruth, error) {
	params := url.Values{"computeAllocationId": {computeID}, "storageVolumeId": {storageID}}
	var result MonthlyProviderTruth
	err := c.get(ctx, "/fabric/monthly-provider-truth?"+params.Encode(), &result)
	return result, err
}

func (c *fabricHTTPClient) CreateComputeAllocation(ctx context.Context, input ComputeAllocationInput, idempotencyKey string) (ComputeAllocation, error) {
	var result ComputeAllocation
	err := c.post(ctx, "/fabric/compute-allocations", input, idempotencyKey, &result)
	return result, err
}

func (c *fabricHTTPClient) GetComputeAllocation(ctx context.Context, id string) (ComputeAllocation, error) {
	var result ComputeAllocation
	err := c.get(ctx, "/fabric/compute-allocations/"+id, &result)
	return result, err
}

func (c *fabricHTTPClient) SyncComputeAllocation(ctx context.Context, id string) (ComputeAllocation, error) {
	var result ComputeAllocation
	err := c.post(ctx, "/fabric/compute-allocations/"+id+"/sync", map[string]string{}, "", &result)
	return result, err
}

func (c *fabricHTTPClient) RenewComputeAllocation(ctx context.Context, id, idempotencyKey string) (ComputeAllocation, error) {
	var result ComputeAllocation
	err := c.post(ctx, "/fabric/compute-allocations/"+url.PathEscape(id)+"/renew", map[string]any{}, idempotencyKey, &result)
	return result, err
}

func (c *fabricHTTPClient) DestroyComputeAllocation(ctx context.Context, id string, idempotencyKey string) (ComputeAllocation, error) {
	var result ComputeAllocation
	err := c.post(ctx, "/fabric/compute-allocations/"+id+"/destroy", map[string]string{}, idempotencyKey, &result)
	return result, err
}

func (c *fabricHTTPClient) CreateStorageVolume(ctx context.Context, input StorageVolumeInput, idempotencyKey string) (StorageVolume, error) {
	var result StorageVolume
	err := c.post(ctx, "/fabric/storage-volumes", input, idempotencyKey, &result)
	return result, err
}

func (c *fabricHTTPClient) SyncStorageVolume(ctx context.Context, id string) (StorageVolume, error) {
	var result StorageVolume
	err := c.post(ctx, "/fabric/storage-volumes/"+id+"/sync", map[string]string{}, "", &result)
	return result, err
}

func (c *fabricHTTPClient) RenewStorageVolume(ctx context.Context, id, idempotencyKey string) (StorageVolume, error) {
	var result StorageVolume
	err := c.post(ctx, "/fabric/storage-volumes/"+url.PathEscape(id)+"/renew", map[string]any{}, idempotencyKey, &result)
	return result, err
}

func (c *fabricHTTPClient) DestroyStorageVolume(ctx context.Context, id string, idempotencyKey string) (StorageVolume, error) {
	var result StorageVolume
	err := c.post(ctx, "/fabric/storage-volumes/"+id+"/destroy", map[string]string{}, idempotencyKey, &result)
	return result, err
}

func (c *fabricHTTPClient) CreateStorageAttachment(ctx context.Context, input StorageAttachmentInput, idempotencyKey string) (StorageAttachment, error) {
	var result StorageAttachment
	err := c.post(ctx, "/fabric/storage-attachments", input, idempotencyKey, &result)
	return result, err
}

func (c *fabricHTTPClient) DetachStorageAttachment(ctx context.Context, id string, idempotencyKey string) (StorageAttachment, error) {
	var result StorageAttachment
	err := c.post(ctx, "/fabric/storage-attachments/"+id+"/detach", map[string]string{}, idempotencyKey, &result)
	return result, err
}

func (c *fabricHTTPClient) WriteGatewaySecret(ctx context.Context, input GatewaySecretWriteInput, idempotencyKey string) (GatewaySecretWriteResult, error) {
	var result GatewaySecretWriteResult
	if err := c.post(ctx, "/fabric/gateway-secrets", input, idempotencyKey, &result); err != nil {
		var httpErr *FabricHTTPError
		if errors.As(err, &httpErr) {
			return result, &FabricHTTPError{StatusCode: httpErr.StatusCode}
		}
		return result, errors.New("fabric gateway secret request failed")
	}
	if result.SecretRef == "" || result.Version == "" || result.Fingerprint == "" {
		return GatewaySecretWriteResult{}, errors.New("fabric gateway secret response invalid")
	}
	return result, nil
}

func (c *fabricHTTPClient) CreateWorkspaceRuntime(ctx context.Context, input WorkspaceRuntimeInput, idempotencyKey string) (WorkspaceRuntime, error) {
	var result WorkspaceRuntime
	err := c.post(ctx, "/fabric/workspace-runtimes", input, idempotencyKey, &result)
	return result, err
}

func (c *fabricHTTPClient) DestroyWorkspaceRuntime(ctx context.Context, workspaceID, idempotencyKey string) (WorkspaceRuntime, error) {
	var result WorkspaceRuntime
	err := c.post(ctx, "/fabric/workspace-runtimes/"+url.PathEscape(workspaceID)+"/destroy", map[string]any{}, idempotencyKey, &result)
	return result, err
}

func (c *fabricHTTPClient) WorkspaceRuntimeStatus(ctx context.Context, workspaceID string) (WorkspaceRuntime, error) {
	var result WorkspaceRuntime
	err := c.get(ctx, "/fabric/workspace-runtimes/"+workspaceID+"/status", &result)
	return result, err
}

func (c *fabricHTTPClient) Readiness(ctx context.Context) (map[string]any, error) {
	result := map[string]any{}
	err := c.get(ctx, "/fabric/readiness", &result)
	return result, err
}

func (c *fabricHTTPClient) ListOperations(ctx context.Context) ([]FabricOperation, error) {
	var result []FabricOperation
	err := c.get(ctx, "/fabric/operations", &result)
	return result, err
}

func (c *fabricHTTPClient) doJSON(req *http.Request, output any) error {
	c.authorize(req)
	res, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fabricHTTPResponseError(res)
	}
	return json.NewDecoder(res.Body).Decode(output)
}

func (c *fabricHTTPClient) post(ctx context.Context, path string, input any, idempotencyKey string, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	return c.doJSON(req, output)
}

func (c *fabricHTTPClient) authorize(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
}

func (c *fabricHTTPClient) get(ctx context.Context, path string, output any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	return c.doJSON(req, output)
}

func fabricHTTPResponseError(res *http.Response) error {
	body, _ := io.ReadAll(res.Body)
	return &FabricHTTPError{StatusCode: res.StatusCode, Body: string(body)}
}
