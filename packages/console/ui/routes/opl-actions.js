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
    id: "compute-allocations.create",
    label: "Create Compute",
    type: "route",
    role: "lab_owner",
    objectKind: "ComputeAllocation",
    routeId: "compute-allocations.create"
  },
  {
    id: "compute-allocations.detail",
    label: "Open Compute Detail",
    type: "route",
    role: "lab_owner",
    objectKind: "ComputeAllocation",
    routeId: "compute-allocations.detail"
  },
  {
    id: "compute-allocations.destroy",
    label: "Destroy Compute",
    type: "api",
    role: "lab_owner",
    objectKind: "ComputeAllocation",
    apiClient: "packages/console/ui/api/resources-api.js",
    apiName: "destroyComputeAllocation"
  },
  {
    id: "storage.create",
    label: "Create Storage",
    type: "route",
    role: "lab_owner",
    objectKind: "StorageVolume",
    routeId: "storage.create"
  },
  {
    id: "storage.detail",
    label: "Open Storage Detail",
    type: "route",
    role: "lab_owner",
    objectKind: "StorageVolume",
    routeId: "storage.detail"
  },
  {
    id: "storage.destroy",
    label: "Destroy Storage",
    type: "api",
    role: "lab_owner",
    objectKind: "StorageVolume",
    apiClient: "packages/console/ui/api/resources-api.js",
    apiName: "destroyStorageVolume"
  },
  {
    id: "attachment.create",
    label: "Attach Storage",
    type: "route",
    role: "lab_owner",
    objectKind: "StorageAttachment",
    routeId: "attachment.create"
  },
  {
    id: "attachment.detail",
    label: "Open Attachment Detail",
    type: "route",
    role: "lab_owner",
    objectKind: "StorageAttachment",
    routeId: "attachment.detail"
  },
  {
    id: "attachment.detach",
    label: "Detach Storage",
    type: "api",
    role: "lab_owner",
    objectKind: "StorageAttachment",
    apiClient: "packages/console/ui/api/resources-api.js",
    apiName: "detachStorage"
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
    id: "admin.userCreate",
    label: "Create User",
    type: "api",
    role: "admin",
    objectKind: "User",
    apiClient: "packages/console/ui/api/console-read-api.js",
    apiName: "createUser"
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
