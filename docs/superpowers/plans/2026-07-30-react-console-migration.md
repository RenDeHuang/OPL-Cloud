# OPL Cloud React Console Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the current Vue Console with a production-built React Console that uses `@openai/apps-sdk-ui`, preserves every frozen business boundary, and exposes the 10 primary pages and 27 slides in the display contract.

**Architecture:** Keep the existing framework-neutral API adapters, DTO decoders, formatting functions, routes, idempotency identities, and source envelopes. Replace only the browser runtime and view layer with a React shell, page-scoped controllers, Apps SDK UI adapters, and Console-specific source/table/modal components. Preserve the current history-based routing model so the Control Plane deployment and URL contract do not change.

**Tech Stack:** React 19, TypeScript, Vite, `@vitejs/plugin-react`, `@openai/apps-sdk-ui`, Tailwind CSS 4 peer runtime, `lucide-react`, Node test runner, Playwright browser acceptance.

---

## File Structure

- `apps/console-ui/src/main.tsx`: React root and global CSS imports.
- `apps/console-ui/src/App.tsx`: top-level route/session composition and global overlays.
- `apps/console-ui/src/app/console-router.ts`: history navigation and route classification.
- `apps/console-ui/src/app/use-console-controller.ts`: page data, commands, request generations, idempotency intents, and secret lifetime.
- `apps/console-ui/src/layout/ConsoleShell.tsx`: desktop and mobile product navigation.
- `apps/console-ui/src/pages/PublicPages.tsx`: public, login, forbidden, not-found, and auth recovery states.
- `apps/console-ui/src/pages/CustomerPages.tsx`: overview, Workspace, API, billing, announcements, account and support slides.
- `apps/console-ui/src/pages/AdminPages.tsx`: operations overview, accounts, reconciliation, resources, system, and announcement management.
- `apps/console-ui/src/components/keys/KeysPanel.tsx`: Key filters, create/edit, reveal, lifecycle and limits.
- `apps/console-ui/src/components/source/SourceState.tsx`: source loading/error/empty/unavailable rendering.
- `apps/console-ui/src/components/ui/*.tsx`: thin wrappers around Apps SDK UI and native semantic fallbacks.
- `apps/console-ui/src/components/ui/apps-sdk.css`: Apps SDK UI and Tailwind CSS entry.
- `apps/console-ui/src/components/ui/tokens.css`: OPL semantic token mapping.
- `apps/console-ui/src/components/ui/components.css`: primitive styles not supplied by the package.
- `apps/console-ui/src/styles.css`: Console shell and feature layout.
- `packages/contracts/opl-cloud-console-ui-contract.json`: machine-readable React/UI implementation contract.
- `tests/ui/react-console-surface.test.ts`: runtime, page, security and interaction source contract.
- `tests/ui/react-ui-primitives.test.ts`: Apps SDK UI adapter contract.
- `tests/ui/console-browser-acceptance.test.ts`: rendered route and browser behavior acceptance.

### Task 1: Freeze The React Runtime Contract

**Files:**
- Create: `packages/contracts/opl-cloud-console-ui-contract.json`
- Create: `tests/ui/react-console-surface.test.ts`
- Create: `tests/ui/react-ui-primitives.test.ts`
- Modify: `tests/contracts/current-product-truth.test.ts`
- Delete: `tests/ui/vue-console-surface.test.ts`
- Delete: `tests/ui/vue-ui-primitives.test.ts`
- Rename: `tests/ui/vue-console-model.test.ts` to `tests/ui/console-model.test.ts`

- [ ] **Step 1: Write the failing React framework test**

```ts
test("Console runtime is React and Vue is retired", async () => {
  const packageJson = JSON.parse(await source("package.json"));
  assert.ok(packageJson.dependencies.react);
  assert.ok(packageJson.dependencies["@openai/apps-sdk-ui"]);
  assert.equal(packageJson.dependencies.vue, undefined);
  assert.match(await source("apps/console-ui/src/main.tsx"), /createRoot/);
  assert.equal((await consoleFiles()).some((path) => path.endsWith(".vue")), false);
});
```

- [ ] **Step 2: Run the test and verify RED**

Run: `node --test tests/ui/react-console-surface.test.ts tests/ui/react-ui-primitives.test.ts tests/contracts/current-product-truth.test.ts`

Expected: FAIL because React dependencies, `main.tsx`, adapters and machine contract do not exist and current docs still require Vue.

- [ ] **Step 3: Add the machine contract and framework-neutral truth assertions**

The JSON contract must set:

```json
{
  "schemaVersion": 1,
  "owner": "OPL Console",
  "state": "current",
  "surface": "standalone_console",
  "framework": "react",
  "componentFoundation": "@openai/apps-sdk-ui",
  "displayContract": "docs/product/console-display-contract-v1.md",
  "browserApiBoundary": "control_plane_product_apis_only",
  "forbidden": ["vue_runtime", "vue_sfc", "vite_vue_plugin", "browser_business_truth", "runtime_gpt_dependency"]
}
```

