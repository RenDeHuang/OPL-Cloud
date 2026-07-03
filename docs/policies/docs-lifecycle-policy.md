# Documentation Lifecycle Policy

This repository adopts the `one-person-lab` documentation lifecycle.

## Active Documentation

Active docs describe current product behavior, architecture boundaries, operating rules, or current launch gaps. They must be short enough to stay maintained.

Active docs must not store:

- dated execution logs,
- branch-specific implementation plans,
- completed closeout notes,
- raw production verification output,
- temporary smoke reports,
- old compatibility stories.

## History

Move completed or dated process material to `docs/history/**`.

History preserves provenance. It is not an active implementation contract.

## Contracts

Machine-readable contracts live in `packages/contracts/**`.

Each contract should state:

- `schemaVersion`
- `owner`
- `purpose`
- `state`
- `machineBoundary`
- `lifecycle`

Contracts should preserve product boundaries, permissions, safety rules, receipt shape, recovery rules, and lower-bound projection guarantees. They should not preserve old implementation process.

## Tests

Tests are classified by lifecycle:

- `long_term_contract`: domain or API behavior that must remain true.
- `migration_guard`: temporary guard for a one-time data or API migration.
- `cleanup_guard`: short-lived guard used while deleting legacy paths.
- `implementation_shape`: source-structure or workflow-shape check that must either become a contract test or be retired.

Temporary tests need an owner and removal condition.

## No Compatibility Layer Rule

This repository should retire old wrappers directly once active callers move to the current surface.

Allowed:

- one-time migration code that upgrades persisted state;
- history notes explaining retired paths;
- contract tests for the new surface.

Not allowed as long-term state:

- duplicate API routes kept only for old callers;
- account/user wallet mirror writes;
- compatibility aliases in contracts;
- tests that assert old paths still work after the migration is complete.
