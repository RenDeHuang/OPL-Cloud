package ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const postgresSchema = `
	CREATE TABLE IF NOT EXISTS wallets (
	  account_id TEXT PRIMARY KEY,
	  balance_cents BIGINT NOT NULL DEFAULT 0,
	  frozen_cents BIGINT NOT NULL DEFAULT 0,
	  total_spent_cents BIGINT NOT NULL DEFAULT 0,
	  currency TEXT NOT NULL DEFAULT 'CNY',
	  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
	);

	ALTER TABLE wallets ADD COLUMN IF NOT EXISTS frozen_cents BIGINT NOT NULL DEFAULT 0;
	ALTER TABLE wallets ADD COLUMN IF NOT EXISTS total_spent_cents BIGINT NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS ledger_entries (
  id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL REFERENCES wallets(account_id),
  amount_cents BIGINT NOT NULL,
  currency TEXT NOT NULL,
  direction TEXT NOT NULL,
  source TEXT NOT NULL,
  operator_user_id TEXT NOT NULL,
  reason TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE ledger_entries ADD COLUMN IF NOT EXISTS reason TEXT;

CREATE TABLE IF NOT EXISTS wallet_transactions (
  id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL REFERENCES wallets(account_id),
  ledger_entry_id TEXT NOT NULL REFERENCES ledger_entries(id),
  amount_cents BIGINT NOT NULL,
  balance_cents BIGINT NOT NULL,
  currency TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE wallet_transactions ADD COLUMN IF NOT EXISTS ledger_entry_id TEXT;
ALTER TABLE wallet_transactions ADD COLUMN IF NOT EXISTS amount_cents BIGINT;
ALTER TABLE wallet_transactions ADD COLUMN IF NOT EXISTS balance_cents BIGINT;
ALTER TABLE wallet_transactions ADD COLUMN IF NOT EXISTS currency TEXT;
UPDATE wallet_transactions SET ledger_entry_id = id WHERE ledger_entry_id IS NULL;
UPDATE wallet_transactions SET amount_cents = 0 WHERE amount_cents IS NULL;
UPDATE wallet_transactions SET balance_cents = 0 WHERE balance_cents IS NULL;
UPDATE wallet_transactions SET currency = 'CNY' WHERE currency IS NULL;
ALTER TABLE wallet_transactions ALTER COLUMN ledger_entry_id SET NOT NULL;
ALTER TABLE wallet_transactions ALTER COLUMN amount_cents SET NOT NULL;
ALTER TABLE wallet_transactions ALTER COLUMN balance_cents SET NOT NULL;
ALTER TABLE wallet_transactions ALTER COLUMN currency SET NOT NULL;

	CREATE TABLE IF NOT EXISTS manual_topups (
  id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL REFERENCES wallets(account_id),
  amount_cents BIGINT NOT NULL,
  currency TEXT NOT NULL,
  operator_user_id TEXT NOT NULL,
  ledger_entry_id TEXT NOT NULL REFERENCES ledger_entries(id),
  wallet_transaction_id TEXT NOT NULL REFERENCES wallet_transactions(id),
  idempotency_key TEXT NOT NULL,
  request_hash TEXT NOT NULL,
  reason TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
	);

	ALTER TABLE manual_topups ADD COLUMN IF NOT EXISTS account_id TEXT;
	ALTER TABLE manual_topups ADD COLUMN IF NOT EXISTS amount_cents BIGINT;
	ALTER TABLE manual_topups ADD COLUMN IF NOT EXISTS currency TEXT;
	ALTER TABLE manual_topups ADD COLUMN IF NOT EXISTS operator_user_id TEXT;
	ALTER TABLE manual_topups ADD COLUMN IF NOT EXISTS ledger_entry_id TEXT;
	ALTER TABLE manual_topups ADD COLUMN IF NOT EXISTS wallet_transaction_id TEXT;
	ALTER TABLE manual_topups ADD COLUMN IF NOT EXISTS idempotency_key TEXT;
	ALTER TABLE manual_topups ADD COLUMN IF NOT EXISTS request_hash TEXT;
	ALTER TABLE manual_topups ADD COLUMN IF NOT EXISTS reason TEXT;
	DO $$
	BEGIN
	  IF EXISTS (
	    SELECT 1 FROM information_schema.columns
	    WHERE table_schema = 'public' AND table_name = 'manual_topups' AND column_name = 'target_account_id'
	  ) THEN
	    EXECUTE 'UPDATE manual_topups SET account_id = target_account_id WHERE account_id IS NULL';
	  END IF;
	END $$;
	UPDATE manual_topups SET account_id = id WHERE account_id IS NULL;
	UPDATE manual_topups SET amount_cents = 0 WHERE amount_cents IS NULL;
	UPDATE manual_topups SET currency = 'CNY' WHERE currency IS NULL;
	UPDATE manual_topups SET operator_user_id = '' WHERE operator_user_id IS NULL;
	UPDATE manual_topups SET ledger_entry_id = id WHERE ledger_entry_id IS NULL;
	UPDATE manual_topups SET wallet_transaction_id = id WHERE wallet_transaction_id IS NULL;
	UPDATE manual_topups SET idempotency_key = 'migrated:' || ctid::text WHERE idempotency_key IS NULL;
	UPDATE manual_topups SET request_hash = 'migrated:' || ctid::text WHERE request_hash IS NULL;
	ALTER TABLE manual_topups ALTER COLUMN account_id SET NOT NULL;
	ALTER TABLE manual_topups ALTER COLUMN amount_cents SET NOT NULL;
	ALTER TABLE manual_topups ALTER COLUMN currency SET NOT NULL;
	ALTER TABLE manual_topups ALTER COLUMN operator_user_id SET NOT NULL;
	ALTER TABLE manual_topups ALTER COLUMN ledger_entry_id SET NOT NULL;
	ALTER TABLE manual_topups ALTER COLUMN wallet_transaction_id SET NOT NULL;
	ALTER TABLE manual_topups ALTER COLUMN idempotency_key SET NOT NULL;
	ALTER TABLE manual_topups ALTER COLUMN request_hash SET NOT NULL;

	CREATE TABLE IF NOT EXISTS holds (
	  id TEXT PRIMARY KEY,
	  account_id TEXT NOT NULL REFERENCES wallets(account_id),
	  workspace_id TEXT NOT NULL,
	  resource_type TEXT NOT NULL,
	  resource_id TEXT NOT NULL,
	  amount_cents BIGINT NOT NULL,
	  currency TEXT NOT NULL,
	  status TEXT NOT NULL,
	  ledger_entry_id TEXT NOT NULL REFERENCES ledger_entries(id),
	  wallet_transaction_id TEXT NOT NULL REFERENCES wallet_transactions(id),
	  idempotency_key TEXT NOT NULL,
	  request_hash TEXT NOT NULL,
	  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
	);

	ALTER TABLE holds ADD COLUMN IF NOT EXISTS idempotency_key TEXT;
	ALTER TABLE holds ADD COLUMN IF NOT EXISTS request_hash TEXT;
	ALTER TABLE holds ADD COLUMN IF NOT EXISTS resource_type TEXT;
	ALTER TABLE holds ADD COLUMN IF NOT EXISTS resource_id TEXT;
	UPDATE holds SET idempotency_key = 'migrated:' || ctid::text WHERE idempotency_key IS NULL;
	UPDATE holds SET request_hash = 'migrated:' || ctid::text WHERE request_hash IS NULL;
	UPDATE holds SET resource_type = 'migrated' WHERE resource_type IS NULL;
	UPDATE holds SET resource_id = workspace_id WHERE resource_id IS NULL;
	ALTER TABLE holds ALTER COLUMN idempotency_key SET NOT NULL;
	ALTER TABLE holds ALTER COLUMN request_hash SET NOT NULL;
	ALTER TABLE holds ALTER COLUMN resource_type SET NOT NULL;
	ALTER TABLE holds ALTER COLUMN resource_id SET NOT NULL;

	CREATE TABLE IF NOT EXISTS evidence_receipts (
	  id TEXT PRIMARY KEY,
	  workspace_id TEXT NOT NULL,
	  provider_request_id TEXT NOT NULL,
	  redacted_url TEXT NOT NULL,
	  token_version TEXT NOT NULL,
	  idempotency_key TEXT NOT NULL,
	  request_hash TEXT NOT NULL,
	  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
	);

	ALTER TABLE evidence_receipts ADD COLUMN IF NOT EXISTS idempotency_key TEXT;
	ALTER TABLE evidence_receipts ADD COLUMN IF NOT EXISTS request_hash TEXT;
	UPDATE evidence_receipts SET idempotency_key = 'migrated:' || ctid::text WHERE idempotency_key IS NULL;
	UPDATE evidence_receipts SET request_hash = 'migrated:' || ctid::text WHERE request_hash IS NULL;
	ALTER TABLE evidence_receipts ALTER COLUMN idempotency_key SET NOT NULL;
	ALTER TABLE evidence_receipts ALTER COLUMN request_hash SET NOT NULL;

	CREATE TABLE IF NOT EXISTS resource_settlements (
	  id TEXT PRIMARY KEY,
	  account_id TEXT NOT NULL REFERENCES wallets(account_id),
	  workspace_id TEXT NOT NULL,
	  resource_type TEXT NOT NULL,
	  resource_id TEXT NOT NULL,
	  amount_cents BIGINT NOT NULL,
	  currency TEXT NOT NULL,
	  status TEXT NOT NULL,
	  ledger_entry_id TEXT NOT NULL REFERENCES ledger_entries(id),
	  wallet_transaction_id TEXT NOT NULL REFERENCES wallet_transactions(id),
	  idempotency_key TEXT NOT NULL,
	  request_hash TEXT NOT NULL,
	  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
	);

	ALTER TABLE resource_settlements ADD COLUMN IF NOT EXISTS idempotency_key TEXT;
	ALTER TABLE resource_settlements ADD COLUMN IF NOT EXISTS request_hash TEXT;
	UPDATE resource_settlements SET idempotency_key = 'migrated:' || ctid::text WHERE idempotency_key IS NULL;
	UPDATE resource_settlements SET request_hash = 'migrated:' || ctid::text WHERE request_hash IS NULL;
	ALTER TABLE resource_settlements ALTER COLUMN idempotency_key SET NOT NULL;
	ALTER TABLE resource_settlements ALTER COLUMN request_hash SET NOT NULL;

	CREATE TABLE IF NOT EXISTS hold_releases (
	  id TEXT PRIMARY KEY,
	  account_id TEXT NOT NULL REFERENCES wallets(account_id),
	  workspace_id TEXT NOT NULL,
	  resource_type TEXT NOT NULL,
	  resource_id TEXT NOT NULL,
	  hold_id TEXT NOT NULL,
	  amount_cents BIGINT NOT NULL,
	  currency TEXT NOT NULL,
	  status TEXT NOT NULL,
	  ledger_entry_id TEXT NOT NULL REFERENCES ledger_entries(id),
	  wallet_transaction_id TEXT NOT NULL REFERENCES wallet_transactions(id),
	  idempotency_key TEXT NOT NULL,
	  request_hash TEXT NOT NULL,
	  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
	);

	ALTER TABLE hold_releases ADD COLUMN IF NOT EXISTS idempotency_key TEXT;
	ALTER TABLE hold_releases ADD COLUMN IF NOT EXISTS request_hash TEXT;
	UPDATE hold_releases SET idempotency_key = 'migrated:' || ctid::text WHERE idempotency_key IS NULL;
	UPDATE hold_releases SET request_hash = 'migrated:' || ctid::text WHERE request_hash IS NULL;
	ALTER TABLE hold_releases ALTER COLUMN idempotency_key SET NOT NULL;
	ALTER TABLE hold_releases ALTER COLUMN request_hash SET NOT NULL;

	CREATE TABLE IF NOT EXISTS reconciliation_reports (
	  id TEXT PRIMARY KEY,
	  status TEXT NOT NULL,
	  report_json TEXT NOT NULL,
	  block_new_workspaces BOOLEAN NOT NULL,
	  reason TEXT NOT NULL,
	  idempotency_key TEXT NOT NULL,
	  request_hash TEXT NOT NULL,
	  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
	);

	ALTER TABLE reconciliation_reports ADD COLUMN IF NOT EXISTS idempotency_key TEXT;
	ALTER TABLE reconciliation_reports ADD COLUMN IF NOT EXISTS request_hash TEXT;
	UPDATE reconciliation_reports SET idempotency_key = 'migrated:' || ctid::text WHERE idempotency_key IS NULL;
	UPDATE reconciliation_reports SET request_hash = 'migrated:' || ctid::text WHERE request_hash IS NULL;
	ALTER TABLE reconciliation_reports ALTER COLUMN idempotency_key SET NOT NULL;
	ALTER TABLE reconciliation_reports ALTER COLUMN request_hash SET NOT NULL;

CREATE TABLE IF NOT EXISTS idempotency_keys (
  service TEXT NOT NULL,
  idempotency_key TEXT NOT NULL,
  request_hash TEXT NOT NULL,
  response_ref TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (service, idempotency_key)
);

	CREATE UNIQUE INDEX IF NOT EXISTS manual_topups_idempotency_key_idx
	  ON manual_topups(idempotency_key);
	CREATE UNIQUE INDEX IF NOT EXISTS holds_idempotency_key_idx
	  ON holds(idempotency_key);
	CREATE UNIQUE INDEX IF NOT EXISTS evidence_receipts_idempotency_key_idx
	  ON evidence_receipts(idempotency_key);
	CREATE UNIQUE INDEX IF NOT EXISTS resource_settlements_idempotency_key_idx
	  ON resource_settlements(idempotency_key);
	CREATE UNIQUE INDEX IF NOT EXISTS hold_releases_idempotency_key_idx
	  ON hold_releases(idempotency_key);
	CREATE UNIQUE INDEX IF NOT EXISTS reconciliation_reports_idempotency_key_idx
	  ON reconciliation_reports(idempotency_key);
	`

