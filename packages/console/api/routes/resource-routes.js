function queryInput(request) {
  if (!request?.url) return {};
  const url = new URL(request.url, "http://localhost");
  const accountId = url.searchParams.get("accountId");
  return accountId ? { accountId } : {};
}

export function buildResourceRoutes({ appService, body, request, pathParams = {}, scopedWorkspaceInput }) {
  const readInput = { ...queryInput(request), ...body };
  return {
    "GET /api/compute-pools": () => appService.computePools(scopedWorkspaceInput(readInput)),
    "GET /api/compute-allocations": () => appService.computeAllocations(scopedWorkspaceInput(readInput)),
    "GET /api/compute-allocations/:id": () => appService.computeAllocation(scopedWorkspaceInput({ ...readInput, computeAllocationId: pathParams.id })),
    "POST /api/compute-allocations": () => appService.createComputeAllocation(scopedWorkspaceInput(body)),
    "POST /api/compute-allocations/:id/destroy": () => appService.destroyComputeAllocation(scopedWorkspaceInput({ ...body, computeAllocationId: pathParams.id })),
    "POST /api/storage-volumes": () => appService.createStorageVolume(scopedWorkspaceInput(body)),
    "POST /api/storage-volumes/destroy": () => appService.destroyStorageVolume(scopedWorkspaceInput(body)),
    "POST /api/storage-attachments": () => appService.attachStorage(scopedWorkspaceInput(body)),
    "POST /api/storage-attachments/detach": () => appService.detachStorage(scopedWorkspaceInput(body))
  };
}
