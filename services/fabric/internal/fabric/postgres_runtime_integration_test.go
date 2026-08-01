package fabric

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/lib/pq"

	fabricent "opl-cloud/services/fabric/ent"
)

type runtimeMutationBarrier struct {
	mutation   string
	mutated    chan struct{}
	release    chan struct{}
	notifyOnce sync.Once
	releaseOne sync.Once
	armed      atomic.Bool
}

func newRuntimeMutationBarrier(mutation string) *runtimeMutationBarrier {
	return &runtimeMutationBarrier{mutation: mutation, mutated: make(chan struct{}), release: make(chan struct{})}
}

func (b *runtimeMutationBarrier) matchesMutation(query string) bool {
	query = strings.ToUpper(strings.TrimSpace(query))
	return strings.HasPrefix(query, b.mutation) && strings.Contains(query, "FABRIC_OPERATIONS")
}

func (b *runtimeMutationBarrier) notifyMutation() {
	b.armed.Store(true)
	b.notifyOnce.Do(func() { close(b.mutated) })
}

func (b *runtimeMutationBarrier) waitBeforeRead(ctx context.Context, query string) error {
	if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(query)), "SELECT") || !strings.Contains(strings.ToUpper(query), "FABRIC_OPERATIONS") || !b.armed.CompareAndSwap(true, false) {
		return nil
	}
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *runtimeMutationBarrier) releaseRead() {
	b.releaseOne.Do(func() { close(b.release) })
}

type runtimeMutationBarrierConnector struct {
	driver.Connector
	barrier *runtimeMutationBarrier
}

func (c *runtimeMutationBarrierConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &runtimeMutationBarrierConn{Conn: conn, barrier: c.barrier}, nil
}

type runtimeMutationBarrierConn struct {
	driver.Conn
	barrier *runtimeMutationBarrier
}

func (c *runtimeMutationBarrierConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := c.barrier.waitBeforeRead(ctx, query); err != nil {
		return nil, err
	}
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err != nil || !c.barrier.matchesMutation(query) {
		return rows, err
	}
	return &runtimeMutationBarrierRows{Rows: rows, notify: c.barrier.notifyMutation}, nil
}

func (c *runtimeMutationBarrierConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	result, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
	if err == nil && c.barrier.matchesMutation(query) {
		c.barrier.notifyMutation()
	}
	return result, err
}

type runtimeMutationBarrierRows struct {
	driver.Rows
	notify func()
	once   sync.Once
}

func (r *runtimeMutationBarrierRows) Close() error {
	err := r.Rows.Close()
	r.once.Do(r.notify)
	return err
}

func newBarrierPostgresOperationStore(t *testing.T, databaseURL, mutation string) (*PostgresOperationStore, *runtimeMutationBarrier) {
	t.Helper()
	connector, err := pq.NewConnector(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	barrier := newRuntimeMutationBarrier(mutation)
	db := sql.OpenDB(&runtimeMutationBarrierConnector{Connector: connector, barrier: barrier})
	store := &PostgresOperationStore{db: db, client: fabricent.NewClient(fabricent.Driver(entsql.OpenDB(dialect.Postgres, db)))}
	t.Cleanup(func() {
		barrier.releaseRead()
		if err := store.client.Close(); err != nil {
			t.Errorf("close barrier operation store: %v", err)
		}
	})
	return store, barrier
}

func TestPostgresRuntimeMutationReturnsOwnFenceAtomically(t *testing.T) {
	for _, mutation := range []string{"INSERT", "UPDATE"} {
		t.Run(strings.ToLower(mutation), func(t *testing.T) {
			databaseURL := fabricTestDatabaseURL(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			currentStore, err := newTestPostgresOperationStore(databaseURL)
			if err != nil {
				t.Fatal(err)
			}
			defer currentStore.client.Close()
			barrierStore, barrier := newBarrierPostgresOperationStore(t, databaseURL, mutation)

			startedAt := time.Date(2026, 7, 17, 0, 0, 0, 123456789, time.UTC)
			operation := newOperation("create_workspace_runtime", "workspace_runtime", "workspace-atomic-fence", "acct-alpha", "workspace-atomic-fence", "runtime-atomic-fence", "request-hash", startedAt)
			operation.ID = "fop-runtime-atomic-fence"
			operation.Status = "started"
			operation.CreatedAt = startedAt
			priorStartedAt := time.Time{}
			if mutation == "UPDATE" {
				seeded, claimed, err := currentStore.ClaimRuntime(ctx, operation)
				if err != nil || !claimed {
					t.Fatalf("seed runtime claim=%#v claimed=%v err=%v", seeded, claimed, err)
				}
				priorStartedAt = seeded.StartedAt
			}

			type claimResult struct {
				operation FabricOperation
				won       bool
				err       error
			}
			result := make(chan claimResult, 1)
			requestedStartedAt := startedAt
			go func() {
				if mutation == "INSERT" {
					stored, claimed, err := barrierStore.ClaimRuntime(ctx, operation)
					result <- claimResult{operation: stored, won: claimed, err: err}
					return
				}
				requestedStartedAt = priorStartedAt.Add(3*time.Minute + 789*time.Nanosecond)
				stored, won, err := barrierStore.ReclaimRuntime(ctx, operation.ID, priorStartedAt, requestedStartedAt)
				result <- claimResult{operation: stored, won: won, err: err}
			}()

			select {
			case <-barrier.mutated:
			case <-ctx.Done():
				t.Fatal("runtime mutation did not reach the readback boundary")
			}
			operations, err := currentStore.List(ctx)
			if err != nil || len(operations) != 1 {
				t.Fatalf("read mutation fence operations=%#v err=%v", operations, err)
			}
			canonicalStartedAt := operations[0].StartedAt
			if canonicalStartedAt.Equal(requestedStartedAt) {
				t.Fatal("test input must exercise PostgreSQL timestamp canonicalization")
			}
			successorStartedAt := canonicalStartedAt.Add(3*time.Minute + 987*time.Nanosecond)
			successor, won, err := currentStore.ReclaimRuntime(ctx, operation.ID, canonicalStartedAt, successorStartedAt)
			if err != nil || !won {
				t.Fatalf("successor reclaim=%#v won=%v err=%v", successor, won, err)
			}
			barrier.releaseRead()
			owner := <-result
			if owner.err != nil || !owner.won {
				t.Fatalf("mutation owner=%#v won=%v err=%v", owner.operation, owner.won, owner.err)
			}
			if !owner.operation.StartedAt.Equal(canonicalStartedAt) {
				t.Fatalf("mutation owner received successor fence: got=%s own=%s successor=%s", owner.operation.StartedAt, canonicalStartedAt, successor.StartedAt)
			}
			owner.operation.Status = "succeeded"
			owner.operation.FinishedAt = successor.StartedAt.Add(time.Second)
			if err := barrierStore.SaveRuntime(ctx, owner.operation); !errors.Is(err, ErrRuntimeOperationNotCurrent) {
				t.Fatalf("superseded owner save error=%v, want ErrRuntimeOperationNotCurrent", err)
			}
			current, won, err := currentStore.ReclaimRuntime(ctx, operation.ID, canonicalStartedAt, successor.StartedAt.Add(time.Minute))
			if err != nil || won || !current.StartedAt.Equal(successor.StartedAt) {
				t.Fatalf("losing reclaim current=%#v won=%v err=%v", current, won, err)
			}
		})
	}
}

type stalePostgresRuntimeProvider struct {
	testProvider
	calls         atomic.Int32
	readbackCalls atomic.Int32
	readback      WorkspaceRuntime
}

type workspaceLaunchReadbackPostgresProvider struct {
	testProvider
	attachmentWrites atomic.Int32
	attachmentReads  atomic.Int32
	secretWrites     atomic.Int32
	secretReads      atomic.Int32
	runtimeWrites    atomic.Int32
	runtimeReads     atomic.Int32
	attachment       StorageAttachment
	secret           GatewaySecret
	runtime          WorkspaceRuntime
}

type workspaceLaunchReadbackBoundaryProvider struct {
	*workspaceLaunchReadbackPostgresProvider
	readErrorStage string
}

func (p *workspaceLaunchReadbackBoundaryProvider) ReadStorageAttachment(ctx context.Context, attachment StorageAttachment, compute ComputeAllocation, volume StorageVolume) (StorageAttachment, error) {
	if p.readErrorStage == "attachment" {
		p.attachmentReads.Add(1)
		return StorageAttachment{}, errors.New("injected attachment read error")
	}
	return p.workspaceLaunchReadbackPostgresProvider.ReadStorageAttachment(ctx, attachment, compute, volume)
}

func (p *workspaceLaunchReadbackBoundaryProvider) ReadGatewaySecretByDigest(ctx context.Context, input GatewaySecretReadbackInput) (GatewaySecret, error) {
	if p.readErrorStage == "secret" {
		p.secretReads.Add(1)
		return GatewaySecret{}, errors.New("injected secret read error")
	}
	return p.workspaceLaunchReadbackPostgresProvider.ReadGatewaySecretByDigest(ctx, input)
}

func (p *workspaceLaunchReadbackBoundaryProvider) WorkspaceRuntimeStatus(ctx context.Context, workspaceID string) (WorkspaceRuntime, error) {
	if p.readErrorStage == "runtime" {
		p.runtimeReads.Add(1)
		return WorkspaceRuntime{}, errors.New("injected runtime read error")
	}
	return p.workspaceLaunchReadbackPostgresProvider.WorkspaceRuntimeStatus(ctx, workspaceID)
}

type workspaceLaunchReadbackBoundaryStore struct {
	OperationStore
	listOverride  func([]FabricOperation) ([]FabricOperation, error)
	convergeCalls atomic.Int32
}

func (s *workspaceLaunchReadbackBoundaryStore) List(ctx context.Context) ([]FabricOperation, error) {
	operations, err := s.OperationStore.List(ctx)
	if err != nil || s.listOverride == nil {
		return operations, err
	}
	return s.listOverride(operations)
}

func (s *workspaceLaunchReadbackBoundaryStore) ConvergeRuntimeReadback(ctx context.Context, expected, next FabricOperation) error {
	s.convergeCalls.Add(1)
	converger, ok := s.OperationStore.(runtimeReadbackConverger)
	if !ok {
		return ErrRuntimeOperationNotCurrent
	}
	return converger.ConvergeRuntimeReadback(ctx, expected, next)
}

func (p *workspaceLaunchReadbackPostgresProvider) CreateStorageAttachment(_ context.Context, input StorageAttachmentInput, _ ComputeAllocation, _ StorageVolume) (StorageAttachment, error) {
	p.attachmentWrites.Add(1)
	p.attachment = StorageAttachment{
		ID: "att_" + stableSuffix(input.IdempotencyKey)[:18], OperationID: input.IdempotencyKey,
		WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID,
		Status: "attached", Provider: "tencent-tke", ProviderAttachmentID: "pv/storage-alpha-pv:pvc/storage-alpha-data",
		ProviderRequestID: providerRequestID("storage-attach", input.IdempotencyKey),
		CostTags:          oplCostTags("acct-alpha", input.WorkspaceID, "att_"+stableSuffix(input.IdempotencyKey)[:18], input.IdempotencyKey),
	}
	return p.attachment, nil
}

func (p *workspaceLaunchReadbackPostgresProvider) ReadStorageAttachment(_ context.Context, _ StorageAttachment, _ ComputeAllocation, _ StorageVolume) (StorageAttachment, error) {
	p.attachmentReads.Add(1)
	return p.attachment, nil
}

func (p *workspaceLaunchReadbackPostgresProvider) UpsertGatewaySecret(_ context.Context, input GatewaySecretInput) (GatewaySecret, error) {
	p.secretWrites.Add(1)
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(input.GatewayAPIKey)))
	p.secret = GatewaySecret{SecretRef: gatewaySecretName(input.WorkspaceID), Version: digest[:16], Fingerprint: "sha256:" + digest}
	return p.secret, nil
}

func (p *workspaceLaunchReadbackPostgresProvider) ReadGatewaySecretByDigest(_ context.Context, input GatewaySecretReadbackInput) (GatewaySecret, error) {
	p.secretReads.Add(1)
	if input.SecretRef != p.secret.SecretRef || input.Fingerprint != p.secret.Fingerprint || input.KeyDigest != strings.TrimPrefix(p.secret.Fingerprint, "sha256:") {
		return GatewaySecret{}, errors.New("gateway_secret_readback_mismatch")
	}
	return p.secret, nil
}

