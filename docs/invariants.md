# Launch Invariants

This file is the mandatory human-readable launch contract for this implementation repository. The target product boundaries come from `https://github.com/gaofeng21cn/one-person-lab-cloud`; the revision reviewed for this freeze is `c349a41d860e706ed43a4090b9e75abb0b130971`.

The upstream repository owns product architecture. This repository owns its selected backend, exact prices, provider procurement, delivery state, and runtime evidence. A frozen target is not a readiness claim. Current gaps and required evidence are recorded here and in `packages/contracts/opl-cloud-launch-freeze-contract.json`.

## Product Surfaces And Owner Lanes

The five product surfaces are OPL Gateway, OPL Workspace, OPL Console, OPL Fabric, and OPL Ledger. Workspace is the product delivered by Fabric after it opens CVM and CBS and deploys the pinned `one-person-lab-app` image; it is not a fifth service repository.

The four implementation owner lanes are Console/Control Plane, Fabric, Gateway integration, and Ledger. Gateway integration is an adapter to the externally deployed Sub2API, never a second Gateway service.

## Pilot Scope

- Administrator-provisioned customer accounts are the only supported account
  entry; public registration is forbidden.
- Capacity evidence covers a 1000-provisioned-user data set. It does not claim
  1000 concurrent users, concurrent logins, concurrent provisioning operations,
  multiple Control Plane replicas, or HA.
- One Console User maps to one OPL Account and one Sub2API User/Wallet. Console
  and Sub2API email must match after `lower(trim(email))`.
- Organization and Membership rows are internal one-to-one compatibility
  records only. They do not authorize sharing or appear in customer DTOs.
- Operators manually pre-fund or adjust the Sub2API wallet through audited
  recharge, debit, and business-refund commands. There is no customer payment,
  top-up, or payment-order surface. Owners may manage general API Keys.
- Each account may own multiple Workspaces. Basic and Pro are the only Pilot
  packages, and every Workspace has an independent quote, launch operation,
  entitlement period, provider resource set, Workspace Key, Secret, Runtime,
  and Workspace Receipt.
- Every Workspace owns an independent customer-controlled `autoRenew` intent,
  defaulting to false. The Fabric NodePool hard-cut preserves the renewal
  primitives; Control Plane/UI enablement and monthly-worker changes are a
  separate implementation closure.
- Backup, recovery, sync, transfer, HA, public registration, and shared
  multi-user collaboration are not Pilot capabilities.

## Console

- Console calls only Control Plane product APIs.
- Sub2API authenticates customer credentials. Control Plane owns local Sessions,
  account mapping, quotes, monthly orchestration, entitlements, expiry, and
  operator review; it stores no second customer password truth.
- Administrators provision users through canonical `POST /api/operator/accounts`
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
- Before that first debit, the compute monthly preflight also requires a
  release-bound production-runner attestation. The authorized runner reads the
  live Tencent STS caller identity and binds it to an operator-audited digest of
  the deployed policy requiring `tag:TagResources` and
  `tag:ModifyResourcesTagValue`. Fabric re-reads STS and requires the exact
  attested identity and release. Missing, malformed, or drifted evidence fails
  before Kubernetes RBAC, capacity, debit, or provider mutation; the proof
  performs zero Tencent Tag writes.
- Basic and Pro use separate pre-created TKE NativeCVM NodePools. Basic's
  customer resource contract is `2c4g`; Pro's is `8c16g`. Each Tencent instance
  type is resolved by stable sorting of the current Zone's PREPAID, SELL, exact-
  shape candidates by monthly price and then instance type, and registered by
  bootstrap and production configuration. Empty `0/0` pools are valid templates, not idle-machine
  inventory. Each launch uses the exact NodePool ID and instance type persisted
  by preflight; label discovery fallback, per-launch SKU selection, and customer-
  path NodePool creation are forbidden.
- Fabric persists a FIFO admission queue per exact NodePool. A short PostgreSQL
  transaction lock orders admission only; no database connection is held during
  provider work. Only the persisted `started` head may prepare, scale, and
  bounded-poll. Once its one scale is confirmed and it enters the separately
  idempotent `claim_pending` continuation, it leaves the allocation queue without
  changing or retrying that claim; a later `started` operation therefore cannot
  be blocked indefinitely by an operator-owned claim review. A fenced short execution lease permits crash recovery without
  allowing a later Workspace to pass the head, while different NodePools run in
  parallel. Before Tencent mutation the head persists the current replica
  baseline, the absolute `N+1` target, and the complete before-machine set.
  Replay aligns that same target and claims only the unique Ready machine in
  `after - before`; it never allocates an old, idle, orphaned, or unregistered
  machine.
- Machine claim and Sync require Tencent to explicitly report `Ready` or
  `Running`; an empty or unknown state never defaults to running. Private-IP CVM
  resolution must return exactly one instance whose TKE Machine, `ins-*` CVM,
  VPC, and Subnet identities all match. Zero, multiple, incomplete, or
  inconsistent results fail closed.
