# User Resource Billing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement user wallet billing, resource usage logs, request usage logs, and Postgres persistence for OPL Cloud.

**Architecture:** Keep the existing Store abstraction and OPL Ledger, but move the billable wallet snapshot to `state.users[userId]`. Keep `accounts` as a compatibility alias while adding `resourceUsageLogs` and `requestUsageLogs` as durable usage evidence tables.

**Tech Stack:** Node.js, `node:test`, React/Vite, existing `MemoryStore`/`JsonFileStore`/`PostgresStore`.

---

### Task 1: Persist Usage Collections

**Files:**
- Modify: `packages/console/src/store.js`
- Modify: `tests/persistence/postgres-store.test.js`

- [ ] **Step 1: Write failing persistence test**

Add `resourceUsageLogs` and `requestUsageLogs` fixtures to `tests/persistence/postgres-store.test.js` and assert round-trip persistence plus table creation for `resource_usage_logs` and `request_usage_logs`.

- [ ] **Step 2: Run red test**

Run: `node --test tests/persistence/postgres-store.test.js`

Expected: FAIL because `PostgresStore` does not create/read/write the two new usage tables.

- [ ] **Step 3: Implement persistence**

Update `emptyState()`, `PostgresStore.read()`, `PostgresStore.write()`, `PostgresStore.ensureSchema()`, and the fake pool in the persistence test to include:

```js
resourceUsageLogs: [],
requestUsageLogs: []
```

Tables:

```sql
CREATE TABLE IF NOT EXISTS resource_usage_logs (
  id text PRIMARY KEY,
  user_id text NOT NULL,
  account_id text NOT NULL,
  workspace_id text NOT NULL,
  resource_type text NOT NULL,
  state jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
)
```

```sql
CREATE TABLE IF NOT EXISTS request_usage_logs (
  id text PRIMARY KEY,
  user_id text NOT NULL,
  account_id text NOT NULL,
  workspace_id text NOT NULL,
  request_id text NOT NULL,
  state jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
)
```

- [ ] **Step 4: Run green test**

Run: `node --test tests/persistence/postgres-store.test.js`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/console/src/store.js tests/persistence/postgres-store.test.js
git commit -m "feat: persist resource usage logs"
```

### Task 2: Move Wallet Snapshot To Users

**Files:**
- Modify: `packages/console/src/opl-cloud.js`
- Modify: `packages/console/api/auth.js`
- Modify: `tests/billing/prepaid-ledger-billing.test.js`
- Modify: `tests/management/auth-session.test.js`

- [ ] **Step 1: Write failing wallet migration tests**

Add tests proving:

```js
assert.equal(state.user.balance, 248.7967);
assert.equal(state.wallet.balance, 248.7967);
assert.equal(state.user.totalRecharged, 250);
assert.equal(state.billingLedger[0].userId, "usr-pi-alpha");
```

For auth-backed users, verify legacy `accounts[accountId]` balance migrates into `users[userId]`.

- [ ] **Step 2: Run red tests**

Run: `node --test tests/billing/prepaid-ledger-billing.test.js tests/management/auth-session.test.js`

Expected: FAIL because wallet fields are still account-scoped and ledger entries do not include `userId`.

- [ ] **Step 3: Implement wallet helpers**

Add helpers in `packages/console/src/opl-cloud.js`:

```js
function ensureBillingCollections(state) {
  state.resourceUsageLogs ??= [];
  state.requestUsageLogs ??= [];
}

function ensureUserWallet(state, { userId, accountId, email = "" }) {
  const id = userId || Object.values(state.users || {}).find((user) => user.accountId === accountId)?.id || `usr-${accountId}`;
  state.users ??= {};
  const legacyAccount = state.accounts?.[accountId] || {};
  state.users[id] ??= { id, email, accountId, status: "active" };
  const user = state.users[id];
  user.accountId ||= accountId;
  user.balance ??= Number(legacyAccount.balance || 0);
  user.frozen ??= Number(legacyAccount.frozen || 0);
  user.holds ??= legacyAccount.holds || {};
  user.totalRecharged ??= 0;
  state.accounts ??= {};
  state.accounts[accountId] = {
    ...state.accounts[accountId],
    id: accountId,
    balance: user.balance,
    frozen: user.frozen,
    holds: user.holds
  };
  return user;
}
```

Update `creditAccount`, `createWorkspace`, `settleBilling`, `billingLedger`, `getState`, and `operatorSummary` to use the user wallet and keep `account` response compatibility.

- [ ] **Step 4: Run green tests**

Run: `node --test tests/billing/prepaid-ledger-billing.test.js tests/management/auth-session.test.js`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/console/src/opl-cloud.js packages/console/api/auth.js tests/billing/prepaid-ledger-billing.test.js tests/management/auth-session.test.js
git commit -m "feat: bill against user wallets"
```

