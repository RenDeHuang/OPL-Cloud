package fabric

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const qualificationWorkspaceDockerfilePath = "../../../../deploy/portable/qualification-workspace.Dockerfile"

const localDockerTestWebUISeed = "local-docker-workspace-secret-2026"

var exactDockerfileImagePattern = regexp.MustCompile(`^[^[:space:]@]+@sha256:[0-9a-f]{64}$`)

func qualificationWorkspaceDockerfile(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(qualificationWorkspaceDockerfilePath)
	if err != nil {
		t.Fatalf("read qualification Workspace Dockerfile: %v", err)
	}
	fromCount := 0
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.EqualFold(fields[0], "FROM") {
			continue
		}
		fromCount++
		if len(fields) != 2 || !exactDockerfileImagePattern.MatchString(fields[1]) {
			t.Fatalf("qualification Workspace Dockerfile FROM must use exact repository@sha256: %q", line)
		}
	}
	if fromCount == 0 {
		t.Fatal("qualification Workspace Dockerfile has no FROM instruction")
	}
	return qualificationWorkspaceDockerfilePath
}

func TestQualificationWorkspaceDockerfileUsesExactImageReferences(t *testing.T) {
	qualificationWorkspaceDockerfile(t)
}

func localDockerSecretTestRoot(t *testing.T) string {
	t.Helper()
	t.Setenv("OPL_AIONUI_ADMIN_PASSWORD_SEED", localDockerTestWebUISeed)
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}

func localDockerSecretTreeDigest(t *testing.T, root string) string {
	t.Helper()
	hash := sha256.New()
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00", relative, info.Mode().String())
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(hash, "%s\x00", target)
		case info.Mode().IsRegular():
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			_, _ = hash.Write(body)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func waitForLocalRuntime(ctx context.Context, url string) error {
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		response, err := client.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 500 {
				return nil
			}
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return fmt.Errorf("local_docker_runtime_http_unavailable")
}

type bindingCheckingDockerRunner struct {
	base                       dockerRunner
	store                      OperationStore
	parents                    map[string]WorkspaceLaunchStageBinding
	mutationIDs                map[string]string
	runtimeCreateCalls         int
	gatewayConnectCalls        int
	gatewayDisconnectCalls     int
	networkRemoveCalls         int
	loseGatewayConnectResponse bool
	loseGatewayCleanupResponse bool
	t                          *testing.T
}

type recordingDockerRunner struct {
	calls [][]string
}

func (r *recordingDockerRunner) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	if len(args) >= 1 && args[0] == "info" {
		return []byte("test-docker-version"), nil
	}
	return nil, fmt.Errorf("unexpected docker call: %q", args)
}

type localDockerComputeRoundTripRunner struct {
	network       dockerNetworkInspect
	mutationCalls int
}

func (r *localDockerComputeRoundTripRunner) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	switch {
	case len(args) >= 1 && args[0] == "info":
		return []byte("test-docker-version"), nil
	case len(args) == 7 && args[0] == "network" && args[1] == "ls":
		if r.network.ID == "" {
			return nil, nil
		}
		return json.Marshal(dockerObjectInventoryRow{ID: r.network.ID, Name: r.network.Name})
	case len(args) >= 4 && args[0] == "network" && args[1] == "create":
		r.mutationCalls++
		r.network = dockerNetworkInspect{ID: "network-round-trip", Name: args[len(args)-1], Labels: map[string]string{}}
		for index := 0; index+1 < len(args); index++ {
			if args[index] != "--label" {
				continue
			}
			key, value, found := strings.Cut(args[index+1], "=")
			if found {
				r.network.Labels[key] = value
			}
		}
		return []byte(r.network.ID), nil
	case len(args) == 3 && args[0] == "network" && args[1] == "inspect" && r.network.ID != "":
		return json.Marshal([]dockerNetworkInspect{r.network})
	default:
		return nil, fmt.Errorf("unexpected docker call: %q", args)
	}
}

type localDockerRuntimeReplayRunner struct {
	calls                  [][]string
	network                dockerNetworkInspect
	volume                 dockerVolumeInspect
	container              dockerContainerInspect
	containerVisible       bool
	containerVisibleOnRead int
	containerReads         int
	runCalls               int
	secretArchive          []byte
}

func (r *localDockerRuntimeReplayRunner) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	switch {
	case len(args) == 8 && args[0] == "container" && args[1] == "ls" && args[5] == "label=opl.fabric.kind=runtime":
		if !r.containerVisible && !(r.containerVisibleOnRead > 0 && r.containerReads >= r.containerVisibleOnRead) {
			return nil, nil
		}
		return json.Marshal(dockerObjectInventoryRow{ID: r.container.ID, Names: strings.TrimPrefix(r.container.Name, "/")})
	case len(args) == 3 && args[0] == "info" && args[1] == "--format" && args[2] == "{{json .}}":
		return []byte(`{"NCPU":64,"MemTotal":274877906944}`), nil
	case len(args) == 7 && args[0] == "network" && args[1] == "ls":
		return json.Marshal(dockerObjectInventoryRow{ID: r.network.ID, Name: r.network.Name})
	case len(args) == 3 && args[0] == "network" && args[1] == "inspect":
		return json.Marshal([]dockerNetworkInspect{r.network})
	case len(args) == 6 && args[0] == "volume" && args[1] == "ls":
		return json.Marshal(dockerObjectInventoryRow{Name: r.volume.Name})
	case len(args) == 3 && args[0] == "volume" && args[1] == "inspect":
		return json.Marshal([]dockerVolumeInspect{r.volume})
	case len(args) == 8 && args[0] == "container" && args[1] == "ls":
		r.containerReads++
		visible := r.containerVisible || r.containerVisibleOnRead > 0 && r.containerReads >= r.containerVisibleOnRead
		if !visible {
			return nil, nil
		}
		return json.Marshal(dockerObjectInventoryRow{ID: r.container.ID, Names: strings.TrimPrefix(r.container.Name, "/")})
	case len(args) == 3 && args[0] == "container" && args[1] == "inspect":
		return json.Marshal([]dockerContainerInspect{r.container})
	case len(args) == 4 && args[0] == "container" && args[1] == "cp":
		return append([]byte(nil), r.secretArchive...), nil
	case len(args) > 0 && args[0] == "run":
		r.runCalls++
		r.containerVisible = true
		return []byte(r.container.ID), nil
	default:
		return nil, fmt.Errorf("unexpected docker call: %q", args)
	}
}

func localDockerReadyRuntimeContainer(t *testing.T, name, id, image string, labels map[string]string, storage localDockerStoragePaths, secretPath string) dockerContainerInspect {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"Id": id, "Name": "/" + name,
		"Config": map[string]any{"Image": image, "Labels": labels, "Healthcheck": map[string]any{
			"Test": []string{"CMD-SHELL", localDockerRuntimeHealthCommand}, "Interval": localDockerRuntimeHealthInterval,
			"Timeout": localDockerRuntimeHealthTimeout, "StartPeriod": localDockerRuntimeHealthStartPeriod, "Retries": localDockerRuntimeHealthRetries,
		}},
		"State":           map[string]any{"Status": "running", "Running": true, "Health": map[string]any{"Status": "healthy"}},
		"NetworkSettings": map[string]any{"Ports": map[string]any{"3000/tcp": []map[string]string{{"HostIp": "127.0.0.1", "HostPort": "30123"}}}},
		"HostConfig": map[string]any{"NanoCpus": int64(2_000_000_000), "Memory": int64(4_294_967_296), "MemorySwap": int64(4_294_967_296), "Mounts": []map[string]any{
			{"Type": "bind", "Source": storage.Data, "Target": "/data", "BindOptions": map[string]any{"Propagation": "rprivate"}},
			{"Type": "bind", "Source": storage.Projects, "Target": "/projects", "BindOptions": map[string]any{"Propagation": "rprivate"}},
			{"Type": "tmpfs", "Target": "/recovery", "TmpfsOptions": map[string]any{"Mode": 448}},
			{"Type": "bind", "Source": secretPath, "Target": "/run/secrets", "ReadOnly": true, "BindOptions": map[string]any{"Propagation": "rprivate"}},
		}},
		"Mounts": []map[string]any{
			{"Type": "bind", "Source": storage.Data, "Destination": "/data", "RW": true, "Propagation": "rprivate"},
			{"Type": "bind", "Source": storage.Projects, "Destination": "/projects", "RW": true, "Propagation": "rprivate"},
			{"Type": "tmpfs", "Destination": "/recovery", "RW": true},
			{"Type": "bind", "Source": secretPath, "Destination": "/run/secrets", "RW": false, "Propagation": "rprivate"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var container dockerContainerInspect
	if err := json.Unmarshal(body, &container); err != nil {
		t.Fatal(err)
	}
	return container
}

func localDockerRuntimeReplayFixture(t *testing.T, visibleOnRead int) (*LocalDockerProvider, *localDockerRuntimeReplayRunner, context.Context, OperationStore, WorkspaceRuntimeInput, ComputeAllocation, StorageVolume) {
	t.Helper()
	accountID, workspaceID := "acct-runtime", "ws-runtime"
	compute := ComputeAllocation{ID: "compute-runtime", OperationID: "compute-operation", AccountID: accountID, WorkspaceID: workspaceID, PackageID: "basic"}
	volume := StorageVolume{ID: "storage-runtime", OperationID: "storage-operation", AccountID: accountID, WorkspaceID: workspaceID, SizeGB: 10}
	input := WorkspaceRuntimeInput{
		WorkspaceID: workspaceID, ComputeID: compute.ID, VolumeID: volume.ID, AttachmentID: "attachment-runtime",
		AttachmentOperationID: "attachment-operation", RuntimeOperationID: "runtime-operation",
		ImageID: "ghcr.io/gaofeng21cn/one-person-lab-webui@sha256:" + strings.Repeat("a", 64), GatewaySecretRef: gatewaySecretName(workspaceID),
		IdempotencyKey: "runtime-idempotency", OperationID: "runtime-operation",
	}
	root := localDockerSecretTestRoot(t)
	key := []byte("runtime-gateway-key")
	digest := fmt.Sprintf("%x", sha256.Sum256(key))
	labels := localDockerLabels(accountID, workspaceID, localRuntimeID(workspaceID), input.RuntimeOperationID, "runtime")
	for key, value := range map[string]string{
		"opl.compute.id": input.ComputeID, "opl.storage.id": input.VolumeID, "opl.attachment.id": input.AttachmentID,
		"opl.attachment.operation.id": input.AttachmentOperationID, "opl.secret.ref": input.GatewaySecretRef, "opl.image.ref": input.ImageID,
		"opl.secret.version": digest[:16], "opl.secret.fingerprint": "sha256:" + digest,
		localDockerComputePackageLabel: compute.PackageID, localDockerStorageSizeGBLabel: strconv.Itoa(volume.SizeGB),
	} {
		labels[key] = value
	}
	secretPath := filepath.Join(root, input.GatewaySecretRef, localDockerGatewayVersionsDir, "sha256-"+digest)
	metadata := localDockerGatewayMetadata{
		AccountID: accountID, WorkspaceID: workspaceID, WorkspaceAPIKeyID: 7, SecretRef: input.GatewaySecretRef,
		Fingerprint: "sha256:" + digest, Version: digest[:16],
	}
	runner := &localDockerRuntimeReplayRunner{
		network:                dockerNetworkInspect{ID: "network-runtime", Name: localDockerName("opl-compute", compute.ID), Labels: localDockerLabels(accountID, workspaceID, compute.ID, compute.OperationID, "compute")},
		volume:                 dockerVolumeInspect{Name: localDockerName("opl-storage", volume.ID), Labels: localDockerLabels(accountID, workspaceID, volume.ID, volume.OperationID, "storage")},
		containerVisibleOnRead: visibleOnRead,
		secretArchive:          validRuntimeSecretArchive(t, key, metadata),
	}
	storageRoot := localDockerStorageTestRoot(t)
	provider := newLocalDockerProvider(LocalDockerProviderConfig{GatewaySecretRoot: root, HostStorageRoot: storageRoot, RuntimeHost: "127.0.0.1", StorageQuotaBackend: localDockerStorageTestQuota(storageRoot)}, runner)
	storagePaths, err := provider.ensureStorageDirectories(context.Background(), localDockerStorageMetadata{
		SchemaVersion: localDockerStorageMetadataSchemaVersion, StorageID: volume.ID, AccountID: accountID, WorkspaceID: workspaceID, SizeGB: volume.SizeGB,
	}, volume.SizeGB)
	if err != nil {
		t.Fatal(err)
	}
	runner.container = localDockerReadyRuntimeContainer(t, localRuntimeName(workspaceID), "container-runtime", input.ImageID, labels, storagePaths, secretPath)
	if err := provider.writeGatewaySecret(input.GatewaySecretRef, key, metadata); err != nil {
		t.Fatal(err)
	}
	binding := WorkspaceLaunchStageBinding{
		SchemaVersion: 1, LaunchOperationID: "launch-runtime", AccountID: accountID, WorkspaceID: workspaceID,
		Stage: "runtime", Action: "ensure_runtime", FabricOperationID: "launch-runtime:runtime", IdempotencyKey: input.IdempotencyKey,
	}
	store := NewMemoryOperationStore()
	now := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)
	ctx := context.WithValue(context.Background(), providerMutationJournalContextKey{}, &providerMutationJournal{
		operations: store, parent: binding, parentOperation: FabricOperation{ID: binding.FabricOperationID}, provider: "local-docker", now: func() time.Time { return now },
	})
	return provider, runner, ctx, store, input, compute, volume
}

type localDockerDestroyRunner struct {
	calls          [][]string
	containers     map[string]dockerContainerInspect
	volumes        map[string]dockerVolumeInspect
	archives       map[string][]byte
	containerRMErr map[string]error
	volumeRMErr    map[string]error
}

func (r *localDockerDestroyRunner) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	if len(args) < 2 {
		return nil, fmt.Errorf("unexpected docker call: %q", args)
	}
	switch {
	case len(args) == 8 && args[0] == "container" && args[1] == "ls":
		name := strings.TrimSuffix(strings.TrimPrefix(args[5], "name=^/"), "$")
		container, ok := r.containers[name]
		if !ok {
			return nil, nil
		}
		return json.Marshal(dockerObjectInventoryRow{ID: container.ID, Names: name})
	case len(args) == 3 && args[0] == "container" && args[1] == "inspect":
		var container dockerContainerInspect
		ok := false
		for _, candidate := range r.containers {
			if candidate.ID == args[2] {
				container, ok = candidate, true
				break
			}
		}
		if !ok {
			return nil, errors.New("container inventory drift")
		}
		return json.Marshal([]dockerContainerInspect{container})
	case len(args) == 4 && args[0] == "container" && args[1] == "cp":
		name := strings.TrimSuffix(args[2], ":"+localDockerRuntimeSecretMountPath+"/.")
		archive, ok := r.archives[name]
		if !ok {
			return nil, errors.New("runtime Secret archive unavailable")
		}
		return append([]byte(nil), archive...), nil
	case len(args) == 4 && args[0] == "container" && args[1] == "rm" && args[2] == "-f":
		name := args[3]
		if err := r.containerRMErr[name]; err != nil {
			return nil, err
		}
		delete(r.containers, name)
		return nil, nil
	case len(args) == 6 && args[0] == "volume" && args[1] == "ls":
		name := strings.TrimSuffix(strings.TrimPrefix(args[3], "name=^"), "$")
		if _, ok := r.volumes[name]; !ok {
			return nil, nil
		}
		return json.Marshal(dockerObjectInventoryRow{Name: name})
	case len(args) == 3 && args[0] == "volume" && args[1] == "inspect":
		name := args[2]
		volume, ok := r.volumes[name]
		if !ok {
			return nil, errors.New("no such volume")
		}
		return json.Marshal([]dockerVolumeInspect{volume})
	case len(args) == 3 && args[0] == "volume" && args[1] == "rm":
		name := args[2]
		if err := r.volumeRMErr[name]; err != nil {
			return nil, err
		}
		delete(r.volumes, name)
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected docker call: %q", args)
	}
}

