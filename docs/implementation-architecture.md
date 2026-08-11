# OPL Cloud Implementation Architecture

## Request Path

```text
Browser Console
  -> Control Plane product API
       -> Sub2API management API: live balance, account Key/usage, idempotent debit/refund
       -> Fabric API: typed compute, storage, attachment, Secret, and runtime stages
       -> Ledger API: receipts and review evidence
```

Sub2API is external and remains the only spendable-balance, API-key, routing,
and request-usage owner. The repository reads those records on demand and does
not mirror them. Its code, image, database, configuration, and deployment remain
outside this repository's mutation boundary.

## MVP Current Breakpoint

The required Core path is
`Console -> Control Plane -> Workspace launcher/provider -> local Docker`.
Fabric now implements the local Docker side of that path and defaults to it;
Tencent/TKE requires explicit instance selection. Fabric also owns typed launch
stage mutation/readback routes with exact immutable operation bindings. The
portable Compose profile still starts PostgreSQL, Control Plane, Fabric, and
Ledger with Workspace launch workers disabled, so Compose health alone remains
a control-service installation check rather than end-to-end Workspace evidence.

The reusable Gateway side is further ahead: Control Plane already projects
Sub2API balance, usage, balance history, and Workspace Key operations, and its
purchase paths use Sub2API charge/refund authority. Ledger already owns receipts
and reconciliation evidence. Those facts do not close the P0 until the same
minimal path reaches a real local Docker Workspace. Mutable current capability
is owned only by [status](status.md); remaining work and priority are owned only
by the [roadmap](roadmap.md).

## Physical Module And Dependency Map

The current repository is a modular product repository with three Go service
modules and one browser application. Repository co-location and one release image
do not authorize implementation imports between service owners.

```text
apps/console-ui
  -> same-origin /api/*
services/control-plane
  -> typed HTTP client -> services/fabric
  -> typed HTTP client -> services/ledger
  -> typed HTTP client -> external Sub2API

services/control-plane ─┐
services/fabric        ├─> services/internal/postgresmigrate
services/ledger       ─┘

packages/contracts -> CI and service-boundary verification only
```

| Module | Physical boundary | Owns | Allowed dependencies | Forbidden coupling |
| --- | --- | --- | --- | --- |
| Console UI | `apps/console-ui`, TypeScript build | presentation and customer interaction | Control Plane product APIs under `/api/*` | direct Fabric, Ledger, Sub2API, Tencent, Kubernetes, persistence, or server implementation imports |
| Control Plane | independent `go.mod`, binary, Deployment and schema | session/account mapping, Workspace entitlement, Launch cursor/attempt/lease/CAS, account/settlement coordination and customer DTOs | typed HTTP clients for Fabric, Ledger and Sub2API; narrow PostgreSQL migration helper | resource-stage reducers, Fabric operation derivation, Fabric/Ledger implementation imports, provider fields/SDKs/Kubernetes, provider mutations, or downstream table writes |
| Fabric | independent `go.mod`, binary, Deployment and schema | compute, storage, attachment, Secret binding, Runtime, provider-neutral operation bindings/store, provider mutations and authoritative readback | provider adapters, cloud SDKs and narrow PostgreSQL migration helper | wallet, customer billing policy, Console session state, or Ledger table writes |
| Ledger | independent `go.mod`, binary, Deployment and schema | receipts, evidence, review, reconciliation and continuation refs; additional evidence verticals remain extensions | narrow PostgreSQL migration helper | Launch continuation authority, spendable balance mutation, provider SDKs, Fabric execution, or Control Plane table writes |
| PostgreSQL migration helper | independent narrow Go module under `services/internal/postgresmigrate` | advisory lock, migration journal and TLS validation mechanics | PostgreSQL driver only | any Console, Control Plane, Fabric or Ledger domain type |
| Machine contracts | JSON under `packages/contracts` | executable ownership and protocol boundaries | tests, build and validation | runtime state, service implementation or a second status owner |

`tests/contracts/module-physical-boundaries.test.ts` enforces the source-level edges:
no Go service may import another service implementation, only Fabric may import
Tencent or Kubernetes SDKs, and Console network calls must remain inside its API
adapter and resolve to `/api/*`. This gate runs through the existing `npm test`
lane; it complements behavior and contract tests rather than replacing them.

Physical deployment isolation is incomplete. The three services use separate
processes and tables, but the portable Compose profile and current external
medopl TKE profile inject one `DATABASE_URL` and one internal service token. Consequently
table ownership and caller identity are contract-enforced, not database-role or
service-credential-enforced. The common image also makes them one release unit,
which is intentional for the current product repository but not independent
service release evidence.

