import assert from "node:assert/strict";
import test from "node:test";

import { emptyState, PostgresStore } from "../../packages/console/src/store.js";

function createFakePool() {
  const tables = {
    organizations: new Map(),
    users: new Map(),
    memberships: [],
    workspaces: new Map(),
    billing_reconciliation_reports: [],
    support_tickets: [],
    evidence_ledger: [],
    billing_ledger: [],
    audit_events: [],
    notifications: [],
    runtime_operations: [],
    compute_resources: [],
    storage_volumes: [],
    storage_attachments: [],
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
      if (normalized.startsWith("CREATE TABLE IF NOT EXISTS") || normalized.startsWith("CREATE UNIQUE INDEX IF NOT EXISTS") || normalized.startsWith("CREATE INDEX IF NOT EXISTS") || normalized.startsWith("DROP INDEX IF EXISTS")) {
        return { rows: [] };
      }
      if (normalized.startsWith("TRUNCATE organizations, users, memberships, workspaces, billing_reconciliation_reports, support_tickets, evidence_ledger, billing_ledger, audit_events, notifications, runtime_operations")) {
        tables.organizations.clear();
        tables.users.clear();
        tables.memberships = [];
        tables.workspaces.clear();
        tables.billing_reconciliation_reports = [];
        tables.support_tickets = [];
        tables.evidence_ledger = [];
        tables.billing_ledger = [];
        tables.audit_events = [];
        tables.notifications = [];
        tables.runtime_operations = [];
        tables.compute_resources = [];
        tables.storage_volumes = [];
        tables.storage_attachments = [];
        tables.resource_usage_logs = [];
        tables.request_usage_logs = [];
        tables.wallet_transactions = [];
        tables.manual_topups = [];
        tables.request_usage_dedup = [];
        return { rows: [] };
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
      if (normalized.startsWith("SELECT state FROM billing_reconciliation_reports")) {
        return { rows: tables.billing_reconciliation_reports.map((state) => ({ state })) };
      }
      if (normalized.startsWith("SELECT state FROM support_tickets")) {
        return { rows: tables.support_tickets.map((state) => ({ state })) };
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
      if (normalized.startsWith("SELECT state FROM compute_resources")) {
        return { rows: tables.compute_resources.map((state) => ({ state })) };
      }
      if (normalized.startsWith("SELECT state FROM storage_volumes")) {
        return { rows: tables.storage_volumes.map((state) => ({ state })) };
      }
      if (normalized.startsWith("SELECT state FROM storage_attachments")) {
        return { rows: tables.storage_attachments.map((state) => ({ state })) };
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
      if (normalized.startsWith("INSERT INTO billing_reconciliation_reports")) {
        tables.billing_reconciliation_reports.push(params[1]);
        return { rows: [] };
      }
      if (normalized.startsWith("INSERT INTO support_tickets")) {
        tables.support_tickets.push(params[5]);
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
      if (normalized.startsWith("INSERT INTO compute_resources")) {
        tables.compute_resources.push(params[2]);
        return { rows: [] };
      }
      if (normalized.startsWith("INSERT INTO storage_volumes")) {
        tables.storage_volumes.push(params[2]);
        return { rows: [] };
      }
      if (normalized.startsWith("INSERT INTO storage_attachments")) {
        tables.storage_attachments.push(params[4]);
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
    organizations: {
      "org-lab": { id: "org-lab", name: "OPL Lab", billingAccountId: "pi-alpha" }
    },
    users: {
      "usr-ada": {
        id: "usr-ada",
        email: "ada@example.com",
        accountId: "pi-alpha",
        balance: 200,
        frozen: 0,
        holds: {},
        totalRecharged: 200
      }
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
    supportTickets: [
      {
        id: "ticket-1",
        accountId: "pi-alpha",
        userId: "usr-ada",
        workspaceId: "ws-alpha",
        title: "Workspace access",
        category: "Workspace",
        priority: "high",
        status: "open",
        createdAt: "2026-07-01T02:30:00.000Z",
        updatedAt: "2026-07-01T02:30:00.000Z",
        messages: [
          {
            author: "ada@example.com",
            text: "Workspace URL returns 403.",
            createdAt: "2026-07-01T02:30:00.000Z"
          }
        ]
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
    computeResources: [
      {
        id: "compute-1",
        ownerAccountId: "pi-alpha",
        ownerUserId: "usr-ada",
        packageId: "basic",
        provider: "tencent-tke",
        providerResourceId: "deployment-1",
        status: "running",
        billingStatus: "active",
        spec: "2c4g",
        createdAt: "2026-07-01T02:40:00.000Z",
        updatedAt: "2026-07-01T02:40:00.000Z"
      }
    ],
    storageVolumes: [
      {
        id: "storage-1",
        ownerAccountId: "pi-alpha",
        ownerUserId: "usr-ada",
        packageId: "basic",
        provider: "tencent-tke",
        providerResourceId: "pvc-1",
        status: "attached",
        billingStatus: "active",
        sizeGb: 20,
        createdAt: "2026-07-01T02:41:00.000Z",
        updatedAt: "2026-07-01T02:42:00.000Z"
      }
    ],
    storageAttachments: [
      {
        id: "attach-1",
        ownerAccountId: "pi-alpha",
        computeId: "compute-1",
        storageId: "storage-1",
        mountPath: "/data",
        provider: "tencent-tke",
        providerAttachmentId: "volume-mount-1",
        status: "attached",
        createdAt: "2026-07-01T02:42:00.000Z",
        updatedAt: "2026-07-01T02:42:00.000Z"
      }
    ],
    resourceUsageLogs: [
      {
        id: "usage-resource-1",
        userId: "usr-ada",
        accountId: "pi-alpha",
        workspaceId: "ws-alpha",
        computeId: "compute-1",
        storageId: "storage-1",
        attachmentId: "attach-1",
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
        reason: "manual_topup",
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
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS organizations")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS users")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS memberships")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS workspaces")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS billing_reconciliation_reports")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS support_tickets")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS evidence_ledger")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS billing_ledger")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS audit_events")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS notifications")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS runtime_operations")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS compute_resources")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS storage_volumes")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS storage_attachments")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS resource_usage_logs")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS request_usage_logs")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS wallet_transactions")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS manual_topups")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS request_usage_dedup")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE UNIQUE INDEX IF NOT EXISTS request_usage_logs_workspace_request_idx")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE UNIQUE INDEX IF NOT EXISTS request_usage_dedup_workspace_source_idx")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE UNIQUE INDEX IF NOT EXISTS resource_usage_logs_workspace_resource_source_idx")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("DROP INDEX IF EXISTS billing_ledger_dedup_idx")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE INDEX IF NOT EXISTS billing_ledger_event_lookup_idx")));
});

