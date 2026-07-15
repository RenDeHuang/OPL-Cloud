# OPL Console Workspace Product V1

## User Job

```text
sign in -> buy compute -> buy/select storage -> attach -> create Workspace URL
        -> open/copy/share URL
```

The owner can repeat this flow for multiple Workspaces. Sharing is the Workspace
URL capability itself; it does not require an organization resource-pool model.

## Owner Surface

Console shows:

- live Sub2API USD balance;
- fixed CNY monthly reference prices;
- Basic compute;
- storage in 10 GB steps;
- resource status, `paidThrough`, auto-renew, and manual-review state;
- attachment and Workspace URL actions;
- billing receipts, support, and account settings.

Console does not show raw request fingerprints, provider credentials, generic
Fabric/Ledger APIs, or Sub2API admin operations.

## Admin Surface

Admin sees account mappings, roles, resource/provider facts, receipt and review
evidence, reconciliation reports, readiness, and explicit cleanup operations.
Admin does not mutate balance through Console.

## Purchase Confirmation

Compute confirmation shows the selected package, fixed monthly price, exact USD
charge, current balance, and entitlement period. Storage confirmation also shows
the 10 GB block count.

The operation is resumable. Provider preparation failure makes no charge.
Insufficient balance cleans the prepared resource. Ambiguous external results
enter manual review. Confirmed charge activates the entitlement and emits a
Ledger receipt.

## Workspace And Storage

A Workspace is a stable URL backed by one StorageVolume and the current runtime
pointer. Compute can be replaced without changing the Workspace URL or deleting
storage. Storage deletion is explicit and irreversible.
