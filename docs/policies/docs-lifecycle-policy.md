# Documentation Lifecycle Policy

This repository applies the OPL Doc method through the hierarchy in
`docs/README.md`. The policy governs semantic ownership; it does not prescribe
one file layout for every topic.

## One Topic, One Current Owner

Classify each changed section as `current_truth`, `active_gap`,
`support_detail`, `history_or_provenance`, or `stale_or_conflicting`.

- Keep one current owner for each semantic topic.
- Reduce other active documents to a pointer plus unique support detail.
- Put all open work in `docs/roadmap.md`; put current evidence in
  `docs/status.md`.
- Move dated plans, design freezes, screenshots, execution logs, raw
  verification output and completed ledgers to `docs/history/**` or rely on Git
  history when no no-resurrection record is needed.
- Delete stale or conflicting text after its successor and callers are proven.

## Downward Reconciliation

An upper-level change must identify affected lower projections. Reconcile them
in the same change when possible. If implementation cannot yet follow a target
decision, preserve the target, report the current implementation honestly and
record one roadmap gap. Never weaken the target to match accidental current
code, and never claim the target is implemented from prose alone.

## Machine Contract Admission

Machine-readable contracts live in `packages/contracts/**`. A fact belongs
there only when all of the following are true:

1. It has one named authority owner.
2. A current runtime, cross-module caller, public interface, security rule,
   data-integrity rule or irreversible side effect depends on it.
3. Deterministic validation is more valuable than leaving the decision to the
   owning implementation.
4. The contract does not duplicate a schema, source constant, workflow or
   another contract that already owns the fact.

Colors, spacing, layout dimensions, page or slide counts, component libraries,
model choices, query strategies, batch sizes, concurrency tuning, worker
intervals, file paths, command sequences, current progress and pending evidence
do not qualify merely because tests can assert them. Use an evolvable guide,
source, API schema, performance test, workflow, `docs/status.md` or
`docs/roadmap.md` instead.

## Tests

Tests are classified by lifecycle:

- `long_term_contract`: domain or API behavior that must remain true.
- `migration_guard`: temporary guard for a one-time data or API migration.
- `cleanup_guard`: short-lived guard used while deleting legacy paths.
- `implementation_shape`: source-structure or workflow-shape check that must either become a contract test or be retired.

Temporary tests need an owner and removal condition.

Long-term tests should validate public behavior, authority, accessibility,
security, integrity and side-effect bounds. They must not turn a replaceable UI
or internal implementation choice into a product freeze.

## No Compatibility Layer Rule

This repository should retire old wrappers directly once active callers move to the current surface.

Allowed:

- one-time migration code that upgrades persisted state;
- history notes explaining retired paths;
- contract tests for the new surface.

Not allowed as long-term state:

- duplicate API routes kept only for old callers;
- account/user wallet mirror writes;
- compatibility aliases in contracts;
- tests that assert old paths still work after the migration is complete.
