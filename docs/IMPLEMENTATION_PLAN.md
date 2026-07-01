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

- `src/main.jsx`
- `services/api/src/opl-cloud.js`
- `tests/domain/workspace-lifecycle.test.js`
- `tests/domain/workspace-url-route.test.js`
- `contracts/opl-cloud-product-contract.json`
- `contracts/opl-cloud-workspace-lifecycle-contract.json`

### OPL Fabric Runtime Providers

Attempt:

- Keep Local Docker as the local runtime loop.
- Keep Tencent TKE as the production runtime provider.
- Keep Tencent CVM as a legacy fallback/debug provider.
- Hand off cloud provisioning through TKE, TCR, Kubernetes Ingress, persistent workspace storage, and legacy CVM contracts.

Receipts:

- `services/api/src/runtime-provider-factory.js`
- `services/api/src/runtime-providers/local-docker.js`
- `services/api/src/runtime-providers/tencent-cvm.js`
- `deploy/tke/opl-cloud-preproduction.env.example`
- `docs/TKE_PREPRODUCTION_DEPLOYMENT.md`
- `infra/tencent-cvm/`
- `tests/providers/local-docker-provider.test.js`
- `tests/providers/tencent-cvm-provider.test.js`
- `tests/providers/tencent-cvm-ansible.test.js`
- `tests/providers/server-provider-config.test.js`

### OPL Ledger And Evidence

Attempt:

- Keep OPL Console as the v1 billing truth.
- Emit OpenMeter usage events when configured.
- Preserve operation attempts, billing ledger entries, audit events, verifier output, and Tencent bill reconciliation evidence.

Receipts:

- `services/api/src/openmeter.js`
- `services/api/src/billing-reconciliation.js`
- `services/api/src/store.js`
- `tools/reconcile-tencent-bills.js`
- `tools/production-verifier.js`
- `tests/billing/`
- `tests/persistence/postgres-store.test.js`
- `tests/production/production-verifier.test.js`

### Production Readiness And Handoff

Attempt:

- Fail closed until production runtime provider, Harbor image, workspace domain, PostgreSQL, OpenMeter, Tencent environment, and required host tools are ready.
- Validate the production manifest without leaking secrets.
- Keep real cloud verification behind an operator-controlled human gate.

Receipts:

- `services/api/src/production-readiness.js`
- `services/api/src/production-manifest.js`
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
- Creating real Tencent CVM, CBS, DNS, or OpenMeter events.
- Injecting or confirming production secrets.

## Active Blockers

Preproduction launch remains blocked until the operator provides or confirms:

Required missing or unconfirmed TKE inputs:

- `OPL_CLOUD_IMAGE`
- `OPL_WORKSPACE_IMAGE`
- `DATABASE_URL` secret value installed for `postgresql://medopl:<password>@10.66.0.21:5432/OPLCloud`
- `OPENMETER_API_KEY`
- `OPL_WORKSPACE_STORAGE_CLASS`
- TLS secret or cert-manager issuer for `cloud.medopl.cn` and `workspace.medopl.cn`
- Ingress/CLB DNS target after TKE deploy

Confirmed preproduction resource decisions:

- `OPL_RUNTIME_PROVIDER=tencent-tke`
- `OPL_PUBLIC_URL=https://cloud.medopl.cn`
- `OPL_CONSOLE_DOMAIN=cloud.medopl.cn`
- `OPL_WORKSPACE_DOMAIN=workspace.medopl.cn`
- The v22 TKE cluster is the OPL Cloud preproduction cluster.
- The v22 TCR registry/namespace continues to serve OPL Cloud.
- The v22 kubeconfig is allowed for OPL Cloud deploy.
- The v22 PostgreSQL service is allowed for OPL Cloud control-plane and ledger persistence.

Legacy CVM-only inputs are no longer production blockers for the TKE route:

- `OPL_IMAGE_ID`
- `OPL_SSH_KEY_ID`

Do not print secret values. Do not commit `.env.preproduction*`.

## Next Step

Add the TKE runtime provider/readiness path and Kubernetes deployment manifest, rerun safe verification, and leave live TKE deploy, DNS changes, and production verifier execution gated for the operator.
