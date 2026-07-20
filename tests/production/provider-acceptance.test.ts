import assert from "node:assert/strict";
import { access, mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { parse } from "yaml";

import {
  PROVIDER_ACCEPTANCE_CONFIRMATION,
  PROVIDER_ACCEPTANCE_SLOTS,
  runProviderAcceptance,
  runProviderAcceptanceCli
} from "../../tools/provider-acceptance.ts";

const acceptanceToken = "provider-acceptance-token";
const approvalId = "approval-pilot-v2";

function acceptanceAuthority(slotId, accountId) {
  return {
    gatewayWriteAllowed: true,
    providerWriteAllowed: true,
    mutationApprovalId: approvalId,
    mutationApprovalJson: JSON.stringify({
      approvalId,
      expiresAt: "2099-07-19T00:00:00Z",
      accountIds: [accountId],
      workspaceIds: [`primary:${accountId}`],
      resourceIds: [slotId]
    })
  };
}

function json(payload, status = 200, headers = {}) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "content-type": "application/json", ...headers }
  });
}

function acceptedSlotPayload(slotId, accountId, overrides = {}) {
  return {
    ok: true,
    status: "reused",
    slot: {
      id: slotId,
      accountId,
      workspaceId: `ws-${slotId}`,
      workspaceUrl: `https://workspace.medopl.cn/w/ws-${slotId}/`,
      computeAllocationId: `ca-${slotId}`,
      computeProviderId: `ins-${slotId}`,
      nodePoolId: `np-${slotId}`,
      storageId: `vol-${slotId}`,
      storageProviderId: `disk-${slotId}`,
      persistentVolumeId: `pv-${slotId}`,
      attachmentId: `att-${slotId}`,
      ...overrides
    }
  };
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
      return json(attempts === 1
        ? { ...acceptedSlotPayload(slotId, slot.accountId), ok: false, status: "in_progress" }
        : acceptedSlotPayload(slotId, slot.accountId));
    };

    const result = await runProviderAcceptance({
      origin: "https://cloud.medopl.cn", acceptanceToken, slotId, accountId: slot.accountId,
      confirmation: PROVIDER_ACCEPTANCE_CONFIRMATION, environmentApproved: true, purchaseBudget: 1,
      maxApprovedProviderCost: 100, attempts: 2, retryDelayMs: 0, fetchImpl,
      ...acceptanceAuthority(slotId, slot.accountId)
    });

    assert.equal(result.status, "reused");
    assert.equal(calls.length, 2);
    for (const call of calls) {
      assert.deepEqual(call.body, {
        accountId: slot.accountId, confirmation: PROVIDER_ACCEPTANCE_CONFIRMATION, slotId,
        environmentApproved: true, purchaseBudget: 1, maxApprovedProviderCost: 100
      });
      assert.equal(call.headers.get("x-opl-provider-acceptance-token"), acceptanceToken);
      assert.equal(call.headers.get("x-opl-operator-token"), null);
      assert.equal(call.headers.get("idempotency-key"), slot.idempotencyKey);
    }
    assert.doesNotMatch(JSON.stringify(result), /provider-acceptance-token/);
  }
});

test("Provider Acceptance rejects missing authority before network access and stops on manual review", async () => {
  let calls = 0;
  await assert.rejects(() => runProviderAcceptance({
    origin: "https://cloud.medopl.cn",
    acceptanceToken,
    slotId: "verification-slot-basic-01",
    accountId: "acct-verification-slot-basic-01",
    confirmation: "yes",
    fetchImpl: async () => { calls += 1; return json({}); }
  }), /provider_acceptance_confirmation_required/);
  assert.equal(calls, 0);

  const fetchImpl = async (input, init = {}) => {
    calls += 1;
    assert.equal(init.method, "POST");
	assert.equal(new Headers(init.headers).get("x-opl-provider-acceptance-token"), acceptanceToken);
    return json({
      ...acceptedSlotPayload("verification-slot-basic-01", "acct-verification-slot-basic-01"),
      ok: false,
      status: "manual_review",
      reason: "provider_acceptance_storage_result_unknown"
    });
  };
  const directory = await mkdtemp(join(tmpdir(), "opl-provider-acceptance-manual-review-"));
  const manifestPath = join(directory, "manifest.json");
  await assert.rejects(() => runProviderAcceptance({
    origin: "https://cloud.medopl.cn",
    acceptanceToken,
    slotId: "verification-slot-basic-01",
    accountId: "acct-verification-slot-basic-01",
    confirmation: PROVIDER_ACCEPTANCE_CONFIRMATION,
    environmentApproved: true,
    purchaseBudget: 1,
    maxApprovedProviderCost: 100,
    attempts: 5,
    retryDelayMs: 0,
    manifestPath,
    fetchImpl,
    ...acceptanceAuthority("verification-slot-basic-01", "acct-verification-slot-basic-01")
  }), /provider_acceptance_manual_review/);
  assert.equal(calls, 1);
  const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
  assert.equal(manifest.status, "manual_review");
  assert.equal(manifest.reason, "provider_acceptance_storage_result_unknown");
  await rm(directory, { recursive: true, force: true });
});

