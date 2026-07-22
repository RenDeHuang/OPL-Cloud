# Status

## Current Boundary

Current status is the contract-frozen Pilot V2 implementation candidate for 2-5
invited customer accounts. Delivery evidence is currently `code-complete=false`,
`pilot-ready=false`, and `production-proven=false`; contract targets are not
runtime evidence and the product is not yet saleable.

The current V2 boundary requires:

- one Console User to one Account to one Sub2API User/Wallet, with Session-scoped
  delegated Gateway credentials;
- no browser Gateway base-address API, link, or Runtime override from Cloud;
  `OPL_SUB2API_BASE_URL` remains server-only;
- general Key create/update/delete/reveal and authoritative per-Key Usage;
- one primary Workspace purchase or renewal with exactly one USD-micros debit;
- compute, storage, attachment, Gateway Secret, and Runtime as fulfillment only;
- source envelopes whose availability and timestamps report real owner readback;
- operator wallet adjustment, resource facts, audit evidence, and announcements.

Remaining blockers:

- Provider Acceptance, Pro real subscription evidence, S9, and fixed-slot
  verification are paused; Pro is open in the production catalog but its
  real evidence remains `not_executed_by_scope` and `productionProven=false`;
- no approved real renewal, production rollout, browser login/WebSocket, model
  request, exact-one Usage/wallet delta, or rollback evidence exists;
- Runtime projects-entry and filesystem-usage product APIs are paused outside this
  release; Console does not display them and persistence is verified only with direct
  SHA256 markers on the Runtime Pod mounts;
- public registration, payment/order UI, backup/recovery/sync/transfer, HA, GPU,
  and multiple Workspaces are outside the Pilot.

Workspace file bodies remain only on CBS. Platform PostgreSQL contains identity,
operation, reference, and audit facts only; PostgreSQL recovery does not back up
or restore Workspace files.

## Preliminary Local Checks

```bash
npm test
npm run typecheck
npm run lint
npm run build
(cd services/control-plane && go test ./... -count=1)
(cd services/fabric && go test ./... -count=1)
(cd services/ledger && go test ./... -count=1)
(cd services/internal/postgresmigrate && go test ./... -count=1)
sentrux check .
git diff --check
```

These checks do not establish code-complete. The final gate additionally parses
Node TAP and Go JSON output, rejects every skip, runs all PostgreSQL suites with
`OPL_POSTGRES_TESTS=1`, and runs the Control Plane capacity suite with
`OPL_CAPACITY_TESTS=1`. Production PostgreSQL is forbidden for that gate.

`pilot-ready` additionally requires separately approved real environment
readback. `production-proven` requires the same immutable revision deployed and
an end-to-end production evidence bundle. The exact levels and commands are in
`docs/invariants.md` and the current Pilot V2 implementation plan.
