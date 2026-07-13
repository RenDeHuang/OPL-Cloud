# Production Resilience E2E Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prove the production Console click path, five simultaneous Tencent CVMs, resource-scoped recovery, visible Workspace replies, and exact cleanup with real Workspace URLs.

**Architecture:** Reuse the existing production verifier for billing and cloud truth, but add strict browser predicates, stable mutation keys, a persisted run manifest, and an optional ready barrier. Keep paid soak and fault drills in explicit manual workflows so routine verification remains small. API writes remain forbidden in Console browser mode except exact manifest cleanup.

**Tech Stack:** Node.js 22, Playwright, GitHub Actions, Go Control Plane/Fabric/Ledger APIs, Tencent TKE, Kubernetes, Node built-in test runner.

---

### Task 1: Make Workspace reply and URL evidence truthful

**Files:**
- Modify: `tools/production-verifier.ts`
- Modify: `tests/production/production-verifier.test.ts`
- Modify: `.github/workflows/verify-production-chain.yml`
- Modify: `tests/production/tke-deploy-workflow.test.ts`

- [ ] **Step 1: Write the failing browser regression test**

Add a fake page state where the marker exists only in the title, sidebar, and user prompt while `Processing` is active. Assert `verifyWorkspaceBrowserUi()` rejects with `workspace_browser_reply_seen_failed`. Add the assistant marker as an exact visible main-message text node, clear Processing, enable send, and assert success.

```ts
await assert.rejects(() => verifyWorkspaceBrowserUi({
  workspaceUrl: "https://workspace.example/w/ws-test/",
  runId: "reply-title-only",
  checks: [],
  browserFactory: fakeWorkspaceBrowserFactory(actions, {
    titleMarkerOnly: true,
    processing: true
  })
}), /workspace_browser_reply_seen_failed/);
```

- [ ] **Step 2: Run the focused test and verify RED**

Run: `node --test tests/production/production-verifier.test.ts`

Expected: FAIL because the current whole-page marker count accepts title/sidebar duplicates.

- [ ] **Step 3: Implement the minimal DOM predicate**

Replace the whole-body counter with a predicate that scans visible elements under `main`, excludes `nav`, `aside`, headings, inputs and the exact user prompt, requires an exact marker text node, requires no active `Processing`, and requires the send control to be enabled.

```ts
function workspaceReplyVisible({ marker, prompt }) {
  const visible = (element) => {
    const rect = element.getBoundingClientRect();
    const style = window.getComputedStyle(element);
    return rect.width > 0 && rect.height > 0 && style.display !== "none" && style.visibility !== "hidden";
  };
  const main = document.querySelector("main") || document.body;
  const reply = [...main.querySelectorAll("p,pre,code,[role='article'],[data-message-role='assistant']")]
    .find((element) => visible(element) && element.textContent?.trim() === marker && !element.closest("nav,aside,h1,h2,h3"));
  const processing = [...document.querySelectorAll("body *")]
    .some((element) => visible(element) && /^Processing(?:\.\.\.)?/i.test(element.textContent?.trim() || ""));
  const send = document.querySelector('[data-testid="guid-send-btn"],button[type="submit"]');
  return Boolean(reply && !processing && send && !send.disabled && send.getAttribute("aria-disabled") !== "true");
}
```

Keep the predicate inside the Playwright `waitForFunction`; do not export a new abstraction.

- [ ] **Step 4: Require and emit the real Workspace URL**

Assert the verifier result contains a public HTTPS `url`. Update the workflow output parser to emit both `workspace_id` and a percent-encoded `workspace_url` after URL validation.

```js
const url = new URL(payload.url);
if (url.protocol !== "https:" || !url.pathname.startsWith("/w/")) throw new Error("workspace_url_required");
process.stdout.write(`workspace_url=${encodeURIComponent(url.toString())}\n`);
```

- [ ] **Step 5: Run focused tests and commit**

Run:

```bash
node --test tests/production/production-verifier.test.ts
node --test tests/production/tke-deploy-workflow.test.ts
git diff --check
```

Expected: PASS.

Commit: `fix(e2e): require visible workspace replies and URLs`

### Task 2: Stabilize mutations, manifests, ownership checks, and barriers

**Files:**
- Modify: `tools/production-verifier.ts`
- Modify: `tests/production/production-verifier.test.ts`

- [ ] **Step 1: Write failing tests for keys and cleanup ownership**

Assert every paid mutation uses `production-verification:<runId>:<slot>:<stage>`. Replay a simulated lost response with the same key and assert one resource and Hold. Feed cleanup a state whose account/name/resource/ownership differs from the manifest and assert it performs no DELETE/POST cleanup writes.

```ts
assert.equal(create.headers["idempotency-key"], "production-verification:run-1:01:create-compute");
await assert.rejects(() => cleanupVerificationResources({ manifest: wrongManifest, ...deps }), /verification_resource_ownership_mismatch/);
assert.equal(cleanupWrites.length, 0);
```

