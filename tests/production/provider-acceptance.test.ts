import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { parse } from "yaml";

import {
  PROVIDER_ACCEPTANCE_CONFIRMATION,
  runProviderAcceptance,
  runProviderAcceptanceCli
} from "../../tools/provider-acceptance.ts";

const operatorToken = "operator-summary-token";

function json(payload, status = 200, headers = {}) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "content-type": "application/json", ...headers }
  });
}

test("Provider Acceptance replays one fixed operator operation until the slot is ready", async () => {
  const calls = [];
  let attempts = 0;
  const fetchImpl = async (input, init = {}) => {
    const url = new URL(input);
    const headers = new Headers(init.headers);
    calls.push({ path: url.pathname, method: init.method || "GET", headers, body: init.body && JSON.parse(init.body) });
    if (url.pathname === "/api/auth/operator-login") {
      assert.equal(headers.get("x-opl-operator-token"), operatorToken);
      return json({ isOperator: true, user: { id: "usr-operator", role: "admin", accountId: "acct-operator" } }, 200, {
        "set-cookie": "opl_session=operator-session; Path=/; HttpOnly",
        "x-opl-csrf-token": "csrf-operator"
      });
    }
    if (url.pathname === "/api/operator/provider-acceptance") {
      attempts += 1;
      return json({
        ok: true,
        status: attempts === 1 ? "in_progress" : "reused",
        slot: { id: "verification-slot-01", accountId: "acct-verification-slot-01" }
      });
    }
    return json({ error: "not_found" }, 404);
  };

  const result = await runProviderAcceptance({
    origin: "https://cloud.medopl.cn",
    operatorToken,
    accountId: "acct-verification-slot-01",
    confirmation: PROVIDER_ACCEPTANCE_CONFIRMATION,
    attempts: 2,
    retryDelayMs: 0,
    fetchImpl
  });

  assert.equal(result.status, "reused");
  assert.deepEqual(calls.map((call) => [call.method, call.path]), [
    ["POST", "/api/auth/operator-login"],
    ["POST", "/api/operator/provider-acceptance"],
    ["POST", "/api/operator/provider-acceptance"]
  ]);
  for (const call of calls.slice(1)) {
    assert.deepEqual(call.body, {
      accountId: "acct-verification-slot-01",
      confirmation: PROVIDER_ACCEPTANCE_CONFIRMATION,
      slotId: "verification-slot-01"
    });
    assert.equal(call.headers.get("x-opl-csrf"), "csrf-operator");
    assert.equal(call.headers.get("idempotency-key"), "provider-acceptance:verification-slot-01");
  }
  assert.doesNotMatch(JSON.stringify(result), /operator-summary-token|operator-session|csrf-operator/);
});

test("Provider Acceptance rejects missing authority before network access and stops on manual review", async () => {
  let calls = 0;
  await assert.rejects(() => runProviderAcceptance({
    origin: "https://cloud.medopl.cn",
    operatorToken,
    accountId: "acct-verification-slot-01",
    confirmation: "yes",
    fetchImpl: async () => { calls += 1; return json({}); }
  }), /provider_acceptance_confirmation_required/);
  assert.equal(calls, 0);

  const fetchImpl = async (input, init = {}) => {
    calls += 1;
    const url = new URL(input);
    if (url.pathname === "/api/auth/operator-login") {
      assert.equal(new Headers(init.headers).get("x-opl-operator-token"), operatorToken);
      return json({ isOperator: true, user: { id: "usr-operator", role: "admin", accountId: "acct-operator" } }, 200, {
        "set-cookie": "opl_session=operator-session; Path=/; HttpOnly",
        "x-opl-csrf-token": "csrf-operator"
      });
    }
    assert.equal(init.method, "POST");
    return json({ ok: false, status: "manual_review", reason: "provider_result_unknown" });
  };
  await assert.rejects(() => runProviderAcceptance({
    origin: "https://cloud.medopl.cn",
    operatorToken,
    accountId: "acct-verification-slot-01",
    confirmation: PROVIDER_ACCEPTANCE_CONFIRMATION,
    attempts: 5,
    retryDelayMs: 0,
    fetchImpl
  }), /provider_acceptance_manual_review/);
  assert.equal(calls, 2);
});

