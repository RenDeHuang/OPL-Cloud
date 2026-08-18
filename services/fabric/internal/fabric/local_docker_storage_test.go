package fabric

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func localDockerStorageTestRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestLocalDockerStorageUsesWorkspaceScopedHostDirectoriesAndSurvivesProviderRestart(t *testing.T) {
	root := localDockerStorageTestRoot(t)
	input := StorageVolumeInput{
		ID: "storage-alpha", OperationID: "storage-operation", IdempotencyKey: "storage-idempotency",
		AccountID: "acct-alpha", WorkspaceID: "ws-alpha", SizeGB: 10,
	}
	provider := newLocalDockerProvider(LocalDockerProviderConfig{HostStorageRoot: root}, &recordingDockerRunner{})

	volume, err := provider.CreateStorageVolume(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	workspaceRoot := filepath.Join(root, localDockerName("opl-workspace", input.WorkspaceID))
	for _, path := range []string{workspaceRoot, filepath.Join(workspaceRoot, "data"), filepath.Join(workspaceRoot, "projects")} {
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("storage path=%q info=%#v err=%v", path, info, statErr)
		}
	}
	if volume.ProviderResourceID != "directory/"+localDockerName("opl-workspace", input.WorkspaceID) {
		t.Fatalf("providerResourceId=%q", volume.ProviderResourceID)
	}
	secondInput := input
	secondInput.ID, secondInput.WorkspaceID = "storage-beta", "ws-beta"
	second, err := provider.CreateStorageVolume(context.Background(), secondInput)
	if err != nil {
		t.Fatal(err)
	}
	if second.ProviderResourceID == volume.ProviderResourceID {
		t.Fatalf("workspace storage paths collided: %q", second.ProviderResourceID)
	}

	restarted := newLocalDockerProvider(LocalDockerProviderConfig{HostStorageRoot: root}, &recordingDockerRunner{})
	readback, err := restarted.ReadStorageVolume(context.Background(), volume)
	if err != nil || readback.Status != "ready" || readback.ProviderResourceID != volume.ProviderResourceID {
		t.Fatalf("readback=%#v err=%v", readback, err)
	}
}

func TestLocalDockerStorageDestroyRequiresExactIdentityAndIsIdempotent(t *testing.T) {
	root := localDockerStorageTestRoot(t)
	provider := newLocalDockerProvider(LocalDockerProviderConfig{HostStorageRoot: root}, &recordingDockerRunner{})
	input := StorageVolumeInput{
		ID: "storage-delete", OperationID: "storage-operation", IdempotencyKey: "storage-idempotency",
		AccountID: "acct-alpha", WorkspaceID: "ws-delete", SizeGB: 10,
	}
	volume, err := provider.CreateStorageVolume(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, localDockerName("opl-workspace", input.WorkspaceID), "data", "marker")
	if err := os.WriteFile(marker, []byte("persistent"), 0600); err != nil {
		t.Fatal(err)
	}
	foreign := volume
	foreign.AccountID = "acct-foreign"
	if _, err := provider.DestroyStorageVolume(context.Background(), foreign); err == nil {
		t.Fatal("foreign account destroyed storage")
	}
	if body, err := os.ReadFile(marker); err != nil || string(body) != "persistent" {
		t.Fatalf("marker changed after rejected destroy body=%q err=%v", body, err)
	}
	if err := os.RemoveAll(filepath.Join(root, localDockerName("opl-workspace", input.WorkspaceID), "projects")); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		destroyed, err := provider.DestroyStorageVolume(context.Background(), volume)
		if err != nil || destroyed.Status != "destroyed" {
			t.Fatalf("attempt=%d destroyed=%#v err=%v", attempt, destroyed, err)
		}
	}
	if _, err := os.Lstat(filepath.Dir(filepath.Dir(marker))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace storage still exists: %v", err)
	}
}

func TestLocalDockerStorageFailsClosedOnWorkspaceSymlink(t *testing.T) {
	root := localDockerStorageTestRoot(t)
	outside := t.TempDir()
	workspaceID := "ws-symlink"
	if err := os.Symlink(outside, filepath.Join(root, localDockerName("opl-workspace", workspaceID))); err != nil {
		t.Fatal(err)
	}
	provider := newLocalDockerProvider(LocalDockerProviderConfig{HostStorageRoot: root}, &recordingDockerRunner{})
	_, err := provider.CreateStorageVolume(context.Background(), StorageVolumeInput{
		ID: "storage-symlink", OperationID: "storage-operation", IdempotencyKey: "storage-idempotency",
		AccountID: "acct-alpha", WorkspaceID: workspaceID, SizeGB: 10,
	})
	if err == nil {
		t.Fatal("expected symlink storage path rejection")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "data")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("outside path mutated: %v", statErr)
	}
}

func TestLocalDockerReadinessRejectsMissingHostStorageRoot(t *testing.T) {
	provider := newLocalDockerProvider(LocalDockerProviderConfig{GatewaySecretRoot: localDockerSecretTestRoot(t)}, &recordingDockerRunner{})
	if _, err := provider.Readiness(context.Background()); err == nil {
		t.Fatal("expected missing host storage root rejection")
	}
}

func TestLocalDockerStoragePreflightChecksHostCapacityBeforeAdmission(t *testing.T) {
	root := localDockerStorageTestRoot(t)
	provider := newLocalDockerProvider(LocalDockerProviderConfig{HostStorageRoot: root}, &recordingDockerRunner{})
	if _, err := provider.MonthlyPreflight(context.Background(), MonthlyPreflightInput{
		ResourceType: "storage", PackageID: "basic", SizeGB: math.MaxInt / (1024 * 1024 * 1024), Zone: "local",
	}); err == nil || err.Error() != "local_docker_storage_capacity_insufficient" {
		t.Fatalf("oversized storage preflight err=%v", err)
	}
}
