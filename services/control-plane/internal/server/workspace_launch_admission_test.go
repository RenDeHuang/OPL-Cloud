package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"opl-cloud/services/control-plane/internal/clients"
)

func init() {
	_ = os.Setenv("OPL_CONTROLLED_BASIC_PILOT_ENABLED", "1")
	_ = os.Setenv("OPL_CONTROLLED_BASIC_PILOT_ACCOUNT_IDS", strings.Join([]string{
		"acct-admin", "acct-alpha", "acct-beta", "acct-claim-cas", "acct-corrupt", "acct-gateway", "acct-launch", "acct-monthly", "acct-renewal",
	}, ","))
	_ = os.Setenv("OPL_CONTROLLED_BASIC_PILOT_MAX_IN_FLIGHT", "1")
}

func TestControlledBasicPilotAdmissionDefaultsClosedBeforeAnySideEffect(t *testing.T) {
	t.Setenv("OPL_CONTROLLED_BASIC_PILOT_ENABLED", "")
	t.Setenv("OPL_CONTROLLED_BASIC_PILOT_ACCOUNT_IDS", "acct-alpha")
	fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
	fixture.sub2API.keys = map[int64]clients.Sub2APIWorkspaceKey{}

	response := fixture.launch(t, `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`, "closed-pilot")
	operations, err := fixture.store.ListRuntimeOperations(context.Background())
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "workspace_launch_admission_disabled") || err != nil ||
		len(operations) != 0 || fixture.sub2API.createCalls != 0 || len(fixture.sub2API.charges) != 0 || len(*fixture.events) != 0 {
		t.Fatalf("closed admission status=%d body=%s operations=%#v creates=%d charges=%#v events=%#v err=%v", response.Code, response.Body.String(), operations, fixture.sub2API.createCalls, fixture.sub2API.charges, *fixture.events, err)
	}
}

