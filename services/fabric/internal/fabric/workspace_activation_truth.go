package fabric

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"opl-cloud/services/fabric/internal/protectedresource"
)

const workspaceActivationTruthSchemaVersion = 1

type workspaceActivationTruthProvider interface {
	WorkspaceActivationTruth(context.Context, WorkspaceActivationTruthInput, ComputeAllocation, StorageVolume, StorageAttachment) (WorkspaceActivationTruth, error)
}

func (s *Service) WorkspaceActivationTruth(ctx context.Context, input WorkspaceActivationTruthInput) (WorkspaceActivationTruth, error) {
	unknown := WorkspaceActivationTruth{
		SchemaVersion: workspaceActivationTruthSchemaVersion,
		Reason:        "identity_mismatch",
		ComputeState:  "unknown",
		StorageState:  "unknown",
		Checks:        []Check{},
	}
	if !validWorkspaceActivationTruthRequest(input) {
		return activationTruthFailure(unknown, "identity_mismatch", "readback_mismatch", ErrInvalidWorkspaceActivationTruth)
	}
	operations, err := s.operations.List(ctx)
	if err != nil {
		return activationTruthFailure(unknown, "provider_unavailable", "provider_error", ErrWorkspaceActivationTruthUnavailable)
	}
	computeOperation, ok := exactWorkspaceActivationOperation(operations, "create_compute_allocation", "compute_allocation", input.ComputeAllocationID, input.ComputeOperationID, input.AccountID, input.WorkspaceID)
	if !ok {
		return activationTruthFailure(unknown, "identity_mismatch", "readback_mismatch", ErrInvalidWorkspaceActivationTruth)
	}
	storageOperation, ok := exactWorkspaceActivationOperation(operations, "create_storage_volume", "storage_volume", input.StorageVolumeID, input.StorageOperationID, input.AccountID, input.WorkspaceID)
	if !ok {
		return activationTruthFailure(unknown, "identity_mismatch", "readback_mismatch", ErrInvalidWorkspaceActivationTruth)
	}
	attachmentOperation, ok := exactWorkspaceActivationOperation(operations, "create_storage_attachment", "storage_attachment", input.AttachmentID, input.AttachmentOperationID, input.AccountID, input.WorkspaceID)
	if !ok {
		return activationTruthFailure(unknown, "identity_mismatch", "readback_mismatch", ErrInvalidWorkspaceActivationTruth)
	}
	runtimeOperation, ok := exactWorkspaceActivationOperation(operations, "create_workspace_runtime", "workspace_runtime", input.WorkspaceID, input.RuntimeOperationID, input.AccountID, input.WorkspaceID)
	if !ok {
		return activationTruthFailure(unknown, "identity_mismatch", "readback_mismatch", ErrInvalidWorkspaceActivationTruth)
	}

	var compute ComputeAllocation
	var storage StorageVolume
	var attachment StorageAttachment
	var runtime WorkspaceRuntime
	if !decodeOperationResource(computeOperation, &compute) || !decodeOperationResource(storageOperation, &storage) ||
		!decodeOperationResource(attachmentOperation, &attachment) || !decodeOperationResource(runtimeOperation, &runtime) {
		return activationTruthFailure(unknown, "identity_mismatch", "readback_mismatch", ErrInvalidWorkspaceActivationTruth)
	}
	compute.OperationID = computeOperation.IdempotencyKey
	storage.OperationID = storageOperation.IdempotencyKey
	attachment.OperationID = attachmentOperation.IdempotencyKey
	runtime.OperationID = runtimeOperation.IdempotencyKey
	unknown.Compute, unknown.Storage, unknown.Attachment = compute, storage, attachment
	if runtime.ID != input.RuntimeID || runtime.OperationID != input.RuntimeOperationID || runtime.WorkspaceID != input.WorkspaceID || runtime.ServiceName != input.ServiceName ||
		!validWorkspaceActivationTruthInput(input, compute, storage, attachment) {
		return activationTruthFailure(unknown, "identity_mismatch", "readback_mismatch", ErrInvalidWorkspaceActivationTruth)
	}
	provider, ok := s.provider.(workspaceActivationTruthProvider)
	if !ok {
		return activationTruthFailure(unknown, "provider_unavailable", "provider_error", ErrWorkspaceActivationTruthUnavailable)
	}
	result, err := provider.WorkspaceActivationTruth(ctx, input, cloneComputeAllocation(compute), cloneStorageVolume(storage), attachment)
	if err != nil {
		if result.SchemaVersion == 0 {
			result = unknown
		}
		return result, fmt.Errorf("%w: %s:%s", ErrWorkspaceActivationTruthUnavailable, result.Reason, result.ErrorClass)
	}
	if !validWorkspaceActivationTruthResult(result, input, compute, storage, attachment) {
		return activationTruthFailure(unknown, "identity_mismatch", "readback_mismatch", ErrWorkspaceActivationTruthUnavailable)
	}
	return result, nil
}

