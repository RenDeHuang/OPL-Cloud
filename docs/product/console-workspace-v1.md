# OPL Console Workspace Product V2

This document owns the Workspace product capability boundary. The
[Console experience guide](console-experience-guide.md) describes durable user
outcomes without freezing navigation or visual implementation; APIs and domain
contracts own field-level facts and permissions.

## User Job

```text
register -> sign in -> observe zero balance -> receive administrator top-up
         -> list Workspaces -> select Basic or Pro -> confirm one Workspace total
         -> provision -> reveal/copy that Workspace access -> open Workspace
```

The public beta allows one customer to register one Account and create multiple
independent Workspaces after an administrator funds its Sub2API wallet. A new
Account starts at zero balance, and registration performs no purchase or Fabric
mutation. Each Workspace has its own launch identity, resources, Key, Secret,
entitlement, Runtime, and Receipt.

## Owner Surface

Console shows:

- live Sub2API USD balance;
- fixed Basic or Pro Workspace package price in USD;
- general Gateway Key create, enable/disable, delete, reveal/copy, and per-Key
  Usage readback;
- resource status, `paidThrough`, auto-renew, and manual-review state;
- Workspace access, billing receipts, announcements, support, and account
  settings.

The Workspace access area answers, in one place and from owner readback: URL,
用户名, 密码 reveal/copy, and the corresponding Workspace Key reveal/copy. The
Workspace Key reuses `POST /api/gateway/keys/{keyId}/reveal`; it does not create a
second secret store or Key API. Console does not expose a Gateway base-address
card or link to the server-only Sub2API backend.

Console does not show raw request fingerprints, provider credentials, generic
Fabric/Ledger APIs, or Sub2API admin operations.

## Admin Surface

Operations sees account mappings, roles, wallet recharge/debit/business refund,
receipt and review evidence, reconciliation reports, readiness, announcements,
and only server-authorized reconciliation or recovery actions. Resource rows
show owner account/user, Workspace, resource type, package/spec, provider ID,
Zone, status, created and expiry times, last readback, and operation/Receipt
references. A missing owner source displays unavailable; Fabric or Ledger facts
are not copied into a new Control Plane truth table.

## Purchase Confirmation

Workspace confirmation shows the selected package/spec, exact total USD charge,
current balance, entitlement period, and the compute/storage fulfillment included
in that total.

The operation is resumable. Read-only provider preflight failure or insufficient
balance stops before debit and before every Fabric write. Ambiguous external
results enter manual review.
Confirmed single charge activates the Workspace after fulfillment and emits one
purchase Receipt. Compute and storage never debit the customer independently.

## Workspace And Storage

A Workspace is a stable URL backed by one independently owned StorageVolume and
the current runtime pointer. The Pilot Console does not expose customer backup,
recovery, transfer, provider-resource replacement, or storage deletion. Unpaid
expiry denies access and performs zero Fabric or Tencent resource mutation.

## Evidence Levels

Contract/UI presence is not availability. `code-complete` requires the complete
local gate, `pilot-ready` requires approved real Pilot evidence, and
`production-proven` requires evidence from the deployed immutable revision.
