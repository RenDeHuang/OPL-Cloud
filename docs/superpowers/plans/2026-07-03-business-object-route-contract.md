# Business Object Route Contract Plan

## Context

OPL Cloud currently lives in one repository, but the intended topology is three decoupled repositories:

- `opl-console`: UI, auth, route contracts, and read-model orchestration.
- `opl-fabric`: runtime, storage, connector, environment, and agent resource execution boundaries.
- `opl-ledger`: evidence, audit, reconciliation, and review policy boundaries.

The current Console route map contains a mix of static pages, implemented control-plane flows, parent-page subroutes, and future product objects. The contract needs to distinguish them so route coverage does not imply a business object is truly implemented.

## Rules

- Static content may remain API-free.
- Dynamic control-plane objects need read/write/action evidence before `implemented` status.
- Routes that are future product commitments but not implemented must be marked as `long_term_gap`.
- Routes that are only subviews rendered by a parent page must be marked as `folded_parent`.
- Placeholder routes that should be cleaned from active product claims must be marked as `dynamic_prune`.
- Console must only cross Fabric and Ledger through package/service boundaries.

## Tasks

1. Add contract tests for route kind, lifecycle, and dynamic object implementation requirements.
2. Add a business object contract for dynamic control-plane objects and future repo ownership.
3. Add UI clickability tests for menu navigation and Workspace list/detail/action flow.
4. Update the route/API contract with `routeKind`, `contractLifecycle`, `capabilities`, and `objectKind` metadata.
5. Keep support, invites, connector approvals, agent registry, and Ledger policy surfaces honest as gaps unless their API/action/evidence closure exists.
6. Run focused tests, full `npm test`, `npm run build`, `sentrux check .`, and `git diff --check`.
