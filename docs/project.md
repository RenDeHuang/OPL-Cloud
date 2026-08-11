# Project Scope

This repository is the target `opl-cloud` implementation of the OPL Cloud
product defined by `one-person-lab-cloud` and follows the development framework
from `one-person-lab`.

## Owned Here

- Console UI and its runtime route registry.
- Control Plane Sessions, account mapping, permissions, named product DTOs,
  Workspace state machines, purchase recovery, support, and product projections.
- Fabric resource catalog, provider-neutral resource operations, attachments,
  runtime operations, provider evidence, content transfer, and snapshot
  boundary. Tencent TKE/CVM/CBS is the current production adapter.
- Ledger receipts, reviews, artifacts, audit, retention, continuation, and
  reconciliation evidence.
- Reusable image, deployment interface, readiness, and verification mechanisms.

## Instance Boundary

`opl-instance-medopl` owns the concrete medopl installation: domains, provider
profile, region and resource ids, enabled plans and prices, image pins, secret
references, promotion policy, and deployment receipts. It does not copy this
repository's runtime code or product contracts.

Those medopl-specific values are still co-located here as migration state. New
instance-specific product truth must target the instance repository rather than
deepening that coupling.

## External

- Sub2API, reached only through the server-only configured management origin:
  spendable balance, API keys, models, routing, and request usage.
- `one-person-lab-app`: Workspace WebUI image and behavior.
- `one-person-lab`: framework and CLI behavior.
- Tencent Cloud: current medopl provider resources and internal cost.

## Explicit Non-Goals

- a second Gateway, wallet, Key store, Usage store, or billing-fact database;
- direct browser access or links to `OPL_SUB2API_BASE_URL`;
- identity mirroring beyond the one authoritative external-account mapping;
- generic downstream proxy routes in Control Plane;
- organization resource pools beyond account ownership and shared Workspace URLs;
- compatibility code for the deleted commercial model;
- speculative route, catalog, or business-object entries in current product contracts.
- a second current Console, Fabric, or Ledger implementation repository.
