# Launch Invariants

This file is the mandatory human-readable launch contract for this implementation repository. The target product boundaries come from `https://github.com/gaofeng21cn/one-person-lab-cloud`; the revision reviewed for this freeze is `c349a41d860e706ed43a4090b9e75abb0b130971`.

The upstream repository owns product architecture. This repository owns its selected backend, exact prices, provider procurement, delivery state, and runtime evidence. A frozen target is not a readiness claim. Current gaps and required evidence are recorded here and in `packages/contracts/opl-cloud-launch-freeze-contract.json`.

## Product Surfaces And Owner Lanes

The five product surfaces are OPL Gateway, OPL Workspace, OPL Console, OPL Fabric, and OPL Ledger. Workspace is the product delivered by Fabric after it opens CVM and CBS and deploys the pinned `one-person-lab-app` image; it is not a fifth service repository.

The four implementation owner lanes are Console/Control Plane, Fabric, Gateway integration, and Ledger. Gateway integration is an adapter to the externally deployed Sub2API, never a second Gateway service.

## Pilot Scope

- The first cohort is 2-5 invited customer accounts; public registration is forbidden.
- One Console User maps to one OPL Account and one Sub2API User/Wallet. Console
  and Sub2API email must match after `lower(trim(email))`.
- Organization and Membership rows are internal one-to-one compatibility
  records only. They do not authorize sharing or appear in customer DTOs.
- Operators manually pre-fund or adjust the Sub2API wallet through audited
  recharge, debit, and business-refund commands. There is no customer payment,
  top-up, or payment-order surface. Owners may manage general API Keys.
- Each account has one Workspace. Basic and Pro are the only Pilot packages.
- `autoRenew` defaults off. Enabling it is rejected and hidden until a real
  renewal has been approved and proven.
- Backup, recovery, sync, transfer, HA, and multiple Workspaces are not Pilot
  capabilities.

## Console

- Console calls only Control Plane product APIs.
- Sub2API authenticates customer credentials. Control Plane owns local Sessions,
  account mapping, quotes, monthly orchestration, entitlements, expiry, and
  operator review; it stores no second customer password truth.
- Operators open invited customers through canonical `POST /api/operator/accounts`
  with `ProvisionAccountRequest`. The command uses `provision` semantics,
  `account.provision` audit action, and an `account-provision` operation identity.
  The backend resolves or creates the Sub2API identity by normalized email and
  atomically stores the one-to-one local graph. Self-registration and SSO are not
  Pilot claims.
- `admin@medopl.cn` owns `acct-admin` and also has operator capability. It enters
  `/console/overview` by default, may use its own `/console/*` resources, and has
  the additional `/admin/*` menu. Operator metadata access never grants owner
  access to another account's Key, password, or Workspace credential.
- The customer table is labelled "客户与计费账户", includes `acct-admin` with
  an administrator marker, and forbids disabling that reserved account in both
  Console and Control Plane.
- Console displays live Sub2API balance, Key metadata, request usage, usage stats, and Ledger billing receipts without creating a wallet, Key database, usage database, or billing fact table.
- Basic is `2c4g` plus 10GB for `52_580_000` USD micros/month:
  `50_000_000` compute plus `2_580_000` storage.
- Pro is `8c16g` plus 100GB for `240_080_000` USD micros/month:
  `214_280_000` compute plus `25_800_000` storage.
- Basic and Pro are both open in the production catalog at their fixed prices.
  Catalog availability means the product can be selected; it is not a Tencent
  capacity claim. The shared Tencent MonthlyPreflight immediately before the
  first debit remains the capacity authority and fails before any side effect.
- Internal Acceptance slots are not customer products and never appear in
  catalog or quote paths. Static package definitions are targets; actual
  availability comes from live Fabric catalog readback.
- Pricing preview and Workspace launch reject an unavailable package with
  `package_unavailable` before Gateway, balance, debit, Ledger, or Tencent calls.

## Fabric

- Fabric is the only Tencent Cloud and Kubernetes writer.
- Customer and verification CVM/CBS procurement uses `PREPAID`, period 1 month, and `NOTIFY_AND_MANUAL_RENEW`.
- `POSTPAID_BY_HOUR` is forbidden for customer and verification CVM/CBS resources.
- Capacity and price preflight is read-only and happens before debit. It cannot buy, reserve, renew, or delete a Tencent resource.
- The shared real-Tencent monthly preflight fails closed unless
  `RUN_TENCENT_CREATE_RELEASE_EXECUTION=1`; this check runs before every first
  Sub2API debit and leaves both the charge count and Fabric mutation count at zero on failure.
