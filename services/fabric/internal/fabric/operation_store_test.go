package fabric

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestProductionPostgresOperationStoreRejectsUnsafeTLSBeforeConnecting(t *testing.T) {
	_, err := NewPostgresOperationStore("host=/does-not-exist dbname=opl sslmode=disable")
	if err == nil || !strings.Contains(err.Error(), "sslmode=verify-full") {
		t.Fatalf("unsafe PostgreSQL error = %v", err)
	}
}

func TestMemoryOperationStoreReadsExactIdentitiesAndFailsClosedOnDuplicates(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryOperationStore()
	now := time.Date(2026, 8, 12, 1, 0, 0, 0, time.UTC)
	exact := newOperation("create_compute_allocation", "compute_allocation", "ca-exact", "acct-exact", "ws-exact", "launch-exact:compute", "hash-exact", now)
	exact.ID = "fop-exact"
	exact.RedactedProviderPayload = map[string]any{
		computeClaimTerminalEvidencePayloadKey: map[string]any{
			"operatorApprovalId": "approval-exact-30970000001", "operatorIdempotencyKey": "approval-exact-30970000001",
		},
	}
	alias := exact
	alias.ID, alias.IdempotencyKey = "fop-alias", "launch-alias:compute"
	alias.RedactedProviderPayload = nil
	for _, operation := range []FabricOperation{exact, alias} {
		if err := store.Append(ctx, operation); err != nil {
			t.Fatal(err)
		}
	}

	got, found, err := store.OperationByActionIdempotency(ctx, exact.Action, exact.IdempotencyKey)
	if err != nil || !found || got.ID != exact.ID {
		t.Fatalf("exact=%#v found=%v err=%v", got, found, err)
	}
	if missing, found, err := store.OperationByActionIdempotency(ctx, exact.Action, "launch-absent:compute"); err != nil || found || missing.ID != "" {
		t.Fatalf("missing=%#v found=%v err=%v", missing, found, err)
	}
	terminal, found, err := store.ComputeClaimTerminalOperation(ctx, "approval-exact-30970000001", "approval-exact-30970000001")
	if err != nil || !found || terminal.ID != exact.ID {
		t.Fatalf("terminal=%#v found=%v err=%v", terminal, found, err)
	}
	if missing, found, err := store.ComputeClaimTerminalOperation(ctx, "approval-absent-30970000001", "approval-absent-30970000001"); err != nil || found || missing.ID != "" {
		t.Fatalf("terminal missing=%#v found=%v err=%v", missing, found, err)
	}

	duplicate := exact
	duplicate.ID = "fop-duplicate"
	if err := store.Append(ctx, duplicate); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.OperationByActionIdempotency(ctx, exact.Action, exact.IdempotencyKey); !errors.Is(err, ErrOperationIdentityConflict) {
		t.Fatalf("exact duplicate error=%v", err)
	}
	if _, _, err := store.ComputeClaimTerminalOperation(ctx, "approval-exact-30970000001", "approval-exact-30970000001"); !errors.Is(err, ErrOperationIdentityConflict) {
		t.Fatalf("terminal duplicate error=%v", err)
	}
}

