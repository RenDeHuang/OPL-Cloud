package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

func canonicalResumePrepareRequest(_ workspaceLaunchReconcileOperation, _ string) productionAcceptanceBResumeExistingPrepareRequest {
	var request productionAcceptanceBResumeExistingPrepareRequest
	request.ApprovalID = "acceptance-b-resume-prepare"
	request.AuthorizationID = "acceptance-b-resume-auth"
	request.ReasonSHA256 = strings.Repeat("e", 64)
	request.Release.CanonicalCloudSHA = strings.Repeat("a", 40)
	request.Release.CanonicalCloudTree = strings.Repeat("d", 40)
	request.Release.DeployedCloudImageDigest = "sha256:" + strings.Repeat("b", 64)
	return request
}

func reservedResumePrepareFixture(t *testing.T) (*workspaceLaunchUnitStore, *workspaceLaunchUnitAdapter, workspaceLaunchReconcileOperation) {
	t.Helper()
	row := workspaceLaunchReservedStageManualReviewRow(t, "debit")
	operation, err := decodeWorkspaceLaunchReconcileOperation(row)
	if err != nil {
		t.Fatal(err)
	}
	return &workspaceLaunchUnitStore{row: row}, &workspaceLaunchUnitAdapter{
		stageObservations: map[string]workspaceLaunchStageObservation{"debit": {State: workspaceLaunchStageAbsent}},
	}, operation
}

func configureResumePrepareRelease(t *testing.T) {
	t.Helper()
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_RELEASE_TREE", strings.Repeat("d", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "registry.example/opl-cloud@sha256:"+strings.Repeat("b", 64))
}

type workspaceLaunchResumePrepareLedger struct {
	fakeLedgerClient
	mutationCalls int
}

func (l *workspaceLaunchResumePrepareLedger) RecordReceipt(ctx context.Context, input clients.ReceiptInput, idempotencyKey string) (clients.Receipt, error) {
	l.mutationCalls++
	return l.fakeLedgerClient.RecordReceipt(ctx, input, idempotencyKey)
}

func (l *workspaceLaunchResumePrepareLedger) RecordReconciliation(ctx context.Context, input clients.ReconciliationInput, idempotencyKey string) (clients.ReconciliationResult, error) {
	l.mutationCalls++
	return l.fakeLedgerClient.RecordReconciliation(ctx, input, idempotencyKey)
}

