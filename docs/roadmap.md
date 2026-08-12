# OPL Cloud Roadmap And Current Gaps

Owner: `one-person-lab-cloud`
Purpose: `single_active_gap_and_priority_owner`
State: `active_planning`

This document owns open product and structural gaps, priority, acceptance, and
phased simplification. It does not own target architecture, current evidence,
agent prompts, branch write sets, shell commands, cleanup procedure, or runtime
and production claims.

## Target Delta

OPL Cloud extends local OPL work into independent online Workspaces, approved
AI/resource use, optional exact Agent Service publication, and inspectable
evidence continuity without copying Package, Runtime, Gateway, provider, or
domain-owner truth.

The target architecture is owned by [architecture.md](./architecture.md).
Current implementation and evidence are owned by
[implementation-architecture.md](./implementation-architecture.md), source,
schemas, tests, runtime readback, and [status.md](./status.md). This file records
only the gaps between them.

The current MVP critical path is a thin Console, one real `local-docker`
Workspace path, and authoritative OPL Gateway accounting. Tencent/TKE belongs to
the medopl instance extension. Self-service onboarding/payment, refined visual
work, and broader managed-platform capabilities are intentionally later. This
vertical is the only `P0` lane. The typed Control Plane-to-Fabric launch binding
is now part of the implemented baseline rather than a second product lane.

The immediate portfolio also prioritizes internal module cohesion, physical
deployment isolation, legacy instance-boundary migration, and deletion-first
simplification.
These structural lanes proceed beside the P0 vertical rather than becoming a
repository-wide refactor gate. Candidate deletion work may start with read-only
caller, persisted-data, and external-consumer admission, but remains unauthorized
for mutation until its row's acceptance evidence is complete.

## Planning Semantics

State and priority answer different questions:

- `in_review`: an implementation attempt has a live pull request;
- `next`: accepted and ready to claim;
- `planned`: accepted but not in the immediate portfolio;
- `candidate`: requires caller, contract, data, and external-consumer admission
  before mutation;
- `later`: waits for an explicit product or evidence trigger;
- `external_owner`: proceeds in its owning repository.

`P0` is the current product or integration critical path, `P1` is high-leverage
work, `P2` requires an owner or boundary decision, and `P3` is trigger-driven.
Priority is not dependency order. Independent rows proceed concurrently; only
an overlapping write set, one shared contract revision, canonical `main`, or a
real production mutation is serialized.

## Product And Structural Gaps

