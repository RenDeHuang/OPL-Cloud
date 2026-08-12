# OPL Cloud Current Status

Owner: `one-person-lab-cloud`
Purpose: `replaceable_current_evidence_snapshot`
State: `current_snapshot`

This file reports what is implemented or evidenced now. It does not define the
target architecture, long-term invariants, open priorities, workflow procedure,
or historical provenance.

## Product Boundary

The current Pilot implementation supports administrator-provisioned accounts.
One Console User maps to one Account and one Sub2API User/Wallet; one Account may
own multiple independent Workspaces. Public registration, customer payment,
shared multi-user Workspaces, backup/recovery/sync/transfer, HA, and GPU are not
current customer capabilities.

Basic and Pro are selectable Workspace packages. Catalog visibility is not
provider-capacity evidence. Customer pricing is exact integer USD micros and
Sub2API remains the only spendable-balance authority.

The configured public model `/v1` endpoint is projected by Control Plane. Its
current presentation is a Console implementation choice. The server-only
Sub2API management origin and credentials are never exposed to the browser.

## Implementation Snapshot

- Public and login surfaces present the generic OPL Cloud product in user-task
  language, preserve the administrator-provisioned Pilot boundary, and use the
  current responsive Console implementation. This is presentation evidence,
  not evidence of a new functional capability.
- Console calls Control Plane product APIs only and projects live Sub2API,
  Fabric, Ledger, and Control Plane facts through customer-safe DTOs.
- Control Plane, Fabric, and Ledger are separate Go processes and PostgreSQL
  schema owners. Portable Compose creates separate service databases and roles
  and maps three distinct service tokens. A local-Workspace Compose smoke has
  started PostgreSQL, Ledger, and Fabric with those boundaries and proved that
  Fabric can reach the explicitly mounted host Docker Engine; Control Plane then
  failed closed at the required external Sub2API authentication boundary. No
  complete installation or current production instance readback proves the full
  boundary effective.
- Fabric defaults to a real `local-docker` adapter and keeps Tencent/TKE behind
  explicit instance selection. CI exercises local compute, storage, attachment,
  Secret binding, Runtime, and authoritative readback; this is Fabric evidence,
  not a complete Console-to-Workspace installation.
- Fabric's unused recovery proof/claim Service, provider, and operation-store
  mutation shell is retired. Five legacy resource inputs no longer carry
  unassigned `LaunchBinding` branches, and the orphan launch-binding readback is
  removed; the active typed Workspace Launch binding path, identity evidence,
  pool-head terminalization, historical migrations/data, and local-Docker gate
  remain.
- Typed Tencent Workspace Launch and the existing `TagComputeMachine` port now
  share one adapter-private compute-ownership core for deterministic CVM tagging,
  Kubernetes node claim, child operations, and authoritative replay readback.
  Provider-neutral Fabric and Control Plane boundaries are unchanged.
- Operator identity-evidence compute/storage readback and pool-head terminal
  replay no longer call the runtime operation-list path. They use narrow
  action/idempotency or approval/idempotency owner lookups, fail closed on an
  exact-identity conflict, and retain historical recovery-binding and mutation-
  ledger read compatibility. Other Fabric operation-list consumers remain.
- Workspace file bodies stay in provider-owned storage: a local Docker volume for
  the local adapter or CBS for the Tencent adapter. Platform PostgreSQL stores
  identity, operation, reference, and evidence facts rather than file bodies.
- Create and Resume now enter one durable Control Plane Reconciler. Its resource
  stages call the typed Fabric HTTP contract and consume the same six-field
  request-hash vectors as Fabric. A separate legacy provider-acceptance surface
  still contains Tencent-specific client and projection knowledge.
- The authenticated Workspace owner can issue one durable, resumable delete
  command. Control Plane coordinates Runtime, attachment, storage, and compute
  cleanup through existing typed Fabric HTTP routes; partial or unknown results
  remain unconfirmed, and success requires authoritative Workspace-list
  readback. This is source and CI evidence, not a complete live installation.