- Compute preflight discovers the existing TKE NodePool with `DescribeNodePools`
  by exact `oplcloud.cn/pool-id`, `oplcloud.cn/package-id`, and
  `oplcloud.cn/instance-type` labels. Exactly one match is required. Zero,
  multiple, incomplete, or changed matches fail before debit; the Tencent
  NodePool ID is stored on `workspace.launch.v2` before debit.
- Fabric creates CBS with a stable `ClientToken`, reads back CVM/CBS identity and billing facts, then binds CBS through a static PV/PVC in the compute Zone.
- Static CBS uses `com.tencent.cloud.csi.cbs`, `volumeHandle=disk-*`, RWO, empty `storageClassName`, Zone affinity, and `persistentVolumeReclaimPolicy=Retain`.
- `UNATTACHED` or `ATTACHED` is provider-ready; PVC `Bound` is required before Workspace deployment.
- Fabric owns provider facts and never changes Sub2API balance or Control Plane entitlement state.

## Ledger

- Ledger records append-only debit, refund, fulfillment, claim, activation, renewal, expiry, review, and verification evidence.
- Ledger never owns or changes spendable balance.
- Customer billing history is read live from Ledger through an account-scoped paginated query and projected through a strict allowlist; Control Plane never copies receipt facts.
- Operator reconciliation is computed by Control Plane from active billing operations, Sub2API balance history, Fabric provider operations, and Ledger receipts. Ledger appends the deterministic exception-only report; Control Plane stores only the latest purchase guard and never repairs money, provider resources, or receipts automatically.
- Receipts contain stable account, Workspace, billing-operation, provider-operation, resource, pricing, period, and redacted Gateway request references.
- `workspace.access_token_reset` uses the stable Runtime credential-rotation identity and records only owner, Runtime, resource, Secret-reference, and credential-version metadata.
- API keys, passwords, raw tokens, provider secrets, and raw Sub2API responses are forbidden in evidence.
- A missing receipt retries only the receipt and never repeats debit, refund, provider purchase, Secret write, or renewal.

## Gateway

- OPL Gateway uses the externally deployed Sub2API backend. Compatibility is gated by required API capabilities; the reported version is diagnostic metadata and never blocks an otherwise compatible deployment. Sub2API code, image, container, database, configuration, and deployment remain immutable from this repository.
- Sub2API is the only owner of spendable USD balance, API keys, model routing, and request usage.
- Control Plane maps the signed-in account through `sub2apiUserId`. Owners,
  including the reserved administrator for its own account, may manage general Keys; Workspace
  convergence separately requires exactly one active reserved Key named `opl-workspace` and fails closed otherwise.
- Required read capabilities are mapped-user balance, available groups, the mapped user's paginated/filterable/sortable Key list, paginated request usage, and aggregate usage stats. Key creation requires a live Sub2API group. Request usage and stats are scoped by both `user_id` and the selected `api_key_id`; every returned identity is validated again by Control Plane.
- For Keys, UserKeys, Usage, and BalanceHistory, a zero-row Sub2API v0.1.162
  response is valid only as `total=0,page=1,pages=1,items=[]`; every other empty
  pagination shape fails closed.
- Request charges use Sub2API `actual_cost`, converted once to integer USD micros. Control Plane returns an explicit unavailable state for a missing capability or upstream failure and never substitutes zero.
- Control Plane decodes a strict customer-safe DTO allowlist. Raw Sub2API admin responses, nested raw Keys, upstream account internals, prompts, and response content never reach Console, OPL PostgreSQL, Ledger, logs, or caches.
- Key DTO fields `quota_used`, `usage_5h`, `usage_1d`, `usage_7d`, and `last_used_at` remain quota and recent-window signals; they do not replace request-level usage and aggregate stats.
- General Key management may project and mutate Sub2API's group, quota, IP allow/deny lists, expiration, 5h/1d/7d limits, and supported reset commands. Control Plane persists none of those facts. Operator Key counts are live, bounded-concurrency reads for only the current account page and are never stored.
- The owner may request an owned Key only through audited
  `POST /api/gateway/keys/{keyId}/reveal`. It is masked by default and
  never enters `/api/state`, browser storage, OPL PostgreSQL, Ledger, logs,
  caches, or operation payloads. The retired Gateway summary route is a 404.
