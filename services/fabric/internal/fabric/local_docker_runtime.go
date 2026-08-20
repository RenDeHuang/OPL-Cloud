package fabric

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	localDockerGatewayKeyFile                 = "opl_gateway_api_key"
	localDockerGatewayMetaFile                = "opl_gateway_metadata.json"
	localDockerWebUIPasswordFile              = "opl_webui_password"
	localDockerWebUISessionSecretFile         = "webui_session_secret"
	localDockerRuntimeSecretMountPath         = "/run/secrets"
	maxLocalDockerRuntimeSecretArchiveBytes   = 64 << 10
	maxLocalDockerRuntimeSecretKeyBytes       = 16 << 10
	maxLocalDockerRuntimeSecretMetadataBytes  = 4 << 10
	maxLocalDockerRuntimeWebUICredentialBytes = 1 << 10
	localDockerRuntimeHealthCommand           = `node -e 'fetch("http://127.0.0.1:3000/").then(r=>process.exit(r.ok?0:1)).catch(()=>process.exit(1))'`
	localDockerRuntimeHealthInterval          = int64(5 * time.Second)
	localDockerRuntimeHealthTimeout           = int64(3 * time.Second)
	localDockerRuntimeHealthStartPeriod       = int64(10 * time.Second)
	localDockerRuntimeHealthRetries           = 120
	localDockerNanoCPUsPerCPU                 = int64(1_000_000_000)
	localDockerBytesPerGiB                    = int64(1024 * 1024 * 1024)
	localDockerComputePackageLabel            = "opl.compute.package.id"
	localDockerStorageSizeGBLabel             = "opl.storage.size-gb"
)

type localDockerGatewayMetadata struct {
	AccountID         string `json:"accountId"`
	WorkspaceID       string `json:"workspaceId"`
	WorkspaceAPIKeyID int64  `json:"workspaceApiKeyId"`
	SecretRef         string `json:"secretRef"`
	Fingerprint       string `json:"fingerprint"`
	Version           string `json:"version"`
}

func (p *LocalDockerProvider) gatewayMetadata(ctx context.Context, secretRef string) (localDockerGatewayMetadata, error) {
	_, metadata, err := p.readGatewaySecretFiles(secretRef)
	return metadata, err
}

type localDockerWebUICredentials struct {
	Password      []byte
	SessionSecret []byte
}

func localDockerWebUICredentialsFor(metadata localDockerGatewayMetadata) (localDockerWebUICredentials, error) {
	seed := strings.TrimSpace(os.Getenv("OPL_AIONUI_ADMIN_PASSWORD_SEED"))
	if seed == "" {
		return localDockerWebUICredentials{}, fmt.Errorf("local_docker_webui_credential_seed_required")
	}
	password := deriveAionUIAdminPassword(seed, metadata.WorkspaceID, metadata.Version)
	sessionSecret := deriveWebUISessionSecret(seed, metadata.WorkspaceID, metadata.Version)
	if metadata.WorkspaceID == "" || metadata.Version == "" || password == "" || sessionSecret == "" {
		return localDockerWebUICredentials{}, fmt.Errorf("local_docker_webui_credential_identity_invalid")
	}
	return localDockerWebUICredentials{Password: []byte(password), SessionSecret: []byte(sessionSecret)}, nil
}

func localDockerRuntimeAccess(metadata localDockerGatewayMetadata) (RuntimeAccess, error) {
	credentials, err := localDockerWebUICredentialsFor(metadata)
	if err != nil {
		return RuntimeAccess{}, err
	}
	return RuntimeAccess{
		Username: webuiUsername, Password: string(credentials.Password), CredentialStatus: "configured",
		CredentialVersion: metadata.Version, SecretRef: metadata.SecretRef,
	}, nil
}

func (p *LocalDockerProvider) UpsertGatewaySecret(ctx context.Context, input GatewaySecretInput) (GatewaySecret, error) {
	secretRef := gatewaySecretName(input.WorkspaceID)
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(input.GatewayAPIKey)))
	if input.Fingerprint != "sha256:"+digest {
		return GatewaySecret{}, fmt.Errorf("local_docker_secret_identity_mismatch")
	}
	metadata := localDockerGatewayMetadata{
		AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, WorkspaceAPIKeyID: input.WorkspaceAPIKeyID,
		SecretRef: secretRef, Fingerprint: "sha256:" + digest, Version: digest[:16],
	}
	writeAttempt, beginErr := beginProviderMutation(ctx, "local_docker_secret_write", "gateway_secret", secretRef, metadata.Version)
	if beginErr != nil {
		return GatewaySecret{}, beginErr
	}
	if writeAttempt != nil && !writeAttempt.Fresh {
		secret, readErr := p.ReadGatewaySecretByDigest(ctx, GatewaySecretReadbackInput{
			AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, WorkspaceAPIKeyID: input.WorkspaceAPIKeyID,
			SecretRef: secretRef, Fingerprint: input.Fingerprint, KeyDigest: digest,
		})
		if readErr == nil {
			if completeErr := writeAttempt.complete(ctx, providerRequestID("docker-secret-write", secretRef), secret, nil); completeErr != nil {
				return GatewaySecret{}, completeErr
			}
			return secret, nil
		}
		if !errors.Is(readErr, ErrWorkspaceLaunchResourceAbsent) {
			return GatewaySecret{}, readErr
		}
		claimed, claimErr := writeAttempt.claimReplay(ctx)
		if claimErr != nil || !claimed {
			return GatewaySecret{}, firstNonNil(claimErr, ErrWorkspaceLaunchPending)
		}
		secret, readErr = p.ReadGatewaySecretByDigest(ctx, GatewaySecretReadbackInput{
			AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, WorkspaceAPIKeyID: input.WorkspaceAPIKeyID,
			SecretRef: secretRef, Fingerprint: input.Fingerprint, KeyDigest: digest,
		})
		if readErr == nil {
			if completeErr := writeAttempt.complete(ctx, providerRequestID("docker-secret-write", secretRef), secret, nil); completeErr != nil {
				return GatewaySecret{}, completeErr
			}
			return secret, nil
		}
		if !errors.Is(readErr, ErrWorkspaceLaunchResourceAbsent) {
			_ = writeAttempt.complete(ctx, "", GatewaySecret{SecretRef: secretRef}, readErr)
			return GatewaySecret{}, readErr
		}
		if dispatchErr := writeAttempt.markReplayDispatch(ctx); dispatchErr != nil {
			return GatewaySecret{}, dispatchErr
		}
	}
	if writeAttempt == nil || writeAttempt.Fresh || writeAttempt.Replay {
		if err := p.writeGatewaySecret(secretRef, []byte(input.GatewayAPIKey), metadata); err != nil {
			_ = writeAttempt.complete(ctx, "", GatewaySecret{SecretRef: secretRef}, err)
			return GatewaySecret{}, err
		}
	}
	secret, err := p.ReadGatewaySecretByDigest(ctx, GatewaySecretReadbackInput{
		AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, WorkspaceAPIKeyID: input.WorkspaceAPIKeyID,
		SecretRef: secretRef, Fingerprint: input.Fingerprint, KeyDigest: digest,
	})
	if err != nil {
		return GatewaySecret{}, err
	}
	if completeErr := writeAttempt.complete(ctx, providerRequestID("docker-secret-write", secretRef), secret, nil); completeErr != nil {
		return GatewaySecret{}, completeErr
	}
	return secret, nil
}

