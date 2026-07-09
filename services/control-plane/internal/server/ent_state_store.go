package server

import (
	"context"
	"os"
	"reflect"
	"strconv"
	"time"

	"entgo.io/ent/dialect"
	_ "github.com/lib/pq"

	controlplaneent "opl-cloud/services/control-plane/ent"
	"opl-cloud/services/control-plane/ent/adminauditevent"
	"opl-cloud/services/control-plane/ent/computeallocation"
	"opl-cloud/services/control-plane/ent/ledgerprojection"
	"opl-cloud/services/control-plane/ent/manualtopupprojection"
	"opl-cloud/services/control-plane/ent/runtimeoperation"
	"opl-cloud/services/control-plane/ent/storageattachment"
	"opl-cloud/services/control-plane/ent/storagevolume"
	"opl-cloud/services/control-plane/ent/wallettransactionprojection"
	"opl-cloud/services/control-plane/ent/workspace"
)

const singletonFactID = "default"

type controlPlaneRecord = map[string]any
type controlPlaneRecordSet = map[string]controlPlaneRecord

type StateStore interface {
	Load(ctx context.Context) (controlPlaneState, error)
	Save(ctx context.Context, facts controlPlaneState) error
}

type controlPlaneState struct {
	Version     int                   `json:"version"`
	Computes    controlPlaneRecordSet `json:"computes,omitempty"`
	Storages    controlPlaneRecordSet `json:"storages,omitempty"`
	Attachments controlPlaneRecordSet `json:"attachments,omitempty"`
	Workspaces  controlPlaneRecordSet `json:"workspaces,omitempty"`
	Users       controlPlaneRecordSet `json:"users,omitempty"`
	Sessions    controlPlaneRecordSet `json:"sessions,omitempty"`
	Orgs        controlPlaneRecordSet `json:"orgs,omitempty"`
	Memberships controlPlaneRecordSet `json:"memberships,omitempty"`
	Support     controlPlaneRecordSet `json:"support,omitempty"`
	Wallets     controlPlaneRecordSet `json:"wallets,omitempty"`
	Ledger      []controlPlaneRecord  `json:"ledger,omitempty"`
	WalletTx    []controlPlaneRecord  `json:"walletTx,omitempty"`
	Topups      []controlPlaneRecord  `json:"topups,omitempty"`
	RuntimeOps  []controlPlaneRecord  `json:"runtimeOperations,omitempty"`
	AuditEvents []controlPlaneRecord  `json:"auditEvents,omitempty"`
	Reconcile   controlPlaneRecord    `json:"billingReconciliation,omitempty"`
}

func StateStoreFromEnv() (StateStore, error) {
	if databaseURL := os.Getenv("DATABASE_URL"); databaseURL != "" {
		return NewPostgresEntStateStore(databaseURL)
	}
	return nil, nil
}

type postgresEntStateStore struct {
	client *controlplaneent.Client
}

func NewPostgresEntStateStore(databaseURL string) (StateStore, error) {
	client, err := controlplaneent.Open(dialect.Postgres, databaseURL)
	if err != nil {
		return nil, err
	}
	if err := client.Schema.Create(context.Background()); err != nil {
		_ = client.Close()
		return nil, err
	}
	return &postgresEntStateStore{client: client}, nil
}

type entRecordField struct {
	EntityField string
	Setter      string
	Path        []string
	Kind        string
}

func textField(entityField, setter string, path ...string) entRecordField {
	return entRecordField{EntityField: entityField, Setter: setter, Path: path, Kind: "text"}
}

func intField(entityField, setter string, path ...string) entRecordField {
	return entRecordField{EntityField: entityField, Setter: setter, Path: path, Kind: "int"}
}

func floatField(entityField, setter string, path ...string) entRecordField {
	return entRecordField{EntityField: entityField, Setter: setter, Path: path, Kind: "float"}
}

func boolField(entityField, setter string, path ...string) entRecordField {
	return entRecordField{EntityField: entityField, Setter: setter, Path: path, Kind: "bool"}
}

