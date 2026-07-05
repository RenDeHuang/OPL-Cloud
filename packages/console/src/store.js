import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname } from "node:path";
import pg from "pg";

const { Pool } = pg;
const STATE_UPDATE_LOCK_KEY = Object.freeze([1869, 5021]);

export function emptyState() {
  return {
    organizations: {},
    users: {},
    memberships: [],
    workspaces: {},
    billingReconciliationReports: [],
    supportTickets: [],
    evidenceLedger: [],
    billingLedger: [],
    audit: [],
    notifications: [],
    runtimeOperations: [],
    computeAllocations: [],
    storageVolumes: [],
    storageAttachments: [],
    resourceUsageLogs: [],
    requestUsageLogs: [],
    walletTransactions: [],
    manualTopups: [],
    requestUsageDedup: []
  };
}

function clone(value) {
  return JSON.parse(JSON.stringify(value));
}

function currentState(rawState = {}) {
  const source = clone(rawState || {});
  const state = emptyState();
  for (const key of Object.keys(state)) {
    if (source[key] !== undefined) state[key] = source[key];
  }
  return state;
}

export class MemoryStore {
  constructor(initialState = emptyState()) {
    this.state = currentState(initialState);
  }

  async read() {
    return clone(this.state);
  }

  async write(nextState) {
    this.state = currentState(nextState);
    return this.read();
  }

  async update(mutator) {
    const nextState = await this.read();
    const result = await mutator(nextState);
    await this.write(nextState);
    return result;
  }
}

export class JsonFileStore {
  constructor(filePath) {
    this.filePath = filePath;
  }

  async read() {
    try {
      const raw = await readFile(this.filePath, "utf8");
      return currentState(JSON.parse(raw));
    } catch (error) {
      if (error.code !== "ENOENT") throw error;
      return emptyState();
    }
  }

  async write(nextState) {
    await mkdir(dirname(this.filePath), { recursive: true });
    await writeFile(this.filePath, `${JSON.stringify(nextState, null, 2)}\n`);
    return this.read();
  }

  async update(mutator) {
    const nextState = await this.read();
    const result = await mutator(nextState);
    await this.write(nextState);
    return result;
  }
}

export class PostgresStore {
  constructor({ connectionString = process.env.DATABASE_URL, pool } = {}) {
    this.pool = pool || new Pool({ connectionString });
    this.initialized = false;
  }

