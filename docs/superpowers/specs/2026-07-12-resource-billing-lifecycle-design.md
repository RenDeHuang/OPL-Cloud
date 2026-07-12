# Resource Billing Lifecycle Design

## Outcome

Compute and storage provisioning become recoverable commercial operations across
Console, Control Plane, Fabric, and Ledger. A successful activation charges exactly
the first hour and leaves exactly seven days of that resource's price frozen. A
failed activation charges nothing. Destroy stops future billing and releases only
the remaining amount of the resource's own Hold after cloud deletion is confirmed.

## Existing Baseline

Commit `3f7923e` already provides useful compensation primitives:

- terminal resources release Holds through Control Plane;
- periodic settlement continues after one resource fails;
- Workspace Runtime creation compensates Ledger receipt failures;
- provider reconciliation preserves commercial projection fields.

Those changes are retained. They do not complete the lifecycle because Ledger still
settles against account-wide frozen totals, release trusts caller-supplied amounts,
and Fabric cannot recover every asynchronous or partially applied provider operation.

The removed `gateway/g0-g4` branch and its old billing code are not dependencies or
merge inputs for this work.

## Considered Approaches

### Selected: Per-Hold Ledger state plus existing reconciliation workers

Ledger owns each Hold's remaining amount and validates every activation, settlement,
and release against it. Control Plane remains the saga coordinator, while existing
Fabric operation persistence and reconciliation workers resume incomplete work.

This is the smallest approach that fixes the accounting authority at its source and
reuses the repository's current service boundaries.

### Rejected: Keep account-wide frozen totals and improve Control Plane bookkeeping

Control Plane could calculate a presumed remaining amount and submit it on release.
That cannot prevent another resource from consuming the Hold, and concurrent Ledger
requests can still overspend the same wallet. It treats projections as financial
authority and therefore does not close the Ledger boundary.

### Rejected: Add a new workflow engine or distributed transaction coordinator

A workflow engine could model every crash point, but it adds infrastructure and a
second operation model while Fabric and Control Plane already persist idempotent
operations. Durable claims, strict Ledger transitions, and reconciliation cover the
required failure modes with less code and fewer moving parts.

## Financial Invariants

1. Every billable compute allocation or storage volume has one owning Hold identified
   by `hold_id`, `account_id`, `workspace_id`, `resource_type`, and `resource_id`.
2. Provisioning initially reserves seven days of guarantee plus the first hour. This
   is authorization, not a debit.
3. Successful provider verification atomically consumes the first-hour portion and
   activates the Hold. The resulting frozen remainder is exactly seven days.
4. Failed provisioning never creates a debit. After cloud absence is confirmed, it
   releases the whole remaining authorization.
5. Periodic settlement first spends account available balance. Any shortfall may
   consume only the current resource's remaining Hold.
6. If available balance plus that Hold cannot cover the next hour, settlement makes
   no partial mutation and triggers the resource suspension/destruction workflow.
   Another resource's Hold is never consumed.
7. An accepted destroy request immediately changes billing to a non-billable
   `stopping` state. Provider cleanup is retried until absence is confirmed.
8. Confirmed destroy changes billing to `stopped` and releases the owning Hold's
   remaining amount. Release never debits wallet balance.
9. A Hold can be consumed or released only once per idempotency key, and its total
   consumed plus released amount can never exceed its original authorization.
10. Wallet totals and Hold state change in one Ledger transaction under a wallet row
    lock. Concurrent activation, settlement, and release cannot lose updates.

For a settlement amount `charge`, Ledger applies:

```text
available_part = min(charge, wallet.available)
hold_part      = charge - available_part

require hold_part <= resource_hold.remaining
wallet.balance       -= charge
wallet.frozen        -= hold_part
resource_hold.remaining -= hold_part
resource_hold.consumed  += hold_part
wallet.available = wallet.balance - wallet.frozen
```

Releasing a Hold applies:

```text
release = resource_hold.remaining
wallet.frozen -= release
resource_hold.remaining = 0
resource_hold.released += release
wallet.balance is unchanged
```

## Ledger Design

The existing Hold becomes the authority for its lifecycle. It records original,
remaining, consumed, and released cents plus a status such as `reserved`, `active`,
`exhausted`, or `released`. Existing account wallet totals remain aggregate views.

Ledger exposes narrow idempotent transitions rather than accepting release arithmetic
from Control Plane:

- reserve activation funds for one resource;
- activate the Hold and consume the first hour after provider verification;
- settle one resource against available balance and that resource's Hold;
- release all remaining funds for a failed or destroyed resource.

Each request includes the Hold identity and resource identity. Ledger rejects missing,
mismatched, already terminal, non-positive, mixed-currency, and idempotency-conflicting
requests. Release amount is calculated inside Ledger; caller-supplied Hold amounts are
not financial authority.

PostgreSQL mutations lock the wallet before reading balances and lock the owning Hold
before changing it. The in-memory store implements the same transitions so tests and
local behavior do not diverge.

## Provisioning Flow

Control Plane first persists or claims an execution request using the caller's
idempotency key and a canonical request hash. A replay with the same hash resumes or
returns the stored result; a different request using the key fails with an
idempotency conflict. Resource IDs derive from the claimed request and remain stable.

