import type { GatewayKeyReveal, GatewayWallet, ReadinessFact, WorkspaceRuntimeStatus } from "./api/dtos.ts";

export const customerMenu = Object.freeze([
  { id: "overview", label: "概览", path: "/console/overview", icon: "LayoutDashboard" },
  { id: "workspace", label: "Workspace", path: "/console/workspace", icon: "Database" },
  { id: "api", label: "API 服务", path: "/console/api", icon: "Server" },
  { id: "billing", label: "账单", path: "/console/billing", icon: "ReceiptText" }
]);

export const apiMenu = Object.freeze([
  { id: "overview", label: "概览", path: "/console/api" },
  { id: "usage", label: "使用记录", path: "/console/api/usage" },
  { id: "keys", label: "API Key", path: "/console/api/keys" }
]);

export const adminMenu = Object.freeze([
  { id: "overview", label: "运维概览", path: "/admin/overview", icon: "LayoutDashboard" },
  { id: "accounts", label: "用户与计费账户", path: "/admin/accounts", icon: "UsersRound" },
  { id: "billing", label: "计费复核", path: "/admin/billing", icon: "CircleDollarSign" },
  { id: "resources", label: "资源状态", path: "/admin/resources", icon: "Database" },
  { id: "system", label: "系统状态", path: "/admin/system", icon: "Activity" }
]);

export function defaultAuthenticatedRoute(): string {
  return "/console/overview";
}

export function apiPage(pathname = ""): "overview" | "usage" | "keys" {
  if (pathname.endsWith("/usage")) return "usage";
  if (pathname.endsWith("/keys")) return "keys";
  return "overview";
}

export function needsSession(pathname = ""): boolean {
  return pathname === "/admin" || pathname.startsWith("/admin/") || pathname.startsWith("/console");
}

export function formatUsdMicros(value: unknown): string {
  if (typeof value !== "number" || !Number.isSafeInteger(value)) return "-";
  return new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(value / 1_000_000);
}

export function formatCount(value: unknown): string {
  return typeof value === "number" && Number.isSafeInteger(value)
    ? new Intl.NumberFormat("zh-CN").format(value)
    : "-";
}

export function formatAvailableBalance(balance: Partial<GatewayWallet> & { available?: boolean } = {}): string {
  return balance.available === false ? "暂不可用" : formatUsdMicros(balance.usdMicros);
}

export function formatDate(value: unknown, includeTime = false): string {
  if (!value) return "-";
  const date = new Date(String(value));
  if (Number.isNaN(date.getTime())) return "-";
  return new Intl.DateTimeFormat("zh-CN", includeTime
    ? { year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false }
    : { year: "numeric", month: "2-digit", day: "2-digit" }).format(date);
}

export function workspaceStatusLabel(runtime: Partial<WorkspaceRuntimeStatus> = {}): string {
  if (runtime.status === "running" && runtime.ready === true) return "运行中";
  if (runtime.status === "unready" || runtime.status === "not_found" || runtime.status === "destroyed") return "暂不可用";
  return "暂不可用";
}

export function readinessRows(runtime: ReadinessFact | null, production: ReadinessFact | null) {
  const row = (label: string, value: ReadinessFact | null) => ({
    label,
    status: value?.ready === true ? "正常" : value?.ready === false ? "需处理" : "-",
    updatedAt: value?.generatedAt || value?.updatedAt || "-"
  });
  return [row("运行依赖", runtime), row("生产依赖", production)];
}

export function maskGatewayKey(key: GatewayKeyReveal | null): GatewayKeyReveal | null {
  if (!key) return null;
  return { ...key, value: "" };
}
