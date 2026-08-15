import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import test from "node:test";

import {
  PRODUCTION_BASIC_ACCEPTANCE_B_ALLOWED_WRITES,
  PRODUCTION_BASIC_ACCEPTANCE_B_CONFIRMATION,
  PRODUCTION_BASIC_ACCEPTANCE_B_FORBIDDEN_WRITES,
  PRODUCTION_BASIC_ACCEPTANCE_B_OPERATION,
  PRODUCTION_BASIC_ACCEPTANCE_B_ACCOUNT_PREPARE_OPERATION,
  blockedProductionBasicAcceptanceBArtifact,
  blockedProductionBasicAcceptanceBPrepareArtifact,
  findUniqueProductionBasicAcceptanceBAccount,
  findUniqueProductionBasicAcceptanceBEmailAccount,
  parseProductionBasicAcceptanceBApproval,
  prepareProductionBasicAcceptanceBAccount,
  productionBasicAcceptanceBStageBudgets,
  productionBasicAcceptanceBApprovalDigest,
  runProductionBasicAcceptanceB,
  readProductionBasicAcceptanceBLaunchUntilTerminal,
  submitProductionBasicAcceptanceBLaunch,
  validateProductionBasicAcceptanceBArtifact,
  validateProductionBasicAcceptanceBPrepareReadback,
  validateProductionBasicAcceptanceBReadback,
  validateProductionBasicAcceptanceBStageBudgets,
  validateProductionBasicAcceptanceBWriteCounts
} from "../../tools/production-basic-acceptance-b.ts";

const APPROVAL_ID = "approval-production-basic-acceptance-b";
const ACCOUNT_ID = "acct-acceptance-b-01";
const LAUNCH_KEY = "workspace-launch:acceptance-b-20260802-01";
const MERGED_MAIN_SHA = "a".repeat(40);
const CLOUD_IMAGE_DIGEST = `sha256:${"b".repeat(64)}`;
const WORKSPACE_IMAGE_DIGEST = `sha256:${"c".repeat(64)}`;

function stableId(...parts) {
  const hash = createHash("sha1");
  for (const part of parts) {
    hash.update(String(part));
    hash.update(Buffer.from([0]));
  }
  return hash.digest("hex");
}

function expectedIdentities() {
  const operationId = `workspace-launch-${stableId(ACCOUNT_ID, LAUNCH_KEY).slice(0, 18)}`;
  return {
    operationId,
    workspaceId: `ws-${stableId("workspace-launch-v2", ACCOUNT_ID, operationId).slice(0, 18)}`
  };
}

function approvalFixture(overrides = {}) {
  const identities = expectedIdentities();
  return {
    schemaVersion: 1,
    operationMode: "acceptance_b_fresh_order",
    approvalId: APPROVAL_ID,
    expiresAt: "2099-08-02T00:00:00Z",
    confirmation: PRODUCTION_BASIC_ACCEPTANCE_B_CONFIRMATION,
    release: {
      mergedMainSha: MERGED_MAIN_SHA,
      cloudImageDigest: CLOUD_IMAGE_DIGEST,
      workspaceImageDigest: WORKSPACE_IMAGE_DIGEST
    },
    customer: { email: "acceptance-b@example.com", accountId: ACCOUNT_ID },
    launch: {
      idempotencyKey: LAUNCH_KEY,
      operationId: identities.operationId,
      workspaceId: identities.workspaceId,
      name: "Acceptance B Basic Workspace",
      packageId: "basic",
      sizeGb: 10,
      autoRenew: false
    },
    expected: { nodePoolId: "np-basic-acceptance-b", resolvedInstanceType: "SA5.MEDIUM4" },
    allowedWrites: [...PRODUCTION_BASIC_ACCEPTANCE_B_ALLOWED_WRITES],
    forbiddenWrites: [...PRODUCTION_BASIC_ACCEPTANCE_B_FORBIDDEN_WRITES],
    ...overrides
  };
}

function exactWriteCounts(overrides = {}) {
  return {
    workspaceLaunchPosts: 1,
    sub2apiDebits: 1,
    tencentCvmCreates: 1,
    tencentCvmOwnershipClaims: 1,
    kubernetesNodeClaims: 1,
    tencentCbsCreates: 1,
    runtimeCreates: 1,
    receiptCreates: 1,
    accountProvisionPosts: 0,
    walletAdjustmentPosts: 0,
    modelRequests: 0,
    refunds: 0,
    renewals: 0,
    deletes: 0,
    replacements: 0,
    ...overrides
  };
}

function exactStageBudgets(overrides = {}) {
  const confirmed = () => ({ attempted: 1, confirmed: 1, unknown: 0, max: 1 });
  return {
    compute_create: confirmed(),
    compute_claim_cvm: confirmed(),
    compute_claim_node: confirmed(),
    cbs_create: confirmed(),
    static_binding_apply: confirmed(),
    attachment: confirmed(),
    secret: confirmed(),
    runtime: confirmed(),
    activation: confirmed(),
    receipt: confirmed(),
    ...overrides
  };
}

