# OPL Cloud Business-Driven Architecture Design

Owner: `one-person-lab-cloud`
Purpose: `approved_architecture_support_detail`
State: `approved_design`

This design organizes the approved OPL Cloud architecture as a set of readable
views. It does not replace the canonical product, architecture, invariant,
implementation, status, or roadmap owners in `docs/README.md`. Target authority
remains in `docs/architecture.md` and `docs/invariants.md`; current behavior and
evidence remain in source, schemas, focused tests, `docs/implementation-architecture.md`,
and `docs/status.md`; open gaps and priorities remain in `docs/roadmap.md`.

## Fixed Input And Output

The fixed input is:

> A customer needs a cloud product for OPL App.

For the first product stage, this means that the customer needs an independently
accessible online OPL App/WebUI Workspace. It does not yet include publishing an
Agent as an API, Embed, or Hosted UI through OPL Serve.

The fixed output is:

> A portable, installable, verifiable, and traceable OPL Cloud product.

The portable output includes Console, Control Plane, Fabric, Ledger, typed
contracts, portable installation assets, an immutable Candidate, qualification
receipts, and an exact-byte Product Release. A concrete medopl deployment is an
Instance-owned consumption of that output, not the Cloud product itself.

## Design Method

The architecture uses three complementary methods:

1. Business-driven design establishes the customer result and end-to-end value
   chain before selecting modules.
2. Domain-driven design assigns one bounded context and authority owner to each
   business fact and mutation.
3. Clean Architecture keeps business rules independent of HTTP, PostgreSQL,
   Docker, Tencent SDKs, and other delivery mechanisms.

The architecture is represented through multiple views rather than one diagram
that mixes product intent, code dependencies, data ownership, runtime behavior,
and deployment topology.

## Domain Model

The Core Domain is **Workspace commercial admission and lifecycle fulfillment**.
The customer buys an entitlement to an accessible, billable, renewable,
deletable, and auditable online Workspace, not a virtual machine or container.

| Domain type | Capability | Primary owner |
| --- | --- | --- |
| Core | Workspace admission, Launch, access, renewal, deletion | Control Plane |
| Supporting | Account policy, product catalog, quote, settlement coordination | Control Plane |
| Supporting | Compute, storage, attachment, Secret, Runtime fulfillment | Fabric |
| Supporting | Receipt, reconciliation, evidence indexing and retention | Ledger |
| Presentation | Customer and operator interaction | Console |
| External | Identity, spendable wallet, Key, routing and Usage | Sub2API |
| External | OPL App product and immutable Workspace image | `one-person-lab-app` |
| External | Local Docker or Tencent/TKE infrastructure | Fabric provider adapters |
| Downstream | Domain, Provider Profile, Secrets, deployment and rollback | Instance owner |

These are capability boundaries, not instructions to create a microservice for
every row. Account, catalog, Workspace lifecycle, and settlement coordination
remain cohesive capabilities inside Control Plane.

## Clean Dependency Rule

Inside each service, dependencies point toward business rules:

```text
HTTP / Worker / CLI
        -> Application Use Case
             -> Domain Model and Business Rules

PostgreSQL Repository -------- implements an inward-owned port
Typed HTTP Client ------------ implements an inward-owned port
Provider Adapter ------------- implements Fabric's provider port
```

Workspace business rules must not depend on SQL, HTTP, Docker SDKs, Tencent
SDKs, or UI state. Existing modules should improve cohesion one real capability
at a time without a rewrite, compatibility layer, global event bus, new shared
domain package, or new service.

## Aggregate And Data Boundaries

| Context | Aggregate root | Principal invariant |
| --- | --- | --- |
| Control Plane | Account | Account policy owns purchase admission |
| Control Plane | Workspace | One Account owns an independent entitlement and lifecycle |
| Control Plane | WorkspaceOperation | One durable command owns stage, budget, idempotency and CAS version |
| Control Plane | BillingReconciliation | The latest accepted mismatch can block new purchases |
| Fabric | FabricOperation | One immutable binding and request hash belong to one idempotency identity |
| Fabric | MachineOwnership | One managed provider resource has one valid owner |
| Ledger | Receipt | Accepted evidence is append-only and does not authorize business mutation |
| Ledger | ReconciliationReport | One idempotency identity records one computed comparison |
| Ledger | EvidenceIndexEntry | Evidence references are indexed without copying owner state |

Console has no business database. Control Plane, Fabric, and Ledger use
separate PostgreSQL roles, databases, schemas, migrations, and transactions.
Sub2API remains the external database owner for identity, wallet, Key, routing,
and Usage. Workspace file bodies and project artifacts remain only on
Workspace-owned storage.

Cross-service consistency uses a persisted Control Plane process manager,
stable operation identities, request hashes, idempotency keys, local ACID,
compare-and-swap, owner-authoritative readback, finite attempt/read budgets,
and explicit `manual_review`. It does not use a distributed database
transaction or infer success from a transport response alone.

## Workspace Business Chain

The first-stage customer chain is:

