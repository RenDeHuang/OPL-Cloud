package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

type memoryStateStore struct {
	path string
	mu   sync.Mutex
}

func NewMemoryStateStore(path string) StateStore {
	return &memoryStateStore{path: path}
}

func (s *memoryStateStore) Load(_ context.Context) (controlPlaneState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return controlPlaneState{}, nil
	}
	if err != nil {
		return controlPlaneState{}, err
	}
	var state controlPlaneState
	if err := json.Unmarshal(data, &state); err != nil {
		return controlPlaneState{}, err
	}
	return state, nil
}

func (s *memoryStateStore) Save(_ context.Context, state controlPlaneState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
