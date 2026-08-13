package clients

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const FabricCapabilityHeader = "X-OPL-Fabric-Capability"

type FabricClient interface {
	Catalog(ctx context.Context) (FabricCatalog, error)
	CreateComputeAllocation(ctx context.Context, input ComputeAllocationInput, idempotencyKey string) (ComputeAllocation, error)
	CreateStorageVolume(ctx context.Context, input StorageVolumeInput, idempotencyKey string) (StorageVolume, error)
	CreateStorageAttachment(ctx context.Context, input StorageAttachmentInput, idempotencyKey string) (StorageAttachment, error)
	WriteGatewaySecret(ctx context.Context, input GatewaySecretWriteInput, idempotencyKey string) (GatewaySecretWriteResult, error)
	CreateWorkspaceRuntime(ctx context.Context, input WorkspaceRuntimeInput, idempotencyKey string) (WorkspaceRuntime, error)
	WorkspaceRuntimeStatus(ctx context.Context, workspaceID string) (WorkspaceRuntime, error)
	Readiness(ctx context.Context) (map[string]any, error)
}

type FabricWorkspaceRuntimeGatewaySecretClient interface {
	BindWorkspaceRuntimeGatewaySecret(context.Context, WorkspaceRuntimeGatewaySecretInput, string) (WorkspaceRuntimeGatewaySecretBinding, error)
	WorkspaceRuntimeGatewaySecret(context.Context, string) (WorkspaceRuntimeGatewaySecretBinding, error)
}

type FabricProviderFactsClient interface {
	ProviderFactsBatch(context.Context, ProviderFactsBatchInput) (ProviderFactsBatch, error)
}

// FabricRuntimeHealthClient exposes Fabric's aggregate, read-only Runtime state.
// It deliberately returns no per-Workspace identities or credentials.
type FabricRuntimeHealthClient interface {
	RuntimeHealthSummary(context.Context) (RuntimeHealthSummary, error)
}

type FabricRenewalClient interface {
	RenewComputeAllocation(context.Context, string, string, string, string) (ProviderResourceMutation, error)
	RenewStorageVolume(context.Context, string, string, string, string) (ProviderResourceMutation, error)
}

type FabricMonthlyPreflightClient interface {
	MonthlyPreflight(context.Context, MonthlyPreflightInput) (MonthlyPreflight, error)
}

type FabricWorkspaceDeleteClient interface {
	DestroyWorkspaceRuntime(context.Context, string, string, string) (WorkspaceRuntime, error)
	DetachStorageAttachment(context.Context, string, string, string, string) (StorageAttachment, error)
	DestroyStorageVolume(context.Context, string, string, string, string) (StorageVolume, error)
	DestroyComputeAllocation(context.Context, string, string, string, string) (ComputeAllocation, error)
	ReadComputeAllocation(context.Context, string) (ComputeAllocation, error)
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
}

type MonthlyPreflightInput struct {
	ResourceType string `json:"resourceType"`
	PackageID    string `json:"packageId"`
	SizeGB       int    `json:"sizeGb,omitempty"`
	Zone         string `json:"zone,omitempty"`
}

type MonthlyPreflight struct {
	ResourceType     string  `json:"resourceType"`
	PackageID        string  `json:"packageId"`
	SizeGB           int     `json:"sizeGb"`
	Zone             string  `json:"zone"`
	Available        bool    `json:"available"`
	ChargeType       string  `json:"chargeType"`
	PeriodMonths     int     `json:"periodMonths"`
	RenewFlag        string  `json:"renewFlag"`
	ProviderPriceCNY float64 `json:"providerPriceCny"`
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
	NodePoolID         string            `json:"nodePoolId,omitempty"`
	InstanceID         string            `json:"instanceId,omitempty"`
	CVMInstanceID      string            `json:"cvmInstanceId,omitempty"`
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
	OperationID        string            `json:"operationId,omitempty"`
	AccountID          string            `json:"accountId,omitempty"`
	Provider           string            `json:"provider,omitempty"`
	ProviderResourceID string            `json:"providerResourceId,omitempty"`
	ProviderRequestID  string            `json:"providerRequestId"`
	WorkspaceID        string            `json:"workspaceId"`
	Status             string            `json:"status"`
	SizeGB             int               `json:"sizeGb,omitempty"`
	CBSStatus          string            `json:"cbsStatus,omitempty"`
	DiskType           string            `json:"diskType,omitempty"`
	RenewFlag          string            `json:"renewFlag,omitempty"`
	Deadline           string            `json:"deadline,omitempty"`
	Zone               string            `json:"zone,omitempty"`
	ProviderData       map[string]string `json:"providerData,omitempty"`
	CostTags           map[string]string `json:"costTags,omitempty"`
}

