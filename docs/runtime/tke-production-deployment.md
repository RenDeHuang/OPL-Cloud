# Tencent TKE Production Deployment

## Deployment Boundary

Tencent TKE is the production runtime provider for the invite-only OPL Console
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
- install secrets without printing secret values;
- restart and wait for Control Plane, Fabric, and Ledger rollouts;
- require both retained Basic/Pro Acceptance slots before an ordinary release;
- run live QA once with one Basic reserved account, one dedicated Key, and one
  model request for the entire release;
- perform no Tencent purchase, renewal, or deletion. Provider Acceptance is a
  separate manually approved workflow.

## Pricing Defaults

Current price defaults belong in a versioned pricing contract and environment template.

Customer prices come only from `opl-cloud-pricing-contract.json` and server DTOs.
Environment values may consume the selected `priceVersion`; they cannot derive
or override customer amounts. Tests assert the contract and runtime consume the
same versioned catalog.
