package server

import (
	"context"
	"errors"
	"sort"

	controlplaneent "opl-cloud/services/control-plane/ent"
	"opl-cloud/services/control-plane/ent/account"
	"opl-cloud/services/control-plane/ent/computeallocation"
	"opl-cloud/services/control-plane/ent/membership"
	"opl-cloud/services/control-plane/ent/organization"
	"opl-cloud/services/control-plane/ent/session"
	"opl-cloud/services/control-plane/ent/storagevolume"
	"opl-cloud/services/control-plane/ent/user"
	"opl-cloud/services/control-plane/ent/workspace"
)

var (
	accountEntFields = []entRecordField{
		textField("OwnerUserID", "SetOwnerUserID", "ownerUserId"),
		intField("Sub2apiUserID", "SetSub2apiUserID", "sub2apiUserId"),
		textField("Name", "SetName", "name"),
		textField("Status", "SetStatus", "status"),
	}
	organizationEntFields = []entRecordField{
		textField("BillingAccountID", "SetBillingAccountID", "billingAccountId"),
		textField("Name", "SetName", "name"),
		textField("Status", "SetStatus", "status"),
	}
	userEntFields = []entRecordField{
		textField("AccountID", "SetAccountID", "accountId"),
		textField("Email", "SetEmail", "email"),
		textField("Role", "SetRole", "role"),
		textField("Status", "SetStatus", "status"),
		textField("DisabledAt", "SetDisabledAt", "disabledAt"),
		textField("DisabledBy", "SetDisabledBy", "disabledBy"),
		textField("DisabledReason", "SetDisabledReason", "disabledReason"),
		textField("DeletedAt", "SetDeletedAt", "deletedAt"),
		textField("DeletedBy", "SetDeletedBy", "deletedBy"),
		textField("DeleteReason", "SetDeleteReason", "deleteReason"),
	}
	sessionEntFields = []entRecordField{
		textField("UserID", "SetUserID", "userId"),
		textField("Csrf", "SetCsrf", "csrf"),
		textField("ExpiresAt", "SetExpiresAt", "expiresAt"),
	}
	membershipEntFields = []entRecordField{
		textField("AccountID", "SetAccountID", "accountId"),
		textField("OrganizationID", "SetOrganizationID", "organizationId"),
		textField("UserID", "SetUserID", "userId"),
		textField("Role", "SetRole", "role"),
		textField("Status", "SetStatus", "status"),
	}
)

func (s *postgresEntStateStore) ListUsers(ctx context.Context, includeDeleted bool) ([]map[string]any, error) {
	rows, err := loadRecordSet(ctx, s.client.User.Query().All, userEntFields)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if !includeDeleted && stringValue(row["status"]) == "deleted" {
			continue
		}
		out = append(out, cloneMap(row))
	}
	return out, nil
}

func (s *postgresEntStateStore) GetUser(ctx context.Context, id string) (map[string]any, bool, error) {
	entity, err := s.client.User.Get(ctx, id)
	if controlplaneent.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return recordFromEnt(entity, userEntFields), true, nil
}

func (s *postgresEntStateStore) GetUserByEmail(ctx context.Context, email string, includeDeleted bool) (map[string]any, bool, error) {
	email, err := canonicalEmail(email)
	if err != nil {
		return nil, false, err
	}
	query := s.client.User.Query().Where(user.Email(email))
	if !includeDeleted {
		query = query.Where(user.StatusNEQ("deleted"))
	}
	entity, err := query.Only(ctx)
	if controlplaneent.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return recordFromEnt(entity, userEntFields), true, nil
}

func (s *postgresEntStateStore) SaveUser(ctx context.Context, row map[string]any) error {
	operator := isReservedOperatorIdentity(row)
	if stringValue(row["role"]) != "owner" && !operator {
		return errInvalidRole
	}
	return s.replaceRecord(ctx, row, func(id string) error { return s.client.User.DeleteOneID(id).Exec(ctx) }, func() any { return s.client.User.Create() }, userEntFields)
}

