package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
)

func (a *controlPlaneWorkspaceLaunchStageAdapter) readWorkspaceLaunchKey(ctx context.Context, operation workspaceLaunchReconcileOperation) (workspaceLaunchStageObservation, error) {
	userID, groupID := operation.int64Fact("sub2apiUserId"), operation.int64Fact("workspaceKeyGroupId")
	name := workspaceReservedKeyName(operation.stringFact("workspaceId"))
	if userID <= 0 || groupID <= 0 || name == "" {
		return workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, errInvalidWorkspaceLaunchOperation
	}
	keys, err := a.service.WorkspaceKeysForConvergence(ctx, userID, name)
	if err != nil {
		return workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, err
	}
	reserved := workspaceKeysNamed(keys, name)
	if len(reserved) == 0 {
		return workspaceLaunchStageObservation{State: workspaceLaunchStageAbsent}, nil
	}
	if len(reserved) != 1 {
		return workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, clients.ErrSub2APIWorkspaceKeyAmbiguous
	}
	key := reserved[0]
	if key.ID <= 0 || key.UserID != userID || key.Status != "active" || key.GroupID == nil || *key.GroupID != groupID || strings.TrimSpace(key.Key) == "" {
		return workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, nil
	}
	return workspaceLaunchStageObservation{State: workspaceLaunchStageReady, Facts: map[string]any{
		"workspaceApiKeyId": key.ID, "workspaceKeyGroupId": groupID, "workspaceKeyStatus": workspaceKeyCodexGroupBound,
		"workspaceKeyFingerprint": workspaceLaunchCredentialFingerprint(key.Key),
	}}, nil
}

func (a *controlPlaneWorkspaceLaunchStageAdapter) mutateWorkspaceLaunchKey(ctx context.Context, operation workspaceLaunchReconcileOperation, idempotencyKey string) error {
	userID, groupID := operation.int64Fact("sub2apiUserId"), operation.int64Fact("workspaceKeyGroupId")
	if idempotencyKey != operation.ID+":workspace-key" || !a.workspaceLaunchKeyMutationCredentialValid(operation) {
		return errWorkspaceLaunchStageAdapterUnavailable
	}
	name := workspaceReservedKeyName(operation.stringFact("workspaceId"))
	keys, err := a.service.WorkspaceKeysForConvergence(ctx, userID, name)
	if err != nil {
		return err
	}
	reserved := workspaceKeysNamed(keys, name)
	if len(reserved) == 0 {
		_, err = a.service.CreateGatewayUserKey(ctx, a.keyCredential, userID, clients.Sub2APICreateKeyInput{Name: name, GroupID: groupID}, idempotencyKey)
		return err
	}
	if len(reserved) != 1 || reserved[0].ID <= 0 || reserved[0].UserID != userID || reserved[0].Status != "active" {
		return clients.ErrSub2APIWorkspaceKeyAmbiguous
	}
	if reserved[0].GroupID != nil && *reserved[0].GroupID == groupID {
		return nil
	}
	_, err = a.service.UpdateGatewayUserKey(ctx, a.keyCredential, userID, reserved[0].ID, clients.Sub2APIUpdateKeyInput{GroupID: &groupID})
	return err
}

func (a *controlPlaneWorkspaceLaunchStageAdapter) workspaceLaunchKeyMutationCredentialValid(operation workspaceLaunchReconcileOperation) bool {
	userID, groupID := operation.int64Fact("sub2apiUserId"), operation.int64Fact("workspaceKeyGroupId")
	return a != nil && userID > 0 && groupID > 0 && a.keyUserID == userID && strings.TrimSpace(a.keyCredential.Bearer) != "" &&
		a.keyCredential.ExpiresAt.After(time.Now())
}