func TestControlledBasicPilotClosedAllowsOnlyExactProductionAcceptanceBLaunch(t *testing.T) {
	t.Setenv("OPL_CONTROLLED_BASIC_PILOT_ENABLED", "")
	t.Setenv("OPL_CONTROLLED_BASIC_PILOT_ACCOUNT_IDS", "")
	t.Setenv("OPL_INTERNAL_SERVICE_TOKEN", "acceptance-b-capability")
	t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
	t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
	t.Setenv("OPL_WORKSPACE_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app@sha256:"+strings.Repeat("c", 64))
	t.Setenv("OPL_BASIC_COMPUTE_NODE_POOL_ID", "np-basic-acceptance")
	t.Setenv("OPL_BASIC_COMPUTE_INSTANCE_TYPE", "SA5.MEDIUM4")
	key := "acceptance-b-closed-pilot"
	operationID := workspaceLaunchOperationID("acct-alpha", key)
	workspaceID := "ws-" + stableID("workspace-launch-v2", "acct-alpha", operationID)[:18]
	approval := map[string]any{
		"schemaVersion": 1, "operationMode": "acceptance_b_fresh_order", "approvalId": "acceptance-b-approval",
		"expiresAt": "2099-08-05T00:00:00Z", "confirmation": "RUN_ONE_INDEPENDENT_FRESH_BASIC_ORDER_FOR_ACCEPTANCE_B",
		"release":         map[string]any{"mergedMainSha": strings.Repeat("a", 40), "cloudImageDigest": "sha256:" + strings.Repeat("b", 64), "workspaceImageDigest": "sha256:" + strings.Repeat("c", 64)},
		"customer":        map[string]any{"email": "alpha@example.com", "accountId": "acct-alpha"},
		"launch":          map[string]any{"idempotencyKey": key, "operationId": operationID, "workspaceId": workspaceID, "name": "Acceptance B Basic Workspace", "packageId": "basic", "sizeGb": 10, "autoRenew": false},
		"expected":        map[string]any{"nodePoolId": "np-basic-acceptance", "resolvedInstanceType": "SA5.MEDIUM4"},
		"allowedWrites":   []string{"submit_one_workspace_launch", "debit_one_basic_month", "create_one_workspace_key", "create_one_cvm", "claim_one_cvm_ownership", "claim_one_node", "create_one_cbs", "create_one_attachment", "upsert_one_gateway_secret", "create_one_runtime", "activate_one_workspace", "record_one_purchase_receipt"},
		"forbiddenWrites": []string{"provision_account", "adjust_wallet", "submit_second_workspace_launch", "create_second_cvm", "create_second_cbs", "refund", "renew", "delete", "replace", "send_model_request"},
	}
	encoded, err := json.Marshal(approval)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPL_PRODUCTION_BASIC_ACCEPTANCE_B_APPROVAL_JSON", string(encoded))
	fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
	fixture.sub2API.keys = map[int64]clients.Sub2APIWorkspaceKey{}
	req := httptest.NewRequest(http.MethodPost, "/api/workspace-launches", strings.NewReader(`{"name":"Acceptance B Basic Workspace","packageId":"basic","sizeGb":10,"autoRenew":false}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("x-opl-acceptance-b-capability", "acceptance-b-capability")
	req.Header.Set("x-opl-acceptance-b-approval-id", "acceptance-b-approval")
	addAuth(req, fixture.session)
	response := httptest.NewRecorder()
	fixture.server.ServeHTTP(response, req)
	operations, listErr := fixture.store.ListRuntimeOperations(context.Background())
	if response.Code != http.StatusAccepted || listErr != nil || len(operations) != 1 || stringValue(operations[0]["id"]) != operationID {
		t.Fatalf("Acceptance B admission status=%d body=%s operations=%#v err=%v", response.Code, response.Body.String(), operations, listErr)
	}
}

func TestControlledBasicPilotClosedRejectsAcceptanceBDriftBeforeAnySideEffect(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		mutateEnv func(t *testing.T)
		mutateReq func(req *http.Request)
	}{
		{name: "missing capability", mutateReq: func(req *http.Request) { req.Header.Del("x-opl-acceptance-b-capability") }},
		{name: "wrong approval id", mutateReq: func(req *http.Request) { req.Header.Set("x-opl-acceptance-b-approval-id", "approval-wrong") }},
		{name: "release drift", mutateEnv: func(t *testing.T) { t.Setenv("OPL_RELEASE_SHA", strings.Repeat("d", 40)) }},
		{name: "workspace digest drift", mutateEnv: func(t *testing.T) {
			t.Setenv("OPL_WORKSPACE_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app@sha256:"+strings.Repeat("d", 64))
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("OPL_CONTROLLED_BASIC_PILOT_ENABLED", "")
			t.Setenv("OPL_CONTROLLED_BASIC_PILOT_ACCOUNT_IDS", "")
			t.Setenv("OPL_INTERNAL_SERVICE_TOKEN", "acceptance-b-capability")
			t.Setenv("OPL_RELEASE_SHA", strings.Repeat("a", 40))
			t.Setenv("OPL_CLOUD_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:"+strings.Repeat("b", 64))
			t.Setenv("OPL_WORKSPACE_IMAGE", "uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app@sha256:"+strings.Repeat("c", 64))
			t.Setenv("OPL_BASIC_COMPUTE_NODE_POOL_ID", "np-basic-acceptance")
			t.Setenv("OPL_BASIC_COMPUTE_INSTANCE_TYPE", "SA5.MEDIUM4")
			key := "acceptance-b-closed-pilot"
			operationID := workspaceLaunchOperationID("acct-alpha", key)
			approval := map[string]any{
				"schemaVersion": 1, "operationMode": "acceptance_b_fresh_order", "approvalId": "acceptance-b-approval",
				"expiresAt": "2099-08-05T00:00:00Z", "confirmation": productionAcceptanceBConfirmation,
				"release":       map[string]any{"mergedMainSha": strings.Repeat("a", 40), "cloudImageDigest": "sha256:" + strings.Repeat("b", 64), "workspaceImageDigest": "sha256:" + strings.Repeat("c", 64)},
				"customer":      map[string]any{"email": "alpha@example.com", "accountId": "acct-alpha"},
				"launch":        map[string]any{"idempotencyKey": key, "operationId": operationID, "workspaceId": "ws-" + stableID("workspace-launch-v2", "acct-alpha", operationID)[:18], "name": "Acceptance B Basic Workspace", "packageId": "basic", "sizeGb": 10, "autoRenew": false},
				"expected":      map[string]any{"nodePoolId": "np-basic-acceptance", "resolvedInstanceType": "SA5.MEDIUM4"},
				"allowedWrites": productionAcceptanceBAllowedWrites, "forbiddenWrites": productionAcceptanceBForbiddenWrites,
			}
			encoded, err := json.Marshal(approval)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv(productionAcceptanceBApprovalEnv, string(encoded))
			if testCase.mutateEnv != nil {
				testCase.mutateEnv(t)
			}
			fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
			fixture.sub2API.keys = map[int64]clients.Sub2APIWorkspaceKey{}
			req := httptest.NewRequest(http.MethodPost, "/api/workspace-launches", strings.NewReader(`{"name":"Acceptance B Basic Workspace","packageId":"basic","sizeGb":10,"autoRenew":false}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", key)
			req.Header.Set("x-opl-acceptance-b-capability", "acceptance-b-capability")
			req.Header.Set("x-opl-acceptance-b-approval-id", "acceptance-b-approval")
			if testCase.mutateReq != nil {
				testCase.mutateReq(req)
			}
			addAuth(req, fixture.session)
			response := httptest.NewRecorder()
			fixture.server.ServeHTTP(response, req)
			operations, listErr := fixture.store.ListRuntimeOperations(context.Background())
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "workspace_launch_admission_disabled") || listErr != nil ||
				len(operations) != 0 || fixture.sub2API.createCalls != 0 || len(fixture.sub2API.charges) != 0 || len(*fixture.events) != 0 {
				t.Fatalf("status=%d body=%s operations=%#v creates=%d charges=%#v events=%#v err=%v", response.Code, response.Body.String(), operations, fixture.sub2API.createCalls, fixture.sub2API.charges, *fixture.events, listErr)
			}
		})
	}
}

