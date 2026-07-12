package ledger

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	ledgerent "opl-cloud/services/ledger/ent"
	"opl-cloud/services/ledger/ent/evidencereceipt"
	"opl-cloud/services/ledger/ent/hold"
	"opl-cloud/services/ledger/ent/holdactivation"
	"opl-cloud/services/ledger/ent/holdrelease"
	"opl-cloud/services/ledger/ent/ledgerentry"
	"opl-cloud/services/ledger/ent/manualtopup"
	"opl-cloud/services/ledger/ent/predicate"
	"opl-cloud/services/ledger/ent/reconciliationreport"
	"opl-cloud/services/ledger/ent/resourcesettlement"
	"opl-cloud/services/ledger/ent/reviewpolicy"
	"opl-cloud/services/ledger/ent/wallettransaction"
)

//go:embed ent_migrations/*.sql
var ledgerMigrations embed.FS

type PostgresStore struct {
	client *ledgerent.Client
	db     *sql.DB
	now    func() time.Time
}

func PostgresSchemaSQL() string {
	entries, err := ledgerMigrations.ReadDir("ent_migrations")
	if err != nil {
		return ""
	}
	var out strings.Builder
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		data, err := ledgerMigrations.ReadFile("ent_migrations/" + entry.Name())
		if err != nil {
			return ""
		}
		out.Write(data)
		out.WriteByte('\n')
	}
	return out.String()
}

func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{
		client: ledgerent.NewClient(ledgerent.Driver(entsql.OpenDB(dialect.Postgres, db))),
		db:     db,
		now:    func() time.Time { return time.Now().UTC() },
	}
}

func (s *PostgresStore) Install(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, PostgresSchemaSQL())
	return err
}