| ID | State | Priority | Current gap | Owner boundary | Acceptance |
| --- | --- | --- | --- | --- | --- |
| `MVP-LOCAL-WORKSPACE-GATEWAY-01` | `next` | `P0` | Thin Console, one Control Plane Reconciler, typed Fabric stage bindings, a real `local-docker` adapter, Sub2API-backed balance/usage/Key/debit/refund paths, an explicit local-Workspace Compose profile, and a durable owner-authorized Workspace delete command exist. The canonical profile enables the Launch worker, mounts Docker authority only into Fabric, and requires an immutable Workspace image; a real smoke reached the host Docker Engine but stopped at the required external Sub2API authentication boundary. One complete live Console create/readback/open/delete path with authoritative Sub2API accounting remains unproven | Fabric provider port + Control Plane Launch coordination + Gateway/Sub2API authority + thin Console; Ledger records only required receipts and reconciliation | On a MacBook or single-server Docker host, Console creates, reads back, opens, and deletes one OPL App/WebUI Workspace through the single Control Plane Reconciler and `local-docker`; every resource stage uses the admitted Fabric binding; balance, usage, debit, and refund remain Sub2API-authoritative; Tencent/TKE is neither exercised nor required |
| `PRODUCT-RELEASE-01` | `next` | `P1` | GitHub has no OPL Cloud tag or Release. The product release workflow publishes the base Compose file, environment template, manifest, and multi-architecture image, but omits `compose.local-workspace.yaml`, which now exists in source; no clean host has installed an exact release and completed the product path | Reusable release workflow and immutable product artifacts in Cloud; concrete installation configuration remains instance-owned | One immutable `v0.x.y` release from exact Cloud `main` publishes the base Compose file, `compose.local-workspace.yaml`, environment template, manifest, and GHCR multi-architecture digest; manifest, tag, target SHA, asset set, and digest read back exactly; a clean Docker host using only those release assets plus its own Sub2API/Workspace configuration completes the admitted install path |
| `LEGACY-LAUNCH-MIGRATION-01` | `next` | `P1` | No admitted migration proves that only quiesced `manual_review` schema-2 Launches can join the single Reconciler without changing identity, money, resources, or attempt budgets | Control Plane migration/source and owner-authoritative read-only clients; Fabric/Sub2API remain zero-mutation fact owners | GET-only facts -> deterministic mapping -> exact-row/result CAS -> CP post-write readback preserves launch/account/Workspace/customer, debit/Key/provider/resource IDs, billing period, all idempotency identities, and consumed/unknown/remaining budgets; any gap stays `manual_review`; migrated rows still require immutable Resume authorization |
| `DEPLOY-ISOLATION-01` | `next` | `P1` | Portable Compose now defines service-specific PostgreSQL roles/databases and three target-scoped service tokens, but the post-split configuration has no real Compose readback and no concrete instance adoption evidence; the shared product image remains one intentional release unit | Reusable Compose/configuration in Cloud; concrete Secret refs, rollout, and production readback in each instance | A real portable installation proves each service starts only with its own database role and inbound identity, rejects cross-owner database access and caller impersonation, and retains independent token rotation; each instance separately proves adoption for one immutable Cloud release |
| `MODULE-COHESION-01` | `next` | `P1` | Control Plane Launch is now split into one provider-neutral Reconciler and focused stage files, Fabric `service.go` was reduced substantially, and Tencent compute ownership has one adapter-private core; Fabric still retains a large mixed facade, remaining Tencent validation/projection knowledge, and operation-list scans in operator extensions | One owning Go module per change | Focused capability files preserve public contracts, state behavior, and full tests without a shared domain/policy package; generic Fabric code has no provider-specific validation or identity inference, and active readback uses exact owner keys |
| `INSTANCE-PROVIDER-ACCEPTANCE-MIGRATION-01` | `next` | `P1` | Normal Launch and Resume use the provider-neutral typed Fabric contract, but the separate provider-acceptance route, legacy Fabric client DTOs, and persisted projections still interpret Tencent/TKE, SA5, CVM/CBS, NodePool, `providerData`, and `costTags` inside Control Plane | Reusable provider-neutral acceptance contract in Cloud + Tencent/TKE implementation/profile in Fabric adapter and `opl-instance-medopl` | Real callers move to a provider-neutral acceptance/readback contract; Tencent offer/profile constants and resource interpretation leave Control Plane; retained historical fields are read-only migration evidence or are explicitly migrated; normal Launch remains unchanged and no second Reconciler appears |
| `INSTANCE-MEDOPL-01` | `external_owner` | `P1` | The Tencent/TKE profile and production workflow source now live in `opl-instance-medopl`, but that repository has no GitHub Environment or Deployment, its profile remains `deployed_unverified`, and product SHA, release tag, image digest, and receipt are unset. Cloud still carries the legacy production Environments, Secrets/variables, and Deployment history | `opl-instance-medopl` owns successor production authority and exact deployment receipts; Cloud owns reusable releases and later retires only its legacy repository state | Instance `production` protection and Secret/variable owner refs are established; one exact immutable Cloud release is deployed through Instance `main`; workflow, Environment, Deployment, Runtime, rollback, and redacted repository receipt read back consistently; only then may Cloud legacy production authority be retired |
| `CLOUD-LEGACY-PRODUCTION-RETIRE-01` | `planned` | `P1` | Six Cloud Environments and 2,084 historical Deployment records remain after workflow ownership moved to Instance. The `production` environment accounts for 2,079 records and currently exposes 23 Secret names and 31 variables; the records include every Actions job that declared the environment rather than 2,079 server rollouts | Cloud GitHub settings and Deployment records, gated by the `opl-instance-medopl` successor receipt | After `INSTANCE-MEDOPL-01` proves the successor, remove medopl production Secrets/variables and obsolete production Environments from Cloud; deactivate and delete only legacy Deployment records proven unrelated to retained Pages/whitepaper or current product release evidence; fresh API readback shows no residual medopl deployment authority in Cloud |
| `REPOSITORY-BRANCH-CLOSEOUT-01` | `next` | `P1` | Excluding `main`, `gh-pages`, and current task branches, three historical remote branches each retain one commit not absorbed by `main`; none has a PR linking it to current ownership or disposition | Git history, PR provenance, current code owners, and remote refs | For each branch, prove the commit is absorbed, semantically superseded, or still required; absorb admitted content before deletion, obtain owner release for obsolete content, then delete local/tracking/wire refs with exact readback. Age alone never authorizes deletion |
| `CONSOLE-SELF-SERVICE-01` | `later` | `P3` | Accounts are operator-provisioned; registration, payment/order, and complete self-service are absent | Console + Control Plane product API and policy | A tenant can onboard, read authoritative wallet/usage, create 0..N Workspaces, and complete one approved payment/order path without acquiring wallet or provider authority |
| `BILLING-EVIDENCE-01` | `later` | `P3` | Full debit/refund/renewal/reconciliation production evidence is outside the accounting-first MVP | Control Plane settlement + Sub2API adapter + Ledger receipts | One immutable release proves exactly one Workspace-period debit, provider renewal/readback, Receipt, and failure recovery without a second wallet |
| `MANAGED-POLICY-01` | `later` | `P3` | Admission and fixed offers are not yet one reusable account/quota/resource policy | Control Plane account policy | Policy authorizes or denies a resource plan without owning package state, provider mutation, or Runtime state |
| `RESOURCE-BINDING-01` | `planned` | `P2` | Environments and connectors lack one portable plan/approve/execute/collect path | Fabric plus selected resource owner | One end-to-end resource path returns provider-neutral facts and a Ledger receipt without a second policy or wallet owner |
| `WORKSPACE-CONTINUITY-01` | `external_owner` | `P2` | App-to-online project/task/artifact continuation lacks owner evidence | App + Workspace owners | Owner contracts and live readback prove continuation without Cloud copying project or artifact truth |
| `PACKAGE-PROJECTION-01` | `external_owner` | `P2` | Cloud does not project exact Package publication and fresh carrier state end to end | Package, carrier, Framework, and one Cloud consumer | One Cloud surface consumes owner refs/readback without adding a Cloud registry, lock, or lifecycle writer |
| `CONNECTOR-01` | `external_owner` | `P2` | Stable generic connector access and domain-adapter evidence are absent | OPL Connect + domain owner + one Cloud consumer | One connector supplies normalized refs while the domain adapter retains semantics and credentials |
| `EVIDENCE-CONTINUATION-01` | `planned` | `P2` | Ledger APIs exist but user-visible action/artifact continuation is not closed | Ledger + one consuming owner | One user-visible action returns exact receipt, review, and continuation refs with authoritative readback |
| `WORKSPACE-ROUTER-01` | `later` | `P3` | Control Plane still proxies Workspace traffic | Router boundary after measured need | A separately owned router preserves Runtime auth/routing without moving entitlement or provider truth |
| `SERVE-01` | `later` | `P3` | Service, Revision, Agent Edge, Hosted UI, and Embed remain target architecture | Serve + Runway + package/domain owners | Exact package digest reaches an immutable Revision and authenticated API with receipts before UI/embed expansion |
| `RUNWAY-01` | `external_owner` | `P3` | Shared Invocation/Session execution lifecycle remains outside Cloud | OPL Runway + one provider adapter | Routing, streaming, cancellation, and readback work without moving Service identity or domain verdicts |