### Task 3: Record Compute, Storage, And Request Usage

**Files:**
- Modify: `packages/console/src/opl-cloud.js`
- Modify: `packages/console/api/server.js`
- Modify: `tests/billing/prepaid-ledger-billing.test.js`
- Modify: `tests/management/management-api.test.js`

- [ ] **Step 1: Write failing usage tests**

Add tests proving hourly settlement writes:

```js
assert.deepEqual(state.resourceUsageLogs.map((log) => log.resourceType), ["compute", "storage"]);
assert.equal(state.resourceUsageLogs[0].unit, "hour");
assert.equal(state.resourceUsageLogs[1].unit, "gb_hour");
assert.equal(state.resourceUsageLogs.every((log) => log.userId === "usr-pi-alpha"), true);
```

Add request usage API test:

```js
const usage = await postJson(origin, "/api/billing/request-usage", {
  workspaceId: "ws-alpha",
  requestId: "req-alpha",
  provider: "openai",
  model: "gpt-5",
  inputTokens: 1000,
  outputTokens: 500,
  amount: 0.25,
  sourceEventId: "gateway_req_alpha"
}, { cookie, "x-opl-csrf": csrfToken });
assert.equal(usage.response.status, 200);
```

- [ ] **Step 2: Run red tests**

Run: `node --test tests/billing/prepaid-ledger-billing.test.js tests/management/management-api.test.js`

Expected: FAIL because usage logs and request usage route do not exist.

- [ ] **Step 3: Implement usage recording**

Add `recordResourceUsage()` and `recordRequestUsage()` methods to `OplCloudService`. Call `recordResourceUsage()` from `debitWorkspaceUsage()` after debit ledger entries are created. Add `POST /api/billing/request-usage` route in `server.js`.

- [ ] **Step 4: Run green tests**

Run: `node --test tests/billing/prepaid-ledger-billing.test.js tests/management/management-api.test.js`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/console/src/opl-cloud.js packages/console/api/server.js tests/billing/prepaid-ledger-billing.test.js tests/management/management-api.test.js
git commit -m "feat: record resource and request usage"
```

### Task 4: Surface Wallet And Usage In Console UI

**Files:**
- Modify: `packages/console/ui/pages/ConsolePage.jsx`
- Modify: `tests/ui/console-information-architecture.test.js`

- [ ] **Step 1: Write failing UI IA test**

Assert the Console UI source includes:

```js
assert.match(source, /用户钱包/);
assert.match(source, /Compute 小时/);
assert.match(source, /Storage GB-hour/);
assert.match(source, /请求用量/);
```

- [ ] **Step 2: Run red UI test**

Run: `node --test tests/ui/console-information-architecture.test.js`

Expected: FAIL because these labels are not present.

- [ ] **Step 3: Implement UI sections**

Show wallet balance/frozen/available/total recharged on Billing and Admin views. Add separate event lists for `resourceUsageLogs` and `requestUsageLogs`.

- [ ] **Step 4: Run green UI test**

Run: `node --test tests/ui/console-information-architecture.test.js`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/console/ui/pages/ConsolePage.jsx tests/ui/console-information-architecture.test.js
git commit -m "feat: show user wallet usage billing"
```

### Task 5: Final Verification

**Files:**
- Verify all changed files.

- [ ] **Step 1: Run full tests**

Run: `npm test`

Expected: PASS.

- [ ] **Step 2: Run production build**

Run: `npm run build`

Expected: PASS.

- [ ] **Step 3: Start local worktree server on a non-main port**

Run:

```bash
PORT=8797 npm start
```

Expected: server listens on `http://127.0.0.1:8797`.

- [ ] **Step 4: Commit any final docs or fixes**

Commit only files from this worktree branch.
