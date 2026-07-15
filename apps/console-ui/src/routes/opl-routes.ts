import { defaultLaunchConfig, isFeatureEnabled } from "../config/launch-config.ts";

type AnyRecord = Record<string, any>;

function currentRoute(route) {
  return Object.freeze({
    ...route,
    requiresAdmin: route.role === "admin",
    requiresAuth: route.role === "lab_owner" || route.role === "admin"
  });
}

export const oplRoutes = Object.freeze([
  currentRoute({
    id: "public.home",
    path: "/",
    label: "首页",
    area: "public",
    role: "public"
  }),
  currentRoute({
    id: "public.pricing",
    path: "/pricing",
    label: "价格",
    area: "public",
    role: "public"
  }),
  currentRoute({
    id: "public.docs",
    path: "/docs",
    label: "文档",
    area: "public",
    role: "public"
  }),
  currentRoute({
    id: "public.status",
    path: "/status",
    label: "服务状态",
    area: "public",
    role: "public"
  }),
  currentRoute({
    id: "auth.login",
    path: "/login",
    label: "登录",
    area: "auth",
    role: "public"
  }),
  currentRoute({
    id: "auth.logout",
    path: "/logout",
    label: "退出登录",
    area: "auth",
    role: "lab_owner"
  }),
  currentRoute({
    id: "console.root",
    path: "/console",
    label: "控制台",
    area: "console",
    role: "lab_owner",
    redirectRouteId: "console.overview"
  }),
  currentRoute({
    id: "console.overview",
    path: "/console/overview",
    label: "概览",
    area: "console",
    role: "lab_owner",
    menu: true
  }),
  currentRoute({
    id: "compute-pools.list",
    path: "/console/compute/pools",
    label: "计算资源池",
    area: "console",
    role: "admin"
  }),
  currentRoute({
    id: "compute-allocations.list",
    path: "/console/compute",
    label: "计算资源",
    area: "console",
    role: "lab_owner"
  }),
  currentRoute({
    id: "compute-allocations.create",
    path: "/console/compute/new",
    label: "开通计算资源",
    area: "console",
    role: "lab_owner"
  }),
  currentRoute({
    id: "compute-allocations.detail",
    path: "/console/compute/:id",
    label: "计算资源详情",
    area: "console",
    role: "lab_owner"
  }),
  currentRoute({
    id: "storage.list",
    path: "/console/storage",
    label: "存储资源",
    area: "console",
    role: "lab_owner"
  }),
  currentRoute({
    id: "storage.create",
    path: "/console/storage/new",
    label: "开通存储资源",
    area: "console",
    role: "lab_owner"
  }),
  currentRoute({
    id: "storage.detail",
    path: "/console/storage/:id",
    label: "存储资源详情",
    area: "console",
    role: "lab_owner"
  }),
  currentRoute({
    id: "attachment.list",
    path: "/console/attachments",
    label: "挂载关系",
    area: "console",
    role: "lab_owner"
  }),
  currentRoute({
    id: "attachment.create",
    path: "/console/attachments/new",
    label: "挂载存储",
    area: "console",
    role: "lab_owner"
  }),
  currentRoute({
    id: "attachment.detail",
    path: "/console/attachments/:id",
    label: "挂载详情",
    area: "console",
    role: "lab_owner"
  }),
  currentRoute({
    id: "resources.relationships",
    path: "/console/resources/relationships",
    label: "资源关系",
    area: "console",
    role: "lab_owner"
  }),
  currentRoute({
    id: "workspace.list",
    path: "/console/workspaces",
    label: "工作区",
    area: "console",
    role: "lab_owner",
    menu: true,
    featureFlag: "workspaces"
  }),
  currentRoute({
    id: "workspace.create",
    path: "/console/workspaces/new",
    label: "创建工作区入口",
    area: "console",
    role: "lab_owner"
  }),
  currentRoute({
    id: "workspace.detail",
    path: "/console/workspaces/:id",
    label: "工作区入口详情",
    area: "console",
    role: "lab_owner"
  }),
  currentRoute({
    id: "gateway.external",
    path: "/console/gateway",
    label: "网关",
    area: "console",
    role: "lab_owner"
  }),
  currentRoute({
    id: "billing.overview",
    path: "/console/billing",
    label: "账单",
    area: "console",
    role: "lab_owner"
  }),
  currentRoute({
    id: "account.overview",
    path: "/console/account",
    label: "账号",
    area: "console",
    role: "lab_owner",
    menu: true
  }),
  currentRoute({
    id: "support.list",
    path: "/console/support",
    label: "工单",
    area: "console",
    role: "lab_owner"
  }),
  currentRoute({
    id: "support.create",
    path: "/console/support/new",
    label: "登记外部工单",
    area: "console",
    role: "lab_owner"
  }),
  currentRoute({
    id: "support.detail",
    path: "/console/support/:id",
    label: "外部工单详情",
    area: "console",
    role: "lab_owner"
  }),
  currentRoute({
    id: "alerts.list",
    path: "/console/alerts",
    label: "提醒",
    area: "console",
    role: "lab_owner"
  }),
  currentRoute({
    id: "admin.root",
    path: "/admin",
    label: "管理",
    area: "admin",
    role: "admin",
    redirectRouteId: "admin.overview"
  }),
  currentRoute({
    id: "admin.overview",
    path: "/admin/overview",
    label: "管理概览",
    area: "admin",
    role: "admin",
    adminMenu: true
  }),
  currentRoute({
    id: "admin.users",
    path: "/admin/users",
    label: "用户",
    area: "admin",
    role: "admin",
    adminMenu: true
  }),
  currentRoute({
    id: "admin.billing",
    path: "/admin/billing",
    label: "账单运营",
    area: "admin",
    role: "admin",
    adminMenu: true
  }),
  currentRoute({
    id: "admin.ledger",
    path: "/admin/ledger",
    label: "账本",
    area: "admin",
    role: "admin",
    adminMenu: true,
    featureFlag: "ledgerAdmin"
  }),
  currentRoute({
    id: "admin.runtime",
    path: "/admin/runtime",
    label: "运行状态",
    area: "admin",
    role: "admin",
    adminMenu: true,
    featureFlag: "runtimeAdmin"
  }),
  currentRoute({
    id: "admin.diagnostics",
    path: "/admin/diagnostics",
    label: "线上诊断",
    area: "admin",
    role: "admin",
    adminMenu: true,
    featureFlag: "runtimeAdmin"
  }),
  currentRoute({
    id: "admin.e2e",
    path: "/admin/e2e",
    label: "E2E记录",
    area: "admin",
    role: "admin",
    adminMenu: true,
    featureFlag: "runtimeAdmin"
  }),
  currentRoute({
    id: "admin.cleanup",
    path: "/admin/cleanup",
    label: "入口清理",
    area: "admin",
    role: "admin",
    adminMenu: true,
    featureFlag: "runtimeAdmin"
  }),
  currentRoute({
    id: "admin.support",
    path: "/admin/support",
    label: "工单运营",
    area: "admin",
    role: "admin",
    adminMenu: true,
    featureFlag: "support"
  }),
  currentRoute({
    id: "error.forbidden",
    path: "/403",
    label: "无权限",
    area: "error",
    role: "public"
  }),
  currentRoute({
    id: "error.notFound",
    path: "/404",
    label: "未找到",
    area: "error",
    role: "public"
  }),
  currentRoute({
    id: "error.server",
    path: "/500",
    label: "错误",
    area: "error",
    role: "public"
  })
]);

