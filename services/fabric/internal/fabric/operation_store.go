package fabric

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"strings"
	"sync"

	_ "github.com/lib/pq"
)

type OperationStore interface {
	Append(ctx context.Context, operation FabricOperation) error
	List(ctx context.Context) ([]FabricOperation, error)
}

type RuntimeAccessStore interface {
	SaveRuntimeAccess(ctx context.Context, runtime WorkspaceRuntime) error
	RuntimeAccess(ctx context.Context, workspaceID string) (RuntimeAccess, bool, error)
}

type MemoryOperationStore struct {
	mu        sync.Mutex
	operation []FabricOperation
	access    map[string]RuntimeAccess
}

func NewMemoryOperationStore() *MemoryOperationStore {
	return &MemoryOperationStore{access: map[string]RuntimeAccess{}}
}

func (s *MemoryOperationStore) Append(_ context.Context, operation FabricOperation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.operation = append(s.operation, operation)
	return nil
}

func (s *MemoryOperationStore) List(_ context.Context) ([]FabricOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	operations := make([]FabricOperation, len(s.operation))
	copy(operations, s.operation)
	return operations, nil
}

func (s *MemoryOperationStore) SaveRuntimeAccess(_ context.Context, runtime WorkspaceRuntime) error {
	if runtime.WorkspaceID == "" || runtime.Access.Username == "" || runtime.Access.Password == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.access == nil {
		s.access = map[string]RuntimeAccess{}
	}
	s.access[runtime.WorkspaceID] = runtime.Access
	return nil
}

func (s *MemoryOperationStore) RuntimeAccess(_ context.Context, workspaceID string) (RuntimeAccess, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	access, ok := s.access[workspaceID]
	return access, ok, nil
}

type PostgresOperationStore struct {
	db *sql.DB
}

//go:embed ent_migrations/*.sql
var fabricMigrations embed.FS

func PostgresOperationSchemaSQL() string {
	entries, err := fabricMigrations.ReadDir("ent_migrations")
	if err != nil {
		return ""
	}
	var out strings.Builder
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		data, err := fabricMigrations.ReadFile("ent_migrations/" + entry.Name())
		if err != nil {
			return ""
		}
		out.Write(data)
		out.WriteByte('\n')
	}
	return out.String()
}

func NewPostgresOperationStore(databaseURL string) (*PostgresOperationStore, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, err
	}
	store := &PostgresOperationStore{db: db}
	if err := store.Install(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *PostgresOperationStore) Install(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, PostgresOperationSchemaSQL())
	return err
}

func (s *PostgresOperationStore) Append(ctx context.Context, operation FabricOperation) error {
	payload := operation.RedactedProviderPayload
	if payload == nil {
		payload = map[string]any{}
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var finishedAt any
	if !operation.FinishedAt.IsZero() {
		finishedAt = operation.FinishedAt
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO fabric_operations(
  id, operation_id, caller_service, action, resource_kind, resource_id, account_id, workspace_id,
  provider, provider_request_id, idempotency_key, request_hash, redacted_provider_payload,
  status, error_code, retryable, started_at, finished_at, created_at
) VALUES (
  $1, $2, $3, $4, $5, $6, NULLIF($7, ''), NULLIF($8, ''), NULLIF($9, ''), NULLIF($10, ''),
  NULLIF($11, ''), NULLIF($12, ''), $13::jsonb, $14, NULLIF($15, ''), $16, $17, $18, $19
)`, operation.ID, operation.OperationID, operation.CallerService, operation.Action, operation.ResourceKind, operation.ResourceID, operation.AccountID, operation.WorkspaceID, operation.Provider, operation.ProviderRequestID, operation.IdempotencyKey, operation.RequestHash, string(payloadJSON), operation.Status, operation.ErrorCode, operation.Retryable, operation.StartedAt, finishedAt, operation.CreatedAt)
	return err
}

func (s *PostgresOperationStore) List(ctx context.Context) ([]FabricOperation, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, operation_id, caller_service, action, resource_kind, resource_id, COALESCE(account_id, ''),
  COALESCE(workspace_id, ''), COALESCE(provider, ''), COALESCE(provider_request_id, ''),
  COALESCE(idempotency_key, ''), COALESCE(request_hash, ''), redacted_provider_payload,
  status, COALESCE(error_code, ''), retryable, started_at, finished_at, created_at
FROM fabric_operations
ORDER BY created_at, id
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var operations []FabricOperation
	for rows.Next() {
		var operation FabricOperation
		var payload []byte
		var finishedAt sql.NullTime
		if err := rows.Scan(&operation.ID, &operation.OperationID, &operation.CallerService, &operation.Action, &operation.ResourceKind, &operation.ResourceID, &operation.AccountID, &operation.WorkspaceID, &operation.Provider, &operation.ProviderRequestID, &operation.IdempotencyKey, &operation.RequestHash, &payload, &operation.Status, &operation.ErrorCode, &operation.Retryable, &operation.StartedAt, &finishedAt, &operation.CreatedAt); err != nil {
			return nil, err
		}
		if finishedAt.Valid {
			operation.FinishedAt = finishedAt.Time
		}
		if len(payload) > 0 {
			_ = json.Unmarshal(payload, &operation.RedactedProviderPayload)
		}
		operations = append(operations, operation)
	}
	return operations, rows.Err()
}

func (s *PostgresOperationStore) SaveRuntimeAccess(ctx context.Context, runtime WorkspaceRuntime) error {
	if runtime.WorkspaceID == "" || runtime.Access.Username == "" || runtime.Access.Password == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO fabric_workspace_runtime_access(
  workspace_id, runtime_id, url, service_name, username, password,
  credential_status, credential_version, secret_ref, updated_at
) VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, $7, $8, NULLIF($9, ''), $10)
ON CONFLICT (workspace_id) DO UPDATE SET
  runtime_id = EXCLUDED.runtime_id,
  url = EXCLUDED.url,
  service_name = EXCLUDED.service_name,
  username = EXCLUDED.username,
  password = EXCLUDED.password,
  credential_status = EXCLUDED.credential_status,
  credential_version = EXCLUDED.credential_version,
  secret_ref = EXCLUDED.secret_ref,
  updated_at = EXCLUDED.updated_at
`, runtime.WorkspaceID, runtime.ID, runtime.URL, runtime.ServiceName, runtime.Access.Username, runtime.Access.Password, runtime.Access.CredentialStatus, runtime.Access.CredentialVersion, runtime.Access.SecretRef, runtime.Access.UpdatedAt)
	return err
}

func (s *PostgresOperationStore) RuntimeAccess(ctx context.Context, workspaceID string) (RuntimeAccess, bool, error) {
	var access RuntimeAccess
	row := s.db.QueryRowContext(ctx, `
SELECT username, password, credential_status, credential_version, COALESCE(secret_ref, ''), updated_at
FROM fabric_workspace_runtime_access
WHERE workspace_id = $1
`, workspaceID)
	if err := row.Scan(&access.Username, &access.Password, &access.CredentialStatus, &access.CredentialVersion, &access.SecretRef, &access.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return RuntimeAccess{}, false, nil
		}
		return RuntimeAccess{}, false, err
	}
	return access, true, nil
}