- If the unique new NativeCVM was created but ownership claim was interrupted
  before any local storage operation started, Fabric exposes a separate zero-
  mutation Describe/get-only proof. It derives identity from the original launch,
  compute allocation, persisted allocation plan, and MachineOwnership; requires
  the unique Ready/Running Machine in `after - before`; verifies the exact
  NodePool/Machine/Node/private-IP/CVM/SKU/Zone and PREPAID one-month manual-renew
  facts; and accepts only an unallocated Node or the exact target ownership. A
  zero local storage-operation count is not proof that CBS is absent: the same
  proof pages through Tencent `DescribeDisks` using the four `opl_*` ownership
  tags and an exact DiskName fallback. It validates DiskName, Zone, size, data-
  disk usage, PREPAID one-month billing, DiskType, manual-renew flag, and
  deadline. Zero candidates reports `storage_not_started`; one exact candidate
  reports `storage_existing_exact` and its `disk-*`; multiple candidates,
  provider failure, or any tag/property drift reports unknown and remains
  manual review. All proof paths report zero Sub2API, Tencent, and Kubernetes
  mutations. The strict compute-plus-storage
  `MonthlyProviderTruth` contract remains unchanged.
- Compute claim convergence may run only after that complete proof and may only
  converge the same CVM name and four ownership tags, one exact Node
  labels/taint patch, and the same MachineOwnership on the proved CVM and Node.
  The original compute operation persists the launch ID, idempotency key,
  target hash, and request hash; a missing, malformed, or drifted existing
  binding fails closed. Exact-key, exact-target replay is idempotent with zero
  incremental external mutation. Sub2API mutations are always zero, Tencent
  mutations are bounded to zero through five, and Kubernetes mutations are
  bounded to zero or one. Ambiguity,
  conflicting ownership, provider/IAM/RBAC failure, or any existing storage
  operation fails closed before mutation and remains manual review.
- Recovery Plan diagnosis and validation use one safe schema with field-level
  mismatches represented by allowlisted values or SHA-256 digests. A validation
  artifact's `runnerDirectMutationCounts=0` means the GitHub runner performs no
  direct Sub2API, Tencent, or Kubernetes write. A later operator-confirmed
  Console execution is a separate Control Plane operation: it may continue only
  the original launch's persisted, bounded CBS, PV/PVC, Gateway Secret, Runtime,
  activation, and Receipt stages and proves terminal identities by authoritative
  readback instead of claiming those background writes are zero.
  Provider attempts come only from the proof counts and per-CVM/per-Node
  `attempted`, `confirmed`, `unknown`, and `missing` evidence. Success requires
  the evidence count to equal `attempted`, every attempt to be confirmed, zero
  unknowns, and no missing fields. A Go response may omit an empty `missing`
  array only for that fully confirmed shape. CVM `missing` accepts only
  `instance`, `instance_name`, and the four `opl_*` ownership tags; Node
  `missing` accepts only `node_ownership`.
- The zero-mutation Fabric ledger readback classifies the exact persisted
  compute binding as `current`, `compute-claim`, `known-legacy`, or `other` and
  exposes only that class plus a SHA-256 digest. It also projects the persisted
  CVM/Node attempt evidence, failure stage, and provider error class without a
  provider call. `known-legacy` requires the exact historical
  `recovery-exec-<20 lowercase hex>` request-hash generation, is
  classification-only, and never authorizes a binding takeover.
  `recoverable_cvm_only` requires a `current` or
  `compute-claim` binding, at least one fully confirmed CVM attempt with zero
  unknown or missing facts, and zero Node attempts; every other result requires
  one operator-compensation decision and no provider entry.
- The system NodePool `np-6l4nkdto`, Machine `np-6l4nkdto-2cdtm`, and Node
  `10.66.0.42` must each resolve uniquely and are protected from every
  Tencent/Kubernetes mutation and cleanup path. Its actual NodePool MachineType
  must be `NativeCVM`, `Native`, or `CXM`: `NativeCVM` must resolve exactly one
  `ins-*` through the Machine LanIP, while `Native` and `CXM` make CVM identity
  explicitly not applicable. Unknown, ambiguous, or configured/actual identity
  mismatches fail closed. The Basic and Pro pools must be distinct from each
  other and from the system pool.
- NodePool creation exists only in the manually approved bootstrap workflow.
  It inventories System/Basic/Pro first, creates only an unambiguously missing
  package pool at replicas 0, and preserves a successfully created pool when
  the other package fails so retry fills only the missing pool. The workflow
  reuses the existing `production` Tencent credentials and kubeconfig. Dry-run
  automatically reports the recommended Basic and Pro SKU and performs zero
  mutation. The production bootstrap configures independent Basic and Pro
  `maxReplicas` values of 50; these are explicit workflow configuration, not
  code defaults, and do not reserve or add capacity across pools. Before
  mutation, the workflow re-reads PREPAID quota, Subnet IP capacity, and the
  TKE cluster node limit for one immediate launch headroom, reports the complete
  sorted pre-mutation NodePool ID inventory, rejects every unknown pool, then
  verifies each selected SKU remains eligible. A customer launch repeats this instantaneous
  global TKE capacity check immediately before debit; it does not create a
  reservation or a second capacity ledger.
  The report, NodePool label, Native `InstanceTypes`, and production configuration
  must register the same value. Mutation also requires the exact confirmation
  `CREATE_MISSING_WORKSPACE_NODEPOOLS` and exact merged `origin/main` SHA.
