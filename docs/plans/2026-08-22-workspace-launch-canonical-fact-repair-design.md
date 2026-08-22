# Workspace Launch Canonical Fact Repair Design

## Decision

Candidate A restores strict readability for historical `workspace.launch.v2`
operations that were created as schema 3 before `specDigest` became a required
canonical fact.

The repair is deliberately limited to the observed compatibility defect:

- `schemaVersion` is 3;
- the strict decoder failure is `missing_canonical_facts`;
- the only missing canonical key is `specDigest`;
- no forbidden legacy key exists;
- the operation is `stage=debit`, `status=manual_review`;
- Fabric can prove one persisted preflight binding and its original digest.

Candidate A does not clear `manual_review`, resume the launch, terminate the
operation, debit an account, or create any provider resource. Candidate B owns
business-state convergence after Candidate A has restored strict decoding.

## Ownership

Primary owner:

```text
services/control-plane
  workspace launch
    runtime operation recovery
      canonical fact repair
```

Supporting owner:

```text
services/fabric
  workspace launch
    preflight binding
      authoritative readback
```

Instance remains the protected production executor and evidence collector. It
does not interpret Fabric records, patch PostgreSQL, or relax its validator.

## Fabric Readback

Fabric adds a typed, read-only endpoint at the same level as the existing
workspace-launch preflight and stage endpoints:

```text
POST /fabric/workspace-launches/preflight/read
```

The request contains only the opaque `providerBindingRef`. Fabric point-reads
the operation whose ID is the binding reference and runs the existing strict
`decodeWorkspaceLaunchPreflight` validation. It returns the persisted identity
facts and `specDigest`; Control Plane then requires every returned identity fact
to match the damaged operation before constructing a repair. The endpoint does
not expose the canonical provider plan.

The endpoint must not call `ResolveWorkspacePlan`, provider readiness, Tencent,
or any mutation method. Current provider configuration is not authoritative for
a historical binding.

## Control Plane Preview

Control Plane adds an operator-protected endpoint alongside workspace-launch
resume and runtime repair:

```text
GET /api/operator/workspace-launches/{operationId}/canonical-facts-repair-preview
```

The implementation lives in
`workspace_launch_canonical_fact_repair.go`, not in admission, diagnostics, or
the Fabric adapter.

Preview performs these checks:

1. Parse the persisted result narrowly enough to classify the known defect,
   without creating a compatibility decoder.
2. Require the exact eligibility conditions listed in the Decision section.
3. Read the persisted Fabric preflight binding through the typed client.
4. Match launch operation, account, workspace, package, size, image, request
   hash, provider profile, and binding identities exactly.
5. Produce a candidate row by adding `specDigest` and incrementing `version`.
6. Require the current strict schema 3 decoder to accept the candidate row.
7. Require the semantic diff to be exactly `specDigest` plus `version`.
8. Return a canonical `previewDigest` that binds the current row result,
   expected version, verified Fabric facts, and proposed change.

Preview is read-only and reports `mutationBudget=0`.

## Control Plane Apply

Control Plane adds:

```text
POST /api/operator/workspace-launches/{operationId}/canonical-facts-repair
```

The request requires one `Idempotency-Key` and exact JSON fields:

```json
{
  "launchVersion": 5,
  "previewDigest": "sha256:<64 lowercase hex>",
  "reason": "historical schema 3 operation predates required specDigest"
}
```

Apply regenerates the preview before writing. It rejects version, identity,
Fabric, or preview digest drift.

The dedicated store mutation performs one PostgreSQL transaction:

1. lock the runtime operation row;
2. compare the exact prior result, version, stage, and status;
3. revalidate that the only defect is the missing `specDigest`;
4. require the desired row to pass the strict decoder;
5. require the exact two-field semantic diff;
6. update the operation;
7. insert the deterministic admin audit event;
8. commit both changes atomically.

An exact replay returns the already repaired result and existing audit fact.
The same idempotency identity with different input fails closed.

## Evidence And Errors

Operator-facing responses contain digests and bounded summaries, never account
IDs, workspace IDs, operation IDs, email addresses, provider plans, tokens, or
database rows. The internal Control Plane-to-Fabric typed contract carries the
exact identities required for owner verification over the existing authenticated
service boundary; those identities are not projected into operator evidence.

Stable failure classes distinguish:

- operation not found;
- operation not eligible;
- Fabric binding not found;
- Fabric binding integrity conflict;
- identity mismatch;
- preview drift;
- CAS conflict;
- audit idempotency conflict.

Every failure remains fail-closed and performs no partial write.

## Verification

Focused tests must prove:

- Fabric success, not-found, corrupted binding, identity drift, and zero
  provider calls;
- preview eligibility and every rejection boundary;
- candidate strict decode and exact two-field diff;
- apply success, exact replay, idempotency conflict, version/CAS conflict, and
  audit atomicity;
- PostgreSQL concurrent apply admits one writer and one audit fact;
- Controlled Basic Pilot retains its exact 11-field contract;
- all mutation spies remain zero outside the Control Plane operation/audit
  transaction.

The aggregate gate is `npm run verify:local:full`.

## Non-Goals

Candidate A does not add:

- a generic JSON patch or migration platform;
- schema 2 compatibility;
- decoder fallback behavior;
- automatic database scanning;
- current-provider-plan recomputation;
- a recovery worker or operator UI;
- debit convergence, operation termination, or launch resume;
- Instance workflow or validator changes;
- provider, billing, Key, Workspace, or Receipt mutation.
