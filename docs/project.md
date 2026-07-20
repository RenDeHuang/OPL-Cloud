# Project Scope

This repository implements the OPL Cloud product defined by
`one-person-lab-cloud` and follows the development framework from
`one-person-lab`.

## Owned Here

- Console UI and its runtime route registry.
- Control Plane Sessions, account mapping, permissions, named product DTOs,
  Workspace state machines, purchase recovery, support, and product projections.
- Fabric resource catalog, Tencent CVM/CBS, attachments, runtime operations,
  provider evidence, content transfer, and snapshot boundary.
- Ledger receipts, reviews, artifacts, audit, retention, continuation, and
  reconciliation evidence.
- TKE manifests, deployment workflow, readiness, and reusable-slot verification.

## External

- Sub2API, reached only through the server-only configured management origin:
  spendable balance, API keys, models, routing, and request usage.
- `one-person-lab-app`: Workspace WebUI image and behavior.
- `one-person-lab`: framework and CLI behavior.
- Tencent Cloud: provider resources and internal cost.

## Explicit Non-Goals

- a second Gateway, wallet, Key store, Usage store, or billing-fact database;
- direct browser access or links to `OPL_SUB2API_BASE_URL`;
- identity mirroring beyond the one authoritative external-account mapping;
- generic downstream proxy routes in Control Plane;
- organization resource pools beyond account ownership and shared Workspace URLs;
- compatibility code for the deleted commercial model;
- speculative route, catalog, or business-object entries in current product contracts.
