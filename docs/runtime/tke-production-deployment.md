# Tencent TKE Production Deployment

## Deployment Boundary

Tencent TKE is the production runtime provider for the operator-provisioned OPL Console
and OPL Workspace Pilot.

The deployment owns:

- separate Control Plane, Fabric, and Ledger Kubernetes Deployments.
- React Console assets built on `@openai/apps-sdk-ui` and served by Control Plane.
- ComputePool and ComputeAllocation handoff to TKE.
- TCR image references.
- Kubernetes Service and Ingress routing.
- Persistent workspace storage through PVC/CBS.
- one-person-lab-app runtime scheduling onto user-owned CVM nodes.
- separate Control Plane, Fabric, and Ledger PostgreSQL schemas.

The deployed Console follows the current implementation in `apps/console-ui`
and the outcomes in `docs/product/console-experience-guide.md`. Its browser code
calls only Control Plane product APIs; Fabric, Ledger, Sub2API, and Tencent
remain server-side boundaries.

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
- use a VPC-capable self-hosted runner only for cluster access and a separate
  GitHub-hosted Ubuntu runner for public API and browser verification;
- validate rendered manifests before apply;
- require `DiskPressure=False` and at least 25 GiB available on both live
  `nodefs` and `imagefs` before any apply;
- install secrets without printing secret values;
- create one candidate revision for each of Control Plane, Fabric, and Ledger
  through the rendered manifest only, without `set image` plus `rollout restart`;
- observe all three Cloud Deployments within one shared 300-second deadline and
  follow each current revision through exact Deployment-to-ReplicaSet and
  ReplicaSet-to-Pod controller owner UIDs and expected images;
- fail immediately on node disk pressure or current-revision Pod eviction,
  image-pull backoff, crash-loop backoff, or unschedulability, while retaining
  historical terminal Pods only as diagnostics for both candidate and rollback;
- capture full rollout diagnostics and upload the artifact before the one
  independent rollback job runs;
- preserve the Workspace ConfigMap digest and never restart or wait for existing
  Workspace Deployments;
- verify the three current-revision Ready Cloud Pod image IDs and unchanged
  Workspace digest in the cluster job, then verify the existing administrator
  identity and read-only Console pages in the public job. The cluster job holds
  no administrator credentials or browser dependencies; the public job holds
  no kubeconfig or Tencent deployment secrets. Full-system
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