- [ ] **Step 2: Run focused tests and verify RED**

Run: `node --test tests/production/production-verifier.test.ts`

Expected: FAIL because create/storage/attachment/workspace and cleanup currently omit stable keys and ownership proof.

- [ ] **Step 3: Add stable mutation keys and atomic manifest writes**

Extend verifier options with `slot`, `manifestPath`, `readyFile`, `releaseFile`, and `barrierTimeoutMs`. Use `writeFile(temp)` then `rename(temp, manifestPath)` after each new identity is known.

```ts
const mutationKey = (stage) => `production-verification:${runId}:${slot}:${stage}`;
await requestJson({ ..., idempotencyKey: mutationKey("create-compute") });
await writeFile(`${manifestPath}.tmp`, JSON.stringify(manifest, null, 2));
await rename(`${manifestPath}.tmp`, manifestPath);
```

Manifest fields must include run/slot/account, names, all resource/Hold/operation/Machine identities, `workspaceId`, and public `workspaceUrl`.

- [ ] **Step 4: Add exact ownership proof before cleanup and fault actions**

Read `/api/state?accountId=...` immediately before mutation. Require exact IDs, run-labelled names, matching Hold IDs, and one Fabric ownership with matching machine/instance/node. Return a typed mismatch without mutating anything.

- [ ] **Step 5: Add the ready/release barrier**

After first Workspace URL and browser/file proof, atomically write `readyFile`, wait up to `barrierTimeoutMs` for `releaseFile`, then continue normal lifecycle. With no barrier paths, preserve current behavior.

- [ ] **Step 6: Run focused tests and commit**

Run: `node --test tests/production/production-verifier.test.ts`

Expected: PASS including lost-response replay, manifest URL, mismatch fail-closed, and barrier timeout.

Commit: `fix(e2e): bind production cleanup to run ownership`

### Task 3: Drive the commercial lifecycle through the Console UI

**Files:**
- Create: `tools/production-console-browser-verifier.ts`
- Create: `tests/production/production-console-browser-verifier.test.ts`
- Modify: `tools/production-verifier.ts`
- Modify: `.github/workflows/verify-production-chain.yml`
- Modify: `tests/production/tke-deploy-workflow.test.ts`

- [ ] **Step 1: Write the failing Console browser contract test**

Use a narrow fake Playwright page and assert this ordered sequence: login, compute Hold preview, compute confirm, compute running, storage Hold preview, storage confirm, storage available, attach confirm, Workspace create, Workspace URL extraction/open, billing evidence, compute destroy confirm, storage destroy confirm.

```ts
assert.deepEqual(stages, [
  "login", "compute_hold_preview", "compute_running", "storage_hold_preview",
  "storage_available", "attached", "workspace_url_opened", "billing_visible",
  "compute_destroyed", "storage_destroyed"
]);
```

- [ ] **Step 2: Run the new test and verify RED**

Run: `node --test tests/production/production-console-browser-verifier.test.ts`

Expected: FAIL because the module does not exist.

- [ ] **Step 3: Implement browser-only commercial mutations**

Use Playwright `getByLabel`, `getByRole`, and dialog names already exposed by the Console. Fill owner credentials, resource names containing run ID, select Basic, inspect visible `每小时价格`, `预冻结`, `7 天`, and `冻结后可用`, click the action and confirm dialog, then poll page/state until the named resource reaches its required status.

The verifier may call read-only `/api/state` to resolve exact IDs after each UI action. It must not call create/attach/workspace/destroy APIs directly.

- [ ] **Step 4: Extract and open the Workspace URL**

From the named Workspace row/detail, read the `打开` link target or newly opened page URL. Validate public HTTPS `/w/<workspaceId>/`, store it in the manifest, navigate to it, and run the strict Workspace browser check from Task 1.

- [ ] **Step 5: Capture acceptance screenshots**

Save `console-login`, `compute-hold`, `compute-running`, `storage-hold`, `workspace-ready`, `billing`, `compute-destroyed`, `storage-destroyed`, and `workspace-reply` PNG files. A screenshot failure fails the browser E2E.

- [ ] **Step 6: Wire the existing production workflow**

Run Console browser mode for the normal single chain and emit `workspace_url`. Keep API mode available for soak slots where browser coverage would add cost without new concurrency evidence.

- [ ] **Step 7: Run tests and commit**

Run:

```bash
node --test tests/production/production-console-browser-verifier.test.ts
node --test tests/production/production-verifier.test.ts
node --test tests/production/tke-deploy-workflow.test.ts
```

Expected: PASS.

Commit: `test(e2e): drive production lifecycle through Console UI`

### Task 4: Add the five-slot production soak workflow

**Files:**
- Create: `tools/production-soak-coordinator.ts`
- Create: `tests/production/production-soak-coordinator.test.ts`
- Create: `.github/workflows/verify-production-soak.yml`
- Modify: `tests/production/tke-deploy-workflow.test.ts`

- [ ] **Step 1: Write failing coordinator and workflow tests**