func (s *postgresEntStateStore) DeleteUser(ctx context.Context, id string) error {
	err := s.client.User.DeleteOneID(id).Exec(ctx)
	if controlplaneent.IsNotFound(err) {
		return nil
	}
	return err
}

func (s *postgresEntStateStore) ListSessions(ctx context.Context) (controlPlaneRecordSet, error) {
	return loadRecordSet(ctx, s.client.Session.Query().All, sessionEntFields)
}

func (s *postgresEntStateStore) GetSession(ctx context.Context, id string) (map[string]any, bool, error) {
	entity, err := s.client.Session.Get(ctx, id)
	if controlplaneent.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return recordFromEnt(entity, sessionEntFields), true, nil
}

func (s *postgresEntStateStore) ListSessionsByUser(ctx context.Context, userID string) (controlPlaneRecordSet, error) {
	return loadRecordSet(ctx, s.client.Session.Query().Where(session.UserID(userID)).All, sessionEntFields)
}

func (s *postgresEntStateStore) SaveSession(ctx context.Context, row map[string]any) error {
	if !validSessionLookupKey(stringValue(row["id"])) {
		return errors.New("invalid_session_id")
	}
	return s.replaceRecord(ctx, row, func(id string) error { return s.client.Session.DeleteOneID(id).Exec(ctx) }, func() any { return s.client.Session.Create() }, sessionEntFields)
}

func (s *postgresEntStateStore) DeleteSession(ctx context.Context, id string) error {
	err := s.client.Session.DeleteOneID(id).Exec(ctx)
	if controlplaneent.IsNotFound(err) {
		return nil
	}
	return err
}

func (s *postgresEntStateStore) ListAccounts(ctx context.Context, accountID string) ([]map[string]any, error) {
	query := s.client.Account.Query()
	if accountID != "" {
		query.Where(account.ID(accountID))
	}
	rows, err := loadRecordSet(ctx, query.All, accountEntFields)
	if err != nil {
		return nil, err
	}
	return filteredRecords(rows, "")
}

func (s *postgresEntStateStore) GetAccount(ctx context.Context, id string) (map[string]any, bool, error) {
	entity, err := s.client.Account.Get(ctx, id)
	if controlplaneent.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return recordFromEnt(entity, accountEntFields), true, nil
}

func (s *postgresEntStateStore) PageAccounts(ctx context.Context, page tablePageQuery) (tablePage, error) {
	query := s.client.Account.Query()
	total, err := query.Clone().Count(ctx)
	if err != nil {
		return tablePage{}, err
	}
	rows, err := loadRecordSet(ctx, query.Order(account.ByID()).Offset(page.Offset).Limit(page.Limit).All, accountEntFields)
	if err != nil {
		return tablePage{}, err
	}
	items, err := filteredRecords(rows, "")
	sort.Slice(items, func(i, j int) bool { return stringValue(items[i]["id"]) < stringValue(items[j]["id"]) })
	return tablePage{Items: items, Total: total}, err
}

func (s *postgresEntStateStore) CountAccountStatuses(ctx context.Context) (map[string]int, error) {
	var groups []struct {
		Status string `json:"status"`
		Count  int    `json:"count"`
	}
	err := s.client.Account.Query().
		GroupBy(account.FieldStatus).
		Aggregate(controlplaneent.As(controlplaneent.Count(), "count")).
		Scan(ctx, &groups)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(groups))
	for _, group := range groups {
		counts[group.Status] = group.Count
	}
	return counts, nil
}

