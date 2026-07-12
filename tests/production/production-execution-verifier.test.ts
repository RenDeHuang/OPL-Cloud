import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import test from "node:test";

import { verifyProductionExecutionChain } from "../../tools/production-execution-verifier.ts";

function jsonResponse(payload, { status = 200, headers = {} } = {}) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "content-type": "application/json", ...headers }
  });
}

test("production execution verifier proves the complete cross-service evidence chain", async () => {
  const runId = "train4-test";
  const digest = `sha256:${createHash("sha256").update(runId).digest("hex")}`;
  const identity = {
    organizationId: "org-operator-test",
    workspaceId: `workspace-${runId}`,
    projectId: "project-test",
    taskId: "task-test",
    requestId: "request-test",
    approvalId: "approval-test",
    jobId: "job-test"
  };
  const responses = [
    jsonResponse({ csrfToken: "csrf-token", user: { id: "usr-operator", accountId: "acct-operator" } }, { headers: { "set-cookie": "opl_session=session; Path=/", "x-opl-csrf-token": "csrf-token" } }),
    jsonResponse({
      organizations: [{ id: identity.organizationId, billingAccountId: "acct-operator", status: "active" }],
      memberships: [{ organizationId: identity.organizationId, userId: "usr-operator", accountId: "acct-operator", status: "active" }]
    }),
    jsonResponse({ ...identity, taskId: undefined, requestId: undefined, approvalId: undefined, jobId: undefined }, { status: 201 }),
    jsonResponse({ ...identity, requestId: undefined, approvalId: undefined, jobId: undefined }, { status: 201 }),
    jsonResponse({ ...identity, approvalId: "", jobId: undefined }, { status: 201 }),
    jsonResponse({ ...identity, jobId: undefined, status: "approved" }),
    jsonResponse({ ...identity, receiptId: "receipt-running", continuationId: "continuation-running", status: "queued" }, { status: 202 }),
    jsonResponse({ ...identity, leaseToken: "lease-token", status: "running" }, { status: 202 }),
    jsonResponse({ ...identity, artifactId: "artifact-test", digest }, { status: 201 }),
    jsonResponse({ ...identity, reviewId: "review-test", decision: "accepted" }, { status: 201 }),
    jsonResponse({ ...identity, artifactIds: ["artifact-test"], reviewIds: ["review-test"], status: "succeeded" }, { status: 202 }),
    jsonResponse({ ...identity, receiptId: "receipt-final", continuationId: "continuation-final", status: "completed" }),
    jsonResponse({ ...identity, artifactId: "artifact-test", reviewId: "review-test", receiptId: "receipt-final", continuationId: "continuation-final", status: "completed" }),
    jsonResponse({ ...identity, receiptId: "receipt-final", continuationId: "continuation-final" }),
    jsonResponse({ ...identity, receiptId: "receipt-final", continuationId: "continuation-final" })
  ];
  const requests = [];
  const fetchImpl = async (url, init = {}) => {
    const parsed = new URL(url);
    const headers = Object.fromEntries(new Headers(init.headers).entries());
    requests.push({ method: init.method || "GET", origin: parsed.origin, path: parsed.pathname, headers, body: init.body ? JSON.parse(init.body) : null });
    const response = responses.shift();
    assert.ok(response, `unexpected request ${init.method || "GET"} ${parsed.pathname}`);
    return response;
  };

  const result = await verifyProductionExecutionChain({
    controlPlaneOrigin: "http://control-plane.test",
    fabricOrigin: "http://fabric.test",
    ledgerOrigin: "http://ledger.test",
    operatorToken: "operator-token",
    internalServiceToken: "internal-secret",
    runId,
    fetchImpl
  });

  assert.equal(result.ok, true);
  assert.equal(result.status, "completed");
  assert.equal(result.receiptId, "receipt-final");
  assert.equal(result.continuationId, "continuation-final");
  assert.equal(responses.length, 0);
  assert.deepEqual(requests.map(({ method, origin, path }) => `${method} ${origin}${path}`), [
    "POST http://control-plane.test/api/auth/operator-login",
    "GET http://control-plane.test/api/management/state",
    "POST http://control-plane.test/api/projects",
    "POST http://control-plane.test/api/projects/project-test/tasks",
    "POST http://control-plane.test/api/execution-requests",
    "POST http://control-plane.test/api/execution-requests/request-test/approve",
    "POST http://control-plane.test/api/execution-requests/request-test/execute",
    "POST http://fabric.test/fabric/jobs/job-test/claim",
    "POST http://ledger.test/ledger/artifacts",
    "POST http://ledger.test/ledger/reviews",
    "POST http://fabric.test/fabric/jobs/job-test/complete",
    "POST http://control-plane.test/api/execution-requests/request-test/sync",
    "GET http://ledger.test/ledger/receipts/receipt-final",
    "GET http://control-plane.test/api/execution-requests/request-test/continuation",
    "GET http://ledger.test/ledger/receipts/receipt-final/continuation"
  ]);
  assert.equal(requests[0].headers["x-opl-operator-token"], "operator-token");
  assert.equal(requests.find(({ path }) => path === "/api/projects").body.organizationId, identity.organizationId);
  assert.ok(requests.filter(({ origin }) => origin === "http://control-plane.test").slice(1).every(({ headers }) => headers.cookie === "opl_session=session" && headers["x-opl-csrf"] === "csrf-token"));
  assert.ok(requests.filter(({ origin }) => origin !== "http://control-plane.test").every(({ headers }) => headers.authorization === "Bearer internal-secret"));
  assert.ok(requests.filter(({ origin }) => origin === "http://control-plane.test").every(({ headers }) => headers.authorization === undefined));
  assert.ok(requests.filter(({ method }) => method === "POST").slice(1).every(({ headers }) => headers["idempotency-key"]));
  const completion = requests.find(({ path }) => path === "/fabric/jobs/job-test/complete");
  assert.deepEqual(completion.body.artifactIds, ["artifact-test"]);
  assert.deepEqual(completion.body.reviewIds, ["review-test"]);
});