test("Provider Acceptance validates every successful response fact before writing evidence", async () => {
  const slotId = "verification-slot-basic-01";
  const accountId = PROVIDER_ACCEPTANCE_SLOTS[slotId].accountId;
  const requiredIds = [
    "workspaceId", "workspaceUrl", "computeAllocationId", "computeProviderId", "nodePoolId", "storageId",
    "storageProviderId", "persistentVolumeId", "attachmentId"
  ];
  const invalidPayloads = [
    { ...acceptedSlotPayload(slotId, accountId), ok: false },
    acceptedSlotPayload("verification-slot-pro-01", accountId),
    acceptedSlotPayload(slotId, "acct-wrong"),
    ...requiredIds.map((field) => acceptedSlotPayload(slotId, accountId, { [field]: "" }))
  ];
  const directory = await mkdtemp(join(tmpdir(), "opl-provider-acceptance-"));
  const manifestPath = join(directory, "manifest.json");

  for (const payload of invalidPayloads) {
    await assert.rejects(() => runProviderAcceptance({
      origin: "https://cloud.medopl.cn", acceptanceToken, slotId, accountId,
      confirmation: PROVIDER_ACCEPTANCE_CONFIRMATION, environmentApproved: true, purchaseBudget: 1,
      maxApprovedProviderCost: 100, attempts: 1, retryDelayMs: 0, manifestPath,
      fetchImpl: async () => json(payload),
      ...acceptanceAuthority(slotId, accountId)
    }), /provider_acceptance_invalid_response/);
    await assert.rejects(access(manifestPath), { code: "ENOENT" });
  }
  await rm(directory, { recursive: true, force: true });
});

test("Provider Acceptance CLI rejects unknown response fields without writing evidence", async (t) => {
  const slotId = "verification-slot-basic-01";
  const accountId = PROVIDER_ACCEPTANCE_SLOTS[slotId].accountId;
  const cases = [
    ["top-level authorization", { ...acceptedSlotPayload(slotId, accountId), authorization: "opaque-authorization-field" }, "opaque-authorization-field"],
    ["top-level unexpected", { ...acceptedSlotPayload(slotId, accountId), unexpected: "opaque-top-level-field" }, "opaque-top-level-field"],
    ["slot unexpected", acceptedSlotPayload(slotId, accountId, { unexpected: "opaque-slot-field" }), "opaque-slot-field"]
  ];

  for (const [name, payload, marker] of cases) {
    await t.test(name, async () => {
      const directory = await mkdtemp(join(tmpdir(), "opl-provider-acceptance-extra-"));
      const manifestPath = join(directory, "manifest.json");
      let stdout = "";
      let stderr = "";
      const code = await runProviderAcceptanceCli({
        argv: ["--allow-gateway-write", "--allow-provider-write", "--approval-id", approvalId],
        env: {
          OPL_CONSOLE_ORIGIN: "https://cloud.medopl.cn",
          OPL_PROVIDER_ACCEPTANCE_TOKEN: acceptanceToken,
          OPL_PROVIDER_ACCEPTANCE_SLOT_ID: slotId,
          OPL_PROVIDER_ACCEPTANCE_ACCOUNT_ID: accountId,
          OPL_PROVIDER_ACCEPTANCE_CONFIRMATION: PROVIDER_ACCEPTANCE_CONFIRMATION,
          OPL_PROVIDER_ACCEPTANCE_ENVIRONMENT_APPROVED: "true",
          OPL_PROVIDER_ACCEPTANCE_PURCHASE_BUDGET: "1",
          OPL_PROVIDER_ACCEPTANCE_MAX_APPROVED_PROVIDER_COST: "100",
          OPL_PROVIDER_ACCEPTANCE_ATTEMPTS: "1",
          OPL_PROVIDER_ACCEPTANCE_RETRY_DELAY_MS: "0",
          OPL_PROVIDER_ACCEPTANCE_MANIFEST_PATH: manifestPath,
          OPL_VERIFY_MUTATION_APPROVAL_JSON: acceptanceAuthority(slotId, accountId).mutationApprovalJson
        },
        stdout: { write: (chunk) => { stdout += chunk; } },
        stderr: { write: (chunk) => { stderr += chunk; } },
        fetchImpl: async () => json(payload)
      });
      assert.equal(code, 1);
      assert.equal(stdout, "");
      assert.match(stderr, /provider_acceptance_invalid_response/);
      assert.doesNotMatch(stdout, new RegExp(marker));
      await assert.rejects(access(manifestPath), { code: "ENOENT" });
      await rm(directory, { recursive: true, force: true });
    });
  }
});