func (p *workspaceLaunchReadbackPostgresProvider) CreateWorkspaceRuntime(_ context.Context, input WorkspaceRuntimeInput, _ ComputeAllocation, _ StorageVolume) (WorkspaceRuntime, error) {
	p.runtimeWrites.Add(1)
	runtimeID := "rt_" + stableSuffix(input.WorkspaceID, input.RuntimeOperationID)[:18]
	p.runtime = WorkspaceRuntime{
		ID: runtimeID, OperationID: input.RuntimeOperationID, WorkspaceID: input.WorkspaceID,
		URL: "https://workspace.medopl.cn/w/" + input.WorkspaceID + "/", Status: "running", ServiceName: "opl-compute-alpha", ImageID: input.ImageID, Ready: true,
		ProviderRequestID: providerRequestID("runtime", input.IdempotencyKey),
		Access:            RuntimeAccess{Username: "admin", CredentialStatus: "configured", CredentialVersion: "version-alpha", SecretRef: "opl-compute-alpha-env"},
		CostTags:          oplCostTags("acct-alpha", input.WorkspaceID, runtimeID, input.RuntimeOperationID),
	}
	return p.runtime, nil
}

func (p *workspaceLaunchReadbackPostgresProvider) WorkspaceRuntimeStatus(_ context.Context, _ string) (WorkspaceRuntime, error) {
	p.runtimeReads.Add(1)
	return p.runtime, nil
}

type failWorkspaceLaunchReadbackSaveStore struct {
	OperationStore
	persistFailed bool
	failed        atomic.Bool
}

func (s *failWorkspaceLaunchReadbackSaveStore) SaveRuntime(ctx context.Context, operation FabricOperation) error {
	if !s.failed.CompareAndSwap(false, true) {
		return s.OperationStore.SaveRuntime(ctx, operation)
	}
	if s.persistFailed {
		failed := operation
		failed.Status = "failed"
		failed.ErrorCode = "injected_runtime_save_failure"
		if err := s.OperationStore.SaveRuntime(ctx, failed); err != nil {
			return err
		}
	}
	return errors.New("injected runtime save failure")
}

func (s *failWorkspaceLaunchReadbackSaveStore) ConvergeRuntimeReadback(ctx context.Context, expected, next FabricOperation) error {
	converger, ok := s.OperationStore.(runtimeReadbackConverger)
	if !ok {
		return ErrRuntimeOperationNotCurrent
	}
	return converger.ConvergeRuntimeReadback(ctx, expected, next)
}

func TestPostgresWorkspaceLaunchStageReadbackProofAndCASAcrossRestart(t *testing.T) {
	for _, stage := range []string{"attachment", "secret", "runtime"} {
		for _, priorStatus := range []string{"started", "failed"} {
			t.Run(stage+"/"+priorStatus, func(t *testing.T) {
				databaseURL := fabricTestDatabaseURL(t)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				firstStore, err := newTestPostgresOperationStore(databaseURL)
				if err != nil {
					t.Fatal(err)
				}
				provider := &workspaceLaunchReadbackPostgresProvider{}
				startedAt := time.Date(2026, 7, 31, 2, 0, 0, 123456000, time.UTC)
				failingStore := &failWorkspaceLaunchReadbackSaveStore{OperationStore: firstStore, persistFailed: priorStatus == "failed"}
				var proofInput WorkspaceLaunchStageReadbackInput
				switch stage {
				case "attachment":
					service := attachmentTestService(provider, failingStore)
					service.now = func() time.Time { return startedAt }
					input := attachmentTestInput("workspace-launch-readback:attachment")
					result, writeErr := service.CreateStorageAttachment(ctx, input)
					if writeErr == nil || result.ID == "" {
						t.Fatalf("attachment write=%#v err=%v", result, writeErr)
					}
					proofInput = WorkspaceLaunchStageReadbackInput{Stage: stage, AccountID: "acct-alpha", WorkspaceID: input.WorkspaceID, IdempotencyKey: input.IdempotencyKey, ComputeID: input.ComputeID, StorageID: input.VolumeID, AttachmentID: result.ID, AttachmentOperationID: input.IdempotencyKey}
				case "secret":
					service := NewServiceWithOperationStore(provider, failingStore)
					service.now = func() time.Time { return startedAt }
					key := "gateway-key-alpha"
					digest := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
					input := GatewaySecretInput{AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", WorkspaceAPIKeyID: 41, Fingerprint: "sha256:" + digest, GatewayAPIKey: key, IdempotencyKey: "workspace-launch-readback:secret"}
					result, writeErr := service.UpsertGatewaySecret(ctx, input)
					if writeErr == nil {
						t.Fatalf("secret write=%#v err=%v", result, writeErr)
					}
					proofInput = WorkspaceLaunchStageReadbackInput{Stage: stage, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, IdempotencyKey: input.IdempotencyKey, GatewaySecretRef: gatewaySecretName(input.WorkspaceID), GatewaySecretFingerprint: input.Fingerprint, WorkspaceAPIKeyID: input.WorkspaceAPIKeyID}
				case "runtime":
					service := runtimeTestService(provider, failingStore)
					service.now = func() time.Time { return startedAt }
					input := runtimeTestInput("workspace-launch-readback:runtime-stage")
					result, writeErr := service.CreateWorkspaceRuntime(ctx, input)
					if writeErr == nil || result.ID == "" {
						t.Fatalf("runtime write=%#v err=%v", result, writeErr)
					}
					proofInput = WorkspaceLaunchStageReadbackInput{Stage: stage, AccountID: "acct-alpha", WorkspaceID: input.WorkspaceID, IdempotencyKey: input.IdempotencyKey, ComputeID: input.ComputeID, StorageID: input.VolumeID, AttachmentID: input.AttachmentID, AttachmentOperationID: input.AttachmentOperationID, RuntimeID: result.ID, RuntimeOperationID: input.RuntimeOperationID, ImageID: input.ImageID, GatewaySecretRef: input.GatewaySecretRef}
				}
				beforeRestart, err := firstStore.List(ctx)
				if err != nil || len(beforeRestart) != 1 || beforeRestart[0].Status != priorStatus {
					t.Fatalf("persisted %s operation=%#v err=%v", priorStatus, beforeRestart, err)
				}
				proofInput.FabricRecordID, proofInput.FabricOperationID = beforeRestart[0].ID, beforeRestart[0].OperationID
				proofInput.RequestHash = beforeRestart[0].RequestHash
				if err := firstStore.client.Close(); err != nil {
					t.Fatal(err)
				}

				reopenedStore, err := newTestPostgresOperationStore(databaseURL)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = reopenedStore.client.Close() })
				var reopenedService *Service
				switch stage {
				case "attachment":
					reopenedService = attachmentTestService(provider, reopenedStore)
				case "runtime":
					reopenedService = runtimeTestService(provider, reopenedStore)
				default:
					reopenedService = NewServiceWithOperationStore(provider, reopenedStore)
				}
				reopenedService.now = func() time.Time { return startedAt.Add(3 * time.Minute) }
				proof, err := reopenedService.WorkspaceLaunchStageReadbackProof(ctx, proofInput)
				if err != nil || !proof.Eligible || proof.PriorStatus != priorStatus || proof.Operation.Status != "succeeded" || proof.BindingDigest == "" {
					t.Fatalf("%s proof=%#v err=%v", stage, proof, err)
				}
				if encoded, err := json.Marshal(proof); err != nil || bytes.Contains(encoded, []byte("keyDigest")) || bytes.Contains(encoded, []byte("gateway-key-alpha")) {
					t.Fatalf("unsafe proof payload=%s err=%v", encoded, err)
				}
				afterProof, err := reopenedStore.List(ctx)
				if err != nil || !reflect.DeepEqual(afterProof, beforeRestart) {
					t.Fatalf("proof mutated PostgreSQL: before=%#v after=%#v err=%v", beforeRestart, afterProof, err)
				}
				proofInput.ExpectedBindingDigest = proof.BindingDigest
				converged, err := reopenedService.ConvergeWorkspaceLaunchStageReadback(ctx, proofInput)
				if err != nil || converged.Operation.Status != "succeeded" || converged.FabricOperationMutationCount != 1 {
					t.Fatalf("%s convergence=%#v err=%v", stage, converged, err)
				}
				if provider.attachmentWrites.Load()+provider.secretWrites.Load()+provider.runtimeWrites.Load() != 1 ||
					provider.attachmentReads.Load()+provider.secretReads.Load()+provider.runtimeReads.Load() != 2 {
					t.Fatalf("%s provider writes=%d/%d/%d reads=%d/%d/%d", stage, provider.attachmentWrites.Load(), provider.secretWrites.Load(), provider.runtimeWrites.Load(), provider.attachmentReads.Load(), provider.secretReads.Load(), provider.runtimeReads.Load())
				}
			})
		}
	}
}

func TestPostgresNormalWorkspaceComputeStagesConvergeAcrossProcessRestart(t *testing.T) {
	for _, packageID := range []string{"basic", "pro"} {
		for _, interruptedStage := range []string{"compute_create", "compute_claim_cvm", "compute_claim_node"} {
			t.Run(packageID+"/"+interruptedStage, func(t *testing.T) {
				databaseURL := fabricTestDatabaseURL(t)
				firstStore, err := newTestPostgresOperationStore(databaseURL)
				if err != nil {
					t.Fatal(err)
				}
				gate := newNormalLaunchProviderWriteGate()
				provider := &normalLaunchComputeProvider{createResultErr: ErrComputeAllocationPending}
				switch interruptedStage {
				case "compute_create":
					provider.createGate = gate
				case "compute_claim_cvm":
					provider.cvmClaimGate = gate
					provider.cvmClaimResponseLost = true
				case "compute_claim_node":
					provider.nodeClaimGate = gate
					provider.nodeClaimResponseLost = true
				}
				input := ComputeAllocationInput{
					AccountID:   "acct-" + packageID + "-" + interruptedStage,
					WorkspaceID: "workspace-" + packageID + "-" + interruptedStage,
					PackageID:   packageID, NodePoolID: "np-" + packageID,
					IdempotencyKey: "workspace-launch-" + packageID + "-" + interruptedStage + ":compute",
				}
				first := NewServiceWithOperationStore(provider, firstStore)
				configureFastComputeAllocationPolling(first, 250*time.Millisecond)
				first.computeAllocationAttemptTimeout = time.Second
				first.computeAllocationFinalizeTimeout = 2 * time.Second
				allocation, err := first.CreateComputeAllocation(context.Background(), input)
				if err != nil {
					t.Fatal(err)
				}
				select {
				case <-gate.entered:
				case <-time.After(5 * time.Second):
					t.Fatalf("%s provider write was not reached", interruptedStage)
				}
				assertNormalLaunchStageBudget(t, firstStore, "create_compute_allocation", interruptedStage, 1, 0, 1)
				if err := firstStore.client.Close(); err != nil {
					t.Fatal(err)
				}
				close(gate.release)
				waitForComputeReconcileIdle(t, first, allocation.ID)

				provider.mu.Lock()
				provider.createGate, provider.cvmClaimGate, provider.nodeClaimGate = nil, nil, nil
				provider.cvmClaimResponseLost, provider.nodeClaimResponseLost = false, false
				provider.mu.Unlock()
				reopenedStore, err := newTestPostgresOperationStore(databaseURL)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = reopenedStore.client.Close() })
				waitForPostgresComputeLeaseExpiry(t, reopenedStore, allocation.ID)
				restarted := NewServiceWithOperationStore(provider, reopenedStore)
				configureFastComputeAllocationPolling(restarted, 2*time.Second)
				restarted.computeAllocationAttemptTimeout = 2 * time.Second
				restarted.computeAllocationFinalizeTimeout = 2 * time.Second
				if replayed, err := restarted.CreateComputeAllocation(context.Background(), input); err != nil || replayed.ID != allocation.ID {
					t.Fatalf("replay=%#v err=%v", replayed, err)
				}
				waitForPostgresNormalComputeSucceeded(t, restarted, reopenedStore, provider, allocation.ID)

				createCalls, readbackCalls, discoveryCalls, cvmClaimCalls, nodeClaimCalls, legacyClaimCalls := provider.counts()
				if createCalls != 1 || readbackCalls != 0 || discoveryCalls == 0 || cvmClaimCalls != 1 || nodeClaimCalls != 1 || legacyClaimCalls != 0 {
					t.Fatalf("provider calls create=%d read=%d discover=%d cvm=%d node=%d legacy=%d", createCalls, readbackCalls, discoveryCalls, cvmClaimCalls, nodeClaimCalls, legacyClaimCalls)
				}
				for _, stage := range []string{"compute_create", "compute_claim_cvm", "compute_claim_node"} {
					assertNormalLaunchStageBudget(t, reopenedStore, "create_compute_allocation", stage, 1, 1, 0)
				}
			})
		}
	}
}

