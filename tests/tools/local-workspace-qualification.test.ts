import assert from "node:assert/strict";
import { readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  exactRepoDigestFromInspection,
  immutableImageDigest,
  liveAuthorityAdjustmentReadback,
  localBuildProxyArgs,
  parseLocalQualificationArgs,
  redactedError,
  validateQualificationSourceIdentity,
  validateLocalQualificationReceipt,
  validateProductMatrixReceipt
} from "../../tools/local-workspace-qualification.ts";
import { runLocalWorkspaceQualification } from "../../tools/local-workspace-qualification.ts";
import {
  productMatrixRequiredPackages,
  productMatrixRequiredTests
} from "../../tools/verify-local.ts";

const sha = "a".repeat(40);
const cloudDigest = `sha256:${"b".repeat(64)}`;
const workspaceDigest = `sha256:${"c".repeat(64)}`;
const workspaceReference = `ghcr.io/example/workspace@${workspaceDigest}`;

test("source image tag inspection hands off one exact immutable RepoDigest", () => {
  const repository = "127.0.0.1:56287/opl-local-qualification-cloud";
  const exact = `${repository}@${cloudDigest}`;
  assert.equal(exactRepoDigestFromInspection(repository, {
    Id: cloudDigest,
    RepoDigests: [exact]
  }), exact);

  for (const [name, inspection] of Object.entries({
    missing: { Id: cloudDigest, RepoDigests: [] },
    "wrong repository": { Id: cloudDigest, RepoDigests: [`127.0.0.1:56287/other@${cloudDigest}`] },
    mutable: { Id: cloudDigest, RepoDigests: [`${repository}:source`] },
    duplicate: { Id: cloudDigest, RepoDigests: [exact, exact] },
    "invalid image identity": { Id: "sha256:not-a-digest", RepoDigests: [exact] }
  })) {
    assert.throws(() => exactRepoDigestFromInspection(repository, inspection), /source-built image/, name);
  }
});

function liveAuthorityHistoryFetch({ duplicate = false } = {}) {
  const requestedPages = [];
  const request = async (input, init = {}) => {
    const url = new URL(String(input));
    if (url.pathname === "/api/v1/auth/login") {
      assert.equal(init.method, "POST");
      return new Response(JSON.stringify({ code: 0, data: { access_token: "test-access" } }), {
        status: 200, headers: { "content-type": "application/json" }
      });
    }
    assert.equal(url.pathname, "/api/v1/admin/users/41/balance-history");
    assert.equal(url.searchParams.get("page_size"), "100");
    assert.equal(url.searchParams.get("type"), "balance");
    const page = Number(url.searchParams.get("page"));
    assert.ok(Number.isInteger(page) && page >= 1 && page <= 3);
    requestedPages.push(page);
    const count = page === 3 ? 1 : 100;
    const items = Array.from({ length: count }, (_, index) => {
      const target = page === 2 && index === 42 || duplicate && page === 3;
      return {
        code: target ? "opl:target" : `opl:filler:${(page - 1) * 100 + index}`,
        type: "balance", value: "-52.580000", status: "used", used_by: 41,
        used_at: "2026-07-16T00:01:00Z", created_at: "2026-07-16T00:00:00Z"
      };
    });
    return new Response(JSON.stringify({ code: 0, data: { items, total: 201, page, page_size: 100, pages: 3 } }), {
      status: 200, headers: { "content-type": "application/json" }
    });
  };
  return { request, requestedPages };
}

