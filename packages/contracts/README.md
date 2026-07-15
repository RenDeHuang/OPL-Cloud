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

- `current`: active contract for current implementation.
- `migration`: temporary migration contract with a removal condition.
- `retired`: historical path kept only in history, not here.

## Rules

1. Contracts preserve product and safety boundaries, not old process.
2. Compatibility aliases do not belong in current contracts.
3. Tests should read contracts where possible instead of scanning source prose.
4. Deployment workflow and image checks belong in `opl-cloud-deployment-contract.json`.
5. Package import and service boundary checks belong in `opl-cloud-package-boundary-contract.json`.
6. Shared execution identities, states, write semantics, ownership, and errors belong in `opl-cloud-shared-execution-contract.json`.
