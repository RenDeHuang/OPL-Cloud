import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import test from "node:test";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";
import { parse } from "yaml";

import { renderTkeManifest } from "../../tools/render-tke-manifest.ts";

const repoFile = (path) => new URL(`../../${path}`, import.meta.url);
const deploymentContractPath = repoFile("packages/contracts/opl-cloud-deployment-contract.json");
const digestA = `sha256:${"a".repeat(64)}`;
const digestB = `sha256:${"b".repeat(64)}`;
const primaryWorkspaceSource = "ghcr.io/gaofeng21cn/one-person-lab-webui@sha256:9d867fe0fc9db48b6efa27371d77770e46fc8cd97d26ef85a81fbdac7e96ca76";
const fallbackWorkspaceSource = "ghcr.io/gaofeng21cn/one-person-lab-webui@sha256:6e1491a3693a820a37b81ab9a26f8efc4262fb9581f981641c6de084b0fa654f";
const fixedSlotDescriptor = {
  id: "verification-slot-01",
  customerProduct: false,
  instanceType: "SA5.MEDIUM4",
  chargeType: "PREPAID",
  periodMonths: 1,
  renewFlag: "NOTIFY_AND_MANUAL_RENEW",
  storageSizeGb: 10
};

async function readJson(path) {
  return JSON.parse(await readFile(path, "utf8"));
}

async function readWorkflow(path) {
  return parse(await readFile(repoFile(path), "utf8"));
}

function workflowJob(workflow, name) {
  const current = workflow.jobs?.[name];
  assert.ok(current, `workflow missing job ${name}`);
  return current;
}

function stepsByName(currentJob) {
  return new Map((currentJob.steps || []).map((step) => [step.name, step]));
}

function serializedStep(step) {
  return `${step?.run || ""}\n${JSON.stringify({ ...step, run: undefined })}`;
}

function serializedRuns(currentJob) {
  return (currentJob.steps || []).map((step) => step.run || "").join("\n");
}

function runImageMetadata(step, workspaceImageTag, workspaceSourceImage) {
  return spawnSync("bash", ["-c", step.run], {
    cwd: fileURLToPath(repoFile(".")),
    encoding: "utf8",
    env: {
      ...process.env,
      GITHUB_ENV: "/dev/null",
      GITHUB_OUTPUT: "/dev/null",
      OPL_CLOUD_IMAGE_REPOSITORY: "registry.example.test/opl/cloud",
      OPL_WORKSPACE_IMAGE_REPOSITORY: "registry.example.test/opl/workspace",
      REQUESTED_IMAGE_TAG: "cloud-test",
      REQUESTED_WORKSPACE_IMAGE_TAG: workspaceImageTag,
      REQUESTED_WORKSPACE_SOURCE_IMAGE: workspaceSourceImage
    }
  });
}

function assertWorkflowContract(workflow, spec, rootContract) {
  const currentJob = workflowJob(workflow, spec.job);
  assert.deepEqual([currentJob["runs-on"]].flat(), spec.runner || rootContract.runner);
  assert.equal(currentJob.environment, rootContract.environment);

  const workflowInputs = Object.keys(workflow.on?.workflow_dispatch?.inputs || {});
  for (const input of spec.inputs || []) assert.ok(workflowInputs.includes(input), `${spec.file} missing input ${input}`);

  const stepMap = stepsByName(currentJob);
  assert.deepEqual([...stepMap.keys()], spec.steps);
  for (const key of spec.requiredEnv || []) {
    assert.ok(Object.hasOwn(currentJob.env || {}, key), `${spec.file} missing env ${key}`);
  }
  for (const key of spec.secretEnv || []) {
    assert.ok(String(currentJob.env?.[key] || "").includes("secrets."), `${key} must come from GitHub secrets`);
  }
  for (const [stepName, tokens] of Object.entries(spec.requiredCommandsByStep || {})) {
    const text = serializedStep(stepMap.get(stepName));
    for (const token of tokens) assert.ok(text.includes(token), `${spec.file} ${stepName} missing ${token}`);
  }

  const text = JSON.stringify(workflow);
  for (const token of spec.forbiddenRunTokens || []) assert.equal(text.includes(token), false, `${spec.file} contains ${token}`);
}

