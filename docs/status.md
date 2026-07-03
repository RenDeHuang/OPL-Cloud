# Status

## Current Launch Boundary

Current status: controlled commercial pilot for CPU Workspaces.

Supported:

- Lab Owner login.
- Admin login.
- Basic and Pro CPU Workspace packages.
- Workspace URL distribution.
- Compute stop, restart, destroy, and recreate from retained storage.
- Explicit storage destruction.
- Seven-day compute and storage holds.
- Resource usage, request usage, wallet transactions, manual top-up audit, billing ledger, and reconciliation records.
- Local Docker development provider.
- Tencent TKE production handoff.
- PostgreSQL persistence when `DATABASE_URL` is configured.

Not yet public GA:

- external payment settlement;
- GPU Workspaces;
- full OPL Gateway product surface;
- standalone OPL Ledger service;
- standalone OPL Fabric service;
- domain evidence judging and artifact registry;
- connector/environment/agent marketplaces.

## Current Gaps

- Retire compatibility-only account APIs and wallet mirror semantics.
- Convert implementation-shape tests into contract-driven tests or delete them.
- Move current price defaults into a versioned pricing contract.
- Keep production evidence in history or external ledgers, not active docs.
- Keep route contract limited to implemented or committed current routes.

## Required Verification

Before claiming a development branch is complete:

```bash
npm test
npm run build
git diff --check
```
