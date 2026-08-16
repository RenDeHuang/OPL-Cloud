import { spawn } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import { mkdir, mkdtemp, readFile, rename, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

import {
  productMatrixRequiredPackages,
  productMatrixRequiredTests,
  productMatrixStages
} from "./verify-local.ts";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const baseComposeFiles = ["compose.yaml", "deploy/portable/compose.local-workspace.yaml"];
const digestPattern = /^sha256:[0-9a-f]{64}$/;
const shaPattern = /^[0-9a-f]{40}$/;
const deferredCloudGates = Object.freeze([
  "tencent-tke",
  "tcr-and-tke-image-pull",
  "kubernetes-secret-and-pod-readiness",
  "production-sub2api",
  "production-secrets-and-network",
  "instance-deployment-and-rollback"
]);

export function immutableImageDigest(value) {
  const normalized = String(value || "").trim();
  if (digestPattern.test(normalized)) return normalized;
  const marker = normalized.lastIndexOf("@sha256:");
  if (marker <= 0) return "";
  const digest = normalized.slice(marker + 1);
  return digestPattern.test(digest) ? digest : "";
}

function immutableImageReference(value) {
  const normalized = String(value || "").trim();
  return /^(?:[^\s@]+)@sha256:[0-9a-f]{64}$/.test(normalized) ? normalized : "";
}

function optionValues(args) {
  const values = new Map();
  let buildSourceImages = false;
  for (let index = 0; index < args.length; index += 1) {
    const token = args[index];
    if (token === "--build-source-images") {
      buildSourceImages = true;
      continue;
    }
    if (!["--source-sha", "--cloud-image", "--workspace-image", "--receipt", "--authority-mode", "--product-matrix-receipt"].includes(token)) {
      throw new Error(`unknown local qualification argument: ${token}`);
    }
    const value = args[index + 1];
    if (!value || value.startsWith("--") || values.has(token)) {
      throw new Error(`local qualification argument ${token} requires one value`);
    }
    values.set(token, value);
    index += 1;
  }
  return { values, buildSourceImages };
}

export function parseLocalQualificationArgs(args = process.argv.slice(2)) {
  const { values, buildSourceImages } = optionValues(args);
  const sourceSha = String(values.get("--source-sha") || "").trim();
  const cloudImage = String(values.get("--cloud-image") || "").trim();
  const workspaceImage = String(values.get("--workspace-image") || "").trim();
  const receiptPath = String(values.get("--receipt") || "").trim();
  const authorityMode = String(values.get("--authority-mode") || "fixture").trim();
  const productMatrixReceipt = String(values.get("--product-matrix-receipt") || "").trim();
  if (!shaPattern.test(sourceSha)) throw new Error("source SHA must be an exact 40-character lowercase commit");
  if (!receiptPath) throw new Error("receipt path is required");
  if (!buildSourceImages && !immutableImageReference(cloudImage)) throw new Error("immutable cloud image repository@digest is required");
  if (!buildSourceImages && !immutableImageReference(workspaceImage)) throw new Error("immutable workspace image repository@digest is required");
  if (buildSourceImages && (cloudImage || workspaceImage)) {
    throw new Error("source image build cannot be combined with explicit image inputs");
  }
  if (authorityMode !== "fixture" && authorityMode !== "live") throw new Error("authority mode must be fixture or live");
  return { sourceSha, cloudImage, workspaceImage, receiptPath, buildSourceImages, authorityMode, productMatrixReceipt };
}

function requireString(value, label) {
  if (!String(value || "").trim()) throw new Error(`${label} is required`);
}

export function redactedError(error) {
  return String(error instanceof Error ? error.message : error)
    .replace(/([a-z][a-z0-9+.-]*:\/\/)[^\s/@:]+(?::[^\s/@]*)?@/gi, "$1[redacted]@")
    .replace(/([a-z][a-z0-9+.-]*:\/\/[^\s?#]+)\?[^\s#]*/gi, "$1?[redacted]")
    .replace(/([a-z][a-z0-9+.-]*:\/\/[^\s#]+)#[^\s]*/gi, "$1#[redacted]")
    .replace(/[\r\n]+/g, " ")
    .slice(0, 1000);
}

function exactQualificationSourceIdentity(value) {
  return value && shaPattern.test(String(value.sha || "")) && shaPattern.test(String(value.tree || "")) && value.clean === true;
}

export function validateQualificationSourceIdentity(before, after, requestedSourceSha) {
  if (!exactQualificationSourceIdentity(before) || !exactQualificationSourceIdentity(after)) {
    throw new Error("local qualification requires a clean exact source identity");
  }
  if (before.sha !== requestedSourceSha || after.sha !== requestedSourceSha) {
    throw new Error("local qualification HEAD does not equal the requested source SHA");
  }
  if (before.sha !== after.sha || before.tree !== after.tree) {
    throw new Error("local qualification source changed while qualification was running");
  }
  return after;
}

export function validateProductMatrixReceipt(value, sourceSha, sourceTree) {
  if (!value || value.schemaVersion !== 1 || value.status !== "READY" || value.zeroSkip !== true ||
    value.source?.sha !== sourceSha || value.source?.tree !== sourceTree) {
    throw new Error("Product matrix receipt source or status is invalid");
  }
  if (!Array.isArray(value.stages) || value.stages.length !== productMatrixStages.length ||
    value.stages.some((stage, index) => stage?.name !== productMatrixStages[index] || stage?.passed !== true || stage?.skipped !== 0)) {
    throw new Error("Product matrix receipt must prove the exact nine-stage zero-skip set");
  }
  if (value.cas?.winnerCount !== 1 || value.cas?.loserMutationCount !== 0) {
    throw new Error("Product matrix receipt CAS proof is invalid");
  }
  const deltas = value.unknown?.authorityWriteDeltas;
  if (!deltas || ["controlPlane", "sub2api", "fabric", "ledger"].some((name) => deltas[name] !== 0)) {
    throw new Error("Product matrix receipt unknown write deltas are invalid");
  }
  const packageNames = Array.isArray(value.packages) ? value.packages.map((entry) => entry?.name) : [];
  if (packageNames.length === 0 || new Set(packageNames).size !== packageNames.length ||
    value.packages.some((entry) => !String(entry?.name || "").trim() || entry?.passed !== true || entry?.skipped !== 0) ||
    productMatrixRequiredPackages.some((name) => !packageNames.includes(name))) {
    throw new Error("Product matrix receipt package evidence is invalid");
  }
  if (!Array.isArray(value.tests) || value.tests.length !== productMatrixRequiredTests.length ||
    value.tests.some((entry, index) => entry?.package !== productMatrixRequiredTests[index].package ||
      entry?.name !== productMatrixRequiredTests[index].name || entry?.passed !== true || entry?.skipped !== 0)) {
    throw new Error("Product matrix receipt test evidence is invalid");
  }
  return value;
}

async function loadProductMatrixReceipt(path, sourceSha, sourceTree) {
  if (!path) return null;
  const raw = await readFile(resolve(path));
  const value = validateProductMatrixReceipt(JSON.parse(raw.toString("utf8")), sourceSha, sourceTree);
  return {
    digest: `sha256:${createHash("sha256").update(raw).digest("hex")}`,
    source: { sha: value.source.sha, tree: value.source.tree },
    stages: [...productMatrixStages],
    packages: value.packages.map((entry) => entry.name),
    tests: value.tests.map((entry) => `${entry.package}:${entry.name}`),
    zeroSkip: true,
    casWinnerCount: 1,
    unknownAuthorityWriteDeltas: { ...value.unknown.authorityWriteDeltas }
  };
}

export function validateLocalQualificationReceipt(value) {
  if (!value || value.schemaVersion !== 1 || value.status !== "READY") throw new Error("READY receipt is required");
  if (!shaPattern.test(String(value.source?.sha || "")) || !shaPattern.test(String(value.source?.tree || ""))) {
    throw new Error("source identity is invalid");
  }
  for (const name of ["cloud", "workspace"]) {
    const image = value.images?.[name];
    if (!immutableImageReference(image?.input) || image?.repoDigest !== image?.input || !digestPattern.test(String(image?.digest || "")) || !digestPattern.test(String(image?.runningDigest || ""))) {
      throw new Error(`${name} image identity is invalid`);
    }
  }
  requireString(value.command, "qualification command");
  if (!value.qualification || !["fixture", "live"].includes(value.qualification.authorityMode) ||
    value.qualification.p0Ready !== (value.qualification.authorityMode === "live" && value.productMatrix?.zeroSkip === true)) {
    throw new Error("qualification authority classification is invalid");
  }
  if (value.qualification.authorityMode === "live" && (!digestPattern.test(String(value.productMatrix?.digest || "")) ||
    value.productMatrix?.casWinnerCount !== 1 || value.productMatrix?.stages?.join("\0") !== productMatrixStages.join("\0") ||
    !Array.isArray(value.productMatrix?.packages) || productMatrixRequiredPackages.some((name) => !value.productMatrix.packages.includes(name)) ||
    value.productMatrix?.tests?.join("\0") !== productMatrixRequiredTests.map((entry) => `${entry.package}:${entry.name}`).join("\0") ||
    ["controlPlane", "sub2api", "fabric", "ledger"].some((name) => value.productMatrix?.unknownAuthorityWriteDeltas?.[name] !== 0))) {
    throw new Error("live qualification requires the exact Product matrix receipt binding");
  }
  for (const name of ["console", "controlPlane", "fabric", "ledger"]) {
    if (value.processes?.[name] !== "ready") throw new Error(`${name} process is not ready`);
  }
  if (value.stores?.ownerSeparated !== true || ["controlPlane", "fabric", "ledger"].some((name) => value.stores?.[name] !== "durable")) {
    throw new Error("durable owner-separated stores are required");
  }
  for (const key of [
    "accountId", "sub2apiUserId", "launchOperationId", "deleteOperationId", "refundOperationId", "workspaceId", "runtimeId", "keyId",
    "debitCode", "purchaseReceiptId", "refundReceiptId"
  ]) {
    requireString(value.identities?.[key], `identity ${key}`);
  }
  if (value.identities.refundOperationId !== value.identities.deleteOperationId) {
    throw new Error("refund operation must reuse the owner DELETE operation identity");
  }
  if (value.debit?.count !== 1 || !String(value.debit?.code || "").startsWith("opl:") ||
    value.debit?.accountId !== value.identities.accountId || value.debit?.code !== value.identities.debitCode || value.debit?.operationId !== value.identities.launchOperationId ||
    value.debit?.workspaceId !== value.identities.workspaceId ||
    String(value.debit?.userId || "") !== String(value.identities.sub2apiUserId) || !/^[1-9][0-9]*$/.test(String(value.debit?.amountUsdMicros || ""))) {
    throw new Error("exact debit evidence is invalid");
  }
  if (!["beforeUsdMicros", "afterUsdMicros", "restoredUsdMicros"].every((key) => /^\d+$/.test(String(value.wallet?.[key] || "")))) {
    throw new Error("wallet readback is invalid");
  }
  if (value.qualification.authorityMode === "fixture" &&
    (BigInt(value.wallet.beforeUsdMicros) - BigInt(value.debit.amountUsdMicros) !== BigInt(value.wallet.afterUsdMicros) ||
      value.wallet.restoredUsdMicros !== value.wallet.beforeUsdMicros)) {
    throw new Error("isolated fixture wallet readback does not equal the exact debit and refund");
  }
  if (value.receipt?.count !== 1 || value.receipt?.id !== value.identities.purchaseReceiptId ||
    value.receipt?.accountId !== value.identities.accountId || value.receipt?.operationId !== value.identities.launchOperationId ||
    value.receipt?.workspaceId !== value.identities.workspaceId || value.receipt?.runtimeId !== value.identities.runtimeId ||
    String(value.receipt?.keyId || "") !== String(value.identities.keyId) ||
    value.receipt?.chargeReference !== value.identities.debitCode || String(value.receipt?.amountUsdMicros || "") !== String(value.debit.amountUsdMicros)) {
    throw new Error("receipt binding is invalid");
  }
  if (!value.restart?.performed || ["operationStable", "workspaceStable", "runtimeStable", "receiptStable"].some((key) => value.restart?.[key] !== true)) {
    throw new Error("restart continuity is invalid");
  }
  if (value.deletion?.ownerAuthorized !== true || value.deletion?.workspaceAbsent !== true || value.deletion?.runtimeAbsent !== true ||
    value.deletion?.workspaceKeyAbsent !== true || value.deletion?.fabricSecretAbsent !== true ||
    value.deletion?.accountId !== value.identities.accountId || value.deletion?.operationId !== value.identities.deleteOperationId ||
    value.deletion?.refundOperationId !== value.identities.refundOperationId || value.deletion?.workspaceId !== value.identities.workspaceId ||
    value.deletion?.runtimeId !== value.identities.runtimeId || String(value.deletion?.keyId || "") !== String(value.identities.keyId)) {
    throw new Error("owner deletion evidence is invalid");
  }
  if (["containers", "volumes", "networks"].some((key) => value.residuals?.[key] !== 0)) {
    throw new Error("exact-labelled residual evidence is invalid");
  }
  if (value.qualification.authorityMode === "fixture" && (value.authorityWriteCounts?.keyCreates !== 1 ||
    value.authorityWriteCounts?.keyDeletes !== 1 || value.authorityWriteCounts?.debits !== 1 || value.authorityWriteCounts?.refunds !== 1)) {
    throw new Error("qualification fixture write counts are invalid");
  }
  if (value.mutationCounts?.workspaceLaunchPosts !== 1 || value.mutationCounts?.workspaceDeleteRequests !== 1 || value.mutationCounts?.refundPosts !== 0) {
    throw new Error("qualification mutation counts are invalid");
  }
  if (value.refund?.count !== 1 || !String(value.refund?.code || "").trim() ||
    value.refund?.accountId !== value.identities.accountId || value.refund?.operationId !== value.identities.refundOperationId || value.refund?.workspaceId !== value.identities.workspaceId ||
    value.refund?.debitCode !== value.identities.debitCode ||
    String(value.refund?.userId || "") !== String(value.identities.sub2apiUserId) ||
    String(value.refund?.amountUsdMicros || "") !== String(value.debit.amountUsdMicros) ||
    value.refund?.receiptId !== value.identities.refundReceiptId) {
    throw new Error("exact refund evidence is invalid");
  }
  if (value.refundReceipt?.count !== 1 || value.refundReceipt?.id !== value.identities.refundReceiptId ||
    value.refundReceipt?.type !== "billing.workspace_refunded.v1" ||
    value.refundReceipt?.accountId !== value.identities.accountId || value.refundReceipt?.operationId !== value.identities.refundOperationId || value.refundReceipt?.workspaceId !== value.identities.workspaceId ||
    value.refundReceipt?.chargeReference !== value.identities.debitCode || value.refundReceipt?.refundCode !== value.refund.code ||
    String(value.refundReceipt?.amountUsdMicros || "") !== String(value.debit.amountUsdMicros)) {
    throw new Error("refund receipt binding is invalid");
  }
  if (value.usage?.source !== "sub2api" || value.usage?.status !== "available") throw new Error("Sub2API usage readback is invalid");
  const serialized = JSON.stringify(value);
  if (/"(?:password|cookie|csrf|authorization|token|apiKey)"\s*:/i.test(serialized)) {
    throw new Error("receipt contains a forbidden credential field");
  }
  return value;
}

function runProcess(command, args, { cwd = root, env = process.env, capture = true, allowFailure = false, stdin } = {}) {
  return new Promise((resolvePromise, reject) => {
    const child = spawn(command, args, { cwd, env, stdio: [stdin === undefined ? "ignore" : "pipe", "pipe", "pipe"] });
    let stdout = "";
    let stderr = "";
    child.stdout.setEncoding("utf8");
    child.stderr.setEncoding("utf8");
    child.stdout.on("data", (chunk) => { stdout += chunk; });
    child.stderr.on("data", (chunk) => { stderr += chunk; });
    if (stdin !== undefined) child.stdin.end(stdin);
    child.on("error", reject);
    child.on("close", (code, signal) => {
      const result = { code, signal, stdout, stderr };
      if (code === 0 || allowFailure) {
        if (!capture && stdout) process.stdout.write(stdout);
        if (!capture && stderr) process.stderr.write(stderr);
        resolvePromise(result);
        return;
      }
      reject(new Error(`${command} ${args.join(" ")} failed with ${signal || code}: ${(stderr || stdout).trim().slice(-8000)}`));
    });
  });
}

async function unusedPort() {
  const server = createServer();
  await new Promise((resolvePromise, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolvePromise);
  });
  const address = server.address();
  const port = address && typeof address === "object" ? address.port : 0;
  await new Promise((resolvePromise, reject) => server.close((error) => error ? reject(error) : resolvePromise()));
  if (!port) throw new Error("local qualification could not allocate a loopback port");
  return port;
}

function stableID(...parts) {
  const hash = createHash("sha1");
  for (const part of parts) {
    hash.update(String(part));
    hash.update(Buffer.from([0]));
  }
  return hash.digest("hex");
}

async function writeJSONAtomic(path, value) {
  await mkdir(dirname(path), { recursive: true });
  const temporary = `${path}.${process.pid}.${randomBytes(4).toString("hex")}.tmp`;
  await writeFile(temporary, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600 });
  await rename(temporary, path);
}

async function dockerImageInspection(image, { pullIfMissing = false } = {}) {
  let inspected = await runProcess("docker", ["image", "inspect", image], { allowFailure: true });
  if (inspected.code !== 0 && pullIfMissing) {
    await runProcess("docker", ["pull", image], { capture: false });
    inspected = await runProcess("docker", ["image", "inspect", image]);
  }
  const values = JSON.parse(inspected.stdout);
  if (!Array.isArray(values) || values.length !== 1 || !digestPattern.test(String(values[0]?.Id || ""))) {
    throw new Error("Docker image inspection did not return one immutable image ID");
  }
  return values[0];
}

async function imageInspection(image) {
  if (!immutableImageReference(image)) throw new Error("qualified image input must be repository@sha256 digest");
  const inspection = await dockerImageInspection(image, { pullIfMissing: true });
  const [repository, digest] = image.split("@");
  if (!Array.isArray(inspection.RepoDigests) || !inspection.RepoDigests.includes(`${repository}@${digest}`)) {
    throw new Error(`Docker RepoDigest does not match admitted image ${repository}`);
  }
  return inspection;
}

export function exactRepoDigestFromInspection(repository, inspection) {
  const repoDigests = inspection?.RepoDigests;
  if (!String(repository || "").trim() || /[@\s]/.test(repository) || !digestPattern.test(String(inspection?.Id || "")) ||
    !Array.isArray(repoDigests) || repoDigests.length !== 1) {
    throw new Error(`source-built image ${repository || "unknown"} has no unique registry manifest digest`);
  }
  const [actualRepository, digest, extra] = String(repoDigests[0]).split("@");
  if (extra !== undefined || actualRepository !== repository || !digestPattern.test(String(digest || ""))) {
    throw new Error(`source-built image ${repository} has no unique registry manifest digest`);
  }
  return `${actualRepository}@${digest}`;
}

export function localBuildProxyArgs() {
  const proxy = String(process.env.OPL_LOCAL_BUILD_PROXY || "").trim();
  if (!proxy) return [];
  if (!/^(?:https?|socks5h?):\/\/[^\s]+$/.test(proxy)) {
    throw new Error("OPL_LOCAL_BUILD_PROXY must be an explicit HTTP(S) or SOCKS proxy URL");
  }
  const parsed = new URL(proxy);
  if (parsed.username || parsed.password) {
    throw new Error("OPL_LOCAL_BUILD_PROXY must not contain credentials");
  }
  if (parsed.search || parsed.hash) {
    throw new Error("OPL_LOCAL_BUILD_PROXY must not contain query or fragment parameters");
  }
  return proxy.startsWith("socks")
    ? ["--build-arg", `HTTPS_PROXY=${proxy}`]
    : ["--build-arg", `HTTP_PROXY=${proxy}`, "--build-arg", `HTTPS_PROXY=${proxy}`];
}

export function qualificationComposeEnvironment(baseEnvironment, exactEntries) {
  const environment = { ...baseEnvironment };
  for (const [key, value] of exactEntries) {
    if (!/^[A-Z][A-Z0-9_]*$/.test(String(key)) || /[\r\n]/.test(String(value))) {
      throw new Error("qualification compose environment entry is invalid");
    }
    environment[key] = String(value);
  }
  return environment;
}

async function buildSourceImages(sourceSha, project, registryPort) {
  const registryContainer = `${project}-registry`;
  const cloudRepository = `127.0.0.1:${registryPort}/${project}-cloud`;
  const workspaceRepository = `127.0.0.1:${registryPort}/${project}-workspace`;
  const cloudTag = `${cloudRepository}:source`;
  const workspaceTag = `${workspaceRepository}:source`;
  try {
    await runProcess("docker", ["run", "-d", "--name", registryContainer, "-p", `127.0.0.1:${registryPort}:5000`, "registry:2"]);
    const proxyArgs = localBuildProxyArgs();
    await runProcess("docker", ["build", ...proxyArgs, "--label", `org.opencontainers.image.revision=${sourceSha}`, "--tag", cloudTag, "."], { capture: false });
    await runProcess("docker", [
      "build", ...proxyArgs, "--label", `org.opencontainers.image.revision=${sourceSha}`,
      "--file", "deploy/portable/qualification-workspace.Dockerfile", "--tag", workspaceTag, "."
    ], { capture: false });
    await runProcess("docker", ["push", cloudTag], { capture: false });
    await runProcess("docker", ["push", workspaceTag], { capture: false });
    const cloudImage = exactRepoDigestFromInspection(cloudRepository, await dockerImageInspection(cloudTag));
    const workspaceImage = exactRepoDigestFromInspection(workspaceRepository, await dockerImageInspection(workspaceTag));
    await imageInspection(cloudImage);
    await imageInspection(workspaceImage);
    return { cloudImage, workspaceImage, tags: [cloudTag, workspaceTag], registryContainer };
  } catch (error) {
    for (const tag of [cloudTag, workspaceTag]) await runProcess("docker", ["image", "rm", tag], { allowFailure: true });
    await runProcess("docker", ["rm", "-f", registryContainer], { allowFailure: true });
    throw error;
  }
}

async function readQualificationSourceIdentity() {
  const [sha, tree, status] = await Promise.all([
    runProcess("git", ["rev-parse", "HEAD"]),
    runProcess("git", ["rev-parse", "HEAD^{tree}"]),
    runProcess("git", ["status", "--porcelain", "--untracked-files=all"])
  ]);
  return { sha: sha.stdout.trim(), tree: tree.stdout.trim(), clean: status.stdout.trim() === "" };
}

function sourceData(envelope, expectedSource) {
  if (!envelope || envelope.source !== expectedSource || envelope.available !== true || !["available", "empty"].includes(envelope.status)) {
    throw new Error(`${expectedSource} source readback is unavailable`);
  }
  return envelope.data;
}

function responseCookie(headers) {
  const value = headers.get("set-cookie");
  return value ? value.split(";", 1)[0] : "";
}

function createHTTP(origin) {
  const request = async (path, init = {}, auth = null) => {
    const headers = new Headers(init.headers || {});
    if (auth?.cookie) headers.set("cookie", auth.cookie);
    if (auth?.csrf) headers.set("x-opl-csrf", auth.csrf);
    const body = init.body === undefined ? undefined : JSON.stringify(init.body);
    if (body !== undefined) headers.set("content-type", "application/json");
    const response = await fetch(`${origin}${path}`, {
      ...init,
      headers,
      body,
      signal: AbortSignal.timeout(30_000)
    });
    const text = await response.text();
    let payload = null;
    if (text.trim()) {
      try { payload = JSON.parse(text); } catch { payload = null; }
    }
    return { response, payload, text };
  };
  const json = async (path, init = {}, auth = null, statuses = [200]) => {
    const result = await request(path, init, auth);
    if (!statuses.includes(result.response.status) || result.payload === null) {
      throw new Error(`HTTP ${init.method || "GET"} ${path} returned ${result.response.status}: ${result.text.slice(0, 500)}`);
    }
    return result;
  };
  return { request, json };
}

async function login(http, email, password) {
  const result = await http.json("/api/auth/login", { method: "POST", body: { email, password } });
  const auth = { cookie: responseCookie(result.response.headers), csrf: result.response.headers.get("x-opl-csrf-token") || "" };
  if (!auth.cookie || !auth.csrf || result.payload?.user?.accountId !== "acct-admin") {
    throw new Error("local qualification login did not establish the reserved account session");
  }
  return auth;
}

async function waitForLaunch(http, operationId, auth) {
  let launch;
  for (let attempt = 0; attempt < 180; attempt += 1) {
    launch = (await http.json(`/api/workspace-launches/${encodeURIComponent(operationId)}`, {}, auth)).payload;
    if (launch?.status === "succeeded" && launch?.phase === "succeeded") return launch;
    if (["manual_review", "failed", "refunded"].includes(String(launch?.status || ""))) {
      throw new Error(`Workspace launch stopped at ${launch.status}/${launch.phase}/${launch.errorCode || "none"}`);
    }
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 1000));
  }
  throw new Error("Workspace launch did not reach succeeded within 180 seconds");
}

async function waitForCompose(compose) {
  await compose(["up", "-d", "--wait", "--wait-timeout", "300"]);
}

async function inspectComposeImage(compose, service) {
  const id = (await compose(["ps", "-q", service])).stdout.trim();
  if (!id) throw new Error(`${service} Compose container is missing`);
  const values = JSON.parse((await runProcess("docker", ["inspect", id])).stdout);
  if (!Array.isArray(values) || values.length !== 1 || !digestPattern.test(String(values[0]?.Image || ""))) {
    throw new Error(`${service} running image readback is invalid`);
  }
  return values[0];
}

async function verifyStores(compose) {
  const query = "select datname || ':' || pg_get_userbyid(datdba) from pg_database where datname in ('opl_control_plane','opl_fabric','opl_ledger') order by datname";
  const rows = (await compose(["exec", "-T", "postgres", "psql", "-U", "postgres", "-d", "postgres", "-Atqc", query])).stdout.trim().split(/\r?\n/);
  const expected = ["opl_control_plane:opl_control_plane", "opl_fabric:opl_fabric", "opl_ledger:opl_ledger"];
  if (JSON.stringify(rows) !== JSON.stringify(expected)) throw new Error("PostgreSQL database ownership is not separated");
  for (const owner of ["control_plane", "fabric", "ledger"]) {
    const count = Number((await compose([
      "exec", "-T", "postgres", "psql", "-U", "postgres", "-d", `opl_${owner}`, "-Atqc",
      `select count(*) from pg_tables where schemaname='public' and tableowner='opl_${owner}'`
    ])).stdout.trim());
    if (!Number.isSafeInteger(count) || count <= 0) throw new Error(`opl_${owner} owns no durable tables`);
  }
  return { controlPlane: "durable", fabric: "durable", ledger: "durable", ownerSeparated: true };
}

async function consoleReadback(http) {
  const home = await http.request("/");
  if (home.response.status !== 200 || !/<div[^>]+id=["']root["']/.test(home.text)) throw new Error("Console entry asset is unavailable");
  const script = home.text.match(/<script[^>]+src=["']([^"']+)["']/)?.[1];
  if (!script) throw new Error("Console hashed script is missing");
  const asset = await http.request(script);
  if (asset.response.status !== 200 || !asset.text.trim()) throw new Error("Console hashed script is unavailable");
}

async function readWorkspaceEvidence(http, auth, operationId, workspaceId, receiptId) {
  const launch = (await http.json(`/api/workspace-launches/${encodeURIComponent(operationId)}`, {}, auth)).payload;
  if (launch?.status !== "succeeded" || launch?.phase !== "succeeded" || launch?.workspaceId !== workspaceId || launch?.receiptId !== receiptId) {
    throw new Error("Workspace launch continuity readback is invalid");
  }
  const page = sourceData((await http.json("/api/workspaces?page=1&pageSize=20", {}, auth)).payload, "control-plane");
  const workspace = page?.items?.find((candidate) => candidate?.id === workspaceId);
  if (!workspace || workspace.url !== launch.url) throw new Error("Workspace owner readback is invalid");
  const runtime = sourceData((await http.json(`/api/workspaces/${encodeURIComponent(workspaceId)}/runtime-status`, {}, auth)).payload, "fabric");
  if (!runtime || runtime.workspaceId !== workspaceId || runtime.ready !== true || runtime.status !== "running" || runtime.url !== launch.url) {
    throw new Error("Workspace runtime readback is invalid");
  }
  const receipt = sourceData((await http.json(`/api/billing/receipts/${encodeURIComponent(receiptId)}`, {}, auth)).payload, "ledger");
  if (receipt?.receiptId !== receiptId || receipt?.workspaceId !== workspaceId || receipt?.type !== "billing.workspace_purchased.v1" || receipt?.status !== "completed") {
    throw new Error("Ledger receipt readback is invalid");
  }
  return { launch, workspace, runtime, receipt };
}

async function exactLabelIDs(kind, accountId, workspaceId) {
  const base = [
    `label=opl.fabric.provider=local-docker`,
    `label=opl.account.id=${accountId}`,
    `label=opl.workspace.id=${workspaceId}`
  ];
  const command = kind === "containers" ? ["ps", "-aq"] : kind === "volumes" ? ["volume", "ls", "-q"] : ["network", "ls", "-q"];
  const args = [...command];
  for (const label of base) args.push("--filter", label);
  return (await runProcess("docker", args)).stdout.trim().split(/\r?\n/).filter(Boolean);
}

async function residualCounts(accountId, workspaceId) {
  const [containers, volumes, networks] = await Promise.all([
    exactLabelIDs("containers", accountId, workspaceId),
    exactLabelIDs("volumes", accountId, workspaceId),
    exactLabelIDs("networks", accountId, workspaceId)
  ]);
  return { containers: containers.length, volumes: volumes.length, networks: networks.length };
}

async function runtimeImageReadback(accountId, workspaceId, expectedImage, expectedImageID) {
  const ids = await exactLabelIDs("containers", accountId, workspaceId);
  const runtime = [];
  for (const id of ids) {
    const [inspection] = JSON.parse((await runProcess("docker", ["inspect", id])).stdout);
    if (inspection?.Config?.Labels?.["opl.fabric.kind"] === "runtime") runtime.push(inspection);
  }
  if (runtime.length !== 1) throw new Error(`expected one exact-labelled Runtime container, found ${runtime.length}`);
  const labels = runtime[0].Config.Labels || {};
  if (labels["opl.image.ref"] !== expectedImage || runtime[0].Image !== expectedImageID) {
    throw new Error("Workspace Runtime image binding is invalid");
  }
  return runtime[0];
}

async function authorityState(port, token) {
  const response = await fetch(`http://127.0.0.1:${port}/qualification/state`, {
    headers: { authorization: `Bearer ${token}` }, signal: AbortSignal.timeout(10_000)
  });
  const text = await response.text();
  if (response.status !== 200) throw new Error(`qualification authority state returned ${response.status}`);
  const payload = JSON.parse(text);
  if (payload?.code !== 0 || !payload.data || typeof payload.data !== "object") {
    throw new Error("qualification authority state envelope is invalid");
  }
  return payload.data;
}

function usdMicrosFromDecimal(value) {
  const normalized = String(value ?? "").trim();
  const match = normalized.match(/^(-?)([0-9]+)(?:\.([0-9]{1,6}))?$/);
  if (!match) throw new Error("live Sub2API adjustment amount is not an exact decimal");
  const micros = BigInt(match[2]) * 1_000_000n + BigInt((match[3] || "").padEnd(6, "0") || "0");
  return `${match[1] === "-" ? "-" : ""}${micros.toString()}`;
}

export async function liveAuthorityAdjustmentReadback(baseURL, email, password, userId, code, expectedValueUsdMicros, request = fetch) {
  const signal = AbortSignal.timeout(10_000);
  const loginResponse = await request(`${baseURL.replace(/\/$/, "")}/api/v1/auth/login`, {
    method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ email, password }),
    signal
  });
  const loginPayload = await loginResponse.json();
  const token = String(loginPayload?.data?.access_token || "");
  if (!loginResponse.ok || loginPayload?.code !== 0 || !token) throw new Error("live Sub2API admin authentication failed");
  let authorityTotal = -1;
  let authorityPages = -1;
  let match = null;
  for (let page = 1; ; page += 1) {
    const url = new URL(`${baseURL.replace(/\/$/, "")}/api/v1/admin/users/${encodeURIComponent(userId)}/balance-history`);
    url.searchParams.set("page", String(page));
    url.searchParams.set("page_size", "100");
    url.searchParams.set("type", "balance");
    const response = await request(url, { headers: { authorization: `Bearer ${token}` }, signal });
    const payload = await response.json();
    if (!response.ok || payload?.code !== 0 || !Array.isArray(payload?.data?.items)) throw new Error("live Sub2API balance-history readback failed");
    const data = payload.data;
    const total = Number(data.total);
    const pages = Number(data.pages);
    const expectedPages = total > 0 ? Math.ceil(total / 100) : 1;
    const expectedItems = total > 0 ? Math.min(100, total - (page - 1) * 100) : 0;
    if (!Number.isSafeInteger(total) || total < 0 || !Number.isSafeInteger(pages) || pages !== expectedPages ||
      data.page !== page || data.page_size !== 100 || page > pages || data.items.length !== expectedItems ||
      page > 1 && (total !== authorityTotal || pages !== authorityPages)) {
      throw new Error("live Sub2API balance-history pagination is invalid");
    }
    if (page === 1) {
      authorityTotal = total;
      authorityPages = pages;
    }
    for (const candidate of data.items) {
      if (candidate?.code !== code) continue;
      if (match) throw new Error("live Sub2API exact adjustment cardinality is invalid");
      const valueUsdMicros = usdMicrosFromDecimal(candidate.value);
      if (candidate.type !== "balance" || candidate.status !== "used" || String(candidate.used_by) !== String(userId) ||
        !String(candidate.used_at || "").trim() || !String(candidate.created_at || "").trim() || valueUsdMicros !== expectedValueUsdMicros) {
        throw new Error("live Sub2API adjustment readback differs from the expected identity");
      }
      match = { code, userId: String(userId), valueUsdMicros, status: "used", count: 1 };
    }
    if (page === authorityPages) break;
  }
  if (match) return match;
  throw new Error("live Sub2API exact adjustment was not found");
}

function receiptCommand(options) {
  const imageArgs = options.buildSourceImages
    ? "--build-source-images"
    : `--cloud-image ${options.cloudImage} --workspace-image ${options.workspaceImage}`;
  const matrixArg = options.productMatrixReceipt ? ` --product-matrix-receipt ${options.productMatrixReceipt}` : "";
  return `npm run qualify:local:workspace -- --source-sha ${options.sourceSha} ${imageArgs} --authority-mode ${options.authorityMode}${matrixArg} --receipt ${options.receiptPath}`;
}

async function writeEarlyNotReady(options, stage, errorCode, error) {
  let tree = "unavailable";
  try {
    tree = (await runProcess("git", ["rev-parse", `${options.sourceSha}^{tree}`])).stdout.trim() || tree;
  } catch {
    // The input receipt still records the exact requested source when the local checkout is incomplete.
  }
  await writeJSONAtomic(options.receiptPath, {
    schemaVersion: 1,
    status: "NOT_READY",
    completedAt: new Date().toISOString(),
    source: { sha: options.sourceSha, tree },
    images: {
      cloud: { input: options.cloudImage || "unavailable", digest: immutableImageDigest(options.cloudImage) || "unavailable" },
      workspace: { input: options.workspaceImage || "unavailable", digest: immutableImageDigest(options.workspaceImage) || "unavailable" }
    },
    command: receiptCommand(options),
    stage,
    errorCode,
    error: redactedError(error),
    deferred: [...deferredCloudGates]
  });
}

export async function runLocalWorkspaceQualification(options) {
  if (options.authorityMode === "live") {
    const baseURL = String(process.env.OPL_SUB2API_BASE_URL || "").trim();
    const email = String(process.env.OPL_SUB2API_ADMIN_EMAIL || "").trim();
    const password = String(process.env.OPL_SUB2API_ADMIN_PASSWORD || "");
    const userEmail = String(process.env.OPL_QUALIFICATION_USER_EMAIL || email).trim();
    const userPassword = String(process.env.OPL_QUALIFICATION_USER_PASSWORD || password);
    if (!/^https:\/\/[^\s]+$/.test(baseURL) || !email || !password || !userEmail || !userPassword ||
      !["sandbox", "preproduction"].includes(String(process.env.OPL_QUALIFICATION_AUTHORITY_CLASS || ""))) {
      const error = new Error("live qualification requires protected non-production Sub2API credentials and HTTPS authority");
      await writeEarlyNotReady(options, "authority_preflight", "live_authority_configuration_missing", error);
      throw error;
    }
  }
  const startedAt = new Date().toISOString();
  const suffix = `${process.pid}-${randomBytes(4).toString("hex")}`;
  const project = `opl-local-qualification-${suffix}`.toLowerCase();
  const tempRoot = await mkdtemp(join(tmpdir(), "opl-local-qualification-"));
  const fabricSecretRoot = join(tempRoot, "fabric-secrets");
  await mkdir(fabricSecretRoot, { recursive: true, mode: 0o700 });
  const envFile = join(tempRoot, "qualification.env");
  const publicPort = await unusedPort();
  const authorityPort = await unusedPort();
  const registryPort = await unusedPort();
  const subnetOctet = 20 + (Number.parseInt(randomBytes(1).toString("hex"), 16) % 200);
  const accountId = "acct-admin";
  const fixtureEmail = "local-qualification@example.test";
  const fixturePassword = `Local-${randomBytes(18).toString("base64url")}-Aa1!`;
  const adminEmail = options.authorityMode === "live" ? String(process.env.OPL_QUALIFICATION_USER_EMAIL || process.env.OPL_SUB2API_ADMIN_EMAIL || "") : fixtureEmail;
  const adminPassword = options.authorityMode === "live" ? String(process.env.OPL_QUALIFICATION_USER_PASSWORD || process.env.OPL_SUB2API_ADMIN_PASSWORD || "") : fixturePassword;
  const userToken = randomBytes(32).toString("hex");
  const authorityToken = randomBytes(32).toString("hex");
  const launchKey = `local-qualification:${suffix}`;
  const operationId = `workspace-launch-${stableID(accountId, launchKey).slice(0, 18)}`;
  const workspaceId = `ws-${stableID("workspace-launch-v2", accountId, operationId).slice(0, 18)}`;
  const sourceBefore = await readQualificationSourceIdentity();
  validateQualificationSourceIdentity(sourceBefore, sourceBefore, options.sourceSha);
  const sourceTree = sourceBefore.tree;
  let productMatrix;
  try {
    productMatrix = await loadProductMatrixReceipt(options.productMatrixReceipt, options.sourceSha, sourceTree);
    if (options.authorityMode === "live" && !productMatrix) throw new Error("live qualification requires the canonical Product matrix receipt");
  } catch (error) {
    await writeEarlyNotReady(options, "product_matrix_preflight", "product_matrix_receipt_invalid", error);
    throw error;
  }

  let cloudImage = options.cloudImage;
  let workspaceImage = options.workspaceImage;
  let builtTags = [];
  let registryContainer = "";
  let stage = "image_admission";
  let composeStarted = false;
  let auth = null;
  let finalReceipt;
  let failure;
  let residuals = { containers: null, volumes: null, networks: null };
  const composePrefix = ["compose", "--project-name", project, "--env-file", envFile];
  const qualificationCompose = options.authorityMode === "fixture" ? "deploy/portable/compose.local-qualification.yaml" : "deploy/portable/compose.local-qualification-live.yaml";
  for (const file of [...baseComposeFiles, qualificationCompose]) composePrefix.push("-f", file);
  let composeEnvironment = process.env;
  const compose = (args, settings = {}) => runProcess("docker", [...composePrefix, ...args], { ...settings, env: composeEnvironment });

  try {
    await runProcess("docker", ["version"]);
    await runProcess("docker", ["compose", "version"]);
    if (options.buildSourceImages) {
      ({ cloudImage, workspaceImage, tags: builtTags, registryContainer } = await buildSourceImages(options.sourceSha, project, registryPort));
    }
    const cloudInspection = await imageInspection(cloudImage);
    const workspaceInspection = await imageInspection(workspaceImage);
    const cloudRevision = String(cloudInspection.Config?.Labels?.["org.opencontainers.image.revision"] || "");
    if (cloudRevision !== options.sourceSha) throw new Error("Cloud image revision label does not equal source SHA");
    const cloudDigest = immutableImageDigest(cloudImage);
    const workspaceDigest = immutableImageDigest(workspaceImage);
    if (!cloudDigest || !workspaceDigest) throw new Error("qualified images must be immutable");

    const context = await runProcess("docker", ["context", "inspect", "--format", "{{(index .Endpoints \"docker\").Host}}"]);
    const dockerHost = context.stdout.trim();
    const dockerSocket = dockerHost.startsWith("unix://") ? dockerHost.slice("unix://".length) : "/var/run/docker.sock";
    const secrets = Array.from({ length: 10 }, () => randomBytes(32).toString("hex"));
    const envEntries = [
      ["OPL_CLOUD_IMAGE", cloudImage],
      ["OPL_WORKSPACE_IMAGE", workspaceImage],
      ["OPL_QUALIFICATION_SOURCE_SHA", options.sourceSha],
      ["OPL_BIND_ADDRESS", "127.0.0.1"],
      ["OPL_HTTP_PORT", publicPort],
      ["OPL_PUBLIC_URL", `http://127.0.0.1:${publicPort}`],
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
      ["OPL_SUB2API_BASE_URL", options.authorityMode === "live" ? String(process.env.OPL_SUB2API_BASE_URL || "") : "http://sub2api-authority:8080"],
      ["OPL_SUB2API_ADMIN_EMAIL", options.authorityMode === "live" ? String(process.env.OPL_SUB2API_ADMIN_EMAIL || "") : adminEmail],
      ["OPL_SUB2API_ADMIN_PASSWORD", options.authorityMode === "live" ? String(process.env.OPL_SUB2API_ADMIN_PASSWORD || "") : adminPassword],
      ["OPL_QUALIFICATION_USER_EMAIL", adminEmail],
      ["OPL_QUALIFICATION_USER_PASSWORD", adminPassword],
      ["OPL_QUALIFICATION_USER_TOKEN", userToken],
      ["OPL_QUALIFICATION_AUTHORITY_TOKEN", authorityToken],
      ["OPL_QUALIFICATION_AUTHORITY_HOST_PORT", authorityPort],
      ["OPL_QUALIFICATION_INITIAL_USD_MICROS", "1000000000"],
      ["OPL_DOCKER_SOCKET_PATH", dockerSocket],
      ["OPL_FABRIC_LOCAL_DOCKER_SECRET_ROOT", fabricSecretRoot],
      ["OPL_FABRIC_LOCAL_DOCKER_GATEWAY_CONTAINER", `${project}-control-plane-1`],
      ["OPL_SUB2API_REQUEST_TIMEOUT_MS", "5000"],
      ["OPL_MONTHLY_BILLING_WORKER_ENABLED", "0"],
      ["OPL_WORKSPACE_LAUNCH_WORKER_ENABLED", "1"],
      ["OPL_FABRIC_LOCAL_DOCKER_TRUSTED_WORKSPACE_IMAGES", workspaceImage],
      ["OPL_FABRIC_LOCAL_DOCKER_HOST", "127.0.0.1"]
    ];
    composeEnvironment = qualificationComposeEnvironment(process.env, envEntries);
    await writeFile(envFile, `${envEntries.map(([key, value]) => `${key}=${value}`).join("\n")}\n`, { mode: 0o600 });
    await compose(["config", "--quiet"]);

    stage = "compose_start";
    composeStarted = true;
    await waitForCompose(compose);
    const [controlPlaneContainer, fabricContainer, ledgerContainer] = await Promise.all([
      inspectComposeImage(compose, "control-plane"),
      inspectComposeImage(compose, "fabric"),
      inspectComposeImage(compose, "ledger")
    ]);
    if ([controlPlaneContainer, fabricContainer, ledgerContainer].some((container) => container.Image !== cloudInspection.Id)) {
      throw new Error("one or more Cloud services did not run the admitted image");
    }
    const stores = await verifyStores(compose);
    const http = createHTTP(`http://127.0.0.1:${publicPort}`);

    stage = "console_and_login";
    await consoleReadback(http);
    auth = await login(http, adminEmail, adminPassword);
    const me = sourceData((await http.json("/api/auth/me", {}, auth)).payload, "sub2api");
    const sub2apiUserId = String(me?.sub2apiUserId || "");
    if (me?.accountId !== accountId || !/^[1-9][0-9]*$/.test(sub2apiUserId) || me?.status !== "active") {
      throw new Error("qualification authority identity binding is invalid");
    }
    const walletBefore = sourceData((await http.json("/api/gateway/wallet", {}, auth)).payload, "sub2api");
    const beforeMicros = String(walletBefore?.usdMicros || "");
    if (!/^[1-9][0-9]*$/.test(beforeMicros)) throw new Error("qualification wallet readback is invalid");

    stage = "workspace_launch";
    const pricing = (await http.json("/api/pricing/preview", {
      method: "POST", body: { resourceType: "workspace", packageId: "basic", sizeGb: 10 }
    }, auth)).payload;
    const amountUsdMicros = String(pricing?.totalChargeUsdMicros || "");
    if (!/^[1-9][0-9]*$/.test(amountUsdMicros) || BigInt(beforeMicros) < BigInt(amountUsdMicros)) {
      throw new Error("qualification quote is invalid or wallet is insufficient");
    }
    const initial = (await http.json("/api/workspace-launches", {
      method: "POST", headers: { "idempotency-key": launchKey },
      body: { name: `Local qualification ${suffix}`, packageId: "basic", sizeGb: 10, autoRenew: false }
    }, auth, [202])).payload;
    if (initial?.operationId !== operationId || initial?.workspaceId !== workspaceId) throw new Error("deterministic launch identity is invalid");
    const launch = await waitForLaunch(http, operationId, auth);
    const receiptId = String(launch.receiptId || "");
    if (!receiptId) throw new Error("terminal launch receipt identity is missing");

    stage = "terminal_readback";
    const evidence = await readWorkspaceEvidence(http, auth, operationId, workspaceId, receiptId);
    const opened = await http.request(`/w/${encodeURIComponent(workspaceId)}/`, { redirect: "follow" });
    if (!opened.response.ok || !opened.text.includes("OPL Workspace READY")) throw new Error("Workspace Runtime open failed");
    const runtimeContainer = await runtimeImageReadback(accountId, workspaceId, workspaceImage, workspaceInspection.Id);
    const usage = sourceData((await http.json("/api/gateway/usage-summary?period=month", {}, auth)).payload, "sub2api");
    const walletAfterCharge = sourceData((await http.json("/api/gateway/wallet", {}, auth)).payload, "sub2api");
    const chargedMicros = String(walletAfterCharge?.usdMicros || "");
    if (!usage || typeof usage.totalRequests !== "number") throw new Error("Sub2API usage readback is invalid");
    const authorityBeforeDelete = options.authorityMode === "fixture" ? await authorityState(authorityPort, authorityToken) : null;
    const debits = authorityBeforeDelete?.adjustments?.filter((candidate) => candidate?.kind === "debit") || [];
    const liveDebit = options.authorityMode === "live" ? await liveAuthorityAdjustmentReadback(
      process.env.OPL_SUB2API_BASE_URL, process.env.OPL_SUB2API_ADMIN_EMAIL, process.env.OPL_SUB2API_ADMIN_PASSWORD,
      sub2apiUserId, evidence.receipt.chargeReference, `-${amountUsdMicros}`
    ) : null;
    const debit = options.authorityMode === "fixture" ? debits.find((candidate) => candidate?.code === evidence.receipt.chargeReference) : {
      code: liveDebit.code, userId: liveDebit.userId, amountUsdMicros, count: liveDebit.count
    };
	const debitCount = options.authorityMode === "fixture" ? debits.filter((candidate) => candidate?.code === evidence.receipt.chargeReference).length : liveDebit.count;
    const afterMicros = options.authorityMode === "fixture" ? String(authorityBeforeDelete.wallet?.usdMicros || "") : chargedMicros;
    if (options.authorityMode === "fixture" && (debits.length !== 1 || authorityBeforeDelete.writeCounts?.debits !== 1 || !debit || String(debit.userId) !== "41" || String(debit.amountUsdMicros) !== amountUsdMicros)) {
      throw new Error("qualification authority exact debit evidence is invalid");
    }
    if (!/^\d+$/.test(afterMicros) || options.authorityMode === "fixture" && BigInt(beforeMicros) - BigInt(amountUsdMicros) !== BigInt(afterMicros)) {
      throw new Error("qualification wallet debit snapshot is invalid");
    }
    const receiptsPage = sourceData((await http.json("/api/billing/receipts?limit=50", {}, auth)).payload, "ledger");
    const receipts = (receiptsPage?.receipts || []).filter((candidate) => candidate?.type === "billing.workspace_purchased.v1");
    const receipt = evidence.receipt;
    if (receipts.length !== 1 || receipt.chargeReference !== debit.code || String(receipt.totalUsdMicros) !== amountUsdMicros ||
      receipt.fulfillment?.runtimeId !== evidence.runtime.runtimeId || String(receipt.fulfillment?.workspaceApiKeyId || "") !== String(launch.workspaceApiKeyId || "")) {
      throw new Error("Ledger receipt exact binding is invalid");
    }

    stage = "restart_continuity";
    await compose(["restart", "control-plane", "fabric", "ledger"]);
    await waitForCompose(compose);
    const restartedAuth = await login(http, adminEmail, adminPassword);
    const afterRestart = await readWorkspaceEvidence(http, restartedAuth, operationId, workspaceId, receiptId);
    const restart = {
      performed: true,
      operationStable: afterRestart.launch.operationId === launch.operationId,
      workspaceStable: afterRestart.workspace.id === evidence.workspace.id,
      runtimeStable: afterRestart.runtime.runtimeId === evidence.runtime.runtimeId,
      receiptStable: afterRestart.receipt.receiptId === receipt.receiptId
    };
    if (Object.values(restart).some((value) => value !== true)) throw new Error("restart continuity changed an exact identity");

    stage = "owner_delete";
    const expectedDeleteOperationId = `workspace-delete-${stableID("workspace.delete.v1", workspaceId).slice(0, 18)}`;
    const deletion = (await http.json(`/api/workspaces/${encodeURIComponent(workspaceId)}`, {
      method: "DELETE", headers: { "idempotency-key": `local-qualification-delete:${workspaceId}` }, body: {}
    }, restartedAuth)).payload;
    if (deletion?.status !== "deleted" || deletion?.accountId !== accountId || String(deletion?.sub2apiUserId) !== String(sub2apiUserId) ||
      deletion?.launchOperationId !== operationId || deletion?.operationId !== expectedDeleteOperationId || deletion?.refundOperationId !== expectedDeleteOperationId ||
      deletion?.workspaceId !== workspaceId || deletion?.runtimeId !== evidence.runtime.runtimeId || String(deletion?.workspaceApiKeyId) !== String(launch.workspaceApiKeyId) ||
      deletion?.debitCode !== debit.code || deletion?.purchaseReceiptId !== receiptId || !String(deletion?.refundCode || "").startsWith("opl:") ||
      !String(deletion?.refundReceiptId || "").trim() || String(deletion?.totalUsdMicros) !== amountUsdMicros ||
      deletion?.runtimeStatus !== "absent" || deletion?.secretStatus !== "absent" || deletion?.keyStatus !== "absent" || deletion?.refundStatus !== "used") {
      throw new Error("owner-authorized Workspace DELETE terminal evidence is invalid");
    }
    const afterDeletePage = sourceData((await http.json("/api/workspaces?page=1&pageSize=20", {}, restartedAuth)).payload, "control-plane");
    const workspaceAbsent = !(afterDeletePage?.items || []).some((candidate) => candidate?.id === workspaceId);
    const runtimeAfterDelete = await http.request(`/api/workspaces/${encodeURIComponent(workspaceId)}/runtime-status`, {}, restartedAuth);
    const runtimeAbsent = runtimeAfterDelete.response.status === 404;
    const keyAfterDelete = await http.request(`/api/gateway/keys/${encodeURIComponent(launch.workspaceApiKeyId)}`, {}, restartedAuth);
    residuals = await residualCounts(accountId, workspaceId);
    const authorityAfterOwnerDelete = options.authorityMode === "fixture" ? await authorityState(authorityPort, authorityToken) : null;
    const workspaceKeyAbsent = keyAfterDelete.response.status === 404 && (options.authorityMode !== "fixture" ||
      !(authorityAfterOwnerDelete?.keys || []).some((candidate) => String(candidate?.id) === String(launch.workspaceApiKeyId)));
    const fabricSecretAbsent = deletion.secretStatus === "absent";
    if (!workspaceAbsent || !runtimeAbsent || !workspaceKeyAbsent || !fabricSecretAbsent || Object.values(residuals).some((count) => count !== 0)) {
      throw new Error("owner DELETE did not prove Workspace, Key, Runtime, and exact-labelled Docker cleanup");
    }
    const fixtureRefunds = authorityAfterOwnerDelete?.adjustments?.filter((candidate) => candidate?.kind === "refund" && candidate?.code === deletion.refundCode) || [];
    const liveRefund = options.authorityMode === "live" ? await liveAuthorityAdjustmentReadback(
      process.env.OPL_SUB2API_BASE_URL, process.env.OPL_SUB2API_ADMIN_EMAIL, process.env.OPL_SUB2API_ADMIN_PASSWORD,
      sub2apiUserId, deletion.refundCode, amountUsdMicros
    ) : null;
    const refund = options.authorityMode === "fixture" ? fixtureRefunds[0] : {
      code: liveRefund.code, userId: liveRefund.userId, amountUsdMicros, status: liveRefund.status
    };
    const refundCount = options.authorityMode === "fixture" ? fixtureRefunds.length : liveRefund.count;
    if (refundCount !== 1 || !refund || refund.code !== deletion.refundCode || String(refund.userId) !== String(sub2apiUserId) ||
      String(refund.amountUsdMicros) !== amountUsdMicros || refund.status !== "used" ||
      options.authorityMode === "fixture" && authorityAfterOwnerDelete?.writeCounts?.refunds !== 1) {
      throw new Error("qualification authority exact refund evidence is invalid");
    }
    const refundReceipt = sourceData((await http.json(`/api/billing/receipts/${encodeURIComponent(deletion.refundReceiptId)}`, {}, restartedAuth)).payload, "ledger");
    if (refundReceipt?.receiptId !== deletion.refundReceiptId || refundReceipt?.type !== "billing.workspace_refunded.v1" || refundReceipt?.status !== "completed" ||
      refundReceipt?.accountId !== accountId || refundReceipt?.operationId !== expectedDeleteOperationId || refundReceipt?.workspaceId !== workspaceId ||
      refundReceipt?.chargeReference !== debit.code || refundReceipt?.refundCode !== deletion.refundCode ||
      String(refundReceipt?.refundUsdMicros) !== amountUsdMicros || String(refundReceipt?.totalUsdMicros) !== amountUsdMicros) {
      throw new Error("Ledger refund Receipt exact binding is invalid");
    }
    const walletAfterDelete = options.authorityMode === "fixture" ? authorityAfterOwnerDelete?.wallet :
      sourceData((await http.json("/api/gateway/wallet", {}, restartedAuth)).payload, "sub2api");
    const restoredMicros = String(walletAfterDelete?.usdMicros || "");
    if (!/^\d+$/.test(restoredMicros) || options.authorityMode === "fixture" && restoredMicros !== beforeMicros) {
      throw new Error("qualification wallet refund snapshot is invalid");
    }

    validateQualificationSourceIdentity(sourceBefore, await readQualificationSourceIdentity(), options.sourceSha);
    finalReceipt = validateLocalQualificationReceipt({
      schemaVersion: 1,
      status: "READY",
      startedAt,
      completedAt: new Date().toISOString(),
      source: { sha: options.sourceSha, tree: sourceTree },
      images: {
        cloud: { input: cloudImage, repoDigest: cloudImage, digest: cloudDigest, runningDigest: cloudInspection.Id },
        workspace: { input: workspaceImage, repoDigest: workspaceImage, digest: workspaceDigest, runningDigest: runtimeContainer.Image }
      },
      command: receiptCommand({ ...options, cloudImage, workspaceImage }),
      processes: { console: "ready", controlPlane: "ready", fabric: "ready", ledger: "ready" },
      stores,
      identities: {
        accountId, sub2apiUserId, launchOperationId: operationId, deleteOperationId: String(deletion.operationId || ""),
        refundOperationId: String(deletion.refundOperationId || ""), workspaceId,
        runtimeId: evidence.runtime.runtimeId, keyId: String(launch.workspaceApiKeyId), debitCode: debit.code,
        purchaseReceiptId: receiptId, refundReceiptId: String(deletion.refundReceiptId || "")
      },
      debit: { count: debitCount, accountId, operationId, workspaceId, code: debit.code, userId: String(debit.userId), amountUsdMicros },
      wallet: { beforeUsdMicros: beforeMicros, afterUsdMicros: afterMicros, restoredUsdMicros: restoredMicros },
      receipt: {
        count: 1, id: receipt.receiptId, accountId, operationId, workspaceId: receipt.workspaceId,
        runtimeId: receipt.fulfillment.runtimeId, keyId: String(receipt.fulfillment.workspaceApiKeyId),
        chargeReference: receipt.chargeReference, amountUsdMicros: String(receipt.totalUsdMicros)
      },
      restart,
      deletion: {
        ownerAuthorized: true, accountId, operationId: String(deletion.operationId || ""), refundOperationId: String(deletion.refundOperationId || ""), workspaceId,
        runtimeId: evidence.runtime.runtimeId, keyId: String(launch.workspaceApiKeyId),
        workspaceAbsent, runtimeAbsent, workspaceKeyAbsent, fabricSecretAbsent
      },
      residuals,
      authorityWriteCounts: authorityAfterOwnerDelete?.writeCounts,
      mutationCounts: { workspaceLaunchPosts: 1, workspaceDeleteRequests: 1, refundPosts: 0 },
      refund: {
        count: refundCount, accountId, operationId: deletion.refundOperationId, workspaceId, debitCode: debit.code,
        code: refund.code, userId: String(refund.userId), amountUsdMicros,
        receiptId: deletion.refundReceiptId
      },
      refundReceipt: {
        count: 1, id: refundReceipt.receiptId, type: refundReceipt.type,
        accountId: refundReceipt.accountId, operationId: refundReceipt.operationId, workspaceId: refundReceipt.workspaceId,
        chargeReference: refundReceipt.chargeReference, refundCode: refundReceipt.refundCode, amountUsdMicros: String(refundReceipt.refundUsdMicros)
      },
      usage: { source: "sub2api", status: "available", totalRequests: usage.totalRequests },
      productMatrix,
      qualification: { authorityMode: options.authorityMode, p0Ready: options.authorityMode === "live" && productMatrix?.zeroSkip === true },
      deferred: [...deferredCloudGates]
    });
  } catch (error) {
    failure = error;
  } finally {
    if (composeStarted) {
      const stop = await compose(["stop", "--timeout", "30", "control-plane", "fabric"], { allowFailure: true });
      const down = await compose(["down", "--volumes", "--remove-orphans"], { allowFailure: true });
      const observed = await residualCounts(accountId, workspaceId).catch(() => null);
      if (stop.code !== 0 || !observed || down.code !== 0) failure ||= new Error("local qualification teardown was not confirmed");
      if (observed) residuals = observed;
    }
    for (const tag of builtTags) await runProcess("docker", ["image", "rm", tag], { allowFailure: true });
    if (registryContainer) {
      const removed = await runProcess("docker", ["rm", "-f", registryContainer], { allowFailure: true });
      if (removed.code !== 0) failure ||= new Error("local source registry cleanup was not confirmed");
    }
    await rm(tempRoot, { recursive: true, force: true });
  }

  if (failure) {
    const notReady = {
      schemaVersion: 1,
      status: "NOT_READY",
      startedAt,
      completedAt: new Date().toISOString(),
      source: { sha: options.sourceSha, tree: sourceTree },
      images: {
        cloud: { input: cloudImage || "unavailable", digest: immutableImageDigest(cloudImage) || "unavailable" },
        workspace: { input: workspaceImage || "unavailable", digest: immutableImageDigest(workspaceImage) || "unavailable" }
      },
      command: receiptCommand({ ...options, cloudImage, workspaceImage }),
      stage,
      errorCode: "local_workspace_qualification_failed",
      error: redactedError(failure),
      residuals,
      deferred: [...deferredCloudGates]
    };
    await writeJSONAtomic(options.receiptPath, notReady);
    throw failure;
  }
  await writeJSONAtomic(options.receiptPath, finalReceipt);
  return finalReceipt;
}

async function main() {
  let options;
  try {
    options = parseLocalQualificationArgs();
    const receipt = await runLocalWorkspaceQualification(options);
    process.stdout.write(`${JSON.stringify(receipt, null, 2)}\n`);
  } catch (error) {
    if (!options) {
      const rawPathIndex = process.argv.indexOf("--receipt");
      const rawPath = rawPathIndex >= 0 ? process.argv[rawPathIndex + 1] : "";
      if (rawPath) {
        await writeJSONAtomic(rawPath, {
          schemaVersion: 1, status: "NOT_READY", stage: "input_validation",
          errorCode: "local_workspace_qualification_input_invalid",
          error: redactedError(error),
          deferred: [...deferredCloudGates]
        });
      }
    }
    console.error(redactedError(error));
    process.exitCode = 1;
  }
}

if (process.argv[1] && pathToFileURL(resolve(process.argv[1])).href === import.meta.url) {
  await main();
}
