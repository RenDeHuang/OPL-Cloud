# Pilot B Rolling Four-Lane Design

**Date:** 2026-07-16
**Status:** Approved for implementation by the product owner
**Base revision:** `b83a61e9767dff46e4ad79891e93267eb71d6f9e`
**Integration branch:** `integration/pilot-launch-b`

## Outcome

Ship a public, manually onboarded, paid Basic Pilot without SSO, self-signup,
automatic renewal, Pro/GPU sales, multi-replica HA, Redis, Temporal, a second
wallet, or copied billing/usage databases.

The implementation keeps the approved Workspace-first Concept B UI. The UI is
being rewritten to Vue in an existing worktree and is not implemented by the
three backend lanes in this design.

## Frozen Ownership

| Fact or side effect | Sole authority | Control Plane responsibility |
| --- | --- | --- |
| Spendable balance and debit/refund | Sub2API | Map account, preflight, invoke deterministic adjustment, persist operation state |
| API Key and request usage | Sub2API | Select one active `opl-workspace` Key and expose a strict customer-safe projection |
| Tencent CVM/CBS and Kubernetes runtime | Fabric | Orchestrate stable calls and retain customer entitlement state |
| Billing and reconciliation evidence | Ledger | Query tenant receipts and append reconciliation reports |
| Identity, quote, launch operation, entitlement | Control Plane | Authoritative owner |

No lane may introduce a second wallet, Key table, usage table, billing fact
table, provider writer, or direct browser call to Sub2API.

## Pilot Scope

Included:

- Operator-created Console users with a manually verified `sub2apiUserId`.
- Operator password reset, user disable, soft delete, and session revocation.
- One server-computed Workspace quote containing compute, storage, and total.
- One total live-balance preflight before a Workspace launch is accepted.
- `POST /api/workspace-launches` plus account-scoped list/detail polling.
- Durable launch state in the existing `control_plane_runtime_operations` table.
- Background continuation after the browser closes or the Control Plane restarts.
- Tenant billing history projected live from Ledger.
- Request usage and aggregate stats projected live from Sub2API.
- Owner-only Runtime password reveal and rotation.
- One retained Provider Acceptance slot and production evidence.

Explicitly excluded:

- SSO, OIDC, self-registration, email verification, or password recovery email.
- Customer Key CRUD. An operator creates exactly one active `opl-workspace` Key.
- Pro/GPU sale, external payment checkout, automatic renewal, or automatic
  reconciliation repair.
- Two Control Plane replicas, rolling HA, Redis, Temporal, or a new queue.
- Persisting Runtime passwords, Gateway Keys, raw Sub2API responses, usage rows,
  or copied Ledger receipts in OPL PostgreSQL.

## Lane Boundaries

### Existing UI Lane

Branch: `feat/vue-console-real-api`

Allowed ownership:

- `apps/console-ui/**`
- UI-only tests under `tests/ui/**`
- `package.json`, `package-lock.json`, `vite.config.ts`, `index.html`
- The Console framework field in
  `packages/contracts/opl-cloud-service-boundary-contract.json`

Forbidden in the UI checkpoint commit:

- `services/**`
- Provider Acceptance tools or workflows
- Billing, identity, or Runtime backend contracts beyond the Vue framework fact

The existing WIP contains backend edits. They must remain preserved in that
worktree but must be split from the UI checkpoint. The backend lanes below are
the merge authority for overlapping behavior.

### Slides 1-3: Transaction Entry

Branch: `feat/slides-1-3-launch-operation`

Owns:

- Manual identity lifecycle gaps, limited to operator password reset.
- Workspace total quote and total live-balance preflight.
- Durable Workspace launch API, customer-safe polling, and launch worker.
- Stable child identities for compute, storage, attachment, Gateway Secret,
  Runtime, Workspace receipt, and audit result.

Does not own:

- Ledger history, Sub2API usage, Runtime credential authorization, Fabric
  provider implementation, UI, deployment, or production evidence.

### Slides 5 and 7: Customer-Visible Facts

Branch: `feat/slides-5-7-customer-facts`

Owns:

- Ledger receipt list client and tenant-safe billing DTO.
- Sub2API request usage, aggregate stats, balance history adapter, and strict
  integer-micros projection.
- Server-computed reconciliation report across Sub2API, local billing
  operations, Fabric provider facts, and Ledger receipts.

Does not own:

- Any copied usage/receipt table, Key CRUD, launch orchestration, Runtime
  password flow, UI, or provider mutation.

### Slide 6: Runtime Owner Isolation

Branch: `fix/slide-6-runtime-owner-isolation`

Owns:

- Non-secret Runtime status for account members.
- Owner-user-only password reveal and password rotation.
- `private, no-store` responses and credential evidence without the credential.
- The minimum Fabric manifest change needed to restart a Runtime after a stable
  credential revision changes.

Does not own:

- SSO or identity-bound Runtime requests, Gateway Key rotation, launch flow,
  billing history, usage, UI, or production rollout.

### Slides 4 and 8-10: Production Proof

Branch: `ops/slides-4-8-10-production`

This branch is created only after the three backend lanes and Vue checkpoint
are integrated. It owns migration rehearsal, backup restore, immutable image
build/deploy, first-slot bootstrap, Provider Acceptance, live QA, rollback, and
evidence updates. It does not add product behavior unrelated to a proven
production blocker.

## Workspace Launch Contract

### Quote

The existing `POST /api/pricing/preview` accepts
`{"resourceType":"workspace","packageId":"basic","sizeGb":10}` and returns:

