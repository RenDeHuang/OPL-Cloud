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

const readModelID = "default"

type ReadModelStore interface {
	Load(ctx context.Context) (readModelSnapshot, error)
	Save(ctx context.Context, snapshot readModelSnapshot) error
}

type readModelSnapshot struct {
	Version     int                       `json:"version"`
	Computes    map[string]map[string]any `json:"computes,omitempty"`
	Storages    map[string]map[string]any `json:"storages,omitempty"`
	Attachments map[string]map[string]any `json:"attachments,omitempty"`
	Workspaces  map[string]map[string]any `json:"workspaces,omitempty"`
	Users       map[string]map[string]any `json:"users,omitempty"`
	Orgs        map[string]map[string]any `json:"orgs,omitempty"`
	Memberships map[string]map[string]any `json:"memberships,omitempty"`
	Wallets     map[string]map[string]any `json:"wallets,omitempty"`
	Ledger      []map[string]any          `json:"ledger,omitempty"`
	Usage       []map[string]any          `json:"usage,omitempty"`
	WalletTx    []map[string]any          `json:"walletTx,omitempty"`
	Topups      []map[string]any          `json:"topups,omitempty"`
}

func ReadModelStoreFromEnv() (ReadModelStore, error) {
	if path := os.Getenv("OPL_CONTROL_PLANE_STATE_FILE"); path != "" {
		return NewJSONReadModelStore(path), nil
	}
	if databaseURL := os.Getenv("DATABASE_URL"); databaseURL != "" {
		return NewPostgresReadModelStore(databaseURL)
	}
	return nil, nil
}

type jsonReadModelStore struct {
	path string
	mu   sync.Mutex
}

func NewJSONReadModelStore(path string) ReadModelStore {
	return &jsonReadModelStore{path: path}
}

func (s *jsonReadModelStore) Load(_ context.Context) (readModelSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return readModelSnapshot{}, nil
	}
	if err != nil {
		return readModelSnapshot{}, err
	}
	var snapshot readModelSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return readModelSnapshot{}, err
	}
	return snapshot, nil
}

func (s *jsonReadModelStore) Save(_ context.Context, snapshot readModelSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

type postgresReadModelStore struct {
	db *sql.DB
}

func NewPostgresReadModelStore(databaseURL string) (ReadModelStore, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, err
	}
	store := &postgresReadModelStore{db: db}
	if err := store.install(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *postgresReadModelStore) install(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS control_plane_read_model (
  id TEXT PRIMARY KEY,
  payload JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`)
	return err
}

func (s *postgresReadModelStore) Load(ctx context.Context) (readModelSnapshot, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM control_plane_read_model WHERE id = $1`, readModelID).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return readModelSnapshot{}, nil
	}
	if err != nil {
		return readModelSnapshot{}, err
	}
	var snapshot readModelSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return readModelSnapshot{}, err
	}
	return snapshot, nil
}

func (s *postgresReadModelStore) Save(ctx context.Context, snapshot readModelSnapshot) error {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO control_plane_read_model (id, payload, updated_at)
VALUES ($1, $2, now())
ON CONFLICT (id) DO UPDATE
SET payload = EXCLUDED.payload, updated_at = now()`, readModelID, data)
	return err
}