func (p *LocalDockerProvider) ReadGatewaySecret(ctx context.Context, input GatewaySecretInput) (GatewaySecret, error) {
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(input.GatewayAPIKey)))
	return p.ReadGatewaySecretByDigest(ctx, GatewaySecretReadbackInput{
		AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, WorkspaceAPIKeyID: input.WorkspaceAPIKeyID,
		SecretRef: gatewaySecretName(input.WorkspaceID), Fingerprint: input.Fingerprint, KeyDigest: digest,
	})
}

func (p *LocalDockerProvider) ReadGatewaySecretByDigest(ctx context.Context, input GatewaySecretReadbackInput) (GatewaySecret, error) {
	key, metadata, err := p.readGatewaySecretFiles(input.SecretRef)
	if err != nil {
		return GatewaySecret{}, err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(key))
	if metadata.AccountID != input.AccountID || metadata.WorkspaceID != input.WorkspaceID || metadata.WorkspaceAPIKeyID != input.WorkspaceAPIKeyID ||
		metadata.SecretRef != input.SecretRef || metadata.Fingerprint != input.Fingerprint || digest != input.KeyDigest || metadata.Version != digest[:16] {
		return GatewaySecret{}, ErrLaunchStageBindingConflict
	}
	return GatewaySecret{SecretRef: input.SecretRef, Version: metadata.Version, Fingerprint: metadata.Fingerprint}, nil
}

func (p *LocalDockerProvider) RemoveGatewaySecret(ctx context.Context, workspaceID string) error {
	secretRef := gatewaySecretName(workspaceID)
	_, metadata, err := p.readGatewaySecretFiles(secretRef)
	if errors.Is(err, ErrWorkspaceLaunchResourceAbsent) {
		return nil
	}
	if err != nil || metadata.WorkspaceID != workspaceID || metadata.SecretRef != secretRef {
		return firstNonNil(err, fmt.Errorf("local_docker_secret_destroy_ownership_mismatch"))
	}
	root, err := p.openGatewaySecretRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	return root.RemoveAll(secretRef)
}

