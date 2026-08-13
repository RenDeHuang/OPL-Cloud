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

- Structural over-design cleanup landed through PR `#285`: staticcheck-U1000 and
  zero-caller dead symbols were removed from Control Plane server/app-state and
  Fabric operator-identity/service/provider/provisioner surfaces, including two
  unreferenced provider-port interfaces; the zero-importer
  `services/fabric/internal/tencent` package, unreferenced
  `tools/runtime-fact-source-eval.sh`, superseded or absorbed brand assets, the
  unused `tailwindcss` dependency, and Console Alert/Tooltip pass-through
  wrappers were deleted. The test-only `memory_table_store` no longer ships in
  the production binary, and the persistent server constructor now requires a
  `controlPlaneTableStore` instead of a never-taken memory fallback. Sub2API,
  Fabric, and Ledger client role interfaces (capability gating with tested
  negative paths) and the module-local ops environment catalogs were re-checked
  against callers and tests and retained. This is source and CI evidence only;
  it does not change code-complete, Pilot, or production flags.

- Public and login surfaces present the generic OPL Cloud product in user-task
  language, preserve the administrator-provisioned Pilot boundary, and use the
  current responsive Console implementation. This is presentation evidence,
  not evidence of a new functional capability.
- Control Plane login now requires an `application/json` media type before
  credential processing. Browser requests with an explicit `Origin` or
  `Referer` are compared with the configured public web origin and rejected
  before login, session persistence, cookie issuance, or CSRF-token issuance
  when cross-origin or invalid. Same-origin Console JSON login remains accepted,
  and JSON non-browser callers that send neither header remain compatible. This
  narrow login CSRF control entered canonical source through PR `#283`
  (`215c53d4fe4ddec938a1255b57080408d1182c67`); it does not close the other S4
  findings or prove a deployed Console path.
- Console calls Control Plane product APIs only and projects live Sub2API,
  Fabric, Ledger, and Control Plane facts through customer-safe DTOs.
- Control Plane, Fabric, and Ledger are separate Go processes and PostgreSQL
  schema owners. Portable Compose creates separate service databases and roles
  and maps three distinct service tokens. A source-built portable Compose
  isolation run started all three services against their own database identities,
  rejected cross-owner database access and caller-token impersonation, and
  rotated each service token without restarting PostgreSQL or unrelated
  services. This runtime acceptance entered canonical source through PR `#260`.
  It proves the reusable Cloud configuration at source revision; it is not a
  clean-host release installation or evidence that an Instance adopted the
  split. A separate local-Workspace Compose smoke started PostgreSQL, Ledger, and
  Fabric with those boundaries and proved that Fabric can reach the explicitly
  mounted host Docker Engine; Control Plane then failed closed at the required
  external Sub2API authentication boundary. Required CI separately exercises the
  Accounting path through real Control Plane HTTP backed by PostgreSQL and a real
  Ledger HTTP process backed by its own PostgreSQL database, with a typed Sub2API
  authority fixture. The Ledger `sslmode=disable` exception is limited to an
  explicit non-production, loopback `OPL_POSTGRES_TESTS=1` gate; it is test
  plumbing, not production evidence.
- Fabric defaults to a real `local-docker` adapter and keeps Tencent/TKE behind
  explicit instance selection. CI exercises local compute, storage, attachment,
  Secret binding, Runtime, and authoritative readback. The generic provider-facts
  Service path now performs only identity validation, adapter delegation, error
  projection, and `LastReadAt` stamping. Tencent `InstanceType`, `providerData`,
  CVM/CBS, NodePool, and `costTags` interpretation belongs to the Tencent adapter;
  the local-Docker adapter has compute, storage, attachment, and Runtime fact
  parity. The typed `POST /fabric/provider-facts/batch` wire remains compatible
  and fails closed on identity or readback errors. Focused tests prove this path
  is read-only, with no provider mutation or Fabric operation write. Provider
  Acceptance Phases B and C are also absorbed. Phase B moved the Control Plane
  route to one provider-neutral facts batch and a narrow acceptance Runtime path
  through PR `#270`. Phase C moved the Cloud Provider Acceptance CLI and
  production live-QA comparison through PR `#282`
  (`bb0a221b12273d4dd788003c3d44b0d14e8dee87`): canonical compute and storage
  provider IDs remain required authority, while legacy `nodePoolId` and
  `persistentVolumeId` values are optional response-only projections that do not
  decide readiness or resource continuity. The Instance owner has not adopted
  and read back this current contract. This is source and CI evidence, not a
  complete Console-to-Workspace installation or Instance Provider Acceptance.
