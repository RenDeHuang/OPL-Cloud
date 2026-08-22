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
	userIDByEmail     map[string]string
	sessions          controlPlaneRecordSet
	sessionIDsByUser  map[string]map[string]struct{}
	computes          controlPlaneRecordSet
	storages          controlPlaneRecordSet
	attachments       controlPlaneRecordSet
	workspaces        controlPlaneRecordSet
	auditEvents       []map[string]any
	announcements     controlPlaneRecordSet
	announcementReads controlPlaneRecordSet
	support           controlPlaneRecordSet
	runtimeOps        []map[string]any
	productionE2E     controlPlaneRecordSet
	reconciliation    map[string]any
	reconciliations   controlPlaneRecordSet
	reconciliationErr error
}

func newMemoryTableStore() *memoryTableStore {
	const developmentOperatorEmail = "admin@opl.local"
	return &memoryTableStore{
		accounts:          controlPlaneRecordSet{"acct-admin": {"id": "acct-admin", "ownerUserId": "usr-admin", "sub2apiUserId": int64(1), "status": "active", "workspacePurchaseEnabled": true}},
		users:             controlPlaneRecordSet{"usr-admin": {"id": "usr-admin", "email": developmentOperatorEmail, "accountId": "acct-admin", "role": "admin", "status": "active"}},
		userIDByEmail:     map[string]string{developmentOperatorEmail: "usr-admin"},
		sessions:          controlPlaneRecordSet{},
		sessionIDsByUser:  map[string]map[string]struct{}{},
		computes:          controlPlaneRecordSet{},
		storages:          controlPlaneRecordSet{},
		attachments:       controlPlaneRecordSet{},
		workspaces:        controlPlaneRecordSet{},
		announcements:     controlPlaneRecordSet{},
		announcementReads: controlPlaneRecordSet{},
		support:           controlPlaneRecordSet{},
		productionE2E:     controlPlaneRecordSet{},
		reconciliations:   controlPlaneRecordSet{},
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

func (s *memoryTableStore) CountAccountStatuses(_ context.Context) (map[string]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := make(map[string]int)
	for _, row := range s.accounts {
		counts[stringValue(row["status"])]++
	}
	return counts, nil
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

func (s *memoryTableStore) ApplyWorkspacePurchaseEligibility(_ context.Context, mutation workspacePurchaseEligibilityMutation) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.auditEvents {
		if stringValue(existing["id"]) != stringValue(mutation.AuditEvent["id"]) {
			continue
		}
		account := s.accounts[mutation.AccountID]
		if account == nil || !workspacePurchaseEligibilityAuditMatches(existing, mutation.AuditEvent) || workspacePurchaseEnabled(account) != mutation.Enabled {
			return nil, errIdempotencyConflict
		}
		return cloneMap(account), nil
	}
	account := s.accounts[mutation.AccountID]
	if account == nil {
		return nil, errors.New("account_not_found")
	}
	updated := cloneMap(account)
	updated["workspacePurchaseEnabled"] = mutation.Enabled
	audit := cloneMap(mutation.AuditEvent)
	audit["before"] = map[string]any{"workspacePurchaseEnabled": workspacePurchaseEnabled(account)}
	s.accounts[mutation.AccountID] = updated
	s.auditEvents = append(s.auditEvents, audit)
	return cloneMap(updated), nil
}

func (s *memoryTableStore) CreateProvisionedAccount(_ context.Context, account, user map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	accounts := cloneStateTable(s.accounts)
	users := cloneStateTable(s.users)
	if err := stageProvisionedAccount(accounts, users, account, user); err != nil {
		return err
	}

	s.accounts, s.users = accounts, users
	s.userIDByEmail[normalizeEmail(stringValue(user["email"]))] = stringValue(user["id"])
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
	delete(s.sessionIDsByUser, userID)
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

func (s *memoryTableStore) GetUserByEmail(_ context.Context, email string, includeDeleted bool) (map[string]any, bool, error) {
	email, err := canonicalEmail(email)
	if err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.users[s.userIDByEmail[email]]
	if row == nil || !includeDeleted && stringValue(row["status"]) == "deleted" {
		return nil, false, nil
	}
	return cloneMap(row), true, nil
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
	operator := isReservedOperatorIdentity(row)
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
	if previous := s.users[userID]; previous != nil {
		delete(s.userIDByEmail, normalizeEmail(stringValue(previous["email"])))
	}
	s.users[userID] = row
	s.userIDByEmail[email] = userID
	return nil
}

func (s *memoryTableStore) DeleteUser(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if row := s.users[id]; row != nil {
		delete(s.userIDByEmail, normalizeEmail(stringValue(row["email"])))
	}
	delete(s.users, id)
	return nil
}

func (s *memoryTableStore) ListSessions(_ context.Context) (controlPlaneRecordSet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneStateTable(s.sessions), nil
}

func (s *memoryTableStore) GetSession(_ context.Context, id string) (map[string]any, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.sessions[id]
	return cloneMap(row), row != nil, nil
}

func (s *memoryTableStore) ListSessionsByUser(_ context.Context, userID string) (controlPlaneRecordSet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := controlPlaneRecordSet{}
	for id := range s.sessionIDsByUser[userID] {
		rows[id] = cloneMap(s.sessions[id])
	}
	return rows, nil
}

func (s *memoryTableStore) SaveSession(_ context.Context, row map[string]any) error {
	if !validSessionLookupKey(stringValue(row["id"])) {
		return errors.New("invalid_session_id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, userID := stringValue(row["id"]), stringValue(row["userId"])
	if previous := s.sessions[id]; previous != nil {
		delete(s.sessionIDsByUser[stringValue(previous["userId"])], id)
	}
	s.sessions[id] = cloneMap(row)
	if s.sessionIDsByUser[userID] == nil {
		s.sessionIDsByUser[userID] = map[string]struct{}{}
	}
	s.sessionIDsByUser[userID][id] = struct{}{}
	return nil
}

func (s *memoryTableStore) DeleteSession(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if row := s.sessions[id]; row != nil {
		delete(s.sessionIDsByUser[stringValue(row["userId"])], id)
	}
	delete(s.sessions, id)
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

func (s *memoryTableStore) CountWorkspaces(_ context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.workspaces), nil
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

func (s *memoryTableStore) ClaimWorkspaceKeyRotation(_ context.Context, row map[string]any) error {
	operation, ok := validWorkspaceKeyRotationClaim(row)
	if !ok {
		return errWorkspaceKeyRotationState
	}
	workspaceID, accountID := stringValue(row["workspaceId"]), stringValue(row["accountId"])
	s.mu.Lock()
	defer s.mu.Unlock()
	workspace := s.workspaces[workspaceID]
	currentKeyID, keyOK := positiveIntegerField(workspace, "workspaceApiKeyId")
	if workspace == nil || firstNonEmpty(stringValue(workspace["accountId"]), stringValue(workspace["ownerAccountId"])) != accountID || !keyOK || currentKeyID != operation.OldKeyID {
		return errWorkspaceKeyRotationInProgress
	}
	for _, existing := range s.runtimeOps {
		if stringValue(existing["workspaceId"]) != workspaceID {
			continue
		}
		if workspaceKeyRotationBlocksDelete(existing) || workspaceDeleteBlocksRotation(existing) {
			return errWorkspaceKeyRotationInProgress
		}
	}
	s.runtimeOps = append(s.runtimeOps, cloneMap(row))
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

func (s *memoryTableStore) ClaimWorkspaceLaunchReconcile(_ context.Context, claim workspaceLaunchReconcileClaim) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reconciliationErr != nil {
		return s.reconciliationErr
	}
	if s.reconciliation != nil {
		blocked, err := billingReconciliationBlockState(s.reconciliation)
		if err != nil {
			return err
		}
		if blocked {
			return errBillingReconciliationBlocked
		}
	}
	desired, err := decodeWorkspaceLaunchReconcileOperation(claim.DesiredOperation)
	if err != nil || desired.stringFact("accountId") != claim.AccountID || desired.boolFact("acceptanceBCapacitySlot") != claim.AcceptanceBCapacitySlot || s.accounts[claim.AccountID] == nil {
		return errWorkspaceLaunchCASConflict
	}
	for _, row := range s.runtimeOps {
		if stringValue(row["id"]) == desired.ID {
			return errWorkspaceLaunchCASConflict
		}
		if stringValue(row["accountId"]) == claim.AccountID && isWorkspaceLaunchAction(stringValue(row["action"])) && !terminalWorkspaceLaunchStatus(stringValue(row["status"])) {
			return errWorkspaceLaunchInProgress
		}
	}
	inFlight, acceptanceClaims := 0, 0
	for _, row := range s.runtimeOps {
		if !isWorkspaceLaunchAction(stringValue(row["action"])) {
			continue
		}
		if workspaceLaunchReconcileAcceptanceSlot(row) || workspaceLaunchHasAcceptanceBCapacitySlot(row) {
			acceptanceClaims++
		}
		if !terminalWorkspaceLaunchStatus(stringValue(row["status"])) {
			inFlight++
		}
	}
	if claim.AcceptanceBCapacitySlot {
		if acceptanceClaims >= 1 {
			return errWorkspaceLaunchCapacityReached
		}
	} else if inFlight >= controlledBasicPilotGlobalInFlightLimit() {
		return errWorkspaceLaunchCapacityReached
	}
	s.runtimeOps = append(s.runtimeOps, cloneMap(claim.DesiredOperation))
	return nil
}

func (s *memoryTableStore) PersistWorkspaceLaunchReconcile(_ context.Context, update workspaceLaunchReconcileCAS) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	desired, err := decodeWorkspaceLaunchReconcileOperation(update.DesiredOperation)
	if err != nil {
		return errWorkspaceLaunchCASConflict
	}
	for i, row := range s.runtimeOps {
		if stringValue(row["id"]) != update.OperationID {
			continue
		}
		if stringValue(row["result"]) != update.ExpectedOperationResult || !workspaceLaunchReconcileIdentityMatches(row, update.DesiredOperation) {
			return errWorkspaceLaunchCASConflict
		}
		if update.WorkspaceReceiptProjection != nil {
			projection := update.WorkspaceReceiptProjection
			if projection.AccountID != desired.stringFact("accountId") || projection.OwnerUserID != desired.stringFact("ownerUserId") ||
				projection.WorkspaceID != desired.stringFact("workspaceId") || projection.ReceiptID != desired.stringFact("receiptId") || desired.Stage != "succeeded" || desired.Status != "succeeded" {
				return errWorkspaceLaunchCASConflict
			}
			if err := applyWorkspaceLaunchReceiptProjection(s.workspaces, update.WorkspaceReceiptProjection); err != nil {
				return err
			}
		}
		s.runtimeOps[i] = cloneMap(update.DesiredOperation)
		return nil
	}
	return errWorkspaceLaunchCASConflict
}

func applyWorkspaceLaunchReceiptProjection(workspaces controlPlaneRecordSet, projection *workspaceLaunchReceiptProjection) error {
	if projection == nil || projection.AccountID == "" || projection.OwnerUserID == "" || projection.WorkspaceID == "" || projection.ReceiptID == "" {
		return errWorkspaceLaunchCASConflict
	}
	workspace := workspaces[projection.WorkspaceID]
	if workspace == nil || firstNonEmpty(stringValue(workspace["accountId"]), stringValue(workspace["ownerAccountId"])) != projection.AccountID ||
		stringValue(workspace["ownerUserId"]) != projection.OwnerUserID || stringValue(workspace["id"]) != projection.WorkspaceID {
		return errWorkspaceLaunchCASConflict
	}
	existing := stringValue(workspace["purchaseReceiptId"])
	if existing != "" && existing != projection.ReceiptID {
		return errIdempotencyConflict
	}
	if existing == projection.ReceiptID {
		return nil
	}
	updated := cloneMap(workspace)
	updated["purchaseReceiptId"] = projection.ReceiptID
	updated["updatedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
	workspaces[projection.WorkspaceID] = updated
	return nil
}

func (s *memoryTableStore) ApplyWorkspaceLaunchCanonicalFactRepair(_ context.Context, update workspaceLaunchCanonicalFactRepairCAS) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	auditID := stringValue(update.AuditEvent["id"])
	for _, audit := range s.auditEvents {
		if stringValue(audit["id"]) == auditID {
			for _, row := range s.runtimeOps {
				if stringValue(row["id"]) == update.OperationID && stringValue(row["result"]) == stringValue(update.DesiredOperation["result"]) && workspaceLaunchCanonicalFactRepairAuditMatches(audit, update.AuditEvent) {
					return nil
				}
			}
			return errIdempotencyConflict
		}
	}
	for index, row := range s.runtimeOps {
		if stringValue(row["id"]) != update.OperationID {
			continue
		}
		if err := validateWorkspaceLaunchCanonicalFactRepairCAS(update, row); err != nil {
			return err
		}
		repaired := cloneMap(row)
		repaired["result"] = stringValue(update.DesiredOperation["result"])
		s.runtimeOps[index] = repaired
		s.auditEvents = append(s.auditEvents, cloneMap(update.AuditEvent))
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
	for _, row := range s.runtimeOps {
		if stringValue(row["workspaceId"]) == claim.WorkspaceID && workspaceDeleteBlocksRenewal(row) {
			return errWorkspaceRenewalCASConflict
		}
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

func (s *memoryTableStore) ActivateWorkspaceLaunchProjection(_ context.Context, row map[string]any) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := stringValue(row["id"])
	prepared, err := prepareWorkspaceLaunchProjection(row, s.users[stringValue(row["ownerUserId"])], s.workspaces[id])
	if err != nil {
		return nil, err
	}
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

func (s *memoryTableStore) ApplyWorkspaceDelete(_ context.Context, mutation workspaceDeleteStoreMutation) error {
	desired, ok := validWorkspaceDeleteStoreMutation(mutation)
	if !ok {
		return errWorkspaceDeleteCASConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	workspace := s.workspaces[desired.WorkspaceID]
	if mutation.RequireWorkspaceAbsent {
		if workspace != nil {
			return errWorkspaceDeleteCASConflict
		}
	} else if !workspaceDeleteWorkspaceProjectionMatches(desired, workspace, mutation.DeleteWorkspace) {
		return errWorkspaceDeleteCASConflict
	}
	if mutation.DeleteWorkspace && (!workspaceDeleteResourceProjectionMatches(desired, "compute", s.computes[desired.ComputeID]) ||
		!workspaceDeleteResourceProjectionMatches(desired, "storage", s.storages[desired.StorageID]) ||
		!workspaceDeleteResourceProjectionMatches(desired, "attachment", s.attachments[desired.AttachmentID])) {
		return errWorkspaceDeleteCASConflict
	}

	operationIndex := -1
	for index, row := range s.runtimeOps {
		if stringValue(row["id"]) == desired.OperationID {
			operationIndex = index
			break
		}
	}
	if mutation.Create {
		for _, row := range s.runtimeOps {
			if stringValue(row["workspaceId"]) == desired.WorkspaceID && (workspaceRenewalBlocksDelete(row) || workspaceKeyRotationBlocksDelete(row)) {
				return errWorkspaceDeleteCASConflict
			}
		}
		if operationIndex >= 0 {
			return errWorkspaceDeleteCASConflict
		}
		s.runtimeOps = append(s.runtimeOps, cloneMap(mutation.DesiredOperation))
	} else {
		if operationIndex < 0 {
			return errWorkspaceDeleteCASConflict
		}
		current, decodeErr := decodeWorkspaceDeleteOperation(s.runtimeOps[operationIndex])
		if decodeErr != nil || stringValue(s.runtimeOps[operationIndex]["result"]) != mutation.ExpectedResult ||
			!workspaceDeleteOperationIdentityMatches(s.runtimeOps[operationIndex], desired) || !validWorkspaceDeleteTransition(current, desired, mutation) {
			return errWorkspaceDeleteCASConflict
		}
		s.runtimeOps[operationIndex] = cloneMap(mutation.DesiredOperation)
	}
	if mutation.DeleteWorkspace {
		delete(s.attachments, desired.AttachmentID)
		delete(s.storages, desired.StorageID)
		delete(s.computes, desired.ComputeID)
		delete(s.workspaces, desired.WorkspaceID)
	}
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

func (s *memoryTableStore) CreateSupportMapping(_ context.Context, row map[string]any, limit int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := stringValue(row["id"])
	accountID := stringValue(row["accountId"])
	if _, exists := s.support[id]; !exists {
		count := 0
		for _, existing := range s.support {
			if stringValue(existing["accountId"]) == accountID {
				count++
			}
		}
		if count >= limit {
			return errors.New("support_mapping_limit_reached")
		}
	}
	s.support[id] = cloneMap(row)
	return nil
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

func (s *memoryTableStore) GetRuntimeOperation(_ context.Context, id string) (map[string]any, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.runtimeOps {
		if stringValue(row["id"]) == id {
			return cloneMap(row), true, nil
		}
	}
	return nil, false, nil
}

func (s *memoryTableStore) PageRuntimeOperations(_ context.Context, query runtimeOperationQuery) (tablePage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	statuses := make(map[string]struct{}, len(query.Statuses))
	for _, status := range query.Statuses {
		statuses[status] = struct{}{}
	}
	excludedStatuses := make(map[string]struct{}, len(query.ExcludedStatuses))
	for _, status := range query.ExcludedStatuses {
		excludedStatuses[status] = struct{}{}
	}
	rows := make([]map[string]any, 0)
	for _, row := range s.runtimeOps {
		if query.AccountID != "" && stringValue(row["accountId"]) != query.AccountID ||
			query.WorkspaceID != "" && stringValue(row["workspaceId"]) != query.WorkspaceID ||
			query.Action != "" && stringValue(row["action"]) != query.Action ||
			query.PeriodStart != "" && stringValue(row["periodStart"]) != query.PeriodStart {
			continue
		}
		if len(statuses) > 0 {
			if _, ok := statuses[stringValue(row["status"])]; !ok {
				continue
			}
		}
		if _, excluded := excludedStatuses[stringValue(row["status"])]; excluded {
			continue
		}
		rows = append(rows, cloneMap(row))
	}
	sort.Slice(rows, func(i, j int) bool {
		left, right := stringValue(rows[i]["createdAt"]), stringValue(rows[j]["createdAt"])
		return left < right || left == right && stringValue(rows[i]["id"]) < stringValue(rows[j]["id"])
	})
	return memoryPage(rows, tablePageQuery{Offset: query.Offset, Limit: query.Limit}), nil
}

func (s *memoryTableStore) SaveRuntimeOperation(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runtimeOps = upsertProjectionByID(s.runtimeOps, cloneMap(row))
	return nil
}

func (s *memoryTableStore) ReserveProductionE2EAttempt(_ context.Context, claim productionE2EAttemptClaim) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.productionE2E[claim.ID] != nil {
		return nil, errProductionE2EAttemptAlreadyExists
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	row := map[string]any{
		"id": claim.ID, "accountId": claim.AccountID, "workspaceId": claim.WorkspaceID,
		"status": "attempted", "result": claim.Binding, "reason": recoveredWorkspaceE2EAttemptReason,
		"url": claim.URL, "createdAt": now, "updatedAt": now,
	}
	s.productionE2E[claim.ID] = row
	return cloneMap(row), nil
}

func (s *memoryTableStore) GetProductionE2EAttempt(_ context.Context, id string) (map[string]any, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.productionE2E[id]
	return cloneMap(row), row != nil, nil
}

func (s *memoryTableStore) CompleteProductionE2EAttempt(_ context.Context, id, binding string) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.productionE2E[id]
	if row == nil {
		return nil, errProductionE2EAttemptNotFound
	}
	if stringValue(row["reason"]) != recoveredWorkspaceE2EAttemptReason || stringValue(row["result"]) != binding {
		return nil, errProductionE2EAttemptBindingMismatch
	}
	switch stringValue(row["status"]) {
	case "attempted":
		row = cloneMap(row)
		row["status"] = "passed"
		row["updatedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
		s.productionE2E[id] = row
	case "passed":
	default:
		return nil, errProductionE2EAttemptBindingMismatch
	}
	return cloneMap(row), nil
}

func (s *memoryTableStore) BillingReconciliation(_ context.Context) (map[string]any, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reconciliationErr != nil {
		return nil, false, s.reconciliationErr
	}
	if s.reconciliation == nil {
		return nil, false, nil
	}
	return cloneMap(s.reconciliation), true, nil
}

func (s *memoryTableStore) ApplyBillingReconciliation(_ context.Context, mutation billingReconciliationMutation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reconciliationErr != nil {
		return s.reconciliationErr
	}
	if err := validateBillingReconciliationMutation(mutation); err != nil {
		return err
	}
	resultID := stringValue(mutation.Row["id"])
	if existing := s.reconciliations[resultID]; existing != nil {
		if !billingReconciliationIdentityMatches(existing, mutation.Row) {
			return errIdempotencyConflict
		}
		for _, event := range s.auditEvents {
			if stringValue(event["id"]) == billingReconciliationAuditID(resultID) {
				if !billingReconciliationAuditIdentityMatches(event, mutation.Row) {
					return errIdempotencyConflict
				}
				return nil
			}
		}
		return errIdempotencyConflict
	}
	currentID := ""
	if s.reconciliation != nil {
		currentID = stringValue(s.reconciliation["id"])
	}
	if currentID != mutation.ExpectedCurrentGuardID {
		return errBillingReconciliationCASConflict
	}
	for _, event := range s.auditEvents {
		if stringValue(event["id"]) == stringValue(mutation.AuditEvent["id"]) {
			return errIdempotencyConflict
		}
	}
	row := cloneMap(mutation.Row)
	if stringValue(row["createdAt"]) == "" {
		row["createdAt"] = time.Now().UTC().Format(time.RFC3339Nano)
	}
	s.reconciliations[resultID] = row
	s.reconciliation = row
	s.auditEvents = append(s.auditEvents, cloneMap(mutation.AuditEvent))
	return nil
}
