package fabric

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/lib/pq"

	fabricent "opl-cloud/services/fabric/ent"
	"opl-cloud/services/fabric/ent/contenttransfer"
	"opl-cloud/services/fabric/ent/contenttransferchunk"
	"opl-cloud/services/fabric/ent/fabricoperation"
	"opl-cloud/services/fabric/ent/machineownership"
	"opl-cloud/services/internal/postgresmigrate"
)

type OperationStore interface {
	Append(ctx context.Context, operation FabricOperation) error
	ClaimRuntime(ctx context.Context, operation FabricOperation) (FabricOperation, bool, error)
	ReclaimRuntime(ctx context.Context, id string, priorStartedAt, startedAt time.Time) (FabricOperation, bool, error)
	SaveRuntime(ctx context.Context, operation FabricOperation) error
	List(ctx context.Context) ([]FabricOperation, error)
	ClaimMachine(ctx context.Context, ownership MachineOwnership) (MachineOwnership, bool, error)
	SaveMachineOwnership(ctx context.Context, ownership MachineOwnership) error
	MachineOwnership(ctx context.Context, resourceID string) (MachineOwnership, error)
	ListMachineOwnerships(ctx context.Context) ([]MachineOwnership, error)
	WithPoolLock(ctx context.Context, poolKey string, fn func(context.Context) error) error
}

type MemoryOperationStore struct {
	mu                sync.Mutex
	operation         []FabricOperation
	transferSessions  map[string]Transfer
	transferKeys      map[string]string
	transferChunks    map[string]map[int]TransferChunk
	machineOwnerships map[string]MachineOwnership
	poolLocks         map[string]*sync.Mutex
}

func NewMemoryOperationStore() *MemoryOperationStore {
	return &MemoryOperationStore{transferSessions: map[string]Transfer{}, transferKeys: map[string]string{}, transferChunks: map[string]map[int]TransferChunk{}, machineOwnerships: map[string]MachineOwnership{}, poolLocks: map[string]*sync.Mutex{}}
}

func (s *MemoryOperationStore) WithPoolLock(ctx context.Context, poolKey string, fn func(context.Context) error) error {
	s.mu.Lock()
	lock := s.poolLocks[poolKey]
	if lock == nil {
		lock = &sync.Mutex{}
		s.poolLocks[poolKey] = lock
	}
	s.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()
	return fn(ctx)
}

func (s *MemoryOperationStore) ClaimMachine(_ context.Context, ownership MachineOwnership) (MachineOwnership, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.machineOwnerships[ownership.ResourceID]; ok {
		if existing.Status == "released" {
			if !sameMachineOwnershipResource(existing, ownership) {
				return MachineOwnership{}, false, ErrMachineOwnershipConflict
			}
			for resourceID, candidate := range s.machineOwnerships {
				if resourceID != ownership.ResourceID && (candidate.MachineID == ownership.MachineID || (ownership.InstanceID != "" && candidate.InstanceID == ownership.InstanceID)) {
					return MachineOwnership{}, false, ErrMachineOwnershipConflict
				}
			}
			ownership.ID = existing.ID
			s.machineOwnerships[ownership.ResourceID] = ownership
			return ownership, true, nil
		}
		if !sameMachineOwnershipReplay(existing, ownership) {
			return MachineOwnership{}, false, ErrMachineOwnershipConflict
		}
		return existing, false, nil
	}
	for _, existing := range s.machineOwnerships {
		if existing.MachineID == ownership.MachineID || (ownership.InstanceID != "" && existing.InstanceID == ownership.InstanceID) {
			return MachineOwnership{}, false, ErrMachineOwnershipConflict
		}
	}
	s.machineOwnerships[ownership.ResourceID] = ownership
	return ownership, true, nil
}

func sameMachineOwnershipResource(existing, requested MachineOwnership) bool {
	return existing.ResourceID == requested.ResourceID && existing.AccountID == requested.AccountID && existing.WorkspaceID == requested.WorkspaceID && existing.PackageID == requested.PackageID
}

func sameMachineOwnershipReplay(existing, requested MachineOwnership) bool {
	return sameMachineOwnershipResource(existing, requested) && existing.MachineID == requested.MachineID && existing.InstanceID == requested.InstanceID
}

func (s *MemoryOperationStore) SaveMachineOwnership(_ context.Context, ownership MachineOwnership) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.machineOwnerships[ownership.ResourceID]; !ok {
		return ErrMachineOwnershipNotFound
	}
	s.machineOwnerships[ownership.ResourceID] = ownership
	return nil
}

