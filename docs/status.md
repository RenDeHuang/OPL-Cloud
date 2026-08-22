# OPL Cloud Current Status

Owner: `one-person-lab-cloud`
Purpose: `replaceable_current_evidence_snapshot`
State: `current_snapshot`

This file reports current implementation and the latest retained evidence. It
is not a chronological work log. Target architecture lives in
[architecture.md](./architecture.md); open outcomes live in
[roadmap.md](./roadmap.md).

## Product Cut

The current Pilot is an administrator-provisioned account product. One Console
User maps to one Account and one Sub2API identity/wallet. An Account may own
multiple independent Workspaces. Public registration, customer payment/top-up,
shared multi-user Workspaces, HA, and GPU are not current customer capabilities.

Basic and Pro are visible Workspace packages with integer USD-micros pricing.
Control Plane owns quotes and purchase eligibility; Sub2API owns spendable
balance, Keys, routing, and usage. The current offer set is Basic and Pro, and
Console and server admission both accept an available balance exactly equal to
the authoritative quote.

Console calls Control Plane product APIs. Control Plane, Fabric, and Ledger are
separate processes and PostgreSQL schema owners. Fabric provides Local-Docker
and Tencent/TKE adapters behind the same provider-neutral boundary. Tencent/TKE
is selected and configured by the medopl instance, not by the portable product.

## Implemented Paths

- Workspace Launch persists one Control Plane operation and coordinates Key,
  debit, Fabric resource stages, activation, and one purchase Receipt. Unknown
  external results enter manual review and resume through the same operation.
- Source now keeps Tencent compute provisioning typed and pending for a
  persisted ten-minute window. A Ready Machine with recoverable ownership
  becomes compute-only `ownership_pending`; one durable same-key continuation
  discovers that Machine and claims CVM/Node ownership without a second
  NodePool scale. The existing administrator Resume route can return only the
  historical matching compute-unknown operation to this same Reconciler after
  authoritative `provider_provisioning` or `ownership_pending` readback. One
  terminal failed compatibility replay can be replaced exactly once by a new
  single-use administrator authorization without changing the original stage,
  attempt, Fabric operation, resource binding, or idempotency key. Local Fabric,
  Control Plane, route, and PostgreSQL tests cover the original attempt, delayed
  readiness, fail-closed conflicts, downstream stages, and one Receipt.
- Candidate A implements a narrow audited repair for the historical schema-3
  Launch defect whose only missing canonical fact is `specDigest`. Fabric
  strictly reads the persisted preflight binding; Control Plane previews the
  exact two-field semantic change and atomically writes only the repaired
  operation result plus one audit event under CAS and idempotency protection.
  Local focused and full PostgreSQL gates pass. No production repair has been
  executed, and the repair intentionally preserves `stage=debit` and
  `status=manual_review`; business-state convergence remains separate work.
- Workspace Delete is permanent under the current contract. It removes the
  owned runtime/resource path, performs no refund or wallet mutation, and writes
  `workspace.deleted.v1` after authoritative cleanup.
- Fabric now exposes typed Local-Docker and Tencent/TKE Runtime delete
  observations, and Control Plane keeps Delete fail closed until the typed
  Runtime, Secret, and resource facts confirm absence. Asymmetric PV/PVC
  residue and the complete Tencent Gateway Secret deletion path remain open
  outcomes rather than proven cleanup.
- Fabric persists `compute_pool_key`, FIFO pool-head ownership, lease owner, and
  lease expiry for compute allocation. Memory and PostgreSQL-focused source
  tests cover same-pool serialization, cross-pool parallelism, database-clock
  fencing, and crash recovery. Current Control Plane admission remains one
  in-flight Launch, so a higher-concurrency Tencent receipt is trigger-gated.
- A successful Launch Receipt is now projected to the Workspace inside the
  owning PostgreSQL CAS transaction. Exact replay preserves the same Receipt;
  a different Receipt or mismatched Account/Workspace identity fails closed.
- Control Plane exposes a protected, read-only disposable Launch reset preview.
  It composes redacted Control Plane, Fabric/provider, Kubernetes, Sub2API, and
  Ledger observations into a deterministic plan digest. It performs no reset,
  refund, Key deletion, provider deletion, Ledger append, or terminalization.
- Workspace renewal authorization is persisted and exposed through Control
  Plane and Console. Expired-Workspace reactivation and live renewal evidence
  are not implemented end to end.
- Fabric persists provider-neutral Launch stage bindings and request hashes
  before provider writes. Local-Docker and Tencent/TKE project the fixed
  Workspace WebUI `http/3000` compatibility fact.