func localDockerDestroyRuntimeContainer(workspaceID string) dockerContainerInspect {
	container := dockerContainerInspect{
		ID:   "container-" + stableSuffix(workspaceID)[:12],
		Name: localRuntimeName(workspaceID),
	}
	container.Config.Image = "ghcr.io/gaofeng21cn/one-person-lab-webui@sha256:" + strings.Repeat("a", 64)
	container.Config.Labels = map[string]string{
		"opl.fabric.provider": "local-docker",
		"opl.fabric.kind":     "runtime",
		"opl.account.id":      "acct-" + stableSuffix(workspaceID)[:8],
		"opl.workspace.id":    workspaceID,
		"opl.resource.id":     localRuntimeID(workspaceID),
		"opl.operation.id":    "runtime-op-" + stableSuffix(workspaceID)[:8],
		"opl.image.ref":       container.Config.Image,
	}
	return container
}

func bindLocalDockerDestroyRuntimeSecret(t *testing.T, container dockerContainerInspect, root, accountID, workspaceID string, key []byte) (dockerContainerInspect, []byte) {
	t.Helper()
	digest := fmt.Sprintf("%x", sha256.Sum256(key))
	metadata := localDockerGatewayMetadata{
		AccountID: accountID, WorkspaceID: workspaceID, WorkspaceAPIKeyID: 1, SecretRef: gatewaySecretName(workspaceID),
		Fingerprint: "sha256:" + digest, Version: digest[:16],
	}
	for key, value := range map[string]string{
		"opl.account.id": metadata.AccountID, "opl.workspace.id": metadata.WorkspaceID, "opl.secret.ref": metadata.SecretRef,
		"opl.secret.version": metadata.Version, "opl.secret.fingerprint": metadata.Fingerprint,
	} {
		container.Config.Labels[key] = value
	}
	secretPath := filepath.Join(root, metadata.SecretRef, localDockerGatewayVersionsDir, "sha256-"+digest)
	hostMount := dockerHostMount{Type: "bind", Source: secretPath, Target: localDockerRuntimeSecretMountPath, ReadOnly: true}
	hostMount.BindOptions.Propagation = "rprivate"
	container.HostConfig.Mounts = []dockerHostMount{hostMount}
	container.Mounts = []dockerRuntimeMount{{
		Type: "bind", Source: "/host_mnt" + secretPath, Destination: localDockerRuntimeSecretMountPath, RW: false, Propagation: "rprivate",
	}}
	return container, validRuntimeSecretArchive(t, key, metadata)
}

func localDockerDestroySecretVolume(workspaceID string) dockerVolumeInspect {
	secretRef := gatewaySecretName(workspaceID)
	return dockerVolumeInspect{
		Name: secretRef,
		Labels: map[string]string{
			"opl.fabric.provider": "local-docker",
			"opl.fabric.kind":     "secret",
			"opl.account.id":      "acct-" + stableSuffix(workspaceID)[:8],
			"opl.workspace.id":    workspaceID,
			"opl.resource.id":     secretRef,
			"opl.operation.id":    "secret-op-" + stableSuffix(workspaceID)[:8],
		},
	}
}

func localDockerDestroySecretVolumeWithLabels(workspaceID string, mutate func(map[string]string)) dockerVolumeInspect {
	volume := localDockerDestroySecretVolume(workspaceID)
	if mutate == nil {
		return volume
	}
	labels := make(map[string]string, len(volume.Labels))
	for key, value := range volume.Labels {
		labels[key] = value
	}
	mutate(labels)
	volume.Labels = labels
	return volume
}

func localDockerRemoveCalls(calls [][]string, resource string) []string {
	removed := make([]string, 0)
	for _, call := range calls {
		switch resource {
		case "container":
			if len(call) == 4 && call[0] == "container" && call[1] == "rm" && call[2] == "-f" {
				removed = append(removed, call[3])
			}
		case "volume":
			if len(call) == 3 && call[0] == "volume" && call[1] == "rm" {
				removed = append(removed, call[2])
			}
		}
	}
	return removed
}

func (r *bindingCheckingDockerRunner) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	stage, mutation := "", ""
	if len(args) >= 2 && args[0] == "network" && args[1] == "create" {
		stage, mutation = "ensure_compute_allocation", "local_docker_network_create"
	}
	if len(args) == 4 && args[0] == "network" && args[1] == "connect" {
		stage, mutation = "runtime", "local_docker_runtime_create"
		r.gatewayConnectCalls++
	}
	if len(args) == 4 && args[0] == "network" && args[1] == "disconnect" {
		r.gatewayDisconnectCalls++
	}
	if len(args) == 3 && args[0] == "network" && args[1] == "rm" {
		r.networkRemoveCalls++
	}
	if len(args) > 0 && args[0] == "run" {
		for _, value := range args {
			if value == "-d" {
				stage, mutation = "runtime", "local_docker_runtime_create"
				r.runtimeCreateCalls++
			}
			if value == "tar" {
				stage, mutation = "secret", "local_docker_secret_write"
			}
		}
	}
	if stage != "" {
		binding := r.parents[stage]
		parent, err := r.store.Get(ctx, binding.FabricOperationID)
		if err != nil || parent.Status != "started" {
			return nil, fmt.Errorf("local_docker_%s_mutation_before_parent_binding", stage)
		}
		persisted, ok := decodeLaunchStageBinding(parent)
		if !ok || persisted != binding {
			return nil, fmt.Errorf("local_docker_%s_parent_binding_mismatch", stage)
		}
		child, err := r.store.Get(ctx, r.mutationIDs[mutation])
		if err != nil || child.Status != "started" || child.Action != mutation {
			return nil, fmt.Errorf("local_docker_%s_mutation_before_child_binding", mutation)
		}
	}
	output, err := r.base.Run(ctx, stdin, args...)
	if err == nil && r.loseGatewayConnectResponse && len(args) == 4 && args[0] == "network" && args[1] == "connect" {
		return output, fmt.Errorf("local_docker_test_gateway_connect_response_lost")
	}
	if err == nil && r.loseGatewayCleanupResponse && len(args) >= 2 && args[0] == "network" && (args[1] == "disconnect" || args[1] == "rm") {
		return output, fmt.Errorf("local_docker_test_gateway_cleanup_response_lost")
	}
	if err != nil && r.t != nil {
		r.t.Logf("docker args=%q error=%v", args, err)
	}
	return output, err
}

func localLaunchBinding(launchID, accountID, workspaceID, stage, action, idempotencyKey string) WorkspaceLaunchStageBinding {
	return WorkspaceLaunchStageBinding{
		SchemaVersion: 1, LaunchOperationID: launchID, AccountID: accountID, WorkspaceID: workspaceID,
		Stage: stage, Action: action, FabricOperationID: launchID + ":" + stage, IdempotencyKey: idempotencyKey,
	}
}

func waitForWorkspaceStage(ctx context.Context, service *Service, input WorkspaceLaunchStageInput) (WorkspaceLaunchStageResult, error) {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		result, err := service.ReadWorkspaceLaunchStage(ctx, input)
		if err != nil {
			return result, err
		}
		if result.State == "ready" {
			return result, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return WorkspaceLaunchStageResult{}, fmt.Errorf("workspace_launch_stage_timeout")
}

func localDockerImageTrustPreflight(t *testing.T, image string) (WorkspaceLaunchPreflight, *recordingDockerRunner, []FabricOperation) {
	t.Helper()
	runner := &recordingDockerRunner{}
	store := NewMemoryOperationStore()
	storageRoot := localDockerStorageTestRoot(t)
	service := NewServiceWithOperationStore(newLocalDockerProvider(LocalDockerProviderConfig{
		GatewaySecretRoot: localDockerSecretTestRoot(t), HostStorageRoot: storageRoot, StorageQuotaBackend: localDockerStorageTestQuota(storageRoot),
	}, runner), store)
	result, err := service.PreflightWorkspaceLaunch(context.Background(), WorkspaceLaunchPreflightInput{
		SchemaVersion: 1, LaunchOperationID: "local-image-trust", AccountID: "acct-local", WorkspaceID: "ws-local",
		PackageID: "basic", SizeGB: 10, WorkspaceImageDigest: image, RequestHash: strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatalf("preflight error=%v", err)
	}
	operations, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("list operations: %v", err)
	}
	return result, runner, operations
}

func TestLocalDockerRejectsUnapprovedWorkspaceImageBeforeDockerOrOperationMutation(t *testing.T) {
	for _, image := range []string{
		"ghcr.io/gaofeng21cn/one-person-lab-webui-attacker@sha256:" + strings.Repeat("a", 64),
		"GHCR.IO/gaofeng21cn/one-person-lab-app@sha256:" + strings.Repeat("a", 64),
		"ghcr.io/gaofeng21cn/one-person-lab-webui:stable@sha256:" + strings.Repeat("a", 64),
		"ghcr.io/gaofeng21cn/one-person-lab-webui@other@sha256:" + strings.Repeat("a", 64),
		"sha256:" + strings.Repeat("a", 64),
	} {
		t.Run(stableSuffix(image)[:12], func(t *testing.T) {
			preflight, runner, operations := localDockerImageTrustPreflight(t, image)
			if preflight.Available || preflight.Reason != "provider_profile_unavailable" {
				t.Fatalf("unapproved image preflight=%#v", preflight)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("unapproved image reached Docker: %#v", runner.calls)
			}
			if len(operations) != 0 {
				t.Fatalf("unapproved image mutated operation store: %#v", operations)
			}
		})
	}
}

func TestLocalDockerExactReleaseManifestImageDoesNotTrustSiblingDigest(t *testing.T) {
	approved := "registry.example.com/opl/one-person-lab-app@sha256:" + strings.Repeat("a", 64)
	provider := newLocalDockerProvider(LocalDockerProviderConfig{TrustedWorkspaceImageSources: []string{approved}}, &recordingDockerRunner{})
	if !provider.ValidateWorkspaceImageReference(approved) {
		t.Fatal("approved release manifest image rejected")
	}
	if provider.ValidateWorkspaceImageReference("registry.example.com/opl/one-person-lab-app@sha256:" + strings.Repeat("b", 64)) {
		t.Fatal("release manifest allowlist trusted an unlisted digest")
	}
}

func TestLocalDockerConfiguredReleaseManifestReplacesDefaultRepositoryTrust(t *testing.T) {
	approved := "registry.example.com/opl/one-person-lab-app@sha256:" + strings.Repeat("a", 64)
	t.Setenv("OPL_FABRIC_LOCAL_DOCKER_TRUSTED_WORKSPACE_IMAGES", approved)
	provider := NewLocalDockerProvider()
	if !provider.ValidateWorkspaceImageReference(approved) {
		t.Fatal("configured release manifest image rejected")
	}
	if provider.ValidateWorkspaceImageReference("ghcr.io/gaofeng21cn/one-person-lab-webui@sha256:" + strings.Repeat("b", 64)) {
		t.Fatal("configured release manifest retained default repository trust")
	}
}

func TestLocalDockerAcceptsApprovedImmutableWorkspaceImage(t *testing.T) {
	image := "ghcr.io/gaofeng21cn/one-person-lab-webui@sha256:" + strings.Repeat("a", 64)
	preflight, runner, operations := localDockerImageTrustPreflight(t, image)
	if !preflight.Available || preflight.Reason != "none" || preflight.ProviderBindingRef == "" {
		t.Fatalf("approved image preflight=%#v", preflight)
	}
	if len(runner.calls) != 1 || len(runner.calls[0]) == 0 || runner.calls[0][0] != "info" {
		t.Fatalf("approved image Docker calls=%#v", runner.calls)
	}
	if len(operations) != 1 || operations[0].ID != preflight.ProviderBindingRef || operations[0].Status != "succeeded" {
		t.Fatalf("approved image operations=%#v", operations)
	}
}

func seedLocalDockerGatewaySecret(t *testing.T, provider *LocalDockerProvider, accountID, workspaceID string) string {
	t.Helper()
	key := []byte("key-" + workspaceID)
	digest := fmt.Sprintf("%x", sha256.Sum256(key))
	secretRef := gatewaySecretName(workspaceID)
	if err := provider.writeGatewaySecret(secretRef, key, localDockerGatewayMetadata{
		AccountID: accountID, WorkspaceID: workspaceID, WorkspaceAPIKeyID: 1, SecretRef: secretRef,
		Fingerprint: "sha256:" + digest, Version: digest[:16],
	}); err != nil {
		t.Fatal(err)
	}
	return secretRef
}

func TestLocalDockerGatewaySecretLifecycleMaterializesExactModesUnderRestrictiveUmask(t *testing.T) {
	const childEnv = "OPL_TEST_LOCAL_DOCKER_SECRET_RESTRICTIVE_UMASK"
	if os.Getenv(childEnv) != "1" {
		command := exec.Command("sh", "-c", `umask 077; exec "$1" -test.run '^TestLocalDockerGatewaySecretLifecycleMaterializesExactModesUnderRestrictiveUmask$'`, "sh", os.Args[0])
		command.Env = append(os.Environ(), childEnv+"=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("restrictive umask child failed: %v\n%s", err, output)
		}
		return
	}

	root := localDockerSecretTestRoot(t)
	provider := newLocalDockerProvider(LocalDockerProviderConfig{GatewaySecretRoot: root}, &recordingDockerRunner{})
	accountID, workspaceID := "acct-umask", "ws-umask"
	secretRef := seedLocalDockerGatewaySecret(t, provider, accountID, workspaceID)
	key := []byte("key-" + workspaceID)
	digest := fmt.Sprintf("%x", sha256.Sum256(key))
	metadata := localDockerGatewayMetadata{
		AccountID: accountID, WorkspaceID: workspaceID, WorkspaceAPIKeyID: 1, SecretRef: secretRef,
		Fingerprint: "sha256:" + digest, Version: digest[:16],
	}
	assertMode := func(path string, want os.FileMode) {
		t.Helper()
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("stat path=%s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("path=%s mode=%v want=%v", path, got, want)
		}
	}
	versionPath := filepath.Join(root, secretRef, localDockerGatewayVersionsDir, "sha256-"+digest)
	assertMode(filepath.Join(root, secretRef), 0711)
	assertMode(filepath.Join(root, secretRef, localDockerGatewayVersionsDir), 0711)
	assertMode(versionPath, 0711)
	assertMode(filepath.Join(versionPath, localDockerGatewayKeyFile), 0444)
	assertMode(filepath.Join(versionPath, localDockerGatewayMetaFile), 0400)
	assertMode(filepath.Join(versionPath, localDockerWebUIPasswordFile), 0400)
	assertMode(filepath.Join(versionPath, localDockerWebUISessionSecretFile), 0400)
	wantPassword := deriveAionUIAdminPassword(localDockerTestWebUISeed, workspaceID, metadata.Version)
	wantSessionSecret := deriveWebUISessionSecret(localDockerTestWebUISeed, workspaceID, metadata.Version)
	if password, err := os.ReadFile(filepath.Join(versionPath, localDockerWebUIPasswordFile)); err != nil || string(password) != wantPassword {
		t.Fatalf("webui password=%q err=%v", password, err)
	}
	if sessionSecret, err := os.ReadFile(filepath.Join(versionPath, localDockerWebUISessionSecretFile)); err != nil || string(sessionSecret) != wantSessionSecret {
		t.Fatalf("webui session secret=%q err=%v", sessionSecret, err)
	}
	if current, err := os.Readlink(filepath.Join(root, secretRef, "current")); err != nil || current != localDockerGatewayVersionsDir+"/sha256-"+digest {
		t.Fatalf("current=%q err=%v", current, err)
	}
	if _, err := provider.ReadGatewaySecretByDigest(context.Background(), GatewaySecretReadbackInput{
		AccountID: accountID, WorkspaceID: workspaceID, WorkspaceAPIKeyID: 1,
		SecretRef: secretRef, Fingerprint: metadata.Fingerprint, KeyDigest: digest,
	}); err != nil {
		t.Fatalf("authoritative read: %v", err)
	}
	if err := provider.writeGatewaySecret(secretRef, key, metadata); err != nil {
		t.Fatalf("same Secret replay: %v", err)
	}

	rotatedKey := []byte("rotated-" + workspaceID)
	rotatedDigest := fmt.Sprintf("%x", sha256.Sum256(rotatedKey))
	rotatedMetadata := localDockerGatewayMetadata{
		AccountID: accountID, WorkspaceID: workspaceID, WorkspaceAPIKeyID: 2, SecretRef: secretRef,
		Fingerprint: "sha256:" + rotatedDigest, Version: rotatedDigest[:16],
	}
	if err := provider.writeGatewaySecret(secretRef, rotatedKey, rotatedMetadata); err != nil {
		t.Fatalf("rotate Secret: %v", err)
	}
	rotatedVersionPath := filepath.Join(root, secretRef, localDockerGatewayVersionsDir, "sha256-"+rotatedDigest)
	assertMode(rotatedVersionPath, 0711)
	assertMode(filepath.Join(rotatedVersionPath, localDockerGatewayKeyFile), 0444)
	assertMode(filepath.Join(rotatedVersionPath, localDockerGatewayMetaFile), 0400)
	assertMode(filepath.Join(rotatedVersionPath, localDockerWebUIPasswordFile), 0400)
	assertMode(filepath.Join(rotatedVersionPath, localDockerWebUISessionSecretFile), 0400)
	if password, err := os.ReadFile(filepath.Join(rotatedVersionPath, localDockerWebUIPasswordFile)); err != nil || string(password) != deriveAionUIAdminPassword(localDockerTestWebUISeed, workspaceID, rotatedMetadata.Version) {
		t.Fatalf("rotated webui password=%q err=%v", password, err)
	}
	if sessionSecret, err := os.ReadFile(filepath.Join(rotatedVersionPath, localDockerWebUISessionSecretFile)); err != nil || string(sessionSecret) != deriveWebUISessionSecret(localDockerTestWebUISeed, workspaceID, rotatedMetadata.Version) {
		t.Fatalf("rotated webui session secret=%q err=%v", sessionSecret, err)
	}
	if _, err := provider.ReadGatewaySecretByDigest(context.Background(), GatewaySecretReadbackInput{
		AccountID: accountID, WorkspaceID: workspaceID, WorkspaceAPIKeyID: 2,
		SecretRef: secretRef, Fingerprint: rotatedMetadata.Fingerprint, KeyDigest: rotatedDigest,
	}); err != nil {
		t.Fatalf("rotated authoritative read: %v", err)
	}
	t.Setenv("OPL_AIONUI_ADMIN_PASSWORD_SEED", "foreign-local-docker-seed")
	if _, err := provider.ReadGatewaySecretByDigest(context.Background(), GatewaySecretReadbackInput{
		AccountID: accountID, WorkspaceID: workspaceID, WorkspaceAPIKeyID: 2,
		SecretRef: secretRef, Fingerprint: rotatedMetadata.Fingerprint, KeyDigest: rotatedDigest,
	}); !errors.Is(err, ErrLaunchStageBindingConflict) {
		t.Fatalf("WebUI credential seed drift read err=%v", err)
	}
	t.Setenv("OPL_AIONUI_ADMIN_PASSWORD_SEED", localDockerTestWebUISeed)
	if err := provider.RemoveGatewaySecret(context.Background(), workspaceID); err != nil {
		t.Fatalf("remove Secret: %v", err)
	}
	if _, _, err := provider.readGatewaySecretFiles(secretRef); !errors.Is(err, ErrWorkspaceLaunchResourceAbsent) {
		t.Fatalf("removed Secret readback err=%v", err)
	}
}

func TestLocalDockerGatewaySecretFailsClosedWithoutWebUISeed(t *testing.T) {
	root := localDockerSecretTestRoot(t)
	t.Setenv("OPL_AIONUI_ADMIN_PASSWORD_SEED", "")
	provider := newLocalDockerProvider(LocalDockerProviderConfig{GatewaySecretRoot: root}, &recordingDockerRunner{})
	key := []byte("key-ws-missing-seed")
	digest := fmt.Sprintf("%x", sha256.Sum256(key))
	err := provider.writeGatewaySecret("opl-gateway-ws-missing-seed", key, localDockerGatewayMetadata{
		AccountID: "acct-missing-seed", WorkspaceID: "ws-missing-seed", WorkspaceAPIKeyID: 1,
		SecretRef: "opl-gateway-ws-missing-seed", Fingerprint: "sha256:" + digest, Version: digest[:16],
	})
	if err == nil || !strings.Contains(err.Error(), "local_docker_webui_credential_seed_required") {
		t.Fatalf("missing WebUI seed err=%v", err)
	}
}

func TestLocalDockerDestroyWorkspaceRuntimeDeletesExactSecretAndPreservesSibling(t *testing.T) {
	workspaceID, otherWorkspaceID := "ws-alpha", "ws-beta"
	runtimeName := localRuntimeName(workspaceID)
	runner := &localDockerDestroyRunner{containers: map[string]dockerContainerInspect{
		runtimeName: localDockerDestroyRuntimeContainer(workspaceID), localRuntimeName(otherWorkspaceID): localDockerDestroyRuntimeContainer(otherWorkspaceID),
	}, volumes: map[string]dockerVolumeInspect{}, archives: map[string][]byte{}}
	root := localDockerSecretTestRoot(t)
	storageRoot := localDockerStorageTestRoot(t)
	provider := newLocalDockerProvider(LocalDockerProviderConfig{GatewaySecretRoot: root, HostStorageRoot: storageRoot, StorageQuotaBackend: localDockerStorageTestQuota(storageRoot)}, runner)
	secretRef := seedLocalDockerGatewaySecret(t, provider, "acct-alpha", workspaceID)
	otherSecretRef := seedLocalDockerGatewaySecret(t, provider, "acct-beta", otherWorkspaceID)
	runner.containers[runtimeName], runner.archives[runtimeName] = bindLocalDockerDestroyRuntimeSecret(
		t, runner.containers[runtimeName], root, "acct-alpha", workspaceID, []byte("key-"+workspaceID),
	)

	runtime, err := provider.DestroyWorkspaceRuntime(context.Background(), workspaceID)
	if err != nil || runtime.Status != "destroyed" {
		t.Fatalf("destroy runtime=%#v err=%v", runtime, err)
	}
	if _, err := os.Stat(filepath.Join(root, secretRef)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned secret path remains err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, otherSecretRef)); err != nil {
		t.Fatalf("sibling secret removed: %v", err)
	}
	if got := localDockerRemoveCalls(runner.calls, "container"); len(got) != 1 || got[0] != runtimeName {
		t.Fatalf("container remove calls=%#v", got)
	}
	if got := localDockerRemoveCalls(runner.calls, "volume"); len(got) != 0 {
		t.Fatalf("secret destroy reached Docker volume: %#v", got)
	}
	if _, err := provider.WorkspaceRuntimeStatus(context.Background(), workspaceID); !errors.Is(err, ErrWorkspaceLaunchResourceAbsent) {
		t.Fatalf("destroyed runtime readback err=%v", err)
	}
	if _, err := provider.WorkspaceRuntimeGatewaySecret(context.Background(), workspaceID); !errors.Is(err, ErrWorkspaceLaunchResourceAbsent) {
		t.Fatalf("destroyed Secret readback err=%v", err)
	}
}

