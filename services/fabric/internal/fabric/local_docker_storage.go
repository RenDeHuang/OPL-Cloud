package fabric

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const localDockerStorageMetadataFile = ".opl-storage.json"
const localDockerStorageQuotaLockFile = ".opl-project-quota.lock"
const localDockerStorageDeletionDirectory = ".opl-storage-deletions"
const localDockerStorageMetadataSchemaVersion = 2
const localDockerStorageDeletionSchemaVersion = 1
const localDockerWriteAccessMode = 2

type localDockerStorageMetadata struct {
	SchemaVersion int    `json:"schemaVersion"`
	StorageID     string `json:"storageId"`
	AccountID     string `json:"accountId"`
	WorkspaceID   string `json:"workspaceId"`
	ProjectID     uint32 `json:"projectId"`
	SizeGB        int    `json:"sizeGb"`
}

type localDockerStorageDeletion struct {
	SchemaVersion int    `json:"schemaVersion"`
	StorageID     string `json:"storageId"`
	AccountID     string `json:"accountId"`
	WorkspaceID   string `json:"workspaceId"`
	WorkspaceName string `json:"workspaceName"`
	ProjectID     uint32 `json:"projectId"`
	SizeGB        int    `json:"sizeGb"`
}

type localDockerStoragePaths struct {
	WorkspaceName string
	Workspace     string
	Data          string
	Projects      string
}

func validateLocalDockerStorageRoot(path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\r\n,") {
		return fmt.Errorf("local_docker_storage_root_invalid")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("local_docker_storage_root_invalid")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return fmt.Errorf("local_docker_storage_root_invalid")
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("local_docker_storage_root_invalid")
	}
	defer directory.Close()
	openedInfo, err := directory.Stat()
	if err != nil || !openedInfo.IsDir() {
		return fmt.Errorf("local_docker_storage_root_invalid")
	}
	return nil
}

func (p *LocalDockerProvider) openStorageRoot() (*os.Root, error) {
	if p.hostStorageRootErr != nil {
		return nil, p.hostStorageRootErr
	}
	if err := validateLocalDockerStorageRoot(p.hostStorageRoot); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(p.hostStorageRoot)
	if err != nil {
		return nil, fmt.Errorf("local_docker_storage_root_unavailable")
	}
	return root, nil
}

func (p *LocalDockerProvider) preflightStorageCapacity(sizeGB int) error {
	requiredBytes, err := localDockerStorageLimitBytes(sizeGB)
	if err != nil {
		return err
	}
	if err := syscall.Access(p.hostStorageRoot, localDockerWriteAccessMode); err != nil {
		return fmt.Errorf("local_docker_storage_root_not_writable")
	}
	var stats syscall.Statfs_t
	if err := syscall.Statfs(p.hostStorageRoot, &stats); err != nil || stats.Bsize <= 0 {
		return fmt.Errorf("local_docker_storage_capacity_unavailable")
	}
	blockSize := uint64(stats.Bsize)
	requiredBlocks := requiredBytes / blockSize
	if requiredBytes%blockSize != 0 {
		requiredBlocks++
	}
	if uint64(stats.Bavail) < requiredBlocks {
		return fmt.Errorf("local_docker_storage_capacity_insufficient")
	}
	return nil
}

func (p *LocalDockerProvider) prepareStorageRoot() error {
	return p.withStorageQuotaLock(func() error {
		root, err := p.openStorageRoot()
		if err != nil {
			return err
		}
		defer root.Close()
		if err := p.resumePendingStorageDeletionsLocked(root); err != nil {
			return err
		}
		if err := p.resumePendingStorageCreationsLocked(root); err != nil {
			return err
		}
		return p.validateStorageMetadataInventoryLocked(root)
	})
}