- ContentTransfer application runtime/API/Ent schema, Archive application models,
  and `ExecutionRequest` application code are retired; historical migrations,
  tables, and data were not dropped. Snapshot/Restore remains an extension
  surface pending owner-authoritative resource disposition.
- The Control Plane Session credential vault is process-local and single
  replica. Horizontal scaling is not supported until a secure shared vault and
  distributed wallet-mutation serialization boundary exist.
- Runtime projects-entry and filesystem-usage product APIs remain outside the
  current release; direct mount-marker checks do not prove those product APIs.

## Evidence Snapshot

Current delivery levels remain:

- `code-complete=false` for the repository as a whole;
- `pilot-ready=false`;
- `production-proven=false`.

Focused local and CI evidence exists for the single Workspace Launch Reconciler,
immutable Resume authorization, typed Fabric stage binding, idempotent settlement,
real local-Docker Fabric stages, source envelopes, and Console behavior. The base
Compose profile remains a low-authority control-services path. The explicit
`compose.local-workspace.yaml` profile now enables the Launch worker, mounts a
configured Docker socket only into Fabric, and requires an immutable Workspace
image. Its real smoke reached the host Docker Engine but stopped at the required
external Sub2API authentication boundary, so it does not prove the complete
Console-to-Workspace path, live Gateway accounting, browser, renewal, rollback,
or production path. Existing Tencent/TKE evidence applies only to medopl instance
provenance.

The base Compose asset, explicit local-Workspace override, GHCR/GitHub Release
workflow, and focused distribution checks exist at source level. The workflow
validates `compose.local-workspace.yaml`, records it in the release manifest,
uploads it with the base Compose file and environment template, and checks the
exact four-asset GitHub Release readback. Fresh GitHub owner readback shows no
OPL Cloud tag, Release, or GHCR package, and no clean-host installation evidence
exists. Source and CI evidence therefore do not prove a published immutable
product or an installed application.

This product repository holds no current instance deployment readback. The
`opl-instance-medopl` repository now owns the medopl profile and production
workflow source, but GitHub currently reports no Instance Environment or
Deployment, and the tracked profile remains `deployed_unverified` with no product
SHA, release tag, image digest, or receipt. Earlier medopl rollout and provider
evidence is migration provenance only; current deployment, Runtime, billing,
rollback, and receipt evidence must be read back from the Instance owner for one
exact Cloud release.

The Cloud GitHub repository still carries the legacy production authority. It
has six Environments and 2,084 historical Deployment records; 2,079 records name
the `production` environment, whose current configuration exposes 23 Secret names
and 31 variables. These records include every Actions job that declared an
environment and are not evidence of 2,079 server rollouts. The residual authority
is migration state, not evidence that Cloud still owns medopl deployment, and it
cannot be retired until the Instance successor and one exact deployment receipt
are proven.

Capacity evidence targets a 1000-provisioned-user data set. It does not claim
1000 concurrent users, concurrent provisioning, multiple Control Plane
replicas, or HA.

## Documentation And Contract Migration

The active documentation hierarchy now separates product concept, target
architecture, durable invariants, current implementation, functional modules,
status/roadmap, operations, and history.

The former Console display freeze is historical provenance, and presentation is
owned by the current Console implementation under an evolvable experience
guide. The machine Console UI contract and superseded package/shared-execution
machine contracts are retired.

The aggregate launch freeze is retired. Focused billing, Control Plane Launch,
Fabric binding, and Ledger evidence contracts now own its retained hard facts and
are exercised by real caller tests. The aggregate deployment contract remains a
migration guard until workflow-family owners retain authorization, identity,
Secret, immutable-image, mutation-bound, readback, and rollback gates. The open
sequence and acceptance conditions live only in [the roadmap](./roadmap.md).

## Evidence Interpretation

The durable definitions of `code-complete`, `pilot-ready`, and
`production-proven` live in [the invariants](./invariants.md). Executable checks
live in source, test, and workflow owners. Product and structural gaps live in
[the roadmap](./roadmap.md); this snapshot does not maintain a second action
list.
