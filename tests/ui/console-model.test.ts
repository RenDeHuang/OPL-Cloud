import assert from "node:assert/strict";
import test from "node:test";

import {
  apiPage,
  formatAvailableBalance,
  formatCount,
  formatUsdMicros,
  hasSufficientWorkspaceLaunchBalance,
  needsSession,
  readinessRows,
  workspaceIdFromPath,
  workspacePage,
  workspaceStatusLabel
} from "../../apps/console-ui/src/console-model.ts";

test("API routes select the current view", () => {
  assert.equal(apiPage("/console/api"), "overview");
  assert.equal(apiPage("/console/api/usage"), "usage");
  assert.equal(apiPage("/console/api/keys"), "keys");
});

test("Workspace routes separate list, launch, and refreshable detail views", () => {
  assert.equal(workspacePage("/console/workspaces"), "list");
  assert.equal(workspacePage("/console/workspaces/"), "list");
  assert.equal(workspacePage("/console/workspaces/new"), "new");
  assert.equal(workspacePage("/console/workspaces/ws%20alpha"), "detail");
  assert.equal(workspacePage("/console/workspaces/ws-alpha"), "detail");
  assert.equal(workspaceIdFromPath("/console/workspaces/ws%20alpha"), "ws alpha");
  assert.equal(workspaceIdFromPath("/console/workspaces/new"), "");
  assert.equal(workspacePage("/console/workspaces/ws-alpha/extra"), null);
  assert.equal(workspacePage("/console/workspace"), null);
});

test("public and login routes render without session recovery", () => {
  assert.equal(needsSession("/"), false);
  assert.equal(needsSession("/login"), false);
  assert.equal(needsSession("/console/overview"), true);
  assert.equal(needsSession("/admin"), true);
});

test("Workspace status never invents a running state", () => {
  assert.equal(workspaceStatusLabel({ status: "running", ready: true }), "运行中");
  assert.equal(workspaceStatusLabel({ status: "unready", ready: false }), "暂不可用");
  assert.equal(workspaceStatusLabel({}), "暂不可用");
});

test("Workspace launch requires balance strictly greater than the server quote", () => {
  assert.equal(hasSufficientWorkspaceLaunchBalance("52580000", 52_580_000), false);
  assert.equal(hasSufficientWorkspaceLaunchBalance("52580001", 52_580_000), true);
});

test("unavailable and zero are distinct source facts", () => {
  assert.equal(formatAvailableBalance({ available: false, status: "unavailable" }), "暂不可用");
  assert.equal(formatAvailableBalance({ available: true, status: "available", usdMicros: "0" }), "$0.00");
  assert.equal(formatUsdMicros("9223372036854775807"), "$9,223,372,036,854.78");
  assert.equal(formatUsdMicros("-9223372036854775808"), "-$9,223,372,036,854.78");
  assert.equal(formatCount(undefined), "-");
  assert.equal(formatUsdMicros(undefined), "-");
  assert.deepEqual(readinessRows(null, null), [
    { label: "运行依赖", status: "暂不可用", updatedAt: "-" },
    { label: "生产依赖", status: "暂不可用", updatedAt: "-" }
  ]);
});
