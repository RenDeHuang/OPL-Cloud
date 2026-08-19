package fabric

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"opl-cloud/services/fabric/internal/protectedresource"
)

const tencentCBSCreateMutationStateSchemaVersion = 2

type tencentCBSCreateMutationState struct {
	SchemaVersion int    `json:"schemaVersion"`
	Region        string `json:"region"`
	DiskType      string `json:"diskType"`
	Zone          string `json:"zone"`
	SizeGB        int    `json:"sizeGb"`
	OperationID   string `json:"operationId"`
	AccountID     string `json:"accountId"`
	WorkspaceID   string `json:"workspaceId"`
	StorageID     string `json:"storageId"`
}

func (p *TencentProvider) CreateStorageVolume(ctx context.Context, input StorageVolumeInput) (StorageVolume, error) {
	if providerMutationJournalFromContext(ctx) == nil {
		volume, err := p.CreateCBSVolume(ctx, input)
		if err != nil {
			return volume, err
		}
		if _, err := p.callKubectl(ctx, []string{"apply", "-f", "-"}, staticCBSManifest(volume), protectedresource.Target{}); err != nil {
			return volume, err
		}
		return volume, nil
	}
	state, err := p.tencentCBSCreateMutationState(ctx, input)
	if err != nil {
		return StorageVolume{}, err
	}
	volume := tencentCBSCreateVolume(input, state, time.Time{})
	cbsAttempt, err := beginProviderMutationWithState(ctx, "tencent_cbs_create", "storage_volume", input.ID, input.ExpectedProviderResourceID, state)
	if err != nil {
		return volume, err
	}
	if cbsAttempt != nil && !cbsAttempt.Fresh {
		var persistedState tencentCBSCreateMutationState
		if !cbsAttempt.state(&persistedState) || !persistedState.matches(input) || persistedState != state {
			return volume, ErrLaunchStageBindingConflict
		}
		var persistedVolume StorageVolume
		if cbsAttempt.resource(&persistedVolume) {
			if !persistedState.matchesVolume(input, persistedVolume) {
				return volume, ErrLaunchStageBindingConflict
			}
			volume = persistedVolume
		} else if cbsAttempt.operation.Status != "started" {
			return volume, ErrLaunchStageBindingConflict
		}
		volume, err = p.readCBSVolume(ctx, input, volume, persistedState)
		if errors.Is(err, ErrWorkspaceLaunchResourceAbsent) {
			claimed, claimErr := cbsAttempt.claimReplay(ctx)
			if claimErr != nil || !claimed {
				return volume, firstNonNil(claimErr, ErrWorkspaceLaunchPending)
			}
			volume, err = p.readCBSVolume(ctx, input, volume, persistedState)
			if errors.Is(err, ErrWorkspaceLaunchResourceAbsent) {
				if err = cbsAttempt.markReplayDispatch(ctx); err != nil {
					return volume, err
				}
				volume, err = p.createCBSVolume(ctx, input, persistedState)
				if err == nil {
					volume, err = p.readCBSVolume(ctx, input, volume, persistedState)
				}
			}
		}
	} else {
		volume, err = p.createCBSVolume(ctx, input, state)
		if err == nil {
			volume, err = p.readCBSVolume(ctx, input, volume, state)
		}
	}
	if err != nil {
		_ = cbsAttempt.complete(ctx, volume.ProviderRequestID, volume, err)
		return volume, err
	}
	if err := cbsAttempt.complete(ctx, volume.ProviderRequestID, volume, nil); err != nil {
		return volume, err
	}

	staticAttempt, err := beginProviderMutation(ctx, "tencent_static_storage_binding_apply", "storage_binding", input.ID, volume.ProviderResourceID)
	if err != nil {
		return volume, err
	}
	if staticAttempt != nil && !staticAttempt.Fresh {
		_ = staticAttempt.resource(&volume)
		volume, err = p.ReadStaticStorageBinding(ctx, volume)
		if errors.Is(err, ErrWorkspaceLaunchResourceAbsent) {
			claimed, claimErr := staticAttempt.claimReplay(ctx)
			if claimErr != nil || !claimed {
				return volume, firstNonNil(claimErr, ErrWorkspaceLaunchPending)
			}
			volume, err = p.ReadStaticStorageBinding(ctx, volume)
			if errors.Is(err, ErrWorkspaceLaunchResourceAbsent) {
				if err = staticAttempt.markReplayDispatch(ctx); err != nil {
					return volume, err
				}
				volume, err = p.ApplyStaticStorageBinding(ctx, volume)
			}
		}
	} else {
		volume, err = p.ApplyStaticStorageBinding(ctx, volume)
	}
	if err != nil {
		_ = staticAttempt.complete(ctx, volume.ProviderRequestID, volume, err)
		return volume, err
	}
	if err := staticAttempt.complete(ctx, volume.ProviderRequestID, volume, nil); err != nil {
		return volume, err
	}
	return volume, nil
}