func TestControlledBasicPilotAdmissionRejectsInvalidConfiguration(t *testing.T) {
	t.Setenv("OPL_CONTROLLED_BASIC_PILOT_ENABLED", "true")
	t.Setenv("OPL_CONTROLLED_BASIC_PILOT_ACCOUNT_IDS", "acct-alpha")
	fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
	response := fixture.launch(t, `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`, "invalid-pilot-config")
	operations, err := fixture.store.ListRuntimeOperations(context.Background())
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "workspace_launch_admission_invalid") || err != nil ||
		len(operations) != 0 || len(*fixture.events) != 0 {
		t.Fatalf("invalid admission status=%d body=%s operations=%#v events=%#v err=%v", response.Code, response.Body.String(), operations, *fixture.events, err)
	}
}

func TestControlledBasicPilotAdmissionRequiresExplicitAccountAndBasic(t *testing.T) {
	for _, tc := range []struct {
		name, allowlist, body, wantCode string
	}{
		{name: "account", allowlist: "acct-beta", body: `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`, wantCode: "workspace_launch_account_not_allowed"},
		{name: "pro", allowlist: "acct-alpha", body: `{"name":"Pro","packageId":"pro","sizeGb":100,"autoRenew":false}`, wantCode: "workspace_launch_basic_only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OPL_CONTROLLED_BASIC_PILOT_ENABLED", "1")
			t.Setenv("OPL_CONTROLLED_BASIC_PILOT_ACCOUNT_IDS", tc.allowlist)
			fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
			fixture.sub2API.keys = map[int64]clients.Sub2APIWorkspaceKey{}
			response := fixture.launch(t, tc.body, "controlled-pilot-"+tc.name)
			operations, err := fixture.store.ListRuntimeOperations(context.Background())
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), tc.wantCode) || err != nil ||
				len(operations) != 0 || fixture.sub2API.createCalls != 0 || len(fixture.sub2API.charges) != 0 || len(*fixture.events) != 0 {
				t.Fatalf("%s admission status=%d body=%s operations=%#v creates=%d charges=%#v events=%#v err=%v", tc.name, response.Code, response.Body.String(), operations, fixture.sub2API.createCalls, fixture.sub2API.charges, *fixture.events, err)
			}
		})
	}
}

