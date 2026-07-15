import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import test from "node:test";
import { fileURLToPath } from "node:url";

import {
  runProductionVerifierCli,
  verificationOwnerFromSeed,
  verifyProductionChain
} from "../../tools/production-verifier.ts";

const FIXED_VERIFICATION_SLOT_ID = "verification-slot-01";
const fixedSlotDescriptor = {
  id: FIXED_VERIFICATION_SLOT_ID,
  customerProduct: false,
  instanceType: "SA5.MEDIUM4",
  chargeType: "PREPAID",
  periodMonths: 1,
  renewFlag: "NOTIFY_AND_MANUAL_RENEW",
  storageSizeGb: 10
};
const ownerSeed = JSON.stringify([{
  id: "usr-verifier",
  email: "owner@example.com",
  password: "correct horse battery staple",
  role: "owner",
  accountId: "acct-alpha",
  sub2apiUserId: 41
}]);

function json(payload, status = 200, headers = {}) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "content-type": "application/json", ...headers }
  });
}

function fixedSlotFixture({ slotCount = 1, ambiguous = false, inactive = false, nameOnly = false, mutate } = {}) {
  const calls = [];
  const computeAllocations = [];
  const storageVolumes = [];
  const workspaces = [];
  const deadline = new Date(Date.now() + 30 * 24 * 60 * 60 * 1000).toISOString();

  for (let index = 0; index < slotCount; index += 1) {
    const suffix = index + 1;
    const workspaceId = `workspace-slot-${suffix}`;
    computeAllocations.push({
      id: `compute-slot-${suffix}`,
      accountId: "acct-alpha",
      workspaceId,
      providerResourceId: `ins-slot-${suffix}`,
      nodePoolId: `np-slot-${suffix}`,
      status: inactive ? "destroyed" : "running",
      costTags: { opl_account_id: "acct-alpha", opl_workspace_id: workspaceId, opl_resource_id: `compute-slot-${suffix}` },
      providerData: {
        instanceType: "SA5.MEDIUM4",
        zone: "ap-guangzhou-3",
        chargeType: "PREPAID",
        periodMonths: "1",
        renewFlag: "NOTIFY_AND_MANUAL_RENEW",
        deadline
      }
    });
    if (!ambiguous) {
      storageVolumes.push({
        id: `storage-slot-${suffix}`,
        accountId: "acct-alpha",
        workspaceId,
        providerResourceId: `disk-slot-${suffix}`,
        sizeGb: 10,
        status: "available",
        costTags: { opl_account_id: "acct-alpha", opl_workspace_id: workspaceId, opl_resource_id: `storage-slot-${suffix}` },
        providerData: {
          diskChargeType: "PREPAID",
          periodMonths: "1",
          renewFlag: "NOTIFY_AND_MANUAL_RENEW",
          deadline,
          zone: "ap-guangzhou-3",
          pvName: `pv-slot-${suffix}`,
          sizeGb: "10"
        }
      });
    }
    workspaces.push({
      id: workspaceId,
      accountId: "acct-alpha",
      ownerAccountId: "acct-alpha",
      name: FIXED_VERIFICATION_SLOT_ID,
      ...(nameOnly ? {} : { verificationSlotId: FIXED_VERIFICATION_SLOT_ID }),
      currentComputeAllocationId: `compute-slot-${suffix}`,
      storageId: `storage-slot-${suffix}`,
      state: "running",
      openable: true,
      url: `https://workspace.medopl.cn/w/workspace-slot-${suffix}/`
    });
  }
  mutate?.({ computeAllocations, storageVolumes, workspaces });

  const fetchImpl = async (input, init = {}) => {
    const url = new URL(String(input));
    const method = init.method || "GET";
    calls.push({ method, path: url.pathname, search: url.search });

    if (url.hostname === "workspace.medopl.cn") {
      return new Response("<!doctype html><main>verification slot ready</main>", { status: 200 });
    }
    if (url.pathname === "/api/production/readiness") return json({ ready: true });
    if (url.pathname === "/api/auth/login") {
      return json({ user: { id: "usr-verifier", accountId: "acct-alpha", role: "owner" } }, 200, {
        "set-cookie": "opl_session=session-alpha; Path=/; HttpOnly",
        "x-opl-csrf-token": "csrf-alpha"
      });
    }

    assert.match(new Headers(init.headers).get("cookie") || "", /opl_session=session-alpha/);
    assert.equal(method, "GET", `ordinary production verification must be read only: ${method} ${url.pathname}`);

    if (url.pathname === "/api/pricing/catalog") {
      return json({
        storagePer10GbMonthly: { cnyCents: 1800, usdMicros: 2_571_429 },
        packages: [
          { id: "basic", available: true, price: { monthlyPriceCnyCents: 35_000, chargeUsdMicros: 50_000_000 } },
          { id: "pro", available: true, price: { monthlyPriceCnyCents: 150_000, chargeUsdMicros: 214_285_715 } }
        ]
      });
    }
    if (url.pathname === "/api/state") {
      return json({
        balance: { source: "sub2api", currency: "USD", userId: 41, usdMicros: 500_000_000 },
        computeAllocations,
        storageVolumes,
        workspaces
      });
    }
    if (url.pathname === "/api/gateway/summary") {
      return json({
        balance: { currency: "USD", usdMicros: 500_000_000 },
        apiKey: { id: "key-alpha", status: "active", maskedValue: "sk-****", revealed: false },
        usage: { usage1dUsdMicros: 1000 }
      });
    }
    return json({ error: "not_found" }, 404);
  };

  return { calls, fetchImpl };
}

