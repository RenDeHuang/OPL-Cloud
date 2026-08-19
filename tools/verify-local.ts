import { spawn } from "node:child_process";
import { randomUUID } from "node:crypto";
import { existsSync } from "node:fs";
import { mkdir, rename, writeFile } from "node:fs/promises";
import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, isAbsolute, join, relative, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { parse as parseYAML } from "yaml";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");

function composePostgresImage() {
  const compose = parseYAML(readFileSync(join(root, "compose.yaml"), "utf8"));
  const image = String(compose?.services?.postgres?.image || "").trim();
  if (!/^postgres:[^\s@]+@sha256:[0-9a-f]{64}$/.test(image)) {
    throw new Error("compose.yaml services.postgres.image must be an exact postgres tag@sha256 reference");
  }
  return image;
}

export const postgresImage = composePostgresImage();

export const goModules = Object.freeze([
  "services/control-plane",
  "services/fabric",
  "services/ledger",
  "services/internal/postgresmigrate"
]);

export const databaseFreeGoTestSpecs = Object.freeze([
  { cwd: "services/control-plane", packages: ["./cmd/control-plane", "./internal/clients"] },
  { cwd: "services/fabric", packages: ["./cmd/fabric", "./cmd/opl-tencent-provisioner", "./internal/http", "./internal/protectedresource"] },
  { cwd: "services/ledger", packages: ["./cmd/ledger", "./internal/http"] },
  { cwd: "services/internal/postgresmigrate", run: "^TestValidateTLS", packages: ["./..."] }
]);

export const localVerificationSteps = Object.freeze([
  { name: "product boundary", command: "npm", args: ["run", "validate:product-boundary"] },
  { name: "Node source tests", command: "npm", args: ["run", "test:source"] },
  { name: "TypeScript typecheck", command: "npm", args: ["run", "typecheck"] },
  { name: "TypeScript lint", command: "npm", args: ["run", "lint"] },
  { name: "Console build", command: "npm", args: ["run", "build"] },
  ...goModules.map((cwd) =>
    ({ name: `${cwd} compile`, command: "go", args: ["test", "-run", "^$", "./..."], cwd })
  ),
  ...databaseFreeGoTestSpecs.map((spec) => ({
    name: `${spec.cwd} database-free tests`,
    command: "go",
    args: ["test", "-count=1", ...(spec.run ? ["-run", spec.run] : []), ...spec.packages],
    cwd: spec.cwd
  })),
  { name: "Git whitespace", command: "git", args: ["diff", "--check"] }
]);

export const postgresVerificationSpecs = Object.freeze([
  { cwd: "services/internal/postgresmigrate", race: true },
  { cwd: "services/ledger" },
  { cwd: "services/control-plane", timeout: "15m" },
  { cwd: "services/fabric" }
]);

export const productMatrixModuleSpecs = Object.freeze([
  { cwd: "services/internal/postgresmigrate", command: "go", argsPrefix: ["test", "-race", "-count=1", "-json"] },
  { cwd: "services/ledger", command: "go", argsPrefix: ["test", "-count=1", "-json"] },
  { cwd: "services/control-plane", command: "go", argsPrefix: ["test", "-timeout=15m", "-count=1", "-json"] },
  { cwd: "services/fabric", command: "go", argsPrefix: ["test", "-count=1", "-json"] }
]);

export const productMatrixStages = Object.freeze([
  "key", "debit", "ensure_compute_allocation", "storage", "attachment", "secret", "runtime", "activation", "receipt"
]);

export const productMatrixRequiredPackages = Object.freeze([
  "opl-cloud/services/control-plane/internal/server",
  "opl-cloud/services/control-plane/internal/clients",
  "opl-cloud/services/fabric/internal/fabric",
  "opl-cloud/services/internal/postgresmigrate",
  "opl-cloud/services/fabric/internal/http",
  "opl-cloud/services/ledger/internal/http",
  "opl-cloud/services/ledger/internal/ledger"
]);