type PostgresStore struct {
	db  *sql.DB
	now func() time.Time
}

func PostgresSchemaSQL() string {
	return postgresSchema
}

func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db, now: func() time.Time { return time.Now().UTC() }}
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

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ManualTopUpResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	existing, existingHash, err := s.manualTopUpByIdempotencyKey(ctx, tx, input.IdempotencyKey)
	if err == nil {
		if existingHash != requestHash {
			return ManualTopUpResult{}, ErrIdempotencyConflict
		}
		existing.Replayed = true
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ManualTopUpResult{}, err
	}

	now := s.now()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO idempotency_keys(service, idempotency_key, request_hash)
VALUES ('ledger.manual_topups', $1, $2)
`, input.IdempotencyKey, requestHash); err != nil {
		return ManualTopUpResult{}, err
	}

	wallet := Wallet{}
	if err := tx.QueryRowContext(ctx, `
	INSERT INTO wallets(account_id, balance_cents, frozen_cents, total_spent_cents, currency, updated_at)
	VALUES ($1, $2, $3, $4, $5, $6)
	ON CONFLICT (account_id) DO UPDATE SET
	  balance_cents = wallets.balance_cents + EXCLUDED.balance_cents,
	  currency = EXCLUDED.currency,
	  updated_at = EXCLUDED.updated_at
	RETURNING account_id, balance_cents, frozen_cents, balance_cents - frozen_cents, total_spent_cents, currency, updated_at
	`, input.AccountID, input.AmountCents, int64(0), int64(0), input.Currency, now).Scan(
		&wallet.AccountID,
		&wallet.BalanceCents,
		&wallet.FrozenCents,
		&wallet.AvailableCents,
		&wallet.TotalSpentCents,
		&wallet.Currency,
		&wallet.UpdatedAt,
	); err != nil {
		return ManualTopUpResult{}, err
	}

	entry := LedgerEntry{
		ID:             postgresID("le", now),
		AccountID:      input.AccountID,
		AmountCents:    input.AmountCents,
		Currency:       input.Currency,
		Direction:      "credit",
		Source:         "manual_topup",
		OperatorUserID: input.OperatorUserID,
		Reason:         input.Reason,
		CreatedAt:      now,
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO ledger_entries(id, account_id, amount_cents, currency, direction, source, operator_user_id, reason, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
`, entry.ID, entry.AccountID, entry.AmountCents, entry.Currency, entry.Direction, entry.Source, entry.OperatorUserID, entry.Reason, entry.CreatedAt); err != nil {
		return ManualTopUpResult{}, err
	}

	walletTx := WalletTransaction{
		ID:            postgresID("wtx", now.Add(time.Nanosecond)),
		AccountID:     input.AccountID,
		LedgerEntryID: entry.ID,
		AmountCents:   input.AmountCents,
		BalanceCents:  wallet.BalanceCents,
		Currency:      input.Currency,
		CreatedAt:     now,
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO wallet_transactions(id, account_id, ledger_entry_id, amount_cents, balance_cents, currency, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
`, walletTx.ID, walletTx.AccountID, walletTx.LedgerEntryID, walletTx.AmountCents, walletTx.BalanceCents, walletTx.Currency, walletTx.CreatedAt); err != nil {
		return ManualTopUpResult{}, err
	}

	topup := ManualTopUp{
		ID:             postgresID("mtu", now.Add(2*time.Nanosecond)),
		AccountID:      input.AccountID,
		AmountCents:    input.AmountCents,
		Currency:       input.Currency,
		OperatorUserID: input.OperatorUserID,
		LedgerEntryID:  entry.ID,
		Reason:         input.Reason,
		CreatedAt:      now,
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO manual_topups(id, account_id, amount_cents, currency, operator_user_id, ledger_entry_id, wallet_transaction_id, idempotency_key, request_hash, reason, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
`, topup.ID, topup.AccountID, topup.AmountCents, topup.Currency, topup.OperatorUserID, topup.LedgerEntryID, walletTx.ID, input.IdempotencyKey, requestHash, topup.Reason, topup.CreatedAt); err != nil {
		return ManualTopUpResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE idempotency_keys
SET response_ref = $1
WHERE service = 'ledger.manual_topups' AND idempotency_key = $2
`, topup.ID, input.IdempotencyKey); err != nil {
		return ManualTopUpResult{}, err
	}

	result := ManualTopUpResult{TopUp: topup, LedgerEntry: entry, WalletTransaction: walletTx, Wallet: wallet}
	return result, tx.Commit()
}

func (s *PostgresStore) Wallet(ctx context.Context, accountID string) (Wallet, error) {
	wallet := Wallet{}
	err := s.db.QueryRowContext(ctx, `
	SELECT account_id, balance_cents, frozen_cents, balance_cents - frozen_cents, total_spent_cents, currency, updated_at
	FROM wallets
	WHERE account_id = $1
	`, accountID).Scan(&wallet.AccountID, &wallet.BalanceCents, &wallet.FrozenCents, &wallet.AvailableCents, &wallet.TotalSpentCents, &wallet.Currency, &wallet.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Wallet{AccountID: accountID, Currency: "CNY"}, nil
	}
	return wallet, err
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

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return HoldResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	existing, existingHash, err := s.holdByIdempotencyKey(ctx, tx, input.IdempotencyKey)
	if err == nil {
		if existingHash != requestHash {
			return HoldResult{}, ErrIdempotencyConflict
		}
		existing.Replayed = true
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return HoldResult{}, err
	}

	now := s.now()
	wallet := Wallet{}
	if err := tx.QueryRowContext(ctx, `
	INSERT INTO wallets(account_id, balance_cents, frozen_cents, total_spent_cents, currency, updated_at)
	VALUES ($1, 0, 0, 0, $2, $3)
	ON CONFLICT (account_id) DO UPDATE SET
	  frozen_cents = wallets.frozen_cents + $4,
	  currency = EXCLUDED.currency,
	  updated_at = EXCLUDED.updated_at
	WHERE wallets.balance_cents - wallets.frozen_cents >= $4
	RETURNING account_id, balance_cents, frozen_cents, balance_cents - frozen_cents, total_spent_cents, currency, updated_at
	`, input.AccountID, input.Currency, now, input.AmountCents).Scan(
		&wallet.AccountID,
		&wallet.BalanceCents,
		&wallet.FrozenCents,
		&wallet.AvailableCents,
		&wallet.TotalSpentCents,
		&wallet.Currency,
		&wallet.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return HoldResult{}, ErrInsufficientBalance
		}
		return HoldResult{}, err
	}

	entry := LedgerEntry{ID: postgresID("le", now), AccountID: input.AccountID, AmountCents: input.AmountCents, Currency: input.Currency, Direction: "hold", Source: input.ResourceType + "_hold", Reason: input.ResourceID, CreatedAt: now}
	if _, err := tx.ExecContext(ctx, `
	INSERT INTO ledger_entries(id, account_id, amount_cents, currency, direction, source, operator_user_id, reason, created_at)
	VALUES ($1, $2, $3, $4, $5, $6, '', $7, $8)
	`, entry.ID, entry.AccountID, entry.AmountCents, entry.Currency, entry.Direction, entry.Source, entry.Reason, entry.CreatedAt); err != nil {
		return HoldResult{}, err
	}

	walletTx := WalletTransaction{ID: postgresID("wtx", now.Add(time.Nanosecond)), AccountID: input.AccountID, LedgerEntryID: entry.ID, AmountCents: input.AmountCents, BalanceCents: wallet.BalanceCents, Currency: input.Currency, CreatedAt: now}
	if _, err := tx.ExecContext(ctx, `
	INSERT INTO wallet_transactions(id, account_id, ledger_entry_id, amount_cents, balance_cents, currency, created_at)
	VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, walletTx.ID, walletTx.AccountID, walletTx.LedgerEntryID, walletTx.AmountCents, walletTx.BalanceCents, walletTx.Currency, walletTx.CreatedAt); err != nil {
		return HoldResult{}, err
	}

	result := HoldResult{ID: postgresID("hold", now.Add(2*time.Nanosecond)), AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: input.ResourceType, ResourceID: input.ResourceID, AmountCents: input.AmountCents, Currency: input.Currency, Status: "held", LedgerEntryID: entry.ID, WalletTransactionID: walletTx.ID, Wallet: wallet, CreatedAt: now}
	if _, err := tx.ExecContext(ctx, `
	INSERT INTO holds(id, account_id, workspace_id, resource_type, resource_id, amount_cents, currency, status, ledger_entry_id, wallet_transaction_id, idempotency_key, request_hash, created_at)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`, result.ID, result.AccountID, result.WorkspaceID, result.ResourceType, result.ResourceID, result.AmountCents, result.Currency, result.Status, result.LedgerEntryID, result.WalletTransactionID, input.IdempotencyKey, requestHash, result.CreatedAt); err != nil {
		return HoldResult{}, err
	}
	return result, tx.Commit()
}

