package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

type workspaceLaunchMonthlyPreflightFabric struct {
	*gatewayAccountingFabric
	events      *[]string
	failureMode string
}

func (f *workspaceLaunchMonthlyPreflightFabric) PreflightWorkspaceLaunch(_ context.Context, input clients.WorkspaceLaunchPreflightInput) (clients.WorkspaceLaunchPreflight, error) {
	*f.events = append(*f.events, "fabric.workspace.preflight")
	return clients.WorkspaceLaunchPreflight{
		SchemaVersion:      clients.WorkspaceLaunchFabricSchemaVersion,
		Available:          true,
		Reason:             "none",
		LaunchOperationID:  input.LaunchOperationID,
		RequestHash:        input.RequestHash,
		ProviderProfileRef: "provider-profile",
		BindingRef:         "workspace-binding",
	}, nil
}

func (f *workspaceLaunchMonthlyPreflightFabric) MonthlyPreflight(_ context.Context, input clients.MonthlyPreflightInput) (clients.MonthlyPreflight, error) {
	*f.events = append(*f.events, "fabric.monthly."+input.ResourceType)
	if f.failureMode == input.ResourceType+"_error" {
		return clients.MonthlyPreflight{}, errors.New("monthly preflight unavailable")
	}
	result := clients.MonthlyPreflight{
		ResourceType: input.ResourceType,
		PackageID:    input.PackageID,
		SizeGB:       input.SizeGB,
		Zone:         input.Zone,
		Available:    f.failureMode != input.ResourceType+"_unavailable",
	}
	if f.failureMode == input.ResourceType+"_invalid" {
		result.PackageID = "drifted"
	}
	return result, nil
}

func (f *workspaceLaunchMonthlyPreflightFabric) EnsureWorkspaceLaunchStage(ctx context.Context, input clients.WorkspaceLaunchStageInput) (clients.WorkspaceLaunchStageResult, error) {
	*f.events = append(*f.events, "fabric.stage."+input.Binding.Stage)
	return f.gatewayAccountingFabric.EnsureWorkspaceLaunchStage(ctx, input)
}

type workspaceLaunchMonthlyPreflightSub2API struct {
	*testSub2APIClient
	events *[]string
	keys   []clients.Sub2APIWorkspaceKey
}

func (c *workspaceLaunchMonthlyPreflightSub2API) UserGroups(_ context.Context, credential clients.SessionDelegatedCredential, userID int64) ([]clients.Sub2APIGroup, error) {
	if credential.Bearer != "test-user-delegated-token" || userID != 41 {
		return nil, errors.New("wrong delegated credential")
	}
	return []clients.Sub2APIGroup{{ID: 7, Name: "Codex", Status: "active"}}, nil
}

func (c *workspaceLaunchMonthlyPreflightSub2API) WorkspaceKeysForConvergence(_ context.Context, userID int64, name string) ([]clients.Sub2APIWorkspaceKey, error) {
	result := make([]clients.Sub2APIWorkspaceKey, 0, len(c.keys))
	for _, key := range c.keys {
		if key.UserID == userID && key.Name == name {
			result = append(result, key)
		}
	}
	return result, nil
}

func (c *workspaceLaunchMonthlyPreflightSub2API) CreateUserKey(_ context.Context, _ clients.SessionDelegatedCredential, userID int64, input clients.Sub2APICreateKeyInput, _ string) (clients.Sub2APIWorkspaceKey, error) {
	*c.events = append(*c.events, "sub2api.workspace-key")
	groupID := input.GroupID
	key := clients.Sub2APIWorkspaceKey{ID: 19, UserID: userID, Name: input.Name, Key: "workspace-key-secret", GroupID: &groupID, Status: "active"}
	c.keys = append(c.keys, key)
	return key, nil
}

func (*workspaceLaunchMonthlyPreflightSub2API) UpdateUserKey(context.Context, clients.SessionDelegatedCredential, int64, int64, clients.Sub2APIUpdateKeyInput) (clients.Sub2APIWorkspaceKey, error) {
	return clients.Sub2APIWorkspaceKey{}, errors.New("unexpected key update")
}

func (*workspaceLaunchMonthlyPreflightSub2API) DeleteUserKey(context.Context, clients.SessionDelegatedCredential, int64, int64) error {
	return errors.New("unexpected key delete")
}