func TestLocalDockerDestroyWorkspaceRuntimeSecretOnlyRemnantIsIdempotent(t *testing.T) {
	workspaceID := "ws-alpha"
	runner := &localDockerDestroyRunner{containers: map[string]dockerContainerInspect{}, volumes: map[string]dockerVolumeInspect{}}
	storageRoot := localDockerStorageTestRoot(t)
	provider := newLocalDockerProvider(LocalDockerProviderConfig{GatewaySecretRoot: localDockerSecretTestRoot(t), HostStorageRoot: storageRoot, StorageQuotaBackend: localDockerStorageTestQuota(storageRoot)}, runner)
	seedLocalDockerGatewaySecret(t, provider, "acct-alpha", workspaceID)
	for attempt := 0; attempt < 2; attempt++ {
		runtime, err := provider.DestroyWorkspaceRuntime(context.Background(), workspaceID)
		if err != nil || runtime.Status != "destroyed" {
			t.Fatalf("attempt=%d runtime=%#v err=%v", attempt, runtime, err)
		}
	}
	if got := localDockerRemoveCalls(runner.calls, "container"); len(got) != 0 {
		t.Fatalf("container remove calls=%#v", got)
	}
}

func TestLocalDockerWorkspaceRuntimeStatusReturnsTypedAbsence(t *testing.T) {
	provider := newLocalDockerProvider(
		LocalDockerProviderConfig{GatewaySecretRoot: localDockerSecretTestRoot(t), HostStorageRoot: localDockerStorageTestRoot(t)},
		&localDockerDestroyRunner{containers: map[string]dockerContainerInspect{}, volumes: map[string]dockerVolumeInspect{}},
	)

	runtime, err := provider.WorkspaceRuntimeStatus(context.Background(), "ws-absent")
	if !errors.Is(err, ErrWorkspaceLaunchResourceAbsent) || runtime.WorkspaceID != "ws-absent" || runtime.ID != "" || runtime.Status != "" {
		t.Fatalf("runtime=%#v err=%v", runtime, err)
	}
}

