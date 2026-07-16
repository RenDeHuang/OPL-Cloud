import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const contractsDir = new URL("../../packages/contracts/", import.meta.url);

async function readContract(file) {
  return JSON.parse(await readFile(new URL(file, contractsDir), "utf8"));
}

test("shared execution contract fixes canonical identities, states, writes, and errors", async () => {
  const contract = await readContract("opl-cloud-shared-execution-contract.json");
  const objects = Object.fromEntries(contract.canonicalObjects.map((object) => [object.kind, object]));

  assert.equal(contract.schemaVersion, 3);
  assert.deepEqual(Object.keys(objects), ["Project", "Task", "ExecutionRequest", "Approval", "Job", "Artifact", "Review", "Receipt", "Continuation"]);
  assert.deepEqual(contract.identity.canonicalIdFields, ["projectId", "taskId", "requestId", "approvalId", "jobId", "artifactId", "reviewId", "receiptId", "continuationId"]);
  assert.deepEqual(contract.stateMachines.task, ["draft", "planned", "awaiting_approval", "queued", "running", "review_required", "review_blocked", "completed", "failed", "cancelled", "archived"]);
  assert.deepEqual(contract.stateMachines.job, ["queued", "provisioning", "running", "collecting", "succeeded", "failed", "cancelled", "timed_out"]);
  assert.deepEqual(contract.stateMachines.receipt, ["planned", "approved", "running", "completed", "failed", "timed_out", "cancelled", "review_required", "review_blocked"]);
  assert.deepEqual(contract.writeEnvelope.requiredFields, ["operationId", "idempotencyKey", "actor", "organizationId", "workspaceId", "clientId", "caller", "occurredAt"]);
  assert.deepEqual(contract.stateMachines.syncEvent, ["accepted", "conflict", "resolved"]);
  assert.deepEqual(Object.fromEntries(Object.entries(contract.errorSemantics).map(([code, value]) => [code, value.httpStatus])), {
    invalid_request: 400,
    unauthenticated: 401,
    forbidden: 403,
    not_found: 404,
    conflict: 409,
    unavailable_dependency: 422,
    quota_exceeded: 429,
    retryable_dependency_failure: 503
  });
  assert.equal(contract.canonicalExample.projectId, contract.canonicalExample.receipt.projectId);
  assert.equal(contract.canonicalExample.taskId, contract.canonicalExample.receipt.taskId);
  assert.equal(contract.canonicalExample.jobId, contract.canonicalExample.receipt.jobId);
  assert.equal(contract.canonicalExample.receipt.receiptId, contract.canonicalExample.continuation.receiptId);
});

test("Ledger general receipt uses the shared execution identity and states", async () => {
  const shared = await readContract("opl-cloud-shared-execution-contract.json");
  const ledger = await readContract("opl-cloud-evidence-ledger-contract.json");

  assert.deepEqual(ledger.generalReceiptV1.statuses, shared.stateMachines.receipt);
  assert.deepEqual(ledger.generalReceiptV1.identityFields, ["organizationId", "workspaceId", ...shared.identity.canonicalIdFields]);
  assert.deepEqual(ledger.generalReceiptV1.evidenceChain, ["request", "plan", "approval", "execution", "environment", "inputRefs", "outputRefs", "reviewerChecks", "cost", "receipt", "continuation"]);
  assert.equal(ledger.generalReceiptV1.writeProtocol, "append_first_with_idempotency");
  assert.deepEqual(ledger.generalReceiptV1.query, {
    endpoint: "GET /ledger/receipts",
    exactFilters: ["organizationId", "workspaceId", "projectId", "taskId", "jobId", "type", "status"],
    order: ["createdAt desc", "receiptId desc"],
    pagination: { cursor: "opaque_createdAt_receiptId", defaultLimit: 50, maxLimit: 100 }
  });
  assert.ok(ledger.generalReceiptV1.forbiddenContent.includes("rawCredential"));
  assert.ok(ledger.generalReceiptV1.forbiddenContent.includes("password"));
  assert.ok(ledger.receiptTypes.includes("execution.receipt.v1"));
  assert.deepEqual(ledger.workspaceAccessTokenResetReceipt, {
    type: "workspace.access_token_reset",
    idempotencyIdentity: "runtime-credential-rotate:<workspaceId>:<request-idempotency-key>",
    requiredRefs: ["runtimeId", "computeAllocationId", "storageId", "credentialVersion", "credentialSecretRef", "owner.userId"],
    forbiddenContent: ["password", "apiKey", "rawCredential", "rawProviderResponse"]
  });
  assert.deepEqual(ledger.generalReceiptV1.reviewPolicy.statuses, ["active", "superseded"]);
  assert.equal(ledger.generalReceiptV1.reviewPolicy.idempotencyNamespace, "review_policy");
  assert.deepEqual(ledger.generalReceiptV1.reviewGate.statuses, ["accepted", "review_required", "review_blocked"]);
  assert.match(ledger.generalReceiptV1.reviewGate.continuationRule, /without an active policy is ineligible/);
  assert.match(ledger.generalReceiptV1.reviewGate.receiptReadRule, /omit continuationId and continuation/);
});

