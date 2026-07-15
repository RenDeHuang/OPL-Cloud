export function money(value) {
  return `¥${Number(value || 0).toFixed(2)}`;
}

export function moneyCents(value) {
  return money(Number(value || 0) / 100);
}

export function usdMicros(value) {
  return `$${(Number(value || 0) / 1_000_000).toFixed(6)}`;
}

export function usdBalance(balance: any = {}) {
  if (balance.available === false || balance.status === "unavailable") return "-";
  return usdMicros(balance.usdMicros);
}

export function paidThrough(value = "") {
  if (!value) return "-";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleDateString("zh-CN", { timeZone: "UTC" });
}

export function statusLabel(workspace) {
  if (!workspace) return "No Workspace";
  const labels = {
    running: "运行中",
    retained: "存储已保留",
    destroyed: "已销毁",
    failed: "失败"
  };
  return labels[workspace.state] || workspace.state;
}

export function valueLabel(value) {
  const labels = {
    preparing: "创建资源",
    charge_pending: "等待扣款",
    active: "有效",
    available: "可用",
    running: "运行中",
    stopped: "已停止",
    destroyed: "已销毁",
    failed: "失败",
    retained: "保留",
    attached_retained: "挂载保留",
    detached_retained: "卸载保留",
    restored_retained: "已恢复",
    past_due: "续费失败",
    renewal_pending: "续费中",
    manual_review: "人工复核",
    ready: "就绪",
    blocked: "阻塞"
  };
  return labels[value] || value || "-";
}

export function customerSafeMessage(value = "", fallback = "操作正在处理中，请稍后刷新。") {
  const raw = String(value || fallback);
  if (/upstream_unavailable|bad gateway|workspace_url_failed|workspace_runtime_not_ready|workspace_url_not_ready|502|503/i.test(raw)) {
    return "正在分发 Docker，预计 3-5 分钟，请稍后再打开 URL。";
  }
  return raw;
}

export function workspaceUrlReady(workspace: any = {}) {
  return Boolean(workspace.openable);
}

export function workspaceAccessLabel(workspace: any = {}) {
  if (workspace.accessState === "available") return "可打开";
  if (workspace.accessState === "distributing") return "分发中";
  return "未启用";
}

export function workspaceOpenActionLabel(workspace: any = {}) {
  if (workspace.accessState === "available") return "打开";
  if (workspace.accessState === "distributing") return "分发中";
  return "已停用";
}

export function workspaceAccessTone(workspace: any = {}) {
  if (workspace.accessState === "available") return "good";
  if (workspace.accessState === "distributing") return "warn";
  return "neutral";
}

export function statusColor(value) {
  if (["running", "active", "available", "ready"].includes(value)) return "green";
  if (["failed", "destroyed", "past_due", "manual_review", "blocked"].includes(value)) return "red";
  if (["stopped"].includes(value)) return "orange";
  return "blue";
}

export function packageText(plan) {
  if (!plan) return "-";
  return `${plan.cpu} CPU / ${plan.memoryGb}GB / ${plan.diskGb}GB`;
}