func (s *PostgresStore) ReleaseHold(ctx context.Context, input HoldReleaseInput) (HoldReleaseResult, error) {
	requestHash, err := hashHoldRelease(input)
	if err != nil {
		return HoldReleaseResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return HoldReleaseResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	existing, existingHash, err := s.holdReleaseByIdempotencyKey(ctx, tx, input.IdempotencyKey)
	if err == nil {
		if existingHash != requestHash {
			return HoldReleaseResult{}, ErrIdempotencyConflict
		}
		existing.Replayed = true
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return HoldReleaseResult{}, err
	}

	now := s.now()
	wallet := Wallet{}
	if err := tx.QueryRowContext(ctx, `
	UPDATE wallets
	SET
	  frozen_cents = frozen_cents - $2,
	  currency = $3,
	  updated_at = $4
	WHERE account_id = $1 AND frozen_cents >= $2
	RETURNING account_id, balance_cents, frozen_cents, balance_cents - frozen_cents, total_spent_cents, currency, updated_at
	`, input.AccountID, input.AmountCents, input.Currency, now).Scan(&wallet.AccountID, &wallet.BalanceCents, &wallet.FrozenCents, &wallet.AvailableCents, &wallet.TotalSpentCents, &wallet.Currency, &wallet.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return HoldReleaseResult{}, ErrInsufficientFrozen
		}
		return HoldReleaseResult{}, err
	}

	entry := LedgerEntry{ID: postgresID("le", now), AccountID: input.AccountID, AmountCents: input.AmountCents, Currency: input.Currency, Direction: "release", Source: input.ResourceType + "_hold_released", Reason: input.Reason, CreatedAt: now}
	if _, err := tx.ExecContext(ctx, `
	INSERT INTO ledger_entries(id, account_id, amount_cents, currency, direction, source, operator_user_id, reason, created_at)
	VALUES ($1, $2, $3, $4, $5, $6, '', $7, $8)
	`, entry.ID, entry.AccountID, entry.AmountCents, entry.Currency, entry.Direction, entry.Source, entry.Reason, entry.CreatedAt); err != nil {
		return HoldReleaseResult{}, err
	}
	walletTx := WalletTransaction{ID: postgresID("wtx", now.Add(time.Nanosecond)), AccountID: input.AccountID, LedgerEntryID: entry.ID, AmountCents: 0, BalanceCents: wallet.BalanceCents, Currency: input.Currency, CreatedAt: now}
	if _, err := tx.ExecContext(ctx, `
	INSERT INTO wallet_transactions(id, account_id, ledger_entry_id, amount_cents, balance_cents, currency, created_at)
	VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, walletTx.ID, walletTx.AccountID, walletTx.LedgerEntryID, walletTx.AmountCents, walletTx.BalanceCents, walletTx.Currency, walletTx.CreatedAt); err != nil {
		return HoldReleaseResult{}, err
	}

	result := HoldReleaseResult{ID: postgresID("hrel", now.Add(2*time.Nanosecond)), AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: input.ResourceType, ResourceID: input.ResourceID, HoldID: input.HoldID, AmountCents: input.AmountCents, Currency: input.Currency, Status: "released", LedgerEntryID: entry.ID, WalletTransactionID: walletTx.ID, Wallet: wallet, CreatedAt: now}
	if _, err := tx.ExecContext(ctx, `
	INSERT INTO hold_releases(id, account_id, workspace_id, resource_type, resource_id, hold_id, amount_cents, currency, status, ledger_entry_id, wallet_transaction_id, idempotency_key, request_hash, created_at)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`, result.ID, result.AccountID, result.WorkspaceID, result.ResourceType, result.ResourceID, result.HoldID, result.AmountCents, result.Currency, result.Status, result.LedgerEntryID, result.WalletTransactionID, input.IdempotencyKey, requestHash, result.CreatedAt); err != nil {
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return EvidenceReceipt{}, err
	}
	defer func() { _ = tx.Rollback() }()

	existing, existingHash, err := s.evidenceByIdempotencyKey(ctx, tx, input.IdempotencyKey)
	if err == nil {
		if existingHash != requestHash {
			return EvidenceReceipt{}, ErrIdempotencyConflict
		}
		existing.Replayed = true
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return EvidenceReceipt{}, err
	}

	now := s.now()
	receipt := EvidenceReceipt{ID: postgresID("ev", now), WorkspaceID: input.WorkspaceID, ProviderRequestID: input.ProviderRequestID, RedactedURL: input.RedactedURL, TokenVersion: input.TokenVersion, CreatedAt: now}
	if _, err := tx.ExecContext(ctx, `
	INSERT INTO evidence_receipts(id, workspace_id, provider_request_id, redacted_url, token_version, idempotency_key, request_hash, created_at)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, receipt.ID, receipt.WorkspaceID, receipt.ProviderRequestID, receipt.RedactedURL, receipt.TokenVersion, input.IdempotencyKey, requestHash, receipt.CreatedAt); err != nil {
		return EvidenceReceipt{}, err
	}
	return receipt, tx.Commit()
}

