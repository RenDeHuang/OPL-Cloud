# OPL Cloud Documentation

This repository follows the `one-person-lab` documentation lifecycle.

## Source Of Truth

- Product truth: `one-person-lab-cloud`.
- Development framework truth: `one-person-lab`.
- Machine truth: `packages/contracts`, tests, API surfaces, runtime readiness payloads, and deployment manifests.
- Human docs: this `docs` tree.

Human docs explain the system. They do not replace contracts or tests.

## Active Docs

- [project.md](./project.md): repository scope and ownership.
- [architecture.md](./architecture.md): product and implementation boundaries.
- [invariants.md](./invariants.md): rules that must stay true across refactors.
- [status.md](./status.md): current launch boundary and known gaps.
- [decisions.md](./decisions.md): durable decisions.
- [product/console-workspace-v1.md](./product/console-workspace-v1.md): OPL Console commercial workspace product.
- [runtime/production-runbook.md](./runtime/production-runbook.md): production operations.
- [runtime/tke-production-deployment.md](./runtime/tke-production-deployment.md): Tencent TKE deployment contract.
- [active/contract-and-test-lifecycle-audit.md](./active/contract-and-test-lifecycle-audit.md): current cleanup audit.

## History

Stage plans, dated design freezes, run evidence, closeout notes, and completed implementation ledgers belong under `docs/history/**`.

Active docs must not become process ledgers.

## Rules

1. Keep durable product rules in docs and machine-readable contracts.
2. Keep dated implementation evidence in history.
3. Do not preserve compatibility wrappers after active callers move to the current surface.
4. Do not test prose wording.
5. Promote temporary tests into contract-driven tests or delete them.
