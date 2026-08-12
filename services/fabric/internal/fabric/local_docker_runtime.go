package fabric

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"strings"
)

const (
	localDockerGatewayKeyFile  = "opl_gateway_api_key"
	localDockerGatewayMetaFile = "opl_gateway_metadata.json"
)

type localDockerGatewayMetadata struct {
	AccountID         string `json:"accountId"`
	WorkspaceID       string `json:"workspaceId"`
	WorkspaceAPIKeyID int64  `json:"workspaceApiKeyId"`
	SecretRef         string `json:"secretRef"`
	Fingerprint       string `json:"fingerprint"`
	Version           string `json:"version"`
}

func secretArchive(files map[string][]byte) ([]byte, error) {
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, name := range []string{localDockerGatewayKeyFile, localDockerGatewayMetaFile} {
		body, ok := files[name]
		if !ok {
			continue
		}
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(body))}); err != nil {
			return nil, err
		}
		if _, err := writer.Write(body); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func (p *LocalDockerProvider) writeGatewaySecret(ctx context.Context, secretRef string, key []byte, metadata localDockerGatewayMetadata) error {
	meta, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	archive, err := secretArchive(map[string][]byte{localDockerGatewayKeyFile: key, localDockerGatewayMetaFile: meta})
	if err != nil {
		return err
	}
	_, err = p.runner.Run(ctx, archive, "run", "--rm", "-i", "--mount", "type=volume,source="+secretRef+",target=/run/opl-secrets", p.helperImage, "tar", "-x", "-C", "/run/opl-secrets")
	return err
}

func (p *LocalDockerProvider) readGatewaySecretFile(ctx context.Context, secretRef, name string) ([]byte, error) {
	if name != localDockerGatewayKeyFile && name != localDockerGatewayMetaFile {
		return nil, fmt.Errorf("local_docker_secret_file_invalid")
	}
	return p.runner.Run(ctx, nil, "run", "--rm", "--mount", "type=volume,source="+secretRef+",target=/run/opl-secrets,readonly", p.helperImage, "cat", "/run/opl-secrets/"+name)
}

func (p *LocalDockerProvider) gatewayMetadata(ctx context.Context, secretRef string) (localDockerGatewayMetadata, error) {
	body, err := p.readGatewaySecretFile(ctx, secretRef, localDockerGatewayMetaFile)
	if err != nil {
		return localDockerGatewayMetadata{}, err
	}
	var metadata localDockerGatewayMetadata
	if json.Unmarshal(body, &metadata) != nil || metadata.SecretRef != secretRef || metadata.WorkspaceID == "" || metadata.WorkspaceAPIKeyID <= 0 {
		return localDockerGatewayMetadata{}, fmt.Errorf("local_docker_secret_metadata_invalid")
	}
	return metadata, nil
}

func (p *LocalDockerProvider) UpsertGatewaySecret(ctx context.Context, input GatewaySecretInput) (GatewaySecret, error) {
	secretRef := gatewaySecretName(input.WorkspaceID)
	labels := localDockerLabels(input.AccountID, input.WorkspaceID, secretRef, input.IdempotencyKey, "secret")
	volumeAttempt, err := beginProviderMutation(ctx, "local_docker_secret_volume_create", "gateway_secret", secretRef, secretRef)
	if err != nil {
		return GatewaySecret{}, err
	}
	readback, exists, err := p.inspectVolume(ctx, secretRef)
	if err != nil {
		return GatewaySecret{}, err
	}
	if !exists {
		if volumeAttempt != nil && !volumeAttempt.Fresh {
			err := fmt.Errorf("local_docker_secret_volume_readback_pending")
			_ = volumeAttempt.complete(ctx, "", GatewaySecret{SecretRef: secretRef}, err)
			return GatewaySecret{}, err
		}
		args := append([]string{"volume", "create"}, dockerLabelArgs(labels)...)
		args = append(args, secretRef)
		if _, err := p.runner.Run(ctx, nil, args...); err != nil {
			_ = volumeAttempt.complete(ctx, "", GatewaySecret{SecretRef: secretRef}, err)
			return GatewaySecret{}, err
		}
		readback, exists, err = p.inspectVolume(ctx, secretRef)
	}
	if err != nil || !exists || !exactDockerLabels(readback.Labels, labels) {
		readErr := fmt.Errorf("local_docker_secret_readback_mismatch")
		_ = volumeAttempt.complete(ctx, "", GatewaySecret{SecretRef: secretRef}, readErr)
		return GatewaySecret{}, readErr
	}
	if completeErr := volumeAttempt.complete(ctx, providerRequestID("docker-secret-volume", secretRef), GatewaySecret{SecretRef: secretRef}, nil); completeErr != nil {
		return GatewaySecret{}, completeErr
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(input.GatewayAPIKey)))
	metadata := localDockerGatewayMetadata{
		AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, WorkspaceAPIKeyID: input.WorkspaceAPIKeyID,
		SecretRef: secretRef, Fingerprint: "sha256:" + digest, Version: digest[:16],
	}
	writeAttempt, beginErr := beginProviderMutation(ctx, "local_docker_secret_write", "gateway_secret", secretRef, metadata.Version)
	if beginErr != nil {
		return GatewaySecret{}, beginErr
	}
	if writeAttempt == nil || writeAttempt.Fresh {
		if err := p.writeGatewaySecret(ctx, secretRef, []byte(input.GatewayAPIKey), metadata); err != nil {
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
	metadata, err := p.gatewayMetadata(ctx, input.SecretRef)
	if err != nil {
		return GatewaySecret{}, err
	}
	key, err := p.readGatewaySecretFile(ctx, input.SecretRef, localDockerGatewayKeyFile)
	if err != nil {
		return GatewaySecret{}, err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(key))
	if metadata.AccountID != input.AccountID || metadata.WorkspaceID != input.WorkspaceID || metadata.WorkspaceAPIKeyID != input.WorkspaceAPIKeyID ||
		metadata.Fingerprint != input.Fingerprint || digest != input.KeyDigest || metadata.Version != digest[:16] {
		return GatewaySecret{}, fmt.Errorf("local_docker_secret_identity_mismatch")
	}
	return GatewaySecret{SecretRef: input.SecretRef, Version: metadata.Version, Fingerprint: metadata.Fingerprint}, nil
}

func (p *LocalDockerProvider) RemoveGatewaySecret(ctx context.Context, workspaceID string) error {
	secretRef := gatewaySecretName(workspaceID)
	if _, exists, err := p.inspectVolume(ctx, secretRef); err != nil {
		return err
	} else if exists {
		_, err = p.runner.Run(ctx, nil, "volume", "rm", secretRef)
		return err
	}
	return nil
}

type dockerContainerInspect struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Image  string `json:"Image"`
	Config struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
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
	} `json:"NetworkSettings"`
	Mounts []struct {
		Type        string `json:"Type"`
		Name        string `json:"Name"`
		Destination string `json:"Destination"`
	} `json:"Mounts"`
}

