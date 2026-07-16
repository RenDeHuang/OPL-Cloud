package fabric

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"strings"
	"sync"
	"time"
)

const storageProvisionTimeout = 10 * time.Minute

type Provider interface {
	MonthlyPreflight(ctx context.Context, input MonthlyPreflightInput) (MonthlyPreflight, error)
	ReconcileComputePool(ctx context.Context, input ComputePoolDemand) (ComputePoolState, error)
	TagComputeMachine(ctx context.Context, machine ProviderMachine, ownership MachineOwnership) error
	DeleteComputeMachine(ctx context.Context, machine ProviderMachine, ownership MachineOwnership) error
	SyncComputeAllocation(ctx context.Context, allocation ComputeAllocation) (ComputeAllocation, error)
	RenewComputeAllocation(ctx context.Context, allocation ComputeAllocation) (ComputeAllocation, error)
	DestroyComputeAllocation(ctx context.Context, allocation ComputeAllocation) (ComputeAllocation, error)
	CreateStorageVolume(ctx context.Context, input StorageVolumeInput) (StorageVolume, error)
	SyncStorageVolume(ctx context.Context, volume StorageVolume) (StorageVolume, error)
	RenewStorageVolume(ctx context.Context, volume StorageVolume) (StorageVolume, error)
	DestroyStorageVolume(ctx context.Context, volume StorageVolume) (StorageVolume, error)
	CreateStorageSnapshot(ctx context.Context, input StorageSnapshotInput, volume StorageVolume) (StorageSnapshot, error)
	SyncStorageSnapshot(ctx context.Context, snapshot StorageSnapshot) (StorageSnapshot, error)
	RestoreStorageSnapshot(ctx context.Context, input StorageRestoreInput, snapshot StorageSnapshot) (StorageVolume, error)
	DestroyStorageSnapshot(ctx context.Context, snapshot StorageSnapshot) (StorageSnapshot, error)
	CreateStorageAttachment(ctx context.Context, input StorageAttachmentInput, compute ComputeAllocation, volume StorageVolume) (StorageAttachment, error)
	DetachStorageAttachment(ctx context.Context, attachment StorageAttachment) (StorageAttachment, error)
	CreateWorkspaceRuntime(ctx context.Context, input WorkspaceRuntimeInput, compute ComputeAllocation, volume StorageVolume) (WorkspaceRuntime, error)
	DestroyWorkspaceRuntime(ctx context.Context, workspaceID string) (WorkspaceRuntime, error)
	WorkspaceRuntimeStatus(ctx context.Context, workspaceID string) (WorkspaceRuntime, error)
	UpsertGatewaySecret(ctx context.Context, input GatewaySecretInput) (GatewaySecret, error)
	Readiness(ctx context.Context) (map[string]any, error)
}

type Service struct {
	provider    Provider
	mu          sync.Mutex
	jobMu       sync.Mutex
	computes    map[string]ComputeAllocation
	volumes     map[string]StorageVolume
	snapshots   map[string]StorageSnapshot
	attachments map[string]StorageAttachment
	destroying  map[string]bool
	operations  OperationStore
	transfers   TransferStore
	now         func() time.Time
}

func NewService(provider Provider) *Service {
	return NewServiceWithOperationStore(provider, NewMemoryOperationStore())
}

func NewServiceWithOperationStore(provider Provider, operations OperationStore) *Service {
	if operations == nil {
		operations = NewMemoryOperationStore()
	}
	computes, volumes, snapshots, attachments, _ := replayResourceState(context.Background(), operations)
	transferStore, _ := operations.(TransferStore)
	if transferStore == nil {
		transferStore = newMemoryTransferStore()
	}
	return &Service{provider: provider, computes: computes, volumes: volumes, snapshots: snapshots, attachments: attachments, destroying: map[string]bool{}, operations: operations, transfers: transferStore, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) Catalog(_ context.Context) Catalog {
	return Catalog{
		SchemaVersion: 1,
		Owner:         "OPL Fabric",
		WorkspacePackages: []WorkspacePackage{
			{ID: "basic", Name: "Basic Workspace", ComputeProfileID: "cpu-basic", CPU: 2, MemoryGB: 4, DiskGB: 10, Provider: "tencent-tke", Available: strings.TrimSpace(packagePlan("basic").NodePoolID) != ""},
			{ID: "pro", Name: "Pro Workspace", ComputeProfileID: "cpu-pro", CPU: 8, MemoryGB: 16, DiskGB: 100, Provider: "tencent-tke", Available: strings.TrimSpace(packagePlan("pro").NodePoolID) != ""},
		},
		StorageClasses: []StorageClass{{ID: "workspace-cbs", StorageClassName: "cbs", Provider: "tencent-tke", Available: true}},
		IngressDomains: []IngressDomain{{ID: "workspace", Host: "workspace.medopl.cn", PathPattern: "/w/<workspaceId>/", Available: true}},
	}
}

func (s *Service) MonthlyPreflight(ctx context.Context, input MonthlyPreflightInput) (MonthlyPreflight, error) {
	if (input.ResourceType != "compute" && input.ResourceType != "storage") || (input.PackageID != "basic" && input.PackageID != "pro") || input.Zone == "" || input.Zone != strings.TrimSpace(input.Zone) ||
		(input.ResourceType == "compute" && input.SizeGB != 0) || (input.ResourceType == "storage" && (input.SizeGB < 10 || input.SizeGB%10 != 0)) {
		return MonthlyPreflight{}, ErrInvalidMonthlyPreflight
	}
	result, err := s.provider.MonthlyPreflight(ctx, input)
	if err != nil {
		return MonthlyPreflight{}, fmt.Errorf("%w: %v", ErrMonthlyPreflightUnavailable, err)
	}
	requiredRequestIDs := []string{"nodePool", "subnets", "availability", "quota"}
	if input.ResourceType == "storage" {
		requiredRequestIDs = []string{"quota", "price"}
	}
	validRequestIDs := len(result.ProviderRequestIDs) > 0
	for _, key := range requiredRequestIDs {
		validRequestIDs = validRequestIDs && strings.TrimSpace(result.ProviderRequestIDs[key]) != ""
	}
	if result.ResourceType != input.ResourceType || result.PackageID != input.PackageID || result.SizeGB != input.SizeGB || result.Zone != input.Zone || !result.Available ||
		result.ChargeType != "PREPAID" || result.PeriodMonths != 1 || result.RenewFlag != "NOTIFY_AND_MANUAL_RENEW" || result.ProviderPriceCNY <= 0 ||
		math.IsNaN(result.ProviderPriceCNY) || math.IsInf(result.ProviderPriceCNY, 0) || !validRequestIDs {
		return MonthlyPreflight{}, ErrMonthlyPreflightUnavailable
	}
	return result, nil
}

func (s *Service) MachineOwnership(ctx context.Context, resourceID string) (MachineOwnership, error) {
	return s.operations.MachineOwnership(ctx, strings.TrimSpace(resourceID))
}

func (s *Service) CreateComputeAllocation(ctx context.Context, input ComputeAllocationInput) (ComputeAllocation, error) {
	if input.PackageID != "basic" && input.PackageID != "pro" {
		return ComputeAllocation{}, ErrUnsupportedComputePackage
	}
	if strings.TrimSpace(input.IdempotencyKey) == "" {
		return ComputeAllocation{}, fmt.Errorf("compute_idempotency_key_required")
	}
	requestHash := hashInput(input)
	now := s.now()
	id := firstNonEmpty(input.ID, "ca_"+stableSuffix("create_compute_allocation", input.IdempotencyKey)[:18])
	input.ID = id
	allocation := ComputeAllocation{
		ID:                id,
		AccountID:         input.AccountID,
		WorkspaceID:       input.WorkspaceID,
		PackageID:         firstNonEmpty(input.PackageID, "basic"),
		Status:            "provisioning",
		Provider:          "tencent-tke",
		ProviderRequestID: providerRequestID("compute", input.IdempotencyKey),
		CreatedAt:         now,
	}
	operation := newOperation("create_compute_allocation", "compute_allocation", id, input.AccountID, input.WorkspaceID, input.IdempotencyKey, requestHash, now)
	operation.ID = "fop_compute_claim_" + stableSuffix("create_compute_allocation", input.IdempotencyKey)
	operation.Status = "started"
	operation.CreatedAt = now
	fillOperationResource(&operation, allocation)
	stored, claimed, err := s.operations.ClaimRuntime(ctx, operation)
	if err != nil {
		return ComputeAllocation{}, err
	}
	if !claimed {
		return replayComputeAllocationOperation(stored, requestHash)
	}
	input.OperationID = operation.OperationID
	s.mu.Lock()
	s.computes[allocation.ID] = allocation
	s.mu.Unlock()

	go func() { _ = s.reconcileComputePool(allocation.PackageID, input.DryRun) }()
	return allocation, nil
}

func replayComputeAllocationOperation(operation FabricOperation, requestHash string) (ComputeAllocation, error) {
	if operation.RequestHash != requestHash {
		return ComputeAllocation{}, ErrComputeIdempotencyConflict
	}
	var allocation ComputeAllocation
	if !decodeOperationResource(operation, &allocation) {
		return ComputeAllocation{}, ErrComputeOperationFailed
	}
	if operation.Status == "started" || operation.Status == "succeeded" {
		return allocation, nil
	}
	return allocation, ErrComputeOperationFailed
}

func (s *Service) GetComputeAllocation(_ context.Context, allocationID string) (ComputeAllocation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	allocation, ok := s.computes[allocationID]
	return allocation, ok
}

func (s *Service) SyncComputeAllocation(ctx context.Context, allocationID string) (ComputeAllocation, error) {
	s.mu.Lock()
	existing := s.computes[allocationID]
	s.mu.Unlock()
	if existing.ID == "" {
		operation := newOperation("sync_compute_allocation", "compute_allocation", allocationID, "", "", "", hashInput(map[string]string{"id": allocationID}), time.Now().UTC())
		operation.ProviderRequestID = providerRequestID("sync-compute", allocationID)
		err := fmt.Errorf("compute_allocation_not_found")
		_ = s.recordOperation(ctx, operation, "rejected", ComputeAllocation{ID: allocationID}, err)
		return ComputeAllocation{}, err
	}
	if existing.Status == "provisioning" && (existing.MachineName == "" || firstNonEmpty(existing.InstanceID, existing.CVMInstanceID) == "" || existing.NodeName == "") {
		operations, err := s.operations.List(ctx)
		if err != nil {
			return existing, err
		}
		for index := len(operations) - 1; index >= 0; index-- {
			operation := operations[index]
			if operation.Action != "create_compute_allocation" || operation.ResourceID != allocationID {
				continue
			}
			if operation.Status == "started" {
				return existing, nil
			}
			if operation.Status == "succeeded" {
				if !decodeOperationResource(operation, &existing) || existing.MachineName == "" || firstNonEmpty(existing.InstanceID, existing.CVMInstanceID) == "" || existing.NodeName == "" {
					return existing, fmt.Errorf("compute_machine_identity_required")
				}
				s.mu.Lock()
				s.computes[allocationID] = existing
				s.mu.Unlock()
			}
			break
		}
	}
	if existing.Status == "failed" && existing.NodePoolID == "" && existing.MachineName == "" && existing.InstanceID == "" {
		return existing, nil
	}
	operation := newOperation("sync_compute_allocation", "compute_allocation", allocationID, existing.AccountID, existing.WorkspaceID, "", hashInput(existing), time.Now().UTC())
	if err := s.recordOperation(ctx, operation, "started", existing, nil); err != nil {
		return ComputeAllocation{}, err
	}
	allocation, err := s.provider.SyncComputeAllocation(ctx, existing)
	if err != nil {
		_ = s.recordOperation(ctx, operation, "failed", allocation, err)
		return allocation, err
	}
	if allocation.ID == "" {
		allocation.ID = existing.ID
	}
	if allocation.AccountID == "" {
		allocation.AccountID = existing.AccountID
	}
	if allocation.WorkspaceID == "" {
		allocation.WorkspaceID = existing.WorkspaceID
	}
	if allocation.PackageID == "" {
		allocation.PackageID = existing.PackageID
	}
	if allocation.Provider == "" {
		allocation.Provider = firstNonEmpty(existing.Provider, "tencent-tke")
	}
	if isExternallyDeletedComputeStatus(allocation.Status) {
		if err := s.releaseMachineOwnership(ctx, allocationID); err != nil {
			_ = s.recordOperation(ctx, operation, "failed", allocation, err)
			return allocation, err
		}
	} else if isReadyResourceStatus(allocation.Status) {
		ownership, ownershipErr := s.operations.MachineOwnership(ctx, allocationID)
		if ownershipErr != nil && ownershipErr != ErrMachineOwnershipNotFound {
			_ = s.recordOperation(ctx, operation, "failed", allocation, ownershipErr)
			return allocation, ownershipErr
		}
		if ownershipErr == nil && ownership.Status == "quarantined" {
			machine := ProviderMachine{
				MachineID: firstNonEmpty(allocation.MachineName, ownership.MachineID), InstanceID: firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID, ownership.InstanceID),
				NodeName: firstNonEmpty(allocation.NodeName, ownership.NodeName), PrivateIP: allocation.PrivateIP, PublicIP: allocation.PublicIP,
				InstanceType: allocation.ProviderData["instanceType"], Zone: allocation.ProviderData["zone"], ChargeType: allocation.ChargeType,
				RenewFlag: allocation.RenewFlag, Deadline: allocation.Deadline, Ready: true,
			}
			if err := s.provider.TagComputeMachine(ctx, machine, ownership); err != nil {
				_ = s.recordOperation(ctx, operation, "failed", allocation, err)
				return allocation, err
			}
			ownership.Status = "active"
			ownership.ReleasedAt = nil
			if err := s.operations.SaveMachineOwnership(ctx, ownership); err != nil {
				_ = s.recordOperation(ctx, operation, "failed", allocation, err)
				return allocation, err
			}
		}
	}
	if err := s.recordOperation(ctx, operation, "succeeded", allocation, nil); err != nil {
		return allocation, err
	}
	s.mu.Lock()
	s.computes[allocationID] = allocation
	s.mu.Unlock()
	return allocation, nil
}

