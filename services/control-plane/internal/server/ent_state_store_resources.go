package server

import (
	"context"
	"errors"

	controlplaneent "opl-cloud/services/control-plane/ent"
	"opl-cloud/services/control-plane/ent/computeallocation"
	"opl-cloud/services/control-plane/ent/storageattachment"
	"opl-cloud/services/control-plane/ent/storagevolume"
)

var (
	computeEntFields = []entRecordField{
		textField("AccountID", "SetAccountID", "accountId"),
		textField("OwnerUserID", "SetOwnerUserID", "ownerUserId"),
		textField("WorkspaceID", "SetWorkspaceID", "workspaceId"),
		textField("Name", "SetName", "name"),
		textField("PackageID", "SetPackageID", "packageId"),
		textField("Provider", "SetProvider", "provider"),
		textField("ProviderResourceID", "SetProviderResourceID", "providerResourceId"),
		textField("ProviderRequestID", "SetProviderRequestID", "providerRequestId"),
		textField("OperationID", "SetOperationID", "operationId"),
		textField("Status", "SetStatus", "status"),
		textField("DesiredStatus", "SetDesiredStatus", "desiredStatus"),
		textField("ProviderStatus", "SetProviderStatus", "providerStatus"),
		textField("LastProviderSyncAt", "SetLastProviderSyncAt", "lastProviderSyncAt"),
		textField("LastProviderSyncError", "SetLastProviderSyncError", "lastProviderSyncError"),
		textField("ExternalDeletedAt", "SetExternalDeletedAt", "externalDeletedAt"),
		textField("BillingStatus", "SetBillingStatus", "billingStatus"),
		textField("BillingOperationID", "SetBillingOperationID", "billingOperationId"),
		billingJSONField("BillingStateJSON", "SetBillingStateJSON"),
		textField("PricingVersion", "SetPricingVersion", "pricingVersion"),
		textField("EvidenceID", "SetEvidenceID", "evidenceId"),
		textField("CvmInstanceID", "SetCvmInstanceID", "cvmInstanceId"),
		textField("InstanceID", "SetInstanceID", "instanceId"),
		floatField("CPU", "SetCPU", "cpu"),
		floatField("MemoryGB", "SetMemoryGB", "memoryGb"),
		floatField("DiskGB", "SetDiskGB", "diskGb"),
	}
	storageEntFields = []entRecordField{
		textField("AccountID", "SetAccountID", "accountId"),
		textField("OwnerUserID", "SetOwnerUserID", "ownerUserId"),
		textField("WorkspaceID", "SetWorkspaceID", "workspaceId"),
		textField("Name", "SetName", "name"),
		textField("PackageID", "SetPackageID", "packageId"),
		textField("Provider", "SetProvider", "provider"),
		textField("ProviderResourceID", "SetProviderResourceID", "providerResourceId"),
		textField("ProviderRequestID", "SetProviderRequestID", "providerRequestId"),
		textField("OperationID", "SetOperationID", "operationId"),
		textField("Status", "SetStatus", "status"),
		textField("DesiredStatus", "SetDesiredStatus", "desiredStatus"),
		textField("ProviderStatus", "SetProviderStatus", "providerStatus"),
		textField("LastProviderSyncAt", "SetLastProviderSyncAt", "lastProviderSyncAt"),
		textField("LastProviderSyncError", "SetLastProviderSyncError", "lastProviderSyncError"),
		textField("ExternalDeletedAt", "SetExternalDeletedAt", "externalDeletedAt"),
		textField("BillingStatus", "SetBillingStatus", "billingStatus"),
		textField("BillingOperationID", "SetBillingOperationID", "billingOperationId"),
		billingJSONField("BillingStateJSON", "SetBillingStateJSON"),
		textField("PricingVersion", "SetPricingVersion", "pricingVersion"),
		textField("MountPath", "SetMountPath", "mountPath"),
		floatField("SizeGB", "SetSizeGB", "sizeGb"),
	}
	attachmentEntFields = []entRecordField{
		textField("AccountID", "SetAccountID", "accountId"),
		textField("WorkspaceID", "SetWorkspaceID", "workspaceId"),
		textField("ComputeAllocationID", "SetComputeAllocationID", "computeAllocationId"),
		textField("StorageID", "SetStorageID", "storageId"),
		textField("VolumeID", "SetVolumeID", "volumeId"),
		textField("OperationID", "SetOperationID", "operationId"),
		textField("Provider", "SetProvider", "provider"),
		textField("ProviderRequestID", "SetProviderRequestID", "providerRequestId"),
		textField("Status", "SetStatus", "status"),
		textField("MountPath", "SetMountPath", "mountPath"),
	}
)

func (s *postgresEntStateStore) ListComputes(ctx context.Context, accountID string) ([]map[string]any, error) {
	query := s.client.ComputeAllocation.Query()
	if accountID != "" {
		query.Where(computeallocation.AccountID(accountID))
	}
	rows, err := loadRecordSet(ctx, query.All, computeEntFields)
	if err != nil {
		return nil, err
	}
	return filteredRecords(rows, accountID)
}

func (s *postgresEntStateStore) GetCompute(ctx context.Context, id string) (map[string]any, bool, error) {
	entity, err := s.client.ComputeAllocation.Get(ctx, id)
	if controlplaneent.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return recordFromEnt(entity, computeEntFields), true, nil
}