// CreateCBSVolume is the first normal-launch storage stage. It deliberately
// stops after the provider has returned a disk identity; Kubernetes binding is
// handled by ApplyStaticStorageBinding so a lost response can be recovered by
// a Describe-only readback without reapplying either side.
func (p *TencentProvider) CreateCBSVolume(ctx context.Context, input StorageVolumeInput) (StorageVolume, error) {
	state, err := p.tencentCBSCreateProposal(ctx, input)
	if err != nil {
		return StorageVolume{}, err
	}
	return p.createCBSVolume(ctx, input, state)
}

func (p *TencentProvider) createCBSVolume(ctx context.Context, input StorageVolumeInput, state tencentCBSCreateMutationState) (StorageVolume, error) {
	if !validTencentCBSCreateStateForContext(ctx, input, state) {
		return StorageVolume{}, ErrLaunchStageBindingConflict
	}
	now := time.Now().UTC()
	volume := tencentCBSCreateVolume(input, state, now)
	response, err := p.provision(ctx, provisionerRequest{
		Action: "create_storage_volume", AccountID: state.AccountID, Region: state.Region, Tags: volume.CostTags,
		Storage: provisionerStorage{
			ID: state.StorageID, SizeGB: uint64(state.SizeGB), Zone: state.Zone, DiskType: state.DiskType,
			ExpectedState: input.ExpectedRecoveryState, ExpectedProviderResourceID: input.ExpectedProviderResourceID,
			AllowExistingExactReplay: input.AllowExistingExactReplay,
		},
	})
	if err != nil {
		return volume, err
	}
	if response.OK && !exactTencentCBSCreateReadback(response, state) {
		return volume, fmt.Errorf("storage_cbs_readback_identity_mismatch")
	}
	volume.ProviderRequestID = response.ProviderRequestID
	if strings.HasPrefix(response.StorageVolumeID, "disk-") {
		volume.ProviderResourceID = response.StorageVolumeID
	}
	applyStorageReadback(&volume, response)
	if !response.OK {
		return volume, provisionerError(response)
	}
	if !state.matchesVolume(input, volume) {
		return volume, fmt.Errorf("storage_cbs_readback_identity_mismatch")
	}
	if volume.ProviderResourceID == "" {
		return volume, fmt.Errorf("storage_cbs_identity_required")
	}
	return volume, nil
}

func (p *TencentProvider) tencentCBSCreateMutationState(ctx context.Context, input StorageVolumeInput) (tencentCBSCreateMutationState, error) {
	journal := providerMutationJournalFromContext(ctx)
	if journal == nil {
		return tencentCBSCreateMutationState{}, ErrLaunchStageBindingConflict
	}
	childID := providerMutationOperationID(journal.parent, "tencent_cbs_create", "storage_volume", input.ID, input.ExpectedProviderResourceID)
	operation, err := journal.operations.Get(ctx, childID)
	if err == nil {
		var state tencentCBSCreateMutationState
		if !decodeProviderMutationState(operation, &state) || !state.matches(input) ||
			journal.parent.FabricOperationID != state.OperationID || journal.parent.AccountID != state.AccountID || journal.parent.WorkspaceID != state.WorkspaceID {
			return tencentCBSCreateMutationState{}, ErrLaunchStageBindingConflict
		}
		if !validTencentCBSCreateStateForContext(ctx, input, state) {
			return tencentCBSCreateMutationState{}, ErrLaunchStageBindingConflict
		}
		return state, nil
	}
	if !errors.Is(err, ErrOperationNotFound) {
		return tencentCBSCreateMutationState{}, err
	}
	state, err := p.tencentCBSCreateProposal(ctx, input)
	if err != nil {
		return tencentCBSCreateMutationState{}, err
	}
	if !state.matches(input) || journal.parent.FabricOperationID != state.OperationID ||
		journal.parent.AccountID != state.AccountID || journal.parent.WorkspaceID != state.WorkspaceID {
		return tencentCBSCreateMutationState{}, ErrLaunchStageBindingConflict
	}
	return state, nil
}

