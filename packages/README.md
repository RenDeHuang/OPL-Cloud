# OPL Cloud Implementation Packages

This directory contains shared package boundaries only. Runtime ownership now lives under `services/*`; browser ownership lives under `apps/console-ui`.

## Packages

| Package | Current role | Runtime owner |
| --- | --- | --- |
| `contracts` | Four narrow cross-owner contracts for candidate/distribution identity, launch hashing, and Workspace Runtime ABI | Candidate/Release tooling, Control Plane, Fabric, and the instance handoff |

## Current Boundary

The current deployment contains three separate Go services and one browser
application:

```text
apps/console-ui
services/control-plane/cmd/control-plane/main.go
services/fabric/cmd/fabric/main.go
services/ledger/cmd/ledger/main.go
```

Console calls only Control Plane. Control Plane calls Fabric, Ledger, and
Sub2API through typed HTTP clients. Sub2API remains the sole customer identity,
wallet, Key, and Usage authority. Runtime services remain under `services/*`;
`packages/contracts` contains the shared machine contracts.

The Console experience principles live in
`docs/product/console-experience-guide.md`; current routes, components and
presentation live in `apps/console-ui`. Visual and navigation choices are not
machine contracts. Downstream authority never moves into the browser.

## Ownership Rule

- Console depends on Control Plane customer DTO contracts, never downstream DTOs.
- Fabric owns resource catalog, runtime execution, and cloud adapter details under `services/fabric`.
- Ledger owns receipts, reconciliation, retention, and opaque evidence under `services/ledger`.
- Control Plane owns Workspace state and monthly operations; compute, storage,
  and attachment rows are Workspace details and Fabric provider facts, not
  standalone customer purchase surfaces.
- The default Workspace runtime template remains `one-person-lab-app`; template behavior belongs to that app contract, not to Console billing or resource ownership.
