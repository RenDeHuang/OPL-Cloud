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

## Current Release Path

The release target is one exact multi-architecture Cloud Candidate qualified on
both Local-Docker and Tencent/TKE, followed by promotion of the same Cloud digest
as a formal Release.

| ID | State | Priority | Current gap | Owner | Acceptance |
| --- | --- | --- | --- | --- | --- |
| `LOCAL-WORKSPACE-LIFECYCLE-01` | `next` | `P0` | No exact-current clean Linux host receipt covers identity, quote/debit, create, readback, WebUI open, restart, permanent Delete, final resource/Key absence, zero Delete wallet mutation, and one deletion Receipt | Control Plane, Fabric, Ledger, Sub2API fixture, local installer | One Candidate completes that journey with owner readback |
| `PORTABLE-INSTALL-01` | `next` | `P1` | The environment template still selects a Workspace image, and portable source retains medopl domain/image fallbacks and provider-specific persisted metadata | Fabric adapter/profile owner, Control Plane routing, installation owner | The installer supplies immutable image, domain, and Provider Profile explicitly; missing inputs fail closed; Control Plane and Fabric consume one admitted Runtime host fact; migration of persisted installation facts has no permanent dual path |
| `PRODUCT-RELEASE-01` | `next` | `P0` | No Local-Docker/Instance receipt pair qualifies one current Candidate, and formal publication still rebuilds the image | Cloud Candidate and Release owner | One current SHA produces one Cloud index and portable bundle; both installations qualify that digest; formal publication verifies both receipts and promotes the existing bytes |
| `INSTANCE-MEDOPL-01` | `external_owner` | `P0` | The Instance lacks a same-Candidate receipt for generic admission, normal purchase, post-activation Runtime/provider/billing readback, executed rollback, and `workspace_verified` | `opl-instance-medopl` | Its protected workflow deploys the portable Candidate with explicit medopl inputs and returns all named readbacks for that exact digest |

## Product And Integrity Work

| ID | State | Priority | Current gap | Owner | Acceptance |
| --- | --- | --- | --- | --- | --- |
| `ACCEPTANCE-B-RETIREMENT-01` | `candidate` | `P1` | Retired Acceptance B routes, headers, environment inputs, fields, and persisted projections remain in current source | Control Plane and Instance configuration owners | Protected readback proves no configured consumer and no non-terminal persisted dependency; then remove the application path in one change while retaining historical data custody |
| `WORKSPACE-RENEWAL-01` | `planned` | `P1` | Auto-renew authorization exists, but expired-Workspace reactivation and a live exactly-once renewal path do not | Control Plane settlement, Sub2API, Fabric, Ledger | An authorized reactivation command and one exact Candidate prove debit, provider renewal/readback, Receipt, and recovery without duplicate money or resources |
| `WORKSPACE-PURCHASE-RECEIPT-PROJECTION-01` | `next` | `P1` | The succeeded Launch Operation and Ledger retain the exact purchase Receipt ID, but Receipt success does not populate the Workspace `purchase_receipt_id` projection; billing evidence and operator DTOs read that projection and can omit the Receipt. Permanent Delete is not blocked because it validates the succeeded Launch Operation against Ledger directly | Control Plane Workspace projection/read-model owner and Ledger client boundary | Receipt recovery appends or reads exactly one Ledger Receipt, projects the same ID without repeating Debit, provider, or Activation mutations, billing/operator read models expose that Receipt, and a focused regression proves legacy Workspaces with an empty projection still delete through Launch Operation plus Ledger authority |
| `LOCAL-WORKSPACE-RECOVERY-01` | `planned` | `P2` | Tests cover unknown debit/provider results, but no exact-current live run proves convergence and bounded cleanup | Control Plane Launch recovery, Fabric readback, Ledger review | One interrupted Launch converges from persisted identity and owner readback; manual review is explicit and cleanup touches only exact owned resources |
| `TENCENT-COMPUTE-POOL-CONCURRENCY-01` | `planned` | `P2` | The typed Tencent Workspace Launch path has durable parent/child operations, ownership CAS, and provider readback, but it does not use the retained Fabric compute-pool head/lease path; the current Pilot in-flight limit is not proof for concurrent Launches against one NodePool | Fabric Tencent compute owner and Instance qualification owner | A focused same-NodePool concurrency test and protected Instance receipt prove one bounded scale/claim decision, distinct Workspace ownership, and no duplicate CVM/TKE mutation |
| `TENCENT-PARTIAL-APPLY-RECOVERY-01` | `planned` | `P2` | Strict readback fails closed when a Tencent/TKE PV/PVC, Secret, or Runtime multi-object apply is partial, but no current qualification receipt proves exact diagnosis and bounded cleanup of those retained resources | Fabric Tencent adapter and Instance operations owner | Injected partial applies enter explicit review, identify only resources bound to the exact operation, and either converge by readback or execute owner-authorized bounded cleanup with a receipt |
| `FABRIC-OPERATION-HISTORY-01` | `verify` | `P2` | Bounded lookup, heartbeat reuse, and pagination are in source, but the earlier external finding has no fresh sealed scan against current canonical source | Fabric and repository security owner | Focused persistence/HTTP/caller tests pass and a fresh scan no longer reports the operation-history exhaustion path |
| `SECRET-VALIDITY-SETTING-01` | `external_owner` | `P2` | GitHub still reported secret validity checks disabled after an attempted setting change | Repository owner and GitHub feature availability | GitHub readback reports enabled, or the owner records that the feature is unavailable for this repository |

## Later Product Scope

Self-service onboarding and payment, customer Suspend/Resume, managed resource
policy, project/artifact continuation, connectors, Package projection, Serve,
and shared Runway integration remain target capabilities. They enter the active
table only when the product owner selects an outcome and a current caller exists.

They are not prerequisites for the current portable Workspace Release.

## Completion Evidence

- Cross-module changes update the owning public contract and both consumers.
- Local qualification and Instance qualification name the exact Candidate SHA
  and multi-architecture digest they exercised.
- Formal publication promotes that digest without a rebuild.
- Money, Secret, persisted-data, provider-resource, and production claims close
  only from their authoritative owner and readback surface.