test("PostgresStore update reads, mutates, and writes state transactionally", async () => {
  const pool = createFakePool();
  const store = new PostgresStore({ pool });

  const result = await store.update((state) => {
    assert.deepEqual(state, emptyState());
    state.users["usr-pi-beta"] = {
      id: "usr-pi-beta",
      email: "pi-beta@example.com",
      accountId: "pi-beta",
      balance: 50,
      frozen: 0,
      holds: {},
      totalRecharged: 50
    };
    return { ok: true };
  });

  assert.deepEqual(result, { ok: true });
  assert.equal((await store.read()).users["usr-pi-beta"].balance, 50);
  assert.deepEqual(
    pool.statements
      .map((statement) => statement.sql)
      .filter((sql) => sql === "BEGIN" || sql === "COMMIT" || sql === "ROLLBACK"),
    ["BEGIN", "COMMIT"]
  );
});

test("PostgresStore billing ledger event index excludes repeatable credit and hold entries without blocking legacy duplicates", async () => {
  const pool = createFakePool();
  const store = new PostgresStore({ pool });

  await store.read();

  const indexStatement = pool.statements.find((statement) =>
    statement.sql.startsWith("CREATE INDEX IF NOT EXISTS billing_ledger_event_lookup_idx")
  )?.sql || "";
  assert.match(indexStatement, /WHERE/);
  assert.match(indexStatement, /state->>'sourceEventId'/);
  assert.match(indexStatement, /state->>'type'\) IN \('compute_debit', 'storage_debit', 'compute_hold_exhausted', 'request_debit'\)/);
  assert.doesNotMatch(indexStatement, /CREATE UNIQUE INDEX/);
  assert.doesNotMatch(indexStatement, /credit/);
  assert.doesNotMatch(indexStatement, /'compute_hold'/);
  assert.doesNotMatch(indexStatement, /storage_hold/);
});
