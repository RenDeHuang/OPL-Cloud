import assert from "node:assert/strict";
import test from "node:test";

import { validateProductionManifest } from "../../services/api/src/production-manifest.js";

test("production manifest requires deployment secret refs for every launch variable", () => {
  const report = validateProductionManifest({
    env: {
      OPL_RUNTIME_PROVIDER: { value: "tencent-cvm" },
      DATABASE_URL: { secretRef: "opl-cloud/database-url" },
      OPENMETER_ENDPOINT: { secretRef: "opl-cloud/openmeter-endpoint" },
      OPENMETER_API_KEY: { secretRef: "opl-cloud/openmeter-api-key" },
      TENCENTCLOUD_SECRET_ID: { secretRef: "opl-cloud/tencent-secret-id" },
      TENCENTCLOUD_SECRET_KEY: { secretRef: "opl-cloud/tencent-secret-key" },
      TENCENTCLOUD_REGION: { value: "ap-guangzhou" },
      OPL_HARBOR_REGISTRY: { value: "harbor.oplcloud.cn" },
      OPL_WORKSPACE_DOMAIN: { value: "workspaces.oplcloud.cn" },
      OPL_WORKSPACE_IMAGE: { value: "harbor.oplcloud.cn/opl/one-person-lab-webui:2026-07-01" },
      OPL_VPC_ID: { secretRef: "opl-cloud/vpc-id" },
      OPL_SUBNET_ID: { secretRef: "opl-cloud/subnet-id" },
      OPL_SECURITY_GROUP_ID: { secretRef: "opl-cloud/security-group-id" },
      OPL_AVAILABILITY_ZONE: { value: "ap-guangzhou-6" },
      OPL_IMAGE_ID: { secretRef: "opl-cloud/image-id" },
      OPL_SSH_KEY_ID: { secretRef: "opl-cloud/ssh-key-id" }
    }
  });

  assert.equal(report.ok, true);
  assert.deepEqual(report.missingEnv, []);
  assert.deepEqual(report.inlineSecretEnv, []);
  assert.deepEqual(report.checks.map((check) => `${check.id}:${check.ok}`), [
    "required_env:true",
    "secret_refs:true",
    "runtime_provider:true",
    "harbor_image:true",
    "workspace_domain:true"
  ]);
});

test("production manifest fails closed on missing env and inline secret values", () => {
  const report = validateProductionManifest({
    env: {
      OPL_RUNTIME_PROVIDER: { value: "local-docker" },
      DATABASE_URL: { value: "postgres://opl:secret@db.example.com:5432/opl_cloud" },
      OPENMETER_API_KEY: { value: "om_secret" },
      OPL_WORKSPACE_DOMAIN: { value: "localhost" },
      OPL_HARBOR_REGISTRY: { value: "harbor.oplcloud.cn" },
      OPL_WORKSPACE_IMAGE: { value: "registry.example.com/opl/one-person-lab-webui:latest" }
    }
  });

  assert.equal(report.ok, false);
  assert.ok(report.missingEnv.includes("OPENMETER_ENDPOINT"));
  assert.ok(report.missingEnv.includes("TENCENTCLOUD_SECRET_KEY"));
  assert.deepEqual(report.inlineSecretEnv.sort(), ["DATABASE_URL", "OPENMETER_API_KEY"]);
  assert.ok(report.failedChecks.includes("required_env"));
  assert.ok(report.failedChecks.includes("secret_refs"));
  assert.ok(report.failedChecks.includes("runtime_provider"));
  assert.ok(report.failedChecks.includes("harbor_image"));
  assert.ok(report.failedChecks.includes("workspace_domain"));
  assert.equal(JSON.stringify(report).includes("postgres://"), false);
  assert.equal(JSON.stringify(report).includes("om_secret"), false);
});