  async read(client = this.pool) {
    await this.ensureSchema(client);
    const state = emptyState();
    const [
      organizations,
      users,
      memberships,
      workspaces,
      billingReconciliationReports,
      supportTickets,
      evidenceLedger,
      billingLedger,
      audit,
      notifications,
      runtimeOperations,
      computeAllocations,
      storageVolumes,
      storageAttachments,
      resourceUsageLogs,
      requestUsageLogs,
      walletTransactions,
      manualTopups,
      requestUsageDedup
    ] = await Promise.all([
      client.query("SELECT id, state FROM organizations ORDER BY id"),
      client.query("SELECT id, state FROM users ORDER BY id"),
      client.query("SELECT state FROM memberships ORDER BY created_at, id"),
      client.query("SELECT id, state FROM workspaces ORDER BY id"),
      client.query("SELECT state FROM billing_reconciliation_reports ORDER BY created_at, id"),
      client.query("SELECT state FROM support_tickets ORDER BY created_at, id"),
      client.query("SELECT state FROM evidence_ledger ORDER BY created_at, id"),
      client.query("SELECT state FROM billing_ledger ORDER BY created_at, id"),
      client.query("SELECT state FROM audit_events ORDER BY created_at, id"),
      client.query("SELECT state FROM notifications ORDER BY created_at, id"),
      client.query("SELECT state FROM runtime_operations ORDER BY created_at, id"),
      client.query("SELECT state FROM compute_allocations ORDER BY created_at, id"),
      client.query("SELECT state FROM storage_volumes ORDER BY created_at, id"),
      client.query("SELECT state FROM storage_attachments ORDER BY created_at, id"),
      client.query("SELECT state FROM resource_usage_logs ORDER BY created_at, id"),
      client.query("SELECT state FROM request_usage_logs ORDER BY created_at, id"),
      client.query("SELECT state FROM wallet_transactions ORDER BY created_at, id"),
      client.query("SELECT state FROM manual_topups ORDER BY created_at, id"),
      client.query("SELECT state FROM request_usage_dedup ORDER BY created_at, id")
    ]);

    for (const row of organizations.rows) state.organizations[row.id] = row.state;
    for (const row of users.rows) state.users[row.id] = row.state;
    state.memberships = memberships.rows.map((row) => row.state);
    for (const row of workspaces.rows) state.workspaces[row.id] = row.state;
    state.billingReconciliationReports = billingReconciliationReports.rows.map((row) => row.state);
    state.supportTickets = supportTickets.rows.map((row) => row.state);
    state.evidenceLedger = evidenceLedger.rows.map((row) => row.state);
    state.billingLedger = billingLedger.rows.map((row) => row.state);
    state.audit = audit.rows.map((row) => row.state);
    state.notifications = notifications.rows.map((row) => row.state);
    state.runtimeOperations = runtimeOperations.rows.map((row) => row.state);
    state.computeAllocations = computeAllocations.rows.map((row) => row.state);
    state.storageVolumes = storageVolumes.rows.map((row) => row.state);
    state.storageAttachments = storageAttachments.rows.map((row) => row.state);
    state.resourceUsageLogs = resourceUsageLogs.rows.map((row) => row.state);
    state.requestUsageLogs = requestUsageLogs.rows.map((row) => row.state);
    state.walletTransactions = walletTransactions.rows.map((row) => row.state);
    state.manualTopups = manualTopups.rows.map((row) => row.state);
    state.requestUsageDedup = requestUsageDedup.rows.map((row) => row.state);
    return currentState(state);
  }

