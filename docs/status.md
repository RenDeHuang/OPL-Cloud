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

## Remote Companion Broker

The Control Plane candidate contains the Cloud-owned remote companion broker
source, Ent persistence, canonical `/v1/remote-companion` snake_case wire, stable
server-keyed invitation/claim hashing, atomic 40-seat reservation, desktop SAS
confirmation, and provider-reclaim receipts with retryable partial state. The
App-aligned mutation paths are `/pairings/claim`,
`/pairings/{pairing_id}/credentials`, and `DELETE /pairings/{pairing_id}`;
receipt state is read through `/revocations/{revocation_receipt_id}` and every
mutation requires `Idempotency-Key`.
Focused tests cover invitation single-use, concurrent seat bounds, manual-code
attempt invalidation, revocation retry, and Tencent TLS UserSig encoding. This is
Cloud-source and local-test evidence only: no live Tencent application Secret,
TestFlight or App Store release, or China three-network qualification is proven
by this repository change.

## Implementation Snapshot

- Control Plane's retained Ent persistence implementation is split inside the
  existing `server` package into identity, resource, and Workspace capability
  files. Fabric's retained Tencent provider implementation is likewise split
  into compute, storage, and Runtime capability files. The existing receivers,
  public interfaces, typed HTTP contracts, PostgreSQL schemas, provider
  operations, and authority boundaries are unchanged. Focused tests plus the
  repository's complete local PostgreSQL, capacity, and local-Docker gate pass;
  no live Tencent mutation was run in this Cloud-source gate. The Instance
  deployment receipt is recorded below. The remaining mixed
  facades stay in the cohesion backlog and require caller-led slices rather than
  a new cross-service framework.
- `npm run verify:local` is the repeatable source gate for product boundaries,
  Node tests, Console typecheck/lint/build, whitepaper rendering, all-module Go
  compilation, database-free Go tests, and Git whitespace. The separate
  `npm run verify:local:full` gate starts an ephemeral PostgreSQL 16 container,
  requires every PostgreSQL, Control Plane capacity, and Fabric local-Docker
  test to finish with zero skips, and removes the container on exit. These are
  local source/runtime test facts only; neither command accesses production or
  proves Instance adoption.
- Structural over-design cleanup landed through PR `#285`: staticcheck-U1000 and
  zero-caller dead symbols were removed from Control Plane server/app-state and
  Fabric operator-identity/service/provider/provisioner surfaces, including two
  unreferenced provider-port interfaces; the zero-importer
  `services/fabric/internal/tencent` package, unreferenced
  `tools/runtime-fact-source-eval.sh`, superseded or absorbed brand assets, and
  Console Alert/Tooltip pass-through wrappers were deleted. The Console imports
  Tailwind CSS through `apps-sdk.css`, so `tailwindcss` remains an explicit root
  peer/build dependency required by `@openai/apps-sdk-ui`. The test-only
  `memory_table_store` no longer ships in the production binary, and the
  persistent server constructor now requires a `controlPlaneTableStore` instead
  of a never-taken memory fallback. Sub2API, Fabric, and Ledger client role
  interfaces (capability gating with tested negative paths) and the module-local
  ops environment catalogs were re-checked against callers and tests and
  retained. This is source and CI evidence only; it does not change
  code-complete, Pilot, or production flags.
- The current simplification slice removed the retired staging verifier entry,
  its tombstone test, and the unused `path-to-regexp` override through PR
  `#340`; made the Ledger evidence contract the single reconciliation-report
  schema owner through PR `#341`; moved three retained deployment tools to
  tool-local `node:util.parseArgs` through PR `#342`; and consolidated stable
  Qualification checkout, Node, PostgreSQL, and Go-test YAML through PR `#343`.
  The same deletion-first pass then retired the 1,767-line aggregate deployment
  migration contract through PR `#345`, made the Ledger contract the single
  Workspace monthly-receipt schema owner through PR `#346`, removed the
  caller-zero Control Plane Artifact/Review/Continuation Ledger client adapter
  through PR `#347`, and removed 312 lines of superseded Console declarations
  through PR `#348`. Desktop 1280px and mobile 390px Console comparisons were
  pixel-identical after the CSS cleanup. Across PRs `#340`-`#343` and
  `#345`-`#348`, the implementation and contract patches remove 2,412 more
  lines than they add. `npm run verify:local` and every required PR check pass
  after canonical absorption.