func (p *LocalDockerProvider) validateStorageMetadataInventoryLocked(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("local_docker_storage_inventory_unavailable")
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return fmt.Errorf("local_docker_storage_inventory_unavailable")
	}
	owners := make(map[uint32]string)
	for _, entry := range entries {
		name := entry.Name()
		switch name {
		case localDockerStorageQuotaLockFile:
			if info, infoErr := entry.Info(); infoErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
				return fmt.Errorf("local_docker_storage_inventory_conflict")
			}
			continue
		case localDockerStorageDeletionDirectory:
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("local_docker_storage_inventory_conflict")
			}
			continue
		case "lost+found":
			lostFound, openErr := root.Open(name)
			if openErr != nil {
				return fmt.Errorf("local_docker_storage_inventory_conflict")
			}
			children, readErr := lostFound.ReadDir(1)
			closeErr := lostFound.Close()
			if readErr != nil && !errors.Is(readErr, io.EOF) || len(children) != 0 || closeErr != nil {
				return fmt.Errorf("local_docker_storage_inventory_conflict")
			}
			continue
		}
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasPrefix(name, "opl-workspace-") && !strings.HasPrefix(name, ".storage-opl-workspace-") {
			return fmt.Errorf("local_docker_storage_inventory_conflict")
		}
		metadata, readErr := readLocalDockerStorageMetadata(root, localDockerStoragePaths{WorkspaceName: name})
		if readErr != nil || metadata.SchemaVersion != localDockerStorageMetadataSchemaVersion || metadata.ProjectID == 0 || metadata.SizeGB <= 0 {
			return fmt.Errorf("local_docker_storage_schema_incompatible")
		}
		expectedWorkspaceName, nameErr := localDockerWorkspaceStorageName(metadata.WorkspaceID)
		if nameErr != nil || name != expectedWorkspaceName && name != ".storage-"+expectedWorkspaceName {
			return fmt.Errorf("local_docker_storage_inventory_conflict")
		}
		if owner, exists := owners[metadata.ProjectID]; exists && owner != metadata.StorageID {
			return fmt.Errorf("local_docker_storage_project_id_conflict")
		}
		owners[metadata.ProjectID] = metadata.StorageID
		limitBytes, limitErr := localDockerStorageLimitBytes(metadata.SizeGB)
		if limitErr != nil {
			return limitErr
		}
		base := filepath.Join(p.hostStorageRoot, name)
		for _, path := range []string{base, filepath.Join(base, "data"), filepath.Join(base, "projects")} {
			quota, quotaErr := p.storageQuota.Read(path)
			if quotaErr != nil || quota.ProjectID != metadata.ProjectID || quota.HardLimitBytes != limitBytes || !quota.Inherits {
				return ErrLocalDockerStorageQuotaReadbackMismatch
			}
		}
	}
	return nil
}

func localDockerStorageLimitBytes(sizeGB int) (uint64, error) {
	if sizeGB <= 0 || uint64(sizeGB) > ^uint64(0)/(1024*1024*1024) {
		return 0, fmt.Errorf("local_docker_storage_size_invalid")
	}
	return uint64(sizeGB) * 1024 * 1024 * 1024, nil
}

func localDockerInitialProjectID(storageID string) uint32 {
	digest := sha256.Sum256([]byte("opl-local-docker-project-quota\x00" + storageID))
	return binary.BigEndian.Uint32(digest[:4]) | 1
}

func localDockerNextProjectID(projectID uint32) uint32 {
	projectID++
	if projectID == 0 {
		return 1
	}
	return projectID
}

func (p *LocalDockerProvider) withStorageQuotaLock(operation func() error) error {
	root, err := p.openStorageRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	lock, err := root.OpenFile(localDockerStorageQuotaLockFile, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return fmt.Errorf("local_docker_storage_quota_lock_unavailable")
	}
	defer lock.Close()
	info, err := lock.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return fmt.Errorf("local_docker_storage_quota_lock_invalid")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("local_docker_storage_quota_lock_unavailable")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return operation()
}

func (p *LocalDockerProvider) allocateStorageProjectID(root *os.Root, metadata localDockerStorageMetadata) (uint32, error) {
	used := make(map[uint32]string)
	directory, err := root.Open(".")
	if err != nil {
		return 0, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return 0, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "opl-workspace-") && !strings.HasPrefix(entry.Name(), ".storage-opl-workspace-") {
			continue
		}
		paths := localDockerStoragePaths{WorkspaceName: entry.Name()}
		existing, readErr := readLocalDockerStorageOwnerMetadata(root, paths)
		if readErr != nil || existing.ProjectID == 0 {
			return 0, ErrLaunchStageBindingConflict
		}
		if existing.StorageID == metadata.StorageID {
			return 0, ErrLaunchStageBindingConflict
		}
		if owner, exists := used[existing.ProjectID]; exists && owner != existing.StorageID {
			return 0, fmt.Errorf("local_docker_storage_project_id_conflict")
		}
		used[existing.ProjectID] = existing.StorageID
	}
	projectID := localDockerInitialProjectID(metadata.StorageID)
	for attempts := uint64(0); attempts < uint64(^uint32(0)); attempts++ {
		if _, exists := used[projectID]; !exists {
			record, readErr := p.storageQuota.ReadProject(p.hostStorageRoot, projectID)
			if readErr != nil {
				return 0, readErr
			}
			if record.HardLimitBytes == 0 && record.SoftLimitBytes == 0 && record.CurrentBytes == 0 && record.CurrentInodes == 0 {
				return projectID, nil
			}
		}
		projectID = localDockerNextProjectID(projectID)
	}
	return 0, fmt.Errorf("local_docker_storage_project_id_exhausted")
}

