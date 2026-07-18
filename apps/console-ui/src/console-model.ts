type RecordLike = Record<string, any>;

export const customerMenu = Object.freeze([
  { id: "overview", label: "概览", path: "/console/overview", icon: "LayoutDashboard" },
  { id: "compute", label: "计算", path: "/console/compute", icon: "MonitorCog" },
  { id: "storage", label: "存储", path: "/console/storage", icon: "Database" },
  { id: "gateway", label: "Gateway", path: "/console/gateway/overview", icon: "Network" },
  { id: "billing", label: "账单", path: "/console/billing", icon: "ReceiptText" }
]);

export const gatewayMenu = Object.freeze([
  { id: "overview", label: "概览", path: "/console/gateway/overview" },
  { id: "usage", label: "Usage", path: "/console/gateway/usage" },
  { id: "keys", label: "API Keys", path: "/console/gateway/keys" }
]);

export const adminMenu = Object.freeze([
  { id: "overview", label: "运维概览", path: "/admin/overview", icon: "LayoutDashboard" },
  { id: "users", label: "用户", path: "/admin/users", icon: "UsersRound" },
  { id: "billing", label: "计费复核", path: "/admin/billing", icon: "CircleDollarSign" },
  { id: "runtime", label: "系统状态", path: "/admin/runtime", icon: "Activity" }
]);

export function defaultAuthenticatedRoute() {
  return "/console/overview";
}

export function gatewayPage(pathname = "") {
  if (pathname.endsWith("/usage")) return "usage";
  if (pathname.endsWith("/keys")) return "keys";
  return "overview";
}

export function needsSession(pathname = "") {
  return pathname === "/admin" || pathname.startsWith("/admin/") || pathname.startsWith("/console");
}

export function formatUsdMicros(value: unknown) {
  if (typeof value !== "number" || !Number.isSafeInteger(value)) return "-";
  return new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(value / 1_000_000);
}

export function formatCount(value: unknown) {
  return typeof value === "number" && Number.isSafeInteger(value)
    ? new Intl.NumberFormat("zh-CN").format(value)
    : "-";
}

export function formatAvailableBalance(balance: RecordLike = {}) {
  return balance.available === false ? "暂不可用" : formatUsdMicros(balance.usdMicros);
}

export function gatewayCanCall(summary: RecordLike = {}) {
  const status = String(summary.apiKey?.status || "").toLowerCase();
  return summary.balance?.available === true
    && Number(summary.balance?.usdMicros) > 0
    && ["active", "available", "enabled"].includes(status);
}

export function maskGatewaySummary(summary: RecordLike | null) {
  if (!summary?.apiKey) return summary;
  const apiKey = { ...summary.apiKey, revealed: false };
  delete apiKey.value;
  return { ...summary, apiKey };
}

export function storageMonthlyPrice(quotes: RecordLike = {}, packageID = "") {
  const micros = quotes[packageID]?.chargeUsdMicros;
  return typeof micros === "number" && Number.isSafeInteger(micros) ? micros : undefined;
}

export function fixedMonthlySpend(resources: RecordLike[] = []) {
  const charges = resources
    .filter((resource) => resource.billingStatus === "active")
    .map((resource) => resource.chargeUsdMicros);
  if (charges.some((value) => typeof value !== "number" || !Number.isSafeInteger(value))) return undefined;
  return charges.reduce((total, value) => total + value, 0);
}

export function workspaceMonthlyPrice(plan: RecordLike = {}, quotes: RecordLike = {}) {
  const compute = plan.price?.chargeUsdMicros;
  const storage = storageMonthlyPrice(quotes, String(plan.id || ""));
  if (typeof compute !== "number" || !Number.isSafeInteger(compute) || storage === undefined) return undefined;
  return compute + storage;
}

export function renewalSummary(resources: RecordLike[] = []) {
  if (resources.length === 0 || resources.some((resource) => typeof resource.autoRenew !== "boolean")) return "-";
  return resources.some((resource) => resource.autoRenew === true) ? "自动续费" : "手动续费";
}

export function formatDate(value: unknown, includeTime = false) {
  if (!value) return "-";
  const date = new Date(String(value));
  if (Number.isNaN(date.getTime())) return "-";
  return new Intl.DateTimeFormat("zh-CN", includeTime
    ? { year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false }
    : { year: "numeric", month: "2-digit", day: "2-digit" }).format(date);
}

