package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/lib/pq"

	controlplaneent "opl-cloud/services/control-plane/ent"
	"opl-cloud/services/control-plane/ent/account"
	"opl-cloud/services/control-plane/ent/adminauditevent"
	"opl-cloud/services/control-plane/ent/announcement"
	"opl-cloud/services/control-plane/ent/announcementread"
	"opl-cloud/services/control-plane/ent/billingreconciliation"
	"opl-cloud/services/control-plane/ent/computeallocation"
	"opl-cloud/services/control-plane/ent/productione2erecord"
	"opl-cloud/services/control-plane/ent/runtimeoperation"
	"opl-cloud/services/control-plane/ent/session"
	"opl-cloud/services/control-plane/ent/storageattachment"
	"opl-cloud/services/control-plane/ent/storagevolume"
	"opl-cloud/services/control-plane/ent/supportticketmapping"
	"opl-cloud/services/control-plane/ent/user"
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

func workspaceBillingJSONField(entityField, setter string) entRecordField {
	return entRecordField{EntityField: entityField, Setter: setter, Kind: "workspace_billing_json"}
}

func jsonTextField(entityField, setter string, path ...string) entRecordField {
	return entRecordField{EntityField: entityField, Setter: setter, Path: path, Kind: "json_text"}
}

type workspaceBillingState struct {
	AutoRenew           bool   `json:"autoRenew"`
	AuthorizedBy        string `json:"authorizedBy"`
	AuthorizedAt        string `json:"authorizedAt"`
	PackageID           string `json:"packageId"`
	StorageGB           int64  `json:"storageGb"`
	PriceVersion        string `json:"priceVersion"`
	Currency            string `json:"currency"`
	BillingUnit         string `json:"billingUnit"`
	ComputeUSDMicros    int64  `json:"computeUsdMicros"`
	StorageUSDMicros    int64  `json:"storageUsdMicros"`
	TotalUSDMicros      int64  `json:"totalUsdMicros"`
	PeriodStart         string `json:"periodStart"`
	PaidThrough         string `json:"paidThrough"`
	NextRenewalAt       string `json:"nextRenewalAt"`
	BillingAnchorDay    int64  `json:"billingAnchorDay"`
	RenewalStatus       string `json:"renewalStatus"`
	ComputeAllocationID string `json:"computeAllocationId"`
	StorageID           string `json:"storageId"`
	ManualReviewReason  string `json:"-"`
}

var workspaceBillingStateRequiredKeys = []string{
	"autoRenew", "authorizedBy", "authorizedAt", "packageId", "storageGb", "priceVersion", "currency", "billingUnit",
	"computeUsdMicros", "storageUsdMicros", "totalUsdMicros", "periodStart", "paidThrough",
	"nextRenewalAt", "billingAnchorDay", "renewalStatus", "computeAllocationId", "storageId",
}

var workspaceBillingStateExclusiveKeys = []string{
	"authorizedBy", "authorizedAt", "storageGb", "priceVersion", "currency", "billingUnit",
	"computeUsdMicros", "storageUsdMicros", "totalUsdMicros", "periodStart", "paidThrough", "nextRenewalAt", "billingAnchorDay",
}

const workspaceBillingLegacyMismatch = "legacy_billing_state_mismatch"

func validateWorkspaceBillingState(row map[string]any) error {
	_, _, err := normalizeWorkspaceBillingStateForWorkspace(row, row)
	return err
}

func lockRowForUpdate(selector *entsql.Selector) {
	if selector.Dialect() == dialect.Postgres {
		selector.ForUpdate()
	}
}

func mergeWorkspaceForSave(existing, incoming map[string]any) (map[string]any, error) {
	row := cloneMap(incoming)
	if existing == nil {
		return row, nil
	}
	for _, key := range []string{"accountId", "ownerAccountId", "ownerUserId"} {
		if current := stringValue(existing[key]); current != "" && stringValue(row[key]) != current {
			return nil, errIdempotencyConflict
		}
	}
	existingBilling := workspaceAcceptedBillingState(existing)
	incomingBilling := workspaceAcceptedBillingState(row)
	if existingBilling != nil && incomingBilling != nil {
		for _, key := range []string{"computeAllocationId", "storageId", "packageId", "priceVersion"} {
			if existingBilling[key] != incomingBilling[key] {
				return nil, errIdempotencyConflict
			}
		}
	}
	existingAutoRenew, existingHasAutoRenew := existing["autoRenew"].(bool)
	incomingAutoRenew, incomingHasAutoRenew := row["autoRenew"].(bool)
	if existingHasAutoRenew && !existingAutoRenew && incomingHasAutoRenew && incomingAutoRenew {
		if existingBilling == nil {
			return nil, errIdempotencyConflict
		}
		for key, value := range existingBilling {
			row[key] = value
		}
	}
	if !workspaceLifecycleInactive(existing) || workspaceLifecycleInactive(row) {
		return row, nil
	}
	for _, key := range []string{"state", "status", "currentComputeAllocationId", "currentAttachmentId"} {
		row[key] = existing[key]
	}
	if existingBilling != nil {
		for key, value := range existingBilling {
			row[key] = value
		}
	}
	return row, nil
}

func workspaceLifecycleInactive(row map[string]any) bool {
	switch firstNonEmpty(stringValue(row["state"]), stringValue(row["status"])) {
	case "suspended", "stopped", "data_deleted", "unrecoverable", "storage_missing", "destroyed":
		return true
	default:
		return false
	}
}

func normalizeWorkspaceBillingStateForWorkspace(row, workspace map[string]any) (workspaceBillingState, bool, error) {
	currentComputeID := stringValue(workspace["currentComputeAllocationId"])
	state, present, err := normalizeWorkspaceBillingState(row, currentComputeID, stringValue(workspace["storageId"]), stringValue(workspace["ownerUserId"]))
	if err != nil || !present || state.RenewalStatus == "manual_review" || currentComputeID != "" {
		return state, present, err
	}
	if state.AutoRenew {
		return workspaceBillingState{}, false, errInvalidWorkspaceBillingState
	}
	switch firstNonEmpty(stringValue(workspace["state"]), stringValue(workspace["status"])) {
	case "suspended", "data_deleted", "unrecoverable":
		return state, present, nil
	default:
		return workspaceBillingState{}, false, errInvalidWorkspaceBillingState
	}
}

