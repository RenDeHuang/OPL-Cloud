package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

type workspaceRecoveryPlanFabric struct {
	*monthlyFabric
	identityEvidence *clients.ComputeClaimIdentityEvidence
}

func (f *workspaceRecoveryPlanFabric) ComputeClaimRecoveryIdentityEvidence(_ context.Context, _ clients.ComputeClaimRecoveryClaimInput) (*clients.ComputeClaimIdentityEvidence, error) {
	return f.identityEvidence, nil
}

func useWorkspaceRecoveryPlanIdentityEvidence(t *testing.T, fixture *workspaceLaunchWorkerFixture, evidence *clients.ComputeClaimIdentityEvidence) {
	t.Helper()
	fixture.service = controlplane.NewService(fixture.ledger, &workspaceRecoveryPlanFabric{
		monthlyFabric: fixture.fabric, identityEvidence: evidence,
	}, fixture.sub2API)
	server, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)
}

func requestWorkspaceRecoveryPlan(t *testing.T, fixture workspaceLaunchWorkerFixture, method, suffix string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	operation := fixture.operation(t)
	request := httptest.NewRequest(method, "/api/operator/workspace-launches/"+operation.ID+"/recovery-plan"+suffix, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	if suffix == "/execute" {
		request.Header.Set("Idempotency-Key", "recovery-plan:"+stringValue(body["planDigest"]))
	}
	addAuth(request, fixture.operator)
	response := httptest.NewRecorder()
	fixture.server.ServeHTTP(response, request)
	return response
}

func TestWorkspaceRecoveryPlanRoutesRequireOperatorSessionAndCSRF(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "runtime", "basic")
	fixture := scenario.fixture
	fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
	server, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)
	path := "/api/operator/workspace-launches/" + scenario.unknown.ID + "/recovery-plan/diagnose"
	body := `{"accountId":"` + scenario.unknown.AccountID + `"}`

	unauthenticated := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	unauthenticated.Header.Set("Content-Type", "application/json")
	unauthenticatedResponse := httptest.NewRecorder()
	server.ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d body=%s", unauthenticatedResponse.Code, unauthenticatedResponse.Body.String())
	}

	owner := tenantOwnerSessionForTest(t, server)
	ownerResponse := requestWithSession(t, server, owner, http.MethodPost, path, body)
	if ownerResponse.Code != http.StatusForbidden {
		t.Fatalf("non-operator status=%d body=%s", ownerResponse.Code, ownerResponse.Body.String())
	}

	missingCSRF := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	missingCSRF.Header.Set("Content-Type", "application/json")
	addSessionCookies(missingCSRF, fixture.operator)
	missingCSRFResponse := httptest.NewRecorder()
	server.ServeHTTP(missingCSRFResponse, missingCSRF)
	if missingCSRFResponse.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d body=%s", missingCSRFResponse.Code, missingCSRFResponse.Body.String())
	}

	operatorResponse := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": scenario.unknown.AccountID})
	if operatorResponse.Code != http.StatusOK {
		t.Fatalf("operator status=%d body=%s", operatorResponse.Code, operatorResponse.Body.String())
	}
	if scenario.readback.stageConvergeCalls != 0 || workspaceLaunchStageWriteCount(fixture, "runtime") != scenario.beforeCurrentWrites {
		t.Fatalf("authorization checks crossed mutation boundary: converge=%d", scenario.readback.stageConvergeCalls)
	}
}

func TestWorkspaceRecoveryPlanExecuteRejectsIdempotencyKeyNotBoundToPlan(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "runtime", "basic")
	fixture := scenario.fixture
	fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
	server, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)
	diagnosed := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": scenario.unknown.AccountID}))
	validated := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", map[string]any{
		"planId": diagnosed.PlanID, "planDigest": diagnosed.PlanDigest,
	}))
	body, err := json.Marshal(map[string]any{
		"planId": validated.PlanID, "planDigest": validated.PlanDigest, "decision": "continue", "confirmation": "CONTINUE_RECOVERY_PLAN",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/operator/workspace-launches/"+scenario.unknown.ID+"/recovery-plan/execute", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "recovery-plan:unbound")
	addAuth(request, fixture.operator)
	response := httptest.NewRecorder()
	fixture.server.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unbound idempotency key status=%d body=%s", response.Code, response.Body.String())
	}
	persisted := fixture.operation(t)
	if persisted.RecoveryExecution != nil || scenario.readback.stageConvergeCalls != 0 || workspaceLaunchStageWriteCount(fixture, "runtime") != scenario.beforeCurrentWrites {
		t.Fatalf("unbound idempotency key crossed execution boundary: operation=%#v converge=%d", persisted, scenario.readback.stageConvergeCalls)
	}
}

func recoveryPlanResponse(t *testing.T, response *httptest.ResponseRecorder) workspaceRecoveryPlanDTO {
	t.Helper()
	var plan workspaceRecoveryPlanDTO
	if err := json.NewDecoder(response.Body).Decode(&plan); err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestWorkspaceRecoveryPlanRetiresLegacyPublicRecoveryRoutes(t *testing.T) {
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "runtime", "basic")
	fixture := scenario.fixture
	fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
	server, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)
	operation := fixture.operation(t)
	routes := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/operator/workspace-launches/" + operation.ID + "/recover"},
		{http.MethodGet, "/api/operator/workspace-launches/" + operation.ID + "/readback-recovery-proof"},
		{http.MethodPost, "/api/operator/workspace-launches/" + operation.ID + "/compute-claim-recovery/proof"},
		{http.MethodPost, "/api/operator/workspace-launches/" + operation.ID + "/compute-claim-recovery/validate"},
		{http.MethodPost, "/api/operator/workspace-launches/" + operation.ID + "/compute-claim-recovery/claim"},
	}
	for _, route := range routes {
		request := httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "legacy-recovery-route")
		addAuth(request, fixture.operator)
		response := httptest.NewRecorder()
		fixture.server.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("legacy route %s %s status=%d body=%s", route.method, route.path, response.Code, response.Body.String())
		}
	}
	if scenario.readback.stageConvergeCalls != 0 || workspaceLaunchStageWriteCount(fixture, "runtime") != scenario.beforeCurrentWrites {
		t.Fatalf("retired routes crossed mutation boundary: converge=%d", scenario.readback.stageConvergeCalls)
	}
}

