# Tencent TKE Production Deployment

## Deployment Boundary

Tencent TKE is the production runtime provider for the current OPL Console / OPL Workspace control-plane slice.

The deployment owns:

- OPL Console control-plane pod.
- ComputeResource provisioning through Tencent TKE node pool creation or expansion.
- TCR image references.
- Kubernetes Service and Ingress routing.
- StorageVolume provisioning through PVC/CBS.
- StorageAttachment handoff that schedules the one-person-lab-app runtime Deployment/Service onto the selected compute node pool and mounts the selected storage volume.
- Workspace URL/token entries served by the Console gateway.
- PostgreSQL control-plane persistence.

## Manifest Rules

Production manifests must:

- avoid inline secrets;
- use secret refs or mounted secret files for sensitive values;
- keep Console and Workspace domains explicit;
- keep Workspace image explicit;
- use an image pull secret for private registry access;
- keep shared Ingress changes deliberate.

## Workflow Rules

Production deploy workflow must:

- run from the approved production environment;
- use a VPC-capable self-hosted runner for cluster access;
- validate rendered manifests before apply;
- install secrets without printing secret values;
- restart and wait for the control-plane rollout;
- leave diagnostics read-only unless the deploy job is explicitly mutating.

## Old Runtime Cleanup

The cleanup workflow is only for old Workspace-as-runtime objects that predate the current resource model.

It may remove:

- `opl-ws-*` Deployments and their ReplicaSets/Pods.
- Matching `opl-ws-*` Services.
- Matching `opl-ws-*-env` Secrets.
- Matching `opl-ws-*-data` PVCs.
- Shared Ingress paths whose backend service name starts with `opl-ws-`.

It must preserve:

- `opl-cloud-control-plane`.
- Shared `opl-cloud` Ingress object and TLS secrets.
- `tcr-pull-secret`.
- TCR image repositories and tags, including `one-person-lab-app`.
- Current node-pool compute, storage volume, attachment, and Workspace URL objects.

Mutating cleanup requires `dry_run=false` and `confirm=CLEAN_OLD_WORKSPACES`.

## Pricing Defaults

Current price defaults belong in a versioned pricing contract and environment template.

Tests should assert the contract and runtime consume the same versioned catalog, not that prose or workflow text contains a particular historical price snapshot.
