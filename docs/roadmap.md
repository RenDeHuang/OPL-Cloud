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
vertical is the only `P0` lane. `LAUNCH-FABRIC-BINDING-01` below is a required
physical-boundary slice of that same vertical, not a second product lane.

The immediate portfolio also prioritizes internal module cohesion, physical
deployment isolation, contract-owner slimdown, and deletion-first simplification.
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
| `MVP-LOCAL-WORKSPACE-GATEWAY-01` | `next` | `P0` | Thin Console and Sub2API-backed balance, usage, Key, debit, and refund paths exist, but no real `local-docker` adapter closes Workspace create, readback, access, delete, and accounting as one path | Fabric provider port + Control Plane Launch coordination + Gateway/Sub2API authority + thin Console; Ledger records only required receipts and reconciliation | On a MacBook or single-server Docker host, Console creates, reads back, opens, and deletes one OPL App/WebUI Workspace through the single Control Plane Reconciler and `local-docker`; every resource stage uses the admitted Fabric binding; balance, usage, debit, and refund remain Sub2API-authoritative; Tencent/TKE is neither exercised nor required |
| `LAUNCH-FABRIC-BINDING-01` | `next` | `P0` | Required slice of the same local Workspace + Gateway MVP vertical: no admitted public contract yet proves an explicit Fabric-owned launch/stage operation binding; the current Control Plane implementation carries provider facts and can infer resource identity from operation keys, listings, and tags | Control Plane business operation/cursor + typed Fabric HTTP contract + Fabric operation store/provider adapter; no cross-import or shared domain package | A real caller sends the minimal immutable provider-neutral binding; Fabric persists it before write and returns it with authoritative readback; focused tests cover request hash, expected binding, idempotency and conflict/unknown handling; CP contains no provider fields, SDK/Kubernetes knowledge, resource reducer, or Fabric operation derivation |
| `LEGACY-LAUNCH-MIGRATION-01` | `next` | `P1` | No admitted migration proves that only quiesced `manual_review` schema-2 Launches can join the single Reconciler without changing identity, money, resources, or attempt budgets | Control Plane migration/source and owner-authoritative read-only clients; Fabric/Sub2API remain zero-mutation fact owners | GET-only facts -> deterministic mapping -> exact-row/result CAS -> CP post-write readback preserves launch/account/Workspace/customer, debit/Key/provider/resource IDs, billing period, all idempotency identities, and consumed/unknown/remaining budgets; any gap stays `manual_review`; migrated rows still require immutable Resume authorization |
| `DEPLOY-ISOLATION-01` | `next` | `P1` | Services share a database credential, internal token, and ConfigMap | Reusable deployment plus service startup configuration | Service-specific database roles/URLs and identities prevent cross-owner table writes and caller impersonation |
| `MODULE-COHESION-01` | `next` | `P1` | Large service files concentrate unrelated capabilities and create change collisions; a single-Reconciler change can worsen this by moving Fabric concerns into Control Plane | One owning Go module per change | Focused capability files preserve package API, state behavior, and full tests without shared policy modules; single-Reconciler candidates are rejected until provider facts/reducers/mutations and Fabric operation derivation live behind the typed Fabric public contract |
| `INSTANCE-MEDOPL-01` | `external_owner` | `P2` | Tencent/TKE profile, provider-specific workflows, and receipts are not fully separated from reusable Cloud | `opl-instance-medopl` | Exact Cloud release refs, provider/Secret owners, values, and deployment receipts are current with no Tencent/TKE prerequisite left in reusable Cloud MVP acceptance |
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

Open phases are:

