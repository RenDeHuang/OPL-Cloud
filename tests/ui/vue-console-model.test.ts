import assert from "node:assert/strict";
import test from "node:test";

import {
  adminMenu,
  customerMenu,
  defaultAuthenticatedRoute,
  fixedMonthlySpend,
  formatCount,
  formatUsdMicros,
  gatewayMenu,
  gatewayPage,
  gatewayCanCall,
  needsSession,
  operatorAttentionItems,
  readinessRows,
  resourceOrderStage,
  resourceNeedsAttention,
  resourceStatusLabel,
  renewalSummary,
  storageMonthlyPrice,
  workspaceMonthlyPrice,
  workspaceProgress
} from "../../apps/console-ui/src/console-model.ts";

test("customer navigation stays visible and Gateway has three real routes", () => {
  assert.deepEqual(customerMenu.map(({ label, path }) => ({ label, path })), [
    { label: "概览", path: "/console/overview" },
    { label: "计算", path: "/console/compute" },
    { label: "存储", path: "/console/storage" },
    { label: "Gateway", path: "/console/gateway/overview" },
    { label: "账单", path: "/console/billing" }
  ]);
  assert.deepEqual(gatewayMenu.map(({ label, path }) => ({ label, path })), [
    { label: "概览", path: "/console/gateway/overview" },
    { label: "Usage", path: "/console/gateway/usage" },
    { label: "API Keys", path: "/console/gateway/keys" }
  ]);
  assert.equal(gatewayPage("/console/gateway"), "overview");
  assert.equal(gatewayPage("/console/gateway/usage"), "usage");
  assert.equal(gatewayPage("/console/gateway/keys"), "keys");
});

test("the unique operator gets additive operations navigation without a separate landing page", () => {
  assert.deepEqual(adminMenu.map(({ label, path }) => ({ label, path })), [
    { label: "运维概览", path: "/admin/overview" },
    { label: "用户", path: "/admin/users" },
    { label: "计费复核", path: "/admin/billing" },
    { label: "系统状态", path: "/admin/runtime" }
  ]);
  assert.equal(defaultAuthenticatedRoute(), "/console/overview");
});

test("public and login routes render without waiting for session recovery", () => {
  assert.equal(needsSession("/"), false);
  assert.equal(needsSession("/login"), false);
  assert.equal(needsSession("/console/overview"), true);
  assert.equal(needsSession("/admin"), true);
});

test("Workspace progress is derived from state resources only", () => {
  const state = {
    workspaces: [{
      id: "ws-1",
      currentComputeAllocationId: "compute-1",
      currentAttachmentId: "attachment-1",
      runtimeId: "runtime-1",
      openable: true
    }],
    computeAllocations: [{ id: "compute-1", status: "running" }],
    storageVolumes: [{ id: "storage-1", status: "available" }],
    storageAttachments: [{
      id: "attachment-1",
      computeAllocationId: "compute-1",
      storageId: "storage-1",
      status: "attached"
    }]
  };

  assert.deepEqual(workspaceProgress(state, state.workspaces[0]), [
    { label: "计算可用", complete: true },
    { label: "存储可用", complete: true },
    { label: "挂载完成", complete: true },
    { label: "Workspace 启动", complete: true },
    { label: "可打开", complete: true }
  ]);
  assert.equal(workspaceProgress({ ...state, storageVolumes: [] }, state.workspaces[0])[1].complete, false);
});

test("four-stage order progress never claims a stage without matching API state", () => {
  assert.equal(resourceOrderStage({ id: "compute-1", status: "provisioning", billingStatus: "preparing" }), 1);
  assert.equal(resourceOrderStage({ id: "compute-1", status: "provisioning", billingStatus: "provider_pending" }), 3);
  assert.equal(resourceOrderStage({ id: "compute-1", status: "running", billingStatus: "active" }), 4);
  assert.equal(resourceOrderStage({ id: "storage-1", status: "available", billingStatus: "active" }), 4);
});

test("billing review is not presented as ordinary provisioning", () => {
  assert.equal(resourceNeedsAttention({ status: "provisioning", billingStatus: "manual_review" }), true);
  assert.equal(resourceNeedsAttention({ status: "provisioning", billingStatus: "provider_pending" }), false);
  assert.equal(resourceStatusLabel({ status: "running", billingStatus: "manual_review" }), "需要处理");
});

