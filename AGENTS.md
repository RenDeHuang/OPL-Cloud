# OPL Cloud Development Rules

`one-person-lab-cloud` is the product repository for OPL Cloud architecture,
Console, Control Plane, Fabric, Ledger, contracts, portable distribution, and
reusable release mechanisms.

## Canonical Owners

- `docs/README.md` maps documentation topics to their canonical owners.
- `docs/architecture.md` and `docs/decisions.md` own target architecture and
  durable decisions.
- `docs/implementation-architecture.md`, source, schemas, and focused tests own
  current implementation facts.
- `docs/status.md` owns current evidence. `docs/roadmap.md` owns open gaps,
  priority, and acceptance outcomes.
- `packages/contracts` owns only machine-readable facts that have a current
  cross-module, public-interface, security, integrity, or irreversible-side-
  effect consumer.
- `opl-instance-medopl` owns the medopl domains, provider profile, production
  environment and Secrets, deployment, rollback, acceptance, and receipts.

`opl-cloud` is an internal package, image, service, namespace, and runner name.
The archived documentation repository and Git history are provenance, not
current writers.

## Reconcile Before Editing

Before implementation, name the objective, primary module owner, canonical
contract or document owner, real callers, exact write set, and completion
evidence.

The latest direct user decision sets product intent. Source, tests, documents,
Candidates, Releases, and runtime readback prove different layers; none of them
silently upgrades a lower-layer result into a production claim.

When current sources disagree:

1. Trace the relevant Git history, callers, contracts, and runtime readback.
2. Classify each statement as target, current implementation, runtime evidence,
   production evidence, history, derived, stale, or unknown.
3. Reconcile the decision in its canonical owner and remove the duplicate
   current writer in the same change.
4. Stop only when unresolved authority would change an irreversible production
   action or the requested terminal outcome.

Issues, PRs, comments, agent sessions, generated docs, and old plans are inputs.
They do not override the current owner.

## Documentation

Follow the hierarchy in `docs/README.md`. Lower layers may implement or report
an upper-layer decision; they do not redefine it.

Keep active documentation limited to current truth, open gaps, and reusable
operations. Completed plans, freezes, task checklists, shell transcripts, and
closeout notes belong in Git history or `docs/history/**` when provenance is
still useful.

Machine contracts contain stable inter-owner facts, not implementation layout,
query strategy, worker tuning, presentation details, current progress, or a
catalog of retired alternatives. Tests exercise the owning behavior or current
consumer instead of restating contract JSON or proving retired files remain
absent.

## Module Ownership

| Module | Owns |
| --- | --- |
| `apps/console-ui` | Presentation and calls to Control Plane product APIs |
| `services/control-plane` | Sessions, account policy, Workspace orchestration, settlement coordination, customer DTOs |
| `services/fabric` | Provider-neutral compute, storage, attachment, Secret binding, Runtime facts, provider adapters |
| `services/ledger` | Receipts, evidence, retention, reconciliation, opaque provenance |
| `services/internal` | Policy-free infrastructure shared by at least two current services |

- Control Plane, Fabric, and Ledger remain separate Go modules, processes, and
  PostgreSQL schema owners. Integration uses typed public HTTP contracts.
- Console reaches service data through Control Plane APIs.
- Provider-specific behavior stays behind the owning Fabric adapter.
- Console does not own persistence, provider calls, billing decisions, or
  Fabric/Ledger/Sub2API state. Control Plane does not own the wallet, provider
  resources, or another service's tables. Fabric does not own customer balance
  or Ledger evidence. Ledger does not own provider mutation or Workspace
  orchestration.
- DTOs, reducers, state machines, retry policy, and authority facts have one
  owner. Other modules use a thin typed client or adapter.
- A shared module requires at least two current callers and remains policy-free.
- Cross-module changes update the owning contract, both consumers, and focused
  boundary tests together.

