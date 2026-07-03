# OPL Cloud Implementation Packages

This directory is the current implementation boundary map. The repository deploys one OPL Console control-plane service while keeping Console, Fabric, Ledger, and contract responsibilities explicit.

## Packages

| Package | Current role | Ownership target |
| --- | --- | --- |
| `console` | OPL Console API, control-plane service, minimal commercial management model, PostgreSQL store, production readiness, production manifest validation, and Console UI | `opl-console` |
| `fabric` | Resource catalog, runtime provider factory, and Local Docker / Tencent TKE adapters | `opl-fabric` or `opl-fabric-adapters` |
| `ledger` | Tencent bill normalization, reconciliation guard helpers, control-plane evidence helpers, and task evidence receipt helpers; billing and evidence contracts are still called by Console service | `opl-ledger` |
| `contracts` | Machine-readable product, resource, management, billing, route, deployment, and evidence contracts shared by Console, Fabric, Workspace, and Ledger | shared contract package or product contract repository |

## Current Boundary

The repository still runs as one deployable OPL Console control-plane service:

```text
packages/console/api/server.js
```

The service may call Fabric and Ledger package code directly for now. New work should keep imports pointed at package boundaries instead of recreating cross-cutting code inside `console`.

## Ownership Rule

When a package becomes independently deployable, keep this repository depending on an API or contract:

- Console should depend on Workspace/Fabric/Ledger contracts.
- Fabric should own resource catalog, runtime execution, and cloud adapter details.
- Ledger should own billing events, reconciliation guard semantics, control-plane evidence, and task evidence receipts.
- ComputeResource contracts describe TKE node pool ownership; StorageVolume contracts describe PVC/CBS ownership; StorageAttachment contracts describe runtime scheduling and mount ownership.
- Workspace runtime behavior remains owned by `one-person-lab-app`.

Do not move OPL Gateway internals, one-person-lab framework internals, or domain-agent marketplaces into this repository.
