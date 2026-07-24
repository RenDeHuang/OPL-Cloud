# Architecture

## Request Path

```text
Browser Console
  -> Control Plane product API
       -> Sub2API management API: live balance, account Key/usage, idempotent debit/refund
       -> Fabric API: CVM, CBS, attachment, runtime, provider facts
       -> Ledger API: receipts and review evidence
```

Sub2API is external and remains the only spendable-balance, API-key, routing,
and request-usage owner. The repository reads those records on demand and does
not mirror them. Its code, image, database, configuration, and deployment remain
outside this repository's mutation boundary.

## Console Source Truth

| Console area | Authority | Control Plane projection |
| --- | --- | --- |
| Signed-in identity | Sub2API identity plus local Session mapping | `/api/auth/me` |
| Public model endpoint | configured Sub2API origin projected as `/v1` | `/api/gateway/endpoint` |
| Wallet, owned Keys, per-Key Usage, account aggregate, balance history | live Sub2API JSON APIs | granular `/api/gateway/*` source DTOs |
| Workspace and renewal state | Control Plane Workspace row | `/api/workspaces` and launch/renewal DTOs |
| Runtime readiness | live Fabric/Kubernetes readback | `/api/workspaces/{workspaceId}/runtime-status` |
| `/data` and `/projects` release persistence | direct Runtime Pod SHA256 markers | rollout/rollback validation only; metadata/statfs product APIs are paused |
| Billing receipts | live Ledger readback | `/api/billing/receipts` |

Each source returns `source`, `status`, `available`, and `fetchedAt`. A successful
zero-row read is `empty`; dependency failure is `unavailable` and carries no
invented zero, empty collection, success state, or stale data. `sourceUpdatedAt`
is omitted unless the authority supplies it. Browser identity parameters never
override the current Session mapping, and raw downstream DTOs never cross the
Control Plane boundary.

Console displays and copies `https://gflabtoken.cn/v1` as the public model
endpoint. It is never a link or redirect target, iframe, HTML source, or direct
browser call to Sub2API management APIs. `OPL_SUB2API_BASE_URL` remains
server-only, and Cloud does not inject a second Runtime Gateway base URL.

`code-complete` means the local contracts, code, PostgreSQL, browser, and
structure gates pass on one revision. `pilot-ready` additionally requires
approved real service/resource evidence. `production-proven` requires the same
immutable revision deployed and authoritatively read back in production.

## Service Ownership

`apps/console-ui` owns presentation only. It has no persistence and never calls
Fabric, Ledger, Tencent, Kubernetes, or Sub2API directly.

`services/control-plane` owns local sessions, one-to-one Account-to-Sub2API
mappings, N Workspace entitlements per Account, Workspace-level monthly
operations, provider references/current pointers, recovery state, and strict
customer DTOs. It does not own live Compute, Storage, Attachment, or Runtime
provider status. Sub2API authenticates customer credentials. Organization and
Membership rows remain internal one-to-one compatibility records only; they are
not shared-account or customer-authorization surfaces.

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

Production upgrades run the journaled migrations against the existing database.
Legacy identity collisions fail closed; migrations never merge or delete those
records automatically. The identity cutover requires the same migrations to pass
against an isolated PostgreSQL copy before production deployment.

## Resource And Billing State

The deployed Sub2API has no generic hold/capture API. The launch path validates
the account and quote, runs read-only provider preflight, confirms balance, and
debits the exact monthly amount before Fabric mutates provider resources. It then
claims every PREPAID CVM/CBS fact and activates the Workspace only after
readback. A confirmed zero-resource result permits one idempotent refund;
partial or unknown provider results enter manual review without refund or
repurchase. Ledger receipt failure retries only the receipt. This behavior is
code-complete; live Sub2API and Tencent evidence remains pending.

Each Workspace operation owns renewal intent and one combined monthly debit.
Compute and storage rows are provider/compatibility facts, not independent
customer renewal controls. At unpaid expiry, access is denied and auto-renew is
disabled, but Control Plane performs no Fabric/Tencent stop, renew, destroy, or
delete mutation; Tencent expiry policy owns eventual provider reclamation.

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
Workspace referrer. The proxy writes `opl_ws_active` as routing context when a
clean Workspace URL is opened; the cookie is not an authentication credential.
It forwards traffic only after Fabric reports the Runtime ready and the
persisted Workspace state becomes `running`.