| ID | State | Priority | Phase and scope | Safety retained | Acceptance |
| --- | --- | --- | --- | --- | --- |
| `CONTRACT-OWNER-02` | `next` | `P1` | Phase 2: split `opl-cloud-launch-freeze-contract.json` by settlement, Control Plane launch/recovery, Fabric provider/resource binding, and Ledger receipt/evidence owner | debit/refund cardinality, idempotency and CAS, immutable Resume authorization, Fabric binding/readback, PREPAID resource protection, append-only evidence | Every retained fact has one owner and real caller/test; Control Plane settlement coordination references Sub2API authority, CP launch/recovery covers the single Reconciler and bounded legacy migration, Fabric owns the stage binding and provider/resource facts, Ledger owns receipt/evidence/continuation refs without continuation authority; the aggregate contract/test are then deleted |
| `CONTRACT-DEDUP-02` | `planned` | `P1` | Phase 2: assign one owner to repeated facts across current machine contracts | public APIs, security, integrity, permissions, irreversible side effects | Other contracts reference the owner or schema; no duplicated mutable implementation/status truth remains |
| `DEPLOY-CONTRACT-03` | `planned` | `P1` | Phase 3: migrate deployment contract one workflow family at a time | production authorization, runner/identity binding, Secrets, immutable images, mutation bounds, readback, diagnostics, rollback | Focused workflow tests own executable shape; the aggregate deployment migration contract is deleted only after all families cut over |

## Simplification Backlog

These rows are deletion-first candidates, not deletion authorization. A
`candidate` first proves real callers, target obligations, persisted-data
handling, external consumers, and rollback.

| ID | State | Priority | Candidate | Risk | Admission or acceptance |
| --- | --- | --- | --- | --- | --- |
| `SIMPLIFY-RECOVERY-CLI-01` | `next` | `P1` | Retire the unreachable legacy Recovery CLI path | medium | Prove workflows use accepted entrypoints, then delete the bypassed parser/execution/artifact branch |
| `SIMPLIFY-CP-ARCHIVE-01` | `candidate` | `P1` | Disabled archive/retention worker, routes, and schemas | high | Resolve retention duties, external callers, and persisted records before coherent removal |
| `SIMPLIFY-CP-EXEC-01` | `candidate` | `P1` | Superseded shared-execution persistence models | medium | Prove database and external-caller disposition, then delete one bounded persisted-model batch |
| `SIMPLIFY-FABRIC-TRANSFER-01` | `candidate` | `P1` | Content transfer vertical with no current in-repo product caller | medium | Inventory external Fabric callers and retained data before route/store/schema removal |
| `SIMPLIFY-FABRIC-SNAPSHOT-01` | `candidate` | `P1` | Snapshot/restore vertical outside the Pilot | medium | Prove no external consumer and preserve provider cleanup/readback obligations |
| `SIMPLIFY-CONSOLE-CSS-01` | `later` | `P3` | Public, login, authenticated, and legacy Console surfaces retain overlapping style layers | medium | Preserve current desktop/mobile behavior and Workspace interaction states while consolidating styles only when it pays for a real Console change |
| `SIMPLIFY-LEDGER-VERTICAL-01` | `candidate` | `P2` | Artifact/review/policy/continuation surfaces beyond current receipt consumers | high | Resolve `EVIDENCE-CONTINUATION-01` and external callers before shrinking |
| `SIMPLIFY-CP-IDENTITY-01` | `candidate` | `P2` | Organization/Membership compatibility storage | high | Decide the self-service identity model and migrate persisted identity explicitly |
| `SIMPLIFY-ACTIONS-REUSE-01` | `planned` | `P2` | Repeated workflow checkout/setup/cleanup mechanics | medium | Consolidate only stable repetition without rebuilding a monolithic dispatcher |
| `SIMPLIFY-CP-FACADE-01` | `planned` | `P2` | Large Control Plane service facade with pass-through methods | medium | Migrate real callers to owning capabilities before removing forwarding methods |
| `SIMPLIFY-CLI-ARGS-01` | `planned` | `P2` | Repeated handwritten CLI argument parsers | low | Preserve accepted flags and errors using the native parser in each owning tool |
| `SIMPLIFY-STATIC-ASSETS-01` | `later` | `P3` | Custom static lookup, SPA fallback, and request-time gzip | medium | Select one compression/edge owner and preserve cache, range, content type, and SPA behavior |

## Evidence Gaps

The immediate evidence gaps are one real local provider Workspace path and one
authoritative Gateway accounting flow. Self-service onboarding/payment, refined
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

## Explicit Non-Goals

- no second Cloud product, implementation repository, wallet, Gateway, package
  registry, lock, Invocation/Session store, or domain-verdict owner;
- no fixed product-level Workspace count or implicit shared Workspace;
- no public Agent endpoint inferred from a Workspace URL or provider session;
- no compatibility layer after current callers and persisted state have moved;
- no readiness claim from planning, contract presence, fake tests, screenshots,
  or document completeness.
