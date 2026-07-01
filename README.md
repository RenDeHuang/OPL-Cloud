# OPL Cloud

OPL Cloud is the online hosted version of OPL.

This repository is the OPL Cloud implementation workspace for the OPL Console and OPL Workspace control-plane flow.

Implementation scope is fixed in [docs/IMPLEMENTATION_SCOPE.md](./docs/IMPLEMENTATION_SCOPE.md): [`one-person-lab`](https://github.com/gaofeng21cn/one-person-lab) provides the development framework concepts, [`one-person-lab-cloud`](https://github.com/gaofeng21cn/one-person-lab-cloud) provides the Cloud product definition, and this repository implements the OPL Console / OPL Workspace control-plane slice.

## Product Names

- `OPL Cloud`: the external product name.
- `OPL Gateway`: the AI capability gateway and usage-metering boundary owned by the Cloud product architecture.
- `OPL Console`: the management entry for opening workspaces, billing, access, and settings.
- `OPL Workspace`: the actual working environment delivered as a URL.
- `OPL Fabric`: the controlled compute and storage provisioning boundary.
- `OPL Ledger`: receipts, billing ledger, audit events, metering, reconciliation, and verifier evidence.

Use only these OPL Cloud names in product copy, UI, and design documents.

## Confirmed Business Flow

```text
PI signs in to OPL Console
-> creates an OPL Workspace
-> chooses one of the default server/disk packages
-> confirms hourly billing and 7-day storage pre-freeze
-> OPL Cloud creates one server
-> OPL Cloud creates one cloud disk
-> OPL Cloud deploys one one-person-lab-app Docker container
-> OPL Cloud mounts the cloud disk into the Docker runtime
-> OPL Cloud creates a stable workspace subdomain URL with a permanent token
-> OPL Console shows the URL
-> PI copies and shares the URL
-> members open the URL and enter the OPL Workspace without login
```

## Core Resource Mapping

```text
1 OPL Workspace
= 1 server
= 1 one-person-lab-app Docker container
= 1 cloud disk
= 1 URL
```

One PI account can own multiple OPL Workspaces.

## Critical Lifecycle Rule

Server and cloud disk lifecycles are separate.

Stopping or destroying a server must not destroy the cloud disk. The cloud disk is destroyed only after an explicit user confirmation. Storage billing continues until cloud disk destruction completes.

## Access Rule

Workspace URLs use:

```text
https://<workspace-slug>.oplcloud.cn/?token=<share-token>
```

The token is permanent until the owner deletes or resets it. Opening the URL does not require login.

## Default Packages

| Package | Server | Cloud disk |
| --- | --- | --- |
| Basic Workspace | 2c / 4GB | 10GB |
| Pro Workspace | 8c / 16GB | 100GB |

## Billing Rule

Billing is hourly. The user-facing price is Tencent Cloud resource cost plus a 10% platform markup.

Storage must not operate unpaid. OPL Cloud freezes enough balance for 7 days of cloud disk storage before opening or resuming a Workspace.

## Product Design

See [PRODUCT_DESIGN.md](./PRODUCT_DESIGN.md) for the frozen v1 product design.

## Current Implementation

The current app implements the local business-chain loop with the Local Docker provider:

- OPL Console UI
- Basic and Pro Workspace creation
- permanent workspace URL token
- server stop/restart/destroy controls
- disk destroy with explicit confirmation
- 7-day storage pre-freeze
- Local Docker Compose workspace artifacts under `.runtime/workspaces`
- real OPL WebUI image default: `ghcr.io/gaofeng21cn/one-person-lab-webui:latest`
- bind-mounted Workspace disk paths mapped to `/data` and `/projects`
- Workspace URL route with token validation
- optional real local Docker execution with `OPL_LOCAL_DOCKER_EXECUTE=1`
- hourly billing settlement endpoint
- billing ledger
- audit receipts

Production Tencent CVM handoff files are in [infra/tencent-cvm](./infra/tencent-cvm). They define the OpenTofu, Ansible, and Caddy shape for route A.

The Tencent CVM provider now has the runner boundary wired into the API. It remains fail-closed unless the required environment variables and tools are present. Runtime readiness is exposed at:

```text
GET /api/runtime/readiness
```

The Console also shows readiness at the top of the page so real cloud creation is not attempted blindly.

Production launch readiness is exposed at:

```text
GET /api/production/readiness
```

It checks the production runtime provider, Harbor image, workspace domain, PostgreSQL, OpenMeter, Tencent environment, and required host tools before launch.

## Run Locally

```bash
npm install
npm test
npm run build
PORT=8787 npm start
```

By default, local development uses an ignored JSON state file under `.runtime/`.
For PostgreSQL control-plane persistence, set:

```bash
DATABASE_URL=postgres://opl:secret@127.0.0.1:5432/opl_cloud \
PORT=8787 npm start
```

When `DATABASE_URL` is set, OPL Console stores accounts, Workspaces, billing ledger entries, audit events, and runtime operation scaffolding in PostgreSQL tables.

For OpenMeter usage events, set:

```bash
OPENMETER_ENDPOINT=https://openmeter.example.com \
OPENMETER_API_KEY=om_... \
PORT=8787 npm start
```

When configured, each billing settlement emits:

- `workspace.server.running_hours`
- `workspace.storage.gb_hours`

`openMeterDefinitions()` in `services/api/src/openmeter.js` declares the matching production meter definitions:

- `opl_workspace_server_running_hours`
- `opl_workspace_storage_gb_hours`

OpenMeter is a usage meter. OPL Console remains the v1 billing ledger and user-facing balance source.

To reconcile OPL ledger debits against normalized Tencent Cloud bill totals from a deployed OPL Console:

```bash
npm run reconcile:tencent -- \
  --console-origin https://<console-domain> \
  --account <pi-account-id> \
  --tencent tencent-bills.json
```

The command reads:

- OPL `server_debit` / `storage_debit` ledger entries from `GET /api/state?accountId=<pi-account-id>`
- Tencent bill rows from the provided local export file

For offline reconciliation against a saved OPL ledger export:

```bash
npm run reconcile:tencent -- --ledger ledger.json --tencent tencent-bills.json
```

Tencent bill rows should be normalized as:

```json
{ "workspaceId": "ws-alpha", "resourceType": "server", "amount": 10, "currency": "CNY" }
```

For raw Tencent billing export rows, include the Workspace identity as a `workspace_id` tag and run:

```bash
npm run reconcile:tencent -- --ledger ledger.json --tencent tencent-export.json --tencent-format raw
```

It compares Tencent cost plus the configured 10% markup against OPL ledger debits and exits non-zero on mismatch. It writes JSON to stdout only and should not leave deployment or smoke artifacts in the repository.

To also start the local OPL Docker container when a Workspace is created:

```bash
OPL_LOCAL_DOCKER_EXECUTE=1 \
OPL_WORKSPACE_IMAGE=ghcr.io/gaofeng21cn/one-person-lab-webui:latest \
PORT=8787 npm start
```

For development UI:

```bash
npm start
npm run dev
```

Then open:

```text
http://127.0.0.1:5173
```

## Tencent CVM Provider

Install OpenTofu and Ansible on the API host, then inject this repo's environment variables from your secret manager:

```bash
cp .env.example .env
```

Do not copy secret files from older projects into this repository. Use `.env.example` as the variable contract and provide real values through local shell env, CI secrets, or a deployment secret manager.

For deployment handoff, keep real secrets outside git and validate the secret-reference manifest:

```bash
npm run validate:production-manifest -- --manifest deploy/production-manifest.example.json
```

The manifest format requires sensitive values to use `secretRef`, not inline plaintext.

Run with Tencent CVM provisioning enabled:

```bash
OPL_RUNTIME_PROVIDER=tencent-cvm \
OPL_WORKSPACE_IMAGE=<harbor-image> \
PORT=8787 npm start
```

On Workspace creation, the provider runs:

```text
tofu init
tofu apply
tofu output -json
ansible-playbook ansible/workspace.yml
```

Then it maps OpenTofu outputs back into the OPL Workspace record: server ID, disk ID, public IP, Docker image, stable URL, and token access state.

Each Workspace gets isolated OpenTofu state under ignored `.runtime/tencent-cvm/<workspaceId>/`. Do not commit tfstate, `.terraform/`, `.runtime/`, plans, or credentials.

Cloud lifecycle controls use Tencent Cloud CLI on the API host:

```text
tccli cvm StopInstances --StoppedMode STOP_CHARGING
tccli cvm StartInstances
tccli cvm RunInstances
tccli cbs AttachDisks
tccli cvm DescribeInstances
tccli cbs DetachDisks
tccli cvm TerminateInstances
tccli cbs TerminateDisks
```

Server actions intentionally retain CBS storage. `TerminateDisks` is used only by the explicit disk destruction action.

## Production Verification

After deploying OPL Console to Tencent TKE with PostgreSQL, OpenMeter, TCR images, TLS, DNS, and HTTPS readiness configured, run the real chain verifier from an operator shell only after explicit approval:

```bash
OPL_CONSOLE_ORIGIN=https://<console-domain> npm run verify:production
```

The verifier is fail-closed. It first checks:

- `GET /api/production/readiness`
- `GET /api/runtime/readiness`

Only when both are ready does it create a real verification Workspace and exercise:

```text
credit account
create Workspace
verify TKE runtime status for Deployment/PVC/Service/Ingress/Endpoints
open Workspace URL
stop runtime compute while retaining workspace storage
restart runtime compute
destroy runtime compute while retaining workspace storage
recreate runtime compute from retained workspace storage
open the same Workspace URL again after recreation
settle billing and emit OpenMeter usage
destroy verification runtime compute
destroy verification workspace storage
```

This command creates billable Tencent Cloud runtime resources and lifecycle events, then attempts to clean up the verification compute and storage on both success and post-creation failure paths. By default, the Workspace name and verification ledger source events include a unique run id so repeated verification runs create fresh cloud resources and remain traceable in billing records. Use a dedicated verification account. Successful runs write structured JSON to stdout; failed runs write structured JSON to stderr, including `cleanupErrors` when cleanup does not fully complete. If the verifier reports cleanup errors, inspect OPL Console and Tencent Cloud and explicitly destroy any remaining verification resources. The command writes no smoke report or generated artifact into the repository.

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
OPL_WEBUI_AUTH_MODE=none
HOME=/data
OPL_WORKSPACE_ROOT=/projects
CODEX_HOME=/data/codex
```

API keys and model credentials must not be injected through CLI arguments, environment variables, or Docker Compose. They are entered inside the WebUI after the Workspace URL is opened.

No-auth mode is acceptable only because OPL Cloud owns the Workspace URL token boundary. Do not expose the container directly without the OPL Workspace URL/token gateway or another trusted proxy boundary.

## Implementation Goal Ledger

See [docs/IMPLEMENTATION_PLAN.md](./docs/IMPLEMENTATION_PLAN.md) for the current goal ledger. Work in this repository follows the One Person Lab development frame:

- goal
- attempt
- readiness
- receipt
- blocker
- next step
- human gate
- recovery
- evidence

The goal ledger preserves the delivered Console, Workspace, Fabric, and Ledger capabilities while keeping real cloud verification behind an explicit human gate.

For the production launch checklist and recovery notes, see [docs/PRODUCTION_RUNBOOK.md](./docs/PRODUCTION_RUNBOOK.md).