func (s *MemoryOperationStore) MachineOwnership(_ context.Context, resourceID string) (MachineOwnership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ownership, ok := s.machineOwnerships[resourceID]
	if !ok {
		return MachineOwnership{}, ErrMachineOwnershipNotFound
	}
	return ownership, nil
}

func (s *MemoryOperationStore) ListMachineOwnerships(_ context.Context) ([]MachineOwnership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]MachineOwnership, 0, len(s.machineOwnerships))
	for _, ownership := range s.machineOwnerships {
		out = append(out, ownership)
	}
	return out, nil
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
				if !sameRuntimeOperationRequest(existing, operation) {
					return existing, false, ErrRuntimeIdempotencyConflict
				}
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

func (s *MemoryOperationStore) ReclaimRuntime(_ context.Context, id string, priorStartedAt, startedAt time.Time) (FabricOperation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.operation {
		operation := s.operation[index]
		if operation.ID != id {
			continue
		}
		if operation.Status == "started" && operation.StartedAt.Equal(priorStartedAt) {
			operation.StartedAt = startedAt
			operation.FinishedAt = time.Time{}
			operation.ErrorCode = ""
			operation.Retryable = false
			s.operation[index] = operation
			return operation, true, nil
		}
		return operation, false, nil
	}
	return FabricOperation{}, false, fmt.Errorf("runtime_operation_not_found")
}

func sameRuntimeOperationRequest(existing, requested FabricOperation) bool {
	return existing.Action == requested.Action &&
		existing.ResourceKind == requested.ResourceKind &&
		existing.ResourceID == requested.ResourceID &&
		existing.AccountID == requested.AccountID &&
		existing.WorkspaceID == requested.WorkspaceID &&
		existing.IdempotencyKey == requested.IdempotencyKey &&
		existing.RequestHash == requested.RequestHash
}