Deployment isolation is an independent implementation lane, not a predecessor
to Console, Control Plane, Fabric, or Ledger development. Portable distribution
assets and provider adapter code stay in this repository, while concrete
manifests, values and secret references stay in `opl-instance-medopl`. The two owners
join only when qualifying an exact deployment, rollback, and authoritative
readback. The common release image may remain shared unless measured release
blast radius creates a separate requirement.

Internal cohesion is also uneven. Fabric resource, runtime and recovery behavior
is concentrated in `internal/fabric/service.go`; Control Plane launch/recovery and
persistence behavior is concentrated in a few multi-thousand-line files. These
are change-collision and review risks inside the correct owner modules, not a
reason to introduce cross-service packages. Their split must preserve packages,
HTTP contracts, state machines and behavior while moving cohesive capabilities
into focused files.

Cohesion work is also lane-scoped rather than a repository-wide freeze. Splits
inside different owning modules may proceed concurrently. Work that touches the
same large file or changes one public contract uses a single short-lived owner
until that shared change reaches fresh-main CI; other module lanes continue.

## Current Simplification Pressure

A read-only structural audit found implemented surfaces whose current in-repo
product demand is absent, superseded, paused, or narrower than their code shape.
These are current implementation facts, not deletion authorization:

| Cluster | Current implementation fact |
| --- | --- |
| Control Plane persistence | Disabled archive/retention and superseded shared-execution models remain; Organization/Membership are one-to-one compatibility storage |
| Fabric optional verticals | ContentTransfer runtime/API/schema surfaces are retired while historical migrations and data remain; Snapshot/Restore still has provider/service/store/route/test surfaces but no current in-repo product caller and remains excluded from the Pilot |
| Ledger optional verticals | Artifact, Review, ReviewPolicy, and Continuation APIs exist while current Control Plane callers primarily consume receipts and reconciliation |
| Indirection and tooling | A large Control Plane facade, repeated CLI parsers, repeated workflow setup/cleanup, and custom static-file behavior create maintenance cost |
| Active-tree residue | Console styles retain multiple generations after the current UI work; dated execution plans and frozen QA assets were retired from active history |

