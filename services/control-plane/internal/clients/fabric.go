package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type FabricClient interface {
	Catalog(ctx context.Context) (FabricCatalog, error)
	CreateComputeAllocation(ctx context.Context, input ComputeAllocationInput, idempotencyKey string) (ComputeAllocation, error)
	GetComputeAllocation(ctx context.Context, id string) (ComputeAllocation, error)
	DestroyComputeAllocation(ctx context.Context, id string, idempotencyKey string) (ComputeAllocation, error)
	CreateStorageVolume(ctx context.Context, input StorageVolumeInput, idempotencyKey string) (StorageVolume, error)
	DestroyStorageVolume(ctx context.Context, id string, idempotencyKey string) (StorageVolume, error)
	CreateStorageAttachment(ctx context.Context, input StorageAttachmentInput, idempotencyKey string) (StorageAttachment, error)
	DetachStorageAttachment(ctx context.Context, id string, idempotencyKey string) (StorageAttachment, error)
	CreateWorkspaceRuntime(ctx context.Context, input WorkspaceRuntimeInput, idempotencyKey string) (WorkspaceRuntime, error)
	WorkspaceRuntimeStatus(ctx context.Context, workspaceID string) (WorkspaceRuntime, error)
	Readiness(ctx context.Context) (map[string]any, error)
	ListOperations(ctx context.Context) ([]FabricOperation, error)
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
}

type ComputeAllocation struct {
	ID                 string `json:"id"`
	AccountID          string `json:"accountId"`
	WorkspaceID        string `json:"workspaceId"`
	PackageID          string `json:"packageId"`
	Status             string `json:"status"`
	Provider           string `json:"provider"`
	ProviderResourceID string `json:"providerResourceId"`
	ProviderRequestID  string `json:"providerRequestId"`
	ServiceName        string `json:"serviceName"`
	PoolID             string `json:"poolId,omitempty"`
	NodePoolID         string `json:"nodePoolId,omitempty"`
	InstanceID         string `json:"instanceId,omitempty"`
	CVMInstanceID      string `json:"cvmInstanceId,omitempty"`
	NodeName           string `json:"nodeName,omitempty"`
	MachineName        string `json:"machineName,omitempty"`
	PrivateIP          string `json:"privateIp,omitempty"`
	PublicIP           string `json:"publicIp,omitempty"`
	BillingStatus      string `json:"billingStatus,omitempty"`
	HoldID             string `json:"holdId,omitempty"`
	HoldAmountCents    int64  `json:"holdAmountCents,omitempty"`
	HoldReleaseID      string `json:"holdReleaseId,omitempty"`
	Wallet             Wallet `json:"wallet,omitempty"`
}

type StorageVolumeInput struct {
	ID          string `json:"id,omitempty"`
	AccountID   string `json:"accountId"`
	WorkspaceID string `json:"workspaceId"`
	SizeGB      int    `json:"sizeGb"`
}

type StorageVolume struct {
	ID                 string `json:"id"`
	AccountID          string `json:"accountId,omitempty"`
	Provider           string `json:"provider,omitempty"`
	ProviderResourceID string `json:"providerResourceId,omitempty"`
	ProviderRequestID  string `json:"providerRequestId"`
	WorkspaceID        string `json:"workspaceId"`
	Status             string `json:"status"`
	SizeGB             int    `json:"sizeGb,omitempty"`
	StorageClass       string `json:"storageClass,omitempty"`
	BillingStatus      string `json:"billingStatus,omitempty"`
	HoldID             string `json:"holdId,omitempty"`
	HoldAmountCents    int64  `json:"holdAmountCents,omitempty"`
	HoldReleaseID      string `json:"holdReleaseId,omitempty"`
	Wallet             Wallet `json:"wallet,omitempty"`
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
	WorkspaceID string `json:"workspaceId"`
	ComputeID   string `json:"computeId"`
	VolumeID    string `json:"volumeId"`
	ImageID     string `json:"imageId"`
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
	client  *http.Client
}

func NewFabricHTTPClient(baseURL string, client *http.Client) FabricClient {
	if client == nil {
		client = http.DefaultClient
	}
	return &fabricHTTPClient{baseURL: baseURL, client: client}
}

func (c *fabricHTTPClient) Catalog(ctx context.Context) (FabricCatalog, error) {
	var result FabricCatalog
	err := c.get(ctx, "/fabric/catalog", &result)
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

func (c *fabricHTTPClient) CreateWorkspaceRuntime(ctx context.Context, input WorkspaceRuntimeInput, idempotencyKey string) (WorkspaceRuntime, error) {
	var result WorkspaceRuntime
	err := c.post(ctx, "/fabric/workspace-runtimes", input, idempotencyKey, &result)
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
	req.Header.Set("Idempotency-Key", idempotencyKey)
	res, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(res.Body)
		return fmt.Errorf("fabric request failed: status %d: %s", res.StatusCode, string(body))
	}
	return json.NewDecoder(res.Body).Decode(output)
}

func (c *fabricHTTPClient) get(ctx context.Context, path string, output any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	res, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(res.Body)
		return fmt.Errorf("fabric request failed: status %d: %s", res.StatusCode, string(body))
	}
	return json.NewDecoder(res.Body).Decode(output)
}