func TestLocalDockerWorkspaceRuntimeStatusHonorsDeadlineWhileMutationLockIsHeld(t *testing.T) {
	storageRoot := localDockerStorageTestRoot(t)
	provider := newLocalDockerProvider(
		LocalDockerProviderConfig{GatewaySecretRoot: localDockerSecretTestRoot(t), HostStorageRoot: storageRoot},
		&localDockerDestroyRunner{containers: map[string]dockerContainerInspect{}, volumes: map[string]dockerVolumeInspect{}},
	)
	lock, err := os.OpenFile(filepath.Join(storageRoot, localDockerStorageQuotaLockFile), os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	locked := true
	defer func() {
		if locked {
			_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, statusErr := provider.WorkspaceRuntimeStatus(ctx, "ws-creating")
		result <- statusErr
	}()
	select {
	case statusErr := <-result:
		if !errors.Is(statusErr, context.DeadlineExceeded) {
			t.Fatalf("status returned before the mutation lock was released: %v", statusErr)
		}
	case <-time.After(time.Second):
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		locked = false
		<-result
		t.Fatal("status ignored its context deadline while waiting for the mutation lock")
	}
}

func TestLocalDockerDestroyWorkspaceRuntimeFailsClosedOnSecretIdentityDrift(t *testing.T) {
	workspaceID := "ws-alpha"
	runner := &localDockerDestroyRunner{containers: map[string]dockerContainerInspect{}, volumes: map[string]dockerVolumeInspect{}}
	root := localDockerSecretTestRoot(t)
	provider := newLocalDockerProvider(LocalDockerProviderConfig{GatewaySecretRoot: root}, runner)
	secretRef := seedLocalDockerGatewaySecret(t, provider, "acct-alpha", workspaceID)
	current, err := os.Readlink(filepath.Join(root, secretRef, "current"))
	if err != nil {
		t.Fatal(err)
	}
	metaPath := filepath.Join(root, secretRef, current, localDockerGatewayMetaFile)
	if err := os.Chmod(metaPath, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metaPath, []byte(`{"accountId":"acct-foreign"}`), 0400); err != nil {
		t.Fatal(err)
	}
	runtime, err := provider.DestroyWorkspaceRuntime(context.Background(), workspaceID)
	if err == nil || runtime.Status == "destroyed" {
		t.Fatalf("destroy runtime=%#v err=%v", runtime, err)
	}
	if _, statErr := os.Stat(filepath.Join(root, secretRef)); statErr != nil {
		t.Fatalf("drifted secret was removed: %v", statErr)
	}
}

func TestLocalDockerComputeStageSurvivesPostgresJSONBRoundTrip(t *testing.T) {
	for _, schemaVersion := range []int{1, workspaceLaunchStageRecordSchemaVersion} {
		t.Run(fmt.Sprintf("record-v%d", schemaVersion), func(t *testing.T) {
			ctx := context.Background()
			runner := &localDockerComputeRoundTripRunner{}
			store := NewMemoryOperationStore()
			storageRoot := localDockerStorageTestRoot(t)
			provider := newLocalDockerProvider(LocalDockerProviderConfig{
				GatewaySecretRoot: localDockerSecretTestRoot(t), HostStorageRoot: storageRoot, StorageQuotaBackend: localDockerStorageTestQuota(storageRoot),
			}, runner)
			service := NewServiceWithOperationStore(provider, store)
			image := defaultLocalDockerWorkspaceImageRepository + "@sha256:" + strings.Repeat("a", 64)
			launchID, accountID, workspaceID := fmt.Sprintf("launch-jsonb-v%d", schemaVersion), "acct-jsonb", "ws-jsonb"
			launchHash := strings.Repeat("b", 64)
			preflight, err := service.PreflightWorkspaceLaunch(ctx, WorkspaceLaunchPreflightInput{
				SchemaVersion: 1, LaunchOperationID: launchID, AccountID: accountID, WorkspaceID: workspaceID,
				PackageID: "basic", SizeGB: 10, WorkspaceImageDigest: image, RequestHash: launchHash,
			})
			if err != nil || !preflight.Available {
				t.Fatalf("preflight=%#v err=%v", preflight, err)
			}
			input := WorkspaceLaunchStageInput{
				Binding:            localLaunchBinding(launchID, accountID, workspaceID, "ensure_compute_allocation", "ensure_compute_allocation", launchID+":ensure-compute-allocation"),
				ProviderProfileRef: "local-docker", ProviderBindingRef: preflight.ProviderBindingRef, SpecDigest: preflight.SpecDigest,
				PackageID: "basic", SizeGB: 10, WorkspaceImageDigest: image,
			}
			input.Binding.RequestHash = workspaceLaunchStageRequestHash(input, launchHash)

			result, err := service.EnsureWorkspaceLaunchStage(ctx, input)
			if err != nil || result.State != "ready" || runner.mutationCalls != 1 {
				t.Fatalf("ensure result=%#v mutations=%d err=%v", result, runner.mutationCalls, err)
			}
			childID := providerMutationOperationID(input.Binding, "local_docker_network_create", "compute_allocation", result.Resources.ComputeAllocationID, runner.network.Name)
			parent, parentErr := store.Get(ctx, input.Binding.FabricOperationID)
			child, childErr := store.Get(ctx, childID)
			if parentErr != nil || childErr != nil || parent.Status != "succeeded" || child.Status != "succeeded" {
				t.Fatalf("parent=%#v parentErr=%v child=%#v childErr=%v", parent, parentErr, child, childErr)
			}
			if schemaVersion == 1 {
				record, ok := decodeWorkspaceLaunchStageRecord(parent)
				if !ok {
					t.Fatal("new parent record did not decode before legacy conversion")
				}
				record.SchemaVersion = 1
				setWorkspaceLaunchStageRecord(&parent, record)
				store.mu.Lock()
				for index := range store.operation {
					if store.operation[index].ID == parent.ID {
						store.operation[index] = parent
					}
				}
				store.mu.Unlock()
			}

			store.mu.Lock()
			for index := range store.operation {
				body, marshalErr := json.Marshal(store.operation[index].RedactedProviderPayload)
				if marshalErr != nil {
					store.mu.Unlock()
					t.Fatal(marshalErr)
				}
				var normalized map[string]any
				if unmarshalErr := json.Unmarshal(body, &normalized); unmarshalErr != nil {
					store.mu.Unlock()
					t.Fatal(unmarshalErr)
				}
				store.operation[index].RedactedProviderPayload = normalized
			}
			store.mu.Unlock()

			readback, err := service.ReadWorkspaceLaunchStage(ctx, input)
			if err != nil || readback.State != "ready" || readback.Reason != "none" || runner.mutationCalls != 1 {
				t.Fatalf("authoritative readback=%#v mutations=%d parentStatus=%s childStatus=%s err=%v", readback, runner.mutationCalls, parent.Status, child.Status, err)
			}
		})
	}
}

func TestLocalDockerWorkspaceCorePath(t *testing.T) {
	if os.Getenv("OPL_FABRIC_LOCAL_DOCKER_INTEGRATION") != "1" {
		t.Skip("set OPL_FABRIC_LOCAL_DOCKER_INTEGRATION=1 to run against the local Docker daemon")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		t.Fatalf("docker daemon unavailable: %v: %s", err, output)
	}

	dockerfile := qualificationWorkspaceDockerfile(t)
	tag := "opl-fabric-local-test:" + stableSuffix(t.Name(), time.Now().String())[:12]
	build := exec.CommandContext(ctx, "docker", "build", "--quiet", "--file", dockerfile, "--tag", tag, filepath.Dir(dockerfile))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build local runtime image: %v: %s", err, output)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", "-f", tag).Run() })
	imageOutput, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", tag).Output()
	if err != nil {
		t.Fatal(err)
	}
	imageID := strings.TrimSpace(string(imageOutput))
	gatewayName := "opl-gateway-test-" + stableSuffix(t.Name(), time.Now().String())[:12]
	if output, err := exec.CommandContext(ctx, "docker", "run", "-d", "--name", gatewayName,
		"--label", "opl.fabric.local-docker.gateway=control-plane", imageID, "sleep", "300").CombinedOutput(); err != nil {
		t.Fatalf("start local gateway container: %v: %s", err, output)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "container", "rm", "-f", gatewayName).Run() })

	launchID := "local-launch-" + stableSuffix(time.Now().String())[:12]
	accountID, workspaceID := "acct-local", "ws-"+stableSuffix(launchID)[:10]
	bindings := map[string]WorkspaceLaunchStageBinding{
		"ensure_compute_allocation": localLaunchBinding(launchID, accountID, workspaceID, "ensure_compute_allocation", "ensure_compute_allocation", launchID+":ensure-compute-allocation"),
		"storage":                   localLaunchBinding(launchID, accountID, workspaceID, "storage", "ensure_storage", launchID+":storage"),
		"attachment":                localLaunchBinding(launchID, accountID, workspaceID, "attachment", "ensure_attachment", launchID+":attachment"),
		"secret":                    localLaunchBinding(launchID, accountID, workspaceID, "secret", "ensure_gateway_secret", launchID+":secret"),
		"runtime":                   localLaunchBinding(launchID, accountID, workspaceID, "runtime", "ensure_runtime", launchID+":runtime"),
	}
	computeID := "ca_" + stableSuffix("create_compute_allocation", bindings["ensure_compute_allocation"].IdempotencyKey)[:18]
	storageID := "vol_" + stableSuffix("create_storage_volume", bindings["storage"].IdempotencyKey)[:16]
	secretRef := gatewaySecretName(workspaceID)
	key := "local-key-" + stableSuffix(launchID)
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
	mutationIDs := map[string]string{}
	store := NewMemoryOperationStore()
	runner := &bindingCheckingDockerRunner{
		base: execDockerRunner{binary: "docker"}, store: store, parents: bindings, mutationIDs: mutationIDs,
		loseGatewayConnectResponse: true, loseGatewayCleanupResponse: true, t: t,
	}
	storageRoot := localDockerStorageTestRoot(t)
	hostCapacityReader := newLocalDockerProvider(LocalDockerProviderConfig{
		HostStorageRoot: storageRoot, StorageQuotaBackend: localDockerStorageTestQuota(storageRoot),
	}, runner)
	hostCapacity, err := hostCapacityReader.readDockerHostCapacity(ctx)
	if err != nil || hostCapacity.CPUs == 0 || hostCapacity.MemoryBytes < 1024*1024*1024 {
		t.Fatalf("local Docker host capacity=%#v err=%v", hostCapacity, err)
	}
	memoryGB := hostCapacity.MemoryBytes / (1024 * 1024 * 1024)
	providerProfileJSON := []byte(fmt.Sprintf(
		`{"schemaVersion":1,"packages":[{"id":"basic","name":"Integration Workspace","available":true,"compute":{"id":"integration-%dc%dg","server":"%dc%dg","cpu":%d,"memoryGb":%d,"diskGb":10,"instanceType":"local-integration-%dc%dg"},"storage":{"sizeGb":10,"quotaPolicy":"linux-project"}}]}`,
		hostCapacity.CPUs, memoryGB, hostCapacity.CPUs, memoryGB, hostCapacity.CPUs, memoryGB, hostCapacity.CPUs, memoryGB,
	))
	provider := newLocalDockerProvider(LocalDockerProviderConfig{
		GatewaySecretRoot: localDockerSecretTestRoot(t), HostStorageRoot: storageRoot, RuntimeHost: "127.0.0.1", RuntimeGatewayContainer: gatewayName,
		StorageQuotaBackend:          localDockerStorageTestQuota(storageRoot),
		TrustedWorkspaceImageSources: []string{imageID},
		ProviderProfileJSON:          providerProfileJSON,
	}, runner)
	service := NewServiceWithOperationStore(provider, store)
	cleanupA := &localDockerWorkspaceCleanup{
		owner: service, workspaceID: workspaceID, runtimeDestroyKey: launchID + ":cleanup-runtime",
		attachmentID: workspaceLaunchAttachmentID(bindings["attachment"]), storageID: storageID, computeID: computeID,
	}
	t.Cleanup(func() { cleanupA.run(t) })
	launchRequestHash := stableSuffix("workspace-launch", launchID, accountID, workspaceID, imageID)
	preflight, err := service.PreflightWorkspaceLaunch(ctx, WorkspaceLaunchPreflightInput{
		SchemaVersion: 1, LaunchOperationID: launchID, AccountID: accountID, WorkspaceID: workspaceID,
		PackageID: "basic", SizeGB: 10, WorkspaceImageDigest: imageID, RequestHash: launchRequestHash,
	})
	if err != nil || !preflight.Available {
		t.Fatalf("preflight=%#v err=%v", preflight, err)
	}
	base := WorkspaceLaunchStageInput{
		ProviderProfileRef: "local-docker", ProviderBindingRef: preflight.ProviderBindingRef, SpecDigest: preflight.SpecDigest, PackageID: "basic",
		SizeGB: 10, WorkspaceImageDigest: imageID,
	}
	bindInput := func(input *WorkspaceLaunchStageInput) {
		input.Binding.RequestHash = workspaceLaunchStageRequestHash(*input, launchRequestHash)
		bindings[input.Binding.Stage] = input.Binding
	}

	computeInput := base
	computeInput.Binding = bindings["ensure_compute_allocation"]
	bindInput(&computeInput)
	mutationIDs["local_docker_network_create"] = providerMutationOperationID(computeInput.Binding, "local_docker_network_create", "compute_allocation", computeID, localDockerName("opl-compute", computeID))
	if _, err := service.EnsureWorkspaceLaunchStage(ctx, computeInput); err != nil {
		t.Fatal(err)
	}
	compute, err := waitForWorkspaceStage(ctx, service, computeInput)
	if err != nil || compute.Resources.ComputeAllocationID != computeID || compute.Resources.ComputeBindingRef != computeInput.Binding.FabricOperationID {
		t.Fatalf("compute=%#v err=%v", compute, err)
	}
	cleanupA.computeReady = true

	storageInput := base
	storageInput.Binding, storageInput.Resources = bindings["storage"], compute.Resources
	bindInput(&storageInput)
	storage, err := service.EnsureWorkspaceLaunchStage(ctx, storageInput)
	if err != nil || storage.State != "ready" {
		t.Fatalf("storage=%#v err=%v", storage, err)
	}
	cleanupA.storageReady = true

	attachmentInput := base
	attachmentInput.Binding, attachmentInput.Resources = bindings["attachment"], storage.Resources
	bindInput(&attachmentInput)
	attachment, err := service.EnsureWorkspaceLaunchStage(ctx, attachmentInput)
	if err != nil || attachment.State != "ready" {
		t.Fatalf("attachment=%#v err=%v", attachment, err)
	}
	cleanupA.attachmentReady = true

	secretInput := base
	secretInput.Binding, secretInput.Resources = bindings["secret"], attachment.Resources
	secretInput.Resources.GatewaySecretFingerprint = "sha256:" + digest
	secretInput.GatewayCredential = &WorkspaceLaunchGatewayCredential{KeyID: 1, Value: key}
	bindInput(&secretInput)
	mutationIDs["local_docker_secret_write"] = providerMutationOperationID(secretInput.Binding, "local_docker_secret_write", "gateway_secret", secretRef, digest[:16])
	secret, err := service.EnsureWorkspaceLaunchStage(ctx, secretInput)
	if err != nil || secret.State != "ready" {
		t.Fatalf("secret=%#v err=%v", secret, err)
	}
	cleanupA.runtimeOrSecretReady = true
	runtimeInput := base
	runtimeInput.Binding, runtimeInput.Resources = bindings["runtime"], secret.Resources
	bindInput(&runtimeInput)
	mutationIDs["local_docker_runtime_create"] = providerMutationOperationID(runtimeInput.Binding, "local_docker_runtime_create", "workspace_runtime", localRuntimeID(workspaceID), localRuntimeName(workspaceID))
	runtime, err := service.EnsureWorkspaceLaunchStage(ctx, runtimeInput)
	if err != nil || runtime.State != "pending" || runtime.Reason != "operation_pending" || runtime.Binding != runtimeInput.Binding || runtime.Resources != runtimeInput.Resources {
		t.Fatalf("initial runtime=%#v err=%v", runtime, err)
	}
	runtime, err = waitForWorkspaceStage(ctx, service, runtimeInput)
	if err != nil || runtime.State != "ready" || runtime.Reason != "none" || runtime.Binding != runtimeInput.Binding || runtime.Resources.RuntimeURL == "" ||
		runner.runtimeCreateCalls != 1 || runner.gatewayConnectCalls != 1 {
		t.Fatalf("converged runtime=%#v err=%v createCalls=%d gatewayConnectCalls=%d", runtime, err, runner.runtimeCreateCalls, runner.gatewayConnectCalls)
	}
	if err := waitForLocalRuntime(ctx, runtime.Resources.RuntimeURL); err != nil {
		t.Fatal(err)
	}
	runtimeURL := "http://" + runtime.Resources.RuntimeServiceName + ":3000/"
	fetchRuntime := `fetch(process.argv[1]).then(async response => { if (!response.ok) process.exit(1); process.stdout.write(await response.text()) }).catch(() => process.exit(1))`
	opened, err := exec.CommandContext(ctx, "docker", "exec", gatewayName, "node", "-e", fetchRuntime, runtimeURL).CombinedOutput()
	if err != nil || !strings.Contains(string(opened), "OPL Workspace READY") {
		t.Fatalf("gateway runtime open: %v: %s", err, opened)
	}
	if output, err := exec.CommandContext(ctx, "docker", "restart", gatewayName).CombinedOutput(); err != nil {
		t.Fatalf("restart gateway container: %v: %s", err, output)
	}
	opened, err = exec.CommandContext(ctx, "docker", "exec", gatewayName, "node", "-e", fetchRuntime, runtimeURL).CombinedOutput()
	if err != nil || !strings.Contains(string(opened), "OPL Workspace READY") {
		t.Fatalf("gateway runtime open after restart: %v: %s", err, opened)
	}
	restartedProvider := newLocalDockerProvider(LocalDockerProviderConfig{
		GatewaySecretRoot: provider.gatewaySecretRoot, HostStorageRoot: storageRoot, RuntimeHost: "127.0.0.1", RuntimeGatewayContainer: gatewayName,
		StorageQuotaBackend:          localDockerStorageTestQuota(storageRoot),
		TrustedWorkspaceImageSources: []string{imageID},
		ProviderProfileJSON:          providerProfileJSON,
	}, runner)
	// The shared operation store represents Fabric's durable operation database. Runtime
	// reservation recovery below is proved independently from the host storage root.
	restartedService := NewServiceWithOperationStore(restartedProvider, store)
	cleanupA.owner = restartedService
	status, err := restartedService.WorkspaceRuntimeStatus(ctx, workspaceID)
	if err != nil || status.ID != runtime.Resources.RuntimeID || status.OperationID != runtimeInput.Binding.FabricOperationID || !status.Ready || status.Access.Password != "" {
		t.Fatalf("canonical runtime status=%#v err=%v", status, err)
	}
	observation := restartedService.ObserveWorkspaceRuntime(ctx, workspaceID)
	if observation.State != WorkspaceOwnerObservationReady || observation.Runtime == nil || observation.Runtime.ID != status.ID {
		t.Fatalf("canonical runtime observation=%#v", observation)
	}
	credentials, err := restartedService.WorkspaceRuntimeCredentials(ctx, accountID, workspaceID)
	if err != nil || credentials.ID != status.ID || credentials.OperationID != status.OperationID ||
		credentials.Access.Username != webuiUsername || credentials.Access.Password != deriveAionUIAdminPassword(localDockerTestWebUISeed, workspaceID, digest[:16]) ||
		credentials.Access.CredentialStatus != "configured" || credentials.Access.CredentialVersion != digest[:16] || credentials.Access.SecretRef != secretRef {
		t.Fatalf("canonical runtime credentials=%#v err=%v", credentials, err)
	}
	if _, err := restartedService.WorkspaceRuntimeCredentials(ctx, accountID+"-other", workspaceID); err == nil {
		t.Fatal("cross-account canonical runtime credentials succeeded")
	}
	facts, err := restartedService.ProviderFactsBatch(ctx, ProviderFactsBatchInput{Items: []ProviderFactInput{{
		AccountID: accountID, WorkspaceID: workspaceID, ResourceType: "runtime", ResourceID: status.ID,
	}}})
	if err != nil || len(facts.Items) != 1 || !facts.Items[0].Available || facts.Items[0].ResourceID != status.ID {
		t.Fatalf("canonical runtime provider facts=%#v err=%v", facts, err)
	}
	networkName := localDockerName("opl-compute", computeID)
	if output, err := exec.CommandContext(ctx, "docker", "network", "disconnect", networkName, gatewayName).CombinedOutput(); err != nil {
		t.Fatalf("create gateway membership drift: %v: %s", err, output)
	}
	if _, err := restartedService.WorkspaceRuntimeStatus(ctx, workspaceID); err == nil {
		t.Fatal("runtime status accepted missing gateway network membership")
	}
	if observation := restartedService.ObserveWorkspaceRuntime(ctx, workspaceID); observation.State == WorkspaceOwnerObservationReady {
		t.Fatalf("runtime observation accepted missing gateway network membership: %#v", observation)
	}
	if _, err := restartedService.WorkspaceRuntimeCredentials(ctx, accountID, workspaceID); err == nil {
		t.Fatal("runtime credentials accepted missing gateway network membership")
	}
	driftedFacts, err := restartedService.ProviderFactsBatch(ctx, ProviderFactsBatchInput{Items: []ProviderFactInput{{
		AccountID: accountID, WorkspaceID: workspaceID, ResourceType: "runtime", ResourceID: status.ID,
	}}})
	if err != nil || len(driftedFacts.Items) != 1 || driftedFacts.Items[0].Available {
		t.Fatalf("runtime facts accepted missing gateway network membership: %#v err=%v", driftedFacts, err)
	}
	if output, err := exec.CommandContext(ctx, "docker", "network", "connect", networkName, gatewayName).CombinedOutput(); err != nil {
		t.Fatalf("restore gateway membership after drift check: %v: %s", err, output)
	}
	if status, err := restartedService.WorkspaceRuntimeStatus(ctx, workspaceID); err != nil || !status.Ready {
		t.Fatalf("runtime status after exact membership restore=%#v err=%v", status, err)
	}
	if runner.runtimeCreateCalls != 1 {
		t.Fatalf("canonical readback repeated Docker create: %d", runner.runtimeCreateCalls)
	}
	if runner.gatewayConnectCalls != 1 {
		t.Fatalf("canonical readback repeated gateway network connect: %d", runner.gatewayConnectCalls)
	}
	attachmentFacts, err := restartedService.ProviderFactsBatch(ctx, ProviderFactsBatchInput{Items: []ProviderFactInput{{
		AccountID: accountID, WorkspaceID: workspaceID, ResourceType: "attachment", ResourceID: attachment.Resources.AttachmentID,
	}}})
	if err != nil || len(attachmentFacts.Items) != 1 || !attachmentFacts.Items[0].Available {
		t.Fatalf("canonical attachment facts after restart=%#v err=%v", attachmentFacts, err)
	}
	for action, operationID := range mutationIDs {
		operation, err := store.Get(ctx, operationID)
		if err != nil || operation.Status != "succeeded" || operation.Action != action {
			t.Fatalf("provider mutation %s=%#v err=%v", action, operation, err)
		}
	}
	storedVolume, ok := restartedService.GetStorageVolume(ctx, storage.Resources.StorageID)
	if !ok {
		t.Fatal("restarted service did not restore Workspace A storage")
	}
	storedCompute, ok := restartedService.GetComputeAllocation(ctx, computeID)
	if !ok {
		t.Fatal("restarted service did not restore Workspace A compute")
	}
	storageRootHandle, err := restartedProvider.openStorageRoot()
	if err != nil {
		t.Fatal(err)
	}
	reservation, reservationErr := readLocalDockerRuntimeReservation(storageRootHandle, localDockerRuntimeReservationName(runtime.Resources.RuntimeID))
	storageMetadata, storageMetadataErr := readLocalDockerStorageMetadata(storageRootHandle, localDockerStoragePaths{WorkspaceName: localDockerName("opl-workspace", workspaceID)})
	if closeErr := storageRootHandle.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	expectedNanoCPUs := hostCapacity.CPUs * uint64(localDockerNanoCPUsPerCPU)
	expectedMemoryBytes := memoryGB * 1024 * 1024 * 1024
	if hostCapacity.MemoryBytes-expectedMemoryBytes >= 1024*1024*1024 || expectedMemoryBytes <= hostCapacity.MemoryBytes/2 {
		t.Fatalf("bounded memory reservation=%d host=%d", expectedMemoryBytes, hostCapacity.MemoryBytes)
	}
	if reservationErr != nil || reservation.WorkspaceID != workspaceID || reservation.ResourceID != runtime.Resources.RuntimeID ||
		reservation.NanoCPUs != expectedNanoCPUs || reservation.MemoryBytes != expectedMemoryBytes {
		t.Fatalf("Workspace A durable reservation=%#v err=%v", reservation, reservationErr)
	}
	if storageMetadataErr != nil || storageMetadata.ProjectID == 0 || storageMetadata.StorageID != storedVolume.ID {
		t.Fatalf("Workspace A storage metadata=%#v err=%v", storageMetadata, storageMetadataErr)
	}
	workspaceStorageRoot := filepath.Join(storageRoot, localDockerName("opl-workspace", workspaceID))
	quotaBackend := localDockerStorageTestQuota(storageRoot).(*fakeLocalDockerProjectQuota)
	workspaceQuota, workspaceQuotaErr := quotaBackend.Read(workspaceStorageRoot)
	if workspaceQuotaErr != nil || workspaceQuota.ProjectID != storageMetadata.ProjectID || workspaceQuota.HardLimitBytes != 10*1024*1024*1024 {
		t.Fatalf("Workspace A quota owner=%#v err=%v", workspaceQuota, workspaceQuotaErr)
	}
	runtimeContainer, exists, err := restartedProvider.inspectContainer(ctx, localRuntimeName(workspaceID))
	if err != nil || !exists || runtimeContainer.HostConfig.NanoCPUs <= 0 || runtimeContainer.HostConfig.Memory <= 0 ||
		uint64(runtimeContainer.HostConfig.NanoCPUs) != expectedNanoCPUs || uint64(runtimeContainer.HostConfig.Memory) != expectedMemoryBytes ||
		runtimeContainer.HostConfig.MemorySwap != runtimeContainer.HostConfig.Memory {
		t.Fatalf("Workspace A Docker HostConfig=%#v exists=%t err=%v", runtimeContainer.HostConfig, exists, err)
	}
	network, exists, err := restartedProvider.inspectNetwork(ctx, networkName)
	if err != nil || !exists || network.ID == "" {
		t.Fatalf("Workspace A compute network after restart=%#v exists=%t err=%v", network, exists, err)
	}

	secondLaunchID := "local-launch-" + stableSuffix("capacity-reuse", time.Now().String())[:12]
	secondWorkspaceID := "ws-" + stableSuffix(secondLaunchID)[:10]
	secondRuntimeID := localRuntimeID(secondWorkspaceID)
	secondReservation := localDockerRuntimeReservation{
		SchemaVersion: localDockerRuntimeReservationSchemaVersion, WorkspaceID: secondWorkspaceID, ResourceID: secondRuntimeID,
		PackageID: "basic", NanoCPUs: expectedNanoCPUs, MemoryBytes: expectedMemoryBytes,
	}
	var retainedCapacityErr error
	if err := restartedProvider.withStorageQuotaLock(ctx, func() error {
		root, openErr := restartedProvider.openStorageRoot()
		if openErr != nil {
			return openErr
		}
		defer root.Close()
		retainedCapacityErr = restartedProvider.localDockerRuntimeCapacityAdmission(ctx, root, secondReservation)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if retainedCapacityErr == nil || retainedCapacityErr.Error() != "local_docker_runtime_capacity_insufficient" {
		t.Fatalf("retained Workspace A must block same-size Workspace B: %v", retainedCapacityErr)
	}

	destroyed, err := restartedProvider.DestroyWorkspaceRuntime(ctx, workspaceID)
	if err != nil || destroyed.Status != "destroyed" {
		t.Fatalf("destroy runtime=%#v err=%v", destroyed, err)
	}
	cleanupA.runtimeOrSecretReady = false
	if _, err := restartedProvider.WorkspaceRuntimeStatus(ctx, workspaceID); !errors.Is(err, ErrWorkspaceLaunchResourceAbsent) {
		t.Fatalf("destroyed runtime readback err=%v", err)
	}
	if _, err := restartedProvider.WorkspaceRuntimeGatewaySecret(ctx, workspaceID); !errors.Is(err, ErrWorkspaceLaunchResourceAbsent) {
		t.Fatalf("destroyed Secret readback err=%v", err)
	}
	if observation := restartedService.ObserveWorkspaceRuntime(ctx, workspaceID); observation.State != WorkspaceOwnerObservationAbsent {
		t.Fatalf("destroyed runtime observation=%#v", observation)
	}
	storageRootHandle, err = restartedProvider.openStorageRoot()
	if err != nil {
		t.Fatal(err)
	}
	_, reservationErr = readLocalDockerRuntimeReservation(storageRootHandle, localDockerRuntimeReservationName(runtime.Resources.RuntimeID))
	if closeErr := storageRootHandle.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if !errors.Is(reservationErr, ErrWorkspaceLaunchResourceAbsent) {
		t.Fatalf("Workspace A reservation survived Runtime deletion: %v", reservationErr)
	}

	detached, err := restartedService.DetachStorageAttachment(ctx, attachment.Resources.AttachmentID)
	if err != nil || detached.Status != "detached" {
		t.Fatalf("canonical attachment detach after restart=%#v err=%v", detached, err)
	}
	cleanupA.attachmentReady = false
	destroyedStorage, err := restartedService.DestroyStorageVolume(ctx, storedVolume.ID)
	if err != nil || destroyedStorage.Status != "destroyed" {
		t.Fatalf("destroy storage after restart=%#v err=%v", destroyedStorage, err)
	}
	cleanupA.storageReady = false
	storageAbsence, err := restartedProvider.ReadStorageVolumeStatus(ctx, storedVolume)
	if err != nil || storageAbsence.Status != "external_deleted" {
		t.Fatalf("destroyed storage absence=%#v err=%v", storageAbsence, err)
	}
	if _, err := os.Stat(workspaceStorageRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Workspace A storage directory survived deletion: %v", err)
	}
	// fakeLocalDockerProjectQuota is a macOS test backend. These assertions prove
	// exact quota owner/read/clear/reuse calls, not Linux kernel quota enforcement.
	quotaBackend.mu.Lock()
	_, quotaCleared := quotaBackend.cleared[storageMetadata.ProjectID]
	quotaClearCalls := quotaBackend.clearCalls
	quotaBackend.mu.Unlock()
	if !quotaCleared || quotaClearCalls != 1 {
		t.Fatalf("Workspace A project quota clear project=%d cleared=%t calls=%d", storageMetadata.ProjectID, quotaCleared, quotaClearCalls)
	}

	network, exists, err = restartedProvider.inspectNetwork(ctx, networkName)
	if err != nil || !exists {
		t.Fatalf("compute network before destroy=%#v exists=%t err=%v", network, exists, err)
	}
	if _, err := restartedProvider.DestroyComputeAllocation(ctx, storedCompute); err != nil {
		t.Fatalf("destroy compute allocation: %v", err)
	}
	cleanupA.computeReady = false
	gateway, exists, err := restartedProvider.inspectContainer(ctx, gatewayName)
	if err != nil || !exists {
		t.Fatalf("gateway after compute destroy=%#v exists=%t err=%v", gateway, exists, err)
	}
	if bound, bindingErr := exactContainerNetworkMembership(gateway, networkName, network.ID); bindingErr != nil || bound {
		t.Fatalf("gateway membership after compute destroy bound=%t err=%v", bound, bindingErr)
	}
	if _, exists, err := restartedProvider.inspectNetwork(ctx, networkName); err != nil || exists {
		t.Fatalf("compute network after destroy exists=%t err=%v", exists, err)
	}
	if runner.gatewayDisconnectCalls != 1 || runner.networkRemoveCalls != 1 {
		t.Fatalf("gateway cleanup calls disconnect=%d networkRemove=%d", runner.gatewayDisconnectCalls, runner.networkRemoveCalls)
	}

	secondBindings := map[string]WorkspaceLaunchStageBinding{
		"ensure_compute_allocation": localLaunchBinding(secondLaunchID, accountID, secondWorkspaceID, "ensure_compute_allocation", "ensure_compute_allocation", secondLaunchID+":ensure-compute-allocation"),
		"storage":                   localLaunchBinding(secondLaunchID, accountID, secondWorkspaceID, "storage", "ensure_storage", secondLaunchID+":storage"),
		"attachment":                localLaunchBinding(secondLaunchID, accountID, secondWorkspaceID, "attachment", "ensure_attachment", secondLaunchID+":attachment"),
		"secret":                    localLaunchBinding(secondLaunchID, accountID, secondWorkspaceID, "secret", "ensure_gateway_secret", secondLaunchID+":secret"),
		"runtime":                   localLaunchBinding(secondLaunchID, accountID, secondWorkspaceID, "runtime", "ensure_runtime", secondLaunchID+":runtime"),
	}
	secondMutationIDs := map[string]string{}
	runner.parents, runner.mutationIDs = secondBindings, secondMutationIDs
	secondComputeID := "ca_" + stableSuffix("create_compute_allocation", secondBindings["ensure_compute_allocation"].IdempotencyKey)[:18]
	secondStorageID := "vol_" + stableSuffix("create_storage_volume", secondBindings["storage"].IdempotencyKey)[:16]
	secondAttachmentID := workspaceLaunchAttachmentID(secondBindings["attachment"])
	secondSecretRef := gatewaySecretName(secondWorkspaceID)
	cleanupB := &localDockerWorkspaceCleanup{
		owner: restartedService, workspaceID: secondWorkspaceID, runtimeDestroyKey: secondLaunchID + ":cleanup-runtime",
		attachmentID: secondAttachmentID, storageID: secondStorageID, computeID: secondComputeID,
	}
	t.Cleanup(func() { cleanupB.run(t) })
	secondKey := "local-key-" + stableSuffix(secondLaunchID)
	secondDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(secondKey)))
	secondLaunchRequestHash := stableSuffix("workspace-launch", secondLaunchID, accountID, secondWorkspaceID, imageID)
	secondPreflight, err := restartedService.PreflightWorkspaceLaunch(ctx, WorkspaceLaunchPreflightInput{
		SchemaVersion: 1, LaunchOperationID: secondLaunchID, AccountID: accountID, WorkspaceID: secondWorkspaceID,
		PackageID: "basic", SizeGB: 10, WorkspaceImageDigest: imageID, RequestHash: secondLaunchRequestHash,
	})
	if err != nil || !secondPreflight.Available {
		t.Fatalf("Workspace B preflight=%#v err=%v", secondPreflight, err)
	}
	secondBase := WorkspaceLaunchStageInput{
		ProviderProfileRef: "local-docker", ProviderBindingRef: secondPreflight.ProviderBindingRef, SpecDigest: secondPreflight.SpecDigest, PackageID: "basic",
		SizeGB: 10, WorkspaceImageDigest: imageID,
	}
	bindSecond := func(input *WorkspaceLaunchStageInput) {
		input.Binding.RequestHash = workspaceLaunchStageRequestHash(*input, secondLaunchRequestHash)
		secondBindings[input.Binding.Stage] = input.Binding
	}

	secondComputeInput := secondBase
	secondComputeInput.Binding = secondBindings["ensure_compute_allocation"]
	bindSecond(&secondComputeInput)
	secondMutationIDs["local_docker_network_create"] = providerMutationOperationID(secondComputeInput.Binding, "local_docker_network_create", "compute_allocation", secondComputeID, localDockerName("opl-compute", secondComputeID))
	if _, err := restartedService.EnsureWorkspaceLaunchStage(ctx, secondComputeInput); err != nil {
		t.Fatal(err)
	}
	secondCompute, err := waitForWorkspaceStage(ctx, restartedService, secondComputeInput)
	if err != nil || secondCompute.Resources.ComputeAllocationID != secondComputeID {
		t.Fatalf("Workspace B compute=%#v err=%v", secondCompute, err)
	}
	cleanupB.computeReady = true

	secondStorageInput := secondBase
	secondStorageInput.Binding, secondStorageInput.Resources = secondBindings["storage"], secondCompute.Resources
	bindSecond(&secondStorageInput)
	secondStorage, err := restartedService.EnsureWorkspaceLaunchStage(ctx, secondStorageInput)
	if err != nil || secondStorage.State != "ready" || secondStorage.Resources.StorageID != secondStorageID {
		t.Fatalf("Workspace B storage=%#v err=%v", secondStorage, err)
	}
	cleanupB.storageReady = true

	secondAttachmentInput := secondBase
	secondAttachmentInput.Binding, secondAttachmentInput.Resources = secondBindings["attachment"], secondStorage.Resources
	bindSecond(&secondAttachmentInput)
	secondAttachment, err := restartedService.EnsureWorkspaceLaunchStage(ctx, secondAttachmentInput)
	if err != nil || secondAttachment.State != "ready" {
		t.Fatalf("Workspace B attachment=%#v err=%v", secondAttachment, err)
	}
	cleanupB.attachmentReady = true

	secondSecretInput := secondBase
	secondSecretInput.Binding, secondSecretInput.Resources = secondBindings["secret"], secondAttachment.Resources
	secondSecretInput.Resources.GatewaySecretFingerprint = "sha256:" + secondDigest
	secondSecretInput.GatewayCredential = &WorkspaceLaunchGatewayCredential{KeyID: 2, Value: secondKey}
	bindSecond(&secondSecretInput)
	secondMutationIDs["local_docker_secret_write"] = providerMutationOperationID(secondSecretInput.Binding, "local_docker_secret_write", "gateway_secret", secondSecretRef, secondDigest[:16])
	secondSecret, err := restartedService.EnsureWorkspaceLaunchStage(ctx, secondSecretInput)
	if err != nil || secondSecret.State != "ready" {
		t.Fatalf("Workspace B Secret=%#v err=%v", secondSecret, err)
	}
	cleanupB.runtimeOrSecretReady = true

	secondRuntimeInput := secondBase
	secondRuntimeInput.Binding, secondRuntimeInput.Resources = secondBindings["runtime"], secondSecret.Resources
	bindSecond(&secondRuntimeInput)
	secondMutationIDs["local_docker_runtime_create"] = providerMutationOperationID(secondRuntimeInput.Binding, "local_docker_runtime_create", "workspace_runtime", secondRuntimeID, localRuntimeName(secondWorkspaceID))
	if _, err := restartedService.EnsureWorkspaceLaunchStage(ctx, secondRuntimeInput); err != nil {
		t.Fatal(err)
	}
	secondRuntime, err := waitForWorkspaceStage(ctx, restartedService, secondRuntimeInput)
	if err != nil || secondRuntime.State != "ready" || secondRuntime.Resources.RuntimeID != secondRuntimeID || secondRuntime.Resources.RuntimeURL == "" {
		t.Fatalf("Workspace B runtime=%#v err=%v", secondRuntime, err)
	}
	if err := waitForLocalRuntime(ctx, secondRuntime.Resources.RuntimeURL); err != nil {
		t.Fatal(err)
	}
	if runner.runtimeCreateCalls != 2 || runner.gatewayConnectCalls != 2 {
		t.Fatalf("Workspace B create calls runtime=%d gatewayConnect=%d", runner.runtimeCreateCalls, runner.gatewayConnectCalls)
	}

	storageRootHandle, err = restartedProvider.openStorageRoot()
	if err != nil {
		t.Fatal(err)
	}
	secondPersistedReservation, secondReservationErr := readLocalDockerRuntimeReservation(storageRootHandle, localDockerRuntimeReservationName(secondRuntimeID))
	secondStorageMetadata, secondStorageMetadataErr := readLocalDockerStorageMetadata(storageRootHandle, localDockerStoragePaths{WorkspaceName: localDockerName("opl-workspace", secondWorkspaceID)})
	if closeErr := storageRootHandle.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	secondWorkspaceStorageRoot := filepath.Join(storageRoot, localDockerName("opl-workspace", secondWorkspaceID))
	secondQuota, secondQuotaErr := quotaBackend.Read(secondWorkspaceStorageRoot)
	if secondReservationErr != nil || secondPersistedReservation != secondReservation {
		t.Fatalf("Workspace B durable reservation=%#v expected=%#v err=%v", secondPersistedReservation, secondReservation, secondReservationErr)
	}
	if secondStorageMetadataErr != nil || secondStorageMetadata.StorageID != secondStorageID || secondStorageMetadata.ProjectID == 0 ||
		secondQuotaErr != nil || secondQuota.ProjectID != secondStorageMetadata.ProjectID || secondQuota.HardLimitBytes != 10*1024*1024*1024 {
		t.Fatalf("Workspace B storage metadata=%#v quota=%#v metadataErr=%v quotaErr=%v", secondStorageMetadata, secondQuota, secondStorageMetadataErr, secondQuotaErr)
	}
	if info, err := os.Stat(secondWorkspaceStorageRoot); err != nil || !info.IsDir() {
		t.Fatalf("Workspace B storage directory info=%#v err=%v", info, err)
	}
	secondRuntimeContainer, exists, err := restartedProvider.inspectContainer(ctx, localRuntimeName(secondWorkspaceID))
	if err != nil || !exists || secondRuntimeContainer.HostConfig.NanoCPUs <= 0 || secondRuntimeContainer.HostConfig.Memory <= 0 ||
		uint64(secondRuntimeContainer.HostConfig.NanoCPUs) != expectedNanoCPUs || uint64(secondRuntimeContainer.HostConfig.Memory) != expectedMemoryBytes ||
		secondRuntimeContainer.HostConfig.MemorySwap != secondRuntimeContainer.HostConfig.Memory {
		t.Fatalf("Workspace B Docker HostConfig=%#v exists=%t err=%v", secondRuntimeContainer.HostConfig, exists, err)
	}
	secondNetworkName := localDockerName("opl-compute", secondComputeID)
	if _, exists, err := restartedProvider.inspectNetwork(ctx, secondNetworkName); err != nil || !exists {
		t.Fatalf("Workspace B compute network exists=%t err=%v", exists, err)
	}

	if destroyed, err := restartedProvider.DestroyWorkspaceRuntime(ctx, secondWorkspaceID); err != nil || destroyed.Status != "destroyed" {
		t.Fatalf("cleanup Workspace B runtime=%#v err=%v", destroyed, err)
	}
	cleanupB.runtimeOrSecretReady = false
	if _, err := restartedProvider.WorkspaceRuntimeStatus(ctx, secondWorkspaceID); !errors.Is(err, ErrWorkspaceLaunchResourceAbsent) {
		t.Fatalf("Workspace B runtime survived cleanup: %v", err)
	}
	if _, err := restartedProvider.WorkspaceRuntimeGatewaySecret(ctx, secondWorkspaceID); !errors.Is(err, ErrWorkspaceLaunchResourceAbsent) {
		t.Fatalf("Workspace B Secret survived cleanup: %v", err)
	}
	storageRootHandle, err = restartedProvider.openStorageRoot()
	if err != nil {
		t.Fatal(err)
	}
	_, secondReservationErr = readLocalDockerRuntimeReservation(storageRootHandle, localDockerRuntimeReservationName(secondRuntimeID))
	if closeErr := storageRootHandle.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if !errors.Is(secondReservationErr, ErrWorkspaceLaunchResourceAbsent) {
		t.Fatalf("Workspace B reservation survived cleanup: %v", secondReservationErr)
	}
	if detached, err := restartedService.DetachStorageAttachment(ctx, secondAttachment.Resources.AttachmentID); err != nil || detached.Status != "detached" {
		t.Fatalf("cleanup Workspace B attachment=%#v err=%v", detached, err)
	}
	cleanupB.attachmentReady = false
	if destroyed, err := restartedService.DestroyStorageVolume(ctx, secondStorageID); err != nil || destroyed.Status != "destroyed" {
		t.Fatalf("cleanup Workspace B storage=%#v err=%v", destroyed, err)
	}
	cleanupB.storageReady = false
	if _, err := os.Stat(secondWorkspaceStorageRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Workspace B storage directory survived cleanup: %v", err)
	}
	quotaBackend.mu.Lock()
	_, secondQuotaCleared := quotaBackend.cleared[secondStorageMetadata.ProjectID]
	quotaClearCalls = quotaBackend.clearCalls
	quotaBackend.mu.Unlock()
	if !secondQuotaCleared || quotaClearCalls != 2 {
		t.Fatalf("Workspace B project quota clear project=%d cleared=%t calls=%d", secondStorageMetadata.ProjectID, secondQuotaCleared, quotaClearCalls)
	}
	if destroying, err := restartedService.DestroyComputeAllocation(ctx, secondComputeID); err != nil || destroying.Status != "destroying" {
		t.Fatalf("start cleanup Workspace B compute=%#v err=%v", destroying, err)
	}
	if operation, err := waitForLocalDockerOperation(ctx, restartedService, "destroy_compute_allocation", "compute_allocation", secondComputeID, "succeeded"); err != nil {
		t.Fatalf("wait for cleanup Workspace B compute operation=%#v err=%v", operation, err)
	}
	if destroyed, err := restartedService.DestroyComputeAllocation(ctx, secondComputeID); err != nil || destroyed.Status != "destroyed" {
		t.Fatalf("cleanup Workspace B compute=%#v err=%v", destroyed, err)
	}
	cleanupB.computeReady = false
	if _, exists, err := restartedProvider.inspectNetwork(ctx, secondNetworkName); err != nil || exists {
		t.Fatalf("Workspace B compute network survived cleanup exists=%t err=%v", exists, err)
	}
}