func normalizeWorkspaceBillingState(row map[string]any, expectedComputeID, expectedStorageID, expectedOwnerID string) (workspaceBillingState, bool, error) {
	_, hasRenewalStatus := row["renewalStatus"]
	_, hasAutoRenew := row["autoRenew"]
	_, hasPriceVersion := row["priceVersion"]
	if !hasRenewalStatus && !hasAutoRenew && !hasPriceVersion {
		return workspaceBillingState{}, false, nil
	}
	if row["renewalStatus"] == "manual_review" {
		for _, key := range workspaceBillingStateExclusiveKeys {
			if _, ok := row[key]; ok {
				return workspaceBillingState{}, false, errInvalidWorkspaceBillingState
			}
		}
		autoRenew, validAutoRenew := row["autoRenew"].(bool)
		reason, validReason := row["manualReviewReason"].(string)
		if !validAutoRenew || autoRenew || !validReason || reason != workspaceBillingLegacyMismatch {
			return workspaceBillingState{}, false, errInvalidWorkspaceBillingState
		}
		return workspaceBillingState{RenewalStatus: "manual_review", ManualReviewReason: reason}, true, nil
	}
	for _, key := range workspaceBillingStateRequiredKeys {
		if _, ok := row[key]; !ok {
			return workspaceBillingState{}, false, fmt.Errorf("%w: missing %s", errInvalidWorkspaceBillingState, key)
		}
	}
	autoRenew, validAutoRenew := row["autoRenew"].(bool)
	authorizedBy, validAuthorizedBy := row["authorizedBy"].(string)
	authorizedAt, validAuthorizedAt := row["authorizedAt"].(string)
	packageID, validPackageID := row["packageId"].(string)
	priceVersion, validPriceVersion := row["priceVersion"].(string)
	currency, validCurrency := row["currency"].(string)
	billingUnit, validBillingUnit := row["billingUnit"].(string)
	periodStartText, validPeriodStart := row["periodStart"].(string)
	paidThroughText, validPaidThrough := row["paidThrough"].(string)
	nextRenewalText, validNextRenewal := row["nextRenewalAt"].(string)
	renewalStatus, validRenewalStatus := row["renewalStatus"].(string)
	computeID, validComputeID := row["computeAllocationId"].(string)
	storageID, validStorageID := row["storageId"].(string)
	if !validAutoRenew || !validAuthorizedBy || !validAuthorizedAt || !validPackageID || !validPriceVersion || !validCurrency || !validBillingUnit ||
		!validPeriodStart || !validPaidThrough || !validNextRenewal || !validRenewalStatus || !validComputeID || !validStorageID {
		return workspaceBillingState{}, false, errInvalidWorkspaceBillingState
	}
	storageGB, validStorageGB := requiredPositiveInteger(row, "storageGb")
	computeUSDMicros, validComputePrice := requiredPositiveInteger(row, "computeUsdMicros")
	storageUSDMicros, validStoragePrice := requiredPositiveInteger(row, "storageUsdMicros")
	totalUSDMicros, validTotal := requiredPositiveInteger(row, "totalUsdMicros")
	billingAnchorDay, validAnchor := requiredPositiveInteger(row, "billingAnchorDay")
	if !validStorageGB || !validComputePrice || !validStoragePrice || !validTotal || !validAnchor || billingAnchorDay > 31 {
		return workspaceBillingState{}, false, errInvalidWorkspaceBillingState
	}
	quote, err := workspacePricingPreview(defaultPricingCatalog(), map[string]any{"packageId": packageID, "sizeGb": storageGB})
	if err != nil {
		return workspaceBillingState{}, false, errInvalidWorkspaceBillingState
	}
	expectedCompute, computeOK := requiredPositiveInteger(mapField(quote, "compute"), "chargeUsdMicros")
	expectedStorage, storageOK := requiredPositiveInteger(mapField(quote, "storage"), "chargeUsdMicros")
	expectedTotal, totalOK := requiredPositiveInteger(quote, "totalChargeUsdMicros")
	checkedTotal, sumOK := checkedAddInt64(computeUSDMicros, storageUSDMicros)
	if !computeOK || !storageOK || !totalOK || !sumOK || computeUSDMicros != expectedCompute || storageUSDMicros != expectedStorage || totalUSDMicros != expectedTotal || totalUSDMicros != checkedTotal ||
		priceVersion != pricingCatalogVersion || currency != pricingCurrency || billingUnit != pricingBillingUnit {
		return workspaceBillingState{}, false, errInvalidWorkspaceBillingState
	}
	periodStart, startErr := time.Parse(time.RFC3339, periodStartText)
	paidThrough, paidErr := time.Parse(time.RFC3339, paidThroughText)
	nextRenewal, nextErr := time.Parse(time.RFC3339, nextRenewalText)
	if startErr != nil || paidErr != nil || nextErr != nil || !paidThrough.After(periodStart) || !nextRenewal.Equal(paidThrough.Add(-24*time.Hour)) {
		return workspaceBillingState{}, false, errInvalidWorkspaceBillingState
	}
	if (renewalStatus != "active" && renewalStatus != "expired_unpaid") || strings.TrimSpace(computeID) == "" || strings.TrimSpace(storageID) == "" ||
		expectedComputeID != "" && computeID != expectedComputeID || expectedStorageID == "" || storageID != expectedStorageID {
		return workspaceBillingState{}, false, errInvalidWorkspaceBillingState
	}
	if renewalStatus == "expired_unpaid" && autoRenew {
		return workspaceBillingState{}, false, errInvalidWorkspaceBillingState
	}
	if autoRenew && (authorizedBy == "" || authorizedAt == "") || authorizedBy != "" && authorizedBy != expectedOwnerID || (authorizedBy == "") != (authorizedAt == "") {
		return workspaceBillingState{}, false, errInvalidWorkspaceBillingState
	}
	if authorizedAt != "" {
		parsed, err := time.Parse(time.RFC3339, authorizedAt)
		if err != nil {
			return workspaceBillingState{}, false, errInvalidWorkspaceBillingState
		}
		authorizedAt = parsed.UTC().Format(time.RFC3339Nano)
	}
	if _, ok := row["manualReviewReason"]; ok {
		return workspaceBillingState{}, false, errInvalidWorkspaceBillingState
	}
	return workspaceBillingState{
		AutoRenew: autoRenew, AuthorizedBy: authorizedBy, AuthorizedAt: authorizedAt,
		PackageID: packageID, StorageGB: storageGB, PriceVersion: priceVersion, Currency: currency, BillingUnit: billingUnit,
		ComputeUSDMicros: computeUSDMicros, StorageUSDMicros: storageUSDMicros, TotalUSDMicros: totalUSDMicros,
		PeriodStart: periodStart.UTC().Format(time.RFC3339Nano), PaidThrough: paidThrough.UTC().Format(time.RFC3339Nano), NextRenewalAt: nextRenewal.UTC().Format(time.RFC3339Nano),
		BillingAnchorDay: billingAnchorDay, RenewalStatus: renewalStatus, ComputeAllocationID: computeID, StorageID: storageID,
	}, true, nil
}

