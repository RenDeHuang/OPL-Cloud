# OPL Cloud Development Rules

This is the single `one-person-lab-cloud` product repository. It owns the OPL
Cloud public architecture, whitepaper, roadmap, Console, Control Plane, Fabric,
Ledger, Workspace delivery, contracts and reusable release mechanisms.

- `docs/roadmap.md` is the only current gap and next-step owner.
- `docs/architecture.md` owns target product and authority boundaries.
- `docs/implementation-architecture.md`, `docs/invariants.md`,
  `packages/contracts`, source and tests own current implementation truth.
- `opl-cloud` remains the internal package, image, service, namespace and runner
  identifier; it is not a second repository owner.
- `opl-instance-medopl` owns the eventual medopl instance profile and deployment
  evidence. Co-located medopl configuration is migration state, not product
  architecture.
- The archived documentation repository is provenance only and must never
  become a parallel current writer.

Before changing billing, Fabric, Workspace, Gateway, Ledger, deployment, or E2E:

1. Read `docs/invariants.md` completely.
2. Read `packages/contracts/opl-cloud-launch-freeze-contract.json`.
3. Read the current machine contract owned by the service being changed.
4. Preserve the approved boundary and update the slide's current state only with matching code, tests, and runtime evidence.

Hard prohibitions:

- The local WSL development machine cannot access the production private network. Never attempt local direct access to production-internal endpoints, clusters, databases, or services. All production deployment and private-network verification must run through the repository's GitHub Actions workflows using the `production` environment and its authorized runner; local work is limited to code changes, workflow dispatch, and reading back GitHub evidence.
- Do not add a second wallet or Gateway service; Sub2API is the Gateway backend and spendable-balance owner.
- Do not introduce `POSTPAID_BY_HOUR` for customer or verification CVM/CBS resources.
- Do not buy or delete Tencent CVM/CBS resources during an ordinary CI, release, or E2E run.
- Do not charge a real monthly product fee during verification.
- Do not add a public test billing mode or clean up customer resources from verification code.

<!-- CODEGRAPH_START -->
## CodeGraph

- This repository uses a local `.codegraph/` index; never commit that directory.
- Prefer CodeGraph for definitions, callers, impact, and code paths; use `rg` for literal text.
- Run `codegraph init .` or `codegraph sync .` when the index is missing or stale.
<!-- CODEGRAPH_END -->