func (state tencentCBSCreateMutationState) matches(input StorageVolumeInput) bool {
	return state.SchemaVersion == tencentCBSCreateMutationStateSchemaVersion && validTencentProviderRegion(state.Region) &&
		state.DiskType != "" && state.DiskType == strings.TrimSpace(state.DiskType) &&
		state.Zone != "" && state.Zone == strings.TrimSpace(state.Zone) && state.Zone == input.Zone &&
		state.SizeGB > 0 && state.SizeGB == input.SizeGB &&
		state.OperationID != "" && state.OperationID == strings.TrimSpace(state.OperationID) && state.OperationID == input.OperationID &&
		state.AccountID != "" && state.AccountID == strings.TrimSpace(state.AccountID) && state.AccountID == input.AccountID &&
		state.WorkspaceID != "" && state.WorkspaceID == strings.TrimSpace(state.WorkspaceID) && state.WorkspaceID == input.WorkspaceID &&
		state.StorageID != "" && state.StorageID == strings.TrimSpace(state.StorageID) && state.StorageID == input.ID
}

func (state tencentCBSCreateMutationState) matchesWorkspacePlan(plan tencentWorkspacePlan) bool {
	return validTencentProviderRegion(plan.Region) && plan.Storage.DiskType != "" && plan.Storage.DiskType == strings.TrimSpace(plan.Storage.DiskType) &&
		plan.Zone != "" && plan.Zone == strings.TrimSpace(plan.Zone) && plan.Storage.SizeGB > 0 &&
		state.Region == plan.Region && state.DiskType == plan.Storage.DiskType && state.Zone == plan.Zone && state.SizeGB == plan.Storage.SizeGB
}

func (state tencentCBSCreateMutationState) matchesVolume(input StorageVolumeInput, volume StorageVolume) bool {
	return state.matches(input) && volume.ID == state.StorageID && volume.AccountID == state.AccountID && volume.WorkspaceID == state.WorkspaceID &&
		volume.SizeGB == state.SizeGB && volume.DiskType == state.DiskType && volume.Zone == state.Zone &&
		volume.ProviderData != nil && volume.ProviderData["region"] == state.Region
}

func (p *TencentProvider) tencentCBSCreateProposal(ctx context.Context, input StorageVolumeInput) (tencentCBSCreateMutationState, error) {
	state := tencentCBSCreateMutationState{
		SchemaVersion: tencentCBSCreateMutationStateSchemaVersion,
		OperationID:   input.OperationID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, StorageID: input.ID,
	}
	if _, present := tencentWorkspacePlanContextValue(ctx); present {
		plan, ok := tencentWorkspacePlanFromContext(ctx)
		if !ok || p == nil || plan.Region != p.region {
			return tencentCBSCreateMutationState{}, ErrLaunchStageBindingConflict
		}
		state.Region, state.DiskType, state.Zone, state.SizeGB = plan.Region, plan.Storage.DiskType, plan.Zone, plan.Storage.SizeGB
		if !state.matches(input) || !state.matchesWorkspacePlan(plan) {
			return tencentCBSCreateMutationState{}, ErrLaunchStageBindingConflict
		}
		return state, nil
	}
	plan, err := p.tencentStoragePlanForContext(ctx, input)
	if err != nil {
		return tencentCBSCreateMutationState{}, err
	}
	if p == nil || !validTencentProviderRegion(p.region) {
		return tencentCBSCreateMutationState{}, ErrProviderPlanUnavailable
	}
	state.Region, state.DiskType, state.Zone, state.SizeGB = p.region, plan.DiskType, input.Zone, input.SizeGB
	if !state.matches(input) {
		return tencentCBSCreateMutationState{}, ErrLaunchStageBindingConflict
	}
	return state, nil
}

func validTencentCBSCreateStateForContext(ctx context.Context, input StorageVolumeInput, state tencentCBSCreateMutationState) bool {
	if !state.matches(input) {
		return false
	}
	if _, present := tencentWorkspacePlanContextValue(ctx); !present {
		return true
	}
	plan, ok := tencentWorkspacePlanFromContext(ctx)
	return ok && state.matchesWorkspacePlan(plan)
}

func tencentCBSCreateVolume(input StorageVolumeInput, state tencentCBSCreateMutationState, createdAt time.Time) StorageVolume {
	return StorageVolume{
		ID: state.StorageID, OperationID: input.IdempotencyKey, AccountID: state.AccountID, WorkspaceID: state.WorkspaceID,
		Status: "pending", Provider: "tencent-tke", SizeGB: state.SizeGB, DiskType: state.DiskType, Zone: state.Zone,
		CostTags: oplCostTags(state.AccountID, state.WorkspaceID, state.StorageID, state.OperationID), CreatedAt: createdAt,
		ProviderData: map[string]string{"pvName": k8sName(state.StorageID) + "-pv", "pvcName": k8sName(state.StorageID) + "-data", "region": state.Region},
	}
}

