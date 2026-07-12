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
	ClaimRuntime(ctx context.Context, operation FabricOperation) (FabricOperation, bool, error)
	SaveRuntime(ctx context.Context, operation FabricOperation) error
	List(ctx context.Context) ([]FabricOperation, error)
	CatalogStore
}

type MemoryOperationStore struct {
	mu                   sync.Mutex
	operation            []FabricOperation
	transferSessions     map[string]Transfer
	transferKeys         map[string]string
	transferChunks       map[string]map[int]TransferChunk
	connectors           map[string]Connector
	environmentTemplates map[string]EnvironmentTemplate
}

func NewMemoryOperationStore() *MemoryOperationStore {
	return &MemoryOperationStore{transferSessions: map[string]Transfer{}, transferKeys: map[string]string{}, transferChunks: map[string]map[int]TransferChunk{}, connectors: map[string]Connector{}, environmentTemplates: map[string]EnvironmentTemplate{}}
}

func (s *MemoryOperationStore) Append(_ context.Context, operation FabricOperation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.operation = append(s.operation, operation)
	return nil
}

func (s *MemoryOperationStore) ClaimRuntime(_ context.Context, operation FabricOperation) (FabricOperation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := len(s.operation) - 1; index >= 0; index-- {
		existing := s.operation[index]
		if existing.Action == operation.Action && existing.IdempotencyKey == operation.IdempotencyKey && existing.Status != "rejected" {
			if existing.Action == "destroy_workspace_runtime" && existing.Status == "failed" {
				operation.ID = existing.ID
				s.operation[index] = operation
				return operation, true, nil
			}
			return existing, false, nil
		}
	}
	s.operation = append(s.operation, operation)
	return operation, true, nil
}

func (s *MemoryOperationStore) SaveRuntime(_ context.Context, operation FabricOperation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.operation {
		if s.operation[index].ID == operation.ID {
			s.operation[index] = operation
			return nil
		}
	}
	return fmt.Errorf("runtime_operation_not_found")
}

func (s *MemoryOperationStore) List(_ context.Context) ([]FabricOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	operations := make([]FabricOperation, len(s.operation))
	copy(operations, s.operation)
	return operations, nil
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
	payloadJSON, err := operationPayloadJSON(operation)
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

func (s *PostgresOperationStore) ClaimRuntime(ctx context.Context, operation FabricOperation) (FabricOperation, bool, error) {
	existing, err := s.client.FabricOperation.Query().
		Where(fabricoperation.Action(operation.Action), fabricoperation.IdempotencyKey(operation.IdempotencyKey), fabricoperation.StatusNEQ("rejected")).
		Order(fabricent.Desc(fabricoperation.FieldCreatedAt, fabricoperation.FieldID)).First(ctx)
	if err == nil {
		if existing.Action == "destroy_workspace_runtime" && existing.Status == "failed" {
			updated, updateErr := s.client.FabricOperation.Update().
				Where(fabricoperation.ID(existing.ID), fabricoperation.Status("failed")).
				SetStatus("started").
				SetErrorCode("").
				SetRetryable(false).
				SetStartedAt(operation.StartedAt).
				ClearFinishedAt().
				Save(ctx)
			if updateErr != nil {
				return FabricOperation{}, false, updateErr
			}
			if updated == 1 {
				operation.ID = existing.ID
				return operation, true, nil
			}
			existing, err = s.client.FabricOperation.Get(ctx, existing.ID)
			if err != nil {
				return FabricOperation{}, false, err
			}
		}
		return fabricOperationFromEnt(existing), false, nil
	}
	if !fabricent.IsNotFound(err) {
		return FabricOperation{}, false, err
	}
	if err := s.Append(ctx, operation); err == nil {
		return operation, true, nil
	}
	concurrent, queryErr := s.client.FabricOperation.Get(ctx, operation.ID)
	if queryErr != nil {
		return FabricOperation{}, false, queryErr
	}
	return fabricOperationFromEnt(concurrent), false, nil
}

func (s *PostgresOperationStore) SaveRuntime(ctx context.Context, operation FabricOperation) error {
	payloadJSON, err := operationPayloadJSON(operation)
	if err != nil {
		return err
	}
	update := s.client.FabricOperation.UpdateOneID(operation.ID).
		SetResourceID(operation.ResourceID).
		SetWorkspaceID(operation.WorkspaceID).
		SetProvider(operation.Provider).
		SetProviderRequestID(operation.ProviderRequestID).
		SetRedactedProviderPayload(payloadJSON).
		SetStatus(operation.Status).
		SetErrorCode(operation.ErrorCode).
		SetRetryable(operation.Retryable)
	if operation.FinishedAt.IsZero() {
		update.ClearFinishedAt()
	} else {
		update.SetFinishedAt(operation.FinishedAt)
	}
	_, err = update.Save(ctx)
	if fabricent.IsNotFound(err) {
		return fmt.Errorf("runtime_operation_not_found")
	}
	return err
}

func operationPayloadJSON(operation FabricOperation) (string, error) {
	payload := operation.RedactedProviderPayload
	if payload == nil {
		payload = map[string]any{}
	}
	data, err := json.Marshal(payload)
	return string(data), err
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
