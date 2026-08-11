# Contributing To OPL Cloud

OPL Cloud is a shared product and implementation repository. Keep changes small,
owner-aligned, and reviewable; do not create another source of current product,
implementation, instance, billing, or deployment truth.

## Source Of Truth

| Topic | Current owner |
| --- | --- |
| Product target and authority boundaries | `docs/architecture.md` |
| Current implementation boundary | `docs/implementation-architecture.md`, `docs/invariants.md`, machine contracts, source, tests, and runtime readback |
| Open gaps and next steps | `docs/roadmap.md` |
| Durable decisions | `docs/decisions.md` |
| Medopl instance profile and deployment evidence | `opl-instance-medopl` after extraction; co-located values here are migration state |

Issues, pull requests, discussions, and project boards are proposals or work
surfaces. They do not replace these owners.

When these surfaces disagree, first trace the conflict through Git history,
the canonical topic owner, real callers, and runtime evidence. Classify each
claim as target, current implementation, runtime/production evidence,
historical, stale, derived, or unknown; then update the canonical owner and
remove the duplicate current writer in the same pull request.

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
npm test
npm run typecheck
npm run lint
npm run build
(cd services/control-plane && go test ./... -count=1)
(cd services/fabric && go test ./... -count=1)
(cd services/ledger && go test ./... -count=1)
(cd services/internal/postgresmigrate && go test ./... -count=1)
git diff --check
```

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