- The current Ledger simplification slice retires the structured Artifact,
  Review, ReviewPolicy, ReviewGate, and Continuation APIs, stores, routes, and
  generated Ent code. Receipt `artifactId`, `reviewId`, `outputRefs`,
  `reviewerChecks`, `continuationId`, and `continuation` remain caller-owned
  opaque provenance; Ledger does not generate continuation identities, hide
  provenance on reads, or authorize Workspace operations. Historical
  `review_policies` rows and Receipt provenance columns remain retained without
  migration or deletion. Focused Ledger Go tests and
  `npm run verify:local:full` pass, including the retained Ledger PostgreSQL
  path with zero skips and the complete local-Docker vertical.
- Control Plane identity now uses `Account` and `User` as its only application
  and Ent models. Organization/Membership runtime stores, provisioning,
  authentication, reconcile paths, generated code, and test-only stores are
  removed. The historical tables, rows, and IDs remain under the
  `202608170002_legacy_identity_table_custody.sql` migration for read-only
  migration custody; no historical table or row was dropped.
- Protected Instance inventory run `31992269752` reported zero WorkspaceBackup
  rows, zero matching Fabric operations, zero `VolumeSnapshot` objects, zero
  `VolumeSnapshotContent` objects, and zero restored PVCs, with zero database,
  Kubernetes, or provider mutations. Cloud then removed the caller-zero Fabric
  Snapshot/Restore service, provider, HTTP, replay, and focused-test surfaces;
  historical migrations and data custody remain unchanged.
- Two further native-replacement candidates were rejected instead of merged.
  Converting the next two CLI parsers to `node:util.parseArgs` added 54 net
  lines, while using `http.ServeFile` only for uncompressed assets retained the
  custom gzip path and added 27 net lines. Both task branches were returned to
  tree-equivalence with fresh `main` and lifecycle-closed. No release,
  deployment, database/resource migration, or production action was performed.

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
  decide readiness or resource continuity. Instance `main` now carries the
  instance-owned callers and protected workflow boundary, but this remains
  source and CI evidence, not a complete Console-to-Workspace installation or
  successful Instance Provider Acceptance.
- The Fabric resource catalog contract now retains only provider-neutral package,
  storage-class, ingress, availability, and capacity boundaries. Its unused
  `workspacePackageNodePools` provider-specific subtree was removed through
  Catalog hard-cut PR `#295`; NodePool, SKU, bootstrap, ownership, and launch
  interpretation remain owned by the Fabric adapter/provisioner and Instance
  workflow. This contract cleanup does not change catalog runtime behavior or
  establish production acceptance.
- Control Plane Provider Acceptance now consumes Fabric's provider-neutral
  monthly-preflight availability plus Control Plane-owned package, size, and
  zone facts. It no longer interprets Tencent purchase mode, renewal policy, or
  CVM/CBS resource kinds. The isolated Console recovery-plan DTO, read adapter,
  controller intents, and Admin review modal are removed; operator
  reconciliation projects the server-owned action back into the same durable
  Launch Reconciler. Instance recovery workflows now execute their tools from
  the Instance checkout; exact candidate deployment and production acceptance
  remain external owner obligations. PR `#280`'s legacy Launch
  migration implementation was replayed against current `main` and not
  admitted: its test harness was Linux-only, its compute readback used retired
  Fabric interfaces, and its partial-history response did not satisfy the
  Control Plane next-stage boundary. No protected Instance inventory currently
  proves an eligible schema-2 row or active consumer. Current source therefore
  has no legacy migration route or temporary Fabric contract; a fresh
  implementation is triggered only by owner-authoritative row evidence.
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
  The later zero-caller `PrepareWorkspace` orchestration path, its private
  Runtime-readback merge helper, and its dedicated errors are removed as one
  closed dead chain. `CreateWorkspaceInput` remains because Provider Acceptance
  has a real caller. The real Control Plane `Service` and capability boundaries
  remain; none of these finite deletions admits an aggregate replacement facade
  or broader removal.
