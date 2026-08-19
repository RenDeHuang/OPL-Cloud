package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/controlplane"
)

func seedWorkspaceGatewayBudget(t *testing.T, store controlPlaneTableStore, workspaceID, accountID string, keyID any) {
	t.Helper()
	row := map[string]any{
		"id": workspaceID, "accountId": accountID, "ownerAccountId": accountID,
		"ownerUserId": "usr-gateway-owner", "state": "running", "status": "running",
	}
	if keyID != nil {
		row["workspaceApiKeyId"] = keyID
	}
	mustStore(t, store.SaveWorkspace(context.Background(), row))
}

func TestWorkspaceGatewayBudgetGETUsesPersistedBindingAndLiveSub2APITruth(t *testing.T) {
	server, client, store, session := newGatewayKeyCommandFixture(t)
	updatedAt := time.Date(2026, 8, 19, 1, 2, 3, 0, time.UTC)
	key := client.keys[9]
	key.QuotaUSDMicros = 12_000_000
	key.QuotaUsedUSDMicros = 3_000_000
	key.RateLimit5hUSDMicros = 500_000
	key.RateLimit1dUSDMicros = 1_000_000
	key.RateLimit7dUSDMicros = 4_000_000
	key.Usage5hUSDMicros = 100_000
	key.Usage1dUSDMicros = 200_000
	key.Usage7dUSDMicros = 300_000
	key.UpdatedAt = updatedAt
	client.keys[9] = key
	seedWorkspaceGatewayBudget(t, store, "workspace-budget", "acct-gateway", int64(9))

	response := requestWithSession(t, server, session, http.MethodGet, "/api/workspaces/workspace-budget/gateway-budget", "")
	if response.Code != http.StatusOK {
		t.Fatalf("GET gateway budget status = %d: %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "workspace-key-secret") || strings.Contains(response.Body.String(), "opl-workspace") {
		t.Fatalf("GET gateway budget leaked key secret or name: %s", response.Body.String())
	}
	envelope := decodeSourceEnvelope(t, response)
	data := mapField(envelope, "data")
	if len(data) != 13 || data["workspaceId"] != "workspace-budget" || data["keyId"] != "9" ||
		data["quotaUsdMicros"] != "12000000" || data["quotaUsedUsdMicros"] != "3000000" ||
		data["rateLimit5hUsdMicros"] != "500000" || data["rateLimit1dUsdMicros"] != "1000000" ||
		data["rateLimit7dUsdMicros"] != "4000000" || data["usage5hUsdMicros"] != "100000" ||
		data["usage1dUsdMicros"] != "200000" || data["usage7dUsdMicros"] != "300000" ||
		data["enabled"] != true || data["status"] != "active" || data["updatedAt"] != updatedAt.Format(time.RFC3339Nano) {
		t.Fatalf("GET gateway budget data = %#v", data)
	}
}

func TestWorkspaceGatewayBudgetGETRejectsUnownedOrInvalidBinding(t *testing.T) {
	t.Run("cross account is not found", func(t *testing.T) {
		server, client, store, _ := newGatewayKeyCommandFixture(t)
		seedWorkspaceGatewayBudget(t, store, "workspace-budget", "acct-gateway", int64(9))
		seedTenantMember(t, store, "acct-other", "org-other", "usr-other", "beta-other@example.com")
		other := loginForTest(t, server, "beta-other@example.com", "CorrectHorseBatteryStaple!")

		response := requestWithSession(t, server, other, http.MethodGet, "/api/workspaces/workspace-budget/gateway-budget", "")
		assertErrorResponse(t, response.Code, response.Body.String(), http.StatusNotFound, "workspace_not_found")
		if len(client.userKeyReadIDs) != 0 {
			t.Fatalf("cross-account GET reached Sub2API: %#v", client.userKeyReadIDs)
		}
	})

	t.Run("missing binding", func(t *testing.T) {
		server, client, store, session := newGatewayKeyCommandFixture(t)
		seedWorkspaceGatewayBudget(t, store, "workspace-budget", "acct-gateway", nil)

		response := requestWithSession(t, server, session, http.MethodGet, "/api/workspaces/workspace-budget/gateway-budget", "")
		assertErrorResponse(t, response.Code, response.Body.String(), http.StatusConflict, "workspace_gateway_key_not_provisioned")
		if len(client.userKeyReadIDs) != 0 {
			t.Fatalf("missing binding reached Sub2API: %#v", client.userKeyReadIDs)
		}
	})

	for _, test := range []struct {
		name  string
		keyID int64
		key   int64
	}{
		{name: "bound key missing", keyID: 99},
		{name: "readback id mismatched", keyID: 9, key: 10},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, client, store, session := newGatewayKeyCommandFixture(t)
			seedWorkspaceGatewayBudget(t, store, "workspace-budget", "acct-gateway", test.keyID)
			if test.key != 0 {
				key := client.keys[test.keyID]
				key.ID = test.key
				client.keys[test.keyID] = key
			}

			response := requestWithSession(t, server, session, http.MethodGet, "/api/workspaces/workspace-budget/gateway-budget", "")
			assertErrorResponse(t, response.Code, response.Body.String(), http.StatusConflict, "workspace_gateway_key_binding_invalid")
		})
	}

	t.Run("source unavailable", func(t *testing.T) {
		server, client, store, session := newGatewayKeyCommandFixture(t)
		seedWorkspaceGatewayBudget(t, store, "workspace-budget", "acct-gateway", int64(9))
		client.userKeyErr = errors.New("sub2api unavailable")

		response := requestWithSession(t, server, session, http.MethodGet, "/api/workspaces/workspace-budget/gateway-budget", "")
		if response.Code != http.StatusBadGateway {
			t.Fatalf("source unavailable status=%d body=%s", response.Code, response.Body.String())
		}
		var envelope map[string]any
		if json.NewDecoder(response.Body).Decode(&envelope) != nil || envelope["source"] != "sub2api" || envelope["status"] != "unavailable" || envelope["available"] != false {
			t.Fatalf("source unavailable envelope=%#v", envelope)
		}
	})
}

