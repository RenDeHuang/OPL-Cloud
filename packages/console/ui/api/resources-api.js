import { postJson } from "./console-api.js";

export function createComputeAllocation(input, csrfToken) {
  return postJson("/api/compute-allocations", input, csrfToken);
}

export function destroyComputeAllocation(input, csrfToken) {
  return postJson(`/api/compute-allocations/${encodeURIComponent(input.computeAllocationId)}/destroy`, input, csrfToken);
}

export function createStorageVolume(input, csrfToken) {
  return postJson("/api/storage-volumes", input, csrfToken);
}

export function destroyStorageVolume(input, csrfToken) {
  return postJson("/api/storage-volumes/destroy", input, csrfToken);
}

export function attachStorage(input, csrfToken) {
  return postJson("/api/storage-attachments", input, csrfToken);
}

export function detachStorage(input, csrfToken) {
  return postJson("/api/storage-attachments/detach", input, csrfToken);
}
