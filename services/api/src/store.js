import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname } from "node:path";
import pg from "pg";

const { Pool } = pg;

export function emptyState() {
  return {
    accounts: {},
    workspaces: {},
    billingLedger: [],
    audit: [],
    notifications: [],
    runtimeOperations: [],
    consoleSession: null,
    consoleSessions: {},
    identityUsers: {},
    identityInvites: {}
  };
}

function clone(value) {
  return JSON.parse(JSON.stringify(value));
}

export class MemoryStore {
  constructor(initialState = emptyState()) {
    this.state = clone(initialState);
  }

  async read() {
    return clone(this.state);
  }

  async write(nextState) {
    this.state = clone(nextState);
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
      return { ...emptyState(), ...JSON.parse(raw) };
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
    const [accounts, workspaces, billingLedger, audit, notifications, runtimeOperations, consoleState] = await Promise.all([
      client.query("SELECT id, state FROM accounts ORDER BY id"),
      client.query("SELECT id, state FROM workspaces ORDER BY id"),
      client.query("SELECT state FROM billing_ledger ORDER BY created_at, id"),
      client.query("SELECT state FROM audit_events ORDER BY created_at, id"),
      client.query("SELECT state FROM notifications ORDER BY created_at, id"),
      client.query("SELECT state FROM runtime_operations ORDER BY created_at, id"),
      client.query("SELECT key, state FROM console_state ORDER BY key")
    ]);

    for (const row of accounts.rows) state.accounts[row.id] = row.state;
    for (const row of workspaces.rows) state.workspaces[row.id] = row.state;
    state.billingLedger = billingLedger.rows.map((row) => row.state);
    state.audit = audit.rows.map((row) => row.state);
    state.notifications = notifications.rows.map((row) => row.state);
    state.runtimeOperations = runtimeOperations.rows.map((row) => row.state);
    for (const row of consoleState.rows) {
      if (row.key in state) state[row.key] = row.state;
    }
    state.consoleSessions ??= {};
    state.identityUsers ??= {};
    state.identityInvites ??= {};
    return clone(state);
  }

  async write(nextState, client = this.pool) {
    await this.ensureSchema(client);
    await client.query("TRUNCATE accounts, workspaces, billing_ledger, audit_events, notifications, runtime_operations, console_state");

    for (const account of Object.values(nextState.accounts || {})) {
      await client.query(
        "INSERT INTO accounts (id, state, updated_at) VALUES ($1, $2, now()) ON CONFLICT (id) DO UPDATE SET state = EXCLUDED.state, updated_at = now()",
        [account.id, account]
      );
    }
    for (const workspace of Object.values(nextState.workspaces || {})) {
      await client.query(
        "INSERT INTO workspaces (id, owner_account_id, state, updated_at) VALUES ($1, $2, $3, now()) ON CONFLICT (id) DO UPDATE SET owner_account_id = EXCLUDED.owner_account_id, state = EXCLUDED.state, updated_at = now()",
        [workspace.id, workspace.ownerAccountId, workspace]
      );
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
    for (const key of ["consoleSession", "consoleSessions", "identityUsers", "identityInvites"]) {
      if (nextState[key] && (typeof nextState[key] !== "object" || Object.keys(nextState[key]).length > 0)) {
        await client.query(
          "INSERT INTO console_state (key, state, updated_at) VALUES ($1, $2, now()) ON CONFLICT (key) DO UPDATE SET state = EXCLUDED.state, updated_at = now()",
          [key, nextState[key]]
        );
      }
    }
    return this.read(client);
  }

  async update(mutator) {
    const client = await this.checkoutClient();
    try {
      await client.query("BEGIN");
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
      CREATE TABLE IF NOT EXISTS accounts (
        id text PRIMARY KEY,
        state jsonb NOT NULL,
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
      CREATE TABLE IF NOT EXISTS console_state (
        key text PRIMARY KEY,
        state jsonb NOT NULL,
        updated_at timestamptz NOT NULL DEFAULT now()
      )
    `);
    this.initialized = true;
  }
}
