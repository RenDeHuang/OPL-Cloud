package server

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	_ "github.com/lib/pq"
)

const singletonFactID = "default"

//go:embed ent_migrations/*.sql
var controlPlaneMigrations embed.FS

type stateRow = map[string]any
type stateTable = map[string]stateRow

type StateStore interface {
	Load(ctx context.Context) (controlPlaneState, error)
	Save(ctx context.Context, facts controlPlaneState) error
}

type controlPlaneState struct {
	Version     int        `json:"version"`
	Computes    stateTable `json:"computes,omitempty"`
	Storages    stateTable `json:"storages,omitempty"`
	Attachments stateTable `json:"attachments,omitempty"`
	Workspaces  stateTable `json:"workspaces,omitempty"`
	Users       stateTable `json:"users,omitempty"`
	Sessions    stateTable `json:"sessions,omitempty"`
	Orgs        stateTable `json:"orgs,omitempty"`
	Memberships stateTable `json:"memberships,omitempty"`
	Support     stateTable `json:"support,omitempty"`
	Wallets     stateTable `json:"wallets,omitempty"`
	Ledger      []stateRow `json:"ledger,omitempty"`
	WalletTx    []stateRow `json:"walletTx,omitempty"`
	Topups      []stateRow `json:"topups,omitempty"`
	RuntimeOps  []stateRow `json:"runtimeOperations,omitempty"`
	AuditEvents []stateRow `json:"auditEvents,omitempty"`
	Reconcile   stateRow   `json:"billingReconciliation,omitempty"`
}

func StateStoreFromEnv() (StateStore, error) {
	if databaseURL := os.Getenv("DATABASE_URL"); databaseURL != "" {
		return NewPostgresEntStateStore(databaseURL)
	}
	return nil, nil
}

type postgresEntStateStore struct {
	db *sql.DB
}

