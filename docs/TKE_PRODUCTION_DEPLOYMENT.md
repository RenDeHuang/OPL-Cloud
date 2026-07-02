# OPL Cloud TKE Production Deployment

## Decision

OPL Cloud production uses Tencent TKE as the runtime.

The former v22 MedOPL TKE cluster, TCR namespace, kubeconfig, and PostgreSQL are now treated as the OPL Cloud production resource pool. Existing external resource names may still contain `medopl` until they are deliberately renamed, but repository language and new deployment assets should use OPL Cloud.

The CVM route is legacy fallback/debug only. `OPL_IMAGE_ID` and `OPL_SSH_KEY_ID` are not required for `OPL_RUNTIME_PROVIDER=tencent-tke`.

## Domains

Use two public domains:

| Domain | Purpose | Default URL shape |
| --- | --- | --- |
| `cloud.medopl.cn` | OPL Console and OPL Cloud control plane | `https://cloud.medopl.cn` |
| `workspace.medopl.cn` | OPL Workspace runtime gateway | `https://workspace.medopl.cn/w/<workspaceId>` |

The default workspace URL mode is path-based so DNS only needs the two fixed records above. If per-workspace subdomains are needed later, add `*.workspace.medopl.cn` and a wildcard/SAN certificate as a separate change.

## DNS Records

Create DNS only after the TKE Ingress has a Tencent Cloud CLB address.

Find the target after deploy:

```bash
kubectl --kubeconfig "$TENCENT_DEPLOY_KUBECONFIG_REF" \
  --namespace opl-cloud \
  get ingress
```

Use one of these DNS shapes:

| Host record | Record type | Record value |
| --- | --- | --- |
| `cloud` | `CNAME` preferred when Tencent exposes a CLB hostname | The CLB hostname shown by the Ingress, for example `<clb-name>.<region>.clb.myqcloud.com` |
| `workspace` | `CNAME` preferred when Tencent exposes a CLB hostname | The same CLB hostname as `cloud` |
| `cloud` | `A` when Ingress exposes only a public IP | The CLB public IP shown by the Ingress |
| `workspace` | `A` when Ingress exposes only a public IP | The same CLB public IP as `cloud` |

Do not point DNS at a TKE node IP. Point both records at the Ingress/CLB.

Recommended TTL: `600`.

## TLS

Use Tencent qcloud certificate-id Secrets for the Ingress TLS hosts. The default production shape uses one Secret per host:

```text
opl-cloud-console-medopl-cn-tls
opl-cloud-workspace-medopl-cn-tls
```

For TKE qcloud Ingress, each Secret must be in the `opl-cloud` namespace, must be type `Opaque`, and must contain this key:

```text
qcloud_cert_id
```

The referenced Tencent Cloud SSL certificates must cover these domains:

```text
cloud.medopl.cn -> OPL_CONSOLE_TLS_CERT_ID
workspace.medopl.cn -> OPL_WORKSPACE_TLS_CERT_ID
```

If you have one wildcard or multi-domain certificate covering both hosts, you can provide it as `OPL_TLS_CERT_ID`; the deploy workflow will install it into both host-specific Secrets. If you already have a qcloud certificate Secret, set `OPL_TLS_SOURCE_NAMESPACE` and `OPL_TLS_SOURCE_SECRET_NAME` to copy it into the two OPL Cloud Secret names.

## Production Env Template

Tracked template:

```text
deploy/tke/opl-cloud-production.env.example
.env.production.inputs.example
```

Ignored local file for the operator to fill:

```text
.env.production.local
```

Do not commit a filled env file. Real values belong in ignored local files, Kubernetes Secrets, GitHub environment secrets, or the cluster secret manager.

## Inputs Already Confirmed

- `OPL_RUNTIME_PROVIDER=tencent-tke`
- Workspace runtime image means the `one-person-lab-app` runtime image.
- The v22 TKE cluster is the OPL Cloud production cluster.
- The v22 TCR registry/namespace continues to serve OPL Cloud.
- The v22 kubeconfig is allowed for OPL Cloud deploy.
- The v22 PostgreSQL service is allowed for OPL Cloud control-plane and ledger persistence.
- OPL Cloud database name is `OPLCloud` on `10.66.0.21:5432`.

## Current Production Inputs

- `OPL_CLOUD_IMAGE=uswccr.ccs.tencentyun.com/oplcloud/opl-cloud:fc1609074f2e`
- `OPL_WORKSPACE_IMAGE=uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app:latest`
- `OPL_K8S_NAMESPACE=opl-cloud`
- `OPL_IMAGE_PULL_SECRET_NAME=tcr-pull-secret`
- `OPL_WORKSPACE_STORAGE_CLASS=cbs`
- `OPL_WORKSPACE_VOLUME_SNAPSHOT_CLASS` should point at the TKE/CBS `VolumeSnapshotClass` used for Workspace storage backups. If the cluster has a default `VolumeSnapshotClass`, this can be omitted, but production operators should set it explicitly before claiming backup readiness.
- `OPL_BILLING_MARKUP=0.2`
- `OPL_BASIC_COMPUTE_HOURLY_CNY=0.39`
- `OPL_PRO_COMPUTE_HOURLY_CNY=3.09`
- `OPL_STORAGE_GB_MONTH_CNY=0.36`
- `DATABASE_URL`, TCR credentials, kubeconfig, and TLS certificate ids are installed as GitHub production environment secrets and Kubernetes Secrets. Do not copy their values into git.
- `cloud.medopl.cn` and `workspace.medopl.cn` point at the OPL Cloud TKE Ingress CLB.