func (s *Service) RenewComputeAllocation(ctx context.Context, allocationID, idempotencyKey string) (ComputeAllocation, error) {
	if strings.TrimSpace(allocationID) == "" || strings.TrimSpace(idempotencyKey) == "" {
		return ComputeAllocation{}, fmt.Errorf("compute_renew_identity_required")
	}
	var result ComputeAllocation
	err := s.operations.WithPoolLock(ctx, "compute-renew:"+allocationID, func(lockCtx context.Context) error {
		s.mu.Lock()
		existing := s.computes[allocationID]
		s.mu.Unlock()
		if !validComputeRenewalIdentity(existing) {
			return fmt.Errorf("compute_allocation_renew_identity_required")
		}
		requestHash := hashInput(map[string]string{"id": allocationID})
		operations, err := s.operations.List(lockCtx)
		if err != nil {
			return err
		}
		operation := newOperation("renew_compute_allocation", "compute_allocation", allocationID, existing.AccountID, existing.WorkspaceID, idempotencyKey, requestHash, s.now())
		started := false
		for _, candidate := range operations {
			if candidate.Action != operation.Action || candidate.IdempotencyKey != idempotencyKey {
				continue
			}
			if candidate.RequestHash != requestHash {
				return fmt.Errorf("compute_renew_idempotency_conflict")
			}
			if candidate.Status == "succeeded" && decodeOperationResource(candidate, &result) {
				return nil
			}
			operation = candidate
			started = true
		}
		if !started {
			if err := s.recordOperation(lockCtx, operation, "started", existing, nil); err != nil {
				return err
			}
		}
		request := existing
		request.ProviderData = maps.Clone(existing.ProviderData)
		request.CostTags = maps.Clone(existing.CostTags)
		result, err = s.provider.RenewComputeAllocation(lockCtx, request)
		if err != nil {
			_ = s.recordOperation(lockCtx, operation, "failed", result, err)
			return err
		}
		if !validComputeRenewal(existing, result) {
			err = fmt.Errorf("compute_renewal_readback_mismatch")
			result = existing
			_ = s.recordOperation(lockCtx, operation, "failed", result, err)
			return err
		}
		if err := s.recordOperation(lockCtx, operation, "succeeded", result, nil); err != nil {
			return err
		}
		s.mu.Lock()
		s.computes[allocationID] = result
		s.mu.Unlock()
		return nil
	})
	return result, err
}

func (s *Service) DestroyComputeAllocation(ctx context.Context, allocationID string) (ComputeAllocation, error) {
	s.mu.Lock()
	existing := s.computes[allocationID]
	s.mu.Unlock()
	if existing.ID == "" {
		operation := newOperation("destroy_compute_allocation", "compute_allocation", allocationID, "", "", "", hashInput(map[string]string{"id": allocationID}), time.Now().UTC())
		operation.ProviderRequestID = providerRequestID("destroy-compute", allocationID)
		err := fmt.Errorf("compute_allocation_not_found")
		_ = s.recordOperation(ctx, operation, "rejected", ComputeAllocation{ID: allocationID}, err)
		return ComputeAllocation{}, err
	}
	plan := packagePlan(firstNonEmpty(existing.PackageID, "basic"))
	operation := newOperation("destroy_compute_allocation", "compute_allocation", allocationID, existing.AccountID, existing.WorkspaceID, "", hashInput(map[string]string{"id": allocationID}), time.Now().UTC())
	allocation := existing
	startWorker := false
	err := s.operations.WithPoolLock(ctx, "compute-destroy:"+allocationID, func(lockCtx context.Context) error {
		latest, found, err := s.latestComputeDestroyOperation(lockCtx, allocationID)
		if err != nil {
			return err
		}
		if found && (latest.Status == "started" || latest.Status == "succeeded") {
			operation = latest
			_ = decodeOperationResource(latest, &allocation)
			if latest.Status == "succeeded" {
				return nil
			}
			s.mu.Lock()
			startWorker = !s.destroying[allocationID]
			s.destroying[allocationID] = true
			s.mu.Unlock()
			return nil
		}
		if !isExternallyDeletedComputeStatus(allocation.Status) {
			allocation.Status = "destroying"
		}
		if err := s.recordOperation(lockCtx, operation, "started", allocation, nil); err != nil {
			return err
		}
		s.mu.Lock()
		s.computes[allocationID] = allocation
		s.destroying[allocationID] = true
		s.mu.Unlock()
		startWorker = true
		return nil
	})
	if err != nil {
		return allocation, err
	}
	if startWorker {
		go s.finishDestroyComputeAllocation(operation, allocation, plan)
	}
	return allocation, nil
}

