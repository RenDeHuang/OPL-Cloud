# Tencent TKE Production Deployment

## Deployment Boundary

Tencent TKE is the production runtime provider for the current OPL Console / OPL Workspace control-plane slice.

The deployment owns:

- OPL Console control-plane pod.
- ComputePool and ComputeAllocation handoff to TKE.
- TCR image references.
- Kubernetes Service and Ingress routing.
- Persistent workspace storage through PVC/CBS.
- one-person-lab-app runtime scheduling onto user-owned CVM nodes.
- PostgreSQL control-plane persistence.

## Manifest Rules

Production manifests must:

- avoid inline secrets;
- use secret refs or mounted secret files for sensitive values;
- keep Console and Workspace domains explicit;
- keep Workspace image explicit;
- use an image pull secret for private registry access;
- keep shared Ingress changes deliberate.
- require `OPL_TENCENT_PROVISIONER_BIN` for Tencent Cloud mutations.

## Workflow Rules

Production deploy workflow must:

- run from the approved production environment;
- use a VPC-capable self-hosted runner for cluster access;
- validate rendered manifests before apply;
- install secrets without printing secret values;
- restart and wait for the control-plane rollout;
- leave diagnostics read-only unless the deploy job is explicitly mutating.

## Pricing Defaults

Current price defaults belong in a versioned pricing contract and environment template.

Tests should assert the contract and runtime consume the same versioned catalog, not that prose or workflow text contains a particular historical price snapshot.
