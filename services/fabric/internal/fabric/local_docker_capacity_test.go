package fabric

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"syscall"
	"testing"
)

func TestLocalDockerProviderRequiresDeploymentOwnedProfile(t *testing.T) {
	t.Setenv(localDockerProviderProfileEnv, "")
	provider := newLocalDockerProvider(LocalDockerProviderConfig{}, &recordingDockerRunner{})
	if _, err := provider.ResolveWorkspacePlan(context.Background(), WorkspaceLaunchPlanInput{PackageID: "basic", SizeGB: 10}); err == nil || err.Error() != "provider_plan_unavailable" {
		t.Fatalf("missing profile resolve err=%v", err)
	}
	if _, err := provider.Readiness(context.Background()); err == nil || err.Error() != "local_docker_provider_profile_required" {
		t.Fatalf("missing profile readiness err=%v", err)
	}
}

func TestLocalDockerProviderUsesConfiguredProfileForCatalogAndPlan(t *testing.T) {
	profile := []byte(`{"schemaVersion":1,"packages":[{"id":"basic","name":"Small Workspace","available":true,"compute":{"id":"configured-small","server":"1c2g","cpu":1,"memoryGb":2,"diskGb":10,"instanceType":"configured-1c2g"},"storage":{"sizeGb":10,"quotaPolicy":"linux-project"}}]}`)
	provider := newLocalDockerProvider(LocalDockerProviderConfig{ProviderProfileJSON: profile}, &recordingDockerRunner{})
	descriptor := provider.Descriptor()
	if len(descriptor.Plans) != 1 || descriptor.Plans["basic"].CPU != 1 || descriptor.Plans["basic"].MemoryGB != 2 || descriptor.Plans["basic"].InstanceType != "configured-1c2g" {
		t.Fatalf("descriptor=%#v", descriptor)
	}
	plan, err := provider.ResolveWorkspacePlan(context.Background(), WorkspaceLaunchPlanInput{PackageID: "basic", SizeGB: 10})
	if err != nil || !strings.Contains(string(plan), `"cpu":1`) || !strings.Contains(string(plan), `"memoryGb":2`) {
		t.Fatalf("configured plan=%s err=%v", plan, err)
	}
}

type localDockerCapacityRunner struct {
	info      []byte
	container dockerContainerInspect
}

func (r *localDockerCapacityRunner) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	switch {
	case len(args) == 3 && args[0] == "info":
		return append([]byte(nil), r.info...), nil
	case len(args) == 8 && args[0] == "container" && args[1] == "ls" && args[5] == "label=opl.fabric.kind=runtime":
		if r.container.ID == "" {
			return nil, nil
		}
		return json.Marshal(dockerObjectInventoryRow{ID: r.container.ID, Names: r.container.Name})
	case len(args) == 8 && args[0] == "container" && args[1] == "ls":
		if r.container.ID != "" && strings.Contains(args[5], r.container.Name) {
			return json.Marshal(dockerObjectInventoryRow{ID: r.container.ID, Names: r.container.Name})
		}
		return nil, nil
	case len(args) == 3 && args[0] == "container" && args[1] == "inspect" && args[2] == r.container.ID:
		return json.Marshal([]dockerContainerInspect{r.container})
	default:
		return nil, errors.New("unexpected docker call")
	}
}

func TestLocalDockerRuntimeHostCapacityRequiresCompleteJSONReadback(t *testing.T) {
	root := localDockerStorageTestRoot(t)
	provider := newLocalDockerProvider(LocalDockerProviderConfig{HostStorageRoot: root}, &localDockerCapacityRunner{info: []byte("{\"NCPU\":2,\"MemTotal\":4294967296}")})
	capacity, err := provider.readDockerHostCapacity(context.Background())
	if err != nil || capacity.CPUs != 2 || capacity.MemoryBytes != 4294967296 {
		t.Fatalf("capacity=%#v err=%v", capacity, err)
	}
	for _, body := range []string{"{\"NCPU\":0,\"MemTotal\":1}", "{\"NCPU\":2}", "not-json", "{\"NCPU\":2,\"MemTotal\":1} trailing"} {
		candidate := newLocalDockerProvider(LocalDockerProviderConfig{HostStorageRoot: root}, &localDockerCapacityRunner{info: []byte(body)})
		if _, err := candidate.readDockerHostCapacity(context.Background()); err == nil {
			t.Fatalf("accepted invalid info=%q", body)
		}
	}
}

