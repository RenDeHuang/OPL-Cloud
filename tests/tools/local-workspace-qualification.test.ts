import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { chmod, mkdtemp, readFile, rm, symlink, writeFile } from "node:fs/promises";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  continueWorkspaceDelete,
  createHTTP,
  exactRepoDigestFromInspection,
  immutableImageDigest,
  liveAuthorityAdjustmentReadback,
  login,
  loadJ0ReadyReceipt,
  loadSub2APISecretFile,
  localBuildProxyArgs,
  qualificationEnvFileEntries,
  qualificationComposeEnvironment,
  parseLocalQualificationArgs,
  redactedError,
  runLocalWorkspaceJ1HTTPQualification,
  sourceData,
  stableID,
  validateQualificationSourceIdentity,
  validateJ0ReadyReceipt,
  validateLocalJ1AccountingReadback,
  validateLocalQualificationReceipt,
  validateProductMatrixReceipt,
  workspaceDeleteFailureEvidence
} from "../../tools/local-workspace-qualification.ts";
import { runLocalWorkspaceQualification } from "../../tools/local-workspace-qualification.ts";
import {
  productMatrixLaneSpecs,
  productMatrixRequiredPackages,
  productMatrixRequiredTests,
  productMatrixVerticalPackage,
  productMatrixVerticalTests
} from "../../tools/verify-local.ts";

const sha = "a".repeat(40);
const cloudDigest = `sha256:${"b".repeat(64)}`;
const workspaceDigest = `sha256:${"c".repeat(64)}`;
const workspaceReference = `ghcr.io/example/workspace@${workspaceDigest}`;

function completeMatrixLanes() {
  return productMatrixLaneSpecs.map((spec) => {
    const packages = spec.cwd === "." ? [productMatrixVerticalPackage] :
      productMatrixRequiredPackages.filter((name) => name === `opl-cloud/${spec.cwd}` || name.startsWith(`opl-cloud/${spec.cwd}/`));
    const passedTests = spec.cwd === "." ?
      productMatrixVerticalTests.map((name) => ({ package: productMatrixVerticalPackage, name })) :
      productMatrixRequiredTests.filter((entry) => packages.includes(entry.package)).map((entry) => ({ ...entry }));
    return {
      order: spec.order,
      cwd: spec.cwd,
      command: spec.command,
      args: spec.cwd === "." ? [...spec.argsPrefix] : [...spec.argsPrefix, ...packages],
      packages,
      failed: 0,
      skipped: 0,
      passedPackages: [...packages],
      passedTests
    };
  });
}

test("owner delete continues only from exact durable compute pending evidence", async () => {
  const path = "/api/workspaces/ws-alpha";
  const init = { method: "DELETE", headers: { "idempotency-key": "delete-alpha" }, body: {} };
  const auth = { cookie: "session=test", csrf: "csrf-test" };
  const operationId = "workspace-delete-alpha";
  const responses = [
    { response: { status: 202, headers: new Headers({ "retry-after": "1" }) }, payload: { status: "pending", phase: "storage_destroyed", ownerStage: "compute", computeStatus: "destroying", operationId, workspaceId: "ws-alpha", computeReadbacks: 1, maxComputeReadbacks: 8 } },
    { response: { status: 202, headers: new Headers({ "retry-after": "1" }) }, payload: { status: "pending", phase: "storage_destroyed", ownerStage: "compute", computeStatus: "destroying", operationId, workspaceId: "ws-alpha", computeReadbacks: 2, maxComputeReadbacks: 8 } },
    { response: { status: 200, headers: new Headers() }, payload: { status: "deleted", operationId, workspaceId: "ws-alpha" } }
  ];
  const calls = [];
  const waits = [];
  const pendingEvidence = [];
  const http = {
    json: async (...args) => {
      calls.push(args);
      return responses.shift();
    }
  };

  const deletion = await continueWorkspaceDelete(http, path, init, auth, {
    operationId, workspaceId: "ws-alpha", onPending: (pending) => pendingEvidence.push(pending)
  }, async (milliseconds) => waits.push(milliseconds));
  assert.equal(deletion.status, "deleted");
  assert.equal(calls.length, 3);
  assert.deepEqual(waits, [1000, 1000]);
  for (const call of calls) assert.deepEqual(call, [path, init, auth, [200, 202]]);
  assert.equal(pendingEvidence.length, 2);
  assert.deepEqual(pendingEvidence.at(-1), {
    phase: "storage_destroyed", ownerStage: "compute", ordinal: 2, max: 8,
    operationDigest: `sha256:${createHash("sha256").update(operationId).digest("hex")}`
  });
  assert.doesNotMatch(JSON.stringify(pendingEvidence), /workspace-delete-alpha|ws-alpha/);
});

test("owner delete response-loss continuation preserves the consumed read ordinal", async () => {
  const responses = [
    { response: { status: 202, headers: new Headers({ "retry-after": "1" }) }, payload: {
      status: "pending", phase: "storage_destroyed", ownerStage: "compute", computeStatus: "destroying",
      operationId: "workspace-delete-alpha", workspaceId: "ws-alpha", computeReadbacks: 2, maxComputeReadbacks: 8
    } },
    { response: { status: 200, headers: new Headers() }, payload: {
      status: "deleted", operationId: "workspace-delete-alpha", workspaceId: "ws-alpha"
    } }
  ];
  const waits = [];
  const result = await continueWorkspaceDelete({ json: async () => responses.shift() }, "/api/workspaces/ws-alpha", {
    method: "DELETE"
  }, null, {
    operationId: "workspace-delete-alpha", workspaceId: "ws-alpha", initialReadback: 1, initialMaxReadbacks: 8
  }, async (milliseconds) => waits.push(milliseconds));
  assert.equal(result.status, "deleted");
  assert.deepEqual(waits, [1000]);
  assert.deepEqual(workspaceDeleteFailureEvidence(new Error("The operation was aborted due to timeout")), {
    status: 0, reasonCode: "request_timeout"
  });
});

test("owner delete rejects pending identity, ordinal, and budget drift", async () => {
  const base = { status: "pending", phase: "storage_destroyed", ownerStage: "compute", computeStatus: "destroying", operationId: "workspace-delete-alpha", workspaceId: "ws-alpha", computeReadbacks: 1, maxComputeReadbacks: 8 };
  for (const [name, pending] of Object.entries({
    identity: { ...base, operationId: "workspace-delete-other" },
    ordinal: { ...base, computeReadbacks: 2 },
    budget: { ...base, maxComputeReadbacks: 9 },
    exhausted: { ...base, computeReadbacks: 8 }
  })) {
    const http = { json: async () => ({ response: { status: 202 }, payload: pending }) };
    await assert.rejects(() => continueWorkspaceDelete(http, "/api/workspaces/ws-alpha", { method: "DELETE" }, null, {
      operationId: "workspace-delete-alpha", workspaceId: "ws-alpha"
    }), /pending evidence/, name);
  }
});

