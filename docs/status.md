# OPL Cloud Current Status

Owner: `one-person-lab-cloud`
Purpose: `replaceable_current_evidence_snapshot`
State: `current_snapshot`

This file reports what is implemented or evidenced now. It does not define the
target architecture, long-term invariants, open priorities, workflow procedure,
or historical provenance.

## Product Boundary

The current Pilot implementation supports administrator-provisioned accounts.
One Console User maps to one Account and one Sub2API User/Wallet; one Account may
own multiple independent Workspaces. Public registration, customer payment,
shared multi-user Workspaces, backup/recovery/sync/transfer, HA, and GPU are not
current customer capabilities.

Basic and Pro are selectable Workspace packages. Catalog visibility is not
provider-capacity evidence. Customer pricing is exact integer USD micros and
Sub2API remains the only spendable-balance authority.

The configured public model `/v1` endpoint is projected by Control Plane. Its
current presentation is a Console implementation choice. The server-only
Sub2API management origin and credentials are never exposed to the browser.

## Implementation Snapshot

- Console calls Control Plane product APIs only and projects live Sub2API,
  Fabric, Ledger, and Control Plane facts through customer-safe DTOs.
- Control Plane, Fabric, and Ledger are separate Go processes and PostgreSQL
  schema owners. The current production deployment still uses a shared database
  credential and internal token, so service-specific database roles and service
  identities remain open work.
- Tencent TKE is the only production-wired Fabric provider, and that path is a
  medopl instance implementation fact rather than reusable Cloud MVP acceptance.
  A Provider interface exists, but `local-docker` has not yet completed launch,
  readback, and recovery acceptance.
- Workspace file bodies remain only on CBS. Platform PostgreSQL stores identity,
  operation, reference, and evidence facts.
- The Control Plane Session credential vault is process-local and single
  replica. Horizontal scaling is not supported until a secure shared vault and
  distributed wallet-mutation serialization boundary exist.
- Runtime projects-entry and filesystem-usage product APIs remain outside the
  current release; direct mount-marker checks do not prove those product APIs.

## Evidence Snapshot

Current delivery levels remain:

- `code-complete=false` for the repository as a whole;
- `pilot-ready=false`;
- `production-proven=false`.

Focused local evidence exists for the non-review Workspace launch path,
idempotent settlement, provider/resource recovery guards, server-authoritative
Recovery Plan handling, source envelopes, and Console behavior. This does not
prove a real `local-docker` Workspace path, live Gateway accounting, Runtime,
browser, renewal, rollback, or production behavior. Existing Tencent/TKE
evidence applies only to its medopl instance path.

An ordinary Cloud rollout has partial deployment readback. Approved Basic
customer evidence still lacks a complete immutable chain covering exact wallet
delta, one Workspace purchase, provider resources, Runtime login/WebSocket,
model Usage, Receipt, renewal, and rollback. Pro provider evidence has not been
executed for the current product revision.

Capacity evidence targets a 1000-provisioned-user data set. It does not claim
1000 concurrent users, concurrent provisioning, multiple Control Plane
replicas, or HA.

## Documentation And Contract Migration

The active documentation hierarchy now separates product concept, target
architecture, durable invariants, current implementation, functional modules,
status/roadmap, operations, and history.

The former Console display freeze is historical provenance, and presentation is
owned by the current Console implementation under an evolvable experience
guide. The machine Console UI contract and superseded package/shared-execution
machine contracts are retired.

The aggregate launch and deployment contracts are migration guards rather than
long-term product specifications. Launch safety still needs to move into the
owning billing, Control Plane, Fabric, and Ledger contracts. Deployment detail
still needs workflow-family migration while preserving authorization, identity,
Secret, immutable-image, mutation-bound, readback, and rollback gates. The open
sequence and acceptance conditions live only in [the roadmap](./roadmap.md).

## Evidence Interpretation

The durable definitions of `code-complete`, `pilot-ready`, and
`production-proven` live in [the invariants](./invariants.md). Executable checks
live in source, test, and workflow owners. Product and structural gaps live in
[the roadmap](./roadmap.md); this snapshot does not maintain a second action
list.
