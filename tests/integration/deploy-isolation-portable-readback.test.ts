import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { createServer } from "node:net";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createHash, randomBytes } from "node:crypto";
import test from "node:test";

const enabled = process.env.OPL_PORTABLE_RUNTIME_READBACK === "1";
const repoRoot = process.cwd();

function command(args: string[], check = true) {
  const result = spawnSync(args[0], args.slice(1), {
    cwd: repoRoot,
    encoding: "utf8",
    maxBuffer: 32 * 1024 * 1024
  });
  if (result.error) throw result.error;
  if (check && result.status !== 0) {
    throw new Error([result.stdout, result.stderr].filter(Boolean).join("\n").trim());
  }
  return result;
}

function docker(args: string[], check = true) {
  return command(["docker", ...args], check);
}

async function unusedHostPort() {
  const server = createServer();
  await new Promise<void>((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  const address = server.address();
  assert.ok(address && typeof address === "object");
  const port = address.port;
  await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()));
  return port;
}

function stableID(...parts: string[]) {
  const hash = createHash("sha1");
  for (const part of parts) {
    hash.update(part);
    hash.update(Buffer.from([0]));
  }
  return hash.digest("hex");
}

function containerState(name: string) {
  const [inspection] = JSON.parse(docker(["inspect", name]).stdout) as Array<{
    RestartCount: number;
    State: { Status: string; Health?: { Status: string } };
  }>;
  return {
    status: inspection.State.Status,
    health: inspection.State.Health?.Status ?? "",
    restartCount: inspection.RestartCount
  };
}

function containerID(name: string) {
  return docker(["inspect", name, "--format", "{{.Id}}"] ).stdout.trim();
}

function containerEnv(name: string) {
  const values = JSON.parse(docker(["inspect", name, "--format", "{{json .Config.Env}}"] ).stdout) as string[];
  return Object.fromEntries(values.map((entry) => {
    const separator = entry.indexOf("=");
    return [entry.slice(0, separator), entry.slice(separator + 1)];
  }));
}

const sub2APIFixture = String.raw`
  import http from "node:http";
  const respond = (response, status, data) => {
    response.writeHead(status, { "content-type": "application/json" });
    response.end(JSON.stringify(status === 200 ? { code: 0, data } : { code: status, data: {} }));
  };
  const server = http.createServer((request, response) => {
    if (request.method === "POST" && request.url === "/api/v1/auth/login") {
      respond(response, 200, {
        access_token: "portable-fixture-access",
        refresh_token: "portable-fixture-refresh",
        user: { id: 91, email: "portable-admin@example.test", status: "active" }
      });
      return;
    }
    if (request.method === "GET" && request.url === "/api/v1/admin/users/91") {
      if (request.headers.authorization !== "Bearer portable-fixture-access") {
        respond(response, 401, {});
        return;
      }
      respond(response, 200, { id: 91, email: "portable-admin@example.test", status: "active" });
      return;
    }
    respond(response, 404, {});
  });
  server.listen(8080, "0.0.0.0");
`;

