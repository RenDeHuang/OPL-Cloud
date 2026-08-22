# Workspace Launch Disposable Reset Design

## Decision

Add one narrow Control Plane operator lane for an explicitly disposable,
non-terminal `workspace.launch.v2` operation that never formed a Workspace
projection. The lane discovers facts through each current owner, freezes an
exact reset plan, executes only the mutations authorized by that plan, and
terminalizes rather than deletes the launch operation.

This is not a general account reset, database reset, provider cleanup, or
Customer Workspace delete API. The normal Workspace Delete lane remains the
only path for an activated Workspace.

## Ownership

- Control Plane owns the reset orchestration, operation terminalization, audit,
  operator DTO, and final reset receipt request.
- Fabric owns persisted preflight/stage bindings, Kubernetes runtime and Secret
  observations, provider-resource deletion, and provider absence readback.
- Sub2API owns Workspace Key state and balance-history/debit truth. Confirmed
  debits are compensated through the existing refund authority; history is
  never deleted.
- Ledger remains append-only. Existing receipts are retained; the reset appends
  one evidence receipt describing the abandoned test launch and compensation.
- Instance owns protected deployment, exact Candidate selection, production
  operator execution, and final receipt retention.

## Eligible Operation

Preview is eligible only when all of the following are true:

- action is `workspace.launch.v2` and the current strict decoder succeeds;
- status is `manual_review` and stage is `debit`;
- no Workspace projection exists for the operation Workspace identity;
- the account/operation is explicitly asserted disposable by the protected
  caller and matches the configured reset authority boundary;
- the Fabric preflight binding matches every canonical launch identity;
- every owner read returns one determinate `absent` or `confirmed` result;
- no unrelated non-terminal operation exists for the same account or Workspace;
- no receipt, Key, debit, Fabric operation, Kubernetes object, or provider
  resource has conflicting ownership.

An unknown, duplicate, unavailable, or conflicting fact makes preview
ineligible. Stage position alone never proves resource absence.

## Operator API

```text
GET  /api/operator/workspace-launches/{operationId}/disposable-reset-preview
POST /api/operator/workspace-launches/{operationId}/disposable-reset
```

Preview is read-only. It returns only identity digests, counts, typed states,
the ordered mutation plan, and a deterministic `resetPlanDigest`. It does not
return raw account, Workspace, operation, Key, provider-resource, or receipt
identities.

Apply requires:

```json
{
  "launchVersion": 6,
  "resetPlanDigest": "sha256:...",
  "reason": "dispose_precommercial_test_workspace_launch"
}
```

and one unique `Idempotency-Key`. Apply regenerates the preview before the first
mutation and rejects any plan drift.

## Execution Order

The read-only inventory fans out after strict launch decode:

1. Sub2API Key and canonical debit history;
2. Ledger receipts for the exact account/Workspace operation identities;
3. Fabric preflight and all launch-stage records;
4. Fabric/Kubernetes runtime and gateway-secret observations;
5. Control Plane Workspace and competing-operation state.

The mutation path is serialized:

1. destroy runtime and gateway Secret when confirmed present;
2. detach storage attachment when confirmed present;
3. destroy storage when confirmed present;
4. destroy compute when confirmed present;
5. require Fabric, provider, and Kubernetes absence readback;
6. delete the exact test Workspace Key when confirmed present;
7. compensate the exact confirmed debit through a unique refund operation;
8. append one Ledger disposable-reset receipt;
9. CAS terminalize the launch operation as `failed` with reset evidence;
10. write the Control Plane operator audit;
11. repeat all owner readbacks and return the final redacted receipt.

If the debit is absent, no wallet mutation occurs. If it is confirmed, the
compensating credit must exactly equal the original debit. A conflicting or
unknown debit stops before Key deletion and terminalization.

## Failure And Replay

Every mutation uses a deterministic child idempotency key derived from the
reset operation identity and frozen plan. The launch operation records the
reset authorization, completed steps, authoritative observations, and mutation
counts. A retry resumes from owner readback and never repeats a confirmed
external mutation.

The terminal state is reached only after all target resources are absent, the
Key is absent, the debit is absent or exactly compensated, and the reset Ledger
receipt is readable. The historical launch row is retained and remains strict
decoder compatible.

## Receipt

The final operator response and Ledger evidence include:

```text
resetPlanDigest
discoveredTestOperations
discoveredProviderResources
destroyedProviderResources
removedWorkspaceRuntimes
removedTestKeys
confirmedTestDebits
compensatedTestDebits
compensatingCreditMicros
retainedTestReceipts
resetEvidenceReceipt
remainingNonTerminalOperations
remainingManualReview
remainingOwnedProviderResources
remainingTestKeys
remainingUnreconciledTestDebits
mutationScopeMatchedPlan
```

Raw identities, credentials, tokens, Provider payloads, and complete database
records are excluded.

## Non-goals

- No direct database deletion or table truncation.
- No deletion of Ledger receipts or Sub2API balance history.
- No TKE cluster, Cloud Deployment, namespace infrastructure, Candidate config,
  shared network, or shared database deletion.
- No batch reset, arbitrary resource ID input, schema compatibility relaxation,
  automatic startup migration, or Instance validator change.
- No Candidate B recovery or continuation of the abandoned purchase.

## Acceptance

- Preview performs zero mutations and binds one deterministic owner-derived
  plan.
- Apply cannot mutate outside the plan and is idempotent across response loss.
- Provider deletion follows runtime, attachment, storage, compute order.
- Financial compensation is exact and append-only evidence remains intact.
- The launch operation ends in a strict-decoder-compatible terminal state.
- Final owner readback reports zero non-terminal operations, manual reviews,
  owned provider resources, test Keys, and unreconciled debits.
- Controlled Basic Pilot returns `disableRequired=false` and Instance may
  continue only after its read-only diagnostic passes.