func TestWorkspaceGatewayBudgetPATCHUpdatesOnlyAllowedPolicyAndPersistsEvidence(t *testing.T) {
	server, client, store, session := newGatewayKeyCommandFixture(t)
	seedWorkspaceGatewayBudget(t, store, "workspace-budget", "acct-gateway", int64(9))
	key := client.keys[9]
	key.QuotaUsedUSDMicros = 7
	key.Usage5hUSDMicros, key.Usage1dUSDMicros, key.Usage7dUSDMicros = 1, 2, 3
	key.UpdatedAt = time.Date(2026, 8, 19, 2, 3, 4, 0, time.UTC)
	client.keys[9] = key
	body := `{"quotaUsdMicros":9000000,"rateLimit5hUsdMicros":500000,"rateLimit1dUsdMicros":1000000,"rateLimit7dUsdMicros":4000000,"enabled":false,"resetQuota":true,"resetRateLimitUsage":true}`

	response := requestWithMutationKeyForTest(t, server, session, http.MethodPatch, "/api/workspaces/workspace-budget/gateway-budget", body, "budget-all-fields")
	if response.Code != http.StatusOK {
		t.Fatalf("PATCH gateway budget status=%d body=%s", response.Code, response.Body.String())
	}
	if client.updateCalls != 1 || len(client.updateInputs) != 1 {
		t.Fatalf("PATCH update calls=%d inputs=%#v", client.updateCalls, client.updateInputs)
	}
	input := client.updateInputs[0]
	if input.Name != nil || input.GroupID != nil || input.IPWhitelist != nil || input.IPBlacklist != nil || input.ExpiresAt != nil ||
		input.QuotaUSDMicros == nil || *input.QuotaUSDMicros != 9_000_000 || input.RateLimit5hUSDMicros == nil || *input.RateLimit5hUSDMicros != 500_000 ||
		input.RateLimit1dUSDMicros == nil || *input.RateLimit1dUSDMicros != 1_000_000 || input.RateLimit7dUSDMicros == nil || *input.RateLimit7dUSDMicros != 4_000_000 ||
		input.Enabled == nil || *input.Enabled || input.ResetQuota == nil || !*input.ResetQuota || input.ResetRateLimitUsage == nil || !*input.ResetRateLimitUsage {
		t.Fatalf("PATCH Sub2API input=%#v", input)
	}
	data := mapField(decodeSourceEnvelope(t, response), "data")
	if data["workspaceId"] != "workspace-budget" || data["keyId"] != "9" || data["enabled"] != false || data["status"] != "disabled" ||
		data["quotaUsdMicros"] != "9000000" || data["quotaUsedUsdMicros"] != "0" || data["usage5hUsdMicros"] != "0" || data["usage1dUsdMicros"] != "0" || data["usage7dUsdMicros"] != "0" {
		t.Fatalf("PATCH response data=%#v", data)
	}
	operations, err := queryRuntimeOperations(context.Background(), store, runtimeOperationQuery{WorkspaceID: "workspace-budget", Action: workspaceGatewayBudgetAction})
	if err != nil || len(operations) != 1 || stringValue(operations[0]["status"]) != "succeeded" || strings.Contains(stringValue(operations[0]["result"]), "workspace-key-secret") {
		t.Fatalf("budget operations=%#v err=%v", operations, err)
	}
	audits, err := store.ListAuditEvents(context.Background(), "acct-gateway")
	if err != nil || len(audits) != 1 || stringValue(audits[0]["action"]) != workspaceGatewayBudgetAction || strings.Contains(string(mustJSON(audits)), "workspace-key-secret") {
		t.Fatalf("budget audits=%#v err=%v", audits, err)
	}
}

