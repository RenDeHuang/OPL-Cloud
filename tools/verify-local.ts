import { spawn } from "node:child_process";
import { randomUUID } from "node:crypto";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");

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

export function parseVerifyLocalArgs(args = process.argv.slice(2)) {
  const unknown = args.filter((arg) => arg !== "--with-postgres");
  if (unknown.length > 0) {
    throw new Error(`unknown verify-local argument: ${unknown.join(", ")}`);
  }
  return { withPostgres: args.includes("--with-postgres") };
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
      "postgres:16"
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

function runGoJSONWithoutSkips(args, { cwd, env }) {
  return new Promise((resolvePromise, reject) => {
    const child = spawn("go", args, { cwd, env, stdio: ["ignore", "pipe", "inherit"] });
    let pending = "";
    let failed = 0;
    let skipped = 0;
    let passedPackages = 0;
    let outputLog = "";
    let parseError;
    const consume = (line) => {
      if (!line.trim()) return;
      try {
        const event = JSON.parse(line);
        if (event.Action === "fail") failed += 1;
        if (event.Action === "skip") skipped += 1;
        if (event.Action === "pass" && !event.Test) passedPackages += 1;
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
      if (parseError) {
        reject(new Error(`invalid go test JSON output: ${parseError.message}`));
      } else if (code !== 0 || failed > 0 || skipped > 0) {
        if (outputLog) process.stderr.write(outputLog.slice(-2_000_000));
        reject(new Error(`Go FAIL ${failed} SKIP ${skipped}; process=${signal || code}`));
      } else {
        process.stdout.write(`Go packages passed: ${passedPackages}; skipped: 0\n`);
        resolvePromise();
      }
    });
  });
}

async function runPostgresVerification(env) {
  for (const spec of postgresVerificationSpecs) {
    const cwd = join(root, spec.cwd);
    printStep(`${spec.cwd} PostgreSQL compile`);
    await runProcess("go", ["test", "-run", "^$", "./..."], { cwd, env });
    const packagesResult = await runProcess(
      "go",
      ["list", "-f", "{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}", "./..."],
      { cwd, env, capture: true }
    );
    const packages = packagesResult.stdout.split(/\r?\n/).map((item) => item.trim()).filter(Boolean);
    if (packages.length === 0) throw new Error(`no Go test packages found under ${spec.cwd}`);
    const args = ["test"];
    if (spec.race) args.push("-race");
    if (spec.timeout) args.push(`-timeout=${spec.timeout}`);
    args.push("-count=1", "-json", ...packages);
    printStep(`${spec.cwd} PostgreSQL tests (zero skips)`);
    await runGoJSONWithoutSkips(args, { cwd, env });
  }
}

const defaultDependencies = Object.freeze({
  runStep,
  withTemporaryPostgres,
  runPostgresVerification
});

export async function runVerification({ withPostgres = false } = {}, dependencies = defaultDependencies) {
  for (const step of localVerificationSteps) await dependencies.runStep(step);
  if (withPostgres) {
    await dependencies.withTemporaryPostgres((env) => dependencies.runPostgresVerification(env));
  }
}

async function main() {
  const options = parseVerifyLocalArgs();
  await runVerification(options);
  process.stdout.write(`\nLocal verification passed${options.withPostgres ? " with PostgreSQL and Docker integration" : ""}.\n`);
}

if (process.argv[1] && pathToFileURL(resolve(process.argv[1])).href === import.meta.url) {
  main().catch((error) => {
    console.error(error instanceof Error ? error.message : error);
    process.exitCode = 1;
  });
}