func (c *workspaceLaunchMonthlyPreflightSub2API) Charge(ctx context.Context, input clients.Sub2APIChargeInput) (clients.Sub2APICharge, error) {
	*c.events = append(*c.events, "sub2api.charge")
	return c.testSub2APIClient.Charge(ctx, input)
}

func (c *workspaceLaunchMonthlyPreflightSub2API) FinancialBalanceHistoryByCodes(_ context.Context, _ int64, codes []string) (map[string]clients.Sub2APIBalanceHistoryEntry, error) {
	c.testSub2APIClient.mu.Lock()
	defer c.testSub2APIClient.mu.Unlock()
	history := map[string]clients.Sub2APIBalanceHistoryEntry{}
	usedAt := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	userID := int64(41)
	for _, code := range codes {
		charge, ok := c.testSub2APIClient.charges[code]
		if ok {
			history[code] = clients.Sub2APIBalanceHistoryEntry{
				Code: code, Type: "balance", ValueUSDMicros: -charge, Status: "used", UsedBy: &userID, UsedAt: &usedAt, CreatedAt: usedAt,
			}
		}
	}
	return history, nil
}

func newWorkspaceLaunchMonthlyPreflightFixture(t *testing.T, failureMode string) (http.Handler, *memoryTableStore, *workspaceLaunchMonthlyPreflightSub2API, *[]string) {
	t.Helper()
	events := []string{}
	client := &workspaceLaunchMonthlyPreflightSub2API{
		testSub2APIClient: &testSub2APIClient{
			balance: 100_000_000, charges: map[string]int64{},
			identities: map[string]clients.Sub2APIIdentity{"alpha@example.com": {ID: 41, Email: "alpha@example.com", Status: "active"}},
			passwords:  map[string]string{"alpha@example.com": "CorrectHorseBatteryStaple!"},
		},
		events: &events,
	}
	fabric := &workspaceLaunchMonthlyPreflightFabric{
		gatewayAccountingFabric: newGatewayAccountingFabric(),
		events:                  &events,
		failureMode:             failureMode,
	}
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, fabric, client), store)
	if err != nil {
		t.Fatal(err)
	}
	return server, store, client, &events
}