type dockerContainerInspect struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Image  string `json:"Image"`
	Config struct {
		Image       string            `json:"Image"`
		Labels      map[string]string `json:"Labels"`
		Healthcheck *struct {
			Test        []string `json:"Test"`
			Interval    int64    `json:"Interval"`
			Timeout     int64    `json:"Timeout"`
			Retries     int      `json:"Retries"`
			StartPeriod int64    `json:"StartPeriod"`
		} `json:"Healthcheck"`
	} `json:"Config"`
	State struct {
		Status  string `json:"Status"`
		Running bool   `json:"Running"`
		Health  *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
	NetworkSettings struct {
		Ports map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"Ports"`
		Networks map[string]dockerEndpointSettings `json:"Networks"`
	} `json:"NetworkSettings"`
	HostConfig struct {
		NanoCPUs   int64             `json:"NanoCpus"`
		Memory     int64             `json:"Memory"`
		MemorySwap int64             `json:"MemorySwap"`
		Mounts     []dockerHostMount `json:"Mounts"`
	} `json:"HostConfig"`
	Mounts []dockerRuntimeMount `json:"Mounts"`
}

type dockerEndpointSettings struct {
	NetworkID string `json:"NetworkID"`
}

func exactContainerNetworkMembership(container dockerContainerInspect, networkName, networkID string) (bool, error) {
	if networkName == "" || networkID == "" {
		return false, fmt.Errorf("local_docker_runtime_network_binding_mismatch")
	}
	seenIDs := make(map[string]string, len(container.NetworkSettings.Networks))
	for name, endpoint := range container.NetworkSettings.Networks {
		if name == "" || endpoint.NetworkID == "" {
			return false, fmt.Errorf("local_docker_runtime_network_binding_mismatch")
		}
		if previous, duplicate := seenIDs[endpoint.NetworkID]; duplicate && previous != name {
			return false, fmt.Errorf("local_docker_runtime_network_binding_mismatch")
		}
		seenIDs[endpoint.NetworkID] = name
		if name == networkName && endpoint.NetworkID != networkID || name != networkName && endpoint.NetworkID == networkID {
			return false, fmt.Errorf("local_docker_runtime_network_binding_mismatch")
		}
	}
	endpoint, exists := container.NetworkSettings.Networks[networkName]
	return exists && endpoint.NetworkID == networkID, nil
}

func localDockerNetworkID(allocation ComputeAllocation) (string, error) {
	if !strings.HasPrefix(allocation.ProviderResourceID, "network/") {
		return "", fmt.Errorf("local_docker_runtime_network_binding_mismatch")
	}
	networkID := strings.TrimPrefix(allocation.ProviderResourceID, "network/")
	if networkID == "" || strings.Contains(networkID, "/") {
		return "", fmt.Errorf("local_docker_runtime_network_binding_mismatch")
	}
	return networkID, nil
}

func (p *LocalDockerProvider) runtimeGatewayNetworkStatus(ctx context.Context, runtime dockerContainerInspect, compute ComputeAllocation) (bool, error) {
	if p.runtimeGatewayContainer == "" {
		return true, nil
	}
	networkName := localDockerName("opl-compute", compute.ID)
	networkID, err := localDockerNetworkID(compute)
	if err != nil {
		return false, err
	}
	runtimeBound, err := exactContainerNetworkMembership(runtime, networkName, networkID)
	if err != nil || !runtimeBound {
		return false, firstNonNil(err, fmt.Errorf("local_docker_runtime_network_binding_mismatch"))
	}
	gateway, exists, err := p.inspectContainer(ctx, p.runtimeGatewayContainer)
	if err != nil || !exists || !gateway.State.Running || gateway.Config.Labels["opl.fabric.local-docker.gateway"] != "control-plane" {
		return false, firstNonNil(err, fmt.Errorf("local_docker_runtime_gateway_identity_mismatch"))
	}
	return exactContainerNetworkMembership(gateway, networkName, networkID)
}

func (p *LocalDockerProvider) ensureRuntimeGatewayNetwork(ctx context.Context, runtime dockerContainerInspect, compute ComputeAllocation, attempt *providerMutationAttempt) error {
	bound, err := p.runtimeGatewayNetworkStatus(ctx, runtime, compute)
	if err != nil || bound || p.runtimeGatewayContainer == "" {
		return err
	}
	if attempt == nil {
		return fmt.Errorf("local_docker_runtime_gateway_mutation_binding_required")
	}
	if !attempt.Fresh && !attempt.Replay {
		claimed, claimErr := attempt.claimReplay(ctx)
		if claimErr != nil || !claimed {
			return firstNonNil(claimErr, ErrWorkspaceLaunchPending)
		}
		bound, err = p.runtimeGatewayNetworkStatus(ctx, runtime, compute)
		if err != nil || bound {
			return err
		}
		if dispatchErr := attempt.markReplayDispatch(ctx); dispatchErr != nil {
			return dispatchErr
		}
	}
	networkName := localDockerName("opl-compute", compute.ID)
	_, connectErr := p.runner.Run(ctx, nil, "network", "connect", networkName, p.runtimeGatewayContainer)
	bound, readErr := p.runtimeGatewayNetworkStatus(ctx, runtime, compute)
	if readErr != nil {
		return readErr
	}
	if bound {
		return nil
	}
	if connectErr != nil {
		return ErrWorkspaceLaunchPending
	}
	return fmt.Errorf("local_docker_runtime_gateway_network_readback_mismatch")
}

func (p *LocalDockerProvider) verifyRuntimeGatewayNetwork(ctx context.Context, container dockerContainerInspect) error {
	if p.runtimeGatewayContainer == "" {
		return nil
	}
	labels := container.Config.Labels
	compute := ComputeAllocation{ID: labels["opl.compute.id"], AccountID: labels["opl.account.id"], WorkspaceID: labels["opl.workspace.id"]}
	if compute.ID == "" || compute.AccountID == "" || compute.WorkspaceID == "" {
		return fmt.Errorf("local_docker_runtime_network_binding_mismatch")
	}
	readback, err := p.ReadComputeAllocation(ctx, compute)
	if err != nil {
		return err
	}
	bound, err := p.runtimeGatewayNetworkStatus(ctx, container, readback)
	if err != nil || !bound {
		return firstNonNil(err, fmt.Errorf("local_docker_runtime_gateway_network_readback_mismatch"))
	}
	return nil
}

type dockerHostMount struct {
	Type        string `json:"Type"`
	Source      string `json:"Source"`
	Target      string `json:"Target"`
	ReadOnly    bool   `json:"ReadOnly"`
	BindOptions struct {
		Propagation string `json:"Propagation"`
	} `json:"BindOptions"`
	TmpfsOptions struct {
		Mode uint32 `json:"Mode"`
	} `json:"TmpfsOptions"`
}

type dockerRuntimeMount struct {
	Type        string `json:"Type"`
	Name        string `json:"Name"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	RW          bool   `json:"RW"`
	Propagation string `json:"Propagation"`
}

func (p *LocalDockerProvider) inspectContainer(ctx context.Context, name string) (dockerContainerInspect, bool, error) {
	inventory, err := p.runner.Run(ctx, nil, "container", "ls", "-a", "--no-trunc", "--filter", "name=^/"+name+"$", "--format", "{{json .}}")
	if err != nil {
		return dockerContainerInspect{}, false, err
	}
	row, exists, err := decodeDockerObjectInventory(inventory, name)
	if err != nil || !exists {
		return dockerContainerInspect{}, false, err
	}
	output, err := p.runner.Run(ctx, nil, "container", "inspect", firstNonEmpty(row.ID, name))
	if err != nil {
		return dockerContainerInspect{}, false, err
	}
	var values []dockerContainerInspect
	if json.Unmarshal(output, &values) != nil || len(values) != 1 || values[0].ID == "" || row.ID != "" && values[0].ID != row.ID {
		return dockerContainerInspect{}, false, fmt.Errorf("local_docker_runtime_readback_invalid")
	}
	values[0].Name = strings.TrimPrefix(values[0].Name, "/")
	if values[0].Name != name {
		return dockerContainerInspect{}, false, fmt.Errorf("local_docker_runtime_readback_invalid")
	}
	return values[0], true, nil
}

func runtimeWritableBindPresent(container dockerContainerInspect, source, destination string) bool {
	hostMatches, runtimeMatches := 0, 0
	for _, mount := range container.HostConfig.Mounts {
		if mount.Type == "bind" && mount.Source == source && mount.Target == destination && !mount.ReadOnly && mount.BindOptions.Propagation == "rprivate" {
			hostMatches++
		}
	}
	for _, mount := range container.Mounts {
		if mount.Type == "bind" && mount.Destination == destination && mount.RW && mount.Propagation == "rprivate" {
			runtimeMatches++
		}
	}
	return hostMatches == 1 && runtimeMatches == 1
}

func runtimeTmpfsPresent(container dockerContainerInspect, destination string) bool {
	hostMatches, runtimeMatches := 0, 0
	for _, mount := range container.HostConfig.Mounts {
		if mount.Type == "tmpfs" && mount.Source == "" && mount.Target == destination && !mount.ReadOnly && mount.TmpfsOptions.Mode == 0700 {
			hostMatches++
		}
	}
	for _, mount := range container.Mounts {
		if mount.Type == "tmpfs" && mount.Destination == destination && mount.RW {
			runtimeMatches++
		}
	}
	return hostMatches == 1 && runtimeMatches == 1
}

func mountTargetsOverlap(left, right string) bool {
	return left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}

func validRuntimeSecretMountViews(container dockerContainerInspect) bool {
	if len(container.HostConfig.Mounts) == 0 || len(container.HostConfig.Mounts) != len(container.Mounts) {
		return false
	}
	hostTargets := make(map[string]struct{}, len(container.HostConfig.Mounts))
	runtimeTargets := make(map[string]struct{}, len(container.Mounts))
	for _, mount := range container.HostConfig.Mounts {
		if mount.Type == "" || mount.Target == "" || mount.Type != "tmpfs" && mount.Source == "" || mount.Type == "tmpfs" && mount.Source != "" {
			return false
		}
		if _, exists := hostTargets[mount.Target]; exists {
			return false
		}
		hostTargets[mount.Target] = struct{}{}
	}
	for _, mount := range container.Mounts {
		if mount.Type == "" || mount.Destination == "" || mount.Type != "tmpfs" && mount.Source == "" || mount.Type == "tmpfs" && mount.Source != "" {
			return false
		}
		if _, exists := runtimeTargets[mount.Destination]; exists {
			return false
		}
		runtimeTargets[mount.Destination] = struct{}{}
	}
	for _, hostMount := range container.HostConfig.Mounts {
		matches := 0
		for _, runtimeMount := range container.Mounts {
			if hostMount.Target != runtimeMount.Destination || hostMount.Type != runtimeMount.Type || hostMount.ReadOnly == runtimeMount.RW {
				continue
			}
			if hostMount.Type == "bind" && hostMount.BindOptions.Propagation != runtimeMount.Propagation {
				continue
			}
			matches++
		}
		if matches != 1 {
			return false
		}
	}
	var hostSecret *dockerHostMount
	for index := range container.HostConfig.Mounts {
		mount := &container.HostConfig.Mounts[index]
		if mountTargetsOverlap(mount.Target, localDockerRuntimeSecretMountPath) {
			if mount.Target != localDockerRuntimeSecretMountPath || hostSecret != nil {
				return false
			}
			hostSecret = mount
		}
	}
	var runtimeSecret *dockerRuntimeMount
	for index := range container.Mounts {
		mount := &container.Mounts[index]
		if mountTargetsOverlap(mount.Destination, localDockerRuntimeSecretMountPath) {
			if mount.Destination != localDockerRuntimeSecretMountPath || runtimeSecret != nil {
				return false
			}
			runtimeSecret = mount
		}
	}
	if hostSecret == nil || runtimeSecret == nil || hostSecret.Type != "bind" || runtimeSecret.Type != "bind" ||
		!hostSecret.ReadOnly || runtimeSecret.RW || hostSecret.BindOptions.Propagation != "rprivate" || runtimeSecret.Propagation != "rprivate" {
		return false
	}
	for _, mount := range container.HostConfig.Mounts {
		if mount.Target != localDockerRuntimeSecretMountPath && mount.Source == hostSecret.Source {
			return false
		}
	}
	for _, mount := range container.Mounts {
		if mount.Destination != localDockerRuntimeSecretMountPath && mount.Source == runtimeSecret.Source {
			return false
		}
	}
	return true
}

func validRuntimeWorkspaceMountViews(container dockerContainerInspect, paths localDockerStoragePaths) bool {
	return validRuntimeSecretMountViews(container) &&
		runtimeWritableBindPresent(container, paths.Data, "/data") &&
		runtimeWritableBindPresent(container, paths.Projects, "/projects") &&
		runtimeTmpfsPresent(container, "/recovery")
}

func validRuntimeHealthcheck(container dockerContainerInspect) bool {
	healthcheck := container.Config.Healthcheck
	return healthcheck != nil && len(healthcheck.Test) == 2 && healthcheck.Test[0] == "CMD-SHELL" &&
		healthcheck.Test[1] == localDockerRuntimeHealthCommand && healthcheck.Interval == localDockerRuntimeHealthInterval &&
		healthcheck.Timeout == localDockerRuntimeHealthTimeout && healthcheck.StartPeriod == localDockerRuntimeHealthStartPeriod &&
		healthcheck.Retries == localDockerRuntimeHealthRetries
}

func validateRuntimeGatewaySecretArchive(body []byte, expected localDockerGatewayMetadata) error {
	fail := func() error { return fmt.Errorf("local_docker_runtime_secret_archive_mismatch") }
	if len(body) == 0 || len(body) > maxLocalDockerRuntimeSecretArchiveBytes || expected.AccountID == "" || expected.WorkspaceID == "" ||
		expected.WorkspaceAPIKeyID <= 0 || !validLocalDockerGatewaySecretRef(expected.SecretRef) {
		return fail()
	}
	reader := tar.NewReader(bytes.NewReader(body))
	var key, metadataBody, password, sessionSecret []byte
	rootSeen := false
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fail()
		}
		switch header.Name {
		case "./":
			if rootSeen || header.Typeflag != tar.TypeDir || header.Size != 0 {
				return fail()
			}
			rootSeen = true
		case "./" + localDockerGatewayKeyFile:
			if key != nil || header.Typeflag != tar.TypeReg || header.Mode&0777 != 0444 || header.Size <= 0 || header.Size > maxLocalDockerRuntimeSecretKeyBytes {
				return fail()
			}
			key, err = io.ReadAll(io.LimitReader(reader, maxLocalDockerRuntimeSecretKeyBytes+1))
			if err != nil || int64(len(key)) != header.Size {
				return fail()
			}
		case "./" + localDockerGatewayMetaFile:
			if metadataBody != nil || header.Typeflag != tar.TypeReg || header.Mode&0777 != 0400 || header.Size <= 0 || header.Size > maxLocalDockerRuntimeSecretMetadataBytes {
				return fail()
			}
			metadataBody, err = io.ReadAll(io.LimitReader(reader, maxLocalDockerRuntimeSecretMetadataBytes+1))
			if err != nil || int64(len(metadataBody)) != header.Size {
				return fail()
			}
		case "./" + localDockerWebUIPasswordFile:
			if password != nil || header.Typeflag != tar.TypeReg || header.Mode&0777 != 0400 || header.Size <= 0 || header.Size > maxLocalDockerRuntimeWebUICredentialBytes {
				return fail()
			}
			password, err = io.ReadAll(io.LimitReader(reader, maxLocalDockerRuntimeWebUICredentialBytes+1))
			if err != nil || int64(len(password)) != header.Size {
				return fail()
			}
		case "./" + localDockerWebUISessionSecretFile:
			if sessionSecret != nil || header.Typeflag != tar.TypeReg || header.Mode&0777 != 0400 || header.Size <= 0 || header.Size > maxLocalDockerRuntimeWebUICredentialBytes {
				return fail()
			}
			sessionSecret, err = io.ReadAll(io.LimitReader(reader, maxLocalDockerRuntimeWebUICredentialBytes+1))
			if err != nil || int64(len(sessionSecret)) != header.Size {
				return fail()
			}
		default:
			return fail()
		}
	}
	if !rootSeen || key == nil || metadataBody == nil || password == nil || sessionSecret == nil {
		return fail()
	}
	decoder := json.NewDecoder(bytes.NewReader(metadataBody))
	decoder.DisallowUnknownFields()
	var actual localDockerGatewayMetadata
	if err := decoder.Decode(&actual); err != nil || decoder.Decode(&struct{}{}) != io.EOF || actual != expected {
		return fail()
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(key))
	if expected.Fingerprint != "sha256:"+digest || expected.Version != digest[:16] {
		return fail()
	}
	credentials, err := localDockerWebUICredentialsFor(expected)
	if err != nil || !bytes.Equal(password, credentials.Password) || !bytes.Equal(sessionSecret, credentials.SessionSecret) {
		return fail()
	}
	return nil
}

