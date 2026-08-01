package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

func TestWorkspaceLaunchOperationRoundTripsWithoutSecrets(t *testing.T) {
	input := workspaceLaunchOperation{
		ID: "launch-alpha", Status: "debit_pending", SchemaVersion: workspaceLaunchSchemaVersion, RequestHash: "hash", Phase: "debit_pending",
		AccountID: "acct-alpha", OwnerUserID: "usr-alpha", WorkspaceID: "ws-alpha", Name: "Alpha", PackageID: "basic",
		StorageGB: 10, PriceVersion: pilotPriceVersion, TotalChargeUSDMicros: 52_580_000,
		ComputeID: "ca-alpha", StorageID: "vol-alpha",
		AttachmentID: "attachment-alpha", AttachmentOperationID: "attach-operation-alpha", WorkspaceOperationID: "workspace-operation-alpha",
		WorkspaceAPIKeyID: 19, RedeemCode: "opl:launch-alpha",
	}
	row := workspaceLaunchOperationRow(input)
	decoded, err := decodeWorkspaceLaunchOperation(row)
	if err != nil || decoded.RequestHash != input.RequestHash || decoded.ID != input.ID || decoded.Status != input.Status || decoded.PriceVersion != pilotPriceVersion {
		t.Fatalf("decoded=%#v err=%v", decoded, err)
	}
	if row["action"] != workspaceLaunchAction || row["resourceKind"] != "workspace_launch" || row["computeAllocationId"] != input.ComputeID || row["storageId"] != input.StorageID {
		t.Fatalf("workspace launch row = %#v", row)
	}
	encoded := stringValue(row["result"])
	for _, forbidden := range []string{"password", "apiKey", "rawProvider"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("encoded launch contains %q: %s", forbidden, encoded)
		}
	}
}

func TestNoLegacyWorkspaceBillingConsumer(t *testing.T) {
	operation := newWorkspaceLaunchOperation("acct-alpha", "usr-alpha", "Alpha", "basic", 10, false, pilotPriceVersion, 52_580_000, "launch-v2")
	encoded := encodeWorkspaceLaunchOperation(operation)
	for _, field := range []string{"pricingVersion", "totalMonthlyPriceCnyCents", "computeBillingOperationId", "storageBillingOperationId"} {
		if strings.Contains(encoded, `"`+field+`"`) {
			t.Fatalf("current Workspace launch persisted legacy field %s: %s", field, encoded)
		}
	}
}

func TestWorkspaceLaunchIdentityCreatesDistinctWorkspacesPerIdempotencyIntent(t *testing.T) {
	first := newWorkspaceLaunchOperation("acct-alpha", "usr-alpha", "Alpha", "basic", 10, false, pilotPriceVersion, 52_580_000, "launch-first")
	replay := newWorkspaceLaunchOperation("acct-alpha", "usr-alpha", "Alpha", "basic", 10, false, pilotPriceVersion, 52_580_000, "launch-first")
	second := newWorkspaceLaunchOperation("acct-alpha", "usr-alpha", "Beta", "basic", 10, false, pilotPriceVersion, 52_580_000, "launch-second")

	if first.WorkspaceID == "" || first.WorkspaceID != replay.WorkspaceID {
		t.Fatalf("same launch intent must keep one Workspace identity: first=%q replay=%q", first.WorkspaceID, replay.WorkspaceID)
	}
	if first.WorkspaceID == second.WorkspaceID {
		t.Fatalf("different launch intents must create distinct Workspaces: first=%q second=%q", first.WorkspaceID, second.WorkspaceID)
	}
}

func TestWorkspaceLaunchUsesWorkspaceScopedReservedKey(t *testing.T) {
	operation := newWorkspaceLaunchOperation("acct-alpha", "usr-alpha", "Alpha", "basic", 10, false, pilotPriceVersion, 52_580_000, "launch-first")
	if operation.WorkspaceID == primaryWorkspaceID(operation.AccountID) {
		t.Fatalf("new Workspace reused legacy primary identity %q", operation.WorkspaceID)
	}
	want := workspaceReservedKeyName(operation.WorkspaceID)
	if want == "opl-workspace" || !strings.HasPrefix(want, "opl-workspace-") {
		t.Fatalf("new Workspace reserved Key name=%q", want)
	}
}

func TestWorkspaceLaunchResponseAllowsOnlyCustomerSafeFields(t *testing.T) {
	operation := workspaceLaunchOperation{
		ID: "launch-alpha", Status: "unknown", SchemaVersion: workspaceLaunchSchemaVersion, RequestHash: "hash", Phase: "debit_pending",
		AccountID: "acct-alpha", OwnerUserID: "usr-private", WorkspaceID: "ws-alpha", Name: "Alpha", PackageID: "basic",
		StorageGB: 10, PriceVersion: pilotPriceVersion, TotalChargeUSDMicros: 52_580_000,
		ComputeID: "ca-alpha", StorageID: "vol-alpha",
		AttachmentID: "attachment-alpha", AttachmentOperationID: "attachment-operation-private", WorkspaceOperationID: "workspace-operation-private",
		WorkspaceAPIKeyID: 19, RedeemCode: "opl:launch-alpha",
		ComputeClaimApproval: &workspaceComputeClaimApprovalBinding{
			ApprovalID:           "approval-alpha",
			ApprovalDigest:       strings.Repeat("a", 64),
			RecoveryKey:          "recovery-alpha",
			WorkspaceImageDigest: "sha256:" + strings.Repeat("b", 64),
			Customer:             workspaceComputeClaimApprovalCustomer{Email: "private-owner@example.com", AccountID: "acct-alpha"},
			Resources:            workspaceComputeClaimApprovalResources{GatewaySecretRef: "private-secret-ref"},
		},
		ReadbackRecoveryProof: customerSensitiveWorkspaceLaunchReadbackProof(),
		ErrorCode:             "upstream_unavailable",
	}
	row := workspaceLaunchOperationRow(operation)
	var persisted map[string]any
	if err := json.Unmarshal([]byte(stringValue(row["result"])), &persisted); err != nil {
		t.Fatal(err)
	}
	persisted["dependencyError"] = "private upstream detail"
	persisted["password"] = "private-password"
	encoded, err := json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	row["result"] = string(encoded)
	row["internalDependencyError"] = "private row detail"

	response, err := workspaceLaunchResponse(row)
	if err != nil {
		t.Fatal(err)
	}
	if response["operationId"] != operation.ID || response["status"] != operation.Status || response["phase"] != operation.Phase || response["errorCode"] != operation.ErrorCode {
		t.Fatalf("workspace launch response = %#v", response)
	}
	if response["priceVersion"] != pilotPriceVersion || response["autoRenew"] != false || response["totalChargeUsdMicros"] != int64(52_580_000) {
		t.Fatalf("workspace launch pricing response = %#v", response)
	}
	if response["workspaceApiKeyId"] != "19" {
		t.Fatalf("workspace launch Key ID must be a decimal string: %#v", response)
	}
	recovery, ok := response["recovery"].(map[string]any)
	if !ok || recovery["approvalId"] != "approval-alpha" || recovery["approvalDigest"] != strings.Repeat("a", 64) ||
		recovery["recoveryKey"] != "recovery-alpha" || recovery["workspaceImageDigest"] != "sha256:"+strings.Repeat("b", 64) || len(recovery) != 4 {
		t.Fatalf("workspace launch recovery projection = %#v", response["recovery"])
	}
	for _, forbidden := range []string{"pricingVersion", "totalMonthlyPriceCnyCents", "readbackRecoveryProof"} {
		if _, ok := response[forbidden]; ok {
			t.Fatalf("workspace launch response exposed %s: %#v", forbidden, response)
		}
	}
	responseJSON, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"usr-private", "attachment-operation-private", "workspace-operation-private", "private upstream detail", "private-password", "private row detail",
		"private-owner@example.com", "private-secret-ref", "10.20.30.99", "machine-private", "ins-private", "node-private",
		"fop-private", "fabric-operation-private", "provider-operation-private",
	} {
		if strings.Contains(string(responseJSON), forbidden) {
			t.Fatalf("workspace launch response leaked %q: %s", forbidden, responseJSON)
		}
	}
}

func customerSensitiveWorkspaceLaunchReadbackProof() *workspaceLaunchReadbackRecoveryProof {
	identity := workspaceLaunchReadbackRecoveryFabricOperationIdentity{
		IdempotencyKey: "workspace-launch-private:compute", FabricRecordID: "fop-private",
		FabricOperationID: "fabric-operation-private", RequestHash: "request-hash-private",
		ResourceOperationID: "resource-operation-private", ProviderOperationID: "provider-operation-private",
	}
	return &workspaceLaunchReadbackRecoveryProof{
		SchemaVersion: 1, Eligible: true, Reason: "none", Stage: "runtime",
		Customer: workspaceLaunchReadbackRecoveryCustomer{Email: "readback-private@example.com", AccountID: "acct-alpha", OwnerUserID: "usr-private"},
		Target: workspaceLaunchReadbackRecoveryTarget{
			LaunchOperationID: "workspace-launch-private", AccountID: "acct-alpha", WorkspaceID: "ws-alpha",
			MachineName: "machine-private", NodeName: "node-private", CVMInstanceID: "ins-private", PrivateIP: "10.20.30.99",
		},
		Resources: workspaceLaunchReadbackRecoveryResources{
			ComputeProviderResourceID: "ins-private", StorageProviderResourceID: "disk-private", AttachmentProviderID: "pv/private:pvc/private",
		},
		OperationIDs: workspaceLaunchReadbackRecoveryOperationIDs{
			LaunchOperationID: "workspace-launch-private", LaunchRequestHash: "launch-request-private", MachineOwnershipID: "ownership-private",
			Compute: identity, Storage: identity, Attachment: identity, Secret: identity, Runtime: identity,
			ActivationOperationID: "activation-operation-private", ReceiptOperationID: "receipt-operation-private",
		},
		WorkspaceImageDigest: "sha256:" + strings.Repeat("d", 64), AttemptBudget: workspaceLaunchStageBudget{Attempted: 1, Unknown: 1, Max: 1},
	}
}

func assertCustomerWorkspaceLaunchResponseSafe(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Code < http.StatusOK || response.Code >= http.StatusMultipleChoices {
		t.Fatalf("customer response status=%d body=%s", response.Code, response.Body.String())
	}
	for _, forbidden := range []string{
		"readbackRecoveryProof", "readback-private@example.com", "10.20.30.99", "machine-private", "ins-private", "node-private",
		"fop-private", "fabric-operation-private", "provider-operation-private", "resource-operation-private", "ownership-private",
	} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("customer response leaked %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestWorkspaceLaunchCustomerEndpointsNeverExposeReadbackRecoveryProof(t *testing.T) {
	fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
	body := `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`
	created := fixture.launch(t, body, "launch-customer-safe")
	assertCustomerWorkspaceLaunchResponseSafe(t, created)
	var createdBody map[string]any
	if err := json.Unmarshal(created.Body.Bytes(), &createdBody); err != nil {
		t.Fatal(err)
	}
	operationID := stringValue(createdBody["operationId"])
	row, found, err := fixture.store.GetRuntimeOperation(context.Background(), operationID)
	if err != nil || !found {
		t.Fatalf("launch operation read found=%t err=%v", found, err)
	}
	operation, err := decodeWorkspaceLaunchOperation(row)
	if err != nil {
		t.Fatal(err)
	}
	operation.ReadbackRecoveryProof = customerSensitiveWorkspaceLaunchReadbackProof()
	mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(operation)))

	replayed := fixture.launch(t, body, "launch-customer-safe")
	assertCustomerWorkspaceLaunchResponseSafe(t, replayed)
	listed := requestWithSession(t, fixture.server, fixture.session, http.MethodGet, "/api/workspace-launches", "")
	assertCustomerWorkspaceLaunchResponseSafe(t, listed)
	detail := requestWithSession(t, fixture.server, fixture.session, http.MethodGet, "/api/workspace-launches/"+operationID, "")
	assertCustomerWorkspaceLaunchResponseSafe(t, detail)
}

type workspaceLaunchHTTPFixture struct {
	server  http.Handler
	store   *memoryTableStore
	session *httptest.ResponseRecorder
	events  *[]string
	sub2API *workspaceLaunchSub2API
	fabric  *monthlyFabric
}

type workspaceLaunchClaimOrderStore struct {
	*memoryTableStore
	events *[]string
	err    error
}

func (s *workspaceLaunchClaimOrderStore) ClaimWorkspaceLaunch(ctx context.Context, claim workspaceLaunchClaimCAS) error {
	*s.events = append(*s.events, "store.claim_workspace_launch")
	if s.err != nil {
		return s.err
	}
	return s.memoryTableStore.ClaimWorkspaceLaunch(ctx, claim)
}

func newWorkspaceLaunchHTTPFixture(t *testing.T, balances ...int64) workspaceLaunchHTTPFixture {
	t.Helper()
	t.Setenv("OPL_MONTHLY_BILLING_WORKER_ENABLED", "false")
	t.Setenv("OPL_PROVIDER_RECONCILE_WORKER_ENABLED", "false")
	t.Setenv("OPL_ARCHIVE_RETENTION_WORKER_ENABLED", "false")
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	promoteWorkspaceLaunchOwner(t, store, "usr-alpha")
	events := []string{}
	sub2API := &monthlySub2API{events: &events, balances: balances}
	launchSub2API := &workspaceLaunchSub2API{monthlySub2API: sub2API, keys: map[int64]clients.Sub2APIWorkspaceKey{
		9: {ID: 9, UserID: 41, Name: "opl-workspace", Key: "workspace-key-secret", Status: "active"},
	}}
	fabric := &monthlyFabric{events: &events}
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, fabric, launchSub2API), store)
	if err != nil {
		t.Fatal(err)
	}
	return workspaceLaunchHTTPFixture{
		server: server, store: store, session: loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!"),
		events: &events, sub2API: launchSub2API, fabric: fabric,
	}
}

func promoteWorkspaceLaunchOwner(t *testing.T, store controlPlaneTableStore, userID string) {
	t.Helper()
	users, err := store.ListUsers(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	user := findRecord(users, userID)
	if user == nil {
		t.Fatalf("workspace launch owner %s not found", userID)
	}
	user["role"] = "owner"
	mustStore(t, store.SaveUser(context.Background(), user))
}

func (f workspaceLaunchHTTPFixture) launch(t *testing.T, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	return requestWithMutationKeyForTest(t, f.server, f.session, http.MethodPost, "/api/workspace-launches", body, key)
}

func TestWorkspaceLaunchRequiresCompleteBodyBeforeExternalCalls(t *testing.T) {
	for name, input := range map[string]struct{ body, errorCode string }{
		"name":             {body: `{"packageId":"basic","sizeGb":10,"autoRenew":false}`},
		"packageId":        {body: `{"name":"Alpha","sizeGb":10,"autoRenew":false}`},
		"sizeGb":           {body: `{"name":"Alpha","packageId":"basic","autoRenew":false}`},
		"autoRenew":        {body: `{"name":"Alpha","packageId":"basic","sizeGb":10}`, errorCode: "autoRenew_required"},
		"autoRenew string": {body: `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":"false"}`, errorCode: "autoRenew_required"},
		"autoRenew number": {body: `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":0}`, errorCode: "autoRenew_required"},
		"autoRenew null":   {body: `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":null}`, errorCode: "autoRenew_required"},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
			response := fixture.launch(t, input.body, "launch-alpha")
			operations, _ := fixture.store.ListRuntimeOperations(context.Background())
			if response.Code != http.StatusBadRequest || input.errorCode != "" && !strings.Contains(response.Body.String(), input.errorCode) || len(*fixture.events) != 0 || len(operations) != 0 {
				t.Fatalf("missing %s status=%d body=%s events=%#v operations=%#v", name, response.Code, response.Body.String(), *fixture.events, operations)
			}
		})
	}
}

func TestCloudAdminCanLaunchOwnWorkspace(t *testing.T) {
	fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
	fixture.session = reservedOperatorSessionForTest(t, fixture.server)
	response := fixture.launch(t, `{"name":"Admin","packageId":"basic","sizeGb":10,"autoRenew":false}`, "launch-admin")
	if response.Code != http.StatusAccepted {
		t.Fatalf("admin launch status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRefundedWorkspaceLaunchAllowsNewIdempotencyKey(t *testing.T) {
	fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
	refunded := newWorkspaceLaunchOperation("acct-alpha", "usr-alpha", "Alpha", "basic", 10, false, pilotPriceVersion, 52_580_000, "refunded-launch")
	refunded.WorkspaceAPIKeyID, refunded.Status, refunded.Phase = 9, "refunded", "refunded"
	mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(refunded)))
	response := fixture.launch(t, `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`, "new-launch-after-refund")
	operations, err := fixture.store.ListRuntimeOperations(context.Background())
	if response.Code != http.StatusAccepted || err != nil || len(operations) != 2 || len(fixture.sub2API.charges) != 0 || len(fixture.sub2API.refunds) != 0 {
		t.Fatalf("new launch status=%d operations=%#v charges=%#v refunds=%#v err=%v body=%s", response.Code, operations, fixture.sub2API.charges, fixture.sub2API.refunds, err, response.Body.String())
	}
}

func TestWorkspaceLaunchRejectsAutoRenewBeforeExternalCalls(t *testing.T) {
	fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
	response := fixture.launch(t, `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":true}`, "launch-auto-renew")
	operations, _ := fixture.store.ListRuntimeOperations(context.Background())
	workspaces, _ := fixture.store.ListWorkspaces(context.Background(), "acct-alpha")
	computes, _ := fixture.store.ListComputes(context.Background(), "acct-alpha")
	storages, _ := fixture.store.ListStorages(context.Background(), "acct-alpha")
	attachments, _ := fixture.store.ListAttachments(context.Background(), "acct-alpha")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "autoRenew_unavailable") {
		t.Fatalf("auto-renew launch status=%d body=%s", response.Code, response.Body.String())
	}
	if len(*fixture.events) != 0 || len(operations) != 0 || len(workspaces) != 0 || len(computes) != 0 || len(storages) != 0 || len(attachments) != 0 || len(fixture.sub2API.charges) != 0 || len(fixture.sub2API.refunds) != 0 || len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 {
		t.Fatalf("auto-renew launch caused side effects: events=%#v operations=%#v workspaces=%#v computes=%#v storages=%#v attachments=%#v charges=%#v refunds=%#v", *fixture.events, operations, workspaces, computes, storages, attachments, fixture.sub2API.charges, fixture.sub2API.refunds)
	}
}

func TestWorkspaceLaunchRejectsUnknownAndCrossPackageStorageBeforeExternalCalls(t *testing.T) {
	for _, body := range []string{
		`{"name":"Alpha","packageId":"basic","sizeGb":100,"autoRenew":false}`,
		`{"name":"Alpha","packageId":"pro","sizeGb":10,"autoRenew":false}`,
		`{"name":"Alpha","packageId":"enterprise","sizeGb":10,"autoRenew":false}`,
	} {
		fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
		response := fixture.launch(t, body, "launch-alpha")
		operations, _ := fixture.store.ListRuntimeOperations(context.Background())
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_pricing_input") || len(*fixture.events) != 0 || len(operations) != 0 {
			t.Fatalf("invalid package/storage status=%d body=%s events=%#v operations=%#v", response.Code, response.Body.String(), *fixture.events, operations)
		}
	}
}

func TestWorkspaceLaunchRejectsClientPrice(t *testing.T) {
	for name, field := range map[string]string{
		"price version": `"priceVersion":"client-price"`,
		"total":         `"totalChargeUsdMicros":1`,
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
			body := `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false,` + field + `}`
			response := fixture.launch(t, body, "launch-client-price")
			operations, _ := fixture.store.ListRuntimeOperations(context.Background())
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "client_pricing_forbidden") || len(*fixture.events) != 0 || len(operations) != 0 {
				t.Fatalf("client price status=%d body=%s events=%#v operations=%#v", response.Code, response.Body.String(), *fixture.events, operations)
			}
		})
	}
}

func TestWorkspaceLaunchTotalPreflightRejectsInsufficientBalanceWithoutSideEffects(t *testing.T) {
	fixture := newWorkspaceLaunchHTTPFixture(t, 52_579_999)
	response := fixture.launch(t, `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`, "launch-alpha")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), errMonthlyInsufficientBalance.Error()) {
		t.Fatalf("insufficient launch status = %d, want 409: %s", response.Code, response.Body.String())
	}
	if want := []string{"fabric.monthly.preflight", "fabric.monthly.preflight", "sub2api.balance"}; !reflect.DeepEqual(*fixture.events, want) {
		t.Fatalf("preflight events = %#v, want %#v", *fixture.events, want)
	}
	operations, _ := fixture.store.ListRuntimeOperations(context.Background())
	computes, _ := fixture.store.ListComputes(context.Background(), "acct-alpha")
	storages, _ := fixture.store.ListStorages(context.Background(), "acct-alpha")
	if len(fixture.sub2API.charges) != 0 || len(fixture.sub2API.refunds) != 0 || len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 || len(operations) != 0 || len(computes) != 0 || len(storages) != 0 {
		t.Fatalf("insufficient launch caused side effects: charges=%#v refunds=%#v compute=%#v storage=%#v operations=%#v", fixture.sub2API.charges, fixture.sub2API.refunds, fixture.fabric.computeIDs, fixture.fabric.storageIDs, operations)
	}
}

func TestWorkspaceLaunchGatewayKeyPreflightFailsBeforeBalanceAndSideEffects(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "missing", err: clients.ErrSub2APIWorkspaceKeyMissing, wantStatus: http.StatusConflict, wantCode: "gateway_key_missing"},
		{name: "ambiguous", err: clients.ErrSub2APIWorkspaceKeyAmbiguous, wantStatus: http.StatusConflict, wantCode: "gateway_key_ambiguous"},
		{name: "unavailable", err: errors.New("Sub2API unavailable"), wantStatus: http.StatusBadGateway, wantCode: "upstream_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
			fixture.sub2API.userKeysErr = tc.err
			response := fixture.launch(t, `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`, "launch-alpha")
			if response.Code != tc.wantStatus || !strings.Contains(response.Body.String(), tc.wantCode) {
				t.Fatalf("Gateway Key launch status = %d, want %d %s: %s", response.Code, tc.wantStatus, tc.wantCode, response.Body.String())
			}
			wantEvents := []string{"fabric.monthly.preflight", "fabric.monthly.preflight", "sub2api.balance", "sub2api.user_keys"}
			operations, _ := fixture.store.ListRuntimeOperations(context.Background())
			if !reflect.DeepEqual(*fixture.events, wantEvents) || len(operations) != 1 || len(fixture.sub2API.charges) != 0 || len(fixture.sub2API.refunds) != 0 || len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 {
				t.Fatalf("Gateway Key failure caused side effects: events=%#v operations=%#v charges=%#v refunds=%#v compute=%#v storage=%#v", *fixture.events, operations, fixture.sub2API.charges, fixture.sub2API.refunds, fixture.fabric.computeIDs, fixture.fabric.storageIDs)
			}
			operation, err := decodeWorkspaceLaunchOperation(operations[0])
			if err != nil || operation.Phase != "key_pending" || operation.WorkspaceAPIKeyID != 0 || strings.Contains(string(mustJSON(operations)), "workspace-key-secret") {
				t.Fatalf("Gateway Key failure did not preserve a non-secret recovery intent: operation=%#v err=%v", operation, err)
			}
		})
	}
}

