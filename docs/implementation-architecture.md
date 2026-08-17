# OPL Cloud Implementation Architecture

This repository implements the OPL Cloud product layer, which is under active
development and has published portable product artifacts. It does not
implement the OPL Framework Cordis Host, the `one-person-lab-app` product
authority, OPL Package publication/lifecycle, or either GUI Shell. Browser Console calls Cloud Control Plane APIs;
App Shells may consume Cloud-facing projections through App/Framework contracts,
but neither path moves Cloud service or database authority into a GUI plugin.

## Request Path

```text
Browser Console
  -> Control Plane product API
       -> Sub2API management API: live balance, account Key/usage, idempotent debit/refund
       -> Fabric API: typed compute, storage, attachment, Secret, and runtime stages
       -> Ledger API: receipts and reconciliation evidence
```

Sub2API is external and remains the only spendable-balance, API-key, routing,
and request-usage owner. The repository reads those records on demand and does
not mirror them. Its code, image, database, configuration, and deployment remain
outside this repository's mutation boundary.

## Family Domains And Host/Client Integration

Cloud implements authority surfaces for family capability domains; it does not
turn those names into a second module registry. The current physical owners are
the Console UI, Control Plane, Fabric, and Ledger service modules described
below. A family name may span those modules and the Framework/App repositories,
while a versioned Cloud image and typed service contracts remain the Cloud
release artifacts.

```text
Cloud service/image + typed API contracts
  <- Framework Host Cordis Cloud adapter contribution
       <- Host-selected product profile
            -> allowlisted Client Cordis graph
                 -> App renderer/package carrier
```

The Framework Host is the only composition authority for this path. It may
project an allowlisted client graph to an App renderer, but Cloud services stay
behind typed HTTP/capability contracts. A Host-derived Client Cordis context can
compose views, slots, actions, RPCs, and events; it cannot discover or install
Packages, own Package currentness, create a second registry, or replace Cloud
service, persistence, provider, wallet, Workspace, or Ledger authority.

The AionUI mainline and the DSH-GUI-derived candidate are App renderer/package
carriers, not Cloud products. Both must consume the same Host projection, client
contribution descriptor, slot/action ABI, and App product profile. Cloud does not
branch its API, image, database, policy, or currentness by renderer. The current
Workspace image candidate still pins exact App, active-shell, and Framework
revisions; that deployment carrier choice is an App/Workspace release concern,
not a Cloud Cordis runtime dependency.

Package and service versioning remain independent where an owner has a real
release cadence. Cloud consumes exact Package publication and carrier refs when
needed, but does not publish a Cloud-owned Package registry, lock, resolver,
currentness projection, or Cordis plugin for a Cloud service. Fabric provider
adapters, Control Plane state machines, Ledger persistence, and native carriers
remain in their owning implementation boundaries.

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
| Ledger | independent `go.mod`, binary, Deployment and schema | receipts, reconciliation, idempotency, retention, and caller-owned opaque provenance refs | narrow PostgreSQL migration helper | review-policy or review-gate semantics, Launch continuation authority, spendable balance mutation, provider SDKs, Fabric execution, or Control Plane table writes |
| PostgreSQL migration helper | independent narrow Go module under `services/internal/postgresmigrate` | advisory lock, migration journal and TLS validation mechanics | PostgreSQL driver only | any Console, Control Plane, Fabric or Ledger domain type |
| Machine contracts | JSON under `packages/contracts` | executable ownership and protocol boundaries | tests, build and validation | runtime state, service implementation or a second status owner |

`tests/contracts/module-physical-boundaries.test.ts` enforces the source-level edges:
no Go service may import another service implementation, only Fabric may import
Tencent or Kubernetes SDKs, and Console network calls must remain inside its API
adapter and resolve to `/api/*`. This gate runs through the existing `npm test`
lane; it complements behavior and contract tests rather than replacing them.

The portable Compose source now gives each service its own PostgreSQL
role/database and inbound service token; Control Plane receives separate Fabric
and Ledger outbound tokens. A source-built portable Compose isolation run starts
the services with those identities, rejects cross-owner database access and
caller-token impersonation, and rotates each token without restarting PostgreSQL
or unrelated services. This proves the reusable source configuration, not a
clean-host release installation or concrete medopl adoption. The common image
still makes the services one intentional product release unit rather than
independent service releases.