- Fabric creates CBS with a stable `ClientToken`, reads back CVM/CBS identity and billing facts, then binds CBS through a static PV/PVC in the compute Zone.
- Normal compute fulfillment persists separate `compute_create` and
  `compute_claim` reservations before their Tencent/Kubernetes writes. Normal
  storage fulfillment likewise persists separate `cbs_create` and
  `static_binding_apply` reservations. Each stage permits at most one external
  write; after a reserved or unknown outcome, restart uses authoritative
  Describe/GET readback only and ambiguity enters manual review.
- Once the original CBS identity is confirmed, `CreateDisks` is permanently
  forbidden for that launch. Only the original static PV/PVC identity may be
  applied or read back. A paid active launch with pending storage is never
  timed out into PV/PVC deletion, retained replacement state, or a replacement
  CBS; it converges the original identity or enters manual review.
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
  including the reserved administrator for its own account, may manage general
  Keys. Every new Workspace converges exactly one reserved Key whose stable name
  is derived from `workspaceId`; the legacy `opl-workspace` name remains bound
  only to an existing legacy Workspace and is never reused for a new Workspace.
- Required read capabilities are mapped-user balance, available groups, the mapped user's paginated/filterable/sortable Key list, paginated request usage, and aggregate usage stats. Key creation requires a live Sub2API group. Request usage and stats are scoped by both `user_id` and the selected `api_key_id`; every returned identity is validated again by Control Plane. Request-list `today`, `week`, and `month` are real `Asia/Shanghai` calendar ranges sent upstream as `start_date`, `end_date`, and `timezone`; week starts on Monday and month starts on the first calendar day.
- For Keys, UserKeys, Usage, and BalanceHistory, a zero-row Sub2API v0.1.162
  response is valid only as `total=0,page=1,pages=1,items=[]`; every other empty
  pagination shape fails closed.
- Spendable balance and non-negative request `actual_cost` values are converted once with `floor(rawDecimalUSD * 1_000_000)` to conservative integer USD micros; malformed, negative, non-finite, or overflowing values are unavailable rather than fabricated. Batch user and Key usage preserves every valid requested item, leaves a missing or malformed requested item unavailable, and fails the whole batch on any extra unrequested identity. Every unavailable source envelope includes a stable source-derived `reasonCode` and never exposes raw upstream errors.
- Request latency uses live Sub2API `first_token_ms` and `duration_ms` only. Both
  values are nullable non-negative integers, are never persisted by OPL, and
  render as `-` rather than `0 ms` when the upstream value is absent; Console
  never derives latency from browser timing, timestamps, Token counts, or
  response arrival.
- Control Plane decodes a strict customer-safe DTO allowlist. Raw Sub2API admin responses, nested raw Keys, upstream account internals, prompts, and response content never reach Console, OPL PostgreSQL, Ledger, logs, or caches.
- Key DTO fields `quota_used`, `usage_5h`, `usage_1d`, `usage_7d`, and `last_used_at` remain quota and recent-window signals; they do not replace request-level usage and aggregate stats.
- General Key management may project and mutate Sub2API's group, quota, IP allow/deny lists, expiration, 5h/1d/7d limits, and supported reset commands. Control Plane persists none of those facts. Operator Key counts are live, bounded-concurrency reads for only the current account page and are never stored.
- The owner may request an owned Key only through audited
  `POST /api/gateway/keys/{keyId}/reveal`. It is masked by default and
  never enters `/api/state`, browser storage, OPL PostgreSQL, Ledger, logs,
  caches, or operation payloads. The retired Gateway summary route is a 404.
- Kubernetes Secret is the only authorized Key persistence point. Every new
  Secret write is deterministically scoped by `accountId`, `workspaceId`,
  `workspaceApiKeyId`, and Key fingerprint; Workspace runtime receives only its
  reference. Existing account-scoped Secrets remain readable without automatic
  Runtime restart and migrate one way on that Workspace's first Key rotation.
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
- Normal Basic and Pro launch use that same single POST and one shared
  `workspace.launch.v2` orchestrator. A replay with the same account, owner,
  package, and request hash resumes the original `key_pending` operation and
  reserved Workspace Key while preserving its original idempotency key; any identity drift returns conflict
  before a second launch or Key. Raw credentials are never persisted.
- The Workspace image is an immutable `repository@sha256:<64 lowercase hex>`
  value. Missing, tag-only, malformed, or changed image identity fails closed
  before launch persistence, debit, or any provider write, with all mutation
  counts zero.
- Provider capacity and price preflight runs before the first charge attempt only.
  Recovery with either `ChargeAttempted` or `ChargeConfirmation` skips a new
  preflight and reconciles the stable charge identity first.
- The submission-time Sub2API total-balance read is a read-only preflight, not a hold or reservation. One
  Workspace operation performs one deterministic total debit; compute and storage are fulfillment-only phases.
- The unique matching Redeem Code history entry is the authority that confirms
  the monthly debit. Balance snapshots remain preflight/projection facts;
  concurrent legitimate Usage may change the balance and must not turn a
  confirmed debit into manual review solely because an exact balance delta is
  unavailable. The monthly debit still has cardinality one.