func TestWorkspaceRecoveryPlanHTTPProjectionOmitsInternalAuthorityBindings(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "runtime", "basic")
	fixture := scenario.fixture
	fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
	server, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)

	response := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": scenario.unknown.AccountID})
	if response.Code != http.StatusOK {
		t.Fatalf("diagnose status=%d body=%s", response.Code, response.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"action", "generatedAt", "validatedAt", "releaseBinding", "targetBinding", "allowedDecisions", "identityEvidence", "approval", "computeClaimRequest"} {
		if _, ok := payload[forbidden]; ok {
			t.Fatalf("HTTP projection exposed internal authority field %q: %s", forbidden, response.Body.String())
		}
	}
	for _, required := range []string{"planId", "planDigest", "status", "operationId", "stages", "mismatches", "mutationCounts"} {
		if _, ok := payload[required]; !ok {
			t.Fatalf("HTTP projection omitted Console field %q: %s", required, response.Body.String())
		}
	}
	counts, ok := payload["mutationCounts"].(map[string]any)
	if !ok || len(counts) != 3 || counts["sub2api"] != float64(0) || counts["tencent"] != float64(0) || counts["kubernetes"] != float64(0) {
		t.Fatalf("HTTP projection mutation counts invalid: %s", response.Body.String())
	}
	persisted := fixture.operation(t)
	if persisted.RecoveryPlan == nil || persisted.RecoveryPlan.ReleaseBinding.MainSHA == "" || persisted.RecoveryPlan.TargetBinding.AuthorityDigest == "" {
		t.Fatalf("safe projection discarded persisted authority: %#v", persisted.RecoveryPlan)
	}
}

func TestWorkspaceRecoveryPlanIdentityEvidenceComparesPrivateIPToPersistedLaunch(t *testing.T) {
	operation := workspaceLaunchOperation{
		ID: "workspace-launch-private-ip", AccountID: "acct-private-ip", WorkspaceID: "ws-private-ip",
		ComputeID: "ca-private-ip", StorageID: "vol-private-ip", ComputePrivateIP: "10.20.30.41",
	}
	proof := workspaceLaunchReadbackRecoveryProof{Target: workspaceLaunchReadbackRecoveryTarget{
		LaunchOperationID: operation.ID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID,
		ComputeAllocationID: operation.ComputeID, StorageID: operation.StorageID, PrivateIP: "10.20.30.99",
	}}
	checks := workspaceRecoveryPlanIdentityEvidence(operation, proof, workspaceRecoveryReleaseBinding{})
	found := false
	for _, check := range checks {
		if check.Field == "target.privateIp" {
			found = true
			if check.Matches || check.ExpectedDigest == check.ActualDigest {
				t.Fatalf("private IP drift was self-compared: %#v", check)
			}
		}
	}
	if !found {
		t.Fatal("private IP identity evidence missing")
	}
}

func TestWorkspaceRecoveryPlanDiagnoseBuildsAndPersistsServerAuthority(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "secret", "basic")
	fixture := scenario.fixture
	fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
	server, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)

	response := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{
		"accountId": scenario.unknown.AccountID,
	})
	if response.Code != http.StatusOK {
		t.Fatalf("diagnose status=%d body=%s", response.Code, response.Body.String())
	}
	plan := recoveryPlanResponse(t, response)
	if plan.PlanID == "" || plan.PlanDigest == "" || plan.Status != "diagnosed" || plan.OperationID != scenario.unknown.ID {
		t.Fatalf("diagnosed plan=%#v launchPrivateIP=%q proofPrivateIP=%q", plan, scenario.unknown.ComputePrivateIP, scenario.readback.providerTruth.Compute.PrivateIP)
	}
	persisted, ok, err := fixture.app.workspaceLaunchOperation(context.Background(), scenario.unknown.ID)
	if err != nil || !ok || persisted.RecoveryPlan == nil || persisted.RecoveryPlan.PlanID != plan.PlanID || persisted.RecoveryPlan.PlanDigest != plan.PlanDigest ||
		persisted.RecoveryPlan.Action != "unknown_stage_continue" || persisted.RecoveryPlan.ReleaseBinding.MainSHA != strings.Repeat("a", 40) ||
		persisted.RecoveryPlan.ReleaseBinding.CloudImageDigest != "sha256:"+strings.Repeat("b", 64) || persisted.RecoveryPlan.TargetBinding.LaunchOperationID != scenario.unknown.ID ||
		persisted.RecoveryPlan.TargetBinding.AccountID != scenario.unknown.AccountID || persisted.RecoveryPlan.TargetBinding.WorkspaceID != scenario.unknown.WorkspaceID ||
		persisted.RecoveryPlan.TargetBinding.WorkspaceImageDigest != scenario.unknown.WorkspaceImageDigest || persisted.RecoveryPlan.MutationCounts != (workspaceRecoveryMutationCounts{}) {
		t.Fatalf("persisted recovery plan operation=%#v ok=%v err=%v", persisted, ok, err)
	}
	if scenario.readback.stageConvergeCalls != 0 || workspaceLaunchStageWriteCount(fixture, "secret") != scenario.beforeCurrentWrites {
		t.Fatalf("diagnosis performed recovery mutation: converge=%d before=%d after=%d", scenario.readback.stageConvergeCalls, scenario.beforeCurrentWrites, workspaceLaunchStageWriteCount(fixture, "secret"))
	}
}

