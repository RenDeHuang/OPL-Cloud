# OPL Cloud

OPL Cloud is the hosted implementation of the product defined by
[`one-person-lab-cloud`](https://github.com/gaofeng21cn/one-person-lab-cloud).
`one-person-lab` supplies the development framework; this repository owns the
Console, Control Plane, Fabric, Ledger, Workspace delivery, and Tencent TKE
deployment.

## Runtime Boundaries

- **Console** is the browser UI. It calls only Control Plane product APIs.
- **Control Plane** serves Console product commands and projections. It owns
  auth, account-to-Sub2API mapping, monthly entitlements, Workspaces, and
  orchestration state.
- **Fabric** owns CVM/CBS, attachments, runtimes, Tencent/Kubernetes calls, and
  provider facts. It does not own billing state.
- **Ledger** owns append-only receipts, reviews, artifacts, audit evidence, and
  reconciliation reports. It is not a spendable-balance service.
- **Sub2API at `gflabtoken.cn`** owns the only spendable USD balance, API keys,
  model routing, and request usage. It remains an external service and source
  repository.

```text
Console -> Control Plane -> Sub2API balance/charge
                         -> Fabric resource operations
                         -> Ledger evidence receipts
```

Control Plane exposes product commands only. It has no generic Fabric, Ledger,
or Sub2API proxy routes.

## Commercial Truth

Resources are prepaid by calendar month. Display prices are fixed CNY amounts;
charges use exact integer USD micros at `1 USD = 7 CNY`.

| Resource | Display price | Sub2API charge |
| --- | ---: | ---: |
| Basic compute, 2 CPU / 4 GB | CNY 350/month | 50,000,000 USD micros |
| Pro compute, 8 CPU / 16 GB | CNY 1,500/month | 214,285,715 USD micros |
| Basic default storage, 10 GB | CNY 18/month | 2,571,429 USD micros |
| Pro default storage, 100 GB | CNY 180/month | 25,714,286 USD micros |

Storage must be at least 10 GB and divisible by 10 GB. Unknown compute plans and
invalid storage sizes are rejected at both Control Plane and Fabric boundaries.

The approved launch settlement reuses the deployed Sub2API v0.1.156 deterministic
Redeem Code and Idempotency-Key path:

```text
validate -> read-only prepaid capacity/price preflight -> confirm balance
         -> Sub2API debit -> Fabric provision and claim
         -> activate monthly entitlements -> Ledger receipt
```

Stable operation identities make debit, provider mutation, claim, activation,
refund, and receipt retries safe. A confirmed no-resource result permits one
idempotent refund; a partial or ambiguous provider result enters manual review
without refund or repurchase. The current implementation still prepares Fabric
before debit and uses postpaid CVM; see `docs/invariants.md` for the frozen target
and per-stage delivery gaps.

## Workspace Model

```text
1 ComputePool       = one package placement pool
1 ComputeAllocation = one account-owned dedicated CVM
1 StorageVolume      = account-owned CBS storage
1 StorageAttachment  = one volume mounted to one allocation runtime
1 Workspace          = stable URL and current runtime pointer
```

Workspace URLs use:

```text
https://workspace.medopl.cn/w/<workspaceId>/
```

Opening a Workspace requires the Runtime password. One account can create
multiple Workspaces; no separate organization resource-pool abstraction is
required for that behavior.

Destroying compute does not delete storage. Storage deletion always requires an
explicit destructive command.

## Repository Layout

- `apps/console-ui`: React Console.
- `services/control-plane`: Console API and product orchestration.
- `services/fabric`: cloud resource and runtime owner.
- `services/ledger`: evidence owner.
- `packages/contracts`: current machine-readable product contracts.
- `deploy` and `.github/workflows`: TKE deployment and verification workflow definitions.
- `docs`: current architecture, invariants, status, and operations only.

## Local Verification

```bash
npm ci
npm test
npm run typecheck
npm run build
(cd services/control-plane && go test ./...)
(cd services/fabric && go test ./...)
(cd services/ledger && go test ./...)
```

Run the API locally with a PostgreSQL database, a Console auth seed whose users
contain positive integer `sub2apiUserId` values, and Sub2API admin credentials:

```bash
DATABASE_URL=postgres://opl:secret@127.0.0.1:5432/opl_cloud \
OPL_SUB2API_BASE_URL=https://gflabtoken.cn \
OPL_SUB2API_ADMIN_EMAIL=<admin-email> \
OPL_SUB2API_ADMIN_PASSWORD=<admin-password> \
PORT=8787 npm start
```

For the UI:

```bash
npm run dev
```

## Production

Production uses Tencent TKE and the three Go service binaries in one OPL Cloud
image. Validate secret references before deployment:

```bash
npm run validate:production-manifest -- \
  --manifest deploy/production-manifest.example.json
```

The `Deploy TKE Production` workflow installs database, internal-service,
Console auth, Sub2API, Tencent, image-pull, and Workspace secrets; renders the
manifest; restarts Control Plane, Fabric, and Ledger; and waits for each rollout.

The legacy production verifier is blocked by the launch freeze and is not a
release gate. It charges a real monthly product and creates then deletes Tencent
resources on every run. Its replacement must reuse the fixed prepaid
`SA5.MEDIUM4` and 10GB CBS Verification Slot, use fake monthly settlement, and
pay only for a dedicated test-key Gateway request.

See [docs/runtime/production-runbook.md](./docs/runtime/production-runbook.md)
for rollout and recovery commands.