func waitForLocalDockerOperation(ctx context.Context, service *Service, action, resourceKind, resourceID, status string) (FabricOperation, error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var latest FabricOperation
	for {
		operations, err := service.ListOperations(ctx)
		if err != nil {
			return latest, err
		}
		for _, operation := range operations {
			if operation.Action != action || operation.ResourceKind != resourceKind || operation.ResourceID != resourceID {
				continue
			}
			latest = operation
		}
		if latest.Status == status {
			return latest, nil
		}
		if latest.Status == "failed" {
			return latest, fmt.Errorf("operation_failed: %s", latest.ErrorCode)
		}
		select {
		case <-ctx.Done():
			return latest, ctx.Err()
		case <-ticker.C:
		}
	}
}

type localDockerWorkspaceCleanup struct {
	owner                *Service
	workspaceID          string
	runtimeDestroyKey    string
	attachmentID         string
	storageID            string
	computeID            string
	runtimeOrSecretReady bool
	attachmentReady      bool
	storageReady         bool
	computeReady         bool
}

func (c *localDockerWorkspaceCleanup) run(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if c.runtimeOrSecretReady {
		destroyed, err := c.owner.DestroyWorkspaceRuntime(ctx, c.workspaceID, c.runtimeDestroyKey)
		if err != nil || destroyed.Status != "destroyed" {
			t.Errorf("cleanup runtime workspace=%s result=%#v err=%v", c.workspaceID, destroyed, err)
			return
		}
		c.runtimeOrSecretReady = false
	}
	if c.attachmentReady {
		detached, err := c.owner.DetachStorageAttachment(ctx, c.attachmentID)
		if err != nil || detached.Status != "detached" {
			t.Errorf("cleanup attachment id=%s result=%#v err=%v", c.attachmentID, detached, err)
			return
		}
		c.attachmentReady = false
	}
	if c.storageReady {
		destroyed, err := c.owner.DestroyStorageVolume(ctx, c.storageID)
		if err != nil || destroyed.Status != "destroyed" {
			t.Errorf("cleanup storage id=%s result=%#v err=%v", c.storageID, destroyed, err)
			return
		}
		c.storageReady = false
	}
	if !c.computeReady {
		return
	}
	destroying, err := c.owner.DestroyComputeAllocation(ctx, c.computeID)
	if err != nil || (destroying.Status != "destroying" && destroying.Status != "destroyed") {
		t.Errorf("start cleanup compute id=%s result=%#v err=%v", c.computeID, destroying, err)
		return
	}
	if destroying.Status == "destroying" {
		operation, waitErr := waitForLocalDockerOperation(ctx, c.owner, "destroy_compute_allocation", "compute_allocation", c.computeID, "succeeded")
		if waitErr != nil {
			t.Errorf("wait cleanup compute id=%s operation=%#v err=%v", c.computeID, operation, waitErr)
			return
		}
	}
	destroyed, err := c.owner.DestroyComputeAllocation(ctx, c.computeID)
	if err != nil || destroyed.Status != "destroyed" {
		t.Errorf("cleanup compute id=%s result=%#v err=%v", c.computeID, destroyed, err)
		return
	}
	c.computeReady = false
}

