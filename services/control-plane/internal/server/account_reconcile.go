package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

const (
	acceptanceBAccountReconcileRoute                = "/api/operator/account-reconciliation"
	acceptanceBAccountReconcileHeader               = "X-OPL-Account-Reconcile-Email"
	acceptanceBAccountReconcilePageSize             = 50
	acceptanceBAccountReconcileMaxPages             = 1000
	acceptanceBAccountReconcileRechargeMicros int64 = 60_000_000
	acceptanceBAccountReconcileReason               = "production Basic Acceptance B account preparation"
)

var errAcceptanceBAccountReconcileUnknown = errors.New("acceptance_b_account_reconcile_unknown")

// acceptanceBAccountReconcileData is deliberately a redacted DTO. It contains
// no customer/account/user/resource identifiers and is safe to upload as an
// operator evidence artifact.
type acceptanceBAccountReconcileData struct {
	SchemaVersion                  int    `json:"schemaVersion"`
	OperationMode                  string `json:"operationMode"`
	Status                         string `json:"status"`
	CustomerIdentitySHA256         string `json:"customerIdentitySha256"`
	AccountProvisionIdentitySHA256 string `json:"accountProvisionIdentitySha256"`
	WalletAdjustmentIdentitySHA256 string `json:"walletAdjustmentIdentitySha256"`
	ApprovalIdentitySHA256         string `json:"approvalIdentitySha256"`
	WorkspaceDebitIdentitySHA256   string `json:"workspaceDebitIdentitySha256"`
	LocalGraph                     string `json:"localGraph"`
	RemoteIdentity                 string `json:"remoteIdentity"`
	CustomerLogin                  string `json:"customerLogin"`
	Wallet                         string `json:"wallet"`
	WalletUSDMicros                string `json:"walletUsdMicros,omitempty"`
	WalletAdjustment               string `json:"walletAdjustment"`
	ApprovalState                  string `json:"approvalState"`
	WorkspaceLaunchState           string `json:"workspaceLaunchState"`
	WorkspaceState                 string `json:"workspaceState"`
	WorkspaceKeyState              string `json:"workspaceKeyState"`
	WorkspaceReceiptState          string `json:"workspaceReceiptState"`
	WorkspaceDebitState            string `json:"workspaceDebitState"`
	WorkspaceCount                 int    `json:"workspaceCount"`
	LaunchCount                    int    `json:"launchCount"`
	KeyCount                       int    `json:"keyCount"`
	ReceiptCount                   int    `json:"receiptCount"`
	ReadbackError                  string `json:"readbackError,omitempty"`
}

// registerAcceptanceBAccountReconcileRoute installs a GET-only route. The
// email is accepted only in a request header so it never appears in a URL,
// response body, or redacted artifact. This handler never invokes a mutation
// client and does not persist a checkpoint.
func registerAcceptanceBAccountReconcileRoute(mux *http.ServeMux, app *controlPlaneServer, service *controlplane.Service) {
	mux.HandleFunc("GET "+acceptanceBAccountReconcileRoute, app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		email, err := canonicalEmail(r.Header.Get(acceptanceBAccountReconcileHeader))
		if err != nil {
			writeError(w, http.StatusBadRequest, "acceptance_b_account_reconcile_email_invalid")
			return
		}
		data, err := app.reconcileAcceptanceBAccount(r.Context(), service, email)
		if err != nil {
			if errors.Is(err, errAcceptanceBAccountReconcileUnknown) {
				// The reconcile contract is a readback, not an availability probe. A
				// failed authority read must remain an explicit unknown data state so
				// the runner can preserve the digest and refuse any mutation.
				writeSourceEnvelope(w, http.StatusOK, "control-plane+sub2api+ledger", "available", data)
				return
			}
			writeSourceEnvelope(w, http.StatusOK, "control-plane+sub2api+ledger", "available", data)
			return
		}
		writeSourceEnvelope(w, http.StatusOK, "control-plane+sub2api+ledger", "available", data)
	}))
}