Deployment isolation is an independent implementation lane, not a predecessor
to Console, Control Plane, Fabric, or Ledger development. Portable distribution
assets and provider adapter code stay in this repository, while concrete
manifests, values and secret references stay in `opl-instance-medopl`. The two owners
join only when qualifying an exact deployment, rollback, and authoritative
readback. The common release image may remain shared unless measured release
blast radius creates a separate requirement.

Internal cohesion has improved but remains uneven. Control Plane Launch now uses
one focused, provider-neutral Reconciler with separate account, Fabric,
activation, service, and persistence files. Fabric moved local-Docker and Tencent
Workspace Launch behavior behind adapter files and reduced
`internal/fabric/service.go`; Tencent compute-allocation identity validation now
belongs to the Tencent adapter and is reused by the targeted operator pool-head
path. The remaining facade and provider/operator extensions still mix several
capabilities. These are change-collision and review risks inside the correct owner
modules, not a reason to introduce cross-service packages.

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
| Control Plane persistence | Archive, `ExecutionRequest`, and `WorkspaceBackup` application/Ent models are deleted while historical SQL/tables remain; Organization/Membership application/Ent models and runtime store APIs are deleted, while raw tables remain only as read-only historical custody for migration validation |
| Control Plane instance extension | The normal Launch/Resume path is provider-neutral. Instance-owned Provider Acceptance consumes one provider-neutral facts batch and a narrow Runtime path; it requires canonical compute/storage provider IDs and treats legacy node-pool and persistent-volume values as optional response-only projections. Instance deployment and production acceptance remain external |
| Fabric optional verticals | ContentTransfer and Snapshot/Restore runtime/API/provider surfaces are retired while historical migrations and data remain; Instance inventory proved no live backup row, Fabric operation, `VolumeSnapshot`, `VolumeSnapshotContent`, or restored-PVC obligation before the Snapshot/Restore cut |
| Fabric launch residue | Recovery proof/claim Service/provider/store mutation shells, unassigned legacy `LaunchBinding` branches, and the duplicate Tencent compute-ownership implementation are retired. Typed Tencent Launch has exact stage-chain readback/replay coverage; other operation-list consumers and the remaining mixed Fabric facade still require caller-led cohesion work |
| Ledger optional verticals | Artifact, Review, ReviewPolicy, ReviewGate, and Continuation APIs, stores, routes, and generated Ent code are retired. Receipt `artifactId`, `reviewId`, `outputRefs`, `reviewerChecks`, `continuationId`, and `continuation` remain caller-owned opaque provenance; historical `review_policies` rows and Receipt provenance columns remain without migration or deletion |
| Machine contract ownership | Billing retains Control Plane orchestration policy but references the Ledger evidence contract as the single reconciliation-report and Workspace monthly-receipt schema owner. The completed aggregate deployment migration guard is deleted; portable distribution and Instance deployment gates remain with their focused owners |
| Indirection and tooling | Three retained deployment tools use tool-local `node:util.parseArgs`, and Qualification reuses its stable setup and Go-test pipeline without changing job identities or zero-skip gates. Further CLI conversion is deferred where explicit native option schemas expand the surface; the large Control Plane facade and custom static-file/gzip behavior still create maintenance cost |
| Active-tree residue | The retired staging verifier entry and its tombstone test are deleted, the unused `path-to-regexp` override is gone, and same-selector/same-responsive-scope Console declarations no longer shadow earlier generations. Cross-selector styling and dated execution or frozen QA provenance are not treated as caller-zero code |

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
consume exact `one-person-lab-cloud` candidates for pre-publication
qualification and immutable Releases after publication. Their internal
artifacts may use the `opl-cloud` identifier, but they never copy runtime code,
product contracts, or spendable-balance state.

