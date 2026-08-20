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
- `later`: waits for an explicit product or evidence trigger;
- `external_owner`: proceeds in its owning repository.

`P0` is the current product or integration critical path, `P1` is high-leverage
work, `P2` requires an owner or boundary decision, and `P3` is trigger-driven.
Priority is not dependency order. Independent rows proceed concurrently; only
an overlapping write set, one shared contract revision, canonical `main`, or a
real production mutation is serialized.

## Business Operating Model

The current Pilot business is an administrator-provisioned account product. A
customer signs in with a Control Plane Session, reads the authoritative
Sub2API balance and usage projection, selects a Workspace package, confirms one
prepaid monthly charge, and submits one durable Workspace Launch. Control Plane
owns the customer-facing operation and commercial coordination; the single
Reconciler calls Fabric for provider-neutral resource stages; Fabric performs
provider mutations and readback; Ledger records receipts and reconciliation
evidence; Sub2API remains the only spendable wallet, Key, routing, and usage
authority. A successful Workspace returns an owner-scoped URL and credentials;
the customer can reveal or rotate access credentials, open the Runtime, inspect
billing evidence, and issue an owner-authorized delete.

The current Pilot does not provide public registration, customer payment/top-up,
shared multi-user Workspaces, or a user-facing Workspace Suspend/Resume flow.
The administrator-only `Launch Resume` command is a bounded continuation of a
`manual_review` Launch and is not the same business action as restoring a
suspended Workspace.

| Actor or authority | Owns | Must not own or imply |
| --- | --- | --- |
| Customer / Workspace owner | Account-scoped commands, package choice, access, usage and billing views, Workspace delete | Wallet truth, provider IDs, or another account's Workspace |
| Console | Presentation and calls to Control Plane product APIs | Persistence, provider mutation, wallet, Ledger truth, or direct Sub2API management access |
| Control Plane | Sessions, account policy, Workspace Launch cursor, entitlement, settlement coordination, customer DTOs | Provider resources, spendable balance, or Ledger evidence truth |
| Fabric | Compute, storage, attachment, Secret binding, Runtime, provider adapter and authoritative resource readback | Customer balance, pricing, or account policy |
| Ledger | Append-only receipts, evidence, review, reconciliation and continuation references | Balance mutation, provider mutation, or Launch authorization |
| Sub2API | Spendable USD balance, API Keys, model routing and request usage | Workspace lifecycle, provider resources, or Cloud receipts |
| Cloud product owner | Portable source, contracts, images, Compose assets, candidate and formal product Release | A concrete medopl production deployment, production Secrets, cluster authority, rollback or Instance receipts |
| `opl-instance-medopl` | Tencent/TKE profile, production Environment/Secrets, deployment, acceptance, canary, rollback and receipts | Copying Cloud runtime source or becoming a second Cloud product owner |

## User-Side Roadmap

This view describes the customer journey and its unresolved product decisions.
It does not turn source-level routes or UI options into proof of a live Pilot.

