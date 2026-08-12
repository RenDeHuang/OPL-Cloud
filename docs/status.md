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
  boundary effective. Required CI separately exercises the Accounting path
  through real Control Plane HTTP backed by PostgreSQL and a real Ledger HTTP
  process backed by its own PostgreSQL database, with a typed Sub2API authority
  fixture. The Ledger `sslmode=disable` exception is limited to an explicit
  non-production, loopback `OPL_POSTGRES_TESTS=1` gate; it is test plumbing, not
  production evidence.
- Fabric defaults to a real `local-docker` adapter and keeps Tencent/TKE behind
  explicit instance selection. CI exercises local compute, storage, attachment,
  Secret binding, Runtime, and authoritative readback. The generic provider-facts
  Service path now performs only identity validation, adapter delegation, error
  projection, and `LastReadAt` stamping. Tencent `InstanceType`, `providerData`,
  CVM/CBS, NodePool, and `costTags` interpretation belongs to the Tencent adapter;
  the local-Docker adapter has compute, storage, attachment, and Runtime fact
  parity. The typed `POST /fabric/provider-facts/batch` wire remains compatible
  and fails closed on identity or readback errors. Focused tests prove this path
  is read-only, with no provider mutation or Fabric operation write. This is
  source and CI evidence, not a complete Console-to-Workspace installation or
  the completion of the wider Provider Acceptance migration.
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
  request-hash vectors as Fabric. Activation readback uses the canonical
  `currentComputeAllocationId` and `currentAttachmentId` Workspace projection
  fields, with retained read compatibility for older projections. A separate
  legacy provider-acceptance surface still contains Tencent-specific client and
  projection knowledge.
- The zero-caller `ReapplyWorkspaceRuntime` Control Plane facade method is
  removed. This application-code cut neither rewrites nor deletes historical
  `runtime_apply` rows and does not mutate or retire Fabric resources. The real
  Control Plane `Service` and capability boundaries remain; broader facade and
  unabsorbed `server/app_state` dead-helper simplification remain open.
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

Focused local and required-CI evidence exists for the single Workspace Launch
Reconciler, immutable Resume authorization, typed Fabric stage binding, real
local-Docker Fabric stages, source envelopes, Console behavior, and the Accounting
source path. The Accounting test proves one stable debit, no additional debit on
replay, one purchase receipt, and linked wallet, balance-history, Workspace, and
operation identities. When the debit response is lost and history is temporarily
unknown, it stays at `manual_review` / `debit`; replay creates no Workspace or
receipt, issues no additional debit, and does not refund. This uses a typed
Sub2API fixture and therefore does not prove external Sub2API authentication,
funding, live balance/usage, or product readiness.

The base Compose profile remains a low-authority control-services path. The
explicit `compose.local-workspace.yaml` profile now enables the Launch worker,
mounts a configured Docker socket only into Fabric, and requires an immutable
Workspace image. Its real smoke reached the host Docker Engine but stopped at the
required external Sub2API authentication boundary, so it does not prove the
complete live Console create/readback/open/delete path, browser, renewal,
rollback, or production path. Existing Tencent/TKE evidence applies only to
medopl instance provenance.

The base Compose asset, explicit local-Workspace override, GHCR/GitHub Release
workflow, and focused distribution checks exist at source level. The workflow
validates `compose.local-workspace.yaml`, records it in the release manifest,
uploads it with the base Compose file and environment template, and checks the
exact four-asset GitHub Release readback. Fresh GitHub owner readback shows no
OPL Cloud tag, Release, or GHCR package, and no clean-host installation evidence
exists. Source and CI evidence therefore do not prove a published immutable
product or an installed application.

GitHub security controls were read back on 2026-08-12 for
`main@1ea5540736c0f2cae5b51fc983e18509b20bea49`. Private vulnerability
reporting, Dependabot alerts and security updates, secret scanning and push
protection, Actions full-SHA pin enforcement, and branch-protection admin
enforcement are enabled. CodeQL default setup is configured weekly for Actions,
Go, and JavaScript/TypeScript; run `31562369252` completed successfully at that
exact commit. The repository has zero open secret-scanning alerts and zero open
Dependabot alerts. Secret validity checks remain disabled after the attempted
setting change did not take effect. Actions still allow all action sources,
with full-SHA pinning as the current executable restriction; `main` requires
only the strict `validate` context, resolves review conversations, and forbids
force pushes and deletion.

CodeQL success did not produce a zero-alert baseline. Fresh API readback reports
15 open alerts: two allocation-size-overflow results and thirteen weak-hash
results across product source, tools, and tests. They are scanner leads pending
boundary-specific triage; this snapshot does not classify them as 15 confirmed
vulnerabilities or dismiss them as false positives. A separate sealed,
risk-based static scan of revision
`24a065d4427b53d65ba0df9cb70b1a36327fb6af` reported three medium and seven low
findings with partial coverage and no runtime exercise. None is recorded as
fixed by the scan, by enabling GitHub controls, or by this documentation.

The repository has no `cloud-release` Environment, tag ruleset, immutable
release enforcement, repository custom coding-agent profile, or coding-agent
automation. A read-only Dependency Review job and expanded monthly
Dependabot coverage exist on the security-hardening task branch until canonical
absorption and GitHub execution readback prove them on `main`; Dependency Review
is therefore not yet a required check.

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