func (s *postgresEntStateStore) SaveAccount(ctx context.Context, row map[string]any) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	rollback := func(err error) error {
		_ = tx.Rollback()
		if int64(numberField(row, "sub2apiUserId", 0)) > 0 && controlplaneent.IsConstraintError(err) {
			return errSub2APIAccountMappingConflict
		}
		return err
	}
	accounts, err := loadRecordSet(ctx, tx.Account.Query().All, accountEntFields)
	if err != nil {
		return rollback(err)
	}
	accountRows, _ := filteredRecords(accounts, "")
	if err := validateSub2APIAccountMapping(accountRows, row); err != nil {
		return rollback(err)
	}
	id := stringValue(row["id"])
	if id == "" {
		return rollback(errors.New("missing_record_id"))
	}
	if err := tx.Account.DeleteOneID(id).Exec(ctx); err != nil && !controlplaneent.IsNotFound(err) {
		return rollback(err)
	}
	if err := saveRecord(ctx, id, row, tx.Account.Create(), accountEntFields); err != nil {
		return rollback(err)
	}
	return tx.Commit()
}

func (s *postgresEntStateStore) CreateProvisionedAccount(ctx context.Context, accountRow, userRow, organizationRow, membershipRow map[string]any) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	rollback := func(err error) error {
		_ = tx.Rollback()
		return err
	}
	replayAfterConstraint := func(conflict error) error {
		_ = tx.Rollback()
		readback, err := s.client.Tx(ctx)
		if err != nil {
			return err
		}
		finish := func(err error) error {
			_ = readback.Rollback()
			return err
		}
		client := readback.Client()
		accounts, err := loadRecordSet(ctx, client.Account.Query().All, accountEntFields)
		if err != nil {
			return finish(err)
		}
		users, err := loadRecordSet(ctx, client.User.Query().All, userEntFields)
		if err != nil {
			return finish(err)
		}
		organizations, err := loadRecordSet(ctx, client.Organization.Query().All, organizationEntFields)
		if err != nil {
			return finish(err)
		}
		memberships, err := loadRecordSet(ctx, client.Membership.Query().All, membershipEntFields)
		if err != nil {
			return finish(err)
		}
		if accounts[stringValue(accountRow["id"])] == nil || users[stringValue(userRow["id"])] == nil ||
			organizations[stringValue(organizationRow["id"])] == nil || memberships[stringValue(membershipRow["id"])] == nil ||
			stageProvisionedAccount(accounts, users, organizations, memberships, accountRow, userRow, organizationRow, membershipRow) != nil {
			return finish(conflict)
		}
		return finish(nil)
	}
	client := tx.Client()
	accountID := stringValue(accountRow["id"])
	accountExists := true
	if _, err := client.Account.UpdateOneID(accountID).Save(ctx); err != nil {
		if controlplaneent.IsNotFound(err) {
			accountExists = false
		} else {
			return rollback(err)
		}
	}
	accounts, err := loadRecordSet(ctx, client.Account.Query().All, accountEntFields)
	if err != nil {
		return rollback(err)
	}
	users, err := loadRecordSet(ctx, client.User.Query().All, userEntFields)
	if err != nil {
		return rollback(err)
	}
	organizations, err := loadRecordSet(ctx, client.Organization.Query().All, organizationEntFields)
	if err != nil {
		return rollback(err)
	}
	memberships, err := loadRecordSet(ctx, client.Membership.Query().All, membershipEntFields)
	if err != nil {
		return rollback(err)
	}

	organizationID := stringValue(organizationRow["id"])
	_, organizationExists := organizations[organizationID]
	userID := stringValue(userRow["id"])
	_, userExists := users[userID]
	membershipID := stringValue(membershipRow["id"])
	_, membershipExists := memberships[membershipID]
	if err := stageProvisionedAccount(accounts, users, organizations, memberships, accountRow, userRow, organizationRow, membershipRow); err != nil {
		return rollback(err)
	}

	if accountExists {
		if _, err := client.Account.UpdateOneID(accountID).
			SetOwnerUserID(stringValue(accounts[accountID]["ownerUserId"])).
			SetSub2apiUserID(int64(numberField(accounts[accountID], "sub2apiUserId", 0))).
			Save(ctx); err != nil {
			if controlplaneent.IsConstraintError(err) {
				return replayAfterConstraint(errSub2APIAccountMappingConflict)
			}
			return rollback(err)
		}
	} else if err := saveRecord(ctx, accountID, accounts[accountID], client.Account.Create(), accountEntFields); err != nil {
		if controlplaneent.IsConstraintError(err) {
			return replayAfterConstraint(errSub2APIAccountMappingConflict)
		}
		return rollback(err)
	}
	if !userExists {
		if err := saveRecord(ctx, userID, users[userID], client.User.Create(), userEntFields); err != nil {
			if controlplaneent.IsConstraintError(err) {
				return replayAfterConstraint(errUserExists)
			}
			return rollback(err)
		}
	}
	if !organizationExists {
		if err := saveRecord(ctx, organizationID, organizations[organizationID], client.Organization.Create(), organizationEntFields); err != nil {
			if controlplaneent.IsConstraintError(err) {
				return replayAfterConstraint(err)
			}
			return rollback(err)
		}
	}
	if !membershipExists {
		if err := saveRecord(ctx, membershipID, memberships[membershipID], client.Membership.Create(), membershipEntFields); err != nil {
			if controlplaneent.IsConstraintError(err) {
				return replayAfterConstraint(err)
			}
			return rollback(err)
		}
	}
	return tx.Commit()
}