func TestWorkspaceRecoveryPlanReadAndValidateReportExactReleaseDriftWithoutExternalMutation(t *testing.T) {
	mainSHA := strings.Repeat("a", 40)
	t.Setenv("OPL_RELEASE_SHA", mainSHA)
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "runtime", "basic")
	fixture := scenario.fixture
	fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
	server, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)
	diagnose := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": scenario.unknown.AccountID})
	if diagnose.Code != http.StatusOK {
		t.Fatalf("diagnose status=%d body=%s", diagnose.Code, diagnose.Body.String())
	}
	plan := recoveryPlanResponse(t, diagnose)

	read := requestWorkspaceRecoveryPlan(t, fixture, http.MethodGet, "", nil)
	if read.Code != http.StatusOK {
		t.Fatalf("read status=%d body=%s", read.Code, read.Body.String())
	}
	persisted := recoveryPlanResponse(t, read)
	if persisted.PlanID != plan.PlanID || persisted.PlanDigest != plan.PlanDigest || persisted.OperationID != scenario.unknown.ID || len(persisted.Mismatches) != 0 {
		t.Fatalf("persisted plan=%#v", persisted)
	}

	validateBody := map[string]any{"planId": plan.PlanID, "planDigest": plan.PlanDigest}
	validatedResponse := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", validateBody)
	if validatedResponse.Code != http.StatusOK {
		t.Fatalf("validate status=%d body=%s", validatedResponse.Code, validatedResponse.Body.String())
	}
	validated := recoveryPlanResponse(t, validatedResponse)
	validatedOperation := fixture.operation(t)
	if validated.Status != "validated" || len(validated.Mismatches) != 0 || validatedOperation.RecoveryPlan == nil || validatedOperation.RecoveryPlan.ValidatedAt == "" {
		t.Fatalf("validated plan=%#v", validated)
	}

	driftedSHA := strings.Repeat("c", 40)
	t.Setenv("OPL_RELEASE_SHA", driftedSHA)
	driftResponse := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", validateBody)
	if driftResponse.Code != http.StatusOK {
		t.Fatalf("drift validate status=%d body=%s", driftResponse.Code, driftResponse.Body.String())
	}
	drifted := recoveryPlanResponse(t, driftResponse)
	if drifted.Status != "blocked" || len(drifted.Mismatches) != 1 || drifted.Mismatches[0].Field != "release.mainSha" ||
		drifted.Mismatches[0].Expected != mainSHA || drifted.Mismatches[0].Actual != driftedSHA || drifted.ErrorCode != "identity_mismatch" {
		t.Fatalf("drifted plan=%#v", drifted)
	}
	if scenario.readback.stageConvergeCalls != 0 || workspaceLaunchStageWriteCount(fixture, "runtime") != scenario.beforeCurrentWrites {
		t.Fatalf("validation performed recovery mutation: converge=%d before=%d after=%d", scenario.readback.stageConvergeCalls, scenario.beforeCurrentWrites, workspaceLaunchStageWriteCount(fixture, "runtime"))
	}
}

func TestWorkspaceRecoveryPlanExecuteUsesOnePersistedExecutionAndOriginalLaunchContinuation(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "secret", "basic")
	fixture := scenario.fixture
	fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
	server, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)

	diagnosed := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": scenario.unknown.AccountID}))
	validated := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", map[string]any{
		"planId": diagnosed.PlanID, "planDigest": diagnosed.PlanDigest,
	}))
	if validated.Status != "validated" {
		t.Fatalf("validated plan=%#v", validated)
	}
	executeBody := map[string]any{
		"planId": validated.PlanID, "planDigest": validated.PlanDigest, "decision": "continue", "confirmation": "CONTINUE_RECOVERY_PLAN",
	}
	firstResponse := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/execute", executeBody)
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("execute status=%d body=%s", firstResponse.Code, firstResponse.Body.String())
	}
	first := recoveryPlanResponse(t, firstResponse)
	if first.ExecutionID == "" || first.RunID == "" || first.Status != "completed" || first.URL == "" || first.ReceiptID == "" {
		t.Fatalf("executed plan=%#v", first)
	}
	writesAfterFirst := map[string]int{}
	for _, stage := range workspaceLaunchContinuationStages {
		writesAfterFirst[stage] = workspaceLaunchStageWriteCount(fixture, stage)
	}
	secondResponse := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/execute", executeBody)
	if secondResponse.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", secondResponse.Code, secondResponse.Body.String())
	}
	second := recoveryPlanResponse(t, secondResponse)
	if second.ExecutionID != first.ExecutionID || second.RunID != first.RunID || second.URL != first.URL || second.ReceiptID != first.ReceiptID {
		t.Fatalf("execution replay drift first=%#v second=%#v", first, second)
	}
	for _, stage := range workspaceLaunchContinuationStages {
		if got := workspaceLaunchStageWriteCount(fixture, stage); got != writesAfterFirst[stage] {
			t.Fatalf("replay repeated %s write: before=%d after=%d", stage, writesAfterFirst[stage], got)
		}
	}
	if len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeIDs) != 1 || len(fixture.fabric.storageIDs) != 1 {
		t.Fatalf("recovery repeated commercial/provider mutation: charges=%d compute=%d storage=%d", len(fixture.sub2API.charges), len(fixture.fabric.computeIDs), len(fixture.fabric.storageIDs))
	}
	persisted, ok, err := fixture.app.workspaceLaunchOperation(context.Background(), scenario.unknown.ID)
	if err != nil || !ok || persisted.RecoveryExecution == nil || persisted.RecoveryExecution.ExecutionID != first.ExecutionID || persisted.Status != "succeeded" {
		t.Fatalf("persisted execution operation=%#v ok=%v err=%v", persisted, ok, err)
	}
}

