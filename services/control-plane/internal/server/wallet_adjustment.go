package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

const maxWalletAdjustmentUSDMicros int64 = 1_000_000_000_000

var (
	errWalletAdjustmentAccount    = errors.New("wallet_adjustment_account_invalid")
	errWalletAdjustmentConflict   = errors.New("wallet_adjustment_conflict")
	errWalletAdjustmentState      = errors.New("wallet_adjustment_state_invalid")
	errWalletAdjustmentUpstream   = errors.New("wallet_adjustment_upstream_unavailable")
	walletAmountPattern           = regexp.MustCompile(`^(0|[1-9][0-9]{0,6})(\.[0-9]{1,6})?$`)
	walletRelatedOperationPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,199}$`)
)

type walletAdjustmentRequest struct {
	Kind                  string `json:"kind"`
	AmountUSD             string `json:"amountUsd"`
	Reason                string `json:"reason"`
	RelatedOperationID    string `json:"relatedOperationId,omitempty"`
	ConfirmationAccountID string `json:"confirmationAccountId"`
}

type walletAdjustmentRecoveryRequest struct {
	AccountID   string `json:"accountId"`
	EvidenceRef string `json:"evidenceRef"`
}

type walletAdjustmentUpstreamFailure struct {
	Phase      string `json:"phase"`
	HTTPStatus int    `json:"httpStatus,omitempty"`
	ErrorCode  string `json:"errorCode"`
	RequestID  string `json:"requestId,omitempty"`
}

type walletAdjustmentOperation struct {
	RequestHash          string                           `json:"requestHash"`
	Phase                string                           `json:"phase"`
	AccountID            string                           `json:"accountId"`
	Sub2APIUserID        int64                            `json:"sub2apiUserId"`
	Kind                 string                           `json:"kind"`
	AmountUSDMicros      int64                            `json:"amountUsdMicros"`
	AmountUSD            string                           `json:"amountUsd"`
	Reason               string                           `json:"reason"`
	RelatedOperationID   string                           `json:"relatedOperationId,omitempty"`
	ActorUserID          string                           `json:"actorUserId"`
	CanonicalRedeemCode  string                           `json:"canonicalRedeemCode,omitempty"`
	RedeemCodeVersion    string                           `json:"redeemCodeVersion,omitempty"`
	LegacySupersession   string                           `json:"legacySupersessionStatus,omitempty"`
	AdjustmentAttempted  bool                             `json:"adjustmentAttempted,omitempty"`
	BeforeBalanceKnown   bool                             `json:"beforeBalanceKnown,omitempty"`
	BeforeBalanceMicros  int64                            `json:"beforeBalanceUsdMicros,omitempty"`
	BeforeBalanceReadAt  string                           `json:"beforeBalanceReadAt,omitempty"`
	AfterBalanceKnown    bool                             `json:"afterBalanceKnown,omitempty"`
	AfterBalanceMicros   int64                            `json:"afterBalanceUsdMicros,omitempty"`
	AfterBalanceReadAt   string                           `json:"afterBalanceReadAt,omitempty"`
	BalanceHistoryRef    string                           `json:"balanceHistoryRef,omitempty"`
	BalanceHistoryUsedAt string                           `json:"balanceHistoryUsedAt,omitempty"`
	ReceiptID            string                           `json:"receiptId,omitempty"`
	ErrorCode            string                           `json:"errorCode,omitempty"`
	RecoveryRequestHash  string                           `json:"recoveryRequestHash,omitempty"`
	RecoveryAttempted    bool                             `json:"recoveryAttempted,omitempty"`
	RecoveryEvidenceRef  string                           `json:"recoveryEvidenceRef,omitempty"`
	RecoveryActorUserID  string                           `json:"recoveryActorUserId,omitempty"`
	RecoveryAuthorizedAt string                           `json:"recoveryAuthorizedAt,omitempty"`
	UpstreamFailure      *walletAdjustmentUpstreamFailure `json:"upstreamFailure,omitempty"`
	CreatedAt            string                           `json:"createdAt"`
	UpdatedAt            string                           `json:"updatedAt"`
	Status               string                           `json:"-"`
}

func (app *controlPlaneServer) createWalletAdjustment(w http.ResponseWriter, r *http.Request, service *controlplane.Service) {
	key, ok := requiredMutationKey(w, r)
	if !ok {
		return
	}
	accountID := strings.TrimSpace(r.PathValue("accountId"))
	var input walletAdjustmentRequest
	if decodeStrictGatewayRequest(r, &input) != nil {
		writeError(w, http.StatusBadRequest, "invalid_wallet_adjustment")
		return
	}
	amountMicros, ok := validWalletAdjustmentRequest(input, accountID, key)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_wallet_adjustment")
		return
	}
	actor, _ := app.sessionUserContext(r)
	actorID := stringValue(actor["id"])
	operationID := "wallet-adjustment-" + stableID(accountID, key)[:18]
	requestHash := stableID("wallet-adjustment-v1", accountID, actorID, input.Kind, strconv.FormatInt(amountMicros, 10), strings.TrimSpace(input.Reason), strings.TrimSpace(input.RelatedOperationID))

	// ponytail: Pilot is a single Control Plane pod; serialize the few operator wallet writes per account.
	unlock := app.lockResource("sub2api-wallet", accountID)
	defer unlock()

	operation, found, err := app.walletAdjustment(r.Context(), operationID, requestHash)
	if err != nil {
		writeWalletAdjustmentError(w, err)
		return
	}
	created := !found
	if !found {
		account, remoteUserID, accountErr := app.walletAdjustmentAccount(r.Context(), service, accountID)
		if accountErr != nil {
			writeWalletAdjustmentError(w, accountErr)
			return
		}
		if input.Kind == "business_refund" {
			if err := app.validateWalletRefundLimit(r.Context(), accountID, input.RelatedOperationID, amountMicros); err != nil {
				writeWalletAdjustmentError(w, err)
				return
			}
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		operation = walletAdjustmentOperation{
			RequestHash: requestHash, Phase: "before_balance", AccountID: accountID, Sub2APIUserID: remoteUserID,
			Kind: input.Kind, AmountUSDMicros: amountMicros, AmountUSD: formatWalletUSD(amountMicros), Reason: strings.TrimSpace(input.Reason),
			RelatedOperationID: strings.TrimSpace(input.RelatedOperationID), ActorUserID: actorID,
			CanonicalRedeemCode: walletAdjustmentRedeemCode(operationID), RedeemCodeVersion: "v2", CreatedAt: now, UpdatedAt: now, Status: "pending",
		}
		if stringValue(account["ownerUserId"]) == "" || app.persistWalletAdjustment(r.Context(), operationID, &operation) != nil {
			writeError(w, http.StatusInternalServerError, "state_persist_failed")
			return
		}
	}
	if operation.Status == "failed" {
		writeWalletAdjustmentError(w, errWalletAdjustmentConflict)
		return
	}

	if operation.Status != "succeeded" && operation.Status != "manual_review" {
		operation, err = app.runWalletAdjustment(r, service, operationID, operation)
		if err != nil {
			writeWalletAdjustmentError(w, err)
			return
		}
	}
	status := http.StatusOK
	if operation.Status == "manual_review" {
		status = http.StatusAccepted
	} else if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, walletAdjustmentDTO(operationID, operation))
}

func (app *controlPlaneServer) getWalletAdjustment(w http.ResponseWriter, r *http.Request) {
	operationID := strings.TrimSpace(r.PathValue("operationId"))
	operation, found, err := app.walletAdjustment(r.Context(), operationID, "")
	if err != nil {
		writeWalletAdjustmentError(w, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "wallet_adjustment_not_found")
		return
	}
	writeJSON(w, http.StatusOK, walletAdjustmentDTO(operationID, operation))
}

func (app *controlPlaneServer) recoverWalletAdjustment(w http.ResponseWriter, r *http.Request, service *controlplane.Service) {
	key, ok := requiredMutationKey(w, r)
	if !ok {
		return
	}
	operationID := strings.TrimSpace(r.PathValue("operationId"))
	var input walletAdjustmentRecoveryRequest
	if decodeStrictGatewayRequest(r, &input) != nil || !validAccountID(input.AccountID) || input.AccountID != strings.TrimSpace(input.AccountID) ||
		!validBillingReviewEvidenceRef(input.EvidenceRef) || operationID == "" || !validBillingReviewOpaqueID(key) {
		writeError(w, http.StatusBadRequest, "invalid_wallet_adjustment_recovery")
		return
	}
	recoveryHash := stableID("wallet-adjustment-recovery-v1", operationID, input.AccountID, input.EvidenceRef, key)
	actorID := app.sessionUserID(r)

	unlock := app.lockResource("sub2api-wallet", input.AccountID)
	defer unlock()
	operation, found, err := app.walletAdjustment(r.Context(), operationID, "")
	if err != nil {
		writeWalletAdjustmentError(w, err)
		return
	}
	if !found || operation.AccountID != input.AccountID {
		writeError(w, http.StatusNotFound, "wallet_adjustment_not_found")
		return
	}
	if operation.Status == "failed" {
		writeWalletAdjustmentError(w, errWalletAdjustmentConflict)
		return
	}
	if operation.Status == "succeeded" {
		if operation.RecoveryRequestHash != recoveryHash {
			writeWalletAdjustmentError(w, errWalletAdjustmentConflict)
			return
		}
	}
	if operation.Status != "succeeded" && operation.Status != "manual_review" && (operation.Status != "pending" || operation.RecoveryRequestHash != recoveryHash) {
		writeWalletAdjustmentError(w, errWalletAdjustmentConflict)
		return
	}

	if operation.Status == "manual_review" {
		operation, err = app.prepareWalletAdjustmentRecovery(r.Context(), service, operationID, operation, recoveryHash, input.EvidenceRef, actorID)
		if err != nil {
			writeWalletAdjustmentError(w, err)
			return
		}
	}
	if operation.Status == "pending" {
		operation, err = app.runWalletAdjustment(r, service, operationID, operation)
		if err != nil {
			writeWalletAdjustmentError(w, err)
			return
		}
	}
	audit := app.auditEvent(r, "gateway.wallet_adjustment.recover", "gateway_wallet", operation.AccountID, operation.AccountID, nil,
		map[string]any{"operationId": operationID, "evidenceRef": input.EvidenceRef, "status": operation.Status}, operation.Status)
	audit["id"] = "audit-" + stableID("gateway.wallet_adjustment.recover", operationID, key)[:12]
	if err := app.tables.SaveAuditEvent(r.Context(), audit); err != nil {
		writeError(w, http.StatusInternalServerError, "state_persist_failed")
		return
	}
	writeJSON(w, http.StatusOK, walletAdjustmentDTO(operationID, operation))
}

func (app *controlPlaneServer) prepareWalletAdjustmentRecovery(ctx context.Context, service *controlplane.Service, operationID string, operation walletAdjustmentOperation, requestHash, evidenceRef, actorID string) (walletAdjustmentOperation, error) {
	if operation.RecoveryRequestHash != "" && operation.RecoveryRequestHash != requestHash {
		return operation, errWalletAdjustmentConflict
	}
	if operation.RecoveryRequestHash == "" {
		operation.RecoveryRequestHash = requestHash
		operation.RecoveryEvidenceRef = evidenceRef
		operation.RecoveryActorUserID = actorID
		operation.RecoveryAuthorizedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err := app.persistWalletAdjustment(ctx, operationID, &operation); err != nil {
			return operation, errWalletAdjustmentState
		}
	}
	if operation.ErrorCode == "wallet_adjustment_recovery_history_conflict" || operation.ErrorCode == "wallet_adjustment_recovery_balance_changed" {
		return operation, errWalletAdjustmentConflict
	}
	history, err := service.FinancialBalanceHistoryScan(ctx, operation.Sub2APIUserID)
	if err != nil {
		recordWalletAdjustmentUpstreamFailure(&operation, "recovery_readback", err, "balance_history_unavailable")
		operation.ErrorCode = "wallet_adjustment_recovery_readback_unavailable"
		if persistErr := app.persistWalletAdjustment(ctx, operationID, &operation); persistErr != nil {
			return operation, errWalletAdjustmentState
		}
		return operation, nil
	}
	legacyCode, v2Code := legacyWalletAdjustmentRedeemCode(operationID), walletAdjustmentRedeemCode(operationID)
	legacyEntry, legacyState, legacyErr := inspectWalletAdjustmentHistory(history, legacyCode, operation)
	v2Entry, v2State, v2Err := inspectWalletAdjustmentHistory(history, v2Code, operation)
	confirmedLegacy, confirmedV2 := legacyState == "confirmed", v2State == "confirmed"
	if legacyErr != nil || v2Err != nil || confirmedLegacy && confirmedV2 || confirmedLegacy && operation.CanonicalRedeemCode != "" ||
		(confirmedLegacy || confirmedV2) && !operation.BeforeBalanceKnown {
		operation.ErrorCode = "wallet_adjustment_recovery_history_conflict"
		if persistErr := app.persistWalletAdjustment(ctx, operationID, &operation); persistErr != nil {
			return operation, errWalletAdjustmentState
		}
		return operation, errWalletAdjustmentConflict
	}
	operation.ErrorCode = ""
	if confirmedLegacy {
		operation.LegacySupersession = "legacy_history_confirmed"
		operation.BalanceHistoryUsedAt = legacyEntry.UsedAt.UTC().Format(time.RFC3339Nano)
		operation.BalanceHistoryRef = walletAdjustmentBalanceHistoryRef(operation.Sub2APIUserID, legacyEntry)
		operation.Status, operation.Phase = "pending", "after_balance"
		if err := app.persistWalletAdjustment(ctx, operationID, &operation); err != nil {
			return operation, errWalletAdjustmentState
		}
		return operation, nil
	}
	if confirmedV2 {
		wasLegacy := operation.CanonicalRedeemCode == "" && operation.RedeemCodeVersion == ""
		operation.CanonicalRedeemCode, operation.RedeemCodeVersion = v2Code, "v2"
		if wasLegacy {
			operation.LegacySupersession = "v2_history_confirmed"
		}
		operation.BalanceHistoryUsedAt = v2Entry.UsedAt.UTC().Format(time.RFC3339Nano)
		operation.BalanceHistoryRef = walletAdjustmentBalanceHistoryRef(operation.Sub2APIUserID, v2Entry)
		operation.Status, operation.Phase = "pending", "after_balance"
		if err := app.persistWalletAdjustment(ctx, operationID, &operation); err != nil {
			return operation, errWalletAdjustmentState
		}
		return operation, nil
	}
	legacyIdentity := operation.CanonicalRedeemCode == "" && operation.RedeemCodeVersion == "" && operation.LegacySupersession == ""
	v2Identity := operation.CanonicalRedeemCode == v2Code && operation.RedeemCodeVersion == "v2" && operation.LegacySupersession == ""
	recoveryEligible := (legacyIdentity || v2Identity) && len(legacyCode) > 32 &&
		operation.AdjustmentAttempted && operation.BeforeBalanceKnown && !operation.RecoveryAttempted &&
		operation.ReceiptID == "" && operation.BalanceHistoryRef == "" && operation.BalanceHistoryUsedAt == ""
	if !recoveryEligible {
		operation.ErrorCode = "wallet_adjustment_recovery_exhausted"
		if err := app.persistWalletAdjustment(ctx, operationID, &operation); err != nil {
			return operation, errWalletAdjustmentState
		}
		return operation, errWalletAdjustmentConflict
	}
	balance, err := service.Sub2APIBalance(ctx, operation.Sub2APIUserID)
	if err != nil {
		recordWalletAdjustmentUpstreamFailure(&operation, "recovery_balance", err, "balance_readback_unavailable")
		operation.ErrorCode = "wallet_adjustment_recovery_balance_unavailable"
		if persistErr := app.persistWalletAdjustment(ctx, operationID, &operation); persistErr != nil {
			return operation, errWalletAdjustmentState
		}
		return operation, nil
	}
	if balance.UserID != operation.Sub2APIUserID || balance.Status != "active" || balance.USDMicros != operation.BeforeBalanceMicros {
		operation.ErrorCode = "wallet_adjustment_recovery_balance_changed"
		if err := app.persistWalletAdjustment(ctx, operationID, &operation); err != nil {
			return operation, errWalletAdjustmentState
		}
		return operation, errWalletAdjustmentConflict
	}
	if legacyIdentity {
		operation.CanonicalRedeemCode, operation.RedeemCodeVersion, operation.LegacySupersession = v2Code, "v2", "v2_adopted"
	}
	operation.RecoveryAttempted, operation.AdjustmentAttempted = true, false
	operation.Status, operation.Phase = "pending", "adjustment"
	if err := app.persistWalletAdjustment(ctx, operationID, &operation); err != nil {
		return operation, errWalletAdjustmentState
	}
	return operation, nil
}

func validWalletAdjustmentRequest(input walletAdjustmentRequest, accountID, key string) (int64, bool) {
	reason, related := strings.TrimSpace(input.Reason), strings.TrimSpace(input.RelatedOperationID)
	if !validAccountID(accountID) || input.ConfirmationAccountID != accountID || strings.TrimSpace(key) != key || len(key) > 200 ||
		(input.Kind != "recharge" && input.Kind != "debit" && input.Kind != "business_refund") || input.AmountUSD != strings.TrimSpace(input.AmountUSD) ||
		!walletAmountPattern.MatchString(input.AmountUSD) || reason == "" || reason != input.Reason || len([]rune(reason)) > 200 || strings.IndexFunc(reason, unicode.IsControl) >= 0 {
		return 0, false
	}
	if input.Kind == "business_refund" {
		if related == "" || related != input.RelatedOperationID || !walletRelatedOperationPattern.MatchString(related) {
			return 0, false
		}
	} else if related != "" {
		return 0, false
	}
	amount, err := clients.ParseUSDDecimalMicros(input.AmountUSD)
	return amount, err == nil && amount > 0 && amount <= maxWalletAdjustmentUSDMicros
}

func (app *controlPlaneServer) walletAdjustmentAccount(ctx context.Context, service *controlplane.Service, accountID string) (map[string]any, int64, error) {
	accounts, err := app.tables.ListAccounts(ctx, accountID)
	if err != nil {
		return nil, 0, errWalletAdjustmentState
	}
	account := findRecord(accounts, accountID)
	remoteUserID, remoteOK := positiveIntegerField(account, "sub2apiUserId")
	if account == nil || stringValue(account["status"]) != "active" || !remoteOK {
		return nil, 0, errWalletAdjustmentAccount
	}
	users, err := app.tables.ListUsers(ctx, false)
	if err != nil {
		return nil, 0, errWalletAdjustmentState
	}
	owner := findRecord(users, stringValue(account["ownerUserId"]))
	if !ownsActiveAccount(account, owner) {
		return nil, 0, errWalletAdjustmentAccount
	}
	remote, err := service.Sub2APIUser(ctx, remoteUserID)
	if err != nil {
		return nil, 0, errWalletAdjustmentUpstream
	}
	if remote.Status != "active" || normalizeEmail(remote.Email) != normalizeEmail(stringValue(owner["email"])) {
		return nil, 0, errWalletAdjustmentAccount
	}
	return account, remoteUserID, nil
}

func (app *controlPlaneServer) walletAdjustment(ctx context.Context, operationID, requestHash string) (walletAdjustmentOperation, bool, error) {
	if operationID == "" {
		return walletAdjustmentOperation{}, false, nil
	}
	rows, err := app.tables.ListRuntimeOperations(ctx)
	if err != nil {
		return walletAdjustmentOperation{}, false, errWalletAdjustmentState
	}
	for _, row := range rows {
		if stringValue(row["id"]) != operationID {
			continue
		}
		operation, err := decodeWalletAdjustment(row)
		if err != nil {
			return walletAdjustmentOperation{}, false, errWalletAdjustmentState
		}
		if requestHash != "" && operation.RequestHash != requestHash {
			return walletAdjustmentOperation{}, false, errIdempotencyConflict
		}
		return operation, true, nil
	}
	return walletAdjustmentOperation{}, false, nil
}

func decodeWalletAdjustment(row map[string]any) (walletAdjustmentOperation, error) {
	var operation walletAdjustmentOperation
	if stringValue(row["action"]) != "gateway.wallet_adjustment.v1" || json.Unmarshal([]byte(stringValue(row["result"])), &operation) != nil {
		return walletAdjustmentOperation{}, errWalletAdjustmentState
	}
	operation.Status = stringValue(row["status"])
	operationID := stringValue(row["id"])
	v2Identity := operation.RedeemCodeVersion == "v2" && operation.CanonicalRedeemCode == walletAdjustmentRedeemCode(operationID)
	legacyReadOnly := operation.RedeemCodeVersion == "" && operation.CanonicalRedeemCode == "" &&
		(operation.Status == "manual_review" || operation.LegacySupersession == "legacy_history_confirmed")
	validSupersession := operation.LegacySupersession == "" || operation.LegacySupersession == "legacy_history_confirmed" ||
		operation.LegacySupersession == "v2_history_confirmed" || operation.LegacySupersession == "v2_adopted"
	if operation.RequestHash == "" || operation.AccountID == "" || operation.Sub2APIUserID <= 0 || operation.ActorUserID == "" || operation.CreatedAt == "" || operation.UpdatedAt == "" ||
		operation.AmountUSDMicros <= 0 || operation.AmountUSDMicros > maxWalletAdjustmentUSDMicros || stringValue(row["accountId"]) != operation.AccountID ||
		stringValue(row["resourceId"]) != operation.AccountID || stringValue(row["resourceKind"]) != "gateway_wallet" || operation.Status == "" ||
		(!v2Identity && !legacyReadOnly) || !validSupersession ||
		(operation.LegacySupersession == "legacy_history_confirmed" && !legacyReadOnly) ||
		((operation.LegacySupersession == "v2_history_confirmed" || operation.LegacySupersession == "v2_adopted") && !v2Identity) ||
		(operation.LegacySupersession == "v2_adopted" && !operation.RecoveryAttempted) ||
		(operation.RecoveryRequestHash != "" && (operation.RecoveryEvidenceRef == "" || operation.RecoveryActorUserID == "" || operation.RecoveryAuthorizedAt == "")) {
		return walletAdjustmentOperation{}, errWalletAdjustmentState
	}
	return operation, nil
}

func (app *controlPlaneServer) persistWalletAdjustment(ctx context.Context, operationID string, operation *walletAdjustmentOperation) error {
	if operation.Status == "pending" && operation.CanonicalRedeemCode == "" && operation.RedeemCodeVersion == "" && operation.LegacySupersession == "" && operation.RecoveryRequestHash == "" {
		operation.CanonicalRedeemCode, operation.RedeemCodeVersion = walletAdjustmentRedeemCode(operationID), "v2"
	}
	operation.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	payload, err := json.Marshal(operation)
	if err != nil {
		return err
	}
	return app.tables.SaveRuntimeOperation(ctx, map[string]any{
		"id": operationID, "operationId": operationID, "accountId": operation.AccountID, "resourceId": operation.AccountID,
		"resourceKind": "gateway_wallet", "action": "gateway.wallet_adjustment.v1", "status": operation.Status,
		"result": string(payload), "createdAt": operation.CreatedAt,
	})
}

func (app *controlPlaneServer) runWalletAdjustment(r *http.Request, service *controlplane.Service, operationID string, operation walletAdjustmentOperation) (walletAdjustmentOperation, error) {
	ctx := r.Context()
	for range 8 {
		switch operation.Phase {
		case "before_balance":
			balance, err := service.Sub2APIBalance(ctx, operation.Sub2APIUserID)
			if err != nil || balance.UserID != operation.Sub2APIUserID || balance.Status != "active" || balance.USDMicros < 0 {
				recordWalletAdjustmentUpstreamFailure(&operation, "before_balance", err, "balance_readback_unavailable")
				if persistErr := app.persistWalletAdjustment(ctx, operationID, &operation); persistErr != nil {
					return operation, errWalletAdjustmentState
				}
				return operation, errWalletAdjustmentUpstream
			}
			if operation.Kind == "debit" && balance.USDMicros < operation.AmountUSDMicros || operation.Kind != "debit" && balance.USDMicros > math.MaxInt64-operation.AmountUSDMicros {
				operation.BeforeBalanceKnown, operation.BeforeBalanceMicros = true, balance.USDMicros
				operation.BeforeBalanceReadAt = time.Now().UTC().Format(time.RFC3339Nano)
				operation.Status, operation.Phase, operation.ErrorCode = "failed", "complete", errWalletAdjustmentConflict.Error()
				if err := app.persistWalletAdjustment(ctx, operationID, &operation); err != nil {
					return operation, errWalletAdjustmentState
				}
				return operation, errWalletAdjustmentConflict
			}
			operation.BeforeBalanceKnown, operation.BeforeBalanceMicros = true, balance.USDMicros
			operation.BeforeBalanceReadAt, operation.Phase = time.Now().UTC().Format(time.RFC3339Nano), "adjustment"
			if err := app.persistWalletAdjustment(ctx, operationID, &operation); err != nil {
				return operation, errWalletAdjustmentState
			}
		case "adjustment":
			if operation.RedeemCodeVersion != "v2" || operation.CanonicalRedeemCode != walletAdjustmentRedeemCode(operationID) {
				return operation, errWalletAdjustmentState
			}
			if !operation.AdjustmentAttempted {
				operation.AdjustmentAttempted, operation.Phase = true, "authoritative_readback"
				if err := app.persistWalletAdjustment(ctx, operationID, &operation); err != nil {
					return operation, errWalletAdjustmentState
				}
				var err error
				if operation.Kind == "debit" {
					_, err = service.ChargeSub2API(ctx, clients.Sub2APIChargeInput{UserID: operation.Sub2APIUserID, Code: operation.CanonicalRedeemCode, ChargeUSDMicros: operation.AmountUSDMicros, Notes: operation.Reason})
				} else {
					_, err = service.RefundSub2API(ctx, clients.Sub2APIRefundInput{UserID: operation.Sub2APIUserID, Code: operation.CanonicalRedeemCode, RefundUSDMicros: operation.AmountUSDMicros, Notes: operation.Reason})
				}
				if err != nil {
					recordWalletAdjustmentUpstreamFailure(&operation, "adjustment", err, "adjustment_unconfirmed")
				}
				if err != nil && !errors.Is(err, clients.ErrSub2APIChargeUnknown) && !errors.Is(err, clients.ErrSub2APIChargeConflict) {
					return app.manualReviewWalletAdjustment(r, operationID, operation, "adjustment_unconfirmed")
				}
			} else {
				operation.Phase = "authoritative_readback"
			}
		case "authoritative_readback":
			if operation.RedeemCodeVersion != "v2" || operation.CanonicalRedeemCode != walletAdjustmentRedeemCode(operationID) {
				return operation, errWalletAdjustmentState
			}
			history, err := service.FinancialBalanceHistoryScan(ctx, operation.Sub2APIUserID)
			entry, confirmErr := confirmWalletAdjustmentHistory(history, operation.CanonicalRedeemCode, operation)
			if err != nil || confirmErr != nil {
				recordWalletAdjustmentUpstreamFailure(&operation, "authoritative_readback", err, "balance_history_unavailable")
				return app.manualReviewWalletAdjustment(r, operationID, operation, "authoritative_readback_unavailable")
			}
			operation.BalanceHistoryUsedAt = entry.UsedAt.UTC().Format(time.RFC3339Nano)
			operation.BalanceHistoryRef = walletAdjustmentBalanceHistoryRef(operation.Sub2APIUserID, entry)
			operation.Phase = "after_balance"
			if err := app.persistWalletAdjustment(ctx, operationID, &operation); err != nil {
				return operation, errWalletAdjustmentState
			}
		case "after_balance":
			balance, err := service.Sub2APIBalance(ctx, operation.Sub2APIUserID)
			expected := operation.BeforeBalanceMicros + operation.AmountUSDMicros
			if operation.Kind == "debit" {
				expected = operation.BeforeBalanceMicros - operation.AmountUSDMicros
			}
			if err != nil || balance.UserID != operation.Sub2APIUserID || balance.Status != "active" || balance.USDMicros != expected {
				recordWalletAdjustmentUpstreamFailure(&operation, "after_balance", err, "balance_readback_mismatch")
				return app.manualReviewWalletAdjustment(r, operationID, operation, "balance_readback_mismatch")
			}
			operation.AfterBalanceKnown, operation.AfterBalanceMicros = true, balance.USDMicros
			operation.AfterBalanceReadAt, operation.Phase = time.Now().UTC().Format(time.RFC3339Nano), "ledger"
			if err := app.persistWalletAdjustment(ctx, operationID, &operation); err != nil {
				return operation, errWalletAdjustmentState
			}
		case "ledger":
			receipt, err := service.RecordMonthlyReceipt(ctx, walletAdjustmentReceipt(operationID, operation), operationID+":ledger")
			if err != nil || receipt.ReceiptID == "" {
				return operation, errWalletAdjustmentUpstream
			}
			operation.ReceiptID, operation.Phase = receipt.ReceiptID, "audit"
			if err := app.persistWalletAdjustment(ctx, operationID, &operation); err != nil {
				return operation, errWalletAdjustmentState
			}
		case "audit":
			if err := app.saveWalletAdjustmentAudit(r, operationID, operation, "succeeded"); err != nil {
				return operation, errWalletAdjustmentState
			}
			operation.Status, operation.Phase, operation.ErrorCode = "succeeded", "complete", ""
			if err := app.persistWalletAdjustment(ctx, operationID, &operation); err != nil {
				return operation, errWalletAdjustmentState
			}
		case "manual_review_audit":
			return app.manualReviewWalletAdjustment(r, operationID, operation, operation.ErrorCode)
		case "complete":
			return operation, nil
		default:
			return operation, errWalletAdjustmentState
		}
	}
	return operation, errWalletAdjustmentState
}

func confirmWalletAdjustmentHistory(history []clients.Sub2APIBalanceHistoryEntry, code string, operation walletAdjustmentOperation) (clients.Sub2APIBalanceHistoryEntry, error) {
	entry, state, err := inspectWalletAdjustmentHistory(history, code, operation)
	if err != nil || state != "confirmed" {
		return clients.Sub2APIBalanceHistoryEntry{}, errWalletAdjustmentConflict
	}
	return entry, nil
}

func inspectWalletAdjustmentHistory(history []clients.Sub2APIBalanceHistoryEntry, code string, operation walletAdjustmentOperation) (clients.Sub2APIBalanceHistoryEntry, string, error) {
	signed := operation.AmountUSDMicros
	if operation.Kind == "debit" {
		signed = -signed
	}
	var match *clients.Sub2APIBalanceHistoryEntry
	for i := range history {
		if history[i].Code != code {
			continue
		}
		if match != nil {
			return clients.Sub2APIBalanceHistoryEntry{}, "conflict", errWalletAdjustmentConflict
		}
		match = &history[i]
	}
	if match == nil {
		return clients.Sub2APIBalanceHistoryEntry{}, "absent", nil
	}
	if match.Type != "balance" || match.Status != "used" || match.UsedBy == nil || *match.UsedBy != operation.Sub2APIUserID || match.UsedAt == nil || match.ValueUSDMicros != signed {
		return clients.Sub2APIBalanceHistoryEntry{}, "conflict", errWalletAdjustmentConflict
	}
	return *match, "confirmed", nil
}

func walletAdjustmentBalanceHistoryRef(userID int64, entry clients.Sub2APIBalanceHistoryEntry) string {
	return "sub2api:balance-history:" + strconv.FormatInt(userID, 10) + ":" + stableID(entry.Code, entry.CreatedAt.Format(time.RFC3339Nano))[:18]
}

func recordWalletAdjustmentUpstreamFailure(operation *walletAdjustmentOperation, phase string, err error, fallbackCode string) {
	details, ok := clients.Sub2APIFailure(err)
	if !ok && operation.UpstreamFailure != nil {
		return
	}
	if !ok {
		details.ErrorCode = fallbackCode
	}
	if details.ErrorCode == "" {
		details.ErrorCode = fallbackCode
	}
	operation.UpstreamFailure = &walletAdjustmentUpstreamFailure{
		Phase: phase, HTTPStatus: details.HTTPStatus, ErrorCode: details.ErrorCode, RequestID: details.RequestID,
	}
}

func (app *controlPlaneServer) manualReviewWalletAdjustment(r *http.Request, operationID string, operation walletAdjustmentOperation, errorCode string) (walletAdjustmentOperation, error) {
	if operation.Phase != "manual_review_audit" {
		operation.Status, operation.Phase, operation.ErrorCode = "pending", "manual_review_audit", errorCode
		if err := app.persistWalletAdjustment(r.Context(), operationID, &operation); err != nil {
			return operation, errWalletAdjustmentState
		}
	}
	if err := app.saveWalletAdjustmentAudit(r, operationID, operation, "manual_review"); err != nil {
		return operation, errWalletAdjustmentState
	}
	operation.Status, operation.Phase = "manual_review", "authoritative_readback"
	if err := app.persistWalletAdjustment(r.Context(), operationID, &operation); err != nil {
		return operation, errWalletAdjustmentState
	}
	return operation, nil
}

func walletAdjustmentReceipt(operationID string, operation walletAdjustmentOperation) clients.ReceiptInput {
	inputRefs := map[string]any{"balanceHistoryRef": operation.BalanceHistoryRef}
	if operation.RelatedOperationID != "" {
		inputRefs["relatedOperationId"] = operation.RelatedOperationID
	}
	return clients.ReceiptInput{
		Type: "gateway.wallet_adjustment.v1", Status: "completed", Surface: "control_plane", AccountID: operation.AccountID,
		RequestID: operationID, Actor: map[string]any{"userId": operation.ActorUserID},
		Execution: map[string]any{"operationId": operationID, "kind": operation.Kind, "amountUsdMicros": operation.AmountUSDMicros},
		InputRefs: inputRefs, Owner: map[string]any{"accountId": operation.AccountID},
	}
}

func (app *controlPlaneServer) saveWalletAdjustmentAudit(r *http.Request, operationID string, operation walletAdjustmentOperation, result string) error {
	before := map[string]any{"balance": walletBalanceEnvelope(operation.BeforeBalanceKnown, operation.BeforeBalanceMicros, operation.BeforeBalanceReadAt)}
	after := map[string]any{
		"kind": operation.Kind, "amountUsd": operation.AmountUSD, "reason": operation.Reason, "status": result,
		"balance": walletBalanceEnvelope(operation.AfterBalanceKnown, operation.AfterBalanceMicros, operation.AfterBalanceReadAt),
	}
	if operation.RelatedOperationID != "" {
		after["relatedOperationId"] = operation.RelatedOperationID
	}
	if operation.BalanceHistoryRef != "" {
		after["balanceHistoryRef"] = operation.BalanceHistoryRef
	}
	event := app.auditEvent(r, "gateway.wallet_adjustment", "gateway_wallet", operation.AccountID, operation.AccountID, before, after, result)
	event["id"] = "audit-" + stableID("gateway.wallet_adjustment", operationID)[:12]
	event["createdAt"] = operation.UpdatedAt
	return app.tables.SaveAuditEvent(r.Context(), event)
}

func (app *controlPlaneServer) validateWalletRefundLimit(ctx context.Context, accountID, relatedOperationID string, amount int64) error {
	rows, err := app.tables.ListRuntimeOperations(ctx)
	if err != nil {
		return errWalletAdjustmentState
	}
	var original int64
	found := false
	var refunded int64
	for _, row := range rows {
		if stringValue(row["accountId"]) != accountID {
			continue
		}
		if stringValue(row["id"]) == relatedOperationID {
			original, found = refundableWalletOperationAmount(row)
			continue
		}
		if stringValue(row["action"]) != "gateway.wallet_adjustment.v1" {
			continue
		}
		operation, decodeErr := decodeWalletAdjustment(row)
		if decodeErr != nil {
			return errWalletAdjustmentState
		}
		if operation.Kind != "business_refund" || operation.RelatedOperationID != relatedOperationID {
			continue
		}
		if operation.Status == "manual_review" || operation.Status != "succeeded" || operation.AmountUSDMicros > math.MaxInt64-refunded {
			return errWalletAdjustmentConflict
		}
		refunded += operation.AmountUSDMicros
	}
	if !found || original <= 0 || amount > original || refunded > original-amount {
		return errWalletAdjustmentConflict
	}
	return nil
}

func refundableWalletOperationAmount(row map[string]any) (int64, bool) {
	if stringValue(row["status"]) != "succeeded" {
		return 0, false
	}
	decoder := json.NewDecoder(bytes.NewBufferString(stringValue(row["result"])))
	decoder.UseNumber()
	var result map[string]any
	if decoder.Decode(&result) != nil {
		return 0, false
	}
	var field string
	switch stringValue(row["action"]) {
	case "workspace.launch", "workspace.launch.v2":
		field = "totalChargeUsdMicros"
	case "workspace.renewal":
		field = "totalUsdMicros"
	default:
		return 0, false
	}
	amount, ok := requiredNonNegativeInteger(result, field)
	return amount, ok && amount > 0
}

func walletAdjustmentDTO(operationID string, operation walletAdjustmentOperation) map[string]any {
	beforeReadAt, afterReadAt := operation.BeforeBalanceReadAt, operation.AfterBalanceReadAt
	if beforeReadAt == "" {
		beforeReadAt = operation.UpdatedAt
	}
	if afterReadAt == "" {
		afterReadAt = operation.UpdatedAt
	}
	result := map[string]any{
		"operationId": operationID, "status": operation.Status, "phase": operation.Phase, "accountId": operation.AccountID,
		"kind": operation.Kind, "amountUsd": operation.AmountUSD, "reason": operation.Reason,
		"beforeBalance": walletBalanceEnvelope(operation.BeforeBalanceKnown, operation.BeforeBalanceMicros, beforeReadAt),
		"afterBalance":  walletBalanceEnvelope(operation.AfterBalanceKnown, operation.AfterBalanceMicros, afterReadAt),
		"actor":         operation.ActorUserID, "createdAt": operation.CreatedAt, "updatedAt": operation.UpdatedAt,
	}
	for key, value := range map[string]string{
		"relatedOperationId": operation.RelatedOperationID, "balanceHistoryRef": operation.BalanceHistoryRef,
		"receiptId": operation.ReceiptID, "errorCode": operation.ErrorCode,
	} {
		if value != "" {
			result[key] = value
		}
	}
	if operation.UpstreamFailure != nil {
		result["upstreamFailure"] = operation.UpstreamFailure
	}
	if operation.Status == "manual_review" {
		result["allowedActions"] = []string{"recover_wallet_adjustment"}
	}
	return result
}

func walletBalanceEnvelope(known bool, micros int64, fetchedAt string) map[string]any {
	result := map[string]any{"source": "sub2api", "status": "unavailable", "available": false, "fetchedAt": fetchedAt}
	if known {
		result["status"], result["available"] = "available", true
		result["data"] = map[string]any{"currency": "USD", "usdMicros": micros}
	}
	return result
}

func walletAdjustmentRedeemCode(operationID string) string {
	return "opl:" + stableID("sub2api-wallet-adjustment-v2", operationID)[:28]
}

func legacyWalletAdjustmentRedeemCode(operationID string) string {
	return "opl:wallet-adjustment:" + stableID(operationID)[:24] + ":v1"
}

func formatWalletUSD(micros int64) string {
	whole, fraction := micros/1_000_000, micros%1_000_000
	decimal := strings.TrimRight(fmt.Sprintf("%06d", fraction), "0")
	if len(decimal) < 2 {
		decimal += strings.Repeat("0", 2-len(decimal))
	}
	return strconv.FormatInt(whole, 10) + "." + decimal
}

func writeWalletAdjustmentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errIdempotencyConflict), errors.Is(err, errWalletAdjustmentConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, errWalletAdjustmentAccount):
		writeError(w, http.StatusNotFound, "wallet_adjustment_account_not_found")
	case errors.Is(err, errWalletAdjustmentState):
		writeError(w, http.StatusInternalServerError, "state_persist_failed")
	default:
		writeError(w, http.StatusBadGateway, errWalletAdjustmentUpstream.Error())
	}
}
