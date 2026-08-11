# Shared Execution Contract

Owner: `one-person-lab-cloud`
Purpose: `shared_execution_planning_contract`
State: `active_target_contract`
Machine boundary: Human-readable planning contract. It is not an executable
schema, implementation, job record, runtime state, or readiness proof.

This contract defines the common resource execution path for OPL App,
OPL Workspace, OPL Serve, OPL Fabric, OPL Console, and OPL Ledger.

The same contract should work for local App actions, cloud Workspace actions,
managed Docker or VM jobs, SSH or HPC jobs, GPU jobs, and later connector-backed
jobs or Agent Service invocations. MAS and other domain agents can use the same
contract as approved callers without routing every request through Console.

## Standard Flow

```text
plan -> approve -> execute -> monitor -> collect -> receipt
```

## Request

An execution request describes what the user wants to run and which resources
may be used.

Required fields:

- `request_id`
- `surface`: `app`, `workspace`, `serve`, `console`, or `domain_agent`
- `caller_ref`
- `actor`
- `workspace_ref` for App/Workspace-originated work
- `service_ref`, `revision_ref`, and `invocation_or_session_ref` for Serve work
- `goal`
- `resource_profile`
- `environment_ref`
- `input_refs`
- `expected_outputs`
- `approval_policy`

## Approval

Approval records what will be used before the job starts.

Required fields:

- `approval_id`
- `request_id`
- `approved_by`
- `approved_at`
- `allowed_compute`
- `allowed_storage`
- `allowed_connectors`
- `allowed_environment`
- `cost_policy`
- `expiry`

## Execution

Execution records the concrete job submitted through Fabric.

Required fields:

- `job_id`
- `request_id`
- `approval_id`
- `adapter`
- `command_or_action_ref`
- `environment_ref`
- `status`
- `started_at`
- `finished_at`
- `logs_ref`

## Collection

Collection records what was produced and where the outputs live.

Required fields:

- `job_id`
- `output_refs`
- `artifact_refs`
- `delivery_refs`
- `review_required`
- `cost_signal`

## Receipt

Every completed, failed, cancelled, or reviewer-blocked job should produce a
Ledger receipt. The receipt links the request, approval, execution, outputs,
review results, owner, and continuation entry.

## Surface Responsibilities

| Surface | Responsibility |
| --- | --- |
| OPL App | Starts local or user-provided resource work through the same plan and receipt path |
| OPL Workspace | Starts cloud workbench jobs and displays status, outputs, and receipts |
| OPL Serve | Submits exact Agent Revision invocations/sessions through Runway and projects endpoint status, events and outputs |
| OPL Console | Applies account/service policy, quota, approval, and billing rules for managed resources and exact package/revision refs |
| OPL Fabric | Selects adapters, prepares resources, runs jobs, and collects outputs |
| OPL Ledger | Records receipts, provenance, reviewer results, and continuation refs |
| Package owner | Owns Package identity, capabilities, entrypoints and exact publication revisions outside this resource-execution contract |
| Native carrier | Owns physical install, update, remove and fresh installed/callable readback |
| OPL Packages | Discovers owner descriptors, delegates configured-carrier actions and aggregates state without creating a parallel resolver, lock or currentness |
| OPL Runway | Owns Invocation/Session orchestration and execution-provider lifecycle for Serve work |
| Domain agent | Uses approved Fabric capabilities while keeping domain truth, quality judgment, and delivery authority |
