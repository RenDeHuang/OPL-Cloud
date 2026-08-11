# Workspace Lifecycle

Owner: `one-person-lab-cloud`
Purpose: `workspace_lifecycle_planning_contract`
State: `active_target_contract`
Machine boundary: Human-readable target lifecycle. It is not an executable
state machine, provisioned Workspace record, runtime readback, or availability
claim.

OPL Workspace is the cloud OPL App workbench. Each user account may own zero or
more independent Workspace Instances. Every instance has its own stable id,
URL, runtime, storage, provider binding, billing period, and lifecycle. The
product sets no fixed count limit; each creation remains subject to balance,
capacity, quota, and policy.

Agent Services published through OPL Serve are separate deployment resources.
An account may own multiple Services without creating another Workspace, and a
Workspace URL cannot become a Service endpoint.

The lifecycle should be understandable as a workbench lifecycle, not as a
container-hosting workflow.

## Lifecycle States

```text
requested -> provisioning -> active -> suspended -> deleted
                         \-> failed
```

## Lifecycle Actions

| Action | Meaning | Main owner |
| --- | --- | --- |
| Create | Provision one new account-owned Workspace with permitted package refs, compute, storage, and access policy | Console policy; Package identity remains with its owner and installed state with the carrier |
| Provision | Prepare runtime, storage, credentials, URL, and base OPL App payload | Fabric |
| Open | User enters the Workspace through an isolated URL | Workspace |
| Rotate credentials | Reset credentials for the workbench access surface | Console |
| Attach storage | Bind workspace volume, bucket, or institutional/managed storage ref | Fabric |
| Suspend | Stop user access and managed compute while retaining policy-defined data | Console |
| Resume | Restore access and runtime according to policy | Console |
| Delete | Remove runtime and apply retention policy to data and receipts | Console |

## Workspace Record

Required planning fields:

- `workspace_id`
- `workspace_name`
- `owner_account_ref`
- `collaboration_policy_ref`
- `url`
- `status`
- `package_owner_descriptor_ref`
- `package_publication_revision_ref`
- `package_carrier_readback_ref`
- `compute_profile`
- `storage_profile`
- `provider_binding_ref`
- `billing_period_ref`
- `gateway_policy_ref`
- `connector_policy_refs`
- `environment_ref`
- `ledger_policy_ref`
- `created_at`
- `updated_at`

## Receipts

Create, suspend, resume, delete, credential rotation, storage binding, and
managed job actions should leave Ledger refs when they affect user work, cost,
security, or reproducibility.

Workspace records only reference Package-owner and carrier truth. Provisioning
cannot publish, install, update, remove, repair or rewrite Package state;
publication routes to the Package owner and physical lifecycle routes to the
configured carrier through Framework delegation.