```text
Login
-> Identity readback
-> Package and quote
-> Balance and provider preflight
-> Durable Launch claim
-> Key
-> Debit
-> Compute
-> Storage
-> Attachment
-> Secret
-> Runtime
-> Activation
-> Purchase Receipt
-> Workspace Ready
-> Open Workspace
-> Gateway Usage
-> Renewal or permanent Delete
```

The durable Launch stage order is:

```text
key -> debit -> ensure_compute_allocation -> storage -> attachment
    -> secret -> runtime -> activation -> receipt -> succeeded
```

Every external stage reads the owner first. If the resource is absent and the
mutation budget remains, Control Plane reserves the attempt before dispatch.
The first post-mutation owner read is mandatory. `ready` advances, typed
`pending` consumes bounded continuation reads, and `unknown`, conflict, or
budget exhaustion enters `manual_review`.

The permanent Delete chain is:

```text
validate Launch, Receipt, current Key and immutable bindings
-> runtime and Secret absent
-> attachment absent
-> storage absent
-> compute absent
-> Sub2API Key absent
-> Control Plane Workspace absent
-> workspace.deleted.v1 Receipt
-> complete
```

Delete performs zero wallet mutation and no automatic refund. Renewal,
Cancel-Renewal, Delete, Refund, operator Launch Resume, and a future customer
Suspend/Resume are distinct business commands.

## Runtime And Operations

Only Control Plane is exposed through the Instance entry point. Fabric, Ledger,
and PostgreSQL remain internal. Fabric alone receives provider authority.
Workspace Runtime uses an exact OPL App image digest that remains distinct from
the OPL Cloud product image.

Operational evidence is layered:

```text
L1 process health: container, port and PostgreSQL
L2 service readiness: Control Plane, Fabric and Ledger
L3 product readiness: identity, wallet, provider, Runtime and Workspace access
L4 business consistency: Debit, resource, entitlement and Receipt agreement
```

Compose health proves only the control-service layers. Product readiness needs
owner-authoritative readback. Infrastructure alarms belong to the Instance;
business alarms project stable redacted operation transition codes. There is no
second alert truth table.

## Product Delivery Chain

The pre-1.0 target delivery chain is:

```text
canonical Cloud SHA
-> immutable multi-architecture Candidate and portable bundle
-> supported Local-Docker clean-host qualification
-> Instance Tencent/TKE qualification and executed rollback
-> both receipts bind the same Cloud SHA and index digest
-> authorized publisher promotes the existing bytes
-> OPL Cloud Product Release
```

Cloud does not dispatch or operate the Instance. An Instance does not rebuild
the Cloud product. Product publication must not rebuild a digest that differs
from the qualified Candidate.

## Business-Driven Roadmap Projection

This phase projection references, but does not replace, `docs/roadmap.md`:

| Milestone | Outcome | Current roadmap ownership |
| --- | --- | --- |
| M0 | Approved architecture and diagram package | Documentation support detail |
| M1 | Portable Candidate with explicit installation-owned inputs | `LOCAL-CONTROL-SERVICES-01`, `LOCAL-WORKSPACE-INSTALL-CONTRACT-01`, `FABRIC-PROVIDER-PROFILE-01` |
| M2 | Exact-current Local-Docker customer lifecycle receipt | `MVP-LOCAL-WORKSPACE-GATEWAY-01`, `OPS-ACCOUNT-IDENTITY-READBACK-01` |
| M3 | Same-Candidate Tencent/TKE qualification and rollback receipts | `INSTANCE-MEDOPL-01` |
| M4 | Exact-byte formal Product Release | `PRODUCT-RELEASE-01` |
| M5 | Live renewal, reactivation, recovery and operational closure | `WORKSPACE-RENEWAL-REACTIVATION-01`, `LOCAL-WORKSPACE-RECOVERY-READBACK-01` |
| M6 | Self-service registration, payment and customer lifecycle expansion | `CONSOLE-SELF-SERVICE-01`, `WORKSPACE-LIFECYCLE-CLOSURE-01` |
| M7 | Agent Service API, Embed and Hosted UI | `SERVE-01`, `RUNWAY-01` |

M2 and M3 may qualify the same Candidate in parallel. M4 requires both receipts.
Module cohesion, security, performance and contract verification proceed inside
their real owners without becoming speculative services or blanket release
gates.

## Product Summary

No total database is introduced. OPL Cloud has three explicit summaries:

1. Console and Control Plane compose customer-safe product DTOs.
2. Ledger stores receipts, digests and evidence references.
3. The Release manifest binds source, images, contracts, installation assets,
   qualification receipts and publication provenance.

The fixed product output is therefore:

```text
OPL Cloud
= Console
+ Control Plane Core Domain
+ Fabric Resource Fulfillment
+ Ledger Evidence
+ Typed Contracts
+ Portable Installation Assets
+ Exact Candidate
+ Qualification Receipts
+ Exact-byte Product Release
```

## Diagram Package

The approved diagrams are indexed under
[`docs/plans/opl-cloud/README.md`](./opl-cloud/README.md). Mermaid sources and
rendered images are kept together under `docs/plans/opl-cloud/diagrams/` so each
view has an explicit name, purpose, and reviewable source.
