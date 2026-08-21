CREATE TABLE IF NOT EXISTS evidence_index_entries (
    id TEXT PRIMARY KEY,
    operation_id TEXT NOT NULL,
    candidate_sha TEXT NOT NULL,
    candidate_tree TEXT NOT NULL,
    image_digest TEXT NOT NULL,
    receipt_id TEXT NOT NULL,
    receipt_type TEXT NOT NULL,
    status TEXT NOT NULL,
    actor TEXT NOT NULL,
    observed_at TIMESTAMPTZ NOT NULL,
    identity_digest TEXT NOT NULL,
    redacted_link TEXT NOT NULL DEFAULT '',
    idempotency_key TEXT NOT NULL UNIQUE,
    request_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS evidence_index_entries_operation_observed
    ON evidence_index_entries (operation_id, observed_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS evidence_index_entries_candidate_observed
    ON evidence_index_entries (candidate_sha, candidate_tree, image_digest, observed_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS evidence_index_entries_receipt_observed
    ON evidence_index_entries (receipt_id, receipt_type, observed_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS evidence_index_entries_status_observed
    ON evidence_index_entries (status, observed_at DESC, id DESC);
