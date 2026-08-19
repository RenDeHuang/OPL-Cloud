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
	if _, err := provider.MonthlyPreflight(context.Background(), MonthlyPreflightInput{ResourceType: "compute", PackageID: "basic", Zone: "local"}); !errors.Is(err, ErrProviderPlanUnavailable) {
		t.Fatalf("missing profile monthly preflight err=%v", err)
	}
}

func TestLocalDockerMonthlyPreflightRequiresAvailableProfilePackage(t *testing.T) {
	profile := []byte(`{"schemaVersion":1,"packages":[{"id":"basic","name":"Small Workspace","available":false,"compute":{"id":"configured-small","server":"1c2g","cpu":1,"memoryGb":2,"diskGb":10,"instanceType":"configured-1c2g"},"storage":{"sizeGb":10,"quotaPolicy":"linux-project"}}]}`)
	provider := newLocalDockerProvider(LocalDockerProviderConfig{ProviderProfileJSON: profile}, &recordingDockerRunner{})
	if _, err := provider.MonthlyPreflight(context.Background(), MonthlyPreflightInput{ResourceType: "compute", PackageID: "basic", Zone: "local"}); !errors.Is(err, ErrProviderPlanUnavailable) {
		t.Fatalf("unavailable package monthly preflight err=%v", err)
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

func TestLocalDockerProviderProfileControlsBasicAndProAvailability(t *testing.T) {
	for _, tc := range []struct {
		name           string
		basicAvailable bool
		proAvailable   bool
	}{
		{name: "Basic only", basicAvailable: true},
		{name: "Pro only", proAvailable: true},
		{name: "Basic and Pro", basicAvailable: true, proAvailable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile, err := json.Marshal(localDockerProviderProfile{
				SchemaVersion: 1,
				Packages: []localDockerPackageProfile{
					{ID: "basic", Name: "Basic Workspace", Available: tc.basicAvailable, Compute: ComputePlan{ID: "local-basic", Server: "2c4g", CPU: 2, MemoryGB: 4, DiskGB: 10, InstanceType: "local-2c4g"}, Storage: localDockerStoragePlan{SizeGB: 10, QuotaPolicy: "linux-project"}},
					{ID: "pro", Name: "Pro Workspace", Available: tc.proAvailable, Compute: ComputePlan{ID: "local-pro", Server: "8c16g", CPU: 8, MemoryGB: 16, DiskGB: 100, InstanceType: "local-8c16g"}, Storage: localDockerStoragePlan{SizeGB: 100, QuotaPolicy: "linux-project"}},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			provider := newLocalDockerProvider(LocalDockerProviderConfig{ProviderProfileJSON: profile}, &recordingDockerRunner{})
			descriptor := provider.Descriptor()
			if len(descriptor.Catalog.WorkspacePackages) != 2 {
				t.Fatalf("catalog packages=%#v", descriptor.Catalog.WorkspacePackages)
			}
			for index, packageID := range []string{"basic", "pro"} {
				wantAvailable := map[string]bool{"basic": tc.basicAvailable, "pro": tc.proAvailable}[packageID]
				catalogPackage := descriptor.Catalog.WorkspacePackages[index]
				if catalogPackage.ID != packageID || catalogPackage.Available != wantAvailable {
					t.Fatalf("%s catalog package=%#v wantAvailable=%v", packageID, catalogPackage, wantAvailable)
				}
				_, planAvailable := descriptor.Plans[packageID]
				if planAvailable != wantAvailable {
					t.Fatalf("%s descriptor plan available=%v want=%v", packageID, planAvailable, wantAvailable)
				}
				sizeGB := map[string]int{"basic": 10, "pro": 100}[packageID]
				_, resolveErr := provider.ResolveWorkspacePlan(context.Background(), WorkspaceLaunchPlanInput{PackageID: packageID, SizeGB: sizeGB})
				if wantAvailable && resolveErr != nil || !wantAvailable && !errors.Is(resolveErr, ErrProviderPlanUnavailable) {
					t.Fatalf("%s resolve error=%v wantAvailable=%v", packageID, resolveErr, wantAvailable)
				}
			}
		})
	}
}

type localDockerCapacityRunner struct {
	info                        []byte
	container                   dockerContainerInspect
	excludeFromRuntimeInventory bool
}

func (r *localDockerCapacityRunner) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	switch {
	case len(args) == 3 && args[0] == "info":
		return append([]byte(nil), r.info...), nil
	case len(args) == 8 && args[0] == "container" && args[1] == "ls" && args[5] == "label=opl.fabric.kind=runtime":
		if r.container.ID == "" || r.excludeFromRuntimeInventory {
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

func TestLocalDockerRuntimeCapacityUsesDurableReservationAcrossProfileDrift(t *testing.T) {
	rootPath := localDockerStorageTestRoot(t)
	workspaceID := "ws-existing"
	resourceID := localRuntimeID(workspaceID)
	runner := &localDockerCapacityRunner{info: []byte("{\"NCPU\":8,\"MemTotal\":17179869184}")}
	runner.container.ID, runner.container.Name = "container-existing", localRuntimeName(workspaceID)
	runner.container.Config.Labels = map[string]string{
		"opl.fabric.provider": "local-docker", "opl.fabric.kind": "runtime", "opl.workspace.id": workspaceID,
		"opl.resource.id": resourceID, localDockerComputePackageLabel: "basic",
	}
	runner.container.HostConfig.NanoCPUs, runner.container.HostConfig.Memory, runner.container.HostConfig.MemorySwap = 2_000_000_000, 4_294_967_296, 4_294_967_296
	profile := []byte(`{"schemaVersion":1,"packages":[{"id":"basic","name":"Current Basic","available":true,"compute":{"id":"current-basic","server":"4c8g","cpu":4,"memoryGb":8,"instanceType":"current-4c8g"},"storage":{"sizeGb":10,"quotaPolicy":"linux-project"}}]}`)
	provider := newLocalDockerProvider(LocalDockerProviderConfig{HostStorageRoot: rootPath, ProviderProfileJSON: profile}, runner)
	root, err := provider.openStorageRoot()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	existing := localDockerRuntimeReservation{SchemaVersion: 1, WorkspaceID: workspaceID, ResourceID: resourceID, PackageID: "basic", NanoCPUs: 2_000_000_000, MemoryBytes: 4_294_967_296}
	if err := writeLocalDockerRuntimeReservation(root, existing); err != nil {
		t.Fatal(err)
	}
	requested := localDockerRuntimeReservation{SchemaVersion: 1, WorkspaceID: "ws-new", ResourceID: localRuntimeID("ws-new"), PackageID: "basic", NanoCPUs: 4_000_000_000, MemoryBytes: 8_589_934_592}
	if err := provider.localDockerRuntimeCapacityAdmission(context.Background(), root, requested); err != nil {
		t.Fatalf("profile drift rejected immutable reservation: %v", err)
	}
	stored, err := readLocalDockerRuntimeReservation(root, localDockerRuntimeReservationName(resourceID))
	if err != nil || stored != existing {
		t.Fatalf("stored reservation=%#v err=%v", stored, err)
	}
	runner.container.HostConfig.NanoCPUs, runner.container.HostConfig.Memory, runner.container.HostConfig.MemorySwap = 4_000_000_000, 8_589_934_592, 8_589_934_592
	if err := provider.localDockerRuntimeCapacityAdmission(context.Background(), root, requested); err == nil || err.Error() != localDockerRuntimeReservationInventoryError {
		t.Fatalf("live limits replaced immutable reservation: %v", err)
	}
}

func TestLocalDockerRuntimeCapacityRecoversMissingReservationFromLiveLimits(t *testing.T) {
	rootPath := localDockerStorageTestRoot(t)
	workspaceID := "ws-existing"
	resourceID := localRuntimeID(workspaceID)
	runner := &localDockerCapacityRunner{info: []byte("{\"NCPU\":8,\"MemTotal\":17179869184}")}
	runner.container.ID, runner.container.Name = "container-existing", localRuntimeName(workspaceID)
	runner.container.Config.Labels = map[string]string{
		"opl.fabric.provider": "local-docker", "opl.fabric.kind": "runtime", "opl.workspace.id": workspaceID,
		"opl.resource.id": resourceID, localDockerComputePackageLabel: "retired-package",
	}
	runner.container.HostConfig.NanoCPUs, runner.container.HostConfig.Memory, runner.container.HostConfig.MemorySwap = 2_000_000_000, 4_294_967_296, 4_294_967_296
	provider := newLocalDockerProvider(LocalDockerProviderConfig{HostStorageRoot: rootPath}, runner)
	root, err := provider.openStorageRoot()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	requested := localDockerRuntimeReservation{SchemaVersion: 1, WorkspaceID: "ws-new", ResourceID: localRuntimeID("ws-new"), PackageID: "basic", NanoCPUs: 2_000_000_000, MemoryBytes: 4_294_967_296}
	if err := provider.localDockerRuntimeCapacityAdmission(context.Background(), root, requested); err != nil {
		t.Fatalf("exact live reservation recovery failed: %v", err)
	}
	want := localDockerRuntimeReservation{SchemaVersion: 1, WorkspaceID: workspaceID, ResourceID: resourceID, PackageID: "retired-package", NanoCPUs: 2_000_000_000, MemoryBytes: 4_294_967_296}
	stored, err := readLocalDockerRuntimeReservation(root, localDockerRuntimeReservationName(resourceID))
	if err != nil || stored != want {
		t.Fatalf("recovered reservation=%#v want=%#v err=%v", stored, want, err)
	}
}

func TestLocalDockerRuntimeCapacityRejectsUnboundedLegacyContainer(t *testing.T) {
	rootPath := localDockerStorageTestRoot(t)
	workspaceID := "ws-legacy"
	resourceID := localRuntimeID(workspaceID)
	runner := &localDockerCapacityRunner{info: []byte("{\"NCPU\":8,\"MemTotal\":17179869184}")}
	runner.container.ID, runner.container.Name = "container-legacy", localRuntimeName(workspaceID)
	runner.container.Config.Labels = map[string]string{
		"opl.fabric.provider": "local-docker", "opl.fabric.kind": "runtime", "opl.workspace.id": workspaceID,
		"opl.resource.id": resourceID, localDockerComputePackageLabel: "basic",
	}
	provider := newLocalDockerProvider(LocalDockerProviderConfig{HostStorageRoot: rootPath}, runner)
	root, err := provider.openStorageRoot()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	requested := localDockerRuntimeReservation{SchemaVersion: 1, WorkspaceID: "ws-new", ResourceID: localRuntimeID("ws-new"), PackageID: "basic", NanoCPUs: 2_000_000_000, MemoryBytes: 4_294_967_296}
	err = provider.localDockerRuntimeCapacityAdmission(context.Background(), root, requested)
	if err == nil || err.Error() != "local_docker_runtime_reservation_inventory_invalid" {
		t.Fatalf("unbounded legacy runtime err=%v", err)
	}
	if _, err := readLocalDockerRuntimeReservation(root, localDockerRuntimeReservationName(resourceID)); !errors.Is(err, ErrWorkspaceLaunchResourceAbsent) {
		t.Fatalf("unbounded legacy runtime created reservation: %v", err)
	}
}

func TestLocalDockerRuntimeCapacityRejectsNonCanonicalContainerName(t *testing.T) {
	rootPath := localDockerStorageTestRoot(t)
	workspaceID := "ws-existing"
	resourceID := localRuntimeID(workspaceID)
	runner := &localDockerCapacityRunner{info: []byte("{\"NCPU\":8,\"MemTotal\":17179869184}")}
	runner.container.ID, runner.container.Name = "container-existing", "foreign-runtime-name"
	runner.container.Config.Labels = map[string]string{
		"opl.fabric.provider": "local-docker", "opl.fabric.kind": "runtime", "opl.workspace.id": workspaceID,
		"opl.resource.id": resourceID, localDockerComputePackageLabel: "basic",
	}
	runner.container.HostConfig.NanoCPUs, runner.container.HostConfig.Memory, runner.container.HostConfig.MemorySwap = 2_000_000_000, 4_294_967_296, 4_294_967_296
	provider := newLocalDockerProvider(LocalDockerProviderConfig{HostStorageRoot: rootPath}, runner)
	root, err := provider.openStorageRoot()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	requested := localDockerRuntimeReservation{SchemaVersion: 1, WorkspaceID: "ws-new", ResourceID: localRuntimeID("ws-new"), PackageID: "basic", NanoCPUs: 2_000_000_000, MemoryBytes: 4_294_967_296}
	err = provider.localDockerRuntimeCapacityAdmission(context.Background(), root, requested)
	if err == nil || err.Error() != "local_docker_runtime_reservation_inventory_invalid" {
		t.Fatalf("non-canonical runtime err=%v", err)
	}
	if _, err := readLocalDockerRuntimeReservation(root, localDockerRuntimeReservationName(resourceID)); !errors.Is(err, ErrWorkspaceLaunchResourceAbsent) {
		t.Fatalf("non-canonical runtime created reservation: %v", err)
	}
}

func TestLocalDockerRuntimeCapacityRejectsStaleReservationBoundToUnmanagedContainer(t *testing.T) {
	rootPath := localDockerStorageTestRoot(t)
	workspaceID := "ws-existing"
	resourceID := localRuntimeID(workspaceID)
	runner := &localDockerCapacityRunner{info: []byte("{\"NCPU\":8,\"MemTotal\":17179869184}"), excludeFromRuntimeInventory: true}
	runner.container.ID, runner.container.Name = "container-existing", localRuntimeName(workspaceID)
	runner.container.Config.Labels = map[string]string{"opl.workspace.id": workspaceID, "opl.resource.id": resourceID, localDockerComputePackageLabel: "basic"}
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
	requested := localDockerRuntimeReservation{SchemaVersion: 1, WorkspaceID: "ws-new", ResourceID: localRuntimeID("ws-new"), PackageID: "basic", NanoCPUs: 2_000_000_000, MemoryBytes: 4_294_967_296}
	err = provider.localDockerRuntimeCapacityAdmission(context.Background(), root, requested)
	if err == nil || err.Error() != "local_docker_runtime_reservation_inventory_invalid" {
		t.Fatalf("unmanaged canonical runtime err=%v", err)
	}
	stored, err := readLocalDockerRuntimeReservation(root, localDockerRuntimeReservationName(resourceID))
	if err != nil || stored != existing {
		t.Fatalf("unmanaged runtime reservation=%#v err=%v", stored, err)
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