Development with distinct owners and write sets may proceed in parallel. Changes
to the same file, one shared contract revision, canonical `main`, or production
state are serialized.

Cross-service coordination remains typed HTTP plus owner readback. Do not add a
new framework, service, shared policy layer, durable workflow engine, or global
event bus unless a current caller and observed missing capability justify it;
the current architecture does not adopt Spring Modulith, Dapr, Temporal, or a
second Cordis runtime.

## Implementation

- Reproduce or trace the real path before fixing a bug. Once the deepest
  breakpoint and owner are known, repair that owner before expanding tests or
  process.
- Prefer the direct path inside the current owner. A new file, abstraction,
  dependency, state, fallback, compatibility path, workflow, or gate needs a
  current caller, contract, observed failure, or concrete reachable risk.
- Improve cohesion inside the existing module before adding a framework or
  service. Adopt a new runtime or architectural dependency only when a measured
  missing capability and a focused replacement path justify it.
- Refactor one live capability at a time, preserve its public behavior and
  persisted data obligations, switch real callers, then remove the retired path.
- Keep source, tests, `docs/status.md`, and `docs/roadmap.md` aligned at their
  respective evidence layers.

## Release And Instance Boundary

Cloud publishes portable GHCR images, GitHub Releases, installation assets, and
reusable provider adapters. It does not own an instance deployment workflow or
require a production environment to build the product.

During pre-1.0, build a replaceable candidate from an exact canonical Cloud SHA
and image digest. The instance owner deploys and qualifies that candidate in its
protected environment. A formal Product Release promotes the same qualified
bytes without a rebuild.

Candidate construction and local development do not authorize a formal
publication or an Instance deployment. The current combined Release workflow
still has an exact-byte promotion gap recorded in `docs/roadmap.md`; do not
publish a successor until the required Instance receipt closes it.

Only the repository owner and `RenDeHuang` may dispatch the manual Cloud Release
workflow from `main`, and the original publisher must remain the triggering
actor. Development, CI, qualification retries, and instance-only work do not
authorize publication.

## Verification

- Run focused checks first.
- Use `npm run verify:local` for ordinary source changes.
- Use `npm run verify:local:full` for persistence, schema, retained service
  behavior, cross-module contracts, or structural changes involving PostgreSQL,
  capacity, or Local-Docker behavior.
- Tests and builds prove their own layer. Instance adoption and production state
  require owner-authoritative deployment and runtime readback.
- Before changing billing, Workspace, Fabric, Ledger, Gateway, deployment, or
  E2E, read the owning architecture/invariant sections and current machine
  contract, then update the source owner and its evidence projection together.

## High-Consequence Boundaries

- Production-private endpoints, clusters, databases, and services are accessed
  only through `opl-instance-medopl` protected workflows and authorized runners.
- The local development machine must never access the production private
  network directly; this repository must not dispatch Instance deployment.
- Sub2API remains the spendable-wallet and Gateway backend authority.
- Do not create a second wallet, Gateway, package registry, lock, or domain
  authority in Cloud.
- Customer and verification compute/storage procurement uses the approved
  prepaid monthly policy.
- Never use `POSTPAID_BY_HOUR` for customer or verification CVM/CBS resources.
- Ordinary CI, release, and E2E are read-only for real customer money and
  provider resources. Real charges, purchases, renewals, or deletion require a
  separate explicit authorization and bounded owner workflow.
- Ordinary CI, release, or E2E must not buy or delete Tencent CVM/CBS resources,
  charge a real monthly product fee, add a public test-billing mode, or clean up
  customer resources from verification code.
- Secrets remain in their approved secret stores and authorized runtime
  boundaries.

<!-- CODEGRAPH_START -->
## CodeGraph

- This repository uses a local `.codegraph/` index; do not commit it.
- Use CodeGraph for definitions, callers, impact, and paths; use `rg` for literal
  text.
- Run `codegraph init .` or `codegraph sync .` when the index is missing or stale.
<!-- CODEGRAPH_END -->
