package server

import (
	"net/http"
	"time"

	"opl-cloud/services/control-plane/internal/controlplane"
)

func NewServer(service *controlplane.Service) http.Handler {
	handler, err := NewPersistentServer(service, newMemoryTableStore())
	if err != nil {
		panic(err)
	}
	return handler
}

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