func TestExactContainerNetworkMembershipRejectsIdentityDrift(t *testing.T) {
	container := dockerContainerInspect{}
	container.NetworkSettings.Networks = map[string]dockerEndpointSettings{"opl-compute-test": {NetworkID: "network-test"}}
	if bound, err := exactContainerNetworkMembership(container, "opl-compute-test", "network-test"); err != nil || !bound {
		t.Fatalf("exact membership bound=%t err=%v", bound, err)
	}
	for name, networks := range map[string]map[string]dockerEndpointSettings{
		"wrong network id":   {"opl-compute-test": {NetworkID: "network-other"}},
		"same id wrong name": {"opl-compute-other": {NetworkID: "network-test"}},
		"duplicate network id": {
			"opl-compute-test": {NetworkID: "network-test"}, "opl-compute-other": {NetworkID: "network-test"},
		},
		"empty endpoint": {"opl-compute-test": {}},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := dockerContainerInspect{}
			candidate.NetworkSettings.Networks = networks
			if _, err := exactContainerNetworkMembership(candidate, "opl-compute-test", "network-test"); err == nil {
				t.Fatal("network identity drift did not fail closed")
			}
		})
	}
}

func TestLocalDockerGatewaySecretOwnerReadDoesNotCallDocker(t *testing.T) {
	runner := &recordingDockerRunner{}
	provider := newLocalDockerProvider(LocalDockerProviderConfig{GatewaySecretRoot: localDockerSecretTestRoot(t)}, runner)
	key := []byte("key")
	digest := fmt.Sprintf("%x", sha256.Sum256(key))
	if err := provider.writeGatewaySecret("opl-gateway-ws-test", key, localDockerGatewayMetadata{
		AccountID: "acct-test", WorkspaceID: "ws-test", WorkspaceAPIKeyID: 1, SecretRef: "opl-gateway-ws-test",
		Fingerprint: "sha256:" + digest, Version: digest[:16],
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ReadGatewaySecretByDigest(context.Background(), GatewaySecretReadbackInput{
		AccountID: "acct-test", WorkspaceID: "ws-test", WorkspaceAPIKeyID: 1, SecretRef: "opl-gateway-ws-test",
		Fingerprint: "sha256:" + digest, KeyDigest: digest,
	}); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("secret owner read reached Docker: %#v", runner.calls)
	}
	misconfigured := newLocalDockerProvider(LocalDockerProviderConfig{}, runner)
	if _, err := misconfigured.ReadGatewaySecretByDigest(context.Background(), GatewaySecretReadbackInput{SecretRef: "opl-gateway-ws-test"}); err == nil {
		t.Fatal("missing gateway secret root did not fail closed")
	}
}

func TestLocalDockerGatewaySecretAuthoritativeReadMatrixIsReadOnly(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		root := localDockerSecretTestRoot(t)
		runner := &recordingDockerRunner{}
		provider := newLocalDockerProvider(LocalDockerProviderConfig{GatewaySecretRoot: root}, runner)
		before := localDockerSecretTreeDigest(t, root)
		_, err := provider.ReadGatewaySecretByDigest(context.Background(), GatewaySecretReadbackInput{SecretRef: "opl-gateway-ws-absent"})
		if !errors.Is(err, ErrWorkspaceLaunchResourceAbsent) {
			t.Fatalf("absent read err=%v", err)
		}
		if after := localDockerSecretTreeDigest(t, root); after != before {
			t.Fatalf("absent GET mutated tree before=%s after=%s", before, after)
		}
		if len(runner.calls) != 0 {
			t.Fatalf("absent GET reached Docker: %#v", runner.calls)
		}
	})

	t.Run("ready", func(t *testing.T) {
		root := localDockerSecretTestRoot(t)
		runner := &recordingDockerRunner{}
		provider := newLocalDockerProvider(LocalDockerProviderConfig{GatewaySecretRoot: root}, runner)
		key := []byte("ready-key")
		digest := fmt.Sprintf("%x", sha256.Sum256(key))
		input := GatewaySecretReadbackInput{
			AccountID: "acct-ready", WorkspaceID: "ws-ready", WorkspaceAPIKeyID: 17, SecretRef: "opl-gateway-ws-ready",
			Fingerprint: "sha256:" + digest, KeyDigest: digest,
		}
		if err := provider.writeGatewaySecret(input.SecretRef, key, localDockerGatewayMetadata{
			AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, WorkspaceAPIKeyID: input.WorkspaceAPIKeyID,
			SecretRef: input.SecretRef, Fingerprint: input.Fingerprint, Version: digest[:16],
		}); err != nil {
			t.Fatal(err)
		}
		before := localDockerSecretTreeDigest(t, root)
		secret, err := provider.ReadGatewaySecretByDigest(context.Background(), input)
		if err != nil || secret.Version != digest[:16] || secret.Fingerprint != input.Fingerprint {
			t.Fatalf("ready secret=%#v err=%v", secret, err)
		}
		if after := localDockerSecretTreeDigest(t, root); after != before {
			t.Fatalf("ready GET mutated tree before=%s after=%s", before, after)
		}
		if len(runner.calls) != 0 {
			t.Fatalf("ready GET reached Docker: %#v", runner.calls)
		}
	})

	t.Run("conflict", func(t *testing.T) {
		root := localDockerSecretTestRoot(t)
		runner := &recordingDockerRunner{}
		provider := newLocalDockerProvider(LocalDockerProviderConfig{GatewaySecretRoot: root}, runner)
		secretRef := seedLocalDockerGatewaySecret(t, provider, "acct-conflict", "ws-conflict")
		before := localDockerSecretTreeDigest(t, root)
		_, err := provider.ReadGatewaySecretByDigest(context.Background(), GatewaySecretReadbackInput{
			AccountID: "acct-foreign", WorkspaceID: "ws-conflict", WorkspaceAPIKeyID: 1, SecretRef: secretRef,
			Fingerprint: "sha256:" + strings.Repeat("0", 64), KeyDigest: strings.Repeat("0", 64),
		})
		if !errors.Is(err, ErrLaunchStageBindingConflict) {
			t.Fatalf("conflict read err=%v", err)
		}
		if after := localDockerSecretTreeDigest(t, root); after != before {
			t.Fatalf("conflict GET mutated tree before=%s after=%s", before, after)
		}
		if len(runner.calls) != 0 {
			t.Fatalf("conflict GET reached Docker: %#v", runner.calls)
		}
	})

	t.Run("root_error", func(t *testing.T) {
		root := localDockerSecretTestRoot(t)
		runner := &recordingDockerRunner{}
		provider := newLocalDockerProvider(LocalDockerProviderConfig{GatewaySecretRoot: root}, runner)
		if err := os.Chmod(root, 0755); err != nil {
			t.Fatal(err)
		}
		before := localDockerSecretTreeDigest(t, root)
		_, err := provider.ReadGatewaySecretByDigest(context.Background(), GatewaySecretReadbackInput{SecretRef: "opl-gateway-ws-error"})
		if err == nil || !strings.Contains(err.Error(), "local_docker_gateway_secret_root_invalid") {
			t.Fatalf("root mode drift read err=%v", err)
		}
		if after := localDockerSecretTreeDigest(t, root); after != before {
			t.Fatalf("error GET mutated tree before=%s after=%s", before, after)
		}
		if len(runner.calls) != 0 {
			t.Fatalf("error GET reached Docker: %#v", runner.calls)
		}
	})
}

