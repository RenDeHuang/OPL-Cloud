package fabric

import (
	"context"
	"fmt"
	"maps"
	"strings"
)

type monthlyProviderTruthProvider interface {
	MonthlyProviderTruth(context.Context, ComputeAllocation, StorageVolume) (MonthlyProviderTruth, error)
}

func (s *Service) MonthlyPreflightReport(ctx context.Context, input MonthlyPreflightReportInput) (MonthlyPreflightReport, error) {
	provider, ok := s.provider.(monthlyPreflightReportProvider)
	if !ok {
		return MonthlyPreflightReport{}, ErrMonthlyPreflightUnavailable
	}
	report, err := provider.MonthlyPreflightReport(ctx, input)
	if err != nil {
		return MonthlyPreflightReport{}, err
	}
	report.Sub2APIMutationCount = 0
	report.TencentMutationCount = 0
	report.KubernetesMutationCount = 0
	return report, nil
}

func (s *Service) MonthlyProviderTruth(ctx context.Context, computeID, storageID string) (MonthlyProviderTruth, error) {
	computeID, storageID = strings.TrimSpace(computeID), strings.TrimSpace(storageID)
	if computeID == "" || storageID == "" {
		return MonthlyProviderTruth{ComputeState: "unknown", StorageState: "unknown"}, ErrInvalidMonthlyProviderTruth
	}
	s.mu.Lock()
	compute, storage := cloneComputeAllocation(s.computes[computeID]), cloneStorageVolume(s.volumes[storageID])
	s.mu.Unlock()
	unknown := unknownMonthlyProviderTruth(compute, storage)
	if !validMonthlyProviderTruthIdentity(compute, storage) {
		return unknown, fmt.Errorf("%w: local_identity", ErrMonthlyProviderTruthUnavailable)
	}
	provider, ok := s.provider.(monthlyProviderTruthProvider)
	if !ok {
		return unknown, fmt.Errorf("%w: provider_unsupported", ErrMonthlyProviderTruthUnavailable)
	}
	result, err := provider.MonthlyProviderTruth(ctx, compute, storage)
	if err != nil {
		return unknown, fmt.Errorf("%w: %v", ErrMonthlyProviderTruthUnavailable, err)
	}
	if !validMonthlyProviderTruthResult(result, compute, storage) {
		return unknown, fmt.Errorf("%w: provider_identity", ErrMonthlyProviderTruthUnavailable)
	}
	return result, nil
}

func unknownMonthlyProviderTruth(compute ComputeAllocation, storage StorageVolume) MonthlyProviderTruth {
	return MonthlyProviderTruth{ComputeState: "unknown", StorageState: "unknown", Compute: cloneComputeAllocation(compute), Storage: cloneStorageVolume(storage)}
}

func cloneComputeAllocation(value ComputeAllocation) ComputeAllocation {
	value.ProviderData = maps.Clone(value.ProviderData)
	value.CostTags = maps.Clone(value.CostTags)
	value.NodeSelector = maps.Clone(value.NodeSelector)
	return value
}

func cloneStorageVolume(value StorageVolume) StorageVolume {
	value.ProviderData = maps.Clone(value.ProviderData)
	value.CostTags = maps.Clone(value.CostTags)
	return value
}

func validMonthlyProviderTruthIdentity(compute ComputeAllocation, storage StorageVolume) bool {
	instanceID := firstNonEmpty(compute.InstanceID, compute.CVMInstanceID)
	instanceType := firstNonEmpty(compute.InstanceType, compute.ProviderData["instanceType"])
	computeZone := firstNonEmpty(compute.Zone, compute.ProviderData["zone"])
	return compute.ID != "" && compute.AccountID != "" && compute.WorkspaceID != "" &&
		compute.PackageID != "" && compute.Provider == "tencent-tke" && compute.ProviderResourceID != "" &&
		compute.NodePoolID != "" && firstNonEmpty(compute.MachineName, compute.ProviderData["machineName"]) != "" && strings.HasPrefix(instanceID, "ins-") && compute.PrivateIP != "" &&
		instanceType != "" && computeZone != "" && validMonthlyTruthTags(compute.CostTags, compute.AccountID, compute.WorkspaceID, compute.ID) &&
		storage.ID != "" && storage.AccountID == compute.AccountID && storage.WorkspaceID == compute.WorkspaceID && storage.Provider == "tencent-tke" &&
		strings.HasPrefix(storage.ProviderResourceID, "disk-") && storage.SizeGB > 0 && storage.DiskType != "" && storage.Zone == computeZone &&
		validMonthlyTruthTags(storage.CostTags, storage.AccountID, storage.WorkspaceID, storage.ID)
}

func validMonthlyTruthTags(tags map[string]string, accountID, workspaceID, resourceID string) bool {
	return tags["opl_account_id"] == accountID && tags["opl_workspace_id"] == workspaceID && tags["opl_resource_id"] == resourceID && strings.TrimSpace(tags["opl_operation_id"]) != ""
}

func validMonthlyProviderTruthResult(result MonthlyProviderTruth, compute ComputeAllocation, storage StorageVolume) bool {
	validState := func(value string) bool { return value == "ready" || value == "absent" || value == "unknown" }
	return validState(result.ComputeState) && validState(result.StorageState) &&
		result.Compute.ID == compute.ID && result.Compute.AccountID == compute.AccountID && result.Compute.WorkspaceID == compute.WorkspaceID &&
		result.Storage.ID == storage.ID && result.Storage.AccountID == storage.AccountID && result.Storage.WorkspaceID == storage.WorkspaceID &&
		validMonthlyProviderTruthIdentity(result.Compute, result.Storage)
}