func NewPostgresEntStateStore(databaseURL string) (StateStore, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, err
	}
	store := &postgresEntStateStore{db: db}
	if err := store.install(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

var postgresFactTables = []string{
	"control_plane_compute_allocations",
	"control_plane_storage_volumes",
	"control_plane_storage_attachments",
	"control_plane_workspaces",
	"control_plane_users",
	"control_plane_sessions",
	"control_plane_organizations",
	"control_plane_memberships",
	"control_plane_support_ticket_mappings",
	"control_plane_wallet_projections",
}

var postgresFactEventTables = []string{
	"control_plane_ledger_projections",
	"control_plane_wallet_transaction_projections",
	"control_plane_manual_topup_projections",
	"control_plane_runtime_operations",
	"control_plane_admin_audit_events",
}

type stateColumn struct {
	Name string
	Path []string
	Kind string
}

var postgresStateColumns = []stateColumn{
	{Name: "owner_account_id", Path: []string{"ownerAccountId"}, Kind: "text"},
	{Name: "owner_user_id", Path: []string{"ownerUserId"}, Kind: "text"},
	{Name: "user_id", Path: []string{"userId"}, Kind: "text"},
	{Name: "email", Path: []string{"email"}, Kind: "text"},
	{Name: "role", Path: []string{"role"}, Kind: "text"},
	{Name: "status", Path: []string{"status"}, Kind: "text"},
	{Name: "password_hash", Path: []string{"passwordHash"}, Kind: "text"},
	{Name: "disabled_at", Path: []string{"disabledAt"}, Kind: "text"},
	{Name: "disabled_by", Path: []string{"disabledBy"}, Kind: "text"},
	{Name: "disabled_reason", Path: []string{"disabledReason"}, Kind: "text"},
	{Name: "deleted_at", Path: []string{"deletedAt"}, Kind: "text"},
	{Name: "deleted_by", Path: []string{"deletedBy"}, Kind: "text"},
	{Name: "delete_reason", Path: []string{"deleteReason"}, Kind: "text"},
	{Name: "organization_id", Path: []string{"organizationId"}, Kind: "text"},
	{Name: "billing_account_id", Path: []string{"billingAccountId"}, Kind: "text"},
	{Name: "name", Path: []string{"name"}, Kind: "text"},
	{Name: "package_id", Path: []string{"packageId"}, Kind: "text"},
	{Name: "provider", Path: []string{"provider"}, Kind: "text"},
	{Name: "provider_resource_id", Path: []string{"providerResourceId"}, Kind: "text"},
	{Name: "provider_request_id", Path: []string{"providerRequestId"}, Kind: "text"},
	{Name: "operation_id", Path: []string{"operationId"}, Kind: "text"},
	{Name: "workspace_id", Path: []string{"workspaceId"}, Kind: "text"},
	{Name: "compute_allocation_id", Path: []string{"computeAllocationId"}, Kind: "text"},
	{Name: "current_compute_allocation_id", Path: []string{"currentComputeAllocationId"}, Kind: "text"},
	{Name: "storage_id", Path: []string{"storageId"}, Kind: "text"},
	{Name: "volume_id", Path: []string{"volumeId"}, Kind: "text"},
	{Name: "attachment_id", Path: []string{"attachmentId"}, Kind: "text"},
	{Name: "current_attachment_id", Path: []string{"currentAttachmentId"}, Kind: "text"},
	{Name: "runtime_id", Path: []string{"runtimeId"}, Kind: "text"},
	{Name: "runtime_service_name", Path: []string{"runtime", "serviceName"}, Kind: "text"},
	{Name: "runtime_service_name_root", Path: []string{"runtimeServiceName"}, Kind: "text"},
	{Name: "service_name", Path: []string{"serviceName"}, Kind: "text"},
	{Name: "url", Path: []string{"url"}, Kind: "text"},
	{Name: "state", Path: []string{"state"}, Kind: "text"},
	{Name: "evidence_id", Path: []string{"evidenceId"}, Kind: "text"},
	{Name: "cvm_instance_id", Path: []string{"cvmInstanceId"}, Kind: "text"},
	{Name: "instance_id", Path: []string{"instanceId"}, Kind: "text"},
	{Name: "node_name", Path: []string{"nodeName"}, Kind: "text"},
	{Name: "machine_name", Path: []string{"machineName"}, Kind: "text"},
	{Name: "mount_path", Path: []string{"mountPath"}, Kind: "text"},
	{Name: "billing_status", Path: []string{"billingStatus"}, Kind: "text"},
	{Name: "hold_id", Path: []string{"holdId"}, Kind: "text"},
	{Name: "hold_release_id", Path: []string{"holdReleaseId"}, Kind: "text"},
	{Name: "ledger_entry_id", Path: []string{"ledgerEntryId"}, Kind: "text"},
	{Name: "wallet_transaction_id", Path: []string{"walletTransactionId"}, Kind: "text"},
	{Name: "settlement_id", Path: []string{"settlementId"}, Kind: "text"},
	{Name: "type", Path: []string{"type"}, Kind: "text"},
	{Name: "resource_id", Path: []string{"resourceId"}, Kind: "text"},
	{Name: "pricing_version", Path: []string{"pricingVersion"}, Kind: "text"},
	{Name: "usage_period_start", Path: []string{"usagePeriodStart"}, Kind: "text"},
	{Name: "usage_period_end", Path: []string{"usagePeriodEnd"}, Kind: "text"},
	{Name: "unit", Path: []string{"unit"}, Kind: "text"},
	{Name: "provider_cost_evidence_ref", Path: []string{"providerCostEvidenceRef"}, Kind: "text"},
	{Name: "currency", Path: []string{"currency"}, Kind: "text"},
	{Name: "source", Path: []string{"source"}, Kind: "text"},
	{Name: "direction", Path: []string{"direction"}, Kind: "text"},
	{Name: "reason", Path: []string{"reason"}, Kind: "text"},
	{Name: "operator_user_id", Path: []string{"operatorUserId"}, Kind: "text"},
	{Name: "actor_user_id", Path: []string{"actorUserId"}, Kind: "text"},
	{Name: "actor_role", Path: []string{"actorRole"}, Kind: "text"},
	{Name: "actor_account_id", Path: []string{"actorAccountId"}, Kind: "text"},
	{Name: "target_account_id", Path: []string{"targetAccountId"}, Kind: "text"},
	{Name: "action", Path: []string{"action"}, Kind: "text"},
	{Name: "resource_kind", Path: []string{"resourceKind"}, Kind: "text"},
	{Name: "ip_address", Path: []string{"ipAddress"}, Kind: "text"},
	{Name: "user_agent", Path: []string{"userAgent"}, Kind: "text"},
	{Name: "result", Path: []string{"result"}, Kind: "text"},
	{Name: "created_at_text", Path: []string{"createdAt"}, Kind: "text"},
	{Name: "csrf", Path: []string{"csrf"}, Kind: "text"},
	{Name: "expires_at", Path: []string{"expiresAt"}, Kind: "text"},
	{Name: "access_token_status", Path: []string{"access", "tokenStatus"}, Kind: "text"},
	{Name: "access_account", Path: []string{"access", "account"}, Kind: "text"},
	{Name: "access_username", Path: []string{"access", "username"}, Kind: "text"},
	{Name: "access_password", Path: []string{"access", "password"}, Kind: "text"},
	{Name: "credential_status", Path: []string{"access", "credentialStatus"}, Kind: "text"},
	{Name: "credential_version", Path: []string{"access", "credentialVersion"}, Kind: "text"},
	{Name: "credential_secret_ref", Path: []string{"access", "secretRef"}, Kind: "text"},
	{Name: "metadata_workspace_id", Path: []string{"metadata", "workspaceId"}, Kind: "text"},
	{Name: "metadata_resource_id", Path: []string{"metadata", "resourceId"}, Kind: "text"},
	{Name: "metadata_settlement_id", Path: []string{"metadata", "settlementId"}, Kind: "text"},
	{Name: "metadata_ledger_entry_id", Path: []string{"metadata", "ledgerEntryId"}, Kind: "text"},
	{Name: "metadata_compute_allocation_id", Path: []string{"metadata", "computeAllocationId"}, Kind: "text"},
	{Name: "metadata_storage_id", Path: []string{"metadata", "storageId"}, Kind: "text"},
	{Name: "price_snapshot_package_id", Path: []string{"priceSnapshot", "packageId"}, Kind: "text"},
	{Name: "price_snapshot_resource_type", Path: []string{"priceSnapshot", "resourceType"}, Kind: "text"},
	{Name: "price_snapshot_currency", Path: []string{"priceSnapshot", "currency"}, Kind: "text"},
	{Name: "price_snapshot_source", Path: []string{"priceSnapshot", "source"}, Kind: "text"},
	{Name: "price_snapshot_sku", Path: []string{"priceSnapshot", "sku"}, Kind: "text"},
	{Name: "guard_status", Path: []string{"guard", "status"}, Kind: "text"},
	{Name: "guard_reason", Path: []string{"guard", "reason"}, Kind: "text"},
	{Name: "message_author", Path: []string{"messageAuthor"}, Kind: "text"},
	{Name: "message_text", Path: []string{"messageText"}, Kind: "text"},
	{Name: "message_created_at", Path: []string{"messageCreatedAt"}, Kind: "text"},
	{Name: "size_gb", Path: []string{"sizeGb"}, Kind: "double"},
	{Name: "cpu", Path: []string{"cpu"}, Kind: "double"},
	{Name: "memory_gb", Path: []string{"memoryGb"}, Kind: "double"},
	{Name: "disk_gb", Path: []string{"diskGb"}, Kind: "double"},
	{Name: "hold_amount_cents", Path: []string{"holdAmountCents"}, Kind: "bigint"},
	{Name: "hold_amount", Path: []string{"holdAmount"}, Kind: "double"},
	{Name: "amount_cents", Path: []string{"amountCents"}, Kind: "bigint"},
	{Name: "balance_cents", Path: []string{"balanceCents"}, Kind: "bigint"},
	{Name: "frozen_cents", Path: []string{"frozenCents"}, Kind: "bigint"},
	{Name: "available_cents", Path: []string{"availableCents"}, Kind: "bigint"},
	{Name: "total_spent_cents", Path: []string{"totalSpentCents"}, Kind: "bigint"},
	{Name: "balance", Path: []string{"balance"}, Kind: "double"},
	{Name: "frozen", Path: []string{"frozen"}, Kind: "double"},
	{Name: "available", Path: []string{"available"}, Kind: "double"},
	{Name: "total_spent", Path: []string{"totalSpent"}, Kind: "double"},
	{Name: "total_recharged", Path: []string{"totalRecharged"}, Kind: "double"},
	{Name: "quantity", Path: []string{"quantity"}, Kind: "double"},
	{Name: "reports", Path: []string{"reports"}, Kind: "bigint"},
	{Name: "price_snapshot_unit_price_cents", Path: []string{"priceSnapshot", "unitPriceCents"}, Kind: "bigint"},
	{Name: "price_snapshot_compute_hourly", Path: []string{"priceSnapshot", "computeHourly"}, Kind: "double"},
	{Name: "price_snapshot_storage_gb_month", Path: []string{"priceSnapshot", "storageGbMonth"}, Kind: "double"},
	{Name: "price_snapshot_size_gb", Path: []string{"priceSnapshot", "sizeGb"}, Kind: "double"},
	{Name: "access_requires_login", Path: []string{"access", "requiresLogin"}, Kind: "bool"},
	{Name: "guard_block_new_workspaces", Path: []string{"guard", "blockNewWorkspaces"}, Kind: "bool"},
}

func (s *postgresEntStateStore) install(ctx context.Context) error {
	entries, err := controlPlaneMigrations.ReadDir("ent_migrations")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		sqlText, err := controlPlaneMigrations.ReadFile("ent_migrations/" + entry.Name())
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, string(sqlText)); err != nil {
			return err
		}
	}
	return nil
}

