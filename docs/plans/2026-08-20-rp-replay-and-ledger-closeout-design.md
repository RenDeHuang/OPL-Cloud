# RP Replay And Ledger Closeout Design

**Status:** Approved for implementation

**Baseline:** `origin/main` at `5e9fb95ba75d67bcaf676506deea4a6b18fc7e92`

**Objective:** Re-admit the completed implementation work behind withdrawn PRs #379 and #380 through decision-complete RPs modeled on Issue #356, three independently reviewable PRs, canonical `main` readback, and evidence-backed child-ledger closeout.

## Decision

Use three RPs and three PRs:

1. **A1 Candidate identity:** replay the #379 implementation that binds one portable Cloud Candidate to the canonical Cloud SHA/tree, OCI index and platform digests, installation assets, checksums, and rerun-unique receipts.
2. **C1 Runtime pricing SSOT:** replay only the price-catalog and accepted-price-snapshot work from #380.
3. **C2 Workspace Runtime ABI:** replay only the fixed Workspace WebUI port `3000` ABI work from #380.

Do not reopen the old PRs, restore their old branches, or treat their green checks as current completion evidence. Their commits and CI runs are provenance used to reconstruct intent and tests on fresh `main`.

## Why The Work Is Split

A1, C1, and C2 have different authority boundaries, write sets, and failure modes:

- A1 is owned by product distribution contracts, Candidate workflow, receipt tooling, and publication documentation.
- C1 is owned by Control Plane billing semantics and the versioned pricing contract. It must preserve immutable prices already accepted by a Workspace purchase while allowing the current catalog to evolve.
- C2 is a cross-module Runtime ABI owned by `packages/contracts` and consumed by Control Plane and Fabric. Fixed port `3000` is an image/runtime compatibility fact, not deployment configuration.

Combining C1 and C2 would make an unrelated billing correction depend on a Runtime network contract and would recreate the coupling that Epic C is intended to remove. The three PRs may be prepared in parallel; only canonical integration and shared documentation projections are serialized.

## RP Contract

Every RP follows Issue #356 and contains these sections:

- decision conclusion;
- current problem;
- confirmed implementation facts;
- facts and semantics that must remain separate;
- minimal implementation;
- migration or deletion order;
- safety gates that must remain;
- acceptance criteria;
- issue terminal state.

An RP authorizes only ordinary development and review. It does not authorize a Product Release, Instance deployment, production mutation, private-network access, provider purchase/deletion, or ledger completion.

## Execution And Integration

Each lane starts from fresh `origin/main` in its own short-lived worktree and branch. Replay is semantic: compare the old commits with current owners and callers, apply only the lane's intended behavior, regenerate derived projections from current inputs, and discard stale documentation claims.

The integration sequence is:

1. locally validate each isolated lane;
2. push each task branch and read back its SHA/tree;
3. create one PR per RP and wait for required CI;
4. merge A1 first because it also carries this approved execution record;
5. refresh C1 and C2 against the new canonical `main`, resolve only real shared-document conflicts, rerun affected gates, and merge them one at a time;
6. read back final remote `main` commit/tree and the relevant canonical blobs;
7. remove task-owned worktrees and branches after absorption is proven.

No lane is complete because a local test, old CI run, task commit, branch, or PR is green. Completion begins only after its bytes are reachable from remote canonical `main`.

## Failure Handling

- If an RP is incomplete, correct the RP before implementation or PR creation.
- If semantic replay conflicts with current architecture or a real caller, return to the canonical owner and revise the smallest affected lane; do not choose an old blob mechanically.
- If local verification fails, repair the first reproducible breakpoint before pushing.
- If a GitHub mutation returns an unknown result, perform read-only reconciliation before any retry.
- If `main` advances, fetch it, replay the lane semantically, and rerun the affected verification.
- If CI fails, diagnose the current PR head and current job logs; old green checks cannot supersede a new failure.

## Verification

Every lane runs focused contract and owner tests first, followed by `npm run verify:local:full` because the changes affect shared contracts, persistence semantics, workflows, or cross-module behavior. Before integration, also run `git diff --check` and inspect the exact write set.

GitHub completion evidence consists of the approved RP, PR head/base identity, required CI success, merge commit, and remote canonical commit/tree/blob readback. Ledger completion evidence references those canonical facts, not the old PRs.

## Ledger Closeout

The private ledger remains a projection of authoritative product evidence:

- mark A1 done only after the A1 PR is merged and its Candidate identity contracts and tests are read back from canonical `main`;
- mark C1 done only after the C1 PR is merged and current pricing plus accepted-snapshot semantics are read back;
- mark C2 done only after the C2 PR is merged and all Runtime ABI consumers resolve port `3000` from the single contract;
- preserve old #379/#380/#381 and old sessions as provenance, not completion owners;
- after each child-ledger mutation, verify the Program rollup, supervisor validation, Git parity, and Dolt push/pull parity.

The next ledger work starts only after these three entries reconcile with canonical Cloud evidence.

## Non-Goals

- No Product Release or successor version publication.
- No Instance deployment or production qualification claim.
- No A2-A5 implementation.
- No C3-C6 implementation.
- No billing entitlement, auto-renew, or full Workspace commercial lifecycle implementation from Epic B.
- No restoration of the combined #380 PR boundary.