func (s *Service) finishDestroyComputeAllocation(operation FabricOperation, existing ComputeAllocation, plan plan) {
	ctx := context.Background()
	allocation := existing
	err := s.operations.WithPoolLock(ctx, plan.ID+":"+plan.InstanceType, func(lockCtx context.Context) error {
		if latest, found, err := s.latestComputeDestroyOperation(lockCtx, existing.ID); err != nil {
			return err
		} else if found && latest.Status == "succeeded" {
			_ = decodeOperationResource(latest, &allocation)
			return nil
		}
		s.mu.Lock()
		current := s.computes[existing.ID]
		s.mu.Unlock()
		var providerErr error
		allocation, providerErr = s.provider.DestroyComputeAllocation(lockCtx, current)
		if providerErr != nil {
			return providerErr
		}
		if err := s.releaseMachineOwnership(lockCtx, existing.ID); err != nil {
			return err
		}
		if err := s.reconcileComputePoolLocked(lockCtx, firstNonEmpty(existing.PackageID, "basic"), false); err != nil {
			return err
		}
		return s.cancelPendingComputeCreation(lockCtx, existing.ID, allocation)
	})
	if err != nil {
		if allocation.ID == "" {
			allocation = existing
		}
		allocation.Status = "destroying"
		_ = s.recordOperation(ctx, operation, "failed", allocation, err)
	} else {
		_ = s.recordOperation(ctx, operation, "succeeded", allocation, nil)
		s.mu.Lock()
		s.computes[existing.ID] = allocation
		s.mu.Unlock()
	}
	s.mu.Lock()
	delete(s.destroying, existing.ID)
	s.mu.Unlock()
}

func (s *Service) releaseMachineOwnership(ctx context.Context, resourceID string) error {
	ownership, err := s.operations.MachineOwnership(ctx, resourceID)
	if err == ErrMachineOwnershipNotFound {
		return nil
	}
	if err != nil || ownership.Status == "released" {
		return err
	}
	now := s.now()
	ownership.Status = "released"
	ownership.ReleasedAt = &now
	return s.operations.SaveMachineOwnership(ctx, ownership)
}

func isExternallyDeletedComputeStatus(status string) bool {
	switch status {
	case "external_deleted", "deleted", "missing":
		return true
	default:
		return false
	}
}

func (s *Service) latestComputeDestroyOperation(ctx context.Context, allocationID string) (FabricOperation, bool, error) {
	operations, err := s.operations.List(ctx)
	if err != nil {
		return FabricOperation{}, false, err
	}
	for index := len(operations) - 1; index >= 0; index-- {
		if operations[index].Action == "destroy_compute_allocation" && operations[index].ResourceID == allocationID {
			return operations[index], true, nil
		}
	}
	return FabricOperation{}, false, nil
}

func (s *Service) cancelPendingComputeCreation(ctx context.Context, allocationID string, allocation ComputeAllocation) error {
	operations, err := s.operations.List(ctx)
	if err != nil {
		return err
	}
	latest := FabricOperation{}
	for _, candidate := range operations {
		if candidate.Action == "create_compute_allocation" && candidate.ResourceID == allocationID {
			latest = candidate
		}
	}
	if latest.Status != "started" && latest.Status != "canceling" {
		return nil
	}
	return s.recordOperation(ctx, latest, "failed", allocation, fmt.Errorf("compute_create_canceled"))
}

func (s *Service) CreateStorageVolume(ctx context.Context, input StorageVolumeInput) (StorageVolume, error) {
	if input.SizeGB < 10 || input.SizeGB%10 != 0 {
		return StorageVolume{}, ErrInvalidStorageSize
	}
	if input.ID == "" {
		if strings.TrimSpace(input.IdempotencyKey) == "" {
			return StorageVolume{}, fmt.Errorf("storage_idempotency_key_required")
		}
		input.ID = "vol_" + stableSuffix("create_storage_volume", input.IdempotencyKey)[:16]
	}
	s.mu.Lock()
	compute := s.computes[input.ComputeID]
	s.mu.Unlock()
	computeZone := strings.TrimSpace(compute.ProviderData["zone"])
	if compute.ID == "" || compute.AccountID != input.AccountID || compute.WorkspaceID != input.WorkspaceID ||
		!isReadyResourceStatus(compute.Status) || computeZone == "" || strings.TrimSpace(input.Zone) != computeZone {
		return StorageVolume{}, fmt.Errorf("storage_compute_zone_mismatch")
	}
	requestHash := hashInput(input)
	var volume StorageVolume
	err := s.operations.WithPoolLock(ctx, "storage-create:"+firstNonEmpty(input.IdempotencyKey, input.ID), func(lockCtx context.Context) error {
		operations, err := s.operations.List(lockCtx)
		if err != nil {
			return err
		}
		for index := len(operations) - 1; index >= 0; index-- {
			candidate := operations[index]
			if candidate.Action != "create_storage_volume" || candidate.IdempotencyKey != input.IdempotencyKey || candidate.ResourceID != input.ID {
				continue
			}
			if candidate.RequestHash != requestHash {
				return fmt.Errorf("storage_create_idempotency_conflict")
			}
			if candidate.Status == "succeeded" && decodeOperationResource(candidate, &volume) {
				return nil
			}
			break
		}
		operation := newOperation("create_storage_volume", "storage_volume", input.ID, input.AccountID, input.WorkspaceID, input.IdempotencyKey, requestHash, s.now())
		input.OperationID = operation.OperationID
		if err := s.recordOperation(lockCtx, operation, "started", StorageVolume{ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Provider: "tencent-tke", ProviderRequestID: providerRequestID("storage", input.IdempotencyKey)}, nil); err != nil {
			return err
		}
		volume, err = s.provider.CreateStorageVolume(lockCtx, input)
		volume.ID = input.ID
		volume.AccountID = firstNonEmpty(volume.AccountID, input.AccountID)
		volume.WorkspaceID = firstNonEmpty(volume.WorkspaceID, input.WorkspaceID)
		volume.Provider = firstNonEmpty(volume.Provider, "tencent-tke")
		volume.Zone = firstNonEmpty(volume.Zone, input.Zone)
		if volume.SizeGB == 0 {
			volume.SizeGB = input.SizeGB
		}
		if err != nil {
			knownCBS := strings.HasPrefix(volume.ProviderResourceID, "disk-")
			if knownCBS {
				volume.Status = "quarantined"
			}
			if recordErr := s.recordOperation(lockCtx, operation, "failed", volume, err); recordErr != nil {
				return recordErr
			}
			if knownCBS {
				s.mu.Lock()
				s.volumes[volume.ID] = volume
				s.mu.Unlock()
			}
			return err
		}
		if err := s.recordOperation(lockCtx, operation, "succeeded", volume, nil); err != nil {
			return err
		}
		s.mu.Lock()
		s.volumes[volume.ID] = volume
		s.mu.Unlock()
		return nil
	})
	return volume, err
}

func (s *Service) GetStorageVolume(_ context.Context, volumeID string) (StorageVolume, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	volume, ok := s.volumes[volumeID]
	return volume, ok
}

func (s *Service) CreateStorageSnapshot(ctx context.Context, input StorageSnapshotInput) (StorageSnapshot, error) {
	if input.AccountID == "" || input.WorkspaceID == "" || input.VolumeID == "" || input.IdempotencyKey == "" {
		return StorageSnapshot{}, fmt.Errorf("storage_snapshot_input_required")
	}
	requestHash := hashInput(input)
	operations, err := s.operations.List(ctx)
	if err != nil {
		return StorageSnapshot{}, err
	}
	for _, operation := range operations {
		if operation.Action != "create_storage_snapshot" || operation.IdempotencyKey != input.IdempotencyKey {
			continue
		}
		if operation.RequestHash != requestHash {
			return StorageSnapshot{}, fmt.Errorf("storage_snapshot_idempotency_conflict")
		}
		var replayed StorageSnapshot
		if operation.Status == "succeeded" && decodeOperationResource(operation, &replayed) {
			return replayed, nil
		}
	}
	s.mu.Lock()
	volume := s.volumes[input.VolumeID]
	s.mu.Unlock()
	if volume.ID == "" || volume.Status != "ready" {
		return StorageSnapshot{}, fmt.Errorf("storage_volume_not_ready")
	}
	now := s.now()
	id := "snap-" + stableSuffix(input.WorkspaceID, input.VolumeID, input.IdempotencyKey)[:16]
	operation := newOperation("create_storage_snapshot", "storage_snapshot", id, input.AccountID, input.WorkspaceID, input.IdempotencyKey, requestHash, now)
	input.OperationID = operation.OperationID
	if err := s.recordOperation(ctx, operation, "started", StorageSnapshot{ID: id, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, VolumeID: input.VolumeID, Status: "creating", Provider: "tencent-tke", ProviderRequestID: providerRequestID("snapshot", input.IdempotencyKey), CreatedAt: now}, nil); err != nil {
		return StorageSnapshot{}, err
	}
	snapshot, err := s.provider.CreateStorageSnapshot(ctx, input, volume)
	if snapshot.ID == "" {
		snapshot.ID = id
	}
	if err != nil {
		_ = s.recordOperation(ctx, operation, "failed", snapshot, err)
		return snapshot, err
	}
	operation.ResourceID = snapshot.ID
	if err := s.recordOperation(ctx, operation, "succeeded", snapshot, nil); err != nil {
		return snapshot, err
	}
	s.mu.Lock()
	s.snapshots[snapshot.ID] = snapshot
	s.mu.Unlock()
	return snapshot, nil
}

func (s *Service) GetStorageSnapshot(_ context.Context, snapshotID string) (StorageSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, ok := s.snapshots[snapshotID]
	return snapshot, ok
}

