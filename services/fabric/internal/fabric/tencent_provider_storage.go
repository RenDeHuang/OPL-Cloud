package fabric

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"opl-cloud/services/fabric/internal/protectedresource"
)

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
	volume := StorageVolume{ID: input.ID, OperationID: input.IdempotencyKey, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Provider: "tencent-tke", SizeGB: input.SizeGB, Zone: input.Zone}
	cbsAttempt, err := beginProviderMutation(ctx, "tencent_cbs_create", "storage_volume", input.ID, input.ExpectedProviderResourceID)
	if err != nil {
		return volume, err
	}
	if cbsAttempt != nil && !cbsAttempt.Fresh {
		_ = cbsAttempt.resource(&volume)
		volume, err = p.ReadCBSVolume(ctx, input, volume)
		if errors.Is(err, ErrWorkspaceLaunchResourceAbsent) {
			claimed, claimErr := cbsAttempt.claimReplay(ctx)
			if claimErr != nil || !claimed {
				return volume, firstNonNil(claimErr, ErrWorkspaceLaunchPending)
			}
			volume, err = p.ReadCBSVolume(ctx, input, volume)
			if errors.Is(err, ErrWorkspaceLaunchResourceAbsent) {
				if err = cbsAttempt.markReplayDispatch(ctx); err != nil {
					return volume, err
				}
				volume, err = p.CreateCBSVolume(ctx, input)
				if err == nil {
					volume, err = p.ReadCBSVolume(ctx, input, volume)
				}
			}
		}
	} else {
		volume, err = p.CreateCBSVolume(ctx, input)
		if err == nil {
			volume, err = p.ReadCBSVolume(ctx, input, volume)
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
	now := time.Now().UTC()
	id := firstNonEmpty(input.ID, fabricID("vol", input.WorkspaceID, now))
	diskType := firstNonEmpty(os.Getenv("TENCENT_CBS_DISK_TYPE"), "CLOUD_BSSD")
	tags := oplCostTags(input.AccountID, input.WorkspaceID, id, input.OperationID)
	volume := StorageVolume{
		ID: id, OperationID: input.IdempotencyKey, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "pending", Provider: "tencent-tke",
		SizeGB: input.SizeGB, DiskType: diskType, Zone: input.Zone, CostTags: tags, CreatedAt: now,
		ProviderData: map[string]string{"pvName": k8sName(id) + "-pv", "pvcName": k8sName(id) + "-data"},
	}
	response, err := p.provision(ctx, provisionerRequest{
		Action: "create_storage_volume", AccountID: input.AccountID, Tags: tags,
		Storage: provisionerStorage{
			ID: id, SizeGB: uint64(input.SizeGB), Zone: input.Zone, DiskType: diskType,
			ExpectedState: input.ExpectedRecoveryState, ExpectedProviderResourceID: input.ExpectedProviderResourceID,
			AllowExistingExactReplay: input.AllowExistingExactReplay,
		},
	})
	if err != nil {
		return volume, err
	}
	volume.ProviderRequestID = response.ProviderRequestID
	if strings.HasPrefix(response.StorageVolumeID, "disk-") {
		volume.ProviderResourceID = response.StorageVolumeID
	}
	applyStorageReadback(&volume, response)
	if !response.OK {
		return volume, provisionerError(response)
	}
	if volume.ProviderResourceID == "" {
		return volume, fmt.Errorf("storage_cbs_identity_required")
	}
	return volume, nil
}

// ReadCBSVolume is intentionally Describe-only. The persisted disk identity
// is authoritative; a response naming another disk is an identity failure.
func (p *TencentProvider) ReadCBSVolume(ctx context.Context, input StorageVolumeInput, persisted StorageVolume) (StorageVolume, error) {
	if persisted.ID == "" {
		persisted.ID = input.ID
	}
	if persisted.AccountID == "" {
		persisted.AccountID = input.AccountID
	}
	if persisted.WorkspaceID == "" {
		persisted.WorkspaceID = input.WorkspaceID
	}
	if persisted.SizeGB == 0 {
		persisted.SizeGB = input.SizeGB
	}
	if persisted.Zone == "" {
		persisted.Zone = input.Zone
	}
	if persisted.Provider == "" {
		persisted.Provider = "tencent-tke"
	}
	if len(persisted.CostTags) == 0 {
		persisted.CostTags = oplCostTags(input.AccountID, input.WorkspaceID, input.ID, input.OperationID)
	}
	if persisted.ID == "" || persisted.AccountID != input.AccountID || persisted.WorkspaceID != input.WorkspaceID || persisted.SizeGB != input.SizeGB || persisted.Zone != input.Zone {
		return persisted, fmt.Errorf("storage_cbs_readback_identity_required")
	}
	if !strings.HasPrefix(persisted.ProviderResourceID, "disk-") {
		if input.OperationID == "" {
			return persisted, fmt.Errorf("storage_cbs_readback_identity_required")
		}
		diskType := firstNonEmpty(os.Getenv("TENCENT_CBS_DISK_TYPE"), "CLOUD_BSSD")
		discovery, discoverErr := p.provision(ctx, provisionerRequest{
			Action: "discover_storage_volume", AccountID: input.AccountID,
			Tags:    oplCostTags(input.AccountID, input.WorkspaceID, input.ID, input.OperationID),
			Storage: provisionerStorage{ID: input.ID, SizeGB: uint64(input.SizeGB), Zone: input.Zone, DiskType: diskType},
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
		persisted.ProviderResourceID = discovery.StorageVolumeID
		persisted.ProviderRequestID = firstNonEmpty(discovery.ProviderRequestID, persisted.ProviderRequestID)
	}
	readback, err := p.ReadStorageVolume(ctx, persisted)
	if err != nil {
		return readback, err
	}
	if readback.ProviderResourceID != persisted.ProviderResourceID ||
		readback.ID != persisted.ID || readback.AccountID != persisted.AccountID || readback.WorkspaceID != persisted.WorkspaceID ||
		readback.SizeGB != persisted.SizeGB || readback.Zone != persisted.Zone ||
		(persisted.DiskType != "" && readback.DiskType != persisted.DiskType) ||
		(persisted.RenewFlag != "" && readback.RenewFlag != persisted.RenewFlag) ||
		(persisted.Deadline != "" && readback.Deadline != persisted.Deadline) ||
		(readback.ProviderData["zone"] != "" && readback.ProviderData["zone"] != persisted.Zone) {
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
	response, err := p.provision(ctx, provisionerRequest{Action: "sync_storage_volume", AccountID: volume.AccountID, Tags: volume.CostTags, Storage: provisionerStorage{
		ID: volume.ProviderResourceID, SizeGB: uint64(volume.SizeGB), Zone: volume.Zone, DiskType: volume.DiskType, Deadline: volume.Deadline,
	}})
	if err != nil {
		return volume, err
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
	if volume.ID == "" {
		return StorageVolume{}, fmt.Errorf("storage_volume_id_required")
	}
	pv, pvc := storageBindingNames(volume)
	resources := []string{}
	if pvc != "" {
		resources = append(resources, "pvc/"+pvc)
	}
	if pv != "" {
		resources = append(resources, "pv/"+pv)
	}
	if len(resources) > 0 {
		if _, err := p.callKubectl(ctx, append([]string{"delete"}, append(resources, "--ignore-not-found=true", "--wait=true")...), nil, protectedresource.Target{}); err != nil {
			return StorageVolume{}, err
		}
	}
	volume.Status = "released"
	if strings.HasPrefix(volume.ProviderResourceID, "disk-") {
		volume.Status = "retained"
	}
	volume.ProviderRequestID = providerRequestID("storage-destroy", volume.ID)
	if volume.Provider == "" {
		volume.Provider = "tencent-tke"
	}
	return volume, nil
}

func (p *TencentProvider) RenewStorageVolume(ctx context.Context, volume StorageVolume) (StorageVolume, error) {
	if volume.ID == "" || !strings.HasPrefix(volume.ProviderResourceID, "disk-") || strings.TrimSpace(volume.Deadline) == "" {
		return StorageVolume{}, fmt.Errorf("storage_volume_renew_identity_required")
	}
	response, err := p.provision(ctx, provisionerRequest{Action: "renew_storage_volume", AccountID: volume.AccountID, Tags: volume.CostTags, Storage: provisionerStorage{
		ID: volume.ProviderResourceID, SizeGB: uint64(volume.SizeGB), Zone: volume.Zone, DiskType: volume.DiskType, Deadline: volume.Deadline,
	}})
	if err != nil {
		return StorageVolume{}, err
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
