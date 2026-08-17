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

The immediate portfolio also prioritizes internal module cohesion, Instance
adoption of the implemented deployment isolation, legacy instance-boundary
migration, and deletion-first simplification.
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
- `completed`: the admitted application-code cut is implemented and verified;
  retained historical data or external obligations are stated separately;
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
| `MVP-LOCAL-WORKSPACE-GATEWAY-01` | `next` | `P0` | Thin Console, one Control Plane Reconciler, typed Fabric stage bindings, a real `local-docker` adapter, Sub2API-backed balance/usage/Key/debit/refund paths, an explicit local-Workspace Compose profile, and a durable owner-authorized Workspace delete command exist. Required CI closes the Accounting source/evidence gap with real Control Plane HTTP plus PostgreSQL, a real Ledger HTTP process plus separate PostgreSQL, and a typed Sub2API authority fixture; it proves exactly-once stable debit, replay safety, one linked receipt, and fail-closed response-loss handling. The fixture is not a real external Sub2API, and the existing live smoke stopped at its authentication boundary. One complete live MacBook or single-server Console create/readback/open/delete path plus real external Sub2API authentication, balance, and usage readback remains unproven | Fabric provider port + Control Plane Launch coordination + Gateway/Sub2API authority + thin Console; Ledger records only required receipts and reconciliation | On a MacBook or single-server Docker host, Console creates, reads back, opens, and deletes one OPL App/WebUI Workspace through the single Control Plane Reconciler and `local-docker`; every resource stage uses the admitted Fabric binding; real external Sub2API authentication, balance, usage, debit, and refund remain authoritative and read back consistently; Tencent/TKE is neither exercised nor required |
| `PRODUCT-RELEASE-01` | `next` | `P1` | Only `v0.1.7` remains public: hosted run `31879240411` published five verified assets and a `linux/amd64` + `linux/arm64` GHCR index from product SHA `a59bde68397528186a5220f73195fa1f3eda311b`; the owner removed historical `v0.1.0`-`v0.1.6` Releases, tags, and GHCR objects. PR `#334` restricts the manual Release workflow to the repository owner, but the workflow still builds and publishes in one dispatch. There is no deployable non-Release candidate channel and no proof that formal publication promotes the exact image bytes already qualified by `opl-instance-medopl` | Cloud owns candidate/release mechanics and portable artifacts; `opl-instance-medopl` owns protected deployment, rollback, product acceptance, and the exact candidate receipt; only the repository owner admits publication | An exact canonical Cloud SHA produces a replaceable digest-addressed candidate without a Git tag, GitHub Release, or versioned GHCR tag; `opl-instance-medopl` deploys it and returns successful rollout and product-acceptance readback; the owner then manually publishes the same SHA and image digest without rebuilding, and failed development or deployment attempts create no formal version. No successor to `v0.1.7` is published before this path passes |
| `LEGACY-LAUNCH-MIGRATION-01` | `candidate` | `P3` | PR `#280`'s runtime candidate is not admitted. Fresh-main replay found a Linux-only test harness, retired Fabric readback interfaces, and a boundary mismatch where Fabric's `ready / legacy_partial_history` response omitted the explicit next-stage `absent` fact required by Control Plane. More importantly, no protected Instance inventory, workflow, or receipt proves that an eligible schema-2 `manual_review` row or active consumer exists, so a temporary migration API and state path have no current payer | Control Plane would own eligibility, exact-row CAS, and Resume; Fabric would own GET-only binding/provider facts; `opl-instance-medopl` owns protected inventory and production authorization; Sub2API remains a zero-mutation fact owner during admission | Trigger only after a protected Instance GET-only inventory proves at least one active candidate and binds it to exact persisted preflight/history, unique operations, canonical identities, provider resources, money, and remaining budgets. Then implement the smallest fresh-main path with cross-boundary partial/full-history tests, exact CAS readback, and immutable Resume authorization. If inventory is zero or no consumer remains, close the gap without runtime migration code; inventory performs no Fabric/provider or Sub2API mutation |
| `REMOTE-COMPANION-BROKER-01` | `candidate` | `P2` | The Cloud Control Plane candidate implements the remote companion broker contract, pairing persistence, atomic seat reservation, provider UserSig issuance, and retryable partial revoke readback. Real Tencent IM application configuration, provider-limit recheck, TestFlight/App Store qualification, and China three-network acceptance remain unproven | `services/control-plane` owns broker state and public HTTP; `opl-link` owns the native client; Tencent IM owns realtime transport; the instance owner owns real provider Secrets and release qualification | Re-run the broker source and contract gates from a fresh canonical Cloud SHA, configure a real Tencent IM application through the instance owner, prove provider account import/absence and UserSig readback without exposing the Secret, then qualify an exact TestFlight candidate and three-network foreground/reconnect behavior before changing the product readiness claim |
| `MODULE-COHESION-01` | `next` | `P1` | Control Plane Launch is one provider-neutral Reconciler with focused stage files, and retained Ent persistence is now separated into identity, resource, and Workspace capability files. Fabric `service.go` was reduced substantially; Tencent compute ownership and compute-allocation identity validation have adapter-private owners; and the retained Tencent provider is separated into compute, storage, and Runtime capability files. These slices preserve the existing receivers, interfaces, HTTP contracts, schemas, provider operations, and behavior under the complete local PostgreSQL/capacity/local-Docker gate. Shared persistence helpers, the remaining Tencent facade capabilities, and other provider/operator extensions still require caller-led cohesion work; no Spring Modulith, Cordis runtime, cross-service domain package, second registry, or global event bus is introduced | One owning Go module per change | Each remaining slice names a real caller and existing owner, reduces a measured mixed facade or duplicate responsibility, preserves public contracts and state behavior under focused plus complete local tests, and adds no shared policy/runtime framework without evidence of a missing capability |
| `INSTANCE-PROVIDER-ACCEPTANCE-MIGRATION-01` | `external_owner` | `P1` | Cloud Provider Facts delegates compute/storage/attachment/Runtime interpretation to Fabric adapters. Control Plane now consumes only provider-neutral monthly-preflight availability plus its own package, size, and zone facts; Instance-owned acceptance tools use canonical compute/storage provider IDs, while optional legacy `nodePoolId` / `persistentVolumeId` projections do not decide readiness or resource continuity. The explicit `OPL_TENCENT_ZONE` configuration remains an Instance/Fabric profile choice. The source cutover is complete; exact candidate deployment and provider readback remain external | `opl-instance-medopl` owns caller execution and protected production acceptance; Cloud owns the reusable provider-neutral contract, runtime, and adapter primitives; Tencent implementation/profile remains in the Fabric adapter and Instance owner | Instance `main` executes the absorbed Cloud contract from the protected workflow and reads back the same provider IDs and refs for an exact candidate; optional legacy projections remain diagnostic-only or are explicitly retired; normal Launch remains unchanged and no second Reconciler appears |
| `INSTANCE-MEDOPL-01` | `external_owner` | `P1` | The Tencent/TKE profile, protected production workflows, instance-specific tools/tests, and first deployment receipt now live in `opl-instance-medopl` `main`. The receipt proves a successful first TKE rollout and public health readback for `v0.1.7`, but the tracked profile remains `deployed_unverified`, runtime readiness is `ready=false`, and Acceptance B is incomplete. Cloud retains reusable candidates/releases and only its current release, Pages, and whitepaper GitHub deployment surfaces | `opl-instance-medopl` owns production authority, all medopl-specific deployment/acceptance/recovery/rollback tooling, and exact candidate receipts; Cloud owns reusable runtime, contracts/adapters, candidates/releases, and any later product fix | Instance production protection and Secret/variable owner refs remain established; the exact candidate's workflow, Environment, Deployment, Runtime, isolation, rollback, product acceptance, and redacted receipt read back consistently before a formal Cloud Release; no instance-specific tool source, GitHub environment, Deployment record, or accepted caller remains in Cloud |
| `CONSOLE-SELF-SERVICE-01` | `later` | `P3` | Accounts are operator-provisioned; registration, payment/order, and complete self-service are absent | Console + Control Plane product API and policy | A tenant can onboard, read authoritative wallet/usage, create 0..N Workspaces, and complete one approved payment/order path without acquiring wallet or provider authority |
| `BILLING-EVIDENCE-01` | `later` | `P3` | Full debit/refund/renewal/reconciliation production evidence is outside the accounting-first MVP | Control Plane settlement + Sub2API adapter + Ledger receipts | One immutable release proves exactly one Workspace-period debit, provider renewal/readback, Receipt, and failure recovery without a second wallet |
| `MANAGED-POLICY-01` | `later` | `P3` | Admission and fixed offers are not yet one reusable account/quota/resource policy | Control Plane account policy | Policy authorizes or denies a resource plan without owning package state, provider mutation, or Runtime state |
| `RESOURCE-BINDING-01` | `planned` | `P2` | Environments and connectors lack one portable plan/approve/execute/collect path | Fabric plus selected resource owner | One end-to-end resource path returns provider-neutral facts and a Ledger receipt without a second policy or wallet owner |
| `WORKSPACE-CONTINUITY-01` | `external_owner` | `P2` | App-to-online project/task/artifact continuation lacks owner evidence | App + Workspace owners | Owner contracts and live readback prove continuation without Cloud copying project or artifact truth |
| `PACKAGE-PROJECTION-01` | `external_owner` | `P2` | Cloud does not project exact Package publication and fresh carrier state end to end | Package, carrier, Framework, and one Cloud consumer | One Cloud surface consumes owner refs/readback without adding a Cloud registry, lock, or lifecycle writer |
| `CONNECTOR-01` | `external_owner` | `P2` | Stable generic connector access and domain-adapter evidence are absent | OPL Connect + domain owner + one Cloud consumer | One connector supplies normalized refs while the domain adapter retains semantics and credentials |
| `EVIDENCE-CONTINUATION-01` | `external_owner` | `P2` | User-visible action, artifact, review, and continuation semantics are not a Ledger-owned vertical; Cloud only records caller-owned opaque provenance | App/Workspace or the relevant domain owner, with Ledger receipt readback | One consuming owner returns and reads back its own action/artifact/review/continuation truth while Cloud Ledger remains a receipt and opaque-provenance recorder |
| `WORKSPACE-ROUTER-01` | `later` | `P3` | Control Plane still proxies Workspace traffic | Router boundary after measured need | A separately owned router preserves Runtime auth/routing without moving entitlement or provider truth |
| `SERVE-01` | `later` | `P3` | Service, Revision, Agent Edge, Hosted UI, and Embed remain target architecture | Serve + Runway + package/domain owners | Exact package digest reaches an immutable Revision and authenticated API with receipts before UI/embed expansion |
| `RUNWAY-01` | `external_owner` | `P3` | Shared Invocation/Session execution lifecycle remains outside Cloud | OPL Runway + one provider adapter | Routing, streaming, cancellation, and readback work without moving Service identity or domain verdicts |

