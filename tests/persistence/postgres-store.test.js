import assert from "node:assert/strict";
import test from "node:test";

import { emptyState, PostgresStore } from "../../packages/console/src/store.js";

function createFakePool() {
  const tables = {
    accounts: new Map(),
    organizations: new Map(),
    users: new Map(),
    memberships: [],
    workspaces: new Map(),
    storage_backups: [],
    billing_reconciliation_reports: [],
    evidence_ledger: [],
    billing_ledger: [],
    audit_events: [],
    notifications: [],
    runtime_operations: [],
    resource_usage_logs: [],
    request_usage_logs: [],
    wallet_transactions: [],
    manual_topups: [],
    request_usage_dedup: []
  };
  const statements = [];

  const pool = {
    tables,
    statements,
    async query(sql, params = []) {
      const normalized = sql.trim().replace(/\s+/g, " ");
      statements.push({ sql: normalized, params });

      if (normalized === "BEGIN" || normalized === "COMMIT" || normalized === "ROLLBACK") {
        return { rows: [] };
      }
      if (normalized.startsWith("CREATE TABLE IF NOT EXISTS") || normalized.startsWith("CREATE UNIQUE INDEX IF NOT EXISTS")) {
        return { rows: [] };
      }
      if (normalized.startsWith("TRUNCATE accounts, organizations, users, memberships, workspaces, storage_backups, billing_reconciliation_reports, evidence_ledger, billing_ledger, audit_events, notifications, runtime_operations, resource_usage_logs, request_usage_logs")) {
        tables.accounts.clear();
        tables.organizations.clear();
        tables.users.clear();
        tables.memberships = [];
        tables.workspaces.clear();
        tables.storage_backups = [];
        tables.billing_reconciliation_reports = [];
        tables.evidence_ledger = [];
        tables.billing_ledger = [];
        tables.audit_events = [];
        tables.notifications = [];
        tables.runtime_operations = [];
        tables.resource_usage_logs = [];
        tables.request_usage_logs = [];
        tables.wallet_transactions = [];
        tables.manual_topups = [];
        tables.request_usage_dedup = [];
        return { rows: [] };
      }
      if (normalized.startsWith("SELECT id, state FROM accounts")) {
        return { rows: [...tables.accounts.entries()].map(([id, state]) => ({ id, state })) };
      }
      if (normalized.startsWith("SELECT id, state FROM organizations")) {
        return { rows: [...tables.organizations.entries()].map(([id, state]) => ({ id, state })) };
      }
      if (normalized.startsWith("SELECT id, state FROM users")) {
        return { rows: [...tables.users.entries()].map(([id, state]) => ({ id, state })) };
      }
      if (normalized.startsWith("SELECT state FROM memberships")) {
        return { rows: tables.memberships.map((state) => ({ state })) };
      }
      if (normalized.startsWith("SELECT id, state FROM workspaces")) {
        return { rows: [...tables.workspaces.entries()].map(([id, state]) => ({ id, state })) };
      }
      if (normalized.startsWith("SELECT state FROM storage_backups")) {
        return { rows: tables.storage_backups.map((state) => ({ state })) };
      }
      if (normalized.startsWith("SELECT state FROM billing_reconciliation_reports")) {
        return { rows: tables.billing_reconciliation_reports.map((state) => ({ state })) };
      }
      if (normalized.startsWith("SELECT state FROM evidence_ledger")) {
        return { rows: tables.evidence_ledger.map((state) => ({ state })) };
      }
      if (normalized.startsWith("SELECT state FROM billing_ledger")) {
        return { rows: tables.billing_ledger.map((state) => ({ state })) };
      }
      if (normalized.startsWith("SELECT state FROM audit_events")) {
        return { rows: tables.audit_events.map((state) => ({ state })) };
      }
      if (normalized.startsWith("SELECT state FROM notifications")) {
        return { rows: tables.notifications.map((state) => ({ state })) };
      }
      if (normalized.startsWith("SELECT state FROM runtime_operations")) {
        return { rows: tables.runtime_operations.map((state) => ({ state })) };
      }
      if (normalized.startsWith("SELECT state FROM resource_usage_logs")) {
        return { rows: tables.resource_usage_logs.map((state) => ({ state })) };
      }
      if (normalized.startsWith("SELECT state FROM request_usage_logs")) {
        return { rows: tables.request_usage_logs.map((state) => ({ state })) };
      }
      if (normalized.startsWith("SELECT state FROM wallet_transactions")) {
        return { rows: tables.wallet_transactions.map((state) => ({ state })) };
      }
      if (normalized.startsWith("SELECT state FROM manual_topups")) {
        return { rows: tables.manual_topups.map((state) => ({ state })) };
      }
      if (normalized.startsWith("SELECT state FROM request_usage_dedup")) {
        return { rows: tables.request_usage_dedup.map((state) => ({ state })) };
      }
      if (normalized.startsWith("INSERT INTO accounts")) {
        tables.accounts.set(params[0], params[1]);
        return { rows: [] };
      }
      if (normalized.startsWith("INSERT INTO organizations")) {
        tables.organizations.set(params[0], params[1]);
        return { rows: [] };
      }
      if (normalized.startsWith("INSERT INTO users")) {
        tables.users.set(params[0], params[1]);
        return { rows: [] };
      }
      if (normalized.startsWith("INSERT INTO memberships")) {
        tables.memberships.push(params[3]);
        return { rows: [] };
      }
      if (normalized.startsWith("INSERT INTO workspaces")) {
        tables.workspaces.set(params[0], params[2]);
        return { rows: [] };
      }
      if (normalized.startsWith("INSERT INTO storage_backups")) {
        tables.storage_backups.push(params[3]);
        return { rows: [] };
      }
      if (normalized.startsWith("INSERT INTO billing_reconciliation_reports")) {
        tables.billing_reconciliation_reports.push(params[1]);
        return { rows: [] };
      }
      if (normalized.startsWith("INSERT INTO evidence_ledger")) {
        tables.evidence_ledger.push(params[3]);
        return { rows: [] };
      }
      if (normalized.startsWith("INSERT INTO billing_ledger")) {
        tables.billing_ledger.push(params[3]);
        return { rows: [] };
      }
      if (normalized.startsWith("INSERT INTO audit_events")) {
        tables.audit_events.push(params[3]);
        return { rows: [] };
      }
      if (normalized.startsWith("INSERT INTO notifications")) {
        tables.notifications.push(params[3]);
        return { rows: [] };
      }
      if (normalized.startsWith("INSERT INTO runtime_operations")) {
        tables.runtime_operations.push(params[3]);
        return { rows: [] };
      }
      if (normalized.startsWith("INSERT INTO resource_usage_logs")) {
        tables.resource_usage_logs.push(params[5]);
        return { rows: [] };
      }
      if (normalized.startsWith("INSERT INTO request_usage_logs")) {
        tables.request_usage_logs.push(params[5]);
        return { rows: [] };
      }
      if (normalized.startsWith("INSERT INTO wallet_transactions")) {
        tables.wallet_transactions.push(params[6]);
        return { rows: [] };
      }
      if (normalized.startsWith("INSERT INTO manual_topups")) {
        tables.manual_topups.push(params[4]);
        return { rows: [] };
      }
      if (normalized.startsWith("INSERT INTO request_usage_dedup")) {
        tables.request_usage_dedup.push(params[6]);
        return { rows: [] };
      }
      throw new Error(`unexpected_sql:${normalized}`);
    },
    async end() {}
  };

  return pool;
}