type StorageAttachmentInput struct {
	AccountID   string `json:"accountId"`
	WorkspaceID string `json:"workspaceId"`
	ComputeID   string `json:"computeId"`
	VolumeID    string `json:"volumeId"`
}

type StorageAttachment struct {
	ID                   string `json:"id"`
	OperationID          string `json:"operationId,omitempty"`
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
	AccountID             string `json:"accountId"`
	WorkspaceID           string `json:"workspaceId"`
	ComputeID             string `json:"computeId"`
	VolumeID              string `json:"volumeId"`
	AttachmentID          string `json:"attachmentId"`
	AttachmentOperationID string `json:"attachmentOperationId"`
	RuntimeOperationID    string `json:"runtimeOperationId"`
	ImageID               string `json:"imageId"`
	GatewaySecretRef      string `json:"gatewaySecretRef"`
}

type GatewaySecretWriteInput struct {
	AccountID         string `json:"accountId"`
	WorkspaceID       string `json:"workspaceId"`
	WorkspaceAPIKeyID int64  `json:"workspaceApiKeyId"`
	Fingerprint       string `json:"fingerprint"`
	GatewayAPIKey     string `json:"gatewayApiKey"`
}

type GatewaySecretWriteResult struct {
	SecretRef   string `json:"secretRef"`
	Version     string `json:"version"`
	Fingerprint string `json:"fingerprint"`
}

type WorkspaceRuntimeGatewaySecretInput struct {
	AccountID         string `json:"accountId"`
	WorkspaceID       string `json:"workspaceId"`
	WorkspaceAPIKeyID int64  `json:"workspaceApiKeyId"`
	SecretRef         string `json:"secretRef"`
	Fingerprint       string `json:"fingerprint"`
}

type WorkspaceRuntimeGatewaySecretBinding struct {
	WorkspaceID       string `json:"workspaceId"`
	WorkspaceAPIKeyID int64  `json:"workspaceApiKeyId"`
	SecretRef         string `json:"secretRef"`
	Fingerprint       string `json:"fingerprint"`
	Bound             bool   `json:"bound"`
}

type ProviderFactInput struct {
	AccountID    string `json:"accountId"`
	WorkspaceID  string `json:"workspaceId"`
	ResourceType string `json:"resourceType"`
	ResourceID   string `json:"resourceId"`
}

type ProviderFactsBatchInput struct {
	Items []ProviderFactInput `json:"items"`
}

type ProviderResourceFacts struct {
	PackageOrSpec string `json:"packageOrSpec,omitempty"`
	ProviderID    string `json:"providerId,omitempty"`
	Zone          string `json:"zone,omitempty"`
	Status        string `json:"status,omitempty"`
	CreatedAt     string `json:"createdAt,omitempty"`
	ExpiresAt     string `json:"expiresAt,omitempty"`
	LastReadAt    string `json:"lastReadAt,omitempty"`
}

type ProviderFact struct {
	AccountID    string                `json:"accountId"`
	WorkspaceID  string                `json:"workspaceId"`
	ResourceType string                `json:"resourceType"`
	ResourceID   string                `json:"resourceId"`
	Available    bool                  `json:"available"`
	Facts        ProviderResourceFacts `json:"facts,omitempty"`
	ErrorCode    string                `json:"errorCode,omitempty"`
}

type ProviderFactsBatch struct {
	Items []ProviderFact `json:"items"`
}

type ProviderResourceMutation struct {
	ID                string `json:"id"`
	OperationID       string `json:"operationId,omitempty"`
	AccountID         string `json:"accountId,omitempty"`
	WorkspaceID       string `json:"workspaceId,omitempty"`
	Status            string `json:"status"`
	ProviderRequestID string `json:"providerRequestId,omitempty"`
}

