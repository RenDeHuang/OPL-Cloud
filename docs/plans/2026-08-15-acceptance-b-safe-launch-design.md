# Acceptance B Safe Launch Design

## Objective

Deploy one immutable `one-person-lab-cloud` release through
`opl-instance-medopl`, then create exactly one Basic Workspace for the approved
customer at `cloud.medopl.cn`. The run is successful only when the Workspace,
runtime, unique Sub2API debit, and Ledger receipt all reconcile.

## Existing Authorities

- Sub2API remains the only owner of spendable balance, request usage, balance
  history, debit, and refund.
- Control Plane owns pricing, Workspace launch orchestration, and the stable
  launch/debit identities.
- Ledger owns append-only receipts and reconciliation evidence. It cannot own or
  mutate balance.
- `one-person-lab-cloud` owns product behavior and product contracts.
- `opl-instance-medopl` owns the selected immutable release, production
  approval, deployment, and instance acceptance execution.

The current billing contract already allows concurrent general API usage and
requires a Workspace debit to be proved by one exact Sub2API history entry. A
wallet before/after delta is not debit identity.

## Defect

The Acceptance B account runner upgrades a server `manual_review` result to
`safe_to_retry_absent` when the customer login succeeds and the four Workspace
footprint counts are zero. It does not prove the debit identity fixed by the
Acceptance B approval. The fresh-order runner separately checks only the first
key page and amount-matches a balance-history item after launch.

This is unsafe in an account with active general usage. It also cannot be fixed
by rejecting all negative balance-history items because this customer has an
older legitimate negative entry. An account-wide negative-history rule would
permanently block the approved customer and contradict operation-scoped
settlement.

## Approved Design

### Approval-Bound Reconciliation

The installed Acceptance B approval already fixes:

```text
customer account
+ launch idempotency key
+ launch operation ID
+ Workspace ID
+ release SHA and image digests
+ package, storage, and expected provider target
```

The Control Plane account-reconciliation GET path will validate that the
installed approval matches the deployed release and target customer. It will
derive the exact Workspace debit code from the approved operation ID and query
Sub2API with `FinancialBalanceHistoryByCodes`. The response will expose only a
redacted approval/debit identity digest and a state of `absent`, `confirmed`,
`conflict`, or `unknown`; it will never expose the redeem code.

`prepared` means all of the following are authoritative and consistent:

- the local Account/User graph is complete and matches one active Sub2API user;
- customer login succeeds;
- the live Sub2API wallet is active;
- the installed approval is current and matches the deployed release/customer;
- the approved launch operation, Workspace, Workspace key, purchase receipt,
  and exact debit are absent;
- there is no unknown or conflicting readback.

General keys, general usage, historical credits, and unrelated historical
debits are recorded as context but do not prove or disprove the approved
Workspace operation. The missing Acceptance-specific recharge operation is
also not a reason to recharge or block a sufficiently funded wallet.

The `safe_to_retry_absent` status is removed from the success set. Recovery of
an existing or unknown approved operation always uses GET of that same
operation and never authorizes a new identity.

### Immediate Write Gate

The fresh-order tool will invoke the same approval-bound GET reconciliation
immediately before the Workspace POST. It will then read the current server
quote and live Sub2API wallet. General usage may change the wallet between
reads; the only money precondition is that the current wallet is strictly above
the current quote. The Sub2API debit remains the authoritative atomic money
write.

The tool then calls the existing submission function, which first GETs the
approved deterministic operation and sends at most one
`POST /api/workspace-launches` with the approved idempotency key. An unknown
response is followed only by GET of the same operation. No second order is
allowed in the Acceptance run.

### Final Evidence

Success requires all three groups:

1. Launch/runtime: the approved operation succeeded, the approved Workspace is
   active, the runtime uses the approved immutable Workspace image, and the
   Workspace URL returns HTTP 200.
2. Money: exactly one Sub2API history entry matches the approved debit code,
   account, and final quote.
3. Receipt: exactly one Ledger purchase receipt matches the Workspace, price
   version, period, amount, and the same debit reference.

Wallet delta and general usage are never substituted for the exact debit.

