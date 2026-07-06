export const consoleActions = Object.freeze([
  {
    id: "workspace.create",
    label: "创建工作区入口",
    type: "route",
    role: "lab_owner",
    objectKind: "Workspace",
    routeId: "workspace.create",
    mutation: true,
    confirmation: "normal",
    operationTimeline: true,
    failureVisible: true
  },
  {
    id: "workspace.detail",
    label: "查看工作区入口",
    type: "route",
    role: "lab_owner",
    objectKind: "Workspace",
    routeId: "workspace.detail"
  },
  {
    id: "workspace.openUrl",
    label: "打开工作区入口",
    type: "external",
    role: "lab_owner",
    objectKind: "WorkspaceAccess",
    requires: ["workspace.url.active"]
  },
  {
    id: "workspace.copyUrl",
    label: "复制工作区入口",
    type: "copy",
    role: "lab_owner",
    objectKind: "WorkspaceAccess",
    requires: ["workspace.url.active"]
  },
  {
    id: "workspace.resetUrl",
    label: "重置工作区入口",
    type: "api",
    role: "lab_owner",
    objectKind: "WorkspaceAccess",
    apiClient: "apps/console-ui/src/api/workspaces-api.ts",
    apiName: "resetWorkspaceToken",
    requires: ["workspace.url.active"],
    mutation: true,
    confirmation: "normal",
    operationTimeline: true,
    failureVisible: true
  },
  {
    id: "workspace.deleteUrl",
    label: "停用工作区入口",
    type: "api",
    role: "lab_owner",
    objectKind: "WorkspaceAccess",
    apiClient: "apps/console-ui/src/api/workspaces-api.ts",
    apiName: "deleteWorkspaceToken",
    requires: ["workspace.url.active"],
    mutation: true,
    confirmation: "normal",
    operationTimeline: true,
    failureVisible: true
  },
  {
    id: "compute-allocations.create",
    label: "开通计算资源",
    type: "route",
    role: "lab_owner",
    objectKind: "ComputeAllocation",
    routeId: "compute-allocations.create",
    mutation: true,
    confirmation: "normal",
    costImpact: "required",
    operationTimeline: true,
    failureVisible: true
  },
  {
    id: "compute-allocations.detail",
    label: "查看计算资源",
    type: "route",
    role: "lab_owner",
    objectKind: "ComputeAllocation",
    routeId: "compute-allocations.detail"
  },
  {
    id: "compute-allocations.destroy",
    label: "销毁计算资源",
    type: "api",
    role: "lab_owner",
    objectKind: "ComputeAllocation",
    apiClient: "apps/console-ui/src/api/resources-api.ts",
    apiName: "destroyComputeAllocation",
    mutation: true,
    destructive: true,
    confirmation: "normal",
    operationTimeline: true,
    failureVisible: true
  },
  {
    id: "storage.create",
    label: "开通存储资源",
    type: "route",
    role: "lab_owner",
    objectKind: "StorageVolume",
    routeId: "storage.create",
    mutation: true,
    confirmation: "normal",
    costImpact: "required",
    operationTimeline: true,
    failureVisible: true
  },
  {
    id: "storage.detail",
    label: "查看存储资源",
    type: "route",
    role: "lab_owner",
    objectKind: "StorageVolume",
    routeId: "storage.detail"
  },
  {
    id: "storage.destroy",
    label: "销毁存储资源",
    type: "api",
    role: "lab_owner",
    objectKind: "StorageVolume",
    apiClient: "apps/console-ui/src/api/resources-api.ts",
    apiName: "destroyStorageVolume",
    mutation: true,
    destructive: true,
    dataLoss: true,
    confirmation: "strong",
    requiredConfirmText: "确认删除数据",
    operationTimeline: true,
    failureVisible: true
  },
  {
    id: "attachment.create",
    label: "挂载存储资源",
    type: "route",
    role: "lab_owner",
    objectKind: "StorageAttachment",
    routeId: "attachment.create",
    mutation: true,
    confirmation: "normal",
    operationTimeline: true,
    failureVisible: true
  },
  {
    id: "attachment.detail",
    label: "查看挂载关系",
    type: "route",
    role: "lab_owner",
    objectKind: "StorageAttachment",
    routeId: "attachment.detail"
  },
  {
    id: "attachment.detach",
    label: "解除挂载",
    type: "api",
    role: "lab_owner",
    objectKind: "StorageAttachment",
    apiClient: "apps/console-ui/src/api/resources-api.ts",
    apiName: "detachStorage",
    mutation: true,
    destructive: true,
    confirmation: "normal",
    operationTimeline: true,
    failureVisible: true
  },
  {
    id: "resources.relationships",
    label: "查看资源关系",
    type: "route",
    role: "lab_owner",
    objectKind: "ResourceRelationship",
    routeId: "resources.relationships"
  },
  {
    id: "billing.wallet",
    label: "钱包与冻结",
    type: "route",
    role: "lab_owner",
    objectKind: "Wallet",
    routeId: "billing.wallet"
  },
  {
    id: "support.create",
    label: "提交工单",
    type: "route",
    role: "lab_owner",
    objectKind: "SupportTicket",
    routeId: "support.create"
  },
  {
    id: "support.detail",
    label: "查看工单",
    type: "route",
    role: "lab_owner",
    objectKind: "SupportTicket",
    routeId: "support.detail"
  },
  {
    id: "gateway.openExternal",
    label: "打开网关",
    type: "route",
    role: "lab_owner",
    objectKind: "GatewayIntegration",
    routeId: "gateway.external"
  },
  {
    id: "admin.manualTopup",
    label: "人工充值",
    type: "api",
    role: "admin",
    objectKind: "Wallet",
    apiClient: "apps/console-ui/src/api/billing-api.ts",
    apiName: "manualTopUp"
  },
  {
    id: "admin.userCreate",
    label: "新建用户",
    type: "api",
    role: "admin",
    objectKind: "User",
    apiClient: "apps/console-ui/src/api/console-read-api.ts",
    apiName: "createUser"
  },
  {
    id: "admin.userDisable",
    label: "禁用用户",
    type: "api",
    role: "admin",
    objectKind: "User",
    apiClient: "apps/console-ui/src/api/console-read-api.ts",
    apiName: "disableUser",
    mutation: true,
    confirmation: "normal",
    failureVisible: true
  },
  {
    id: "admin.userDelete",
    label: "删除用户",
    type: "api",
    role: "admin",
    objectKind: "User",
    apiClient: "apps/console-ui/src/api/console-read-api.ts",
    apiName: "deleteUser",
    mutation: true,
    destructive: true,
    confirmation: "normal",
    failureVisible: true
  },
  {
    id: "admin.diagnostics",
    label: "查看线上诊断",
    type: "route",
    role: "admin",
    objectKind: "RuntimeReadiness",
    routeId: "admin.diagnostics"
  },
  {
    id: "admin.e2e",
    label: "查看 E2E 记录",
    type: "route",
    role: "admin",
    objectKind: "ProductionVerification",
    routeId: "admin.e2e"
  },
  {
    id: "admin.userWallet.disabled",
    label: "用户钱包详情",
    type: "disabled",
    role: "admin",
    objectKind: "Wallet",
    disabledReason: "请在用户表使用人工充值；独立钱包详情不在当前上线范围。"
  }
]);
