package ledger

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"
	"time"
)

var (
	ErrInvalidEvidenceIndexInput   = errors.New("invalid evidence index input")
	ErrInvalidEvidenceIndexQuery   = errors.New("invalid evidence index query")
	ErrEvidenceIndexExportTooLarge = errors.New("evidence index export exceeds the bounded limit")
)

const (
	DefaultEvidenceIndexPageSize = 100
	MaxEvidenceIndexPageSize     = 500
	MaxEvidenceIndexExportSize   = 5000
)

// EvidenceIndexStore is intentionally separate from Store. The index is a
// cross-surface evidence lookup, not a second receipt or domain-state API.
type EvidenceIndexStore interface {
	RecordEvidenceIndex(context.Context, EvidenceIndexInput) (EvidenceIndexEntry, error)
	ListEvidenceIndex(context.Context, EvidenceIndexQuery) (EvidenceIndexPage, error)
	ExportEvidenceIndex(context.Context, EvidenceIndexQuery) (EvidenceIndexExport, error)
}

// EvidenceIndexInput contains only owner-supplied, non-secret identity facts.
// Source receipts and service state remain in their owning stores.
type EvidenceIndexInput struct {
	OperationID    string    `json:"operationId"`
	CandidateSHA   string    `json:"candidateSha"`
	CandidateTree  string    `json:"candidateTree"`
	ImageDigest    string    `json:"imageDigest"`
	ReceiptID      string    `json:"receiptId"`
	ReceiptType    string    `json:"receiptType"`
	Status         string    `json:"status"`
	Actor          string    `json:"actor"`
	ObservedAt     time.Time `json:"observedAt"`
	IdentityDigest string    `json:"identityDigest"`
	RedactedLink   string    `json:"redactedLink,omitempty"`
	IdempotencyKey string    `json:"-"`
}

type EvidenceIndexEntry struct {
	EvidenceIndexInput
	EvidenceID string    `json:"evidenceId"`
	CreatedAt  time.Time `json:"createdAt"`
	Replayed   bool      `json:"replayed"`
}

type EvidenceIndexQuery struct {
	OperationID   string
	CandidateSHA  string
	CandidateTree string
	ImageDigest   string
	ReceiptID     string
	ReceiptType   string
	Status        string
	Cursor        string
	Limit         int
}

type EvidenceIndexPage struct {
	Entries    []EvidenceIndexEntry `json:"entries"`
	NextCursor string               `json:"nextCursor"`
	HasMore    bool                 `json:"hasMore"`
}

// EvidenceIndexExport is a stable, redacted projection for qualification and
// future migration tooling. Idempotency keys are deliberately never exported.
type EvidenceIndexExport struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Entries       []EvidenceIndexEntry `json:"entries"`
}

type evidenceIndexCursor struct {
	ObservedAt time.Time `json:"observedAt"`
	EvidenceID string    `json:"evidenceId"`
}

func normalizeEvidenceIndexQuery(query EvidenceIndexQuery) (EvidenceIndexQuery, evidenceIndexCursor, error) {
	if query.Limit == 0 {
		query.Limit = DefaultEvidenceIndexPageSize
	}
	if query.Limit < 1 || query.Limit > MaxEvidenceIndexPageSize {
		return EvidenceIndexQuery{}, evidenceIndexCursor{}, ErrInvalidEvidenceIndexQuery
	}
	if query.Cursor == "" {
		return query, evidenceIndexCursor{}, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(query.Cursor)
	if err != nil {
		return EvidenceIndexQuery{}, evidenceIndexCursor{}, ErrInvalidEvidenceIndexQuery
	}
	var cursor evidenceIndexCursor
	if json.Unmarshal(payload, &cursor) != nil || cursor.ObservedAt.IsZero() || cursor.EvidenceID == "" {
		return EvidenceIndexQuery{}, evidenceIndexCursor{}, ErrInvalidEvidenceIndexQuery
	}
	return query, cursor, nil
}

func encodeEvidenceIndexCursor(entry EvidenceIndexEntry) string {
	payload, _ := json.Marshal(evidenceIndexCursor{ObservedAt: entry.ObservedAt, EvidenceID: entry.EvidenceID})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func validateEvidenceIndexInput(input EvidenceIndexInput) error {
	for _, value := range []string{
		input.OperationID, input.CandidateSHA, input.CandidateTree, input.ImageDigest,
		input.ReceiptID, input.ReceiptType, input.Status, input.Actor,
		input.IdentityDigest, input.IdempotencyKey,
	} {
		if !isOpaqueReference(value) {
			return ErrInvalidEvidenceIndexInput
		}
	}
	if input.ObservedAt.IsZero() || input.ObservedAt.Location() == nil || input.ObservedAt.Year() < 2000 || input.ObservedAt.Year() > 2100 {
		return ErrInvalidEvidenceIndexInput
	}
	if input.RedactedLink != "" && !isRedactedEvidenceLink(input.RedactedLink) {
		return ErrInvalidEvidenceIndexInput
	}
	return nil
}

func isRedactedEvidenceLink(value string) bool {
	if len(value) > 2048 || strings.ContainsAny(value, "\r\n") || strings.ContainsAny(value, "?#") || strings.Contains(value, "@") {
		return false
	}
	if !strings.Contains(value, "://") {
		return isOpaqueReference(value)
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil {
		return false
	}
	if parsed.Scheme == "" {
		return isOpaqueReference(value)
	}
	return (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != ""
}

func sortEvidenceIndexEntries(entries []EvidenceIndexEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].ObservedAt.Equal(entries[j].ObservedAt) {
			return entries[i].EvidenceID > entries[j].EvidenceID
		}
		return entries[i].ObservedAt.After(entries[j].ObservedAt)
	})
}