| ID | State | Priority | User journey or gap | Owner boundary | Acceptance |
| --- | --- | --- | --- | --- | --- |
| `MVP-LOCAL-WORKSPACE-GATEWAY-01` | `next` | `P0` | Sign in with an administrator-provisioned account, read balance/usage, choose Basic, create one Workspace, read back Runtime and credentials, open it, permanently delete it, and reconcile the original debit, deletion Receipt, and unchanged wallet history on a clean MacBook or single-server host. | Console + Control Plane + Fabric + Sub2API + Ledger | One real external Sub2API identity and wallet completes the end-to-end create/readback/open/delete journey with zero Delete wallet mutation; no fixture, source-only test, or health check is promoted to Pilot evidence. |
| `CONSOLE-LAUNCH-CONSISTENCY-01` | `next` | `P0` | The Console and product docs show Basic and Pro, while the current controlled Pilot admission rejects every non-Basic launch. The UI also rejects a balance exactly equal to the quoted charge although the server accepts `balance >= charge`. | Console + Control Plane product policy | Product owner chooses Basic-only Pilot or admits Pro; catalog, UI, pricing preview, admission, and error copy expose the same offer set; equality-boundary tests prove the same decision in browser and server paths. |
| `WORKSPACE-RENEWAL-REACTIVATION-01` | `planned` | `P1` | Renewal worker and billing state exist, but new launches and the customer API reject `autoRenew=true`; after unpaid expiry, the customer receives `workspace_reactivation_required` without a reactivation command. | Control Plane settlement + Sub2API + Ledger | Explicitly choose whether renewal enters the current product. If yes, add customer authorization, exactly-once debit, provider renewal/readback, expiry suspension, reactivation, refund/manual-review behavior, and receipts. If no, remove or demote unreachable renewal claims and fields. |
| `WORKSPACE-LIFECYCLE-CLOSURE-01` | `planned` | `P2` | Target lifecycle includes user Suspend/Resume, while current user routes do not; `Launch Resume` is administrator-only recovery for an unfinished Launch. | Console + Control Plane lifecycle owner | Either implement owner-authorized Suspend/Resume with provider readback and receipts, or explicitly move those actions to later scope. In both cases, route names and docs must not call Launch Resume a Workspace Resume. |
| `CONSOLE-SELF-SERVICE-01` | `later` | `P3` | Public registration, payment/top-up, organization identity and shared Workspace collaboration remain outside the Pilot. | Console + Control Plane identity and policy | A tenant can onboard, read authoritative wallet/usage, create 0..N Workspaces, and complete one approved payment/order path without creating a second wallet or provider authority. |

## Operations-Side Roadmap

Operations is a chain of controlled decisions, not a second product workflow:
install or select an exact product artifact, bind the external identity, run the
single Reconciler with bounded mutations, classify unknown results as manual
review, and qualify the exact bytes in the owning environment.

| ID | State | Priority | Operational lane | Owner boundary | Acceptance |
| --- | --- | --- | --- | --- | --- |
| `OPS-ACCOUNT-IDENTITY-READBACK-01` | `next` | `P1` | Bind the installation-owned Sub2API administrator to the reserved operator account and fail closed on identity, Session, wallet, or permission mismatch. | Control Plane + Sub2API | A clean installation proves the same external identity through login, `/auth/me`, balance, usage, and account-scope readback without exposing the management origin to the browser. |
| `MVP-LOCAL-WORKSPACE-GATEWAY-01` | `next` | `P0` | Operate one Launch Reconciler across Key, debit, compute, storage, attachment, Secret, Runtime, activation and Receipt stages; replay only through persisted idempotency and owner-authorized recovery. | Control Plane orchestration; Fabric mutation/readback; Ledger evidence | Lost debit responses remain `manual_review/debit`, never double-charge, create, or refund; each successful stage is bound to the typed Fabric contract and authoritative readback. |
| `LOCAL-WORKSPACE-INSTALL-CONTRACT-01` | `next` | `P1` | Make the explicit local Workspace profile executable by an operator: immutable Cloud image, immutable Workspace image, Docker socket, task-owned Secret root, gateway-container identity, and launch worker must be selected together. | Cloud installation assets + Fabric local-Docker adapter | Release assets and installation docs provide every required variable and a clean host reaches provider, Secret, Runtime and delete readback; the base Compose profile remains clearly control-services-only. |
| `PRODUCT-RELEASE-01` | `next` | `P1` | Separate replaceable candidate build from formal Release publication; a candidate is qualified before owner-promoted Release publication. | Cloud product owner + `opl-instance-medopl` | Exact candidate SHA and image digest deploy successfully, receive Instance product-acceptance readback, and are promoted without rebuilding different bytes; failed or unknown qualification creates no formal version. |
| `INSTANCE-MEDOPL-01` | `external_owner` | `P1` | Run protected production deployment, canary, acceptance, rollback and redacted receipt from the Instance repository rather than Cloud. | `opl-instance-medopl` | Runtime readiness, provider identity, isolation, billing, rollback and Acceptance B all read back for the exact candidate; `deployed_unverified` and `ready=false` remain until then. |

