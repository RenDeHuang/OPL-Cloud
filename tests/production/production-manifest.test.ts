import assert from "node:assert/strict";
import test from "node:test";

import { validateProductionManifest } from "../../services/control-plane/ops/production-manifest.ts";

const cloudImage = `registry.example.com/opl/opl-cloud@sha256:${"a".repeat(64)}`;
const workspaceImage = `registry.example.com/opl/one-person-lab-app@sha256:${"b".repeat(64)}`;

test("production manifest requires deployment secret refs for every launch variable", () => {
  const report = validateProductionManifest({
    env: {
      OPL_RUNTIME_PROVIDER: { value: "tencent-tke" },
      DATABASE_URL: { secretRef: "opl-cloud/database-url" },
      OPL_INTERNAL_SERVICE_TOKEN: { secretRef: "opl-cloud/internal-service-token" },
      OPL_CONSOLE_USERS_JSON: { secretRef: "opl-cloud/auth-users-json" },
      OPL_PUBLIC_URL: { value: "https://cloud.medopl.cn" },
      OPL_CONSOLE_DOMAIN: { value: "cloud.medopl.cn" },
      OPL_WORKSPACE_DOMAIN: { value: "workspace.medopl.cn" },
      OPL_CLOUD_IMAGE: { value: cloudImage },
      OPL_WORKSPACE_IMAGE: { value: workspaceImage },
      OPL_K8S_NAMESPACE: { value: "opl-cloud" },
      OPL_INGRESS_CLASS: { value: "qcloud" },
      OPL_IMAGE_PULL_SECRET_NAME: { value: "tcr-pull-secret" },
      OPL_WORKSPACE_STORAGE_CLASS: { value: "cbs" },
      OPL_TENCENT_ZONE: { value: "na-siliconvalley-1" },
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
      OPL_INTERNAL_SERVICE_TOKEN: { secretRef: "opl-cloud/internal-service-token" },
      OPL_CONSOLE_USERS_JSON: { secretRef: "opl-cloud/auth-users-json" },
      OPL_PUBLIC_URL: { value: "https://cloud.medopl.cn" },
      OPL_CONSOLE_DOMAIN: { value: "cloud.medopl.cn" },
      OPL_WORKSPACE_DOMAIN: { value: "workspace.medopl.cn" },
      OPL_CLOUD_IMAGE: { value: cloudImage },
      OPL_WORKSPACE_IMAGE: { value: workspaceImage },
      OPL_K8S_NAMESPACE: { value: "opl-cloud" },
      OPL_INGRESS_CLASS: { value: "qcloud" },
      OPL_IMAGE_PULL_SECRET_NAME: { value: "tcr-pull-secret" },
      OPL_WORKSPACE_STORAGE_CLASS: { value: "cbs" },
      OPL_TENCENT_ZONE: { value: "na-siliconvalley-1" },
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
  assert.ok(report.missingEnv.includes("OPL_INTERNAL_SERVICE_TOKEN"));
  assert.ok(report.missingEnv.includes("OPL_WORKSPACE_STORAGE_CLASS"));
  assert.ok(report.missingEnv.includes("OPL_TENCENT_ZONE"));
  assert.deepEqual(report.inlineSecretEnv.sort(), ["DATABASE_URL"]);
  assert.ok(report.failedChecks.includes("required_env"));
  assert.ok(report.failedChecks.includes("secret_refs"));
  assert.ok(report.failedChecks.includes("registry_images"));
  assert.ok(report.failedChecks.includes("workspace_domain"));
  assert.equal(JSON.stringify(report).includes("postgres://"), false);
  assert.equal(JSON.stringify(report).includes("TENCENTCLOUD_SECRET"), false);
});

test("production manifest treats an empty service-side launch zone as missing", () => {
  const report = validateProductionManifest({
    env: {
      OPL_RUNTIME_PROVIDER: { value: "tencent-tke" },
      OPL_TENCENT_ZONE: { value: "   " }
    }
  });

  assert.ok(report.missingEnv.includes("OPL_TENCENT_ZONE"));
});

test("production manifest rejects empty container image tags", () => {
  const report = validateProductionManifest({
    env: {
      OPL_RUNTIME_PROVIDER: { value: "tencent-tke" },
      DATABASE_URL: { secretRef: "opl-cloud/database-url" },
      OPL_CONSOLE_USERS_JSON: { secretRef: "opl-cloud/auth-users-json" },
      OPL_PUBLIC_URL: { value: "https://cloud.medopl.cn" },
      OPL_CONSOLE_DOMAIN: { value: "cloud.medopl.cn" },
      OPL_WORKSPACE_DOMAIN: { value: "workspace.medopl.cn" },
      OPL_CLOUD_IMAGE: { value: "registry.example.com/opl/opl-cloud:" },
      OPL_WORKSPACE_IMAGE: { value: "registry.example.com/opl/one-person-lab-app:" },
      OPL_K8S_NAMESPACE: { value: "opl-cloud" },
      OPL_INGRESS_CLASS: { value: "qcloud" },
      OPL_IMAGE_PULL_SECRET_NAME: { value: "tcr-pull-secret" },
      OPL_WORKSPACE_STORAGE_CLASS: { value: "cbs" },
      OPL_TENCENT_ZONE: { value: "na-siliconvalley-1" },
      TENCENT_DEPLOY_KUBECONFIG_REF: { secretRef: "opl-cloud/tencent-deploy-kubeconfig-ref" },
      TENCENT_DEPLOY_CLUSTER_ID: { value: "cls-123" },
      TENCENT_TCR_REGISTRY: { value: "registry.example.com" },
      TENCENT_TCR_NAMESPACE: { value: "opl" },
      TENCENT_TCR_REGION: { value: "ap-guangzhou" }
    }
  });

  assert.equal(report.ok, false);
  assert.ok(report.failedChecks.includes("registry_images"));
});

test("production manifest rejects latest and every tag-only production image", () => {
  for (const image of [
    "registry.example.com/opl/opl-cloud:latest",
    "registry.example.com/opl/opl-cloud:26.7.13"
  ]) {
    const report = validateProductionManifest({
      env: {
        OPL_RUNTIME_PROVIDER: { value: "tencent-tke" },
        DATABASE_URL: { secretRef: "opl-cloud/database-url" },
        OPL_INTERNAL_SERVICE_TOKEN: { secretRef: "opl-cloud/internal-service-token" },
        OPL_CONSOLE_USERS_JSON: { secretRef: "opl-cloud/auth-users-json" },
        OPL_PUBLIC_URL: { value: "https://cloud.medopl.cn" },
        OPL_CONSOLE_DOMAIN: { value: "cloud.medopl.cn" },
        OPL_WORKSPACE_DOMAIN: { value: "workspace.medopl.cn" },
        OPL_CLOUD_IMAGE: { value: image },
        OPL_WORKSPACE_IMAGE: { value: workspaceImage },
        OPL_K8S_NAMESPACE: { value: "opl-cloud" },
        OPL_INGRESS_CLASS: { value: "qcloud" },
        OPL_IMAGE_PULL_SECRET_NAME: { value: "tcr-pull-secret" },
        OPL_WORKSPACE_STORAGE_CLASS: { value: "cbs" },
        OPL_TENCENT_ZONE: { value: "na-siliconvalley-1" },
        TENCENT_DEPLOY_KUBECONFIG_REF: { secretRef: "opl-cloud/tencent-deploy-kubeconfig-ref" },
        TENCENT_DEPLOY_CLUSTER_ID: { value: "cls-123" },
        TENCENT_TCR_REGISTRY: { value: "registry.example.com" },
        TENCENT_TCR_NAMESPACE: { value: "opl" },
        TENCENT_TCR_REGION: { value: "ap-guangzhou" }
      }
    });

    assert.equal(report.ok, false);
    assert.ok(report.failedChecks.includes("registry_images"));
  }
});

test("production manifest rejects non-TKE production providers", () => {
  const report = validateProductionManifest({
    env: {
      OPL_RUNTIME_PROVIDER: { value: "unsupported-production-runtime" },
      DATABASE_URL: { secretRef: "opl-cloud/database-url" },
      OPL_WORKSPACE_DOMAIN: { value: "workspace.medopl.cn" },
      OPL_WORKSPACE_IMAGE: { value: workspaceImage }
    }
  });

  assert.equal(report.ok, false);
  assert.ok(report.failedChecks.includes("runtime_provider"));
});