- At the first authoritative debit confirmation, Control Plane freezes
  `periodStart`, `paidThrough`, and `billingAnchorDay` in the launch operation.
  Replays reuse those persisted values and never recalculate them.
- Debit failure forbids every Tencent resource write.
- A confirmed provider result showing no billable resource exists permits exactly one idempotent refund.
- A partial or unknown provider result enters `manual_review` without refund or a second purchase.
- A transient tag, label, taint, or strict compute Sync failure after creation
  persists `compute_claim_pending`. The same launch may perform only claim-only
  convergence for its original compute identity; it never repeats preflight,
  debit, compute prepare, scale, or procurement. Only successful strict claim
  readback advances the original launch to `storage_fulfilling`, where the
  original storage operation identity still permits at most one CBS create.
  Unprovable or conflicting state remains `manual_review` without refund or a
  replacement CVM.
- Legacy `workspace.launch.v2` rows in `manual_review/compute_fulfilling` are
  read-only diagnosis candidates. Only a confirmed debit, the persisted launch
  identity, a complete identity proved from the matching Fabric allocation,
  allocation plan, and MachineOwnership, zero storage-create operations, and a
  successful compute-only proof permit a PostgreSQL CAS that persists the proof
  identity and enters `compute_claim_pending`; that normalization performs zero
  Sub2API, Tencent, and Kubernetes mutations.
- Immediately before activation, and again before opening the Workspace URL,
  Control Plane calls `POST /fabric/workspace-activation-truth`. Despite using
  POST for a structured proof request, the endpoint is Describe/GET-only and its
  Sub2API, Tencent, and Kubernetes mutation counts are all 0. It freshly proves
  compute ownership, the unique CBS/PV/PVC and original Attachment identity,
  Gateway Secret identity, exactly one Runtime, one Ready Pod on the claimed
  Node, exact Service/Endpoints routing, and the Workspace NetworkPolicy.
  Zero or multiple candidates, identity drift, or classified Kubernetes read
  errors fail closed; activation enters `manual_review`, and URL access is denied.
- Dedicated `workspace.launch.v2` review recovery uses the Console flow
  `diagnose -> view persisted Recovery Plan -> validate -> confirm continue`.
  The operator supplies only `accountId`, the original launch operation ID, and
  the decision; Console execution submits only `planId`, `planDigest`, decision,
  and the fixed confirmation. Control Plane alone reads resource identities,
  release SHA, Cloud and Workspace digests, generates the approval digest, and
  persists the execution/run identity and fenced lease. The former `/recover`,
  `/readback-recovery-proof`, and `/compute-claim-recovery/*` public routes are
  404. Only `manual_review` is eligible for this operator flow.
  Provider reconciliation uses internal
  `GET /fabric/monthly-provider-truth?computeAllocationId=<id>&storageVolumeId=<id>`
  only for `workspace.launch.v2` manual-review recovery and reuses the existing
  Tencent provisioner `provider_truth` Describe-only truth. If either Fabric local
  identity is missing, or provider identity, SKU, Zone, ownership, `PREPAID`,
  manual-renew, or deadline cannot be verified exactly, the result is `unknown`;
  it is never `absent` and never permits refund. The GET does not run Sync, Tag,
  kubectl apply, delete, label, purchase, renew, or destroy. It is distinct from
  the fresh Workspace ActivationTruth used by activation and URL access.
  The recovery matrix resumes missing storage or attachment with the original
  identities, refunds exactly once only when both resources are confirmed absent,
  retries receipt-only phases, and leaves unsafe or unknown provider states in
  review.
- A Ledger failure after activation leaves the entitlement active and retries only its receipt.
- The original `workspace.launch.v2` operation persists one attempt budget for
  each of `storage`, `attachment`, `secret`, `runtime`, `activation`, and
  `receipt`. Each stage has `max=1` and records `attempted`, `confirmed`, and
  `unknown`. A PostgreSQL CAS reserves the attempt before the external write;
  restart reloads the remaining budget from the same launch result. Unknown or
  exhausted outcomes enter `manual_review`, and the active worker never writes
  that stage again.
- Replays never create a second debit, refund, purchase, renewal, Secret, or receipt.
- The non-review V2 path has local focused evidence from debit through pure Fabric
  fulfillment, activation, confirmed-absence refund, and receipt-only retry.
  Server-authoritative Recovery Plan handling has local focused evidence only.
  No real Sub2API, Tencent, Runtime, browser, or production evidence is claimed.

## Products And Lifecycle

- Workspace is the customer subscription and owns the canonical renewal intent,
  price snapshot, period, paid-through value, and renewal status. Compute and
  storage rows are provider and compatibility facts.
- Customer prices are fixed integer USD micros under
  `pilot-usd-2026-07-v1`; provider costs never derive a customer charge.
- Provider SKU may vary by approved environment but must satisfy the customer CPU and memory contract.
- At renewal evaluation, `autoRenew=false` performs no debit and no Fabric
  renewal call. `autoRenew=true` performs one Workspace commercial operation:
  read the Workspace intent and `paidThrough`, run wallet and compute/storage
  read-only preflight, debit the combined monthly price once, renew the same CVM
  and CBS, read back both deadlines from Fabric, extend `paidThrough`, and append
  one `billing.workspace_renewed.v1` Receipt.