const controlPlaneServerPackage = productMatrixRequiredPackages[0];
const controlPlaneClientsPackage = productMatrixRequiredPackages[1];
const fabricPackage = productMatrixRequiredPackages[2];
const postgresMigratePackage = productMatrixRequiredPackages[3];
const fabricHTTPPackage = productMatrixRequiredPackages[4];
const ledgerHTTPPackage = productMatrixRequiredPackages[5];
const ledgerPackage = productMatrixRequiredPackages[6];
export const productMatrixRequiredTests = Object.freeze([
  "TestWorkspaceLaunchReservedStageReplayMatrix",
  "TestWorkspaceLaunchReservedStageReplayRefusesUncertainAuthority",
  "TestWorkspaceLaunchReservedStageReplayRefusesStateAndAuthorizationDrift",
  "TestWorkspaceLaunchReservedStageReplayCASAllowsOneWriter",
  "TestWorkspaceLaunchReservedStageReplaySurvivesCrashBeforeTransportSend",
  "TestWorkspaceLaunchReservedStageReplayPostReadMatrix",
  "TestWorkspaceLaunchPendingReadbackIsBoundedAndCanConvergeReadOnly",
  "TestWorkspaceLaunchRecoveryAtEveryStageContinuesOriginalOperationToSucceeded",
  "TestWorkspaceLaunchReceiptOnlyReplayReachesTerminalWithoutRepeatingPriorStages",
  "TestPostgresWorkspaceLaunchReplayClaimSurvivesReconcilerRestartWithoutSkip",
  "TestPostgresWorkspaceLaunchConcurrentReplayResumeAllowsOneWriter",
  "TestWorkspaceLaunchResumeRouteWaitsForOriginalCallerCredential"
].map((name) => Object.freeze({ package: controlPlaneServerPackage, name })).concat([
  Object.freeze({ package: controlPlaneClientsPackage, name: "TestSub2APIFinancialBalanceHistoryByCodesReadsAuthoritativeFinalPageAfterTarget" }),
  Object.freeze({ package: controlPlaneClientsPackage, name: "TestSub2APIFinancialBalanceHistoryByCodesRejectsDuplicateOnLaterPage" }),
  Object.freeze({ package: fabricPackage, name: "TestWorkspaceLaunchStageReadContextRejectsProviderMutation" }),
  Object.freeze({ package: fabricPackage, name: "TestTencentWorkspaceLaunchComputeReadIsGETOnlyBeforeSameOperationOwnershipRecovery" }),
  Object.freeze({ package: fabricPackage, name: "TestTencentWorkspaceLaunchComputeReadMissingOwnershipFailsClosedOnAuthoritativeConflictOrError" }),
  Object.freeze({ package: fabricPackage, name: "TestQualificationWorkspaceDockerfileUsesExactImageReferences" }),
  Object.freeze({ package: fabricPackage, name: "TestLocalDockerWorkspaceCorePath" }),
  Object.freeze({ package: fabricPackage, name: "TestLocalDockerDestroyWorkspaceRuntimeDeletesExactSecretAndPreservesSibling" }),
  Object.freeze({ package: fabricPackage, name: "TestLocalDockerWorkspaceRuntimeStatusReturnsTypedAbsence" }),
  Object.freeze({ package: fabricPackage, name: "TestLocalDockerRuntimeStatusFailsClosedOnSecretIdentityOrMountDrift" }),
  Object.freeze({ package: postgresMigratePackage, name: "TestApplyRunsMigrationOnlyOnce" }),
  Object.freeze({ package: postgresMigratePackage, name: "TestApplySerializesConcurrentStartup" }),
  Object.freeze({ package: postgresMigratePackage, name: "TestApplyDoesNotRecordFailedMigration" }),
  Object.freeze({ package: fabricHTTPPackage, name: "TestWorkspaceLaunchTypedEnsureRequiresExactHeaderAndReturnsNeutralDTO" }),
  Object.freeze({ package: fabricHTTPPackage, name: "TestWorkspaceLaunchEnsureCapabilityUsesFabricOperationOwnerIdentity" }),
  Object.freeze({ package: fabricHTTPPackage, name: "TestServerDestroysWorkspaceRuntime" }),
  Object.freeze({ package: fabricHTTPPackage, name: "TestServerReturnsTypedWorkspaceOwnerObservations" }),
  Object.freeze({ package: ledgerHTTPPackage, name: "TestLedgerReceiptReadCapabilityAcceptsExactAccountAndOptionalWorkspace" }),
  Object.freeze({ package: ledgerPackage, name: "TestPostgresStoreRunsEmbeddedMigrationsOnce" })
]));

