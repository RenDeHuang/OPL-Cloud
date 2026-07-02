import assert from "node:assert/strict";
import { chmod, mkdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { productionReadiness } from "../../packages/console/src/production-readiness.js";

const productionEnv = {
  OPL_RUNTIME_PROVIDER: "tencent-cvm",
  OPL_HARBOR_REGISTRY: "harbor.oplcloud.cn",
  OPL_WORKSPACE_IMAGE: "harbor.oplcloud.cn/opl/one-person-lab-webui:2026-07-01",
  OPL_WORKSPACE_WEBUI_PORT: "3000",
  OPL_WORKSPACE_DATA_DIR: "/data",
  OPL_WORKSPACE_PROJECTS_DIR: "/projects",
  OPL_WORKSPACE_DOMAIN: "workspaces.oplcloud.cn",
  DATABASE_URL: "postgres://opl:secret@db.example.com:5432/opl_cloud",
  TENCENTCLOUD_SECRET_ID: "sid",
  TENCENTCLOUD_SECRET_KEY: "skey",
  TENCENTCLOUD_REGION: "ap-guangzhou",
  OPL_VPC_ID: "vpc-123",
  OPL_SUBNET_ID: "subnet-123",
  OPL_SECURITY_GROUP_ID: "sg-123",
  OPL_AVAILABILITY_ZONE: "ap-guangzhou-6",
  OPL_IMAGE_ID: "img-123",
  OPL_SSH_KEY_ID: "skey-123"
};

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
  TENCENT_DEPLOY_KUBECONFIG_REF: "/tmp/kubeconfig",
  TENCENT_DEPLOY_CLUSTER_ID: "cls-123",
  TENCENT_TCR_REGISTRY: "registry.example.com",
  TENCENT_TCR_NAMESPACE: "opl",
  TENCENT_TCR_REGION: "ap-guangzhou"
};

test("productionReadiness passes only when production runtime, image, persistence, Tencent env, and tools are present", async () => {
  const report = await productionReadiness({
    env: productionEnv,
    commandExists: () => true
  });

  assert.equal(report.ready, true);
  assert.deepEqual(report.missingEnv, []);
  assert.deepEqual(report.missingTools, []);
  assert.deepEqual(report.failedChecks, []);
  assert.deepEqual(report.checks.map((check) => `${check.id}:${check.ok}`), [
    "runtime_provider:true",
    "harbor_image:true",
    "opl_app_contract:true",
    "workspace_domain:true",
    "database_url:true",
    "provider_env:true",
    "tools:true"
  ]);
});

test("productionReadiness supports Tencent TKE without CVM image or SSH key fields", async () => {
  const report = await productionReadiness({
    env: tkeProductionEnv,
    commandExists: (command) => command === "kubectl"
  });

  assert.equal(report.ready, true);
  assert.deepEqual(report.missingEnv, []);
  assert.deepEqual(report.missingTools, []);
  assert.deepEqual(report.failedChecks, []);
  assert.equal(JSON.stringify(report).includes("OPL_IMAGE_ID"), false);
  assert.equal(JSON.stringify(report).includes("OPL_SSH_KEY_ID"), false);
  assert.deepEqual(report.checks.map((check) => `${check.id}:${check.ok}`), [
    "runtime_provider:true",
    "registry_images:true",
    "opl_app_contract:true",
    "workspace_domain:true",
    "database_url:true",
    "provider_env:true",
    "tools:true"
  ]);
});

test("productionReadiness reports TKE-specific blockers without requiring CVM fields", async () => {
  const report = await productionReadiness({
    env: {
      ...tkeProductionEnv,
      OPL_CLOUD_IMAGE: "",
      OPL_WORKSPACE_STORAGE_CLASS: "",
      OPL_IMAGE_ID: "",
      OPL_SSH_KEY_ID: ""
    },
    commandExists: () => false
  });

  assert.equal(report.ready, false);
  assert.ok(report.missingEnv.includes("OPL_CLOUD_IMAGE"));
  assert.ok(report.missingEnv.includes("OPL_WORKSPACE_STORAGE_CLASS"));
  assert.equal(report.missingEnv.includes("OPL_IMAGE_ID"), false);
  assert.equal(report.missingEnv.includes("OPL_SSH_KEY_ID"), false);
  assert.deepEqual(report.missingTools, ["kubectl"]);
  assert.ok(report.failedChecks.includes("registry_images"));
  assert.ok(report.failedChecks.includes("provider_env"));
});

test("productionReadiness reports concrete production blockers without leaking secret values", async () => {
  const report = await productionReadiness({
    env: {
      OPL_RUNTIME_PROVIDER: "tencent-cvm",
      OPL_WORKSPACE_IMAGE: "ghcr.io/gaofeng21cn/one-person-lab-webui:latest",
      OPL_WORKSPACE_DOMAIN: "localhost",
      TENCENTCLOUD_SECRET_ID: "sid"
    },
    commandExists: (command) => command === "tofu"
  });

  assert.equal(report.ready, false);
  assert.deepEqual(report.missingTools, ["ansible-playbook", "tccli", "caddy"]);
  assert.ok(report.missingEnv.includes("DATABASE_URL"));
  assert.ok(report.missingEnv.includes("TENCENTCLOUD_SECRET_KEY"));
  assert.ok(report.failedChecks.includes("provider_env"));
  assert.ok(report.failedChecks.includes("harbor_image"));
  assert.ok(report.failedChecks.includes("workspace_domain"));
  assert.equal(JSON.stringify(report).includes("sid"), false);
});

test("productionReadiness requires the Workspace image to come from the configured Harbor registry", async () => {
  const missingRegistry = await productionReadiness({
    env: {
      ...productionEnv,
      OPL_HARBOR_REGISTRY: ""
    },
    commandExists: () => true
  });
  assert.equal(missingRegistry.ready, false);
  assert.ok(missingRegistry.missingEnv.includes("OPL_HARBOR_REGISTRY"));
  assert.ok(missingRegistry.failedChecks.includes("harbor_image"));

  const wrongRegistry = await productionReadiness({
    env: {
      ...productionEnv,
      OPL_HARBOR_REGISTRY: "harbor.oplcloud.cn",
      OPL_WORKSPACE_IMAGE: "registry.example.com/opl/one-person-lab-webui:2026-07-01"
    },
    commandExists: () => true
  });

  assert.equal(wrongRegistry.ready, false);
  assert.deepEqual(wrongRegistry.missingEnv, []);
  assert.ok(wrongRegistry.failedChecks.includes("harbor_image"));
});

test("productionReadiness requires the one-person-lab-app WebUI runtime contract", async () => {
  const report = await productionReadiness({
    env: {
      ...productionEnv,
      OPL_WORKSPACE_WEBUI_PORT: "8080",
      OPL_WORKSPACE_DATA_DIR: "/tmp/data",
      OPL_WORKSPACE_PROJECTS_DIR: "/data/projects"
    },
    commandExists: () => true
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
    for (const tool of ["tofu", "ansible-playbook", "tccli", "caddy"]) {
      const toolPath = join(binDir, tool);
      await writeFile(toolPath, "#!/bin/sh\nexit 0\n");
      await chmod(toolPath, 0o755);
    }

    const report = await productionReadiness({
      env: {
        ...productionEnv,
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