## Security Hardening Portfolio

This table is the sole active plan for repository security findings. `immediate`
means a low-risk control or triage step that can proceed now; `planned` means an
accepted implementation phase with an explicit owner boundary; `candidate`
requires claim-specific admission before source mutation; and `external_owner`
requires GitHub or an Instance owner to supply the missing authority or evidence.
Scanner output remains evidence until triaged, implemented, and revalidated.
S1, S2, S3, and `SECURITY-CODEQL-TRIAGE-01` are no longer active plan rows: their
absorbed source or terminal classification evidence belongs in
[status.md](./status.md). Fifteen CodeQL alerts remain open (`#1`-`#11` and
`#14`-`#17`); GitHub reports `#12` and `#13` as fixed. No dismissal or settings
mutation was authorized or performed, and the open disposition is not an
admitted fix lane.

| ID | Class | Priority | Current gap | Owner boundary | Acceptance |
| --- | --- | --- | --- | --- | --- |
| `SECURITY-GITHUB-AGENT-PILOT` | `candidate` | `P3` | GitHub `Agents` is available as a Copilot cloud-agent task/PR surface, but this repository already has a canonical Codex lifecycle and no demonstrated need for a second autonomous writer or scheduled automation | Development workflow only; no release, deployment, Secrets, production, product-Agent, or domain-agent authority | Adopt only after a narrow documentation/test-maintenance pilot proves unique ownership, PR-only output, least tools, branch-protection compliance, bounded cost, and no duplicate lifecycle; otherwise retain no repository agent profile or automation |
| `SECURITY-SECRET-VALIDITY` | `external_owner` | `P2` | Secret scanning and push protection are enabled, but the repository setting continues to report `secret_scanning_validity_checks=disabled` after a write attempt | GitHub feature/plan availability and repository owner settings | GitHub reports validity checks enabled, or owner-authoritative documentation confirms the feature is unavailable for this repository; until then no completion claim is made |
| `SECURITY-SCAN-REMEDIATION-04` | `in_review` | `P1` | PR `#309` absorbed the preceding five-finding remediation at canonical `d8a4df0f130a1545da0efe43dfebe16fa08e5844`; the ten unresolved UI occurrences from older revision `24a065d4427b53d65ba0df9cb70b1a36327fb6af` do not reproduce there. Sealed Standard scan `761fd61d-b7ee-41ff-afd2-34f5671b1af5` reported one current low-severity Fabric resource-exhaustion finding: authenticated heartbeat, Runtime status, and operation-list requests use unbounded shared history, and fresh heartbeat keys grow it. The FG-184 candidate replaces request-path full-list scans with indexed bounded lookups, coalesces heartbeat state per job attempt, paginates the operation endpoint at a fixed maximum of 100, and migrates all known production/recovery callers; it is not yet absorbed or re-scanned | Fabric owns operation persistence and HTTP pagination; Control Plane/runner transport and lease authority remain unchanged; production callers consume the bounded Fabric contract without adding a second history owner | Fresh canonical `main` readback plus a sealed scan no longer reports `resource-exhaustion.fabric-operation-history`; focused memory and PostgreSQL store tests prove point lookup, duplicate fail-closed behavior, bounded heartbeat cardinality, Runtime identity bounds, and cursor pagination; caller tests prove complete multi-page readback and repeated-cursor rejection. Platform, Instance adoption, and production claims remain separately owner-authoritative |

