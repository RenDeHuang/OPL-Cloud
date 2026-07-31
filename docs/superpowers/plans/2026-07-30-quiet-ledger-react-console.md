# Quiet Ledger React Console Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rebuild the frozen OPL Cloud React Console around the selected Quiet Ledger reference while preserving every source-of-truth boundary and leaving the reference PNG byte-for-byte unchanged.

**Architecture:** Keep the existing React controller, routes, DTOs, SourceEnvelope handling, and `@openai/apps-sdk-ui` adapters. Change only the presentation contract in the shared shell, customer overview, semantic tokens, responsive CSS, documentation, and matching contract tests; all customer and Admin pages continue to consume Control Plane product APIs through the existing controller.

**Tech Stack:** React 19, TypeScript, Vite, `@openai/apps-sdk-ui`, `lucide-react`, Node test runner, local fake-only demo server.

---

### Task 1: Freeze The Selected Visual Contract

**Files:**
- Create: `docs/superpowers/plans/2026-07-30-quiet-ledger-react-console.md`
- Modify: `tests/ui/react-console-surface.test.ts`
- Modify: `packages/contracts/opl-cloud-console-ui-contract.json`
- Preserve: `output/imagegen/opl-console-option-1-quiet-ledger-1440x1024.png`

- [ ] **Step 1: Write the failing surface test**

```ts
test("React Console uses the frozen Quiet Ledger presentation", async () => {
  const [overview, shell, styles, contract] = await Promise.all([
    source("apps/console-ui/src/pages/CustomerPages.tsx"),
    source("apps/console-ui/src/layout/ConsoleShell.tsx"),
    source("apps/console-ui/src/styles.css"),
    source("packages/contracts/opl-cloud-console-ui-contract.json")
  ]);
  assert.match(shell, /data-visual-direction="quiet-ledger"/);
  assert.doesNotMatch(overview, />C-OV-01</);
  assert.doesNotMatch(overview, /分别来自各自权威来源/);
  assert.match(styles, /--action:\s*#075b3b/);
  assert.equal(JSON.parse(contract).visualDirection.id, "quiet-ledger");
});
```

- [ ] **Step 2: Run the focused test and verify RED**

Run: `node --test tests/ui/react-console-surface.test.ts`

Expected: FAIL because `data-visual-direction`, the Quiet Ledger token, and machine-contract entry do not exist, while the old internal copy is still visible.

- [ ] **Step 3: Record the immutable source checksum**

Run: `sha256sum output/imagegen/opl-console-option-1-quiet-ledger-1440x1024.png`

Expected: `9b75ba8b01cda552fcb7d44bc774797f22697f5fd4a0c067e885bc4895b738b5`.

### Task 2: Rebuild The Shared Shell And Overview Hierarchy

**Files:**
- Modify: `apps/console-ui/src/layout/ConsoleShell.tsx`
- Modify: `apps/console-ui/src/pages/CustomerPages.tsx`
- Test: `tests/ui/react-console-surface.test.ts`

- [ ] **Step 1: Mark the shared visual direction on the real shell**

```tsx
<div className="app-shell" data-visual-direction="quiet-ledger">
```

- [ ] **Step 2: Replace the overview hero with a truth-safe open metric band**

```tsx
<section className="overview-summary" aria-label="账户关键指标">
  <Metric emphasis label="可用余额" note="API 服务余额" value={wallet ? formatUsdMicros(wallet.usdMicros) : "暂不可用"} />
  <Metric label="本月 API 实际费用" note="请求实际消费" value={usage ? formatUsdMicros(usage.totalActualCostUsdMicros) : "暂不可用"} />
  <Metric label="Workspace" note="当前账户总数" value={workspaces ? formatCount(workspaces.total) : "暂不可用"} />
</section>
```

- [ ] **Step 3: Keep the Workspace summary primary and reduce billing/announcement weight**

Use the existing Workspace, Ledger receipt, and announcement SourceEnvelope blocks. Do not add a trend graph because the current overview DTO has no authoritative time series.

- [ ] **Step 4: Run the focused test and verify GREEN**

Run: `node --test tests/ui/react-console-surface.test.ts`

Expected: all tests in the file pass.

### Task 3: Apply Quiet Ledger Across Customer And Admin Surfaces