export const routesById = new Map<string, AnyRecord>(oplRoutes.map((route) => [route.id, route]));

function escapeRegex(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function routePattern(path) {
  return new RegExp(`^${path.split("/").map((part) => part.startsWith(":") ? "[^/]+" : escapeRegex(part)).join("/")}$`);
}

export function normalizePath(pathname) {
  if (!pathname || pathname === "") return "/";
  return pathname.length > 1 ? pathname.replace(/\/+$/, "") : pathname;
}

export function routeTo(id, params: AnyRecord = {}, options: AnyRecord = {}) {
  const route = routesById.get(id);
  if (!route) throw new Error(`unknown route id: ${id}`);
  if (options.role === "lab_owner" && route.role === "admin") {
    throw new Error(`route ${id} not allowed for lab_owner`);
  }
  return route.path.replace(/:([^/]+)/g, (_, key) => {
    const value = params[key];
    if (value === undefined || value === null || value === "") {
      throw new Error(`missing route param: ${key}`);
    }
    return encodeURIComponent(String(value));
  });
}

export function findRoute(pathname) {
  const normalized = normalizePath(pathname);
  const route = oplRoutes.find((entry) => entry.path === normalized)
    || oplRoutes.find((entry) => entry.path.includes(":") && routePattern(entry.path).test(normalized));
  if (!route) return routesById.get("error.notFound");
  if (route.redirectRouteId) {
    return { ...route, redirect: routeTo(route.redirectRouteId, {}, { role: route.role }) };
  }
  return route;
}

export function menuRoutesFor(role, config = defaultLaunchConfig) {
  return oplRoutes.filter((route) => {
    if (role === "admin" && !route.adminMenu) return false;
    if (role !== "admin" && !route.menu) return false;
    if (route.featureFlag && !isFeatureEnabled(route.featureFlag, config)) return false;
    return true;
  });
}

export const ownerMenuRoutes = menuRoutesFor("lab_owner");
export const adminMenuRoutes = menuRoutesFor("admin");

export function navigate(path) {
  window.history.pushState({}, "", path);
  window.dispatchEvent(new PopStateEvent("popstate"));
}