func waitForPostgresComputeLeaseExpiry(t *testing.T, store *PostgresOperationStore, resourceID string) {
	t.Helper()
	var remainingMilliseconds int64
	if err := store.db.QueryRowContext(context.Background(), `
		SELECT COALESCE(CEIL(EXTRACT(EPOCH FROM GREATEST(
			compute_pool_lease_expires_at - clock_timestamp(), interval '0 seconds'
		)) * 1000), 0)::bigint
		FROM fabric_operations
		WHERE action = 'create_compute_allocation' AND resource_id = $1`, resourceID).Scan(&remainingMilliseconds); err != nil {
		t.Fatal(err)
	}
	if remainingMilliseconds > 0 {
		timer := time.NewTimer(time.Duration(remainingMilliseconds+10) * time.Millisecond)
		defer timer.Stop()
		<-timer.C
	}
}

func waitForPostgresNormalComputeSucceeded(t *testing.T, service *Service, store *PostgresOperationStore, provider *normalLaunchComputeProvider, resourceID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		operations, err := store.List(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, operation := range operations {
			if operation.Action == "create_compute_allocation" && operation.ResourceID == resourceID && operation.Status == "succeeded" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	operations, operationsErr := store.List(context.Background())
	ownership, ownershipErr := store.MachineOwnership(context.Background(), resourceID)
	create, readback, discovery, cvm, node, legacy := provider.counts()
	service.mu.Lock()
	reconciling := service.reconciling[resourceID]
	service.mu.Unlock()
	t.Fatalf("compute did not converge: operations=%#v operationsErr=%v ownership=%#v ownershipErr=%v calls=create:%d read:%d discover:%d cvm:%d node:%d legacy:%d reconciling=%v", operations, operationsErr, ownership, ownershipErr, create, readback, discovery, cvm, node, legacy, reconciling)
}

func TestPostgresNormalWorkspaceCBSResponseLossConvergesAcrossProcessRestart(t *testing.T) {
	for _, test := range []struct {
		packageID string
		sizeGB    int
	}{
		{packageID: "basic", sizeGB: 10},
		{packageID: "pro", sizeGB: 100},
	} {
		t.Run(test.packageID, func(t *testing.T) {
			databaseURL := fabricTestDatabaseURL(t)
			firstStore, err := newTestPostgresOperationStore(databaseURL)
			if err != nil {
				t.Fatal(err)
			}
			gate := newNormalLaunchProviderWriteGate()
			provider := &normalLaunchStorageProvider{cbsCreateGate: gate}
			first := NewServiceWithOperationStore(provider, firstStore)
			computeInput := ComputeAllocationInput{
				AccountID: "acct-" + test.packageID, WorkspaceID: "workspace-" + test.packageID,
				PackageID: test.packageID, NodePoolID: "np-" + test.packageID,
				IdempotencyKey: "workspace-launch-" + test.packageID + ":compute",
			}
			compute, err := first.CreateComputeAllocation(context.Background(), computeInput)
			if err != nil {
				t.Fatal(err)
			}
			waitForOperation(t, first, "create_compute_allocation", "compute_allocation", compute.ID, "succeeded")
			input := StorageVolumeInput{
				ID: "storage-" + test.packageID, AccountID: computeInput.AccountID, WorkspaceID: computeInput.WorkspaceID,
				ComputeID: compute.ID, Zone: "ap-guangzhou-3", SizeGB: test.sizeGB,
				IdempotencyKey: "workspace-launch-" + test.packageID + ":storage",
			}
			type storageResult struct {
				volume StorageVolume
				err    error
			}
			result := make(chan storageResult, 1)
			go func() {
				volume, createErr := first.CreateStorageVolume(context.Background(), input)
				result <- storageResult{volume: volume, err: createErr}
			}()
			select {
			case <-gate.entered:
			case <-time.After(time.Second):
				t.Fatal("CreateDisks-equivalent provider write was not reached")
			}
			assertNormalLaunchStageBudget(t, firstStore, "cbs_create", "cbs_create", 1, 0, 1)
			operations, err := firstStore.List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			for _, operation := range operations {
				if operation.Action != "cbs_create" {
					continue
				}
				var persisted StorageVolume
				if !decodeOperationResource(operation, &persisted) || persisted.ID != input.ID || persisted.ProviderResourceID != "" {
					t.Fatalf("pre-restart CBS identity=%#v operation=%#v", persisted, operation)
				}
			}
			if err := firstStore.client.Close(); err != nil {
				t.Fatal(err)
			}
			close(gate.release)
			if firstResult := <-result; firstResult.err == nil {
				t.Fatalf("closed store unexpectedly persisted CBS result=%#v", firstResult.volume)
			}

			provider.mu.Lock()
			provider.cbsCreateGate = nil
			provider.mu.Unlock()
			reopenedStore, err := newTestPostgresOperationStore(databaseURL)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopenedStore.client.Close() })
			restarted := NewServiceWithOperationStore(provider, reopenedStore)
			volume, err := restarted.CreateStorageVolume(context.Background(), input)
			if err != nil || volume.Status != "ready" || !strings.HasPrefix(volume.ProviderResourceID, "disk-") {
				t.Fatalf("CBS replay=%#v err=%v", volume, err)
			}
			cbsCreate, cbsReadback, bindingApply, bindingReadback := provider.storageCounts()
			if cbsCreate != 1 || cbsReadback != 1 || bindingApply != 1 || bindingReadback != 0 {
				t.Fatalf("provider calls CreateCBS=%d ReadCBS=%d ApplyBinding=%d ReadBinding=%d", cbsCreate, cbsReadback, bindingApply, bindingReadback)
			}
			assertNormalLaunchStageOperation(t, reopenedStore, "cbs_create", input, volume, "succeeded")
			assertNormalLaunchStageOperation(t, reopenedStore, "static_binding_apply", input, volume, "succeeded")
		})
	}
}

func interruptedWorkspaceLaunchReadbackFixture(t *testing.T, stage string) (*workspaceLaunchReadbackBoundaryProvider, *MemoryOperationStore, WorkspaceLaunchStageReadbackInput) {
	t.Helper()
	ctx := context.Background()
	provider := &workspaceLaunchReadbackBoundaryProvider{workspaceLaunchReadbackPostgresProvider: &workspaceLaunchReadbackPostgresProvider{}}
	store := NewMemoryOperationStore()
	failingStore := &failWorkspaceLaunchReadbackSaveStore{OperationStore: store}
	var input WorkspaceLaunchStageReadbackInput
	switch stage {
	case "attachment":
		service := attachmentTestService(provider, failingStore)
		writeInput := attachmentTestInput("workspace-launch-boundary:attachment")
		result, err := service.CreateStorageAttachment(ctx, writeInput)
		if err == nil || result.ID == "" {
			t.Fatalf("attachment write=%#v err=%v", result, err)
		}
		input = WorkspaceLaunchStageReadbackInput{
			Stage: stage, AccountID: "acct-alpha", WorkspaceID: writeInput.WorkspaceID, IdempotencyKey: writeInput.IdempotencyKey,
			ComputeID: writeInput.ComputeID, StorageID: writeInput.VolumeID, AttachmentID: result.ID, AttachmentOperationID: writeInput.IdempotencyKey,
		}
	case "secret":
		service := NewServiceWithOperationStore(provider, failingStore)
		key := "gateway-key-boundary"
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
		writeInput := GatewaySecretInput{
			AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", WorkspaceAPIKeyID: 41,
			Fingerprint: "sha256:" + digest, GatewayAPIKey: key, IdempotencyKey: "workspace-launch-boundary:secret",
		}
		if result, err := service.UpsertGatewaySecret(ctx, writeInput); err == nil {
			t.Fatalf("secret write=%#v err=%v", result, err)
		}
		input = WorkspaceLaunchStageReadbackInput{
			Stage: stage, AccountID: writeInput.AccountID, WorkspaceID: writeInput.WorkspaceID, IdempotencyKey: writeInput.IdempotencyKey,
			GatewaySecretRef: gatewaySecretName(writeInput.WorkspaceID), GatewaySecretFingerprint: writeInput.Fingerprint, WorkspaceAPIKeyID: writeInput.WorkspaceAPIKeyID,
		}
	case "runtime":
		service := runtimeTestService(provider, failingStore)
		writeInput := runtimeTestInput("workspace-launch-boundary:runtime")
		result, err := service.CreateWorkspaceRuntime(ctx, writeInput)
		if err == nil || result.ID == "" {
			t.Fatalf("runtime write=%#v err=%v", result, err)
		}
		input = WorkspaceLaunchStageReadbackInput{
			Stage: stage, AccountID: "acct-alpha", WorkspaceID: writeInput.WorkspaceID, IdempotencyKey: writeInput.IdempotencyKey,
			ComputeID: writeInput.ComputeID, StorageID: writeInput.VolumeID, AttachmentID: writeInput.AttachmentID,
			AttachmentOperationID: writeInput.AttachmentOperationID, RuntimeID: result.ID, RuntimeOperationID: writeInput.RuntimeOperationID,
			ImageID: writeInput.ImageID, GatewaySecretRef: writeInput.GatewaySecretRef,
		}
	default:
		t.Fatalf("unsupported stage %q", stage)
	}
	operations, err := store.List(ctx)
	if err != nil || len(operations) != 1 || operations[0].Status != "started" {
		t.Fatalf("interrupted %s operation=%#v err=%v", stage, operations, err)
	}
	input.FabricRecordID = operations[0].ID
	input.FabricOperationID = operations[0].OperationID
	input.RequestHash = operations[0].RequestHash
	return provider, store, input
}

func workspaceLaunchReadbackProviderWrites(provider *workspaceLaunchReadbackBoundaryProvider) int32 {
	return provider.attachmentWrites.Load() + provider.secretWrites.Load() + provider.runtimeWrites.Load()
}

func TestWorkspaceLaunchStageReadbackBoundaryFailsClosedWithoutExactAuthority(t *testing.T) {
	tests := []struct {
		name           string
		stage          string
		mutateInput    func(*WorkspaceLaunchStageReadbackInput)
		mutateProvider func(*workspaceLaunchReadbackBoundaryProvider)
		listOverride   func([]FabricOperation) ([]FabricOperation, error)
	}{
		{name: "operation missing", stage: "attachment", listOverride: func([]FabricOperation) ([]FabricOperation, error) { return nil, nil }},
		{name: "operation multiple", stage: "attachment", listOverride: func(operations []FabricOperation) ([]FabricOperation, error) {
			return append(operations, operations[0]), nil
		}},
		{name: "operation store read error", stage: "attachment", listOverride: func([]FabricOperation) ([]FabricOperation, error) {
			return nil, errors.New("injected operation store read error")
		}},
		{name: "request hash drift", stage: "runtime", mutateInput: func(input *WorkspaceLaunchStageReadbackInput) { input.RequestHash = strings.Repeat("f", 64) }},
		{name: "idempotency key drift", stage: "secret", mutateInput: func(input *WorkspaceLaunchStageReadbackInput) { input.IdempotencyKey += "-other" }},
		{name: "attachment identity drift", stage: "attachment", mutateProvider: func(provider *workspaceLaunchReadbackBoundaryProvider) {
			provider.attachment.CostTags["opl_operation_id"] = "attachment-operation-other"
		}},
		{name: "secret identity drift", stage: "secret", mutateProvider: func(provider *workspaceLaunchReadbackBoundaryProvider) {
			provider.secret.Fingerprint = "sha256:" + strings.Repeat("f", 64)
		}},
		{name: "runtime identity drift", stage: "runtime", mutateProvider: func(provider *workspaceLaunchReadbackBoundaryProvider) {
			provider.runtime.CostTags["opl_resource_id"] = "rt_other"
		}},
		{name: "attachment read error", stage: "attachment", mutateProvider: func(provider *workspaceLaunchReadbackBoundaryProvider) { provider.readErrorStage = "attachment" }},
		{name: "secret read error", stage: "secret", mutateProvider: func(provider *workspaceLaunchReadbackBoundaryProvider) { provider.readErrorStage = "secret" }},
		{name: "runtime read error", stage: "runtime", mutateProvider: func(provider *workspaceLaunchReadbackBoundaryProvider) { provider.readErrorStage = "runtime" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider, operationStore, input := interruptedWorkspaceLaunchReadbackFixture(t, test.stage)
			before, err := operationStore.List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if test.mutateInput != nil {
				test.mutateInput(&input)
			}
			if test.mutateProvider != nil {
				test.mutateProvider(provider)
			}
			store := &workspaceLaunchReadbackBoundaryStore{OperationStore: operationStore, listOverride: test.listOverride}
			var service *Service
			switch test.stage {
			case "attachment":
				service = attachmentTestService(provider, store)
			case "runtime":
				service = runtimeTestService(provider, store)
			default:
				service = NewServiceWithOperationStore(provider, store)
			}
			if proof, proofErr := service.WorkspaceLaunchStageReadbackProof(context.Background(), input); proofErr == nil || proof.Eligible {
				t.Fatalf("proof=%#v err=%v, want fail closed", proof, proofErr)
			}
			input.ExpectedBindingDigest = strings.Repeat("a", 64)
			if proof, convergeErr := service.ConvergeWorkspaceLaunchStageReadback(context.Background(), input); convergeErr == nil || proof.Eligible {
				t.Fatalf("convergence=%#v err=%v, want fail closed", proof, convergeErr)
			}
			after, err := operationStore.List(context.Background())
			if err != nil || !reflect.DeepEqual(before, after) || store.convergeCalls.Load() != 0 || workspaceLaunchReadbackProviderWrites(provider) != 1 {
				t.Fatalf("crossed fail-closed boundary: before=%#v after=%#v CAS=%d writes=%d err=%v", before, after, store.convergeCalls.Load(), workspaceLaunchReadbackProviderWrites(provider), err)
			}
		})
	}
}

