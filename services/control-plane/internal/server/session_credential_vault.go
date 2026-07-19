package server

import (
	"errors"
	"sync"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
)

var errInvalidSessionCredential = errors.New("invalid_session_credential")

type SessionDelegatedCredential = clients.SessionDelegatedCredential

type SessionCredentialVault struct {
	mu          sync.Mutex
	credentials map[string]SessionDelegatedCredential
	now         func() time.Time
}

func newSessionCredentialVault(now func() time.Time) *SessionCredentialVault {
	if now == nil {
		now = time.Now
	}
	return &SessionCredentialVault{credentials: map[string]SessionDelegatedCredential{}, now: now}
}

func (vault *SessionCredentialVault) Put(sessionKey string, credential SessionDelegatedCredential) error {
	if !validSessionLookupKey(sessionKey) || credential.Bearer == "" || !credential.ExpiresAt.After(vault.now().UTC()) {
		return errInvalidSessionCredential
	}
	vault.mu.Lock()
	defer vault.mu.Unlock()
	vault.credentials[sessionKey] = credential
	return nil
}

func (vault *SessionCredentialVault) Get(sessionKey string) (SessionDelegatedCredential, bool) {
	vault.mu.Lock()
	defer vault.mu.Unlock()
	credential, ok := vault.credentials[sessionKey]
	if !ok || !credential.ExpiresAt.After(vault.now().UTC()) {
		delete(vault.credentials, sessionKey)
		return SessionDelegatedCredential{}, false
	}
	return credential, true
}

func (vault *SessionCredentialVault) Delete(sessionKeys ...string) {
	vault.mu.Lock()
	defer vault.mu.Unlock()
	for _, sessionKey := range sessionKeys {
		delete(vault.credentials, sessionKey)
	}
}
