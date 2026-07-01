# OPL Cloud Production Runbook

This runbook defines the production launch and recovery checks for OPL Cloud.

## Required Runtime

Production uses:

- OPL Console API with `OPL_RUNTIME_PROVIDER=tencent-tke`
- Tencent TKE for OPL Cloud control-plane deployment
- one-person-lab-app as the per-Workspace runtime image
- Persistent workspace storage through the TKE storage class
- TCR-hosted OPL Cloud and one-person-lab-app images
- PostgreSQL control-plane persistence
- OpenMeter usage events
- Kubernetes Ingress for `cloud.medopl.cn` and `workspace.medopl.cn`

## Required Secrets And Config

Inject values through a deployment secret manager or host environment. Do not commit real values.

```text
DATABASE_URL
OPENMETER_ENDPOINT
OPENMETER_API_KEY
OPL_RUNTIME_PROVIDER=tencent-tke
OPL_PUBLIC_URL=https://cloud.medopl.cn
OPL_CONSOLE_DOMAIN=cloud.medopl.cn
OPL_WORKSPACE_DOMAIN=workspace.medopl.cn
OPL_CLOUD_IMAGE
OPL_WORKSPACE_IMAGE
OPL_K8S_NAMESPACE
OPL_INGRESS_CLASS
OPL_IMAGE_PULL_SECRET_NAME
OPL_WORKSPACE_STORAGE_CLASS
TENCENT_DEPLOY_KUBECONFIG_REF
TENCENT_DEPLOY_CLUSTER_ID
TENCENT_TCR_REGISTRY
TENCENT_TCR_NAMESPACE
TENCENT_TCR_REGION
```

`OPL_CLOUD_IMAGE` and `OPL_WORKSPACE_IMAGE` must start with `TENCENT_TCR_REGISTRY/` and point to TCR images, not public development images.

`one-person-lab-app` WebUI contract is fixed at port `3000` with persistent `/data` and `/projects`. Keep API/model keys out of CLI arguments, environment variables, and Docker Compose; they are entered inside WebUI.

## Required Host Tools

```text
kubectl
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

The TKE control-plane manifest is:

```text
deploy/tke/opl-cloud.k8s.json
```

The operator input file to fill locally is:

```text
.env.production.local
```

## Automated Chain Verification

After readiness is green, run:

```bash
OPL_CONSOLE_ORIGIN=https://<console-domain> npm run verify:production
```

This command creates a real verification Workspace, opens its URL, stops/restarts/destroys/recreates runtime compute while retaining workspace storage, reopens the same URL after recreation, runs one billing settlement, then destroys the verification compute and storage. If a check fails after Workspace creation, the verifier still attempts cleanup before returning the original failure. Default Workspace names and verification ledger source events include a unique run id so repeated verifier runs create fresh cloud resources and remain traceable in billing records. Successful runs write structured JSON to stdout; failed runs write structured JSON to stderr, including `cleanupErrors` when cleanup does not fully complete. It must not leave smoke outputs in the repository.

Use a dedicated verification account. If the verifier reports `cleanupErrors`, inspect OPL Console and Tencent Cloud, then explicitly destroy any remaining verification server or disk to stop billing.

## Launch Checklist

1. Confirm `GET /api/production/readiness` returns `ready: true`.
2. Confirm `GET /api/runtime/readiness` returns `ready: true`.
3. Confirm `npm run validate:production-manifest -- --manifest <manifest.json>` passes for the deployment manifest.
4. Create one Basic Workspace from OPL Console.
5. Verify TKE creates exactly one runtime compute unit and one persistent storage binding for that Workspace.
6. Verify the runtime starts one `one-person-lab-app` container.
7. Verify Ingress serves `https://workspace.medopl.cn/w/<workspaceId>?token=<token>`.
8. Verify the runtime maps persistent storage to `/data` and `/projects`.
9. Stop runtime compute and confirm workspace storage remains active.
10. Restart runtime compute and confirm the Workspace URL/token still works.
11. Destroy runtime compute and confirm storage is retained and still billable.
12. Recreate the runtime from retained storage and confirm the same Workspace URL/token works.
14. Run one billing settlement and confirm OpenMeter receives usage events.
15. Run `npm run verify:production` against the deployed OPL Console and keep the stdout or stderr JSON result in the deployment record, not in git.
16. Run `npm run reconcile:tencent -- --console-origin https://<console-domain> --account <pi-account-id> --tencent <tencent-bills.json>` so the OPL ledger is read from the deployed Console. Add `--tencent-format raw` for exported Tencent rows carrying a `workspace_id` tag. Use `--ledger <ledger.json>` only for an offline saved OPL ledger export. Keep the stdout result in the deployment record, not in git.

## Recovery Notes

- Compute stop or destroy must never destroy workspace storage.
- Storage destruction is a separate user-confirmed action. When invoked while runtime compute still exists, OPL Console must destroy compute first, mark storage retained, and then destroy storage so compute and storage billing both stop.
- Check `runtime_operations` first when a Workspace action fails. It records operation type, status, attempt count, timestamps, and error message.
- If runtime compute is lost but storage remains, restart the compute-destroyed Workspace from OPL Console. The API should record `recreate_server`, recreate the runtime, reattach retained storage, and keep the existing Workspace URL/token.
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