func validWorkspaceActivationTruthRequest(input WorkspaceActivationTruthInput) bool {
	values := []string{
		input.LaunchOperationID, input.AccountID, input.WorkspaceID, input.ComputeAllocationID, input.ComputeOperationID,
		input.StorageVolumeID, input.StorageOperationID, input.AttachmentID, input.AttachmentOperationID,
		input.RuntimeID, input.RuntimeOperationID, input.ServiceName, input.WorkspaceImageDigest,
		input.GatewaySecretRef, input.GatewaySecretFingerprint,
	}
	for _, value := range values {
		if value == "" || value != strings.TrimSpace(value) {
			return false
		}
	}
	_, imageOK := approvedWorkspaceImageDigest(input.WorkspaceImageDigest)
	return imageOK && input.WorkspaceAPIKeyID > 0 && validDigest(strings.TrimPrefix(input.GatewaySecretFingerprint, "sha256:")) &&
		input.ComputeOperationID == input.LaunchOperationID+":compute" &&
		input.StorageOperationID == input.LaunchOperationID+":storage" &&
		input.AttachmentOperationID == input.LaunchOperationID+":attachment" &&
		input.RuntimeOperationID == input.LaunchOperationID+":workspace:runtime"
}

func exactWorkspaceActivationOperation(operations []FabricOperation, action, resourceKind, resourceID, idempotencyKey, accountID, workspaceID string) (FabricOperation, bool) {
	matches := []FabricOperation{}
	for _, operation := range operations {
		if operation.Action != action || operation.ResourceKind != resourceKind {
			continue
		}
		if operation.IdempotencyKey == idempotencyKey || operation.ResourceID == resourceID ||
			operation.AccountID == accountID && operation.WorkspaceID == workspaceID {
			matches = append(matches, operation)
		}
	}
	if len(matches) == 0 {
		return FabricOperation{}, false
	}
	var succeeded FabricOperation
	for _, operation := range matches {
		if operation.ResourceID != resourceID || operation.IdempotencyKey != idempotencyKey ||
			operation.AccountID != accountID || operation.WorkspaceID != workspaceID || operation.ID == "" || operation.OperationID == "" {
			return FabricOperation{}, false
		}
		switch operation.Status {
		case "started":
		case "succeeded":
			if succeeded.ID != "" {
				return FabricOperation{}, false
			}
			succeeded = operation
		default:
			return FabricOperation{}, false
		}
	}
	for _, operation := range matches {
		if operation.RequestHash != succeeded.RequestHash {
			return FabricOperation{}, false
		}
	}
	return succeeded, succeeded.ID != ""
}

