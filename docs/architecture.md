# Architecture

## Request Path

```text
Browser Console
  -> Control Plane product API
       -> Sub2API management API: live balance and exact adjustment
       -> Fabric API: CVM, CBS, attachment, runtime, provider facts
       -> Ledger API: receipts and review evidence
```

Sub2API is external and remains the only spendable-balance, API-key, routing,
and request-usage owner. The repository does not mirror those records.

## Service Ownership

`apps/console-ui` owns presentation only. It has no persistence and never calls
Fabric, Ledger, Tencent, Kubernetes, or Sub2API directly.

`services/control-plane` owns Console auth, account mappings, organizations,
Workspaces, monthly entitlements, billing-operation recovery, support mappings,
and product projections. Its public routes express product commands rather than
generic downstream APIs.

`services/fabric` owns compute pools, dedicated CVM allocations, CBS volumes,
attachments, Workspace runtimes, provider operations, and all Tencent/Kubernetes
SDK calls. Provider callbacks may update resource facts but cannot overwrite
Control Plane entitlement state.

`services/ledger` owns EvidenceReceipt, ReviewPolicy, ReconciliationReport,
Artifact, Continuation, retention, audit, and idempotency records. It never
changes Sub2API balance.

`packages/contracts` is machine-readable current truth, not a runtime service.
Speculative route and object entries remain outside the active contracts.

## Persistence

Control Plane, Fabric, and Ledger each own their PostgreSQL schema. Cross-service
writes go through typed HTTP clients; no service writes another service's tables.
Sub2API data remains in Sub2API.

All three services serialize startup migrations with one database-wide PostgreSQL
advisory lock. A migration is journaled in `opl_schema_migrations` by service and
version only after it succeeds. Completed hard cuts, backfills, Ent schema changes,
and embedded SQL are skipped on every later start; a failed migration has no success
record and is retried on the next start.

This deployment starts from a fresh database. There is no compatibility layer,
dual write, historical billing schema, or old-state importer.

## Resource And Billing State

Fabric preparation happens before the external charge. Control Plane persists a
stable billing operation and redeem code before side effects. Only a confirmed
Sub2API adjustment activates the monthly entitlement. Ledger receipt failure is
retryable and does not reverse a confirmed charge.

All attachment and Workspace runtime operations require an active entitlement.
Compute expiration destroys compute; storage expiration retains data but blocks
use until a new entitlement is purchased.

## Workspace Access Path

The current Workspace data path is:

```text
Browser
  -> workspace.medopl.cn shared CLB / TKE Ingress
  -> Control Plane reverse proxy
  -> Fabric-created per-Workspace ClusterIP Service :3000
  -> Workspace runtime
```

`/w/<workspaceId>/` selects a Workspace from the URL. Root `/api/`, `/ws`, and
other Workspace-host requests select it from the `opl_ws_active` cookie or a
Workspace referrer. A `?token=` query only sets `opl_ws_active` and
`opl_ws_<workspaceId>` HttpOnly cookies and is then removed by redirect. The
proxy checks Workspace state and the runtime Service name, but does not read or
validate the token cookie.

Fabric runs the Workspace image in `cloud` deployment mode with `password`
authentication. It injects `OPL_SHARE_TOKEN`, but the inspected active runtime
source has no consumer that treats the value as an authentication credential.
Production currently selects that image through a mutable `latest` tag, so the
exact deployed digest-to-source revision remains unverified. Fabric derives the
runtime password and session secret from the same stable input and stores them
in a Kubernetes Secret. Control Plane removes the plaintext password from
Console responses. Its reset/delete-token commands change only the Control
Plane access projection and audit event; they do not rotate or revoke the Fabric
Secret or a runtime session.

This is a real exception to the Control Plane product-command boundary: it
carries Workspace HTML, API, and WebSocket data-plane traffic. The available
evidence does not prove an unauthenticated data disclosure; the inspected
runtime source retains password authentication. The Control Plane path does
prove that the URL token is neither validated nor exchanged there and that
reset/delete does not revoke runtime credentials. Under the inspected runtime
source, that token also does not grant access. The mutable production image tag
prevents extending the source finding to an exact deployed revision. Control
Plane availability is coupled to every Workspace connection, and the paid
verifier's 2xx/non-empty-page check can pass on the login page without proving
an authenticated Workspace session.

Keeping the shared proxy avoids per-Workspace CLB rules and is the smallest
temporary topology, but it requires an explicit token-to-runtime-session
contract before it can be treated as complete. Routing every Workspace Service
directly with native TKE Ingress removes Control Plane from the data path, but
does not solve authentication and adds per-Workspace rule quota, creation,
deletion, retry, and orphan reconciliation responsibilities. Do not add those
routes until live CLB limits and the access model are approved.

The recommended direction is to retain the single shared entry while choosing
either a dedicated Workspace Router or an explicitly supported gateway role for
the existing proxy. That component must validate an unforgeable credential,
establish the runtime session, support WebSocket upgrades, and rotate/revoke
access with Fabric runtime lifecycle. This is an access-security and ownership
decision, not an implementation detail; no router or security-model change is
authorized by this document.

## Production

Production runs Control Plane, Fabric, and Ledger as separate Kubernetes
Deployments. Secrets are Kubernetes Secret references, configuration is a shared
ConfigMap, and the deploy workflow waits for all three rollouts. The single paid
production verifier uses the public Console product chain.

Control Plane remains one Pod. Its opt-in PostgreSQL capacity gate covers 1,000
accounts/resources, 100 concurrent Console requests, 20 concurrent resource
commands with same-key replay, and a 1,000-resource renewal scan. The current
local baseline passes its five-second request gate with no duplicate charge,
claim, or receipt. Additional replicas remain out of scope unless production
measurements breach the gate after query-level fixes; process-local resource
locks must be replaced with database coordination before any replica increase.

Infrastructure alarms remain in Tencent Cloud Monitor. Business alarms are a
projection of current Control Plane compute and storage rows in Operator Summary;
there is no alert table. `manual_review`, `past_due`, `ledger_receipt_pending`, and
`cleanup_failed` transitions emit stable, redacted log codes for CLS alerting.