  async write(nextState, client = this.pool) {
    await this.ensureSchema(client);
    await client.query("TRUNCATE organizations, users, memberships, workspaces, billing_reconciliation_reports, support_tickets, evidence_ledger, billing_ledger, audit_events, notifications, runtime_operations, compute_allocations, storage_volumes, storage_attachments, resource_usage_logs, request_usage_logs, wallet_transactions, manual_topups, request_usage_dedup");

    for (const organization of Object.values(nextState.organizations || {})) {
      await client.query(
        "INSERT INTO organizations (id, state, updated_at) VALUES ($1, $2, now()) ON CONFLICT (id) DO UPDATE SET state = EXCLUDED.state, updated_at = now()",
        [organization.id, organization]
      );
    }
    for (const user of Object.values(nextState.users || {})) {
      await client.query(
        "INSERT INTO users (id, state, updated_at) VALUES ($1, $2, now()) ON CONFLICT (id) DO UPDATE SET state = EXCLUDED.state, updated_at = now()",
        [user.id, user]
      );
    }
    for (const membership of nextState.memberships || []) {
      await client.query(
        "INSERT INTO memberships (id, organization_id, user_id, state, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6)",
        [
          membership.id,
          membership.organizationId,
          membership.userId,
          membership,
          membership.createdAt || new Date().toISOString(),
          membership.updatedAt || membership.createdAt || new Date().toISOString()
        ]
      );
    }
    for (const workspace of Object.values(nextState.workspaces || {})) {
      await client.query(
        "INSERT INTO workspaces (id, owner_account_id, state, updated_at) VALUES ($1, $2, $3, now()) ON CONFLICT (id) DO UPDATE SET owner_account_id = EXCLUDED.owner_account_id, state = EXCLUDED.state, updated_at = now()",
        [workspace.id, workspace.ownerAccountId, workspace]
      );
    }
    for (const report of nextState.billingReconciliationReports || []) {
      await client.query("INSERT INTO billing_reconciliation_reports (id, state, created_at) VALUES ($1, $2, $3)", [
        report.id,
        report,
        report.createdAt || report.generatedAt || new Date().toISOString()
      ]);
    }
    for (const ticket of nextState.supportTickets || []) {
      await client.query("INSERT INTO support_tickets (id, account_id, user_id, workspace_id, status, state, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)", [
        ticket.id,
        ticket.accountId,
        ticket.userId || "",
        ticket.workspaceId || "",
        ticket.status,
        ticket,
        ticket.createdAt || new Date().toISOString(),
        ticket.updatedAt || ticket.createdAt || new Date().toISOString()
      ]);
    }
    for (const entry of nextState.evidenceLedger || []) {
      await client.query("INSERT INTO evidence_ledger (id, account_id, workspace_id, state, created_at) VALUES ($1, $2, $3, $4, $5)", [
        entry.id,
        entry.accountId,
        entry.workspaceId || "",
        entry,
        entry.createdAt || new Date().toISOString()
      ]);
    }
    for (const entry of nextState.billingLedger || []) {
      await client.query("INSERT INTO billing_ledger (id, account_id, workspace_id, state, created_at) VALUES ($1, $2, $3, $4, $5)", [
        entry.id,
        entry.accountId,
        entry.workspaceId,
        entry,
        entry.createdAt || new Date().toISOString()
      ]);
    }
    for (const entry of nextState.audit || []) {
      await client.query("INSERT INTO audit_events (id, account_id, workspace_id, state, created_at) VALUES ($1, $2, $3, $4, $5)", [
        entry.id,
        entry.accountId,
        entry.workspaceId,
        entry,
        entry.createdAt || new Date().toISOString()
      ]);
    }
    for (const entry of nextState.notifications || []) {
      await client.query("INSERT INTO notifications (id, account_id, workspace_id, state, created_at) VALUES ($1, $2, $3, $4, $5)", [
        entry.id,
        entry.accountId,
        entry.workspaceId,
        entry,
        entry.createdAt || new Date().toISOString()
      ]);
    }
    for (const operation of nextState.runtimeOperations || []) {
      await client.query("INSERT INTO runtime_operations (id, workspace_id, operation_type, state, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6)", [
        operation.id,
        operation.workspaceId,
        operation.operationType,
        operation,
        operation.createdAt || new Date().toISOString(),
        operation.updatedAt || operation.createdAt || new Date().toISOString()
      ]);
    }
    for (const compute of nextState.computeAllocations || []) {
      await client.query("INSERT INTO compute_allocations (id, account_id, state, created_at, updated_at) VALUES ($1, $2, $3, $4, $5)", [
        compute.id,
        compute.ownerAccountId,
        compute,
        compute.createdAt || new Date().toISOString(),
        compute.updatedAt || compute.createdAt || new Date().toISOString()
      ]);
    }
    for (const storage of nextState.storageVolumes || []) {
      await client.query("INSERT INTO storage_volumes (id, account_id, state, created_at, updated_at) VALUES ($1, $2, $3, $4, $5)", [
        storage.id,
        storage.ownerAccountId,
        storage,
        storage.createdAt || new Date().toISOString(),
        storage.updatedAt || storage.createdAt || new Date().toISOString()
      ]);
    }
    for (const attachment of nextState.storageAttachments || []) {
      await client.query("INSERT INTO storage_attachments (id, account_id, compute_allocation_id, storage_id, state, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7)", [
        attachment.id,
        attachment.ownerAccountId,
        attachment.computeAllocationId,
        attachment.storageId,
        attachment,
        attachment.createdAt || new Date().toISOString(),
        attachment.updatedAt || attachment.createdAt || new Date().toISOString()
      ]);
    }
    for (const usage of nextState.resourceUsageLogs || []) {
      await client.query("INSERT INTO resource_usage_logs (id, user_id, account_id, workspace_id, resource_type, state, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7)", [
        usage.id,
        usage.userId,
        usage.accountId,
        usage.workspaceId,
        usage.resourceType,
        usage,
        usage.createdAt || new Date().toISOString()
      ]);
    }
    for (const usage of nextState.requestUsageLogs || []) {
      await client.query("INSERT INTO request_usage_logs (id, user_id, account_id, workspace_id, request_id, state, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7)", [
        usage.id,
        usage.userId,
        usage.accountId,
        usage.workspaceId,
        usage.requestId,
        usage,
        usage.createdAt || new Date().toISOString()
      ]);
    }
    for (const transaction of nextState.walletTransactions || []) {
      await client.query("INSERT INTO wallet_transactions (id, user_id, account_id, workspace_id, transaction_type, source_event_id, state, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)", [
        transaction.id,
        transaction.userId,
        transaction.accountId,
        transaction.workspaceId || "",
        transaction.type,
        transaction.sourceEventId || "",
        transaction,
        transaction.createdAt || new Date().toISOString()
      ]);
    }
    for (const topup of nextState.manualTopups || []) {
      await client.query("INSERT INTO manual_topups (id, operator_user_id, target_user_id, target_account_id, state, created_at) VALUES ($1, $2, $3, $4, $5, $6)", [
        topup.id,
        topup.operatorUserId || "",
        topup.targetUserId,
        topup.targetAccountId,
        topup,
        topup.createdAt || new Date().toISOString()
      ]);
    }
    for (const dedup of nextState.requestUsageDedup || []) {
      await client.query("INSERT INTO request_usage_dedup (id, workspace_id, source_event_id, request_id, request_fingerprint, usage_log_id, state, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)", [
        dedup.id,
        dedup.workspaceId,
        dedup.sourceEventId,
        dedup.requestId,
        dedup.requestFingerprint,
        dedup.usageLogId,
        dedup,
        dedup.createdAt || new Date().toISOString()
      ]);
    }
    return this.read(client);
  }