test("local qualification accepts exact source and immutable image identities", () => {
  assert.deepEqual(parseLocalQualificationArgs([
    "--source-sha", sha,
    "--cloud-image", `ghcr.io/example/cloud@${cloudDigest}`,
    "--workspace-image", workspaceReference,
    "--receipt", "/tmp/qualification.json"
  ]), {
    sourceSha: sha,
    cloudImage: `ghcr.io/example/cloud@${cloudDigest}`,
    workspaceImage: workspaceReference,
    receiptPath: "/tmp/qualification.json",
    buildSourceImages: false,
    authorityMode: "fixture",
    productMatrixReceipt: ""
  });
  assert.equal(immutableImageDigest(`ghcr.io/example/cloud@${cloudDigest}`), cloudDigest);
  assert.equal(immutableImageDigest(workspaceDigest), workspaceDigest);
  assert.throws(() => parseLocalQualificationArgs(["--source-sha", "main"]), /source SHA/);
  assert.throws(() => parseLocalQualificationArgs([
    "--source-sha", sha, "--cloud-image", `ghcr.io/example/cloud@${cloudDigest}`, "--workspace-image", workspaceReference,
    "--receipt", "/tmp/qualification.json", "--authority-mode", "production"
  ]), /authority mode/);
  assert.throws(() => parseLocalQualificationArgs([
    "--source-sha", sha,
    "--cloud-image", "ghcr.io/example/cloud:latest",
    "--workspace-image", workspaceReference,
    "--receipt", "/tmp/qualification.json"
  ]), /immutable cloud image/);
});

test("live authority adjustment readback counts the exact code through the final financial page", async () => {
  const exact = liveAuthorityHistoryFetch();
  const evidence = await liveAuthorityAdjustmentReadback(
    "https://sandbox.example", "admin@example.test", "password", "41", "opl:target", "-52580000", exact.request
  );
  assert.deepEqual(exact.requestedPages, [1, 2, 3]);
  assert.deepEqual(evidence, { code: "opl:target", userId: "41", valueUsdMicros: "-52580000", status: "used", count: 1 });

  const duplicate = liveAuthorityHistoryFetch({ duplicate: true });
  await assert.rejects(() => liveAuthorityAdjustmentReadback(
    "https://sandbox.example", "admin@example.test", "password", "41", "opl:target", "-52580000", duplicate.request
  ), /cardinality/);
  assert.deepEqual(duplicate.requestedPages, [1, 2, 3]);
});