test("Provider Acceptance CLI rejects unsupported manual-review reasons without writing evidence", async () => {
  const slotId = "verification-slot-basic-01";
  const accountId = PROVIDER_ACCEPTANCE_SLOTS[slotId].accountId;
  const directory = await mkdtemp(join(tmpdir(), "opl-provider-acceptance-reason-"));
  const manifestPath = join(directory, "manifest.json");
  let stdout = "";
  let stderr = "";
  const code = await runProviderAcceptanceCli({
    argv: ["--allow-gateway-write", "--allow-provider-write", "--approval-id", approvalId],
    env: {
      OPL_CONSOLE_ORIGIN: "https://cloud.medopl.cn",
      OPL_PROVIDER_ACCEPTANCE_TOKEN: acceptanceToken,
      OPL_PROVIDER_ACCEPTANCE_SLOT_ID: slotId,
      OPL_PROVIDER_ACCEPTANCE_ACCOUNT_ID: accountId,
      OPL_PROVIDER_ACCEPTANCE_CONFIRMATION: PROVIDER_ACCEPTANCE_CONFIRMATION,
      OPL_PROVIDER_ACCEPTANCE_ENVIRONMENT_APPROVED: "true",
      OPL_PROVIDER_ACCEPTANCE_PURCHASE_BUDGET: "1",
      OPL_PROVIDER_ACCEPTANCE_MAX_APPROVED_PROVIDER_COST: "100",
      OPL_PROVIDER_ACCEPTANCE_ATTEMPTS: "1",
      OPL_PROVIDER_ACCEPTANCE_RETRY_DELAY_MS: "0",
      OPL_PROVIDER_ACCEPTANCE_MANIFEST_PATH: manifestPath,
      OPL_VERIFY_MUTATION_APPROVAL_JSON: acceptanceAuthority(slotId, accountId).mutationApprovalJson
    },
    stdout: { write: (chunk) => { stdout += chunk; } },
    stderr: { write: (chunk) => { stderr += chunk; } },
    fetchImpl: async () => json({
      ...acceptedSlotPayload(slotId, accountId),
      ok: false,
      status: "manual_review",
      reason: "unexpected_manual_review_reason"
    })
  });
  assert.equal(code, 1);
  assert.equal(stdout, "");
  assert.match(stderr, /provider_acceptance_invalid_response/);
  await assert.rejects(access(manifestPath), { code: "ENOENT" });
  await rm(directory, { recursive: true, force: true });
});