export function parseVerifyLocalArgs(args = process.argv.slice(2)) {
  let withPostgres = false;
  let productMatrixReceipt = "";
  for (let index = 0; index < args.length; index += 1) {
    const token = args[index];
    if (token === "--with-postgres") {
      if (withPostgres) throw new Error("verify-local argument --with-postgres may be provided once");
      withPostgres = true;
      continue;
    }
    if (token === "--product-matrix-receipt") {
      const value = args[index + 1];
      if (!value || value.startsWith("--") || productMatrixReceipt) {
        throw new Error("verify-local argument --product-matrix-receipt requires one value");
      }
      if (!isAbsolute(value)) throw new Error("Product matrix receipt must use an absolute path");
      const withinRoot = relative(root, resolve(value));
      if (withinRoot === "" || (!withinRoot.startsWith("..") && !isAbsolute(withinRoot))) {
        throw new Error("Product matrix receipt must be written outside the source repository");
      }
      productMatrixReceipt = resolve(value);
      index += 1;
      continue;
    }
    throw new Error(`unknown verify-local argument: ${token}`);
  }
  if (productMatrixReceipt && !withPostgres) {
    throw new Error("Product matrix receipt requires --with-postgres");
  }
  return { withPostgres, productMatrixReceipt };
}

function stepCwd(step) {
  return step.cwd ? join(root, step.cwd) : root;
}

function stepEnv() {
  return process.env;
}

function printStep(name) {
  process.stdout.write(`\n==> ${name}\n`);
}

function runProcess(command, args, { cwd = root, env = process.env, capture = false, allowFailure = false } = {}) {
  return new Promise((resolvePromise, reject) => {
    const child = spawn(command, args, {
      cwd,
      env,
      stdio: capture ? ["ignore", "pipe", "pipe"] : "inherit"
    });
    let stdout = "";
    let stderr = "";
    if (capture) {
      child.stdout.setEncoding("utf8");
      child.stderr.setEncoding("utf8");
      child.stdout.on("data", (chunk) => { stdout += chunk; });
      child.stderr.on("data", (chunk) => { stderr += chunk; });
    }
    child.on("error", reject);
    child.on("close", (code, signal) => {
      if (code === 0 || allowFailure) {
        resolvePromise({ code, signal, stdout, stderr });
        return;
      }
      const detail = capture ? `\n${stderr || stdout}` : "";
      reject(new Error(`${command} ${args.join(" ")} failed with ${signal || code}${detail}`));
    });
  });
}

async function runStep(step) {
  printStep(step.name);
  await runProcess(step.command, step.args, { cwd: stepCwd(step), env: stepEnv() });
}

function parseDockerPort(output) {
  for (const line of String(output).trim().split(/\r?\n/)) {
    const match = line.match(/:(\d+)$/);
    if (match) return match[1];
  }
  throw new Error(`could not parse PostgreSQL Docker port: ${String(output).trim()}`);
}

