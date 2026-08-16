import assert from "node:assert/strict";
import test, { after } from "node:test";

import {
  mutationApprovalFromJson,
  requestJson,
  runProductionVerifierCli,
  verificationOwnerFromSeed,
  verifyProductionChain,
  walletFact
} from "../../tools/production-verifier.ts";

const FIXED_VERIFICATION_SLOT_ID = "verification-slot-basic-01";
const BASIC_ACCOUNT_ID = "acct-verification-slot-basic-01";
const PRO_ACCOUNT_ID = "acct-verification-slot-pro-01";
const ADMIN_EMAIL = "admin@medopl.cn";
const ADMIN_PASSWORD = "admin-password";
const previousAdminEmail = process.env.OPL_SUB2API_ADMIN_EMAIL;
const previousAdminPassword = process.env.OPL_SUB2API_ADMIN_PASSWORD;
process.env.OPL_SUB2API_ADMIN_EMAIL = ADMIN_EMAIL;
process.env.OPL_SUB2API_ADMIN_PASSWORD = ADMIN_PASSWORD;
after(() => {
  if (previousAdminEmail === undefined) delete process.env.OPL_SUB2API_ADMIN_EMAIL;
  else process.env.OPL_SUB2API_ADMIN_EMAIL = previousAdminEmail;
  if (previousAdminPassword === undefined) delete process.env.OPL_SUB2API_ADMIN_PASSWORD;
  else process.env.OPL_SUB2API_ADMIN_PASSWORD = previousAdminPassword;
});
const fixedSlotDescriptor = {
  id: FIXED_VERIFICATION_SLOT_ID,
  customerProduct: false,
  instanceType: "SA5.MEDIUM4",
  server: "2c4g",
  cpu: 2,
  memoryGb: 4,
  cbsGb: 10,
  chargeType: "PREPAID",
  periodMonths: 1,
  renewFlag: "NOTIFY_AND_MANUAL_RENEW"
};
const proSlotDescriptor = {
  id: "verification-slot-pro-01",
  customerProduct: false,
  instanceType: "SA5.2XLARGE16",
  server: "8c16g",
  cpu: 8,
  memoryGb: 16,
  cbsGb: 100,
  chargeType: "PREPAID",
  periodMonths: 1,
  renewFlag: "NOTIFY_AND_MANUAL_RENEW"
};
const ownerSeed = JSON.stringify([
  {
    id: "usr-verifier-basic", email: "basic-owner@example.com", password: "correct horse battery staple",
    role: "owner", accountId: BASIC_ACCOUNT_ID, sub2apiUserId: 41
  },
  {
    id: "usr-verifier-pro", email: "pro-owner@example.com", password: "correct horse battery staple pro",
    role: "owner", accountId: PRO_ACCOUNT_ID, sub2apiUserId: 42
  }
]);

test("production mutation approval binds one approval to exact target allowlists", () => {
  const raw = JSON.stringify({
    approvalId: "approval-production-verification",
    expiresAt: "2099-07-19T00:00:00Z",
    accountIds: [BASIC_ACCOUNT_ID],
    workspaceIds: ["workspace-basic"],
    resourceIds: [FIXED_VERIFICATION_SLOT_ID]
  });
  const target = {
    approvalId: "approval-production-verification",
    accountId: BASIC_ACCOUNT_ID,
    workspaceId: "workspace-basic",
    resourceIds: [FIXED_VERIFICATION_SLOT_ID]
  };
  assert.equal(mutationApprovalFromJson(raw, target, "verification").approvalId, "approval-production-verification");
  assert.throws(() => mutationApprovalFromJson(raw.replace('"2099-07-19T00:00:00Z"', "0"), target, "verification"), /verification_approval_manifest_invalid/);
  assert.throws(() => mutationApprovalFromJson(raw.replace("2099-07-19T00:00:00Z", "July 19, 2099 UTC"), target, "verification"), /verification_approval_manifest_invalid/);
  assert.throws(() => mutationApprovalFromJson(raw, { ...target, approvalId: "approval-other" }, "verification"), /verification_approval_id_mismatch/);
  assert.throws(() => mutationApprovalFromJson(raw, { ...target, workspaceId: "workspace-other" }, "verification"), /verification_target_forbidden/);
  assert.throws(() => mutationApprovalFromJson(raw.replace("2099-07-19", "2020-07-19"), target, "verification"), /verification_approval_expired/);
});

