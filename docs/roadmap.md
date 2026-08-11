# OPL Cloud Roadmap And Current Gaps

Owner: `one-person-lab-cloud`
Purpose: `single_active_truth_plan`
State: `active_planning`
Machine boundary: This document owns only the current human-readable Cloud
planning summary, open product/structure gaps, evidence boundary, and next Agent
prompt. It does not prove service implementation, runtime health, billing,
security, release, owner acceptance, or production readiness.

## Target State

OPL Cloud extends OPL work from a local App into zero or more independent online
Workspaces per account, approved AI/resource use, optional exact Agent Service
publication, and inspectable evidence continuity. Cloud products consume Package-owner
identity/publication refs, native-carrier readback, Framework aggregation and
execution refs, and domain-owner judgments without creating competing truth.

This repository is the OPL Cloud product and implementation owner. Target truth
belongs to the product docs; current implementation truth belongs to the named
service source, machine contract, tests and runtime readback. Gateway, App,
Framework, Runway, package and domain truth remains with their corresponding
external owners.

## Current Status Summary

| Theme | Current state | Boundary |
| --- | --- | --- |
| Repository role | `product_and_implementation_owner` | Product docs and reusable implementation share one repository but retain distinct evidence levels |
| Product split | `documented` | Gateway, Workspace, Serve, Console, Fabric, and Ledger have one target responsibility owner each |
| Framework boundary | `documented` | OPL Packages aggregates owner descriptors and native carrier state; OPL Runway owns invocation/session execution; Cloud consumes refs |
| Domain boundary | `documented` | Domain Agents retain professional truth, quality, artifact, and delivery authority |
| Workspace identity | `decided` | One account may own 0..N independent Workspaces; Services remain separate deployment resources |
| Implementation owner | `unified` | `one-person-lab-cloud` owns Console, Control Plane, Fabric, and Ledger implementation; `opl-cloud` is an internal artifact/service identifier |
| Instance owner | `defined_pending_materialization` | `opl-instance-medopl` will own the first commercial instance profile and deployment evidence without copying runtime code |
| Active documentation | `consolidated` | This file owns current gaps/next prompt; public product map stays in the root README and the technical split stays in architecture |
| Whitepaper | `source_and_build_profile_present` | Source/build evidence does not prove publication or Cloud service readiness |
| Service delivery | `candidate_not_production_proven` | Current state is owned by `docs/status.md`, machine contracts, CI, deployment readback and owner evidence |

## Functional And Structural Gaps

| Gap | Target outcome | Owner route |
| --- | --- | --- |
| Workspace continuity | One project/task/artifact model works locally and online | App + Workspace implementation owners |
| Console user platform | Self-service onboarding, balance/usage view, 0..N Workspace lifecycle, support, and administrator governance share one tenant-safe product surface | `one-person-lab-cloud` Console + Control Plane |
| Managed-resource policy | Account approval and quotas stay distinct from package, service, and resource mutation | `one-person-lab-cloud` Console + Control Plane |
| Provider-neutral Fabric | Product contracts and launch/recovery use provider-neutral facts; Tencent TKE, local Docker, and generic Kubernetes remain adapters | `one-person-lab-cloud` Fabric |
| Instance separation | Medopl domains, provider profile, enabled plans/prices, image pins, secret refs, and deployment receipts move out of reusable implementation code | `opl-instance-medopl` |
| Resource binding | Compute, storage, environments, and connectors share one plan/approve/execute/collect contract | `one-person-lab-cloud` Fabric |
| Balance settlement | Gateway stays the only spendable wallet; Console projects total account billing and settles each Workspace period; Fabric owns zero balance | `one-person-lab-cloud` Control Plane + Gateway + Ledger |
| Package projection | Every Cloud surface consumes exact owner publication refs plus fresh carrier state without a registry copy or Cloud writer | Package owner + native carrier + Framework aggregation + each consumer |
| Service publication | An exact package digest becomes a stable Service, immutable Revision, and controlled Deployment | OPL Serve + package/domain contract owners |
| Public service edge | External consumers receive authenticated, rate-limited API, event, and Webhook surfaces | OPL Serve Agent Edge + Console policy |
| Provider lifecycle | Native and approved external providers share one Invocation/Session contract | OPL Runway + Fabric/provider adapters |
| Hosted clients | API, Embed, and Hosted UI consume one Serve contract | OPL Serve |
| Connector boundary | Shared access stays generic while domain adapters keep domain semantics | OPL Connect + domain owner |
| Evidence continuation | Runs and service calls return owner refs, review status, and continuation without centralizing source data | OPL Ledger + workbench/Serve owners |

These are product-family gaps. A gap closes only after its named owner surface
exists and the target architecture can reference fresh machine, runtime or
implementation evidence without copying an external owner's truth.

## Evidence Gaps