function readbackFixture(approval, overrides = {}) {
  const url = `https://workspace.medopl.cn/w/${approval.launch.workspaceId}/`;
  const totalChargeUsdMicros = 52_580_000;
  const value = {
    schemaVersion: 1,
    operationMode: PRODUCTION_BASIC_ACCEPTANCE_B_OPERATION.operationMode,
    status: "succeeded",
    approvalId: approval.approvalId,
    approvalDigest: productionBasicAcceptanceBApprovalDigest(approval),
    release: { ...approval.release },
    baseline: { workspaceCount: 0, workspaceLaunchCount: 0, workspaceKeyCount: 0, workspaceReceiptCount: 0 },
    quote: { packageId: "basic", sizeGb: 10, priceVersion: "pilot-usd-2026-07-v1", currency: "USD", totalChargeUsdMicros },
    debit: { operationId: `${approval.launch.operationId}:charge`, count: 1, amountUsdMicros: totalChargeUsdMicros },
    launch: {
      operationId: approval.launch.operationId,
      accountId: approval.customer.accountId,
      workspaceId: approval.launch.workspaceId,
      name: approval.launch.name,
      packageId: "basic",
      sizeGb: 10,
      autoRenew: false,
      priceVersion: "pilot-usd-2026-07-v1",
      currency: "USD",
      totalChargeUsdMicros,
      status: "succeeded",
      phase: "succeeded",
      computeAllocationId: "ca_acceptance_b_01",
      storageId: "vol_acceptance_b_01",
      attachmentId: "att_acceptance_b_01",
      runtimeId: "runtime_acceptance_b_01",
      receiptId: "receipt_acceptance_b_01",
      url
    },
    compute: {
      allocationId: "ca_acceptance_b_01",
      nodePoolId: approval.expected.nodePoolId,
      instanceType: approval.expected.resolvedInstanceType,
      cvmInstanceId: "ins-acceptanceb01",
      nodeName: "10.66.1.88",
      chargeType: "PREPAID",
      periodMonths: 1,
      renewFlag: "NOTIFY_AND_MANUAL_RENEW"
    },
    storage: {
      id: "vol_acceptance_b_01",
      providerResourceId: "disk-acceptanceb01",
      sizeGb: 10,
      chargeType: "PREPAID",
      periodMonths: 1,
      renewFlag: "NOTIFY_AND_MANUAL_RENEW"
    },
    attachment: { id: "att_acceptance_b_01", status: "attached" },
    runtime: {
      id: "runtime_acceptance_b_01",
      status: "running",
      ready: true,
      url,
      podImageId: `containerd://workspace@${approval.release.workspaceImageDigest}`
    },
    receipt: {
      id: "receipt_acceptance_b_01",
      type: "billing.workspace_purchased.v1",
      status: "completed",
      workspaceId: approval.launch.workspaceId,
      computeAllocationId: "ca_acceptance_b_01",
      storageId: "vol_acceptance_b_01",
      runtimeId: "runtime_acceptance_b_01",
      totalChargeUsdMicros
    },
    workspaceUrl: { url, statusCode: 200 },
    stageBudgets: exactStageBudgets(),
    writeCounts: exactWriteCounts()
  };
  return { ...value, ...overrides };
}

function sourcePayload(source, data, status = "available") {
  return {
    source,
    status,
    available: true,
    fetchedAt: "2026-08-04T00:00:00.000Z",
    data
  };
}

function response(payload, status = 200, headers = {}) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "cache-control": "private, no-store", ...headers }
  });
}

function prepareFetchFixture({ keys = [], walletValues = ["0"], adjustmentAvailable = true, adjustmentPostStatus = 201, accountAvailable = true, accountPostStatus = 201 } = {}) {
  const email = "prepare@example.com";
  const accountId = `acct-${stableId("account", email).slice(0, 18)}`;
  const emailDigest = createHash("sha256").update(email).update(Buffer.from([0])).digest("hex");
  const operationKey = `acceptance-b-wallet-recharge-v1:${accountId}:${emailDigest}`;
  const walletOperationId = `wallet-adjustment-${stableId(accountId, operationKey).slice(0, 18)}`;
  const requests = [];
  let walletRead = 0;
  let adjustmentExists = adjustmentAvailable;
  let accountExists = accountAvailable;
  const account = {
    accountId,
    consoleUserId: "usr-prepare",
    sub2apiUserId: "41",
    email,
    role: "owner",
    status: "active"
  };
  const adjustment = {
    operationId: walletOperationId,
    accountId,
    kind: "recharge",
    amountUsd: "60.000000",
    reason: "production Basic Acceptance B account preparation",
    status: "succeeded",
    phase: "complete",
    beforeBalance: sourcePayload("sub2api", { currency: "USD", usdMicros: "0" }),
    afterBalance: sourcePayload("sub2api", { currency: "USD", usdMicros: "60000000" })
  };
  const fetchImpl = async (url, init = {}) => {
    const parsed = new URL(url);
    requests.push({ method: init.method || "GET", path: parsed.pathname + parsed.search, body: init.body ? JSON.parse(init.body) : undefined });
    if (parsed.pathname === "/api/auth/login") {
      const body = JSON.parse(init.body);
      const isAdmin = body.email === "admin@medopl.cn";
      return response({ user: isAdmin ? { accountId: "acct-admin", role: "admin" } : { accountId, role: "owner" } }, 200, { "set-cookie": `${isAdmin ? "admin" : "customer"}=session; Path=/` });
    }
    if (parsed.pathname === "/api/operator/accounts") {
      if (init.method === "POST") {
        accountExists = true;
        const idempotencyKey = init.headers?.["Idempotency-Key"] || "";
        return response({ status: "succeeded", accountId, operationId: `account-provision-${stableId(idempotencyKey, email).slice(0, 18)}` }, accountPostStatus);
      }
      return response(sourcePayload("control-plane+sub2api", accountExists ? { items: [account], total: 1, page: 1, pageSize: 50 } : { items: [], total: 0, page: 1, pageSize: 50 }, accountExists ? "available" : "empty"));
    }
    if (parsed.pathname === "/api/auth/me") return response(sourcePayload("sub2api", { accountId, email, role: "owner", status: "active", consoleUserId: "usr-prepare", sub2apiUserId: "41" }));
    if (parsed.pathname === "/api/workspaces") return response(sourcePayload("control-plane", { items: [], total: 0, page: 1, pageSize: 50 }, "empty"));
    if (parsed.pathname === "/api/workspace-launches") return response({ items: [] });
    if (parsed.pathname === "/api/gateway/keys") {
      const page = Number(parsed.searchParams.get("page"));
      const pageSize = Number(parsed.searchParams.get("pageSize"));
      const start = (page - 1) * pageSize;
      const items = keys.slice(start, start + pageSize);
      return response(sourcePayload("sub2api", { items, total: keys.length, page, pageSize }, keys.length ? "available" : "empty"));
    }
    if (parsed.pathname === "/api/billing/receipts") return response(sourcePayload("ledger", { receipts: [], hasMore: false }, "empty"));
    if (parsed.pathname === "/api/pricing/preview") return response({ resourceType: "workspace", packageId: "basic", currency: "USD", priceVersion: "pilot-usd-2026-07-v1", totalChargeUsdMicros: 52580000, storage: { priceSnapshot: { sizeGb: 10 } } });
    if (parsed.pathname === "/api/gateway/wallet") {
      const value = walletValues[Math.min(walletRead++, walletValues.length - 1)];
      return response(sourcePayload("sub2api", { userId: "41", currency: "USD", usdMicros: value, status: "active" }));
    }
    if (parsed.pathname === `/api/operator/wallet-adjustments/${walletOperationId}`) {
      return adjustmentExists ? response(adjustment) : response({ error: "wallet_adjustment_not_found" }, 404);
    }
    if (parsed.pathname === `/api/operator/accounts/${accountId}/wallet-adjustments` && init.method === "POST") {
      adjustmentExists = true;
      return response(adjustment, adjustmentPostStatus);
    }
    throw new Error(`unexpected_request:${init.method || "GET"}:${parsed.pathname}`);
  };
  return { email, accountId, walletOperationId, requests, fetchImpl };
}