- Kubernetes Secret is the only authorized Key persistence point. Fabric writes or rotates an account-scoped Secret, and Workspace runtime receives only its reference.
- The global `OPL_CODEX_API_KEY` is forbidden for customer Workspaces.
- Console may display and copy the public model endpoint derived from the existing
  configured Sub2API origin plus `/v1` (`https://gflabtoken.cn/v1` in production).
  It never exposes admin routes or credentials, links or redirects to the Sub2API
  UI, embeds it, scrapes HTML, or calls Sub2API directly from the browser. Runtime
  keeps the official App/Shell endpoint behavior and Cloud adds no second Gateway.

Every Console source projection carries `source`, `status`, `available`, and
`fetchedAt`. `empty` means a successful authoritative read with zero rows;
`unavailable` means the dependency failed and must not include fallback data,
zero values, empty collections, or a success state. `sourceUpdatedAt` is omitted
unless the authority supplies it. Identity scope comes only from the current
Session; browser `accountId`, `user_id`, and `api_key_id` inputs are ignored.

## Monthly Settlement

The approved purchase protocol does not depend on a generic hold/capture API. It uses the verified deterministic Redeem Code and Idempotency-Key path:

```text
validate account and quote
-> read-only provider capacity and price preflight
-> confirm live Sub2API balance
-> debit exact monthly amount
-> provision one-month PREPAID CVM and CBS
-> claim and read back all provider resources
-> activate compute and storage entitlements
-> record receipts
```

- Debit, provider mutation, claim, activation, refund, Secret write, renewal, and receipt each use stable operation-scoped identities.
- Operator wallet adjustment, `workspace.launch.v2` debit/refund, and Workspace
  renewal debit/refund share the single-replica process-local
  `lockResource("sub2api-wallet", accountId)` critical section. No second lock
  service is introduced; multi-replica execution remains forbidden until an
  approved distributed serialization boundary exists.
- One authenticated `POST /api/workspace-launches` stores a durable
  `workspace.launch.v2` RuntimeOperation. Current V2 recovery resumes the stable
  total-debit, pure Fabric fulfillment, activation, and receipt sub-operations
  after browser close or process restart through `succeeded` or `refunded`.
- Provider capacity and price preflight runs before the first charge attempt only.
  Recovery with either `ChargeAttempted` or `ChargeConfirmation` skips a new
  preflight and reconciles the stable charge identity first.
- The submission-time Sub2API total-balance read is a read-only preflight, not a hold or reservation. One
  Workspace operation performs one deterministic total debit; compute and storage are fulfillment-only phases.
- Financial proof requires `preBalanceUsdMicros > totalChargeUsdMicros` and
  `postBalanceUsdMicros == preBalanceUsdMicros - totalChargeUsdMicros`. Any
  mismatch enters `manual_review` with zero Fabric writes.
- Debit failure forbids every Tencent resource write.
- A confirmed provider result showing no billable resource exists permits exactly one idempotent refund.
- A partial or unknown provider result enters `manual_review` without refund or a second purchase.
- Immediately before activation, `SyncMonthlyCompute` and `SyncMonthlyStorage`
  must revalidate resource/account/Workspace identity, Zone, compute SKU, storage
  capacity, `PREPAID`, `NOTIFY_AND_MANUAL_RENEW`, and deadline. A mismatch remains
  in `manual_review` without activation.
- Dedicated `workspace.launch.v2` review recovery uses
  `POST /api/operator/workspace-launches/{operationId}/recover`. Reconciliation
  items require `accountId`, `billingOperationId`, `phase`, `errorCode`, and
  `allowedActions`; only `manual_review` exposes `recover_workspace_launch`.
  This dedicated recovery and DTO have integrated local fake evidence.
  Provider reconciliation uses internal
  `GET /fabric/monthly-provider-truth?computeAllocationId=<id>&storageVolumeId=<id>`
  only for `workspace.launch.v2` manual-review recovery and reuses the existing
  Tencent provisioner `provider_truth` Describe-only truth. If either Fabric local
  identity is missing, or provider identity, SKU, Zone, ownership, `PREPAID`,
  manual-renew, or deadline cannot be verified exactly, the result is `unknown`;
  it is never `absent` and never permits refund. The GET does not run Sync, Tag,
  kubectl apply, delete, label, purchase, renew, or destroy. It does not replace
  activation readback; activation still uses `SyncMonthlyCompute` and
  `SyncMonthlyStorage`.
  The recovery matrix resumes missing storage or attachment with the original
  identities, refunds exactly once only when both resources are confirmed absent,
  retries receipt-only phases, and leaves unsafe or unknown provider states in
  review.
