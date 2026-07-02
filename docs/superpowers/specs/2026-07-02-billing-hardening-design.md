# Billing Hardening Design

**Status:** Frozen for implementation on branch `feature/user-resource-billing`.

**Goal:** Harden OPL Cloud account login and billing data paths by aligning request charging, usage logs, idempotency, wallet transactions, manual top-up audit, and quota behavior with Sub2API-style billing discipline while keeping OPL-native Workspace billing objects.

**Reference Model:** Sub2API charges requests inside one SQL transaction after claiming a deduplication key, comparing request fingerprints, updating user balance and quota, and appending usage evidence. OPL Cloud should mirror that billing logic with OPL objects: user wallet, Workspace, request usage, compute/storage resource usage, wallet transactions, OPL Ledger, and audit events.

---

## Fixed Scope

1. Request billing is idempotent and conflict-aware.
   - `workspaceId + requestId` and `workspaceId + sourceEventId` identify a request billing event.
   - A deterministic `requestFingerprint` is computed from provider, model, token counts, requested amount, and source event.
   - Repeating the same request with the same fingerprint returns the existing usage log.
   - Repeating the same request with a different fingerprint throws `request_usage_fingerprint_conflict`.

2. Wallet transactions explain all wallet mutations.
   - Add `walletTransactions` as the normalized wallet timeline.
   - Every credit, hold, debit, hold capture, release, refund, request debit, and adjustment should create a wallet transaction when it changes money.
   - Existing `billingLedger` remains the public OPL Ledger. Wallet transactions are the wallet reconciliation layer.

3. Manual top-up becomes an auditable business object.
   - Add `manualTopups`.
   - Admin top-up creates a top-up record with operator, target user/account, amount, reason, before/after balance, ledger entry id, transaction id, and audit event.
   - `POST /api/accounts/credit` remains compatible but routes to this top-up flow.

4. Request usage has quota and rate-window controls.
   - User wallet can hold `requestQuota` with `limit`, `used`, `windowLimit`, `windowUsed`, and `windowStartedAt`.
   - Charging request usage increments quota only after dedup/fingerprint validation.
   - If quota would be exceeded, the request is rejected with `request_quota_exceeded` and no balance is deducted.

5. Postgres persistence exposes hard constraints.
   - Add durable tables for `wallet_transactions`, `manual_topups`, and `request_usage_dedup`.
   - Add unique indexes equivalent to:
     - `request_usage_logs(workspace_id, request_id)`
     - `request_usage_dedup(workspace_id, source_event_id)`
     - `resource_usage_logs(workspace_id, resource_type, source_event_id)`
     - `billing_ledger(account_id, workspace_id, type, source_event_id, funding_source)`
   - The existing JSON state store remains for local/dev compatibility.

6. Hot billing code has a row-level transaction path when supported.
   - Introduce a store capability for request usage billing.
   - `PostgresStore.applyRequestUsageBilling()` performs the dedup, fingerprint check, wallet update, quota update, usage log insert, ledger insert, and wallet transaction insert in one SQL transaction.
   - Memory and JSON stores keep the same behavior through the service-level state update path.

---

## Data Model

### Request Usage Dedup

```json
{
  "id": "dedup-ws-alpha-gateway_req_alpha",
  "workspaceId": "ws-alpha",
  "accountId": "pi-alpha",
  "userId": "usr-pi-alpha",
  "requestId": "req-alpha",
  "sourceEventId": "gateway_req_alpha",
  "requestFingerprint": "fp-...",
  "usageLogId": "usage-request-...",
  "createdAt": "2026-07-02T12:00:00.000Z"
}
```

### Wallet Transaction

```json
{
  "id": "wallet-tx-...",
  "userId": "usr-pi-alpha",
  "accountId": "pi-alpha",
  "workspaceId": "ws-alpha",
  "type": "request_debit",
  "amount": -0.25,
  "currency": "CNY",
  "balanceBefore": 248.7967,
  "balanceAfter": 248.5467,
  "frozenBefore": 202.16,
  "frozenAfter": 202.16,
  "sourceEventId": "gateway_req_alpha",
  "ledgerEntryId": "ledger-...",
  "usageLogId": "usage-request-...",
  "fundingSource": "available_balance",
  "createdAt": "2026-07-02T12:00:00.000Z"
}
```

### Manual Top-up

```json
{
  "id": "manual-topup-...",
  "operatorUserId": "usr-admin",
  "operatorAccountId": "admin",
  "targetUserId": "usr-pi-alpha",
  "targetAccountId": "pi-alpha",
  "amount": 500,
  "currency": "CNY",
  "reason": "manual_top_up",
  "status": "completed",
  "balanceBefore": 0,
  "balanceAfter": 500,
  "ledgerEntryId": "ledger-...",
  "walletTransactionId": "wallet-tx-...",
  "createdAt": "2026-07-02T12:00:00.000Z"
}
```

### Request Quota

```json
{
  "limit": 100000,
  "used": 1200,
  "windowLimit": 1000,
  "windowUsed": 20,
  "windowSeconds": 3600,
  "windowStartedAt": "2026-07-02T12:00:00.000Z"
}
```

---

## Behavior

1. Manual top-up:
   - Resolve target user by `accountId`, `userId`, or email.
   - Capture before balance and frozen values.
   - Increase `users[userId].balance` and `totalRecharged`.
   - Mirror compatibility `accounts[accountId]`.
   - Append `billingLedger`, `walletTransactions`, `manualTopups`, and `audit`.

2. Request usage billing:
   - Resolve workspace and wallet owner.
   - Normalize amount, tokens, provider, model, and source event.
   - Compute request fingerprint.
   - Check existing usage by `workspaceId + requestId` or `workspaceId + sourceEventId`.
   - If existing fingerprint matches, return it without quota or wallet changes.
   - If existing fingerprint differs, throw `request_usage_fingerprint_conflict`.
   - Check quota. If exceeded, throw `request_quota_exceeded`.
   - Deduct available balance only. If available balance is insufficient, charge available amount and record unpaid.
   - Append request usage log, wallet transaction, ledger debit, and audit event.

3. Resource usage billing:
   - Keep current compute/storage hold behavior.
   - Add durable unique indexes and wallet transaction rows for resource debits when money is charged.
   - Repeated settlement with the same source event returns existing ledger/usage evidence without duplicate charges.

4. Persistence:
   - Empty state includes `walletTransactions`, `manualTopups`, and `requestUsageDedup`.
   - Postgres read/write persists the new collections.
   - Postgres schema declares the hard unique indexes used for commercial deployments.

---

## Non-Goals

- External payment provider checkout.
- Tax invoices.
- OAuth/OIDC/TOTP.
- Full multi-replica persisted sessions.
- Replacing all existing domain state tables with fully normalized relational tables.

---

## Acceptance Criteria

1. Repeating the same request usage event with the same fingerprint returns the original log and does not double charge.
2. Repeating the same request id or source event with different usage/cost throws `request_usage_fingerprint_conflict`.
3. Request quota exhaustion rejects billing before balance changes.
4. Successful request usage writes a request usage log, wallet transaction, ledger debit, and audit event.
5. Admin manual top-up writes a manual top-up record, wallet transaction, ledger credit, and audit event with before/after balance.
6. PostgresStore exposes tables and unique indexes for dedup, request usage, resource usage, billing ledger, wallet transactions, and manual top-ups.
7. Existing MemoryStore/JsonFileStore tests still pass.
