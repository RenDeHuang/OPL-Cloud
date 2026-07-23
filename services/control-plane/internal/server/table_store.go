package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/mail"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/domain"
)

var errBillingOperationInProgress = errors.New("billing_operation_in_progress")
var errSub2APIAccountMappingConflict = errors.New("sub2api_account_mapping_conflict")
var errPrimaryWorkspaceExists = errors.New("primary_workspace_already_exists")
var errWorkspaceActivationConflict = errors.New("workspace_activation_conflict")
var errWorkspaceAPIKeyCASConflict = errors.New("workspace_api_key_cas_conflict")
var errInvalidAccountID = errors.New("invalid_account_id")
var errInvalidEmail = errors.New("invalid_email")
var errMembershipExists = errors.New("membership_already_exists")
var errAccountIdentityConflict = errors.New("account_identity_conflict")
var errAnnouncementStateConflict = errors.New("announcement_state_conflict")
var errAnnouncementNotActive = errors.New("announcement_not_active")

type workspaceCreateOperationResult struct {
	RequestHash          string                     `json:"requestHash"`
	LeaseExpiresAt       *time.Time                 `json:"leaseExpiresAt,omitempty"`
	Workspace            domain.WorkspaceProjection `json:"workspace"`
	AcceptedBillingState map[string]any             `json:"acceptedBillingState,omitempty"`
}

type announcementMutation struct {
	AnnouncementID  string
	Create          bool
	AllowedStatuses []string
	RequestHash     string
	Patch           map[string]any
	AuditEvent      map[string]any
}

func prepareAnnouncementMutation(current map[string]any, mutation announcementMutation, now time.Time) (map[string]any, error) {
	if mutation.AnnouncementID == "" || mutation.RequestHash == "" || stringValue(mutation.AuditEvent["id"]) == "" {
		return nil, errAnnouncementStateConflict
	}
	if mutation.Create {
		if current != nil {
			return nil, errIdempotencyConflict
		}
		current = map[string]any{"id": mutation.AnnouncementID, "createdAt": now.UTC().Format(time.RFC3339Nano)}
	} else {
		if current == nil || !announcementStatusAllowed(stringValue(current["status"]), mutation.AllowedStatuses) {
			return nil, errAnnouncementStateConflict
		}
		current = cloneMap(current)
	}
	for key, value := range mutation.Patch {
		current[key] = value
	}
	current["id"] = mutation.AnnouncementID
	current["updatedAt"] = now.UTC().Format(time.RFC3339Nano)
	if !validAnnouncementRecord(current) {
		return nil, errAnnouncementStateConflict
	}
	return current, nil
}

func announcementStatusAllowed(status string, allowed []string) bool {
	for _, candidate := range allowed {
		if status == candidate {
			return true
		}
	}
	return false
}

func validAnnouncementRecord(row map[string]any) bool {
	if strings.TrimSpace(stringValue(row["title"])) == "" || strings.TrimSpace(stringValue(row["body"])) == "" ||
		strings.TrimSpace(stringValue(row["createdByUserId"])) == "" || strings.TrimSpace(stringValue(row["updatedByUserId"])) == "" ||
		!announcementStatusAllowed(stringValue(row["status"]), []string{"draft", "scheduled", "published", "withdrawn"}) {
		return false
	}
	startsAt, startsOK := optionalAnnouncementTime(stringValue(row["startsAt"]))
	endsAt, endsOK := optionalAnnouncementTime(stringValue(row["endsAt"]))
	if !startsOK || !endsOK || (!startsAt.IsZero() && !endsAt.IsZero() && !endsAt.After(startsAt)) {
		return false
	}
	_, publishedOK := optionalAnnouncementTime(stringValue(row["publishedAt"]))
	return publishedOK
}

func optionalAnnouncementTime(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, true
	}
	parsed, err := time.Parse(time.RFC3339, value)
	return parsed, err == nil
}