var (
	accountEntFields = []entRecordField{
		textField("OwnerUserID", "SetOwnerUserID", "ownerUserId"),
		textField("Name", "SetName", "name"),
		textField("Status", "SetStatus", "status"),
	}
	organizationEntFields = []entRecordField{
		textField("BillingAccountID", "SetBillingAccountID", "billingAccountId"),
		textField("Name", "SetName", "name"),
		textField("Status", "SetStatus", "status"),
	}
	userEntFields = []entRecordField{
		textField("AccountID", "SetAccountID", "accountId"),
		textField("Email", "SetEmail", "email"),
		textField("Role", "SetRole", "role"),
		textField("Status", "SetStatus", "status"),
		textField("PasswordHash", "SetPasswordHash", "passwordHash"),
		textField("DisabledAt", "SetDisabledAt", "disabledAt"),
		textField("DisabledBy", "SetDisabledBy", "disabledBy"),
		textField("DisabledReason", "SetDisabledReason", "disabledReason"),
		textField("DeletedAt", "SetDeletedAt", "deletedAt"),
		textField("DeletedBy", "SetDeletedBy", "deletedBy"),
		textField("DeleteReason", "SetDeleteReason", "deleteReason"),
	}
	sessionEntFields = []entRecordField{
		textField("UserID", "SetUserID", "userId"),
		textField("Csrf", "SetCsrf", "csrf"),
		textField("ExpiresAt", "SetExpiresAt", "expiresAt"),
	}
	membershipEntFields = []entRecordField{
		textField("AccountID", "SetAccountID", "accountId"),
		textField("OrganizationID", "SetOrganizationID", "organizationId"),
		textField("UserID", "SetUserID", "userId"),
		textField("Role", "SetRole", "role"),
		textField("Status", "SetStatus", "status"),
	}
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
		textField("BillingStatus", "SetBillingStatus", "billingStatus"),
		textField("HoldID", "SetHoldID", "holdId"),
		textField("HoldReleaseID", "SetHoldReleaseID", "holdReleaseId"),
		textField("SettlementID", "SetSettlementID", "settlementId"),
		textField("LedgerEntryID", "SetLedgerEntryID", "ledgerEntryId"),
		textField("WalletTransactionID", "SetWalletTransactionID", "walletTransactionId"),
		textField("PricingVersion", "SetPricingVersion", "pricingVersion"),
		textField("UsagePeriodEnd", "SetUsagePeriodEnd", "usagePeriodEnd"),
		textField("EvidenceID", "SetEvidenceID", "evidenceId"),
		textField("CvmInstanceID", "SetCvmInstanceID", "cvmInstanceId"),
		textField("InstanceID", "SetInstanceID", "instanceId"),
		textField("NodeName", "SetNodeName", "nodeName"),
		textField("MachineName", "SetMachineName", "machineName"),
		intField("HoldAmountCents", "SetHoldAmountCents", "holdAmountCents"),
		floatField("HoldAmount", "SetHoldAmount", "holdAmount"),
		floatField("CPU", "SetCPU", "cpu"),
		floatField("MemoryGB", "SetMemoryGB", "memoryGb"),
		floatField("DiskGB", "SetDiskGB", "diskGb"),
		textField("PriceSnapshotPackageID", "SetPriceSnapshotPackageID", "priceSnapshot", "packageId"),
		textField("PriceSnapshotResourceType", "SetPriceSnapshotResourceType", "priceSnapshot", "resourceType"),
		textField("PriceSnapshotCurrency", "SetPriceSnapshotCurrency", "priceSnapshot", "currency"),
		textField("PriceSnapshotSource", "SetPriceSnapshotSource", "priceSnapshot", "source"),
		textField("PriceSnapshotSku", "SetPriceSnapshotSku", "priceSnapshot", "sku"),
		intField("PriceSnapshotUnitPriceCents", "SetPriceSnapshotUnitPriceCents", "priceSnapshot", "unitPriceCents"),
		floatField("PriceSnapshotComputeHourly", "SetPriceSnapshotComputeHourly", "priceSnapshot", "computeHourly"),
	}
	storageEntFields = []entRecordField{
		textField("AccountID", "SetAccountID", "accountId"),
		textField("OwnerUserID", "SetOwnerUserID", "ownerUserId"),
		textField("WorkspaceID", "SetWorkspaceID", "workspaceId"),
		textField("Name", "SetName", "name"),
		textField("Provider", "SetProvider", "provider"),
		textField("ProviderResourceID", "SetProviderResourceID", "providerResourceId"),
		textField("ProviderRequestID", "SetProviderRequestID", "providerRequestId"),
		textField("OperationID", "SetOperationID", "operationId"),
		textField("Status", "SetStatus", "status"),
		textField("BillingStatus", "SetBillingStatus", "billingStatus"),
		textField("HoldID", "SetHoldID", "holdId"),
		textField("HoldReleaseID", "SetHoldReleaseID", "holdReleaseId"),
		textField("SettlementID", "SetSettlementID", "settlementId"),
		textField("LedgerEntryID", "SetLedgerEntryID", "ledgerEntryId"),
		textField("WalletTransactionID", "SetWalletTransactionID", "walletTransactionId"),
		textField("PricingVersion", "SetPricingVersion", "pricingVersion"),
		textField("UsagePeriodEnd", "SetUsagePeriodEnd", "usagePeriodEnd"),
		textField("MountPath", "SetMountPath", "mountPath"),
		intField("HoldAmountCents", "SetHoldAmountCents", "holdAmountCents"),
		floatField("HoldAmount", "SetHoldAmount", "holdAmount"),
		floatField("SizeGB", "SetSizeGB", "sizeGb"),
		textField("PriceSnapshotResourceType", "SetPriceSnapshotResourceType", "priceSnapshot", "resourceType"),
		textField("PriceSnapshotCurrency", "SetPriceSnapshotCurrency", "priceSnapshot", "currency"),
		textField("PriceSnapshotSource", "SetPriceSnapshotSource", "priceSnapshot", "source"),
		intField("PriceSnapshotUnitPriceCents", "SetPriceSnapshotUnitPriceCents", "priceSnapshot", "unitPriceCents"),
		floatField("PriceSnapshotStorageGBMonth", "SetPriceSnapshotStorageGBMonth", "priceSnapshot", "storageGbMonth"),
		floatField("PriceSnapshotSizeGB", "SetPriceSnapshotSizeGB", "priceSnapshot", "sizeGb"),
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
	workspaceEntFields = []entRecordField{
		textField("AccountID", "SetAccountID", "accountId"),
		textField("OwnerAccountID", "SetOwnerAccountID", "ownerAccountId"),
		textField("OwnerUserID", "SetOwnerUserID", "ownerUserId"),
		textField("UserID", "SetUserID", "userId"),
		textField("Name", "SetName", "name"),
		textField("URL", "SetURL", "url"),
		textField("State", "SetState", "state"),
		textField("Status", "SetStatus", "status"),
		textField("StorageID", "SetStorageID", "storageId"),
		textField("CurrentComputeAllocationID", "SetCurrentComputeAllocationID", "currentComputeAllocationId"),
		textField("CurrentAttachmentID", "SetCurrentAttachmentID", "currentAttachmentId"),
		textField("RuntimeID", "SetRuntimeID", "runtimeId"),
		textField("RuntimeServiceName", "SetRuntimeServiceName", "runtime", "serviceName"),
		textField("RuntimeServiceNameRoot", "SetRuntimeServiceNameRoot", "runtimeServiceName"),
		textField("ServiceName", "SetServiceName", "serviceName"),
		textField("AccessTokenStatus", "SetAccessTokenStatus", "access", "tokenStatus"),
		textField("AccessAccount", "SetAccessAccount", "access", "account"),
		textField("AccessUsername", "SetAccessUsername", "access", "username"),
		textField("AccessPassword", "SetAccessPassword", "access", "password"),
		textField("CredentialStatus", "SetCredentialStatus", "access", "credentialStatus"),
		textField("CredentialVersion", "SetCredentialVersion", "access", "credentialVersion"),
		textField("CredentialSecretRef", "SetCredentialSecretRef", "access", "secretRef"),
		boolField("AccessRequiresLogin", "SetAccessRequiresLogin", "access", "requiresLogin"),
	}
	walletEntFields = []entRecordField{
		textField("AccountID", "SetAccountID", "accountId"),
		textField("Currency", "SetCurrency", "currency"),
		intField("BalanceCents", "SetBalanceCents", "balanceCents"),
		intField("FrozenCents", "SetFrozenCents", "frozenCents"),
		intField("AvailableCents", "SetAvailableCents", "availableCents"),
		intField("TotalSpentCents", "SetTotalSpentCents", "totalSpentCents"),
		floatField("Balance", "SetBalance", "balance"),
		floatField("Frozen", "SetFrozen", "frozen"),
		floatField("Available", "SetAvailable", "available"),
		floatField("TotalSpent", "SetTotalSpent", "totalSpent"),
		floatField("TotalRecharged", "SetTotalRecharged", "totalRecharged"),
	}
	ledgerEntFields = []entRecordField{
		textField("AccountID", "SetAccountID", "accountId"),
		textField("Type", "SetType", "type"),
		textField("ResourceID", "SetResourceID", "resourceId"),
		textField("ResourceKind", "SetResourceKind", "resourceKind"),
		textField("WorkspaceID", "SetWorkspaceID", "workspaceId"),
		textField("ComputeAllocationID", "SetComputeAllocationID", "computeAllocationId"),
		textField("StorageID", "SetStorageID", "storageId"),
		textField("SettlementID", "SetSettlementID", "settlementId"),
		textField("PricingVersion", "SetPricingVersion", "pricingVersion"),
		textField("UsagePeriodStart", "SetUsagePeriodStart", "usagePeriodStart"),
		textField("UsagePeriodEnd", "SetUsagePeriodEnd", "usagePeriodEnd"),
		textField("Unit", "SetUnit", "unit"),
		textField("ProviderCostEvidenceRef", "SetProviderCostEvidenceRef", "providerCostEvidenceRef"),
		textField("Currency", "SetCurrency", "currency"),
		intField("AmountCents", "SetAmountCents", "amountCents"),
		floatField("Quantity", "SetQuantity", "quantity"),
		textField("Direction", "SetDirection", "direction"),
		textField("PriceSnapshotPackageID", "SetPriceSnapshotPackageID", "priceSnapshot", "packageId"),
		textField("PriceSnapshotResourceType", "SetPriceSnapshotResourceType", "priceSnapshot", "resourceType"),
		textField("PriceSnapshotCurrency", "SetPriceSnapshotCurrency", "priceSnapshot", "currency"),
		textField("PriceSnapshotSource", "SetPriceSnapshotSource", "priceSnapshot", "source"),
		textField("PriceSnapshotSku", "SetPriceSnapshotSku", "priceSnapshot", "sku"),
		intField("PriceSnapshotUnitPriceCents", "SetPriceSnapshotUnitPriceCents", "priceSnapshot", "unitPriceCents"),
		floatField("PriceSnapshotComputeHourly", "SetPriceSnapshotComputeHourly", "priceSnapshot", "computeHourly"),
		floatField("PriceSnapshotStorageGBMonth", "SetPriceSnapshotStorageGBMonth", "priceSnapshot", "storageGbMonth"),
		floatField("PriceSnapshotSizeGB", "SetPriceSnapshotSizeGB", "priceSnapshot", "sizeGb"),
	}
	walletTxEntFields = []entRecordField{
		textField("AccountID", "SetAccountID", "accountId"),
		textField("Type", "SetType", "type"),
		textField("LedgerEntryID", "SetLedgerEntryID", "ledgerEntryId"),
		textField("ResourceID", "SetResourceID", "resourceId"),
		textField("WorkspaceID", "SetWorkspaceID", "workspaceId"),
		textField("ComputeAllocationID", "SetComputeAllocationID", "computeAllocationId"),
		textField("StorageID", "SetStorageID", "storageId"),
		textField("SettlementID", "SetSettlementID", "settlementId"),
		textField("Currency", "SetCurrency", "currency"),
		intField("AmountCents", "SetAmountCents", "amountCents"),
		textField("MetadataWorkspaceID", "SetMetadataWorkspaceID", "metadata", "workspaceId"),
		textField("MetadataResourceID", "SetMetadataResourceID", "metadata", "resourceId"),
		textField("MetadataSettlementID", "SetMetadataSettlementID", "metadata", "settlementId"),
		textField("MetadataLedgerEntryID", "SetMetadataLedgerEntryID", "metadata", "ledgerEntryId"),
		textField("MetadataComputeAllocationID", "SetMetadataComputeAllocationID", "metadata", "computeAllocationId"),
		textField("MetadataStorageID", "SetMetadataStorageID", "metadata", "storageId"),
	}
	topupEntFields = []entRecordField{
		textField("AccountID", "SetAccountID", "accountId"),
		textField("OperatorUserID", "SetOperatorUserID", "operatorUserId"),
		textField("Currency", "SetCurrency", "currency"),
		textField("Source", "SetSource", "source"),
		textField("Reason", "SetReason", "reason"),
		intField("AmountCents", "SetAmountCents", "amountCents"),
	}
	runtimeOpEntFields = []entRecordField{
		textField("OperationID", "SetOperationID", "operationId"),
		textField("AccountID", "SetAccountID", "accountId"),
		textField("WorkspaceID", "SetWorkspaceID", "workspaceId"),
		textField("ResourceID", "SetResourceID", "resourceId"),
		textField("ResourceKind", "SetResourceKind", "resourceKind"),
		textField("Action", "SetAction", "action"),
		textField("Provider", "SetProvider", "provider"),
		textField("ProviderRequestID", "SetProviderRequestID", "providerRequestId"),
		textField("Status", "SetStatus", "status"),
		textField("Result", "SetResult", "result"),
		textField("ComputeAllocationID", "SetComputeAllocationID", "computeAllocationId"),
		textField("StorageID", "SetStorageID", "storageId"),
		textField("AttachmentID", "SetAttachmentID", "attachmentId"),
		textField("RuntimeServiceName", "SetRuntimeServiceName", "runtimeServiceName"),
		textField("CvmInstanceID", "SetCvmInstanceID", "cvmInstanceId"),
		textField("InstanceID", "SetInstanceID", "instanceId"),
		textField("NodeName", "SetNodeName", "nodeName"),
		textField("MachineName", "SetMachineName", "machineName"),
	}
	auditEntFields = []entRecordField{
		textField("ActorUserID", "SetActorUserID", "actorUserId"),
		textField("ActorRole", "SetActorRole", "actorRole"),
		textField("ActorAccountID", "SetActorAccountID", "actorAccountId"),
		textField("TargetAccountID", "SetTargetAccountID", "targetAccountId"),
		textField("Action", "SetAction", "action"),
		textField("ResourceKind", "SetResourceKind", "resourceKind"),
		textField("ResourceID", "SetResourceID", "resourceId"),
		textField("IPAddress", "SetIPAddress", "ipAddress"),
		textField("UserAgent", "SetUserAgent", "userAgent"),
		textField("Result", "SetResult", "result"),
	}
	supportEntFields = []entRecordField{
		textField("AccountID", "SetAccountID", "accountId"),
		textField("UserID", "SetUserID", "userId"),
		textField("WorkspaceID", "SetWorkspaceID", "workspaceId"),
		textField("ResourceID", "SetResourceID", "resourceId"),
		textField("ResourceKind", "SetResourceKind", "resourceKind"),
		textField("Status", "SetStatus", "status"),
		textField("Source", "SetSource", "source"),
		textField("URL", "SetURL", "url"),
		textField("Reason", "SetReason", "reason"),
	}
	reconcileEntFields = []entRecordField{
		textField("Status", "SetStatus", "status"),
		textField("GuardStatus", "SetGuardStatus", "guard", "status"),
		textField("GuardReason", "SetGuardReason", "guard", "reason"),
		textField("MessageAuthor", "SetMessageAuthor", "messageAuthor"),
		textField("MessageText", "SetMessageText", "messageText"),
		textField("MessageCreatedAt", "SetMessageCreatedAt", "messageCreatedAt"),
		boolField("GuardBlockNewWorkspaces", "SetGuardBlockNewWorkspaces", "guard", "blockNewWorkspaces"),
		intField("Reports", "SetReports", "reports"),
	}
)

