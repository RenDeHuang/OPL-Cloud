import { defaultLaunchConfig, isFeatureEnabled } from "../config/launch-config.js";

function currentRoute(route) {
  return Object.freeze({
    status: "implemented",
    contractLifecycle: "current",
    ownerRepo: "opl-console",
    capabilities: ["read"],
    ...route,
    requiresAdmin: route.role === "admin",
    requiresAuth: route.role === "lab_owner" || route.role === "admin"
  });
}

export const oplRoutes = Object.freeze([
  currentRoute({
    id: "public.home",
    path: "/",
    label: "Public Home",
    area: "public",
    role: "public",
    requiresAuth: false,
    routeKind: "static_content",
    pageModule: "packages/console/ui/pages/HomePage.jsx",
    serviceBoundary: "StaticPublicContent"
  }),
  currentRoute({
    id: "public.pricing",
    path: "/pricing",
    label: "Pricing",
    area: "public",
    role: "public",
    status: "folded_into_parent",
    contractLifecycle: "folded_parent",
    routeKind: "static_content",
    pageModule: "packages/console/ui/pages/HomePage.jsx",
    serviceBoundary: "StaticPublicContent"
  }),
  currentRoute({
    id: "public.docs",
    path: "/docs",
    label: "Docs",
    area: "public",
    role: "public",
    status: "folded_into_parent",
    contractLifecycle: "folded_parent",
    routeKind: "static_content",
    pageModule: "packages/console/ui/pages/HomePage.jsx",
    serviceBoundary: "StaticPublicContent"
  }),
  currentRoute({
    id: "public.status",
    path: "/status",
    label: "Service Status",
    area: "public",
    role: "public",
    status: "folded_into_parent",
    contractLifecycle: "folded_parent",
    routeKind: "static_content",
    pageModule: "packages/console/ui/pages/HomePage.jsx",
    serviceBoundary: "StaticPublicContent"
  }),
  currentRoute({
    id: "auth.login",
    path: "/login",
    label: "Login",
    area: "auth",
    role: "public",
    requiresAuth: false,
    routeKind: "auth_flow",
    pageModule: "packages/console/ui/pages/LoginPage.jsx",
    apiClient: "packages/console/ui/api/auth-api.js",
    apiRoutes: ["POST /api/auth/login"],
    serviceBoundary: "AuthController",
    capabilities: ["read", "authenticate", "session"]
  }),
  currentRoute({
    id: "auth.logout",
    path: "/logout",
    label: "Logout",
    area: "auth",
    role: "lab_owner",
    hiddenInMenu: true,
    routeKind: "auth_flow",
    pageModule: "packages/console/ui/pages/LoginPage.jsx",
    apiClient: "packages/console/ui/api/auth-api.js",
    apiRoutes: ["POST /api/auth/logout"],
    serviceBoundary: "AuthController",
    capabilities: ["read", "session"]
  }),
  currentRoute({
    id: "console.root",
    path: "/console",
    label: "Console",
    area: "console",
    role: "lab_owner",
    redirectRouteId: "console.overview",
    hiddenInMenu: true,
    status: "folded_into_parent",
    contractLifecycle: "folded_parent",
    routeKind: "read_model",
    serviceBoundary: "ConsoleRouter"
  }),
  currentRoute({
    id: "console.overview",
    path: "/console/overview",
    label: "Overview",
    area: "console",
    role: "lab_owner",
    menu: true,
    routeKind: "read_model",
    pageModule: "packages/console/ui/pages/OverviewPage.jsx",
    apiClient: "packages/console/ui/api/console-read-api.js",
    apiRoutes: ["GET /api/state", "GET /api/support/tickets"],
    serviceBoundary: "ConsoleReadModelService",
    capabilities: ["read", "summary"]
  }),
  currentRoute({
    id: "workspace.list",
    path: "/console/workspaces",
    label: "Workspaces",
    area: "console",
    role: "lab_owner",
    menu: true,
    featureFlag: "workspaces",
    routeKind: "business_object",
    objectKind: "Workspace",
    pageModule: "packages/console/ui/pages/workspaces/WorkspacesPage.jsx",
    apiClient: "packages/console/ui/api/workspaces-api.js",
    apiRoutes: ["GET /api/state", "POST /api/workspaces/reset-token", "POST /api/workspaces/delete-token"],
    serviceBoundary: "WorkspaceLifecycleService",
    capabilities: ["list", "read", "action"]
  }),
  currentRoute({
    id: "workspace.create",
    path: "/console/workspaces/new",
    label: "Create Workspace",
    area: "console",
    role: "lab_owner",
    hiddenInMenu: true,
    featureFlag: "workspaces",
    routeKind: "business_object",
    objectKind: "Workspace",
    pageModule: "packages/console/ui/pages/workspaces/CreateWorkspacePage.jsx",
    apiClient: "packages/console/ui/api/workspaces-api.js",
    apiRoutes: ["GET /api/state", "POST /api/workspaces"],
    serviceBoundary: "WorkspaceLifecycleService",
    capabilities: ["read", "write"]
  }),
  currentRoute({
    id: "workspace.detail",
    path: "/console/workspaces/:id",
    label: "Workspace Detail",
    area: "console",
    role: "lab_owner",
    hiddenInMenu: true,
    featureFlag: "workspaces",
    routeKind: "business_object",
    objectKind: "Workspace",
    pageModule: "packages/console/ui/pages/workspaces/WorkspaceDetailPage.jsx",
    apiClient: "packages/console/ui/api/workspaces-api.js",
    apiRoutes: [
      "GET /api/state",
      "POST /api/workspaces/stop-server",
      "POST /api/workspaces/restart-server",
      "POST /api/workspaces/destroy-server",
      "POST /api/workspaces/destroy-disk",
      "POST /api/workspaces/runtime-status"
    ],
    serviceBoundary: "WorkspaceLifecycleService",
    capabilities: ["detail", "read", "action", "evidence"]
  }),
  currentRoute({
    id: "gateway.external",
    path: "/console/gateway",
    label: "Gateway",
    area: "console",
    role: "lab_owner",
    menu: true,
    featureFlag: "gatewayExternalLink",
    status: "external",
    routeKind: "external_integration",
    objectKind: "GatewayIntegration",
    externalUrl: defaultLaunchConfig.gatewayExternalUrl,
    pageModule: "packages/console/ui/pages/gateway/GatewayPage.jsx",
    serviceBoundary: "OPLGatewayExternalIntegration",
    capabilities: ["read", "external_link"]
  }),
  currentRoute({
    id: "billing.overview",
    path: "/console/billing",
    label: "Billing",
    area: "console",
    role: "lab_owner",
    menu: true,
    featureFlag: "billing",
    routeKind: "read_model",
    objectKind: "Wallet",
    pageModule: "packages/console/ui/pages/billing/BillingPage.jsx",
    apiClient: "packages/console/ui/api/billing-api.js",
    apiRoutes: ["GET /api/state"],
    serviceBoundary: "WalletService",
    capabilities: ["read", "list", "detail"]
  }),
  currentRoute({
    id: "billing.wallet",
    path: "/console/billing/wallet",
    label: "Wallet and Holds",
    area: "console",
    role: "lab_owner",
    hiddenInMenu: true,
    featureFlag: "billing",
    status: "folded_into_parent",
    contractLifecycle: "folded_parent",
    routeKind: "read_model",
    objectKind: "Wallet",
    pageModule: "packages/console/ui/pages/billing/BillingPage.jsx",
    apiClient: "packages/console/ui/api/billing-api.js",
    apiRoutes: ["GET /api/state"],
    serviceBoundary: "WalletService",
    capabilities: ["read", "detail"]
  }),
  currentRoute({
    id: "account.overview",
    path: "/console/account",
    label: "Account & Lab",
    area: "console",
    role: "lab_owner",
    menu: true,
    routeKind: "read_model",
    objectKind: "User",
    pageModule: "packages/console/ui/pages/account/AccountPage.jsx",
    apiClient: "packages/console/ui/api/console-read-api.js",
    apiRoutes: ["GET /api/state", "GET /api/auth/me"],
    serviceBoundary: "ManagementModel",
    capabilities: ["read", "detail"]
  }),
  currentRoute({
    id: "support.list",
    path: "/console/support",
    label: "Support",
    area: "console",
    role: "lab_owner",
    menu: true,
    featureFlag: "support",
    routeKind: "business_object",
    objectKind: "SupportTicket",
    pageModule: "packages/console/ui/pages/support/SupportPage.jsx",
    apiClient: "packages/console/ui/api/support-api.js",
    apiRoutes: ["GET /api/support/tickets"],
    serviceBoundary: "SupportTicketService",
    capabilities: ["list", "read", "audit"]
  }),
  currentRoute({
    id: "support.create",
    path: "/console/support/new",
    label: "New Ticket",
    area: "console",
    role: "lab_owner",
    hiddenInMenu: true,
    featureFlag: "support",
    routeKind: "business_object",
    objectKind: "SupportTicket",
    pageModule: "packages/console/ui/pages/support/SupportPage.jsx",
    apiClient: "packages/console/ui/api/support-api.js",
    apiRoutes: ["GET /api/support/tickets", "POST /api/support/tickets"],
    serviceBoundary: "SupportTicketService",
    capabilities: ["read", "write", "audit"]
  }),
  currentRoute({
    id: "support.detail",
    path: "/console/support/:id",
    label: "Ticket Detail",
    area: "console",
    role: "lab_owner",
    hiddenInMenu: true,
    featureFlag: "support",
    routeKind: "business_object",
    objectKind: "SupportTicket",
    pageModule: "packages/console/ui/pages/support/SupportPage.jsx",
    apiClient: "packages/console/ui/api/support-api.js",
    apiRoutes: ["GET /api/support/tickets"],
    serviceBoundary: "SupportTicketService",
    capabilities: ["detail", "read", "audit"]
  }),
  currentRoute({
    id: "alerts.list",
    path: "/console/alerts",
    label: "Alerts",
    area: "console",
    role: "lab_owner",
    menu: true,
    featureFlag: "alerts",
    routeKind: "read_model",
    objectKind: "RuntimeReadiness",
    pageModule: "packages/console/ui/pages/catalog/FabricPages.jsx",
    apiClient: "packages/console/ui/api/console-read-api.js",
    apiRoutes: ["GET /api/state", "GET /api/support/tickets"],
    serviceBoundary: "ConsoleReadModelService",
    capabilities: ["read", "list"]
  }),
  currentRoute({
    id: "admin.root",
    path: "/admin",
    label: "Admin",
    area: "admin",
    role: "admin",
    redirectRouteId: "admin.overview",
    hiddenInMenu: true,
    status: "folded_into_parent",
    contractLifecycle: "folded_parent",
    routeKind: "read_model",
    serviceBoundary: "ConsoleRouter"
  }),
  currentRoute({
    id: "admin.overview",
    path: "/admin/overview",
    label: "Admin Overview",
    area: "admin",
    role: "admin",
    adminMenu: true,
    routeKind: "read_model",
    pageModule: "packages/console/ui/pages/admin/AdminOverviewPage.jsx",
    apiClient: "packages/console/ui/api/console-read-api.js",
    apiRoutes: ["GET /api/state", "GET /api/operator/summary"],
    serviceBoundary: "ConsoleReadModelService",
    capabilities: ["read", "summary"]
  }),
  currentRoute({
    id: "admin.users",
    path: "/admin/users",
    label: "Users",
    area: "admin",
    role: "admin",
    adminMenu: true,
    routeKind: "read_model",
    objectKind: "User",
    pageModule: "packages/console/ui/pages/admin/AdminOverviewPage.jsx",
    apiClient: "packages/console/ui/api/console-read-api.js",
    apiRoutes: ["GET /api/management/state", "POST /api/billing/topups"],
    serviceBoundary: "ManagementModel",
    capabilities: ["list", "read", "action", "audit"]
  }),
  currentRoute({
    id: "admin.billing",
    path: "/admin/billing",
    label: "Billing Ops",
    area: "admin",
    role: "admin",
    adminMenu: true,
    featureFlag: "manualTopup",
    routeKind: "read_model",
    objectKind: "Wallet",
    pageModule: "packages/console/ui/pages/admin/AdminOverviewPage.jsx",
    apiClient: "packages/console/ui/api/billing-api.js",
    apiRoutes: ["GET /api/state", "POST /api/billing/topups"],
    serviceBoundary: "WalletService",
    capabilities: ["read", "list", "action", "audit"]
  }),
  currentRoute({
    id: "admin.ledger",
    path: "/admin/ledger",
    label: "Ledger",
    area: "admin",
    role: "admin",
    ownerRepo: "opl-ledger",
    adminMenu: true,
    featureFlag: "ledgerAdmin",
    routeKind: "read_model",
    objectKind: "Usage",
    pageModule: "packages/console/ui/pages/admin/AdminOverviewPage.jsx",
    apiClient: "packages/console/ui/api/ledger-api.js",
    apiRoutes: ["GET /api/state", "GET /api/ledger/task-receipts"],
    serviceBoundary: "LedgerEvidenceService",
    capabilities: ["read", "list", "evidence", "audit"]
  }),
  currentRoute({
    id: "admin.runtime",
    path: "/admin/runtime",
    label: "Runtime",
    area: "admin",
    role: "admin",
    adminMenu: true,
    featureFlag: "runtimeAdmin",
    routeKind: "read_model",
    objectKind: "RuntimeReadiness",
    pageModule: "packages/console/ui/pages/admin/AdminOverviewPage.jsx",
    apiClient: "packages/console/ui/api/console-read-api.js",
    apiRoutes: ["GET /api/runtime/readiness", "GET /api/production/readiness", "GET /api/operator/summary"],
    serviceBoundary: "RuntimeOperationService",
    capabilities: ["read", "detail", "audit"]
  }),
  currentRoute({
    id: "admin.support",
    path: "/admin/support",
    label: "Support Ops",
    area: "admin",
    role: "admin",
    adminMenu: true,
    featureFlag: "support",
    routeKind: "business_object",
    objectKind: "SupportTicket",
    pageModule: "packages/console/ui/pages/admin/AdminOverviewPage.jsx",
    apiClient: "packages/console/ui/api/support-api.js",
    apiRoutes: ["GET /api/support/tickets"],
    serviceBoundary: "SupportTicketService",
    capabilities: ["list", "read", "audit"]
  }),
  currentRoute({
    id: "error.forbidden",
    path: "/403",
    label: "Forbidden",
    area: "error",
    role: "public",
    hiddenInMenu: true,
    routeKind: "static_content",
    pageModule: "packages/console/ui/pages/shared/page-widgets.jsx",
    serviceBoundary: "ConsoleRouter"
  }),
  currentRoute({
    id: "error.notFound",
    path: "/404",
    label: "Not Found",
    area: "error",
    role: "public",
    hiddenInMenu: true,
    routeKind: "static_content",
    pageModule: "packages/console/ui/pages/shared/page-widgets.jsx",
    serviceBoundary: "ConsoleRouter"
  }),
  currentRoute({
    id: "error.server",
    path: "/500",
    label: "Error",
    area: "error",
    role: "public",
    hiddenInMenu: true,
    routeKind: "static_content",
    pageModule: "packages/console/ui/pages/shared/page-widgets.jsx",
    serviceBoundary: "ConsoleRouter"
  })
]);

export const routesById = new Map(oplRoutes.map((route) => [route.id, route]));

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

export function routeTo(id, params = {}, options = {}) {
  const route = routesById.get(id);
  if (!route) throw new Error(`unknown route id: ${id}`);
  const actorRole = options.role;
  if (actorRole === "lab_owner" && route.role === "admin") {
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
