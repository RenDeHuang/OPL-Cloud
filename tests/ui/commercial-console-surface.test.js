import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const repoRoot = new URL("../../", import.meta.url);

async function source(relativePath) {
  return readFile(new URL(relativePath, repoRoot), "utf8");
}

test("commercial Console UI is built from the maintained surface component layer", async () => {
  const surfaceSource = await source("packages/console/ui/pages/shared/commercial-console.jsx");

  for (const exportName of [
    "ConsoleSurface",
    "MetricStrip",
    "InsightPanel",
    "StatusPill",
    "ResourceSplit",
    "ActionGroup",
    "TimelineList",
    "ObjectTable"
  ]) {
    assert.match(surfaceSource, new RegExp(`export function ${exportName}\\b`), `${exportName} must be exported by the commercial UI layer`);
  }
});

test("business-chain pages use the commercial surface instead of old card stacks", async () => {
  for (const page of [
    "packages/console/ui/pages/OverviewPage.jsx",
    "packages/console/ui/pages/workspaces/WorkspacesPage.jsx",
    "packages/console/ui/pages/workspaces/WorkspaceDetailPage.jsx",
    "packages/console/ui/pages/workspaces/CreateWorkspacePage.jsx",
    "packages/console/ui/pages/billing/BillingPage.jsx",
    "packages/console/ui/pages/gateway/GatewayPage.jsx",
    "packages/console/ui/pages/account/AccountPage.jsx",
    "packages/console/ui/pages/support/SupportPage.jsx",
    "packages/console/ui/pages/admin/AdminOverviewPage.jsx"
  ]) {
    const pageSource = await source(page);
    assert.match(pageSource, /shared\/commercial-console\.jsx/, `${page} must import the commercial Console surface`);
    assert.doesNotMatch(pageSource, /StatisticCard/, `${page} must not use the old metric card layer directly`);
  }
});

test("public entry is Console-first and does not use the retired marketing hero shell", async () => {
  const homeSource = await source("packages/console/ui/pages/HomePage.jsx");
  assert.match(homeSource, /publicConsole/, "public home should present the Console product surface");
  assert.doesNotMatch(homeSource, /homeHero|heroPreview|chainPreview/, "retired marketing hero classes must stay removed");
});

test("authenticated shell is branded as OPL Console", async () => {
  const shellSource = await source("packages/console/ui/pages/ConsolePage.jsx");
  assert.match(shellSource, /title="OPL Console"/, "authenticated app shell must use OPL Console product naming");
  assert.doesNotMatch(shellSource, /title="OPL Cloud"/, "authenticated app shell must not retain old OPL Cloud naming");
});

test("visible app chrome does not retain old Cloud or reserved backlog copy", async () => {
  for (const page of [
    "packages/console/ui/main.jsx",
    "packages/console/ui/pages/LoginPage.jsx",
    "packages/console/ui/pages/admin/AdminOverviewPage.jsx"
  ]) {
    const pageSource = await source(page);
    assert.doesNotMatch(pageSource, /Loading OPL Cloud|> OPL Cloud</, `${page} must use OPL Console in visible chrome`);
    assert.doesNotMatch(pageSource, /status: "reserved"|value: "Backlog"|not in current launch/, `${page} must not show reserved/backlog product copy`);
  }
});

test("create Workspace flow is a single commercial submit action", async () => {
  const createSource = await source("packages/console/ui/pages/workspaces/CreateWorkspacePage.jsx");
  const stateSource = await source("packages/console/ui/store/console-state.js");

  assert.doesNotMatch(createSource, /StepsForm/, "create flow must not hide provisioning behind a multi-step wizard");
  assert.match(createSource, /htmlType="submit"/, "create flow must expose one clear submit button");
  assert.match(createSource, /attachmentId/, "Workspace entry creation must require an attached compute/storage pair");
  assert.match(createSource, /const created = await runAction/, "create flow must inspect action success before navigating");
  assert.match(createSource, /if \(created\) navigate/, "create flow must not navigate away after failed provisioning");
  assert.match(stateSource, /return result \|\| true/, "runAction must return successful action payloads");
  assert.match(stateSource, /return false/, "runAction must report failed actions");
});