test("Provider Acceptance workflow is manual, fixed, and cannot mutate resources directly", async () => {
  const workflow = parse(await readFile(".github/workflows/provider-acceptance.yml", "utf8"));
  const contract = JSON.parse(await readFile("packages/contracts/opl-cloud-deployment-contract.json", "utf8"));
  const backend = await readFile("services/control-plane/internal/server/routes_provider_acceptance.go", "utf8");
  const spec = contract.providerAcceptanceWorkflow;
  const job = workflow.jobs.accept;
  const runStep = job.steps.find((step) => step.name === "Run one-time Provider Acceptance");
  const source = JSON.stringify(workflow);

  assert.equal(spec.file, ".github/workflows/provider-acceptance.yml");
  assert.equal(spec.job, "accept");
  assert.equal(spec.mode, "operator_only_one_time_fixed_slot");
  assert.equal(spec.endpoint, "/api/operator/provider-acceptance");
  assert.equal(spec.fixedAccountId, "acct-verification-slot-01");
  assert.equal(spec.fixedSlotId, "verification-slot-01");
  assert.equal(spec.idempotencyKey, "provider-acceptance:verification-slot-01");
  assert.equal(spec.confirmation, PROVIDER_ACCEPTANCE_CONFIRMATION);
  assert.deepEqual(spec.resourceShape, {
    instanceType: "SA5.MEDIUM4",
    cbsGb: 10,
    chargeType: "PREPAID",
    periodMonths: 1,
    renewFlag: "NOTIFY_AND_MANUAL_RENEW"
  });
  assert.equal(workflow.concurrency.group, "provider-acceptance-verification-slot-01");
  assert.equal(workflow.concurrency["cancel-in-progress"], false);
  assert.equal(workflow.on.workflow_dispatch.inputs.account_id.required, true);
  assert.equal(workflow.on.workflow_dispatch.inputs.confirmation.required, true);
  assert.equal(job.environment, "production");
  assert.equal(job.env.OPL_PROVIDER_ACCEPTANCE_ACCOUNT_ID, "${{ inputs.account_id }}");
  assert.equal(job.env.OPL_PROVIDER_ACCEPTANCE_CONFIRMATION, "${{ inputs.confirmation }}");
  assert.equal(job.env.OPL_PROVIDER_ACCEPTANCE_OPERATOR_TOKEN, undefined);
  assert.equal(runStep.env.OPL_PROVIDER_ACCEPTANCE_OPERATOR_TOKEN, "${{ secrets.OPL_OPERATOR_SUMMARY_TOKEN }}");
  assert.equal(job.env.OPL_PROVIDER_ACCEPTANCE_AUTH_USERS_JSON, undefined);
  assert.ok(spec.requiredEnv.includes("OPL_PROVIDER_ACCEPTANCE_OPERATOR_TOKEN"));
  assert.deepEqual(spec.secretEnv, ["OPL_PROVIDER_ACCEPTANCE_OPERATOR_TOKEN"]);
  assert.match(source, /node tools\/provider-acceptance\.ts/);
  assert.doesNotMatch(source, /TENCENTCLOUD_SECRET|compute-allocations|storage-volumes|destroy|delete|renew/i);
  assert.match(backend, /POST \/api\/operator\/provider-acceptance/);
  assert.match(backend, /provider-acceptance:" \+ providerAcceptanceSlotID/);
});

test("Provider Acceptance CLI requires the fixed confirmation before network access", async () => {
  let calls = 0;
  let stderr = "";
  const code = await runProviderAcceptanceCli({
    env: {},
    stdout: { write: () => {} },
    stderr: { write: (chunk) => { stderr += chunk; } },
    fetchImpl: async () => { calls += 1; return json({}); }
  });
  assert.equal(code, 1);
  assert.match(stderr, /provider_acceptance_confirmation_required/);
  assert.equal(calls, 0);
});
