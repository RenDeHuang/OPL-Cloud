# Public Beta A-N Delivery Design

## Decision

Deliver the next OPL Cloud product cut as a public-registration, zero-initial-
balance, administrator-funded, controlled Workspace beta. The output is a
portable `one-person-lab-cloud` Candidate whose exact bytes are qualified
locally and by the Instance owner before those bytes are promoted as a formal
Release.

A-N are portfolio work-package aliases. They are not fourteen services, one
global workflow, or permission for Cloud to own Instance deployment. Each
package lands in an existing bounded context, closes with focused evidence, and
joins the product chain only at explicit integration gates.

## Product Outcome

The shortest complete customer and operator journey is:

```text
public registration
  -> Account/User/Sub2API identity convergence with zero balance
  -> administrator wallet top-up
  -> authoritative quote and balance admission
  -> exactly-once Workspace purchase and provisioning
  -> Runtime access and Receipt readback
  -> renewal or permanent deletion with recovery
  -> operator visibility, alert closure, and data recovery readiness
  -> one exact Candidate
  -> Local and Tencent/TKE qualification of the same digest
  -> exact-byte formal Release
```

Registration does not purchase cloud resources. Zero or insufficient balance
rejects purchase before debit, Fabric mutation, or Ledger receipt. Customer-
operated payment, shared Workspace membership, HA, GPU, and a second wallet are
outside this beta cut.

## Repository And Authority Boundary

`one-person-lab-cloud` owns:

- Console, Control Plane, Fabric, and Ledger product behavior;
- the three PostgreSQL schema owners and their restore compatibility;
- provider-neutral contracts and reusable provider adapters;
- portable installation assets, Candidate identity, qualification admission,
  and formal Release promotion.

`opl-instance-medopl` remains an external owner for:

- domain, TLS, ingress, production Secrets, Provider Profile, and Workspace
  image selection;
- deployment, production backup schedules, alert routing, rollback, and
  protected Tencent/TKE mutations;
- authoritative Instance qualification and operational receipts.

Cloud work may define the input and receipt contract for that boundary. It does
not dispatch Instance deployment, access the production private network, or
perform real purchases, renewals, deletions, restores, or releases as part of
this design change.

## Bounded Contexts

| Bounded context | Work packages | Owns | Does not own |
| --- | --- | --- | --- |
| Account and Access | D, M | Registration, account/session policy, public auth, disable/recovery boundaries | Wallet balance, provider resources, TLS/Ingress |
| Catalog and Admission | E, I | Packages, quote, purchase eligibility, customer DTO decision | Spendable balance or Instance price overrides |
| Settlement Coordination | C, E | Debit/top-up coordination, stable idempotency identities, money readback gates | A second wallet or direct Sub2API table writes |
| Workspace Lifecycle | A orchestration side, B, C | Launch/Renewal/Delete use cases, Workspace projection, lifecycle recovery | Provider resource truth or Ledger evidence truth |
| Fabric Resource Lifecycle | A, H | Provider-neutral mutation/readback, persisted bindings, compute-pool serialization | Customer balance or Workspace product policy |
| Operations and Recovery | F, J, L Cloud side | Typed operation projections, allowed recovery commands, stable active/recovered signals | Another domain reducer or Instance alert receiver |
| Data Continuity | K Cloud side | Per-service restore compatibility and owner readback obligations | Production backup schedule, keys, retention, or restore execution |
| Delivery and Readiness | G, N | Candidate identity, portable assets, qualification admission, exact-byte promotion | Instance deployment or rollback execution |

Control Plane remains one process and Go module. Cohesion is improved inside
that module; no second Control Plane service, global business reducer, durable
workflow engine, event bus, or shared policy layer is introduced.

## Clean Architecture Direction

```text
Console / HTTP Route / DTO
  -> Application Use Case
  -> Bounded-context Operation and Reducer
  -> Owner Port
  -> PostgreSQL / Fabric / Ledger / Sub2API Adapter
```

Dependencies point inward. Routes validate transport concerns and call one use
case. Use cases coordinate but do not redefine owner facts. Domain operations
own legal transitions. Adapters translate typed public contracts and persist
only their owner's state.