- A Ledger failure after activation leaves the entitlement active and retries only its receipt.
- Replays never create a second debit, refund, purchase, renewal, Secret, or receipt.
- The non-review V2 path has local focused evidence from debit through pure Fabric
  fulfillment, activation, confirmed-absence refund, and receipt-only retry.
  Dedicated manual-review recovery has integrated local fake evidence.
  No real Sub2API, Tencent, Runtime, browser, or production evidence is claimed.

## Products And Lifecycle

- Workspace is the customer subscription and owns the canonical renewal intent,
  price snapshot, period, paid-through value, and renewal status. Compute and
  storage rows are provider and compatibility facts.
- Customer prices are fixed integer USD micros under
  `pilot-usd-2026-07-v1`; provider costs never derive a customer charge.
- Provider SKU may vary by approved environment but must satisfy the customer CPU and memory contract.
- One Workspace renewal uses one combined Sub2API debit, manual provider renewal
  of the same CVM/CBS, readback, entitlement extension, and one receipt.
- Tencent automatic renewal is forbidden.
- At unpaid expiry compute is stopped and access is denied. CBS is retained;
  expiry, release, QA, and rollback never delete it.
- Workspace file bodies live only on CBS. OPL PostgreSQL and Ledger never store
  them, and OPL provides no backup/recovery/sync/transfer guarantee for deleted
  or corrupted CBS data.

## Workspace Access And Secrets

- Workspace URLs are stable and require Runtime password login.
- A routing cookie selects a Runtime Service and is not an authentication credential.
- Ordinary Runtime status is non-secret and never returns a password or Kubernetes Secret reference.
- Only the signed-in user whose ID equals `Workspace.ownerUserId` may reveal or rotate the Runtime password. These responses are `private, no-store`; the password never enters Workspace persistence, RuntimeOperation, audit, logs, or Ledger.
- Runtime credential rotation reuses stable Fabric and Ledger idempotency identities. A credential revision changes the Runtime Secret and Pod template so Kubernetes rolls the Deployment without exposing the password or seed in metadata.
- Pilot Runtime isolation means only the owner receives the Runtime password. SSO and binding each Runtime HTTP request to the Console identity are not Pilot claims.
- Workspace access requires active compute and storage entitlements plus real runtime readiness.
- A Workspace release candidate is exactly one `one-person-lab-app` commit, one
  `opl-aion-shell` commit, and one `one-person-lab` Framework commit. Each input must be a full 40-character Git SHA already
  merged into its repository's `main`; branch names, short SHAs, and unmerged commits fail closed.
- The fixed release candidates are App `6b334ef7f239eb01c40578159e6df9ed2e7f97dc`,
  active shell `dbd9d68115604673df85033d7a0ab323d65a79a2`, and Framework
  `51d16f0e93aebf3fd5ccf96082490395fcbb8711`.
- The Cloud release `ref` is a full 40-character commit SHA. Its checked-out HEAD
  must match exactly and be an ancestor of the workflow repository's `main` readback.
  Branch names, short SHAs, and unmerged Cloud commits fail before publication.
- The release workflow checks out all three candidates detached, runs the App's existing
  `ensure:shell`, and builds the active shell Docker context directly into TCR.
- Production deploys only the TCR `repository@sha256` read back after publication;
  `latest` and tag-only production references are forbidden. The immutable TCR digest
  and Ready Pod `imageID` remain unavailable until their respective publication and
  deployment readbacks succeed; placeholders and local timestamps are not evidence.
- Ordinary Cloud deploy updates the immutable Workspace image default for new
  Fabric operations but does not restart or wait for existing Workspace
  Deployments while Runtime rollout is paused. Cloud rollback restores all
  prior ConfigMap data before restoring the three Cloud images.