func (p *LocalDockerProvider) verifyRuntimeGatewaySecret(ctx context.Context, container dockerContainerInspect, expected localDockerGatewayMetadata) error {
	if !validRuntimeSecretMountViews(container) || container.Name == "" || container.Config.Labels["opl.account.id"] != expected.AccountID ||
		container.Config.Labels["opl.workspace.id"] != expected.WorkspaceID || container.Config.Labels["opl.secret.ref"] != expected.SecretRef ||
		container.Config.Labels["opl.secret.version"] != expected.Version || container.Config.Labels["opl.secret.fingerprint"] != expected.Fingerprint {
		return fmt.Errorf("local_docker_runtime_secret_binding_mismatch")
	}
	body, err := p.runner.Run(ctx, nil, "container", "cp", container.Name+":"+localDockerRuntimeSecretMountPath+"/.", "-")
	if err != nil {
		return fmt.Errorf("local_docker_runtime_secret_archive_unavailable")
	}
	return validateRuntimeGatewaySecretArchive(body, expected)
}

func (p *LocalDockerProvider) configureRuntimeGateway(ctx context.Context, containerName, secretPath string) error {
	if !p.configureRuntimeGatewayEnabled {
		return nil
	}
	key, err := os.ReadFile(filepath.Join(secretPath, localDockerGatewayKeyFile))
	if err != nil || len(bytes.TrimSpace(key)) == 0 {
		return fmt.Errorf("local_docker_runtime_gateway_key_unavailable")
	}
	if _, err := p.runner.Run(ctx, key, "container", "exec", "-i", containerName, "opl", "system", "configure-codex", "--api-key-stdin", "--json"); err != nil {
		return fmt.Errorf("local_docker_runtime_gateway_configuration_failed")
	}
	return nil
}

func localRuntimeID(workspaceID string) string {
	return "rt_" + stableSuffix("local-docker", workspaceID)[:18]
}

func localRuntimeName(workspaceID string) string { return localDockerName("opl-runtime", workspaceID) }

type localDockerRuntimeCgroupLimits struct {
	CPU       int
	NanoCPUs  int64
	Memory    int64
	PackageID string
}

func (p *LocalDockerProvider) runtimeCgroupLimits(packageID string) (localDockerRuntimeCgroupLimits, bool) {
	item, ok := p.localDockerPackagePlan(packageID)
	if !ok {
		return localDockerRuntimeCgroupLimits{}, false
	}
	return p.runtimeCgroupLimitsForPlan(packageID, item.Compute)
}

func (p *LocalDockerProvider) runtimeCgroupLimitsForPlan(packageID string, plan ComputePlan) (localDockerRuntimeCgroupLimits, bool) {
	if plan.CPU <= 0 || plan.MemoryGB <= 0 {
		return localDockerRuntimeCgroupLimits{}, false
	}
	return localDockerRuntimeCgroupLimits{
		CPU: plan.CPU, NanoCPUs: int64(plan.CPU) * localDockerNanoCPUsPerCPU,
		Memory: int64(plan.MemoryGB) * localDockerBytesPerGiB, PackageID: packageID,
	}, true
}