func (s *postgresEntStateStore) Load(ctx context.Context) (controlPlaneState, error) {
	var facts controlPlaneState
	var err error
	if facts.Computes, err = s.loadFactTable(ctx, "control_plane_compute_allocations"); err != nil {
		return facts, err
	}
	if facts.Storages, err = s.loadFactTable(ctx, "control_plane_storage_volumes"); err != nil {
		return facts, err
	}
	if facts.Attachments, err = s.loadFactTable(ctx, "control_plane_storage_attachments"); err != nil {
		return facts, err
	}
	if facts.Workspaces, err = s.loadFactTable(ctx, "control_plane_workspaces"); err != nil {
		return facts, err
	}
	if facts.Users, err = s.loadFactTable(ctx, "control_plane_users"); err != nil {
		return facts, err
	}
	if facts.Sessions, err = s.loadFactTable(ctx, "control_plane_sessions"); err != nil {
		return facts, err
	}
	if facts.Orgs, err = s.loadFactTable(ctx, "control_plane_organizations"); err != nil {
		return facts, err
	}
	if facts.Memberships, err = s.loadFactTable(ctx, "control_plane_memberships"); err != nil {
		return facts, err
	}
	if facts.Support, err = s.loadFactTable(ctx, "control_plane_support_ticket_mappings"); err != nil {
		return facts, err
	}
	if facts.Wallets, err = s.loadFactTable(ctx, "control_plane_wallet_projections"); err != nil {
		return facts, err
	}
	if facts.Ledger, err = s.loadFactEvents(ctx, "control_plane_ledger_projections"); err != nil {
		return facts, err
	}
	if facts.WalletTx, err = s.loadFactEvents(ctx, "control_plane_wallet_transaction_projections"); err != nil {
		return facts, err
	}
	if facts.Topups, err = s.loadFactEvents(ctx, "control_plane_manual_topup_projections"); err != nil {
		return facts, err
	}
	if facts.RuntimeOps, err = s.loadFactEvents(ctx, "control_plane_runtime_operations"); err != nil {
		return facts, err
	}
	if facts.AuditEvents, err = s.loadFactEvents(ctx, "control_plane_admin_audit_events"); err != nil {
		return facts, err
	}
	facts.Reconcile, err = s.loadSingleton(ctx, "control_plane_billing_reconciliation")
	return facts, err
}

