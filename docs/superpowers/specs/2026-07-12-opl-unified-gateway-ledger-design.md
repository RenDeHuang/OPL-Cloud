# OPL Unified Gateway And Ledger Design

## Status

Accepted product direction on 2026-07-12.

This design supersedes the customer-portal and manual Gateway budget decisions in
`2026-07-11-opl-gateway-sub2api-console-integration-design.md`. Sub2API remains
the independently operated OPL Gateway backend, but customers do not enter its
portal. OPL Console owns every customer-facing Gateway workflow, and OPL Ledger
is the only customer wallet and financial ledger.

## Product Result

An OPL customer signs in and recharges once in Console. The same CNY wallet can
pay for compute, storage, and API usage. Console lets the customer create and
manage multiple API Keys, bind one dedicated Key to each Workspace, inspect
request-level usage, and see the resulting Ledger charges.

The customer never needs a Sub2API password and never sees a Sub2API balance.
Sub2API continues to own API authentication, routing, real-time execution limits,
raw usage facts, and upstream-provider operations.

## Ownership

| Boundary | Owner |
| --- | --- |
| Customer identity, organizations, memberships, and tenant checks | Control Plane |
| Customer CNY balance, holds, compute/storage/API debits, and receipts | OPL Ledger |
| API Keys, model routing, request execution, Token counts, and `actual_cost` | Sub2API |
| OPL-to-Sub2API identity and Key-to-Workspace mappings | Control Plane |
| Workspace Key Secret and runtime injection | Fabric |
| Customer Gateway experience | Console UI |
| Channels, upstream accounts, provider credentials, routing internals, and Sub2API updates | Gateway operators |

Sub2API usage is provider evidence. Ledger entries are customer financial facts.
They are joined by immutable Sub2API usage and request identifiers, not by shared
database tables.

## Identity And Attribution

Control Plane uses one dedicated machine administrator to call restricted
Sub2API administration APIs. That administrator does not own customer Keys or
make model requests.

Every OPL user maps to a distinct Sub2API user:

```text
OPL user + OPL billing account
-> gateway_identity
-> Sub2API user
-> one or more Sub2API API Keys
```

The mapping is created idempotently when the customer first creates a local Key
or provisions a Workspace Key. OPL registration itself does not depend on
Sub2API availability.

The stable customer identity is the OPL user ID. A verified OIDC
`(issuer, subject)` may bind the corresponding Sub2API identity, but email is
not the durable join key.

Each Key has one intended attribution scope:

- a local Key belongs to its OPL user and billing account;
- a Workspace Key belongs to its OPL user and exactly one Workspace;
- one Key cannot be bound to multiple Workspaces;
- a Workspace Key must not be reused for local calls.

Sub2API usage provides `user_id`, `api_key_id`, `request_id`, model, Token
counts, `actual_cost`, and time. Control Plane joins those facts to
`gateway_identity` and the Workspace Key binding before Ledger settlement.

Shared Workspace credentials do not identify individual members. All API usage
from a shared Workspace is attributed to the Workspace, its owner, and its OPL
billing account.

## Console Product Surface

Console exposes three Gateway views using the existing Console design system.

### Overview

- Gateway availability;
- unified OPL wallet balance;
- today and current-month requests, Tokens, and settled CNY charges;
- latest successful usage synchronization time;
- customer-actionable delayed usage or billing status;
- available model summary.

### API Keys

- list the current user's masked Keys;
- create multiple Keys within account policy;
- edit name, quota, expiry, rate limit, and enabled state;
- delete or rotate a Key;
- show local or Workspace attribution and the bound Workspace;
- show the full secret once in the successful creation response;
- never reveal an existing full secret again; rotation creates a new secret.

A local Key is created from the API Keys view. A Workspace Key is provisioned
from the Workspace workflow, is marked as dedicated to that Workspace, and is
injected automatically. The customer cannot bind a local Key to a Workspace or
reuse a Workspace Key elsewhere.

### Usage

- cursor-paginated request records;
- filters for time, Key, model, and Workspace;
- request ID, Key name, Workspace, model, request type, and time;
- input, output, cache-read, and cache-write Tokens;
- Sub2API `actual_cost` and settled CNY charge;
- explicit pending state for usage not yet settled to Ledger;
- latest synchronization timestamp.

Console does not expose Sub2API channels, upstream accounts, credentials,
routing internals, technical balance, migrations, or update controls.

## Control Plane API

The browser calls only tenant-protected Control Plane APIs:

