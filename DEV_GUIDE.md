# OPL Cloud Developer Guide

## Product Boundary

OPL Cloud develops and releases one portable product: Console, Control Plane,
Fabric, Ledger, and Workspace delivery. It publishes reusable source,
multi-architecture images, Compose assets, and GitHub Releases; it does not
deploy a concrete customer instance.

`opl-instance-medopl` is the separate owner for the medopl instance profile. It
explicitly binds the `.com` domains, Tencent/TKE profile, immutable Workspace
image, production configuration and Secrets, deployment, verification,
rollback, and receipts while consuming an immutable OPL Cloud product SHA and
image digest. Cloud source must not provide those instance defaults.

Current implementation facts belong to
[implementation-architecture.md](docs/implementation-architecture.md) and
[status.md](docs/status.md). Current P0 gaps and their acceptance outcomes belong
only to [roadmap.md](docs/roadmap.md).

## MVP Development Path

The MVP has one vertical path:

```text
thin Console
  -> Control Plane Workspace orchestration
  -> local-docker Workspace provider
  -> OPL App/WebUI Workspace
  -> Sub2API-authoritative balance, usage, and debit
```

The source contains the thin Console, the Sub2API-backed Gateway accounting
surfaces, and Fabric's `local-docker` provider with an isolated Docker
integration test. Closing the product Core path still requires the same-revision
Console-to-Workspace acceptance evidence described in the roadmap. Workspace
Delete performs no wallet or refund mutation. Ledger records the required
receipts and reconciliation evidence; it never owns spendable balance.

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
that the Cloud control services start; it does not by itself prove Workspace
create, readback, access, and delete through the local Docker provider.

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
npm run verify:local
```

The default gate needs no database. It validates the product boundary, Node
tests, Console typecheck/lint/build, whitepaper build, all four Go modules, and
Git whitespace. Go coverage means all-module compilation plus the explicitly
database-free package tests. Changes to persistence, capacity behavior, local Docker, or a
cross-service path also run the complete local gate:

```bash
npm run verify:local:full
```

The complete gate uses Docker to start an ephemeral PostgreSQL 16 container,
runs the PostgreSQL, capacity, and local-Docker integration tests with zero
skips, and removes the temporary container on exit. Neither gate accesses a
production network or dispatches an instance deployment.
