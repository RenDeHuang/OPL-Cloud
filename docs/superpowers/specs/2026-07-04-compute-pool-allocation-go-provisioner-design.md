# Compute Pool Allocation Go Provisioner Design

## Status

Approved direction for the next OPL Cloud implementation cycle.

This spec replaces the failed nodepool-per-user goal. It does not preserve a compatibility layer for the old implementation.

## Product Goal

OPL Cloud sells account-owned compute, storage, and workspace access as a closed commercial chain:

1. A user selects a compute package.
2. OPL Cloud finds the matching Tencent TKE node pool for that package.
3. If the matching node pool does not exist, OPL Cloud creates it.
4. OPL Cloud opens one CVM node in that pool for the user.
5. OPL Cloud records which account and user own that CVM allocation.
6. OPL Cloud deploys the one-person-lab-app Docker runtime onto that CVM node.
7. OPL Cloud mounts the user's cloud disk into that runtime.
8. OPL Cloud creates one Workspace URL for that runtime.
9. OPL Cloud starts billing for compute and storage using ledger records.

One Workspace URL maps to one user-owned CVM runtime.

## Non-Goals

- Do not create a new TKE node pool for every user compute purchase.
- Do not keep `tccli`, `runTccli`, or tccli-specific tests as a compatibility path.
- Do not expose raw TKE/Fabric evidence to Lab Owner screens.
- Do not hide resource creation state behind a toast-only interaction.
- Do not make Workspace the resource body. Workspace remains the access entry.
- Do not require compute to exist before storage can be purchased.

## Domain Model

### ComputePool

`ComputePool` is a package-level TKE node pool.

Fields:

- `id`: stable OPL id, for example `pool-basic-2c4g`.
- `packageId`: package that uses this pool, for example `basic`.
- `instanceType`: Tencent CVM instance type.
- `cpu`: package CPU count.
- `memoryGb`: package memory.
- `nodePoolId`: Tencent TKE node pool id.
- `status`: `missing`, `creating`, `ready`, `scaling`, `failed`.
- `hourlyPrice`: CNY per hour shown in Console.
- `providerData`: admin-only provider data.

Invariant:

- One compute package maps to one active ComputePool.
- The same pool can host many account-owned CVM allocations.

### ComputeAllocation

`ComputeAllocation` is the customer-owned compute resource. It represents one CVM node opened in a ComputePool for one account.

Fields:

- `id`: OPL allocation id.
- `ownerAccountId`
- `ownerUserId`
- `packageId`
- `poolId`
- `nodePoolId`
- `instanceId`: Tencent CVM instance id when known.
- `nodeName`: Kubernetes node name when joined.
- `status`: `provisioning`, `running`, `failed`, `destroying`, `destroyed`.
- `billingStatus`: `active`, `paused`, `closed`.
- `hourlyPrice`
- `holdAmount`
- `operationId`
- `createdAt`, `updatedAt`, `destroyedAt`

Invariant:

- One ComputeAllocation is owned by exactly one account.
- One ComputeAllocation corresponds to one user-dedicated CVM node.
- Runtime pods for a Workspace must be scheduled onto the owned node.

### StorageVolume

`StorageVolume` is an account-owned Tencent cloud disk exposed through PVC/CBS.

Fields:

- `id`
- `ownerAccountId`
- `ownerUserId`
- `sizeGb`
- `storageClass`
- `providerResourceId`
- `status`: `provisioning`, `available`, `attached`, `failed`, `destroying`, `destroyed`.
- `gbMonthPrice`
- `hourlyEstimate`
- `holdAmount`
- `createdAt`, `updatedAt`, `destroyedAt`

Invariant:

- Storage can exist without compute.
- Storage can be attached to at most one active ComputeAllocation at a time in v1.
- Storage must retain data after compute is destroyed unless the user explicitly destroys storage.

### StorageAttachment

`StorageAttachment` connects one StorageVolume to one ComputeAllocation runtime.

Fields:

- `id`
- `ownerAccountId`
- `computeAllocationId`
- `storageVolumeId`
- `mountPath`
- `status`: `attaching`, `attached`, `detaching`, `detached`, `failed`.
- `runtimeDeploymentName`
- `operationId`

Invariant:

- Attachment deploys or reconciles one-person-lab-app on the selected compute allocation.
- Attachment must mount the selected StorageVolume into the runtime.