func TestLocalDockerGatewaySecretConfigurationAndModeDriftFailClosed(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		provider := newLocalDockerProvider(LocalDockerProviderConfig{}, &recordingDockerRunner{})
		if _, err := provider.ReadGatewaySecretByDigest(context.Background(), GatewaySecretReadbackInput{SecretRef: "opl-gateway-ws-test"}); err == nil {
			t.Fatal("missing root accepted")
		}
	})
	t.Run("relative", func(t *testing.T) {
		provider := newLocalDockerProvider(LocalDockerProviderConfig{GatewaySecretRoot: "relative/secrets"}, &recordingDockerRunner{})
		if _, err := provider.ReadGatewaySecretByDigest(context.Background(), GatewaySecretReadbackInput{SecretRef: "opl-gateway-ws-test"}); err == nil {
			t.Fatal("relative root accepted")
		}
	})
	t.Run("configured_mode", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0755); err != nil {
			t.Fatal(err)
		}
		provider := newLocalDockerProvider(LocalDockerProviderConfig{GatewaySecretRoot: root}, &recordingDockerRunner{})
		if _, err := provider.ReadGatewaySecretByDigest(context.Background(), GatewaySecretReadbackInput{SecretRef: "opl-gateway-ws-test"}); err == nil {
			t.Fatal("insecure root mode accepted")
		}
	})
	for _, testCase := range []struct {
		name        string
		prepare     func(t *testing.T, root, secretRef string)
		driftedPath func(root, secretRef string) string
	}{
		{
			name: "secret_directory_mode",
			prepare: func(t *testing.T, root, secretRef string) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(root, secretRef), 0700); err != nil {
					t.Fatal(err)
				}
			},
			driftedPath: func(root, secretRef string) string { return filepath.Join(root, secretRef) },
		},
		{
			name: "versions_directory_mode",
			prepare: func(t *testing.T, root, secretRef string) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(root, secretRef), 0711); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(filepath.Join(root, secretRef), 0711); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(root, secretRef, localDockerGatewayVersionsDir), 0700); err != nil {
					t.Fatal(err)
				}
			},
			driftedPath: func(root, secretRef string) string {
				return filepath.Join(root, secretRef, localDockerGatewayVersionsDir)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := localDockerSecretTestRoot(t)
			secretRef := "opl-gateway-ws-mode"
			testCase.prepare(t, root, secretRef)
			provider := newLocalDockerProvider(LocalDockerProviderConfig{GatewaySecretRoot: root}, &recordingDockerRunner{})
			key := []byte("mode-key")
			digest := fmt.Sprintf("%x", sha256.Sum256(key))
			err := provider.writeGatewaySecret(secretRef, key, localDockerGatewayMetadata{
				AccountID: "acct-mode", WorkspaceID: "ws-mode", WorkspaceAPIKeyID: 1, SecretRef: secretRef,
				Fingerprint: "sha256:" + digest, Version: digest[:16],
			})
			if !errors.Is(err, ErrLaunchStageBindingConflict) {
				t.Fatalf("directory mode drift err=%v", err)
			}
			info, statErr := os.Lstat(testCase.driftedPath(root, secretRef))
			if statErr != nil || info.Mode().Perm() != 0700 {
				t.Fatalf("drifted directory was modified info=%#v err=%v", info, statErr)
			}
		})
	}
	for _, testCase := range []struct {
		name string
		file string
		mode os.FileMode
	}{
		{name: "key_file_mode", file: localDockerGatewayKeyFile, mode: 0644},
		{name: "metadata_file_mode", file: localDockerGatewayMetaFile, mode: 0600},
		{name: "webui_password_file_mode", file: localDockerWebUIPasswordFile, mode: 0600},
		{name: "webui_session_secret_file_mode", file: localDockerWebUISessionSecretFile, mode: 0600},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := localDockerSecretTestRoot(t)
			provider := newLocalDockerProvider(LocalDockerProviderConfig{GatewaySecretRoot: root}, &recordingDockerRunner{})
			secretRef := seedLocalDockerGatewaySecret(t, provider, "acct-mode", "ws-mode")
			current, err := os.Readlink(filepath.Join(root, secretRef, "current"))
			if err != nil {
				t.Fatal(err)
			}
			driftedPath := filepath.Join(root, secretRef, current, testCase.file)
			if err := os.Chmod(driftedPath, testCase.mode); err != nil {
				t.Fatal(err)
			}
			_, _, err = provider.readGatewaySecretFiles(secretRef)
			if !errors.Is(err, ErrLaunchStageBindingConflict) {
				t.Fatalf("file mode drift read err=%v", err)
			}
			key := []byte("key-ws-mode")
			digest := fmt.Sprintf("%x", sha256.Sum256(key))
			err = provider.writeGatewaySecret(secretRef, key, localDockerGatewayMetadata{
				AccountID: "acct-mode", WorkspaceID: "ws-mode", WorkspaceAPIKeyID: 1, SecretRef: secretRef,
				Fingerprint: "sha256:" + digest, Version: digest[:16],
			})
			if !errors.Is(err, ErrLaunchStageBindingConflict) {
				t.Fatalf("file mode drift write err=%v", err)
			}
			info, statErr := os.Lstat(driftedPath)
			if statErr != nil || info.Mode().Perm() != testCase.mode {
				t.Fatalf("drifted file was modified info=%#v err=%v", info, statErr)
			}
		})
	}
}

