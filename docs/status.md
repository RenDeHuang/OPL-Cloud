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
- Holds, hold releases, resource settlements, wallet transactions, manual top-up audit, billing ledger, evidence receipts, and reconciliation records.
- PostgreSQL-backed Organizations and Memberships with fixed `owner`, `admin`, and `member` roles and server-side tenant isolation for customer APIs.
- Standalone Go Fabric and Ledger services with PostgreSQL persistence and typed Control Plane clients.
- General execution receipts, Artifact Manifests, Review Results, and receipt continuation references.
- Monotonic receipt retention, legal holds, privacy redaction evidence, and idempotent memory/PostgreSQL retention operations.
- Canonical project/task identities, cursor-based Workspace metadata sync, persisted conflict records, and explicit conflict resolution.
- Resumable, chunked Workspace content upload and download with per-chunk and final digest verification.
- Workspace backup, export, restore, clone, and backup destruction APIs backed by Fabric snapshot operations.
- External support ticket mapping API and admin lookup queue.
- Local-to-staging operator mode using the same staging PostgreSQL and Tencent TKE resource pool as cloud staging.
- Tencent TKE production handoff.
- PostgreSQL persistence for Control Plane, Fabric, and Ledger when their database URLs are configured.

Not yet public GA:

- external payment settlement;
- GPU Workspaces;
- production Workspace snapshot and disaster-recovery readiness;
- OPL Gateway integration beyond the current external link;
- domain reviewer policy and artifact byte storage;
- connector/environment/agent marketplaces.

The Workspace recovery contract and APIs are implemented, but production snapshot readiness is blocked on the current TKE cluster. Fabric uses the GA `snapshot.storage.k8s.io/v1` contract while the installed Tencent CBS snapshot components expose only `v1beta1` (the latest available addon, `1.1.17`, still uses snapshot sidecars `v3.0.6`). Do not claim production backup or disaster-recovery readiness until the cluster exposes the GA API and a restore drill passes. Fabric must not add a `v1beta1` compatibility path.

## Product Gaps

- External payment settlement.
- GPU Workspace package.
- Production Workspace snapshot support and a real backup/restore disaster drill.
- OPL Gateway identity, key projection, quota, usage, and Ledger settlement integration.
- Human-readable receipt pages and export.
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

Before cloud staging rollout:

```bash
npm run staging:readiness
OPL_CONFIRM_REAL_CLOUD_E2E=1 npm run staging:e2e
```

After cloud staging rollout:

```bash
OPL_CONSOLE_ORIGIN=https://<console-domain> npm run verify:production
```