**Files:**
- Modify: `apps/console-ui/src/styles.css`
- Modify: `apps/console-ui/src/components/ui/tokens.css`
- Test: `tests/ui/react-console-surface.test.ts`

- [ ] **Step 1: Establish the measured visual tokens**

```css
:root {
  --action: #075b3b;
  --action-hover: #064a31;
  --action-soft: #e8f1ec;
  --canvas: #ffffff;
  --sidebar-surface: #fbfcfb;
  --ink: #111827;
  --muted: #667085;
  --line: #d9dfdc;
}
```

- [ ] **Step 2: Match the reference composition**

Keep the measured `240px` desktop rail, `92px` restrained top bar, open metric rows, square-edged page bands, `<=8px` object-card radii, thin separators, and a single deep-green action color. Remove the navy overview band, blue navigation wash, gradients, decorative shapes, and stacked section-card treatment.

- [ ] **Step 3: Preserve complete mobile reachability**

At `<=820px`, use the existing mobile navigation and object-card alternatives, ensure every primary page remains reachable, and keep controls at least `44px` high without horizontal viewport overflow.

- [ ] **Step 4: Run type and surface checks**

Run: `npm run typecheck && node --test tests/ui/react-console-surface.test.ts`

Expected: both commands exit `0`.

### Task 4: Update The Frozen React Design Contract

**Files:**
- Modify: `docs/superpowers/specs/2026-07-30-react-console-ui-design.md`
- Modify: `packages/contracts/opl-cloud-console-ui-contract.json`
- Modify: `docs/product/console-display-contract-v1.md`

- [ ] **Step 1: Record the selected direction and immutable reference**

Document `Quiet Ledger`, the exact reference path and SHA-256, its layout/palette rules, and the prohibition on modifying or regenerating the selected image.

- [ ] **Step 2: Record truth-safe deviations**

Document that Control Plane Workspace totals are not Fabric runtime checks and that the overview does not render a seven-day trend until an authoritative time-series DTO exists.

- [ ] **Step 3: Validate contract JSON and current truth tests**

Run: `node -e "JSON.parse(require('node:fs').readFileSync('packages/contracts/opl-cloud-console-ui-contract.json','utf8'))"`

Run: `node --test tests/contracts/current-product-truth.test.ts tests/contracts/current-product-boundary.test.ts`

Expected: JSON parse and both contract suites pass.

### Task 5: Browser And Design QA Gate

**Files:**
- Modify: `design-qa.md`
- Create: `output/browser-qa/quiet-ledger-console-overview-1440x1024.png`
- Create: `output/browser-qa/quiet-ledger-console-overview-390x844.png`

- [ ] **Step 1: Run complete verification**

Run: `npm run typecheck`

Run: `npm run lint`

Run: `npm test`

Run: `npm run build`

Expected: every command exits `0` with zero test failures.

- [ ] **Step 2: Start the fake-only local demo**

Run: `npm run demo`

Expected: the server prints a localhost login URL and fixture customer/Admin credentials, with no external network or real billing/resource mutations.

- [ ] **Step 3: Capture the same reference state and viewport**

Capture authenticated overview screenshots at `1440x1024` and `390x844`, plus focused shell, metric, Workspace, and secondary-panel regions. Exercise navigation, primary actions, source unavailable states, menus, forms, and modals.

- [ ] **Step 4: Compare source and implementation together**

Open the immutable PNG and desktop implementation capture in one comparison artifact. Fix every actionable P0/P1/P2 mismatch without modifying the source PNG.

- [ ] **Step 5: Write the blocking QA result**

Update project-root `design-qa.md` with source path/hash, implementation paths, viewport/state, full and focused comparison evidence, findings, patches, and exactly `final result: passed` when no P0/P1/P2 remains.

- [ ] **Step 6: Recheck reference immutability and worktree scope**

Run: `sha256sum output/imagegen/opl-console-option-1-quiet-ledger-1440x1024.png`

Run: `git status --short --branch`

Expected: the selected PNG hash remains `9b75ba8b01cda552fcb7d44bc774797f22697f5fd4a0c067e885bc4895b738b5`, and all tracked changes are confined to `codex/uiux-display-contract`.
