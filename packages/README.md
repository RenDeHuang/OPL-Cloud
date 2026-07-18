# OPL Cloud Implementation Packages

This directory contains shared package boundaries only. Runtime ownership now lives under `services/*`; browser ownership lives under `apps/console-ui`.

## Packages

| Package | Current role | Runtime owner |
| --- | --- | --- |
| `contracts` | Current machine-readable product, source, billing, resource, deployment, and evidence boundaries | Console/Control Plane, Fabric, Ledger, and the external Sub2API adapter |

## Current Boundary

The current deployment contains three separate Go services and one Vue browser
application:

```text
apps/console-ui
services/control-plane/cmd/control-plane/main.go
services/fabric/cmd/fabric/main.go
services/ledger/cmd/ledger/main.go
```

Console calls only Control Plane. Control Plane calls Fabric, Ledger, and
Sub2API through typed HTTP clients. Sub2API remains the sole customer identity,
wallet, Key, and Usage authority. Do not recreate runtime services, a wallet, or
a Gateway under `packages/*`.

## Ownership Rule

- Console depends on Control Plane customer DTO contracts, never downstream DTOs.
- Fabric owns resource catalog, runtime execution, and cloud adapter details under `services/fabric`.
- Ledger owns billing events, reconciliation guard semantics, control-plane evidence, and task evidence receipts under `services/ledger`.
- Control Plane owns Workspace state and monthly operations; compute, storage,
  and attachment rows are Workspace details and Fabric provider facts, not
  standalone customer purchase surfaces.
- The default Workspace runtime template remains `one-person-lab-app`; template behavior belongs to that app contract, not to Console billing or resource ownership.

Do not move OPL Gateway internals, one-person-lab framework internals, or domain-agent marketplaces into this repository.