func TestProductionAcceptanceBResumePrepareBuildsExactReadOnlyCandidate(t *testing.T) {
	configureResumePrepareRelease(t)
	store, adapter, operation := reservedResumePrepareFixture(t)
	before := stringValue(store.row["result"])
	now := time.Date(2026, 8, 17, 2, 0, 0, 0, time.UTC)
	request := canonicalResumePrepareRequest(operation, workspaceLaunchStageAbsent)

	approval, err := prepareProductionAcceptanceBResumeExisting(context.Background(), store, adapter, operation.ID, request, now)
	if err != nil {
		t.Fatal(err)
	}
	if approval.SchemaVersion != 1 || approval.OperationMode != "acceptance_b_resume_existing" || approval.ApprovalID != request.ApprovalID ||
		approval.ExpiresAt != now.Add(productionAcceptanceBResumePrepareLifetime).Format(time.RFC3339) || approval.Release != request.Release ||
		approval.Authorization.OperationID != operation.ID || approval.Authorization.LaunchVersion != operation.Version ||
		approval.Authorization.AuthorizedStage != operation.Stage || approval.Authorization.ReasonSHA256 != request.ReasonSHA256 ||
		approval.Authorization.MutationBudget != 0 || approval.Authorization.IdempotentReplayBudget != 1 ||
		approval.Authorization.AuthoritativeReadBudget != workspaceLaunchAuthoritativeReadBudget ||
		approval.Reconciliation.AuthoritativeStageState != workspaceLaunchStageAbsent || approval.IdentityDigests != workspaceLaunchAcceptanceBIdentityDigests(operation) {
		t.Fatalf("unexpected candidate: %#v", approval)
	}
	if adapter.reads != 1 || adapter.mutations != 0 || store.persistenceWrites != 0 || stringValue(store.row["result"]) != before {
		t.Fatalf("prepare mutated authority: reads=%d mutations=%d persistenceWrites=%d changed=%v", adapter.reads, adapter.mutations, store.persistenceWrites, stringValue(store.row["result"]) != before)
	}
	authorization := workspaceLaunchResumeAuthorization{
		AuthorizationID: request.AuthorizationID, LaunchVersion: operation.Version, AuthorizedStage: operation.Stage,
		AuthorizedBy: "operator-reviewer", AuthorizedAt: now.Format(time.RFC3339), Reason: "reviewed exact stage evidence",
		MutationBudget: approval.Authorization.MutationBudget, IdempotentReplayBudget: approval.Authorization.IdempotentReplayBudget,
		AuthoritativeReadBudget: approval.Authorization.AuthoritativeReadBudget,
	}
	approval.Authorization.ReasonSHA256 = acceptanceBDigestParts(authorization.Reason)
	header := http.Header{}
	header.Set(productionAcceptanceBApprovalID, approval.ApprovalID)
	header.Set(productionAcceptanceBCapability, "prepare-capability")
	t.Setenv("OPL_INTERNAL_SERVICE_TOKEN", "prepare-capability")
	if _, ok := productionAcceptanceBResumeExistingApproved(header, approval, authorization, operation, workspaceLaunchStageObservation{State: workspaceLaunchStageAbsent}, now); !ok {
		t.Fatal("exact prepared candidate was not admitted by Resume")
	}
	if _, ok := productionAcceptanceBResumeExistingApproved(header, approval, authorization, operation, workspaceLaunchStageObservation{State: workspaceLaunchStagePending}, now); ok {
		t.Fatal("Resume admitted a candidate after provider observation drift")
	}
	drifted := operation
	drifted.Version++
	if _, ok := productionAcceptanceBResumeExistingApproved(header, approval, authorization, drifted, workspaceLaunchStageObservation{State: workspaceLaunchStageAbsent}, now); ok {
		t.Fatal("Resume admitted a candidate after operation version drift")
	}
	drifted = operation
	driftedAttempt := drifted.Attempts[drifted.Stage]
	driftedAttempt.Unknown, driftedAttempt.Status = 1, "unknown"
	drifted.Attempts[drifted.Stage] = driftedAttempt
	if _, ok := productionAcceptanceBResumeExistingApproved(header, approval, authorization, drifted, workspaceLaunchStageObservation{State: workspaceLaunchStageAbsent}, now); ok {
		t.Fatal("Resume admitted a candidate after attempt drift")
	}
	driftedAuthorization := authorization
	driftedAuthorization.IdempotentReplayBudget = 0
	if _, ok := productionAcceptanceBResumeExistingApproved(header, approval, driftedAuthorization, operation, workspaceLaunchStageObservation{State: workspaceLaunchStageAbsent}, now); ok {
		t.Fatal("Resume admitted a candidate after budget drift")
	}
	if _, ok := productionAcceptanceBResumeExistingApproved(header, approval, authorization, operation, workspaceLaunchStageObservation{State: workspaceLaunchStageAbsent}, now.Add(productionAcceptanceBResumePrepareLifetime)); ok {
		t.Fatal("Resume admitted an expired prepared candidate")
	}

	encoded, err := json.Marshal(approval)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"acct-unit", "usr-unit", "ws-unit", "profile-unit", "binding-unit", "sub2apiRedeemCode", `"idempotencyKey"`} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("candidate leaked raw owner fact %q: %s", forbidden, encoded)
		}
	}
}

func TestProductionAcceptanceBResumePrepareDebitReadHasZeroOwnerMutations(t *testing.T) {
	configureResumePrepareRelease(t)
	store, _, operation := reservedResumePrepareFixture(t)
	var fabricCalls []string
	fabric := &fakeFabricClient{calls: &fabricCalls}
	ledger := &workspaceLaunchResumePrepareLedger{}
	sub2API := &workspaceLaunchDebitReadbackStub{
		testSub2APIClient: &testSub2APIClient{charges: map[string]int64{}},
		history:           map[string]clients.Sub2APIBalanceHistoryEntry{},
	}
	service := controlplane.NewService(ledger, fabric, sub2API)
	adapter := &controlPlaneWorkspaceLaunchStageAdapter{app: &controlPlaneServer{}, service: service}
	request := canonicalResumePrepareRequest(operation, workspaceLaunchStagePending)

	approval, err := prepareProductionAcceptanceBResumeExisting(context.Background(), store, adapter, operation.ID, request, time.Now())
	if err != nil || approval.Reconciliation.AuthoritativeStageState != workspaceLaunchStagePending {
		t.Fatalf("debit read-only prepare failed: approval=%#v err=%v", approval, err)
	}
	if sub2API.historyReads != 1 || store.persistenceWrites != 0 || sub2API.chargeCalls != 0 || len(fabricCalls) != 0 || ledger.mutationCalls != 0 {
		t.Fatalf("prepare crossed an owner mutation boundary: sub2APIHistoryReads=%d persistenceWrites=%d sub2APIDebitMutations=%d fabricCalls=%v ledgerMutations=%d", sub2API.historyReads, store.persistenceWrites, sub2API.chargeCalls, fabricCalls, ledger.mutationCalls)
	}
}

