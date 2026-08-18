package fabric

import (
	"bytes"
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
const localDockerWriteAccessMode = 2

type localDockerStorageMetadata struct {
	SchemaVersion int    `json:"schemaVersion"`
	StorageID     string `json:"storageId"`
	AccountID     string `json:"accountId"`
	WorkspaceID   string `json:"workspaceId"`
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
	if sizeGB <= 0 {
		return fmt.Errorf("local_docker_storage_size_invalid")
	}
	if err := syscall.Access(p.hostStorageRoot, localDockerWriteAccessMode); err != nil {
		return fmt.Errorf("local_docker_storage_root_not_writable")
	}
	var stats syscall.Statfs_t
	if err := syscall.Statfs(p.hostStorageRoot, &stats); err != nil || stats.Bsize <= 0 {
		return fmt.Errorf("local_docker_storage_capacity_unavailable")
	}
	requiredBytes := uint64(sizeGB) * 1024 * 1024 * 1024
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

func (p *LocalDockerProvider) ensureStorageDirectories(metadata localDockerStorageMetadata) (localDockerStoragePaths, error) {
	if metadata.SchemaVersion != 1 || metadata.StorageID == "" || metadata.AccountID == "" || metadata.WorkspaceID == "" {
		return localDockerStoragePaths{}, fmt.Errorf("local_docker_storage_identity_invalid")
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
	if existing, readErr := readLocalDockerStorageMetadata(root, paths); readErr == nil {
		if existing != metadata {
			return localDockerStoragePaths{}, ErrLaunchStageBindingConflict
		}
		return paths, nil
	} else if !errors.Is(readErr, ErrWorkspaceLaunchResourceAbsent) {
		return localDockerStoragePaths{}, readErr
	}

	stagingSuffix, err := localDockerSecretStagingName()
	if err != nil {
		return localDockerStoragePaths{}, err
	}
	staging := ".storage-" + strings.TrimPrefix(stagingSuffix, ".")
	defer root.RemoveAll(staging)
	if err := ensureLocalDockerStorageDirectory(root, staging, 0700); err != nil {
		return localDockerStoragePaths{}, err
	}
	for _, child := range []string{"data", "projects"} {
		if err := ensureLocalDockerStorageDirectory(root, staging+"/"+child, 0700); err != nil {
			return localDockerStoragePaths{}, err
		}
	}
	if err := writeLocalDockerStorageMetadata(root, staging+"/"+localDockerStorageMetadataFile, metadata); err != nil {
		return localDockerStoragePaths{}, err
	}
	if err := root.Rename(staging, paths.WorkspaceName); err != nil {
		existing, readErr := readLocalDockerStorageMetadata(root, paths)
		if readErr != nil || existing != metadata {
			return localDockerStoragePaths{}, ErrLaunchStageBindingConflict
		}
	}
	return paths, nil
}

func (p *LocalDockerProvider) readStorageDirectories(volume StorageVolume) (localDockerStoragePaths, error) {
	if volume.ID == "" || volume.AccountID == "" || volume.WorkspaceID == "" {
		return localDockerStoragePaths{}, fmt.Errorf("local_docker_storage_identity_invalid")
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
	expected := localDockerStorageMetadata{SchemaVersion: 1, StorageID: volume.ID, AccountID: volume.AccountID, WorkspaceID: volume.WorkspaceID}
	if metadata != expected || volume.ProviderResourceID != "" && volume.ProviderResourceID != "directory/"+paths.WorkspaceName {
		return localDockerStoragePaths{}, ErrLaunchStageBindingConflict
	}
	return paths, nil
}
