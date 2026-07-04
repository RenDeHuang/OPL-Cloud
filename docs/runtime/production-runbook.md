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

Use the production verifier only from an approved operator environment.

The verifier creates a real ComputeAllocation, StorageVolume, and StorageAttachment, creates a Workspace URL entry, opens the public URL, records one Gateway request usage event, verifies wallet/ledger/usage state, and attempts cleanup.

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
