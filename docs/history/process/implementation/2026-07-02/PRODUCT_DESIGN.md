# OPL Cloud Product Design

Status: superseded product design record

Superseded by the active contracts in `packages/contracts/` and the current product notes in `docs/product/console-workspace-v1.md`. This record no longer defines product truth where it conflicts with account-owned ComputeAllocation, StorageVolume, StorageAttachment, stable Workspace URL, and RuntimeTemplate boundaries.

## Naming

OPL Cloud is the external product name. It means the online hosted version of OPL.

OPL Console is the management entry. It handles workspace creation, configuration, access links, billing, resource lifecycle, and settings.

OPL Workspace is the stable URL entry and lifecycle record users access. The current runtime behind that URL uses a RuntimeTemplate image, with `one-person-lab-app` as the default template.

Product copy, user-facing UI, and future design documents must use only the fixed OPL Cloud names.

## Target User

The target customer is a large PI, lab owner, or research group administrator who wants to create and distribute OPL workspaces for research work.

The primary job is workspace distribution. A PI creates an OPL Workspace in OPL Console, receives a URL, and shares that URL with members, students, or collaborators.

## Core Resource Model

The active OPL Workspace model maps to:

```text
1 OPL Workspace
= 1 persistent workspace storage volume
= 1 stable URL entry
= current ComputeAllocation/StorageAttachment runtime pointer
= RuntimeTemplate image for the deployed app
```

One PI account can own multiple OPL Workspaces:

```text
PI Account
  -> Workspace 1
     -> Server 1
     -> RuntimeTemplate 1
     -> Disk 1
     -> URL 1

  -> Workspace 2
     -> Server 2
     -> RuntimeTemplate 2
     -> Disk 2
     -> URL 2

  -> Workspace 3
     -> Server 3
     -> RuntimeTemplate 3
     -> Disk 3
     -> URL 3
```

Compute and persistent storage have separate lifecycles. Stopping or destroying compute must not destroy persistent storage. Storage is destroyed only after an explicit user action and confirmation.

## Product Responsibilities

OPL Cloud owns the online hosted product experience.

The fixed OPL Cloud product layers are:

- OPL Gateway: AI capability gateway, provider routing, token policy, and usage policy boundary.
- OPL Workspace: the stable URL entry backed by persistent storage and the current runtime pointer.
- OPL Console: the account, billing, access, settings, and lifecycle control surface.
- OPL Fabric: the compute, storage, image, route, and infrastructure handoff layer.
- OPL Ledger: billing ledger, audit events, usage receipts, verifier output, and reconciliation evidence.

OPL Console owns:

- workspace creation
- compute and storage package configuration
- workspace URL issuance
- URL copy/reset/delete actions
- compute stop/restart/destroy actions
- storage destroy action
- billing, pre-freeze, and cost records
- resource status and audit receipts

OPL Workspace owns:

- the actual `one-person-lab-app` WebUI
- the lab working environment
- files, project outputs, and runtime data stored on mounted persistent storage

## Repository Decoupling Boundary

This repository is the OPL Console control-plane implementation workspace. It may keep adapter code for OPL Workspace delivery, OPL Fabric handoff, and OPL Ledger v1 receipts while those interfaces are still small, but it must preserve service boundaries so they can move into separate repositories without rewriting product behavior.

The intended repository split is:

| Repository | Owns | This repository should depend on |
| --- | --- | --- |
| OPL Console | PI-facing management surface, Workspace lifecycle, billing display, readiness gates, deployment handoff | Workspace, Fabric, and Ledger contracts |
| OPL Workspace | `one-person-lab-app` runtime image, WebUI behavior, mounted data/project semantics, app-level token behavior | Workspace runtime contract |
| OPL Ledger | holds, debits, releases, audit receipts, notifications, reconciliation, verifier evidence | Ledger API or event contract |

OPL Fabric adapters can remain inside Console for v1 because TKE, TCR, PVC/CBS, Service, and Ingress are currently deployment handoff details. If Fabric grows into connector, environment, compute, or agent-package orchestration, move it behind a Fabric service contract instead of expanding Console into a general infrastructure platform.

## Workspace Creation Flow

The default creation flow is:

```text
PI signs in to OPL Console
-> Open ComputeAllocation and StorageVolume
-> Confirm hourly billing and seven-day resource holds
-> OPL Cloud creates the ComputeAllocation
-> OPL Cloud creates the persistent workspace storage volume
-> Attach storage to compute
-> Create OPL Workspace URL entry
-> OPL Cloud deploys the configured RuntimeTemplate image
-> OPL Cloud mounts persistent storage into the runtime
-> OPL Cloud configures the workspace URL
-> OPL Console shows the URL
-> PI copies the URL and shares it with members
-> Members open the URL and enter the OPL Workspace without login
```

The URL is the delivery artifact.

## URL and Access Model

Workspace URLs use the shared Workspace gateway host plus token:

```text
https://workspace.medopl.cn/w/<workspaceId>?token=<share-token>
```

The token is permanent by default. It remains valid until the workspace owner deletes or resets it.

Anyone with the URL can enter the OPL Workspace. Opening the URL does not require login.

OPL Console must provide:

- copy URL
- reset token
- delete token
- show token status

After restart, stop, or server recreation, the Workspace URL should remain stable whenever technically possible.

## Default Packages

OPL Cloud v1 production currently provides two CPU packages:

| Package | Compute | Cloud disk |
| --- | --- | --- |
| Basic Workspace | 2 CPU / 4GB | 10GB |
| Pro Workspace | 8 CPU / 16GB | 100GB |

GPU Workspaces are a future product package. They must not be exposed until a GPU node pool, image compatibility, scheduler behavior, pricing, billing holds, and production verifier evidence are all confirmed.

