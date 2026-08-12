package fabric

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
	base        dockerRunner
	store       OperationStore
	parents     map[string]WorkspaceLaunchStageBinding
	mutationIDs map[string]string
	t           *testing.T
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

func (r bindingCheckingDockerRunner) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	stage, mutation := "", ""
	if len(args) >= 2 && args[0] == "network" && args[1] == "create" {
		stage, mutation = "ensure_compute_allocation", "local_docker_network_create"
	}
	if len(args) >= 2 && args[0] == "volume" && args[1] == "create" {
		stage, mutation = "storage", "local_docker_volume_create"
		if strings.Contains(args[len(args)-1], "gateway") {
			stage, mutation = "secret", "local_docker_secret_volume_create"
		}
	}
	if len(args) > 0 && args[0] == "run" {
		for _, value := range args {
			if value == "-d" {
				stage, mutation = "runtime", "local_docker_runtime_create"
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
	if err != nil && !dockerMissing(err) && r.t != nil {
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
	service := NewServiceWithOperationStore(newLocalDockerProvider(LocalDockerProviderConfig{}, runner), store)
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
		"ghcr.io/gaofeng21cn/one-person-lab-app-attacker@sha256:" + strings.Repeat("a", 64),
		"GHCR.IO/gaofeng21cn/one-person-lab-app@sha256:" + strings.Repeat("a", 64),
		"ghcr.io/gaofeng21cn/one-person-lab-app:stable@sha256:" + strings.Repeat("a", 64),
		"ghcr.io/gaofeng21cn/one-person-lab-app@other@sha256:" + strings.Repeat("a", 64),
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
	if provider.ValidateWorkspaceImageReference("ghcr.io/gaofeng21cn/one-person-lab-app@sha256:" + strings.Repeat("b", 64)) {
		t.Fatal("configured release manifest retained default repository trust")
	}
}

func TestLocalDockerAcceptsApprovedImmutableWorkspaceImage(t *testing.T) {
	image := "ghcr.io/gaofeng21cn/one-person-lab-app@sha256:" + strings.Repeat("a", 64)
	preflight, runner, operations := localDockerImageTrustPreflight(t, image)
	if !preflight.Available || preflight.Reason != "none" || preflight.BindingRef == "" {
		t.Fatalf("approved image preflight=%#v", preflight)
	}
	if len(runner.calls) != 1 || len(runner.calls[0]) == 0 || runner.calls[0][0] != "info" {
		t.Fatalf("approved image Docker calls=%#v", runner.calls)
	}
	if len(operations) != 1 || operations[0].ID != preflight.BindingRef || operations[0].Status != "succeeded" {
		t.Fatalf("approved image operations=%#v", operations)
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

	temp := t.TempDir()
	dockerfile := "FROM alpine:3.20\nEXPOSE 3000\nHEALTHCHECK --interval=1s --timeout=1s --retries=20 CMD wget -q -O- http://127.0.0.1:3000/ || exit 1\nCMD [\"sh\",\"-c\",\"while true; do { printf 'HTTP/1.1 200 OK\\r\\nContent-Length: 2\\r\\n\\r\\nok'; } | nc -l -p 3000; done\"]\n"
	if err := os.WriteFile(filepath.Join(temp, "Dockerfile"), []byte(dockerfile), 0600); err != nil {
		t.Fatal(err)
	}
	tag := "opl-fabric-local-test:" + stableSuffix(t.Name(), time.Now().String())[:12]
	build := exec.CommandContext(ctx, "docker", "build", "--quiet", "--tag", tag, temp)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build local runtime image: %v: %s", err, output)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", "-f", tag).Run() })
	imageOutput, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", tag).Output()
	if err != nil {
		t.Fatal(err)
	}
	imageID := strings.TrimSpace(string(imageOutput))

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
	runner := bindingCheckingDockerRunner{base: execDockerRunner{binary: "docker"}, store: store, parents: bindings, mutationIDs: mutationIDs, t: t}
	provider := newLocalDockerProvider(LocalDockerProviderConfig{
		HelperImage: imageID, RuntimeHost: "127.0.0.1", TrustedWorkspaceImageSources: []string{imageID},
	}, runner)
	service := NewServiceWithOperationStore(provider, store)
	launchRequestHash := stableSuffix("workspace-launch", launchID, accountID, workspaceID, imageID)
	preflight, err := service.PreflightWorkspaceLaunch(ctx, WorkspaceLaunchPreflightInput{
		SchemaVersion: 1, LaunchOperationID: launchID, AccountID: accountID, WorkspaceID: workspaceID,
		PackageID: "basic", SizeGB: 10, WorkspaceImageDigest: imageID, RequestHash: launchRequestHash,
	})
	if err != nil || !preflight.Available {
		t.Fatalf("preflight=%#v err=%v", preflight, err)
	}
	base := WorkspaceLaunchStageInput{
		ProviderProfileRef: "local-docker", PreflightBindingRef: preflight.BindingRef, PackageID: "basic",
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

	storageInput := base
	storageInput.Binding, storageInput.Resources = bindings["storage"], compute.Resources
	bindInput(&storageInput)
	mutationIDs["local_docker_volume_create"] = providerMutationOperationID(storageInput.Binding, "local_docker_volume_create", "storage_volume", storageID, localDockerName("opl-storage", storageID))
	storage, err := service.EnsureWorkspaceLaunchStage(ctx, storageInput)
	if err != nil || storage.State != "ready" {
		t.Fatalf("storage=%#v err=%v", storage, err)
	}

	attachmentInput := base
	attachmentInput.Binding, attachmentInput.Resources = bindings["attachment"], storage.Resources
	bindInput(&attachmentInput)
	attachment, err := service.EnsureWorkspaceLaunchStage(ctx, attachmentInput)
	if err != nil || attachment.State != "ready" {
		t.Fatalf("attachment=%#v err=%v", attachment, err)
	}

	secretInput := base
	secretInput.Binding, secretInput.Resources = bindings["secret"], attachment.Resources
	secretInput.Resources.GatewaySecretFingerprint = "sha256:" + digest
	secretInput.GatewayCredential = &WorkspaceLaunchGatewayCredential{KeyID: 1, Value: key}
	bindInput(&secretInput)
	mutationIDs["local_docker_secret_volume_create"] = providerMutationOperationID(secretInput.Binding, "local_docker_secret_volume_create", "gateway_secret", secretRef, secretRef)
	mutationIDs["local_docker_secret_write"] = providerMutationOperationID(secretInput.Binding, "local_docker_secret_write", "gateway_secret", secretRef, digest[:16])
	secret, err := service.EnsureWorkspaceLaunchStage(ctx, secretInput)
	if err != nil || secret.State != "ready" {
		t.Fatalf("secret=%#v err=%v", secret, err)
	}

	runtimeInput := base
	runtimeInput.Binding, runtimeInput.Resources = bindings["runtime"], secret.Resources
	bindInput(&runtimeInput)
	mutationIDs["local_docker_runtime_create"] = providerMutationOperationID(runtimeInput.Binding, "local_docker_runtime_create", "workspace_runtime", localRuntimeID(workspaceID), localRuntimeName(workspaceID))
	runtime, err := service.EnsureWorkspaceLaunchStage(ctx, runtimeInput)
	if err != nil || runtime.State != "ready" || runtime.Resources.RuntimeURL == "" {
		t.Fatalf("runtime=%#v err=%v", runtime, err)
	}
	t.Cleanup(func() {
		_, _ = provider.DestroyWorkspaceRuntime(context.Background(), workspaceID)
		_ = provider.RemoveGatewaySecret(context.Background(), workspaceID)
		_, _ = provider.DestroyStorageVolume(context.Background(), StorageVolume{ID: storage.Resources.StorageID})
		_, _ = provider.DestroyComputeAllocation(context.Background(), ComputeAllocation{ID: compute.Resources.ComputeAllocationID})
	})
	if err := waitForLocalRuntime(ctx, runtime.Resources.RuntimeURL); err != nil {
		t.Fatal(err)
	}

	for action, operationID := range mutationIDs {
		operation, err := store.Get(ctx, operationID)
		if err != nil || operation.Status != "succeeded" || operation.Action != action {
			t.Fatalf("provider mutation %s=%#v err=%v", action, operation, err)
		}
	}
}