- Fabric's unused recovery proof/claim Service, provider, and operation-store
  mutation shell is retired. Five legacy resource inputs no longer carry
  unassigned `LaunchBinding` branches, and the orphan launch-binding readback is
  removed; the active typed Workspace Launch binding path, identity evidence,
  pool-head terminalization, historical migrations/data, and local-Docker gate
  remain.
- Typed Tencent Workspace Launch and the existing `TagComputeMachine` port now
  share one adapter-private compute-ownership core for deterministic CVM tagging,
  Kubernetes node claim, child operations, and authoritative replay readback.
  Focused tests cover the typed compute, storage, attachment, Secret, and Runtime
  stage chain, exact CBS/static binding and Runtime/Gateway binding readback, and
  GET-only replay. Provider-neutral Fabric and Control Plane boundaries are
  unchanged. The ownership/readback and typed Launch slices entered canonical
  source through PRs `#275`, `#277`, and `#278`. This is implementation evidence,
  not live Tencent mutation or current medopl deployment evidence.
- Tencent compute-allocation identity validation now belongs to the Tencent
  adapter rather than generic Fabric `service.go`; targeted operator pool-head
  readback calls that adapter-owned validator. This source ownership cut entered
  canonical source through PR `#281`. Other Fabric cohesion work remains bounded
  by its real callers and owning adapters.
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
  fields, with retained read compatibility for older projections. The separate
  Provider Acceptance surface remains instance-oriented production tooling, but
  its Cloud route and callers now decide readiness from provider-neutral facts
  and canonical provider IDs rather than legacy NodePool/PV projections.
- The zero-caller `ReapplyWorkspaceRuntime` Control Plane facade method is
  removed. This application-code cut neither rewrites nor deletes historical
  `runtime_apply` rows and does not mutate or retire Fabric resources. The
  zero-caller `server/app_state` forwarding and cache helpers are also removed.
  The real Control Plane `Service` and capability boundaries remain; neither
  finite deletion admits an aggregate replacement facade or broader removal.
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
and preserves the exact four-asset GitHub Release contract. This revision builds
and validates the multi-architecture OCI layout in a read-only job with no
checkout credentials or `GH_TOKEN`, then hands one digest-checked Actions
artifact to a separate publish job. Only publish receives `contents:write` and
`packages:write`, runs no checkout, dependency install, repository code, or
third-party Action, and is bound to the protected `cloud-release` Environment.
Local multi-architecture OCI build and publish-command dry-run evidence does not
prove the current split workflow on GitHub-hosted runners. Historical hosted
release-workflow runs predate the split and did not leave a current OPL Cloud tag
or GitHub Release; no qualifying current-revision GHCR digest/package readback or
clean-host installation evidence exists. Source, historical workflow, and local
evidence therefore do not prove a published immutable product or an installed
application.

GitHub security controls were read back on 2026-08-13 for
`main@215c53d4fe4ddec938a1255b57080408d1182c67`. Private vulnerability
reporting, Dependabot alerts and security updates, secret scanning and push
protection, Actions full-SHA pin enforcement, and branch-protection admin
enforcement are enabled. CodeQL default setup is configured weekly for Actions,
Go, and JavaScript/TypeScript; run `31652027980` completed successfully for all
three analyses at the current `main` SHA. The repository has zero open
secret-scanning alerts and zero open Dependabot alerts. Secret validity checks
remain disabled after the attempted setting change did not take effect. Actions
allow GitHub-owned and verified creators plus the SHA-pinned reusable whitepaper
workflow; other action sources are denied and full-SHA pinning remains required.
The default workflow token is read-only and cannot approve pull requests.
`main` strictly requires the GitHub Actions `validate` and `dependency-review`
contexts, resolves review conversations, and forbids force pushes and deletion.

