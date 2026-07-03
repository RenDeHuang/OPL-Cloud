# Contract And Test Lifecycle Audit

Status: active cleanup audit.

## Purpose

Classify current docs, contracts, tests, and compatibility paths by lifecycle so this repository can follow the `one-person-lab` framework without carrying a long-term compatibility layer.

## Long-Term Docs

Keep active:

- `docs/project.md`
- `docs/architecture.md`
- `docs/invariants.md`
- `docs/decisions.md`
- `docs/status.md`
- `docs/product/console-workspace-v1.md`
- `docs/runtime/production-runbook.md`
- `docs/runtime/tke-production-deployment.md`
- `docs/policies/docs-lifecycle-policy.md`

## History Docs

Archived under `docs/history/process/implementation/2026-07-02/`:

- dated plans;
- dated design freezes;
- controlled pilot checklist;
- implementation goal ledger;
- implementation scope snapshot;
- production runbook snapshot;
- TKE deployment snapshot;
- product design freeze snapshot.

## Long-Term Contract Tests

Retain as long-term contract tests:

- billing holds, debits, request usage, wallet transactions, manual top-up audit, and reconciliation;
- Workspace lifecycle, URL access, storage backup, and restore;
- auth, session, role, tenant, disabled status, and persisted commercial identity;
- PostgreSQL persistence for current commercial data objects;
- runtime readiness and production manifest secret safety;
- Ledger evidence and human-readable receipt boundaries;
- route/API contract tests driven by machine-readable route contracts.

## Temporary Or Fragile Tests

Refactor or delete:

- source line-count tests;
- source string scans for UI copy;
- workflow regex tests that should be structured deployment contract tests;
- fixed historical price snapshot tests;
- legacy auth import tests;
- account wallet mirror tests;
- compatibility route tests for `/api/accounts/credit`.

## Contracts Needing Metadata Or Cleanup

- `opl-cloud-billing-ledger-contract.json`: remove legacy ledger aliases, add lifecycle metadata.
- `opl-cloud-management-contract.json`: make user wallet and billing ownership current truth, remove compatibility language.
- `opl-cloud-route-api-contract.json`: keep current implemented/committed routes, avoid treating future placeholders as active truth.
- `opl-cloud-product-contract.json`: add owner/purpose/state/lifecycle metadata.
- `opl-cloud-workspace-lifecycle-contract.json`: add owner/purpose/state/lifecycle metadata.

## Compatibility Paths To Delete

- Runtime wallet mirror writes into `state.accounts`.
- Legacy JSON user import as a durable auth source.
- Compatibility semantics for account crediting.
- Tests that prove the old compatibility layer still works.

## Migration Rule

One-time migration may read old state and write current state.

After migration, runtime code should mutate only the current commercial model.