- The current production PostgreSQL endpoint is internal and does not offer TLS,
  so the TKE ConfigMap sets `PGSSLMODE=disable`. A TLS-capable database migration
  must change this contract and its deployment evidence together. Application
  startup accepts this Pilot exception only when `PGSSLMODE=disable` is explicit
  and `DATABASE_URL` names one RFC1918 IPv4 literal; public, socket, empty,
  multiple, and non-literal hosts remain rejected. `sslmode=verify-full` remains
  the normal path.
- CBS is mounted at `/data` and `/projects`.
- Runtime remains the only possible authority for `/projects` file metadata and mounted filesystem usage, but those
  product APIs and their Console presentation are paused outside this release. Release persistence checks write and
  hash small markers directly in the Runtime Pod on `/data` and `/projects`; they do not claim metadata/statfs evidence.

## Console User Experience

- Authentication, lazy-route loading, and account-state loading have distinct timeout, error, and retry states.
- Public and login routes render immediately; a session check may enrich or redirect them but never gates their first interactive screen.
- The first authenticated screen answers live wallet status, Workspace
  usability, current server-projected price/period, AI actual spend, receipts,
  and actionable failures.
- Billing history is a tenant-scoped projection of Ledger receipts. Gateway request history and totals are tenant-scoped projections of live Sub2API usage APIs. Neither projection persists a second copy of the facts.
- Balance, entitlements, billing receipts, and Gateway usage load independently. One unavailable source cannot hold the whole Console in a spinner or erase facts from another source.
- The primary flow is one recoverable Workspace launch covering package,
  server-projected total price, debit, PREPAID resources, Gateway Secret,
  Runtime, and URL. Compute/storage are Workspace details, not separate buys.
- Workspace status polls every 10 seconds for at most 30 attempts, stops on ready or terminal state, and offers manual retry after a real error or timeout.
- Gateway fetches only when its page is opened, masks the Key by default, and
  follows a successful create with the existing owner-only reveal command so the
  browser can display/copy the real Key. Plaintext remains only in browser memory
  and is cleared on route leave, refresh, logout, or the existing timeout.
- A successful authoritative read with zero rows is `empty` and renders "暂无数据";
  an upstream failure is `unavailable` and renders "暂不可用" with retry. Empty
  Workspace, Runtime objects, Keys, Usage, receipts, and billing reviews are not
  service failures.
- Workspace answers URL, username, password reveal/copy, and the corresponding Workspace Key reveal/copy;
  Workspace Key reveal reuses the owned per-Key Gateway route.
- Control Plane owns the two-table minimal Pilot announcement and read state; it does not copy Sub2API notices.
- Desktop and mobile QA must prove responsive layout, keyboard access, error recovery, and no sensitive-information overlap or leakage.

The existing public Home, Login, and Logo/brand entry remain unchanged in Pilot V2.

## Evidence Levels

- `code-complete` requires current contracts, code, local PostgreSQL, browser, structure, and machine-checked
  zero-SKIP gates on one integration HEAD.
- `pilot-ready` additionally requires separately approved real Gateway, Runtime, Tencent, billing, and browser evidence.
- `production-proven` additionally requires the same immutable revision deployed and read back in production.

`sourceUpdatedAt` is returned only when the authoritative owner supplies it. Final Go gates parse `go test -json`
and fail on `Action=skip`; PostgreSQL suites set `OPL_POSTGRES_TESTS=1`, and a Control Plane zero-SKIP claim also
sets `OPL_CAPACITY_TESTS=1`.

## Verification Slot

Provider Acceptance owns two retained non-customer slots:

| Slot | Package | CVM | CBS | Provider billing |
| --- | --- | --- | ---: | --- |
| `verification-slot-basic-01` | Basic | `SA5.MEDIUM4` (`2c4g`) | 10GB | `PREPAID`, one month, manual renew |
| `verification-slot-pro-01` | Pro | `SA5.2XLARGE16` (`8c16g`) | 100GB | `PREPAID`, one month, manual renew |

- Lifetime purchase budget is one per slot. Read-only inventory runs first;
  multiple or ambiguous candidates stop without purchase.
- Provider Acceptance, Pro verification, and the fixed-slot production verifier
  are paused and do not gate ordinary Basic rollout. Their workflows remain
  separate from deploy and retain their explicit approval boundaries.
- The normal Console Basic canary is the only planned write-path validation for
  this rollout. It runs once after health/readiness and uses normal account,
  wallet, Key, launch, Fabric, Runtime, Usage, and Ledger paths.