test("owner delete failure keeps only redacted last pending evidence", async () => {
  const operationId = "workspace-delete-secret-alpha";
  const pending = {
    status: "pending", phase: "storage_destroyed", ownerStage: "compute", computeStatus: "destroying",
    operationId, workspaceId: "ws-secret-alpha", computeReadbacks: 1, maxComputeReadbacks: 8
  };
  const evidence = [];
  let calls = 0;
  const http = {
    json: async (_path, _init, _auth, statuses) => {
      if (statuses.includes(202)) {
        calls += 1;
        if (calls === 1) return { response: { status: 202, headers: new Headers({ "retry-after": "1" }) }, payload: pending };
        throw new Error(`HTTP 502 operation=${operationId} workspace=${pending.workspaceId}`);
      }
      throw new Error("unexpected status set");
    }
  };
  await assert.rejects(() => continueWorkspaceDelete(http, "/api/workspaces/ws-secret-alpha", { method: "DELETE" }, null, {
    operationId, workspaceId: pending.workspaceId, onPending: (value) => evidence.push(value)
  }, async () => {}), /continuation failed/);
  assert.equal(evidence.length, 1);
  assert.deepEqual(evidence[0], {
    phase: "storage_destroyed", ownerStage: "compute", ordinal: 1, max: 8,
    operationDigest: `sha256:${createHash("sha256").update(operationId).digest("hex")}`
  });
  assert.doesNotMatch(JSON.stringify(evidence), /workspace-delete-secret-alpha|ws-secret-alpha/);
});

test("qualification compose uses the runner-owned exact environment", () => {
  const exactSecretRoot = "/tmp/opl-qualification/fabric-secrets";
  const environment = qualificationComposeEnvironment({
    PATH: "/usr/bin",
    OPL_WORKSPACE_IMAGE: "registry.example/stale@sha256:" + "d".repeat(64),
    OPL_FABRIC_LOCAL_DOCKER_SECRET_ROOT: "/tmp/stale-secrets"
  }, [
    ["OPL_WORKSPACE_IMAGE", workspaceReference],
    ["OPL_FABRIC_LOCAL_DOCKER_SECRET_ROOT", exactSecretRoot]
  ]);

  assert.equal(environment.PATH, "/usr/bin");
  assert.equal(environment.OPL_WORKSPACE_IMAGE, workspaceReference);
  assert.equal(environment.OPL_FABRIC_LOCAL_DOCKER_SECRET_ROOT, exactSecretRoot);
});

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
    productMatrixReceipt: "",
    j0ReadyReceipt: "",
    sub2apiSecretFile: ""
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

function j0ReadyReceipt(sourceSha = sha, sourceTree = "d".repeat(40)) {
  return {
    schemaVersion: 1,
    kind: "opl.local-workspace.j0-ready.v1",
    status: "READY",
    source: { sha: sourceSha, tree: sourceTree, clean: true },
    gates: [
      ...["E0", "E1", "E2", "E3"].map((id) => ({ id, status: "GREEN", failed: 0, skipped: 0 })),
      { id: "E4", status: "BLOCKED_PRODUCT_DECISION_OUT_OF_P0", failed: 0, skipped: 0 },
      ...["E5", "E6", "E7"].map((id) => ({ id, status: "GREEN", failed: 0, skipped: 0 }))
    ],
    authority: { mode: "live", class: "sandbox", dedicated: true, confirmed: true },
    provider: "local-docker",
    failed: 0,
    skipped: 0
  };
}

test("live arguments require one external J0 READY receipt instead of a Product matrix", () => {
  const parsed = parseLocalQualificationArgs([
    "--source-sha", sha,
    "--cloud-image", `ghcr.io/example/cloud@${cloudDigest}`,
    "--workspace-image", workspaceReference,
    "--receipt", "/tmp/qualification.json",
    "--authority-mode", "live",
    "--j0-ready-receipt", "/tmp/j0-ready.json"
  ]);
  assert.equal(parsed.j0ReadyReceipt, "/tmp/j0-ready.json");
  assert.equal(parsed.productMatrixReceipt, "");
  assert.throws(() => parseLocalQualificationArgs([
    "--source-sha", sha,
    "--cloud-image", `ghcr.io/example/cloud@${cloudDigest}`,
    "--workspace-image", workspaceReference,
    "--receipt", "/tmp/qualification.json",
    "--authority-mode", "live"
  ]), /J0 READY receipt/);
  assert.throws(() => parseLocalQualificationArgs([
    "--source-sha", sha,
    "--cloud-image", `ghcr.io/example/cloud@${cloudDigest}`,
    "--workspace-image", workspaceReference,
    "--receipt", "/tmp/qualification.json",
    "--authority-mode", "live",
    "--j0-ready-receipt", "/tmp/j0-ready.json",
    "--product-matrix-receipt", "/tmp/product-matrix.json"
  ]), /Product matrix/);
});

