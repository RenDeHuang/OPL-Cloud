import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import { access, mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { createServer, request as httpRequest } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test, { after, before } from "node:test";

import {
  authorityState,
  buildSourceImages,
  createHTTP,
  login,
  qualificationComposeEnvironment,
  readWorkspaceEvidence,
  residualCounts,
  stableID,
  unusedPort,
  verifyStores,
  waitForCompose,
  waitForLaunch
} from "../../tools/local-workspace-qualification.ts";
import { productMatrixStages } from "../../tools/verify-local.ts";

const enabled = process.env.OPL_VERTICAL_INTEGRATION === "1";
const root = process.cwd();
const blockedVerticalLanes = Object.freeze({ E4: "BLOCKED_PRODUCT_DECISION" });
const selectedLanes = new Set(String(process.env.OPL_VERTICAL_LANES || "").split(",").map((value) => value.trim()).filter(Boolean));
const executableLanes = new Set(["E0", "E1", "E2", "E3", "E5", "E6", "E7"]);
if (selectedLanes.has("E4")) throw new Error(`E4 ${blockedVerticalLanes.E4}`);
for (const lane of selectedLanes) {
  if (!executableLanes.has(lane)) throw new Error(`unknown vertical lane ${lane}`);
}

function verticalTest(lane: string, name: string, body: () => Promise<void>, options: Record<string, unknown> = {}) {
  if (selectedLanes.size === 0 || selectedLanes.has(lane)) test(name, body, options);
}

function command(args: string[], env = process.env, check = true) {
  const result = spawnSync(args[0], args.slice(1), {
    cwd: root,
    env,
    encoding: "utf8",
    maxBuffer: 64 * 1024 * 1024
  });
  if (result.error) throw result.error;
  if (check && result.status !== 0) {
    throw new Error([result.stdout, result.stderr].filter(Boolean).join("\n").trim());
  }
  return result;
}

function immutableReference(value: string) {
  return /^[^\s@]+@sha256:[0-9a-f]{64}$/.test(value);
}

function sourceData(envelope: any, expectedSource: string) {
  assert.equal(envelope?.source, expectedSource);
  assert.equal(envelope?.available, true);
  assert.ok(["available", "empty"].includes(envelope?.status));
  return envelope.data;
}

async function postWithResponseLoss(origin: string, path: string, init: any, auth: any) {
  let resolveUpstream: (value: any) => void;
  let rejectUpstream: (error: Error) => void;
  const upstreamResult = new Promise((resolve, reject) => {
    resolveUpstream = resolve;
    rejectUpstream = reject;
  });
  const proxy = createServer((incoming, outgoing) => {
    const target = new URL(path, origin);
    const upstream = httpRequest(target, {
      method: incoming.method,
      headers: { ...incoming.headers, host: target.host }
    }, (response) => {
      const chunks: Buffer[] = [];
      response.on("data", (chunk) => chunks.push(Buffer.from(chunk)));
      response.on("end", () => {
        const text = Buffer.concat(chunks).toString("utf8");
        let payload = null;
        try { payload = JSON.parse(text); } catch { payload = null; }
        resolveUpstream({ status: response.statusCode || 0, payload, text });
        outgoing.socket?.destroy();
      });
    });
    upstream.on("error", (error) => {
      rejectUpstream(error);
      outgoing.socket?.destroy(error);
    });
    incoming.pipe(upstream);
  });
  await new Promise<void>((resolvePromise, reject) => {
    proxy.once("error", reject);
    proxy.listen(0, "127.0.0.1", resolvePromise);
  });
  const address = proxy.address();
  assert.ok(address && typeof address === "object");
  const headers = new Headers(init.headers || {});
  headers.set("cookie", auth.cookie);
  headers.set("x-opl-csrf", auth.csrf);
  headers.set("content-type", "application/json");
  let clientUnknown = false;
  try {
    await fetch(`http://127.0.0.1:${address.port}${path}`, {
      method: init.method,
      headers,
      body: JSON.stringify(init.body),
      signal: AbortSignal.timeout(30_000)
    });
  } catch {
    clientUnknown = true;
  }
  const upstream = await upstreamResult;
  await new Promise<void>((resolvePromise) => proxy.close(() => resolvePromise()));
  assert.equal(clientUnknown, true, "the client must not observe the accepted launch response");
  return { clientUnknown, upstream };
}

async function loginWhenReady(http: any, email: string, password: string) {
  let lastError: unknown;
  for (let attempt = 0; attempt < 60; attempt += 1) {
    try {
      return await login(http, email, password);
    } catch (error) {
      lastError = error;
      await new Promise((resolvePromise) => setTimeout(resolvePromise, 250));
    }
  }
  throw lastError;
}

if (enabled) {
  const state: Record<string, any> = {};
  const providerStages = ["ensure_compute_allocation", "storage", "attachment", "secret", "runtime"];

  const psqlRows = async (database: string, query: string) => (await state.compose([
    "exec", "-T", "postgres", "psql", "-U", "postgres", "-d", database, "-Atqc", query
  ])).stdout.trim().split(/\r?\n/).filter(Boolean);

  const fabricStageEvidence = async () => {
    const rows = await psqlRows("opl_fabric", [
      "select concat_ws('|', redacted_provider_payload::jsonb#>>'{launchStageBinding,binding,stage}', status, count(*))",
      "from fabric_operations",
      `where account_id='${state.accountId}' and workspace_id='${state.workspaceId}'`,
      "and redacted_provider_payload::jsonb ? 'launchStageBinding'",
      "group by redacted_provider_payload::jsonb#>>'{launchStageBinding,binding,stage}', status order by 1"
    ].join(" "));
    return rows.map((row: string) => {
      const [stage, status, count] = row.split("|");
      return { stage, status, count: Number(count) };
    });
  };

  const operationCount = async () => Number((await psqlRows("opl_control_plane", [
    "select count(*) from control_plane_runtime_operations",
    `where action='workspace.launch.v2' and operation_id='${state.operationId}'`,
    `and account_id='${state.accountId}' and workspace_id='${state.workspaceId}'`
  ].join(" ")))[0] || 0);

  const launchIdentities = (evidence: any) => ({
    operationId: evidence.launch.operationId,
    workspaceId: evidence.launch.workspaceId,
    keyId: String(evidence.launch.workspaceApiKeyId || ""),
    redeemCode: String(evidence.receipt.chargeReference || ""),
    computeAllocationId: String(evidence.launch.computeAllocationId || ""),
    storageId: String(evidence.launch.storageId || ""),
    attachmentId: String(evidence.launch.attachmentId || ""),
    runtimeId: String(evidence.runtime.runtimeId || "")
  });

  const dockerIDs = (args: string[]) => command(["docker", ...args], process.env, false).stdout.trim().split(/\r?\n/).filter(Boolean);
  const exactOwnedIDs = (kind: "containers" | "volumes" | "networks") => {
    const args = kind === "containers" ? ["ps", "-aq"] : kind === "volumes" ? ["volume", "ls", "-q"] : ["network", "ls", "-q"];
    for (const label of [
      "opl.fabric.provider=local-docker",
      `opl.account.id=${state.accountId}`,
      `opl.workspace.id=${state.workspaceId}`
    ]) args.push("--filter", `label=${label}`);
    return dockerIDs(args);
  };
  const projectIDs = (kind: "containers" | "volumes" | "networks") => {
    const args = kind === "containers" ? ["ps", "-aq"] : kind === "volumes" ? ["volume", "ls", "-q"] : ["network", "ls", "-q"];
    args.push("--filter", `label=com.docker.compose.project=${state.project}`);
    return dockerIDs(args);
  };

  const cleanupOwnedResources = async () => {
    if (state.cleanupEvidence) return state.cleanupEvidence;
    for (const id of exactOwnedIDs("containers")) {
      command(["docker", "stop", "--time", "10", id], process.env, false);
      command(["docker", "rm", id], process.env, false);
    }
    for (const id of exactOwnedIDs("volumes")) command(["docker", "volume", "rm", id], process.env, false);
    if (state.compose) await state.compose(["down", "--volumes", "--remove-orphans", "--timeout", "30"], { allowFailure: true });
    for (const id of exactOwnedIDs("networks")) command(["docker", "network", "rm", id], process.env, false);
    if (state.registryContainer) {
      command(["docker", "stop", "--time", "10", state.registryContainer], process.env, false);
      command(["docker", "rm", state.registryContainer], process.env, false);
    }
    for (const image of state.builtTags || []) command(["docker", "image", "rm", image], process.env, false);
    if (state.tempRoot) await rm(state.tempRoot, { recursive: true, force: true });
    let tempRootPresent = false;
    try { await access(state.tempRoot); tempRootPresent = true; } catch { tempRootPresent = false; }
    state.cleanupEvidence = {
      residuals: await residualCounts(state.accountId, state.workspaceId),
      compose: {
        containers: projectIDs("containers").length,
        volumes: projectIDs("volumes").length,
        networks: projectIDs("networks").length
      },
      registryContainers: dockerIDs(["ps", "-aq", "--filter", `name=^/${state.registryContainer}$`]).length,
      taskImages: (state.builtTags || []).filter((image: string) => command(["docker", "image", "inspect", image], process.env, false).status === 0).length,
      tempRootPresent
    };
    return state.cleanupEvidence;
  };

  const ensureLaunchContinuation = async () => {
    if (state.evidence) return;
    state.auth = await loginWhenReady(state.http, state.email, state.password);
    state.meBefore = sourceData((await state.http.json("/api/auth/me", {}, state.auth)).payload, "sub2api");
    state.walletBefore = sourceData((await state.http.json("/api/gateway/wallet", {}, state.auth)).payload, "sub2api");
    const pricing = (await state.http.json("/api/pricing/preview", {
      method: "POST", body: { resourceType: "workspace", packageId: "basic", sizeGb: 10 }
    }, state.auth)).payload;
    state.amountUsdMicros = String(pricing.totalChargeUsdMicros);
    const loss = await postWithResponseLoss(`http://127.0.0.1:${state.publicPort}`, "/api/workspace-launches", {
      method: "POST", headers: { "idempotency-key": state.launchKey },
      body: { name: "Vertical qualification", packageId: "basic", sizeGb: 10, autoRenew: false }
    }, state.auth);
    state.workspaceLaunchPosts = 1;
    assert.equal(loss.upstream.status, 202);
    assert.equal(loss.upstream.payload?.operationId, state.operationId);
    assert.equal(loss.upstream.payload?.workspaceId, state.workspaceId);
    state.launchUnknown = { clientUnknown: loss.clientUnknown, upstreamStatus: loss.upstream.status };

    await state.compose(["restart", "control-plane", "fabric", "ledger"]);
    await waitForCompose(state.compose);
    state.auth = await loginWhenReady(state.http, state.email, state.password);
    state.launch = await waitForLaunch(state.http, state.operationId, state.auth);
    state.receiptId = String(state.launch.receiptId);
    const beforeRestart = await readWorkspaceEvidence(state.http, state.auth, state.operationId, state.workspaceId, state.receiptId);
    state.identitiesBeforeRestart = launchIdentities(beforeRestart);
    assert.ok(Object.values(state.identitiesBeforeRestart).every((value) => String(value).trim() !== ""));
    state.fabricStagesBeforeRestart = await fabricStageEvidence();

    await state.compose(["restart", "control-plane", "fabric", "ledger"]);
    await waitForCompose(state.compose);
    state.auth = await loginWhenReady(state.http, state.email, state.password);
    state.evidence = await readWorkspaceEvidence(state.http, state.auth, state.operationId, state.workspaceId, state.receiptId);
    state.launch = state.evidence.launch;
    state.identitiesAfterRestart = launchIdentities(state.evidence);
    state.fabricStagesAfterRestart = await fabricStageEvidence();
    state.controlPlaneOperationCount = await operationCount();
    const receiptsPage = sourceData((await state.http.json("/api/billing/receipts?limit=50", {}, state.auth)).payload, "ledger");
    state.purchaseReceipts = (receiptsPage?.receipts || []).filter((entry: any) => entry?.type === "billing.workspace_purchased.v1");
  };

  before(async () => {
    const suffix = `${process.pid}-${randomBytes(4).toString("hex")}`;
    state.project = `opl-vertical-${suffix}`;
    state.tempRoot = await mkdtemp(join(tmpdir(), "opl-vertical-"));
    state.secretRoot = join(state.tempRoot, "fabric-secrets");
    await mkdir(state.secretRoot, { recursive: true, mode: 0o700 });
    state.envFile = join(state.tempRoot, "vertical.env");
    state.publicPort = await unusedPort();
    state.authorityPort = await unusedPort();
    state.registryPort = await unusedPort();
    state.accountId = "acct-admin";
    state.email = "local-vertical@example.test";
    state.password = `Vertical-${randomBytes(18).toString("base64url")}-Aa1!`;
    state.userToken = randomBytes(32).toString("hex");
    state.authorityToken = randomBytes(32).toString("hex");
    state.launchKey = `local-vertical:${suffix}`;
    state.operationId = `workspace-launch-${stableID(state.accountId, state.launchKey).slice(0, 18)}`;
    state.workspaceId = `ws-${stableID("workspace-launch-v2", state.accountId, state.operationId).slice(0, 18)}`;
    state.sourceSha = command(["git", "rev-parse", "HEAD"]).stdout.trim();

    state.cloudImage = String(process.env.OPL_CLOUD_IMAGE || "").trim();
    state.workspaceImage = String(process.env.OPL_WORKSPACE_IMAGE || "").trim();
    state.builtTags = [];
    state.registryContainer = "";
    if (!immutableReference(state.cloudImage) || !immutableReference(state.workspaceImage)) {
      const built = await buildSourceImages(state.sourceSha, state.project, state.registryPort);
      state.cloudImage = built.cloudImage;
      state.workspaceImage = built.workspaceImage;
      state.builtTags = built.tags;
      state.registryContainer = built.registryContainer;
    }

    const dockerHost = command(["docker", "context", "inspect", "--format", "{{(index .Endpoints \"docker\").Host}}"])
      .stdout.trim();
    const dockerSocket = dockerHost.startsWith("unix://") ? dockerHost.slice("unix://".length) : "/var/run/docker.sock";
    const subnetOctet = 20 + (Number.parseInt(randomBytes(1).toString("hex"), 16) % 200);
    const secrets = Array.from({ length: 10 }, () => randomBytes(32).toString("hex"));
    const entries = [
      ["OPL_CLOUD_IMAGE", state.cloudImage],
      ["OPL_WORKSPACE_IMAGE", state.workspaceImage],
      ["OPL_QUALIFICATION_SOURCE_SHA", state.sourceSha],
      ["OPL_BIND_ADDRESS", "127.0.0.1"],
      ["OPL_HTTP_PORT", String(state.publicPort)],
      ["OPL_PUBLIC_URL", `http://127.0.0.1:${state.publicPort}`],
      ["OPL_DOCKER_SUBNET", `10.251.${subnetOctet}.0/24`],
      ["OPL_POSTGRES_HOST", `10.251.${subnetOctet}.10`],
      ["OPL_POSTGRES_ADMIN_PASSWORD", secrets[0]],
      ["OPL_CONTROL_PLANE_DATABASE_PASSWORD", secrets[1]],
      ["OPL_FABRIC_DATABASE_PASSWORD", secrets[2]],
      ["OPL_LEDGER_DATABASE_PASSWORD", secrets[3]],
      ["OPL_CONTROL_PLANE_SERVICE_TOKEN", secrets[4]],
      ["OPL_FABRIC_SERVICE_TOKEN", secrets[5]],
      ["OPL_LEDGER_SERVICE_TOKEN", secrets[6]],
      ["OPL_FABRIC_RUNNER_SERVICE_TOKEN", secrets[7]],
      ["OPL_FABRIC_CAPABILITY_KEY", secrets[8]],
      ["OPL_LEDGER_CAPABILITY_KEY", secrets[9]],
      ["OPL_AIONUI_ADMIN_PASSWORD_SEED", randomBytes(32).toString("hex")],
      ["OPL_SUB2API_BASE_URL", "http://sub2api-authority:8080"],
      ["OPL_SUB2API_ADMIN_EMAIL", state.email],
      ["OPL_SUB2API_ADMIN_PASSWORD", state.password],
      ["OPL_QUALIFICATION_USER_EMAIL", state.email],
      ["OPL_QUALIFICATION_USER_PASSWORD", state.password],
      ["OPL_QUALIFICATION_USER_TOKEN", state.userToken],
      ["OPL_QUALIFICATION_AUTHORITY_TOKEN", state.authorityToken],
      ["OPL_QUALIFICATION_AUTHORITY_HOST_PORT", String(state.authorityPort)],
      ["OPL_QUALIFICATION_INITIAL_USD_MICROS", "1000000000"],
      ["OPL_DOCKER_SOCKET_PATH", dockerSocket],
      ["OPL_FABRIC_LOCAL_DOCKER_SECRET_ROOT", state.secretRoot],
      ["OPL_FABRIC_LOCAL_DOCKER_GATEWAY_CONTAINER", `${state.project}-control-plane-1`],
      ["OPL_SUB2API_REQUEST_TIMEOUT_MS", "5000"],
      ["OPL_MONTHLY_BILLING_WORKER_ENABLED", "0"],
      ["OPL_WORKSPACE_LAUNCH_WORKER_ENABLED", "1"],
      ["OPL_FABRIC_LOCAL_DOCKER_TRUSTED_WORKSPACE_IMAGES", state.workspaceImage],
      ["OPL_FABRIC_LOCAL_DOCKER_HOST", "127.0.0.1"]
    ];
    state.composeEnv = qualificationComposeEnvironment(process.env, entries);
    await writeFile(state.envFile, `${entries.map(([key, value]) => `${key}=${value}`).join("\n")}\n`, { mode: 0o600 });
    state.composePrefix = [
      "compose", "--project-name", state.project, "--env-file", state.envFile,
      "-f", "compose.yaml",
      "-f", "deploy/portable/compose.local-workspace.yaml",
      "-f", "deploy/portable/compose.local-qualification.yaml"
    ];
    state.compose = async (args: string[], settings: { allowFailure?: boolean } = {}) => {
      const result = command(["docker", ...state.composePrefix, ...args], state.composeEnv, !settings.allowFailure);
      return { code: result.status, stdout: result.stdout, stderr: result.stderr };
    };
    await state.compose(["config", "--quiet"]);
    await waitForCompose(state.compose);
    state.http = createHTTP(`http://127.0.0.1:${state.publicPort}`);
  }, { timeout: 20 * 60_000 });

  after(async () => {
    if (!state.project) return;
    const evidence = await cleanupOwnedResources();
    assert.deepEqual(evidence, {
      residuals: { containers: 0, volumes: 0, networks: 0 },
      compose: { containers: 0, volumes: 0, networks: 0 },
      registryContainers: 0,
      taskImages: 0,
      tempRootPresent: false
    });
  }, { timeout: 5 * 60_000 });

  verticalTest("E0", "E0 fresh PostgreSQL owners and restart count zero", async () => {
    const stores = await verifyStores(state.compose);
    assert.deepEqual(stores, { controlPlane: "durable", fabric: "durable", ledger: "durable", ownerSeparated: true });
    for (const service of ["postgres", "sub2api-authority", "ledger", "fabric", "control-plane"]) {
      const id = (await state.compose(["ps", "-q", service])).stdout.trim();
      assert.ok(id, `${service} container`);
      const [inspection] = JSON.parse(command(["docker", "inspect", id]).stdout);
      assert.equal(inspection.State.Status, "running", `${service} status`);
      assert.equal(inspection.State.Health?.Status, "healthy", `${service} health`);
      assert.equal(inspection.RestartCount, 0, `${service} restart count`);
    }
    const authority = await authorityState(state.authorityPort, state.authorityToken);
    assert.deepEqual(authority.writeCounts, { keyCreates: 0, keyDeletes: 0, debits: 0, refunds: 0 });
  });

  verticalTest("E1", "E1 canonical nine-stage launch is exactly once", async () => {
    await ensureLaunchContinuation();
    const attempts = state.launch.continuationAttemptBudgets;
    assert.deepEqual(Object.keys(attempts).sort(), [...productMatrixStages].sort());
    for (const stage of productMatrixStages) {
      assert.equal(attempts[stage].attempted, 1, `${stage} attempted`);
      assert.equal(attempts[stage].confirmed, 1, `${stage} confirmed`);
      assert.equal(attempts[stage].unknown, 0, `${stage} unknown`);
      assert.equal(attempts[stage].max, 1, `${stage} max`);
      assert.equal(attempts[stage].status, "confirmed", `${stage} status`);
    }
    const authority = await authorityState(state.authorityPort, state.authorityToken);
    assert.equal(authority.writeCounts.keyCreates, 1);
    assert.equal(authority.writeCounts.debits, 1);
  }, { timeout: 8 * 60_000 });

  verticalTest("E2", "E2 intermediate and succeeded restart preserve identities", async () => {
    await ensureLaunchContinuation();
    const before = state.evidence;
    await state.compose(["restart", "control-plane", "fabric", "ledger"]);
    await waitForCompose(state.compose);
    state.auth = await loginWhenReady(state.http, state.email, state.password);
    const afterRestart = await readWorkspaceEvidence(state.http, state.auth, state.operationId, state.workspaceId, state.receiptId);
    assert.equal(afterRestart.launch.operationId, before.launch.operationId);
    assert.equal(afterRestart.workspace.id, before.workspace.id);
    assert.equal(afterRestart.runtime.runtimeId, before.runtime.runtimeId);
    assert.equal(afterRestart.receipt.receiptId, before.receipt.receiptId);
    state.evidence = afterRestart;
  }, { timeout: 5 * 60_000 });

  verticalTest("E3", "E3 runtime status and workspace open are authoritative", async () => {
    await ensureLaunchContinuation();
    const runtime = state.evidence.runtime;
    assert.equal(runtime.ready, true);
    assert.equal(runtime.status, "running");
    const opened = await state.http.request(`/w/${encodeURIComponent(state.workspaceId)}/`, { redirect: "follow" });
    assert.equal(opened.response.status, 200, opened.text);
    assert.match(opened.text, /OPL Workspace READY/);
    assert.equal(runtime.url, state.launch.url);
  });

  // E4 is intentionally not registered: BLOCKED_PRODUCT_DECISION. This harness must not call commercial Owner DELETE.

  verticalTest("E5", "E5 launch pending unknown continuation preserves one operation", async () => {
    await ensureLaunchContinuation();
    assert.deepEqual(state.launchUnknown, { clientUnknown: true, upstreamStatus: 202 });
    assert.equal(state.workspaceLaunchPosts, 1);
    assert.equal(state.controlPlaneOperationCount, 1);
    assert.deepEqual(state.identitiesAfterRestart, state.identitiesBeforeRestart);
    assert.deepEqual(state.fabricStagesAfterRestart, state.fabricStagesBeforeRestart);
    assert.deepEqual(state.fabricStagesAfterRestart, providerStages.slice().sort().map((stage) => ({ stage, status: "succeeded", count: 1 })));
    const authority = await authorityState(state.authorityPort, state.authorityToken);
    assert.deepEqual(authority.writeCounts, { keyCreates: 1, keyDeletes: 0, debits: 1, refunds: 0 });
    assert.equal(state.purchaseReceipts.length, 1);
  }, { timeout: 8 * 60_000 });

  verticalTest("E6", "E6 fixture authority launch cardinalities and bindings are exact", async () => {
    await ensureLaunchContinuation();
    const authorityBeforeReplay = await authorityState(state.authorityPort, state.authorityToken);
    const me = sourceData((await state.http.json("/api/auth/me", {}, state.auth)).payload, "sub2api");
    const wallet = sourceData((await state.http.json("/api/gateway/wallet", {}, state.auth)).payload, "sub2api");
    const usage = sourceData((await state.http.json("/api/gateway/usage-summary?period=month", {}, state.auth)).payload, "sub2api");
    assert.equal(me.accountId, state.accountId);
    assert.equal(String(me.sub2apiUserId), String(authorityBeforeReplay.user.id));
    assert.equal(me.status, "active");
    assert.equal(authorityBeforeReplay.user.email, state.email);
    assert.equal(authorityBeforeReplay.user.status, "active");
    assert.equal(typeof usage.totalRequests, "number");
    assert.equal(authorityBeforeReplay.keys.length, 1);
    assert.equal(String(authorityBeforeReplay.keys[0].id), state.identitiesAfterRestart.keyId);
    assert.equal(authorityBeforeReplay.keys[0].status, "active");
    const debits = authorityBeforeReplay.adjustments.filter((entry: any) => entry.kind === "debit");
    const refunds = authorityBeforeReplay.adjustments.filter((entry: any) => entry.kind === "refund");
    assert.equal(debits.length, 1);
    assert.equal(refunds.length, 0);
    assert.equal(debits[0].code, state.identitiesAfterRestart.redeemCode);
    assert.equal(String(debits[0].amountUsdMicros), state.amountUsdMicros);
    assert.equal(String(debits[0].userId), String(authorityBeforeReplay.user.id));
    assert.equal(String(wallet.usdMicros), String(authorityBeforeReplay.wallet.usdMicros));
    assert.equal(BigInt(state.walletBefore.usdMicros) - BigInt(state.amountUsdMicros), BigInt(wallet.usdMicros));
    assert.equal(state.purchaseReceipts.length, 1);
    assert.equal(state.purchaseReceipts[0].receiptId, state.receiptId);
    assert.equal(state.evidence.receipt.chargeReference, debits[0].code);
    assert.equal(String(state.evidence.receipt.totalUsdMicros), state.amountUsdMicros);

    await waitForLaunch(state.http, state.operationId, state.auth);
    await readWorkspaceEvidence(state.http, state.auth, state.operationId, state.workspaceId, state.receiptId);
    const authorityAfterReplay = await authorityState(state.authorityPort, state.authorityToken);
    assert.deepEqual(authorityAfterReplay.writeCounts, authorityBeforeReplay.writeCounts);
    assert.deepEqual(authorityAfterReplay.writeCounts, { keyCreates: 1, keyDeletes: 0, debits: 1, refunds: 0 });
    const receiptsPage = sourceData((await state.http.json("/api/billing/receipts?limit=50", {}, state.auth)).payload, "ledger");
    assert.equal((receiptsPage.receipts || []).filter((entry: any) => entry.type === "billing.workspace_purchased.v1").length, 1);
    assert.equal((receiptsPage.receipts || []).filter((entry: any) => entry.type === "billing.workspace_refunded.v1").length, 0);
  }, { timeout: 8 * 60_000 });

  verticalTest("E7", "E7 qualification-owned exact-label cleanup leaves zero residuals", async () => {
    await ensureLaunchContinuation();
    const evidence = await cleanupOwnedResources();
    assert.deepEqual(evidence, {
      residuals: { containers: 0, volumes: 0, networks: 0 },
      compose: { containers: 0, volumes: 0, networks: 0 },
      registryContainers: 0,
      taskImages: 0,
      tempRootPresent: false
    });
  }, { timeout: 5 * 60_000 });
}
