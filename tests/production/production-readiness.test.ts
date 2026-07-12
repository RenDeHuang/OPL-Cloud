import assert from "node:assert/strict";
import { chmod, mkdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { productionReadiness } from "../../services/fabric/ops/production-readiness.ts";

const tkeProductionEnv = {
  OPL_RUNTIME_PROVIDER: "tencent-tke",
  OPL_CLOUD_IMAGE: "registry.example.com/opl/opl-cloud:2026-07-01",
  OPL_WORKSPACE_IMAGE: "registry.example.com/opl/one-person-lab-app:2026-07-01",
  OPL_AIONUI_ADMIN_PASSWORD_SEED: "workspace-secret-2026-very-long",
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
  OPL_TENCENT_PROVISIONER_BIN: "/usr/local/bin/opl-tencent-provisioner",
  DATABASE_URL: "postgresql://opl:secret@db.example.com:5432/opl_cloud",
  OPL_CONSOLE_USERS_JSON: JSON.stringify([
    {
      id: "usr-pi-production",
      email: "pi@medopl.cn",
      password: "ProdPiPass2026!",
      name: "Production PI",
      role: "owner",
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
  TENCENTCLOUD_SECRET_ID: "secret-id",
  TENCENTCLOUD_SECRET_KEY: "secret-key",
  TENCENTCLOUD_REGION: "ap-guangzhou",
  TENCENT_DEPLOY_KUBECONFIG_REF: "/tmp/kubeconfig",
  TENCENT_DEPLOY_CLUSTER_ID: "cls-123",
  TENCENT_CVM_SUBNET_ID: "subnet-123",
  TENCENT_CVM_SECURITY_GROUP_IDS: "sg-123,sg-456",
  RUN_TENCENT_CREATE_RELEASE_EXECUTION: "1",
  TENCENT_TCR_REGISTRY: "registry.example.com",
  TENCENT_TCR_NAMESPACE: "opl",
  TENCENT_TCR_REGION: "ap-guangzhou"
};

test("productionReadiness passes only when the TKE production runtime, images, persistence, Tencent env, and kubectl are present", async () => {
  const report = await productionReadiness({
    env: tkeProductionEnv,
    commandExists: (command) => command === "kubectl" || command === "/usr/local/bin/opl-tencent-provisioner"
  });

  assert.equal(report.ready, true);
  assert.deepEqual(report.missingEnv, []);
  assert.deepEqual(report.missingTools, []);
  assert.deepEqual(report.failedChecks, []);
  assert.deepEqual(report.checks.map((check) => `${check.id}:${check.ok}`), [
    "runtime_provider:true",
    "registry_images:true",
    "opl_app_contract:true",
    "aionui_admin_password_seed:true",
    "workspace_domain:true",
    "database_url:true",
    "auth_seed:true",
    "provider_env:true",
    "live_mutation_guard:true",
    "tools:true"
  ]);
});

test("productionReadiness requires the AionUI admin password seed for managed WebUI login", async () => {
  const { OPL_AIONUI_ADMIN_PASSWORD_SEED, ...envWithoutWebuiPasswordSeed } = tkeProductionEnv;

  const report = await productionReadiness({
    env: envWithoutWebuiPasswordSeed,
    commandExists: (command) => command === "kubectl" || command === "/usr/local/bin/opl-tencent-provisioner"
  });

  assert.equal(report.ready, false);
  assert.ok(report.missingEnv.includes("OPL_AIONUI_ADMIN_PASSWORD_SEED"));
  assert.ok(report.failedChecks.includes("aionui_admin_password_seed"));
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
  assert.deepEqual(report.missingTools, ["kubectl", "/usr/local/bin/opl-tencent-provisioner"]);
  assert.ok(report.failedChecks.includes("registry_images"));
  assert.ok(report.failedChecks.includes("provider_env"));
});

test("productionReadiness rejects empty container image tags", async () => {
  const report = await productionReadiness({
    env: {
      ...tkeProductionEnv,
      OPL_CLOUD_IMAGE: "registry.example.com/opl/opl-cloud:",
      OPL_WORKSPACE_IMAGE: "registry.example.com/opl/one-person-lab-app:"
    },
    commandExists: (command) => command === "kubectl" || command === "/usr/local/bin/opl-tencent-provisioner"
  });

  assert.equal(report.ready, false);
  assert.ok(report.failedChecks.includes("registry_images"));
});

test("productionReadiness requires Tencent Go SDK mutation inputs for live compute allocation", async () => {
  const {
    TENCENTCLOUD_SECRET_ID,
    TENCENTCLOUD_SECRET_KEY,
    TENCENTCLOUD_REGION,
    TENCENT_CVM_SUBNET_ID,
    TENCENT_CVM_SECURITY_GROUP_IDS,
    RUN_TENCENT_CREATE_RELEASE_EXECUTION,
    ...missingMutationEnv
  } = tkeProductionEnv;
  const report = await productionReadiness({
    env: missingMutationEnv,
    commandExists: (command) => command === "kubectl" || command === "/usr/local/bin/opl-tencent-provisioner"
  });

  assert.equal(report.ready, false);
  assert.ok(report.missingEnv.includes("TENCENTCLOUD_SECRET_ID"));
  assert.ok(report.missingEnv.includes("TENCENTCLOUD_SECRET_KEY"));
  assert.ok(report.missingEnv.includes("TENCENTCLOUD_REGION"));
  assert.ok(report.missingEnv.includes("TENCENT_CVM_SUBNET_ID"));
  assert.ok(report.missingEnv.includes("TENCENT_CVM_SECURITY_GROUP_IDS"));
  assert.ok(report.missingEnv.includes("RUN_TENCENT_CREATE_RELEASE_EXECUTION"));
  assert.ok(report.failedChecks.includes("provider_env"));
});

test("productionReadiness fails closed without a production auth user seed", async () => {
  const { OPL_CONSOLE_USERS_JSON, ...envWithoutAuthSeed } = tkeProductionEnv;
  const report = await productionReadiness({
    env: envWithoutAuthSeed,
    commandExists: (command) => command === "kubectl" || command === "/usr/local/bin/opl-tencent-provisioner"
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
      OPL_PI_EMAIL: "weak-pi@medopl.cn",
      OPL_PI_ACCOUNT_ID: "acct-owner",
      OPL_PI_PASSWORD: "password",
      OPL_ADMIN_EMAIL: "admin@medopl.cn",
      OPL_ADMIN_ACCOUNT_ID: "acct-admin",
      OPL_ADMIN_PASSWORD: "placeholder"
    },
    commandExists: (command) => command === "kubectl" || command === "/usr/local/bin/opl-tencent-provisioner"
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
    commandExists: (command) => command === "kubectl" || command === "/usr/local/bin/opl-tencent-provisioner"
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
    commandExists: (command) => command === "kubectl" || command === "/usr/local/bin/opl-tencent-provisioner"
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
    for (const tool of ["kubectl", "opl-tencent-provisioner"]) {
      const toolPath = join(binDir, tool);
      await writeFile(toolPath, "#!/bin/sh\nexit 0\n");
      await chmod(toolPath, 0o755);
    }

    const report = await productionReadiness({
      env: {
        ...tkeProductionEnv,
        OPL_TENCENT_PROVISIONER_BIN: join(binDir, "opl-tencent-provisioner"),
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

test("productionReadiness fails when the Go provisioner binary is not executable", async () => {
  const report = await productionReadiness({
    env: tkeProductionEnv,
    commandExists: (command) => command === "kubectl"
  });

  assert.equal(report.ready, false);
  assert.deepEqual(report.missingTools, ["/usr/local/bin/opl-tencent-provisioner"]);
  assert.ok(report.failedChecks.includes("tools"));
});