Assert exactly five slots, distinct run IDs, one ready file per slot, a 15-minute bounded soak, five unique public Workspace URLs, no release before all slots are ready, `concurrency.cancel-in-progress=false`, 60-minute timeout, `always()` artifact upload, and exact manifest cleanup.

- [ ] **Step 2: Run tests and verify RED**

Run:

```bash
node --test tests/production/production-soak-coordinator.test.ts
node --test tests/production/tke-deploy-workflow.test.ts
```

Expected: FAIL because coordinator/workflow do not exist.

- [ ] **Step 3: Implement the stdlib coordinator**

Spawn five `production-verifier.ts` child processes with unique slot/run/manifest/ready/release paths. Wait for all ready manifests, validate distinct resource/machine/instance/node/Workspace IDs and URLs, poll the read-only evidence command during soak, then atomically create all release files. Forward exit codes and aggregate JSON without swallowing cleanup errors.

- [ ] **Step 4: Implement the manual paid workflow**

Require explicit `confirm_paid_soak=RUN_5_TENCENT_CVMS`, run capacity/readiness checks before top-up or creation, install Chromium once, execute the coordinator, upload manifests/results/screenshots always, and run a final zero-residual diagnostic. Do not call range cleanup workflows.

- [ ] **Step 5: Run tests and commit**

Run focused coordinator/workflow tests and `git diff --check`.

Commit: `test(e2e): add five-machine production soak`

### Task 5: Add resource-scoped production fault drills

**Files:**
- Create: `tools/production-fault-verifier.ts`
- Create: `tests/production/production-fault-verifier.test.ts`
- Create: `.github/workflows/verify-production-faults.yml`
- Modify: `tests/production/tke-deploy-workflow.test.ts`

- [ ] **Step 1: Write failing fault-boundary tests**

For each scenario assert exact ownership proof occurs before mutation. Test wrong account, wrong run-labelled name, duplicate ownership, and missing machine identity; all must fail with zero mutation calls. Test the allowed sequence: lost-response replay, exact Workspace Pod delete/recovery, exact detach/reattach, exact verifier Machine delete/reconcile, forced browser failure/cleanup.

- [ ] **Step 2: Run tests and verify RED**

Run: `node --test tests/production/production-fault-verifier.test.ts`

Expected: FAIL because the verifier does not exist.

- [ ] **Step 3: Implement allowed drills only**

Reuse Task 2 manifest validation. Use the existing HTTP client for replay/detach/reattach and `kubectl` for the exact namespaced Pod. For external Machine deletion, call the existing Tencent provisioner only with the manifest instance ID after machine/instance/node triple match; never accept a Node Pool or selector argument.

- [ ] **Step 4: Verify recovery and money invariants**

After each fault, poll until expected recovery/terminal state. Require one resource/Hold after replay, PVC digest after Pod recovery and reattach, and `billingStatus=stopped` plus the same Hold released after external Machine deletion. Cleanup release must not increase wallet balance.

- [ ] **Step 5: Add the manual workflow**

Require `confirm_fault_drill=RUN_RESOURCE_SCOPED_FAULTS`, one scenario at a time, non-overlapping production concurrency, manifest/result uploads always, and final zero-residual diagnostics. Explicitly forbid shared service, database, Node Pool, network and account-level fault commands in workflow contract tests.

- [ ] **Step 6: Run tests and commit**

Run focused fault/workflow tests and `git diff --check`.

Commit: `test(e2e): add resource-scoped production fault drills`

### Task 6: Full verification, review, landing, and production evidence

**Files:**
- Modify: `docs/status.md`
- Modify: `README.md` only if commands or launch boundary changed

- [ ] **Step 1: Run fresh local verification**

```bash
npm test
npm run typecheck
npm run build
git diff --check
```

Expected: all pass.

- [ ] **Step 2: Run spec and code-quality reviews**

Review for secret leakage, paid-resource cleanup, cross-account deletion, idempotency, exact URL output, forbidden App changes, and workflow concurrency. Fix all Critical/Important findings and re-run tests.

- [ ] **Step 3: Merge and push**

Fast-forward `main`, run `npm test` on merged code, push `main`. Service code changes, if any, require image release and TKE rollout before verifier execution; verifier/workflow-only changes do not.

- [ ] **Step 4: Run the normal production chain**

Require success, a real `workspaceUrl`, and manually inspect every screenshot. The final Workspace screenshot must visibly contain the assistant marker and no active Processing state.

- [ ] **Step 5: Run the five-machine soak**

Confirm five distinct public Workspace URLs, five active ownerships at the barrier, 15 minutes stable, and zero cleanup residuals.

- [ ] **Step 6: Run resource-scoped fault drills**

Run all allowed scenarios sequentially. Require exact recovery/money evidence and zero residuals.

- [ ] **Step 7: Clean temporary state**

Remove only this task's worktree, merged branch, downloaded artifacts, ready/release files and manifests. Preserve `.worktrees/opl-gateway` and `.worktrees/unified-gateway-contract`.