func (s *PostgresStore) ManualTopUp(ctx context.Context, input ManualTopUpInput) (ManualTopUpResult, error) {
	requestHash, err := hashManualTopUp(input)
	if err != nil {
		return ManualTopUpResult{}, err
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return ManualTopUpResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	if existing, existingHash, err := s.manualTopUpByIdempotencyKey(ctx, tx, input.IdempotencyKey); err == nil {
		if existingHash != requestHash {
			return ManualTopUpResult{}, ErrIdempotencyConflict
		}
		existing.Replayed = true
		return existing, tx.Commit()
	} else if !ledgerent.IsNotFound(err) {
		return ManualTopUpResult{}, err
	}

	now := s.now()
	wallet, err := s.ensureWallet(ctx, tx, input.AccountID, input.Currency, now)
	if err != nil {
		return ManualTopUpResult{}, err
	}
	wallet.BalanceCents += input.AmountCents
	wallet.Currency = input.Currency
	wallet.AvailableCents = wallet.BalanceCents - wallet.FrozenCents
	wallet.UpdatedAt = now
	if err := s.saveWallet(ctx, tx, wallet); err != nil {
		return ManualTopUpResult{}, err
	}

	entry := LedgerEntry{ID: postgresID("le", now), AccountID: input.AccountID, AmountCents: input.AmountCents, Currency: input.Currency, Direction: "credit", Source: "manual_topup", OperatorUserID: input.OperatorUserID, Reason: input.Reason, CreatedAt: now}
	if err := createLedgerEntry(ctx, tx, entry); err != nil {
		return ManualTopUpResult{}, err
	}
	walletTx := WalletTransaction{ID: postgresID("wtx", now.Add(time.Nanosecond)), AccountID: input.AccountID, LedgerEntryID: entry.ID, AmountCents: input.AmountCents, BalanceCents: wallet.BalanceCents, FrozenCents: wallet.FrozenCents, AvailableCents: wallet.AvailableCents, TotalSpentCents: wallet.TotalSpentCents, Currency: input.Currency, CreatedAt: now}
	if err := createWalletTransaction(ctx, tx, walletTx); err != nil {
		return ManualTopUpResult{}, err
	}
	topup := ManualTopUp{ID: postgresID("mtu", now.Add(2*time.Nanosecond)), AccountID: input.AccountID, AmountCents: input.AmountCents, Currency: input.Currency, OperatorUserID: input.OperatorUserID, LedgerEntryID: entry.ID, Reason: input.Reason, CreatedAt: now}
	if err := tx.ManualTopup.Create().
		SetID(topup.ID).
		SetAccountID(topup.AccountID).
		SetAmountCents(topup.AmountCents).
		SetCurrency(topup.Currency).
		SetOperatorUserID(topup.OperatorUserID).
		SetLedgerEntryID(topup.LedgerEntryID).
		SetWalletTransactionID(walletTx.ID).
		SetIdempotencyKey(input.IdempotencyKey).
		SetRequestHash(requestHash).
		SetReason(topup.Reason).
		SetCreatedAt(topup.CreatedAt).
		Exec(ctx); err != nil {
		return ManualTopUpResult{}, err
	}
	result := ManualTopUpResult{TopUp: topup, LedgerEntry: entry, WalletTransaction: walletTx, Wallet: wallet}
	return result, tx.Commit()
}

func (s *PostgresStore) CreateHold(ctx context.Context, input HoldInput) (HoldResult, error) {
	unlock, err := s.lockAccount(ctx, input.AccountID)
	if err != nil {
		return HoldResult{}, err
	}
	defer unlock()
	if input.ResourceType == "" || input.ResourceID == "" || input.AmountCents <= 0 || input.ActivationAmountCents < 0 || input.ActivationAmountCents > input.AmountCents {
		return HoldResult{}, ErrInvalidHoldInput
	}
	requestHash, err := hashJSON(struct {
		AccountID             string `json:"accountId"`
		WorkspaceID           string `json:"workspaceId"`
		ResourceType          string `json:"resourceType"`
		ResourceID            string `json:"resourceId"`
		AmountCents           int64  `json:"amountCents"`
		ActivationAmountCents int64  `json:"activationAmountCents"`
		Currency              string `json:"currency"`
	}{input.AccountID, input.WorkspaceID, input.ResourceType, input.ResourceID, input.AmountCents, input.ActivationAmountCents, input.Currency})
	if err != nil {
		return HoldResult{}, err
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return HoldResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	if existing, existingHash, err := s.holdByIdempotencyKey(ctx, tx, input.IdempotencyKey); err == nil {
		if existingHash != requestHash {
			return HoldResult{}, ErrIdempotencyConflict
		}
		existing.Replayed = true
		return existing, tx.Commit()
	} else if !ledgerent.IsNotFound(err) {
		return HoldResult{}, err
	}

	now := s.now()
	wallet, err := s.ensureWallet(ctx, tx, input.AccountID, input.Currency, now)
	if err != nil {
		return HoldResult{}, err
	}
	if wallet.BalanceCents-wallet.FrozenCents < input.AmountCents {
		return HoldResult{}, ErrInsufficientBalance
	}
	wallet.FrozenCents += input.AmountCents
	wallet.Currency = input.Currency
	wallet.AvailableCents = wallet.BalanceCents - wallet.FrozenCents
	wallet.UpdatedAt = now
	if err := s.saveWallet(ctx, tx, wallet); err != nil {
		return HoldResult{}, err
	}

	entry := LedgerEntry{ID: postgresID("le", now), AccountID: input.AccountID, AmountCents: input.AmountCents, Currency: input.Currency, Direction: "hold", Source: input.ResourceType + "_hold", Reason: input.ResourceID, CreatedAt: now}
	if err := createLedgerEntry(ctx, tx, entry); err != nil {
		return HoldResult{}, err
	}
	walletTx := WalletTransaction{ID: postgresID("wtx", now.Add(time.Nanosecond)), AccountID: input.AccountID, LedgerEntryID: entry.ID, AmountCents: input.AmountCents, BalanceCents: wallet.BalanceCents, FrozenCents: wallet.FrozenCents, AvailableCents: wallet.AvailableCents, TotalSpentCents: wallet.TotalSpentCents, Currency: input.Currency, CreatedAt: now}
	if err := createWalletTransaction(ctx, tx, walletTx); err != nil {
		return HoldResult{}, err
	}
	result := HoldResult{ID: postgresID("hold", now.Add(2*time.Nanosecond)), AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: input.ResourceType, ResourceID: input.ResourceID, AmountCents: input.AmountCents, ActivationAmountCents: input.ActivationAmountCents, OriginalCents: input.AmountCents, RemainingCents: input.AmountCents, Currency: input.Currency, Status: "reserved", LedgerEntryID: entry.ID, WalletTransactionID: walletTx.ID, Wallet: wallet, CreatedAt: now}
	if err := tx.Hold.Create().
		SetID(result.ID).
		SetAccountID(result.AccountID).
		SetWorkspaceID(result.WorkspaceID).
		SetResourceType(result.ResourceType).
		SetResourceID(result.ResourceID).
		SetAmountCents(result.AmountCents).
		SetActivationAmountCents(result.ActivationAmountCents).
		SetOriginalCents(result.OriginalCents).
		SetRemainingCents(result.RemainingCents).
		SetConsumedCents(0).
		SetReleasedCents(0).
		SetCurrency(result.Currency).
		SetStatus(result.Status).
		SetLedgerEntryID(result.LedgerEntryID).
		SetWalletTransactionID(result.WalletTransactionID).
		SetIdempotencyKey(input.IdempotencyKey).
		SetRequestHash(requestHash).
		SetCreatedAt(result.CreatedAt).
		Exec(ctx); err != nil {
		return HoldResult{}, err
	}
	return result, tx.Commit()
}

func (s *PostgresStore) ActivateHold(ctx context.Context, input HoldActivationInput) (HoldActivationResult, error) {
	unlock, err := s.lockAccount(ctx, input.AccountID)
	if err != nil {
		return HoldActivationResult{}, err
	}
	defer unlock()
	requestHash, err := hashJSON(input)
	if err != nil {
		return HoldActivationResult{}, err
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return HoldActivationResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if existing, existingHash, err := s.holdActivationByIdempotencyKey(ctx, tx, input.IdempotencyKey); err == nil {
		if existingHash != requestHash {
			return HoldActivationResult{}, ErrIdempotencyConflict
		}
		existing.Replayed = true
		return existing, tx.Commit()
	} else if !ledgerent.IsNotFound(err) {
		return HoldActivationResult{}, err
	}
	row, err := tx.Hold.Get(ctx, input.HoldID)
	if ledgerent.IsNotFound(err) {
		return HoldActivationResult{}, ErrHoldNotFound
	}
	if err != nil {
		return HoldActivationResult{}, err
	}
	if !holdEntMatches(row, input.AccountID, input.WorkspaceID, input.ResourceType, input.ResourceID, input.Currency) {
		return HoldActivationResult{}, ErrHoldIdentityMismatch
	}
	if row.Status != "reserved" || row.ActivationAmountCents <= 0 || row.RemainingCents < row.ActivationAmountCents {
		return HoldActivationResult{}, ErrInvalidHoldState
	}
	wallet, err := s.walletByAccount(ctx, tx, input.AccountID)
	if err != nil {
		return HoldActivationResult{}, err
	}
	amount := row.ActivationAmountCents
	if wallet.BalanceCents < amount || wallet.FrozenCents < amount {
		return HoldActivationResult{}, ErrInsufficientBalance
	}
	now := s.now()
	wallet.BalanceCents -= amount
	wallet.FrozenCents -= amount
	wallet.TotalSpentCents += amount
	wallet.AvailableCents = wallet.BalanceCents - wallet.FrozenCents
	wallet.UpdatedAt = now
	if err := s.saveWallet(ctx, tx, wallet); err != nil {
		return HoldActivationResult{}, err
	}
	remaining := row.RemainingCents - amount
	consumed := row.ConsumedCents + amount
	if err := tx.Hold.UpdateOneID(row.ID).SetRemainingCents(remaining).SetConsumedCents(consumed).SetProviderEvidenceRef(input.ProviderEvidenceRef).SetStatus("active").Exec(ctx); err != nil {
		return HoldActivationResult{}, err
	}
	entry := LedgerEntry{ID: postgresID("le", now), AccountID: input.AccountID, AmountCents: amount, Currency: input.Currency, Direction: "debit", Source: input.ResourceType + "_activation", Reason: input.ResourceID, CreatedAt: now}
	if err := createLedgerEntry(ctx, tx, entry); err != nil {
		return HoldActivationResult{}, err
	}
	walletTx := WalletTransaction{ID: postgresID("wtx", now.Add(time.Nanosecond)), AccountID: input.AccountID, LedgerEntryID: entry.ID, AmountCents: -amount, BalanceCents: wallet.BalanceCents, FrozenCents: wallet.FrozenCents, AvailableCents: wallet.AvailableCents, TotalSpentCents: wallet.TotalSpentCents, Currency: input.Currency, CreatedAt: now}
	if err := createWalletTransaction(ctx, tx, walletTx); err != nil {
		return HoldActivationResult{}, err
	}
	id := postgresID("hact", now.Add(2*time.Nanosecond))
	if err := tx.HoldActivation.Create().SetID(id).SetAccountID(input.AccountID).SetWorkspaceID(input.WorkspaceID).SetResourceType(input.ResourceType).SetResourceID(input.ResourceID).SetHoldID(input.HoldID).SetAmountCents(amount).SetCurrency(input.Currency).SetStatus("activated").SetProviderEvidenceRef(input.ProviderEvidenceRef).SetLedgerEntryID(entry.ID).SetWalletTransactionID(walletTx.ID).SetIdempotencyKey(input.IdempotencyKey).SetRequestHash(requestHash).SetCreatedAt(now).Exec(ctx); err != nil {
		return HoldActivationResult{}, err
	}
	holdResult := holdResultFromEnt(row, wallet)
	holdResult.RemainingCents = remaining
	holdResult.ConsumedCents = consumed
	holdResult.ProviderEvidenceRef = input.ProviderEvidenceRef
	holdResult.Status = "active"
	result := HoldActivationResult{HoldResult: holdResult, ActivationLedgerEntryID: entry.ID, ActivationWalletTransactionID: walletTx.ID}
	return result, tx.Commit()
}

func (s *PostgresStore) ReleaseHold(ctx context.Context, input HoldReleaseInput) (HoldReleaseResult, error) {
	unlock, err := s.lockAccount(ctx, input.AccountID)
	if err != nil {
		return HoldReleaseResult{}, err
	}
	defer unlock()
	requestHash, err := hashHoldRelease(input)
	if err != nil {
		return HoldReleaseResult{}, err
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return HoldReleaseResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	if existing, existingHash, err := s.holdReleaseByIdempotencyKey(ctx, tx, input.IdempotencyKey); err == nil {
		if existingHash != requestHash {
			return HoldReleaseResult{}, ErrIdempotencyConflict
		}
		existing.Replayed = true
		return existing, tx.Commit()
	} else if !ledgerent.IsNotFound(err) {
		return HoldReleaseResult{}, err
	}

	row, err := tx.Hold.Get(ctx, input.HoldID)
	if ledgerent.IsNotFound(err) {
		return HoldReleaseResult{}, ErrHoldNotFound
	}
	if err != nil {
		return HoldReleaseResult{}, err
	}
	if !holdEntMatches(row, input.AccountID, input.WorkspaceID, input.ResourceType, input.ResourceID, input.Currency) {
		return HoldReleaseResult{}, ErrHoldIdentityMismatch
	}
	if row.Status == "released" || row.RemainingCents <= 0 {
		return HoldReleaseResult{}, ErrInvalidHoldState
	}
	amount := row.RemainingCents
	now := s.now()
	wallet, err := s.walletByAccount(ctx, tx, input.AccountID)
	if ledgerent.IsNotFound(err) {
		return HoldReleaseResult{}, ErrInsufficientFrozen
	}
	if err != nil {
		return HoldReleaseResult{}, err
	}
	if wallet.FrozenCents < amount {
		return HoldReleaseResult{}, ErrInsufficientFrozen
	}
	wallet.FrozenCents -= amount
	wallet.Currency = input.Currency
	wallet.AvailableCents = wallet.BalanceCents - wallet.FrozenCents
	wallet.UpdatedAt = now
	if err := s.saveWallet(ctx, tx, wallet); err != nil {
		return HoldReleaseResult{}, err
	}

	entry := LedgerEntry{ID: postgresID("le", now), AccountID: input.AccountID, AmountCents: amount, Currency: input.Currency, Direction: "release", Source: input.ResourceType + "_hold_released", Reason: input.Reason, CreatedAt: now}
	if err := createLedgerEntry(ctx, tx, entry); err != nil {
		return HoldReleaseResult{}, err
	}
	walletTx := WalletTransaction{ID: postgresID("wtx", now.Add(time.Nanosecond)), AccountID: input.AccountID, LedgerEntryID: entry.ID, AmountCents: 0, BalanceCents: wallet.BalanceCents, FrozenCents: wallet.FrozenCents, AvailableCents: wallet.AvailableCents, TotalSpentCents: wallet.TotalSpentCents, Currency: input.Currency, CreatedAt: now}
	if err := createWalletTransaction(ctx, tx, walletTx); err != nil {
		return HoldReleaseResult{}, err
	}
	result := HoldReleaseResult{ID: postgresID("hrel", now.Add(2*time.Nanosecond)), AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: input.ResourceType, ResourceID: input.ResourceID, HoldID: input.HoldID, AmountCents: amount, Currency: input.Currency, Status: "released", LedgerEntryID: entry.ID, WalletTransactionID: walletTx.ID, Wallet: wallet, CreatedAt: now}
	if err := tx.HoldRelease.Create().
		SetID(result.ID).
		SetAccountID(result.AccountID).
		SetWorkspaceID(result.WorkspaceID).
		SetResourceType(result.ResourceType).
		SetResourceID(result.ResourceID).
		SetHoldID(result.HoldID).
		SetAmountCents(result.AmountCents).
		SetCurrency(result.Currency).
		SetStatus(result.Status).
		SetLedgerEntryID(result.LedgerEntryID).
		SetWalletTransactionID(result.WalletTransactionID).
		SetIdempotencyKey(input.IdempotencyKey).
		SetRequestHash(requestHash).
		SetCreatedAt(result.CreatedAt).
		Exec(ctx); err != nil {
		return HoldReleaseResult{}, err
	}
	if err := tx.Hold.UpdateOneID(row.ID).SetRemainingCents(0).SetReleasedCents(row.ReleasedCents + amount).SetStatus("released").Exec(ctx); err != nil {
		return HoldReleaseResult{}, err
	}
	return result, tx.Commit()
}

func (s *PostgresStore) RecordReceipt(ctx context.Context, input ReceiptInput) (Receipt, error) {
	if err := validateReceiptInput(input); err != nil {
		return Receipt{}, err
	}
	hashInput := input
	hashInput.IdempotencyKey = ""
	requestHash, err := hashJSON(hashInput)
	if err != nil {
		return Receipt{}, err
	}
	if existing, existingHash, err := s.receiptByIdempotencyKey(ctx, input.IdempotencyKey); err == nil {
		if existingHash != requestHash {
			return Receipt{}, ErrIdempotencyConflict
		}
		existing.Replayed = true
		return existing, nil
	} else if !ledgerent.IsNotFound(err) {
		return Receipt{}, err
	}
	now := s.now()
	receipt := Receipt{ReceiptInput: hashInput, ReceiptID: postgresID("receipt", now), CreatedAt: now}
	finalizeReceiptContinuation(&receipt)
	payload, err := json.Marshal(receiptPayload{ReceiptInput: receipt.ReceiptInput, Retention: receipt.Retention})
	if err != nil {
		return Receipt{}, err
	}
	if err := s.client.EvidenceReceipt.Create().
		SetID(receipt.ReceiptID).
		SetReceiptType(receipt.Type).
		SetStatus(receipt.Status).
		SetOrganizationID(receipt.OrganizationID).
		SetWorkspaceID(receipt.WorkspaceID).
		SetProjectID(receipt.ProjectID).
		SetTaskID(receipt.TaskID).
		SetJobID(receipt.JobID).
		SetPayloadJSON(string(payload)).
		SetSupersedesReceiptID(receipt.SupersedesReceiptID).
		SetIdempotencyKey(input.IdempotencyKey).
		SetRequestHash(requestHash).
		SetCreatedAt(receipt.CreatedAt).
		Exec(ctx); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

func (s *PostgresStore) ListReceipts(ctx context.Context, query ReceiptQuery) (ReceiptPage, error) {
	query, cursor, err := normalizeReceiptQuery(query)
	if err != nil {
		return ReceiptPage{}, err
	}
	q := s.client.EvidenceReceipt.Query()
	if query.OrganizationID != "" {
		q = q.Where(evidencereceipt.OrganizationID(query.OrganizationID))
	}
	if query.WorkspaceID != "" {
		q = q.Where(evidencereceipt.WorkspaceID(query.WorkspaceID))
	}
	if query.ProjectID != "" {
		q = q.Where(evidencereceipt.ProjectID(query.ProjectID))
	}
	if query.TaskID != "" {
		q = q.Where(evidencereceipt.TaskID(query.TaskID))
	}
	if query.JobID != "" {
		q = q.Where(evidencereceipt.JobID(query.JobID))
	}
	if query.Type != "" {
		q = q.Where(evidencereceipt.ReceiptType(query.Type))
	}
	if query.Status != "" {
		q = q.Where(evidencereceipt.Status(query.Status))
	}
	if !cursor.CreatedAt.IsZero() {
		q = q.Where(evidencereceipt.Or(
			evidencereceipt.CreatedAtLT(cursor.CreatedAt),
			evidencereceipt.And(evidencereceipt.CreatedAtEQ(cursor.CreatedAt), evidencereceipt.IDLT(cursor.ReceiptID)),
		))
	}
	rows, err := q.Order(ledgerent.Desc(evidencereceipt.FieldCreatedAt, evidencereceipt.FieldID)).Limit(query.Limit + 1).All(ctx)
	if err != nil {
		return ReceiptPage{}, err
	}
	hasMore := len(rows) > query.Limit
	if hasMore {
		rows = rows[:query.Limit]
	}
	receipts := make([]Receipt, 0, len(rows))
	for _, row := range rows {
		receipts = append(receipts, s.receiptForRead(ctx, receiptFromEnt(row)))
	}
	page := ReceiptPage{Receipts: receipts, HasMore: hasMore}
	if hasMore {
		page.NextCursor = encodeReceiptCursor(receipts[len(receipts)-1])
	}
	return page, nil
}

func (s *PostgresStore) Receipt(ctx context.Context, receiptID string) (Receipt, error) {
	receipt, err := s.receipt(ctx, receiptID)
	if err != nil {
		return Receipt{}, err
	}
	return s.receiptForRead(ctx, receipt), nil
}

func (s *PostgresStore) UpdateReceiptRetention(ctx context.Context, input ReceiptRetentionInput) (ReceiptRetentionResult, error) {
	if input.ReceiptID == "" || input.IdempotencyKey == "" || (input.RetainUntil.IsZero() && !input.LegalHold) {
		return ReceiptRetentionResult{}, ErrInvalidReceiptRetentionInput
	}
	requestHash, err := hashJSON(struct {
		ReceiptID   string    `json:"receiptId"`
		RetainUntil time.Time `json:"retainUntil"`
		LegalHold   bool      `json:"legalHold"`
	}{input.ReceiptID, input.RetainUntil, input.LegalHold})
	if err != nil {
		return ReceiptRetentionResult{}, err
	}
	return s.mutateReceipt(ctx, "receipt-retention", input.IdempotencyKey, requestHash, input.ReceiptID, func(receipt *Receipt) error {
		if !input.RetainUntil.IsZero() && !receipt.Retention.RetainUntil.IsZero() && input.RetainUntil.Before(receipt.Retention.RetainUntil) {
			return ErrReceiptRetentionShortening
		}
		if input.RetainUntil.After(receipt.Retention.RetainUntil) {
			receipt.Retention.RetainUntil = input.RetainUntil
		}
		receipt.Retention.LegalHold = receipt.Retention.LegalHold || input.LegalHold
		return nil
	})
}

func (s *PostgresStore) PrivacyDeleteReceipt(ctx context.Context, input ReceiptPrivacyDeleteInput) (ReceiptRetentionResult, error) {
	if input.ReceiptID == "" || input.Reason == "" || input.IdempotencyKey == "" {
		return ReceiptRetentionResult{}, ErrInvalidReceiptRetentionInput
	}
	requestHash, err := hashJSON(struct {
		ReceiptID string `json:"receiptId"`
		Reason    string `json:"reason"`
	}{input.ReceiptID, input.Reason})
	if err != nil {
		return ReceiptRetentionResult{}, err
	}
	return s.mutateReceipt(ctx, "receipt-privacy", input.IdempotencyKey, requestHash, input.ReceiptID, func(receipt *Receipt) error {
		if receipt.Retention.LegalHold {
			return ErrReceiptLegalHold
		}
		if receipt.Retention.RetainUntil.After(s.now()) {
			return ErrReceiptRetentionActive
		}
		if receipt.Retention.PrivacyRedaction == nil {
			receipt.Actor = nil
			receipt.Owner = nil
			receipt.Continuation = nil
			receipt.Retention.PrivacyRedaction = &PrivacyRedactionEvidence{AppliedAt: s.now(), Reason: input.Reason, Eligible: true}
		}
		return nil
	})
}

func (s *PostgresStore) mutateReceipt(ctx context.Context, service, idempotencyKey, requestHash, receiptID string, mutate func(*Receipt) error) (ReceiptRetentionResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReceiptRetentionResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	lockKey := service + ":" + idempotencyKey
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", lockKey); err != nil {
		return ReceiptRetentionResult{}, err
	}
	idempotencyID := evidenceID("idempotency", lockKey)
	var existingHash, responseJSON string
	err = tx.QueryRowContext(ctx, "SELECT request_hash, response_ref FROM idempotency_keys WHERE id = $1", idempotencyID).Scan(&existingHash, &responseJSON)
	if err == nil {
		if existingHash != requestHash {
			return ReceiptRetentionResult{}, ErrIdempotencyConflict
		}
		var result ReceiptRetentionResult
		if err := json.Unmarshal([]byte(responseJSON), &result); err != nil {
			return ReceiptRetentionResult{}, err
		}
		result.Replayed = true
		return result, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ReceiptRetentionResult{}, err
	}
	var payloadJSON string
	var createdAt time.Time
	if err := tx.QueryRowContext(ctx, "SELECT payload_json, created_at FROM evidence_receipts WHERE id = $1 FOR UPDATE /* ledger_receipt_mutation */", receiptID).Scan(&payloadJSON, &createdAt); errors.Is(err, sql.ErrNoRows) {
		return ReceiptRetentionResult{}, ErrReceiptNotFound
	} else if err != nil {
		return ReceiptRetentionResult{}, err
	}
	var stored receiptPayload
	if err := json.Unmarshal([]byte(payloadJSON), &stored); err != nil {
		return ReceiptRetentionResult{}, err
	}
	receipt := Receipt{ReceiptInput: stored.ReceiptInput, ReceiptID: receiptID, CreatedAt: createdAt, Retention: stored.Retention}
	if err := mutate(&receipt); err != nil {
		return ReceiptRetentionResult{}, err
	}
	payload, err := json.Marshal(receiptPayload{ReceiptInput: receipt.ReceiptInput, Retention: receipt.Retention})
	if err != nil {
		return ReceiptRetentionResult{}, err
	}
	result := ReceiptRetentionResult{ReceiptID: receipt.ReceiptID, Retention: receipt.Retention}
	response, err := json.Marshal(result)
	if err != nil {
		return ReceiptRetentionResult{}, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE evidence_receipts SET payload_json = $1 WHERE id = $2", string(payload), receiptID); err != nil {
		return ReceiptRetentionResult{}, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO idempotency_keys (id, service, idempotency_key, request_hash, response_ref, created_at) VALUES ($1, $2, $3, $4, $5, $6)", idempotencyID, service, idempotencyKey, requestHash, string(response), s.now()); err != nil {
		return ReceiptRetentionResult{}, err
	}
	return result, tx.Commit()
}

func (s *PostgresStore) receipt(ctx context.Context, receiptID string) (Receipt, error) {
	row, err := s.client.EvidenceReceipt.Get(ctx, receiptID)
	if ledgerent.IsNotFound(err) {
		return Receipt{}, ErrReceiptNotFound
	}
	if err != nil {
		return Receipt{}, err
	}
	return receiptFromEnt(row), nil
}

func (s *PostgresStore) Continuation(ctx context.Context, receiptID string) (map[string]any, error) {
	receipt, err := s.receipt(ctx, receiptID)
	if err != nil {
		return nil, err
	}
	continuation, err := continuationFromReceipt(receipt)
	if err != nil {
		return nil, err
	}
	identity := executionIdentityFromReceipt(receipt)
	if !validExecutionIdentity(identity) {
		return nil, ErrContinuationIneligible
	}
	gate, err := s.EvaluateReviewGate(ctx, ReviewGateInput{ExecutionIdentity: identity, ReviewIDs: stringSlice(continuation["reviewIds"])})
	if errors.Is(err, ErrReviewPolicyNotFound) {
		return nil, ErrContinuationIneligible
	}
	if err != nil {
		return nil, err
	}
	if !gate.ContinuationEligible {
		return nil, ErrContinuationIneligible
	}
	return continuation, nil
}

func (s *PostgresStore) receiptForRead(ctx context.Context, receipt Receipt) Receipt {
	gate, err := s.EvaluateReviewGate(ctx, ReviewGateInput{ExecutionIdentity: executionIdentityFromReceipt(receipt), ReviewIDs: stringSlice(receipt.Continuation["reviewIds"])})
	return receiptForRead(receipt, gate, err)
}

func (s *PostgresStore) RecordArtifact(ctx context.Context, input ArtifactInput) (Artifact, error) {
	if err := validateArtifactInput(input); err != nil {
		return Artifact{}, err
	}
	receipt, err := s.RecordReceipt(ctx, ReceiptInput{
		Type: artifactReceiptType, Status: "completed", Surface: "ledger",
		OrganizationID: input.OrganizationID, WorkspaceID: input.WorkspaceID, ProjectID: input.ProjectID, TaskID: input.TaskID, JobID: input.JobID,
		ArtifactID:     evidenceID("artifact", input.IdempotencyKey),
		OutputRefs:     map[string]any{"digest": input.Digest, "mediaType": input.MediaType, "sizeBytes": input.SizeBytes, "storageRef": input.StorageRef},
		IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return Artifact{}, err
	}
	return artifactFromReceipt(receipt), nil
}

func (s *PostgresStore) Artifact(ctx context.Context, artifactID string) (Artifact, error) {
	rows, err := s.client.EvidenceReceipt.Query().Where(evidencereceipt.ReceiptType(artifactReceiptType)).All(ctx)
	if err != nil {
		return Artifact{}, err
	}
	// ponytail: linear scan is enough for initial evidence volume; add indexed promoted columns when measured query load requires it.
	for _, row := range rows {
		receipt := receiptFromEnt(row)
		if receipt.ArtifactID == artifactID {
			return artifactFromReceipt(receipt), nil
		}
	}
	return Artifact{}, ErrArtifactNotFound
}

func (s *PostgresStore) RecordReview(ctx context.Context, input ReviewInput) (Review, error) {
	if err := validateReviewInput(input); err != nil {
		return Review{}, err
	}
	status := reviewReceiptStatus(input.Decision)
	receipt, err := s.RecordReceipt(ctx, ReceiptInput{
		Type: reviewReceiptType, Status: status, Surface: "ledger",
		OrganizationID: input.OrganizationID, WorkspaceID: input.WorkspaceID, ProjectID: input.ProjectID, TaskID: input.TaskID, JobID: input.JobID,
		ReviewID:       evidenceID("review", input.IdempotencyKey),
		ReviewerChecks: map[string]any{"reviewerRef": input.ReviewerRef, "reviewerVersion": input.ReviewerVersion, "inputArtifactDigests": input.InputArtifactDigests, "checks": input.Checks, "decision": input.Decision},
		IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return Review{}, err
	}
	return reviewFromReceipt(receipt), nil
}

func (s *PostgresStore) Review(ctx context.Context, reviewID string) (Review, error) {
	rows, err := s.client.EvidenceReceipt.Query().Where(evidencereceipt.ReceiptType(reviewReceiptType)).All(ctx)
	if err != nil {
		return Review{}, err
	}
	// ponytail: linear scan is enough for initial evidence volume; add indexed promoted columns when measured query load requires it.
	for _, row := range rows {
		receipt := receiptFromEnt(row)
		if receipt.ReviewID == reviewID {
			return reviewFromReceipt(receipt), nil
		}
	}
	return Review{}, ErrReviewNotFound
}

func (s *PostgresStore) CreateReviewPolicy(ctx context.Context, input ReviewPolicyInput) (ReviewPolicy, error) {
	if err := validateReviewPolicyInput(input); err != nil {
		return ReviewPolicy{}, err
	}
	hashInput := input
	hashInput.IdempotencyKey = ""
	requestHash, err := hashJSON(hashInput)
	if err != nil {
		return ReviewPolicy{}, err
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return ReviewPolicy{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if row, err := tx.ReviewPolicy.Query().Where(reviewpolicy.IdempotencyKey(input.IdempotencyKey)).Only(ctx); err == nil {
		if row.RequestHash != requestHash {
			return ReviewPolicy{}, ErrIdempotencyConflict
		}
		policy, err := reviewPolicyFromEnt(row)
		if err != nil {
			return ReviewPolicy{}, err
		}
		policy.Replayed = true
		return policy, tx.Commit()
	} else if !ledgerent.IsNotFound(err) {
		return ReviewPolicy{}, err
	}

	scope := reviewPolicyScopePredicates(input.ExecutionIdentity)
	if input.SupersedesPolicyID == "" {
		exists, err := tx.ReviewPolicy.Query().Where(append(scope, reviewpolicy.StatusEQ("active"))...).Exist(ctx)
		if err != nil {
			return ReviewPolicy{}, err
		}
		if exists {
			_ = tx.Rollback()
			return s.replayReviewPolicy(ctx, input.IdempotencyKey, requestHash)
		}
	} else {
		previous, err := tx.ReviewPolicy.Get(ctx, input.SupersedesPolicyID)
		if ledgerent.IsNotFound(err) {
			return ReviewPolicy{}, ErrReviewPolicyNotFound
		}
		if err != nil {
			return ReviewPolicy{}, err
		}
		previousPolicy, err := reviewPolicyFromEnt(previous)
		if err != nil {
			return ReviewPolicy{}, err
		}
		if previousPolicy.Status != "active" || !sameExecutionIdentity(previousPolicy.ExecutionIdentity, input.ExecutionIdentity) || previousPolicy.Version == input.Version {
			_ = tx.Rollback()
			return s.replayReviewPolicy(ctx, input.IdempotencyKey, requestHash)
		}
		if err := tx.ReviewPolicy.UpdateOneID(previous.ID).SetStatus("superseded").Exec(ctx); err != nil {
			return ReviewPolicy{}, err
		}
	}
	requiredJSON, err := json.Marshal(input.RequiredReviewers)
	if err != nil {
		return ReviewPolicy{}, err
	}
	now := s.now()
	policyID := evidenceID("review-policy", input.IdempotencyKey)
	row, err := tx.ReviewPolicy.Create().
		SetID(policyID).
		SetOrganizationID(input.OrganizationID).
		SetWorkspaceID(input.WorkspaceID).
		SetProjectID(input.ProjectID).
		SetTaskID(input.TaskID).
		SetJobID(input.JobID).
		SetVersion(input.Version).
		SetRequiredReviewersJSON(string(requiredJSON)).
		SetStatus("active").
		SetSupersedesPolicyID(input.SupersedesPolicyID).
		SetIdempotencyKey(input.IdempotencyKey).
		SetRequestHash(requestHash).
		SetCreatedAt(now).
		Save(ctx)
	if err != nil {
		if ledgerent.IsConstraintError(err) {
			_ = tx.Rollback()
			return s.replayReviewPolicy(ctx, input.IdempotencyKey, requestHash)
		}
		return ReviewPolicy{}, err
	}
	policy, err := reviewPolicyFromEnt(row)
	if err != nil {
		return ReviewPolicy{}, err
	}
	return policy, tx.Commit()
}

func (s *PostgresStore) replayReviewPolicy(ctx context.Context, idempotencyKey, requestHash string) (ReviewPolicy, error) {
	row, err := s.client.ReviewPolicy.Query().Where(reviewpolicy.IdempotencyKey(idempotencyKey)).Only(ctx)
	if ledgerent.IsNotFound(err) {
		return ReviewPolicy{}, ErrInvalidReviewPolicyInput
	}
	if err != nil {
		return ReviewPolicy{}, err
	}
	if row.RequestHash != requestHash {
		return ReviewPolicy{}, ErrIdempotencyConflict
	}
	policy, err := reviewPolicyFromEnt(row)
	if err != nil {
		return ReviewPolicy{}, err
	}
	policy.Replayed = true
	return policy, nil
}

func (s *PostgresStore) ReviewPolicy(ctx context.Context, policyID string) (ReviewPolicy, error) {
	row, err := s.client.ReviewPolicy.Get(ctx, policyID)
	if ledgerent.IsNotFound(err) {
		return ReviewPolicy{}, ErrReviewPolicyNotFound
	}
	if err != nil {
		return ReviewPolicy{}, err
	}
	return reviewPolicyFromEnt(row)
}

func (s *PostgresStore) ListReviewPolicies(ctx context.Context, query ReviewPolicyQuery) ([]ReviewPolicy, error) {
	q := s.client.ReviewPolicy.Query()
	if query.OrganizationID != "" {
		q = q.Where(reviewpolicy.OrganizationID(query.OrganizationID))
	}
	if query.WorkspaceID != "" {
		q = q.Where(reviewpolicy.WorkspaceID(query.WorkspaceID))
	}
	if query.ProjectID != "" {
		q = q.Where(reviewpolicy.ProjectID(query.ProjectID))
	}
	if query.TaskID != "" {
		q = q.Where(reviewpolicy.TaskID(query.TaskID))
	}
	if query.JobID != "" {
		q = q.Where(reviewpolicy.JobID(query.JobID))
	}
	if query.Status != "" {
		q = q.Where(reviewpolicy.Status(query.Status))
	}
	rows, err := q.Order(ledgerent.Desc(reviewpolicy.FieldCreatedAt, reviewpolicy.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	policies := make([]ReviewPolicy, 0, len(rows))
	for _, row := range rows {
		policy, err := reviewPolicyFromEnt(row)
		if err != nil {
			return nil, err
		}
		policies = append(policies, policy)
	}
	return policies, nil
}

func (s *PostgresStore) EvaluateReviewGate(ctx context.Context, input ReviewGateInput) (ReviewGateResult, error) {
	if !validExecutionIdentity(input.ExecutionIdentity) {
		return ReviewGateResult{}, ErrInvalidReviewGateInput
	}
	row, err := s.client.ReviewPolicy.Query().Where(append(reviewPolicyScopePredicates(input.ExecutionIdentity), reviewpolicy.StatusEQ("active"))...).Only(ctx)
	if ledgerent.IsNotFound(err) {
		return ReviewGateResult{}, ErrReviewPolicyNotFound
	}
	if err != nil {
		return ReviewGateResult{}, err
	}
	policy, err := reviewPolicyFromEnt(row)
	if err != nil {
		return ReviewGateResult{}, err
	}
	wanted := make(map[string]struct{}, len(input.ReviewIDs))
	for _, id := range input.ReviewIDs {
		wanted[id] = struct{}{}
	}
	reviewRows, err := s.client.EvidenceReceipt.Query().Where(evidencereceipt.ReceiptType(reviewReceiptType)).All(ctx)
	if err != nil {
		return ReviewGateResult{}, err
	}
	reviews := make([]Review, 0, len(wanted))
	for _, reviewRow := range reviewRows {
		receipt := receiptFromEnt(reviewRow)
		if _, ok := wanted[receipt.ReviewID]; ok {
			reviews = append(reviews, reviewFromReceipt(receipt))
		}
	}
	return evaluateReviewGate(policy, reviews), nil
}

func reviewPolicyFromEnt(row *ledgerent.ReviewPolicy) (ReviewPolicy, error) {
	var required []RequiredReviewer
	if err := json.Unmarshal([]byte(row.RequiredReviewersJSON), &required); err != nil {
		return ReviewPolicy{}, err
	}
	return ReviewPolicy{
		ReviewPolicyInput: ReviewPolicyInput{
			ExecutionIdentity: ExecutionIdentity{OrganizationID: row.OrganizationID, WorkspaceID: row.WorkspaceID, ProjectID: row.ProjectID, TaskID: row.TaskID, JobID: row.JobID},
			Version:           row.Version, RequiredReviewers: required, SupersedesPolicyID: row.SupersedesPolicyID,
		},
		PolicyID: row.ID, Status: row.Status, CreatedAt: row.CreatedAt,
	}, nil
}

func reviewPolicyScopePredicates(identity ExecutionIdentity) []predicate.ReviewPolicy {
	return []predicate.ReviewPolicy{
		reviewpolicy.OrganizationID(identity.OrganizationID),
		reviewpolicy.WorkspaceID(identity.WorkspaceID),
		reviewpolicy.ProjectID(identity.ProjectID),
		reviewpolicy.TaskID(identity.TaskID),
		reviewpolicy.JobID(identity.JobID),
	}
}

func (s *PostgresStore) SettleResource(ctx context.Context, input ResourceSettlementInput) (ResourceSettlementResult, error) {
	unlock, err := s.lockAccount(ctx, input.AccountID)
	if err != nil {
		return ResourceSettlementResult{}, err
	}
	defer unlock()
	requestHash, err := hashJSON(struct {
		AccountID               string         `json:"accountId"`
		WorkspaceID             string         `json:"workspaceId"`
		ResourceType            string         `json:"resourceType"`
		ResourceID              string         `json:"resourceId"`
		HoldID                  string         `json:"holdId"`
		AmountCents             int64          `json:"amountCents"`
		Currency                string         `json:"currency"`
		PricingVersion          string         `json:"pricingVersion"`
		PriceSnapshot           map[string]any `json:"priceSnapshot"`
		UsagePeriodStart        string         `json:"usagePeriodStart"`
		UsagePeriodEnd          string         `json:"usagePeriodEnd"`
		Quantity                float64        `json:"quantity"`
		Unit                    string         `json:"unit"`
		ProviderCostEvidenceRef string         `json:"providerCostEvidenceRef"`
	}{input.AccountID, input.WorkspaceID, input.ResourceType, input.ResourceID, input.HoldID, input.AmountCents, input.Currency, input.PricingVersion, input.PriceSnapshot, input.UsagePeriodStart, input.UsagePeriodEnd, input.Quantity, input.Unit, input.ProviderCostEvidenceRef})
	if err != nil {
		return ResourceSettlementResult{}, err
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return ResourceSettlementResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	if existing, existingHash, err := s.settlementByIdempotencyKey(ctx, tx, input.IdempotencyKey); err == nil {
		if existingHash != requestHash {
			return ResourceSettlementResult{}, ErrIdempotencyConflict
		}
		existing.Replayed = true
		return existing, tx.Commit()
	} else if !ledgerent.IsNotFound(err) {
		return ResourceSettlementResult{}, err
	}

	row, err := tx.Hold.Get(ctx, input.HoldID)
	if ledgerent.IsNotFound(err) {
		return ResourceSettlementResult{}, ErrHoldNotFound
	}
	if err != nil {
		return ResourceSettlementResult{}, err
	}
	if !holdEntMatches(row, input.AccountID, input.WorkspaceID, input.ResourceType, input.ResourceID, input.Currency) {
		return ResourceSettlementResult{}, ErrHoldIdentityMismatch
	}
	if row.Status != "active" && row.Status != "exhausted" {
		return ResourceSettlementResult{}, ErrInvalidHoldState
	}
	now := s.now()
	wallet, err := s.walletByAccount(ctx, tx, input.AccountID)
	if ledgerent.IsNotFound(err) {
		return ResourceSettlementResult{}, ErrInsufficientBalance
	}
	if err != nil {
		return ResourceSettlementResult{}, err
	}
	availablePart := minInt64(input.AmountCents, wallet.AvailableCents)
	holdPart := input.AmountCents - availablePart
	if holdPart > row.RemainingCents {
		return ResourceSettlementResult{}, ErrInsufficientResourceHold
	}
	wallet.BalanceCents -= input.AmountCents
	wallet.FrozenCents -= holdPart
	wallet.TotalSpentCents += input.AmountCents
	wallet.Currency = input.Currency
	wallet.AvailableCents = wallet.BalanceCents - wallet.FrozenCents
	wallet.UpdatedAt = now
	if err := s.saveWallet(ctx, tx, wallet); err != nil {
		return ResourceSettlementResult{}, err
	}

	entry := LedgerEntry{ID: postgresID("le", now), AccountID: input.AccountID, AmountCents: input.AmountCents, Currency: input.Currency, Direction: "debit", Source: input.ResourceType + "_settlement", Reason: input.WorkspaceID, CreatedAt: now}
	if err := createLedgerEntry(ctx, tx, entry); err != nil {
		return ResourceSettlementResult{}, err
	}
	walletTx := WalletTransaction{ID: postgresID("wtx", now.Add(time.Nanosecond)), AccountID: input.AccountID, LedgerEntryID: entry.ID, AmountCents: -input.AmountCents, BalanceCents: wallet.BalanceCents, FrozenCents: wallet.FrozenCents, AvailableCents: wallet.AvailableCents, TotalSpentCents: wallet.TotalSpentCents, Currency: input.Currency, CreatedAt: now}
	if err := createWalletTransaction(ctx, tx, walletTx); err != nil {
		return ResourceSettlementResult{}, err
	}
	remaining := row.RemainingCents - holdPart
	status := row.Status
	if remaining == 0 {
		status = "exhausted"
	}
	if err := tx.Hold.UpdateOneID(row.ID).SetRemainingCents(remaining).SetConsumedCents(row.ConsumedCents + holdPart).SetStatus(status).Exec(ctx); err != nil {
		return ResourceSettlementResult{}, err
	}
	result := ResourceSettlementResult{ID: postgresID("settle", now.Add(2*time.Nanosecond)), AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: input.ResourceType, ResourceID: input.ResourceID, HoldID: input.HoldID, HoldRemainingCents: remaining, AmountCents: input.AmountCents, Currency: input.Currency, Status: "settled", LedgerEntryID: entry.ID, WalletTransactionID: walletTx.ID, PricingVersion: input.PricingVersion, PriceSnapshot: cloneAnyMap(input.PriceSnapshot), UsagePeriodStart: input.UsagePeriodStart, UsagePeriodEnd: input.UsagePeriodEnd, Quantity: input.Quantity, Unit: input.Unit, ProviderCostEvidenceRef: input.ProviderCostEvidenceRef, Wallet: wallet, CreatedAt: now}
	priceSnapshotJSON, err := json.Marshal(result.PriceSnapshot)
	if err != nil {
		return ResourceSettlementResult{}, err
	}
	if err := tx.ResourceSettlement.Create().
		SetID(result.ID).
		SetAccountID(result.AccountID).
		SetWorkspaceID(result.WorkspaceID).
		SetResourceType(result.ResourceType).
		SetResourceID(result.ResourceID).
		SetHoldID(result.HoldID).
		SetAmountCents(result.AmountCents).
		SetCurrency(result.Currency).
		SetStatus(result.Status).
		SetLedgerEntryID(result.LedgerEntryID).
		SetWalletTransactionID(result.WalletTransactionID).
		SetPricingVersion(result.PricingVersion).
		SetPriceSnapshotJSON(string(priceSnapshotJSON)).
		SetUsagePeriodStart(result.UsagePeriodStart).
		SetUsagePeriodEnd(result.UsagePeriodEnd).
		SetQuantity(result.Quantity).
		SetUnit(result.Unit).
		SetProviderCostEvidenceRef(result.ProviderCostEvidenceRef).
		SetIdempotencyKey(input.IdempotencyKey).
		SetRequestHash(requestHash).
		SetCreatedAt(result.CreatedAt).
		Exec(ctx); err != nil {
		return ResourceSettlementResult{}, err
	}
	return result, tx.Commit()
}

func (s *PostgresStore) RecordReconciliation(ctx context.Context, input ReconciliationInput) (ReconciliationResult, error) {
	requestHash, err := hashJSON(input.Report)
	if err != nil {
		return ReconciliationResult{}, err
	}
	if existing, existingHash, err := s.reconciliationByIdempotencyKey(ctx, input.IdempotencyKey); err == nil {
		if existingHash != requestHash {
			return ReconciliationResult{}, ErrIdempotencyConflict
		}
		existing.Replayed = true
		return existing, nil
	} else if !ledgerent.IsNotFound(err) {
		return ReconciliationResult{}, err
	}
	id := stringFromAny(input.Report["id"])
	if id == "" {
		id = postgresID("recon", s.now())
	}
	status := stringFromAny(input.Report["status"])
	if status == "" {
		status = "ok"
	}
	reportJSON, err := json.Marshal(input.Report)
	if err != nil {
		return ReconciliationResult{}, err
	}
	now := s.now()
	result := ReconciliationResult{ID: id, Status: status, Report: input.Report, BlockNewWorkspaces: status != "ok", Reason: "operator_reconciliation", CreatedAt: now}
	if err := s.client.ReconciliationReport.Create().
		SetID(result.ID).
		SetStatus(result.Status).
		SetReportJSON(string(reportJSON)).
		SetBlockNewWorkspaces(result.BlockNewWorkspaces).
		SetReason(result.Reason).
		SetIdempotencyKey(input.IdempotencyKey).
		SetRequestHash(requestHash).
		SetCreatedAt(result.CreatedAt).
		Exec(ctx); err != nil {
		return ReconciliationResult{}, err
	}
	return result, nil
}

func (s *PostgresStore) Wallet(ctx context.Context, accountID string) (Wallet, error) {
	wallet, err := s.client.Wallet.Get(ctx, accountID)
	if ledgerent.IsNotFound(err) {
		return Wallet{AccountID: accountID, Currency: "CNY"}, nil
	}
	if err != nil {
		return Wallet{}, err
	}
	return walletFromEnt(wallet), nil
}

func (s *PostgresStore) ListLedgerEntries(ctx context.Context, accountID string) ([]LedgerEntry, error) {
	q := s.client.LedgerEntry.Query()
	if accountID != "" {
		q = q.Where(ledgerentry.AccountID(accountID))
	}
	rows, err := q.Order(ledgerent.Asc(ledgerentry.FieldCreatedAt, ledgerentry.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]LedgerEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, ledgerEntryFromEnt(row))
	}
	return out, nil
}

func (s *PostgresStore) ListWalletTransactions(ctx context.Context, accountID string) ([]WalletTransaction, error) {
	q := s.client.WalletTransaction.Query()
	if accountID != "" {
		q = q.Where(wallettransaction.AccountID(accountID))
	}
	rows, err := q.Order(ledgerent.Asc(wallettransaction.FieldCreatedAt, wallettransaction.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]WalletTransaction, 0, len(rows))
	for _, row := range rows {
		out = append(out, walletTransactionFromEnt(row))
	}
	return out, nil
}

func (s *PostgresStore) ListManualTopUps(ctx context.Context, accountID string) ([]ManualTopUp, error) {
	q := s.client.ManualTopup.Query()
	if accountID != "" {
		q = q.Where(manualtopup.AccountID(accountID))
	}
	rows, err := q.Order(ledgerent.Asc(manualtopup.FieldCreatedAt, manualtopup.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ManualTopUp, 0, len(rows))
	for _, row := range rows {
		out = append(out, manualTopUpFromEnt(row))
	}
	return out, nil
}

func (s *PostgresStore) ListResourceSettlements(ctx context.Context, accountID string) ([]ResourceSettlementResult, error) {
	q := s.client.ResourceSettlement.Query()
	if accountID != "" {
		q = q.Where(resourcesettlement.AccountID(accountID))
	}
	rows, err := q.Order(ledgerent.Asc(resourcesettlement.FieldCreatedAt, resourcesettlement.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ResourceSettlementResult, 0, len(rows))
	for _, row := range rows {
		out = append(out, resourceSettlementFromEnt(row, Wallet{}))
	}
	return out, nil
}

func (s *PostgresStore) ensureWallet(ctx context.Context, tx *ledgerent.Tx, accountID string, currency string, now time.Time) (Wallet, error) {
	wallet, err := s.walletByAccount(ctx, tx, accountID)
	if err == nil {
		return wallet, nil
	}
	if !ledgerent.IsNotFound(err) {
		return Wallet{}, err
	}
	created, err := tx.Wallet.Create().
		SetID(accountID).
		SetBalanceCents(0).
		SetFrozenCents(0).
		SetAvailableCents(0).
		SetTotalSpentCents(0).
		SetCurrency(currency).
		SetUpdatedAt(now).
		Save(ctx)
	if err != nil {
		return Wallet{}, err
	}
	return walletFromEnt(created), nil
}

func (s *PostgresStore) walletByAccount(ctx context.Context, tx *ledgerent.Tx, accountID string) (Wallet, error) {
	wallet, err := tx.Wallet.Get(ctx, accountID)
	if err != nil {
		return Wallet{}, err
	}
	return walletFromEnt(wallet), nil
}

func (s *PostgresStore) saveWallet(ctx context.Context, tx *ledgerent.Tx, wallet Wallet) error {
	return tx.Wallet.UpdateOneID(wallet.AccountID).
		SetBalanceCents(wallet.BalanceCents).
		SetFrozenCents(wallet.FrozenCents).
		SetAvailableCents(wallet.AvailableCents).
		SetTotalSpentCents(wallet.TotalSpentCents).
		SetCurrency(wallet.Currency).
		SetUpdatedAt(wallet.UpdatedAt).
		Exec(ctx)
}

func (s *PostgresStore) manualTopUpByIdempotencyKey(ctx context.Context, tx *ledgerent.Tx, key string) (ManualTopUpResult, string, error) {
	row, err := tx.ManualTopup.Query().Where(manualtopup.IdempotencyKey(key)).Only(ctx)
	if err != nil {
		return ManualTopUpResult{}, "", err
	}
	entry, err := tx.LedgerEntry.Get(ctx, row.LedgerEntryID)
	if err != nil {
		return ManualTopUpResult{}, "", err
	}
	walletTx, err := tx.WalletTransaction.Get(ctx, row.WalletTransactionID)
	if err != nil {
		return ManualTopUpResult{}, "", err
	}
	wallet, err := s.walletByAccount(ctx, tx, row.AccountID)
	if err != nil {
		return ManualTopUpResult{}, "", err
	}
	return ManualTopUpResult{TopUp: manualTopUpFromEnt(row), LedgerEntry: ledgerEntryFromEnt(entry), WalletTransaction: walletTransactionFromEnt(walletTx), Wallet: wallet}, row.RequestHash, nil
}

func (s *PostgresStore) holdByIdempotencyKey(ctx context.Context, tx *ledgerent.Tx, key string) (HoldResult, string, error) {
	row, err := tx.Hold.Query().Where(hold.IdempotencyKey(key)).Only(ctx)
	if err != nil {
		return HoldResult{}, "", err
	}
	wallet, err := s.walletByAccount(ctx, tx, row.AccountID)
	if err != nil {
		return HoldResult{}, "", err
	}
	return holdResultFromEnt(row, wallet), row.RequestHash, nil
}

func (s *PostgresStore) holdActivationByIdempotencyKey(ctx context.Context, tx *ledgerent.Tx, key string) (HoldActivationResult, string, error) {
	row, err := tx.HoldActivation.Query().Where(holdactivation.IdempotencyKey(key)).Only(ctx)
	if err != nil {
		return HoldActivationResult{}, "", err
	}
	holdRow, err := tx.Hold.Get(ctx, row.HoldID)
	if err != nil {
		return HoldActivationResult{}, "", err
	}
	wallet, err := s.walletByAccount(ctx, tx, row.AccountID)
	if err != nil {
		return HoldActivationResult{}, "", err
	}
	return HoldActivationResult{HoldResult: holdResultFromEnt(holdRow, wallet), ActivationLedgerEntryID: row.LedgerEntryID, ActivationWalletTransactionID: row.WalletTransactionID}, row.RequestHash, nil
}

func (s *PostgresStore) holdReleaseByIdempotencyKey(ctx context.Context, tx *ledgerent.Tx, key string) (HoldReleaseResult, string, error) {
	row, err := tx.HoldRelease.Query().Where(holdrelease.IdempotencyKey(key)).Only(ctx)
	if err != nil {
		return HoldReleaseResult{}, "", err
	}
	wallet, err := s.walletByAccount(ctx, tx, row.AccountID)
	if err != nil {
		return HoldReleaseResult{}, "", err
	}
	return HoldReleaseResult{ID: row.ID, AccountID: row.AccountID, WorkspaceID: row.WorkspaceID, ResourceType: row.ResourceType, ResourceID: row.ResourceID, HoldID: row.HoldID, AmountCents: row.AmountCents, Currency: row.Currency, Status: row.Status, LedgerEntryID: row.LedgerEntryID, WalletTransactionID: row.WalletTransactionID, Wallet: wallet, CreatedAt: row.CreatedAt}, row.RequestHash, nil
}

func (s *PostgresStore) receiptByIdempotencyKey(ctx context.Context, key string) (Receipt, string, error) {
	row, err := s.client.EvidenceReceipt.Query().Where(evidencereceipt.IdempotencyKey(key)).Only(ctx)
	if err != nil {
		return Receipt{}, "", err
	}
	return receiptFromEnt(row), row.RequestHash, nil
}

func receiptFromEnt(row *ledgerent.EvidenceReceipt) Receipt {
	// ponytail: payload is canonical; promote fields to columns only when a real query needs an index.
	var stored receiptPayload
	_ = json.Unmarshal([]byte(row.PayloadJSON), &stored)
	input := stored.ReceiptInput
	input.Type = row.ReceiptType
	input.Status = row.Status
	input.OrganizationID = row.OrganizationID
	input.WorkspaceID = row.WorkspaceID
	input.ProjectID = row.ProjectID
	input.TaskID = row.TaskID
	input.JobID = row.JobID
	input.SupersedesReceiptID = row.SupersedesReceiptID
	return Receipt{ReceiptInput: input, ReceiptID: row.ID, CreatedAt: row.CreatedAt, Retention: stored.Retention}
}

type receiptPayload struct {
	ReceiptInput
	Retention ReceiptRetention `json:"retention"`
}

func (s *PostgresStore) settlementByIdempotencyKey(ctx context.Context, tx *ledgerent.Tx, key string) (ResourceSettlementResult, string, error) {
	row, err := tx.ResourceSettlement.Query().Where(resourcesettlement.IdempotencyKey(key)).Only(ctx)
	if err != nil {
		return ResourceSettlementResult{}, "", err
	}
	wallet, err := s.walletByAccount(ctx, tx, row.AccountID)
	if err != nil {
		return ResourceSettlementResult{}, "", err
	}
	return resourceSettlementFromEnt(row, wallet), row.RequestHash, nil
}

func (s *PostgresStore) reconciliationByIdempotencyKey(ctx context.Context, key string) (ReconciliationResult, string, error) {
	row, err := s.client.ReconciliationReport.Query().Where(reconciliationreport.IdempotencyKey(key)).Only(ctx)
	if err != nil {
		return ReconciliationResult{}, "", err
	}
	result := ReconciliationResult{ID: row.ID, Status: row.Status, BlockNewWorkspaces: row.BlockNewWorkspaces, Reason: row.Reason, CreatedAt: row.CreatedAt}
	_ = json.Unmarshal([]byte(row.ReportJSON), &result.Report)
	return result, row.RequestHash, nil
}

func createLedgerEntry(ctx context.Context, tx *ledgerent.Tx, entry LedgerEntry) error {
	return tx.LedgerEntry.Create().
		SetID(entry.ID).
		SetAccountID(entry.AccountID).
		SetAmountCents(entry.AmountCents).
		SetCurrency(entry.Currency).
		SetDirection(entry.Direction).
		SetSource(entry.Source).
		SetOperatorUserID(entry.OperatorUserID).
		SetReason(entry.Reason).
		SetCreatedAt(entry.CreatedAt).
		Exec(ctx)
}

func createWalletTransaction(ctx context.Context, tx *ledgerent.Tx, walletTx WalletTransaction) error {
	return tx.WalletTransaction.Create().
		SetID(walletTx.ID).
		SetAccountID(walletTx.AccountID).
		SetLedgerEntryID(walletTx.LedgerEntryID).
		SetAmountCents(walletTx.AmountCents).
		SetBalanceCents(walletTx.BalanceCents).
		SetFrozenCents(walletTx.FrozenCents).
		SetAvailableCents(walletTx.AvailableCents).
		SetTotalSpentCents(walletTx.TotalSpentCents).
		SetCurrency(walletTx.Currency).
		SetCreatedAt(walletTx.CreatedAt).
		Exec(ctx)
}

func walletFromEnt(row *ledgerent.Wallet) Wallet {
	return Wallet{AccountID: row.ID, BalanceCents: row.BalanceCents, FrozenCents: row.FrozenCents, AvailableCents: row.AvailableCents, TotalSpentCents: row.TotalSpentCents, Currency: row.Currency, UpdatedAt: row.UpdatedAt}
}

func ledgerEntryFromEnt(row *ledgerent.LedgerEntry) LedgerEntry {
	return LedgerEntry{ID: row.ID, AccountID: row.AccountID, AmountCents: row.AmountCents, Currency: row.Currency, Direction: row.Direction, Source: row.Source, OperatorUserID: row.OperatorUserID, Reason: row.Reason, CreatedAt: row.CreatedAt}
}

func walletTransactionFromEnt(row *ledgerent.WalletTransaction) WalletTransaction {
	return WalletTransaction{ID: row.ID, AccountID: row.AccountID, LedgerEntryID: row.LedgerEntryID, AmountCents: row.AmountCents, BalanceCents: row.BalanceCents, FrozenCents: row.FrozenCents, AvailableCents: row.AvailableCents, TotalSpentCents: row.TotalSpentCents, Currency: row.Currency, CreatedAt: row.CreatedAt}
}

func manualTopUpFromEnt(row *ledgerent.ManualTopup) ManualTopUp {
	return ManualTopUp{ID: row.ID, AccountID: row.AccountID, AmountCents: row.AmountCents, Currency: row.Currency, OperatorUserID: row.OperatorUserID, LedgerEntryID: row.LedgerEntryID, Reason: row.Reason, CreatedAt: row.CreatedAt}
}

func resourceSettlementFromEnt(row *ledgerent.ResourceSettlement, wallet Wallet) ResourceSettlementResult {
	result := ResourceSettlementResult{ID: row.ID, AccountID: row.AccountID, WorkspaceID: row.WorkspaceID, ResourceType: row.ResourceType, ResourceID: row.ResourceID, HoldID: row.HoldID, AmountCents: row.AmountCents, Currency: row.Currency, Status: row.Status, LedgerEntryID: row.LedgerEntryID, WalletTransactionID: row.WalletTransactionID, PricingVersion: row.PricingVersion, UsagePeriodStart: row.UsagePeriodStart, UsagePeriodEnd: row.UsagePeriodEnd, Quantity: row.Quantity, Unit: row.Unit, ProviderCostEvidenceRef: row.ProviderCostEvidenceRef, Wallet: wallet, CreatedAt: row.CreatedAt}
	_ = json.Unmarshal([]byte(row.PriceSnapshotJSON), &result.PriceSnapshot)
	return result
}

func holdResultFromEnt(row *ledgerent.Hold, wallet Wallet) HoldResult {
	return HoldResult{ID: row.ID, AccountID: row.AccountID, WorkspaceID: row.WorkspaceID, ResourceType: row.ResourceType, ResourceID: row.ResourceID, AmountCents: row.AmountCents, ActivationAmountCents: row.ActivationAmountCents, OriginalCents: row.OriginalCents, RemainingCents: row.RemainingCents, ConsumedCents: row.ConsumedCents, ReleasedCents: row.ReleasedCents, ProviderEvidenceRef: row.ProviderEvidenceRef, Currency: row.Currency, Status: row.Status, LedgerEntryID: row.LedgerEntryID, WalletTransactionID: row.WalletTransactionID, Wallet: wallet, CreatedAt: row.CreatedAt}
}

func holdEntMatches(row *ledgerent.Hold, accountID, workspaceID, resourceType, resourceID, currency string) bool {
	return row.AccountID == accountID && row.WorkspaceID == workspaceID && row.ResourceType == resourceType && row.ResourceID == resourceID && row.Currency == currency
}

func (s *PostgresStore) lockAccount(ctx context.Context, accountID string) (func(), error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	key := "ledger-account:" + accountID
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock(hashtext($1))", key); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return func() {
		_, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock(hashtext($1))", key)
		_ = conn.Close()
	}, nil
}

func postgresID(prefix string, t time.Time) string {
	return fmt.Sprintf("%s_%d", prefix, t.UnixNano())
}

var _ = errors.Is
