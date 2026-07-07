# User Resource Billing Design

**Status:** Frozen for implementation on branch `feature/user-resource-billing`.

**Goal:** Upgrade OPL Console billing from account-scoped Workspace ledger entries to a user wallet plus resource-metered billing system aligned with the One Person Lab Cloud product model.

**Reference Model:** Sub2API keeps user balance, API keys, quota, usage logs, payment orders, and audit logs as durable database records. OPL Cloud should adopt the same billing discipline, but use OPL-native billable objects: OPL User, OPL Workspace, compute hours, storage GB-hours, request usage, OPL Ledger entries, and audit events.

---

## Fixed Decisions

1. User is the wallet owner.
   - `state.users[userId].balance` is the current spendable balance.
   - `state.users[userId].frozen` is the total prepaid hold amount.
   - `state.users[userId].holds.compute` and `state.users[userId].holds.storage` track frozen resource pools.
   - `state.users[userId].totalRecharged` is cumulative manual or payment recharge value.
   - `state.users[userId].status` controls login and billing eligibility.

2. Account records become compatibility aliases.
   - Existing `state.accounts[accountId]` remains readable during migration.
   - Auth users already have `accountId`; migration maps legacy account balances and holds to the matching user.
   - New billing logic must debit by `userId`, not by `accountId`.
   - Existing API inputs may still accept `accountId` temporarily, but server auth resolves it to the logged-in user.

3. OPL Ledger remains the money truth.
   - `billingLedger` stays append-only at the domain level.
   - Every wallet mutation writes a ledger entry with `userId`, `workspaceId`, `type`, `amount`, `currency`, `sourceEventId`, and `metadata`.
   - Recharge, hold, debit, hold release, refund, adjustment, and request usage all write ledger entries.
   - `users.balance` and `users.frozen` are materialized wallet snapshots derived and updated with ledger mutations.

4. Resource usage logs explain every compute/storage debit.
   - Add `resourceUsageLogs` to control-plane state and Postgres persistence.
   - Compute rows record billable hours.
   - Storage rows record GB-hours.
   - Each row links to the ledger debit it explains.

5. Request usage logs are separate from infrastructure resource logs.
   - Add `requestUsageLogs` to control-plane state and Postgres persistence.
   - Request rows support future Gateway integration and manual/API recording now.
   - Token/request usage debits the same user wallet and writes both request usage and ledger rows.

6. Workspace billing owner is explicit.
   - New Workspaces store `ownerUserId`.
   - Existing `ownerAccountId` remains for compatibility and historical filtering.
   - UI and API should display the user wallet as the billing owner.

7. Hourly settlement is idempotent.
   - `sourceEventId` remains the idempotency key for billing ticks.
   - Repeating a billing tick with the same source event must not duplicate resource usage rows or ledger debits.

8. Storage billing continues until storage is destroyed.
   - Running compute bills compute + storage.
   - Stopped or destroyed compute bills storage only while disk/PVC remains retained.
   - Destroyed storage stops storage billing.

---

## Data Model

### User Wallet

Stored in `state.users[userId]` and persisted by the existing `users` Postgres table:

```json
{
  "id": "usr-pi-demo",
  "email": "pi-demo@opl.local",
  "role": "pi",
  "status": "active",
  "accountId": "pi-demo",
  "balance": 500,
  "frozen": 202.16,
  "holds": {
    "compute": 201.6,
    "storage": 0.56
  },
  "totalRecharged": 500
}
```

### Resource Usage Log

Stored in `state.resourceUsageLogs[]` and persisted in a dedicated `resource_usage_logs` Postgres table:

```json
{
  "id": "usage-resource-...",
  "userId": "usr-pi-demo",
  "accountId": "pi-demo",
  "workspaceId": "ws-alpha",
  "resourceType": "compute",
  "quantity": 1,
  "unit": "hour",
  "unitPrice": 2.4,
  "amount": 2.4,
  "currency": "CNY",
  "periodStart": "2026-07-02T10:00:00.000Z",
  "periodEnd": "2026-07-02T11:00:00.000Z",
  "ledgerEntryId": "ledger-...",
  "sourceEventId": "billing_tick_20260702_10",
  "createdAt": "2026-07-02T11:00:00.000Z",
  "metadata": {
    "packageId": "basic",
    "baseHourly": 2,
    "markup": 0.2
  }
}
```