Fabric runs the Workspace image in `cloud` deployment mode with `password`
authentication. Fabric derives the runtime password and session secret from a
stable per-Workspace credential seed and stores them in a Kubernetes Secret.
Control Plane resolves the target Workspace's persisted `workspaceApiKeyId` and
hands the Key transiently to Fabric. Fabric writes or rotates a deterministic
Workspace-scoped Kubernetes Secret bound to account, Workspace, Key ID, and
fingerprint, and records only its ref, version, and fingerprint. Existing
account-scoped Secrets remain read-compatible until that Workspace's first Key
rotation; ordinary reads never infer scope from Workspace count or Key name.
Ordinary runtime status is non-secret. Dedicated owner-only POST commands reveal
or rotate the password transiently; Control Plane never persists it, and Console
retains it only in Workspace detail component memory. A Workspace image candidate
combines exact `one-person-lab-app`, `opl-aion-shell`, and `one-person-lab` Framework
commits; all must be full 40-character SHAs already merged into their respective `main`.
The fixed candidates are App `6b334ef7f239eb01c40578159e6df9ed2e7f97dc`, shell
`dbd9d68115604673df85033d7a0ab323d65a79a2`, and Framework
`51d16f0e93aebf3fd5ccf96082490395fcbb8711`. The release workflow checks out all three detached, runs the existing
`ensure:shell`, builds the active shell context into TCR, and reads back the immutable
digest. Production manifests accept only the resulting target `repository@sha256`.
The immutable Workspace image is pinned for deployment, but a customer
Workspace Ready-Pod `imageID` readback remains pending. No configured digest,
placeholder, or local timestamp substitutes for that Pod evidence.

This is a real exception to the Control Plane product-command boundary: it
carries Workspace HTML, API, and WebSocket data-plane traffic. The available
evidence does not prove an unauthenticated data disclosure; the inspected
runtime source retains password authentication. Until the published digest and
Ready-Pod `imageID` exist, that source finding cannot be extended to an
exact deployed revision.
Control Plane availability is coupled to every Workspace connection, and a
2xx/non-empty-page check can pass on the login page without proving an
authenticated Workspace session.

Keeping the shared proxy avoids per-Workspace CLB rules and is the smallest
topology for the initial 2-5 operator-provisioned accounts. Control Plane selects the Runtime
Service; the Runtime owns password validation, its authenticated session, and
WebSocket access. Routing every Workspace Service directly with native TKE
Ingress removes Control Plane from the data path, but does not replace Runtime
authentication and adds per-Workspace rule quota, creation, deletion, retry,
and orphan reconciliation responsibilities. Do not add those routes until live
CLB limits justify the extra ownership.

The current decision is to retain the single shared entry and explicitly accept
Control Plane availability coupling for the operator-provisioned Pilot. A dedicated
Workspace Router remains a later ownership and scaling decision; no router or
security-model change is authorized by this document.

## Production

Production runs Control Plane, Fabric, and Ledger as separate Kubernetes
Deployments. Secrets are Kubernetes Secret references, configuration is a shared
ConfigMap, and the deploy workflow waits for all three rollouts. The production
Fabric catalog exposes both Basic and Pro; availability means product access,
while Tencent MonthlyPreflight remains the capacity authority before debit.
Separately approved provider verification remains paused and does not gate
ordinary deploy.

The Cloud services have ordinary rollout and deployment readback evidence. That
evidence does not complete the Basic canary, customer Workspace imageID, or model
Usage checks, and it does not imply a real Pro purchase.

Image publication accepts a full 40-character Cloud commit only. The release
workflow reads back the exact checked-out HEAD and official Cloud `origin/main`,
then requires the candidate to be contained in that main history before building.

Control Plane remains one Pod. Existing load evidence covers request concurrency
and replay, but its historical per-resource renewal scan is not proof of the
current Workspace renewal saga. The current gates must run against an isolated
PostgreSQL database. Additional replicas remain out of scope unless
production measurements justify the ownership and locking changes.

Infrastructure alarms remain in Tencent Cloud Monitor. Business alarms are a
projection of Workspace renewal operations plus compute/storage compatibility
facts; there is no alert table. Stable, redacted transition codes drive CLS
alerting.