type RuntimeHealthSummary struct {
	Total   int `json:"total"`
	Ready   int `json:"ready"`
	Unready int `json:"unready"`
}

type WorkspaceRuntime struct {
	ID                string                 `json:"id"`
	OperationID       string                 `json:"operationId,omitempty"`
	WorkspaceID       string                 `json:"workspaceId"`
	URL               string                 `json:"url"`
	Status            string                 `json:"status"`
	ServiceName       string                 `json:"serviceName"`
	ProviderRequestID string                 `json:"providerRequestId,omitempty"`
	Access            WorkspaceRuntimeAccess `json:"access,omitempty"`
	Ready             bool                   `json:"ready"`
	Checks            []any                  `json:"checks"`
}

type WorkspaceRuntimeAccess struct {
	Username          string `json:"username,omitempty"`
	Password          string `json:"password,omitempty"`
	CredentialStatus  string `json:"credentialStatus,omitempty"`
	CredentialVersion string `json:"credentialVersion,omitempty"`
	SecretRef         string `json:"secretRef,omitempty"`
}

type fabricHTTPClient struct {
	baseURL       string
	token         string
	capabilityKey string
	client        *http.Client
}

func NewFabricHTTPClientWithCapability(baseURL, token, capabilityKey string, client *http.Client) FabricClient {
	if client == nil {
		client = http.DefaultClient
	}
	return &fabricHTTPClient{baseURL: baseURL, token: token, capabilityKey: capabilityKey, client: client}
}

type fabricMutationScope struct {
	AccountID    string
	WorkspaceID  string
	ResourceKind string
	ResourceID   string
	Action       string
}

