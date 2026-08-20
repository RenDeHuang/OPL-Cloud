# Workspace Delete Lifecycle Design

**Status:** Approved for implementation

**Objective:** Make Workspace Delete permanently remove the Workspace and all
owned resources without changing the customer's wallet balance, while keeping
the operation durable, replay-safe, and provider-neutral.

## Ownership And Contract

- `services/control-plane` owns the user-visible Delete command, the durable
  business cursor, authorization, lifecycle concurrency, and Workspace
  projection removal.
- `services/fabric` owns authoritative runtime, Secret, attachment, storage,
  compute mutation and absence readback behind the existing provider port.
- `services/ledger` owns the append-only `workspace.deleted.v1` evidence
  Receipt. It neither authorizes deletion nor mutates wallet or provider state.
- `packages/contracts` owns the cross-module Delete operation and Receipt
  shapes. Architecture and invariant documents own the durable product meaning.
- Sub2API remains the Workspace Key and wallet authority. Delete removes the
  exact Workspace Key but performs no debit, refund, or wallet adjustment.

The product meanings are independent:

```text
Delete        = permanently remove all Workspace resources; no automatic refund
Cancel Renewal = retain resources until paidThrough; stop the next renewal
Refund         = a separate financial operation with its own authorization
```

This design applies to `customer_owned`, `platform_owned`, and `managed_tke`
Workspaces and to the `local-docker` and `tencent-tke` Fabric adapters.

## Chosen Approach

Delete uses a hard-cut `workspace.delete.v2` operation. Reinterpreting the
persisted `workspace.delete.v1` action was rejected because its immutable
request hash, phases, debit identity, refund identity, result, and Receipt all
encode the old automatic-refund contract. A compatibility engine that can run
both state machines was also rejected because it adds permanent complexity to
an irreversible path.

Before creating a v2 operation, Control Plane checks the deterministic v1
operation identity:

- a completed v1 operation whose Workspace is absent returns its stable
  historical terminal result;
- any non-terminal v1 operation blocks v2 before a mutation and reports an
  explicit conflict/manual-review result;
- no v1 row is rewritten, migrated, or refunded again;
- release admission requires a read-only inventory proving that no active v1
  Delete operation remains.

## Launch Identity Admission

Delete is admitted against the immutable succeeded Launch operation and its
exact Ledger Receipt, not against pricing or debit history. Both supported
Launch Receipt types carry the same provider-neutral fulfillment identity:

```text
accountId, workspaceId, ownerUserId
computeAllocationId, storageId, attachmentId, runtimeId
workspaceApiKeyId, workspaceKeyFingerprint, runtimeServiceName
```

- charged Launch uses `billing.workspace_purchased.v1` and retains its required
  `Cost` facts;
- zero-cost Launch uses `workspace.created`, has no `Cost`, and writes the same
  fulfillment identity for new operations;
- Delete accepts the exact historical `workspace.created` shape only when it is
  bound to an existing immutable succeeded Launch operation. Historical
  Receipts are not rewritten or backfilled.

The Launch operation, Workspace projection, and Receipt must agree on account,
owner, Workspace, and resource identities before the v2 operation is persisted.
Missing, conflicting, or unknown evidence fails closed before the first Fabric
or Sub2API mutation. A positive Debit and purchase amount are not Delete
prerequisites.

## Durable Delete Flow

The v2 cursor is:

```text
claimed
-> runtime_secret_absent
-> attachment_absent
-> storage_absent
-> compute_absent
-> key_absent
-> workspace_absent
-> deletion_receipt_recorded
-> complete
```

Each resource stage performs exact identity readback before mutation and
authoritative absence readback after mutation. Unknown, pending beyond the
bounded read budget, identity conflict, or provider error cannot advance the
cursor. Existing response-loss replay authorization remains only where a
mutation cannot otherwise be proven safe to repeat.

Workspace removal and the `workspace_absent` cursor transition are one
transaction. The RuntimeOperation row remains after Workspace removal, so a
Ledger outage retries only `workspace.deleted.v1`; it never repeats provider,
Key, or Workspace deletion. A repeat DELETE first loads the deterministic v2
operation and can therefore complete after the Workspace projection is absent.

Delete and Renewal are mutually exclusive lifecycle operations. The v2 claim
must reject a non-terminal Renewal operation, and Renewal claim/worker paths
must reject or skip a non-terminal v2 Delete. This rule is enforced in durable
store transactions; process-local locks only reduce duplicate work and are not
the authority.

## Deletion Receipt

`workspace.deleted.v1` is a strict non-financial Receipt:

- `Status = completed`, `Surface = control_plane`;
- `AccountID`, `WorkspaceID`, and owner fields match the Delete operation;
- `RequestID` is the deterministic v2 operation ID;
- `InputRefs` contains only the exact `launchReceiptId`;
- `Execution` binds the Delete operation and the resource identities;
- `OutputRefs` proves runtime/Secret, attachment, storage, compute, Key, and
  Workspace are absent;
- `Cost` is empty and `SupersedesReceiptID` is empty.

Ledger validates the shape only. Control Plane reads and validates the Launch
Receipt before deletion, sends the deletion Receipt, and requires an exact
response round-trip. `billing.workspace_refunded.v1` remains valid for a
separate refund workflow but is never emitted by Delete.

## Provider Completion

Local-Docker already deletes the container, Gateway Secret, durable CPU/memory
reservation, storage quota and directory, attachment binding, and compute
network with authoritative absence checks. This change preserves that code and
adds a real-Docker lifecycle acceptance path:

```text
create A -> restart Fabric -> delete A -> prove capacity released -> create B
```

Tencent storage Delete first removes the exact PV/PVC binding, then asks the
existing provisioner to delete the exact CBS disk. The provisioner validates
the disk identity, prepaid mode, size, zone, type, renewal flag, and ownership
tags before `TerminateDisks`, and advances only after bounded
`DescribeDisks` readback proves `NOT_FOUND`.

Tencent compute continues to use TKE `DeleteClusterMachines` with
`InstanceDeleteMode=terminate`. After TKE Machine absence, the provisioner also
performs bounded CVM readback. Fabric reports `external_deleted` only when both
the Machine and CVM are authoritatively absent. No ordinary test or verification
command calls real Tencent mutation APIs.

## Acceptance Evidence

- zero-cost and charged Workspaces both delete successfully without any debit,
  refund, wallet history, or refund Receipt call;
- missing or mismatched Launch/Receipt/resource identity fails before mutation;
- response loss and service restart resume the same v2 operation without
  repeating confirmed deletion stages;
- Workspace absence followed by Ledger failure retries only the deletion
  Receipt;
- Delete and Renewal cannot both acquire durable mutation authority;
- Ledger rejects deletion Receipts with cost, refund, supersedes, extra input
  refs, missing identity, or non-absent output;
- Tencent fake-SDK tests prove exact CBS termination and CBS/CVM absence; no real
  Tencent resource is bought or deleted;
- real Docker proves A can be deleted after Fabric restart and its released
  capacity reused by B;
- focused tests, `npm run verify:local`, and `npm run verify:local:full` pass.

## Non-goals

- No Archive, Restore, Snapshot, new service, event bus, or workflow engine.
- No refund API, cancellation redesign, proration, or billing-policy change.
- No automatic migration or guessed completion of historical v1 Delete rows.
- No rewrite of historical Launch Receipts.
- No real Tencent CVM/CBS deletion during development or CI.