func validRuntimeCgroupLimits(container dockerContainerInspect, limits localDockerRuntimeCgroupLimits) bool {
	return container.HostConfig.NanoCPUs == limits.NanoCPUs &&
		container.HostConfig.Memory == limits.Memory &&
		container.HostConfig.MemorySwap == limits.Memory
}

func (p *LocalDockerProvider) CreateWorkspaceRuntime(ctx context.Context, input WorkspaceRuntimeInput, compute ComputeAllocation, volume StorageVolume) (WorkspaceRuntime, error) {
	item, ok := p.localDockerPackagePlan(compute.PackageID)
	if !ok {
		return WorkspaceRuntime{}, ErrUnsupportedComputePackage
	}
	return p.createWorkspaceRuntimeWithPlan(ctx, input, compute, volume, item.Compute)
}

func (p *LocalDockerProvider) createWorkspaceRuntimeWithPlan(ctx context.Context, input WorkspaceRuntimeInput, compute ComputeAllocation, volume StorageVolume, plan ComputePlan) (WorkspaceRuntime, error) {
	var result WorkspaceRuntime
	err := p.withStorageQuotaLock(ctx, func() error {
		var lockedErr error
		result, lockedErr = p.createWorkspaceRuntimeLocked(ctx, input, compute, volume, plan)
		return lockedErr
	})
	return result, err
}

func (p *LocalDockerProvider) createWorkspaceRuntimeLocked(ctx context.Context, input WorkspaceRuntimeInput, compute ComputeAllocation, volume StorageVolume, plan ComputePlan) (WorkspaceRuntime, error) {
	computeReadback, err := p.ReadComputeAllocation(ctx, compute)
	if err != nil {
		return WorkspaceRuntime{}, err
	}
	limits, limitsOK := p.runtimeCgroupLimitsForPlan(computeReadback.PackageID, plan)
	if !limitsOK {
		return WorkspaceRuntime{}, ErrUnsupportedComputePackage
	}
	volumeReadback, err := p.ReadStorageVolume(ctx, volume)
	if err != nil {
		return WorkspaceRuntime{}, err
	}
	storagePaths, err := p.readStorageDirectories(volumeReadback)
	if err != nil {
		return WorkspaceRuntime{}, err
	}
	if err := ensureLocalDockerCodexHome(storagePaths.Data); err != nil {
		return WorkspaceRuntime{}, err
	}
	secretMetadata, err := p.gatewayMetadata(ctx, input.GatewaySecretRef)
	if err != nil {
		return WorkspaceRuntime{}, err
	}
	secretPath, err := p.gatewaySecretVersionHostPath(input.GatewaySecretRef, secretMetadata)
	if err != nil {
		return WorkspaceRuntime{}, err
	}
	name, runtimeID := localRuntimeName(input.WorkspaceID), localRuntimeID(input.WorkspaceID)
	labels := localDockerLabels(compute.AccountID, input.WorkspaceID, runtimeID, input.RuntimeOperationID, "runtime")
	for key, value := range map[string]string{
		"opl.compute.id": input.ComputeID, "opl.storage.id": input.VolumeID, "opl.attachment.id": input.AttachmentID,
		"opl.attachment.operation.id": input.AttachmentOperationID, "opl.secret.ref": input.GatewaySecretRef, "opl.image.ref": input.ImageID,
		"opl.secret.version": secretMetadata.Version, "opl.secret.fingerprint": secretMetadata.Fingerprint,
		localDockerComputePackageLabel: limits.PackageID, localDockerStorageSizeGBLabel: strconv.Itoa(volumeReadback.SizeGB),
	} {
		labels[key] = value
	}
	attempt, beginErr := beginProviderMutation(ctx, "local_docker_runtime_create", "workspace_runtime", runtimeID, name)
	if beginErr != nil {
		return WorkspaceRuntime{}, beginErr
	}
	container, exists, inspectErr := p.inspectContainer(ctx, name)
	if inspectErr != nil {
		return WorkspaceRuntime{}, inspectErr
	}
	if !exists {
		if attempt != nil && !attempt.Fresh {
			claimed, claimErr := attempt.claimReplay(ctx)
			if claimErr != nil || !claimed {
				return WorkspaceRuntime{}, firstNonNil(claimErr, ErrWorkspaceLaunchPending)
			}
			container, exists, inspectErr = p.inspectContainer(ctx, name)
			if inspectErr != nil {
				_ = attempt.complete(ctx, "", WorkspaceRuntime{ID: runtimeID, WorkspaceID: input.WorkspaceID}, inspectErr)
				return WorkspaceRuntime{}, inspectErr
			}
		}
		if !exists {
			root, rootErr := p.openStorageRoot()
			if rootErr != nil {
				return WorkspaceRuntime{}, rootErr
			}
			reservation := localDockerRuntimeReservationFor(input, limits)
			admissionErr := p.localDockerRuntimeCapacityAdmission(ctx, root, reservation)
			if admissionErr == nil {
				admissionErr = writeLocalDockerRuntimeReservation(root, reservation)
			}
			closeErr := root.Close()
			if admissionErr != nil || closeErr != nil {
				return WorkspaceRuntime{}, firstNonNil(admissionErr, closeErr)
			}
			if dispatchErr := attempt.markReplayDispatch(ctx); dispatchErr != nil {
				return WorkspaceRuntime{}, dispatchErr
			}
			args := append([]string{"run", "-d", "--name", name}, dockerLabelArgs(labels)...)
			args = append(args,
				"--health-cmd", localDockerRuntimeHealthCommand,
				"--health-interval", "5s", "--health-timeout", "3s", "--health-start-period", "10s", "--health-retries", "120",
				"--cpus", strconv.Itoa(limits.CPU),
				"--memory", strconv.FormatInt(limits.Memory, 10), "--memory-swap", strconv.FormatInt(limits.Memory, 10),
				"--network", localDockerName("opl-compute", computeReadback.ID),
				"--mount", "type=bind,source="+storagePaths.Data+",target=/data,bind-propagation=rprivate",
				"--mount", "type=bind,source="+storagePaths.Projects+",target=/projects,bind-propagation=rprivate",
				"--mount", "type=tmpfs,target=/recovery,tmpfs-mode=0700",
				"--mount", "type=bind,source="+secretPath+",target=/run/secrets,readonly,bind-propagation=rprivate",
				"-p", p.runtimeHost+"::3000",
				"-e", "OPL_WEBUI_DEPLOYMENT_MODE=cloud", "-e", "OPL_WEBUI_AUTH_MODE=password", "-e", "OPL_WEBUI_USERNAME="+webuiUsername,
				"-e", "OPL_WEBUI_PASSWORD_FILE=/run/secrets/"+localDockerWebUIPasswordFile,
				"-e", "OPL_WEBUI_SESSION_SECRET_FILE=/run/secrets/"+localDockerWebUISessionSecretFile,
				"-e", "OPL_WORKSPACE_ID="+input.WorkspaceID, "-e", "OPL_COMPUTE_ALLOCATION_ID="+input.ComputeID,
				"-e", "OPL_OWNER_ACCOUNT_ID="+compute.AccountID, "-e", "DATA_DIR=/data", "-e", "AIONUI_DATA_DIR=/data",
				"-e", "OPL_PROJECTS_DIR=/projects", "-e", "OPL_WORKSPACE_ROOT=/projects", "-e", "OPL_WEBUI_RECOVERY_DIR=/recovery",
				"-e", "AIONUI_ALLOW_REMOTE=true", "-e", "ALLOW_REMOTE=true", "-e", "HOME=/data", "-e", "CODEX_HOME=/data/.codex",
				input.ImageID,
			)
			if _, err := p.runner.Run(ctx, nil, args...); err != nil {
				_, stillExists, absentErr := p.inspectContainer(ctx, name)
				if absentErr == nil && !stillExists {
					root, rootErr := p.openStorageRoot()
					if rootErr == nil {
						rootErr = removeLocalDockerRuntimeReservation(root, reservation.ResourceID)
						closeErr := root.Close()
						rootErr = firstNonNil(rootErr, closeErr)
					}
					if rootErr != nil {
						err = firstNonNil(rootErr, err)
					}
				}
				_ = attempt.complete(ctx, "", WorkspaceRuntime{ID: runtimeID, WorkspaceID: input.WorkspaceID}, err)
				return WorkspaceRuntime{}, err
			}
			container, exists, inspectErr = p.inspectContainer(ctx, name)
		}
	}
	if inspectErr != nil || !exists || !exactDockerLabels(container.Config.Labels, labels) || !validRuntimeHealthcheck(container) ||
		!validRuntimeWorkspaceMountViews(container, storagePaths) || !validRuntimeCgroupLimits(container, limits) {
		readErr := fmt.Errorf("local_docker_runtime_readback_mismatch")
		_ = attempt.complete(ctx, "", WorkspaceRuntime{ID: runtimeID, WorkspaceID: input.WorkspaceID}, readErr)
		return WorkspaceRuntime{}, readErr
	}
	root, rootErr := p.openStorageRoot()
	if rootErr != nil {
		return WorkspaceRuntime{}, rootErr
	}
	reservationErr := ensureLocalDockerRuntimeReservation(root, localDockerRuntimeReservationFor(input, limits))
	closeErr := root.Close()
	if reservationErr != nil || closeErr != nil {
		return WorkspaceRuntime{}, firstNonNil(reservationErr, closeErr)
	}
	if secretMetadata.AccountID != compute.AccountID || secretMetadata.WorkspaceID != input.WorkspaceID || secretMetadata.SecretRef != input.GatewaySecretRef {
		readErr := fmt.Errorf("local_docker_runtime_secret_binding_mismatch")
		_ = attempt.complete(ctx, "", WorkspaceRuntime{ID: runtimeID, WorkspaceID: input.WorkspaceID}, readErr)
		return WorkspaceRuntime{}, readErr
	}
	if readErr := p.verifyRuntimeGatewaySecret(ctx, container, secretMetadata); readErr != nil {
		_ = attempt.complete(ctx, "", WorkspaceRuntime{ID: runtimeID, WorkspaceID: input.WorkspaceID}, readErr)
		return WorkspaceRuntime{}, readErr
	}
	if readErr := p.ensureRuntimeGatewayNetwork(ctx, container, computeReadback, attempt); readErr != nil {
		if !errors.Is(readErr, ErrWorkspaceLaunchPending) {
			_ = attempt.complete(ctx, "", WorkspaceRuntime{ID: runtimeID, WorkspaceID: input.WorkspaceID}, readErr)
		}
		return WorkspaceRuntime{}, readErr
	}
	resource, err := p.runtimeFromContainer(container)
	if err != nil {
		return WorkspaceRuntime{}, err
	}
	resource.Access, err = localDockerRuntimeAccess(secretMetadata)
	if err != nil {
		return WorkspaceRuntime{}, err
	}
	if !resource.Ready {
		if completeErr := attempt.complete(ctx, resource.ProviderRequestID, resource, nil); completeErr != nil {
			return WorkspaceRuntime{}, completeErr
		}
		return resource, ErrWorkspaceLaunchPending
	}
	if err := p.configureRuntimeGateway(ctx, container.Name, secretPath); err != nil {
		_ = attempt.complete(ctx, resource.ProviderRequestID, resource, err)
		return WorkspaceRuntime{}, err
	}
	if completeErr := attempt.complete(ctx, resource.ProviderRequestID, resource, nil); completeErr != nil {
		return WorkspaceRuntime{}, completeErr
	}
	return resource, nil
}

