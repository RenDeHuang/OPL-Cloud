# OPL Cloud Product Design

Status: v1 product design freeze

## Naming

OPL Cloud is the external product name. It means the online hosted version of OPL.

OPL Console is the management entry. It handles workspace creation, configuration, access links, billing, resource lifecycle, and settings.

OPL Workspace is the real working environment users access. It is delivered as a URL and runs one dedicated `one-person-lab-app` Docker instance.

The old internal product name must not be used in product copy, user-facing UI, or future design documents.

## Target User

The target customer is a large PI, lab owner, or research group administrator who wants to create and distribute OPL workspaces for research work.

The primary job is workspace distribution. A PI creates an OPL Workspace in OPL Console, receives a URL, and shares that URL with members, students, or collaborators.

## Core Resource Model

One OPL Workspace maps to exactly:

```text
1 OPL Workspace
= 1 server
= 1 one-person-lab-app Docker container
= 1 cloud disk
= 1 URL
```

One PI account can own multiple OPL Workspaces:

```text
PI Account
  -> Workspace 1
     -> Server 1
     -> Docker 1
     -> Disk 1
     -> URL 1

  -> Workspace 2
     -> Server 2
     -> Docker 2
     -> Disk 2
     -> URL 2

  -> Workspace 3
     -> Server 3
     -> Docker 3
     -> Disk 3
     -> URL 3
```

Servers and cloud disks have separate lifecycles. Stopping or destroying a server must not destroy the cloud disk. The cloud disk is destroyed only after an explicit user action and confirmation.

## Product Responsibilities

OPL Cloud owns the online hosted product experience.

OPL Console owns:

- workspace creation
- server and disk configuration
- workspace URL issuance
- URL copy/reset/delete actions
- server stop/restart/destroy actions
- cloud disk destroy action
- billing, pre-freeze, and cost records
- resource status and audit receipts

OPL Workspace owns:

- the actual `one-person-lab-app` WebUI
- the lab working environment
- files, project outputs, and runtime data stored on the mounted cloud disk

## Workspace Creation Flow

The default creation flow is:

```text
PI signs in to OPL Console
-> Create OPL Workspace
-> Choose server package
-> Choose cloud disk package
-> Confirm hourly billing and 7-day storage pre-freeze
-> OPL Cloud creates the server
-> OPL Cloud creates the cloud disk
-> OPL Cloud deploys one-person-lab-app Docker
-> OPL Cloud mounts the cloud disk into the Docker runtime
-> OPL Cloud configures the workspace URL
-> OPL Console shows the URL
-> PI copies the URL and shares it with members
-> Members open the URL and enter the OPL Workspace without login
```

The URL is the delivery artifact.

## URL and Access Model

Workspace URLs use a workspace subdomain plus token:

