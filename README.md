# OPL Cloud

OPL Cloud is the online hosted version of OPL.

This repository holds the v1 product design and a compact OPL Console implementation for the Workspace provisioning flow.

## Product Names

- `OPL Cloud`: the external product name.
- `OPL Console`: the management entry for opening workspaces, billing, access, and settings.
- `OPL Workspace`: the actual working environment delivered as a URL.

Do not use the old internal product name in product copy, UI, or design documents.

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
- bind-mounted Workspace disk path mapped to `/data`
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

## OPL WebUI Runtime Boundary

The `one-person-lab-app` Docker runtime should persist data under `/data`. The current templates set:

```text
ALLOW_REMOTE=true
DATA_DIR=/data
OPL_WEBUI_AUTH_MODE=none
HOME=/data
OPL_WORKSPACE_ROOT=/data/workspaces
CODEX_HOME=/data/codex
```

No-auth mode is acceptable only because OPL Cloud owns the Workspace URL token boundary. Do not expose the container directly without the OPL Workspace URL/token gateway or another trusted proxy boundary.

## Seven Phases

See [docs/IMPLEMENTATION_PLAN.md](./docs/IMPLEMENTATION_PLAN.md) for the current 7-phase plan:

1. Product and local runtime loop
2. Tencent IaC scaffold
3. Real Tencent CVM Workspace creation
4. Cloud lifecycle controls
5. PostgreSQL persistence
6. OpenMeter metering
7. Production hardening
