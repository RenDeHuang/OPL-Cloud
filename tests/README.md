# Test Lifecycle

Tests in this repository are not automatically permanent.

## Long-Term Contract Tests

Long-term contract tests protect:

- Workspace lifecycle and URL access.
- Compute/storage separation.
- Storage backup and restore.
- User auth, role, disabled status, and tenant ownership.
- User wallet, holds, resource usage, request usage, idempotent debits, wallet transactions, manual top-up audit, billing ledger, and reconciliation.
- PostgreSQL persistence for commercial data objects.
- Runtime readiness and production manifest secret safety.
- Ledger receipt and evidence boundaries.
- Route/API contracts from `packages/contracts`.

## Temporary Tests

Temporary tests must have a removal condition.

Examples:

- migration guards;
- cleanup guards while deleting old routes;
- short-lived structure checks during a large split.

## Tests To Avoid

Avoid long-term tests that assert:

- prose wording;
- UI copy by raw source string search;
- arbitrary line counts;
- exact workflow text when a structured contract can express the rule;
- old compatibility routes after active callers move.

## Verification

Branch completion requires:

```bash
npm test
npm run build
git diff --check
```