### Workspace

`Workspace` is the URL and token entry for a runtime already backed by compute and storage.

Fields:

- `id`
- `ownerAccountId`
- `attachmentId`
- `computeAllocationId`
- `storageVolumeId`
- `url`
- `tokenStatus`
- `runtimeStatus`

Invariant:

- One Workspace URL maps to one CVM-backed runtime.
- Workspace does not own compute or storage lifecycle.

### Wallet and Ledger

Ledger entries must reference resource ids directly:

- `computeAllocationId`
- `computePoolId`
- `storageVolumeId`
- `storageAttachmentId`
- `workspaceId`

Compute billing starts when ComputeAllocation becomes `running` or when the cloud provider confirms the CVM is billable, whichever is earlier in provider evidence. Storage billing starts when StorageVolume becomes `available` or provider confirms the disk is billable.

## Console UX Requirements

### Compute Creation

The compute creation screen must show before submit:

- Package name.
- CPU and memory.
- Hourly compute price.
- Initial hold amount.
- Balance after hold.
- Estimated available runtime from wallet balance.
- Billing start rule.

After submit:

- The button must show loading.
- The API must return an `operationId` and a resource id quickly.
- The UI must navigate to the ComputeAllocation detail page.
- The detail page must show a provisioning timeline.
- Failure must be visible on the detail page with the provider-safe reason and retry/cancel action when available.

### Storage Creation

The storage creation screen must show before submit:

- Capacity.
- GB-month price.
- Hourly estimate.
- Initial hold amount.
- Balance after hold.
- Billing start rule.

After submit:

- The UI must navigate to the StorageVolume detail page.
- The detail page must show provisioning status.
- Storage must remain visible and useful even when there is no compute allocation.

### Storage Detail

Storage detail must show:

- Capacity.
- Price.
- Status.
- Current attachment if attached.
- Attachment history.
- Related Workspaces.
- Destroy action gated by explicit data-loss confirmation.

For v1, file browsing without compute is not required. Users inspect files through one-person-lab-app after attaching storage to compute.

### Attachment and Workspace

Attachment creation must show:

- Selected compute allocation.
- Selected storage volume.
- Mount path.
- Runtime image.
- Workspace URL behavior.

Workspace detail must show:

- URL.
- Token status.
- Runtime readiness.
- Linked compute allocation.
- Linked storage volume.
- Billing references.

## API Contract

Current commercial routes should describe only active capability.

Required API surface:

- `GET /api/compute-pools`
- `POST /api/compute-allocations`
- `GET /api/compute-allocations/:id`
- `POST /api/compute-allocations/:id/destroy`
- `POST /api/storage-volumes`
- `GET /api/storage-volumes/:id`
- `POST /api/storage-volumes/:id/destroy`
- `POST /api/storage-attachments`
- `POST /api/storage-attachments/:id/detach`
- `POST /api/workspaces`
- `POST /api/workspaces/:id/runtime-status`

Existing broad `POST /api/compute-resources` naming should be replaced or narrowed to the new allocation language. If the route is kept temporarily during migration, it must have a removal condition in the same commit and cannot be part of the active route contract.

## Go SDK Provisioner Boundary

OPL Cloud keeps the Node.js control plane for:

- Console UI.
- Auth.
- Account and user ownership.
- Wallet and ledger.
- Commercial route/API surface.
- Operation state.

A small Go provisioner owns Tencent Cloud mutations using Tencent Cloud Go SDK.

Provider responsibilities:

- Ensure ComputePool exists for a package.
- Create the package-level TKE node pool if missing.
- Scale or open one CVM node in the matching node pool.
- Return instance id and later node name.
- Delete or release a user-owned CVM allocation.
- Create and delete cloud disk/PVC resources when the chosen API requires provider mutation.
- Return normalized provider errors.

Invocation model:

- v1 should use a local provisioner binary called from Node with JSON stdin/stdout.
- The binary path is configured by `OPL_TENCENT_PROVISIONER_BIN`.
- Later, the same contract can move to an internal HTTP service without changing Console domain logic.

Example request:

