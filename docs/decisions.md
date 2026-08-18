# Decisions

## 2026-08-15: Pre-1.0 Instance Qualification Gates Product Publication

During the current immature pre-1.0 phase, a formal OPL Cloud Release is the
result of successful candidate adoption, not an input used to discover whether
the product can deploy. Cloud must first produce a replaceable candidate bound
to one exact canonical product SHA and image digest. `opl-instance-medopl` then
deploys and qualifies that candidate through its protected workflow. Only after
the required deployment and product acceptance readback succeeds may the
repository owner explicitly publish the same SHA and image bytes as a formal
Release.

This decision responds to observed version churn. Eight Releases, `v0.1.0`
through `v0.1.7`, were published in roughly 49 hours while successive changes
were still closing the same Acceptance B path. They were historical debugging
checkpoints, not eight independently qualified product handoffs. The repository
owner subsequently removed `v0.1.0` through `v0.1.6`; only `v0.1.7` remains on
the current GitHub Release, tag, and GHCR public surfaces.

Candidate and Release identities are distinct. Candidate CI output, assets, and
exact-SHA image tags may be replaced or discarded before qualification. A
formal Release must promote the exact candidate SHA and digest that the Instance
qualified; it must not rebuild different image bytes after the successful
deployment. Cloud therefore owns a separate owner-only Candidate workflow that
builds one `linux/amd64` image from an exact canonical SHA, reads back its
registry digest and revision, and emits the canonical neutral Candidate receipt
without creating a Git tag, GitHub Release, or versioned image tag. Exact-byte
promotion of an already qualified Candidate remains an explicit implementation
gap. Until that gap is closed and the Candidate has a successful Instance
receipt, no successor to `v0.1.7` is admitted.

The dependency is evidence-only and does not move authority between
repositories. Cloud owns reusable product source, candidate/release mechanics,
portable assets, and publication. `opl-instance-medopl` owns its environment,
Secrets, provider state, deployment, rollback, acceptance, and receipts. Cloud
does not dispatch or operate the Instance; the Product owner consumes its
exact-SHA/digest receipt as the pre-1.0 publication gate.

Only the repository owner may explicitly dispatch a formal Release from
`main`. PRs, merges, CI, schedules, collaborator actions, deployment retries,
and failed qualification do not publish a version. The create-only workflow
prevents accidental version reuse, but it does not remove the repository
owner's authority to repair or delete public artifacts through a separate
explicit cleanup decision. Documentation-only, test-only, CI-performance, and
Instance-only changes do not independently justify a Product Release.

## 2026-08-15: Keep The Go/TypeScript Service Architecture And Adopt Frameworks By Evidence

OPL Cloud keeps its current Go/TypeScript architecture. Control Plane, Fabric,
and Ledger remain separate Go modules, processes, and PostgreSQL schema owners;
Console remains a TypeScript browser application; cross-service integration
continues through typed public HTTP contracts. The current cohesion problem is
inside retained owner modules, not a missing application framework.

Spring Modulith is not adopted. Using it would require a Java replatform while
collapsing or duplicating process and persistence boundaries that already carry
real authority. OPL Cloud also does not add Cordis, Dapr, Temporal, a second
plugin registry, or a global event bus to make the architecture look more
uniform. Framework maturity alone is not evidence that the product needs the
framework's runtime model.

The near-term architecture work is deliberately smaller: split large retained
implementation files by their existing capability owners, preserve the public
interfaces and behavior, and expose one repeatable local verification entry.
Reconsider a framework only when a real caller and observed failure show a
specific missing capability, such as durable recovery across process restarts,
runtime plugin installation/isolation, or repeated service-infrastructure
duplication, and a focused replacement proves measurable benefit without
creating a second authority.

## 2026-08-14: Framework Cordis Composition Stops At The Cloud API Boundary