func TestWorkspaceLaunchKeyPendingDifferentRequestKeyRecoversOriginalLaunch(t *testing.T) {
	for _, plan := range []struct {
		packageID string
		sizeGB    int
	}{
		{packageID: "basic", sizeGB: 10},
		{packageID: "pro", sizeGB: 100},
	} {
		t.Run(plan.packageID, func(t *testing.T) {
			fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
			fixture.sub2API.userKeysErr = clients.ErrSub2APIWorkspaceKeyMissing
			body := fmt.Sprintf(`{"name":"Alpha","packageId":%q,"sizeGb":%d,"autoRenew":false}`, plan.packageID, plan.sizeGB)

			first := fixture.launch(t, body, "launch-key-pending-first")
			if first.Code != http.StatusConflict || !strings.Contains(first.Body.String(), "gateway_key_missing") {
				t.Fatalf("first key-pending POST status=%d body=%s", first.Code, first.Body.String())
			}
			fixture.sub2API.userKeysErr = nil
			fixture.sub2API.keys = map[int64]clients.Sub2APIWorkspaceKey{}
			second := fixture.launch(t, body, "launch-key-pending-retry")
			if second.Code != http.StatusAccepted {
				t.Fatalf("same request with a new key must recover original launch: status=%d body=%s", second.Code, second.Body.String())
			}
			var secondBody map[string]any
			operations, err := fixture.store.ListRuntimeOperations(context.Background())
			if err != nil || len(operations) != 1 {
				t.Fatalf("first key-pending operations=%#v err=%v", operations, err)
			}
			original, err := decodeWorkspaceLaunchOperation(operations[0])
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(second.Body.Bytes(), &secondBody); err != nil {
				t.Fatal(err)
			}
			if original.ID != stringValue(secondBody["operationId"]) {
				t.Fatalf("same key-pending request created a second launch: original=%#v second=%#v", original, secondBody)
			}
			operations, err = fixture.store.ListRuntimeOperations(context.Background())
			if err != nil || len(operations) != 1 || fixture.sub2API.createCalls != 1 || strings.Contains(string(mustJSON(operations)), "created-workspace-key-secret") {
				t.Fatalf("key-pending recovery operations=%#v createCalls=%d err=%v", operations, fixture.sub2API.createCalls, err)
			}
		})
	}
}

func TestWorkspaceLaunchKeyPendingConcurrentRecoveryReturnsPersistedWinner(t *testing.T) {
	t.Setenv("OPL_MONTHLY_BILLING_WORKER_ENABLED", "false")
	t.Setenv("OPL_PROVIDER_RECONCILE_WORKER_ENABLED", "false")
	t.Setenv("OPL_ARCHIVE_RETENTION_WORKER_ENABLED", "false")
	store := &workspaceLaunchKeyPersistBarrierStore{memoryTableStore: newMemoryTableStore(), release: make(chan struct{})}
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	promoteWorkspaceLaunchOwner(t, store, "usr-alpha")
	original := newWorkspaceLaunchOperation("acct-alpha", "usr-alpha", "Alpha", "basic", 10, false, pricingCatalogVersion, 52_580_000, "launch-key-pending-original")
	original.Phase = "key_pending"
	mustStore(t, store.ClaimWorkspaceLaunch(context.Background(), workspaceLaunchClaimCAS{
		AccountID: "acct-alpha", DesiredOperation: workspaceLaunchOperationRow(original),
	}))

	newServer := func() (http.Handler, *httptest.ResponseRecorder) {
		events := []string{}
		key := clients.Sub2APIWorkspaceKey{ID: 19, UserID: 41, Name: workspaceReservedKeyName(original.WorkspaceID), Status: "active"}
		gateway := &workspaceLaunchSub2API{
			monthlySub2API: &monthlySub2API{events: &events, balances: []int64{1_000_000_000}},
			keys:           map[int64]clients.Sub2APIWorkspaceKey{key.ID: key},
		}
		server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &monthlyFabric{events: &events}, gateway), store)
		if err != nil {
			t.Fatal(err)
		}
		return server, loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	}
	firstServer, firstSession := newServer()
	secondServer, secondSession := newServer()
	results := make(chan *httptest.ResponseRecorder, 2)
	for index, pair := range []struct {
		server  http.Handler
		session *httptest.ResponseRecorder
	}{{firstServer, firstSession}, {secondServer, secondSession}} {
		go func(index int, pair struct {
			server  http.Handler
			session *httptest.ResponseRecorder
		}) {
			results <- requestWithMutationKeyForTest(t, pair.server, pair.session, http.MethodPost, "/api/workspace-launches", `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`, fmt.Sprintf("launch-key-pending-recovery-%d", index))
		}(index, pair)
	}
	for range 2 {
		response := <-results
		if response.Code != http.StatusAccepted {
			t.Fatalf("concurrent key-pending recovery status=%d body=%s", response.Code, response.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || stringValue(body["operationId"]) != original.ID {
			t.Fatalf("concurrent key-pending recovery body=%#v err=%v", body, err)
		}
	}
	rows, err := store.memoryTableStore.ListRuntimeOperations(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("concurrent key-pending rows=%#v err=%v", rows, err)
	}
	persisted, err := decodeWorkspaceLaunchOperation(rows[0])
	if err != nil || persisted.ID != original.ID || persisted.Phase != "debit_pending" || persisted.WorkspaceAPIKeyID != 19 {
		t.Fatalf("concurrent key-pending persisted=%#v err=%v", persisted, err)
	}
}

func TestWorkspaceKeyConvergenceCreatesBeforeBalanceAndPersistsID(t *testing.T) {
	fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
	client := fixture.sub2API
	client.keys = map[int64]clients.Sub2APIWorkspaceKey{}

	response := fixture.launch(t, `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`, "launch-converge")
	if response.Code != http.StatusAccepted {
		t.Fatalf("launch status=%d body=%s", response.Code, response.Body.String())
	}
	operations, err := fixture.store.ListRuntimeOperations(context.Background())
	if err != nil || len(operations) != 1 {
		t.Fatalf("launch operations=%#v err=%v", operations, err)
	}
	operation, err := decodeWorkspaceLaunchOperation(operations[0])
	if err != nil || operation.WorkspaceAPIKeyID != 19 || client.createCalls != 1 {
		t.Fatalf("converged operation=%#v creates=%d err=%v", operation, client.createCalls, err)
	}
	if got := *fixture.events; !reflect.DeepEqual(got, []string{
		"fabric.monthly.preflight", "fabric.monthly.preflight", "sub2api.balance", "sub2api.user_keys", "sub2api.create_workspace_key", "sub2api.user_keys",
	}) {
		t.Fatalf("convergence order=%#v", got)
	}
	if strings.Contains(string(mustJSON(operations)), "created-workspace-key-secret") {
		t.Fatalf("launch operation persisted raw Key: %#v", operations)
	}
	replay := fixture.launch(t, `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`, "launch-converge")
	if replay.Code != http.StatusAccepted || client.createCalls != 1 || len(client.keys) != 1 {
		t.Fatalf("convergence replay status=%d creates=%d keys=%#v", replay.Code, client.createCalls, client.keys)
	}
}

func TestWorkspaceLaunchClaimsBeforeCreatingWorkspaceKey(t *testing.T) {
	t.Setenv("OPL_MONTHLY_BILLING_WORKER_ENABLED", "false")
	t.Setenv("OPL_PROVIDER_RECONCILE_WORKER_ENABLED", "false")
	t.Setenv("OPL_ARCHIVE_RETENTION_WORKER_ENABLED", "false")
	events := []string{}
	store := &workspaceLaunchClaimOrderStore{memoryTableStore: newMemoryTableStore(), events: &events}
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	promoteWorkspaceLaunchOwner(t, store, "usr-alpha")
	sub2API := &workspaceLaunchSub2API{monthlySub2API: &monthlySub2API{events: &events, balances: []int64{1_000_000_000}}, keys: map[int64]clients.Sub2APIWorkspaceKey{}}
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &monthlyFabric{events: &events}, sub2API), store)
	if err != nil {
		t.Fatal(err)
	}
	session := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	response := requestWithMutationKeyForTest(t, server, session, http.MethodPost, "/api/workspace-launches", `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`, "launch-claim-first")
	if response.Code != http.StatusAccepted {
		t.Fatalf("launch status=%d body=%s", response.Code, response.Body.String())
	}
	claimIndex, createIndex := slices.Index(events, "store.claim_workspace_launch"), slices.Index(events, "sub2api.create_workspace_key")
	if claimIndex < 0 || createIndex < 0 || claimIndex > createIndex {
		t.Fatalf("Workspace launch must claim before Key creation: %#v", events)
	}
}

func TestWorkspaceLaunchLosingCASDoesNotCreateWorkspaceKey(t *testing.T) {
	t.Setenv("OPL_MONTHLY_BILLING_WORKER_ENABLED", "false")
	t.Setenv("OPL_PROVIDER_RECONCILE_WORKER_ENABLED", "false")
	t.Setenv("OPL_ARCHIVE_RETENTION_WORKER_ENABLED", "false")
	events := []string{}
	store := &workspaceLaunchClaimOrderStore{memoryTableStore: newMemoryTableStore(), events: &events, err: errWorkspaceLaunchInProgress}
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	promoteWorkspaceLaunchOwner(t, store, "usr-alpha")
	sub2API := &workspaceLaunchSub2API{monthlySub2API: &monthlySub2API{events: &events, balances: []int64{1_000_000_000}}, keys: map[int64]clients.Sub2APIWorkspaceKey{}}
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &monthlyFabric{events: &events}, sub2API), store)
	if err != nil {
		t.Fatal(err)
	}
	session := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	response := requestWithMutationKeyForTest(t, server, session, http.MethodPost, "/api/workspace-launches", `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`, "launch-loser")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), errWorkspaceLaunchInProgress.Error()) {
		t.Fatalf("losing launch status=%d body=%s", response.Code, response.Body.String())
	}
	if sub2API.createCalls != 0 || len(sub2API.keys) != 0 {
		t.Fatalf("losing launch created orphaned Workspace Key: creates=%d keys=%#v events=%#v", sub2API.createCalls, sub2API.keys, events)
	}
}

func TestWorkspaceKeyAmbiguityStopsBeforeBalanceAndCharge(t *testing.T) {
	operation := newWorkspaceLaunchOperation("acct-alpha", "usr-alpha", "Alpha", "basic", 10, false, pilotPriceVersion, 52_580_000, "launch-ambiguous")
	name := workspaceReservedKeyName(operation.WorkspaceID)
	for _, keys := range []map[int64]clients.Sub2APIWorkspaceKey{
		{9: {ID: 9, UserID: 41, Name: name, Status: "disabled"}},
		{9: {ID: 9, UserID: 41, Name: name, Status: "active"}, 10: {ID: 10, UserID: 41, Name: name, Status: "active"}},
	} {
		fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
		client := fixture.sub2API
		client.keys = keys
		response := fixture.launch(t, `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`, "launch-ambiguous")
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "gateway_key_ambiguous") {
			t.Fatalf("ambiguous launch=%d body=%s", response.Code, response.Body.String())
		}
		operations, _ := fixture.store.ListRuntimeOperations(context.Background())
		if countStrings(*fixture.events, "sub2api.balance") != 1 || len(client.charges) != 0 || len(operations) != 1 {
			t.Fatalf("ambiguous Key crossed billing gate: events=%#v charges=%#v operations=%#v", *fixture.events, client.charges, operations)
		}
		operation, err := decodeWorkspaceLaunchOperation(operations[0])
		if err != nil || operation.Phase != "key_pending" || operation.WorkspaceAPIKeyID != 0 {
			t.Fatalf("ambiguous Key lost recovery intent: operation=%#v err=%v", operation, err)
		}
	}
}

func TestWorkspaceLaunchReplayAndFingerprintConflictAvoidExternalSideEffects(t *testing.T) {
	fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
	body := `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`
	first := fixture.launch(t, body, "launch-alpha")
	if first.Code != http.StatusAccepted {
		t.Fatalf("first launch status = %d, want 202: %s", first.Code, first.Body.String())
	}
	var original map[string]any
	if err := json.NewDecoder(first.Body).Decode(&original); err != nil {
		t.Fatal(err)
	}
	eventCount := len(*fixture.events)
	replay := fixture.launch(t, body, "launch-alpha")
	var replayed map[string]any
	if err := json.NewDecoder(replay.Body).Decode(&replayed); err != nil {
		t.Fatal(err)
	}
	if replay.Code != http.StatusAccepted || replayed["operationId"] != original["operationId"] || len(*fixture.events) != eventCount {
		t.Fatalf("launch replay = status %d body %#v events %#v", replay.Code, replayed, *fixture.events)
	}
	for _, changed := range []string{
		`{"name":"Beta","packageId":"basic","sizeGb":10,"autoRenew":false}`,
		`{"name":"Alpha","packageId":"pro","sizeGb":100,"autoRenew":false}`,
	} {
		conflict := fixture.launch(t, changed, "launch-alpha")
		if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), errIdempotencyConflict.Error()) {
			t.Fatalf("changed launch status = %d, want 409: %s", conflict.Code, conflict.Body.String())
		}
	}
	operations, _ := fixture.store.ListRuntimeOperations(context.Background())
	if len(*fixture.events) != eventCount || len(operations) != 1 || len(fixture.sub2API.charges) != 0 || len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 {
		t.Fatalf("launch replay caused side effects: events=%#v operations=%#v", *fixture.events, operations)
	}
}

func TestWorkspaceLaunchPreflightGuardsRunBeforeExternalCalls(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, workspaceLaunchHTTPFixture)
		code  string
	}{
		{
			name: "reconciliation guard",
			setup: func(t *testing.T, fixture workspaceLaunchHTTPFixture) {
				mustStore(t, fixture.store.SaveBillingReconciliation(context.Background(), map[string]any{"id": "global", "guard": map[string]any{"blockNewWorkspaces": true}}))
			},
			code: "billing_reconciliation_blocked",
		},
		{
			name: "different active launch",
			setup: func(t *testing.T, fixture workspaceLaunchHTTPFixture) {
				mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(workspaceLaunchOperation{
					ID: "launch-other", Status: "preparing", RequestHash: "other", Phase: "compute", AccountID: "acct-alpha", OwnerUserID: "usr-alpha",
					WorkspaceID: primaryWorkspaceID("acct-alpha"), PackageID: "basic", StorageGB: 10,
				})))
			},
			code: "workspace_launch_in_progress",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newWorkspaceLaunchHTTPFixture(t)
			tt.setup(t, fixture)
			response := fixture.launch(t, `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`, "launch-alpha")
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), tt.code) {
				t.Fatalf("guarded launch status = %d, want 409 %s: %s", response.Code, tt.code, response.Body.String())
			}
			if len(*fixture.events) != 0 {
				t.Fatalf("guarded launch reached dependencies: %#v", *fixture.events)
			}
		})
	}
}

func TestExistingWorkspaceDoesNotBlockIndependentLaunch(t *testing.T) {
	fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
	mustStore(t, fixture.store.SaveWorkspace(context.Background(), map[string]any{
		"id": primaryWorkspaceID("acct-alpha"), "accountId": "acct-alpha", "ownerAccountId": "acct-alpha", "status": "running",
	}))
	response := fixture.launch(t, `{"name":"Second","packageId":"basic","sizeGb":10,"autoRenew":false}`, "launch-second")
	if response.Code != http.StatusAccepted {
		t.Fatalf("existing Workspace blocked independent launch: %d %s", response.Code, response.Body.String())
	}
}

func TestWorkspaceLaunchRequiresOwnerBeforeExternalCalls(t *testing.T) {
	fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
	users, err := fixture.store.ListUsers(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if findRecord(users, "usr-alpha") == nil {
		t.Fatal("owner missing")
	}
	fixture.store.accounts["acct-alpha"]["ownerUserId"] = "usr-other"

	response := fixture.launch(t, `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`, "launch-alpha")
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "reauthentication_required") {
		t.Fatalf("owner mismatch launch status = %d, want 401: %s", response.Code, response.Body.String())
	}
	operations, _ := fixture.store.ListRuntimeOperations(context.Background())
	if len(*fixture.events) != 0 || len(operations) != 0 {
		t.Fatalf("owner mismatch launch reached dependencies: events=%#v operations=%#v", *fixture.events, operations)
	}
}

