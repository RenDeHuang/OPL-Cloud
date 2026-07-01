import assert from "node:assert/strict";
import test from "node:test";

import { productionReadiness } from "../../services/api/src/production-readiness.js";

const productionEnv = {
  OPL_RUNTIME_PROVIDER: "tencent-cvm",
  OPL_HARBOR_REGISTRY: "harbor.oplcloud.cn",
  OPL_WORKSPACE_IMAGE: "harbor.oplcloud.cn/opl/one-person-lab-webui:2026-07-01",
  OPL_WORKSPACE_WEBUI_PORT: "3000",
  OPL_WORKSPACE_DATA_DIR: "/data",
  OPL_WORKSPACE_PROJECTS_DIR: "/projects",
  OPL_WORKSPACE_DOMAIN: "workspaces.oplcloud.cn",
  DATABASE_URL: "postgres://opl:secret@db.example.com:5432/opl_cloud",
  OPENMETER_ENDPOINT: "https://openmeter.example.com",
  OPENMETER_API_KEY: "om_secret",
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

test("productionReadiness passes only when production runtime, Harbor image, persistence, metering, Tencent env, and tools are present", async () => {
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
    "openmeter:true",
    "tencent_env:true",
    "tools:true"
  ]);
});

test("productionReadiness reports concrete production blockers without leaking secret values", async () => {
  const report = await productionReadiness({
    env: {
      OPL_RUNTIME_PROVIDER: "local-docker",
      OPL_WORKSPACE_IMAGE: "ghcr.io/gaofeng21cn/one-person-lab-webui:latest",
      OPL_WORKSPACE_DOMAIN: "localhost",
      TENCENTCLOUD_SECRET_ID: "sid"
    },
    commandExists: (command) => command === "tofu"
  });

  assert.equal(report.ready, false);
  assert.deepEqual(report.missingTools, ["ansible-playbook", "tccli", "caddy"]);
  assert.ok(report.missingEnv.includes("DATABASE_URL"));
  assert.ok(report.missingEnv.includes("OPENMETER_ENDPOINT"));
  assert.ok(report.missingEnv.includes("OPENMETER_API_KEY"));
  assert.ok(report.missingEnv.includes("TENCENTCLOUD_SECRET_KEY"));
  assert.ok(report.failedChecks.includes("runtime_provider"));
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
