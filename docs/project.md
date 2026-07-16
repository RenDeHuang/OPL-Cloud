# Project Scope

This repository implements the OPL Cloud product defined by
`one-person-lab-cloud` and follows the development framework from
`one-person-lab`.

## Owned Here

- Console UI and its runtime route registry.
- Control Plane auth, account mapping, organizations, Workspaces, monthly
  entitlement, purchase recovery, support, and product projections.
- Fabric resource catalog, Tencent CVM/CBS, attachments, runtime operations,
  provider evidence, content transfer, and snapshot boundary.
- Ledger receipts, reviews, artifacts, audit, retention, continuation, and
  reconciliation evidence.
- TKE manifests, deployment workflow, readiness, and reusable-slot verification.

## External

- Sub2API at `gflabtoken.cn`: balance, API keys, model routing, and request usage.
- `one-person-lab-app`: Workspace WebUI image and behavior.
- `one-person-lab`: framework and CLI behavior.
- Tencent Cloud: provider resources and internal cost.

## Explicit Non-Goals

- a second Gateway service or database in this repository;
- Sub2API key CRUD, request-usage sync, or identity mirroring;
- generic downstream proxy routes in Control Plane;
- organization resource pools beyond account ownership and shared Workspace URLs;
- compatibility code for the deleted commercial model;
- speculative route, catalog, or business-object entries in current product contracts.
