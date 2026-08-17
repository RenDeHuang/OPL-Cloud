# OPL Ledger

Owner: `one-person-lab-cloud`
Purpose: `ledger_target_reference`
State: `active_target_reference`
Machine boundary: Human-readable target evidence model. It is not a receipt
store, runtime ledger, source database, quality verdict, owner receipt, or
production-readiness source.

OPL Ledger is the target evidence-record capability for OPL Cloud work.

It records what happened, which inputs and environments were used, which outputs
were produced, what checks ran, and how the work can be reviewed or continued
later.

Ledger records receipts and provenance. It does not replace the domain source
of truth, domain-quality judgment, or delivery authority owned by MAS, MAG, RCA,
BookForge, OPL App, or another domain owner.

## Receipt Shape

Every meaningful App action, Workspace action, Serve deployment or invocation,
or Cloud-managed job should be able to leave a receipt:

```text
plan → approval → command/code → environment → input refs → output refs → reviewer result → owner → continuation ref
```

## What Ledger Owns

- Append-only job and Workspace receipts.
- Reconciliation evidence and idempotency for Ledger writes.
- Receipt retention and privacy lifecycle operations.
- Opaque provenance fields supplied by the calling owner, including artifact,
  review, output, reviewer-check, and continuation refs.

For skill-first flows, Ledger should record which main skill, enhancement pack,
connector, input refs, selected sources, outputs, and continuation entry were
used as opaque provenance. This gives MAS, Workspace, App, and other callers a
shared evidence trail without moving domain truth or continuation authority into
Ledger.

For Serve flows, a receipt should connect exact package digest, service,
revision, deployment, consumer-policy, provider-session, resource, model-usage,
input, output, artifact, review, cost and continuation refs where applicable;
Ledger persists these as caller-owned provenance and does not interpret them.
Ledger does not store provider secrets or become the canonical event/session
store.

Gateway remains the only spendable-balance owner, and Console remains the
account-total billing and settlement-policy surface. Ledger records immutable
evidence about money movements and resource charges; it does not maintain a
Fabric balance or a second mutable account balance.

## MVP Boundary

Core Ledger is limited to receipts, reconciliation evidence, idempotency, and
receipt lifecycle operations required by the local Workspace plus Gateway
accounting path. The structured Artifact, Review, ReviewPolicy, ReviewGate, and
Continuation APIs have been retired. Their historical receipts, the historical
`review_policies` table, and receipt provenance columns remain readable or
retained for data integrity; no new structured writes, continuation identity
generation, or Workspace authorization is provided. Current capability belongs
to [status](status.md), while any later owner decision belongs only to the
[roadmap](roadmap.md).

## Evidence Record View

Receipts should be useful to people, not only machines. A human-readable record
should answer:

- what was requested;
- who approved it;
- what ran;
- which inputs and environments were used;
- which artifact refs were supplied;
- which review-check refs were supplied;
- what the result was;
- who owns follow-up;
- where the work can continue.

## Retention And Continuation

Ledger keeps enough receipt data to support audit, handoff, and later owner-side
review. Retention policy belongs to the receipt lifecycle owner, while source
data and artifact storage remain with the owning storage or domain system.
`continuationId` and `continuation` are caller-supplied opaque provenance:
Ledger does not generate an identity, resolve a continuation, hide it on reads,
or authorize a Workspace operation.

## What Ledger Does Not Own

Ledger is not the file store, database, model provider, runtime scheduler,
connector owner, skill owner, or domain-quality authority. It records
references, receipts, and provenance from the owning systems.

## Review And Domain Ownership

Review decisions and gates stay with the domain owner that understands them.
Ledger records only opaque `reviewId`, `reviewerChecks`, and related refs; it
does not own review policies, evaluate gates, or turn a review result into
Workspace authorization.

Examples of domain-owned review semantics:

- MAS: citation, statistics, figure-code, and manuscript consistency.
- MAG: funder fit, eligibility, compliance, and budget fields.
- RCA: chart data source, transformation, and narrative consistency.
- BookForge: chapter continuity, citation coverage, style consistency, and
  export readiness.