func (s *Service) SyncStorageSnapshot(ctx context.Context, snapshotID string) (StorageSnapshot, error) {
	s.mu.Lock()
	snapshot := s.snapshots[snapshotID]
	s.mu.Unlock()
	if snapshot.ID == "" {
		return StorageSnapshot{}, fmt.Errorf("storage_snapshot_not_found")
	}
	operation := newOperation("sync_storage_snapshot", "storage_snapshot", snapshotID, snapshot.AccountID, snapshot.WorkspaceID, "", hashInput(map[string]string{"id": snapshotID}), s.now())
	if err := s.recordOperation(ctx, operation, "started", snapshot, nil); err != nil {
		return StorageSnapshot{}, err
	}
	synced, err := s.provider.SyncStorageSnapshot(ctx, snapshot)
	if err != nil {
		_ = s.recordOperation(ctx, operation, "failed", synced, err)
		return synced, err
	}
	if err := s.recordOperation(ctx, operation, "succeeded", synced, nil); err != nil {
		return synced, err
	}
	s.mu.Lock()
	s.snapshots[snapshotID] = synced
	s.mu.Unlock()
	return synced, nil
}

func (s *Service) RestoreStorageSnapshot(ctx context.Context, input StorageRestoreInput) (StorageVolume, error) {
	if input.SnapshotID == "" || input.AccountID == "" || input.WorkspaceID == "" || input.TargetVolumeID == "" || input.IdempotencyKey == "" {
		return StorageVolume{}, fmt.Errorf("storage_restore_input_required")
	}
	s.mu.Lock()
	snapshot := s.snapshots[input.SnapshotID]
	s.mu.Unlock()
	if snapshot.ID == "" || snapshot.Status != "ready" {
		return StorageVolume{}, fmt.Errorf("storage_snapshot_not_ready")
	}
	operation := newOperation("restore_storage_snapshot", "storage_volume", input.TargetVolumeID, input.AccountID, input.WorkspaceID, input.IdempotencyKey, hashInput(input), s.now())
	input.OperationID = operation.OperationID
	if err := s.recordOperation(ctx, operation, "started", StorageVolume{ID: input.TargetVolumeID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "restoring", Provider: snapshot.Provider, ProviderRequestID: providerRequestID("restore", input.IdempotencyKey)}, nil); err != nil {
		return StorageVolume{}, err
	}
	volume, err := s.provider.RestoreStorageSnapshot(ctx, input, snapshot)
	if err != nil {
		_ = s.recordOperation(ctx, operation, "failed", volume, err)
		return volume, err
	}
	if err := s.recordOperation(ctx, operation, "succeeded", volume, nil); err != nil {
		return volume, err
	}
	s.mu.Lock()
	s.volumes[volume.ID] = volume
	s.mu.Unlock()
	return volume, nil
}

func (s *Service) DestroyStorageSnapshot(ctx context.Context, snapshotID string) (StorageSnapshot, error) {
	s.mu.Lock()
	snapshot := s.snapshots[snapshotID]
	s.mu.Unlock()
	if snapshot.ID == "" {
		return StorageSnapshot{}, fmt.Errorf("storage_snapshot_not_found")
	}
	operation := newOperation("destroy_storage_snapshot", "storage_snapshot", snapshotID, snapshot.AccountID, snapshot.WorkspaceID, "", hashInput(map[string]string{"id": snapshotID}), s.now())
	if err := s.recordOperation(ctx, operation, "started", snapshot, nil); err != nil {
		return StorageSnapshot{}, err
	}
	destroyed, err := s.provider.DestroyStorageSnapshot(ctx, snapshot)
	if err != nil {
		_ = s.recordOperation(ctx, operation, "failed", destroyed, err)
		return destroyed, err
	}
	if err := s.recordOperation(ctx, operation, "succeeded", destroyed, nil); err != nil {
		return destroyed, err
	}
	s.mu.Lock()
	s.snapshots[snapshotID] = destroyed
	s.mu.Unlock()
	return destroyed, nil
}

func (s *Service) DestroyStorageVolume(ctx context.Context, volumeID string) (StorageVolume, error) {
	s.mu.Lock()
	existing := s.volumes[volumeID]
	s.mu.Unlock()
	if existing.ID == "" {
		operation := newOperation("destroy_storage_volume", "storage_volume", volumeID, "", "", "", hashInput(map[string]string{"id": volumeID}), time.Now().UTC())
		operation.ProviderRequestID = providerRequestID("destroy-storage", volumeID)
		err := fmt.Errorf("storage_volume_not_found")
		_ = s.recordOperation(ctx, operation, "rejected", StorageVolume{ID: volumeID}, err)
		return StorageVolume{}, err
	}
	operation := newOperation("destroy_storage_volume", "storage_volume", volumeID, existing.AccountID, existing.WorkspaceID, "", hashInput(map[string]string{"id": volumeID}), time.Now().UTC())
	if err := s.recordOperation(ctx, operation, "started", existing, nil); err != nil {
		return StorageVolume{}, err
	}
	volume, err := s.provider.DestroyStorageVolume(ctx, existing)
	if err != nil {
		_ = s.recordOperation(ctx, operation, "failed", volume, err)
		return volume, err
	}
	if err := s.recordOperation(ctx, operation, "succeeded", volume, nil); err != nil {
		return volume, err
	}
	s.mu.Lock()
	s.volumes[volumeID] = volume
	s.mu.Unlock()
	return volume, nil
}

func (s *Service) SyncStorageVolume(ctx context.Context, volumeID string) (StorageVolume, error) {
	s.mu.Lock()
	existing := s.volumes[volumeID]
	s.mu.Unlock()
	if existing.ID == "" {
		operation := newOperation("sync_storage_volume", "storage_volume", volumeID, "", "", "", hashInput(map[string]string{"id": volumeID}), time.Now().UTC())
		operation.ProviderRequestID = providerRequestID("sync-storage", volumeID)
		err := fmt.Errorf("storage_volume_not_found")
		_ = s.recordOperation(ctx, operation, "rejected", StorageVolume{ID: volumeID}, err)
		return StorageVolume{}, err
	}
	if isRetainedStorageStatus(existing.Status) {
		return existing, nil
	}
	operation := newOperation("sync_storage_volume", "storage_volume", volumeID, existing.AccountID, existing.WorkspaceID, "", hashInput(existing), time.Now().UTC())
	if err := s.recordOperation(ctx, operation, "started", existing, nil); err != nil {
		return StorageVolume{}, err
	}
	volume, err := s.provider.SyncStorageVolume(ctx, existing)
	if volume.ID == "" {
		volume.ID = existing.ID
	}
	if volume.AccountID == "" {
		volume.AccountID = existing.AccountID
	}
	if volume.WorkspaceID == "" {
		volume.WorkspaceID = existing.WorkspaceID
	}
	if volume.Provider == "" {
		volume.Provider = firstNonEmpty(existing.Provider, "tencent-tke")
	}
	if volume.ProviderResourceID == "" {
		volume.ProviderResourceID = existing.ProviderResourceID
	}
	if volume.ProviderRequestID == "" {
		volume.ProviderRequestID = existing.ProviderRequestID
	}
	if err != nil {
		_ = s.recordOperation(ctx, operation, "failed", volume, err)
		return volume, err
	}
	if volume.Status == "pending" && !existing.CreatedAt.IsZero() && s.now().Sub(existing.CreatedAt) >= storageProvisionTimeout {
		cleaned, cleanupErr := s.provider.DestroyStorageVolume(ctx, volume)
		if cleanupErr != nil {
			volume.Status = "quarantined"
			if recordErr := s.recordOperation(ctx, operation, "failed", volume, cleanupErr); recordErr != nil {
				return volume, recordErr
			}
			s.mu.Lock()
			s.volumes[volumeID] = volume
			s.mu.Unlock()
			return volume, nil
		}
		volume = cleaned
	}
	if err := s.recordOperation(ctx, operation, "succeeded", volume, nil); err != nil {
		return volume, err
	}
	s.mu.Lock()
	s.volumes[volumeID] = volume
	s.mu.Unlock()
	return volume, nil
}

func (s *Service) RenewStorageVolume(ctx context.Context, volumeID, idempotencyKey string) (StorageVolume, error) {
	if strings.TrimSpace(volumeID) == "" || strings.TrimSpace(idempotencyKey) == "" {
		return StorageVolume{}, fmt.Errorf("storage_renew_identity_required")
	}
	var result StorageVolume
	err := s.operations.WithPoolLock(ctx, "storage-renew:"+volumeID, func(lockCtx context.Context) error {
		s.mu.Lock()
		existing := s.volumes[volumeID]
		s.mu.Unlock()
		if existing.ID == "" || !strings.HasPrefix(existing.ProviderResourceID, "disk-") || strings.TrimSpace(existing.Deadline) == "" {
			return fmt.Errorf("storage_volume_renew_identity_required")
		}
		requestHash := hashInput(map[string]string{"id": volumeID})
		operations, err := s.operations.List(lockCtx)
		if err != nil {
			return err
		}
		operation := newOperation("renew_storage_volume", "storage_volume", volumeID, existing.AccountID, existing.WorkspaceID, idempotencyKey, requestHash, s.now())
		started := false
		for _, candidate := range operations {
			if candidate.Action != operation.Action || candidate.IdempotencyKey != idempotencyKey {
				continue
			}
			if candidate.RequestHash != requestHash {
				return fmt.Errorf("storage_renew_idempotency_conflict")
			}
			if candidate.Status == "succeeded" && decodeOperationResource(candidate, &result) {
				return nil
			}
			operation = candidate
			started = true
		}
		if !started {
			if err := s.recordOperation(lockCtx, operation, "started", existing, nil); err != nil {
				return err
			}
		}
		result, err = s.provider.RenewStorageVolume(lockCtx, existing)
		if err != nil {
			_ = s.recordOperation(lockCtx, operation, "failed", result, err)
			return err
		}
		if !validStorageRenewal(existing, result) {
			err = fmt.Errorf("storage_renewal_readback_mismatch")
			result = existing
			_ = s.recordOperation(lockCtx, operation, "failed", result, err)
			return err
		}
		if isRetainedStorageStatus(existing.Status) {
			result.Status = "pending"
		}
		if err := s.recordOperation(lockCtx, operation, "succeeded", result, nil); err != nil {
			return err
		}
		s.mu.Lock()
		s.volumes[volumeID] = result
		s.mu.Unlock()
		return nil
	})
	return result, err
}