func TestLocalDockerRuntimeCapacityCountsDurableReservations(t *testing.T) {
	rootPath := localDockerStorageTestRoot(t)
	workspaceID := "ws-existing"
	resourceID := localRuntimeID(workspaceID)
	runner := &localDockerCapacityRunner{info: []byte("{\"NCPU\":4,\"MemTotal\":8589934592}")}
	runner.container.ID, runner.container.Name = "container-existing", localRuntimeName(workspaceID)
	runner.container.Config.Labels = map[string]string{"opl.fabric.provider": "local-docker", "opl.fabric.kind": "runtime", "opl.workspace.id": workspaceID, "opl.resource.id": resourceID, localDockerComputePackageLabel: "basic"}
	runner.container.HostConfig.NanoCPUs, runner.container.HostConfig.Memory, runner.container.HostConfig.MemorySwap = 2_000_000_000, 4_294_967_296, 4_294_967_296
	provider := newLocalDockerProvider(LocalDockerProviderConfig{HostStorageRoot: rootPath}, runner)
	root, err := provider.openStorageRoot()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	existing := localDockerRuntimeReservation{SchemaVersion: 1, WorkspaceID: workspaceID, ResourceID: resourceID, PackageID: "basic", NanoCPUs: 2_000_000_000, MemoryBytes: 4_294_967_296}
	if err := writeLocalDockerRuntimeReservation(root, existing); err != nil {
		t.Fatal(err)
	}
	exact := localDockerRuntimeReservation{SchemaVersion: 1, WorkspaceID: "ws-new", ResourceID: "rt-new", PackageID: "basic", NanoCPUs: 2_000_000_000, MemoryBytes: 4_294_967_296}
	if err := provider.localDockerRuntimeCapacityAdmission(context.Background(), root, exact); err != nil {
		t.Fatalf("exact capacity rejected: %v", err)
	}
	over := localDockerRuntimeReservation{SchemaVersion: 1, WorkspaceID: "ws-over", ResourceID: "rt-over", PackageID: "basic", NanoCPUs: 3_000_000_000, MemoryBytes: 5_368_709_120}
	if err := provider.localDockerRuntimeCapacityAdmission(context.Background(), root, over); err == nil || err.Error() != "local_docker_runtime_capacity_insufficient" {
		t.Fatalf("over-capacity err=%v", err)
	}
}

func TestLocalDockerRuntimeCapacityRecoversReservationWithoutContainer(t *testing.T) {
	rootPath := localDockerStorageTestRoot(t)
	provider := newLocalDockerProvider(LocalDockerProviderConfig{HostStorageRoot: rootPath}, &localDockerCapacityRunner{info: []byte("{\"NCPU\":2,\"MemTotal\":4294967296}")})
	root, err := provider.openStorageRoot()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	stale := localDockerRuntimeReservation{SchemaVersion: 1, WorkspaceID: "ws-stale", ResourceID: localRuntimeID("ws-stale"), PackageID: "basic", NanoCPUs: 2_000_000_000, MemoryBytes: 4_294_967_296}
	if err := writeLocalDockerRuntimeReservation(root, stale); err != nil {
		t.Fatal(err)
	}
	requested := localDockerRuntimeReservation{SchemaVersion: 1, WorkspaceID: "ws-new", ResourceID: localRuntimeID("ws-new"), PackageID: "basic", NanoCPUs: 2_000_000_000, MemoryBytes: 4_294_967_296}
	if err := provider.localDockerRuntimeCapacityAdmission(context.Background(), root, requested); err != nil {
		t.Fatalf("stale reservation blocked capacity: %v", err)
	}
	if _, err := readLocalDockerRuntimeReservation(root, localDockerRuntimeReservationName(stale.ResourceID)); !errors.Is(err, ErrWorkspaceLaunchResourceAbsent) {
		t.Fatalf("stale reservation remains err=%v", err)
	}
}

func TestLocalDockerStorageReservationDeduplicatesMetadataAndTombstone(t *testing.T) {
	rootPath := localDockerStorageTestRoot(t)
	provider := newLocalDockerProvider(localDockerStorageTestConfig(rootPath), &recordingDockerRunner{})
	root, err := provider.openStorageRoot()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	metadata := localDockerStorageMetadata{SchemaVersion: localDockerStorageMetadataSchemaVersion, StorageID: "storage-same", AccountID: "acct", WorkspaceID: "ws-same", SizeGB: 1, ProjectID: 42}
	workspaceName, err := localDockerWorkspaceStorageName(metadata.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{workspaceName, ".storage-" + workspaceName} {
		if err := ensureLocalDockerStorageDirectory(root, name, 0700); err != nil {
			t.Fatal(err)
		}
		for _, child := range []string{"data", "projects"} {
			if err := ensureLocalDockerStorageDirectory(root, name+"/"+child, 0700); err != nil {
				t.Fatal(err)
			}
		}
		if err := writeLocalDockerStorageMetadata(root, name+"/"+localDockerStorageMetadataFile, metadata); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeLocalDockerStorageDeletion(root, localDockerStorageDeletionFromMetadata(metadata, workspaceName)); err != nil {
		t.Fatal(err)
	}
	reserved, err := provider.storageReservationBytesLocked(root, "")
	if err != nil || reserved != 1024*1024*1024 {
		t.Fatalf("reserved=%d err=%v", reserved, err)
	}
}

func TestLocalDockerEffectiveCapacityRejectsOverflow(t *testing.T) {
	stats := syscall.Statfs_t{Bsize: 4096, Blocks: ^uint64(0), Bfree: 0, Bavail: 1}
	if _, err := localDockerEffectiveCapacity(stats); err == nil {
		t.Fatal("overflowing statfs accepted")
	}
}

func TestRuntimeReservationRejectsTrailingJSON(t *testing.T) {
	rootPath := localDockerStorageTestRoot(t)
	provider := newLocalDockerProvider(LocalDockerProviderConfig{HostStorageRoot: rootPath}, &recordingDockerRunner{})
	root, err := provider.openStorageRoot()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := ensureLocalDockerStorageDirectory(root, localDockerRuntimeReservationDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile(localDockerRuntimeReservationName("rt-bad"), []byte("{\"schemaVersion\":1,\"workspaceId\":\"ws\",\"resourceId\":\"rt-bad\",\"packageId\":\"basic\",\"nanoCpus\":1,\"memoryBytes\":1} {}"), 0400); err != nil {
		t.Fatal(err)
	}
	if _, err := readLocalDockerRuntimeReservation(root, localDockerRuntimeReservationName("rt-bad")); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}