test("Acceptance B exposes one dedicated fresh-order operation and parses its exact approval", () => {
  assert.deepEqual(PRODUCTION_BASIC_ACCEPTANCE_B_OPERATION, {
    schemaVersion: 1,
    operationMode: "acceptance_b_fresh_order",
    confirmation: "RUN_ONE_INDEPENDENT_FRESH_BASIC_ORDER_FOR_ACCEPTANCE_B",
    packageId: "basic",
    sizeGb: 10,
    autoRenew: false,
    workspaceLaunchPostCount: 1,
    exactWrites: { sub2apiDebit: 1, cvmCreate: 1, cvmOwnershipClaim: 1, nodeClaim: 1, cbsCreate: 1, runtimeCreate: 1, receiptCreate: 1 },
    terminalEvidence: [
      "launch_succeeded",
      "runtime_ready",
      "receipt_completed",
      "pod_image_id_equals_approved_workspace_image_digest",
      "workspace_url_http_200"
    ]
  });
  const raw = approvalFixture();
  const parsed = parseProductionBasicAcceptanceBApproval(JSON.stringify(raw), {
    approvalId: APPROVAL_ID,
    now: new Date("2026-08-02T00:00:00Z")
  });
  assert.deepEqual(parsed, raw);
  assert.match(productionBasicAcceptanceBApprovalDigest(parsed), /^[0-9a-f]{64}$/);
});

test("Acceptance B approval rejects expiry, identity drift, extra fields, and broader write authority", () => {
  const identities = expectedIdentities();
  const cases = [
    approvalFixture({ expiresAt: "2026-08-01T00:00:00Z" }),
    approvalFixture({ launch: { ...approvalFixture().launch, operationId: `${identities.operationId}-drift` } }),
    approvalFixture({ extra: true }),
    approvalFixture({ allowedWrites: [...PRODUCTION_BASIC_ACCEPTANCE_B_ALLOWED_WRITES, "send_model_request"] })
  ];
  for (const value of cases) {
    assert.throws(() => parseProductionBasicAcceptanceBApproval(value, {
      approvalId: APPROVAL_ID,
      now: new Date("2026-08-02T00:00:00Z")
    }), /production_basic_acceptance_b_approval_invalid/);
  }
});

test("Acceptance B matches the approved account pair across pages without requiring a singleton production account total", () => {
  const approved = { accountId: ACCOUNT_ID, email: "acceptance-b@example.com", status: "active" };
  const pages = [
    {
      items: Array.from({ length: 50 }, (_, index) => ({
        accountId: `acct-filler-${index + 1}`,
        email: `filler-${index + 1}@example.com`
      })),
      total: 51,
      page: 1,
      pageSize: 50
    },
    { items: [approved], total: 51, page: 2, pageSize: 50 }
  ];
  assert.deepEqual(findUniqueProductionBasicAcceptanceBAccount(pages, approved.accountId, approved.email), approved);
  const pairDriftOnAnotherPage = {
    ...pages[0],
    items: [{ accountId: approved.accountId, email: "different@example.com" }, ...pages[0].items.slice(1)]
  };
  assert.throws(() => findUniqueProductionBasicAcceptanceBAccount([pairDriftOnAnotherPage, pages[1]], approved.accountId, approved.email), /production_basic_acceptance_b_account_readback_invalid/);
  const emailDriftOnAnotherPage = {
    ...pages[0],
    items: [{ accountId: "acct-different", email: approved.email }, ...pages[0].items.slice(1)]
  };
  assert.throws(() => findUniqueProductionBasicAcceptanceBAccount([emailDriftOnAnotherPage, pages[1]], approved.accountId, approved.email), /production_basic_acceptance_b_account_readback_invalid/);
  const duplicatePages = [
    { ...pages[0], total: 52 },
    { items: [approved, { ...approved }], total: 52, page: 2, pageSize: 50 }
  ];
  assert.throws(() => findUniqueProductionBasicAcceptanceBAccount([
    ...duplicatePages
  ], approved.accountId, approved.email), /production_basic_acceptance_b_account_readback_invalid/);
});