func TestControlledBasicPilotDisableDoesNotBlockReadOrOriginalContinuation(t *testing.T) {
	fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
	original := newWorkspaceLaunchOperation("acct-alpha", "usr-alpha", "Alpha", "basic", 10, false, pricingCatalogVersion, 52_580_000, "existing-launch")
	original.Status, original.Phase = "key_pending", "key_pending"
	mustStore(t, fixture.store.ClaimWorkspaceLaunch(context.Background(), workspaceLaunchClaimCAS{
		AccountID: original.AccountID, DesiredOperation: workspaceLaunchOperationRow(original),
	}))
	t.Setenv("OPL_CONTROLLED_BASIC_PILOT_ENABLED", "0")

	list := requestWithSession(t, fixture.server, fixture.session, http.MethodGet, "/api/workspace-launches", "")
	continued := fixture.launch(t, `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`, "existing-launch")
	if list.Code != http.StatusOK || continued.Code != http.StatusAccepted || strings.Contains(continued.Body.String(), "workspace_launch_admission_disabled") {
		t.Fatalf("disabled read=%d continuation=%d body=%s", list.Code, continued.Code, continued.Body.String())
	}
}

func TestControlledBasicPilotGlobalCapacityAllowsOneCrossAccountClaim(t *testing.T) {
	t.Setenv("OPL_CONTROLLED_BASIC_PILOT_MAX_IN_FLIGHT", "1")
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	seedTenantMember(t, store, "acct-beta", "org-beta", "usr-beta", "beta@example.com")
	claims := make([]workspaceLaunchClaimCAS, 0, 2)
	for _, identity := range []struct{ accountID, userID, key string }{
		{accountID: "acct-alpha", userID: "usr-alpha", key: "alpha-global-cap"},
		{accountID: "acct-beta", userID: "usr-beta", key: "beta-global-cap"},
	} {
		operation := newWorkspaceLaunchOperation(identity.accountID, identity.userID, "Basic", "basic", 10, false, pricingCatalogVersion, 52_580_000, identity.key)
		operation.Status, operation.Phase = "key_pending", "key_pending"
		claims = append(claims, workspaceLaunchClaimCAS{AccountID: identity.accountID, DesiredOperation: workspaceLaunchOperationRow(operation)})
	}
	start, results := make(chan struct{}), make(chan error, len(claims))
	for _, claim := range claims {
		go func(claim workspaceLaunchClaimCAS) {
			<-start
			results <- store.ClaimWorkspaceLaunch(context.Background(), claim)
		}(claim)
	}
	close(start)
	won, capacity := 0, 0
	for range claims {
		if err := <-results; err == nil {
			won++
		} else if err.Error() == "workspace_launch_capacity_reached" {
			capacity++
		} else {
			t.Fatalf("unexpected claim error: %v", err)
		}
	}
	rows, err := store.ListRuntimeOperations(context.Background())
	if err != nil || won != 1 || capacity != 1 || len(rows) != 1 {
		t.Fatalf("global capacity won=%d capacity=%d rows=%#v err=%v", won, capacity, rows, err)
	}
}

