# Architecture

## Product Layers

OPL Cloud has five product layers:

- OPL Gateway: AI provider routing, token policy, and request usage policy.
- OPL Workspace: stable URL entry and lifecycle record backed by persistent storage and the current runtime pointer.
- OPL Console: account, workspace, billing, access, support, and admin control surface.
- OPL Fabric: compute, storage, image, route, and infrastructure handoff.
- OPL Ledger: billing ledger, wallet transactions, audit events, receipts, verifier evidence, and reconciliation.

## Current Implementation Slice

This repository has four runtime components and one non-runtime contract package:

- `apps/console-ui`: React + TypeScript browser UI. It owns no persistence and calls only the Control Plane API.
- `services/control-plane`: Go UI-facing API and product orchestrator. It owns auth, organizations, users, workspaces, support, operation requests, and UI projections. It calls Ledger and Fabric through typed HTTP clients.
- `services/ledger`: Go service backed by PostgreSQL. It owns wallets, holds, manual top-ups, ledger entries, wallet transactions, receipts, audit events, evidence references, idempotency keys, and reconciliation.
- `services/fabric`: Go service backed by PostgreSQL for Tencent Cloud and Kubernetes operations. It owns resource catalog, compute, storage, attachments, runtime templates, runtimes, provider request ids, retryable resource operations, resumable content transfers, and storage snapshots.
- `packages/contracts`: machine-readable product, route, lifecycle, billing, management, storage, evidence, and service-boundary contracts. It is source-of-truth data, not a fifth runtime service.

## Boundaries

Console UI may call Fabric and Ledger only through Control Plane routes. It must not import PostgreSQL drivers, Tencent Cloud SDKs, Kubernetes clients, or service internals.

Control Plane may call Fabric and Ledger only through published service APIs. It must not write ledger tables directly and must not call Tencent Cloud or Kubernetes clients directly.

Ledger owns money and evidence persistence. All Ledger write APIs require idempotency keys and use append-first writes.

Fabric owns cloud resource operations. Tencent Cloud SDK and Kubernetes client-go imports live under `services/fabric` only.

Workspace metadata sync, conflict records, recovery manifests, and product projections belong to Control Plane. Workspace content transfer and provider snapshot execution belong to Fabric. Ledger owns the resulting receipts and evidence references; it does not own artifact bytes.

Fabric details such as TKE, runtime images, Ingress, PVC/CBS, node-pool allocation, and runtime operation evidence are admin/operator surfaces. Lab Owner UI should expose product status and allowed actions, not raw infrastructure evidence.

`one-person-lab-app` is the default Workspace runtime template image. It is not a billable business object, storage owner, or lifecycle owner. ComputeAllocation, StorageVolume, StorageAttachment, Workspace URL, and Ledger records remain the commercial object truth.

Ledger details such as dedup rows, request fingerprints, and raw event payloads are admin/operator surfaces. Lab Owner UI should expose wallet balance, holds, recent charges, usage, top-ups, and human-readable receipts.

## Persistence

PostgreSQL is the production persistence target for Control Plane, Fabric, and Ledger. In-memory stores are intended for tests and local development.

Commercial identity, wallet, Workspace, billing, support, audit, and receipt data must persist across rollouts when `DATABASE_URL` is configured.

Fabric and Ledger reject a production start without `DATABASE_URL`. Production deployment and readiness checks must supply and verify each service's database configuration.

## Current Production Constraint

Workspace recovery APIs and Fabric snapshot operations use `snapshot.storage.k8s.io/v1`. The current TKE CBS snapshot installation exposes only `v1beta1`, so production backup and restore are not ready even though the service contracts and orchestration exist. The resolution is to provide the GA snapshot API or replace the provider implementation behind Fabric; adding a legacy API compatibility layer is forbidden.

OPL Gateway is deployed and operated outside this repository. The current Console integration is an external link only. Gateway keys, routing policy, model policy, and raw usage remain Gateway-owned until explicit Control Plane, Fabric, and Ledger integration contracts are implemented.

## No Compatibility Layer

Legacy Node API and store paths are retired directly after active callers move. The repository must not keep a Node proxy, BFF, or compatibility route layer in front of the Go services. The only acceptable bridge is a one-time state migration that reads the old data shape and writes the new service-owned tables.