func validWorkspaceActivationTruthResult(result WorkspaceActivationTruth, input WorkspaceActivationTruthInput, compute ComputeAllocation, storage StorageVolume, attachment StorageAttachment) bool {
	return result.SchemaVersion == workspaceActivationTruthSchemaVersion && result.Ready && result.Reason == "none" && result.ErrorClass == "" &&
		result.ComputeState == "ready" && result.StorageState == "ready" &&
		result.Compute.ID == compute.ID && result.Compute.OperationID == compute.OperationID &&
		result.Storage.ID == storage.ID && result.Storage.OperationID == storage.OperationID &&
		result.Attachment.ID == attachment.ID && result.Attachment.OperationID == attachment.OperationID &&
		result.Runtime.ID == input.RuntimeID && result.Runtime.OperationID == input.RuntimeOperationID && result.Runtime.ServiceName == input.ServiceName &&
		result.Sub2APIMutationCount == 0 && result.TencentMutationCount == 0 && result.KubernetesMutationCount == 0 && result.Checks != nil
}

func (p *TencentProvider) WorkspaceActivationTruth(
	ctx context.Context,
	input WorkspaceActivationTruthInput,
	compute ComputeAllocation,
	storage StorageVolume,
	attachment StorageAttachment,
) (WorkspaceActivationTruth, error) {
	truth := WorkspaceActivationTruth{
		SchemaVersion: workspaceActivationTruthSchemaVersion,
		Reason:        "identity_mismatch",
		ComputeState:  "unknown",
		StorageState:  "unknown",
		Compute:       compute,
		Storage:       storage,
		Attachment:    attachment,
		Checks:        []Check{},
	}
	if !validWorkspaceActivationTruthInput(input, compute, storage, attachment) {
		return activationTruthFailure(truth, "identity_mismatch", "readback_mismatch", ErrInvalidWorkspaceActivationTruth)
	}

	providerTruth, err := p.MonthlyProviderTruth(ctx, compute, storage)
	truth.ComputeState, truth.StorageState = providerTruth.ComputeState, providerTruth.StorageState
	truth.Compute, truth.Storage = providerTruth.Compute, providerTruth.Storage
	if err != nil {
		return activationTruthProviderFailure(truth, err)
	}
	if providerTruth.ComputeState != "ready" || providerTruth.StorageState != "ready" ||
		!sameActivationCompute(compute, providerTruth.Compute) || !sameActivationStorage(storage, providerTruth.Storage) {
		return activationTruthFailure(truth, "identity_mismatch", "readback_mismatch", ErrWorkspaceActivationTruthUnavailable)
	}
	truth.Checks = append(truth.Checks,
		Check{Name: "compute_provider_truth", OK: true},
		Check{Name: "storage_provider_truth", OK: true},
	)

	workspaceRaw, err := p.callKubectl(ctx, []string{
		"get", "deployment,service,networkpolicy,secret,pod,pv,pvc",
		"-l", "oplcloud.cn/workspace-id=" + input.WorkspaceID, "-o", "json",
	}, nil, protectedresource.Target{})
	if err != nil {
		return activationTruthProviderFailure(truth, err)
	}
	exactRaw, err := p.callKubectl(ctx, []string{
		"get", "endpoints/" + input.ServiceName, "secret/" + input.GatewaySecretRef,
		"node/" + compute.NodeName, "-o", "json",
	}, nil, protectedresource.Target{})
	if err != nil {
		return activationTruthProviderFailure(truth, err)
	}
	volumeAttachmentRaw, err := p.callKubectl(ctx, []string{"get", "volumeattachment", "-o", "json"}, nil, protectedresource.Target{})
	if err != nil {
		return activationTruthProviderFailure(truth, err)
	}

	workspaceItems, err := strictKubectlItems(workspaceRaw)
	if err != nil {
		return activationTruthProviderFailure(truth, err)
	}
	exactItems, err := strictKubectlItems(exactRaw)
	if err != nil {
		return activationTruthProviderFailure(truth, err)
	}
	deployment, state := exactActivationResource(workspaceItems, "Deployment", input.ServiceName)
	if state != "one" {
		return activationTruthCardinalityFailure(truth, state)
	}
	service, state := exactActivationResource(workspaceItems, "Service", input.ServiceName)
	if state != "one" {
		return activationTruthCardinalityFailure(truth, state)
	}
	policy, state := exactActivationResource(workspaceItems, "NetworkPolicy", input.ServiceName)
	if state != "one" {
		return activationTruthCardinalityFailure(truth, state)
	}
	runtimeSecret, state := exactActivationResource(workspaceItems, "Secret", input.ServiceName+"-env")
	if state != "one" {
		return activationTruthCardinalityFailure(truth, state)
	}
	pvName, pvcName := storageBindingNames(storage)
	pv, state := exactActivationResource(workspaceItems, "PersistentVolume", pvName)
	if state != "one" {
		return activationTruthCardinalityFailure(truth, state)
	}
	pvc, state := exactActivationResource(workspaceItems, "PersistentVolumeClaim", pvcName)
	if state != "one" {
		return activationTruthCardinalityFailure(truth, state)
	}
	endpoint, state := exactActivationResource(exactItems, "Endpoints", input.ServiceName)
	if state != "one" {
		return activationTruthCardinalityFailure(truth, state)
	}
	gatewaySecret, state := exactActivationResource(exactItems, "Secret", input.GatewaySecretRef)
	if state != "one" {
		return activationTruthCardinalityFailure(truth, state)
	}
	node, state := exactActivationResource(exactItems, "Node", compute.NodeName)
	if state != "one" {
		return activationTruthCardinalityFailure(truth, state)
	}

	pods := activationResourcesOfKind(workspaceItems, "Pod")
	activePods := make([]map[string]any, 0, len(pods))
	for _, pod := range pods {
		phase := stringValue(nested(pod, "status", "phase"))
		if phase != "Succeeded" && phase != "Failed" {
			activePods = append(activePods, pod)
		}
	}
	if len(activePods) != 1 {
		state := "none"
		if len(activePods) > 1 {
			state = "multiple"
		}
		return activationTruthCardinalityFailure(truth, state)
	}
	pod := activePods[0]

	labels := activationIdentityLabels(input)
	for _, resource := range []map[string]any{deployment, service, policy, runtimeSecret, pod} {
		if !resourceHasActivationIdentity(resource, labels) {
			return activationTruthFailure(truth, "identity_mismatch", "readback_mismatch", ErrWorkspaceActivationTruthUnavailable)
		}
	}
	if !validActivationStorageBinding(input, storage, attachment, pv, pvc, pvName, pvcName) ||
		!validActivationNode(input, compute, node) ||
		!validActivationGatewaySecret(input, gatewaySecret) ||
		!validActivationRuntimeSecret(runtimeSecret) {
		return activationTruthFailure(truth, "identity_mismatch", "readback_mismatch", ErrWorkspaceActivationTruthUnavailable)
	}

	volumeAttachmentItems, err := strictKubectlItems(volumeAttachmentRaw)
	if err != nil {
		return activationTruthProviderFailure(truth, err)
	}
	volumeAttachment, state := exactActivationVolumeAttachment(volumeAttachmentItems, pvName)
	if state != "one" {
		return activationTruthCardinalityFailure(truth, state)
	}
	if nested(volumeAttachment, "status", "attached") != true ||
		stringValue(nested(volumeAttachment, "spec", "attacher")) != "com.tencent.cloud.csi.cbs" ||
		stringValue(nested(volumeAttachment, "spec", "nodeName")) != compute.NodeName {
		return activationTruthFailure(truth, "identity_mismatch", "readback_mismatch", ErrWorkspaceActivationTruthUnavailable)
	}

	wantSelector := stringAnyMap(runtimeSelectorLabels(input.ServiceName, compute))
	if !reflect.DeepEqual(nested(deployment, "spec", "selector", "matchLabels"), wantSelector) ||
		!reflect.DeepEqual(nested(service, "spec", "selector"), wantSelector) ||
		!workspaceNetworkPolicyReady(policy, deployment) ||
		!workloadUsesPVC(deployment, pvcName) || !workloadUsesPVC(pod, pvcName) {
		return activationTruthFailure(truth, "identity_mismatch", "readback_mismatch", ErrWorkspaceActivationTruthUnavailable)
	}

	conditions := conditionStatuses(nested(pod, "status", "conditions"))
	podName := stringValue(nested(pod, "metadata", "name"))
	podUID := stringValue(nested(pod, "metadata", "uid"))
	podIP := stringValue(nested(pod, "status", "podIP"))
	imageID, imageReady := activationWorkspaceImageID(pod)
	approvedDigest, approved := approvedWorkspaceImageDigest(input.WorkspaceImageDigest)
	runtimeDigest, runtimeImageReady := runtimeImageDigest(imageID)
	if conditions["PodScheduled"] != "True" || conditions["Ready"] != "True" ||
		!workspaceRuntimeIsolationReady(deployment, []any{pod}) ||
		stringValue(nested(pod, "spec", "nodeName")) != compute.NodeName || podName == "" || podUID == "" || podIP == "" ||
		!imageReady || !approved || !runtimeImageReady || runtimeDigest != approvedDigest ||
		!activationDeploymentImageMatches(deployment, approvedDigest) {
		return activationTruthFailure(truth, "identity_mismatch", "readback_mismatch", ErrWorkspaceActivationTruthUnavailable)
	}
	endpointIPs, endpointOK := activationEndpointIPs(endpoint, podName, podUID, podIP)
	if !endpointOK {
		return activationTruthFailure(truth, "identity_mismatch", "readback_mismatch", ErrWorkspaceActivationTruthUnavailable)
	}

	truth.Ready = true
	truth.Reason = "none"
	truth.ErrorClass = ""
	truth.Runtime = WorkspaceActivationRuntimeTruth{
		ID:                   input.RuntimeID,
		OperationID:          input.RuntimeOperationID,
		ServiceName:          input.ServiceName,
		DeploymentName:       input.ServiceName,
		RuntimeSecretRef:     input.ServiceName + "-env",
		GatewaySecretRef:     input.GatewaySecretRef,
		PVName:               pvName,
		PVCName:              pvcName,
		VolumeAttachmentName: stringValue(nested(volumeAttachment, "metadata", "name")),
		PodName:              podName,
		PodIP:                podIP,
		NodeName:             compute.NodeName,
		ImageID:              imageID,
		EndpointIPs:          endpointIPs,
	}
	truth.Checks = append(truth.Checks,
		Check{Name: "compute_ownership", OK: true},
		Check{Name: "storage_binding", OK: true},
		Check{Name: "attachment_identity", OK: true},
		Check{Name: "gateway_secret_identity", OK: true},
		Check{Name: "runtime_cardinality", OK: true},
		Check{Name: "runtime_ready_pod", OK: true},
		Check{Name: "service_endpoints_identity", OK: true},
		Check{Name: "workspace_network_policy", OK: true},
	)
	return truth, nil
}