async function manifestFixture() {
  const manifest = await readJson(repoFile("deploy/tke/opl-cloud.k8s.json"));
  const config = manifest.items.find((item) => item.kind === "ConfigMap");
  return {
    manifest,
    values: {
      ...config.data,
      OPL_K8S_NAMESPACE: "opl-test",
      OPL_PUBLIC_URL: "https://console.example.test",
      OPL_CONSOLE_DOMAIN: "console.example.test",
      OPL_WORKSPACE_DOMAIN: "workspace.example.test",
      OPL_CLOUD_IMAGE: `registry.example.test/opl/cloud@${digestA}`,
      OPL_WORKSPACE_IMAGE: `registry.example.test/opl/workspace@${digestB}`,
      OPL_IMAGE_PULL_SECRET_NAME: "pull-test",
      OPL_TENCENT_ZONE: "na-siliconvalley-1",
      OPL_SUB2API_BASE_URL: "https://wallet.example.test",
      OPL_SUB2API_SUPPORTED_VERSIONS: "0.1.155",
      OPL_SUB2API_REQUEST_TIMEOUT_MS: "7000",
      OPL_MONTHLY_BILLING_WORKER_ENABLED: "1",
      OPL_MONTHLY_BILLING_INTERVAL_MS: "60000"
    }
  };
}

test("TKE deploy workflow matches the current deployment contract", async () => {
  const contract = await readJson(deploymentContractPath);
  assertWorkflowContract(await readWorkflow(contract.deployWorkflow.file), contract.deployWorkflow, contract);
  assert.ok(contract.deployWorkflow.requiredEnv.includes("OPL_TENCENT_ZONE"));
  assert.equal(contract.productionVerificationWorkflow.launchStatus, "blocked");
  assert.equal(contract.productionVerificationWorkflow.replacement, "reusable_prepaid_verification_slot");
});

test("production verification is read only and fixed to the reusable prepaid slot", async () => {
  const workflow = await readWorkflow(".github/workflows/verify-production-chain.yml");
  assert.deepEqual(Object.keys(workflow.jobs), ["verify"]);
  const currentJob = workflowJob(workflow, "verify");
  const runs = serializedRuns(currentJob);
  const inputs = Object.keys(workflow.on.workflow_dispatch.inputs || {});

  assert.equal(workflow.concurrency.group, "production-resource-verification");
  assert.equal(workflow.concurrency["cancel-in-progress"], false);
  assert.equal(inputs.includes("paid_confirmation"), false);
  assert.equal(Object.hasOwn(currentJob.env, "OPL_VERIFY_PAID_CONFIRMATION"), false);
  assert.equal(Object.hasOwn(currentJob.env, "OPL_VERIFY_MODEL_ACCESS_KEY"), false);
  assert.equal(currentJob.env.OPL_VERIFY_AUTH_USERS_JSON, "${{ secrets.OPL_VERIFY_AUTH_USERS_JSON || secrets.OPL_CONSOLE_USERS_JSON }}");
  assert.equal(currentJob.env.OPL_VERIFY_SLOT_ID, "verification-slot-01");
  assert.deepEqual(JSON.parse(currentJob.env.OPL_VERIFY_SLOT_DESCRIPTOR_JSON), fixedSlotDescriptor);
  assert.match(runs, /node tools\/production-verifier\.ts --browser-e2e/);
  assert.doesNotMatch(runs, /paid.confirmation|compute-allocations|storage-volumes|destroy|detach/i);

  const verifier = await readFile(repoFile("tools/production-verifier.ts"), "utf8");
  assert.doesNotMatch(verifier, /cleanupVerificationResources|productionVerificationMutationKey|paid_confirmation_required|I_UNDERSTAND_THIS_SPENDS_REAL_BALANCE/);
});

