import { operationEnvelope, postJson } from "./console-api.ts";

function withUnknownResult(request, failureReason) {
  return request.catch((error) => {
    if (error?.payload) throw error;
    const unknown: any = new Error(failureReason, { cause: error });
    unknown.payload = { status: "unknown", retryable: true, failureReason };
    throw unknown;
  });
}

export function createComputeAllocation(input, csrfToken, idempotencyKey = "") {
  return withUnknownResult(
    postJson("/api/compute-allocations", input, csrfToken, idempotencyKey)
      .then((payload) => operationEnvelope(payload, { next: { detailRouteId: "compute-allocations.detail" } })),
    "计算资源开通结果未知，请重试同一开通请求。"
  );
}

export function destroyComputeAllocation(input, csrfToken) {
  return postJson(`/api/compute-allocations/${encodeURIComponent(input.computeAllocationId)}/destroy`, input, csrfToken)
    .then((payload) => operationEnvelope(payload, { resourceId: input.computeAllocationId, next: { detailRouteId: "compute-allocations.detail" } }));
}

export function syncComputeAllocation(input, csrfToken) {
  return postJson(`/api/compute-allocations/${encodeURIComponent(input.computeAllocationId)}/sync`, input, csrfToken)
    .then((payload) => operationEnvelope(payload, { resourceId: input.computeAllocationId, next: { detailRouteId: "compute-allocations.detail" } }));
}

export function createStorageVolume(input, csrfToken, idempotencyKey = "") {
  return withUnknownResult(
    postJson("/api/storage-volumes", input, csrfToken, idempotencyKey)
      .then((payload) => operationEnvelope(payload, { next: { detailRouteId: "storage.detail" } })),
    "存储资源开通结果未知，请重试同一开通请求。"
  );
}

export const reactivateStorageVolume = createStorageVolume;

export function destroyStorageVolume(input, csrfToken) {
  return postJson("/api/storage-volumes/destroy", input, csrfToken)
    .then((payload) => operationEnvelope(payload, { resourceId: input.storageId, next: { detailRouteId: "storage.detail" } }));
}

export function syncStorageVolume(input, csrfToken) {
  return postJson(`/api/storage-volumes/${encodeURIComponent(input.storageId)}/sync`, input, csrfToken)
    .then((payload) => operationEnvelope(payload, { resourceId: input.storageId, next: { detailRouteId: "storage.detail" } }));
}

export function setResourceAutoRenew(input, csrfToken, idempotencyKey = "") {
  return postJson(`/api/resources/${encodeURIComponent(input.resourceId)}/auto-renew`, { autoRenew: input.autoRenew }, csrfToken, idempotencyKey)
    .then((payload) => operationEnvelope(payload, { resourceId: input.resourceId }));
}

export function attachStorage(input, csrfToken, idempotencyKey = "") {
  return withUnknownResult(
    postJson("/api/storage-attachments", input, csrfToken, idempotencyKey)
      .then((payload) => operationEnvelope(payload, { next: { detailRouteId: "attachment.detail" } })),
    "存储挂载结果未知，请重试同一开通请求。"
  );
}

export function detachStorage(input, csrfToken) {
  return postJson("/api/storage-attachments/detach", input, csrfToken)
    .then((payload) => operationEnvelope(payload, { resourceId: input.attachmentId, next: { detailRouteId: "attachment.detail" } }));
}