test("production verifier requires one mapped owner", () => {
  assert.deepEqual(verificationOwnerFromSeed(ownerSeed, "acct-alpha"), {
    accountId: "acct-alpha",
    email: "owner@example.com",
    password: "correct horse battery staple",
    sub2apiUserId: 41
  });
  assert.throws(() => verificationOwnerFromSeed(JSON.stringify([{ role: "owner", accountId: "acct-alpha", email: "a", password: "b" }])), /verification_owner_mapping_required/);
  assert.throws(() => verificationOwnerFromSeed(JSON.stringify([
    { role: "owner", accountId: "acct-alpha", email: "a", password: "b", sub2apiUserId: 1 },
    { role: "owner", accountId: "acct-alpha", email: "c", password: "d", sub2apiUserId: 2 }
  ])), /verification_owner_credentials_required/);
});

test("production verifier rejects a non-production Console host before network access", async () => {
  let calls = 0;
  await assert.rejects(() => verifyProductionChain({
    origin: "https://attacker.example",
    authUsersJson: ownerSeed,
    slotDescriptor: fixedSlotDescriptor,
    runId: "wrong-console-host",
    fetchImpl: async () => { calls += 1; return json({}); }
  }), /public_console_origin_required/);
  assert.equal(calls, 0);
});

test("production verifier requires an exact controlled slot descriptor before network access", async () => {
  const invalidDescriptors = [
    undefined,
    "{",
    { ...fixedSlotDescriptor, id: "verification-slot-02" },
    { ...fixedSlotDescriptor, customerProduct: true },
    { ...fixedSlotDescriptor, instanceType: "SA5.LARGE4" },
    { ...fixedSlotDescriptor, chargeType: "POSTPAID_BY_HOUR" },
    { ...fixedSlotDescriptor, periodMonths: 2 },
    { ...fixedSlotDescriptor, renewFlag: "NOTIFY_AND_AUTO_RENEW" },
    { ...fixedSlotDescriptor, storageSizeGb: 20 }
  ];

  for (const slotDescriptor of invalidDescriptors) {
    let calls = 0;
    await assert.rejects(() => verifyProductionChain({
      origin: "https://cloud.medopl.cn",
      authUsersJson: ownerSeed,
      slotDescriptor,
      runId: "slot-descriptor-guard",
      fetchImpl: async () => { calls += 1; return json({}); }
    }), /verification_slot_descriptor_(?:required|invalid)/);
    assert.equal(calls, 0);
  }
});

