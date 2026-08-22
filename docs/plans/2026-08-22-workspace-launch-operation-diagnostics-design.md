# Workspace Launch Operation Diagnostics Design

## Objective

Expose a read-only, redacted classification for `workspace.launch.v2` runtime
operations rejected by the current reconcile decoder. The evidence must identify
the decoder failure class without changing Controlled Basic Pilot admission,
accepting legacy state, or exposing operation, Account, or Workspace identities.

## Ownership

- Primary module owner: `services/control-plane`.
- Runtime truth owner: Control Plane `runtime_operations` and the current
  `decodeWorkspaceLaunchReconcileOperation` implementation.
- Consumer: protected Instance diagnostics. Instance admission remains a strict
  consumer of the existing `controlledBasicPilot` contract.

## API Shape

`GET /api/operator/health` gains a sibling source envelope named
`workspaceLaunchOperationDiagnostics`. It is not nested inside
`controlledBasicPilot`, because Instance validates the Pilot data with an exact
key contract.

The diagnostic data contains a schema version, a failed operation count, and a
bounded list of failed-operation summaries. Each summary contains only:

- an SHA-256 digest of the operation identity;
- action, status, and persisted timestamps;
- decoded scalar metadata when safely available: schema version, version, stage;
- recognized launch-stage attempt keys, total count, and fixed-enum status counters;
- recognized launch-stage observation keys, total count, and fixed-enum state counters;
- missing canonical keys and forbidden legacy keys;
- the exact decoder failure category.

It never contains the operation ID, Account ID, Workspace ID, complete result,
canonical fact values, continuation authorization identifiers, provider IDs,
credentials, arbitrary persisted strings, or arbitrary error messages. Persisted
timestamps are projected only when they parse as RFC3339, and operation status
is projected only when it belongs to the known current or legacy launch status
set.

## Decoder Classification

The existing decoder remains strict. Rejected operations still unwrap to
`invalid_workspace_launch_operation`; callers therefore keep their existing
fail-closed behavior. Internally, rejection sites attach a stable category such
as:

- `empty_result`
- `invalid_json`
- `schema_version_mismatch`
- `invalid_version`
- `invalid_stage`
- `missing_attempts`
- `invalid_attempts`
- `invalid_observations`
- `invalid_resume_authorization`
- `invalid_continuation_claim`
- `forbidden_legacy_fields`
- `missing_canonical_facts`
- `row_identity_mismatch`
- `status_stage_mismatch`

The health projection reads the category from the actual decoder error rather
than running a second permissive decoder.

## Safety Behavior

- No database writes or provider calls are introduced.
- `controlledBasicPilot.failures.operation_decode_failed` remains unchanged.
- `disableRequired` remains true while any operation fails decoding.
- Legacy schema 2 is not accepted or migrated by this change.
- The diagnostic list is bounded to avoid turning the health route into an
  unbounded data-export surface.

## Verification

Focused tests prove that:

- representative decoder failures receive the expected category while still
  matching `invalid_workspace_launch_operation`;
- the health response preserves the exact existing Pilot shape;
- the new sibling envelope contains only redacted metadata;
- raw identities and result values do not appear in the serialized response;
- valid operations do not appear in the failure list.
