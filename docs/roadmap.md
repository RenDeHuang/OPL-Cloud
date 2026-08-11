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
| Development governance | `parallel_work_serialized_integration` | Independent module lanes may develop and review concurrently; only an overlapping write set, one shared contract revision, canonical `main`, or a real production mutation is serialized |
| Source module boundaries | `enforced_in_ci` | Console network ownership, Go cross-service imports and Fabric-only cloud SDK ownership are checked by `tests/contracts/module-physical-boundaries.test.ts` through `npm test` |
| Whitepaper | `source_and_build_profile_present` | Source/build evidence does not prove publication or Cloud service readiness |
| Service delivery | `candidate_not_production_proven` | Current state is owned by `docs/status.md`, machine contracts, CI, deployment readback and owner evidence |

## Functional And Structural Gaps

State and priority answer different questions. `in_review` has a live
implementation PR, `next` is accepted and ready to claim, `planned` is accepted
but not in the immediate portfolio, `candidate` requires a caller/contract/data
admission decision before mutation, `later` waits for an explicit trigger, and
`external_owner` proceeds in its owning repository. Priority ranks urgency and
net product or structural benefit: `P0` is the current product or integration
critical path, `P1` is high-leverage work to keep active, `P2` is valuable after
an owner or boundary decision, and `P3` is trigger-driven.

Priority is not dependency order. Independent `P0` and `P1` rows should run in
parallel; a `P2` row may start whenever it has a free owner and non-blocking
write set. Only one overlapping write set, one shared contract revision,
canonical `main`, or one real production mutation is serialized. Each execution
lane selects one row, stays inside its owner/write set, and closes only the
listed acceptance boundary. Open pull requests remain the live execution view;
this table owns why the work exists and what completion means.

