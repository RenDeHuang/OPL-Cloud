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
work, and broader managed-platform capabilities are intentionally later. The
local customer vertical remains `P0`; the current explicit Release objective
also makes `PRODUCT-RELEASE-01` and the external `INSTANCE-MEDOPL-01`
qualification gate `P0` until one dual-path Release is admitted. The typed
Control Plane-to-Fabric launch binding is implemented baseline rather than a
second product lane.

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
customer signs in with a Control Plane Session, reads the authoritative Sub2API
balance and usage projection, selects an available Cloud-defined Workspace
package, confirms one prepaid monthly charge, and submits one durable Workspace
Launch. Control Plane owns the versioned price catalog, customer-facing operation
and commercial coordination; the single Reconciler calls Fabric for
provider-neutral resource stages; Fabric performs provider mutations and
readback; Ledger records receipts and reconciliation evidence; Sub2API remains
the only spendable wallet, Key, routing, and usage authority.

The current Pilot does not provide public registration, customer payment/top-up,
shared multi-user Workspaces, or a user-facing Workspace Suspend/Resume flow.
The administrator-only `Launch Resume` command is a bounded continuation of a
`manual_review` Launch and is not the same business action as restoring a
suspended Workspace.

| Actor or authority | Owns | Must not own or imply |
| --- | --- | --- |
| Customer / Workspace owner | Account-scoped commands, package choice, access, usage and billing views, Workspace delete | Wallet truth, provider IDs, or another account's Workspace |
| Console | Presentation and calls to Control Plane product APIs | Pricing decisions, persistence, provider mutation, wallet, Ledger truth, or direct Sub2API management access |
| Control Plane | Sessions, account policy, versioned pricing, Workspace Launch/Delete cursor, entitlement, settlement coordination, customer DTOs | Provider resources, spendable balance, or Ledger evidence truth |
| Fabric | Provider Profile resolution, compute, storage, attachment, Secret binding, Runtime, adapter mutation and authoritative resource readback | Customer balance, customer pricing, or account policy |
| Ledger | Append-only receipts, evidence, reconciliation and caller-owned opaque refs | Pricing, balance mutation, provider mutation, or Launch authorization |
| Sub2API | Spendable USD balance, API Keys, model routing and request usage | Workspace lifecycle, provider resources, or Cloud receipts |
| Cloud product owner | Portable source, adapters, contracts, multi-architecture image, candidate, install assets and formal Release | medopl domains, private Workspace image, Tencent resources, production Secrets, deployment, rollback or Instance receipts |
| Local installer | Local-Docker Provider Profile, immutable Workspace image, Docker/Secret roots and local qualification receipt | Tencent profile or Cloud customer-pricing policy |
| `opl-instance-medopl` | `.com` domains, Tencent/TKE Provider Profile, enabled Cloud-plan subset, immutable Workspace image, production Environment/Secrets, deployment, generic qualification, rollback and receipts | Cloud customer prices, copying Cloud runtime source, or becoming a second Cloud product owner |

## Release Objective Reference

The portable dual-path Release target and authority boundaries are owned by
[decisions.md](./decisions.md) and [architecture.md](./architecture.md). For
planning purposes, `PRODUCT-RELEASE-01` closes only when one exact
multi-architecture Cloud Candidate has separate successful supported
Local-Docker and Tencent/TKE qualification receipts and formal publication
promotes that existing Cloud digest. This paragraph references the target; it
does not redefine installation inputs, domains, Provider Profiles, or release
identity.

## Module And Lane Map

This map assigns one primary owner to each lane. Consuming modules may be named,
but they do not become another lane or SSOT writer.

