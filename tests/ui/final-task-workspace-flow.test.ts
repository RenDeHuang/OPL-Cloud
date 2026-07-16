import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../", import.meta.url);

async function source(path) {
  return readFile(new URL(path, root), "utf8");
}

test("overview presents one Workspace with balance entitlements expiry and recovery", async () => {
  const overview = await source("apps/console-ui/src/pages/OverviewPage.tsx");

  for (const label of ["Sub2API 余额", "计算权益", "存储权益", "有效期至", "异常", "恢复"]) {
    assert.match(overview, new RegExp(label));
  }
  assert.match(overview, /const workspace = workspaces\[0\]/);
  assert.match(overview, /!workspace &&/);
  assert.match(overview, /workspaceOpenActionLabel/);
  assert.doesNotMatch(overview, /title="开通流程"/);
  assert.doesNotMatch(overview, /routeTo\("compute-allocations\.create"\)|routeTo\("storage\.create"\)|routeTo\("attachment\.create"\)/);
});

test("existing non-placeholder Workspace hides creation entry points", async () => {
  const [overview, workspaces, createPage, resources] = await Promise.all([
    source("apps/console-ui/src/pages/OverviewPage.tsx"),
    source("apps/console-ui/src/pages/workspaces/WorkspacesPage.tsx"),
    source("apps/console-ui/src/pages/workspaces/CreateWorkspacePage.tsx"),
    source("apps/console-ui/src/pages/resources/ResourceProvisioningPages.tsx")
  ]);

  assert.match(overview, /!workspace && \{ label: "开通 Workspace"/);
  assert.match(workspaces, /workspaces\.length === 0/);
  assert.match(createPage, /if \(existingWorkspace && !isWorkspaceLaunchPlaceholder\(existingWorkspace\)\)/);
  assert.match(resources, /hasWorkspace \? undefined/);
});

test("billing exposes catalog prices and settlement outcomes", async () => {
  const billing = await source("apps/console-ui/src/pages/billing/BillingPage.tsx");

  assert.match(billing, /getPricingCatalog/);
  assert.match(billing, /plan\.price\?\.monthlyPriceCnyCents/);
  assert.match(billing, /storagePer10GbMonthly/);
  for (const label of ["扣款", "退款", "人工复核", "有效期至"]) {
    assert.match(billing, new RegExp(label));
  }
  assert.doesNotMatch(billing, /35000|150000|1800|18000/);
});

test("Workspace detail exposes owner-only Gateway sync without raw keys", async () => {
  const detail = await source("apps/console-ui/src/pages/workspaces/WorkspaceDetailPage.tsx");

  assert.match(detail, /rotateWorkspaceGatewaySecret/);
  assert.match(detail, /session\.user\?\.role === "owner"/);
  assert.match(detail, /同步\/轮换 Gateway Key/);
  assert.match(detail, /fingerprint/);
  assert.doesNotMatch(detail, /apiKey\.value|gatewayApiKey|localStorage|sessionStorage/);
});

test("compute storage and attachment live under advanced resource management", async () => {
  const workspaces = await source("apps/console-ui/src/pages/workspaces/WorkspacesPage.tsx");

  assert.match(workspaces, /title="高级资源管理"/);
  assert.match(workspaces, /routeTo\("compute-allocations\.list"\)/);
  assert.match(workspaces, /routeTo\("storage\.list"\)/);
  assert.match(workspaces, /routeTo\("attachment\.list"\)/);
});