## Module Owners And Write Sets

| Lane | Owner | Write set | Acceptance gate blocked |
| --- | --- | --- | --- |
| Product reconcile | `product_chain` | `services/control-plane/internal/server/account_reconcile.go`, `services/control-plane/internal/server/workspace_launch_admission.go`, their focused Go tests, `tools/production-basic-acceptance-b-reconcile.ts`, reconcile tests, the deployment contract | A false `prepared`/retry decision cannot reach the writer |
| Product writer | `product_chain` under serial integration | `tools/production-basic-acceptance-b.ts` and focused tests | The single Workspace POST cannot run on stale or unbound evidence |
| Release/deployment | `deployment_chain` for diagnostics; root for mutation | No release-performance write set in P0. Instance changes are allowed only if the new product contract cannot be consumed by the current workflow | The deployed SHA/digests/approval must be one immutable identity |
| Runtime evidence | `runtime_evidence` | No repository writes and no production mutations | GET-only preflight and terminal evidence must be complete |
| Production mutation | root controller only | GitHub release, TCR copy, TKE rollout, approval rotation, one Workspace order | No concurrent or duplicate production write |

Implementation writers do not run in parallel on overlapping product files.
Independent release diagnostics and tests may run in parallel. Canonical merge,
release, deployment, and the Workspace POST are serialized by the root
controller.

## Release Decision

This P0 changes Control Plane reconciliation behavior, so the standard product
image rebuild is required. The release identity binds product SHA, Cloud image
digest, Workspace image digest, and Acceptance approval; a tool-only bypass
would weaken that contract.

The following are measured release costs but are not P0 blockers and are not in
this write set:

| Concern | Decision for this P0 |
| --- | --- |
| runtime-stage `npm ci --omit=dev` | Do not change. Removal requires proving every in-pod Node diagnostic has moved or remains executable. |
| repeated npm installs | Do not change. It costs time but does not block correctness. |
| no persistent BuildKit layer cache | Defer as an independent P1 workflow-only optimization with cold/warm reproducibility evidence. |
| amd64 and arm64 on one hosted runner | Do not split. Native matrix assembly changes artifact and attestation design. |
| one image contains local-docker and Tencent tooling | Do not split. It changes portable release, Compose, TKE, rollback, and provider contracts. |
| Acceptance script change rebuilds the release | Keep for this P0 because the server changes and approval/release identity require one new immutable product release. |

No `npm ci` is run merely to create the worktree or write plans. Focused Node
tests run directly with Node, and Go tests use the Go module. Dependency
installation is performed only for a validation command that demonstrably
requires it.

## Critical Path

```text
approved design
-> failing focused tests
-> product reconcile and immediate write gate
-> focused Node and Go tests
-> full local verification
-> product PR and canonical main readback
-> one immutable product release
-> Instance TCR copy and TKE rollout
-> GET-only approval-bound reconcile
-> exactly one Workspace POST
-> exact operation GET readback
-> runtime + debit + receipt terminal evidence
```

## Failure Rules

- Any missing, conflicting, stale, or unavailable authority yields `unknown` or
  `manual_review`, never `prepared`.
- No account preparation or wallet recharge is run for this customer.
- No full historical usage scan is repeated. Existing usage evidence is retained
  only as context; the approved Workspace debit is resolved by exact code.
- A failed or unknown Workspace POST is not retried in the Acceptance run.
- Release or deployment failure stops before customer mutation.
- Terminal runtime, debit, or receipt mismatch stops with the original operation
  frozen for readback; it does not authorize a successor order.

## Verification

- Focused Node tests prove general usage is allowed, unrelated historical debits
  are allowed, the approved debit blocks a new POST, pagination fails closed,
  and the pre-POST readback prevents stale writes.
- Focused Go tests prove approval/deployment binding and exact Sub2API debit-code
  lookup without exposing the code.
- Product boundary and full local verification must pass before merge.
- Release readback must prove the exact tag, product SHA, two image digests,
  platforms, assets, and attestations.
- Instance readback must prove the deployed image IDs and approval SHA.
- Production success requires the final three evidence groups above.