func exactTencentCBSCreateReadback(response provisionerResponse, state tencentCBSCreateMutationState) bool {
	return exactStorageRegionReadback(response, state.Region) && response.ProviderData["diskType"] == state.DiskType &&
		response.ProviderData["zone"] == state.Zone && response.ProviderData["sizeGb"] == strconv.Itoa(state.SizeGB)
}

// ReadCBSVolume is intentionally Describe-only. The persisted disk identity
// is authoritative; a response naming another disk is an identity failure.
func (p *TencentProvider) ReadCBSVolume(ctx context.Context, input StorageVolumeInput, persisted StorageVolume) (StorageVolume, error) {
	state := tencentCBSCreateMutationState{
		SchemaVersion: tencentCBSCreateMutationStateSchemaVersion,
		Region:        persisted.ProviderData["region"],
		DiskType:      persisted.DiskType,
		Zone:          persisted.Zone,
		SizeGB:        persisted.SizeGB,
		OperationID:   input.OperationID,
		AccountID:     input.AccountID,
		WorkspaceID:   input.WorkspaceID,
		StorageID:     input.ID,
	}
	return p.readCBSVolume(ctx, input, persisted, state)
}

func (p *TencentProvider) readCBSVolume(ctx context.Context, input StorageVolumeInput, persisted StorageVolume, state tencentCBSCreateMutationState) (StorageVolume, error) {
	if !validTencentCBSCreateStateForContext(ctx, input, state) || !state.matchesVolume(input, persisted) {
		return persisted, fmt.Errorf("storage_cbs_readback_identity_required")
	}
	if !strings.HasPrefix(persisted.ProviderResourceID, "disk-") {
		discovery, discoverErr := p.provision(ctx, provisionerRequest{
			Action: "discover_storage_volume", AccountID: state.AccountID, Region: state.Region,
			Tags:    oplCostTags(state.AccountID, state.WorkspaceID, state.StorageID, state.OperationID),
			Storage: provisionerStorage{ID: state.StorageID, SizeGB: uint64(state.SizeGB), Zone: state.Zone, DiskType: state.DiskType},
		})
		if discoverErr != nil {
			return persisted, discoverErr
		}
		if discovery.OK && discovery.MutationCount == 0 && discovery.StorageState == "storage_not_started" && discovery.Status == "absent" &&
			discovery.StorageVolumeID == "" {
			return persisted, ErrWorkspaceLaunchResourceAbsent
		}
		if !discovery.OK || discovery.MutationCount != 0 || discovery.StorageState != "storage_existing_exact" || !strings.HasPrefix(discovery.StorageVolumeID, "disk-") {
			return persisted, fmt.Errorf("storage_cbs_readback_identity_required")
		}
		if !exactTencentCBSCreateReadback(discovery, state) {
			return persisted, fmt.Errorf("storage_cbs_readback_identity_mismatch")
		}
		persisted.ProviderResourceID = discovery.StorageVolumeID
		persisted.ProviderRequestID = firstNonEmpty(discovery.ProviderRequestID, persisted.ProviderRequestID)
		applyStorageReadback(&persisted, discovery)
		if !state.matchesVolume(input, persisted) {
			return persisted, fmt.Errorf("storage_cbs_readback_identity_mismatch")
		}
	}
	expectedProviderResourceID := persisted.ProviderResourceID
	expectedRenewFlag, expectedDeadline := persisted.RenewFlag, persisted.Deadline
	readback, err := p.ReadStorageVolume(ctx, persisted)
	if err != nil {
		return readback, err
	}
	if readback.ProviderResourceID != expectedProviderResourceID || !state.matchesVolume(input, readback) ||
		(expectedRenewFlag != "" && readback.RenewFlag != expectedRenewFlag) ||
		(expectedDeadline != "" && readback.Deadline != expectedDeadline) ||
		(readback.ProviderData["zone"] != "" && readback.ProviderData["zone"] != state.Zone) {
		return readback, fmt.Errorf("storage_cbs_readback_identity_mismatch")
	}
	return readback, nil
}