- OPL-controlled renewal never enables Tencent automatic renewal. CVM and CBS
  remain `PREPAID`, one month, and `NOTIFY_AND_MANUAL_RENEW`; Fabric retains
  idempotent `RenewComputeAllocation` and `RenewStorageVolume` readback paths.
- Insufficient balance, partial provider success, or an unknown provider result
  enters `manual_review` without a duplicate debit, renewal, or replacement
  purchase. Once `paidThrough` is reached, access is denied even while review is
  unresolved.
- At unpaid expiry Workspace access is denied and renewal intent is disabled.
  OPL does not stop, destroy, delete, renew, or otherwise mutate CVM/CBS; Tencent
  expiry policy owns eventual provider reclamation. The expiry receipt records
  `providerAction=none_expire_by_provider`.
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
- Workspace access requires a current Control Plane Workspace entitlement and
  live Fabric readback for Compute, Storage, Attachment, and Runtime. A Fabric
  timeout or unavailable item fails closed; Control Plane provider-state copies
  are references and never substitute for live provider truth.
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
- Every self-hosted checkout in the production rollout and Basic customer-operation
  workflows uses a fresh `run_id/run_attempt/job` source directory, runs repository
  commands only from that directory, and removes it only when this job created it.
  Before any production write in those flows, the job requires
  `GITHUB_REF=refs/heads/main` and proves `GITHUB_SHA`, checkout HEAD, and the only
  remote head `refs/heads/main` are the same commit. The workflows never delete
  branches or repair persistent runner state.
- Ordinary Cloud deploy updates the immutable Workspace image default for new
  Fabric operations but does not restart or wait for existing Workspace
  Deployments while Runtime rollout is paused. A separate explicit
  `PROMOTE_WORKSPACE_IMAGE` main-only workflow may CAS-promote that ConfigMap
  default from an exact old digest to an immutable TCR digest, with a rollback
  snapshot and no Cloud rollout, Workspace restart, or Tencent/provider write.
  Cloud rollback restores all prior ConfigMap data before restoring the three
  Cloud images.
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

These paused non-customer slot SKUs do not define the customer Basic resource
contract or select the SKU for a customer launch.

- Lifetime purchase budget is one per slot. Read-only inventory runs first;
  multiple or ambiguous candidates stop without purchase.
- Provider Acceptance, Pro verification, and the fixed-slot production verifier
  are paused and do not gate ordinary Basic rollout. Their workflows remain
  separate from deploy and retain their explicit approval boundaries.
- The normal Console Basic canary is the only planned write-path validation for
  this rollout. It runs once after health/readiness and uses normal account,
  wallet, Key, launch, Fabric, Runtime, Usage, and Ledger paths.
- Ordinary rollout and its verifiers remain read-only and perform zero customer,
  wallet, model, Workspace, Tencent, or Kubernetes business mutation. The Basic
  customer operation is a separate manual workflow and is never called by CI,
  release, ordinary rollout, or E2E.
- The canary is a manual release-owner invocation of the existing
  `production-live-qa` runner, not CI, rollout, E2E, or a public test API. It
  runs as one concurrency-locked workflow: the self-hosted TKE VPC job owns
  revision, Fabric, Kubernetes, account, wallet, and launch evidence without a
  browser; the dependent `ubuntu-latest` job re-reads public authority and owns
  the Workspace browser, WebSocket, and single model request without kubeconfig
  or Tencent credentials. Their same-run handoff is redacted evidence, never a
  substitute for account, wallet, launch, Usage, or Ledger authority.
  `operator_precharge` retains the explicit account-provision, wallet-recharge,
  Workspace-purchase, and model-request approvals. A narrowly approved
  `operator_precharge_recovery` may continue the same E2E only after a completed
  historical precharge: it reads the exact non-secret approved wallet-adjustment
  operation, validates its mapped active account, recharge reason, status,
  phase, and exact USD-micros delta, and performs zero account-provision or
  wallet-adjustment POSTs. It never derives or recovers a raw wallet idempotency
  key. Recovery requires an empty `resume_run_id`; it never downloads or uses an
  earlier checkpoint. Its approval binds one new launch idempotency key plus the deterministic
  launch-operation and Workspace IDs; every first submission or later recovery
  reads only those public identities and never submits a second launch. Both
  modes read the current server quote and live spendable wallet before their
  first launch POST, then prove the exact Control Plane debit, receipt, and one
  Workspace resource chain. The runner submits exactly one launch POST, polls
  only that operation, proves separate recharge/product/Usage evidence, the
  approved resolved SKU across the NodePool plan, Fabric allocation, Tencent
  truth and operator facts, the Basic `2c4g` catalog and Runtime limits, and the
  dedicated-pool `N -> N+1` resource chain, and emits only redacted evidence.
  Before every business write, the runner revalidates the approved
  merged SHA (including a live `origin/main` read) and Cloud digest against the
  current Control Plane, Fabric, and Ledger Deployment revision -> ReplicaSet -> Ready Pod owner chain; a boolean
  readiness response is not accepted as this gate. Its same-run atomic checkpoint is only
  a recovery hint: deterministic account, wallet-operation, launch-operation,
  and Workspace identities are recovered from authoritative service readback,
  unknown historical HTTP attempts remain null, and an attempted or otherwise
  unprovable model result is never sent again.