func (p *LocalDockerProvider) RepairWorkspaceRuntime(ctx context.Context, input WorkspaceRuntimeInput, compute ComputeAllocation, volume StorageVolume) (WorkspaceRuntime, error) {
	name := localRuntimeName(input.WorkspaceID)
	container, exists, err := p.inspectContainer(ctx, name)
	if err != nil {
		return WorkspaceRuntime{}, err
	}
	if exists {
		current, readErr := p.runtimeFromContainer(container)
		if readErr != nil {
			return WorkspaceRuntime{}, readErr
		}
		if current.OperationID == input.RuntimeOperationID && current.ImageID == input.ImageID {
			return p.WorkspaceRuntimeStatus(ctx, input.WorkspaceID)
		}
		labels := container.Config.Labels
		previousOperationMatches := current.OperationID == input.PreviousRuntimeOperationID || failedRepairRuntimeOperationForLaunch(current.OperationID, input.PreviousRuntimeOperationID)
		if !previousOperationMatches || current.WorkspaceID != input.WorkspaceID || current.ID != localRuntimeID(input.WorkspaceID) ||
			labels["opl.account.id"] != compute.AccountID || labels["opl.compute.id"] != input.ComputeID || labels["opl.storage.id"] != input.VolumeID ||
			labels["opl.attachment.id"] != input.AttachmentID || labels["opl.attachment.operation.id"] != input.AttachmentOperationID ||
			labels["opl.secret.ref"] != input.GatewaySecretRef || (current.OperationID != input.PreviousRuntimeOperationID && labels["opl.image.ref"] != input.ImageID) {
			return WorkspaceRuntime{}, fmt.Errorf("local_docker_runtime_repair_ownership_mismatch")
		}
		if _, err := p.runner.Run(ctx, nil, "container", "rm", "-f", name); err != nil {
			return WorkspaceRuntime{}, err
		}
	}
	return p.CreateWorkspaceRuntime(ctx, input, compute, volume)
}

func failedRepairRuntimeOperationForLaunch(candidateOperationID, previousRuntimeOperationID string) bool {
	marker := ":runtime-repair:"
	launchID, _, found := strings.Cut(candidateOperationID, marker)
	return found && launchID != "" && previousRuntimeOperationID == launchID+":runtime"
}

func ensureLocalDockerCodexHome(dataPath string) error {
	path := filepath.Join(dataPath, ".codex")
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return ErrLaunchStageBindingConflict
	}
	return nil
}