func TestWorkspaceRecoveryPlanExecutionLeaseHasOneCrossInstanceWinner(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "runtime", "basic")
	fixture := scenario.fixture
	fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
	server, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)
	diagnosed := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": scenario.unknown.AccountID}))
	validated := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", map[string]any{
		"planId": diagnosed.PlanID, "planDigest": diagnosed.PlanDigest,
	}))
	peer, err := newControlPlaneAppWithStore(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	type reservation struct {
		execution workspaceRecoveryExecution
		won       bool
		err       error
	}
	results := make(chan reservation, 2)
	for _, app := range []*controlPlaneServer{fixture.app, peer} {
		go func(candidate *controlPlaneServer) {
			execution, won, reserveErr := candidate.reserveWorkspaceRecoveryExecution(context.Background(), fixture.service, scenario.unknown.ID, validated.PlanID, validated.PlanDigest, "continue")
			results <- reservation{execution: execution, won: won, err: reserveErr}
		}(app)
	}
	winners := 0
	var executionID, runID string
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if executionID == "" {
			executionID, runID = result.execution.ExecutionID, result.execution.RunIdentity
		} else if result.execution.ExecutionID != executionID || result.execution.RunIdentity != runID {
			t.Fatalf("lease contenders received different execution identity: first=%s/%s second=%s/%s", executionID, runID, result.execution.ExecutionID, result.execution.RunIdentity)
		}
		if result.won {
			winners++
		}
	}
	if winners != 1 || executionID == "" || runID == "" {
		t.Fatalf("execution lease winners=%d execution=%s run=%s", winners, executionID, runID)
	}
	if scenario.readback.stageConvergeCalls != 0 || workspaceLaunchStageWriteCount(fixture, "runtime") != scenario.beforeCurrentWrites {
		t.Fatalf("lease reservation crossed recovery mutation boundary: converge=%d before=%d after=%d", scenario.readback.stageConvergeCalls, scenario.beforeCurrentWrites, workspaceLaunchStageWriteCount(fixture, "runtime"))
	}
}

func TestWorkspaceRecoveryPlanConcurrentConsoleExecuteDoesNotEnterProviderTwice(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")
	claimed := computeClaimRecoveryProofForLaunch(operation, "target_owned")
	fixture.fabric.computeClaimResult = &claimed
	configureWorkspaceLaunchFulfillment(t, fixture)
	configureWorkspaceComputeClaimReadback(fixture, operation)
	diagnosed := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
	validated := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", map[string]any{
		"planId": diagnosed.PlanID, "planDigest": diagnosed.PlanDigest,
	}))
	peerServer, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	peerFixture := fixture
	peerFixture.server, peerFixture.operator = peerServer, reservedOperatorSessionForTest(t, peerServer)

	providerEntered := make(chan struct{}, 2)
	releaseProvider := make(chan struct{})
	fixture.fabric.beforeComputeClaim = func() {
		providerEntered <- struct{}{}
		<-releaseProvider
	}
	executeBody := map[string]any{
		"planId": validated.PlanID, "planDigest": validated.PlanDigest, "decision": "continue", "confirmation": "CONTINUE_RECOVERY_PLAN",
	}
	firstResult := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstResult <- requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/execute", executeBody)
	}()
	select {
	case <-providerEntered:
	case <-time.After(2 * time.Second):
		close(releaseProvider)
		t.Fatal("first execution did not enter compute claim provider")
	}
	secondResult := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		secondResult <- requestWorkspaceRecoveryPlan(t, peerFixture, http.MethodPost, "/execute", executeBody)
	}()
	var second *httptest.ResponseRecorder
	select {
	case <-providerEntered:
		close(releaseProvider)
		<-firstResult
		<-secondResult
		t.Fatal("lease loser entered compute claim provider")
	case second = <-secondResult:
	case <-time.After(2 * time.Second):
		close(releaseProvider)
		<-firstResult
		<-secondResult
		t.Fatal("lease loser did not return persisted execution")
	}
	if second.Code != http.StatusOK {
		close(releaseProvider)
		<-firstResult
		t.Fatalf("lease loser status=%d body=%s", second.Code, second.Body.String())
	}
	loserPlan := recoveryPlanResponse(t, second)
	if loserPlan.Status != "executing" || loserPlan.ExecutionID == "" || loserPlan.RunID == "" {
		close(releaseProvider)
		<-firstResult
		t.Fatalf("lease loser projection=%#v", loserPlan)
	}
	close(releaseProvider)
	first := <-firstResult
	if first.Code != http.StatusOK || len(fixture.fabric.computeClaimCalls) != 1 {
		t.Fatalf("winner status=%d body=%s claims=%d", first.Code, first.Body.String(), len(fixture.fabric.computeClaimCalls))
	}
}

func TestWorkspaceRecoveryPlanExpiredLeaseReconcilesSameExecutionAfterRestart(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")
	claimed := computeClaimRecoveryProofForLaunch(operation, "target_owned")
	fixture.fabric.computeClaimResult = &claimed
	configureWorkspaceLaunchFulfillment(t, fixture)
	configureWorkspaceComputeClaimReadback(fixture, operation)
	diagnosed := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
	validated := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", map[string]any{
		"planId": diagnosed.PlanID, "planDigest": diagnosed.PlanDigest,
	}))
	execution, won, err := fixture.app.reserveWorkspaceRecoveryExecution(context.Background(), fixture.service, operation.ID, validated.PlanID, validated.PlanDigest, "continue")
	if err != nil || !won {
		t.Fatalf("initial execution reservation won=%v err=%v execution=%#v", won, err, execution)
	}
	persisted := fixture.operation(t)
	persisted.RecoveryExecution.LeaseExpiresAt = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(persisted)))
	restartedServer, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	restarted := fixture
	restarted.server, restarted.operator = restartedServer, reservedOperatorSessionForTest(t, restartedServer)
	response := requestWorkspaceRecoveryPlan(t, restarted, http.MethodPost, "/execute", map[string]any{
		"planId": validated.PlanID, "planDigest": validated.PlanDigest, "decision": "continue", "confirmation": "CONTINUE_RECOVERY_PLAN",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("restart reconcile status=%d body=%s", response.Code, response.Body.String())
	}
	completed := recoveryPlanResponse(t, response)
	if completed.Status != "completed" || completed.ExecutionID != execution.ExecutionID || completed.RunID != execution.RunIdentity || completed.URL == "" || completed.ReceiptID == "" ||
		len(fixture.fabric.computeClaimCalls) != 1 || len(fixture.fabric.storageIDs) != 1 || len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeIDs) != 1 {
		t.Fatalf("restart reconcile did not reuse exact execution: plan=%#v claims=%d storage=%d charges=%d computes=%d", completed, len(fixture.fabric.computeClaimCalls), len(fixture.fabric.storageIDs), len(fixture.sub2API.charges), len(fixture.fabric.computeIDs))
	}
}