  async update(mutator) {
    const client = await this.checkoutClient();
    try {
      await client.query("BEGIN");
      await client.query("SELECT pg_advisory_xact_lock($1, $2)", STATE_UPDATE_LOCK_KEY);
      const nextState = await this.read(client);
      const result = await mutator(nextState);
      await this.write(nextState, client);
      await client.query("COMMIT");
      return result;
    } catch (error) {
      await client.query("ROLLBACK");
      throw error;
    } finally {
      client.release?.();
    }
  }

  async close() {
    await this.pool.end();
  }

  async checkoutClient() {
    if (typeof this.pool.connect === "function") return this.pool.connect();
    return this.pool;
  }

  async ensureSchema(client = this.pool) {
    if (this.initialized) return;
    await client.query(`
      CREATE TABLE IF NOT EXISTS organizations (
        id text PRIMARY KEY,
        state jsonb NOT NULL,
        updated_at timestamptz NOT NULL DEFAULT now()
      )
    `);
    await client.query(`
      CREATE TABLE IF NOT EXISTS users (
        id text PRIMARY KEY,
        state jsonb NOT NULL,
        updated_at timestamptz NOT NULL DEFAULT now()
      )
    `);
    await client.query(`
      CREATE TABLE IF NOT EXISTS memberships (
        id text PRIMARY KEY,
        organization_id text NOT NULL,
        user_id text NOT NULL,
        state jsonb NOT NULL,
        created_at timestamptz NOT NULL DEFAULT now(),
        updated_at timestamptz NOT NULL DEFAULT now()
      )
    `);
    await client.query(`
      CREATE TABLE IF NOT EXISTS workspaces (
        id text PRIMARY KEY,
        owner_account_id text NOT NULL,
        state jsonb NOT NULL,
        updated_at timestamptz NOT NULL DEFAULT now()
      )
    `);
    await client.query(`
      CREATE TABLE IF NOT EXISTS billing_reconciliation_reports (
        id text PRIMARY KEY,
        state jsonb NOT NULL,
        created_at timestamptz NOT NULL DEFAULT now()
      )
    `);
    await client.query(`
      CREATE TABLE IF NOT EXISTS support_tickets (
        id text PRIMARY KEY,
        account_id text NOT NULL,
        user_id text NOT NULL,
        workspace_id text NOT NULL,
        status text NOT NULL,
        state jsonb NOT NULL,
        created_at timestamptz NOT NULL DEFAULT now(),
        updated_at timestamptz NOT NULL DEFAULT now()
      )
    `);
    await client.query(`
      CREATE TABLE IF NOT EXISTS evidence_ledger (
        id text PRIMARY KEY,
        account_id text NOT NULL,
        workspace_id text NOT NULL,
        state jsonb NOT NULL,
        created_at timestamptz NOT NULL DEFAULT now()
      )
    `);
    await client.query(`
      CREATE TABLE IF NOT EXISTS billing_ledger (
        id text PRIMARY KEY,
        account_id text NOT NULL,
        workspace_id text NOT NULL,
        state jsonb NOT NULL,
        created_at timestamptz NOT NULL DEFAULT now()
      )
    `);
    await client.query(`
      CREATE TABLE IF NOT EXISTS audit_events (
        id text PRIMARY KEY,
        account_id text NOT NULL,
        workspace_id text NOT NULL,
        state jsonb NOT NULL,
        created_at timestamptz NOT NULL DEFAULT now()
      )
    `);
    await client.query(`
      CREATE TABLE IF NOT EXISTS notifications (
        id text PRIMARY KEY,
        account_id text NOT NULL,
        workspace_id text NOT NULL,
        state jsonb NOT NULL,
        created_at timestamptz NOT NULL DEFAULT now()
      )
    `);
    await client.query(`
      CREATE TABLE IF NOT EXISTS runtime_operations (
        id text PRIMARY KEY,
        workspace_id text NOT NULL,
        operation_type text NOT NULL,
        state jsonb NOT NULL,
        created_at timestamptz NOT NULL DEFAULT now(),
        updated_at timestamptz NOT NULL DEFAULT now()
      )
    `);
    await client.query(`
      CREATE TABLE IF NOT EXISTS compute_allocations (
        id text PRIMARY KEY,
        account_id text NOT NULL,
        state jsonb NOT NULL,
        created_at timestamptz NOT NULL DEFAULT now(),
        updated_at timestamptz NOT NULL DEFAULT now()
      )
    `);
    await client.query(`
      CREATE TABLE IF NOT EXISTS storage_volumes (
        id text PRIMARY KEY,
        account_id text NOT NULL,
        state jsonb NOT NULL,
        created_at timestamptz NOT NULL DEFAULT now(),
        updated_at timestamptz NOT NULL DEFAULT now()
      )
    `);
    await client.query(`
      CREATE TABLE IF NOT EXISTS storage_attachments (
        id text PRIMARY KEY,
        account_id text NOT NULL,
        compute_allocation_id text NOT NULL,
        storage_id text NOT NULL,
        state jsonb NOT NULL,
        created_at timestamptz NOT NULL DEFAULT now(),
        updated_at timestamptz NOT NULL DEFAULT now()
      )
    `);
    await client.query(`
      CREATE TABLE IF NOT EXISTS resource_usage_logs (
        id text PRIMARY KEY,
        user_id text NOT NULL,
        account_id text NOT NULL,
        workspace_id text NOT NULL,
        resource_type text NOT NULL,
        state jsonb NOT NULL,
        created_at timestamptz NOT NULL DEFAULT now()
      )
    `);
    await client.query(`
      CREATE TABLE IF NOT EXISTS request_usage_logs (
        id text PRIMARY KEY,
        user_id text NOT NULL,
        account_id text NOT NULL,
        workspace_id text NOT NULL,
        request_id text NOT NULL,
        state jsonb NOT NULL,
        created_at timestamptz NOT NULL DEFAULT now()
      )
    `);
    await client.query(`
      CREATE TABLE IF NOT EXISTS wallet_transactions (
        id text PRIMARY KEY,
        user_id text NOT NULL,
        account_id text NOT NULL,
        workspace_id text NOT NULL,
        transaction_type text NOT NULL,
        source_event_id text NOT NULL,
        state jsonb NOT NULL,
        created_at timestamptz NOT NULL DEFAULT now()
      )
    `);
    await client.query(`
      CREATE TABLE IF NOT EXISTS manual_topups (
        id text PRIMARY KEY,
        operator_user_id text NOT NULL,
        target_user_id text NOT NULL,
        target_account_id text NOT NULL,
        state jsonb NOT NULL,
        created_at timestamptz NOT NULL DEFAULT now()
      )
    `);
    await client.query(`
      CREATE TABLE IF NOT EXISTS request_usage_dedup (
        id text PRIMARY KEY,
        workspace_id text NOT NULL,
        source_event_id text NOT NULL,
        request_id text NOT NULL,
        request_fingerprint text NOT NULL,
        usage_log_id text NOT NULL,
        state jsonb NOT NULL,
        created_at timestamptz NOT NULL DEFAULT now()
      )
    `);
    await this.ensureCurrentColumns(client);
    await this.dropRetiredColumnRequirements(client);
    await this.pruneRetiredRows(client);
    await client.query("CREATE UNIQUE INDEX IF NOT EXISTS request_usage_logs_workspace_request_idx ON request_usage_logs (workspace_id, request_id)");
    await client.query("CREATE UNIQUE INDEX IF NOT EXISTS request_usage_dedup_workspace_source_idx ON request_usage_dedup (workspace_id, source_event_id)");
    await client.query("CREATE UNIQUE INDEX IF NOT EXISTS resource_usage_logs_workspace_resource_source_idx ON resource_usage_logs (workspace_id, resource_type, ((state->>'sourceEventId')))");
    await client.query("DROP INDEX IF EXISTS billing_ledger_dedup_idx");
    await client.query(`
      CREATE INDEX IF NOT EXISTS billing_ledger_event_lookup_idx
      ON billing_ledger (
        account_id,
        workspace_id,
        ((state->>'type')),
        ((state->>'sourceEventId')),
        (COALESCE(state->'metadata'->>'fundingSource', ''))
      )
      WHERE
        state->>'sourceEventId' IS NOT NULL
        AND state->>'sourceEventId' <> ''
        AND (state->>'type') IN ('compute_debit', 'storage_debit', 'compute_hold_exhausted', 'request_debit')
    `);
    this.initialized = true;
  }