The Instance boundary also owns medopl-specific production, acceptance,
recovery, canary, rollback, and approval/evidence tooling. Those sources and
focused tests are now canonical in `opl-instance-medopl` `main`; Cloud retains
product runtime code, provider-neutral contracts, reusable adapters, and
portable candidate/release assets. Instance workflows still checkout an exact
Cloud `product_sha`, but they execute instance tools from the run-scoped
Instance checkout. Cloud no longer provides an instance-specific production
command or an accepted caller for these paths.

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
mappings, Account/User owner authorization, N Workspace entitlements per
Account, Workspace-level monthly operations, the Launch business cursor,
attempts/leases/CAS, settlement coordination, selected provider-profile refs,
and strict customer DTOs. It does not own a Fabric operation store,
resource-stage reducer, live Compute, Storage, Attachment, Secret, or Runtime
status, or provider mutation. Sub2API authenticates customer credentials.
Organization and Membership application/Ent models, runtime store APIs, and
provisioning writes are retired; their raw PostgreSQL tables remain only to
preserve historical rows and IDs for migration validation. They are not shared-
account or customer-authorization surfaces.

The login route admits JSON before credential processing. If a browser supplies
an `Origin` or `Referer`, Control Plane compares its scheme, host, and effective
port with `OPL_PUBLIC_URL` or the request web origin and rejects a mismatch or an
invalid configured origin before login or session material is written. The
same-origin Console JSON path remains valid, as do non-browser JSON callers that
supply neither header. This is a narrow pre-session request boundary, not a
general claim that every remaining browser or login hardening finding is closed.

`services/fabric` owns compute, storage, attachments, Secret binding, Workspace
runtimes, provider-neutral stage-operation bindings, its operation store,
provider mutations, and provider readback. The local Docker and Tencent/TKE
adapters each own their concrete writes and authoritative readback; Tencent
TKE/CVM/CBS and Kubernetes names do not enter the typed launch contract.
Provider callbacks may update resource facts but cannot overwrite Control Plane
entitlement state.

`services/ledger` owns receipts, reconciliation, idempotency, retention, and
caller-owned opaque provenance fields. Artifact, Review, ReviewPolicy, ReviewGate,
and Continuation APIs and their stores are retired. Historical `review_policies`
rows and Receipt provenance columns remain for data integrity; Ledger neither
interprets these refs nor generates continuation identities, hides them on reads,
or authorizes or advances a Workspace Launch. Control Plane's typed continuation
authorization is a separate owner-owned path.

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
mutation sequences remain inside that adapter. Generic `kubernetes` follows only
when the common contract is proven by real paths. Control Plane keeps the one
Launch business Reconciler and selected provider-profile ref; Fabric persists
each stage-operation binding and the provider resource mapping.

The read-only `POST /fabric/provider-facts/batch` boundary delegates resource
interpretation to the selected adapter. Control Plane Provider Acceptance uses
that same provider-neutral facts shape for compute, storage, attachment, and
Runtime readiness. The Cloud Provider Acceptance CLI and production live-QA
require canonical compute/storage provider IDs; compatibility node-pool and
persistent-volume fields are optional response-only projections and do not
participate in readiness or continuity comparison. The Local Docker adapter also
validates an immutable Workspace image against its trusted repository or exact
release-manifest source before Docker access or Fabric operation persistence.

## Launch Boundary Integration

Fabric exposes `/fabric/workspace-launches/preflight`,
`/fabric/workspace-launches/stages/read`, and
`/fabric/workspace-launches/stages/ensure`. The stage DTO contains only the
provider-neutral binding and resource refs used by the real Control Plane caller.
Fabric persists the parent binding and a deterministic child record before each
actual provider write, then reads both by exact operation identity; typed
readback never scans operation listings or reconstructs Launch ownership from
suffixes or provider tags. Both owners consume the same focused golden vectors,
and the normal Launch/Resume caller is integrated. This closes the typed boundary
slice but not the full Console-to-local-Workspace P0 vertical.

The current recovery row keeps `Max=1` and does not reset `Attempted`. An
operator may CAS-persist one exact-idempotency replay budget plus a finite typed
continuation-read budget; the server binds the starting readback count. Fabric
reports only `ready/none`, `pending/provider_provisioning`, the three explicit
absent reasons, or the two explicit unknown reasons. Adapters perform owner
read, child replay CAS, owner read again, then reuse the exact original key only
for still-absent resources. Budget exhaustion records `unknown/manual_review`.
Schema-v3 rows missing the new fields decode with zero authorization and cannot
read or mutate until explicitly reviewed.

