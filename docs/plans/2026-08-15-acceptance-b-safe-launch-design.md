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

The GET-only reconcile dispatch uses an empty `approval_id`, empty
`resume_run_id`, and every confirmation set to `false`. The fresh-order
dispatch uses the exact installed `approval_id`, empty `resume_run_id`, only
`confirm_workspace_purchase=true`, and every other confirmation set to
`false`. This parameter matrix is part of the machine deployment contract.

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
| Product release | root controller for mutation | Standard product release publishes the Cloud image for the canonical product SHA; it does not own the Workspace image | The deployed Cloud SHA/digest must match the product release |
| Workspace image/deployment | `deployment_chain` and the Instance/`one-person-lab-app` owner; root for mutation | Select and verify the immutable Workspace image, approval, TCR copy, TKE rollout, and Pod image IDs | TCR, TKE, Approval, and Pod readback must agree on the Workspace digest |
| Release optimization | independent parallel release writer | Workflow-only performance/reproducibility changes, integrated independently and never required by this Acceptance checkpoint | It must not change the Acceptance identity or delay the correctness release |
| Runtime evidence | `runtime_evidence` | No repository writes and no production mutations | GET-only preflight and terminal evidence must be complete |
| Production mutation | root controller only | GitHub release, TCR copy, TKE rollout, approval rotation, one Workspace order | No concurrent or duplicate production write |

Implementation writers do not run in parallel on overlapping product files.
Independent release diagnostics and tests may run in parallel. Canonical merge,
release, deployment, and the Workspace POST are serialized by the root
controller.

## Release Decision

This P0 changes Control Plane reconciliation behavior, so the standard product
Cloud image rebuild is required. The product release binds the canonical
product SHA to the immutable Cloud image digest only. The Workspace image is
owned by the Instance/`one-person-lab-app` lane; the production approval binds
that separately selected immutable digest, and deployment must prove it through
TCR, TKE workload configuration, the installed Approval, and the ready Workspace
Pod image ID. A tool-only bypass would weaken those contracts.

The following are measured release costs but are not P0 blockers and are not in
this write set:

| Concern | Decision for this P0 |
| --- | --- |
| runtime-stage `npm ci --omit=dev` | Do not change. Removal requires proving every in-pod Node diagnostic has moved or remains executable. |
| repeated npm installs | Do not change. It costs time but does not block correctness. |
| release workflow performance and cache | Owned by an independent parallel release writer. It is not a dependency of this Acceptance checkpoint and must preserve cold/warm reproducibility. |
| architecture matrix and image composition | May be evaluated in that independent release lane only with unchanged artifact, attestation, portability, rollback, and provider contracts. |
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
-> one immutable Cloud product release
-> Instance selects/verifies Workspace image and performs TCR copy/TKE rollout
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
- Product release readback must prove the exact tag, product SHA, Cloud image
  digest, platforms, assets, and attestations.
- Instance readback must prove Cloud and Workspace TCR/TKE image IDs, the exact
  Approval bindings, and the ready Pod image IDs.
- Production success requires the final three evidence groups above.
