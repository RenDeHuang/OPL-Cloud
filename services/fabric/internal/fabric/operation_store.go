package fabric

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"sync"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/lib/pq"

	fabricent "opl-cloud/services/fabric/ent"
	"opl-cloud/services/fabric/ent/fabricoperation"
	"opl-cloud/services/fabric/ent/machineownership"
	"opl-cloud/services/internal/postgresmigrate"
)

var ErrOperationNotFound = errors.New("fabric_operation_not_found")
var ErrOperationIdentityConflict = errors.New("fabric_operation_identity_conflict")

type OperationStore interface {
	Append(ctx context.Context, operation FabricOperation) error
	Get(ctx context.Context, id string) (FabricOperation, error)
	OperationByActionIdempotency(ctx context.Context, action, idempotencyKey string) (FabricOperation, bool, error)
	ComputeClaimTerminalOperation(ctx context.Context, approvalID, idempotencyKey string) (FabricOperation, bool, error)
	ClaimRuntime(ctx context.Context, operation FabricOperation) (FabricOperation, bool, error)
	ClaimComputePoolRuntime(ctx context.Context, operation FabricOperation) (FabricOperation, bool, error)
	ReclaimRuntime(ctx context.Context, id string, priorStartedAt, startedAt time.Time) (FabricOperation, bool, error)
	ComputePoolHead(ctx context.Context, poolKey string) (FabricOperation, bool, error)
	TryClaimComputePoolHead(ctx context.Context, operationID, poolKey, leaseOwner string, now, leaseExpiresAt time.Time) (FabricOperation, bool, error)
	ReleaseComputePoolHead(ctx context.Context, operationID, poolKey, leaseOwner string) error
	SaveRuntime(ctx context.Context, operation FabricOperation) error
	SaveComputeClaimRecovery(ctx context.Context, current, next FabricOperation) error
	List(ctx context.Context) ([]FabricOperation, error)
	ClaimMachine(ctx context.Context, ownership MachineOwnership) (MachineOwnership, bool, error)
	SaveMachineOwnership(ctx context.Context, ownership MachineOwnership) error
	MachineOwnership(ctx context.Context, resourceID string) (MachineOwnership, error)
	ListMachineOwnerships(ctx context.Context) ([]MachineOwnership, error)
	WithPoolLock(ctx context.Context, poolKey string, fn func(context.Context) error) error
}

// runtimeReadbackConverger is deliberately optional.  It is a separate CAS
// path so a provider readback can confirm an already attempted write without
// weakening SaveRuntime's owner lease semantics or re-running an apply.
type runtimeReadbackConverger interface {
	ConvergeRuntimeReadback(ctx context.Context, expected, next FabricOperation) error
}

type MemoryOperationStore struct {
	mu                sync.Mutex
	operation         []FabricOperation
	machineOwnerships map[string]MachineOwnership
	poolLocks         map[string]*sync.Mutex
}

