# OPL Cloud Production Runbook

This runbook defines the production launch and recovery checks for OPL Cloud.

## Required Runtime

Production uses:

- OPL Console API with `OPL_RUNTIME_PROVIDER=tencent-cvm`
- Tencent CVM for one Workspace server
- Tencent CBS for one retained Workspace disk
- Harbor-hosted `one-person-lab-app` image
- PostgreSQL control-plane persistence
- OpenMeter usage events
- Caddy Workspace HTTPS routing

## Required Secrets And Config

Inject values through a deployment secret manager or host environment. Do not commit real values.

```text
DATABASE_URL
OPENMETER_ENDPOINT
OPENMETER_API_KEY
TENCENTCLOUD_SECRET_ID
TENCENTCLOUD_SECRET_KEY
TENCENTCLOUD_REGION
OPL_RUNTIME_PROVIDER=tencent-cvm
OPL_HARBOR_REGISTRY
OPL_WORKSPACE_DOMAIN
OPL_WORKSPACE_IMAGE
OPL_VPC_ID
OPL_SUBNET_ID
OPL_SECURITY_GROUP_ID
OPL_AVAILABILITY_ZONE
OPL_IMAGE_ID
OPL_SSH_KEY_ID
```

`OPL_WORKSPACE_IMAGE` must start with `OPL_HARBOR_REGISTRY/` and point to the Harbor production image, not a public development image.

## Required Host Tools

```text
tofu
ansible-playbook
tccli
caddy
```

The API exposes:

```text
GET /api/runtime/readiness
GET /api/production/readiness
```

Both must be reviewed before creating production Workspaces.

Before deploying, validate the secret-reference manifest:

```bash
npm run validate:production-manifest -- --manifest deploy/production-manifest.example.json
```

Use the example as a contract. Real secret values belong in the deployment secret manager, not in git.

## Automated Chain Verification

After readiness is green, run:

```bash
OPL_CONSOLE_ORIGIN=https://<console-domain> npm run verify:production
```

This command creates a real verification Workspace, opens its URL, stops/restarts/destroys/recreates server compute while retaining CBS storage, reopens the same URL after recreation, runs one billing settlement, then destroys the verification server and disk. It writes results to stdout only and must not leave smoke outputs in the repository.

Use a dedicated verification account. If the verifier fails before its cleanup checks, inspect OPL Console and Tencent Cloud, then explicitly destroy the verification server and disk to stop billing.

## Launch Checklist

1. Confirm `GET /api/production/readiness` returns `ready: true`.
2. Confirm `GET /api/runtime/readiness` returns `ready: true`.
3. Confirm `npm run validate:production-manifest -- --manifest <manifest.json>` passes for the deployment manifest.
4. Create one Basic Workspace from OPL Console.
5. Verify Tencent creates exactly one CVM and one CBS disk for that Workspace.
6. Verify Ansible starts one `one-person-lab-app` container.
7. Verify Caddy serves `https://<workspace-slug>.<domain>/?token=<token>`.
8. Verify `/etc/caddy/Caddyfile` imports `/etc/caddy/conf.d/*.caddy` and `caddy reload --config /etc/caddy/Caddyfile` succeeds.
9. Verify the CBS data disk is mounted at `/data/opl` before Docker starts, and that the container maps `/data/opl` to `/data`.
10. Stop the server and confirm CBS storage remains active.
11. Restart the server and confirm the Workspace URL/token still works.
12. Destroy the server and confirm CBS storage is detached, retained, and still billable.
13. Restart the server-destroyed Workspace and confirm a new CVM is created, the retained CBS disk is attached, Ansible restores the Docker runtime, and the same Workspace URL/token works.
14. Run one billing settlement and confirm OpenMeter receives usage events.
15. Run `npm run verify:production` against the deployed OPL Console and keep the stdout result in the deployment record, not in git.
16. Run `npm run reconcile:tencent -- --console-origin https://<console-domain> --account <pi-account-id> --tencent <tencent-bills.json>` so the OPL ledger is read from the deployed Console. Add `--tencent-format raw` for exported Tencent rows carrying a `workspace_id` tag. Use `--ledger <ledger.json>` only for an offline saved OPL ledger export. Keep the stdout result in the deployment record, not in git.

## Recovery Notes

- Server stop or destroy must never destroy the CBS disk.
- Disk destruction is a separate user-confirmed action. When invoked while server compute still exists, OPL Console must destroy the server first, mark the disk detached and retained, and then destroy the disk so compute and storage billing both stop.
- Check `runtime_operations` first when a Workspace action fails. It records operation type, status, attempt count, timestamps, and error message.
- If CVM is lost but CBS remains, restart the server-destroyed Workspace from OPL Console. The API should record `recreate_server`, call `RunInstances`, attach the retained CBS disk, rerun Ansible, and keep the existing Workspace URL/token.
- If OpenMeter rejects usage events, settlement fails so the operator can retry without silently splitting usage and billing records.
- If Tencent bill reconciliation fails, inspect the mismatch before issuing invoices or increasing account balances.
- If PostgreSQL is unavailable, stop provisioning new Workspaces until control-plane persistence is restored.

## Artifact Hygiene

Do not commit:

```text
.env
.runtime/
dist/
infra/tencent-cvm/.terraform/
terraform.tfstate*
*.tfplan
cloud credentials
smoke outputs
```