test("resource provisioning pages call resource APIs instead of disabled placeholders", async () => {
  const resourceSource = await source("packages/console/ui/pages/resources/ResourceProvisioningPages.jsx");

  for (const apiName of ["createComputeAllocation", "createStorageVolume", "attachStorage"]) {
    assert.match(resourceSource, new RegExp(`${apiName}\\(`), `resource UI must call ${apiName}`);
  }
  assert.doesNotMatch(resourceSource, /disabled: true/, "resource creation actions must not remain disabled placeholders");
});

test("resource provisioning UI shows price, hold, balance impact, and operation state", async () => {
  const resourceSource = await source("packages/console/ui/pages/resources/ResourceProvisioningPages.jsx");

  for (const requiredSignal of [
    "computeHourlyPrice",
    "computeHoldAmount",
    "storageGbMonthPrice",
    "storageHourlyEstimate",
    "balanceAfterHold",
    "operationId",
    "safeMessage",
    "providerRequestId"
  ]) {
    assert.match(resourceSource, new RegExp(requiredSignal), `resource UI must expose ${requiredSignal}`);
  }
  assert.match(resourceSource, /¥\/小时|每小时/, "compute creation must show hourly pricing before submit");
  assert.match(resourceSource, /冻结|hold/i, "resource creation must show prepaid hold impact before submit");
});

test("Admin users surface is backed by management state and can create login users", async () => {
  const adminSource = await source("packages/console/ui/pages/admin/AdminOverviewPage.jsx");
  const stateSource = await source("packages/console/ui/store/console-state.js");
  const apiSource = await source("packages/console/ui/api/console-read-api.js");
  const routesSource = await source("packages/console/ui/routes/opl-routes.js");

  assert.match(stateSource, /getManagementState/, "admin state hook must load the management read model");
  assert.match(apiSource, /createUser/, "admin API client must expose user creation");
  assert.match(apiSource, /"\/api\/users"/, "user creation client must call POST /api/users");
  assert.match(adminSource, /managementState\.users/, "Admin Users must list management users, not only session.user");
  assert.match(adminSource, /createUser\(/, "Admin Users must submit the new user form");
  assert.doesNotMatch(adminSource, /data=\{\[\{\s*id: session\.user\.id/, "Admin Users must not render only the current session user");
  assert.doesNotMatch(adminSource, /新建用户<\/Button><\/Tooltip>|disabled>新建用户/, "new user action must not remain disabled");
  assert.match(routesSource, /"POST \/api\/users"/, "admin.users route contract must declare user creation");
});

test("Workspace detail links to first-class resources and excludes retired compute lifecycle controls", async () => {
  const listSource = await source("packages/console/ui/pages/workspaces/WorkspacesPage.jsx");
  const detailSource = await source("packages/console/ui/pages/workspaces/WorkspaceDetailPage.jsx");

  assert.match(listSource, /资源/, "Workspace list must expose a resource-management entry");
  assert.match(listSource, /routeTo\("workspace.detail"/, "Workspace list resource entry must route to Workspace detail");
  assert.match(detailSource, /routeTo\("compute-allocations.detail"/, "detail must link to the attached ComputeAllocation");
  assert.match(detailSource, /routeTo\("storage.detail"/, "detail must link to the attached StorageVolume");
  assert.match(detailSource, /routeTo\("attachment.detail"/, "detail must link to the StorageAttachment");
});

test("active UI and docs describe the ComputeAllocation, StorageVolume, attachment, and URL-entry chain", async () => {
  for (const file of [
    "README.md",
    "docs/invariants.md",
    "docs/runtime/production-runbook.md",
    "packages/console/ui/pages/HomePage.jsx",
    "packages/console/ui/pages/OverviewPage.jsx",
    "packages/console/ui/pages/billing/BillingPage.jsx",
    "packages/console/ui/pages/workspaces/WorkspacesPage.jsx",
    "packages/console/ui/pages/admin/AdminOverviewPage.jsx"
  ]) {
    const text = await source(file);
    assert.match(text, /ComputeAllocation|compute allocation|计算分配|计算/, `${file} must describe compute allocation capability`);
    assert.match(text, /StorageVolume|storage volume|存储资源|存储/, `${file} must describe storage volume capability`);
    assert.match(text, /Workspace URL|URL entry|Workspace/, `${file} must describe Workspace as an access entry`);
  }
});