func (s *Service) CreateStorageAttachment(ctx context.Context, input StorageAttachmentInput) (StorageAttachment, error) {
	if strings.TrimSpace(input.IdempotencyKey) == "" {
		return StorageAttachment{}, fmt.Errorf("storage_attachment_idempotency_key_required")
	}
	requestHash := hashInput(input)
	s.mu.Lock()
	compute := s.computes[input.ComputeID]
	volume := s.volumes[input.VolumeID]
	s.mu.Unlock()
	now := s.now()
	operation := newOperation("create_storage_attachment", "storage_attachment", input.IdempotencyKey, compute.AccountID, input.WorkspaceID, input.IdempotencyKey, requestHash, now)
	if err := validateAttachmentInput(input, compute, volume); err != nil {
		operation.ProviderRequestID = providerRequestID("storage-attach", input.IdempotencyKey)
		_ = s.recordOperation(ctx, operation, "rejected", StorageAttachment{ID: operation.ResourceID, WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID, ProviderRequestID: operation.ProviderRequestID}, err)
		return StorageAttachment{}, err
	}
	operation.ID = "fop_attachment_claim_" + stableSuffix("create_storage_attachment", input.IdempotencyKey)
	operation.Status = "started"
	operation.CreatedAt = now
	fillOperationResource(&operation, StorageAttachment{ID: operation.ResourceID, WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID, Provider: "tencent-tke", ProviderRequestID: providerRequestID("storage-attach", input.IdempotencyKey)})
	input.OperationID = operation.OperationID
	stored, claimed, err := s.operations.ClaimRuntime(ctx, operation)
	if err != nil {
		return StorageAttachment{}, err
	}
	if !claimed {
		return replayStorageAttachmentOperation(stored, requestHash)
	}
	attachment, err := s.provider.CreateStorageAttachment(ctx, input, compute, volume)
	if err != nil {
		_ = s.saveStorageAttachmentOperation(ctx, stored, "failed", attachment, err)
		return attachment, err
	}
	if err := s.saveStorageAttachmentOperation(ctx, stored, "succeeded", attachment, nil); err != nil {
		return attachment, err
	}
	s.mu.Lock()
	s.attachments[attachment.ID] = attachment
	s.mu.Unlock()
	return attachment, nil
}

func replayStorageAttachmentOperation(operation FabricOperation, requestHash string) (StorageAttachment, error) {
	if operation.RequestHash != requestHash {
		return StorageAttachment{}, ErrStorageAttachmentIdempotencyConflict
	}
	switch operation.Status {
	case "started":
		return StorageAttachment{}, ErrStorageAttachmentOperationInProgress
	case "succeeded":
		var attachment StorageAttachment
		if decodeOperationResource(operation, &attachment) {
			return attachment, nil
		}
	}
	// ponytail: provider attach is not safely repeatable; reconciliation must resolve failed or corrupt claims.
	return StorageAttachment{}, ErrStorageAttachmentOperationFailed
}

func (s *Service) saveStorageAttachmentOperation(ctx context.Context, operation FabricOperation, status string, attachment StorageAttachment, operationErr error) error {
	operation.Status = status
	operation.FinishedAt = s.now()
	operation.ErrorCode = errorCode(operationErr)
	operation.Retryable = false
	fillOperationResource(&operation, attachment)
	return s.operations.SaveRuntime(ctx, operation)
}

func (s *Service) DetachStorageAttachment(ctx context.Context, attachmentID string) (StorageAttachment, error) {
	s.mu.Lock()
	existing := s.attachments[attachmentID]
	s.mu.Unlock()
	if existing.ID == "" {
		operation := newOperation("detach_storage_attachment", "storage_attachment", attachmentID, "", "", "", hashInput(map[string]string{"id": attachmentID}), time.Now().UTC())
		operation.ProviderRequestID = providerRequestID("detach-attachment", attachmentID)
		err := fmt.Errorf("storage_attachment_not_found")
		_ = s.recordOperation(ctx, operation, "rejected", StorageAttachment{ID: attachmentID}, err)
		return StorageAttachment{}, err
	}
	operation := newOperation("detach_storage_attachment", "storage_attachment", attachmentID, "", existing.WorkspaceID, "", hashInput(map[string]string{"id": attachmentID}), time.Now().UTC())
	if err := s.recordOperation(ctx, operation, "started", existing, nil); err != nil {
		return StorageAttachment{}, err
	}
	attachment, err := s.provider.DetachStorageAttachment(ctx, existing)
	if err != nil {
		_ = s.recordOperation(ctx, operation, "failed", attachment, err)
		return attachment, err
	}
	if err := s.recordOperation(ctx, operation, "succeeded", attachment, nil); err != nil {
		return attachment, err
	}
	s.mu.Lock()
	s.attachments[attachmentID] = attachment
	s.mu.Unlock()
	return attachment, nil
}

func (s *Service) CreateWorkspaceRuntime(ctx context.Context, input WorkspaceRuntimeInput) (WorkspaceRuntime, error) {
	if strings.TrimSpace(input.IdempotencyKey) == "" {
		return WorkspaceRuntime{}, fmt.Errorf("runtime_idempotency_key_required")
	}
	requestHash := hashInput(input)
	s.mu.Lock()
	compute := s.computes[input.ComputeID]
	volume := s.volumes[input.VolumeID]
	s.mu.Unlock()
	now := s.now()
	operation := newOperation("create_workspace_runtime", "workspace_runtime", input.WorkspaceID, compute.AccountID, input.WorkspaceID, input.IdempotencyKey, requestHash, now)
	operation.ID = "fop_runtime_claim_" + stableSuffix("create_workspace_runtime", input.IdempotencyKey)
	operation.Status = "started"
	operation.CreatedAt = now
	fillOperationResource(&operation, WorkspaceRuntime{WorkspaceID: input.WorkspaceID, ProviderRequestID: providerRequestID("runtime", input.IdempotencyKey)})
	input.OperationID = operation.OperationID
	stored, claimed, err := s.operations.ClaimRuntime(ctx, operation)
	if err != nil {
		return WorkspaceRuntime{}, err
	}
	if !claimed {
		return replayRuntimeOperation(stored, requestHash)
	}
	if err := validateRuntimeInput(input, compute, volume); err != nil {
		_ = s.saveRuntimeOperation(ctx, stored, "failed", WorkspaceRuntime{WorkspaceID: input.WorkspaceID, ProviderRequestID: stored.ProviderRequestID}, err)
		return WorkspaceRuntime{}, err
	}
	runtime, err := s.provider.CreateWorkspaceRuntime(ctx, input, compute, volume)
	runtime.Access.Password = ""
	if err != nil {
		_ = s.saveRuntimeOperation(ctx, stored, "failed", runtime, err)
		return runtime, err
	}
	if err := s.saveRuntimeOperation(ctx, stored, "succeeded", runtime, nil); err != nil {
		return runtime, err
	}
	return runtime, nil
}

func (s *Service) DestroyWorkspaceRuntime(ctx context.Context, workspaceID, idempotencyKey string) (WorkspaceRuntime, error) {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(idempotencyKey) == "" {
		return WorkspaceRuntime{}, fmt.Errorf("runtime_destroy_identity_required")
	}
	requestHash := hashInput(map[string]string{"workspaceId": workspaceID})
	now := s.now()
	operation := newOperation("destroy_workspace_runtime", "workspace_runtime", workspaceID, "", workspaceID, idempotencyKey, requestHash, now)
	operation.ID = "fop_runtime_destroy_claim_" + stableSuffix("destroy_workspace_runtime", idempotencyKey)
	operation.Status = "started"
	operation.CreatedAt = now
	fillOperationResource(&operation, WorkspaceRuntime{WorkspaceID: workspaceID, ProviderRequestID: providerRequestID("runtime-destroy", idempotencyKey)})
	stored, claimed, err := s.operations.ClaimRuntime(ctx, operation)
	if err != nil {
		return WorkspaceRuntime{}, err
	}
	if !claimed {
		return replayRuntimeOperation(stored, requestHash)
	}
	runtime, err := s.provider.DestroyWorkspaceRuntime(ctx, workspaceID)
	runtime.Access.Password = ""
	runtime.WorkspaceID = firstNonEmpty(runtime.WorkspaceID, workspaceID)
	runtime.ProviderRequestID = firstNonEmpty(runtime.ProviderRequestID, providerRequestID("runtime-destroy", idempotencyKey))
	if err != nil {
		_ = s.saveRuntimeOperation(ctx, stored, "failed", runtime, err)
		return runtime, err
	}
	if err := s.saveRuntimeOperation(ctx, stored, "succeeded", runtime, nil); err != nil {
		return runtime, err
	}
	return runtime, nil
}

