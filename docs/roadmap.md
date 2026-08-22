# OPL Cloud Roadmap And Current Gaps

Owner: `one-person-lab-cloud`
Purpose: `single_active_gap_and_priority_owner`
State: `active_planning`

This file contains only open outcomes. Current evidence belongs in
[status.md](./status.md); architecture and durable decisions belong in
[architecture.md](./architecture.md) and [decisions.md](./decisions.md).

## Priority

`P0` blocks the current portable Workspace Release. `P1` is the next accepted
product or integrity outcome. `P2` is deferred until its named trigger exists.
An `external_owner` item proceeds in that owner's repository.

## Public Beta Work Packages

The accepted delivery target is public registration with zero initial balance,
administrator top-up, controlled Workspace purchase, complete lifecycle
recovery, and one portable Candidate qualified without changing its bytes. The
letters A-N are stable portfolio aliases; the IDs below are the canonical gap
identities.

| Alias / ID | State | Priority | Cloud owner and current gap | Focused acceptance | External completion |
| --- | --- | --- | --- | --- | --- |
| A / `PB-A-FABRIC-DELETE-01` | `active` | `P0` | Fabric resource lifecycle; typed Runtime residue observation exists, but Tencent Gateway Secret deletion and asymmetric PV/PVC residue do not yet converge through exact persisted ownership | Fabric adapter tests inject each single residue, prove exact-owner deletion and final `absent`, and reject unknown/conflict without unrelated mutation | Same-Candidate Tencent/TKE protected deletion readback |
| B / `PB-B-WORKSPACE-RECEIPT-01` | `verify` | `P0` | Control Plane Workspace projection; source atomically projects the Launch Receipt, but no exact-current Candidate receipt proves restart and customer readback | PostgreSQL CAS tests prove first projection, same-Receipt replay, conflicting Receipt rejection, identity mismatch rejection, and restart readback | Local and Instance purchase receipts name the same projected Ledger Receipt |
| C / `PB-C-LIFECYCLE-RECONCILERS-01` | `planned` | `P0` | Control Plane Workspace lifecycle; Renewal worker exists, but expired reactivation/live exactly-once evidence and a service-authorized Delete reconciler are missing | Separate Renewal and Delete tests prove claim/lease/CAS, restart recovery, one debit/provider mutation/Receipt, full Delete absence, and explicit manual review | Instance runs the same Candidate through bounded Renewal and Delete readback |
| D / `PB-D-PUBLIC-REGISTRATION-01` | `next` | `P0` | Control Plane Account and Access plus Console; no public route, durable Registration operation, or Sub2API identity convergence exists | Concurrent same-email requests create one User, Account, and Sub2API identity; response loss resumes without raw password persistence; initial balance is zero and registration performs no purchase/Fabric mutation | Instance exposes the admitted public route behind its ingress |
| E / `PB-E-REGISTER-TOPUP-PURCHASE-01` | `planned` | `P0` | Control Plane admission and settlement; administrator top-up, quote, balance, and Launch exist but are not proven for a self-registered Account | Focused chain proves zero/insufficient balance has zero debit/provider writes, one audited top-up, exact-balance purchase, and idempotent replay without duplicate money/resources | Protected Instance test funds only its bounded beta identity |
| F / `PB-F-TYPED-OPERATIONS-01` | `active` | `P1` | Each Control Plane/Fabric bounded context owns its reducer; live changed paths still contain generic maps/strings and retired Acceptance B surfaces | Per-capability decoder/reducer tests reject illegal transitions and preserve current persisted data; current consumers are switched before a retired path is removed | Instance readback must prove no configured or non-terminal Acceptance B consumer before Cloud removes that path |
| G / `PB-G-EXACT-CANDIDATE-01` | `next` | `P0` | Cloud Candidate owner; no exact-current clean Local and Tencent/TKE receipt pair exists for one SHA/tree/index digest | Candidate tooling validates canonical manifest, bundle checksums, both platform digests, and one complete clean Local PostgreSQL business journey | `opl-instance-medopl` qualifies the same digest and returns provider/runtime/billing readback |
| H / `PB-H-NODEPOOL-LEASE-01` | `later` | `P2` | Fabric capacity; durable PostgreSQL NodePool FIFO claim/lease and fencing exist, while current admitted concurrency remains one | Triggered only by `maxInFlight > 1`, concurrent Acceptance B capacity, or another proven same-pool caller; then rerun memory/PostgreSQL serialization and lease-recovery tests before bounded Tencent concurrency qualification | Tencent concurrency qualification only after the trigger exists |
| I / `PB-I-PRODUCT-POLICY-01` | `verify` | `P0` | Control Plane and Console source now agree on Basic/Pro admission and `balance >= quote`; the public-beta chain must preserve that decision | Existing catalog, quote, customer DTO, Console, and server admission focused tests pass unchanged after D/E integration | Instance profile selects only Cloud-admitted plans and cannot override price |
| J / `PB-J-OPERATIONS-RECOVERY-01` | `active` | `P0` | Control Plane Operations and Recovery; Launch/Renewal review exists and disposable reset preview is read-only, while Registration/Delete/Reset recovery and unified closure evidence are incomplete | Every supported manual-review operation appears once with owner, blocker and allowed action; recovery is idempotent, owner-read, audited, and cannot write another owner's state | Protected Instance operator executes only Cloud-authorized commands and retains receipts |
| K / `PB-K-DATA-RECOVERY-01` | `planned` | `P0` | Cloud defines schema/migration and restore validation obligations; it does not operate production backups | Cloud restore validator proves Control Plane, Fabric, and Ledger identities, operations, bindings, and receipts after an isolated restore | Instance owns encrypted schedules, retention, RPO/RTO, restore execution, and restore receipt |
| L / `PB-L-ALERT-OPERATIONS-01` | `planned` | `P0` | Cloud service owners expose stable failure/recovery signals; current alerts are mainly process logs and Console projection | Fault tests emit stable active/recovered signals for worker, manual review, database, provider, Ledger, backup-readiness, and purchase-stop states | Instance routes signals to an external receiver and proves acknowledge, stop-purchase, recover, readback, close |
| M / `PB-M-PUBLIC-BOUNDARY-01` | `planned` | `P0` | Control Plane owns auth/session/account lifecycle, Console owns public UI, and portable assets own ingress requirements; public registration abuse and data-exit paths are incomplete | Auth tests prove rate limits, non-enumerating failures, CSRF/session boundaries, disable revocation, no-store secret reads, and operator-assisted recovery/data exit | Instance owns TLS, domains, ingress, production Secrets, and public-browser receipt |
| N / `PB-N-DISTRIBUTION-RELEASE-01` | `next` | `P0` | Cloud distribution owner; portable defaults still carry installation facts and formal Release still rebuilds instead of promoting qualified bytes | Clean install requires explicit immutable Workspace image/domain/Profile; publication verifies both qualification receipts and promotes the exact Candidate digest with matching assets/checksums | Instance deploys and rolls back the exact digest, retaining deployment and rollback receipts |

