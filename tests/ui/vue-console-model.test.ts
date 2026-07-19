import assert from "node:assert/strict";
import test from "node:test";

import {
  adminMenu,
  apiMenu,
  apiPage,
  customerMenu,
  defaultAuthenticatedRoute,
  formatAvailableBalance,
  formatCount,
  formatUsdMicros,
  needsSession,
  readinessRows,
  workspaceStatusLabel
} from "../../apps/console-ui/src/console-model.ts";

test("customer navigation exposes only the four pilot surfaces", () => {
  assert.deepEqual(customerMenu.map(({ label, path }) => ({ label, path })), [
    { label: "概览", path: "/console/overview" },
    { label: "Workspace", path: "/console/workspace" },
    { label: "API 服务", path: "/console/api" },
    { label: "账单", path: "/console/billing" }
  ]);
  assert.deepEqual(apiMenu.map(({ label, path }) => ({ label, path })), [
    { label: "概览", path: "/console/api" },
    { label: "使用记录", path: "/console/api/usage" },
    { label: "API Key", path: "/console/api/keys" }
  ]);
  assert.equal(apiPage("/console/api"), "overview");
  assert.equal(apiPage("/console/api/usage"), "usage");
  assert.equal(apiPage("/console/api/keys"), "keys");
});

test("operator navigation has the five frozen operations surfaces", () => {
  assert.deepEqual(adminMenu.map(({ label, path }) => ({ label, path })), [
    { label: "运维概览", path: "/admin/overview" },
    { label: "用户与计费账户", path: "/admin/accounts" },
    { label: "计费复核", path: "/admin/billing" },
    { label: "资源状态", path: "/admin/resources" },
    { label: "系统状态", path: "/admin/system" }
  ]);
  assert.equal(defaultAuthenticatedRoute(), "/console/overview");
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

test("unavailable and zero are distinct source facts", () => {
  assert.equal(formatAvailableBalance({ available: false, status: "unavailable" }), "暂不可用");
  assert.equal(formatAvailableBalance({ available: true, status: "available", usdMicros: 0 }), "$0.00");
  assert.equal(formatCount(undefined), "-");
  assert.equal(formatUsdMicros(undefined), "-");
  assert.deepEqual(readinessRows(null, null), [
    { label: "运行依赖", status: "-", updatedAt: "-" },
    { label: "生产依赖", status: "-", updatedAt: "-" }
  ]);
});