// ApplyStaticStorageBinding is the sole Kubernetes write in the staged
// storage path. It always follows the apply with the same strict GET proof.
func (p *TencentProvider) ApplyStaticStorageBinding(ctx context.Context, volume StorageVolume) (StorageVolume, error) {
	if err := validateStaticStorageBindingInput(volume); err != nil {
		return volume, err
	}
	if _, err := p.callKubectl(ctx, []string{"apply", "-f", "-"}, staticCBSManifest(volume), protectedresource.Target{}); err != nil {
		return volume, err
	}
	return p.ReadStaticStorageBinding(ctx, volume)
}

// ReadStaticStorageBinding performs only Kubernetes GETs and verifies the
// original PV/PVC/CBS identity. Missing, duplicate, or drifted objects fail
// closed; a not-yet-Bound PVC is returned as pending for later readback.
func (p *TencentProvider) ReadStaticStorageBinding(ctx context.Context, volume StorageVolume) (StorageVolume, error) {
	if err := validateStaticStorageBindingInput(volume); err != nil {
		return volume, err
	}
	pvName, pvcName := storageBindingNames(volume)
	raw, err := p.callKubectl(ctx, []string{"get", "pv/" + pvName, "pvc/" + pvcName, "--ignore-not-found", "-o", "json"}, nil, protectedresource.Target{})
	if err != nil {
		return volume, err
	}
	items, err := strictKubectlItems(raw)
	if err != nil {
		return volume, err
	}
	var pv, pvc map[string]any
	pvMatches, pvcMatches := 0, 0
	for _, item := range items {
		resource, ok := item.(map[string]any)
		if !ok {
			return volume, fmt.Errorf("storage_static_binding_response_invalid")
		}
		switch {
		case stringValue(resource["kind"]) == "PersistentVolume" && stringValue(nested(resource, "metadata", "name")) == pvName:
			pv, pvMatches = resource, pvMatches+1
		case stringValue(resource["kind"]) == "PersistentVolumeClaim" && stringValue(nested(resource, "metadata", "name")) == pvcName:
			pvc, pvcMatches = resource, pvcMatches+1
		}
	}
	if len(items) == 0 && pvMatches == 0 && pvcMatches == 0 {
		return volume, ErrWorkspaceLaunchResourceAbsent
	}
	if len(items) != 2 || pvMatches != 1 || pvcMatches != 1 {
		return volume, fmt.Errorf("storage_static_binding_unverified")
	}
	expectedTags := volume.CostTags
	if len(expectedTags) == 0 {
		expectedTags = oplCostTags(volume.AccountID, volume.WorkspaceID, volume.ID, volume.OperationID)
	}
	for _, resource := range []map[string]any{pv, pvc} {
		for key, expected := range k8sCostLabels(expectedTags) {
			if expected != "" && stringValue(nested(resource, "metadata", "labels", key)) != expected {
				return volume, fmt.Errorf("storage_static_binding_identity_mismatch")
			}
		}
	}
	pvSpec, _ := pv["spec"].(map[string]any)
	pvcSpec, _ := pvc["spec"].(map[string]any)
	expectedCapacity := fmt.Sprintf("%dGi", volume.SizeGB)
	expectedNodeAffinity := map[string]any{"required": map[string]any{"nodeSelectorTerms": []any{map[string]any{"matchExpressions": []any{map[string]any{"key": "topology.kubernetes.io/zone", "operator": "In", "values": []any{volume.Zone}}}}}}}
	pvAccessModes, _ := pvSpec["accessModes"].([]any)
	pvcAccessModes, _ := pvcSpec["accessModes"].([]any)
	if stringValue(nested(pv, "spec", "csi", "driver")) != "com.tencent.cloud.csi.cbs" ||
		stringValue(nested(pv, "spec", "csi", "volumeHandle")) != volume.ProviderResourceID ||
		stringValue(pvSpec["persistentVolumeReclaimPolicy"]) != "Retain" || stringValue(pvSpec["storageClassName"]) != "" ||
		stringValue(pvcSpec["storageClassName"]) != "" || stringValue(pvcSpec["volumeName"]) != pvName ||
		len(pvAccessModes) != 1 || stringValue(pvAccessModes[0]) != "ReadWriteOnce" || len(pvcAccessModes) != 1 || stringValue(pvcAccessModes[0]) != "ReadWriteOnce" ||
		stringValue(nested(pv, "spec", "capacity", "storage")) != expectedCapacity || stringValue(nested(pvc, "spec", "resources", "requests", "storage")) != expectedCapacity ||
		!reflect.DeepEqual(pvSpec["nodeAffinity"], expectedNodeAffinity) {
		return volume, fmt.Errorf("storage_static_binding_identity_mismatch")
	}
	if volume.ProviderData == nil {
		volume.ProviderData = map[string]string{}
	}
	volume.ProviderData["pvName"], volume.ProviderData["pvcName"] = pvName, pvcName
	if stringValue(nested(pvc, "status", "phase")) == "Bound" {
		volume.Status = "ready"
	} else {
		volume.Status = "pending"
	}
	return volume, nil
}

