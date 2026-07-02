const DEFAULT_WORKSPACE_IMAGE = "ghcr.io/gaofeng21cn/one-person-lab-app:latest";
const DEFAULT_WORKSPACE_DOMAIN = "workspace.medopl.cn";
const DEFAULT_STORAGE_CLASS = "cbs";

function clone(value) {
  return JSON.parse(JSON.stringify(value));
}

function workspaceHost(value) {
  return String(value || DEFAULT_WORKSPACE_DOMAIN).replace(/^https?:\/\//, "").replace(/\/$/, "");
}

function summarizeResources(items = []) {
  return {
    available: items.filter((item) => item.available).map((item) => item.id),
    unavailable: items
      .filter((item) => !item.available)
      .map((item) => ({
        id: item.id,
        reason: item.unavailableReason || "not_available"
      }))
  };
}

export function defaultFabricResourceCatalog({ env = process.env } = {}) {
  const storageClassName = env.OPL_WORKSPACE_STORAGE_CLASS || DEFAULT_STORAGE_CLASS;
  const workspaceImage = env.OPL_WORKSPACE_IMAGE || DEFAULT_WORKSPACE_IMAGE;
  const workspaceDomain = workspaceHost(env.OPL_WORKSPACE_DOMAIN);

  const commonRefs = {
    storageClassId: "workspace-cbs",
    workspaceImageId: "one-person-lab-app",
    ingressDomainId: "workspace",
    environmentTemplateId: "opl-app-webui"
  };

  return {
    schemaVersion: 1,
    owner: "OPL Fabric",
    workspacePackages: [
      {
        id: "basic",
        name: "Basic Workspace",
        accelerator: "cpu",
        cpu: 2,
        memoryGb: 4,
        gpu: 0,
        server: "2c4g",
        diskGb: 10,
        available: true,
        verified: true,
        computeProfileId: "cpu-basic",
        ...commonRefs
      },
      {
        id: "pro",
        name: "Pro Workspace",
        accelerator: "cpu",
        cpu: 8,
        memoryGb: 16,
        gpu: 0,
        server: "8c16g",
        diskGb: 100,
        available: true,
        verified: true,
        computeProfileId: "cpu-pro",
        ...commonRefs
      },
      {
        id: "gpu",
        name: "GPU Workspace",
        accelerator: "gpu",
        cpu: 16,
        memoryGb: 64,
        gpu: 1,
        server: "16c64g-1gpu",
        diskGb: 500,
        available: false,
        verified: false,
        unavailableReason: "gpu_node_pool_not_verified",
        computeProfileId: "gpu-standard",
        ...commonRefs
      }
    ],
    computeProfiles: [
      { id: "cpu-basic", accelerator: "cpu", cpu: 2, memoryGb: 4, gpu: 0, available: true, provider: "tencent-tke" },
      { id: "cpu-pro", accelerator: "cpu", cpu: 8, memoryGb: 16, gpu: 0, available: true, provider: "tencent-tke" },
      {
        id: "gpu-standard",
        accelerator: "gpu",
        cpu: 16,
        memoryGb: 64,
        gpu: 1,
        available: false,
        provider: "tencent-tke",
        unavailableReason: "gpu_node_pool_not_verified"
      }
    ],
    storageClasses: [
      {
        id: "workspace-cbs",
        name: "Tencent CBS Workspace PVC",
        provider: "tencent-tke",
        storageClassName,
        accessMode: "ReadWriteOnce",
        available: true
      }
    ],
    workspaceImages: [
      {
        id: "one-person-lab-app",
        name: "one-person-lab-app WebUI",
        image: workspaceImage,
        port: 3000,
        persistentMounts: ["/data", "/projects"],
        available: true
      }
    ],
    ingressDomains: [
      {
        id: "workspace",
        host: workspaceDomain,
        pathPattern: "/w/<workspaceId>",
        available: true
      }
    ],
    environmentTemplates: [
      {
        id: "opl-app-webui",
        name: "OPL App WebUI",
        workspaceImageId: "one-person-lab-app",
        port: 3000,
        persistentMounts: ["/data", "/projects"],
        available: true
      }
    ],
    connectors: [
      {
        id: "opl-connect-registry",
        name: "OPL Connect Registry",
        available: false,
        unavailableReason: "connector_registry_not_implemented_in_console_control_plane"
      }
    ],
    agentPackages: [
      {
        id: "opl-agent-registry",
        name: "OPL Agent Registry",
        available: false,
        unavailableReason: "agent_registry_not_implemented_in_console_control_plane"
      }
    ]
  };
}

export function availableWorkspacePackages(catalog) {
  return (catalog?.workspacePackages || [])
    .filter((item) => item.available)
    .map(clone);
}

export function selectWorkspacePackage(catalog, packageId, { requireAvailable = false } = {}) {
  const packagePlan = (catalog?.workspacePackages || []).find((item) => item.id === packageId);
  if (!packagePlan) throw new Error("unknown_package");
  if (requireAvailable && !packagePlan.available) {
    throw new Error(`package_unavailable:${packagePlan.id}:${packagePlan.unavailableReason || "not_available"}`);
  }
  return clone(packagePlan);
}

export function fabricCatalogReadiness(catalog) {
  const workspacePackages = summarizeResources(catalog?.workspacePackages || []);
  const storageClasses = summarizeResources(catalog?.storageClasses || []);
  const workspaceImages = summarizeResources(catalog?.workspaceImages || []);
  const ingressDomains = summarizeResources(catalog?.ingressDomains || []);
  const environmentTemplates = summarizeResources(catalog?.environmentTemplates || []);

  return {
    ready:
      workspacePackages.available.length > 0 &&
      storageClasses.available.length > 0 &&
      workspaceImages.available.length > 0 &&
      ingressDomains.available.length > 0 &&
      environmentTemplates.available.length > 0,
    workspacePackages,
    storageClasses,
    workspaceImages,
    ingressDomains,
    environmentTemplates,
    connectors: summarizeResources(catalog?.connectors || []),
    agentPackages: summarizeResources(catalog?.agentPackages || [])
  };
}
