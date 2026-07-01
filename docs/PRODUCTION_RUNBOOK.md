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
OPL_WORKSPACE_DOMAIN
OPL_WORKSPACE_IMAGE
OPL_VPC_ID
OPL_SUBNET_ID
OPL_SECURITY_GROUP_ID
OPL_AVAILABILITY_ZONE
OPL_IMAGE_ID
OPL_SSH_KEY_ID
```

`OPL_WORKSPACE_IMAGE` must point to the Harbor production image, not a public development image.

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

## Launch Checklist

1. Confirm `GET /api/production/readiness` returns `ready: true`.
2. Confirm `GET /api/runtime/readiness` returns `ready: true`.
3. Create one Basic Workspace from OPL Console.
4. Verify Tencent creates exactly one CVM and one CBS disk for that Workspace.
5. Verify Ansible starts one `one-person-lab-app` container.
6. Verify Caddy serves `https://<workspace-slug>.<domain>/?token=<token>`.
7. Verify the Workspace disk is mounted to `/data`.
8. Stop the server and confirm CBS storage remains active.
9. Restart the server and confirm the Workspace URL/token still works.
10. Run one billing settlement and confirm OpenMeter receives usage events.

## Recovery Notes

- Server stop or destroy must never destroy the CBS disk.
- Disk destruction is a separate user-confirmed action.
- If CVM is lost but CBS remains, recreate server from the retained Workspace record and reattach storage.
- If OpenMeter rejects usage events, settlement fails so the operator can retry without silently splitting usage and billing records.
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
