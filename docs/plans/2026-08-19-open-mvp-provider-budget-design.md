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
resource lock. Rotation reads the current bound Key budget from Sub2API before
creating the replacement, creates the new Key with the same quota/rate-limit
policy and enabled state, installs the replacement Secret, persists the new Key
binding, then retires the old Key according to the existing rotation sequence.
The operation must fail closed when the old budget cannot be read or copied;
rotation must never silently create an unlimited Key.

Control Plane does not persist a quota replica. Existing durable operation and
audit evidence may record identifiers, requested mutation, and outcome, but
all budget readback is live from Sub2API.

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