F is an on-touch rule rather than a preliminary rewrite:

```text
change Registration -> type Registration command/result/transitions now
change Delete       -> type Delete claim/recovery transitions now
change Renewal      -> type Renewal reactivation/recovery transitions now
change Reset        -> type only the live Reset boundary now
final F audit       -> prove all changed consumers use those types
```

This keeps current callers and persisted data as the reason for every type,
avoids a global operation abstraction, and prevents an unrelated migration from
blocking customer value.

## Persistence And Convergence Rules

Every irreversible or externally uncertain step follows one chain:

```text
validate canonical identity
  -> persist command intent and idempotency identity
  -> claim by CAS/lease
  -> read authoritative owner state
  -> mutate only when the readback permits it
  -> persist typed result
  -> project customer state
  -> append evidence Receipt
  -> read every owner back before terminal success
```

The persistence owners are separate:

| Owner | Durable facts |
| --- | --- |
| Control Plane PostgreSQL | Account/User mapping, Session policy, Registration/Wallet/Launch/Renewal/Delete/Reset operations, Workspace projection, operator audit |
| Fabric PostgreSQL | Fabric operations, exact resource bindings, provider mutation epochs, machine ownership, compute-pool FIFO claim and lease |
| Ledger PostgreSQL | Append-only receipts, evidence identity, retention metadata, reconciliation status |
| Sub2API | Spendable wallet, balance history, Workspace Key, routing and usage truth |
| Instance | Deployment, backup/restore, public-edge, alert/on-call, rollback, and qualification receipts |

No service writes another service's tables. Cross-owner recovery is based on
typed HTTP readback, deterministic child idempotency keys, and persisted local
progress. `unknown` or conflicting identity fails closed and becomes an
explicit operator state; retries never infer success from timeout or local
stage position.

## Current Baseline

The design baseline is canonical `main@95843e5f8f19b0c7e72f288c022a18d4338628d4`.

| Alias | Baseline classification | Next Cloud outcome |
| --- | --- | --- |
| A | Partial | Delete Tencent Gateway Secret and independently converge asymmetric PV/PVC residue from exact persisted ownership |
| B | Source complete, Candidate evidence pending | Rerun memory/PostgreSQL Receipt projection gates and carry the identity into G |
| C | Partial | Add Delete Reconciler and explicit expired-Workspace renewal reactivation; retain exactly-once owner readback |
| D | Missing public capability | Add durable Registration Operation before exposing public route and Console form |
| E | Components exist | Prove the self-registered Account through zero balance, one top-up, exact-balance purchase, and replay |
| F | Incremental | Type only C/D/J paths as they change, then run one boundary audit |
| G | Candidate tooling exists | Produce one current clean Local receipt and consume the same digest's Instance receipt |
| H | Source complete, trigger gated | Do not reimplement lease; rerun capacity and Tencent evidence only after concurrency trigger |
| I | Source complete, integration verification pending | Preserve Basic/Pro and `balance >= quote` across D/E |
| J | Partial | Complete Registration/Delete/Reset recovery and one operator projection with bounded actions |
| K | Missing Cloud restore qualification | Prove three isolated database restores and owner readback; Instance owns production backup/restore receipt |
| L | Partial | Derive stable active/recovered signals from persisted owner state; Instance owns routing and on-call closure |
| M | Partial | Extend current auth/session controls to registration and data-exit/recovery boundaries; Instance owns public edge |
| N | P0 gap | Remove Release rebuild and promote only a receipt-admitted Candidate digest |

## Focused Completion Model

Every alias has its own definition of done:

