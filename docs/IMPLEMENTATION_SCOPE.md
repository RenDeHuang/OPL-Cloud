# OPL Cloud Implementation Scope

This repository implements the OPL Cloud control-plane slice for OPL Console and OPL Workspace.

## Source Of Truth

- [`one-person-lab`](https://github.com/gaofeng21cn/one-person-lab) is the development framework source. Its framework concepts guide how this repository models runtime providers, attempts, readiness, receipts, recovery, contracts, and human-visible gates.
- [`one-person-lab-cloud`](https://github.com/gaofeng21cn/one-person-lab-cloud) is the OPL Cloud product definition. Its product matrix defines the Cloud product boundary: OPL Gateway, OPL Console, OPL Workspace, and planned Evidence Services.
- This repository is the implementation workspace for the OPL Console and OPL Workspace control-plane subset of OPL Cloud.

## Implemented Product Slice

This repository is responsible for:

- OPL Console workspace provisioning and management.
- OPL Workspace lifecycle control for local Docker and Tencent CVM runtimes.
- Workspace URL and token access.
- Server and cloud disk lifecycle separation.
- Billing ledger, storage hold, metering events, and Tencent bill reconciliation.
- Runtime readiness, production readiness, and production chain verification.
- Deployment handoff assets for Tencent CVM, CBS, Caddy, Harbor image validation, PostgreSQL, and OpenMeter.

## Framework Alignment

The implementation should map Cloud behavior back to One Person Lab framework concepts:

| One Person Lab concept | This repository |
| --- | --- |
| Runtime provider | Local Docker provider and Tencent CVM provider |
| Attempt / operation ledger | `runtime_operations` |
| Readiness gate | `/api/runtime/readiness` and `/api/production/readiness` |
| Receipt / audit trail | billing ledger, audit events, verifier output, reconciliation output |
| Human gate | explicit server and disk lifecycle confirmations |
| Recovery path | restart and recreate server from retained disk |
| Machine-readable contract | `contracts/`, tests, manifests, readiness payloads |

## Out Of Scope

This repository should not become the owner of:

- OPL Gateway internals.
- One Person Lab framework internals.
- one-person-lab-app desktop or WebUI product internals.
- Evidence Services beyond control-plane references, receipts, and future integration boundaries.
- Capability pack marketplaces or domain-agent implementation details.

If a future change primarily belongs to one of those areas, keep this repository to the integration contract and move the implementation to the owning repository or service.

## Development Rule

When a production issue requires another safety check, first decide whether it belongs to:

1. A framework concept from `one-person-lab`.
2. A Cloud product boundary from `one-person-lab-cloud`.
3. This repository's Console / Workspace control-plane implementation.

Only implement it here when it is part of the third category or when this repository owns the integration boundary.