test("USD values are formatted from integer micros", () => {
  assert.equal(formatUsdMicros(83_200_000), "$83.20");
  assert.equal(formatUsdMicros(undefined), "-");
  assert.equal(formatUsdMicros(null), "-");
  assert.equal(formatUsdMicros("83200000"), "-");
});

test("counts stay unavailable until an integer fact exists", () => {
  assert.equal(formatCount(12), "12");
  assert.equal(formatCount(undefined), "-");
  assert.equal(formatCount("12"), "-");
});

test("operator attention is projected only from real resource and operation facts", () => {
  const items = operatorAttentionItems({
    computeAllocations: [
      { id: "compute-review", accountId: "acct-1", workspaceId: "ws-1", billingStatus: "manual_review", updatedAt: "2026-07-17T01:00:00Z" },
      { id: "compute-ok", accountId: "acct-1", billingStatus: "active" }
    ],
    storageVolumes: [{ id: "storage-due", accountId: "acct-2", billingStatus: "past_due" }]
  }, {
    failedOperations: [{ id: "op-failed", accountId: "acct-3", workspaceId: "ws-3", status: "failed" }],
    resourceAnomalies: [{ id: "anomaly-1", workspaceId: "ws-4", status: "missing_storage" }]
  });

  assert.deepEqual(items.map((item) => [item.kind, item.id, item.status]), [
    ["计算", "compute-review", "manual_review"],
    ["存储", "storage-due", "past_due"],
    ["失败操作", "op-failed", "failed"],
    ["资源异常", "anomaly-1", "missing_storage"]
  ]);
});

test("readiness never invents a healthy state", () => {
  assert.deepEqual(readinessRows(null, null), [
    { label: "运行依赖", status: "-", updatedAt: "-" },
    { label: "生产依赖", status: "-", updatedAt: "-" }
  ]);
  assert.deepEqual(readinessRows({ ready: true, generatedAt: "runtime-time" }, { ready: false, generatedAt: "production-time" }), [
    { label: "运行依赖", status: "正常", updatedAt: "runtime-time" },
    { label: "生产依赖", status: "需处理", updatedAt: "production-time" }
  ]);
});

test("Gateway usability requires a live Key and positive spendable balance", () => {
  const activeKey = { apiKey: { status: "active" }, balance: { available: true, usdMicros: 1 } };
  assert.equal(gatewayCanCall(activeKey), true);
  assert.equal(gatewayCanCall({ ...activeKey, balance: { available: true, usdMicros: 0 } }), false);
  assert.equal(gatewayCanCall({ ...activeKey, apiKey: { status: "disabled" } }), false);
});

test("storage price uses the exact server preview without block rounding", () => {
  const quotes = {
    basic: { chargeUsdMicros: 2_571_429 },
    pro: { chargeUsdMicros: 25_714_286 }
  };
  assert.equal(storageMonthlyPrice(quotes, "basic"), 2_571_429);
  assert.equal(storageMonthlyPrice(quotes, "pro"), 25_714_286);
  assert.equal(storageMonthlyPrice({ basic: { chargeUsdMicros: null } }, "basic"), undefined);
});

test("missing monthly price facts stay unavailable instead of becoming zero", () => {
  assert.equal(fixedMonthlySpend([]), 0);
  assert.equal(fixedMonthlySpend([{ billingStatus: "active" }]), undefined);
  assert.equal(fixedMonthlySpend([{ billingStatus: "active", chargeUsdMicros: "50000000" }]), undefined);
  assert.equal(fixedMonthlySpend([
    { billingStatus: "active", chargeUsdMicros: 50_000_000 },
    { billingStatus: "active", chargeUsdMicros: 2_571_429 }
  ]), 52_571_429);

  const basic = { id: "basic", price: { chargeUsdMicros: 50_000_000 } };
  assert.equal(workspaceMonthlyPrice(basic, {}), undefined);
  assert.equal(workspaceMonthlyPrice(basic, { basic: { chargeUsdMicros: 2_571_429 } }), 52_571_429);
});

test("renewal summary does not invent a policy when no resources exist", () => {
  assert.equal(renewalSummary([]), "-");
  assert.equal(renewalSummary([{}]), "-");
  assert.equal(renewalSummary([{ autoRenew: false }]), "手动续费");
  assert.equal(renewalSummary([{ autoRenew: true }]), "自动续费");
});
