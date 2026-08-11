import assert from "node:assert/strict";
import test from "node:test";

import { renderTkeManifest } from "../../tools/render-tke-manifest.ts";

function values(overrides = {}) {
  return {
    OPL_K8S_NAMESPACE: "opl-cloud",
    OPL_PUBLIC_URL: "https://cloud.example.com",
    OPL_CONSOLE_DOMAIN: "cloud.example.com",
    OPL_WORKSPACE_DOMAIN: "workspace.example.com",
    OPL_RELEASE_SHA: "a".repeat(40),
    OPL_CLOUD_IMAGE: `ghcr.io/example/opl-cloud@sha256:${"b".repeat(64)}`,
    OPL_WORKSPACE_IMAGE: `ghcr.io/example/opl-app@sha256:${"c".repeat(64)}`,
    OPL_IMAGE_PULL_SECRET_NAME: "registry-pull",
    OPL_WORKSPACE_STORAGE_CLASS: "cbs",
    OPL_TENCENT_PROVISIONER_BIN: "/usr/local/bin/opl-tencent-provisioner",
    OPL_TENCENT_ZONE: "ap-guangzhou-6",
    OPL_WORKSPACE_WEBUI_PORT: "3210",
    OPL_WORKSPACE_DATA_DIR: "/data",
    OPL_WORKSPACE_PROJECTS_DIR: "/projects",
    OPL_CLOUD_NODE_SELECTOR_KEY: "oplcloud.cn/role",
    OPL_CLOUD_NODE_SELECTOR_VALUE: "system",
    OPL_MONTHLY_BILLING_WORKER_ENABLED: "0",
    OPL_MONTHLY_BILLING_INTERVAL_MS: "60000",
    OPL_WORKSPACE_LAUNCH_WORKER_ENABLED: "0",
    OPL_WORKSPACE_LAUNCH_INTERVAL_MS: "5000",
    OPL_CONTROLLED_BASIC_PILOT_ENABLED: "0",
    OPL_CONTROLLED_BASIC_PILOT_ACCOUNT_IDS: "",
    OPL_CONTROLLED_BASIC_PILOT_MAX_IN_FLIGHT: "1",
    OPL_RECOVERY_ACCEPTANCE_CANARY_ENABLED: "0",
    OPL_RECOVERY_ACCEPTANCE_CANARY_ACCOUNT_IDS: "",
    OPL_SUB2API_BASE_URL: "https://gateway.example.com",
    OPL_SUB2API_REQUEST_TIMEOUT_MS: "5000",
    OPL_BASIC_COMPUTE_INSTANCE_TYPE: "basic-2c4g",
    OPL_PRO_COMPUTE_INSTANCE_TYPE: "pro-8c16g",
    OPL_SYSTEM_COMPUTE_NODE_POOL_ID: "np-system",
    OPL_SYSTEM_COMPUTE_MACHINE_ID: "machine-system",
    OPL_SYSTEM_COMPUTE_NODE_NAME: "10.0.0.10",
    OPL_SYSTEM_COMPUTE_MACHINE_TYPE: "Native",
    OPL_SYSTEM_COMPUTE_CVM_ID: "",
    OPL_BASIC_COMPUTE_NODE_POOL_ID: "np-basic",
    OPL_PRO_COMPUTE_NODE_POOL_ID: "np-pro",
    OPL_BASIC_COMPUTE_NODE_POOL_MAX_REPLICAS: "50",
    OPL_PRO_COMPUTE_NODE_POOL_MAX_REPLICAS: "50",
    OPL_CODEX_MODEL: "gpt-5",
    OPL_CODEX_REASONING_EFFORT: "medium",
    OPL_CONSOLE_TLS_SECRET_NAME: "console-tls",
    OPL_WORKSPACE_TLS_SECRET_NAME: "workspace-tls",
    OPL_INGRESS_CLASS: "qcloud",
    TENCENTCLOUD_REGION: "ap-guangzhou",
    TENCENT_DEPLOY_CLUSTER_ID: "cls-example",
    TENCENT_CVM_SUBNET_ID: "subnet-example",
    TENCENT_CVM_SECURITY_GROUP_IDS: "sg-example",
    TENCENT_CVM_SYSTEM_DISK_TYPE: "CLOUD_BSSD",
    TENCENT_CVM_SYSTEM_DISK_SIZE_GB: "50",
    RUN_TENCENT_CREATE_RELEASE_EXECUTION: "0",
    TENCENT_TCR_REGISTRY: "registry.example.com",
    TENCENT_TCR_NAMESPACE: "opl",
    TENCENT_TCR_REGION: "ap-guangzhou",
    TENCENT_DEPLOY_KUBECONFIG_REF: "kubeconfig-ref",
    ...overrides
  };
}

const manifest = {
  items: [
    { kind: "Namespace", metadata: { name: "template" } },
    {
      kind: "ConfigMap",
      metadata: { name: "opl-cloud-config" },
      data: { OPL_RELEASE_SHA: "template", OPL_TENCENT_ZONE: "template" }
    },
    {
      kind: "Deployment",
      metadata: { name: "opl-cloud-fabric" },
      spec: { template: { spec: { containers: [{ name: "fabric", image: "template" }] } } }
    },
    {
      kind: "Ingress",
      metadata: { name: "opl-cloud" },
      spec: { rules: [{ host: "console.template" }, { host: "workspace.template" }] }
    }
  ]
};

test("TKE adapter renderer applies instance values without mutating its template", () => {
  const rendered = renderTkeManifest({ manifest, values: values() });
  assert.equal(manifest.items[0].metadata.name, "template");
  assert.equal(rendered.items[0].metadata.name, "opl-cloud");
  assert.equal(rendered.items[1].metadata.namespace, "opl-cloud");
  assert.equal(rendered.items[1].data.OPL_RELEASE_SHA, "a".repeat(40));
  assert.equal(rendered.items[2].spec.template.spec.containers[0].image, values().OPL_CLOUD_IMAGE);
  assert.deepEqual(rendered.items[2].spec.template.spec.nodeSelector, { "oplcloud.cn/role": "system" });
  assert.equal(rendered.items[3].spec.rules[0].host, "cloud.example.com");
  assert.equal(rendered.items[3].spec.rules[1].host, "workspace.example.com");

  assert.equal(renderTkeManifest({ manifest, values: values(), skipSharedIngress: true }).items.some((item) => item.kind === "Ingress"), false);
  assert.throws(() => renderTkeManifest({ manifest, values: values({ OPL_RELEASE_SHA: "" }) }), /missing_tke_manifest_values:OPL_RELEASE_SHA/);
  assert.throws(() => renderTkeManifest({ manifest, values: values({ OPL_TENCENT_ZONE: "na-siliconvalley-1" }) }), /tencent_zone_region_mismatch/);
});
