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
- `reserved`: future path that must not be treated as implemented.
- `retired`: historical path kept only in history, not here.

## Rules

1. Contracts preserve product and safety boundaries, not old process.
2. Compatibility aliases do not belong in current contracts.
3. Future placeholder routes must not be treated as implemented routes.
4. Tests should read contracts where possible instead of scanning source prose.
