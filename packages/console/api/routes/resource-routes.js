export function buildResourceRoutes({ appService, body, pathParams = {}, scopedWorkspaceInput }) {
  return {
    "GET /api/compute-pools": () => appService.computePools(scopedWorkspaceInput(body)),
    "GET /api/compute-allocations": () => appService.computeAllocations(scopedWorkspaceInput(body)),
    "GET /api/compute-allocations/:id": () => appService.computeAllocation(scopedWorkspaceInput({ ...body, computeAllocationId: pathParams.id })),
    "POST /api/compute-allocations": () => appService.createComputeAllocation(scopedWorkspaceInput(body)),
    "POST /api/compute-allocations/:id/destroy": () => appService.destroyComputeAllocation(scopedWorkspaceInput({ ...body, computeAllocationId: pathParams.id })),
    "POST /api/storage-volumes": () => appService.createStorageVolume(scopedWorkspaceInput(body)),
    "POST /api/storage-volumes/destroy": () => appService.destroyStorageVolume(scopedWorkspaceInput(body)),
    "POST /api/storage-attachments": () => appService.attachStorage(scopedWorkspaceInput(body)),
    "POST /api/storage-attachments/detach": () => appService.detachStorage(scopedWorkspaceInput(body))
  };
}