const pricingCatalogResponse = {
  priceVersion: "pilot-usd-2026-07-v1",
  billingUnit: "calendar_month",
  currency: "USD",
  displayCurrency: "USD",
  walletCurrency: "USD",
  storageSize: { minimumGb: 10, stepGb: 10 },
  storagePer10GbMonthly: {
    priceVersion: "pilot-usd-2026-07-v1", currency: "USD", displayCurrency: "USD", usdMicros: 2_580_000
  },
  packages: [
    {
      id: "basic", name: "Basic", available: true, cpu: 2, memoryGb: 4, diskGb: 10, server: "2c4g",
      price: { priceVersion: "pilot-usd-2026-07-v1", currency: "USD", displayCurrency: "USD", chargeUsdMicros: 50_000_000 }
    },
    {
      id: "pro", name: "Pro", available: true, cpu: 8, memoryGb: 16, diskGb: 100, server: "8c16g",
      price: { priceVersion: "pilot-usd-2026-07-v1", currency: "USD", displayCurrency: "USD", chargeUsdMicros: 214_280_000 }
    }
  ]
};

function json(payload, status = 200, headers = {}) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "content-type": "application/json", ...headers }
  });
}

function source(payload, sourceName = "sub2api", status = "available", headers = {}) {
  return json({ source: sourceName, status, available: true, fetchedAt: new Date().toISOString(), data: payload }, 200, {
    "cache-control": "private, no-store",
    ...headers
  });
}

test("production wallet verifier requires a canonical non-negative int64 decimal string", () => {
  const envelope = (usdMicros) => ({
    status: "available",
    data: { userId: "41", currency: "USD", usdMicros, status: "active" }
  });

  assert.equal(walletFact(envelope("9223372036854775807"), "41").usdMicros, "9223372036854775807");
  for (const value of [500_000_000, "01", "-1", "9223372036854775808"]) {
    assert.throws(() => walletFact(envelope(value), "41"), /dedicated_workspace_wallet_required/);
  }
});