- The authenticated Workspace owner can issue one durable, resumable delete
  command. Control Plane coordinates Runtime, attachment, storage, and compute
  cleanup through existing typed Fabric HTTP routes; partial or unknown results
  remain unconfirmed, and success requires authoritative Workspace-list
  readback. This is source and CI evidence, not a complete live installation.
- ContentTransfer application runtime/API/Ent schema, Archive application models,
  `ExecutionRequest` application code, and Control Plane `WorkspaceBackup` Ent
  model are retired; historical migrations, tables, and data were not dropped.
  Snapshot/Restore application service, provider, HTTP, replay, and focused-test
  surfaces are also retired after the protected zero inventory recorded above.
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
workflow, and focused distribution checks exist at source level. Historically,
eight Product Releases, `v0.1.0` through `v0.1.7`, were published between
2026-08-13T09:50:02Z and 2026-08-15T10:49:30Z while the same Acceptance B path
was still under development. The repository owner removed `v0.1.0` through
`v0.1.6` from the GitHub Release, tag, and GHCR public surfaces on 2026-08-15.
Only `v0.1.7` remains. Release workflow run `31879240411` published it from
exact product SHA
`a59bde68397528186a5220f73195fa1f3eda311b`. Its GHCR multi-architecture index
digest is `sha256:e64504731f8b61c0864cf59faa647a1150e8a2a5eada34b26faf3a5487d28e8f`,
with `linux/amd64` and `linux/arm64` manifests. The GitHub Release contains
`compose.yaml`, `compose.local-workspace.yaml`, `opl-cloud.env.example`,
`SHA256SUMS`, and `opl-cloud-release.json`; manifest readback matched the
release tag, product SHA, image digest, platforms, and asset set. This proves
publication of the portable product artifact, not installation, Instance
qualification, or production readiness.

Current public readback shows only Git tag and GitHub Release `v0.1.7`, and only
the `v0.1.7` GHCR tag. GHCR retains its top multi-architecture index plus four
required child/attestation objects. The five downloaded Release assets match
their API digests and `SHA256SUMS`. GitHub still reports `immutable=false` and
no tag ruleset; those settings do not override the repository owner's explicit
cleanup authority. Later canonical `main` commits must not be presented as the
`v0.1.7` product SHA or digest.

The current pre-1.0 admission decision now requires successful
`opl-instance-medopl` deployment and product readback for an exact candidate
SHA/digest before a formal successor Release. The existing workflow cannot yet
prove that order because one dispatch builds and publishes the image. A
deployable non-Release candidate channel and exact-byte promotion remain open,
along with clean-host installation, the complete live Console
create/readback/open/delete path, and real external Sub2API
authentication/balance/usage evidence.

The seven successful `v0.1.1` through `v0.1.7` workflow runs spent 1,361 to
1,963 seconds in the multi-architecture image build while their publish jobs
took 44 to 103 seconds. The current source removes target-architecture
emulation from the compute-heavy Node and Go build stages, cross-compiles the
four Go binaries for the selected image platform, reuses one Fabric dependency
build for both Fabric executables, and persists reusable BuildKit layers in the
GitHub Actions cache. The immutable OCI artifact, protected publish job,
checksums, attestations, and public readback remain unchanged. An uncached local
`docker-container` Buildx run on an Arm host completed the full `linux/amd64` +
`linux/arm64` OCI layout in 66 seconds and emitted both target manifests plus
their attestations. This proves the optimized build path locally, not GitHub
runner performance; no successor Release is admitted merely to benchmark it,
so a hosted post-change Release duration is not yet available.

GitHub security controls were read back on 2026-08-13 for the post-release
canonical baseline before this documentation merge. Private vulnerability
reporting, Dependabot alerts and security updates, secret scanning and push
protection, Actions full-SHA pin enforcement, and branch-protection admin
  enforcement are enabled. CodeQL default setup is configured weekly for Actions,
  Go, and JavaScript/TypeScript; run `31674571131` completed successfully for all
  three analyses at the current `main` SHA. The repository has zero open