## Phased Contract Slimdown

Phase 1 removes subjective and low-risk locks. The Console visual freeze,
implementation-specific query/pagination/navigation fields, launch status
ledger, dated execution plans, frozen screenshots, and superseded machine
contracts are retired in the current documentation-normalization revision.
Evidence for that completed baseline belongs in [status.md](./status.md), not in
this roadmap.

The focused settlement, Control Plane Launch/Recovery, Fabric binding, Ledger
evidence, portable distribution, and Instance deployment owners now replace the
retired aggregate launch and deployment guards. The deployment migration phase
is complete; the remaining open contract phase is:

| ID | State | Priority | Phase and scope | Safety retained | Acceptance |
| --- | --- | --- | --- | --- | --- |
| `CONTRACT-DEDUP-02` | `next` | `P1` | Phase 2 is underway: Billing references Ledger-owned `reconciliationReportV1` and `workspaceMonthlyBillingReceiptV1`; other repeated fact families remain scoped one owner at a time | public APIs, security, integrity, permissions, irreversible side effects | Each remaining consumer references the owning contract or schema; no duplicated mutable implementation/status truth remains |

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
| `SIMPLIFY-FABRIC-SNAPSHOT-01` | `completed` | `P1` | The caller-zero Snapshot/Restore HTTP, service, provider, operation-replay, and focused test surface is retired after the Instance owner proved zero backup rows, Fabric operation rows, `VolumeSnapshot` objects, `VolumeSnapshotContent` objects, and restored PVCs. Historical migrations and rows remain retained. | Cloud owns the reusable Fabric surface; `opl-instance-medopl` owns production inventory and recovery authority | Focused Fabric and full local gates pass; the protected Instance inventory is zero and no current Cloud caller or provider implementation remains. |
| `SIMPLIFY-LEDGER-VERTICAL-01` | `completed` | `P2` | The caller-zero Control Plane evidence client adapter and structured Artifact, Review, ReviewPolicy, ReviewGate, and Continuation Ledger vertical are retired. Receipt provenance fields remain opaque and caller-owned; historical `review_policies` rows and Receipt provenance columns remain retained. | Ledger owns receipts, reconciliation, idempotency, retention, and opaque provenance; calling domains own artifact/review/continuation semantics and authorization | Focused Ledger, Control Plane client, contract, and route tests pass; current source has no structured Artifact/Review/Policy/Gate/Continuation writer or route, while historical SQL/migrations remain unchanged. |
| `SIMPLIFY-CP-IDENTITY-01` | `completed` | `P2` | Organization/Membership application models, runtime store APIs, reconciliation, and provisioning writes are retired. Account/User is the sole runtime identity and authorization owner; raw legacy tables and IDs remain under additive custody migration. | Control Plane identity owner; Account/User is the runtime authority and legacy tables are migration custody only | Account/session, identity, and full PostgreSQL gates pass; no runtime Organization/Membership caller remains and the custody migration preserves historical rows without restoring the old writer. |
| `SIMPLIFY-ACTIONS-REUSE-01` | `next` | `P2` | Qualification now reuses stable checkout, Node setup, PostgreSQL service, and Go-test pipeline YAML while preserving the four job identities and zero-skip gates; other workflow repetition remains separately scoped | medium | Consolidate only stable repetition with a semantic workflow test; do not rebuild a monolithic dispatcher |
| `SIMPLIFY-CP-FACADE-01` | `planned` | `P2` | The zero-caller `ReapplyWorkspaceRuntime` forwarding method, finite zero-caller `server/app_state` helpers, and the closed dead `PrepareWorkspace` orchestration chain are removed without changing historical rows or Fabric resources. `CreateWorkspaceInput` remains for its real Provider Acceptance caller. The real `Service` facade and capability boundaries remain; no broader caller-zero admission exists | medium | Remove only separately proven-zero forwarding or dead helpers, migrate real callers to owning capabilities, and preserve the real `Service` and capability boundaries without introducing an aggregate replacement facade |
| `SIMPLIFY-CLI-ARGS-01` | `later` | `P3` | Three tools use tool-local `node:util.parseArgs`; a further focused conversion expanded rather than simplified the retained surface, so remaining parsers stay local | low | Reconsider only when a real tool change removes more bespoke parsing than the explicit native option schema and compatibility tests add; do not add a shared CLI framework |
| `SIMPLIFY-STATIC-ASSETS-01` | `later` | `P3` | Native file delivery alone does not remove the custom request-time gzip branch, so the current static behavior remains | medium | Select one compression/build/edge owner and preserve cache, range, content type, and SPA behavior before deleting the custom branch |
## Evidence Gaps

Accounting source and required-CI evidence are closed by the real Control Plane
HTTP/PostgreSQL and Ledger HTTP/separate-PostgreSQL path with a typed Sub2API
authority fixture. The immediate evidence gaps are one complete live MacBook or
single-server Console create/readback/open/delete path, real external Sub2API
authentication plus balance and usage readback on that path, one installable
immutable product Release containing the local-Workspace profile, and one
Instance-owned medopl deployment receipt. Self-service onboarding/payment,
refined Console presentation, managed resources, connector execution,
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
4. Production and Instance qualification bind one exact candidate SHA and image
   digest; formal publication promotes those same bytes while preserving
   authorization, Secret, mutation, readback, and rollback boundaries.
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
