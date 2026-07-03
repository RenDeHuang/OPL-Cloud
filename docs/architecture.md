# Architecture

## Product Layers

OPL Cloud has five product layers:

- OPL Gateway: AI provider routing, token policy, and request usage policy.
- OPL Workspace: URL-delivered working environment running `one-person-lab-app`.
- OPL Console: account, workspace, billing, access, support, and admin control surface.
- OPL Fabric: compute, storage, image, route, and infrastructure handoff.
- OPL Ledger: billing ledger, wallet transactions, audit events, receipts, verifier evidence, and reconciliation.

## Current Implementation Slice

This repository deploys one OPL Console control-plane process.

The `packages` layout is a boundary map for future extraction:

- `packages/console`: API, UI, auth, management model, store, readiness, manifests, and Console services.
- `packages/fabric`: runtime providers and resource catalog.
- `packages/ledger`: receipt and reconciliation helpers.
- `packages/contracts`: machine-readable product, route, lifecycle, billing, management, storage, and evidence contracts.

## Boundaries

Console may call Fabric only through package boundary exports or future service APIs.

Console may call Ledger only through package boundary exports or future service APIs.

Fabric details such as TKE, Docker, Ingress, PVC, VolumeSnapshot, and runtime operation evidence are admin/operator surfaces. Lab Owner UI should expose product status and allowed actions, not raw infrastructure evidence.

Ledger details such as dedup rows, request fingerprints, and raw event payloads are admin/operator surfaces. Lab Owner UI should expose wallet balance, holds, recent charges, usage, top-ups, and human-readable receipts.

## Persistence

PostgreSQL is the production persistence target. JSON state remains a local development store.

Commercial identity, wallet, Workspace, billing, support, audit, and receipt data must persist across rollouts when `DATABASE_URL` is configured.

## No Compatibility Layer

Legacy paths are retired directly after active callers move. The only acceptable bridge is a one-time state migration that writes the current data model.
