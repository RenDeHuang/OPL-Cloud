package fabric

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

var errStorageDestroyRecoveryUnconfirmed = errors.New("storage_destroy_recovery_unconfirmed")

const storageDestroyPhaseDispatchAuthorized = "dispatch_authorized_uncertain"

func attachmentReadbackMatches(result StorageAttachment, input StorageAttachmentInput, compute ComputeAllocation, volume StorageVolume) bool {
	return strings.HasPrefix(result.ID, "att_") && result.OperationID == input.IdempotencyKey &&
		result.WorkspaceID == input.WorkspaceID && result.ComputeID == input.ComputeID && result.VolumeID == input.VolumeID &&
		result.Status == "attached" && result.ProviderAttachmentID != "" && result.ProviderRequestID != "" &&
		compute.AccountID != "" && compute.WorkspaceID == input.WorkspaceID && volume.AccountID == compute.AccountID && volume.WorkspaceID == input.WorkspaceID
}
func (s *Service) CreateStorageVolume(ctx context.Context, input StorageVolumeInput) (StorageVolume, error) {
	input.AllowExistingExactReplay = false
	if input.SizeGB < 10 || input.SizeGB%10 != 0 {
		return StorageVolume{}, ErrInvalidStorageSize
	}
	if !validStorageRecoveryExpectation(input.ExpectedRecoveryState, input.ExpectedProviderResourceID) {
		return StorageVolume{}, fmt.Errorf("storage_recovery_expectation_invalid")
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
	computeZone := strings.TrimSpace(compute.Zone)
	if computeZone == "" {
		computeZone = strings.TrimSpace(compute.ProviderData["zone"])
	}
	if compute.ID == "" || compute.AccountID != input.AccountID || compute.WorkspaceID != input.WorkspaceID ||
		!isReadyResourceStatus(compute.Status) || computeZone == "" || strings.TrimSpace(input.Zone) != computeZone {
		return StorageVolume{}, fmt.Errorf("storage_compute_zone_mismatch")
	}
	requestHash := hashInput(input)
	var volume StorageVolume
	lockKey := "storage-create:" + firstNonEmpty(input.IdempotencyKey, input.ID)
	err := s.operations.WithPoolLock(ctx, lockKey, func(lockCtx context.Context) error {
		var err error
		operation := newOperation("create_storage_volume", "storage_volume", input.ID, input.AccountID, input.WorkspaceID, input.IdempotencyKey, requestHash, s.now())
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
			input.AllowExistingExactReplay = true
			break
		}
		input.OperationID = operation.OperationID
		if err := s.recordOperation(lockCtx, operation, "started", StorageVolume{ID: input.ID, OperationID: input.IdempotencyKey, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Provider: s.provider.Descriptor().Name, ProviderRequestID: providerRequestID("storage", input.IdempotencyKey)}, nil); err != nil {
			return err
		}
		volume, err = s.provider.CreateStorageVolume(s.providerMutationContext(lockCtx, operation), input)
		volume.ID = input.ID
		volume.OperationID = input.IdempotencyKey
		volume.AccountID = firstNonEmpty(volume.AccountID, input.AccountID)
		volume.WorkspaceID = firstNonEmpty(volume.WorkspaceID, input.WorkspaceID)
		volume.Provider = firstNonEmpty(volume.Provider, s.provider.Descriptor().Name)
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

func validStorageRecoveryExpectation(state, providerResourceID string) bool {
	switch state {
	case "":
		return providerResourceID == ""
	case "storage_not_started":
		return providerResourceID == ""
	case "storage_existing_exact":
		return strings.HasPrefix(providerResourceID, "disk-")
	default:
		return false
	}
}

func (s *Service) GetStorageVolume(_ context.Context, volumeID string) (StorageVolume, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	volume, ok := s.volumes[volumeID]
	return volume, ok
}

func (s *Service) ReadStorageVolume(ctx context.Context, volumeID string) (StorageVolume, error) {
	s.mu.Lock()
	existing := cloneStorageVolume(s.volumes[volumeID])
	s.mu.Unlock()
	if existing.ID == "" {
		return StorageVolume{}, fmt.Errorf("storage_volume_not_found")
	}
	reader, ok := s.provider.(storageVolumeStatusReader)
	if !ok {
		return existing, nil
	}
	volume, err := reader.ReadStorageVolumeStatus(ctx, existing)
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
		volume.Provider = existing.Provider
	}
	if volume.ProviderResourceID == "" {
		volume.ProviderResourceID = existing.ProviderResourceID
	}
	if volume.ProviderRequestID == "" {
		volume.ProviderRequestID = existing.ProviderRequestID
	}
	return volume, err
}

func (s *Service) DestroyStorageVolume(ctx context.Context, volumeID string) (StorageVolume, error) {
	s.mu.Lock()
	missing := s.volumes[volumeID].ID == ""
	s.mu.Unlock()
	if missing {
		if err := s.hydrateMissingResourceState(ctx); err != nil {
			return StorageVolume{}, err
		}
	}

	var result StorageVolume
	err := s.operations.WithPoolLock(ctx, "storage-destroy:"+volumeID, func(lockCtx context.Context) error {
		s.mu.Lock()
		existing := cloneStorageVolume(s.volumes[volumeID])
		s.mu.Unlock()
		result = existing
		if existing.ID == "" {
			operation := newOperation("destroy_storage_volume", "storage_volume", volumeID, "", "", "", hashInput(map[string]string{"id": volumeID}), s.now())
			operation.ProviderRequestID = providerRequestID("destroy-storage", volumeID)
			err := fmt.Errorf("storage_volume_not_found")
			_ = s.recordOperation(lockCtx, operation, "rejected", StorageVolume{ID: volumeID}, err)
			return err
		}

		latest, found, err := s.operations.LatestResourceOperation(lockCtx, "storage_volume", volumeID)
		if err != nil {
			return err
		}
		if found && latest.Action == "destroy_storage_volume" {
			persisted, valid := validStorageDestroyOperation(latest, volumeID)
			if !valid || !sameStorageDestroyStableIdentity(existing, persisted) {
				return fmt.Errorf("storage_destroy_replay_identity_mismatch")
			}
			result = persisted
			switch latest.Status {
			case "succeeded":
				s.mu.Lock()
				s.volumes[volumeID] = cloneStorageVolume(persisted)
				s.mu.Unlock()
				return nil
			case "started":
				return s.recoverStorageDestroyByReadback(lockCtx, latest, existing, persisted, &result)
			case "failed":
				return s.recoverStorageDestroyByReadback(lockCtx, latest, existing, persisted, &result)
			default:
				return fmt.Errorf("storage_destroy_replay_status_invalid")
			}
		}

		operation := newOperation("destroy_storage_volume", "storage_volume", volumeID, existing.AccountID, existing.WorkspaceID, "", hashInput(map[string]string{"id": volumeID}), s.now())
		request := cloneStorageVolume(existing)
		if request.Provider == "tencent-tke" {
			request.Status = "destroying"
			if request.ProviderData == nil {
				request.ProviderData = map[string]string{}
			}
			request.ProviderData["storageDestroyPhase"] = storageDestroyPhaseDispatchAuthorized
			request.ProviderData["storageDestroyMutationCount"] = "0"
		}
		if err := s.recordOperation(lockCtx, operation, "started", request, nil); err != nil {
			return err
		}
		return s.dispatchStorageDestroy(lockCtx, operation, existing, request, &result)
	})
	return result, err
}

func (s *Service) dispatchStorageDestroy(ctx context.Context, operation FabricOperation, existing, request StorageVolume, result *StorageVolume) error {
	volume, providerErr := s.provider.DestroyStorageVolume(ctx, cloneStorageVolume(request))
	if providerErr != nil && volume.ID == "" {
		volume = cloneStorageVolume(request)
	}
	*result = volume
	if !sameStorageDestroyStableIdentity(existing, volume) {
		err := fmt.Errorf("storage_destroy_provider_identity_mismatch")
		_ = s.recordOperation(ctx, operation, "failed", volume, err)
		return err
	}
	if providerErr != nil {
		_ = s.recordOperation(ctx, operation, "failed", volume, providerErr)
		return providerErr
	}
	if err := s.recordOperation(ctx, operation, "succeeded", volume, nil); err != nil {
		return err
	}
	s.mu.Lock()
	s.volumes[existing.ID] = cloneStorageVolume(volume)
	s.mu.Unlock()
	return nil
}

func validStorageDestroyOperation(operation FabricOperation, volumeID string) (StorageVolume, bool) {
	var volume StorageVolume
	if operation.Action != "destroy_storage_volume" || operation.ResourceKind != "storage_volume" || operation.ResourceID != volumeID || operation.IdempotencyKey != "" ||
		operation.RequestHash != hashInput(map[string]string{"id": volumeID}) || !decodeOperationResource(operation, &volume) ||
		volume.ID != volumeID || operation.AccountID != volume.AccountID || operation.WorkspaceID != volume.WorkspaceID ||
		operation.Provider != volume.Provider || operation.ProviderRequestID != volume.ProviderRequestID {
		return StorageVolume{}, false
	}
	return volume, true
}

func sameStorageDestroyStableIdentity(previous, current StorageVolume) bool {
	return previous.ID == current.ID && previous.OperationID == current.OperationID && previous.AccountID == current.AccountID && previous.WorkspaceID == current.WorkspaceID &&
		previous.Provider == current.Provider && previous.ProviderResourceID == current.ProviderResourceID && previous.SizeGB == current.SizeGB &&
		previous.StorageClass == current.StorageClass && previous.DiskType == current.DiskType && previous.RenewFlag == current.RenewFlag &&
		previous.Deadline == current.Deadline && previous.Zone == current.Zone && previous.CreatedAt.Equal(current.CreatedAt) &&
		reflect.DeepEqual(previous.CostTags, current.CostTags) && sameStorageDestroyProviderData(previous.ProviderData, current.ProviderData)
}

func sameStorageDestroyProviderData(previous, current map[string]string) bool {
	for key, previousValue := range previous {
		currentValue, exists := current[key]
		if !exists || previousValue != currentValue && !isStorageDestroyEvidenceKey(key) {
			return false
		}
	}
	for key := range current {
		if _, exists := previous[key]; !exists && !isStorageDestroyEvidenceKey(key) {
			return false
		}
	}
	return true
}

func isStorageDestroyEvidenceKey(key string) bool {
	switch key {
	case "storageVolumeId", "cbsStatus", "status", "terminateCbsRequestId", "describeCbsRequestId", "storageDestroyPhase", "storageDestroyMutationCount":
		return true
	default:
		return false
	}
}

func (s *Service) recoverStorageDestroyByReadback(ctx context.Context, operation FabricOperation, existing, persisted StorageVolume, result *StorageVolume) error {
	reader, ok := s.provider.(storageVolumeStatusReader)
	if !ok {
		return errStorageDestroyRecoveryUnconfirmed
	}
	readback, readErr := reader.ReadStorageVolumeStatus(ctx, cloneStorageVolume(persisted))
	*result = readback
	if !sameStorageDestroyStableIdentity(existing, readback) {
		return fmt.Errorf("storage_destroy_replay_identity_mismatch")
	}
	if readErr != nil {
		return fmt.Errorf("%w: %v", errStorageDestroyRecoveryUnconfirmed, readErr)
	}
	if !storageDestroyReadbackConfirmsAbsence(readback) {
		return errStorageDestroyRecoveryUnconfirmed
	}
	if err := s.recordOperation(ctx, operation, "succeeded", readback, nil); err != nil {
		return err
	}
	s.mu.Lock()
	s.volumes[existing.ID] = cloneStorageVolume(readback)
	s.mu.Unlock()
	return nil
}

func storageDestroyReadbackConfirmsAbsence(volume StorageVolume) bool {
	switch volume.Provider {
	case "tencent-tke":
		return volume.Status == "external_deleted" && volume.CBSStatus == "NOT_FOUND" && strings.HasPrefix(volume.ProviderResourceID, "disk-") &&
			volume.ProviderData["storageVolumeId"] == volume.ProviderResourceID && volume.ProviderData["cbsStatus"] == "NOT_FOUND" &&
			volume.ProviderData["status"] == "external_deleted" && volume.ProviderData["storageDestroyPhase"] == "absence_confirmed" &&
			(volume.ProviderData["storageDestroyMutationCount"] == "0" || volume.ProviderData["storageDestroyMutationCount"] == "1") &&
			strings.TrimSpace(volume.ProviderData["describeCbsRequestId"]) != ""
	case "local-docker":
		return volume.Status == "external_deleted" && strings.HasPrefix(volume.ProviderResourceID, "directory/") &&
			strings.TrimSpace(strings.TrimPrefix(volume.ProviderResourceID, "directory/")) != ""
	default:
		return false
	}
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
		volume.Provider = firstNonEmpty(existing.Provider, s.provider.Descriptor().Name)
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
	// A paid launch owns the original storage identity for its full recovery
	// window. A pending readback must never turn a timeout into a destructive
	// cleanup or a replacement resource; the caller's durable stage budget
	// decides whether to retry or move to manual review.
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
	attachmentID := "att_" + stableSuffix(input.IdempotencyKey)[:18]
	operation := newOperation("create_storage_attachment", "storage_attachment", attachmentID, compute.AccountID, input.WorkspaceID, input.IdempotencyKey, requestHash, now)
	if err := validateAttachmentInput(input, compute, volume); err != nil {
		operation.ProviderRequestID = providerRequestID("storage-attach", input.IdempotencyKey)
		_ = s.recordOperation(ctx, operation, "rejected", StorageAttachment{ID: operation.ResourceID, WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID, ProviderRequestID: operation.ProviderRequestID}, err)
		return StorageAttachment{}, err
	}
	operation.ID = "fop_attachment_claim_" + stableSuffix("create_storage_attachment", input.IdempotencyKey)
	operation.Status = "started"
	operation.CreatedAt = now
	fillOperationResource(&operation, StorageAttachment{ID: attachmentID, OperationID: input.IdempotencyKey, WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID, Provider: s.provider.Descriptor().Name, ProviderRequestID: providerRequestID("storage-attach", input.IdempotencyKey)})
	input.OperationID = input.IdempotencyKey
	stored, claimed, err := s.claimRuntimeOperation(ctx, operation)
	if err != nil {
		return StorageAttachment{}, err
	}
	if !claimed {
		if stored.RequestHash != requestHash {
			return StorageAttachment{}, ErrStorageAttachmentIdempotencyConflict
		}
		if runtimeOperationNeedsReadback(stored, now) {
			reader, ok := s.provider.(storageAttachmentReadbackProvider)
			if !ok {
				return StorageAttachment{}, ErrStorageAttachmentOperationFailed
			}
			candidate := StorageAttachment{ID: stored.ResourceID, OperationID: input.IdempotencyKey, WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID}
			_ = decodeOperationResource(stored, &candidate)
			candidate.OperationID, candidate.WorkspaceID = input.IdempotencyKey, input.WorkspaceID
			candidate.ComputeID, candidate.VolumeID = input.ComputeID, input.VolumeID
			readback, readErr := reader.ReadStorageAttachment(ctx, candidate, compute, volume)
			if readErr != nil || !attachmentReadbackMatches(readback, input, compute, volume) {
				return StorageAttachment{}, ErrStorageAttachmentOperationFailed
			}
			if _, convergeErr := s.convergeRuntimeOperationReadback(ctx, stored, readback, nil); convergeErr != nil {
				return StorageAttachment{}, convergeErr
			}
			s.mu.Lock()
			s.attachments[readback.ID] = readback
			s.mu.Unlock()
			return readback, nil
		}
		return replayStorageAttachmentOperation(stored, requestHash)
	}
	attachment, err := s.provider.CreateStorageAttachment(s.providerMutationContext(ctx, operation), input, compute, volume)
	attachment.OperationID = input.IdempotencyKey
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
		if err := s.hydrateMissingResourceState(ctx); err != nil {
			return StorageAttachment{}, err
		}
		s.mu.Lock()
		existing = s.attachments[attachmentID]
		s.mu.Unlock()
	}
	if existing.ID == "" {
		operation := newOperation("detach_storage_attachment", "storage_attachment", attachmentID, "", "", "", hashInput(map[string]string{"id": attachmentID}), time.Now().UTC())
		operation.ProviderRequestID = providerRequestID("detach-attachment", attachmentID)
		err := fmt.Errorf("storage_attachment_not_found")
		_ = s.recordOperation(ctx, operation, "rejected", StorageAttachment{ID: attachmentID}, err)
		return StorageAttachment{}, err
	}
	if existing.Status == "detached" {
		return existing, nil
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