func announcementIsActive(row map[string]any, now time.Time) bool {
	if !announcementStatusAllowed(stringValue(row["status"]), []string{"scheduled", "published"}) {
		return false
	}
	startsAt, startsOK := optionalAnnouncementTime(stringValue(row["startsAt"]))
	endsAt, endsOK := optionalAnnouncementTime(stringValue(row["endsAt"]))
	return startsOK && endsOK && (startsAt.IsZero() || !startsAt.After(now)) && (endsAt.IsZero() || endsAt.After(now))
}

func announcementReplay(audit map[string]any, mutation announcementMutation) (map[string]any, error) {
	if audit == nil {
		return nil, nil
	}
	after := mapField(audit, "after")
	announcement := mapField(after, "announcement")
	if stringValue(after["requestHash"]) != mutation.RequestHash || stringValue(audit["action"]) != stringValue(mutation.AuditEvent["action"]) ||
		stringValue(audit["resourceId"]) != mutation.AnnouncementID || stringValue(announcement["id"]) != mutation.AnnouncementID {
		return nil, errIdempotencyConflict
	}
	return announcement, nil
}

func announcementAudit(mutation announcementMutation, before, after map[string]any) map[string]any {
	audit := cloneMap(mutation.AuditEvent)
	audit["before"] = cloneMap(before)
	audit["after"] = map[string]any{"requestHash": mutation.RequestHash, "announcement": cloneMap(after)}
	return audit
}

func announcementReadID(announcementID, userID string) string {
	return "announcement-read-" + stableID(announcementID, userID)[:18]
}

func decodeWorkspaceCreateOperation(operation map[string]any) (workspaceCreateOperationResult, error) {
	var result workspaceCreateOperationResult
	if err := json.Unmarshal([]byte(stringValue(operation["result"])), &result); err != nil || result.RequestHash == "" || result.Workspace.ID == "" {
		return workspaceCreateOperationResult{}, errors.New("invalid_workspace_create_operation")
	}
	return result, nil
}

func encodeWorkspaceCreateOperation(result workspaceCreateOperationResult) string {
	payload, _ := json.Marshal(result)
	return string(payload)
}

func workspaceCreateClaimIdentity(result workspaceCreateOperationResult) string {
	billingState, _ := json.Marshal(result.AcceptedBillingState)
	return stableID(result.RequestHash, string(billingState))
}

func workspaceCreateClaimCompatible(current, claim workspaceCreateOperationResult, persisted map[string]any) bool {
	persistedID := stringValue(persisted["id"])
	persistedAccountID := firstNonEmpty(stringValue(persisted["accountId"]), stringValue(persisted["ownerAccountId"]))
	if persistedID == "" || persistedAccountID == "" ||
		current.Workspace.ID != claim.Workspace.ID || current.Workspace.ID != persistedID ||
		current.Workspace.AccountID != claim.Workspace.AccountID || current.Workspace.AccountID != persistedAccountID {
		return false
	}
	if workspaceCreateClaimIdentity(current) == workspaceCreateClaimIdentity(claim) {
		return true
	}
	persistedBillingState := workspaceAcceptedBillingState(persisted)
	claimBillingState := workspaceAcceptedBillingState(workspaceProjectionBillingRow(claim.Workspace, claim.AcceptedBillingState))
	return current.AcceptedBillingState == nil && claim.AcceptedBillingState != nil && current.RequestHash == claim.RequestHash &&
		persistedBillingState != nil && claimBillingState != nil &&
		workspaceCreateProjectionCompatible(persisted, current.Workspace, claimBillingState, true) &&
		workspaceCreateProjectionCompatible(persisted, claim.Workspace, claimBillingState, true)
}