## Verified Entrypoints

The current production deployment has these verified HTTPS entrypoints:

- `https://cloud.medopl.cn/api/state` returns HTTP 200.
- `https://cloud.medopl.cn/api/production/readiness` returns HTTP 200 with `ready: true`.
- `https://workspace.medopl.cn/` returns HTTP 200.

The TKE Ingress must keep this annotation so HTTPS traffic reaches the pod backend:

```text
ingress.cloud.tencent.com/direct-access: "true"
```

Do not treat a green entrypoint check as proof of the full Workspace lifecycle. The full lifecycle proof remains `npm run verify:production`, which creates real runtime resources and requires explicit operator approval.

## TKE Runtime Provider

The `tencent-tke` runtime provider maps one OPL Workspace to:

- one Deployment for the one-person-lab-app runtime compute
- one Service
- one Ingress path under `workspace.medopl.cn/w/<workspaceId>`
- one Secret for the Workspace token
- one PVC for retained workspace storage

Stopping, destroying, or recreating compute must not delete the PVC. PVC deletion is only done by the explicit storage destroy path.

The production verifier also calls the read-only runtime status API for TKE Workspaces. That check verifies the Deployment is ready, the one-person-lab-app image matches the Workspace runtime image, the PVC is bound, the Deployment mounts the retained PVC, the Service has endpoints, and the Ingress route points at the expected Workspace Service path.

## Workspace Storage Backup And Restore

OPL Cloud storage backup uses Kubernetes `VolumeSnapshot` for the retained Workspace PVC. On Tencent TKE this is backed by CBS CSI snapshot capability.

Default policy:

```text
daily_7_weekly_4
retainLast=11
restoreMode=restore_to_new_workspace
```

The current control-plane API is:

```text
POST /api/workspaces/storage-backups
POST /api/workspaces/restore-storage-backup
POST /api/workspaces/prune-storage-backups
```

Restore is intentionally not in-place. A restore creates a new billable Workspace with a new PVC whose `dataSource` points at the selected `VolumeSnapshot`. The restored Workspace must pass the same prepaid hold rule as any new Workspace: seven days of compute and storage are frozen first, and the first hour is charged immediately.

Retention pruning deletes only `VolumeSnapshot` objects. It must never delete the source PVC, a restored PVC, runtime compute, Service, Ingress route, or Workspace access token.

## Where To Put Values

Use three locations:

| Value type | Local dry-run location | Cluster/runtime location | Git-tracked reference |
| --- | --- | --- | --- |
| Non-secret config such as domains, namespace, ingress class, image refs, storage class | ignored `.env.production` | ConfigMap or Deployment env | `deploy/tke/opl-cloud-production.env.example` and `deploy/production-manifest.example.json` |
| Secret references such as `DATABASE_URL`, kubeconfig ref, TCR credentials | ignored `.env.production` only if needed locally | Kubernetes Secret, GitHub environment secret, or secret manager | only `secretRef` names in `deploy/production-manifest.example.json` |
| DNS record values | not known until Ingress exists | Tencent DNS console | documented in this file only |

Recommended Kubernetes Secret keys:

```text
Secret opl-cloud-database
  DATABASE_URL

Secret opl-cloud-operator
  OPL_OPERATOR_SUMMARY_TOKEN

Secret opl-cloud-deploy
  TENCENT_DEPLOY_KUBECONFIG_REF

Secret tcr-pull-secret
  .dockerconfigjson
```

`OPL_OPERATOR_SUMMARY_TOKEN` is optional. When it is absent, `GET /api/operator/summary` is disabled with a 403 response. When it is present, call the endpoint with the `x-opl-operator-token` header.

The tracked production manifest is a contract, not the live secret payload. Keep real secret values outside git.

## Billing

OPL Ledger is the v1 billing truth. Workspace pricing uses a local Tencent price catalog snapshot and applies `OPL_BILLING_MARKUP`, defaulting to `0.2`.

Current production defaults were queried from Tencent Cloud in `na-siliconvalley` on 2026-07-02 using CVM `DescribeZoneInstanceConfigInfos` and the existing CBS storage price snapshot:

- Basic compute cost: `SA5.MEDIUM4`, `0.39 CNY/hour`; billed user price after markup: `0.468 CNY/hour`.
- Pro compute cost: `SA5.4XLARGE32`, `3.09 CNY/hour`; billed user price after markup: `3.708 CNY/hour`. The Pro Pod requests `8 CPU / 16Gi`; this larger CPU node class is the current capacity backing because an `8 CPU / 16GB` CVM does not expose enough Kubernetes allocatable CPU and memory for a single Pro Pod after system reservations.
- Storage cost: `CLOUD_PREMIUM`, `0.36 CNY/GB-month`; billed user price after markup: `0.432 CNY/GB-month`.

GPU pricing is intentionally not part of the current production package set. GPU Workspaces require a verified GPU node pool before they can be exposed without drifting from the OPL Cloud product truth.

The production billing chain is:

```text
Workspace open/resume -> 7-day compute and storage holds -> hourly internal debits from available balance -> frozen hold consumption only after available balance is exhausted -> hold release or auto-stop/freeze -> Tencent bill reconciliation
```

External metering systems are not required for production billing. OPL Ledger remains the v1 billing truth.