test("ordinary production verifier reuses exactly one fixed slot without resource mutations", async () => {
  const fixture = fixedSlotFixture();
  const result = await verifyProductionChain({
    origin: "https://cloud.medopl.cn",
    authUsersJson: ownerSeed,
    accountId: "acct-alpha",
    slotDescriptor: fixedSlotDescriptor,
    runId: "read-only-smoke",
    purchaseBudgetRemaining: 0,
    fetchImpl: fixture.fetchImpl
  });

  assert.equal(result.ok, true);
  assert.equal(result.status, "reused");
  assert.equal(result.slot.id, FIXED_VERIFICATION_SLOT_ID);
  assert.equal(result.slot.computeAllocationId, "compute-slot-1");
  assert.equal(result.slot.computeProviderResourceId, "ins-slot-1");
  assert.equal(result.slot.nodePoolId, "np-slot-1");
  assert.equal(result.slot.storageId, "storage-slot-1");
  assert.equal(result.slot.storageProviderResourceId, "disk-slot-1");
  assert.equal(result.slot.persistentVolumeId, "pv-slot-1");
  assert.equal(result.workspaceId, "workspace-slot-1");
  assert.doesNotMatch(JSON.stringify(result), /correct horse|session-alpha|sk-\*\*\*\*/);
  assert.deepEqual(fixture.calls.filter((call) => call.method !== "GET"), [
    { method: "POST", path: "/api/auth/login", search: "" }
  ]);
  assert.equal(fixture.calls.some((call) => /create|destroy|detach|sync/i.test(call.path)), false);
});

test("zero slots only reports that independent Provider Acceptance is required", async () => {
  const fixture = fixedSlotFixture({ slotCount: 0 });
  const result = await verifyProductionChain({
    origin: "https://cloud.medopl.cn",
    authUsersJson: ownerSeed,
    slotDescriptor: fixedSlotDescriptor,
    runId: "slot-missing",
    purchaseBudgetRemaining: 1,
    fetchImpl: fixture.fetchImpl
  });

  assert.equal(result.ok, false);
  assert.equal(result.status, "provider_acceptance_required");
  assert.equal(result.slotId, FIXED_VERIFICATION_SLOT_ID);
  assert.equal(result.purchaseBudgetRemaining, 1);
  assert.equal(fixture.calls.some((call) => call.path.includes("compute-allocations") || call.path.includes("storage-volumes")), false);
});

test("the fixed slot can use the existing exact Workspace name as its marker", async () => {
  const fixture = fixedSlotFixture({ nameOnly: true });
  const result = await verifyProductionChain({
    origin: "https://cloud.medopl.cn",
    authUsersJson: ownerSeed,
    slotDescriptor: fixedSlotDescriptor,
    runId: "named-slot",
    purchaseBudgetRemaining: 0,
    fetchImpl: fixture.fetchImpl
  });

  assert.equal(result.ok, true);
  assert.equal(result.slot.id, FIXED_VERIFICATION_SLOT_ID);
});

test("fixed-slot selection fails closed on exhausted budget, duplicates, or ambiguity", async () => {
  const options = (fixture, purchaseBudgetRemaining = 1) => ({
    origin: "https://cloud.medopl.cn",
    authUsersJson: ownerSeed,
    slotDescriptor: fixedSlotDescriptor,
    runId: "slot-guard",
    purchaseBudgetRemaining,
    fetchImpl: fixture.fetchImpl
  });

  await assert.rejects(() => verifyProductionChain(options(fixedSlotFixture({ slotCount: 0 }), 0)), /verification_slot_purchase_budget_exhausted/);
  await assert.rejects(() => verifyProductionChain(options(fixedSlotFixture({ slotCount: 2 }))), /verification_slot_multiple/);
  await assert.rejects(() => verifyProductionChain(options(fixedSlotFixture({ ambiguous: true }))), /verification_slot_ambiguous/);
  await assert.rejects(() => verifyProductionChain(options(fixedSlotFixture({ inactive: true }))), /verification_slot_ambiguous/);
});