OPL Framework is adopting Cordis for in-process composition. OPL Cloud does not
follow that change as a repository-wide or service-runtime migration. Cordis
addresses Framework process composition, dependency injection, events, effects,
and teardown; Cloud owns independently deployed service, persistence, provider,
billing-coordination, and evidence authorities.

The supported integration is a Framework-owned Cordis plugin that wraps a typed
Cloud client and calls the existing public HTTP and capability contracts.
Control Plane, Fabric, and Ledger remain Cloud authorities. Fabric provider
adapters remain behind the native Fabric provider port, and the Instance owner
continues to select the concrete provider profile and deployment.

OPL Cloud will not add a Cordis dependency or sidecar, mirror Framework plugin
state, or create a second plugin registry, installed lock, event log, or service
lifecycle. Cordis plugin and composition versions do not authorize arbitrary
mixing of Cloud service binaries or schemas; the Cloud product remains one
intentional release unit with compatibility enforced at its typed contracts.

Reconsidering Cordis inside a future Cloud process requires a new explicit
decision backed by a real in-process caller and a verified replacement,
isolation, diagnosis, or teardown outcome. Framework migration alone is not that
evidence.

## 2026-08-11: Modularity And Deletion-First Work Proceed In Parallel

The immediate execution portfolio gives module cohesion, physical deployment
isolation, contract-owner slimdown, and deletion-first simplification the same
high-leverage `P1` attention as other accepted structural work. This promotes
`MODULE-COHESION-01` and `DEPLOY-ISOLATION-01` to `next/P1` without making either
one a prerequisite for the P0 local Workspace vertical.

Implementation is divided by physical owner and exact write set: Fabric Core,
Control Plane Launch coordination, thin Console, cross-module contracts, legacy
Recovery tooling, and deployment configuration may progress independently.
Only one owner changes a shared public contract or the same large source file at
a time, and canonical `main` integration remains serialized.

Simplification is deletion-first rather than refactor-first. The unreachable
Recovery CLI may proceed once its accepted workflow callers are proven. Archive,
shared-execution, ContentTransfer, and Snapshot/Restore candidates begin with
read-only caller, persisted-data, external-consumer, and cleanup-obligation
admission. If admitted for deletion, their implementation returns to the owning
Control Plane or Fabric lane; a second writer does not first modularize code that
should be removed.

Internal cohesion work splits only retained live capabilities inside their
existing module and package. It does not create a shared domain package, a
universal contract, a second Reconciler, or a cross-service implementation
import. Deployment isolation remains independently deliverable and does not
force separate product images without measured release-blast-radius evidence.

## 2026-08-11: MVP Core Is Local Workspace Plus Gateway Accounting

OPL Cloud MVP has one Core vertical path: a thin Console for essential
Workspace, balance, and usage management; a real
`Console -> Control Plane -> Workspace launcher/provider -> local Docker`
OPL App/WebUI Workspace lifecycle; and Sub2API-owned balance, usage, debit, and
refund authority with minimal Ledger receipts and reconciliation evidence.

Self-service signup, payment/top-up, detailed UI refinement, OPL Serve, managed
resource policy, generic Kubernetes, and nonessential Ledger evidence verticals
are extensions or later work. Tencent/TKE is an extension adapter selected by
`opl-instance-medopl`, not an OPL Cloud MVP prerequisite. Compose startup of the
three control services does not count as a local Docker Workspace provider.

## 2026-08-11: One Durable Launch, Separate Physical Owners

One Workspace Launch has one durable Control Plane operation and business state
machine. Create and Resume enter the same Reconciler. The durable chain is
`preflight -> key -> debit -> ensure compute allocation -> storage -> attachment
-> secret -> runtime -> activation -> receipt -> succeeded`; preflight is the
read-only admission gate before the first external write, and a Workspace URL is
Runtime-authoritative readback/projection rather than a mutation stage.

