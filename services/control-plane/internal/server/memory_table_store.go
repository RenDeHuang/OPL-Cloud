package server

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

type memoryTableStore struct {
	mu                sync.Mutex
	accounts          controlPlaneRecordSet
	users             controlPlaneRecordSet
	sessions          controlPlaneRecordSet
	organizations     controlPlaneRecordSet
	memberships       controlPlaneRecordSet
	computes          controlPlaneRecordSet
	storages          controlPlaneRecordSet
	attachments       controlPlaneRecordSet
	workspaces        controlPlaneRecordSet
	auditEvents       []map[string]any
	announcements     controlPlaneRecordSet
	announcementReads controlPlaneRecordSet
	support           controlPlaneRecordSet
	runtimeOps        []map[string]any
	reconciliation    map[string]any
}

func newMemoryTableStore() *memoryTableStore {
	return &memoryTableStore{
		accounts:          controlPlaneRecordSet{"acct-admin": {"id": "acct-admin", "ownerUserId": "usr-admin", "sub2apiUserId": int64(1), "status": "active"}},
		users:             controlPlaneRecordSet{"usr-admin": {"id": "usr-admin", "email": "admin@medopl.cn", "accountId": "acct-admin", "role": "admin", "status": "active"}},
		sessions:          controlPlaneRecordSet{},
		organizations:     controlPlaneRecordSet{"org-admin": {"id": "org-admin", "name": "OPL Cloud", "billingAccountId": "acct-admin", "status": "active"}},
		memberships:       controlPlaneRecordSet{"mem-admin": {"id": "mem-admin", "accountId": "acct-admin", "organizationId": "org-admin", "userId": "usr-admin", "role": "owner", "status": "active"}},
		computes:          controlPlaneRecordSet{},
		storages:          controlPlaneRecordSet{},
		attachments:       controlPlaneRecordSet{},
		workspaces:        controlPlaneRecordSet{},
		announcements:     controlPlaneRecordSet{},
		announcementReads: controlPlaneRecordSet{},
		support:           controlPlaneRecordSet{},
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

func (s *memoryTableStore) GetAccount(_ context.Context, id string) (map[string]any, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.accounts[id]
	return cloneMap(row), row != nil, nil
}

func (s *memoryTableStore) PageAccounts(_ context.Context, query tablePageQuery) (tablePage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, _ := filteredRecords(s.accounts, "")
	return memoryPage(rows, query), nil
}

func (s *memoryTableStore) SaveAccount(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	accountID, ownerUserID := stringValue(row["id"]), stringValue(row["ownerUserId"])
	if !validAccountID(accountID) || ownerUserID == "" {
		return errAccountIdentityConflict
	}
	if _, ok := positiveIntegerField(row, "sub2apiUserId"); !ok {
		return errMonthlyAccountUnmapped
	}
	accounts, _ := filteredRecords(s.accounts, "")
	if err := validateSub2APIAccountMapping(accounts, row); err != nil {
		return err
	}
	for _, existing := range s.accounts {
		if stringValue(existing["id"]) != accountID && stringValue(existing["ownerUserId"]) == ownerUserID {
			return errAccountIdentityConflict
		}
	}
	if owner := s.users[ownerUserID]; owner != nil && stringValue(owner["accountId"]) != accountID {
		return errAccountIdentityConflict
	}
	s.accounts[accountID] = cloneMap(row)
	return nil
}

func (s *memoryTableStore) CreateProvisionedAccount(_ context.Context, account, user, organization, membership map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	accounts := cloneStateTable(s.accounts)
	users := cloneStateTable(s.users)
	organizations := cloneStateTable(s.organizations)
	memberships := cloneStateTable(s.memberships)

	if err := stageProvisionedAccount(accounts, users, organizations, memberships, account, user, organization, membership); err != nil {
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

func (s *memoryTableStore) GetUser(_ context.Context, id string) (map[string]any, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.users[id]
	return cloneMap(row), row != nil, nil
}

func (s *memoryTableStore) SaveUser(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	userID, accountID := stringValue(row["id"]), stringValue(row["accountId"])
	email, err := canonicalEmail(stringValue(row["email"]))
	if err != nil {
		return err
	}
	account := s.accounts[accountID]
	operator := userID == "usr-admin" && accountID == "acct-admin" && email == "admin@medopl.cn" && stringValue(row["role"]) == "admin"
	if stringValue(row["role"]) != "owner" && !operator {
		return errInvalidRole
	}
	if userID == "" || account == nil || stringValue(account["ownerUserId"]) != userID || stringValue(row["passwordHash"]) != "" {
		return errAccountIdentityConflict
	}
	for _, existing := range s.users {
		if stringValue(existing["id"]) == userID {
			if stringValue(existing["accountId"]) != accountID || normalizeEmail(stringValue(existing["email"])) != email {
				return errUserExists
			}
			continue
		}
		if stringValue(existing["accountId"]) == accountID {
			return errAccountIdentityConflict
		}
		if normalizeEmail(stringValue(existing["email"])) == email {
			return errUserExists
		}
	}
	row = cloneMap(row)
	row["email"] = email
	s.users[userID] = row
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
	if !validSessionLookupKey(stringValue(row["id"])) {
		return errors.New("invalid_session_id")
	}
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
	organizationID, accountID := stringValue(row["id"]), stringValue(row["billingAccountId"])
	if organizationID == "" || s.accounts[accountID] == nil {
		return errAccountNotFound
	}
	for _, existing := range s.organizations {
		if stringValue(existing["id"]) != organizationID && stringValue(existing["billingAccountId"]) == accountID {
			return errMembershipAccountMismatch
		}
		if stringValue(existing["id"]) == organizationID && stringValue(existing["billingAccountId"]) != accountID {
			return errMembershipAccountMismatch
		}
	}
	s.organizations[organizationID] = cloneMap(row)
	return nil
}

func (s *memoryTableStore) ListMemberships(_ context.Context) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filteredRecords(s.memberships, "")
}

func (s *memoryTableStore) SaveMembership(_ context.Context, row map[string]any) error {
	if stringValue(row["role"]) != "owner" {
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
	if stringValue(s.accounts[accountID]["ownerUserId"]) != stringValue(user["id"]) || stringValue(row["role"]) != "owner" || stringValue(row["status"]) != "active" {
		return errMembershipAccountMismatch
	}
	membershipID := stringValue(row["id"])
	for _, existing := range s.memberships {
		if stringValue(existing["id"]) == membershipID {
			if stringValue(existing["accountId"]) != accountID || stringValue(existing["organizationId"]) != stringValue(row["organizationId"]) || stringValue(existing["userId"]) != stringValue(row["userId"]) {
				return errMembershipExists
			}
			continue
		}
		if stringValue(existing["accountId"]) == accountID || stringValue(existing["organizationId"]) == stringValue(row["organizationId"]) || stringValue(existing["userId"]) == stringValue(row["userId"]) {
			return errMembershipExists
		}
	}
	s.memberships[membershipID] = cloneMap(row)
	return nil
}

func (s *memoryTableStore) ListComputes(_ context.Context, accountID string) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filteredRecords(s.computes, accountID)
}

func (s *memoryTableStore) GetCompute(_ context.Context, id string) (map[string]any, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.computes[id]
	return cloneMap(row), row != nil, nil
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

func (s *memoryTableStore) GetStorage(_ context.Context, id string) (map[string]any, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.storages[id]
	return cloneMap(row), row != nil, nil
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

func (s *memoryTableStore) GetAttachment(_ context.Context, id string) (map[string]any, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.attachments[id]
	return cloneMap(row), row != nil, nil
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

func (s *memoryTableStore) GetWorkspace(_ context.Context, id string) (map[string]any, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.workspaces[id]
	return cloneMap(row), row != nil, nil
}

func (s *memoryTableStore) PageWorkspaces(_ context.Context, accountID string, query tablePageQuery) (tablePage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, _ := filteredRecords(s.workspaces, accountID)
	return memoryPage(rows, query), nil
}

func (s *memoryTableStore) CountWorkspacesByAccount(_ context.Context, accountIDs []string) (map[string]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := make(map[string]int, len(accountIDs))
	allowed := make(map[string]bool, len(accountIDs))
	for _, accountID := range accountIDs {
		counts[accountID], allowed[accountID] = 0, true
	}
	for _, row := range s.workspaces {
		accountID := firstNonEmpty(stringValue(row["accountId"]), stringValue(row["ownerAccountId"]))
		if allowed[accountID] {
			counts[accountID]++
		}
	}
	return counts, nil
}

func memoryPage(rows []map[string]any, query tablePageQuery) tablePage {
	sort.Slice(rows, func(i, j int) bool { return stringValue(rows[i]["id"]) < stringValue(rows[j]["id"]) })
	total := len(rows)
	if query.Offset >= total {
		return tablePage{Items: []map[string]any{}, Total: total}
	}
	end := query.Offset + query.Limit
	if end > total {
		end = total
	}
	return tablePage{Items: rows[query.Offset:end], Total: total}
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

func (s *memoryTableStore) CompareAndSwapWorkspaceAPIKey(_ context.Context, workspaceID string, expectedID, newID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	workspace := s.workspaces[workspaceID]
	currentID, ok := requiredPositiveInteger(workspace, "workspaceApiKeyId")
	if workspace == nil || !ok || expectedID <= 0 || newID <= 0 || currentID != expectedID && currentID != newID {
		return errWorkspaceAPIKeyCASConflict
	}
	if currentID == newID {
		return nil
	}
	workspace = cloneMap(workspace)
	workspace["workspaceApiKeyId"] = newID
	workspace["updatedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
	s.workspaces[workspaceID] = workspace
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
	desired := cloneMap(current)
	desired["autoRenew"], desired["authorizedBy"], desired["authorizedAt"] = update.WorkspacePatch.AutoRenew, update.WorkspacePatch.AuthorizedBy, update.WorkspacePatch.AuthorizedAt
	if err := validateWorkspaceBillingState(desired); err != nil {
		return err
	}
	if err := validateWorkspaceRenewalIntentAudit(update, current); err != nil {
		return err
	}
	auditExists := false
	for _, row := range s.auditEvents {
		if stringValue(row["id"]) != stringValue(update.AuditEvent["id"]) {
			continue
		}
		if !workspaceRenewalIntentAuditIdentityMatches(row, update.AuditEvent) {
			return errIdempotencyConflict
		}
		auditExists = true
		break
	}
	s.workspaces[update.WorkspaceID] = desired
	s.runtimeOps = append(s.runtimeOps, cloneMap(update.CommandOperation))
	if !auditExists {
		s.auditEvents = append(s.auditEvents, cloneMap(update.AuditEvent))
	}
	return nil
}

func (s *memoryTableStore) ClaimWorkspaceLaunch(_ context.Context, claim workspaceLaunchClaimCAS) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	desired, err := decodeWorkspaceLaunchOperation(claim.DesiredOperation)
	if err != nil || desired.AccountID != claim.AccountID || s.accounts[claim.AccountID] == nil {
		return errWorkspaceLaunchCASConflict
	}
	index := -1
	for i, row := range s.runtimeOps {
		if stringValue(row["id"]) == desired.ID {
			index = i
			break
		}
	}
	if claim.ExpectedOperationResult == "" {
		if index >= 0 {
			return errWorkspaceLaunchCASConflict
		}
		for _, row := range s.runtimeOps {
			if stringValue(row["accountId"]) == claim.AccountID && isWorkspaceLaunchAction(stringValue(row["action"])) && !terminalWorkspaceLaunchStatus(stringValue(row["status"])) {
				return errWorkspaceLaunchInProgress
			}
		}
		s.runtimeOps = append(s.runtimeOps, cloneMap(claim.DesiredOperation))
		return nil
	}
	if index < 0 || stringValue(s.runtimeOps[index]["result"]) != claim.ExpectedOperationResult {
		return errWorkspaceLaunchCASConflict
	}
	if !workspaceLaunchClaimIdentityMatches(s.runtimeOps[index], claim.DesiredOperation) {
		return errIdempotencyConflict
	}
	s.runtimeOps[index] = cloneMap(claim.DesiredOperation)
	return nil
}

func (s *memoryTableStore) PersistWorkspaceLaunch(_ context.Context, update workspaceLaunchPersistCAS) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, row := range s.runtimeOps {
		if stringValue(row["id"]) != update.OperationID {
			continue
		}
		if stringValue(row["result"]) != update.ExpectedOperationResult || !workspaceLaunchClaimIdentityMatches(row, update.DesiredOperation) {
			return errWorkspaceLaunchCASConflict
		}
		s.runtimeOps[i] = cloneMap(update.DesiredOperation)
		return nil
	}
	return errWorkspaceLaunchCASConflict
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
	if index < 0 || stringValue(s.runtimeOps[index]["result"]) != update.ExpectedOperationResult ||
		stringValue(s.runtimeOps[index]["workspaceId"]) != stringValue(update.DesiredOperation["workspaceId"]) ||
		stringValue(s.runtimeOps[index]["action"]) != stringValue(update.DesiredOperation["action"]) {
		return errWorkspaceRenewalCASConflict
	}
	if update.WorkspacePatch != nil {
		current := s.workspaces[update.WorkspaceID]
		if update.WorkspaceID == "" || update.ExpectedWorkspacePaidThrough == "" || update.WorkspaceID != stringValue(s.runtimeOps[index]["workspaceId"]) ||
			current == nil || stringValue(current["paidThrough"]) != update.ExpectedWorkspacePaidThrough ||
			!workspaceRenewalExpectedFieldsMatch(current, update.ExpectedWorkspaceFields) {
			return errWorkspaceRenewalCASConflict
		}
		workspace, err := mergeWorkspaceRenewalPatch(current, update.WorkspacePatch)
		if err != nil {
			return err
		}
		s.workspaces[update.WorkspaceID] = workspace
	} else if update.WorkspaceID != "" || update.ExpectedWorkspacePaidThrough != "" || len(update.ExpectedWorkspaceFields) != 0 {
		return errInvalidWorkspaceRenewalPatch
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

func (s *memoryTableStore) ListAnnouncements(_ context.Context) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filteredRecords(s.announcements, "")
}

func (s *memoryTableStore) ApplyAnnouncementMutation(_ context.Context, mutation announcementMutation) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if replay, err := announcementReplay(findRecord(s.auditEvents, stringValue(mutation.AuditEvent["id"])), mutation); replay != nil || err != nil {
		return cloneMap(replay), err
	}
	var current map[string]any
	if existing := s.announcements[mutation.AnnouncementID]; existing != nil {
		current = cloneMap(existing)
	}
	desired, err := prepareAnnouncementMutation(current, mutation, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	audit := announcementAudit(mutation, current, desired)
	s.announcements[mutation.AnnouncementID] = cloneMap(desired)
	s.auditEvents = upsertProjectionByID(s.auditEvents, audit)
	return cloneMap(desired), nil
}

func (s *memoryTableStore) ListAnnouncementReads(_ context.Context, userID string) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := make([]map[string]any, 0, len(s.announcementReads))
	for _, row := range s.announcementReads {
		if userID == "" || stringValue(row["userId"]) == userID {
			rows = append(rows, cloneMap(row))
		}
	}
	return rows, nil
}

func (s *memoryTableStore) MarkAnnouncementRead(_ context.Context, announcementID, userID, readAt string) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if announcementID == "" || userID == "" {
		return nil, errAnnouncementNotActive
	}
	id := announcementReadID(announcementID, userID)
	if existing := s.announcementReads[id]; existing != nil {
		return cloneMap(existing), nil
	}
	readTime, ok := optionalAnnouncementTime(readAt)
	announcement := s.announcements[announcementID]
	if !ok || !announcementIsActive(announcement, readTime) {
		return nil, errAnnouncementNotActive
	}
	row := map[string]any{
		"id": id, "announcementId": announcementID, "userId": userID, "readAt": readAt,
		"createdAt": readAt, "updatedAt": readAt,
	}
	s.announcementReads[id] = row
	return cloneMap(row), nil
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
