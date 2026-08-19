# Fabric Provider Decoupling Design

**Status:** Approved for implementation

**Baseline:** `codex/local-docker-resource-quotas` at `1be37632` (PR #367 is still open)

**Objective:** Keep one Workspace Launch business flow while moving Provider plan ownership, resource specifications, provider writes, and authoritative readback into the selected Fabric Provider without modifying the Local-Docker resource-quota topic.

## Ownership

- `packages/contracts` owns the typed catalog and immutable launch-binding contracts.
- `services/fabric` owns Provider profile/catalog resolution, Provider binding persistence, provider-neutral stage operations, provider mutations, and provider readback.
- `services/control-plane` owns Workspace business orchestration, billing coordination, stage order, and recovery authorization. It stores only opaque Provider binding identity and must not derive infrastructure specifications.
- `apps/console-ui` owns product presentation and calls only Control Plane APIs. It exposes package choices, not Docker or Tencent infrastructure parameters.
- `opl-instance-medopl` remains the owner of concrete production Provider Profile, environment, Secrets, deployment, and verification. This repository does not deploy production.

## Design

### Provider binding

Fabric resolves a selected `packageId` against the selected Provider Profile during read-only preflight. It canonicalizes the Provider-owned plan, computes a `specDigest`, and persists an immutable Provider binding containing the profile reference, selected capacity/storage identity, and canonical plan. The preflight response and every Workspace Launch stage carry only the opaque binding identity and digest required for exact validation.

Replay and readback load the original binding. A missing binding, digest mismatch, or provider-resource mismatch fails closed; the current Provider configuration is never silently substituted for an existing launch.

### Internal Provider responsibilities

The existing `Provider` facade remains the live Fabric port to avoid a service or framework rewrite. Its capabilities are made explicit inside Fabric:

- plan/catalog resolution;
- compute allocation;
- storage volume;
- storage attachment;
- Runtime;
- provider facts and authoritative readback.

Fabric Core keeps stage orchestration, typed HTTP, operation/mutation journals, and generic binding validation. Provider adapters keep all concrete Docker, Linux quota, CVM, TKE, CBS, Kubernetes, and provider-specific readback behavior.

### Local-Docker Provider

Local-Docker plan values (CPU, memory, storage, and quota policy) come from a deployment-owned Provider Profile and are validated at startup/preflight. Existing Local-Docker host-capacity and Linux project-quota behavior from PR #367 remains unchanged; this branch only binds those admitted facts to the immutable Provider plan and exercises the existing behavior through the new contract.

### Tencent/TKE Provider

The Tencent Provider Profile supplies package shape, CVM instance type, TKE capacity pool/NodePool, zone, CBS disk type/size policy, and billing/renewal policy. Preflight validates live CVM/TKE/CBS inventory, quota, and price, then binds the exact selected identities. TKE scale operations retain the current absolute replica target, baseline machine inventory, provider request IDs, and read-only continuation model. CBS, PV/PVC, and Kubernetes Runtime remain adapter-owned and are validated against the original binding.

### Control Plane and Console

Control Plane keeps the existing `key -> debit -> compute -> storage -> attachment -> secret -> runtime -> activation` reconciler. It submits `providerProfileRef`, `providerBindingRef`, and `specDigest` but never submits or computes Docker/Tencent infrastructure parameters. Console receives only product-level catalog fields and submits `packageId`.

## Implementation slices

1. Isolate the worktree and branch from PR #367.
2. Update catalog and launch-binding contracts and golden vectors.
3. Add Fabric plan/binding capabilities and persistence while retaining the Provider facade.
4. Update Local-Docker and Tencent adapters in parallel behind the stable Fabric contract.
5. Wire Control Plane stage inputs, replay, and Console catalog projection to opaque Provider binding identity.
6. Add focused isolation, binding-drift, replay/readback, and regression tests; update current architecture/status/roadmap projections.

## Non-goals

- No changes to `codex/local-docker-resource-quotas` or PR #367 commits.
- No shared-node TKE redesign.
- No replacement of the current Kubernetes `kubectl` transport without observed evidence.
- No per-Workspace CLB/Ingress redesign.
- No second Provider registry, event bus, workflow framework, or new service.
- No production deployment, Tencent live mutation, push, or pull request.

## Acceptance evidence

- Console and Control Plane contain no Provider-specific resource specifications.
- Local-Docker and Tencent catalogs/plans can differ without cross-provider code changes.
- A launch replay uses the original immutable binding and `specDigest`.
- Provider configuration drift returns a typed conflict instead of silently selecting a new plan.
- Local-Docker quota/capacity behavior from PR #367 remains green and its branch remains untouched.
- Tencent compute/CVM/TKE, CBS/PV/PVC, Runtime, and readback tests preserve exact identity and asynchronous continuation semantics.
- Focused tests pass, followed by the applicable `npm run verify:local:full` gate.