function fixedSlotFixture({
  slotCount = 1,
  ambiguous = false,
  inactive = false,
  nameOnly = false,
  customerProduct = false,
  descriptor = fixedSlotDescriptor,
  accountId = descriptor.id === proSlotDescriptor.id ? PRO_ACCOUNT_ID : BASIC_ACCOUNT_ID,
  catalog = pricingCatalogResponse,
  readiness = { ready: true, cloudImagesReady: true, workspaceImagesReady: true, immutableImagesReady: true },
  includeCurrentReceipt = true,
  receiptCacheControl = "private, no-store",
  receiptOverrides = {},
  adminUserOverrides = {},
  managementStateOverrides = {},
  mutate
} = {}) {
  const calls = [];
  const computeAllocations = [];
  const storageVolumes = [];
  const workspaces = [];
  const deadline = new Date(Date.now() + 30 * 24 * 60 * 60 * 1000).toISOString();
  const periodStart = new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString();
  const runtimeOperations = [
    { id: "provider-op-compute-1", accountId, workspaceId: "workspace-slot-1", action: "create_compute_allocation", status: "succeeded", providerRequestId: "ins-slot-1", result: '{"resource":"compute-slot-1"}' },
    { id: "provider-op-storage-1", accountId, workspaceId: "workspace-slot-1", action: "create_storage_volume", status: "succeeded", providerRequestId: "disk-slot-1", result: '{"resource":"storage-slot-1"}' },
    { id: "workspace-launch-1", accountId, workspaceId: "workspace-slot-1", action: "workspace.launch", status: "succeeded", providerRequestId: "", result: '{"phase":"completed","internalCredential":"must-not-emit"}' },
    { id: "workspace-renewal-1", accountId, workspaceId: "workspace-slot-1", action: "workspace.renewal", status: "succeeded", providerRequestId: "", result: '{"phase":"completed"}' },
    { id: "job-progress-1", accountId, workspaceId: "workspace-slot-1", action: "job.execute", status: "running", result: { internalCredential: "ignored-job-secret" } }
  ];

  for (let index = 0; index < slotCount; index += 1) {
    const suffix = index + 1;
    const workspaceId = `workspace-slot-${suffix}`;
    computeAllocations.push({
      id: `compute-slot-${suffix}`,
      accountId,
      workspaceId,
      providerResourceId: `ins-slot-${suffix}`,
      nodePoolId: `np-slot-${suffix}`,
      status: inactive ? "destroyed" : "running",
      costTags: { opl_account_id: accountId, opl_workspace_id: workspaceId, opl_resource_id: `compute-slot-${suffix}` },
      providerData: {
        instanceType: descriptor.instanceType,
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
        accountId,
        workspaceId,
        providerResourceId: `disk-slot-${suffix}`,
        sizeGb: descriptor.cbsGb,
        status: "available",
        costTags: { opl_account_id: accountId, opl_workspace_id: workspaceId, opl_resource_id: `storage-slot-${suffix}` },
        providerData: {
          diskChargeType: "PREPAID",
          periodMonths: "1",
          renewFlag: "NOTIFY_AND_MANUAL_RENEW",
          deadline,
          zone: "ap-guangzhou-3",
          pvName: `pv-slot-${suffix}`,
          sizeGb: String(descriptor.cbsGb)
        }
      });
    }
    workspaces.push({
      id: workspaceId,
      accountId,
      ownerAccountId: accountId,
      name: descriptor.id,
      ...(nameOnly ? {} : { verificationSlotId: descriptor.id }),
      customerProduct,
      currentComputeAllocationId: `compute-slot-${suffix}`,
      storageId: `storage-slot-${suffix}`,
      state: "running",
      openable: true,
      receiptId: "receipt-current-1",
      url: `https://workspace.medopl.cn/w/workspace-slot-${suffix}/`
    });
  }
  mutate?.({ computeAllocations, storageVolumes, workspaces, runtimeOperations });

  const fetchImpl = async (input, init = {}) => {
    const url = new URL(String(input));
    const method = init.method || "GET";
    const headers = new Headers(init.headers);
    calls.push({ method, path: url.pathname, search: url.search, signal: init.signal });

    if (url.hostname === "workspace.medopl.cn") {
      return new Response("<!doctype html><main>verification slot ready</main>", { status: 200 });
    }
    if (url.pathname === "/api/production/readiness") return json(readiness);
    if (url.pathname === "/api/auth/login") {
      const credentials = JSON.parse(init.body);
      if (credentials.email === ADMIN_EMAIL && credentials.password === ADMIN_PASSWORD) {
        return json({ user: { id: "usr-admin", accountId: "acct-admin", role: "admin", ...adminUserOverrides } }, 200, {
          "set-cookie": "opl_session=session-admin; Path=/; HttpOnly",
          "x-opl-csrf-token": "csrf-admin"
        });
      }
      const expectedOwner = JSON.parse(ownerSeed).find((user) => user.accountId === accountId);
      assert.deepEqual(credentials, { email: expectedOwner.email, password: expectedOwner.password });
      return json({ user: { id: `usr-verifier-${descriptor.id}`, accountId, role: "owner" } }, 200, {
        "set-cookie": "opl_session=session-alpha; Path=/; HttpOnly",
        "x-opl-csrf-token": "csrf-alpha"
      });
    }

    assert.equal(method, "GET", `ordinary production verification must be read only: ${method} ${url.pathname}`);

    if (url.pathname === "/api/state") throw new Error("retired_api_state_requested");
    if (url.pathname === "/api/management/state") {
      assert.match(headers.get("cookie") || "", /opl_session=session-admin/);
      return json({
        computeAllocations: [
          ...computeAllocations,
          { id: "compute-other", accountId: "acct-other", workspaceId: "workspace-other" }
        ],
        storageVolumes: [
          ...storageVolumes,
          { id: "storage-other", accountId: "acct-other", workspaceId: "workspace-other" }
        ],
        workspaces: [
          ...workspaces,
          { id: "workspace-other", accountId: "acct-other", ownerAccountId: "acct-other" }
        ],
        runtimeOperations: [
          ...(slotCount > 0 ? runtimeOperations : []),
          { id: "operation-other", accountId: "acct-other", action: "job.execute", status: "running" }
        ],
        ...managementStateOverrides
      });
    }

    assert.match(headers.get("cookie") || "", /opl_session=session-alpha/);

    if (url.pathname === "/api/pricing/catalog") {
      return json(catalog);
    }
    if (url.pathname === "/api/gateway/wallet") {
      return source({ userId: accountId === PRO_ACCOUNT_ID ? "42" : "41", currency: "USD", usdMicros: "500000000", status: "active" });
    }
    if (url.pathname === "/api/gateway/keys") {
      return source({ items: [{ id: accountId === PRO_ACCOUNT_ID ? "10" : "9", name: "opl-workspace", status: "active" }], total: 1 });
    }
    if (url.pathname === "/api/billing/receipts/receipt-current-1" && includeCurrentReceipt && slotCount > 0) {
      return source({
        receiptId: "receipt-current-1", type: "workspace.created", status: "completed",
        workspaceId: "workspace-slot-1", createdAt: periodStart, ...receiptOverrides
      }, "ledger", "available", { "cache-control": receiptCacheControl });
    }
    return json({ error: "not_found" }, 404);
  };

  return { calls, fetchImpl };
}

