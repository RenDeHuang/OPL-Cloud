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
	AccountID      string `json:"accountId"`
	WorkspaceID    string `json:"workspaceId"`
	PackageID      string `json:"packageId"`
	IdempotencyKey string `json:"-"`
	DryRun         bool   `json:"dryRun,omitempty"`
}

type ComputeAllocation struct {
	ID                string    `json:"id"`
	AccountID         string    `json:"accountId"`
	WorkspaceID       string    `json:"workspaceId"`
	PackageID         string    `json:"packageId"`
	Status            string    `json:"status"`
	Provider          string    `json:"provider"`
	ProviderRequestID string    `json:"providerRequestId"`
	CreatedAt         time.Time `json:"createdAt"`
}

type StorageVolumeInput struct {
	AccountID      string `json:"accountId"`
	WorkspaceID    string `json:"workspaceId"`
	SizeGB         int    `json:"sizeGb"`
	IdempotencyKey string `json:"-"`
}

type StorageVolume struct {
	ID                string    `json:"id"`
	WorkspaceID       string    `json:"workspaceId"`
	Status            string    `json:"status"`
	ProviderRequestID string    `json:"providerRequestId"`
	CreatedAt         time.Time `json:"createdAt"`
}

type StorageAttachmentInput struct {
	WorkspaceID    string `json:"workspaceId"`
	VolumeID       string `json:"volumeId"`
	IdempotencyKey string `json:"-"`
}

type StorageAttachment struct {
	ID                string    `json:"id"`
	WorkspaceID       string    `json:"workspaceId"`
	VolumeID          string    `json:"volumeId"`
	Status            string    `json:"status"`
	ProviderRequestID string    `json:"providerRequestId"`
	CreatedAt         time.Time `json:"createdAt"`
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
	ProviderRequestID string    `json:"providerRequestId"`
	CreatedAt         time.Time `json:"createdAt"`
}