func (p *LocalDockerProvider) runtimeFromContainer(container dockerContainerInspect) (WorkspaceRuntime, error) {
	labels := container.Config.Labels
	workspaceID, runtimeID := labels["opl.workspace.id"], labels["opl.resource.id"]
	if workspaceID == "" || runtimeID == "" || labels["opl.operation.id"] == "" || labels["opl.image.ref"] == "" {
		return WorkspaceRuntime{}, fmt.Errorf("local_docker_runtime_identity_mismatch")
	}
	bindings := container.NetworkSettings.Ports["3000/tcp"]
	url := ""
	if len(bindings) == 1 && bindings[0].HostPort != "" {
		host := firstNonEmpty(bindings[0].HostIP, p.runtimeHost)
		if host == "0.0.0.0" || host == "::" {
			host = p.runtimeHost
		}
		url = "http://" + net.JoinHostPort(host, bindings[0].HostPort) + "/"
	}
	ready := container.State.Running && url != "" && validRuntimeHealthcheck(container) &&
		container.State.Health != nil && container.State.Health.Status == "healthy"
	status := "unready"
	if ready {
		status = "running"
	}
	return WorkspaceRuntime{
		ID: runtimeID, OperationID: labels["opl.operation.id"], WorkspaceID: workspaceID, URL: url, Status: status,
		ServiceName: container.Name, ImageID: labels["opl.image.ref"], ProviderRequestID: providerRequestID("docker-runtime-read", runtimeID), Ready: ready,
		Checks: []Check{{Name: "docker_container_running", OK: container.State.Running}, {Name: "runtime_port_published", OK: url != ""}}, CreatedAt: p.now(),
	}, nil
}

func (p *LocalDockerProvider) WorkspaceRuntimeStatus(ctx context.Context, workspaceID string) (WorkspaceRuntime, error) {
	var result WorkspaceRuntime
	absent := false
	err := p.withStorageQuotaLock(ctx, func() error {
		container, exists, inspectErr := p.inspectContainer(ctx, localRuntimeName(workspaceID))
		if inspectErr != nil {
			return inspectErr
		}
		if !exists {
			absent = true
			return nil
		}
		root, rootErr := p.openStorageRoot()
		if rootErr != nil {
			return rootErr
		}
		reservation, reconcileErr := reconcileLocalDockerRuntimeReservation(root, container)
		closeErr := root.Close()
		if reconcileErr != nil || closeErr != nil {
			if reconcileErr != nil && reconcileErr.Error() == localDockerRuntimeReservationInventoryError {
				return fmt.Errorf("local_docker_runtime_readback_mismatch")
			}
			return firstNonNil(reconcileErr, closeErr)
		}
		limits, ok := runtimeCgroupLimitsFromReservation(reservation)
		if !ok {
			return fmt.Errorf("local_docker_runtime_readback_mismatch")
		}
		var statusErr error
		result, statusErr = p.workspaceRuntimeStatusWithLimits(ctx, workspaceID, limits)
		return statusErr
	})
	if err != nil {
		return WorkspaceRuntime{}, err
	}
	if absent {
		return WorkspaceRuntime{WorkspaceID: workspaceID}, ErrWorkspaceLaunchResourceAbsent
	}
	return result, nil
}

func runtimeCgroupLimitsFromReservation(reservation localDockerRuntimeReservation) (localDockerRuntimeCgroupLimits, bool) {
	const maxInt64 = uint64(1<<63 - 1)
	if !validLocalDockerRuntimeReservation(reservation) || reservation.NanoCPUs > maxInt64 || reservation.MemoryBytes > maxInt64 {
		return localDockerRuntimeCgroupLimits{}, false
	}
	return localDockerRuntimeCgroupLimits{
		NanoCPUs: int64(reservation.NanoCPUs), Memory: int64(reservation.MemoryBytes), PackageID: reservation.PackageID,
	}, true
}

func (p *LocalDockerProvider) workspaceRuntimeStatusWithPlan(ctx context.Context, workspaceID string, plan ComputePlan) (WorkspaceRuntime, error) {
	limits, limitsOK := p.runtimeCgroupLimitsForPlan("", plan)
	if !limitsOK {
		return WorkspaceRuntime{}, fmt.Errorf("local_docker_runtime_readback_mismatch")
	}
	return p.workspaceRuntimeStatusWithLimits(ctx, workspaceID, limits)
}

func (p *LocalDockerProvider) workspaceRuntimeStatusWithLimits(ctx context.Context, workspaceID string, limits localDockerRuntimeCgroupLimits) (WorkspaceRuntime, error) {
	container, exists, err := p.inspectContainer(ctx, localRuntimeName(workspaceID))
	if err != nil {
		return WorkspaceRuntime{}, err
	}
	if !exists {
		return WorkspaceRuntime{WorkspaceID: workspaceID}, ErrWorkspaceLaunchResourceAbsent
	}
	if !validRuntimeCgroupLimits(container, limits) {
		return WorkspaceRuntime{}, fmt.Errorf("local_docker_runtime_readback_mismatch")
	}
	secretRef := gatewaySecretName(workspaceID)
	metadata, err := p.gatewayMetadata(ctx, secretRef)
	if err != nil {
		return WorkspaceRuntime{}, err
	}
	if _, err := p.gatewaySecretVersionHostPath(secretRef, metadata); err != nil {
		return WorkspaceRuntime{}, err
	}
	if metadata.WorkspaceID != workspaceID || metadata.SecretRef != secretRef {
		return WorkspaceRuntime{}, ErrLaunchStageBindingConflict
	}
	storageSizeGB, parseErr := strconv.Atoi(container.Config.Labels[localDockerStorageSizeGBLabel])
	if parseErr != nil || storageSizeGB <= 0 {
		return WorkspaceRuntime{}, fmt.Errorf("local_docker_runtime_storage_binding_mismatch")
	}
	storage, err := p.ReadStorageVolume(ctx, StorageVolume{
		ID: container.Config.Labels["opl.storage.id"], AccountID: container.Config.Labels["opl.account.id"], WorkspaceID: workspaceID,
		SizeGB: storageSizeGB,
	})
	if err != nil {
		return WorkspaceRuntime{}, err
	}
	paths, err := p.readStorageDirectories(storage)
	if err != nil || !validRuntimeWorkspaceMountViews(container, paths) {
		return WorkspaceRuntime{}, firstNonNil(err, fmt.Errorf("local_docker_runtime_storage_binding_mismatch"))
	}
	if !validRuntimeHealthcheck(container) {
		return WorkspaceRuntime{}, fmt.Errorf("local_docker_runtime_healthcheck_binding_mismatch")
	}
	if err := p.verifyRuntimeGatewaySecret(ctx, container, metadata); err != nil {
		return WorkspaceRuntime{}, err
	}
	if err := p.verifyRuntimeGatewayNetwork(ctx, container); err != nil {
		return WorkspaceRuntime{}, err
	}
	runtime, err := p.runtimeFromContainer(container)
	if err != nil {
		return WorkspaceRuntime{}, err
	}
	runtime.Access, err = localDockerRuntimeAccess(metadata)
	if err != nil {
		return WorkspaceRuntime{}, err
	}
	return runtime, nil
}

func (*LocalDockerProvider) WorkspaceRuntimeProviderFacts(runtime WorkspaceRuntime) ProviderResourceFacts {
	return ProviderResourceFacts{ProviderID: runtime.ServiceName, Status: runtime.Status}
}

func (p *LocalDockerProvider) DestroyWorkspaceRuntime(ctx context.Context, workspaceID string) (WorkspaceRuntime, error) {
	var result WorkspaceRuntime
	err := p.withStorageQuotaLock(ctx, func() error {
		var lockedErr error
		result, lockedErr = p.destroyWorkspaceRuntimeLocked(ctx, workspaceID)
		return lockedErr
	})
	return result, err
}