func (s *PostgresStore) SettleResource(ctx context.Context, input ResourceSettlementInput) (ResourceSettlementResult, error) {
	requestHash, err := hashJSON(struct {
		AccountID    string `json:"accountId"`
		WorkspaceID  string `json:"workspaceId"`
		ResourceType string `json:"resourceType"`
		ResourceID   string `json:"resourceId"`
		AmountCents  int64  `json:"amountCents"`
		Currency     string `json:"currency"`
	}{input.AccountID, input.WorkspaceID, input.ResourceType, input.ResourceID, input.AmountCents, input.Currency})
	if err != nil {
		return ResourceSettlementResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ResourceSettlementResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	existing, existingHash, err := s.settlementByIdempotencyKey(ctx, tx, input.IdempotencyKey)
	if err == nil {
		if existingHash != requestHash {
			return ResourceSettlementResult{}, ErrIdempotencyConflict
		}
		existing.Replayed = true
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ResourceSettlementResult{}, err
	}

	now := s.now()
	wallet := Wallet{}
	if err := tx.QueryRowContext(ctx, `
	UPDATE wallets
	SET
	  balance_cents = balance_cents - $2,
	  frozen_cents = GREATEST(0, frozen_cents - $2),
	  total_spent_cents = total_spent_cents + $2,
	  currency = $3,
	  updated_at = $4
	WHERE account_id = $1 AND balance_cents >= $2
	RETURNING account_id, balance_cents, frozen_cents, balance_cents - frozen_cents, total_spent_cents, currency, updated_at
	`, input.AccountID, input.AmountCents, input.Currency, now).Scan(&wallet.AccountID, &wallet.BalanceCents, &wallet.FrozenCents, &wallet.AvailableCents, &wallet.TotalSpentCents, &wallet.Currency, &wallet.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ResourceSettlementResult{}, ErrInsufficientBalance
		}
		return ResourceSettlementResult{}, err
	}

	entry := LedgerEntry{ID: postgresID("le", now), AccountID: input.AccountID, AmountCents: input.AmountCents, Currency: input.Currency, Direction: "debit", Source: input.ResourceType + "_settlement", Reason: input.WorkspaceID, CreatedAt: now}
	if _, err := tx.ExecContext(ctx, `
	INSERT INTO ledger_entries(id, account_id, amount_cents, currency, direction, source, operator_user_id, reason, created_at)
	VALUES ($1, $2, $3, $4, $5, $6, '', $7, $8)
	`, entry.ID, entry.AccountID, entry.AmountCents, entry.Currency, entry.Direction, entry.Source, entry.Reason, entry.CreatedAt); err != nil {
		return ResourceSettlementResult{}, err
	}
	walletTx := WalletTransaction{ID: postgresID("wtx", now.Add(time.Nanosecond)), AccountID: input.AccountID, LedgerEntryID: entry.ID, AmountCents: -input.AmountCents, BalanceCents: wallet.BalanceCents, Currency: input.Currency, CreatedAt: now}
	if _, err := tx.ExecContext(ctx, `
	INSERT INTO wallet_transactions(id, account_id, ledger_entry_id, amount_cents, balance_cents, currency, created_at)
	VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, walletTx.ID, walletTx.AccountID, walletTx.LedgerEntryID, walletTx.AmountCents, walletTx.BalanceCents, walletTx.Currency, walletTx.CreatedAt); err != nil {
		return ResourceSettlementResult{}, err
	}

	result := ResourceSettlementResult{ID: postgresID("settle", now.Add(2*time.Nanosecond)), AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: input.ResourceType, ResourceID: input.ResourceID, AmountCents: input.AmountCents, Currency: input.Currency, Status: "settled", LedgerEntryID: entry.ID, WalletTransactionID: walletTx.ID, Wallet: wallet, CreatedAt: now}
	if _, err := tx.ExecContext(ctx, `
	INSERT INTO resource_settlements(id, account_id, workspace_id, resource_type, resource_id, amount_cents, currency, status, ledger_entry_id, wallet_transaction_id, idempotency_key, request_hash, created_at)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`, result.ID, result.AccountID, result.WorkspaceID, result.ResourceType, result.ResourceID, result.AmountCents, result.Currency, result.Status, result.LedgerEntryID, result.WalletTransactionID, input.IdempotencyKey, requestHash, result.CreatedAt); err != nil {
		return ResourceSettlementResult{}, err
	}
	return result, tx.Commit()
}

func (s *PostgresStore) RecordReconciliation(ctx context.Context, input ReconciliationInput) (ReconciliationResult, error) {
	requestHash, err := hashJSON(input.Report)
	if err != nil {
		return ReconciliationResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReconciliationResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	existing, existingHash, err := s.reconciliationByIdempotencyKey(ctx, tx, input.IdempotencyKey)
	if err == nil {
		if existingHash != requestHash {
			return ReconciliationResult{}, ErrIdempotencyConflict
		}
		existing.Replayed = true
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
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
	if _, err := tx.ExecContext(ctx, `
	INSERT INTO reconciliation_reports(id, status, report_json, block_new_workspaces, reason, idempotency_key, request_hash, created_at)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, result.ID, result.Status, string(reportJSON), result.BlockNewWorkspaces, result.Reason, input.IdempotencyKey, requestHash, result.CreatedAt); err != nil {
		return ReconciliationResult{}, err
	}
	return result, tx.Commit()
}