| Alias | Focused completion evidence |
| --- | --- |
| A | Each exact single residue is detected and deleted; conflict/unknown performs no unrelated mutation |
| B | First Receipt projection succeeds, same value replays, conflicting value and identity drift fail, restart preserves it |
| C | Delete restarts to complete absence; Renewal reactivation restarts without duplicate debit, provider mutation, or Receipt |
| D | Concurrent same-email and response-loss requests converge to one Account/User/Sub2API identity with zero balance |
| E | Zero/insufficient balance has no side effects; one top-up and exact balance produce one Launch/debit/Receipt |
| F | Changed decoders reject unknown fields and illegal transitions while explicitly reading current persisted rows |
| G | Local and Instance receipts bind one canonical SHA/tree/index digest and qualified child digests |
| H | After a named trigger, same pool serializes, different pools run concurrently, and an expired lease recovers without duplicate scale |
| I | Catalog, quote, DTO, Console, and server admission return one package/price/eligibility decision |
| J | Every supported exceptional operation appears once with owner, blocker, allowed action, audit, and closure readback |
| K | Each database is seeded, dumped, restored in isolation, migrated, started, and read back through its owner API |
| L | Persisted blocking states emit stable redacted active/recovered signals that survive restart and close only on owner truth |
| M | Registration/login/session/CSRF/rate-limit/disable/isolation/data-exit tests pass; public TLS/Secrets remain Instance evidence |
| N | Release admission verifies Candidate plus required receipts, publishes the same digest, and contains no image rebuild step |

Focused tests are the package definition of done. `npm run verify:local` is the
ordinary merge regression gate. `npm run verify:local:full` is required at
PostgreSQL, schema, cross-module, and pre-Candidate joins. Neither aggregate
command replaces package-specific evidence.

## Shortest Execution Route

The executable graph is stored in
[2026-08-22-public-beta-a-n-execution-dag.mmd](./2026-08-22-public-beta-a-n-execution-dag.mmd).

### Wave 0: serialized reconciliation

1. Freeze the target, baseline SHA, A-N definitions of done, and Cloud/Instance
   authority boundary.
2. Rerun A/B/H/I focused checks against the baseline.
3. Close stale documentation claims before opening implementation branches.
4. Reserve the Control Plane and Fabric migration sequence numbers.

This stage is serialized because every later branch depends on the same truth.
It prevents already implemented H/I behavior from being rebuilt and prevents A
from being closed by tests that do not cover its known residuals.

### Wave 1: three parallel business lanes

| Lane | Ordered work | Primary write set |
| --- | --- | --- |
| Resource lifecycle | A -> C Delete | Fabric Tencent delete adapter, then Control Plane Delete worker/use case |
| Customer commerce | I verification -> D -> E -> M registration delta | Control Plane Account/Access and Console, then the cross-domain customer chain |
| Lifecycle/delivery preparation | C Renewal -> N offline admission preparation | Control Plane Renewal; then distribution contracts/tests with no Candidate mutation |

B verification feeds the E chain without a code branch. F is applied inside
each touched capability. Work within each lane is serial because later steps
consume the earlier domain behavior. The three lanes may run concurrently while
their cohesive source files are disjoint.

If two branches need the same Control Plane migration, `table_store.go`,
`server.go`, generated schema, or public contract revision, implementation may
continue in cohesive files but contract/migration ownership and final wiring
are serialized.

### Wave 2: three parallel product lanes after business join

| Lane | Ordered work | Reason |
| --- | --- | --- |
| Operations | F audit -> J -> L Cloud signals | L consumes J's final persistent operation classification |
| Durability | schema freeze -> K | A restore fixture is valid only after the schema set is stable |
| Distribution | N portable/exact-byte implementation -> G tooling verification | Both share Candidate/distribution contracts, so one lane owns them serially |

M's final security acceptance runs after D/E because only then does the real
public route and customer flow exist.

### Wave 3: serialized integration and evidence

1. Merge each branch into fresh canonical `main`, one writer at a time.
2. Run one cross-module `npm run verify:local:full` gate.
3. Freeze one source SHA/tree and build one Candidate index digest.
4. Qualify that digest through Local-Docker.
5. Hand the same digest to the Instance owner.
6. The Instance owner returns Tencent/TKE, backup/restore, alert/on-call,
   public-edge, and executed rollback receipts.
7. N admits those receipts and promotes the existing digest without rebuild.

Candidate creation is intentionally late. Any Candidate built before all Cloud
P0 changes join would be discarded, making its receipts wasted work.

### Exact-SHA Candidate bridge

