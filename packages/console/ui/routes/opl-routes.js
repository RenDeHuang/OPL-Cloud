import { defaultLaunchConfig, isFeatureEnabled } from "../config/launch-config.js";

const paidMutationProtocol = Object.freeze({
  mutation: true,
  confirmation: "normal",
  costImpact: "required",
  operationTimeline: true,
  failureVisible: true,
  visibleStages: ["已提交", "冻结余额", "云资源创建中", "Runtime 部署中", "存储挂载中", "URL 可用"]
});

const normalMutationProtocol = Object.freeze({
  mutation: true,
  confirmation: "normal",
  operationTimeline: true,
  failureVisible: true,
  visibleStages: ["已提交", "冻结余额", "云资源创建中", "Runtime 部署中", "存储挂载中", "URL 可用"]
});

const destructiveMutationProtocol = Object.freeze({
  mutation: true,
  confirmation: "normal",
  destructive: true,
  operationTimeline: true,
  failureVisible: true,
  visibleStages: ["已提交", "冻结余额", "云资源创建中", "Runtime 部署中", "存储挂载中", "URL 可用"]
});

const storageDestroyProtocol = Object.freeze({
  mutation: true,
  confirmation: "strong",
  destructive: true,
  dataLoss: true,
  confirmText: "确认删除数据",
  operationTimeline: true,
  failureVisible: true,
  visibleStages: ["已提交", "冻结余额", "云资源创建中", "Runtime 部署中", "存储挂载中", "URL 可用"]
});

const computeAllocationFields = Object.freeze([
  "id",
  "name",
  "packageId",
  "spec",
  "nodePoolId",
  "nodeName",
  "instanceId",
  "privateIp",
  "publicIp",
  "billingStatus",
  "workspaceId",
  "status",
  "operationId",
  "providerRequestId",
  "safeMessage"
]);

const storageVolumeFields = Object.freeze([
  "id",
  "name",
  "sizeGb",
  "storageClassId",
  "providerResourceId",
  "billingStatus",
  "status",
  "operationId",
  "providerRequestId",
  "safeMessage"
]);

