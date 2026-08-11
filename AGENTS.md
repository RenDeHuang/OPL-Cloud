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
instance receipts apply to the exact release being promoted; they must not
become prerequisites for unrelated local development, CI, or preview work.
Reusable deployment code and instance-specific application may progress in
parallel and converge only for deployment qualification and readback.

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