async function waitForHealthyPostgres(containerName) {
  const deadline = Date.now() + 60_000;
  while (Date.now() < deadline) {
    const result = await runProcess(
      "docker",
      ["inspect", "--format", "{{.State.Health.Status}}", containerName],
      { capture: true, allowFailure: true }
    );
    const status = result.stdout.trim();
    if (result.code === 0 && status === "healthy") return;
    if (result.code !== 0 || status === "unhealthy") {
      throw new Error(`temporary PostgreSQL container is ${status || "unavailable"}: ${result.stderr.trim()}`);
    }
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 500));
  }
  throw new Error("temporary PostgreSQL did not become healthy within 60 seconds");
}

async function withTemporaryPostgres(callback) {
  const containerName = `opl-cloud-verify-${process.pid}-${randomUUID().slice(0, 8)}`;
  let started = false;
  const stop = async () => {
    if (!started) return;
    await runProcess("docker", ["rm", "--force", containerName], { capture: true, allowFailure: true });
    started = false;
  };
  const interrupt = () => {
    void stop().finally(() => process.exit(130));
  };
  process.once("SIGINT", interrupt);
  process.once("SIGTERM", interrupt);
  try {
    printStep("temporary PostgreSQL 16");
    await runProcess("docker", [
      "run", "--detach", "--rm", "--name", containerName,
      "--env", "POSTGRES_HOST_AUTH_METHOD=trust",
      "--health-cmd", "pg_isready -U postgres -d postgres",
      "--health-interval", "1s",
      "--health-timeout", "5s",
      "--health-retries", "60",
      "--publish", "127.0.0.1::5432",
      postgresImage
    ]);
    started = true;
    await waitForHealthyPostgres(containerName);
    const portResult = await runProcess("docker", ["port", containerName, "5432/tcp"], { capture: true });
    const postgresEnv = {
      ...process.env,
      PGHOST: "127.0.0.1",
      PGPORT: parseDockerPort(portResult.stdout),
      PGUSER: "postgres",
      PGDATABASE: "postgres",
      PGSSLMODE: "disable",
      OPL_POSTGRES_TESTS: "1",
      OPL_CAPACITY_TESTS: "1",
      OPL_FABRIC_LOCAL_DOCKER_INTEGRATION: "1"
    };
    return await callback(postgresEnv);
  } finally {
    process.removeListener("SIGINT", interrupt);
    process.removeListener("SIGTERM", interrupt);
    await stop();
  }
}

export function summarizeGoTestFailures(events) {
  const failedTests = new Map();
  const failedPackages = new Set();
  for (const event of events) {
    if (event?.Action !== "fail" || typeof event.Package !== "string" || !event.Package) continue;
    if (typeof event.Test === "string" && event.Test) {
      failedTests.set(`${event.Package}\0${event.Test}`, { package: event.Package, name: event.Test });
    } else {
      failedPackages.add(event.Package);
    }
  }
  const values = [...failedTests.values()];
  const tests = values.filter((candidate) => !values.some((other) =>
    other.package === candidate.package && other.name.startsWith(`${candidate.name}/`)
  )).sort((left, right) => left.package.localeCompare(right.package) || left.name.localeCompare(right.name));
  return { tests, packages: [...failedPackages].sort() };
}