func (p *stalePostgresRuntimeProvider) CreateWorkspaceRuntime(_ context.Context, input WorkspaceRuntimeInput, _ ComputeAllocation, _ StorageVolume) (WorkspaceRuntime, error) {
	p.calls.Add(1)
	p.readback = WorkspaceRuntime{
		ID: "rt_postgres-alpha", OperationID: input.RuntimeOperationID, WorkspaceID: input.WorkspaceID,
		Status: "running", Ready: true, ServiceName: "opl-compute-alpha", ImageID: input.ImageID,
		ProviderRequestID: providerRequestID("runtime", input.IdempotencyKey),
		CostTags:          oplCostTags("acct-alpha", input.WorkspaceID, "rt_postgres-alpha", input.RuntimeOperationID),
	}
	return p.readback, nil
}

func (p *stalePostgresRuntimeProvider) WorkspaceRuntimeStatus(_ context.Context, _ string) (WorkspaceRuntime, error) {
	p.readbackCalls.Add(1)
	return p.readback, nil
}

func TestPostgresStaleRuntimeClaimConvergesAcrossServiceInstances(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	firstStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatalf("open first operation store: %v", err)
	}
	defer firstStore.client.Close()
	secondStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatalf("open second operation store: %v", err)
	}
	defer secondStore.client.Close()

	provider := &stalePostgresRuntimeProvider{}
	firstService := runtimeTestService(provider, &failFirstRuntimeSaveStore{OperationStore: firstStore})
	secondService := runtimeTestService(provider, secondStore)
	startedAt := time.Date(2026, 7, 17, 0, 0, 0, 123456000, time.UTC)
	firstService.now = func() time.Time { return startedAt }
	secondService.now = func() time.Time { return startedAt.Add(3 * time.Minute) }
	input := runtimeTestInput("postgres-runtime-stale")

	firstResult, firstErr := firstService.CreateWorkspaceRuntime(ctx, input)
	if firstErr == nil || firstErr.Error() != "injected runtime save failure" || firstResult.ID != provider.readback.ID || provider.calls.Load() != 1 {
		t.Fatalf("first runtime=%#v err=%v providerCalls=%d", firstResult, firstErr, provider.calls.Load())
	}
	operations, err := firstStore.List(ctx)
	if err != nil || len(operations) != 1 || operations[0].Status != "started" {
		t.Fatalf("persisted old claim=%#v err=%v", operations, err)
	}

	secondResult, secondErr := secondService.CreateWorkspaceRuntime(ctx, input)
	if secondErr != nil || secondResult.ID != provider.readback.ID || provider.calls.Load() != 1 || provider.readbackCalls.Load() != 1 {
		t.Fatalf("readback convergence runtime=%#v err=%v providerCalls=%d readbackCalls=%d", secondResult, secondErr, provider.calls.Load(), provider.readbackCalls.Load())
	}
	firstReplay, firstReplayErr := firstService.CreateWorkspaceRuntime(ctx, input)
	if firstReplayErr != nil || firstReplay.ID != secondResult.ID || provider.calls.Load() != 1 {
		t.Fatalf("final replay=%#v err=%v providerCalls=%d", firstReplay, firstReplayErr, provider.calls.Load())
	}
	operations, err = secondStore.List(ctx)
	if err != nil || len(operations) != 1 || operations[0].Status != "succeeded" {
		t.Fatalf("final operations=%#v err=%v", operations, err)
	}
}

func TestPostgresWorkspaceLaunchRuntimeReadbackProofAndCASAcrossRestart(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	firstStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatalf("open first operation store: %v", err)
	}

	provider := &stalePostgresRuntimeProvider{}
	startedAt := time.Date(2026, 7, 31, 1, 0, 0, 123456000, time.UTC)
	firstService := runtimeTestService(provider, &failFirstRuntimeSaveStore{OperationStore: firstStore})
	firstService.now = func() time.Time { return startedAt }
	input := runtimeTestInput("workspace-launch-readback:runtime")
	result, writeErr := firstService.CreateWorkspaceRuntime(ctx, input)
	if writeErr == nil || writeErr.Error() != "injected runtime save failure" || result.ID == "" || provider.calls.Load() != 1 {
		t.Fatalf("first runtime=%#v err=%v providerWrites=%d", result, writeErr, provider.calls.Load())
	}
	beforeRestart, err := firstStore.List(ctx)
	if err != nil || len(beforeRestart) != 1 || beforeRestart[0].Status != "started" {
		t.Fatalf("persisted interrupted operation=%#v err=%v", beforeRestart, err)
	}
	if err := firstStore.client.Close(); err != nil {
		t.Fatal(err)
	}

	reopenedStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatalf("reopen operation store: %v", err)
	}
	t.Cleanup(func() { _ = reopenedStore.client.Close() })
	reopenedService := runtimeTestService(provider, reopenedStore)
	reopenedService.now = func() time.Time { return startedAt.Add(3 * time.Minute) }
	interrupted := beforeRestart[0]
	proofInput := WorkspaceLaunchStageReadbackInput{
		Stage: "runtime", FabricRecordID: interrupted.ID, FabricOperationID: interrupted.OperationID,
		AccountID: interrupted.AccountID, WorkspaceID: input.WorkspaceID, IdempotencyKey: input.IdempotencyKey,
		RequestHash: interrupted.RequestHash, ComputeID: input.ComputeID, StorageID: input.VolumeID,
		AttachmentID: input.AttachmentID, AttachmentOperationID: input.AttachmentOperationID,
		RuntimeID: result.ID, RuntimeOperationID: input.RuntimeOperationID, ImageID: input.ImageID,
		GatewaySecretRef: input.GatewaySecretRef,
	}

	proof, err := reopenedService.WorkspaceLaunchStageReadbackProof(ctx, proofInput)
	if err != nil || !proof.Eligible || proof.Stage != "runtime" || proof.PriorStatus != "started" || proof.BindingDigest == "" ||
		proof.Operation.Status != "succeeded" || proof.Operation.ResourceID != input.WorkspaceID || provider.calls.Load() != 1 || provider.readbackCalls.Load() != 1 {
		t.Fatalf("runtime proof=%#v err=%v providerWrites=%d providerReads=%d", proof, err, provider.calls.Load(), provider.readbackCalls.Load())
	}
	afterProof, err := reopenedStore.List(ctx)
	if err != nil || !reflect.DeepEqual(afterProof, beforeRestart) {
		t.Fatalf("read-only proof mutated PostgreSQL: before=%#v after=%#v err=%v", beforeRestart, afterProof, err)
	}

	proofInput.ExpectedBindingDigest = proof.BindingDigest
	converged, err := reopenedService.ConvergeWorkspaceLaunchStageReadback(ctx, proofInput)
	if err != nil || !converged.Eligible || converged.Operation.Status != "succeeded" || provider.calls.Load() != 1 || provider.readbackCalls.Load() != 2 {
		t.Fatalf("runtime convergence=%#v err=%v providerWrites=%d providerReads=%d", converged, err, provider.calls.Load(), provider.readbackCalls.Load())
	}
	afterConvergence, err := reopenedStore.List(ctx)
	if err != nil || len(afterConvergence) != 1 || afterConvergence[0].Status != "succeeded" {
		t.Fatalf("converged operation=%#v err=%v", afterConvergence, err)
	}
}

func TestPostgresRuntimeClaimAcrossServiceInstances(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var firstStore, secondStore *PostgresOperationStore
	t.Cleanup(func() {
		if secondStore != nil {
			if err := secondStore.client.Close(); err != nil {
				t.Errorf("close second operation store: %v", err)
			}
		}
		if firstStore != nil {
			if err := firstStore.client.Close(); err != nil {
				t.Errorf("close first operation store: %v", err)
			}
		}
	})

	var err error
	firstStore, err = newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatalf("open first operation store: %v", err)
	}
	secondStore, err = newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatalf("open second operation store: %v", err)
	}

	provider := &blockingRuntimeProvider{entered: make(chan struct{}), release: make(chan struct{})}
	firstService := runtimeTestService(provider, firstStore)
	secondService := runtimeTestService(provider, secondStore)
	input := runtimeTestInput("postgres-runtime-shared")
	firstDone := make(chan error, 1)
	go func() {
		_, err := firstService.CreateWorkspaceRuntime(ctx, input)
		firstDone <- err
	}()
	select {
	case <-provider.entered:
	case <-ctx.Done():
		t.Fatal("first provider call did not start")
	}
	if _, err := secondService.CreateWorkspaceRuntime(ctx, input); err != ErrRuntimeOperationInProgress {
		t.Fatalf("concurrent replay error = %v, want %v", err, ErrRuntimeOperationInProgress)
	}
	if calls := provider.calls.Load(); calls != 1 {
		t.Fatalf("provider calls = %d, want 1", calls)
	}
	close(provider.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first runtime create: %v", err)
	}

	replayed, err := NewServiceWithOperationStore(provider, secondStore).CreateWorkspaceRuntime(ctx, input)
	if err != nil || replayed.ID != "runtime-alpha" || provider.calls.Load() != 1 {
		t.Fatalf("postgres restart replay = %#v err=%v providerCalls=%d", replayed, err, provider.calls.Load())
	}
}

func TestPostgresComputePoolHeadSerializesDifferentWorkspacesAcrossServiceInstances(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	firstStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.client.Close()
	secondStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.client.Close()

	provider := newSerializedPoolProvider("workspace-alpha")
	firstService := NewServiceWithOperationStore(provider, firstStore)
	secondService := NewServiceWithOperationStore(provider, secondStore)
	configureFastComputeAllocationPolling(firstService, 15*time.Millisecond)
	configureFastComputeAllocationPolling(secondService, 100*time.Millisecond)
	firstService.computeAllocationFinalizeTimeout = 2 * time.Second
	secondService.computeAllocationFinalizeTimeout = 2 * time.Second
	firstInput := ComputeAllocationInput{AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", PackageID: "basic", NodePoolID: "np-basic", IdempotencyKey: "postgres-compute-alpha"}
	secondInput := ComputeAllocationInput{AccountID: "acct-beta", WorkspaceID: "workspace-beta", PackageID: "basic", NodePoolID: "np-basic", IdempotencyKey: "postgres-compute-beta"}

	first, err := firstService.CreateComputeAllocation(ctx, firstInput)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.firstHeadCall:
	case <-ctx.Done():
		t.Fatal("PostgreSQL head did not reach provider")
	}
	waitForComputeReconcileIdle(t, firstService, first.ID)
	second, err := secondService.CreateComputeAllocation(ctx, secondInput)
	if err != nil {
		t.Fatal(err)
	}
	waitForComputeReconcileIdle(t, secondService, second.ID)
	if calls := provider.workspaceCalls("workspace-beta"); calls != 0 {
		t.Fatalf("second PostgreSQL service bypassed persisted head: calls=%d", calls)
	}
	if calls := provider.workspacePrepareCalls("workspace-beta"); calls != 0 {
		t.Fatalf("second PostgreSQL service prepared before persisted head: calls=%d", calls)
	}

	provider.allowHeadCompletion()
	if _, err := secondService.CreateComputeAllocation(ctx, firstInput); err != nil {
		t.Fatal(err)
	}
	waitForPostgresComputeOperationSucceeded(t, ctx, secondService, secondStore, provider, first.ID, firstInput.WorkspaceID)
	if _, err := firstService.CreateComputeAllocation(ctx, secondInput); err != nil {
		t.Fatal(err)
	}
	waitForPostgresComputeOperationSucceeded(t, ctx, firstService, firstStore, provider, second.ID, secondInput.WorkspaceID)
	if targets, ambiguous := provider.allocationEvidence("np-basic"); !reflect.DeepEqual(targets, []int64{1, 2}) || ambiguous != 0 {
		t.Fatalf("PostgreSQL scale targets=%v ambiguous=%d", targets, ambiguous)
	}
}