- [ ] **Step 4: Run focused tests**

Run: `node --test tests/ui/react-console-surface.test.ts tests/ui/react-ui-primitives.test.ts tests/ui/console-model.test.ts tests/contracts/current-product-truth.test.ts`

Expected: React tests still FAIL only on implementation sites; pure model and machine-contract assertions PASS.

- [ ] **Step 5: Commit the contract tests**

```bash
git add packages/contracts/opl-cloud-console-ui-contract.json tests/ui tests/contracts/current-product-truth.test.ts
git commit -m "test: freeze React console runtime contract"
```

### Task 2: Migrate Build And Primitive Foundations

**Files:**
- Modify: `package.json`
- Modify: `package-lock.json`
- Modify: `vite.config.ts`
- Modify: `index.html`
- Modify: `apps/console-ui/tsconfig.json`
- Create: `apps/console-ui/src/main.tsx`
- Create: `apps/console-ui/src/components/ui/apps-sdk.css`
- Create: `apps/console-ui/src/components/ui/index.ts`
- Create: `apps/console-ui/src/components/ui/Button.tsx`
- Create: `apps/console-ui/src/components/ui/Badge.tsx`
- Create: `apps/console-ui/src/components/ui/Field.tsx`
- Create: `apps/console-ui/src/components/ui/Select.tsx`
- Create: `apps/console-ui/src/components/ui/Checkbox.tsx`
- Create: `apps/console-ui/src/components/ui/SegmentedControl.tsx`
- Create: `apps/console-ui/src/components/ui/Alert.tsx`
- Create: `apps/console-ui/src/components/ui/Tooltip.tsx`
- Create: `apps/console-ui/src/components/ui/Modal.tsx`
- Create: `apps/console-ui/src/components/source/SourceState.tsx`

- [ ] **Step 1: Install React and Apps SDK UI dependencies**

Run:

```bash
npm install react@^19 react-dom@^19 @openai/apps-sdk-ui@0.2.2 lucide-react@^0.468 tailwindcss@^4 @vitejs/plugin-react@^4
npm uninstall vue @lucide/vue @vitejs/plugin-vue vue-tsc
```

Expected: `package-lock.json` resolves React 19, Apps SDK UI and Tailwind 4, with no Vue packages required by the root app.

- [ ] **Step 2: Switch Vite, TypeScript and HTML to TSX**

Use `react()` in Vite, include `src/**/*.tsx`, set `jsx: "react-jsx"`, and change the HTML entry to `/apps/console-ui/src/main.tsx`.

- [ ] **Step 3: Implement primitive adapters**

Each adapter must import at least one real Apps SDK UI export where the package provides it and expose OPL defaults. Native controls are allowed only where the package has no suitable export.

- [ ] **Step 4: Run primitive tests and typecheck**

Run: `npm run typecheck && node --test tests/ui/react-ui-primitives.test.ts`

Expected: PASS.

- [ ] **Step 5: Commit foundations**

```bash
git add package.json package-lock.json vite.config.ts index.html apps/console-ui/tsconfig.json apps/console-ui/src/main.tsx apps/console-ui/src/components
git commit -m "feat: add React console foundations"
```

### Task 3: Port Router, Session, Source And Secret Controllers

**Files:**
- Create: `apps/console-ui/src/app/console-router.ts`
- Create: `apps/console-ui/src/app/use-console-controller.ts`
- Create: `apps/console-ui/src/app/console-controller-types.ts`
- Modify: `tests/ui/console-source-adapters.test.ts`
- Modify: `tests/ui/gateway-request-lifecycle.test.ts`
- Modify: `tests/ui/resources-api-idempotency.test.ts`

- [ ] **Step 1: Add failing controller-boundary tests**

The tests must assert that React controller source:

```ts
assert.match(controller, /secretLifetimeMs\s*=\s*60_000/);
assert.match(controller, /requestGeneration/);
assert.match(controller, /clearSecrets/);
assert.match(controller, /workspaceLaunchIdempotencyKey/);
assert.doesNotMatch(controller, /localStorage|sessionStorage/);
```

- [ ] **Step 2: Verify RED**

Run: `node --test tests/ui/console-source-adapters.test.ts tests/ui/gateway-request-lifecycle.test.ts tests/ui/resources-api-idempotency.test.ts`

Expected: FAIL because the React controller does not exist.

- [ ] **Step 3: Port the framework-neutral orchestration**

Use React state and effects for:

- history/popstate route changes;
- Session recovery and operator gate;
- independent source loading/errors;
- stale-response generation guards;
- Workspace launch polling and recovery;
- stable idempotency intents for every command;
- 60-second reveal cleanup and route/session invalidation;
- modal/toast state.