func (s *postgresEntStateStore) SaveCompute(ctx context.Context, row map[string]any) error {
	return s.saveResourcePreservingAutoRenew(ctx, "compute", row)
}

func (s *postgresEntStateStore) DeleteCompute(ctx context.Context, id string) error {
	err := s.client.ComputeAllocation.DeleteOneID(id).Exec(ctx)
	if controlplaneent.IsNotFound(err) {
		return nil
	}
	return err
}

func (s *postgresEntStateStore) ListStorages(ctx context.Context, accountID string) ([]map[string]any, error) {
	query := s.client.StorageVolume.Query()
	if accountID != "" {
		query.Where(storagevolume.AccountID(accountID))
	}
	rows, err := loadRecordSet(ctx, query.All, storageEntFields)
	if err != nil {
		return nil, err
	}
	return filteredRecords(rows, accountID)
}

func (s *postgresEntStateStore) GetStorage(ctx context.Context, id string) (map[string]any, bool, error) {
	entity, err := s.client.StorageVolume.Get(ctx, id)
	if controlplaneent.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return recordFromEnt(entity, storageEntFields), true, nil
}

func (s *postgresEntStateStore) SaveStorage(ctx context.Context, row map[string]any) error {
	return s.saveResourcePreservingAutoRenew(ctx, "storage", row)
}

func (s *postgresEntStateStore) saveResourcePreservingAutoRenew(ctx context.Context, resourceType string, row map[string]any) error {
	id := stringValue(row["id"])
	if id == "" {
		return errors.New("missing_record_id")
	}
	if resourceType != "compute" && resourceType != "storage" {
		return errors.New("invalid_billing_resource_type")
	}
	for range 4 {
		tx, err := s.client.Tx(ctx)
		if err != nil {
			return err
		}
		rollback := func(err error) error {
			_ = tx.Rollback()
			return err
		}
		client := tx.Client()
		var current map[string]any
		switch resourceType {
		case "compute":
			entity, lockErr := client.ComputeAllocation.UpdateOneID(id).Save(ctx)
			if lockErr == nil {
				current = recordFromEnt(entity, computeEntFields)
			} else if !controlplaneent.IsNotFound(lockErr) {
				return rollback(lockErr)
			}
		case "storage":
			entity, lockErr := client.StorageVolume.UpdateOneID(id).Save(ctx)
			if lockErr == nil {
				current = recordFromEnt(entity, storageEntFields)
			} else if !controlplaneent.IsNotFound(lockErr) {
				return rollback(lockErr)
			}
		}
		if current == nil {
			var createErr error
			if resourceType == "compute" {
				createErr = saveRecord(ctx, id, row, client.ComputeAllocation.Create(), computeEntFields)
			} else {
				createErr = saveRecord(ctx, id, row, client.StorageVolume.Create(), storageEntFields)
			}
			if controlplaneent.IsConstraintError(createErr) {
				_ = tx.Rollback()
				continue
			}
			if createErr != nil {
				return rollback(createErr)
			}
			return tx.Commit()
		}
		if stringValue(current["accountId"]) != stringValue(row["accountId"]) {
			return rollback(errIdempotencyConflict)
		}
		saved := preserveResourceAutoRenew(current, row)
		if resourceType == "compute" {
			builder := client.ComputeAllocation.UpdateOneID(id)
			setRecordFieldsWithEmptyText(builder, saved, computeEntFields, true)
			err = execCreate(ctx, builder)
		} else {
			builder := client.StorageVolume.UpdateOneID(id)
			setRecordFieldsWithEmptyText(builder, saved, storageEntFields, true)
			err = execCreate(ctx, builder)
		}
		if err != nil {
			return rollback(err)
		}
		return tx.Commit()
	}
	return errors.New("resource_save_retry_exhausted")
}

func (s *postgresEntStateStore) DeleteStorage(ctx context.Context, id string) error {
	err := s.client.StorageVolume.DeleteOneID(id).Exec(ctx)
	if controlplaneent.IsNotFound(err) {
		return nil
	}
	return err
}

func (s *postgresEntStateStore) ListAttachments(ctx context.Context, accountID string) ([]map[string]any, error) {
	query := s.client.StorageAttachment.Query()
	if accountID != "" {
		query.Where(storageattachment.AccountID(accountID))
	}
	rows, err := loadRecordSet(ctx, query.All, attachmentEntFields)
	if err != nil {
		return nil, err
	}
	return filteredRecords(rows, accountID)
}

func (s *postgresEntStateStore) GetAttachment(ctx context.Context, id string) (map[string]any, bool, error) {
	entity, err := s.client.StorageAttachment.Get(ctx, id)
	if controlplaneent.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return recordFromEnt(entity, attachmentEntFields), true, nil
}

func (s *postgresEntStateStore) SaveAttachment(ctx context.Context, row map[string]any) error {
	return s.replaceRecord(ctx, row, func(id string) error { return s.client.StorageAttachment.DeleteOneID(id).Exec(ctx) }, func() any { return s.client.StorageAttachment.Create() }, attachmentEntFields)
}

func (s *postgresEntStateStore) DeleteAttachment(ctx context.Context, id string) error {
	err := s.client.StorageAttachment.DeleteOneID(id).Exec(ctx)
	if controlplaneent.IsNotFound(err) {
		return nil
	}
	return err
}
