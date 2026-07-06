import assert from "node:assert/strict";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  loadEnvFile,
  requiredStagingLocalEnv,
  validateStagingLocalEnv
} from "../../tools/staging-env.ts";

test("staging env loader parses ignored local env files without overwriting explicit process env", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-staging-env-"));
  try {
    const envFile = join(root, ".env.staging.local");
    await writeFile(envFile, [
      "# local-to-staging config",
      "OPL_RUNTIME_PROVIDER=tencent-tke",
      "DATABASE_URL=postgresql://opl:secret@db.example.com:5432/opl_cloud",
      "OPL_WORKSPACE_DOMAIN=\"workspace.medopl.cn\"",
      "TENCENTCLOUD_REGION='ap-guangzhou'",
      "TENCENT_DEPLOY_CLUSTER_ID=cls-from-file",
      ""
    ].join("\n"));

    const env = loadEnvFile({
      filePath: envFile,
      baseEnv: {
        TENCENT_DEPLOY_CLUSTER_ID: "cls-explicit"
      }
    });

    assert.equal(env.OPL_RUNTIME_PROVIDER, "tencent-tke");
    assert.equal(env.DATABASE_URL, "postgresql://opl:secret@db.example.com:5432/opl_cloud");
    assert.equal(env.OPL_WORKSPACE_DOMAIN, "workspace.medopl.cn");
    assert.equal(env.TENCENTCLOUD_REGION, "ap-guangzhou");
    assert.equal(env.TENCENT_DEPLOY_CLUSTER_ID, "cls-explicit");
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("staging env validation requires Tencent mode, shared persistence, and cloud mutation settings", () => {
  const report = validateStagingLocalEnv({
    OPL_RUNTIME_PROVIDER: "local-docker",
    DATABASE_URL: "",
    OPL_WORKSPACE_DOMAIN: "localhost"
  });

  assert.equal(report.ready, false);
  assert.ok(report.missingEnv.includes("DATABASE_URL"));
  assert.ok(report.missingEnv.includes("OPL_TENCENT_PROVISIONER_BIN"));
  assert.ok(report.missingEnv.includes("TENCENT_CVM_SUBNET_ID"));
  assert.ok(report.missingEnv.includes("TENCENT_CVM_SECURITY_GROUP_IDS"));
  assert.ok(report.missingEnv.includes("OPL_BASIC_COMPUTE_INSTANCE_TYPE"));
  assert.ok(report.failedChecks.includes("runtime_provider"));
  assert.ok(report.failedChecks.includes("workspace_domain"));
  assert.equal(requiredStagingLocalEnv.includes("TENCENTCLOUD_SECRET_ID"), true);
});
