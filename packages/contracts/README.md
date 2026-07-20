# OPL Cloud Contracts

Machine-readable contracts are the durable source for behavior that tests should enforce.

Each contract should declare:

- `schemaVersion`
- `owner`
- `purpose`
- `state`
- `machineBoundary`
- `lifecycle`

## Lifecycle

- `current`: active contract for current implementation or Pilot operations.
- `migration`: temporary migration contract with a removal condition.
- `superseded`: historical shape retained in place for audit context; it is not
  an implementation or UI authority and must name its replacement.

## Rules

1. Contracts preserve product and safety boundaries, not old process.
2. Compatibility aliases do not belong in current contracts. Internal
   one-to-one persistence records must be labeled as compatibility-only.
3. Tests should read contracts where possible instead of scanning source prose.
4. Deployment workflow and image checks belong in `opl-cloud-deployment-contract.json`.
5. Package import and service boundary checks belong in `opl-cloud-package-boundary-contract.json`.
6. Shared execution identities, states, write semantics, ownership, and errors belong in `opl-cloud-shared-execution-contract.json`.
7. Product reads reuse `SourceEnvelope<T>` and the server-side
   `writeSourceEnvelope`; do not create per-product envelope types.
8. `source`, `status`, `available`, and `fetchedAt` report the actual read. Return
   `sourceUpdatedAt` only when the authority provides it; local time is not a
   substitute.
9. A target contract field does not prove delivery. `codeComplete`, `pilotReady`,
   and `productionProven` advance only with their matching code and evidence.
