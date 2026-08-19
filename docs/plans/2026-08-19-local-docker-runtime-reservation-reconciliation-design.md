# Local-Docker Runtime Reservation Reconciliation Design

**Status:** Approved for implementation

**Objective:** Make Local-Docker runtime capacity reconciliation preserve an existing Workspace's immutable admitted CPU and memory facts across Provider profile changes without guessing facts for pre-reservation containers.

## Ownership

- `services/fabric` owns Local-Docker runtime reservations, Docker cgroup readback, capacity admission, and inventory failure behavior.
- `packages/contracts/opl-cloud-fabric-launch-binding-contract.json` owns the immutable Provider plan rule: a current Provider Profile change cannot replace an existing binding.
- Control Plane, Console, Ledger, and Tencent/TKE do not participate in Local-Docker host-capacity reconciliation.

## Design

Fabric writes a durable runtime reservation before dispatching `docker run`. The reservation records the Workspace and resource identities, package identity, NanoCPUs, and memory bytes admitted for that runtime. Reconciliation therefore treats an existing valid reservation as the capacity-accounting authority and compares Docker's live labels and `HostConfig` exactly against it.

The live container name must equal the deterministic Local-Docker runtime name for its Workspace, and each resource identity may appear only once in inventory. This prevents a non-canonical or duplicate labeled container from losing its reservation during the reverse absence pass and escaping capacity accounting.

Reconciliation must not resolve an existing runtime's package through the current deployment profile. A later profile may change or remove a package while the old runtime remains bound to its original immutable plan. The old reservation remains charged at its admitted values; new launches use the current profile.

A live container without a reservation is recovered only from exact live facts: deterministic Workspace/resource identity, a non-empty package label, positive `NanoCPUs` and memory limits, and `MemorySwap == Memory`. Fabric writes those exact values as the reservation without consulting the current profile. An unbounded or incomplete legacy container still fails closed and requires an explicit migration. A reservation without a container is removed only after authoritative container-absence readback.

## Acceptance Evidence

- A runtime reserved at `2c4g` remains countable after the current `basic` profile changes to `4c8g`.
- A new runtime is admitted using the new profile while the old runtime remains charged at `2c4g`.
- A bounded live runtime without a reservation is recovered from its exact live cgroup limits without consulting the current profile.
- An unbounded or incomplete live runtime returns `local_docker_runtime_reservation_inventory_invalid` and creates no reservation.
- A non-canonical runtime name or duplicate resource identity fails closed before capacity is summed.
- Public Local-Docker Runtime status uses the same durable reservation reconciliation and remains consistent with capacity admission across profile drift.
- Runtime status presence and complete readback remain inside the same storage-root lock, and waiting for that lock honors the caller's context deadline.
- Package, Workspace, resource, cgroup, or reservation drift fails closed.
- Existing stale-reservation recovery and aggregate capacity tests remain green.

## Non-goals

- No migration of the four pre-quota local test runtimes.
- No inference from current profile, Docker usage, defaults, or product pricing.
- No Tencent/TKE, Control Plane, Ledger, or Console change.
- No deletion or mutation of the current shared Docker daemon.
