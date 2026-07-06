# OPL Cloud

OPL Cloud is the online hosted version of OPL.

This repository is the OPL Cloud implementation workspace for the OPL Console and OPL Workspace control-plane flow.

Implementation scope is fixed in [docs/project.md](./docs/project.md): [`one-person-lab`](https://github.com/gaofeng21cn/one-person-lab) provides the development framework concepts and lifecycle rules, [`one-person-lab-cloud`](https://github.com/gaofeng21cn/one-person-lab-cloud) provides the Cloud product definition, and this repository implements the OPL Console / OPL Workspace control-plane slice.

The implementation boundary map lives under [packages](./packages): `console`, `fabric`, `ledger`, and `contracts`.

## Product Names

- `OPL Cloud`: the external product name.
- `OPL Gateway`: the AI capability gateway, provider routing, and usage policy boundary owned by the Cloud product architecture.
- `OPL Console`: the management entry for opening workspaces, billing, access, and settings.
- `OPL Workspace`: the actual working environment delivered as a URL.
- `OPL Fabric`: the controlled compute and storage provisioning boundary.
- `OPL Ledger`: receipts, billing ledger, audit events, reconciliation, and verifier evidence.

Use only these OPL Cloud names in product copy, UI, and design documents.

## Confirmed Business Flow

```text
PI signs in to OPL Console
-> selects a compute package
-> OPL Cloud ensures the package ComputePool exists
-> opens one dedicated CVM ComputeAllocation for the PI
-> opens a StorageVolume
-> attaches the storage volume to the ComputeAllocation
-> OPL Cloud deploys/routes one-person-lab-app WebUI runtime onto that CVM
-> creates an OPL Workspace URL entry from the attachment
-> OPL Console shows the URL
-> PI copies and shares the URL
-> members open the URL and enter the OPL Workspace without login
```

## Core Resource Mapping

```text
1 ComputePool = package-level Tencent TKE node pool for one compute specification
1 ComputeAllocation = account-owned dedicated CVM node inside one ComputePool
1 StorageVolume = persistent account-owned storage
1 StorageAttachment = one storage volume mounted to one ComputeAllocation runtime
1 OPL Workspace = stable URL token entry backed by one StorageVolume and the current ComputeAllocation/StorageAttachment runtime pointer
```

One PI account can own multiple compute allocations, storage volumes, attachments, and Workspace URL entries.

Fabric-managed ComputePools are explicit placement pools, not user resource identities. TKE autoscaling and node-pool auto repair are disabled for these pools; every billable CVM must be created, owned, shown, and destroyed through a Console ComputeAllocation record.

## Critical Lifecycle Rule

Compute pools, compute allocations, and persistent storage lifecycles are separate first-class resources.

Lab Owner controls are: open compute allocation, open storage, attach storage, create Workspace URL, reset/delete URL, detach storage, destroy compute allocation, and destroy storage. The current commercial model does not expose "stop compute to save cost" as an owner action because a dedicated CVM remains billable until destroyed.

## Access Rule

Workspace URLs use:

```text
https://workspace.medopl.cn/w/<workspaceId>?token=<share-token>
```

The token is permanent until the owner deletes or resets it. Opening the URL does not require login.

## Default Packages

| Package | Compute | Persistent storage |
| --- | --- | --- |
| Basic Workspace | 2 CPU / 4GB | 10GB |
| Pro Workspace | 8 CPU / 16GB | 100GB |

GPU Workspaces are outside the current production package list. They require a verified GPU ComputePool before becoming a product package.

## Billing Rule

Billing is hourly. The user-facing price is Tencent Cloud resource cost plus a 20% platform markup.

Compute and storage must not operate unpaid. OPL Cloud freezes enough balance for 7 days of compute before opening a ComputeAllocation, and enough balance for 7 days of storage before opening storage. Ledger and usage records carry `computeAllocationId`, `computePoolId`, `storageVolumeId`, `storageAttachmentId`, and `workspaceId` where applicable. If available balance or holds are exhausted, OPL Console records account and runtime notifications; owner-facing controls remain resource based.

## Product Design

See [docs/product/console-workspace-v1.md](./docs/product/console-workspace-v1.md) for the current OPL Console commercial workspace product design.

For the current launch boundary, use [docs/status.md](./docs/status.md). The current status is controlled commercial pilot readiness for CPU Workspaces, not public GA.

## Current Implementation

The current app implements the commercial control-plane chain with the Tencent TKE runtime provider:

- OPL Console UI
- Basic and Pro CPU compute allocation creation
- persistent storage creation
- storage attachment to compute
- Workspace URL entry creation from an attachment
- permanent workspace URL token
- 7-day compute and storage prepaid holds
- real OPL Workspace app image default: `ghcr.io/gaofeng21cn/one-person-lab-app:latest`
- TKE/CVM runtime dispatch for one-person-lab-app
- Workspace storage mounted to `/data` and `/projects`
- Workspace URL route with token validation
- request usage billing endpoint
- billing ledger
- audit receipts

Production runtime readiness is exposed at:

```text
GET /api/runtime/readiness
```

The Console also shows readiness at the top of the page so real cloud creation is not attempted blindly.

Production launch readiness is exposed at:

```text
GET /api/production/readiness
```

It checks the Tencent TKE production runtime provider, TCR images, workspace domain, PostgreSQL, Tencent environment, and required host tools before launch.

## Run Locally Against Staging

```bash
npm install
npm test
npm run build
cp deploy/tke/opl-cloud-staging.local.env.example .env.staging.local
npm run staging:readiness
npm run staging:local
npm run staging:ui
```

`staging:local` loads the ignored `.env.staging.local`, uses `tencent-tke`, the shared staging `DATABASE_URL`, and the Go Tencent provisioner. It is the local operator Console for the same staging system used by cloud rollout.

For shared PostgreSQL control-plane persistence, set:

```bash
DATABASE_URL=postgres://opl:secret@127.0.0.1:5432/opl_cloud \
PORT=8787 npm start
```

When `DATABASE_URL` is set, OPL Console stores login users, account balances, Workspaces, billing ledger entries, audit events, and runtime operation scaffolding in PostgreSQL tables. `OPL_CONSOLE_USERS_JSON` is only the bootstrap seed for the first PI/admin login users; after those users are written to the control-plane store, account status, roles, ownership, balances, Workspaces, billing, and audit records persist with the database across Console rollouts.

TKE deployments must provide real PI/Admin users through secrets or an existing staging database. `OPL_CONSOLE_USERS_JSON` is a bootstrap seed, not a demo-account contract.

OPL Ledger is the v1 billing truth. External metering systems are not required for production billing.

```bash
OPL_BILLING_MARKUP=0.2 \
OPL_BASIC_COMPUTE_HOURLY_CNY=0.39 \
OPL_PRO_COMPUTE_HOURLY_CNY=3.09 \
OPL_STORAGE_GB_MONTH_CNY=0.36 \
PORT=8787 npm start
```

OPL Console remains the v1 billing ledger and user-facing balance source.

To reconcile OPL ledger debits against normalized Tencent Cloud bill totals from a deployed OPL Console:

```bash
npm run reconcile:tencent -- \
  --console-origin https://<console-domain> \
  --account <pi-account-id> \
  --tencent tencent-bills.json
```

The command reads:

- OPL `compute_debit` / `storage_debit` ledger entries from `GET /api/state?accountId=<pi-account-id>`
- Tencent bill rows from the provided local export file

For offline reconciliation against a saved OPL ledger export:

```bash
npm run reconcile:tencent -- --ledger ledger.json --tencent tencent-bills.json
```

Tencent bill rows should be normalized as:

```json
{ "workspaceId": "ws-alpha", "resourceType": "compute", "amount": 10, "currency": "CNY" }
```

For raw Tencent billing export rows, include the Workspace identity as a `workspace_id` tag and run:

```bash
npm run reconcile:tencent -- --ledger ledger.json --tencent tencent-export.json --tencent-format raw
```

It compares Tencent cost plus the configured 20% markup against OPL ledger debits and exits non-zero on mismatch. It writes JSON to stdout only and should not leave deployment or smoke artifacts in the repository.

For development UI without cloud mutation, run the Vite UI against an already running local-to-staging API:

```bash
npm run dev
```

Then open:

```text
http://127.0.0.1:5173
```

Real E2E is protected by an explicit paid-operation guard:

```bash
OPL_CONFIRM_REAL_CLOUD_E2E=1 npm run staging:e2e
```

See [DEV_GUIDE.md](./DEV_GUIDE.md) for the local-to-staging and cloud-staging gate sequence.

## Production Deployment Contract

Production deployment uses Tencent TKE only. Inject this repo's environment variables from your secret manager:

```bash
cp .env.example .env
```

Do not copy secret files from older projects into this repository. Use `.env.example` as the variable contract and provide real values through local shell env, CI secrets, or a deployment secret manager.

For deployment handoff, keep real secrets outside git and validate the secret-reference manifest:

```bash
npm run validate:production-manifest -- --manifest deploy/production-manifest.example.json
```

The manifest format requires sensitive values to use `secretRef`, not inline plaintext.

Run the Console with `OPL_RUNTIME_PROVIDER=tencent-tke`, TCR image refs, a kubeconfig reference, namespace, Ingress class, image pull Secret, Workspace storage class, and `OPL_TENCENT_PROVISIONER_BIN`. ComputePool maps to the package-level TKE node pool, ComputeAllocation maps to one account-owned CVM node in that pool, StorageVolume maps to PVC/CBS, StorageAttachment deploys one-person-lab-app onto the selected allocation and mounts storage, and Workspace maps to the URL token entry.

## Production Verification

After deploying OPL Console to Tencent TKE with PostgreSQL, TCR images, TLS, DNS, and HTTPS readiness configured, run the real chain verifier from an operator shell only after explicit approval:

```bash
OPL_CONSOLE_ORIGIN=https://<console-domain> npm run verify:production
```

The production verifier is fail-closed and requires public HTTPS Console and Workspace URLs. Local-to-staging uses `npm run staging:e2e` instead, which may talk to a local Console origin but still requires a public Workspace URL. Both verifiers first check:

- `GET /api/production/readiness`
- `GET /api/runtime/readiness`

Only when both are ready does it create real verification resources and exercise:

```text
credit account
ensure ComputePool
create dedicated CVM ComputeAllocation
create storage
attach storage to the allocation
create Workspace URL
verify TKE runtime status for CVM node/Deployment/PVC/Service/Ingress/Endpoints
open Workspace URL
record Gateway request usage
detach storage
destroy verification compute allocation
destroy verification storage
```

This command creates billable Tencent Cloud allocation and storage resources, then attempts to clean them up on both success and post-creation failure paths. By default, the Workspace name and verification ledger source events include a unique run id so repeated verification runs create fresh cloud resources and remain traceable in billing records. Use a dedicated verification account. Successful runs write structured JSON to stdout; failed runs write structured JSON to stderr, including `cleanupErrors` when cleanup does not fully complete. If the verifier reports cleanup errors, inspect OPL Console and Tencent Cloud and explicitly destroy any remaining verification resources. The command writes no smoke report or generated artifact into the repository.

Optional verifier controls:

```bash
OPL_VERIFY_ACCOUNT_ID=pi-production-verifier
OPL_VERIFY_RUN_ID=20260701-preprod-a
OPL_VERIFY_WORKSPACE_NAME="Production Verification Lab"
OPL_VERIFY_PACKAGE_ID=basic
OPL_VERIFY_CREDIT_AMOUNT=1000
OPL_VERIFY_URL_ATTEMPTS=12
OPL_VERIFY_RETRY_DELAY_MS=5000
```

## OPL WebUI Runtime Boundary

OPL Cloud references the One Person Lab repositories only as runtime contracts. `one-person-lab` owns the framework/CLI layer; `one-person-lab-app` owns the Docker/WebUI product entry that OPL Cloud deploys as one Workspace container.

The `one-person-lab-app` Docker/WebUI runtime exposes port `3000` and must persist two mounted paths:

- `/data`: WebUI internal state, configuration, sessions, maintenance logs, and caches.
- `/projects`: long-lived project files and Workspace deliverables.

The current production templates set:

```text
ALLOW_REMOTE=true
DATA_DIR=/data
AIONUI_DATA_DIR=/data
OPL_PROJECTS_DIR=/projects
WEBUI_AUTH=False
ENABLE_PERSISTENT_CONFIG=False
OPL_WEBUI_AUTH_MODE=none
HOME=/data
OPL_WORKSPACE_ROOT=/projects
CODEX_HOME=/data/codex
```

API keys and model credentials must not be injected through CLI arguments, environment variables, or Docker Compose. They are entered inside the WebUI after the Workspace URL is opened.

No-auth mode is acceptable only because OPL Cloud owns the Workspace URL token boundary. Do not expose the container directly without the OPL Workspace URL/token gateway or another trusted proxy boundary.

## Documentation And Development Lifecycle

See [docs/README.md](./docs/README.md) for the active documentation map. Work in this repository follows the One Person Lab development frame:

- goal
- attempt
- readiness
- receipt
- blocker
- next step
- human gate
- recovery
- evidence

Active docs describe current truth. Dated plans, design freezes, closeouts, run evidence, and completed implementation ledgers live in `docs/history/**`.

For the production runbook and recovery notes, see [docs/runtime/production-runbook.md](./docs/runtime/production-runbook.md).
