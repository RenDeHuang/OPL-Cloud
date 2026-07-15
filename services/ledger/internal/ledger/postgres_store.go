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

	"opl-cloud/services/internal/postgresmigrate"
	ledgerent "opl-cloud/services/ledger/ent"
	"opl-cloud/services/ledger/ent/evidencereceipt"
	"opl-cloud/services/ledger/ent/predicate"
	"opl-cloud/services/ledger/ent/reconciliationreport"
	"opl-cloud/services/ledger/ent/reviewpolicy"
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
	finalizeReceiptContinuation(&receipt)
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
	input.AccountID = row.AccountID
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

func (s *PostgresStore) reconciliationByIdempotencyKey(ctx context.Context, key string) (ReconciliationResult, string, error) {
	row, err := s.client.ReconciliationReport.Query().Where(reconciliationreport.IdempotencyKey(key)).Only(ctx)
	if err != nil {
		return ReconciliationResult{}, "", err
	}
	result := ReconciliationResult{ID: row.ID, Status: row.Status, BlockNewWorkspaces: row.BlockNewWorkspaces, Reason: row.Reason, CreatedAt: row.CreatedAt}
	_ = json.Unmarshal([]byte(row.ReportJSON), &result.Report)
	return result, row.RequestHash, nil
}

func postgresID(prefix string, t time.Time) string {
	return fmt.Sprintf("%s_%d", prefix, t.UnixNano())
}

var _ = errors.Is