```json
{
  "action": "create_compute_allocation",
  "accountId": "pi-alpha",
  "userId": "user-alpha",
  "packageId": "basic",
  "pool": {
    "id": "pool-basic-2c4g",
    "instanceType": "SA5.LARGE4",
    "desiredNodeLabels": {
      "oplcloud.cn/pool-id": "pool-basic-2c4g"
    }
  },
  "allocation": {
    "id": "computealloc-alpha-001"
  }
}
```

Example response:

```json
{
  "ok": true,
  "operationId": "op-001",
  "poolId": "pool-basic-2c4g",
  "nodePoolId": "np-abc",
  "instanceId": "ins-abc",
  "nodeName": "",
  "status": "provisioning",
  "providerData": {
    "requestId": "req-abc"
  }
}
```

Failure response:

```json
{
  "ok": false,
  "errorCode": "tencent_permission_denied",
  "message": "Tencent CAM denied creating CVM in the selected node pool.",
  "providerRequestId": "req-abc",
  "retryable": false
}
```

## tccli Removal

Remove:

- Dockerfile `tccli` installation.
- `REQUIRED_TOOLS` references to `tccli`.
- `runTccli`.
- tccli argument normalization.
- tccli-specific tests.
- deployment contract requirements that list `tccli`.

Replace with:

- `OPL_TENCENT_PROVISIONER_BIN`.
- Tencent credential env required by the Go provisioner.
- readiness check that confirms the provisioner binary exists and can answer a dry-run `readiness` action.

## Billing Rules

Compute:

- Show hourly price before submit.
- Hold enough balance before cloud mutation.
- Start active billing when provider confirms billable CVM or allocation enters `running`.
- Release hold and close billing when allocation is destroyed.
- Failed allocation must not remain active billable.

Storage:

- Show GB-month price and hourly estimate before submit.
- Storage can be bought without compute.
- Storage billing starts when provider confirms disk/PVC availability.
- Storage billing continues while unattached.
- Destroying compute must not destroy storage.

Attachment:

- Attachment has no direct price in v1 unless a provider fee is introduced.
- Attachment ledger records still exist for audit and reconciliation.

## Error Handling

All long cloud operations must create an operation record:

- `operationId`
- `resourceType`
- `resourceId`
- `status`
- `startedAt`
- `updatedAt`
- `providerRequestId`
- `safeMessage`
- `rawProviderError` stored admin-only

User-facing UI must show:

- Provisioning.
- Running.
- Failed with safe reason.
- Retry/cancel if available.

Do not make the user infer success from a toast.

## Route and UI Contract

Active route contract should include:

- Compute pools list/read.
- Compute allocation create/list/detail/destroy.
- Storage volume create/list/detail/destroy.
- Attachment create/list/detail/detach.
- Workspace create/list/detail/open URL.
- Billing wallet and ledger.
- Admin user and wallet operations that are actually implemented.

Backlog routes must stay out of the active route contract.

## Test Strategy

Keep tests minimal and tied to commercial truth:

- Compute package maps to one ComputePool.
- Create compute allocation records account/user/package/pool/instance ownership.
- Storage can be created without compute.
- Storage can attach to a compute allocation.
- Workspace URL references one compute allocation and one storage attachment.
- Price and hold data are returned for compute and storage creation screens.
- Failed provisioner responses are visible in resource detail state.
- tccli is absent from Dockerfile, readiness, provider code, and deployment contract.

Do not add tests that lock source-code string shapes such as `navigate("/path")` or specific shell command text.

## E2E Strategy

Local E2E:

1. Seed wallet.
2. Read compute package prices.
3. Create storage without compute.
4. Create compute allocation.
5. Attach storage.
6. Create Workspace URL.
7. Write a file through runtime.
8. Destroy compute allocation.
9. Create a second compute allocation from the same package pool.
10. Reattach same storage.
11. Read the file.
12. Verify ledger records.

Public staging E2E:

- Run only after CAM, image, security group, subnet, and node pool templates are confirmed.
- Create real paid compute and storage.
- Clean all paid E2E resources at the end.
- Leave storage retained only during the persistence step, then destroy it after verification.

## Rollout

1. Implement and verify locally in an isolated worktree.
2. Run local E2E.
3. Merge to main only after review.
4. Deploy control plane.
5. Run public staging E2E once.
6. Keep public staging resources only for the agreed validation window.

## Open Decisions

None for v1. The user has confirmed that one user owns one dedicated CVM node, and one Workspace URL maps to that CVM-backed runtime.