func (s *PostgresStore) manualTopUpByIdempotencyKey(ctx context.Context, tx *sql.Tx, key string) (ManualTopUpResult, string, error) {
	result := ManualTopUpResult{}
	var requestHash string
	err := tx.QueryRowContext(ctx, `
SELECT
	  mt.id, mt.account_id, mt.amount_cents, mt.currency, mt.operator_user_id, mt.ledger_entry_id, mt.reason, mt.created_at, mt.request_hash,
	  le.id, le.account_id, le.amount_cents, le.currency, le.direction, le.source, le.operator_user_id, le.reason, le.created_at,
	  wt.id, wt.account_id, wt.ledger_entry_id, wt.amount_cents, wt.balance_cents, wt.currency, wt.created_at,
	  w.account_id, w.balance_cents, w.frozen_cents, w.balance_cents - w.frozen_cents, w.total_spent_cents, w.currency, w.updated_at
FROM manual_topups mt
JOIN ledger_entries le ON le.id = mt.ledger_entry_id
JOIN wallet_transactions wt ON wt.id = mt.wallet_transaction_id
JOIN wallets w ON w.account_id = mt.account_id
WHERE mt.idempotency_key = $1
`, key).Scan(
		&result.TopUp.ID,
		&result.TopUp.AccountID,
		&result.TopUp.AmountCents,
		&result.TopUp.Currency,
		&result.TopUp.OperatorUserID,
		&result.TopUp.LedgerEntryID,
		&result.TopUp.Reason,
		&result.TopUp.CreatedAt,
		&requestHash,
		&result.LedgerEntry.ID,
		&result.LedgerEntry.AccountID,
		&result.LedgerEntry.AmountCents,
		&result.LedgerEntry.Currency,
		&result.LedgerEntry.Direction,
		&result.LedgerEntry.Source,
		&result.LedgerEntry.OperatorUserID,
		&result.LedgerEntry.Reason,
		&result.LedgerEntry.CreatedAt,
		&result.WalletTransaction.ID,
		&result.WalletTransaction.AccountID,
		&result.WalletTransaction.LedgerEntryID,
		&result.WalletTransaction.AmountCents,
		&result.WalletTransaction.BalanceCents,
		&result.WalletTransaction.Currency,
		&result.WalletTransaction.CreatedAt,
		&result.Wallet.AccountID,
		&result.Wallet.BalanceCents,
		&result.Wallet.FrozenCents,
		&result.Wallet.AvailableCents,
		&result.Wallet.TotalSpentCents,
		&result.Wallet.Currency,
		&result.Wallet.UpdatedAt,
	)
	return result, requestHash, err
}