func TestWorkspaceLaunchOwnerLifecycleFencesInitialKeyAndClaim(t *testing.T) {
	t.Setenv("OPL_MONTHLY_BILLING_WORKER_ENABLED", "false")
	t.Setenv("OPL_PROVIDER_RECONCILE_WORKER_ENABLED", "false")
	t.Setenv("OPL_ARCHIVE_RETENTION_WORKER_ENABLED", "false")
	store := &recordingWorkspaceLaunchStore{
		memoryTableStore: newMemoryTableStore(), lifecycleStarted: make(chan struct{}), releaseLifecycle: make(chan struct{}),
		workspaceLaunchClaimed: make(chan struct{}),
	}
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	promoteWorkspaceLaunchOwner(t, store, "usr-alpha")
	events := []string{}
	sub2API := &workspaceLaunchSub2API{monthlySub2API: &monthlySub2API{events: &events, balances: []int64{1_000_000_000}}, keys: map[int64]clients.Sub2APIWorkspaceKey{}}
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &monthlyFabric{events: &events}, sub2API), store)
	if err != nil {
		t.Fatal(err)
	}
	session := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	app := server.(*controlPlaneHTTPHandler).app

	disableResult := make(chan error, 1)
	go func() {
		_, err := app.disableUser(map[string]any{"userId": "usr-alpha", "reason": "pilot_offboarding"})
		disableResult <- err
	}()
	select {
	case <-store.lifecycleStarted:
	case <-time.After(time.Second):
		t.Fatal("account disable did not enter the lifecycle transaction")
	}

	req := httptest.NewRequest(http.MethodPost, "/api/workspace-launches", strings.NewReader(`{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "launch-lifecycle-fence")
	addAuth(req, session)
	response := httptest.NewRecorder()
	launchDone := make(chan struct{})
	go func() {
		server.ServeHTTP(response, req)
		close(launchDone)
	}()
	claimCrossedLifecycleFence := false
	select {
	case <-store.workspaceLaunchClaimed:
		claimCrossedLifecycleFence = true
	case <-time.After(100 * time.Millisecond):
	}
	close(store.releaseLifecycle)
	if err := <-disableResult; err != nil {
		t.Fatal(err)
	}
	select {
	case <-launchDone:
	case <-time.After(time.Second):
		t.Fatal("Workspace launch did not leave the lifecycle fence")
	}
	operations, err := store.ListRuntimeOperations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if claimCrossedLifecycleFence || response.Code != http.StatusUnauthorized || sub2API.createCalls != 0 || len(operations) != 0 {
		t.Fatalf("launch crossed owner lifecycle fence: crossed=%t status=%d body=%s creates=%d operations=%#v", claimCrossedLifecycleFence, response.Code, response.Body.String(), sub2API.createCalls, operations)
	}
}

func TestWorkspaceLaunchListAndDetailAreTenantScoped(t *testing.T) {
	fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
	created := fixture.launch(t, `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`, "launch-alpha")
	if created.Code != http.StatusAccepted {
		t.Fatalf("launch status = %d: %s", created.Code, created.Body.String())
	}
	var launch map[string]any
	if err := json.NewDecoder(created.Body).Decode(&launch); err != nil {
		t.Fatal(err)
	}
	if launch["autoRenew"] != false || launch["priceVersion"] != pilotPriceVersion || launch["totalChargeUsdMicros"] != float64(52_580_000) || strings.Contains(created.Body.String(), "pricingVersion") || strings.Contains(created.Body.String(), "totalMonthlyPriceCnyCents") {
		t.Fatalf("created launch projection = %#v", launch)
	}
	operationID := stringValue(launch["operationId"])

	alphaList := requestWithSession(t, fixture.server, fixture.session, http.MethodGet, "/api/workspace-launches", "")
	if alphaList.Code != http.StatusOK || !strings.Contains(alphaList.Body.String(), operationID) || !strings.Contains(alphaList.Body.String(), `"autoRenew":false`) || !strings.Contains(alphaList.Body.String(), `"priceVersion":"pilot-usd-2026-07-v1"`) || strings.Contains(alphaList.Body.String(), "usr-alpha") || strings.Contains(alphaList.Body.String(), "pricingVersion") {
		t.Fatalf("alpha launch list status=%d body=%s", alphaList.Code, alphaList.Body.String())
	}
	alphaDetail := requestWithSession(t, fixture.server, fixture.session, http.MethodGet, "/api/workspace-launches/"+operationID, "")
	if alphaDetail.Code != http.StatusOK || !strings.Contains(alphaDetail.Body.String(), operationID) || !strings.Contains(alphaDetail.Body.String(), `"autoRenew":false`) || !strings.Contains(alphaDetail.Body.String(), `"priceVersion":"pilot-usd-2026-07-v1"`) || strings.Contains(alphaDetail.Body.String(), "pricingVersion") {
		t.Fatalf("alpha launch detail status=%d body=%s", alphaDetail.Code, alphaDetail.Body.String())
	}
	var detail map[string]any
	if err := json.Unmarshal(alphaDetail.Body.Bytes(), &detail); err != nil || stringValue(detail["operationId"]) != operationID {
		t.Fatalf("alpha launch detail must be one complete JSON object: detail=%#v err=%v body=%q", detail, err, alphaDetail.Body.String())
	}

	seedTenantMember(t, fixture.store, "acct-beta", "org-beta", "usr-beta", "beta@example.com")
	betaSession := loginForTest(t, fixture.server, "beta@example.com", "CorrectHorseBatteryStaple!")
	betaList := requestWithSession(t, fixture.server, betaSession, http.MethodGet, "/api/workspace-launches", "")
	if betaList.Code != http.StatusOK || strings.TrimSpace(betaList.Body.String()) != "[]" {
		t.Fatalf("beta launch list status=%d body=%s", betaList.Code, betaList.Body.String())
	}
	for _, id := range []string{operationID, "launch-missing"} {
		response := requestWithSession(t, fixture.server, betaSession, http.MethodGet, "/api/workspace-launches/"+id, "")
		if response.Code != http.StatusNotFound {
			t.Fatalf("beta launch detail %s status=%d, want 404: %s", id, response.Code, response.Body.String())
		}
	}
}

func TestWorkspaceLaunchWorkerDefaultsToTenSecondsAndRunsIndependently(t *testing.T) {
	if defaultWorkspaceLaunchInterval != 10*time.Second {
		t.Fatalf("default interval=%s", defaultWorkspaceLaunchInterval)
	}
	t.Setenv("OPL_WORKSPACE_LAUNCH_WORKER_ENABLED", "true")
	t.Setenv("OPL_WORKSPACE_LAUNCH_INTERVAL_MS", "25")
	if !workspaceLaunchWorkerEnabled() || workspaceLaunchWorkerInterval() != 25*time.Millisecond {
		t.Fatalf("worker enabled=%t interval=%s", workspaceLaunchWorkerEnabled(), workspaceLaunchWorkerInterval())
	}
}

type recordingWorkspaceLaunchStore struct {
	*memoryTableStore
	lifecycleStarted           chan struct{}
	releaseLifecycle           chan struct{}
	lifecycleSignal            sync.Once
	workspaceLaunchClaimed     chan struct{}
	workspaceLaunchClaimSignal sync.Once
	workspaceLaunchPersistErr  error
	activationErr              error
	activationPersistBeforeErr bool
	activationCalls            int
}

func (s *recordingWorkspaceLaunchStore) ApplyUserLifecycle(ctx context.Context, user map[string]any) error {
	if s.lifecycleStarted != nil {
		s.lifecycleSignal.Do(func() {
			close(s.lifecycleStarted)
			<-s.releaseLifecycle
		})
	}
	return s.memoryTableStore.ApplyUserLifecycle(ctx, user)
}

func (s *recordingWorkspaceLaunchStore) ClaimWorkspaceLaunch(ctx context.Context, claim workspaceLaunchClaimCAS) error {
	if s.workspaceLaunchClaimed != nil {
		s.workspaceLaunchClaimSignal.Do(func() { close(s.workspaceLaunchClaimed) })
	}
	return s.memoryTableStore.ClaimWorkspaceLaunch(ctx, claim)
}

func (s *recordingWorkspaceLaunchStore) PersistWorkspaceLaunch(ctx context.Context, update workspaceLaunchPersistCAS) error {
	if s.workspaceLaunchPersistErr != nil {
		return s.workspaceLaunchPersistErr
	}
	return s.memoryTableStore.PersistWorkspaceLaunch(ctx, update)
}

func (s *recordingWorkspaceLaunchStore) ActivateWorkspace(ctx context.Context, row map[string]any) (map[string]any, error) {
	s.activationCalls++
	if s.activationErr != nil && !s.activationPersistBeforeErr {
		return nil, s.activationErr
	}
	activated, err := s.memoryTableStore.ActivateWorkspace(ctx, row)
	if err != nil {
		return nil, err
	}
	if s.activationErr != nil {
		return activated, s.activationErr
	}
	return activated, nil
}

type workspaceLaunchLedger struct {
	fakeLedgerClient
	events                *[]string
	receipts              map[string]clients.Receipt
	receiptInputs         []clients.ReceiptInput
	receiptErrors         []error
	persistReceiptOnError bool
	workspaceReceiptCalls int
}

func (l *workspaceLaunchLedger) RecordReceipt(_ context.Context, input clients.ReceiptInput, key string) (clients.Receipt, error) {
	l.receiptInputs = append(l.receiptInputs, input)
	if input.Type == "workspace.created" {
		*l.events = append(*l.events, "ledger.workspace.receipt")
		l.workspaceReceiptCalls++
	}
	receipt := clients.Receipt{ReceiptInput: input, ReceiptID: "receipt-" + stableID(key)[:12]}
	if len(l.receiptErrors) > 0 {
		err := l.receiptErrors[0]
		l.receiptErrors = l.receiptErrors[1:]
		if err != nil {
			if l.persistReceiptOnError {
				l.receipts[key] = receipt
			}
			return clients.Receipt{}, err
		}
	}
	if receipt, ok := l.receipts[key]; ok {
		return receipt, nil
	}
	l.receipts[key] = receipt
	return receipt, nil
}

func (l *workspaceLaunchLedger) ListReceipts(_ context.Context, query clients.ReceiptQuery) (clients.ReceiptPage, error) {
	receipts := make([]clients.Receipt, 0, len(l.receipts))
	for _, receipt := range l.receipts {
		if receipt.AccountID == query.AccountID {
			receipts = append(receipts, receipt)
		}
	}
	return clients.ReceiptPage{Receipts: receipts}, nil
}

type workspaceLaunchSub2API struct {
	*monthlySub2API
	keys        map[int64]clients.Sub2APIWorkspaceKey
	createCalls int
	userKeysErr error
}

type durableWorkspaceLaunchSub2API struct {
	*workspaceLaunchSub2API
	mu                sync.Mutex
	balance           int64
	chargeCalls       []clients.Sub2APIChargeInput
	appliedCharges    map[string]clients.Sub2APIChargeInput
	loseNextResponses int
}

func (s *durableWorkspaceLaunchSub2API) Balance(_ context.Context, userID int64) (clients.Sub2APIBalance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	*s.events = append(*s.events, "sub2api.balance")
	return clients.Sub2APIBalance{UserID: userID, USDMicros: s.balance, Status: "active"}, nil
}

func (s *durableWorkspaceLaunchSub2API) Charge(_ context.Context, input clients.Sub2APIChargeInput) (clients.Sub2APICharge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	*s.events = append(*s.events, "sub2api.charge")
	s.chargeCalls = append(s.chargeCalls, input)
	if existing, ok := s.appliedCharges[input.Code]; ok {
		if existing.UserID != input.UserID || existing.ChargeUSDMicros != input.ChargeUSDMicros {
			return clients.Sub2APICharge{}, clients.ErrSub2APIChargeConflict
		}
		return clients.Sub2APICharge{Code: input.Code, UserID: input.UserID, ChargeUSDMicros: input.ChargeUSDMicros, Status: "used"}, nil
	}
	if input.ChargeUSDMicros <= 0 || input.ChargeUSDMicros > s.balance {
		return clients.Sub2APICharge{}, errMonthlyInsufficientBalance
	}
	s.balance -= input.ChargeUSDMicros
	s.appliedCharges[input.Code] = input
	if s.loseNextResponses > 0 {
		s.loseNextResponses--
		return clients.Sub2APICharge{}, clients.ErrSub2APIChargeUnknown
	}
	return clients.Sub2APICharge{Code: input.Code, UserID: input.UserID, ChargeUSDMicros: input.ChargeUSDMicros, Status: "used"}, nil
}

func (s *durableWorkspaceLaunchSub2API) Usage(context.Context, clients.Sub2APIUsageQuery) (clients.Sub2APIUsagePage, error) {
	return clients.Sub2APIUsagePage{}, nil
}

func (s *durableWorkspaceLaunchSub2API) UsageStats(context.Context, clients.Sub2APIUsageStatsQuery) (clients.Sub2APIUsageStats, error) {
	return clients.Sub2APIUsageStats{}, nil
}

func (s *durableWorkspaceLaunchSub2API) FinancialBalanceHistoryByCodes(_ context.Context, userID int64, codes []string) (map[string]clients.Sub2APIBalanceHistoryEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	usedAt := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	matches := make(map[string]clients.Sub2APIBalanceHistoryEntry)
	for _, input := range s.appliedCharges {
		for _, code := range codes {
			if input.Code == code {
				usedBy := userID
				matches[code] = clients.Sub2APIBalanceHistoryEntry{Code: input.Code, Type: "balance", ValueUSDMicros: -input.ChargeUSDMicros, Status: "used", UsedBy: &usedBy, UsedAt: &usedAt, CreatedAt: usedAt}
			}
		}
	}
	return matches, nil
}

type workspaceLaunchClaimBarrierStore struct {
	*memoryTableStore
	mu      sync.Mutex
	armed   bool
	waiting int
	release chan struct{}
}

type workspaceLaunchKeyPersistBarrierStore struct {
	*memoryTableStore
	mu      sync.Mutex
	waiting int
	release chan struct{}
}

func (s *workspaceLaunchKeyPersistBarrierStore) PersistWorkspaceLaunch(ctx context.Context, update workspaceLaunchPersistCAS) error {
	s.mu.Lock()
	s.waiting++
	if s.waiting == 2 {
		close(s.release)
	}
	release := s.release
	s.mu.Unlock()
	<-release
	return s.memoryTableStore.PersistWorkspaceLaunch(ctx, update)
}

func (s *workspaceLaunchClaimBarrierStore) ClaimWorkspaceLaunch(ctx context.Context, claim workspaceLaunchClaimCAS) error {
	s.mu.Lock()
	if !s.armed {
		s.mu.Unlock()
		return s.memoryTableStore.ClaimWorkspaceLaunch(ctx, claim)
	}
	s.waiting++
	if s.waiting == 2 {
		close(s.release)
	}
	release := s.release
	s.mu.Unlock()
	<-release
	return s.memoryTableStore.ClaimWorkspaceLaunch(ctx, claim)
}

func (s *workspaceLaunchSub2API) WorkspaceKey(ctx context.Context, userID int64) (clients.Sub2APIWorkspaceKey, error) {
	*s.events = append(*s.events, "sub2api.workspace_key")
	return s.monthlySub2API.WorkspaceKey(ctx, userID)
}

func (s *workspaceLaunchSub2API) WorkspaceKeysForConvergence(_ context.Context, userID int64, name string) ([]clients.Sub2APIWorkspaceKey, error) {
	keys := make([]clients.Sub2APIWorkspaceKey, 0, len(s.keys))
	for _, key := range s.keys {
		if key.UserID == userID && key.Name == name {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func (s *workspaceLaunchSub2API) WorkspaceUserKeysForConvergence(_ context.Context, credential clients.SessionDelegatedCredential, userID int64, name string) ([]clients.Sub2APIWorkspaceKey, error) {
	*s.events = append(*s.events, "sub2api.user_keys")
	if credential.Bearer != "test-user-delegated-token" {
		return nil, errors.New("wrong delegated credential")
	}
	if s.userKeysErr != nil {
		return nil, s.userKeysErr
	}
	keys := make([]clients.Sub2APIWorkspaceKey, 0, len(s.keys))
	for _, key := range s.keys {
		if key.UserID == userID && key.Name == name {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func (s *workspaceLaunchSub2API) UserKey(_ context.Context, credential clients.SessionDelegatedCredential, userID, keyID int64) (clients.Sub2APIWorkspaceKey, error) {
	if credential.Bearer != "test-user-delegated-token" {
		return clients.Sub2APIWorkspaceKey{}, errors.New("wrong delegated credential")
	}
	key, ok := s.keys[keyID]
	if !ok || key.UserID != userID {
		return clients.Sub2APIWorkspaceKey{}, clients.ErrSub2APIKeyNotFound
	}
	return key, nil
}

func (s *workspaceLaunchSub2API) CreateUserKey(_ context.Context, credential clients.SessionDelegatedCredential, userID int64, input clients.Sub2APICreateKeyInput, idempotencyKey string) (clients.Sub2APIWorkspaceKey, error) {
	*s.events = append(*s.events, "sub2api.create_workspace_key")
	if credential.Bearer != "test-user-delegated-token" || idempotencyKey == "" || !strings.HasPrefix(input.Name, "opl-workspace-") || input.Name == "opl-workspace" || input.ExpiresInDays != nil {
		return clients.Sub2APIWorkspaceKey{}, errors.New("invalid Workspace Key create")
	}
	s.createCalls++
	key := clients.Sub2APIWorkspaceKey{ID: int64(18 + s.createCalls), UserID: userID, Name: input.Name, Key: "created-workspace-key-secret", Status: "active"}
	if s.keys == nil {
		s.keys = map[int64]clients.Sub2APIWorkspaceKey{}
	}
	s.keys[key.ID] = key
	return key, nil
}

func (s *workspaceLaunchSub2API) UpdateUserKey(context.Context, clients.SessionDelegatedCredential, int64, int64, clients.Sub2APIUpdateKeyInput) (clients.Sub2APIWorkspaceKey, error) {
	return clients.Sub2APIWorkspaceKey{}, errors.New("unexpected Workspace Key update")
}

func (s *workspaceLaunchSub2API) DeleteUserKey(context.Context, clients.SessionDelegatedCredential, int64, int64) error {
	return errors.New("unexpected Workspace Key delete")
}

func (s *workspaceLaunchSub2API) Usage(context.Context, clients.Sub2APIUsageQuery) (clients.Sub2APIUsagePage, error) {
	return clients.Sub2APIUsagePage{}, nil
}

func (s *workspaceLaunchSub2API) UsageStats(context.Context, clients.Sub2APIUsageStatsQuery) (clients.Sub2APIUsageStats, error) {
	return clients.Sub2APIUsageStats{}, nil
}

func (s *workspaceLaunchSub2API) FinancialBalanceHistoryByCodes(_ context.Context, userID int64, codes []string) (map[string]clients.Sub2APIBalanceHistoryEntry, error) {
	usedAt := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	matches := make(map[string]clients.Sub2APIBalanceHistoryEntry)
	for _, charge := range s.charges {
		for _, code := range codes {
			if charge.Code == code {
				usedBy := userID
				matches[code] = clients.Sub2APIBalanceHistoryEntry{Code: charge.Code, Type: "balance", ValueUSDMicros: -charge.ChargeUSDMicros, Status: "used", UsedBy: &usedBy, UsedAt: &usedAt, CreatedAt: usedAt}
			}
		}
	}
	return matches, nil
}

type workspaceLaunchWorkerFixture struct {
	app         *controlPlaneServer
	service     *controlplane.Service
	server      http.Handler
	operator    *httptest.ResponseRecorder
	store       *recordingWorkspaceLaunchStore
	events      *[]string
	sub2API     *workspaceLaunchSub2API
	fabric      *monthlyFabric
	ledger      *workspaceLaunchLedger
	operationID string
}

func newWorkspaceLaunchWorkerFixture(t *testing.T, balances []int64, chargeErrors []error, runtimeErr error, autoRenew ...bool) workspaceLaunchWorkerFixture {
	renew := len(autoRenew) != 0 && autoRenew[0]
	return newWorkspaceLaunchWorkerFixtureForPlan(t, balances, chargeErrors, runtimeErr, "basic", 10, renew)
}

func newWorkspaceLaunchWorkerFixtureForPlan(t *testing.T, balances []int64, chargeErrors []error, runtimeErr error, packageID string, storageGB int, autoRenew bool) workspaceLaunchWorkerFixture {
	t.Helper()
	if currentWorkspaceImageDigest() == "" {
		t.Setenv("OPL_WORKSPACE_IMAGE", workspaceImageRepository+"@sha256:"+strings.Repeat("f", 64))
	}
	t.Setenv("OPL_MONTHLY_BILLING_WORKER_ENABLED", "false")
	t.Setenv("OPL_PROVIDER_RECONCILE_WORKER_ENABLED", "false")
	t.Setenv("OPL_ARCHIVE_RETENTION_WORKER_ENABLED", "false")
	store := &recordingWorkspaceLaunchStore{memoryTableStore: newMemoryTableStore()}
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	promoteWorkspaceLaunchOwner(t, store, "usr-alpha")
	events := []string{}
	sub2API := &workspaceLaunchSub2API{monthlySub2API: &monthlySub2API{events: &events, balances: balances, chargeErrors: chargeErrors}}
	fabric := &monthlyFabric{fakeFabricClient: fakeFabricClient{calls: &events, runtimeErr: runtimeErr}, events: &events}
	ledger := &workspaceLaunchLedger{events: &events, receipts: map[string]clients.Receipt{}}
	service := controlplane.NewService(ledger, fabric, sub2API)
	server, err := NewPersistentServer(service, store)
	if err != nil {
		t.Fatal(err)
	}
	session := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	created := requestWithMutationKeyForTest(t, server, session, http.MethodPost, "/api/workspace-launches", fmt.Sprintf(`{"name":"Alpha","packageId":%q,"sizeGb":%d,"autoRenew":false}`, packageID, storageGB), "launch-alpha")
	if created.Code != http.StatusAccepted {
		t.Fatalf("launch status = %d: %s", created.Code, created.Body.String())
	}
	var response map[string]any
	if err := json.NewDecoder(created.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	operationID := stringValue(response["operationId"])
	if autoRenew {
		operations, err := store.ListRuntimeOperations(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		operation, err := decodeWorkspaceLaunchOperation(recordByID(operations, operationID))
		if err != nil {
			t.Fatal(err)
		}
		operation.AutoRenew = true
		operation.RequestHash = newWorkspaceLaunchOperation(operation.AccountID, operation.OwnerUserID, operation.Name, operation.PackageID, operation.StorageGB, true, operation.PriceVersion, operation.TotalChargeUSDMicros, "launch-alpha").RequestHash
		mustStore(t, store.memoryTableStore.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(operation)))
	}
	app, err := newControlPlaneAppWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	return workspaceLaunchWorkerFixture{
		app: app, service: service, server: server, operator: reservedOperatorSessionForTest(t, server), store: store, events: &events, sub2API: sub2API, fabric: fabric, ledger: ledger,
		operationID: operationID,
	}
}

func TestWorkspaceLaunchSingleTotalDebit(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	rows, err := fixture.store.ListRuntimeOperations(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("launch rows=%#v err=%v", rows, err)
	}
	var persisted map[string]any
	if err := json.Unmarshal([]byte(stringValue(rows[0]["result"])), &persisted); err != nil {
		t.Fatal(err)
	}
	operation := fixture.operation(t)
	if stringValue(rows[0]["action"]) != "workspace.launch.v2" || persisted["schemaVersion"] != float64(2) || operation.Status != "debited" || operation.Phase != "debited" {
		t.Fatalf("debited launch row=%#v operation=%#v", rows[0], operation)
	}
	if len(fixture.sub2API.charges) != 1 || fixture.sub2API.charges[0].ChargeUSDMicros != 52_580_000 {
		t.Fatalf("Workspace debit calls=%#v", fixture.sub2API.charges)
	}
	if len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 || countStrings(*fixture.events, "fabric.attachment") != 0 || countStrings(*fixture.events, "fabric.runtime") != 0 {
		t.Fatalf("request crossed fulfillment gate: events=%#v", *fixture.events)
	}
}

func TestWorkspaceLaunchPersistsDiscoveredNodePoolBeforeCharge(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000}, nil, nil)
	operation := fixture.operation(t)
	var persisted map[string]any
	if err := json.Unmarshal([]byte(operation.PersistedResult), &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted["computeNodePoolId"] != "np-basic" {
		t.Fatalf("discovered NodePoolID was not persisted before charge: %#v", persisted)
	}
}

func TestWorkspaceLaunchRejectsEqualBalanceBeforeCharge(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{100_000_000, 52_580_000, 0}, nil, nil)
	err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service)
	operation := fixture.operation(t)
	if !errors.Is(err, errMonthlyInsufficientBalance) || operation.Status != "insufficient" || operation.Phase != "debit_pending" ||
		len(fixture.sub2API.charges) != 0 || len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 {
		t.Fatalf("equal balance crossed debit gate: err=%v operation=%#v charges=%#v compute=%#v storage=%#v", err, operation, fixture.sub2API.charges, fixture.fabric.computeIDs, fixture.fabric.storageIDs)
	}
}

func TestWorkspaceLaunchConcurrentUsageDoesNotInvalidateUniqueRedeemHistory(t *testing.T) {
	for _, plan := range []struct {
		packageID string
		sizeGB    int
		charge    int64
	}{
		{packageID: "basic", sizeGB: 10, charge: 52_580_000},
		{packageID: "pro", sizeGB: 100, charge: 240_080_000},
	} {
		t.Run(plan.packageID, func(t *testing.T) {
			preBalance := plan.charge + 100_000_000
			fixture := newWorkspaceLaunchWorkerFixtureForPlan(t, []int64{preBalance, preBalance, 40_000_000}, nil, nil, plan.packageID, plan.sizeGB, false)
			if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
				t.Fatalf("unique redeem history should authorize monthly charge despite concurrent Usage: %v", err)
			}
			operation := fixture.operation(t)
			if operation.Status != "debited" || operation.Phase != "debited" || len(fixture.sub2API.charges) != 1 || fixture.sub2API.charges[0].ChargeUSDMicros != plan.charge || operation.ErrorCode != "" {
				t.Fatalf("concurrent Usage incorrectly blocked launch: operation=%#v charges=%#v", operation, fixture.sub2API.charges)
			}
		})
	}
}

func TestWorkspaceLaunchUniqueRedeemHistoryDoesNotDependOnPostBalanceRead(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{100_000_000, 100_000_000}, nil, nil)
	fixture.sub2API.balanceErrors = []error{nil, errors.New("post charge balance unavailable")}
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatalf("unique redeem history must remain the charge authority: %v", err)
	}
	operation := fixture.operation(t)
	if operation.Status != "debited" || operation.Phase != "debited" || len(fixture.sub2API.charges) != 1 || operation.ErrorCode != "" ||
		operation.PostChargeBalanceKnown || !workspaceLaunchChargeConfirmed(operation, 41) || operation.BillingPeriodState != "frozen" {
		t.Fatalf("post balance read incorrectly blocked authoritative charge: operation=%#v charges=%#v", operation, fixture.sub2API.charges)
	}
	_ = fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service)
	continued := fixture.operation(t)
	if continued.ErrorCode == "post_charge_balance_unavailable" || continued.ErrorCode == "post_charge_balance_invalid" || len(fixture.sub2API.charges) != 1 {
		t.Fatalf("post-balance projection failure blocked fulfillment: operation=%#v charges=%#v", continued, fixture.sub2API.charges)
	}
	operation.Status, operation.Phase = "manual_review", "compute_fulfilling"
	mustStore(t, fixture.store.memoryTableStore.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(operation)))
	restarted, err := newControlPlaneAppWithStore(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	replayed := fixture.operation(t)
	if !workspaceLaunchChargeConfirmed(replayed, 41) || replayed.PeriodStart != operation.PeriodStart || replayed.PaidThrough != operation.PaidThrough || len(fixture.sub2API.charges) != 1 {
		t.Fatalf("restart lost authoritative charge/period: replayed=%#v charges=%#v", replayed, fixture.sub2API.charges)
	}
	_ = restarted
}

func TestWorkspaceLaunchInvalidImageDigestStopsBeforeAnyExternalCall(t *testing.T) {
	validDigest := "sha256:" + strings.Repeat("a", 64)
	for name, image := range map[string]string{
		"missing":          "",
		"bare digest":      validDigest,
		"tag-only":         "uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app:latest",
		"wrong repository": "registry.example/one-person-lab-app@" + validDigest,
		"repository drift": "uswccr.ccs.tencentyun.com/other/one-person-lab-app@" + validDigest,
		"uppercase digest": "uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app@sha256:" + strings.Repeat("A", 64),
		"invalid":          "uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app@sha256:not-a-digest",
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newWorkspaceLaunchHTTPFixture(t, 1_000_000_000)
			t.Setenv("OPL_WORKSPACE_IMAGE", image)
			response := fixture.launch(t, `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`, "launch-invalid-image")
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "workspace_image_digest_invalid") {
				t.Fatalf("invalid image digest status=%d body=%s", response.Code, response.Body.String())
			}
			operations, err := fixture.store.ListRuntimeOperations(context.Background())
			if err != nil || len(operations) != 0 || len(*fixture.events) != 0 || len(fixture.sub2API.charges) != 0 || fixture.sub2API.createCalls != 0 || len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 {
				t.Fatalf("invalid image digest crossed mutation gate: operations=%#v events=%#v charges=%#v keys=%d compute=%#v storage=%#v", operations, *fixture.events, fixture.sub2API.charges, fixture.sub2API.createCalls, fixture.fabric.computeIDs, fixture.fabric.storageIDs)
			}
		})
	}
}

func TestWorkspaceLaunchImageDigestDriftStopsBeforeChargeAndProviderWrite(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000}, nil, nil)
	operation := fixture.operation(t)
	t.Setenv("OPL_WORKSPACE_IMAGE", workspaceImageRepository+"@sha256:"+strings.Repeat("e", 64))
	err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service)
	after := fixture.operation(t)
	if err == nil || after.Status != "unknown" || after.ErrorCode != "workspace_image_digest_drift" || after.WorkspaceImageDigest != operation.WorkspaceImageDigest ||
		len(fixture.sub2API.charges) != 0 || len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 {
		t.Fatalf("image drift crossed charge/provider gate: err=%v operation=%#v charges=%#v compute=%#v storage=%#v", err, after, fixture.sub2API.charges, fixture.fabric.computeIDs, fixture.fabric.storageIDs)
	}
}

func TestWorkspaceLaunchPeriodFreezeSurvivesWorkerRestart(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	before := fixture.operation(t)
	if before.PeriodStart != "" || before.PaidThrough != "" || before.BillingAnchorDay != 0 {
		t.Fatalf("billing period froze before authoritative charge confirmation: %#v", before)
	}
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	charged := fixture.operation(t)
	periodStart, startErr := time.Parse(time.RFC3339, charged.PeriodStart)
	paidThrough, paidErr := time.Parse(time.RFC3339, charged.PaidThrough)
	wantPeriodStart := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	if charged.Status != "debited" || startErr != nil || paidErr != nil || !periodStart.Equal(wantPeriodStart) || !paidThrough.Equal(nextBillingMonth(periodStart, periodStart.Day())) || charged.BillingAnchorDay != periodStart.Day() {
		t.Fatalf("authoritative charge did not freeze one billing period: before=%#v after=%#v startErr=%v paidErr=%v", before, charged, startErr, paidErr)
	}
	charged.Status, charged.Phase = "manual_review", "compute_fulfilling"
	mustStore(t, fixture.store.memoryTableStore.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(charged)))
	restarted, err := newControlPlaneAppWithStore(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	_ = restarted
	after := fixture.operation(t)
	if after.PeriodStart != charged.PeriodStart || after.PaidThrough != charged.PaidThrough || after.BillingAnchorDay != charged.BillingAnchorDay {
		t.Fatalf("worker restart recomputed billing period: charged=%#v after=%#v", charged, after)
	}
}

func TestWorkspaceLaunchWorkerRechecksProviderPreflightBeforeFirstCharge(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{100_000_000, 100_000_000, 47_420_000}, nil, nil)
	fixture.fabric.preflightResults = []clients.MonthlyPreflight{{}, {}}
	err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service)
	operation := fixture.operation(t)
	if err == nil || operation.Status != "unknown" || operation.Phase != "debit_pending" || operation.ErrorCode != "fabric_compute_preflight_failed" ||
		len(fixture.sub2API.charges) != 0 || len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 {
		t.Fatalf("worker skipped preflight gate: err=%v operation=%#v charges=%#v compute=%#v storage=%#v", err, operation, fixture.sub2API.charges, fixture.fabric.computeIDs, fixture.fabric.storageIDs)
	}
}

func TestWorkspaceLaunchWriteDisabledPreflightStopsBeforeChargeAndFabricMutation(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{100_000_000, 100_000_000, 47_420_000}, nil, nil)
	fixture.fabric.preflightErr = errors.New("live_mutation_flag_required")
	err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service)
	operation := fixture.operation(t)
	if err == nil || operation.Status != "unknown" || operation.Phase != "debit_pending" || operation.ErrorCode != "fabric_compute_preflight_failed" ||
		len(fixture.sub2API.charges) != 0 || len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 ||
		countStrings(*fixture.events, "fabric.attachment") != 0 || countStrings(*fixture.events, "fabric.runtime") != 0 {
		t.Fatalf("disabled Tencent writes crossed preflight gate: err=%v operation=%#v charges=%#v events=%#v", err, operation, fixture.sub2API.charges, *fixture.events)
	}
}

func TestWorkspaceLaunchWorkerRejectsChangedDiscoveredNodePoolBeforeFirstCharge(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{100_000_000, 100_000_000, 47_420_000}, nil, nil)
	zone := monthlyComputeLaunchZone()
	fixture.fabric.preflightResults = []clients.MonthlyPreflight{
		monthlyPreflightResult(clients.MonthlyPreflightInput{ResourceType: "compute", PackageID: "basic", Zone: zone}, "np-other"),
		monthlyPreflightResult(clients.MonthlyPreflightInput{ResourceType: "storage", PackageID: "basic", SizeGB: 10, Zone: zone}, ""),
	}
	err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service)
	operation := fixture.operation(t)
	if err == nil || operation.Status != "unknown" || operation.Phase != "debit_pending" || operation.ErrorCode != "fabric_compute_preflight_failed" ||
		len(fixture.sub2API.charges) != 0 || len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 {
		t.Fatalf("changed NodePoolID crossed charge gate: err=%v operation=%#v charges=%#v compute=%#v storage=%#v", err, operation, fixture.sub2API.charges, fixture.fabric.computeIDs, fixture.fabric.storageIDs)
	}
}

func TestWorkspaceLaunchActivationReadsProviderTruthAgain(t *testing.T) {
	workspaceImageDigest := workspaceImageRepository + "@sha256:" + strings.Repeat("a", 64)
	t.Setenv("OPL_WORKSPACE_IMAGE", workspaceImageDigest)
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	operation := configureWorkspaceLaunchFulfillment(t, fixture)
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	if countStrings(*fixture.events, "fabric.workspace-activation-truth") != 1 || countStrings(*fixture.events, "fabric.compute.sync") != 1 ||
		countStrings(*fixture.events, "fabric.storage.get") != 1 || countStrings(*fixture.events, "fabric.storage.sync") != 0 {
		t.Fatalf("activation did not use the single read-only truth gate: events=%#v", *fixture.events)
	}
	if len(fixture.fabric.activationTruthInputs) != 1 {
		t.Fatalf("activation truth inputs=%#v", fixture.fabric.activationTruthInputs)
	}
	input := fixture.fabric.activationTruthInputs[0]
	if input.LaunchOperationID != operation.ID || input.AccountID != operation.AccountID || input.WorkspaceID != operation.WorkspaceID ||
		input.ComputeAllocationID != operation.ComputeID || input.ComputeOperationID != operation.ID+":compute" ||
		input.StorageVolumeID != operation.StorageID || input.StorageOperationID != operation.ID+":storage" ||
		input.AttachmentID == "" || input.AttachmentOperationID != operation.AttachmentOperationID ||
		input.RuntimeID == "" || input.RuntimeOperationID != operation.WorkspaceOperationID+":runtime" ||
		input.ServiceName == "" || input.WorkspaceImageDigest != workspaceImageDigest ||
		input.GatewaySecretRef == "" || input.WorkspaceAPIKeyID != operation.WorkspaceAPIKeyID || input.GatewaySecretFingerprint == "" {
		t.Fatalf("activation truth identity=%#v operation=%#v", input, operation)
	}
}

func TestWorkspaceLaunchActivationTruthFailureStopsBeforeWorkspaceAndReceipt(t *testing.T) {
	t.Setenv("OPL_WORKSPACE_IMAGE", workspaceImageRepository+"@sha256:"+strings.Repeat("b", 64))
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	operation := configureWorkspaceLaunchFulfillment(t, fixture)
	fixture.fabric.activationTruth = &clients.WorkspaceActivationTruth{
		SchemaVersion: 1, Ready: false, Reason: "identity_mismatch", ErrorClass: "readback_mismatch",
		ComputeState: "ready", StorageState: "ready", Checks: []any{},
	}
	fixture.fabric.activationTruthErr = errors.New("workspace activation truth unavailable")
	for range 2 {
		_ = fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service)
	}
	current := fixture.operation(t)
	workspaces, _ := fixture.store.ListWorkspaces(context.Background(), operation.AccountID)
	if current.Status != "manual_review" || current.Phase != "activating" || current.ErrorCode != "workspace_launch_activation_truth_identity_mismatch" ||
		len(workspaces) != 0 || len(fixture.ledger.receiptInputs) != 0 || countStrings(*fixture.events, "fabric.workspace-activation-truth") != 1 {
		t.Fatalf("activation truth failure crossed gate: operation=%#v workspaces=%#v receipts=%#v events=%#v", current, workspaces, fixture.ledger.receiptInputs, *fixture.events)
	}
}

func configureWorkspaceLaunchFulfillment(t *testing.T, fixture workspaceLaunchWorkerFixture) workspaceLaunchOperation {
	t.Helper()
	operation := fixture.operation(t)
	instanceType := "S5.MEDIUM4"
	if operation.PackageID == "pro" {
		instanceType = "SA5.2XLARGE16"
	}
	fixture.fabric.computeSync = clients.ComputeAllocation{
		ID: operation.ComputeID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, PackageID: operation.PackageID,
		Status: "running", Provider: "tencent-tke", ProviderResourceID: "ins-" + operation.ComputeID, ProviderRequestID: "req-" + operation.ComputeID,
		InstanceID: "ins-" + operation.ComputeID, InstanceType: instanceType, Zone: "ap-shanghai-2", ChargeType: "PREPAID",
		RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2099-01-01T00:00:00Z", ProviderData: map[string]string{"zone": "ap-shanghai-2", "instanceType": instanceType},
	}
	fixture.fabric.storageSync = clients.StorageVolume{
		ID: operation.StorageID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, Status: "available",
		Provider: "tencent-tke", ProviderResourceID: "disk-" + operation.StorageID, ProviderRequestID: "req-" + operation.StorageID,
		SizeGB: operation.StorageGB, CBSStatus: "UNATTACHED", DiskType: "CLOUD_PREMIUM", RenewFlag: "NOTIFY_AND_MANUAL_RENEW",
		Deadline: "2099-01-01T00:00:00Z", Zone: "ap-shanghai-2", ProviderData: map[string]string{"chargeType": "PREPAID"},
	}
	return operation
}

func TestWorkspaceLaunchFulfillmentOnly(t *testing.T) {
	for _, test := range []struct {
		packageID string
		storageGB int
		total     int64
	}{
		{packageID: "basic", storageGB: 10, total: 52_580_000},
		{packageID: "pro", storageGB: 100, total: 240_080_000},
	} {
		t.Run(test.packageID, func(t *testing.T) {
			fixture := newWorkspaceLaunchWorkerFixtureForPlan(t, []int64{1_000_000_000, 1_000_000_000, 1_000_000_000 - test.total}, nil, nil, test.packageID, test.storageGB, false)
			configureWorkspaceLaunchFulfillment(t, fixture)
			if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
				t.Fatal(err)
			}
			if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
				t.Fatal(err)
			}

			operation := fixture.operation(t)
			if operation.Status != "succeeded" || operation.Phase != "succeeded" || operation.AttachmentID == "" || operation.RuntimeServiceName == "" || operation.URL == "" {
				t.Fatalf("fulfilled launch=%#v", operation)
			}
			if len(fixture.sub2API.charges) != 1 || fixture.sub2API.charges[0].ChargeUSDMicros != test.total || len(fixture.sub2API.refunds) != 0 {
				t.Fatalf("Workspace billing calls: charges=%#v refunds=%#v", fixture.sub2API.charges, fixture.sub2API.refunds)
			}
			if len(fixture.fabric.computeIDs) != 1 || len(fixture.fabric.storageIDs) != 1 || countStrings(*fixture.events, "fabric.compute.sync") != 1 ||
				countStrings(*fixture.events, "fabric.storage.get") != 1 || countStrings(*fixture.events, "fabric.storage.sync") != 0 ||
				countStrings(*fixture.events, "fabric.attachment") != 1 || countStrings(*fixture.events, "fabric.gateway-secret") != 1 || countStrings(*fixture.events, "fabric.runtime") != 1 ||
				countStrings(*fixture.events, "fabric.workspace-activation-truth") != 1 {
				t.Fatalf("fulfillment events=%#v", *fixture.events)
			}
			assertWorkspaceLaunchRuntimeIdentity(t, fixture.fabric.runtimeInputs, operation)
			computes, _ := fixture.store.ListComputes(context.Background(), operation.AccountID)
			storages, _ := fixture.store.ListStorages(context.Background(), operation.AccountID)
			if len(computes) != 1 || len(storages) != 1 {
				t.Fatalf("fulfilled resources: computes=%#v storages=%#v", computes, storages)
			}
			for _, row := range []map[string]any{computes[0], storages[0]} {
				for _, forbidden := range []string{"billingOperationId", "sub2apiRedeemCode", "chargeUsdMicros", "priceVersion"} {
					if _, ok := row[forbidden]; ok {
						t.Fatalf("resource retained customer billing field %s: %#v", forbidden, row)
					}
				}
			}
		})
	}
}

func TestWorkspaceLaunchContinuationAttemptBudgetsArePersistedAndConfirmed(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	configureWorkspaceLaunchFulfillment(t, fixture)
	for range 2 {
		if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
			t.Fatal(err)
		}
	}

	operation := fixture.operation(t)
	for _, stage := range []string{"storage", "attachment", "secret", "runtime", "activation", "receipt"} {
		budget := persistedWorkspaceLaunchStageBudget(t, operation, stage)
		if budget["attempted"] != float64(1) || budget["confirmed"] != float64(1) || budget["unknown"] != float64(0) || budget["max"] != float64(1) {
			t.Fatalf("%s budget=%#v operation=%#v", stage, budget, operation)
		}
	}
}

func TestWorkspaceLaunchUnknownStageAttemptSurvivesRestartWithoutSecondWrite(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	configureWorkspaceLaunchFulfillment(t, fixture)
	fixture.fabric.gatewaySecretErr = errors.New("gateway secret response unknown")
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err == nil {
		t.Fatal("unknown Secret outcome was not returned")
	}

	first := fixture.operation(t)
	if first.Status != "manual_review" || first.Phase != "secret_writing" || first.ErrorCode != "workspace_launch_secret_attempt_unknown" {
		t.Fatalf("unknown Secret outcome=%#v", first)
	}
	budget := persistedWorkspaceLaunchStageBudget(t, first, "secret")
	if budget["attempted"] != float64(1) || budget["confirmed"] != float64(0) || budget["unknown"] != float64(1) || budget["max"] != float64(1) {
		t.Fatalf("unknown Secret budget=%#v", budget)
	}
	beforeWrites := countStrings(*fixture.events, "fabric.gateway-secret")

	restarted, err := newControlPlaneAppWithStore(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	after := fixture.operation(t)
	if countStrings(*fixture.events, "fabric.gateway-secret") != beforeWrites || after.Status != "manual_review" || persistedWorkspaceLaunchStageBudget(t, after, "secret")["unknown"] != float64(1) {
		t.Fatalf("restart repeated unknown Secret write: before=%d events=%#v operation=%#v", beforeWrites, *fixture.events, after)
	}
}

func TestPostgresWorkspaceLaunchUnknownStageAttemptSurvivesStoreReopenWithoutSecondWrite(t *testing.T) {
	t.Setenv("OPL_MONTHLY_BILLING_WORKER_ENABLED", "false")
	t.Setenv("OPL_PROVIDER_RECONCILE_WORKER_ENABLED", "false")
	t.Setenv("OPL_ARCHIVE_RETENTION_WORKER_ENABLED", "false")
	t.Setenv("OPL_WORKSPACE_IMAGE", workspaceImageRepository+"@sha256:"+strings.Repeat("f", 64))

	admin := openControlPlaneTestPostgres(t)
	schema := fmt.Sprintf("control_plane_workspace_launch_budget_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		_ = admin.Close()
	})
	databaseURL := controlPlaneTestPostgresURL(t, "postgres", schema)

	stateStore, err := newTestPostgresEntStateStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	firstStore := stateStore.(*postgresEntStateStore)
	seedTenantMember(t, firstStore, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")

	operation := newWorkspaceLaunchOperation("acct-alpha", "usr-alpha", "Alpha", "basic", 10, false, pilotPriceVersion, 52_580_000, "launch-postgres-secret-unknown")
	operation.Status, operation.Phase = "preparing", "secret_writing"
	operation.WorkspaceAPIKeyID = 19
	operation.AttachmentID = "attachment-alpha"
	operation.ContinuationAttemptBudgets["storage"] = workspaceLaunchStageBudget{Attempted: 1, Confirmed: 1, Max: workspaceLaunchStageMax}
	operation.ContinuationAttemptBudgets["attachment"] = workspaceLaunchStageBudget{Attempted: 1, Confirmed: 1, Max: workspaceLaunchStageMax}
	if err := firstStore.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(operation)); err != nil {
		t.Fatal(err)
	}

	events := []string{}
	sub2API := &workspaceLaunchSub2API{
		monthlySub2API: &monthlySub2API{events: &events},
		keys: map[int64]clients.Sub2APIWorkspaceKey{
			19: {ID: 19, UserID: 41, Name: workspaceReservedKeyName(operation.WorkspaceID), Key: "workspace-key-secret", Status: "active"},
		},
	}
	fabric := &monthlyFabric{
		fakeFabricClient: fakeFabricClient{calls: &events, gatewaySecretErr: errors.New("gateway secret response unknown")},
		events:           &events,
	}
	service := controlplane.NewService(fakeLedgerClient{}, fabric, sub2API)
	firstApp, err := newControlPlaneAppWithStore(firstStore)
	if err != nil {
		t.Fatal(err)
	}
	if err := firstApp.runWorkspaceLaunchesOnce(context.Background(), service); err == nil {
		t.Fatal("unknown Secret outcome was not returned")
	}
	row, found, err := firstStore.GetRuntimeOperation(context.Background(), operation.ID)
	if err != nil || !found {
		t.Fatalf("first operation found=%t err=%v", found, err)
	}
	first, err := decodeWorkspaceLaunchOperation(row)
	if err != nil {
		t.Fatal(err)
	}
	budget := first.ContinuationAttemptBudgets["secret"]
	if first.Status != "manual_review" || first.ErrorCode != "workspace_launch_secret_attempt_unknown" ||
		budget != (workspaceLaunchStageBudget{Attempted: 1, Unknown: 1, Max: workspaceLaunchStageMax}) || countStrings(events, "fabric.gateway-secret") != 1 {
		t.Fatalf("first unknown Secret outcome=%#v budget=%#v events=%#v", first, budget, events)
	}
	if err := firstStore.client.Close(); err != nil {
		t.Fatal(err)
	}

	restartedState, err := newTestPostgresEntStateStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	restartedStore := restartedState.(*postgresEntStateStore)
	t.Cleanup(func() { _ = restartedStore.client.Close() })
	restartedApp, err := newControlPlaneAppWithStore(restartedStore)
	if err != nil {
		t.Fatal(err)
	}
	if err := restartedApp.runWorkspaceLaunchesOnce(context.Background(), service); err != nil {
		t.Fatal(err)
	}
	restartedRow, found, err := restartedStore.GetRuntimeOperation(context.Background(), operation.ID)
	if err != nil || !found {
		t.Fatalf("restarted operation found=%t err=%v", found, err)
	}
	restarted, err := decodeWorkspaceLaunchOperation(restartedRow)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Status != "manual_review" || restarted.ContinuationAttemptBudgets["secret"] != budget || countStrings(events, "fabric.gateway-secret") != 1 {
		t.Fatalf("store reopen repeated unknown Secret write: operation=%#v events=%#v", restarted, events)
	}
}

func TestWorkspaceLaunchStorageUnknownAttemptSurvivesRestartWithoutSecondWrite(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	configureWorkspaceLaunchFulfillment(t, fixture)
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	fixture.fabric.storageCreateErr = errors.New("storage response unknown")
	fixture.fabric.storageSyncErr = errors.New("storage readback unavailable")
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err == nil {
		t.Fatal("unknown Storage outcome was not returned")
	}

	first := fixture.operation(t)
	if first.Status != "manual_review" || first.Phase != "storage_fulfilling" || first.ErrorCode != "workspace_launch_storage_attempt_unknown" {
		t.Fatalf("unknown Storage outcome=%#v", first)
	}
	budget := persistedWorkspaceLaunchStageBudget(t, first, "storage")
	if budget["attempted"] != float64(1) || budget["confirmed"] != float64(0) || budget["unknown"] != float64(1) || budget["max"] != float64(1) {
		t.Fatalf("unknown Storage budget=%#v", budget)
	}
	beforeWrites := len(fixture.fabric.storageIDs)

	restarted, err := newControlPlaneAppWithStore(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	after := fixture.operation(t)
	if len(fixture.fabric.storageIDs) != beforeWrites || after.Status != "manual_review" || persistedWorkspaceLaunchStageBudget(t, after, "storage")["unknown"] != float64(1) {
		t.Fatalf("restart repeated unknown Storage write: before=%d after=%d operation=%#v", beforeWrites, len(fixture.fabric.storageIDs), after)
	}
}

func TestWorkspaceLaunchConfirmedStorageAttemptResumesByReadbackWithoutSecondWrite(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	configureWorkspaceLaunchFulfillment(t, fixture)
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}

	operation := fixture.operation(t)
	operation.Status, operation.Phase, operation.ErrorCode = "preparing", "compute_fulfilling", ""
	if outcome, err := fixture.app.fulfillWorkspaceLaunchResource(context.Background(), fixture.service, &operation, "compute"); err != nil || outcome != "ready" {
		t.Fatalf("compute setup outcome=%q err=%v", outcome, err)
	}
	operation.Phase = "storage_fulfilling"
	if err := fixture.app.persistWorkspaceLaunch(context.Background(), &operation); err != nil {
		t.Fatal(err)
	}
	if outcome, err := fixture.app.fulfillWorkspaceLaunchResource(context.Background(), fixture.service, &operation, "storage"); err != nil || outcome != "ready" {
		t.Fatalf("storage setup outcome=%q err=%v", outcome, err)
	}
	beforeWrites := len(fixture.fabric.storageIDs)
	beforePrepareEvents := countStrings(*fixture.events, "fabric.storage.prepare")
	beforeReadEvents := countStrings(*fixture.events, "fabric.storage.get")

	restarted, err := newControlPlaneAppWithStore(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}

	completed := fixture.operation(t)
	if completed.Status != "succeeded" || completed.Phase != "succeeded" || len(fixture.fabric.storageIDs) != beforeWrites ||
		countStrings(*fixture.events, "fabric.storage.get") != beforeReadEvents+1 || countStrings(*fixture.events, "fabric.storage.prepare") != beforePrepareEvents {
		t.Fatalf("confirmed Storage restart did not resume by readback: operation=%#v storageWrites=%#v events=%#v", completed, fixture.fabric.storageIDs, *fixture.events)
	}
}

func TestWorkspaceLaunchAttachmentUnknownAttemptSurvivesRestartWithoutSecondWrite(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	configureWorkspaceLaunchFulfillment(t, fixture)
	fixture.fabric.attachmentErr = errors.New("attachment response unknown")
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err == nil {
		t.Fatal("unknown Attachment outcome was not returned")
	}

	first := fixture.operation(t)
	if first.Status != "manual_review" || first.Phase != "attaching" || first.ErrorCode != "workspace_launch_attachment_attempt_unknown" {
		t.Fatalf("unknown Attachment outcome=%#v", first)
	}
	budget := persistedWorkspaceLaunchStageBudget(t, first, "attachment")
	if budget["attempted"] != float64(1) || budget["confirmed"] != float64(0) || budget["unknown"] != float64(1) || budget["max"] != float64(1) {
		t.Fatalf("unknown Attachment budget=%#v", budget)
	}
	beforeWrites := countStrings(*fixture.events, "fabric.attachment")

	restarted, err := newControlPlaneAppWithStore(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	after := fixture.operation(t)
	if countStrings(*fixture.events, "fabric.attachment") != beforeWrites || after.Status != "manual_review" || persistedWorkspaceLaunchStageBudget(t, after, "attachment")["unknown"] != float64(1) {
		t.Fatalf("restart repeated unknown Attachment write: before=%d events=%#v operation=%#v", beforeWrites, *fixture.events, after)
	}
}

func TestWorkspaceLaunchRuntimeUnknownAttemptSurvivesRestartWithoutSecondWrite(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, errors.New("runtime response unknown"))
	configureWorkspaceLaunchFulfillment(t, fixture)
	fixture.fabric.runtimeStatusErr = errors.New("runtime readback unavailable")
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err == nil {
		t.Fatal("unknown Runtime outcome was not returned")
	}

	first := fixture.operation(t)
	if first.Status != "manual_review" || first.Phase != "runtime_starting" || first.ErrorCode != "workspace_launch_runtime_attempt_unknown" {
		t.Fatalf("unknown Runtime outcome=%#v", first)
	}
	budget := persistedWorkspaceLaunchStageBudget(t, first, "runtime")
	if budget["attempted"] != float64(1) || budget["confirmed"] != float64(0) || budget["unknown"] != float64(1) || budget["max"] != float64(1) {
		t.Fatalf("unknown Runtime budget=%#v", budget)
	}
	beforeWrites := countStrings(*fixture.events, "fabric.runtime")

	restarted, err := newControlPlaneAppWithStore(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	after := fixture.operation(t)
	if countStrings(*fixture.events, "fabric.runtime") != beforeWrites || after.Status != "manual_review" || persistedWorkspaceLaunchStageBudget(t, after, "runtime")["unknown"] != float64(1) {
		t.Fatalf("restart repeated unknown Runtime write: before=%d events=%#v operation=%#v", beforeWrites, *fixture.events, after)
	}
}

func TestWorkspaceLaunchActivationUnknownAttemptSurvivesRestartWithoutSecondWrite(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	configureWorkspaceLaunchFulfillment(t, fixture)
	fixture.store.activationErr = errors.New("activation response unknown")
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err == nil {
		t.Fatal("unknown activation outcome was not returned")
	}

	first := fixture.operation(t)
	if first.Status != "manual_review" || first.Phase != "activating" || first.ErrorCode != "workspace_launch_activation_attempt_unknown" {
		t.Fatalf("unknown activation outcome=%#v", first)
	}
	budget := persistedWorkspaceLaunchStageBudget(t, first, "activation")
	if budget["attempted"] != float64(1) || budget["confirmed"] != float64(0) || budget["unknown"] != float64(1) || budget["max"] != float64(1) {
		t.Fatalf("unknown activation budget=%#v", budget)
	}
	beforeWrites := fixture.store.activationCalls

	restarted, err := newControlPlaneAppWithStore(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	after := fixture.operation(t)
	if fixture.store.activationCalls != beforeWrites || after.Status != "manual_review" || persistedWorkspaceLaunchStageBudget(t, after, "activation")["unknown"] != float64(1) {
		t.Fatalf("restart repeated unknown activation write: before=%d after=%d operation=%#v", beforeWrites, fixture.store.activationCalls, after)
	}
}

func TestWorkspaceLaunchReceiptUnknownAttemptSurvivesRestartWithoutSecondWrite(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	configureWorkspaceLaunchFulfillment(t, fixture)
	fixture.ledger.receiptErrors = []error{errors.New("receipt response unknown")}
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err == nil {
		t.Fatal("unknown Receipt outcome was not returned")
	}

	first := fixture.operation(t)
	if first.Status != "manual_review" || first.Phase != "receipt_pending" || first.ErrorCode != "workspace_launch_receipt_attempt_unknown" {
		t.Fatalf("unknown Receipt outcome=%#v", first)
	}
	budget := persistedWorkspaceLaunchStageBudget(t, first, "receipt")
	if budget["attempted"] != float64(1) || budget["confirmed"] != float64(0) || budget["unknown"] != float64(1) || budget["max"] != float64(1) {
		t.Fatalf("unknown Receipt budget=%#v", budget)
	}
	beforeWrites := len(fixture.ledger.receiptInputs)

	restarted, err := newControlPlaneAppWithStore(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	after := fixture.operation(t)
	if len(fixture.ledger.receiptInputs) != beforeWrites || after.Status != "manual_review" || persistedWorkspaceLaunchStageBudget(t, after, "receipt")["unknown"] != float64(1) {
		t.Fatalf("restart repeated unknown Receipt write: before=%d after=%d operation=%#v", beforeWrites, len(fixture.ledger.receiptInputs), after)
	}
}

func TestWorkspaceLaunchExhaustedStageReservationStopsBeforeWrite(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000}, nil, nil)
	operation := fixture.operation(t)
	operation.Status, operation.Phase, operation.ErrorCode = "preparing", "secret_writing", ""
	releaseWorkspaceLaunchLease(&operation)
	row := workspaceLaunchOperationRow(operation)
	var result map[string]any
	if err := json.Unmarshal([]byte(stringValue(row["result"])), &result); err != nil {
		t.Fatal(err)
	}
	result["continuationAttemptBudgets"] = map[string]any{
		"storage":    map[string]any{"attempted": 1, "confirmed": 1, "unknown": 0, "max": 1},
		"attachment": map[string]any{"attempted": 1, "confirmed": 1, "unknown": 0, "max": 1},
		"secret":     map[string]any{"attempted": 1, "confirmed": 0, "unknown": 0, "max": 1},
		"runtime":    map[string]any{"attempted": 0, "confirmed": 0, "unknown": 0, "max": 1},
		"activation": map[string]any{"attempted": 0, "confirmed": 0, "unknown": 0, "max": 1},
		"receipt":    map[string]any{"attempted": 0, "confirmed": 0, "unknown": 0, "max": 1},
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	row["result"] = string(encoded)
	mustStore(t, fixture.store.memoryTableStore.SaveRuntimeOperation(context.Background(), row))

	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err == nil {
		t.Fatal("exhausted Secret reservation was not rejected")
	}
	current := fixture.operation(t)
	if current.Status != "manual_review" || current.ErrorCode != "workspace_launch_secret_attempt_unknown" || countStrings(*fixture.events, "fabric.gateway-secret") != 0 {
		t.Fatalf("exhausted Secret reservation crossed write gate: operation=%#v events=%#v", current, *fixture.events)
	}
	budget := persistedWorkspaceLaunchStageBudget(t, current, "secret")
	if budget["attempted"] != float64(1) || budget["unknown"] != float64(1) || budget["max"] != float64(1) {
		t.Fatalf("exhausted Secret budget=%#v", budget)
	}
}

func persistedWorkspaceLaunchStageBudget(t *testing.T, operation workspaceLaunchOperation, stage string) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal([]byte(operation.PersistedResult), &result); err != nil {
		t.Fatal(err)
	}
	budgets, _ := result["continuationAttemptBudgets"].(map[string]any)
	budget, _ := budgets[stage].(map[string]any)
	if budget == nil {
		t.Fatalf("missing %s budget in result=%#v", stage, result)
	}
	return budget
}

func TestWorkspaceLaunchFulfillmentUsesPersistedNodePool(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	configureWorkspaceLaunchFulfillment(t, fixture)
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	if len(fixture.fabric.computeInputs) != 1 || structToMap(fixture.fabric.computeInputs[0])["nodePoolId"] != "np-basic" {
		t.Fatalf("compute fulfillment did not use persisted NodePoolID: %#v", fixture.fabric.computeInputs)
	}
}

func TestWorkspaceLaunchPersistsComputeClaimPendingAndWorkerStops(t *testing.T) {
	for _, packageID := range []string{"basic", "pro"} {
		t.Run(packageID, func(t *testing.T) {
			storageGB := 10
			instanceType := "S5.MEDIUM4"
			totalCharge := int64(52_580_000)
			if packageID == "pro" {
				storageGB = 100
				instanceType = "SA5.2XLARGE16"
				totalCharge = 240_080_000
			}
			fixture := newWorkspaceLaunchWorkerFixtureForPlan(t, []int64{1_000_000_000, 1_000_000_000, 1_000_000_000 - totalCharge}, nil, nil, packageID, storageGB, false)
			operation := fixture.operation(t)
			pending := clients.ComputeAllocation{
				ID: operation.ComputeID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, PackageID: packageID,
				Status: "compute_claim_pending", Provider: "tencent-tke", PoolID: "pool-" + packageID, NodePoolID: "np-" + packageID,
				MachineName: "machine-claim-fixture", NodeName: "node-claim-fixture", InstanceID: "ins-claim-fixture", CVMInstanceID: "ins-claim-fixture",
				PrivateIP: "10.20.30.40", InstanceType: instanceType, Zone: "ap-shanghai-2", ChargeType: "PREPAID",
				RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2099-01-01T00:00:00Z", ProviderData: map[string]string{"recoveryAction": "compute_claim_recovery"},
			}
			fixture.fabric.mutateCompute = func(created *clients.ComputeAllocation) { *created = pending }
			fixture.fabric.computeSync = pending

			for range 2 {
				if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
					t.Fatal(err)
				}
			}
			persisted := fixture.operation(t)
			if persisted.Status != "compute_claim_pending" || persisted.Phase != "compute_claim_pending" ||
				persisted.ComputePoolID != pending.PoolID || persisted.ComputeNodePoolID != pending.NodePoolID ||
				persisted.ComputeMachineName != pending.MachineName || persisted.ComputeNodeName != pending.NodeName ||
				persisted.ComputeCVMInstanceID != pending.CVMInstanceID || persisted.ComputeInstanceType != pending.InstanceType || persisted.ComputeZone != pending.Zone {
				t.Fatalf("compute claim pending identity not persisted: %#v", persisted)
			}
			if len(fixture.fabric.computeIDs) != 1 || len(fixture.fabric.storageIDs) != 0 || len(fixture.sub2API.charges) != 1 || len(fixture.sub2API.refunds) != 0 ||
				countStrings(*fixture.events, "fabric.attachment") != 0 || countStrings(*fixture.events, "fabric.compute-claim.proof") != 0 || countStrings(*fixture.events, "fabric.compute-claim.claim") != 0 {
				t.Fatalf("pending replay crossed recovery gate: events=%#v compute=%#v storage=%#v charges=%#v refunds=%#v", *fixture.events, fixture.fabric.computeIDs, fixture.fabric.storageIDs, fixture.sub2API.charges, fixture.sub2API.refunds)
			}
		})
	}
}

func workspaceLaunchComputeClaimPendingFixture(t *testing.T, packageID string) (workspaceLaunchWorkerFixture, workspaceLaunchOperation) {
	t.Helper()
	storageGB := 10
	totalCharge := int64(52_580_000)
	instanceType := "S5.MEDIUM4"
	if packageID == "pro" {
		storageGB = 100
		totalCharge = 240_080_000
		instanceType = "SA5.2XLARGE16"
	}
	fixture := newWorkspaceLaunchWorkerFixtureForPlan(t, []int64{1_000_000_000, 1_000_000_000, 1_000_000_000 - totalCharge}, nil, nil, packageID, storageGB, false)
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	operation := fixture.operation(t)
	pending := clients.ComputeAllocation{
		ID: operation.ComputeID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, PackageID: packageID,
		Status: "compute_claim_pending", Provider: "tencent-tke", PoolID: "pool-" + packageID, NodePoolID: operation.ComputeNodePoolID,
		MachineName: "machine-claim-fixture", NodeName: "node-claim-fixture", InstanceID: "ins-claim-fixture", CVMInstanceID: "ins-claim-fixture",
		PrivateIP: "10.20.30.40", InstanceType: instanceType, Zone: "ap-shanghai-2", ChargeType: "PREPAID",
		RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2099-01-01T00:00:00Z", ProviderData: map[string]string{"recoveryAction": "compute_claim_recovery"},
	}
	fixture.fabric.mutateCompute = func(created *clients.ComputeAllocation) { *created = pending }
	fixture.fabric.computeSync = pending
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	operation = fixture.operation(t)
	if operation.Status != "compute_claim_pending" || operation.Phase != "compute_claim_pending" {
		t.Fatalf("launch did not enter compute claim pending: %#v", operation)
	}
	return fixture, operation
}

func computeClaimRecoveryProofForLaunch(operation workspaceLaunchOperation, nodeOwnershipState string) clients.ComputeClaimRecoveryProof {
	return computeClaimRecoveryProofForLaunchStorage(operation, nodeOwnershipState, "storage_not_started", "")
}

func computeClaimRecoveryProofForLaunchStorage(operation workspaceLaunchOperation, nodeOwnershipState, storageState, storageProviderResourceID string) clients.ComputeClaimRecoveryProof {
	return clients.ComputeClaimRecoveryProof{
		SchemaVersion: 1, Eligible: true, Reason: "none", StorageState: storageState, StorageProviderResourceID: storageProviderResourceID,
		LaunchOperationID: operation.ID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID,
		ComputeAllocationID: operation.ComputeID, StorageVolumeID: operation.StorageID, PackageID: operation.PackageID,
		PoolID: operation.ComputePoolID, NodePoolID: operation.ComputeNodePoolID, MachineName: operation.ComputeMachineName,
		NodeName: operation.ComputeNodeName, CVMInstanceID: operation.ComputeCVMInstanceID, PrivateIP: operation.ComputePrivateIP,
		InstanceType: operation.ComputeInstanceType, Zone: operation.ComputeZone, ChargeType: "PREPAID", PeriodMonths: 1,
		RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2099-01-01T00:00:00Z", NodeOwnershipState: nodeOwnershipState,
		CVMOwnershipState: "target_owned", Evidence: &clients.ComputeClaimEvidence{},
	}
}

func computeClaimTestStableSuffix(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, ":")))
	return fmt.Sprintf("%x", sum)
}

func computeClaimRecoveryApprovalResources(operation workspaceLaunchOperation, storageState, storageProviderResourceID string) map[string]any {
	runtimeOperationID := operation.WorkspaceOperationID + ":runtime"
	return map[string]any{
		"computeOperationId":        operation.ID + ":compute",
		"storageOperationId":        operation.ID + ":storage",
		"storageState":              storageState,
		"storageProviderResourceId": storageProviderResourceID,
		"attachmentId":              "att_" + computeClaimTestStableSuffix(operation.AttachmentOperationID)[:18],
		"attachmentOperationId":     operation.AttachmentOperationID,
		"workspaceApiKeyId":         fmt.Sprintf("%d", operation.WorkspaceAPIKeyID),
		"gatewaySecretRef":          "opl-gateway-" + computeClaimTestStableSuffix(operation.WorkspaceID)[:16],
		"secretOperationId":         operation.WorkspaceOperationID + ":secret:gateway-secret",
		"runtimeId":                 "rt_" + computeClaimTestStableSuffix(operation.WorkspaceID, runtimeOperationID)[:18],
		"runtimeOperationId":        runtimeOperationID,
		"receiptOperationId":        operation.ID + ":purchase-receipt",
	}
}

func computeClaimRecoveryRequestBody(t *testing.T, operation workspaceLaunchOperation, approved bool, idempotencyKey string) string {
	return computeClaimRecoveryRequestBodyForStorage(t, operation, approved, idempotencyKey, "storage_not_started", "")
}

func computeClaimRecoveryRequestBodyForStorage(t *testing.T, operation workspaceLaunchOperation, approved bool, idempotencyKey, storageState, storageProviderResourceID string) string {
	t.Helper()
	body := map[string]any{
		"accountId": operation.AccountID, "workspaceId": operation.WorkspaceID, "computeAllocationId": operation.ComputeID,
		"storageId": operation.StorageID, "packageId": operation.PackageID, "poolId": operation.ComputePoolID,
		"nodePoolId": operation.ComputeNodePoolID, "machineName": operation.ComputeMachineName, "nodeName": operation.ComputeNodeName,
		"cvmInstanceId": operation.ComputeCVMInstanceID, "privateIp": operation.ComputePrivateIP,
		"instanceType": operation.ComputeInstanceType, "zone": operation.ComputeZone,
	}
	if approved {
		resources := computeClaimRecoveryApprovalResources(operation, storageState, storageProviderResourceID)
		attemptLimits := map[string]any{
			"claim":   map[string]any{"sub2api": 0, "tencent": 5, "kubernetes": 1},
			"storage": 1, "attachment": 1, "secret": 1, "runtime": 1, "activation": 1, "receipt": 1,
		}
		storageWrite := "create_original_cbs"
		if storageState == "storage_existing_exact" {
			storageWrite = "reuse_original_cbs"
		}
		allowedWrites := []string{
			"claim_existing_cvm_node", storageWrite, "create_original_pv_pvc_attachment", "upsert_original_gateway_secret",
			"create_original_workspace_runtime", "activate_original_workspace", "record_original_purchase_receipt",
		}
		forbiddenWrites := []string{"create_launch", "debit", "recharge", "refund", "scale", "create_cvm", "create_second_cbs", "delete", "replace"}
		approval := map[string]any{
			"schemaVersion":        2,
			"approvalId":           "approval-compute-claim-fixture",
			"expiresAt":            "2099-08-28T00:00:00Z",
			"mergedMainSha":        strings.Repeat("a", 40),
			"cloudImageDigest":     "sha256:" + strings.Repeat("b", 64),
			"workspaceImageDigest": operation.WorkspaceImageDigest,
			"confirmation":         "RECOVER_PROVEN_COMPUTE_AND_CONTINUE_ORIGINAL_LAUNCH",
			"idempotencyKey":       idempotencyKey,
			"recoveryKey":          "compute-claim-recovery-fixture",
			"customer":             map[string]any{"email": "alpha@example.com", "accountId": operation.AccountID},
			"target": map[string]any{
				"launchOperationId": operation.ID, "accountId": operation.AccountID, "workspaceId": operation.WorkspaceID,
				"computeAllocationId": operation.ComputeID, "storageId": operation.StorageID, "packageId": operation.PackageID,
				"poolId": operation.ComputePoolID, "nodePoolId": operation.ComputeNodePoolID, "machineName": operation.ComputeMachineName,
				"nodeName": operation.ComputeNodeName, "cvmInstanceId": operation.ComputeCVMInstanceID, "privateIp": operation.ComputePrivateIP,
				"instanceType": operation.ComputeInstanceType, "zone": operation.ComputeZone, "chargeType": operation.ComputeChargeType,
				"periodMonths": 1, "renewFlag": operation.ComputeRenewFlag, "deadline": operation.ComputeDeadline,
			},
			"resources": resources, "attemptLimits": attemptLimits, "allowedWrites": allowedWrites, "forbiddenWrites": forbiddenWrites,
		}
		approvalJSON, err := json.Marshal(approval)
		if err != nil {
			t.Fatal(err)
		}
		approvalDigest := sha256.Sum256(approvalJSON)
		body["approvalId"] = approval["approvalId"]
		body["approvalDigest"] = fmt.Sprintf("%x", approvalDigest)
		body["expiresAt"] = approval["expiresAt"]
		body["mergedMainSha"] = approval["mergedMainSha"]
		body["cloudImageDigest"] = approval["cloudImageDigest"]
		body["workspaceImageDigest"] = approval["workspaceImageDigest"]
		body["customerEmail"] = "alpha@example.com"
		body["recoveryKey"] = approval["recoveryKey"]
		body["resources"] = resources
		body["attemptLimits"] = attemptLimits
		body["allowedWrites"] = allowedWrites
		body["forbiddenWrites"] = forbiddenWrites
		body["confirm"] = approval["confirmation"]
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func requestComputeClaimWithCapabilityForTest(t *testing.T, server http.Handler, session *httptest.ResponseRecorder, path, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	t.Setenv("OPL_INTERNAL_SERVICE_TOKEN", "compute-claim-internal-capability")
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("x-opl-compute-claim-capability", "compute-claim-internal-capability")
	addAuth(req, session)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
}

func workspaceLaunchLegacyComputeClaimFixture(t *testing.T, packageID string) (workspaceLaunchWorkerFixture, workspaceLaunchOperation) {
	t.Helper()
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, packageID)
	target := operation
	operation.Status, operation.Phase, operation.ErrorCode = "manual_review", "compute_fulfilling", "legacy_compute_claim_interrupted"
	operation.ComputePoolID, operation.ComputeMachineName, operation.ComputeNodeName = "", "", ""
	operation.ComputeCVMInstanceID, operation.ComputePrivateIP, operation.ComputeInstanceType = "", "", ""
	operation.ComputeZone, operation.ComputeChargeType, operation.ComputeRenewFlag, operation.ComputeDeadline = "", "", "", ""
	mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(operation)))
	return fixture, target
}

func TestWorkspaceComputeClaimDiagnosisIsReadOnlyAndExact(t *testing.T) {
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")
	path := "/api/operator/workspace-launches/" + operation.ID + "/compute-claim-recovery/proof"

	response := requestWithSession(t, fixture.server, fixture.operator, http.MethodPost, path, computeClaimRecoveryRequestBody(t, operation, false, ""))
	if response.Code != http.StatusOK {
		t.Fatalf("compute claim diagnosis status=%d body=%s", response.Code, response.Body.String())
	}
	var proof clients.ComputeClaimRecoveryProof
	if err := json.NewDecoder(response.Body).Decode(&proof); err != nil {
		t.Fatal(err)
	}
	current := fixture.operation(t)
	if !proof.Eligible || proof.StorageState != "storage_not_started" || proof.Reason != "none" || proof.Sub2APIMutationCount != 0 || proof.TencentMutationCount != 0 || proof.KubernetesMutationCount != 0 ||
		current.Status != "compute_claim_pending" || current.Phase != "compute_claim_pending" || len(fixture.fabric.computeClaimInputs) != 1 || len(fixture.fabric.computeClaimCalls) != 0 ||
		len(fixture.fabric.storageIDs) != 0 || len(fixture.sub2API.charges) != 1 || len(fixture.sub2API.refunds) != 0 || countStrings(*fixture.events, "fabric.monthly-provider-truth") != 0 {
		t.Fatalf("diagnosis mutated state: proof=%#v current=%#v inputs=%#v claims=%#v events=%#v", proof, current, fixture.fabric.computeClaimInputs, fixture.fabric.computeClaimCalls, *fixture.events)
	}

	mismatch := operation
	mismatch.ComputeMachineName = "machine-other-fixture"
	response = requestWithSession(t, fixture.server, fixture.operator, http.MethodPost, path, computeClaimRecoveryRequestBody(t, mismatch, false, ""))
	if response.Code != http.StatusConflict || len(fixture.fabric.computeClaimInputs) != 1 || len(fixture.fabric.computeClaimCalls) != 0 {
		t.Fatalf("mismatched diagnosis crossed identity gate: status=%d body=%s inputs=%#v claims=%#v", response.Code, response.Body.String(), fixture.fabric.computeClaimInputs, fixture.fabric.computeClaimCalls)
	}
}

func TestWorkspaceComputeClaimDiagnosisAcceptsLegacyPhaseWithoutMutation(t *testing.T) {
	for _, packageID := range []string{"basic", "pro"} {
		t.Run(packageID, func(t *testing.T) {
			fixture, operation := workspaceLaunchLegacyComputeClaimFixture(t, packageID)
			fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")
			path := "/api/operator/workspace-launches/" + operation.ID + "/compute-claim-recovery/proof"

			response := requestWithSession(t, fixture.server, fixture.operator, http.MethodPost, path, computeClaimRecoveryRequestBody(t, operation, false, ""))
			current := fixture.operation(t)
			if response.Code != http.StatusOK || current.Status != "manual_review" || current.Phase != "compute_fulfilling" ||
				len(fixture.fabric.computeClaimInputs) != 1 || len(fixture.fabric.computeClaimCalls) != 0 || len(fixture.fabric.storageIDs) != 0 ||
				len(fixture.sub2API.charges) != 1 || len(fixture.sub2API.refunds) != 0 {
				t.Fatalf("legacy diagnosis crossed read-only boundary: status=%d body=%s operation=%#v proofs=%#v claims=%#v", response.Code, response.Body.String(), current, fixture.fabric.computeClaimInputs, fixture.fabric.computeClaimCalls)
			}
		})
	}
}

func TestWorkspaceComputeClaimLegacyPhaseRejectsPartialPersistedIdentityBeforeFabric(t *testing.T) {
	fixture, target := workspaceLaunchLegacyComputeClaimFixture(t, "basic")
	legacy := fixture.operation(t)
	legacy.ComputePrivateIP = target.ComputePrivateIP
	mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(legacy)))
	path := "/api/operator/workspace-launches/" + target.ID + "/compute-claim-recovery/proof"

	response := requestWithSession(t, fixture.server, fixture.operator, http.MethodPost, path, computeClaimRecoveryRequestBody(t, target, false, ""))
	if response.Code != http.StatusConflict || len(fixture.fabric.computeClaimInputs) != 0 || len(fixture.fabric.computeClaimCalls) != 0 ||
		len(fixture.fabric.storageIDs) != 0 || len(fixture.sub2API.refunds) != 0 {
		t.Fatalf("partial legacy identity crossed Fabric gate: status=%d body=%s proofs=%#v claims=%#v", response.Code, response.Body.String(), fixture.fabric.computeClaimInputs, fixture.fabric.computeClaimCalls)
	}
}

func TestWorkspaceComputeClaimLegacyPhaseNormalizesOnlyAfterProof(t *testing.T) {
	fixture, operation := workspaceLaunchLegacyComputeClaimFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")
	claimed := computeClaimRecoveryProofForLaunch(operation, "target_owned")
	fixture.fabric.computeClaimResult = &claimed
	configureWorkspaceLaunchFulfillment(t, fixture)
	configureWorkspaceComputeClaimReadback(fixture, operation)
	fixture.fabric.beforeComputeClaim = func() {
		current := fixture.operation(t)
		if current.Status != "compute_claim_pending" || current.Phase != "compute_claim_pending" {
			t.Fatalf("Fabric claim called before legacy CAS normalization: %#v", current)
		}
	}
	path := "/api/operator/workspace-launches/" + operation.ID + "/compute-claim-recovery/claim"

	response := requestComputeClaimWithCapabilityForTest(t, fixture.server, fixture.operator, path, computeClaimRecoveryRequestBody(t, operation, true, "compute-claim-legacy"), "compute-claim-legacy")
	current := fixture.operation(t)
	if response.Code != http.StatusOK || current.Status != "preparing" || current.Phase != "storage_fulfilling" ||
		len(fixture.fabric.computeClaimInputs) != 1 || len(fixture.fabric.computeClaimCalls) != 1 || countStrings(*fixture.events, "fabric.compute.get") != 1 || len(fixture.fabric.storageIDs) != 0 ||
		len(fixture.sub2API.charges) != 1 || len(fixture.sub2API.refunds) != 0 {
		t.Fatalf("legacy claim did not normalize and resume original launch: status=%d body=%s operation=%#v proofs=%#v claims=%#v", response.Code, response.Body.String(), current, fixture.fabric.computeClaimInputs, fixture.fabric.computeClaimCalls)
	}
}

func TestWorkspaceComputeClaimLegacyPhaseProofFailureDoesNotNormalize(t *testing.T) {
	fixture, operation := workspaceLaunchLegacyComputeClaimFixture(t, "pro")
	proof := computeClaimRecoveryProofForLaunch(operation, "unallocated")
	proof.Eligible, proof.Reason, proof.StorageState = false, "identity_mismatch", "unknown"
	fixture.fabric.computeClaimProof = proof
	fixture.fabric.computeClaimProofErr = errors.New("legacy proof rejected")
	path := "/api/operator/workspace-launches/" + operation.ID + "/compute-claim-recovery/claim"

	response := requestComputeClaimWithCapabilityForTest(t, fixture.server, fixture.operator, path, computeClaimRecoveryRequestBody(t, operation, true, "compute-claim-legacy-proof-failure"), "compute-claim-legacy-proof-failure")
	current := fixture.operation(t)
	if response.Code != http.StatusConflict || current.Status != "manual_review" || current.Phase != "compute_fulfilling" ||
		len(fixture.fabric.computeClaimInputs) != 1 || len(fixture.fabric.computeClaimCalls) != 0 || len(fixture.fabric.storageIDs) != 0 ||
		len(fixture.sub2API.charges) != 1 || len(fixture.sub2API.refunds) != 0 {
		t.Fatalf("failed legacy proof normalized or mutated: status=%d body=%s operation=%#v proofs=%#v claims=%#v", response.Code, response.Body.String(), current, fixture.fabric.computeClaimInputs, fixture.fabric.computeClaimCalls)
	}
}

func TestWorkspaceComputeClaimLegacyPhaseCASConflictStopsBeforeFabricClaim(t *testing.T) {
	fixture, operation := workspaceLaunchLegacyComputeClaimFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")
	fixture.store.workspaceLaunchPersistErr = errWorkspaceLaunchCASConflict
	path := "/api/operator/workspace-launches/" + operation.ID + "/compute-claim-recovery/claim"

	response := requestComputeClaimWithCapabilityForTest(t, fixture.server, fixture.operator, path, computeClaimRecoveryRequestBody(t, operation, true, "compute-claim-legacy-cas-conflict"), "compute-claim-legacy-cas-conflict")
	current := fixture.operation(t)
	if response.Code != http.StatusConflict || current.Status != "manual_review" || current.Phase != "compute_fulfilling" ||
		len(fixture.fabric.computeClaimInputs) != 1 || len(fixture.fabric.computeClaimCalls) != 0 || len(fixture.fabric.storageIDs) != 0 ||
		len(fixture.sub2API.charges) != 1 || len(fixture.sub2API.refunds) != 0 {
		t.Fatalf("legacy CAS conflict crossed claim boundary: status=%d body=%s operation=%#v proofs=%#v claims=%#v", response.Code, response.Body.String(), current, fixture.fabric.computeClaimInputs, fixture.fabric.computeClaimCalls)
	}
}

func TestWorkspaceComputeClaimRequiresServerCapabilityBeforeFabric(t *testing.T) {
	for _, capability := range []string{"", "wrong-capability"} {
		t.Run(firstNonEmpty(capability, "missing"), func(t *testing.T) {
			fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
			t.Setenv("OPL_INTERNAL_SERVICE_TOKEN", "compute-claim-internal-capability")
			path := "/api/operator/workspace-launches/" + operation.ID + "/compute-claim-recovery/claim"
			req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(computeClaimRecoveryRequestBody(t, operation, true, "compute-claim-capability-gate")))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", "compute-claim-capability-gate")
			if capability != "" {
				req.Header.Set("x-opl-compute-claim-capability", capability)
			}
			addAuth(req, fixture.operator)
			response := httptest.NewRecorder()
			fixture.server.ServeHTTP(response, req)

			if response.Code != http.StatusForbidden || len(fixture.fabric.computeClaimInputs) != 0 || len(fixture.fabric.computeClaimCalls) != 0 ||
				len(fixture.fabric.storageIDs) != 0 || len(fixture.sub2API.refunds) != 0 {
				t.Fatalf("invalid capability crossed Fabric gate: status=%d body=%s proofs=%#v claims=%#v", response.Code, response.Body.String(), fixture.fabric.computeClaimInputs, fixture.fabric.computeClaimCalls)
			}
		})
	}
}

func TestWorkspaceComputeClaimRejectsInvalidApprovalAndTargetBeforeFabric(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
		status int
	}{
		{name: "missing private ip", mutate: func(body map[string]any) { delete(body, "privateIp") }, status: http.StatusBadRequest},
		{name: "private ip identity", mutate: func(body map[string]any) { body["privateIp"] = "10.20.30.99" }, status: http.StatusConflict},
		{name: "approval id", mutate: func(body map[string]any) { body["approvalId"] = "x" }, status: http.StatusBadRequest},
		{name: "merged sha", mutate: func(body map[string]any) { body["mergedMainSha"] = strings.Repeat("a", 39) }, status: http.StatusBadRequest},
		{name: "cloud digest", mutate: func(body map[string]any) { body["cloudImageDigest"] = "sha256:" + strings.Repeat("b", 63) }, status: http.StatusBadRequest},
		{name: "confirmation", mutate: func(body map[string]any) { body["confirm"] = "CLAIM_SOMETHING_ELSE" }, status: http.StatusBadRequest},
		{name: "machine identity", mutate: func(body map[string]any) { body["machineName"] = "machine-other-fixture" }, status: http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
			fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "target_owned")
			path := "/api/operator/workspace-launches/" + operation.ID + "/compute-claim-recovery/claim"
			var body map[string]any
			if err := json.Unmarshal([]byte(computeClaimRecoveryRequestBody(t, operation, true, "compute-claim-invalid")), &body); err != nil {
				t.Fatal(err)
			}
			tc.mutate(body)
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			response := requestComputeClaimWithCapabilityForTest(t, fixture.server, fixture.operator, path, string(encoded), "compute-claim-invalid")
			if response.Code != tc.status || len(fixture.fabric.computeClaimInputs) != 0 || len(fixture.fabric.computeClaimCalls) != 0 {
				t.Fatalf("invalid %s crossed Fabric gate: status=%d body=%s proofs=%#v claims=%#v", tc.name, response.Code, response.Body.String(), fixture.fabric.computeClaimInputs, fixture.fabric.computeClaimCalls)
			}
		})
	}
}

func TestWorkspaceComputeClaimApprovalResumesOriginalStorageOnce(t *testing.T) {
	for _, test := range []struct {
		name                      string
		packageID                 string
		storageState              string
		storageProviderResourceID string
	}{
		{name: "basic_absent", packageID: "basic", storageState: "storage_not_started"},
		{name: "pro_absent", packageID: "pro", storageState: "storage_not_started"},
		{name: "basic_existing_exact", packageID: "basic", storageState: "storage_existing_exact", storageProviderResourceID: "disk-existing-fixture"},
		{name: "pro_existing_exact", packageID: "pro", storageState: "storage_existing_exact", storageProviderResourceID: "disk-existing-fixture"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, test.packageID)
			fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunchStorage(operation, "target_owned", test.storageState, test.storageProviderResourceID)
			configureWorkspaceLaunchFulfillment(t, fixture)
			if test.storageProviderResourceID != "" {
				fixture.fabric.mutateStorage = func(volume *clients.StorageVolume) { volume.ProviderResourceID = test.storageProviderResourceID }
				fixture.fabric.storageSync.ProviderResourceID = test.storageProviderResourceID
			}
			configureWorkspaceComputeClaimReadback(fixture, operation)
			path := "/api/operator/workspace-launches/" + operation.ID + "/compute-claim-recovery/claim"
			body := computeClaimRecoveryRequestBodyForStorage(t, operation, true, "compute-claim-fixture", test.storageState, test.storageProviderResourceID)

			first := requestComputeClaimWithCapabilityForTest(t, fixture.server, fixture.operator, path, body, "compute-claim-fixture")
			second := requestComputeClaimWithCapabilityForTest(t, fixture.server, fixture.operator, path, body, "compute-claim-fixture")
			var driftedIPBody map[string]any
			if err := json.Unmarshal([]byte(body), &driftedIPBody); err != nil {
				t.Fatal(err)
			}
			driftedIPBody["privateIp"] = "10.20.30.99"
			driftedIPJSON, err := json.Marshal(driftedIPBody)
			if err != nil {
				t.Fatal(err)
			}
			driftedIP := requestComputeClaimWithCapabilityForTest(t, fixture.server, fixture.operator, path, string(driftedIPJSON), "compute-claim-fixture")
			changedKey := requestComputeClaimWithCapabilityForTest(t, fixture.server, fixture.operator, path, body, "compute-claim-other")
			claimed := fixture.operation(t)
			if first.Code != http.StatusOK || second.Code != http.StatusOK || driftedIP.Code != http.StatusConflict || changedKey.Code != http.StatusConflict ||
				claimed.Status != "preparing" || claimed.Phase != "storage_fulfilling" || claimed.ComputeClaimProof == nil || claimed.ComputeClaimProof.PrivateIP != operation.ComputePrivateIP ||
				len(fixture.fabric.computeClaimCalls) != 1 || len(fixture.fabric.computeClaimKeys) != 1 || fixture.fabric.computeClaimKeys[0] != "compute-claim-fixture" ||
				countStrings(*fixture.events, "fabric.compute.get") != 1 ||
				len(fixture.fabric.storageIDs) != 0 || len(fixture.fabric.computeIDs) != 1 || len(fixture.sub2API.charges) != 1 || len(fixture.sub2API.refunds) != 0 {
				t.Fatalf("claim did not bind replay identity or stop at original storage phase: first=%d second=%d driftedIP=%d changedKey=%d operation=%#v calls=%#v keys=%#v compute=%#v storage=%#v", first.Code, second.Code, driftedIP.Code, changedKey.Code, claimed, fixture.fabric.computeClaimCalls, fixture.fabric.computeClaimKeys, fixture.fabric.computeIDs, fixture.fabric.storageIDs)
			}
			for range 2 {
				if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
					t.Fatal(err)
				}
			}
			completed := fixture.operation(t)
			if completed.Status != "succeeded" || len(fixture.fabric.storageIDs) != 1 || fixture.fabric.storageIDs[0] != operation.StorageID ||
				len(fixture.fabric.storageCreateKeys) != 1 || fixture.fabric.storageCreateKeys[0] != operation.ID+":storage" ||
				len(fixture.fabric.storageInputs) != 1 || fixture.fabric.storageInputs[0].ExpectedRecoveryState != test.storageState ||
				fixture.fabric.storageInputs[0].ExpectedProviderResourceID != test.storageProviderResourceID ||
				len(fixture.fabric.computeIDs) != 1 || len(fixture.sub2API.charges) != 1 || len(fixture.sub2API.refunds) != 0 {
				t.Fatalf("recovered launch duplicated fulfillment: operation=%#v compute=%#v storage=%#v keys=%#v charges=%#v refunds=%#v", completed, fixture.fabric.computeIDs, fixture.fabric.storageIDs, fixture.fabric.storageCreateKeys, fixture.sub2API.charges, fixture.sub2API.refunds)
			}
			assertWorkspaceLaunchRuntimeIdentity(t, fixture.fabric.runtimeInputs, completed)
		})
	}
}

func TestWorkspaceComputeClaimRejectsStorageIdentityDriftBeforeClaimOrStorageMutation(t *testing.T) {
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunchStorage(operation, "target_owned", "storage_existing_exact", "disk-drifted-fixture")
	path := "/api/operator/workspace-launches/" + operation.ID + "/compute-claim-recovery/claim"
	body := computeClaimRecoveryRequestBodyForStorage(t, operation, true, "compute-claim-storage-drift", "storage_existing_exact", "disk-approved-fixture")

	response := requestComputeClaimWithCapabilityForTest(t, fixture.server, fixture.operator, path, body, "compute-claim-storage-drift")
	current := fixture.operation(t)
	if response.Code != http.StatusConflict || current.Status != "manual_review" || len(fixture.fabric.computeClaimInputs) != 1 ||
		len(fixture.fabric.computeClaimCalls) != 0 || len(fixture.fabric.storageIDs) != 0 || len(fixture.sub2API.refunds) != 0 {
		t.Fatalf("storage identity drift crossed recovery gate: status=%d body=%s operation=%#v proofs=%#v claims=%#v storage=%#v", response.Code, response.Body.String(), current, fixture.fabric.computeClaimInputs, fixture.fabric.computeClaimCalls, fixture.fabric.storageInputs)
	}
}

func TestWorkspaceComputeClaimApprovalBindsMissingHistoricalWorkspaceImageDigestBeforeFabric(t *testing.T) {
	fixture, approvedOperation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	if approvedOperation.WorkspaceImageDigest == "" {
		t.Fatal("fixture must carry the immutable Workspace image digest used by the approval")
	}
	historicalOperation := approvedOperation
	historicalOperation.WorkspaceImageDigest = ""
	mustStore(t, fixture.store.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(historicalOperation)))

	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(approvedOperation, "target_owned")
	configureWorkspaceLaunchFulfillment(t, fixture)
	configureWorkspaceComputeClaimReadback(fixture, approvedOperation)
	fixture.fabric.beforeComputeClaimProof = func() {
		persisted := fixture.operation(t)
		if persisted.WorkspaceImageDigest != approvedOperation.WorkspaceImageDigest || persisted.ComputeClaimApproval == nil ||
			persisted.ComputeClaimApproval.WorkspaceImageDigest != approvedOperation.WorkspaceImageDigest {
			t.Fatalf("historical Workspace image digest and approval were not bound before Fabric proof: %#v", persisted)
		}
	}

	path := "/api/operator/workspace-launches/" + approvedOperation.ID + "/compute-claim-recovery/claim"
	response := requestComputeClaimWithCapabilityForTest(
		t,
		fixture.server,
		fixture.operator,
		path,
		computeClaimRecoveryRequestBody(t, approvedOperation, true, "compute-claim-historical-workspace-image"),
		"compute-claim-historical-workspace-image",
	)
	persisted := fixture.operation(t)
	if response.Code != http.StatusOK || persisted.WorkspaceImageDigest != approvedOperation.WorkspaceImageDigest ||
		persisted.ComputeClaimApproval == nil || persisted.Status != "preparing" || persisted.Phase != "storage_fulfilling" ||
		len(fixture.fabric.computeClaimInputs) != 1 || len(fixture.fabric.computeClaimCalls) != 1 || len(fixture.fabric.storageIDs) != 0 {
		t.Fatalf("historical Workspace image digest did not bind and resume safely: status=%d body=%s operation=%#v proofs=%#v claims=%#v storage=%#v", response.Code, response.Body.String(), persisted, fixture.fabric.computeClaimInputs, fixture.fabric.computeClaimCalls, fixture.fabric.storageIDs)
	}
}

func TestWorkspaceComputeClaimReadbackReplayNeverClaimsOrCreatesStorageTwice(t *testing.T) {
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "target_owned")
	configureWorkspaceLaunchFulfillment(t, fixture)
	ready := configureWorkspaceComputeClaimReadback(fixture, operation)
	fixture.fabric.computeReadResults = []clients.ComputeAllocation{{ID: operation.ComputeID}, ready}
	fixture.fabric.computeReadErrors = []error{errors.New("compute GET unavailable"), nil}
	path := "/api/operator/workspace-launches/" + operation.ID + "/compute-claim-recovery/claim"
	body := computeClaimRecoveryRequestBody(t, operation, true, "compute-claim-readback-replay")

	first := requestComputeClaimWithCapabilityForTest(t, fixture.server, fixture.operator, path, body, "compute-claim-readback-replay")
	blocked := fixture.operation(t)
	if first.Code != http.StatusBadGateway || blocked.Status != "manual_review" || blocked.Phase != "compute_claim_pending" ||
		blocked.ErrorCode != "workspace_compute_claim_readback_unavailable" || blocked.ComputeClaimProof == nil ||
		len(fixture.fabric.computeClaimCalls) != 1 || countStrings(*fixture.events, "fabric.compute.get") != 1 || len(fixture.fabric.storageIDs) != 0 {
		t.Fatalf("failed compute readback crossed continuation gate: status=%d body=%s operation=%#v claims=%#v storage=%#v events=%#v", first.Code, first.Body.String(), blocked, fixture.fabric.computeClaimCalls, fixture.fabric.storageIDs, *fixture.events)
	}

	second := requestComputeClaimWithCapabilityForTest(t, fixture.server, fixture.operator, path, body, "compute-claim-readback-replay")
	resumed := fixture.operation(t)
	if second.Code != http.StatusOK || resumed.Status != "preparing" || resumed.Phase != "storage_fulfilling" || resumed.ErrorCode != "" ||
		len(fixture.fabric.computeClaimCalls) != 1 || countStrings(*fixture.events, "fabric.compute.get") != 2 || len(fixture.fabric.storageIDs) != 0 {
		t.Fatalf("readback replay repeated claim or crossed storage gate: status=%d body=%s operation=%#v claims=%#v storage=%#v events=%#v", second.Code, second.Body.String(), resumed, fixture.fabric.computeClaimCalls, fixture.fabric.storageIDs, *fixture.events)
	}
}

func configureWorkspaceComputeClaimReadback(fixture workspaceLaunchWorkerFixture, operation workspaceLaunchOperation) clients.ComputeAllocation {
	readback := fixture.fabric.computeSync
	readback.OperationID = operation.ID + ":compute"
	readback.PoolID = operation.ComputePoolID
	readback.NodePoolID = operation.ComputeNodePoolID
	readback.MachineName = operation.ComputeMachineName
	readback.NodeName = operation.ComputeNodeName
	readback.InstanceID = operation.ComputeCVMInstanceID
	readback.CVMInstanceID = operation.ComputeCVMInstanceID
	readback.ProviderResourceID = operation.ComputeCVMInstanceID
	readback.PrivateIP = operation.ComputePrivateIP
	readback.InstanceType = operation.ComputeInstanceType
	readback.Zone = operation.ComputeZone
	readback.ChargeType = operation.ComputeChargeType
	readback.RenewFlag = operation.ComputeRenewFlag
	readback.Deadline = operation.ComputeDeadline
	readback.ProviderData["instanceType"] = operation.ComputeInstanceType
	readback.ProviderData["zone"] = operation.ComputeZone
	readback.ProviderData["chargeType"] = operation.ComputeChargeType
	readback.ProviderData["renewFlag"] = operation.ComputeRenewFlag
	readback.ProviderData["deadline"] = operation.ComputeDeadline
	readback.ProviderData["machineName"] = operation.ComputeMachineName
	readback.CostTags = map[string]string{
		"opl_account_id": operation.AccountID, "opl_workspace_id": operation.WorkspaceID,
		"opl_resource_id": operation.ComputeID, "opl_operation_id": "owner-claim-fixture",
	}
	fixture.fabric.computeSync = readback
	return readback
}

func assertWorkspaceLaunchRuntimeIdentity(t *testing.T, inputs []clients.WorkspaceRuntimeInput, operation workspaceLaunchOperation) {
	t.Helper()
	if len(inputs) != 1 {
		t.Fatalf("runtime inputs=%#v", inputs)
	}
	input := inputs[0]
	if input.WorkspaceID != operation.WorkspaceID || input.ComputeID != operation.ComputeID || input.VolumeID != operation.StorageID ||
		input.AttachmentID != operation.AttachmentID || input.AttachmentOperationID != operation.AttachmentOperationID ||
		input.RuntimeOperationID != operation.WorkspaceOperationID+":runtime" || input.ImageID != operation.WorkspaceImageDigest ||
		!strings.HasPrefix(operation.WorkspaceImageDigest, "uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app@sha256:") {
		t.Fatalf("runtime identity=%#v operation=%#v", input, operation)
	}
}

func TestWorkspaceComputeClaimPrivateIPProofAndReadbackDriftStopBeforeStorage(t *testing.T) {
	for _, tc := range []struct {
		name          string
		driftReadback bool
		wantClaims    int
	}{
		{name: "proof", wantClaims: 0},
		{name: "readback", driftReadback: true, wantClaims: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "pro")
			proof := computeClaimRecoveryProofForLaunch(operation, "unallocated")
			if tc.driftReadback {
				fixture.fabric.computeClaimProof = proof
				readback := computeClaimRecoveryProofForLaunch(operation, "target_owned")
				readback.PrivateIP = "10.20.30.99"
				fixture.fabric.computeClaimResult = &readback
			} else {
				proof.PrivateIP = "10.20.30.99"
				fixture.fabric.computeClaimProof = proof
			}
			path := "/api/operator/workspace-launches/" + operation.ID + "/compute-claim-recovery/claim"
			response := requestComputeClaimWithCapabilityForTest(t, fixture.server, fixture.operator, path, computeClaimRecoveryRequestBody(t, operation, true, "compute-claim-private-ip-drift"), "compute-claim-private-ip-drift")
			current := fixture.operation(t)
			if response.Code != http.StatusConflict || current.Status != "manual_review" || current.Phase != "compute_claim_pending" ||
				len(fixture.fabric.computeClaimCalls) != tc.wantClaims || len(fixture.fabric.storageIDs) != 0 || len(fixture.fabric.computeIDs) != 1 ||
				len(fixture.sub2API.charges) != 1 || len(fixture.sub2API.refunds) != 0 {
				t.Fatalf("private IP %s drift crossed recovery gate: status=%d operation=%#v proofs=%#v claims=%#v storage=%#v", tc.name, response.Code, current, fixture.fabric.computeClaimInputs, fixture.fabric.computeClaimCalls, fixture.fabric.storageIDs)
			}
		})
	}
}

func TestWorkspaceComputeClaimPartialMutationFailurePreservesCountsAndStops(t *testing.T) {
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "pro")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")
	failed := computeClaimRecoveryProofForLaunch(operation, "unallocated")
	failed.Eligible, failed.Reason = false, "iam_rbac"
	failed.TencentMutationCount, failed.KubernetesMutationCount = 3, 1
	failed.FailureStage, failed.ProviderErrorClass = "node_patch_readback", "iam_rbac"
	failed.Evidence = &clients.ComputeClaimEvidence{
		CVM:  clients.ComputeClaimMutationEvidence{Attempted: 3, Confirmed: 3},
		Node: clients.ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"node_ownership"}},
	}
	fixture.fabric.computeClaimResult = &failed
	fixture.fabric.computeClaimErr = errors.New("classified claim readback failure")
	path := "/api/operator/workspace-launches/" + operation.ID + "/compute-claim-recovery/claim"

	response := requestComputeClaimWithCapabilityForTest(t, fixture.server, fixture.operator, path, computeClaimRecoveryRequestBody(t, operation, true, "compute-claim-partial"), "compute-claim-partial")
	var result clients.ComputeClaimRecoveryProof
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	current := fixture.operation(t)
	if response.Code != http.StatusConflict || result.Reason != "iam_rbac" || result.TencentMutationCount != 3 || result.KubernetesMutationCount != 1 ||
		current.Status != "manual_review" || current.Phase != "compute_claim_pending" || len(fixture.fabric.computeClaimInputs) != 1 ||
		len(fixture.fabric.computeClaimCalls) != 1 || len(fixture.fabric.storageIDs) != 0 || len(fixture.fabric.computeIDs) != 1 ||
		len(fixture.sub2API.charges) != 1 || len(fixture.sub2API.refunds) != 0 {
		t.Fatalf("partial claim failure lost counts or advanced state: status=%d result=%#v operation=%#v events=%#v", response.Code, result, current, *fixture.events)
	}
}

func TestWorkspaceComputeClaimRejectsMutationCountsAboveHardBoundsBeforeStorage(t *testing.T) {
	for _, tc := range []struct {
		name       string
		sub2API    int
		tencent    int
		kubernetes int
	}{
		{name: "sub2api", sub2API: 1},
		{name: "tencent", tencent: 6},
		{name: "kubernetes", kubernetes: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
			fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")
			claimed := computeClaimRecoveryProofForLaunch(operation, "target_owned")
			claimed.Sub2APIMutationCount = tc.sub2API
			claimed.TencentMutationCount = tc.tencent
			claimed.KubernetesMutationCount = tc.kubernetes
			fixture.fabric.computeClaimResult = &claimed
			path := "/api/operator/workspace-launches/" + operation.ID + "/compute-claim-recovery/claim"

			response := requestComputeClaimWithCapabilityForTest(t, fixture.server, fixture.operator, path, computeClaimRecoveryRequestBody(t, operation, true, "compute-claim-over-bound"), "compute-claim-over-bound")
			current := fixture.operation(t)
			if response.Code != http.StatusConflict || current.Status != "manual_review" || current.Phase != "compute_claim_pending" ||
				len(fixture.fabric.computeClaimCalls) != 1 || len(fixture.fabric.storageIDs) != 0 || len(fixture.fabric.computeIDs) != 1 ||
				len(fixture.sub2API.charges) != 1 || len(fixture.sub2API.refunds) != 0 {
				t.Fatalf("over-bound %s crossed storage gate: status=%d operation=%#v result=%s", tc.name, response.Code, current, response.Body.String())
			}
		})
	}
}

func TestWorkspaceComputeClaimRejectsUnknownOrMissingMutationEvidenceBeforeStorage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*clients.ComputeClaimRecoveryProof)
	}{
		{name: "unknown cvm", mutate: func(proof *clients.ComputeClaimRecoveryProof) {
			proof.TencentMutationCount = 1
			proof.Evidence.CVM = clients.ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{"opl_workspace_id"}}
		}},
		{name: "unconfirmed node", mutate: func(proof *clients.ComputeClaimRecoveryProof) {
			proof.KubernetesMutationCount = 1
			proof.Evidence.Node = clients.ComputeClaimMutationEvidence{Attempted: 1, Missing: []string{"node_ownership"}}
		}},
		{name: "confirmed and unknown overlap", mutate: func(proof *clients.ComputeClaimRecoveryProof) {
			proof.TencentMutationCount = 1
			proof.Evidence.CVM = clients.ComputeClaimMutationEvidence{Attempted: 1, Confirmed: 1, Unknown: 1, Missing: []string{"instance_name"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
			fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")
			claimed := computeClaimRecoveryProofForLaunch(operation, "target_owned")
			tc.mutate(&claimed)
			claimed.FailureStage, claimed.ProviderErrorClass = "claim_final_readback", "readback_mismatch"
			fixture.fabric.computeClaimResult = &claimed
			path := "/api/operator/workspace-launches/" + operation.ID + "/compute-claim-recovery/claim"

			response := requestComputeClaimWithCapabilityForTest(t, fixture.server, fixture.operator, path, computeClaimRecoveryRequestBody(t, operation, true, "compute-claim-evidence"), "compute-claim-evidence")
			current := fixture.operation(t)
			if response.Code != http.StatusConflict || current.Status != "manual_review" || current.Phase != "compute_claim_pending" ||
				len(fixture.fabric.computeClaimCalls) != 1 || len(fixture.fabric.storageIDs) != 0 || len(fixture.fabric.computeIDs) != 1 ||
				len(fixture.sub2API.charges) != 1 || len(fixture.sub2API.refunds) != 0 {
				t.Fatalf("%s evidence crossed storage gate: status=%d operation=%#v result=%s", tc.name, response.Code, current, response.Body.String())
			}
		})
	}
}

func TestWorkspaceComputeClaimRejectsAndRedactsUnallowlistedMutationEvidence(t *testing.T) {
	fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
	fixture.fabric.computeClaimProof = computeClaimRecoveryProofForLaunch(operation, "unallocated")
	const marker = "ghp_secret"
	claimed := computeClaimRecoveryProofForLaunch(operation, "target_owned")
	claimed.Eligible, claimed.Reason = false, "provider_describe"
	claimed.TencentMutationCount = 1
	claimed.Evidence.CVM = clients.ComputeClaimMutationEvidence{Attempted: 1, Unknown: 1, Missing: []string{marker}}
	claimed.FailureStage, claimed.ProviderErrorClass = "cvm_final_readback", "readback_mismatch"
	fixture.fabric.computeClaimResult = &claimed
	path := "/api/operator/workspace-launches/" + operation.ID + "/compute-claim-recovery/claim"

	response := requestComputeClaimWithCapabilityForTest(t, fixture.server, fixture.operator, path, computeClaimRecoveryRequestBody(t, operation, true, "compute-claim-unallowlisted-evidence"), "compute-claim-unallowlisted-evidence")
	current := fixture.operation(t)

	if response.Code != http.StatusConflict || current.Status != "manual_review" || current.Phase != "compute_claim_pending" ||
		len(fixture.fabric.computeClaimCalls) != 1 || len(fixture.fabric.storageIDs) != 0 || strings.Contains(response.Body.String(), marker) {
		t.Fatalf("unallowlisted evidence crossed or leaked from the storage gate: status=%d operation=%#v result=%s", response.Code, current, response.Body.String())
	}
}

func TestWorkspaceComputeClaimMutationEvidenceRejectsOverlappingCardinality(t *testing.T) {
	evidence := clients.ComputeClaimMutationEvidence{
		Attempted: 1,
		Confirmed: 1,
		Unknown:   1,
		Missing:   []string{"instance_name"},
	}
	if workspaceComputeClaimMutationEvidenceMatches(evidence, 1, 5, "cvm", false) {
		t.Fatal("confirmed plus unknown may not exceed attempted")
	}
}

func TestWorkspaceComputeClaimFailureStopsInManualReviewWithSafeReason(t *testing.T) {
	for _, reason := range []string{"iam_rbac", "provider_describe", "multiple_candidate", "identity_mismatch", "node_ownership_conflict", "storage_already_started"} {
		t.Run(reason, func(t *testing.T) {
			fixture, operation := workspaceLaunchComputeClaimPendingFixture(t, "basic")
			proof := computeClaimRecoveryProofForLaunch(operation, "unallocated")
			proof.Eligible, proof.Reason, proof.StorageState = false, reason, "unknown"
			fixture.fabric.computeClaimProof = proof
			fixture.fabric.computeClaimProofErr = errors.New("classified compute claim failure")
			path := "/api/operator/workspace-launches/" + operation.ID + "/compute-claim-recovery/claim"

			response := requestComputeClaimWithCapabilityForTest(t, fixture.server, fixture.operator, path, computeClaimRecoveryRequestBody(t, operation, true, "compute-claim-failure"), "compute-claim-failure")
			if response.Code != http.StatusConflict {
				t.Fatalf("classified claim status=%d body=%s", response.Code, response.Body.String())
			}
			var result clients.ComputeClaimRecoveryProof
			if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
				t.Fatal(err)
			}
			current := fixture.operation(t)
			if result.Reason != reason || result.Sub2APIMutationCount != 0 || result.TencentMutationCount != 0 || result.KubernetesMutationCount != 0 ||
				current.Status != "manual_review" || current.Phase != "compute_claim_pending" || len(fixture.fabric.computeClaimCalls) != 0 ||
				len(fixture.fabric.storageIDs) != 0 || len(fixture.fabric.computeIDs) != 1 || len(fixture.sub2API.charges) != 1 || len(fixture.sub2API.refunds) != 0 {
				t.Fatalf("classified claim crossed fail-closed gate: result=%#v operation=%#v events=%#v", result, current, *fixture.events)
			}
		})
	}
}

func TestWorkspaceLaunchSingleReceipt(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	expected := configureWorkspaceLaunchFulfillment(t, fixture)
	for range 2 {
		if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
			t.Fatal(err)
		}
	}
	if len(fixture.ledger.receiptInputs) != 1 {
		t.Fatalf("launch receipts=%#v", fixture.ledger.receiptInputs)
	}
	receipt := fixture.ledger.receiptInputs[0]
	if receipt.Type != "billing.workspace_purchased.v1" || receipt.AccountID != expected.AccountID || receipt.WorkspaceID != expected.WorkspaceID || receipt.RequestID != expected.ID {
		t.Fatalf("Workspace purchase receipt=%#v", receipt)
	}
	if receipt.Cost["priceVersion"] != pilotPriceVersion || receipt.Cost["currency"] != "USD" || receipt.Cost["billingUnit"] != "calendar_month" ||
		receipt.Cost["totalUsdMicros"] != int64(52_580_000) || receipt.Cost["sub2apiRedeemCode"] != expected.RedeemCode ||
		stringValue(receipt.Cost["periodStart"]) == "" || stringValue(receipt.Cost["paidThrough"]) == "" {
		t.Fatalf("Workspace purchase cost=%#v", receipt.Cost)
	}
	components := mapField(receipt.Cost, "components")
	if numberField(mapField(components, "compute"), "chargeUsdMicros", 0) != 50_000_000 || numberField(mapField(components, "storage"), "chargeUsdMicros", 0) != 2_580_000 {
		t.Fatalf("Workspace purchase components=%#v", components)
	}
	if receipt.Execution["computeAllocationId"] != expected.ComputeID || receipt.Execution["storageId"] != expected.StorageID ||
		stringValue(receipt.Execution["attachmentId"]) == "" || stringValue(receipt.Execution["runtimeId"]) == "" {
		t.Fatalf("Workspace purchase fulfillment=%#v", receipt.Execution)
	}
}

func TestWorkspaceLaunchCreateResponseLossConvergesFromReadback(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	configureWorkspaceLaunchFulfillment(t, fixture)
	fixture.fabric.createErr = errors.New("Fabric create response lost")
	for range 2 {
		if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
			t.Fatal(err)
		}
	}
	operation := fixture.operation(t)
	if operation.Status != "succeeded" || operation.Phase != "succeeded" || len(fixture.sub2API.refunds) != 0 || len(fixture.ledger.receiptInputs) != 1 {
		t.Fatalf("response-loss launch=%#v refunds=%#v receipts=%#v", operation, fixture.sub2API.refunds, fixture.ledger.receiptInputs)
	}
}

func TestWorkspaceLaunchRuntimeReadinessWaits(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	operation := configureWorkspaceLaunchFulfillment(t, fixture)
	runtime := clients.WorkspaceRuntime{
		ID: "runtime-from-fabric", OperationID: operation.WorkspaceOperationID + ":runtime", WorkspaceID: operation.WorkspaceID, URL: "https://workspace.medopl.cn/w/" + operation.WorkspaceID + "/",
		Status: "unready", ServiceName: "opl-compute-from-fabric",
		Access: clients.WorkspaceRuntimeAccess{Username: "admin", CredentialStatus: "configured", CredentialVersion: "v1", SecretRef: "opl-compute-from-fabric-env"},
	}
	ready := runtime
	ready.Status, ready.Ready = "running", true
	fixture.fabric.runtime = runtime
	fixture.fabric.runtimeStatusResults = []clients.WorkspaceRuntime{runtime, ready}
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	waiting := fixture.operation(t)
	workspaces, _ := fixture.store.ListWorkspaces(context.Background(), operation.AccountID)
	if waiting.Status != "waiting" || waiting.Phase != "runtime_starting" || len(workspaces) != 0 || len(fixture.ledger.receiptInputs) != 0 {
		t.Fatalf("unready runtime launch=%#v workspaces=%#v receipts=%#v", waiting, workspaces, fixture.ledger.receiptInputs)
	}
	beforeEvents := append([]string(nil), (*fixture.events)...)
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	completed := fixture.operation(t)
	if completed.Status != "succeeded" || completed.Phase != "succeeded" || len(fixture.fabric.runtimeInputs) != 1 || countStrings(*fixture.events, "fabric.runtime-status") != 2 || len(fixture.ledger.receiptInputs) != 1 {
		t.Fatalf("ready runtime launch=%#v runtime calls=%#v receipts=%#v", completed, fixture.fabric.runtimeInputs, fixture.ledger.receiptInputs)
	}
	for _, event := range []string{"fabric.compute.prepare", "fabric.storage.prepare", "fabric.attachment", "fabric.gateway-secret"} {
		if countStrings(*fixture.events, event) != countStrings(beforeEvents, event) {
			t.Fatalf("runtime readiness retry repeated %s: before=%#v after=%#v", event, beforeEvents, *fixture.events)
		}
	}
	for _, event := range []string{"fabric.compute.sync", "fabric.storage.sync"} {
		if countStrings(*fixture.events, event) != countStrings(beforeEvents, event) {
			t.Fatalf("activation repeated mutating Sync %s: before=%#v after=%#v", event, beforeEvents, *fixture.events)
		}
	}
	if countStrings(*fixture.events, "fabric.workspace-activation-truth") != countStrings(beforeEvents, "fabric.workspace-activation-truth")+1 {
		t.Fatalf("activation did not read fresh truth once: before=%#v after=%#v", beforeEvents, *fixture.events)
	}
}

func TestWorkspaceLaunchActivationRejectsProviderZoneDrift(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	operation := configureWorkspaceLaunchFulfillment(t, fixture)
	runtime := clients.WorkspaceRuntime{
		ID: "runtime-from-fabric", OperationID: operation.WorkspaceOperationID + ":runtime", WorkspaceID: operation.WorkspaceID, URL: "https://workspace.medopl.cn/w/" + operation.WorkspaceID + "/",
		Status: "unready", ServiceName: "opl-compute-from-fabric",
		Access: clients.WorkspaceRuntimeAccess{Username: "admin", CredentialStatus: "configured", CredentialVersion: "v1", SecretRef: "opl-compute-from-fabric-env"},
	}
	ready := runtime
	ready.Status, ready.Ready = "running", true
	fixture.fabric.runtime = runtime
	fixture.fabric.runtimeStatusResults = []clients.WorkspaceRuntime{runtime, ready}
	for range 2 {
		if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
			t.Fatal(err)
		}
	}
	fixture.fabric.activationTruth = &clients.WorkspaceActivationTruth{
		SchemaVersion: 1, Ready: false, Reason: "identity_mismatch", ErrorClass: "readback_mismatch",
		ComputeState: "unknown", StorageState: "ready", Checks: []any{},
	}
	fixture.fabric.activationTruthErr = errors.New("provider Zone drift")
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err == nil {
		t.Fatal("provider Zone drift was accepted before activation")
	}
	current := fixture.operation(t)
	workspaces, _ := fixture.store.ListWorkspaces(context.Background(), operation.AccountID)
	if current.Status != "manual_review" || current.ErrorCode != "workspace_launch_activation_truth_identity_mismatch" || len(workspaces) != 0 || len(fixture.ledger.receiptInputs) != 0 {
		t.Fatalf("Zone drift activation=%#v workspaces=%#v receipts=%#v", current, workspaces, fixture.ledger.receiptInputs)
	}
}

func TestWorkspaceLaunchRuntimeReadbackDoesNotBackfillAuthority(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	operation := configureWorkspaceLaunchFulfillment(t, fixture)
	fixture.fabric.runtime = clients.WorkspaceRuntime{
		ID: "runtime-from-create", WorkspaceID: operation.WorkspaceID, URL: "https://workspace.medopl.cn/w/" + operation.WorkspaceID + "/",
		Status: "running", ServiceName: "opl-compute-from-create", Ready: true,
		Access: clients.WorkspaceRuntimeAccess{Username: "admin", CredentialStatus: "configured", CredentialVersion: "v1", SecretRef: "runtime-secret-from-create"},
	}
	fixture.fabric.runtimeStatusResults = []clients.WorkspaceRuntime{{WorkspaceID: operation.WorkspaceID, Status: "running", Ready: true}}
	for range 2 {
		_ = fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service)
	}

	current := fixture.operation(t)
	workspaces, _ := fixture.store.ListWorkspaces(context.Background(), operation.AccountID)
	if current.Status != "manual_review" || current.ErrorCode != "workspace_launch_runtime_attempt_unknown" ||
		persistedWorkspaceLaunchStageBudget(t, current, "runtime")["unknown"] != float64(1) || len(workspaces) != 0 || len(fixture.ledger.receiptInputs) != 0 {
		t.Fatalf("partial Runtime readback launch=%#v workspaces=%#v receipts=%#v", current, workspaces, fixture.ledger.receiptInputs)
	}
}

func TestWorkspaceLaunchAttachmentAllowsProviderDTOWithoutMountPath(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	operation := configureWorkspaceLaunchFulfillment(t, fixture)
	fixture.fabric.attachment = clients.StorageAttachment{
		ID: "attachment-from-tencent", WorkspaceID: operation.WorkspaceID, ComputeID: operation.ComputeID, VolumeID: operation.StorageID,
		Status: "attached", Provider: "tencent-tke", ProviderAttachmentID: "deployment/runtime:pvc/storage", ProviderRequestID: "request-from-tencent",
	}
	for range 2 {
		if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
			t.Fatal(err)
		}
	}
	completed := fixture.operation(t)
	if completed.Status != "succeeded" || completed.AttachmentID != "attachment-from-tencent" {
		t.Fatalf("Tencent attachment launch=%#v", completed)
	}
	attachments, _ := fixture.store.ListAttachments(context.Background(), operation.AccountID)
	if len(attachments) != 1 || stringValue(attachments[0]["providerAttachmentId"]) == "" || stringValue(attachments[0]["mountPath"]) != "/data" {
		t.Fatalf("attachment projection=%#v", attachments)
	}
}

func TestWorkspaceLaunchRefundWhenNoResources(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	operation := fixture.operation(t)
	fixture.fabric.createErr = errors.New("compute create response lost")
	fixture.fabric.computeSync = clients.ComputeAllocation{
		ID: operation.ComputeID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, Status: "external_deleted",
	}
	fixture.fabric.storageSyncErr = &clients.FabricHTTPError{StatusCode: http.StatusInternalServerError, Body: `{"error":"storage_volume_not_found"}`}
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	refunded := fixture.operation(t)
	if refunded.Status != "refunded" || refunded.Phase != "refunded" || len(fixture.sub2API.refunds) != 1 || fixture.sub2API.refunds[0].RefundUSDMicros != 52_580_000 {
		t.Fatalf("refunded launch=%#v refunds=%#v", refunded, fixture.sub2API.refunds)
	}
	if len(fixture.fabric.storageIDs) != 0 || countStrings(*fixture.events, "fabric.storage.get") != 1 || countStrings(*fixture.events, "fabric.storage.sync") != 0 ||
		countStrings(*fixture.events, "fabric.attachment") != 0 || countStrings(*fixture.events, "fabric.runtime") != 0 {
		t.Fatalf("absent compute crossed fulfillment: events=%#v", *fixture.events)
	}
	if len(fixture.ledger.receiptInputs) != 1 || fixture.ledger.receiptInputs[0].Type != "billing.workspace_refunded.v1" {
		t.Fatalf("refund receipts=%#v", fixture.ledger.receiptInputs)
	}
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil || len(fixture.sub2API.refunds) != 1 || len(fixture.ledger.receiptInputs) != 1 {
		t.Fatalf("refund replay err=%v refunds=%#v receipts=%#v", err, fixture.sub2API.refunds, fixture.ledger.receiptInputs)
	}
}

func TestWorkspaceLaunchComputeAbsentRequiresAuthoritativeStorageAbsenceBeforeRefund(t *testing.T) {
	for _, tc := range []struct {
		name, wantCode string
		err            error
	}{
		{name: "present", wantCode: "fabric_storage_presence_blocks_refund"},
		{name: "readback unavailable", wantCode: "fabric_storage_readback_unconfirmed_blocks_refund", err: errors.New("Fabric storage readback unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
			operation := fixture.operation(t)
			fixture.fabric.createErr = errors.New("compute create response lost")
			fixture.fabric.computeSync = clients.ComputeAllocation{
				ID: operation.ComputeID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, Status: "external_deleted",
			}
			fixture.fabric.storageSyncErr = tc.err
			if tc.name == "present" {
				fixture.fabric.storageSync = clients.StorageVolume{
					ID: operation.StorageID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, Status: "available",
					Provider: "tencent-tke", ProviderResourceID: "disk-" + operation.StorageID, CBSStatus: "UNATTACHED",
				}
			}
			for range 2 {
				_ = fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service)
			}

			current := fixture.operation(t)
			if current.Status != "manual_review" || current.ErrorCode != tc.wantCode || len(fixture.sub2API.refunds) != 0 ||
				countStrings(*fixture.events, "fabric.storage.get") != 1 || countStrings(*fixture.events, "fabric.storage.sync") != 0 {
				t.Fatalf("storage recovery=%#v refunds=%#v events=%#v", current, fixture.sub2API.refunds, *fixture.events)
			}
		})
	}
}

func TestWorkspaceLaunchPartialResourceManualReview(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	operation := configureWorkspaceLaunchFulfillment(t, fixture)
	fixture.fabric.storageSync = clients.StorageVolume{
		ID: operation.StorageID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, Status: "external_deleted", CBSStatus: "NOT_FOUND",
	}
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	_ = fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service)
	partial := fixture.operation(t)
	if partial.Status != "manual_review" || partial.Phase != "storage_fulfilling" || len(fixture.sub2API.refunds) != 0 {
		t.Fatalf("partial launch=%#v refunds=%#v", partial, fixture.sub2API.refunds)
	}
	workspaces, _ := fixture.store.ListWorkspaces(context.Background(), operation.AccountID)
	if len(workspaces) != 0 || countStrings(*fixture.events, "fabric.attachment") != 0 || countStrings(*fixture.events, "fabric.runtime") != 0 || len(fixture.ledger.receiptInputs) != 0 {
		t.Fatalf("partial launch crossed activation: workspaces=%#v events=%#v receipts=%#v", workspaces, *fixture.events, fixture.ledger.receiptInputs)
	}
}

func TestWorkspaceLaunchReceiptUnknownDoesNotRetry(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	configureWorkspaceLaunchFulfillment(t, fixture)
	fixture.ledger.receiptErrors = []error{errors.New("Ledger unavailable"), nil}
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err == nil {
		t.Fatal("first Ledger failure was not returned")
	}
	pending := fixture.operation(t)
	workspaces, _ := fixture.store.ListWorkspaces(context.Background(), pending.AccountID)
	if pending.Phase != "receipt_pending" || pending.Status != "manual_review" || pending.ErrorCode != "workspace_launch_receipt_attempt_unknown" ||
		persistedWorkspaceLaunchStageBudget(t, pending, "receipt")["unknown"] != float64(1) || len(workspaces) != 1 ||
		stringValue(workspaces[0]["runtimeId"]) == "" || len(fixture.ledger.receiptInputs) != 1 {
		t.Fatalf("receipt pending launch=%#v workspaces=%#v receipts=%#v", pending, workspaces, fixture.ledger.receiptInputs)
	}
	beforeEvents := append([]string(nil), (*fixture.events)...)
	beforeCharges := len(fixture.sub2API.charges)
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	stopped := fixture.operation(t)
	if stopped.Status != "manual_review" || stopped.Phase != "receipt_pending" || stopped.ErrorCode != "workspace_launch_receipt_attempt_unknown" ||
		len(fixture.ledger.receiptInputs) != 1 || len(fixture.sub2API.charges) != beforeCharges {
		t.Fatalf("receipt retry launch=%#v charges=%#v receipts=%#v", stopped, fixture.sub2API.charges, fixture.ledger.receiptInputs)
	}
	for _, event := range []string{"fabric.compute.prepare", "fabric.compute.sync", "fabric.storage.prepare", "fabric.storage.sync", "fabric.attachment", "fabric.gateway-secret", "fabric.runtime"} {
		if countStrings(*fixture.events, event) != countStrings(beforeEvents, event) {
			t.Fatalf("receipt retry repeated %s: before=%#v after=%#v", event, beforeEvents, *fixture.events)
		}
	}
}

func TestWorkspaceLaunchNoFabricBeforeDebit(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000}, []error{clients.ErrSub2APIChargeUnknown}, nil)
	err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service)
	operation := fixture.operation(t)
	if !errors.Is(err, clients.ErrSub2APIChargeUnknown) || operation.Phase != "debit_pending" || operation.ErrorCode != "sub2api_charge_unconfirmed" {
		t.Fatalf("unknown debit err=%v operation=%#v", err, operation)
	}
	if len(fixture.sub2API.charges) != 1 || fixture.sub2API.charges[0].ChargeUSDMicros != 52_580_000 || len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 || countStrings(*fixture.events, "fabric.attachment") != 0 || countStrings(*fixture.events, "fabric.runtime") != 0 {
		t.Fatalf("unconfirmed debit crossed fulfillment gate: events=%#v charges=%#v", *fixture.events, fixture.sub2API.charges)
	}
}

func TestWorkspaceLaunchRestartRecoversLostDebitResponse(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000}, nil, nil)
	gateway := &durableWorkspaceLaunchSub2API{
		workspaceLaunchSub2API: fixture.sub2API, balance: 1_000_000_000,
		appliedCharges: map[string]clients.Sub2APIChargeInput{}, loseNextResponses: 1,
	}
	service := controlplane.NewService(fixture.ledger, fixture.fabric, gateway)
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), service); !errors.Is(err, clients.ErrSub2APIChargeUnknown) {
		t.Fatalf("lost response error=%v", err)
	}
	fixture.fabric.preflightResults = []clients.MonthlyPreflight{{}, {}}
	restarted, err := newControlPlaneAppWithStore(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.runWorkspaceLaunchesOnce(context.Background(), service); err != nil {
		t.Fatal(err)
	}
	operation := fixture.operation(t)
	if operation.Status != "debited" || operation.Phase != "debited" || len(gateway.chargeCalls) != 1 || gateway.chargeCalls[0].ChargeUSDMicros != 52_580_000 {
		t.Fatalf("restart operation=%#v calls=%#v", operation, gateway.chargeCalls)
	}
	if len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 {
		t.Fatalf("restart crossed fulfillment gate: events=%#v", *fixture.events)
	}
}

func TestPostgresWorkspaceLaunchRestartAfterDebitRefundsProviderFailureExactlyOnce(t *testing.T) {
	t.Setenv("OPL_MONTHLY_BILLING_WORKER_ENABLED", "false")
	t.Setenv("OPL_PROVIDER_RECONCILE_WORKER_ENABLED", "false")
	t.Setenv("OPL_ARCHIVE_RETENTION_WORKER_ENABLED", "false")
	store := newPostgresWorkspaceRenewalStore(t)
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	events := []string{}
	sub2API := &workspaceLaunchSub2API{monthlySub2API: &monthlySub2API{events: &events, balances: []int64{1_000_000_000, 1_000_000_000, 947_420_000}}}
	fabric := &monthlyFabric{fakeFabricClient: fakeFabricClient{calls: &events}, events: &events}
	ledger := &workspaceLaunchLedger{events: &events, receipts: map[string]clients.Receipt{}}
	service := controlplane.NewService(ledger, fabric, sub2API)
	server, err := NewPersistentServer(service, store)
	if err != nil {
		t.Fatal(err)
	}
	session := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	created := requestWithMutationKeyForTest(t, server, session, http.MethodPost, "/api/workspace-launches", `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`, "launch-postgres-provider-failure")
	if created.Code != http.StatusAccepted {
		t.Fatalf("launch status=%d body=%s", created.Code, created.Body.String())
	}
	var response map[string]any
	if err := json.NewDecoder(created.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	operationID := stringValue(response["operationId"])
	first, err := newControlPlaneAppWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.runWorkspaceLaunchesOnce(context.Background(), service); err != nil {
		t.Fatal(err)
	}
	row, found, err := store.GetRuntimeOperation(context.Background(), operationID)
	if err != nil || !found {
		t.Fatalf("debited operation found=%t err=%v", found, err)
	}
	debited, err := decodeWorkspaceLaunchOperation(row)
	if err != nil || debited.Status != "debited" || debited.Phase != "debited" || len(sub2API.charges) != 1 || len(fabric.computeIDs) != 0 {
		t.Fatalf("debited operation=%#v err=%v charges=%#v compute=%#v", debited, err, sub2API.charges, fabric.computeIDs)
	}

	fabric.createErr = errors.New("provider failed after debit")
	fabric.computeSync = clients.ComputeAllocation{ID: debited.ComputeID, AccountID: debited.AccountID, WorkspaceID: debited.WorkspaceID, Status: "external_deleted"}
	fabric.storageSyncErr = &clients.FabricHTTPError{StatusCode: http.StatusInternalServerError, Body: `{"error":"storage_volume_not_found"}`}
	for range 2 {
		restarted, restartErr := newControlPlaneAppWithStore(store)
		if restartErr != nil {
			t.Fatal(restartErr)
		}
		if restartErr := restarted.runWorkspaceLaunchesOnce(context.Background(), service); restartErr != nil {
			t.Fatal(restartErr)
		}
	}

	row, found, err = store.GetRuntimeOperation(context.Background(), operationID)
	if err != nil || !found {
		t.Fatalf("refunded operation found=%t err=%v", found, err)
	}
	refunded, err := decodeWorkspaceLaunchOperation(row)
	if err != nil || refunded.Status != "refunded" || refunded.Phase != "refunded" {
		t.Fatalf("refunded operation=%#v err=%v", refunded, err)
	}
	if len(sub2API.charges) != 1 || len(sub2API.refunds) != 1 || sub2API.refunds[0].RefundUSDMicros != 52_580_000 ||
		len(fabric.computeIDs) != 1 || len(fabric.storageIDs) != 0 || countStrings(events, "fabric.compute.prepare") != 1 ||
		countStrings(events, "fabric.storage.get") != 1 || countStrings(events, "fabric.storage.sync") != 0 ||
		len(ledger.receiptInputs) != 1 || ledger.receiptInputs[0].Type != "billing.workspace_refunded.v1" {
		t.Fatalf("recovery charges=%#v refunds=%#v compute=%#v storage=%#v receipts=%#v events=%#v", sub2API.charges, sub2API.refunds, fabric.computeIDs, fabric.storageIDs, ledger.receiptInputs, events)
	}
}

func TestWorkspaceLaunchConcurrentWorkers(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000}, nil, nil)
	configureWorkspaceLaunchFulfillment(t, fixture)
	gateway := &durableWorkspaceLaunchSub2API{
		workspaceLaunchSub2API: fixture.sub2API, balance: 1_000_000_000,
		appliedCharges: map[string]clients.Sub2APIChargeInput{},
	}
	service := controlplane.NewService(fixture.ledger, fixture.fabric, gateway)
	second, err := newControlPlaneAppWithStore(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, app := range []*controlPlaneServer{fixture.app, second} {
		go func(app *controlPlaneServer) {
			<-start
			results <- app.runWorkspaceLaunchesOnce(context.Background(), service)
		}(app)
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	operation := fixture.operation(t)
	settled := operation.Status == "debited" && operation.Phase == "debited" || operation.Status == "succeeded" && operation.Phase == "succeeded"
	if !settled || len(gateway.chargeCalls) != 1 || gateway.chargeCalls[0].ChargeUSDMicros != 52_580_000 {
		t.Fatalf("concurrent operation=%#v calls=%#v", operation, gateway.chargeCalls)
	}
}

func TestWorkspaceLaunchCAS(t *testing.T) {
	t.Setenv("OPL_MONTHLY_BILLING_WORKER_ENABLED", "false")
	t.Setenv("OPL_PROVIDER_RECONCILE_WORKER_ENABLED", "false")
	t.Setenv("OPL_ARCHIVE_RETENTION_WORKER_ENABLED", "false")
	store := &workspaceLaunchClaimBarrierStore{memoryTableStore: newMemoryTableStore(), release: make(chan struct{})}
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	promoteWorkspaceLaunchOwner(t, store, "usr-alpha")

	newServer := func() (http.Handler, *httptest.ResponseRecorder) {
		events := []string{}
		gateway := &workspaceLaunchSub2API{
			monthlySub2API: &monthlySub2API{events: &events, balances: []int64{1_000_000_000}},
			keys:           map[int64]clients.Sub2APIWorkspaceKey{9: {ID: 9, UserID: 41, Name: "opl-workspace", Status: "active"}},
		}
		server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &monthlyFabric{events: &events}, gateway), store)
		if err != nil {
			t.Fatal(err)
		}
		return server, loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	}
	firstServer, firstSession := newServer()
	secondServer, secondSession := newServer()
	store.mu.Lock()
	store.armed = true
	store.mu.Unlock()

	results := make(chan *httptest.ResponseRecorder, 2)
	for index, pair := range []struct {
		server  http.Handler
		session *httptest.ResponseRecorder
	}{{firstServer, firstSession}, {secondServer, secondSession}} {
		go func(index int, pair struct {
			server  http.Handler
			session *httptest.ResponseRecorder
		}) {
			results <- requestWithMutationKeyForTest(t, pair.server, pair.session, http.MethodPost, "/api/workspace-launches", `{"name":"Alpha","packageId":"basic","sizeGb":10,"autoRenew":false}`, fmt.Sprintf("launch-cas-%d", index))
		}(index, pair)
	}
	accepted := 0
	operationIDs := make([]string, 0, 2)
	for range 2 {
		response := <-results
		if response.Code != http.StatusAccepted {
			t.Fatalf("CAS response status=%d body=%s", response.Code, response.Body.String())
		}
		accepted++
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		operationIDs = append(operationIDs, stringValue(body["operationId"]))
	}
	rows, err := store.memoryTableStore.ListRuntimeOperations(context.Background())
	if err != nil || accepted != 2 || len(rows) != 1 || stringValue(rows[0]["action"]) != "workspace.launch.v2" ||
		len(operationIDs) != 2 || operationIDs[0] == "" || operationIDs[0] != operationIDs[1] || operationIDs[0] != stringValue(rows[0]["id"]) {
		t.Fatalf("CAS accepted=%d operationIDs=%#v rows=%#v err=%v", accepted, operationIDs, rows, err)
	}
}

func (f workspaceLaunchWorkerFixture) operation(t *testing.T) workspaceLaunchOperation {
	t.Helper()
	rows, err := f.store.ListRuntimeOperations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	row := findRecord(rows, f.operationID)
	operation, err := decodeWorkspaceLaunchOperation(row)
	if err != nil {
		t.Fatal(err)
	}
	return operation
}

func TestWorkspaceLaunchRevalidatesOwnerBeforeDebit(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, workspaceLaunchWorkerFixture)
	}{
		{name: "disabled", mutate: func(t *testing.T, fixture workspaceLaunchWorkerFixture) {
			owner, err := fixture.app.findUserByID(context.Background(), "usr-alpha")
			if err != nil || owner == nil {
				t.Fatalf("find launch owner: owner=%#v err=%v", owner, err)
			}
			owner["status"] = "disabled"
			if err := fixture.store.ApplyUserLifecycle(context.Background(), owner); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "reciprocal mismatch", mutate: func(_ *testing.T, fixture workspaceLaunchWorkerFixture) {
			fixture.store.mu.Lock()
			fixture.store.accounts["acct-alpha"]["ownerUserId"] = "usr-other"
			fixture.store.mu.Unlock()
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000}, nil, nil)
			test.mutate(t, fixture)
			beforeEvents := append([]string(nil), (*fixture.events)...)
			if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err == nil {
				t.Fatal("invalid launch owner did not stop the worker")
			}
			operation := fixture.operation(t)
			if operation.Status != "manual_review" || operation.ErrorCode != "workspace_launch_owner_identity_mismatch" || len(fixture.sub2API.charges) != 0 ||
				len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 || !reflect.DeepEqual(*fixture.events, beforeEvents) {
				t.Fatalf("invalid owner operation=%#v events=%#v before=%#v", operation, *fixture.events, beforeEvents)
			}
		})
	}
}

func TestWorkspaceLaunchOwnerLifecycleFencesClaimBeforeDebit(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000}, nil, nil)
	fixture.store.lifecycleStarted = make(chan struct{})
	fixture.store.releaseLifecycle = make(chan struct{})
	fixture.store.workspaceLaunchClaimed = make(chan struct{})

	disableResult := make(chan error, 1)
	go func() {
		_, err := fixture.app.disableUser(map[string]any{"userId": "usr-alpha", "reason": "pilot_offboarding"})
		disableResult <- err
	}()
	select {
	case <-fixture.store.lifecycleStarted:
	case <-time.After(time.Second):
		t.Fatal("account disable did not enter the lifecycle transaction")
	}

	workerResult := make(chan error, 1)
	go func() { workerResult <- fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service) }()
	claimCrossedLifecycleFence := false
	select {
	case <-fixture.store.workspaceLaunchClaimed:
		claimCrossedLifecycleFence = true
	case <-time.After(100 * time.Millisecond):
	}
	close(fixture.store.releaseLifecycle)
	if err := <-disableResult; err != nil {
		t.Fatal(err)
	}
	if err := <-workerResult; err == nil {
		t.Fatal("disabled launch owner did not stop the worker")
	}
	if claimCrossedLifecycleFence {
		t.Fatal("Workspace launch claim crossed an in-progress owner lifecycle change")
	}
	operation := fixture.operation(t)
	if operation.Status != "manual_review" || operation.ErrorCode != "workspace_launch_owner_identity_mismatch" || len(fixture.sub2API.charges) != 0 {
		t.Fatalf("disabled owner operation=%#v charges=%#v", operation, fixture.sub2API.charges)
	}
}

func chargedWorkspaceLaunchReview(t *testing.T, fixture workspaceLaunchWorkerFixture) workspaceLaunchOperation {
	t.Helper()
	if err := fixture.app.runWorkspaceLaunchesOnce(context.Background(), fixture.service); err != nil {
		t.Fatal(err)
	}
	operation := fixture.operation(t)
	if operation.Status != "debited" || operation.ChargeConfirmation == nil || !operation.PostChargeBalanceKnown {
		t.Fatalf("launch was not charged exactly before review: %#v", operation)
	}
	operation.Status, operation.Phase, operation.ErrorCode = "manual_review", "storage_fulfilling", "fabric_storage_confirmed_absent_after_compute_created"
	mustStore(t, fixture.store.memoryTableStore.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(operation)))
	return fixture.operation(t)
}

func recoverWorkspaceLaunchForTest(t *testing.T, fixture workspaceLaunchWorkerFixture, key string) *httptest.ResponseRecorder {
	t.Helper()
	operation := fixture.operation(t)
	body := fmt.Sprintf(`{"accountId":%q,"billingOperationId":%q,"evidenceRef":"case-20260720-cbs"}`, operation.AccountID, operation.ID)
	return requestWithMutationKeyForTest(t, fixture.server, fixture.operator, http.MethodPost, "/api/operator/workspace-launches/"+operation.ID+"/recover", body, key)
}

func TestWorkspaceLaunchRecoveryRetriesAbsentStorageWithOriginalIdentity(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	operation := chargedWorkspaceLaunchReview(t, fixture)
	configureWorkspaceLaunchFulfillment(t, fixture)
	fixture.fabric.storageSync = clients.StorageVolume{
		ID: operation.StorageID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, Status: "external_deleted", CBSStatus: "NOT_FOUND",
	}
	fixture.fabric.providerTruth = &clients.MonthlyProviderTruth{
		ComputeState: "ready", StorageState: "absent", Compute: fixture.fabric.computeSync, Storage: fixture.fabric.storageSync,
	}
	fixture.fabric.mutateStorage = func(created *clients.StorageVolume) { fixture.fabric.storageSync = *created }

	response := recoverWorkspaceLaunchForTest(t, fixture, "launch-recovery-storage")
	if response.Code != http.StatusOK {
		t.Fatalf("storage recovery status=%d body=%s", response.Code, response.Body.String())
	}
	recovered := fixture.operation(t)
	if recovered.Status != "succeeded" || len(fixture.fabric.storageIDs) != 1 || fixture.fabric.storageIDs[0] != operation.StorageID ||
		len(fixture.fabric.storageCreateKeys) != 1 || fixture.fabric.storageCreateKeys[0] != operation.ID+":storage" ||
		len(fixture.fabric.storageInputs) != 1 || fixture.fabric.storageInputs[0].ID != operation.StorageID || len(fixture.sub2API.refunds) != 0 || len(fixture.sub2API.charges) != 1 {
		t.Fatalf("storage recovery=%#v ids=%#v keys=%#v inputs=%#v charges=%#v refunds=%#v", recovered, fixture.fabric.storageIDs, fixture.fabric.storageCreateKeys, fixture.fabric.storageInputs, fixture.sub2API.charges, fixture.sub2API.refunds)
	}
}

func TestWorkspaceLaunchRecoveryRefundsBothAbsentOnlyOnce(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
	operation := chargedWorkspaceLaunchReview(t, fixture)
	fixture.fabric.computeSync = clients.ComputeAllocation{ID: operation.ComputeID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, Status: "external_deleted"}
	fixture.fabric.storageSync = clients.StorageVolume{ID: operation.StorageID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, Status: "external_deleted", CBSStatus: "NOT_FOUND"}
	fixture.fabric.providerTruth = &clients.MonthlyProviderTruth{
		ComputeState: "absent", StorageState: "absent", Compute: fixture.fabric.computeSync, Storage: fixture.fabric.storageSync,
	}

	first := recoverWorkspaceLaunchForTest(t, fixture, "launch-recovery-refund")
	second := recoverWorkspaceLaunchForTest(t, fixture, "launch-recovery-refund")
	refunded := fixture.operation(t)
	if first.Code != http.StatusOK || second.Code != http.StatusOK || refunded.Status != "refunded" || len(fixture.sub2API.refunds) != 1 ||
		fixture.sub2API.refunds[0].Code != operation.RefundCode || fixture.sub2API.refunds[0].RefundUSDMicros != operation.TotalChargeUSDMicros ||
		len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 || len(fixture.sub2API.charges) != 1 {
		t.Fatalf("both-absent recovery first=%d second=%d operation=%#v charges=%#v refunds=%#v compute=%#v storage=%#v", first.Code, second.Code, refunded, fixture.sub2API.charges, fixture.sub2API.refunds, fixture.fabric.computeIDs, fixture.fabric.storageIDs)
	}
}

func TestWorkspaceLaunchRecoveryKeepsUnsafeProviderStatesInReview(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(workspaceLaunchWorkerFixture, workspaceLaunchOperation)
	}{
		{name: "unknown", setup: func(fixture workspaceLaunchWorkerFixture, _ workspaceLaunchOperation) {
			configureWorkspaceLaunchFulfillment(t, fixture)
			fixture.fabric.providerTruthErr = errors.New("provider truth unavailable")
		}},
		{name: "compute absent storage ready", setup: func(fixture workspaceLaunchWorkerFixture, operation workspaceLaunchOperation) {
			configureWorkspaceLaunchFulfillment(t, fixture)
			fixture.fabric.computeSync = clients.ComputeAllocation{ID: operation.ComputeID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, Status: "external_deleted"}
			fixture.fabric.providerTruth = &clients.MonthlyProviderTruth{
				ComputeState: "absent", StorageState: "ready", Compute: fixture.fabric.computeSync, Storage: fixture.fabric.storageSync,
			}
		}},
		{name: "absent state contradicts ready facts", setup: func(fixture workspaceLaunchWorkerFixture, _ workspaceLaunchOperation) {
			configureWorkspaceLaunchFulfillment(t, fixture)
			fixture.fabric.providerTruth = &clients.MonthlyProviderTruth{
				ComputeState: "absent", StorageState: "absent", Compute: fixture.fabric.computeSync, Storage: fixture.fabric.storageSync,
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
			operation := chargedWorkspaceLaunchReview(t, fixture)
			tc.setup(fixture, operation)
			response := recoverWorkspaceLaunchForTest(t, fixture, "launch-recovery-unsafe")
			current := fixture.operation(t)
			if response.Code != http.StatusOK || current.Status != "manual_review" || len(fixture.sub2API.refunds) != 0 ||
				len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 || len(fixture.sub2API.charges) != 1 ||
				countStrings(*fixture.events, "fabric.monthly-provider-truth") != 1 || countStrings(*fixture.events, "fabric.compute.sync") != 0 || countStrings(*fixture.events, "fabric.storage.sync") != 0 {
				t.Fatalf("unsafe recovery status=%d body=%s operation=%#v charges=%#v refunds=%#v compute=%#v storage=%#v", response.Code, response.Body.String(), current, fixture.sub2API.charges, fixture.sub2API.refunds, fixture.fabric.computeIDs, fixture.fabric.storageIDs)
			}
		})
	}
}

func TestWorkspaceLaunchRecoveryRejectsUnconfirmedChargeBeforeProviderTruth(t *testing.T) {
	fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000}, nil, nil)
	operation := fixture.operation(t)
	operation.Status, operation.Phase, operation.ErrorCode = "manual_review", "storage_fulfilling", "sub2api_charge_unconfirmed"
	mustStore(t, fixture.store.memoryTableStore.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(operation)))

	response := recoverWorkspaceLaunchForTest(t, fixture, "launch-recovery-unconfirmed")
	current := fixture.operation(t)
	if response.Code != http.StatusOK || current.Status != "manual_review" || current.ErrorCode != "workspace_launch_charge_unconfirmed" ||
		len(fixture.sub2API.charges) != 0 || len(fixture.sub2API.refunds) != 0 || countStrings(*fixture.events, "fabric.monthly-provider-truth") != 0 ||
		len(fixture.fabric.computeIDs) != 0 || len(fixture.fabric.storageIDs) != 0 {
		t.Fatalf("unconfirmed charge recovery status=%d body=%s operation=%#v events=%#v charges=%#v refunds=%#v", response.Code, response.Body.String(), current, *fixture.events, fixture.sub2API.charges, fixture.sub2API.refunds)
	}
}

func TestWorkspaceLaunchRecoveryRetriesOnlyReceiptAfterLedgerFailure(t *testing.T) {
	t.Run("purchase", func(t *testing.T) {
		t.Setenv("OPL_INTERNAL_SERVICE_TOKEN", "workspace-launch-readback-capability")
		scenario := newWorkspaceLaunchReadbackRecoveryScenario(t, "receipt", "basic")
		fixture := scenario.fixture
		fixture.service = controlplane.NewService(fixture.ledger, scenario.readback, fixture.sub2API)
		server, err := NewPersistentServer(fixture.service, fixture.store)
		if err != nil {
			t.Fatal(err)
		}
		fixture.server, fixture.operator = server, reservedOperatorSessionForTest(t, server)
		beforeEvents := append([]string(nil), (*fixture.events)...)
		beforeCharges := len(fixture.sub2API.charges)

		key := "launch-recovery-purchase-receipt"
		approval := testWorkspaceLaunchReadbackApproval(t, scenario.approvalOperation, "receipt", key, scenario.readback)
		response := requestWorkspaceLaunchReadbackRecovery(t, fixture, approval, key)
		current := fixture.operation(t)
		if response.Code != http.StatusOK || current.Status != "succeeded" || current.Phase != "succeeded" || current.ReceiptID == "" ||
			len(fixture.ledger.receiptInputs) != scenario.beforeCurrentWrites || len(fixture.sub2API.charges) != beforeCharges ||
			countStrings(*fixture.events, "fabric.monthly-provider-truth") != countStrings(beforeEvents, "fabric.monthly-provider-truth")+1 {
			t.Fatalf("purchase receipt recovery status=%d body=%s operation=%#v charges=%#v receipts=%#v", response.Code, response.Body.String(), current, fixture.sub2API.charges, fixture.ledger.receiptInputs)
		}
		assertNoWorkspaceLaunchRecoveryFabricWrites(t, beforeEvents, *fixture.events)
	})

	t.Run("refund", func(t *testing.T) {
		fixture := newWorkspaceLaunchWorkerFixture(t, []int64{1_000_000_000, 1_000_000_000, 947_420_000}, nil, nil)
		operation := chargedWorkspaceLaunchReview(t, fixture)
		fixture.fabric.providerTruth = &clients.MonthlyProviderTruth{
			ComputeState: "absent", StorageState: "absent",
			Compute: clients.ComputeAllocation{ID: operation.ComputeID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, Status: "external_deleted"},
			Storage: clients.StorageVolume{ID: operation.StorageID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID, Status: "external_deleted", CBSStatus: "NOT_FOUND"},
		}
		fixture.ledger.receiptErrors = []error{errors.New("Ledger unavailable"), nil}
		first := recoverWorkspaceLaunchForTest(t, fixture, "launch-recovery-refund-receipt")
		operation = fixture.operation(t)
		if first.Code != http.StatusOK || operation.Phase != "refund_pending" || operation.RefundConfirmation == nil || len(fixture.sub2API.refunds) != 1 {
			t.Fatalf("refund receipt setup status=%d body=%s operation=%#v refunds=%#v", first.Code, first.Body.String(), operation, fixture.sub2API.refunds)
		}
		operation.Status = "manual_review"
		mustStore(t, fixture.store.memoryTableStore.SaveRuntimeOperation(context.Background(), workspaceLaunchOperationRow(operation)))
		beforeEvents := append([]string(nil), (*fixture.events)...)
		beforeCharges, beforeRefunds := len(fixture.sub2API.charges), len(fixture.sub2API.refunds)

		second := recoverWorkspaceLaunchForTest(t, fixture, "launch-recovery-refund-receipt")
		current := fixture.operation(t)
		if second.Code != http.StatusOK || current.Status != "refunded" || current.Phase != "refunded" || len(fixture.ledger.receiptInputs) != 2 ||
			len(fixture.sub2API.charges) != beforeCharges || len(fixture.sub2API.refunds) != beforeRefunds {
			t.Fatalf("refund receipt recovery status=%d body=%s operation=%#v charges=%#v refunds=%#v receipts=%#v", second.Code, second.Body.String(), current, fixture.sub2API.charges, fixture.sub2API.refunds, fixture.ledger.receiptInputs)
		}
		assertNoWorkspaceLaunchRecoveryFabricWrites(t, beforeEvents, *fixture.events)
	})
}

func assertNoWorkspaceLaunchRecoveryFabricWrites(t *testing.T, before, after []string) {
	t.Helper()
	for _, event := range []string{"fabric.compute.prepare", "fabric.compute.sync", "fabric.storage.prepare", "fabric.storage.sync", "fabric.attachment", "fabric.gateway-secret", "fabric.runtime"} {
		if countStrings(after, event) != countStrings(before, event) {
			t.Fatalf("receipt-only recovery repeated %s: before=%#v after=%#v", event, before, after)
		}
	}
}

func countStrings(values []string, target string) int {
	count := 0
	for _, value := range values {
		if value == target {
			count++
		}
	}
	return count
}
