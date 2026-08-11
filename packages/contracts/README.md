# OPL Cloud Contracts

Machine-readable contracts are narrow deterministic gates for durable
cross-module, public-interface, security, integrity and irreversible-side-effect
facts. They are not a complete product specification or a snapshot of current
implementation.

Each contract should declare:

- `schemaVersion`
- `owner`
- `purpose`
- `state`
- `machineBoundary`
- `lifecycle`

## Launch Owner Map

Workspace launch facts are split by the authority that can write or verify
them:

| Boundary | Current owner contract |
| --- | --- |
| Customer settlement coordination and Sub2API balance authority | `opl-cloud-billing-ledger-contract.json` |
| Launch business operation, stage decision, and recovery authorization | `opl-cloud-control-plane-launch-contract.json` |
| Fabric stage operation, idempotency, request hash, and resource binding | `opl-cloud-fabric-launch-binding-contract.json` |
| Receipt, evidence, reconciliation, and continuation refs | `opl-cloud-evidence-ledger-contract.json` |

These focused contracts are the only current machine owners for the launch and
settlement boundary. Do not recreate an aggregate launch contract.

## Admission

Add or retain a field only when it has one authority owner, a current caller or
credible hard-boundary risk, and no stronger schema/source/workflow owner.

Do not encode:

- visual direction, colors, dimensions, navigation count or component choice;
- internal query, pagination, batching, concurrency or worker tuning;
- exact source layout, workflow steps, shell commands or timeout values already
  owned by executable code;
- current implementation progress, pending evidence or roadmap stages;
- historical compatibility shape without a live reader.

Tests should validate the owning behavior or interface. Reading a JSON file and
asserting every value does not by itself make those values durable product
truth.

## Lifecycle

- `current`: active contract for current implementation or Pilot operations.
- `migration`: temporary migration contract with a removal condition.
- `superseded`: temporary retirement state while a proven live reader is being
  removed. After cutover, move needed provenance to `docs/history/**` and delete
  the machine contract.

## Rules

1. Contracts preserve eligible product and safety boundaries, not UI taste,
   implementation tuning, current status or old process.
2. Compatibility aliases do not belong in current contracts. Internal
   one-to-one persistence records must be labeled as compatibility-only.
3. Tests should exercise the actual boundary where practical. Contract-driven
   tests are appropriate only for eligible machine facts.
4. `opl-cloud-deployment-contract.json` is a migration surface. Production
   authorization, immutable identity, secret handling, mutation bounds,
   readback and rollback gates remain protected while workflow structure moves
   back to executable workflows and focused tests.
5. Portable image and release checks belong in
   `opl-cloud-distribution-contract.json`. Concrete deployment workflows,
   manifests, Secrets, rollback, and production receipts belong to the selected
   instance repository; medopl uses
   `opl-instance-medopl/contracts/medopl-deployment-contract.json`.
6. Package import and service boundary checks belong in
   `opl-cloud-service-boundary-contract.json`. Retired package and
   shared-execution machine shapes remain available in Git history only; do not
   reintroduce them as current contracts.
7. Human target descriptions such as shared execution may remain in their
   functional/architecture owner without recreating a machine contract before a
   real cross-module caller requires one.
8. Product reads reuse `SourceEnvelope<T>` and the server-side
   `writeSourceEnvelope`; do not create per-product envelope types.
9. `source`, `status`, `available`, and `fetchedAt` report the actual read. Return
   `sourceUpdatedAt` only when the authority provides it; local time is not a
   substitute.
10. Delivery levels belong in `docs/status.md` with matching evidence, never as
   mutable booleans inside a long-term machine contract.
11. `one-person-lab-cloud` is the single current product and implementation
    repository. Contracts may name Console, Control Plane, Fabric, and Ledger as
    logical service owners and `opl-cloud` as an artifact/service identifier,
    but must not project them as separate current repositories.
