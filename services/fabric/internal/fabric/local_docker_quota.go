package fabric

import "errors"

var ErrLocalDockerStorageQuotaUnavailable = errors.New("local_docker_storage_quota_unavailable")
var ErrLocalDockerStorageQuotaReadbackMismatch = errors.New("local_docker_storage_quota_readback_mismatch")

type localDockerProjectQuotaState struct {
	ProjectID      uint32
	HardLimitBytes uint64
	Inherits       bool
}

type localDockerProjectQuotaRecord struct {
	HardLimitBytes uint64
	SoftLimitBytes uint64
	CurrentBytes   uint64
	CurrentInodes  uint64
}

// localDockerProjectQuota is the provider-owned host capability. A nil or
// unsupported implementation must never be replaced by a free-space check.
type localDockerProjectQuota interface {
	Preflight(root string) error
	Apply(path string, projectID uint32, hardLimitBytes uint64) error
	Read(path string) (localDockerProjectQuotaState, error)
	ReadProject(root string, projectID uint32) (localDockerProjectQuotaRecord, error)
	// Clear is idempotent and succeeds only after hard and soft limits read back as zero.
	Clear(path string, projectID uint32) error
}