func localDockerWorkspaceStorageName(workspaceID string) (string, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" || strings.ContainsAny(workspaceID, "\r\n") {
		return "", fmt.Errorf("local_docker_storage_identity_invalid")
	}
	return localDockerName("opl-workspace", workspaceID), nil
}

func (p *LocalDockerProvider) storagePaths(workspaceID string) (localDockerStoragePaths, error) {
	if p.hostStorageRootErr != nil {
		return localDockerStoragePaths{}, p.hostStorageRootErr
	}
	name, err := localDockerWorkspaceStorageName(workspaceID)
	if err != nil {
		return localDockerStoragePaths{}, err
	}
	workspace := filepath.Join(p.hostStorageRoot, name)
	return localDockerStoragePaths{
		WorkspaceName: name,
		Workspace:     workspace,
		Data:          filepath.Join(workspace, "data"),
		Projects:      filepath.Join(workspace, "projects"),
	}, nil
}

func ensureLocalDockerStorageDirectory(root *os.Root, name string, mode os.FileMode) error {
	if err := root.Mkdir(name, mode); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := root.Lstat(name)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrLaunchStageBindingConflict
	}
	return nil
}

func writeLocalDockerStorageMetadata(root *os.Root, name string, metadata localDockerStorageMetadata) error {
	body, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
	if err != nil {
		return err
	}
	if err = file.Chmod(0400); err == nil {
		_, err = file.Write(body)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func syncLocalDockerStorageDirectory(root *os.Root, name string) error {
	directory, err := root.Open(name)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func localDockerStorageDeletionName(workspaceName string) string {
	return localDockerStorageDeletionDirectory + "/" + workspaceName + ".json"
}

func ensureLocalDockerStorageDeletionDirectory(root *os.Root) error {
	if err := ensureLocalDockerStorageDirectory(root, localDockerStorageDeletionDirectory, 0700); err != nil {
		return err
	}
	info, err := root.Lstat(localDockerStorageDeletionDirectory)
	if err != nil || info.Mode().Perm() != 0700 {
		return fmt.Errorf("local_docker_storage_deletion_journal_invalid")
	}
	return syncLocalDockerStorageDirectory(root, ".")
}

func validLocalDockerStorageDeletion(deletion localDockerStorageDeletion) bool {
	workspaceName, err := localDockerWorkspaceStorageName(deletion.WorkspaceID)
	return err == nil && deletion.SchemaVersion == localDockerStorageDeletionSchemaVersion && deletion.StorageID != "" && deletion.AccountID != "" &&
		deletion.WorkspaceName == workspaceName && deletion.ProjectID != 0 && deletion.SizeGB > 0
}

func readLocalDockerStorageDeletion(root *os.Root, name string) (localDockerStorageDeletion, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return localDockerStorageDeletion{}, ErrWorkspaceLaunchResourceAbsent
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0400 {
		return localDockerStorageDeletion{}, fmt.Errorf("local_docker_storage_deletion_journal_invalid")
	}
	body, err := root.ReadFile(name)
	if err != nil {
		return localDockerStorageDeletion{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var deletion localDockerStorageDeletion
	if decoder.Decode(&deletion) != nil || decoder.Decode(&struct{}{}) != io.EOF || !validLocalDockerStorageDeletion(deletion) {
		return localDockerStorageDeletion{}, fmt.Errorf("local_docker_storage_deletion_journal_invalid")
	}
	return deletion, nil
}

func writeLocalDockerStorageDeletion(root *os.Root, deletion localDockerStorageDeletion) error {
	if !validLocalDockerStorageDeletion(deletion) {
		return fmt.Errorf("local_docker_storage_deletion_journal_invalid")
	}
	if err := ensureLocalDockerStorageDeletionDirectory(root); err != nil {
		return err
	}
	target := localDockerStorageDeletionName(deletion.WorkspaceName)
	if existing, err := readLocalDockerStorageDeletion(root, target); err == nil {
		if existing != deletion {
			return ErrLaunchStageBindingConflict
		}
		return nil
	} else if !errors.Is(err, ErrWorkspaceLaunchResourceAbsent) {
		return err
	}
	temporarySuffix, err := localDockerSecretStagingName()
	if err != nil {
		return err
	}
	temporary := localDockerStorageDeletionDirectory + "/.delete-" + strings.TrimPrefix(temporarySuffix, ".")
	defer root.Remove(temporary)
	if err := writeLocalDockerStorageMetadataJSON(root, temporary, deletion); err != nil {
		return err
	}
	if err := root.Rename(temporary, target); err != nil {
		return err
	}
	return syncLocalDockerStorageDirectory(root, localDockerStorageDeletionDirectory)
}

func writeLocalDockerStorageMetadataJSON(root *os.Root, name string, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
	if err != nil {
		return err
	}
	if err = file.Chmod(0400); err == nil {
		_, err = file.Write(body)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func localDockerStorageDeletionFromMetadata(metadata localDockerStorageMetadata, workspaceName string) localDockerStorageDeletion {
	return localDockerStorageDeletion{
		SchemaVersion: localDockerStorageDeletionSchemaVersion, StorageID: metadata.StorageID, AccountID: metadata.AccountID,
		WorkspaceID: metadata.WorkspaceID, WorkspaceName: workspaceName, ProjectID: metadata.ProjectID, SizeGB: metadata.SizeGB,
	}
}

func (p *LocalDockerProvider) resumeStorageDeletionLocked(root *os.Root, deletion localDockerStorageDeletion) error {
	paths := localDockerStoragePaths{WorkspaceName: deletion.WorkspaceName}
	metadata, err := readLocalDockerStorageOwnerMetadata(root, paths)
	if err == nil {
		if localDockerStorageDeletionFromMetadata(metadata, deletion.WorkspaceName) != deletion {
			return ErrLaunchStageBindingConflict
		}
		limitBytes, limitErr := localDockerStorageLimitBytes(deletion.SizeGB)
		if limitErr != nil {
			return limitErr
		}
		quota, quotaErr := p.storageQuota.Read(filepath.Join(p.hostStorageRoot, deletion.WorkspaceName))
		if quotaErr != nil || quota.ProjectID != deletion.ProjectID || quota.HardLimitBytes != limitBytes || !quota.Inherits {
			return fmt.Errorf("local_docker_storage_destroy_quota_mismatch")
		}
		if err := root.RemoveAll(deletion.WorkspaceName); err != nil {
			return err
		}
		if err := syncLocalDockerStorageDirectory(root, "."); err != nil {
			return err
		}
	} else if !errors.Is(err, ErrWorkspaceLaunchResourceAbsent) {
		return err
	}
	if err := p.storageQuota.Clear(p.hostStorageRoot, deletion.ProjectID); err != nil {
		return err
	}
	if err := root.Remove(localDockerStorageDeletionName(deletion.WorkspaceName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncLocalDockerStorageDirectory(root, localDockerStorageDeletionDirectory)
}

func (p *LocalDockerProvider) resumePendingStorageDeletionsLocked(root *os.Root) error {
	info, err := root.Lstat(localDockerStorageDeletionDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return fmt.Errorf("local_docker_storage_deletion_journal_invalid")
	}
	directory, err := root.Open(localDockerStorageDeletionDirectory)
	if err != nil {
		return err
	}
	entries, err := directory.ReadDir(-1)
	directory.Close()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".delete-") && !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			if err := root.Remove(localDockerStorageDeletionDirectory + "/" + entry.Name()); err != nil {
				return err
			}
			continue
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(entry.Name(), ".json") || strings.HasPrefix(entry.Name(), ".") {
			return fmt.Errorf("local_docker_storage_deletion_journal_invalid")
		}
		deletion, readErr := readLocalDockerStorageDeletion(root, localDockerStorageDeletionDirectory+"/"+entry.Name())
		if readErr != nil || entry.Name() != deletion.WorkspaceName+".json" {
			return firstNonNil(readErr, fmt.Errorf("local_docker_storage_deletion_journal_invalid"))
		}
		if err := p.resumeStorageDeletionLocked(root, deletion); err != nil {
			return err
		}
	}
	return syncLocalDockerStorageDirectory(root, localDockerStorageDeletionDirectory)
}

func (p *LocalDockerProvider) resumePendingStorageCreationsLocked(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, err := directory.ReadDir(-1)
	directory.Close()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".storage-") {
			continue
		}
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("local_docker_storage_inventory_conflict")
		}
		metadata, readErr := readLocalDockerStorageMetadata(root, localDockerStoragePaths{WorkspaceName: entry.Name()})
		if errors.Is(readErr, ErrLaunchStageBindingConflict) && localDockerStorageStagingIsEmpty(root, entry.Name()) {
			if err := root.RemoveAll(entry.Name()); err != nil {
				return err
			}
			if err := syncLocalDockerStorageDirectory(root, "."); err != nil {
				return err
			}
			continue
		}
		if readErr != nil || metadata.SchemaVersion != localDockerStorageMetadataSchemaVersion || metadata.ProjectID == 0 || metadata.SizeGB <= 0 {
			return fmt.Errorf("local_docker_storage_schema_incompatible")
		}
		workspaceName, nameErr := localDockerWorkspaceStorageName(metadata.WorkspaceID)
		if nameErr != nil || entry.Name() != ".storage-"+workspaceName {
			return fmt.Errorf("local_docker_storage_inventory_conflict")
		}
		if existing, existingErr := readLocalDockerStorageMetadata(root, localDockerStoragePaths{WorkspaceName: workspaceName}); existingErr == nil {
			if existing != metadata {
				return ErrLaunchStageBindingConflict
			}
			if err := root.RemoveAll(entry.Name()); err != nil {
				return err
			}
			if err := syncLocalDockerStorageDirectory(root, "."); err != nil {
				return err
			}
			continue
		} else if !errors.Is(existingErr, ErrWorkspaceLaunchResourceAbsent) {
			return existingErr
		}
		limitBytes, limitErr := localDockerStorageLimitBytes(metadata.SizeGB)
		if limitErr != nil {
			return limitErr
		}
		stagingPath := filepath.Join(p.hostStorageRoot, entry.Name())
		if err := p.storageQuota.Apply(stagingPath, metadata.ProjectID, limitBytes); err != nil {
			return err
		}
		if err := p.verifyStorageQuotaPaths(localDockerStoragePaths{Workspace: stagingPath, Data: filepath.Join(stagingPath, "data"), Projects: filepath.Join(stagingPath, "projects")}, metadata.ProjectID, limitBytes); err != nil {
			return err
		}
		if err := root.Rename(entry.Name(), workspaceName); err != nil {
			return err
		}
		if err := syncLocalDockerStorageDirectory(root, "."); err != nil {
			return err
		}
	}
	return nil
}

func localDockerStorageStagingIsEmpty(root *os.Root, name string) bool {
	directory, err := root.Open(name)
	if err != nil {
		return false
	}
	entries, err := directory.ReadDir(-1)
	directory.Close()
	if err != nil || len(entries) != 2 {
		return false
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() || entry.Name() != "data" && entry.Name() != "projects" {
			return false
		}
		child, childErr := root.Open(name + "/" + entry.Name())
		if childErr != nil {
			return false
		}
		children, readErr := child.ReadDir(1)
		closeErr := child.Close()
		if (readErr != nil && !errors.Is(readErr, io.EOF)) || len(children) != 0 || closeErr != nil {
			return false
		}
	}
	return true
}

func readLocalDockerStorageOwnerMetadata(root *os.Root, paths localDockerStoragePaths) (localDockerStorageMetadata, error) {
	info, err := root.Lstat(paths.WorkspaceName)
	if errors.Is(err, os.ErrNotExist) {
		return localDockerStorageMetadata{}, ErrWorkspaceLaunchResourceAbsent
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return localDockerStorageMetadata{}, ErrLaunchStageBindingConflict
	}
	metadataName := paths.WorkspaceName + "/" + localDockerStorageMetadataFile
	info, err = root.Lstat(metadataName)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0400 {
		return localDockerStorageMetadata{}, ErrLaunchStageBindingConflict
	}
	body, err := root.ReadFile(metadataName)
	if err != nil {
		return localDockerStorageMetadata{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var metadata localDockerStorageMetadata
	if decoder.Decode(&metadata) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return localDockerStorageMetadata{}, ErrLaunchStageBindingConflict
	}
	return metadata, nil
}

func readLocalDockerStorageMetadata(root *os.Root, paths localDockerStoragePaths) (localDockerStorageMetadata, error) {
	metadata, err := readLocalDockerStorageOwnerMetadata(root, paths)
	if err != nil {
		return localDockerStorageMetadata{}, err
	}
	for _, name := range []string{paths.WorkspaceName + "/data", paths.WorkspaceName + "/projects"} {
		info, err := root.Lstat(name)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return localDockerStorageMetadata{}, ErrLaunchStageBindingConflict
		}
	}
	return metadata, nil
}

func (p *LocalDockerProvider) ensureStorageDirectories(metadata localDockerStorageMetadata, sizeGB int) (localDockerStoragePaths, error) {
	if metadata.SchemaVersion != localDockerStorageMetadataSchemaVersion || metadata.StorageID == "" || metadata.AccountID == "" || metadata.WorkspaceID == "" || metadata.SizeGB <= 0 || metadata.SizeGB != sizeGB {
		return localDockerStoragePaths{}, fmt.Errorf("local_docker_storage_identity_invalid")
	}
	if err := p.storageQuota.Preflight(p.hostStorageRoot); err != nil {
		return localDockerStoragePaths{}, err
	}
	paths, err := p.storagePaths(metadata.WorkspaceID)
	if err != nil {
		return localDockerStoragePaths{}, err
	}
	root, err := p.openStorageRoot()
	if err != nil {
		return localDockerStoragePaths{}, err
	}
	defer root.Close()
	limitBytes, err := localDockerStorageLimitBytes(sizeGB)
	if err != nil {
		return localDockerStoragePaths{}, err
	}
	err = p.withStorageQuotaLock(func() error {
		if err := p.resumePendingStorageDeletionsLocked(root); err != nil {
			return err
		}
		if err := p.resumePendingStorageCreationsLocked(root); err != nil {
			return err
		}
		if err := p.validateStorageMetadataInventoryLocked(root); err != nil {
			return err
		}
		if existing, readErr := readLocalDockerStorageMetadata(root, paths); readErr == nil {
			if existing.StorageID != metadata.StorageID || existing.AccountID != metadata.AccountID || existing.WorkspaceID != metadata.WorkspaceID || existing.ProjectID == 0 || existing.SizeGB != metadata.SizeGB {
				return ErrLaunchStageBindingConflict
			}
			metadata.ProjectID = existing.ProjectID
			if err := p.storageQuota.Apply(paths.Workspace, metadata.ProjectID, limitBytes); err != nil {
				return err
			}
			return p.verifyStorageQuotaPaths(paths, metadata.ProjectID, limitBytes)
		} else if !errors.Is(readErr, ErrWorkspaceLaunchResourceAbsent) {
			return readErr
		}

		staging := ".storage-" + paths.WorkspaceName
		if existing, readErr := readLocalDockerStorageMetadata(root, localDockerStoragePaths{WorkspaceName: staging}); readErr == nil {
			if existing.StorageID != metadata.StorageID || existing.AccountID != metadata.AccountID || existing.WorkspaceID != metadata.WorkspaceID || existing.ProjectID == 0 || existing.SizeGB != metadata.SizeGB {
				return ErrLaunchStageBindingConflict
			}
			metadata.ProjectID = existing.ProjectID
			stagingPath := filepath.Join(p.hostStorageRoot, staging)
			if err := p.storageQuota.Apply(stagingPath, metadata.ProjectID, limitBytes); err != nil {
				return err
			}
			if err := p.verifyStorageQuotaPaths(localDockerStoragePaths{Workspace: stagingPath, Data: filepath.Join(stagingPath, "data"), Projects: filepath.Join(stagingPath, "projects")}, metadata.ProjectID, limitBytes); err != nil {
				return err
			}
			if err := root.Rename(staging, paths.WorkspaceName); err != nil {
				return err
			}
			return syncLocalDockerStorageDirectory(root, ".")
		} else if !errors.Is(readErr, ErrWorkspaceLaunchResourceAbsent) {
			return readErr
		}
		metadata.ProjectID, err = p.allocateStorageProjectID(root, metadata)
		if err != nil {
			return err
		}
		if err := ensureLocalDockerStorageDirectory(root, staging, 0700); err != nil {
			return err
		}
		for _, child := range []string{"data", "projects"} {
			if err := ensureLocalDockerStorageDirectory(root, staging+"/"+child, 0700); err != nil {
				return err
			}
		}
		if err := writeLocalDockerStorageMetadata(root, staging+"/"+localDockerStorageMetadataFile, metadata); err != nil {
			return err
		}
		if err := syncLocalDockerStorageDirectory(root, staging); err != nil {
			return err
		}
		if err := syncLocalDockerStorageDirectory(root, "."); err != nil {
			return err
		}
		stagingPath := filepath.Join(p.hostStorageRoot, staging)
		if err := p.storageQuota.Apply(stagingPath, metadata.ProjectID, limitBytes); err != nil {
			return err
		}
		if err := p.verifyStorageQuotaPaths(localDockerStoragePaths{Workspace: stagingPath, Data: filepath.Join(stagingPath, "data"), Projects: filepath.Join(stagingPath, "projects")}, metadata.ProjectID, limitBytes); err != nil {
			return err
		}
		if err := root.Rename(staging, paths.WorkspaceName); err != nil {
			existing, readErr := readLocalDockerStorageMetadata(root, paths)
			if readErr != nil || existing != metadata {
				return ErrLaunchStageBindingConflict
			}
		}
		return syncLocalDockerStorageDirectory(root, ".")
	})
	if err != nil {
		return localDockerStoragePaths{}, err
	}
	return paths, nil
}

func (p *LocalDockerProvider) readStorageDirectories(volume StorageVolume) (localDockerStoragePaths, error) {
	if volume.ID == "" || volume.AccountID == "" || volume.WorkspaceID == "" {
		return localDockerStoragePaths{}, fmt.Errorf("local_docker_storage_identity_invalid")
	}
	if err := p.storageQuota.Preflight(p.hostStorageRoot); err != nil {
		return localDockerStoragePaths{}, err
	}
	paths, err := p.storagePaths(volume.WorkspaceID)
	if err != nil {
		return localDockerStoragePaths{}, err
	}
	root, err := p.openStorageRoot()
	if err != nil {
		return localDockerStoragePaths{}, err
	}
	defer root.Close()
	metadata, err := readLocalDockerStorageMetadata(root, paths)
	if err != nil {
		return localDockerStoragePaths{}, err
	}
	if metadata.SchemaVersion != localDockerStorageMetadataSchemaVersion || metadata.ProjectID == 0 || metadata.StorageID != volume.ID || metadata.AccountID != volume.AccountID || metadata.WorkspaceID != volume.WorkspaceID || metadata.SizeGB != volume.SizeGB || volume.ProviderResourceID != "" && volume.ProviderResourceID != "directory/"+paths.WorkspaceName {
		return localDockerStoragePaths{}, ErrLaunchStageBindingConflict
	}
	limitBytes, err := localDockerStorageLimitBytes(volume.SizeGB)
	if err != nil {
		return localDockerStoragePaths{}, err
	}
	if err := p.verifyStorageQuotaPaths(paths, metadata.ProjectID, limitBytes); err != nil {
		return localDockerStoragePaths{}, err
	}
	return paths, nil
}

func (p *LocalDockerProvider) verifyStorageQuotaPaths(paths localDockerStoragePaths, projectID uint32, hardLimitBytes uint64) error {
	for _, path := range []string{paths.Workspace, paths.Data, paths.Projects} {
		quota, readErr := p.storageQuota.Read(path)
		if readErr != nil || quota.ProjectID != projectID || quota.HardLimitBytes != hardLimitBytes || !quota.Inherits {
			return fmt.Errorf("%w: path=%s project=%d/%d hard=%d/%d inherits=%t", ErrLocalDockerStorageQuotaReadbackMismatch, filepath.Base(path), quota.ProjectID, projectID, quota.HardLimitBytes, hardLimitBytes, quota.Inherits)
		}
	}
	return nil
}