```text
claim request
-> Ledger reserves seven days plus first hour
-> save provisioning projection with hold_id
-> Fabric claims and starts provider operation
-> verify real provider identity and readiness
-> Ledger consumes first hour and activates Hold
-> save running/available projection with billing active
```

Provider success requires usable provider evidence, not only an `OK` response:

- compute requires the expected Machine and a non-empty Machine or Node identity;
- storage requires the expected PVC identity and usable status;
- returned identity must belong to the requested resource and Workspace.

If Control Plane crashes after any arrow, reconciliation resumes from the durable
claim and idempotent downstream transition. In particular, provider success followed
by a missed Ledger activation is finalized once, rather than creating a second cloud
resource.

## Provisioning Failure

Fabric treats timeout, no Machine, no Node identity, no PVC, malformed provider
evidence, and partial provider mutation as provisioning failures. Before reporting a
compensatable terminal failure, it discovers and deletes any resource created by the
operation and confirms absence.

```text
provider failure
-> discover operation residue
-> retry cleanup until absence is confirmed
-> mark Fabric operation failed and cleaned
-> Ledger releases the full remaining authorization
-> Control Plane saves failed/stopped terminal projection
```

If cleanup cannot confirm absence, the operation remains reconcilable and the Hold
remains reserved. It is not charged and is not released while a paid cloud resource
may still exist. This prevents both customer overcharging and unbacked provider cost.

## Destroy Flow

Control Plane claims the destroy request and changes billing from `active` to
`stopping` before calling Fabric. Settlement input generation excludes `stopping`,
`stopped`, failed, and destroyed resources, so no later hourly period is charged.

Fabric deletion must propagate provider and `kubectl` failures. A successful destroy
requires confirmed absence of the compute Machine/Node or storage PVC, not merely a
successful command exit. Reconciliation retries incomplete deletion with the same
operation key.

Only after confirmed absence does Control Plane request Ledger to release the owning
Hold's remaining amount and persist `billing_status=stopped`. If projection persistence
fails after Ledger release, replay returns the same release and reconciliation repairs
the projection.

The platform absorbs provider cost caused by delayed or failed cleanup after an
accepted destroy request; customers are not billed beyond that request.

## Control Plane And Console

Control Plane projections expose the commercial state without becoming accounting
authority. At minimum they retain the operation ID, Hold ID, provider status,
`billing_status`, latest settlement ID, release ID, and customer-safe failure detail.

The existing Console resource polling and result surfaces are reused. They display
the backend states `provisioning`, `running` or `available`, `destroying`, `failed`,
and `destroyed`, together with `pending`, `active`, `stopping`, or `stopped` billing.
Console does not calculate Hold remainder or infer successful activation from an HTTP
acceptance response.

Workspace projection failures are repaired from the durable execution request,
Fabric operation, Ledger receipt, and resource projections. Existing Runtime cleanup
on receipt failure remains in place.

## Reconciliation And Exhaustion

The existing periodic settlement worker continues processing unrelated accounts and
resources after one failure. An insufficient resource emits a durable failure result
for Control Plane, which moves billing out of `active` and requests suspension or
destruction using a stable key. Replays cannot charge the failed hour twice.

Reconciliation covers these incomplete states:

- claimed request without a Ledger reservation;
- reservation without a Fabric operation;
- Fabric operation started but not terminal;
- verified provider resource without Ledger activation;
- provider failure with possible residue;
- destroy requested but cloud deletion unconfirmed;
- Ledger release completed but Control Plane projection stale.

Workers aggregate errors and continue processing other resources.

## Migration

The Ledger migration adds Hold lifecycle fields and the constraints/indexes required
for resource ownership and idempotent release. Existing active Holds are backfilled
conservatively with remaining amount equal to their original amount minus any
unambiguously attributable prior consumption. Ambiguous historical Holds are not
automatically released; they are flagged for reconciliation to avoid inventing money.

No new dependency, queue, workflow engine, or generic abstraction is introduced.

## Verification

The implementation leaves focused runnable checks at each boundary:

- Ledger unit and PostgreSQL tests for activation, available-first settlement,
  own-Hold fallback, release arithmetic, identity rejection, replay, and concurrent
  settle/release serialization;
- Fabric tests for crash recovery, no-Machine/no-Node/no-PVC results, partial-create
  cleanup, strict delete errors, and confirmed absence;
- Control Plane failure-injection tests at every provisioning and destroy boundary,
  stable request identity, projection repair, billing exclusion, and Hold exhaustion;
- Console contract tests proving backend lifecycle states are shown without local
  balance arithmetic;
- cross-service tests proving successful activation, failed activation, hourly
  settlement, exhausted Hold, destroy, and replay end to end;
- complete existing Node, Control Plane, Fabric, and Ledger suites.

## Completion Criteria

The lifecycle is closed only when all of the following are demonstrated:

- success leaves one real resource, one first-hour debit, and one active seven-day Hold;
- failure leaves no cloud residue, no debit, and no frozen funds;
- hourly billing uses available balance before only the owning Hold;
- Hold exhaustion stops the resource without touching another Hold;
- destroy prevents future periods, confirms cloud deletion, and releases only the
  owning Hold's remaining amount without debiting balance;
- process crashes and repeated requests converge to the same financial and provider
  result;
- Console shows the reconciled backend truth for every terminal and in-progress state.
