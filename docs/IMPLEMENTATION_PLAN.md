# OPL Cloud Seven-Phase Implementation Plan

This plan tracks the v1 business chain:

```text
PI opens OPL Console
-> creates an OPL Workspace
-> OPL Cloud creates one server, one cloud disk, one one-person-lab-app Docker runtime, and one URL
-> PI shares the URL
-> members enter the OPL Workspace without login
-> OPL Console manages lifecycle, billing, and audit
```

Development discipline follows the One Person Lab style: small durable contracts, focused diffs, no phase-only smoke artifacts in git, and machine-readable behavior tests instead of prose assertions.

## Phase 1: Product and Local Runtime Loop

Status: implemented.

Delivered:

- OPL Cloud, OPL Console, and OPL Workspace naming.
- Product design freeze in `PRODUCT_DESIGN.md`.
- Console UI for Workspace creation, URL access, server/disk lifecycle, billing, and audit.
- Local Docker provider that writes real Docker Compose runtime artifacts under ignored `.runtime/workspaces`.
- Optional local execution with `OPL_LOCAL_DOCKER_EXECUTE=1`.
- Permanent URL token and no-login Workspace access rule.
- Server and disk lifecycle separation.
- 7-day storage pre-freeze.

Durable checks:

- `tests/workspace-lifecycle.test.js`
- `tests/local-docker-provider.test.js`
- `tests/workspace-url-route.test.js`

## Phase 2: Tencent IaC Scaffold

Status: implemented.

Delivered:

- OpenTofu scaffold in `infra/tencent-cvm`.
- Tencent CVM server resource.
- Tencent CBS cloud disk resource.
- Server and disk attachment.
- Cloud-init bootstrapping.
- Ansible deployment for Docker Compose, Caddy route, and `one-person-lab-app`.
- Sensitive Workspace URL output.

Durable checks:

- `tofu init -backend=false -input=false`
- `tofu validate`

Cleanup rule:

- Remove `infra/tencent-cvm/.terraform/` after validation.
- Do not commit `.terraform/`, tfstate, plan files, or cloud credentials.

## Phase 3: Real Tencent CVM Workspace Creation

Status: active.

Delivered:

- `tencent-cvm` runtime provider selectable with `OPL_RUNTIME_PROVIDER=tencent-cvm`.
- Provider runs `tofu init`, `tofu apply`, `tofu output -json`, and `ansible-playbook`.
- Provider maps server ID, disk ID, public IP, Docker image, URL, and disk mount back into the OPL Workspace record.
- Provider fails closed until required Tencent environment and tools are present.
- Provider exposes readiness data through `GET /api/runtime/readiness`.
- OpenTofu state is isolated per Workspace under ignored `.runtime/tencent-cvm/<workspaceId>/`.

Required environment:

```text
TENCENTCLOUD_SECRET_ID
TENCENTCLOUD_SECRET_KEY
TENCENTCLOUD_REGION
OPL_WORKSPACE_DOMAIN
OPL_VPC_ID
OPL_SUBNET_ID
OPL_SECURITY_GROUP_ID
OPL_AVAILABILITY_ZONE
OPL_IMAGE_ID
OPL_SSH_KEY_ID
OPL_WORKSPACE_IMAGE
```

Required tools on the API host:

```text
tofu
ansible-playbook
```

Next real-cloud verification:

1. Inject the environment through shell env, CI secrets, or a deployment secret manager.
2. Start API with `OPL_RUNTIME_PROVIDER=tencent-cvm`.
3. Open OPL Console and confirm runtime readiness is ready.
4. Credit the PI account.
5. Create one Basic Workspace.
6. Verify Tencent creates one CVM, one CBS disk, one Docker runtime, and one URL.
7. Verify the URL reaches the OPL WebUI and the disk is mounted to `/data`.

Do not copy secret files from older projects into this repo.

## Phase 4: Cloud Lifecycle Controls

Status: not implemented.

Goal:

- Stop server billing without destroying CBS storage.
- Restart or recreate the server while preserving the disk, token, and URL.
- Destroy the server while retaining disk.
- Destroy the disk only after explicit data-loss confirmation.

Implementation direction:

- Prefer Tencent Cloud APIs or OpenTofu state-aware operations that preserve the separate server/disk lifecycle.
- Keep per-Workspace state isolated under `.runtime/tencent-cvm/<workspaceId>/`.
- Never implement destructive fallbacks that can delete storage when only server lifecycle was requested.

## Phase 5: PostgreSQL Persistence

Status: implemented and verified against a temporary real PostgreSQL container.

Goal:

- Replace JSON file state with PostgreSQL as the durable control-plane database.
- Keep JSON store only for local development if useful.

Tables to introduce:

- `accounts`
- `workspaces`
- `billing_ledger`
- `audit_events`
- `runtime_operations`

Delivered:

- `PostgresStore` uses the same store interface as local JSON storage.
- API selects PostgreSQL automatically when `DATABASE_URL` is configured.
- Schema creation is owned by the API startup path.
- Accounts, Workspaces, billing ledger entries, and audit events persist to dedicated PostgreSQL tables with JSONB state payloads.
- `runtime_operations` table exists for retry-aware cloud operations.

Requirements:

- Workspace state must survive API restart.
- Workspace URL/token state must remain stable.
- Runtime operations should be auditable and retry-aware.

Verification:

1. `tests/postgres-store.test.js` verifies the table contract and transaction path.
2. A temporary `postgres:16-alpine` container verified real `PostgresStore` read/write behavior.
3. The temporary container and files are removed after verification.

Next production step:

- Provide a managed production `DATABASE_URL` and run the same create/restart/readback workflow against the deployed API.

## Phase 6: OpenMeter Metering

Status: implemented at the event sink boundary.

Goal:

- Send server runtime and storage usage events to OpenMeter.
- Keep OPL Console ledger as the product billing truth for v1.
- Keep Lago for later invoice or subscription workflows.

Event families:

- `workspace.server.running_hours`
- `workspace.storage.gb_hours`
- `workspace.storage.hold`
- `workspace.lifecycle.action`

Delivered:

- `OpenMeterClient` posts CloudEvents-style JSON usage events to `OPENMETER_ENDPOINT`.
- API enables OpenMeter only when both `OPENMETER_ENDPOINT` and `OPENMETER_API_KEY` are configured.
- Billing settlement emits:
  - `workspace.server.running_hours`
  - `workspace.storage.gb_hours`
- OpenMeter rejection fails the settlement request instead of silently splitting ledger and usage events.

Not yet delivered:

- OpenMeter meter definitions and dashboards.
- Lago invoice/subscription integration.
- Tencent bill reconciliation.

Billing rules remain:

- Hourly server billing.
- Storage billing until disk destruction.
- Tencent cost plus 10% platform markup.
- 7-day storage pre-freeze before opening or resuming.

## Phase 7: Production Hardening

Status: not implemented.

Goal:

- Use Harbor as the production image registry.
- Use Caddy for HTTPS Workspace URL routing.
- Move secrets into a deployment secret manager.
- Add audit/recovery runbooks.
- Add cloud cost reconciliation against Tencent billing.

Hardening requirements:

- No committed secrets.
- No public no-auth container without token gateway or trusted proxy boundary.
- No untracked runtime artifacts left in the repo after verification.
- Workspace URL contains a permanent owner-controlled token until reset or deletion.

## Current Blocker For Real Phase 3

The repository is ready to attempt real Tencent creation once the required Tencent environment variables are injected. Without those values, `tencent-cvm` intentionally reports readiness gaps and refuses to create cloud resources.
