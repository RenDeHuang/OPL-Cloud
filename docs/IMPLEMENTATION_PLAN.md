# OPL Cloud Implementation Goal Ledger

This repository is the OPL Cloud implementation workspace for the OPL Console and OPL Workspace control-plane slice.

## Product Truth

[`one-person-lab-cloud`](https://github.com/gaofeng21cn/one-person-lab-cloud) owns the Cloud product definition and fixed product layers:

- OPL Gateway
- OPL Workspace
- OPL Console
- OPL Fabric
- OPL Ledger

This repository currently implements the OPL Console / OPL Workspace control plane, with early OPL Fabric and OPL Ledger boundaries. The production-shaped deployment target is Tencent TKE.

## Development Truth

[`one-person-lab`](https://github.com/gaofeng21cn/one-person-lab) owns the development framework concepts. Work here is organized by:

- goal
- attempt
- readiness
- receipt
- blocker
- next step
- human gate
- recovery
- evidence

The repository should not accumulate phase-only smoke files, temporary reports, or broad staged narratives that outlive the actual implementation evidence.

## Goal

Support this business chain:

```text
PI signs in to OPL Console
-> creates an OPL Workspace
-> OPL Cloud creates one workspace runtime compute unit, one persistent workspace storage volume, one one-person-lab-app runtime container, and one URL
-> PI shares the URL
-> members enter the OPL Workspace without login
-> OPL Console manages lifecycle, billing, audit, readiness, recovery, and evidence
```

Resource invariant:

```text
1 OPL Workspace
= 1 runtime compute unit
= 1 one-person-lab-app runtime container
= 1 persistent workspace storage volume
= 1 URL
```

Compute and storage lifecycles stay separate. Stopping or recreating compute must not destroy workspace storage. Storage destruction is explicit and is the only action that stops storage billing.

## Current Attempts And Receipts

### Console And Workspace Control Plane

Attempt:

- Implement the PI-facing OPL Console for workspace distribution.
- Keep Workspace URLs token-gated and usable without member login.
- Preserve one PI account to many Workspaces.

Receipts:

- `packages/console/ui/main.jsx`
- `packages/console/src/opl-cloud.js`
- `tests/domain/workspace-lifecycle.test.js`
- `tests/domain/workspace-url-route.test.js`
- `packages/contracts/opl-cloud-product-contract.json`
- `packages/contracts/opl-cloud-workspace-lifecycle-contract.json`

### OPL Fabric Runtime Providers

Attempt:

- Keep Local Docker as the local runtime loop.
- Keep Tencent TKE as the production runtime provider.
- Keep Tencent CVM as a legacy fallback/debug provider.
- Hand off cloud provisioning through TKE, TCR, Kubernetes Ingress, persistent workspace storage, and legacy CVM contracts.

Receipts:

- `packages/fabric/src/runtime-provider-factory.js`
- `packages/fabric/src/runtime-providers/local-docker.js`
- `packages/fabric/src/runtime-providers/tencent-tke.js`
- `packages/fabric/src/runtime-providers/tencent-cvm.js`
- `deploy/tke/opl-cloud.k8s.json`
- `deploy/tke/opl-cloud-production.env.example`
- `docs/TKE_PRODUCTION_DEPLOYMENT.md`
- `infra/tencent-cvm/`
- `tests/providers/local-docker-provider.test.js`
- `tests/providers/tencent-cvm-provider.test.js`
- `tests/providers/tencent-cvm-ansible.test.js`
- `tests/providers/server-provider-config.test.js`

### OPL Ledger And Evidence

Attempt:

- Keep OPL Console as the v1 billing truth.
- Keep OPL Ledger as the v1 billing truth with prepaid compute/storage holds, hourly internal debits from available balance first, frozen-hold consumption after balance exhaustion, hold release, and auto-stop/freeze receipts.
- Preserve operation attempts, billing ledger entries, audit events, notifications, verifier output, and Tencent bill reconciliation evidence.

Receipts:

- `packages/ledger/src/billing-reconciliation.js`
- `packages/console/src/store.js`
- `tools/reconcile-tencent-bills.js`
- `tools/production-verifier.js`
- `tests/billing/`
- `tests/persistence/postgres-store.test.js`
- `tests/production/production-verifier.test.js`

### Production Readiness And Handoff

Attempt:

- Fail closed until production runtime provider, registry images, workspace domain, PostgreSQL, Tencent environment, and required host tools are ready.
- Validate the production manifest without leaking secrets.
- Keep real cloud verification behind an operator-controlled human gate.

Receipts:

- `packages/console/src/production-readiness.js`
- `packages/console/src/production-manifest.js`
- `deploy/production-manifest.example.json`
- `docs/PRODUCTION_RUNBOOK.md`
- `tools/validate-production-manifest.js`
- `tests/production/production-readiness.test.js`
- `tests/production/production-manifest.test.js`
- `tests/production/production-manifest-cli.test.js`

## Readiness Gates

Local readiness:

```text
GET /api/runtime/readiness
```

Production readiness:

```text
GET /api/production/readiness
```

Manifest readiness:

```bash
npm run validate:production-manifest -- --manifest deploy/production-manifest.example.json
```

Structural readiness:

```bash
sentrux check .
```

Development verification:

```bash
npm test
npm run build
```

## Human Gates

The following actions require explicit human approval before execution:

- Renaming the GitHub repository.
- Renaming the local folder.
- Running `npm run verify:production`.
- Creating real Workspace runtime resources, storage, DNS, or billing side effects outside the documented production deploy workflow.
- Injecting or confirming production secrets.

## Current Production Evidence

The OPL Cloud TKE production entrypoint is deployed and externally reachable.

Verified production inputs:

- `OPL_CLOUD_IMAGE=uswccr.ccs.tencentyun.com/oplcloud/opl-cloud:fc1609074f2e`
- `OPL_WORKSPACE_IMAGE=uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app:latest`
- `OPL_RUNTIME_PROVIDER=tencent-tke`
- `OPL_PUBLIC_URL=https://cloud.medopl.cn`
- `OPL_CONSOLE_DOMAIN=cloud.medopl.cn`
- `OPL_WORKSPACE_DOMAIN=workspace.medopl.cn`
- `OPL_WORKSPACE_STORAGE_CLASS=cbs`
- `OPL_K8S_NAMESPACE=opl-cloud`
- `OPL_IMAGE_PULL_SECRET_NAME=tcr-pull-secret`
- The v22 TKE cluster is the OPL Cloud production cluster.
- The v22 TCR registry/namespace continues to serve OPL Cloud.
- The v22 kubeconfig is allowed for OPL Cloud deploy.
- The v22 PostgreSQL service is allowed for OPL Cloud control-plane and ledger persistence.
- TLS is installed through Tencent qcloud certificate-id Secrets:
  - `opl-cloud-console-medopl-cn-tls`
  - `opl-cloud-workspace-medopl-cn-tls`
- `cloud.medopl.cn` and `workspace.medopl.cn` both point at the OPL Cloud TKE Ingress CLB.
- The TKE Ingress uses `ingress.cloud.tencent.com/direct-access: "true"` so HTTPS traffic reaches the pod backend.

Verified external entrypoints:

- `https://cloud.medopl.cn/api/state` returns HTTP 200.
- `https://cloud.medopl.cn/api/production/readiness` returns HTTP 200 with `ready: true`.
- `https://workspace.medopl.cn/` returns HTTP 200.

Verified production Workspace lifecycle:

- Run id: `20260702T001006Z-pilot-gaps`.
- Verification Workspace: `ws-9w6zwy`.
- Receipt path: `.runtime/verification/20260702T001006Z-pilot-gaps.stdout.json` (ignored, not committed).
- Result: `ok: true`.
- Runtime status passed on first attempt:
  - `deployment_ready`
  - `workspace_image_pulled`
  - `pvc_bound`
  - `deployment_uses_retained_pvc`
  - `service_targets_workspace`
  - `service_endpoints_ready`
  - `ingress_routes_workspace_url`
- Workspace URL opened on first attempt before and after compute recreation.
- Lifecycle checks passed:
  - stop compute while retaining storage
  - restart compute
  - destroy compute while retaining storage
  - recreate compute from retained storage
  - settle billing
  - destroy verification compute
  - destroy verification storage
- Cleanup errors: none.
- Console state after cleanup: `state=destroyed`, `server.billingStatus=stopped`, `disk.billingStatus=stopped`, `access.tokenStatus=unavailable`, `account.frozen=0`.
- Previous successful run: `20260701T234830Z-console-decoupling`.

Legacy CVM-only inputs are no longer production blockers for the TKE route:

- `OPL_IMAGE_ID`
- `OPL_SSH_KEY_ID`

Do not print secret values. Do not commit `.env.production*` or legacy `.env.preproduction*` files.

## Current Pilot Gate

Use [CONTROLLED_PILOT_CHECKLIST.md](./CONTROLLED_PILOT_CHECKLIST.md) as the current non-reconciliation pilot gate. Tencent bill reconciliation remains a separate finance validation task. The OPL Console control-plane should not claim public GA or full OPL Cloud completeness until payment settlement, data backup/restore policy, Gateway product surface, and full Ledger service boundaries are separately closed.
