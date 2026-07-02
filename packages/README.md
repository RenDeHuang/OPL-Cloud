# OPL Cloud Implementation Packages

This directory is a migration staging layout. It keeps the current repository deployable while making the future split into separate implementation repositories explicit.

## Packages

| Package | Current role | Future extraction target |
| --- | --- | --- |
| `console` | OPL Console API, control-plane service, minimal commercial management model, PostgreSQL store, production readiness, production manifest validation, and Console UI | `opl-console` |
| `fabric` | Runtime provider factory and Local Docker / Tencent TKE / legacy Tencent CVM adapters | `opl-fabric` or `opl-fabric-adapters` |
| `ledger` | Tencent bill normalization, reconciliation guard helpers, and control-plane evidence receipt helpers; billing and evidence contracts are still called by Console service | `opl-ledger` |
| `contracts` | Machine-readable product, lifecycle, management, billing, storage backup, and evidence contracts shared by Console, Fabric, Workspace, and Ledger | shared contract package or product contract repository |

## Current Boundary

The repository still runs as one deployable OPL Console control-plane service:

```text
packages/console/api/server.js
```

The service may call Fabric and Ledger package code directly for now. New work should keep imports pointed at package boundaries instead of recreating cross-cutting code inside `console`.

## Extraction Rule

When a package becomes independently deployable, move it out with its tests and keep this repository depending on an API or contract:

- Console should depend on Workspace/Fabric/Ledger contracts.
- Fabric should own runtime execution and cloud adapter details.
- Ledger should own billing events, reconciliation guard semantics, and later provenance receipts.
- Storage backup contracts should stay shared: Console owns the user-visible operation and receipts, while Fabric owns the provider-specific snapshot/restore mechanics.
- Workspace runtime behavior remains owned by `one-person-lab-app`.

Do not move OPL Gateway internals, one-person-lab framework internals, or domain-agent marketplaces into this repository.
