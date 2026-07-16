package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/lib/pq"

	controlplaneent "opl-cloud/services/control-plane/ent"
	"opl-cloud/services/control-plane/ent/account"
	"opl-cloud/services/control-plane/ent/adminauditevent"
	"opl-cloud/services/control-plane/ent/billingreconciliation"
	"opl-cloud/services/control-plane/ent/computeallocation"
	"opl-cloud/services/control-plane/ent/productione2erecord"
	"opl-cloud/services/control-plane/ent/runtimeoperation"
	"opl-cloud/services/control-plane/ent/storageattachment"
	"opl-cloud/services/control-plane/ent/storagevolume"
	"opl-cloud/services/control-plane/ent/supportticketmapping"
	"opl-cloud/services/control-plane/ent/workspace"
	"opl-cloud/services/control-plane/ent/workspacebackup"
	"opl-cloud/services/control-plane/ent/workspacesyncevent"
	controlplanemigrations "opl-cloud/services/control-plane/migrations"
	"opl-cloud/services/internal/postgresmigrate"
)

const (
	singletonFactID                  = "default"
	controlPlaneMaxOpenDBConnections = 20
)

var errIdempotencyConflict = errors.New("idempotency_conflict")

type controlPlaneRecord = map[string]any
type controlPlaneRecordSet = map[string]controlPlaneRecord

type StateStore interface {
	controlPlaneTableStore
}

func StateStoreFromEnv() (StateStore, error) {
	if databaseURL := os.Getenv("DATABASE_URL"); databaseURL != "" {
		return NewPostgresEntStateStore(databaseURL)
	}
	return nil, errors.New("DATABASE_URL is required for control-plane persistence")
}

type postgresEntStateStore struct {
	client *controlplaneent.Client
}

func NewPostgresEntStateStore(databaseURL string) (StateStore, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, err
	}
	// ponytail: fixed for the single Pod; revisit only with measured DB and replica capacity.
	db.SetMaxOpenConns(controlPlaneMaxOpenDBConnections)
	driver := entsql.OpenDB(dialect.Postgres, db)
	client := controlplaneent.NewClient(controlplaneent.Driver(driver))
	ctx := context.Background()
	if err := postgresmigrate.Apply(ctx, db, "control-plane", []postgresmigrate.Migration{
		{Version: "202607140001_sub2api_monthly_hard_cut", Run: func(ctx context.Context) error {
			return controlplanemigrations.Apply(ctx, driver)
		}},
		{Version: "202607150001_legacy_membership_normalize", Run: func(ctx context.Context) error {
			return validateAndNormalizeLegacyMemberships(ctx, driver)
		}},
		{Version: "202607150002_pre_schema_backfill", Run: func(ctx context.Context) error {
			return backfillControlPlaneMigrationNulls(ctx, driver)
		}},
		{Version: "202607150003_ent_schema", Run: func(ctx context.Context) error {
			return client.Schema.Create(ctx)
		}},
		{Version: "202607150004_post_schema_backfill", Run: func(ctx context.Context) error {
			return backfillControlPlaneMigrationNulls(ctx, driver)
		}},
		{Version: "202607160001_sub2api_user_unique", Run: func(ctx context.Context) error {
			return controlplanemigrations.ApplySub2APIUserUniqueness(ctx, driver)
		}},
		{Version: "202607160002_primary_workspace", Run: func(ctx context.Context) error {
			return controlplanemigrations.ApplyPrimaryWorkspace(ctx, driver)
		}},
	}); err != nil {
		_ = client.Close()
		return nil, err
	}
	return &postgresEntStateStore{client: client}, nil
}

func validateAndNormalizeLegacyMemberships(ctx context.Context, driver dialect.Driver) error {
	const query = `
DO $$
BEGIN
	IF to_regclass('control_plane_memberships') IS NULL THEN
		RETURN;
	END IF;
	IF NOT EXISTS (SELECT 1 FROM control_plane_memberships) THEN
    RETURN;
  END IF;
  IF to_regclass('control_plane_accounts') IS NULL
    OR to_regclass('control_plane_organizations') IS NULL
    OR to_regclass('control_plane_users') IS NULL
  THEN
    RAISE EXCEPTION 'legacy membership truth tables are missing';
  END IF;
  IF EXISTS (
    SELECT 1
    FROM control_plane_memberships memberships
    LEFT JOIN control_plane_accounts accounts ON accounts.id = memberships.account_id
    LEFT JOIN control_plane_organizations organizations ON organizations.id = memberships.organization_id
    LEFT JOIN control_plane_users users ON users.id = memberships.user_id
	WHERE memberships.role IS NULL
	  OR btrim(memberships.role) = ''
	  OR lower(btrim(memberships.role)) NOT IN ('owner', 'admin', 'member')
      OR accounts.id IS NULL
      OR organizations.id IS NULL
      OR users.id IS NULL
      OR organizations.billing_account_id <> memberships.account_id
      OR users.account_id <> memberships.account_id
  ) THEN
    RAISE EXCEPTION 'legacy membership cannot be mapped without guessing';
  END IF;
  UPDATE control_plane_memberships SET role = lower(btrim(role));
END $$;`
	tx, err := driver.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin legacy membership migration: %w", err)
	}
	if err := tx.Exec(ctx, query, []any{}, nil); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("validate legacy memberships: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit legacy membership migration: %w", err)
	}
	return nil
}