func (p *LocalDockerProvider) inspectContainer(ctx context.Context, name string) (dockerContainerInspect, bool, error) {
	output, err := p.runner.Run(ctx, nil, "container", "inspect", name)
	if dockerMissing(err) {
		return dockerContainerInspect{}, false, nil
	}
	if err != nil {
		return dockerContainerInspect{}, false, err
	}
	var values []dockerContainerInspect
	if json.Unmarshal(output, &values) != nil || len(values) != 1 || values[0].ID == "" {
		return dockerContainerInspect{}, false, fmt.Errorf("local_docker_runtime_readback_invalid")
	}
	values[0].Name = strings.TrimPrefix(values[0].Name, "/")
	return values[0], true, nil
}

func runtimeMountPresent(container dockerContainerInspect, name, destination string) bool {
	for _, mount := range container.Mounts {
		if mount.Type == "volume" && mount.Name == name && mount.Destination == destination {
			return true
		}
	}
	return false
}

func localRuntimeID(workspaceID string) string {
	return "rt_" + stableSuffix("local-docker", workspaceID)[:18]
}

func localRuntimeName(workspaceID string) string { return localDockerName("opl-runtime", workspaceID) }

func (p *LocalDockerProvider) CreateWorkspaceRuntime(ctx context.Context, input WorkspaceRuntimeInput, compute ComputeAllocation, volume StorageVolume) (WorkspaceRuntime, error) {
	computeReadback, err := p.ReadComputeAllocation(ctx, compute)
	if err != nil {
		return WorkspaceRuntime{}, err
	}
	volumeReadback, err := p.ReadStorageVolume(ctx, volume)
	if err != nil {
		return WorkspaceRuntime{}, err
	}
	if _, err := p.gatewayMetadata(ctx, input.GatewaySecretRef); err != nil {
		return WorkspaceRuntime{}, err
	}
	name, runtimeID := localRuntimeName(input.WorkspaceID), localRuntimeID(input.WorkspaceID)
	labels := localDockerLabels(compute.AccountID, input.WorkspaceID, runtimeID, input.RuntimeOperationID, "runtime")
	for key, value := range map[string]string{
		"opl.compute.id": input.ComputeID, "opl.storage.id": input.VolumeID, "opl.attachment.id": input.AttachmentID,
		"opl.attachment.operation.id": input.AttachmentOperationID, "opl.secret.ref": input.GatewaySecretRef, "opl.image.ref": input.ImageID,
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
			err := fmt.Errorf("local_docker_runtime_mutation_readback_pending")
			_ = attempt.complete(ctx, "", WorkspaceRuntime{ID: runtimeID, WorkspaceID: input.WorkspaceID}, err)
			return WorkspaceRuntime{}, err
		}
		volumeName := localDockerName("opl-storage", volumeReadback.ID)
		args := append([]string{"run", "-d", "--name", name}, dockerLabelArgs(labels)...)
		args = append(args,
			"--network", localDockerName("opl-compute", computeReadback.ID),
			"--mount", "type=volume,source="+volumeName+",target=/data",
			"--mount", "type=volume,source="+input.GatewaySecretRef+",target=/run/secrets,readonly",
			"-p", p.runtimeHost+"::3000",
			"-e", "OPL_WEBUI_DEPLOYMENT_MODE=cloud", "-e", "OPL_GATEWAY_API_KEY_FILE=/run/secrets/"+localDockerGatewayKeyFile,
			"-e", "OPL_WORKSPACE_ID="+input.WorkspaceID, "-e", "OPL_COMPUTE_ALLOCATION_ID="+input.ComputeID,
			"-e", "OPL_OWNER_ACCOUNT_ID="+compute.AccountID, "-e", "DATA_DIR=/data", "-e", "AIONUI_DATA_DIR=/data",
			"-e", "OPL_PROJECTS_DIR=/data/projects", input.ImageID,
		)
		if _, err := p.runner.Run(ctx, nil, args...); err != nil {
			_ = attempt.complete(ctx, "", WorkspaceRuntime{ID: runtimeID, WorkspaceID: input.WorkspaceID}, err)
			return WorkspaceRuntime{}, err
		}
		container, exists, inspectErr = p.inspectContainer(ctx, name)
	}
	if inspectErr != nil || !exists || !exactDockerLabels(container.Config.Labels, labels) ||
		!runtimeMountPresent(container, strings.TrimPrefix(volumeReadback.ProviderResourceID, "volume/"), "/data") ||
		!runtimeMountPresent(container, input.GatewaySecretRef, "/run/secrets") {
		readErr := fmt.Errorf("local_docker_runtime_readback_mismatch")
		_ = attempt.complete(ctx, "", WorkspaceRuntime{ID: runtimeID, WorkspaceID: input.WorkspaceID}, readErr)
		return WorkspaceRuntime{}, readErr
	}
	resource, err := p.runtimeFromContainer(container)
	if err != nil {
		return WorkspaceRuntime{}, err
	}
	if completeErr := attempt.complete(ctx, resource.ProviderRequestID, resource, nil); completeErr != nil {
		return WorkspaceRuntime{}, completeErr
	}
	return resource, nil
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
	ready := container.State.Running && url != "" && (container.State.Health == nil || container.State.Health.Status == "healthy")
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
	container, exists, err := p.inspectContainer(ctx, localRuntimeName(workspaceID))
	if err != nil {
		return WorkspaceRuntime{}, err
	}
	if !exists {
		return WorkspaceRuntime{WorkspaceID: workspaceID, Status: "not_found"}, fmt.Errorf("local_docker_runtime_not_found")
	}
	return p.runtimeFromContainer(container)
}

