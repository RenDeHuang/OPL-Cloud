# Launch Invariants

This file is the mandatory human-readable launch contract for this implementation repository. The target product boundaries come from `https://github.com/gaofeng21cn/one-person-lab-cloud`; the revision reviewed for this freeze is `c349a41d860e706ed43a4090b9e75abb0b130971`.

The upstream repository owns product architecture. This repository owns its selected backend, exact prices, provider procurement, delivery state, and runtime evidence. A frozen target is not a readiness claim. Current gaps and required evidence are recorded here and in `packages/contracts/opl-cloud-launch-freeze-contract.json`.

## Product Surfaces And Owner Lanes

The five product surfaces are OPL Gateway, OPL Workspace, OPL Console, OPL Fabric, and OPL Ledger. Workspace is the product delivered by Fabric after it opens CVM and CBS and deploys the pinned `one-person-lab-app` image; it is not a fifth service repository.

The four implementation owner lanes are Console/Control Plane, Fabric, Gateway integration, and Ledger. Gateway integration is an adapter to the externally deployed Sub2API, never a second Gateway service.

## Console

- Console calls only Control Plane product APIs.
- Control Plane owns account-to-Sub2API mapping, quotes, monthly orchestration, entitlements, renewal, expiry, and operator review.
- Pilot customer identities are provisioned and mapped manually. Operators can reset a customer's password and revoke existing sessions; self-registration and SSO are not Pilot claims.
- Console displays live Sub2API balance, Key metadata, request usage, usage stats, and Ledger billing receipts without creating a wallet, Key database, usage database, or billing fact table.
- Basic compute is `2c4g` for CNY 350/month; its default 10GB storage is billed separately at CNY 18/month.
- Pro compute is `8c16g` for CNY 1,500/month; its default 100GB storage is billed separately at CNY 180/month.
- Basic and Pro are target-saleable products. A production catalog entry becomes available only with matching pricing, PREPAID provider behavior, idempotency tests, and runtime evidence.
- The internal Verification Slot is not a customer product and never appears in catalog or quote paths.

## Fabric

- Fabric is the only Tencent Cloud and Kubernetes writer.
- Customer and verification CVM/CBS procurement uses `PREPAID`, period 1 month, and `NOTIFY_AND_MANUAL_RENEW`.
- `POSTPAID_BY_HOUR` is forbidden for customer and verification CVM/CBS resources.
- Capacity and price preflight is read-only and happens before debit. It cannot buy, reserve, renew, or delete a Tencent resource.
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
- API keys, passwords, raw tokens, provider secrets, and raw Sub2API responses are forbidden in evidence.
- A missing receipt retries only the receipt and never repeats debit, refund, provider purchase, Secret write, or renewal.

## Gateway

- OPL Gateway uses the externally deployed Sub2API backend. Compatibility is gated by required API capabilities; the reported version is diagnostic metadata and never blocks an otherwise compatible deployment. Sub2API code, image, container, database, configuration, and deployment remain immutable from this repository.
- Sub2API is the only owner of spendable USD balance, API keys, model routing, and request usage.
- Control Plane maps the signed-in account through `sub2apiUserId` and selects exactly one active Key named `opl-workspace`; zero or multiple matches fail closed.
- Required read capabilities are mapped-user balance, the mapped user's paginated Key list, paginated request usage, and aggregate usage stats. Request usage and stats are scoped by both `user_id` and the selected `api_key_id`; every returned identity is validated again by Control Plane.
- Request charges use Sub2API `actual_cost`, converted once to integer USD micros. Control Plane returns an explicit unavailable state for a missing capability or upstream failure and never substitutes zero.
- Control Plane decodes a strict customer-safe DTO allowlist. Raw Sub2API admin responses, nested raw Keys, upstream account internals, prompts, and response content never reach Console, OPL PostgreSQL, Ledger, logs, or caches.
- Key DTO fields `quota_used`, `usage_5h`, `usage_1d`, `usage_7d`, and `last_used_at` remain quota and recent-window signals; they do not replace request-level usage and aggregate stats.
- The owner may request the Key through a dedicated `private, no-store` endpoint. It is masked by default and never enters `/api/state`, browser storage, OPL PostgreSQL, Ledger, logs, caches, or operation payloads.
- Kubernetes Secret is the only authorized Key persistence point. Fabric writes or rotates an account-scoped Secret, and Workspace runtime receives only its reference.
- The global `OPL_CODEX_API_KEY` is forbidden for customer Workspaces.

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
- One authenticated `POST /api/workspace-launches` stores a durable `workspace.launch` RuntimeOperation. The existing provider reconciliation worker resumes its compute, storage, attachment, Gateway Secret, Runtime, and receipt phases after browser close or process restart.
- The submission-time Sub2API total-balance read is a read-only preflight, not a hold or reservation. Each child purchase still performs its existing safety preflight and deterministic debit protocol.
- Debit failure forbids every Tencent resource write.
- A confirmed provider result showing no billable resource exists permits exactly one idempotent refund.
- A partial or unknown provider result enters `manual_review` without refund or a second purchase.
- A Ledger failure after activation leaves the entitlement active and retries only its receipt.
- Replays never create a second debit, refund, purchase, renewal, Secret, or receipt.
- The merged implementation follows debit before provider mutation. Server-computed read-only reconciliation and its blocking purchase guard are CI-verified; production rollout and live reconciliation evidence remain pending.

