package server

import "time"

func newControlPlaneApp() *controlPlaneServer {
	return newControlPlaneAppEmpty()
}

func newControlPlaneAppEmpty() *controlPlaneServer {
	tables := newMemoryTableStore()
	return &controlPlaneServer{
		tables:             tables,
		sessionCredentials: newSessionCredentialVault(time.Now),
		loginRateLimits:    map[string]loginFailure{},
	}
}