test("READY receipt binds the exact durable and accounting evidence", () => {
  const receipt = {
    schemaVersion: 1,
    status: "READY",
    source: { sha, tree: "d".repeat(40) },
    images: {
      cloud: { input: `ghcr.io/example/cloud@${cloudDigest}`, repoDigest: `ghcr.io/example/cloud@${cloudDigest}`, digest: cloudDigest, runningDigest: cloudDigest },
      workspace: { input: workspaceReference, repoDigest: workspaceReference, digest: workspaceDigest, runningDigest: workspaceDigest }
    },
    command: "npm run qualify:local:workspace -- --source-sha [redacted]",
    processes: { console: "ready", controlPlane: "ready", fabric: "ready", ledger: "ready" },
    stores: { controlPlane: "durable", fabric: "durable", ledger: "durable", ownerSeparated: true },
    identities: {
      accountId: "acct-admin", sub2apiUserId: "41", launchOperationId: "workspace-launch-alpha",
      deleteOperationId: "workspace-delete-alpha", refundOperationId: "workspace-delete-alpha", workspaceId: "ws-alpha", runtimeId: "rt-alpha", keyId: "71",
      debitCode: "opl:qualification-alpha", purchaseReceiptId: "receipt-alpha", refundReceiptId: "receipt-refund"
    },
    debit: { count: 1, accountId: "acct-admin", operationId: "workspace-launch-alpha", workspaceId: "ws-alpha", code: "opl:qualification-alpha", userId: "41", amountUsdMicros: "52580000" },
    wallet: { beforeUsdMicros: "100000000", afterUsdMicros: "47420000", restoredUsdMicros: "100000000" },
    receipt: {
      count: 1, id: "receipt-alpha", accountId: "acct-admin", operationId: "workspace-launch-alpha", workspaceId: "ws-alpha", runtimeId: "rt-alpha",
      keyId: "71", chargeReference: "opl:qualification-alpha", amountUsdMicros: "52580000"
    },
    restart: { performed: true, operationStable: true, workspaceStable: true, runtimeStable: true, receiptStable: true },
    deletion: {
      ownerAuthorized: true, accountId: "acct-admin", operationId: "workspace-delete-alpha", refundOperationId: "workspace-delete-alpha", workspaceId: "ws-alpha", runtimeId: "rt-alpha", keyId: "71",
      workspaceAbsent: true, runtimeAbsent: true, workspaceKeyAbsent: true, fabricSecretAbsent: true
    },
    residuals: { containers: 0, volumes: 0, networks: 0 },
    authorityWriteCounts: { keyCreates: 1, keyDeletes: 1, debits: 1, refunds: 1 },
    mutationCounts: { workspaceLaunchPosts: 1, workspaceDeleteRequests: 1, refundPosts: 0 },
    refund: {
      count: 1, accountId: "acct-admin", operationId: "workspace-delete-alpha", workspaceId: "ws-alpha", debitCode: "opl:qualification-alpha",
      code: "opl:qualification-refund", userId: "41", amountUsdMicros: "52580000", receiptId: "receipt-refund"
    },
    refundReceipt: {
      count: 1, id: "receipt-refund", type: "billing.workspace_refunded.v1", accountId: "acct-admin", operationId: "workspace-delete-alpha",
      workspaceId: "ws-alpha", chargeReference: "opl:qualification-alpha", refundCode: "opl:qualification-refund", amountUsdMicros: "52580000"
    },
    usage: { source: "sub2api", status: "available", totalRequests: 0 },
    productMatrix: {
      digest: `sha256:${"e".repeat(64)}`,
      stages: ["key", "debit", "ensure_compute_allocation", "storage", "attachment", "secret", "runtime", "activation", "receipt"],
      packages: [...productMatrixRequiredPackages],
      tests: productMatrixRequiredTests.map((entry) => `${entry.package}:${entry.name}`),
      zeroSkip: true,
      casWinnerCount: 1,
      unknownAuthorityWriteDeltas: { controlPlane: 0, sub2api: 0, fabric: 0, ledger: 0 }
    },
    qualification: { authorityMode: "live", p0Ready: true },
    deferred: ["tencent-tke", "production-sub2api", "production-secrets"]
  };
  assert.equal(validateLocalQualificationReceipt(receipt), receipt);

  assert.throws(() => validateLocalQualificationReceipt({ ...receipt, status: "NOT_READY" }), /READY receipt/);
  assert.throws(() => validateLocalQualificationReceipt({ ...receipt, debit: { ...receipt.debit, count: 2 } }), /debit/);
  const concurrentUsageReceipt = { ...receipt, wallet: { ...receipt.wallet, afterUsdMicros: "47420001", restoredUsdMicros: "99999999" } };
  assert.equal(validateLocalQualificationReceipt(concurrentUsageReceipt), concurrentUsageReceipt);
  assert.throws(() => validateLocalQualificationReceipt({ ...receipt, restart: { ...receipt.restart, runtimeStable: false } }), /restart/);
  assert.throws(() => validateLocalQualificationReceipt({ ...receipt, residuals: { ...receipt.residuals, volumes: 1 } }), /residual/);
  assert.throws(() => validateLocalQualificationReceipt({ ...receipt, mutationCounts: { ...receipt.mutationCounts, refundPosts: 1 } }), /mutation counts/);
  assert.throws(() => validateLocalQualificationReceipt({
    ...receipt, refundReceipt: { ...receipt.refundReceipt, type: "gateway.wallet_adjustment.v1" }
  }), /refund receipt/);
  assert.throws(() => validateLocalQualificationReceipt({
    ...receipt, deletion: { ...receipt.deletion, keyId: "72" }
  }), /owner deletion/);
  const fixtureReceipt = { ...receipt, qualification: { authorityMode: "fixture", p0Ready: false } };
  assert.throws(() => validateLocalQualificationReceipt({ ...fixtureReceipt, qualification: { authorityMode: "fixture", p0Ready: true } }), /authority classification/);
  assert.throws(() => validateLocalQualificationReceipt({ ...fixtureReceipt, wallet: { ...receipt.wallet, afterUsdMicros: "47420001" } }), /fixture wallet/);
  assert.throws(() => validateLocalQualificationReceipt({ ...fixtureReceipt, authorityWriteCounts: { ...receipt.authorityWriteCounts, debits: 2 } }), /fixture write counts/);
});