test("Acceptance B account preparation exposes only a redacted zero-workspace checkpoint", () => {
  assert.deepEqual(PRODUCTION_BASIC_ACCEPTANCE_B_ACCOUNT_PREPARE_OPERATION, {
    schemaVersion: 1,
    operationMode: "acceptance_b_account_prepare",
    packageId: "basic",
    sizeGb: 10,
    autoRenew: false,
    rechargeUsdMicros: "60000000",
    confirmation: "PREPARE_ONE_ACCEPTANCE_B_ACCOUNT_WITH_ONE_PROVISION_AND_ONE_RECHARGE",
    forbiddenWrites: [
      "workspace_launch", "sub2api_debit", "cvm_create", "node_claim", "cbs_create", "attachment_create",
      "gateway_secret", "runtime_create", "workspace_activate", "workspace_receipt", "model_request", "refund", "renew", "delete", "replace"
    ]
  });
  const evidence = {
    schemaVersion: 1,
    operationMode: "acceptance_b_account_prepare",
    status: "succeeded",
    mergedMainSha: "a".repeat(40),
    customerIdentitySha256: "b".repeat(64),
    identity: { accountProvisionIdentitySha256: "c".repeat(64), status: "active" },
    baseline: { workspaceCount: 0, workspaceLaunchCount: 0, workspaceKeyCount: 0, workspaceReceiptCount: 0 },
    quote: { packageId: "basic", sizeGb: 10, totalChargeUsdMicros: 52580000, currency: "USD" },
    wallet: { beforeUsdMicros: "0", afterUsdMicros: "60000000", rechargeIdentitySha256: "d".repeat(64), rechargeCount: 1 },
    writeCounts: { accountProvisionPosts: 1, walletAdjustmentPosts: 1, workspaceLaunchPosts: 0, sub2apiDebits: 0, tencentCvmCreates: 0, kubernetesNodeClaims: 0, tencentCbsCreates: 0, runtimeCreates: 0, receiptCreates: 0, modelRequests: 0, refunds: 0, renewals: 0, deletes: 0, replacements: 0 }
  };
  assert.deepEqual(validateProductionBasicAcceptanceBPrepareReadback(evidence, { mergedSha: evidence.mergedMainSha }), evidence);
  for (const forbidden of [
    { password: "must-not-be-present" },
    { identity: { ...evidence.identity, accountId: "acct-raw" } },
    { wallet: { ...evidence.wallet, operationId: "wallet-adjustment-raw" } },
    { nested: { email: "raw@example.com" } }
  ]) {
    assert.throws(() => validateProductionBasicAcceptanceBPrepareReadback({ ...evidence, ...forbidden }, { mergedSha: evidence.mergedMainSha }), /production_basic_acceptance_b_prepare_readback_invalid/);
  }
});

test("Acceptance B prepare re-reads the wallet after an existing recharge operation", async () => {
  const fixture = prepareFetchFixture({ walletValues: ["0", "60000000"] });
  const result = await prepareProductionBasicAcceptanceBAccount({
    origin: "https://cloud.medopl.cn",
    adminEmail: "admin@medopl.cn",
    adminPassword: "admin-password",
    customerEmail: fixture.email,
    customerPassword: "customer-password",
    mergedSha: MERGED_MAIN_SHA,
    fetchImpl: fixture.fetchImpl,
    now: new Date("2026-08-04T00:00:00.000Z")
  });
  assert.equal(result.wallet.afterUsdMicros, "60000000");
  assert.equal(result.wallet.rechargeCount, 1);
  assert.equal(result.writeCounts.walletAdjustmentPosts, 1);
  assert.equal(fixture.requests.filter((request) => request.method === "POST" && request.path.includes("wallet-adjustments")).length, 0);
  assert.equal(fixture.requests.filter((request) => request.path === "/api/gateway/wallet").length, 2);
});

test("Acceptance B prepare never retries a recharge after an untrusted POST when readback proves success", async () => {
  const fixture = prepareFetchFixture({ adjustmentAvailable: false, adjustmentPostStatus: 202, walletValues: ["0", "60000000"] });
  const result = await prepareProductionBasicAcceptanceBAccount({
    origin: "https://cloud.medopl.cn",
    adminEmail: "admin@medopl.cn",
    adminPassword: "admin-password",
    customerEmail: fixture.email,
    customerPassword: "customer-password",
    mergedSha: MERGED_MAIN_SHA,
    fetchImpl: fixture.fetchImpl,
    now: new Date("2026-08-04T00:00:00.000Z")
  });
  assert.equal(result.wallet.afterUsdMicros, "60000000");
  assert.equal(result.writeCounts.walletAdjustmentPosts, 1);
  assert.equal(fixture.requests.filter((request) => request.method === "POST" && request.path.endsWith("/wallet-adjustments")).length, 1);
});

