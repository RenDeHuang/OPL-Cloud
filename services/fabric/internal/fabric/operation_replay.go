package fabric

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
)

func replayResourceState(ctx context.Context, operations OperationStore) (map[string]ComputeAllocation, map[string]StorageVolume, map[string]StorageAttachment, map[string]WorkspaceRuntime) {
	computes := map[string]ComputeAllocation{}
	volumes := map[string]StorageVolume{}
	attachments := map[string]StorageAttachment{}
	runtimes := map[string]WorkspaceRuntime{}
	attachmentRecords := map[string]bool{}
	canonicalAttachments := map[string]StorageAttachment{}
	canonicalAttachmentWorkspaces := map[string]string{}
	canonicalAttachmentConflicts := map[string]bool{}
	records, err := operations.List(ctx)
	if err != nil {
		return computes, volumes, attachments, runtimes
	}
	for _, operation := range records {
		if operation.ResourceKind == "storage_attachment" && operation.ResourceID != "" {
			attachmentRecords[operation.ResourceID] = true
		}
		if attachment, workspaceID, candidate, valid := canonicalWorkspaceLaunchAttachment(operation); candidate {
			if !valid || canonicalAttachmentConflicts[workspaceID] {
				canonicalAttachmentConflicts[workspaceID] = true
				continue
			}
			if existingID, exists := canonicalAttachmentWorkspaces[workspaceID]; exists && existingID != attachment.ID {
				canonicalAttachmentConflicts[workspaceID] = true
				continue
			}
			if existing, exists := canonicalAttachments[attachment.ID]; exists && existing.WorkspaceID != attachment.WorkspaceID {
				canonicalAttachmentConflicts[workspaceID] = true
				canonicalAttachmentConflicts[existing.WorkspaceID] = true
				continue
			}
			if _, exists := canonicalAttachments[attachment.ID]; exists {
				canonicalAttachmentConflicts[workspaceID] = true
				continue
			}
			canonicalAttachments[attachment.ID] = attachment
			canonicalAttachmentWorkspaces[workspaceID] = attachment.ID
		}
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
				previous, exists := computes[resource.ID]
				if !exists || !validTencentFailedComputeDestroyReplay(operation, resource) || !sameComputeDestroyStableIdentity(previous, resource) {
					continue
				}
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
	for attachmentID, attachment := range canonicalAttachments {
		compute, computeOK := computes[attachment.ComputeID]
		volume, volumeOK := volumes[attachment.VolumeID]
		if canonicalAttachmentConflicts[attachment.WorkspaceID] || attachmentRecords[attachmentID] || !computeOK || !volumeOK ||
			compute.AccountID == "" || compute.WorkspaceID != attachment.WorkspaceID || volume.AccountID != compute.AccountID || volume.WorkspaceID != attachment.WorkspaceID {
			continue
		}
		attachments[attachmentID] = attachment
	}
	return computes, volumes, attachments, runtimes
}

func (s *Service) hydrateMissingResourceState(ctx context.Context) error {
	computes, volumes, attachments, err := projectWorkspaceLaunchDeleteResources(ctx, s.operations)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.computes == nil {
		s.computes = map[string]ComputeAllocation{}
	}
	if s.volumes == nil {
		s.volumes = map[string]StorageVolume{}
	}
	if s.attachments == nil {
		s.attachments = map[string]StorageAttachment{}
	}
	for id, compute := range computes {
		if s.computes[id].ID == "" {
			s.computes[id] = cloneComputeAllocation(compute)
		}
	}
	for id, volume := range volumes {
		if s.volumes[id].ID == "" {
			s.volumes[id] = cloneStorageVolume(volume)
		}
	}
	for id, attachment := range attachments {
		if s.attachments[id].ID == "" {
			s.attachments[id] = attachment
		}
	}
	return nil
}

type workspaceLaunchDeleteStageCandidate struct {
	binding    WorkspaceLaunchStageBinding
	record     workspaceLaunchStageRecord
	resourceID string
	compute    ComputeAllocation
	volume     StorageVolume
	attachment StorageAttachment
}

type workspaceLaunchDeleteStageSet struct {
	byWorkspace       map[string]workspaceLaunchDeleteStageCandidate
	workspaceByID     map[string]string
	workspaceConflict map[string]bool
	resourceConflict  map[string]bool
}

func newWorkspaceLaunchDeleteStageSet() *workspaceLaunchDeleteStageSet {
	return &workspaceLaunchDeleteStageSet{
		byWorkspace:       map[string]workspaceLaunchDeleteStageCandidate{},
		workspaceByID:     map[string]string{},
		workspaceConflict: map[string]bool{},
		resourceConflict:  map[string]bool{},
	}
}

func (s *workspaceLaunchDeleteStageSet) add(workspaceID, resourceID string, candidate workspaceLaunchDeleteStageCandidate, valid bool) {
	if !valid || workspaceID == "" || resourceID == "" {
		if workspaceID != "" {
			s.workspaceConflict[workspaceID] = true
		}
		if resourceID != "" {
			s.resourceConflict[resourceID] = true
			if owner := s.workspaceByID[resourceID]; owner != "" {
				s.workspaceConflict[owner] = true
			}
		}
		return
	}
	if existing, exists := s.byWorkspace[workspaceID]; exists {
		s.workspaceConflict[workspaceID] = true
		s.resourceConflict[existing.resourceID] = true
		s.resourceConflict[resourceID] = true
		return
	}
	if owner, exists := s.workspaceByID[resourceID]; exists {
		s.workspaceConflict[workspaceID] = true
		s.workspaceConflict[owner] = true
		s.resourceConflict[resourceID] = true
		return
	}
	s.byWorkspace[workspaceID] = candidate
	s.workspaceByID[resourceID] = workspaceID
}

func (s *workspaceLaunchDeleteStageSet) canonical(workspaceID string) (workspaceLaunchDeleteStageCandidate, bool) {
	candidate, ok := s.byWorkspace[workspaceID]
	return candidate, ok && !s.workspaceConflict[workspaceID] && !s.resourceConflict[candidate.resourceID]
}

func projectWorkspaceLaunchDeleteResources(ctx context.Context, operations OperationStore) (map[string]ComputeAllocation, map[string]StorageVolume, map[string]StorageAttachment, error) {
	records, err := operations.List(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	computeStages := newWorkspaceLaunchDeleteStageSet()
	storageStages := newWorkspaceLaunchDeleteStageSet()
	attachmentStages := newWorkspaceLaunchDeleteStageSet()
	for _, operation := range records {
		for stage, stages := range map[string]*workspaceLaunchDeleteStageSet{
			"ensure_compute_allocation": computeStages,
			"storage":                   storageStages,
			"attachment":                attachmentStages,
		} {
			candidate, workspaceID, resourceID, isCandidate, valid := canonicalWorkspaceLaunchDeleteStage(operation, stage)
			if isCandidate {
				stages.add(workspaceID, resourceID, candidate, valid)
			}
		}
	}

	computes := map[string]ComputeAllocation{}
	volumes := map[string]StorageVolume{}
	attachments := map[string]StorageAttachment{}
	for workspaceID := range computeStages.byWorkspace {
		if candidate, ok := computeStages.canonical(workspaceID); ok {
			computes[candidate.compute.ID] = cloneComputeAllocation(candidate.compute)
		}
	}
	for workspaceID := range storageStages.byWorkspace {
		storage, storageOK := storageStages.canonical(workspaceID)
		compute, computeOK := computeStages.canonical(workspaceID)
		if !storageOK || !computeOK || !sameWorkspaceLaunchDeleteSequence(compute, storage) ||
			storage.record.RequestResources.ComputeAllocationID != compute.compute.ID ||
			storage.record.RequestResources.ComputeBindingRef != compute.binding.FabricOperationID {
			continue
		}
		volumes[storage.volume.ID] = cloneStorageVolume(storage.volume)
	}
	for workspaceID := range attachmentStages.byWorkspace {
		attachment, attachmentOK := attachmentStages.canonical(workspaceID)
		compute, computeOK := computeStages.canonical(workspaceID)
		storage, storageOK := storageStages.canonical(workspaceID)
		if !attachmentOK || !computeOK || !storageOK || !sameWorkspaceLaunchDeleteSequence(compute, attachment) ||
			!sameWorkspaceLaunchDeleteSequence(storage, attachment) ||
			attachment.record.RequestResources.ComputeAllocationID != compute.compute.ID ||
			attachment.record.RequestResources.ComputeBindingRef != compute.binding.FabricOperationID ||
			attachment.record.RequestResources.StorageID != storage.volume.ID ||
			attachment.record.RequestResources.StorageBindingRef != storage.binding.FabricOperationID {
			continue
		}
		attachments[attachment.attachment.ID] = attachment.attachment
	}
	return computes, volumes, attachments, nil
}

func canonicalWorkspaceLaunchDeleteStage(operation FabricOperation, stage string) (workspaceLaunchDeleteStageCandidate, string, string, bool, bool) {
	action, supported := workspaceLaunchStageAction(stage)
	if !supported || (stage != "ensure_compute_allocation" && stage != "storage" && stage != "attachment") {
		return workspaceLaunchDeleteStageCandidate{}, "", "", false, false
	}
	binding, bindingOK := decodeLaunchStageBinding(operation)
	isCandidate := operation.Action == action || bindingOK && binding.Stage == stage
	workspaceID := operation.WorkspaceID
	if bindingOK {
		workspaceID = binding.WorkspaceID
	}
	if !isCandidate {
		return workspaceLaunchDeleteStageCandidate{}, workspaceID, "", false, false
	}
	record, recordOK := decodeWorkspaceLaunchStageRecord(operation)
	resourceID := workspaceLaunchDeleteRecordResourceID(stage, record)
	if resourceID == "" && bindingOK {
		resourceID = workspaceLaunchDeleteBindingResourceID(stage, binding)
	}
	candidate := workspaceLaunchDeleteStageCandidate{binding: binding, record: record, resourceID: resourceID}
	if !bindingOK || !recordOK || operation.Status != "succeeded" || operation.FinishedAt.IsZero() ||
		operation.Action != action || operation.ResourceKind != "workspace_launch_stage" || binding.Stage != stage || binding.Action != action ||
		operation.ID != binding.FabricOperationID || operation.OperationID != binding.FabricOperationID || operation.ResourceID != binding.FabricOperationID ||
		operation.Provider == "" || operation.Provider != record.ProviderProfileRef || record.GatewayKeyID != 0 ||
		binding.ExpectedResourceBinding != workspaceLaunchCurrentStageBinding(stage, record.RequestResources) ||
		!workspaceLaunchResourcesContain(record.Resources, record.RequestResources) {
		return candidate, workspaceID, resourceID, true, false
	}
	var state struct {
		Compute    *ComputeAllocation `json:"compute,omitempty"`
		Storage    *StorageVolume     `json:"storage,omitempty"`
		Attachment *StorageAttachment `json:"attachment,omitempty"`
	}
	if len(record.ProviderState) == 0 || json.Unmarshal(record.ProviderState, &state) != nil {
		return candidate, workspaceID, resourceID, true, false
	}
	switch stage {
	case "ensure_compute_allocation":
		candidate.compute = firstNonNilWorkspaceLaunchDeleteCompute(state.Compute)
		return candidate, workspaceID, resourceID, true, validWorkspaceLaunchDeleteCompute(candidate, state)
	case "storage":
		candidate.volume = firstNonNilWorkspaceLaunchDeleteStorage(state.Storage)
		return candidate, workspaceID, resourceID, true, validWorkspaceLaunchDeleteStorage(candidate, state)
	case "attachment":
		candidate.attachment = firstNonNilWorkspaceLaunchDeleteAttachment(state.Attachment)
		return candidate, workspaceID, resourceID, true, validWorkspaceLaunchDeleteAttachment(candidate, state)
	default:
		return candidate, workspaceID, resourceID, true, false
	}
}

func workspaceLaunchDeleteRecordResourceID(stage string, record workspaceLaunchStageRecord) string {
	return map[string]string{
		"ensure_compute_allocation": record.Resources.ComputeAllocationID,
		"storage":                   record.Resources.StorageID,
		"attachment":                record.Resources.AttachmentID,
	}[stage]
}

func workspaceLaunchDeleteBindingResourceID(stage string, binding WorkspaceLaunchStageBinding) string {
	return map[string]string{
		"ensure_compute_allocation": workspaceLaunchComputeID(binding),
		"storage":                   workspaceLaunchStorageID(binding),
		"attachment":                workspaceLaunchAttachmentID(binding),
	}[stage]
}

func firstNonNilWorkspaceLaunchDeleteCompute(value *ComputeAllocation) ComputeAllocation {
	if value == nil {
		return ComputeAllocation{}
	}
	return *value
}

func firstNonNilWorkspaceLaunchDeleteStorage(value *StorageVolume) StorageVolume {
	if value == nil {
		return StorageVolume{}
	}
	return *value
}

func firstNonNilWorkspaceLaunchDeleteAttachment(value *StorageAttachment) StorageAttachment {
	if value == nil {
		return StorageAttachment{}
	}
	return *value
}

func validWorkspaceLaunchDeleteCompute(candidate workspaceLaunchDeleteStageCandidate, state struct {
	Compute    *ComputeAllocation `json:"compute,omitempty"`
	Storage    *StorageVolume     `json:"storage,omitempty"`
	Attachment *StorageAttachment `json:"attachment,omitempty"`
}) bool {
	binding, record, compute := candidate.binding, candidate.record, candidate.compute
	expected := WorkspaceLaunchResources{ComputeAllocationID: workspaceLaunchComputeID(binding), ComputeBindingRef: binding.FabricOperationID}
	return record.RequestResources == (WorkspaceLaunchResources{}) && record.Resources == expected && state.Compute != nil && state.Storage == nil && state.Attachment == nil &&
		compute.ID == expected.ComputeAllocationID && compute.OperationID == binding.FabricOperationID && compute.AccountID == binding.AccountID && compute.WorkspaceID == binding.WorkspaceID &&
		compute.PackageID != "" && compute.Provider == record.ProviderProfileRef && compute.ProviderResourceID != "" && compute.ProviderRequestID != "" &&
		compute.NodePoolID != "" && compute.InstanceType != "" && compute.Zone != "" && compute.ChargeType != "" && isReadyResourceStatus(compute.Status)
}

func validWorkspaceLaunchDeleteStorage(candidate workspaceLaunchDeleteStageCandidate, state struct {
	Compute    *ComputeAllocation `json:"compute,omitempty"`
	Storage    *StorageVolume     `json:"storage,omitempty"`
	Attachment *StorageAttachment `json:"attachment,omitempty"`
}) bool {
	binding, record, volume := candidate.binding, candidate.record, candidate.volume
	request := WorkspaceLaunchResources{ComputeAllocationID: record.RequestResources.ComputeAllocationID, ComputeBindingRef: record.RequestResources.ComputeBindingRef}
	expected := request
	expected.StorageID, expected.StorageBindingRef = workspaceLaunchStorageID(binding), binding.FabricOperationID
	return request.ComputeAllocationID != "" && request.ComputeBindingRef != "" && record.RequestResources == request && record.Resources == expected &&
		state.Compute == nil && state.Storage != nil && state.Attachment == nil && volume.ID == expected.StorageID && volume.OperationID == binding.IdempotencyKey &&
		volume.AccountID == binding.AccountID && volume.WorkspaceID == binding.WorkspaceID && volume.Provider == record.ProviderProfileRef &&
		volume.ProviderResourceID != "" && volume.ProviderRequestID != "" && volume.SizeGB > 0 && volume.StorageClass != "" && volume.DiskType != "" && volume.Zone != "" && isReadyResourceStatus(volume.Status)
}

func validWorkspaceLaunchDeleteAttachment(candidate workspaceLaunchDeleteStageCandidate, state struct {
	Compute    *ComputeAllocation `json:"compute,omitempty"`
	Storage    *StorageVolume     `json:"storage,omitempty"`
	Attachment *StorageAttachment `json:"attachment,omitempty"`
}) bool {
	binding, record, attachment := candidate.binding, candidate.record, candidate.attachment
	request := WorkspaceLaunchResources{
		ComputeAllocationID: record.RequestResources.ComputeAllocationID, ComputeBindingRef: record.RequestResources.ComputeBindingRef,
		StorageID: record.RequestResources.StorageID, StorageBindingRef: record.RequestResources.StorageBindingRef,
	}
	expected := request
	expected.AttachmentID, expected.AttachmentBindingRef = workspaceLaunchAttachmentID(binding), binding.FabricOperationID
	return request.ComputeAllocationID != "" && request.ComputeBindingRef != "" && request.StorageID != "" && request.StorageBindingRef != "" &&
		record.RequestResources == request && record.Resources == expected && state.Compute == nil && state.Storage == nil && state.Attachment != nil &&
		attachment.ID == expected.AttachmentID && attachment.OperationID == binding.IdempotencyKey && attachment.WorkspaceID == binding.WorkspaceID &&
		attachment.ComputeID == request.ComputeAllocationID && attachment.VolumeID == request.StorageID && attachment.Provider == record.ProviderProfileRef &&
		attachment.ProviderAttachmentID != "" && attachment.ProviderRequestID != "" && attachment.Status == "attached"
}

func sameWorkspaceLaunchDeleteSequence(left, right workspaceLaunchDeleteStageCandidate) bool {
	return left.binding.LaunchOperationID == right.binding.LaunchOperationID && left.binding.AccountID == right.binding.AccountID &&
		left.binding.WorkspaceID == right.binding.WorkspaceID && left.record.ProviderProfileRef == right.record.ProviderProfileRef &&
		left.record.ProviderBindingRef == right.record.ProviderBindingRef && left.record.SpecDigest == right.record.SpecDigest
}

func validTencentFailedComputeDestroyReplay(operation FabricOperation, resource ComputeAllocation) bool {
	return operation.Action == "destroy_compute_allocation" && operation.ResourceKind == "compute_allocation" && operation.Status == "failed" &&
		operation.ResourceID != "" && operation.AccountID != "" && operation.WorkspaceID != "" &&
		operation.ResourceID == resource.ID && operation.AccountID == resource.AccountID && operation.WorkspaceID == resource.WorkspaceID &&
		operation.Provider == resource.Provider && operation.ProviderRequestID == resource.ProviderRequestID &&
		validTencentComputeDestroyStableIdentity(resource) &&
		(validTencentComputeAbsenceEvidence(resource) || validTencentComputeDestroyAttemptEvidence(resource) || validTencentComputeDestroyDispatchEvidence(resource))
}

func sameComputeDestroyStableIdentity(previous, failed ComputeAllocation) bool {
	return previous.ID == failed.ID && previous.OperationID == failed.OperationID && previous.AccountID == failed.AccountID && previous.WorkspaceID == failed.WorkspaceID &&
		previous.PackageID == failed.PackageID && previous.Provider == failed.Provider && previous.ProviderResourceID == failed.ProviderResourceID &&
		previous.PoolID == failed.PoolID && previous.NodePoolID == failed.NodePoolID && previous.InstanceID == failed.InstanceID && previous.CVMInstanceID == failed.CVMInstanceID &&
		previous.NodeName == failed.NodeName && previous.MachineName == failed.MachineName && previous.PrivateIP == failed.PrivateIP && previous.PublicIP == failed.PublicIP &&
		previous.InstanceType == failed.InstanceType && previous.Zone == failed.Zone && previous.ChargeType == failed.ChargeType && previous.RenewFlag == failed.RenewFlag &&
		previous.Deadline == failed.Deadline && previous.ServiceName == failed.ServiceName && previous.CreatedAt == failed.CreatedAt &&
		reflect.DeepEqual(previous.CostTags, failed.CostTags) && reflect.DeepEqual(previous.NodeSelector, failed.NodeSelector) &&
		reflect.DeepEqual(previous.ClaimTerminalEvidence, failed.ClaimTerminalEvidence) && sameTencentComputeDeleteProviderData(previous.ProviderData, failed.ProviderData)
}

func sameTencentComputeDeleteProviderData(previous, failed map[string]string) bool {
	for key, previousValue := range previous {
		failedValue, exists := failed[key]
		if !exists || previousValue != failedValue && !isTencentComputeDeleteEvidenceKey(key) {
			return false
		}
	}
	for key := range failed {
		if _, exists := previous[key]; !exists && !isTencentComputeDeleteEvidenceKey(key) {
			return false
		}
	}
	return true
}

func canonicalWorkspaceLaunchAttachment(operation FabricOperation) (StorageAttachment, string, bool, bool) {
	binding, bindingOK := decodeLaunchStageBinding(operation)
	candidate := operation.Action == "ensure_attachment" || bindingOK && binding.Stage == "attachment"
	workspaceID := operation.WorkspaceID
	if bindingOK {
		workspaceID = binding.WorkspaceID
	}
	if !candidate {
		return StorageAttachment{}, workspaceID, false, false
	}
	record, recordOK := decodeWorkspaceLaunchStageRecord(operation)
	if !bindingOK || !recordOK || operation.Status != "succeeded" || operation.Action != "ensure_attachment" ||
		operation.ResourceKind != "workspace_launch_stage" || operation.ID != binding.FabricOperationID || operation.OperationID != binding.FabricOperationID ||
		operation.ResourceID != binding.FabricOperationID || binding.Stage != "attachment" || binding.Action != operation.Action ||
		operation.Provider == "" || operation.Provider != record.ProviderProfileRef || record.GatewayKeyID != 0 ||
		record.RequestResources.AttachmentID != "" || record.RequestResources.AttachmentBindingRef != "" ||
		!workspaceLaunchResourcesContain(record.Resources, record.RequestResources) || record.Resources.AttachmentBindingRef != binding.FabricOperationID ||
		record.Resources.AttachmentID != workspaceLaunchAttachmentID(binding) {
		return StorageAttachment{}, workspaceID, true, false
	}
	var state struct {
		Attachment *StorageAttachment `json:"attachment,omitempty"`
	}
	if len(record.ProviderState) == 0 || json.Unmarshal(record.ProviderState, &state) != nil || state.Attachment == nil {
		return StorageAttachment{}, workspaceID, true, false
	}
	attachment := *state.Attachment
	if attachment.ID != record.Resources.AttachmentID || attachment.OperationID != binding.IdempotencyKey || attachment.WorkspaceID != binding.WorkspaceID ||
		attachment.ComputeID == "" || attachment.ComputeID != record.Resources.ComputeAllocationID || attachment.ComputeID != record.RequestResources.ComputeAllocationID ||
		attachment.VolumeID == "" || attachment.VolumeID != record.Resources.StorageID || attachment.VolumeID != record.RequestResources.StorageID ||
		attachment.Status != "attached" || attachment.Provider != operation.Provider || attachment.ProviderAttachmentID == "" || attachment.ProviderRequestID == "" {
		return StorageAttachment{}, workspaceID, true, false
	}
	return attachment, workspaceID, true, true
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
	launchBinding := operation.RedactedProviderPayload[launchStageBindingPayloadKey]
	providerBinding := operation.RedactedProviderPayload[providerMutationBindingPayloadKey]
	providerState := operation.RedactedProviderPayload[providerMutationStatePayloadKey]
	providerReplayEpoch := operation.RedactedProviderPayload[providerMutationReplayEpochPayloadKey]
	providerChildResourceID := operation.ResourceID
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
	case GatewaySecret:
		operation.ResourceID = firstNonEmpty(value.SecretRef, operation.ResourceID)
		operation.RedactedProviderPayload = map[string]any{"resource": value}
	case Job:
		redacted := value
		redacted.LeaseToken = ""
		redacted.leaseTokenHash = ""
		operation.ResourceID = value.JobID
		operation.WorkspaceID = value.WorkspaceID
		operation.ProviderRequestID = firstNonEmpty(operation.ProviderRequestID, value.JobID)
		operation.RedactedProviderPayload = map[string]any{"resource": redacted, "leaseTokenHash": value.leaseTokenHash}
	}
	if launchBinding != nil {
		operation.RedactedProviderPayload[launchStageBindingPayloadKey] = launchBinding
	}
	if providerBinding != nil {
		operation.ResourceID = providerChildResourceID
		operation.RedactedProviderPayload[providerMutationBindingPayloadKey] = providerBinding
	}
	if providerState != nil {
		operation.RedactedProviderPayload[providerMutationStatePayloadKey] = providerState
	}
	if providerReplayEpoch != nil {
		operation.RedactedProviderPayload[providerMutationReplayEpochPayloadKey] = providerReplayEpoch
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