  async ensureCurrentColumns(client) {
    const migrations = {
      memberships: {
        organization_id: "text NOT NULL DEFAULT ''",
        user_id: "text NOT NULL DEFAULT ''",
        created_at: "timestamptz NOT NULL DEFAULT now()",
        updated_at: "timestamptz NOT NULL DEFAULT now()"
      },
      workspaces: {
        owner_account_id: "text NOT NULL DEFAULT ''",
        updated_at: "timestamptz NOT NULL DEFAULT now()"
      },
      support_tickets: {
        account_id: "text NOT NULL DEFAULT ''",
        user_id: "text NOT NULL DEFAULT ''",
        workspace_id: "text NOT NULL DEFAULT ''",
        status: "text NOT NULL DEFAULT ''",
        created_at: "timestamptz NOT NULL DEFAULT now()",
        updated_at: "timestamptz NOT NULL DEFAULT now()"
      },
      evidence_ledger: {
        account_id: "text NOT NULL DEFAULT ''",
        workspace_id: "text NOT NULL DEFAULT ''",
        created_at: "timestamptz NOT NULL DEFAULT now()"
      },
      billing_ledger: {
        account_id: "text NOT NULL DEFAULT ''",
        workspace_id: "text NOT NULL DEFAULT ''",
        created_at: "timestamptz NOT NULL DEFAULT now()"
      },
      audit_events: {
        account_id: "text NOT NULL DEFAULT ''",
        workspace_id: "text NOT NULL DEFAULT ''",
        created_at: "timestamptz NOT NULL DEFAULT now()"
      },
      notifications: {
        account_id: "text NOT NULL DEFAULT ''",
        workspace_id: "text NOT NULL DEFAULT ''",
        created_at: "timestamptz NOT NULL DEFAULT now()"
      },
      runtime_operations: {
        workspace_id: "text NOT NULL DEFAULT ''",
        operation_type: "text NOT NULL DEFAULT ''",
        created_at: "timestamptz NOT NULL DEFAULT now()",
        updated_at: "timestamptz NOT NULL DEFAULT now()"
      },
      compute_allocations: {
        account_id: "text NOT NULL DEFAULT ''",
        created_at: "timestamptz NOT NULL DEFAULT now()",
        updated_at: "timestamptz NOT NULL DEFAULT now()"
      },
      storage_volumes: {
        account_id: "text NOT NULL DEFAULT ''",
        created_at: "timestamptz NOT NULL DEFAULT now()",
        updated_at: "timestamptz NOT NULL DEFAULT now()"
      },
      storage_attachments: {
        account_id: "text NOT NULL DEFAULT ''",
        compute_allocation_id: "text NOT NULL DEFAULT ''",
        storage_id: "text NOT NULL DEFAULT ''",
        created_at: "timestamptz NOT NULL DEFAULT now()",
        updated_at: "timestamptz NOT NULL DEFAULT now()"
      },
      resource_usage_logs: {
        user_id: "text NOT NULL DEFAULT ''",
        account_id: "text NOT NULL DEFAULT ''",
        workspace_id: "text NOT NULL DEFAULT ''",
        resource_type: "text NOT NULL DEFAULT ''",
        created_at: "timestamptz NOT NULL DEFAULT now()"
      },
      request_usage_logs: {
        user_id: "text NOT NULL DEFAULT ''",
        account_id: "text NOT NULL DEFAULT ''",
        workspace_id: "text NOT NULL DEFAULT ''",
        request_id: "text NOT NULL DEFAULT ''",
        created_at: "timestamptz NOT NULL DEFAULT now()"
      },
      wallet_transactions: {
        user_id: "text NOT NULL DEFAULT ''",
        account_id: "text NOT NULL DEFAULT ''",
        workspace_id: "text NOT NULL DEFAULT ''",
        transaction_type: "text NOT NULL DEFAULT ''",
        source_event_id: "text NOT NULL DEFAULT ''",
        created_at: "timestamptz NOT NULL DEFAULT now()"
      },
      manual_topups: {
        operator_user_id: "text NOT NULL DEFAULT ''",
        target_user_id: "text NOT NULL DEFAULT ''",
        target_account_id: "text NOT NULL DEFAULT ''",
        created_at: "timestamptz NOT NULL DEFAULT now()"
      },
      request_usage_dedup: {
        workspace_id: "text NOT NULL DEFAULT ''",
        source_event_id: "text NOT NULL DEFAULT ''",
        request_id: "text NOT NULL DEFAULT ''",
        request_fingerprint: "text NOT NULL DEFAULT ''",
        usage_log_id: "text NOT NULL DEFAULT ''",
        created_at: "timestamptz NOT NULL DEFAULT now()"
      }
    };

    for (const [table, columns] of Object.entries(migrations)) {
      for (const [column, definition] of Object.entries(columns)) {
        await client.query(`ALTER TABLE ${table} ADD COLUMN IF NOT EXISTS ${column} ${definition}`);
      }
    }
  }

  async dropRetiredColumnRequirements(client) {
    await client.query(`
      DO $$
      BEGIN
        IF EXISTS (
          SELECT 1
          FROM information_schema.columns
          WHERE table_schema = current_schema()
            AND table_name = 'storage_attachments'
            AND column_name = 'compute_id'
        ) THEN
          ALTER TABLE storage_attachments ALTER COLUMN compute_id DROP NOT NULL;
        END IF;
      END
      $$;
    `);
  }

  async pruneRetiredRows(client) {
    await client.query("DELETE FROM storage_attachments WHERE NOT (state ? 'computeAllocationId')");
  }
}