type controlPlaneTableStore interface {
	ListAccounts(ctx context.Context, accountID string) ([]map[string]any, error)
	SaveAccount(ctx context.Context, row map[string]any) error
	CreateInvitedAccount(ctx context.Context, account, user, organization, membership map[string]any) error
	ApplyUserLifecycle(ctx context.Context, user map[string]any) error
	ListUsers(ctx context.Context, includeDeleted bool) ([]map[string]any, error)
	SaveUser(ctx context.Context, row map[string]any) error
	DeleteUser(ctx context.Context, id string) error
	ListSessions(ctx context.Context) (controlPlaneRecordSet, error)
	SaveSession(ctx context.Context, row map[string]any) error
	DeleteSession(ctx context.Context, id string) error
	ListOrganizations(ctx context.Context) ([]map[string]any, error)
	SaveOrganization(ctx context.Context, row map[string]any) error
	ListMemberships(ctx context.Context) ([]map[string]any, error)
	SaveMembership(ctx context.Context, row map[string]any) error

	ListComputes(ctx context.Context, accountID string) ([]map[string]any, error)
	SaveCompute(ctx context.Context, row map[string]any) error
	DeleteCompute(ctx context.Context, id string) error
	ListStorages(ctx context.Context, accountID string) ([]map[string]any, error)
	SaveStorage(ctx context.Context, row map[string]any) error
	DeleteStorage(ctx context.Context, id string) error
	ListAttachments(ctx context.Context, accountID string) ([]map[string]any, error)
	SaveAttachment(ctx context.Context, row map[string]any) error
	DeleteAttachment(ctx context.Context, id string) error
	ListWorkspaces(ctx context.Context, accountID string) ([]map[string]any, error)
	SaveWorkspace(ctx context.Context, row map[string]any) error
	CompareAndSwapWorkspaceAPIKey(ctx context.Context, workspaceID string, expectedID, newID int64) error
	ApplyWorkspaceRenewalIntent(ctx context.Context, update workspaceRenewalIntentCAS) error
	ClaimWorkspaceLaunch(ctx context.Context, claim workspaceLaunchClaimCAS) error
	PersistWorkspaceLaunch(ctx context.Context, update workspaceLaunchPersistCAS) error
	ClaimWorkspaceRenewal(ctx context.Context, claim workspaceRenewalClaimCAS) error
	PersistWorkspaceRenewal(ctx context.Context, update workspaceRenewalPersistCAS) error
	ActivateWorkspace(ctx context.Context, row map[string]any) (map[string]any, error)
	ClaimWorkspaceCreate(ctx context.Context, workspace map[string]any, operation map[string]any) error
	DeleteWorkspace(ctx context.Context, id string) error

	ListAuditEvents(ctx context.Context, accountID string) ([]map[string]any, error)
	SaveAuditEvent(ctx context.Context, row map[string]any) error
	ListAnnouncements(ctx context.Context) ([]map[string]any, error)
	ApplyAnnouncementMutation(ctx context.Context, mutation announcementMutation) (map[string]any, error)
	ListAnnouncementReads(ctx context.Context, userID string) ([]map[string]any, error)
	MarkAnnouncementRead(ctx context.Context, announcementID, userID, readAt string) (map[string]any, error)
	ListSupportMappings(ctx context.Context, accountID string) ([]map[string]any, error)
	SaveSupportMapping(ctx context.Context, row map[string]any) error
	ListRuntimeOperations(ctx context.Context) ([]map[string]any, error)
	SaveRuntimeOperation(ctx context.Context, row map[string]any) error
	BillingReconciliation(ctx context.Context) (map[string]any, bool, error)
	SaveBillingReconciliation(ctx context.Context, row map[string]any) error
}

