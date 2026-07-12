# Production Runbook

## Readiness

Runtime readiness:

```text
GET /api/runtime/readiness
```

Production readiness:

```text
GET /api/production/readiness
```

Manifest validation:

```bash
npm run validate:production-manifest -- --manifest deploy/production-manifest.example.json
```

## Required Production Inputs

Production must provide:

- `DATABASE_URL`
- `OPL_RUNTIME_PROVIDER=tencent-tke`
- `OPL_CLOUD_IMAGE`
- `OPL_WORKSPACE_IMAGE`
- `OPL_PUBLIC_URL`
- `OPL_CONSOLE_DOMAIN`
- `OPL_WORKSPACE_DOMAIN`
- `OPL_WORKSPACE_STORAGE_CLASS`
- Tencent Kubernetes credentials through secret files or secret refs
- production auth seed or persisted users

## Human Gates

The following actions require explicit human approval:

- running real production verification;
- creating real ComputeAllocation, StorageVolume, and StorageAttachment resources;
- injecting or confirming production secrets;
- changing production domain, TLS, registry, or kubeconfig inputs;
- destroying production StorageVolume data.

## Verification

Use `npm run staging:e2e` from a local operator shell before rollout when the local Console is connected to staging PostgreSQL and staging TKE. This command requires `OPL_CONFIRM_REAL_CLOUD_E2E=1`, may use a local Console origin, and still requires a public HTTPS Workspace URL.

Use `npm run verify:production` only after cloud staging rollout from an approved operator environment. This command requires public HTTPS Console and Workspace URLs.

Both verifiers create a real ComputeAllocation, StorageVolume, and StorageAttachment, create a Workspace URL entry, open the public URL, verify wallet, Ledger facts, Fabric/provider evidence, and attempt cleanup. Paid resource cleanup passes only when the destroy response is `billingStatus=stopped` and returns a `holdReleaseId` for the same `holdId` created during provisioning.

Resource billing must follow this order:

1. Reserve seven days plus the first hour in the resource Hold.
2. Activate the Hold only after Fabric verifies the claimed CVM/PVC identity; activation charges the first hour.
3. Start periodic settlement after that prepaid hour. Settlement uses available balance first, then only the resource's own Hold.
4. Persist `billingStatus=stopping` before cloud deletion. After deletion is confirmed, stop future settlement and release the Hold remainder without another debit.
5. If provisioning exhausts its retries without a Machine, persist `failed`; reconciliation releases the unactivated Hold.

Verification output belongs in runtime evidence or `docs/history/**`, not active docs.

## Recovery

For Workspace action failures:

1. Check runtime operations.
2. Check audit events.
3. Check billing ledger and wallet transactions.
4. Check Fabric provider evidence.
5. Detach storage before destroying compute or storage resources.
6. Destroy storage only after explicit owner/admin confirmation.

For billing failures:

1. Stop new Workspace openings if reconciliation fails closed.
2. Preserve existing storage unless explicit destruction is confirmed.
3. Use manual top-up audit for operator-funded corrections.
