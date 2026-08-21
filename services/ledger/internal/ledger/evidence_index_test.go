package ledger

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func validEvidenceIndexInput() EvidenceIndexInput {
	return EvidenceIndexInput{
		OperationID:    "launch-alpha",
		CandidateSHA:   strings.Repeat("a", 40),
		CandidateTree:  strings.Repeat("b", 40),
		ImageDigest:    "sha256:" + strings.Repeat("c", 64),
		ReceiptID:      "receipt-alpha",
		ReceiptType:    "candidate.qualified.v1",
		Status:         "admission_ready",
		Actor:          "instance-medopl",
		ObservedAt:     time.Date(2026, 8, 21, 4, 0, 0, 0, time.UTC),
		IdentityDigest: "sha256:" + strings.Repeat("d", 64),
		RedactedLink:   "https://example.invalid/evidence/alpha",
		IdempotencyKey: "launch-alpha:instance-admission",
	}
}

func TestEvidenceIndexAppendOnlyIdempotencyAndExactCandidateQuery(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	input := validEvidenceIndexInput()
	entry, err := store.RecordEvidenceIndex(ctx, input)
	if err != nil {
		t.Fatalf("RecordEvidenceIndex() error = %v", err)
	}
	if entry.EvidenceID == "" || entry.Replayed || entry.IdempotencyKey != "" {
		t.Fatalf("unexpected first entry: %#v", entry)
	}
	replay, err := store.RecordEvidenceIndex(ctx, input)
	if err != nil || !replay.Replayed || replay.EvidenceID != entry.EvidenceID {
		t.Fatalf("replay = %#v error=%v", replay, err)
	}
	conflict := input
	conflict.Status = "blocked"
	if _, err := store.RecordEvidenceIndex(ctx, conflict); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting replay error = %v, want ErrIdempotencyConflict", err)
	}
	second := input
	second.OperationID = "launch-beta"
	second.ReceiptID = "receipt-beta"
	second.IdempotencyKey = "launch-beta:instance-admission"
	second.ObservedAt = input.ObservedAt.Add(time.Minute)
	if _, err := store.RecordEvidenceIndex(ctx, second); err != nil {
		t.Fatalf("second evidence error = %v", err)
	}
	page, err := store.ListEvidenceIndex(ctx, EvidenceIndexQuery{OperationID: input.OperationID, CandidateSHA: input.CandidateSHA, CandidateTree: input.CandidateTree, ImageDigest: input.ImageDigest})
	if err != nil || len(page.Entries) != 1 || page.Entries[0].EvidenceID != entry.EvidenceID {
		t.Fatalf("exact candidate query = %#v error=%v", page, err)
	}
	export, err := store.ExportEvidenceIndex(ctx, EvidenceIndexQuery{CandidateSHA: input.CandidateSHA})
	if err != nil || export.SchemaVersion != 1 || len(export.Entries) != 2 {
		t.Fatalf("export = %#v error=%v", export, err)
	}
	if export.Entries[0].IdempotencyKey != "" || export.Entries[0].RedactedLink != input.RedactedLink {
		t.Fatalf("export leaked or changed redacted fields: %#v", export.Entries[0])
	}
}

func TestEvidenceIndexRejectsUnredactedOrIncompleteFacts(t *testing.T) {
	base := validEvidenceIndexInput()
	for _, tc := range []struct {
		name   string
		mutate func(*EvidenceIndexInput)
	}{
		{name: "missing operation", mutate: func(input *EvidenceIndexInput) { input.OperationID = "" }},
		{name: "missing candidate", mutate: func(input *EvidenceIndexInput) { input.CandidateTree = "" }},
		{name: "missing receipt", mutate: func(input *EvidenceIndexInput) { input.ReceiptID = "" }},
		{name: "missing observed time", mutate: func(input *EvidenceIndexInput) { input.ObservedAt = time.Time{} }},
		{name: "query in link", mutate: func(input *EvidenceIndexInput) { input.RedactedLink = "https://example.invalid/evidence?token=bad" }},
		{name: "link userinfo", mutate: func(input *EvidenceIndexInput) { input.RedactedLink = "https://secret@example.invalid/evidence" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := base
			input.IdempotencyKey += ":" + strings.ReplaceAll(tc.name, " ", "-")
			tc.mutate(&input)
			if _, err := NewMemoryStore().RecordEvidenceIndex(context.Background(), input); !errors.Is(err, ErrInvalidEvidenceIndexInput) {
				t.Fatalf("error = %v, want ErrInvalidEvidenceIndexInput", err)
			}
		})
	}
}

func TestEvidenceIndexSchemaContainsOwnerBoundedTable(t *testing.T) {
	schema := PostgresSchemaSQL()
	for _, marker := range []string{
		"CREATE TABLE IF NOT EXISTS evidence_index_entries",
		"operation_id TEXT NOT NULL",
		"candidate_tree TEXT NOT NULL",
		"image_digest TEXT NOT NULL",
		"identity_digest TEXT NOT NULL",
		"evidence_index_entries_operation_observed",
		"evidence_index_entries_candidate_observed",
	} {
		if !strings.Contains(schema, marker) {
			t.Fatalf("schema missing %q", marker)
		}
	}
}