func TestProductionAcceptanceBResumePrepareRejectsNonDebitStagesBeforeOwnerRead(t *testing.T) {
	configureResumePrepareRelease(t)
	for _, stage := range []string{"key", "ensure_compute_allocation", "storage", "attachment", "secret", "runtime", "activation", "receipt"} {
		t.Run(stage, func(t *testing.T) {
			store, adapter, operation := reservedResumePrepareFixture(t)
			operation.Stage = stage
			attempt := operation.Attempts[stage]
			attempt.Attempted, attempt.Status = 1, "reserved"
			attempt.IdempotencyKey = workspaceLaunchStageIdempotencyKey(operation, 1)
			operation.Attempts[stage] = attempt
			row, err := workspaceLaunchReconcileOperationRow(operation)
			if err != nil {
				t.Fatal(err)
			}
			store.row = row
			request := canonicalResumePrepareRequest(operation, workspaceLaunchStageReady)
			before := stringValue(store.row["result"])
			if _, err := prepareProductionAcceptanceBResumeExisting(context.Background(), store, adapter, operation.ID, request, time.Now()); err == nil {
				t.Fatal("non-debit stage was admitted")
			}
			// The rejected stage must stop before any owner read or write-capable path.
			if adapter.reads != 0 || adapter.mutations != 0 || store.persistenceWrites != 0 || stringValue(store.row["result"]) != before {
				t.Fatalf("non-debit prepare crossed a mutation boundary: reads=%d mutations=%d persistenceWrites=%d changed=%v", adapter.reads, adapter.mutations, store.persistenceWrites, stringValue(store.row["result"]) != before)
			}
		})
	}
}

func TestProductionAcceptanceBResumePrepareRejectsDriftAndUnknownWithoutMutation(t *testing.T) {
	configureResumePrepareRelease(t)
	tests := []struct {
		name   string
		mutate func(*productionAcceptanceBResumeExistingPrepareRequest, *workspaceLaunchUnitAdapter)
	}{
		{name: "release sha", mutate: func(r *productionAcceptanceBResumeExistingPrepareRequest, _ *workspaceLaunchUnitAdapter) {
			r.Release.CanonicalCloudSHA = strings.Repeat("f", 40)
		}},
		{name: "release tree", mutate: func(r *productionAcceptanceBResumeExistingPrepareRequest, _ *workspaceLaunchUnitAdapter) {
			r.Release.CanonicalCloudTree = strings.Repeat("f", 40)
		}},
		{name: "image", mutate: func(r *productionAcceptanceBResumeExistingPrepareRequest, _ *workspaceLaunchUnitAdapter) {
			r.Release.DeployedCloudImageDigest = "sha256:" + strings.Repeat("f", 64)
		}},
		{name: "unknown observation", mutate: func(_ *productionAcceptanceBResumeExistingPrepareRequest, a *workspaceLaunchUnitAdapter) {
			a.stageObservations["debit"] = workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}
		}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			store, adapter, operation := reservedResumePrepareFixture(t)
			before := stringValue(store.row["result"])
			request := canonicalResumePrepareRequest(operation, workspaceLaunchStageAbsent)
			testCase.mutate(&request, adapter)
			if _, err := prepareProductionAcceptanceBResumeExisting(context.Background(), store, adapter, operation.ID, request, time.Now()); err == nil {
				t.Fatal("drifted prepare was accepted")
			}
			if adapter.mutations != 0 || stringValue(store.row["result"]) != before {
				t.Fatal("rejected prepare mutated authority")
			}
		})
	}
}

func TestProductionAcceptanceBResumePrepareRejectsInvalidBudgetState(t *testing.T) {
	configureResumePrepareRelease(t)
	store, adapter, operation := reservedResumePrepareFixture(t)
	attempt := operation.Attempts[operation.Stage]
	attempt.Max = 2
	operation.Attempts[operation.Stage] = attempt
	row, err := workspaceLaunchReconcileOperationRow(operation)
	if err != nil {
		t.Fatal(err)
	}
	store.row = row
	request := canonicalResumePrepareRequest(operation, workspaceLaunchStageAbsent)
	if _, err := prepareProductionAcceptanceBResumeExisting(context.Background(), store, adapter, operation.ID, request, time.Now()); err == nil {
		t.Fatal("non-canonical budget state was accepted")
	}
}

func TestProductionAcceptanceBResumePrepareRequestRequiresExactShortLivedBinding(t *testing.T) {
	configureResumePrepareRelease(t)
	_, _, operation := reservedResumePrepareFixture(t)
	request := canonicalResumePrepareRequest(operation, workspaceLaunchStageAbsent)
	if !productionAcceptanceBResumeExistingPrepareRequestValid(request) {
		t.Fatal("canonical prepare request was rejected")
	}
	for _, mutate := range []func(*productionAcceptanceBResumeExistingPrepareRequest){
		func(value *productionAcceptanceBResumeExistingPrepareRequest) { value.ApprovalID = "short" },
		func(value *productionAcceptanceBResumeExistingPrepareRequest) { value.AuthorizationID = "bad id" },
		func(value *productionAcceptanceBResumeExistingPrepareRequest) { value.ReasonSHA256 = "raw reason" },
	} {
		drifted := request
		mutate(&drifted)
		if productionAcceptanceBResumeExistingPrepareRequestValid(drifted) {
			t.Fatalf("invalid prepare request passed validation: %#v", drifted)
		}
	}
}