```text
https://<workspace-slug>.oplcloud.cn/?token=<share-token>
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

OPL Cloud v1 provides two default packages:

| Package | Server | Cloud disk |
| --- | --- | --- |
| Basic Workspace | 2c / 4GB | 10GB |
| Pro Workspace | 8c / 16GB | 100GB |

Custom server and disk configuration can be added later, but v1 should lead with these two packages.

## Billing Rules

Billing is hourly.

The user-facing price is based on Tencent Cloud resource cost plus a 10% platform markup.

```text
user price = Tencent Cloud hourly cost * 1.10
```

Workspace billing has two parts:

- server hourly cost
- cloud disk storage cost

Cloud disk cost is shown in a user-friendly monthly form when helpful, but the system may internally convert it to hourly cost for ledger and pre-freeze calculations.

OPL Console must show:

- current package
- current hourly estimate
- server billing status
- cloud disk billing status
- frozen amount
- current balance or credit
- recent billing events

## Storage Pre-Freeze Rule

Storage must not enter unpaid operation.

Before opening or continuing a Workspace, OPL Cloud freezes enough balance to cover 7 days of cloud disk storage.

If there is not enough balance for the 7-day storage freeze, the Workspace cannot be opened or resumed.

Stopping the server does not release the storage freeze. The cloud disk remains billable and protected because it still holds Workspace data.

Destroying the cloud disk releases future storage billing after the destroy action completes.

## Server and Storage Lifecycle

The server and cloud disk are independent resources.

### Running

The server is active, the Docker runtime is running, the cloud disk is mounted, and the URL is usable.

Billing:

- server billing active
- storage billing active

Allowed actions:

- copy URL
- stop server
- restart server
- destroy server
- destroy cloud disk with confirmation

### Stopped

The server is stopped and server billing stops. The cloud disk is retained.

Billing:

- server billing stopped
- storage billing active

Allowed actions:

- restart server
- destroy server
- destroy cloud disk with confirmation
- copy URL, but opening the URL should show that the Workspace is stopped or unavailable until restored

### Server Destroyed, Disk Retained

The server has been deleted. The cloud disk remains.

Billing:

- server billing stopped
- storage billing active

Allowed actions:

- recreate server from the retained disk
- destroy cloud disk with confirmation

### Disk Destroyed

The cloud disk has been explicitly destroyed.

Billing:

- server billing stopped
- storage billing stopped

Allowed actions:

- no restore from this disk
- create a new Workspace if needed

## Workspace State Model

Workspace state is the standard status used by OPL Console to decide what the user sees, what actions are allowed, and what billing should happen.

User-facing states:

```text
Creating
Running
Stopping
Stopped
Restarting
Server destroyed, storage retained
Destroying storage
Destroyed
Failed
```

Internal states:

```text
draft
freezing_storage_balance
creating_server
creating_disk
deploying_docker
configuring_url
running
stopping_server
stopped_server_disk_retained
restarting_server
destroying_server
server_destroyed_disk_retained
destroying_disk
destroyed
failed
cleanup_required
```

The state model prevents invalid actions. For example, OPL Console must not allow storage destruction without confirmation, and it must not treat server destruction as storage destruction.

## Destructive Action Protection

Server stop requires confirmation.

Server destroy requires confirmation and must clearly say that the cloud disk remains.

Cloud disk destroy requires a stronger confirmation because it deletes Workspace data. The user must explicitly confirm that Workspace data will be lost.

Cloud disk destroy is the only action that stops storage billing.

## OPL Workspace Data

`one-person-lab-app` stores Workspace data on the mounted cloud disk.

The cloud disk is the primary persistence layer for:

- uploaded files
- project files
- generated outputs
- runtime artifacts
- local application state

A database may be added for metadata, session records, indexing, and audit references. The database is not the primary store for large Workspace files.

For v1, the default assumption is:

```text
one-person-lab-app Docker
-> mounted cloud disk at /workspace or /data
-> application data persists on the disk
```

## OPL Console Pages

V1 OPL Console should include:

- Workspace list
- Create Workspace
- Workspace detail
- Workspace URL and token controls
- Server controls
- Storage controls
- Billing and freeze details
- Audit receipts
- Account settings

The member experience is intentionally minimal:

```text
Open Workspace URL
-> enter OPL Workspace
```

Members do not see server controls, cloud resource configuration, billing, or audit pages.

## Non-Goals for V1

V1 does not include:

- member login for shared URLs
- per-member roles inside OPL Console
- arbitrary custom package marketplace
- Kubernetes as the default runtime
- multi-server Workspace clusters
- automatic cloud disk deletion when server is stopped or destroyed
- unpaid storage grace operation
- external payment provider settlement unless separately scoped

## V1 Product Acceptance Criteria

The product design is satisfied when OPL Console can support this flow:

```text
PI creates Workspace
-> selects Basic or Pro package
-> confirms billing and storage pre-freeze
-> receives a stable URL with token
-> shares URL with members
-> members enter OPL Workspace without login
-> PI can stop server while retaining storage
-> PI can restart from retained storage
-> PI can destroy server without destroying disk
-> PI can explicitly destroy disk with confirmation
-> OPL Console shows billing and audit receipts
```
