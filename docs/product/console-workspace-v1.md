# OPL Console Workspace Product V1

## Target User

Target users are Lab Owners and administrators who create, fund, operate, and distribute OPL Workspaces.

The primary Lab Owner job is:

```text
sign in -> create Workspace -> confirm cost and hold -> copy URL -> share URL with members
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
- Create Workspace
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
- Workspace state: running, stopped, compute destroyed, storage retained, storage destroyed.
- Package, compute state, storage state, hourly estimate, and seven-day hold estimate.
- Create Workspace flow: name, package, confirmation, balance sufficiency.
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

## Workspace Creation

Creation flow:

1. Name.
2. Package.
3. Confirm.

Confirm shows:

- compute hourly price;
- storage price;
- seven-day hold;
- current balance;
- frozen balance;
- available balance;
- whether the Workspace can be opened.

## Billing Explanation

Billing UI explains wallet state, holds, recent debits, usage, and top-ups.

Raw ledger and dedup internals are not primary Lab Owner UI.