func backfillControlPlaneMigrationNulls(ctx context.Context, driver dialect.Driver) error {
	const query = `
DO $$
DECLARE
  target_schema text;
  target_table text;
  target_column text;
  target_type text;
BEGIN
  FOR target_schema, target_table IN
    SELECT table_schema, table_name
    FROM information_schema.tables
    WHERE table_schema = 'public'
      AND table_name LIKE 'control_plane_%'
      AND table_type = 'BASE TABLE'
  LOOP
    EXECUTE format('ALTER TABLE %I.%I ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ', target_schema, target_table);
    EXECUTE format('ALTER TABLE %I.%I ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ', target_schema, target_table);
    EXECUTE format(
      'UPDATE %I.%I SET created_at = COALESCE(created_at, NOW()), updated_at = COALESCE(updated_at, created_at, NOW()) WHERE created_at IS NULL OR updated_at IS NULL',
      target_schema,
      target_table
    );
  END LOOP;

  IF to_regclass('public.control_plane_storage_attachments') IS NOT NULL
    AND EXISTS (
      SELECT 1 FROM information_schema.columns
      WHERE table_schema = 'public' AND table_name = 'control_plane_storage_attachments' AND column_name = 'account_id'
    )
  THEN
    IF to_regclass('public.control_plane_workspaces') IS NOT NULL
      AND EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'control_plane_storage_attachments' AND column_name = 'workspace_id'
      )
      AND EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'control_plane_workspaces' AND column_name = 'account_id'
      )
    THEN
      UPDATE control_plane_storage_attachments attachments
      SET account_id = workspaces.account_id
      FROM control_plane_workspaces workspaces
      WHERE COALESCE(attachments.account_id, '') = ''
        AND COALESCE(attachments.workspace_id, '') <> ''
        AND attachments.workspace_id = workspaces.id
        AND COALESCE(workspaces.account_id, '') <> '';
    END IF;

    IF to_regclass('public.control_plane_storage_volumes') IS NOT NULL
      AND EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'control_plane_storage_attachments' AND column_name = 'storage_id'
      )
      AND EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'control_plane_storage_volumes' AND column_name = 'account_id'
      )
    THEN
      UPDATE control_plane_storage_attachments attachments
      SET account_id = volumes.account_id
      FROM control_plane_storage_volumes volumes
      WHERE COALESCE(attachments.account_id, '') = ''
        AND COALESCE(attachments.storage_id, '') <> ''
        AND attachments.storage_id = volumes.id
        AND COALESCE(volumes.account_id, '') <> '';
    END IF;

    IF to_regclass('public.control_plane_storage_volumes') IS NOT NULL
      AND EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'control_plane_storage_attachments' AND column_name = 'volume_id'
      )
      AND EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'control_plane_storage_volumes' AND column_name = 'account_id'
      )
    THEN
      UPDATE control_plane_storage_attachments attachments
      SET account_id = volumes.account_id
      FROM control_plane_storage_volumes volumes
      WHERE COALESCE(attachments.account_id, '') = ''
        AND COALESCE(attachments.volume_id, '') <> ''
        AND attachments.volume_id = volumes.id
        AND COALESCE(volumes.account_id, '') <> '';
    END IF;

    IF to_regclass('public.control_plane_compute_allocations') IS NOT NULL
      AND EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'control_plane_storage_attachments' AND column_name = 'compute_allocation_id'
      )
      AND EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'control_plane_compute_allocations' AND column_name = 'account_id'
      )
    THEN
      UPDATE control_plane_storage_attachments attachments
      SET account_id = computes.account_id
      FROM control_plane_compute_allocations computes
      WHERE COALESCE(attachments.account_id, '') = ''
        AND COALESCE(attachments.compute_allocation_id, '') <> ''
        AND attachments.compute_allocation_id = computes.id
        AND COALESCE(computes.account_id, '') <> '';
    END IF;
  END IF;

  FOR target_schema, target_table, target_column, target_type IN
    SELECT c.table_schema, c.table_name, c.column_name, c.data_type
    FROM information_schema.columns c
    JOIN information_schema.tables t
      ON t.table_schema = c.table_schema
      AND t.table_name = c.table_name
      AND t.table_type = 'BASE TABLE'
    WHERE c.table_schema = 'public'
      AND c.table_name LIKE 'control_plane_%'
      AND c.column_name NOT IN ('id', 'created_at', 'updated_at')
      AND c.data_type IN ('text', 'character varying', 'character', 'boolean', 'bigint', 'integer', 'double precision', 'numeric', 'real')
  LOOP
    IF target_type IN ('text', 'character varying', 'character') THEN
      EXECUTE format('UPDATE %I.%I SET %I = '''' WHERE %I IS NULL', target_schema, target_table, target_column, target_column);
    ELSIF target_type = 'boolean' THEN
      EXECUTE format('UPDATE %I.%I SET %I = false WHERE %I IS NULL', target_schema, target_table, target_column, target_column);
    ELSE
      EXECUTE format('UPDATE %I.%I SET %I = 0 WHERE %I IS NULL', target_schema, target_table, target_column, target_column);
    END IF;
  END LOOP;
END $$;`
	if err := driver.Exec(ctx, query, []any{}, nil); err != nil {
		return fmt.Errorf("backfill control-plane migration nulls: %w", err)
	}
	return nil
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

func billingJSONField(entityField, setter string) entRecordField {
	return entRecordField{EntityField: entityField, Setter: setter, Kind: "billing_json"}
}

// ponytail: keep low-cardinality monthly state in one JSON column at the current
// scale; promote fields to indexed columns only when renewal scans become measurable.
var monthlyBillingStateKeys = []string{
	"resourceType",
	"billingOperationStartedAt",
	"sub2apiRedeemCode",
	"sub2apiRefundCode",
	"monthlyPriceCnyCents",
	"chargeUsdMicros",
	"billingAnchorDay",
	"periodStart",
	"paidThrough",
	"autoRenew",
	"lastRenewalAttemptAt",
	"lastBillingError",
	"lastReceiptId",
	"postChargeBalanceUsdMicros",
	"postChargeBalanceKnown",
	"computeAllocationId",
	"zone",
	"chargeType",
	"renewFlag",
	"deadline",
	"cbsStatus",
	"diskType",
	"providerData",
	"costTags",
	"nodePoolId",
	"instanceType",
	"requestedPeriodMonths",
	"periodMonths",
	"verificationSlotId",
	"customerProduct",
	"pvName",
	"persistentVolumeName",
}

var (
	accountEntFields = []entRecordField{
		textField("OwnerUserID", "SetOwnerUserID", "ownerUserId"),
		intField("Sub2apiUserID", "SetSub2apiUserID", "sub2apiUserId"),
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
		textField("NodeName", "SetNodeName", "nodeName"),
		textField("MachineName", "SetMachineName", "machineName"),
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
		textField("AccessAccount", "SetAccessAccount", "access", "account"),
		textField("AccessUsername", "SetAccessUsername", "access", "username"),
		textField("CredentialStatus", "SetCredentialStatus", "access", "credentialStatus"),
		textField("CredentialVersion", "SetCredentialVersion", "access", "credentialVersion"),
		textField("CredentialSecretRef", "SetCredentialSecretRef", "access", "secretRef"),
		textField("VerificationSlotID", "SetVerificationSlotID", "verificationSlotId"),
		boolField("CustomerProduct", "SetCustomerProduct", "customerProduct"),
	}
	workspaceBackupEntFields = []entRecordField{
		textField("AccountID", "SetAccountID", "accountId"),
		textField("WorkspaceID", "SetWorkspaceID", "workspaceId"),
		textField("StorageID", "SetStorageID", "storageId"),
		textField("SnapshotID", "SetSnapshotID", "snapshotId"),
		textField("Status", "SetStatus", "status"),
		textField("IdempotencyKey", "SetIdempotencyKey", "idempotencyKey"),
		textField("RequestHash", "SetRequestHash", "requestHash"),
		textField("ManifestJSON", "SetManifestJSON", "manifestJson"),
		textField("RestoredStorageID", "SetRestoredStorageID", "restoredStorageId"),
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
	projectTaskSyncHeadEntFields = []entRecordField{
		textField("Kind", "SetKind", "kind"),
		textField("OrganizationID", "SetOrganizationID", "organizationId"),
		textField("WorkspaceID", "SetWorkspaceID", "workspaceId"),
		textField("ProjectID", "SetProjectID", "projectId"),
		textField("LocalAliasID", "SetLocalAliasID", "localAliasId"),
		intField("Version", "SetVersion", "version"),
		textField("Status", "SetStatus", "status"),
	}
	executionRequestEntFields = []entRecordField{
		textField("OrganizationID", "SetOrganizationID", "organizationId"),
		textField("WorkspaceID", "SetWorkspaceID", "workspaceId"),
		textField("ProjectID", "SetProjectID", "projectId"),
		textField("TaskID", "SetTaskID", "taskId"),
		textField("ActorUserID", "SetActorUserID", "actorUserId"),
		textField("ApprovalID", "SetApprovalID", "approvalId"),
		textField("ApprovalStatus", "SetApprovalStatus", "approvalStatus"),
		textField("ApprovedBy", "SetApprovedBy", "approvedBy"),
		textField("ApprovedAt", "SetApprovedAt", "approvedAt"),
		textField("Status", "SetStatus", "status"),
		textField("EnvironmentRef", "SetEnvironmentRef", "environmentRef"),
		textField("JobID", "SetJobID", "jobId"),
		textField("ReceiptID", "SetReceiptID", "receiptId"),
		textField("ContinuationID", "SetContinuationID", "continuationId"),
		textField("IdempotencyKey", "SetIdempotencyKey", "idempotencyKey"),
		intField("Version", "SetVersion", "version"),
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
		textField("ExternalSystem", "SetExternalSystem", "externalSystem"),
		textField("ExternalTicketID", "SetExternalTicketID", "externalTicketId"),
		textField("ExternalURL", "SetExternalURL", "externalUrl"),
		textField("OperationID", "SetOperationID", "operationId"),
		textField("ResourceID", "SetResourceID", "resourceId"),
		textField("ResourceKind", "SetResourceKind", "resourceKind"),
		textField("Title", "SetTitle", "title"),
		textField("Category", "SetCategory", "category"),
		textField("Priority", "SetPriority", "priority"),
		textField("Status", "SetStatus", "status"),
		textField("Source", "SetSource", "source"),
		textField("URL", "SetURL", "url"),
		textField("Reason", "SetReason", "reason"),
	}
	productionE2EEntFields = []entRecordField{
		textField("AccountID", "SetAccountID", "accountId"),
		textField("WorkspaceID", "SetWorkspaceID", "workspaceId"),
		textField("Status", "SetStatus", "status"),
		textField("Result", "SetResult", "result"),
		textField("Reason", "SetReason", "reason"),
		textField("URL", "SetURL", "url"),
	}
	archiveJobEntFields = []entRecordField{
		textField("ResourceKind", "SetResourceKind", "resourceKind"),
		textField("Status", "SetStatus", "status"),
		textField("Reason", "SetReason", "reason"),
		intField("AmountCents", "SetAmountCents", "amountCents"),
	}
	archivedResourceEntFields = []entRecordField{
		textField("AccountID", "SetAccountID", "accountId"),
		textField("WorkspaceID", "SetWorkspaceID", "workspaceId"),
		textField("ResourceID", "SetResourceID", "resourceId"),
		textField("ResourceKind", "SetResourceKind", "resourceKind"),
		textField("Name", "SetName", "name"),
		textField("Status", "SetStatus", "status"),
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

func (s *postgresEntStateStore) ListUsers(ctx context.Context, includeDeleted bool) ([]map[string]any, error) {
	rows, err := loadRecordSet(ctx, s.client.User.Query().All, userEntFields)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if !includeDeleted && stringValue(row["status"]) == "deleted" {
			continue
		}
		out = append(out, cloneMap(row))
	}
	return out, nil
}

func (s *postgresEntStateStore) SaveUser(ctx context.Context, row map[string]any) error {
	if !validRole(stringValue(row["role"])) {
		return errInvalidRole
	}
	return s.replaceRecord(ctx, row, func(id string) error { return s.client.User.DeleteOneID(id).Exec(ctx) }, func() any { return s.client.User.Create() }, userEntFields)
}

func (s *postgresEntStateStore) DeleteUser(ctx context.Context, id string) error {
	err := s.client.User.DeleteOneID(id).Exec(ctx)
	if controlplaneent.IsNotFound(err) {
		return nil
	}
	return err
}

func (s *postgresEntStateStore) ListSessions(ctx context.Context) (controlPlaneRecordSet, error) {
	return loadRecordSet(ctx, s.client.Session.Query().All, sessionEntFields)
}

func (s *postgresEntStateStore) SaveSession(ctx context.Context, row map[string]any) error {
	return s.replaceRecord(ctx, row, func(id string) error { return s.client.Session.DeleteOneID(id).Exec(ctx) }, func() any { return s.client.Session.Create() }, sessionEntFields)
}

func (s *postgresEntStateStore) DeleteSession(ctx context.Context, id string) error {
	err := s.client.Session.DeleteOneID(id).Exec(ctx)
	if controlplaneent.IsNotFound(err) {
		return nil
	}
	return err
}

func (s *postgresEntStateStore) ListAccounts(ctx context.Context, accountID string) ([]map[string]any, error) {
	query := s.client.Account.Query()
	if accountID != "" {
		query.Where(account.ID(accountID))
	}
	rows, err := loadRecordSet(ctx, query.All, accountEntFields)
	if err != nil {
		return nil, err
	}
	return filteredRecords(rows, "")
}

func (s *postgresEntStateStore) SaveAccount(ctx context.Context, row map[string]any) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	rollback := func(err error) error {
		_ = tx.Rollback()
		if int64(numberField(row, "sub2apiUserId", 0)) > 0 && controlplaneent.IsConstraintError(err) {
			return errSub2APIAccountMappingConflict
		}
		return err
	}
	accounts, err := loadRecordSet(ctx, tx.Account.Query().All, accountEntFields)
	if err != nil {
		return rollback(err)
	}
	accountRows, _ := filteredRecords(accounts, "")
	if err := validateSub2APIAccountMapping(accountRows, row); err != nil {
		return rollback(err)
	}
	id := stringValue(row["id"])
	if id == "" {
		return rollback(errors.New("missing_record_id"))
	}
	if err := tx.Account.DeleteOneID(id).Exec(ctx); err != nil && !controlplaneent.IsNotFound(err) {
		return rollback(err)
	}
	if err := saveRecord(ctx, id, row, tx.Account.Create(), accountEntFields); err != nil {
		return rollback(err)
	}
	return tx.Commit()
}

func (s *postgresEntStateStore) ListOrganizations(ctx context.Context) ([]map[string]any, error) {
	rows, err := loadRecordSet(ctx, s.client.Organization.Query().All, organizationEntFields)
	if err != nil {
		return nil, err
	}
	return filteredRecords(rows, "")
}

func (s *postgresEntStateStore) SaveOrganization(ctx context.Context, row map[string]any) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	client := tx.Client()
	if _, err := client.Account.Get(ctx, stringValue(row["billingAccountId"])); err != nil {
		_ = tx.Rollback()
		if controlplaneent.IsNotFound(err) {
			return errAccountNotFound
		}
		return err
	}
	if err := s.replaceRecord(ctx, row, func(id string) error { return client.Organization.DeleteOneID(id).Exec(ctx) }, func() any { return client.Organization.Create() }, organizationEntFields); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *postgresEntStateStore) ListMemberships(ctx context.Context) ([]map[string]any, error) {
	rows, err := loadRecordSet(ctx, s.client.Membership.Query().All, membershipEntFields)
	if err != nil {
		return nil, err
	}
	return filteredRecords(rows, "")
}

func (s *postgresEntStateStore) SaveMembership(ctx context.Context, row map[string]any) error {
	if !validRole(stringValue(row["role"])) {
		return errInvalidRole
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	client := tx.Client()
	accountID := stringValue(row["accountId"])
	if _, err := client.Account.Get(ctx, accountID); err != nil {
		_ = tx.Rollback()
		if controlplaneent.IsNotFound(err) {
			return errAccountNotFound
		}
		return err
	}
	organization, err := client.Organization.Get(ctx, stringValue(row["organizationId"]))
	if err != nil {
		_ = tx.Rollback()
		if controlplaneent.IsNotFound(err) {
			return errOrganizationNotFound
		}
		return err
	}
	user, err := client.User.Get(ctx, stringValue(row["userId"]))
	if err != nil {
		_ = tx.Rollback()
		if controlplaneent.IsNotFound(err) {
			return errMembershipUserNotFound
		}
		return err
	}
	if organization.BillingAccountID != accountID || user.AccountID != accountID {
		_ = tx.Rollback()
		return errMembershipAccountMismatch
	}
	if err := s.replaceRecord(ctx, row, func(id string) error { return client.Membership.DeleteOneID(id).Exec(ctx) }, func() any { return client.Membership.Create() }, membershipEntFields); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *postgresEntStateStore) ListProjectTaskSyncHeads(ctx context.Context) ([]map[string]any, error) {
	rows, err := loadRecordSet(ctx, s.client.ProjectTaskSyncHead.Query().All, projectTaskSyncHeadEntFields)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		switch stringValue(row["kind"]) {
		case "project":
			row["projectId"] = row["id"]
		case "task":
			row["taskId"] = row["id"]
		}
	}
	return filteredRecords(rows, "")
}

func (s *postgresEntStateStore) SaveProjectTaskSyncHead(ctx context.Context, row map[string]any) error {
	return s.upsertRecord(ctx, row,
		func(id string) (any, error) { return s.client.ProjectTaskSyncHead.Get(ctx, id) },
		projectTaskSyncHeadIdentityMatches,
		func() any { return s.client.ProjectTaskSyncHead.Create() },
		func(id string) any { return s.client.ProjectTaskSyncHead.UpdateOneID(id) },
		projectTaskSyncHeadEntFields,
		false,
	)
}

func (s *postgresEntStateStore) ListWorkspaceSyncEvents(ctx context.Context, workspaceID string, after int64, limit int) ([]map[string]any, error) {
	query := s.client.WorkspaceSyncEvent.Query().
		Where(workspacesyncevent.WorkspaceID(workspaceID), workspacesyncevent.CursorGT(after)).
		Order(controlplaneent.Asc(workspacesyncevent.FieldCursor))
	if limit > 0 {
		query.Limit(limit)
	}
	rows, err := query.All(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		payload := map[string]any{}
		if err := json.Unmarshal([]byte(row.PayloadJSON), &payload); err != nil {
			return nil, err
		}
		result = append(result, map[string]any{
			"id":             row.ID,
			"operationId":    row.OperationID,
			"workspaceId":    row.WorkspaceID,
			"cursor":         row.Cursor,
			"entityKind":     row.EntityKind,
			"projectId":      row.ProjectID,
			"taskId":         row.TaskID,
			"clientId":       row.ClientID,
			"actorUserId":    row.ActorUserID,
			"baseVersion":    row.BaseVersion,
			"serverVersion":  row.ServerVersion,
			"operation":      row.Operation,
			"status":         row.Status,
			"payload":        payload,
			"contentDigest":  row.ContentDigest,
			"idempotencyKey": row.IdempotencyKey,
			"requestHash":    row.RequestHash,
			"conflictId":     row.ConflictID,
			"createdAt":      row.CreatedAt.UTC().Format(time.RFC3339Nano),
			"occurredAt":     row.OccurredAt.UTC().Format(time.RFC3339),
		})
	}
	return result, nil
}

func (s *postgresEntStateStore) SaveWorkspaceSyncEvent(ctx context.Context, row map[string]any) error {
	id := stringValue(row["id"])
	idempotencyKey := stringValue(row["idempotencyKey"])
	requestHash := stringValue(row["requestHash"])
	existing, err := s.client.WorkspaceSyncEvent.Query().
		Where(workspacesyncevent.Or(
			workspacesyncevent.ID(id),
			workspacesyncevent.IdempotencyKey(idempotencyKey),
			workspacesyncevent.And(workspacesyncevent.WorkspaceID(stringValue(row["workspaceId"])), workspacesyncevent.OperationID(stringValue(row["operationId"]))),
		)).
		Only(ctx)
	if err == nil {
		if existing.ID == id && existing.IdempotencyKey == idempotencyKey && existing.RequestHash == requestHash {
			return nil
		}
		return errIdempotencyConflict
	}
	if !controlplaneent.IsNotFound(err) {
		return err
	}
	payload, err := json.Marshal(row["payload"])
	if err != nil {
		return err
	}
	occurredAt, err := time.Parse(time.RFC3339, stringValue(row["occurredAt"]))
	if err != nil {
		return err
	}
	_, err = s.client.WorkspaceSyncEvent.Create().
		SetID(id).
		SetOperationID(stringValue(row["operationId"])).
		SetWorkspaceID(stringValue(row["workspaceId"])).
		SetCursor(int64(numberField(row, "cursor", 0))).
		SetEntityKind(stringValue(row["entityKind"])).
		SetProjectID(stringValue(row["projectId"])).
		SetTaskID(stringValue(row["taskId"])).
		SetClientID(stringValue(row["clientId"])).
		SetActorUserID(stringValue(row["actorUserId"])).
		SetBaseVersion(int64(numberField(row, "baseVersion", 0))).
		SetServerVersion(int64(numberField(row, "serverVersion", 0))).
		SetOperation(stringValue(row["operation"])).
		SetStatus(stringValue(row["status"])).
		SetPayloadJSON(string(payload)).
		SetContentDigest(stringValue(row["contentDigest"])).
		SetIdempotencyKey(idempotencyKey).
		SetRequestHash(requestHash).
		SetConflictID(stringValue(row["conflictId"])).
		SetOccurredAt(occurredAt).
		Save(ctx)
	return err
}

func (s *postgresEntStateStore) ListExecutionRequests(ctx context.Context) ([]map[string]any, error) {
	rows, err := loadRecordSet(ctx, s.client.ExecutionRequest.Query().All, executionRequestEntFields)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		row["requestId"] = row["id"]
	}
	return filteredRecords(rows, "")
}

