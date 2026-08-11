package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

type workspaceLaunchResumeRouteSub2API struct {
	*testSub2APIClient
	keys              []clients.Sub2APIWorkspaceKey
	credentials       []clients.SessionDelegatedCredential
	expectedCreateKey string
	convergenceReads  int
	createCalls       int
}

func (c *workspaceLaunchResumeRouteSub2API) WorkspaceKeysForConvergence(_ context.Context, userID int64, name string) ([]clients.Sub2APIWorkspaceKey, error) {
	if userID != 41 || name == "" {
		return nil, errors.New("wrong workspace key identity")
	}
	c.convergenceReads++
	result := make([]clients.Sub2APIWorkspaceKey, 0, len(c.keys))
	for _, key := range c.keys {
		if key.UserID == userID && key.Name == name {
			result = append(result, key)
		}
	}
	return result, nil
}

func (c *workspaceLaunchResumeRouteSub2API) CreateUserKey(_ context.Context, credential clients.SessionDelegatedCredential, userID int64, input clients.Sub2APICreateKeyInput, idempotencyKey string) (clients.Sub2APIWorkspaceKey, error) {
	c.credentials = append(c.credentials, credential)
	if credential.Bearer != "test-user-delegated-token" || userID != 41 || input.GroupID != 7 || idempotencyKey != c.expectedCreateKey {
		return clients.Sub2APIWorkspaceKey{}, errors.New("wrong delegated key mutation")
	}
	c.createCalls++
	groupID := input.GroupID
	key := clients.Sub2APIWorkspaceKey{ID: 19, UserID: userID, Name: input.Name, Key: "route-created-key-secret", GroupID: &groupID, Status: "active"}
	c.keys = append(c.keys, key)
	return key, nil
}

func (*workspaceLaunchResumeRouteSub2API) UpdateUserKey(context.Context, clients.SessionDelegatedCredential, int64, int64, clients.Sub2APIUpdateKeyInput) (clients.Sub2APIWorkspaceKey, error) {
	return clients.Sub2APIWorkspaceKey{}, errors.New("unexpected key update")
}

func (*workspaceLaunchResumeRouteSub2API) DeleteUserKey(context.Context, clients.SessionDelegatedCredential, int64, int64) error {
	return errors.New("unexpected key delete")
}

func TestWorkspaceLaunchResumeRouteWaitsForOriginalCallerCredential(t *testing.T) {
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	client := &workspaceLaunchResumeRouteSub2API{testSub2APIClient: &testSub2APIClient{
		balance: 100_000_000, charges: map[string]int64{},
		identities: map[string]clients.Sub2APIIdentity{"alpha@example.com": {ID: 41, Email: "alpha@example.com", Status: "active"}},
		passwords:  map[string]string{"alpha@example.com": "CorrectHorseBatteryStaple!"},
	}}
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, client), store)
	if err != nil {
		t.Fatal(err)
	}
	operator := reservedOperatorSessionForTest(t, server)
	customer := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	launchKey := "launch-route-key"
	command := workspaceLaunchUnitCommand()
	command.OperationID = workspaceLaunchOperationID("acct-alpha", launchKey)
	command.AccountID = "acct-alpha"
	command.OwnerUserID = "usr-alpha"
	command.Sub2APIUserID = 41
	command.WorkspaceID = "ws-route-resume"
	command.Name = "Route Resume"
	client.expectedCreateKey = command.OperationID + ":workspace-key"
	operation, err := newWorkspaceLaunchReconcileOperation(command)
	if err != nil {
		t.Fatal(err)
	}
	operation.Status = "manual_review"
	row, err := workspaceLaunchReconcileOperationRow(operation)
	if err != nil {
		t.Fatal(err)
	}
	mustStore(t, store.SaveRuntimeOperation(context.Background(), row))

	resumeBody := `{"launchVersion":1,"authorizedStage":"key","reason":"bounded operator retry","mutationBudget":1}`
	resume := requestWithMutationKeyForTest(t, server, operator, http.MethodPost, "/api/operator/workspace-launches/"+operation.ID+"/resume", resumeBody, "resume-route-key")
	if resume.Code != http.StatusOK {
		t.Fatalf("operator resume status=%d body=%s", resume.Code, resume.Body.String())
	}
	waitingRow, found, err := store.GetRuntimeOperation(context.Background(), operation.ID)
	if err != nil || !found {
		t.Fatalf("read waiting launch found=%v err=%v", found, err)
	}
	waiting, err := decodeWorkspaceLaunchReconcileOperation(waitingRow)
	if err != nil || waiting.Status != "pending" || waiting.Stage != "key" || waiting.Attempts["key"].Attempted != 0 ||
		waiting.ResumeAuthorization == nil || waiting.ResumeAuthorization.AuthorizationID != "resume-route-key" || waiting.ResumeAuthorizationConsumedAt != "" || client.createCalls != 0 {
		t.Fatalf("operator consumed credential-bound authorization: operation=%s creates=%d err=%v", workspaceLaunchReconcileResultSummary(waiting), client.createCalls, err)
	}

	launchBody := `{"name":"Route Resume","packageId":"basic","sizeGb":10,"autoRenew":false}`
	continuedResponse := requestWithMutationKeyForTest(t, server, customer, http.MethodPost, "/api/workspace-launches", launchBody, launchKey)
	if continuedResponse.Code != http.StatusAccepted {
		t.Fatalf("customer continuation status=%d body=%s", continuedResponse.Code, continuedResponse.Body.String())
	}
	continuedRow, found, err := store.GetRuntimeOperation(context.Background(), operation.ID)
	if err != nil || !found {
		t.Fatalf("read continued launch found=%v err=%v", found, err)
	}
	continued, err := decodeWorkspaceLaunchReconcileOperation(continuedRow)
	if err != nil || continued.Status != "pending" || continued.Stage != "debit" || continued.Attempts["key"].Attempted != 1 ||
		continued.ResumeAuthorization == nil || continued.ResumeAuthorization.AuthorizationID != "resume-route-key" || continued.ResumeAuthorizationConsumedAt == "" ||
		client.createCalls != 1 || len(client.credentials) != 1 || client.credentials[0].Bearer != "test-user-delegated-token" {
		t.Fatalf("customer continuation operation=%s creates=%d credentials=%#v err=%v", workspaceLaunchReconcileResultSummary(continued), client.createCalls, client.credentials, err)
	}
	if strings.Contains(stringValue(continuedRow["result"]), "test-user-delegated-token") {
		t.Fatal("persisted launch result contains delegated bearer")
	}

	readsBefore := client.convergenceReads
	exactResume := requestWithMutationKeyForTest(t, server, operator, http.MethodPost, "/api/operator/workspace-launches/"+operation.ID+"/resume", resumeBody, "resume-route-key")
	exactLaunch := requestWithMutationKeyForTest(t, server, customer, http.MethodPost, "/api/workspace-launches", launchBody, launchKey)
	if exactResume.Code != http.StatusOK || exactLaunch.Code != http.StatusAccepted || client.createCalls != 1 || client.convergenceReads != readsBefore {
		t.Fatalf("exact retries caused work: resume=%d launch=%d creates=%d reads=%d/%d", exactResume.Code, exactLaunch.Code, client.createCalls, client.convergenceReads, readsBefore)
	}
}
