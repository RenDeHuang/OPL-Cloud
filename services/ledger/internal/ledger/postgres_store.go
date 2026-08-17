package ledger

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"opl-cloud/services/internal/postgresmigrate"
	ledgerent "opl-cloud/services/ledger/ent"
	"opl-cloud/services/ledger/ent/evidencereceipt"
	"opl-cloud/services/ledger/ent/reconciliationreport"
)

//go:embed ent_migrations/*.sql
var ledgerMigrations embed.FS

type embeddedMigration struct {
	version string
	query   string
}

type PostgresStore struct {
	client *ledgerent.Client
	db     *sql.DB
	now    func() time.Time
}

func ledgerEmbeddedMigrations() ([]embeddedMigration, error) {
	entries, err := ledgerMigrations.ReadDir("ent_migrations")
	if err != nil {
		return nil, err
	}
	migrations := make([]embeddedMigration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		data, err := ledgerMigrations.ReadFile("ent_migrations/" + entry.Name())
		if err != nil {
			return nil, err
		}
		migrations = append(migrations, embeddedMigration{
			version: strings.TrimSuffix(entry.Name(), ".sql"),
			query:   string(data),
		})
	}
	return migrations, nil
}

func PostgresSchemaSQL() string {
	migrations, err := ledgerEmbeddedMigrations()
	if err != nil {
		return ""
	}
	var out strings.Builder
	for _, migration := range migrations {
		out.WriteString(migration.query)
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

func (s *PostgresStore) Ready(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *PostgresStore) Install(ctx context.Context) error {
	embedded, err := ledgerEmbeddedMigrations()
	if err != nil {
		return err
	}
	migrations := make([]postgresmigrate.Migration, 0, len(embedded))
	for _, migration := range embedded {
		migration := migration
		migrations = append(migrations, postgresmigrate.Migration{
			Version: migration.version,
			Run: func(ctx context.Context) error {
				_, err := s.db.ExecContext(ctx, migration.query)
				return err
			},
		})
	}
	return postgresmigrate.Apply(ctx, s.db, "ledger", migrations)
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
	payload, err := json.Marshal(receiptPayload{ReceiptInput: receipt.ReceiptInput, Retention: receipt.Retention})
	if err != nil {
		return Receipt{}, err
	}
	if err := s.client.EvidenceReceipt.Create().
		SetID(receipt.ReceiptID).
		SetReceiptType(receipt.Type).
		SetStatus(receipt.Status).
		SetAccountID(receipt.AccountID).
		SetOrganizationID(receipt.OrganizationID).
		SetWorkspaceID(receipt.WorkspaceID).
		SetProjectID(receipt.ProjectID).
		SetTaskID(receipt.TaskID).
		SetJobID(receipt.JobID).
		SetArtifactID(receipt.ArtifactID).
		SetReviewID(receipt.ReviewID).
		SetPayloadJSON(string(payload)).
		SetSupersedesReceiptID(receipt.SupersedesReceiptID).
		SetIdempotencyKey(input.IdempotencyKey).
		SetRequestHash(requestHash).
		SetCreatedAt(receipt.CreatedAt).
		Exec(ctx); err != nil {
		if ledgerent.IsConstraintError(err) {
			if existing, existingHash, replayErr := s.receiptByIdempotencyKey(ctx, input.IdempotencyKey); replayErr == nil {
				if existingHash != requestHash {
					return Receipt{}, ErrIdempotencyConflict
				}
				existing.Replayed = true
				return existing, nil
			}
		}
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
	if query.AccountID != "" {
		q = q.Where(evidencereceipt.AccountID(query.AccountID))
	}
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
	if query.TypePrefix != "" {
		q = q.Where(evidencereceipt.ReceiptTypeHasPrefix(query.TypePrefix))
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
		receipt, err := receiptFromEnt(row)
		if err != nil {
			return ReceiptPage{}, err
		}
		receipts = append(receipts, receipt)
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
	return receipt, nil
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
	if err := decodeStoredJSON(payloadJSON, &stored); err != nil {
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
	return receiptFromEnt(row)
}

func (s *PostgresStore) RecordReconciliation(ctx context.Context, input ReconciliationInput) (ReconciliationResult, error) {
	if err := validateReconciliationInput(input); err != nil {
		return ReconciliationResult{}, err
	}
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
	status := stringFromAny(input.Report["status"])
	reportJSON, err := json.Marshal(input.Report)
	if err != nil {
		return ReconciliationResult{}, err
	}
	now := s.now()
	result := ReconciliationResult{ID: id, Status: status, Report: input.Report, BlockNewWorkspaces: status == "mismatch", Reason: "operator_reconciliation", CreatedAt: now}
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
		if ledgerent.IsConstraintError(err) {
			if existing, existingHash, replayErr := s.reconciliationByIdempotencyKey(ctx, input.IdempotencyKey); replayErr == nil {
				if existingHash != requestHash {
					return ReconciliationResult{}, ErrIdempotencyConflict
				}
				existing.Replayed = true
				return existing, nil
			}
		}
		return ReconciliationResult{}, err
	}
	return result, nil
}

func (s *PostgresStore) receiptByIdempotencyKey(ctx context.Context, key string) (Receipt, string, error) {
	row, err := s.client.EvidenceReceipt.Query().Where(evidencereceipt.IdempotencyKey(key)).Only(ctx)
	if err != nil {
		return Receipt{}, "", err
	}
	receipt, err := receiptFromEnt(row)
	return receipt, row.RequestHash, err
}

func receiptFromEnt(row *ledgerent.EvidenceReceipt) (Receipt, error) {
	// The payload remains canonical for flexible evidence; indexed identity columns override its projections.
	var stored receiptPayload
	if err := decodeStoredJSON(row.PayloadJSON, &stored); err != nil {
		return Receipt{}, err
	}
	input := stored.ReceiptInput
	input.Type = row.ReceiptType
	input.Status = row.Status
	input.AccountID = row.AccountID
	input.OrganizationID = row.OrganizationID
	input.WorkspaceID = row.WorkspaceID
	input.ProjectID = row.ProjectID
	input.TaskID = row.TaskID
	input.JobID = row.JobID
	input.ArtifactID = row.ArtifactID
	input.ReviewID = row.ReviewID
	input.SupersedesReceiptID = row.SupersedesReceiptID
	return Receipt{ReceiptInput: input, ReceiptID: row.ID, CreatedAt: row.CreatedAt, Retention: stored.Retention}, nil
}

type receiptPayload struct {
	ReceiptInput
	Retention ReceiptRetention `json:"retention"`
}

func (s *PostgresStore) reconciliationByIdempotencyKey(ctx context.Context, key string) (ReconciliationResult, string, error) {
	row, err := s.client.ReconciliationReport.Query().Where(reconciliationreport.IdempotencyKey(key)).Only(ctx)
	if err != nil {
		return ReconciliationResult{}, "", err
	}
	result, err := reconciliationFromEnt(row)
	return result, row.RequestHash, err
}

func reconciliationFromEnt(row *ledgerent.ReconciliationReport) (ReconciliationResult, error) {
	result := ReconciliationResult{ID: row.ID, Status: row.Status, BlockNewWorkspaces: row.BlockNewWorkspaces, Reason: row.Reason, CreatedAt: row.CreatedAt}
	if err := decodeStoredJSON(row.ReportJSON, &result.Report); err != nil || validateReconciliationResult(result) != nil {
		return ReconciliationResult{}, ErrInvalidReconciliationInput
	}
	return result, nil
}

func decodeStoredJSON(payload string, target any) error {
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err != nil {
			return err
		}
		return errors.New("stored JSON contains multiple values")
	}
	return nil
}

func postgresID(prefix string, t time.Time) string {
	return fmt.Sprintf("%s_%d", prefix, t.UnixNano())
}

var _ = errors.Is