- Paused verification code and fake tests are not production evidence.
- An ordinary `workspace.launch.v2` in `compute_claim_pending` is owned by the
  original active launch worker. It reuses only the original
  `operationId:compute` Fabric claim identity and does not require an operator
  approval or recovery key. `manual_review` is excluded from this worker and
  remains operator-recovery-only. Before any continuation, the worker performs
  fresh authoritative CVM and Node readback, verifies `kubectl auth can-i patch
  nodes`, reads the Tencent STS identity, and proves the deployed role has
  `tag:TagResources` and `tag:ModifyResourcesTagValue`; these preflights perform
  zero Tencent Tag writes. A PostgreSQL CAS selects one winner. The winner may
  perform zero Tencent writes and at most one exact Node patch, followed by at
  most six read-only Node observations. It never reruns monthly preflight,
  Sub2API debit, NodePool scale, CVM creation, rename, or tag writes. Exact
  target-owned readback continues the same launch through storage, Runtime,
  activation, and Receipt; unprovable state enters `manual_review`.
- The production customer-operation workflow may only ask Control Plane to
  diagnose and zero-mutation validate its persisted Recovery Plan; it cannot
  execute recovery or rebuild a plan from workflow inputs. The one real recovery
  starts from an authenticated reserved operator's Console confirmation. Control
  Plane then generates an approval digest bound to the
  exact merged main SHA, Cloud and Workspace image digests, expiry, customer,
  launch/account/Workspace/compute/Machine/Node/CVM/Pool/SKU facts, original
  storage/attachment/runtime operation identities, approved storage state and
  exact provider disk identity when present, Workspace Key, recovery key, and
  per-stage attempt limits. It approves only convergence of the existing
  CVM/Node followed by the original launch's one CBS, PV/PVC attachment, Gateway
  Secret, Runtime, activation, and purchase Receipt. It forbids a new launch,
  debit, recharge, refund, scale, new CVM, second CBS, delete, or replacement.
  Node name, private IP, provider resources, and release digests are read from
  authorities and cannot be supplied by Console. The operator Session and CSRF
  authorize confirmation; the validated persisted plan, server-generated
  approval digest, execution ID, run ID, and byte-exact current lease token gate
  the mutation. The approval ID and HTTP mutation idempotency key are part of
  the persisted replay identity. The persisted plan carries
  `proof.storageState` and `proof.storageProviderResourceId` through its binding
  digest. The original launch
  GET response projects only the persisted approval ID, approval digest,
  recovery key, and Workspace image digest; the continuation artifact carries
  that exact readback for the later E2E handoff. Customer email, the full
  approval, Gateway Secret references, credentials, and runner capabilities are
  never included. A successful diagnosis never authorizes claim by itself.
  Before the claim provider can write, Fabric CAS-reserves the bounded Tencent
  and Kubernetes mutation budget in the original compute operation. A legacy
  binding without this ledger may reserve once after the same exact read-only
  proof; once reserved, every same-key retry is readback-only. A missing provider
  outcome remains conservatively unknown at the full bound and never authorizes
  another external write. One narrower repair continuation is allowed only when
  the observed failure is a CVM ownership-tag readback with zero unknown writes
  and zero Kubernetes attempts, and a fresh authoritative proof shows that exact
  CVM is now target-owned while the exact Node remains unallocated. Fabric keeps
  the original `operationId:compute` claim identity, reconciles the old ledger
  without any binding takeover, and allows the one remaining Node patch. The
  recovery approval and recovery key remain Control Plane authorization and audit
  facts only; Control Plane calls Fabric with the original claim identity. Every
  other observed failure or identity drift remains readback-only. A newly
  submitted approval must be unexpired, while a byte-exact approval already
  persisted on the launch may replay after expiry. Lease takeover keeps the same
  execution/run identity, and a stale holder cannot finalize after its token has
  been fenced out.
- Production closure requires two independent evidence sets and neither may
  substitute for the other. Acceptance A restores the one exact existing Launch
  with zero additional debit, CVM creation, or Tencent Tag write, at most one Node
  patch, and CBS creation only when storage is authoritatively not started; it
  must end with Launch succeeded, Runtime Ready, completed Receipt, the approved
  Workspace Pod image digest, and Workspace URL HTTP 200. Acceptance B submits
  one independent fresh Basic order exactly once and proves exactly one debit,
  CVM, Node claim, CBS, Runtime, and Receipt plus the same terminal URL and image
  evidence. Deployment and every private-network readback run only through the
  repository GitHub Actions `production` environment and its authorized runner.
- `recovered_workspace_e2e` is a separate hosted job in the same workflow and
  has a one-way dependency on the persisted completed Recovery Plan execution
  plus authoritative Workspace and Receipt readback. It has no
  kubeconfig, Tencent credentials, or internal service capability, and cannot
  launch, debit, recharge, refund, scale, or mutate Fabric resources. A separate
  `confirm_single_model_request` approval binds the exact release, customer,
  launch, Workspace, compute, storage, attachment, Runtime, Receipt, Workspace
  Key, model, and request key. Before sending, Control Plane persists a
  create-only `attempted` marker; any existing marker or unknown result forbids
  resending forever. Only the same binding may CAS the marker to `passed` after
  password login, WebSocket, exactly one model response, exactly one Usage, and
  the matching balance delta are proved. This E2E cannot modify or block the
  resource-delivery state machine.