- [ ] **Step 4: Run controller and adapter tests**

Run: `node --test tests/ui/console-source-adapters.test.ts tests/ui/gateway-request-lifecycle.test.ts tests/ui/resources-api-idempotency.test.ts tests/ui/console-model.test.ts`

Expected: PASS.

- [ ] **Step 5: Commit controller port**

```bash
git add apps/console-ui/src/app tests/ui
git commit -m "feat: port console orchestration to React"
```

### Task 4: Build The React Shell And Public States

**Files:**
- Create: `apps/console-ui/src/App.tsx`
- Create: `apps/console-ui/src/layout/ConsoleShell.tsx`
- Create: `apps/console-ui/src/pages/PublicPages.tsx`
- Modify: `apps/console-ui/src/styles.css`
- Modify: `tests/ui/react-console-surface.test.ts`

- [ ] **Step 1: Add failing shell tests**

Assert the customer five-item navigation, Admin five-item navigation, account menu, logout, mobile reachability, login form, `/403` and not-found states.

- [ ] **Step 2: Verify RED**

Run: `node --test tests/ui/react-console-surface.test.ts`

Expected: FAIL on missing shell and page source.

- [ ] **Step 3: Implement the shell**

Use a fixed desktop sidebar, restrained header, mobile drawer/bottom navigation, semantic landmarks and the selected B direction. Keep primary content unframed and use panels only for real grouped tools.

- [ ] **Step 4: Run typecheck and shell tests**

Run: `npm run typecheck && node --test tests/ui/react-console-surface.test.ts`

Expected: PASS for shell/public assertions.

- [ ] **Step 5: Commit shell**

```bash
git add apps/console-ui/src/App.tsx apps/console-ui/src/layout apps/console-ui/src/pages/PublicPages.tsx apps/console-ui/src/styles.css tests/ui/react-console-surface.test.ts
git commit -m "feat: build React console shell"
```

### Task 5: Port Customer Slides

**Files:**
- Create: `apps/console-ui/src/pages/CustomerPages.tsx`
- Create: `apps/console-ui/src/features/overview/OverviewPage.tsx`
- Create: `apps/console-ui/src/features/workspaces/WorkspacePages.tsx`
- Create: `apps/console-ui/src/features/api/ApiPages.tsx`
- Create: `apps/console-ui/src/features/billing/BillingPage.tsx`
- Create: `apps/console-ui/src/features/announcements/AnnouncementsPage.tsx`
- Create: `apps/console-ui/src/components/keys/KeysPanel.tsx`
- Modify: `tests/ui/customer-console-flow.test.ts`
- Modify: `tests/ui/pricing-preview.test.ts`
- Modify: `tests/ui/balance-availability.test.ts`

- [ ] **Step 1: Point customer-flow tests at React feature boundaries**

Keep assertions for all C-OV, C-WS, C-API, C-BIL and C-ANN content, source states, commands, exact prices, no browser pricing, reveal/no-store and recovery behavior.

- [ ] **Step 2: Verify RED**

Run: `node --test tests/ui/customer-console-flow.test.ts tests/ui/pricing-preview.test.ts tests/ui/balance-availability.test.ts`

Expected: FAIL because the React customer features do not exist.

- [ ] **Step 3: Implement customer pages**

Render every customer slide from the frozen display contract with realistic loading, error, empty and unavailable states. Preserve existing API adapter calls and controller commands.

- [ ] **Step 4: Run focused tests and build**

Run: `npm run typecheck && node --test tests/ui/customer-console-flow.test.ts tests/ui/pricing-preview.test.ts tests/ui/balance-availability.test.ts && npm run build`

Expected: PASS.

- [ ] **Step 5: Commit customer pages**

```bash
git add apps/console-ui/src/pages/CustomerPages.tsx apps/console-ui/src/features apps/console-ui/src/components/keys tests/ui
git commit -m "feat: port customer console slides to React"
```

### Task 6: Port Admin Slides And Global Modals

**Files:**
- Create: `apps/console-ui/src/pages/AdminPages.tsx`
- Create: `apps/console-ui/src/features/admin/AdminOverviewPage.tsx`
- Create: `apps/console-ui/src/features/admin/AccountsPage.tsx`
- Create: `apps/console-ui/src/features/admin/ReconciliationPage.tsx`
- Create: `apps/console-ui/src/features/admin/ResourcesPage.tsx`
- Create: `apps/console-ui/src/features/admin/SystemPage.tsx`
- Create: `apps/console-ui/src/features/admin/AdminModals.tsx`
- Modify: `tests/ui/operator-console-flow.test.ts`

- [ ] **Step 1: Point operator tests at React feature boundaries**

