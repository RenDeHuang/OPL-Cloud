export function buildResourceRoutes({ appService, body, scopedWorkspaceInput }) {
  return {
    "POST /api/compute-resources": () => appService.createComputeResource(scopedWorkspaceInput(body)),
    "POST /api/compute-resources/destroy": () => appService.destroyComputeResource(scopedWorkspaceInput(body)),
    "POST /api/storage-volumes": () => appService.createStorageVolume(scopedWorkspaceInput(body)),
    "POST /api/storage-volumes/destroy": () => appService.destroyStorageVolume(scopedWorkspaceInput(body)),
    "POST /api/storage-attachments": () => appService.attachStorage(scopedWorkspaceInput(body)),
    "POST /api/storage-attachments/detach": () => appService.detachStorage(scopedWorkspaceInput(body))
  };
}
