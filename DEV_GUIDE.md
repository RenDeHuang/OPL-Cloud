# OPL Cloud Developer Guide

## Product Boundary

OPL Cloud develops and releases one portable product: Console, Control Plane,
Fabric, Ledger, and Workspace delivery. It publishes reusable source,
multi-architecture images, Compose assets, and GitHub Releases; it does not
deploy a concrete customer instance.

`opl-instance-medopl` is the separate owner for the medopl.cn profile. It owns
Tencent/TKE selection, production configuration and Secrets, deployment,
verification, rollback, and receipts while consuming an immutable OPL Cloud
product SHA and image digest.

Current implementation facts belong to
[implementation-architecture.md](docs/implementation-architecture.md) and
[status.md](docs/status.md). The only current P0 gap and its acceptance outcome
belong to [roadmap.md](docs/roadmap.md).

## MVP Development Path

The MVP has one vertical path:

```text
thin Console
  -> Control Plane Workspace orchestration
  -> local-docker Workspace provider
  -> OPL App/WebUI Workspace
  -> Sub2API-authoritative balance, usage, debit, and refund
```

The source already contains the thin Console and the Sub2API-backed Gateway
accounting surfaces. It does not yet contain the `local-docker` provider, so the
vertical path is not complete. Ledger records the required receipts and
reconciliation evidence; it never owns spendable balance.

## Local Console Preview

```bash
npm ci
npm run demo
```

The demo binds to `127.0.0.1`, uses in-memory fixtures, and makes no external
requests. It proves only the interaction preview.

## Portable Control Services

Use the release-owned Compose file and environment template to validate or run
PostgreSQL, Ledger, Fabric, and Control Plane:

```bash
docker compose --env-file deploy/portable/opl-cloud.env.example config --quiet
```

For an actual installation, use the three assets from one GitHub Release and
replace the template values as described in
[installation.md](docs/installation.md). A healthy Compose stack proves only
that the Cloud control services start. Workspace create, readback, access, and
delete remain unavailable until the real `local-docker` provider is implemented.

## Provider Adapters

Fabric owns provider-neutral resource operations and adapter boundaries. The
current source still wires the Tencent provider, which is an implementation
fact used by the medopl instance, not an OPL Cloud MVP prerequisite. Do not add
Tencent credentials, production domains, deployment dispatch, or instance
receipts to this repository.

## Ownership Rules

- Console calls only Control Plane product APIs.
- Control Plane owns Workspace orchestration and billing coordination.
- Fabric owns provider resources and provider adapters.
- Sub2API owns identity credentials, spendable balance, API Keys, routing, and
  request usage.
- Ledger owns append-only receipts and reconciliation evidence.

## Pre-Commit Checks

```bash
npm run validate:product-boundary
npm test
npm run typecheck
npm run lint
npm run build
npm run build:whitepaper
(cd services/control-plane && go test ./...)
(cd services/fabric && go test ./...)
(cd services/ledger && go test ./...)
git diff --check
```
