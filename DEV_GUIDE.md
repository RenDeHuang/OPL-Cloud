# OPL Console Developer Guide

## Product Truth

OPL Console is the commercial control plane. The current resource model is:

- `ComputePool`: package-level Tencent TKE node pool for one fixed compute specification.
- `ComputeAllocation`: account-owned dedicated CVM node inside one ComputePool for one-person-lab-app.
- `StorageVolume`: account-owned retained PVC/cloud storage.
- `StorageAttachment`: a storage volume mounted to a ComputeAllocation at a mount path such as `/data`.
- `Workspace`: URL token and WebUI entry composed from an attached compute allocation/storage pair.
- `Sub2API`: the external owner of spendable USD balance, API keys, routing, and request usage.
- `Ledger`: append-only receipts and reconciliation evidence; it does not own spendable balance.

Workspace is not the only resource body. It is the access entry.

## Runtime Modes

OPL Console has two supported operator modes:

- `local-to-staging`: local Console API/UI connected to staging PostgreSQL and staging TKE; can create real Tencent resources after explicit operator confirmation.
- `cloud-staging`: deployed Console in TKE using the same staging PostgreSQL and resource pool; validates rollout, ingress, TLS, image, and secret wiring.

The code path is shared. The difference is environment and ingress. `local-to-staging` and `cloud-staging` use the same durable service databases and Sub2API account mapping so accounts, monthly entitlements, resources, receipt references, and Workspace URLs describe one system.

## Local To Staging

```bash
cp deploy/tke/opl-cloud-staging.local.env.example .env.staging.local
npm run staging:readiness
npm run staging:local
npm run staging:ui
```

`staging:local` loads the ignored `.env.staging.local`, builds the Go Tencent provisioner, requires `OPL_RUNTIME_PROVIDER=tencent-tke`, and uses staging PostgreSQL. It does not reset state or seed demo users.

Run real local-to-staging E2E only after readiness passes and you intend to create billable Tencent Cloud resources:

```bash
OPL_CONFIRM_REAL_CLOUD_E2E=1 npm run staging:e2e
```

This verifier may use a local Console origin such as `http://127.0.0.1:8787`, but the Workspace URL still must be a public HTTPS staging URL.

## Cloud Staging E2E

After rollout, run the public verifier against the deployed Console. Both Console and Workspace URLs must be public HTTPS URLs.

Expected chain:

1. Login.
2. Read the mapped user's live Sub2API USD balance.
3. Purchase Basic compute and verify the exact `50000000` USD-micro charge.
4. Purchase 10 GB storage and verify the exact `2571429` USD-micro charge.
5. Verify both monthly entitlements and Ledger receipts.
6. Attach storage to compute.
7. Create the Workspace URL and poll runtime readiness.
8. Open the public Workspace URL and receive HTTP 200 from one-person-lab-app.
9. Verify the exact total balance delta, stable redeem codes, and provider facts.
10. Detach and destroy only the resources created by the run; verify exact cleanup.

Run after staging is configured:

```bash
npm run validate:production-manifest
OPL_CONSOLE_ORIGIN=https://<console-domain> npm run verify:production
```

Rollout confidence gate:

```text
unit/contract tests pass
+ local-to-staging readiness pass
+ local-to-staging real E2E pass
+ cloud-staging readiness pass
+ cloud-staging public E2E pass
= ready to consider public launch
```

## Required Env Vars

- `OPL_RUNTIME_PROVIDER`: must be `tencent-tke`.
- `OPL_WORKSPACE_IMAGE`: pullable one-person-lab-app image.
- `OPL_WORKSPACE_DOMAIN`: public Workspace domain.
- `OPL_K8S_NAMESPACE`: Kubernetes namespace.
- `OPL_INGRESS_CLASS`: ingress class.
- `OPL_WORKSPACE_STORAGE_CLASS`: PVC storage class.
- `OPL_IMAGE_PULL_SECRET_NAME`: image pull secret.
- `TENCENT_DEPLOY_KUBECONFIG_REF`: kubeconfig path.
- `OPL_TENCENT_PROVISIONER_BIN`: local Go SDK provisioner binary used for Tencent Cloud mutations.
- `DATABASE_URL`: required for durable shared staging state.
- `OPL_SUB2API_BASE_URL`: Sub2API management origin.
- `OPL_SUB2API_ADMIN_EMAIL` and `OPL_SUB2API_ADMIN_PASSWORD`: secret-backed management credentials.
- `OPL_SUB2API_SUPPORTED_VERSIONS`: versions approved by the Gateway update gate.
- `OPL_MONTHLY_BILLING_WORKER_ENABLED`: enables renewal and expiration processing.

## Route Registry Rules

- `apps/console-ui/src/routes/opl-routes.ts` contains only current runtime routes.
- Speculative routes do not belong in the runtime registry.
- Every enabled UI route must have a stable route id and routeTo path.
- Lab Owner routes do not expose operator/Fabric/Ledger raw evidence.

## Compute Storage Billing Semantics

- Creating a ComputeAllocation prepares Fabric capacity, charges one calendar month through Sub2API, then activates the entitlement.
- Creating storage follows the same prepare-charge-activate order and accepts only positive 10 GB blocks.
- Attaching storage does not create a priced resource; it records the mount relationship.
- Workspace entry creates a URL token for an existing active attachment.
- Renewal extends from `paidThrough` with the original integer price snapshot and stable redeem code.
- Expired compute is destroyed. Expired storage is retained and inaccessible until explicitly reactivated.
- Fabric owns provider state, Control Plane owns entitlement, Sub2API owns balance, and Ledger owns receipts.

## Pre-Commit Checklist

```bash
node --test tests/domain/resource-provisioning.test.ts
cd services/fabric && go test ./...
node --test tests/ui/commercial-console-routes.test.ts tests/ui/commercial-console-surface.test.ts tests/ui/console-clickability-contract.test.ts
npm run build
git diff --check
```

## Common Failures

- Image pull denied: make `OPL_WORKSPACE_IMAGE` pullable and verify `OPL_IMAGE_PULL_SECRET_NAME`.
- Localhost Workspace URL: staging e2e must use a public `OPL_WORKSPACE_DOMAIN`.
- Missing storage class: set `OPL_WORKSPACE_STORAGE_CLASS` to an available class.
- Ingress path not routing: check shared Ingress class and `/w/<workspaceId>` path.
- Leftover cloud resources: detach storage, destroy compute allocation, then destroy storage.
