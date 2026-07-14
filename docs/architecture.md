# Architecture

## Request Path

```text
Browser Console
  -> Control Plane product API
       -> Sub2API management API: live balance and exact adjustment
       -> Fabric API: CVM, CBS, attachment, runtime, provider facts
       -> Ledger API: receipts and review evidence
```

Sub2API is external and remains the only spendable-balance, API-key, routing,
and request-usage owner. The repository does not mirror those records.

## Service Ownership

`apps/console-ui` owns presentation only. It has no persistence and never calls
Fabric, Ledger, Tencent, Kubernetes, or Sub2API directly.

`services/control-plane` owns Console auth, account mappings, organizations,
Workspaces, monthly entitlements, billing-operation recovery, support mappings,
and product projections. Its public routes express product commands rather than
generic downstream APIs.

`services/fabric` owns compute pools, dedicated CVM allocations, CBS volumes,
attachments, Workspace runtimes, provider operations, and all Tencent/Kubernetes
SDK calls. Provider callbacks may update resource facts but cannot overwrite
Control Plane entitlement state.

`services/ledger` owns EvidenceReceipt, ReviewPolicy, ReconciliationReport,
Artifact, Continuation, retention, audit, and idempotency records. It never
changes Sub2API balance.

`packages/contracts` is machine-readable current truth, not a runtime service.
Speculative route and object entries remain outside the active contracts.

## Persistence

Control Plane, Fabric, and Ledger each own their PostgreSQL schema. Cross-service
writes go through typed HTTP clients; no service writes another service's tables.
Sub2API data remains in Sub2API.

This deployment starts from a fresh database. There is no compatibility layer,
dual write, historical billing schema, or old-state importer.

## Resource And Billing State

Fabric preparation happens before the external charge. Control Plane persists a
stable billing operation and redeem code before side effects. Only a confirmed
Sub2API adjustment activates the monthly entitlement. Ledger receipt failure is
retryable and does not reverse a confirmed charge.

All attachment and Workspace runtime operations require an active entitlement.
Compute expiration destroys compute; storage expiration retains data but blocks
use until a new entitlement is purchased.

## Production

Production runs Control Plane, Fabric, and Ledger as separate Kubernetes
Deployments. Secrets are Kubernetes Secret references, configuration is a shared
ConfigMap, and the deploy workflow waits for all three rollouts. The single paid
production verifier uses the public Console product chain.
