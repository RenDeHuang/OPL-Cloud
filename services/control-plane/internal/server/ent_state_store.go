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
	"opl-cloud/services/control-plane/ent/adminauditevent"
	"opl-cloud/services/control-plane/ent/announcement"
	"opl-cloud/services/control-plane/ent/announcementread"
	"opl-cloud/services/control-plane/ent/billingreconciliation"
	"opl-cloud/services/control-plane/ent/productione2erecord"
	"opl-cloud/services/control-plane/ent/runtimeoperation"
	"opl-cloud/services/control-plane/ent/supportticketmapping"
	controlplanemigrations "opl-cloud/services/control-plane/migrations"
	"opl-cloud/services/internal/postgresmigrate"
)

const (
	singletonFactID                  = "default"
	controlPlaneMaxOpenDBConnections = 20
)

var errIdempotencyConflict = errors.New("idempotency_conflict")
var errInvalidWorkspaceBillingState = errors.New("invalid_workspace_billing_state")

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
	if err := postgresmigrate.ValidateTLS(databaseURL); err != nil {
		return nil, err
	}
	return newPostgresEntStateStore(databaseURL)
}

func newTestPostgresEntStateStore(databaseURL string) (StateStore, error) {
	return newPostgresEntStateStore(databaseURL)
}