func TestWorkspaceRecoveryPlanExpiredLeaseRejectsStaleHolderFinalize(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "runtime", "basic")
	fixture := scenario.fixture
	fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
	server, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)
	diagnosed := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": scenario.unknown.AccountID}))
	validated := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", map[string]any{
		"planId": diagnosed.PlanID, "planDigest": diagnosed.PlanDigest,
	}))
	stale, won, err := fixture.app.reserveWorkspaceRecoveryExecution(context.Background(), fixture.service, scenario.unknown.ID, validated.PlanID, validated.PlanDigest, "continue")
	if err != nil || !won || stale.LeaseToken == "" {
		t.Fatalf("initial reservation won=%v err=%v execution=%#v", won, err, stale)
	}
	persisted := fixture.operation(t)
	persisted.RecoveryExecution.LeaseExpiresAt = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(persisted)))
	fresh, won, err := fixture.app.reacquireWorkspaceRecoveryExecution(context.Background(), scenario.unknown.ID, validated.PlanID, validated.PlanDigest, "continue")
	if err != nil || !won || fresh.LeaseToken == "" || fresh.LeaseToken == stale.LeaseToken {
		t.Fatalf("lease takeover won=%v err=%v stale=%#v fresh=%#v", won, err, stale, fresh)
	}
	if _, err := fixture.app.finalizeWorkspaceRecoveryExecution(context.Background(), scenario.unknown.ID, stale.ExecutionID, stale.LeaseToken, workspaceRecoveryMutationOutcome{Status: "unknown"}, nil); !errors.Is(err, errBillingReviewIdentity) {
		t.Fatalf("stale holder finalize err=%v", err)
	}
	current := fixture.operation(t)
	if current.RecoveryExecution == nil || current.RecoveryExecution.LeaseToken != fresh.LeaseToken || current.RecoveryExecution.Status != "running" {
		t.Fatalf("stale holder changed current lease: %#v", current.RecoveryExecution)
	}
	if scenario.readback.stageConvergeCalls != 0 || workspaceLaunchStageWriteCount(fixture, "runtime") != scenario.beforeCurrentWrites {
		t.Fatalf("lease fencing crossed recovery mutation boundary: converge=%d", scenario.readback.stageConvergeCalls)
	}
}

func TestWorkspaceRecoveryPlanExecuteRejectsBlockedPlanAndKeepsManualReview(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "runtime", "basic")
	fixture := scenario.fixture
	fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
	server, err := NewPersistentServer(fixture.service, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)
	diagnosed := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": scenario.unknown.AccountID}))
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("c", 40))
	blocked := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", map[string]any{
		"planId": diagnosed.PlanID, "planDigest": diagnosed.PlanDigest,
	}))
	response := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/execute", map[string]any{
		"planId": blocked.PlanID, "planDigest": blocked.PlanDigest, "decision": "continue", "confirmation": "CONTINUE_RECOVERY_PLAN",
	})
	if response.Code != http.StatusConflict {
		t.Fatalf("blocked execute status=%d body=%s", response.Code, response.Body.String())
	}
	persisted, ok, err := fixture.app.workspaceLaunchOperation(context.Background(), scenario.unknown.ID)
	if err != nil || !ok || persisted.Status != "manual_review" || persisted.RecoveryExecution != nil {
		t.Fatalf("blocked execution changed launch operation=%#v ok=%v err=%v", persisted, ok, err)
	}
	if scenario.readback.stageConvergeCalls != 0 || workspaceLaunchStageWriteCount(fixture, "runtime") != scenario.beforeCurrentWrites {
		t.Fatalf("blocked execution mutated provider state: converge=%d before=%d after=%d", scenario.readback.stageConvergeCalls, scenario.beforeCurrentWrites, workspaceLaunchStageWriteCount(fixture, "runtime"))
	}
}

func TestWorkspaceRecoveryPlanDiagnoseAndValidateComputeClaimFromServerAuthority(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")

	diagnoseResponse := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID})
	if diagnoseResponse.Code != http.StatusOK {
		t.Fatalf("compute claim diagnose status=%d body=%s", diagnoseResponse.Code, diagnoseResponse.Body.String())
	}
	diagnosed := recoveryPlanResponse(t, diagnoseResponse)
	persistedPlan := fixture.operation(t).RecoveryPlan
	if diagnosed.Status != "diagnosed" || persistedPlan == nil || persistedPlan.Action != "compute_claim_continue" || persistedPlan.TargetBinding.Stage != "compute_claim" ||
		persistedPlan.TargetBinding.CVMInstanceID != operation.ComputeCVMInstanceID || persistedPlan.TargetBinding.NodeName != operation.ComputeNodeName ||
		persistedPlan.TargetBinding.WorkspaceAPIKeyID != operation.WorkspaceAPIKeyID || persistedPlan.MutationCounts != (workspaceRecoveryMutationCounts{}) {
		t.Fatalf("compute claim diagnosed plan=%#v", diagnosed)
	}
	validateResponse := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", map[string]any{
		"planId": diagnosed.PlanID, "planDigest": diagnosed.PlanDigest,
	})
	if validateResponse.Code != http.StatusOK {
		t.Fatalf("compute claim validate status=%d body=%s", validateResponse.Code, validateResponse.Body.String())
	}
	validated := recoveryPlanResponse(t, validateResponse)
	validatedOperation := fixture.operation(t)
	if validated.Status != "validated" || len(validated.Mismatches) != 0 || validatedOperation.RecoveryPlan == nil || validatedOperation.RecoveryPlan.ValidatedAt == "" {
		t.Fatalf("compute claim validated plan=%#v", validated)
	}
	persisted := fixture.operation(t)
	if persisted.Status != "compute_claim_pending" || persisted.Phase != "compute_claim_pending" || persisted.ComputeClaimApproval != nil ||
		len(fixture.fabric.computeClaimInputs) != 2 || len(fixture.fabric.computeClaimCalls) != 0 || len(fixture.fabric.storageIDs) != 0 {
		t.Fatalf("compute claim plan crossed zero-mutation boundary: operation=%#v proofs=%#v claims=%#v storage=%#v", persisted, fixture.fabric.computeClaimInputs, fixture.fabric.computeClaimCalls, fixture.fabric.storageIDs)
	}
}

