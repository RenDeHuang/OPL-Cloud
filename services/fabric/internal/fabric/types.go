package fabric

import "time"

type Catalog struct {
	SchemaVersion     int                `json:"schemaVersion"`
	Owner             string             `json:"owner"`
	WorkspacePackages []WorkspacePackage `json:"workspacePackages"`
	StorageClasses    []StorageClass     `json:"storageClasses"`
	IngressDomains    []IngressDomain    `json:"ingressDomains"`
}

type WorkspacePackage struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	ComputeProfileID string `json:"computeProfileId"`
	CPU              int    `json:"cpu"`
	MemoryGB         int    `json:"memoryGb"`
	DiskGB           int    `json:"diskGb"`
	Provider         string `json:"provider"`
	Available        bool   `json:"available"`
}

type StorageClass struct {
	ID               string `json:"id"`
	StorageClassName string `json:"storageClassName"`
	Provider         string `json:"provider"`
	Available        bool   `json:"available"`
}

type IngressDomain struct {
	ID          string `json:"id"`
	Host        string `json:"host"`
	PathPattern string `json:"pathPattern"`
	Available   bool   `json:"available"`
}

type ComputeAllocationInput struct {
	ID             string `json:"id,omitempty"`
	AccountID      string `json:"accountId"`
	WorkspaceID    string `json:"workspaceId"`
	PackageID      string `json:"packageId"`
	IdempotencyKey string `json:"-"`
	DryRun         bool   `json:"dryRun,omitempty"`
}

type ComputeAllocation struct {
	ID                 string            `json:"id"`
	AccountID          string            `json:"accountId"`
	WorkspaceID        string            `json:"workspaceId"`
	PackageID          string            `json:"packageId"`
	Status             string            `json:"status"`
	Provider           string            `json:"provider"`
	ProviderResourceID string            `json:"providerResourceId,omitempty"`
	ProviderRequestID  string            `json:"providerRequestId"`
	PoolID             string            `json:"poolId,omitempty"`
	NodePoolID         string            `json:"nodePoolId,omitempty"`
	InstanceID         string            `json:"instanceId,omitempty"`
	CVMInstanceID      string            `json:"cvmInstanceId,omitempty"`
	NodeName           string            `json:"nodeName,omitempty"`
	MachineName        string            `json:"machineName,omitempty"`
	PrivateIP          string            `json:"privateIp,omitempty"`
	PublicIP           string            `json:"publicIp,omitempty"`
	ServiceName        string            `json:"serviceName,omitempty"`
	NodeSelector       map[string]any    `json:"nodeSelector,omitempty"`
	ProviderData       map[string]string `json:"providerData,omitempty"`
	CreatedAt          time.Time         `json:"createdAt"`
}

type StorageVolumeInput struct {
	ID             string `json:"id,omitempty"`
	AccountID      string `json:"accountId"`
	WorkspaceID    string `json:"workspaceId"`
	SizeGB         int    `json:"sizeGb"`
	IdempotencyKey string `json:"-"`
}

type StorageVolume struct {
	ID                 string    `json:"id"`
	AccountID          string    `json:"accountId,omitempty"`
	WorkspaceID        string    `json:"workspaceId"`
	Status             string    `json:"status"`
	Provider           string    `json:"provider,omitempty"`
	ProviderResourceID string    `json:"providerResourceId,omitempty"`
	ProviderRequestID  string    `json:"providerRequestId"`
	SizeGB             int       `json:"sizeGb,omitempty"`
	StorageClass       string    `json:"storageClass,omitempty"`
	CreatedAt          time.Time `json:"createdAt"`
}

type StorageAttachmentInput struct {
	WorkspaceID    string `json:"workspaceId"`
	ComputeID      string `json:"computeId"`
	VolumeID       string `json:"volumeId"`
	IdempotencyKey string `json:"-"`
}

type StorageAttachment struct {
	ID                   string    `json:"id"`
	WorkspaceID          string    `json:"workspaceId"`
	ComputeID            string    `json:"computeId,omitempty"`
	VolumeID             string    `json:"volumeId"`
	Status               string    `json:"status"`
	Provider             string    `json:"provider,omitempty"`
	ProviderAttachmentID string    `json:"providerAttachmentId,omitempty"`
	ProviderRequestID    string    `json:"providerRequestId"`
	CreatedAt            time.Time `json:"createdAt"`
}

type WorkspaceRuntimeInput struct {
	WorkspaceID    string `json:"workspaceId"`
	ComputeID      string `json:"computeId"`
	VolumeID       string `json:"volumeId"`
	ImageID        string `json:"imageId"`
	IdempotencyKey string `json:"-"`
}

type WorkspaceRuntime struct {
	ID                string    `json:"id"`
	WorkspaceID       string    `json:"workspaceId"`
	URL               string    `json:"url"`
	Status            string    `json:"status"`
	ServiceName       string    `json:"serviceName,omitempty"`
	ProviderRequestID string    `json:"providerRequestId"`
	Ready             bool      `json:"ready,omitempty"`
	Checks            []Check   `json:"checks,omitempty"`
	CreatedAt         time.Time `json:"createdAt"`
}

type Check struct {
	Name    string         `json:"name"`
	OK      bool           `json:"ok"`
	Details map[string]any `json:"details,omitempty"`
}
