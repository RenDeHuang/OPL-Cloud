# OPL Cloud Documentation

This repository follows the `one-person-lab` documentation lifecycle.

## Source Of Truth

- Product target and public architecture: this repository's
  [architecture.md](./architecture.md), whitepaper and roadmap.
- Current implementation: [implementation-architecture.md](./implementation-architecture.md),
  [invariants.md](./invariants.md), `packages/contracts`, source, tests, runtime
  readback and deployment manifests.
- Development framework truth: `one-person-lab`.
- First instance truth: `opl-instance-medopl` after extraction; current medopl
  values co-located here are migration state.

Human docs explain the system. They do not replace machine contracts, runtime
readback or owner acceptance. `opl-cloud` is an internal artifact and service
identifier, not another repository.

## Active Docs

- [project.md](./project.md): repository scope and ownership.
- [architecture.md](./architecture.md): target product and authority boundaries.
- [implementation-architecture.md](./implementation-architecture.md): current implemented request, persistence, provider and production boundaries.
- [invariants.md](./invariants.md): rules that must stay true across refactors.
- [status.md](./status.md): current launch boundary and known gaps.
- [decisions.md](./decisions.md): durable decisions.
- [roadmap.md](./roadmap.md): the single current gap and next-step owner.
- [whitepapers/opl-cloud-whitepaper.md](./whitepapers/opl-cloud-whitepaper.md): public product whitepaper source.
- [product/console-workspace-v1.md](./product/console-workspace-v1.md): OPL Console commercial workspace product.
- [runtime/production-runbook.md](./runtime/production-runbook.md): production operations.
- [runtime/tke-production-deployment.md](./runtime/tke-production-deployment.md): Tencent TKE deployment contract.
- [policies/docs-lifecycle-policy.md](./policies/docs-lifecycle-policy.md): active documentation, contract, and test lifecycle.
- [policies/development-worktree-policy.md](./policies/development-worktree-policy.md): worktree, branch, stash, and repository size rules.

## History

Dated plans, design freezes, run evidence, closeout notes, and completed implementation ledgers belong under `docs/history/**`.

Active docs must not become process ledgers.

## Rules

1. Keep durable product rules in docs and machine-readable contracts.
2. Keep dated implementation evidence in history.
3. Do not preserve compatibility wrappers after active callers move to the current surface.
4. Do not test prose wording.
5. Promote temporary tests into contract-driven tests or delete them.