## Products And Lifecycle

- Compute and storage are independent monthly entitlements. Workspace creation requires one active compute entitlement and one active storage entitlement.
- Prices are integer CNY cents and USD micros at fixed `1 USD = 7 CNY`; conversion happens once with ceiling division.
- Provider SKU may vary by approved environment but must satisfy the customer CPU and memory contract.
- Renewal follows preflight, debit, provider manual renewal, readback, entitlement extension, and receipt.
- Tencent automatic renewal is forbidden.
- Expired compute is stopped or destroyed. Expired storage is retained and inaccessible until reactivated or explicitly destroyed.
- Deleting PVC/PV records retained or released state; it cannot claim that CBS was destroyed without an explicit Tencent return result.

## Workspace Access And Secrets

- Workspace URLs are stable and require Runtime password login.
- A routing cookie selects a Runtime Service and is not an authentication credential.
- Persisted Workspace, list, public, and evidence responses never expose credentials; only the authorized runtime-status command returns a password transiently.
- Workspace access requires active compute and storage entitlements plus real runtime readiness.
- The pinned source image is `ghcr.io/gaofeng21cn/one-person-lab-webui:26.7.13@sha256:9d867fe0fc9db48b6efa27371d77770e46fc8cd97d26ef85a81fbdac7e96ca76`.
- Production mirrors that source to TCR and deploys the target `repository@sha256`; `latest` and tag-only production references are forbidden.
- CBS is mounted at `/data` and `/projects`.

## Console User Experience

- Authentication, lazy-route loading, and account-state loading have distinct timeout, error, and retry states.
- Public and login routes render immediately; a session check may enrich or redirect them but never gates their first interactive screen.
- The first authenticated screen answers current balance, Workspace usability, active compute and storage, expiry, current-period fixed charges, AI actual spend, and actionable failures.
- Billing history is a tenant-scoped projection of Ledger receipts. Gateway request history and totals are tenant-scoped projections of live Sub2API usage APIs. Neither projection persists a second copy of the facts.
- Balance, entitlements, billing receipts, and Gateway usage load independently. One unavailable source cannot hold the whole Console in a spinner or erase facts from another source.
- The primary flow is one recoverable Workspace launch guide covering plan, storage, total price, debit, PREPAID resources, Gateway Secret, Runtime, and URL.
- Workspace status polls every 10 seconds for at most 30 attempts, stops on ready or terminal state, and offers manual retry after a real error or timeout.
- Gateway fetches only when its page is opened, masks the Key by default, supports explicit reveal/copy, and clears sensitive response state on route leave or logout.
- Desktop and mobile QA must prove responsive layout, keyboard access, error recovery, and no sensitive-information overlap or leakage.

## Verification Slot

`verification-slot-01` is the only real-resource launch verification slot:

| Property | Frozen value |
| --- | --- |
| CVM | `SA5.MEDIUM4` (`2c4g`) |
| CBS | `10GB` minimum prepaid volume |
| Provider billing | `PREPAID`, one month |
| Renewal | `NOTIFY_AND_MANUAL_RENEW` |
| Customer product | No |
| Concurrency | One verification run |
| Lifetime | Retain and reuse for the full paid period |

