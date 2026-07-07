# OPL Cloud Implementation Packages

This directory contains shared package boundaries only. Runtime ownership now lives under `services/*`; browser ownership lives under `apps/console-ui`.

## Packages

| Package | Current role | Ownership target |
| --- | --- | --- |
| `contracts` | Machine-readable product, management, billing, resource allocation, deployment, and evidence contracts shared by Console, Fabric, Workspace, and Ledger | shared contract package or product contract repository |

## Current Boundary

The repository still runs as one deployable OPL Console control-plane service:

```text
services/control-plane/cmd/control-plane/main.go
```

The service may call Fabric and Ledger through service clients or shared contract helpers only. Do not recreate Console business services under `packages/*`.

## Ownership Rule

When a package becomes independently deployable, keep this repository depending on an API or contract:

- Console should depend on Workspace/Fabric/Ledger contracts.
- Fabric owns resource catalog, runtime execution, and cloud adapter details under `services/fabric`.
- Ledger owns billing events, reconciliation guard semantics, control-plane evidence, and task evidence receipts under `services/ledger`.
- ComputePool, ComputeAllocation, StorageVolume, and StorageAttachment contracts should stay shared: Console owns user-visible operations and receipts, while Fabric owns provider-specific execution mechanics.
- Workspace runtime behavior remains owned by `one-person-lab-app`.

Do not move OPL Gateway internals, one-person-lab framework internals, or domain-agent marketplaces into this repository.
