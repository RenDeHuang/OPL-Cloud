# Open MVP Provider And Gateway Budget Design

## Classification

This is an implementation design for the approved P0-1 and P0-2 delivery. It
does not replace product, architecture, invariant, status, or roadmap owners.

## Objective And Ownership

- Objective: close package availability and per-Workspace Gateway budget for
  the customer-owned open MVP without changing the upper-layer SSOT.
- P0-1 owner: Fabric owns provider package availability; Control Plane owns
  launch admission; Console owns presentation.
- P0-2 owner: Control Plane owns the Workspace-scoped operation and customer
  API; Sub2API remains the only Key, quota, usage, and spendable-wallet truth.
- Interface owner: `packages/contracts/opl-cloud-console-source-truth-contract.json`
  admits the new customer-safe Workspace budget projection and mutation.
- Completion evidence: focused Go, TypeScript contract, and UI tests; then one
  successful `npm run verify:local:full` on the integrated candidate.

## P0-1: Provider Package Availability

The selected Fabric provider profile is the only runtime authority for whether
`basic` or `pro` is available. Control Plane continues to consume the live
Fabric catalog and rejects a package that is absent or unavailable. In
`customer_owned`, neither package creates an OPL Cloud compute/storage charge;
the existing controlled Basic-only Pilot admission remains unchanged for other
deployment modes.

Console must render only catalog items with `available=true`. A disabled card
for an unavailable package is not a selectable product and must not be shown.
Regression coverage proves Basic-only, Pro-only, and Basic+Pro customer-owned
profiles, unavailable-package rejection, and zero Cloud resource price.

## P0-2: Workspace Gateway Budget

Control Plane exposes two Workspace-scoped routes:

```text
GET   /api/workspaces/{workspaceId}/gateway-budget
PATCH /api/workspaces/{workspaceId}/gateway-budget
```

Both routes authenticate the session account, load the exact Workspace, require
its persisted `workspaceApiKeyId`, then read or update that exact Sub2API Key.
Name prefixes are not authorization evidence. The projection exposes only:

```text
workspaceId, keyId, status, quotaUsdMicros, quotaUsedUsdMicros,
rateLimit5hUsdMicros, rateLimit1dUsdMicros, rateLimit7dUsdMicros,
usage5hUsdMicros, usage1dUsdMicros, usage7dUsdMicros, enabled, updatedAt
```

PATCH accepts only:

```text
quotaUsdMicros, rateLimit5hUsdMicros, rateLimit1dUsdMicros,
rateLimit7dUsdMicros, enabled, resetQuota, resetRateLimitUsage
```

It never renames, changes group, deletes, reveals, or rebinds a Key. The generic
Gateway Key route continues to reject Workspace-reserved Key mutation.

## Rotation And Concurrency

Workspace budget mutation and Key rotation serialize on the same Workspace
resource lock. A live Sub2API read proves the exact bound Key before either
operation mutates it. A non-terminal rotation durably blocks budget mutation;
a non-terminal budget mutation durably blocks rotation.

Sub2API cannot import quota or rolling-window usage counters into a replacement
Key. Rotation therefore must not copy the old limits and reset their usage. For
an active Key it persists the disable intent, disables the old Key, waits for
zero current concurrency, then reads the final counters. A finite total quota
`Q` with final usage `U` transfers as replacement quota `Q-U`; an originally
unlimited quota remains unlimited. `U >= Q` fails before replacement creation,
because a zero replacement quota means unlimited rather than no remaining
budget.

A finite rolling limit may be copied only when its final live usage is zero.
Any non-zero 5h, 1d, or 7d usage fails closed before replacement creation; P0
does not permanently reduce the future window or reset the current window.
Disabled, quota-exhausted, expired, and explicitly expiring Keys also fail
before mutation because the current Sub2API create API cannot preserve those
semantics without an active or renewed replacement.

After the frozen policy is admitted, rotation creates one replacement with the
remaining total quota and admitted rolling limits, verifies live readback,
installs the replacement Secret, persists the new Key binding, and retires the
old Key. Model access is unavailable from old-Key disable until replacement
runtime readback. The operation must fail closed when disable, drain, counters,
or policy readback cannot be proved; it must never silently create an unlimited
Key.

Control Plane does not persist a quota replica. Existing durable operation and
audit evidence may record identifiers, requested mutation, and outcome, but
all budget readback is live from Sub2API.

The current process lock is sufficient only for the single Control Plane
replica in the first Local-Docker MVP. Multi-replica TKE qualification requires
a PostgreSQL advisory lock or atomic durable Workspace operation claim and is
not proved by this change.

## Console And Errors

The Workspace detail surface owns budget editing. It reads the Workspace-scoped
route and sends only approved fields. It shows explicit unavailable/error state
when Sub2API cannot provide current truth and must not invent zero limits.

Stable errors distinguish: Workspace not found, Workspace Key not provisioned,
bound Key not found or mismatched, invalid budget input, and Gateway source
unavailable. Authorization failures reveal no cross-account Workspace or Key
facts.

## Verification

Focused tests cover provider profile combinations, customer-owned pricing,
exact Workspace-Key binding, cross-account denial, update allowlist, reserved
generic route protection, live Sub2API readback, reset operations, rotation
continuity, and Console DTO/call/render behavior. The integrated candidate runs
`npm run verify:local:full` exactly once after focused suites pass.
