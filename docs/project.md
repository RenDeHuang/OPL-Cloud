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
  default local-Docker and explicit Tencent/TKE paths. Historical migrations
  and data preserve ContentTransfer and Snapshot/Restore custody.
- Ledger receipts, reconciliation evidence, idempotency, retention, and
  caller-owned opaque provenance required by Core, including custody of
  historical rows and receipt provenance columns.
- Portable image, Compose installation assets, product release, readiness, and
  reusable provider-verification mechanisms.

## Instance Boundary

`opl-instance-medopl` owns the concrete medopl installation: domains, provider
profile, region and resource ids, the enabled subset of Cloud-defined plans,
image pins, secret references, promotion policy, and deployment receipts. Cloud
Control Plane owns the versioned customer price catalog. The Instance does not
copy this repository's runtime code or product contracts.

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
