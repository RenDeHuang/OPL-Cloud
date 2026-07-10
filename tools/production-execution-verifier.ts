import { createHash } from "node:crypto";

function normalizedOrigin(value, name) {
  let parsed;
  try {
    parsed = new URL(value);
  } catch {
    throw new Error(`${name}_invalid`);
  }
  if (!['http:', 'https:'].includes(parsed.protocol)) throw new Error(`${name}_invalid`);
  return parsed.origin;
}

function required(value, name) {
  if (!value) throw new Error(`${name}_required`);
  return value;
}

function assertFields(payload, expected, stage) {
  for (const [field, value] of Object.entries(expected)) {
    if (payload?.[field] !== value) throw new Error(`${stage}_${field}_mismatch`);
  }
}

async function requestJson({ fetchImpl, origin, path, method = "GET", body, auth, idempotencyKey, headers = {} }) {
  const response = await fetchImpl(`${origin}${path}`, {
    method,
    headers: {
      ...(body ? { "content-type": "application/json" } : {}),
      ...(auth?.cookie ? { cookie: auth.cookie, "x-opl-csrf": auth.csrf } : {}),
      ...(idempotencyKey ? { "Idempotency-Key": idempotencyKey } : {}),
      ...headers
    },
    body: body ? JSON.stringify(body) : undefined
  });
  const payload = await response.json();
  if (!response.ok) throw new Error(`request_failed:${method}:${path}:${response.status}:${payload?.error || "unknown"}`);
  return { payload, response };
}