test("production verifier requires one mapped owner", () => {
  assert.deepEqual(verificationOwnerFromSeed(ownerSeed, BASIC_ACCOUNT_ID), {
    accountId: BASIC_ACCOUNT_ID,
    email: "basic-owner@example.com",
    password: "correct horse battery staple",
    sub2apiUserId: 41
  });
  assert.throws(() => verificationOwnerFromSeed(JSON.stringify([{ role: "owner", accountId: BASIC_ACCOUNT_ID, email: "a", password: "b" }])), /verification_owner_mapping_required/);
  assert.throws(() => verificationOwnerFromSeed(JSON.stringify([
    { role: "owner", accountId: BASIC_ACCOUNT_ID, email: "a", password: "b", sub2apiUserId: 1 },
    { role: "owner", accountId: BASIC_ACCOUNT_ID, email: "c", password: "d", sub2apiUserId: 2 }
  ])), /verification_owner_credentials_required/);
});

test("production verifier fetches have a bounded timeout and preserve caller aborts", async () => {
  const signals: AbortSignal[] = [];
  const delayedFetch = async (_input, init = {}) => new Promise<Response>((resolve, reject) => {
    if (init.signal) {
      signals.push(init.signal);
      init.signal.addEventListener("abort", () => reject(init.signal.reason), { once: true });
    }
    setTimeout(() => resolve(json({ ok: true })), 30);
  });

  await assert.rejects(
    requestJson({ fetchImpl: delayedFetch, origin: "https://cloud.medopl.cn", path: "/api/production/readiness", timeoutMs: 5 }),
    (error: any) => error?.name === "TimeoutError"
  );

  const caller = new AbortController();
  const pending = requestJson({
    fetchImpl: delayedFetch,
    origin: "https://cloud.medopl.cn",
    path: "/api/production/readiness",
    timeoutMs: 1_000,
    signal: caller.signal
  });
  caller.abort(new Error("caller_abort"));
  await assert.rejects(pending, /caller_abort/);
  assert.equal(signals.length, 2);
  assert.equal(signals.every((signal) => signal.aborted), true);
});

test("production verifier requires the dedicated account id before network access", async () => {
  let calls = 0;
  await assert.rejects(() => verifyProductionChain({
    origin: "https://cloud.medopl.cn",
    authUsersJson: ownerSeed,
    slotDescriptor: fixedSlotDescriptor,
    runId: "missing-account",
    fetchImpl: async () => { calls += 1; return json({}); }
  }), /verification_account_id_required/);
  assert.equal(calls, 0);
});

test("production verifier requires the independent reserved admin credentials before network access", async () => {
  for (const credentials of [
    { adminEmail: "", adminPassword: ADMIN_PASSWORD },
    { adminEmail: ADMIN_EMAIL, adminPassword: "" },
    { adminEmail: "owner@example.com", adminPassword: ADMIN_PASSWORD }
  ]) {
    let calls = 0;
    await assert.rejects(() => verifyProductionChain({
      origin: "https://cloud.medopl.cn",
      authUsersJson: ownerSeed,
      accountId: BASIC_ACCOUNT_ID,
      slotDescriptor: fixedSlotDescriptor,
      runId: "reserved-admin-guard",
      ...credentials,
      fetchImpl: async () => { calls += 1; return json({}); }
    }), /verification_admin_credentials_required/);
    assert.equal(calls, 0);
  }
});

test("production verifier freezes each slot to its reserved account before network access", async () => {
  for (const [slotId, slotDescriptor, accountId] of [
    [fixedSlotDescriptor.id, fixedSlotDescriptor, PRO_ACCOUNT_ID],
    [proSlotDescriptor.id, proSlotDescriptor, BASIC_ACCOUNT_ID],
    [fixedSlotDescriptor.id, fixedSlotDescriptor, "acct-arbitrary"]
  ]) {
    let calls = 0;
    await assert.rejects(() => verifyProductionChain({
      origin: "https://cloud.medopl.cn", authUsersJson: ownerSeed, accountId, slotId, slotDescriptor,
      runId: "fixed-account-guard", fetchImpl: async () => { calls += 1; return json({}); }
    }), /verification_account_id_fixed/);
    assert.equal(calls, 0);
  }
});

