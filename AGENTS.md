# OPL Cloud Development Rules

This is the single `one-person-lab-cloud` product repository. It owns the OPL
Cloud public architecture, whitepaper, roadmap, Console, Control Plane, Fabric,
Ledger, Workspace delivery, contracts and reusable release mechanisms.

- `docs/README.md` owns the documentation hierarchy and topic-to-owner map.
- `docs/architecture.md` owns the target product and authority boundaries.
- `docs/implementation-architecture.md`, source, schemas, tests and runtime
  readback describe the current implementation at their respective layers.
- `docs/status.md` owns the current evidence snapshot; `docs/roadmap.md` owns
  gaps, priorities and acceptance outcomes only.
- `opl-cloud` remains the internal package, image, service, namespace and runner
  identifier; it is not a second repository owner.
- `opl-instance-medopl` is the only medopl instance owner. It owns domains,
  provider selection, production environments and Secrets, deployment,
  verification, rollback, and receipts while consuming an immutable Cloud
  release.
- This repository must not contain an instance deployment workflow or require a
  production environment for product release. It publishes portable GHCR images,
  GitHub Releases, installation assets, and reusable provider adapters only.
- The archived documentation repository is provenance only and must never
  become a parallel current writer.

## SSOT Reconciliation

Before implementation, name the objective, the primary module owner, the
canonical contract or document owner, and the evidence needed for completion.
The latest direct user decision controls product intent; it does not by itself
prove implementation, deployment, or production state.

When current documents, contracts, source, tests, PRs, or runtime evidence
conflict:

1. Do not select the newest timestamp or the easiest statement to implement.
2. Trace how the conflict arose with fresh Git history, PR context, real
   callers, the canonical topic owner, and authoritative runtime readback where
   applicable.
3. Classify each statement as target, current implementation, runtime evidence,
   production evidence, historical provenance, derived, stale, or unknown.
4. Reconcile the decision in the canonical owner and remove or redirect the
   duplicate writer in the same change. Do not leave two current truths.
5. If the conflict changes an irreversible production action or cannot be
   resolved from owner evidence, stop before mutation and request the missing
   decision.

Issues, PR descriptions, comments, generated docs, and archived repositories
are inputs or provenance, never independent SSOT owners.

## Documentation Layers

Follow the hierarchy in `docs/README.md`. A lower layer may refine or report an
upper-layer decision, but it must not redefine it. When an upper-layer product
or architecture decision changes, update every affected lower-layer projection
in the same change or mark the unresolved projection as an explicit roadmap
gap. Do not leave two current truths.

Machine contracts are admission gates only for facts that need deterministic
cross-module or safety enforcement. They must not freeze colors, dimensions,
page counts, component libraries, model choices, query strategies, worker
intervals, current progress, or other ordinary implementation decisions. Keep
those in the current implementation, an evolvable guide, performance tests, or
`docs/status.md` as appropriate.

## Module Ownership And Physical Boundaries

Assign every feature to one primary module before editing:

| Module | Owns | Must not own |
| --- | --- | --- |
| `apps/console-ui` | Presentation and calls to the Control Plane product API | Persistence, provider calls, billing decisions, Fabric/Ledger/Sub2API calls |
| `services/control-plane` | Sessions, account policy, Workspace orchestration, billing coordination, customer DTOs | Spendable wallet, provider resources, Fabric/Ledger tables |
| `services/fabric` | Provider-neutral compute, storage, attachment, runtime facts, and provider adapters | Customer balance, Console policy, Ledger evidence truth |
| `services/ledger` | Receipts, evidence, review, reconciliation, and continuation refs | Balance mutation, provider resources, Workspace orchestration |
| `packages/contracts` | Machine-readable cross-module contracts | Runtime behavior or duplicated implementation state |
| `services/internal` | Semantics-free infrastructure shared by at least two real services | Product orchestration, DTOs, state machines, provider logic |

- Keep Control Plane, Fabric, and Ledger as separate Go modules, processes, and
  PostgreSQL schema owners. Cross-service integration uses typed public HTTP
  contracts; never import another service's `internal`, `ent`, `cmd`, or source
  packages and never read or write another service's tables.
- Console UI calls only Control Plane product APIs. It must not deep-import
  service source, contracts, provider SDKs, or infrastructure helpers.
- Put provider-specific behavior behind the owning Fabric adapter. When
  touching an existing Tencent leak outside that adapter, do not spread it;
  either move it behind the port or record the unresolved boundary in the
  roadmap owner.
- Do not copy DTOs, reducers, state machines, authority facts, or retry logic
  across modules. Establish one owner and use a thin typed client or adapter.
- No circular dependencies, path traversal into sibling modules, or generic
  `shared` package created for one caller. A shared module requires at least two
  current callers and must remain policy-free.
- A cross-module change must name the owning contract and update both sides and
  focused contract tests atomically. Keep orchestration in the module that owns
  the user-visible operation, not in a downstream resource or evidence module.

`gaofeng21cn` and `RenDeHuang` may independently develop and merge ordinary PRs
after required CI passes and review conversations are resolved. Module owners
route review when it adds value; the repository does not automatically request
a reviewer for every PR or require an approving review. Production mutation
authorization remains separate in the owning instance repository and is
governed by protected GitHub Actions environments, exact inputs, and
authoritative readback.