func validateStaticStorageBindingInput(volume StorageVolume) error {
	if volume.ID == "" || volume.AccountID == "" || volume.WorkspaceID == "" || !strings.HasPrefix(volume.ProviderResourceID, "disk-") ||
		volume.SizeGB <= 0 || strings.TrimSpace(volume.Zone) == "" {
		return fmt.Errorf("storage_static_binding_identity_required")
	}
	pvName, pvcName := storageBindingNames(volume)
	if pvName == "" || pvcName == "" {
		return fmt.Errorf("storage_static_binding_names_required")
	}
	return nil
}

func (p *TencentProvider) SyncStorageVolume(ctx context.Context, volume StorageVolume) (StorageVolume, error) {
	return p.ReadStorageVolumeStatus(ctx, volume)
}

func (p *TencentProvider) ReadStorageVolumeStatus(ctx context.Context, volume StorageVolume) (StorageVolume, error) {
	volume, err := p.ReadStorageVolume(ctx, volume)
	if err != nil || volume.Status == "external_deleted" || volume.Status == "pending" {
		return volume, err
	}
	pvc := storagePVCName(volume)
	raw, err := p.callKubectl(ctx, []string{"get", "pvc/" + pvc, "-o", "json"}, nil, protectedresource.Target{})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "notfound") || strings.Contains(strings.ToLower(err.Error()), "not found") {
			volume.Status = "pending"
			return volume, nil
		}
		return volume, err
	}
	pvcResource := findK8s(kubectlItems(raw), "PersistentVolumeClaim", pvc)
	if pvcResource != nil && stringValue(nested(pvcResource, "status", "phase")) == "Bound" {
		volume.Status = "ready"
	} else {
		volume.Status = "pending"
	}
	return volume, nil
}

func (p *TencentProvider) ReadStorageVolume(ctx context.Context, volume StorageVolume) (StorageVolume, error) {
	if volume.ID == "" || !strings.HasPrefix(volume.ProviderResourceID, "disk-") {
		return StorageVolume{}, fmt.Errorf("storage_volume_cbs_identity_required")
	}
	region := volume.ProviderData["region"]
	if region == "" || region != strings.TrimSpace(region) {
		return volume, fmt.Errorf("storage_cbs_region_identity_required")
	}
	diskType := volume.DiskType
	if _, present := tencentWorkspacePlanContextValue(ctx); present {
		bound, ok := tencentWorkspacePlanFromContext(ctx)
		if !ok || volume.ProviderData["region"] != bound.Region || volume.SizeGB != bound.Storage.SizeGB ||
			volume.Zone != bound.Zone || volume.DiskType != bound.Storage.DiskType {
			return volume, ErrLaunchStageBindingConflict
		}
	}
	response, err := p.provision(ctx, provisionerRequest{Action: "sync_storage_volume", AccountID: volume.AccountID, Region: region, Tags: volume.CostTags, Storage: provisionerStorage{
		ID: volume.ProviderResourceID, SizeGB: uint64(volume.SizeGB), Zone: volume.Zone, DiskType: diskType, Deadline: volume.Deadline,
	}})
	if err != nil {
		return volume, err
	}
	if response.OK && (response.StorageVolumeID != volume.ProviderResourceID || !exactStorageRegionReadback(response, region)) {
		return volume, fmt.Errorf("storage_cbs_region_readback_mismatch")
	}
	if strings.HasPrefix(response.StorageVolumeID, "disk-") {
		volume.ProviderResourceID = response.StorageVolumeID
	}
	applyStorageReadback(&volume, response)
	if !response.OK {
		return volume, provisionerError(response)
	}
	if response.Status == "external_deleted" {
		volume.Status = "external_deleted"
		return volume, nil
	}
	if !isCBSProviderReady(volume.CBSStatus) {
		volume.Status = "pending"
		return volume, nil
	}
	volume.Status = "ready"
	if volume.Provider == "" {
		volume.Provider = "tencent-tke"
	}
	return volume, nil
}