Recovery is not a second state machine or resource writer. It may persist one
immutable Resume authorization for the original launch, bound to the launch ID,
current version, current stage, independent mutation/idempotent-replay/read
budgets, a server-bound readback baseline, reviewer, time, and reason. The
read budget is finite operator authorization for typed owner continuation
evidence, not an elapsed-time or polling inference; exhaustion is
`unknown/manual_review`, never absence. Control Plane must persist it with
compare-and-swap before the original Reconciler can continue. Legacy schema-v3
rows missing these fields default to zero. Recovery cannot supply or rewrite
resource IDs, reset `Attempted`, raise `Max`, create a successor launch, or
perform a Fabric/provider mutation directly.

The single Reconciler does not create a monolith. `services/control-plane` owns
only the business cursor, attempt/lease/CAS state, account and settlement
coordination, and customer projection. `services/fabric` owns compute, storage,
attachment, Secret binding, Runtime, its operation store, provider/Kubernetes
mutation, and authoritative resource readback. `services/ledger` owns append-only
receipts, reconciliation, idempotency, and caller-owned opaque provenance; none
of those refs can authorize or advance a Launch. Control Plane's typed
continuation authorization remains separate. Sub2API remains the identity,
wallet, Key, and Usage authority.

Control Plane calls Fabric through a typed public HTTP contract carrying an
explicit, immutable, provider-neutral launch/stage operation binding. Fabric
persists that binding before a provider write and returns it with readback;
provider adapters map it to Machine, CVM, Node, CBS, Runtime, and other provider
identities. Control Plane must not infer ownership from an idempotency-key suffix,
an unscoped operation listing, provider tags, or provider-specific fields.

Any legacy launch migration is a bounded Control Plane state migration, not a
new launch. Only quiesced `manual_review` schema-2 rows are eligible. Migration
uses owner-authoritative GET-only facts, deterministic mapping, exact-row/result
CAS, and post-write Control Plane readback while preserving all original
identity, money, resource, idempotency, billing-period, and attempt-budget facts.
Anything missing, conflicting, unknown, or not exactly preservable remains in
`manual_review` with zero provider or wallet mutation and still requires the
immutable Resume authorization.

## 2026-08-11: Product Release And Instance Deployment Are Separate

This decision separates repository authority; it no longer defines the
pre-1.0 publication order. The 2026-08-15 candidate-qualification decision
above requires the Instance to qualify an exact candidate before Cloud
publishes that candidate as a formal Release.

`one-person-lab-cloud` publishes the installable product: source, contracts,
multi-architecture GHCR image, GitHub Release, Compose assets, and reusable
provider adapters. Its release workflow uses no production environment and does
not deploy, diagnose, verify, or roll back a concrete installation.

`opl-instance-medopl` is the only medopl.cn customization and deployment owner.
Its `main` workflow and protected `production` environment select Tencent/TKE,
hold Secrets, consume an exact Cloud product SHA and image digest, and own
deployment, canary, rollback, and receipts. Product source is never copied into
the Instance repository, and Instance state is never written back into Cloud.

## 2026-08-11: One Product Repository And Explicit Instances

`one-person-lab-cloud` is the single product and implementation owner for the
OPL Cloud architecture, whitepaper, roadmap, Console, Control Plane, Fabric,
Ledger, Workspace delivery, contracts and reusable release mechanisms. The
transferred implementation repository keeps its GitHub identity and history,
then takes this canonical name. The former documentation repository becomes a
read-only archive after its current product truth and Pages path are absorbed.

`opl-cloud` remains the short package, image, binary, service, namespace,
environment-variable and runner identifier. It is not a second repository.
Earlier standalone Console, Fabric, Ledger and deployment repositories are
prototypes or history, not parallel current writers.

A concrete installation is an instance, not a deployment-code fork. The first
commercial instance is `opl-instance-medopl`. It owns medopl domains, provider
profile, enabled plans and prices, image pins, secret references, promotion
policy, and deployment receipts while consuming immutable
`one-person-lab-cloud` releases.

An account may own zero or more independent Workspaces. There is no fixed
product-level count limit; each creation remains subject to balance, provider
capacity, quota, and policy. Each Workspace owns independent identity,
resources, credentials, billing period, and receipts.