| Primary owner | Consuming modules | Current lanes | Disposition for the dual-path Release |
| --- | --- | --- | --- |
| Control Plane | Console, Fabric client, Ledger client, Sub2API client | `MVP-LOCAL-WORKSPACE-GATEWAY-01`, `CONSOLE-LAUNCH-CONSISTENCY-01`, `OPS-ACCOUNT-IDENTITY-READBACK-01`, `LOCAL-WORKSPACE-RECOVERY-READBACK-01`, `WORKSPACE-RENEWAL-REACTIVATION-01`, `WORKSPACE-LIFECYCLE-CLOSURE-01`, `CONSOLE-SELF-SERVICE-01`, `BILLING-EVIDENCE-01`, `MANAGED-POLICY-01`, `LEGACY-LAUNCH-MIGRATION-01`, `SIMPLIFY-CP-FACADE-01` | The first three lanes close the customer-side Release path. Acceptance B hard-cut stays in `SIMPLIFY-CP-FACADE-01`; renewal, lifecycle, self-service, billing expansion, managed policy and legacy migration do not block this Release without a live obligation. |
| Console | Control Plane product API | No independent current lane; Console is the presentation consumer in Control Plane-owned lanes | Keep the thin projection and align offer/admission behavior; never move pricing, persistence, provider, wallet or Ledger authority into Console. |
| Fabric | Control Plane, local installer, Instance | `FABRIC-PROVIDER-PROFILE-01`, `RESOURCE-BINDING-01`, `SECURITY-SCAN-REMEDIATION-04` | Remove installation-specific defaults, keep both providers behind typed adapters, and close only the provider-profile slice needed by this Release. |
| Ledger | Control Plane | No independent current lane | Record purchase/deletion receipts and accepted price snapshots for the consuming Control Plane lanes; do not price or mutate wallet balance. |
| Gateway / Sub2API | Control Plane | No Cloud lane or service; it is the external authority consumed by Control Plane-owned lanes | Keep Sub2API as the only wallet, Key, routing and usage owner; do not add a second Gateway. |
| Contracts | Control Plane, Fabric, Ledger, tools | `CONTRACT-DEDUP-02` | Remove the second mutable price catalog and give the fixed Runtime port one versioned ABI owner; do not create pricing-conflict or port-specific lanes. |
| Release and local install | All Cloud modules, local installation owner, Instance | `LOCAL-CONTROL-SERVICES-01`, `LOCAL-WORKSPACE-INSTALL-CONTRACT-01`, `PRODUCT-RELEASE-01` | Build one multi-architecture Candidate, qualify that exact digest on both paths, and promote without rebuild. |
| `opl-instance-medopl` | Cloud Candidate and reusable Fabric adapter | `INSTANCE-MEDOPL-01` | Own `.com`, Tencent profile/image, deployment, generic admission, post-activation readback, executed rollback and receipts. Completed provider-acceptance migration remains folded here. |
| Per owning module / developer tooling | Real callers only | `MODULE-COHESION-01`, `SIMPLIFY-ACTIONS-REUSE-01`, `SIMPLIFY-CLI-ARGS-01`, `SIMPLIFY-STATIC-ASSETS-01` | Proceed only in owner-scoped slices; none is a blanket Release gate. |
| GitHub / repository security owner | Development workflows and affected service | `SECURITY-GITHUB-AGENT-PILOT`, `SECURITY-SECRET-VALIDITY` | Remain separate security/tooling decisions and do not become product Release gates without a release-impacting finding. |
| External or later product owners | Cloud projections only | `WORKSPACE-CONTINUITY-01`, `PACKAGE-PROJECTION-01`, `CONNECTOR-01`, `EVIDENCE-CONTINUATION-01`, `WORKSPACE-ROUTER-01`, `SERVE-01`, `RUNWAY-01` | Keep outside the current dual-path Workspace Release. |

## Journey References

- Customer: `OPS-ACCOUNT-IDENTITY-READBACK-01` ->
  `CONSOLE-LAUNCH-CONSISTENCY-01` -> `MVP-LOCAL-WORKSPACE-GATEWAY-01`.
- Local installation: `LOCAL-CONTROL-SERVICES-01` ->
  `LOCAL-WORKSPACE-INSTALL-CONTRACT-01` ->
  `MVP-LOCAL-WORKSPACE-GATEWAY-01`.
