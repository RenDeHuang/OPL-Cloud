import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  PRODUCTION_BASIC_ACCEPTANCE_B_ALLOWED_WRITES,
  PRODUCTION_BASIC_ACCEPTANCE_B_CONFIRMATION,
  PRODUCTION_BASIC_ACCEPTANCE_B_FORBIDDEN_WRITES,
  PRODUCTION_BASIC_ACCEPTANCE_B_OPERATION,
  PRODUCTION_BASIC_ACCEPTANCE_B_ACCOUNT_PREPARE_OPERATION,
  blockedProductionBasicAcceptanceBPrepareArtifact,
  findUniqueProductionBasicAcceptanceBAccount,
  findUniqueProductionBasicAcceptanceBEmailAccount,
  parseProductionBasicAcceptanceBApproval,
  productionBasicAcceptanceBApprovalDigest,
  validateProductionBasicAcceptanceBPrepareReadback,
  validateProductionBasicAcceptanceBReadback,
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
    writeCounts: exactWriteCounts()
  };
  return { ...value, ...overrides };
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
    exactWrites: { sub2apiDebit: 1, cvmCreate: 1, nodeClaim: 1, cbsCreate: 1, runtimeCreate: 1, receiptCreate: 1 },
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
    exactWriteCounts({ kubernetesNodeClaims: 0 }),
    exactWriteCounts({ modelRequests: 1 }),
    { ...exactWriteCounts(), unexpected: 0 }
  ]) {
    assert.throws(() => validateProductionBasicAcceptanceBWriteCounts(value), /production_basic_acceptance_b_write_counts_invalid/);
  }
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
    { ...base, runtime: { ...base.runtime, podImageId: `containerd://workspace@sha256:${"d".repeat(64)}` } },
    { ...base, receipt: { ...base.receipt, workspaceId: "ws-drift" } },
    { ...base, workspaceUrl: { ...base.workspaceUrl, statusCode: 503 } }
  ];
  for (const value of cases) {
    assert.throws(() => validateProductionBasicAcceptanceBReadback(value, approval), /production_basic_acceptance_b_readback_invalid/);
  }
});

test("deployment machine contract registers the local-only Acceptance B integration boundary", async () => {
  const deployment = JSON.parse(await readFile(new URL("../../packages/contracts/opl-cloud-deployment-contract.json", import.meta.url), "utf8"));
  assert.deepEqual(deployment.productionBasicAcceptanceB, {
    tool: "tools/production-basic-acceptance-b.ts",
    operationMode: "acceptance_b_fresh_order",
    execution: "github_actions_production_environment_authoritative_readback_workflow",
    productionNetwork: "github_actions_production_environment_authorized_runner_only",
    workflowIntegration: {
      file: ".github/workflows/production-basic-customer-operation.yml",
      job: "acceptance-b-fresh-order",
      ownership: "integrated_after_acceptance_a_identity_lane_on_main",
      resourceClosureRunner: ["self-hosted", "tencent-cloud", "opl-cloud", "tke-vpc"],
      modelRequest: "forbidden_not_part_of_acceptance_b"
    },
    operationContract: PRODUCTION_BASIC_ACCEPTANCE_B_OPERATION,
    approvalSchema: {
      schemaVersion: 1,
      exactTopLevelFields: ["schemaVersion", "operationMode", "approvalId", "expiresAt", "confirmation", "release", "customer", "launch", "expected", "allowedWrites", "forbiddenWrites"],
      releaseFields: ["mergedMainSha", "cloudImageDigest", "workspaceImageDigest"],
      customerFields: ["email", "accountId"],
      launchFields: ["idempotencyKey", "operationId", "workspaceId", "name", "packageId", "sizeGb", "autoRenew"],
      expectedFields: ["nodePoolId", "resolvedInstanceType"],
      allowedWrites: PRODUCTION_BASIC_ACCEPTANCE_B_ALLOWED_WRITES,
      forbiddenWrites: PRODUCTION_BASIC_ACCEPTANCE_B_FORBIDDEN_WRITES
    },
    writeAccounting: {
      authority: "authoritative_service_readback_not_http_attempts",
      exactCountFields: ["workspaceLaunchPosts", "sub2apiDebits", "tencentCvmCreates", "kubernetesNodeClaims", "tencentCbsCreates", "runtimeCreates", "receiptCreates"],
      zeroCountFields: ["accountProvisionPosts", "walletAdjustmentPosts", "modelRequests", "refunds", "renewals", "deletes", "replacements"]
    },
    readback: {
      baseline: "zero_workspace_launch_workspace_key_and_workspace_receipt_for_approved_account",
      authorities: ["control_plane_launch", "sub2api_debit_history", "fabric_operations_and_provider_truth", "runtime_ready_pod", "ledger_purchase_receipt", "workspace_url_http"],
      terminalEvidence: PRODUCTION_BASIC_ACCEPTANCE_B_OPERATION.terminalEvidence,
      forbiddenFields: ["password", "token", "secret", "redeem_code", "provider_request_id", "model_prompt", "model_response"]
    }
  });
});

test("Acceptance B reads only its independent customer password secret", async () => {
  const workflow = await readFile(new URL("../../.github/workflows/production-basic-customer-operation.yml", import.meta.url), "utf8");
  const acceptanceB = workflow.slice(workflow.indexOf("  acceptance-b-fresh-order:"), workflow.indexOf("  controlled-pilot-closed-validate:"));
  assert.match(acceptanceB, /OPL_PRODUCTION_BASIC_ACCEPTANCE_B_CUSTOMER_PASSWORD:\s*\$\{\{ secrets\.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_CUSTOMER_PASSWORD \}\}/);
  assert.doesNotMatch(acceptanceB, /OPL_BASIC_CANARY_CUSTOMER_PASSWORD/);
});
