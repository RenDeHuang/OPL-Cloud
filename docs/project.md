# Project Scope

This repository is the `one-person-lab-cloud` product and implementation owner.
It follows the development framework from `one-person-lab`. The short
`opl-cloud` identifier remains internal to packages and runtime artifacts.

## Owned Here

- Console UI and its runtime route registry.
- Control Plane Sessions, account mapping, permissions, named product DTOs,
  Workspace state machines, purchase recovery, support, and product projections.
- Fabric resource catalog, provider-neutral resource operations, attachments,
  runtime operations, provider evidence, and provider adapters, including the
  default local-Docker and explicit Tencent/TKE paths. ContentTransfer
  application runtime/API/schema is retired while historical migrations/data
  remain; Snapshot/Restore remains an extension candidate, not MVP Core.
- Ledger receipts and reconciliation evidence required by Core. Reviews,
  artifacts, retention, and continuation are extension candidates.
- Portable image, Compose installation assets, product release, readiness, and
  reusable provider-verification mechanisms.

## Instance Boundary

`opl-instance-medopl` owns the concrete medopl installation: domains, provider
profile, region and resource ids, enabled plans and prices, image pins, secret
references, promotion policy, and deployment receipts. It does not copy this
repository's runtime code or product contracts.

Medopl-specific manifests, production workflows, Secrets, runbooks, rollback,
canaries, and receipts belong only in that instance repository. This repository
retains provider adapter source and portable product-release mechanisms, but no
automatic instance deployment writer.

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
- a second OPL Cloud product, documentation, planning, or implementation repository.