test("local build proxy rejects credentials or URL parameters and errors redact the entire URL authority and query", () => {
  const previous = process.env.OPL_LOCAL_BUILD_PROXY;
  try {
    process.env.OPL_LOCAL_BUILD_PROXY = "http://builder:secret@127.0.0.1:3128";
    assert.throws(() => localBuildProxyArgs(), /must not contain credentials/);
    assert.doesNotMatch(redactedError(new Error("pull https://builder:secret@example.test/v2 failed")), /builder|secret/);
    assert.match(redactedError(new Error("pull https://builder:secret@example.test/v2 failed")), /https:\/\/\[redacted\]@example\.test/);
    process.env.OPL_LOCAL_BUILD_PROXY = "https://proxy.example/path?token=s3cr3t";
    assert.throws(() => localBuildProxyArgs(), /must not contain query or fragment/);
    const queryRedacted = redactedError(new Error("build https://proxy.example/path?token=s3cr3t#credential failed"));
    assert.doesNotMatch(queryRedacted, /s3cr3t|credential/);
    assert.match(queryRedacted, /\?\[redacted\]/);
  } finally {
    if (previous === undefined) delete process.env.OPL_LOCAL_BUILD_PROXY;
    else process.env.OPL_LOCAL_BUILD_PROXY = previous;
  }
});

test("qualification source identity requires one clean unchanged HEAD and tree", () => {
  const source = { sha, tree: "d".repeat(40), clean: true };
  assert.deepEqual(validateQualificationSourceIdentity(source, source, sha), source);
  assert.throws(() => validateQualificationSourceIdentity({ ...source, clean: false }, source, sha), /clean/);
  assert.throws(() => validateQualificationSourceIdentity(source, { ...source, tree: "e".repeat(40) }, sha), /changed/);
  assert.throws(() => validateQualificationSourceIdentity(source, source, "f".repeat(40)), /requested source/);
});

test("Product matrix receipt admission binds exact source, nine stages, CAS, and unknown zero deltas", () => {
  const matrix = {
    schemaVersion: 1,
    status: "READY",
    source: { sha, tree: "d".repeat(40) },
    zeroSkip: true,
    stages: ["key", "debit", "ensure_compute_allocation", "storage", "attachment", "secret", "runtime", "activation", "receipt"]
      .map((name) => ({ name, passed: true, skipped: 0 })),
    cas: { winnerCount: 1, loserMutationCount: 0 },
    unknown: { authorityWriteDeltas: { controlPlane: 0, sub2api: 0, fabric: 0, ledger: 0 } },
    packages: productMatrixRequiredPackages.map((name) => ({ name, passed: true, skipped: 0 })),
    tests: productMatrixRequiredTests.map((entry) => ({ ...entry, passed: true, skipped: 0 }))
  };
  assert.equal(validateProductMatrixReceipt(matrix, sha, "d".repeat(40)), matrix);
  assert.throws(() => validateProductMatrixReceipt({ ...matrix, stages: [] }, sha, "d".repeat(40)), /nine-stage/);
  assert.throws(() => validateProductMatrixReceipt({ ...matrix, cas: { winnerCount: 2, loserMutationCount: 0 } }, sha, "d".repeat(40)), /CAS/);
  assert.throws(() => validateProductMatrixReceipt({
    ...matrix, unknown: { authorityWriteDeltas: { ...matrix.unknown.authorityWriteDeltas, ledger: 1 } }
  }, sha, "d".repeat(40)), /write deltas/);
  assert.throws(() => validateProductMatrixReceipt({ ...matrix, packages: matrix.packages.slice(1) }, sha, "d".repeat(40)), /package evidence/);
  assert.throws(() => validateProductMatrixReceipt({ ...matrix, tests: matrix.tests.slice(1) }, sha, "d".repeat(40)), /test evidence/);
  assert.throws(() => validateProductMatrixReceipt({
    ...matrix, tests: [{ ...matrix.tests[0], skipped: 1 }, ...matrix.tests.slice(1)]
  }, sha, "d".repeat(40)), /test evidence/);
});