func TestMemoryOperationStoreBoundsResourceQueriesAndOperationPages(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryOperationStore()
	createdAt := time.Date(2026, 8, 14, 1, 0, 0, 0, time.UTC)
	for index := 0; index < 8; index++ {
		operation := newOperation("unrelated", "storage_volume", fmt.Sprintf("storage-%d", index), "acct-other", "workspace-other", fmt.Sprintf("other-%d", index), "other-hash", createdAt.Add(time.Duration(index)*time.Second))
		operation.ID = fmt.Sprintf("fop-unrelated-%02d", index)
		operation.Status = "succeeded"
		operation.CreatedAt = createdAt.Add(time.Duration(index) * time.Second)
		if err := store.Append(ctx, operation); err != nil {
			t.Fatal(err)
		}
	}
	job := Job{JobID: "job-alpha", WorkspaceID: "workspace-alpha", Status: "queued", Attempt: 1, CreatedAt: createdAt, UpdatedAt: createdAt}
	jobOperation := newOperation("create_job", "job", job.JobID, "", job.WorkspaceID, "job-create", "job-hash", createdAt.Add(20*time.Second))
	jobOperation.ID = "fop-job-create"
	jobOperation.Status = job.Status
	jobOperation.CreatedAt = createdAt.Add(20 * time.Second)
	fillOperationResource(&jobOperation, job)
	if err := store.Append(ctx, jobOperation); err != nil {
		t.Fatal(err)
	}
	claim := jobOperation
	claim.ID = "fop-job-claim"
	claim.Action = "claim_job"
	claim.IdempotencyKey = "claim-once"
	claim.RequestHash = "claim-hash"
	claim.CreatedAt = createdAt.Add(21 * time.Second)
	claim.Status = "running"
	if err := store.Append(ctx, claim); err != nil {
		t.Fatal(err)
	}

	latest, found, err := store.LatestResourceOperation(ctx, "job", job.JobID)
	if err != nil || !found || latest.ID != claim.ID {
		t.Fatalf("latest=%#v found=%v err=%v", latest, found, err)
	}
	replayed, found, err := store.OperationByResourceActionIdempotency(ctx, "job", job.JobID, "claim_job", "claim-once")
	if err != nil || !found || replayed.ID != claim.ID {
		t.Fatalf("replayed=%#v found=%v err=%v", replayed, found, err)
	}

	runtime := newOperation("create_workspace_runtime", "workspace_runtime", "workspace-alpha", "acct-alpha", "workspace-alpha", "runtime-once", "runtime-hash", createdAt.Add(30*time.Second))
	runtime.ID = "fop-runtime-alpha"
	runtime.Status = "succeeded"
	runtime.CreatedAt = createdAt.Add(30 * time.Second)
	if err := store.Append(ctx, runtime); err != nil {
		t.Fatal(err)
	}
	candidates, err := store.WorkspaceRuntimeIdentityCandidates(ctx, "workspace-alpha")
	if err != nil || len(candidates) != 1 || candidates[0].ID != runtime.ID {
		t.Fatalf("runtime candidates=%#v err=%v", candidates, err)
	}

	page, err := store.ListPage(ctx, "", 3)
	if err != nil || len(page.Operations) != 3 || page.NextCursor == "" {
		t.Fatalf("first page=%#v err=%v", page, err)
	}
	next, err := store.ListPage(ctx, page.NextCursor, 3)
	if err != nil || len(next.Operations) != 3 || next.Operations[0].ID == page.Operations[0].ID {
		t.Fatalf("second page=%#v err=%v", next, err)
	}
	if _, err := store.ListPage(ctx, "not-a-cursor", 3); !errors.Is(err, ErrInvalidOperationPage) {
		t.Fatalf("invalid cursor error=%v", err)
	}
	if _, err := store.ListPage(ctx, strings.Repeat("a", maxFabricOperationCursorSize+1), 3); !errors.Is(err, ErrInvalidOperationPage) {
		t.Fatalf("oversized cursor error=%v", err)
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

func TestMemoryOperationStoreComputePoolHeadReadIsFIFOAndDoesNotClaimLease(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryOperationStore()
	createdAt := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	first := FabricOperation{
		ID: "fop-head-first", Action: "create_compute_allocation", ResourceID: "ca-first",
		OperationID: "op-first", IdempotencyKey: "launch-first:compute", RequestHash: "hash-first", Status: "claim_pending",
		ComputePoolKey: "np-basic", ComputePoolLeaseOwner: "existing-owner", CreatedAt: createdAt,
	}
	expiresAt := createdAt.Add(time.Hour)
	first.ComputePoolLeaseExpires = &expiresAt
	second := FabricOperation{
		ID: "fop-head-second", Action: "create_compute_allocation", ResourceID: "ca-second",
		OperationID: "op-second", IdempotencyKey: "launch-second:compute", RequestHash: "hash-second", Status: "started",
		ComputePoolKey: "np-basic", CreatedAt: createdAt.Add(time.Second),
	}
	for _, operation := range []FabricOperation{first, second} {
		if _, claimed, err := store.ClaimComputePoolRuntime(ctx, operation); err != nil || !claimed {
			t.Fatalf("seed operation %s: claimed=%v err=%v", operation.ID, claimed, err)
		}
	}

	head, found, err := store.ComputePoolHead(ctx, "np-basic")
	if err != nil || !found || head.ID != first.ID || head.Status != "claim_pending" || head.ComputePoolLeaseOwner != "existing-owner" || head.ComputePoolLeaseExpires == nil || !head.ComputePoolLeaseExpires.Equal(expiresAt) {
		t.Fatalf("head=%#v found=%v err=%v", head, found, err)
	}
	missing, found, err := store.ComputePoolHead(ctx, "np-pro")
	if err != nil || found || missing.ID != "" {
		t.Fatalf("missing=%#v found=%v err=%v", missing, found, err)
	}
	stored, err := store.List(ctx)
	if err != nil || len(stored) != 2 || stored[0].ComputePoolLeaseOwner != "existing-owner" || stored[1].ComputePoolLeaseOwner != "" {
		t.Fatalf("read-only head query changed operations: %#v err=%v", stored, err)
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

func TestHistoricalEntHardCutMigrationRetainsContentTransferTables(t *testing.T) {
	migration, err := os.ReadFile("ent_migrations/202607090001_ent_hard_cut.sql")
	if err != nil {
		t.Fatalf("read historical embedded migration: %v", err)
	}
	for _, table := range []string{"fabric_content_transfers", "fabric_content_transfer_chunks"} {
		if !strings.Contains(string(migration), "CREATE TABLE IF NOT EXISTS "+table) {
			t.Fatalf("historical embedded migration missing content transfer table %q", table)
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
