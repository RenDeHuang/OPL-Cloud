# Ledger Receipt Schema

Owner: `one-person-lab-cloud`
Purpose: `ledger_receipt_planning_schema`
State: `active_target_contract`
Machine boundary: Human-readable target shape. It is not a registered machine
schema, persisted receipt, runtime readback, domain verdict, or owner receipt.

This document defines the minimal receipt shape for OPL Ledger planning and
implementation.

Receipts are evidence records. They point to source systems, artifacts, logs,
and review results instead of copying all underlying data into Ledger. For
skill-first flows, receipts can point to the main skill, enhancement packs,
Connect adapter, source refs, and selected outputs without making Ledger the
domain source of truth.

## Minimal JSON Shape

```json
{
  "receipt_id": "ledger.receipt.example",
  "schema_version": "0.1",
  "surface": "serve",
  "actor": "user-or-service-ref",
  "caller_ref": "app-workspace-serve-console-or-domain-agent-ref",
  "workspace_ref": "publisher-workspace-ref-or-null",
  "service": {
    "service_ref": "agent-service-ref-or-null",
    "revision_ref": "agent-revision-ref-or-null",
    "deployment_ref": "deployment-ref-or-null",
    "invocation_or_session_ref": "invocation-or-session-ref-or-null",
    "consumer_principal_ref": "consumer-principal-ref-or-null"
  },
  "job_ref": "fabric-job-ref",
  "request": {
    "goal": "human-readable goal",
    "input_refs": [],
    "expected_outputs": []
  },
  "approval": {
    "approved_by": "owner-ref",
    "approved_at": "2026-07-01T00:00:00Z",
    "policy_ref": "policy-ref"
  },
  "execution": {
    "adapter": "docker",
    "environment_ref": "environment-ref",
    "command_or_action_ref": "action-ref",
    "status": "completed",
    "started_at": "2026-07-01T00:00:00Z",
    "finished_at": "2026-07-01T00:00:00Z",
    "logs_ref": "logs-ref"
  },
  "outputs": {
    "artifact_refs": [],
    "delivery_refs": []
  },
  "review": {
    "required": false,
    "reviewer_gate_ref": null,
    "result": "not_required"
  },
  "cost": {
    "meter_refs": [],
    "estimated_cost": null
  },
  "owner": {
    "owner_ref": "owner-ref",
    "responsibility": "review-and-continuation"
  },
  "continuation_ref": "continuation-ref"
}
```

## Status Values

- `planned`
- `approved`
- `running`
- `completed`
- `failed`
- `cancelled`
- `review_required`
- `review_blocked`

## Reviewer Result Values

- `not_required`
- `passed`
- `needs_human_review`
- `blocked`
- `waived_by_owner`

## Boundary

Ledger receipts support review, audit, handoff, continuation, and later
reproduction. Runtime truth still belongs to the owning runtime, storage truth
belongs to the owning storage system, and domain-quality truth belongs to the
responsible domain owner or reviewer. Ledger records provenance and receipts;
it does not replace MAS, ScholarSkills, or another domain owner.

For Serve receipts, Service/Revision/Deployment truth remains with OPL Serve and
Invocation/Session truth remains with OPL Runway. Provider session identifiers,
events and costs are refs to owning systems, not Ledger-owned runtime state.