func (p *TencentProvider) tencentStoragePlanForContext(ctx context.Context, input StorageVolumeInput) (tencentStoragePlan, error) {
	if _, present := tencentWorkspacePlanContextValue(ctx); present {
		bound, ok := tencentWorkspacePlanFromContext(ctx)
		if !ok || input.SizeGB != bound.Storage.SizeGB || input.Zone != bound.Zone || bound.Storage.DiskType == "" ||
			bound.Storage.DiskType != strings.TrimSpace(bound.Storage.DiskType) {
			return tencentStoragePlan{}, ErrLaunchStageBindingConflict
		}
		return bound.Storage, nil
	}
	if p == nil {
		return tencentStoragePlan{}, ErrProviderPlanUnavailable
	}
	diskType := p.storageDiskType
	if diskType == "" {
		return tencentStoragePlan{}, fmt.Errorf("tencent_storage_disk_type_required")
	}
	return tencentStoragePlan{SizeGB: input.SizeGB, DiskType: diskType}, nil
}

func (p *TencentProvider) ReadStorageProviderFacts(ctx context.Context, volume StorageVolume) (ProviderResourceFacts, error) {
	readback, err := p.ReadStorageVolume(ctx, volume)
	if err != nil {
		return ProviderResourceFacts{}, err
	}
	return ProviderResourceFacts{
		PackageOrSpec: firstNonEmpty(readback.DiskType, readback.StorageClass),
		ProviderID:    readback.ProviderResourceID,
		Zone:          readback.Zone,
		Status:        firstNonEmpty(readback.CBSStatus, readback.Status),
		ExpiresAt:     readback.Deadline,
	}, nil
}

func (p *TencentProvider) DestroyStorageVolume(ctx context.Context, volume StorageVolume) (StorageVolume, error) {
	pv, pvc, validIdentity := tencentStorageDestroyBindingIdentity(volume)
	if !validIdentity {
		return volume, fmt.Errorf("storage_volume_destroy_identity_required")
	}
	if _, err := p.ReadStaticStorageBinding(ctx, volume); err != nil {
		return volume, fmt.Errorf("storage_volume_destroy_binding_unverified: %w", err)
	}
	readback, err := p.ReadStorageVolume(ctx, volume)
	if err != nil {
		return volume, err
	}
	if !sameStorageDestroyStableIdentity(volume, readback) {
		return volume, fmt.Errorf("storage_volume_destroy_readback_mismatch")
	}
	volume = readback
	resources := []string{"pvc/" + pvc, "pv/" + pv}
	if _, err := p.callKubectl(ctx, append([]string{"delete"}, append(resources, "--ignore-not-found=true", "--wait=true")...), nil, protectedresource.Target{}); err != nil {
		return StorageVolume{}, err
	}
	expectedProviderResourceID := volume.ProviderResourceID
	response, err := p.provision(ctx, provisionerRequest{
		Action: "destroy_storage_volume", AccountID: volume.AccountID, Region: volume.ProviderData["region"], Tags: volume.CostTags,
		Storage: provisionerStorage{ID: volume.ProviderResourceID, SizeGB: uint64(volume.SizeGB), Zone: volume.Zone, DiskType: volume.DiskType},
	})
	if err != nil {
		return volume, err
	}
	if !validStorageDestroyMutationEvidence(response) {
		return volume, fmt.Errorf("storage_volume_destroy_readback_mismatch")
	}
	result := cloneStorageVolume(volume)
	applyStorageDestroyReadback(&result, response)
	if response.Status != "" {
		result.Status = response.Status
	}
	if !validStorageDestroyResponseProviderData(volume.ProviderData, response.ProviderData) {
		return result, fmt.Errorf("storage_volume_destroy_readback_mismatch")
	}
	if !response.OK {
		return result, provisionerError(response)
	}
	if response.StorageVolumeID != expectedProviderResourceID || !storageDestroyReadbackConfirmsAbsence(result) {
		return result, fmt.Errorf("storage_volume_destroy_readback_mismatch")
	}
	return result, nil
}

func tencentStorageDestroyBindingIdentity(volume StorageVolume) (string, string, bool) {
	name := k8sName(volume.ID)
	pv, pvc := name+"-pv", name+"-data"
	expectedTags := oplCostTags(volume.AccountID, volume.WorkspaceID, volume.ID, volume.OperationID)
	valid := strings.TrimSpace(volume.ID) != "" && strings.TrimSpace(volume.AccountID) != "" && strings.TrimSpace(volume.WorkspaceID) != "" &&
		strings.TrimSpace(volume.OperationID) != "" && volume.Provider == "tencent-tke" && strings.HasPrefix(volume.ProviderResourceID, "disk-") &&
		len(volume.ProviderResourceID) > len("disk-") && volume.SizeGB > 0 && strings.TrimSpace(volume.Zone) != "" && strings.TrimSpace(volume.DiskType) != "" &&
		reflect.DeepEqual(volume.CostTags, expectedTags) && volume.ProviderData["pvName"] == pv && volume.ProviderData["pvcName"] == pvc &&
		strings.TrimSpace(volume.ProviderData["region"]) != "" && volume.ProviderData["region"] == strings.TrimSpace(volume.ProviderData["region"])
	return pv, pvc, valid
}