func (s *postgresEntStateStore) SaveExecutionRequest(ctx context.Context, row map[string]any) error {
	return s.upsertRecord(ctx, row,
		func(id string) (any, error) { return s.client.ExecutionRequest.Get(ctx, id) },
		executionRequestIdentityMatches,
		func() any { return s.client.ExecutionRequest.Create() },
		func(id string) any { return s.client.ExecutionRequest.UpdateOneID(id) },
		executionRequestEntFields,
		false,
	)
}

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

func (s *postgresEntStateStore) SaveCompute(ctx context.Context, row map[string]any) error {
	return s.upsertRecord(ctx, row,
		func(id string) (any, error) { return s.client.ComputeAllocation.Get(ctx, id) },
		computeResourceIdentityMatches,
		func() any { return s.client.ComputeAllocation.Create() },
		func(id string) any { return s.client.ComputeAllocation.UpdateOneID(id) },
		computeEntFields,
		true,
	)
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

func (s *postgresEntStateStore) SaveStorage(ctx context.Context, row map[string]any) error {
	return s.upsertRecord(ctx, row,
		func(id string) (any, error) { return s.client.StorageVolume.Get(ctx, id) },
		storageResourceIdentityMatches,
		func() any { return s.client.StorageVolume.Create() },
		func(id string) any { return s.client.StorageVolume.UpdateOneID(id) },
		storageEntFields,
		true,
	)
}

func (s *postgresEntStateStore) ClaimResourceBillingOperation(ctx context.Context, resourceType string, row map[string]any) (map[string]any, bool, error) {
	id := stringValue(row["id"])
	switch resourceType {
	case "compute":
		return claimEntBillingOperation(ctx, row,
			func() (map[string]any, error) {
				entity, err := s.client.ComputeAllocation.Query().Where(computeallocation.ID(id)).Only(ctx)
				if controlplaneent.IsNotFound(err) {
					return nil, nil
				}
				if err != nil {
					return nil, err
				}
				return recordFromEnt(entity, computeEntFields), nil
			},
			func() error { return saveRecord(ctx, id, row, s.client.ComputeAllocation.Create(), computeEntFields) },
			func(expectedOperationID string) (int, error) {
				builder := s.client.ComputeAllocation.Update().Where(computeallocation.ID(id), computeallocation.BillingOperationID(expectedOperationID))
				setRecordFields(builder, row, computeEntFields)
				return builder.Save(ctx)
			},
		)
	case "storage":
		return claimEntBillingOperation(ctx, row,
			func() (map[string]any, error) {
				entity, err := s.client.StorageVolume.Query().Where(storagevolume.ID(id)).Only(ctx)
				if controlplaneent.IsNotFound(err) {
					return nil, nil
				}
				if err != nil {
					return nil, err
				}
				return recordFromEnt(entity, storageEntFields), nil
			},
			func() error { return saveRecord(ctx, id, row, s.client.StorageVolume.Create(), storageEntFields) },
			func(expectedOperationID string) (int, error) {
				builder := s.client.StorageVolume.Update().Where(storagevolume.ID(id), storagevolume.BillingOperationID(expectedOperationID))
				setRecordFields(builder, row, storageEntFields)
				return builder.Save(ctx)
			},
		)
	default:
		return nil, false, errors.New("invalid_billing_resource_type")
	}
}

func claimEntBillingOperation(ctx context.Context, row map[string]any, load func() (map[string]any, error), create func() error, update func(string) (int, error)) (map[string]any, bool, error) {
	if stringValue(row["id"]) == "" || stringValue(row["billingOperationId"]) == "" {
		return nil, false, errors.New("billing_operation_identity_required")
	}
	for range 4 {
		existing, err := load()
		if err != nil {
			return nil, false, err
		}
		if existing == nil {
			if err := create(); err != nil {
				if controlplaneent.IsConstraintError(err) {
					continue
				}
				return nil, false, err
			}
			return cloneMap(row), true, nil
		}
		operationID := stringValue(row["billingOperationId"])
		currentOperationID := stringValue(existing["billingOperationId"])
		if currentOperationID == operationID {
			if !billingOperationIdentityMatches(existing, row) {
				return nil, false, errIdempotencyConflict
			}
			return existing, false, nil
		}
		if billingOperationInProgress(stringValue(existing["billingStatus"])) {
			return existing, false, errBillingOperationInProgress
		}
		updated, err := update(currentOperationID)
		if err != nil {
			return nil, false, err
		}
		if updated == 1 {
			return mergeMaps(existing, row), true, nil
		}
	}
	return nil, false, errors.New("billing_operation_claim_retry_exhausted")
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

func (s *postgresEntStateStore) ListWorkspaces(ctx context.Context, accountID string) ([]map[string]any, error) {
	query := s.client.Workspace.Query()
	if accountID != "" {
		query.Where(workspace.Or(workspace.AccountID(accountID), workspace.And(workspace.AccountID(""), workspace.OwnerAccountID(accountID))))
	}
	rows, err := loadRecordSet(ctx, query.All, workspaceEntFields)
	if err != nil {
		return nil, err
	}
	return filteredRecords(rows, accountID)
}

func (s *postgresEntStateStore) SaveWorkspace(ctx context.Context, row map[string]any) error {
	row = cloneMap(row)
	if _, ok := row["customerProduct"]; !ok {
		row["customerProduct"] = true
	}
	return s.replaceRecord(ctx, row, func(id string) error { return s.client.Workspace.DeleteOneID(id).Exec(ctx) }, func() any { return s.client.Workspace.Create() }, workspaceEntFields)
}

func (s *postgresEntStateStore) ClaimWorkspaceCreate(ctx context.Context, workspaceRow map[string]any, operation map[string]any) error {
	if firstNonEmpty(stringValue(workspaceRow["accountId"]), stringValue(workspaceRow["ownerAccountId"])) == "" || stringValue(workspaceRow["id"]) == "" || stringValue(operation["id"]) == "" {
		return errors.New("invalid_workspace_create_claim")
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	workspaceRow = cloneMap(workspaceRow)
	if _, ok := workspaceRow["customerProduct"]; !ok {
		workspaceRow["customerProduct"] = true
	}
	if err := saveRecord(ctx, stringValue(workspaceRow["id"]), workspaceRow, tx.Workspace.Create(), workspaceEntFields); err != nil {
		if controlplaneent.IsConstraintError(err) {
			return errPrimaryWorkspaceExists
		}
		return err
	}
	if err := saveRecord(ctx, stringValue(operation["id"]), operation, tx.RuntimeOperation.Create(), runtimeOpEntFields); err != nil {
		if controlplaneent.IsConstraintError(err) {
			return errPrimaryWorkspaceExists
		}
		return err
	}
	return tx.Commit()
}

func (s *postgresEntStateStore) ClaimWorkspaceResume(ctx context.Context, workspaceID string, operation map[string]any) (map[string]any, bool, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	claim, err := decodeWorkspaceResumeOperation(operation)
	if err != nil {
		return nil, false, err
	}
	existing, err := tx.RuntimeOperation.Get(ctx, stringValue(operation["id"]))
	if err == nil {
		existingRecord := recordFromEnt(existing, runtimeOpEntFields)
		result, err := decodeWorkspaceResumeOperation(existingRecord)
		if err != nil {
			return nil, false, err
		}
		if result.RequestHash != claim.RequestHash {
			return nil, false, errIdempotencyConflict
		}
		if existing.Status == "succeeded" && result.Response != nil {
			return cloneMap(result.Response), true, tx.Commit()
		}
		if existing.Status == "started" && result.LeaseExpiresAt != nil && result.LeaseExpiresAt.After(time.Now().UTC()) {
			return nil, false, errWorkspaceResumeInProgress
		}
		update := tx.RuntimeOperation.UpdateOneID(existing.ID).SetStatus("started").SetResult(stringValue(operation["result"]))
		if existing.Status == "retryable" {
			update.Where(runtimeoperation.StatusEQ("retryable"))
		} else {
			update.Where(runtimeoperation.StatusEQ("started"), runtimeoperation.ResultEQ(existing.Result))
		}
		if _, err := update.Save(ctx); err != nil {
			if controlplaneent.IsNotFound(err) {
				return nil, false, errWorkspaceResumeInProgress
			}
			return nil, false, err
		}
		if _, err := tx.Workspace.UpdateOneID(workspaceID).SetState("resuming").SetStatus("resuming").Save(ctx); err != nil {
			return nil, false, err
		}
		return nil, false, tx.Commit()
	}
	if !controlplaneent.IsNotFound(err) {
		return nil, false, err
	}
	if _, err := tx.Workspace.UpdateOneID(workspaceID).
		Where(workspace.Or(workspace.StateIn("suspended", "stopped"), workspace.And(workspace.StateEQ(""), workspace.StatusIn("suspended", "stopped")))).
		SetState("resuming").SetStatus("resuming").Save(ctx); err != nil {
		if controlplaneent.IsNotFound(err) {
			concurrent, queryErr := tx.RuntimeOperation.Get(ctx, stringValue(operation["id"]))
			if queryErr == nil {
				result, decodeErr := decodeWorkspaceResumeOperation(recordFromEnt(concurrent, runtimeOpEntFields))
				if decodeErr != nil {
					return nil, false, decodeErr
				}
				if result.RequestHash != claim.RequestHash {
					return nil, false, errIdempotencyConflict
				}
				if concurrent.Status == "succeeded" && result.Response != nil {
					return cloneMap(result.Response), true, tx.Commit()
				}
				return nil, false, errWorkspaceResumeInProgress
			}
			return nil, false, errWorkspaceResumeInProgress
		}
		return nil, false, err
	}
	store := &postgresEntStateStore{client: tx.Client()}
	if err := store.SaveRuntimeOperation(ctx, operation); err != nil {
		return nil, false, err
	}
	return nil, false, tx.Commit()
}

func (s *postgresEntStateStore) FailWorkspaceResume(ctx context.Context, workspaceID string, operationID string, errorCode string) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	operation, err := tx.RuntimeOperation.Get(ctx, operationID)
	if controlplaneent.IsNotFound(err) {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	result, err := decodeWorkspaceResumeOperation(recordFromEnt(operation, runtimeOpEntFields))
	if err != nil {
		return err
	}
	result.ErrorCode = errorCode
	result.LeaseExpiresAt = nil
	if _, err := tx.RuntimeOperation.UpdateOneID(operationID).Where(runtimeoperation.StatusEQ("started")).SetStatus("retryable").SetResult(encodeWorkspaceResumeOperation(result)).Save(ctx); err != nil && !controlplaneent.IsNotFound(err) {
		return err
	}
	if _, err := tx.Workspace.UpdateOneID(workspaceID).Where(workspace.Or(workspace.StateEQ("resuming"), workspace.StatusEQ("resuming"))).SetState("suspended").SetStatus("suspended").Save(ctx); err != nil && !controlplaneent.IsNotFound(err) {
		return err
	}
	return tx.Commit()
}

func (s *postgresEntStateStore) CommitWorkspaceResume(ctx context.Context, workspace map[string]any, audit map[string]any, operation map[string]any) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	store := &postgresEntStateStore{client: tx.Client()}
	if err := store.SaveAuditEvent(ctx, audit); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := store.SaveRuntimeOperation(ctx, operation); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := store.SaveWorkspace(ctx, workspace); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *postgresEntStateStore) DeleteWorkspace(ctx context.Context, id string) error {
	err := s.client.Workspace.DeleteOneID(id).Exec(ctx)
	if controlplaneent.IsNotFound(err) {
		return nil
	}
	return err
}

func (s *postgresEntStateStore) ListWorkspaceBackups(ctx context.Context, workspaceID string) ([]map[string]any, error) {
	query := s.client.WorkspaceBackup.Query()
	if workspaceID != "" {
		query = query.Where(workspacebackup.WorkspaceID(workspaceID))
	}
	rows, err := loadRecordSet(ctx, query.All, workspaceBackupEntFields)
	if err != nil {
		return nil, err
	}
	return filteredRecords(rows, "")
}

func (s *postgresEntStateStore) SaveWorkspaceBackup(ctx context.Context, row map[string]any) error {
	id := stringValue(row["id"])
	if id == "" {
		return errors.New("missing_record_id")
	}
	existing, err := s.client.WorkspaceBackup.Query().Where(workspacebackup.IdempotencyKey(stringValue(row["idempotencyKey"]))).Only(ctx)
	if err == nil {
		if existing.RequestHash != stringValue(row["requestHash"]) {
			return errIdempotencyConflict
		}
		builder := s.client.WorkspaceBackup.UpdateOneID(existing.ID)
		setRecordFields(builder, row, workspaceBackupEntFields)
		return execCreate(ctx, builder)
	}
	if !controlplaneent.IsNotFound(err) {
		return err
	}
	return saveRecord(ctx, id, row, s.client.WorkspaceBackup.Create(), workspaceBackupEntFields)
}

func (s *postgresEntStateStore) ListAuditEvents(ctx context.Context, accountID string) ([]map[string]any, error) {
	query := s.client.AdminAuditEvent.Query().Order(controlplaneent.Asc(adminauditevent.FieldCreatedAt, adminauditevent.FieldID))
	if accountID != "" {
		query.Where(adminauditevent.Or(adminauditevent.TargetAccountID(accountID), adminauditevent.And(adminauditevent.TargetAccountID(""), adminauditevent.ActorAccountID(accountID))))
	}
	rows, err := loadEventRows(ctx, query.All, auditEntFields)
	return filteredEvents(rows, accountID), err
}

func (s *postgresEntStateStore) SaveAuditEvent(ctx context.Context, row map[string]any) error {
	return s.replaceRecord(ctx, row, func(id string) error { return s.client.AdminAuditEvent.DeleteOneID(id).Exec(ctx) }, func() any { return s.client.AdminAuditEvent.Create() }, auditEntFields)
}

func (s *postgresEntStateStore) ListSupportMappings(ctx context.Context, accountID string) ([]map[string]any, error) {
	query := s.client.SupportTicketMapping.Query()
	if accountID != "" {
		query.Where(supportticketmapping.AccountID(accountID))
	}
	rows, err := loadRecordSet(ctx, query.All, supportEntFields)
	if err != nil {
		return nil, err
	}
	return filteredRecords(rows, accountID)
}

func (s *postgresEntStateStore) SaveSupportMapping(ctx context.Context, row map[string]any) error {
	return s.replaceRecord(ctx, row, func(id string) error { return s.client.SupportTicketMapping.DeleteOneID(id).Exec(ctx) }, func() any { return s.client.SupportTicketMapping.Create() }, supportEntFields)
}

func (s *postgresEntStateStore) ListRuntimeOperations(ctx context.Context) ([]map[string]any, error) {
	rows, err := loadEventRows(ctx, s.client.RuntimeOperation.Query().Order(controlplaneent.Asc(runtimeoperation.FieldCreatedAt, runtimeoperation.FieldID)).All, runtimeOpEntFields)
	return rows, err
}

func (s *postgresEntStateStore) SaveRuntimeOperation(ctx context.Context, row map[string]any) error {
	return s.upsertRecord(ctx, row,
		func(id string) (any, error) { return s.client.RuntimeOperation.Get(ctx, id) },
		runtimeOperationIdentityMatches,
		func() any { return s.client.RuntimeOperation.Create() },
		func(id string) any { return s.client.RuntimeOperation.UpdateOneID(id) },
		runtimeOpEntFields,
		true,
	)
}

func (s *postgresEntStateStore) BillingReconciliation(ctx context.Context) (map[string]any, bool, error) {
	row, err := s.client.BillingReconciliation.Query().Order(controlplaneent.Desc(billingreconciliation.FieldCreatedAt, billingreconciliation.FieldID)).First(ctx)
	if controlplaneent.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return recordFromEnt(row, reconcileEntFields), true, nil
}

func (s *postgresEntStateStore) SaveBillingReconciliation(ctx context.Context, row map[string]any) error {
	return s.replaceRecord(ctx, row, func(id string) error { return s.client.BillingReconciliation.DeleteOneID(id).Exec(ctx) }, func() any { return s.client.BillingReconciliation.Create() }, reconcileEntFields)
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

func (s *postgresEntStateStore) ArchiveState(ctx context.Context) (map[string]any, error) {
	jobs, err := loadEventRows(ctx, s.client.ArchiveJob.Query().All, archiveJobEntFields)
	if err != nil {
		return nil, err
	}
	computes, err := loadEventRows(ctx, s.client.ArchivedComputeAllocation.Query().All, archivedResourceEntFields)
	if err != nil {
		return nil, err
	}
	storages, err := loadEventRows(ctx, s.client.ArchivedStorageVolume.Query().All, archivedResourceEntFields)
	if err != nil {
		return nil, err
	}
	attachments, err := loadEventRows(ctx, s.client.ArchivedStorageAttachment.Query().All, archivedResourceEntFields)
	if err != nil {
		return nil, err
	}
	workspaces, err := loadEventRows(ctx, s.client.ArchivedWorkspace.Query().All, archivedResourceEntFields)
	if err != nil {
		return nil, err
	}
	auditEvents, err := loadEventRows(ctx, s.client.ArchivedAdminAuditEvent.Query().All, auditEntFields)
	if err != nil {
		return nil, err
	}
	e2eRecords, err := loadEventRows(ctx, s.client.ProductionE2ERecord.Query().All, productionE2EEntFields)
	if err != nil {
		return nil, err
	}
	resources := make([]any, 0, len(computes)+len(storages)+len(attachments)+len(workspaces))
	for _, rows := range [][]controlPlaneRecord{computes, storages, attachments, workspaces} {
		for _, row := range rows {
			resources = append(resources, row)
		}
	}
	return map[string]any{
		"jobs":             rowsAsAny(jobs),
		"resources":        resources,
		"adminAuditEvents": rowsAsAny(auditEvents),
		"productionE2E":    productionE2ESummary(e2eRecords),
		"retentionPolicy":  currentRetentionPolicy().dto(),
	}, nil
}

func (s *postgresEntStateStore) ApplyRetention(ctx context.Context, policy retentionPolicy) (map[string]any, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	result := map[string]any{"retentionPolicy": policy.dto()}
	if cutoff := policy.cutoff(policy.AdminAuditDays); !cutoff.IsZero() {
		rows, err := tx.AdminAuditEvent.Query().Where(adminauditevent.CreatedAtLT(cutoff)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			record := recordFromEnt(row, auditEntFields)
			if err := saveRecord(ctx, row.ID, record, tx.ArchivedAdminAuditEvent.Create(), auditEntFields); err != nil {
				return nil, err
			}
		}
		if len(rows) > 0 {
			if _, err := tx.AdminAuditEvent.Delete().Where(adminauditevent.CreatedAtLT(cutoff)).Exec(ctx); err != nil {
				return nil, err
			}
		}
		result["adminAuditArchived"] = len(rows)
	}
	if cutoff := policy.cutoff(policy.SupportDays); !cutoff.IsZero() {
		deleted, err := tx.SupportTicketMapping.Delete().Where(supportticketmapping.CreatedAtLT(cutoff)).Exec(ctx)
		if err != nil {
			return nil, err
		}
		result["supportDeleted"] = deleted
	}
	if cutoff := policy.cutoff(policy.ProductionE2EDays); !cutoff.IsZero() {
		deleted, err := tx.ProductionE2ERecord.Delete().Where(productione2erecord.CreatedAtLT(cutoff)).Exec(ctx)
		if err != nil {
			return nil, err
		}
		result["productionE2EDeleted"] = deleted
	}
	if err := tx.ArchiveJob.Create().
		SetID("archive-job-" + stableID(time.Now().UTC().Format(time.RFC3339Nano), "scheduled_retention")[:12]).
		SetResourceKind("retention_policy").
		SetStatus("succeeded").
		SetReason("scheduled_retention").
		SetCreatedAt(time.Now().UTC()).
		SetUpdatedAt(time.Now().UTC()).
		Exec(ctx); err != nil {
		return nil, err
	}
	return result, tx.Commit()
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
		if field.Kind == "billing_json" {
			var billing map[string]any
			if text := stringValue(raw); text != "" && json.Unmarshal([]byte(text), &billing) == nil {
				for key, value := range billing {
					row[key] = value
				}
			}
			continue
		}
		if isZero(raw) && field.Kind != "bool" {
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
	seen := map[string]bool{}
	for index, row := range rows {
		id := firstNonEmpty(stringValue(row["id"]), prefix+"-"+stableID(stringValue(row["accountId"]), stringValue(row["createdAt"]), stringValue(row["type"]), strconv.Itoa(index))[:12])
		if seen[id] {
			continue
		}
		seen[id] = true
		if err := saveRecord(ctx, id, row, create(), fields); err != nil {
			return fmt.Errorf("save %s projection %s: %w", prefix, id, err)
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
	setRecordFields(builder, row, fields)
	return execCreate(ctx, builder)
}

func setRecordFields(builder any, row controlPlaneRecord, fields []entRecordField) {
	setRecordFieldsWithEmptyText(builder, row, fields, false)
}

func setRecordFieldsWithEmptyText(builder any, row controlPlaneRecord, fields []entRecordField, includeEmptyText bool) {
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
		case "billing_json":
			billing := map[string]any{}
			for _, key := range monthlyBillingStateKeys {
				if value, ok := row[key]; ok {
					billing[key] = value
				}
			}
			if encoded, err := json.Marshal(billing); err == nil {
				callSetter(builder, field.Setter, string(encoded))
			}
		default:
			text := stringValue(value)
			if text != "" || includeEmptyText {
				callSetter(builder, field.Setter, text)
			}
		}
	}
}

func (s *postgresEntStateStore) upsertRecord(ctx context.Context, row map[string]any, get func(string) (any, error), identityMatches func(any, map[string]any) bool, create func() any, update func(string) any, fields []entRecordField, includeEmptyText bool) error {
	id := stringValue(row["id"])
	if id == "" {
		return errors.New("missing_record_id")
	}
	if existing, err := get(id); err == nil {
		if !identityMatches(existing, row) {
			return errIdempotencyConflict
		}
		builder := update(id)
		setRecordFieldsWithEmptyText(builder, row, fields, includeEmptyText)
		return execCreate(ctx, builder)
	} else if !controlplaneent.IsNotFound(err) {
		return err
	}
	if err := saveRecord(ctx, id, row, create(), fields); !controlplaneent.IsConstraintError(err) {
		return err
	}
	// Another writer inserted the canonical ID between the read and create.
	existing, err := get(id)
	if err != nil || !identityMatches(existing, row) {
		return errIdempotencyConflict
	}
	return nil
}

func projectTaskSyncHeadIdentityMatches(existing any, row map[string]any) bool {
	entity, ok := existing.(*controlplaneent.ProjectTaskSyncHead)
	return ok && entity.Kind == stringValue(row["kind"]) && entity.OrganizationID == stringValue(row["organizationId"]) && entity.WorkspaceID == stringValue(row["workspaceId"]) && entity.ProjectID == stringValue(row["projectId"]) && entity.LocalAliasID == stringValue(row["localAliasId"])
}

func executionRequestIdentityMatches(existing any, row map[string]any) bool {
	entity, ok := existing.(*controlplaneent.ExecutionRequest)
	return ok && entity.OrganizationID == stringValue(row["organizationId"]) && entity.WorkspaceID == stringValue(row["workspaceId"]) && entity.ProjectID == stringValue(row["projectId"]) && entity.TaskID == stringValue(row["taskId"]) && entity.ActorUserID == stringValue(row["actorUserId"]) && entity.EnvironmentRef == stringValue(row["environmentRef"]) && entity.IdempotencyKey == stringValue(row["idempotencyKey"])
}

func computeResourceIdentityMatches(existing any, row map[string]any) bool {
	entity, ok := existing.(*controlplaneent.ComputeAllocation)
	return ok && entity.AccountID == stringValue(row["accountId"])
}

func storageResourceIdentityMatches(existing any, row map[string]any) bool {
	entity, ok := existing.(*controlplaneent.StorageVolume)
	return ok && entity.AccountID == stringValue(row["accountId"])
}

func runtimeOperationIdentityMatches(existing any, row map[string]any) bool {
	entity, ok := existing.(*controlplaneent.RuntimeOperation)
	return ok && entity.OperationID == stringValue(row["operationId"]) && entity.AccountID == stringValue(row["accountId"]) && entity.WorkspaceID == stringValue(row["workspaceId"]) && entity.ResourceID == stringValue(row["resourceId"]) && entity.ResourceKind == stringValue(row["resourceKind"]) && entity.Action == stringValue(row["action"])
}

func (s *postgresEntStateStore) replaceRecord(ctx context.Context, row map[string]any, deleteOne func(string) error, create func() any, fields []entRecordField) error {
	id := stringValue(row["id"])
	if id == "" {
		return errors.New("missing_record_id")
	}
	if err := deleteOne(id); err != nil && !controlplaneent.IsNotFound(err) {
		return err
	}
	return saveRecord(ctx, id, row, create(), fields)
}

func filteredRecords(rows controlPlaneRecordSet, accountID string) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if accountID != "" && firstNonEmpty(stringValue(row["accountId"]), stringValue(row["ownerAccountId"])) != accountID {
			continue
		}
		out = append(out, cloneMap(row))
	}
	return out, nil
}

func filteredEvents(rows []controlPlaneRecord, accountID string) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if accountID != "" && firstNonEmpty(stringValue(row["accountId"]), stringValue(row["targetAccountId"]), stringValue(row["actorAccountId"])) != accountID {
			continue
		}
		out = append(out, cloneMap(row))
	}
	return out
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