func validWorkspaceActivationTruthInput(input WorkspaceActivationTruthInput, compute ComputeAllocation, storage StorageVolume, attachment StorageAttachment) bool {
	_, imageOK := approvedWorkspaceImageDigest(input.WorkspaceImageDigest)
	return strings.TrimSpace(input.LaunchOperationID) != "" && strings.TrimSpace(input.AccountID) != "" && strings.TrimSpace(input.WorkspaceID) != "" &&
		strings.TrimSpace(input.ComputeAllocationID) != "" && strings.TrimSpace(input.ComputeOperationID) != "" &&
		strings.TrimSpace(input.StorageVolumeID) != "" && strings.TrimSpace(input.StorageOperationID) != "" &&
		strings.TrimSpace(input.AttachmentID) != "" && strings.TrimSpace(input.AttachmentOperationID) != "" &&
		strings.TrimSpace(input.RuntimeID) != "" && strings.TrimSpace(input.RuntimeOperationID) != "" && strings.TrimSpace(input.ServiceName) != "" &&
		strings.TrimSpace(input.GatewaySecretRef) != "" && input.WorkspaceAPIKeyID > 0 && validDigest(strings.TrimPrefix(input.GatewaySecretFingerprint, "sha256:")) && imageOK &&
		compute.ID == input.ComputeAllocationID && compute.OperationID == input.ComputeOperationID && compute.AccountID == input.AccountID && compute.WorkspaceID == input.WorkspaceID &&
		storage.ID == input.StorageVolumeID && storage.OperationID == input.StorageOperationID && storage.AccountID == input.AccountID && storage.WorkspaceID == input.WorkspaceID &&
		attachment.ID == input.AttachmentID && attachment.OperationID == input.AttachmentOperationID && attachment.WorkspaceID == input.WorkspaceID &&
		attachment.ComputeID == compute.ID && attachment.VolumeID == storage.ID && attachment.Status == "attached" &&
		strings.TrimSpace(compute.NodeName) != "" && strings.TrimSpace(compute.PrivateIP) != "" && strings.TrimSpace(storage.ProviderResourceID) != ""
}

