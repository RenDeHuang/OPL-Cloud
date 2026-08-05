package fabric

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"opl-cloud/services/fabric/ent/contenttransfer"
	"opl-cloud/services/fabric/ent/contenttransferchunk"
)

func TestProductionPostgresOperationStoreRejectsUnsafeTLSBeforeConnecting(t *testing.T) {
	_, err := NewPostgresOperationStore("host=/does-not-exist dbname=opl sslmode=disable")
	if err == nil || !strings.Contains(err.Error(), "sslmode=verify-full") {
		t.Fatalf("unsafe PostgreSQL error = %v", err)
	}
}

func TestMemoryOperationStoreReclaimRuntimeFencesOldOwner(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryOperationStore()
	oldStartedAt := time.Date(2026, 7, 17, 0, 0, 0, 123456000, time.UTC)
	operation := newOperation("create_workspace_runtime", "workspace_runtime", "workspace-alpha", "acct-alpha", "workspace-alpha", "runtime-fence", "request-hash", oldStartedAt)
	operation.ID = "fop-runtime-fence"
	operation.Status = "started"
	operation.ErrorCode = "stale_error"
	operation.FinishedAt = oldStartedAt.Add(time.Second)
	operation.CreatedAt = oldStartedAt
	oldOwner, claimed, err := store.ClaimRuntime(ctx, operation)
	if err != nil || !claimed {
		t.Fatalf("claim old owner=%#v claimed=%v err=%v", oldOwner, claimed, err)
	}

	newStartedAt := oldStartedAt.Add(3 * time.Minute)
	newOwner, won, err := store.ReclaimRuntime(ctx, oldOwner.ID, oldOwner.StartedAt, newStartedAt)
	if err != nil || !won || !newOwner.StartedAt.Equal(newStartedAt) || !newOwner.FinishedAt.IsZero() || newOwner.ErrorCode != "" {
		t.Fatalf("reclaim new owner=%#v won=%v err=%v", newOwner, won, err)
	}
	current, won, err := store.ReclaimRuntime(ctx, oldOwner.ID, oldOwner.StartedAt, newStartedAt.Add(time.Second))
	if err != nil || won || !current.StartedAt.Equal(newStartedAt) {
		t.Fatalf("losing reclaim current=%#v won=%v err=%v", current, won, err)
	}

	oldOwner.Status = "succeeded"
	oldOwner.FinishedAt = newStartedAt.Add(time.Second)
	oldOwner.RedactedProviderPayload = map[string]any{"resource": WorkspaceRuntime{ID: "runtime-old", WorkspaceID: "workspace-alpha"}}
	if err := store.SaveRuntime(ctx, oldOwner); !errors.Is(err, ErrRuntimeOperationNotCurrent) {
		t.Fatalf("old owner save error=%v, want ErrRuntimeOperationNotCurrent", err)
	}
	newOwner.Status = "succeeded"
	newOwner.FinishedAt = newStartedAt.Add(2 * time.Second)
	newOwner.RedactedProviderPayload = map[string]any{"resource": WorkspaceRuntime{ID: "runtime-current", WorkspaceID: "workspace-alpha"}}
	if err := store.SaveRuntime(ctx, newOwner); err != nil {
		t.Fatalf("new owner save: %v", err)
	}
	operations, err := store.List(ctx)
	if err != nil || len(operations) != 1 || operations[0].Status != "succeeded" || !operations[0].StartedAt.Equal(newStartedAt) {
		t.Fatalf("final operations=%#v err=%v", operations, err)
	}
	var runtime WorkspaceRuntime
	if !decodeOperationResource(operations[0], &runtime) || runtime.ID != "runtime-current" {
		t.Fatalf("old owner overwrote current result: runtime=%#v operation=%#v", runtime, operations[0])
	}
}