secret-scanning alerts and zero open Dependabot alerts. Secret validity checks
remain disabled after the attempted setting change did not take effect. Actions
allow GitHub-owned and verified creators plus the SHA-pinned reusable whitepaper
workflow; other action sources are denied and full-SHA pinning remains required.
The default workflow token is read-only and cannot approve pull requests.
`main` strictly requires the GitHub Actions `validate` and `dependency-review`
contexts, resolves review conversations, and forbids force pushes and deletion.

CodeQL success did not produce a zero-alert baseline.
`SECURITY-CODEQL-TRIAGE-01` completed with `mutation_zero`: the original 15
high-security-severity alerts were individually classified `not_actionable`, with
zero `confirmed` and zero `needs_review`; GitHub now reports alerts `#12` and
`#13` as `fixed`. Fresh GitHub API readback for the current `main` reports 15
alerts still open:
`#1`-`#11` and `#14`-`#17`. Alerts `#16` and `#17` are the newly surfaced
`go/weak-sensitive-data-hashing` comments from merged PR `#287`; static review
matches the already triaged Go fingerprint family: SHA-256 is used for opaque
Workspace API-key fingerprints and replay/readback consistency, not password
storage, password verification, or authorization. They remain open in GitHub;
this classification does not authorize dismissal or a code change. No alert
dismissal, settings mutation, or security-only code write was performed. This
triage result therefore does not establish a zero-alert baseline or product
readiness. A separate sealed, risk-based static scan of revision
`24a065d4427b53d65ba0df9cb70b1a36327fb6af` reported three medium and seven low
findings with partial coverage and no runtime exercise. That sealed report
remains immutable historical evidence; later source remediation and rescans are
recorded separately below rather than rewriting its finding dispositions.

The canonical S0 baseline includes the disclosure policy, read-only Dependency
Review job, and expanded monthly Dependabot coverage. PR `#261` merged as
`919abb76a2177e0c10db0b97a4adb41ada4bc5ef`; its exact head passed
`dependency-review`, `validate`, and the three CodeQL language analyses before
merge. GitHub now requires strict `validate` and `dependency-review` on `main`.
The `cloud-release` Environment exists with protected branches as its only
protection rule and has no Secrets or variables. The Release workflow has only
the manual `workflow_dispatch` trigger, and both build and publish require the
original actor and current triggering actor to equal the repository owner. It
is create-only during ordinary publication and rejects an existing Git tag,
GitHub Release, or GHCR release tag. PR `#334` merged this owner gate as
`4060590fde52e9224c45968857729650806c990a`; all ten hosted checks passed and no
new Release run was triggered. These controls prevent collaborator or
accidental CI publication, but they do not yet provide the pre-Release candidate
deployment path.

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
non-browser JSON paths. The current Control Plane support API derives user,
status, category, and priority from server/session authority; its login-rate
state has bounded keys, capacity, and TTL eviction, and its Fabric client bounds
success and error responses. Control Plane already bounds incoming JSON bodies;
Fabric now applies a uniform 1 MiB JSON admission before capability,
operation-store, or provider mutation, and Ledger applies the same bound before
store calls on its JSON POST routes. Oversized requests return `413` while
existing authentication and idempotency ordering remains covered. The admitted
S4 source slices are now closed: release build inputs are pinned and verified by
the canonical release-boundary validator. Current workflow source also emits a
per-asset `SHA256SUMS`, creates GitHub OIDC-backed provenance attestations for
the exact Release asset bytes, binds both the signing workflow revision and the
separately selected product SHA, and verifies downloaded assets against the
checksum manifest, signer workflow, release identity, and image digest. Release
`v0.1.7` executed this path: hosted run `31879240411` completed both the
`Attest release assets` and `Read back release` steps, and the published asset
set includes `SHA256SUMS`. This is exact `v0.1.7` publication evidence, not
retroactive proof for earlier Releases.
Clean-host installation and production deployment remain `PRODUCT-RELEASE-01`
/ Instance evidence gaps, not another S4 source lane. These controls are source,
test, and exact `v0.1.7` publication evidence only. CodeQL triage is
terminal at the classification layer only, with alert disposition explicitly
unperformed as recorded above.

The repository security scan for canonical revision `b7217daddf1520d7f442cea5b8dba2c6df636cdf`
reported eight source findings (five high, three medium). PR `#307` absorbed
their source remediation at `884e02c1e4242fc33ff6aea55dc4594e61c68cbc`, and PR
`#308` absorbed the subsequently discovered compute-pool-head terminalization
capability omission at `5fc1e5fba29837c6fb2215c427992de966b6a5e5`.