func sameActivationCompute(expected, actual ComputeAllocation) bool {
	return actual.ID == expected.ID && actual.OperationID == expected.OperationID && actual.AccountID == expected.AccountID && actual.WorkspaceID == expected.WorkspaceID &&
		firstNonEmpty(actual.InstanceID, actual.CVMInstanceID) == firstNonEmpty(expected.InstanceID, expected.CVMInstanceID) &&
		actual.NodeName == expected.NodeName && actual.PrivateIP == expected.PrivateIP
}

func sameActivationStorage(expected, actual StorageVolume) bool {
	return actual.ID == expected.ID && actual.OperationID == expected.OperationID && actual.AccountID == expected.AccountID && actual.WorkspaceID == expected.WorkspaceID &&
		actual.ProviderResourceID == expected.ProviderResourceID && actual.SizeGB == expected.SizeGB && actual.Zone == expected.Zone
}

func activationIdentityLabels(input WorkspaceActivationTruthInput) map[string]string {
	return map[string]string{
		"oplcloud.cn/account-id":              input.AccountID,
		"oplcloud.cn/workspace-id":            input.WorkspaceID,
		"oplcloud.cn/compute-allocation-id":   input.ComputeAllocationID,
		"oplcloud.cn/storage-id":              input.StorageVolumeID,
		"oplcloud.cn/attachment-id":           input.AttachmentID,
		"oplcloud.cn/attachment-operation-id": input.AttachmentOperationID,
		"oplcloud.cn/runtime-id":              input.RuntimeID,
		"oplcloud.cn/runtime-operation-id":    input.RuntimeOperationID,
	}
}

