# Launch Invariants

This file is the mandatory human-readable launch contract for this implementation repository. The target product boundaries come from `https://github.com/gaofeng21cn/one-person-lab-cloud`; the revision reviewed for this freeze is `fdeb0e4df3e4905fb1c3551337b9dfda65bb2119`.

The upstream repository owns product architecture. This repository owns its selected backend, exact prices, provider procurement, delivery state, and runtime evidence. A frozen target is not a readiness claim. Current gaps and required evidence are recorded here and in `packages/contracts/opl-cloud-launch-freeze-contract.json`.

## Product Surfaces And Owner Lanes

The five product surfaces are OPL Gateway, OPL Workspace, OPL Console, OPL Fabric, and OPL Ledger. Workspace is the product delivered by Fabric after it opens CVM and CBS and deploys the pinned `one-person-lab-app` image; it is not a fifth service repository.

The four implementation owner lanes are Console/Control Plane, Fabric, Gateway integration, and Ledger. Gateway integration is an adapter to the externally deployed Sub2API, never a second Gateway service.

## Console

- Console calls only Control Plane product APIs.
- Control Plane owns account-to-Sub2API mapping, quotes, monthly orchestration, entitlements, renewal, expiry, and operator review.
- Console displays live Sub2API balance, Key metadata, and Key DTO usage without creating a wallet, Key database, or usage database.
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
- Receipts contain stable account, Workspace, billing-operation, provider-operation, resource, pricing, period, and redacted Gateway request references.
- API keys, passwords, raw tokens, provider secrets, and raw Sub2API responses are forbidden in evidence.
- A missing receipt retries only the receipt and never repeats debit, refund, provider purchase, Secret write, or renewal.

## Gateway

- OPL Gateway uses the externally deployed Sub2API v0.1.156 backend. Cloud accepts the API-compatible v0.1.155 rollback and v0.1.156 current versions; its code, image, container, database, configuration, and deployment are immutable from this repository.
- Sub2API is the only owner of spendable USD balance, API keys, model routing, and request usage.
- Control Plane maps the signed-in account through `sub2apiUserId` and selects exactly one active Key named `opl-workspace`; zero or multiple matches fail closed.
- `GET /api/v1/admin/users/:id/usage` is not accepted as evidence in v0.1.155 or v0.1.156. Usage comes from the Key DTO fields `quota_used`, `usage_5h`, `usage_1d`, `usage_7d`, and `last_used_at`.
- The owner may request the Key through a dedicated `private, no-store` endpoint. It is masked by default and never enters `/api/state`, browser storage, OPL PostgreSQL, Ledger, logs, caches, or operation payloads.
- Kubernetes Secret is the only authorized Key persistence point. Fabric writes or rotates an account-scoped Secret, and Workspace runtime receives only its reference.
- The global `OPL_CODEX_API_KEY` is forbidden for customer Workspaces.

## Monthly Settlement

Sub2API v0.1.156 has no generic hold/capture API. The approved purchase protocol therefore reuses the already verified deterministic Redeem Code and Idempotency-Key path:

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
- Debit failure forbids every Tencent resource write.
- A confirmed provider result showing no billable resource exists permits exactly one idempotent refund.
- A partial or unknown provider result enters `manual_review` without refund or a second purchase.
- A Ledger failure after activation leaves the entitlement active and retries only its receipt.
- Replays never create a second debit, refund, purchase, renewal, Secret, or receipt.
- The current implementation still prepares Fabric before debit; that path must exit before launch.

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
| 1. Offer and identity | Show mapped customers Basic and Pro without the verification SKU. | Console, Gateway | Basic mapping exists; Pro implementation evidence is incomplete. | Product contract, tenant tests, deployed account readback. |
| 2. Wallet and quote | Show live balance and exact compute plus storage quote before side effects. | Console, Gateway | Basic exists; Pro and complete presentation remain incomplete. | Balance/quote tests, failure UI, period and retention disclosure. |
| 3. Balance debit | Debit the exact monthly amount once before provider mutation. | Console, Gateway, Ledger | Current order is reversed. | Deterministic debit, balance check, replay/concurrency evidence. |
| 4. Prepaid fulfillment | Open one-month PREPAID CVM/CBS after debit. | Fabric, Console | Blocked by POSTPAID CVM and missing explicit CBS. | Request shapes, provider readback, duplicate-purchase guard. |
| 5. Claim and activate | Activate only after every resource is owned and read back. | All four lanes | Claim/refund/manual-review behavior is incomplete. | Claim identity, confirmed-absence refund, ambiguous-result review. |
| 6. Workspace access | Authenticate to a ready, persistent, account-keyed Workspace. | Fabric, Console, Ledger | Password/readiness exist; CBS, Key, image, browser, and WebSocket evidence are missing. | Login, WebSocket 101, Secret rotation, digest readback. |
| 7. Gateway usage | Reveal the owner Key and make a metered Workspace model request. | Gateway, Console, Ledger | Balance/portal exist; Key and usage attribution are unproven. | Tenant isolation, model response, Key DTO usage increase, no leakage. |
| 8. Renewal and recovery | Renew customer and Tencent periods once with deterministic recovery. | All four lanes | Entitlement worker exists; provider PREPAID renewal does not. | Renewal replay, deadline readback, refund/review receipts. |
| 9. Reusable verification | Prove releases without per-run Tencent purchase or deletion. | All four lanes | Legacy verifier violates this rule. | Retained Slot, fake commercial chain, real Workspace/Gateway proof. |
| 10. Production release | Declare ready from immutable artifacts, rollout, rollback, and real evidence. | All four lanes | CI/rollout exist; remaining production evidence is incomplete. | CI, grouped rollback, read-only smoke, fixed-Slot receipt. |

## Delivery Phases

The six delivery phases are: contract and cleanup; Fabric PREPAID and Workspace; Gateway account projection; commercial plans and settlement; Console UX and release safety; integration, real verification, and rollout. These phases organize four sessions and do not replace the ten business launch stages.

## Completion Rule

A launch stage is complete only when its human and machine contracts are current, focused and full tests pass, merged CI passes, exact image digests are deployed, and the listed runtime evidence is recorded. Documentation or a green fake alone never proves production delivery.
