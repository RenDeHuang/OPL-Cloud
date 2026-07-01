# OPL Cloud TKE Preproduction Deployment

## Decision

OPL Cloud preproduction uses Tencent TKE as the production-shaped runtime.

The former v22 MedOPL TKE cluster, TCR namespace, kubeconfig, and PostgreSQL are now treated as the OPL Cloud preproduction resource pool. Existing external resource names may still contain `medopl` until they are deliberately renamed, but repository language and new deployment assets should use OPL Cloud.

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

Use one Kubernetes TLS secret covering both domains:

```text
opl-cloud-medopl-cn-tls
```

The certificate should include these SANs:

```text
cloud.medopl.cn
workspace.medopl.cn
```

If cert-manager is used instead of a pre-created TLS secret, the issuer name must be added to the TKE manifest before live deploy.

## Preproduction Env Template

Tracked template:

```text
deploy/tke/opl-cloud-preproduction.env.example
```

Do not commit a filled env file. Real values belong in ignored local files, Kubernetes Secrets, GitHub environment secrets, or the cluster secret manager.

## Inputs Already Confirmed

- `OPL_RUNTIME_PROVIDER=tencent-tke`
- Workspace runtime image means the `one-person-lab-app` runtime image.
- The v22 TKE cluster is the OPL Cloud preproduction cluster.
- The v22 TCR registry/namespace continues to serve OPL Cloud.
- The v22 kubeconfig is allowed for OPL Cloud deploy.
- The v22 PostgreSQL service is allowed for OPL Cloud control-plane and ledger persistence.
- OPL Cloud database name is `OPLCloud` on `10.66.0.21:5432`.

## Still Needed

- `OPL_CLOUD_IMAGE`: the control-plane image built from this repository and pushed to TCR.
- `OPL_WORKSPACE_IMAGE`: the exact `one-person-lab-app` image reference and tag.
- `DATABASE_URL`: install the real value as a secret; do not commit the password.
- `OPENMETER_API_KEY`: generate and install as a secret.
- `OPL_WORKSPACE_STORAGE_CLASS`: confirm with `kubectl get storageclass`.
- TLS secret or cert-manager issuer for `cloud.medopl.cn` and `workspace.medopl.cn`.
- The Ingress/CLB address after deploy, then create the DNS records.
- Confirmation that `opl-cloud` is the target namespace and `tcr-pull-secret` is the image pull secret name.

## Where To Put Values

Use three locations:

| Value type | Local dry-run location | Cluster/runtime location | Git-tracked reference |
| --- | --- | --- | --- |
| Non-secret config such as domains, namespace, ingress class, image refs, storage class | ignored `.env.preproduction` | ConfigMap or Deployment env | `deploy/tke/opl-cloud-preproduction.env.example` and `deploy/production-manifest.example.json` |
| Secret references such as `DATABASE_URL`, `OPENMETER_API_KEY`, kubeconfig ref, TCR credentials | ignored `.env.preproduction` only if needed locally | Kubernetes Secret, GitHub environment secret, or secret manager | only `secretRef` names in `deploy/production-manifest.example.json` |
| DNS record values | not known until Ingress exists | Tencent DNS console | documented in this file only |

Recommended Kubernetes Secret keys:

```text
Secret opl-cloud-database
  DATABASE_URL

Secret opl-cloud-openmeter
  OPENMETER_API_KEY

Secret opl-cloud-deploy
  TENCENT_DEPLOY_KUBECONFIG_REF

Secret tcr-pull-secret
  .dockerconfigjson
```

The tracked production manifest is a contract, not the live secret payload. Keep real secret values outside git.

## OpenMeter

Default preproduction setting:

```text
OPENMETER_ENDPOINT=http://openmeter.opl-cloud.svc.cluster.local:8888
```

This assumes OpenMeter runs in the same cluster and is not public. If OpenMeter is external, replace the endpoint with its HTTPS URL and keep only the API key secret.