func acceptanceBAccountDigest(email string) string {
	return acceptanceBDigestParts(email)
}

func acceptanceBAccountIdentityDigest(email string) string {
	return acceptanceBDigestParts("acceptance-b-account-provision-v1:" + acceptanceBAccountDigest(email))
}

func acceptanceBWalletIdentityDigest(accountID, email string) string {
	return acceptanceBDigestParts(accountID, acceptanceBWalletOperationID(accountID, email))
}

func productionAcceptanceBApprovalIdentityDigest(approval productionAcceptanceBApproval) string {
	parts := []string{
		"acceptance-b-approval-v1",
		strconv.Itoa(approval.SchemaVersion), approval.OperationMode, approval.ApprovalID, approval.ExpiresAt, approval.Confirmation,
		approval.Release.MergedMainSHA, approval.Release.CloudImageDigest, approval.Release.WorkspaceImageDigest,
		approval.Customer.Email, approval.Customer.AccountID,
		approval.Launch.IdempotencyKey, approval.Launch.OperationID, approval.Launch.WorkspaceID, approval.Launch.Name,
		approval.Launch.PackageID, strconv.Itoa(approval.Launch.SizeGB), strconv.FormatBool(approval.Launch.AutoRenew),
		approval.Expected.NodePoolID, approval.Expected.ResolvedInstanceType,
		"allowed-writes",
	}
	parts = append(parts, approval.AllowedWrites...)
	parts = append(parts, "forbidden-writes")
	parts = append(parts, approval.ForbiddenWrites...)
	return acceptanceBDigestParts(parts...)
}

