# OPL Fabric

Owner: `one-person-lab-cloud`
Purpose: `fabric_target_reference`
State: `active_target_reference`
Machine boundary: Human-readable target platform definition. It does not prove
resource availability, adapter implementation, binding success, runtime state,
billing, or production readiness.

OPL Fabric is the target resource and connector substrate for OPL App, OPL
Workspace, Cloud-managed jobs, and approved domain-agent actions. The target
connects work and Serve invocations to compute, storage, software environments,
and external systems through a shared plan, approval, execution, collection,
and receipt pattern.

Fabric is not a package registry or package lifecycle owner.
Fabric also owns no customer wallet, balance, or charge ledger.

```text
OPL Fabric
├─ OPL Connect        connector access and normalized source refs
├─ OPL Compute        Docker, VM, GPU, SSH, HPC and managed workers
├─ OPL Environments   software stacks and runtime environment refs
├─ Workspace Storage  volumes, private buckets and institutional storage refs
├─ Surface adapters   Gateway/App/Workspace/Serve integration
└─ Execution adapters resource binding, status and output collection
```

## Reusable Capability Boundary

OPL Console governs Cloud-hosted or explicitly managed use, but is not the only
Fabric caller. App, Workspace, Serve through Runway, and approved domain agents
can call Fabric when their capability and policy refs allow it. Ledger records
receipt refs. The resource owner remains authority for the underlying resource
state.

## OPL Connect

Connect owns stable connector access, normalized source refs, credentials,
error semantics, retries and rate limits. It does not own OPL Package identity,
publication, discovery or carrier lifecycle. A connector implementation may be
distributed in an owner Package and installed by its configured native carrier;
Framework supplies the aggregated installed/callable ref before Fabric binds it
to a run.

Domain-specific retrieval strategy, evidence judgment, synthesis and quality
remain with the domain adapter and domain Agent.

## OPL Compute

Compute adapters cover local or managed Docker/VM, GPU workers, SSH/HPC and
later managed workers. Every path follows:

```text
plan -> approve -> execute -> monitor -> collect -> receipt
```

Console policy becomes relevant for Cloud-hosted or explicitly managed
resources. User-provided resources can use the same flow without default Cloud
billing.

## Provider Model

Fabric exposes provider-neutral resource capabilities to callers. Provider-
specific identifiers, retry rules, diagnostics, and mutation sequences stay
inside adapters. The first production path may use `tencent-tke`, but Tencent
Cloud is one adapter rather than the Fabric product definition.

An OPL Cloud instance selects an approved provider profile. The first target
set is `tencent-tke` for the `medopl` hosted instance, `local-docker` for a Mac
or Linux host, and a generic `kubernetes` adapter for self-hosted clusters. The
initial implementation may select one primary adapter per instance while every
Workspace persists its exact provider binding so later instances can expose
more than one provider without changing Workspace identity.

`local-docker` is now implemented as the default Core adapter. Fabric startup
selects it even when `NODE_ENV=production`; `tencent-tke` is available only when
an instance explicitly sets `OPL_FABRIC_PROVIDER=tencent-tke`. Both adapters pay
the same provider-neutral Fabric port, while provider writes and authoritative
readback stay inside the selected adapter. The real Docker integration gate
exercises compute, storage, attachment, Secret binding, and Runtime, but this
Fabric evidence alone does not prove the complete Console-to-Workspace path.
Current facts belong to [status](status.md), and the remaining end-to-end gap
belongs to the [roadmap](roadmap.md).

Control Plane owns one durable Workspace Launch business state machine; Create
and Resume enter its same Reconciler. Fabric does not own that cursor or a second
Recovery state machine. It owns the resource-stage implementation, durable
operation store, provider/Kubernetes mutation, and authoritative readback for
compute, storage, attachment, Secret binding, and Runtime.

Every stage request arrives through a typed public Fabric HTTP contract with an
explicit, immutable, provider-neutral binding for the Launch operation, account
and Workspace, stage/action, stable stage operation/idempotency identity, request
hash, and expected resource binding. Fabric persists that binding before a
provider write and returns it with readback. The selected adapter then maps it to
Machine, CVM, Node, CBS, Runtime, or local-Docker identities. Control Plane must
not derive resource ownership from `:compute` suffixes, unscoped operation
listings, provider tags, or adapter fields.

Recovery may cause Control Plane to re-enter the original Reconciler only after
an immutable CAS-persisted Resume authorization. It does not call a provider or
provider-specific mutation directly. The current Fabric routes mirror the real
typed caller DTO and accept only the five canonical stage/action pairs. The
current Control Plane caller and Fabric implementation consume the same golden
request-hash vectors. The end-to-end Console-to-local-Workspace gate remains
separate from this Fabric-owned implementation proof.

For Serve, Fabric may prepare an isolated sandbox or worker, inject approved
secret refs, apply network/egress policy, enforce resource limits and collect
outputs. The stable public endpoint remains the Serve Agent Edge. Fabric does
not expose sandbox ports or own Service, Revision, Deployment, Invocation or
Session identity.

## OPL Environments

Environments describe software stacks, container/runtime refs, hardware needs
and compatibility constraints. They are resource inputs, not Package locks.
When an environment requires an OPL Package, Fabric consumes the owner
descriptor/publication ref and fresh carrier state exposed through Framework
aggregation, then records the resource binding.

## Package Resource Binding

Fabric may project package requirements for resource planning:

- exact owner descriptor and publication revision refs;
- fresh carrier installed/callable ref;
- compute and hardware requirements;
- storage and data-boundary requirements;
- environment and connector requirements;
- review gates and runtime policy refs.

Fabric can report `binding_available`, `binding_blocked` or equivalent resource
status. It cannot publish, install, update, remove or repair Package bytes, or
choose a revision. A missing publication routes to the Package owner; missing
physical bytes route to the configured carrier through Framework aggregation;
a missing resource routes to Fabric or Console policy as applicable.

## Resource Catalog

| Resource type | User-facing examples | Owner |
| --- | --- | --- |
| Compute | Standard compute, GPU, SSH/HPC, managed worker | OPL Compute / resource provider |
| Storage | Workspace volume, private bucket, institutional storage ref | Storage owner + Fabric binding |
| Connector | Literature source, database, internal API, tool integration | OPL Connect + source owner |
| Environment | Python/R, CUDA, document tooling, runtime profile | OPL Environments |
| Package requirement | Owner descriptor/publication ref, fresh carrier state and resource requirements | Package owner + native carrier; Framework aggregation; Fabric projection only |
| Serve execution | Sandbox/worker, network, secret and artifact binding for an exact Agent Revision | Runway lifecycle + Fabric resource truth |

This catalog lets products select resources without exposing infrastructure
internals or creating a second package registry.