func TestWorkspaceGatewayBudgetPATCHRejectsInvalidRequestsBeforeSub2API(t *testing.T) {
	tests := map[string]string{
		"empty":           `{}`,
		"unknown":         `{"name":"forbidden"}`,
		"negative":        `{"quotaUsdMicros":-1}`,
		"fractional":      `{"quotaUsdMicros":1.5}`,
		"overflow":        `{"quotaUsdMicros":9223372036854775808}`,
		"null amount":     `{"quotaUsdMicros":null,"enabled":false}`,
		"null boolean":    `{"enabled":null}`,
		"duplicate field": `{"quotaUsdMicros":100,"quotaUsdMicros":200}`,
		"multiple values": `{"enabled":true} {"enabled":false}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			server, client, store, session := newGatewayKeyCommandFixture(t)
			seedWorkspaceGatewayBudget(t, store, "workspace-budget", "acct-gateway", int64(9))
			response := requestWithMutationKeyForTest(t, server, session, http.MethodPatch, "/api/workspaces/workspace-budget/gateway-budget", body, "invalid-"+name)
			assertErrorResponse(t, response.Code, response.Body.String(), http.StatusBadRequest, "invalid_workspace_gateway_budget_request")
			if client.updateCalls != 0 {
				t.Fatalf("invalid PATCH reached Sub2API: %d", client.updateCalls)
			}
		})
	}
}

func TestWorkspaceGatewayBudgetPATCHRequiresCSRFAndIdempotencyKey(t *testing.T) {
	server, client, store, session := newGatewayKeyCommandFixture(t)
	seedWorkspaceGatewayBudget(t, store, "workspace-budget", "acct-gateway", int64(9))
	path := "/api/workspaces/workspace-budget/gateway-budget"

	missingKey := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(`{"enabled":false}`))
	missingKey.Header.Set("Content-Type", "application/json")
	addAuth(missingKey, session)
	missingKeyResponse := httptest.NewRecorder()
	server.ServeHTTP(missingKeyResponse, missingKey)
	assertErrorResponse(t, missingKeyResponse.Code, missingKeyResponse.Body.String(), http.StatusBadRequest, "missing Idempotency-Key")

	missingCSRF := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(`{"enabled":false}`))
	missingCSRF.Header.Set("Content-Type", "application/json")
	missingCSRF.Header.Set("Idempotency-Key", "budget-missing-csrf")
	addSessionCookies(missingCSRF, session)
	missingCSRFResponse := httptest.NewRecorder()
	server.ServeHTTP(missingCSRFResponse, missingCSRF)
	assertErrorResponse(t, missingCSRFResponse.Code, missingCSRFResponse.Body.String(), http.StatusForbidden, "csrf_token_invalid")

	if client.updateCalls != 0 || len(client.userKeyReadIDs) != 0 {
		t.Fatalf("unauthorized PATCH reached Sub2API: reads=%#v updates=%d", client.userKeyReadIDs, client.updateCalls)
	}
}

func TestWorkspaceGatewayBudgetPATCHIsIdempotentAcrossResponseLossAndRestart(t *testing.T) {
	server, client, store, session := newGatewayKeyCommandFixture(t)
	seedWorkspaceGatewayBudget(t, store, "workspace-budget", "acct-gateway", int64(9))
	client.updateErr = errors.New("response lost after write")
	body := `{"quotaUsdMicros":7000000}`

	first := requestWithMutationKeyForTest(t, server, session, http.MethodPatch, "/api/workspaces/workspace-budget/gateway-budget", body, "budget-response-loss")
	if first.Code != http.StatusOK || client.updateCalls != 1 {
		t.Fatalf("response-loss PATCH status=%d calls=%d body=%s", first.Code, client.updateCalls, first.Body.String())
	}
	client.updateErr = nil
	restarted, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, client), store)
	if err != nil {
		t.Fatal(err)
	}
	restartedSession := loginForTest(t, restarted, "gateway-owner@example.com", "CorrectHorseBatteryStaple!")
	replay := requestWithMutationKeyForTest(t, restarted, restartedSession, http.MethodPatch, "/api/workspaces/workspace-budget/gateway-budget", body, "budget-response-loss")
	if replay.Code != http.StatusOK || client.updateCalls != 1 {
		t.Fatalf("restarted replay status=%d calls=%d body=%s", replay.Code, client.updateCalls, replay.Body.String())
	}
	conflict := requestWithMutationKeyForTest(t, restarted, restartedSession, http.MethodPatch, "/api/workspaces/workspace-budget/gateway-budget", `{"quotaUsdMicros":8000000}`, "budget-response-loss")
	assertErrorResponse(t, conflict.Code, conflict.Body.String(), http.StatusConflict, "idempotency_conflict")
	if client.updateCalls != 1 {
		t.Fatalf("idempotency conflict repeated write: %d", client.updateCalls)
	}
}

func TestWorkspaceGatewayBudgetSucceededReplayReturnsCurrentTruthWithoutRewritingAudit(t *testing.T) {
	server, client, store, session := newGatewayKeyCommandFixture(t)
	seedWorkspaceGatewayBudget(t, store, "workspace-budget", "acct-gateway", int64(9))
	path := "/api/workspaces/workspace-budget/gateway-budget"
	firstBody := `{"quotaUsdMicros":7000000}`
	if response := requestWithMutationKeyForTest(t, server, session, http.MethodPatch, path, firstBody, "budget-first"); response.Code != http.StatusOK {
		t.Fatalf("first PATCH status=%d body=%s", response.Code, response.Body.String())
	}
	originalAudits, err := store.ListAuditEvents(context.Background(), "acct-gateway")
	if err != nil || len(originalAudits) != 1 {
		t.Fatalf("original audits=%#v err=%v", originalAudits, err)
	}

	replayRequest := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(firstBody))
	replayRequest.Header.Set("Content-Type", "application/json")
	replayRequest.Header.Set("Idempotency-Key", "budget-first")
	replayRequest.Header.Set("User-Agent", "different-replay-agent")
	replayRequest.RemoteAddr = "192.0.2.44:4321"
	addAuth(replayRequest, session)
	replayResponse := httptest.NewRecorder()
	server.ServeHTTP(replayResponse, replayRequest)
	if replayResponse.Code != http.StatusOK || client.updateCalls != 1 {
		t.Fatalf("same-state replay status=%d calls=%d body=%s", replayResponse.Code, client.updateCalls, replayResponse.Body.String())
	}
	afterReplayAudits, err := store.ListAuditEvents(context.Background(), "acct-gateway")
	if err != nil || !reflect.DeepEqual(afterReplayAudits, originalAudits) {
		t.Fatalf("replay rewrote audit: before=%#v after=%#v err=%v", originalAudits, afterReplayAudits, err)
	}

	if response := requestWithMutationKeyForTest(t, server, session, http.MethodPatch, path, `{"quotaUsdMicros":8000000}`, "budget-second"); response.Code != http.StatusOK {
		t.Fatalf("second PATCH status=%d body=%s", response.Code, response.Body.String())
	}
	oldReplay := requestWithMutationKeyForTest(t, server, session, http.MethodPatch, path, firstBody, "budget-first")
	if oldReplay.Code != http.StatusOK || mapField(decodeSourceEnvelope(t, oldReplay), "data")["quotaUsdMicros"] != "8000000" || client.updateCalls != 2 {
		t.Fatalf("old succeeded replay status=%d calls=%d body=%s", oldReplay.Code, client.updateCalls, oldReplay.Body.String())
	}
}

func TestWorkspaceGatewayBudgetResetUsesSuccessfulUpdateEvidenceBeforeNewUsage(t *testing.T) {
	server, client, store, session := newGatewayKeyCommandFixture(t)
	seedWorkspaceGatewayBudget(t, store, "workspace-budget", "acct-gateway", int64(9))
	key := client.keys[9]
	key.QuotaUsedUSDMicros = 9
	key.Usage5hUSDMicros, key.Usage1dUSDMicros, key.Usage7dUSDMicros = 8, 7, 6
	client.keys[9] = key
	client.postUpdateQuotaUsed = 4
	client.postUpdateUsage = [3]int64{3, 2, 1}

	response := requestWithMutationKeyForTest(t, server, session, http.MethodPatch, "/api/workspaces/workspace-budget/gateway-budget", `{"resetQuota":true,"resetRateLimitUsage":true}`, "budget-reset-live-usage")
	if response.Code != http.StatusOK || client.updateCalls != 1 {
		t.Fatalf("reset with new usage status=%d calls=%d body=%s", response.Code, client.updateCalls, response.Body.String())
	}
	data := mapField(decodeSourceEnvelope(t, response), "data")
	if data["quotaUsedUsdMicros"] != "4" || data["usage5hUsdMicros"] != "3" || data["usage1dUsdMicros"] != "2" || data["usage7dUsdMicros"] != "1" {
		t.Fatalf("reset did not return current live usage: %#v", data)
	}
}

func TestWorkspaceGatewayBudgetPATCHDoesNotRepeatUncertainWrite(t *testing.T) {
	server, client, store, session := newGatewayKeyCommandFixture(t)
	seedWorkspaceGatewayBudget(t, store, "workspace-budget", "acct-gateway", int64(9))
	client.updateErr = errors.New("write outcome unknown")
	client.updateFailsBeforeWrite = true
	body := `{"quotaUsdMicros":7000000}`

	first := requestWithMutationKeyForTest(t, server, session, http.MethodPatch, "/api/workspaces/workspace-budget/gateway-budget", body, "budget-uncertain")
	if first.Code != http.StatusBadGateway || client.updateCalls != 1 {
		t.Fatalf("uncertain PATCH status=%d calls=%d body=%s", first.Code, client.updateCalls, first.Body.String())
	}
	client.updateErr = nil
	client.updateFailsBeforeWrite = false
	replay := requestWithMutationKeyForTest(t, server, session, http.MethodPatch, "/api/workspaces/workspace-budget/gateway-budget", body, "budget-uncertain")
	if replay.Code != http.StatusBadGateway || client.updateCalls != 1 {
		t.Fatalf("uncertain replay status=%d calls=%d body=%s", replay.Code, client.updateCalls, replay.Body.String())
	}
}

func TestWorkspaceGatewayBudgetPATCHSerializesWithDurableBudgetAndRotationOperations(t *testing.T) {
	tests := []struct {
		name      string
		operation map[string]any
		errorCode string
	}{
		{
			name: "budget update", errorCode: "workspace_gateway_budget_update_in_progress",
			operation: map[string]any{"id": "budget-existing", "operationId": "budget-existing", "accountId": "acct-gateway", "workspaceId": "workspace-budget", "resourceId": "workspace-budget", "resourceKind": "workspace_gateway_budget", "action": workspaceGatewayBudgetAction, "status": "started", "result": `{}`},
		},
		{
			name: "key rotation", errorCode: "workspace_key_rotation_in_progress",
			operation: map[string]any{"id": "rotation-existing", "operationId": "rotation-existing", "accountId": "acct-gateway", "workspaceId": "workspace-budget", "resourceId": "workspace-budget", "resourceKind": "workspace_gateway_key", "action": "workspace.gateway_key.rotate", "status": "started", "result": `{}`},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, client, store, session := newGatewayKeyCommandFixture(t)
			seedWorkspaceGatewayBudget(t, store, "workspace-budget", "acct-gateway", int64(9))
			mustStore(t, store.SaveRuntimeOperation(context.Background(), test.operation))
			response := requestWithMutationKeyForTest(t, server, session, http.MethodPatch, "/api/workspaces/workspace-budget/gateway-budget", `{"enabled":false}`, "budget-blocked")
			assertErrorResponse(t, response.Code, response.Body.String(), http.StatusConflict, test.errorCode)
			if client.updateCalls != 0 {
				t.Fatalf("blocked PATCH reached Sub2API: %d", client.updateCalls)
			}
		})
	}
}

func TestWorkspaceGatewayBudgetPATCHUsesExactPersistedBinding(t *testing.T) {
	server, client, store, session := newGatewayKeyCommandFixture(t)
	seedWorkspaceGatewayBudget(t, store, "workspace-budget", "acct-gateway", int64(9))
	key := client.keys[9]
	key.ID = 10
	client.keys[9] = key

	response := requestWithMutationKeyForTest(t, server, session, http.MethodPatch, "/api/workspaces/workspace-budget/gateway-budget", `{"enabled":false}`, "budget-binding")
	assertErrorResponse(t, response.Code, response.Body.String(), http.StatusConflict, "workspace_gateway_key_binding_invalid")
	if client.updateCalls != 0 || !reflect.DeepEqual(client.userKeyReadIDs, []int64{9}) {
		t.Fatalf("binding mismatch calls: reads=%#v updates=%d", client.userKeyReadIDs, client.updateCalls)
	}
}

func TestWorkspaceGatewayBudgetPATCHCrossAccountDoesNotRevealWorkspace(t *testing.T) {
	server, client, store, _ := newGatewayKeyCommandFixture(t)
	seedWorkspaceGatewayBudget(t, store, "workspace-budget", "acct-gateway", int64(9))
	seedTenantMember(t, store, "acct-other", "org-other", "usr-other", "beta-other@example.com")
	other := loginForTest(t, server, "beta-other@example.com", "CorrectHorseBatteryStaple!")

	response := requestWithMutationKeyForTest(t, server, other, http.MethodPatch, "/api/workspaces/workspace-budget/gateway-budget", `{"enabled":false}`, "budget-cross-account")
	assertErrorResponse(t, response.Code, response.Body.String(), http.StatusNotFound, "workspace_not_found")
	if client.updateCalls != 0 || len(client.userKeyReadIDs) != 0 {
		t.Fatalf("cross-account PATCH reached Sub2API: reads=%#v updates=%d", client.userKeyReadIDs, client.updateCalls)
	}
}