test("production verifier rejects a non-production Console host before network access", async () => {
  let calls = 0;
  await assert.rejects(() => verifyProductionChain({
    origin: "https://attacker.example",
    authUsersJson: ownerSeed,
    accountId: BASIC_ACCOUNT_ID,
    slotDescriptor: fixedSlotDescriptor,
    runId: "wrong-console-host",
    fetchImpl: async () => { calls += 1; return json({}); }
  }), /public_console_origin_required/);
  assert.equal(calls, 0);
});

test("production verifier requires an exact controlled slot descriptor before network access", async () => {
  const missingServer = { ...fixedSlotDescriptor };
  delete missingServer.server;
  const invalidDescriptors = [
    undefined,
    "{",
    missingServer,
    { ...fixedSlotDescriptor, id: "verification-slot-02" },
    { ...fixedSlotDescriptor, customerProduct: true },
    { ...fixedSlotDescriptor, instanceType: "SA5.LARGE4" },
    { ...fixedSlotDescriptor, server: "2c2g" },
    { ...fixedSlotDescriptor, cpu: 4 },
    { ...fixedSlotDescriptor, memoryGb: 2 },
    { ...fixedSlotDescriptor, cbsGb: 20 },
    { ...fixedSlotDescriptor, chargeType: "POSTPAID_BY_HOUR" },
    { ...fixedSlotDescriptor, periodMonths: 2 },
    { ...fixedSlotDescriptor, renewFlag: "NOTIFY_AND_AUTO_RENEW" },
    { ...fixedSlotDescriptor, unexpected: true }
  ];

  for (const slotDescriptor of invalidDescriptors) {
    let calls = 0;
    await assert.rejects(() => verifyProductionChain({
      origin: "https://cloud.medopl.cn",
      authUsersJson: ownerSeed,
      accountId: BASIC_ACCOUNT_ID,
      slotDescriptor,
      runId: "slot-descriptor-guard",
      fetchImpl: async () => { calls += 1; return json({}); }
    }), /verification_slot_descriptor_(?:required|invalid)/);
    assert.equal(calls, 0);
  }
});

test("production verifier rejects legacy converted prices and CNY customer catalog fields", async () => {
  const legacyConverted = structuredClone(pricingCatalogResponse);
  legacyConverted.storagePer10GbMonthly.usdMicros = 2_571_429;
  legacyConverted.packages.find((row) => row.id === "pro").price.chargeUsdMicros = 214_285_715;
  const cnyPublicShape = structuredClone(pricingCatalogResponse);
  cnyPublicShape.storagePer10GbMonthly.usdMicros = 2_571_429;
  cnyPublicShape.storagePer10GbMonthly.cnyCents = 1_800;
  cnyPublicShape.packages[0].price.monthlyPriceCnyCents = 35_000;
  cnyPublicShape.packages[1].price.chargeUsdMicros = 214_285_715;
  cnyPublicShape.packages[1].price.monthlyPriceCnyCents = 150_000;

  for (const catalog of [legacyConverted, cnyPublicShape]) {
    const fixture = fixedSlotFixture({ catalog });
    await assert.rejects(() => verifyProductionChain({
      origin: "https://cloud.medopl.cn", authUsersJson: ownerSeed, accountId: BASIC_ACCOUNT_ID,
      slotDescriptor: fixedSlotDescriptor, runId: "catalog-contract-guard", fetchImpl: fixture.fetchImpl
    }), /catalog_(?:price|usd)_contract|(?:basic|pro|storage)_catalog_price/);
  }
});