## Phased Contract Slimdown

Phase 1 removes subjective and low-risk locks. The Console visual freeze,
implementation-specific query/pagination/navigation fields, launch status
ledger, dated execution plans, frozen screenshots, and superseded machine
contracts are retired in the current documentation-normalization revision.
Evidence for that completed baseline belongs in [status.md](./status.md), not in
this roadmap.

The focused settlement, Control Plane Launch/Recovery, Fabric binding, and Ledger
evidence contracts now replace the retired aggregate launch freeze. Remaining
open phases are:

| ID | State | Priority | Phase and scope | Safety retained | Acceptance |
| --- | --- | --- | --- | --- | --- |
| `CONTRACT-DEDUP-02` | `planned` | `P1` | Phase 2: assign one owner to repeated facts across current machine contracts | public APIs, security, integrity, permissions, irreversible side effects | Other contracts reference the owner or schema; no duplicated mutable implementation/status truth remains |
| `DEPLOY-CONTRACT-03` | `planned` | `P1` | Phase 3: migrate deployment contract one workflow family at a time | production authorization, runner/identity binding, Secrets, immutable images, mutation bounds, readback, diagnostics, rollback | Focused workflow tests own executable shape; the aggregate deployment migration contract is deleted only after all families cut over |

## Simplification Backlog

These rows are deletion-first candidates, not deletion authorization. A
`candidate` first proves real callers, target obligations, persisted-data
handling, external consumers, and rollback.

A `next` simplification row has completed read-only admission only for the
stated application-code cut; retained database rows, historical migrations,
provider resources, and cleanup obligations remain outside that authorization.
An `external_owner` row requires owner-authoritative readback before Cloud may
resume the deletion.