```json
{
  "resourceType": "workspace",
  "pricingVersion": "2026-07-16-opl-monthly-v2",
  "billingUnit": "calendar_month",
  "compute": {"monthlyPriceCnyCents": 35000, "chargeUsdMicros": 50000000},
  "storage": {"monthlyPriceCnyCents": 1800, "chargeUsdMicros": 2571429},
  "totalMonthlyPriceCnyCents": 36800,
  "totalChargeUsdMicros": 52571429
}
```

All additions are checked integer additions. The frontend does not derive a
price.

### Submission

`POST /api/workspace-launches` requires an authenticated owner session and an
`Idempotency-Key`. The server:

1. validates the account, package, storage size, and primary-Workspace rule;
2. computes the total quote;
3. performs read-only Fabric preflight for compute and storage;
4. reads the mapped Sub2API user's live balance once and rejects an insufficient
   total before any debit or provider mutation;
5. writes a `workspace.launch` RuntimeOperation containing only the request
   fingerprint, phase, stable child IDs, safe result references, and error code;
6. returns `202` with the operation ID.

The total balance check is a precondition, not a wallet hold. Each existing
resource purchase still confirms its own balance immediately before its
deterministic debit. If another Sub2API consumer spends between the total
preflight and a later child debit, the launch remains recoverable at its durable
phase; it never repeats a completed debit or provider purchase.

### Continuation

The launch worker advances these phases:

```text
queued
  -> compute
  -> storage
  -> attachment
  -> gateway_secret_and_runtime
  -> workspace_receipt
  -> succeeded
```

Each phase uses IDs derived from the launch operation ID. Existing monthly
purchase and Fabric idempotency remain the side-effect authority. The worker
does not execute a later phase until the current persisted fact is complete.
`manual_review` and reconciliation mismatch stop the chain. Retryable dependency
failure remains pending with an error code and is retried by the worker.

The existing provider reconciliation worker starts immediately on process
startup in production. It also advances pending `workspace.launch` operations;
the POST handler wakes an immediate background attempt. No new queue or table is
introduced.

`GET /api/workspace-launches` lets a reopened browser discover the active or
latest account launch. `GET /api/workspace-launches/{id}` is account scoped and
returns no credentials, raw provider payload, redeem code, or internal error.

## Customer Facts Contract

### Billing

`GET /api/billing/receipts` passes the signed-in account ID to Ledger's existing
paginated list API. Control Plane validates every returned receipt and exposes
only billing types and a customer allowlist. `GET /api/billing/receipts/{id}`
uses the same projection. Receipts are never copied into Control Plane storage.

### Gateway Usage

`GET /api/gateway/usage` and `GET /api/gateway/usage/stats` first resolve exactly
one active `opl-workspace` Key, then call Sub2API with both `user_id` and
`api_key_id`. Every row is checked again. Unknown nested admin fields, account
internals, prompts, responses, IP addresses, user agents, Keys, and raw payloads
are discarded during decoding.

`actual_cost` and `total_actual_cost` are converted once from JSON decimal to
integer USD micros. A value that cannot be represented safely is unavailable;
it is never returned as zero.

### Reconciliation

The operator-only reconciliation command computes the report on the server. It
compares deterministic Sub2API balance-history entries, Control Plane billing
operations, Fabric provider IDs, and Ledger receipt IDs. It appends one report
to Ledger and stores only the latest guard projection in Control Plane.

The report never changes money or resource state. Missing, ambiguous, or
unavailable evidence yields `mismatch`, which blocks new purchases until a later
`ok` report supersedes the local guard.

## Runtime Credential Contract

The existing Runtime status command becomes non-secret for every account member.
Only the user whose ID equals `Workspace.ownerUserId` can call reveal or rotate.
Both responses use `Cache-Control: private, no-store`; the password exists only
in the Fabric response and the current HTTP response.

Rotation reuses Fabric's idempotent `CreateWorkspaceRuntime` apply path with a
new stable credential revision. The pod-template annotation includes that
revision, so Kubernetes rolls the Runtime when the Secret changes. The receipt
contains credential version/fingerprint metadata only.

Pilot isolation means the owner is the only Console identity allowed to obtain
the Runtime password. It does not mean every Runtime HTTP request is bound to
the Console session. That stronger property requires Runtime SSO and is outside
this Pilot.

## Integration and Merge Order

The root worktree is the only integration owner. Feature worktrees never merge
one another directly.

1. Merge Slides 1-3 into `integration/pilot-launch-b`.
2. Rebase and merge Slides 5/7.
3. Rebase and merge Slide 6.
4. Split the existing Vue WIP, rebase the UI-only checkpoint, and merge it.
5. Create and execute the production branch from that integration HEAD.
6. Run final full verification and open one PR from integration to `main`.

Mechanical conflicts in `server.go`, `service.go`, shared test fakes, and machine
contracts are resolved only on the integration branch. Business behavior stays
with its owning lane.

## Completion Gates

A backend lane is mergeable only when its focused tests and the complete Go test
suite pass from a clean worktree. The integration branch must also pass contract
and TypeScript tests. Production readiness additionally requires:

- duplicate-data preflight;
- successful PostgreSQL backup and isolated restore;
- migration journal and restart-no-replay evidence;
- immutable Cloud and Workspace image digests built from integration HEAD;
- one retained fixed-slot Provider Acceptance record;
- normal live QA with one real model request and usage readback;
- deployed readiness, browser flow, WebSocket, rollback, and reconciliation
  evidence.

No fake test, contract edit, or green readiness endpoint substitutes for these
runtime facts.
