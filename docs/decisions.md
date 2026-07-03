# Decisions

## 2026-07-03: Adopt One Person Lab Lifecycle

Decision: this repository follows the `one-person-lab` docs/contracts/tests lifecycle.

Implications:

- active docs describe current truth;
- process records move to `docs/history/**`;
- machine contracts live in `packages/contracts/**`;
- tests are classified by lifecycle;
- temporary tests are retired once their migration or cleanup condition is met.

## 2026-07-03: No Long-Term Compatibility Layer

Decision: do not keep compatibility wrappers, old aliases, or duplicate routes after active callers move.

Allowed bridge: one-time migration code that upgrades persisted data into the current model.

Disallowed long-term state:

- `accounts` wallet mirror semantics;
- legacy user import as an auth source after store users exist;
- retired account-credit endpoint aliases;
- contracts that preserve legacy aliases as current product truth.

## 2026-07-03: User Wallet Is Commercial Billing Truth

Decision: commercial balance, holds, total recharge, and wallet transactions attach to users and billing ownership, not to legacy account mirrors.

Workspace and ledger records may keep `accountId` as billing-account identity, but wallet mutation happens through the current user wallet model.

## 2026-07-03: Lab Owner UI Hides Operator Evidence

Decision: Lab Owner UI is a commercial workspace distribution and billing explanation surface.

Raw Fabric, Runtime, Production Readiness, request fingerprint, dedup, and raw Ledger evidence belong to Admin/operator views.

## 2026-07-03: Deployment Tests Follow Machine Contracts

Decision: workflow, image, deployment, and route tests should read machine contracts or exported manifests instead of hard-coding long source snippets in tests.

This keeps tests permanent while avoiding phase-only guards that break on harmless implementation edits.
