//go:build !linux

package fabric

type unsupportedLocalDockerProjectQuota struct{}

func newLocalDockerProjectQuota(string) localDockerProjectQuota {
	return unsupportedLocalDockerProjectQuota{}
}

func (unsupportedLocalDockerProjectQuota) Preflight(string) error {
	return ErrLocalDockerStorageQuotaUnavailable
}
func (unsupportedLocalDockerProjectQuota) Apply(string, uint32, uint64) error {
	return ErrLocalDockerStorageQuotaUnavailable
}
func (unsupportedLocalDockerProjectQuota) Read(string) (localDockerProjectQuotaState, error) {
	return localDockerProjectQuotaState{}, ErrLocalDockerStorageQuotaUnavailable
}

func (unsupportedLocalDockerProjectQuota) ReadProject(string, uint32) (localDockerProjectQuotaRecord, error) {
	return localDockerProjectQuotaRecord{}, ErrLocalDockerStorageQuotaUnavailable
}
func (unsupportedLocalDockerProjectQuota) Clear(string, uint32) error {
	return ErrLocalDockerStorageQuotaUnavailable
}