func (s *PostgresStore) holdByIdempotencyKey(ctx context.Context, tx *sql.Tx, key string) (HoldResult, string, error) {
	result := HoldResult{}
	var requestHash string
	err := tx.QueryRowContext(ctx, `
	SELECT
	  h.id, h.account_id, h.workspace_id, h.resource_type, h.resource_id, h.amount_cents, h.currency, h.status, h.ledger_entry_id, h.wallet_transaction_id, h.created_at, h.request_hash,
	  w.account_id, w.balance_cents, w.frozen_cents, w.balance_cents - w.frozen_cents, w.total_spent_cents, w.currency, w.updated_at
	FROM holds h
	JOIN wallets w ON w.account_id = h.account_id
	WHERE h.idempotency_key = $1
	`, key).Scan(
		&result.ID,
		&result.AccountID,
		&result.WorkspaceID,
		&result.ResourceType,
		&result.ResourceID,
		&result.AmountCents,
		&result.Currency,
		&result.Status,
		&result.LedgerEntryID,
		&result.WalletTransactionID,
		&result.CreatedAt,
		&requestHash,
		&result.Wallet.AccountID,
		&result.Wallet.BalanceCents,
		&result.Wallet.FrozenCents,
		&result.Wallet.AvailableCents,
		&result.Wallet.TotalSpentCents,
		&result.Wallet.Currency,
		&result.Wallet.UpdatedAt,
	)
	return result, requestHash, err
}