test("J0 READY admission binds exact clean source, P0 gates, authority, provider, and external 0600 file", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-j0-ready-test-"));
  const path = join(root, "j0-ready.json");
  const link = join(root, "j0-ready-link.json");
  const parentLink = join(root, "repo-parent");
  const value = j0ReadyReceipt();
  await writeFile(path, JSON.stringify(value), { mode: 0o600 });
  try {
    assert.equal(validateJ0ReadyReceipt(value, sha, "d".repeat(40)), value);
    const loaded = await loadJ0ReadyReceipt(path, sha, "d".repeat(40));
    assert.match(loaded.digest, /^sha256:[0-9a-f]{64}$/);
    assert.deepEqual(loaded.source, value.source);
    assert.deepEqual(loaded.gates, value.gates);
    for (const invalid of [
      { ...value, kind: "opl.local-workspace.product-matrix.v1" },
      { ...value, status: "NOT_READY" },
      { ...value, source: { ...value.source, sha: "b".repeat(40) } },
      { ...value, source: { ...value.source, tree: "c".repeat(40) } },
      { ...value, source: { ...value.source, clean: false } },
      { ...value, gates: value.gates.map((gate) => gate.id === "E5" ? { ...gate, status: "BLOCKED" } : gate) },
      { ...value, gates: value.gates.map((gate) => gate.id === "E4" ? { ...gate, status: "GREEN" } : gate) },
      { ...value, authority: { ...value.authority, dedicated: false } },
      { ...value, provider: "tencent" },
      { ...value, skipped: 1 },
      { ...value, unexpected: true },
      { ...value, authority: { ...value.authority, token: "forbidden" } },
      { ...value, gates: value.gates.map((gate) => gate.id === "E7" ? { ...gate, detail: "duplicate truth" } : gate) }
    ]) {
      assert.throws(() => validateJ0ReadyReceipt(invalid, sha, "d".repeat(40)), /j0_ready_receipt_invalid/);
    }
    await chmod(path, 0o644);
    await assert.rejects(() => loadJ0ReadyReceipt(path, sha, "d".repeat(40)), /j0_ready_receipt_invalid/);
    await chmod(path, 0o600);
    await symlink(path, link);
    await assert.rejects(() => loadJ0ReadyReceipt(link, sha, "d".repeat(40)), /j0_ready_receipt_invalid/);
    await symlink(new URL("../..", import.meta.url).pathname, parentLink);
    await assert.rejects(() => loadJ0ReadyReceipt(join(parentLink, "package.json"), sha, "d".repeat(40)), /j0_ready_receipt_invalid/);
    await assert.rejects(() => loadJ0ReadyReceipt(path, sha, "d".repeat(40), {
      currentUid: typeof process.getuid === "function" ? process.getuid() + 1 : 1
    }), /j0_ready_receipt_invalid/);
    await assert.rejects(() => loadJ0ReadyReceipt(new URL("../../package.json", import.meta.url).pathname, sha, "d".repeat(40)), /j0_ready_receipt_invalid/);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("top-level live entry rejects J0 source and gate drift before starting the J1 stack or HTTP", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-j0-top-level-reject-"));
  const path = join(root, "j0-ready.json");
  const outputPath = join(root, "j1.json");
  const baseOptions = {
    sourceSha: sha,
    cloudImage: `ghcr.io/example/cloud@${cloudDigest}`,
    workspaceImage: workspaceReference,
    receiptPath: outputPath,
    buildSourceImages: false,
    authorityMode: "live",
    productMatrixReceipt: "",
    j0ReadyReceipt: path,
    sub2apiSecretFile: ""
  };
  let stackStarts = 0;
  const dependencies = {
    loadLiveAuthority: async () => ({ baseURL: "https://sandbox.example.test", adminEmail: "admin@example.test", adminPassword: "password", authorityClass: "sandbox", qualificationUserEmail: "user@example.test", qualificationUserPassword: "password" }),
    readSourceIdentity: async () => ({ sha, tree: "d".repeat(40), clean: true }),
    runLiveJ1: async () => { stackStarts += 1; throw new Error("J1 stack must not start"); }
  };
  try {
    for (const invalid of [
      j0ReadyReceipt("b".repeat(40)),
      j0ReadyReceipt(sha, "c".repeat(40)),
      { ...j0ReadyReceipt(), gates: j0ReadyReceipt().gates.slice(0, -1) },
      { ...j0ReadyReceipt(), gates: j0ReadyReceipt().gates.map((gate) => gate.id === "E6" ? { ...gate, skipped: 1 } : gate) }
    ]) {
      await writeFile(path, JSON.stringify(invalid), { mode: 0o600 });
      await assert.rejects(() => runLocalWorkspaceQualification(baseOptions, dependencies), /j0_ready_receipt_invalid/);
    }
    assert.equal(stackStarts, 0);
    const failureReceipt = JSON.parse(await readFile(outputPath, "utf8"));
    assert.equal(failureReceipt.stage, "j0_ready_preflight");
    assert.equal(failureReceipt.errorCode, "j0_ready_receipt_invalid");
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

const sub2apiSecretValues = Object.freeze({
  OPL_SUB2API_BASE_URL: "https://sandbox.example.test",
  OPL_SUB2API_ADMIN_EMAIL: "admin@example.test",
  OPL_SUB2API_ADMIN_PASSWORD: "admin-password-not-real",
  OPL_QUALIFICATION_AUTHORITY_CLASS: "sandbox",
  OPL_QUALIFICATION_USER_EMAIL: "user@example.test",
  OPL_QUALIFICATION_USER_PASSWORD: "user-password-not-real"
});

function sub2apiSecretYAML(values = sub2apiSecretValues) {
  return Object.entries(values).map(([key, value]) => `${key}: ${JSON.stringify(value)}`).join("\n") + "\n";
}

test("live Sub2API secret file admission is exact and never creates a second env SSOT", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-sub2api-secret-test-"));
  const path = join(root, "sub2api.yaml");
  const link = join(root, "sub2api-link.yaml");
  try {
    await writeFile(path, sub2apiSecretYAML(), { mode: 0o600 });
    const admitted = await loadSub2APISecretFile(path, { environment: {} });
    assert.deepEqual(admitted, {
      baseURL: sub2apiSecretValues.OPL_SUB2API_BASE_URL,
      adminEmail: sub2apiSecretValues.OPL_SUB2API_ADMIN_EMAIL,
      adminPassword: sub2apiSecretValues.OPL_SUB2API_ADMIN_PASSWORD,
      authorityClass: sub2apiSecretValues.OPL_QUALIFICATION_AUTHORITY_CLASS,
      qualificationUserEmail: sub2apiSecretValues.OPL_QUALIFICATION_USER_EMAIL,
      qualificationUserPassword: sub2apiSecretValues.OPL_QUALIFICATION_USER_PASSWORD
    });
    assert.deepEqual(parseLocalQualificationArgs([
      "--source-sha", sha, "--cloud-image", `ghcr.io/example/cloud@${cloudDigest}`,
      "--workspace-image", workspaceReference, "--receipt", "/tmp/qualification.json",
      "--authority-mode", "live", "--j0-ready-receipt", "/tmp/j0-ready.json", "--sub2api-secret-file", path
    ]).sub2apiSecretFile, path);
    assert.throws(() => parseLocalQualificationArgs([
      "--source-sha", sha, "--cloud-image", `ghcr.io/example/cloud@${cloudDigest}`,
      "--workspace-image", workspaceReference, "--receipt", "/tmp/qualification.json",
      "--sub2api-secret-file", path
    ]), /live authority mode/);
    assert.throws(() => parseLocalQualificationArgs([
      "--source-sha", sha, "--cloud-image", `ghcr.io/example/cloud@${cloudDigest}`,
      "--workspace-image", workspaceReference, "--receipt", "/tmp/qualification.json",
      "--authority-mode", "live", "--j0-ready-receipt", "/tmp/j0-ready.json", "--sub2api-secret-file", "relative.yaml"
    ]), /absolute/);

    await assert.rejects(() => loadSub2APISecretFile(path, {
      environment: { OPL_SUB2API_BASE_URL: "https://outer.example.test" }
    }), /sub2api_secret_file_env_conflict/);
    await assert.rejects(() => loadSub2APISecretFile(path, {
      environment: {}, currentUid: typeof process.getuid === "function" ? process.getuid() + 1 : 1
    }), /sub2api_secret_file_invalid/);
    await assert.rejects(() => loadSub2APISecretFile(root, { environment: {} }), /sub2api_secret_file_invalid/);

    await symlink(path, link);
    await assert.rejects(() => loadSub2APISecretFile(link, { environment: {} }), /sub2api_secret_file_invalid/);
    await chmod(path, 0o640);
    await assert.rejects(() => loadSub2APISecretFile(path, { environment: {} }), /sub2api_secret_file_invalid/);
    await chmod(path, 0o600);

    for (const [name, values] of Object.entries({
      unknown: { ...sub2apiSecretValues, UNKNOWN_FIELD: "forbidden" },
      missing: Object.fromEntries(Object.entries(sub2apiSecretValues).slice(1)),
      wrongType: { ...sub2apiSecretValues, OPL_SUB2API_ADMIN_PASSWORD: 42 }
    })) {
      await writeFile(path, sub2apiSecretYAML(values), { mode: 0o600 });
      await assert.rejects(() => loadSub2APISecretFile(path, { environment: {} }), /sub2api_secret_file_schema_invalid/, name);
    }

    const envFileEntries = qualificationEnvFileEntries([
      ...Object.entries(sub2apiSecretValues), ["OPL_CLOUD_IMAGE", "cloud-image"]
    ], { authorityMode: "live", sub2apiSecretFile: path });
    assert.deepEqual(envFileEntries, [["OPL_CLOUD_IMAGE", "cloud-image"]]);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("Sub2API secret file failures redact the path and values before Docker", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-sub2api-secret-redaction-"));
  const path = join(root, "private-source.yaml");
  const receiptPath = join(root, "receipt.json");
  const previous = Object.fromEntries(Object.keys(sub2apiSecretValues).map((name) => [name, process.env[name]]));
  for (const name of Object.keys(sub2apiSecretValues)) delete process.env[name];
  try {
    await writeFile(path, `${sub2apiSecretYAML()}UNKNOWN_FIELD: ${JSON.stringify("do-not-leak-value")}\n`, { mode: 0o600 });
    await assert.rejects(() => runLocalWorkspaceQualification({
      sourceSha: sha,
      cloudImage: `ghcr.io/example/cloud@${cloudDigest}`,
      workspaceImage: workspaceReference,
      receiptPath,
      buildSourceImages: false,
      authorityMode: "live",
      productMatrixReceipt: "",
      sub2apiSecretFile: path
    }), /sub2api_secret_file_schema_invalid/);
    const receipt = JSON.parse(await readFile(receiptPath, "utf8"));
    assert.equal(receipt.stage, "authority_preflight");
    assert.equal(receipt.errorCode, "sub2api_secret_file_schema_invalid");
    assert.equal(receipt.error, "sub2api_secret_file_schema_invalid");
    assert.doesNotMatch(JSON.stringify(receipt), /private-source|do-not-leak-value|admin-password-not-real/);
  } finally {
    for (const [name, value] of Object.entries(previous)) {
      if (value === undefined) delete process.env[name];
      else process.env[name] = value;
    }
    await rm(root, { recursive: true, force: true });
  }
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

test("Local J1 accounting allows unrelated history and rejects duplicate operation evidence", () => {
  const operationId = "workspace-launch-current";
  const workspaceId = "ws-current";
  const receiptId = "receipt-current";
  const runtimeId = "runtime-current";
  const keyId = "700";
  const debitCode = "opl:current-debit";
  const amountUsdMicros = "52580000";
  const currentKey = { id: keyId, kind: "workspace", status: "active" };
  const historicalKey = { id: "699", kind: "workspace", status: "active" };
  const currentReceipt = {
    receiptId, operationId, workspaceId, type: "billing.workspace_purchased.v1", status: "completed",
    chargeReference: debitCode, totalUsdMicros: amountUsdMicros,
    fulfillment: { runtimeId, workspaceApiKeyId: keyId }
  };
  const historicalReceipt = {
    ...currentReceipt,
    receiptId: "receipt-historical",
    operationId: "workspace-launch-historical",
    workspaceId: "ws-historical",
    chargeReference: "opl:historical-debit",
    fulfillment: { runtimeId: "runtime-historical", workspaceApiKeyId: "699" }
  };
  const input = {
    operationId, workspaceId, receiptId, runtimeId, keyId, sub2apiUserId: "41", debitCode, amountUsdMicros,
    beforeMicros: "1000000000", afterMicros: "947420001",
    baselineKeys: [historicalKey], baselineReceipts: [historicalReceipt],
    keys: [historicalKey, currentKey], receipts: [historicalReceipt, currentReceipt],
    key: currentKey, keyUsage: { totalRequests: 0 }, usage: { totalRequests: 0 },
    history: { items: [{ valueUsdMicros: "-1000000", status: "used" }] },
    debit: { count: 1, code: debitCode, userId: "41", amountUsdMicros },
    evidence: {
      launch: { operationId, workspaceId, receiptId },
      runtime: { runtimeId },
      receipt: currentReceipt
    }
  };

  assert.deepEqual(validateLocalJ1AccountingReadback(input), { walletExactDeltaObserved: false });
  assert.throws(() => validateLocalJ1AccountingReadback({ ...input, keys: [...input.keys, currentKey] }), /key cardinality/);
  assert.throws(() => validateLocalJ1AccountingReadback({ ...input, receipts: [...input.receipts, currentReceipt] }), /receipt cardinality/);
  assert.throws(() => validateLocalJ1AccountingReadback({ ...input, debit: { ...input.debit, count: 2 } }), /debit cardinality/);
  assert.throws(() => validateLocalJ1AccountingReadback({ ...input, baselineKeys: [...input.baselineKeys, currentKey] }), /predates/);
  assert.throws(() => validateLocalJ1AccountingReadback({ ...input, baselineReceipts: [...input.baselineReceipts, currentReceipt] }), /predates/);
});

test("READY receipt binds the exact durable and accounting evidence", () => {
  const fixtureReceipt = {
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
      lanes: completeMatrixLanes().map((lane) => ({
        order: lane.order, cwd: lane.cwd, command: lane.command, args: lane.args,
        packages: lane.packages, failed: lane.failed, skipped: lane.skipped
      })),
      verticalTests: [...productMatrixVerticalTests],
      zeroSkip: true,
      casWinnerCount: 1,
      unknownAuthorityWriteDeltas: { controlPlane: 0, sub2api: 0, fabric: 0, ledger: 0 }
    },
    qualification: { authorityMode: "fixture", p0Ready: false },
    deferred: ["tencent-tke", "production-sub2api", "production-secrets"]
  };
  assert.equal(validateLocalQualificationReceipt(fixtureReceipt), fixtureReceipt);

  assert.throws(() => validateLocalQualificationReceipt({ ...fixtureReceipt, status: "NOT_READY" }), /READY receipt/);
  assert.throws(() => validateLocalQualificationReceipt({ ...fixtureReceipt, debit: { ...fixtureReceipt.debit, count: 2 } }), /debit/);
  assert.throws(() => validateLocalQualificationReceipt({ ...fixtureReceipt, restart: { ...fixtureReceipt.restart, runtimeStable: false } }), /restart/);
  assert.throws(() => validateLocalQualificationReceipt({ ...fixtureReceipt, residuals: { ...fixtureReceipt.residuals, volumes: 1 } }), /residual/);
  assert.throws(() => validateLocalQualificationReceipt({ ...fixtureReceipt, mutationCounts: { ...fixtureReceipt.mutationCounts, refundPosts: 1 } }), /mutation counts/);
  assert.throws(() => validateLocalQualificationReceipt({
    ...fixtureReceipt, refundReceipt: { ...fixtureReceipt.refundReceipt, type: "gateway.wallet_adjustment.v1" }
  }), /refund receipt/);
  assert.throws(() => validateLocalQualificationReceipt({
    ...fixtureReceipt, deletion: { ...fixtureReceipt.deletion, keyId: "72" }
  }), /owner deletion/);
  assert.throws(() => validateLocalQualificationReceipt({ ...fixtureReceipt, qualification: { authorityMode: "fixture", p0Ready: true } }), /authority classification/);
  assert.throws(() => validateLocalQualificationReceipt({ ...fixtureReceipt, wallet: { ...fixtureReceipt.wallet, afterUsdMicros: "47420001" } }), /fixture wallet/);
  assert.throws(() => validateLocalQualificationReceipt({ ...fixtureReceipt, authorityWriteCounts: { ...fixtureReceipt.authorityWriteCounts, debits: 2 } }), /authority write counts/);
});

test("local build proxy rejects credentials or URL parameters and errors redact the entire URL authority and query", () => {
  const previous = process.env.OPL_LOCAL_BUILD_PROXY;
  try {
    process.env.OPL_LOCAL_BUILD_PROXY = "socks5://host.docker.internal:10808";
    assert.deepEqual(localBuildProxyArgs(), ["--build-arg", "HTTPS_PROXY=socks5://host.docker.internal:10808"]);
    process.env.OPL_LOCAL_BUILD_PROXY = "socks5h://host.docker.internal:10808";
    assert.throws(() => localBuildProxyArgs(), /does not support socks5h:\/\/; use socks5:\/\//);
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
    tests: productMatrixRequiredTests.map((entry) => ({ ...entry, passed: true, skipped: 0 })),
    lanes: completeMatrixLanes(),
    verticalTests: productMatrixVerticalTests.map((name) => ({ name, passed: true, skipped: 0 }))
  };
  assert.equal(validateProductMatrixReceipt(matrix, sha, "d".repeat(40)), matrix);
  assert.throws(() => validateProductMatrixReceipt({ ...matrix, lanes: matrix.lanes.slice(1) }, sha, "d".repeat(40)), /lane|vertical/i);
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
	assert.match(compose, /OPL_FABRIC_LOCAL_DOCKER_GATEWAY_CONTAINER: \$\{OPL_FABRIC_LOCAL_DOCKER_GATEWAY_CONTAINER:\?Set the task-owned Control Plane gateway container\}/);
	assert.match(compose, /opl\.fabric\.local-docker\.gateway: control-plane/);
  assert.match(compose, /OPL_TENCENT_ZONE: local/);
  const envExample = await readFile(new URL("../../deploy/portable/opl-cloud.env.example", import.meta.url), "utf8");
  assert.match(envExample, /^OPL_FABRIC_LOCAL_DOCKER_SECRET_ROOT=\/absolute\/path\/to\/opl-fabric-secrets$/m);
  const runner = await readFile(new URL("../../tools/local-workspace-qualification.ts", import.meta.url), "utf8");
  assert.match(runner, /\["OPL_FABRIC_LOCAL_DOCKER_SECRET_ROOT", fabricSecretRoot\]/);
	assert.match(runner, /\["OPL_FABRIC_LOCAL_DOCKER_GATEWAY_CONTAINER", `\$\{project\}-control-plane-1`\]/);
  assert.match(runner, /env: composeEnvironment/);
  assert.match(runner, /exactRepoDigestFromInspection\(cloudRepository, await dockerImageInspection\(cloudTag\)\)/);
  assert.match(runner, /exactRepoDigestFromInspection\(workspaceRepository, await dockerImageInspection\(workspaceTag\)\)/);
  assert.match(runner, /await imageInspection\(cloudImage\)/);
  assert.match(runner, /await imageInspection\(workspaceImage\)/);
  assert.doesNotMatch(runner, /imageInspection\((?:cloud|workspace)Tag\)/);
  assert.match(runner, /if \(options\.authorityMode === "live"\)[\s\S]*runLocalWorkspaceJ1HTTPQualification[\s\S]*\} else \{[\s\S]*stage = "console_and_login"/);
  assert.doesNotMatch(runner, /runLocalWorkspaceJ1HTTPQualification\([\s\S]{0,5000}?validateQualificationSourceIdentity\([^)]*\);\s*return;/);
  assert.match(runner, /liveAdmissionOverride[\s\S]*OPL_CONTROLLED_BASIC_PILOT_ACCOUNT_IDS: \$\{OPL_CONTROLLED_BASIC_PILOT_ACCOUNT_IDS:/);
  assert.match(runner, /cleanupLocalQualificationResources\(scope\.accountId, scope\.workspaceId, async \(\) => \{[\s\S]*compose\(\["down"/);
});

test("sourceData and createHTTP/login preserve account mapping scope without Workspace mutation", async () => {
  assert.equal(typeof sourceData, "function");
  const requests = [];
  let mappingPosts = 0;
  let workspacePosts = 0;
  const server = createServer(async (request, response) => {
    const url = new URL(request.url || "/", "http://qualification.test");
    requests.push({ method: request.method, path: url.pathname, cookie: request.headers.cookie || "", csrf: request.headers["x-opl-csrf"] || "" });
    response.setHeader("content-type", "application/json");
    if (request.method === "POST" && url.pathname === "/api/auth/login") {
      response.statusCode = 200;
      response.setHeader("set-cookie", ["opl_session=admin-session; Path=/; HttpOnly"]);
      response.setHeader("x-opl-csrf-token", "csrf-admin");
      response.end(JSON.stringify({ user: { accountId: "acct-admin", role: "admin" } }));
      return;
    }
    if (request.method === "POST" && url.pathname === "/api/operator/accounts") {
      assert.equal(request.headers.cookie, "opl_session=admin-session");
      assert.equal(request.headers["x-opl-csrf"], "csrf-admin");
      mappingPosts += 1;
      response.statusCode = 201;
      response.end(JSON.stringify({ status: "succeeded", accountId: "acct-mapped", operationId: "account-provision-mapped" }));
      return;
    }
    if (request.method === "GET" && url.pathname === "/api/operator/accounts") {
      assert.equal(request.headers.cookie, "opl_session=admin-session");
      assert.equal(request.headers["x-opl-csrf"], "csrf-admin");
      response.statusCode = 200;
      response.end(JSON.stringify({ source: "control-plane+sub2api", available: true, status: "available", data: {
        items: [{ accountId: "acct-mapped", email: "customer@example.test", status: "active" }], total: 1, page: 1, pageSize: 50
      } }));
      return;
    }
    if (request.method === "POST" && url.pathname === "/api/workspace-launches") workspacePosts += 1;
    response.statusCode = 404;
    response.end(JSON.stringify({ error: "not_found" }));
  });
  await new Promise((resolvePromise, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolvePromise);
  });
  const address = server.address();
  assert.ok(address && typeof address === "object");
  const http = createHTTP(`http://127.0.0.1:${address.port}`);
  try {
    const admin = await login(http, "admin@example.test", "admin-password");
    assert.deepEqual(admin, { cookie: "opl_session=admin-session", csrf: "csrf-admin" });
    const mapping = await http.json("/api/operator/accounts", {
      method: "POST", headers: { "idempotency-key": "account-provision-mapped" },
      body: { email: "customer@example.test", password: "customer-password" }
    }, admin, [201]);
    assert.equal(mapping.payload.accountId, "acct-mapped");
    const readback = await http.json("/api/operator/accounts?page=1&pageSize=50", {}, admin);
    const accountPage = sourceData(readback.payload, "control-plane+sub2api");
    assert.equal(accountPage.items[0].accountId, "acct-mapped");
    assert.throws(() => sourceData({ source: "control-plane", available: true, status: "available", data: accountPage }, "control-plane+sub2api"), /unavailable/);
    assert.throws(() => sourceData({ source: "control-plane+sub2api", available: false, status: "unavailable", data: accountPage }, "control-plane+sub2api"), /unavailable/);
    const qualification = await login(http, "customer@example.test", "customer-password");
    assert.deepEqual(qualification, { cookie: "opl_session=admin-session", csrf: "csrf-admin" });
    assert.equal(mappingPosts, 1);
    assert.equal(workspacePosts, 0);
    assert.deepEqual(requests.map(({ method, path }) => `${method}:${path}`), [
      "POST:/api/auth/login", "POST:/api/operator/accounts", "GET:/api/operator/accounts", "POST:/api/auth/login"
    ]);
  } finally {
    await new Promise((resolvePromise) => server.close(() => resolvePromise()));
  }
});

test("sourceData export has no undefined import or default export dependency", async () => {
  const source = await readFile(new URL("../../tools/local-workspace-qualification.ts", import.meta.url), "utf8");
  assert.match(source, /export function sourceData\(/);
  assert.doesNotMatch(source, /import\s+sourceData\s+from/);
});

test("canonical J1 HTTP preview covers every live stage and validates exact local cleanup", async () => {
  const accountEmail = "customer@example.test";
  const accountPassword = "customer-password";
  const accountProvisionKey = "account-provision-j1";
  const accountId = `acct-${stableID("account", accountEmail).slice(0, 18)}`;
  const accountOperationId = `account-provision-${stableID(accountProvisionKey, accountEmail).slice(0, 18)}`;
  const launchKey = "local-qualification:j1";
  const operationId = `workspace-launch-${stableID(accountId, launchKey).slice(0, 18)}`;
  const workspaceId = `ws-${stableID("workspace-launch-v2", accountId, operationId).slice(0, 18)}`;
  const receiptId = "receipt-j1";
  const runtimeId = "runtime-j1";
  const keyId = "700";
  const debitCode = "opl:j1-debit";
  const amountUsdMicros = "52580000";
  const workspaceURL = `http://workspace.test/w/${workspaceId}/`;
  const historicalKey = { id: "699", kind: "workspace", status: "active" };
  const historicalReceipt = {
    receiptId: "receipt-historical", operationId: "workspace-launch-historical", workspaceId: "ws-historical",
    type: "billing.workspace_purchased.v1", status: "completed", chargeReference: "opl:historical-debit",
    totalUsdMicros: "52580000", fulfillment: { runtimeId: "runtime-historical", workspaceApiKeyId: "699" }
  };
  const requests = [];
  const counts = { mappingPosts: 0, workspacePosts: 0, keyCreates: 0, debits: 0, refunds: 0, deletes: 0, restarts: 0 };
  let launchReads = 0;
  let server;
  const envelope = (source, data, status = "available") => ({ source, available: true, status, data });
  const send = (response, status, payload, headers = {}) => {
    response.writeHead(status, { "content-type": "application/json", ...headers });
    response.end(JSON.stringify(payload));
  };
  const launch = (status = "succeeded") => ({
    operationId, workspaceId, status, phase: status, receiptId, url: workspaceURL,
    workspaceApiKeyId: keyId, computeAllocationId: "compute-j1", storageId: "storage-j1", attachmentId: "attachment-j1"
  });
  server = createServer(async (request, response) => {
    const url = new URL(request.url || "/", "http://qualification.test");
    const method = request.method || "GET";
    requests.push({ method, path: url.pathname });
    if (method === "GET" && url.pathname === "/api/healthz") return send(response, 200, { status: "ok" });
    if (method === "GET" && url.pathname === "/") { response.writeHead(200, { "content-type": "text/html" }); response.end("<div id=\"root\"><script src=\"/assets/app.js\"></script></div>"); return; }
    if (method === "GET" && url.pathname === "/assets/app.js") { response.writeHead(200, { "content-type": "application/javascript" }); response.end("console.log('stub')"); return; }
    if (method === "POST" && url.pathname === "/api/auth/login") {
      const body = await new Promise((resolvePromise) => { let text = ""; request.on("data", (chunk) => { text += chunk; }); request.on("end", () => resolvePromise(JSON.parse(text))); });
      const admin = body.email === "admin@example.test";
      const customer = body.email === accountEmail;
      if (!admin && !customer) return send(response, 401, { error: "invalid_credentials" });
      return send(response, 200, { user: { accountId: admin ? "acct-admin" : accountId, role: admin ? "admin" : "owner" } }, { "set-cookie": `${admin ? "admin" : "customer"}=session; Path=/`, "x-opl-csrf-token": admin ? "csrf-admin" : "csrf-customer" });
    }
    if (method === "POST" && url.pathname === "/api/operator/accounts") {
      counts.mappingPosts += 1;
      return send(response, 201, { status: "succeeded", accountId, operationId: accountOperationId });
    }
    if (method === "GET" && url.pathname === "/api/operator/accounts") {
      return send(response, 200, envelope("control-plane+sub2api", { items: [{ accountId, email: accountEmail, status: "active", sub2apiUserId: "41" }], total: 1, page: 1, pageSize: 50 }));
    }
    if (method === "GET" && url.pathname === "/api/auth/me") return send(response, 200, envelope("sub2api", { accountId, email: accountEmail, role: "owner", status: "active", sub2apiUserId: "41" }));
    if (method === "GET" && url.pathname === "/api/gateway/wallet") return send(response, 200, envelope("sub2api", { userId: "41", currency: "USD", usdMicros: counts.debits ? "947420000" : "1000000000", status: "active" }));
    if (method === "GET" && url.pathname === "/api/gateway/usage-summary") return send(response, 200, envelope("sub2api", { totalRequests: 0 }));
    if (method === "GET" && url.pathname === "/api/gateway/keys") {
      const items = counts.keyCreates ? [historicalKey, { id: keyId, kind: "workspace", status: "active" }] : [historicalKey];
      return send(response, 200, envelope("sub2api", { items, total: items.length, page: 1, pageSize: 100, pages: 1 }));
    }
    if (method === "POST" && url.pathname === "/api/pricing/preview") return send(response, 200, { resourceType: "workspace", packageId: "basic", currency: "USD", totalChargeUsdMicros: Number(amountUsdMicros) });
    if (method === "POST" && url.pathname === "/api/workspace-launches") {
      counts.workspacePosts += 1; counts.keyCreates += 1; counts.debits += 1;
      return send(response, 202, launch("pending"));
    }
    if (method === "GET" && url.pathname === `/api/workspace-launches/${operationId}`) {
      launchReads += 1;
      return send(response, 200, launch(launchReads === 1 ? "pending" : "succeeded"));
    }
    if (method === "GET" && url.pathname === "/api/workspaces") return send(response, 200, envelope("control-plane", { items: [{ id: workspaceId, url: workspaceURL }], total: 1, page: 1, pageSize: 20 }));
    if (method === "GET" && url.pathname === `/api/workspaces/${workspaceId}/runtime-status`) return send(response, 200, envelope("fabric", { workspaceId, runtimeId, ready: true, status: "running", url: workspaceURL }));
    if (method === "GET" && url.pathname === `/api/billing/receipts/${receiptId}`) return send(response, 200, envelope("ledger", { receiptId, accountId, operationId, workspaceId, type: "billing.workspace_purchased.v1", status: "completed", chargeReference: debitCode, totalUsdMicros: amountUsdMicros, fulfillment: { runtimeId, workspaceApiKeyId: keyId } }));
    if (method === "GET" && url.pathname === "/api/billing/receipts") {
      const currentReceipt = { receiptId, accountId, operationId, workspaceId, type: "billing.workspace_purchased.v1", status: "completed", chargeReference: debitCode, totalUsdMicros: amountUsdMicros, fulfillment: { runtimeId, workspaceApiKeyId: keyId } };
      return send(response, 200, envelope("ledger", { receipts: counts.debits ? [historicalReceipt, currentReceipt] : [historicalReceipt], hasMore: false, nextCursor: "" }));
    }
    if (method === "GET" && url.pathname === `/api/gateway/keys/${keyId}`) return send(response, 200, envelope("sub2api", { id: keyId, kind: "workspace", status: "active" }));
    if (method === "GET" && url.pathname === `/api/gateway/keys/${keyId}/usage-summary`) return send(response, 200, envelope("sub2api", { totalRequests: 0 }));
    if (method === "GET" && url.pathname === "/api/gateway/balance-history") return send(response, 200, envelope("sub2api", { items: [{ valueUsdMicros: `-${amountUsdMicros}`, status: "used" }], total: 1, page: 1, pageSize: 20, pages: 1 }));
    if (method === "GET" && url.pathname === `/w/${workspaceId}/`) { response.writeHead(200, { "content-type": "text/html" }); response.end("<html>OPL Workspace READY</html>"); return; }
    return send(response, 404, { error: "not_found" });
  });
  await new Promise((resolvePromise, reject) => { server.once("error", reject); server.listen(0, "127.0.0.1", resolvePromise); });
  const address = server.address();
  const http = createHTTP(`http://127.0.0.1:${address.port}`);
  const stages = [];
  const cleanupCalls = [];
  const j0Root = await mkdtemp(join(tmpdir(), "opl-j0-top-level-"));
  const j0Path = join(j0Root, "ready.json");
  const outputPath = join(j0Root, "j1.json");
  await writeFile(j0Path, JSON.stringify(j0ReadyReceipt(sha, "d".repeat(40))), { mode: 0o600 });
  try {
    const options = parseLocalQualificationArgs([
      "--source-sha", sha, "--cloud-image", `ghcr.io/example/cloud@${cloudDigest}`,
      "--workspace-image", workspaceReference, "--receipt", outputPath,
      "--authority-mode", "live", "--j0-ready-receipt", j0Path
    ]);
    const result = await runLocalWorkspaceQualification(options, {
      loadLiveAuthority: async () => ({ baseURL: "https://sandbox.example.test", adminEmail: "admin@example.test", adminPassword: "admin-password", authorityClass: "sandbox", qualificationUserEmail: "admin@example.test", qualificationUserPassword: "admin-password" }),
      readSourceIdentity: async () => ({ sha, tree: "d".repeat(40), clean: true }),
      runLiveJ1: async ({ j0Ready }) => runLocalWorkspaceJ1HTTPQualification({
        http, adminEmail: "admin@example.test", adminPassword: "admin-password", qualificationEmail: accountEmail, qualificationPassword: accountPassword,
        accountProvisionKey, launchKey, operationId, workspaceId, workspaceName: "J1", wait: async () => {},
        onStage: (stage) => stages.push(stage), readRuntime: async () => ({ runningDigest: workspaceDigest }),
        readDebit: async ({ code, sub2apiUserId, amountUsdMicros: value }) => ({ code, userId: sub2apiUserId, amountUsdMicros: value, count: 1 }),
        browserQualification: async ({ onLaunchIdentity }) => {
          const launch = { operationId, workspaceId, receiptId, workspaceApiKeyId: keyId, autoRenew: false, status: "succeeded" };
          onLaunchIdentity(launch);
          counts.workspacePosts += 1;
          counts.keyCreates += 1;
          counts.debits += 1;
          launchReads = 1;
          return {
            launch,
            receiptId,
            evidence: {
              status: "READY",
              screenshots: ["01-login.png", "02-balance-usage.png", "03-create-confirmation.png", "04-ready.png", "05-detail.png", "06-workspace-ready.png"]
                .map((name) => ({ name, digest: `sha256:${"e".repeat(64)}`, sizeBytes: 1 })),
              requests: [], responses: [], pageErrors: [], consoleErrors: [],
              launchPosts: 1, ownerDeleteRequests: 0, refundPosts: 0,
              readback: { autoRenew: false, workspaceState: "running", runtimeStatus: "running", runtimeReady: true }
            }
          };
        },
        cleanup: async (scope) => { cleanupCalls.push(scope); return { containers: 0, volumes: 0, networks: 0 }; },
        receiptBase: {
          schemaVersion: 1, status: "READY", source: { sha, tree: "d".repeat(40) },
          images: { cloud: { input: `ghcr.io/example/cloud@${cloudDigest}`, repoDigest: `ghcr.io/example/cloud@${cloudDigest}`, digest: cloudDigest, runningDigest: cloudDigest }, workspace: { input: workspaceReference, repoDigest: workspaceReference, digest: workspaceDigest, runningDigest: workspaceDigest } },
          command: "npm run qualify:local:workspace", processes: { console: "ready", controlPlane: "ready", fabric: "ready", ledger: "ready" }, stores: { controlPlane: "durable", fabric: "durable", ledger: "durable", ownerSeparated: true },
          j0Ready: { ...j0Ready, digest: j0Ready.digest },
          qualification: { authorityMode: "live", p0Ready: true }, deferred: []
        }
      })
    });
    assert.equal(result.status, "READY");
    const writtenReceipt = JSON.parse(await readFile(outputPath, "utf8"));
    assert.equal(writtenReceipt.j0Ready.digest, result.j0Ready.digest);
    assert.deepEqual(writtenReceipt.j0Ready.source, { sha, tree: "d".repeat(40), clean: true });
    assert.deepEqual(writtenReceipt.j0Ready.gates.map(({ id, status }) => ({ id, status })), j0ReadyReceipt().gates.map(({ id, status }) => ({ id, status })));
    assert.equal(Object.prototype.hasOwnProperty.call(writtenReceipt, "productMatrix"), false);
    assert.deepEqual(stages, ["bootstrap_ready", "admin_login", "account_provision", "qualification_login", "wallet_usage_baseline", "pricing_preview", "workspace_launch", "terminal_readback", "workspace_open", "accounting_readback", "receipt_validation", "qualification_cleanup"]);
    assert.deepEqual(cleanupCalls, [{ accountId, workspaceId }]);
    assert.deepEqual(counts, { mappingPosts: 1, workspacePosts: 1, keyCreates: 1, debits: 1, refunds: 0, deletes: 0, restarts: 0 });
    assert.equal(requests.filter((request) => request.method === "POST" && request.path === "/api/workspace-launches").length, 0);
    assert.equal(requests.some((request) => request.method === "DELETE"), false);
    assert.equal(requests.some((request) => request.path.includes("refund")), false);
  } finally {
    await new Promise((resolvePromise) => server.close(() => resolvePromise()));
    await rm(j0Root, { recursive: true, force: true });
  }
});

test("canonical J1 HTTP preview cleans a failure before Workspace Create", async () => {
  const cleanupCalls = [];
  const http = {
    json: async (path) => {
      if (path === "/api/healthz") return { payload: { status: "ok" } };
      throw new Error("unexpected request after conflict");
    },
    request: async (path) => path === "/" ? { response: { status: 200 }, text: "<div id=\"root\"><script src=\"/app.js\"></script></div>" } :
      { response: { status: 200 }, text: "asset" }
  };
  http.json = async (path) => {
    if (path === "/api/healthz") return { payload: { status: "ok" } };
    if (path === "/api/auth/login") return { response: { headers: new Headers({ "set-cookie": "admin=session", "x-opl-csrf-token": "csrf" }) }, payload: { user: { accountId: "acct-admin" } } };
    if (path === "/api/operator/accounts") return { response: { status: 201 }, payload: { status: "succeeded", accountId: "acct-wrong", operationId: "account-provision-wrong" } };
    throw new Error("unexpected request after conflict");
  };
  await assert.rejects(() => runLocalWorkspaceJ1HTTPQualification({
    http, adminEmail: "admin@example.test", adminPassword: "password", qualificationEmail: "customer@example.test", qualificationPassword: "password",
    accountProvisionKey: "mapping", launchKey: "launch", operationId: "operation", workspaceId: "workspace", workspaceName: "J1",
    readRuntime: async () => ({}), readDebit: async () => ({}), cleanup: async (scope) => { cleanupCalls.push(scope); return { containers: 0, volumes: 0, networks: 0 }; }, receiptBase: {}
  }), /provision response/);
  assert.deepEqual(cleanupCalls, [{ failed: true, launchSubmitted: false, recoveryAuthority: null }]);
  assert.throws(() => sourceData({ source: "control-plane+sub2api", available: true, status: "conflict", data: {} }, "control-plane+sub2api"), /unavailable/);
});

test("canonical J1 HTTP preview preserves exact recovery authority after Workspace Create submission", async () => {
  const accountId = `acct-${stableID("account", "customer@example.test").slice(0, 18)}`;
  const provisionOperationId = `account-provision-${stableID("mapping", "customer@example.test").slice(0, 18)}`;
  const cleanupCalls = [];
  const launch = {
    operationId: "operation", workspaceId: "workspace", schemaVersion: 3, version: 7,
    status: "manual_review", stage: "debit", phase: "debit",
    continuationAttemptBudgets: Object.fromEntries([
      "key", "debit", "ensure_compute_allocation", "storage", "attachment", "secret", "runtime", "activation", "receipt"
    ].map((stage) => [stage, {
      attempted: stage === "key" || stage === "debit" ? 1 : 0,
      confirmed: stage === "key" ? 1 : 0,
      unknown: stage === "debit" ? 1 : 0,
      max: 1,
      status: stage === "key" ? "confirmed" : stage === "debit" ? "unknown" : "",
      idempotencyKey: stage === "key" ? "key-write" : stage === "debit" ? "debit-write" : "",
      pendingReadbacks: 0,
      maxPendingReadbacks: 3
    }]))
  };
  const http = {
    request: async (path) => path === "/"
      ? { response: { status: 200 }, text: '<div id="root"><script src="/app.js"></script></div>' }
      : { response: { status: 200 }, text: "asset" },
    json: async (path, init = {}) => {
      if (path === "/api/healthz") return { payload: { status: "ok" } };
      if (path === "/api/auth/login") return { response: { headers: new Headers({ "set-cookie": "session=test", "x-opl-csrf-token": "csrf" }) }, payload: { user: { accountId: init.body.email === "customer@example.test" ? accountId : "acct-admin" } } };
      if (path === "/api/operator/accounts" && init.method === "POST") return { payload: { status: "succeeded", accountId, operationId: provisionOperationId } };
      if (path.startsWith("/api/operator/accounts?")) return { payload: { source: "control-plane+sub2api", available: true, status: "available", data: { items: [{ accountId, email: "customer@example.test", status: "active", sub2apiUserId: "41" }], total: 1, page: 1, pageSize: 50 } } };
      if (path === "/api/auth/me") return { payload: { source: "sub2api", available: true, status: "available", data: { accountId, email: "customer@example.test", role: "owner", status: "active", sub2apiUserId: "41" } } };
      if (path === "/api/gateway/wallet") return { payload: { source: "sub2api", available: true, status: "available", data: { usdMicros: "100000000" } } };
      if (path === "/api/gateway/usage-summary?period=month") return { payload: { source: "sub2api", available: true, status: "available", data: { totalRequests: 0 } } };
      if (path === "/api/gateway/keys?page=1&pageSize=100") return { payload: { source: "sub2api", available: true, status: "available", data: { items: [], total: 0, page: 1, pageSize: 100, pages: 1 } } };
      if (path === "/api/billing/receipts?limit=100") return { payload: { source: "ledger", available: true, status: "available", data: { receipts: [], hasMore: false } } };
      if (path === "/api/pricing/preview") return { payload: { resourceType: "workspace", packageId: "basic", currency: "USD", totalChargeUsdMicros: "52580000" } };
      if (path === "/api/workspace-launches" && init.method === "POST") return { payload: { operationId: "operation", workspaceId: "workspace" } };
      if (path === "/api/workspace-launches/operation") return { payload: launch };
      throw new Error(`unexpected request ${path}`);
    }
  };
  await assert.rejects(() => runLocalWorkspaceJ1HTTPQualification({
    http, adminEmail: "admin@example.test", adminPassword: "password", qualificationEmail: "customer@example.test", qualificationPassword: "password",
    accountProvisionKey: "mapping", launchKey: "launch", operationId: "operation", workspaceId: "workspace", workspaceName: "J1",
    wait: async () => {}, readRuntime: async () => ({}), readDebit: async () => ({}),
    cleanup: async (scope) => { cleanupCalls.push(scope); return null; }, receiptBase: {}
  }), /manual_review\/debit\/none/);
  assert.equal(cleanupCalls.length, 1);
  assert.equal(cleanupCalls[0].failed, true);
  assert.equal(cleanupCalls[0].launchSubmitted, true);
  assert.equal(cleanupCalls[0].recoveryAuthority.status, "manual_review");
  assert.deepEqual(cleanupCalls[0].recoveryAuthority.externalWrites.debits, { attempted: 1, confirmed: 0, unknown: 1 });
  const serializedAuthority = JSON.stringify(cleanupCalls[0].recoveryAuthority);
  for (const forbidden of ['"operationId":"operation"', '"workspaceId":"workspace"', "key-write", "debit-write"]) {
    assert.equal(serializedAuthority.includes(forbidden), false, forbidden);
  }
});

test("canonical J1 HTTP preview still forbids cleanup when the post-Create owner readback is unavailable", async () => {
  let submitted = false;
  const cleanupCalls = [];
  const accountId = `acct-${stableID("account", "customer@example.test").slice(0, 18)}`;
  const http = {
    request: async (path) => path === "/"
      ? { response: { status: 200 }, text: '<div id="root"><script src="/app.js"></script></div>' }
      : { response: { status: 200 }, text: "asset" },
    json: async (path, init = {}) => {
      if (path === "/api/healthz") return { payload: { status: "ok" } };
      if (path === "/api/auth/login") return { response: { headers: new Headers({ "set-cookie": "session=test", "x-opl-csrf-token": "csrf" }) }, payload: { user: { accountId: init.body.email === "customer@example.test" ? accountId : "acct-admin" } } };
      if (path === "/api/operator/accounts" && init.method === "POST") return { payload: { status: "succeeded", accountId, operationId: `account-provision-${stableID("mapping", "customer@example.test").slice(0, 18)}` } };
      if (path.startsWith("/api/operator/accounts?")) return { payload: { source: "control-plane+sub2api", available: true, status: "available", data: { items: [{ accountId, email: "customer@example.test", status: "active", sub2apiUserId: "41" }], total: 1, page: 1, pageSize: 50 } } };
      if (path === "/api/auth/me") return { payload: { source: "sub2api", available: true, status: "available", data: { accountId, email: "customer@example.test", role: "owner", status: "active", sub2apiUserId: "41" } } };
      if (path === "/api/gateway/wallet") return { payload: { source: "sub2api", available: true, status: "available", data: { usdMicros: "100000000" } } };
      if (path === "/api/gateway/usage-summary?period=month") return { payload: { source: "sub2api", available: true, status: "available", data: { totalRequests: 0 } } };
      if (path === "/api/gateway/keys?page=1&pageSize=100") return { payload: { source: "sub2api", available: true, status: "available", data: { items: [], total: 0, page: 1, pageSize: 100, pages: 1 } } };
      if (path === "/api/billing/receipts?limit=100") return { payload: { source: "ledger", available: true, status: "available", data: { receipts: [], hasMore: false } } };
      if (path === "/api/pricing/preview") return { payload: { resourceType: "workspace", packageId: "basic", currency: "USD", totalChargeUsdMicros: "52580000" } };
      if (path === "/api/workspace-launches" && init.method === "POST") { submitted = true; throw new Error("response unavailable"); }
      if (path === "/api/workspace-launches/operation" && submitted) throw new Error("owner readback unavailable");
      throw new Error(`unexpected request ${path}`);
    }
  };
  await assert.rejects(() => runLocalWorkspaceJ1HTTPQualification({
    http, adminEmail: "admin@example.test", adminPassword: "password", qualificationEmail: "customer@example.test", qualificationPassword: "password",
    accountProvisionKey: "mapping", launchKey: "launch", operationId: "operation", workspaceId: "workspace", workspaceName: "J1",
    wait: async () => {}, readRuntime: async () => ({}), readDebit: async () => ({}),
    cleanup: async (scope) => { cleanupCalls.push(scope); return null; }, receiptBase: {}
  }), /response unavailable/);
  assert.deepEqual(cleanupCalls, [{ accountId, failed: true, launchSubmitted: true, recoveryAuthority: null }]);
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