Fresh post-mutation typed `pending` uses a distinct system continuation record,
not the operator Resume record. The mutation's mandatory owner read persists
`PendingReadbacks=1`, a zero-mutation/zero-replay authorization, and an exact
account/Launch/Workspace/stage/idempotency/attempt/version binding in one CAS.
At most two subsequent reads are allowed. Before each GET, Control Plane claims
and increments the exact ordinal by CAS; a loser stops before GET, and a crashed
claim is never refunded or reissued. Ready consumes the authorization and
advances, pending may claim only a remaining slot, and unknown/conflict/error or
exact exhaustion records `unknown/manual_review`. A schema-v3 row without the
new authorization and claim maps is explicitly zero-budget.

Fabric's child transport claim is a local replay epoch, not Control Plane
operator authorization and not a second business attempt budget. It binds the
parent operation, exact child identity, original idempotency key, and lease
generation only to serialize dispatch and crash recovery inside Fabric.

Control Plane uses its own Fabric transport identity for these mutations and
signs a short-lived capability binding account, Workspace, resource kind/id,
action, operation identity, expiry, and request-body digest. Fabric derives the
expected scope from the typed request and rejects missing or mismatched
capabilities before operation-store or provider mutation. Runner transport
identity remains limited to job lease routes.

Ordinary Fabric Runtime status is a non-secret read and always redacts the
provider password. Credential reveal is a separate POST issued only after the
Control Plane verifies the Workspace owner; Fabric requires the same short-lived
request-bound capability and independently matches account and Workspace to the
persisted Runtime operation before returning the password. The former compute,
volume, and snapshot sync HTTP routes had no product caller and are absent;
Fabric's internal reconciliation methods remain owned by the service and are not
transport-token-only public writes.

The targeted compute-pool-head terminalization route is the only current
operator exception. Its protected Instance workflow must sign `caller=operator`
for the exact request body, while Fabric independently derives account,
Workspace, node-pool, approval, and replay scope from its persisted candidate or
exact terminal evidence before accepting the capability. The product source and
tests define this protocol; Instance credential wiring, deployment, and runtime
readback remain owned by `opl-instance-medopl` and are not implied by source
absorption.

## Persistence

Control Plane, Fabric, and Ledger each own their PostgreSQL schema and table
namespaces. Cross-service writes go through typed HTTP clients; no service writes
another service's tables. Sub2API data remains in Sub2API. The portable Compose
configuration and its source-built acceptance prove separate roles/databases and
cross-owner denial. Legacy production credentials have not been replaced and
read back through the Instance owner, so production adoption remains unproven.

Ledger verifies capability signature, caller, resource, action, operation,
expiry, and body digest before any owner lookup, then compares the claims with
the persisted account and Workspace. Only Receipt identity is used for the
capability owner lookup. Artifact and review identifiers remain provenance
columns for historical compatibility, while `review_policies` remains a
historical table with no current writer, API, or migration/delete operation.

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
repurchase. Ledger receipt failure retries only the receipt. Source and focused
tests implement this behavior; live Sub2API and Tencent evidence remains pending,
and the repository remains `code-complete=false`.

Activation readback is the Control Plane `GetWorkspace(workspaceId)` point-read
projection matched to the original launch and Fabric bindings; the removed
`POST /fabric/workspace-activation-truth` route is not authority. The terminal
purchase receipt uses `RequestID=launchOperationId` and
`Idempotency-Key=<launchOperationId>:purchase-receipt`, with exact Workspace,
debit code, user, total, component, and downstream resource identities.

Workspace DELETE is a separate durable Control Plane owner operation. Before
cleanup, it reads the exact Ledger purchase Receipt and exact Sub2API debit
history entry. It then consumes Fabric's typed Runtime and Gateway Secret
observations (`ready/absent/pending/conflict/error`) and advances only through
the same-operation chain `runtime + Secret absence -> attachment -> storage ->
compute -> Sub2API Key absence -> exact Sub2API business refund -> Ledger refund
Receipt -> Control Plane Workspace absence`. The operation binds the same
account, Workspace, Runtime, Key, debit code, purchase Receipt, and refund
Receipt throughout. Refund response loss performs exact-code GET only; Receipt
failure retries only the Receipt. No Local Docker runner, Fabric adapter, or
operator wallet adjustment owns Key deletion or the business refund.

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
stable per-Workspace credential seed. Tencent/TKE stores them in a Kubernetes
Secret; `local-docker` stores immutable versions under a protected host-owned
root and mounts only the selected version read-only into the Runtime.
Control Plane resolves the target Workspace's persisted `workspaceApiKeyId` and
hands the Key transiently to Fabric. Fabric writes or rotates a deterministic
Workspace-scoped secret bound to account, Workspace, Key ID, and fingerprint,
and records only its ref, version, and fingerprint. Existing
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

