# Contributing To OPL Cloud

OPL Cloud is a shared product and implementation repository. Keep changes small,
owner-aligned, and reviewable; do not create another source of current product,
implementation, instance, billing, or deployment truth.

## Source Of Truth

| Topic | Current owner |
| --- | --- |
| Product target and authority boundaries | `docs/architecture.md` |
| Current implementation boundary | `docs/implementation-architecture.md`, `docs/invariants.md`, machine contracts, source, tests, and runtime readback |
| Open gaps, simplification candidates, priority and concurrent lanes | `docs/roadmap.md` |
| Durable decisions | `docs/decisions.md` |
| Medopl instance profile and deployment evidence | initialized `opl-instance-medopl`; co-located values and stale repository identity remain migration work |

Issues, pull requests, discussions, and project boards are proposals or work
surfaces. They do not replace these owners.

When these surfaces disagree, first trace the conflict through Git history,
the canonical topic owner, real callers, and runtime evidence. Classify each
claim as target, current implementation, runtime/production evidence,
historical, stale, derived, or unknown; then update the canonical owner and
remove the duplicate current writer in the same pull request.

## Choosing Work

1. Read the ranked functional and simplification tables in `docs/roadmap.md`,
   then check open pull requests for an existing owner. The roadmap owns intent,
   priority and acceptance; the pull request owns the live execution attempt.
2. Select one gap ID and keep its owner/write set explicit in the pull request.
   Multiple rows may proceed concurrently; priority ranks urgency and benefit,
   not a global dependency queue. Do not combine Console, provider, billing,
   deployment and cleanup changes unless one acceptance path requires them.
3. A simplification row in `candidate` state authorizes read-only caller,
   target-contract, persisted-data and external-consumer admission only. Promote
   it in the roadmap before deleting a public route, schema or stored model.
4. Read the physical dependency map in `docs/implementation-architecture.md`.
   Cross-service behavior uses typed HTTP contracts. Runtime Go imports between
   Control Plane, Fabric and Ledger are forbidden; the narrow PostgreSQL
   migration helper is the only shared Go module.
5. Rebase or update from fresh `main` before review. If another pull request has
   entered the same write set, coordinate ownership or split at a contract/API
   boundary before continuing.

Use `parallel_work_serialized_integration`: develop independent module lanes in
parallel and serialize only an overlapping write set, one shared contract
revision, canonical `main`, or a real production mutation. A production or
instance readback is a qualification gate for that exact release, not a reason
to block unrelated development, CI, or preview work. Reusable deployment code
and instance-specific application may progress independently and converge at
deployment qualification.

## Branch And Pull Request Flow

1. Start one short-lived branch or worktree from fresh `origin/main` for one
   objective. Use `codex/<objective>` for Codex-authored branches.
2. Keep the write set narrow. Separate unrelated UI, contract, billing, auth,
   runtime, infrastructure, and documentation work.
3. Open a pull request to `main` and complete the repository template.
4. Update the branch to current `main` and resolve every review conversation.
   Human review is risk-based and may be requested, but is not a universal merge
   gate for either active developer.
5. Merge only after the required `validate` check succeeds. Delete the branch
   after merge.

Direct pushes and force pushes are not the normal path. An administrator may
bypass the PR path only for a time-critical repository or production recovery, and
must leave the reason and final readback in a pull request or incident record.

Before editing, name one primary module: Console UI, Control Plane, Fabric,
Ledger, contracts, or shared infrastructure. Cross-service behavior uses typed
public HTTP contracts. Do not import sibling service source, access sibling
tables, deep-import service code from Console UI, copy state machines/DTOs, or
create a shared package for one caller. If a change truly crosses modules, name
the owning contract and update both sides and their focused tests together.

## Validation

The required `validate` check aggregates four parallel jobs:

- `node-console`
- `postgres-ledger`
- `control-plane`
- `fabric`

Keep `validate` as the single branch-protection context. Do not add path-based
skip logic, a merge queue, or a second aggregate check without measured queue or
runtime evidence that the current roughly three-minute gate is a real blocker.

Run the checks affected by your change before pushing. The repository-wide
baseline is:

```bash
npm run verify:local
```

For persistence, capacity, local-Docker, or cross-service behavior changes, run
`npm run verify:local:full`. It starts an ephemeral PostgreSQL 16 container and
requires the PostgreSQL, capacity, and local-Docker integration tests to finish
without skips before removing the container. Both local gates remain source
verification only; they do not access production or replace the required hosted
`validate` check.

Documentation-only changes still run the shared PR gate. This keeps one stable
required context and avoids a second change-classification authority.

## Evidence And Production

- A local pass or green PR proves only the checked implementation revision.
- Do not report `pilot-ready` or `production-proven` without the matching
  immutable deployment and owner-authoritative readback.
- Production deployment and private-network verification run only through the
  approved GitHub Actions workflows and `production` environment.
- Never add secrets, customer data, raw provider responses, or mutable local
  configuration to a pull request.
- Treat Dependabot pull requests as code changes. Major Action upgrades that
  touch production workflows require deliberate contract-test updates and
  explicit production-risk review; they are not auto-merged.
