# RP Replay And Ledger Closeout Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Re-admit A1 Candidate identity, C1 Runtime pricing SSOT, and C2 Workspace Runtime ABI through decision-complete RPs, isolated PRs, canonical `main` evidence, and balanced child-ledger updates.

**Architecture:** Treat old PRs #379 and #380 as provenance only. Create one #356-shaped RP and one fresh-main worktree/PR per authority boundary, prepare independent lanes in parallel, serialize only canonical integration and shared documentation projections, and update the private ledger only from merged remote evidence.

**Tech Stack:** Git/Git worktrees, GitHub Issues and Pull Requests, GitHub Actions, TypeScript/Node contract tests, Go service tests, JSON contracts, GitHub Actions YAML, Dolt-backed OPL Ledger.

---

### Task 1: Admit A1, C1, and C2 through decision-complete RPs

**Files:**
- Reference: `docs/plans/2026-08-20-rp-replay-and-ledger-closeout-design.md`
- Reference: `docs/README.md`
- Reference: `docs/architecture.md`
- Reference: `docs/decisions.md`
- Reference: `docs/status.md`
- Reference: `docs/roadmap.md`

**Step 1: Draft three independent Issue bodies**

For A1, C1, and C2, include the nine required #356 sections: decision, problem, confirmed facts, separated facts/semantics, minimal implementation, migration/deletion order, retained safety gates, acceptance, and terminal state.

**Step 2: Check scope separation**

Confirm that:

- A1 contains only Candidate identity/distribution work;
- C1 contains only Control Plane current pricing and accepted purchase snapshots;
- C2 contains only the fixed Workspace WebUI port Runtime ABI;
- no RP authorizes Release, Instance deployment, production mutation, or ledger completion.

**Step 3: Create the RPs**

Run one authenticated `gh issue create --repo gaofeng21cn/one-person-lab-cloud` command per body.

Expected: three new open Issues with distinct numbers and URLs.

**Step 4: Read every RP back**

Run:

```bash
gh issue view <RP_NUMBER> --repo gaofeng21cn/one-person-lab-cloud --json number,title,body,state,url
```

Expected: exact title/body equality, `state=OPEN`, and all nine sections present.

**Step 5: Record RP identities in the three execution lanes**

Use the issue number in branch/PR descriptions and later ledger evidence. Do not mark any ledger entry done.

### Task 2: Replay A1 Candidate identity from fresh `main`

**Files:**
- Modify: `.github/workflows/build-opl-cloud-candidate.yml`
- Modify: `docs/implementation-architecture.md`
- Modify: `docs/roadmap.md`
- Modify: `docs/runtime/production-runbook.md`
- Modify: `docs/status.md`
- Modify: `packages/contracts/opl-cloud-candidate-receipt-contract.json`
- Modify: `packages/contracts/opl-cloud-distribution-contract.json`
- Modify: `tests/contracts/product-candidate-receipt.test.ts`
- Modify: `tests/contracts/product-candidate-workflow.test.ts`
- Modify: `tests/contracts/product-distribution.test.ts`
- Modify: `tests/tools/cloud-candidate-receipt.test.ts`
- Modify: `tools/cloud-candidate-receipt.ts`
- Modify: `tools/validate-product-boundary.mjs`
- Include: `docs/plans/2026-08-20-rp-replay-and-ledger-closeout-design.md`
- Include: `docs/plans/2026-08-20-rp-replay-and-ledger-closeout.md`

**Step 1: Reconstruct the old intent**

Compare commits `6fbb3bd9`, `a7a71524`, and `ffbf9ef5` with fresh `origin/main`. Keep the exact Candidate V2 identity chain and rerun-unique artifact behavior; reject stale progress claims and unrelated publication changes.

**Step 2: Apply the smallest semantic replay**

Replay the three commits in order, resolve conflicts against current contract owners and callers, and inspect every documentation conflict instead of taking an old blob wholesale.

**Step 3: Run focused Candidate verification**

Run:

```bash
node --test tests/contracts/product-candidate-receipt.test.ts tests/contracts/product-candidate-workflow.test.ts tests/contracts/product-distribution.test.ts tests/tools/cloud-candidate-receipt.test.ts
npm run validate:product-boundary
```