func NewMemoryOperationStore() *MemoryOperationStore {
	return &MemoryOperationStore{machineOwnerships: map[string]MachineOwnership{}, poolLocks: map[string]*sync.Mutex{}}
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

func computePoolLockKey(poolKey string) string {
	return "fabric-pool:" + poolKey
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

func (s *MemoryOperationStore) Get(_ context.Context, id string) (FabricOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := len(s.operation) - 1; index >= 0; index-- {
		if s.operation[index].ID == id {
			return s.operation[index], nil
		}
	}
	return FabricOperation{}, ErrOperationNotFound
}

func (s *MemoryOperationStore) OperationByActionIdempotency(_ context.Context, action, idempotencyKey string) (FabricOperation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var match FabricOperation
	found := false
	for _, operation := range s.operation {
		if operation.Action != action || operation.IdempotencyKey != idempotencyKey {
			continue
		}
		if found {
			return FabricOperation{}, false, ErrOperationIdentityConflict
		}
		match, found = operation, true
	}
	return match, found, nil
}

func (s *MemoryOperationStore) ComputeClaimTerminalOperation(_ context.Context, approvalID, idempotencyKey string) (FabricOperation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var match FabricOperation
	found := false
	for _, operation := range s.operation {
		if operation.Action != "create_compute_allocation" || !computeClaimTerminalIdentityMatches(operation, approvalID, idempotencyKey) {
			continue
		}
		if found {
			return FabricOperation{}, false, ErrOperationIdentityConflict
		}
		match, found = operation, true
	}
	return match, found, nil
}

func computeClaimTerminalIdentityMatches(operation FabricOperation, approvalID, idempotencyKey string) bool {
	value, present := operation.RedactedProviderPayload[computeClaimTerminalEvidencePayloadKey]
	if !present {
		return false
	}
	body, err := json.Marshal(value)
	if err != nil {
		return false
	}
	var identity struct {
		OperatorApprovalID     string `json:"operatorApprovalId"`
		OperatorIdempotencyKey string `json:"operatorIdempotencyKey"`
	}
	if json.Unmarshal(body, &identity) != nil {
		return false
	}
	return identity.OperatorApprovalID == approvalID || identity.OperatorIdempotencyKey == idempotencyKey
}

func (s *MemoryOperationStore) ClaimRuntime(_ context.Context, operation FabricOperation) (FabricOperation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claimRuntimeLocked(operation)
}

func (s *MemoryOperationStore) ClaimComputePoolRuntime(_ context.Context, operation FabricOperation) (FabricOperation, bool, error) {
	if strings.TrimSpace(operation.ComputePoolKey) == "" {
		return FabricOperation{}, false, fmt.Errorf("compute_pool_key_required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	operation.CreatedAt = time.Now().UTC()
	return s.claimRuntimeLocked(operation)
}

func (s *MemoryOperationStore) claimRuntimeLocked(operation FabricOperation) (FabricOperation, bool, error) {
	for index := len(s.operation) - 1; index >= 0; index-- {
		existing := s.operation[index]
		if existing.Action == operation.Action && existing.IdempotencyKey == operation.IdempotencyKey && existing.Status != "rejected" {
			if existing.RequestHash == operation.RequestHash && existing.ComputePoolKey == "" && operation.ComputePoolKey != "" {
				existing.ComputePoolKey = operation.ComputePoolKey
				s.operation[index] = existing
			}
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

func (s *MemoryOperationStore) TryClaimComputePoolHead(_ context.Context, operationID, poolKey, leaseOwner string, now, leaseExpiresAt time.Time) (FabricOperation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	head := memoryComputePoolHeadIndex(s.operation, poolKey)
	if head < 0 {
		return FabricOperation{}, false, fmt.Errorf("compute_pool_head_not_found")
	}
	current := s.operation[head]
	if current.ID != operationID {
		return current, false, nil
	}
	if current.Status != "started" {
		return current, false, nil
	}
	if current.ComputePoolLeaseOwner != "" && current.ComputePoolLeaseOwner != leaseOwner && current.ComputePoolLeaseExpires != nil && current.ComputePoolLeaseExpires.After(now) {
		return current, false, nil
	}
	current.ComputePoolLeaseOwner = leaseOwner
	current.ComputePoolLeaseExpires = &leaseExpiresAt
	s.operation[head] = current
	return current, true, nil
}

func (s *MemoryOperationStore) ComputePoolHead(_ context.Context, poolKey string) (FabricOperation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	head := memoryComputePoolHeadIndex(s.operation, poolKey)
	if head < 0 {
		return FabricOperation{}, false, nil
	}
	return s.operation[head], true, nil
}

func memoryComputePoolHeadIndex(operations []FabricOperation, poolKey string) int {
	head := -1
	for index := range operations {
		operation := operations[index]
		if operation.Action != "create_compute_allocation" || !computePoolHeadStatus(operation.Status) || operation.ComputePoolKey != poolKey {
			continue
		}
		if head < 0 || operation.CreatedAt.Before(operations[head].CreatedAt) || (operation.CreatedAt.Equal(operations[head].CreatedAt) && operation.ID < operations[head].ID) {
			head = index
		}
	}
	return head
}

func (s *MemoryOperationStore) ReleaseComputePoolHead(_ context.Context, operationID, poolKey, leaseOwner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.operation {
		operation := s.operation[index]
		if operation.ID != operationID {
			continue
		}
		if operation.ComputePoolKey != poolKey || operation.ComputePoolLeaseOwner != leaseOwner {
			return ErrRuntimeOperationNotCurrent
		}
		operation.ComputePoolLeaseOwner = ""
		operation.ComputePoolLeaseExpires = nil
		s.operation[index] = operation
		return nil
	}
	return fmt.Errorf("runtime_operation_not_found")
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

func sameRuntimeReadbackIdentity(current, expected FabricOperation) bool {
	return current.ID == expected.ID && current.OperationID == expected.OperationID &&
		current.CallerService == expected.CallerService && current.Action == expected.Action &&
		current.ResourceKind == expected.ResourceKind && current.ResourceID == expected.ResourceID &&
		current.AccountID == expected.AccountID && current.WorkspaceID == expected.WorkspaceID &&
		current.Provider == expected.Provider && current.ProviderRequestID == expected.ProviderRequestID &&
		current.IdempotencyKey == expected.IdempotencyKey && current.RequestHash == expected.RequestHash &&
		current.Status == expected.Status && current.StartedAt.Equal(expected.StartedAt)
}

func validRuntimeReadbackConvergence(expected, next FabricOperation) bool {
	return expected.ID != "" && (expected.Status == "started" || expected.Status == "failed") && next.ID == expected.ID &&
		next.Status == "succeeded" && !next.FinishedAt.IsZero() &&
		next.OperationID == expected.OperationID && next.CallerService == expected.CallerService &&
		next.Action == expected.Action && next.ResourceKind == expected.ResourceKind &&
		next.AccountID == expected.AccountID && next.WorkspaceID == expected.WorkspaceID &&
		next.IdempotencyKey == expected.IdempotencyKey && next.RequestHash == expected.RequestHash &&
		next.StartedAt.Equal(expected.StartedAt)
}

func (s *MemoryOperationStore) SaveRuntime(_ context.Context, operation FabricOperation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.operation {
		if s.operation[index].ID == operation.ID {
			if s.operation[index].Status != "started" || !s.operation[index].StartedAt.Equal(operation.StartedAt) ||
				(operation.ComputePoolKey != "" && (operation.ComputePoolLeaseOwner == "" || s.operation[index].ComputePoolLeaseOwner != operation.ComputePoolLeaseOwner)) {
				return ErrRuntimeOperationNotCurrent
			}
			if operation.Status != "started" {
				operation.ComputePoolLeaseOwner = ""
				operation.ComputePoolLeaseExpires = nil
			}
			s.operation[index] = operation
			return nil
		}
	}
	return fmt.Errorf("runtime_operation_not_found")
}

func (s *MemoryOperationStore) ConvergeRuntimeReadback(_ context.Context, expected, next FabricOperation) error {
	if !validRuntimeReadbackConvergence(expected, next) {
		return ErrRuntimeOperationNotCurrent
	}
	expectedPayload, err := operationPayloadJSON(expected)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.operation {
		current := s.operation[index]
		currentPayload, payloadErr := operationPayloadJSON(current)
		if payloadErr != nil {
			return payloadErr
		}
		if current.ID == expected.ID && sameRuntimeReadbackIdentity(current, expected) && currentPayload == expectedPayload {
			s.operation[index] = next
			return nil
		}
	}
	return ErrRuntimeOperationNotCurrent
}

func (s *MemoryOperationStore) SaveComputeClaimRecovery(_ context.Context, expected, next FabricOperation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validComputeClaimRecoveryTransition(expected, next) || !sameComputeClaimRecoveryOperation(expected, next) ||
		!validComputeClaimRecoveryBindingTransition(expected, next) || !validComputeClaimRecoveryMutationTransition(expected, next) ||
		!validComputeClaimRecoveryReconciliationTransition(expected, next) || !validComputeClaimTerminalTransition(expected, next) {
		return ErrRuntimeOperationNotCurrent
	}
	expectedPayload, err := operationPayloadJSON(expected)
	if err != nil {
		return err
	}
	for index := range s.operation {
		current := s.operation[index]
		if current.ID != next.ID {
			continue
		}
		currentPayload, err := operationPayloadJSON(current)
		if err != nil {
			return err
		}
		if current.Status != expected.Status || !sameComputeClaimRecoveryOperation(current, expected) || currentPayload != expectedPayload {
			return ErrRuntimeOperationNotCurrent
		}
		s.operation[index] = next
		return nil
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
	if err := postgresmigrate.ValidateTLS(databaseURL); err != nil {
		return nil, err
	}
	return newPostgresOperationStore(databaseURL)
}

func newTestPostgresOperationStore(databaseURL string) (*PostgresOperationStore, error) {
	return newPostgresOperationStore(databaseURL)
}

func newPostgresOperationStore(databaseURL string) (*PostgresOperationStore, error) {
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
		SetComputePoolKey(operation.ComputePoolKey).
		SetComputePoolLeaseOwner(operation.ComputePoolLeaseOwner).
		SetNillableComputePoolLeaseExpiresAt(operation.ComputePoolLeaseExpires).
		SetStartedAt(operation.StartedAt).
		SetCreatedAt(operation.CreatedAt)
	if !operation.FinishedAt.IsZero() {
		create.SetFinishedAt(operation.FinishedAt)
	}
	return create.Exec(ctx)
}

func (s *PostgresOperationStore) Get(ctx context.Context, id string) (FabricOperation, error) {
	row, err := s.client.FabricOperation.Get(ctx, id)
	if fabricent.IsNotFound(err) {
		return FabricOperation{}, ErrOperationNotFound
	}
	if err != nil {
		return FabricOperation{}, err
	}
	return fabricOperationFromEnt(row), nil
}

func (s *PostgresOperationStore) OperationByActionIdempotency(ctx context.Context, action, idempotencyKey string) (FabricOperation, bool, error) {
	rows, err := s.client.FabricOperation.Query().
		Where(fabricoperation.Action(action), fabricoperation.IdempotencyKey(idempotencyKey)).
		Limit(2).
		All(ctx)
	if err != nil {
		return FabricOperation{}, false, err
	}
	if len(rows) == 0 {
		return FabricOperation{}, false, nil
	}
	if len(rows) != 1 {
		return FabricOperation{}, false, ErrOperationIdentityConflict
	}
	return fabricOperationFromEnt(rows[0]), true, nil
}

func (s *PostgresOperationStore) ComputeClaimTerminalOperation(ctx context.Context, approvalID, idempotencyKey string) (FabricOperation, bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, operation_id, caller_service, action, resource_kind, resource_id, account_id, workspace_id,
			provider, provider_request_id, idempotency_key, request_hash, redacted_provider_payload, status,
			error_code, retryable, compute_pool_key, compute_pool_lease_owner, compute_pool_lease_expires_at,
			started_at, finished_at, created_at
		FROM fabric_operations
		WHERE action = 'create_compute_allocation' AND (
			redacted_provider_payload::jsonb #>> '{computeClaimTerminalEvidence,operatorApprovalId}' = $1 OR
			redacted_provider_payload::jsonb #>> '{computeClaimTerminalEvidence,operatorIdempotencyKey}' = $2
		)
		ORDER BY created_at DESC, id DESC
		LIMIT 2`, approvalID, idempotencyKey)
	if err != nil {
		return FabricOperation{}, false, err
	}
	defer rows.Close()
	var match FabricOperation
	count := 0
	for rows.Next() {
		operation, scanErr := scanPostgresFabricOperation(rows)
		if scanErr != nil {
			return FabricOperation{}, false, scanErr
		}
		match = operation
		count++
	}
	if err := rows.Err(); err != nil {
		return FabricOperation{}, false, err
	}
	if count == 0 {
		return FabricOperation{}, false, nil
	}
	if count != 1 {
		return FabricOperation{}, false, ErrOperationIdentityConflict
	}
	return match, true, nil
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
	key := computePoolLockKey(poolKey)
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
		if existing.RequestHash == operation.RequestHash && existing.ComputePoolKey == "" && operation.ComputePoolKey != "" {
			updated, updateErr := s.client.FabricOperation.Update().
				Where(fabricoperation.ID(existing.ID), fabricoperation.ComputePoolKey(""), fabricoperation.RequestHash(operation.RequestHash)).
				SetComputePoolKey(operation.ComputePoolKey).
				Save(ctx)
			if updateErr != nil {
				return FabricOperation{}, false, updateErr
			}
			if updated == 1 {
				existing, err = s.client.FabricOperation.Get(ctx, existing.ID)
				if err != nil {
					return FabricOperation{}, false, err
				}
			}
		}
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
			error_code, retryable, compute_pool_key, compute_pool_lease_owner, compute_pool_lease_expires_at, started_at, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)
		RETURNING started_at`,
		operation.ID, operation.OperationID, operation.CallerService, operation.Action, operation.ResourceKind, operation.ResourceID,
		operation.AccountID, operation.WorkspaceID, operation.Provider, operation.ProviderRequestID, operation.IdempotencyKey,
		operation.RequestHash, payloadJSON, operation.Status, operation.ErrorCode, operation.Retryable, operation.ComputePoolKey,
		operation.ComputePoolLeaseOwner, operation.ComputePoolLeaseExpires, operation.StartedAt, operation.CreatedAt,
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

func (s *PostgresOperationStore) ClaimComputePoolRuntime(ctx context.Context, operation FabricOperation) (FabricOperation, bool, error) {
	poolKey := strings.TrimSpace(operation.ComputePoolKey)
	if poolKey == "" {
		return FabricOperation{}, false, fmt.Errorf("compute_pool_key_required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FabricOperation{}, false, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", computePoolLockKey(poolKey)); err != nil {
		return FabricOperation{}, false, err
	}
	existing, err := postgresFabricOperationByClaim(ctx, tx, operation.Action, operation.IdempotencyKey)
	if err == nil {
		if existing.RequestHash == operation.RequestHash && existing.ComputePoolKey == "" {
			result, updateErr := tx.ExecContext(ctx, `
				UPDATE fabric_operations SET compute_pool_key = $1
				WHERE id = $2 AND request_hash = $3 AND compute_pool_key = ''`, poolKey, existing.ID, operation.RequestHash)
			if updateErr != nil {
				return FabricOperation{}, false, updateErr
			}
			updated, updateErr := result.RowsAffected()
			if updateErr != nil {
				return FabricOperation{}, false, updateErr
			}
			if updated == 1 {
				existing.ComputePoolKey = poolKey
			}
		}
		if err := tx.Commit(); err != nil {
			return FabricOperation{}, false, err
		}
		committed = true
		return existing, false, nil
	}
	if err != sql.ErrNoRows {
		return FabricOperation{}, false, err
	}
	payloadJSON, err := operationPayloadJSON(operation)
	if err != nil {
		return FabricOperation{}, false, err
	}
	err = tx.QueryRowContext(ctx, `
		INSERT INTO fabric_operations (
			id, operation_id, caller_service, action, resource_kind, resource_id, account_id, workspace_id,
			provider, provider_request_id, idempotency_key, request_hash, redacted_provider_payload, status,
			error_code, retryable, compute_pool_key, compute_pool_lease_owner, compute_pool_lease_expires_at, started_at, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, clock_timestamp())
		ON CONFLICT (id) DO NOTHING
		RETURNING started_at, created_at`,
		operation.ID, operation.OperationID, operation.CallerService, operation.Action, operation.ResourceKind, operation.ResourceID,
		operation.AccountID, operation.WorkspaceID, operation.Provider, operation.ProviderRequestID, operation.IdempotencyKey,
		operation.RequestHash, payloadJSON, operation.Status, operation.ErrorCode, operation.Retryable, operation.ComputePoolKey,
		operation.ComputePoolLeaseOwner, operation.ComputePoolLeaseExpires, operation.StartedAt,
	).Scan(&operation.StartedAt, &operation.CreatedAt)
	claimed := err == nil
	if err == sql.ErrNoRows {
		operation, err = postgresFabricOperationByClaim(ctx, tx, operation.Action, operation.IdempotencyKey)
	}
	if err != nil {
		return FabricOperation{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return FabricOperation{}, false, err
	}
	committed = true
	return operation, claimed, nil
}

func postgresFabricOperationByClaim(ctx context.Context, tx *sql.Tx, action, idempotencyKey string) (FabricOperation, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT id, operation_id, caller_service, action, resource_kind, resource_id, account_id, workspace_id,
			provider, provider_request_id, idempotency_key, request_hash, redacted_provider_payload, status,
			error_code, retryable, compute_pool_key, compute_pool_lease_owner, compute_pool_lease_expires_at,
			started_at, finished_at, created_at
		FROM fabric_operations
		WHERE action = $1 AND idempotency_key = $2 AND status <> 'rejected'
		ORDER BY created_at DESC, id DESC
		LIMIT 1`, action, idempotencyKey)
	return scanPostgresFabricOperation(row)
}

func postgresFabricOperationByPoolHead(ctx context.Context, tx *sql.Tx, poolKey string) (FabricOperation, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT id, operation_id, caller_service, action, resource_kind, resource_id, account_id, workspace_id,
			provider, provider_request_id, idempotency_key, request_hash, redacted_provider_payload, status,
			error_code, retryable, compute_pool_key, compute_pool_lease_owner, compute_pool_lease_expires_at,
			started_at, finished_at, created_at
		FROM fabric_operations
		WHERE action = 'create_compute_allocation' AND status IN ('started', 'claim_pending') AND compute_pool_key = $1
		ORDER BY created_at, id
		LIMIT 1`, poolKey)
	return scanPostgresFabricOperation(row)
}

func computePoolHeadStatus(status string) bool {
	return status == "started" || status == "claim_pending"
}

type postgresRowScanner interface {
	Scan(dest ...any) error
}

func scanPostgresFabricOperation(row postgresRowScanner) (FabricOperation, error) {
	var operation FabricOperation
	var payload string
	var leaseExpiresAt, finishedAt sql.NullTime
	err := row.Scan(
		&operation.ID, &operation.OperationID, &operation.CallerService, &operation.Action, &operation.ResourceKind, &operation.ResourceID,
		&operation.AccountID, &operation.WorkspaceID, &operation.Provider, &operation.ProviderRequestID, &operation.IdempotencyKey,
		&operation.RequestHash, &payload, &operation.Status, &operation.ErrorCode, &operation.Retryable, &operation.ComputePoolKey,
		&operation.ComputePoolLeaseOwner, &leaseExpiresAt, &operation.StartedAt, &finishedAt, &operation.CreatedAt,
	)
	if err != nil {
		return FabricOperation{}, err
	}
	if leaseExpiresAt.Valid {
		operation.ComputePoolLeaseExpires = &leaseExpiresAt.Time
	}
	if finishedAt.Valid {
		operation.FinishedAt = finishedAt.Time
	}
	if payload != "" {
		_ = json.Unmarshal([]byte(payload), &operation.RedactedProviderPayload)
	}
	return operation, nil
}

func (s *PostgresOperationStore) ComputePoolHead(ctx context.Context, poolKey string) (FabricOperation, bool, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, operation_id, caller_service, action, resource_kind, resource_id, account_id, workspace_id,
			provider, provider_request_id, idempotency_key, request_hash, redacted_provider_payload, status,
			error_code, retryable, compute_pool_key, compute_pool_lease_owner, compute_pool_lease_expires_at,
			started_at, finished_at, created_at
		FROM fabric_operations
		WHERE action = 'create_compute_allocation' AND status IN ('started', 'claim_pending') AND compute_pool_key = $1
		ORDER BY created_at, id
		LIMIT 1`, poolKey)
	operation, err := scanPostgresFabricOperation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return FabricOperation{}, false, nil
	}
	if err != nil {
		return FabricOperation{}, false, err
	}
	return operation, true, nil
}

func (s *PostgresOperationStore) TryClaimComputePoolHead(ctx context.Context, operationID, poolKey, leaseOwner string, now, leaseExpiresAt time.Time) (FabricOperation, bool, error) {
	leaseDuration := leaseExpiresAt.Sub(now)
	if leaseDuration <= 0 {
		return FabricOperation{}, false, fmt.Errorf("compute_pool_lease_duration_invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FabricOperation{}, false, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", computePoolLockKey(poolKey)); err != nil {
		return FabricOperation{}, false, err
	}
	current, err := postgresFabricOperationByPoolHead(ctx, tx, poolKey)
	if err != nil {
		if err == sql.ErrNoRows {
			return FabricOperation{}, false, fmt.Errorf("compute_pool_head_not_found")
		}
		return FabricOperation{}, false, err
	}
	claimed := false
	if current.ID == operationID {
		var databaseLeaseExpiresAt time.Time
		updateErr := tx.QueryRowContext(ctx, `
			UPDATE fabric_operations
			SET compute_pool_lease_owner = $1,
				compute_pool_lease_expires_at = clock_timestamp() + ($2 * interval '1 microsecond')
			WHERE id = $3 AND status = 'started' AND compute_pool_key = $4
				AND (compute_pool_lease_owner = '' OR compute_pool_lease_owner = $1
					OR compute_pool_lease_expires_at IS NULL OR compute_pool_lease_expires_at <= clock_timestamp())
			RETURNING compute_pool_lease_expires_at`, leaseOwner, leaseDuration.Microseconds(), operationID, poolKey).Scan(&databaseLeaseExpiresAt)
		if updateErr != nil && updateErr != sql.ErrNoRows {
			return FabricOperation{}, false, updateErr
		}
		if updateErr == nil {
			current.ComputePoolLeaseOwner = leaseOwner
			current.ComputePoolLeaseExpires = &databaseLeaseExpiresAt
			claimed = true
		}
	}
	if err := tx.Commit(); err != nil {
		return FabricOperation{}, false, err
	}
	committed = true
	return current, claimed, nil
}

func (s *PostgresOperationStore) ReleaseComputePoolHead(ctx context.Context, operationID, poolKey, leaseOwner string) error {
	updated, err := s.client.FabricOperation.Update().
		Where(
			fabricoperation.ID(operationID),
			fabricoperation.Status("started"),
			fabricoperation.ComputePoolKey(poolKey),
			fabricoperation.ComputePoolLeaseOwner(leaseOwner),
		).
		SetComputePoolLeaseOwner("").
		ClearComputePoolLeaseExpiresAt().
		Save(ctx)
	if err != nil {
		return err
	}
	if updated != 1 {
		return ErrRuntimeOperationNotCurrent
	}
	return nil
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
		Where(fabricoperation.ID(operation.ID), fabricoperation.Status("started"), fabricoperation.StartedAt(operation.StartedAt))
	if operation.ComputePoolKey != "" {
		if operation.ComputePoolLeaseOwner == "" {
			return ErrRuntimeOperationNotCurrent
		}
		update.Where(fabricoperation.ComputePoolKey(operation.ComputePoolKey), fabricoperation.ComputePoolLeaseOwner(operation.ComputePoolLeaseOwner))
	}
	update.
		SetResourceID(operation.ResourceID).
		SetWorkspaceID(operation.WorkspaceID).
		SetProvider(operation.Provider).
		SetProviderRequestID(operation.ProviderRequestID).
		SetRedactedProviderPayload(payloadJSON).
		SetStatus(operation.Status).
		SetErrorCode(operation.ErrorCode).
		SetRetryable(operation.Retryable)
	if operation.ComputePoolKey != "" {
		update.SetComputePoolKey(operation.ComputePoolKey)
		if operation.Status == "started" {
			update.SetComputePoolLeaseOwner(operation.ComputePoolLeaseOwner).SetNillableComputePoolLeaseExpiresAt(operation.ComputePoolLeaseExpires)
		} else {
			update.SetComputePoolLeaseOwner("").ClearComputePoolLeaseExpiresAt()
		}
	}
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

func (s *PostgresOperationStore) SaveComputeClaimRecovery(ctx context.Context, expected, next FabricOperation) error {
	if !validComputeClaimRecoveryTransition(expected, next) || !sameComputeClaimRecoveryOperation(expected, next) ||
		!validComputeClaimRecoveryBindingTransition(expected, next) || !validComputeClaimRecoveryMutationTransition(expected, next) ||
		!validComputeClaimRecoveryReconciliationTransition(expected, next) || !validComputeClaimTerminalTransition(expected, next) {
		return ErrRuntimeOperationNotCurrent
	}
	expectedPayloadJSON, err := operationPayloadJSON(expected)
	if err != nil {
		return err
	}
	nextPayloadJSON, err := operationPayloadJSON(next)
	if err != nil {
		return err
	}
	update := s.client.FabricOperation.Update().Where(
		fabricoperation.ID(expected.ID), fabricoperation.Status(expected.Status),
		fabricoperation.Action("create_compute_allocation"), fabricoperation.ResourceKind(expected.ResourceKind),
		fabricoperation.ResourceID(expected.ResourceID), fabricoperation.AccountID(expected.AccountID),
		fabricoperation.WorkspaceID(expected.WorkspaceID), fabricoperation.IdempotencyKey(expected.IdempotencyKey),
		fabricoperation.RequestHash(expected.RequestHash),
		func(selector *entsql.Selector) {
			selector.Where(entsql.P(func(builder *entsql.Builder) {
				builder.WriteString(selector.C(fabricoperation.FieldRedactedProviderPayload)).WriteString("::jsonb = ").Arg(expectedPayloadJSON).WriteString("::jsonb")
			}))
		},
	).SetStatus(next.Status).SetErrorCode(next.ErrorCode).SetRetryable(false).
		SetProvider(next.Provider).SetProviderRequestID(next.ProviderRequestID).SetRedactedProviderPayload(nextPayloadJSON)
	if next.Status == "claim_pending" {
		update.ClearFinishedAt()
	} else if next.FinishedAt.IsZero() {
		return ErrRuntimeOperationNotCurrent
	} else {
		update.SetFinishedAt(next.FinishedAt)
	}
	updated, err := update.Save(ctx)
	if err != nil {
		return err
	}
	if updated != 1 {
		return ErrRuntimeOperationNotCurrent
	}
	return nil
}

func (s *PostgresOperationStore) ConvergeRuntimeReadback(ctx context.Context, expected, next FabricOperation) error {
	if !validRuntimeReadbackConvergence(expected, next) {
		return ErrRuntimeOperationNotCurrent
	}
	expectedPayload, err := operationPayloadJSON(expected)
	if err != nil {
		return err
	}
	nextPayload, err := operationPayloadJSON(next)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE fabric_operations
		SET resource_id = $1, workspace_id = $2, provider = $3, provider_request_id = $4,
			redacted_provider_payload = $5::jsonb, status = $6, error_code = $7,
			retryable = false, finished_at = $8
		WHERE id = $9 AND operation_id = $10 AND caller_service = $11 AND action = $12 AND
			resource_kind = $13 AND resource_id = $14 AND account_id = $15 AND workspace_id = $16 AND
			provider = $17 AND provider_request_id = $18 AND idempotency_key = $19 AND request_hash = $20 AND
			status = $21 AND started_at = $22 AND redacted_provider_payload::jsonb = $23::jsonb`,
		next.ResourceID, next.WorkspaceID, next.Provider, next.ProviderRequestID, nextPayload,
		next.Status, next.ErrorCode, next.FinishedAt, expected.ID, expected.OperationID, expected.CallerService,
		expected.Action, expected.ResourceKind, expected.ResourceID, expected.AccountID, expected.WorkspaceID,
		expected.Provider, expected.ProviderRequestID, expected.IdempotencyKey, expected.RequestHash, expected.Status,
		expected.StartedAt, expectedPayload)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return ErrRuntimeOperationNotCurrent
	}
	return nil
}

func validComputeClaimRecoveryTransition(current, next FabricOperation) bool {
	if current.Status == "failed" && next.Status == "claim_pending" ||
		current.Status == "claim_pending" && next.Status == "claim_pending" ||
		current.Status == "claim_pending" && next.Status == "succeeded" ||
		current.Status == "claim_pending" && next.Status == "failed" {
		return true
	}
	if current.Status != "succeeded" || next.Status != "succeeded" {
		return false
	}
	currentLedger, currentPresent, currentValid := decodeComputeClaimRecoveryMutation(current)
	nextLedger, nextPresent, nextValid := decodeComputeClaimRecoveryMutation(next)
	validNodeTransition := !currentPresent && nextPresent && nextValid && nextLedger.State == "node_reserved" &&
		(validLegacyNodeReservationTransition(current, next, nextLedger) || validConfirmedNodeDriftReservationTransition(current, next, nextLedger)) ||
		currentPresent && currentValid && currentLedger.State == "node_reserved" && nextPresent && nextValid &&
			nextLedger.State == "observed" && nextLedger.TencentMutationCount == 0 && currentLedger.Generation == nextLedger.Generation &&
			currentLedger.AttemptDigest == nextLedger.AttemptDigest &&
			reflect.DeepEqual(nextLedger.Evidence.CVM, ComputeClaimMutationEvidence{})
	if !validNodeTransition {
		return false
	}
	currentWithoutPayload, nextWithoutPayload := current, next
	currentWithoutPayload.RedactedProviderPayload, nextWithoutPayload.RedactedProviderPayload = nil, nil
	if !reflect.DeepEqual(currentWithoutPayload, nextWithoutPayload) {
		return false
	}
	currentPayload, nextPayload := maps.Clone(current.RedactedProviderPayload), maps.Clone(next.RedactedProviderPayload)
	delete(currentPayload, computeClaimRecoveryMutationPayloadKey)
	delete(nextPayload, computeClaimRecoveryMutationPayloadKey)
	return reflect.DeepEqual(currentPayload, nextPayload)
}

func sameComputeClaimRecoveryOperation(current, next FabricOperation) bool {
	return current.ID == next.ID && current.Action == "create_compute_allocation" && current.Action == next.Action &&
		current.ResourceKind == next.ResourceKind && current.ResourceID == next.ResourceID && current.AccountID == next.AccountID &&
		current.WorkspaceID == next.WorkspaceID && current.IdempotencyKey == next.IdempotencyKey && current.RequestHash == next.RequestHash
}

func validComputeClaimRecoveryBindingTransition(current, next FabricOperation) bool {
	currentBinding, currentPresent, currentValid := decodeComputeClaimRecoveryBinding(current)
	nextBinding, nextPresent, nextValid := decodeComputeClaimRecoveryBinding(next)
	if !nextPresent || !nextValid {
		return false
	}
	if currentPresent {
		return currentValid && currentBinding == nextBinding
	}
	if next.Status == "claim_pending" {
		return true
	}
	if next.Status != "failed" {
		return false
	}
	launchOperationID, ok := strings.CutSuffix(current.IdempotencyKey, ":compute")
	return ok && nextBinding.LaunchOperationID == launchOperationID && nextBinding.IdempotencyKey == current.IdempotencyKey && nextBinding.RequestHash == current.RequestHash
}

func validComputeClaimTerminalTransition(current, next FabricOperation) bool {
	if next.Status != "failed" {
		return true
	}
	if current.Status != "claim_pending" || next.FinishedAt.IsZero() {
		return false
	}
	evidence, present, valid := decodeComputeClaimTerminalEvidence(next)
	return present && valid && evidence.FabricRecordID == current.ID && evidence.OperationID == current.OperationID && evidence.IdempotencyKey == current.IdempotencyKey &&
		evidence.RequestHash == current.RequestHash && evidence.ComputeAllocationID == current.ResourceID && evidence.AccountID == current.AccountID && evidence.WorkspaceID == current.WorkspaceID &&
		next.ErrorCode == evidence.ErrorCode
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
		ID:                      row.ID,
		OperationID:             row.OperationID,
		CallerService:           row.CallerService,
		Action:                  row.Action,
		ResourceKind:            row.ResourceKind,
		ResourceID:              row.ResourceID,
		AccountID:               row.AccountID,
		WorkspaceID:             row.WorkspaceID,
		Provider:                row.Provider,
		ProviderRequestID:       row.ProviderRequestID,
		IdempotencyKey:          row.IdempotencyKey,
		RequestHash:             row.RequestHash,
		Status:                  row.Status,
		ErrorCode:               row.ErrorCode,
		Retryable:               row.Retryable,
		ComputePoolKey:          row.ComputePoolKey,
		ComputePoolLeaseOwner:   row.ComputePoolLeaseOwner,
		ComputePoolLeaseExpires: row.ComputePoolLeaseExpiresAt,
		StartedAt:               row.StartedAt,
		CreatedAt:               row.CreatedAt,
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