func TestWorkspaceLaunchMonthlyPreflightFailureBlocksDebitAndFabricMutation(t *testing.T) {
	t.Setenv(controlledBasicPilotEnabledEnv, "1")
	t.Setenv(controlledBasicPilotAccountsEnv, "acct-alpha")
	t.Setenv("OPL_TENCENT_ZONE", "ap-guangzhou-1")
	t.Setenv("OPL_WORKSPACE_LAUNCH_WORKER_ENABLED", "0")

	for _, failureMode := range []string{
		"compute_error", "compute_unavailable", "compute_invalid",
		"storage_error", "storage_unavailable", "storage_invalid",
	} {
		t.Run(failureMode, func(t *testing.T) {
			server, store, client, events := newWorkspaceLaunchMonthlyPreflightFixture(t, failureMode)
			session := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")

			response := requestWithMutationKeyForTest(t, server, session, http.MethodPost, "/api/workspace-launches",
				`{"name":"Monthly preflight failure","packageId":"basic","sizeGb":10,"autoRenew":false}`,
				"monthly-preflight-"+failureMode)
			if response.Code != http.StatusAccepted {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			handler := server.(*controlPlaneHTTPHandler)
			_ = handler.app.runWorkspaceLaunchesOnce(context.Background(), handler.service)
			operations, err := store.ListRuntimeOperations(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(operations) != 1 || len(client.charges) != 0 {
				t.Fatalf("monthly %s failure crossed debit: operations=%#v charges=%#v events=%#v", failureMode, operations, client.charges, *events)
			}
			for _, event := range *events {
				if strings.HasPrefix(event, "fabric.stage.") || event == "sub2api.charge" {
					t.Fatalf("monthly %s failure crossed mutation boundary: events=%#v", failureMode, *events)
				}
			}
		})
	}
}

func TestWorkspaceLaunchMissingProviderZoneParksReservedDebitWithoutAuthorityWrite(t *testing.T) {
	t.Setenv(controlledBasicPilotEnabledEnv, "1")
	t.Setenv(controlledBasicPilotAccountsEnv, "acct-alpha")
	t.Setenv("OPL_TENCENT_ZONE", "")
	t.Setenv("OPL_WORKSPACE_LAUNCH_WORKER_ENABLED", "0")

	server, store, client, events := newWorkspaceLaunchMonthlyPreflightFixture(t, "")
	session := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	response := requestWithMutationKeyForTest(t, server, session, http.MethodPost, "/api/workspace-launches",
		`{"name":"Missing local provider zone","packageId":"basic","sizeGb":10,"autoRenew":false}`,
		"missing-local-provider-zone")
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}

	handler := server.(*controlPlaneHTTPHandler)
	if err := handler.app.runWorkspaceLaunchesOnce(context.Background(), handler.service); err == nil || !errors.Is(err, errWorkspaceLaunchMonthlyPreflightInvalid) {
		t.Fatalf("run launch error=%v, want %v", err, errWorkspaceLaunchMonthlyPreflightInvalid)
	}
	rows, err := store.ListRuntimeOperations(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("read launch operations=%#v err=%v", rows, err)
	}
	operation, err := decodeWorkspaceLaunchReconcileOperation(rows[0])
	if err != nil {
		t.Fatal(err)
	}
	attempt := operation.Attempts["debit"]
	if operation.Status != "manual_review" || operation.Stage != "debit" ||
		attempt.Attempted != 1 || attempt.Confirmed != 0 || attempt.Unknown != 1 || attempt.Max != 1 || attempt.Status != "unknown" ||
		operation.Observations["debit"].State != workspaceLaunchStageUnknown {
		t.Fatalf("unexpected debit failure transition: operation=%#v attempt=%#v", operation, attempt)
	}
	if len(client.charges) != 0 {
		t.Fatalf("missing provider zone wrote debit authority: charges=%#v events=%#v", client.charges, *events)
	}
}

func TestWorkspaceLaunchMonthlyPreflightRunsBeforeDebitAndProviderStages(t *testing.T) {
	t.Setenv(controlledBasicPilotEnabledEnv, "1")
	t.Setenv(controlledBasicPilotAccountsEnv, "acct-alpha")
	t.Setenv("OPL_TENCENT_ZONE", "ap-guangzhou-1")
	t.Setenv("OPL_WORKSPACE_LAUNCH_WORKER_ENABLED", "0")

	server, store, client, events := newWorkspaceLaunchMonthlyPreflightFixture(t, "")
	session := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	response := requestWithMutationKeyForTest(t, server, session, http.MethodPost, "/api/workspace-launches",
		`{"name":"Monthly preflight success","packageId":"basic","sizeGb":10,"autoRenew":false}`,
		"monthly-preflight-success")
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(client.charges) != 0 {
		t.Fatalf("launch route debited before worker: charges=%#v events=%#v", client.charges, *events)
	}

	handler := server.(*controlPlaneHTTPHandler)
	for range 20 {
		if err := handler.app.runWorkspaceLaunchesOnce(context.Background(), handler.service); err != nil {
			t.Fatalf("run launch: %v", err)
		}
		if len(*events) >= 10 {
			break
		}
	}
	operations, err := store.ListRuntimeOperations(context.Background())
	if err != nil || len(operations) != 1 {
		t.Fatalf("read launch operations=%#v err=%v", operations, err)
	}
	key, monthlyCompute, monthlyStorage, debit, firstFabricStage := -1, -1, -1, -1, -1
	for index, event := range *events {
		switch event {
		case "sub2api.workspace-key":
			key = index
		case "fabric.monthly.compute":
			monthlyCompute = index
		case "fabric.monthly.storage":
			monthlyStorage = index
		case "sub2api.charge":
			debit = index
		}
		if firstFabricStage < 0 && strings.HasPrefix(event, "fabric.stage.") {
			firstFabricStage = index
		}
	}
	if key < 0 || monthlyCompute < 0 || monthlyStorage < 0 || debit < 0 || firstFabricStage < 0 ||
		key >= monthlyCompute || monthlyCompute >= monthlyStorage || monthlyStorage >= debit || debit >= firstFabricStage {
		t.Fatalf("want key < compute < storage < debit < first Fabric stage: events=%#v", *events)
	}
}