func (s *PostgresStore) evidenceByIdempotencyKey(ctx context.Context, tx *sql.Tx, key string) (EvidenceReceipt, string, error) {
	result := EvidenceReceipt{}
	var requestHash string
	err := tx.QueryRowContext(ctx, `
	SELECT id, workspace_id, provider_request_id, redacted_url, token_version, created_at, request_hash
	FROM evidence_receipts
	WHERE idempotency_key = $1
	`, key).Scan(
		&result.ID,
		&result.WorkspaceID,
		&result.ProviderRequestID,
		&result.RedactedURL,
		&result.TokenVersion,
		&result.CreatedAt,
		&requestHash,
	)
	return result, requestHash, err
}

func (s *PostgresStore) settlementByIdempotencyKey(ctx context.Context, tx *sql.Tx, key string) (ResourceSettlementResult, string, error) {
	result := ResourceSettlementResult{}
	var requestHash string
	err := tx.QueryRowContext(ctx, `
	SELECT
	  rs.id, rs.account_id, rs.workspace_id, rs.resource_type, rs.resource_id, rs.amount_cents, rs.currency, rs.status, rs.ledger_entry_id, rs.wallet_transaction_id, rs.created_at, rs.request_hash,
	  w.account_id, w.balance_cents, w.frozen_cents, w.balance_cents - w.frozen_cents, w.total_spent_cents, w.currency, w.updated_at
	FROM resource_settlements rs
	JOIN wallets w ON w.account_id = rs.account_id
	WHERE rs.idempotency_key = $1
	`, key).Scan(
		&result.ID,
		&result.AccountID,
		&result.WorkspaceID,
		&result.ResourceType,
		&result.ResourceID,
		&result.AmountCents,
		&result.Currency,
		&result.Status,
		&result.LedgerEntryID,
		&result.WalletTransactionID,
		&result.CreatedAt,
		&requestHash,
		&result.Wallet.AccountID,
		&result.Wallet.BalanceCents,
		&result.Wallet.FrozenCents,
		&result.Wallet.AvailableCents,
		&result.Wallet.TotalSpentCents,
		&result.Wallet.Currency,
		&result.Wallet.UpdatedAt,
	)
	return result, requestHash, err
}

