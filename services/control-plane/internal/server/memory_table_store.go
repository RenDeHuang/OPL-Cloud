package server

import (
	"context"
	"sort"
	"sync"
	"time"
)

type memoryTableStore struct {
	mu             sync.Mutex
	accounts       controlPlaneRecordSet
	users          controlPlaneRecordSet
	sessions       controlPlaneRecordSet
	organizations  controlPlaneRecordSet
	memberships    controlPlaneRecordSet
	computes       controlPlaneRecordSet
	storages       controlPlaneRecordSet
	attachments    controlPlaneRecordSet
	workspaces     controlPlaneRecordSet
	backups        controlPlaneRecordSet
	wallets        controlPlaneRecordSet
	ledger         []map[string]any
	walletTx       []map[string]any
	topups         []map[string]any
	auditEvents    []map[string]any
	support        controlPlaneRecordSet
	runtimeOps     []map[string]any
	projectTasks   controlPlaneRecordSet
	syncEvents     []map[string]any
	executionReqs  controlPlaneRecordSet
	reconciliation map[string]any
}

func newMemoryTableStore() *memoryTableStore {
	return &memoryTableStore{
		accounts:      controlPlaneRecordSet{"acct-admin": {"id": "acct-admin", "status": "active"}, "acct-alpha": {"id": "acct-alpha", "status": "active"}},
		users:         controlPlaneRecordSet{"usr-admin": {"id": "usr-admin", "email": "admin@medopl.cn", "accountId": "acct-alpha", "role": "admin", "status": "active"}},
		sessions:      controlPlaneRecordSet{},
		organizations: controlPlaneRecordSet{"org-alpha": {"id": "org-alpha", "billingAccountId": "acct-alpha", "status": "active"}},
		memberships:   controlPlaneRecordSet{"mem-admin-alpha": {"id": "mem-admin-alpha", "organizationId": "org-alpha", "userId": "usr-admin", "accountId": "acct-alpha", "role": "admin", "status": "active"}},
		computes:      controlPlaneRecordSet{},
		storages:      controlPlaneRecordSet{},
		attachments:   controlPlaneRecordSet{},
		workspaces:    controlPlaneRecordSet{},
		backups:       controlPlaneRecordSet{},
		wallets:       controlPlaneRecordSet{},
		support:       controlPlaneRecordSet{},
		projectTasks:  controlPlaneRecordSet{},
		executionReqs: controlPlaneRecordSet{},
	}
}

func (s *memoryTableStore) ListAccounts(_ context.Context) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filteredRecords(s.accounts, "")
}

func (s *memoryTableStore) SaveAccount(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accounts[stringValue(row["id"])] = cloneMap(row)
	return nil
}

func (s *memoryTableStore) ListWorkspaceBackups(_ context.Context, workspaceID string) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]any, 0)
	for _, row := range s.backups {
		if workspaceID == "" || stringValue(row["workspaceId"]) == workspaceID {
			out = append(out, cloneMap(row))
		}
	}
	sort.Slice(out, func(i, j int) bool { return stringValue(out[i]["createdAt"]) < stringValue(out[j]["createdAt"]) })
	return out, nil
}

func (s *memoryTableStore) SaveWorkspaceBackup(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.backups {
		if stringValue(existing["idempotencyKey"]) != stringValue(row["idempotencyKey"]) {
			continue
		}
		if stringValue(existing["requestHash"]) != stringValue(row["requestHash"]) {
			return errIdempotencyConflict
		}
		s.backups[stringValue(existing["id"])] = cloneMap(row)
		return nil
	}
	s.backups[stringValue(row["id"])] = cloneMap(row)
	return nil
}

func (s *memoryTableStore) ListUsers(_ context.Context, includeDeleted bool) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]any, 0, len(s.users))
	for _, row := range s.users {
		if !includeDeleted && stringValue(row["status"]) == "deleted" {
			continue
		}
		out = append(out, cloneMap(row))
	}
	return out, nil
}

func (s *memoryTableStore) SaveUser(_ context.Context, row map[string]any) error {
	if !validRole(stringValue(row["role"])) {
		return errInvalidRole
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[stringValue(row["id"])] = cloneMap(row)
	return nil
}

func (s *memoryTableStore) DeleteUser(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.users, id)
	return nil
}

func (s *memoryTableStore) ListSessions(_ context.Context) (controlPlaneRecordSet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneStateTable(s.sessions), nil
}

func (s *memoryTableStore) SaveSession(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[stringValue(row["id"])] = cloneMap(row)
	return nil
}

func (s *memoryTableStore) DeleteSession(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
	return nil
}

func (s *memoryTableStore) ListOrganizations(_ context.Context) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filteredRecords(s.organizations, "")
}

func (s *memoryTableStore) SaveOrganization(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.accounts[stringValue(row["billingAccountId"])] == nil {
		return errAccountNotFound
	}
	s.organizations[stringValue(row["id"])] = cloneMap(row)
	return nil
}

