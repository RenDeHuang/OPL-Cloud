import { createOplCloud } from "../../packages/console/src/opl-cloud.js";
import { MemoryStore } from "../../packages/console/src/store.js";

export const TEST_PRICING = {
  serverHourly: {
    basic: 1,
    pro: 4,
    gpu: 20
  },
  computeHourly: {
    basic: 1,
    pro: 4,
    gpu: 20
  },
  storageGbMonth: 0.2,
  diskGbMonth: 0.2,
  markup: 0.2
};

export function currentResourceProvider(overrides = {}) {
  return {
    name: "test-provider",
    workspaceUrl({ workspaceId, slug, token }) {
      const id = slug || workspaceId;
      return `https://workspace.example.com/w/${id}?token=${token}`;
    },
    async createComputeResource({ computeId, packagePlan }) {
      return {
        providerResourceId: `nodepool/np-${computeId}`,
        nodePoolId: `np-${computeId}`,
        status: "running",
        billingStatus: "active",
        spec: packagePlan.server,
        image: "one-person-lab-app:test",
        runtime: {
          workloadName: `opl-${computeId}`,
          serviceName: `opl-${computeId}`,
          service: `service/opl-${computeId}`,
          dockerId: `deployment/opl-${computeId}`,
          nodeSelector: { "oplcloud.cn/compute-id": computeId }
        }
      };
    },
    async createStorageVolume({ storageId, packagePlan, storage = {} }) {
      return {
        providerResourceId: `pvc/opl-${storageId}-data`,
        status: "available",
        billingStatus: "active",
        sizeGb: storage.sizeGb || packagePlan.diskGb
      };
    },
    async attachStorage({ attachmentId, attachment, compute, storage }) {
      return {
        providerAttachmentId: `${compute.runtime?.dockerId || `deployment/opl-${compute.id}`}:${storage.providerResourceId}:${attachment.mountPath || "/data"}`,
        status: "attached",
        computeStatus: "running",
        storageStatus: "attached",
        runtime: compute.runtime
      };
    },
    async createWorkspaceEntry({ workspaceId, slug, token, compute }) {
      return {
        provider: "test-provider",
        slug,
        url: `https://workspace.example.com/w/${workspaceId}?token=${token}`,
        status: compute.status === "running" ? "ready" : "provisioning"
      };
    },
    ...overrides
  };
}

export function createCurrentTestService({ store = new MemoryStore(), runtimeProvider = currentResourceProvider(), pricing = TEST_PRICING, fabricCatalog } = {}) {
  return createOplCloud({
    store,
    runtimeProvider,
    pricing,
    ...(fabricCatalog ? { fabricCatalog } : {})
  });
}

export async function provisionWorkspace(service, {
  accountId = "pi-alpha",
  userId = "usr-alpha",
  organizationId = "",
  workspaceName = "Test Lab",
  packageId = "basic",
  sizeGb = 10
} = {}) {
  const compute = await service.createComputeResource({
    accountId,
    userId,
    packageId,
    name: `${workspaceName} compute`
  });
  const storage = await service.createStorageVolume({
    accountId,
    userId,
    packageId,
    sizeGb,
    name: `${workspaceName} storage`
  });
  const attachment = await service.attachStorage({
    accountId,
    computeId: compute.id,
    storageId: storage.id,
    mountPath: "/data"
  });
  const workspace = await service.createWorkspace({
    accountId,
    ...(organizationId ? { organizationId, userId } : {}),
    workspaceName,
    attachmentId: attachment.id
  });
  return { compute, storage, attachment, workspace };
}
