package fabric

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const localDockerGatewayVersionsDir = "versions"

func validateLocalDockerGatewaySecretRoot(path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\r\n,") {
		return fmt.Errorf("local_docker_gateway_secret_root_invalid")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return fmt.Errorf("local_docker_gateway_secret_root_invalid")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return fmt.Errorf("local_docker_gateway_secret_root_invalid")
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("local_docker_gateway_secret_root_invalid")
	}
	defer directory.Close()
	openedInfo, err := directory.Stat()
	if err != nil || !openedInfo.IsDir() || openedInfo.Mode().Perm() != 0700 {
		return fmt.Errorf("local_docker_gateway_secret_root_invalid")
	}
	return nil
}

func validLocalDockerGatewaySecretRef(secretRef string) bool {
	if !strings.HasPrefix(secretRef, "opl-gateway-") || len(secretRef) > 253 || strings.Contains(secretRef, "..") {
		return false
	}
	for _, char := range secretRef {
		if !((char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-') {
			return false
		}
	}
	return true
}

func (p *LocalDockerProvider) openGatewaySecretRoot() (*os.Root, error) {
	if p.gatewaySecretRootErr != nil {
		return nil, p.gatewaySecretRootErr
	}
	if err := validateLocalDockerGatewaySecretRoot(p.gatewaySecretRoot); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(p.gatewaySecretRoot)
	if err != nil {
		return nil, fmt.Errorf("local_docker_gateway_secret_root_unavailable")
	}
	return root, nil
}

func (p *LocalDockerProvider) gatewaySecretVersionHostPath(secretRef string, metadata localDockerGatewayMetadata) (string, error) {
	if p.gatewaySecretRootErr != nil || !validLocalDockerGatewaySecretRef(secretRef) {
		return "", fmt.Errorf("local_docker_gateway_secret_path_invalid")
	}
	_, current, err := p.readGatewaySecretFiles(secretRef)
	if err != nil {
		return "", err
	}
	if current != metadata {
		return "", ErrLaunchStageBindingConflict
	}
	digest := strings.TrimPrefix(metadata.Fingerprint, "sha256:")
	versionName, err := localDockerGatewayVersionDir(digest)
	if err != nil || metadata.Version != digest[:16] {
		return "", ErrLaunchStageBindingConflict
	}
	return filepath.Join(p.gatewaySecretRoot, secretRef, localDockerGatewayVersionsDir, versionName), nil
}

func localDockerGatewayVersionDir(digest string) (string, error) {
	if len(digest) != 64 || digest != strings.ToLower(digest) || !validDigest(digest) {
		return "", fmt.Errorf("local_docker_gateway_secret_digest_invalid")
	}
	return "sha256-" + digest, nil
}

func localDockerGatewayCurrentTarget(digest string) (string, error) {
	version, err := localDockerGatewayVersionDir(digest)
	if err != nil {
		return "", err
	}
	return localDockerGatewayVersionsDir + "/" + version, nil
}

func syncLocalDockerSecretPath(root *os.Root, name string) error {
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func writeLocalDockerSecretFile(root *os.Root, name string, body []byte, mode os.FileMode) error {
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err = file.Write(body); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func localDockerSecretStagingName() (string, error) {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return ".staging-" + hex.EncodeToString(random), nil
}

func (p *LocalDockerProvider) writeGatewaySecret(secretRef string, key []byte, metadata localDockerGatewayMetadata) error {
	if !validLocalDockerGatewaySecretRef(secretRef) || metadata.SecretRef != secretRef || len(key) == 0 {
		return fmt.Errorf("local_docker_secret_identity_mismatch")
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(key))
	versionName, err := localDockerGatewayVersionDir(digest)
	if err != nil || metadata.Fingerprint != "sha256:"+digest || metadata.Version != digest[:16] {
		return fmt.Errorf("local_docker_secret_identity_mismatch")
	}
	meta, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	root, err := p.openGatewaySecretRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.MkdirAll(secretRef+"/"+localDockerGatewayVersionsDir, 0711); err != nil {
		return err
	}
	for _, directory := range []string{secretRef, secretRef + "/" + localDockerGatewayVersionsDir} {
		info, statErr := root.Lstat(directory)
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0711 {
			return ErrLaunchStageBindingConflict
		}
	}
	versionPath := secretRef + "/" + localDockerGatewayVersionsDir + "/" + versionName
	if info, statErr := root.Lstat(versionPath); statErr == nil {
		if !info.IsDir() {
			return ErrLaunchStageBindingConflict
		}
		storedKey, storedMetadata, readErr := readLocalDockerGatewayVersion(root, secretRef, versionName)
		if readErr != nil || string(storedKey) != string(key) || storedMetadata != metadata {
			return ErrLaunchStageBindingConflict
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	} else {
		stagingName, randomErr := localDockerSecretStagingName()
		if randomErr != nil {
			return randomErr
		}
		stagingPath := secretRef + "/" + localDockerGatewayVersionsDir + "/" + stagingName
		if err := root.Mkdir(stagingPath, 0711); err != nil {
			return err
		}
		if err := writeLocalDockerSecretFile(root, stagingPath+"/"+localDockerGatewayKeyFile, key, 0444); err != nil {
			return err
		}
		if err := writeLocalDockerSecretFile(root, stagingPath+"/"+localDockerGatewayMetaFile, meta, 0400); err != nil {
			return err
		}
		if err := syncLocalDockerSecretPath(root, stagingPath); err != nil {
			return err
		}
		if err := root.Rename(stagingPath, versionPath); err != nil {
			if storedKey, storedMetadata, readErr := readLocalDockerGatewayVersion(root, secretRef, versionName); readErr != nil || string(storedKey) != string(key) || storedMetadata != metadata {
				return err
			}
			_ = root.RemoveAll(stagingPath)
		}
		if err := syncLocalDockerSecretPath(root, secretRef+"/"+localDockerGatewayVersionsDir); err != nil {
			return err
		}
	}
	currentTarget, _ := localDockerGatewayCurrentTarget(digest)
	tempName, err := localDockerSecretStagingName()
	if err != nil {
		return err
	}
	tempCurrent := secretRef + "/.current-" + tempName
	if err := root.Symlink(currentTarget, tempCurrent); err != nil {
		return err
	}
	if err := root.Rename(tempCurrent, secretRef+"/current"); err != nil {
		return err
	}
	return syncLocalDockerSecretPath(root, secretRef)
}

func readLocalDockerGatewayVersion(root *os.Root, secretRef, versionName string) ([]byte, localDockerGatewayMetadata, error) {
	base := secretRef + "/" + localDockerGatewayVersionsDir + "/" + versionName
	for _, directory := range []string{secretRef, secretRef + "/" + localDockerGatewayVersionsDir, base} {
		info, err := root.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0711 {
			return nil, localDockerGatewayMetadata{}, ErrLaunchStageBindingConflict
		}
	}
	for name, mode := range map[string]os.FileMode{localDockerGatewayKeyFile: 0444, localDockerGatewayMetaFile: 0400} {
		info, err := root.Lstat(base + "/" + name)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != mode {
			return nil, localDockerGatewayMetadata{}, ErrLaunchStageBindingConflict
		}
	}
	key, err := root.ReadFile(base + "/" + localDockerGatewayKeyFile)
	if err != nil {
		return nil, localDockerGatewayMetadata{}, err
	}
	body, err := root.ReadFile(base + "/" + localDockerGatewayMetaFile)
	if err != nil {
		return nil, localDockerGatewayMetadata{}, err
	}
	var metadata localDockerGatewayMetadata
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&metadata) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		return nil, localDockerGatewayMetadata{}, ErrLaunchStageBindingConflict
	}
	return key, metadata, nil
}

func (p *LocalDockerProvider) readGatewaySecretFiles(secretRef string) ([]byte, localDockerGatewayMetadata, error) {
	if !validLocalDockerGatewaySecretRef(secretRef) {
		return nil, localDockerGatewayMetadata{}, ErrLaunchStageBindingConflict
	}
	root, err := p.openGatewaySecretRoot()
	if err != nil {
		return nil, localDockerGatewayMetadata{}, err
	}
	defer root.Close()
	secretInfo, err := root.Lstat(secretRef)
	if errors.Is(err, os.ErrNotExist) {
		return nil, localDockerGatewayMetadata{}, ErrWorkspaceLaunchResourceAbsent
	}
	if err != nil {
		return nil, localDockerGatewayMetadata{}, err
	}
	if !secretInfo.IsDir() || secretInfo.Mode()&os.ModeSymlink != 0 {
		return nil, localDockerGatewayMetadata{}, ErrLaunchStageBindingConflict
	}
	if secretInfo.Mode().Perm() != 0711 {
		return nil, localDockerGatewayMetadata{}, ErrLaunchStageBindingConflict
	}
	currentTarget, err := root.Readlink(secretRef + "/current")
	if errors.Is(err, os.ErrNotExist) {
		return nil, localDockerGatewayMetadata{}, ErrWorkspaceLaunchResourceAbsent
	}
	if err != nil || !strings.HasPrefix(currentTarget, localDockerGatewayVersionsDir+"/sha256-") || strings.Contains(currentTarget, "..") || filepath.IsAbs(currentTarget) {
		return nil, localDockerGatewayMetadata{}, ErrLaunchStageBindingConflict
	}
	versionName := strings.TrimPrefix(currentTarget, localDockerGatewayVersionsDir+"/")
	digest := strings.TrimPrefix(versionName, "sha256-")
	if expected, targetErr := localDockerGatewayCurrentTarget(digest); targetErr != nil || expected != currentTarget {
		return nil, localDockerGatewayMetadata{}, ErrLaunchStageBindingConflict
	}
	key, metadata, err := readLocalDockerGatewayVersion(root, secretRef, versionName)
	if err != nil {
		return nil, localDockerGatewayMetadata{}, err
	}
	actualDigest := fmt.Sprintf("%x", sha256.Sum256(key))
	if actualDigest != digest || metadata.SecretRef != secretRef || metadata.Fingerprint != "sha256:"+digest || metadata.Version != digest[:16] ||
		metadata.AccountID == "" || metadata.WorkspaceID == "" || metadata.WorkspaceAPIKeyID <= 0 {
		return nil, localDockerGatewayMetadata{}, ErrLaunchStageBindingConflict
	}
	return key, metadata, nil
}