func resourceHasActivationIdentity(resource map[string]any, expected map[string]string) bool {
	for key, value := range expected {
		if stringValue(nested(resource, "metadata", "labels", key)) != value {
			return false
		}
	}
	return true
}

func validActivationStorageBinding(input WorkspaceActivationTruthInput, storage StorageVolume, attachment StorageAttachment, pv, pvc map[string]any, pvName, pvcName string) bool {
	for _, resource := range []map[string]any{pv, pvc} {
		if stringValue(nested(resource, "metadata", "labels", "oplcloud.cn/account-id")) != input.AccountID ||
			stringValue(nested(resource, "metadata", "labels", "oplcloud.cn/workspace-id")) != input.WorkspaceID ||
			stringValue(nested(resource, "metadata", "labels", "oplcloud.cn/storage-id")) != input.StorageVolumeID {
			return false
		}
	}
	return pvName != "" && pvcName != "" && attachment.ProviderAttachmentID == "pv/"+pvName+":pvc/"+pvcName &&
		stringValue(nested(pv, "metadata", "name")) == pvName && stringValue(nested(pvc, "metadata", "name")) == pvcName &&
		stringValue(nested(pv, "spec", "csi", "driver")) == "com.tencent.cloud.csi.cbs" &&
		stringValue(nested(pv, "spec", "csi", "volumeHandle")) == storage.ProviderResourceID &&
		stringValue(nested(pvc, "spec", "volumeName")) == pvName && stringValue(nested(pvc, "status", "phase")) == "Bound"
}