```text
GET    /api/gateway
GET    /api/gateway/models
GET    /api/gateway/keys
POST   /api/gateway/keys
PUT    /api/gateway/keys/{keyId}
POST   /api/gateway/keys/{keyId}/rotate
DELETE /api/gateway/keys/{keyId}
GET    /api/gateway/usage
PUT    /api/workspaces/{workspaceId}/gateway-key
DELETE /api/workspaces/{workspaceId}/gateway-key
```

Control Plane derives the OPL user and billing account from the authenticated
session. Customer requests cannot submit an arbitrary Sub2API user ID or OPL
billing account ID.

The Sub2API adapter owns response-shape normalization and version compatibility.
Console does not consume Sub2API responses directly.

## Secret Handling

- Sub2API generates and stores API Keys.
- A complete Key may pass through Control Plane only in the creation response or
  an internal Fabric binding operation.
- A complete Key is never persisted by Control Plane or Ledger.
- Logs, audit events, receipts, errors, browser storage, and cached projections
  contain only a Key ID and masked value.
- Key creation responses use `Cache-Control: no-store`.
- Fabric stores a Workspace Key in a Kubernetes Secret, mounts it at
  `/run/secrets/gateway_api_key`, and sets
  `OPL_GATEWAY_API_KEY_FILE=/run/secrets/gateway_api_key`.
- Rotation updates the Secret and rolls the Workspace runtime before disabling
  the old Key.
- Provisioning a Workspace Key does not return the complete secret to the
  browser; Control Plane sends it directly to Fabric and returns only its ID and
  masked value.

## Unified Wallet And Settlement

OPL Ledger owns the wallet, balance, holds, debits, and refunds. Control Plane
may request Ledger operations but cannot declare money available by itself.
Sub2API retains a hidden, bounded technical balance because it must enforce API
spending in real time without adding Ledger latency to every model request. That
technical balance is an execution limit backed by Ledger funds, not a second
customer wallet and not an independent financial record.

There is no customer-facing Gateway budget allocation action. Control Plane
automatically maintains a bounded technical-credit window:

```text
Ledger available CNY
-> create a Gateway-specific Ledger hold behind the scenes
-> convert the reserve using a versioned USD/CNY rate
-> add bounded technical credit to the mapped Sub2API user
-> Sub2API executes and records usage
-> usage synchronizer debits the wallet and consumes the matching Gateway hold
-> replenish only while Ledger has available funds and reconciliation is healthy
```

The low and high watermarks are operator configuration, not product controls.
The maximum unsynchronized financial exposure cannot exceed the outstanding
technical-credit window.

Each technical-credit grant is linked to one Ledger hold and one mapped Sub2API
user. Ledger tracks the remaining amount of every hold. Gateway settlement
identifies the mapped user and atomically consumes that user's remaining Gateway
holds oldest first; one usage may consume more than one hold. It must not consume
an arbitrary amount from the account's aggregate frozen balance. This requires
extending the current resource-settlement contract with reserved-resource
identity and hold-consumption records; otherwise compute, storage, and Gateway
settlements could consume one another's frozen funds. A grant is sent to Sub2API
only after its hold succeeds. If the remote grant fails, the hold is released
idempotently.

For V1, Sub2API `actual_cost` is the final USD API charge fact. Ledger converts
it to CNY using the effective versioned exchange-rate snapshot and does not add
a second markup. Any API sales multiplier belongs in Sub2API pricing and is
already reflected in `actual_cost`.

Every settled API usage creates one append-only `gateway_debit` containing:

- OPL billing account, actor user, and optional Workspace;
- Sub2API user, Key, usage, and request identifiers;
- technical-credit window and consumed Ledger hold identifiers;
- model and Token breakdown;
- original `actual_cost` in USD;
- exchange-rate version, rounding rule, and final CNY cents;
- synchronization and usage timestamps.

`sub2api_usage_id` has a unique settlement constraint. Overlapping polling,
retries, and out-of-order delivery cannot create duplicate charges.

Unused reserves are released when the user is disabled, all Keys are disabled,
or reconciliation confirms that technical credit has been withdrawn. A release
must never exceed both the remaining linked hold and the confirmed unused
technical balance.

## Failure Rules

- **Ledger unavailable or hold rejected:** do not add technical credit. Existing
  bounded credit may continue serving requests; no unbounded spending is
  possible.
- **Sub2API unavailable:** keep the Workspace accessible, show Gateway
  unavailable, and do not create API debits without usage evidence.