func (s *MemoryOperationStore) SaveRuntime(_ context.Context, operation FabricOperation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.operation {
		if s.operation[index].ID == operation.ID {
			if s.operation[index].Status != "started" || !s.operation[index].StartedAt.Equal(operation.StartedAt) {
				return ErrRuntimeOperationNotCurrent
			}
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

type embeddedMigration struct {
	version string
	query   string
}

func fabricEmbeddedMigrations() ([]embeddedMigration, error) {
	entries, err := fabricMigrations.ReadDir("ent_migrations")
	if err != nil {
		return nil, err
	}
	migrations := make([]embeddedMigration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		data, err := fabricMigrations.ReadFile("ent_migrations/" + entry.Name())
		if err != nil {
			return nil, err
		}
		migrations = append(migrations, embeddedMigration{
			version: strings.TrimSuffix(entry.Name(), ".sql"),
			query:   string(data),
		})
	}
	return migrations, nil
}

func PostgresOperationSchemaSQL() string {
	migrations, err := fabricEmbeddedMigrations()
	if err != nil {
		return ""
	}
	var out strings.Builder
	for _, migration := range migrations {
		out.WriteString(migration.query)
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
	embedded, err := fabricEmbeddedMigrations()
	if err != nil {
		return err
	}
	migrations := make([]postgresmigrate.Migration, 0, len(embedded))
	for _, migration := range embedded {
		migration := migration
		migrations = append(migrations, postgresmigrate.Migration{
			Version: migration.version,
			Run: func(ctx context.Context) error {
				_, err := s.db.ExecContext(ctx, migration.query)
				return err
			},
		})
	}
	return postgresmigrate.Apply(ctx, s.db, "fabric", migrations)
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

func (s *PostgresOperationStore) ClaimMachine(ctx context.Context, ownership MachineOwnership) (MachineOwnership, bool, error) {
	existing, err := s.client.MachineOwnership.Query().Where(machineownership.ResourceID(ownership.ResourceID)).Only(ctx)
	if err == nil {
		result := machineOwnershipFromEnt(existing)
		if result.Status == "released" {
			if !sameMachineOwnershipResource(result, ownership) {
				return MachineOwnership{}, false, ErrMachineOwnershipConflict
			}
			ownership.ID = result.ID
			if err := s.SaveMachineOwnership(ctx, ownership); err != nil {
				return MachineOwnership{}, false, err
			}
			return ownership, true, nil
		}
		if !sameMachineOwnershipReplay(result, ownership) {
			return MachineOwnership{}, false, ErrMachineOwnershipConflict
		}
		return result, false, nil
	}
	if !fabricent.IsNotFound(err) {
		return MachineOwnership{}, false, err
	}
	create := s.client.MachineOwnership.Create().
		SetID(ownership.ID).
		SetResourceID(ownership.ResourceID).
		SetAccountID(ownership.AccountID).
		SetWorkspaceID(ownership.WorkspaceID).
		SetPackageID(ownership.PackageID).
		SetNodePoolID(ownership.NodePoolID).
		SetMachineID(ownership.MachineID).
		SetNodeName(ownership.NodeName).
		SetStatus(ownership.Status).
		SetProviderRequestID(ownership.ProviderRequestID).
		SetClaimedAt(ownership.ClaimedAt)
	if ownership.InstanceID != "" {
		create.SetInstanceID(ownership.InstanceID)
	}
	if ownership.ReleasedAt != nil {
		create.SetReleasedAt(*ownership.ReleasedAt)
	}
	created, err := create.Save(ctx)
	if fabricent.IsConstraintError(err) {
		return MachineOwnership{}, false, ErrMachineOwnershipConflict
	}
	if err != nil {
		return MachineOwnership{}, false, err
	}
	return machineOwnershipFromEnt(created), true, nil
}

func (s *PostgresOperationStore) SaveMachineOwnership(ctx context.Context, ownership MachineOwnership) error {
	row, err := s.client.MachineOwnership.Query().Where(machineownership.ResourceID(ownership.ResourceID)).Only(ctx)
	if fabricent.IsNotFound(err) {
		return ErrMachineOwnershipNotFound
	}
	if err != nil {
		return err
	}
	update := s.client.MachineOwnership.UpdateOneID(row.ID).
		SetAccountID(ownership.AccountID).
		SetWorkspaceID(ownership.WorkspaceID).
		SetPackageID(ownership.PackageID).
		SetNodePoolID(ownership.NodePoolID).
		SetMachineID(ownership.MachineID).
		SetNodeName(ownership.NodeName).
		SetStatus(ownership.Status).
		SetProviderRequestID(ownership.ProviderRequestID).
		SetClaimedAt(ownership.ClaimedAt)
	if ownership.InstanceID == "" {
		update.ClearInstanceID()
	} else {
		update.SetInstanceID(ownership.InstanceID)
	}
	if ownership.ReleasedAt == nil {
		update.ClearReleasedAt()
	} else {
		update.SetReleasedAt(*ownership.ReleasedAt)
	}
	if err := update.Exec(ctx); fabricent.IsConstraintError(err) {
		return ErrMachineOwnershipConflict
	} else {
		return err
	}
}

func (s *PostgresOperationStore) MachineOwnership(ctx context.Context, resourceID string) (MachineOwnership, error) {
	row, err := s.client.MachineOwnership.Query().Where(machineownership.ResourceID(resourceID)).Only(ctx)
	if fabricent.IsNotFound(err) {
		return MachineOwnership{}, ErrMachineOwnershipNotFound
	}
	if err != nil {
		return MachineOwnership{}, err
	}
	return machineOwnershipFromEnt(row), nil
}

func (s *PostgresOperationStore) ListMachineOwnerships(ctx context.Context) ([]MachineOwnership, error) {
	rows, err := s.client.MachineOwnership.Query().Order(fabricent.Asc(machineownership.FieldClaimedAt, machineownership.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]MachineOwnership, 0, len(rows))
	for _, row := range rows {
		out = append(out, machineOwnershipFromEnt(row))
	}
	return out, nil
}

func (s *PostgresOperationStore) WithPoolLock(ctx context.Context, poolKey string, fn func(context.Context) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	key := "fabric-pool:" + poolKey
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock(hashtext($1))", key); err != nil {
		return err
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock(hashtext($1))", key) }()
	return fn(ctx)
}

func (s *PostgresOperationStore) ClaimRuntime(ctx context.Context, operation FabricOperation) (FabricOperation, bool, error) {
	existing, err := s.client.FabricOperation.Query().
		Where(fabricoperation.Action(operation.Action), fabricoperation.IdempotencyKey(operation.IdempotencyKey), fabricoperation.StatusNEQ("rejected")).
		Order(fabricent.Desc(fabricoperation.FieldCreatedAt, fabricoperation.FieldID)).First(ctx)
	if err == nil {
		if existing.Action == "destroy_workspace_runtime" && existing.Status == "failed" {
			if !sameRuntimeOperationRequest(fabricOperationFromEnt(existing), operation) {
				return FabricOperation{}, false, ErrRuntimeIdempotencyConflict
			}
			updated, updateErr := s.client.FabricOperation.Update().
				Where(
					fabricoperation.ID(existing.ID),
					fabricoperation.Status("failed"),
					fabricoperation.Action(operation.Action),
					fabricoperation.ResourceKind(operation.ResourceKind),
					fabricoperation.ResourceID(operation.ResourceID),
					fabricoperation.AccountID(operation.AccountID),
					fabricoperation.WorkspaceID(operation.WorkspaceID),
					fabricoperation.IdempotencyKey(operation.IdempotencyKey),
					fabricoperation.RequestHash(operation.RequestHash),
				).
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
				current, getErr := s.client.FabricOperation.Get(ctx, existing.ID)
				if getErr != nil {
					return FabricOperation{}, false, getErr
				}
				return fabricOperationFromEnt(current), true, nil
			}
			existing, err = s.client.FabricOperation.Get(ctx, existing.ID)
			if err != nil {
				return FabricOperation{}, false, err
			}
			if !sameRuntimeOperationRequest(fabricOperationFromEnt(existing), operation) {
				return FabricOperation{}, false, ErrRuntimeIdempotencyConflict
			}
		}
		return fabricOperationFromEnt(existing), false, nil
	}
	if !fabricent.IsNotFound(err) {
		return FabricOperation{}, false, err
	}
	payloadJSON, err := operationPayloadJSON(operation)
	if err != nil {
		return FabricOperation{}, false, err
	}
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO fabric_operations (
			id, operation_id, caller_service, action, resource_kind, resource_id, account_id, workspace_id,
			provider, provider_request_id, idempotency_key, request_hash, redacted_provider_payload, status,
			error_code, retryable, started_at, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
		RETURNING started_at`,
		operation.ID, operation.OperationID, operation.CallerService, operation.Action, operation.ResourceKind, operation.ResourceID,
		operation.AccountID, operation.WorkspaceID, operation.Provider, operation.ProviderRequestID, operation.IdempotencyKey,
		operation.RequestHash, payloadJSON, operation.Status, operation.ErrorCode, operation.Retryable, operation.StartedAt, operation.CreatedAt,
	).Scan(&operation.StartedAt)
	if err == nil {
		return operation, true, nil
	}
	concurrent, queryErr := s.client.FabricOperation.Get(ctx, operation.ID)
	if queryErr != nil {
		return FabricOperation{}, false, queryErr
	}
	return fabricOperationFromEnt(concurrent), false, nil
}

func (s *PostgresOperationStore) ReclaimRuntime(ctx context.Context, id string, priorStartedAt, startedAt time.Time) (FabricOperation, bool, error) {
	existing, err := s.client.FabricOperation.Get(ctx, id)
	if fabricent.IsNotFound(err) {
		return FabricOperation{}, false, fmt.Errorf("runtime_operation_not_found")
	}
	if err != nil {
		return FabricOperation{}, false, err
	}
	err = s.db.QueryRowContext(ctx, `
		UPDATE fabric_operations
		SET started_at = $1, finished_at = NULL, error_code = '', retryable = false
		WHERE id = $2 AND status = 'started' AND started_at = $3
		RETURNING started_at`, startedAt, id, priorStartedAt).Scan(&existing.StartedAt)
	if err == nil {
		existing.FinishedAt = nil
		existing.ErrorCode = ""
		existing.Retryable = false
		return fabricOperationFromEnt(existing), true, nil
	}
	if err != sql.ErrNoRows {
		return FabricOperation{}, false, err
	}
	current, err := s.client.FabricOperation.Get(ctx, id)
	if fabricent.IsNotFound(err) {
		return FabricOperation{}, false, fmt.Errorf("runtime_operation_not_found")
	}
	if err != nil {
		return FabricOperation{}, false, err
	}
	return fabricOperationFromEnt(current), false, nil
}

func (s *PostgresOperationStore) SaveRuntime(ctx context.Context, operation FabricOperation) error {
	payloadJSON, err := operationPayloadJSON(operation)
	if err != nil {
		return err
	}
	update := s.client.FabricOperation.Update().
		Where(fabricoperation.ID(operation.ID), fabricoperation.Status("started"), fabricoperation.StartedAt(operation.StartedAt)).
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
	updated, err := update.Save(ctx)
	if err != nil {
		return err
	}
	if updated == 1 {
		return nil
	}
	_, err = s.client.FabricOperation.Get(ctx, operation.ID)
	if fabricent.IsNotFound(err) {
		return fmt.Errorf("runtime_operation_not_found")
	}
	if err != nil {
		return err
	}
	return ErrRuntimeOperationNotCurrent
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

func machineOwnershipFromEnt(row *fabricent.MachineOwnership) MachineOwnership {
	return MachineOwnership{ID: row.ID, ResourceID: row.ResourceID, AccountID: row.AccountID, WorkspaceID: row.WorkspaceID, PackageID: row.PackageID, NodePoolID: row.NodePoolID, MachineID: row.MachineID, InstanceID: row.InstanceID, NodeName: row.NodeName, Status: row.Status, ProviderRequestID: row.ProviderRequestID, ClaimedAt: row.ClaimedAt, ReleasedAt: row.ReleasedAt}
}
