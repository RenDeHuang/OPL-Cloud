import assert from "node:assert/strict";
import test from "node:test";

import { validateProductionManifest } from "../../packages/console/src/production-manifest.js";

test("production manifest requires deployment secret refs for every launch variable", () => {
  const report = validateProductionManifest({
    env: {
      OPL_RUNTIME_PROVIDER: { value: "tencent-tke" },
      DATABASE_URL: { secretRef: "opl-cloud/database-url" },
      OPL_CONSOLE_USERS_JSON: { secretRef: "opl-cloud/auth-users-json" },
      OPL_PUBLIC_URL: { value: "https://cloud.medopl.cn" },
      OPL_CONSOLE_DOMAIN: { value: "cloud.medopl.cn" },
      OPL_WORKSPACE_DOMAIN: { value: "workspace.medopl.cn" },
      OPL_CLOUD_IMAGE: { value: "registry.example.com/opl/opl-cloud:2026-07-01" },
      OPL_WORKSPACE_IMAGE: { value: "registry.example.com/opl/one-person-lab-app:2026-07-01" },
      OPL_K8S_NAMESPACE: { value: "opl-cloud" },
      OPL_INGRESS_CLASS: { value: "qcloud" },
      OPL_IMAGE_PULL_SECRET_NAME: { value: "tcr-pull-secret" },
      OPL_WORKSPACE_STORAGE_CLASS: { value: "cbs" },
      TENCENT_DEPLOY_KUBECONFIG_REF: { secretRef: "opl-cloud/tencent-deploy-kubeconfig-ref" },
      TENCENT_DEPLOY_CLUSTER_ID: { value: "cls-123" },
      TENCENT_TCR_REGISTRY: { value: "registry.example.com" },
      TENCENT_TCR_NAMESPACE: { value: "opl" },
      TENCENT_TCR_REGION: { value: "ap-guangzhou" }
    }
  });

  assert.equal(report.ok, true);
  assert.deepEqual(report.missingEnv, []);
  assert.deepEqual(report.inlineSecretEnv, []);
  assert.deepEqual(report.checks.map((check) => `${check.id}:${check.ok}`), [
    "required_env:true",
    "secret_refs:true",
    "runtime_provider:true",
    "registry_images:true",
    "workspace_domain:true"
  ]);
});

test("production manifest validates Tencent TKE fields only", () => {
  const report = validateProductionManifest({
    env: {
      OPL_RUNTIME_PROVIDER: { value: "tencent-tke" },
      DATABASE_URL: { secretRef: "opl-cloud/database-url" },
      OPL_CONSOLE_USERS_JSON: { secretRef: "opl-cloud/auth-users-json" },
      OPL_PUBLIC_URL: { value: "https://cloud.medopl.cn" },
      OPL_CONSOLE_DOMAIN: { value: "cloud.medopl.cn" },
      OPL_WORKSPACE_DOMAIN: { value: "workspace.medopl.cn" },
      OPL_CLOUD_IMAGE: { value: "registry.example.com/opl/opl-cloud:2026-07-01" },
      OPL_WORKSPACE_IMAGE: { value: "registry.example.com/opl/one-person-lab-app:2026-07-01" },
      OPL_K8S_NAMESPACE: { value: "opl-cloud" },
      OPL_INGRESS_CLASS: { value: "qcloud" },
      OPL_IMAGE_PULL_SECRET_NAME: { value: "tcr-pull-secret" },
      OPL_WORKSPACE_STORAGE_CLASS: { value: "cbs" },
      TENCENT_DEPLOY_KUBECONFIG_REF: { secretRef: "opl-cloud/tencent-deploy-kubeconfig-ref" },
      TENCENT_DEPLOY_CLUSTER_ID: { value: "cls-123" },
      TENCENT_TCR_REGISTRY: { value: "registry.example.com" },
      TENCENT_TCR_NAMESPACE: { value: "opl" },
      TENCENT_TCR_REGION: { value: "ap-guangzhou" }
    }
  });

  assert.equal(report.ok, true);
  assert.deepEqual(report.missingEnv, []);
  assert.deepEqual(report.inlineSecretEnv, []);
  assert.deepEqual(report.checks.map((check) => `${check.id}:${check.ok}`), [
    "required_env:true",
    "secret_refs:true",
    "runtime_provider:true",
    "registry_images:true",
    "workspace_domain:true"
  ]);
});

test("production manifest fails closed on missing env and inline secret values", () => {
  const report = validateProductionManifest({
    env: {
      OPL_RUNTIME_PROVIDER: { value: "tencent-tke" },
      DATABASE_URL: { value: "postgres://opl:secret@db.example.com:5432/opl_cloud" },
      OPL_WORKSPACE_DOMAIN: { value: "localhost" },
      OPL_WORKSPACE_IMAGE: { value: "registry.example.com/opl/one-person-lab-app:latest" }
    }
  });

  assert.equal(report.ok, false);
  assert.ok(report.missingEnv.includes("OPL_CLOUD_IMAGE"));
  assert.ok(report.missingEnv.includes("OPL_CONSOLE_USERS_JSON"));
  assert.ok(report.missingEnv.includes("OPL_WORKSPACE_STORAGE_CLASS"));
  assert.deepEqual(report.inlineSecretEnv.sort(), ["DATABASE_URL"]);
  assert.ok(report.failedChecks.includes("required_env"));
  assert.ok(report.failedChecks.includes("secret_refs"));
  assert.ok(report.failedChecks.includes("registry_images"));
  assert.ok(report.failedChecks.includes("workspace_domain"));
  assert.equal(JSON.stringify(report).includes("postgres://"), false);
  assert.equal(JSON.stringify(report).includes("TENCENTCLOUD_SECRET"), false);
});

test("production manifest rejects non-TKE production providers", () => {
  const report = validateProductionManifest({
    env: {
      OPL_RUNTIME_PROVIDER: { value: "unsupported-production-runtime" },
      DATABASE_URL: { secretRef: "opl-cloud/database-url" },
      OPL_WORKSPACE_DOMAIN: { value: "workspace.medopl.cn" },
      OPL_WORKSPACE_IMAGE: { value: "registry.example.com/opl/one-person-lab-app:2026-07-01" }
    }
  });

  assert.equal(report.ok, false);
  assert.ok(report.failedChecks.includes("runtime_provider"));
});
