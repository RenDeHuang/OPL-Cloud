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

OPL Console has two service-connected operator modes and one fake-only local
preview:

- `local-demo`: localhost-only React Console backed by in-memory fixture data;
  it cannot reach external services or mutate real billing or resources.
- `local-to-staging`: local Console API/UI connected to staging PostgreSQL and
  staging TKE; ordinary use is read-only, and any real mutation requires its
  separately approved operator workflow.
- `cloud-staging`: deployed Console in TKE using the same staging PostgreSQL and resource pool; validates rollout, ingress, TLS, image, and secret wiring.

The code path is shared. The difference is environment and ingress. `local-to-staging` and `cloud-staging` use the same durable service databases and Sub2API account mapping so accounts, monthly entitlements, resources, receipt references, and Workspace URLs describe one system.

## Local Demo

```bash
npm run demo
```

The command prints the localhost URL and customer/Admin fixture credentials. It
binds only to `127.0.0.1`, stores state in memory, and makes zero external
requests. It is an interaction preview, not staging or production evidence.

## Local To Staging

```bash
cp deploy/tke/opl-cloud-staging.local.env.example .env.staging.local
npm run staging:readiness
npm run staging:local
npm run staging:ui
```

`staging:local` loads the ignored `.env.staging.local`, builds the Go Tencent provisioner, requires `OPL_RUNTIME_PROVIDER=tencent-tke`, and uses staging PostgreSQL. It does not reset state or seed demo users.

The former paid staging verifier is retired and intentionally exits non-zero:

```bash
npm run staging:e2e
```

Do not use local-to-staging as a purchase, cleanup, or release-evidence path.
Provider, billing, and customer-resource writes require their separately
approved manual workflows.

## Cloud Staging Verification

Without a new release-owner approval, only read-only verification is authorized:

```bash
npm run validate:production-manifest -- \
  --manifest deploy/production-manifest.example.json
OPL_VERIFY_SLOT_DESCRIPTOR_JSON='<approved-fixed-slot-descriptor>' \
  npm run verify:production -- --read-only \
  --origin https://<console-domain> \
  --account <reserved-account-id>
OPL_CONSOLE_ORIGIN=https://<console-domain> \
  node tools/production-live-qa.ts --read-only
node tools/provider-acceptance.ts --read-only
```

These commands do not buy, renew, or delete Tencent resources and do not charge
a customer. The production verifiers also require the secret-backed credentials
listed below; placeholder values are not evidence. The separately approved Basic
customer canary is not an ordinary CI, rollout, E2E, or local-development command.

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
- `OPL_SUB2API_BASE_URL`: server-only Sub2API management origin. It must never be
  returned to the browser or used as a public endpoint fallback.
- `OPL_SUB2API_ADMIN_EMAIL` and `OPL_SUB2API_ADMIN_PASSWORD`: secret-backed management credentials.
- `OPL_MONTHLY_BILLING_WORKER_ENABLED`: enables renewal and expiration processing.
- `OPL_BASIC_COMPUTE_INSTANCE_TYPE` and `OPL_PRO_COMPUTE_INSTANCE_TYPE`: package
  SKUs. Monthly preflight discovers exactly one matching OPL-labeled NodePool;
  static NodePool ID environment variables are not used.

## Route Registry Rules

- `apps/console-ui/src/app/console-router.ts` contains only current runtime routes.
- Speculative routes do not belong in the runtime registry.
- Every enabled UI route must have a stable route id and routeTo path.
- Lab Owner routes do not expose operator/Fabric/Ledger raw evidence.

## Workspace Billing Semantics

- One Workspace purchase or renewal creates exactly one customer debit for the
  package total in integer USD micros.
- Compute, storage, attachment, Gateway Secret, and Runtime are fulfillment of
  that Workspace operation; they do not create independent customer charges.
- Control Plane persists the stable operation identity and converges state after
  authoritative Sub2API and Fabric readback.
- Renewal extends from `paidThrough` with the original Workspace price snapshot
  and stable redeem code.
- Unpaid expiry denies Workspace access, disables auto-renew, and writes
  evidence. It performs no Fabric or Tencent stop, renew, destroy, or delete
  mutation; Tencent expiry policy owns eventual provider reclamation.
- Fabric owns provider state, Control Plane owns Workspace orchestration,
  Sub2API owns balance/Key/Usage, and Ledger stores append-only evidence.

## Pre-Commit Checklist

```bash
npm test
npm run typecheck
npm run lint
npm run build
(cd services/control-plane && go test ./...)
(cd services/fabric && go test ./...)
(cd services/ledger && go test ./...)
git diff --check
```

## Common Failures

- Image pull denied: make `OPL_WORKSPACE_IMAGE` pullable and verify `OPL_IMAGE_PULL_SECRET_NAME`.
- Localhost Workspace URL: any approved real Workspace verification requires a
  public `OPL_WORKSPACE_DOMAIN`; the local demo does not prove it.
- Missing storage class: set `OPL_WORKSPACE_STORAGE_CLASS` to an available class.
- Ingress path not routing: check shared Ingress class and `/w/<workspaceId>` path.
- Unexpected provider resources: stop and reconcile through the approved
  operator path; verification code must not delete customer CVM/CBS resources.