- **Usage sync delayed:** show stale/pending status, retry an overlapping time
  window, and preserve the same usage-ID uniqueness rule.
- **Balance mismatch:** stop automatic replenishment, retain bounded existing
  credit, and require reconciliation.
- **Key creation response lost:** do not try to reveal the old secret; let the
  user rotate or create another Key.
- **Workspace Secret update fails:** keep the previous binding and Key active;
  do not report the new binding as successful.
- **Unsupported Sub2API contract:** disable new identities, Key mutations,
  technical-credit changes, and Workspace bindings. Existing data-plane use
  remains bounded by current technical credit.
- **Technical-credit grant fails after hold:** release the linked hold with the
  same operation identity and do not report funding as successful.

## Sub2API Updates

The initial integration uses unmodified upstream Sub2API through published HTTP
APIs. A fork is allowed only after a contract test proves an essential API or
correctness property cannot be supplied by an adapter.

Production never tracks `latest`. Each deployment records the upstream version,
image digest, database schema version, adapter contract version, and last passing
contract check.

Before adopting an upstream release:

1. pin the candidate image tag and digest;
2. back up Sub2API PostgreSQL and Redis;
3. run migrations against a restored database copy;
4. run health, version, identity, Key CRUD, usage, balance add/subtract, and
   `/v1/models` contract checks with a dedicated probe user;
5. complete one real model request and verify usage plus Ledger settlement in
   staging;
6. canary the candidate before production promotion;
7. require human approval for production migration and traffic switch.

If an update is incompatible, production stays on the pinned prior version.
Application rollback is sufficient only when the database remains backward
compatible; otherwise the database backup is part of rollback.

If a fork becomes necessary, it must remain a thin patch set over an upstream
release, avoid OPL-specific Sub2API tables, preserve default upstream behavior,
and expose a configurable integration interface. Every new upstream tag is
rebased through the same contract and migration gates.

## Deployment Boundary

Sub2API may remain on its current server or move into a separate
`opl-gateway` Kubernetes namespace. Placement does not change ownership.
PostgreSQL, Redis, migrations, backups, and updates remain operationally
independent from OPL Console.

Restricted administration routes are reachable only from Control Plane and
approved operator networks. Public routes expose the model data plane, not the
Sub2API customer portal.

## Verification

The implementation is accepted only when all of the following pass:

1. first Key provisioning produces one active mapped Sub2API user idempotently,
   while ordinary OPL registration works when Sub2API is unavailable;
2. one user cannot list or mutate another user's Keys;
3. Key creation returns the complete secret once, and later reads are masked;
4. no complete Key appears in databases, logs, audit events, receipts, errors,
   or browser storage;
5. a dedicated Workspace Key is injected and produces a real model response;
6. usage is attributed to the correct user, billing account, Key, and Workspace;
7. repeated and overlapping usage sync produces one Ledger debit per usage ID;
8. compute, storage, and API debits reduce the same Ledger wallet;
9. an API settlement consumes only its linked Gateway hold and leaves compute
   and storage holds unchanged;
10. insufficient Ledger funds stop replenishment and eventually stop API use at
   the bounded Sub2API credit limit;
11. balance mismatch stops replenishment and raises an operator-visible alert;
12. the current production Sub2API version passes the full contract check;
13. a candidate Sub2API update passes migration, contract, real-request,
    settlement, canary, and rollback drills before promotion.

## Delivery Order

1. Rebase the existing `gateway/g0-g4` work onto current `main` and remove the
   later dual-wallet/manual-budget direction.
2. Add targeted hold consumption to Ledger resource settlement.
3. Finish identity provisioning and tenant-protected Key CRUD.
4. Replace the read-only Gateway page with Overview, API Keys, and Usage views.
5. Make automatic technical-credit reservation and Ledger settlement the only
   API funding path.
6. Finish dedicated Workspace Key creation, injection, rotation, and cleanup.
7. Extend compatibility checks to mutating balance and Key probe operations.
8. Run staging and production acceptance, including an upstream update drill.

## Non-Goals

- exposing or embedding the Sub2API portal;
- exposing a second customer wallet or manual Gateway budget allocation;
- storing complete Keys in Control Plane or Ledger;
- sharing one Sub2API customer user or Key across all OPL customers;
- identifying individual people behind shared Workspace credentials;
- reading or writing Sub2API database tables directly;
- proxying model traffic through Control Plane;
- forking Sub2API before an adapter limitation is proven.