func (s *postgresEntStateStore) Save(ctx context.Context, facts controlPlaneState) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := replaceStateTable(ctx, tx, "control_plane_compute_allocations", facts.Computes); err != nil {
		return rollback(tx, err)
	}
	if err := replaceStateTable(ctx, tx, "control_plane_storage_volumes", facts.Storages); err != nil {
		return rollback(tx, err)
	}
	if err := replaceStateTable(ctx, tx, "control_plane_storage_attachments", facts.Attachments); err != nil {
		return rollback(tx, err)
	}
	if err := replaceStateTable(ctx, tx, "control_plane_workspaces", facts.Workspaces); err != nil {
		return rollback(tx, err)
	}
	if err := replaceStateTable(ctx, tx, "control_plane_users", facts.Users); err != nil {
		return rollback(tx, err)
	}
	if err := replaceStateTable(ctx, tx, "control_plane_sessions", facts.Sessions); err != nil {
		return rollback(tx, err)
	}
	if err := replaceStateTable(ctx, tx, "control_plane_organizations", facts.Orgs); err != nil {
		return rollback(tx, err)
	}
	if err := replaceStateTable(ctx, tx, "control_plane_memberships", facts.Memberships); err != nil {
		return rollback(tx, err)
	}
	if err := replaceStateTable(ctx, tx, "control_plane_support_ticket_mappings", facts.Support); err != nil {
		return rollback(tx, err)
	}
	if err := replaceStateTable(ctx, tx, "control_plane_wallet_projections", facts.Wallets); err != nil {
		return rollback(tx, err)
	}
	if err := replaceStateEvents(ctx, tx, "control_plane_ledger_projections", facts.Ledger); err != nil {
		return rollback(tx, err)
	}
	if err := replaceStateEvents(ctx, tx, "control_plane_wallet_transaction_projections", facts.WalletTx); err != nil {
		return rollback(tx, err)
	}
	if err := replaceStateEvents(ctx, tx, "control_plane_manual_topup_projections", facts.Topups); err != nil {
		return rollback(tx, err)
	}
	if err := replaceStateEvents(ctx, tx, "control_plane_runtime_operations", facts.RuntimeOps); err != nil {
		return rollback(tx, err)
	}
	if err := replaceStateEvents(ctx, tx, "control_plane_admin_audit_events", facts.AuditEvents); err != nil {
		return rollback(tx, err)
	}
	if err := replaceSingleton(ctx, tx, "control_plane_billing_reconciliation", facts.Reconcile); err != nil {
		return rollback(tx, err)
	}
	return tx.Commit()
}