func prepareWorkspaceActivation(row, owner, compute, storage, attachment, existing map[string]any) (map[string]any, error) {
	row = cloneMap(row)
	state := workspaceAcceptedBillingState(row)
	accountID, ownerID, workspaceID := firstNonEmpty(stringValue(row["accountId"]), stringValue(row["ownerAccountId"])), stringValue(row["ownerUserId"]), stringValue(row["id"])
	computeID, storageID, attachmentID := stringValue(row["currentComputeAllocationId"]), stringValue(row["storageId"]), stringValue(row["currentAttachmentId"])
	if state == nil || accountID == "" || ownerID == "" || workspaceID == "" || computeID == "" || storageID == "" || attachmentID == "" ||
		stringValue(compute["id"]) != computeID || stringValue(storage["id"]) != storageID || stringValue(attachment["id"]) != attachmentID ||
		stringValue(compute["accountId"]) != accountID || stringValue(storage["accountId"]) != accountID || stringValue(attachment["accountId"]) != accountID ||
		stringValue(compute["ownerUserId"]) != ownerID || stringValue(storage["ownerUserId"]) != ownerID ||
		stringValue(compute["workspaceId"]) != workspaceID || stringValue(storage["workspaceId"]) != workspaceID || stringValue(attachment["workspaceId"]) != workspaceID ||
		!workspaceActivationStatus(stringValue(compute["status"]), "compute") || !workspaceActivationStatus(stringValue(storage["status"]), "storage") || !workspaceActivationStatus(stringValue(attachment["status"]), "attachment") ||
		firstNonEmpty(stringValue(attachment["computeAllocationId"]), stringValue(attachment["computeId"])) != computeID ||
		firstNonEmpty(stringValue(attachment["storageId"]), stringValue(attachment["volumeId"])) != storageID ||
		!workspaceResourceCoversEntitlement("compute", compute, state) || !workspaceResourceCoversEntitlement("storage", storage, state) {
		return nil, errWorkspaceActivationConflict
	}
	if stringValue(owner["id"]) != ownerID || stringValue(owner["accountId"]) != accountID || stringValue(owner["status"]) != "active" || stringValue(owner["role"]) != "owner" {
		row["autoRenew"] = false
	}
	row, err := mergeWorkspaceForSave(existing, row)
	if err != nil || validateWorkspaceBillingState(row) != nil {
		return nil, errWorkspaceActivationConflict
	}
	return row, nil
}

func workspaceResourceCoversEntitlement(resourceType string, resource, state map[string]any) bool {
	paidThrough, paidErr := time.Parse(time.RFC3339, stringValue(state["paidThrough"]))
	if paidErr != nil {
		return false
	}
	if stringValue(resource["billingStatus"]) == "active" {
		resourcePaidThrough, err := time.Parse(time.RFC3339, stringValue(resource["paidThrough"]))
		return err == nil && !resourcePaidThrough.Before(paidThrough)
	}
	for _, key := range []string{"billingOperationId", "sub2apiRedeemCode", "chargeUsdMicros", "priceVersion", "periodStart", "paidThrough"} {
		if _, exists := resource[key]; exists {
			return false
		}
	}
	expected := map[string]any{
		"packageId": state["packageId"], "periodStart": state["periodStart"], "paidThrough": state["paidThrough"],
		"zone": firstNonEmpty(stringValue(resource["zone"]), providerDataValue(resource, "zone")),
	}
	if resourceType == "storage" {
		expected["sizeGb"] = state["storageGb"]
	}
	return monthlyPurchaseReadbackConfirmed(resourceType, expected, resource)
}

func workspaceActivationStatus(status, kind string) bool {
	switch kind {
	case "compute":
		return status == "running" || status == "ready" || status == "available" || status == "active"
	case "storage":
		return status == "available" || status == "ready" || status == "bound" || status == "attached"
	case "attachment":
		return status == "attached" || status == "ready"
	default:
		return false
	}
}

func validateSub2APIAccountMapping(accounts []map[string]any, row map[string]any) error {
	userID := int64(numberField(row, "sub2apiUserId", 0))
	if userID <= 0 {
		return nil
	}
	accountID := stringValue(row["id"])
	for _, existing := range accounts {
		if stringValue(existing["id"]) != accountID && int64(numberField(existing, "sub2apiUserId", 0)) == userID {
			return errSub2APIAccountMappingConflict
		}
	}
	return nil
}