func newPostgresEntStateStore(databaseURL string) (StateStore, error) {
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
		{Version: "202607170001_invited_account_identity", Run: func(ctx context.Context) error {
			return controlplanemigrations.ApplyInvitedAccountIdentity(ctx, driver)
		}},
		{Version: "202607170002_workspace_renewal", Run: func(ctx context.Context) error {
			return controlplanemigrations.ApplyWorkspaceRenewal(ctx, driver)
		}},
		{Version: "202607170003_workspace_auto_renew_audit", Run: func(ctx context.Context) error {
			return controlplanemigrations.ApplyAutoRenewAudit(ctx, driver)
		}},
		{Version: "202607180001_customer_identity_hard_cut", Run: func(ctx context.Context) error {
			return controlplanemigrations.ApplyCustomerIdentityHardCut(ctx, driver)
		}},
		{Version: "202607190001_workspace_api_key_id", Run: func(ctx context.Context) error {
			return controlplanemigrations.ApplyWorkspaceAPIKeyID(ctx, driver)
		}},
		{Version: "202607190002_pilot_announcements", Run: func(ctx context.Context) error {
			return controlplanemigrations.ApplyPilotAnnouncements(ctx, driver)
		}},
		{Version: "202607230001_workspace_purchase_receipt_id", Run: func(ctx context.Context) error {
			return controlplanemigrations.ApplyWorkspacePurchaseReceiptID(ctx, driver)
		}},
		{Version: "202607240001_multi_workspace_pagination", Run: func(ctx context.Context) error {
			return controlplanemigrations.ApplyMultiWorkspacePagination(ctx, driver)
		}},
		{Version: "202607250001_control_plane_capacity_indexes", Run: func(ctx context.Context) error {
			return controlplanemigrations.ApplyControlPlaneCapacityIndexes(ctx, driver)
		}},
		{Version: "202608170001_remote_companion_broker", Run: func(ctx context.Context) error {
			return controlplanemigrations.ApplyRemoteCompanionBroker(ctx, driver)
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

func jsonTextField(entityField, setter string, path ...string) entRecordField {
	return entRecordField{EntityField: entityField, Setter: setter, Path: path, Kind: "json_text"}
}

func lockRowForUpdate(selector *entsql.Selector) {
	if selector.Dialect() == dialect.Postgres {
		selector.ForUpdate()
	}
}

// ponytail: keep low-cardinality monthly state in one JSON column at the current
// scale; promote fields to indexed columns only when renewal scans become measurable.
var monthlyBillingStateKeys = []string{
	"resourceType",
	"billingOperationStartedAt",
	"sub2apiRedeemCode",
	"sub2apiRefundCode",
	"priceVersion",
	"currency",
	"priceSnapshot",
	"monthlyPriceCnyCents",
	"chargeUsdMicros",
	"billingAnchorDay",
	"periodStart",
	"paidThrough",
	"autoRenew",
	"lastRenewalAttemptAt",
	"lastBillingError",
	"manualReviewReason",
	"lastReceiptId",
	"sub2apiChargeConfirmation",
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
	"reviewResolutionKey",
	"reviewResolutionFingerprint",
	"reviewResolutionDecision",
	"reviewResolutionEvidenceRef",
	"reviewResolutionReviewer",
	"reviewResolutionPhase",
	"reviewResolutionReceiptId",
	"reviewOriginalReceiptId",
	"reviewResolutionResolvedAt",
	"reviewResolutionResult",
}

var (
	runtimeOpEntFields = []entRecordField{
		textField("OperationID", "SetOperationID", "operationId"),
		textField("AccountID", "SetAccountID", "accountId"),
		textField("WorkspaceID", "SetWorkspaceID", "workspaceId"),
		textField("PeriodStart", "SetPeriodStart", "periodStart"),
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
		jsonTextField("BeforeJSON", "SetBeforeJSON", "before"),
		jsonTextField("AfterJSON", "SetAfterJSON", "after"),
		textField("Result", "SetResult", "result"),
	}
	announcementEntFields = []entRecordField{
		textField("Title", "SetTitle", "title"),
		textField("Body", "SetBody", "body"),
		textField("Status", "SetStatus", "status"),
		textField("StartsAt", "SetStartsAt", "startsAt"),
		textField("EndsAt", "SetEndsAt", "endsAt"),
		textField("PublishedAt", "SetPublishedAt", "publishedAt"),
		textField("CreatedByUserID", "SetCreatedByUserID", "createdByUserId"),
		textField("UpdatedByUserID", "SetUpdatedByUserID", "updatedByUserId"),
	}
	announcementReadEntFields = []entRecordField{
		textField("AnnouncementID", "SetAnnouncementID", "announcementId"),
		textField("UserID", "SetUserID", "userId"),
		textField("ReadAt", "SetReadAt", "readAt"),
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

func (s *postgresEntStateStore) ListAnnouncements(ctx context.Context) ([]map[string]any, error) {
	entities, err := s.client.Announcement.Query().Order(controlplaneent.Desc(announcement.FieldCreatedAt, announcement.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]map[string]any, 0, len(entities))
	for _, entity := range entities {
		rows = append(rows, announcementRecordFromEnt(entity))
	}
	return rows, nil
}

func (s *postgresEntStateStore) ApplyAnnouncementMutation(ctx context.Context, mutation announcementMutation) (map[string]any, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	auditID := stringValue(mutation.AuditEvent["id"])
	if existing, auditErr := client.AdminAuditEvent.Query().Where(adminauditevent.IDEQ(auditID), lockRowForUpdate).Only(ctx); auditErr == nil {
		return announcementReplay(recordFromEnt(existing, auditEntFields), mutation)
	} else if !controlplaneent.IsNotFound(auditErr) {
		return nil, auditErr
	}

	var current map[string]any
	entity, queryErr := client.Announcement.Query().Where(announcement.IDEQ(mutation.AnnouncementID), lockRowForUpdate).Only(ctx)
	if queryErr == nil {
		current = announcementRecordFromEnt(entity)
	} else if !controlplaneent.IsNotFound(queryErr) {
		return nil, queryErr
	}
	if existing, auditErr := client.AdminAuditEvent.Query().Where(adminauditevent.IDEQ(auditID), lockRowForUpdate).Only(ctx); auditErr == nil {
		return announcementReplay(recordFromEnt(existing, auditEntFields), mutation)
	} else if !controlplaneent.IsNotFound(auditErr) {
		return nil, auditErr
	}
	desired, err := prepareAnnouncementMutation(current, mutation, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if entity == nil {
		err = saveRecord(ctx, mutation.AnnouncementID, desired, client.Announcement.Create(), announcementEntFields)
	} else {
		builder := client.Announcement.UpdateOneID(mutation.AnnouncementID)
		setRecordFieldsWithEmptyText(builder, desired, announcementEntFields, true)
		err = execCreate(ctx, builder)
	}
	if err != nil {
		if controlplaneent.IsConstraintError(err) {
			_ = tx.Rollback()
			return s.replayAnnouncementMutation(ctx, mutation)
		}
		return nil, err
	}
	saved, err := client.Announcement.Get(ctx, mutation.AnnouncementID)
	if err != nil {
		return nil, err
	}
	authoritative := announcementRecordFromEnt(saved)
	audit := announcementAudit(mutation, current, authoritative)
	if err := saveRecord(ctx, auditID, audit, client.AdminAuditEvent.Create(), auditEntFields); err != nil {
		if controlplaneent.IsConstraintError(err) {
			_ = tx.Rollback()
			return s.replayAnnouncementMutation(ctx, mutation)
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return authoritative, nil
}

func (s *postgresEntStateStore) replayAnnouncementMutation(ctx context.Context, mutation announcementMutation) (map[string]any, error) {
	existing, err := s.client.AdminAuditEvent.Get(ctx, stringValue(mutation.AuditEvent["id"]))
	if err != nil {
		if controlplaneent.IsNotFound(err) {
			return nil, errIdempotencyConflict
		}
		return nil, err
	}
	return announcementReplay(recordFromEnt(existing, auditEntFields), mutation)
}

func (s *postgresEntStateStore) ListAnnouncementReads(ctx context.Context, userID string) ([]map[string]any, error) {
	query := s.client.AnnouncementRead.Query().Order(controlplaneent.Asc(announcementread.FieldCreatedAt, announcementread.FieldID))
	if userID != "" {
		query.Where(announcementread.UserID(userID))
	}
	entities, err := query.All(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]map[string]any, 0, len(entities))
	for _, entity := range entities {
		rows = append(rows, announcementReadRecordFromEnt(entity))
	}
	return rows, nil
}

func (s *postgresEntStateStore) MarkAnnouncementRead(ctx context.Context, announcementID, userID, readAt string) (map[string]any, error) {
	if announcementID == "" || userID == "" {
		return nil, errAnnouncementNotActive
	}
	id := announcementReadID(announcementID, userID)
	if existing, err := s.client.AnnouncementRead.Get(ctx, id); err == nil {
		return announcementReadRecordFromEnt(existing), nil
	} else if !controlplaneent.IsNotFound(err) {
		return nil, err
	}
	readTime, ok := optionalAnnouncementTime(readAt)
	if !ok {
		return nil, errAnnouncementNotActive
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	announcementEntity, err := client.Announcement.Query().Where(announcement.IDEQ(announcementID), lockRowForUpdate).Only(ctx)
	if err != nil {
		if !controlplaneent.IsNotFound(err) {
			return nil, err
		}
		return nil, errAnnouncementNotActive
	}
	if existing, queryErr := client.AnnouncementRead.Get(ctx, id); queryErr == nil {
		return announcementReadRecordFromEnt(existing), nil
	} else if !controlplaneent.IsNotFound(queryErr) {
		return nil, queryErr
	}
	if !announcementIsActive(announcementRecordFromEnt(announcementEntity), readTime) {
		return nil, errAnnouncementNotActive
	}
	row := map[string]any{
		"id": id, "announcementId": announcementID, "userId": userID, "readAt": readAt,
		"createdAt": readAt, "updatedAt": readAt,
	}
	if err := saveRecord(ctx, id, row, client.AnnouncementRead.Create(), announcementReadEntFields); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return row, nil
}

func announcementRecordFromEnt(entity *controlplaneent.Announcement) map[string]any {
	if entity == nil {
		return nil
	}
	row := recordFromEnt(entity, announcementEntFields)
	if !entity.UpdatedAt.IsZero() {
		row["updatedAt"] = entity.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	return row
}

func announcementReadRecordFromEnt(entity *controlplaneent.AnnouncementRead) map[string]any {
	if entity == nil {
		return nil
	}
	row := recordFromEnt(entity, announcementReadEntFields)
	if !entity.UpdatedAt.IsZero() {
		row["updatedAt"] = entity.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	return row
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

func (s *postgresEntStateStore) CreateSupportMapping(ctx context.Context, row map[string]any, limit int) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	rollback := func(err error) error {
		_ = tx.Rollback()
		return err
	}
	if s.client.Driver().Dialect() == dialect.Postgres {
		if err := tx.Driver().Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", []any{stringValue(row["accountId"])}, nil); err != nil {
			return rollback(err)
		}
	}
	id := stringValue(row["id"])
	if _, err := tx.SupportTicketMapping.Get(ctx, id); controlplaneent.IsNotFound(err) {
		count, err := tx.SupportTicketMapping.Query().Where(supportticketmapping.AccountIDEQ(stringValue(row["accountId"]))).Count(ctx)
		if err != nil {
			return rollback(err)
		}
		if count >= limit {
			return rollback(errors.New("support_mapping_limit_reached"))
		}
	} else if err != nil {
		return rollback(err)
	}
	if err := tx.SupportTicketMapping.DeleteOneID(id).Exec(ctx); err != nil && !controlplaneent.IsNotFound(err) {
		return rollback(err)
	}
	if err := saveRecord(ctx, id, row, tx.SupportTicketMapping.Create(), supportEntFields); err != nil {
		return rollback(err)
	}
	return tx.Commit()
}

func (s *postgresEntStateStore) SaveSupportMapping(ctx context.Context, row map[string]any) error {
	return s.replaceRecord(ctx, row, func(id string) error { return s.client.SupportTicketMapping.DeleteOneID(id).Exec(ctx) }, func() any { return s.client.SupportTicketMapping.Create() }, supportEntFields)
}

func (s *postgresEntStateStore) ListRuntimeOperations(ctx context.Context) ([]map[string]any, error) {
	rows, err := loadEventRows(ctx, s.client.RuntimeOperation.Query().Order(controlplaneent.Asc(runtimeoperation.FieldCreatedAt, runtimeoperation.FieldID)).All, runtimeOpEntFields)
	return rows, err
}

func (s *postgresEntStateStore) GetRuntimeOperation(ctx context.Context, id string) (map[string]any, bool, error) {
	entity, err := s.client.RuntimeOperation.Get(ctx, id)
	if controlplaneent.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return recordFromEnt(entity, runtimeOpEntFields), true, nil
}

func (s *postgresEntStateStore) PageRuntimeOperations(ctx context.Context, page runtimeOperationQuery) (tablePage, error) {
	query := s.client.RuntimeOperation.Query()
	if page.AccountID != "" {
		query.Where(runtimeoperation.AccountIDEQ(page.AccountID))
	}
	if page.WorkspaceID != "" {
		query.Where(runtimeoperation.WorkspaceIDEQ(page.WorkspaceID))
	}
	if page.Action != "" {
		query.Where(runtimeoperation.ActionEQ(page.Action))
	}
	if len(page.Statuses) > 0 {
		query.Where(runtimeoperation.StatusIn(page.Statuses...))
	}
	if len(page.ExcludedStatuses) > 0 {
		query.Where(runtimeoperation.StatusNotIn(page.ExcludedStatuses...))
	}
	if page.PeriodStart != "" {
		query.Where(runtimeoperation.PeriodStartEQ(page.PeriodStart))
	}
	total, err := query.Clone().Count(ctx)
	if err != nil {
		return tablePage{}, err
	}
	rows, err := loadEventRows(ctx, query.Order(controlplaneent.Asc(runtimeoperation.FieldCreatedAt, runtimeoperation.FieldID)).Offset(page.Offset).Limit(page.Limit).All, runtimeOpEntFields)
	if err != nil {
		return tablePage{}, err
	}
	return tablePage{Items: rows, Total: total}, nil
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

func (s *postgresEntStateStore) ReserveProductionE2EAttempt(ctx context.Context, claim productionE2EAttemptClaim) (map[string]any, error) {
	now := time.Now().UTC()
	err := s.client.ProductionE2ERecord.Create().
		SetID(claim.ID).
		SetAccountID(claim.AccountID).
		SetWorkspaceID(claim.WorkspaceID).
		SetStatus("attempted").
		SetResult(claim.Binding).
		SetReason(recoveredWorkspaceE2EAttemptReason).
		SetURL(claim.URL).
		SetCreatedAt(now).
		SetUpdatedAt(now).
		Exec(ctx)
	if controlplaneent.IsConstraintError(err) {
		return nil, errProductionE2EAttemptAlreadyExists
	}
	if err != nil {
		return nil, err
	}
	record, found, err := s.GetProductionE2EAttempt(ctx, claim.ID)
	if err != nil || !found {
		return nil, err
	}
	return record, nil
}

func (s *postgresEntStateStore) GetProductionE2EAttempt(ctx context.Context, id string) (map[string]any, bool, error) {
	entity, err := s.client.ProductionE2ERecord.Get(ctx, id)
	if controlplaneent.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return recordFromEnt(entity, productionE2EEntFields), true, nil
}

func (s *postgresEntStateStore) CompleteProductionE2EAttempt(ctx context.Context, id, binding string) (map[string]any, error) {
	updated, err := s.client.ProductionE2ERecord.Update().Where(
		productione2erecord.IDEQ(id),
		productione2erecord.ReasonEQ(recoveredWorkspaceE2EAttemptReason),
		productione2erecord.ResultEQ(binding),
		productione2erecord.StatusEQ("attempted"),
	).SetStatus("passed").SetUpdatedAt(time.Now().UTC()).Save(ctx)
	if err != nil {
		return nil, err
	}
	record, found, err := s.GetProductionE2EAttempt(ctx, id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errProductionE2EAttemptNotFound
	}
	if stringValue(record["reason"]) != recoveredWorkspaceE2EAttemptReason || stringValue(record["result"]) != binding {
		return nil, errProductionE2EAttemptBindingMismatch
	}
	if updated == 0 && stringValue(record["status"]) != "passed" {
		return nil, errProductionE2EAttemptBindingMismatch
	}
	return record, nil
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

func (s *postgresEntStateStore) ArchiveState(ctx context.Context) (map[string]any, error) {
	auditEvents, err := loadEventRows(ctx, s.client.ArchivedAdminAuditEvent.Query().All, auditEntFields)
	if err != nil {
		return nil, err
	}
	e2eRecords, err := loadEventRows(ctx, s.client.ProductionE2ERecord.Query().All, productionE2EEntFields)
	if err != nil {
		return nil, err
	}
	return map[string]any{
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
	if updatedAt, ok := fieldValue(value, "UpdatedAt").(time.Time); ok && !updatedAt.IsZero() {
		row["updatedAt"] = updatedAt.UTC().Format(time.RFC3339Nano)
	}
	var workspaceBillingJSON string
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
		if field.Kind == "workspace_billing_json" {
			workspaceBillingJSON = stringValue(raw)
			continue
		}
		if field.Kind == "json_text" {
			var decoded any
			if text := stringValue(raw); text != "" && json.Unmarshal([]byte(text), &decoded) == nil {
				setPath(row, field.Path, decoded)
			}
			continue
		}
		if isZero(raw) && field.Kind != "bool" {
			continue
		}
		setPath(row, field.Path, raw)
	}
	if billing, err := decodeWorkspaceBillingState(workspaceBillingJSON, row); err == nil {
		for key, value := range billing {
			row[key] = value
		}
	}
	return row
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
			if encoded, err := encodeMonthlyBillingState(row); err == nil {
				callSetter(builder, field.Setter, string(encoded))
			}
		case "workspace_billing_json":
			if encoded, err := encodeWorkspaceBillingState(row); err == nil {
				callSetter(builder, field.Setter, encoded)
			}
		case "json_text":
			if encoded, err := json.Marshal(value); err == nil {
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

func encodeMonthlyBillingState(row map[string]any) (string, error) {
	billing := map[string]any{}
	for _, key := range monthlyBillingStateKeys {
		if value, ok := row[key]; ok {
			billing[key] = value
		}
	}
	encoded, err := json.Marshal(billing)
	return string(encoded), err
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