test("PostgresStore persists OPL Cloud state into control-plane tables", async () => {
  const pool = createFakePool();
  const store = new PostgresStore({ pool });
  const state = {
    ...emptyState(),
    accounts: {
      "pi-alpha": { id: "pi-alpha", balance: 200, frozen: 0, createdAt: "2026-07-01T00:00:00.000Z" }
    },
    organizations: {
      "org-lab": { id: "org-lab", name: "OPL Lab", billingAccountId: "pi-alpha" }
    },
    users: {
      "usr-ada": { id: "usr-ada", email: "ada@example.com" }
    },
    memberships: [
      { id: "membership-1", organizationId: "org-lab", userId: "usr-ada", role: "owner", status: "active" }
    ],
    workspaces: {
      "ws-alpha": {
        id: "ws-alpha",
        ownerAccountId: "pi-alpha",
        name: "Grant Lab",
        slug: "grant-lab-alpha",
        url: "https://grant-lab-alpha.oplcloud.cn/?token=share_alpha"
      }
    },
    evidenceLedger: [
      { id: "receipt-1", workspaceId: "ws-alpha", accountId: "pi-alpha", type: "workspace.created" }
    ],
    storageBackups: [
      {
        id: "backup-1",
        accountId: "pi-alpha",
        workspaceId: "ws-alpha",
        status: "available",
        snapshotName: "backup-1",
        createdAt: "2026-07-01T01:00:00.000Z"
      }
    ],
    billingReconciliationReports: [
      {
        id: "recon-1",
        ok: true,
        generatedAt: "2026-07-01T02:00:00.000Z",
        mismatches: [],
        guard: {
          status: "ok",
          blockNewWorkspaces: false,
          reason: "billing_reconciliation_ok"
        }
      }
    ],
    billingLedger: [
      { id: "ledger-1", workspaceId: "ws-alpha", accountId: "pi-alpha", type: "storage_hold", amount: 0.5 }
    ],
    audit: [
      { id: "audit-1", workspaceId: "ws-alpha", accountId: "pi-alpha", type: "workspace.created" }
    ],
    notifications: [
      { id: "notification-1", workspaceId: "ws-alpha", accountId: "pi-alpha", type: "workspace.created" }
    ],
    runtimeOperations: [
      {
        id: "op-1",
        workspaceId: "ws-alpha",
        operationType: "create_workspace",
        status: "succeeded",
        attempts: 1
      }
    ],
    resourceUsageLogs: [
      {
        id: "usage-resource-1",
        userId: "usr-ada",
        accountId: "pi-alpha",
        workspaceId: "ws-alpha",
        resourceType: "compute",
        quantity: 1,
        unit: "hour",
        unitPrice: 1.2,
        amount: 1.2,
        currency: "CNY",
        sourceEventId: "billing_tick_1",
        createdAt: "2026-07-01T03:00:00.000Z"
      }
    ],
    requestUsageLogs: [
      {
        id: "usage-request-1",
        userId: "usr-ada",
        accountId: "pi-alpha",
        workspaceId: "ws-alpha",
        requestId: "req-1",
        provider: "openai",
        model: "gpt-5",
        inputTokens: 100,
        outputTokens: 20,
        amount: 0.1,
        currency: "CNY",
        sourceEventId: "gateway_req_1",
        createdAt: "2026-07-01T03:01:00.000Z"
      }
    ],
    walletTransactions: [
      {
        id: "wallet-tx-1",
        userId: "usr-ada",
        accountId: "pi-alpha",
        workspaceId: "ws-alpha",
        type: "request_debit",
        amount: -0.1,
        currency: "CNY",
        balanceBefore: 200,
        balanceAfter: 199.9,
        frozenBefore: 0,
        frozenAfter: 0,
        sourceEventId: "gateway_req_1",
        usageLogId: "usage-request-1",
        ledgerEntryId: "ledger-2",
        fundingSource: "available_balance",
        createdAt: "2026-07-01T03:01:00.000Z"
      }
    ],
    manualTopups: [
      {
        id: "manual-topup-1",
        operatorUserId: "usr-admin",
        operatorAccountId: "admin",
        targetUserId: "usr-ada",
        targetAccountId: "pi-alpha",
        amount: 200,
        currency: "CNY",
        reason: "pilot_credit",
        status: "completed",
        balanceBefore: 0,
        balanceAfter: 200,
        ledgerEntryId: "ledger-1",
        walletTransactionId: "wallet-tx-0",
        createdAt: "2026-07-01T00:00:00.000Z"
      }
    ],
    requestUsageDedup: [
      {
        id: "dedup-1",
        userId: "usr-ada",
        accountId: "pi-alpha",
        workspaceId: "ws-alpha",
        requestId: "req-1",
        sourceEventId: "gateway_req_1",
        requestFingerprint: "fingerprint-1",
        usageLogId: "usage-request-1",
        createdAt: "2026-07-01T03:01:00.000Z"
      }
    ]
  };

  await store.write(state);
  const persisted = await store.read();

  assert.deepEqual(persisted, state);
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS accounts")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS organizations")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS users")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS memberships")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS workspaces")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS storage_backups")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS billing_reconciliation_reports")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS evidence_ledger")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS billing_ledger")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS audit_events")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS notifications")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS runtime_operations")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS resource_usage_logs")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS request_usage_logs")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS wallet_transactions")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS manual_topups")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS request_usage_dedup")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE UNIQUE INDEX IF NOT EXISTS request_usage_logs_workspace_request_idx")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE UNIQUE INDEX IF NOT EXISTS request_usage_dedup_workspace_source_idx")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE UNIQUE INDEX IF NOT EXISTS resource_usage_logs_workspace_resource_source_idx")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE UNIQUE INDEX IF NOT EXISTS billing_ledger_dedup_idx")));
});

test("PostgresStore update reads, mutates, and writes state transactionally", async () => {
  const pool = createFakePool();
  const store = new PostgresStore({ pool });

  const result = await store.update((state) => {
    assert.deepEqual(state, emptyState());
    state.accounts["pi-beta"] = { id: "pi-beta", balance: 50, frozen: 0 };
    return { ok: true };
  });

  assert.deepEqual(result, { ok: true });
  assert.equal((await store.read()).accounts["pi-beta"].balance, 50);
  assert.deepEqual(
    pool.statements
      .map((statement) => statement.sql)
      .filter((sql) => sql === "BEGIN" || sql === "COMMIT" || sql === "ROLLBACK"),
    ["BEGIN", "COMMIT"]
  );
});
