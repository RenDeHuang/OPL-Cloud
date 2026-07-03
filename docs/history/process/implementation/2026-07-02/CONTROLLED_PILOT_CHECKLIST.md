# OPL Cloud Controlled Pilot Checklist

Status: production pilot readiness checklist

This repository is the OPL Console control-plane implementation. It is ready for controlled production pilot only when the following checks stay true.

## 1. Destroyed Workspace Access

When persistent storage is destroyed, the Workspace URL must stop being usable.

Required behavior:

- `workspace.state=destroyed`
- `server.billingStatus=stopped`
- `disk.billingStatus=stopped`
- `access.tokenStatus=unavailable`
- token reset fails closed with `workspace_storage_destroyed`
- historical URL can remain in the record only as audit context

## 2. Operator Alerts And Minimal Monitoring

OPL Console must expose enough information for an operator to see failed openings, hold exhaustion, auto-stop events, and failed runtime operations.

Current surfaces:

- Console `Alerts` section from `state.notifications`
- `GET /api/operator/summary` with `x-opl-operator-token`
- `runtimeOperations` records with operation type, status, timestamps, and error
- production diagnostics workflow for TKE deployment state

Pilot rule:

- Workspace creation failure must record an error notification.
- Frozen-hold consumption must record a warning notification.
- Compute hold exhaustion must record auto-stop intent.
- Storage hold exhaustion must freeze Workspace state until top-up or explicit storage destruction.
- A failing Tencent bill reconciliation report must create an operator-visible `billing.reconciliation_guard_blocked` notification.

## 3. User Billing Rules

The user-visible rule is:

```text
Tencent resource cost * 1.20
```

Billing behavior:

- OPL Cloud freezes 7 days of compute and storage before opening or resuming a Workspace.
- Opening or resuming charges the first hour immediately.
- All usage rounds up to whole hours.
- Available balance is charged first.
- Frozen holds are consumed only after available balance is exhausted.
- Compute hold exhaustion stops compute.
- Storage hold exhaustion preserves data but freezes the Workspace until top-up or explicit storage destruction.
- Stopping compute releases unused compute hold.
- Destroying storage releases unused storage hold and stops storage billing.

## 4. Data Responsibility Boundary

Current production storage is a retained TKE PVC backed by Tencent CBS.

Pilot boundary:

- OPL Console controls lifecycle and billing state.
- OPL Workspace writes application data to mounted persistent storage.
- Storage destruction is irreversible from OPL Console's perspective.
- OPL Console can create retained storage backups through Kubernetes `VolumeSnapshot`.
- OPL Console can restore a backup into a new billable Workspace using a new PVC.
- Backup retention pruning deletes only snapshot objects and must never delete source or restored PVCs.
- Pilot users must still be told that explicit storage destruction deletes the active Workspace PVC; recovery depends on an available retained snapshot.

Future GA requirement:

- expose snapshot cadence and retention in the Console UI
- add restore verification evidence to the production verifier
- define deletion grace period
- define user-facing data loss responsibility

## 5. Tencent Bill Reconciliation Guard

Current production billing truth is OPL Ledger. Tencent bill reconciliation is the finance guard that proves OPL debits cover Tencent cost plus the configured markup.

Required behavior:

- `npm run reconcile:tencent` outputs a reconciliation report with a `guard` object.
- `POST /api/billing/reconciliation` stores the report in `billing_reconciliation_reports`.
- If the latest report fails, OPL Console blocks new Workspace creation.
- If the latest report fails, OPL Console blocks restore-to-new-Workspace from backup.
- Existing Workspace access, billing settlement, stop, destroy compute, and destroy storage remain available.
- Operator summary shows the active guard and recent global billing notification even when scoped to one account.

## 6. Tutorial Screenshot Refresh

Screenshot refresh is a documentation task, not an OPL Console control-plane implementation task.

Required screenshot set:

- OPL Gateway API key list
- OPL Gateway create key action
- OPL Gateway use-key modal
- Codex App / Codex CLI configuration tab
- GitHub Release assets download area
- DMG installation drag target
- OPL App access key input
- OPL App ready state
- OPL App entry screen

Rules:

- browser zoom 125%-150%
- crop to the action area
- hide all secret keys
- remove old names such as `gflab`
- remove old model names
- one screenshot should answer one user action

## 7. Launch Boundary

Current status:

```text
Controlled production pilot ready for Basic CPU Workspace.
Not GA.
Not full OPL Cloud launch.
```

What is proven:

- OPL Console can create a real TKE Workspace.
- TKE pulls the `one-person-lab-app` image.
- PVC is bound and mounted.
- Service endpoints are ready.
- Ingress routes the Workspace URL.
- Stop, restart, destroy compute, recreate from storage, billing settlement, and cleanup have passed production verifier.

What is not included in this pilot:

- public self-serve sales
- payment provider settlement
- GPU Workspace package
- full OPL Gateway product surface
- full OPL Ledger service beyond task evidence receipt v1 baseline
- OPL Connect, Environments, Agent Registry, or domain-agent marketplace