func (*LocalDockerProvider) WorkspaceRuntimeProviderFacts(runtime WorkspaceRuntime) ProviderResourceFacts {
	return ProviderResourceFacts{ProviderID: runtime.ServiceName, Status: runtime.Status}
}

func (p *LocalDockerProvider) DestroyWorkspaceRuntime(ctx context.Context, workspaceID string) (WorkspaceRuntime, error) {
	name := localRuntimeName(workspaceID)
	container, exists, err := p.inspectContainer(ctx, name)
	if err != nil {
		return WorkspaceRuntime{}, err
	}
	result := WorkspaceRuntime{ID: localRuntimeID(workspaceID), WorkspaceID: workspaceID, ServiceName: name, Status: "destroyed", ProviderRequestID: providerRequestID("docker-runtime-destroy", workspaceID)}
	if exists {
		if current, readErr := p.runtimeFromContainer(container); readErr == nil {
			result = current
			result.Status, result.Ready = "destroyed", false
		}
		if _, err := p.runner.Run(ctx, nil, "container", "rm", "-f", name); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (p *LocalDockerProvider) BindWorkspaceRuntimeGatewaySecret(ctx context.Context, input WorkspaceRuntimeGatewaySecretInput) (WorkspaceRuntimeGatewaySecretBinding, error) {
	metadata, err := p.gatewayMetadata(ctx, input.SecretRef)
	if err != nil {
		return WorkspaceRuntimeGatewaySecretBinding{}, err
	}
	container, exists, err := p.inspectContainer(ctx, localRuntimeName(input.WorkspaceID))
	if err != nil || !exists || metadata.WorkspaceAPIKeyID != input.WorkspaceAPIKeyID || metadata.Fingerprint != input.Fingerprint ||
		container.Config.Labels["opl.secret.ref"] != input.SecretRef || !runtimeMountPresent(container, input.SecretRef, "/run/secrets") {
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
	container, exists, err := p.inspectContainer(ctx, localRuntimeName(workspaceID))
	bound := err == nil && exists && container.Config.Labels["opl.secret.ref"] == secretRef && runtimeMountPresent(container, secretRef, "/run/secrets")
	if err != nil {
		return WorkspaceRuntimeGatewaySecretBinding{}, err
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