Development follows `parallel_work_serialized_integration`. Multiple roadmap
`next` lanes may proceed at once when they have distinct owners and write sets.
Serialize only changes to the same files, one shared contract revision,
canonical `main`, or a real production mutation. Production qualification and
Instance receipts apply to the exact candidate being considered for
publication; they must not become prerequisites for unrelated local
development, CI, or preview work. Reusable deployment code and instance-specific
application may progress in parallel and converge only for candidate
deployment, qualification, and publication readback.

## Product Release Admission

During the current pre-1.0 phase, do not dispatch a formal Product Release as a
development checkpoint or to obtain deployment evidence. First build a
replaceable candidate from an exact canonical Cloud SHA and image digest, then
have `opl-instance-medopl` deploy and qualify that candidate through its own
protected workflow. A successor Release is admitted only after the Instance
receipt proves the required deployment and product acceptance for that exact
SHA/digest, and the formal publication promotes the same image bytes without a
rebuild.

Only the repository owner may explicitly dispatch the manual Release workflow
from `main`. A PR, merge, CI run, schedule, collaborator action, deployment
retry, or failed qualification never authorizes publication. The current
workflow still combines candidate build with formal publication; until a
deployable candidate channel and exact-byte promotion are implemented, record
the gap in `docs/roadmap.md` and do not publish a successor to `v0.1.7`.

Cloud retains reusable product and publication authority;
`opl-instance-medopl` retains environment, Secret, provider, deployment,
rollback, acceptance, and receipt authority. Using its receipt as a release
gate does not authorize Cloud to operate the Instance. The create-only workflow
prevents accidental version reuse but does not override an explicit repository
owner cleanup decision. Documentation-only, test-only, CI-performance, and
Instance-only changes do not independently justify a Product Release.

## Architecture Adoption And Cohesion

The default architecture is the current Go/TypeScript service system, not a
placeholder awaiting a larger framework. Control Plane, Fabric, and Ledger keep
their separate process, module, schema, and authority boundaries; Console keeps
its typed Control Plane API boundary. `docs/decisions.md` owns current adoption
decisions. These rules govern how an Agent may propose or implement a change.

- Adopt a new framework, runtime, shared infrastructure layer, or architectural
  dependency only when a current caller and observed failure prove a specific
  missing capability. The proposal must name the affected authority, the
  smallest replacement path, migration and rollback obligations, focused
  acceptance evidence, and a measurable benefit over improving the owning
  module. Popularity, maturity, ecosystem size, or architectural uniformity are
  not sufficient evidence.
- Improve cohesion inside the existing owner first. Split a mixed facade or
  large implementation file along real capabilities, callers, transactions,
  and provider boundaries while preserving receivers, public HTTP contracts,
  schemas, persistence semantics, and behavior. File length alone does not
  justify a new service, shared package, domain layer, plugin system, or event
  bus.
- Under the current decision, do not introduce Spring Modulith, a Cloud Cordis
  runtime or sidecar, Dapr, Temporal, a second plugin registry, or a global
  event bus. Reconsider one only through a new explicit architecture decision
  backed by the evidence above. Framework-owned Cordis integration stops at a
  typed Cloud client/API adapter; it must not acquire Cloud service authority.
- Keep cross-service coordination explicit through typed public HTTP contracts
  and owner readback. In-process events may organize code inside one owner but
  must not become cross-service truth. Add durable workflow machinery only for
  a demonstrated restart/recovery problem that the current owner cannot safely
  solve with its existing persisted state and idempotent operations.
- Refactor by moving one coherent capability at a time, preserving the real
  caller path and testing behavior before and after the move. Do not use a
  rewrite, compatibility layer, permanent dual path, or speculative abstraction
  to make a structural change appear safer.
- Use `npm run verify:local` as the repeatable source gate for ordinary changes.
  Use `npm run verify:local:full` for persistence or schema work, retained
  Control Plane/Fabric/Ledger behavior, cross-module contracts, and structural
  changes whose risk includes PostgreSQL, capacity, or local-Docker behavior.
  Run focused checks first, then the applicable aggregate gate before canonical
  integration. Neither local gate proves production or Instance adoption.

Before changing billing, Fabric, Workspace, Gateway, Ledger, deployment, or E2E:

1. Read the relevant target, architecture and invariant sections identified by
   `docs/README.md`.
2. Read only the current machine contract owned by the affected boundary and
   its real callers. `opl-cloud-launch-freeze-contract.json` is a migration
   source, not a universal development prerequisite.
3. Preserve hard safety and authority boundaries. Record implementation and
   readiness changes in source/tests and `docs/status.md`; record remaining
   work in `docs/roadmap.md`.

Hard prohibitions:

- The local development machine cannot access the production private network.
  Never attempt local direct access to production-internal endpoints, clusters,
  databases, or services. Medopl production deployment and private-network
  verification run only through `opl-instance-medopl` workflows using its
  protected `production` environment and authorized runner. This repository may
  publish product releases but must not dispatch instance deployment.
- Do not add a second wallet or Gateway service; Sub2API is the Gateway backend and spendable-balance owner.
- Do not introduce `POSTPAID_BY_HOUR` for customer or verification CVM/CBS resources.
- Do not buy or delete Tencent CVM/CBS resources during an ordinary CI, release, or E2E run.
- Do not charge a real monthly product fee during verification.
- Do not add a public test billing mode or clean up customer resources from verification code.

<!-- CODEGRAPH_START -->
## CodeGraph

- This repository uses a local `.codegraph/` index; never commit that directory.
- Prefer CodeGraph for definitions, callers, impact, and code paths; use `rg` for literal text.
- Run `codegraph init .` or `codegraph sync .` when the index is missing or stale.
<!-- CODEGRAPH_END -->