test("ordinary production verifier reuses exactly one fixed slot without resource mutations", async () => {
  const fixture = fixedSlotFixture();
  const result = await verifyProductionChain({
    origin: "https://cloud.medopl.cn",
    authUsersJson: ownerSeed,
    accountId: BASIC_ACCOUNT_ID,
    slotDescriptor: fixedSlotDescriptor,
    runId: "read-only-smoke",
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
  assert.equal(result.wallet.usdMicros, "500000000");
  assert.equal(result.key.id, "9");
  assert.equal(result.ledgerReceipt.receiptId, "receipt-current-1");
  assert.deepEqual(result.runtimeOperations.map(({ id, action, status, operationDigest }) => ({ id, action, status, digestLength: operationDigest.length })), [
    { id: "job-progress-1", action: "job.execute", status: "running", digestLength: 64 },
    { id: "provider-op-compute-1", action: "create_compute_allocation", status: "succeeded", digestLength: 64 },
    { id: "provider-op-storage-1", action: "create_storage_volume", status: "succeeded", digestLength: 64 },
    { id: "workspace-launch-1", action: "workspace.launch", status: "succeeded", digestLength: 64 },
    { id: "workspace-renewal-1", action: "workspace.renewal", status: "succeeded", digestLength: 64 }
  ]);
  assert.doesNotMatch(JSON.stringify(result), /correct horse|session-alpha|sk-\*\*\*\*|must-not-emit/);
  assert.doesNotMatch(JSON.stringify(result), /ignored-job-secret/);
  assert.equal(fixture.calls.some((call) => call.path === "/api/billing/receipts/receipt-current-1"), true);
  assert.equal(fixture.calls.some((call) => call.path === "/api/billing/receipts"), false);
  assert.equal(fixture.calls.some((call) => call.path === "/api/gateway/summary" || /^\/api\/workspaces\/[^/]+\/receipt$/.test(call.path)), false);
  assert.deepEqual(fixture.calls.filter((call) => call.method !== "GET"), [
    { method: "POST", path: "/api/auth/login", search: "", signal: fixture.calls[1].signal },
    { method: "POST", path: "/api/auth/login", search: "", signal: fixture.calls[3].signal }
  ]);
  assert.equal(fixture.calls.some((call) => call.path === "/api/management/state"), true);
  assert.equal(fixture.calls.some((call) => call.path === "/api/state"), false);
  assert.equal(fixture.calls.some((call) => /create|destroy|detach|sync/i.test(call.path)), false);
  assert.equal(fixture.calls.every((call) => call.signal instanceof AbortSignal), true);
});

test("production verifier requires immutable Ready Pod facts and the existing Ledger source contract", async () => {
  await assert.rejects(() => verifyProductionChain({
    origin: "https://cloud.medopl.cn", authUsersJson: ownerSeed, accountId: BASIC_ACCOUNT_ID,
    slotDescriptor: fixedSlotDescriptor, runId: "missing-image-proof",
    fetchImpl: fixedSlotFixture({ readiness: { ready: true } }).fetchImpl
  }), /production_readiness/);

  await assert.rejects(() => verifyProductionChain({
    origin: "https://cloud.medopl.cn", authUsersJson: ownerSeed, accountId: BASIC_ACCOUNT_ID,
    slotDescriptor: fixedSlotDescriptor, runId: "missing-receipt",
    fetchImpl: fixedSlotFixture({ includeCurrentReceipt: false }).fetchImpl
  }), /request_failed:GET:\/api\/billing\/receipts\/receipt-current-1:404/);

  await assert.rejects(() => verifyProductionChain({
    origin: "https://cloud.medopl.cn", authUsersJson: ownerSeed, accountId: BASIC_ACCOUNT_ID,
    slotDescriptor: fixedSlotDescriptor, runId: "missing-receipt-reference",
    fetchImpl: fixedSlotFixture({ mutate: ({ workspaces }) => { workspaces[0].receiptId = ""; } }).fetchImpl
  }), /verification_slot_ambiguous/);

  for (const fixture of [
    fixedSlotFixture({ receiptCacheControl: "no-store" }),
    fixedSlotFixture({ receiptOverrides: { type: "billing.resource_purchased.v1" } }),
    fixedSlotFixture({ receiptOverrides: { internalCredential: "must-not-accept" } })
  ]) {
    await assert.rejects(() => verifyProductionChain({
      origin: "https://cloud.medopl.cn", authUsersJson: ownerSeed, accountId: BASIC_ACCOUNT_ID,
      slotDescriptor: fixedSlotDescriptor, runId: "unsafe-workspace-receipt", fetchImpl: fixture.fetchImpl
    }), /(?:source_contract_invalid|ledger_receipt_invalid)/);
  }
});

test("production verifier snapshots every account RuntimeOperation without returning result payloads", async () => {
  for (const mutate of [
    ({ runtimeOperations }) => { runtimeOperations[2].status = ""; },
    ({ runtimeOperations }) => { delete runtimeOperations[3].action; },
    ({ runtimeOperations }) => { delete runtimeOperations[0].id; },
    ({ runtimeOperations }) => { runtimeOperations.push({ ...runtimeOperations[3], id: runtimeOperations[3].id }); },
    ({ runtimeOperations }) => { runtimeOperations.splice(0); }
  ]) {
    const fixture = fixedSlotFixture({ mutate });
    await assert.rejects(() => verifyProductionChain({
      origin: "https://cloud.medopl.cn", authUsersJson: ownerSeed, accountId: BASIC_ACCOUNT_ID,
      slotDescriptor: fixedSlotDescriptor, runId: "runtime-operation-snapshot", fetchImpl: fixture.fetchImpl
    }), /runtime_operation_history_required/);
  }
});

test("production verifier fails closed when management state omits a reserved-account inventory", async () => {
  const fixture = fixedSlotFixture({ managementStateOverrides: { runtimeOperations: null } });
  await assert.rejects(() => verifyProductionChain({
    origin: "https://cloud.medopl.cn",
    authUsersJson: ownerSeed,
    accountId: BASIC_ACCOUNT_ID,
    slotDescriptor: fixedSlotDescriptor,
    runId: "management-state-inventory",
    fetchImpl: fixture.fetchImpl
  }), /management_state_account_inventory_invalid/);
});

test("production verifier rejects a session that is not the exact reserved admin identity", async () => {
  const fixture = fixedSlotFixture({ adminUserOverrides: { id: "usr-other" } });
  await assert.rejects(() => verifyProductionChain({
    origin: "https://cloud.medopl.cn",
    authUsersJson: ownerSeed,
    accountId: BASIC_ACCOUNT_ID,
    slotDescriptor: fixedSlotDescriptor,
    runId: "reserved-admin-identity",
    fetchImpl: fixture.fetchImpl
  }), /verification_admin_identity_invalid/);
  assert.equal(fixture.calls.some((call) => call.path === "/api/management/state"), false);
});

test("ordinary production verifier accepts the fixed Pro slot without resource mutations", async () => {
  const fixture = fixedSlotFixture({ descriptor: proSlotDescriptor });
  const result = await verifyProductionChain({
    origin: "https://cloud.medopl.cn", authUsersJson: ownerSeed, accountId: PRO_ACCOUNT_ID,
    slotId: proSlotDescriptor.id, slotDescriptor: proSlotDescriptor, runId: "read-only-pro-smoke",
    fetchImpl: fixture.fetchImpl
  });
  assert.equal(result.ok, true);
  assert.equal(result.status, "reused");
  assert.equal(result.slot.id, proSlotDescriptor.id);
  assert.equal(fixture.calls.some((call) => /create|destroy|detach|sync|renew/i.test(call.path)), false);
});

test("zero slots only reports that independent Provider Acceptance is required", async () => {
  const fixture = fixedSlotFixture({ slotCount: 0 });
  const result = await verifyProductionChain({
    origin: "https://cloud.medopl.cn",
    authUsersJson: ownerSeed,
    accountId: BASIC_ACCOUNT_ID,
    slotDescriptor: fixedSlotDescriptor,
    runId: "slot-missing",
    fetchImpl: fixture.fetchImpl
  });

  assert.equal(result.ok, false);
  assert.equal(result.status, "provider_acceptance_required");
  assert.equal(result.slotId, FIXED_VERIFICATION_SLOT_ID);
  assert.equal(Object.hasOwn(result, "purchaseBudgetRemaining"), false);
  assert.equal(fixture.calls.some((call) => call.path.includes("compute-allocations") || call.path.includes("storage-volumes")), false);
});

test("same-name customer Workspaces cannot stand in for the server-marked fixed slot", async () => {
  await assert.rejects(() => verifyProductionChain({
    origin: "https://cloud.medopl.cn",
    authUsersJson: ownerSeed,
    accountId: BASIC_ACCOUNT_ID,
    slotDescriptor: fixedSlotDescriptor,
    runId: "same-name-not-slot",
    fetchImpl: fixedSlotFixture({ nameOnly: true }).fetchImpl
  }), /verification_slot_ambiguous/);

  const customerProduct = fixedSlotFixture({ customerProduct: true });
  await assert.rejects(() => verifyProductionChain({
    origin: "https://cloud.medopl.cn",
    authUsersJson: ownerSeed,
    accountId: BASIC_ACCOUNT_ID,
    slotDescriptor: fixedSlotDescriptor,
    runId: "customer-workspace-rejected",
    fetchImpl: customerProduct.fetchImpl
  }), /verification_slot_ambiguous/);
});

test("fixed-slot selection fails closed on duplicates or ambiguity", async () => {
  const options = (fixture) => ({
    origin: "https://cloud.medopl.cn",
    authUsersJson: ownerSeed,
    accountId: BASIC_ACCOUNT_ID,
    slotDescriptor: fixedSlotDescriptor,
    runId: "slot-guard",
    fetchImpl: fixture.fetchImpl
  });

  await assert.rejects(() => verifyProductionChain(options(fixedSlotFixture({ slotCount: 2 }))), /verification_slot_multiple/);
  await assert.rejects(() => verifyProductionChain(options(fixedSlotFixture({ ambiguous: true }))), /verification_slot_ambiguous/);
  await assert.rejects(() => verifyProductionChain(options(fixedSlotFixture({ inactive: true }))), /verification_slot_ambiguous/);
});

test("fixed-slot selection requires the complete reserved account inventory to have cardinality one", async () => {
  const mutations = [
    ({ workspaces }) => workspaces.splice(0),
    ({ workspaces }) => workspaces.push({ ...structuredClone(workspaces[0]), id: "workspace-extra", verificationSlotId: "unrelated-slot" }),
    ({ computeAllocations }) => computeAllocations.push({ ...structuredClone(computeAllocations[0]), id: "compute-extra", providerResourceId: "ins-extra" }),
    ({ storageVolumes }) => storageVolumes.push({ ...structuredClone(storageVolumes[0]), id: "storage-extra", providerResourceId: "disk-extra" }),
    ({ computeAllocations }) => computeAllocations.push({ ...structuredClone(computeAllocations[0]), id: "compute-duplicate-provider" }),
    ({ storageVolumes }) => storageVolumes.push({ ...structuredClone(storageVolumes[0]), id: "storage-duplicate-provider" })
  ];

  for (const mutate of mutations) {
    const fixture = fixedSlotFixture({ mutate });
    await assert.rejects(() => verifyProductionChain({
      origin: "https://cloud.medopl.cn", authUsersJson: ownerSeed, accountId: BASIC_ACCOUNT_ID,
      slotDescriptor: fixedSlotDescriptor, runId: "complete-slot-cardinality", fetchImpl: fixture.fetchImpl
    }), /verification_slot_(?:multiple|ambiguous)/);
  }
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
    ({ workspaces }) => { workspaces[0].accountId = "acct-other"; },
    ({ workspaces }) => { workspaces[0].customerProduct = true; },
    ({ workspaces }) => { delete workspaces[0].customerProduct; }
  ];

  for (const mutate of mutations) {
    const fixture = fixedSlotFixture({ mutate });
    await assert.rejects(() => verifyProductionChain({
      origin: "https://cloud.medopl.cn",
      authUsersJson: ownerSeed,
      accountId: BASIC_ACCOUNT_ID,
      slotDescriptor: fixedSlotDescriptor,
      runId: "slot-compliance",
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
      accountId: BASIC_ACCOUNT_ID,
      slotDescriptor: fixedSlotDescriptor,
      runId: "slot-internal-one-month",
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

test("production verifier CLI requires protected reserved admin env before network access", async () => {
  let stderr = "";
  let calls = 0;
  const code = await runProductionVerifierCli({
    argv: [],
    env: {
      OPL_CONSOLE_ORIGIN: "https://cloud.medopl.cn",
      OPL_VERIFY_AUTH_USERS_JSON: ownerSeed,
      OPL_VERIFY_ACCOUNT_ID: BASIC_ACCOUNT_ID,
      OPL_VERIFY_SLOT_DESCRIPTOR_JSON: JSON.stringify(fixedSlotDescriptor),
      OPL_VERIFY_RUN_ID: "missing-admin-env"
    },
    stdout: { write: () => {} },
    stderr: { write: (chunk) => { stderr += chunk; } },
    fetchImpl: async () => { calls += 1; return json({}); }
  });

  assert.equal(code, 1);
  assert.match(stderr, /verification_admin_credentials_required/);
  assert.equal(calls, 0);
});

test("production verifier CLI help describes the read-only evidence level and fixed-slot verification", async () => {
  let stdout = "";
  let calls = 0;
  const code = await runProductionVerifierCli({
    argv: ["--help"],
    stdout: { write: (chunk) => { stdout += chunk; } },
    stderr: { write: () => {} },
    fetchImpl: async () => { calls += 1; return json({}); }
  });

  assert.equal(code, 0);
  assert.match(stdout, /--read-only/);
  assert.match(stdout, /read-only/i);
  assert.match(stdout, /evidence level: read-only/i);
  assert.match(stdout, /verification-slot-basic-01/);
  assert.doesNotMatch(stdout, /paid.confirmation|spends real balance/i);
  assert.equal(calls, 0);
});