func (s *postgresEntStateStore) loadFactTable(ctx context.Context, table string) (stateTable, error) {
	rows, err := s.db.QueryContext(ctx, selectFactSQL(table, ""))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := stateTable{}
	for rows.Next() {
		row, err := scanStateRow(rows)
		if err != nil {
			return nil, err
		}
		out[stringValue(row["id"])] = row
	}
	return out, rows.Err()
}

func (s *postgresEntStateStore) loadFactEvents(ctx context.Context, table string) ([]stateRow, error) {
	rows, err := s.db.QueryContext(ctx, selectFactSQL(table, " ORDER BY created_at, id"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []stateRow
	for rows.Next() {
		row, err := scanStateRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *postgresEntStateStore) loadSingleton(ctx context.Context, table string) (stateRow, error) {
	rows, err := s.db.QueryContext(ctx, selectFactSQL(table, " WHERE id = $1"), singletonFactID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	row, err := scanStateRow(rows)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return row, err
}

func replaceStateTable(ctx context.Context, tx *sql.Tx, table string, rows stateTable) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+table); err != nil {
		return err
	}
	for id, row := range rows {
		if _, err := tx.ExecContext(ctx, insertFactSQL(table, "updated_at"), stateValues(id, row, stringValue(row["accountId"]))...); err != nil {
			return err
		}
	}
	return nil
}

func replaceStateEvents(ctx context.Context, tx *sql.Tx, table string, rows []stateRow) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+table); err != nil {
		return err
	}
	for index, row := range rows {
		id := firstNonEmpty(stringValue(row["id"]), stableID(table, stringValue(row["accountId"]), stringValue(row["createdAt"]), stringValue(row["type"]))[:12])
		values := stateValues(id, row, stringValue(row["accountId"]))
		values = append(values, index)
		if _, err := tx.ExecContext(ctx, insertFactSQL(table, "created_at"), values...); err != nil {
			return err
		}
	}
	return nil
}