func TestControlledBasicPilotHealthIsRedactedAndRequiresDisableOnFirstFailure(t *testing.T) {
	t.Setenv("OPL_CONTROLLED_BASIC_PILOT_ENABLED", "1")
	t.Setenv("OPL_CONTROLLED_BASIC_PILOT_ACCOUNT_IDS", "acct-alpha")
	fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
	operation := newWorkspaceLaunchOperation("acct-alpha", "usr-alpha", "Alpha", "basic", 10, false, pricingCatalogVersion, 52_580_000, "pilot-observability")
	operation.Status, operation.Phase, operation.ErrorCode = "manual_review", "compute_fulfilling", "fabric_compute_fulfillment_unconfirmed"
	operation.WorkspaceAPIKeyID = 9
	mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(operation)))
	operator := reservedOperatorSessionForTest(t, fixture.server)
	response := requestWithSession(t, fixture.server, operator, http.MethodGet, "/api/operator/health", "")
	var envelope map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	data, _ := envelope["data"].(map[string]any)
	pilot, _ := data["controlledBasicPilot"].(map[string]any)
	pilotData, _ := pilot["data"].(map[string]any)
	serialized := string(mustJSON(pilotData))
	if response.Code != http.StatusOK || pilot["available"] != true || numberField(pilotData, "inFlight", -1) != 1 ||
		numberField(pilotData, "manualReview", -1) != 1 || pilotData["disableRequired"] != true ||
		!strings.Contains(serialized, "compute_fulfilling") || !strings.Contains(serialized, "fabric_compute_fulfillment_unconfirmed") ||
		strings.Contains(serialized, "acct-alpha") || strings.Contains(serialized, operation.ID) || strings.Contains(serialized, operation.WorkspaceID) {
		t.Fatalf("controlled Pilot health status=%d pilot=%#v", response.Code, pilot)
	}
}

func TestControlledBasicPilotHealthCountsLegacyCapacityWithoutFalseFailure(t *testing.T) {
	t.Setenv("OPL_CONTROLLED_BASIC_PILOT_ENABLED", "1")
	t.Setenv("OPL_CONTROLLED_BASIC_PILOT_ACCOUNT_IDS", "acct-alpha")
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	mustStore(t, store.SaveRuntimeOperation(context.Background(), map[string]any{
		"id": "legacy-launch", "operationId": "legacy-launch", "accountId": "acct-alpha", "action": "workspace.launch", "status": "waiting", "result": `{}`,
	}))
	metrics, err := controlledBasicPilotMetrics(context.Background(), store)
	stages, _ := metrics["stages"].(map[string]int)
	failures, _ := metrics["failures"].(map[string]int)
	if err != nil || numberField(metrics, "inFlight", -1) != 1 || stages["legacy_operation"] != 1 || len(failures) != 0 || metrics["disableRequired"] != false {
		t.Fatalf("legacy capacity metrics=%#v err=%v", metrics, err)
	}
}

func TestControlledBasicPilotHealthClearsFailureAfterAuthoritativeTerminalState(t *testing.T) {
	t.Setenv("OPL_CONTROLLED_BASIC_PILOT_ENABLED", "0")
	t.Setenv("OPL_CONTROLLED_BASIC_PILOT_ACCOUNT_IDS", "")
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	operation := newWorkspaceLaunchOperation("acct-alpha", "usr-alpha", "Alpha", "basic", 10, false, pricingCatalogVersion, 52_580_000, "terminal-pilot")
	operation.Status, operation.Phase, operation.ErrorCode = "refunded", "refunded", "fabric_compute_fulfillment_unconfirmed"
	operation.WorkspaceAPIKeyID = 9
	mustStore(t, store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(operation)))
	metrics, err := controlledBasicPilotMetrics(context.Background(), store)
	failures, _ := metrics["failures"].(map[string]int)
	if err != nil || numberField(metrics, "inFlight", -1) != 0 || len(failures) != 0 || metrics["disableRequired"] != false {
		t.Fatalf("terminal metrics=%#v err=%v", metrics, err)
	}
}