As of 2026-08-11, the only open pull request is
[PR #218](https://github.com/gaofeng21cn/one-person-lab-cloud/pull/218), a
Console visual refresh whose last `validate` passed but whose branch is behind
`main`. It does not implement self-service onboarding, payment, provider
portability or production evidence.

| ID | State | Priority | Urgency | Net benefit | Gap and current fact | Owner / write set | Close condition |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `CONSOLE-UI-01` | `in_review` | `P0` | immediate | medium | Visual hierarchy refresh only; behavior and data-source contracts are unchanged | `apps/console-ui` styles and public page in PR #218 | Fresh-main `validate`, resolved conversations when present, risk-based review and visual/browser acceptance; no functional gap may be marked closed |
| `CONSOLE-SELF-SERVICE-01` | `next` | `P0` | immediate | very high | Current Pilot is administrator-provisioned; public registration, payment/order and complete tenant self-service are absent | Console UI + Control Plane + management/product contracts | A tenant can onboard, see authoritative balance/usage, create 0..N Workspaces and complete one approved payment/order path without acquiring wallet or provider authority |
| `FABRIC-PORT-01` | `next` | `P0` | immediate | very high | A `Provider` interface exists, but startup, DTOs and launch/recovery truth remain Tencent-specific | Fabric source + Fabric contract + focused Control Plane client DTOs only when required | One real `local-docker` adapter passes provider-neutral launch, readback and recovery contracts while the Tencent path remains green |
| `BILLING-EVIDENCE-01` | `planned` | `P1` | high | very high | Gateway wallet projection and Workspace debit, refund, renewal and reconciliation paths are code-complete and locally tested; customer auto-renew and real monthly evidence remain absent | Control Plane billing policy + Gateway calls + Ledger receipts; no Fabric balance state | One immutable release proves an exact single Workspace-period debit, provider renewal/readback, Ledger receipt and failure recovery without a second wallet |
| `DEPLOY-ISOLATION-01` | `next` | `P1` | high | high | Services are separate Deployments but share one database credential, internal token and ConfigMap | reusable deploy contract/manifests + service startup configuration; instance application stays in `opl-instance-medopl` | Service-specific DB roles/URLs and internal identities prevent cross-owner table writes and caller impersonation; reusable and instance changes converge only for deployment readback |
| `MODULE-COHESION-01` | `next` | `P1` | high | high | Fabric `service.go` and Control Plane launch, recovery and state-store files concentrate unrelated capabilities and create review conflicts | one focused slice inside one owning Go module per PR; no cross-module package or public API/schema change | Resource/runtime/recovery/persistence capabilities move into focused files with unchanged package API, state machine and full tests |
| `MANAGED-POLICY-01` | `planned` | `P1` | medium | high | Controlled Pilot admission and a fixed offer exist, but reusable account approval, quota and managed-resource policy are not yet one portable surface | Control Plane policy + Console management/product contracts | Account policy authorizes or denies a resource plan without owning package state, provider mutation or Runtime state |
| `INSTANCE-MEDOPL-01` | `external_owner` | `P1` | high | high | Instance repository is initialized, but its profile still names the pre-unification implementation repos and Cloud still co-locates medopl values | `opl-instance-medopl` profile and receipt lane; it may run in parallel with reusable Cloud extraction | Instance profile points to `gaofeng21cn/one-person-lab-cloud`, immutable release refs and secret-owner refs are current, reusable Cloud has no medopl-owned writer, and fresh receipts exist |
| `RESOURCE-BINDING-01` | `planned` | `P2` | medium | high | Compute/storage launch is implemented for the Pilot; environments and connectors do not yet share one portable plan/approve/execute/collect surface | Fabric + the selected connector/environment owner contract | One end-to-end resource path returns provider-neutral facts and a Ledger receipt without a second policy or wallet owner |
| `WORKSPACE-CONTINUITY-01` | `external_owner` | `P2` | medium | high | One project/task/artifact continuation model across App and online Workspace lacks owner evidence | App + Workspace implementation owners | Owner contracts and live readback prove continuation without Cloud copying project or artifact truth |
| `ACTIONS-MAINT-01` | `external_owner` | `P2` | medium | medium | Cloud's registered third-party Action and reusable-workflow refs are SHA-pinned and contract-checked; the former 3,500-line production dispatcher is now 279 lines calling five typed operation-family workflows. The upstream `one-person-lab` reusable whitepaper workflow still checks out its toolchain with `ref: main` in both build and publish jobs | `one-person-lab/.github/workflows/reusable-whitepaper.yml` only; Cloud remains a read-only consumer at a pinned reusable-workflow SHA | Upstream pins both internal toolchain checkouts to immutable commit refs and rejects mutable refs in its contract tests; the Cloud caller remains pinned to one reviewed reusable-workflow SHA |
| `PACKAGE-PROJECTION-01` | `external_owner` | `P2` | medium | medium | Cloud does not yet project exact Package publication plus fresh carrier state end to end | Package owner + native carrier + Framework aggregation + one Cloud consumer | One Cloud surface consumes owner refs/readback without adding a Cloud registry, lock or lifecycle writer |
| `CONNECTOR-01` | `external_owner` | `P2` | medium | medium | Stable generic connector access and domain-specific adapter evidence are not yet available to Cloud | OPL Connect + selected domain owner + one Cloud consumer | One connector supplies normalized source refs while its domain adapter retains domain semantics and credentials remain with the connector owner |
| `EVIDENCE-CONTINUATION-01` | `planned` | `P2` | medium | medium | Ledger APIs exist, but exact user-visible action/artifact continuation is not closed across Cloud surfaces | Ledger + one consuming owner | One user-visible action returns exact receipt, review and continuation refs with authoritative readback |
| `WORKSPACE-ROUTER-01` | `later` | `P3` | low | medium | Control Plane still proxies Workspace HTML/API/WebSocket traffic, coupling management-plane availability to the data plane | dedicated router boundary only after measured need | A separately owned router preserves Runtime authentication and routing readback without moving entitlement or provider truth |
| `SERVE-01` | `later` | `P3` | low | high | Service, Revision, Deployment, Agent Edge, Hosted UI and Embed remain target architecture | OPL Serve + Runway + package/domain owners | Exact package digest reaches an immutable Revision and authenticated public API with receipts before Hosted UI/Embed expansion |
| `RUNWAY-01` | `external_owner` | `P3` | low | high | A shared Invocation/Session lifecycle for native and approved external execution providers remains outside current Cloud implementation | OPL Runway + one Fabric/provider adapter | One Invocation/Session contract supports routing, streaming, cancellation and readback without moving Service identity or domain verdicts |

These are product-family gaps. A gap closes only after its named owner surface
exists and the target architecture can reference fresh machine, runtime or
implementation evidence without copying an external owner's truth.

## Simplification Backlog

The repository-wide over-design audit is a deletion-first risk map, not deletion
authorization. It considered structural complexity only; it did not establish
correctness, security, performance, external API usage, or data-retention
obligations. A `candidate` row first proves its target-state disposition, all
real callers, persisted-data handling, and rollback boundary. Only then may it
be promoted to `next` or removed from the backlog. Net benefit is the expected
reduction in code, concepts, maintenance and change collisions after those
obligations are preserved.

| ID | State | Priority | Candidate and evidence | Net benefit | Implementation risk | Owner / admission and close condition |
| --- | --- | --- | --- | --- | --- | --- |
| `SIMPLIFY-RECOVERY-CLI-01` | `next` | `P0` | Retire the old Recovery CLI branch inside `production-live-qa.ts`; the real entrypoint rejects seven old flags while tests still call the bypassed internal path | medium-high | medium | `tools/production-live-qa.ts` + focused tests; prove current workflows use only accepted entrypoints, then delete the unreachable parser/execution/artifact branch |
| `SIMPLIFY-CP-ARCHIVE-01` | `candidate` | `P1` | Remove the disabled archive/retention vertical; its worker switch is absent from deploy/workflow configuration, its two operator APIs have no in-repo consumer, and six Archive Ent models generate about 15,956 lines | very high | high | Control Plane archive routes/store/schema only; first decide retention obligations and external callers, then migrate or preserve required records before deleting the vertical |
| `SIMPLIFY-CP-EXEC-01` | `candidate` | `P1` | Remove `ExecutionRequest`, `ProjectTaskSyncHead`, `WorkspaceBackup`, and `WorkspaceSyncEvent`; their contract is superseded and no non-test product caller was found, while generated code is about 13,366 lines | very high | medium | Control Plane schema/store + superseded contract; prove database and external-caller disposition, then delete one coherent persisted-model batch |
| `SIMPLIFY-FABRIC-TRANSFER-01` | `candidate` | `P1` | Remove `ContentTransfer`; it carries about 5,029 generated lines plus service, stores, routes and tests, but has no in-repo Control Plane or product caller and transfer is outside the Pilot | high | medium | Fabric transfer/schema/routes only; inventory external Fabric API callers and retained data before coherent route/store/schema removal |
| `SIMPLIFY-FABRIC-SNAPSHOT-01` | `candidate` | `P1` | Remove StorageSnapshot/Restore; provider methods, Tencent implementation, service state, routes and tests exist without an in-repo product caller, while backup/recovery is outside the Pilot | high | medium | Fabric provider/service/routes; prove no external API consumer and preserve any required provider cleanup/readback before removal |
| `SIMPLIFY-CONSOLE-CSS-01` | `planned` | `P1` | Collapse multiple retained visual generations in the 6,010-line stylesheet after PR #218; runtime fixes the active direction to `quiet-ledger` | medium | medium | `apps/console-ui/src/styles.css` only after the visual PR lands; preserve current desktop/mobile baselines and Workspace interaction states |
| `SIMPLIFY-DOCS-PLANS-01` | `next` | `P1` | Remove or historicalize seven completed dated plan/spec files totaling 1,446 lines; active-doc policy assigns completed execution detail to Git history or `docs/history/**` | medium-low | low | `docs/superpowers/**`; retain only unique no-resurrection provenance and leave no active navigation dependency |
| `SIMPLIFY-BROWSER-QA-01` | `next` | `P1` | Remove 14 tracked Browser QA PNGs totaling about 2.3 MB; the path is ignored, current UI contracts do not own these dated outputs, and only historical plans reference them | medium-low | low | `output/browser-qa/**`; verify no active README, product contract or visual test consumes the images |
| `SIMPLIFY-LEDGER-VERTICAL-01` | `candidate` | `P2` | Shrink Artifact, Review, ReviewPolicy and Continuation verticals; Ledger exposes full HTTP/store implementations, but current Control Plane product paths consume receipts/reconciliation rather than these APIs | high | high | Ledger API/store/schema + Cloud consumer contracts; first resolve `EVIDENCE-CONTINUATION-01` and external callers so target evidence capability is not deleted from accidental current usage alone |
| `SIMPLIFY-CP-IDENTITY-01` | `candidate` | `P2` | Remove one-to-one Organization/Membership compatibility storage if self-service identity no longer needs it; the pair generates about 4,187 lines and spreads through provisioning, reconcile, bootstrap and migrations without a Console DTO | high | high | Control Plane identity/schema/migrations; decide the self-service account model and run an explicit persisted-identity migration before deleting compatibility rows |
| `SIMPLIFY-PROVIDER-ACCEPTANCE-01` | `candidate` | `P2` | Shrink the paused fixed-slot Provider Acceptance surface; two manual workflows, tools, service routes and tests remain while the contract marks the lane paused and not a release gate | medium-high | high | provider-acceptance workflows/tools/routes; preserve one explicit approved real-provider evidence route if it remains a current obligation |
| `SIMPLIFY-ACTIONS-REUSE-01` | `planned` | `P2` | Consolidate repeated isolated-checkout, remote verification, cleanup and Node setup steps across the five operation-family workflows using a repository-local composite action | medium | medium | `.github/workflows/**` + one `.github/actions/**` owner; prove the action hides stable repetition rather than reintroducing a monolithic dispatcher |
| `SIMPLIFY-CP-FACADE-01` | `planned` | `P2` | Shrink the 79-method `internal/controlplane.Service` facade; many methods assert one capability or forward directly to Fabric, Ledger or Sub2API | medium | medium | Control Plane service/server ports; preserve one owning product orchestration boundary and migrate real callers before removing pass-through methods |
| `SIMPLIFY-CLI-ARGS-01` | `planned` | `P2` | Replace seven handwritten `cliArgs` or `parseArgs` implementations with Node `node:util.parseArgs` | medium-low | low | the seven owning tools and their focused tests; preserve existing accepted flags and error behavior without a shared custom parser |
| `SIMPLIFY-STATIC-ASSETS-01` | `later` | `P3` | Replace Control Plane's custom static-file lookup, SPA fallback, `Accept-Encoding` parser and request-time gzip with `http.FileServer`, build-time compression or the ingress owner | low | medium | Control Plane static handler + deploy edge; select one compression owner and preserve cache, SPA, range and content-type behavior |

Rows with the same priority are ordered by urgency and expected net benefit, not
by a global lock. High-risk candidates can undergo read-only caller and data
admission in parallel with product development; their eventual schema or public
route mutations integrate serially only where write sets overlap.

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

## Concurrent Delivery Lanes

The development model is `parallel_work_serialized_integration`: independent
work proceeds concurrently, while the smallest shared mutation is serialized.
No production qualification, instance receipt, or unrelated lane may be used as
a prerequisite for starting local development, CI, or a non-production preview.

The recommended initial portfolio uses five code owners plus one verification
owner. With fewer people, preserve lanes 2, 3 and 4 first; lane 1 already has a
live PR owner, and lanes 5 and 6 fill available capacity without becoming gates.

| Lane | Work now | Owner and bounded write set | Parallel and integration boundary | First accepted outcome |
| --- | --- | --- | --- | --- |
| 1. UI closeout | `CONSOLE-UI-01` | Existing PR #218 owner; `components.css`, `tokens.css`, `PublicPages.tsx`, `styles.css` only | Rebase and integrate before another writer changes those four files; Console API/backend work continues in parallel | Fresh-main visual/browser acceptance and green `validate`, with no functional claim |
| 2. Console product | `CONSOLE-SELF-SERVICE-01`, then `MANAGED-POLICY-01` | One Console/Control Plane product owner; auth, account, balance/usage, order and Workspace product API/UI slices | Avoid lane 1's four files until it lands; one owner controls each changed product contract, while backend and unaffected UI can develop concurrently | One account onboards, reads authoritative wallet/usage and creates multiple independent Workspaces through product APIs |
| 3. Fabric portability | `FABRIC-PORT-01` | One Fabric provider owner; provider port, `local-docker`, startup selection and only required Control Plane provider DTOs | Fabric dead-feature candidates may be audited in parallel; branches touching `service.go`, provider interfaces or Fabric HTTP contracts replay and integrate one at a time | Real local Docker launch/readback/recovery passes the same provider-neutral contract as Tencent |
| 4. Deployment and instance | `DEPLOY-ISOLATION-01` plus `INSTANCE-MEDOPL-01` | One reusable-Cloud owner for `deploy/**` and startup configuration; a different instance owner for `opl-instance-medopl` | Both repositories develop independently and join only for exact release qualification, rollback and readback | Service-specific DB/internal identities in reusable manifests and current immutable refs in the medopl profile |
| 5. Simplification | `SIMPLIFY-RECOVERY-CLI-01`, `SIMPLIFY-DOCS-PLANS-01`, `SIMPLIFY-BROWSER-QA-01`; admission audits for the high-yield candidates | One low-risk cleanup owner; separate read-only Control Plane and Fabric caller/data reviewers may work concurrently | Each deletion is a separate coherent PR; no schema or public route deletion begins from the over-design audit alone | Retired CLI path and inactive tracked artifacts are gone; high-risk rows have an evidence-backed keep/shrink/delete disposition |
| 6. Evidence | `BILLING-EVIDENCE-01` | One verification owner for focused fixtures, tools and approved evidence workflows; owning service code changes return to lanes 2 or 3 | Local deterministic evidence work runs now; paid provider or production mutation waits only for its own approval and exact merged revision | Exact-one Workspace-period settlement and receipt path is reproducible locally before approved live qualification |

`MODULE-COHESION-01` is executed inside the owning lane, not as a repository-wide
refactor. Control Plane cohesion may run beside Fabric portability when its write
set excludes the Console product slice. Fabric cohesion may run beside Console
work but shares a short integration owner with lane 3 when it touches
`service.go` or the Provider contract.

The efficient integration sequence is:

1. Immediately refresh PR #218 and land the three low-risk simplification rows
   as independent PRs; lanes 2 through 4 start at the same time and do not wait.
2. Integrate backward-compatible product or provider contract changes before
   their consumers. Replay overlapping branches on fresh `main`; do not keep a
   second facade, DTO, route or runtime fallback after the caller cutover.
3. Promote high-yield deletion candidates only after caller and persisted-data
   admission. Control Plane archive/shared-execution work waits only for an
   overlapping Control Plane mutation; Fabric transfer/snapshot work waits only
   for lane 3's shared Fabric integration boundary.
4. Run full local gates on each merged candidate. Production and instance
   qualification use one exact immutable revision after the relevant code lanes
   converge; failed live evidence leaves only that evidence row open.

Rows marked `planned` are not blocked by completion of `next` lanes. When an
owner and capacity are available, promote the row and proceed without inventing
a dependency. Rows marked `external_owner` proceed in their repository, then
integrate through exact refs and public contracts.

## Integration And Production Gates

These gates apply when lanes converge or when an exact revision is promoted;
they do not govern whether independent development may begin.

1. A changed cross-module boundary has one owner, one compatible contract
   revision, and focused tests on both sides.
2. Each branch is replayed on fresh `main`; the required `validate` context and
   review-conversation gate pass before the canonical merge.
3. A deployment-isolation release joins reusable Cloud artifacts with the exact
   instance profile only at deployment qualification, rollback and readback.
4. Runtime, billing, security, owner acceptance and production claims close only
   from their authoritative evidence; failure leaves that evidence lane open and
   does not roll unrelated development backward.

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

Advance one owner-approved OPL Cloud functional, structural or simplification
row and fold the result back into this single Active Truth without promoting
code or tests into runtime claims.

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

1. Each execution lane selects exactly one open row from `Functional And
   Structural Gaps` or `Simplification Backlog`, names its owner surface and
   checks the active portfolio for an existing overlapping writer. Other lanes
   may own non-overlapping rows concurrently.
2. Verify the current implementation and contract state from fresh owner
   evidence; do not infer it from Cloud prose.
3. For a `candidate`, first produce the caller, target-contract, persisted-data
   and external-consumer disposition. Do not mutate the candidate until that
   evidence promotes it to `next` or `planned`.
4. Implement or update only the authorized owner surface and its focused tests.
5. Rewrite the focused Cloud target reference only where the owner boundary or
   public target changed.
6. Remove or rewrite the closed gap in this file; keep evidence-only tails in
   `Evidence Gaps`.
7. Re-rank affected rows when fresh urgency, benefit, risk or caller evidence
   changes; do not preserve an obsolete priority for historical continuity.
8. Update the docs index only when a current owner or entry changes.

### Verification Commands

- run this repository's affected focused/full validation;
- run the OPL Doc doctor against each changed repository as a risk map;
- scan all tracked Markdown relative links;
- run `git diff --check`;
- if whitepaper source/profile changes, run
  `node --experimental-strip-types scripts/build-opl-cloud-whitepaper.ts`.

### Completion Gate

- one row is closed in its owning surface, or one `candidate` has an
  evidence-backed keep/shrink/delete disposition and revised admission state;
- no competing truth or long-term compatibility path is introduced;
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