func stageInvitedAccount(accounts, users, organizations, memberships controlPlaneRecordSet, account, user, organization, membership map[string]any) error {
	accountID := stringValue(account["id"])
	sub2APIUserID, mapped := positiveIntegerField(account, "sub2apiUserId")
	userID := stringValue(user["id"])
	if !validAccountID(accountID) || userID == "" || stringValue(account["ownerUserId"]) != userID {
		return errInvalidAccountID
	}
	if !mapped {
		return errMonthlyAccountUnmapped
	}
	accountRows, _ := filteredRecords(accounts, "")
	if err := validateSub2APIAccountMapping(accountRows, account); err != nil {
		return err
	}
	if stringValue(account["status"]) != "active" {
		return errInvalidAccountID
	}
	accountRow := cloneMap(account)
	if existing := accounts[accountID]; existing != nil {
		existingMapping := int64(numberField(existing, "sub2apiUserId", 0))
		if stringValue(existing["status"]) != "active" || existingMapping != sub2APIUserID || stringValue(existing["ownerUserId"]) != userID {
			return errAccountIdentityConflict
		}
		accountRow = cloneMap(existing)
	}

	email, err := canonicalEmail(stringValue(user["email"]))
	if err != nil {
		return err
	}
	if userID == "" || stringValue(user["accountId"]) != accountID || stringValue(user["status"]) != "active" {
		return errInvalidEmail
	}
	role := stringValue(user["role"])
	operator := userID == "usr-admin" && accountID == "acct-admin" && email == "admin@medopl.cn" && role == "admin"
	if (!operator && role != "owner") || stringValue(user["passwordHash"]) != "" {
		return errInvalidRole
	}
	userExists := false
	for _, existing := range users {
		sameID := stringValue(existing["id"]) == userID
		sameEmail := normalizeEmail(stringValue(existing["email"])) == email
		if stringValue(existing["accountId"]) == accountID && !sameID {
			return errAccountIdentityConflict
		}
		if !sameID && !sameEmail {
			continue
		}
		if !sameID || !sameEmail || stringValue(existing["accountId"]) != accountID || stringValue(existing["role"]) != role || stringValue(existing["status"]) != "active" {
			return errUserExists
		}
		userExists = true
	}
	userRow := cloneMap(user)
	userRow["email"] = email

	organizationID := stringValue(organization["id"])
	if organizationID == "" || stringValue(organization["billingAccountId"]) != accountID || stringValue(organization["status"]) != "active" {
		return errMembershipAccountMismatch
	}
	organizationRow, organizationExists := cloneMap(organization), false
	for _, existing := range organizations {
		sameID := stringValue(existing["id"]) == organizationID
		sameAccount := stringValue(existing["billingAccountId"]) == accountID
		if !sameID && !sameAccount {
			continue
		}
		if !sameID || !sameAccount || stringValue(existing["status"]) != "active" || stringValue(existing["name"]) != stringValue(organization["name"]) {
			return errMembershipAccountMismatch
		}
		organizationRow = cloneMap(existing)
		organizationExists = true
	}

	membershipID := stringValue(membership["id"])
	if membershipID == "" || stringValue(membership["accountId"]) != accountID || stringValue(membership["organizationId"]) != organizationID {
		return errMembershipAccountMismatch
	}
	if stringValue(membership["userId"]) != userID {
		return errMembershipUserNotFound
	}
	if stringValue(membership["role"]) != "owner" || stringValue(membership["status"]) != "active" {
		return errInvalidRole
	}
	membershipExists := false
	for _, existing := range memberships {
		collides := stringValue(existing["id"]) == membershipID || stringValue(existing["accountId"]) == accountID || stringValue(existing["organizationId"]) == organizationID || stringValue(existing["userId"]) == userID
		if !collides {
			continue
		}
		if stringValue(existing["id"]) != membershipID || stringValue(existing["accountId"]) != accountID || stringValue(existing["organizationId"]) != organizationID || stringValue(existing["userId"]) != userID || stringValue(existing["role"]) != "owner" || stringValue(existing["status"]) != "active" {
			return errMembershipExists
		}
		membershipExists = true
	}
	accounts[accountID] = accountRow
	if !userExists {
		users[userID] = userRow
	}
	if !organizationExists {
		organizations[organizationID] = organizationRow
	}
	if !membershipExists {
		memberships[membershipID] = cloneMap(membership)
	}
	return nil
}

func normalizeEmail(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func canonicalEmail(value string) (string, error) {
	email := normalizeEmail(value)
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email {
		return "", errInvalidEmail
	}
	return email, nil
}

func validAccountID(value string) bool {
	return len(value) >= 3 && len(value) <= 48 && value == compactID(value)
}

func preserveResourceAutoRenew(current, incoming map[string]any) controlPlaneRecord {
	row := cloneMap(incoming)
	if autoRenew, ok := current["autoRenew"]; ok {
		row["autoRenew"] = autoRenew
	} else {
		delete(row, "autoRenew")
	}
	return row
}