test("Provider Acceptance workflow is independently approved, dual-slot fixed, and cannot mutate resources directly", async () => {
  const workflow = parse(await readFile(".github/workflows/provider-acceptance.yml", "utf8"));
  const contract = JSON.parse(await readFile("packages/contracts/opl-cloud-deployment-contract.json", "utf8"));
  const launch = JSON.parse(await readFile("packages/contracts/opl-cloud-launch-freeze-contract.json", "utf8"));
  const backend = await readFile("services/control-plane/internal/server/routes_provider_acceptance.go", "utf8");
  const spec = contract.providerAcceptanceWorkflow;
  const deploySpec = contract.deployWorkflow;
  const job = workflow.jobs.accept;
  const runStep = job.steps.find((step) => step.name === "Run one-time Provider Acceptance");
  const source = JSON.stringify(workflow);

  assert.equal(spec.file, ".github/workflows/provider-acceptance.yml");
  assert.equal(spec.job, "accept");
  assert.equal(spec.mode, "operator_only_one_time_dual_fixed_slot");
  assert.equal(spec.mutationAuthorityWiring, "dispatch_approval_id_protected_manifest_explicit_cli_allow_flags");
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
  assert.equal(workflow.on.workflow_dispatch.inputs.approval_id.required, true);
  assert.equal(job.environment, "production-provider-acceptance");
  assert.equal(job.env.OPL_PROVIDER_ACCEPTANCE_SLOT_ID, "${{ inputs.slot_id }}");
  assert.equal(job.env.OPL_PROVIDER_ACCEPTANCE_ACCOUNT_ID, "${{ inputs.account_id }}");
  assert.equal(job.env.OPL_PROVIDER_ACCEPTANCE_CONFIRMATION, "${{ inputs.confirmation }}");
  assert.equal(job.env.OPL_PROVIDER_ACCEPTANCE_ENVIRONMENT_APPROVED, "true");
  assert.equal(job.env.OPL_PROVIDER_ACCEPTANCE_PURCHASE_BUDGET, "${{ inputs.purchase_budget }}");
  assert.equal(job.env.OPL_PROVIDER_ACCEPTANCE_MAX_APPROVED_PROVIDER_COST, "${{ inputs.max_approved_provider_cost }}");
  assert.equal(job.env.OPL_PROVIDER_ACCEPTANCE_TOKEN, undefined);
  assert.equal(runStep.env.OPL_PROVIDER_ACCEPTANCE_TOKEN, "${{ secrets.OPL_PROVIDER_ACCEPTANCE_TOKEN }}");
  assert.equal(runStep.env.OPL_VERIFY_MUTATION_APPROVAL_JSON, "${{ secrets.OPL_VERIFY_MUTATION_APPROVAL_JSON }}");
  assert.equal(runStep.env.OPL_VERIFY_MUTATION_APPROVAL_ID, "${{ inputs.approval_id }}");
  assert.equal(runStep.env.OPL_PROVIDER_ACCEPTANCE_OPERATOR_TOKEN, undefined);
  assert.equal(job.env.OPL_PROVIDER_ACCEPTANCE_AUTH_USERS_JSON, undefined);
  assert.equal(spec.environment, "production-provider-acceptance");
  assert.ok(spec.requiredEnv.includes("OPL_PROVIDER_ACCEPTANCE_TOKEN"));
  assert.deepEqual(spec.secretEnv, ["OPL_PROVIDER_ACCEPTANCE_TOKEN", "OPL_VERIFY_MUTATION_APPROVAL_JSON"]);
  assert.ok(deploySpec.requiredEnv.includes("OPL_PROVIDER_ACCEPTANCE_TOKEN"));
  assert.ok(deploySpec.secretEnv.includes("OPL_PROVIDER_ACCEPTANCE_TOKEN"));
  assert.ok(deploySpec.requiredCommandsByStep["Install Kubernetes secrets"].includes('--from-file=OPL_PROVIDER_ACCEPTANCE_TOKEN="$secret_dir/provider-acceptance-token"'));
  assert.equal(launch.verification.providerAcceptance.approvalEnvironment, "production-provider-acceptance");
  assert.equal(launch.verification.providerAcceptance.credentialEnv, "OPL_PROVIDER_ACCEPTANCE_TOKEN");
  assert.equal(launch.verification.providerAcceptance.credentialHeader, "x-opl-provider-acceptance-token");
  assert.equal(launch.verification.providerAcceptance.operatorSessionAccepted, false);
  assert.equal(launch.verification.providerAcceptance.genericOperatorTokenAccepted, false);
  assert.match(runStep.run, /node tools\/provider-acceptance\.ts --allow-gateway-write --allow-provider-write --approval-id "\$OPL_VERIFY_MUTATION_APPROVAL_ID"/);
  assert.doesNotMatch(source, /TENCENTCLOUD_SECRET|compute-allocations|storage-volumes|destroy|delete|renew/i);
  assert.match(backend, /POST \/api\/operator\/provider-acceptance/);
  assert.match(backend, /providerAcceptanceSlots/);
});

test("ordinary production verification requires both fixed slots and has no Acceptance mutation", async () => {
  const workflow = parse(await readFile(".github/workflows/verify-production-chain.yml", "utf8"));
  const source = await readFile(".github/workflows/verify-production-chain.yml", "utf8");
  const inputs = workflow.on.workflow_dispatch.inputs;
  assert.equal(inputs.basic_account_id, undefined);
  assert.equal(inputs.pro_account_id, undefined);
  assert.deepEqual(workflow.jobs.verify.strategy.matrix.include.map(({ slot_id, account_id }) => ({ slot_id, account_id })), [
    { slot_id: "verification-slot-basic-01", account_id: "acct-verification-slot-basic-01" },
    { slot_id: "verification-slot-pro-01", account_id: "acct-verification-slot-pro-01" }
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

test("Provider Acceptance read-only evidence level requires no mutation authority", async () => {
  let stdout = "";
  let stderr = "";
  let calls = 0;
  const code = await runProviderAcceptanceCli({
    argv: ["--read-only"],
    env: {},
    stdout: { write: (chunk) => { stdout += chunk; } },
    stderr: { write: (chunk) => { stderr += chunk; } },
    fetchImpl: async () => { calls += 1; return json({}); }
  });
  assert.equal(code, 0, stderr);
  assert.equal(calls, 0);
  assert.deepEqual(JSON.parse(stdout), {
    ok: true,
    mode: "read-only",
    evidenceLevel: "read-only",
    writesPerformed: 0
  });

  stderr = "";
  const denied = await runProviderAcceptanceCli({
    argv: ["--allow-gateway-write", "--allow-provider-write", "--approval-id", "approval-pilot-v2"],
    env: {},
    stdout: { write: () => {} },
    stderr: { write: (chunk) => { stderr += chunk; } },
    fetchImpl: async () => { calls += 1; return json({}); }
  });
  assert.equal(denied, 1);
  assert.match(stderr, /provider_acceptance_approval_manifest_required/);
  assert.equal(calls, 0);
});