func (p *LocalDockerProvider) destroyWorkspaceRuntimeLocked(ctx context.Context, workspaceID string) (WorkspaceRuntime, error) {
	name := localRuntimeName(workspaceID)
	container, exists, err := p.inspectContainer(ctx, name)
	if err != nil {
		return WorkspaceRuntime{}, err
	}
	result := WorkspaceRuntime{ID: localRuntimeID(workspaceID), WorkspaceID: workspaceID, ServiceName: name, ProviderRequestID: providerRequestID("docker-runtime-destroy", workspaceID)}
	if exists {
		current, readErr := p.runtimeFromContainer(container)
		if readErr != nil || current.WorkspaceID != workspaceID || current.ID != localRuntimeID(workspaceID) || current.ServiceName != name ||
			container.Config.Labels["opl.fabric.provider"] != "local-docker" || container.Config.Labels["opl.fabric.kind"] != "runtime" ||
			container.Config.Labels["opl.account.id"] == "" {
			return result, fmt.Errorf("local_docker_runtime_destroy_ownership_mismatch")
		}
		result = current
	}
	secretRef := gatewaySecretName(workspaceID)
	_, metadata, secretErr := p.readGatewaySecretFiles(secretRef)
	if secretErr != nil && !errors.Is(secretErr, ErrWorkspaceLaunchResourceAbsent) {
		return result, secretErr
	}
	if secretErr == nil && (metadata.WorkspaceID != workspaceID || metadata.SecretRef != secretRef) {
		return result, fmt.Errorf("local_docker_secret_destroy_ownership_mismatch")
	}
	if exists {
		if secretErr != nil || metadata.AccountID != container.Config.Labels["opl.account.id"] {
			return result, fmt.Errorf("local_docker_runtime_destroy_ownership_mismatch")
		}
		if err := p.verifyRuntimeGatewaySecret(ctx, container, metadata); err != nil {
			return result, fmt.Errorf("local_docker_runtime_destroy_ownership_mismatch")
		}
		if _, err := p.runner.Run(ctx, nil, "container", "rm", "-f", name); err != nil {
			return result, err
		}
	}
	if _, stillExists, inspectErr := p.inspectContainer(ctx, name); inspectErr != nil || stillExists {
		return result, firstNonNil(inspectErr, fmt.Errorf("local_docker_runtime_destroy_readback_mismatch"))
	}
	root, rootErr := p.openStorageRoot()
	if rootErr != nil {
		return result, rootErr
	}
	reservation, reservationErr := readLocalDockerRuntimeReservation(root, localDockerRuntimeReservationName(localRuntimeID(workspaceID)))
	if reservationErr == nil {
		if reservation.WorkspaceID != workspaceID || reservation.ResourceID != localRuntimeID(workspaceID) {
			root.Close()
			return result, fmt.Errorf("local_docker_runtime_destroy_ownership_mismatch")
		}
		reservationErr = removeLocalDockerRuntimeReservation(root, reservation.ResourceID)
	} else if !errors.Is(reservationErr, ErrWorkspaceLaunchResourceAbsent) {
		root.Close()
		return result, reservationErr
	}
	if errors.Is(reservationErr, ErrWorkspaceLaunchResourceAbsent) {
		reservationErr = nil
	}
	closeErr := root.Close()
	if reservationErr != nil || closeErr != nil {
		return result, firstNonNil(reservationErr, closeErr)
	}
	if err := p.RemoveGatewaySecret(ctx, workspaceID); err != nil {
		return result, err
	}
	result.Status, result.Ready = "destroyed", false
	return result, nil
}

func (p *LocalDockerProvider) BindWorkspaceRuntimeGatewaySecret(ctx context.Context, input WorkspaceRuntimeGatewaySecretInput) (WorkspaceRuntimeGatewaySecretBinding, error) {
	metadata, err := p.gatewayMetadata(ctx, input.SecretRef)
	if err != nil {
		return WorkspaceRuntimeGatewaySecretBinding{}, err
	}
	if _, pathErr := p.gatewaySecretVersionHostPath(input.SecretRef, metadata); pathErr != nil {
		return WorkspaceRuntimeGatewaySecretBinding{}, pathErr
	}
	container, exists, err := p.inspectContainer(ctx, localRuntimeName(input.WorkspaceID))
	if err != nil || !exists || metadata.WorkspaceID != input.WorkspaceID || metadata.WorkspaceAPIKeyID != input.WorkspaceAPIKeyID || metadata.Fingerprint != input.Fingerprint {
		return WorkspaceRuntimeGatewaySecretBinding{}, fmt.Errorf("local_docker_runtime_secret_binding_mismatch")
	}
	if err := p.verifyRuntimeGatewaySecret(ctx, container, metadata); err != nil {
		return WorkspaceRuntimeGatewaySecretBinding{}, fmt.Errorf("local_docker_runtime_secret_binding_mismatch")
	}
	return WorkspaceRuntimeGatewaySecretBinding{
		WorkspaceID: input.WorkspaceID, WorkspaceAPIKeyID: input.WorkspaceAPIKeyID, SecretRef: input.SecretRef,
		Fingerprint: input.Fingerprint, Bound: true,
	}, nil
}

func (p *LocalDockerProvider) WorkspaceRuntimeGatewaySecret(ctx context.Context, workspaceID string) (WorkspaceRuntimeGatewaySecretBinding, error) {
	secretRef := gatewaySecretName(workspaceID)
	metadata, err := p.gatewayMetadata(ctx, secretRef)
	if err != nil {
		return WorkspaceRuntimeGatewaySecretBinding{}, err
	}
	if _, pathErr := p.gatewaySecretVersionHostPath(secretRef, metadata); pathErr != nil {
		return WorkspaceRuntimeGatewaySecretBinding{}, pathErr
	}
	container, exists, err := p.inspectContainer(ctx, localRuntimeName(workspaceID))
	if err != nil {
		return WorkspaceRuntimeGatewaySecretBinding{}, err
	}
	bound := false
	if exists {
		if metadata.WorkspaceID != workspaceID || metadata.SecretRef != secretRef {
			return WorkspaceRuntimeGatewaySecretBinding{}, ErrLaunchStageBindingConflict
		}
		if err := p.verifyRuntimeGatewaySecret(ctx, container, metadata); err != nil {
			return WorkspaceRuntimeGatewaySecretBinding{}, err
		}
		bound = true
	}
	return WorkspaceRuntimeGatewaySecretBinding{
		WorkspaceID: workspaceID, WorkspaceAPIKeyID: metadata.WorkspaceAPIKeyID, SecretRef: secretRef,
		Fingerprint: metadata.Fingerprint, Bound: bound,
	}, nil
}

func (p *LocalDockerProvider) RuntimeHealthSummary(ctx context.Context) (RuntimeHealthSummary, error) {
	output, err := p.runner.Run(ctx, nil, "container", "ls", "-a", "--filter", "label=opl.fabric.kind=runtime", "--format", "{{.Names}}")
	if err != nil {
		return RuntimeHealthSummary{}, err
	}
	result := RuntimeHealthSummary{}
	for _, name := range strings.Fields(string(output)) {
		container, exists, inspectErr := p.inspectContainer(ctx, name)
		if inspectErr != nil || !exists {
			return RuntimeHealthSummary{}, firstNonNil(inspectErr, fmt.Errorf("local_docker_runtime_readback_invalid"))
		}
		result.Total++
		if container.State.Running {
			result.Ready++
		} else {
			result.Unready++
		}
	}
	return result, nil
}
