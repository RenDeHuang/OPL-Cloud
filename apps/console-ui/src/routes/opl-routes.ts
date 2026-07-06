// @ts-nocheck
// ponytail: migrated JS first; replace with domain DTO types when these pages change behavior.
import { defaultLaunchConfig, isFeatureEnabled } from "../config/launch-config.ts";

const computeAllocationStages = Object.freeze(["已提交", "冻结余额", "云资源创建中", "Runtime 部署中", "存储挂载中", "URL 可用"]);
const storageCreateStages = Object.freeze(["已提交", "冻结余额", "存储创建中", "可挂载"]);
const storageDestroyStages = Object.freeze(["已提交", "释放冻结", "销毁存储", "已删除"]);
const attachmentCreateStages = Object.freeze(["已提交", "挂载中", "可创建入口"]);
const attachmentDetachStages = Object.freeze(["已提交", "解除挂载", "存储保留"]);
const workspaceEntryStages = Object.freeze(["已提交", "生成 URL", "URL 可用"]);

const computeCreateProtocol = Object.freeze({
  mutation: true,
  asyncProvisioning: true,
  initialStatus: "provisioning",
  readyStatus: "running",
  pollRoute: "GET /api/compute-allocations/:id",
  pollQuery: ["accountId"],
  confirmation: "normal",
  costImpact: "required",
  operationTimeline: true,
  failureVisible: true,
  visibleStages: computeAllocationStages
});

const normalMutationProtocol = Object.freeze({
  mutation: true,
  confirmation: "normal",
  operationTimeline: true,
  failureVisible: true
});

const computeDestroyProtocol = Object.freeze({
  mutation: true,
  asyncProvisioning: true,
  pollRoute: "GET /api/compute-allocations/:id",
  pollQuery: ["accountId"],
  confirmation: "normal",
  destructive: true,
  operationTimeline: true,
  failureVisible: true,
  visibleStages: computeAllocationStages
});

const storageCreateProtocol = Object.freeze({
  mutation: true,
  confirmation: "normal",
  costImpact: "required",
  operationTimeline: true,
  failureVisible: true,
  visibleStages: storageCreateStages
});

const storageDestroyProtocol = Object.freeze({
  mutation: true,
  confirmation: "strong",
  destructive: true,
  dataLoss: true,
  confirmText: "确认删除数据",
  operationTimeline: true,
  failureVisible: true,
  visibleStages: storageDestroyStages
});

const attachmentCreateProtocol = Object.freeze({
  mutation: true,
  confirmation: "normal",
  operationTimeline: true,
  failureVisible: true,
  visibleStages: attachmentCreateStages
});

const attachmentDetachProtocol = Object.freeze({
  mutation: true,
  confirmation: "normal",
  destructive: true,
  operationTimeline: true,
  failureVisible: true,
  visibleStages: attachmentDetachStages
});

const workspaceCreateProtocol = Object.freeze({
  mutation: true,
  confirmation: "normal",
  operationTimeline: true,
  failureVisible: true,
  visibleStages: workspaceEntryStages
});

const destructiveMutationProtocol = Object.freeze({
  mutation: true,
  confirmation: "normal",
  destructive: true,
  operationTimeline: true,
  failureVisible: true
});

const computeAllocationFields = Object.freeze([
  "id",
  "ownerAccountId",
  "name",
  "packageId",
  "spec",
  "nodePoolId",
  "cvmInstanceId",
  "machineName",
  "nodeName",
  "privateIp",
  "publicIp",
  "billingStatus",
  "workspaceId",
  "status",
  "operationId",
  "providerRequestId",
  "safeMessage"
]);

const workspaceFields = Object.freeze([
  "id",
  "ownerAccountId",
  "storageId",
  "currentComputeAllocationId",
  "currentAttachmentId",
  "url",
  "access.tokenStatus",
  "runtime.status",
  "state"
]);