func validActivationNode(input WorkspaceActivationTruthInput, compute ComputeAllocation, node map[string]any) bool {
	if stringValue(nested(node, "metadata", "name")) != compute.NodeName ||
		stringValue(nested(node, "metadata", "labels", "medopl.cn/workload")) != "workspace" ||
		stringValue(nested(node, "metadata", "labels", "oplcloud.cn/resource-id")) != compute.ID ||
		stringValue(nested(node, "metadata", "labels", "oplcloud.cn/account-id")) != input.AccountID ||
		stringValue(nested(node, "metadata", "labels", "oplcloud.cn/workspace-id")) != input.WorkspaceID {
		return false
	}
	addresses, _ := nested(node, "status", "addresses").([]any)
	matchingIPs := 0
	for _, item := range addresses {
		address, _ := item.(map[string]any)
		if stringValue(address["type"]) == "InternalIP" && stringValue(address["address"]) == compute.PrivateIP {
			matchingIPs++
		}
	}
	taints, _ := nested(node, "spec", "taints").([]any)
	matchingTaints := 0
	workspaceTaints := 0
	for _, item := range taints {
		taint, _ := item.(map[string]any)
		if stringValue(taint["key"]) == "oplcloud.cn/workspace-id" {
			workspaceTaints++
			if stringValue(taint["value"]) == input.WorkspaceID && stringValue(taint["effect"]) == "NoSchedule" {
				matchingTaints++
			}
		}
	}
	return matchingIPs == 1 && workspaceTaints == 1 && matchingTaints == 1
}

func validActivationGatewaySecret(input WorkspaceActivationTruthInput, secret map[string]any) bool {
	data, _ := secret["data"].(map[string]any)
	return stringValue(secret["type"]) == "Opaque" && stringValue(nested(secret, "metadata", "labels", "app.kubernetes.io/name")) == "opl-gateway-secret" &&
		stringValue(nested(secret, "metadata", "annotations", "oplcloud.cn/account-id")) == input.AccountID &&
		stringValue(nested(secret, "metadata", "annotations", "oplcloud.cn/workspace-id")) == input.WorkspaceID &&
		stringValue(nested(secret, "metadata", "annotations", "oplcloud.cn/workspace-api-key-id")) == strconv.FormatInt(input.WorkspaceAPIKeyID, 10) &&
		stringValue(nested(secret, "metadata", "annotations", "oplcloud.cn/secret-fingerprint")) == input.GatewaySecretFingerprint &&
		len(data) == 1 && stringValue(data["opl_gateway_api_key"]) != ""
}

func validActivationRuntimeSecret(secret map[string]any) bool {
	data, _ := secret["data"].(map[string]any)
	return stringValue(secret["type"]) == "Opaque" && len(data) == 2 && stringValue(data["webui_password"]) != "" && stringValue(data["webui_session_secret"]) != ""
}

func exactActivationResource(items []any, kind, name string) (map[string]any, string) {
	all := activationResourcesOfKind(items, kind)
	if len(all) == 0 {
		return map[string]any{}, "none"
	}
	if len(all) != 1 || stringValue(nested(all[0], "metadata", "name")) != name {
		return map[string]any{}, "multiple"
	}
	return all[0], "one"
}