- Tencent/TKE: `FABRIC-PROVIDER-PROFILE-01` -> `INSTANCE-MEDOPL-01`.
- Publication: both qualification receipts -> `PRODUCT-RELEASE-01` -> exact-byte
  formal Release.

For this specific dual-path Release, the blocking set is the four P0 rows plus
the release slices of `OPS-ACCOUNT-IDENTITY-READBACK-01`,
`LOCAL-CONTROL-SERVICES-01`,
`LOCAL-WORKSPACE-INSTALL-CONTRACT-01`, `FABRIC-PROVIDER-PROFILE-01`,
`CONTRACT-DEDUP-02`, and `SIMPLIFY-CP-FACADE-01`. The remaining P1/P2/P3 rows do
not become Release gates unless fresh owner evidence proves they affect the
exact Candidate.

## Conflict Reconciliation

Conflict aliases are not independent lanes. This inventory contains only open
target-to-implementation or implementation-to-implementation conflicts. Each is
handled by its existing lane; resolved history belongs in Git history or
[status.md](./status.md), not in this table.

| Question | Current disposition | Owning lane |
| --- | --- | --- |
| Basic/Pro visibility and exact-balance admission disagree | Still open; align the offer set and equality behavior | `CONSOLE-LAUNCH-CONSISTENCY-01` |
| Renewal implementation is unreachable from current customer commands | Still open; finish or demote the feature | `WORKSPACE-RENEWAL-REACTIVATION-01` |
| Target Suspend/Resume differs from operator Launch Resume | Still open; implement a distinct lifecycle or move it later | `WORKSPACE-LIFECYCLE-CLOSURE-01` |
| Candidate qualification and Release publication order differ from the current workflows | Qualify the portable multi-architecture Candidate on both paths and promote its exact bytes after both receipts | `PRODUCT-RELEASE-01` |
| Control Plane price code and the pricing JSON both carry exact current amounts | Keep exact prices in one versioned Control Plane runtime catalog; contracts retain schema/invariants | `CONTRACT-DEDUP-02` |
| Port `3000` is a fixed Runtime ABI but a current environment variable implies arbitrary configurability | Give the ABI one versioned cross-module owner and remove the false configuration surface | `CONTRACT-DEDUP-02` |
| Cloud still provides medopl domain/image fallbacks although installations own those inputs | Require explicit profile/domain/image inputs and fail closed | `FABRIC-PROVIDER-PROFILE-01` |
| Acceptance B is retired target intent but source/config/persisted projections still consume it | Prove zero external consumer, zero configured environment and zero non-terminal persisted dependency, then hard-cut it | `SIMPLIFY-CP-FACADE-01` |

## Product And Structural Gaps