func (a *controlPlaneWorkspaceLaunchStageAdapter) readWorkspaceLaunchDebit(ctx context.Context, operation workspaceLaunchReconcileOperation) (workspaceLaunchStageObservation, error) {
	userID, code := operation.int64Fact("sub2apiUserId"), operation.stringFact("sub2apiRedeemCode")
	if userID <= 0 || code == "" {
		return workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, errInvalidWorkspaceLaunchOperation
	}
	history, err := a.service.FinancialBalanceHistoryByCodes(ctx, userID, []string{code})
	if err != nil {
		return workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, err
	}
	entry, found := history[code]
	if !found {
		if workspaceLaunchDebitReadbackCanConverge(operation) {
			return workspaceLaunchStageObservation{State: workspaceLaunchStagePending}, nil
		}
		return workspaceLaunchStageObservation{State: workspaceLaunchStageAbsent}, nil
	}
	row := map[string]any{"sub2apiRedeemCode": code, "chargeUsdMicros": operation.int64Fact("totalChargeUsdMicros")}
	if reason := sub2APIReconciliationCode(row, userID, history); reason != "" {
		return workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, errors.New(reason)
	}
	if entry.UsedAt == nil || entry.UsedAt.IsZero() {
		if workspaceLaunchDebitReadbackCanConverge(operation) {
			return workspaceLaunchStageObservation{State: workspaceLaunchStagePending}, nil
		}
		return workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, nil
	}
	postChargeBalance, err := a.service.Sub2APIBalance(ctx, userID)
	if err != nil {
		if workspaceLaunchDebitReadbackCanConverge(operation) {
			return workspaceLaunchStageObservation{State: workspaceLaunchStagePending}, nil
		}
		return workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, err
	}
	if postChargeBalance.UserID != userID || postChargeBalance.USDMicros < 0 {
		return workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, nil
	}
	periodStart := entry.UsedAt.UTC()
	return workspaceLaunchStageObservation{State: workspaceLaunchStageReady, Facts: map[string]any{
		"chargeAttempted":            true,
		"chargeConfirmation":         map[string]any{"code": code, "userId": userID, "chargeUsdMicros": operation.int64Fact("totalChargeUsdMicros"), "status": "used"},
		"preChargeBalanceUsdMicros":  operation.int64Fact("preChargeBalanceUsdMicros"),
		"postChargeBalanceUsdMicros": postChargeBalance.USDMicros, "postChargeBalanceKnown": true,
		"billingPeriodState": "frozen", "periodStart": periodStart.Format(time.RFC3339Nano),
		"paidThrough": nextBillingMonth(periodStart, periodStart.Day()).Format(time.RFC3339Nano), "billingAnchorDay": periodStart.Day(),
	}}, nil
}

func workspaceLaunchDebitReadbackCanConverge(operation workspaceLaunchReconcileOperation) bool {
	attempt, ok := operation.Attempts["debit"]
	return ok && operation.Stage == "debit" && attempt.Attempted == 1 && attempt.Confirmed == 0 && attempt.Unknown == 0 &&
		attempt.Max == 1 && attempt.Status == "reserved" && attempt.IdempotencyKey == workspaceLaunchStageIdempotencyKey(operation, 1)
}

func (a *controlPlaneWorkspaceLaunchStageAdapter) mutateWorkspaceLaunchDebit(ctx context.Context, operation workspaceLaunchReconcileOperation) error {
	userID, code := operation.int64Fact("sub2apiUserId"), operation.stringFact("sub2apiRedeemCode")
	if userID <= 0 || code == "" || workspaceLaunchStageIdempotencyKey(operation, 1) != code {
		return errInvalidWorkspaceLaunchOperation
	}
	history, err := a.service.FinancialBalanceHistoryByCodes(ctx, userID, []string{code})
	if err != nil {
		return err
	}
	row := map[string]any{"sub2apiRedeemCode": code, "chargeUsdMicros": operation.int64Fact("totalChargeUsdMicros")}
	if reason := sub2APIReconciliationCode(row, userID, history); reason == "" {
		return nil
	} else if reason != "sub2api_charge_missing" {
		return errors.New(reason)
	}
	if err := a.preflightWorkspaceLaunchMonthly(ctx, operation); err != nil {
		return errors.Join(errWorkspaceLaunchMutationNotDispatched, err)
	}
	_, err = a.service.ChargeSub2API(ctx, clients.Sub2APIChargeInput{
		UserID: userID, Code: code, ChargeUSDMicros: operation.int64Fact("totalChargeUsdMicros"),
		Notes: "OPL Workspace launch " + operation.stringFact("workspaceId"),
	})
	return err
}

func workspaceLaunchPurchaseReceiptFromLedger(ctx context.Context, adapter *controlPlaneWorkspaceLaunchStageAdapter, input clients.ReceiptInput) (clients.Receipt, bool, error) {
	receipts, err := reconciliationLedgerReceipts(ctx, adapter.service, input.AccountID)
	if err != nil {
		return clients.Receipt{}, false, err
	}
	var match *clients.Receipt
	for index := range receipts {
		receipt := receipts[index]
		if receipt.RequestID != input.RequestID {
			continue
		}
		if !workspaceLaunchReceiptInputMatches(receipt.ReceiptInput, input) || receipt.ReceiptID == "" || match != nil {
			return clients.Receipt{}, false, errors.New("workspace_launch_receipt_identity_mismatch")
		}
		match = &receipt
	}
	if match == nil {
		return clients.Receipt{}, false, nil
	}
	return *match, true, nil
}

func workspaceLaunchReceiptInputMatches(actual, expected clients.ReceiptInput) bool {
	actualJSON, actualErr := json.Marshal(actual)
	expectedJSON, expectedErr := json.Marshal(expected)
	return actualErr == nil && expectedErr == nil && bytes.Equal(actualJSON, expectedJSON)
}

func workspaceLaunchCredentialFingerprint(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(digest[:])
}
