# OPL Cloud

OPL Cloud is the hosted implementation of the product defined by
[`one-person-lab-cloud`](https://github.com/gaofeng21cn/one-person-lab-cloud).
`one-person-lab` supplies the development framework; this repository owns the
Console, Control Plane, Fabric, Ledger, Workspace delivery, and Tencent TKE
deployment.

## Runtime Boundaries

- **Console** is the Vue browser UI. It calls only Control Plane product APIs.
- **Control Plane** owns local sessions, the account-to-Sub2API mapping,
  Workspace lifecycle, monthly operations, and customer-safe projections. It
  does not own customer passwords or a second identity system.
- **Fabric** owns CVM/CBS, attachments, runtimes, Tencent/Kubernetes calls, and
  provider facts. It does not own billing state.
- **Ledger** owns append-only receipts, reviews, artifacts, audit evidence, and
  reconciliation reports. It is not a spendable-balance service.
- **Sub2API** owns customer authentication, the only spendable USD wallet, API
  keys, model routing, request usage, and balance history. It remains external
  to this repository.

```text
Console -> Control Plane -> Sub2API balance/charge
                         -> Fabric resource operations
                         -> Ledger evidence receipts
```

Control Plane exposes product commands only. It has no generic Fabric, Ledger,
or Sub2API proxy routes.

## Invite-Only Pilot

The first cohort is 2-5 manually invited customer accounts. One Console User
maps to one OPL Account and one Sub2API User/Wallet. Console and Sub2API emails
must match after `lower(trim(email))`. Operators pre-fund the Sub2API wallet;
there is no public registration, payment/order UI, or customer Key mutation.

Customer prices are fixed monthly USD facts. The browser displays server DTOs
and never converts provider costs or derives totals.

| Workspace | Compute | Storage | Monthly total |
| --- | ---: | ---: | ---: |
| Basic (2 CPU / 4 GB) | USD 50.00 | USD 2.58 / 10 GB | USD 52.58 |
| Pro (8 CPU / 16 GB) | USD 214.28 | USD 25.80 / 100 GB | USD 240.08 |

Only Basic with 10 GB and Pro with 100 GB are accepted in this Pilot. The price
version is `pilot-usd-2026-07-v1`; all charge decisions use integer USD micros.

The approved launch settlement reuses the deployed Sub2API deterministic
Redeem Code and Idempotency-Key path:

```text
validate -> read-only prepaid capacity/price preflight -> confirm balance
         -> Sub2API debit -> Fabric provision and claim
         -> activate monthly entitlements -> Ledger receipt
```

Stable operation identities make debit, provider mutation, claim, activation,
refund, and receipt retries safe. A confirmed no-resource result permits one
idempotent refund; a partial or ambiguous provider result enters manual review
without refund or repurchase. This debit-first PREPAID chain is code-complete;
real Sub2API and Tencent execution evidence is still pending.

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

Opening a Workspace requires the Runtime password. One account owns exactly one
primary Workspace. A second Workspace creation returns 409. Backup, recovery,
sync, transfer, and collaboration flows are not Pilot capabilities.

Workspace file bodies stay on CBS and never enter OPL PostgreSQL or Ledger. CBS
survives ordinary Pod/CVM replacement, but OPL provides no Workspace backup or
recovery guarantee for deletion or corruption. Ordinary expiry, release, QA,
and rollback never delete CBS.

`autoRenew` defaults off. The current API rejects enabling it, and Console must
not expose an enable control until a real renewal is proven.

## Repository Layout

- `apps/console-ui`: Vue Console.
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

Run the API locally with PostgreSQL and Sub2API admin credentials. Do not set
the retired `OPL_CONSOLE_USERS_JSON`; invited owners are created through the
operator API and resolved by normalized email in Sub2API.

```bash
DATABASE_URL=postgres://opl:secret@127.0.0.1:5432/opl_cloud \
OPL_SUB2API_BASE_URL=<sub2api-base-url> \
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

Basic and Pro each have a separate retained Provider Acceptance slot. Ordinary
release verification requires both slots but runs live QA once with one Basic
reserved account, one dedicated Key, and one model request. The code verifies
exact-one Usage, balance delta, image IDs, receipts, stable CVM/CBS facts, and
zero provider mutation. Provider Acceptance and this real request have not been
run for the current candidate, so the Pilot is not production-proven.

The current deploy workflow still references the retired
`OPL_CONSOLE_USERS_JSON` secret while the Control Plane fails closed when it is
present. That deployment cutover must be fixed and verified before production.

See [docs/runtime/production-runbook.md](./docs/runtime/production-runbook.md)
for rollout and recovery commands.
