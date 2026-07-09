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
	"opl-cloud/services/ledger/ent/holdrelease"
	"opl-cloud/services/ledger/ent/ledgerentry"
	"opl-cloud/services/ledger/ent/manualtopup"
	"opl-cloud/services/ledger/ent/reconciliationreport"
	"opl-cloud/services/ledger/ent/resourcesettlement"
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
	if input.ResourceType == "" || input.ResourceID == "" || input.AmountCents <= 0 {
		return HoldResult{}, ErrInvalidHoldInput
	}
	requestHash, err := hashJSON(struct {
		AccountID    string `json:"accountId"`
		WorkspaceID  string `json:"workspaceId"`
		ResourceType string `json:"resourceType"`
		ResourceID   string `json:"resourceId"`
		AmountCents  int64  `json:"amountCents"`
		Currency     string `json:"currency"`
	}{input.AccountID, input.WorkspaceID, input.ResourceType, input.ResourceID, input.AmountCents, input.Currency})
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
	result := HoldResult{ID: postgresID("hold", now.Add(2*time.Nanosecond)), AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: input.ResourceType, ResourceID: input.ResourceID, AmountCents: input.AmountCents, Currency: input.Currency, Status: "held", LedgerEntryID: entry.ID, WalletTransactionID: walletTx.ID, Wallet: wallet, CreatedAt: now}
	if err := tx.Hold.Create().
		SetID(result.ID).
		SetAccountID(result.AccountID).
		SetWorkspaceID(result.WorkspaceID).
		SetResourceType(result.ResourceType).
		SetResourceID(result.ResourceID).
		SetAmountCents(result.AmountCents).
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

func (s *PostgresStore) ReleaseHold(ctx context.Context, input HoldReleaseInput) (HoldReleaseResult, error) {
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

	now := s.now()
	wallet, err := s.walletByAccount(ctx, tx, input.AccountID)
	if ledgerent.IsNotFound(err) {
		return HoldReleaseResult{}, ErrInsufficientFrozen
	}
	if err != nil {
		return HoldReleaseResult{}, err
	}
	if wallet.FrozenCents < input.AmountCents {
		return HoldReleaseResult{}, ErrInsufficientFrozen
	}
	wallet.FrozenCents -= input.AmountCents
	wallet.Currency = input.Currency
	wallet.AvailableCents = wallet.BalanceCents - wallet.FrozenCents
	wallet.UpdatedAt = now
	if err := s.saveWallet(ctx, tx, wallet); err != nil {
		return HoldReleaseResult{}, err
	}

	entry := LedgerEntry{ID: postgresID("le", now), AccountID: input.AccountID, AmountCents: input.AmountCents, Currency: input.Currency, Direction: "release", Source: input.ResourceType + "_hold_released", Reason: input.Reason, CreatedAt: now}
	if err := createLedgerEntry(ctx, tx, entry); err != nil {
		return HoldReleaseResult{}, err
	}
	walletTx := WalletTransaction{ID: postgresID("wtx", now.Add(time.Nanosecond)), AccountID: input.AccountID, LedgerEntryID: entry.ID, AmountCents: 0, BalanceCents: wallet.BalanceCents, FrozenCents: wallet.FrozenCents, AvailableCents: wallet.AvailableCents, TotalSpentCents: wallet.TotalSpentCents, Currency: input.Currency, CreatedAt: now}
	if err := createWalletTransaction(ctx, tx, walletTx); err != nil {
		return HoldReleaseResult{}, err
	}
	result := HoldReleaseResult{ID: postgresID("hrel", now.Add(2*time.Nanosecond)), AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: input.ResourceType, ResourceID: input.ResourceID, HoldID: input.HoldID, AmountCents: input.AmountCents, Currency: input.Currency, Status: "released", LedgerEntryID: entry.ID, WalletTransactionID: walletTx.ID, Wallet: wallet, CreatedAt: now}
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
	return result, tx.Commit()
}

func (s *PostgresStore) RecordEvidence(ctx context.Context, input EvidenceInput) (EvidenceReceipt, error) {
	requestHash, err := hashJSON(struct {
		WorkspaceID       string `json:"workspaceId"`
		ProviderRequestID string `json:"providerRequestId"`
		RedactedURL       string `json:"redactedUrl"`
		TokenVersion      string `json:"tokenVersion"`
	}{input.WorkspaceID, input.ProviderRequestID, input.RedactedURL, input.TokenVersion})
	if err != nil {
		return EvidenceReceipt{}, err
	}
	if existing, existingHash, err := s.evidenceByIdempotencyKey(ctx, input.IdempotencyKey); err == nil {
		if existingHash != requestHash {
			return EvidenceReceipt{}, ErrIdempotencyConflict
		}
		existing.Replayed = true
		return existing, nil
	} else if !ledgerent.IsNotFound(err) {
		return EvidenceReceipt{}, err
	}
	now := s.now()
	receipt := EvidenceReceipt{ID: postgresID("ev", now), WorkspaceID: input.WorkspaceID, ProviderRequestID: input.ProviderRequestID, RedactedURL: input.RedactedURL, TokenVersion: input.TokenVersion, CreatedAt: now}
	if err := s.client.EvidenceReceipt.Create().
		SetID(receipt.ID).
		SetWorkspaceID(receipt.WorkspaceID).
		SetProviderRequestID(receipt.ProviderRequestID).
		SetRedactedURL(receipt.RedactedURL).
		SetTokenVersion(receipt.TokenVersion).
		SetIdempotencyKey(input.IdempotencyKey).
		SetRequestHash(requestHash).
		SetCreatedAt(receipt.CreatedAt).
		Exec(ctx); err != nil {
		return EvidenceReceipt{}, err
	}
	return receipt, nil
}