func (s *PostgresStore) holdReleaseByIdempotencyKey(ctx context.Context, tx *sql.Tx, key string) (HoldReleaseResult, string, error) {
	result := HoldReleaseResult{}
	var requestHash string
	err := tx.QueryRowContext(ctx, `
	SELECT
	  hr.id, hr.account_id, hr.workspace_id, hr.resource_type, hr.resource_id, hr.hold_id, hr.amount_cents, hr.currency, hr.status, hr.ledger_entry_id, hr.wallet_transaction_id, hr.created_at, hr.request_hash,
	  w.account_id, w.balance_cents, w.frozen_cents, w.balance_cents - w.frozen_cents, w.total_spent_cents, w.currency, w.updated_at
	FROM hold_releases hr
	JOIN wallets w ON w.account_id = hr.account_id
	WHERE hr.idempotency_key = $1
	`, key).Scan(
		&result.ID,
		&result.AccountID,
		&result.WorkspaceID,
		&result.ResourceType,
		&result.ResourceID,
		&result.HoldID,
		&result.AmountCents,
		&result.Currency,
		&result.Status,
		&result.LedgerEntryID,
		&result.WalletTransactionID,
		&result.CreatedAt,
		&requestHash,
		&result.Wallet.AccountID,
		&result.Wallet.BalanceCents,
		&result.Wallet.FrozenCents,
		&result.Wallet.AvailableCents,
		&result.Wallet.TotalSpentCents,
		&result.Wallet.Currency,
		&result.Wallet.UpdatedAt,
	)
	return result, requestHash, err
}

func (s *PostgresStore) reconciliationByIdempotencyKey(ctx context.Context, tx *sql.Tx, key string) (ReconciliationResult, string, error) {
	result := ReconciliationResult{}
	var requestHash string
	var reportJSON string
	err := tx.QueryRowContext(ctx, `
	SELECT id, status, report_json, block_new_workspaces, reason, created_at, request_hash
	FROM reconciliation_reports
	WHERE idempotency_key = $1
	`, key).Scan(
		&result.ID,
		&result.Status,
		&reportJSON,
		&result.BlockNewWorkspaces,
		&result.Reason,
		&result.CreatedAt,
		&requestHash,
	)
	if err == nil {
		_ = json.Unmarshal([]byte(reportJSON), &result.Report)
	}
	return result, requestHash, err
}

func postgresID(prefix string, t time.Time) string {
	return fmt.Sprintf("%s_%d", prefix, t.UnixNano())
}