func waitForPostgresComputeOperationSucceeded(t *testing.T, ctx context.Context, service *Service, store *PostgresOperationStore, provider *serializedPoolProvider, resourceID, workspaceID string) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var latest FabricOperation
	for {
		operations, err := store.List(ctx)
		if err != nil {
			t.Fatalf("list PostgreSQL compute operations: %v", err)
		}
		for _, operation := range operations {
			if operation.Action != "create_compute_allocation" || operation.ResourceKind != "compute_allocation" || operation.ResourceID != resourceID {
				continue
			}
			latest = operation
			switch operation.Status {
			case "succeeded":
				if operation.OperationID == "" || operation.ProviderRequestID == "" || operation.RequestHash == "" || operation.StartedAt.IsZero() || operation.FinishedAt.IsZero() {
					t.Fatalf("PostgreSQL compute operation missing audit fields: %#v", operation)
				}
				return
			case "failed", "claim_pending":
				t.Fatalf("PostgreSQL compute operation reached %s: %#v", operation.Status, operation)
			}
		}
		select {
		case <-ctx.Done():
			service.mu.Lock()
			reconciling := service.reconciling[resourceID]
			service.mu.Unlock()
			targets, ambiguous := provider.allocationEvidence("np-basic")
			t.Fatalf("PostgreSQL compute operation did not succeed: resource=%s operation=%#v leaseOwner=%q leaseExpires=%v providerCalls=%d prepareCalls=%d targets=%v ambiguous=%d reconciling=%v context=%v",
				resourceID, latest, latest.ComputePoolLeaseOwner, latest.ComputePoolLeaseExpires,
				provider.workspaceCalls(workspaceID), provider.workspacePrepareCalls(workspaceID), targets, ambiguous, reconciling, ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestPostgresComputeClaimPendingKeepsFIFOHead(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.client.Close()
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

func TestPostgresComputePoolLeaseUsesDatabaseClockAcrossServiceInstances(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	firstStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.client.Close()
	secondStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.client.Close()

	operation := newOperation("create_compute_allocation", "compute_allocation", "compute-clock-skew", "acct-alpha", "workspace-alpha", "compute-clock-skew", "hash-clock-skew", time.Now().UTC())
	operation.ID = "fop_compute_claim_clock_skew"
	operation.Status = "started"
	operation.ComputePoolKey = "np-basic"
	stored, claimed, err := firstStore.ClaimComputePoolRuntime(ctx, operation)
	if err != nil || !claimed {
		t.Fatalf("seed compute operation: claimed=%v err=%v", claimed, err)
	}

	skewedNow := time.Now().UTC().Add(-time.Hour)
	if _, claimed, err := firstStore.TryClaimComputePoolHead(ctx, stored.ID, "np-basic", "lease-skewed", skewedNow, skewedNow.Add(time.Minute)); err != nil || !claimed {
		t.Fatalf("first lease: claimed=%v err=%v", claimed, err)
	}
	current, claimed, err := secondStore.TryClaimComputePoolHead(ctx, stored.ID, "np-basic", "lease-current", time.Now().UTC(), time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if claimed || current.ComputePoolLeaseOwner != "lease-skewed" {
		t.Fatalf("database lease was stolen because of process clock skew: claimed=%v current=%#v", claimed, current)
	}
}

func TestPostgresDestroyRuntimeFailedRetryBindsWorkspaceAcrossServiceInstances(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	firstStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatalf("open first operation store: %v", err)
	}
	defer firstStore.client.Close()
	secondStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatalf("open second operation store: %v", err)
	}
	defer secondStore.client.Close()

	originalProvider := &failOnceDestroyProvider{}
	originalService := NewServiceWithOperationStore(originalProvider, firstStore)
	if _, err := originalService.DestroyWorkspaceRuntime(ctx, "workspace-alpha", "postgres-runtime-destroy-once"); err == nil {
		t.Fatal("first destroy succeeded, want transient failure")
	}
	before, err := firstStore.List(ctx)
	if err != nil || len(before) != 1 || before[0].Status != "failed" {
		t.Fatalf("failed operation = %#v err=%v", before, err)
	}

	otherProvider := &countingRuntimeProvider{}
	services := []*Service{
		NewServiceWithOperationStore(otherProvider, firstStore),
		NewServiceWithOperationStore(otherProvider, secondStore),
	}
	start := make(chan struct{})
	results := make(chan error, len(services))
	for _, service := range services {
		service := service
		go func() {
			<-start
			_, err := service.DestroyWorkspaceRuntime(ctx, "workspace-beta", "postgres-runtime-destroy-once")
			results <- err
		}()
	}
	close(start)
	for range services {
		if err := <-results; !errors.Is(err, ErrRuntimeIdempotencyConflict) {
			t.Fatalf("cross-workspace retry error = %v, want ErrRuntimeIdempotencyConflict", err)
		}
	}
	after, err := firstStore.List(ctx)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("cross-workspace retry changed operation: before=%#v after=%#v err=%v", before, after, err)
	}
	if otherProvider.destroyCalls.Load() != 0 {
		t.Fatalf("cross-workspace provider calls = %d, want 0", otherProvider.destroyCalls.Load())
	}

	runtime, err := originalService.DestroyWorkspaceRuntime(ctx, "workspace-alpha", "postgres-runtime-destroy-once")
	if err != nil || runtime.Status != "destroyed" || originalProvider.destroyCalls.Load() != 2 {
		t.Fatalf("original retry = %#v err=%v providerCalls=%d", runtime, err, originalProvider.destroyCalls.Load())
	}
}

func TestPostgresComputeClaimRecoveryOperationCASRejectsSkippedTransition(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	store, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.client.Close()

	operation := postgresComputeClaimOperation("skipped", "failed")
	if err := store.Append(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	recovered := operation
	recovered.Status = "succeeded"
	recovered.FinishedAt = operation.CreatedAt.Add(time.Minute)
	if err := store.SaveComputeClaimRecovery(context.Background(), operation, recovered); !errors.Is(err, ErrRuntimeOperationNotCurrent) {
		t.Fatalf("failed -> succeeded error=%v, want ErrRuntimeOperationNotCurrent", err)
	}
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 1 || operations[0].Status != "failed" {
		t.Fatalf("skipped transition changed operation: operations=%#v err=%v", operations, err)
	}
}

func TestPostgresComputeClaimRecoveryOperationCASRejectsStaleAndRequestHashDrift(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	store, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.client.Close()

	operation := postgresComputeClaimOperation("identity", "failed")
	if err := store.Append(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	pending := operation
	pending.Status = "claim_pending"
	pending.FinishedAt = time.Time{}
	pending.RedactedProviderPayload = withComputeClaimRecoveryBinding(pending.RedactedProviderPayload, postgresComputeClaimRecoveryBinding("identity"))
	if err := store.SaveComputeClaimRecovery(context.Background(), operation, pending); err != nil {
		t.Fatalf("failed -> claim_pending: %v", err)
	}
	recovered := pending
	recovered.Status = "succeeded"
	recovered.FinishedAt = operation.CreatedAt.Add(time.Minute)
	if err := store.SaveComputeClaimRecovery(context.Background(), operation, recovered); !errors.Is(err, ErrRuntimeOperationNotCurrent) {
		t.Fatalf("stale failed owner error=%v, want ErrRuntimeOperationNotCurrent", err)
	}
	drifted := recovered
	drifted.RequestHash = "different-request-hash"
	if err := store.SaveComputeClaimRecovery(context.Background(), pending, drifted); !errors.Is(err, ErrRuntimeOperationNotCurrent) {
		t.Fatalf("request hash drift error=%v, want ErrRuntimeOperationNotCurrent", err)
	}
	if err := store.SaveComputeClaimRecovery(context.Background(), pending, recovered); err != nil {
		t.Fatalf("claim_pending -> succeeded: %v", err)
	}
	if err := store.SaveComputeClaimRecovery(context.Background(), pending, recovered); !errors.Is(err, ErrRuntimeOperationNotCurrent) {
		t.Fatalf("stale claim_pending owner error=%v, want ErrRuntimeOperationNotCurrent", err)
	}
}

func TestPostgresComputeClaimRecoveryOperationCASRejectsBindingDrift(t *testing.T) {
	drifts := map[string]func(*computeClaimRecoveryBinding){
		"launch": func(binding *computeClaimRecoveryBinding) { binding.LaunchOperationID = "launch-postgres-other" },
		"idempotency": func(binding *computeClaimRecoveryBinding) {
			binding.IdempotencyKey = "launch-postgres-binding:compute-other"
		},
		"target":  func(binding *computeClaimRecoveryBinding) { binding.TargetHash = "different-target-hash" },
		"request": func(binding *computeClaimRecoveryBinding) { binding.RequestHash = "different-request-hash" },
	}
	for name, drift := range drifts {
		t.Run(name, func(t *testing.T) {
			databaseURL := fabricTestDatabaseURL(t)
			store, err := newTestPostgresOperationStore(databaseURL)
			if err != nil {
				t.Fatal(err)
			}
			defer store.client.Close()

			operation := postgresComputeClaimOperation("binding", "failed")
			if err := store.Append(context.Background(), operation); err != nil {
				t.Fatal(err)
			}
			pending := operation
			pending.Status, pending.FinishedAt = "claim_pending", time.Time{}
			binding := postgresComputeClaimRecoveryBinding("binding")
			pending.RedactedProviderPayload = withComputeClaimRecoveryBinding(pending.RedactedProviderPayload, binding)
			if err := store.SaveComputeClaimRecovery(context.Background(), operation, pending); err != nil {
				t.Fatalf("failed -> claim_pending: %v", err)
			}
			drift(&binding)
			drifted := pending
			drifted.Status, drifted.FinishedAt = "succeeded", operation.CreatedAt.Add(time.Minute)
			drifted.RedactedProviderPayload = withComputeClaimRecoveryBinding(drifted.RedactedProviderPayload, binding)
			if err := store.SaveComputeClaimRecovery(context.Background(), pending, drifted); !errors.Is(err, ErrRuntimeOperationNotCurrent) {
				t.Fatalf("binding drift error=%v, want ErrRuntimeOperationNotCurrent", err)
			}
			operations, err := store.List(context.Background())
			if err != nil || len(operations) != 1 {
				t.Fatalf("binding drift changed operation: operations=%#v err=%v", operations, err)
			}
			expectedPayloadJSON, expectedPayloadErr := operationPayloadJSON(pending)
			var expectedPayload map[string]any
			if expectedPayloadErr != nil || json.Unmarshal([]byte(expectedPayloadJSON), &expectedPayload) != nil ||
				operations[0].Status != "claim_pending" || !reflect.DeepEqual(operations[0].RedactedProviderPayload, expectedPayload) {
				t.Fatalf("binding drift changed operation: operations=%#v expectedPayload=%#v", operations, expectedPayload)
			}
		})
	}
}

func TestPostgresComputeClaimRecoveryOperationCASHasSingleConcurrentWinner(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	firstStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.client.Close()
	secondStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.client.Close()

	operation := postgresComputeClaimOperation("concurrent", "claim_pending")
	if err := firstStore.Append(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	recovered := operation
	recovered.Status = "succeeded"
	recovered.FinishedAt = operation.CreatedAt.Add(time.Minute)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, store := range []*PostgresOperationStore{firstStore, secondStore} {
		store := store
		go func() {
			<-start
			results <- store.SaveComputeClaimRecovery(context.Background(), operation, recovered)
		}()
	}
	close(start)
	winners := 0
	for range 2 {
		err := <-results
		if err == nil {
			winners++
			continue
		}
		if !errors.Is(err, ErrRuntimeOperationNotCurrent) {
			t.Fatalf("concurrent CAS error=%v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent CAS winners=%d, want 1", winners)
	}
}

func TestPostgresComputeClaimRecoveryNodeReservationCASHasSingleWinnerAndKeepsOriginalBinding(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	firstStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.client.Close()
	secondStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.client.Close()

	operation := postgresComputeClaimOperation("node-reservation", "claim_pending")
	observed := observedComputeClaimRecoveryMutation(ComputeClaimRecoveryProof{
		Reason: "provider_describe", TencentMutationCount: 1, KubernetesMutationCount: 0,
		FailureStage: "cvm_tag_readback", ProviderErrorClass: "provider_error",
		Evidence: &ComputeClaimEvidence{CVM: ComputeClaimMutationEvidence{Attempted: 1, Missing: []string{"opl_account_id"}}},
	})
	operation.RedactedProviderPayload = withComputeClaimRecoveryMutation(operation.RedactedProviderPayload, observed)
	if err := firstStore.Append(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	nodeReserved := operation
	nodeReserved.RedactedProviderPayload = withComputeClaimRecoveryMutation(nodeReserved.RedactedProviderPayload, nodeReservedComputeClaimRecoveryMutation(observed))

	for name, drift := range map[string]func(*computeClaimRecoveryBinding){
		"launch":      func(binding *computeClaimRecoveryBinding) { binding.LaunchOperationID = "launch-postgres-other" },
		"idempotency": func(binding *computeClaimRecoveryBinding) { binding.IdempotencyKey += "-other" },
		"target":      func(binding *computeClaimRecoveryBinding) { binding.TargetHash = "different-target-hash" },
		"request":     func(binding *computeClaimRecoveryBinding) { binding.RequestHash = "different-request-hash" },
	} {
		t.Run(name, func(t *testing.T) {
			binding, present, valid := decodeComputeClaimRecoveryBinding(nodeReserved)
			if !present || !valid {
				t.Fatalf("node reservation binding=%#v present=%v valid=%v", binding, present, valid)
			}
			drift(&binding)
			drifted := nodeReserved
			drifted.RedactedProviderPayload = withComputeClaimRecoveryBinding(drifted.RedactedProviderPayload, binding)
			if err := firstStore.SaveComputeClaimRecovery(context.Background(), operation, drifted); !errors.Is(err, ErrRuntimeOperationNotCurrent) {
				t.Fatalf("node reservation binding drift error=%v, want ErrRuntimeOperationNotCurrent", err)
			}
		})
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	for _, store := range []*PostgresOperationStore{firstStore, secondStore} {
		store := store
		go func() {
			<-start
			results <- store.SaveComputeClaimRecovery(context.Background(), operation, nodeReserved)
		}()
	}
	close(start)
	winners := 0
	for range 2 {
		err := <-results
		if err == nil {
			winners++
			continue
		}
		if !errors.Is(err, ErrRuntimeOperationNotCurrent) {
			t.Fatalf("node reservation concurrent CAS error=%v", err)
		}
	}
	stored, err := firstStore.List(context.Background())
	binding, bindingPresent, bindingValid := decodeComputeClaimRecoveryBinding(stored[0])
	ledger, ledgerPresent, ledgerValid := decodeComputeClaimRecoveryMutation(stored[0])
	if winners != 1 || err != nil || len(stored) != 1 || !bindingPresent || !bindingValid ||
		binding != postgresComputeClaimRecoveryBinding("node-reservation") || !ledgerPresent || !ledgerValid || ledger.State != "node_reserved" ||
		ledger.TencentMutationCount != 1 || ledger.KubernetesMutationCount != 1 || ledger.Evidence.CVM.Attempted != 1 ||
		ledger.Evidence.CVM.Confirmed != 1 || ledger.Evidence.Node.Attempted != 1 || ledger.Evidence.Node.Unknown != 1 {
		t.Fatalf("winners=%d stored=%#v err=%v binding=%#v present=%v valid=%v ledger=%#v ledgerPresent=%v ledgerValid=%v", winners, stored, err, binding, bindingPresent, bindingValid, ledger, ledgerPresent, ledgerValid)
	}
}

func TestPostgresComputeClaimRecoveryOwnershipCASActivatesOnlySameTarget(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	store, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.client.Close()

	ownership := postgresComputeClaimOwnership("identity")
	stored, created, err := store.ClaimMachine(context.Background(), ownership)
	if err != nil || !created {
		t.Fatalf("seed ownership=%#v created=%v err=%v", stored, created, err)
	}
	target := stored
	target.Status = "active"
	target.ReleasedAt = nil
	if err := store.ActivateComputeClaimRecoveryOwnership(context.Background(), target); err != nil {
		t.Fatalf("quarantined -> active: %v", err)
	}
	active, err := store.MachineOwnership(context.Background(), target.ResourceID)
	if err != nil || active.Status != "active" || !sameComputeClaimRecoveryOwnership(active, target) {
		t.Fatalf("active ownership=%#v err=%v", active, err)
	}
	if err := store.ActivateComputeClaimRecoveryOwnership(context.Background(), target); err != nil {
		t.Fatalf("active replay: %v", err)
	}

	drifts := map[string]func(*MachineOwnership){
		"account":   func(value *MachineOwnership) { value.AccountID = "acct-other" },
		"workspace": func(value *MachineOwnership) { value.WorkspaceID = "workspace-other" },
		"package":   func(value *MachineOwnership) { value.PackageID = "pro" },
		"pool":      func(value *MachineOwnership) { value.NodePoolID = "np-other" },
		"machine":   func(value *MachineOwnership) { value.MachineID = "machine-other" },
		"node":      func(value *MachineOwnership) { value.NodeName = "node-other" },
		"cvm":       func(value *MachineOwnership) { value.InstanceID = "ins-other" },
	}
	for name, drift := range drifts {
		t.Run(name, func(t *testing.T) {
			conflict := target
			drift(&conflict)
			if err := store.ActivateComputeClaimRecoveryOwnership(context.Background(), conflict); !errors.Is(err, ErrMachineOwnershipConflict) {
				t.Fatalf("identity drift error=%v, want ErrMachineOwnershipConflict", err)
			}
			current, err := store.MachineOwnership(context.Background(), target.ResourceID)
			if err != nil || !reflect.DeepEqual(current, active) {
				t.Fatalf("identity drift changed ownership: current=%#v active=%#v err=%v", current, active, err)
			}
		})
	}
}

func TestPostgresComputeClaimRecoveryOwnershipCASRejectsNonRecoverableStatus(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	store, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.client.Close()

	ownership := postgresComputeClaimOwnership("status")
	ownership.Status = "claimed"
	stored, created, err := store.ClaimMachine(context.Background(), ownership)
	if err != nil || !created {
		t.Fatalf("seed ownership=%#v created=%v err=%v", stored, created, err)
	}
	target := stored
	target.Status = "active"
	if err := store.ActivateComputeClaimRecoveryOwnership(context.Background(), target); !errors.Is(err, ErrMachineOwnershipConflict) {
		t.Fatalf("claimed -> active error=%v, want ErrMachineOwnershipConflict", err)
	}
	current, err := store.MachineOwnership(context.Background(), target.ResourceID)
	if err != nil || current.Status != "claimed" {
		t.Fatalf("non-recoverable status changed ownership: current=%#v err=%v", current, err)
	}
}

type postgresComputeClaimRecoveryProvider struct {
	fakeComputeClaimRecoveryProvider
	storageCreates atomic.Int32
	storageReplays atomic.Int32
}

func (p *postgresComputeClaimRecoveryProvider) CreateStorageVolume(_ context.Context, input StorageVolumeInput) (StorageVolume, error) {
	if input.AllowExistingExactReplay {
		p.storageReplays.Add(1)
	} else {
		p.storageCreates.Add(1)
	}
	return StorageVolume{
		ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "ready", Provider: "tencent-tke",
		ProviderResourceID: "disk-postgres-fixture", ProviderRequestID: providerRequestID("storage", input.IdempotencyKey),
		Zone: input.Zone, SizeGB: input.SizeGB,
	}, nil
}

type failOnceStorageTerminalAppendStore struct {
	OperationStore
	failed bool
}

func (s *failOnceStorageTerminalAppendStore) Append(ctx context.Context, operation FabricOperation) error {
	if !s.failed && operation.Action == "create_storage_volume" && operation.Status == "succeeded" {
		s.failed = true
		return errors.New("storage terminal append failed")
	}
	return s.OperationStore.Append(ctx, operation)
}

func TestPostgresComputeClaimRecoveryRestartAfterActiveOwnershipSkipsProviderMutation(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	firstStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.client.Close()
	provider, input, claimInput := seedPostgresActiveComputeClaimRecovery(t, firstStore, "pro")
	secondStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.client.Close()

	restarted := NewServiceWithOperationStore(provider, secondStore)
	result, err := restarted.ClaimComputeRecovery(context.Background(), claimInput)
	operations, operationsErr := secondStore.List(context.Background())
	if err != nil || operationsErr != nil || !result.Eligible || result.TencentMutationCount != 0 || result.KubernetesMutationCount != 0 ||
		provider.proofCalls != 1 || provider.claimCalls != 0 || provider.tagCalls != 0 || provider.scaleCalls != 0 || provider.storageCreates.Load() != 0 {
		t.Fatalf("restart result=%#v err=%v operationsErr=%v provider=%#v storageCreates=%d", result, err, operationsErr, provider, provider.storageCreates.Load())
	}
	assertRecoveredComputeOperation(t, operations, input, "succeeded")
	ownership, err := secondStore.MachineOwnership(context.Background(), input.ComputeAllocationID)
	if err != nil || ownership.Status != "active" {
		t.Fatalf("restart ownership=%#v err=%v", ownership, err)
	}
}

func TestPostgresComputeClaimRecoveryRestartAfterTargetOwnedReadbackConvergesLocalStateOnly(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	firstStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	provider, input, claimInput := seedPostgresComputeClaimRecovery(t, firstStore, "basic", false)
	if err := firstStore.client.Close(); err != nil {
		t.Fatal(err)
	}
	secondStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.client.Close()

	result, err := NewServiceWithOperationStore(provider, secondStore).ClaimComputeRecovery(context.Background(), claimInput)
	operations, operationsErr := secondStore.List(context.Background())
	ownership, ownershipErr := secondStore.MachineOwnership(context.Background(), input.ComputeAllocationID)
	if err != nil || operationsErr != nil || ownershipErr != nil || !result.Eligible || ownership.Status != "active" ||
		result.TencentMutationCount != 0 || result.KubernetesMutationCount != 0 || provider.proofCalls != 1 || provider.claimCalls != 0 ||
		provider.tagCalls != 0 || provider.scaleCalls != 0 || provider.storageCreates.Load() != 0 {
		t.Fatalf("restart result=%#v err=%v operationsErr=%v ownership=%#v ownershipErr=%v provider=%#v storageCreates=%d", result, err, operationsErr, ownership, ownershipErr, provider, provider.storageCreates.Load())
	}
	assertRecoveredComputeOperation(t, operations, input, "succeeded")
}

func TestPostgresComputeClaimRecoveryReservedReplayAfterRestartReturnsUnknownWithoutMutation(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	firstStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	provider, input, claimInput := seedPostgresComputeClaimRecovery(t, firstStore, "basic", false)
	operations, err := firstStore.List(context.Background())
	if err != nil || len(operations) != 1 {
		t.Fatalf("seed operations=%#v err=%v", operations, err)
	}
	reserved := operations[0]
	reserved.RedactedProviderPayload = withComputeClaimRecoveryMutation(reserved.RedactedProviderPayload, reservedComputeClaimRecoveryMutation())
	if err := firstStore.SaveComputeClaimRecovery(context.Background(), operations[0], reserved); err != nil {
		t.Fatal(err)
	}
	if err := firstStore.client.Close(); err != nil {
		t.Fatal(err)
	}

	secondStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.client.Close()
	result, claimErr := NewServiceWithOperationStore(provider, secondStore).ClaimComputeRecovery(context.Background(), claimInput)
	ownership, ownershipErr := secondStore.MachineOwnership(context.Background(), input.ComputeAllocationID)
	stored, listErr := secondStore.List(context.Background())
	if listErr != nil || len(stored) != 1 {
		t.Fatalf("stored operations=%#v err=%v", stored, listErr)
	}
	ledger, ledgerPresent, ledgerValid := decodeComputeClaimRecoveryMutation(stored[0])

	if claimErr == nil || result.Eligible || result.Reason != "provider_describe" || result.TencentMutationCount != 5 ||
		result.KubernetesMutationCount != 1 || result.Evidence == nil || result.Evidence.CVM.Attempted != 5 ||
		result.Evidence.CVM.Confirmed != 0 || result.Evidence.CVM.Unknown != 5 || result.Evidence.Node.Attempted != 1 ||
		result.Evidence.Node.Confirmed != 0 || result.Evidence.Node.Unknown != 1 || ownershipErr != nil ||
		ownership.Status != "quarantined" || stored[0].Status != "claim_pending" || !ledgerPresent || !ledgerValid ||
		!reflect.DeepEqual(ledger, reservedComputeClaimRecoveryMutation()) || provider.proofCalls != 1 || provider.claimCalls != 0 ||
		provider.tagCalls != 0 || provider.scaleCalls != 0 || provider.storageCreates.Load() != 0 {
		t.Fatalf("result=%#v claimErr=%v ownership=%#v ownershipErr=%v stored=%#v ledger=%#v present=%v valid=%v provider=%#v storageCreates=%d", result, claimErr, ownership, ownershipErr, stored, ledger, ledgerPresent, ledgerValid, provider, provider.storageCreates.Load())
	}
}

func TestPostgresComputeClaimRecoveryFailureReplayDoesNotMutateAcrossServiceInstances(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	firstStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	provider, _, claimInput := seedPostgresComputeClaimRecovery(t, firstStore, "basic", false)
	provider.proof.NodeOwnershipState = "unallocated"
	provider.proof.CVMOwnershipState = "recoverable"
	provider.claim.Proof = provider.proof
	provider.claim.Proof.Reason = "provider_describe"
	provider.claim.TencentMutationCount = 1
	provider.claim.KubernetesMutationCount = 0
	provider.claim.FailureStage = "cvm_tag_readback"
	provider.claim.ProviderErrorClass = "timeout"
	provider.claim.Evidence = &ComputeClaimEvidence{
		CVM:  ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"opl_workspace_id"}},
		Node: ComputeClaimMutationEvidence{},
	}
	provider.claimErr = errors.New("provider readback timed out")

	first, firstErr := NewServiceWithOperationStore(provider, firstStore).ClaimComputeRecovery(context.Background(), claimInput)
	if closeErr := firstStore.client.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	secondStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.client.Close()
	provider.proofErr = errors.New("provider proof unavailable")
	second, secondErr := NewServiceWithOperationStore(provider, secondStore).ClaimComputeRecovery(context.Background(), claimInput)

	if firstErr == nil || secondErr == nil || first.Eligible || second.Eligible || provider.claimCalls != 1 || provider.proofCalls != 2 {
		t.Fatalf("first=%#v firstErr=%v second=%#v secondErr=%v provider=%#v", first, firstErr, second, secondErr, provider)
	}
	if second.Reason != first.Reason || second.TencentMutationCount != first.TencentMutationCount ||
		second.KubernetesMutationCount != first.KubernetesMutationCount || second.FailureStage != first.FailureStage ||
		second.ProviderErrorClass != first.ProviderErrorClass || !reflect.DeepEqual(second.Evidence, first.Evidence) {
		t.Fatalf("PostgreSQL replay changed persisted mutation proof: first=%#v second=%#v", first, second)
	}
}

func TestPostgresComputeClaimRecoveryObservedSuccessReadbackRegressionFailsClosedAcrossRestart(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	firstStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	provider, input, claimInput := seedPostgresComputeClaimRecovery(t, firstStore, "basic", false)
	provider.proof.NodeOwnershipState = "unallocated"
	provider.proof.CVMOwnershipState = "recoverable"
	activationStore := &failOnceComputeClaimActivationStore{OperationStore: firstStore, fail: true}

	first, firstErr := NewServiceWithOperationStore(provider, activationStore).ClaimComputeRecovery(context.Background(), claimInput)
	if closeErr := firstStore.client.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	secondStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.client.Close()
	second, secondErr := NewServiceWithOperationStore(provider, secondStore).ClaimComputeRecovery(context.Background(), claimInput)
	ownership, ownershipErr := secondStore.MachineOwnership(context.Background(), input.ComputeAllocationID)

	if firstErr == nil || secondErr == nil || first.TencentMutationCount != 1 || first.KubernetesMutationCount != 1 ||
		second.Eligible || second.Reason != "identity_mismatch" || second.FailureStage != "claim_final_readback" ||
		second.ProviderErrorClass != "readback_mismatch" || second.TencentMutationCount != 1 || second.KubernetesMutationCount != 1 ||
		ownershipErr != nil || ownership.Status != "quarantined" || provider.proofCalls != 2 || provider.claimCalls != 1 {
		t.Fatalf("first=%#v firstErr=%v second=%#v secondErr=%v ownership=%#v ownershipErr=%v provider=%#v", first, firstErr, second, secondErr, ownership, ownershipErr, provider)
	}
}

func TestPostgresComputeClaimRecoveryNodeReservationCrashFailsClosedWithoutSecondPatch(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	firstStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	provider, input, claimInput := seedPostgresComputeClaimRecovery(t, firstStore, "basic", false)
	provider.proof.NodeOwnershipState = "unallocated"
	provider.proof.CVMOwnershipState = "recoverable"
	provider.claim.Proof = provider.proof
	provider.claim.Proof.Reason = "provider_describe"
	provider.claim.TencentMutationCount = 1
	provider.claim.KubernetesMutationCount = 0
	provider.claim.FailureStage = "cvm_tag_readback"
	provider.claim.ProviderErrorClass = "provider_error"
	provider.claim.Evidence = &ComputeClaimEvidence{CVM: ComputeClaimMutationEvidence{Attempted: 1, Missing: []string{"opl_account_id"}}}
	provider.claimErr = errors.New("provider tag readback failed")
	if _, err := NewServiceWithOperationStore(provider, firstStore).ClaimComputeRecovery(context.Background(), claimInput); err == nil {
		t.Fatal("first claim unexpectedly succeeded")
	}
	provider.proof.CVMOwnershipState = "target_owned"
	provider.proof.NodeOwnershipState = "unallocated"
	provider.claim = ComputeClaimProviderClaim{
		Proof: ComputeClaimProviderProof{
			Status: "proven", Reason: "none", MachineName: provider.proof.MachineName, NodeName: provider.proof.NodeName,
			CVMInstanceID: provider.proof.CVMInstanceID, PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType,
			Zone: provider.proof.Zone, ChargeType: provider.proof.ChargeType, PeriodMonths: provider.proof.PeriodMonths,
			RenewFlag: provider.proof.RenewFlag, Deadline: provider.proof.Deadline, CVMOwnershipState: "target_owned", NodeOwnershipState: "target_owned",
		},
		KubernetesMutationCount: 1,
		Evidence:                &ComputeClaimEvidence{Node: ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1}},
	}
	provider.claimErr = nil
	interrupted, interruptedErr := NewServiceWithOperationStore(provider, &failAfterComputeClaimNodeReservationStore{OperationStore: firstStore}).ClaimComputeRecovery(context.Background(), claimInput)
	if interruptedErr == nil || interrupted.Eligible || provider.claimCalls != 1 {
		t.Fatalf("interrupted=%#v err=%v provider=%#v", interrupted, interruptedErr, provider)
	}
	if err := firstStore.client.Close(); err != nil {
		t.Fatal(err)
	}
	secondStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.client.Close()

	restarted, restartErr := NewServiceWithOperationStore(provider, secondStore).ClaimComputeRecovery(context.Background(), claimInput)
	ownership, ownershipErr := secondStore.MachineOwnership(context.Background(), input.ComputeAllocationID)
	operations, operationsErr := secondStore.List(context.Background())
	ledger, ledgerPresent, ledgerValid := decodeComputeClaimRecoveryMutation(operations[0])
	binding, bindingPresent, bindingValid := decodeComputeClaimRecoveryBinding(operations[0])
	if restartErr == nil || restarted.Eligible || restarted.Reason != "provider_describe" || restarted.TencentMutationCount != 1 ||
		restarted.KubernetesMutationCount != 1 || ownershipErr != nil || ownership.Status != "quarantined" || operationsErr != nil ||
		provider.claimCalls != 1 || !ledgerPresent || !ledgerValid || ledger.State != "node_reserved" || ledger.Evidence.CVM.Confirmed != 1 ||
		ledger.Evidence.Node.Attempted != 1 || ledger.Evidence.Node.Unknown != 1 || !bindingPresent || !bindingValid ||
		binding.IdempotencyKey != input.LaunchOperationID+":compute" {
		t.Fatalf("restarted=%#v err=%v ownership=%#v ownershipErr=%v operationsErr=%v ledger=%#v present=%v valid=%v binding=%#v bindingPresent=%v bindingValid=%v provider=%#v", restarted, restartErr, ownership, ownershipErr, operationsErr, ledger, ledgerPresent, ledgerValid, binding, bindingPresent, bindingValid, provider)
	}
}

func TestPostgresComputeClaimRecoveryNodePatchCrashConvergesByReadbackAndReplaysZeroMutation(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	firstStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	provider, input, claimInput := seedPostgresComputeClaimRecovery(t, firstStore, "basic", false)
	provider.proof.NodeOwnershipState = "unallocated"
	provider.proof.CVMOwnershipState = "recoverable"
	provider.claim.Proof = provider.proof
	provider.claim.Proof.Reason = "provider_describe"
	provider.claim.TencentMutationCount = 1
	provider.claim.KubernetesMutationCount = 0
	provider.claim.FailureStage = "cvm_tag_readback"
	provider.claim.ProviderErrorClass = "provider_error"
	provider.claim.Evidence = &ComputeClaimEvidence{CVM: ComputeClaimMutationEvidence{Attempted: 1, Missing: []string{"opl_account_id"}}}
	provider.claimErr = errors.New("provider tag readback failed")
	if _, err := NewServiceWithOperationStore(provider, firstStore).ClaimComputeRecovery(context.Background(), claimInput); err == nil {
		t.Fatal("first claim unexpectedly succeeded")
	}
	provider.proof.CVMOwnershipState = "target_owned"
	provider.proof.NodeOwnershipState = "unallocated"
	provider.claim = ComputeClaimProviderClaim{
		Proof: ComputeClaimProviderProof{
			Status: "proven", Reason: "none", MachineName: provider.proof.MachineName, NodeName: provider.proof.NodeName,
			CVMInstanceID: provider.proof.CVMInstanceID, PrivateIP: provider.proof.PrivateIP, InstanceType: provider.proof.InstanceType,
			Zone: provider.proof.Zone, ChargeType: provider.proof.ChargeType, PeriodMonths: provider.proof.PeriodMonths,
			RenewFlag: provider.proof.RenewFlag, Deadline: provider.proof.Deadline, CVMOwnershipState: "target_owned", NodeOwnershipState: "target_owned",
		},
		KubernetesMutationCount: 1,
		Evidence:                &ComputeClaimEvidence{Node: ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1}},
	}
	provider.claimErr = nil
	interrupted, interruptedErr := NewServiceWithOperationStore(provider, &failBeforeComputeClaimObservedSaveStore{OperationStore: firstStore}).ClaimComputeRecovery(context.Background(), claimInput)
	if interruptedErr == nil || interrupted.Eligible || provider.claimCalls != 2 {
		t.Fatalf("interrupted=%#v err=%v provider=%#v", interrupted, interruptedErr, provider)
	}
	if err := firstStore.client.Close(); err != nil {
		t.Fatal(err)
	}
	secondStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.client.Close()
	provider.proof.NodeOwnershipState = "target_owned"

	recovered, recoverErr := NewServiceWithOperationStore(provider, secondStore).ClaimComputeRecovery(context.Background(), claimInput)
	replayed, replayErr := NewServiceWithOperationStore(provider, secondStore).ClaimComputeRecovery(context.Background(), claimInput)
	ownership, ownershipErr := secondStore.MachineOwnership(context.Background(), input.ComputeAllocationID)
	operations, operationsErr := secondStore.List(context.Background())
	ledger, ledgerPresent, ledgerValid := decodeComputeClaimRecoveryMutation(operations[0])
	binding, bindingPresent, bindingValid := decodeComputeClaimRecoveryBinding(operations[0])
	if recoverErr != nil || replayErr != nil || !recovered.Eligible || !replayed.Eligible || recovered.TencentMutationCount != 0 ||
		recovered.KubernetesMutationCount != 0 || replayed.TencentMutationCount != 0 || replayed.KubernetesMutationCount != 0 ||
		ownershipErr != nil || ownership.Status != "active" || operationsErr != nil || provider.claimCalls != 2 ||
		!ledgerPresent || !ledgerValid || !successfulNodeClaimRecoveryMutation(ledger) || ledger.Evidence.CVM.Confirmed != 1 ||
		ledger.Evidence.Node.Confirmed != 1 || !bindingPresent || !bindingValid || binding.IdempotencyKey != input.LaunchOperationID+":compute" {
		t.Fatalf("recovered=%#v recoverErr=%v replayed=%#v replayErr=%v ownership=%#v ownershipErr=%v operationsErr=%v ledger=%#v present=%v valid=%v binding=%#v bindingPresent=%v bindingValid=%v provider=%#v", recovered, recoverErr, replayed, replayErr, ownership, ownershipErr, operationsErr, ledger, ledgerPresent, ledgerValid, binding, bindingPresent, bindingValid, provider)
	}
}

func TestPostgresComputeClaimRecoveryStorageIdentityCreatesCBSOnceAcrossRestart(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	firstStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.client.Close()
	provider, input, claimInput := seedPostgresActiveComputeClaimRecovery(t, firstStore, "basic")
	firstService := NewServiceWithOperationStore(provider, firstStore)
	if result, err := firstService.ClaimComputeRecovery(context.Background(), claimInput); err != nil || !result.Eligible {
		t.Fatalf("claim recovery result=%#v err=%v", result, err)
	}
	secondStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.client.Close()
	secondService := NewServiceWithOperationStore(provider, secondStore)
	storageInput := StorageVolumeInput{
		ID: input.StorageVolumeID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID,
		ComputeID: input.ComputeAllocationID, Zone: claimInput.Zone, SizeGB: 10,
		IdempotencyKey: input.LaunchOperationID + ":storage",
	}

	start := make(chan struct{})
	type storageResult struct {
		volume StorageVolume
		err    error
	}
	results := make(chan storageResult, 2)
	for _, service := range []*Service{firstService, secondService} {
		service := service
		go func() {
			<-start
			volume, err := service.CreateStorageVolume(context.Background(), storageInput)
			results <- storageResult{volume: volume, err: err}
		}()
	}
	close(start)
	for range 2 {
		result := <-results
		if result.err != nil || result.volume.ID != input.StorageVolumeID || result.volume.ProviderResourceID != "disk-postgres-fixture" {
			t.Fatalf("storage result=%#v err=%v", result.volume, result.err)
		}
	}
	thirdStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer thirdStore.client.Close()
	replayed, err := NewServiceWithOperationStore(provider, thirdStore).CreateStorageVolume(context.Background(), storageInput)
	if err != nil || replayed.ID != input.StorageVolumeID || provider.storageCreates.Load() != 1 {
		t.Fatalf("storage restart replay=%#v err=%v providerCreates=%d", replayed, err, provider.storageCreates.Load())
	}
	operations, err := thirdStore.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	started, succeeded := 0, 0
	for _, operation := range operations {
		if operation.Action != "create_storage_volume" {
			continue
		}
		if operation.ResourceID != input.StorageVolumeID || operation.IdempotencyKey != input.LaunchOperationID+":storage" {
			t.Fatalf("unexpected storage identity: %#v", operation)
		}
		switch operation.Status {
		case "started":
			started++
		case "succeeded":
			succeeded++
		}
	}
	if started != 1 || succeeded != 1 {
		t.Fatalf("storage transitions started=%d succeeded=%d operations=%#v", started, succeeded, operations)
	}
}

func TestPostgresStorageRecoveryReusesCreatedCBSAfterTerminalWriteFailureAndRestart(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	firstStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	provider, input, claimInput := seedPostgresActiveComputeClaimRecovery(t, firstStore, "basic")
	if result, err := NewServiceWithOperationStore(provider, firstStore).ClaimComputeRecovery(context.Background(), claimInput); err != nil || !result.Eligible {
		t.Fatalf("claim recovery result=%#v err=%v", result, err)
	}
	service := NewServiceWithOperationStore(provider, &failOnceStorageTerminalAppendStore{OperationStore: firstStore})
	storageInput := StorageVolumeInput{
		ID: input.StorageVolumeID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID,
		ComputeID: input.ComputeAllocationID, Zone: claimInput.Zone, SizeGB: 10,
		ExpectedRecoveryState: "storage_not_started", IdempotencyKey: input.LaunchOperationID + ":storage",
	}

	first, firstErr := service.CreateStorageVolume(context.Background(), storageInput)
	if firstErr == nil || first.ProviderResourceID != "disk-postgres-fixture" || provider.storageCreates.Load() != 1 {
		t.Fatalf("first storage=%#v err=%v providerCreates=%d", first, firstErr, provider.storageCreates.Load())
	}
	if err := firstStore.client.Close(); err != nil {
		t.Fatal(err)
	}
	secondStore, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.client.Close()

	replayed, replayErr := NewServiceWithOperationStore(provider, secondStore).CreateStorageVolume(context.Background(), storageInput)
	if replayErr != nil || replayed.ProviderResourceID != "disk-postgres-fixture" || provider.storageCreates.Load() != 1 || provider.storageReplays.Load() != 1 {
		t.Fatalf("replayed storage=%#v err=%v providerCreates=%d providerReplays=%d", replayed, replayErr, provider.storageCreates.Load(), provider.storageReplays.Load())
	}
}

func seedPostgresActiveComputeClaimRecovery(t *testing.T, store *PostgresOperationStore, packageID string) (*postgresComputeClaimRecoveryProvider, ComputeClaimRecoveryInput, ComputeClaimRecoveryClaimInput) {
	return seedPostgresComputeClaimRecovery(t, store, packageID, true)
}

func seedPostgresComputeClaimRecovery(t *testing.T, store *PostgresOperationStore, packageID string, activateOwnership bool) (*postgresComputeClaimRecoveryProvider, ComputeClaimRecoveryInput, ComputeClaimRecoveryClaimInput) {
	t.Helper()
	_, memoryStore, fixtureProvider, input := seedComputeClaimRecovery(t, packageID)
	operations, err := memoryStore.List(context.Background())
	if err != nil || len(operations) != 1 {
		t.Fatalf("fixture operations=%#v err=%v", operations, err)
	}
	operation := operations[0]
	if err := store.Append(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	provider := &postgresComputeClaimRecoveryProvider{fakeComputeClaimRecoveryProvider: *fixtureProvider}
	provider.proof.NodeOwnershipState = "target_owned"
	provider.proof.CVMOwnershipState = "target_owned"
	claimInput := ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: input, MachineName: provider.proof.MachineName, NodeName: provider.proof.NodeName,
		CVMInstanceID: provider.proof.CVMInstanceID, PrivateIP: provider.proof.PrivateIP,
		InstanceType: provider.proof.InstanceType, Zone: provider.proof.Zone,
		IdempotencyKey: input.LaunchOperationID + ":compute",
	}
	pending := operation
	pending.Status, pending.ErrorCode, pending.FinishedAt = "claim_pending", "", time.Time{}
	pending.RedactedProviderPayload = withComputeClaimRecoveryBinding(pending.RedactedProviderPayload, newComputeClaimRecoveryBinding(claimInput))
	if err := store.SaveComputeClaimRecovery(context.Background(), operation, pending); err != nil {
		t.Fatal(err)
	}
	ownership, err := memoryStore.MachineOwnership(context.Background(), input.ComputeAllocationID)
	if err != nil {
		t.Fatal(err)
	}
	stored, created, err := store.ClaimMachine(context.Background(), ownership)
	if err != nil || !created {
		t.Fatalf("seed PostgreSQL ownership=%#v created=%v err=%v", stored, created, err)
	}
	if activateOwnership {
		stored.Status, stored.ReleasedAt = "active", nil
		if err := store.ActivateComputeClaimRecoveryOwnership(context.Background(), stored); err != nil {
			t.Fatal(err)
		}
	}
	return provider, input, claimInput
}

func postgresComputeClaimOperation(suffix, status string) FabricOperation {
	now := time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC)
	operation := newOperation(
		"create_compute_allocation", "compute_allocation", "ca-postgres-"+suffix,
		"acct-postgres-"+suffix, "workspace-postgres-"+suffix, "launch-postgres-"+suffix+":compute",
		"request-hash-"+suffix, now,
	)
	operation.ID = "fop-postgres-compute-claim-" + suffix
	operation.Status = status
	operation.CreatedAt = now
	if status == "failed" {
		operation.FinishedAt = now.Add(time.Second)
	}
	fillOperationResource(&operation, ComputeAllocation{
		ID: operation.ResourceID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID,
	})
	if status == "claim_pending" {
		operation.RedactedProviderPayload = withComputeClaimRecoveryBinding(operation.RedactedProviderPayload, postgresComputeClaimRecoveryBinding(suffix))
	}
	return operation
}

func postgresComputeClaimRecoveryBinding(suffix string) computeClaimRecoveryBinding {
	return computeClaimRecoveryBinding{
		LaunchOperationID: "launch-postgres-" + suffix,
		IdempotencyKey:    "launch-postgres-" + suffix + ":compute",
		TargetHash:        "target-hash-" + suffix,
		RequestHash:       "claim-request-hash-" + suffix,
	}
}

func postgresComputeClaimOwnership(suffix string) MachineOwnership {
	return MachineOwnership{
		ID: "owner-postgres-" + suffix, ResourceID: "ca-postgres-" + suffix,
		AccountID: "acct-postgres-" + suffix, WorkspaceID: "workspace-postgres-" + suffix,
		PackageID: "basic", NodePoolID: "np-postgres-basic", MachineID: "machine-postgres-" + suffix,
		InstanceID: "ins-postgres-" + suffix, NodeName: "node-postgres-" + suffix, Status: "quarantined",
		ProviderRequestID: "redacted-provider-reference", ClaimedAt: time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC),
	}
}

func TestPostgresOperationStoreRunsEmbeddedMigrationsOnce(t *testing.T) {
	databaseURL := fabricTestDatabaseURL(t)
	first, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.client.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var migrationCount int
	if err := db.QueryRow(`SELECT count(*) FROM opl_schema_migrations WHERE service = 'fabric'`).Scan(&migrationCount); err != nil {
		t.Fatalf("read Fabric migration journal: %v", err)
	}
	if migrationCount != 6 {
		t.Fatalf("Fabric migration count = %d, want 6", migrationCount)
	}
	if _, err := db.Exec(`DROP TABLE machine_ownerships`); err != nil {
		t.Fatal(err)
	}
	second, err := newTestPostgresOperationStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.client.Close(); err != nil {
		t.Fatal(err)
	}
	var table sql.NullString
	if err := db.QueryRow(`SELECT to_regclass('machine_ownerships')`).Scan(&table); err != nil {
		t.Fatal(err)
	}
	if table.Valid {
		t.Fatal("second Fabric startup repeated embedded DDL")
	}
}

func fabricTestDatabaseURL(t *testing.T) string {
	t.Helper()
	databaseURL := os.Getenv("FABRIC_TEST_DATABASE_URL")
	optional := false
	if databaseURL == "" {
		if os.Getenv("OPL_POSTGRES_TESTS") == "1" {
			databaseURL = "connect_timeout=10"
		} else {
			databaseURL = "host=/var/run/postgresql dbname=postgres sslmode=disable"
			optional = true
		}
	}
	admin, err := sql.Open("postgres", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := admin.Ping(); err != nil {
		_ = admin.Close()
		if optional {
			t.Skipf("local PostgreSQL unavailable: %v", err)
		}
		t.Fatal(err)
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	schema := "fabric_test_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		_ = admin.Close()
	})
	if parsed, err := url.Parse(databaseURL); err == nil && parsed.Scheme != "" {
		query := parsed.Query()
		query.Set("search_path", schema)
		query.Set("connect_timeout", "5")
		query.Set("statement_timeout", "10000")
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return databaseURL + " search_path=" + schema + " connect_timeout=5 statement_timeout=10000"
}
