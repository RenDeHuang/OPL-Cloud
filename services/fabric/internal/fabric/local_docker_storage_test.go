package fabric

import (
	"context"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type fakeLocalDockerProjectQuota struct {
	mu          sync.Mutex
	states      []fakeLocalDockerProjectQuotaEntry
	cleared     map[uint32]struct{}
	inheritDirs bool
	clearErr    error
	clearCalls  int
}

type fakeLocalDockerProjectQuotaEntry struct {
	info  os.FileInfo
	state localDockerProjectQuotaState
}

var localDockerStorageTestQuotas sync.Map

func newFakeLocalDockerProjectQuota() *fakeLocalDockerProjectQuota {
	return &fakeLocalDockerProjectQuota{cleared: make(map[uint32]struct{}), inheritDirs: true}
}

func (q *fakeLocalDockerProjectQuota) Preflight(string) error { return nil }

func (q *fakeLocalDockerProjectQuota) Apply(path string, projectID uint32, hardLimitBytes uint64) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return filepath.WalkDir(path, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		for index := range q.states {
			if os.SameFile(q.states[index].info, info) {
				q.states[index].state = localDockerProjectQuotaState{ProjectID: projectID, HardLimitBytes: hardLimitBytes, Inherits: info.IsDir() && q.inheritDirs}
				return nil
			}
		}
		q.states = append(q.states, fakeLocalDockerProjectQuotaEntry{
			info: info, state: localDockerProjectQuotaState{ProjectID: projectID, HardLimitBytes: hardLimitBytes, Inherits: info.IsDir() && q.inheritDirs},
		})
		return nil
	})
}

func (q *fakeLocalDockerProjectQuota) Read(path string) (localDockerProjectQuotaState, error) {
	info, err := os.Stat(path)
	if err != nil {
		return localDockerProjectQuotaState{}, ErrLocalDockerStorageQuotaReadbackMismatch
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, entry := range q.states {
		if os.SameFile(entry.info, info) {
			return entry.state, nil
		}
	}
	return localDockerProjectQuotaState{}, ErrLocalDockerStorageQuotaReadbackMismatch
}

type unavailableLocalDockerProjectQuota struct{}

func (unavailableLocalDockerProjectQuota) Preflight(string) error {
	return ErrLocalDockerStorageQuotaUnavailable
}
func (unavailableLocalDockerProjectQuota) Apply(string, uint32, uint64) error {
	return ErrLocalDockerStorageQuotaUnavailable
}
func (unavailableLocalDockerProjectQuota) Read(string) (localDockerProjectQuotaState, error) {
	return localDockerProjectQuotaState{}, ErrLocalDockerStorageQuotaUnavailable
}
func (unavailableLocalDockerProjectQuota) ReadProject(string, uint32) (localDockerProjectQuotaRecord, error) {
	return localDockerProjectQuotaRecord{}, ErrLocalDockerStorageQuotaUnavailable
}
func (unavailableLocalDockerProjectQuota) Clear(string, uint32) error {
	return ErrLocalDockerStorageQuotaUnavailable
}

func (q *fakeLocalDockerProjectQuota) Clear(_ string, projectID uint32) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.clearCalls++
	if q.clearErr != nil {
		return q.clearErr
	}
	retained := q.states[:0]
	for _, entry := range q.states {
		if entry.state.ProjectID == projectID {
			continue
		}
		retained = append(retained, entry)
	}
	q.states = retained
	q.cleared[projectID] = struct{}{}
	return nil
}

func (q *fakeLocalDockerProjectQuota) ReadProject(_ string, projectID uint32) (localDockerProjectQuotaRecord, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, entry := range q.states {
		if entry.state.ProjectID == projectID {
			return localDockerProjectQuotaRecord{
				HardLimitBytes: entry.state.HardLimitBytes,
				SoftLimitBytes: entry.state.HardLimitBytes,
				CurrentInodes:  1,
			}, nil
		}
	}
	return localDockerProjectQuotaRecord{}, nil
}

func (q *fakeLocalDockerProjectQuota) drift(path string, update func(*localDockerProjectQuotaState)) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for index := range q.states {
		if os.SameFile(q.states[index].info, info) {
			update(&q.states[index].state)
			return nil
		}
	}
	return ErrLocalDockerStorageQuotaReadbackMismatch
}

func localDockerStorageTestRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	localDockerStorageTestQuotas.Store(root, newFakeLocalDockerProjectQuota())
	return root
}

func localDockerStorageTestQuota(root string) localDockerProjectQuota {
	quota, ok := localDockerStorageTestQuotas.Load(root)
	if !ok {
		panic("missing local Docker storage test quota")
	}
	return quota.(localDockerProjectQuota)
}

func localDockerStorageTestConfig(root string) LocalDockerProviderConfig {
	return LocalDockerProviderConfig{HostStorageRoot: root, StorageQuotaBackend: localDockerStorageTestQuota(root)}
}

func TestLocalDockerStorageUsesWorkspaceScopedHostDirectoriesAndSurvivesProviderRestart(t *testing.T) {
	root := localDockerStorageTestRoot(t)
	input := StorageVolumeInput{
		ID: "storage-alpha", OperationID: "storage-operation", IdempotencyKey: "storage-idempotency",
		AccountID: "acct-alpha", WorkspaceID: "ws-alpha", SizeGB: 10,
	}
	provider := newLocalDockerProvider(localDockerStorageTestConfig(root), &recordingDockerRunner{})

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

	restarted := newLocalDockerProvider(localDockerStorageTestConfig(root), &recordingDockerRunner{})
	readback, err := restarted.ReadStorageVolume(context.Background(), volume)
	if err != nil || readback.Status != "ready" || readback.ProviderResourceID != volume.ProviderResourceID {
		t.Fatalf("readback=%#v err=%v", readback, err)
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer rootHandle.Close()
	metadata, err := readLocalDockerStorageMetadata(rootHandle, localDockerStoragePaths{WorkspaceName: localDockerName("opl-workspace", input.WorkspaceID)})
	if err != nil || metadata.ProjectID == 0 || metadata.SizeGB != input.SizeGB {
		t.Fatalf("quota metadata=%#v err=%v", metadata, err)
	}
	quota, err := localDockerStorageTestQuota(root).Read(workspaceRoot)
	if err != nil || quota.ProjectID != metadata.ProjectID || quota.HardLimitBytes != uint64(input.SizeGB)*1024*1024*1024 {
		t.Fatalf("quota readback=%#v err=%v", quota, err)
	}
}

func TestLocalDockerStorageCreateResumesDurableStagingAfterRestart(t *testing.T) {
	root := localDockerStorageTestRoot(t)
	input := StorageVolumeInput{
		ID: "storage-resume-staging", OperationID: "storage-operation", IdempotencyKey: "storage-idempotency",
		AccountID: "acct-alpha", WorkspaceID: "ws-resume-staging", SizeGB: 10,
	}
	workspaceName := localDockerName("opl-workspace", input.WorkspaceID)
	stagingName := ".storage-" + workspaceName
	for _, name := range []string{stagingName, stagingName + "/data", stagingName + "/projects"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	metadata := localDockerStorageMetadata{
		SchemaVersion: localDockerStorageMetadataSchemaVersion, StorageID: input.ID, AccountID: input.AccountID,
		WorkspaceID: input.WorkspaceID, ProjectID: localDockerInitialProjectID(input.ID), SizeGB: input.SizeGB,
	}
	if err := writeLocalDockerStorageMetadata(rootHandle, stagingName+"/"+localDockerStorageMetadataFile, metadata); err != nil {
		t.Fatal(err)
	}
	rootHandle.Close()
	provider := newLocalDockerProvider(localDockerStorageTestConfig(root), &recordingDockerRunner{})
	volume, err := provider.CreateStorageVolume(context.Background(), input)
	if err != nil || volume.Status != "ready" {
		t.Fatalf("resumed volume=%#v err=%v", volume, err)
	}
	if _, err := os.Stat(filepath.Join(root, workspaceName)); err != nil {
		t.Fatalf("resumed workspace missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, stagingName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging survived recovery: %v", err)
	}
}

func TestLocalDockerStorageFailsClosedOnProjectQuotaReadbackDrift(t *testing.T) {
	root := localDockerStorageTestRoot(t)
	quota := localDockerStorageTestQuota(root).(*fakeLocalDockerProjectQuota)
	provider := newLocalDockerProvider(localDockerStorageTestConfig(root), &recordingDockerRunner{})
	input := StorageVolumeInput{
		ID: "storage-quota-drift", OperationID: "storage-operation", IdempotencyKey: "storage-idempotency",
		AccountID: "acct-alpha", WorkspaceID: "ws-quota-drift", SizeGB: 10,
	}
	volume, err := provider.CreateStorageVolume(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	workspaceRoot := filepath.Join(root, localDockerName("opl-workspace", input.WorkspaceID))
	if _, err := quota.Read(workspaceRoot); err != nil {
		t.Fatal(err)
	}
	if err := quota.drift(workspaceRoot, func(state *localDockerProjectQuotaState) { state.HardLimitBytes++ }); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ReadStorageVolume(context.Background(), volume); !errors.Is(err, ErrLocalDockerStorageQuotaReadbackMismatch) {
		t.Fatalf("quota drift err=%v", err)
	}
	if err := quota.drift(workspaceRoot, func(state *localDockerProjectQuotaState) { state.HardLimitBytes-- }); err != nil {
		t.Fatal(err)
	}
	projects := filepath.Join(workspaceRoot, "projects")
	if err := quota.drift(projects, func(state *localDockerProjectQuotaState) { state.Inherits = false }); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ReadStorageVolume(context.Background(), volume); !errors.Is(err, ErrLocalDockerStorageQuotaReadbackMismatch) {
		t.Fatalf("project inheritance drift err=%v", err)
	}
}

func TestLocalDockerStorageCreateRequiresImmediateProjectInheritanceReadback(t *testing.T) {
	root := localDockerStorageTestRoot(t)
	quota := localDockerStorageTestQuota(root).(*fakeLocalDockerProjectQuota)
	quota.inheritDirs = false
	provider := newLocalDockerProvider(localDockerStorageTestConfig(root), &recordingDockerRunner{})
	_, err := provider.CreateStorageVolume(context.Background(), StorageVolumeInput{
		ID: "storage-no-inherit", OperationID: "storage-operation", IdempotencyKey: "storage-idempotency",
		AccountID: "acct-alpha", WorkspaceID: "ws-no-inherit", SizeGB: 10,
	})
	if !errors.Is(err, ErrLocalDockerStorageQuotaReadbackMismatch) {
		t.Fatalf("create without project inheritance err=%v", err)
	}
}

func TestLocalDockerReadinessRejectsLegacyStorageBeforeLaunch(t *testing.T) {
	root := localDockerStorageTestRoot(t)
	workspaceName := localDockerName("opl-workspace", "ws-legacy")
	for _, path := range []string{workspaceName, workspaceName + "/data", workspaceName + "/projects"} {
		if err := os.Mkdir(filepath.Join(root, path), 0700); err != nil {
			t.Fatal(err)
		}
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeLocalDockerStorageMetadata(rootHandle, workspaceName+"/"+localDockerStorageMetadataFile, localDockerStorageMetadata{
		SchemaVersion: 1, StorageID: "storage-legacy", AccountID: "acct-alpha", WorkspaceID: "ws-legacy",
	}); err != nil {
		t.Fatal(err)
	}
	rootHandle.Close()
	provider := newLocalDockerProvider(LocalDockerProviderConfig{
		GatewaySecretRoot: localDockerSecretTestRoot(t), HostStorageRoot: root, StorageQuotaBackend: localDockerStorageTestQuota(root),
	}, &recordingDockerRunner{})
	if _, err := provider.Readiness(context.Background()); err == nil || err.Error() != "local_docker_storage_schema_incompatible" {
		t.Fatalf("legacy storage readiness err=%v", err)
	}
}

func TestLocalDockerStoragePreflightRejectsUnavailableProjectQuota(t *testing.T) {
	root := localDockerStorageTestRoot(t)
	provider := newLocalDockerProvider(LocalDockerProviderConfig{
		GatewaySecretRoot: localDockerSecretTestRoot(t), HostStorageRoot: root, StorageQuotaBackend: unavailableLocalDockerProjectQuota{},
	}, &recordingDockerRunner{})
	_, err := provider.MonthlyPreflight(context.Background(), MonthlyPreflightInput{
		ResourceType: "storage", PackageID: "basic", SizeGB: 10, Zone: "local",
	})
	if !errors.Is(err, ErrLocalDockerStorageQuotaUnavailable) {
		t.Fatalf("storage preflight err=%v", err)
	}
}

func TestLocalDockerStorageDestroyRequiresExactIdentityAndIsIdempotent(t *testing.T) {
	root := localDockerStorageTestRoot(t)
	provider := newLocalDockerProvider(localDockerStorageTestConfig(root), &recordingDockerRunner{})
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

func TestLocalDockerStorageDestroyConcurrentAndCrashRecoverable(t *testing.T) {
	root := localDockerStorageTestRoot(t)
	quota := localDockerStorageTestQuota(root).(*fakeLocalDockerProjectQuota)
	provider := newLocalDockerProvider(localDockerStorageTestConfig(root), &recordingDockerRunner{})
	input := StorageVolumeInput{
		ID: "storage-delete-concurrent", OperationID: "storage-operation", IdempotencyKey: "storage-idempotency",
		AccountID: "acct-alpha", WorkspaceID: "ws-delete-concurrent", SizeGB: 10,
	}
	volume, err := provider.CreateStorageVolume(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	quota.clearErr = errors.New("injected clear failure")
	if _, err := provider.DestroyStorageVolume(context.Background(), volume); err == nil {
		t.Fatal("destroy unexpectedly ignored quota clear failure")
	}
	workspaceName := localDockerName("opl-workspace", input.WorkspaceID)
	if _, err := os.Stat(filepath.Join(root, localDockerStorageDeletionDirectory, workspaceName+".json")); err != nil {
		t.Fatalf("deletion tombstone missing after clear failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, workspaceName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace survived failed clear: %v", err)
	}
	quota.clearErr = nil
	provider = newLocalDockerProvider(localDockerStorageTestConfig(root), &recordingDockerRunner{})
	const attempts = 8
	results := make(chan error, attempts)
	var group sync.WaitGroup
	for index := 0; index < attempts; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, destroyErr := provider.DestroyStorageVolume(context.Background(), volume)
			results <- destroyErr
		}()
	}
	group.Wait()
	close(results)
	for destroyErr := range results {
		if destroyErr != nil {
			t.Fatalf("concurrent destroy err=%v", destroyErr)
		}
	}
	quota.mu.Lock()
	clearCalls := quota.clearCalls
	quota.mu.Unlock()
	if clearCalls != 2 {
		t.Fatalf("unexpected clear call count=%d", clearCalls)
	}
	if _, err := os.Stat(filepath.Join(root, localDockerStorageDeletionDirectory, workspaceName+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deletion tombstone survived recovery: %v", err)
	}
}

func TestLocalDockerStorageFailsClosedOnWorkspaceSymlink(t *testing.T) {
	root := localDockerStorageTestRoot(t)
	outside := t.TempDir()
	workspaceID := "ws-symlink"
	if err := os.Symlink(outside, filepath.Join(root, localDockerName("opl-workspace", workspaceID))); err != nil {
		t.Fatal(err)
	}
	provider := newLocalDockerProvider(localDockerStorageTestConfig(root), &recordingDockerRunner{})
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
	provider := newLocalDockerProvider(localDockerStorageTestConfig(root), &recordingDockerRunner{})
	if _, err := provider.MonthlyPreflight(context.Background(), MonthlyPreflightInput{
		ResourceType: "storage", PackageID: "basic", SizeGB: math.MaxInt / (1024 * 1024 * 1024), Zone: "local",
	}); err == nil || err.Error() != "local_docker_storage_capacity_insufficient" {
		t.Fatalf("oversized storage preflight err=%v", err)
	}
	if _, err := provider.MonthlyPreflight(context.Background(), MonthlyPreflightInput{
		ResourceType: "storage", PackageID: "basic", SizeGB: math.MaxInt, Zone: "local",
	}); err == nil || err.Error() != "local_docker_storage_size_invalid" {
		t.Fatalf("overflowing storage preflight err=%v", err)
	}
}