## Local Deployment Roadmap

Local deployment has two intentionally different profiles. The base Compose
profile starts PostgreSQL, Ledger, Fabric and Control Plane as separate control
services and publishes only Control Plane. The explicit local Workspace profile
adds Docker authority, a task-owned Secret root, an immutable Workspace image
and the Launch worker. A healthy base stack is therefore not a Workspace-ready
installation.

| ID | State | Priority | Local path | Owner boundary | Acceptance |
| --- | --- | --- | --- | --- | --- |
| `LOCAL-CONTROL-SERVICES-01` | `next` | `P1` | Verify one public Release on a clean Docker host: attested assets, independent service/database credentials, three service schemas, health checks, operator identity binding and Control Plane readback. | Cloud installation assets + three service owners | `v0.1.7` assets start and read back as a healthy control plane; the result is explicitly labelled non-Workspace until the overlay path passes. |
| `LOCAL-WORKSPACE-INSTALL-CONTRACT-01` | `next` | `P1` | Overlay `deploy/portable/compose.local-workspace.yaml` with the exact Cloud and Workspace digests, Docker socket and Secret-root/gateway settings; only Fabric receives host Docker authority. | Fabric adapter + Cloud installation owner | `docker compose config` and clean-host qualification prove no missing required variable, no mutable image tag, and no Docker authority granted to Control Plane or Ledger. |
| `MVP-LOCAL-WORKSPACE-GATEWAY-01` | `next` | `P0` | Run the actual customer path through local Docker, including external Sub2API login, Basic quote/debit, Workspace Runtime readback, browser open, permanent owner delete, unchanged wallet readback, and deletion Receipt reconciliation. | Control Plane + Fabric + Sub2API + Ledger | One clean-host live run passes create/readback/open/delete with zero Delete wallet mutation and leaves no foreign or unlabeled resource; fixture-only qualification remains a lower evidence level. |
| `LOCAL-WORKSPACE-RECOVERY-READBACK-01` | `planned` | `P1` | When a provider or external billing result is unknown, preserve the original Launch identity and recovery authority instead of issuing an unbounded cleanup or successor Launch. | Control Plane Launch recovery + Fabric readback + Ledger review | Repeated readback converges by identity; manual review contains exact stage, budget, owner, and reason; cleanup occurs only after owner-authoritative absence/readback. |

## Cloud Deployment Roadmap

Cloud deployment means qualification of a concrete Instance, not publication of
the portable Cloud product. Cloud owns reusable adapters and release assets;
`opl-instance-medopl` owns Tencent/TKE selection, Secrets, protected mutation,
runtime acceptance, rollback and receipts.

| ID | State | Priority | Cloud/Instance path | Owner boundary | Acceptance |
| --- | --- | --- | --- | --- | --- |
| `PRODUCT-RELEASE-01` | `next` | `P1` | Build a digest-addressed non-Release candidate, deploy it through the Instance owner, then manually promote the same qualified bytes to a formal Cloud Release. | Cloud product owner + Instance consumer | No successor to the current public Release is published while the one-dispatch build-and-publish workflow cannot prove exact-byte promotion. |
| `INSTANCE-PROVIDER-ACCEPTANCE-MIGRATION-01` | `external_owner` | `P1` | Consume provider-neutral Cloud facts in the protected Instance acceptance path; canonical compute/storage IDs decide readiness, while legacy projections remain diagnostic-only. | Instance caller + Cloud Fabric adapter | Exact candidate readback returns the same provider IDs and refs used by the acceptance decision; no second Reconciler or provider-specific policy leaks into Control Plane. |
| `INSTANCE-MEDOPL-01` | `external_owner` | `P1` | Deploy the exact Cloud candidate to Tencent/TKE, read back rollout/runtime/isolation/product state, and retain rollback authority in Instance. | `opl-instance-medopl` | The Instance receipt proves production protection, deployment, Runtime readiness, provider isolation, billing, rollback and product acceptance for one exact immutable candidate. |

## Conflict And Decision Roadmap