Expected: all Candidate/distribution tests and the product-boundary validator pass.

**Step 4: Run the aggregate gate**

Run:

```bash
npm run verify:local:full
git diff --check origin/main...HEAD
```

Expected: full local verification passes and no whitespace errors exist.

**Step 5: Commit and push the recoverable lane**

Push `codex/opl-tnw-a1-candidate-identity-replay` without force, then read back the remote SHA and tree.

**Step 6: Create the A1 PR**

Create an ordinary PR linked to the A1 RP. State focused/full verification and explicitly defer Release, Instance, A2-A5, and ledger completion.

### Task 3: Replay C1 Runtime pricing SSOT in its own worktree

**Files:**
- Modify: `packages/contracts/opl-cloud-pricing-contract.json`
- Modify: `services/control-plane/internal/server/ent_state_store_test.go`
- Modify: `services/control-plane/internal/server/ent_state_store_workspace.go`
- Modify: `services/control-plane/internal/server/pricing.go`
- Modify: `services/control-plane/internal/server/pricing_monthly_test.go`
- Modify: `services/control-plane/internal/server/workspace_launch_activation.go`
- Modify: `services/control-plane/internal/server/workspace_launch_reconciler_test.go`
- Modify: `tests/contracts/current-product-truth.test.ts`
- Modify: `tests/contracts/monthly-billing-hard-cut.test.ts`
- Modify after fresh reconciliation: `docs/status.md`
- Modify after fresh reconciliation: `docs/roadmap.md`

**Step 1: Create the isolated lane**

Create `codex/opl-tnw-c1-runtime-pricing-replay` from the then-current `origin/main` in a new worktree.

**Step 2: Write or restore the focused failing assertions**

Assert that the versioned Control Plane Runtime catalog is the only current customer-price owner, accepted purchases persist immutable compute/storage snapshots, storage price lookup is versioned and block-size bound, and catalog changes do not reprice accepted periods.

**Step 3: Replay only C1 behavior**

Use `d0e92c6c`, `20823450`, `30e3a1e1`, and `fc3ce7fb` as provenance. Do not introduce the C2 ABI contract, port changes, or C2 documentation.

**Step 4: Run focused pricing verification**

Run:

```bash
go test ./services/control-plane/internal/server -run 'Test.*(Pricing|Price|Snapshot|WorkspaceLaunch)'
node --test tests/contracts/current-product-truth.test.ts tests/contracts/monthly-billing-hard-cut.test.ts
```

Expected: current catalog and accepted-snapshot cases pass.

**Step 5: Reconcile evidence documents and run the aggregate gate**

Update only current implementation evidence and remaining gaps, then run:

```bash
npm run verify:local:full
git diff --check origin/main...HEAD
```

Expected: full local verification passes and the diff contains no C2 Runtime ABI files.

**Step 6: Commit, push, read back, and create the C1 PR**

Link the PR to the C1 RP and explicitly preserve already accepted Workspace price facts and all existing billing/provider safety gates.

### Task 4: Replay C2 Workspace Runtime ABI in its own worktree

**Files:**
- Modify: `docs/implementation-architecture.md`
- Modify: `packages/contracts/README.md`
- Create: `packages/contracts/opl-cloud-workspace-runtime-abi-contract.json`
- Modify: `services/control-plane/internal/server/app_state.go`
- Modify: `services/control-plane/internal/server/security_bounds_test.go`
- Modify: `services/fabric/internal/fabric/local_docker_runtime.go`
- Modify: `services/fabric/internal/fabric/tencent_provider.go`
- Create: `services/fabric/internal/fabric/workspace_runtime_abi_test.go`
- Modify: `services/fabric/ops/production-readiness.ts`
- Create: `tests/contracts/workspace-runtime-abi.test.ts`
- Modify: `tests/production/production-readiness.test.ts`
- Modify after fresh reconciliation: `docs/status.md`
- Modify after fresh reconciliation: `docs/roadmap.md`

**Step 1: Create the isolated lane**

Create `codex/opl-tnw-c2-workspace-runtime-abi-replay` from the then-current `origin/main` in a new worktree.

**Step 2: Write or restore the focused failing assertions**

