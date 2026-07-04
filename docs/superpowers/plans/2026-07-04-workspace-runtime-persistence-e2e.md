# Workspace Runtime Persistence E2E Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend production verification to prove Workspace runtime file persistence across compute replacement through the public Workspace URL gateway.

**Architecture:** Keep Console as the commercial resource control plane. Add small helper functions to `tools/production-verifier.js` that call one-person-lab-app runtime APIs through each Workspace URL. The verifier creates a second compute allocation and attachment against the retained storage, then proves the same file is readable through the second runtime.

**Tech Stack:** Node.js verifier, native `fetch`, WebUI HTTP APIs, OPL Console resource APIs, existing TKE provider.

---

### Task 1: Runtime API Helpers

**Files:**
- Modify: `tools/production-verifier.js`
- Test: `tests/production/production-verifier.test.js`

- [ ] **Step 1: Write failing tests**

Add a test that expects the verifier to call these public Workspace URL paths after the first Workspace URL opens:

```text
GET https://workspace.example/w/ws-prod/?token=share
GET https://workspace.example/w/ws-prod/api/auth/user
POST https://workspace.example/w/ws-prod/api/fs/write
POST https://workspace.example/w/ws-prod/api/fs/read
```

The mock `POST /api/fs/write` response should be `{ "success": true, "data": true }`.
The mock `POST /api/fs/read` response should be `{ "success": true, "data": "opl persistence <runId>" }`.

Run:

```bash
node --test tests/production/production-verifier.test.js
```

Expected: FAIL because the verifier does not call runtime file APIs yet.

- [ ] **Step 2: Implement minimal runtime helpers**

Add helpers:

```js
function workspaceApiUrl(workspaceUrl, path) {
  const parsed = new URL(workspaceUrl);
  const normalizedPath = path.startsWith("/") ? path.slice(1) : path;
  parsed.pathname = `${parsed.pathname.replace(/\/$/, "")}/${normalizedPath}`;
  parsed.search = "";
  return parsed.toString();
}

async function requestWorkspaceJson({ fetchImpl, workspaceUrl, path, method = "GET", body = null }) {
  const response = await fetchImpl(workspaceApiUrl(workspaceUrl, path), {
    method,
    headers: body ? { "content-type": "application/json" } : undefined,
    body: body ? JSON.stringify(body) : undefined
  });
  const payload = await readResponse(response);
  if (!response.ok) {
    const message = typeof payload === "string" ? payload : payload.error || JSON.stringify(payload);
    throw new Error(`workspace_api_failed:${method}:${path}:${response.status}:${message}`);
  }
  return payload;
}
```

- [ ] **Step 3: Verify tests pass**

Run:

```bash
node --test tests/production/production-verifier.test.js
```

Expected: PASS for the updated production verifier tests.

- [ ] **Step 4: Commit**

```bash
git add tools/production-verifier.js tests/production/production-verifier.test.js
git commit -m "Verify workspace runtime file API"
```

### Task 2: Compute Replacement Persistence Flow

**Files:**
- Modify: `tools/production-verifier.js`
- Test: `tests/production/production-verifier.test.js`

- [ ] **Step 1: Write failing tests**

Add a test that verifies the request order:

```text
POST /api/storage-attachments/detach
POST /api/compute-allocations/<first>/destroy
POST /api/compute-allocations
POST /api/storage-attachments
POST /api/workspaces
POST /api/workspaces/runtime-status
GET <second workspace URL>
POST <second workspace URL>/api/fs/read
POST /api/storage-attachments/detach
POST /api/compute-allocations/<second>/destroy
POST /api/storage-volumes/destroy
```

The same storage id must be used for the second attachment.

Run:

```bash
node --test tests/production/production-verifier.test.js
```

Expected: FAIL because the verifier currently destroys storage after the first compute.

- [ ] **Step 2: Implement second compute flow**

Change `verifyProductionChain` to retain storage after the first runtime proof, destroy only first attachment/compute, then create:

```js
const replacementCompute = await requestJson({
  fetchImpl,
  origin: normalizedOrigin,
  path: "/api/compute-allocations",
  method: "POST",
  auth,
  body: { accountId, packageId, name: `${effectiveWorkspaceName} replacement compute ${runId}` }
});
```

Attach the original `storage.id`, create a second Workspace URL, open it, and read `/projects/opl-e2e/<runId>.txt`.

- [ ] **Step 3: Harden cleanup**

Track `replacementCompute`, `replacementAttachment`, and `replacementWorkspace`. Cleanup order must be:

1. detach replacement attachment
2. destroy replacement compute
3. detach first attachment if still attached
4. destroy first compute if still active
5. destroy storage

- [ ] **Step 4: Verify tests pass**

Run:

```bash
node --test tests/production/production-verifier.test.js
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/production-verifier.js tests/production/production-verifier.test.js
git commit -m "Verify storage persistence across compute replacement"
```

### Task 3: Full Verification And Deploy

**Files:**
- No new source files.

- [ ] **Step 1: Run local verification**

```bash
npm test
npm run build
```

Expected: all tests pass and Vite build completes.

- [ ] **Step 2: Push and deploy**

```bash
git push origin main
short=$(git rev-parse --short=12 HEAD)
gh workflow run "Release OPL Cloud Image" --ref main -f ref=$(git rev-parse HEAD) -f image_tag=$short -f publish_cloud_image=true -f mirror_workspace_image=false -f workspace_source_image= -f workspace_image_tag=latest
gh workflow run "Deploy TKE Production" --ref main -f cloud_image=uswccr.ccs.tencentyun.com/oplcloud/opl-cloud:$short -f workspace_image=uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app:latest
```

Expected: both workflows complete successfully.

- [ ] **Step 3: Run one paid public staging E2E**

```bash
token_file=$(cat /tmp/opl-operator-token-file-path)
OPL_CONSOLE_ORIGIN=https://cloud.medopl.cn \
OPL_VERIFY_OPERATOR_TOKEN="$(cat "$token_file")" \
OPL_VERIFY_RUN_ID="native-e2e-$(date +%Y%m%d%H%M%S)" \
OPL_VERIFY_URL_ATTEMPTS=30 \
OPL_VERIFY_RETRY_DELAY_MS=10000 \
npm run verify:production
```

Expected: verifier prints `ok: true` and includes runtime persistence checks.

- [ ] **Step 4: Confirm no active verifier resources remain**

Query `GET /api/state?accountId=pi-production-verifier` through operator session and confirm:

```json
{
  "activeCompute": [],
  "activeStorage": [],
  "activeAttachments": [],
  "wallet": { "frozen": 0 }
}
```

- [ ] **Step 5: Commit docs if not already committed**

```bash
git status --short
```

Expected: clean worktree.

