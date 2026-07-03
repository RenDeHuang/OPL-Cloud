import assert from "node:assert/strict";
import { chmod, mkdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { productionReadiness } from "../../packages/console/src/production-readiness.js";

const tkeProductionEnv = {
  OPL_RUNTIME_PROVIDER: "tencent-tke",
  OPL_CLOUD_IMAGE: "registry.example.com/opl/opl-cloud:2026-07-01",
  OPL_WORKSPACE_IMAGE: "registry.example.com/opl/one-person-lab-app:2026-07-01",
  OPL_WORKSPACE_WEBUI_PORT: "3000",
  OPL_WORKSPACE_DATA_DIR: "/data",
  OPL_WORKSPACE_PROJECTS_DIR: "/projects",
  OPL_PUBLIC_URL: "https://cloud.medopl.cn",
  OPL_CONSOLE_DOMAIN: "cloud.medopl.cn",
  OPL_WORKSPACE_DOMAIN: "workspace.medopl.cn",
  OPL_K8S_NAMESPACE: "opl-cloud",
  OPL_INGRESS_CLASS: "qcloud",
  OPL_IMAGE_PULL_SECRET_NAME: "tcr-pull-secret",
  OPL_WORKSPACE_STORAGE_CLASS: "cbs",
  DATABASE_URL: "postgresql://opl:secret@db.example.com:5432/opl_cloud",
  OPL_CONSOLE_USERS_JSON: JSON.stringify([
    {
      id: "usr-pi-production",
      email: "pi@medopl.cn",
      password: "ProdPiPass2026!",
      name: "Production PI",
      role: "pi",
      accountId: "pi-production"
    },
    {
      id: "usr-admin-production",
      email: "admin@medopl.cn",
      password: "ProdAdminPass2026!",
      name: "Production Admin",
      role: "admin",
      accountId: "admin"
    }
  ]),
  TENCENT_DEPLOY_KUBECONFIG_REF: "/tmp/kubeconfig",
  TENCENT_DEPLOY_CLUSTER_ID: "cls-123",
  TENCENT_TCR_REGISTRY: "registry.example.com",
  TENCENT_TCR_NAMESPACE: "opl",
  TENCENT_TCR_REGION: "ap-guangzhou"
};

test("productionReadiness passes only when the TKE production runtime, images, persistence, Tencent env, and kubectl are present", async () => {
  const report = await productionReadiness({
    env: tkeProductionEnv,
    commandExists: (command) => command === "kubectl"
  });

  assert.equal(report.ready, true);
  assert.deepEqual(report.missingEnv, []);
  assert.deepEqual(report.missingTools, []);
  assert.deepEqual(report.failedChecks, []);
  assert.deepEqual(report.checks.map((check) => `${check.id}:${check.ok}`), [
    "runtime_provider:true",
    "registry_images:true",
    "opl_app_contract:true",
    "workspace_domain:true",
    "database_url:true",
    "auth_seed:true",
    "provider_env:true",
    "tools:true"
  ]);
});

test("productionReadiness reports only TKE-specific blockers", async () => {
  const report = await productionReadiness({
    env: {
      ...tkeProductionEnv,
      OPL_CLOUD_IMAGE: "",
      OPL_WORKSPACE_STORAGE_CLASS: ""
    },
    commandExists: () => false
  });

  assert.equal(report.ready, false);
  assert.ok(report.missingEnv.includes("OPL_CLOUD_IMAGE"));
  assert.ok(report.missingEnv.includes("OPL_WORKSPACE_STORAGE_CLASS"));
  assert.deepEqual(report.missingTools, ["kubectl"]);
  assert.ok(report.failedChecks.includes("registry_images"));
  assert.ok(report.failedChecks.includes("provider_env"));
});

test("productionReadiness fails closed without a production auth user seed", async () => {
  const { OPL_CONSOLE_USERS_JSON, ...envWithoutAuthSeed } = tkeProductionEnv;
  const report = await productionReadiness({
    env: envWithoutAuthSeed,
    commandExists: (command) => command === "kubectl"
  });

  assert.equal(report.ready, false);
  assert.ok(report.failedChecks.includes("auth_seed"));
  assert.equal(
    report.checks.find((check) => check.id === "auth_seed").message,
    "OPL_CONSOLE_USERS_JSON or explicit PI/Admin auth credentials are required for production"
  );
});

test("productionReadiness rejects weak auth credentials", async () => {
  const report = await productionReadiness({
    env: {
      ...tkeProductionEnv,
      OPL_CONSOLE_USERS_JSON: "",
      OPL_PI_EMAIL: "owner@example.com",
      OPL_PI_ACCOUNT_ID: "acct-owner",
      OPL_PI_PASSWORD: "password",
      OPL_ADMIN_EMAIL: "admin@example.com",
      OPL_ADMIN_ACCOUNT_ID: "acct-admin",
      OPL_ADMIN_PASSWORD: "placeholder"
    },
    commandExists: (command) => command === "kubectl"
  });

  assert.equal(report.ready, false);
  assert.ok(report.failedChecks.includes("auth_seed"));
});

test("productionReadiness rejects the built-in admin bootstrap credential", async () => {
  const report = await productionReadiness({
    env: {
      ...tkeProductionEnv,
      OPL_CONSOLE_USERS_JSON: JSON.stringify([
        {
          id: "usr-pi-production",
          email: "pi@medopl.cn",
          password: "ProdPiPass2026!",
          name: "Production PI",
          role: "pi",
          accountId: "pi-production"
        },
        {
          id: "usr-admin-bootstrap",
          email: "admin@opl.local",
          password: "OplAdminPass2026!",
          name: "OPL Admin",
          role: "admin",
          accountId: "admin"
        }
      ])
    },
    commandExists: (command) => command === "kubectl"
  });

  assert.equal(report.ready, false);
  assert.ok(report.failedChecks.includes("auth_seed"));
});

test("productionReadiness rejects non-TKE production providers", async () => {
  const report = await productionReadiness({
    env: {
      OPL_RUNTIME_PROVIDER: "unsupported-production-runtime",
      OPL_WORKSPACE_IMAGE: "registry.example.com/opl/one-person-lab-app:2026-07-01",
      OPL_WORKSPACE_DOMAIN: "localhost",
      DATABASE_URL: "postgresql://opl:secret@db.example.com:5432/opl_cloud"
    },
    commandExists: () => false
  });

  assert.equal(report.ready, false);
  assert.deepEqual(report.missingTools, []);
  assert.ok(report.failedChecks.includes("runtime_provider"));
  assert.ok(report.failedChecks.includes("workspace_domain"));
  assert.equal(JSON.stringify(report).includes("TENCENTCLOUD_SECRET"), false);
});

test("productionReadiness requires the one-person-lab-app WebUI runtime contract", async () => {
  const report = await productionReadiness({
    env: {
      ...tkeProductionEnv,
      OPL_WORKSPACE_WEBUI_PORT: "8080",
      OPL_WORKSPACE_DATA_DIR: "/tmp/data",
      OPL_WORKSPACE_PROJECTS_DIR: "/data/projects"
    },
    commandExists: (command) => command === "kubectl"
  });

  assert.equal(report.ready, false);
  assert.deepEqual(report.missingEnv, []);
  assert.ok(report.failedChecks.includes("opl_app_contract"));
  assert.equal(
    report.checks.find((check) => check.id === "opl_app_contract").message,
    "one-person-lab-app WebUI must expose port 3000 and persist /data plus /projects"
  );
});

test("productionReadiness default command probe checks required tools from PATH", async () => {
  const binDir = join(tmpdir(), `opl-cloud-tools-${Date.now()}`);
  await mkdir(binDir, { recursive: true });
  try {
    for (const tool of ["kubectl"]) {
      const toolPath = join(binDir, tool);
      await writeFile(toolPath, "#!/bin/sh\nexit 0\n");
      await chmod(toolPath, 0o755);
    }

    const report = await productionReadiness({
      env: {
        ...tkeProductionEnv,
        PATH: binDir
      }
    });

    assert.equal(report.ready, true);
    assert.deepEqual(report.missingTools, []);
    assert.ok(report.checks.find((check) => check.id === "tools").ok);
  } finally {
    await rm(binDir, { recursive: true, force: true });
  }
});