func (s *postgresEntStateStore) ApplyUserLifecycle(ctx context.Context, user map[string]any) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	rollback := func(err error) error {
		_ = tx.Rollback()
		return err
	}
	client := tx.Client()
	userID := stringValue(user["id"])
	userUpdate := client.User.UpdateOneID(userID)
	setRecordFieldsWithEmptyText(userUpdate, user, userEntFields, true)
	if err := execCreate(ctx, userUpdate); err != nil {
		if controlplaneent.IsNotFound(err) {
			return rollback(errUserNotFound)
		}
		return rollback(err)
	}
	if _, err := client.Session.Delete().Where(session.UserID(userID)).Exec(ctx); err != nil {
		return rollback(err)
	}
	if stringValue(user["role"]) == "owner" {
		accountID := stringValue(user["accountId"])
		computes, err := client.ComputeAllocation.Query().Where(computeallocation.AccountID(accountID)).All(ctx)
		if err != nil {
			return rollback(err)
		}
		sort.Slice(computes, func(i, j int) bool { return computes[i].ID < computes[j].ID })
		for _, compute := range computes {
			current, err := client.ComputeAllocation.UpdateOneID(compute.ID).Save(ctx)
			if controlplaneent.IsNotFound(err) {
				continue
			}
			if err != nil {
				return rollback(err)
			}
			row := recordFromEnt(current, computeEntFields)
			if row["autoRenew"] != true {
				continue
			}
			row["autoRenew"] = false
			billingState, err := encodeMonthlyBillingState(row)
			if err != nil {
				return rollback(err)
			}
			if _, err := client.ComputeAllocation.UpdateOneID(compute.ID).SetBillingStateJSON(billingState).Save(ctx); err != nil {
				return rollback(err)
			}
		}
		storages, err := client.StorageVolume.Query().Where(storagevolume.AccountID(accountID)).All(ctx)
		if err != nil {
			return rollback(err)
		}
		sort.Slice(storages, func(i, j int) bool { return storages[i].ID < storages[j].ID })
		for _, storage := range storages {
			current, err := client.StorageVolume.UpdateOneID(storage.ID).Save(ctx)
			if controlplaneent.IsNotFound(err) {
				continue
			}
			if err != nil {
				return rollback(err)
			}
			row := recordFromEnt(current, storageEntFields)
			if row["autoRenew"] != true {
				continue
			}
			row["autoRenew"] = false
			billingState, err := encodeMonthlyBillingState(row)
			if err != nil {
				return rollback(err)
			}
			if _, err := client.StorageVolume.UpdateOneID(storage.ID).SetBillingStateJSON(billingState).Save(ctx); err != nil {
				return rollback(err)
			}
		}
		workspaces, err := client.Workspace.Query().Where(workspace.OwnerUserID(userID)).All(ctx)
		if err != nil {
			return rollback(err)
		}
		sort.Slice(workspaces, func(i, j int) bool { return workspaces[i].ID < workspaces[j].ID })
		for _, entity := range workspaces {
			base := recordFromEnt(entity, workspaceEntFields)
			billing, err := decodeWorkspaceBillingState(entity.BillingStateJSON, base)
			if err != nil || billing == nil || billing["autoRenew"] != true {
				continue
			}
			billing["ownerUserId"], billing["currentComputeAllocationId"] = entity.OwnerUserID, entity.CurrentComputeAllocationID
			billing["state"], billing["status"] = entity.State, entity.Status
			billing["autoRenew"] = false
			billingState, err := encodeWorkspaceBillingState(billing)
			if err != nil {
				return rollback(err)
			}
			if _, err := client.Workspace.UpdateOneID(entity.ID).SetBillingStateJSON(billingState).Save(ctx); err != nil {
				return rollback(err)
			}
		}
	}
	return tx.Commit()
}