func (s *memoryTableStore) ListMemberships(_ context.Context) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filteredRecords(s.memberships, "")
}

func (s *memoryTableStore) SaveMembership(_ context.Context, row map[string]any) error {
	if !validRole(stringValue(row["role"])) {
		return errInvalidRole
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	accountID := stringValue(row["accountId"])
	organization := s.organizations[stringValue(row["organizationId"])]
	user := s.users[stringValue(row["userId"])]
	if s.accounts[accountID] == nil {
		return errAccountNotFound
	}
	if organization == nil {
		return errOrganizationNotFound
	}
	if user == nil {
		return errMembershipUserNotFound
	}
	if stringValue(organization["billingAccountId"]) != accountID || stringValue(user["accountId"]) != accountID {
		return errMembershipAccountMismatch
	}
	s.memberships[stringValue(row["id"])] = cloneMap(row)
	return nil
}

func (s *memoryTableStore) ListProjectTaskSyncHeads(_ context.Context) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filteredRecords(s.projectTasks, "")
}

func (s *memoryTableStore) SaveProjectTaskSyncHead(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.projectTasks[stringValue(row["id"])] = cloneMap(row)
	return nil
}

func (s *memoryTableStore) ListWorkspaceSyncEvents(_ context.Context, workspaceID string, after int64, limit int) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := make([]map[string]any, 0, len(s.syncEvents))
	for _, row := range s.syncEvents {
		if stringValue(row["workspaceId"]) == workspaceID && int64(numberField(row, "cursor", 0)) > after {
			rows = append(rows, cloneMap(row))
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		return numberField(rows[i], "cursor", 0) < numberField(rows[j], "cursor", 0)
	})
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func (s *memoryTableStore) SaveWorkspaceSyncEvent(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.syncEvents {
		if stringValue(existing["id"]) == stringValue(row["id"]) || stringValue(existing["idempotencyKey"]) == stringValue(row["idempotencyKey"]) || (stringValue(existing["workspaceId"]) == stringValue(row["workspaceId"]) && stringValue(existing["operationId"]) == stringValue(row["operationId"])) {
			if stringValue(existing["requestHash"]) == stringValue(row["requestHash"]) {
				return nil
			}
			return errIdempotencyConflict
		}
	}
	s.syncEvents = append(s.syncEvents, cloneMap(row))
	return nil
}

func (s *memoryTableStore) ListExecutionRequests(_ context.Context) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filteredRecords(s.executionReqs, "")
}

func (s *memoryTableStore) SaveExecutionRequest(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executionReqs[stringValue(row["id"])] = cloneMap(row)
	return nil
}

func (s *memoryTableStore) ListComputes(_ context.Context, accountID string) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filteredRecords(s.computes, accountID)
}

func (s *memoryTableStore) SaveCompute(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.computes[stringValue(row["id"])] = cloneMap(row)
	return nil
}

func (s *memoryTableStore) DeleteCompute(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.computes, id)
	return nil
}

func (s *memoryTableStore) ListStorages(_ context.Context, accountID string) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filteredRecords(s.storages, accountID)
}

func (s *memoryTableStore) SaveStorage(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.storages[stringValue(row["id"])] = cloneMap(row)
	return nil
}

func (s *memoryTableStore) DeleteStorage(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.storages, id)
	return nil
}

func (s *memoryTableStore) ListAttachments(_ context.Context, accountID string) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filteredRecords(s.attachments, accountID)
}

func (s *memoryTableStore) SaveAttachment(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attachments[stringValue(row["id"])] = cloneMap(row)
	return nil
}

func (s *memoryTableStore) DeleteAttachment(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.attachments, id)
	return nil
}

func (s *memoryTableStore) ListWorkspaces(_ context.Context, accountID string) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filteredRecords(s.workspaces, accountID)
}

func (s *memoryTableStore) SaveWorkspace(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row = cloneMap(row)
	access, _ := row["access"].(map[string]any)
	access = cloneMap(access)
	delete(access, "password")
	row["access"] = access
	s.workspaces[stringValue(row["id"])] = row
	return nil
}