The keep, shrink, or delete candidates, priority, risk, admission evidence, and
owner boundaries live only in the
[Simplification Backlog](roadmap.md#simplification-backlog). Candidate admission
must trace real callers, target obligations, persisted data, and external
consumers before any public route or schema is removed.

## Repository And Instance Boundary

`one-person-lab-cloud` owns both product architecture and this reusable Console,
Control Plane, Fabric, and Ledger implementation. These are logical service
boundaries inside one repository, not authorization for separate current
implementation repos. `opl-cloud` is retained only as the short package, image,
binary, service, namespace, environment-variable and runner identifier.

`opl-instance-medopl` owns one concrete installation: domain names, provider
profile, region and resource ids, enabled plans and prices, image pins, secret
references, promotion policy, and deployment receipts. Instance repositories
consume immutable `one-person-lab-cloud` releases whose internal artifacts may
use the `opl-cloud` identifier, and never copy runtime code, product
contracts, or spendable-balance state.

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

Control Plane currently projects `https://gflabtoken.cn/v1` as the public model
endpoint. Console may present that public endpoint according to the current UX.
It never exposes or directly calls Sub2API management APIs;
`OPL_SUB2API_BASE_URL` remains server-only, and Cloud does not inject a second
Runtime Gateway base URL.

`code-complete` means the local contracts, code, PostgreSQL, browser, and
structure gates pass on one revision. `pilot-ready` additionally requires
approved real service/resource evidence. `production-proven` requires the same
immutable revision deployed and authoritatively read back in production.

## Service Ownership

`apps/console-ui` owns presentation only. It has no persistence and never calls
Fabric, Ledger, Tencent, Kubernetes, or Sub2API directly.

`services/control-plane` owns local sessions, one-to-one Account-to-Sub2API
mappings, N Workspace entitlements per Account, Workspace-level monthly
operations, the Launch business cursor, attempts/leases/CAS, settlement
coordination, selected provider-profile refs, and strict customer DTOs. It does
not own a Fabric operation store, resource-stage reducer, live Compute, Storage,
Attachment, Secret, or Runtime status, or provider mutation. Sub2API
authenticates customer credentials. Organization and
Membership rows remain internal one-to-one compatibility records only; they are
not shared-account or customer-authorization surfaces.

`services/fabric` owns compute, storage, attachments, Secret binding, Workspace
runtimes, provider-neutral stage-operation bindings, its operation store,
provider mutations, and provider readback. The local Docker and Tencent/TKE
adapters each own their concrete writes and authoritative readback; Tencent
TKE/CVM/CBS and Kubernetes names do not enter the typed launch contract.
Provider callbacks may update resource facts but cannot overwrite Control Plane
entitlement state.

`services/ledger` owns receipt, evidence, review, reconciliation, and
continuation references. ReviewPolicy, Artifact, Continuation, retention, and
related stores beyond the Core receipts remain implemented extension surfaces,
not MVP prerequisites. Ledger never changes Sub2API balance and its refs cannot
authorize or advance a Workspace Launch.

`packages/contracts` contains narrow machine-enforced cross-module, interface,
security, integrity, permission, and irreversible-side-effect boundaries; it is
not a runtime service or a complete current-implementation specification.
Speculative route and object entries remain outside the active contracts.

## Provider Port

Fabric exposes one Go `Provider` port paid by both `local-docker` and
`tencent-tke`. Process startup defaults to `local-docker` independently of
`NODE_ENV`; only `OPL_FABRIC_PROVIDER=tencent-tke` selects the Tencent adapter.
The Fabric CI job enables the real local Docker integration test, which verifies
the provider writes and owner-authoritative readback rather than treating an
interface or control-service health check as portability evidence.

The Core port exposes provider-neutral compute, storage, attachment, runtime,
preflight, readback, renewal, and recovery facts. The selected instance profile
chooses an adapter. Provider-specific identities, diagnostics, retry rules, and
mutation sequences remain inside that adapter. The first additional adapter is
`local-docker`; generic `kubernetes` follows when the common contract is proven
by both real paths. Control Plane keeps the one Launch business Reconciler and
selected provider-profile ref; Fabric persists each stage-operation binding and
the provider resource mapping.

## Launch Boundary Integration

Fabric exposes `/fabric/workspace-launches/preflight`,
`/fabric/workspace-launches/stages/read`, and
`/fabric/workspace-launches/stages/ensure`. The stage DTO contains only the
provider-neutral binding and resource refs used by the real Control Plane caller.
Fabric persists the parent binding and a deterministic child record before each
actual provider write, then reads both by exact operation identity; typed
readback never scans operation listings or reconstructs Launch ownership from
suffixes or provider tags. The Control Plane caller and the Fabric source remain
separately owned changes that must be absorbed serially before claiming the full
launch boundary or advancing the roadmap P0.

## Persistence

Control Plane, Fabric, and Ledger each own their PostgreSQL schema and table
namespaces. Cross-service writes go through typed HTTP clients; no service writes
another service's tables. Sub2API data remains in Sub2API. The current production
credential can technically reach all three namespaces, so database least
privilege remains an open deployment-isolation task rather than a completed
physical boundary.

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

## Current Medopl Workspace Access Path

The current medopl Tencent/TKE extension data path is:

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
combines exact `one-person-lab-app`, active-shell, and Framework revisions. The
Workspace owner publishes that image independently; an instance pins its
immutable `repository@sha256` alongside the OPL Cloud product release. The Cloud
product release does not build, publish, or promote an instance Workspace image.
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

Keeping the shared proxy avoids per-Workspace CLB rules and is the current
topology for administrator-provisioned accounts. Control Plane selects the Runtime
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

## Product Release And Instance Qualification

Cloud publishes one multi-architecture GHCR image and GitHub Release containing
Compose, an environment template, and a release manifest. Product release uses
no production environment and performs no instance deployment. The image is
identified by a version tag, exact product SHA, and immutable digest; mutable
`latest` and `stable` tags are forbidden.

The private `opl-instance-medopl` repository owns the current medopl/TKE
configuration, production environment, deployment workflow, rollback, canaries,
and receipts. Historical rollout evidence predates this owner split and does
not prove the migrated Instance path. Fresh production claims require Instance
workflow readback for the exact Cloud release.

Control Plane remains one Pod. Existing load evidence covers request concurrency
and replay, but its historical per-resource renewal scan is not proof of the
current Workspace renewal saga. The current gates must run against an isolated
PostgreSQL database. Additional replicas remain out of scope unless
production measurements justify the ownership and locking changes.

Infrastructure alarms remain in Tencent Cloud Monitor. Business alarms are a
projection of Workspace renewal operations plus compute/storage compatibility
facts; there is no alert table. Stable, redacted transition codes drive CLS
alerting.
