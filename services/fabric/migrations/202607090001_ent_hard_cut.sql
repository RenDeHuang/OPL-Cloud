CREATE TABLE IF NOT EXISTS fabric_operations (id TEXT PRIMARY KEY, operation_id TEXT NOT NULL, caller_service TEXT NOT NULL, action TEXT NOT NULL, resource_kind TEXT NOT NULL, resource_id TEXT NOT NULL, account_id TEXT, workspace_id TEXT, provider TEXT, provider_request_id TEXT, idempotency_key TEXT, request_hash TEXT, redacted_provider_payload JSONB NOT NULL DEFAULT '{}'::jsonb, status TEXT NOT NULL, error_code TEXT, retryable BOOLEAN NOT NULL DEFAULT false, started_at TIMESTAMPTZ NOT NULL, finished_at TIMESTAMPTZ, created_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS fabric_workspace_runtime_access (workspace_id TEXT PRIMARY KEY, runtime_id TEXT NOT NULL, url TEXT NOT NULL, service_name TEXT, username TEXT NOT NULL, password TEXT NOT NULL, credential_status TEXT NOT NULL, credential_version TEXT NOT NULL, secret_ref TEXT, updated_at TIMESTAMPTZ NOT NULL DEFAULT now());

CREATE INDEX IF NOT EXISTS fabric_operations_operation_id_idx ON fabric_operations(operation_id);
CREATE INDEX IF NOT EXISTS fabric_operations_resource_idx ON fabric_operations(resource_kind, resource_id);
CREATE INDEX IF NOT EXISTS fabric_operations_workspace_idx ON fabric_operations(workspace_id);
CREATE INDEX IF NOT EXISTS fabric_operations_created_idx ON fabric_operations(created_at);