func replayRuntimeOperation(operation FabricOperation, requestHash string) (WorkspaceRuntime, error) {
	if operation.RequestHash != requestHash {
		return WorkspaceRuntime{}, ErrRuntimeIdempotencyConflict
	}
	switch operation.Status {
	case "started":
		return WorkspaceRuntime{}, ErrRuntimeOperationInProgress
	case "succeeded":
		var runtime WorkspaceRuntime
		if decodeOperationResource(operation, &runtime) {
			runtime.Access.Password = ""
			return runtime, nil
		}
	}
	// ponytail: provider apply is not safely repeatable; reconciliation must resolve failed or corrupt claims.
	return WorkspaceRuntime{}, ErrRuntimeOperationFailed
}

func (s *Service) saveRuntimeOperation(ctx context.Context, operation FabricOperation, status string, runtime WorkspaceRuntime, operationErr error) error {
	operation.Status = status
	operation.FinishedAt = s.now()
	operation.ErrorCode = errorCode(operationErr)
	operation.Retryable = false
	fillOperationResource(&operation, runtime)
	return s.operations.SaveRuntime(ctx, operation)
}

func (s *Service) WorkspaceRuntimeStatus(ctx context.Context, workspaceID string) (WorkspaceRuntime, error) {
	runtime, err := s.provider.WorkspaceRuntimeStatus(ctx, workspaceID)
	if err != nil {
		return runtime, err
	}
	return runtime, nil
}

func (s *Service) UpsertGatewaySecret(ctx context.Context, input GatewaySecretInput) (GatewaySecret, error) {
	if strings.TrimSpace(input.AccountID) == "" || strings.TrimSpace(input.GatewayAPIKey) == "" || strings.TrimSpace(input.IdempotencyKey) == "" {
		return GatewaySecret{}, fmt.Errorf("gateway_secret_input_required")
	}
	keyDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(input.GatewayAPIKey)))
	requestHash := hashInput(map[string]string{"accountId": input.AccountID, "keyDigest": keyDigest})
	now := s.now()
	secretRef := gatewaySecretName(input.AccountID)
	operation := newOperation("upsert_gateway_secret", "gateway_secret", secretRef, input.AccountID, "", input.IdempotencyKey, requestHash, now)
	operation.ID = "fop_gateway_secret_claim_" + stableSuffix("upsert_gateway_secret", input.IdempotencyKey)
	operation.Status = "started"
	operation.CreatedAt = now
	operation.ProviderRequestID = providerRequestID("gateway-secret", input.IdempotencyKey)
	operation.RedactedProviderPayload = map[string]any{"resource": GatewaySecret{SecretRef: secretRef}, "keyDigest": keyDigest}
	stored, claimed, err := s.operations.ClaimRuntime(ctx, operation)
	if err != nil {
		return GatewaySecret{}, err
	}
	if !claimed {
		if stored.RequestHash != requestHash {
			return GatewaySecret{}, ErrGatewaySecretIdempotencyConflict
		}
		if stored.Status == "succeeded" {
			var replayed GatewaySecret
			if decodeOperationResource(stored, &replayed) {
				return replayed, nil
			}
		}
		return GatewaySecret{}, fmt.Errorf("gateway_secret_operation_%s", stored.Status)
	}
	secret, providerErr := s.provider.UpsertGatewaySecret(ctx, input)
	stored.Status = operationStatus(providerErr)
	stored.FinishedAt = s.now()
	stored.ErrorCode = errorCode(providerErr)
	stored.RedactedProviderPayload = map[string]any{"resource": secret, "keyDigest": keyDigest}
	if saveErr := s.operations.SaveRuntime(ctx, stored); saveErr != nil && providerErr == nil {
		return GatewaySecret{}, saveErr
	}
	return secret, providerErr
}

func (s *Service) Readiness(ctx context.Context) (map[string]any, error) {
	return s.provider.Readiness(ctx)
}

func (s *Service) ListOperations(ctx context.Context) ([]FabricOperation, error) {
	return s.operations.List(ctx)
}

func (s *Service) CreateJob(ctx context.Context, input JobInput) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if input.OrganizationID == "" || input.WorkspaceID == "" || input.ProjectID == "" || input.TaskID == "" || input.RequestID == "" || input.ApprovalID == "" || input.IdempotencyKey == "" {
		return Job{}, ErrInvalidJobInput
	}
	requestHash := hashInput(input)
	operations, err := s.operations.List(ctx)
	if err != nil {
		return Job{}, err
	}
	// ponytail: linear scan is enough for the initial job volume; add an indexed store query when measured throughput requires it.
	for _, operation := range operations {
		if operation.ResourceKind != "job" || operation.Action != "create_job" || operation.IdempotencyKey != input.IdempotencyKey {
			continue
		}
		if operation.RequestHash != requestHash {
			return Job{}, ErrJobIdempotencyConflict
		}
		var job Job
		if decodeOperationResource(operation, &job) {
			job.Replayed = true
			return job, nil
		}
	}
	now := s.now()
	job := Job{
		JobID:          "job-" + stableSuffix(input.IdempotencyKey, input.RequestID, input.TaskID)[:16],
		OrganizationID: input.OrganizationID,
		WorkspaceID:    input.WorkspaceID,
		ProjectID:      input.ProjectID,
		TaskID:         input.TaskID,
		RequestID:      input.RequestID,
		ApprovalID:     input.ApprovalID,
		EnvironmentRef: input.EnvironmentRef,
		Status:         "queued",
		Attempt:        1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	operation := newOperation("create_job", "job", job.JobID, "", input.WorkspaceID, input.IdempotencyKey, requestHash, now)
	operation.ProviderRequestID = job.JobID
	if err := s.recordOperation(ctx, operation, job.Status, job, nil); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) Job(ctx context.Context, jobID string) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	return s.jobLocked(ctx, jobID, true)
}

func (s *Service) jobLocked(ctx context.Context, jobID string, expire bool) (Job, error) {
	operations, err := s.operations.List(ctx)
	if err != nil {
		return Job{}, err
	}
	var job Job
	leaseTokenHash := ""
	found := false
	for _, operation := range operations {
		if operation.ResourceKind == "job" && operation.ResourceID == jobID && decodeOperationResource(operation, &job) {
			found = true
			leaseTokenHash, _ = operation.RedactedProviderPayload["leaseTokenHash"].(string)
		}
	}
	if !found {
		return Job{}, ErrJobNotFound
	}
	job.leaseTokenHash = leaseTokenHash
	if expire && job.Status == "running" && job.LeaseExpiresAt != nil && !s.now().Before(*job.LeaseExpiresAt) {
		job.Status = "timed_out"
		job.ErrorCode = "lease_expired"
		job.UpdatedAt = s.now()
		if err := s.appendJobTransition(ctx, "timeout_job", "timeout-"+job.JobID+fmt.Sprintf("-%d", job.Attempt), hashInput(map[string]any{"jobId": job.JobID, "attempt": job.Attempt}), job, "runner"); err != nil {
			return Job{}, err
		}
	}
	return job, nil
}