function runGoJSONWithoutSkips(args, { cwd, env }) {
  return new Promise((resolvePromise, reject) => {
    const child = spawn("go", args, { cwd, env, stdio: ["ignore", "pipe", "inherit"] });
    let pending = "";
    const failureEvents = [];
    let skipped = 0;
    let outputLog = "";
    let parseError;
    const passedPackages = new Set();
    const passedTests = new Map();
    const consume = (line) => {
      if (!line.trim()) return;
      try {
        const event = JSON.parse(line);
        if (event.Action === "fail") failureEvents.push(event);
        if (event.Action === "skip") skipped += 1;
        if (event.Action === "pass" && !event.Test && event.Package) {
          passedPackages.add(event.Package);
        }
        if (event.Action === "pass" && event.Test && event.Package) {
          passedTests.set(`${event.Package}\0${event.Test}`, { package: event.Package, name: event.Test });
        }
        if (event.Output && outputLog.length < 2_000_000) outputLog += event.Output;
      } catch (error) {
        parseError ||= error;
      }
    };
    child.stdout.setEncoding("utf8");
    child.stdout.on("data", (chunk) => {
      pending += chunk;
      const lines = pending.split(/\r?\n/);
      pending = lines.pop() || "";
      for (const line of lines) consume(line);
    });
    child.on("error", reject);
    child.on("close", (code, signal) => {
      consume(pending);
      const failures = summarizeGoTestFailures(failureEvents);
      const failed = failures.tests.length + failures.packages.length;
      if (parseError) {
        reject(new Error(`invalid go test JSON output: ${parseError.message}`));
      } else if (code !== 0 || failures.tests.length > 0 || failures.packages.length > 0 || skipped > 0) {
        if (outputLog) process.stderr.write(outputLog.slice(-2_000_000));
        const details = [
          failures.tests.length > 0 ? `tests=${failures.tests.map((entry) => `${entry.package}:${entry.name}`).join(",")}` : "",
          failures.packages.length > 0 ? `packages=${failures.packages.join(",")}` : ""
        ].filter(Boolean).join("; ");
        reject(new Error(`Go FAIL tests=${failures.tests.length} packages=${failures.packages.length} SKIP ${skipped}; process=${signal || code}${details ? `; ${details}` : ""}`));
      } else {
        process.stdout.write(`Go packages passed: ${passedPackages.size}; skipped: 0\n`);
        resolvePromise({
          failed,
          skipped,
          passedPackages: [...passedPackages].sort(),
          passedTests: [...passedTests.values()].sort((left, right) =>
            left.package.localeCompare(right.package) || left.name.localeCompare(right.name))
        });
      }
    });
  });
}

async function runPostgresVerification(env) {
  const verifyModule = async (spec) => {
    const cwd = join(root, spec.cwd);
    printStep(`${spec.cwd} PostgreSQL compile`);
    await runProcess("go", ["test", "-run", "^$", "./..."], { cwd, env });
    const packagesResult = await runProcess(
      "go",
      ["list", "-f", "{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}", "./..."],
      { cwd, env, capture: true }
    );
    const packages = packagesResult.stdout.split(/\r?\n/).map((item) => item.trim()).filter(Boolean).sort();
    if (packages.length === 0) throw new Error(`no Go test packages found under ${spec.cwd}`);
    const args = ["test"];
    if (spec.race) args.push("-race");
    if (spec.timeout) args.push(`-timeout=${spec.timeout}`);
    args.push("-count=1", "-json", ...packages);
    printStep(`${spec.cwd} PostgreSQL tests (zero skips)`);
    const result = await runGoJSONWithoutSkips(args, { cwd, env });
    return { cwd: spec.cwd, command: "go", args: [...args], packages: [...packages], ...result };
  };
  const settled = await Promise.allSettled(postgresVerificationSpecs.map((spec) => verifyModule(spec)));
  const failures = settled.flatMap((result, index) => result.status === "rejected"
    ? [`${postgresVerificationSpecs[index].cwd}: ${result.reason instanceof Error ? result.reason.message : String(result.reason)}`]
    : []);
  if (failures.length > 0) throw new Error(`PostgreSQL module verification failed:\n${failures.join("\n")}`);
  return settled.map((result) => result.value);
}

function packageBelongsToModule(packageName, cwd) {
  return packageName === `opl-cloud/${cwd}` || packageName.startsWith(`opl-cloud/${cwd}/`);
}

function sameStrings(left, right) {
  return Array.isArray(left) && left.length === right.length && left.every((value, index) => value === right[index]);
}

