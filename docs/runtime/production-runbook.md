# Production Runbook

## Readiness

```text
GET /api/runtime/readiness
GET /api/production/readiness
```

Validate deployment secret references:

```bash
npm run validate:production-manifest -- \
  --manifest deploy/production-manifest.example.json
```

## Required Inputs

- PostgreSQL `DATABASE_URL`;
- Console auth users with positive integer `sub2apiUserId` values;
- `OPL_SUB2API_BASE_URL`, supported version list, timeout, and admin credentials;
- TKE kubeconfig, namespace, domains, TLS, storage class, and image-pull secret;
- OPL Cloud and Workspace image references;
- Tencent mutation credentials, region, cluster, subnet, and security groups;
- internal service token and AionUI password seed.

Secrets must be GitHub/Kubernetes secrets. Never place credentials in the
manifest, command arguments, logs, or verifier artifacts.

## Deploy

Use the `Deploy TKE Production` workflow with immutable image references. It
installs secrets, renders the manifest, applies it, restarts all ConfigMap
consumers, and waits for each rollout.

Manual bounded rollout checks:

```bash
kubectl -n opl-cloud rollout status deployment/opl-cloud-control-plane --timeout=5m
kubectl -n opl-cloud rollout status deployment/opl-cloud-fabric --timeout=5m
kubectl -n opl-cloud rollout status deployment/opl-cloud-ledger --timeout=5m
```

Then check health and readiness through the production endpoints. An old image
digest or timeout is a failed rollout.

## Paid E2E

Use a dedicated mapped account with enough Sub2API balance. The exact guard is
mandatory:

```bash
OPL_CONSOLE_ORIGIN=https://cloud.medopl.cn \
OPL_VERIFY_AUTH_USERS_JSON='<secret auth seed>' \
OPL_VERIFY_PAID_CONFIRMATION=I_UNDERSTAND_THIS_SPENDS_REAL_BALANCE \
OPL_VERIFY_RUN_ID=<unique-run-id> \
npm run verify:production -- --browser-e2e
```

The verifier must prove:

1. one mapped account and live starting balance;
2. Basic compute charge of `50000000` USD micros;
3. 10 GB storage charge of `2571429` USD micros;
4. exact total balance delta of `52571429` USD micros;
5. stable redeem code per resource operation;
6. active monthly entitlements and two Ledger receipts;
7. compute, storage, attachment, and public Workspace URL readiness;
8. exact cleanup of only the run's resources.

Keep the run failed until cleanup is confirmed. Do not substitute broad cloud
cleanup commands for resource IDs recorded by the verifier.

## Billing Recovery

- `preparing`: inspect the Fabric operation; no charge is expected yet.
- `charge_pending`: replay the same persisted redeem code; never create a new code.
- `manual_review`: compare the exact Sub2API adjustment and balance snapshots
  before changing entitlement state.
- `active` with missing receipt: retry only the Ledger receipt write.
- expired compute: confirm provider deletion.
- expired storage: preserve data and block attachment until reactivation.

## Sub2API Updates

Sub2API stays on official images. Run the config repository's guarded updater:

```bash
cd /home/ubuntu/sub2api
bash tests/safe-update.test.sh
bash scripts/safe-update.sh
```

The updater accepts only an approved version/revision, probes login, version,
balance-read, and adjustment-route availability without changing a real balance,
and restores the previous digest on failure.
