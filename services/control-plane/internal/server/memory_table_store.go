package server

import (
	"context"
	"errors"
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
		support:       controlPlaneRecordSet{},
		projectTasks:  controlPlaneRecordSet{},
		executionReqs: controlPlaneRecordSet{},
	}
}

func (s *memoryTableStore) ListAccounts(_ context.Context, accountID string) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if accountID != "" {
		if account := s.accounts[accountID]; account != nil {
			return []map[string]any{cloneMap(account)}, nil
		}
		return []map[string]any{}, nil
	}
	return filteredRecords(s.accounts, "")
}

func (s *memoryTableStore) SaveAccount(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	accounts, _ := filteredRecords(s.accounts, "")
	if err := validateSub2APIAccountMapping(accounts, row); err != nil {
		return err
	}
	s.accounts[stringValue(row["id"])] = cloneMap(row)
	return nil
}

func (s *memoryTableStore) CreateInvitedAccount(_ context.Context, account, user, organization, membership map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	accounts := cloneStateTable(s.accounts)
	users := cloneStateTable(s.users)
	organizations := cloneStateTable(s.organizations)
	memberships := cloneStateTable(s.memberships)

	if err := stageInvitedAccount(accounts, users, organizations, memberships, account, user, organization, membership); err != nil {
		return err
	}

	s.accounts, s.users, s.organizations, s.memberships = accounts, users, organizations, memberships
	return nil
}

func (s *memoryTableStore) ApplyUserLifecycle(_ context.Context, user map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	userID := stringValue(user["id"])
	if s.users[userID] == nil {
		return errUserNotFound
	}
	users := cloneStateTable(s.users)
	sessions := cloneStateTable(s.sessions)
	computes := cloneStateTable(s.computes)
	storages := cloneStateTable(s.storages)
	workspaces := cloneStateTable(s.workspaces)
	users[userID] = cloneMap(user)
	for id, session := range sessions {
		if stringValue(session["userId"]) == userID {
			delete(sessions, id)
		}
	}
	if stringValue(user["role"]) == "owner" {
		accountID := stringValue(user["accountId"])
		for _, rows := range []controlPlaneRecordSet{computes, storages} {
			for id, row := range rows {
				if firstNonEmpty(stringValue(row["accountId"]), stringValue(row["ownerAccountId"])) == accountID && row["autoRenew"] == true {
					row = cloneMap(row)
					row["autoRenew"] = false
					rows[id] = row
				}
			}
		}
		for id, row := range workspaces {
			if stringValue(row["ownerUserId"]) != userID || row["autoRenew"] != true || validateWorkspaceBillingState(row) != nil {
				continue
			}
			row = cloneMap(row)
			row["autoRenew"] = false
			workspaces[id] = row
		}
	}
	s.users, s.sessions, s.computes, s.storages, s.workspaces = users, sessions, computes, storages, workspaces
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
	id := stringValue(row["id"])
	if current := s.computes[id]; current != nil {
		row = preserveResourceAutoRenew(current, row)
	}
	s.computes[id] = cloneMap(row)
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
	id := stringValue(row["id"])
	if current := s.storages[id]; current != nil {
		row = preserveResourceAutoRenew(current, row)
	}
	s.storages[id] = cloneMap(row)
	return nil
}

func (s *memoryTableStore) SetResourceAutoRenew(_ context.Context, resourceType, id, accountID string, autoRenew bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var records controlPlaneRecordSet
	switch resourceType {
	case "compute":
		records = s.computes
	case "storage":
		records = s.storages
	default:
		return errors.New("invalid_billing_resource_type")
	}
	current := records[id]
	if current == nil {
		return errors.New("resource_not_found")
	}
	if firstNonEmpty(stringValue(current["accountId"]), stringValue(current["ownerAccountId"])) != accountID {
		return errIdempotencyConflict
	}
	current = cloneMap(current)
	current["autoRenew"] = autoRenew
	records[id] = current
	return nil
}

