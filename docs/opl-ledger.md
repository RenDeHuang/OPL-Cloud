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
plan → approval → command/code → environment → input refs → output refs → reviewer result → owner → continuation entry
```

## What Ledger Owns

- Job receipts.
- Artifact provenance.
- Reviewer checks.
- Policy decisions.
- Agent Service revision, deployment, traffic and invocation/session refs.
- Workspace charge, refund, renewal, and resource reconciliation receipts.
- Audit records.
- Continuation refs.

For skill-first flows, Ledger should record which main skill, enhancement pack,
connector, input refs, selected sources, outputs, and continuation entry were
used. This gives MAS, Workspace, App, and other callers a shared evidence trail
without moving domain truth into Ledger.

For Serve flows, a receipt should connect exact package digest, service,
revision, deployment, consumer-policy, provider-session, resource, model-usage,
input, output, artifact, review, cost and continuation refs where applicable.
Ledger does not store provider secrets or become the canonical event/session
store.

Gateway remains the only spendable-balance owner, and Console remains the
account-total billing and settlement-policy surface. Ledger records immutable
evidence about money movements and resource charges; it does not maintain a
Fabric balance or a second mutable account balance.

## MVP Boundary

Core Ledger is limited to receipts and reconciliation evidence required by the
local Workspace plus Gateway accounting path. Artifact, Review, ReviewPolicy,
Continuation, broad retention, and cross-domain evidence experiences are
extensions; their existing code does not make them MVP prerequisites. Current
capability belongs to [status](status.md), while any keep, shrink, or later-work
decision belongs to the [roadmap](roadmap.md).

## Evidence Record View

Receipts should be useful to people, not only machines. A human-readable record
should answer:

- what was requested;
- who approved it;
- what ran;
- which inputs and environments were used;
- which artifacts were produced;
- which review checks ran;
- what the result was;
- who owns follow-up;
- where the work can continue.

## Retention And Continuation

Ledger should keep enough information to support audit, review, handoff, and
later continuation. Retention policy belongs to Console, while source data and
artifact storage remain with the owning storage or domain system.

## What Ledger Does Not Own

Ledger is not the file store, database, model provider, runtime scheduler,
connector owner, skill owner, or domain-quality authority. It records
references, receipts, and provenance from the owning systems.

## Reviewer Gate

Reviewer gates should stay domain-aware:

- MAS: citation, statistics, figure-code, and manuscript consistency.
- MAG: funder fit, eligibility, compliance, and budget fields.
- RCA: chart data source, transformation, and narrative consistency.
- BookForge: chapter continuity, citation coverage, style consistency, and
  export readiness.