CodeQL success did not produce a zero-alert baseline.
`SECURITY-CODEQL-TRIAGE-01` completed with `mutation_zero`: all 15 high-security-
severity alerts were individually classified `not_actionable`, with zero
`confirmed` and zero `needs_review`. Fresh GitHub API readback for
`main@215c53d4fe4ddec938a1255b57080408d1182c67` still reports those same 15
alerts open after the successful current-main run. They cover
`go/weak-sensitive-data-hashing`, `js/weak-cryptographic-algorithm`, and
`go/allocation-size-overflow`. Alerts `#1` through `#15` remain open; no alert
dismissal, fix, setting mutation, or code write was performed. This triage
result therefore does not establish a fix, dismissal, zero-alert baseline, or
product readiness. A separate sealed, risk-based static scan of revision
`24a065d4427b53d65ba0df9cb70b1a36327fb6af` reported three medium and seven low
findings with partial coverage and no runtime exercise. None is recorded as
fixed by the scan, by enabling GitHub controls, or by this documentation.

The canonical S0 baseline includes the disclosure policy, read-only Dependency
Review job, and expanded monthly Dependabot coverage. PR `#261` merged as
`919abb76a2177e0c10db0b97a4adb41ada4bc5ef`; its exact head passed
`dependency-review`, `validate`, and the three CodeQL language analyses before
merge. GitHub now requires strict `validate` and `dependency-review` on `main`.
The `cloud-release` Environment exists with protected branches as its only
protection rule and has no Secrets or variables. No tag ruleset, immutable
release enforcement, repository custom coding-agent profile, or coding-agent
automation exists. These controls and checks do not prove dependency risk is
zero or that a release has run.

Security currentness now includes the absorbed S1, Gateway, S2, S3, and narrow
login-CSRF implementation lanes. PR `#271`
(`1635a949b0e9440de841e5163e7eb1980e4bd10d`) merged the split read-only build and
publish-only release workflow. PR `#272`
(`1fd5419081ffbc56b87ec7ee439561a44704cc32`) merged the dead Gateway helper
removal. PR `#276` (`92a4cd104adf6d76540ea4c203f792a539b31655`)
merged Local Docker trusted immutable-image admission: an unapproved repository,
bare digest, tag-plus-digest, case-variant, or multi-`@` reference fails before
Docker or operation-store mutation, while an approved repository or exact
release-manifest digest remains accepted. PR `#279`
(`f20984d999e4229fc23a74df8dd44e9e82cd7f5c`) merged separate Control Plane and
runner transport identities plus a short-lived, HMAC-bound Control Plane
capability over account, Workspace, resource kind/id, action, operation,
expiration, and request-body digest. Mismatch tests fail before Fabric operation-
store or provider mutation. These are source and test controls only: they do not
prove a release, installation, live external Sub2API path, Instance deployment,
or product readiness. PR `#283` adds the pre-session login request admission
described above without changing the accepted same-origin Console or headerless
non-browser JSON paths. The remaining S4 findings retain their own candidate or
successor obligations. CodeQL triage is terminal at the classification layer
only, with alert disposition explicitly unperformed as recorded above.

This product repository holds no current instance deployment readback. The
`opl-instance-medopl` repository now owns the medopl profile and production
workflow source, but GitHub currently reports no Instance Environment or
Deployment, and the tracked profile remains `deployed_unverified` with no product
SHA, release tag, image digest, or receipt. Earlier medopl rollout and provider
evidence is migration provenance only; current deployment, Runtime, billing,
rollback, and receipt evidence must be read back from the Instance owner for one
exact Cloud release.

The Cloud GitHub repository still carries the legacy production authority. It
has six non-release Environments in addition to `cloud-release`, and 2,086
historical Deployment records; 2,079 records name the `production` environment,
whose current configuration exposes 23 Secret names and 31 variables. These
records include every Actions job that declared an environment and are not
evidence of 2,079 server rollouts. The residual authority is migration state, not
evidence that Cloud still owns medopl deployment, and it cannot be retired until
the Instance successor and one exact deployment receipt are proven.

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