Standard scan `55437d10-456a-41ae-b39c-c7e4f0cdbd81` was then sealed against
that exact canonical revision. It did not reproduce the earlier terminalization
finding and reported five new high-confidence source findings: one medium
Control Plane body-limit bypass and four low findings covering Fabric Runtime
credential disclosure, three zero-caller Fabric sync HTTP mutations, Ledger
owner lookup before capability rejection, and unbounded Workspace renewal
command history. PR `#309` absorbed their source remediation at canonical
revision `d8a4df0f130a1545da0efe43dfebe16fa08e5844`.

The ten occurrences still shown as unresolved in older scan
`b07b4eaa-a94a-47fd-9023-5e7838bc657b` belong to revision
`24a065d4427b53d65ba0df9cb70b1a36327fb6af`. Fresh source revalidation against
the current canonical revision did not reproduce them; the UI disposition is
historical occurrence workflow state, not current-source evidence.

Sealed Standard scan `761fd61d-b7ee-41ff-afd2-34f5671b1af5` against canonical
`d8a4df0f130a1545da0efe43dfebe16fa08e5844` reported one new low-severity,
high-confidence finding: authenticated Fabric job heartbeat, Workspace Runtime
status, and operation-list paths materialize unbounded shared operation history,
while fresh heartbeat keys continually append rows. The current FG-184 source
candidate replaces request-path full-list filtering with indexed bounded
queries, keeps one mutable heartbeat row per job attempt, and requires
`limit`/cursor pagination with a maximum page size of 100; all known production
and recovery callers follow every cursor page and reject a repeated cursor.
Focused memory-store, Fabric HTTP, caller, and PostgreSQL 16 integration tests
pass, including point lookup, duplicate fail-closed behavior, 50 fresh heartbeat
keys with three total job rows, and complete cursor traversal. This candidate is
not yet canonical `main`, and the finding remains open pending absorption plus a
fresh sealed scan of the absorbed revision. No
production, private-network, deployment, provider mutation, or live load test
was performed.

Fresh Instance source and receipt evidence now prove a bounded first deployment,
but not full product acceptance. Instance `main` now owns and executes the
medopl production, acceptance, recovery, diagnostic, render, and rollout tools;
the current workflow and manifest still do not prove the full Fabric capability
credential set or a deployable non-Release candidate channel whose exact bytes
can later be promoted as a formal Cloud Release.

The `opl-instance-medopl` repository now owns the medopl profile, production
workflow source, instance-specific production/acceptance/recovery tools, and
receipts. Its checked-in first-deployment receipt records a successful TKE
rollout and public health readback for Cloud release `v0.1.7`, while keeping the
tracked profile `deployed_unverified`: runtime readiness is `ready=false` and
Acceptance B remains incomplete. This is Instance-owner evidence, not a Cloud
deployment claim; current deployment, Runtime, billing, rollback, and receipt
claims must still be read back from Instance for the exact candidate.

Cloud GitHub no longer carries a medopl production Environment, Secret/variable
surface, or historical medopl Deployment record. Fresh API readback lists only
the current `cloud-release`, `github-pages`, and `whitepaper-production`
Environments and 13 corresponding Deployment records: eight release records,
three Pages records, and two whitepaper records. The protected `cloud-release`
Environment remains the product-release authority; Pages and whitepaper retain
their own publication evidence.

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

The aggregate launch and deployment guards are retired. Focused billing, Control
Plane Launch, Fabric binding, Ledger evidence, portable distribution, and
Instance deployment owners retain their hard facts. The remaining Instance
acceptance and Cloud external-settings cleanup are operational gaps, not a reason
to recreate an aggregate machine contract; their acceptance conditions live only
in [the roadmap](./roadmap.md).

## Evidence Interpretation

The durable definitions of `code-complete`, `pilot-ready`, and
`production-proven` live in [the invariants](./invariants.md). Executable checks
live in source, test, and workflow owners. Product and structural gaps live in
[the roadmap](./roadmap.md); this snapshot does not maintain a second action
list.