| ID | State | Priority | Candidate | Risk | Admission or acceptance |
| --- | --- | --- | --- | --- | --- |
| `SIMPLIFY-RECOVERY-CLI-01` | `external_owner` | `P1` | Active medopl recovery workflows still execute the Cloud CLI from a pinned product revision | medium | The Instance owner retires or cuts over all three recovery modes and proves the five workflow references absent; Cloud then deletes the CLI and its self-tests |
| `SIMPLIFY-FABRIC-SNAPSHOT-01` | `external_owner` | `P1` | No product caller remains, but Fabric operations and Control Plane backup rows may still bind real `VolumeSnapshot` and restored PVC resources | high | The Instance owner correlates and drains backup rows, Fabric operations, `VolumeSnapshot` objects, and restored PVCs to confirmed-zero before Cloud deletes snapshot/restore creation, synchronization, recovery, and destroy code |
| `SIMPLIFY-FABRIC-READBACK-SCAN-01` | `planned` | `P2` | Operator identity-evidence and pool-head replay still scan the operation table and match resource/idempotency/account/Workspace alternatives even though typed Launch uses exact point reads | medium | Replace active inference scans with existing exact operation identities or owner-backed unique keys, preserve required historical read compatibility, and delete the alternative-match branches after focused PostgreSQL and provider tests pass |
| `SIMPLIFY-CONSOLE-CSS-01` | `later` | `P3` | Public, login, authenticated, and legacy Console surfaces retain overlapping style layers | medium | Preserve current desktop/mobile behavior and Workspace interaction states while consolidating styles only when it pays for a real Console change |
| `SIMPLIFY-LEDGER-VERTICAL-01` | `candidate` | `P2` | Artifact/review/policy/continuation surfaces beyond current receipt consumers | high | Resolve `EVIDENCE-CONTINUATION-01` and external callers before shrinking |
| `SIMPLIFY-CP-IDENTITY-01` | `candidate` | `P2` | Organization/Membership compatibility storage | high | Decide the self-service identity model and migrate persisted identity explicitly |
| `SIMPLIFY-ACTIONS-REUSE-01` | `planned` | `P2` | Repeated workflow checkout/setup/cleanup mechanics | medium | Consolidate only stable repetition without rebuilding a monolithic dispatcher |
| `SIMPLIFY-CP-FACADE-01` | `planned` | `P2` | Large Control Plane service facade with pass-through methods | medium | Migrate real callers to owning capabilities before removing forwarding methods |
| `SIMPLIFY-CLI-ARGS-01` | `planned` | `P2` | Repeated handwritten CLI argument parsers | low | Preserve accepted flags and errors using the native parser in each owning tool |
| `SIMPLIFY-STATIC-ASSETS-01` | `later` | `P3` | Custom static lookup, SPA fallback, and request-time gzip | medium | Select one compression/edge owner and preserve cache, range, content type, and SPA behavior |

## Evidence Gaps

The immediate evidence gaps are one complete local Console-to-Workspace path,
one authoritative Gateway accounting flow on that same path, one installable
immutable product Release containing the local-Workspace profile, and one
Instance-owned medopl deployment receipt. Self-service onboarding/payment, refined
Console presentation, managed resources, connector execution,
App-to-Workspace continuation, public-edge isolation, full deployment
health/rollback, exact monthly settlement and quota readback, user-visible
continuation, production soak, and owner acceptance remain later or external.

Docs, contracts, tests, screenshots, pull requests, and rendered artifacts close
only their own layer. Runtime and production gaps close only from the named
owner and exact immutable revision.

## Integration And Production Gates

1. A changed cross-module boundary has one owner, one compatible contract
   revision, and focused tests on both sides.
2. A single-Reconciler candidate keeps resource stage implementation, Fabric
   operation derivation, provider facts, and mutations inside Fabric; tests do
   not admit cross-owner code into Control Plane.
3. Each implementation replays on fresh canonical `main`; overlapping branches
   reconcile semantically rather than preserving duplicate current truths.
4. Production and instance qualification bind one exact immutable release and
   preserve authorization, Secret, mutation, readback, and rollback boundaries.
5. A failed runtime or production evidence lane stays open without rolling
   unrelated local development backward.
6. Instance production authority is established and read back before Cloud's
   legacy medopl Secrets, Environments, Deployment records, or rollback evidence
   are removed.

## Explicit Non-Goals

- no second Cloud product, implementation repository, wallet, Gateway, package
  registry, lock, Invocation/Session store, or domain-verdict owner;
- no fixed product-level Workspace count or implicit shared Workspace;
- no public Agent endpoint inferred from a Workspace URL or provider session;
- no compatibility layer after current callers and persisted state have moved;
- no readiness claim from planning, contract presence, fake tests, screenshots,
  or document completeness.
