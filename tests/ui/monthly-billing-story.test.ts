import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../", import.meta.url);

async function source(path) {
  return readFile(new URL(path, root), "utf8");
}

test("frozen monthly catalog defines separate Basic and Pro compute and storage prices", async () => {
  const pricing = JSON.parse(await source("packages/contracts/opl-cloud-pricing-contract.json"));

  assert.deepEqual(pricing.computeMonthly.basic, { cnyCents: 35000, usdMicros: 50000000 });
  assert.deepEqual(pricing.computeMonthly.pro, { cnyCents: 150000, usdMicros: 214285715 });
  assert.deepEqual(pricing.storageMonthly["10"], { cnyCents: 1800, usdMicros: 2571429 });
  assert.deepEqual(pricing.storageMonthly["100"], { cnyCents: 18000, usdMicros: 25714286 });
});

test("Billing renders compute and storage components from the server catalog", async () => {
  const billingSource = await source("apps/console-ui/src/pages/billing/BillingPage.tsx");

  assert.match(billingSource, /getPricingCatalog/);
  assert.match(billingSource, /catalog\?\.packages/);
  assert.match(billingSource, /storagePer10GbMonthly/);
  assert.match(billingSource, /plan\.available/);
  assert.match(billingSource, /计算月价/);
  assert.match(billingSource, /存储月价/);
  assert.doesNotMatch(billingSource, /¥350\.00|¥1,?500\.00|\$50\.000000/);
});

test("Workspace launch is one recoverable flow over existing resource APIs", async () => {
  const createSource = await source("apps/console-ui/src/pages/workspaces/CreateWorkspacePage.tsx");

  for (const label of [
    "选择套餐与存储",
    "确认月度总价",
    "完成月费扣款",
    "准备 PREPAID 资源",
    "启动 Gateway 与 Runtime",
    "打开 Workspace URL"
  ]) {
    assert.match(createSource, new RegExp(label));
  }
  assert.match(createSource, /<Steps/);
  assert.match(createSource, /getPricingCatalog/);
  assert.match(createSource, /previewPricing/);
  assert.match(createSource, /totalChargeUsdMicros/);
  assert.match(createSource, /launchPending\.current/);
  assert.match(createSource, /createComputeAllocation\(/);
  assert.match(createSource, /createStorageVolume\(/);
  assert.match(createSource, /attachStorage\(/);
  assert.match(createSource, /createWorkspace\(/);
  assert.ok(createSource.indexOf("createComputeAllocation(") < createSource.indexOf("createStorageVolume("));
  assert.ok(createSource.indexOf("createStorageVolume(") < createSource.indexOf("attachStorage("));
  assert.ok(createSource.indexOf("attachStorage(") < createSource.indexOf("createWorkspace("));
  assert.doesNotMatch(createSource, /routeTo\("compute-allocations\.create"\)/);
  assert.doesNotMatch(createSource, /routeTo\("storage\.create"\)/);
  assert.doesNotMatch(createSource, /routeTo\("attachment\.create"\)/);
  assert.match(createSource, /firstIncomplete === -1 \? 5 : firstIncomplete/);
});

test("Workspace launch derives progress and pricing from one attached resource pair", async () => {
  const createSource = await source("apps/console-ui/src/pages/workspaces/CreateWorkspacePage.tsx");

  assert.ok(createSource.indexOf("const attachment = ") < createSource.indexOf("const compute = "));
  assert.match(createSource, /item\.id === attachment\?\.computeAllocationId/);
  assert.match(createSource, /item\.id === attachment\?\.storageId/);
});

test("Workspace launch stops on non-terminal resources and keeps placeholder recovery visible", async () => {
  const createSource = await source("apps/console-ui/src/pages/workspaces/CreateWorkspacePage.tsx");

  assert.match(createSource, /if \(existingWorkspace && !isWorkspaceLaunchPlaceholder\(existingWorkspace\)\)/);
  assert.match(createSource, /workspaceName: existingWorkspace\?\.name \|\| values\.workspaceName/);
  assert.match(createSource, /createWorkspaceLaunchIntent\(requested, launchIntent\.current, launchScope\)/);
  assert.match(createSource, /const launchInProgress = Boolean/);
  assert.match(createSource, /label=\{launchInProgress \? "继续同一开通请求" : "开通 Workspace"\}/);
  assert.match(createSource, /const computeStep = await launchWorkspaceResource/);
  assert.match(createSource, /if \(!computeStep\.ready\) \{[\s\S]*?return;[\s\S]*?\}[\s\S]*?const storageStep = await launchWorkspaceResource/);
  assert.match(createSource, /if \(!storageStep\.ready\) \{[\s\S]*?return;[\s\S]*?\}[\s\S]*?let nextAttachment/);
});

test("resource provisioning tells the debit-first PREPAID sequence", async () => {
  const resourceSource = await source("apps/console-ui/src/pages/resources/ResourceProvisioningPages.tsx");
  const sharedSource = await source("apps/console-ui/src/pages/shared/commercial-console.tsx");

  assert.match(resourceSource, /computeAllocationStages = Object\.freeze\(\["已提交", "只读资源预检中", "月费扣款中", "PREPAID 开通中", "资源认领中", "月度权益已激活", "Runtime 部署中", "URL 可用"\]\)/);
  assert.match(resourceSource, /storageCreateStages = Object\.freeze\(\["已提交", "只读资源预检中", "月费扣款中", "PREPAID 开通中", "资源认领中", "月度权益已激活", "可挂载"\]\)/);
  assert.match(resourceSource, /只读资源预检和月费扣款；扣款成功后开通 PREPAID 资源、完成资源认领与权益激活，再部署 Runtime 并生成 URL/);
  assert.match(resourceSource, /只读资源预检和月费扣款；扣款成功后开通 PREPAID 存储、完成资源认领与权益激活，再进入可挂载状态/);
  assert.doesNotMatch(resourceSource, /云资源准备中.*余额扣款中|存储准备中.*余额扣款中|正在创建云资源、完成月费扣款|正在创建并准备挂载/);
  assert.doesNotMatch(sharedSource, /云资源准备中.*余额扣款中|正在创建云资源、完成月费扣款/);
});