test("fixed-slot reuse requires prepaid shape, live deadlines, one zone, and account ownership", async () => {
  const mutations = [
    ({ computeAllocations }) => { computeAllocations[0].accountId = "acct-other"; },
    ({ computeAllocations }) => { computeAllocations[0].costTags.opl_account_id = "acct-other"; },
    ({ computeAllocations }) => { computeAllocations[0].providerData.instanceType = "SA5.LARGE4"; },
    ({ computeAllocations }) => { delete computeAllocations[0].nodePoolId; },
    ({ computeAllocations }) => { computeAllocations[0].providerData.chargeType = "POSTPAID_BY_HOUR"; },
    ({ computeAllocations }) => { computeAllocations[0].providerData.periodMonths = "2"; },
    ({ computeAllocations }) => { computeAllocations[0].providerData.renewFlag = "NOTIFY_AND_AUTO_RENEW"; },
    ({ computeAllocations }) => { computeAllocations[0].providerData.deadline = new Date(Date.now() - 1000).toISOString(); },
    ({ storageVolumes }) => { storageVolumes[0].providerData.diskChargeType = "POSTPAID_BY_HOUR"; },
    ({ storageVolumes }) => { storageVolumes[0].providerData.renewFlag = "NOTIFY_AND_AUTO_RENEW"; },
    ({ storageVolumes }) => { storageVolumes[0].providerData.zone = "ap-guangzhou-4"; },
    ({ storageVolumes }) => { delete storageVolumes[0].providerData.pvName; },
    ({ storageVolumes }) => { storageVolumes[0].sizeGb = 20; },
    ({ storageVolumes }) => { storageVolumes[0].workspaceId = "workspace-other"; },
    ({ workspaces }) => { workspaces[0].accountId = "acct-other"; }
  ];

  for (const mutate of mutations) {
    const fixture = fixedSlotFixture({ mutate });
    await assert.rejects(() => verifyProductionChain({
      origin: "https://cloud.medopl.cn",
      authUsersJson: ownerSeed,
      slotDescriptor: fixedSlotDescriptor,
      runId: "slot-compliance",
      purchaseBudgetRemaining: 0,
      fetchImpl: fixture.fetchImpl
    }), /verification_slot_ambiguous/);
  }
});

test("fixed-slot reuse requires exact one-month provider periods from resource state", async () => {
  const mutations = [
    ({ computeAllocations }) => { delete computeAllocations[0].providerData.periodMonths; },
    ({ storageVolumes }) => { delete storageVolumes[0].providerData.periodMonths; }
  ];

  for (const mutate of mutations) {
    const fixture = fixedSlotFixture({ mutate });
    await assert.rejects(() => verifyProductionChain({
      origin: "https://cloud.medopl.cn",
      authUsersJson: ownerSeed,
      slotDescriptor: fixedSlotDescriptor,
      runId: "slot-internal-one-month",
      purchaseBudgetRemaining: 0,
      fetchImpl: fixture.fetchImpl
    }), /verification_slot_ambiguous/);
  }
});

test("production verifier CLI rejects legacy paid flags before network access", async () => {
  let stderr = "";
  let calls = 0;
  const code = await runProductionVerifierCli({
    argv: ["--paid-confirmation", "legacy"],
    env: {},
    stdout: { write: () => {} },
    stderr: { write: (chunk) => { stderr += chunk; } },
    fetchImpl: async () => { calls += 1; return json({}); }
  });

  assert.equal(code, 1);
  assert.match(stderr, /production_verifier_read_only/);
  assert.equal(calls, 0);
});

test("production verifier CLI help describes read-only fixed-slot verification", async () => {
  let stdout = "";
  let calls = 0;
  const code = await runProductionVerifierCli({
    argv: ["--help"],
    stdout: { write: (chunk) => { stdout += chunk; } },
    stderr: { write: () => {} },
    fetchImpl: async () => { calls += 1; return json({}); }
  });

  assert.equal(code, 0);
  assert.match(stdout, /read-only/i);
  assert.match(stdout, /verification-slot-01/);
  assert.doesNotMatch(stdout, /paid.confirmation|spends real balance/i);
  assert.equal(calls, 0);
});

test("retired staging verifier fails closed before loading env or reaching the network", () => {
  const script = fileURLToPath(new URL("../../tools/staging-local-verifier.ts", import.meta.url));
  const result = spawnSync(process.execPath, [script], {
    encoding: "utf8",
    env: {
      ...process.env,
      OPL_STAGING_ENV_FILE: "/definitely/missing/staging.env",
      OPL_CONSOLE_ORIGIN: "https://unreachable.invalid"
    }
  });

  assert.equal(result.status, 1);
  assert.match(result.stderr, /staging_local_verifier_retired/);
  assert.doesNotMatch(result.stderr, /ENOENT|real_cloud_e2e_confirmation_required|creates and later destroys/i);
});
