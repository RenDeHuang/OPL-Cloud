package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	_ "github.com/lib/pq"
)

const singletonFactID = "default"

type factRow = map[string]any
type factTable = map[string]factRow

type FactStore interface {
	Load(ctx context.Context) (controlPlaneFacts, error)
	Save(ctx context.Context, facts controlPlaneFacts) error
}

type controlPlaneFacts struct {
	Version     int       `json:"version"`
	Computes    factTable `json:"computes,omitempty"`
	Storages    factTable `json:"storages,omitempty"`
	Attachments factTable `json:"attachments,omitempty"`
	Workspaces  factTable `json:"workspaces,omitempty"`
	Users       factTable `json:"users,omitempty"`
	Orgs        factTable `json:"orgs,omitempty"`
	Memberships factTable `json:"memberships,omitempty"`
	Support     factTable `json:"support,omitempty"`
	Wallets     factTable `json:"wallets,omitempty"`
	Ledger      []factRow `json:"ledger,omitempty"`
	WalletTx    []factRow `json:"walletTx,omitempty"`
	Topups      []factRow `json:"topups,omitempty"`
	RuntimeOps  []factRow `json:"runtimeOperations,omitempty"`
	AuditEvents []factRow `json:"auditEvents,omitempty"`
	Reconcile   factRow   `json:"billingReconciliation,omitempty"`
}

func FactStoreFromEnv() (FactStore, error) {
	if path := os.Getenv("OPL_CONTROL_PLANE_FACTS_FILE"); path != "" {
		return NewFileFactStore(path), nil
	}
	if databaseURL := os.Getenv("DATABASE_URL"); databaseURL != "" {
		return NewPostgresFactStore(databaseURL)
	}
	return nil, nil
}

type fileFactStore struct {
	path string
	mu   sync.Mutex
}

func NewFileFactStore(path string) FactStore {
	return &fileFactStore{path: path}
}

func (s *fileFactStore) Load(_ context.Context) (controlPlaneFacts, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return controlPlaneFacts{}, nil
	}
	if err != nil {
		return controlPlaneFacts{}, err
	}
	var facts controlPlaneFacts
	if err := json.Unmarshal(data, &facts); err != nil {
		return controlPlaneFacts{}, err
	}
	return facts, nil
}