func replaceSingleton(ctx context.Context, tx *sql.Tx, table string, row stateRow) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+table); err != nil {
		return err
	}
	if row == nil {
		return nil
	}
	_, err := tx.ExecContext(ctx, insertFactSQL(table, "updated_at"), stateValues(singletonFactID, row, "")...)
	return err
}

func selectFactSQL(table string, suffix string) string {
	columns := []string{"id", "account_id"}
	for _, column := range postgresStateColumns {
		columns = append(columns, column.Name+"::text")
	}
	return `SELECT ` + strings.Join(columns, ", ") + ` FROM ` + table + suffix
}

func scanStateRow(rows *sql.Rows) (stateRow, error) {
	values := make([]sql.NullString, len(postgresStateColumns)+2)
	dest := make([]any, len(values))
	for index := range values {
		dest[index] = &values[index]
	}
	if err := rows.Scan(dest...); err != nil {
		return nil, err
	}
	row := stateRow{"id": values[0].String}
	if values[1].Valid && values[1].String != "" {
		row["accountId"] = values[1].String
	}
	for index, column := range postgresStateColumns {
		value := values[index+2]
		if !value.Valid || value.String == "" {
			continue
		}
		parsed, ok := parseColumnValue(value.String, column.Kind)
		if ok {
			setPath(row, column.Path, parsed)
		}
	}
	return row, nil
}

func parseColumnValue(value string, kind string) (any, bool) {
	switch kind {
	case "bigint":
		parsed, err := strconv.ParseInt(value, 10, 64)
		return parsed, err == nil
	case "double":
		parsed, err := strconv.ParseFloat(value, 64)
		return parsed, err == nil
	case "bool":
		parsed, err := strconv.ParseBool(value)
		return parsed, err == nil
	default:
		return value, true
	}
}

func insertFactSQL(table string, timestampColumn string) string {
	columns := []string{"id", "account_id"}
	placeholders := []string{"$1", "$2"}
	for index, column := range postgresStateColumns {
		columns = append(columns, column.Name)
		placeholders = append(placeholders, fmt.Sprintf("$%d", index+3))
	}
	timestampExpr := "now()"
	if timestampColumn == "created_at" {
		timestampExpr = fmt.Sprintf("now() + ($%d || ' microseconds')::interval", len(placeholders)+1)
	}
	columns = append(columns, timestampColumn)
	placeholders = append(placeholders, timestampExpr)
	return `INSERT INTO ` + table + ` (` + strings.Join(columns, ", ") + `) VALUES (` + strings.Join(placeholders, ", ") + `)`
}

func stateValues(id string, row stateRow, accountID string) []any {
	values := []any{id, accountID}
	for _, column := range postgresStateColumns {
		values = append(values, columnValue(row, column))
	}
	return values
}

func columnValue(row stateRow, column stateColumn) any {
	value, ok := valueAtPath(row, column.Path)
	if !ok || value == nil {
		return nil
	}
	switch column.Kind {
	case "bigint":
		return int64(numberValue(value))
	case "double":
		return numberValue(value)
	case "bool":
		if parsed, ok := value.(bool); ok {
			return parsed
		}
		parsed, err := strconv.ParseBool(stringValue(value))
		if err != nil {
			return nil
		}
		return parsed
	default:
		text := stringValue(value)
		if text == "" {
			return nil
		}
		return text
	}
}

func valueAtPath(row stateRow, path []string) (any, bool) {
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

func setPath(row stateRow, path []string, value any) {
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
	case json.Number:
		parsed, _ := typed.Float64()
		return parsed
	default:
		parsed, _ := strconv.ParseFloat(stringValue(value), 64)
		return parsed
	}
}

func rollback(tx *sql.Tx, err error) error {
	_ = tx.Rollback()
	return err
}