if (enabled) test("portable Compose enforces runtime database and caller isolation", {
  timeout: 20 * 60_000
}, async () => {
  const suffix = `${process.pid}-${randomBytes(4).toString("hex")}`;
  const project = `opl-cloud-isolation-${suffix}`;
  const image = `${project}:source`;
  const network = `${project}_opl-cloud`;
  const postgresVolume = `${project}_opl-cloud-postgres`;
  const fixture = `${project}-sub2api`;
  const tempRoot = await mkdtemp(join(tmpdir(), "opl-cloud-isolation-"));
  const envFile = join(tempRoot, "portable.env");
  const hostPort = await unusedHostPort();
  const subnetOctet = 20 + (process.pid % 200);
  const postgresHost = `10.251.${subnetOctet}.10`;
  const passwords = {
    admin: randomBytes(32).toString("hex"),
    controlPlane: randomBytes(32).toString("hex"),
    fabric: randomBytes(32).toString("hex"),
    ledger: randomBytes(32).toString("hex")
  };
  const tokens = {
    controlPlane: randomBytes(32).toString("hex"),
    fabric: randomBytes(32).toString("hex"),
    ledger: randomBytes(32).toString("hex")
  };
  const names = {
    postgres: `${project}-postgres-1`,
    controlPlane: `${project}-control-plane-1`,
    fabric: `${project}-fabric-1`,
    ledger: `${project}-ledger-1`
  };
  const composeServices = {
    postgres: "postgres",
    controlPlane: "control-plane",
    fabric: "fabric",
    ledger: "ledger"
  };
  const composePrefix = [
    "compose", "--project-name", project, "--env-file", envFile, "-f", "compose.yaml"
  ];
  const compose = (args: string[], check = true) => docker([...composePrefix, ...args], check);
  const writeEnvironment = async () => {
    const values = [
      `OPL_CLOUD_IMAGE=${image}`,
      "OPL_BIND_ADDRESS=127.0.0.1",
      `OPL_HTTP_PORT=${hostPort}`,
      `OPL_PUBLIC_URL=http://127.0.0.1:${hostPort}`,
      `OPL_DOCKER_SUBNET=10.251.${subnetOctet}.0/24`,
      `OPL_POSTGRES_HOST=${postgresHost}`,
      `OPL_POSTGRES_ADMIN_PASSWORD=${passwords.admin}`,
      `OPL_CONTROL_PLANE_DATABASE_PASSWORD=${passwords.controlPlane}`,
      `OPL_FABRIC_DATABASE_PASSWORD=${passwords.fabric}`,
      `OPL_LEDGER_DATABASE_PASSWORD=${passwords.ledger}`,
      `OPL_CONTROL_PLANE_SERVICE_TOKEN=${tokens.controlPlane}`,
      `OPL_FABRIC_SERVICE_TOKEN=${tokens.fabric}`,
      `OPL_LEDGER_SERVICE_TOKEN=${tokens.ledger}`,
      "OPL_SUB2API_BASE_URL=http://sub2api-fixture:8080",
      "OPL_SUB2API_ADMIN_EMAIL=portable-admin@example.test",
      "OPL_SUB2API_ADMIN_PASSWORD=portable-fixture-password",
      "OPL_SUB2API_REQUEST_TIMEOUT_MS=2000",
      `OPL_AIONUI_ADMIN_PASSWORD_SEED=${randomBytes(32).toString("hex")}`,
      "OPL_MONTHLY_BILLING_WORKER_ENABLED=0",
      "OPL_WORKSPACE_LAUNCH_WORKER_ENABLED=0",
      ""
    ];
    await writeFile(envFile, values.join("\n"), { mode: 0o600 });
  };
  const waitForHealthy = (name: string) => {
    for (let attempt = 0; attempt < 90; attempt += 1) {
      const state = containerState(name);
      if (state.status === "running" && state.health === "healthy") return;
      command(["sleep", "1"]);
    }
    assert.fail(`${name} did not become healthy`);
  };
  const assertHealthyWithoutRestarts = (serviceNames = Object.values(names)) => {
    for (const name of serviceNames) {
      const state = containerState(name);
      assert.equal(state.status, "running", `${name} state`);
      assert.equal(state.health, "healthy", `${name} health`);
      assert.equal(state.restartCount, 0, `${name} restart count`);
    }
  };
  const probeToken = (url: string, token: string) => docker([
    "run", "--rm", "--network", network, "--entrypoint", "curl", image,
    "--silent", "--output", "/dev/null", "--write-out", "%{http_code}",
    "-H", `Authorization: Bearer ${token}`, url
  ]).stdout.trim();
  const fetchWithRetry = async (path: string, init?: RequestInit) => {
    return fetchURLWithRetry(`http://127.0.0.1:${hostPort}${path}`, init);
  };
  const fetchURLWithRetry = async (url: string, init?: RequestInit, attempts = 20) => {
    let lastError: unknown;
    for (let attempt = 0; attempt < attempts; attempt += 1) {
      try {
        return await fetch(url, init);
      } catch (error) {
        lastError = error;
        await new Promise((resolve) => setTimeout(resolve, 250));
      }
    }
    throw lastError;
  };
  const runtimeReadiness = async () => {
    const response = await fetchWithRetry("/api/runtime/readiness");
    assert.equal(response.status, 200, await response.text());
  };
  const billingReceipts = async () => {
    const login = await fetchWithRetry("/api/auth/login", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ email: "portable-admin@example.test", password: "portable-fixture-password" })
    });
    const loginBody = await login.text();
    assert.equal(login.status, 200, loginBody);
    const cookie = login.headers.get("set-cookie")?.split(";", 1)[0];
    assert.ok(cookie, "Control Plane login did not return a session cookie");
    const response = await fetchWithRetry("/api/billing/receipts", {
      headers: { cookie }
    });
    const responseBody = await response.text();
    assert.equal(response.status, 200, responseBody);
    const envelope = JSON.parse(responseBody) as { source?: string; status?: string };
    assert.equal(envelope.source, "ledger");
    assert.equal(envelope.status, "empty");
  };
  const serviceIDs = () => Object.fromEntries(
    Object.entries(names).map(([service, name]) => [service, containerID(name)])
  );
  const recreate = (...services: string[]) => {
    for (const service of services) {
      const key = service as keyof typeof names;
      compose(["up", "-d", "--no-deps", "--force-recreate", composeServices[key]]);
      waitForHealthy(names[key]);
    }
  };
  const probeControlPlaneInboundIdentity = async (acceptedToken: string, rejectedTokens: string[]) => {
    const probePort = await unusedHostPort();
    const probeName = `${project}-control-token-probe`;
    const approvalID = `portable-${randomBytes(6).toString("hex")}`;
    const launchKey = `portable-launch-${randomBytes(6).toString("hex")}`;
    const operationID = `workspace-launch-${stableID("acct-admin", launchKey).slice(0, 18)}`;
    const cloudDigest = `sha256:${"a".repeat(64)}`;
    const workspaceDigest = `sha256:${"b".repeat(64)}`;
    const approval = {
      schemaVersion: 1,
      operationMode: "acceptance_b_fresh_order",
      approvalId: approvalID,
      expiresAt: new Date(Date.now() + 5 * 60_000).toISOString(),
      confirmation: "RUN_ONE_INDEPENDENT_FRESH_BASIC_ORDER_FOR_ACCEPTANCE_B",
      release: {
        mergedMainSha: revision,
        cloudImageDigest: cloudDigest,
        workspaceImageDigest: workspaceDigest
      },
      customer: { email: "portable-admin@example.test", accountId: "acct-admin" },
      launch: {
        idempotencyKey: launchKey,
        operationId: operationID,
        workspaceId: `ws-${stableID("workspace-launch-v2", "acct-admin", operationID).slice(0, 18)}`,
        name: "Portable token probe",
        packageId: "basic",
        sizeGb: 10,
        autoRenew: false
      },
      allowedWrites: [
        "submit_one_workspace_launch", "debit_one_basic_month", "create_one_workspace_key",
        "ensure_one_compute_allocation", "ensure_one_storage", "ensure_one_attachment",
        "ensure_one_gateway_secret", "ensure_one_runtime", "activate_one_workspace",
        "record_one_purchase_receipt"
      ],
      forbiddenWrites: [
        "provision_account", "adjust_wallet", "submit_second_workspace_launch",
        "create_second_compute_allocation", "create_second_storage", "refund", "renew",
        "delete", "replace", "send_model_request"
      ]
    };
    try {
      docker([
        "run", "-d", "--name", probeName, "--network", network,
        "-p", `127.0.0.1:${probePort}:8788`, "--label", "opl.task=deploy-isolation-01",
        "-e", "NODE_ENV=production", "-e", "PGSSLMODE=disable", "-e", "CONTROL_PLANE_ADDR=:8788",
        "-e", `DATABASE_URL=postgresql://opl_control_plane:${passwords.controlPlane}@${postgresHost}:5432/opl_control_plane?sslmode=disable`,
        "-e", "LEDGER_URL=http://ledger:8081", "-e", "FABRIC_URL=http://127.0.0.1:1",
        "-e", `OPL_INTERNAL_SERVICE_TOKEN=${acceptedToken}`,
        "-e", `OPL_FABRIC_SERVICE_TOKEN=${tokens.fabric}`,
        "-e", `OPL_LEDGER_SERVICE_TOKEN=${tokens.ledger}`,
        "-e", "OPL_SUB2API_BASE_URL=http://sub2api-fixture:8080",
        "-e", "OPL_SUB2API_ADMIN_EMAIL=portable-admin@example.test",
        "-e", "OPL_SUB2API_ADMIN_PASSWORD=portable-fixture-password",
        "-e", "OPL_SUB2API_REQUEST_TIMEOUT_MS=2000",
        "-e", `OPL_AIONUI_ADMIN_PASSWORD_SEED=${randomBytes(32).toString("hex")}`,
        "-e", "OPL_MONTHLY_BILLING_WORKER_ENABLED=0",
        "-e", "OPL_WORKSPACE_LAUNCH_WORKER_ENABLED=0",
        "-e", `OPL_RELEASE_SHA=${revision}`,
        "-e", `OPL_CLOUD_IMAGE=registry.example/opl-cloud@${cloudDigest}`,
        "-e", `OPL_WORKSPACE_IMAGE=registry.example/opl-workspace@${workspaceDigest}`,
        "-e", `OPL_PRODUCTION_BASIC_ACCEPTANCE_B_APPROVAL_JSON=${JSON.stringify(approval)}`,
        image, "/usr/local/bin/opl-control-plane"
      ]);
      const health = await fetchURLWithRetry(`http://127.0.0.1:${probePort}/api/healthz`, undefined, 60);
      assert.equal(health.status, 200, await health.text());
      const login = await fetchURLWithRetry(`http://127.0.0.1:${probePort}/api/auth/login`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ email: "portable-admin@example.test", password: "portable-fixture-password" })
      });
      const loginBody = await login.text();
      assert.equal(login.status, 200, loginBody);
      const cookie = login.headers.get("set-cookie")?.split(";", 1)[0];
      const csrf = login.headers.get("x-opl-csrf-token");
      assert.ok(cookie && csrf, "Control Plane token probe login did not return session and CSRF credentials");
      const launch = async (candidate: string) => {
        const response = await fetchURLWithRetry(`http://127.0.0.1:${probePort}/api/workspace-launches`, {
          method: "POST",
          headers: {
            "content-type": "application/json",
            cookie,
            "x-opl-csrf": csrf,
            "idempotency-key": launchKey,
            "x-opl-acceptance-b-capability": candidate,
            "x-opl-acceptance-b-approval-id": approvalID
          },
          body: JSON.stringify({ name: "Portable token probe", packageId: "basic", sizeGb: 10, autoRenew: false })
        });
        return { status: response.status, body: await response.text() };
      };
      for (const rejectedToken of rejectedTokens) {
        const rejected = await launch(rejectedToken);
        assert.equal(rejected.status, 409, rejected.body);
        assert.match(rejected.body, /workspace_launch_admission_disabled/);
      }
      const accepted = await launch(acceptedToken);
      assert.equal(accepted.status, 502, accepted.body);
      assert.match(accepted.body, /upstream_unavailable/);
    } catch (error) {
      const inspection = docker([
        "inspect", probeName, "--format",
        "status={{.State.Status}} exit={{.State.ExitCode}} error={{json .State.Error}}"
      ], false);
      const logs = docker(["logs", "--tail", "50", probeName], false);
      throw new Error([
        error instanceof Error ? error.stack ?? error.message : String(error),
        inspection.stdout, inspection.stderr, logs.stdout, logs.stderr
      ].filter(Boolean).join("\n"));
    } finally {
      docker(["rm", "-f", probeName], false);
      assert.notEqual(docker(["inspect", probeName], false).status, 0, "Control Plane token probe was not removed");
    }
  };

  await writeEnvironment();
  const revision = command(["git", "rev-parse", "HEAD"]).stdout.trim();

  try {
    docker(["version"]);
    docker(["compose", "version"]);
    compose(["config", "--quiet"]);
    docker([
      "build", "--label", `org.opencontainers.image.revision=${revision}`,
      "--label", "opl.task=deploy-isolation-01", "-t", image, "."
    ]);
    compose(["up", "-d", "--wait", "--wait-timeout", "180", "postgres", "ledger", "fabric"]);
    docker([
      "run", "-d", "--name", fixture, "--network", network,
      "--network-alias", "sub2api-fixture", "--label", "opl.task=deploy-isolation-01",
      image, "node", "--input-type=module", "-e", sub2APIFixture
    ]);
    compose(["up", "-d", "--no-deps", "control-plane"]);
    waitForHealthy(names.controlPlane);
    assertHealthyWithoutRestarts();

    const expectedDatabaseURLs = {
      controlPlane: `postgresql://opl_control_plane:${passwords.controlPlane}@${postgresHost}:5432/opl_control_plane?sslmode=disable`,
      fabric: `postgresql://opl_fabric:${passwords.fabric}@${postgresHost}:5432/opl_fabric?sslmode=disable`,
      ledger: `postgresql://opl_ledger:${passwords.ledger}@${postgresHost}:5432/opl_ledger?sslmode=disable`
    };
    assert.equal(containerEnv(names.controlPlane).DATABASE_URL, expectedDatabaseURLs.controlPlane);
    assert.equal(containerEnv(names.fabric).DATABASE_URL, expectedDatabaseURLs.fabric);
    assert.equal(containerEnv(names.ledger).DATABASE_URL, expectedDatabaseURLs.ledger);
    assert.equal(containerEnv(names.controlPlane).OPL_INTERNAL_SERVICE_TOKEN, tokens.controlPlane);
    assert.equal(containerEnv(names.controlPlane).OPL_FABRIC_SERVICE_TOKEN, tokens.fabric);
    assert.equal(containerEnv(names.controlPlane).OPL_LEDGER_SERVICE_TOKEN, tokens.ledger);
    assert.equal(containerEnv(names.fabric).OPL_INTERNAL_SERVICE_TOKEN, tokens.fabric);
    assert.equal(containerEnv(names.ledger).OPL_INTERNAL_SERVICE_TOKEN, tokens.ledger);

    const databases = {
      controlPlane: { role: "opl_control_plane", database: "opl_control_plane", password: passwords.controlPlane },
      fabric: { role: "opl_fabric", database: "opl_fabric", password: passwords.fabric },
      ledger: { role: "opl_ledger", database: "opl_ledger", password: passwords.ledger }
    };
    for (const [owner, identity] of Object.entries(databases)) {
      const own = docker([
        "exec", "-e", `PGPASSWORD=${identity.password}`, names.postgres,
        "psql", "-h", "127.0.0.1", "-U", identity.role, "-d", identity.database,
        "-Atqc", "select current_user || ':' || current_database()"
      ]).stdout.trim();
      assert.equal(own, `${identity.role}:${identity.database}`);
      const privileges = docker([
        "exec", names.postgres, "psql", "-U", "postgres", "-d", "postgres", "-Atqc",
        `select rolsuper || ':' || rolcreatedb || ':' || rolcreaterole from pg_roles where rolname='${identity.role}'`
      ]).stdout.trim();
      assert.equal(privileges, "false:false:false");
      const tableCount = Number(docker([
        "exec", "-e", `PGPASSWORD=${identity.password}`, names.postgres,
        "psql", "-h", "127.0.0.1", "-U", identity.role, "-d", identity.database,
        "-Atqc", "select count(*) from pg_tables where schemaname='public' and tableowner=current_user"
      ]).stdout.trim());
      assert.ok(tableCount > 0, `${owner} owns no migrated tables`);
      for (const target of Object.values(databases)) {
        if (target.database === identity.database) continue;
        const tableAccess = docker([
          "exec", names.postgres, "psql", "-U", "postgres", "-d", target.database,
          "-Atqc", `select coalesce(bool_or(has_table_privilege('${identity.role}', format('%I.%I', schemaname, tablename), privilege)), false) from pg_tables cross join (values ('SELECT'), ('INSERT'), ('UPDATE'), ('DELETE'), ('TRUNCATE'), ('REFERENCES'), ('TRIGGER')) as privileges(privilege) where schemaname='public'`
        ]).stdout.trim();
        assert.equal(tableAccess, "f", `${owner} has table access in ${target.database}`);
        const rejected = docker([
          "exec", "-e", `PGPASSWORD=${identity.password}`, names.postgres,
          "psql", "-h", "127.0.0.1", "-U", identity.role, "-d", target.database,
          "-Atqc", "select count(*) from pg_tables"
        ], false);
        assert.notEqual(rejected.status, 0, `${owner} connected to ${target.database}`);
        assert.match(`${rejected.stdout}\n${rejected.stderr}`, /permission denied for database|does not have CONNECT privilege/);
      }
    }

    assert.equal(probeToken("http://fabric:8082/fabric/catalog", tokens.fabric), "200");
    assert.equal(probeToken("http://fabric:8082/fabric/catalog", tokens.controlPlane), "401");
    assert.equal(probeToken("http://fabric:8082/fabric/catalog", tokens.ledger), "401");
    assert.equal(probeToken("http://ledger:8081/ledger/receipts", tokens.ledger), "200");
    assert.equal(probeToken("http://ledger:8081/ledger/receipts", tokens.controlPlane), "401");
    assert.equal(probeToken("http://ledger:8081/ledger/receipts", tokens.fabric), "401");
    await runtimeReadiness();
    await billingReceipts();
    await probeControlPlaneInboundIdentity(tokens.controlPlane, [tokens.fabric, tokens.ledger]);

    let before = serviceIDs();
    const oldControlPlaneToken = tokens.controlPlane;
    tokens.controlPlane = randomBytes(32).toString("hex");
    await writeEnvironment();
    compose(["config", "--quiet"]);
    recreate("controlPlane");
    let after = serviceIDs();
    assert.notEqual(after.controlPlane, before.controlPlane);
    assert.equal(after.postgres, before.postgres);
    assert.equal(after.fabric, before.fabric);
    assert.equal(after.ledger, before.ledger);
    assert.equal(containerEnv(names.controlPlane).OPL_INTERNAL_SERVICE_TOKEN, tokens.controlPlane);
    for (const candidate of [oldControlPlaneToken, tokens.controlPlane]) {
      assert.equal(probeToken("http://fabric:8082/fabric/catalog", candidate), "401");
      assert.equal(probeToken("http://ledger:8081/ledger/receipts", candidate), "401");
    }
    assertHealthyWithoutRestarts();
    await runtimeReadiness();
    await billingReceipts();
    await probeControlPlaneInboundIdentity(tokens.controlPlane, [oldControlPlaneToken, tokens.fabric, tokens.ledger]);

    before = after;
    const oldFabricToken = tokens.fabric;
    tokens.fabric = randomBytes(32).toString("hex");
    await writeEnvironment();
    compose(["config", "--quiet"]);
    recreate("fabric", "controlPlane");
    after = serviceIDs();
    assert.notEqual(after.fabric, before.fabric);
    assert.notEqual(after.controlPlane, before.controlPlane);
    assert.equal(after.postgres, before.postgres);
    assert.equal(after.ledger, before.ledger);
    assert.equal(probeToken("http://fabric:8082/fabric/catalog", oldFabricToken), "401");
    assert.equal(probeToken("http://fabric:8082/fabric/catalog", tokens.fabric), "200");
    assertHealthyWithoutRestarts();
    await runtimeReadiness();
    await billingReceipts();

    before = after;
    const oldLedgerToken = tokens.ledger;
    tokens.ledger = randomBytes(32).toString("hex");
    await writeEnvironment();
    compose(["config", "--quiet"]);
    recreate("ledger", "controlPlane");
    after = serviceIDs();
    assert.notEqual(after.ledger, before.ledger);
    assert.notEqual(after.controlPlane, before.controlPlane);
    assert.equal(after.postgres, before.postgres);
    assert.equal(after.fabric, before.fabric);
    assert.equal(probeToken("http://ledger:8081/ledger/receipts", oldLedgerToken), "401");
    assert.equal(probeToken("http://ledger:8081/ledger/receipts", tokens.ledger), "200");
    assertHealthyWithoutRestarts();
    await runtimeReadiness();
    await billingReceipts();
  } finally {
    docker(["rm", "-f", fixture], false);
    compose(["down", "--volumes", "--remove-orphans"], false);
    docker(["image", "rm", "-f", image], false);
    await rm(tempRoot, { recursive: true, force: true });
    assert.notEqual(docker(["inspect", fixture], false).status, 0, "fixture container was not removed");
    assert.notEqual(docker(["network", "inspect", network], false).status, 0, "task network was not removed");
    assert.notEqual(docker(["volume", "inspect", postgresVolume], false).status, 0, "task volume was not removed");
    assert.notEqual(docker(["image", "inspect", image], false).status, 0, "task image was not removed");
  }
});