func acceptanceBDigestParts(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func acceptanceBAccountOperationID(email string) string {
	key := "acceptance-b-account-provision-v1:" + acceptanceBAccountDigest(email)
	return "account-provision-" + stableID(key, email)[:18]
}

func acceptanceBWalletOperationID(accountID, email string) string {
	key := "acceptance-b-wallet-recharge-v1:" + accountID + ":" + acceptanceBAccountDigest(email)
	return "wallet-adjustment-" + stableID(accountID, key)[:18]
}

func (app *controlPlaneServer) reconcileAcceptanceBAccount(ctx context.Context, service *controlplane.Service, email string) (acceptanceBAccountReconcileData, error) {
	accountID := "acct-" + stableID("account", email)[:18]
	userID := "usr-" + stableID("customer", email)[:18]
	data := acceptanceBAccountReconcileData{
		SchemaVersion: 1, OperationMode: "acceptance_b_account_reconcile", Status: "unknown",
		CustomerIdentitySHA256:         acceptanceBAccountDigest(email),
		AccountProvisionIdentitySHA256: acceptanceBAccountIdentityDigest(email),
		WalletAdjustmentIdentitySHA256: acceptanceBWalletIdentityDigest(accountID, email),
		LocalGraph:                     "unknown", RemoteIdentity: "unknown", CustomerLogin: "not_attempted",
		Wallet: "unknown", WalletAdjustment: "unknown", ApprovalState: "unknown", WorkspaceLaunchState: "unknown",
		WorkspaceState: "unknown", WorkspaceKeyState: "unknown", WorkspaceReceiptState: "unknown", WorkspaceDebitState: "unknown",
	}

	localState, localAccount, localUser, err := app.acceptanceBLocalGraph(ctx, accountID, userID, email)
	if err != nil {
		data.ReadbackError = "local_authority_unavailable"
		return data, errAcceptanceBAccountReconcileUnknown
	}
	data.LocalGraph = localState

	remoteState, remote, err := acceptanceBRemoteIdentity(ctx, service, email)
	if err != nil {
		data.ReadbackError = "sub2api_authority_unavailable"
		return data, errAcceptanceBAccountReconcileUnknown
	}
	data.RemoteIdentity = remoteState

	if localState == "absent" && remoteState == "absent" {
		data.Status = "safe_to_retry_absent"
		return data, nil
	}
	if localState != "complete" || remoteState != "active" || localAccount == nil || localUser == nil || remote == nil ||
		int64(numberField(localAccount, "sub2apiUserId", 0)) != remote.ID || normalizeEmail(stringValue(localUser["email"])) != remote.Email {
		data.Status = "partial"
		return data, nil
	}
	data.CustomerIdentitySHA256 = acceptanceBDigestParts(accountID, userID, strconv.FormatInt(remote.ID, 10), email, "active")
	data.CustomerLogin = "active_identity_readback_only"

	wallet, err := service.Sub2APIBalance(ctx, remote.ID)
	if err != nil || wallet.UserID != remote.ID || wallet.Status != "active" || wallet.USDMicros < 0 {
		data.ReadbackError = "wallet_authority_unavailable"
		return data, errAcceptanceBAccountReconcileUnknown
	}
	data.Wallet = "available"
	data.WalletUSDMicros = strconv.FormatInt(wallet.USDMicros, 10)

	walletOperationID := acceptanceBWalletOperationID(accountID, email)
	operation, found, err := app.walletAdjustment(ctx, walletOperationID, "")
	if err != nil {
		data.ReadbackError = "wallet_adjustment_authority_invalid"
		return data, errAcceptanceBAccountReconcileUnknown
	}
	if !found {
		data.WalletAdjustment = "absent"
	} else if operation.AccountID != accountID || operation.Kind != "recharge" || operation.AmountUSDMicros != acceptanceBAccountReconcileRechargeMicros ||
		operation.Reason != acceptanceBAccountReconcileReason || operation.Status != "succeeded" || operation.Phase != "complete" ||
		!operation.BeforeBalanceKnown || !operation.AfterBalanceKnown || operation.AfterBalanceMicros-operation.BeforeBalanceMicros != acceptanceBAccountReconcileRechargeMicros {
		data.WalletAdjustment = "manual_review"
		data.Status = "manual_review"
		return data, nil
	} else {
		data.WalletAdjustment = "succeeded"
	}

	approval, parsed := parseProductionAcceptanceBApproval()
	if !parsed || !productionAcceptanceBDeploymentApproved(approval, accountID, email, time.Now()) {
		data.ReadbackError = "approval_authority_invalid"
		return data, errAcceptanceBAccountReconcileUnknown
	}
	data.ApprovalState = "bound"
	data.ApprovalIdentitySHA256 = productionAcceptanceBApprovalIdentityDigest(approval)
	redeemCode := monthlyRedeemCode(monthlyEnvironment(), approval.Launch.OperationID)

	launch, launchFound, err := app.tables.GetRuntimeOperation(ctx, approval.Launch.OperationID)
	if err != nil {
		data.ReadbackError = "launch_authority_unavailable"
		return data, errAcceptanceBAccountReconcileUnknown
	}
	data.WorkspaceLaunchState = "absent"
	expectedDebitUSDMicros := int64(0)
	if launchFound {
		data.LaunchCount = 1
		data.WorkspaceLaunchState = "present"
		if stringValue(launch["id"]) != approval.Launch.OperationID || stringValue(launch["accountId"]) != accountID ||
			stringValue(launch["workspaceId"]) != approval.Launch.WorkspaceID || stringValue(launch["action"]) != workspaceLaunchAction {
			data.WorkspaceLaunchState = "conflict"
		} else if operation, decodeErr := decodeWorkspaceLaunchReconcileOperation(launch); decodeErr != nil ||
			operation.ID != approval.Launch.OperationID || operation.stringFact("accountId") != accountID ||
			operation.int64Fact("sub2apiUserId") != remote.ID || operation.stringFact("workspaceId") != approval.Launch.WorkspaceID ||
			operation.stringFact("name") != approval.Launch.Name || operation.stringFact("packageId") != approval.Launch.PackageID ||
			operation.intFact("sizeGb") != approval.Launch.SizeGB || operation.boolFact("autoRenew") != approval.Launch.AutoRenew ||
			operation.stringFact("sub2apiRedeemCode") != redeemCode || operation.int64Fact("totalChargeUsdMicros") <= 0 {
			data.WorkspaceLaunchState = "conflict"
		} else {
			expectedDebitUSDMicros = operation.int64Fact("totalChargeUsdMicros")
		}
	}

	workspace, workspaceFound, err := app.tables.GetWorkspace(ctx, approval.Launch.WorkspaceID)
	if err != nil {
		data.ReadbackError = "workspace_authority_unavailable"
		return data, errAcceptanceBAccountReconcileUnknown
	}
	data.WorkspaceState = "absent"
	if workspaceFound {
		data.WorkspaceCount = 1
		data.WorkspaceState = "present"
		if stringValue(workspace["id"]) != approval.Launch.WorkspaceID || stringValue(workspace["ownerAccountId"]) != accountID {
			data.WorkspaceState = "conflict"
		}
	}

	keyName := workspaceReservedKeyName(approval.Launch.WorkspaceID)
	keys, err := service.WorkspaceKeysForConvergence(ctx, remote.ID, keyName)
	if err != nil {
		data.ReadbackError = "key_authority_unavailable"
		return data, errAcceptanceBAccountReconcileUnknown
	}
	data.WorkspaceKeyState = "absent"
	data.KeyCount = len(keys)
	if len(keys) > 0 {
		data.WorkspaceKeyState = "present"
		for _, key := range keys {
			if key.UserID != remote.ID || key.Name != keyName {
				data.WorkspaceKeyState = "conflict"
				break
			}
		}
	}

	data.ReceiptCount, err = acceptanceBWorkspaceReceiptCount(ctx, service, accountID, approval.Launch.WorkspaceID)
	if err != nil {
		data.ReadbackError = "ledger_authority_unavailable"
		return data, errAcceptanceBAccountReconcileUnknown
	}
	data.WorkspaceReceiptState = "absent"
	if data.ReceiptCount > 0 {
		data.WorkspaceReceiptState = "present"
	}

	data.WorkspaceDebitIdentitySHA256 = acceptanceBDigestParts(accountID, strconv.FormatInt(remote.ID, 10), approval.Launch.OperationID, approval.Launch.WorkspaceID, redeemCode)
	history, err := service.FinancialBalanceHistoryByCodes(ctx, remote.ID, []string{redeemCode})
	if err != nil {
		data.ReadbackError = "workspace_debit_authority_unavailable"
		return data, errAcceptanceBAccountReconcileUnknown
	}
	data.WorkspaceDebitState = "absent"
	if debit, found := history[redeemCode]; found {
		data.WorkspaceDebitState = "confirmed"
		if len(history) != 1 || debit.Code != redeemCode || debit.Type != "balance" || debit.Status != "used" || debit.UsedBy == nil || *debit.UsedBy != remote.ID || debit.UsedAt == nil ||
			expectedDebitUSDMicros <= 0 || debit.ValueUSDMicros != -expectedDebitUSDMicros {
			data.WorkspaceDebitState = "conflict"
		}
	} else if len(history) != 0 {
		data.WorkspaceDebitState = "conflict"
	}

	if data.WorkspaceLaunchState != "absent" || data.WorkspaceState != "absent" || data.WorkspaceKeyState != "absent" ||
		data.WorkspaceReceiptState != "absent" || data.WorkspaceDebitState != "absent" {
		data.Status = "partial"
		return data, nil
	}
	data.Status = "prepared"
	return data, nil
}

func acceptanceBWorkspaceReceiptCount(ctx context.Context, service *controlplane.Service, accountID, workspaceID string) (int, error) {
	cursor := ""
	count := 0
	for page := 0; page < acceptanceBAccountReconcileMaxPages; page++ {
		result, err := service.BillingReceipts(ctx, clients.ReceiptQuery{AccountID: accountID, TypePrefix: "billing.", Cursor: cursor, Limit: 100})
		if err != nil {
			return 0, err
		}
		for _, receipt := range result.Receipts {
			if receipt.Type == "billing.workspace_purchased.v1" && receipt.AccountID == accountID && receipt.WorkspaceID == workspaceID {
				count++
			}
		}
		if !result.HasMore {
			return count, nil
		}
		if strings.TrimSpace(result.NextCursor) == "" || result.NextCursor == cursor {
			return 0, errAcceptanceBAccountReconcileUnknown
		}
		cursor = result.NextCursor
	}
	return 0, errAcceptanceBAccountReconcileUnknown
}

func (app *controlPlaneServer) acceptanceBLocalGraph(ctx context.Context, accountID, userID, email string) (string, map[string]any, map[string]any, error) {
	account, accountFound, err := app.tables.GetAccount(ctx, accountID)
	if err != nil {
		return "unknown", nil, nil, err
	}
	user, userFound, err := app.tables.GetUserByEmail(ctx, email, true)
	if err != nil {
		return "unknown", nil, nil, err
	}
	if !accountFound && !userFound {
		return "absent", nil, nil, nil
	}
	if !accountFound || !userFound || stringValue(user["id"]) != userID || stringValue(user["accountId"]) != accountID ||
		normalizeEmail(stringValue(user["email"])) != email || stringValue(user["role"]) != "owner" || stringValue(user["status"]) != "active" ||
		stringValue(account["id"]) != accountID || stringValue(account["ownerUserId"]) != userID || stringValue(account["status"]) != "active" || int64(numberField(account, "sub2apiUserId", 0)) <= 0 {
		return "partial", account, user, nil
	}
	if owner, found, err := app.tables.GetUser(ctx, userID); err != nil {
		return "unknown", nil, nil, err
	} else if !found || stringValue(owner["id"]) != userID {
		return "partial", account, user, nil
	}
	return "complete", account, user, nil
}

func acceptanceBRemoteIdentity(ctx context.Context, service *controlplane.Service, email string) (string, *clients.Sub2APIUser, error) {
	var matches []clients.Sub2APIUser
	var total int64 = -1
	var pages int
	for page := 1; ; page++ {
		if page > acceptanceBAccountReconcileMaxPages {
			return "unknown", nil, errAcceptanceBAccountReconcileUnknown
		}
		result, err := service.Sub2APIAdminUsers(ctx, clients.Sub2APIUserPageQuery{Page: page, PageSize: acceptanceBAccountReconcilePageSize, Search: email, SortBy: "id", SortOrder: "asc"})
		if err != nil {
			return "unknown", nil, err
		}
		if page == 1 {
			total = result.Total
			pages = result.Pages
		} else if result.Total != total || result.Pages != pages {
			return "unknown", nil, errAcceptanceBAccountReconcileUnknown
		}
		for _, item := range result.Items {
			if normalizeEmail(item.Email) == email {
				matches = append(matches, item)
			}
		}
		if page >= pages {
			break
		}
	}
	if len(matches) == 0 {
		return "absent", nil, nil
	}
	if len(matches) != 1 {
		return "ambiguous", nil, nil
	}
	if matches[0].Status != "active" {
		return "disabled", &matches[0], nil
	}
	return "active", &matches[0], nil
}

func acceptanceBBillingReceiptCount(ctx context.Context, service *controlplane.Service, accountID string) (int, error) {
	cursor := ""
	count := 0
	for page := 0; page < acceptanceBAccountReconcileMaxPages; page++ {
		result, err := service.BillingReceipts(ctx, clients.ReceiptQuery{AccountID: accountID, TypePrefix: "billing.", Cursor: cursor, Limit: 100})
		if err != nil {
			return 0, err
		}
		count += len(result.Receipts)
		if !result.HasMore {
			return count, nil
		}
		if strings.TrimSpace(result.NextCursor) == "" || result.NextCursor == cursor {
			return 0, errAcceptanceBAccountReconcileUnknown
		}
		cursor = result.NextCursor
	}
	return 0, errAcceptanceBAccountReconcileUnknown
}