The following register distinguishes a real business contradiction from a lower
evidence level or a stale statement. A source-level implementation, fixture, or
first deployment receipt does not override a conflicting user-facing contract;
an unknown runtime result does not become a failure or success by inference.
`resolved_in_docs` means the apparent conflict is already reconciled by an
explicit boundary and needs no implementation change; it does not claim the
referenced runtime path is qualified.

| ID | Class | State | Conflict or ambiguity | Resolution owner | Required evidence or decision |
| --- | --- | --- | --- | --- | --- |
| `CONFLICT-CONSOLE-OFFER-ADMISSION-01` | `business_conflict` | `next` | Basic/Pro are shown as selectable, but controlled Pilot admission rejects Pro; UI and server also disagree when balance equals the quote. | Product owner + Console + Control Plane | Choose Basic-only or admit Pro; align catalog, UI, server admission and equality-boundary tests. |
| `CONFLICT-RENEWAL-REACTIVATION-01` | `business_conflict` | `planned` | Renewal worker/state machine exists, but customer requests cannot enable auto-renew and expired users have no reactivation command. | Product owner + Control Plane settlement | Decide whether renewal is current scope; then either close the user flow with real billing/provider evidence or remove/demote unreachable claims. |
| `CONFLICT-LIFECYCLE-RESUME-01` | `semantic_conflict` | `planned` | Target Workspace lifecycle says Suspend/Resume; current `Resume` is administrator Launch recovery and user Workspace Resume is retired. | Architecture/product owner + Control Plane | Implement a distinct user lifecycle command or explicitly move it later; reserve `Launch Resume` for recovery terminology. |
| `CONFLICT-COMPOSE-PROFILE-01` | `clarified_boundary` | `resolved_in_docs` | Base Compose worker-disabled health and local Workspace overlay worker-enabled Docker authority look inconsistent when treated as one profile. | Cloud installation owner | Keep both profiles; acceptance must label base health as control-services-only and overlay qualification as Workspace evidence. |
| `CONFLICT-CLOUD-INSTANCE-RELEASE-ORDER-01` | `implementation_gap` | `next` | Target order is candidate -> Instance qualification -> formal Release, while current workflow builds and publishes in one dispatch. | Cloud release owner + Instance owner | Implement non-Release candidate and exact-byte promotion; do not publish a successor on the current sequence. |
| `CONFLICT-CLOUD-WORKSPACE-IMAGE-OWNER-01` | `authority_boundary` | `resolved_in_docs` | Cloud image, Workspace image and Instance image pins can be mistaken for one release artifact. | Cloud + App/Workspace + Instance owners | Keep Cloud Release responsible for Cloud image only; require a separately immutable Workspace image ref in local/Instance profiles. |
| `CONFLICT-EVIDENCE-CURRENTNESS-01` | `stale_projection` | `next` | Older roadmap wording says a local-Workspace Release and first Instance receipt are missing, while current evidence says those artifacts/receipt exist but clean-host/live and Acceptance B evidence remain open. | Roadmap owner + Status owner | Replace stale gap wording with clean-host/live installation, external Sub2API, Runtime readiness, Acceptance B and rollback evidence. |

## Product And Structural Gaps