export function validateProductMatrixModules(results) {
  if (!Array.isArray(results) || results.length !== productMatrixModuleSpecs.length) {
    throw new Error("Product matrix receipt requires every module result");
  }
  const byCwd = new Map();
  for (const result of results) {
    if (!result?.cwd || byCwd.has(result.cwd)) throw new Error("Product matrix module identity is duplicated or missing");
    byCwd.set(result.cwd, result);
  }
  return productMatrixModuleSpecs.map((spec) => {
    const result = byCwd.get(spec.cwd);
    if (!result || result.command !== spec.command || result.failed !== 0 || result.skipped !== 0) {
      throw new Error(`Product matrix module ${spec.cwd} identity or zero-skip result is invalid`);
    }
    const packages = Array.isArray(result.packages) ? result.packages : [];
    if (packages.length === 0 || new Set(packages).size !== packages.length ||
      packages.some((name) => !packageBelongsToModule(name, spec.cwd))) {
      throw new Error(`Product matrix module ${spec.cwd} exact package list is invalid`);
    }
    const expectedArgs = [...spec.argsPrefix, ...packages];
    if (!sameStrings(result.args, expectedArgs)) {
      throw new Error(`Product matrix module ${spec.cwd} normalized command is invalid`);
    }
    const passedPackages = Array.isArray(result.passedPackages) ? result.passedPackages : [];
    if (passedPackages.some((name) => !packages.includes(name)) ||
      packages.some((name) => !passedPackages.includes(name))) {
      throw new Error(`Product matrix module ${spec.cwd} package pass evidence is invalid`);
    }
    const passedTests = Array.isArray(result.passedTests) ? result.passedTests : [];
    if (passedTests.some((entry) => !packageBelongsToModule(entry?.package, spec.cwd))) {
      throw new Error(`Product matrix module ${spec.cwd} test provenance is invalid`);
    }
    for (const entry of productMatrixRequiredTests.filter((candidate) => packageBelongsToModule(candidate.package, spec.cwd))) {
      if (!passedTests.some((candidate) => candidate.package === entry.package && candidate.name === entry.name)) {
        throw new Error(`Product matrix required test did not pass in module ${spec.cwd}: ${entry.package} ${entry.name}`);
      }
    }
    return {
      cwd: spec.cwd,
      command: spec.command,
      args: [...result.args],
      packages: [...packages],
      failed: 0,
      skipped: 0,
      passedPackages: [...passedPackages],
      passedTests: passedTests.map((entry) => ({ package: entry.package, name: entry.name }))
    };
  });
}

function exactSourceIdentity(value) {
  return value && /^[0-9a-f]{40}$/.test(value.sha) && /^[0-9a-f]{40}$/.test(value.tree) && value.clean === true;
}