export function resourceOrderStage(resource: RecordLike = {}) {
  if (!resource.id) return 0;
  const status = String(resource.status || "");
  const billing = String(resource.billingStatus || "");
  if (["running", "available", "attached", "ready"].includes(status) && billing === "active") return 4;
  if (billing === "provider_pending" || resource.providerRequestId || resource.providerResourceId) return 3;
  if (["charge_pending", "active", "manual_review", "past_due", "refunded"].includes(billing)) return 2;
  return 1;
}

export function workspaceProgress(state: RecordLike = {}, workspace: RecordLike = {}) {
  const attachments = state.storageAttachments || [];
  const attachment = attachments.find((item) => item.id === workspace.currentAttachmentId)
    || attachments.find((item) => item.workspaceId === workspace.id);
  const computeId = workspace.currentComputeAllocationId || attachment?.computeAllocationId;
  const storageId = workspace.storageId || attachment?.storageId;
  const compute = (state.computeAllocations || []).find((item) => item.id === computeId);
  const storage = (state.storageVolumes || []).find((item) => item.id === storageId);
  const runtimeStarted = Boolean(
    workspace.runtimeId
    || workspace.runtimeServiceName
    || workspace.runtime?.serviceName
    || ["running", "ready"].includes(String(workspace.runtime?.status || ""))
  );
  return [
    { label: "计算可用", complete: ["running", "ready"].includes(String(compute?.status || "")) },
    { label: "存储可用", complete: ["available", "ready"].includes(String(storage?.status || "")) },
    { label: "挂载完成", complete: ["attached", "ready"].includes(String(attachment?.status || "")) },
    { label: "Workspace 启动", complete: runtimeStarted },
    { label: "可打开", complete: workspace.openable === true || workspace.accessState === "available" }
  ];
}

export function resourceStatusLabel(resource: RecordLike = {}) {
  if (resourceNeedsAttention(resource)) return "需要处理";
  if (["running", "available", "attached", "ready"].includes(String(resource.status || ""))) return "可用";
  return "开通中";
}

export function resourceNeedsAttention(resource: RecordLike = {}) {
  const needsAttention = ["failed", "unknown", "manual_review", "past_due", "refunded"];
  return needsAttention.includes(String(resource.status || ""))
    || needsAttention.includes(String(resource.billingStatus || ""));
}

export function operatorAttentionItems(management: RecordLike = {}, summary: RecordLike = {}) {
  const resources = [
    ...(management.computeAllocations || []).map((item) => ({ ...item, kind: "计算", resourceType: "compute", status: item.billingStatus || item.status })),
    ...(management.storageVolumes || []).map((item) => ({ ...item, kind: "存储", resourceType: "storage", status: item.billingStatus || item.status }))
  ].filter((item) => ["manual_review", "past_due"].includes(String(item.billingStatus || "")));
  const failed = (summary.failedOperations || []).map((item) => ({
    ...item,
    id: item.id || item.operationId,
    kind: "失败操作",
    status: item.status || "failed"
  }));
  const anomalies = (summary.resourceAnomalies || []).map((item) => ({
    ...item,
    id: item.id || item.resourceId || item.workspaceId,
    kind: "资源异常",
    status: item.status
  }));
  return [...resources, ...failed, ...anomalies];
}

export function readinessRows(runtime: RecordLike | null, production: RecordLike | null) {
  const row = (label: string, value: RecordLike | null) => ({
    label,
    status: value?.ready === true ? "正常" : value?.ready === false ? "需处理" : "-",
    updatedAt: value?.generatedAt || value?.updatedAt || "-"
  });
  return [row("运行依赖", runtime), row("生产依赖", production)];
}

export function customerBillingStatusLabel(status: unknown) {
  const labels: Record<string, string> = {
    active: "有效",
    preparing: "受理中",
    charge_pending: "订单确认中",
    provider_pending: "云端开通中",
    manual_review: "需要人工处理",
    past_due: "已到期",
    refunded: "已退款",
    failed: "失败"
  };
  return labels[String(status || "")] || "处理中";
}