func (s *Service) CancelJob(ctx context.Context, jobID string, idempotencyKey string) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if idempotencyKey == "" {
		return Job{}, ErrInvalidJobInput
	}
	requestHash := hashInput(map[string]string{"jobId": jobID})
	if replayed, ok, err := s.replayedJobTransition(ctx, "cancel_job", jobID, idempotencyKey, requestHash); ok || err != nil {
		return replayed, err
	}
	job, err := s.jobLocked(ctx, jobID, true)
	if err != nil {
		return Job{}, err
	}
	if job.Status == "cancelled" {
		job.Replayed = true
		return job, nil
	}
	if job.Status != "queued" && job.Status != "running" {
		return Job{}, ErrJobStateConflict
	}
	now := s.now()
	job.Status = "cancelled"
	job.UpdatedAt = now
	if err := s.appendJobTransition(ctx, "cancel_job", idempotencyKey, requestHash, job, "control-plane"); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) ClaimJob(ctx context.Context, jobID string, input JobClaimInput) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if jobID == "" || input.RunnerID == "" || input.IdempotencyKey == "" {
		return Job{}, ErrInvalidJobInput
	}
	requestHash := hashInput(map[string]string{"jobId": jobID, "runnerId": input.RunnerID})
	if replayed, ok, err := s.replayedJobTransition(ctx, "claim_job", jobID, input.IdempotencyKey, requestHash); ok || err != nil {
		return replayed, err
	}
	job, err := s.jobLocked(ctx, jobID, true)
	if err != nil {
		return Job{}, err
	}
	if job.Status != "queued" {
		return Job{}, ErrJobStateConflict
	}
	now := s.now()
	token, err := newLeaseToken()
	if err != nil {
		return Job{}, err
	}
	expiresAt := now.Add(30 * time.Second)
	job.Status = "running"
	job.LeaseOwner = input.RunnerID
	job.LeaseExpiresAt = &expiresAt
	job.LeaseToken = token
	job.leaseTokenHash = stableSuffix(token)
	job.UpdatedAt = now
	if err := s.appendJobTransition(ctx, "claim_job", input.IdempotencyKey, requestHash, job, "runner"); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) HeartbeatJob(ctx context.Context, jobID string, input JobHeartbeatInput) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if jobID == "" || input.RunnerID == "" || input.LeaseToken == "" || input.IdempotencyKey == "" {
		return Job{}, ErrInvalidJobInput
	}
	requestHash := hashInput(map[string]string{"jobId": jobID, "runnerId": input.RunnerID, "leaseTokenHash": stableSuffix(input.LeaseToken)})
	if replayed, ok, err := s.replayedJobTransition(ctx, "heartbeat_job", jobID, input.IdempotencyKey, requestHash); ok || err != nil {
		return replayed, err
	}
	job, err := s.activeLeasedJob(ctx, jobID, input.RunnerID, input.LeaseToken)
	if err != nil {
		return Job{}, err
	}
	now := s.now()
	expiresAt := now.Add(30 * time.Second)
	job.LeaseExpiresAt = &expiresAt
	job.UpdatedAt = now
	if err := s.appendJobTransition(ctx, "heartbeat_job", input.IdempotencyKey, requestHash, job, "runner"); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) CompleteJob(ctx context.Context, jobID string, input JobCompleteInput) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if jobID == "" || input.RunnerID == "" || input.LeaseToken == "" || len(input.ArtifactIDs) == 0 || len(input.ReviewIDs) == 0 || input.IdempotencyKey == "" {
		return Job{}, ErrInvalidJobInput
	}
	requestHash := hashInput(struct {
		JobID, RunnerID, LeaseTokenHash string
		ArtifactIDs, ReviewIDs          []string
	}{jobID, input.RunnerID, stableSuffix(input.LeaseToken), input.ArtifactIDs, input.ReviewIDs})
	if replayed, ok, err := s.replayedJobTransition(ctx, "complete_job", jobID, input.IdempotencyKey, requestHash); ok || err != nil {
		return replayed, err
	}
	job, err := s.activeLeasedJob(ctx, jobID, input.RunnerID, input.LeaseToken)
	if err != nil {
		return Job{}, err
	}
	job.Status = "succeeded"
	job.ArtifactIDs = append([]string(nil), input.ArtifactIDs...)
	job.ReviewIDs = append([]string(nil), input.ReviewIDs...)
	job.ErrorCode = ""
	job.UpdatedAt = s.now()
	if err := s.appendJobTransition(ctx, "complete_job", input.IdempotencyKey, requestHash, job, "runner"); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) FailJob(ctx context.Context, jobID string, input JobFailInput) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if jobID == "" || input.RunnerID == "" || input.LeaseToken == "" || input.ErrorCode == "" || input.IdempotencyKey == "" {
		return Job{}, ErrInvalidJobInput
	}
	requestHash := hashInput(map[string]string{"jobId": jobID, "runnerId": input.RunnerID, "leaseTokenHash": stableSuffix(input.LeaseToken), "errorCode": input.ErrorCode})
	if replayed, ok, err := s.replayedJobTransition(ctx, "fail_job", jobID, input.IdempotencyKey, requestHash); ok || err != nil {
		return replayed, err
	}
	job, err := s.activeLeasedJob(ctx, jobID, input.RunnerID, input.LeaseToken)
	if err != nil {
		return Job{}, err
	}
	job.Status = "failed"
	job.ErrorCode = input.ErrorCode
	job.UpdatedAt = s.now()
	if err := s.appendJobTransition(ctx, "fail_job", input.IdempotencyKey, requestHash, job, "runner"); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) RetryJob(ctx context.Context, jobID, idempotencyKey string) (Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if jobID == "" || idempotencyKey == "" {
		return Job{}, ErrInvalidJobInput
	}
	requestHash := hashInput(map[string]string{"jobId": jobID})
	if replayed, ok, err := s.replayedJobTransition(ctx, "retry_job", jobID, idempotencyKey, requestHash); ok || err != nil {
		return replayed, err
	}
	job, err := s.jobLocked(ctx, jobID, true)
	if err != nil {
		return Job{}, err
	}
	if job.Status != "failed" && job.Status != "timed_out" {
		return Job{}, ErrJobStateConflict
	}
	job.Status = "queued"
	job.Attempt++
	job.LeaseOwner = ""
	job.LeaseExpiresAt = nil
	job.LeaseToken = ""
	job.leaseTokenHash = ""
	job.ArtifactIDs = nil
	job.ReviewIDs = nil
	job.ErrorCode = ""
	job.UpdatedAt = s.now()
	if err := s.appendJobTransition(ctx, "retry_job", idempotencyKey, requestHash, job, "control-plane"); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) activeLeasedJob(ctx context.Context, jobID, runnerID, leaseToken string) (Job, error) {
	job, err := s.jobLocked(ctx, jobID, true)
	if err != nil {
		return Job{}, err
	}
	if job.Status != "running" {
		return Job{}, ErrJobStateConflict
	}
	if job.LeaseOwner != runnerID || subtle.ConstantTimeCompare([]byte(job.leaseTokenHash), []byte(stableSuffix(leaseToken))) != 1 {
		return Job{}, ErrJobLeaseMismatch
	}
	return job, nil
}

func (s *Service) replayedJobTransition(ctx context.Context, action, jobID, idempotencyKey, requestHash string) (Job, bool, error) {
	operations, err := s.operations.List(ctx)
	if err != nil {
		return Job{}, false, err
	}
	for _, operation := range operations {
		if operation.ResourceKind != "job" || operation.ResourceID != jobID || operation.Action != action || operation.IdempotencyKey != idempotencyKey {
			continue
		}
		if operation.RequestHash != requestHash {
			return Job{}, false, ErrJobIdempotencyConflict
		}
		var job Job
		if decodeOperationResource(operation, &job) {
			job.Replayed = true
			return job, true, nil
		}
	}
	return Job{}, false, nil
}

func (s *Service) appendJobTransition(ctx context.Context, action, idempotencyKey, requestHash string, job Job, caller string) error {
	operation := newOperation(action, "job", job.JobID, "", job.WorkspaceID, idempotencyKey, requestHash, s.now())
	operation.ProviderRequestID = job.JobID
	operation.CallerService = caller
	return s.recordOperation(ctx, operation, job.Status, job, nil)
}

func newLeaseToken() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return "lease-" + hex.EncodeToString(data), nil
}

func replayResourceState(ctx context.Context, operations OperationStore) (map[string]ComputeAllocation, map[string]StorageVolume, map[string]StorageSnapshot, map[string]StorageAttachment, map[string]WorkspaceRuntime) {
	computes := map[string]ComputeAllocation{}
	volumes := map[string]StorageVolume{}
	snapshots := map[string]StorageSnapshot{}
	attachments := map[string]StorageAttachment{}
	runtimes := map[string]WorkspaceRuntime{}
	records, err := operations.List(ctx)
	if err != nil {
		return computes, volumes, snapshots, attachments, runtimes
	}
	for _, operation := range records {
		switch operation.ResourceKind {
		case "compute_allocation":
			var resource ComputeAllocation
			if !decodeOperationResource(operation, &resource) {
				continue
			}
			if operation.Status == "started" && operation.Action != "create_compute_allocation" {
				continue
			}
			if operation.Status == "failed" && !strings.HasPrefix(operation.Action, "create_") {
				continue
			}
			computes[resource.ID] = resource
		case "storage_volume":
			var resource StorageVolume
			if !decodeOperationResource(operation, &resource) {
				continue
			}
			if operation.Status != "succeeded" {
				if operation.Status != "failed" || operation.Action != "create_storage_volume" || !strings.HasPrefix(resource.ProviderResourceID, "disk-") {
					continue
				}
				resource.Status = "quarantined"
			}
			volumes[resource.ID] = resource
		case "storage_snapshot":
			var resource StorageSnapshot
			if operation.Status != "succeeded" || !decodeOperationResource(operation, &resource) {
				continue
			}
			snapshots[resource.ID] = resource
		case "storage_attachment":
			var resource StorageAttachment
			if operation.Status != "succeeded" || !decodeOperationResource(operation, &resource) {
				continue
			}
			attachments[resource.ID] = resource
		case "workspace_runtime":
			var resource WorkspaceRuntime
			if operation.Status != "succeeded" || !decodeOperationResource(operation, &resource) {
				continue
			}
			runtimes[resource.WorkspaceID] = resource
		}
	}
	return computes, volumes, snapshots, attachments, runtimes
}

func decodeOperationResource(operation FabricOperation, target any) bool {
	resource, ok := operation.RedactedProviderPayload["resource"]
	if !ok {
		return false
	}
	data, err := json.Marshal(resource)
	if err != nil {
		return false
	}
	return json.Unmarshal(data, target) == nil
}

func newOperation(action string, resourceKind string, resourceID string, accountID string, workspaceID string, idempotencyKey string, requestHash string, now time.Time) FabricOperation {
	operationID := "op_" + action + "_" + stableSuffix(firstNonEmpty(idempotencyKey, resourceID, accountID, workspaceID, fmt.Sprintf("%d", now.UnixNano())), resourceKind, action)[:12]
	return FabricOperation{
		OperationID:    operationID,
		CallerService:  "control-plane",
		Action:         action,
		ResourceKind:   resourceKind,
		ResourceID:     resourceID,
		AccountID:      accountID,
		WorkspaceID:    workspaceID,
		IdempotencyKey: idempotencyKey,
		RequestHash:    requestHash,
		StartedAt:      now,
	}
}

func (s *Service) recordOperation(ctx context.Context, base FabricOperation, status string, resource any, operationErr error) error {
	now := s.now()
	operation := base
	operation.ID = fabricID("fop", firstNonEmpty(base.OperationID, base.ResourceID)+"_"+status, now)
	operation.Status = status
	operation.CreatedAt = now
	if status != "started" {
		operation.FinishedAt = now
	}
	if operationErr != nil {
		operation.ErrorCode = errorCode(operationErr)
	}
	fillOperationResource(&operation, resource)
	return s.operations.Append(ctx, operation)
}