test("service owners match the shared execution contract", async () => {
  const shared = await readContract("opl-cloud-shared-execution-contract.json");
  const boundary = await readContract("opl-cloud-service-boundary-contract.json");

  for (const object of shared.canonicalObjects) {
    assert.ok(boundary.services[object.service].owns.includes(object.ownershipKey), `${object.kind} ownership must stay with ${object.service}`);
  }
  assert.deepEqual(boundary.externalServices.gateway.owns, ["spendableBalances", "apiKeys", "routePolicies", "modelPolicies", "usageEvents"]);
  assert.equal(boundary.externalServices.gateway.calls, undefined);
  assert.equal(boundary.externalServices.gateway.evidenceSink, undefined);
  assert.ok(boundary.services.ledger.owns.includes("reviewPolicies"));
  assert.ok(boundary.services.ledger.readApis.includes("reviewGateEvaluation"));
});

test("published HTTP APIs preserve service ownership without compatibility routes", async () => {
  const shared = await readContract("opl-cloud-shared-execution-contract.json");
  const ledger = await readContract("opl-cloud-evidence-ledger-contract.json");

  assert.deepEqual(shared.httpApis.controlPlane, {
    createProject: "POST /api/projects",
    createTask: "POST /api/projects/<projectId>/tasks",
    requestExecution: "POST /api/execution-requests",
    approveExecution: "POST /api/execution-requests/<requestId>/approve",
    executeRequest: "POST /api/execution-requests/<requestId>/execute",
    syncExecution: "POST /api/execution-requests/<requestId>/sync",
    resolveExecutionContinuation: "GET /api/execution-requests/<requestId>/continuation",
    queryExecution: "GET /api/execution-requests/<requestId>",
    pushSyncMutation: "POST /api/workspaces/<workspaceId>/sync/mutations",
    pullSyncChanges: "GET /api/workspaces/<workspaceId>/sync/changes?after=<cursor>&limit=<n>",
    resolveSyncConflict: "POST /api/workspaces/<workspaceId>/sync/conflicts/<conflictId>/resolve"
  });
  assert.deepEqual(shared.httpApis.fabric, {
    createJob: "POST /fabric/jobs",
    queryJob: "GET /fabric/jobs/<jobId>",
    cancelJob: "POST /fabric/jobs/<jobId>/cancel",
    claimJob: "POST /fabric/jobs/<jobId>/claim",
    heartbeatJob: "POST /fabric/jobs/<jobId>/heartbeat",
    completeJob: "POST /fabric/jobs/<jobId>/complete",
    failJob: "POST /fabric/jobs/<jobId>/fail",
    retryJob: "POST /fabric/jobs/<jobId>/retry"
  });
  assert.deepEqual(shared.httpApis.ledger, {
    recordReceipt: "POST /ledger/receipts",
    queryReceipt: "GET /ledger/receipts/<receiptId>",
    resolveContinuation: "GET /ledger/receipts/<receiptId>/continuation",
    recordArtifact: "POST /ledger/artifacts",
    queryArtifact: "GET /ledger/artifacts/<artifactId>",
    recordReview: "POST /ledger/reviews",
    queryReview: "GET /ledger/reviews/<reviewId>"
  });
  assert.ok(ledger.receiptTypes.includes("artifact.manifest.v1"));
  assert.ok(ledger.receiptTypes.includes("review.result.v1"));
  assert.equal(ledger.generalReceiptV1.api.resolveContinuation, shared.httpApis.ledger.resolveContinuation);
  assert.equal(ledger.generalReceiptV1.api.evaluateReviewGate, "POST /ledger/review-gates/evaluate");
});