export async function verifyProductionExecutionChain({
  controlPlaneOrigin,
  fabricOrigin,
  ledgerOrigin,
  operatorToken,
  runId,
  fetchImpl = globalThis.fetch
}) {
  const controlPlane = normalizedOrigin(controlPlaneOrigin, "control_plane_origin");
  const fabric = normalizedOrigin(fabricOrigin, "fabric_origin");
  const ledger = normalizedOrigin(ledgerOrigin, "ledger_origin");
  required(operatorToken, "operator_token");
  if (!/^[A-Za-z0-9._:-]{1,80}$/.test(runId || "")) throw new Error("run_id_invalid");

  const organizationId = "org-production-verifier";
  const workspaceId = `workspace-${runId}`;
  const runnerId = `runner-${runId}`;
  const environmentRef = "environment-production-verifier";
  const digest = `sha256:${createHash("sha256").update(runId).digest("hex")}`;
  const key = (stage) => `production-execution:${runId}:${stage}`;

  const login = await requestJson({
    fetchImpl,
    origin: controlPlane,
    path: "/api/auth/operator-login",
    method: "POST",
    body: {},
    headers: { "x-opl-operator-token": operatorToken }
  });
  const auth = {
    cookie: required(login.response.headers.get("set-cookie")?.split(";", 1)[0], "session_cookie"),
    csrf: required(login.response.headers.get("x-opl-csrf-token") || login.payload.csrfToken, "csrf_token")
  };

  const project = (await requestJson({
    fetchImpl, origin: controlPlane, path: "/api/projects", method: "POST", auth, idempotencyKey: key("project"),
    body: { organizationId, workspaceId, localAliasId: `local-project-${runId}` }
  })).payload;
  assertFields(project, { organizationId, workspaceId }, "project");
  required(project.projectId, "project_id");

  const task = (await requestJson({
    fetchImpl, origin: controlPlane, path: `/api/projects/${encodeURIComponent(project.projectId)}/tasks`, method: "POST", auth, idempotencyKey: key("task"),
    body: { organizationId, workspaceId, localAliasId: `local-task-${runId}` }
  })).payload;
  assertFields(task, { organizationId, workspaceId, projectId: project.projectId }, "task");
  required(task.taskId, "task_id");

  const executionIdentity = { organizationId, workspaceId, projectId: project.projectId, taskId: task.taskId };
  const requested = (await requestJson({
    fetchImpl, origin: controlPlane, path: "/api/execution-requests", method: "POST", auth, idempotencyKey: key("request"),
    body: { ...executionIdentity, environmentRef }
  })).payload;
  assertFields(requested, executionIdentity, "request");
  required(requested.requestId, "request_id");

  const approved = (await requestJson({
    fetchImpl, origin: controlPlane, path: `/api/execution-requests/${encodeURIComponent(requested.requestId)}/approve`, method: "POST", auth, idempotencyKey: key("approve"), body: {}
  })).payload;
  assertFields(approved, { ...executionIdentity, requestId: requested.requestId, status: "approved" }, "approval");
  required(approved.approvalId, "approval_id");

  const executed = (await requestJson({
    fetchImpl, origin: controlPlane, path: `/api/execution-requests/${encodeURIComponent(requested.requestId)}/execute`, method: "POST", auth, idempotencyKey: key("execute"), body: {}
  })).payload;
  assertFields(executed, { ...executionIdentity, requestId: requested.requestId, approvalId: approved.approvalId }, "execution");
  required(executed.jobId, "job_id");
  required(executed.receiptId, "running_receipt_id");

  const claimed = (await requestJson({
    fetchImpl, origin: fabric, path: `/fabric/jobs/${encodeURIComponent(executed.jobId)}/claim`, method: "POST", idempotencyKey: key("claim"), body: { runnerId }
  })).payload;
  assertFields(claimed, { ...executionIdentity, requestId: requested.requestId, approvalId: approved.approvalId, jobId: executed.jobId, status: "running" }, "claim");
  required(claimed.leaseToken, "lease_token");

  const artifact = (await requestJson({
    fetchImpl, origin: ledger, path: "/ledger/artifacts", method: "POST", idempotencyKey: key("artifact"),
    body: { ...executionIdentity, jobId: executed.jobId, digest, mediaType: "application/json", sizeBytes: Buffer.byteLength(runId), storageRef: `artifact:${runId}` }
  })).payload;
  assertFields(artifact, { ...executionIdentity, jobId: executed.jobId, digest }, "artifact");
  required(artifact.artifactId, "artifact_id");

  const review = (await requestJson({
    fetchImpl, origin: ledger, path: "/ledger/reviews", method: "POST", idempotencyKey: key("review"),
    body: {
      ...executionIdentity,
      jobId: executed.jobId,
      reviewerRef: "production-verifier",
      reviewerVersion: "1",
      inputArtifactDigests: [digest],
      checks: { productionExecutionChain: "passed" },
      decision: "accepted"
    }
  })).payload;
  assertFields(review, { ...executionIdentity, jobId: executed.jobId, decision: "accepted" }, "review");
  required(review.reviewId, "review_id");

  const completed = (await requestJson({
    fetchImpl, origin: fabric, path: `/fabric/jobs/${encodeURIComponent(executed.jobId)}/complete`, method: "POST", idempotencyKey: key("complete"),
    body: { runnerId, leaseToken: claimed.leaseToken, artifactIds: [artifact.artifactId], reviewIds: [review.reviewId] }
  })).payload;
  assertFields(completed, { ...executionIdentity, jobId: executed.jobId, status: "succeeded" }, "complete");

  const synced = (await requestJson({
    fetchImpl, origin: controlPlane, path: `/api/execution-requests/${encodeURIComponent(requested.requestId)}/sync`, method: "POST", auth, idempotencyKey: key("sync"), body: {}
  })).payload;
  assertFields(synced, { ...executionIdentity, requestId: requested.requestId, approvalId: approved.approvalId, jobId: executed.jobId, status: "completed" }, "sync");
  required(synced.receiptId, "receipt_id");
  required(synced.continuationId, "continuation_id");

  const receipt = (await requestJson({ fetchImpl, origin: ledger, path: `/ledger/receipts/${encodeURIComponent(synced.receiptId)}` })).payload;
  assertFields(receipt, { ...executionIdentity, requestId: requested.requestId, approvalId: approved.approvalId, jobId: executed.jobId, artifactId: artifact.artifactId, reviewId: review.reviewId, receiptId: synced.receiptId, continuationId: synced.continuationId, status: "completed" }, "receipt");

  const continuationPath = `/api/execution-requests/${encodeURIComponent(requested.requestId)}/continuation`;
  const controlPlaneContinuation = (await requestJson({ fetchImpl, origin: controlPlane, path: continuationPath, auth })).payload;
  const ledgerContinuation = (await requestJson({ fetchImpl, origin: ledger, path: `/ledger/receipts/${encodeURIComponent(synced.receiptId)}/continuation` })).payload;
  const continuationIdentity = { projectId: project.projectId, taskId: task.taskId, receiptId: synced.receiptId, continuationId: synced.continuationId };
  assertFields(controlPlaneContinuation, continuationIdentity, "control_plane_continuation");
  assertFields(ledgerContinuation, continuationIdentity, "ledger_continuation");

  return {
    ok: true,
    runId,
    status: synced.status,
    projectId: project.projectId,
    taskId: task.taskId,
    requestId: requested.requestId,
    approvalId: approved.approvalId,
    jobId: executed.jobId,
    artifactId: artifact.artifactId,
    reviewId: review.reviewId,
    receiptId: synced.receiptId,
    continuationId: synced.continuationId
  };
}

export async function runProductionExecutionVerifierCli({ env = process.env, stdout = process.stdout, stderr = process.stderr, fetchImpl = globalThis.fetch } = {}) {
  try {
    const result = await verifyProductionExecutionChain({
      controlPlaneOrigin: env.OPL_EXECUTION_CONTROL_PLANE_ORIGIN,
      fabricOrigin: env.OPL_EXECUTION_FABRIC_ORIGIN,
      ledgerOrigin: env.OPL_EXECUTION_LEDGER_ORIGIN,
      operatorToken: env.OPL_EXECUTION_OPERATOR_TOKEN,
      runId: env.OPL_EXECUTION_RUN_ID,
      fetchImpl
    });
    stdout.write(`${JSON.stringify(result, null, 2)}\n`);
    return 0;
  } catch (error) {
    stderr.write(`${JSON.stringify({ ok: false, error: error?.message || String(error) }, null, 2)}\n`);
    return 1;
  }
}

if (import.meta.url === `file://${process.argv[1]}`) {
  runProductionExecutionVerifierCli().then((code) => {
    process.exitCode = code;
  });
}
