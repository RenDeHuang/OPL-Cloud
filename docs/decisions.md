# Decisions

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
current version, current stage, remaining mutation budget, reviewer, time, and
reason. Control Plane must persist it with compare-and-swap before the original
Reconciler can continue. Recovery cannot supply or rewrite resource IDs, reset a
budget, create a successor launch, or perform a Fabric/provider mutation.

The single Reconciler does not create a monolith. `services/control-plane` owns
only the business cursor, attempt/lease/CAS state, account and settlement
coordination, and customer projection. `services/fabric` owns compute, storage,
attachment, Secret binding, Runtime, its operation store, provider/Kubernetes
mutation, and authoritative resource readback. `services/ledger` owns append-only
receipt, evidence, review, reconciliation, and continuation references, but none
of those references can authorize or advance a Launch. Sub2API remains the
identity, wallet, Key, and Usage authority.

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

`one-person-lab-cloud` publishes the installable product: source, contracts,
multi-architecture GHCR image, GitHub Release, Compose assets, and reusable
provider adapters. Its release workflow uses no production environment and does
not deploy, diagnose, verify, or roll back a concrete installation.

`opl-instance-medopl` is the only medopl.cn customization and deployment owner.
Its `main` workflow and protected `production` environment select Tencent/TKE,
hold Secrets, consume an immutable Cloud product SHA and image digest, and own
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
