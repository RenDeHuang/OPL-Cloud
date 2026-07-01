import assert from "node:assert/strict";
import test from "node:test";

import { emptyState, PostgresStore } from "../services/api/src/store.js";

function createFakePool() {
  const tables = {
    accounts: new Map(),
    workspaces: new Map(),
    billing_ledger: [],
    audit_events: [],
    runtime_operations: []
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
      if (normalized.startsWith("CREATE TABLE IF NOT EXISTS")) {
        return { rows: [] };
      }
      if (normalized.startsWith("TRUNCATE accounts, workspaces, billing_ledger, audit_events, runtime_operations")) {
        tables.accounts.clear();
        tables.workspaces.clear();
        tables.billing_ledger = [];
        tables.audit_events = [];
        tables.runtime_operations = [];
        return { rows: [] };
      }
      if (normalized.startsWith("SELECT id, state FROM accounts")) {
        return { rows: [...tables.accounts.entries()].map(([id, state]) => ({ id, state })) };
      }
      if (normalized.startsWith("SELECT id, state FROM workspaces")) {
        return { rows: [...tables.workspaces.entries()].map(([id, state]) => ({ id, state })) };
      }
      if (normalized.startsWith("SELECT state FROM billing_ledger")) {
        return { rows: tables.billing_ledger.map((state) => ({ state })) };
      }
      if (normalized.startsWith("SELECT state FROM audit_events")) {
        return { rows: tables.audit_events.map((state) => ({ state })) };
      }
      if (normalized.startsWith("INSERT INTO accounts")) {
        tables.accounts.set(params[0], params[1]);
        return { rows: [] };
      }
      if (normalized.startsWith("INSERT INTO workspaces")) {
        tables.workspaces.set(params[0], params[2]);
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
    accounts: {
      "pi-alpha": { id: "pi-alpha", balance: 200, frozen: 0, createdAt: "2026-07-01T00:00:00.000Z" }
    },
    workspaces: {
      "ws-alpha": {
        id: "ws-alpha",
        ownerAccountId: "pi-alpha",
        name: "Grant Lab",
        slug: "grant-lab-alpha",
        url: "https://grant-lab-alpha.oplcloud.cn/?token=share_alpha"
      }
    },
    billingLedger: [
      { id: "ledger-1", workspaceId: "ws-alpha", accountId: "pi-alpha", type: "storage_hold", amount: 0.5 }
    ],
    audit: [
      { id: "audit-1", workspaceId: "ws-alpha", accountId: "pi-alpha", type: "workspace.created" }
    ]
  };

  await store.write(state);
  const persisted = await store.read();

  assert.deepEqual(persisted, state);
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS accounts")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS workspaces")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS billing_ledger")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS audit_events")));
  assert.ok(pool.statements.some((statement) => statement.sql.includes("CREATE TABLE IF NOT EXISTS runtime_operations")));
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
