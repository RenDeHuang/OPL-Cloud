export function createFakeRuntimeProvider(overrides = {}) {
  const provider = {
    name: "tencent-tke",
    workspaceUrl({ slug, token }) {
      return `https://workspace.example.test/w/${slug}?token=${token}`;
    },
    async createComputeAllocation({ computeAllocationId, packagePlan }) {
      return {
        providerResourceId: `cvm/${computeAllocationId}`,
        operationId: `op-${computeAllocationId}`,
        poolId: `pool-${packagePlan.id}-${packagePlan.server}`,
        nodePoolId: packagePlan.nodePoolId || `np-${packagePlan.id}`,
        instanceId: `ins-${computeAllocationId}`,
        nodeName: `${computeAllocationId}.node.internal`,
        status: "running",
        billingStatus: "active",
        spec: packagePlan.server,
        image: "ghcr.io/gaofeng21cn/one-person-lab-app:latest",
        runtime: {
          serviceName: `opl-runtime-${computeAllocationId}`,
          service: `service/opl-runtime-${computeAllocationId}`
        },
        providerData: {
          computeAllocationId,
          packageId: packagePlan.id,
          requestId: `req-${computeAllocationId}`
        }
      };
    },
    async createStorageVolume({ storageId, packagePlan }) {
      return {
        providerResourceId: `pvc/${storageId}`,
        operationId: `op-${storageId}`,
        status: "available",
        billingStatus: "active",
        storageClass: packagePlan.storageClassId || "cbs",
        providerData: {
          storageId,
          diskGb: packagePlan.diskGb,
          requestId: `req-${storageId}`
        }
      };
    },
    async attachStorage({ attachment, compute, storage }) {
      return {
        providerAttachmentId: `mount/${compute.id}:${storage.id}:${attachment.mountPath}`,
        operationId: `op-${attachment.id}`,
        status: "attached",
        computeStatus: "running",
        storageStatus: "attached",
        providerData: {
          computeAllocationId: compute.id,
          storageId: storage.id,
          mountPath: attachment.mountPath
        }
      };
    },
    async detachStorage() {
      return { status: "detached" };
    },
    async destroyComputeAllocation() {
      return { status: "destroyed" };
    },
    async destroyStorageVolume() {
      return { status: "destroyed" };
    },
    async createWorkspaceEntry({ workspaceId, slug, token, compute }) {
      return {
        slug,
        url: `https://workspace.example.test/w/${slug}?token=${token}`,
        status: "ready",
        provider: "tencent-tke",
        providerData: {
          workspaceId,
          runtimeService: compute.runtime?.serviceName || ""
        }
      };
    }
  };
  return { ...provider, ...overrides };
}
