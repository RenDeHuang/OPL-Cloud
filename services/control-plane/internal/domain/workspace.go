package domain

type WorkspaceProjection struct {
	ID                 string `json:"id"`
	AccountID          string `json:"accountId"`
	OwnerID            string `json:"ownerId"`
	Name               string `json:"name"`
	PackageID          string `json:"packageId"`
	Provider           string `json:"provider"`
	URL                string `json:"url"`
	Status             string `json:"status"`
	ComputeID          string `json:"computeAllocationId"`
	VolumeID           string `json:"storageId"`
	AttachmentID       string `json:"attachmentId"`
	RuntimeID          string `json:"runtimeId"`
	RuntimeServiceName string `json:"runtimeServiceName,omitempty"`
	EvidenceID         string `json:"evidenceId"`
}