func (s *PostgresStore) SettleResource(ctx context.Context, input ResourceSettlementInput) (ResourceSettlementResult, error) {
	requestHash, err := hashJSON(struct {
		AccountID               string         `json:"accountId"`
		WorkspaceID             string         `json:"workspaceId"`
		ResourceType            string         `json:"resourceType"`
		ResourceID              string         `json:"resourceId"`
		AmountCents             int64          `json:"amountCents"`
		Currency                string         `json:"currency"`
		PricingVersion          string         `json:"pricingVersion"`
		PriceSnapshot           map[string]any `json:"priceSnapshot"`
		UsagePeriodStart        string         `json:"usagePeriodStart"`
		UsagePeriodEnd          string         `json:"usagePeriodEnd"`
		Quantity                float64        `json:"quantity"`
		Unit                    string         `json:"unit"`
		ProviderCostEvidenceRef string         `json:"providerCostEvidenceRef"`
	}{input.AccountID, input.WorkspaceID, input.ResourceType, input.ResourceID, input.AmountCents, input.Currency, input.PricingVersion, input.PriceSnapshot, input.UsagePeriodStart, input.UsagePeriodEnd, input.Quantity, input.Unit, input.ProviderCostEvidenceRef})
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

	now := s.now()
	wallet, err := s.walletByAccount(ctx, tx, input.AccountID)
	if ledgerent.IsNotFound(err) {
		return ResourceSettlementResult{}, ErrInsufficientBalance
	}
	if err != nil {
		return ResourceSettlementResult{}, err
	}
	if wallet.BalanceCents < input.AmountCents {
		return ResourceSettlementResult{}, ErrInsufficientBalance
	}
	wallet.BalanceCents -= input.AmountCents
	wallet.FrozenCents -= minInt64(wallet.FrozenCents, input.AmountCents)
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
	result := ResourceSettlementResult{ID: postgresID("settle", now.Add(2*time.Nanosecond)), AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: input.ResourceType, ResourceID: input.ResourceID, AmountCents: input.AmountCents, Currency: input.Currency, Status: "settled", LedgerEntryID: entry.ID, WalletTransactionID: walletTx.ID, PricingVersion: input.PricingVersion, PriceSnapshot: cloneAnyMap(input.PriceSnapshot), UsagePeriodStart: input.UsagePeriodStart, UsagePeriodEnd: input.UsagePeriodEnd, Quantity: input.Quantity, Unit: input.Unit, ProviderCostEvidenceRef: input.ProviderCostEvidenceRef, Wallet: wallet, CreatedAt: now}
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
	return HoldResult{ID: row.ID, AccountID: row.AccountID, WorkspaceID: row.WorkspaceID, ResourceType: row.ResourceType, ResourceID: row.ResourceID, AmountCents: row.AmountCents, Currency: row.Currency, Status: row.Status, LedgerEntryID: row.LedgerEntryID, WalletTransactionID: row.WalletTransactionID, Wallet: wallet, CreatedAt: row.CreatedAt}, row.RequestHash, nil
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

func (s *PostgresStore) evidenceByIdempotencyKey(ctx context.Context, key string) (EvidenceReceipt, string, error) {
	row, err := s.client.EvidenceReceipt.Query().Where(evidencereceipt.IdempotencyKey(key)).Only(ctx)
	if err != nil {
		return EvidenceReceipt{}, "", err
	}
	return EvidenceReceipt{ID: row.ID, WorkspaceID: row.WorkspaceID, ProviderRequestID: row.ProviderRequestID, RedactedURL: row.RedactedURL, TokenVersion: row.TokenVersion, CreatedAt: row.CreatedAt}, row.RequestHash, nil
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
	result := ResourceSettlementResult{ID: row.ID, AccountID: row.AccountID, WorkspaceID: row.WorkspaceID, ResourceType: row.ResourceType, ResourceID: row.ResourceID, AmountCents: row.AmountCents, Currency: row.Currency, Status: row.Status, LedgerEntryID: row.LedgerEntryID, WalletTransactionID: row.WalletTransactionID, PricingVersion: row.PricingVersion, UsagePeriodStart: row.UsagePeriodStart, UsagePeriodEnd: row.UsagePeriodEnd, Quantity: row.Quantity, Unit: row.Unit, ProviderCostEvidenceRef: row.ProviderCostEvidenceRef, Wallet: wallet, CreatedAt: row.CreatedAt}
	_ = json.Unmarshal([]byte(row.PriceSnapshotJSON), &result.PriceSnapshot)
	return result
}

func postgresID(prefix string, t time.Time) string {
	return fmt.Sprintf("%s_%d", prefix, t.UnixNano())
}

var _ = errors.Is