func (s *postgresEntStateStore) Load(ctx context.Context) (controlPlaneState, error) {
	var facts controlPlaneState
	var err error
	if facts.Computes, err = loadRecordSet(ctx, s.client.ComputeAllocation.Query().All, computeEntFields); err != nil {
		return facts, err
	}
	if facts.Storages, err = loadRecordSet(ctx, s.client.StorageVolume.Query().All, storageEntFields); err != nil {
		return facts, err
	}
	if facts.Attachments, err = loadRecordSet(ctx, s.client.StorageAttachment.Query().All, attachmentEntFields); err != nil {
		return facts, err
	}
	if facts.Workspaces, err = loadRecordSet(ctx, s.client.Workspace.Query().All, workspaceEntFields); err != nil {
		return facts, err
	}
	if facts.Users, err = loadRecordSet(ctx, s.client.User.Query().All, userEntFields); err != nil {
		return facts, err
	}
	if facts.Sessions, err = loadRecordSet(ctx, s.client.Session.Query().All, sessionEntFields); err != nil {
		return facts, err
	}
	if facts.Orgs, err = loadRecordSet(ctx, s.client.Organization.Query().All, organizationEntFields); err != nil {
		return facts, err
	}
	if facts.Memberships, err = loadRecordSet(ctx, s.client.Membership.Query().All, membershipEntFields); err != nil {
		return facts, err
	}
	if facts.Support, err = loadRecordSet(ctx, s.client.SupportTicketMapping.Query().All, supportEntFields); err != nil {
		return facts, err
	}
	if facts.Wallets, err = loadRecordSet(ctx, s.client.WalletProjection.Query().All, walletEntFields); err != nil {
		return facts, err
	}
	if facts.Ledger, err = loadEventRows(ctx, s.client.LedgerProjection.Query().Order(controlplaneent.Asc(ledgerprojection.FieldCreatedAt, ledgerprojection.FieldID)).All, ledgerEntFields); err != nil {
		return facts, err
	}
	if facts.WalletTx, err = loadEventRows(ctx, s.client.WalletTransactionProjection.Query().Order(controlplaneent.Asc(wallettransactionprojection.FieldCreatedAt, wallettransactionprojection.FieldID)).All, walletTxEntFields); err != nil {
		return facts, err
	}
	if facts.Topups, err = loadEventRows(ctx, s.client.ManualTopupProjection.Query().Order(controlplaneent.Asc(manualtopupprojection.FieldCreatedAt, manualtopupprojection.FieldID)).All, topupEntFields); err != nil {
		return facts, err
	}
	if facts.RuntimeOps, err = loadEventRows(ctx, s.client.RuntimeOperation.Query().Order(controlplaneent.Asc(runtimeoperation.FieldCreatedAt, runtimeoperation.FieldID)).All, runtimeOpEntFields); err != nil {
		return facts, err
	}
	if facts.AuditEvents, err = loadEventRows(ctx, s.client.AdminAuditEvent.Query().Order(controlplaneent.Asc(adminauditevent.FieldCreatedAt, adminauditevent.FieldID)).All, auditEntFields); err != nil {
		return facts, err
	}
	row, err := s.client.BillingReconciliation.Get(ctx, singletonFactID)
	if controlplaneent.IsNotFound(err) {
		return facts, nil
	}
	if err != nil {
		return facts, err
	}
	facts.Reconcile = recordFromEnt(row, reconcileEntFields)
	return facts, nil
}