test("TKE deploy runs separately gated real Workspace QA only after rollout", async () => {
  const deployWorkflow = await readWorkflow(".github/workflows/deploy-tke-production.yml");
  const liveQa = workflowJob(deployWorkflow, "live-qa");
  const runs = serializedRuns(liveQa);
  const readOnlyWorkflow = JSON.stringify(await readWorkflow(".github/workflows/verify-production-chain.yml"));

  assert.equal(liveQa.needs, "deploy");
  assert.equal(liveQa.environment, "production");
  assert.deepEqual([liveQa["runs-on"]].flat(), ["ubuntu-latest"]);
  assert.equal(liveQa.env.OPL_VERIFY_AUTH_USERS_JSON, "${{ secrets.OPL_VERIFY_AUTH_USERS_JSON || secrets.OPL_CONSOLE_USERS_JSON }}");
  assert.equal(liveQa.env.OPL_VERIFY_SLOT_ID, "verification-slot-01");
  assert.deepEqual(JSON.parse(liveQa.env.OPL_VERIFY_SLOT_DESCRIPTOR_JSON), fixedSlotDescriptor);
  assert.notEqual(liveQa.env.OPL_VERIFY_PURCHASE_BUDGET_REMAINING, "0");
  assert.match(String(liveQa.env.OPL_VERIFY_PURCHASE_BUDGET_REMAINING), /OPL_VERIFY_PURCHASE_BUDGET_REMAINING|'1'/);
  assert.equal(liveQa.env.OPL_VERIFY_LIVE_QA_CONFIRMATION, "I_UNDERSTAND_THIS_SENDS_ONE_REAL_MODEL_REQUEST");
  assert.equal(Object.hasOwn(liveQa.env, "OPL_VERIFY_MODEL_ACCESS_KEY"), false);
  assert.match(runs, /npm ci/);
  assert.match(runs, /playwright install --with-deps chromium/);
  assert.match(runs, /node tools\/production-live-qa\.ts/);
  assert.doesNotMatch(runs, /compute-allocations|storage-volumes|destroy|detach|renew/i);
  assert.doesNotMatch(readOnlyWorkflow, /production-live-qa|LIVE_QA_CONFIRMATION/);
});