func (s *memoryTableStore) ClaimResourceBillingOperation(_ context.Context, resourceType string, row map[string]any) (map[string]any, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var records controlPlaneRecordSet
	switch resourceType {
	case "compute":
		records = s.computes
	case "storage":
		records = s.storages
	default:
		return nil, false, errors.New("invalid_billing_resource_type")
	}
	id, operationID := stringValue(row["id"]), stringValue(row["billingOperationId"])
	if id == "" || operationID == "" {
		return nil, false, errors.New("billing_operation_identity_required")
	}
	if !monthlyPriceSnapshotAvailable(row) {
		return nil, false, errMonthlyPriceSnapshotUnavailable
	}
	existing := records[id]
	if existing == nil {
		records[id] = cloneMap(row)
		return cloneMap(row), true, nil
	}
	if stringValue(existing["billingOperationId"]) == operationID {
		if !billingOperationIdentityMatches(existing, row) {
			return nil, false, errIdempotencyConflict
		}
		return cloneMap(existing), false, nil
	}
	if stringValue(row["billingStatus"]) == "renewal_pending" && existing["autoRenew"] != true {
		return cloneMap(existing), false, nil
	}
	if billingOperationInProgress(stringValue(existing["billingStatus"])) {
		return cloneMap(existing), false, errBillingOperationInProgress
	}
	claimed := preserveResourceAutoRenew(existing, mergeMaps(existing, row))
	if _, confirmationExists := row["sub2apiChargeConfirmation"]; !confirmationExists {
		delete(claimed, "sub2apiChargeConfirmation")
	}
	if lastReceiptID, reset := row["lastReceiptId"].(string); reset && lastReceiptID == "" {
		claimed["lastReceiptId"] = ""
	}
	records[id] = claimed
	return cloneMap(claimed), true, nil
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
	if err := validateWorkspaceBillingState(row); err != nil {
		return err
	}
	row = cloneMap(row)
	if _, ok := row["customerProduct"]; !ok {
		row["customerProduct"] = true
	}
	var err error
	row, err = mergeWorkspaceForSave(s.workspaces[stringValue(row["id"])], row)
	if err != nil {
		return err
	}
	if err := validateWorkspaceBillingState(row); err != nil {
		return err
	}
	access, _ := row["access"].(map[string]any)
	access = cloneMap(access)
	delete(access, "password")
	row["access"] = access
	s.workspaces[stringValue(row["id"])] = row
	return nil
}

func (s *memoryTableStore) ApplyWorkspaceRenewalIntent(_ context.Context, update workspaceRenewalIntentCAS) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.workspaces[update.WorkspaceID]
	currentAutoRenew, validAutoRenew := current["autoRenew"].(bool)
	if current == nil || stringValue(current["accountId"]) != update.AccountID || stringValue(current["ownerUserId"]) != update.OwnerUserID ||
		stringValue(current["paidThrough"]) != update.ExpectedPaidThrough || !validAutoRenew || currentAutoRenew != update.ExpectedAutoRenew ||
		runtimeOperationsVersion(s.runtimeOps, update.WorkspaceID) != update.ExpectedOperationsVersion {
		return errWorkspaceRenewalCASConflict
	}
	for _, row := range s.runtimeOps {
		if stringValue(row["id"]) == stringValue(update.CommandOperation["id"]) {
			return errWorkspaceRenewalCASConflict
		}
	}
	if err := validateWorkspaceBillingState(update.DesiredWorkspace); err != nil {
		return err
	}
	s.workspaces[update.WorkspaceID] = cloneMap(update.DesiredWorkspace)
	s.runtimeOps = append(s.runtimeOps, cloneMap(update.CommandOperation))
	return nil
}

func (s *memoryTableStore) ClaimWorkspaceRenewal(_ context.Context, claim workspaceRenewalClaimCAS) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	workspace := s.workspaces[claim.WorkspaceID]
	autoRenew, validAutoRenew := workspace["autoRenew"].(bool)
	if workspace == nil || stringValue(workspace["accountId"]) != claim.AccountID || stringValue(workspace["paidThrough"]) != claim.ExpectedPaidThrough ||
		!validAutoRenew || autoRenew != claim.ExpectedAutoRenew || runtimeOperationsVersion(s.runtimeOps, claim.WorkspaceID) != claim.ExpectedOperationsVersion {
		return errWorkspaceRenewalCASConflict
	}
	id := stringValue(claim.DesiredOperation["id"])
	index := -1
	for i, row := range s.runtimeOps {
		if stringValue(row["id"]) == id {
			index = i
			break
		}
	}
	if claim.ExpectedOperationResult == "" {
		if index >= 0 {
			return errWorkspaceRenewalCASConflict
		}
		s.runtimeOps = append(s.runtimeOps, cloneMap(claim.DesiredOperation))
		return nil
	}
	if index < 0 || stringValue(s.runtimeOps[index]["result"]) != claim.ExpectedOperationResult {
		return errWorkspaceRenewalCASConflict
	}
	if !workspaceRenewalClaimIdentityMatches(s.runtimeOps[index], claim.DesiredOperation) {
		return errIdempotencyConflict
	}
	s.runtimeOps[index] = cloneMap(claim.DesiredOperation)
	return nil
}