test("Acceptance B prepare never retries a provision after an untrusted POST when the account readback proves success", async () => {
  const fixture = prepareFetchFixture({ accountAvailable: false, accountPostStatus: 202, walletValues: ["60000000"] });
  const result = await prepareProductionBasicAcceptanceBAccount({
    origin: "https://cloud.medopl.cn",
    adminEmail: "admin@medopl.cn",
    adminPassword: "admin-password",
    customerEmail: fixture.email,
    customerPassword: "customer-password",
    mergedSha: MERGED_MAIN_SHA,
    fetchImpl: fixture.fetchImpl,
    now: new Date("2026-08-04T00:00:00.000Z")
  });
  assert.equal(result.writeCounts.accountProvisionPosts, 1);
  assert.equal(fixture.requests.filter((request) => request.method === "POST" && request.path === "/api/operator/accounts").length, 1);
});

test("Acceptance B prepare treats five non-Workspace gateway keys as a fresh baseline", async () => {
  const fixture = prepareFetchFixture({
    keys: Array.from({ length: 5 }, (_, index) => ({ id: `general-${index + 1}`, kind: "general", status: "active" })),
    walletValues: ["60000000"]
  });
  const result = await prepareProductionBasicAcceptanceBAccount({
    origin: "https://cloud.medopl.cn",
    adminEmail: "admin@medopl.cn",
    adminPassword: "admin-password",
    customerEmail: fixture.email,
    customerPassword: "customer-password",
    mergedSha: MERGED_MAIN_SHA,
    fetchImpl: fixture.fetchImpl,
    now: new Date("2026-08-04T00:00:00.000Z")
  });
  assert.equal(result.status, "succeeded");
  assert.equal(result.baseline.workspaceKeyCount, 0);
  assert.deepEqual(validateProductionBasicAcceptanceBPrepareReadback(result, { mergedSha: MERGED_MAIN_SHA }), result);
});

test("Acceptance B prepare finds a Workspace key on a later gateway page and blocks", async () => {
  const fixture = prepareFetchFixture({
    keys: [
      ...Array.from({ length: 50 }, (_, index) => ({ id: `general-${index + 1}`, kind: "general", status: "active" })),
      { id: "workspace-1", kind: "workspace", status: "active" }
    ],
    walletValues: ["60000000"]
  });
  await assert.rejects(() => prepareProductionBasicAcceptanceBAccount({
    origin: "https://cloud.medopl.cn",
    adminEmail: "admin@medopl.cn",
    adminPassword: "admin-password",
    customerEmail: fixture.email,
    customerPassword: "customer-password",
    mergedSha: MERGED_MAIN_SHA,
    fetchImpl: fixture.fetchImpl,
    now: new Date("2026-08-04T00:00:00.000Z")
  }), /production_basic_acceptance_b_baseline_not_fresh/);
  assert.equal(fixture.requests.filter((request) => request.path.startsWith("/api/gateway/keys?")).length, 2);
});

test("Acceptance B prepare fails closed when a gateway key kind cannot be classified", async () => {
  const fixture = prepareFetchFixture({
    keys: [{ id: "unknown-1", status: "active" }],
    walletValues: ["60000000"]
  });
  await assert.rejects(() => prepareProductionBasicAcceptanceBAccount({
    origin: "https://cloud.medopl.cn",
    adminEmail: "admin@medopl.cn",
    adminPassword: "admin-password",
    customerEmail: fixture.email,
    customerPassword: "customer-password",
    mergedSha: MERGED_MAIN_SHA,
    fetchImpl: fixture.fetchImpl,
    now: new Date("2026-08-04T00:00:00.000Z")
  }), /production_basic_acceptance_b_baseline_not_fresh/);
});

test("Acceptance B blocked preparation keeps partial provision or recharge results unknown", () => {
  const artifact = blockedProductionBasicAcceptanceBPrepareArtifact("production_basic_acceptance_b_recharge_failed");
  assert.equal(artifact.status, "blocked");
  assert.equal(artifact.mutationLedgerState, "unknown");
  assert.deepEqual(artifact.runnerDirectMutationCounts, { sub2api: "unknown", tencent: "unknown", kubernetes: "unknown" });
  assert.deepEqual(artifact.reconciliationRequired, ["account_provision", "wallet_recharge"]);
  assert.notDeepEqual(artifact.runnerDirectMutationCounts, { sub2api: 0, tencent: 0, kubernetes: 0 });
});

test("Acceptance B email selector returns no account, one account, and fails duplicate identities", () => {
  const empty = [{ items: [], total: 0, page: 1, pageSize: 50 }];
  assert.equal(findUniqueProductionBasicAcceptanceBEmailAccount(empty, "prepare@example.com"), null);
  const account = { accountId: "acct-prepare", consoleUserId: "usr-prepare", sub2apiUserId: "41", email: "prepare@example.com", role: "owner", status: "active" };
  const pages = [{ items: [account], total: 1, page: 1, pageSize: 50 }];
  assert.deepEqual(findUniqueProductionBasicAcceptanceBEmailAccount(pages, account.email), account);
  assert.throws(() => findUniqueProductionBasicAcceptanceBEmailAccount([{ items: [account, { ...account, accountId: "acct-other" }], total: 2, page: 1, pageSize: 50 }], account.email), /production_basic_acceptance_b_account_readback_invalid/);
});