test("image release pins and verifies both source and target digests", async () => {
  const workflow = await readWorkflow(".github/workflows/release-opl-cloud-image.yml");
  const currentJob = workflowJob(workflow, "build-push");
  const metadata = serializedStep(stepsByName(currentJob).get("Image metadata"));
  const source = JSON.stringify(workflow);
  const runs = serializedRuns(currentJob);

  assert.doesNotMatch(metadata, /\$\{\{\s*inputs\./);
  for (const value of [primaryWorkspaceSource, fallbackWorkspaceSource, "26.7.13", "26.7.12"]) {
    assert.match(source, new RegExp(value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  }
  assert.match(runs, /docker buildx imagetools inspect/);
  assert.match(runs, /docker buildx imagetools create --prefer-index=false/);
  assert.match(runs, /sha256:\[0-9a-f\]\{64\}/);
  assert.match(runs, /OPL_CLOUD_IMAGE=.*@\$\{cloud_digest\}/);
  assert.match(runs, /OPL_WORKSPACE_IMAGE=.*@\$\{workspace_digest\}/);
  assert.match(runs, /workspace_digest.*\$\{WORKSPACE_SOURCE_IMAGE##\*@\}/s);
  assert.doesNotMatch(runs, /:latest\b|:stable\b/);
});

test("image release accepts only the exact primary and fallback tag-digest pairs", async () => {
  const workflow = await readWorkflow(".github/workflows/release-opl-cloud-image.yml");
  const metadata = stepsByName(workflowJob(workflow, "build-push")).get("Image metadata");

  for (const [tag, source] of [["26.7.13", primaryWorkspaceSource], ["26.7.12", fallbackWorkspaceSource]]) {
    const result = runImageMetadata(metadata, tag, source);
    assert.equal(result.status, 0, result.stderr);
  }
  for (const [tag, source] of [
    ["26.7.13", fallbackWorkspaceSource],
    ["26.7.12", primaryWorkspaceSource],
    ["latest", primaryWorkspaceSource],
    ["stable", primaryWorkspaceSource],
    ["26.7.11", primaryWorkspaceSource]
  ]) {
    const result = runImageMetadata(metadata, tag, source);
    assert.notEqual(result.status, 0, `${tag} must not accept ${source}`);
  }
});

test("TKE deploy installs Sub2API credentials and validates account mappings", async () => {
  const workflow = await readWorkflow(".github/workflows/deploy-tke-production.yml");
  const currentJob = workflowJob(workflow, "deploy");
  const steps = stepsByName(currentJob);
  const prepare = serializedStep(steps.get("Prepare kubeconfig"));
  const install = serializedStep(steps.get("Install Kubernetes secrets"));
  const cleanup = steps.get("Remove deployment secrets");

  assert.match(install, /create secret generic opl-cloud-sub2api/);
  assert.match(install, /--from-file=OPL_SUB2API_ADMIN_EMAIL/);
  assert.match(install, /--from-file=OPL_SUB2API_ADMIN_PASSWORD/);
  assert.match(install, /Number\.isSafeInteger\(user\.sub2apiUserId\)/);
  assert.match(install, /user\.sub2apiUserId > 0/);
  assert.equal(currentJob.env.OPL_SUB2API_SUPPORTED_VERSIONS.includes("0.1.155"), true);
  assert.equal(currentJob.env.OPL_TENCENT_ZONE, "${{ vars.OPL_TENCENT_ZONE || 'na-siliconvalley-1' }}");
  assert.equal(Object.hasOwn(currentJob.env, "OPL_CODEX_API_KEY"), false);
  assert.doesNotMatch(install, /OPL_CODEX_API_KEY|opl-cloud-workspace-codex/);
  assert.doesNotMatch(install, /console\.log\([^)]*(?:password|auth-users-json)/i);
  assert.equal(cleanup?.if, "always()");
  assert.match(serializedStep(cleanup), /find "\$secret_dir" -mindepth 1 -delete/);
  assert.match(serializedStep(cleanup), /"\$RUNNER_TEMP"\/\*\|\/tmp\/\*/);
  assert.ok(
    prepare.indexOf('echo "OPL_DEPLOY_SECRET_DIR=$secret_dir" >> "$GITHUB_ENV"') < prepare.indexOf('if [ -f "$TENCENT_DEPLOY_KUBECONFIG_PATH" ]'),
    "the cleanup path must be exported before kubeconfig preparation can fail"
  );
});

test("deployment inputs contain monthly and Sub2API config without retired billing env", async () => {
  const sources = await Promise.all([
    readFile(repoFile(".github/workflows/deploy-tke-production.yml"), "utf8"),
    readFile(deploymentContractPath, "utf8"),
    readFile(repoFile("tools/render-tke-manifest.ts"), "utf8"),
    readFile(repoFile("deploy/tke/opl-cloud.k8s.json"), "utf8")
  ]);
  const joined = sources.join("\n");

  for (const key of [
    "OPL_MONTHLY_BILLING_WORKER_ENABLED",
    "OPL_MONTHLY_BILLING_INTERVAL_MS",
    "OPL_SUB2API_BASE_URL",
    "OPL_SUB2API_SUPPORTED_VERSIONS",
    "OPL_SUB2API_REQUEST_TIMEOUT_MS"
  ]) assert.match(joined, new RegExp(key));
  assert.match(joined, /OPL_TENCENT_ZONE/);
  assert.doesNotMatch(joined, /OPL_(?:BASIC|PRO)_COMPUTE_HOURLY_CNY|OPL_STORAGE_GB_MONTH_CNY|OPL_RESOURCE_BILLING_/);
});

test("TKE manifest renderer replaces current values and never renders secrets", async () => {
  const { manifest, values } = await manifestFixture();
  const rendered = renderTkeManifest({ manifest, values });
  const source = JSON.stringify(rendered);
  const config = rendered.items.find((item) => item.kind === "ConfigMap");

  assert.equal(rendered.items[0].metadata.name, "opl-test");
  assert.equal(config.data.OPL_CLOUD_IMAGE, values.OPL_CLOUD_IMAGE);
  assert.equal(config.data.OPL_SUB2API_BASE_URL, values.OPL_SUB2API_BASE_URL);
  assert.equal(config.data.OPL_SUB2API_REQUEST_TIMEOUT_MS, "7000");
  assert.equal(config.data.OPL_TENCENT_ZONE, "na-siliconvalley-1");
  assert.equal(config.data.OPL_MONTHLY_BILLING_INTERVAL_MS, "60000");
  assert.doesNotMatch(source, /postgresql:\/\//i);
  const controlPlane = rendered.items.find((item) => item.kind === "Deployment" && item.metadata.name === "opl-cloud-control-plane");
  assert.deepEqual(controlPlane.spec.template.spec.containers[0].envFrom, [{ configMapRef: { name: "opl-cloud-config" } }]);
  const sub2apiEnv = controlPlane.spec.template.spec.containers[0].env.filter((item) => item.name.startsWith("OPL_SUB2API_ADMIN_"));
  assert.equal(sub2apiEnv.length, 2);
  assert.equal(sub2apiEnv.every((item) => item.valueFrom?.secretKeyRef && item.value === undefined), true);

  for (const deployment of rendered.items.filter((item) => item.kind === "Deployment")) {
    assert.deepEqual(deployment.spec.template.spec.imagePullSecrets, [{ name: "pull-test" }]);
  }
});

test("TKE manifest renderer can leave shared Ingress ownership untouched", async () => {
  const { manifest, values } = await manifestFixture();
  const rendered = renderTkeManifest({ manifest, values, skipSharedIngress: true });
  assert.equal(rendered.items.some((item) => item.kind === "Ingress" && item.metadata?.name === "opl-cloud"), false);
});

test("TKE deploy requires image digests and rolls back the complete Cloud and App image set", async () => {
  const workflow = await readWorkflow(".github/workflows/deploy-tke-production.yml");
  const currentJob = workflowJob(workflow, "deploy");
  const inputs = Object.keys(workflow.on.workflow_dispatch.inputs || {});
  const checks = serializedStep(stepsByName(currentJob).get("Check deployment inputs"));
  const apply = serializedStep(stepsByName(currentJob).get("Render and apply manifest"));

  assert.equal(inputs.includes("exercise_rollback"), true);
  assert.match(String(currentJob.env.OPL_EXERCISE_ROLLBACK), /inputs\.exercise_rollback/);
  assert.match(checks, /repository@sha256/);
  assert.match(checks, /OPL_TENCENT_ZONE/);
  assert.match(checks, /sha256:\[0-9a-f\]\{64\}/);
  assert.doesNotMatch(checks, /must include a non-empty container tag/);
  for (const deployment of ["opl-cloud-control-plane", "opl-cloud-ledger", "opl-cloud-fabric"]) {
    assert.match(apply, new RegExp(deployment));
  }
  assert.match(apply, /previous.*OPL_WORKSPACE_IMAGE/is);
  assert.match(apply, /get deployment -l ['"]oplcloud\.cn\/workspace-id['"] -o json/);
  assert.match(apply, /workspace-images\.tsv/);
  assert.match(apply, /container\.name === "workspace"/);
  assert.match(apply, /apply_candidate_images\(\)/);
  assert.match(apply, /restore_previous_images\(\)/);
  assert.match(apply, /\$container=\$OPL_WORKSPACE_IMAGE/);
  assert.match(apply, /\$container=\$previous_image/);
  assert.match(apply, /rollout status "deployment\/\$deployment" --timeout=300s/);
  assert.match(apply, /if \[ "\$OPL_EXERCISE_ROLLBACK" = "true" \]/);
  assert.match(apply, /restore_previous_images[\s\S]*apply_candidate_images/);
  assert.match(apply, /kubectl[^\n]*set image/);
  assert.match(apply, /kubectl[^\n]*patch configmap opl-cloud-config/);
  assert.match(apply, /JSON\.stringify\(\{ data: \{ OPL_WORKSPACE_IMAGE:/);
  assert.match(apply, /trap .*rollback.* ERR/);
  assert.match(apply, /rollout restart/);
  assert.match(apply, /rollout status/);
});

test("TKE rollback functions restore, read back, and reapply every Cloud and App image", async () => {
  const workflow = await readWorkflow(".github/workflows/deploy-tke-production.yml");
  const apply = stepsByName(workflowJob(workflow, "deploy")).get("Render and apply manifest").run;
  const functionStart = apply.indexOf("patch_workspace_image() {");
  const functionEnd = apply.indexOf("\ntrap rollback_images ERR");
  assert.ok(functionStart >= 0 && functionEnd > functionStart);
  const functions = apply.slice(functionStart, functionEnd);
  const root = await mkdtemp(join(tmpdir(), "opl-rollback-test-"));
  const rollbackDir = join(root, "previous-images");
  const oldCloud = `registry.example.test/opl/cloud@sha256:${"a".repeat(64)}`;
  const candidateCloud = `registry.example.test/opl/cloud@sha256:${"b".repeat(64)}`;
  const oldWorkspace = `registry.example.test/opl/workspace@sha256:${"c".repeat(64)}`;
  const candidateWorkspace = `registry.example.test/opl/workspace@sha256:${"d".repeat(64)}`;

  try {
    await mkdir(rollbackDir);
    await Promise.all([
      ...["opl-cloud-control-plane", "opl-cloud-ledger", "opl-cloud-fabric"].map((name) => writeFile(join(rollbackDir, name), oldCloud)),
      writeFile(join(rollbackDir, "OPL_WORKSPACE_IMAGE"), oldWorkspace),
      writeFile(join(rollbackDir, "workspace-images.tsv"), `workspace-slot-1\tworkspace\t${oldWorkspace}\n`)
    ]);
    const harness = `
      set -Eeuo pipefail
      rollback_dir="$TEST_ROOT/previous-images"
      workspace_images="$rollback_dir/workspace-images.tsv"
      config_image="$OPL_WORKSPACE_IMAGE"
      declare -A images=(
        [opl-cloud-control-plane]="$OPL_CLOUD_IMAGE"
        [opl-cloud-ledger]="$OPL_CLOUD_IMAGE"
        [opl-cloud-fabric]="$OPL_CLOUD_IMAGE"
        [workspace-slot-1]="$OPL_WORKSPACE_IMAGE"
      )
      : > "$TEST_ROOT/kubectl.log"
      kubectl() {
        local command="" target="" assignment="" arg last
        printf '%s ' "$@" >> "$TEST_ROOT/kubectl.log"
        printf '\n' >> "$TEST_ROOT/kubectl.log"
        for arg in "$@"; do
          case "$arg" in
            get|patch|set|rollout) command="$arg" ;;
            deployment/*) target="\${arg#deployment/}" ;;
            *=*) assignment="$arg" ;;
          esac
        done
        case "$command" in
          get) printf '%s' "\${images[$target]}" ;;
          patch)
            last="\${!#}"
            config_image="$(node -e 'process.stdout.write(JSON.parse(process.argv[1]).data.OPL_WORKSPACE_IMAGE)' "$last")"
            ;;
          set) images[$target]="\${assignment#*=}" ;;
          rollout) ;;
        esac
      }
${functions}
      restore_previous_images
      printf '%s\n' "$config_image" "\${images[opl-cloud-control-plane]}" "\${images[opl-cloud-ledger]}" "\${images[opl-cloud-fabric]}" "\${images[workspace-slot-1]}" > "$TEST_ROOT/restored.txt"
      apply_candidate_images
      printf '%s\n' "$config_image" "\${images[opl-cloud-control-plane]}" "\${images[opl-cloud-ledger]}" "\${images[opl-cloud-fabric]}" "\${images[workspace-slot-1]}" > "$TEST_ROOT/candidate.txt"
    `;
    const result = spawnSync("bash", ["-c", harness], {
      cwd: fileURLToPath(repoFile(".")),
      encoding: "utf8",
      env: {
        ...process.env,
        KUBECONFIG: "/dev/null",
        OPL_CLOUD_IMAGE: candidateCloud,
        OPL_K8S_NAMESPACE: "opl-test",
        OPL_WORKSPACE_IMAGE: candidateWorkspace,
        TEST_ROOT: root
      }
    });
    assert.equal(result.status, 0, result.stderr);
    assert.deepEqual((await readFile(join(root, "restored.txt"), "utf8")).trim().split("\n"), [oldWorkspace, oldCloud, oldCloud, oldCloud, oldWorkspace]);
    assert.deepEqual((await readFile(join(root, "candidate.txt"), "utf8")).trim().split("\n"), [candidateWorkspace, candidateCloud, candidateCloud, candidateCloud, candidateWorkspace]);

    const log = await readFile(join(root, "kubectl.log"), "utf8");
    for (const deployment of ["opl-cloud-control-plane", "opl-cloud-ledger", "opl-cloud-fabric", "workspace-slot-1"]) {
      assert.equal(log.match(new RegExp(`get deployment/${deployment}`, "g"))?.length, 2, `${deployment} must be read back after restore and reapply`);
    }
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