func (s *postgresEntStateStore) ListOrganizations(ctx context.Context) ([]map[string]any, error) {
	rows, err := loadRecordSet(ctx, s.client.Organization.Query().All, organizationEntFields)
	if err != nil {
		return nil, err
	}
	return filteredRecords(rows, "")
}

func (s *postgresEntStateStore) GetOrganizationByAccount(ctx context.Context, accountID string) (map[string]any, bool, error) {
	entity, err := s.client.Organization.Query().Where(organization.BillingAccountID(accountID)).Only(ctx)
	if controlplaneent.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return recordFromEnt(entity, organizationEntFields), true, nil
}

func (s *postgresEntStateStore) SaveOrganization(ctx context.Context, row map[string]any) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	client := tx.Client()
	if _, err := client.Account.Get(ctx, stringValue(row["billingAccountId"])); err != nil {
		_ = tx.Rollback()
		if controlplaneent.IsNotFound(err) {
			return errAccountNotFound
		}
		return err
	}
	if err := s.replaceRecord(ctx, row, func(id string) error { return client.Organization.DeleteOneID(id).Exec(ctx) }, func() any { return client.Organization.Create() }, organizationEntFields); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *postgresEntStateStore) ListMemberships(ctx context.Context) ([]map[string]any, error) {
	rows, err := loadRecordSet(ctx, s.client.Membership.Query().All, membershipEntFields)
	if err != nil {
		return nil, err
	}
	return filteredRecords(rows, "")
}

func (s *postgresEntStateStore) GetMembershipByAccount(ctx context.Context, accountID string) (map[string]any, bool, error) {
	entity, err := s.client.Membership.Query().Where(membership.AccountID(accountID)).Only(ctx)
	if controlplaneent.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return recordFromEnt(entity, membershipEntFields), true, nil
}

func (s *postgresEntStateStore) SaveMembership(ctx context.Context, row map[string]any) error {
	if stringValue(row["role"]) != "owner" {
		return errInvalidRole
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	client := tx.Client()
	accountID := stringValue(row["accountId"])
	if _, err := client.Account.Get(ctx, accountID); err != nil {
		_ = tx.Rollback()
		if controlplaneent.IsNotFound(err) {
			return errAccountNotFound
		}
		return err
	}
	organization, err := client.Organization.Get(ctx, stringValue(row["organizationId"]))
	if err != nil {
		_ = tx.Rollback()
		if controlplaneent.IsNotFound(err) {
			return errOrganizationNotFound
		}
		return err
	}
	user, err := client.User.Get(ctx, stringValue(row["userId"]))
	if err != nil {
		_ = tx.Rollback()
		if controlplaneent.IsNotFound(err) {
			return errMembershipUserNotFound
		}
		return err
	}
	if organization.BillingAccountID != accountID || user.AccountID != accountID {
		_ = tx.Rollback()
		return errMembershipAccountMismatch
	}
	if err := s.replaceRecord(ctx, row, func(id string) error { return client.Membership.DeleteOneID(id).Exec(ctx) }, func() any { return client.Membership.Create() }, membershipEntFields); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