func TestMemoryOperationStoreComputePoolAdmissionIsFIFOAndFencesExpiredOwner(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryOperationStore()
	createdAt := time.Date(2026, 7, 26, 1, 0, 0, 0, time.UTC)
	first := newOperation("create_compute_allocation", "compute_allocation", "compute-first", "acct-alpha", "workspace-first", "compute-first", "hash-first", createdAt)
	first.ID = "fop-compute-first"
	first.Status = "started"
	first.CreatedAt = createdAt
	first.ComputePoolKey = "np-basic"
	second := newOperation("create_compute_allocation", "compute_allocation", "compute-second", "acct-beta", "workspace-second", "compute-second", "hash-second", createdAt.Add(time.Second))
	second.ID = "fop-compute-second"
	second.Status = "started"
	second.CreatedAt = createdAt.Add(time.Second)
	second.ComputePoolKey = "np-basic"
	for _, operation := range []FabricOperation{first, second} {
		if _, claimed, err := store.ClaimComputePoolRuntime(ctx, operation); err != nil || !claimed {
			t.Fatalf("seed compute operation %s: claimed=%v err=%v", operation.ID, claimed, err)
		}
	}

	if queued, claimed, err := store.TryClaimComputePoolHead(ctx, second.ID, "np-basic", "lease-second", createdAt, createdAt.Add(time.Minute)); err != nil || claimed || queued.ID != first.ID {
		t.Fatalf("non-head claim=%#v claimed=%v err=%v", queued, claimed, err)
	}
	firstOwner, claimed, err := store.TryClaimComputePoolHead(ctx, first.ID, "np-basic", "lease-first", createdAt, createdAt.Add(time.Minute))
	if err != nil || !claimed || firstOwner.ComputePoolLeaseOwner != "lease-first" {
		t.Fatalf("first head claim=%#v claimed=%v err=%v", firstOwner, claimed, err)
	}
	if current, claimed, err := store.TryClaimComputePoolHead(ctx, first.ID, "np-basic", "lease-other", createdAt.Add(30*time.Second), createdAt.Add(90*time.Second)); err != nil || claimed || current.ComputePoolLeaseOwner != "lease-first" {
		t.Fatalf("live lease steal=%#v claimed=%v err=%v", current, claimed, err)
	}
	secondOwner, claimed, err := store.TryClaimComputePoolHead(ctx, first.ID, "np-basic", "lease-other", createdAt.Add(time.Minute), createdAt.Add(2*time.Minute))
	if err != nil || !claimed || secondOwner.ComputePoolLeaseOwner != "lease-other" {
		t.Fatalf("expired lease reclaim=%#v claimed=%v err=%v", secondOwner, claimed, err)
	}

	firstOwner.Status = "succeeded"
	firstOwner.FinishedAt = createdAt.Add(70 * time.Second)
	if err := store.SaveRuntime(ctx, firstOwner); !errors.Is(err, ErrRuntimeOperationNotCurrent) {
		t.Fatalf("expired owner save error=%v, want ErrRuntimeOperationNotCurrent", err)
	}
	secondOwner.Status = "succeeded"
	secondOwner.FinishedAt = createdAt.Add(80 * time.Second)
	if err := store.SaveRuntime(ctx, secondOwner); err != nil {
		t.Fatalf("current owner save: %v", err)
	}
	if queued, claimed, err := store.TryClaimComputePoolHead(ctx, second.ID, "np-basic", "lease-second", createdAt.Add(90*time.Second), createdAt.Add(3*time.Minute)); err != nil || !claimed || queued.ID != second.ID {
		t.Fatalf("successor claim=%#v claimed=%v err=%v", queued, claimed, err)
	}
}

func TestMemoryOperationStoreComputeClaimPendingKeepsFIFOHead(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryOperationStore()
	createdAt := time.Date(2026, 7, 26, 1, 0, 0, 0, time.UTC)
	first := newOperation("create_compute_allocation", "compute_allocation", "compute-first", "acct-alpha", "workspace-first", "compute-first", "hash-first", createdAt)
	first.ID = "fop-compute-first"
	first.Status = "started"
	first.CreatedAt = createdAt
	first.ComputePoolKey = "np-basic"
	second := newOperation("create_compute_allocation", "compute_allocation", "compute-second", "acct-beta", "workspace-second", "compute-second", "hash-second", createdAt.Add(time.Second))
	second.ID = "fop-compute-second"
	second.Status = "started"
	second.CreatedAt = createdAt.Add(time.Second)
	second.ComputePoolKey = "np-basic"
	for _, operation := range []FabricOperation{first, second} {
		if _, claimed, err := store.ClaimComputePoolRuntime(ctx, operation); err != nil || !claimed {
			t.Fatalf("seed compute operation %s: claimed=%v err=%v", operation.ID, claimed, err)
		}
	}

	head, claimed, err := store.TryClaimComputePoolHead(ctx, first.ID, "np-basic", "lease-first", createdAt, createdAt.Add(time.Minute))
	if err != nil || !claimed {
		t.Fatalf("claim first head=%#v claimed=%v err=%v", head, claimed, err)
	}
	head.Status = "claim_pending"
	head.FinishedAt = time.Time{}
	if err := store.SaveRuntime(ctx, head); err != nil {
		t.Fatalf("persist claim_pending head: %v", err)
	}

	queued, claimed, err := store.TryClaimComputePoolHead(ctx, second.ID, "np-basic", "lease-second", createdAt.Add(time.Minute), createdAt.Add(2*time.Minute))
	if err != nil || claimed || queued.ID != first.ID || queued.Status != "claim_pending" {
		t.Fatalf("claim_pending head was bypassed: queued=%#v claimed=%v err=%v", queued, claimed, err)
	}
}