func fillOperationResource(operation *FabricOperation, resource any) {
	switch value := resource.(type) {
	case ComputeAllocation:
		operation.ResourceID = firstNonEmpty(value.ID, operation.ResourceID)
		operation.AccountID = firstNonEmpty(value.AccountID, operation.AccountID)
		operation.WorkspaceID = firstNonEmpty(value.WorkspaceID, operation.WorkspaceID)
		operation.Provider = firstNonEmpty(value.Provider, operation.Provider)
		operation.ProviderRequestID = firstNonEmpty(value.ProviderRequestID, operation.ProviderRequestID)
		operation.RedactedProviderPayload = map[string]any{"resource": value, "providerResourceId": value.ProviderResourceID, "nodeName": value.NodeName, "instanceId": firstNonEmpty(value.CVMInstanceID, value.InstanceID), "costTags": value.CostTags}
	case StorageVolume:
		operation.ResourceID = firstNonEmpty(value.ID, operation.ResourceID)
		operation.AccountID = firstNonEmpty(value.AccountID, operation.AccountID)
		operation.WorkspaceID = firstNonEmpty(value.WorkspaceID, operation.WorkspaceID)
		operation.Provider = firstNonEmpty(value.Provider, operation.Provider)
		operation.ProviderRequestID = firstNonEmpty(value.ProviderRequestID, operation.ProviderRequestID)
		operation.RedactedProviderPayload = map[string]any{"resource": value, "providerResourceId": value.ProviderResourceID, "storageClass": value.StorageClass, "sizeGb": value.SizeGB, "costTags": value.CostTags}
	case StorageSnapshot:
		operation.ResourceID = firstNonEmpty(value.ID, operation.ResourceID)
		operation.AccountID = firstNonEmpty(value.AccountID, operation.AccountID)
		operation.WorkspaceID = firstNonEmpty(value.WorkspaceID, operation.WorkspaceID)
		operation.Provider = firstNonEmpty(value.Provider, operation.Provider)
		operation.ProviderRequestID = firstNonEmpty(value.ProviderRequestID, operation.ProviderRequestID)
		operation.RedactedProviderPayload = map[string]any{"resource": value, "providerSnapshotRef": value.ProviderSnapshotRef, "volumeId": value.VolumeID, "snapshotClass": value.SnapshotClass}
	case StorageAttachment:
		operation.ResourceID = firstNonEmpty(value.ID, operation.ResourceID)
		operation.WorkspaceID = firstNonEmpty(value.WorkspaceID, operation.WorkspaceID)
		operation.Provider = firstNonEmpty(value.Provider, operation.Provider)
		operation.ProviderRequestID = firstNonEmpty(value.ProviderRequestID, operation.ProviderRequestID)
		operation.RedactedProviderPayload = map[string]any{"resource": value, "providerAttachmentId": value.ProviderAttachmentID, "computeId": value.ComputeID, "volumeId": value.VolumeID, "costTags": value.CostTags}
	case WorkspaceRuntime:
		redacted := value
		credentialConfigured := value.Access.CredentialStatus == "configured" || value.Access.Password != ""
		if redacted.Access.Password != "" {
			redacted.Access.Password = ""
			redacted.Access.CredentialStatus = firstNonEmpty(redacted.Access.CredentialStatus, "configured")
		}
		operation.ResourceID = firstNonEmpty(value.WorkspaceID, operation.ResourceID)
		operation.WorkspaceID = firstNonEmpty(value.WorkspaceID, operation.WorkspaceID)
		operation.ProviderRequestID = firstNonEmpty(value.ProviderRequestID, operation.ProviderRequestID)
		operation.RedactedProviderPayload = map[string]any{"resource": redacted, "serviceName": value.ServiceName, "ready": value.Ready, "credentialConfigured": credentialConfigured, "credentialVersion": value.Access.CredentialVersion, "secretRef": value.Access.SecretRef, "costTags": value.CostTags}
	case Job:
		redacted := value
		redacted.LeaseToken = ""
		redacted.leaseTokenHash = ""
		operation.ResourceID = value.JobID
		operation.WorkspaceID = value.WorkspaceID
		operation.ProviderRequestID = firstNonEmpty(operation.ProviderRequestID, value.JobID)
		operation.RedactedProviderPayload = map[string]any{"resource": redacted, "leaseTokenHash": value.leaseTokenHash}
	}
}

func operationStatus(err error) string {
	if err != nil {
		return "failed"
	}
	return "succeeded"
}

func errorCode(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	if text == "" {
		return "provider_error"
	}
	return strings.Fields(text)[0]
}

func validateAttachmentInput(input StorageAttachmentInput, compute ComputeAllocation, volume StorageVolume) error {
	if compute.ID == "" {
		return fmt.Errorf("compute_allocation_not_found")
	}
	if volume.ID == "" {
		return fmt.Errorf("storage_volume_not_found")
	}
	if compute.AccountID == "" || volume.AccountID == "" || compute.AccountID != volume.AccountID {
		return fmt.Errorf("resource_account_mismatch")
	}
	if strings.TrimSpace(input.WorkspaceID) == "" || input.WorkspaceID != compute.WorkspaceID || input.WorkspaceID != volume.WorkspaceID {
		return fmt.Errorf("resource_workspace_mismatch")
	}
	if !isReadyResourceStatus(compute.Status) || volume.Status != "ready" {
		return fmt.Errorf("resource_status_invalid")
	}
	return nil
}

func validComputeRenewal(existing, renewed ComputeAllocation) bool {
	instanceID := firstNonEmpty(existing.InstanceID, existing.CVMInstanceID)
	if !validComputeRenewalIdentity(existing) || !validComputeRenewalIdentity(renewed) || renewed.ProviderData["instanceType"] != existing.ProviderData["instanceType"] || renewed.ProviderData["zone"] != existing.ProviderData["zone"] {
		return false
	}
	for _, key := range []string{"opl_account_id", "opl_workspace_id", "opl_resource_id", "opl_operation_id"} {
		if renewed.CostTags[key] != existing.CostTags[key] {
			return false
		}
	}
	return renewed.ID == existing.ID && renewed.AccountID == existing.AccountID && renewed.WorkspaceID == existing.WorkspaceID &&
		firstNonEmpty(renewed.InstanceID, renewed.CVMInstanceID) == instanceID &&
		(renewed.InstanceID == "" || renewed.InstanceID == instanceID) && (renewed.CVMInstanceID == "" || renewed.CVMInstanceID == instanceID) &&
		renewed.ChargeType == "PREPAID" && renewed.RenewFlag == "NOTIFY_AND_MANUAL_RENEW" && renewalDeadlineIncreased(existing.Deadline, renewed.Deadline)
}

func validComputeRenewalIdentity(allocation ComputeAllocation) bool {
	instanceID := firstNonEmpty(allocation.InstanceID, allocation.CVMInstanceID)
	if allocation.ID == "" || allocation.AccountID == "" || allocation.WorkspaceID == "" || !strings.HasPrefix(instanceID, "ins-") || strings.TrimSpace(allocation.Deadline) == "" || strings.TrimSpace(allocation.ProviderData["instanceType"]) == "" || strings.TrimSpace(allocation.ProviderData["zone"]) == "" {
		return false
	}
	return allocation.CostTags["opl_account_id"] == allocation.AccountID && allocation.CostTags["opl_workspace_id"] == allocation.WorkspaceID && allocation.CostTags["opl_resource_id"] == allocation.ID && strings.TrimSpace(allocation.CostTags["opl_operation_id"]) != ""
}

func validStorageRenewal(existing, renewed StorageVolume) bool {
	return renewed.ID == existing.ID && renewed.AccountID == existing.AccountID && renewed.WorkspaceID == existing.WorkspaceID &&
		renewed.ProviderResourceID == existing.ProviderResourceID && renewed.ProviderData["diskChargeType"] == "PREPAID" &&
		renewed.RenewFlag == "NOTIFY_AND_MANUAL_RENEW" && renewalDeadlineIncreased(existing.Deadline, renewed.Deadline)
}

func renewalDeadlineIncreased(previous, current string) bool {
	previousTime, previousErr := time.Parse(time.RFC3339, previous)
	currentTime, currentErr := time.Parse(time.RFC3339, current)
	return previousErr == nil && currentErr == nil && currentTime.After(previousTime)
}

func validateRuntimeInput(input WorkspaceRuntimeInput, compute ComputeAllocation, volume StorageVolume) error {
	if compute.ID == "" {
		return fmt.Errorf("compute_allocation_not_found")
	}
	if volume.ID == "" {
		return fmt.Errorf("storage_volume_not_found")
	}
	if compute.AccountID == "" || volume.AccountID == "" || compute.AccountID != volume.AccountID {
		return fmt.Errorf("resource_account_mismatch")
	}
	if strings.TrimSpace(input.WorkspaceID) == "" || input.WorkspaceID != compute.WorkspaceID || input.WorkspaceID != volume.WorkspaceID {
		return fmt.Errorf("resource_workspace_mismatch")
	}
	if !isReadyResourceStatus(compute.Status) || volume.Status != "ready" {
		return fmt.Errorf("resource_status_invalid")
	}
	if strings.TrimSpace(input.GatewaySecretRef) == "" || input.GatewaySecretRef != gatewaySecretName(compute.AccountID) {
		return fmt.Errorf("gateway_secret_ref_mismatch")
	}
	return nil
}

func isReadyResourceStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running", "ready", "active":
		return true
	default:
		return false
	}
}

func isRetainedStorageStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "retained", "released":
		return true
	default:
		return false
	}
}

func hashInput(input any) string {
	data, err := json.Marshal(input)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func stableSuffix(values ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(values, ":")))
	return hex.EncodeToString(sum[:])
}

func fabricID(prefix string, owner string, now time.Time) string {
	return fmt.Sprintf("%s_%s_%d", prefix, owner, now.UnixNano())
}

func providerRequestID(prefix string, key string) string {
	if key == "" {
		key = "no-idempotency-key"
	}
	return fmt.Sprintf("%s_%s", prefix, key)
}
