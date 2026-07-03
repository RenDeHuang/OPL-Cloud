# OPL Console Resource Product V1

## Target User

Target users are Lab Owners and administrators who create, fund, operate, and distribute OPL compute, storage, attachments, and Workspace URLs.

The primary Lab Owner job is:

```text
sign in -> open compute -> open storage -> attach storage -> create Workspace URL -> share URL with members
```

## Commercial Information Architecture

Public:

- Home
- Pricing
- Docs
- Status
- Login

Lab Owner Console:

- Overview
- Compute
- Storage
- Attachments
- Workspaces
- Create Workspace URL
- Workspace URL access
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
- ComputeResource list, detail, and creation.
- StorageVolume list, detail, and creation.
- StorageAttachment list, detail, and creation.
- Workspace URL copy, open, reset, and delete.
- Workspace state derived from compute, storage, attachment, URL token, and runtime readiness.
- Package, compute state, storage state, hourly estimate, and seven-day hold estimate.
- Create Workspace URL flow: select attachment, name entry, confirmation, runtime readiness.
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

Compute creation flow:

1. Name.
2. Package.
3. Confirm seven-day compute hold.
4. Create or expand the Tencent TKE node pool.

Storage creation flow:

1. Name.
2. Size.
3. Confirm seven-day storage hold.
4. Create the PVC/CBS-backed volume.

Attachment flow:

1. Select compute.
2. Select storage.
3. Confirm mount path.
4. Schedule the one-person-lab-app runtime onto the selected node pool and mount the selected volume.

Workspace URL flow:

1. Select attachment.
2. Name URL entry.
3. Confirm runtime readiness.
4. Copy or open the URL.

Confirm shows:

- compute hourly price;
- storage price;
- seven-day hold;
- current balance;
- frozen balance;
- available balance;
- whether the selected resource action can be completed.

## Billing Explanation

Billing UI explains wallet state, holds, recent debits, usage, and top-ups.

Raw ledger and dedup internals are not primary Lab Owner UI.
