# Status

## Current Boundary

Current status is a Fast Invite-Only Paid Pilot candidate for 2-5 customer
accounts. It is code-complete through Task 10, but not production-proven and not
yet saleable.

Code-complete and locally tested:

- one Console User to one Account to one Sub2API User/Wallet identity hard cut,
  normalized-email verification, Sub2API password authority, and tenant Sessions;
- granular source DTOs for Auth, Wallet, Key list/status, Usage, Usage Stats,
  balance history, Workspace, Runtime readiness, and Ledger receipts;
- fixed Basic `52_580_000` and Pro `240_080_000` USD-micros monthly Workspace prices;
- one-submit durable Workspace launch with debit-before-provider recovery;
- one Workspace-level renewal operation, while enabling `autoRenew` remains blocked;
- PREPAID Tencent CVM/CBS request/readback, retained CBS expiry behavior, Runtime,
  owner-only credential commands, and account-scoped Gateway Secret handling;
- Ledger validation, receipts, reconciliation, and replay safety;
- dual Basic/Pro Provider Acceptance code and one-request Basic release live-QA;
- immutable Ready-Pod imageID checks, security boundaries, and grouped rollback.

Remaining blockers:

- Task 11 truth hard cut must land, then Task 12 must integrate the clean UI commit;
- the deploy workflow still injects retired `OPL_CONSOLE_USERS_JSON`, which the
  Control Plane now rejects; deployment identity cutover is pending;
- Task 13A Node, Go, four isolated PostgreSQL suites, Sentrux, and desktop/mobile
  source-truth QA have not run on one final SHA;
- Basic and Pro Provider Acceptance has not run and no real Tencent resource
  evidence exists for this candidate;
- no approved real renewal, production rollout, browser login/WebSocket, model
  request, exact-one Usage/wallet delta, or rollback evidence exists;
- public registration, payment/order UI, Key mutation, backup/recovery/sync/
  transfer, HA, GPU, and multiple Workspaces are outside the Pilot.

Workspace file bodies remain only on CBS. Platform PostgreSQL contains identity,
operation, reference, and audit facts only; PostgreSQL recovery does not back up
or restore Workspace files.

## Completion Gate

```bash
npm test
npm run typecheck
npm run build
(cd services/control-plane && go test ./...)
(cd services/fabric && go test ./...)
(cd services/ledger && go test ./...)
git diff --check
```

Production delivery additionally requires immutable image publication, both
retained Acceptance slots, one approved Basic live-QA request, bounded rollout
for all three services, source-truth readback, and the evidence defined by
`docs/invariants.md`.