During the current pre-1.0 phase, Cloud must produce a replaceable candidate
from one exact canonical product SHA before formal publication.
`opl-instance-medopl` owns deployment and qualification of that exact SHA and
image digest through its protected environment. Only after successful rollout
and product-acceptance readback may the repository owner manually publish the
same SHA and image bytes as a formal Release. Cloud does not dispatch or operate
the Instance, and failed development or deployment attempts do not create a
formal version.

The current Release workflow cannot yet implement that complete path. It builds
and validates the OCI layout in a read-only job, passes one digest-checked
Actions artifact to a separate publish job, and grants
`contents:write`, `packages:write`, `artifact-metadata:write`,
`attestations:write`, and `id-token:write` only to that publish job under the
protected `cloud-release` Environment. Both jobs run in one owner-only manual
Release dispatch, so the repository still lacks a deployable non-Release
candidate channel and exact-byte promotion from an already qualified candidate.
Until those gaps close, no successor to `v0.1.7` is admitted.

The build emits a SHA-256 manifest for every GitHub Release asset; the publish job
checks those bytes, signs a GitHub OIDC-backed attestation that binds the
workflow commit/ref, selected product SHA, release tag, image digest, and
checksum-manifest digest, publishes the assets, downloads them again, and
verifies both checksum and predicate identity. The image is identified by a
version tag, exact product SHA, and immutable digest; mutable `latest` and
`stable` tags are forbidden.

Only `v0.1.7` remains on the current GitHub Release, Git tag, and GHCR public
surfaces. Hosted run `31879240411` published its five assets from product SHA
`a59bde68397528186a5220f73195fa1f3eda311b`; the multi-architecture GHCR index
for `linux/amd64` and `linux/arm64` is
`sha256:e64504731f8b61c0864cf59faa647a1150e8a2a5eada34b26faf3a5487d28e8f`.
The owner removed historical `v0.1.0` through `v0.1.6` Releases, tags, and GHCR
objects, so none is a current installation or rollback target. The `v0.1.7`
readback proves portable publication; clean-host installation, the complete
external Sub2API-backed Workspace flow, and Instance adoption remain separate
evidence surfaces.

Repository security automation currently uses GitHub-managed CodeQL default
setup rather than a second workflow-owned CodeQL configuration. Pull requests
have a dedicated read-only Dependency Review job, and Dependabot covers
GitHub Actions, npm, all four Go modules, and the root Dockerfile on a bounded
monthly schedule. These scanners produce evidence for triage; they do not
change product or vulnerability status by themselves.

GitHub's repository `Agents` tab is the Copilot cloud-agent task surface. No
repository custom agent profile or automation is part of the current Cloud
implementation. If adopted later, it remains a PR-producing development tool
with no direct canonical, release, deployment, or production authority; OPL
product Agents and domain agents are separate concepts.

The private `opl-instance-medopl` repository owns the current medopl/TKE
configuration, production environment, deployment workflow, rollback, canaries,
receipts, and instance-specific tool source. Its first TKE deployment receipt
proves the exact `v0.1.7` Cloud artifact and public health readback, while
keeping readiness and Acceptance B incomplete. Fresh production claims still
require Instance workflow readback for the exact Cloud candidate SHA and image
digest; formal publication must retain those same bytes.

Control Plane remains one Pod. Existing load evidence covers request concurrency
and replay, but its historical per-resource renewal scan is not proof of the
current Workspace renewal saga. The current gates must run against an isolated
PostgreSQL database. Additional replicas remain out of scope unless
production measurements justify the ownership and locking changes.

Infrastructure alarms remain in Tencent Cloud Monitor. Business alarms are a
projection of Workspace renewal operations plus compute/storage compatibility
facts; there is no alert table. Stable, redacted transition codes drive CLS
alerting.