- Paused verification code and fake tests are not production evidence.

## Launch Stages

| Stage | Business | Owners | Current state | Required output and evidence |
| --- | --- | --- | --- | --- |
| 1. Offer and identity | Show invited mapped owners Basic and Pro without the Acceptance SKUs. | Console, Gateway | Canonical `POST /api/operator/accounts` provisioning and the strict one-to-one mapped-owner graph have integrated local evidence; deployment and authenticated production identity readback remain pending. | Product contract, tenant tests, deployed account readback. |
| 2. Wallet and quote | Show live wallet and exact Workspace quote before side effects. | Console, Gateway | Granular Wallet/Key/Usage/Stats/history DTOs, fixed USD Basic/Pro quotes, and local Console integration are code-complete; live authenticated Sub2API evidence is pending. | Source-contract tests, quote tests, unavailable-state UI tests. |
| 3. Balance debit | Debit the exact monthly amount once before provider mutation. | Console, Gateway, Ledger | Durable one-submit launch, debit-first recovery, and replay are code-complete; deployed browser and live Sub2API evidence are pending. | Deterministic debit, balance check, replay/concurrency evidence. |
| 4. Prepaid fulfillment | Open one-month PREPAID CVM/CBS after debit. | Fabric, Console | PREPAID CVM/CBS request/readback and pure-fulfillment recovery behind one Workspace debit are code-complete in local tests; live Tencent evidence is pending. | Request shapes, provider readback, duplicate-purchase guard. |
| 5. Claim and activate | Activate only after every resource is owned and read back. | All four lanes | Non-review V2 claim, confirmed-absence one-refund convergence, activation, purchased/refunded Receipt paths, dedicated launch review recovery, and its reconciliation DTO have integrated local fake evidence; live evidence remains pending. | Claim identity, confirmed-absence refund, ambiguous-result review. |
| 6. Workspace access | Authenticate to a ready, persistent, account-keyed Workspace. | Fabric, Console, Ledger | V2 attachment, Secret, Runtime readiness gate, activation, receipt-only recovery, status, and credential flows have local focused evidence on the non-review path. Runtime metadata/statfs API and Console presentation are paused; immutable image, browser, WebSocket, model, direct mount-marker persistence, and deployed evidence remain pending. | Owner isolation, login, WebSocket 101, Secret rotation, credential revision, digest readback, and direct `/data`/`/projects` marker retention. |
| 7. Gateway usage | Reveal the owner Key, make a metered Workspace model request, and show its customer-safe cost and Token facts. | Gateway, Console, Ledger | Wallet, Key list, request Usage, Usage Stats, balance history, and integer-cost projections are code-complete and locally tested; a real model request and production readback remain pending. | Tenant isolation, model response, request usage and stats projection, integer `actual_cost`, no leakage. |
| 8. Renewal and recovery | Renew one Workspace period with deterministic recovery. | All four lanes | Workspace-level claim, combined debit, same-ID provider renewal/readback, expiry, refund/review, and receipt recovery are code-complete; enabling auto-renew and real renewal evidence are pending. | Isolated PostgreSQL concurrency, renewal replay, deadline readback, real approved renewal. |
| 9. Reusable verification | Prove releases without per-run Tencent purchase or deletion. | All four lanes | Provider Acceptance, Pro verification, and fixed-slot verification are paused and do not gate the Basic rollout. | Future separately approved retained-slot evidence. |
| 10. Production release | Declare ready from immutable artifacts, rollout, rollback, and real evidence. | All four lanes | Security, immutable imageID checks, ConfigMap-aware Cloud rollback, read-only TKE diagnostics, release tooling, Task 12 UI, Task 13A local gates, and the deployment identity cutover are code-complete locally; immutable publication, rollout, rollback, and runtime evidence remain pending. | Full local gates, immutable digests, rollout, rollback, source-truth QA, approved real evidence. |

## Delivery Phases

The six delivery phases are: contract and cleanup; Fabric PREPAID and Workspace; Gateway account projection; commercial plans and settlement; Console UX and release safety; integration, real verification, and rollout. These phases organize four sessions and do not replace the ten business launch stages.

## Completion Rule

A launch stage is complete only when its human and machine contracts are current, focused and full tests pass, merged CI passes, exact image digests are deployed, and the listed runtime evidence is recorded. Documentation or a green fake alone never proves production delivery.
