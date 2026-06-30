# OPL Cloud Implementation Plan

This repo implements OPL Cloud in four compact phases. OPL Console owns product workflow, billing, token access, and audit. Runtime providers execute infrastructure actions.

## Phase 1: OPL Console MVP

Status: implemented with the Local Docker provider as the default runtime path.

Scope:

- Workspace list
- Create Basic or Pro Workspace
- Permanent URL token
- Server stop/restart/destroy
- Disk destroy with explicit data-loss confirmation
- Billing ledger
- Audit receipts
- 7-day storage pre-freeze
- Workspace URL token validation route
- Hourly billing settlement endpoint

Reference borrowed from `projects/platform-v22`:

- left navigation + top status shell
- resource control page shape
- billing/audit ledger idea
- fail-closed resource lifecycle contracts

Not borrowed:

- old product naming
- old product semantics
- old source code
- K8s/TKE default route

## Phase 2: Local Docker Provider

Goal: prove a real `one-person-lab-app` Docker runtime can be created and controlled by OPL Console on one test machine.

Status: implemented for artifact generation by default and real Docker execution when `OPL_LOCAL_DOCKER_EXECUTE=1`.

Expected tools:

- Docker Compose
- local bind mount or Docker volume as disk substitute
- OPL Cloud API route for local token validation
- Caddy for production workspace URL routing

Provider actions:

```text
createWorkspaceRuntime()
  create workspace directory
  write compose file
  create disk directory
  optionally run one-person-lab-app container with Docker Compose
  discover local container URL when execution is enabled
  return URL

stopServer()
  docker compose stop
  keep volume

restartServer()
  docker compose up -d
  reuse volume and URL

destroyServer()
  docker compose down
  keep volume

destroyDisk()
  remove volume only after explicit confirmation
```

## Phase 3: Tencent CVM Provider

Goal: implement route A with real Tencent Cloud resources.

Status: production handoff scaffold exists in `infra/tencent-cvm`. The API provider currently fails closed unless a real cloud execution runner is configured and writes OpenTofu/Ansible outputs back to the Workspace record.

Expected tools:

- OpenTofu or Terraform with TencentCloud provider
- cloud-init for first boot
- Ansible for Docker/Compose deployment
- Harbor for image distribution
- Caddy or Traefik for HTTPS reverse proxy

Provider actions:

```text
createWorkspaceRuntime()
  create CVM server
  create CBS cloud disk
  attach and mount disk
  install Docker
  pull one-person-lab-app image
  start Docker Compose
  configure workspace subdomain
  return stable URL

stopServer()
  stop CVM billing when possible
  keep CBS disk

restartServer()
  start or recreate CVM
  reattach CBS disk
  restart Docker
  preserve URL and token

destroyServer()
  destroy CVM
  keep CBS disk

destroyDisk()
  destroy CBS disk
  stop storage billing
```

Handoff files:

- `infra/tencent-cvm/main.tf`
- `infra/tencent-cvm/variables.tf`
- `infra/tencent-cvm/cloud-init.yml`
- `infra/tencent-cvm/ansible/workspace.yml`
- `infra/tencent-cvm/ansible/Caddyfile.j2`

## Phase 4: Billing and Audit Closure

Goal: make billing an auditable OPL Console truth.

Status: minimum ledger closure implemented. `settleBilling()` records hourly server and storage debits with Tencent-cost-plus-10% pricing. OpenMeter should consume the same event types next; Lago remains later for invoices/subscriptions.

Ledger events:

- `credit`
- `storage_hold`
- `server_debit`
- `storage_debit`
- `server_billing_stopped`
- `server_destroyed`
- `storage_destroyed`
- `token_reset`
- `token_deleted`

Rules:

- Billing is hourly.
- User-facing price is Tencent Cloud resource cost plus 10%.
- Storage cannot run unpaid.
- Opening or resuming requires enough balance for a 7-day storage freeze.
- Stopping or destroying the server stops server billing only.
- Storage billing stops only after disk destruction completes.

Future integrations:

- Tencent Cloud pricing and bill APIs for cost reconciliation
- OpenMeter for usage event metering
- Lago only if external invoicing or subscriptions become required