Preserve account mapping, reserved admin, live nested sources, wallet readback, reconciliation allowedActions, Fabric resource authority, health source and announcement lifecycle assertions.

- [ ] **Step 2: Verify RED**

Run: `node --test tests/ui/operator-console-flow.test.ts`

Expected: FAIL because the React Admin features do not exist.

- [ ] **Step 3: Implement Admin pages and accessible modals**

Support focus trap, Escape, background scroll lock, close focus restore, explicit target confirmation and authoritative operation readback.

- [ ] **Step 4: Run focused tests and typecheck**

Run: `npm run typecheck && node --test tests/ui/operator-console-flow.test.ts`

Expected: PASS.

- [ ] **Step 5: Commit Admin pages**

```bash
git add apps/console-ui/src/pages/AdminPages.tsx apps/console-ui/src/features/admin tests/ui/operator-console-flow.test.ts
git commit -m "feat: port admin console slides to React"
```

### Task 7: Retire Vue And Update Current Documentation

**Files:**
- Delete: `apps/console-ui/src/App.vue`
- Delete: `apps/console-ui/src/components/keys/KeysPanel.vue`
- Delete: `apps/console-ui/src/components/ui/*.vue`
- Delete: `apps/console-ui/src/main.ts`
- Modify: `README.md`
- Modify: `packages/README.md`
- Modify: `docs/runtime/tke-production-deployment.md`
- Modify: all remaining tests that read `App.vue`
- Modify: `tools/console-browser-qa.ts`

- [ ] **Step 1: Replace all remaining SFC filename assumptions**

Run: `rg -n 'App\.vue|KeysPanel\.vue|\.vue\b|Vue Console|@lucide/vue|@vitejs/plugin-vue|vue-tsc|createApp' README.md packages docs apps tests tools package.json vite.config.ts`

Expected before edit: matches list the remaining migration sites.

- [ ] **Step 2: Update current docs and test paths**

Describe the current browser UI as React Console and make flow tests read stable React files or execute browser behavior.

- [ ] **Step 3: Delete Vue production files**

Use `apply_patch` deletion and confirm no `.vue` file remains under `apps/console-ui`.

- [ ] **Step 4: Run no-Vue scan and full tests**

Run:

```bash
test -z "$(find apps/console-ui -name '*.vue' -print)"
! rg -n '@lucide/vue|@vitejs/plugin-vue|vue-tsc|from "vue"|Vue Console' README.md packages docs apps tests tools package.json vite.config.ts
npm test
```

Expected: PASS.

- [ ] **Step 5: Commit retirement**

```bash
git add -A
git commit -m "refactor: retire Vue console implementation"
```

### Task 8: Browser Acceptance And Visual QA

**Files:**
- Modify: `tests/ui/console-browser-acceptance.test.ts`
- Modify: `tools/console-browser-qa.ts`
- Create: `design-qa.md`

- [ ] **Step 1: Start the staging API and React UI**

Run the repository's existing local staging commands and record the resulting API/UI URLs. Do not change backend truth or introduce mock browser business data.

- [ ] **Step 2: Run browser acceptance**

Run: `node --test tests/ui/console-browser-acceptance.test.ts`

Expected: PASS for route inventory and browser harness prerequisites.

- [ ] **Step 3: Capture desktop and mobile states**

Use the approved browser path. If only Playwright is available, request the user's permission before direct use. Capture at least overview, Workspace purchase, API keys, billing, Admin accounts, reconciliation, resources and system pages at desktop and mobile widths.

- [ ] **Step 4: Write and close `design-qa.md`**

Compare rendered screenshots against the selected B mockup and spec. Fix all P0/P1/P2 issues and set `final result: passed`; retain only optional P3 notes.

- [ ] **Step 5: Run final verification**

Run:

```bash
npm run typecheck
npm run lint
npm test
npm run build
git diff --check
git status --short --branch
```

Expected: all commands exit 0; status contains only intentional committed work or no changes.

- [ ] **Step 6: Commit QA closure**

```bash
git add tests/ui/console-browser-acceptance.test.ts tools/console-browser-qa.ts design-qa.md
git commit -m "test: verify React console experience"
```

### Task 9: Final Branch Handoff Without Main Integration

**Files:**
- No source changes unless final verification identifies a defect.

- [ ] **Step 1: Verify branch isolation**

Run: `git branch --show-current && git worktree list && git log --oneline --decorate -8`

Expected: current branch is `codex/uiux-display-contract`; `main` remains at its original commit and no merge is performed.

- [ ] **Step 2: Keep the React dev server running**

Start Vite on an unused local port and retain the session for user review.

- [ ] **Step 3: Report artifacts and gaps**

Report the worktree path, branch, commits, local URL, validation commands, ImageGen status, and any remaining P3 visual notes. Do not merge, push, create a PR, or modify `main`.
