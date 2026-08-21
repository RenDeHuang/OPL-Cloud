# OPL Cloud Architecture Diagram Package Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Materialize the approved OPL Cloud business-driven architecture as 13 named, indexed, reproducibly rendered Mermaid diagrams.

**Architecture:** Keep the approved design as support detail under `docs/plans`, with one Mermaid source and one same-named SVG per view. The package index explains the question, owner boundary, and canonical SSOT for every diagram; generated images never become independent architecture, status, or roadmap owners.

**Tech Stack:** Markdown, Mermaid, SVG, digest-pinned official Mermaid CLI container, existing repository documentation gates.

---

### Task 1: Create The Diagram Package Contract

**Files:**
- Create: `docs/plans/opl-cloud/README.md`
- Create: `docs/plans/opl-cloud/diagrams/mermaid-config.json`

**Step 1: Define the expected inventory**

List all 13 diagram base names, Chinese titles, questions answered, canonical
owners, Mermaid source paths, and SVG paths in `README.md`.

**Step 2: Define deterministic render configuration**

Configure a neutral theme, white background, readable fonts, and stable flowchart
spacing in `mermaid-config.json`. Do not turn visual values into machine contracts.

**Step 3: Verify the inventory count**

Run:

```bash
test "$(rg -c '^\| [0-9]{2} ' docs/plans/opl-cloud/README.md)" -eq 13
```

Expected: exit status 0.

### Task 2: Write The Approved Mermaid Sources

**Files:**
- Create: `docs/plans/opl-cloud/diagrams/01-business-context.mmd`
- Create: `docs/plans/opl-cloud/diagrams/02-ddd-bounded-contexts.mmd`
- Create: `docs/plans/opl-cloud/diagrams/03-data-ownership.mmd`
- Create: `docs/plans/opl-cloud/diagrams/04-workspace-launch-sequence.mmd`
- Create: `docs/plans/opl-cloud/diagrams/05-stage-recovery-state-machine.mmd`
- Create: `docs/plans/opl-cloud/diagrams/06-workspace-lifecycle.mmd`
- Create: `docs/plans/opl-cloud/diagrams/07-workspace-access-sequence.mmd`
- Create: `docs/plans/opl-cloud/diagrams/08-workspace-delete-flow.mmd`
- Create: `docs/plans/opl-cloud/diagrams/09-billing-reconciliation.mmd`
- Create: `docs/plans/opl-cloud/diagrams/10-runtime-deployment-topology.mmd`
- Create: `docs/plans/opl-cloud/diagrams/11-operations-observability-and-incident-response.mmd`
- Create: `docs/plans/opl-cloud/diagrams/12-candidate-qualification-and-product-release.mmd`
- Create: `docs/plans/opl-cloud/diagrams/13-business-driven-delivery-roadmap.mmd`

**Step 1: Write each source with a descriptive title**

Each source starts with Mermaid frontmatter whose title says what the image
explains. Preserve the approved owner and data boundaries; do not introduce a
new service, database, wallet, deployment owner, or current-readiness claim.

**Step 2: Verify the exact source inventory**

Run:

```bash
test "$(find docs/plans/opl-cloud/diagrams -maxdepth 1 -name '*.mmd' | wc -l | tr -d ' ')" -eq 13
```

Expected: exit status 0.

**Step 3: Verify every source has a title**

Run:

```bash
for source in docs/plans/opl-cloud/diagrams/*.mmd; do
  rg -q '^title:' "$source"
done
```

Expected: exit status 0.

### Task 3: Render And Validate Every Diagram

**Files:**
- Create: `docs/plans/opl-cloud/diagrams/*.svg`

**Step 1: Render with a pinned Mermaid CLI**

Run from the repository root with the official multi-architecture image pinned
by manifest digest:

```bash
root="$PWD/docs/plans/opl-cloud"
image="ghcr.io/mermaid-js/mermaid-cli/mermaid-cli:11.16.0@sha256:29077c6bd02f14bdfdd5fee552d9c00fe68d4fab3cd84952d21e2d1faf2fadaf"
for source in docs/plans/opl-cloud/diagrams/*.mmd; do
  name="$(basename "${source%.mmd}")"
  docker run --rm --user "$(id -u):$(id -g)" \
    -v "$root:/data" \
    "$image" \
    -q -i "diagrams/$name.mmd" -o "diagrams/$name.svg" \
    -c "diagrams/mermaid-config.json" -b white -w 2400
done
```

Expected: all 13 commands exit successfully with no Mermaid parse error.

**Step 2: Verify one-to-one source/render pairs**

Run:

```bash
for source in docs/plans/opl-cloud/diagrams/*.mmd; do
  test -s "${source%.mmd}.svg"
done
test "$(find docs/plans/opl-cloud/diagrams -maxdepth 1 -name '*.svg' | wc -l | tr -d ' ')" -eq 13
```

Expected: exit status 0.

**Step 3: Inspect representative renders**

Render the business context, Workspace Launch sequence, data ownership, and
delivery roadmap SVGs to bitmap previews if required by the local viewer, then
check that titles, labels, arrows, and subgraph boundaries are visible without
overlap or clipping.

### Task 4: Validate Documentation Integrity

**Files:**
- Verify: `docs/plans/2026-08-22-opl-cloud-business-driven-architecture-design.md`
- Verify: `docs/plans/2026-08-22-opl-cloud-architecture-diagram-package.md`
- Verify: `docs/plans/opl-cloud/README.md`
- Verify: `docs/plans/opl-cloud/diagrams/**`

**Step 1: Validate references and whitespace**

Run:

```bash
git diff --check
for source in docs/plans/opl-cloud/diagrams/*.mmd; do
  test -s "${source%.mmd}.svg"
done
```

Expected: exit status 0.

**Step 2: Run the ordinary repository gate**

Run:

```bash
npm run verify:local
```

Expected: PASS. This proves the repository source/documentation gate, not
runtime, Instance, qualification, or production readiness.

**Step 3: Review the final diff**

Run:

```bash
git diff --stat
git status --short
```

Expected: only the approved plan, package index, Mermaid sources, and rendered
SVGs are new or changed.
