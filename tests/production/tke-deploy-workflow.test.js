import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { renderTkeManifest } from "../../tools/render-tke-manifest.js";

test("TKE preproduction deploy workflow runs only on the VPC self-hosted runner", async () => {
  const workflow = await readFile(".github/workflows/deploy-tke-preproduction.yml", "utf8");

  assert.match(workflow, /workflow_dispatch:/);
  assert.match(workflow, /environment: production/);
  assert.match(workflow, /runs-on: \[self-hosted, tencent-cloud, opl-cloud, tke-vpc\]/);
  assert.match(workflow, /uses: actions\/setup-node@v4/);
  assert.match(workflow, /node-version: "22"/);
  assert.match(workflow, /OPL_CLOUD_IMAGE: \$\{\{ inputs\.cloud_image \}\}/);
  assert.match(workflow, /OPL_WORKSPACE_IMAGE: \$\{\{ inputs\.workspace_image \}\}/);
  assert.match(workflow, /TENCENT_DEPLOY_KUBECONFIG_B64: \$\{\{ secrets\.TENCENT_DEPLOY_KUBECONFIG_B64 \}\}/);
  assert.match(workflow, /TENCENT_DEPLOY_KUBECONFIG: \$\{\{ secrets\.TENCENT_DEPLOY_KUBECONFIG \}\}/);
  assert.match(workflow, /TENCENT_DEPLOY_KUBECONFIG_PATH: \$\{\{ vars\.TENCENT_DEPLOY_KUBECONFIG_PATH \|\| '\/home\/actions\/\.secrets\/medopl\/v22\/kubeconfig-package-d-deploy' \}\}/);
  assert.match(workflow, /tools\/render-tke-manifest\.js/);
  assert.match(workflow, /kubectl --kubeconfig "\$KUBECONFIG"/);
  assert.match(workflow, /rollout status deployment\/opl-cloud-control-plane/);
  assert.match(workflow, /get ingress opl-cloud/);
});

test("TKE preproduction deploy workflow installs secrets without command-line secret values", async () => {
  const workflow = await readFile(".github/workflows/deploy-tke-preproduction.yml", "utf8");

  assert.doesNotMatch(workflow, /--from-literal=DATABASE_URL/);
  assert.doesNotMatch(workflow, /--from-literal=OPENMETER_API_KEY/);
  assert.doesNotMatch(workflow, /--docker-password/);
  assert.match(workflow, /--from-file=DATABASE_URL="\$secret_dir\/database-url"/);
  assert.match(workflow, /--from-file=OPENMETER_API_KEY="\$secret_dir\/openmeter-api-key"/);
  assert.match(workflow, /--from-file=\.dockerconfigjson="\$docker_config"/);
  assert.match(workflow, /--from-file=kubeconfig="\$KUBECONFIG"/);
});

test("TKE manifest renderer replaces deploy-time values without rendering secrets", async () => {
  const source = await readFile("deploy/tke/opl-cloud.k8s.json", "utf8");
  const manifest = JSON.parse(source);
  const rendered = renderTkeManifest({
    manifest,
    values: {
      OPL_K8S_NAMESPACE: "opl-cloud",
      OPL_PUBLIC_URL: "https://cloud.medopl.cn",
      OPL_CONSOLE_DOMAIN: "cloud.medopl.cn",
      OPL_WORKSPACE_DOMAIN: "workspace.medopl.cn",
      OPL_CLOUD_IMAGE: "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud:test",
      OPL_WORKSPACE_IMAGE: "uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app:latest",
      OPL_IMAGE_PULL_SECRET_NAME: "tcr-pull-secret",
      OPL_WORKSPACE_STORAGE_CLASS: "cbs",
      OPL_TLS_SECRET_NAME: "opl-cloud-medopl-cn-tls",
      OPL_INGRESS_CLASS: "qcloud",
      OPENMETER_ENDPOINT: "http://openmeter.opl-cloud.svc.cluster.local:8888",
      TENCENT_DEPLOY_CLUSTER_ID: "cls-oplcloud",
      TENCENT_TCR_REGISTRY: "uswccr.ccs.tencentyun.com",
      TENCENT_TCR_NAMESPACE: "oplcloud",
      TENCENT_TCR_REGION: "na-siliconvalley",
      TENCENT_DEPLOY_KUBECONFIG_REF: "/var/run/opl-cloud/kubeconfig/kubeconfig"
    }
  });

  const text = JSON.stringify(rendered);
  assert.equal(text.includes("registry.example.com"), false);
  assert.equal(text.includes("cls-xxxxxxxx"), false);
  assert.equal(text.includes("postgresql://"), false);
  assert.equal(text.includes("OPENMETER_API_KEY"), true);

  const items = rendered.items;
  const namespace = items.find((item) => item.kind === "Namespace");
  const config = items.find((item) => item.kind === "ConfigMap");
  const deployment = items.find((item) => item.kind === "Deployment");
  const ingress = items.find((item) => item.kind === "Ingress");

  assert.equal(namespace.metadata.name, "opl-cloud");
  assert.equal(config.metadata.namespace, "opl-cloud");
  assert.equal(config.data.OPL_CLOUD_IMAGE, "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud:test");
  assert.equal(config.data.OPL_WORKSPACE_IMAGE, "uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app:latest");
  assert.equal(config.data.TENCENT_DEPLOY_CLUSTER_ID, "cls-oplcloud");
  assert.equal(config.data.TENCENT_TCR_REGISTRY, "uswccr.ccs.tencentyun.com");
  assert.equal(deployment.spec.template.spec.containers[0].image, "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud:test");
  assert.deepEqual(deployment.spec.template.spec.imagePullSecrets, [{ name: "tcr-pull-secret" }]);
  assert.equal(ingress.spec.ingressClassName, "qcloud");
  assert.equal(ingress.spec.tls[0].secretName, "opl-cloud-medopl-cn-tls");
  assert.deepEqual(ingress.spec.rules.map((rule) => rule.host), ["cloud.medopl.cn", "workspace.medopl.cn"]);
});