test("package exposes one local Workspace qualification command", async () => {
  const packageJson = JSON.parse(await readFile(new URL("../../package.json", import.meta.url), "utf8"));
  assert.equal(packageJson.scripts["qualify:local:workspace"], "node tools/local-workspace-qualification.ts");
  const compose = await readFile(new URL("../../deploy/portable/compose.local-workspace.yaml", import.meta.url), "utf8");
  assert.match(compose, /source: \$\{OPL_FABRIC_LOCAL_DOCKER_SECRET_ROOT:\?Set a task-owned local Docker Secret root\}/);
  assert.match(compose, /target: \$\{OPL_FABRIC_LOCAL_DOCKER_SECRET_ROOT:\?Set a task-owned local Docker Secret root\}/);
  assert.match(compose, /OPL_FABRIC_LOCAL_DOCKER_SECRET_ROOT: \$\{OPL_FABRIC_LOCAL_DOCKER_SECRET_ROOT:\?Set a task-owned local Docker Secret root\}/);
  const envExample = await readFile(new URL("../../deploy/portable/opl-cloud.env.example", import.meta.url), "utf8");
  assert.match(envExample, /^OPL_FABRIC_LOCAL_DOCKER_SECRET_ROOT=\/absolute\/path\/to\/opl-fabric-secrets$/m);
  const runner = await readFile(new URL("../../tools/local-workspace-qualification.ts", import.meta.url), "utf8");
  assert.match(runner, /OPL_FABRIC_LOCAL_DOCKER_SECRET_ROOT=\$\{fabricSecretRoot\}/);
  assert.match(runner, /exactRepoDigestFromInspection\(cloudRepository, await dockerImageInspection\(cloudTag\)\)/);
  assert.match(runner, /exactRepoDigestFromInspection\(workspaceRepository, await dockerImageInspection\(workspaceTag\)\)/);
  assert.match(runner, /await imageInspection\(cloudImage\)/);
  assert.match(runner, /await imageInspection\(workspaceImage\)/);
  assert.doesNotMatch(runner, /imageInspection\((?:cloud|workspace)Tag\)/);
});

test("live authority configuration fails closed before Docker and writes a redacted receipt", async () => {
  const receiptPath = join(tmpdir(), `local-live-preflight-${process.pid}.json`);
  const previous = {
    baseURL: process.env.OPL_SUB2API_BASE_URL,
    email: process.env.OPL_SUB2API_ADMIN_EMAIL,
    password: process.env.OPL_SUB2API_ADMIN_PASSWORD,
    authorityClass: process.env.OPL_QUALIFICATION_AUTHORITY_CLASS
  };
  delete process.env.OPL_SUB2API_BASE_URL;
  delete process.env.OPL_SUB2API_ADMIN_EMAIL;
  delete process.env.OPL_SUB2API_ADMIN_PASSWORD;
  delete process.env.OPL_QUALIFICATION_AUTHORITY_CLASS;
  try {
    await assert.rejects(() => runLocalWorkspaceQualification({
      sourceSha: sha,
      cloudImage: `ghcr.io/example/cloud@${cloudDigest}`,
      workspaceImage: workspaceReference,
      receiptPath,
      buildSourceImages: false,
      authorityMode: "live"
    }), /protected non-production Sub2API credentials/);
    const receipt = JSON.parse(await readFile(receiptPath, "utf8"));
    assert.equal(receipt.status, "NOT_READY");
    assert.equal(receipt.stage, "authority_preflight");
    assert.equal(receipt.errorCode, "live_authority_configuration_missing");
  } finally {
    for (const [name, value] of Object.entries({
      OPL_SUB2API_BASE_URL: previous.baseURL,
      OPL_SUB2API_ADMIN_EMAIL: previous.email,
      OPL_SUB2API_ADMIN_PASSWORD: previous.password,
      OPL_QUALIFICATION_AUTHORITY_CLASS: previous.authorityClass
    })) {
      if (value === undefined) delete process.env[name];
      else process.env[name] = value;
    }
    await rm(receiptPath, { force: true });
  }
});