Storage usage uses:

```json
{
  "resourceType": "storage",
  "quantity": 20,
  "unit": "gb_hour",
  "unitPrice": 0.0006667,
  "amount": 0.0133
}
```

### Request Usage Log

Stored in `state.requestUsageLogs[]` and persisted in a dedicated `request_usage_logs` Postgres table:

```json
{
  "id": "usage-request-...",
  "userId": "usr-pi-demo",
  "accountId": "pi-demo",
  "workspaceId": "ws-alpha",
  "requestId": "req-...",
  "provider": "openai",
  "model": "gpt-5",
  "inputTokens": 1200,
  "outputTokens": 400,
  "unitPrice": 0.00001,
  "amount": 0.016,
  "currency": "CNY",
  "ledgerEntryId": "ledger-...",
  "sourceEventId": "gateway_req_...",
  "createdAt": "2026-07-02T11:01:00.000Z",
  "metadata": {}
}
```

---

## API Changes

### Keep Compatible

- `POST /api/accounts/credit` remains temporarily available for the admin UI.
- It resolves `accountId` to a user and writes to that user's wallet.
- Response includes both `userId` and `accountId`.

### Add User-Oriented Billing APIs

- `POST /api/users/credit`
  - Admin only.
  - Inputs: `userId` or `email`, `amount`, `reason`.
  - Writes wallet snapshot + billing ledger.

- `POST /api/billing/request-usage`
  - Authenticated internal/admin endpoint for Gateway or Workspace usage reporters.
  - Inputs: `workspaceId`, `requestId`, `provider`, `model`, token counts or direct `amount`, `sourceEventId`.
  - Idempotent on `sourceEventId`.

- `GET /api/state`
  - Returns `user`, `wallet`, `resourceUsageLogs`, `requestUsageLogs`, and `billingLedger` for the logged-in user scope.

---

## Migration Rules

1. On read/update, initialize missing collections:
   - `resourceUsageLogs: []`
   - `requestUsageLogs: []`

2. On auth user load or service write, ensure user wallet fields exist:
   - `balance`
   - `frozen`
   - `holds`
   - `totalRecharged`

3. Legacy balance migration:
   - Find each auth user with `accountId`.
   - If the user wallet has no balance/frozen/holds and `accounts[accountId]` exists, copy account wallet fields to the user.
   - Keep the account record for historical queries.

4. New writes update user wallet first and mirror account balance only for compatibility responses.

---

## UI Changes

Billing page should show:

- Current user wallet: balance, frozen, available, total recharged.
- Manual recharge records.
- Compute hourly usage rows.
- Storage hourly usage rows.
- Request/token usage rows when present.
- Billing ledger as the immutable money timeline.

Workspace detail should show:

- Billing owner.
- Current package.
- Compute hourly price.
- Storage GB-hour price.
- Latest hourly settlement.

Admin page should show:

- User list with role, status, balance, frozen, total recharged.
- Manual recharge by user email/userId.
- Disable/enable user action.

---

## Non-Goals For This Implementation

- External payment provider integration.
- Self-service checkout.
- Tax invoice generation.
- Full organization wallet pooling.
- Real-time Workspace Gateway token capture unless a caller posts request usage to the new endpoint.

---

## Acceptance Criteria

1. Existing local accounts still log in.
2. Existing account balances migrate to user wallets.
3. Admin manual recharge increases `users[userId].balance`, `users[userId].totalRecharged`, and writes a ledger credit.
4. Compute/storage creation checks the user wallet and freezes resource holds before cloud mutation.
5. Hourly settlement writes compute/storage resource usage logs and matching debit ledger entries.
6. Stopped compute stops compute usage but keeps storage usage.
7. Request usage endpoint writes request usage logs and debits the user wallet idempotently.
8. PostgresStore persists `resource_usage_logs` and `request_usage_logs`.
9. Console Billing UI displays user wallet plus compute/storage/request usage sections.
10. Full `npm test` and `npm run build` pass in the worktree.
