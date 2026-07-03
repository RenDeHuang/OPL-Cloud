# Invariants

## Workspace Resource Mapping

```text
1 OPL Workspace
= 1 runtime compute unit
= 1 one-person-lab-app runtime container
= 1 persistent workspace storage volume
= 1 URL
```

One Lab Owner can own multiple OPL Workspaces.

## Access

Workspace URLs are long-lived token URLs:

```text
https://workspace.medopl.cn/w/<workspaceId>?token=<share-token>
```

Opening a Workspace URL does not require member login. The token remains valid until the owner resets or deletes it.

Workspace summaries, readiness reports, and public API responses must not leak the share token unless the route is explicitly authenticated and scoped to the owner/admin.

## Compute And Storage

Compute and persistent storage have separate lifecycles.

Stopping or destroying compute must not destroy persistent storage.

Storage destruction requires explicit confirmation and is the only action that stops storage billing.

## Billing

Billing is hourly.

Before opening or resuming a Workspace, OPL Cloud freezes enough balance for 7 days of compute and persistent storage.

Debits charge available balance first, then the relevant frozen hold.

If compute hold is exhausted, compute stops.

If storage hold is exhausted, storage is preserved and the Workspace is frozen until top-up or explicit storage destruction.

Manual top-ups must create both a wallet transaction and a manual top-up audit record.

Usage debits must be idempotent by source event or request fingerprint.

## Permissions

Lab Owner sees Workspace distribution, URL actions, package, state, balance, bill explanation, usage, support, alerts, and human-readable receipts.

Admin sees users, roles, manual recharge, audit, runtime readiness, production readiness, raw Ledger evidence, Fabric internals, and support queues.

Lab Owner must not see request fingerprints, dedup rows, runtime evidence, production readiness, manual settlement, or raw Ledger event internals.

## Runtime

Production readiness fails closed until runtime provider, registry image, workspace domain, PostgreSQL, Tencent configuration, required host tools, and auth seed/persistence requirements are satisfied.

Production manifests must not inline secrets. Sensitive values must use secret references or mounted secret files.
