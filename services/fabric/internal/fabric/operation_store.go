package fabric

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/lib/pq"

	fabricent "opl-cloud/services/fabric/ent"
	"opl-cloud/services/fabric/ent/contenttransfer"
	"opl-cloud/services/fabric/ent/contenttransferchunk"
	"opl-cloud/services/fabric/ent/fabricoperation"
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
	mu               sync.Mutex
	operation        []FabricOperation
	access           map[string]RuntimeAccess
	transferSessions map[string]Transfer
	transferKeys     map[string]string
	transferChunks   map[string]map[int]TransferChunk
}

func NewMemoryOperationStore() *MemoryOperationStore {
	return &MemoryOperationStore{access: map[string]RuntimeAccess{}, transferSessions: map[string]Transfer{}, transferKeys: map[string]string{}, transferChunks: map[string]map[int]TransferChunk{}}
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
	db     *sql.DB
	client *fabricent.Client
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
	store := &PostgresOperationStore{
		db:     db,
		client: fabricent.NewClient(fabricent.Driver(entsql.OpenDB(dialect.Postgres, db))),
	}
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

func (s *PostgresOperationStore) CreateTransfer(ctx context.Context, transfer Transfer) (Transfer, error) {
	existing, err := s.client.ContentTransfer.Query().Where(contenttransfer.IdempotencyKey(transfer.IdempotencyKey)).Only(ctx)
	if err == nil {
		if existing.RequestHash != transfer.RequestHash {
			return Transfer{}, ErrTransferChunkConflict
		}
		return transferFromEnt(existing), nil
	}
	if !fabricent.IsNotFound(err) {
		return Transfer{}, err
	}
	created, err := s.client.ContentTransfer.Create().
		SetID(transfer.TransferID).SetOrganizationID(transfer.OrganizationID).SetWorkspaceID(transfer.WorkspaceID).
		SetProjectID(transfer.ProjectID).SetPath(transfer.Path).SetDigest(transfer.Digest).SetSize(transfer.Size).
		SetChunkSize(transfer.ChunkSize).SetChunkCount(transfer.ChunkCount).SetStatus(transfer.Status).
		SetIdempotencyKey(transfer.IdempotencyKey).SetRequestHash(transfer.RequestHash).SetCreatedAt(transfer.CreatedAt).Save(ctx)
	if err != nil {
		concurrent, queryErr := s.client.ContentTransfer.Query().Where(contenttransfer.IdempotencyKey(transfer.IdempotencyKey)).Only(ctx)
		if queryErr == nil {
			if concurrent.RequestHash != transfer.RequestHash {
				return Transfer{}, ErrTransferChunkConflict
			}
			return transferFromEnt(concurrent), nil
		}
		return Transfer{}, err
	}
	return transferFromEnt(created), nil
}

func (s *PostgresOperationStore) Transfer(ctx context.Context, id string) (Transfer, error) {
	row, err := s.client.ContentTransfer.Get(ctx, id)
	if fabricent.IsNotFound(err) {
		return Transfer{}, ErrTransferNotFound
	}
	if err != nil {
		return Transfer{}, err
	}
	result := transferFromEnt(row)
	chunks, err := s.TransferChunks(ctx, id)
	if err != nil {
		return Transfer{}, err
	}
	result.ReceivedChunks = receivedIndexesFromChunks(chunks)
	return result, nil
}

func (s *PostgresOperationStore) SaveTransfer(ctx context.Context, transfer Transfer) error {
	update := s.client.ContentTransfer.UpdateOneID(transfer.TransferID).SetStatus(transfer.Status)
	if transfer.CompletedAt != nil {
		update.SetCompletedAt(*transfer.CompletedAt)
	}
	_, err := update.Save(ctx)
	if fabricent.IsNotFound(err) {
		return ErrTransferNotFound
	}
	return err
}

func (s *PostgresOperationStore) SaveTransferChunk(ctx context.Context, id string, chunk TransferChunk) error {
	existing, err := s.client.ContentTransferChunk.Query().Where(contenttransferchunk.TransferID(id), contenttransferchunk.ChunkIndex(chunk.Index)).Only(ctx)
	if err == nil {
		if existing.Digest != chunk.Digest {
			return ErrTransferChunkConflict
		}
		return nil
	}
	if !fabricent.IsNotFound(err) {
		return err
	}
	_, err = s.client.ContentTransferChunk.Create().SetID(id + "-" + fmt.Sprint(chunk.Index)).SetTransferID(id).
		SetChunkIndex(chunk.Index).SetDigest(chunk.Digest).SetBody(chunk.Body).Save(ctx)
	if err != nil {
		concurrent, queryErr := s.client.ContentTransferChunk.Query().Where(contenttransferchunk.TransferID(id), contenttransferchunk.ChunkIndex(chunk.Index)).Only(ctx)
		if queryErr == nil {
			if concurrent.Digest != chunk.Digest {
				return ErrTransferChunkConflict
			}
			return nil
		}
	}
	return err
}

func (s *PostgresOperationStore) TransferChunks(ctx context.Context, id string) ([]TransferChunk, error) {
	rows, err := s.client.ContentTransferChunk.Query().Where(contenttransferchunk.TransferID(id)).Order(fabricent.Asc(contenttransferchunk.FieldChunkIndex)).All(ctx)
	if err != nil {
		return nil, err
	}
	chunks := make([]TransferChunk, 0, len(rows))
	for _, row := range rows {
		chunks = append(chunks, TransferChunk{Index: row.ChunkIndex, Digest: row.Digest, Body: row.Body})
	}
	return chunks, nil
}

func (s *PostgresOperationStore) Content(ctx context.Context, workspaceID, digest string) (Content, error) {
	row, err := s.client.ContentTransfer.Query().Where(contenttransfer.WorkspaceID(workspaceID), contenttransfer.Digest(digest), contenttransfer.Status("completed")).First(ctx)
	if fabricent.IsNotFound(err) {
		return Content{}, ErrContentNotFound
	}
	if err != nil {
		return Content{}, err
	}
	chunks, err := s.TransferChunks(ctx, row.ID)
	if err != nil {
		return Content{}, err
	}
	var body []byte
	for _, chunk := range chunks {
		body = append(body, chunk.Body...)
	}
	return Content{Digest: digest, WorkspaceID: row.WorkspaceID, Path: row.Path, Body: body}, nil
}

func transferFromEnt(row *fabricent.ContentTransfer) Transfer {
	return Transfer{TransferID: row.ID, OrganizationID: row.OrganizationID, WorkspaceID: row.WorkspaceID, ProjectID: row.ProjectID, Path: row.Path, Digest: row.Digest, Size: row.Size, ChunkSize: row.ChunkSize, ChunkCount: row.ChunkCount, Status: row.Status, IdempotencyKey: row.IdempotencyKey, RequestHash: row.RequestHash, CreatedAt: row.CreatedAt, CompletedAt: row.CompletedAt}
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
	create := s.client.FabricOperation.Create().
		SetID(operation.ID).
		SetOperationID(operation.OperationID).
		SetCallerService(operation.CallerService).
		SetAction(operation.Action).
		SetResourceKind(operation.ResourceKind).
		SetResourceID(operation.ResourceID).
		SetAccountID(operation.AccountID).
		SetWorkspaceID(operation.WorkspaceID).
		SetProvider(operation.Provider).
		SetProviderRequestID(operation.ProviderRequestID).
		SetIdempotencyKey(operation.IdempotencyKey).
		SetRequestHash(operation.RequestHash).
		SetRedactedProviderPayload(string(payloadJSON)).
		SetStatus(operation.Status).
		SetErrorCode(operation.ErrorCode).
		SetRetryable(operation.Retryable).
		SetStartedAt(operation.StartedAt).
		SetCreatedAt(operation.CreatedAt)
	if !operation.FinishedAt.IsZero() {
		create.SetFinishedAt(operation.FinishedAt)
	}
	return create.Exec(ctx)
}

func (s *PostgresOperationStore) List(ctx context.Context) ([]FabricOperation, error) {
	rows, err := s.client.FabricOperation.Query().Order(fabricent.Asc(fabricoperation.FieldCreatedAt, fabricoperation.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	operations := make([]FabricOperation, 0, len(rows))
	for _, row := range rows {
		operations = append(operations, fabricOperationFromEnt(row))
	}
	return operations, nil
}

func (s *PostgresOperationStore) SaveRuntimeAccess(ctx context.Context, runtime WorkspaceRuntime) error {
	if runtime.WorkspaceID == "" || runtime.Access.Username == "" || runtime.Access.Password == "" {
		return nil
	}
	_, err := s.client.WorkspaceRuntimeAccess.Get(ctx, runtime.WorkspaceID)
	if fabricent.IsNotFound(err) {
		return s.client.WorkspaceRuntimeAccess.Create().
			SetID(runtime.WorkspaceID).
			SetRuntimeID(runtime.ID).
			SetURL(runtime.URL).
			SetServiceName(runtime.ServiceName).
			SetUsername(runtime.Access.Username).
			SetPassword(runtime.Access.Password).
			SetCredentialStatus(runtime.Access.CredentialStatus).
			SetCredentialVersion(runtime.Access.CredentialVersion).
			SetSecretRef(runtime.Access.SecretRef).
			SetUpdatedAt(runtime.Access.UpdatedAt).
			Exec(ctx)
	}
	if err != nil {
		return err
	}
	return s.client.WorkspaceRuntimeAccess.UpdateOneID(runtime.WorkspaceID).
		SetRuntimeID(runtime.ID).
		SetURL(runtime.URL).
		SetServiceName(runtime.ServiceName).
		SetUsername(runtime.Access.Username).
		SetPassword(runtime.Access.Password).
		SetCredentialStatus(runtime.Access.CredentialStatus).
		SetCredentialVersion(runtime.Access.CredentialVersion).
		SetSecretRef(runtime.Access.SecretRef).
		SetUpdatedAt(runtime.Access.UpdatedAt).
		Exec(ctx)
}

func (s *PostgresOperationStore) RuntimeAccess(ctx context.Context, workspaceID string) (RuntimeAccess, bool, error) {
	row, err := s.client.WorkspaceRuntimeAccess.Get(ctx, workspaceID)
	if fabricent.IsNotFound(err) {
		return RuntimeAccess{}, false, nil
	}
	if err != nil {
		return RuntimeAccess{}, false, err
	}
	return RuntimeAccess{Username: row.Username, Password: row.Password, CredentialStatus: row.CredentialStatus, CredentialVersion: row.CredentialVersion, SecretRef: row.SecretRef, UpdatedAt: row.UpdatedAt}, true, nil
}

func fabricOperationFromEnt(row *fabricent.FabricOperation) FabricOperation {
	operation := FabricOperation{
		ID:                row.ID,
		OperationID:       row.OperationID,
		CallerService:     row.CallerService,
		Action:            row.Action,
		ResourceKind:      row.ResourceKind,
		ResourceID:        row.ResourceID,
		AccountID:         row.AccountID,
		WorkspaceID:       row.WorkspaceID,
		Provider:          row.Provider,
		ProviderRequestID: row.ProviderRequestID,
		IdempotencyKey:    row.IdempotencyKey,
		RequestHash:       row.RequestHash,
		Status:            row.Status,
		ErrorCode:         row.ErrorCode,
		Retryable:         row.Retryable,
		StartedAt:         row.StartedAt,
		CreatedAt:         row.CreatedAt,
	}
	if row.FinishedAt != nil {
		operation.FinishedAt = *row.FinishedAt
	}
	if row.RedactedProviderPayload != "" {
		_ = json.Unmarshal([]byte(row.RedactedProviderPayload), &operation.RedactedProviderPayload)
	}
	return operation
}
