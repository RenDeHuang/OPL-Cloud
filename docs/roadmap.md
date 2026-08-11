# OPL Cloud Roadmap And Current Gaps

Owner: `one-person-lab-cloud`
Purpose: `single_active_truth_plan`
State: `active_planning`
Machine boundary: This document owns only the current human-readable Cloud
planning summary, open product/structure gaps, evidence boundary, and next Agent
prompt. It does not prove service implementation, runtime health, billing,
security, release, owner acceptance, or production readiness.

## Target State

OPL Cloud extends OPL work from a local App into zero or more independent online
Workspaces per account, approved AI/resource use, optional exact Agent Service
publication, and inspectable evidence continuity. Cloud products consume Package-owner
identity/publication refs, native-carrier readback, Framework aggregation and
execution refs, and domain-owner judgments without creating competing truth.

This repository is the OPL Cloud product and implementation owner. Target truth
belongs to the product docs; current implementation truth belongs to the named
service source, machine contract, tests and runtime readback. Gateway, App,
Framework, Runway, package and domain truth remains with their corresponding
external owners.

## Current Status Summary

| Theme | Current state | Boundary |
| --- | --- | --- |
| Repository role | `product_and_implementation_owner` | Product docs and reusable implementation share one repository but retain distinct evidence levels |
| Product split | `documented` | Gateway, Workspace, Serve, Console, Fabric, and Ledger have one target responsibility owner each |
| Framework boundary | `documented` | OPL Packages aggregates owner descriptors and native carrier state; OPL Runway owns invocation/session execution; Cloud consumes refs |
| Domain boundary | `documented` | Domain Agents retain professional truth, quality, artifact, and delivery authority |
| Workspace identity | `decided` | One account may own 0..N independent Workspaces; Services remain separate deployment resources |
| Implementation owner | `unified` | `one-person-lab-cloud` owns Console, Control Plane, Fabric, and Ledger implementation; `opl-cloud` is an internal artifact/service identifier |
| Instance owner | `initialized_extraction_pending` | `opl-instance-medopl` exists as the first commercial instance owner; its stale implementation-repository identity, co-located Cloud values and missing owner receipts still require migration |
| Active documentation | `consolidated` | This file owns current gaps/next prompt; public product map stays in the root README and the technical split stays in architecture |
| Development governance | `strict_ci_with_independent_developers` | Both active developers may merge PRs after current `validate` and resolved conversations; reviews are requested deliberately rather than for every PR, module source boundaries are machine-checked, and production environments accept protected branches only |
| Source module boundaries | `enforced_in_ci` | Console network ownership, Go cross-service imports and Fabric-only cloud SDK ownership are checked by `tests/contracts/module-physical-boundaries.test.ts` through `npm test` |
| Whitepaper | `source_and_build_profile_present` | Source/build evidence does not prove publication or Cloud service readiness |
| Service delivery | `candidate_not_production_proven` | Current state is owned by `docs/status.md`, machine contracts, CI, deployment readback and owner evidence |

## Functional And Structural Gaps

States are `in_review`, `next`, `planned`, `later`, or `external_owner`. A
developer selects one row, stays inside its owner/write set, and closes only the
listed acceptance boundary. Open pull requests remain the live execution view;
this table owns why the work exists and what completion means.