test("Acceptance B write accounting accepts only the frozen one-order cardinalities", () => {
  assert.deepEqual(validateProductionBasicAcceptanceBWriteCounts(exactWriteCounts()), exactWriteCounts());
  for (const value of [
    exactWriteCounts({ workspaceLaunchPosts: 2 }),
    exactWriteCounts({ tencentCvmOwnershipClaims: 0 }),
    exactWriteCounts({ kubernetesNodeClaims: 0 }),
    exactWriteCounts({ modelRequests: 1 }),
    { ...exactWriteCounts(), unexpected: 0 }
  ]) {
    assert.throws(() => validateProductionBasicAcceptanceBWriteCounts(value), /production_basic_acceptance_b_write_counts_invalid/);
  }
});

test("Acceptance B launch reads the deterministic identity before its single POST and reconciles a lost response", async () => {
  const approval = parseProductionBasicAcceptanceBApproval(approvalFixture(), {
    approvalId: APPROVAL_ID,
    now: new Date("2026-08-02T00:00:00Z")
  });
  const launch = { operationId: approval.launch.operationId, workspaceId: approval.launch.workspaceId, accountId: approval.customer.accountId, status: "queued", phase: "compute_fulfilling" };
  for (const responseLost of [false, true]) {
    const calls = [];
    let getCount = 0;
    const fetchImpl = async (input, init = {}) => {
      const url = new URL(String(input));
      const method = String(init.method || "GET").toUpperCase();
      calls.push({ method, path: url.pathname });
      if (method === "POST") {
        if (responseLost) throw new Error("response_lost");
        return response(launch, 202);
      }
      if (method === "GET" && url.pathname === `/api/workspace-launches/${approval.launch.operationId}`) {
        getCount += 1;
        if (getCount === 1) return response({ error: "not_found" }, 404);
        return response(launch);
      }
      throw new Error(`unexpected_request:${method}:${url.pathname}`);
    };
    assert.deepEqual(await submitProductionBasicAcceptanceBLaunch({
      requestOptions: { fetchImpl, origin: "https://cloud.medopl.cn", timeoutMs: 1_000 },
      customerAuth: { cookie: "customer=test", csrfToken: "csrf-test" },
      approval,
      internalServiceToken: "acceptance-b-capability"
    }), launch);
    assert.equal(calls.filter((call) => call.method === "POST").length, 1);
    assert.equal(calls.filter((call) => call.method === "GET").length, responseLost ? 2 : 1);
  }
});

test("Acceptance B runtime configuration requires distinct Fabric and Control Plane credentials", async () => {
  const approval = approvalFixture();
  const baseOptions = {
    origin: "https://cloud.medopl.cn",
    fabricOrigin: "http://127.0.0.1:18082",
    fabricServiceToken: "fabric-transport-token",
    internalServiceToken: "control-plane-capability-token",
    customerPassword: "customer-password",
    approvalJson: JSON.stringify(approval),
    approvalId: APPROVAL_ID,
    mergedSha: MERGED_MAIN_SHA,
    kubeconfigPath: "/tmp/acceptance-kubeconfig",
    fetchImpl: async () => { throw new Error("unexpected_network"); }
  };
  await assert.rejects(() => runProductionBasicAcceptanceB(baseOptions), /production_basic_acceptance_b_admin_login_failed|unexpected_network/);
  await assert.rejects(() => runProductionBasicAcceptanceB({
    ...baseOptions,
    fabricServiceToken: "same-token",
    internalServiceToken: "same-token"
  }), /production_basic_acceptance_b_config_invalid/);
});

test("Acceptance B launch continues an existing deterministic operation without a second POST", async () => {
  const approval = parseProductionBasicAcceptanceBApproval(approvalFixture(), {
    approvalId: APPROVAL_ID,
    now: new Date("2026-08-02T00:00:00Z")
  });
  const launch = { operationId: approval.launch.operationId, workspaceId: approval.launch.workspaceId, accountId: approval.customer.accountId, status: "queued", phase: "compute_fulfilling" };
  const calls = [];
  const fetchImpl = async (input, init = {}) => {
    const url = new URL(String(input));
    const method = String(init.method || "GET").toUpperCase();
    calls.push({ method, path: url.pathname });
    if (method === "GET" && url.pathname === `/api/workspace-launches/${approval.launch.operationId}`) return response(launch);
    throw new Error(`unexpected_request:${method}:${url.pathname}`);
  };
  assert.deepEqual(await submitProductionBasicAcceptanceBLaunch({
    requestOptions: { fetchImpl, origin: "https://cloud.medopl.cn", timeoutMs: 1_000 },
    customerAuth: { cookie: "customer=test", csrfToken: "csrf-test" },
    approval,
    internalServiceToken: "acceptance-b-capability"
  }), launch);
  assert.deepEqual(calls, [{ method: "GET", path: `/api/workspace-launches/${approval.launch.operationId}` }]);
});