| ID | State | Priority | Current gap | Owner boundary | Acceptance |
| --- | --- | --- | --- | --- | --- |
| `MVP-LOCAL-WORKSPACE-GATEWAY-01` | `next` | `P0` | No one release-qualified clean-host run binds external identity/wallet/usage, Workspace create/readback/open/restart, permanent no-refund Delete, final resource/Key absence and the deletion Receipt to one exact Candidate. The qualification tool still expects retired refund-on-Delete semantics | Control Plane owns Launch/Delete and commercial coordination; Fabric owns Local-Docker resources; Sub2API owns wallet/Key/usage; Ledger owns purchase/deletion receipts; Console projects | On one supported clean Linux Docker host, the same Candidate completes login, quote/debit, create/readback/open, restart, permanent Delete, final resource/Key absence, unchanged post-debit wallet history, and exactly one `workspace.deleted.v1` receipt. Delete performs zero wallet or refund mutation, and the qualification tool/tests match that contract |
| `CONSOLE-LAUNCH-CONSISTENCY-01` | `next` | `P0` | Basic and Pro are visible in the catalog and Console, but controlled Pilot admission accepts only Basic. The Console requires balance to be greater than the quote while the server accepts equality | Console offer projection + Control Plane admission policy | One owner decision defines the Pilot offer set; catalog, pricing, Console, server admission and user-facing errors agree, and focused tests cover both visible packages and the exact-balance boundary |
| `LOCAL-CONTROL-SERVICES-01` | `next` | `P1` | The base Compose profile starts PostgreSQL, Ledger, Fabric and Control Plane without Workspace host authority. Current Release assets can prove only this control-services profile | Cloud installation assets + Control Plane/Fabric/Ledger process owners | The Candidate install bundle starts with independent credentials and schemas, health/readback succeeds, and the receipt explicitly says that control-service health is not Workspace qualification |
| `LOCAL-WORKSPACE-INSTALL-CONTRACT-01` | `next` | `P1` | The public environment template still embeds a Workspace image value, and the Secret-root ownership/permission contract has no successful clean-host qualification for the exact Candidate | Portable install assets + Fabric Local-Docker adapter | Candidate assets contain no installation-specific image/domain default; a clean operator supplies exact images/profile, Docker authority reaches only Fabric, host requirements fail closed, and the full Workspace path runs without qualification-only hidden values |
| `OPS-ACCOUNT-IDENTITY-READBACK-01` | `next` | `P1` | The final clean-host lifecycle receipt does not yet bind the same external identity through login, Session, wallet and usage readback | Control Plane identity mapping + Sub2API authority | One Candidate-qualified installation reads back the same active external identity through login, Session, wallet and usage surfaces and fails closed on any ID, email, permission or wallet mismatch |
| `LOCAL-WORKSPACE-RECOVERY-READBACK-01` | `planned` | `P1` | Source and fixture tests preserve unknown debit/provider results, but a live local recovery path has not proved convergence, bounded cleanup and absence of foreign-resource mutation | Control Plane Launch recovery + Fabric readback + Ledger review | One exact Launch with an interrupted external/provider result converges from persisted identity and owner-authoritative readback; manual review records stage/budget/reason and cleanup touches only exact owned resources |
| `PRODUCT-RELEASE-01` | `next` | `P0` | The source-level Candidate path now defines one multi-architecture Cloud index, both child digests, and a checksum-bound portable install bundle without deployment facts. No local/Instance receipt pair yet qualifies one such Candidate, and the formal workflow still rebuilds instead of promoting its exact bytes | Cloud owns multi-architecture Candidate/install bundle and publication; local qualification owns its image/profile receipt; `opl-instance-medopl` owns `.com`/Tencent profile, deployment, generic qualification and rollback receipt; only the Cloud Release publisher allowlist (repository owner or `RenDeHuang`) publishes | One current-main SHA produces one multi-architecture Cloud index with child digests and portable assets. Local-Docker and Tencent/TKE qualify that same index using separate deployment-owned Workspace images/profiles. Purchase/operation/receipt evidence freezes Candidate identity and full package/amount/billing/provider fulfillment. Formal publication only promotes the existing index digest, verifies both receipts, and creates no rebuilt image. No successor to `v0.1.7` is published before this passes |
| `LEGACY-LAUNCH-MIGRATION-01` | `candidate` | `P3` | No protected Instance inventory proves an eligible schema-2 `manual_review` row or active consumer, so a temporary migration API/state path has no current payer. The proposed path must also use current Fabric readback contracts and return the explicit next-stage fact required by Control Plane | Control Plane would own eligibility, exact-row CAS, and Resume; Fabric would own GET-only binding/provider facts; `opl-instance-medopl` owns protected inventory and production authorization; Sub2API remains a zero-mutation fact owner during admission | Trigger only after a protected Instance GET-only inventory proves at least one active candidate and binds it to exact persisted preflight/history, unique operations, canonical identities, provider resources, money, and remaining budgets. Then implement the smallest fresh-main path with cross-boundary partial/full-history tests, exact CAS readback, and immutable Resume authorization. If inventory is zero or no consumer remains, close the gap without runtime migration code; inventory performs no Fabric/provider or Sub2API mutation |
| `MODULE-COHESION-01` | `next` | `P1` | Shared persistence helpers, remaining Tencent facade capabilities and other provider/operator extensions still mix distinct real-caller responsibilities inside their owning modules | One owning Go module per change | Each remaining slice names a real caller and existing owner, reduces a measured mixed facade or duplicate responsibility, preserves public contracts and state behavior under focused plus complete local tests, and adds no shared policy/runtime framework without evidence of a missing capability |
| `FABRIC-PROVIDER-PROFILE-01` | `next` | `P1` | Cloud still contains medopl Workspace-domain fallbacks, a private Tencent Workspace image repository, a Local-Docker image-repository default and medopl-specific persisted Kubernetes metadata keys. Control Plane proxy routing also consumes its own medopl hostname fallback instead of one explicit installation fact | Fabric owns adapter/profile resolution, trust validation and binding persistence; Control Plane consumes the typed Workspace host/runtime fact for gateway routing; installer/Instance owns the concrete profile, domain, image and provider resources | Missing explicit managed profile/domain/image fails closed; Control Plane and Fabric consume one admitted hostname/runtime fact; Local-Docker and Tencent/TKE pass focused contracts for the same Candidate; persisted metadata keys are inventoried and migrated once without permanent dual-read behavior |
| `INSTANCE-MEDOPL-01` | `external_owner` | `P0` | No Instance receipt set binds successful generic admission, an owner-authoritative normal purchase, post-activation Runtime/provider/billing readback, an actually executed verified rollback and `workspace_verified` to the same portable Candidate | `opl-instance-medopl` owns production authority, `.com`/profile/image inputs, deployment, generic admission/post-activation readback, rollback and receipts; Cloud owns reusable runtime/contracts/adapters and the Candidate | The same multi-architecture Cloud Candidate qualified locally is deployed with explicit `.com`, TCR Workspace image and Tencent profile; protected readback proves rollout, canonical provider IDs, Runtime, isolation, one normal purchase, billing, executed rollback and `workspace_verified`. No Acceptance B input or semantics remains |
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
Closed finding evidence and the current alert inventory belong in
[status.md](./status.md); only admitted open work remains below.