export function buildProductMatrixReceipt(before, after, results, completedAt = new Date().toISOString()) {
  if (!exactSourceIdentity(before) || !exactSourceIdentity(after)) {
    throw new Error("Product matrix receipt requires a clean exact source identity");
  }
  if (before.sha !== after.sha || before.tree !== after.tree) {
    throw new Error("Product matrix source changed while the full gate was running");
  }
  const modules = validateProductMatrixModules(results);
  const passedPackages = new Set(modules.flatMap((result) => result.passedPackages || []));
  for (const packageName of productMatrixRequiredPackages) {
    if (!passedPackages.has(packageName)) throw new Error(`Product matrix required package did not pass: ${packageName}`);
  }
  const passedTests = new Set(modules.flatMap((result) =>
    (result.passedTests || []).map((entry) => `${entry.package}\0${entry.name}`)));
  for (const entry of productMatrixRequiredTests) {
    if (!passedTests.has(`${entry.package}\0${entry.name}`)) {
      throw new Error(`Product matrix required test did not pass: ${entry.package} ${entry.name}`);
    }
  }
  const stageEvidenceTests = [
    "TestWorkspaceLaunchReservedStageReplayMatrix",
    "TestWorkspaceLaunchReservedStageReplayPostReadMatrix",
    "TestWorkspaceLaunchRecoveryAtEveryStageContinuesOriginalOperationToSucceeded",
    "TestPostgresWorkspaceLaunchReplayClaimSurvivesReconcilerRestartWithoutSkip"
  ];
  return {
    schemaVersion: 1,
    status: "READY",
    completedAt,
    source: { sha: before.sha, tree: before.tree },
    zeroSkip: true,
    modules,
    packages: [...passedPackages].sort().map((name) => ({ name, passed: true, skipped: 0 })),
    tests: productMatrixRequiredTests.map((entry) => ({ ...entry, passed: true, skipped: 0 })),
    stages: productMatrixStages.map((name) => ({ name, passed: true, skipped: 0, evidenceTests: [...stageEvidenceTests] })),
    cas: {
      winnerCount: 1,
      loserMutationCount: 0,
      evidenceTests: [
        "TestWorkspaceLaunchReservedStageReplayCASAllowsOneWriter",
        "TestPostgresWorkspaceLaunchConcurrentReplayResumeAllowsOneWriter"
      ]
    },
    unknown: {
      authorityWriteDeltas: { controlPlane: 0, sub2api: 0, fabric: 0, ledger: 0 },
      evidenceTests: ["TestWorkspaceLaunchReservedStageReplayRefusesUncertainAuthority"]
    }
  };
}

async function readSourceIdentity() {
  const [sha, tree, status] = await Promise.all([
    runProcess("git", ["rev-parse", "HEAD"], { capture: true }),
    runProcess("git", ["rev-parse", "HEAD^{tree}"], { capture: true }),
    runProcess("git", ["status", "--porcelain", "--untracked-files=all"], { capture: true })
  ]);
  return { sha: sha.stdout.trim(), tree: tree.stdout.trim(), clean: status.stdout.trim() === "" };
}

async function writeProductMatrixReceipt(path, receipt) {
  const directory = dirname(path);
  await mkdir(directory, { recursive: true });
  const temporary = join(directory, `.${Date.now()}-${process.pid}-${randomUUID()}.tmp`);
  await writeFile(temporary, `${JSON.stringify(receipt, null, 2)}\n`, { mode: 0o600 });
  await rename(temporary, path);
}

const defaultDependencies = Object.freeze({
  runStep,
  withTemporaryPostgres,
  runPostgresVerification,
  readSourceIdentity,
  writeProductMatrixReceipt
});

export async function runVerification({ withPostgres = false } = {}, dependencies = defaultDependencies) {
  for (const step of localVerificationSteps) await dependencies.runStep(step);
  let postgresResults = [];
  if (withPostgres) {
    postgresResults = await dependencies.withTemporaryPostgres((env) => dependencies.runPostgresVerification(env));
  }
  return { postgresResults };
}

async function runVerificationWithReceipt(options, dependencies = defaultDependencies) {
  const before = options.productMatrixReceipt ? await dependencies.readSourceIdentity() : null;
  if (before && !exactSourceIdentity(before)) throw new Error("Product matrix receipt requires a clean exact source identity");
  const result = await runVerification(options, dependencies);
  if (!options.productMatrixReceipt) return result;
  const after = await dependencies.readSourceIdentity();
  const receipt = buildProductMatrixReceipt(before, after, result.postgresResults);
  await dependencies.writeProductMatrixReceipt(options.productMatrixReceipt, receipt);
  return { ...result, productMatrixReceipt: receipt };
}

async function main() {
  const options = parseVerifyLocalArgs();
  await runVerificationWithReceipt(options);
  process.stdout.write(`\nLocal verification passed${options.withPostgres ? " with PostgreSQL and Docker integration" : ""}.\n`);
}

if (process.argv[1] && pathToFileURL(resolve(process.argv[1])).href === import.meta.url) {
  main().catch((error) => {
    console.error(error instanceof Error ? error.message : error);
    process.exitCode = 1;
  });
}