F is not a preliminary global rewrite. Each live capability tightens its own
types while implementing the business outcome. H remains out of the critical
path until its named trigger exists.

## Execution And Test Policy

- Independent Account, Commerce, and Lifecycle owners may develop in parallel
  in separate worktrees with disjoint write sets.
- A shared public contract, schema migration, generated projection, canonical
  `main`, Candidate identity, or publication mutation has one serialized writer.
- Each work package closes first with domain, application, persistence, and
  boundary focused tests owned by that capability.
- `npm run verify:local` remains the ordinary merge regression gate.
  `npm run verify:local:full` runs at persistence/cross-module integration
  checkpoints and before Candidate construction, not as a substitute for the
  work package's focused acceptance.
- Cloud can mark G/K/L/M/N only `cloud_complete` while their required Instance
  receipts remain external. Public-beta readiness requires both layers.

## Deferred Product Scope

Customer-operated payment/top-up, shared multi-user Workspaces, customer
Suspend/Resume, HA, GPU, managed-resource policy, project/artifact continuation,
connectors, Package projection, Serve, and shared Runway integration are not
public-beta prerequisites.

## Independent Deferred Integrity Outcomes

| ID | State | Priority | Current gap | Owner | Acceptance |
| --- | --- | --- | --- | --- | --- |
| `FABRIC-OPERATION-HISTORY-01` | `verify` | `P2` | Bounded lookup, heartbeat reuse, and pagination are in source, but the earlier external finding has no fresh sealed scan against current canonical source | Fabric and repository security owner | Focused persistence/HTTP/caller tests pass and a fresh scan no longer reports the operation-history exhaustion path |
| `SECRET-VALIDITY-SETTING-01` | `external_owner` | `P2` | GitHub still reported secret validity checks disabled after an attempted setting change | Repository owner and GitHub feature availability | GitHub readback reports enabled, or the owner records that the feature is unavailable for this repository |

## Completion Evidence

- Each A-N item records its owner, exact Cloud SHA, focused tests, persistence or
  typed-boundary readback, remaining external receipt, and terminal status.
- Cross-module changes update the owning public contract and both consumers.
- Local and Instance qualification name the exact Candidate SHA and
  multi-architecture digest they exercised.
- Formal publication promotes that digest without a rebuild.
- Money, Secret, persisted-data, provider-resource, and production claims close
  only from their authoritative owner and readback surface.