func exactStorageRegionReadback(response provisionerResponse, expected string) bool {
	region := response.ProviderData["region"]
	return region != "" && region == strings.TrimSpace(region) && (expected == "" || region == expected)
}

func validStorageDestroyMutationEvidence(response provisionerResponse) bool {
	phase := response.ProviderData["storageDestroyPhase"]
	count := response.ProviderData["storageDestroyMutationCount"]
	switch phase {
	case "terminate_not_attempted", "precondition_unconfirmed":
		return response.MutationCount == 0 && count == "0"
	case "terminate_attempted":
		return response.MutationCount == 1 && count == "1"
	case "absence_confirmed":
		return response.MutationCount == 0 && count == "0" || response.MutationCount == 1 && count == "1"
	default:
		return false
	}
}

func validStorageDestroyResponseProviderData(previous, response map[string]string) bool {
	for key, value := range response {
		if isStorageDestroyEvidenceKey(key) {
			continue
		}
		if previousValue, exists := previous[key]; !exists || previousValue != value {
			return false
		}
	}
	return true
}

func applyStorageDestroyReadback(volume *StorageVolume, response provisionerResponse) {
	volume.ProviderRequestID = firstNonEmpty(response.ProviderRequestID, volume.ProviderRequestID)
	volume.CBSStatus = response.CBSStatus
	if volume.ProviderData == nil {
		volume.ProviderData = map[string]string{}
	}
	for key, value := range response.ProviderData {
		if isStorageDestroyEvidenceKey(key) {
			volume.ProviderData[key] = value
		}
	}
}

func (p *TencentProvider) RenewStorageVolume(ctx context.Context, volume StorageVolume) (StorageVolume, error) {
	if volume.ID == "" || !strings.HasPrefix(volume.ProviderResourceID, "disk-") || strings.TrimSpace(volume.Deadline) == "" {
		return StorageVolume{}, fmt.Errorf("storage_volume_renew_identity_required")
	}
	region := volume.ProviderData["region"]
	if region == "" || region != strings.TrimSpace(region) {
		return volume, fmt.Errorf("storage_cbs_region_identity_required")
	}
	response, err := p.provision(ctx, provisionerRequest{Action: "renew_storage_volume", AccountID: volume.AccountID, Region: region, Tags: volume.CostTags, Storage: provisionerStorage{
		ID: volume.ProviderResourceID, SizeGB: uint64(volume.SizeGB), Zone: volume.Zone, DiskType: volume.DiskType, Deadline: volume.Deadline,
	}})
	if err != nil {
		return StorageVolume{}, err
	}
	if response.OK && (response.StorageVolumeID != volume.ProviderResourceID || !exactStorageRegionReadback(response, region)) {
		return volume, fmt.Errorf("storage_cbs_region_readback_mismatch")
	}
	if response.StorageVolumeID != "" {
		volume.ProviderResourceID = response.StorageVolumeID
	}
	applyStorageReadback(&volume, response)
	if !response.OK {
		return volume, provisionerError(response)
	}
	return volume, nil
}

func applyStorageReadback(volume *StorageVolume, response provisionerResponse) {
	volume.ProviderRequestID = firstNonEmpty(response.ProviderRequestID, volume.ProviderRequestID)
	volume.CBSStatus = response.CBSStatus
	if volume.ProviderData == nil {
		volume.ProviderData = map[string]string{}
	}
	if !response.OK {
		for key, value := range response.ProviderData {
			if isStorageDestroyEvidenceKey(key) {
				volume.ProviderData[key] = value
			}
		}
		return
	}
	for key, value := range response.ProviderData {
		volume.ProviderData[key] = value
	}
	volume.DiskType = firstNonEmpty(response.ProviderData["diskType"], volume.DiskType)
	volume.RenewFlag = firstNonEmpty(response.ProviderData["renewFlag"], volume.RenewFlag)
	volume.Deadline = firstNonEmpty(response.ProviderData["deadline"], volume.Deadline)
	volume.Zone = firstNonEmpty(response.ProviderData["zone"], volume.Zone)
}

func isCBSProviderReady(status string) bool {
	return status == "UNATTACHED" || status == "ATTACHED"
}