func TestWorkspaceRecoveryPlanValidateReportsComputeIdentityConflictAndKeepsManualReview(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	operation.Status, operation.ErrorCode = "manual_review", "workspace_compute_claim_identity_mismatch"
	mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(operation)))
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")

	diagnosed := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
	driftedCVM := "ins-conflicting-authority"
	fixture.fabric.computeClaimProof.CVMInstanceID = driftedCVM
	response := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", map[string]any{
		"planId": diagnosed.PlanID, "planDigest": diagnosed.PlanDigest,
	})
	if response.Code != http.StatusOK {
		t.Fatalf("identity conflict validate status=%d body=%s", response.Code, response.Body.String())
	}
	blocked := recoveryPlanResponse(t, response)
	if blocked.Status != "blocked" || blocked.ErrorCode != "identity_mismatch" || len(blocked.Mismatches) == 0 {
		t.Fatalf("identity conflict did not produce blocked plan: %#v", blocked)
	}
	found := false
	wantDigests := workspaceComputeClaimIdentityDigestCheck("tencent.cvmInstanceId", operation.ComputeCVMInstanceID, driftedCVM)
	for _, mismatch := range blocked.Mismatches {
		if mismatch.Field == "tencent.cvmInstanceId" && mismatch.Expected == "" && mismatch.Actual == "" &&
			mismatch.ExpectedDigest == wantDigests.ExpectedDigest && mismatch.ActualDigest == wantDigests.ActualDigest {
			found = true
		}
	}
	if !found {
		t.Fatalf("identity conflict omitted exact CVM expected/actual: %#v", blocked.Mismatches)
	}
	persisted := fixture.operation(t)
	if persisted.Status != "manual_review" || persisted.Phase != "compute_claim_pending" || persisted.RecoveryExecution != nil ||
		len(fixture.fabric.computeClaimCalls) != 0 || len(fixture.fabric.storageIDs) != 0 {
		t.Fatalf("identity conflict crossed fail-closed boundary: operation=%#v claims=%#v storage=%#v", persisted, fixture.fabric.computeClaimCalls, fixture.fabric.storageIDs)
	}
	internalExact := false
	for _, mismatch := range persisted.RecoveryPlan.Mismatches {
		internalExact = internalExact || mismatch.Field == "tencent.cvmInstanceId" && mismatch.Expected == operation.ComputeCVMInstanceID && mismatch.Actual == driftedCVM
	}
	if !internalExact {
		t.Fatalf("persisted authority omitted exact CVM expected/actual: %#v", persisted.RecoveryPlan.Mismatches)
	}
}

func TestWorkspaceRecoveryPlanDiagnoseCreatesSuccessorForFailedZeroMutationExecution(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")

	first := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
	failed := fixture.operation(t)
	failed.Status, failed.ErrorCode = "manual_review", "workspace_compute_claim_identity_mismatch"
	failed.RecoveryPlan.Status, failed.RecoveryPlan.ErrorCode = "failed", "workspace_compute_claim_identity_mismatch"
	failed.RecoveryExecution = &workspaceRecoveryExecution{
		ExecutionID: "recovery-exec-failed-zero", RunIdentity: "control-plane-run-failed-zero",
		PlanID: first.PlanID, PlanDigest: first.PlanDigest, ApprovalDigest: strings.Repeat("c", 64), Decision: "continue",
		Status: "failed", StartedAt: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano),
		CompletedAt: time.Now().UTC().Format(time.RFC3339Nano), ErrorCode: failed.ErrorCode,
	}
	mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(failed)))

	successorResponse := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID})
	if successorResponse.Code != http.StatusOK {
		t.Fatalf("successor diagnose status=%d body=%s", successorResponse.Code, successorResponse.Body.String())
	}
	successor := recoveryPlanResponse(t, successorResponse)
	persisted := fixture.operation(t)
	if successor.Status != "diagnosed" || successor.PlanID == first.PlanID || successor.PlanDigest == first.PlanDigest ||
		persisted.RecoveryExecution != nil || len(persisted.RecoveryHistory) != 1 {
		t.Fatalf("failed zero execution did not create successor: first=%#v successor=%#v operation=%#v", first, successor, persisted)
	}
	history := persisted.RecoveryHistory[0]
	if history.Plan.PlanID != first.PlanID || history.Plan.PlanDigest != first.PlanDigest || history.Plan.Status != "failed" ||
		history.Execution.ExecutionID != "recovery-exec-failed-zero" || history.Execution.ErrorCode != failed.ErrorCode ||
		history.Execution.MutationOutcome.Status != "confirmed_zero" || history.Execution.MutationOutcome.Counts != (workspaceRecoveryMutationCounts{}) ||
		persisted.RecoveryPlan.Generation != 1 || persisted.RecoveryPlan.PredecessorPlanDigest != first.PlanDigest ||
		persisted.RecoveryPlan.PredecessorExecutionID != history.Execution.ExecutionID {
		t.Fatalf("successor evidence not preserved: history=%#v current=%#v", history, persisted.RecoveryPlan)
	}
	if len(fixture.fabric.computeClaimCalls) != 0 || len(fixture.fabric.storageIDs) != 0 || len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeIDs) != 1 {
		t.Fatalf("successor diagnose crossed zero-mutation boundary: claims=%d storage=%d charges=%d computes=%d", len(fixture.fabric.computeClaimCalls), len(fixture.fabric.storageIDs), len(fixture.sub2API.charges), len(fixture.fabric.computeIDs))
	}
	replayed := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
	replayedOperation := fixture.operation(t)
	if replayed.PlanID != successor.PlanID || replayed.PlanDigest != successor.PlanDigest || len(replayedOperation.RecoveryHistory) != 1 || replayedOperation.RecoveryExecution != nil {
		t.Fatalf("successor diagnose replay drifted identity: successor=%#v replay=%#v operation=%#v", successor, replayed, replayedOperation)
	}
}

