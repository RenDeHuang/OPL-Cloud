# OPL Cloud

OPL Cloud is the cloud product of One Person Lab. This repository is its single
product and implementation owner: it contains the public architecture,
whitepaper and roadmap together with Console, Control Plane, Fabric, Ledger,
Workspace delivery, contracts and release mechanisms. `one-person-lab` supplies
the development framework and `one-person-lab-app` supplies the local and
Workspace application experience.

The canonical repository name is `one-person-lab-cloud`, aligned with
`one-person-lab-app`. The short identifier `opl-cloud` remains correct for npm,
images, binaries, services, namespaces, environment variables and runner
labels. Concrete installations belong to instance repositories; the first
commercial instance is `opl-instance-medopl`. Current medopl/Tencent TKE values
remain temporarily co-located here as explicit instance-migration state.

Product target truth starts at [docs/architecture.md](./docs/architecture.md),
current implementation truth at
[docs/implementation-architecture.md](./docs/implementation-architecture.md),
remaining gaps at [docs/roadmap.md](./docs/roadmap.md), and launch invariants at
[docs/invariants.md](./docs/invariants.md). None of these documents or their
tests alone proves a deployed runtime or production readiness.

Read the [OPL Cloud whitepaper](https://gaofeng21cn.github.io/one-person-lab-cloud/latest/whitepapers/opl-cloud-whitepaper.html)
or its [source](./docs/whitepapers/opl-cloud-whitepaper.md).

## Product Surfaces

| Need | Product surface | Responsibility |
| --- | --- | --- |
| AI access and usage | **OPL Gateway** | Model access, routing, provider policy and usage signals |
| Online project work | **OPL Workspace** | Zero or more independent cloud workbenches per account |
| External Agent use | **OPL Serve** | Exact Service, immutable Revision, Deployment, API, Embed and Hosted UI |
| Account governance | **OPL Console** | Account onboarding, Workspace lifecycle, policy, quota and billing projection |
| Data, tools and compute | **OPL Fabric** | Provider-neutral Connect, Compute, Storage, Environments and execution adapters |
| Evidence continuity | **OPL Ledger** | Append-only receipts, provenance, review and continuation refs |

## Runtime Boundaries

- **Console** is the React browser UI, built on `@openai/apps-sdk-ui`. It calls
  only Control Plane product APIs.
- **Control Plane** owns local sessions, the account-to-Sub2API mapping,
  Workspace lifecycle, monthly operations, and customer-safe projections. It
  does not own customer passwords or a second identity system.
- **Fabric** owns provider resource operations, attachments, runtimes, and
  provider facts. The current production adapter is Tencent TKE/CVM/CBS; the
  product target is provider-neutral. Fabric does not own billing state.
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

The frozen Console display surface contains 10 primary pages: five customer
pages and five additional Admin pages. Together with the global account and
support views, these pages expose 27 slides defined by
[`docs/product/console-display-contract-v1.md`](./docs/product/console-display-contract-v1.md).
That display contract fixes what the UI presents; it does not change business
readiness or make the browser an authority for product facts.

## Operator-Provisioned Pilot

Accounts are administrator-provisioned. One Console User maps to one OPL Account
and one Sub2API User/Wallet. Console and Sub2API emails must match after
`lower(trim(email))`. Operators pre-fund the Sub2API wallet; there is no public
registration or payment/order UI. The current launch capacity evidence uses a
1000-provisioned-user data set; it is not a claim of 1000 concurrent users,
concurrent logins, concurrent provisioning operations, or HA. Owners may manage
general Keys through Control Plane using a Session-bound delegated credential.
Each Workspace launch converges its own reserved Key and Kubernetes Secret from
a stable `workspaceId` identity; the legacy `opl-workspace` Key remains bound
only to its existing Workspace.

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
         -> one Sub2API Workspace-total debit -> Fabric compute/storage fulfillment
         -> activate one Workspace entitlement -> one Ledger purchase receipt
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

Opening a Workspace requires the Runtime password. One Account/Wallet may own
multiple independent Workspaces; each idempotency identity replays one Workspace,
while a new identity creates another Workspace with independent resources,
Workspace Key, Secret, period, and Receipt. Backup, recovery, sync, transfer,
and collaboration flows are not Pilot capabilities.

Workspace file bodies stay on CBS and never enter OPL PostgreSQL or Ledger. OPL
provides no Workspace backup or recovery guarantee for provider expiry, deletion,
or corruption. Unpaid expiry denies access and writes evidence only; it performs
no Fabric or Tencent stop, renew, destroy, or delete mutation.

`autoRenew` defaults off. The current API rejects enabling it, and Console must
not expose an enable control until a real renewal is proven.

Console displays and copies the public model endpoint
`https://gflabtoken.cn/v1` through a Control Plane projection. The endpoint is
text only: Console never links or redirects to Sub2API, embeds it, or calls its
management API from the browser. `OPL_SUB2API_BASE_URL` remains server-only,
and Cloud does not inject a second Gateway base URL into Runtime.

The current React Console implementation has local `code-complete` evidence
only. Overall Pilot V2 delivery remains `codeComplete=false`,
`pilotReady=false`, and `productionProven=false` until the complete release
gates and separately approved real evidence pass. Only the same immutable
deployed revision with production readback can be `production-proven`.

## Repository Layout

- `apps/console-ui`: React Console built on `@openai/apps-sdk-ui`.
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
the retired `OPL_CONSOLE_USERS_JSON`; provisioned owners are created through
the operator API and resolved by normalized email in Sub2API.

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

For a clickable localhost-only demo with in-memory fixture data:

```bash
npm run demo
```

Demo credentials:

- Customer: `fixture@example.com` / `fixture-password`
- Admin: `operator@example.com` / `operator-password`

The command also prints the URL and credentials at startup. It binds only to
`127.0.0.1`, calls no external service, and cannot perform real billing,
Sub2API, Fabric, Ledger, Tencent, or Kubernetes mutations. This is a development
preview, not a public test mode or production readiness claim.

## Production

The current `medopl` production instance uses Tencent TKE and the three Go
service binaries in one OPL Cloud image. Its domains, Tencent profile, enabled
plans and prices, image pins, secret refs, and deployment evidence are still
temporarily co-located here until `opl-instance-medopl` is materialized. That
co-location is migration state, not the reusable repository boundary.

Validate secret references before deployment:

```bash
npm run validate:production-manifest -- \
  --manifest deploy/production-manifest.example.json
```

The `Deploy TKE Production` workflow installs database, internal-service,
Sub2API, Tencent, image-pull, and Workspace secrets; renders the
manifest once; and observes Control Plane, Fabric, and Ledger within one shared
300-second deadline. It first requires healthy nodefs/imagefs capacity, preserves
the current Workspace digest without restarting Workspace Deployments, and
uploads failure diagnostics before the independent rollback job.

Basic and Pro definitions and prices remain in code, and both are present in the
production catalog. Catalog visibility is not evidence of a real purchase;
separately approved provider verification remains paused and does not gate
ordinary rollout.

The retired local Console user seed is no longer accepted by deployment. The
workflow bootstraps the fixed operator from Sub2API and administrators open users
through `POST /api/operator/accounts`. An ordinary Cloud rollout has been read
back, but the Basic canary, customer Workspace imageID, and model Usage evidence
remain incomplete.

See [docs/runtime/production-runbook.md](./docs/runtime/production-runbook.md)
for rollout and recovery commands.