- The lifetime purchase budget is one. Read-only inventory must first prove there is no reusable compliant slot; multiple or ambiguous candidates stop without purchase.
- Ordinary CI and commercial E2E use fake Sub2API and fake provider mutations.
- Runtime QA reuses the Slot, authenticates to the candidate image, proves WebSocket behavior, makes one real model request with a dedicated test Key, and removes only temporary workload and test data.
- A run never purchases, renews, or deletes Tencent CVM, CBS, node-pool, or PV resources.
- Slot renewal is a future separate manual decision, never an automatic action.
- Production smoke is read-only. The legacy paid verifier is blocked until replaced.

## Launch Stages

| Stage | Business | Owners | Current state | Required output and evidence |
| --- | --- | --- | --- | --- |
| 1. Offer and identity | Show mapped customers Basic and Pro without the verification SKU. | Console, Gateway | Manual mapping and operator password reset are CI-verified; self-registration and SSO are outside the Pilot, while Pro and deployed evidence remain incomplete. | Product contract, tenant tests, deployed account readback. |
| 2. Wallet and quote | Show live balance and exact compute plus storage quote before side effects. | Console, Gateway | Integer Workspace compute-plus-storage quote is CI-verified; deployed presentation and live Sub2API evidence remain incomplete. | Balance/quote tests, failure UI, period and retention disclosure. |
| 3. Balance debit | Debit the exact monthly amount once before provider mutation. | Console, Gateway, Ledger | Durable one-submit launch, total balance preflight, debit-first child operations, restart recovery, and replay are CI-verified; deployed browser and live Sub2API evidence remain pending. | Deterministic debit, balance check, replay/concurrency evidence. |
| 4. Prepaid fulfillment | Open one-month PREPAID CVM/CBS after debit. | Fabric, Console | PREPAID CVM/CBS request and readback are CI-verified; live Tencent evidence is pending. | Request shapes, provider readback, duplicate-purchase guard. |
| 5. Claim and activate | Activate only after every resource is owned and read back. | All four lanes | Claim, confirmed-absence refund, manual-review resolution, and server-computed read-only reconciliation with a blocking guard are CI-verified; live reconciliation evidence is pending. | Claim identity, confirmed-absence refund, ambiguous-result review. |
| 6. Workspace access | Authenticate to a ready, persistent, account-keyed Workspace. | Fabric, Console, Ledger | Attachment, Secret, readiness, and runtime isolation are CI-verified; browser, WebSocket, and live model evidence are pending. | Login, WebSocket 101, Secret rotation, digest readback. |
| 7. Gateway usage | Reveal the owner Key, make a metered Workspace model request, and show its customer-safe cost and Token facts. | Gateway, Console, Ledger | Balance, Key summary, request-level usage, aggregate stats, and customer-safe integer-cost projections are CI-verified; a real model request and production readback remain pending. | Tenant isolation, model response, request usage and stats projection, integer `actual_cost`, no leakage. |
| 8. Renewal and recovery | Renew customer and Tencent periods once with deterministic recovery. | All four lanes | Entitlement worker exists; provider PREPAID renewal does not. | Renewal replay, deadline readback, refund/review receipts. |
| 9. Reusable verification | Prove releases without per-run Tencent purchase or deletion. | All four lanes | Legacy verifier violates this rule. | Retained Slot, fake commercial chain, real Workspace/Gateway proof. |
| 10. Production release | Declare ready from immutable artifacts, rollout, rollback, and real evidence. | All four lanes | CI/rollout exist; remaining production evidence is incomplete. | CI, grouped rollback, read-only smoke, fixed-Slot receipt. |

## Delivery Phases

The six delivery phases are: contract and cleanup; Fabric PREPAID and Workspace; Gateway account projection; commercial plans and settlement; Console UX and release safety; integration, real verification, and rollout. These phases organize four sessions and do not replace the ten business launch stages.

## Completion Rule

A launch stage is complete only when its human and machine contracts are current, focused and full tests pass, merged CI passes, exact image digests are deployed, and the listed runtime evidence is recorded. Documentation or a green fake alone never proves production delivery.