Fabric's target contract is provider-neutral. Tencent TKE is the first adapter,
not the product definition. Launch and recovery share one Control Plane state
machine; provider-specific facts and mutations stay in the adapter.

Gateway/Sub2API remains the only spendable wallet. Console owns the account-total
billing projection, pricing, and settlement policy. Fabric has no balance, and
Ledger records immutable billing and reconciliation evidence.

## 2026-07-14: Sub2API Is The Only Spendable Balance

Sub2API owns USD balance, API keys, models, routing, and request usage. Control
Plane uses the configured server-only management origin for Session-authorized
product operations. Console displays authoritative readback and Ledger stores
evidence only. The browser never receives the internal origin.

## 2026-07-14: Resources Are Prepaid Monthly

Basic and Pro are Workspace packages priced as fixed integer USD micros. Each
purchase or renewal creates one debit for the package total. Tencent compute and
storage costs remain internal evidence and never become separate customer
charges.

## 2026-07-14: Control Plane Serves Console Product Commands Only

Control Plane orchestrates product outcomes. It does not expose generic Fabric,
Ledger, or Sub2API proxies and does not enter App, Workspace runtime, or MAS
direct call chains.

## 2026-07-19: Hard Cut After Caller Migration

Inventory current callers first, migrate them, then delete old routes, DTOs,
field consumers, fallbacks, and non-authoritative truth. Old product routes
return 404 without a compatibility layer. Executed migrations, historical
billing, Receipts, Ledger evidence, and Git history are never deleted or
rewritten; non-terminal legacy operations must be cleared or handled manually
before cutover.

## 2026-07-14: Provider And Commercial State Have Different Owners

Fabric owns resource/provider facts. Control Plane owns monthly entitlements and
billing operations. Ledger owns append-only evidence. A Fabric response must not
replace Control Plane commercial fields.

## 2026-07-16: Reusable Verification Replaces Per-Run Paid Provisioning

The legacy paid verifier is blocked and is not a release gate. Ordinary CI and
commercial E2E use fake monthly settlement and provider mutations. Runtime E2E
reuses one prepaid `SA5.MEDIUM4` plus 10GB CBS Verification Slot for its paid
period and deletes only temporary workloads and test data. A real provider
purchase or renewal requires a separate explicit Provider Acceptance run.

## 2026-07-21: Public Gateway And Management Endpoints Stay Separate

Control Plane projects the configured public `/v1` endpoint and Console may
present it as text, a copy target, or an external link according to the current
UX. The durable security boundary is different: Console never exposes, links,
redirects to, embeds, scrapes, or calls the Sub2API management origin from the
browser. `OPL_SUB2API_BASE_URL` stays server-only, and Cloud does not inject a
second Runtime Gateway base URL.

## 2026-07-19: Evidence Levels Cannot Be Inferred

`code-complete` requires the local machine-enforced full gate. `pilot-ready`
requires separately approved real Pilot readback. `production-proven` requires
the same immutable revision deployed with production evidence. A lower level
never implies a higher one.

## 2026-08-17: Control Plane Owns Workspace Purchase Eligibility

Sub2API identity and spendable balance, a local Cloud Account, and permission to
buy a new Workspace are separate facts. A Gateway-only account may authenticate
through the Gateway but cannot purchase a Workspace. Operator provisioning must
select either `full_cloud_customer` (eligibility enabled) or `gateway_only`
(eligibility disabled); reusing an existing Sub2API identity does not change the
selected product scope.

`workspacePurchaseEnabled` on the Control Plane Account is the only current
purchase-eligibility authority. Grant and revoke are explicit operator actions,
audited with actor, reason, before, after, and account identity. Revocation blocks
new purchases only and never deletes or changes existing Workspaces. Historical
accounts remain disabled until a product-approved migration inventory is read
back. The Instance per-account pilot allowlist is not removed until that
Control Plane migration and readback are complete.