Assert that Control Plane, Local-Docker, Tencent/TKE, and production readiness all consume the same versioned ABI fact: Workspace WebUI listens on fixed container port `3000`. Assert that no deployment environment variable can redefine it.

**Step 3: Replay only C2 behavior**

Use `b4cce861` as provenance. Do not change pricing contracts, accepted purchase snapshots, or Control Plane pricing code.

**Step 4: Run focused Runtime ABI verification**

Run:

```bash
go test ./services/control-plane/internal/server -run 'Test.*(Runtime|Port|Security)'
go test ./services/fabric/internal/fabric -run 'Test.*WorkspaceRuntimeABI'
node --test tests/contracts/workspace-runtime-abi.test.ts tests/production/production-readiness.test.ts
```

Expected: every consumer agrees on the versioned fixed port and rejects a duplicate configuration owner.

**Step 5: Reconcile evidence documents and run the aggregate gate**

Run:

```bash
npm run verify:local:full
git diff --check origin/main...HEAD
```

Expected: full local verification passes and the diff contains no C1 pricing files.

**Step 6: Commit, push, read back, and create the C2 PR**

Link the PR to the C2 RP and state that the fixed port is an image/runtime ABI, not a production deployment claim.

### Task 5: Integrate the three PRs against fresh canonical truth

**Files:**
- Reconcile as needed: `docs/implementation-architecture.md`
- Reconcile as needed: `docs/status.md`
- Reconcile as needed: `docs/roadmap.md`

**Step 1: Wait for required CI on all current PR heads**

Expected: dependency review and validation succeed for each exact PR head.

**Step 2: Merge A1 and read back canonical identity**

Merge the A1 PR through the repository's normal GitHub route. Fetch `origin/main`, record the merge commit/tree, and verify that the Candidate contract/workflow/tool blobs are reachable.

**Step 3: Refresh C1 against the new main**

Fetch, semantically replay C1 if needed, rerun affected focused/full gates, push without force when possible, wait for current CI, merge, and read back the pricing contract/source/test blobs.

**Step 4: Refresh C2 against the new main**

Repeat the same currentness and verification procedure, then read back the Runtime ABI contract and every current consumer.

**Step 5: Verify final remote parity**

Run:

```bash
git fetch origin main
git rev-parse origin/main
git rev-parse 'origin/main^{tree}'
gh pr view <PR_NUMBER> --json state,mergedAt,mergeCommit,statusCheckRollup,url
```

Expected: all three PRs are merged, current CI is successful, and final remote `main` contains all three non-overlapping outcomes.

**Step 6: Clean task-owned execution surfaces**

Remove the three worktrees and delete only their absorbed local and remote branches. Confirm the primary checkout is clean; preserve old PRs/issues/commits as GitHub provenance.

### Task 6: Update and balance the private child ledger

**Files:**
- Modify through the Ledger owner API/CLI: private Program `opl-tnw` entries A1, C1, and C2
- Read only: remote canonical Cloud commit/tree/blob evidence
- Read only: private Ledger Program rollup and supervisor validation

**Step 1: Build one evidence packet per child entry**

Each packet contains its RP URL, merged PR URL, merge commit, final canonical commit/tree, decisive blob/readback, focused/full verification, and remaining dependent work.

**Step 2: Close A1, C1, and C2 independently**

Set an entry to done only when its own evidence packet is complete. Do not infer Epic or Program completion and do not close A2-A5, C3-C6, or any Epic B entry.

**Step 3: Recompute rollups**

Expected minimum rollups after exactly these closures:

- Epic A: A1 closed; A2-A5 remain open.
- Epic C: C1 and C2 closed; C3-C6 remain open.
- Epic B: unchanged.

Use the Ledger's own weighting/rollup rules rather than deriving percentages manually.

**Step 4: Verify Ledger validity and parity**

Read back the child entries, Program rollup, dependency graph, `validation_errors=0`, Git working-tree parity, and Dolt push/pull parity.

Expected: the private ledger and canonical Cloud evidence agree, with no entry credited from old PRs alone.

**Step 5: Select the next ready child task**

Read the recomputed dependency graph and choose the highest-priority `ready` child. Open its session only after the ledger shows a real executable next action and a non-overlapping owner/write set.