func TestWorkspaceRecoveryPlanDiagnoseCreatesSuccessorForAuthoritativeObservedZeroLedger(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")
	useWorkspaceRecoveryPlanIdentityEvidence(t, &fixture, &clients.ComputeClaimIdentityEvidence{
		Checks: []clients.ComputeClaimIdentityCheck{{
			Field: "binding.compatibility", Matches: true, Expected: "current_or_historical", Actual: "historical",
		}},
		MutationLedger: "observed", MutationLedgerOutcome: "confirmed_zero", MutationLedgerDigest: strings.Repeat("d", 64),
	})

	first := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
	failed := fixture.operation(t)
	failed.Status, failed.ErrorCode = "manual_review", "workspace_compute_claim_identity_mismatch"
	failed.RecoveryPlan.Status, failed.RecoveryPlan.ErrorCode = "failed", failed.ErrorCode
	failed.RecoveryExecution = &workspaceRecoveryExecution{
		ExecutionID: "recovery-exec-failed-authoritative-zero", RunIdentity: "control-plane-run-failed-authoritative-zero",
		PlanID: first.PlanID, PlanDigest: first.PlanDigest, ApprovalDigest: strings.Repeat("c", 64), Decision: "continue",
		Status: "failed", StartedAt: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano),
		CompletedAt: time.Now().UTC().Format(time.RFC3339Nano), ErrorCode: failed.ErrorCode,
	}
	mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(failed)))

	successor := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
	persisted := fixture.operation(t)
	if successor.Status != "diagnosed" || successor.PlanID == first.PlanID || successor.PlanDigest == first.PlanDigest ||
		persisted.RecoveryExecution != nil || len(persisted.RecoveryHistory) != 1 ||
		persisted.RecoveryHistory[0].Execution.MutationOutcome.Status != "confirmed_zero" ||
		persisted.RecoveryHistory[0].Execution.MutationOutcome.Source != "fabric_mutation_ledger_confirmed_zero" ||
		persisted.RecoveryHistory[0].Execution.MutationOutcome.EvidenceDigest != strings.Repeat("d", 64) {
		t.Fatalf("authoritative zero ledger did not create successor: first=%#v successor=%#v operation=%#v", first, successor, persisted)
	}
	if len(fixture.fabric.computeClaimCalls) != 0 || len(fixture.fabric.storageIDs) != 0 || len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeIDs) != 1 {
		t.Fatalf("authoritative successor crossed zero-mutation boundary: claims=%d storage=%d charges=%d computes=%d", len(fixture.fabric.computeClaimCalls), len(fixture.fabric.storageIDs), len(fixture.sub2API.charges), len(fixture.fabric.computeIDs))
	}
}

func TestWorkspaceRecoveryPlanSuccessorRejectsUnconfirmedFabricLedgerEvidence(t *testing.T) {
	planID, planDigest := "recovery-plan-failed", strings.Repeat("a", 64)
	operation := workspaceLaunchOperation{
		RecoveryPlan: &workspaceRecoveryPlan{
			PlanID: planID, PlanDigest: planDigest, Status: "failed", Action: "compute_claim_continue",
		},
		RecoveryExecution: &workspaceRecoveryExecution{
			ExecutionID: "recovery-exec-failed", PlanID: planID, PlanDigest: planDigest, Status: "failed",
			CompletedAt: time.Now().UTC().Format(time.RFC3339Nano),
		},
	}
	tests := map[string]clients.ComputeClaimIdentityEvidence{
		"nonzero": {
			MutationLedger: "observed", MutationLedgerOutcome: "nonzero", MutationLedgerDigest: strings.Repeat("b", 64),
		},
		"unknown": {
			MutationLedger: "observed", MutationLedgerOutcome: "unknown", MutationLedgerDigest: strings.Repeat("b", 64),
		},
		"missing_digest": {
			MutationLedger: "observed", MutationLedgerOutcome: "confirmed_zero",
		},
		"invalid_digest": {
			MutationLedger: "observed", MutationLedgerOutcome: "confirmed_zero", MutationLedgerDigest: "not-a-digest",
		},
		"contradictory_absent": {
			MutationLedger: "absent", MutationLedgerOutcome: "nonzero", MutationLedgerDigest: strings.Repeat("b", 64),
		},
	}
	for name, evidence := range tests {
		t.Run(name, func(t *testing.T) {
			if outcome, ok := workspaceRecoveryExecutionConfirmedZero(operation, &evidence); ok {
				t.Fatalf("unconfirmed evidence accepted: outcome=%#v evidence=%#v", outcome, evidence)
			}
		})
	}
}