func (s *postgresEntStateStore) SettlementResourceRows(ctx context.Context) (controlPlaneRecordSet, controlPlaneRecordSet, error) {
	computes, err := loadRecordSet(ctx, s.client.ComputeAllocation.Query().All, computeEntFields)
	if err != nil {
		return nil, nil, err
	}
	storages, err := loadRecordSet(ctx, s.client.StorageVolume.Query().All, storageEntFields)
	if err != nil {
		return nil, nil, err
	}
	return computes, storages, nil
}

func (s *postgresEntStateStore) ArchiveTerminalResources(ctx context.Context, reason string) (map[string]any, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()
	result := map[string]any{
		"computeArchived":    0,
		"storageArchived":    0,
		"attachmentArchived": 0,
		"workspaceArchived":  0,
	}

	computes, err := tx.ComputeAllocation.Query().All(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range computes {
		if !terminalComputeStatus(row.Status) {
			continue
		}
		if err := saveArchivedResource(ctx, tx.ArchivedComputeAllocation.Create(), "compute", row.ID, row.AccountID, row.WorkspaceID, row.Name, row.Status, reason, now); err != nil {
			return nil, err
		}
		if _, err := tx.ComputeAllocation.Delete().Where(computeallocation.ID(row.ID)).Exec(ctx); err != nil {
			return nil, err
		}
		result["computeArchived"] = result["computeArchived"].(int) + 1
	}

	storages, err := tx.StorageVolume.Query().All(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range storages {
		if !terminalStorageStatus(row.Status) {
			continue
		}
		if err := saveArchivedResource(ctx, tx.ArchivedStorageVolume.Create(), "storage", row.ID, row.AccountID, row.WorkspaceID, row.Name, row.Status, reason, now); err != nil {
			return nil, err
		}
		if _, err := tx.StorageVolume.Delete().Where(storagevolume.ID(row.ID)).Exec(ctx); err != nil {
			return nil, err
		}
		result["storageArchived"] = result["storageArchived"].(int) + 1
	}

	attachments, err := tx.StorageAttachment.Query().All(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range attachments {
		if !terminalAttachmentStatus(row.Status) {
			continue
		}
		if err := saveArchivedResource(ctx, tx.ArchivedStorageAttachment.Create(), "attachment", row.ID, row.AccountID, row.WorkspaceID, row.ID, row.Status, reason, now); err != nil {
			return nil, err
		}
		if _, err := tx.StorageAttachment.Delete().Where(storageattachment.ID(row.ID)).Exec(ctx); err != nil {
			return nil, err
		}
		result["attachmentArchived"] = result["attachmentArchived"].(int) + 1
	}

	workspaces, err := tx.Workspace.Query().All(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range workspaces {
		if !terminalWorkspaceStatus(firstNonEmpty(row.State, row.Status)) {
			continue
		}
		if err := saveArchivedResource(ctx, tx.ArchivedWorkspace.Create(), "workspace", row.ID, firstNonEmpty(row.OwnerAccountID, row.AccountID), row.ID, row.Name, firstNonEmpty(row.State, row.Status), reason, now); err != nil {
			return nil, err
		}
		if _, err := tx.Workspace.Delete().Where(workspace.ID(row.ID)).Exec(ctx); err != nil {
			return nil, err
		}
		result["workspaceArchived"] = result["workspaceArchived"].(int) + 1
	}

	total := result["computeArchived"].(int) + result["storageArchived"].(int) + result["attachmentArchived"].(int) + result["workspaceArchived"].(int)
	if total > 0 {
		if err := tx.ArchiveJob.Create().
			SetID("archive-job-" + stableID(now.Format(time.RFC3339Nano), reason)[:12]).
			SetResourceKind("terminal_control_plane_resources").
			SetStatus("succeeded").
			SetReason(reason).
			SetAmountCents(int64(total)).
			SetCreatedAt(now).
			SetUpdatedAt(now).
			Exec(ctx); err != nil {
			return nil, err
		}
	}
	result["archived"] = total
	result["reason"] = reason
	return result, tx.Commit()
}

func (s *postgresEntStateStore) Save(ctx context.Context, facts controlPlaneState) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ComputeAllocation.Delete().Exec(ctx); err != nil {
		return err
	}
	if err := saveRecordSet(ctx, facts.Computes, func() any { return tx.ComputeAllocation.Create() }, computeEntFields); err != nil {
		return err
	}
	if _, err := tx.StorageVolume.Delete().Exec(ctx); err != nil {
		return err
	}
	if err := saveRecordSet(ctx, facts.Storages, func() any { return tx.StorageVolume.Create() }, storageEntFields); err != nil {
		return err
	}
	if _, err := tx.StorageAttachment.Delete().Exec(ctx); err != nil {
		return err
	}
	if err := saveRecordSet(ctx, facts.Attachments, func() any { return tx.StorageAttachment.Create() }, attachmentEntFields); err != nil {
		return err
	}
	if _, err := tx.Workspace.Delete().Exec(ctx); err != nil {
		return err
	}
	if err := saveRecordSet(ctx, facts.Workspaces, func() any { return tx.Workspace.Create() }, workspaceEntFields); err != nil {
		return err
	}
	if _, err := tx.User.Delete().Exec(ctx); err != nil {
		return err
	}
	if err := saveRecordSet(ctx, facts.Users, func() any { return tx.User.Create() }, userEntFields); err != nil {
		return err
	}
	if _, err := tx.Session.Delete().Exec(ctx); err != nil {
		return err
	}
	if err := saveRecordSet(ctx, facts.Sessions, func() any { return tx.Session.Create() }, sessionEntFields); err != nil {
		return err
	}
	if _, err := tx.Organization.Delete().Exec(ctx); err != nil {
		return err
	}
	if err := saveRecordSet(ctx, facts.Orgs, func() any { return tx.Organization.Create() }, organizationEntFields); err != nil {
		return err
	}
	if _, err := tx.Membership.Delete().Exec(ctx); err != nil {
		return err
	}
	if err := saveRecordSet(ctx, facts.Memberships, func() any { return tx.Membership.Create() }, membershipEntFields); err != nil {
		return err
	}
	if _, err := tx.SupportTicketMapping.Delete().Exec(ctx); err != nil {
		return err
	}
	if err := saveRecordSet(ctx, facts.Support, func() any { return tx.SupportTicketMapping.Create() }, supportEntFields); err != nil {
		return err
	}
	if _, err := tx.WalletProjection.Delete().Exec(ctx); err != nil {
		return err
	}
	if err := saveRecordSet(ctx, facts.Wallets, func() any { return tx.WalletProjection.Create() }, walletEntFields); err != nil {
		return err
	}
	if _, err := tx.LedgerProjection.Delete().Exec(ctx); err != nil {
		return err
	}
	if err := saveEventRows(ctx, facts.Ledger, func() any { return tx.LedgerProjection.Create() }, ledgerEntFields, "ledger"); err != nil {
		return err
	}
	if _, err := tx.WalletTransactionProjection.Delete().Exec(ctx); err != nil {
		return err
	}
	if err := saveEventRows(ctx, facts.WalletTx, func() any { return tx.WalletTransactionProjection.Create() }, walletTxEntFields, "wallet_tx"); err != nil {
		return err
	}
	if _, err := tx.ManualTopupProjection.Delete().Exec(ctx); err != nil {
		return err
	}
	if err := saveEventRows(ctx, facts.Topups, func() any { return tx.ManualTopupProjection.Create() }, topupEntFields, "topup"); err != nil {
		return err
	}
	if _, err := tx.RuntimeOperation.Delete().Exec(ctx); err != nil {
		return err
	}
	if err := saveEventRows(ctx, facts.RuntimeOps, func() any { return tx.RuntimeOperation.Create() }, runtimeOpEntFields, "runtime_op"); err != nil {
		return err
	}
	if _, err := tx.AdminAuditEvent.Delete().Exec(ctx); err != nil {
		return err
	}
	if err := saveEventRows(ctx, facts.AuditEvents, func() any { return tx.AdminAuditEvent.Create() }, auditEntFields, "audit"); err != nil {
		return err
	}
	if _, err := tx.BillingReconciliation.Delete().Exec(ctx); err != nil {
		return err
	}
	if facts.Reconcile != nil {
		if err := saveRecord(ctx, singletonFactID, facts.Reconcile, tx.BillingReconciliation.Create(), reconcileEntFields); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func loadRecordSet[T any](ctx context.Context, all func(context.Context) ([]*T, error), fields []entRecordField) (controlPlaneRecordSet, error) {
	rows, err := all(ctx)
	if err != nil {
		return nil, err
	}
	out := controlPlaneRecordSet{}
	for _, row := range rows {
		record := recordFromEnt(row, fields)
		out[stringValue(record["id"])] = record
	}
	return out, nil
}

func loadEventRows[T any](ctx context.Context, all func(context.Context) ([]*T, error), fields []entRecordField) ([]controlPlaneRecord, error) {
	rows, err := all(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]controlPlaneRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, recordFromEnt(row, fields))
	}
	return out, nil
}

func recordFromEnt(entity any, fields []entRecordField) controlPlaneRecord {
	value := reflect.Indirect(reflect.ValueOf(entity))
	row := controlPlaneRecord{"id": stringValue(fieldValue(value, "ID"))}
	if createdAt, ok := fieldValue(value, "CreatedAt").(time.Time); ok && !createdAt.IsZero() {
		row["createdAt"] = createdAt.UTC().Format(time.RFC3339Nano)
	}
	for _, field := range fields {
		raw := fieldValue(value, field.EntityField)
		if isZero(raw) {
			continue
		}
		setPath(row, field.Path, raw)
	}
	return row
}

func saveRecordSet(ctx context.Context, rows controlPlaneRecordSet, create func() any, fields []entRecordField) error {
	for id, row := range rows {
		if err := saveRecord(ctx, firstNonEmpty(stringValue(row["id"]), id), row, create(), fields); err != nil {
			return err
		}
	}
	return nil
}

func saveEventRows(ctx context.Context, rows []controlPlaneRecord, create func() any, fields []entRecordField, prefix string) error {
	for index, row := range rows {
		id := firstNonEmpty(stringValue(row["id"]), prefix+"-"+stableID(stringValue(row["accountId"]), stringValue(row["createdAt"]), stringValue(row["type"]), strconv.Itoa(index))[:12])
		if err := saveRecord(ctx, id, row, create(), fields); err != nil {
			return err
		}
	}
	return nil
}

func saveRecord(ctx context.Context, id string, row controlPlaneRecord, builder any, fields []entRecordField) error {
	callSetter(builder, "SetID", id)
	if createdAt, ok := parseRecordTime(row); ok {
		callSetter(builder, "SetCreatedAt", createdAt)
		callSetter(builder, "SetUpdatedAt", createdAt)
	}
	for _, field := range fields {
		if field.Setter == "" {
			continue
		}
		value, ok := valueAtPath(row, field.Path)
		if !ok {
			continue
		}
		switch field.Kind {
		case "int":
			callSetter(builder, field.Setter, int64(numberValue(value)))
		case "float":
			callSetter(builder, field.Setter, numberValue(value))
		case "bool":
			callSetter(builder, field.Setter, boolValue(value))
		default:
			text := stringValue(value)
			if text != "" {
				callSetter(builder, field.Setter, text)
			}
		}
	}
	return execCreate(ctx, builder)
}

func saveArchivedResource(ctx context.Context, builder any, kind string, id string, accountID string, workspaceID string, name string, status string, reason string, archivedAt time.Time) error {
	callSetter(builder, "SetID", "archived-"+kind+"-"+id)
	callSetter(builder, "SetAccountID", accountID)
	callSetter(builder, "SetWorkspaceID", workspaceID)
	callSetter(builder, "SetResourceID", id)
	callSetter(builder, "SetResourceKind", kind)
	callSetter(builder, "SetName", name)
	callSetter(builder, "SetStatus", status)
	callSetter(builder, "SetReason", reason)
	callSetter(builder, "SetArchivedAt", archivedAt)
	callSetter(builder, "SetCreatedAt", archivedAt)
	callSetter(builder, "SetUpdatedAt", archivedAt)
	err := execCreate(ctx, builder)
	if controlplaneent.IsConstraintError(err) {
		return nil
	}
	return err
}

func callSetter(builder any, name string, value any) {
	method := reflect.ValueOf(builder).MethodByName(name)
	if !method.IsValid() {
		return
	}
	method.Call([]reflect.Value{reflect.ValueOf(value)})
}

func execCreate(ctx context.Context, builder any) error {
	results := reflect.ValueOf(builder).MethodByName("Exec").Call([]reflect.Value{reflect.ValueOf(ctx)})
	if len(results) == 0 || results[0].IsNil() {
		return nil
	}
	return results[0].Interface().(error)
}

func fieldValue(value reflect.Value, name string) any {
	field := value.FieldByName(name)
	if !field.IsValid() || !field.CanInterface() {
		return nil
	}
	return field.Interface()
}

func isZero(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return typed == ""
	case int64:
		return typed == 0
	case float64:
		return typed == 0
	case bool:
		return !typed
	case time.Time:
		return typed.IsZero()
	default:
		return reflect.ValueOf(value).IsZero()
	}
}

func parseRecordTime(row controlPlaneRecord) (time.Time, bool) {
	text := stringValue(row["createdAt"])
	if text == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999 -0700 MST"} {
		parsed, err := time.Parse(layout, text)
		if err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

func valueAtPath(row controlPlaneRecord, path []string) (any, bool) {
	var current any = row
	for _, part := range path {
		asMap, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = asMap[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func setPath(row controlPlaneRecord, path []string, value any) {
	if len(path) == 0 {
		return
	}
	current := row
	for _, part := range path[:len(path)-1] {
		next, _ := current[part].(map[string]any)
		if next == nil {
			next = map[string]any{}
			current[part] = next
		}
		current = next
	}
	current[path[len(path)-1]] = value
}

func numberValue(value any) float64 {
	switch typed := value.(type) {
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case float64:
		return typed
	case float32:
		return float64(typed)
	default:
		parsed, _ := strconv.ParseFloat(stringValue(value), 64)
		return parsed
	}
}

func boolValue(value any) bool {
	if parsed, ok := value.(bool); ok {
		return parsed
	}
	parsed, _ := strconv.ParseBool(stringValue(value))
	return parsed
}
