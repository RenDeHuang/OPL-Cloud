package fabric

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLaunchStageBindingReadbackTracksPersistedOperation(t *testing.T) {
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(testProvider{}, store)
	operation := newOperation("create_storage_volume", "storage_volume", "vol-alpha", "acct-alpha", "ws-alpha", "launch-alpha:storage", hashInput(map[string]string{"size": "10"}), time.Now().UTC())
	operation.ID, operation.Status, operation.CreatedAt = "fop-alpha", "started", time.Now().UTC()
	bindLaunchStageOperation(&operation, "storage", map[string]string{"compute": "ca-alpha", "storage": "vol-alpha"})
	fillOperationResource(&operation, StorageVolume{ID: "vol-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "pending"})
	if err := store.Append(context.Background(), operation); err != nil {
		t.Fatal(err)
	}

	started, err := service.LaunchStageBindingReadback(context.Background(), "launch-alpha", "storage")
	if err != nil || started.Available || started.Status != "started" || started.Binding.ExpectedResourceIDs["compute"] != "ca-alpha" || started.Binding.Digest == "" {
		t.Fatalf("started readback = %#v, %v", started, err)
	}

	terminal := operation
	terminal.ID, terminal.Status, terminal.CreatedAt = "fop-beta", "succeeded", time.Now().UTC().Add(time.Second)
	fillOperationResource(&terminal, StorageVolume{ID: "vol-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", Status: "ready"})
	if err := store.Append(context.Background(), terminal); err != nil {
		t.Fatal(err)
	}
	finished, err := service.LaunchStageBindingReadback(context.Background(), "launch-alpha", "storage")
	if err != nil || !finished.Available || finished.Status != "succeeded" || finished.Binding.Digest != started.Binding.Digest {
		t.Fatalf("finished readback = %#v, %v", finished, err)
	}
}

func TestLaunchStageBindingReadbackRejectsConflictingPersistedIdentity(t *testing.T) {
	store := NewMemoryOperationStore()
	service := NewServiceWithOperationStore(testProvider{}, store)
	for _, requestHash := range []string{hashInput("first"), hashInput("second")} {
		operation := newOperation("create_storage_volume", "storage_volume", "vol-alpha", "acct-alpha", "ws-alpha", "launch-alpha:storage", requestHash, time.Now().UTC())
		operation.ID, operation.Status = fabricID("fop", requestHash, time.Now().UTC()), "started"
		bindLaunchStageOperation(&operation, "storage", map[string]string{"storage": "vol-alpha"})
		_ = store.Append(context.Background(), operation)
	}
	if _, err := service.LaunchStageBindingReadback(context.Background(), "launch-alpha", "storage"); !errors.Is(err, ErrLaunchStageBindingConflict) {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestLaunchStageBindingRequiresExplicitLaunchStageIdentity(t *testing.T) {
	operation := newOperation("create_storage_volume", "storage_volume", "vol-alpha", "acct-alpha", "ws-alpha", "ordinary-key", hashInput("request"), time.Now().UTC())
	bindLaunchStageOperation(&operation, "storage", map[string]string{"storage": "vol-alpha"})
	if _, ok := decodeLaunchStageBinding(operation); ok {
		t.Fatal("ordinary operation acquired a fabricated Launch binding")
	}
}