func (state workspaceBillingState) record() map[string]any {
	if state.RenewalStatus == "manual_review" {
		return map[string]any{"autoRenew": false, "renewalStatus": state.RenewalStatus, "manualReviewReason": state.ManualReviewReason}
	}
	row := map[string]any{
		"autoRenew": state.AutoRenew, "authorizedBy": state.AuthorizedBy, "authorizedAt": state.AuthorizedAt,
		"packageId": state.PackageID, "storageGb": state.StorageGB,
		"priceVersion": state.PriceVersion, "currency": state.Currency, "billingUnit": state.BillingUnit,
		"computeUsdMicros": state.ComputeUSDMicros, "storageUsdMicros": state.StorageUSDMicros, "totalUsdMicros": state.TotalUSDMicros,
		"periodStart": state.PeriodStart, "paidThrough": state.PaidThrough, "nextRenewalAt": state.NextRenewalAt,
		"billingAnchorDay": state.BillingAnchorDay, "renewalStatus": state.RenewalStatus,
		"computeAllocationId": state.ComputeAllocationID, "storageId": state.StorageID,
	}
	return row
}

func encodeWorkspaceBillingState(row map[string]any) (string, error) {
	state, present, err := normalizeWorkspaceBillingStateForWorkspace(row, row)
	if err != nil {
		return "", err
	}
	if !present {
		return "{}", nil
	}
	if state.RenewalStatus == "manual_review" {
		encoded, err := json.Marshal(state.record())
		return string(encoded), err
	}
	encoded, err := json.Marshal(state)
	return string(encoded), err
}

func decodeWorkspaceBillingState(encoded string, workspace map[string]any) (map[string]any, error) {
	if strings.TrimSpace(encoded) == "" {
		return nil, errInvalidWorkspaceBillingState
	}
	if strings.TrimSpace(encoded) == "{}" {
		return nil, nil
	}
	var shape map[string]json.RawMessage
	shapeDecoder := json.NewDecoder(strings.NewReader(encoded))
	if err := shapeDecoder.Decode(&shape); err != nil {
		return nil, err
	}
	if err := ensureJSONEOF(shapeDecoder); err != nil {
		return nil, err
	}
	if len(shape) == 3 && shape["autoRenew"] != nil && shape["renewalStatus"] != nil && shape["manualReviewReason"] != nil {
		var marker map[string]any
		if err := json.Unmarshal([]byte(encoded), &marker); err != nil {
			return nil, err
		}
		normalized, _, err := normalizeWorkspaceBillingState(marker, "", "", "")
		if err != nil {
			return nil, err
		}
		return normalized.record(), nil
	}
	allowed := map[string]bool{}
	for _, key := range workspaceBillingStateRequiredKeys {
		allowed[key] = true
		if _, ok := shape[key]; !ok {
			return nil, fmt.Errorf("%w: missing %s", errInvalidWorkspaceBillingState, key)
		}
	}
	for key := range shape {
		if !allowed[key] {
			return nil, fmt.Errorf("%w: unknown %s", errInvalidWorkspaceBillingState, key)
		}
	}
	var state workspaceBillingState
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return nil, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	normalized, _, err := normalizeWorkspaceBillingStateForWorkspace(state.record(), workspace)
	if err != nil {
		return nil, err
	}
	return normalized.record(), nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errInvalidWorkspaceBillingState
	}
	return nil
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
		intField("WorkspaceAPIKeyID", "SetWorkspaceAPIKeyID", "workspaceApiKeyId"),
		textField("AccessAccount", "SetAccessAccount", "access", "account"),
		textField("AccessUsername", "SetAccessUsername", "access", "username"),
		textField("CredentialStatus", "SetCredentialStatus", "access", "credentialStatus"),
		textField("CredentialVersion", "SetCredentialVersion", "access", "credentialVersion"),
		textField("CredentialSecretRef", "SetCredentialSecretRef", "access", "secretRef"),
		textField("VerificationSlotID", "SetVerificationSlotID", "verificationSlotId"),
		boolField("CustomerProduct", "SetCustomerProduct", "customerProduct"),
		workspaceBillingJSONField("BillingStateJSON", "SetBillingStateJSON"),
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
	operator := stringValue(row["id"]) == "usr-admin" && stringValue(row["accountId"]) == "acct-admin" && normalizeEmail(stringValue(row["email"])) == "admin@medopl.cn" && stringValue(row["role"]) == "admin"
	if stringValue(row["role"]) != "owner" && !operator {
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
	if !validSessionLookupKey(stringValue(row["id"])) {
		return errors.New("invalid_session_id")
	}
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

func (s *postgresEntStateStore) CreateInvitedAccount(ctx context.Context, accountRow, userRow, organizationRow, membershipRow map[string]any) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	rollback := func(err error) error {
		_ = tx.Rollback()
		return err
	}
	replayAfterConstraint := func(conflict error) error {
		_ = tx.Rollback()
		readback, err := s.client.Tx(ctx)
		if err != nil {
			return err
		}
		finish := func(err error) error {
			_ = readback.Rollback()
			return err
		}
		client := readback.Client()
		accounts, err := loadRecordSet(ctx, client.Account.Query().All, accountEntFields)
		if err != nil {
			return finish(err)
		}
		users, err := loadRecordSet(ctx, client.User.Query().All, userEntFields)
		if err != nil {
			return finish(err)
		}
		organizations, err := loadRecordSet(ctx, client.Organization.Query().All, organizationEntFields)
		if err != nil {
			return finish(err)
		}
		memberships, err := loadRecordSet(ctx, client.Membership.Query().All, membershipEntFields)
		if err != nil {
			return finish(err)
		}
		if accounts[stringValue(accountRow["id"])] == nil || users[stringValue(userRow["id"])] == nil ||
			organizations[stringValue(organizationRow["id"])] == nil || memberships[stringValue(membershipRow["id"])] == nil ||
			stageInvitedAccount(accounts, users, organizations, memberships, accountRow, userRow, organizationRow, membershipRow) != nil {
			return finish(conflict)
		}
		return finish(nil)
	}
	client := tx.Client()
	accountID := stringValue(accountRow["id"])
	accountExists := true
	if _, err := client.Account.UpdateOneID(accountID).Save(ctx); err != nil {
		if controlplaneent.IsNotFound(err) {
			accountExists = false
		} else {
			return rollback(err)
		}
	}
	accounts, err := loadRecordSet(ctx, client.Account.Query().All, accountEntFields)
	if err != nil {
		return rollback(err)
	}
	users, err := loadRecordSet(ctx, client.User.Query().All, userEntFields)
	if err != nil {
		return rollback(err)
	}
	organizations, err := loadRecordSet(ctx, client.Organization.Query().All, organizationEntFields)
	if err != nil {
		return rollback(err)
	}
	memberships, err := loadRecordSet(ctx, client.Membership.Query().All, membershipEntFields)
	if err != nil {
		return rollback(err)
	}

	organizationID := stringValue(organizationRow["id"])
	_, organizationExists := organizations[organizationID]
	userID := stringValue(userRow["id"])
	_, userExists := users[userID]
	membershipID := stringValue(membershipRow["id"])
	_, membershipExists := memberships[membershipID]
	if err := stageInvitedAccount(accounts, users, organizations, memberships, accountRow, userRow, organizationRow, membershipRow); err != nil {
		return rollback(err)
	}

	if accountExists {
		if _, err := client.Account.UpdateOneID(accountID).
			SetOwnerUserID(stringValue(accounts[accountID]["ownerUserId"])).
			SetSub2apiUserID(int64(numberField(accounts[accountID], "sub2apiUserId", 0))).
			Save(ctx); err != nil {
			if controlplaneent.IsConstraintError(err) {
				return replayAfterConstraint(errSub2APIAccountMappingConflict)
			}
			return rollback(err)
		}
	} else if err := saveRecord(ctx, accountID, accounts[accountID], client.Account.Create(), accountEntFields); err != nil {
		if controlplaneent.IsConstraintError(err) {
			return replayAfterConstraint(errSub2APIAccountMappingConflict)
		}
		return rollback(err)
	}
	if !userExists {
		if err := saveRecord(ctx, userID, users[userID], client.User.Create(), userEntFields); err != nil {
			if controlplaneent.IsConstraintError(err) {
				return replayAfterConstraint(errUserExists)
			}
			return rollback(err)
		}
	}
	if !organizationExists {
		if err := saveRecord(ctx, organizationID, organizations[organizationID], client.Organization.Create(), organizationEntFields); err != nil {
			if controlplaneent.IsConstraintError(err) {
				return replayAfterConstraint(err)
			}
			return rollback(err)
		}
	}
	if !membershipExists {
		if err := saveRecord(ctx, membershipID, memberships[membershipID], client.Membership.Create(), membershipEntFields); err != nil {
			if controlplaneent.IsConstraintError(err) {
				return replayAfterConstraint(err)
			}
			return rollback(err)
		}
	}
	return tx.Commit()
}

