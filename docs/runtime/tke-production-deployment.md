# Tencent TKE Production Deployment

## Deployment Boundary

Tencent TKE is the production runtime provider for the operator-provisioned OPL Console
and OPL Workspace Pilot.

The deployment owns:

- separate Control Plane, Fabric, and Ledger Kubernetes Deployments.
- Vue Console assets served by Control Plane.
- ComputePool and ComputeAllocation handoff to TKE.
- TCR image references.
- Kubernetes Service and Ingress routing.
- Persistent workspace storage through PVC/CBS.
- one-person-lab-app runtime scheduling onto user-owned CVM nodes.
- separate Control Plane, Fabric, and Ledger PostgreSQL schemas.

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
- require `DiskPressure=False` and at least 25 GiB available on both live
  `nodefs` and `imagefs` before any apply;
- install secrets without printing secret values;
- create one candidate revision for each of Control Plane, Fabric, and Ledger
  through the rendered manifest only, without `set image` plus `rollout restart`;
- observe all three Cloud Deployments within one shared 300-second deadline and
  fail immediately on eviction, disk pressure, image-pull backoff, crash-loop
  backoff, or unschedulable Pods;
- capture full rollout diagnostics and upload the artifact before the one
  independent rollback job runs;
- preserve the Workspace ConfigMap digest and never restart or wait for existing
  Workspace Deployments;
- verify the three Ready Cloud Pod image IDs, the unchanged Workspace digest,
  the existing administrator identity, and read-only Console pages. Full-system
  `/api/production/readiness` remains diagnostic and is not a Pod-local probe;
- keep Provider Acceptance and Basic/Pro fixed-slot verification paused and out
  of the ordinary release gate;
- perform no Tencent purchase, renewal, or deletion. Provider Acceptance is a
  separate manually approved workflow.

## Pricing Defaults

Current price defaults belong in a versioned pricing contract and environment template.

Customer prices come only from `opl-cloud-pricing-contract.json` and server DTOs.
Environment values may consume the selected `priceVersion`; they cannot derive
or override customer amounts. Tests assert the contract and runtime consume the
same versioned catalog.