func TestLocalDockerGatewaySecretCrashCutRotationAndRestart(t *testing.T) {
	root := localDockerSecretTestRoot(t)
	secretRef := "opl-gateway-ws-restart"
	key := []byte("gateway-key-v1")
	digest := fmt.Sprintf("%x", sha256.Sum256(key))
	versionName, err := localDockerGatewayVersionDir(digest)
	if err != nil {
		t.Fatal(err)
	}
	metadata := localDockerGatewayMetadata{
		AccountID: "acct-restart", WorkspaceID: "ws-restart", WorkspaceAPIKeyID: 9, SecretRef: secretRef,
		Fingerprint: "sha256:" + digest, Version: digest[:16],
	}
	versionPath := filepath.Join(root, secretRef, localDockerGatewayVersionsDir, versionName)
	if err := os.MkdirAll(versionPath, 0711); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{
		filepath.Join(root, secretRef),
		filepath.Join(root, secretRef, localDockerGatewayVersionsDir),
		versionPath,
	} {
		if err := os.Chmod(directory, 0711); err != nil {
			t.Fatal(err)
		}
	}
	body, _ := json.Marshal(metadata)
	keyPath := filepath.Join(versionPath, localDockerGatewayKeyFile)
	if err := os.WriteFile(keyPath, key, 0444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(keyPath, 0444); err != nil {
		t.Fatal(err)
	}
	metadataPath := filepath.Join(versionPath, localDockerGatewayMetaFile)
	if err := os.WriteFile(metadataPath, body, 0400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(metadataPath, 0400); err != nil {
		t.Fatal(err)
	}
	passwordPath := filepath.Join(versionPath, localDockerWebUIPasswordFile)
	if err := os.WriteFile(passwordPath, []byte(deriveAionUIAdminPassword(localDockerTestWebUISeed, metadata.WorkspaceID, metadata.Version)), 0400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(passwordPath, 0400); err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(versionPath, localDockerWebUISessionSecretFile)
	if err := os.WriteFile(sessionPath, []byte(deriveWebUISessionSecret(localDockerTestWebUISeed, metadata.WorkspaceID, metadata.Version)), 0400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sessionPath, 0400); err != nil {
		t.Fatal(err)
	}
	currentTarget, _ := localDockerGatewayCurrentTarget(digest)
	if err := os.Symlink(currentTarget, filepath.Join(root, secretRef, ".current-.staging-crash")); err != nil {
		t.Fatal(err)
	}

	provider := newLocalDockerProvider(LocalDockerProviderConfig{GatewaySecretRoot: root}, &recordingDockerRunner{})
	if _, _, err := provider.readGatewaySecretFiles(secretRef); !errors.Is(err, ErrWorkspaceLaunchResourceAbsent) {
		t.Fatalf("pre-publication crash read err=%v", err)
	}
	restarted := newLocalDockerProvider(LocalDockerProviderConfig{GatewaySecretRoot: root}, &recordingDockerRunner{})
	if err := restarted.writeGatewaySecret(secretRef, key, metadata); err != nil {
		t.Fatalf("restart publish: %v", err)
	}
	if _, _, err := restarted.readGatewaySecretFiles(secretRef); err != nil {
		t.Fatalf("restart read: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, secretRef, localDockerGatewayKeyFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy top-level key link exists err=%v", err)
	}
	oldKey, err := os.ReadFile(filepath.Join(versionPath, localDockerGatewayKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	oldPassword, err := os.ReadFile(filepath.Join(versionPath, localDockerWebUIPasswordFile))
	if err != nil {
		t.Fatal(err)
	}
	oldSessionSecret, err := os.ReadFile(filepath.Join(versionPath, localDockerWebUISessionSecretFile))
	if err != nil {
		t.Fatal(err)
	}

	rotatedKey := []byte("gateway-key-v2")
	rotatedDigest := fmt.Sprintf("%x", sha256.Sum256(rotatedKey))
	rotatedMetadata := metadata
	rotatedMetadata.Fingerprint, rotatedMetadata.Version = "sha256:"+rotatedDigest, rotatedDigest[:16]
	if err := restarted.writeGatewaySecret(secretRef, rotatedKey, rotatedMetadata); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if current, err := os.Readlink(filepath.Join(root, secretRef, "current")); err != nil || current != localDockerGatewayVersionsDir+"/sha256-"+rotatedDigest {
		t.Fatalf("current=%q err=%v", current, err)
	}
	if preserved, err := os.ReadFile(filepath.Join(versionPath, localDockerGatewayKeyFile)); err != nil || string(preserved) != string(oldKey) {
		t.Fatalf("immutable old version changed body=%q err=%v", preserved, err)
	}
	if preserved, err := os.ReadFile(filepath.Join(versionPath, localDockerWebUIPasswordFile)); err != nil || string(preserved) != string(oldPassword) {
		t.Fatalf("immutable old WebUI password changed body=%q err=%v", preserved, err)
	}
	if preserved, err := os.ReadFile(filepath.Join(versionPath, localDockerWebUISessionSecretFile)); err != nil || string(preserved) != string(oldSessionSecret) {
		t.Fatalf("immutable old WebUI session secret changed body=%q err=%v", preserved, err)
	}
	finalProvider := newLocalDockerProvider(LocalDockerProviderConfig{GatewaySecretRoot: root}, &recordingDockerRunner{})
	if readKey, readMetadata, err := finalProvider.readGatewaySecretFiles(secretRef); err != nil || string(readKey) != string(rotatedKey) || readMetadata != rotatedMetadata {
		t.Fatalf("post-rotation restart key=%q metadata=%#v err=%v", readKey, readMetadata, err)
	}
}

func TestLocalDockerRuntimeUsesExactReadOnlySecretBindAndIdentityLabels(t *testing.T) {
	provider, runner, ctx, _, input, compute, volume := localDockerRuntimeReplayFixture(t, 0)
	runtime, err := provider.CreateWorkspaceRuntime(ctx, input, compute, volume)
	if err != nil || !runtime.Ready {
		t.Fatalf("runtime=%#v err=%v", runtime, err)
	}
	if runner.runCalls != 1 {
		t.Fatalf("runtime create calls=%d all=%#v", runner.runCalls, runner.calls)
	}
	var run []string
	for _, call := range runner.calls {
		if len(call) > 0 && call[0] == "run" {
			run = call
			break
		}
	}
	joined := strings.Join(run, " ")
	secretPath := runner.container.Mounts[3].Source
	storagePaths, storageErr := provider.storagePaths(input.WorkspaceID)
	if storageErr != nil {
		t.Fatal(storageErr)
	}
	for _, required := range []string{
		`--health-cmd node -e 'fetch("http://127.0.0.1:3000/").then(r=>process.exit(r.ok?0:1)).catch(()=>process.exit(1))'`,
		"--health-interval 5s",
		"--health-timeout 3s",
		"--health-start-period 10s",
		"--health-retries 120",
		"type=bind,source=" + storagePaths.Data + ",target=/data,bind-propagation=rprivate",
		"type=bind,source=" + storagePaths.Projects + ",target=/projects,bind-propagation=rprivate",
		"type=tmpfs,target=/recovery,tmpfs-mode=0700",
		"type=bind,source=" + secretPath + ",target=/run/secrets,readonly,bind-propagation=rprivate",
		"OPL_PROJECTS_DIR=/projects",
		"OPL_WORKSPACE_ROOT=/projects",
		"OPL_WEBUI_RECOVERY_DIR=/recovery",
		"OPL_WEBUI_AUTH_MODE=password",
		"OPL_WEBUI_USERNAME=opl",
		"OPL_WEBUI_PASSWORD_FILE=/run/secrets/opl_webui_password",
		"OPL_WEBUI_SESSION_SECRET_FILE=/run/secrets/webui_session_secret",
		"AIONUI_ALLOW_REMOTE=true",
		"ALLOW_REMOTE=true",
		"HOME=/data",
		"CODEX_HOME=/data/.codex",
		"opl.secret.ref=" + input.GatewaySecretRef,
		"opl.secret.version=" + runner.container.Config.Labels["opl.secret.version"],
		"opl.secret.fingerprint=" + runner.container.Config.Labels["opl.secret.fingerprint"],
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("docker run=%q missing %q", run, required)
		}
		if strings.Contains(joined, "OPL_GATEWAY_API_KEY_FILE=") {
			t.Fatalf("runtime must configure the Gateway key after WebUI health, not during App startup: %q", run)
		}
	}
	if strings.Contains(joined, "type=volume,source="+input.GatewaySecretRef) || strings.Contains(joined, " tar ") {
		t.Fatalf("runtime used legacy secret volume/helper: %q", run)
	}
}

func TestLocalDockerRuntimeEnforcesComputePlanCgroupLimits(t *testing.T) {
	for _, test := range []struct {
		name       string
		packageID  string
		cpus       string
		memoryByte string
	}{
		{name: "basic", packageID: "basic", cpus: "2", memoryByte: "4294967296"},
		{name: "pro", packageID: "pro", cpus: "8", memoryByte: "17179869184"},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider, runner, ctx, _, input, compute, volume := localDockerRuntimeReplayFixture(t, 0)
			compute.PackageID = test.packageID
			runner.container.Config.Labels["opl.compute.package.id"] = test.packageID
			setLocalDockerRuntimeHostConfigLimits(t, &runner.container, test.cpus, test.memoryByte)

			runtime, err := provider.CreateWorkspaceRuntime(ctx, input, compute, volume)
			if err != nil || !runtime.Ready {
				t.Fatalf("runtime=%#v err=%v", runtime, err)
			}
			var run []string
			for _, call := range runner.calls {
				if len(call) > 0 && call[0] == "run" {
					run = call
					break
				}
			}
			for index := 0; index+1 < len(run); index++ {
				if run[index] == "--cpus" && run[index+1] == test.cpus {
					break
				}
				if index == len(run)-2 {
					t.Fatalf("docker run=%q missing --cpus %s", run, test.cpus)
				}
			}
			for _, flag := range []string{"--memory", "--memory-swap"} {
				found := false
				for index := 0; index+1 < len(run); index++ {
					if run[index] == flag && run[index+1] == test.memoryByte {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("docker run=%q missing %s %s", run, flag, test.memoryByte)
				}
			}
		})
	}
}

func TestLocalDockerRuntimeFailsClosedOnCgroupReadbackDrift(t *testing.T) {
	provider, runner, ctx, _, input, compute, volume := localDockerRuntimeReplayFixture(t, 1)
	compute.PackageID = "basic"
	runner.container.Config.Labels["opl.compute.package.id"] = compute.PackageID
	setLocalDockerRuntimeHostConfigLimits(t, &runner.container, "0", "0")

	if _, err := provider.CreateWorkspaceRuntime(ctx, input, compute, volume); err == nil || err.Error() != "local_docker_runtime_readback_mismatch" {
		t.Fatalf("create err=%v", err)
	}

	setLocalDockerRuntimeHostConfigLimits(t, &runner.container, "2", "4294967296")
	if runtime, err := provider.WorkspaceRuntimeStatus(context.Background(), input.WorkspaceID); err != nil || !runtime.Ready {
		t.Fatalf("baseline status runtime=%#v err=%v", runtime, err)
	}

	setLocalDockerRuntimeHostConfigLimits(t, &runner.container, "1", "4294967296")
	if _, err := provider.WorkspaceRuntimeStatus(context.Background(), input.WorkspaceID); err == nil || err.Error() != "local_docker_runtime_readback_mismatch" {
		t.Fatalf("CPU status err=%v", err)
	}

	setLocalDockerRuntimeHostConfigLimits(t, &runner.container, "2", "4294967296")
	runner.container.HostConfig.Memory--
	if _, err := provider.WorkspaceRuntimeStatus(context.Background(), input.WorkspaceID); err == nil || err.Error() != "local_docker_runtime_readback_mismatch" {
		t.Fatalf("memory status err=%v", err)
	}

	setLocalDockerRuntimeHostConfigLimits(t, &runner.container, "2", "4294967296")
	runner.container.HostConfig.MemorySwap--
	if _, err := provider.WorkspaceRuntimeStatus(context.Background(), input.WorkspaceID); err == nil || err.Error() != "local_docker_runtime_readback_mismatch" {
		t.Fatalf("memory swap status err=%v", err)
	}
}

func TestLocalDockerRuntimeStatusUsesDurableReservationAcrossProfileDrift(t *testing.T) {
	provider, runner, ctx, _, input, compute, volume := localDockerRuntimeReplayFixture(t, 0)
	runtime, err := provider.CreateWorkspaceRuntime(ctx, input, compute, volume)
	if err != nil || !runtime.Ready {
		t.Fatalf("create runtime=%#v err=%v", runtime, err)
	}
	profile := []byte(`{"schemaVersion":1,"packages":[{"id":"basic","name":"Current Basic","available":true,"compute":{"id":"current-basic","server":"4c8g","cpu":4,"memoryGb":8,"instanceType":"current-4c8g"},"storage":{"sizeGb":10,"quotaPolicy":"linux-project"}}]}`)
	drifted := newLocalDockerProvider(LocalDockerProviderConfig{
		GatewaySecretRoot: provider.gatewaySecretRoot, HostStorageRoot: provider.hostStorageRoot, RuntimeHost: provider.runtimeHost,
		StorageQuotaBackend: provider.storageQuota, ProviderProfileJSON: profile,
	}, runner)
	status, err := drifted.WorkspaceRuntimeStatus(context.Background(), input.WorkspaceID)
	if err != nil || !status.Ready || status.ID != runtime.ID {
		t.Fatalf("profile-drifted status=%#v err=%v", status, err)
	}
}

func setLocalDockerRuntimeHostConfigLimits(t *testing.T, container *dockerContainerInspect, cpus, memoryBytes string) {
	t.Helper()
	cpuCount, err := strconv.ParseInt(cpus, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	memory, err := strconv.ParseInt(memoryBytes, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(container)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	hostConfig, ok := raw["HostConfig"].(map[string]any)
	if !ok {
		t.Fatal("missing HostConfig")
	}
	hostConfig["NanoCpus"] = cpuCount * 1_000_000_000
	hostConfig["Memory"] = memory
	hostConfig["MemorySwap"] = memory
	body, err = json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, container); err != nil {
		t.Fatal(err)
	}
}

func TestLocalDockerRuntimeCreateKeepsStartingHealthPendingUntilAuthoritativeReady(t *testing.T) {
	provider, runner, ctx, store, input, compute, volume := localDockerRuntimeReplayFixture(t, 0)
	runner.container.State.Health.Status = "starting"

	runtime, err := provider.CreateWorkspaceRuntime(ctx, input, compute, volume)
	if !errors.Is(err, ErrWorkspaceLaunchPending) || runtime.Ready || runtime.Status != "unready" {
		t.Fatalf("starting runtime=%#v err=%v", runtime, err)
	}
	childID := providerMutationOperationID(
		providerMutationJournalFromContext(ctx).parent, "local_docker_runtime_create", "workspace_runtime", localRuntimeID(input.WorkspaceID), localRuntimeName(input.WorkspaceID),
	)
	child, childErr := store.Get(context.Background(), childID)
	if childErr != nil || child.Status != "succeeded" || runner.runCalls != 1 {
		t.Fatalf("starting child=%#v err=%v runCalls=%d", child, childErr, runner.runCalls)
	}

	runner.container.State.Health.Status = "healthy"
	runtime, err = provider.CreateWorkspaceRuntime(ctx, input, compute, volume)
	if err != nil || !runtime.Ready || runtime.Status != "running" || runner.runCalls != 1 {
		t.Fatalf("ready runtime=%#v err=%v runCalls=%d", runtime, err, runner.runCalls)
	}
	child, childErr = store.Get(context.Background(), childID)
	if childErr != nil || child.Status != "succeeded" {
		t.Fatalf("ready child=%#v err=%v", child, childErr)
	}
}

func TestLocalDockerRuntimeStatusFailsClosedOnSecretIdentityOrMountDrift(t *testing.T) {
	provider, runner, _, _, input, _, _ := localDockerRuntimeReplayFixture(t, 1)
	if runtime, err := provider.WorkspaceRuntimeStatus(context.Background(), input.WorkspaceID); err != nil || !runtime.Ready ||
		runtime.Access.Username != webuiUsername || runtime.Access.Password != deriveAionUIAdminPassword(localDockerTestWebUISeed, input.WorkspaceID, runner.container.Config.Labels["opl.secret.version"]) ||
		runtime.Access.CredentialStatus != "configured" || runtime.Access.CredentialVersion != runner.container.Config.Labels["opl.secret.version"] || runtime.Access.SecretRef != input.GatewaySecretRef {
		t.Fatalf("baseline runtime=%#v err=%v", runtime, err)
	}

	t.Run("label", func(t *testing.T) {
		original := runner.container.Config.Labels["opl.secret.version"]
		runner.container.Config.Labels["opl.secret.version"] = "foreign-version"
		t.Cleanup(func() { runner.container.Config.Labels["opl.secret.version"] = original })
		if runtime, err := provider.WorkspaceRuntimeStatus(context.Background(), input.WorkspaceID); err == nil || runtime.Ready {
			t.Fatalf("drifted label runtime=%#v err=%v", runtime, err)
		}
	})

	t.Run("writable_mount", func(t *testing.T) {
		runner.container.Mounts[3].RW = true
		t.Cleanup(func() { runner.container.Mounts[3].RW = false })
		if runtime, err := provider.WorkspaceRuntimeStatus(context.Background(), input.WorkspaceID); err == nil || runtime.Ready {
			t.Fatalf("writable mount runtime=%#v err=%v", runtime, err)
		}
	})

	t.Run("projects_source", func(t *testing.T) {
		original := runner.container.HostConfig.Mounts[1].Source
		runner.container.HostConfig.Mounts[1].Source = filepath.Join(provider.hostStorageRoot, "foreign", "projects")
		t.Cleanup(func() { runner.container.HostConfig.Mounts[1].Source = original })
		if runtime, err := provider.WorkspaceRuntimeStatus(context.Background(), input.WorkspaceID); err == nil || runtime.Ready {
			t.Fatalf("drifted projects mount runtime=%#v err=%v", runtime, err)
		}
	})

	t.Run("healthcheck", func(t *testing.T) {
		original := runner.container.Config.Healthcheck.Test[1]
		runner.container.Config.Healthcheck.Test[1] = "exit 0"
		t.Cleanup(func() { runner.container.Config.Healthcheck.Test[1] = original })
		if runtime, err := provider.WorkspaceRuntimeStatus(context.Background(), input.WorkspaceID); err == nil || runtime.Ready {
			t.Fatalf("drifted healthcheck runtime=%#v err=%v", runtime, err)
		}
	})
}

func TestLocalDockerRuntimeReplayClaimSecondReadReadySkipsCreate(t *testing.T) {
	provider, runner, ctx, store, input, compute, volume := localDockerRuntimeReplayFixture(t, 2)
	childID := providerMutationOperationID(
		providerMutationJournalFromContext(ctx).parent, "local_docker_runtime_create", "workspace_runtime", localRuntimeID(input.WorkspaceID), localRuntimeName(input.WorkspaceID),
	)
	fresh, err := beginProviderMutation(ctx, "local_docker_runtime_create", "workspace_runtime", localRuntimeID(input.WorkspaceID), localRuntimeName(input.WorkspaceID))
	if err != nil || fresh == nil || !fresh.Fresh {
		t.Fatalf("seed child=%#v err=%v", fresh, err)
	}
	runtime, err := provider.CreateWorkspaceRuntime(ctx, input, compute, volume)
	if err != nil || !runtime.Ready {
		t.Fatalf("runtime=%#v err=%v", runtime, err)
	}
	if runner.containerReads != 2 || runner.runCalls != 0 {
		t.Fatalf("container reads=%d run calls=%d all=%#v", runner.containerReads, runner.runCalls, runner.calls)
	}
	operation, err := store.Get(context.Background(), childID)
	if err != nil || operation.Status != "succeeded" {
		t.Fatalf("child=%#v err=%v", operation, err)
	}
}

func TestLocalDockerDestroyWorkspaceRuntimeRejectsForeignRuntimeBeforeMutation(t *testing.T) {
	workspaceID := "ws-alpha"
	container := localDockerDestroyRuntimeContainer(workspaceID)
	container.Config.Labels["opl.workspace.id"] = "ws-foreign"
	runner := &localDockerDestroyRunner{
		containers: map[string]dockerContainerInspect{localRuntimeName(workspaceID): container}, volumes: map[string]dockerVolumeInspect{},
	}
	root := localDockerSecretTestRoot(t)
	provider := newLocalDockerProvider(LocalDockerProviderConfig{GatewaySecretRoot: root}, runner)
	secretRef := seedLocalDockerGatewaySecret(t, provider, "acct-alpha", workspaceID)
	if runtime, err := provider.DestroyWorkspaceRuntime(context.Background(), workspaceID); err == nil || runtime.Status == "destroyed" {
		t.Fatalf("runtime=%#v err=%v", runtime, err)
	}
	if got := localDockerRemoveCalls(runner.calls, "container"); len(got) != 0 {
		t.Fatalf("foreign runtime removed: %#v", got)
	}
	if _, err := os.Stat(filepath.Join(root, secretRef)); err != nil {
		t.Fatalf("secret removed after foreign runtime: %v", err)
	}
}

func TestLocalDockerDestroyWorkspaceRuntimeRejectsMountedSecretArchiveDriftBeforeMutation(t *testing.T) {
	workspaceID, accountID := "ws-alpha", "acct-alpha"
	runtimeName := localRuntimeName(workspaceID)
	runner := &localDockerDestroyRunner{
		containers: map[string]dockerContainerInspect{runtimeName: localDockerDestroyRuntimeContainer(workspaceID)},
		volumes:    map[string]dockerVolumeInspect{}, archives: map[string][]byte{},
	}
	root := localDockerSecretTestRoot(t)
	provider := newLocalDockerProvider(LocalDockerProviderConfig{GatewaySecretRoot: root}, runner)
	secretRef := seedLocalDockerGatewaySecret(t, provider, accountID, workspaceID)
	runner.containers[runtimeName], _ = bindLocalDockerDestroyRuntimeSecret(
		t, runner.containers[runtimeName], root, accountID, workspaceID, []byte("key-"+workspaceID),
	)
	runner.archives[runtimeName] = []byte("invalid archive")

	if runtime, err := provider.DestroyWorkspaceRuntime(context.Background(), workspaceID); err == nil || runtime.Status == "destroyed" {
		t.Fatalf("runtime=%#v err=%v", runtime, err)
	}
	if got := localDockerRemoveCalls(runner.calls, "container"); len(got) != 0 {
		t.Fatalf("runtime removed after archive drift: %#v", got)
	}
	if _, err := os.Stat(filepath.Join(root, secretRef)); err != nil {
		t.Fatalf("Secret removed after archive drift: %v", err)
	}
}