func (s *fileFactStore) Save(_ context.Context, facts controlPlaneFacts) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(facts, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

type postgresFactStore struct {
	db *sql.DB
}

func NewPostgresFactStore(databaseURL string) (FactStore, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, err
	}
	store := &postgresFactStore{db: db}
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

func (s *postgresFactStore) install(ctx context.Context) error {
	for _, table := range postgresFactTables {
		if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+table+` (
  id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL DEFAULT '',
  payload JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`); err != nil {
			return err
		}
	}
	for _, table := range postgresFactEventTables {
		if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+table+` (
  id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL DEFAULT '',
  payload JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`); err != nil {
			return err
		}
	}
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS control_plane_billing_reconciliation (
  id TEXT PRIMARY KEY,
  payload JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`)
	return err
}

func (s *postgresFactStore) Load(ctx context.Context) (controlPlaneFacts, error) {
	var facts controlPlaneFacts
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

func (s *postgresFactStore) Save(ctx context.Context, facts controlPlaneFacts) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := replaceFactTable(ctx, tx, "control_plane_compute_allocations", facts.Computes); err != nil {
		return rollback(tx, err)
	}
	if err := replaceFactTable(ctx, tx, "control_plane_storage_volumes", facts.Storages); err != nil {
		return rollback(tx, err)
	}
	if err := replaceFactTable(ctx, tx, "control_plane_storage_attachments", facts.Attachments); err != nil {
		return rollback(tx, err)
	}
	if err := replaceFactTable(ctx, tx, "control_plane_workspaces", facts.Workspaces); err != nil {
		return rollback(tx, err)
	}
	if err := replaceFactTable(ctx, tx, "control_plane_users", facts.Users); err != nil {
		return rollback(tx, err)
	}
	if err := replaceFactTable(ctx, tx, "control_plane_organizations", facts.Orgs); err != nil {
		return rollback(tx, err)
	}
	if err := replaceFactTable(ctx, tx, "control_plane_memberships", facts.Memberships); err != nil {
		return rollback(tx, err)
	}
	if err := replaceFactTable(ctx, tx, "control_plane_support_ticket_mappings", facts.Support); err != nil {
		return rollback(tx, err)
	}
	if err := replaceFactTable(ctx, tx, "control_plane_wallet_projections", facts.Wallets); err != nil {
		return rollback(tx, err)
	}
	if err := replaceFactEvents(ctx, tx, "control_plane_ledger_projections", facts.Ledger); err != nil {
		return rollback(tx, err)
	}
	if err := replaceFactEvents(ctx, tx, "control_plane_wallet_transaction_projections", facts.WalletTx); err != nil {
		return rollback(tx, err)
	}
	if err := replaceFactEvents(ctx, tx, "control_plane_manual_topup_projections", facts.Topups); err != nil {
		return rollback(tx, err)
	}
	if err := replaceFactEvents(ctx, tx, "control_plane_runtime_operations", facts.RuntimeOps); err != nil {
		return rollback(tx, err)
	}
	if err := replaceFactEvents(ctx, tx, "control_plane_admin_audit_events", facts.AuditEvents); err != nil {
		return rollback(tx, err)
	}
	if err := replaceSingleton(ctx, tx, "control_plane_billing_reconciliation", facts.Reconcile); err != nil {
		return rollback(tx, err)
	}
	return tx.Commit()
}

func (s *postgresFactStore) loadFactTable(ctx context.Context, table string) (factTable, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, payload FROM `+table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := factTable{}
	for rows.Next() {
		var id string
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			return nil, err
		}
		var row factRow
		if err := json.Unmarshal(data, &row); err != nil {
			return nil, err
		}
		out[id] = row
	}
	return out, rows.Err()
}

func (s *postgresFactStore) loadFactEvents(ctx context.Context, table string) ([]factRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM `+table+` ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []factRow
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var row factRow
		if err := json.Unmarshal(data, &row); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *postgresFactStore) loadSingleton(ctx context.Context, table string) (factRow, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM `+table+` WHERE id = $1`, singletonFactID).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var row factRow
	return row, json.Unmarshal(data, &row)
}

func replaceFactTable(ctx context.Context, tx *sql.Tx, table string, rows factTable) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+table); err != nil {
		return err
	}
	for id, row := range rows {
		data, err := json.Marshal(row)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+table+` (id, account_id, payload, updated_at) VALUES ($1, $2, $3, now())`, id, stringValue(row["accountId"]), data); err != nil {
			return err
		}
	}
	return nil
}

func replaceFactEvents(ctx context.Context, tx *sql.Tx, table string, rows []factRow) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+table); err != nil {
		return err
	}
	for index, row := range rows {
		id := firstNonEmpty(stringValue(row["id"]), stableID(table, stringValue(row["accountId"]), stringValue(row["createdAt"]), stringValue(row["type"]))[:12])
		data, err := json.Marshal(row)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+table+` (id, account_id, payload, created_at) VALUES ($1, $2, $3, now() + ($4 || ' microseconds')::interval)`, id, stringValue(row["accountId"]), data, index); err != nil {
			return err
		}
	}
	return nil
}

func replaceSingleton(ctx context.Context, tx *sql.Tx, table string, row factRow) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+table); err != nil {
		return err
	}
	if row == nil {
		return nil
	}
	data, err := json.Marshal(row)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO `+table+` (id, payload, updated_at) VALUES ($1, $2, now())`, singletonFactID, data)
	return err
}

func rollback(tx *sql.Tx, err error) error {
	_ = tx.Rollback()
	return err
}
