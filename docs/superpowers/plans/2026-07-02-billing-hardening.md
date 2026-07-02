# Billing Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Harden OPL Cloud request billing by adding Sub2API-style deduplication, request fingerprints, quota checks, wallet transactions, manual top-up audit records, and Postgres persistence constraints.

**Architecture:** Keep the existing `OplCloudService` and Store abstraction. Add state-backed behavior for Memory/JSON stores and Postgres schema/index support for commercial deployments, with a future-ready `applyRequestUsageBilling` store capability when a store supports row-level billing transactions.

**Tech Stack:** Node.js, `node:test`, existing `MemoryStore`/`JsonFileStore`/`PostgresStore`, React/Vite UI unchanged except state exposure.

---

### Task 1: Add Billing Hardening Persistence

**Files:**
- Modify: `packages/console/src/store.js`
- Modify: `tests/persistence/postgres-store.test.js`

- [ ] **Step 1: Write failing persistence test**

Add fixtures and assertions for `walletTransactions`, `manualTopups`, and `requestUsageDedup`. Assert Postgres schema contains the new tables and unique indexes.

- [ ] **Step 2: Run red test**

Run: `node --test tests/persistence/postgres-store.test.js`

Expected: FAIL because the new collections and indexes are not persisted.

- [ ] **Step 3: Implement persistence**

Update `emptyState()`, `PostgresStore.read()`, `PostgresStore.write()`, and `PostgresStore.ensureSchema()` with:

```js
walletTransactions: [],
manualTopups: [],
requestUsageDedup: []
```

Add tables:

```sql
wallet_transactions(id, user_id, account_id, workspace_id, transaction_type, source_event_id, state, created_at)
manual_topups(id, operator_user_id, target_user_id, target_account_id, state, created_at)
request_usage_dedup(id, workspace_id, source_event_id, request_id, request_fingerprint, usage_log_id, state, created_at)
```

Add unique indexes:

```sql
request_usage_logs(workspace_id, request_id)
request_usage_dedup(workspace_id, source_event_id)
resource_usage_logs(workspace_id, resource_type, source_event_id)
billing_ledger(account_id, workspace_id, ((state->>'type')), ((state->>'sourceEventId')), COALESCE(state->'metadata'->>'fundingSource', ''))
```

- [ ] **Step 4: Run green test**

Run: `node --test tests/persistence/postgres-store.test.js`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/console/src/store.js tests/persistence/postgres-store.test.js docs/superpowers/specs/2026-07-02-billing-hardening-design.md docs/superpowers/plans/2026-07-02-billing-hardening.md
git commit -m "feat: persist billing hardening records"
```

### Task 2: Add Wallet Transactions And Manual Top-up Audit

**Files:**
- Modify: `packages/console/src/opl-cloud.js`
- Modify: `packages/console/api/server.js`
- Modify: `tests/billing/prepaid-ledger-billing.test.js`
- Modify: `tests/management/auth-session.test.js`

- [ ] **Step 1: Write failing manual top-up tests**

Assert `creditAccount()` records:

```js
state.manualTopups.length === 1
state.walletTransactions.some((tx) => tx.type === "credit")
state.billingLedger.some((entry) => entry.type === "credit")
```

For authenticated admin API top-up, assert `operatorUserId` is recorded.

- [ ] **Step 2: Run red tests**

Run: `node --test tests/billing/prepaid-ledger-billing.test.js tests/management/auth-session.test.js`

Expected: FAIL because manual top-ups and wallet transactions are missing.

- [ ] **Step 3: Implement top-up audit**

Add `walletTransactions`, `manualTopups`, and helper methods to append wallet transactions with before/after balances. Update `/api/accounts/credit` to pass authenticated operator info into `creditAccount()`.

- [ ] **Step 4: Run green tests**

Run: `node --test tests/billing/prepaid-ledger-billing.test.js tests/management/auth-session.test.js`

Expected: PASS.

### Task 3: Add Request Dedup, Fingerprints, And Quota

**Files:**
- Modify: `packages/console/src/opl-cloud.js`
- Modify: `tests/billing/prepaid-ledger-billing.test.js`

- [ ] **Step 1: Write failing request billing tests**

Add tests for:

```js
same request + same fingerprint returns existing log and charges once
same request + different amount throws request_usage_fingerprint_conflict
quota exhausted throws request_quota_exceeded and does not change wallet
successful request usage writes wallet transaction and request dedup row
```

- [ ] **Step 2: Run red tests**

Run: `node --test tests/billing/prepaid-ledger-billing.test.js`

Expected: FAIL because fingerprint conflicts, quota, wallet transaction, and dedup rows are not implemented.

- [ ] **Step 3: Implement request billing logic**

Add deterministic fingerprint generation, dedup checks, quota checks, quota increments, request usage dedup records, wallet transaction rows, and ledger linkage.

- [ ] **Step 4: Run green tests**

Run: `node --test tests/billing/prepaid-ledger-billing.test.js`

Expected: PASS.

### Task 4: Expose Hardened Billing State

**Files:**
- Modify: `packages/console/src/opl-cloud.js`
- Modify: `tests/ui/console-information-architecture.test.js`

- [ ] **Step 1: Write state/UI source assertions**

Assert `getState()` exposes `walletTransactions`, `manualTopups`, and `requestUsageDedup` for the scoped account. Keep UI source assertions lightweight unless product UI changes are requested separately.

- [ ] **Step 2: Run red test**

Run: `node --test tests/ui/console-information-architecture.test.js tests/billing/prepaid-ledger-billing.test.js`

Expected: FAIL until state exposure is added.

- [ ] **Step 3: Implement state exposure**

Filter hardened billing records by scoped account and return them from `getState()` and `operatorSummary()` where appropriate.

- [ ] **Step 4: Run green test**

Run: `node --test tests/ui/console-information-architecture.test.js tests/billing/prepaid-ledger-billing.test.js`

Expected: PASS.

### Task 5: Final Verification

**Files:**
- Verify all changed files.

- [ ] **Step 1: Run full tests**

Run: `npm test`

Expected: PASS.

- [ ] **Step 2: Run build**

Run: `npm run build`

Expected: PASS.

- [ ] **Step 3: Diff check**

Run: `git diff --check`

Expected: no whitespace errors.