- Fabric operation reads use bounded lookup and cursor pagination, and repeated
  job heartbeats reuse one mutable operation identity. A fresh sealed external
  scan has not yet closed the earlier resource-history finding.
- Portable Compose separates Ledger, Fabric, and Control Plane credentials,
  databases, and service tokens. The Local-Docker override grants Docker Engine
  access to Fabric only and requires an immutable Workspace image.
- Candidate tooling builds one `linux/amd64` + `linux/arm64` Cloud index and a
  checksum-bound installation bundle. Candidate manifests bind source, image,
  child manifests, assets, and workflow provenance without selecting an
  installation domain, Provider Profile, or Workspace image.
- Ledger owns the append-only Cloud Evidence Index for operation identity,
  Candidate identity, receipt identity, status, and redacted export links.
  Control Plane billing reconciliation uses one transaction and the same
  purchase-admission lock as Workspace Launch; a committed mismatch blocks new
  purchases without changing Ledger's evidence ownership.
- The active machine contracts are Candidate identity, Distribution identity,
  Control Plane/Fabric Launch request hashing, and the Workspace Runtime ABI.
  Other current behavior is owned by source, public APIs, schemas, and focused
  tests.

## Local Runtime Evidence

Separate Local-Docker runs on 2026-08-19 exercised both ownership modes on
`linux/arm64`:

- `customer_owned + local-docker` created two independent running Workspaces,
  preserved them across control-service restart, and completed a real model
  request whose Sub2API usage increased by exactly one request.
- `platform_owned + local-docker` created one prepaid Workspace, one Workspace
  Key, one `52,580,000` USD-micros debit, and one linked purchase Receipt. A
  failed first Runtime was repaired by replacing only that Runtime while
  retaining the confirmed Key, debit, compute, storage, attachment, and Secret.
  Exact replay did not duplicate those resources or the Receipt. The repaired
  Workspace completed a real model request and survived Control Plane restart.

The retained evidence is outside Git under
`/Users/huangrende/Desktop/opl-cloud/evidence/2026-08-18-v2` and
`/Users/huangrende/Desktop/opl-cloud/evidence/2026-08-19-platform-owned-repair`.
Tokens and login cookies are not retained.

These runs do not form one exact-current clean-host create/delete journey. They
do not prove final resource and Key absence, zero Delete wallet mutation, and
the deletion Receipt for the same Candidate.

## Distribution Evidence

`v0.1.7` is the only retained public Product Release. It was published from
product SHA `a59bde68397528186a5220f73195fa1f3eda311b` as multi-architecture
index `sha256:e64504731f8b61c0864cf59faa647a1150e8a2a5eada34b26faf3a5487d28e8f`.
Its five installation assets match their API digests and `SHA256SUMS`.

That proves the public bytes of `v0.1.7`; it does not prove current `main`, a
clean installation, or medopl production readiness. No hosted run has yet built
and qualified the current portable Candidate, and the formal Release workflow
still rebuilds instead of promoting previously qualified bytes.

## Instance Evidence

`opl-instance-medopl` owns the medopl profile, production workflow, Secrets,
deployment, verification, rollback, and receipts. Retained evidence shows an old
Candidate reached a successful deployment mutation and later standalone
deployment verification. The latest recorded generic admission attempt was
blocked at customer login with HTTP `503`.

There is no receipt set for one current portable Candidate that proves generic
admission, a normal purchase, post-activation Runtime/provider/billing readback,
an executed and verified rollback, and `workspace_verified`. Cloud therefore
does not claim current Instance qualification or production readiness.

## Repository Security Evidence

The latest retained GitHub readback reports private vulnerability reporting,
Dependabot alerts and updates, secret scanning and push protection, full-SHA
Actions pinning, strict required `validate` and `dependency-review` checks, and
force-push/deletion protection on `main`. Secret validity checks were still
reported disabled after an attempted settings change.

Current source includes request-size limits, separate Control Plane and runner
transport identities, scoped Fabric capabilities, immutable Local-Docker image
admission, same-origin browser login admission, and bounded Fabric operation
history. These are source controls; their external alert and scan state remains
separate.

## Readiness Summary

Source and local tests cover the current service boundaries, money and
idempotency rules, persistence paths, provider adapters, portable assets, and
Local-Docker lifecycle. The accepted public-beta target additionally requires
public registration, lifecycle recovery, operator/data/alert closure, and the
same-byte Candidate/Release path. Their current evidence and open outcomes are
projected through the A-N packages in [roadmap.md](./roadmap.md); none is
upgraded by an older Candidate or Instance receipt.

The evidence meanings are defined in [invariants.md](./invariants.md). A test,
document, Candidate, Release, or Instance receipt proves only the layer and exact
identity it names.
