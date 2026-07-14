DROP TABLE IF EXISTS hold_activations;
DROP TABLE IF EXISTS hold_releases;
DROP TABLE IF EXISTS holds;
DROP TABLE IF EXISTS resource_settlements;
DROP TABLE IF EXISTS manual_topups;
DROP TABLE IF EXISTS wallet_transactions;
DROP TABLE IF EXISTS ledger_entries;
DROP TABLE IF EXISTS wallets;

CREATE TABLE IF NOT EXISTS idempotency_keys (
    id TEXT PRIMARY KEY,
    service TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    response_ref TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS evidence_receipts (
    id TEXT PRIMARY KEY,
    receipt_type TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT '',
    account_id TEXT NOT NULL DEFAULT '',
    organization_id TEXT NOT NULL DEFAULT '',
    workspace_id TEXT NOT NULL DEFAULT '',
    project_id TEXT NOT NULL DEFAULT '',
    task_id TEXT NOT NULL DEFAULT '',
    job_id TEXT NOT NULL DEFAULT '',
    payload_json TEXT NOT NULL DEFAULT '{}',
    supersedes_receipt_id TEXT NOT NULL DEFAULT '',
    provider_request_id TEXT NOT NULL DEFAULT '',
    redacted_url TEXT NOT NULL DEFAULT '',
    token_version TEXT NOT NULL DEFAULT '',
    idempotency_key TEXT NOT NULL UNIQUE,
    request_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE evidence_receipts ADD COLUMN IF NOT EXISTS account_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS evidence_receipts_account_created ON evidence_receipts (account_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS evidence_receipts_organization_created ON evidence_receipts (organization_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS evidence_receipts_workspace_created ON evidence_receipts (workspace_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS evidence_receipts_project_created ON evidence_receipts (project_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS evidence_receipts_task_created ON evidence_receipts (task_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS evidence_receipts_job_created ON evidence_receipts (job_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS evidence_receipts_type_created ON evidence_receipts (receipt_type, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS evidence_receipts_status_created ON evidence_receipts (status, created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS review_policies (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL DEFAULT '',
    workspace_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    job_id TEXT NOT NULL,
    version TEXT NOT NULL,
    required_reviewers_json TEXT NOT NULL,
    status TEXT NOT NULL,
    supersedes_policy_id TEXT NOT NULL DEFAULT '',
    idempotency_key TEXT NOT NULL UNIQUE,
    request_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (status IN ('active', 'superseded'))
);
CREATE INDEX IF NOT EXISTS review_policies_scope_created ON review_policies (organization_id, workspace_id, project_id, task_id, job_id, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS review_policies_active_scope ON review_policies (organization_id, workspace_id, project_id, task_id, job_id) WHERE status = 'active';

CREATE TABLE IF NOT EXISTS reconciliation_reports (
    id TEXT PRIMARY KEY,
    status TEXT NOT NULL DEFAULT 'ok',
    report_json TEXT NOT NULL DEFAULT '{}',
    block_new_workspaces BOOLEAN NOT NULL DEFAULT false,
    reason TEXT NOT NULL DEFAULT '',
    idempotency_key TEXT NOT NULL UNIQUE,
    request_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