| ID | Class | Priority | Current gap | Owner boundary | Acceptance |
| --- | --- | --- | --- | --- | --- |
| `SECURITY-GITHUB-AGENT-PILOT` | `candidate` | `P3` | GitHub `Agents` is available as a Copilot cloud-agent task/PR surface, but this repository already has a canonical Codex lifecycle and no demonstrated need for a second autonomous writer or scheduled automation | Development workflow only; no release, deployment, Secrets, production, product-Agent, or domain-agent authority | Adopt only after a narrow documentation/test-maintenance pilot proves unique ownership, PR-only output, least tools, branch-protection compliance, bounded cost, and no duplicate lifecycle; otherwise retain no repository agent profile or automation |
| `SECURITY-SECRET-VALIDITY` | `external_owner` | `P2` | Secret scanning and push protection are enabled, but the repository setting continues to report `secret_scanning_validity_checks=disabled` after a write attempt | GitHub feature/plan availability and repository owner settings | GitHub reports validity checks enabled, or owner-authoritative documentation confirms the feature is unavailable for this repository; until then no completion claim is made |
| `SECURITY-SCAN-REMEDIATION-04` | `in_review` | `P1` | One current low-severity Fabric resource-exhaustion finding remains open: authenticated heartbeat, Runtime status and operation-list requests use unbounded shared history. A bounded lookup/heartbeat/pagination candidate is not yet canonical or re-scanned | Fabric owns operation persistence and HTTP pagination; Control Plane/runner transport and lease authority remain unchanged; production callers consume the bounded Fabric contract without adding a second history owner | Fresh canonical `main` readback plus a sealed scan no longer reports `resource-exhaustion.fabric-operation-history`; focused memory and PostgreSQL store tests prove point lookup, duplicate fail-closed behavior, bounded heartbeat cardinality, Runtime identity bounds, and cursor pagination; caller tests prove complete multi-page readback and repeated-cursor rejection. Platform, Instance adoption, and production claims remain separately owner-authoritative |

## Phased Contract Slimdown

Completed contract-retirement evidence belongs in [status.md](./status.md). The
remaining open contract phase is:

