package domain

type WorkspaceProjection struct {
	ID                 string `json:"id"`
	AccountID          string `json:"accountId"`
	OwnerID            string `json:"ownerId"`
	Name               string `json:"name"`
	PackageID          string `json:"packageId"`
	URL                string `json:"url"`
	Status             string `json:"status"`
	HoldID             string `json:"holdId"`
	ComputeID          string `json:"computeId"`
	VolumeID           string `json:"volumeId"`
	RuntimeID          string `json:"runtimeId"`
	RuntimeServiceName string `json:"runtimeServiceName,omitempty"`
	EvidenceID         string `json:"evidenceId"`
}