Custom compute and disk configuration can be added later, but the current production package list should stay narrow until the lifecycle, billing, and Workspace usability proofs are stable.

## Billing Rules

Billing is hourly.

The user-facing price is based on Tencent Cloud resource cost plus a 20% platform markup.

```text
user price = Tencent Cloud cost * 1.20
```

Workspace billing has two parts:

- compute hourly cost
- persistent storage cost

Cloud disk cost is shown in a user-friendly monthly form when helpful, but the system may internally convert it to hourly cost for ledger and pre-freeze calculations.

OPL Console must show:

- current package
- current hourly estimate
- compute billing status
- storage billing status
- frozen amount
- current balance or credit
- recent billing events

## Prepaid Hold Rule

Compute and storage must not enter unpaid operation.

Before creating a ComputeAllocation or StorageVolume, OPL Cloud freezes enough balance to cover seven days of that resource. Workspace creation validates already held compute/storage resources and does not create an additional Workspace hold.

If there is not enough balance for the resource prepaid hold, that compute or storage resource cannot be opened.

Hourly debits charge available balance first. If available balance is exhausted, OPL Cloud notifies the operator and starts consuming the relevant frozen hold.

If compute hold is exhausted, OPL Cloud stops compute. If storage hold is exhausted, OPL Cloud preserves storage and freezes the Workspace state until the user adds balance or explicitly destroys storage.

Stopping compute releases unused compute hold. Storage hold remains because persistent storage still holds Workspace data.

Destroying storage releases unused storage hold and stops future storage billing after the destroy action completes.

## Compute and Storage Lifecycle

Compute and storage are independent resources.

### Running

Compute is active, the configured RuntimeTemplate runtime is ready, persistent storage is mounted, and the URL is usable.

Billing:

- compute billing active
- storage billing active

Allowed actions:

- copy URL
- stop compute
- restart compute
- destroy compute
- destroy storage with confirmation

### Stopped

Compute is stopped and compute billing stops. Storage is retained.

Billing:

- compute billing stopped
- storage billing active

Allowed actions:

- restart compute
- destroy compute
- destroy storage with confirmation
- copy URL, but opening the URL should show that the Workspace is stopped or unavailable until restored

### Compute Destroyed, Storage Retained

Compute has been deleted. Storage remains.

Billing:

- compute billing stopped
- storage billing active

Allowed actions:

- recreate compute from retained storage
- destroy storage with confirmation

### Storage Destroyed

Storage has been explicitly destroyed.

Billing:

- compute billing stopped
- storage billing stopped

Allowed actions:

- no restore from this storage
- create a new Workspace if needed

Access:

- Workspace URL is no longer usable.
- token status is `unavailable`.
- token reset must fail closed because there is no retained Workspace storage to reattach.

## Workspace State Model

Workspace state is the standard status used by OPL Console to decide what the user sees, what actions are allowed, and what billing should happen.

User-facing states:

```text
Creating
Running
Stopping
Stopped
Restarting
Compute destroyed, storage retained
Storage hold exhausted
Destroying storage
Destroyed
Failed
```

Internal states:

```text
draft
freezing_prepaid_hold
creating_server
creating_disk
deploying_docker
configuring_url
running
stopping_server
stopped_server_disk_retained
storage_hold_exhausted
stopped_storage_hold_exhausted
restarting_server
destroying_server
server_destroyed_disk_retained
destroying_disk
destroyed
failed
cleanup_required
```

The state model prevents invalid actions. For example, OPL Console must not allow storage destruction without confirmation, and it must not treat compute destruction as storage destruction.

## Destructive Action Protection

Compute stop requires confirmation.

Compute destroy requires confirmation and must clearly say that storage remains.

Storage destroy requires a stronger confirmation because it deletes Workspace data. The user must explicitly confirm that Workspace data will be lost.

Storage destroy is the only action that stops storage billing.

## OPL Workspace Data

`one-person-lab-app` stores Workspace data on mounted persistent storage.

Persistent storage is the primary persistence layer for:

- uploaded files
- project files
- generated outputs
- runtime artifacts
- local application state

A database may be added for metadata, session records, indexing, and audit references. The database is not the primary store for large Workspace files.

For v1, the default assumption is:

```text
RuntimeTemplate image
-> mounted persistent storage at /data and /projects
-> application data persists on storage
```

## OPL Console Pages

V1 OPL Console should include:

- Workspace list
- Create Workspace
- Workspace detail
- Workspace URL and token controls
- Compute controls
- Storage controls
- Billing and freeze details
- Audit receipts
- Account settings

The member experience is intentionally minimal:

```text
Open Workspace URL
-> enter OPL Workspace
```

Members do not see compute controls, cloud resource configuration, billing, or audit pages.

## Non-Goals for V1

V1 does not include:

- member login for shared URLs
- per-member roles inside OPL Console
- arbitrary custom package marketplace
- exposing Kubernetes primitives to ordinary users
- GPU Workspace packages before a verified GPU node pool exists
- multi-server Workspace clusters
- automatic storage deletion when compute is stopped or destroyed
- unpaid storage grace operation
- external payment provider settlement unless separately scoped

## V1 Product Acceptance Criteria

The product design is satisfied when OPL Console can support this flow:

```text
PI creates Workspace
-> selects a Basic or Pro CPU package
-> confirms billing and compute/storage prepaid hold
-> receives a stable URL with token
-> shares URL with members
-> members enter OPL Workspace without login
-> PI can stop compute while retaining storage
-> PI can restart from retained storage
-> PI can destroy compute without destroying storage
-> PI can explicitly destroy storage with confirmation
-> OPL Console shows billing and audit receipts
```
