# OPL Cloud Development Rules

Before changing billing, Fabric, Workspace, Gateway, Ledger, deployment, or E2E:

1. Read `docs/invariants.md` completely.
2. Read `packages/contracts/opl-cloud-launch-freeze-contract.json`.
3. Read the current machine contract owned by the service being changed.
4. Preserve the approved boundary and update the slide's current state only with matching code, tests, and runtime evidence.

Hard prohibitions:

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