As of 2026-08-11, the only human-authored feature change in review is
[PR #218](https://github.com/gaofeng21cn/one-person-lab-cloud/pull/218), a
Console visual refresh limited to four UI/style files. It does not implement
self-service onboarding, payment, provider portability or production evidence.
Open Dependabot pull requests are maintenance candidates and do not count as
product capability in progress.

| ID | State | Gap and current fact | Owner / write set | Close condition |
| --- | --- | --- | --- | --- |
| `CONSOLE-UI-01` | `in_review` | Visual hierarchy refresh only; behavior and data-source contracts are unchanged | `apps/console-ui` styles and public page in PR #218 | Fresh-main `validate`, resolved conversations when present, risk-based review and visual/browser acceptance; no functional gap may be marked closed |
| `FABRIC-PORT-01` | `next` | A `Provider` interface exists, but startup, DTOs and launch/recovery truth remain Tencent-specific | Fabric source + Fabric contract + focused Control Plane client DTOs only when required | One real `local-docker` adapter passes provider-neutral launch, readback and recovery contracts while the Tencent path remains green |
| `CONSOLE-SELF-SERVICE-01` | `next` | Current Pilot is administrator-provisioned; public registration, payment/order and complete tenant self-service are absent | Console UI + Control Plane + management/product contracts | A tenant can onboard, see authoritative balance/usage, create 0..N Workspaces and complete one approved payment/order path without acquiring wallet or provider authority |
| `MANAGED-POLICY-01` | `planned` | Controlled Pilot admission and a fixed offer exist, but reusable account approval, quota and managed-resource policy are not yet one portable surface | Control Plane policy + Console management/product contracts | Account policy authorizes or denies a resource plan without owning package state, provider mutation or Runtime state |
| `BILLING-EVIDENCE-01` | `planned` | Gateway wallet projection and Workspace debit, refund, renewal and reconciliation paths are code-complete and locally tested; customer auto-renew and real monthly evidence remain absent | Control Plane billing policy + Gateway calls + Ledger receipts; no Fabric balance state | One immutable release proves an exact single Workspace-period debit, provider renewal/readback, Ledger receipt and failure recovery without a second wallet |
| `DEPLOY-ISOLATION-01` | `planned` | Services are separate Deployments but share one database credential, internal token and ConfigMap | reusable deploy contract/manifests + service startup configuration | Service-specific DB roles/URLs and internal identities prevent cross-owner table writes and caller impersonation; instance secrets migrate with rollback and readback |
| `MODULE-COHESION-01` | `planned` | Fabric `service.go` and Control Plane launch, recovery and state-store files concentrate unrelated capabilities and create review conflicts | one owning Go package per change, no public API or schema change | Resource/runtime/recovery/persistence capabilities move into focused files with unchanged package API, state machine and full tests |
| `ACTIONS-MAINT-01` | `planned` | The production customer workflow remains a 3,500-line multi-operation release surface | repository CI + production contracts; preserve one production authorization owner | Operation-family workflows share one explicit authorization/readback owner, and remaining third-party Action/toolchain refs are immutable |
| `INSTANCE-MEDOPL-01` | `external_owner` | Instance repository is initialized, but its profile still names the pre-unification implementation repos and Cloud still co-locates medopl values | `opl-instance-medopl` first; Cloud deploy files only for extraction cleanup | Instance profile points to `gaofeng21cn/one-person-lab-cloud`, immutable release refs and secret-owner refs are current, reusable Cloud has no medopl-owned writer, and fresh receipts exist |
| `WORKSPACE-ROUTER-01` | `later` | Control Plane still proxies Workspace HTML/API/WebSocket traffic, coupling management-plane availability to the data plane | dedicated router boundary only after measured need | A separately owned router preserves Runtime authentication and routing readback without moving entitlement or provider truth |
| `WORKSPACE-CONTINUITY-01` | `external_owner` | One project/task/artifact continuation model across App and online Workspace lacks owner evidence | App + Workspace implementation owners | Owner contracts and live readback prove continuation without Cloud copying project or artifact truth |
| `RESOURCE-BINDING-01` | `planned` | Compute/storage launch is implemented for the Pilot; environments and connectors do not yet share one portable plan/approve/execute/collect surface | Fabric + the selected connector/environment owner contract | One end-to-end resource path returns provider-neutral facts and a Ledger receipt without a second policy or wallet owner |
| `PACKAGE-PROJECTION-01` | `external_owner` | Cloud does not yet project exact Package publication plus fresh carrier state end to end | Package owner + native carrier + Framework aggregation + one Cloud consumer | One Cloud surface consumes owner refs/readback without adding a Cloud registry, lock or lifecycle writer |
| `CONNECTOR-01` | `external_owner` | Stable generic connector access and domain-specific adapter evidence are not yet available to Cloud | OPL Connect + selected domain owner + one Cloud consumer | One connector supplies normalized source refs while its domain adapter retains domain semantics and credentials remain with the connector owner |
| `SERVE-01` | `later` | Service, Revision, Deployment, Agent Edge, Hosted UI and Embed remain target architecture | OPL Serve + Runway + package/domain owners | Exact package digest reaches an immutable Revision and authenticated public API with receipts before Hosted UI/Embed expansion |
| `RUNWAY-01` | `external_owner` | A shared Invocation/Session lifecycle for native and approved external execution providers remains outside current Cloud implementation | OPL Runway + one Fabric/provider adapter | One Invocation/Session contract supports routing, streaming, cancellation and readback without moving Service identity or domain verdicts |
| `EVIDENCE-CONTINUATION-01` | `planned` | Ledger APIs exist, but exact user-visible action/artifact continuation is not closed across Cloud surfaces | Ledger + one consuming owner | One user-visible action returns exact receipt, review and continuation refs with authoritative readback |

These are product-family gaps. A gap closes only after its named owner surface
exists and the target architecture can reference fresh machine, runtime or
implementation evidence without copying an external owner's truth.

## Evidence Gaps

Real App-to-Workspace continuation, self-service Console onboarding and payment,
local/self-hosted provider runtime paths, managed and institution-owned resource
paths, data-egress acceptance, connector/provider runtime evidence, public-edge
security and isolation, deployment health and rollback, Invocation/Session
streaming and cancellation, billing/quota readback, user-visible receipts,
production soak, release evidence, and owner acceptance remain later evidence
lanes.

Docs, planning contracts, generated projections, tests, or a rendered
whitepaper can close only their own layers. They cannot substitute for these
runtime, release, security, billing, domain, or owner-evidence lanes.

## Target Delivery Order

This is priority order, not a single-owner serialization rule. Rows with
non-overlapping owners and write sets may proceed concurrently.

1. Complete `CONSOLE-UI-01` without promoting it into a functional claim.
2. Close `FABRIC-PORT-01` with one real `local-docker` path while preserving the
   current Tencent adapter.
3. Close `CONSOLE-SELF-SERVICE-01` from the current administrator-provisioned
   Pilot boundary.
4. Close `MANAGED-POLICY-01` without giving Console provider or package authority,
   then prove `BILLING-EVIDENCE-01` against the external Gateway owner.
5. Correct and extract `INSTANCE-MEDOPL-01`, then close
   `DEPLOY-ISOLATION-01` through instance-bound migration and readback.
6. Split one high-conflict owner file under `MODULE-COHESION-01` only after its
   focused behavior suite is green.
7. Split `ACTIONS-MAINT-01` only at operation-family boundaries while preserving
   one production authorization and readback owner.
8. Close `RESOURCE-BINDING-01` with explicit plan, approval, collection,
   settlement, and receipt.
9. Project exact owner Package identity/publication and fresh carrier
   status/actions into Cloud surfaces without a registry or lock copy.
10. Close one `CONNECTOR-01` and `EVIDENCE-CONTINUATION-01` path without moving
    connector credentials or domain truth into Cloud.
11. Close a portable `SERVE-01` Entrypoint Contract, immutable Agent Revision,
    dedicated Agent Edge and API-only private-beta path.
12. Close `RUNWAY-01` with one OPL-native provider adapter.
13. Add Hosted UI and Embed clients only through the same public API.
14. Add broader service plans only after live security, isolation, billing, and
    soak evidence exists.

## Explicit Non-Goals

- Do not rebuild package discovery, installation, lock, update, rollback, or
  repair in Cloud.
- Do not impose a fixed product-level Workspace count or collapse multiple
  Workspaces into an account singleton.
- Do not merge independently owned Workspaces into an implicit multi-tenant
  SaaS workbench.
- Do not expose a Workspace URL, sandbox, or provider session as a public Agent
  endpoint.
- Do not create separate execution behavior for API, Hosted UI, and Embed.
- Do not restore a second Cloud product, documentation, Console, Fabric, or
  Ledger repository as a parallel current writer.
- Do not treat policy approval, resource binding, receipt presence, document
  completeness, or artifact rendering as Agent, service, or production
  readiness.

## Next-Round Agent Prompt

### Goal

Close one owner-approved OPL Cloud functional or structural gap and fold the
result back into this single Active Truth without promoting code or tests into
runtime claims.

### Write Scope

- `docs/roadmap.md`
- the one existing product or planning-contract owner directly affected by the
  selected gap
- `docs/README.md` and root `README*` only if public navigation or product
  language materially changes
- the owning source, machine contract and focused tests when implementation is
  explicitly in scope

### Non-goals And Forbidden Scope

- no second Cloud product or implementation repository;
- no second package registry, lock, Invocation/Session store, domain verdict,
  or owner receipt;
- no parallel Cloud writer outside `one-person-lab-cloud` or the explicit
  non-code instance contract;
- no availability, release, billing, security, or production claim without
  exact owner evidence.

### Live Truth Inputs

- fresh `main`, worktree, dirty, ahead/behind, remote, and owner/write-set gate
  for every affected repository;
- current App and Framework contracts/read models for package, state/action,
  Workspace, and Runway boundaries;
- the selected service owner's machine contract, source/tests, runtime readback,
  blockers, and owner receipt;
- this index, architecture, focused product owner, and current roadmap.

### Required Actions

1. Select exactly one open row from `Functional And Structural Gaps` and name
   its owner surface.
2. Verify the current implementation and contract state from fresh owner
   evidence; do not infer it from Cloud prose.
3. Implement or update only the authorized owner surface and its focused tests.
4. Rewrite the focused Cloud target reference only where the owner boundary or
   public target changed.
5. Remove or rewrite the closed gap in this file; keep evidence-only tails in
   `Evidence Gaps`.
6. Update the docs index only when a current owner or entry changes.

### Verification Commands

- run this repository's affected focused/full validation;
- run the OPL Doc doctor against each changed repository as a risk map;
- scan all tracked Markdown relative links;
- run `git diff --check`;
- if whitepaper source/profile changes, run
  `node --experimental-strip-types scripts/build-opl-cloud-whitepaper.ts`.

### Completion Gate

- one structural gap is closed in its owning surface and no competing truth is
  introduced;
- current docs describe only fresh facts and remaining gaps;
- tests/docs evidence is not promoted to runtime, release, owner acceptance, or
  production readiness;
- changed repository `main` branches contain the final bytes and their task
  worktrees/branches are cleaned after absorption.

### Foldback Target

- current status, remaining gaps, evidence tails, and the next prompt return to
  `docs/roadmap.md`;
- stable product responsibility returns to `docs/architecture.md` or the one
  focused product/contract owner;
- navigation changes return to `docs/README.md` and public entry language only
  when needed.
