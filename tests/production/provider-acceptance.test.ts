import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { parse } from "yaml";

import {
  PROVIDER_ACCEPTANCE_CONFIRMATION,
  PROVIDER_ACCEPTANCE_SLOTS,
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

test("Provider Acceptance replays each fixed Basic and Pro operation with separate authority", async () => {
  assert.deepEqual(PROVIDER_ACCEPTANCE_SLOTS, {
    "verification-slot-basic-01": { accountId: "acct-verification-slot-basic-01", idempotencyKey: "provider-acceptance:verification-slot-basic-01" },
    "verification-slot-pro-01": { accountId: "acct-verification-slot-pro-01", idempotencyKey: "provider-acceptance:verification-slot-pro-01" }
  });
  for (const [slotId, slot] of Object.entries(PROVIDER_ACCEPTANCE_SLOTS)) {
    const calls = [];
    let attempts = 0;
    const fetchImpl = async (input, init = {}) => {
      const url = new URL(input);
      const headers = new Headers(init.headers);
      calls.push({ path: url.pathname, method: init.method || "GET", headers, body: init.body && JSON.parse(init.body) });
      attempts += 1;
      return json({ ok: true, status: attempts === 1 ? "in_progress" : "reused", slot: { id: slotId, accountId: slot.accountId } });
    };

    const result = await runProviderAcceptance({
      origin: "https://cloud.medopl.cn", operatorToken, slotId, accountId: slot.accountId,
      confirmation: PROVIDER_ACCEPTANCE_CONFIRMATION, environmentApproved: true, purchaseBudget: 1,
      maxApprovedProviderCost: 100, attempts: 2, retryDelayMs: 0, fetchImpl
    });

    assert.equal(result.status, "reused");
    assert.equal(calls.length, 2);
    for (const call of calls) {
      assert.deepEqual(call.body, {
        accountId: slot.accountId, confirmation: PROVIDER_ACCEPTANCE_CONFIRMATION, slotId,
        environmentApproved: true, purchaseBudget: 1, maxApprovedProviderCost: 100
      });
      assert.equal(call.headers.get("x-opl-operator-token"), operatorToken);
      assert.equal(call.headers.get("idempotency-key"), slot.idempotencyKey);
    }
    assert.doesNotMatch(JSON.stringify(result), /operator-summary-token/);
  }
});

test("Provider Acceptance rejects missing authority before network access and stops on manual review", async () => {
  let calls = 0;
  await assert.rejects(() => runProviderAcceptance({
    origin: "https://cloud.medopl.cn",
    operatorToken,
    slotId: "verification-slot-basic-01",
    accountId: "acct-verification-slot-basic-01",
    confirmation: "yes",
    fetchImpl: async () => { calls += 1; return json({}); }
  }), /provider_acceptance_confirmation_required/);
  assert.equal(calls, 0);

  const fetchImpl = async (input, init = {}) => {
    calls += 1;
    assert.equal(init.method, "POST");
	assert.equal(new Headers(init.headers).get("x-opl-operator-token"), operatorToken);
    return json({ ok: false, status: "manual_review", reason: "provider_result_unknown" });
  };
  await assert.rejects(() => runProviderAcceptance({
    origin: "https://cloud.medopl.cn",
    operatorToken,
    slotId: "verification-slot-basic-01",
    accountId: "acct-verification-slot-basic-01",
    confirmation: PROVIDER_ACCEPTANCE_CONFIRMATION,
    environmentApproved: true,
    purchaseBudget: 1,
    maxApprovedProviderCost: 100,
    attempts: 5,
    retryDelayMs: 0,
    fetchImpl
  }), /provider_acceptance_manual_review/);
  assert.equal(calls, 1);
});

test("Provider Acceptance workflow is independently approved, dual-slot fixed, and cannot mutate resources directly", async () => {
  const workflow = parse(await readFile(".github/workflows/provider-acceptance.yml", "utf8"));
  const contract = JSON.parse(await readFile("packages/contracts/opl-cloud-deployment-contract.json", "utf8"));
  const backend = await readFile("services/control-plane/internal/server/routes_provider_acceptance.go", "utf8");
  const spec = contract.providerAcceptanceWorkflow;
  const job = workflow.jobs.accept;
  const runStep = job.steps.find((step) => step.name === "Run one-time Provider Acceptance");
  const source = JSON.stringify(workflow);

  assert.equal(spec.file, ".github/workflows/provider-acceptance.yml");
  assert.equal(spec.job, "accept");
  assert.equal(spec.mode, "operator_only_one_time_dual_fixed_slot");
  assert.equal(spec.endpoint, "/api/operator/provider-acceptance");
  assert.equal(spec.lifetimePurchaseBudget, 2);
  assert.deepEqual(spec.fixedSlots.map(({ id, accountId, idempotencyKey, packageId, instanceType, cbsGb }) => ({ id, accountId, idempotencyKey, packageId, instanceType, cbsGb })), [
    { id: "verification-slot-basic-01", accountId: "acct-verification-slot-basic-01", idempotencyKey: "provider-acceptance:verification-slot-basic-01", packageId: "basic", instanceType: "SA5.MEDIUM4", cbsGb: 10 },
    { id: "verification-slot-pro-01", accountId: "acct-verification-slot-pro-01", idempotencyKey: "provider-acceptance:verification-slot-pro-01", packageId: "pro", instanceType: "SA5.2XLARGE16", cbsGb: 100 }
  ]);
  assert.equal(spec.confirmation, PROVIDER_ACCEPTANCE_CONFIRMATION);
  assert.equal(workflow.concurrency.group, "provider-acceptance-${{ inputs.slot_id }}");
  assert.equal(workflow.concurrency["cancel-in-progress"], false);
  assert.deepEqual(workflow.on.workflow_dispatch.inputs.slot_id.options, ["verification-slot-basic-01", "verification-slot-pro-01"]);
  assert.equal(workflow.on.workflow_dispatch.inputs.account_id.required, true);
  assert.equal(workflow.on.workflow_dispatch.inputs.confirmation.required, true);
  assert.equal(workflow.on.workflow_dispatch.inputs.purchase_budget.required, true);
  assert.equal(workflow.on.workflow_dispatch.inputs.max_approved_provider_cost.required, true);
  assert.equal(job.environment, "production");
  assert.equal(job.env.OPL_PROVIDER_ACCEPTANCE_SLOT_ID, "${{ inputs.slot_id }}");
  assert.equal(job.env.OPL_PROVIDER_ACCEPTANCE_ACCOUNT_ID, "${{ inputs.account_id }}");
  assert.equal(job.env.OPL_PROVIDER_ACCEPTANCE_CONFIRMATION, "${{ inputs.confirmation }}");
  assert.equal(job.env.OPL_PROVIDER_ACCEPTANCE_ENVIRONMENT_APPROVED, "true");
  assert.equal(job.env.OPL_PROVIDER_ACCEPTANCE_PURCHASE_BUDGET, "${{ inputs.purchase_budget }}");
  assert.equal(job.env.OPL_PROVIDER_ACCEPTANCE_MAX_APPROVED_PROVIDER_COST, "${{ inputs.max_approved_provider_cost }}");
  assert.equal(job.env.OPL_PROVIDER_ACCEPTANCE_OPERATOR_TOKEN, undefined);
  assert.equal(runStep.env.OPL_PROVIDER_ACCEPTANCE_OPERATOR_TOKEN, "${{ secrets.OPL_OPERATOR_SUMMARY_TOKEN }}");
  assert.equal(job.env.OPL_PROVIDER_ACCEPTANCE_AUTH_USERS_JSON, undefined);
  assert.ok(spec.requiredEnv.includes("OPL_PROVIDER_ACCEPTANCE_OPERATOR_TOKEN"));
  assert.deepEqual(spec.secretEnv, ["OPL_PROVIDER_ACCEPTANCE_OPERATOR_TOKEN"]);
  assert.match(source, /node tools\/provider-acceptance\.ts/);
  assert.doesNotMatch(source, /TENCENTCLOUD_SECRET|compute-allocations|storage-volumes|destroy|delete|renew/i);
  assert.match(backend, /POST \/api\/operator\/provider-acceptance/);
  assert.match(backend, /providerAcceptanceSlots/);
});

test("ordinary production verification requires both fixed slots and has no Acceptance mutation", async () => {
  const workflow = parse(await readFile(".github/workflows/verify-production-chain.yml", "utf8"));
  const source = await readFile(".github/workflows/verify-production-chain.yml", "utf8");
  const inputs = workflow.on.workflow_dispatch.inputs;
  assert.equal(inputs.basic_account_id.required, true);
  assert.equal(inputs.pro_account_id.required, true);
  assert.deepEqual(workflow.jobs.verify.strategy.matrix.include.map(({ slot_id, account_id }) => ({ slot_id, account_id })), [
    { slot_id: "verification-slot-basic-01", account_id: "${{ inputs.basic_account_id }}" },
    { slot_id: "verification-slot-pro-01", account_id: "${{ inputs.pro_account_id }}" }
  ]);
  assert.doesNotMatch(source, /provider-acceptance\.ts|\/api\/operator\/provider-acceptance|compute-allocations|storage-volumes|destroy|delete/i);
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
