# OPL Cloud Implementation Scope

This repository implements the OPL Cloud control-plane slice for OPL Console and OPL Workspace.

## Source Of Truth

- [`one-person-lab`](https://github.com/gaofeng21cn/one-person-lab) is the development framework source. Its framework concepts guide how this repository models runtime providers, attempts, readiness, receipts, recovery, contracts, and human-visible gates.
- [`one-person-lab-cloud`](https://github.com/gaofeng21cn/one-person-lab-cloud) is the OPL Cloud product definition. Its product matrix defines the Cloud product boundary: OPL Gateway, OPL Workspace, OPL Console, OPL Fabric, and OPL Ledger.
- This repository is the implementation workspace for the OPL Console and OPL Workspace control-plane subset of OPL Cloud.

## Implemented Product Slice

This repository is responsible for:

- OPL Console workspace provisioning and management.
- Minimal OPL Console commercial management model for users, organizations, memberships, billing accounts, packages, balances, and holds.
- OPL Workspace lifecycle control for local Docker, Tencent TKE, and legacy Tencent CVM runtimes.
- OPL Fabric handoff through Local Docker, Tencent TKE, TCR, Kubernetes Ingress, persistent workspace storage, and legacy Tencent CVM contracts.
- Long-lived Workspace URL token access. Tokens are permanent until the owner resets or deletes them after leakage.
- Compute and persistent workspace storage lifecycle separation.
- OPL Ledger records for prepaid compute/storage holds, hourly debits, hold releases, audit events, notifications, verifier output, and Tencent bill reconciliation.
- OPL Ledger evidence receipts v0 for control-plane actions that affect workspace access, runtime, storage, cost, or continuation.
- Runtime readiness, production readiness, and production chain verification.
- Deployment handoff assets for Tencent TKE, TCR image validation, Kubernetes Ingress, PostgreSQL, and legacy Tencent CVM.

## Package Layout

The implementation is staged for future repository extraction under `packages/`:

- `packages/console`: OPL Console API, control-plane service, management model, store, readiness, manifest validation, and UI.
- `packages/fabric`: runtime provider factory and Local Docker / Tencent TKE / legacy Tencent CVM adapters.
- `packages/ledger`: billing reconciliation helpers, evidence receipt helpers, and future Ledger extraction boundary.
- `packages/contracts`: machine-readable product, lifecycle, management, billing, and evidence contracts.

This is a migration layout, not a monorepo product claim. The service still deploys as one OPL Console control-plane process while imports are kept near the future package boundaries.

## Framework Alignment

The implementation should map Cloud behavior back to One Person Lab framework concepts:

| One Person Lab concept | This repository |
| --- | --- |
| Runtime provider | Local Docker provider, Tencent TKE production target, and Tencent CVM legacy fallback |
| Attempt / operation ledger | `runtime_operations` |
| Readiness gate | `/api/runtime/readiness` and `/api/production/readiness` |
| Receipt / audit trail | billing ledger, evidence ledger, audit events, verifier output, reconciliation output |
| Human gate | explicit compute and storage lifecycle confirmations |
| Recovery path | restart or recreate runtime compute from retained workspace storage |
| Machine-readable contract | `packages/contracts/`, tests, manifests, readiness payloads |

## Out Of Scope

This repository should not become the owner of:

- OPL Gateway internals.
- One Person Lab framework internals.
- one-person-lab-app desktop or WebUI product internals.
- OPL Ledger services beyond control-plane receipts, reconciliation references, and future integration boundaries.
- Capability pack marketplaces or domain-agent implementation details.

If a future change primarily belongs to one of those areas, keep this repository to the integration contract and move the implementation to the owning repository or service.

## Development Rule

When a production issue requires another safety check, first decide whether it belongs to:

1. A framework concept from `one-person-lab`.
2. A Cloud product boundary from `one-person-lab-cloud`.
3. This repository's Console / Workspace control-plane implementation.

Only implement it here when it is part of the third category or when this repository owns the integration boundary.