func (s *memoryTableStore) ClaimWorkspaceResume(_ context.Context, workspaceID string, operation map[string]any) (map[string]any, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	for index := range s.runtimeOps {
		existing := s.runtimeOps[index]
		if stringValue(existing["id"]) != stringValue(operation["id"]) {
			continue
		}
		result, err := decodeWorkspaceResumeOperation(existing)
		if err != nil {
			return nil, false, err
		}
		claim, _ := decodeWorkspaceResumeOperation(operation)
		if result.RequestHash != claim.RequestHash {
			return nil, false, errIdempotencyConflict
		}
		if stringValue(existing["status"]) == "succeeded" && result.Response != nil {
			return cloneMap(result.Response), true, nil
		}
		if stringValue(existing["status"]) == "started" && result.LeaseExpiresAt != nil && result.LeaseExpiresAt.After(now) {
			return nil, false, errWorkspaceResumeInProgress
		}
		s.runtimeOps[index] = cloneMap(operation)
		workspace := cloneMap(s.workspaces[workspaceID])
		workspace["state"], workspace["status"] = "resuming", "resuming"
		s.workspaces[workspaceID] = workspace
		return nil, false, nil
	}
	workspace, ok := s.workspaces[workspaceID]
	if !ok {
		return nil, false, errWorkspaceNotSuspended
	}
	state := firstNonEmpty(stringValue(workspace["state"]), stringValue(workspace["status"]))
	if state == "resuming" {
		return nil, false, errWorkspaceResumeInProgress
	}
	if state != "suspended" && state != "stopped" {
		return nil, false, errWorkspaceNotSuspended
	}
	workspace = cloneMap(workspace)
	workspace["state"], workspace["status"] = "resuming", "resuming"
	s.workspaces[workspaceID] = workspace
	s.runtimeOps = append(s.runtimeOps, cloneMap(operation))
	return nil, false, nil
}

func (s *memoryTableStore) FailWorkspaceResume(_ context.Context, workspaceID string, operationID string, errorCode string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	workspace := cloneMap(s.workspaces[workspaceID])
	if firstNonEmpty(stringValue(workspace["state"]), stringValue(workspace["status"])) == "resuming" {
		workspace["state"], workspace["status"] = "suspended", "suspended"
		s.workspaces[workspaceID] = workspace
	}
	for index := range s.runtimeOps {
		if stringValue(s.runtimeOps[index]["id"]) != operationID {
			continue
		}
		operation := cloneMap(s.runtimeOps[index])
		result, err := decodeWorkspaceResumeOperation(operation)
		if err != nil {
			return err
		}
		result.ErrorCode = errorCode
		result.LeaseExpiresAt = nil
		operation["status"] = "retryable"
		operation["result"] = encodeWorkspaceResumeOperation(result)
		s.runtimeOps[index] = operation
		break
	}
	return nil
}

func (s *memoryTableStore) CommitWorkspaceResume(_ context.Context, workspace map[string]any, audit map[string]any, operation map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	workspace = cloneMap(workspace)
	access := cloneMap(mapField(workspace, "access"))
	delete(access, "password")
	workspace["access"] = access
	s.workspaces[stringValue(workspace["id"])] = workspace
	s.auditEvents = upsertProjectionByID(s.auditEvents, cloneMap(audit))
	s.runtimeOps = upsertProjectionByID(s.runtimeOps, cloneMap(operation))
	return nil
}

func (s *memoryTableStore) DeleteWorkspace(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.workspaces, id)
	return nil
}

func (s *memoryTableStore) ListWallets(_ context.Context, accountID string) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filteredRecords(s.wallets, accountID)
}

func (s *memoryTableStore) SaveWallet(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wallets[firstNonEmpty(stringValue(row["id"]), stringValue(row["accountId"]))] = cloneMap(row)
	return nil
}

func (s *memoryTableStore) ListLedger(_ context.Context, accountID string) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filteredEvents(s.ledger, accountID), nil
}

func (s *memoryTableStore) SaveLedgerEntry(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ledger = upsertProjectionByID(s.ledger, cloneMap(row))
	return nil
}

func (s *memoryTableStore) ListWalletTransactions(_ context.Context, accountID string) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filteredEvents(s.walletTx, accountID), nil
}

func (s *memoryTableStore) SaveWalletTransaction(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.walletTx = upsertProjectionByID(s.walletTx, cloneMap(row))
	return nil
}

func (s *memoryTableStore) ListManualTopups(_ context.Context, accountID string) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filteredEvents(s.topups, accountID), nil
}

func (s *memoryTableStore) SaveManualTopup(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.topups = upsertProjectionByID(s.topups, cloneMap(row))
	return nil
}

func (s *memoryTableStore) ListAuditEvents(_ context.Context, accountID string) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filteredEvents(s.auditEvents, accountID), nil
}

func (s *memoryTableStore) SaveAuditEvent(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auditEvents = upsertProjectionByID(s.auditEvents, cloneMap(row))
	return nil
}

func (s *memoryTableStore) ListSupportMappings(_ context.Context, accountID string) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filteredRecords(s.support, accountID)
}

func (s *memoryTableStore) SaveSupportMapping(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.support[stringValue(row["id"])] = cloneMap(row)
	return nil
}

func (s *memoryTableStore) ListRuntimeOperations(_ context.Context) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filteredEvents(s.runtimeOps, ""), nil
}

func (s *memoryTableStore) SaveRuntimeOperation(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runtimeOps = upsertProjectionByID(s.runtimeOps, cloneMap(row))
	return nil
}

func (s *memoryTableStore) BillingReconciliation(_ context.Context) (map[string]any, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reconciliation == nil {
		return nil, false, nil
	}
	return cloneMap(s.reconciliation), true, nil
}

func (s *memoryTableStore) SaveBillingReconciliation(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reconciliation = cloneMap(row)
	return nil
}