type fabricCapabilityClaims struct {
	Version      int    `json:"version"`
	Caller       string `json:"caller"`
	AccountID    string `json:"accountId"`
	WorkspaceID  string `json:"workspaceId"`
	ResourceKind string `json:"resourceKind"`
	ResourceID   string `json:"resourceId"`
	Action       string `json:"action"`
	OperationID  string `json:"operationId"`
	ExpiresAt    int64  `json:"expiresAt"`
	BodySHA256   string `json:"bodySha256"`
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

func (c *fabricHTTPClient) CreateComputeAllocation(ctx context.Context, input ComputeAllocationInput, idempotencyKey string) (ComputeAllocation, error) {
	var result ComputeAllocation
	err := c.postMutation(ctx, "/fabric/compute-allocations", input, idempotencyKey, fabricMutationScope{
		AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceKind: "compute_allocation", ResourceID: input.ID, Action: "create_compute_allocation",
	}, &result)
	if err != nil {
		var httpErr *FabricHTTPError
		if errors.As(err, &httpErr) {
			_ = json.Unmarshal([]byte(httpErr.Body), &result)
		}
	}
	return result, err
}

func (c *fabricHTTPClient) RenewComputeAllocation(ctx context.Context, accountID, workspaceID, id, idempotencyKey string) (ProviderResourceMutation, error) {
	var result ProviderResourceMutation
	input := map[string]string{"accountId": accountID, "workspaceId": workspaceID}
	err := c.postMutation(ctx, "/fabric/compute-allocations/"+url.PathEscape(id)+"/renew", input, idempotencyKey, fabricMutationScope{
		AccountID: accountID, WorkspaceID: workspaceID, ResourceKind: "compute_allocation", ResourceID: id, Action: "renew_compute_allocation",
	}, &result)
	return result, err
}

func (c *fabricHTTPClient) DestroyComputeAllocation(ctx context.Context, accountID, workspaceID, id, idempotencyKey string) (ComputeAllocation, error) {
	var result ComputeAllocation
	input := map[string]string{"accountId": accountID, "workspaceId": workspaceID}
	err := c.postMutation(ctx, "/fabric/compute-allocations/"+url.PathEscape(id)+"/destroy", input, idempotencyKey, fabricMutationScope{
		AccountID: accountID, WorkspaceID: workspaceID, ResourceKind: "compute_allocation", ResourceID: id, Action: "destroy_compute_allocation",
	}, &result)
	return result, err
}

func (c *fabricHTTPClient) ReadComputeAllocation(ctx context.Context, id string) (ComputeAllocation, error) {
	var result ComputeAllocation
	err := c.get(ctx, "/fabric/compute-allocations/"+url.PathEscape(id), &result)
	return result, err
}

func (c *fabricHTTPClient) CreateStorageVolume(ctx context.Context, input StorageVolumeInput, idempotencyKey string) (StorageVolume, error) {
	var result StorageVolume
	err := c.postMutation(ctx, "/fabric/storage-volumes", input, idempotencyKey, fabricMutationScope{
		AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceKind: "storage_volume", ResourceID: input.ID, Action: "create_storage_volume",
	}, &result)
	return result, err
}

func (c *fabricHTTPClient) RenewStorageVolume(ctx context.Context, accountID, workspaceID, id, idempotencyKey string) (ProviderResourceMutation, error) {
	var result ProviderResourceMutation
	input := map[string]string{"accountId": accountID, "workspaceId": workspaceID}
	err := c.postMutation(ctx, "/fabric/storage-volumes/"+url.PathEscape(id)+"/renew", input, idempotencyKey, fabricMutationScope{
		AccountID: accountID, WorkspaceID: workspaceID, ResourceKind: "storage_volume", ResourceID: id, Action: "renew_storage_volume",
	}, &result)
	return result, err
}

func (c *fabricHTTPClient) DestroyStorageVolume(ctx context.Context, accountID, workspaceID, id, idempotencyKey string) (StorageVolume, error) {
	var result StorageVolume
	input := map[string]string{"accountId": accountID, "workspaceId": workspaceID}
	err := c.postMutation(ctx, "/fabric/storage-volumes/"+url.PathEscape(id)+"/destroy", input, idempotencyKey, fabricMutationScope{
		AccountID: accountID, WorkspaceID: workspaceID, ResourceKind: "storage_volume", ResourceID: id, Action: "destroy_storage_volume",
	}, &result)
	return result, err
}

func (c *fabricHTTPClient) CreateStorageAttachment(ctx context.Context, input StorageAttachmentInput, idempotencyKey string) (StorageAttachment, error) {
	var result StorageAttachment
	err := c.postMutation(ctx, "/fabric/storage-attachments", input, idempotencyKey, fabricMutationScope{
		AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceKind: "storage_attachment", ResourceID: input.ComputeID + ":" + input.VolumeID, Action: "create_storage_attachment",
	}, &result)
	return result, err
}

func (c *fabricHTTPClient) DetachStorageAttachment(ctx context.Context, accountID, workspaceID, id, idempotencyKey string) (StorageAttachment, error) {
	var result StorageAttachment
	input := map[string]string{"accountId": accountID, "workspaceId": workspaceID}
	err := c.postMutation(ctx, "/fabric/storage-attachments/"+url.PathEscape(id)+"/detach", input, idempotencyKey, fabricMutationScope{
		AccountID: accountID, WorkspaceID: workspaceID, ResourceKind: "storage_attachment", ResourceID: id, Action: "detach_storage_attachment",
	}, &result)
	return result, err
}

func (c *fabricHTTPClient) WriteGatewaySecret(ctx context.Context, input GatewaySecretWriteInput, idempotencyKey string) (GatewaySecretWriteResult, error) {
	var result GatewaySecretWriteResult
	if err := c.postMutation(ctx, "/fabric/gateway-secrets", input, idempotencyKey, fabricMutationScope{
		AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceKind: "gateway_secret", ResourceID: input.WorkspaceID, Action: "upsert_gateway_secret",
	}, &result); err != nil {
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

func (c *fabricHTTPClient) BindWorkspaceRuntimeGatewaySecret(ctx context.Context, input WorkspaceRuntimeGatewaySecretInput, idempotencyKey string) (WorkspaceRuntimeGatewaySecretBinding, error) {
	var result WorkspaceRuntimeGatewaySecretBinding
	err := c.postMutation(ctx, "/fabric/workspace-runtimes/"+url.PathEscape(input.WorkspaceID)+"/gateway-secret", input, idempotencyKey, fabricMutationScope{
		AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceKind: "workspace_runtime_gateway_secret", ResourceID: input.WorkspaceID, Action: "bind_workspace_runtime_gateway_secret",
	}, &result)
	return result, err
}

func (c *fabricHTTPClient) WorkspaceRuntimeGatewaySecret(ctx context.Context, workspaceID string) (WorkspaceRuntimeGatewaySecretBinding, error) {
	var result WorkspaceRuntimeGatewaySecretBinding
	err := c.get(ctx, "/fabric/workspace-runtimes/"+url.PathEscape(workspaceID)+"/gateway-secret", &result)
	return result, err
}

func (c *fabricHTTPClient) ProviderFactsBatch(ctx context.Context, input ProviderFactsBatchInput) (ProviderFactsBatch, error) {
	var result ProviderFactsBatch
	err := c.post(ctx, "/fabric/provider-facts/batch", input, "", &result)
	return result, err
}

func (c *fabricHTTPClient) RuntimeHealthSummary(ctx context.Context) (RuntimeHealthSummary, error) {
	var result RuntimeHealthSummary
	err := c.get(ctx, "/fabric/runtime-health-summary", &result)
	return result, err
}

func (c *fabricHTTPClient) CreateWorkspaceRuntime(ctx context.Context, input WorkspaceRuntimeInput, idempotencyKey string) (WorkspaceRuntime, error) {
	var result WorkspaceRuntime
	action := "create_workspace_runtime"
	if input.RuntimeOperationID != idempotencyKey {
		action = "update_workspace_runtime"
	}
	err := c.postMutation(ctx, "/fabric/workspace-runtimes", input, idempotencyKey, fabricMutationScope{
		AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceKind: "workspace_runtime", ResourceID: input.WorkspaceID, Action: action,
	}, &result)
	return result, err
}

func (c *fabricHTTPClient) DestroyWorkspaceRuntime(ctx context.Context, accountID, workspaceID, idempotencyKey string) (WorkspaceRuntime, error) {
	var result WorkspaceRuntime
	input := map[string]string{"accountId": accountID, "workspaceId": workspaceID}
	err := c.postMutation(ctx, "/fabric/workspace-runtimes/"+url.PathEscape(workspaceID)+"/destroy", input, idempotencyKey, fabricMutationScope{
		AccountID: accountID, WorkspaceID: workspaceID, ResourceKind: "workspace_runtime", ResourceID: workspaceID, Action: "destroy_workspace_runtime",
	}, &result)
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
	body, err := io.ReadAll(io.LimitReader(res.Body, maxFabricResponseBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxFabricResponseBytes {
		return errors.New("fabric response too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err != nil {
			return err
		}
		return errors.New("fabric response contains multiple JSON values")
	}
	return nil
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

func (c *fabricHTTPClient) postMutation(ctx context.Context, path string, input any, idempotencyKey string, scope fabricMutationScope, output any) error {
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
	if c.capabilityKey != "" {
		capability, err := signFabricCapability(c.capabilityKey, scope, idempotencyKey, body, time.Now())
		if err != nil {
			return err
		}
		req.Header.Set(FabricCapabilityHeader, capability)
	}
	return c.doJSON(req, output)
}

func signFabricCapability(key string, scope fabricMutationScope, operationID string, body []byte, now time.Time) (string, error) {
	digest := sha256.Sum256(body)
	claims := fabricCapabilityClaims{
		Version: 1, Caller: "control-plane", AccountID: scope.AccountID, WorkspaceID: scope.WorkspaceID,
		ResourceKind: scope.ResourceKind, ResourceID: scope.ResourceID, Action: scope.Action,
		OperationID: operationID, ExpiresAt: now.Add(time.Minute).Unix(), BodySHA256: hex.EncodeToString(digest[:]),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
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
	body, _ := io.ReadAll(io.LimitReader(res.Body, maxFabricErrorBodyBytes))
	return &FabricHTTPError{StatusCode: res.StatusCode, Body: string(body)}
}

const (
	maxFabricResponseBytes  = 1 << 20
	maxFabricErrorBodyBytes = 64 << 10
)
