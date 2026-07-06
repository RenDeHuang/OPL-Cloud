import { operationEnvelope, postJson } from "./console-api.ts";

export function createComputeAllocation(input, csrfToken) {
  return postJson("/api/compute-allocations", input, csrfToken)
    .then((payload) => operationEnvelope(payload, { next: { detailRouteId: "compute-allocations.detail" } }));
}

export function destroyComputeAllocation(input, csrfToken) {
  return postJson(`/api/compute-allocations/${encodeURIComponent(input.computeAllocationId)}/destroy`, input, csrfToken)
    .then((payload) => operationEnvelope(payload, { resourceId: input.computeAllocationId, next: { detailRouteId: "compute-allocations.detail" } }));
}

export function createStorageVolume(input, csrfToken) {
  return postJson("/api/storage-volumes", input, csrfToken)
    .then((payload) => operationEnvelope(payload, { next: { detailRouteId: "storage.detail" } }));
}

export function destroyStorageVolume(input, csrfToken) {
  return postJson("/api/storage-volumes/destroy", input, csrfToken)
    .then((payload) => operationEnvelope(payload, { resourceId: input.storageId, next: { detailRouteId: "storage.detail" } }));
}

export function attachStorage(input, csrfToken) {
  return postJson("/api/storage-attachments", input, csrfToken)
    .then((payload) => operationEnvelope(payload, { next: { detailRouteId: "attachment.detail" } }));
}

export function detachStorage(input, csrfToken) {
  return postJson("/api/storage-attachments/detach", input, csrfToken)
    .then((payload) => operationEnvelope(payload, { resourceId: input.attachmentId, next: { detailRouteId: "attachment.detail" } }));
}