func (s *memoryTableStore) PersistWorkspaceRenewal(_ context.Context, update workspaceRenewalPersistCAS) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := -1
	for i, row := range s.runtimeOps {
		if stringValue(row["id"]) == update.OperationID {
			index = i
			break
		}
	}
	if index < 0 || stringValue(s.runtimeOps[index]["result"]) != update.ExpectedOperationResult {
		return errWorkspaceRenewalCASConflict
	}
	if update.DesiredWorkspace != nil {
		if err := validateWorkspaceBillingState(update.DesiredWorkspace); err != nil {
			return err
		}
		workspace := cloneMap(update.DesiredWorkspace)
		access := cloneMap(mapField(workspace, "access"))
		delete(access, "password")
		workspace["access"] = access
		s.workspaces[stringValue(workspace["id"])] = workspace
	}
	s.runtimeOps[index] = cloneMap(update.DesiredOperation)
	return nil
}

func (s *memoryTableStore) ActivateWorkspace(_ context.Context, row map[string]any) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := stringValue(row["id"])
	prepared, err := prepareWorkspaceActivation(
		row,
		s.users[stringValue(row["ownerUserId"])],
		s.computes[stringValue(row["currentComputeAllocationId"])],
		s.storages[stringValue(row["storageId"])],
		s.attachments[stringValue(row["currentAttachmentId"])],
		s.workspaces[id],
	)
	if err != nil {
		return nil, err
	}
	if _, ok := prepared["customerProduct"]; !ok {
		prepared["customerProduct"] = true
	}
	access := cloneMap(mapField(prepared, "access"))
	delete(access, "password")
	prepared["access"] = access
	s.workspaces[id] = cloneMap(prepared)
	return cloneMap(prepared), nil
}

func (s *memoryTableStore) ClaimWorkspaceCreate(_ context.Context, workspace map[string]any, operation map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	accountID := firstNonEmpty(stringValue(workspace["accountId"]), stringValue(workspace["ownerAccountId"]))
	workspaceID, operationID := stringValue(workspace["id"]), stringValue(operation["id"])
	if accountID == "" || workspaceID == "" || operationID == "" {
		return errors.New("invalid_workspace_create_claim")
	}
	if stringValue(operation["accountId"]) != accountID || stringValue(operation["workspaceId"]) != workspaceID {
		return errPrimaryWorkspaceExists
	}
	var claim workspaceCreateOperationResult
	if stringValue(operation["action"]) == "workspace.create" {
		var claimErr error
		claim, claimErr = decodeWorkspaceCreateOperation(operation)
		if claimErr != nil || claim.Workspace.ID != workspaceID || claim.Workspace.AccountID != accountID {
			return errPrimaryWorkspaceExists
		}
	}
	for index, existing := range s.runtimeOps {
		if stringValue(existing["id"]) != operationID {
			continue
		}
		if stringValue(operation["action"]) != "workspace.create" || stringValue(existing["action"]) != "workspace.create" {
			return errPrimaryWorkspaceExists
		}
		current, currentErr := decodeWorkspaceCreateOperation(existing)
		persisted := s.workspaces[workspaceID]
		if currentErr != nil || stringValue(existing["accountId"]) != accountID || stringValue(existing["workspaceId"]) != workspaceID ||
			!workspaceCreateClaimCompatible(current, claim, persisted) {
			return errPrimaryWorkspaceExists
		}
		status := stringValue(existing["status"])
		if status != "retryable" && (status != "started" || current.LeaseExpiresAt != nil && current.LeaseExpiresAt.After(time.Now().UTC())) {
			return errPrimaryWorkspaceExists
		}
		if persisted == nil || firstNonEmpty(stringValue(persisted["accountId"]), stringValue(persisted["ownerAccountId"])) != accountID {
			return errPrimaryWorkspaceExists
		}
		s.runtimeOps[index] = cloneMap(operation)
		return nil
	}
	for _, existing := range s.workspaces {
		if firstNonEmpty(stringValue(existing["accountId"]), stringValue(existing["ownerAccountId"])) == accountID {
			return errPrimaryWorkspaceExists
		}
	}
	workspace = cloneMap(workspace)
	if _, ok := workspace["customerProduct"]; !ok {
		workspace["customerProduct"] = true
	}
	s.workspaces[workspaceID] = workspace
	s.runtimeOps = append(s.runtimeOps, cloneMap(operation))
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
