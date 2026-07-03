import { postJson } from "./console-api.js";

export function createComputeResource(input, csrfToken) {
  return postJson("/api/compute-resources", input, csrfToken);
}

export function destroyComputeResource(input, csrfToken) {
  return postJson("/api/compute-resources/destroy", input, csrfToken);
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
