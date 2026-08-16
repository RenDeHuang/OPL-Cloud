import assert from "node:assert/strict";
import { access, mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  PROVIDER_ACCEPTANCE_CONFIRMATION,
  PROVIDER_ACCEPTANCE_SLOTS,
  runProviderAcceptance,
  runProviderAcceptanceCli
} from "../../tools/provider-acceptance.ts";

const acceptanceToken = "provider-acceptance-token";
const approvalId = "approval-production-verification";

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

function acceptedSlotPayloadWithStatus(slotId, accountId, status, overrides = {}) {
  const payload = acceptedSlotPayload(slotId, accountId, overrides);
  for (const key of ["nodePoolId", "persistentVolumeId"]) {
    if (!Object.hasOwn(overrides, key)) delete payload.slot[key];
  }
  return { ...payload, ok: status === "ready" || status === "reused", status };
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

test("Provider Acceptance accepts ready and reused facts without legacy slot projections", async () => {
  const slotId = "verification-slot-basic-01";
  const accountId = PROVIDER_ACCEPTANCE_SLOTS[slotId].accountId;
  const directory = await mkdtemp(join(tmpdir(), "opl-provider-acceptance-legacy-cutover-"));
  const manifestPath = join(directory, "manifest.json");

  for (const status of ["ready", "reused"]) {
    for (const legacyProjection of [
      { nodePoolId: "np-compat", persistentVolumeId: "pv-compat" },
      { nodePoolId: "", persistentVolumeId: "" },
      { }
    ]) {
      const result = await runProviderAcceptance({
        origin: "https://cloud.medopl.cn", acceptanceToken, slotId, accountId,
        confirmation: PROVIDER_ACCEPTANCE_CONFIRMATION, environmentApproved: true, purchaseBudget: 1,
        maxApprovedProviderCost: 100, attempts: 1, retryDelayMs: 0, manifestPath,
        fetchImpl: async () => json(acceptedSlotPayloadWithStatus(slotId, accountId, status, legacyProjection)),
        ...acceptanceAuthority(slotId, accountId)
      });
      assert.equal(result.status, status);
      assert.equal(result.slot.computeProviderId, `ins-${slotId}`);
      assert.equal(result.slot.storageProviderId, `disk-${slotId}`);
      assert.equal(result.slot.nodePoolId, legacyProjection.nodePoolId ?? "");
      assert.equal(result.slot.persistentVolumeId, legacyProjection.persistentVolumeId ?? "");
      await rm(manifestPath, { force: true });
    }
  }
  await rm(directory, { recursive: true, force: true });
});

test("Provider Acceptance validates canonical successful response facts before writing evidence", async () => {
  const slotId = "verification-slot-basic-01";
  const accountId = PROVIDER_ACCEPTANCE_SLOTS[slotId].accountId;
  const requiredIds = [
    "workspaceId", "workspaceUrl", "computeAllocationId", "computeProviderId", "storageId",
    "storageProviderId", "attachmentId"
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
    argv: ["--allow-gateway-write", "--allow-provider-write", "--approval-id", "approval-production-verification"],
    env: {},
    stdout: { write: () => {} },
    stderr: { write: (chunk) => { stderr += chunk; } },
    fetchImpl: async () => { calls += 1; return json({}); }
  });
  assert.equal(denied, 1);
  assert.match(stderr, /provider_acceptance_approval_manifest_required/);
  assert.equal(calls, 0);
});
