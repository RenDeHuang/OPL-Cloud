# OPL Console Workspace Product V1

## Target User

Target users are Lab Owners and administrators who create, fund, operate, and distribute OPL Workspaces.

The primary Lab Owner job is:

```text
sign in -> open compute allocation -> open or select storage -> attach storage -> create Workspace URL -> copy URL -> share URL with members
```

## Commercial Information Architecture

Public:

- Home
- Pricing
- Docs
- Status
- Login
- Register
- Email verify
- Forgot password
- Reset password

Lab Owner Console:

- Overview
- Workspaces
- Compute
- Storage
- Attachments
- Create Workspace URL
- Workspace access
- Gateway usage summary
- Billing wallet
- Account and Lab
- Support
- Alerts
- Human-readable receipts

Admin:

- Overview
- Users
- User wallet
- Manual top-ups
- Governance policies
- Audit
- Runtime readiness
- Fabric catalog internals
- Ledger events and receipts
- Support queue

## Lab Owner Surface

Lab Owner sees:

- Workspace list.
- Workspace URL copy, open, reset, and delete.
- Workspace URL state and runtime readiness.
- Package, ComputePool, ComputeAllocation state, storage state, hourly estimate, and seven-day hold estimate.
- Compute creation flow: package, hourly price, hold, balance sufficiency, provisioning status, and failure details.
- Storage creation flow: capacity, GB-month price, hourly estimate, hold, balance sufficiency, provisioning status, and failure details.
- Attachment flow: selected compute allocation, selected storage volume, mount path, runtime image, and Workspace URL behavior.
- Billing: balance, frozen amount, available balance, recent charges, usage, and top-ups.
- Support tickets and alerts.

Lab Owner must not see:

- request fingerprint;
- dedup rows;
- raw runtime evidence;
- production readiness;
- manual settlement;
- raw Ledger events.

## Admin Surface

Admin sees:

- users and disabled status;
- roles and ownership;
- manual recharge;
- wallet transaction history;
- manual top-up audit;
- runtime and production readiness;
- Fabric resource catalog internals;
- raw Ledger evidence;
- support queue.

## Resource Creation

Creation flow:

1. Select package and open one dedicated CVM ComputeAllocation from its ComputePool.
2. Open or select a StorageVolume.
3. Attach the StorageVolume to the ComputeAllocation.
4. Create the Workspace URL entry.

Confirm shows:

- compute hourly price;
- storage price;
- seven-day hold;
- current balance;
- frozen balance;
- available balance;
- provisioning status and whether the Workspace URL can be opened.

## Billing Explanation

Billing UI explains wallet state, holds, recent debits, usage, and top-ups.

Raw ledger and dedup internals are not primary Lab Owner UI.