- An original `workspace.launch.v2` that entered `manual_review` with exactly
  one continuation stage persisted as
  `attempted=1, confirmed=0, unknown=1, max=1` may be recovered only through
  the persisted Control Plane Recovery Plan after operator Console confirmation,
  then through the shared `fulfillWorkspaceLaunch` orchestrator used by normal
  Basic, normal Pro, and compute-claim continuation.
  Its GET proof persists nothing, performs zero PostgreSQL writes, and performs
  zero Sub2API, Tencent, or Kubernetes mutation. It revalidates the customer,
  original launch and Workspace, full Basic/Pro product truth, compute and
  storage through `MonthlyProviderTruth`, the exact active MachineOwnership,
  and distinct launch idempotency, Fabric internal operation, provider
  `opl_operation_id`, and stage resource operation identities. It also requires
  the stage-specific authority: storage,
  attachment, Gateway Secret, Runtime, `WorkspaceActivationTruth`, or Ledger
  Receipt. For Attachment, Gateway Secret, and Runtime, the authenticated Fabric
  proof POST is a structured GET/Describe-only operation with zero Fabric
  operation, PostgreSQL, Sub2API, Tencent, or Kubernetes mutation. One unique
  and identity-exact proof may first CAS the matching Fabric operation from
  `started/failed` to `succeeded`, then CAS the original Control Plane launch to
  `attempted=1, confirmed=1, unknown=0, max=1`. Each CAS has a maximum of one
  winner. Storage, activation, and Receipt use their existing authoritative
  readback followed by only the original launch CAS. The unknown stage's
  external write is never reissued. A concurrent loser, absent or multiple
  candidate, identity drift, or any read error leaves the launch in
  `manual_review` with zero additional external write. After successful CAS
  convergence, later original launch stages still reserve and consume their own
  persisted `max=1` budgets through the shared orchestrator.
- The legacy `workspace_launch_readback_diagnose` and
  `workspace_launch_readback_recover` workflow modes are retired. Readback is an
  internal Recovery Plan authority, and real continuation requires a persisted
  server-generated approval bound to
  the exact merged main SHA, Cloud and Workspace digests, expiry, protected
  customer identity, the complete protected product/CVM/Node target without
  projection, launch/Workspace and all resource and original operation
  identities, unknown stage, original attempt budget, recovery key, and the
  stage-specific remaining write set. It explicitly forbids a new launch,
  debit, recharge, refund, scale, CVM, second CBS, deletion, replacement, or
  retry of the unknown external write. The GET proof's and runner-direct
  Sub2API, Tencent, and Kubernetes mutation counts are each zero. These scoped
  counts do not describe the whole recovery: the existing launch worker's
  bounded background mutation counts are reported separately as `unknown` until
  terminal authoritative readback proves Receipt and URL.
- A Recovery Plan execution that already persisted the exact approval, approval
  digest, idempotency key, full target, and proof may be replayed from
  `preparing`, `waiting`, or terminal state even after that approval expires.
  This replay reconstructs the operator response exclusively from persisted
  state, does not require the former manual-review proof to remain available,
  and performs zero database, Fabric, Sub2API, Tencent, or Kubernetes writes.
  An expired approval that was never persisted is rejected, and any key,
  digest, or target drift returns conflict without mutation.
- A nonterminal Recovery execution whose lease token and expiry are both empty
  may reacquire a fresh fenced lease only for the same persisted execution and
  run identity. A partial lease or malformed expiry fails closed. Once the
  original launch worker releases that lease, its PostgreSQL launch CAS also
  synchronizes `succeeded` to completed Plan/Execution with URL and Receipt, or
  `manual_review` to the matching failed terminal projection. Transient CBS or
  Runtime readback after a confirmed write remains retryable and readback-only:
  persisted stage budgets never reset and no second CBS, Runtime, Secret,
  debit, CVM, Tag, or Node write is issued.
- If an exact active Fabric MachineOwnership and current compute binding are
  preserved while authoritative provider truth proves the same CVM is
  target-owned and its unique Node is still unallocated, Fabric may reserve the
  existing node-only mutation ledger under the original launch lock. This path
  performs zero Tencent writes, permits at most one CAS-bound Kubernetes Node
  patch, and then requires target-owned provider readback. Any competing owner,
  malformed binding, existing unknown ledger outcome, or stale readback remains
  `manual_review` without another patch.
- A terminal failed Recovery Plan may produce a successor only when Control
  Plane has explicit persisted `confirmed_zero` mutation evidence, or fresh
  authoritative Fabric evidence proves the original compute mutation ledger is
  absent or observed with complete confirmed-zero evidence. Fabric projects the
  ledger outcome plus a SHA-256 digest; Control Plane persists that digest with
  the archived execution and never infers zero from a missing, reserved,
  incomplete, invalid, or positive ledger. The predecessor Plan, Execution, approval identity, error, and
  mutation outcome remain immutable history. The successor has a new
  generation-bound PlanID/PlanDigest and requires fresh validation,
  confirmation, approval, execution, and run identities. Nonzero or
  unprovable outcomes always replay the failed execution without provider entry.