| ID | State | Priority | Phase and scope | Safety retained | Acceptance |
| --- | --- | --- | --- | --- | --- |
| `CONTRACT-DEDUP-02` | `next` | `P1` | Exact current price values are duplicated between Control Plane runtime code and `opl-cloud-pricing-contract.json`. Runtime port `3000` is repeated across Control Plane/Fabric and `OPL_WORKSPACE_WEBUI_PORT` implies arbitrary configuration although the current implementation accepts only `3000` | public APIs, accepted price snapshots, Runtime compatibility, security, integrity, permissions, irreversible side effects | Control Plane has one versioned runtime price catalog and accepted Workspaces retain immutable snapshots; cross-module contracts describe price schema/invariants without a second mutable catalog. One versioned Workspace Runtime ABI contract owns port `3000`; Control Plane proxy and both Fabric adapters consume or validate that fact, the false environment option is removed, and focused tests enforce the ABI without a new lane |

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
| `SIMPLIFY-CP-FACADE-01` | `candidate` | `P1` | Earlier zero-caller forwarding/helpers are removed. The remaining release-relevant residue is the Acceptance B fresh-launch bypass, account reconciliation route, special resume prepare/header/env/binding, `acceptanceBCapacitySlot`, and matching machine-contract/test surface. Generic controlled-Pilot policy, normal purchase, normal operator Resume and the single Reconciler remain | medium | Instance protected readback first proves zero configured Acceptance B environment and zero external consumer; Control Plane inventory proves zero non-terminal persisted dependency on the special fields. Then hard-cut the route/header/env/contract/application fields in one Control Plane-owned change; retain historical migrations/rows as read-only custody and add no compatibility branch or new lane |
| `SIMPLIFY-CLI-ARGS-01` | `later` | `P3` | Three tools use tool-local `node:util.parseArgs`; a further focused conversion expanded rather than simplified the retained surface, so remaining parsers stay local | low | Reconsider only when a real tool change removes more bespoke parsing than the explicit native option schema and compatibility tests add; do not add a shared CLI framework |
| `SIMPLIFY-STATIC-ASSETS-01` | `later` | `P3` | Native file delivery alone does not remove the custom request-time gzip branch, so the current static behavior remains | medium | Select one compression/build/edge owner and preserve cache, range, content type, and SPA behavior before deleting the custom branch |

## Evidence Gaps

The open Release evidence outcomes are the acceptance clauses of the three P0
lanes: one exact-current supported Local-Docker lifecycle receipt, one Instance
generic admission/post-activation/executed-rollback receipt set for the same
Candidate, and exact-byte formal promotion. `FABRIC-PROVIDER-PROFILE-01` and
`LOCAL-WORKSPACE-INSTALL-CONTRACT-01` supply their required portable inputs.
Acceptance B is retired intent and its remaining Cloud implementation is a
hard-cut task, not qualification evidence.

Exact run IDs, SHA/digests, observed failures and historical successes belong
only in [status.md](./status.md). A new run updates that snapshot, not this
Roadmap, unless it closes or changes one of the gaps above.

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
4. Local and Instance qualification bind one exact Candidate SHA and
   multi-architecture Cloud index digest. Their receipts separately bind the
   selected Workspace image, Provider Profile, domain, runtime and authoritative
   readback; formal publication promotes the same Cloud bytes.
5. A failed runtime or production evidence lane stays open without rolling
   unrelated local development backward.
6. Instance production authority is established and read back before any
   persisted medopl metadata contract is migrated. Cloud owns no Instance
   Secret, Environment, Deployment record, or rollback writer.

## Explicit Non-Goals

- no second Cloud product, implementation repository, wallet, Gateway, package
  registry, lock, Invocation/Session store, or domain-verdict owner;
- no fixed product-level Workspace count or implicit shared Workspace;
- no public Agent endpoint inferred from a Workspace URL or provider session;
- no compatibility layer after current callers and persisted state have moved;
- no readiness claim from planning, contract presence, fake tests, screenshots,
  or document completeness.