func TestPostgresOperationSchemaDefinesFabricOperationsAuditTable(t *testing.T) {
	schema := PostgresOperationSchemaSQL()
	for _, marker := range []string{
		"CREATE TABLE IF NOT EXISTS fabric_operations",
		"operation_id TEXT NOT NULL",
		"caller_service TEXT NOT NULL",
		"resource_kind TEXT NOT NULL",
		"provider_request_id TEXT NOT NULL DEFAULT ''",
		"request_hash TEXT NOT NULL DEFAULT ''",
		"redacted_provider_payload TEXT NOT NULL DEFAULT '{}'",
		"CREATE INDEX IF NOT EXISTS fabric_operations_resource_idx",
		"CREATE UNIQUE INDEX IF NOT EXISTS fabric_operations_runtime_claim_idx",
		"compute_pool_key TEXT NOT NULL DEFAULT ''",
		"compute_pool_lease_owner TEXT NOT NULL DEFAULT ''",
		"compute_pool_lease_expires_at TIMESTAMPTZ",
		"CREATE INDEX IF NOT EXISTS fabric_operations_compute_pool_head_idx",
		"CREATE UNIQUE INDEX IF NOT EXISTS fabric_operations_compute_claim_idx",
	} {
		if !strings.Contains(schema, marker) {
			t.Fatalf("schema missing %q", marker)
		}
	}
	if strings.Contains(schema, "JSONB") {
		t.Fatalf("fabric schema must not keep JSONB fact columns")
	}
}

func TestRuntimeClaimMigrationMatchesEmbeddedCopy(t *testing.T) {
	formal, err := os.ReadFile("../../migrations/202607110003_runtime_operation_claim.sql")
	if err != nil {
		t.Fatalf("read formal migration: %v", err)
	}
	embedded, err := os.ReadFile("ent_migrations/202607110003_runtime_operation_claim.sql")
	if err != nil {
		t.Fatalf("read embedded migration: %v", err)
	}
	if !bytes.Equal(formal, embedded) {
		t.Fatal("formal and embedded runtime claim migrations differ")
	}
}

func TestComputePoolAdmissionMigrationMatchesEmbeddedCopy(t *testing.T) {
	formal, err := os.ReadFile("../../migrations/202607260001_compute_pool_admission.sql")
	if err != nil {
		t.Fatalf("read formal migration: %v", err)
	}
	embedded, err := os.ReadFile("ent_migrations/202607260001_compute_pool_admission.sql")
	if err != nil {
		t.Fatalf("read embedded migration: %v", err)
	}
	if !bytes.Equal(formal, embedded) {
		t.Fatal("formal and embedded compute pool admission migrations differ")
	}
}

func TestComputeClaimPendingPoolHeadMigrationMatchesEmbeddedCopy(t *testing.T) {
	formal, err := os.ReadFile("../../migrations/202607290001_compute_claim_pending_pool_head.sql")
	if err != nil {
		t.Fatalf("read formal migration: %v", err)
	}
	embedded, err := os.ReadFile("ent_migrations/202607290001_compute_claim_pending_pool_head.sql")
	if err != nil {
		t.Fatalf("read embedded migration: %v", err)
	}
	if !bytes.Equal(formal, embedded) {
		t.Fatal("formal and embedded compute claim pending pool head migrations differ")
	}
}

func TestPostgresOperationSchemaDefinesContentTransferTables(t *testing.T) {
	schema := PostgresOperationSchemaSQL()
	for _, table := range []string{contenttransfer.Table, contenttransferchunk.Table} {
		if !strings.Contains(schema, "CREATE TABLE IF NOT EXISTS "+table) {
			t.Fatalf("schema missing content transfer table %q", table)
		}
	}
}

func TestPostgresOperationSchemaDropsRetiredWorkspaceRuntimeAccessTable(t *testing.T) {
	schema := PostgresOperationSchemaSQL()
	createAt := strings.Index(schema, "CREATE TABLE IF NOT EXISTS fabric_workspace_runtime_access")
	dropAt := strings.Index(schema, "DROP TABLE IF EXISTS fabric_workspace_runtime_access")
	if dropAt < 0 || dropAt < createAt {
		t.Fatal("Fabric hard-cut migration must drop the retired runtime access table")
	}
}

func TestPostgresMigrationChainRejectsStandalonePatchMarkers(t *testing.T) {
	for lineNumber, line := range strings.Split(PostgresOperationSchemaSQL(), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		if strings.Trim(trimmed, "+-@*") == "" {
			t.Fatalf("migration chain line %d is a standalone non-SQL patch marker: %q", lineNumber+1, line)
		}
	}
}