func activationResourcesOfKind(items []any, kind string) []map[string]any {
	result := []map[string]any{}
	for _, item := range items {
		resource, ok := item.(map[string]any)
		if ok && stringValue(resource["kind"]) == kind {
			result = append(result, resource)
		}
	}
	return result
}

func exactActivationVolumeAttachment(items []any, pvName string) (map[string]any, string) {
	matches := []map[string]any{}
	for _, resource := range activationResourcesOfKind(items, "VolumeAttachment") {
		if stringValue(nested(resource, "spec", "source", "persistentVolumeName")) == pvName {
			matches = append(matches, resource)
		}
	}
	if len(matches) == 0 {
		return map[string]any{}, "none"
	}
	if len(matches) != 1 {
		return map[string]any{}, "multiple"
	}
	return matches[0], "one"
}

func activationWorkspaceImageID(pod map[string]any) (string, bool) {
	statuses, _ := nested(pod, "status", "containerStatuses").([]any)
	if len(statuses) != 1 {
		return "", false
	}
	status, _ := statuses[0].(map[string]any)
	imageID := stringValue(status["imageID"])
	return imageID, stringValue(status["name"]) == "workspace" && status["ready"] == true && imageID != ""
}

func activationDeploymentImageMatches(deployment map[string]any, approvedDigest string) bool {
	digest, ok := immutableImageDigest(stringValue(firstContainerField(deployment, "image")))
	return ok && digest == approvedDigest
}

func approvedWorkspaceImageDigest(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "sha256:") && len(value) == len("sha256:")+64 {
		return value, validDigest(strings.TrimPrefix(value, "sha256:"))
	}
	return immutableImageDigest(value)
}

func activationEndpointIPs(endpoints map[string]any, podName, podUID, podIP string) ([]string, bool) {
	subsets, _ := endpoints["subsets"].([]any)
	addresses := []string{}
	for _, rawSubset := range subsets {
		subset, _ := rawSubset.(map[string]any)
		notReady, _ := subset["notReadyAddresses"].([]any)
		if len(notReady) != 0 {
			return nil, false
		}
		ready, _ := subset["addresses"].([]any)
		for _, rawAddress := range ready {
			address, _ := rawAddress.(map[string]any)
			if stringValue(address["ip"]) != podIP || stringValue(nested(address, "targetRef", "kind")) != "Pod" ||
				stringValue(nested(address, "targetRef", "name")) != podName || stringValue(nested(address, "targetRef", "uid")) != podUID {
				return nil, false
			}
			addresses = append(addresses, podIP)
		}
	}
	sort.Strings(addresses)
	return addresses, len(addresses) == 1
}

func activationTruthCardinalityFailure(truth WorkspaceActivationTruth, state string) (WorkspaceActivationTruth, error) {
	if state == "multiple" {
		return activationTruthFailure(truth, "multiple_candidate", "ownership_conflict", ErrWorkspaceActivationTruthUnavailable)
	}
	return activationTruthFailure(truth, "absent", "readback_mismatch", ErrWorkspaceActivationTruthUnavailable)
}

func activationTruthProviderFailure(truth WorkspaceActivationTruth, cause error) (WorkspaceActivationTruth, error) {
	class := computeClaimKubectlErrorClass(cause)
	if errors.Is(cause, context.DeadlineExceeded) || errors.Is(cause, context.Canceled) {
		class = "timeout"
	}
	return activationTruthFailure(truth, "provider_unavailable", class, ErrWorkspaceActivationTruthUnavailable)
}

func activationTruthFailure(truth WorkspaceActivationTruth, reason, class string, sentinel error) (WorkspaceActivationTruth, error) {
	truth.Ready = false
	truth.Reason = reason
	truth.ErrorClass = class
	return truth, fmt.Errorf("%w: %s:%s", sentinel, reason, class)
}