test("Acceptance B timeout continuation polls the exact existing Launch with GET only", async () => {
  const approval = parseProductionBasicAcceptanceBApproval(approvalFixture(), {
    approvalId: APPROVAL_ID,
    now: new Date("2026-08-02T00:00:00Z")
  });
  const calls = [];
  const states = [
    { status: "queued", phase: "storage_fulfilling" },
    { status: "succeeded", phase: "succeeded" }
  ];
  const fetchImpl = async (input, init = {}) => {
    const url = new URL(String(input));
    const method = String(init.method || "GET").toUpperCase();
    calls.push({ method, path: url.pathname });
    if (method !== "GET" || url.pathname !== `/api/workspace-launches/${approval.launch.operationId}`) {
      throw new Error(`unexpected_request:${method}:${url.pathname}`);
    }
    const state = states[Math.min(calls.length - 1, states.length - 1)];
    return response({
      operationId: approval.launch.operationId,
      workspaceId: approval.launch.workspaceId,
      accountId: approval.customer.accountId,
      ...state
    });
  };

  const launch = await readProductionBasicAcceptanceBLaunchUntilTerminal({
    requestOptions: { fetchImpl, origin: "https://cloud.medopl.cn", timeoutMs: 1_000 },
    customerAuth: { cookie: "customer=test", csrfToken: "csrf-test" },
    approval,
    launchPollAttempts: 2,
    launchPollDelayMs: 0
  });
  assert.equal(launch.status, "succeeded");
  assert.equal(launch.phase, "succeeded");
  assert.deepEqual(calls, [
    { method: "GET", path: `/api/workspace-launches/${approval.launch.operationId}` },
    { method: "GET", path: `/api/workspace-launches/${approval.launch.operationId}` }
  ]);
});

test("Acceptance B timeout preserves only the last safe server Launch observation", async () => {
  const approval = parseProductionBasicAcceptanceBApproval(approvalFixture(), {
    approvalId: APPROVAL_ID,
    now: new Date("2026-08-02T00:00:00Z")
  });
  const fetchImpl = async (input, init = {}) => {
    const url = new URL(String(input));
    const method = String(init.method || "GET").toUpperCase();
    if (method !== "GET" || url.pathname !== `/api/workspace-launches/${approval.launch.operationId}`) {
      throw new Error(`unexpected_request:${method}:${url.pathname}`);
    }
    return response({
      operationId: approval.launch.operationId,
      workspaceId: approval.launch.workspaceId,
      accountId: approval.customer.accountId,
      status: "retryable",
      phase: "compute_claim_pending",
      errorCode: "fabric_compute_claim_pending"
    });
  };

  await assert.rejects(() => readProductionBasicAcceptanceBLaunchUntilTerminal({
    requestOptions: { fetchImpl, origin: "https://cloud.medopl.cn", timeoutMs: 1_000 },
    customerAuth: { cookie: "customer=test", csrfToken: "csrf-test" },
    approval,
    launchPollAttempts: 2,
    launchPollDelayMs: 0
  }), (error) => {
    assert.equal(error.message, "production_basic_acceptance_b_launch_timeout");
    assert.deepEqual(error.launchReadback, {
      responseReceived: true,
      status: "retryable",
      phase: "compute_claim_pending",
      errorCode: "fabric_compute_claim_pending"
    });
    return true;
  });
});

test("Acceptance B blocked artifact uses the shared validator and forbids identities", () => {
  const approval = parseProductionBasicAcceptanceBApproval(approvalFixture(), {
    approvalId: APPROVAL_ID,
    now: new Date("2026-08-02T00:00:00Z")
  });
  const error = new Error("production_basic_acceptance_b_launch_timeout");
  error.launchReadback = {
    responseReceived: true,
    status: "retryable",
    phase: "compute_claim_pending",
    errorCode: "fabric_compute_claim_pending"
  };
  const artifact = blockedProductionBasicAcceptanceBArtifact(error);
  assert.deepEqual(validateProductionBasicAcceptanceBArtifact(artifact, approval), artifact);
  assert.deepEqual(artifact.launchReadback, error.launchReadback);
  assert.deepEqual(
    validateProductionBasicAcceptanceBArtifact(readbackFixture(approval), approval),
    readbackFixture(approval)
  );
  const noResponse = blockedProductionBasicAcceptanceBArtifact("production_basic_acceptance_b_launch_readback_unknown");
  assert.deepEqual(validateProductionBasicAcceptanceBArtifact(noResponse, approval).launchReadback, {
    responseReceived: false,
    status: "unknown",
    phase: "unknown",
    errorCode: "unknown"
  });
  assert.throws(() => validateProductionBasicAcceptanceBArtifact({
    ...artifact,
    launchReadback: { ...artifact.launchReadback, accountId: ACCOUNT_ID }
  }, approval), /production_basic_acceptance_b_blocked_readback_invalid/);
  assert.throws(() => validateProductionBasicAcceptanceBArtifact({
    ...artifact,
    launchReadback: { ...artifact.launchReadback, errorCode: "unsafe error with spaces" }
  }, approval), /production_basic_acceptance_b_blocked_readback_invalid/);
});

test("Acceptance B launch stops after one POST when deterministic readback is absent", async () => {
  const approval = parseProductionBasicAcceptanceBApproval(approvalFixture(), {
    approvalId: APPROVAL_ID,
    now: new Date("2026-08-02T00:00:00Z")
  });
  const calls = [];
  const fetchImpl = async (input, init = {}) => {
    const url = new URL(String(input));
    const method = String(init.method || "GET").toUpperCase();
    calls.push({ method, path: url.pathname });
    if (method === "POST") throw new Error("response_lost");
    return response({ error: "not_found" }, 404);
  };
  await assert.rejects(() => submitProductionBasicAcceptanceBLaunch({
    requestOptions: { fetchImpl, origin: "https://cloud.medopl.cn", timeoutMs: 1_000 },
    customerAuth: { cookie: "customer=test", csrfToken: "csrf-test" },
    approval,
    internalServiceToken: "acceptance-b-capability"
  }), /production_basic_acceptance_b_launch_outcome_unknown/);
  assert.equal(calls.filter((call) => call.method === "POST").length, 1);
  assert.equal(calls.filter((call) => call.method === "GET").length, 2);
});

