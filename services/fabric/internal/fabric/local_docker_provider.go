package fabric

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

const defaultLocalDockerWorkspaceImageRepository = "ghcr.io/gaofeng21cn/one-person-lab-webui"

type dockerRunner interface {
	Run(context.Context, []byte, ...string) ([]byte, error)
}

type execDockerRunner struct{ binary string }

func (r execDockerRunner) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, r.binary, args...)
	if stdin != nil {
		command.Stdin = bytes.NewReader(stdin)
	}
	output, err := command.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("docker_%s_failed: %v: %s", firstNonEmpty(firstArg(args), "command"), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func firstArg(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

type LocalDockerProviderConfig struct {
	DockerBinary                 string
	GatewaySecretRoot            string
	HostStorageRoot              string
	RuntimeHost                  string
	RuntimeGatewayContainer      string
	ConfigureRuntimeGateway      bool
	StorageQuotaBackend          localDockerProjectQuota
	TrustedWorkspaceImageSources []string
}

type LocalDockerProvider struct {
	runner                            dockerRunner
	gatewaySecretRoot                 string
	gatewaySecretRootErr              error
	hostStorageRoot                   string
	hostStorageRootErr                error
	runtimeHost                       string
	runtimeGatewayContainer           string
	configureRuntimeGatewayEnabled    bool
	trustedWorkspaceImageRepositories map[string]struct{}
	trustedWorkspaceImageReferences   map[string]struct{}
	now                               func() time.Time
	storageQuota                      localDockerProjectQuota
}

func NewLocalDockerProvider() *LocalDockerProvider {
	trustedSources := []string{defaultLocalDockerWorkspaceImageRepository}
	if raw, configured := os.LookupEnv("OPL_FABRIC_LOCAL_DOCKER_TRUSTED_WORKSPACE_IMAGES"); configured {
		trustedSources = strings.Split(raw, ",")
	}
	return newLocalDockerProvider(LocalDockerProviderConfig{
		DockerBinary:                 firstNonEmpty(strings.TrimSpace(os.Getenv("OPL_FABRIC_DOCKER_BINARY")), "docker"),
		GatewaySecretRoot:            strings.TrimSpace(os.Getenv("OPL_FABRIC_LOCAL_DOCKER_SECRET_ROOT")),
		HostStorageRoot:              strings.TrimSpace(os.Getenv("OPL_FABRIC_LOCAL_DOCKER_STORAGE_ROOT")),
		RuntimeHost:                  firstNonEmpty(strings.TrimSpace(os.Getenv("OPL_FABRIC_LOCAL_DOCKER_HOST")), "127.0.0.1"),
		RuntimeGatewayContainer:      strings.TrimSpace(os.Getenv("OPL_FABRIC_LOCAL_DOCKER_GATEWAY_CONTAINER")),
		ConfigureRuntimeGateway:      true,
		TrustedWorkspaceImageSources: trustedSources,
	}, nil)
}

func newLocalDockerProvider(config LocalDockerProviderConfig, runner dockerRunner) *LocalDockerProvider {
	if runner == nil {
		runner = execDockerRunner{binary: firstNonEmpty(strings.TrimSpace(config.DockerBinary), "docker")}
	}
	trustedSources := config.TrustedWorkspaceImageSources
	if trustedSources == nil {
		trustedSources = []string{defaultLocalDockerWorkspaceImageRepository}
	}
	trustedRepositories, trustedReferences := localDockerWorkspaceImageTrust(trustedSources)
	secretRoot := strings.TrimSpace(config.GatewaySecretRoot)
	secretRootErr := validateLocalDockerGatewaySecretRoot(secretRoot)
	storageRoot := strings.TrimSpace(config.HostStorageRoot)
	storageRootErr := validateLocalDockerStorageRoot(storageRoot)
	storageQuota := config.StorageQuotaBackend
	if storageQuota == nil {
		storageQuota = newLocalDockerProjectQuota(storageRoot)
	}
	return &LocalDockerProvider{
		runner: runner, gatewaySecretRoot: secretRoot, gatewaySecretRootErr: secretRootErr,
		hostStorageRoot: storageRoot, hostStorageRootErr: storageRootErr,
		storageQuota:                      storageQuota,
		runtimeHost:                       firstNonEmpty(strings.TrimSpace(config.RuntimeHost), "127.0.0.1"),
		runtimeGatewayContainer:           strings.TrimSpace(config.RuntimeGatewayContainer),
		configureRuntimeGatewayEnabled:    config.ConfigureRuntimeGateway,
		trustedWorkspaceImageRepositories: trustedRepositories, trustedWorkspaceImageReferences: trustedReferences,
		now: func() time.Time { return time.Now().UTC() },
	}
}

func immutableLocalDockerImage(value string) (repository, reference string, ok bool) {
	value = strings.TrimSpace(value)
	digest := ""
	if strings.HasPrefix(value, "sha256:") {
		digest = strings.TrimPrefix(value, "sha256:")
	} else {
		before, after, found := strings.Cut(value, "@sha256:")
		if !found || before == "" || strings.Contains(before, "@") {
			return "", "", false
		}
		repository, digest = before, after
	}
	if len(digest) != 64 || digest != strings.ToLower(digest) || !validDigest(digest) {
		return "", "", false
	}
	return repository, value, true
}

func localDockerWorkspaceImageTrust(sources []string) (map[string]struct{}, map[string]struct{}) {
	repositories, references := map[string]struct{}{}, map[string]struct{}{}
	for _, raw := range sources {
		source := strings.TrimSpace(raw)
		if source == "" {
			continue
		}
		if _, reference, ok := immutableLocalDockerImage(source); ok {
			references[reference] = struct{}{}
			continue
		}
		lastComponent := source[strings.LastIndex(source, "/")+1:]
		if source != strings.ToLower(source) || strings.ContainsAny(source, "@ \t\r\n") || strings.Contains(lastComponent, ":") || strings.HasSuffix(source, "/") {
			continue
		}
		repositories[source] = struct{}{}
	}
	return repositories, references
}

func (*LocalDockerProvider) Descriptor() ProviderDescriptor {
	basic := ComputePlan{ID: "local-basic-2c4g", Server: "2c4g", CPU: 2, MemoryGB: 4, DiskGB: 10, InstanceType: "local-2c4g"}
	pro := ComputePlan{ID: "local-pro-8c16g", Server: "8c16g", CPU: 8, MemoryGB: 16, DiskGB: 100, InstanceType: "local-8c16g"}
	return ProviderDescriptor{
		Name: "local-docker", Plans: map[string]ComputePlan{"basic": basic, "pro": pro},
		DefaultComputePoolIDs: map[string]string{"basic": "local-docker", "pro": "local-docker"},
		Catalog: Catalog{
			SchemaVersion: 1, Owner: "OPL Fabric",
			WorkspacePackages: []WorkspacePackage{
				{ID: "basic", Name: "Basic Workspace", ComputeProfileID: "cpu-basic", CPU: 2, MemoryGB: 4, DiskGB: 10, Provider: "local-docker", Available: true},
				{ID: "pro", Name: "Pro Workspace", ComputeProfileID: "cpu-pro", CPU: 8, MemoryGB: 16, DiskGB: 100, Provider: "local-docker", Available: true},
			},
			StorageClasses: []StorageClass{{ID: "workspace-local", StorageClassName: "host-directory", Provider: "local-docker", Available: true}},
			IngressDomains: []IngressDomain{{ID: "workspace", Host: "127.0.0.1", PathPattern: "/", Available: true}},
		},
	}
}

func (p *LocalDockerProvider) ResolveWorkspacePlan(_ context.Context, input WorkspaceLaunchPlanInput) (json.RawMessage, error) {
	plan, ok := p.Descriptor().Plans[input.PackageID]
	if !ok || plan.ID == "" || input.SizeGB < 10 || input.SizeGB%10 != 0 {
		return nil, ErrProviderPlanUnavailable
	}
	return json.Marshal(map[string]any{
		"compute": plan,
		"storage": map[string]any{"sizeGb": input.SizeGB},
	})
}

func (p *LocalDockerProvider) ValidateWorkspaceImageReference(value string) bool {
	repository, reference, ok := immutableLocalDockerImage(value)
	if !ok {
		return false
	}
	if _, trusted := p.trustedWorkspaceImageReferences[reference]; trusted {
		return true
	}
	_, trusted := p.trustedWorkspaceImageRepositories[repository]
	return repository != "" && trusted
}

func (*LocalDockerProvider) ValidateComputeAllocation(allocation ComputeAllocation, prepared ComputeAllocationPreparation) error {
	if allocation.Provider != "local-docker" || allocation.PoolID != prepared.PoolID || allocation.NodePoolID != prepared.NodePoolID ||
		allocation.PackageID != prepared.PackageID || allocation.InstanceType != prepared.InstanceType ||
		!strings.HasPrefix(allocation.ProviderResourceID, "network/") || allocation.Zone != "local" || allocation.ChargeType != "LOCAL" {
		return fmt.Errorf("compute_provider_readback_mismatch")
	}
	return nil
}

func (p *LocalDockerProvider) MonthlyPreflight(ctx context.Context, input MonthlyPreflightInput) (MonthlyPreflight, error) {
	if _, err := p.runner.Run(ctx, nil, "info", "--format", "{{.ServerVersion}}"); err != nil {
		return MonthlyPreflight{}, err
	}
	requestIDs := map[string]string{"nodePool": "local-docker", "subnets": "local-bridge", "availability": "docker-engine", "quota": "local-host"}
	if input.ResourceType == "storage" {
		if err := p.storageQuota.Preflight(p.hostStorageRoot); err != nil {
			return MonthlyPreflight{}, err
		}
		if err := p.prepareStorageRoot(); err != nil {
			return MonthlyPreflight{}, err
		}
		if err := p.preflightStorageCapacity(input.SizeGB); err != nil {
			return MonthlyPreflight{}, err
		}
		requestIDs = map[string]string{"quota": "local-host", "price": "not-applicable"}
	}
	return MonthlyPreflight{
		ResourceType: input.ResourceType, PackageID: input.PackageID, NodePoolID: "local-docker", SizeGB: input.SizeGB, Zone: input.Zone,
		Available: true, ChargeType: "LOCAL", RenewFlag: "NOT_APPLICABLE", ProviderRequestIDs: requestIDs,
	}, nil
}

func (p *LocalDockerProvider) PrepareComputeAllocation(ctx context.Context, input ComputeAllocationInput) (ComputeAllocationPreparation, error) {
	plan, ok := providerPlan(p, input.PackageID)
	if !ok {
		return ComputeAllocationPreparation{}, ErrUnsupportedComputePackage
	}
	if input.NodePoolID != "local-docker" {
		return ComputeAllocationPreparation{}, fmt.Errorf("local_docker_compute_pool_mismatch")
	}
	if _, err := p.runner.Run(ctx, nil, "info", "--format", "{{.ServerVersion}}"); err != nil {
		return ComputeAllocationPreparation{}, err
	}
	return ComputeAllocationPreparation{
		PoolID: plan.ID, PackageID: input.PackageID, NodePoolID: input.NodePoolID, InstanceType: plan.InstanceType,
		MaxReplicas: 1024, BaselineReplicas: 0, TargetReplicas: 1, ProviderRequestID: providerRequestID("docker-prepare", input.ID),
	}, nil
}

type dockerNetworkInspect struct {
	ID     string            `json:"Id"`
	Name   string            `json:"Name"`
	Labels map[string]string `json:"Labels"`
}

type dockerVolumeInspect struct {
	Name   string            `json:"Name"`
	Labels map[string]string `json:"Labels"`
}

type dockerObjectInventoryRow struct {
	ID    string `json:"ID"`
	Name  string `json:"Name"`
	Names string `json:"Names"`
}

func decodeDockerObjectInventory(output []byte, objectName string) (dockerObjectInventoryRow, bool, error) {
	rows := make([]dockerObjectInventoryRow, 0, 1)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row dockerObjectInventoryRow
		if json.Unmarshal([]byte(line), &row) != nil {
			return dockerObjectInventoryRow{}, false, fmt.Errorf("local_docker_inventory_readback_invalid")
		}
		if row.Name != "" && row.Names != "" && row.Name != row.Names {
			return dockerObjectInventoryRow{}, false, fmt.Errorf("local_docker_inventory_identity_conflict")
		}
		if firstNonEmpty(row.Name, row.Names) != objectName {
			return dockerObjectInventoryRow{}, false, fmt.Errorf("local_docker_inventory_identity_conflict")
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return dockerObjectInventoryRow{}, false, nil
	}
	if len(rows) != 1 || firstNonEmpty(rows[0].Name, rows[0].Names) != objectName {
		return dockerObjectInventoryRow{}, false, fmt.Errorf("local_docker_inventory_identity_conflict")
	}
	return rows[0], true, nil
}

func (p *LocalDockerProvider) inspectNetwork(ctx context.Context, name string) (dockerNetworkInspect, bool, error) {
	inventory, err := p.runner.Run(ctx, nil, "network", "ls", "--no-trunc", "--filter", "name=^"+name+"$", "--format", "{{json .}}")
	if err != nil {
		return dockerNetworkInspect{}, false, err
	}
	row, exists, err := decodeDockerObjectInventory(inventory, name)
	if err != nil || !exists {
		return dockerNetworkInspect{}, false, err
	}
	output, err := p.runner.Run(ctx, nil, "network", "inspect", firstNonEmpty(row.ID, name))
	if err != nil {
		return dockerNetworkInspect{}, false, err
	}
	var values []dockerNetworkInspect
	if json.Unmarshal(output, &values) != nil || len(values) != 1 || values[0].ID == "" || values[0].Name != name || row.ID != "" && values[0].ID != row.ID {
		return dockerNetworkInspect{}, false, fmt.Errorf("local_docker_network_readback_invalid")
	}
	return values[0], true, nil
}

func localDockerLabels(accountID, workspaceID, resourceID, operationID, kind string) map[string]string {
	return map[string]string{
		"opl.fabric.provider": "local-docker", "opl.fabric.kind": kind, "opl.account.id": accountID,
		"opl.workspace.id": workspaceID, "opl.resource.id": resourceID, "opl.operation.id": operationID,
	}
}

func dockerLabelArgs(labels map[string]string) []string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	args := make([]string, 0, len(labels)*2)
	for _, key := range keys {
		if value := labels[key]; value != "" {
			args = append(args, "--label", key+"="+value)
		}
	}
	return args
}

func exactDockerLabels(actual, expected map[string]string) bool {
	for key, value := range expected {
		if value != "" && actual[key] != value {
			return false
		}
	}
	return true
}

func localDockerName(prefix, id string) string {
	return prefix + "-" + stableSuffix(id)[:16]
}

func (p *LocalDockerProvider) CreateComputeAllocation(ctx context.Context, input ComputeAllocationExecution) (ComputeAllocation, error) {
	allocation, prepared := input.Allocation, input.Plan
	name := localDockerName("opl-compute", allocation.ID)
	labels := localDockerLabels(allocation.AccountID, allocation.WorkspaceID, allocation.ID, allocation.OperationID, "compute")
	if input.DryRun {
		return p.localComputeAllocation(allocation, prepared, "dry-run"), nil
	}
	attempt, err := beginProviderMutation(ctx, "local_docker_network_create", "compute_allocation", allocation.ID, name)
	if err != nil {
		return ComputeAllocation{}, err
	}
	readback, exists, err := p.inspectNetwork(ctx, name)
	if err != nil {
		return ComputeAllocation{}, err
	}
	if !exists {
		if attempt != nil && !attempt.Fresh {
			claimed, claimErr := attempt.claimReplay(ctx)
			if claimErr != nil || !claimed {
				return ComputeAllocation{}, firstNonNil(claimErr, ErrWorkspaceLaunchPending)
			}
			readback, exists, err = p.inspectNetwork(ctx, name)
			if err != nil {
				_ = attempt.complete(ctx, "", allocation, err)
				return ComputeAllocation{}, err
			}
		}
		if !exists {
			if dispatchErr := attempt.markReplayDispatch(ctx); dispatchErr != nil {
				return ComputeAllocation{}, dispatchErr
			}
			args := append([]string{"network", "create", "--driver", "bridge"}, dockerLabelArgs(labels)...)
			args = append(args, name)
			if _, err := p.runner.Run(ctx, nil, args...); err != nil {
				_ = attempt.complete(ctx, "", allocation, err)
				return ComputeAllocation{}, err
			}
			readback, exists, err = p.inspectNetwork(ctx, name)
		}
	}
	if err != nil || !exists || !exactDockerLabels(readback.Labels, labels) {
		readErr := fmt.Errorf("local_docker_compute_readback_mismatch")
		_ = attempt.complete(ctx, "", allocation, readErr)
		return ComputeAllocation{}, readErr
	}
	resource := p.localComputeAllocation(allocation, prepared, readback.ID)
	if completeErr := attempt.complete(ctx, resource.ProviderRequestID, resource, nil); completeErr != nil {
		return ComputeAllocation{}, completeErr
	}
	return resource, nil
}

func (p *LocalDockerProvider) localComputeAllocation(allocation ComputeAllocation, prepared ComputeAllocationPreparation, networkID string) ComputeAllocation {
	deadline := p.now().AddDate(0, 1, 0).Format(time.RFC3339)
	return ComputeAllocation{
		ID: allocation.ID, OperationID: allocation.OperationID, AccountID: allocation.AccountID, WorkspaceID: allocation.WorkspaceID,
		PackageID: allocation.PackageID, Status: "running", Provider: "local-docker", ProviderResourceID: "network/" + networkID,
		ProviderRequestID: providerRequestID("docker-compute", allocation.ID), PoolID: prepared.PoolID, NodePoolID: prepared.NodePoolID,
		InstanceType: prepared.InstanceType, Zone: "local", ChargeType: "LOCAL", RenewFlag: "NOT_APPLICABLE", Deadline: deadline,
		CreatedAt: p.now(),
	}
}

func (p *LocalDockerProvider) TagComputeMachine(ctx context.Context, _ ProviderMachine, ownership MachineOwnership) error {
	readback, exists, err := p.inspectNetwork(ctx, localDockerName("opl-compute", ownership.ResourceID))
	if err != nil || !exists {
		return firstNonNil(err, fmt.Errorf("local_docker_compute_not_found"))
	}
	expected := localDockerLabels(ownership.AccountID, ownership.WorkspaceID, ownership.ResourceID, "", "compute")
	if !exactDockerLabels(readback.Labels, expected) {
		return fmt.Errorf("local_docker_compute_ownership_mismatch")
	}
	return nil
}

func firstNonNil(values ...error) error {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func (p *LocalDockerProvider) ReadComputeAllocation(ctx context.Context, allocation ComputeAllocation) (ComputeAllocation, error) {
	name := localDockerName("opl-compute", allocation.ID)
	readback, exists, err := p.inspectNetwork(ctx, name)
	if err != nil {
		return allocation, err
	}
	if !exists {
		allocation.Status = "external_deleted"
		return allocation, fmt.Errorf("local_docker_compute_not_found")
	}
	expected := localDockerLabels(allocation.AccountID, allocation.WorkspaceID, allocation.ID, "", "compute")
	if !exactDockerLabels(readback.Labels, expected) {
		return allocation, fmt.Errorf("local_docker_compute_ownership_mismatch")
	}
	allocation.Status, allocation.Provider, allocation.ProviderResourceID = "running", "local-docker", "network/"+readback.ID
	allocation.ProviderRequestID = providerRequestID("docker-compute-read", allocation.ID)
	allocation.Zone = "local"
	return allocation, nil
}

func (p *LocalDockerProvider) ReadComputeProviderFacts(ctx context.Context, allocation ComputeAllocation) (ProviderResourceFacts, error) {
	readback, err := p.ReadComputeAllocation(ctx, allocation)
	if err != nil {
		return ProviderResourceFacts{}, err
	}
	return ProviderResourceFacts{
		PackageOrSpec: readback.InstanceType,
		ProviderID:    readback.ProviderResourceID,
		Zone:          readback.Zone,
		Status:        readback.Status,
		ExpiresAt:     readback.Deadline,
	}, nil
}

func (p *LocalDockerProvider) SyncComputeAllocation(ctx context.Context, allocation ComputeAllocation) (ComputeAllocation, error) {
	return p.ReadComputeAllocation(ctx, allocation)
}

func (p *LocalDockerProvider) RenewComputeAllocation(ctx context.Context, allocation ComputeAllocation) (ComputeAllocation, error) {
	readback, err := p.ReadComputeAllocation(ctx, allocation)
	if err != nil {
		return readback, err
	}
	readback.Deadline = p.now().AddDate(0, 1, 0).Format(time.RFC3339)
	return readback, nil
}

func (p *LocalDockerProvider) DestroyComputeAllocation(ctx context.Context, allocation ComputeAllocation) (ComputeAllocation, error) {
	name := localDockerName("opl-compute", allocation.ID)
	network, exists, err := p.inspectNetwork(ctx, name)
	if err != nil {
		return allocation, err
	}
	if exists {
		expected := localDockerLabels(allocation.AccountID, allocation.WorkspaceID, allocation.ID, "", "compute")
		if !exactDockerLabels(network.Labels, expected) {
			return allocation, fmt.Errorf("local_docker_compute_ownership_mismatch")
		}
		if p.runtimeGatewayContainer != "" {
			gateway, gatewayExists, inspectErr := p.inspectContainer(ctx, p.runtimeGatewayContainer)
			if inspectErr != nil || !gatewayExists || gateway.Config.Labels["opl.fabric.local-docker.gateway"] != "control-plane" {
				return allocation, firstNonNil(inspectErr, fmt.Errorf("local_docker_runtime_gateway_identity_mismatch"))
			}
			bound, bindingErr := exactContainerNetworkMembership(gateway, name, network.ID)
			if bindingErr != nil {
				return allocation, bindingErr
			}
			if bound {
				_, disconnectErr := p.runner.Run(ctx, nil, "network", "disconnect", name, p.runtimeGatewayContainer)
				gateway, gatewayExists, inspectErr = p.inspectContainer(ctx, p.runtimeGatewayContainer)
				if inspectErr != nil || !gatewayExists {
					return allocation, firstNonNil(inspectErr, fmt.Errorf("local_docker_runtime_gateway_identity_mismatch"))
				}
				bound, bindingErr = exactContainerNetworkMembership(gateway, name, network.ID)
				if bindingErr != nil || bound {
					return allocation, firstNonNil(bindingErr, disconnectErr, fmt.Errorf("local_docker_runtime_gateway_network_readback_mismatch"))
				}
			}
		}
		_, removeErr := p.runner.Run(ctx, nil, "network", "rm", name)
		if _, stillExists, readErr := p.inspectNetwork(ctx, name); readErr != nil || stillExists {
			return allocation, firstNonNil(readErr, removeErr, fmt.Errorf("local_docker_compute_destroy_readback_mismatch"))
		}
	}
	allocation.Status, allocation.ProviderRequestID = "destroyed", providerRequestID("docker-compute-destroy", allocation.ID)
	return allocation, nil
}

func (p *LocalDockerProvider) CreateStorageVolume(ctx context.Context, input StorageVolumeInput) (StorageVolume, error) {
	paths, err := p.storagePaths(input.WorkspaceID)
	if err != nil {
		return StorageVolume{}, err
	}
	attempt, err := beginProviderMutation(ctx, "local_docker_storage_directory_create", "storage_volume", input.ID, paths.WorkspaceName)
	if err != nil {
		return StorageVolume{}, err
	}
	metadata := localDockerStorageMetadata{SchemaVersion: localDockerStorageMetadataSchemaVersion, StorageID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, SizeGB: input.SizeGB}
	if attempt != nil && !attempt.Fresh {
		if _, readErr := p.readStorageDirectories(StorageVolume{ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, SizeGB: input.SizeGB}); readErr != nil {
			claimed, claimErr := attempt.claimReplay(ctx)
			if claimErr != nil || !claimed {
				return StorageVolume{}, firstNonNil(claimErr, ErrWorkspaceLaunchPending)
			}
			if dispatchErr := attempt.markReplayDispatch(ctx); dispatchErr != nil {
				return StorageVolume{}, dispatchErr
			}
		}
	} else if attempt != nil {
		if dispatchErr := attempt.markReplayDispatch(ctx); dispatchErr != nil {
			return StorageVolume{}, dispatchErr
		}
	}
	if _, err := p.ensureStorageDirectories(metadata, input.SizeGB); err != nil {
		readErr := fmt.Errorf("local_docker_storage_readback_mismatch: %w", err)
		_ = attempt.complete(ctx, "", StorageVolume{ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID}, readErr)
		return StorageVolume{}, readErr
	}
	deadline := p.now().AddDate(0, 1, 0).Format(time.RFC3339)
	resource := StorageVolume{
		ID: input.ID, OperationID: input.IdempotencyKey, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "ready",
		Provider: "local-docker", ProviderResourceID: "directory/" + paths.WorkspaceName, ProviderRequestID: providerRequestID("docker-storage", input.ID),
		SizeGB: input.SizeGB, StorageClass: "host-directory", DiskType: "local-directory",
		RenewFlag: "NOT_APPLICABLE", Deadline: deadline, Zone: "local", CreatedAt: p.now(),
	}
	if completeErr := attempt.complete(ctx, resource.ProviderRequestID, resource, nil); completeErr != nil {
		return StorageVolume{}, completeErr
	}
	return resource, nil
}

func (p *LocalDockerProvider) ReadStorageVolume(ctx context.Context, volume StorageVolume) (StorageVolume, error) {
	paths, err := p.readStorageDirectories(volume)
	if errors.Is(err, ErrWorkspaceLaunchResourceAbsent) {
		volume.Status = "external_deleted"
		return volume, fmt.Errorf("local_docker_storage_not_found")
	}
	if err != nil {
		return volume, fmt.Errorf("local_docker_storage_ownership_mismatch: %w", err)
	}
	volume.Status, volume.Provider, volume.ProviderResourceID = "ready", "local-docker", "directory/"+paths.WorkspaceName
	volume.StorageClass, volume.DiskType = "host-directory", "local-directory"
	volume.ProviderRequestID = providerRequestID("docker-storage-read", volume.ID)
	return volume, nil
}

func (p *LocalDockerProvider) ReadStorageProviderFacts(ctx context.Context, volume StorageVolume) (ProviderResourceFacts, error) {
	readback, err := p.ReadStorageVolume(ctx, volume)
	if err != nil {
		return ProviderResourceFacts{}, err
	}
	return ProviderResourceFacts{
		PackageOrSpec: firstNonEmpty(readback.DiskType, readback.StorageClass),
		ProviderID:    readback.ProviderResourceID,
		Zone:          readback.Zone,
		Status:        readback.Status,
		ExpiresAt:     readback.Deadline,
	}, nil
}

func (p *LocalDockerProvider) ReadStorageVolumeStatus(ctx context.Context, volume StorageVolume) (StorageVolume, error) {
	return p.ReadStorageVolume(ctx, volume)
}

func (p *LocalDockerProvider) SyncStorageVolume(ctx context.Context, volume StorageVolume) (StorageVolume, error) {
	return p.ReadStorageVolume(ctx, volume)
}

func (p *LocalDockerProvider) RenewStorageVolume(ctx context.Context, volume StorageVolume) (StorageVolume, error) {
	readback, err := p.ReadStorageVolume(ctx, volume)
	if err != nil {
		return readback, err
	}
	readback.Deadline = p.now().AddDate(0, 1, 0).Format(time.RFC3339)
	return readback, nil
}

func (p *LocalDockerProvider) DestroyStorageVolume(ctx context.Context, volume StorageVolume) (StorageVolume, error) {
	paths, pathErr := p.storagePaths(volume.WorkspaceID)
	if pathErr != nil || volume.ID == "" || volume.AccountID == "" {
		return volume, firstNonNil(pathErr, fmt.Errorf("local_docker_storage_identity_invalid"))
	}
	if err := p.storageQuota.Preflight(p.hostStorageRoot); err != nil {
		return volume, err
	}
	if err := p.withStorageQuotaLock(func() error {
		root, err := p.openStorageRoot()
		if err != nil {
			return err
		}
		defer root.Close()
		deletionName := localDockerStorageDeletionName(paths.WorkspaceName)
		deletion, err := readLocalDockerStorageDeletion(root, deletionName)
		if err == nil {
			if deletion.StorageID != volume.ID || deletion.AccountID != volume.AccountID || deletion.WorkspaceID != volume.WorkspaceID || deletion.SizeGB != volume.SizeGB {
				return fmt.Errorf("local_docker_storage_destroy_ownership_mismatch")
			}
			return p.resumeStorageDeletionLocked(root, deletion)
		}
		if !errors.Is(err, ErrWorkspaceLaunchResourceAbsent) {
			return err
		}
		metadata, err := readLocalDockerStorageOwnerMetadata(root, paths)
		if errors.Is(err, ErrWorkspaceLaunchResourceAbsent) {
			return nil
		}
		if err != nil || metadata.SchemaVersion != localDockerStorageMetadataSchemaVersion || metadata.ProjectID == 0 || metadata.StorageID != volume.ID || metadata.AccountID != volume.AccountID || metadata.WorkspaceID != volume.WorkspaceID || metadata.SizeGB != volume.SizeGB {
			return firstNonNil(err, fmt.Errorf("local_docker_storage_destroy_ownership_mismatch"))
		}
		deletion = localDockerStorageDeletionFromMetadata(metadata, paths.WorkspaceName)
		if err := writeLocalDockerStorageDeletion(root, deletion); err != nil {
			return err
		}
		return p.resumeStorageDeletionLocked(root, deletion)
	}); err != nil {
		return volume, err
	}
	volume.Status, volume.ProviderRequestID = "destroyed", providerRequestID("docker-storage-destroy", volume.ID)
	return volume, nil
}

func (p *LocalDockerProvider) CreateStorageAttachment(ctx context.Context, input StorageAttachmentInput, compute ComputeAllocation, volume StorageVolume) (StorageAttachment, error) {
	if _, err := p.ReadComputeAllocation(ctx, compute); err != nil {
		return StorageAttachment{}, err
	}
	if _, err := p.ReadStorageVolume(ctx, volume); err != nil {
		return StorageAttachment{}, err
	}
	paths, err := p.storagePaths(volume.WorkspaceID)
	if err != nil {
		return StorageAttachment{}, err
	}
	id := "att_" + stableSuffix(input.IdempotencyKey)[:18]
	return StorageAttachment{
		ID: id, OperationID: input.IdempotencyKey, WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID,
		Status: "attached", Provider: "local-docker", ProviderAttachmentID: "docker/" + localDockerName("opl-compute", compute.ID) + "/" + paths.WorkspaceName,
		ProviderRequestID: providerRequestID("docker-attachment", input.IdempotencyKey), CreatedAt: p.now(),
	}, nil
}

func (p *LocalDockerProvider) ReadStorageAttachment(ctx context.Context, attachment StorageAttachment, compute ComputeAllocation, volume StorageVolume) (StorageAttachment, error) {
	if _, err := p.ReadComputeAllocation(ctx, compute); err != nil {
		return attachment, err
	}
	if _, err := p.ReadStorageVolume(ctx, volume); err != nil {
		return attachment, err
	}
	paths, err := p.storagePaths(volume.WorkspaceID)
	if err != nil {
		return attachment, err
	}
	attachment.Status, attachment.Provider = "attached", "local-docker"
	attachment.ProviderAttachmentID = "docker/" + localDockerName("opl-compute", compute.ID) + "/" + paths.WorkspaceName
	attachment.ProviderRequestID = providerRequestID("docker-attachment-read", attachment.OperationID)
	return attachment, nil
}

func (p *LocalDockerProvider) ReadStorageAttachmentProviderFacts(ctx context.Context, attachment StorageAttachment, compute ComputeAllocation, volume StorageVolume) (ProviderResourceFacts, error) {
	readback, err := p.ReadStorageAttachment(ctx, attachment, compute, volume)
	if err != nil {
		return ProviderResourceFacts{}, err
	}
	return ProviderResourceFacts{PackageOrSpec: "/data", ProviderID: readback.ProviderAttachmentID, Status: readback.Status}, nil
}

func (*LocalDockerProvider) DetachStorageAttachment(_ context.Context, attachment StorageAttachment) (StorageAttachment, error) {
	attachment.Status = "detached"
	attachment.ProviderRequestID = providerRequestID("docker-attachment-detach", attachment.OperationID)
	return attachment, nil
}

func (p *LocalDockerProvider) Readiness(ctx context.Context) (map[string]any, error) {
	if p.gatewaySecretRootErr != nil {
		return nil, p.gatewaySecretRootErr
	}
	if p.hostStorageRootErr != nil {
		return nil, p.hostStorageRootErr
	}
	if err := p.storageQuota.Preflight(p.hostStorageRoot); err != nil {
		return nil, err
	}
	if err := p.prepareStorageRoot(); err != nil {
		return nil, err
	}
	output, err := p.runner.Run(ctx, nil, "info", "--format", "{{.ServerVersion}}")
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": "ready", "provider": "local-docker", "dockerVersion": strings.TrimSpace(string(output))}, nil
}