const billingFields = Object.freeze([
  "availableBalance",
  "frozenBalance",
  "activeHourlyEstimate",
  "nextSettlementAt",
  "runningDuration"
]);

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
    label: "首页",
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
    label: "价格",
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
    label: "文档",
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
    label: "服务状态",
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
    label: "登录",
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
    label: "退出登录",
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
    label: "控制台",
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
    label: "概览",
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
    id: "compute-pools.list",
    path: "/console/compute/pools",
    label: "计算资源池",
    area: "console",
    role: "admin",
    ownerRepo: "opl-fabric",
    hiddenInMenu: true,
    featureFlag: "workspaces",
    routeKind: "read_model",
    objectKind: "ComputePool",
    pageModule: "packages/console/ui/pages/resources/ResourceProvisioningPages.jsx",
    apiClient: "packages/console/ui/api/resources-api.js",
    apiRoutes: ["GET /api/compute-pools"],
    serviceBoundary: "ComputePoolCatalogService",
    capabilities: ["list", "read", "evidence"]
  }),
  currentRoute({
    id: "compute-allocations.list",
    path: "/console/compute",
    label: "计算资源",
    area: "console",
    role: "lab_owner",
    menu: true,
    featureFlag: "workspaces",
    routeKind: "business_object",
    objectKind: "ComputeAllocation",
    pageModule: "packages/console/ui/pages/resources/ResourceProvisioningPages.jsx",
    apiClient: "packages/console/ui/api/resources-api.js",
    apiRoutes: ["GET /api/state", "GET /api/compute-allocations"],
    serviceBoundary: "ComputeAllocationService",
    dynamicFields: computeAllocationFields,
    capabilities: ["list", "read", "evidence"]
  }),
  currentRoute({
    id: "compute-allocations.create",
    path: "/console/compute/new",
    label: "开通计算资源",
    area: "console",
    role: "lab_owner",
    hiddenInMenu: true,
    featureFlag: "workspaces",
    routeKind: "business_object",
    objectKind: "ComputeAllocation",
    pageModule: "packages/console/ui/pages/resources/ResourceProvisioningPages.jsx",
    apiClient: "packages/console/ui/api/resources-api.js",
    apiRoutes: ["GET /api/compute-pools", "POST /api/compute-allocations"],
    serviceBoundary: "ComputeAllocationService",
    dynamicFields: computeAllocationFields,
    capabilities: ["read", "write", "action", "evidence"],
    operationProtocol: paidMutationProtocol
  }),
  currentRoute({
    id: "compute-allocations.detail",
    path: "/console/compute/:id",
    label: "计算资源详情",
    area: "console",
    role: "lab_owner",
    hiddenInMenu: true,
    featureFlag: "workspaces",
    routeKind: "business_object",
    objectKind: "ComputeAllocation",
    pageModule: "packages/console/ui/pages/resources/ResourceProvisioningPages.jsx",
    apiClient: "packages/console/ui/api/resources-api.js",
    apiRoutes: ["GET /api/compute-allocations/:id", "POST /api/compute-allocations/:id/destroy"],
    serviceBoundary: "ComputeAllocationService",
    dynamicFields: computeAllocationFields,
    capabilities: ["detail", "read", "action", "evidence"],
    operationProtocol: destructiveMutationProtocol
  }),
  currentRoute({
    id: "storage.list",
    path: "/console/storage",
    label: "存储资源",
    area: "console",
    role: "lab_owner",
    menu: true,
    featureFlag: "workspaces",
    routeKind: "business_object",
    objectKind: "StorageVolume",
    pageModule: "packages/console/ui/pages/resources/ResourceProvisioningPages.jsx",
    apiClient: "packages/console/ui/api/resources-api.js",
    apiRoutes: ["GET /api/state"],
    serviceBoundary: "ResourceProvisioningService",
    dynamicFields: storageVolumeFields,
    capabilities: ["list", "read", "action", "evidence"]
  }),
  currentRoute({
    id: "storage.create",
    path: "/console/storage/new",
    label: "开通存储资源",
    area: "console",
    role: "lab_owner",
    hiddenInMenu: true,
    featureFlag: "workspaces",
    routeKind: "business_object",
    objectKind: "StorageVolume",
    pageModule: "packages/console/ui/pages/resources/ResourceProvisioningPages.jsx",
    apiClient: "packages/console/ui/api/resources-api.js",
    apiRoutes: ["GET /api/state", "POST /api/storage-volumes"],
    serviceBoundary: "ResourceProvisioningService",
    dynamicFields: storageVolumeFields,
    capabilities: ["read", "write", "action", "evidence"],
    operationProtocol: paidMutationProtocol
  }),
  currentRoute({
    id: "storage.detail",
    path: "/console/storage/:id",
    label: "存储资源详情",
    area: "console",
    role: "lab_owner",
    hiddenInMenu: true,
    featureFlag: "workspaces",
    routeKind: "business_object",
    objectKind: "StorageVolume",
    pageModule: "packages/console/ui/pages/resources/ResourceProvisioningPages.jsx",
    apiClient: "packages/console/ui/api/resources-api.js",
    apiRoutes: ["GET /api/state", "POST /api/storage-volumes/destroy"],
    serviceBoundary: "ResourceProvisioningService",
    dynamicFields: storageVolumeFields,
    capabilities: ["detail", "read", "action", "evidence"],
    operationProtocol: storageDestroyProtocol
  }),
  currentRoute({
    id: "attachment.list",
    path: "/console/attachments",
    label: "挂载关系",
    area: "console",
    role: "lab_owner",
    menu: true,
    featureFlag: "workspaces",
    routeKind: "business_object",
    objectKind: "StorageAttachment",
    pageModule: "packages/console/ui/pages/resources/ResourceProvisioningPages.jsx",
    apiClient: "packages/console/ui/api/resources-api.js",
    apiRoutes: ["GET /api/state"],
    serviceBoundary: "ResourceProvisioningService",
    capabilities: ["list", "read", "action", "evidence"]
  }),
  currentRoute({
    id: "attachment.create",
    path: "/console/attachments/new",
    label: "挂载存储",
    area: "console",
    role: "lab_owner",
    hiddenInMenu: true,
    featureFlag: "workspaces",
    routeKind: "business_object",
    objectKind: "StorageAttachment",
    pageModule: "packages/console/ui/pages/resources/ResourceProvisioningPages.jsx",
    apiClient: "packages/console/ui/api/resources-api.js",
    apiRoutes: ["GET /api/state", "POST /api/storage-attachments"],
    serviceBoundary: "ResourceProvisioningService",
    capabilities: ["read", "write", "action", "evidence"],
    operationProtocol: normalMutationProtocol
  }),
  currentRoute({
    id: "attachment.detail",
    path: "/console/attachments/:id",
    label: "挂载详情",
    area: "console",
    role: "lab_owner",
    hiddenInMenu: true,
    featureFlag: "workspaces",
    routeKind: "business_object",
    objectKind: "StorageAttachment",
    pageModule: "packages/console/ui/pages/resources/ResourceProvisioningPages.jsx",
    apiClient: "packages/console/ui/api/resources-api.js",
    apiRoutes: ["GET /api/state", "POST /api/storage-attachments/detach"],
    serviceBoundary: "ResourceProvisioningService",
    capabilities: ["detail", "read", "action", "evidence"],
    operationProtocol: destructiveMutationProtocol
  }),
  currentRoute({
    id: "resources.relationships",
    path: "/console/resources/relationships",
    label: "资源关系",
    area: "console",
    role: "lab_owner",
    menu: true,
    featureFlag: "workspaces",
    routeKind: "read_model",
    objectKind: "ResourceRelationship",
    pageModule: "packages/console/ui/pages/resources/ResourceProvisioningPages.jsx",
    apiClient: "packages/console/ui/api/console-read-api.js",
    apiRoutes: ["GET /api/state"],
    serviceBoundary: "ConsoleReadModelService",
    capabilities: ["read", "summary", "evidence"]
  }),
  currentRoute({
    id: "workspace.list",
    path: "/console/workspaces",
    label: "工作区入口",
    area: "console",
    role: "lab_owner",
    menu: true,
    featureFlag: "workspaces",
    routeKind: "business_object",
    objectKind: "Workspace",
    pageModule: "packages/console/ui/pages/workspaces/WorkspacesPage.jsx",
    apiClient: "packages/console/ui/api/workspaces-api.js",
    apiRoutes: ["GET /api/state", "POST /api/workspaces/reset-token", "POST /api/workspaces/delete-token"],
    serviceBoundary: "WorkspaceEntryService",
    capabilities: ["list", "read", "action"]
  }),
  currentRoute({
    id: "workspace.create",
    path: "/console/workspaces/new",
    label: "创建工作区入口",
    area: "console",
    role: "lab_owner",
    hiddenInMenu: true,
    featureFlag: "workspaces",
    routeKind: "business_object",
    objectKind: "Workspace",
    pageModule: "packages/console/ui/pages/workspaces/CreateWorkspacePage.jsx",
    apiClient: "packages/console/ui/api/workspaces-api.js",
    apiRoutes: ["GET /api/state", "POST /api/workspaces"],
    serviceBoundary: "WorkspaceEntryService",
    capabilities: ["read", "write"],
    operationProtocol: normalMutationProtocol
  }),
  currentRoute({
    id: "workspace.detail",
    path: "/console/workspaces/:id",
    label: "工作区入口详情",
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
      "POST /api/workspaces/reset-token",
      "POST /api/workspaces/delete-token",
      "POST /api/workspaces/runtime-status"
    ],
    serviceBoundary: "WorkspaceEntryService",
    capabilities: ["detail", "read", "action", "evidence"],
    operationProtocol: destructiveMutationProtocol
  }),
  currentRoute({
    id: "gateway.external",
    path: "/console/gateway",
    label: "网关",
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
    label: "账单",
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
    dynamicFields: billingFields,
    capabilities: ["read", "list", "detail"]
  }),
  currentRoute({
    id: "billing.wallet",
    path: "/console/billing/wallet",
    label: "钱包与冻结",
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
    dynamicFields: billingFields,
    capabilities: ["read", "detail"]
  }),
  currentRoute({
    id: "account.overview",
    path: "/console/account",
    label: "账号与实验室",
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
    label: "工单",
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
    label: "新建工单",
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
    label: "工单详情",
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
    label: "提醒",
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
    label: "管理",
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
    label: "管理概览",
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
    label: "用户",
    area: "admin",
    role: "admin",
    adminMenu: true,
    routeKind: "read_model",
    objectKind: "User",
    pageModule: "packages/console/ui/pages/admin/AdminOverviewPage.jsx",
    apiClient: "packages/console/ui/api/console-read-api.js",
    apiRoutes: ["GET /api/management/state", "POST /api/users", "POST /api/users/disable", "POST /api/users/delete", "POST /api/billing/topups"],
    serviceBoundary: "ManagementModel",
    capabilities: ["list", "read", "action", "audit"]
  }),
  currentRoute({
    id: "admin.billing",
    path: "/admin/billing",
    label: "账单运营",
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
    label: "账本",
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
    label: "运行状态",
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
    id: "admin.diagnostics",
    path: "/admin/diagnostics",
    label: "线上诊断",
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
    capabilities: ["read", "detail", "audit", "evidence"]
  }),
  currentRoute({
    id: "admin.e2e",
    path: "/admin/e2e",
    label: "E2E记录",
    area: "admin",
    role: "admin",
    adminMenu: true,
    featureFlag: "runtimeAdmin",
    routeKind: "read_model",
    objectKind: "ProductionVerification",
    pageModule: "packages/console/ui/pages/admin/AdminOverviewPage.jsx",
    apiClient: "packages/console/ui/api/console-read-api.js",
    apiRoutes: ["GET /api/operator/summary"],
    serviceBoundary: "RuntimeOperationService",
    capabilities: ["read", "list", "audit", "evidence"]
  }),
  currentRoute({
    id: "admin.cleanup",
    path: "/admin/cleanup",
    label: "入口清理",
    area: "admin",
    role: "admin",
    adminMenu: true,
    featureFlag: "runtimeAdmin",
    routeKind: "read_model",
    objectKind: "RuntimeReadiness",
    pageModule: "packages/console/ui/pages/admin/AdminOverviewPage.jsx",
    apiClient: "packages/console/ui/api/console-read-api.js",
    apiRoutes: ["GET /api/management/state", "GET /api/operator/summary", "POST /api/operator/cleanup-workspace-access"],
    serviceBoundary: "WorkspaceEntryService",
    capabilities: ["read", "list", "action", "audit"],
    operationProtocol: destructiveMutationProtocol
  }),
  currentRoute({
    id: "admin.support",
    path: "/admin/support",
    label: "工单运营",
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
    label: "无权限",
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
    label: "未找到",
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
    label: "错误",
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