Real App-to-Workspace continuation, self-service Console onboarding and payment,
local/self-hosted provider runtime paths, managed and institution-owned resource
paths, data-egress acceptance, connector/provider runtime evidence, public-edge
security and isolation, deployment health and rollback, Invocation/Session
streaming and cancellation, billing/quota readback, user-visible receipts,
production soak, release evidence, and owner acceptance remain later evidence
lanes.

Docs, planning contracts, generated projections, tests, or a rendered
whitepaper can close only their own layers. They cannot substitute for these
runtime, release, security, billing, domain, or owner-evidence lanes.

## Target Delivery Order

1. Preserve the current Tencent path behind a provider-neutral Fabric seam and
   prove one `local-docker` path before broadening provider routing.
2. Expand the Console from administrator-provisioned Pilot to tenant-safe user
   onboarding, Workspace lifecycle, balance/usage, and support.
3. Materialize `opl-instance-medopl` and extract the current medopl profile and
   deployment evidence without copying product code.
4. Close one resource execution path with explicit plan, approval, collection,
   settlement, and receipt.
5. Project exact owner Package identity/publication and fresh carrier
   status/actions into Cloud surfaces without a registry or lock copy.
6. Add exact Ledger/public readback for user-visible actions and artifacts.
7. Close a portable Service Entrypoint Contract and immutable Agent Revision.
8. Add a dedicated Agent Edge and API-only private-beta path.
9. Add the provider-neutral Runway port and one OPL-native provider adapter.
10. Add Hosted UI and Embed clients only through the same public API.
11. Add broader service plans only after live security, isolation, billing, and
    soak evidence exists.

## Explicit Non-Goals

- Do not rebuild package discovery, installation, lock, update, rollback, or
  repair in Cloud.
- Do not impose a fixed product-level Workspace count or collapse multiple
  Workspaces into an account singleton.
- Do not merge independently owned Workspaces into an implicit multi-tenant
  SaaS workbench.
- Do not expose a Workspace URL, sandbox, or provider session as a public Agent
  endpoint.
- Do not create separate execution behavior for API, Hosted UI, and Embed.
- Do not restore a second Cloud product, documentation, Console, Fabric, or
  Ledger repository as a parallel current writer.
- Do not treat policy approval, resource binding, receipt presence, document
  completeness, or artifact rendering as Agent, service, or production
  readiness.

## Next-Round Agent Prompt

### Goal

Close one owner-approved OPL Cloud functional or structural gap and fold the
result back into this single Active Truth without promoting code or tests into
runtime claims.

### Write Scope

- `docs/roadmap.md`
- the one existing product or planning-contract owner directly affected by the
  selected gap
- `docs/README.md` and root `README*` only if public navigation or product
  language materially changes
- the owning source, machine contract and focused tests when implementation is
  explicitly in scope

### Non-goals And Forbidden Scope

- no second Cloud product or implementation repository;
- no second package registry, lock, Invocation/Session store, domain verdict,
  or owner receipt;
- no parallel Cloud writer outside `one-person-lab-cloud` or the explicit
  non-code instance contract;
- no availability, release, billing, security, or production claim without
  exact owner evidence.

### Live Truth Inputs

- fresh `main`, worktree, dirty, ahead/behind, remote, and owner/write-set gate
  for every affected repository;
- current App and Framework contracts/read models for package, state/action,
  Workspace, and Runway boundaries;
- the selected service owner's machine contract, source/tests, runtime readback,
  blockers, and owner receipt;
- this index, architecture, focused product owner, and current roadmap.

### Required Actions

1. Select exactly one open row from `Functional And Structural Gaps` and name
   its owner surface.
2. Verify the current implementation and contract state from fresh owner
   evidence; do not infer it from Cloud prose.
3. Implement or update only the authorized owner surface and its focused tests.
4. Rewrite the focused Cloud target reference only where the owner boundary or
   public target changed.
5. Remove or rewrite the closed gap in this file; keep evidence-only tails in
   `Evidence Gaps`.
6. Update the docs index only when a current owner or entry changes.

### Verification Commands

- run this repository's affected focused/full validation;
- run the OPL Doc doctor against each changed repository as a risk map;
- scan all tracked Markdown relative links;
- run `git diff --check`;
- if whitepaper source/profile changes, run
  `node --experimental-strip-types scripts/build-opl-cloud-whitepaper.ts`.

### Completion Gate

- one structural gap is closed in its owning surface and no competing truth is
  introduced;
- current docs describe only fresh facts and remaining gaps;
- tests/docs evidence is not promoted to runtime, release, owner acceptance, or
  production readiness;
- changed repository `main` branches contain the final bytes and their task
  worktrees/branches are cleaned after absorption.

### Foldback Target

- current status, remaining gaps, evidence tails, and the next prompt return to
  `docs/roadmap.md`;
- stable product responsibility returns to `docs/architecture.md` or the one
  focused product/contract owner;
- navigation changes return to `docs/README.md` and public entry language only
  when needed.