- A terminal failed Diagnose response may include a non-persisted
  `successorGate` projection containing only fixed booleans and enum states for
  Plan terminality, Execution failure/completion, lease release, Plan identity,
  persisted mutation outcome, and Fabric mutation-ledger evidence. It never
  includes approval material, lease values, resource identity, private IP,
  provider request identity, or a mutation-ledger digest. The production runner
  validates this exact allowlist even when Diagnose remains blocked, uploads the
  redacted evidence, and still fails the operation without retrying or mutating.
- Recovery Plan diagnosis, validation, blocked, and continuation projections
  use explicit allowlist DTOs. Complete proof and approval data remain inside
  Control Plane persistence and are never uploaded. The workflow only receives
  the safe plan projection and may upload that projection after validation.
  Recovery and the minimal recovered-Workspace E2E handoff are bound by the approval digest
  and a stable digest of the complete resource and operation identity, without
  exposing customer email, private IP, Secret facts, credentials, capabilities,
  provider request IDs, or complete operation identities.

## Launch Stages

| Stage | Business | Owners | Current state | Required output and evidence |
| --- | --- | --- | --- | --- |
| 1. Offer and identity | Show operator-provisioned mapped owners Basic and Pro without the Acceptance SKUs. | Console, Gateway | Canonical `POST /api/operator/accounts` provisioning and the strict one-to-one mapped-owner graph have integrated local evidence; deployment and authenticated production identity readback remain pending. | Product contract, tenant tests, deployed account readback. |
| 2. Wallet and quote | Show live wallet and exact Workspace quote before side effects. | Console, Gateway | Granular Wallet/Key/Usage/Stats/history DTOs, fixed USD Basic/Pro quotes, and local Console integration are code-complete; live authenticated Sub2API evidence is pending. | Source-contract tests, quote tests, unavailable-state UI tests. |
| 3. Balance debit | Debit the exact monthly amount once before provider mutation. | Console, Gateway, Ledger | Durable one-submit launch, debit-first recovery, and replay are code-complete; deployed browser and live Sub2API evidence are pending. | Deterministic debit, balance check, replay/concurrency evidence. |
| 4. Prepaid fulfillment | Scale the exact package NodePool from N to N+1 and open one independent CBS only after debit. | Fabric, Console | Allocation-scoped procurement, absolute-target replay, unique new-machine claim, bootstrap, and system-resource guards have local evidence; real NodePool creation and Tencent procurement remain pending. | Request shape, before/after machine proof, provider readback, duplicate-purchase protection. |
| 5. Claim and activate | Activate only after every resource is owned and read back. | All four lanes | Non-review V2 claim, confirmed-absence one-refund convergence, activation, purchased/refunded Receipt paths, dedicated launch review recovery, and its reconciliation DTO have integrated local fake evidence; live evidence remains pending. | Claim identity, confirmed-absence refund, ambiguous-result review. |
| 6. Workspace access | Authenticate to a ready, persistent, account-keyed Workspace. | Fabric, Console, Ledger | V2 attachment, Secret, Runtime readiness gate, activation, receipt-only recovery, status, and credential flows have local focused evidence on the non-review path. Runtime metadata/statfs API and Console presentation are paused; immutable image, browser, WebSocket, model, direct mount-marker persistence, and deployed evidence remain pending. | Owner isolation, login, WebSocket 101, Secret rotation, credential revision, digest readback, and direct `/data`/`/projects` marker retention. |
| 7. Gateway usage | Reveal the owner Key, make a metered Workspace model request, and show its customer-safe cost and Token facts. | Gateway, Console, Ledger | Wallet, Key list, request Usage, Usage Stats, balance history, and integer-cost projections are code-complete and locally tested; a real model request and production readback remain pending. | Tenant isolation, model response, request usage and stats projection, integer `actual_cost`, no leakage. |
| 8. Renewal and recovery | Renew one Workspace period only when its customer-controlled autoRenew intent is true. | All four lanes | Same-CVM/CBS Fabric renewal and readback primitives remain locally tested. Control Plane/UI intent enablement, worker selection, and real renewal evidence are outside the Fabric NodePool hard-cut. | Isolated PostgreSQL concurrency, zero calls when false, one combined debit when true, renewal replay, deadline readback, real approved renewal. |
| 9. Reusable verification | Prove releases without per-run Tencent purchase or deletion. | All four lanes | Provider Acceptance, Pro verification, and fixed-slot verification are paused and do not gate the Basic rollout. | Future separately approved retained-slot evidence. |
| 10. Production release | Declare ready from immutable artifacts, rollout, rollback, and real evidence. | All four lanes | Security, immutable imageID checks, ConfigMap-aware Cloud rollback, read-only TKE diagnostics, release tooling, Console browser coverage, local integration gates, and the deployment identity cutover are code-complete locally; immutable publication, rollout, rollback, and runtime evidence remain pending. | Full local gates, immutable digests, rollout, rollback, source-truth QA, approved real evidence. |

## Completion Rule

A launch stage is complete only when its human and machine contracts are current, focused and full tests pass, merged CI passes, exact image digests are deployed, and the listed runtime evidence is recorded. Documentation or a green fake alone never proves production delivery.