The delegated Cloud operator does not currently satisfy the Candidate
workflow's canonical-`main` repository-owner dispatch gate. This does not
justify weakening the reusable workflow or rebuilding the Product on another
branch. Before a bridge is cut, N first lands the reusable Candidate workflow
on canonical `main` with exact `product_sha` and `product_tree` inputs, a fresh
remote-`main` HEAD equality check, and a checked-out tree equality check. After
the final Cloud SHA/tree is merged and read back from canonical `main`, the
operator may create one disposable `codex/candidate-<short-sha>-bridge` branch
with exactly one child commit.

That child commit may change only the Candidate workflow dispatch expression.
The expression binds the repository, full bridge ref, `actor`,
`triggering_actor`, the exact 40-character canonical Product SHA, and the exact
Product tree. The job still checks out the Product SHA rather than the bridge
commit, proves that fresh remote `main` has that exact HEAD, proves the
checked-out tree equals the input tree, archives only that tree, and records
the bridge workflow SHA separately as provenance. Therefore the bridge
authorizes a single build of canonical Product bytes without becoming a
Product source or release authority.

The bridge is rejected when any identity differs, is never merged into `main`,
and never authorizes formal Release. Its exact remote head is retained through
Release admission so the run and single-parent/single-diff shape remain
independently readable. It is deleted only after formal Release readback or
after the Candidate is explicitly abandoned. Candidate retries retain the same
Product SHA and produce a new attempt-specific manifest; only one selected
manifest/digest may enter Local and Instance qualification.

Qualification receipts are authority facts, not caller-supplied assertions.
Release admission receives immutable artifact or attestation locators and
fetches every Local, Instance, restore, alert/on-call, public-edge, and rollback
receipt from an allowlisted repository/workflow/ref or GitHub OIDC attestation.
It verifies the successful run, run attempt, actor and triggering actor,
protected environment where applicable, artifact ID and digest, workflow SHA,
and exact Candidate identity before validating the receipt body. A JSON value
provided directly by the Release dispatcher is never accepted as owner
evidence.

## Parallelism And Serialization Rules

Parallel work is permitted only when all are true:

- different bounded-context owner and reducer;
- disjoint exact write sets;
- no shared contract or schema revision;
- no dependency on the other branch's new persisted state;
- each branch can close with its own focused test.

Work is serialized when any are true:

- the same PostgreSQL schema/migration chain is modified;
- one machine contract and both consumers must change atomically;
- shared wiring such as `server.go` or `routes_admin.go` must be reconciled;
- one operation reducer/state transition has multiple proposed writers;
- canonical `main`, Candidate identity, Instance mutation, or Release state is
  being changed.

Different database owners may migrate in parallel. Two branches modifying the
same database owner's migration chain may not.

## Critical Path

```text
I verify
  -> D public Registration
  -> E register/top-up/purchase
  -> F changed-operation audit
  -> J recovery projection
  -> L Cloud signal closure
  -> fresh-main full gate
  -> G exact Candidate
  -> Instance external receipts
  -> N exact-byte Release
```

A -> C Delete and C Renewal run beside that path but must join before F/J is
closed. K and N's offline tooling run beside J/L after their prerequisites.
H remains outside the critical path until its named concurrency trigger exists.

This route is shortest because it reuses B/H/I evidence, builds no framework
without a current caller, maximizes independent Fabric/Identity/Renewal work,
and postpones expensive Candidate and Instance work until the bytes are final.

## Final Gates

`Cloud Complete` requires:

- every Cloud-owned focused acceptance passes at one exact canonical SHA;
- all touched public contracts have both consumer boundary tests;
- three-database restore compatibility passes;
- `verify:local:full` passes without PostgreSQL skips;
- Candidate and Release admission enforce exact identity and no rebuild.

`Public Beta Ready` requires, for the same Candidate:

```text
Cloud Complete
+ Local qualification receipt
+ Tencent/TKE qualification receipt
+ Instance backup/restore receipt
+ Instance alert/on-call exercise receipt
+ Instance public-edge receipt
+ Instance executed rollback receipt
+ Cloud formal publication/readback receipt
```

Missing external receipts leave G/K/L/M/N as `external_owner_pending`; Cloud
tests cannot promote them to production claims. Formal publication is the last
mutation and is authorized separately by the repository owner.