func TestWorkspaceRecoveryPlanDiagnoseKeepsFailedExecutionWithNonzeroMutationEvidence(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")
	first := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
	failed := fixture.operation(t)
	failed.Status, failed.ErrorCode = "manual_review", "workspace_compute_claim_identity_mismatch"
	failed.RecoveryPlan.Status, failed.RecoveryPlan.ErrorCode = "failed", failed.ErrorCode
	failed.RecoveryExecution = &workspaceRecoveryExecution{
		ExecutionID: "recovery-exec-failed-nonzero", RunIdentity: "control-plane-run-failed-nonzero",
		PlanID: first.PlanID, PlanDigest: first.PlanDigest, ApprovalDigest: strings.Repeat("c", 64), Decision: "continue",
		Status: "failed", StartedAt: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano),
		CompletedAt: time.Now().UTC().Format(time.RFC3339Nano), ErrorCode: failed.ErrorCode,
		MutationOutcome: workspaceRecoveryMutationOutcome{
			Status: "nonzero", Counts: workspaceRecoveryMutationCounts{Kubernetes: 1}, Source: "compute_claim_response",
		},
	}
	mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(failed)))

	replayed := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
	persisted := fixture.operation(t)
	if replayed.PlanID != first.PlanID || replayed.PlanDigest != first.PlanDigest || replayed.Status != "failed" ||
		persisted.RecoveryExecution == nil || persisted.RecoveryExecution.ExecutionID != failed.RecoveryExecution.ExecutionID || len(persisted.RecoveryHistory) != 0 ||
		len(fixture.fabric.computeClaimCalls) != 0 || len(fixture.fabric.storageIDs) != 0 {
		t.Fatalf("nonzero failed execution was replaced or repeated: replay=%#v operation=%#v", replayed, persisted)
	}
}

func TestWorkspaceRecoveryPlanDiagnosePersistsFieldMismatch(t *testing.T) {
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	proof := computeClaimRecoveryProofForLaunch(operation, "unallocated")
	input := workspaceComputeClaimRecoveryRequestForOperation(operation)
	evidence := &clients.ComputeClaimIdentityEvidence{
		Checks: []clients.ComputeClaimIdentityCheck{{
			Field: "binding.operationId", Matches: false, Expected: operation.ID + ":compute", Actual: "op-conflict",
		}},
		MutationLedger: "absent",
	}
	plan, err := newWorkspaceComputeClaimRecoveryPlan(operation, input, proof, evidence, workspaceRecoveryReleaseBinding{
		MainSHA: strings.Repeat("a", 40), CloudImageDigest: "sha256:" + strings.Repeat("b", 64), WorkspaceImageDigest: deployedImageDigest(operation.WorkspaceImageDigest),
	})
	if err != nil || plan.Status != "blocked" || len(plan.Mismatches) != 1 || plan.Mismatches[0].Field != "binding.operationId" ||
		plan.Mismatches[0].Expected != operation.ID+":compute" || plan.Mismatches[0].Actual != "op-conflict" || len(fixture.fabric.computeClaimCalls) != 0 {
		t.Fatalf("diagnose mismatch plan=%#v err=%v", plan, err)
	}
}

func TestWorkspaceRecoveryPlanExecuteComputeClaimContinuesOriginalLaunchOnce(t *testing.T) {
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")
	claimed := computeClaimRecoveryProofForLaunch(operation, "target_owned")
	fixture.fabric.computeClaimResult = &claimed
	configureWorkspaceLaunchFulfillment(t, fixture)
	configureWorkspaceComputeClaimReadback(fixture, operation)

	diagnosed := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/diagnose", map[string]any{"accountId": operation.AccountID}))
	validated := recoveryPlanResponse(t, requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/validate", map[string]any{
		"planId": diagnosed.PlanID, "planDigest": diagnosed.PlanDigest,
	}))
	executeBody := map[string]any{
		"planId": validated.PlanID, "planDigest": validated.PlanDigest, "decision": "continue", "confirmation": "CONTINUE_RECOVERY_PLAN",
	}
	firstResponse := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/execute", executeBody)
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("compute claim execute status=%d body=%s", firstResponse.Code, firstResponse.Body.String())
	}
	first := recoveryPlanResponse(t, firstResponse)
	if first.Status != "completed" || first.ExecutionID == "" || first.RunID == "" || first.URL == "" || first.ReceiptID == "" {
		t.Fatalf("compute claim executed plan=%#v", first)
	}
	claimCalls, storageCreates := len(fixture.fabric.computeClaimCalls), len(fixture.fabric.storageIDs)
	secondResponse := requestWorkspaceRecoveryPlan(t, fixture, http.MethodPost, "/execute", executeBody)
	if secondResponse.Code != http.StatusOK {
		t.Fatalf("compute claim replay status=%d body=%s", secondResponse.Code, secondResponse.Body.String())
	}
	second := recoveryPlanResponse(t, secondResponse)
	if second.ExecutionID != first.ExecutionID || second.RunID != first.RunID || len(fixture.fabric.computeClaimCalls) != claimCalls || len(fixture.fabric.storageIDs) != storageCreates ||
		claimCalls != 1 || storageCreates != 1 || len(fixture.sub2API.charges) != 1 || len(fixture.fabric.computeIDs) != 1 {
		t.Fatalf("compute claim replay repeated mutation: first=%#v second=%#v claims=%d storage=%d charges=%d computes=%d", first, second, len(fixture.fabric.computeClaimCalls), len(fixture.fabric.storageIDs), len(fixture.sub2API.charges), len(fixture.fabric.computeIDs))
	}
	persisted := fixture.operation(t)
	if persisted.Status != "succeeded" || persisted.Phase != "succeeded" || persisted.ComputeClaimApproval == nil || persisted.ComputeClaimProof == nil ||
		persisted.RecoveryExecution == nil || persisted.RecoveryExecution.ExecutionID != first.ExecutionID {
		t.Fatalf("compute claim execution not persisted on original launch: %#v", persisted)
	}
}
