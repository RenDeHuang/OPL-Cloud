# Tencent/TKE Completion Overlays Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add four evidence-based Tencent/TKE completion diagrams with explicit owners, responsibilities, persistence boundaries, gaps, and next acceptance evidence.

**Architecture:** Keep diagrams 01-13 as target architecture views and add diagrams 14-17 as read-only projections of current source, `docs/status.md`, and `docs/roadmap.md`. Render each Mermaid source to a same-named SVG without changing any canonical current-state owner or production resource.

**Tech Stack:** Markdown, Mermaid 11.x, SVG, Playwright/Chrome render verification, npm repository verification.

---

### Task 1: Add the four Mermaid source diagrams

**Files:**
- Create: `docs/plans/opl-cloud/diagrams/14-tencent-tke-completion-overview.mmd`
- Create: `docs/plans/opl-cloud/diagrams/15-tencent-tke-launch-persistence-chain.mmd`
- Create: `docs/plans/opl-cloud/diagrams/16-tencent-tke-recovery-delete-reconciliation.mmd`
- Create: `docs/plans/opl-cloud/diagrams/17-tencent-tke-candidate-release-evidence-chain.mmd`

**Step 1: Write the overview source**

Show the fixed Input/Output, customer chain, bounded contexts, TKE extension,
status levels, owner duties, P0/P1 gaps, and Later scope. Reconcile any current
document conflict through source, focused tests, history, and the canonical owner
before finalizing the overlay.

**Step 2: Write the Launch persistence source**

Show `preflight -> key -> debit -> compute -> storage -> attachment -> secret ->
runtime -> activation -> receipt`. For each write boundary state what is persisted
before the external call and which authoritative readback permits progression.

**Step 3: Write the recovery/delete/reconciliation source**

Show the finite read budget, CAS claims, manual review, exact owner absence order,
zero Delete wallet mutation, append-only deletion Receipt, and reconciliation guard.

**Step 4: Write the Candidate/Release evidence source**

Show one exact SHA/digest, Local-Docker receipt, medopl Tencent/TKE receipt,
executed rollback, `workspace_verified`, and exact-byte promotion. Mark the current
P0 blockers with their exact roadmap IDs.

**Step 5: Inspect labels**

Run:

```bash
rg -n 'Owner:|职责:|状态:|Gap:|Next Evidence:' docs/plans/opl-cloud/diagrams/1[4-7]-*.mmd
```

Expected: every primary capability node contains the required ownership and evidence fields.

### Task 2: Update the package index and reading guidance

**Files:**
- Modify: `docs/plans/opl-cloud/README.md`

**Step 1: Add index rows 14-17**

State the exact question answered by each image and its canonical owners.

**Step 2: Add evidence legend and TKE reading order**

Explain that 01-13 are target views, 14-17 are completion overlays, evidence levels
are not percentages, and Instance state can only be raised by Instance-owner receipts.

**Step 3: Add gallery entries**

Add same-named SVG links for diagrams 14-17.

**Step 4: Verify links and filenames**

Run:

```bash
for source in docs/plans/opl-cloud/diagrams/1[4-7]-*.mmd; do
  test -f "${source%.mmd}.svg"
done
```

Expected before rendering: failure because SVG files do not exist yet; after Task 3: success.

### Task 3: Render and inspect SVG files

**Files:**
- Create: `docs/plans/opl-cloud/diagrams/14-tencent-tke-completion-overview.svg`
- Create: `docs/plans/opl-cloud/diagrams/15-tencent-tke-launch-persistence-chain.svg`
- Create: `docs/plans/opl-cloud/diagrams/16-tencent-tke-recovery-delete-reconciliation.svg`
- Create: `docs/plans/opl-cloud/diagrams/17-tencent-tke-candidate-release-evidence-chain.svg`

**Step 1: Render all four sources with Mermaid 11.17.0**

Use the existing Playwright package and system Chrome. Load the fixed Mermaid ESM,
apply `docs/plans/opl-cloud/diagrams/mermaid-config.json`, render each source, and
write the same-named SVG.

**Step 2: Validate SVG XML**

Run:

```bash
for svg in docs/plans/opl-cloud/diagrams/1[4-7]-*.svg; do
  xmllint --noout "$svg"
done
```

Expected: all files parse successfully.

**Step 3: Render SVG previews to PNG and inspect**

Open each SVG through Playwright at a wide viewport, capture a full-element PNG,
and inspect that no node text overlaps, truncates, or becomes unreadably small.

**Step 4: Revise and rerender if needed**

Change diagram grouping or line breaks, never evidence claims, until all four views
are legible.

### Task 4: Verify the documentation package

**Files:**
- Modify: `docs/status.md`
- Modify: `docs/roadmap.md`
- Modify: `docs/plans/2026-08-22-tencent-tke-completion-overlay-design.md`
- Verify: `docs/plans/2026-08-22-tencent-tke-completion-overlays.md`
- Verify: `docs/plans/opl-cloud/README.md`
- Verify: `docs/plans/opl-cloud/diagrams/14-*.mmd`
- Verify: `docs/plans/opl-cloud/diagrams/15-*.mmd`
- Verify: `docs/plans/opl-cloud/diagrams/16-*.mmd`
- Verify: `docs/plans/opl-cloud/diagrams/17-*.mmd`

**Step 1: Check repository diff**

Run:

```bash
git diff --check
git status --short
```

Expected: no whitespace errors and only the intended package files are modified/untracked.

**Step 2: Run local verification**

Run:

```bash
npm run verify:local
```

Expected: pass with no required skipped check.

**Step 3: Reconcile discovered gaps in the canonical Roadmap**

Remove any stale completed gap only after current source and focused tests prove
the acceptance. Add newly observed persistence or concurrency gaps with an exact
owner and acceptance outcome before referencing them from a diagram.

**Step 4: Review claims against owners**

Run focused `rg` checks against `docs/status.md`, `docs/roadmap.md`, and the named
source/schema files. Confirm the diagrams do not claim current TKE Instance
qualification, production readiness, or exact-byte release promotion.

**Step 5: Commit the diagram package**

```bash
git add docs/status.md \
  docs/roadmap.md \
  docs/plans/2026-08-22-tencent-tke-completion-overlay-design.md \
  docs/plans/2026-08-22-tencent-tke-completion-overlays.md \
  docs/plans/opl-cloud/README.md \
  docs/plans/opl-cloud/diagrams/14-* \
  docs/plans/opl-cloud/diagrams/15-* \
  docs/plans/opl-cloud/diagrams/16-* \
  docs/plans/opl-cloud/diagrams/17-*
git commit -m "docs: map tencent tke completion and persistence"
```