test("Acceptance B launch classifies a deterministic HTTP rejection without treating it as unknown", async () => {
  const approval = parseProductionBasicAcceptanceBApproval(approvalFixture(), {
    approvalId: APPROVAL_ID,
    now: new Date("2026-08-02T00:00:00Z")
  });
  const calls = [];
  const fetchImpl = async (input, init = {}) => {
    const url = new URL(String(input));
    const method = String(init.method || "GET").toUpperCase();
    calls.push({ method, path: url.pathname });
    if (method === "GET" && url.pathname === `/api/workspace-launches/${approval.launch.operationId}`) {
      return response({ error: "not_found" }, 404);
    }
    if (method === "POST" && url.pathname === "/api/workspace-launches") {
      return response({ error: "workspace_launch_admission_disabled" }, 409);
    }
    throw new Error(`unexpected_request:${method}:${url.pathname}`);
  };
  await assert.rejects(() => submitProductionBasicAcceptanceBLaunch({
    requestOptions: { fetchImpl, origin: "https://cloud.medopl.cn", timeoutMs: 1_000 },
    customerAuth: { cookie: "customer=test", csrfToken: "csrf-test" },
    approval,
    internalServiceToken: "acceptance-b-capability"
  }), /production_basic_acceptance_b_launch_rejected_http_409_workspace_launch_admission_disabled/);
  assert.equal(calls.filter((call) => call.method === "POST").length, 1);
  assert.equal(calls.filter((call) => call.method === "GET").length, 1);
});

test("Acceptance B stage budgets separately prove CVM create, ownership, Node, storage, and continuation", () => {
  const approval = approvalFixture();
  const launch = {
    computeAllocationId: "ca_acceptance_b_01",
    storageId: "vol_acceptance_b_01",
    attachmentId: "att_acceptance_b_01",
    continuationAttemptBudgets: Object.fromEntries(["attachment", "secret", "runtime", "activation", "receipt"].map((stage) => [stage, exactStageBudgets()[stage]]))
  };
  const operations = [
    { action: "create_compute_allocation", status: "succeeded", resourceId: launch.computeAllocationId, redactedProviderPayload: { normalLaunchMutationBudget: {
      compute_create: exactStageBudgets().compute_create,
      compute_claim_cvm: exactStageBudgets().compute_claim_cvm,
      compute_claim_node: exactStageBudgets().compute_claim_node
    } } },
    { action: "create_storage_volume", status: "succeeded", resourceId: launch.storageId, redactedProviderPayload: { normalLaunchMutationBudget: {
      cbs_create: exactStageBudgets().cbs_create,
      static_binding_apply: exactStageBudgets().static_binding_apply
    } } },
    { action: "create_storage_attachment", status: "succeeded", resourceId: launch.attachmentId },
    { action: "upsert_gateway_secret", status: "succeeded" },
    { action: "create_workspace_runtime", status: "succeeded" }
  ];
  assert.deepEqual(productionBasicAcceptanceBStageBudgets(operations, launch), exactStageBudgets());
  assert.deepEqual(validateProductionBasicAcceptanceBStageBudgets(exactStageBudgets()), exactStageBudgets());
  for (const value of [
    exactStageBudgets({ compute_claim_cvm: { attempted: 1, confirmed: 0, unknown: 1, max: 1 } }),
    exactStageBudgets({ unexpected: { attempted: 1, confirmed: 1, unknown: 0, max: 1 } }),
    Object.fromEntries(Object.entries(exactStageBudgets()).filter(([stage]) => stage !== "static_binding_apply"))
  ]) assert.throws(() => validateProductionBasicAcceptanceBStageBudgets(value), /production_basic_acceptance_b_stage_budgets_invalid/);
  assert.equal(approval.expected.nodePoolId, "np-basic-acceptance-b");
});

test("Acceptance B readback proves the exact fresh Basic resource chain and terminal URL", () => {
  const approval = parseProductionBasicAcceptanceBApproval(approvalFixture(), {
    approvalId: APPROVAL_ID,
    now: new Date("2026-08-02T00:00:00Z")
  });
  const readback = readbackFixture(approval);
  assert.deepEqual(validateProductionBasicAcceptanceBReadback(readback, approval), readback);
});

test("Acceptance B readback fails closed on non-fresh baseline, count, image, receipt, or URL drift", () => {
  const approval = parseProductionBasicAcceptanceBApproval(approvalFixture(), {
    approvalId: APPROVAL_ID,
    now: new Date("2026-08-02T00:00:00Z")
  });
  const base = readbackFixture(approval);
  const cases = [
    { ...base, baseline: { ...base.baseline, workspaceCount: 1 } },
    { ...base, writeCounts: exactWriteCounts({ receiptCreates: 0 }) },
    { ...base, stageBudgets: exactStageBudgets({ static_binding_apply: { attempted: 1, confirmed: 0, unknown: 1, max: 1 } }) },
    { ...base, runtime: { ...base.runtime, podImageId: `containerd://workspace@sha256:${"d".repeat(64)}` } },
    { ...base, receipt: { ...base.receipt, workspaceId: "ws-drift" } },
    { ...base, workspaceUrl: { ...base.workspaceUrl, statusCode: 503 } }
  ];
  for (const value of cases) {
    assert.throws(() => validateProductionBasicAcceptanceBReadback(value, approval), /production_basic_acceptance_b_readback_invalid/);
  }
});
