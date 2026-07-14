# Invariants

## Ownership

- Console calls only Control Plane product APIs.
- Sub2API owns spendable balance, API keys, routing, and request usage.
- Control Plane owns account mapping, monthly entitlement, and billing operation.
- Fabric owns cloud resources and provider facts, never billing state.
- Ledger owns append-only evidence, never spendable balance.
- No service writes another service's database.

## Pricing

- Basic compute: `35000` CNY cents and `50000000` USD micros per calendar month.
- Pro compute: `150000` CNY cents and `214285715` USD micros per calendar month.
- Each 10 GB storage block: `1800` CNY cents and `2571429` USD micros per calendar month.
- Storage size is a positive multiple of 10 GB.
- Money decisions use integers; runtime price lookup and float conversion are forbidden.

## Purchase And Recovery

- Validate before any provider or money call.
- Persist resource, billing operation, and stable redeem code before side effects.
- Fabric prepares before charge; entitlement activates only after exact charge confirmation.
- A retry reuses the same operation and redeem code.
- Ambiguous charge state enters `manual_review`; it is never treated as success or failure by inference.
- Ledger receipt retry never repeats or reverses a confirmed charge.

## Lifecycle

- Renewal extends from the current `paidThrough`.
- Expired compute is destroyed.
- Expired storage is retained and inaccessible until reactivated.
- Attachment and Workspace runtime mutations require active entitlement.
- Destroying compute never destroys storage.
- Storage destruction requires an explicit confirmation.

## Access And Secrets

- Workspace share URLs remain valid until reset or deletion and do not require Console login.
- Public and evidence responses never expose raw credentials or share tokens.
- Production manifests contain secret references only.
- Every production Console account has one positive integer `sub2apiUserId`.

## Verification

- Production E2E requires explicit confirmation that it spends real balance.
- It verifies exact balance delta, redeem-code identity, receipts, Workspace access, and exact resource cleanup.
- It deletes only resources created by its own run.
