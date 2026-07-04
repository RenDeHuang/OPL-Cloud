# Status

## Current Launch Boundary

Current status: controlled commercial pilot for CPU Workspaces.

Supported:

- Lab Owner login.
- Admin login.
- Basic and Pro CPU Workspace packages.
- Package-level ComputePools.
- Account-owned dedicated CVM ComputeAllocations.
- Workspace URL distribution.
- Explicit compute allocation destruction.
- Independent storage creation before or after compute.
- Storage attachment to a compute allocation.
- Explicit storage destruction.
- Seven-day compute and storage holds.
- Resource usage, request usage, wallet transactions, manual top-up audit, billing ledger, and reconciliation records.
- Server-backed support ticket list, creation, detail, and admin queue.
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

## Product Gaps

- External payment settlement.
- GPU Workspace package.
- Full OPL Gateway key and quota product surface.
- Standalone OPL Fabric and OPL Ledger services.
- Connector, environment, and agent marketplaces beyond approved catalog shells.

## Repository Hygiene Rules

- Active docs describe current truth only.
- Machine contracts live in `packages/contracts/**`.
- Tests should read contracts or runtime outputs where possible.
- Temporary cleanup guards need an owner and removal condition.

## Required Verification

Before claiming a development branch is complete:

```bash
npm test
npm run build
git diff --check
```
