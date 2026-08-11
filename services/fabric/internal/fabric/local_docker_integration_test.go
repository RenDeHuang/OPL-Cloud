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
	base     dockerRunner
	store    OperationStore
	launchID string
	t        *testing.T
}

func (r bindingCheckingDockerRunner) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	stage := ""
	if len(args) >= 2 && args[0] == "network" && args[1] == "create" {
		stage = "compute"
	}
	if len(args) >= 2 && args[0] == "volume" && args[1] == "create" {
		stage = "storage"
		if strings.Contains(args[len(args)-1], "gateway") {
			stage = "secret"
		}
	}
	if len(args) > 0 && args[0] == "run" {
		for _, value := range args {
			if value == "-d" {
				stage = "runtime"
			}
			if value == "tar" {
				stage = "secret"
			}
		}
	}
	if stage != "" {
		operations, err := r.store.List(ctx)
		if err != nil {
			return nil, err
		}
		found := false
		for _, operation := range operations {
			binding, ok := decodeLaunchStageBinding(operation)
			found = found || ok && binding.LaunchOperationID == r.launchID && binding.Stage == stage && operation.Status == "started"
		}
		if !found {
			return nil, fmt.Errorf("local_docker_%s_mutation_before_binding", stage)
		}
	}
	output, err := r.base.Run(ctx, stdin, args...)
	if err != nil && !dockerMissing(err) && r.t != nil {
		r.t.Logf("docker args=%q error=%v", args, err)
	}
	return output, err
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
	store := NewMemoryOperationStore()
	runner := bindingCheckingDockerRunner{base: execDockerRunner{binary: "docker"}, store: store, launchID: launchID, t: t}
	provider := newLocalDockerProvider(LocalDockerProviderConfig{HelperImage: imageID, RuntimeHost: "127.0.0.1"}, runner)
	service := NewServiceWithOperationStore(provider, store)

	compute, err := service.CreateComputeAllocation(ctx, ComputeAllocationInput{
		AccountID: "acct-local", WorkspaceID: "ws-" + stableSuffix(launchID)[:10], PackageID: "basic", IdempotencyKey: launchID + ":compute",
	})
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		current, ok := service.GetComputeAllocation(ctx, compute.ID)
		if ok && current.Status == "running" {
			compute = current
			break
		}
		if ok && current.Status == "quarantined" {
			operations, _ := store.List(ctx)
			t.Fatalf("compute quarantined: %#v operations=%#v", current, operations)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if compute.Status != "running" {
		t.Fatalf("compute did not become ready: %#v", compute)
	}

	volume, err := service.CreateStorageVolume(ctx, StorageVolumeInput{
		AccountID: compute.AccountID, WorkspaceID: compute.WorkspaceID, ComputeID: compute.ID, Zone: "local", SizeGB: 10, IdempotencyKey: launchID + ":storage",
	})
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := service.CreateStorageAttachment(ctx, StorageAttachmentInput{
		WorkspaceID: compute.WorkspaceID, ComputeID: compute.ID, VolumeID: volume.ID, IdempotencyKey: launchID + ":attachment",
	})
	if err != nil {
		t.Fatal(err)
	}
	key := "local-key-" + stableSuffix(launchID)
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
	secret, err := service.UpsertGatewaySecret(ctx, GatewaySecretInput{
		AccountID: compute.AccountID, WorkspaceID: compute.WorkspaceID, WorkspaceAPIKeyID: 1, Fingerprint: "sha256:" + digest,
		GatewayAPIKey: key, IdempotencyKey: launchID + ":secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := service.CreateWorkspaceRuntime(ctx, WorkspaceRuntimeInput{
		WorkspaceID: compute.WorkspaceID, ComputeID: compute.ID, VolumeID: volume.ID, AttachmentID: attachment.ID,
		AttachmentOperationID: attachment.OperationID, RuntimeOperationID: launchID + ":runtime", ImageID: imageID,
		GatewaySecretRef: secret.SecretRef, IdempotencyKey: launchID + ":runtime",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = service.DestroyWorkspaceRuntime(context.Background(), compute.WorkspaceID, launchID+":runtime-destroy")
		_, _ = service.DestroyStorageVolume(context.Background(), volume.ID)
		_, _ = service.DestroyComputeAllocation(context.Background(), compute.ID)
		_ = provider.RemoveGatewaySecret(context.Background(), compute.WorkspaceID)
	})
	if err := waitForLocalRuntime(ctx, runtime.URL); err != nil {
		t.Fatal(err)
	}
	var status WorkspaceRuntime
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		status, err = service.WorkspaceRuntimeStatus(ctx, compute.WorkspaceID)
		if err == nil && status.Ready {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if err != nil || !status.Ready || status.URL == "" || status.ImageID != imageID {
		t.Fatalf("runtime status = %#v, %v", status, err)
	}

	for _, stage := range []string{"compute", "storage", "attachment", "secret", "runtime"} {
		readback, err := service.LaunchStageBindingReadback(ctx, launchID, stage)
		if err != nil || !readback.Available || readback.Binding.Digest == "" || readback.Binding.RequestHash == "" {
			t.Fatalf("%s binding readback = %#v, %v", stage, readback, err)
		}
	}
}
