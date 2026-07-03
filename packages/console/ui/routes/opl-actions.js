export const consoleActions = Object.freeze([
  {
    id: "workspace.create",
    label: "Create Workspace",
    type: "route",
    role: "lab_owner",
    objectKind: "Workspace",
    routeId: "workspace.create"
  },
  {
    id: "workspace.detail",
    label: "Open Workspace Detail",
    type: "route",
    role: "lab_owner",
    objectKind: "Workspace",
    routeId: "workspace.detail"
  },
  {
    id: "workspace.openUrl",
    label: "Open Workspace URL",
    type: "external",
    role: "lab_owner",
    objectKind: "WorkspaceAccess",
    requires: ["workspace.url.active"]
  },
  {
    id: "workspace.copyUrl",
    label: "Copy Workspace URL",
    type: "copy",
    role: "lab_owner",
    objectKind: "WorkspaceAccess",
    requires: ["workspace.url.active"]
  },
  {
    id: "workspace.resetUrl",
    label: "Reset Workspace URL",
    type: "api",
    role: "lab_owner",
    objectKind: "WorkspaceAccess",
    apiClient: "packages/console/ui/api/workspaces-api.js",
    apiName: "resetWorkspaceToken",
    requires: ["workspace.url.active"]
  },
  {
    id: "workspace.deleteUrl",
    label: "Disable Workspace URL",
    type: "api",
    role: "lab_owner",
    objectKind: "WorkspaceAccess",
    apiClient: "packages/console/ui/api/workspaces-api.js",
    apiName: "deleteWorkspaceToken",
    requires: ["workspace.url.active"]
  },
  {
    id: "workspace.stopCompute",
    label: "Stop Compute",
    type: "api",
    role: "lab_owner",
    objectKind: "WorkspaceCompute",
    apiClient: "packages/console/ui/api/workspaces-api.js",
    apiName: "stopWorkspaceServer"
  },
  {
    id: "workspace.restartCompute",
    label: "Restart Compute",
    type: "api",
    role: "lab_owner",
    objectKind: "WorkspaceCompute",
    apiClient: "packages/console/ui/api/workspaces-api.js",
    apiName: "restartWorkspaceServer"
  },
  {
    id: "workspace.destroyCompute",
    label: "Destroy Compute",
    type: "api",
    role: "lab_owner",
    objectKind: "WorkspaceCompute",
    apiClient: "packages/console/ui/api/workspaces-api.js",
    apiName: "destroyWorkspaceServer"
  },
  {
    id: "workspace.destroyStorage",
    label: "Destroy Storage",
    type: "api",
    role: "lab_owner",
    objectKind: "WorkspaceStorage",
    apiClient: "packages/console/ui/api/workspaces-api.js",
    apiName: "destroyWorkspaceDisk"
  },
  {
    id: "billing.wallet",
    label: "Wallet and Holds",
    type: "route",
    role: "lab_owner",
    objectKind: "Wallet",
    routeId: "billing.wallet"
  },
  {
    id: "support.create",
    label: "Create Support Ticket",
    type: "route",
    role: "lab_owner",
    objectKind: "SupportTicket",
    routeId: "support.create"
  },
  {
    id: "support.detail",
    label: "Open Support Ticket",
    type: "route",
    role: "lab_owner",
    objectKind: "SupportTicket",
    routeId: "support.detail"
  },
  {
    id: "gateway.openExternal",
    label: "Open OPL Gateway",
    type: "route",
    role: "lab_owner",
    objectKind: "GatewayIntegration",
    routeId: "gateway.external"
  },
  {
    id: "admin.manualTopup",
    label: "Manual Top-up",
    type: "api",
    role: "admin",
    objectKind: "Wallet",
    apiClient: "packages/console/ui/api/billing-api.js",
    apiName: "manualTopUp"
  },
  {
    id: "admin.userCreate.disabled",
    label: "Create User",
    type: "disabled",
    role: "admin",
    objectKind: "User",
    disabledReason: "User creation route is not part of the current commercial launch contract."
  },
  {
    id: "admin.userWallet.disabled",
    label: "User Wallet Detail",
    type: "disabled",
    role: "admin",
    objectKind: "Wallet",
    disabledReason: "Use Manual Top-up in the Users table; standalone wallet detail route is backlog."
  }
]);
