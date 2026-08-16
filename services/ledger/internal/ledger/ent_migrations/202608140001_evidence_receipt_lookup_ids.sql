ALTER TABLE evidence_receipts ADD COLUMN IF NOT EXISTS artifact_id TEXT NOT NULL DEFAULT '';
ALTER TABLE evidence_receipts ADD COLUMN IF NOT EXISTS review_id TEXT NOT NULL DEFAULT '';

UPDATE evidence_receipts
SET artifact_id = COALESCE(payload_json::jsonb ->> 'artifactId', '')
WHERE artifact_id = '' AND receipt_type = 'artifact.manifest.v1';

UPDATE evidence_receipts
SET review_id = COALESCE(payload_json::jsonb ->> 'reviewId', '')
WHERE review_id = '' AND receipt_type = 'review.result.v1';

CREATE INDEX IF NOT EXISTS evidence_receipts_artifact_id ON evidence_receipts (artifact_id);
CREATE INDEX IF NOT EXISTS evidence_receipts_review_id ON evidence_receipts (review_id);