const storageVolumeFields = Object.freeze([
  "id",
  "ownerAccountId",
  "name",
  "sizeGb",
  "storageClassId",
  "providerResourceId",
  "hourlyEstimate",
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
    pageModule: "apps/console-ui/src/pages/HomePage.tsx",
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
    pageModule: "apps/console-ui/src/pages/HomePage.tsx",
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
    pageModule: "apps/console-ui/src/pages/HomePage.tsx",
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
    pageModule: "apps/console-ui/src/pages/HomePage.tsx",
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
    pageModule: "apps/console-ui/src/pages/LoginPage.tsx",
    apiClient: "apps/console-ui/src/api/auth-api.ts",
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
    pageModule: "apps/console-ui/src/pages/LoginPage.tsx",
    apiClient: "apps/console-ui/src/api/auth-api.ts",
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
    pageModule: "apps/console-ui/src/pages/OverviewPage.tsx",
    apiClient: "apps/console-ui/src/api/console-read-api.ts",
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
    pageModule: "apps/console-ui/src/pages/resources/ResourceProvisioningPages.tsx",
    apiClient: "apps/console-ui/src/api/resources-api.ts",
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
    hiddenInMenu: true,
    featureFlag: "workspaces",
    routeKind: "business_object",
    objectKind: "ComputeAllocation",
    pageModule: "apps/console-ui/src/pages/resources/ResourceProvisioningPages.tsx",
    apiClient: "apps/console-ui/src/api/resources-api.ts",
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
    pageModule: "apps/console-ui/src/pages/resources/ResourceProvisioningPages.tsx",
    apiClient: "apps/console-ui/src/api/resources-api.ts",
    apiRoutes: ["GET /api/compute-pools", "POST /api/compute-allocations"],
    serviceBoundary: "ComputeAllocationService",
    dynamicFields: computeAllocationFields,
    capabilities: ["read", "write", "action", "evidence"],
    operationProtocol: computeCreateProtocol
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
    pageModule: "apps/console-ui/src/pages/resources/ResourceProvisioningPages.tsx",
    apiClient: "apps/console-ui/src/api/resources-api.ts",
    apiRoutes: ["GET /api/compute-allocations/:id", "POST /api/compute-allocations/:id/destroy"],
    serviceBoundary: "ComputeAllocationService",
    dynamicFields: computeAllocationFields,
    capabilities: ["detail", "read", "action", "evidence"],
    operationProtocol: computeDestroyProtocol
  }),
  currentRoute({
    id: "storage.list",
    path: "/console/storage",
    label: "存储资源",
    area: "console",
    role: "lab_owner",
    hiddenInMenu: true,
    featureFlag: "workspaces",
    routeKind: "business_object",
    objectKind: "StorageVolume",
    pageModule: "apps/console-ui/src/pages/resources/ResourceProvisioningPages.tsx",
    apiClient: "apps/console-ui/src/api/resources-api.ts",
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
    pageModule: "apps/console-ui/src/pages/resources/ResourceProvisioningPages.tsx",
    apiClient: "apps/console-ui/src/api/resources-api.ts",
    apiRoutes: ["GET /api/state", "POST /api/storage-volumes"],
    serviceBoundary: "ResourceProvisioningService",
    dynamicFields: storageVolumeFields,
    capabilities: ["read", "write", "action", "evidence"],
    operationProtocol: storageCreateProtocol
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
    pageModule: "apps/console-ui/src/pages/resources/ResourceProvisioningPages.tsx",
    apiClient: "apps/console-ui/src/api/resources-api.ts",
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
    hiddenInMenu: true,
    featureFlag: "workspaces",
    routeKind: "business_object",
    objectKind: "StorageAttachment",
    pageModule: "apps/console-ui/src/pages/resources/ResourceProvisioningPages.tsx",
    apiClient: "apps/console-ui/src/api/resources-api.ts",
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
    pageModule: "apps/console-ui/src/pages/resources/ResourceProvisioningPages.tsx",
    apiClient: "apps/console-ui/src/api/resources-api.ts",
    apiRoutes: ["GET /api/state", "POST /api/storage-attachments"],
    serviceBoundary: "ResourceProvisioningService",
    capabilities: ["read", "write", "action", "evidence"],
    operationProtocol: attachmentCreateProtocol
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
    pageModule: "apps/console-ui/src/pages/resources/ResourceProvisioningPages.tsx",
    apiClient: "apps/console-ui/src/api/resources-api.ts",
    apiRoutes: ["GET /api/state", "POST /api/storage-attachments/detach"],
    serviceBoundary: "ResourceProvisioningService",
    capabilities: ["detail", "read", "action", "evidence"],
    operationProtocol: attachmentDetachProtocol
  }),
  currentRoute({
    id: "resources.relationships",
    path: "/console/resources/relationships",
    label: "资源关系",
    area: "console",
    role: "lab_owner",
    hiddenInMenu: true,
    featureFlag: "workspaces",
    routeKind: "read_model",
    objectKind: "ResourceRelationship",
    pageModule: "apps/console-ui/src/pages/resources/ResourceProvisioningPages.tsx",
    apiClient: "apps/console-ui/src/api/console-read-api.ts",
    apiRoutes: ["GET /api/state"],
    serviceBoundary: "ConsoleReadModelService",
    capabilities: ["read", "summary", "evidence"]
  }),
  currentRoute({
    id: "workspace.list",
    path: "/console/workspaces",
    label: "工作区",
    area: "console",
    role: "lab_owner",
    menu: true,
    featureFlag: "workspaces",
    routeKind: "business_object",
    objectKind: "Workspace",
    pageModule: "apps/console-ui/src/pages/workspaces/WorkspacesPage.tsx",
    apiClient: "apps/console-ui/src/api/workspaces-api.ts",
    apiRoutes: ["GET /api/state", "POST /api/workspaces/reset-token", "POST /api/workspaces/delete-token"],
    serviceBoundary: "WorkspaceLifecycleService",
    dynamicFields: workspaceFields,
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
    pageModule: "apps/console-ui/src/pages/workspaces/CreateWorkspacePage.tsx",
    apiClient: "apps/console-ui/src/api/workspaces-api.ts",
    apiRoutes: ["GET /api/state", "POST /api/workspaces"],
    serviceBoundary: "WorkspaceLifecycleService",
    dynamicFields: ["workspaceName", "attachmentId", "storageId", "currentComputeAllocationId", "currentAttachmentId", "url", "runtime.status"],
    capabilities: ["read", "write"],
    operationProtocol: workspaceCreateProtocol
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
    pageModule: "apps/console-ui/src/pages/workspaces/WorkspaceDetailPage.tsx",
    apiClient: "apps/console-ui/src/api/workspaces-api.ts",
    apiRoutes: [
      "GET /api/state",
      "POST /api/workspaces/reset-token",
      "POST /api/workspaces/delete-token",
      "POST /api/workspaces/runtime-status"
    ],
    serviceBoundary: "WorkspaceLifecycleService",
    dynamicFields: workspaceFields,
    capabilities: ["detail", "read", "action", "evidence"],
    operationProtocol: destructiveMutationProtocol
  }),
  currentRoute({
    id: "gateway.external",
    path: "/console/gateway",
    label: "网关",
    area: "console",
    role: "lab_owner",
    hiddenInMenu: true,
    featureFlag: "gatewayExternalLink",
    status: "external",
    routeKind: "external_integration",
    objectKind: "GatewayIntegration",
    externalUrl: defaultLaunchConfig.gatewayExternalUrl,
    pageModule: "apps/console-ui/src/pages/gateway/GatewayPage.tsx",
    serviceBoundary: "OPLGatewayExternalIntegration",
    capabilities: ["read", "external_link"]
  }),
  currentRoute({
    id: "billing.overview",
    path: "/console/billing",
    label: "账单",
    area: "console",
    role: "lab_owner",
    hiddenInMenu: true,
    featureFlag: "billing",
    routeKind: "read_model",
    objectKind: "Wallet",
    pageModule: "apps/console-ui/src/pages/billing/BillingPage.tsx",
    apiClient: "apps/console-ui/src/api/billing-api.ts",
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
    pageModule: "apps/console-ui/src/pages/billing/BillingPage.tsx",
    apiClient: "apps/console-ui/src/api/billing-api.ts",
    apiRoutes: ["GET /api/state"],
    serviceBoundary: "WalletService",
    dynamicFields: billingFields,
    capabilities: ["read", "detail"]
  }),
  currentRoute({
    id: "account.overview",
    path: "/console/account",
    label: "配置",
    area: "console",
    role: "lab_owner",
    menu: true,
    routeKind: "read_model",
    objectKind: "User",
    pageModule: "apps/console-ui/src/pages/account/AccountPage.tsx",
    apiClient: "apps/console-ui/src/api/console-read-api.ts",
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
    hiddenInMenu: true,
    featureFlag: "support",
    routeKind: "business_object",
    objectKind: "SupportTicket",
    pageModule: "apps/console-ui/src/pages/support/SupportPage.tsx",
    apiClient: "apps/console-ui/src/api/support-api.ts",
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
    pageModule: "apps/console-ui/src/pages/support/SupportPage.tsx",
    apiClient: "apps/console-ui/src/api/support-api.ts",
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
    pageModule: "apps/console-ui/src/pages/support/SupportPage.tsx",
    apiClient: "apps/console-ui/src/api/support-api.ts",
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
    hiddenInMenu: true,
    featureFlag: "alerts",
    routeKind: "read_model",
    objectKind: "RuntimeReadiness",
    pageModule: "apps/console-ui/src/pages/catalog/FabricPages.tsx",
    apiClient: "apps/console-ui/src/api/console-read-api.ts",
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
    pageModule: "apps/console-ui/src/pages/admin/AdminOverviewPage.tsx",
    apiClient: "apps/console-ui/src/api/console-read-api.ts",
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
    pageModule: "apps/console-ui/src/pages/admin/AdminOverviewPage.tsx",
    apiClient: "apps/console-ui/src/api/console-read-api.ts",
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
    pageModule: "apps/console-ui/src/pages/admin/AdminOverviewPage.tsx",
    apiClient: "apps/console-ui/src/api/billing-api.ts",
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
    pageModule: "apps/console-ui/src/pages/admin/AdminOverviewPage.tsx",
    apiClient: "apps/console-ui/src/api/ledger-api.ts",
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
    pageModule: "apps/console-ui/src/pages/admin/AdminOverviewPage.tsx",
    apiClient: "apps/console-ui/src/api/console-read-api.ts",
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
    pageModule: "apps/console-ui/src/pages/admin/AdminOverviewPage.tsx",
    apiClient: "apps/console-ui/src/api/console-read-api.ts",
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
    pageModule: "apps/console-ui/src/pages/admin/AdminOverviewPage.tsx",
    apiClient: "apps/console-ui/src/api/console-read-api.ts",
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
    pageModule: "apps/console-ui/src/pages/admin/AdminOverviewPage.tsx",
    apiClient: "apps/console-ui/src/api/console-read-api.ts",
    apiRoutes: ["GET /api/management/state", "GET /api/operator/summary", "POST /api/operator/cleanup-workspace-access"],
    serviceBoundary: "WorkspaceLifecycleService",
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
    pageModule: "apps/console-ui/src/pages/admin/AdminOverviewPage.tsx",
    apiClient: "apps/console-ui/src/api/support-api.ts",
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
    pageModule: "apps/console-ui/src/pages/shared/page-widgets.tsx",
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
    pageModule: "apps/console-ui/src/pages/shared/page-widgets.tsx",
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
    pageModule: "apps/console-ui/src/pages/shared/page-widgets.tsx",
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