| ID | State | Priority | Current gap | Owner boundary | Acceptance |
| --- | --- | --- | --- | --- | --- |
| `MVP-LOCAL-WORKSPACE-GATEWAY-01` | `next` | `P0` | Thin Console, one Control Plane Reconciler, typed Fabric stage bindings, a real `local-docker` adapter, Sub2API-backed balance/usage/Key/debit plus independent refund paths, an explicit local-Workspace Compose profile, and a durable owner-authorized no-refund Workspace Delete command exist. Required CI closes the Accounting source/evidence gap with real Control Plane HTTP plus PostgreSQL, a real Ledger HTTP process plus separate PostgreSQL, and a typed Sub2API authority fixture; it proves exactly-once stable debit, replay safety, one linked receipt, and fail-closed response-loss handling. The fixture is not a real external Sub2API, and the existing live smoke stopped at its authentication boundary. One complete live MacBook or single-server Console create/readback/open/delete path plus real external Sub2API authentication, balance, and usage readback remains unproven | Fabric provider port + Control Plane Launch/Delete coordination + Gateway/Sub2API authority + thin Console; Ledger records only required receipts and reconciliation | On a MacBook or single-server Docker host, Console creates, reads back, opens, and permanently deletes one OPL App/WebUI Workspace without a Delete wallet mutation through the single Control Plane Reconciler and `local-docker`; every resource stage uses the admitted Fabric binding; real external Sub2API authentication, balance, usage, debit, and any independently authorized refund remain authoritative and read back consistently; Tencent/TKE is neither exercised nor required |
| `CONSOLE-LAUNCH-CONSISTENCY-01` | `next` | `P0` | Basic and Pro are visible in the catalog and Console, but controlled Pilot admission accepts only Basic. The Console requires balance to be greater than the quote while the server accepts equality | Console offer projection + Control Plane admission policy | One owner decision defines the Pilot offer set; catalog, pricing, Console, server admission and user-facing errors agree, and focused tests cover both visible packages and the exact-balance boundary |
| `LOCAL-WORKSPACE-INSTALL-CONTRACT-01` | `next` | `P1` | The local Workspace overlay requires an immutable Workspace image, Docker socket, task-owned Secret root and gateway container, but the public environment template does not currently name every required overlay variable and the live smoke stopped before the complete journey | Portable release assets + Fabric local-Docker adapter | A clean operator using only admitted release assets can configure the overlay without an implicit qualification-only value; Compose validation and live readback prove Docker authority is limited to Fabric and the complete Workspace path is runnable |
| `OPS-ACCOUNT-IDENTITY-READBACK-01` | `next` | `P1` | Installation bootstrap binds a reserved operator to Sub2API, but clean-host external authentication, wallet, usage and account-scope readback remain unproven | Control Plane identity mapping + Sub2API authority | One clean installation reads back the same active external identity through login, Session, wallet and usage surfaces and fails closed on any ID, email, permission or wallet mismatch |
| `LOCAL-WORKSPACE-RECOVERY-READBACK-01` | `planned` | `P1` | Source and fixture tests preserve unknown debit/provider results, but a live local recovery path has not proved convergence, bounded cleanup and absence of foreign-resource mutation | Control Plane Launch recovery + Fabric readback + Ledger review | One exact Launch with an interrupted external/provider result converges from persisted identity and owner-authoritative readback; manual review records stage/budget/reason and cleanup touches only exact owned resources |
| `PRODUCT-RELEASE-01` | `next` | `P1` | Only `v0.1.7` remains public: hosted run `31879240411` published five verified assets and a `linux/amd64` + `linux/arm64` GHCR index from product SHA `a59bde68397528186a5220f73195fa1f3eda311b`; the owner removed historical `v0.1.0`-`v0.1.6` Releases, tags, and GHCR objects. The owner-only non-Release Candidate workflow and neutral receipt contract now exist at source level, but no Candidate has been dispatched or qualified. The formal Release workflow still rebuilds and publishes in one dispatch, so exact-byte promotion of the image already qualified by `opl-instance-medopl` remains absent | Cloud owns candidate/release mechanics and portable artifacts; `opl-instance-medopl` owns protected deployment, rollback, product acceptance, and the exact candidate receipt; only the repository owner admits publication | An exact canonical Cloud SHA produces a replaceable digest-addressed candidate without a Git tag, GitHub Release, or versioned GHCR tag; `opl-instance-medopl` deploys it and returns successful rollout and product-acceptance readback; the owner then manually publishes the same SHA and image digest without rebuilding, and failed development or deployment attempts create no formal version. No successor to `v0.1.7` is published before this path passes |
| `LEGACY-LAUNCH-MIGRATION-01` | `candidate` | `P3` | PR `#280`'s runtime candidate is not admitted. Fresh-main replay found a Linux-only test harness, retired Fabric readback interfaces, and a boundary mismatch where Fabric's `ready / legacy_partial_history` response omitted the explicit next-stage `absent` fact required by Control Plane. More importantly, no protected Instance inventory, workflow, or receipt proves that an eligible schema-2 `manual_review` row or active consumer exists, so a temporary migration API and state path have no current payer | Control Plane would own eligibility, exact-row CAS, and Resume; Fabric would own GET-only binding/provider facts; `opl-instance-medopl` owns protected inventory and production authorization; Sub2API remains a zero-mutation fact owner during admission | Trigger only after a protected Instance GET-only inventory proves at least one active candidate and binds it to exact persisted preflight/history, unique operations, canonical identities, provider resources, money, and remaining budgets. Then implement the smallest fresh-main path with cross-boundary partial/full-history tests, exact CAS readback, and immutable Resume authorization. If inventory is zero or no consumer remains, close the gap without runtime migration code; inventory performs no Fabric/provider or Sub2API mutation |
| `MODULE-COHESION-01` | `next` | `P1` | Control Plane Launch is one provider-neutral Reconciler with focused stage files, and retained Ent persistence is now separated into identity, resource, and Workspace capability files. Fabric `service.go` was reduced substantially; Tencent compute ownership and compute-allocation identity validation have adapter-private owners; and the retained Tencent provider is separated into compute, storage, and Runtime capability files. These slices preserve the existing receivers, interfaces, HTTP contracts, schemas, provider operations, and behavior under the complete local PostgreSQL/capacity/local-Docker gate. Shared persistence helpers, the remaining Tencent facade capabilities, and other provider/operator extensions still require caller-led cohesion work; no Spring Modulith, Cordis runtime, cross-service domain package, second registry, or global event bus is introduced | One owning Go module per change | Each remaining slice names a real caller and existing owner, reduces a measured mixed facade or duplicate responsibility, preserves public contracts and state behavior under focused plus complete local tests, and adds no shared policy/runtime framework without evidence of a missing capability |
| `FABRIC-PROVIDER-PROFILE-01` | `next` | `P1` | The independent Fabric Provider profile slice resolves Local-Docker and Tencent/TKE package infrastructure facts inside their adapters, persists an immutable plan binding and digest for Workspace Launch, and removes legacy Tencent package-shape fallbacks from Fabric orchestration, recovery, destroy, and readback. The remaining gap is canonical integration and Instance-owned deployment qualification; no live Tencent mutation is part of this Cloud change | `services/fabric` owns adapter/profile resolution and binding persistence; Control Plane/Console own only the provider-neutral launch flow and product package identity; `opl-instance-medopl` owns concrete production profile and qualification | The exact candidate passes the focused Fabric/Control Plane/contract gates, a replay survives Provider profile drift using the original binding, missing profile fails closed, PR #367 remains untouched, and Instance later reads back the same profile/binding facts without a second Workspace Reconciler |
| `INSTANCE-PROVIDER-ACCEPTANCE-MIGRATION-01` | `external_owner` | `P1` | Cloud Provider Facts delegates compute/storage/attachment/Runtime interpretation to Fabric adapters. Control Plane now consumes only provider-neutral monthly-preflight availability plus its own package, size, and zone facts; Instance-owned acceptance tools use canonical compute/storage provider IDs, while optional legacy `nodePoolId` / `persistentVolumeId` projections do not decide readiness or resource continuity. The explicit `OPL_TENCENT_ZONE` configuration remains an Instance/Fabric profile choice. The source cutover is complete; exact candidate deployment and provider readback remain external | `opl-instance-medopl` owns caller execution and protected production acceptance; Cloud owns the reusable provider-neutral contract, runtime, and adapter primitives; Tencent implementation/profile remains in the Fabric adapter and Instance owner | Instance `main` executes the absorbed Cloud contract from the protected workflow and reads back the same provider IDs and refs for an exact candidate; optional legacy projections remain diagnostic-only or are explicitly retired; normal Launch remains unchanged and no second Reconciler appears |
| `INSTANCE-MEDOPL-01` | `external_owner` | `P1` | The Tencent/TKE profile, protected production workflows, instance-specific tools/tests, and first deployment receipt now live in `opl-instance-medopl` `main`. The receipt proves a successful first TKE rollout and public health readback for `v0.1.7`, but the tracked profile remains `deployed_unverified`, runtime readiness is `ready=false`, and Acceptance B is incomplete. Cloud retains reusable candidates/releases and only its current release, Pages, and whitepaper GitHub deployment surfaces | `opl-instance-medopl` owns production authority, all medopl-specific deployment/acceptance/recovery/rollback tooling, and exact candidate receipts; Cloud owns reusable runtime, contracts/adapters, candidates/releases, and any later product fix | Instance production protection and Secret/variable owner refs remain established; the exact candidate's workflow, Environment, Deployment, Runtime, isolation, rollback, product acceptance, and redacted receipt read back consistently before a formal Cloud Release; no instance-specific tool source, GitHub environment, Deployment record, or accepted caller remains in Cloud |
| `WORKSPACE-RENEWAL-REACTIVATION-01` | `planned` | `P1` | Renewal worker and persisted billing states exist, but all new Launch and customer update paths reject enabling auto-renew, and an expired customer has no reactivation command | Control Plane settlement + Sub2API adapter + Fabric renewal/readback + Ledger receipts | Product owner either closes the customer authorization, renewal, suspension and reactivation flow with exactly-once live evidence, or removes/demotes the unreachable product claims and fields |
| `WORKSPACE-LIFECYCLE-CLOSURE-01` | `planned` | `P2` | Target Workspace Suspend/Resume actions have no customer route; the only current Resume is administrator-authorized continuation of a `manual_review` Launch | Console + Control Plane lifecycle owner | Implement distinct customer Suspend/Resume with provider readback and receipts, or move the actions to later target scope; no surface conflates Launch Resume with Workspace Resume |
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
| `SIMPLIFY-ACTIONS-REUSE-01` | `next` | `P2` | Qualification now reuses stable checkout, Node setup, PostgreSQL service, and Go-test pipeline YAML while preserving the four job identities and zero-skip gates; other workflow repetition remains separately scoped | medium | Consolidate only stable repetition with a semantic workflow test; do not rebuild a monolithic dispatcher |
| `SIMPLIFY-CP-FACADE-01` | `planned` | `P2` | The zero-caller `ReapplyWorkspaceRuntime` forwarding method, finite zero-caller `server/app_state` helpers, and the closed dead `PrepareWorkspace` orchestration chain are removed without changing historical rows or Fabric resources. `CreateWorkspaceInput` remains for its real Provider Acceptance caller. The real `Service` facade and capability boundaries remain; no broader caller-zero admission exists | medium | Remove only separately proven-zero forwarding or dead helpers, migrate real callers to owning capabilities, and preserve the real `Service` and capability boundaries without introducing an aggregate replacement facade |
| `SIMPLIFY-CLI-ARGS-01` | `later` | `P3` | Three tools use tool-local `node:util.parseArgs`; a further focused conversion expanded rather than simplified the retained surface, so remaining parsers stay local | low | Reconsider only when a real tool change removes more bespoke parsing than the explicit native option schema and compatibility tests add; do not add a shared CLI framework |
| `SIMPLIFY-STATIC-ASSETS-01` | `later` | `P3` | Native file delivery alone does not remove the custom request-time gzip branch, so the current static behavior remains | medium | Select one compression/build/edge owner and preserve cache, range, content type, and SPA behavior before deleting the custom branch |
## Evidence Gaps

Accounting source and required-CI evidence are closed by the real Control Plane
HTTP/PostgreSQL and Ledger HTTP/separate-PostgreSQL path with a typed Sub2API
authority fixture. The immediate evidence gaps are one complete live MacBook or
single-server Console create/readback/open/delete path, real external Sub2API
authentication plus balance and usage readback on that path, and clean-host
qualification of the already published local-Workspace profile. The first
Instance-owned medopl deployment receipt exists for `v0.1.7`, but Runtime
readiness remains `ready=false`, Acceptance B is incomplete, and exact-candidate
rollback/product qualification remain open. Self-service onboarding/payment,
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
