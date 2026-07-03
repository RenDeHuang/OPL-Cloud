export function money(value) {
  return `¥${Number(value || 0).toFixed(2)}`;
}

export function planHold(plan) {
  if (!plan) return 0;
  return Number(plan.price?.computeHourly || 0) * 24 * 7
    + Number(plan.price?.storageGbMonth || 0) * Number(plan.diskGb || 0) / 30 * 7;
}

export function available(wallet) {
  return Number(wallet?.available ?? (Number(wallet?.balance || 0) - Number(wallet?.frozen || 0)));
}

export function statusLabel(workspace) {
  if (!workspace) return "No Workspace";
  const labels = {
    running: "运行中",
    storage_hold_exhausted: "存储冻结不足",
    compute_hold_exhausted: "计算冻结不足",
    destroyed: "已销毁",
    failed: "失败"
  };
  return labels[workspace.state] || workspace.state;
}

export function valueLabel(value) {
  const labels = {
    active: "有效",
    available: "可用",
    running: "运行中",
    closed: "计费结束",
    destroyed: "已销毁",
    failed: "失败",
    retained: "保留",
    attached_retained: "挂载保留",
    detached_retained: "卸载保留",
    hold_exhausted: "冻结不足",
    ready: "就绪",
    blocked: "阻塞"
  };
  return labels[value] || value || "-";
}

export function statusColor(value) {
  if (["running", "active", "available", "ready"].includes(value)) return "green";
  if (["failed", "destroyed", "hold_exhausted", "blocked"].includes(value)) return "red";
  if (["closed", "detached_retained"].includes(value)) return "orange";
  return "blue";
}

export function packageText(plan) {
  if (!plan) return "-";
  return `${plan.cpu} CPU / ${plan.memoryGb}GB / ${plan.diskGb}GB`;
}

export function usageAmount(logs, resourceType = "") {
  return (logs || [])
    .filter((log) => !resourceType || log.resourceType === resourceType)
    .reduce((sum, log) => sum + Number(log.amount || 0), 0);
}

export function usageQuantity(logs, resourceType) {
  return (logs || [])
    .filter((log) => log.resourceType === resourceType)
    .reduce((sum, log) => sum + Number(log.quantity || 0), 0);
}