func (s *postgresEntStateStore) ApplyUserLifecycle(ctx context.Context, user map[string]any) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	rollback := func(err error) error {
		_ = tx.Rollback()
		return err
	}
	client := tx.Client()
	userID := stringValue(user["id"])
	userUpdate := client.User.UpdateOneID(userID)
	setRecordFieldsWithEmptyText(userUpdate, user, userEntFields, true)
	if err := execCreate(ctx, userUpdate); err != nil {
		if controlplaneent.IsNotFound(err) {
			return rollback(errUserNotFound)
		}
		return rollback(err)
	}
	if _, err := client.Session.Delete().Where(session.UserID(userID)).Exec(ctx); err != nil {
		return rollback(err)
	}
	if stringValue(user["role"]) == "owner" {
		accountID := stringValue(user["accountId"])
		computes, err := client.ComputeAllocation.Query().Where(computeallocation.AccountID(accountID)).All(ctx)
		if err != nil {
			return rollback(err)
		}
		sort.Slice(computes, func(i, j int) bool { return computes[i].ID < computes[j].ID })
		for _, compute := range computes {
			current, err := client.ComputeAllocation.UpdateOneID(compute.ID).Save(ctx)
			if controlplaneent.IsNotFound(err) {
				continue
			}
			if err != nil {
				return rollback(err)
			}
			row := recordFromEnt(current, computeEntFields)
			if row["autoRenew"] != true {
				continue
			}
			row["autoRenew"] = false
			billingState, err := encodeMonthlyBillingState(row)
			if err != nil {
				return rollback(err)
			}
			if _, err := client.ComputeAllocation.UpdateOneID(compute.ID).SetBillingStateJSON(billingState).Save(ctx); err != nil {
				return rollback(err)
			}
		}
		storages, err := client.StorageVolume.Query().Where(storagevolume.AccountID(accountID)).All(ctx)
		if err != nil {
			return rollback(err)
		}
		sort.Slice(storages, func(i, j int) bool { return storages[i].ID < storages[j].ID })
		for _, storage := range storages {
			current, err := client.StorageVolume.UpdateOneID(storage.ID).Save(ctx)
			if controlplaneent.IsNotFound(err) {
				continue
			}
			if err != nil {
				return rollback(err)
			}
			row := recordFromEnt(current, storageEntFields)
			if row["autoRenew"] != true {
				continue
			}
			row["autoRenew"] = false
			billingState, err := encodeMonthlyBillingState(row)
			if err != nil {
				return rollback(err)
			}
			if _, err := client.StorageVolume.UpdateOneID(storage.ID).SetBillingStateJSON(billingState).Save(ctx); err != nil {
				return rollback(err)
			}
		}
		workspaces, err := client.Workspace.Query().Where(workspace.OwnerUserID(userID)).All(ctx)
		if err != nil {
			return rollback(err)
		}
		sort.Slice(workspaces, func(i, j int) bool { return workspaces[i].ID < workspaces[j].ID })
		for _, entity := range workspaces {
			base := recordFromEnt(entity, workspaceEntFields)
			billing, err := decodeWorkspaceBillingState(entity.BillingStateJSON, base)
			if err != nil || billing == nil || billing["autoRenew"] != true {
				continue
			}
			billing["ownerUserId"], billing["currentComputeAllocationId"] = entity.OwnerUserID, entity.CurrentComputeAllocationID
			billing["state"], billing["status"] = entity.State, entity.Status
			billing["autoRenew"] = false
			billingState, err := encodeWorkspaceBillingState(billing)
			if err != nil {
				return rollback(err)
			}
			if _, err := client.Workspace.UpdateOneID(entity.ID).SetBillingStateJSON(billingState).Save(ctx); err != nil {
				return rollback(err)
			}
		}
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
	if stringValue(row["role"]) != "owner" {
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

func (s *postgresEntStateStore) SetResourceAutoRenew(ctx context.Context, resourceType, id, accountID string, autoRenew bool) error {
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
		entity, err := client.ComputeAllocation.UpdateOneID(id).Save(ctx)
		if err != nil {
			return rollback(err)
		}
		current = recordFromEnt(entity, computeEntFields)
	case "storage":
		entity, err := client.StorageVolume.UpdateOneID(id).Save(ctx)
		if err != nil {
			return rollback(err)
		}
		current = recordFromEnt(entity, storageEntFields)
	default:
		return rollback(errors.New("invalid_billing_resource_type"))
	}
	if stringValue(current["accountId"]) != accountID {
		return rollback(errIdempotencyConflict)
	}
	current["autoRenew"] = autoRenew
	billingState, err := encodeMonthlyBillingState(current)
	if err != nil {
		return rollback(err)
	}
	if resourceType == "compute" {
		_, err = client.ComputeAllocation.UpdateOneID(id).SetBillingStateJSON(billingState).Save(ctx)
	} else {
		_, err = client.StorageVolume.UpdateOneID(id).SetBillingStateJSON(billingState).Save(ctx)
	}
	if err != nil {
		return rollback(err)
	}
	return tx.Commit()
}

func (s *postgresEntStateStore) ClaimResourceBillingOperation(ctx context.Context, resourceType string, row map[string]any) (map[string]any, bool, error) {
	id, operationID := stringValue(row["id"]), stringValue(row["billingOperationId"])
	if id == "" || operationID == "" {
		return nil, false, errors.New("billing_operation_identity_required")
	}
	if resourceType != "compute" && resourceType != "storage" {
		return nil, false, errors.New("invalid_billing_resource_type")
	}
	if !monthlyPriceSnapshotAvailable(row) {
		return nil, false, errMonthlyPriceSnapshotUnavailable
	}
	for range 4 {
		tx, err := s.client.Tx(ctx)
		if err != nil {
			return nil, false, err
		}
		rollback := func(err error) (map[string]any, bool, error) {
			_ = tx.Rollback()
			return nil, false, err
		}
		client := tx.Client()
		var existing map[string]any
		switch resourceType {
		case "compute":
			entity, lockErr := client.ComputeAllocation.UpdateOneID(id).Save(ctx)
			if lockErr == nil {
				existing = recordFromEnt(entity, computeEntFields)
			} else if !controlplaneent.IsNotFound(lockErr) {
				return rollback(lockErr)
			}
		case "storage":
			entity, lockErr := client.StorageVolume.UpdateOneID(id).Save(ctx)
			if lockErr == nil {
				existing = recordFromEnt(entity, storageEntFields)
			} else if !controlplaneent.IsNotFound(lockErr) {
				return rollback(lockErr)
			}
		}
		if existing == nil {
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
			if err := tx.Commit(); err != nil {
				return nil, false, err
			}
			return cloneMap(row), true, nil
		}
		currentOperationID := stringValue(existing["billingOperationId"])
		if currentOperationID == operationID {
			if !billingOperationIdentityMatches(existing, row) {
				return rollback(errIdempotencyConflict)
			}
			_ = tx.Rollback()
			return cloneMap(existing), false, nil
		}
		if stringValue(row["billingStatus"]) == "renewal_pending" && existing["autoRenew"] != true {
			_ = tx.Rollback()
			return cloneMap(existing), false, nil
		}
		if billingOperationInProgress(stringValue(existing["billingStatus"])) {
			_ = tx.Rollback()
			return cloneMap(existing), false, errBillingOperationInProgress
		}
		claimed := preserveResourceAutoRenew(existing, mergeMaps(existing, row))
		if _, confirmationExists := row["sub2apiChargeConfirmation"]; !confirmationExists {
			delete(claimed, "sub2apiChargeConfirmation")
		}
		if lastReceiptID, reset := row["lastReceiptId"].(string); reset && lastReceiptID == "" {
			claimed["lastReceiptId"] = ""
		}
		if resourceType == "compute" {
			builder := client.ComputeAllocation.UpdateOneID(id)
			setRecordFields(builder, claimed, computeEntFields)
			err = execCreate(ctx, builder)
		} else {
			builder := client.StorageVolume.UpdateOneID(id)
			setRecordFields(builder, claimed, storageEntFields)
			err = execCreate(ctx, builder)
		}
		if err != nil {
			return rollback(err)
		}
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return claimed, true, nil
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
	if err := validateWorkspaceBillingState(row); err != nil {
		return err
	}
	row = cloneMap(row)
	if _, ok := row["customerProduct"]; !ok {
		row["customerProduct"] = true
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := saveWorkspaceRecord(ctx, tx.Client(), row); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *postgresEntStateStore) CompareAndSwapWorkspaceAPIKey(ctx context.Context, workspaceID string, expectedID, newID int64) error {
	if workspaceID == "" || expectedID <= 0 || newID <= 0 {
		return errWorkspaceAPIKeyCASConflict
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	entity, err := tx.Workspace.Query().Where(workspace.IDEQ(workspaceID), lockRowForUpdate).Only(ctx)
	if err != nil {
		if controlplaneent.IsNotFound(err) {
			return errWorkspaceAPIKeyCASConflict
		}
		return err
	}
	if entity.WorkspaceAPIKeyID == newID {
		return tx.Commit()
	}
	if entity.WorkspaceAPIKeyID != expectedID {
		return errWorkspaceAPIKeyCASConflict
	}
	if err := tx.Workspace.UpdateOneID(workspaceID).SetWorkspaceAPIKeyID(newID).Exec(ctx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *postgresEntStateStore) ApplyWorkspaceRenewalIntent(ctx context.Context, update workspaceRenewalIntentCAS) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	entity, err := client.Workspace.Query().Where(workspace.IDEQ(update.WorkspaceID), lockRowForUpdate).Only(ctx)
	if err != nil {
		if controlplaneent.IsNotFound(err) {
			return errWorkspaceRenewalCASConflict
		}
		return err
	}
	current := recordFromEnt(entity, workspaceEntFields)
	currentAutoRenew, validAutoRenew := current["autoRenew"].(bool)
	if stringValue(current["accountId"]) != update.AccountID || stringValue(current["ownerUserId"]) != update.OwnerUserID ||
		stringValue(current["paidThrough"]) != update.ExpectedPaidThrough || !validAutoRenew || currentAutoRenew != update.ExpectedAutoRenew {
		return errWorkspaceRenewalCASConflict
	}
	operationEntities, err := client.RuntimeOperation.Query().Where(runtimeoperation.WorkspaceIDEQ(update.WorkspaceID), lockRowForUpdate).All(ctx)
	if err != nil {
		return err
	}
	operations := make([]map[string]any, 0, len(operationEntities))
	for _, operation := range operationEntities {
		row := recordFromEnt(operation, runtimeOpEntFields)
		if stringValue(row["id"]) == stringValue(update.CommandOperation["id"]) {
			return errWorkspaceRenewalCASConflict
		}
		operations = append(operations, row)
	}
	if runtimeOperationsVersion(operations, update.WorkspaceID) != update.ExpectedOperationsVersion {
		return errWorkspaceRenewalCASConflict
	}
	desired := cloneMap(current)
	desired["autoRenew"], desired["authorizedBy"], desired["authorizedAt"] = update.WorkspacePatch.AutoRenew, update.WorkspacePatch.AuthorizedBy, update.WorkspacePatch.AuthorizedAt
	if err := validateWorkspaceBillingState(desired); err != nil {
		return err
	}
	if err := validateWorkspaceRenewalIntentAudit(update, current); err != nil {
		return err
	}
	auditID := stringValue(update.AuditEvent["id"])
	auditExists := false
	auditEntity, err := client.AdminAuditEvent.Query().Where(adminauditevent.IDEQ(auditID), lockRowForUpdate).Only(ctx)
	if err == nil {
		auditExists = true
		if !workspaceRenewalIntentAuditIdentityMatches(recordFromEnt(auditEntity, auditEntFields), update.AuditEvent) {
			return errIdempotencyConflict
		}
	} else if !controlplaneent.IsNotFound(err) {
		return err
	}
	builder := client.Workspace.UpdateOneID(update.WorkspaceID)
	setRecordFieldsWithEmptyText(builder, desired, workspaceEntFields, true)
	if err := execCreate(ctx, builder); err != nil {
		return err
	}
	command := controlPlaneRecord(update.CommandOperation)
	if err := saveRecord(ctx, stringValue(command["id"]), command, client.RuntimeOperation.Create(), runtimeOpEntFields); err != nil {
		if controlplaneent.IsConstraintError(err) {
			return errWorkspaceRenewalCASConflict
		}
		return err
	}
	if !auditExists {
		audit := controlPlaneRecord(update.AuditEvent)
		if err := saveRecord(ctx, auditID, audit, client.AdminAuditEvent.Create(), auditEntFields); err != nil {
			if controlplaneent.IsConstraintError(err) {
				return errIdempotencyConflict
			}
			return err
		}
	}
	return tx.Commit()
}

func (s *postgresEntStateStore) ClaimWorkspaceLaunch(ctx context.Context, claim workspaceLaunchClaimCAS) error {
	desired, err := decodeWorkspaceLaunchOperation(claim.DesiredOperation)
	if err != nil || desired.AccountID != claim.AccountID {
		return errWorkspaceLaunchCASConflict
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	if _, err := client.Account.Query().Where(account.IDEQ(claim.AccountID), lockRowForUpdate).Only(ctx); err != nil {
		if controlplaneent.IsNotFound(err) {
			return errWorkspaceLaunchCASConflict
		}
		return err
	}
	entities, err := client.RuntimeOperation.Query().Where(runtimeoperation.AccountIDEQ(claim.AccountID), lockRowForUpdate).All(ctx)
	if err != nil {
		return err
	}
	var existing map[string]any
	for _, entity := range entities {
		row := recordFromEnt(entity, runtimeOpEntFields)
		if stringValue(row["id"]) == desired.ID {
			existing = row
		}
		if claim.ExpectedOperationResult == "" && isWorkspaceLaunchAction(stringValue(row["action"])) && !terminalWorkspaceLaunchStatus(stringValue(row["status"])) {
			return errWorkspaceLaunchInProgress
		}
	}
	if claim.ExpectedOperationResult == "" {
		if existing != nil {
			return errWorkspaceLaunchCASConflict
		}
		if err := saveRecord(ctx, desired.ID, controlPlaneRecord(claim.DesiredOperation), client.RuntimeOperation.Create(), runtimeOpEntFields); err != nil {
			if controlplaneent.IsConstraintError(err) {
				return errWorkspaceLaunchCASConflict
			}
			return err
		}
	} else {
		if existing == nil || stringValue(existing["result"]) != claim.ExpectedOperationResult {
			return errWorkspaceLaunchCASConflict
		}
		if !workspaceLaunchClaimIdentityMatches(existing, claim.DesiredOperation) {
			return errIdempotencyConflict
		}
		builder := client.RuntimeOperation.UpdateOneID(desired.ID)
		setRecordFieldsWithEmptyText(builder, claim.DesiredOperation, runtimeOpEntFields, true)
		if err := execCreate(ctx, builder); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *postgresEntStateStore) PersistWorkspaceLaunch(ctx context.Context, update workspaceLaunchPersistCAS) error {
	if _, err := decodeWorkspaceLaunchOperation(update.DesiredOperation); err != nil {
		return errWorkspaceLaunchCASConflict
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	entity, err := client.RuntimeOperation.Query().Where(runtimeoperation.IDEQ(update.OperationID), lockRowForUpdate).Only(ctx)
	if err != nil {
		if controlplaneent.IsNotFound(err) {
			return errWorkspaceLaunchCASConflict
		}
		return err
	}
	current := recordFromEnt(entity, runtimeOpEntFields)
	if stringValue(current["result"]) != update.ExpectedOperationResult || !workspaceLaunchClaimIdentityMatches(current, update.DesiredOperation) {
		return errWorkspaceLaunchCASConflict
	}
	builder := client.RuntimeOperation.UpdateOneID(update.OperationID)
	setRecordFieldsWithEmptyText(builder, update.DesiredOperation, runtimeOpEntFields, true)
	if err := execCreate(ctx, builder); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *postgresEntStateStore) ClaimWorkspaceRenewal(ctx context.Context, claim workspaceRenewalClaimCAS) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	entity, err := client.Workspace.Query().Where(workspace.IDEQ(claim.WorkspaceID), lockRowForUpdate).Only(ctx)
	if err != nil {
		if controlplaneent.IsNotFound(err) {
			return errWorkspaceRenewalCASConflict
		}
		return err
	}
	current := recordFromEnt(entity, workspaceEntFields)
	autoRenew, validAutoRenew := current["autoRenew"].(bool)
	if stringValue(current["accountId"]) != claim.AccountID || stringValue(current["paidThrough"]) != claim.ExpectedPaidThrough || !validAutoRenew || autoRenew != claim.ExpectedAutoRenew {
		return errWorkspaceRenewalCASConflict
	}
	operationEntities, err := client.RuntimeOperation.Query().Where(runtimeoperation.WorkspaceIDEQ(claim.WorkspaceID), lockRowForUpdate).All(ctx)
	if err != nil {
		return err
	}
	operations := make([]map[string]any, 0, len(operationEntities))
	desiredID := stringValue(claim.DesiredOperation["id"])
	var existing map[string]any
	for _, operation := range operationEntities {
		row := recordFromEnt(operation, runtimeOpEntFields)
		operations = append(operations, row)
		if stringValue(row["id"]) == desiredID {
			existing = row
		}
	}
	if runtimeOperationsVersion(operations, claim.WorkspaceID) != claim.ExpectedOperationsVersion {
		return errWorkspaceRenewalCASConflict
	}
	if claim.ExpectedOperationResult == "" {
		if existing != nil {
			return errWorkspaceRenewalCASConflict
		}
		if err := saveRecord(ctx, desiredID, controlPlaneRecord(claim.DesiredOperation), client.RuntimeOperation.Create(), runtimeOpEntFields); err != nil {
			if controlplaneent.IsConstraintError(err) {
				return errWorkspaceRenewalCASConflict
			}
			return err
		}
	} else {
		if existing == nil || stringValue(existing["result"]) != claim.ExpectedOperationResult {
			return errWorkspaceRenewalCASConflict
		}
		if !workspaceRenewalClaimIdentityMatches(existing, claim.DesiredOperation) {
			return errIdempotencyConflict
		}
		builder := client.RuntimeOperation.UpdateOneID(desiredID)
		setRecordFieldsWithEmptyText(builder, claim.DesiredOperation, runtimeOpEntFields, true)
		if err := execCreate(ctx, builder); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *postgresEntStateStore) PersistWorkspaceRenewal(ctx context.Context, update workspaceRenewalPersistCAS) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	var mergedWorkspace map[string]any
	if update.WorkspacePatch != nil {
		if update.WorkspaceID == "" || update.ExpectedWorkspacePaidThrough == "" || update.WorkspaceID != stringValue(update.DesiredOperation["workspaceId"]) {
			return errWorkspaceRenewalCASConflict
		}
		entity, err := client.Workspace.Query().Where(workspace.IDEQ(update.WorkspaceID), lockRowForUpdate).Only(ctx)
		if err != nil {
			return err
		}
		currentWorkspace := recordFromEnt(entity, workspaceEntFields)
		if stringValue(currentWorkspace["paidThrough"]) != update.ExpectedWorkspacePaidThrough ||
			!workspaceRenewalExpectedFieldsMatch(currentWorkspace, update.ExpectedWorkspaceFields) {
			return errWorkspaceRenewalCASConflict
		}
		mergedWorkspace, err = mergeWorkspaceRenewalPatch(currentWorkspace, update.WorkspacePatch)
		if err != nil {
			return err
		}
	} else if update.WorkspaceID != "" || update.ExpectedWorkspacePaidThrough != "" || len(update.ExpectedWorkspaceFields) != 0 {
		return errInvalidWorkspaceRenewalPatch
	}
	entity, err := client.RuntimeOperation.Query().Where(runtimeoperation.IDEQ(update.OperationID), lockRowForUpdate).Only(ctx)
	if err != nil {
		if controlplaneent.IsNotFound(err) {
			return errWorkspaceRenewalCASConflict
		}
		return err
	}
	current := recordFromEnt(entity, runtimeOpEntFields)
	if stringValue(current["result"]) != update.ExpectedOperationResult || stringValue(current["workspaceId"]) != stringValue(update.DesiredOperation["workspaceId"]) ||
		stringValue(current["action"]) != stringValue(update.DesiredOperation["action"]) {
		return errWorkspaceRenewalCASConflict
	}
	if mergedWorkspace != nil {
		if update.WorkspaceID != stringValue(current["workspaceId"]) {
			return errWorkspaceRenewalCASConflict
		}
		builder := client.Workspace.UpdateOneID(update.WorkspaceID)
		setRecordFieldsWithEmptyText(builder, mergedWorkspace, workspaceEntFields, true)
		if err := execCreate(ctx, builder); err != nil {
			return err
		}
	}
	builder := client.RuntimeOperation.UpdateOneID(update.OperationID)
	setRecordFieldsWithEmptyText(builder, update.DesiredOperation, runtimeOpEntFields, true)
	if err := execCreate(ctx, builder); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *postgresEntStateStore) ActivateWorkspace(ctx context.Context, row map[string]any) (map[string]any, error) {
	if err := validateWorkspaceBillingState(row); err != nil {
		return nil, err
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	load := func(entity any, err error, fields []entRecordField) (map[string]any, error) {
		if controlplaneent.IsNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return recordFromEnt(entity, fields), nil
	}
	ownerEntity, ownerErr := client.User.Query().Where(user.IDEQ(stringValue(row["ownerUserId"])), lockRowForUpdate).Only(ctx)
	owner, err := load(ownerEntity, ownerErr, userEntFields)
	if err != nil {
		return nil, err
	}
	computeEntity, computeErr := client.ComputeAllocation.Query().Where(computeallocation.IDEQ(stringValue(row["currentComputeAllocationId"])), lockRowForUpdate).Only(ctx)
	compute, err := load(computeEntity, computeErr, computeEntFields)
	if err != nil {
		return nil, err
	}
	storageEntity, storageErr := client.StorageVolume.Query().Where(storagevolume.IDEQ(stringValue(row["storageId"])), lockRowForUpdate).Only(ctx)
	storage, err := load(storageEntity, storageErr, storageEntFields)
	if err != nil {
		return nil, err
	}
	attachmentEntity, attachmentErr := client.StorageAttachment.Query().Where(storageattachment.IDEQ(stringValue(row["currentAttachmentId"])), lockRowForUpdate).Only(ctx)
	attachment, err := load(attachmentEntity, attachmentErr, attachmentEntFields)
	if err != nil {
		return nil, err
	}
	existingEntity, existingErr := client.Workspace.Query().Where(workspace.IDEQ(stringValue(row["id"])), lockRowForUpdate).Only(ctx)
	existing, err := load(existingEntity, existingErr, workspaceEntFields)
	if err != nil {
		return nil, err
	}
	prepared, err := prepareWorkspaceActivation(row, owner, compute, storage, attachment, existing)
	if err != nil {
		return nil, err
	}
	if _, ok := prepared["customerProduct"]; !ok {
		prepared["customerProduct"] = true
	}
	if err := saveWorkspaceRecord(ctx, client, prepared); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return prepared, nil
}

func saveWorkspaceRecord(ctx context.Context, client *controlplaneent.Client, row map[string]any) error {
	id := stringValue(row["id"])
	if id == "" {
		return errors.New("missing_record_id")
	}
	entity, err := client.Workspace.Query().Where(workspace.IDEQ(id), lockRowForUpdate).Only(ctx)
	if err == nil {
		row, err = mergeWorkspaceForSave(recordFromEnt(entity, workspaceEntFields), row)
		if err != nil {
			return err
		}
		if err := validateWorkspaceBillingState(row); err != nil {
			return err
		}
		builder := client.Workspace.UpdateOneID(id)
		setRecordFieldsWithEmptyText(builder, row, workspaceEntFields, true)
		return execCreate(ctx, builder)
	}
	if !controlplaneent.IsNotFound(err) {
		return err
	}
	if err := saveRecord(ctx, id, row, client.Workspace.Create(), workspaceEntFields); controlplaneent.IsConstraintError(err) {
		return errIdempotencyConflict
	} else {
		return err
	}
}

func (s *postgresEntStateStore) ClaimWorkspaceCreate(ctx context.Context, workspaceRow map[string]any, operation map[string]any) error {
	accountID := firstNonEmpty(stringValue(workspaceRow["accountId"]), stringValue(workspaceRow["ownerAccountId"]))
	workspaceID, operationID := stringValue(workspaceRow["id"]), stringValue(operation["id"])
	if accountID == "" || workspaceID == "" || operationID == "" {
		return errors.New("invalid_workspace_create_claim")
	}
	if stringValue(operation["accountId"]) != accountID || stringValue(operation["workspaceId"]) != workspaceID {
		return errPrimaryWorkspaceExists
	}
	var claim workspaceCreateOperationResult
	if stringValue(operation["action"]) == "workspace.create" {
		var claimErr error
		claim, claimErr = decodeWorkspaceCreateOperation(operation)
		if claimErr != nil || claim.Workspace.ID != workspaceID || claim.Workspace.AccountID != accountID {
			return errPrimaryWorkspaceExists
		}
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	existing, err := tx.RuntimeOperation.Get(ctx, operationID)
	if err == nil {
		if stringValue(operation["action"]) != "workspace.create" || existing.Action != "workspace.create" {
			return errPrimaryWorkspaceExists
		}
		current, currentErr := decodeWorkspaceCreateOperation(recordFromEnt(existing, runtimeOpEntFields))
		persistedEntity, persistedErr := tx.Workspace.Query().Where(workspace.IDEQ(workspaceID), lockRowForUpdate).Only(ctx)
		if controlplaneent.IsNotFound(persistedErr) {
			return errPrimaryWorkspaceExists
		}
		if persistedErr != nil {
			return persistedErr
		}
		persisted := recordFromEnt(persistedEntity, workspaceEntFields)
		if currentErr != nil || !workspaceCreateClaimCompatible(current, claim, persisted) || existing.AccountID != accountID || existing.WorkspaceID != workspaceID {
			return errPrimaryWorkspaceExists
		}
		if existing.Status != "retryable" && (existing.Status != "started" || current.LeaseExpiresAt != nil && current.LeaseExpiresAt.After(time.Now().UTC())) {
			return errPrimaryWorkspaceExists
		}
		_, err := tx.RuntimeOperation.UpdateOneID(existing.ID).
			Where(runtimeoperation.StatusEQ(existing.Status), runtimeoperation.ResultEQ(existing.Result)).
			SetStatus("started").
			SetResult(stringValue(operation["result"])).
			Save(ctx)
		if err != nil {
			if controlplaneent.IsNotFound(err) {
				return errPrimaryWorkspaceExists
			}
			return err
		}
		return tx.Commit()
	}
	if !controlplaneent.IsNotFound(err) {
		return err
	}
	if _, err := tx.Workspace.Query().Where(workspace.Or(workspace.AccountID(accountID), workspace.And(workspace.AccountID(""), workspace.OwnerAccountID(accountID)))).First(ctx); err == nil {
		return errPrimaryWorkspaceExists
	} else if !controlplaneent.IsNotFound(err) {
		return err
	}
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
	if err := validateWorkspaceBillingState(workspace); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := saveWorkspaceRecord(ctx, tx.Client(), workspace); err != nil {
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

func projectTaskSyncHeadIdentityMatches(existing any, row map[string]any) bool {
	entity, ok := existing.(*controlplaneent.ProjectTaskSyncHead)
	return ok && entity.Kind == stringValue(row["kind"]) && entity.OrganizationID == stringValue(row["organizationId"]) && entity.WorkspaceID == stringValue(row["workspaceId"]) && entity.ProjectID == stringValue(row["projectId"]) && entity.LocalAliasID == stringValue(row["localAliasId"])
}

func executionRequestIdentityMatches(existing any, row map[string]any) bool {
	entity, ok := existing.(*controlplaneent.ExecutionRequest)
	return ok && entity.OrganizationID == stringValue(row["organizationId"]) && entity.WorkspaceID == stringValue(row["workspaceId"]) && entity.ProjectID == stringValue(row["projectId"]) && entity.TaskID == stringValue(row["taskId"]) && entity.ActorUserID == stringValue(row["actorUserId"]) && entity.EnvironmentRef == stringValue(row["environmentRef"]) && entity.IdempotencyKey == stringValue(row["idempotencyKey"])
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
