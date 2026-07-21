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
const cloudCandidateSha = "c".repeat(40);
const cloudMainSha = "d".repeat(40);
const workspaceAppSha = "a".repeat(40);
const workspaceShellSha = "b".repeat(40);
const workspaceFrameworkSha = "e".repeat(40);
const workspaceImageTag = `${workspaceAppSha.slice(0, 12)}-${workspaceShellSha.slice(0, 12)}-${workspaceFrameworkSha.slice(0, 12)}`;
const workspaceDigest = `sha256:${"d".repeat(64)}`;
const basicSlotDescriptor = {
  id: "verification-slot-basic-01",
  customerProduct: false,
  instanceType: "SA5.MEDIUM4",
  server: "2c4g",
  cpu: 2,
  memoryGb: 4,
  cbsGb: 10,
  chargeType: "PREPAID",
  periodMonths: 1,
  renewFlag: "NOTIFY_AND_MANUAL_RENEW"
};
const proSlotDescriptor = {
  id: "verification-slot-pro-01",
  customerProduct: false,
  instanceType: "SA5.2XLARGE16",
  server: "8c16g",
  cpu: 8,
  memoryGb: 16,
  cbsGb: 100,
  chargeType: "PREPAID",
  periodMonths: 1,
  renewFlag: "NOTIFY_AND_MANUAL_RENEW"
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

function runImageMetadata(step, appSha, shellSha, frameworkSha) {
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
      REQUESTED_WORKSPACE_APP_SHA: appSha,
      REQUESTED_WORKSPACE_SHELL_SHA: shellSha,
      REQUESTED_WORKSPACE_FRAMEWORK_SHA: frameworkSha,
      PUBLISH_CLOUD_IMAGE: "false",
      PUBLISH_WORKSPACE_IMAGE: "true"
    }
  });
}

function runCloudSourceGate(step, requestedSha, {
  headSha = requestedSha,
  mainSha = cloudMainSha,
  merged = true
} = {}) {
  const harness = `
git() {
  printf 'git %s\\n' "$*" >&2
  case "$*" in
    "rev-parse HEAD") printf '%s\\n' "$CLOUD_HEAD_SHA" ;;
    "fetch --no-tags https://github.com/$OPL_CLOUD_SOURCE_REPOSITORY.git main:refs/remotes/release-source/main") ;;
    "rev-parse refs/remotes/release-source/main") printf '%s\\n' "$CLOUD_MAIN_SHA" ;;
    "merge-base --is-ancestor $CLOUD_HEAD_SHA $CLOUD_MAIN_SHA") [ "$CLOUD_CANDIDATE_MERGED" = "true" ] ;;
    *) return 2 ;;
  esac
}
${step.run}
`;
  return spawnSync("bash", ["-c", harness], {
    cwd: fileURLToPath(repoFile(".")),
    encoding: "utf8",
    env: {
      ...process.env,
      GITHUB_ENV: "/dev/null",
      REQUESTED_CLOUD_SHA: requestedSha,
      OPL_CLOUD_SOURCE_REPOSITORY: "RenDeHuang/OPL-Cloud",
      CLOUD_HEAD_SHA: headSha,
      CLOUD_MAIN_SHA: mainSha,
      CLOUD_CANDIDATE_MERGED: merged ? "true" : "false"
    }
  });
}

async function runImageReleaseStep(step, publishCloudImage, publishWorkspaceImage) {
  const root = await mkdtemp(join(tmpdir(), "opl-image-release-"));
  const commandLog = join(root, "commands.log");
  const githubOutput = join(root, "output");
  const githubEnv = join(root, "env");
  const githubSummary = join(root, "summary");
  await Promise.all([commandLog, githubOutput, githubEnv, githubSummary].map((path) => writeFile(path, "")));
  const cloudDigest = `sha256:${"c".repeat(64)}`;
  const harness = `
docker() {
  printf 'docker %s\\n' "$*" >> "$COMMAND_LOG"
  case "$*" in
    *"--password-stdin"*) command cat >/dev/null ;;
  esac
  case "$*" in
    *"imagetools inspect $OPL_CLOUD_IMAGE_REF"*) printf '%s\\n' "$CLOUD_DIGEST" ;;
    *"imagetools inspect "*) printf '%s\\n' "$WORKSPACE_DIGEST" ;;
  esac
}
${step.run}
`;
  const result = spawnSync("bash", ["-c", harness], {
    cwd: fileURLToPath(repoFile(".")),
    encoding: "utf8",
    env: {
      ...process.env,
      COMMAND_LOG: commandLog,
      GITHUB_OUTPUT: githubOutput,
      GITHUB_ENV: githubEnv,
      GITHUB_STEP_SUMMARY: githubSummary,
      PUBLISH_CLOUD_IMAGE: publishCloudImage ? "true" : "false",
      PUBLISH_WORKSPACE_IMAGE: publishWorkspaceImage ? "true" : "false",
      TCR_ID: "test-user",
      TCR_SECRET: "test-password",
      OPL_CLOUD_IMAGE_CONTEXT: ".",
      OPL_CLOUD_IMAGE_REPOSITORY: "registry.example.test/opl/cloud",
      OPL_CLOUD_IMAGE_REF: "registry.example.test/opl/cloud:cloud-test",
      OPL_WORKSPACE_IMAGE_REPOSITORY: "registry.example.test/opl/workspace",
      OPL_WORKSPACE_IMAGE_REF: `registry.example.test/opl/workspace:${workspaceImageTag}`,
      OPL_WORKSPACE_SOURCE_ROOT: "/tmp/one-person-lab-app",
      OPL_WORKSPACE_FRAMEWORK_SHA: workspaceFrameworkSha,
      CLOUD_DIGEST: cloudDigest,
      WORKSPACE_DIGEST: workspaceDigest
    }
  });
  const outputs = Object.fromEntries((await readFile(githubOutput, "utf8"))
    .trim().split("\n").filter(Boolean).map((line) => {
      const separator = line.indexOf("=");
      return [line.slice(0, separator), line.slice(separator + 1)];
    }));
  const commands = await readFile(commandLog, "utf8");
  await rm(root, { recursive: true, force: true });
  return { ...result, commands, outputs, cloudDigest };
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
      OPL_TENCENT_ZONE: "ap-guangzhou-3",
      TENCENTCLOUD_REGION: "ap-guangzhou",
      OPL_SUB2API_BASE_URL: "https://wallet.example.test",
      OPL_SUB2API_REQUEST_TIMEOUT_MS: "7000",
      OPL_MONTHLY_BILLING_WORKER_ENABLED: "1",
      OPL_MONTHLY_BILLING_INTERVAL_MS: "60000"
    }
  };
}

test("TKE deploy workflow matches the current deployment contract", async () => {
  const contract = await readJson(deploymentContractPath);
  const deployWorkflow = await readWorkflow(contract.deployWorkflow.file);
  assertWorkflowContract(deployWorkflow, contract.deployWorkflow, contract);
  assertWorkflowContract(deployWorkflow, contract.productionDiagnosticsJob, contract);
  assertWorkflowContract(deployWorkflow, contract.productionRollForwardRecoveryJob, contract);
  assert.equal(contract.productionRollForwardRecoveryJob.automaticRollback, false);
  assert.equal(contract.productionRollForwardRecoveryJob.candidatePodDeletion, false);
  assert.equal(contract.productionRollForwardRecoveryJob.providerMutation, false);
  assert.equal(contract.productionRollForwardRecoveryJob.walletMutation, false);
  assert.ok(contract.deployWorkflow.requiredEnv.includes("OPL_TENCENT_ZONE"));
  assert.deepEqual(contract.deployWorkflow.preDebitTencentMutationGate, {
    env: "RUN_TENCENT_CREATE_RELEASE_EXECUTION",
    requiredValue: "1",
    enforcement: "shared_tencent_monthly_preflight"
  });
  for (const key of [
    "OPL_OPERATOR_CIDRS",
    "OPL_TRUSTED_PROXY_CIDRS",
    "OPL_BASIC_COMPUTE_NODE_POOL_ID",
    "OPL_PRO_COMPUTE_NODE_POOL_ID",
    "OPL_CODEX_BASE_URL",
    "OPL_GATEWAY_PUBLIC_BASE_URL",
    "OPL_PROVIDER_ACCEPTANCE_TOKEN",
    "OPL_VERIFY_BASIC_ACCOUNT_ID",
    "OPL_VERIFY_PRO_ACCOUNT_ID",
    "OPL_VERIFY_MUTATION_APPROVAL_ID"
  ]) {
    assert.equal(contract.deployWorkflow.requiredEnv.includes(key), false, key);
  }
  assert.equal(contract.productionVerificationWorkflow.launchStatus, "paused");
  assert.equal(contract.productionVerificationWorkflow.mode, "read_only_dual_fixed_slots");
  assert.deepEqual(contract.productionVerificationWorkflow.requiredInputs, []);
  assert.equal(contract.productionVerificationWorkflow.requestTimeoutMsDefault, 30_000);
  assert.equal(contract.productionVerificationWorkflow.timeoutMinutes, 15);
  assert.deepEqual(contract.productionVerificationWorkflow.slotDescriptors, [basicSlotDescriptor, proSlotDescriptor]);
  assertWorkflowContract(await readWorkflow(contract.productionVerificationWorkflow.file), contract.productionVerificationWorkflow, contract);
  assert.equal(contract.productionLiveQaJob, undefined);
  assert.equal(contract.providerAcceptanceWorkflow.launchStatus, "paused");
  assert.equal(contract.productionBootstrapJob.mode, "endpoints_and_cloud_image_readiness_only");
  assert.equal(contract.productionBootstrapJob.releaseComplete, false);
  assert.equal(contract.productionBootstrapJob.approvalEnvironment, "production");
  assertWorkflowContract(deployWorkflow, contract.productionBootstrapJob, contract);
  assert.equal(contract.productionReleaseGateJob.bootstrapConclusion, "release_incomplete_failure");
  assertWorkflowContract(deployWorkflow, contract.productionReleaseGateJob, contract);
  assert.ok(contract.productionLegacySecretCleanupJob);
  assert.equal(contract.productionLegacySecretCleanupJob.trigger, "candidate_rollout_successful");
  assert.equal(contract.productionLegacySecretCleanupJob.legacySecretName, "opl-cloud-workspace-codex");
  assert.equal(contract.productionLegacySecretCleanupJob.accountScopedSecretDeletionForbidden, true);
  assert.equal(contract.productionLegacySecretCleanupJob.failureBehavior, "fail_workflow_without_image_rollback");
  assertWorkflowContract(deployWorkflow, contract.productionLegacySecretCleanupJob, contract);
  assert.equal(contract.productionRollbackJob.trigger, "post_snapshot_deploy_or_bootstrap_readiness_not_successful");
  assertWorkflowContract(deployWorkflow, contract.productionRollbackJob, contract);
  assert.deepEqual(contract.imageReleaseWorkflow.outputs, ["cloud_image", "workspace_image"]);
  assert.equal(contract.imageReleaseWorkflow.skippedOutput, "empty");
  assert.doesNotMatch(JSON.stringify(contract), /paid_confirmation|OPL_VERIFY_PAID_CONFIRMATION|OPL_VERIFY_MODEL_ACCESS_KEY/);
});

test("production verification is read only and requires both reusable prepaid slots", async () => {
  const workflow = await readWorkflow(".github/workflows/verify-production-chain.yml");
  assert.deepEqual(Object.keys(workflow.jobs), ["verify"]);
  const currentJob = workflowJob(workflow, "verify");
  const runs = serializedRuns(currentJob);
  const inputs = Object.keys(workflow.on.workflow_dispatch.inputs || {});

  assert.equal(workflow.concurrency.group, "production-resource-verification");
  assert.equal(workflow.concurrency["cancel-in-progress"], false);
  assert.equal(currentJob["timeout-minutes"], 15);
  assert.equal(workflow.on.workflow_dispatch.inputs.basic_account_id, undefined);
  assert.equal(workflow.on.workflow_dispatch.inputs.pro_account_id, undefined);
  assert.equal(workflow.on.workflow_dispatch.inputs.request_timeout_ms.default, "30000");
  assert.equal(currentJob.env.OPL_VERIFY_REQUEST_TIMEOUT_MS, "${{ inputs.request_timeout_ms }}");
  assert.equal(inputs.includes("paid_confirmation"), false);
  assert.equal(Object.hasOwn(currentJob.env, "OPL_VERIFY_PAID_CONFIRMATION"), false);
  assert.equal(Object.hasOwn(currentJob.env, "OPL_VERIFY_MODEL_ACCESS_KEY"), false);
  assert.equal(currentJob.env.OPL_VERIFY_AUTH_USERS_JSON, "${{ secrets.OPL_VERIFY_AUTH_USERS_JSON }}");
  assert.equal(currentJob.env.OPL_VERIFY_SLOT_ID, "${{ matrix.slot_id }}");
  assert.equal(currentJob.env.OPL_VERIFY_SLOT_DESCRIPTOR_JSON, "${{ matrix.descriptor }}");
  assert.deepEqual(currentJob.strategy.matrix.include.map((entry) => ({
    slotId: entry.slot_id, accountId: entry.account_id, descriptor: JSON.parse(entry.descriptor)
  })), [
    { slotId: basicSlotDescriptor.id, accountId: "acct-verification-slot-basic-01", descriptor: basicSlotDescriptor },
    { slotId: proSlotDescriptor.id, accountId: "acct-verification-slot-pro-01", descriptor: proSlotDescriptor }
  ]);
  assert.equal(Object.hasOwn(currentJob.env, "OPL_VERIFY_PURCHASE_BUDGET_REMAINING"), false);
  assert.match(runs, /node tools\/production-verifier\.ts --browser-e2e/);
  assert.doesNotMatch(runs, /paid.confirmation|compute-allocations|storage-volumes|destroy|detach/i);

  const verifier = await readFile(repoFile("tools/production-verifier.ts"), "utf8");
  assert.doesNotMatch(verifier, /cleanupVerificationResources|productionVerificationMutationKey|paid_confirmation_required|I_UNDERSTAND_THIS_SPENDS_REAL_BALANCE/);
});

test("ordinary TKE deploy has no Acceptance or live QA mutation gate", async () => {
  const deployWorkflow = await readWorkflow(".github/workflows/deploy-tke-production.yml");
  const deploy = workflowJob(deployWorkflow, "deploy");
  const inputGate = stepsByName(deploy).get("Check deployment inputs");
  const source = JSON.stringify(deployWorkflow);
  assert.equal(deployWorkflow.jobs["live-qa"], undefined);
  assert.equal(deployWorkflow.on.workflow_dispatch.inputs.live_qa_approval_id, undefined);
  assert.doesNotMatch(source, /OPL_VERIFY_|OPL_PROVIDER_ACCEPTANCE_TOKEN|production-provider-acceptance/);
  assert.doesNotMatch(source, /OPL_OPERATOR_CIDRS|OPL_TRUSTED_PROXY_CIDRS/);
  assert.doesNotMatch(source, /OPL_BASIC_COMPUTE_NODE_POOL_ID|OPL_PRO_COMPUTE_NODE_POOL_ID/);
  assert.doesNotMatch(source, /OPL_CODEX_BASE_URL|OPL_GATEWAY_PUBLIC_BASE_URL/);
  assert.doesNotMatch(inputGate.run, /NODE_POOL_ID|GATEWAY_PUBLIC_BASE_URL|PROVIDER_ACCEPTANCE|OPL_VERIFY_/);
});

test("TKE diagnostics are read only and mutually exclusive with deploy", async () => {
  const workflow = await readWorkflow(".github/workflows/deploy-tke-production.yml");
  const input = workflow.on.workflow_dispatch.inputs.diagnostics_only;
  const deploy = workflowJob(workflow, "deploy");
  const diagnose = workflowJob(workflow, "diagnose");
  const releaseGate = workflowJob(workflow, "release-gate");
  const runs = serializedStep(stepsByName(diagnose).get("Read cluster diagnostics"));

  assert.equal(input.type, "boolean");
  assert.equal(input.default, false);
  assert.equal(deploy.if, "${{ !inputs.diagnostics_only && inputs.roll_forward_mode == 'none' }}");
  assert.equal(diagnose.if, "${{ inputs.diagnostics_only && inputs.roll_forward_mode == 'none' }}");
  assert.match(String(releaseGate.if), /!inputs\.diagnostics_only/);
  assert.deepEqual(diagnose["runs-on"], ["self-hosted", "tencent-cloud", "opl-cloud", "tke-vpc"]);
  assert.equal(diagnose.environment, "production");
  assert.match(runs, /get nodes -o wide/);
  assert.match(runs, /get deploy,rs,pod -o wide/);
  assert.match(runs, /get events/);
  assert.match(runs, /describe "\$pod"/);
  assert.match(runs, /logs/);
  assert.doesNotMatch(runs, /\b(?:apply|delete|patch|scale)\b|set image|rollout restart/);
});

test("TKE roll-forward recovery preserves the candidate pod and never rolls back", async () => {
  const workflow = await readWorkflow(".github/workflows/deploy-tke-production.yml");
  const input = workflow.on.workflow_dispatch.inputs.roll_forward_mode;
  const deploy = workflowJob(workflow, "deploy");
  const recover = workflowJob(workflow, "roll-forward");
  const releaseGate = workflowJob(workflow, "release-gate");
  const apply = serializedStep(stepsByName(recover).get("Apply roll-forward image and capture diagnostics"));

  assert.equal(input.type, "choice");
  assert.equal(input.default, "none");
  assert.deepEqual(input.options, ["none", "control-plane", "all-cloud"]);
  assert.equal(recover.environment, "production");
  assert.deepEqual(recover["runs-on"], ["self-hosted", "tencent-cloud", "opl-cloud", "tke-vpc"]);
  assert.match(String(deploy.if), /roll_forward_mode == 'none'/);
  assert.match(String(releaseGate.if), /roll_forward_mode == 'none'/);
  assert.equal(
    recover.env.OPL_CANDIDATE_IMAGE,
    "uswccr.ccs.tencentyun.com/oplcloud/opl-cloud@sha256:1c68bd613223ce06ffd1042ec226144830f1533c937569e886cd07ade97b5ca5"
  );
  assert.match(apply, /set image[\s\\]+deployment\/opl-cloud-control-plane/);
  assert.match(apply, /set image[\s\\]+deployment\/opl-cloud-ledger/);
  assert.match(apply, /set image[\s\\]+deployment\/opl-cloud-fabric/);
  assert.match(apply, /imageID/);
  assert.match(apply, /describe "pod\/\$pod"/);
  assert.match(apply, /logs "pod\/\$pod".*--previous/s);
  assert.match(apply, /rollout status/);
  assert.doesNotMatch(apply, /restore_previous|rollback|rollout undo|delete (?:pod|deployment)/i);
});

test("TKE roll-forward recovery reports PostgreSQL transport without leaking the DSN", async () => {
  const workflow = await readWorkflow(".github/workflows/deploy-tke-production.yml");
  const recover = workflowJob(workflow, "roll-forward");
  const diagnostic = stepsByName(recover).get("Read sanitized PostgreSQL transport");
  assert.ok(diagnostic?.run, "roll-forward recovery is missing the PostgreSQL transport diagnostic");

  const databaseURL = "postgresql://pilot-user:pilot-password@10.66.0.21:5432/opl?sslmode=disable";
  const resources = {
    apiVersion: "v1",
    kind: "List",
    items: [
      { apiVersion: "v1", kind: "ConfigMap", metadata: { name: "opl-cloud-config" }, data: {} },
      {
        apiVersion: "v1",
        kind: "Secret",
        metadata: { name: "opl-cloud-database" },
        data: { DATABASE_URL: Buffer.from(databaseURL).toString("base64") }
      }
    ]
  };
  const harness = `
kubectl() {
  case " $* " in
    *" get configmap/opl-cloud-config secret/opl-cloud-database -o json "*) printf '%s' "$TEST_RESOURCE_JSON" ;;
    *) return 64 ;;
  esac
}
${diagnostic.run}
`;
  const result = spawnSync("bash", ["-c", harness], {
    cwd: fileURLToPath(repoFile(".")),
    encoding: "utf8",
    env: {
      ...process.env,
      GITHUB_OUTPUT: "/dev/null",
      KUBECONFIG: "/dev/null",
      OPL_K8S_NAMESPACE: "opl-test",
      TEST_RESOURCE_JSON: JSON.stringify(resources)
    }
  });

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /^configMapPGSSLMODE=absent$/m);
  assert.match(result.stdout, /^databaseURLHostKind=rfc1918_ipv4$/m);
  assert.match(result.stdout, /^databaseURLSslmode=disable$/m);
  assert.doesNotMatch(`${result.stdout}\n${result.stderr}`, /pilot-user|pilot-password|10\.66\.0\.21|postgres(?:ql)?:\/\//);
});

test("TKE roll-forward recovery repairs only the approved private PostgreSQL transport", async () => {
  const workflow = await readWorkflow(".github/workflows/deploy-tke-production.yml");
  const input = workflow.on.workflow_dispatch.inputs.repair_postgres_transport;
  const repair = stepsByName(workflowJob(workflow, "roll-forward")).get("Repair approved private PostgreSQL transport");
  assert.equal(input.type, "boolean");
  assert.equal(input.default, false);
  assert.equal(
    repair?.if,
    "${{ inputs.repair_postgres_transport && !inputs.diagnostics_only && inputs.roll_forward_mode == 'control-plane' }}"
  );
  assert.ok(repair?.run, "roll-forward recovery is missing the PostgreSQL transport repair");

  const root = await mkdtemp(join(tmpdir(), "opl-postgres-transport-repair-"));
  const kubectlLog = join(root, "kubectl.log");
  const harness = `
kubectl() {
  printf '%s\n' "$*" >> "$TEST_KUBECTL_LOG"
  case " $* " in
    *" patch configmap/opl-cloud-config --type merge -p "*) ;;
    *" get configmap/opl-cloud-config -o jsonpath={.data.PGSSLMODE} "*) printf 'disable' ;;
    *) return 64 ;;
  esac
}
${repair.run}
`;
  const runRepair = (extraEnv = {}) => spawnSync("bash", ["-c", harness], {
    cwd: fileURLToPath(repoFile(".")),
    encoding: "utf8",
    env: {
      ...process.env,
      KUBECONFIG: "/dev/null",
      OPL_K8S_NAMESPACE: "opl-test",
      OPL_RECOVERY_PG_CONFIG_MODE: "absent",
      OPL_RECOVERY_DATABASE_HOST_KIND: "rfc1918_ipv4",
      OPL_RECOVERY_DATABASE_SSLMODE: "absent",
      TEST_KUBECTL_LOG: kubectlLog,
      ...extraEnv
    }
  });

  try {
    await writeFile(kubectlLog, "");
    const success = runRepair();
    assert.equal(success.status, 0, success.stderr);
    assert.match(await readFile(kubectlLog, "utf8"), /patch configmap\/opl-cloud-config --type merge -p \{"data":\{"PGSSLMODE":"disable"\}\}/);

    for (const extraEnv of [
      { OPL_RECOVERY_PG_CONFIG_MODE: "other" },
      { OPL_RECOVERY_DATABASE_HOST_KIND: "non_literal" },
      { OPL_RECOVERY_DATABASE_SSLMODE: "verify-full" }
    ]) {
      await writeFile(kubectlLog, "");
      assert.notEqual(runRepair(extraEnv).status, 0);
      assert.equal(await readFile(kubectlLog, "utf8"), "");
    }
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("TKE roll-forward recovery backs up before read-only primary Workspace conflict inspection", async () => {
  const workflow = await readWorkflow(".github/workflows/deploy-tke-production.yml");
  const input = workflow.on.workflow_dispatch.inputs.inspect_primary_workspace_conflict;
  const recover = workflowJob(workflow, "roll-forward");
  const inspect = stepsByName(recover).get("Back up and inspect primary Workspace conflict");
  assert.equal(input.type, "boolean");
  assert.equal(input.default, false);
  assert.equal(
    inspect?.if,
    "${{ inputs.inspect_primary_workspace_conflict && !inputs.diagnostics_only && inputs.roll_forward_mode == 'control-plane' }}"
  );
  assert.equal(inspect?.env?.DATABASE_URL, "${{ secrets.DATABASE_URL }}");
  assert.equal(inspect?.env?.PGSSLMODE, "disable");
  assert.equal(
    recover.env.OPL_POSTGRES_CLIENT_IMAGE,
    "quay.io/sclorg/postgresql-16-c9s@sha256:cf7fb6396ee839019b6fef1838474e5b752828a512bb27030f13a28f42ec5617"
  );
  assert.ok(inspect?.run, "roll-forward recovery is missing the migration conflict inspection");
  assert.match(inspect.run, /install -m 600 \/dev\/null "\$backup_file"/);
  assert.match(inspect.run, /docker run --rm/);
  assert.match(inspect.run, /--entrypoint \/bin\/bash "\$OPL_POSTGRES_CLIENT_IMAGE"/);
  assert.match(inspect.run, /pg_dump .*--format=custom/);
  assert.match(inspect.run, /pg_restore --list/);
  assert.match(inspect.run, /sha256sum/);
  assert.match(inspect.run, /duplicate_accounts/);
  assert.match(inspect.run, /control_plane_compute_allocations/);
  assert.match(inspect.run, /control_plane_storage_volumes/);
  assert.match(inspect.run, /control_plane_storage_attachments/);
  assert.match(inspect.run, /control_plane_runtime_operations/);
  assert.ok(inspect.run.indexOf("pg_dump") < inspect.run.indexOf("psql"), "backup must complete before conflict inspection");
  assert.doesNotMatch(inspect.run, /^\s*(?:pg_dump|pg_restore|psql)\b/m);
  assert.doesNotMatch(inspect.run, /\b(?:DELETE|UPDATE|INSERT|ALTER|DROP|TRUNCATE)\b/i);
  assert.doesNotMatch(inspect.run, /echo .*DATABASE_URL|printf .*DATABASE_URL/);
});

test("TKE bootstrap deploy is approved, read only, and cannot complete a release", async () => {
  const workflow = await readWorkflow(".github/workflows/deploy-tke-production.yml");
  const input = workflow.on.workflow_dispatch.inputs.bootstrap_mode;
  const deploy = workflowJob(workflow, "deploy");
  const bootstrap = workflowJob(workflow, "bootstrap-readiness");
  const releaseGate = workflowJob(workflow, "release-gate");
  const cleanup = workflowJob(workflow, "retire-legacy-workspace-secret");
  const rollback = workflowJob(workflow, "rollback-live-qa");
  const rolloutRun = serializedStep(stepsByName(deploy).get("Render and apply manifest"));
  const rollbackRun = serializedStep(stepsByName(rollback).get("Restore previous Cloud and App images"));
  const bootstrapRun = serializedRuns(bootstrap);
  const releaseRun = serializedRuns(releaseGate);

  assert.equal(input.type, "boolean");
  assert.equal(input.default, false);
  assert.equal(deploy.environment, "production");
  assert.equal(deploy.env.OPL_BOOTSTRAP_MODE, "${{ inputs.bootstrap_mode }}");
  assert.match(String(deploy.env.OPL_MONTHLY_BILLING_WORKER_ENABLED), /inputs\.bootstrap_mode.*'0'/);
  assert.equal(bootstrap.needs, "deploy");
  assert.equal(bootstrap.if, "${{ inputs.roll_forward_mode == 'none' && inputs.bootstrap_mode && needs.deploy.result == 'success' }}");
  assert.equal(bootstrap.environment, "production");
  assert.equal(stepsByName(bootstrap).get("Set up Node")?.uses, "actions/setup-node@v4");
  assert.equal(stepsByName(bootstrap).get("Set up Node")?.with?.["node-version"], "22");
  assert.match(bootstrapRun, /\/api\/production\/readiness/);
  assert.match(bootstrapRun, /cloudImagesReady/);
  assert.match(bootstrapRun, /workspaceImagesReady/);
  assert.match(bootstrapRun, /immutableImagesReady/);
  assert.match(bootstrapRun, /releaseComplete.*false/s);
  assert.match(bootstrapRun, /release incomplete/i);
  assert.doesNotMatch(bootstrapRun, /production-live-qa|provider-acceptance|purchase|delete|renew|POST/i);

  assert.deepEqual(releaseGate.needs, ["deploy", "bootstrap-readiness"]);
  assert.equal(releaseGate.if, "${{ always() && !inputs.diagnostics_only && inputs.roll_forward_mode == 'none' }}");
  assert.match(releaseRun, /release incomplete/i);
  assert.match(releaseRun, /releaseComplete.*false/s);
  assert.match(releaseRun, /exit 1/);
  assert.match(String(cleanup.if), /!inputs\.bootstrap_mode/);
  assert.deepEqual(rollback.needs, ["deploy", "bootstrap-readiness"]);
  assert.match(String(rollback.if), /inputs\.bootstrap_mode.*needs\.bootstrap-readiness\.result != 'success'/);
  assert.match(String(rollback.if), /!inputs\.bootstrap_mode.*needs\.deploy\.result != 'success'/);
  assert.doesNotMatch(String(rollback.if), /release-gate/);
  assert.match(rolloutRun, /OPL_BOOTSTRAP_MODE[\s\S]*apply_bootstrap_images/);
  assert.match(rolloutRun, /OPL_BOOTSTRAP_MODE[\s\S]*restore_previous_bootstrap_images/);
  assert.match(rollbackRun, /inputs\.bootstrap_mode[\s\S]*restore_previous_bootstrap_images/);
});

test("image release accepts only a full Cloud commit contained in the workflow repository main", async () => {
  const [workflow, contract] = await Promise.all([
    readWorkflow(".github/workflows/release-opl-cloud-image.yml"),
    readJson(deploymentContractPath)
  ]);
  const steps = stepsByName(workflowJob(workflow, "build-push"));
  const checkout = steps.get("Checkout");
  const verify = steps.get("Verify Cloud source");

  assert.equal(checkout?.with?.ref, "${{ inputs.ref }}");
  assert.equal(checkout?.with?.["fetch-depth"], 0);
  assert.ok(verify, "release workflow missing Verify Cloud source");
  assert.deepEqual(contract.imageReleaseWorkflow.cloudCandidate, {
    input: "ref",
    repositoryAuthority: "github.repository",
    requirements: ["40_character_git_sha", "checked_out_head_exact_match", "merged_into_workflow_repository_main"],
    mainReadback: "refs/remotes/release-source/main"
  });
  assert.equal(workflowJob(workflow, "build-push").env.OPL_CLOUD_SOURCE_REPOSITORY, "${{ github.repository }}");
  assert.match(verify.run, /\^\[0-9a-fA-F\]\{40\}\$/);
  assert.doesNotMatch(verify.run, /remote set-url/);
  assert.match(verify.run, /fetch --no-tags "https:\/\/github\.com\/\$\{OPL_CLOUD_SOURCE_REPOSITORY\}\.git" main:refs\/remotes\/release-source\/main/);
  assert.match(verify.run, /rev-parse HEAD/);
  assert.match(verify.run, /rev-parse refs\/remotes\/release-source\/main/);
  assert.match(verify.run, /merge-base --is-ancestor "\$cloud_head_sha" "\$cloud_main_sha"/);

  const accepted = runCloudSourceGate(verify, cloudCandidateSha);
  assert.equal(accepted.status, 0, accepted.stderr);
  assert.match(accepted.stderr, /git fetch --no-tags https:\/\/github\.com\/RenDeHuang\/OPL-Cloud\.git main:refs\/remotes\/release-source\/main/);
  assert.match(accepted.stderr, /git merge-base --is-ancestor/);

  for (const invalidSha of ["main", "abcdef0", "g".repeat(40), "c".repeat(39), "c".repeat(41)]) {
    assert.notEqual(runCloudSourceGate(verify, invalidSha).status, 0, `Cloud ref must reject ${invalidSha}`);
  }
  assert.notEqual(runCloudSourceGate(verify, cloudCandidateSha, { headSha: "e".repeat(40) }).status, 0);
  assert.notEqual(runCloudSourceGate(verify, cloudCandidateSha, { mainSha: "main" }).status, 0);
  assert.notEqual(runCloudSourceGate(verify, cloudCandidateSha, { merged: false }).status, 0);
});

test("image release builds Workspace from exact merged-main App, active-shell, and Framework commits", async () => {
  const workflow = await readWorkflow(".github/workflows/release-opl-cloud-image.yml");
  const currentJob = workflowJob(workflow, "build-push");
  const inputs = workflow.on.workflow_dispatch.inputs;
  const steps = stepsByName(currentJob);
  const metadata = serializedStep(stepsByName(currentJob).get("Image metadata"));
  const setupNode = steps.get("Set up Node");
  const prepare = serializedStep(steps.get("Prepare Workspace App source"));
  const source = JSON.stringify(workflow);
  const runs = serializedRuns(currentJob);

  assert.deepEqual(Object.keys(inputs), [
    "ref",
    "image_tag",
    "publish_cloud_image",
    "publish_workspace_image",
    "workspace_app_main_sha",
    "workspace_shell_main_sha",
    "workspace_framework_main_sha"
  ]);
  assert.equal(inputs.workspace_app_main_sha.required, true);
  assert.equal(inputs.workspace_shell_main_sha.required, true);
  assert.equal(inputs.workspace_framework_main_sha.required, true);
  assert.doesNotMatch(metadata, /\$\{\{\s*inputs\./);
  assert.match(metadata, /\^\[0-9a-fA-F\]\{40\}\$/);
  assert.match(metadata, /tr '\[:upper:\]' '\[:lower:\]'/);
  assert.match(metadata, /workspace_image_tag="\$\{workspace_app_sha:0:12\}-\$\{workspace_shell_sha:0:12\}-\$\{workspace_framework_sha:0:12\}"/);
  assert.equal(setupNode?.uses, "actions/setup-node@v4");
  assert.equal(setupNode?.with?.["node-version"], "22");
  assert.match(String(setupNode?.if), /publish_workspace_image/);
  assert.match(prepare, /git clone --filter=blob:none --single-branch --branch main/);
  assert.match(prepare, /github\.com\/gaofeng21cn\/one-person-lab-app\.git/);
  assert.match(prepare, /github\.com\/gaofeng21cn\/opl-aion-shell\.git/);
  assert.match(prepare, /github\.com\/gaofeng21cn\/one-person-lab\.git/);
  assert.match(prepare, /merge-base --is-ancestor "\$OPL_WORKSPACE_APP_SHA" origin\/main/);
  assert.match(prepare, /merge-base --is-ancestor "\$OPL_WORKSPACE_SHELL_SHA" origin\/main/);
  assert.match(prepare, /merge-base --is-ancestor "\$OPL_WORKSPACE_FRAMEWORK_SHA" origin\/main/);
  assert.match(prepare, /checkout --detach "\$OPL_WORKSPACE_APP_SHA"/);
  assert.match(prepare, /checkout --detach "\$OPL_WORKSPACE_SHELL_SHA"/);
  assert.match(prepare, /checkout --detach "\$OPL_WORKSPACE_FRAMEWORK_SHA"/);
  assert.match(prepare, /npm run ensure:shell/);
  assert.match(prepare, /git -C "\$workspace_root" rev-parse HEAD/);
  assert.match(prepare, /git -C "\$shell_root" rev-parse HEAD/);
  assert.match(prepare, /git -C "\$framework_root" rev-parse HEAD/);
  assert.match(prepare, /grep -Fxq '\.git' "\$shell_root\/\.dockerignore"/);
  assert.match(runs, /docker buildx imagetools inspect/);
  assert.match(runs, /docker buildx build --push[\s\S]*shells\/aionui\/Dockerfile[\s\S]*shells\/aionui/);
  assert.match(runs, /--build-arg OPL_FRAMEWORK_REF="\$OPL_WORKSPACE_FRAMEWORK_SHA"/);
  assert.match(runs, /sha256:\[0-9a-f\]\{64\}/);
  assert.match(runs, /OPL_CLOUD_IMAGE=.*@\$\{cloud_digest\}/);
  assert.match(runs, /OPL_WORKSPACE_IMAGE=.*@\$\{workspace_digest\}/);
  assert.doesNotMatch(source, /mirror_workspace_image|workspace_source_image|MIRROR_WORKSPACE_IMAGE|REQUESTED_WORKSPACE_IMAGE_TAG|WORKSPACE_SOURCE_IMAGE/i);
  assert.doesNotMatch(runs, /docker login ghcr\.io|imagetools create|git ls-remote|org\.opencontainers\.image\.revision/);
  assert.doesNotMatch(source, /v?26\.7\.1[23]|:latest\b|@latest\b/);
});

test("image release switches isolate publication commands and leave skipped outputs empty", async () => {
  const workflow = await readWorkflow(".github/workflows/release-opl-cloud-image.yml");
  const currentJob = workflowJob(workflow, "build-push");
  const release = stepsByName(currentJob).get("Build and push images");

  const disabled = await runImageReleaseStep(release, false, false);
  assert.equal(disabled.status, 0, disabled.stderr);
  assert.equal(disabled.commands, "");
  assert.deepEqual(disabled.outputs, { cloud_image: "", workspace_image: "" });

  const cloudOnly = await runImageReleaseStep(release, true, false);
  assert.equal(cloudOnly.status, 0, cloudOnly.stderr);
  assert.match(cloudOnly.commands, /docker buildx build --push/);
  assert.match(cloudOnly.commands, /imagetools inspect registry\.example\.test\/opl\/cloud:cloud-test/);
  assert.doesNotMatch(cloudOnly.commands, /ghcr\.io|one-person-lab|imagetools create|git ls-remote|curl /);
  assert.deepEqual(cloudOnly.outputs, {
    cloud_image: `registry.example.test/opl/cloud@${cloudOnly.cloudDigest}`,
    workspace_image: ""
  });

  const workspaceOnly = await runImageReleaseStep(release, false, true);
  assert.equal(workspaceOnly.status, 0, workspaceOnly.stderr);
  assert.doesNotMatch(workspaceOnly.commands, /imagetools inspect registry\.example\.test\/opl\/cloud/);
  assert.match(workspaceOnly.commands, /docker buildx build --push -f \/tmp\/one-person-lab-app\/shells\/aionui\/Dockerfile/);
  assert.match(workspaceOnly.commands, /-t registry\.example\.test\/opl\/workspace:aaaaaaaaaaaa-bbbbbbbbbbbb-eeeeeeeeeeee \/tmp\/one-person-lab-app\/shells\/aionui/);
  assert.match(workspaceOnly.commands, /imagetools inspect registry\.example\.test\/opl\/workspace:aaaaaaaaaaaa-bbbbbbbbbbbb-eeeeeeeeeeee/);
  assert.doesNotMatch(workspaceOnly.commands, /ghcr\.io|imagetools create|git ls-remote|curl /);
  assert.deepEqual(workspaceOnly.outputs, {
    cloud_image: "",
    workspace_image: `registry.example.test/opl/workspace@${workspaceDigest}`
  });

  assert.equal(currentJob.outputs.cloud_image, "${{ steps.images.outputs.cloud_image }}");
  assert.equal(currentJob.outputs.workspace_image, "${{ steps.images.outputs.workspace_image }}");
});

test("image release accepts only full App, active-shell, and Framework commit SHAs", async () => {
  const workflow = await readWorkflow(".github/workflows/release-opl-cloud-image.yml");
  const metadata = stepsByName(workflowJob(workflow, "build-push")).get("Image metadata");

  for (const [appSha, shellSha, frameworkSha] of [
    [workspaceAppSha, workspaceShellSha, workspaceFrameworkSha],
    [workspaceAppSha.toUpperCase(), workspaceShellSha.toUpperCase(), workspaceFrameworkSha.toUpperCase()]
  ]) {
    const result = runImageMetadata(metadata, appSha, shellSha, frameworkSha);
    assert.equal(result.status, 0, result.stderr);
  }
  for (const invalidSha of ["main", "abcdef0", "g".repeat(40), "a".repeat(39), "a".repeat(41)]) {
    const invalidApp = runImageMetadata(metadata, invalidSha, workspaceShellSha, workspaceFrameworkSha);
    assert.notEqual(invalidApp.status, 0, `App SHA must reject ${invalidSha}`);
    const invalidShell = runImageMetadata(metadata, workspaceAppSha, invalidSha, workspaceFrameworkSha);
    assert.notEqual(invalidShell.status, 0, `active-shell SHA must reject ${invalidSha}`);
    const invalidFramework = runImageMetadata(metadata, workspaceAppSha, workspaceShellSha, invalidSha);
    assert.notEqual(invalidFramework.status, 0, `Framework SHA must reject ${invalidSha}`);
  }
});

test("TKE deploy installs Sub2API credentials without Acceptance credentials", async () => {
  const workflow = await readWorkflow(".github/workflows/deploy-tke-production.yml");
  const currentJob = workflowJob(workflow, "deploy");
  const steps = stepsByName(currentJob);
  const prepare = serializedStep(steps.get("Prepare kubeconfig"));
  const install = serializedStep(steps.get("Install Kubernetes secrets"));
  const cleanup = steps.get("Remove deployment secrets");

  assert.match(install, /create secret generic opl-cloud-sub2api/);
  assert.match(install, /--from-file=OPL_SUB2API_ADMIN_EMAIL/);
  assert.match(install, /--from-file=OPL_SUB2API_ADMIN_PASSWORD/);
  assert.equal(Object.hasOwn(currentJob.env, "OPL_PROVIDER_ACCEPTANCE_TOKEN"), false);
  assert.doesNotMatch(install, /provider-acceptance|OPL_PROVIDER_ACCEPTANCE_TOKEN/);
  assert.equal(currentJob.env.OPL_TENCENT_ZONE, "${{ vars.OPL_TENCENT_ZONE || 'na-siliconvalley-1' }}");
  assert.equal(currentJob.env.TENCENTCLOUD_REGION, "${{ vars.TENCENTCLOUD_REGION || 'na-siliconvalley' }}");
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
    "OPL_SUB2API_REQUEST_TIMEOUT_MS",
    "OPL_TENCENT_ZONE"
  ]) assert.match(joined, new RegExp(key));
  assert.match(joined, /OPL_TENCENT_ZONE/);
  assert.doesNotMatch(joined, /OPL_(?:BASIC|PRO)_COMPUTE_HOURLY_CNY|OPL_STORAGE_GB_MONTH_CNY|OPL_RESOURCE_BILLING_/);
  assert.doesNotMatch(joined, /OPL_COMPUTE_LAUNCH_ZONE/);
});

test("production deployment surfaces do not configure a Workspace VolumeSnapshotClass", async () => {
  const paths = [
    ".github/workflows/deploy-tke-production.yml",
    "deploy/tke/opl-cloud.k8s.json",
    "deploy/tke/opl-cloud-production.env.example",
    "tools/render-tke-manifest.ts",
    "packages/contracts/opl-cloud-deployment-contract.json"
  ];
  for (const path of paths) {
    const source = await readFile(repoFile(path), "utf8");
    assert.doesNotMatch(source, /OPL_WORKSPACE_VOLUME_SNAPSHOT_CLASS/, path);
  }
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
  assert.equal(config.data.OPL_TENCENT_ZONE, "ap-guangzhou-3");
  assert.equal(config.data.OPL_MONTHLY_BILLING_INTERVAL_MS, "60000");
  assert.equal(config.data.OPL_OPERATOR_CIDRS, undefined);
  assert.equal(config.data.OPL_TRUSTED_PROXY_CIDRS, undefined);
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

test("TKE manifest renderer rejects a whitespace-only launch zone before rendering", async () => {
  const { manifest, values } = await manifestFixture();
  assert.throws(
    () => renderTkeManifest({ manifest, values: { ...values, OPL_TENCENT_ZONE: "   " } }),
    /missing_tke_manifest_values:.*OPL_TENCENT_ZONE/
  );
});

test("TKE manifest renderer rejects Tencent region and zone mismatches in either direction", async () => {
  const { manifest, values } = await manifestFixture();
  for (const [region, zone] of [
    ["na-siliconvalley", "ap-guangzhou-3"],
    ["ap-guangzhou", "na-siliconvalley-1"]
  ]) {
    assert.throws(
      () => renderTkeManifest({ manifest, values: { ...values, TENCENTCLOUD_REGION: region, OPL_TENCENT_ZONE: zone } }),
      /tencent_zone_region_mismatch/
    );
  }
});

test("TKE deploy never applies a ConfigMap with a mismatched Tencent region and zone", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-region-gate-"));
  const rollbackDir = join(root, "previous-images");
  const kubectlLog = join(root, "kubectl.log");
  try {
    const { values } = await manifestFixture();
    const apply = stepsByName(workflowJob(await readWorkflow(".github/workflows/deploy-tke-production.yml"), "deploy")).get("Render and apply manifest")?.run;
    await mkdir(rollbackDir);
    await Promise.all([
      ...["opl-cloud-control-plane", "opl-cloud-ledger", "opl-cloud-fabric"].map((name) => writeFile(join(rollbackDir, name), values.OPL_CLOUD_IMAGE)),
      writeFile(join(rollbackDir, "opl-cloud-config.json"), JSON.stringify({ data: values })),
      writeFile(join(rollbackDir, "OPL_WORKSPACE_IMAGE"), values.OPL_WORKSPACE_IMAGE),
      writeFile(join(rollbackDir, "workspace-images.tsv"), "")
    ]);
    const result = spawnSync("bash", ["-c", `
      kubectl() {
        printf '%s\\n' "$*" >> "$TEST_KUBECTL_LOG"
        return 1
      }
${apply}
    `], {
      cwd: fileURLToPath(repoFile(".")),
      encoding: "utf8",
      env: {
        ...process.env,
        ...values,
        KUBECONFIG: "/dev/null",
        OPL_DEPLOY_SECRET_DIR: root,
        OPL_EXERCISE_ROLLBACK: "false",
        OPL_TENCENT_ZONE: "na-siliconvalley-1",
        TENCENTCLOUD_REGION: "ap-guangzhou",
        TEST_KUBECTL_LOG: kubectlLog
      }
    });

    assert.notEqual(result.status, 0);
    assert.doesNotMatch(await readFile(kubectlLog, "utf8"), /(?:^| )apply -f(?: |$)/m);
    assert.match(result.stderr, /tencent_zone_region_mismatch/);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("TKE manifest renderer rejects another whitespace-only required value before rendering", async () => {
  const { manifest, values } = await manifestFixture();
  assert.throws(
    () => renderTkeManifest({ manifest, values: { ...values, OPL_PUBLIC_URL: "   " } }),
    /missing_tke_manifest_values:.*OPL_PUBLIC_URL/
  );
});

test("TKE manifest renderer can leave shared Ingress ownership untouched", async () => {
  const { manifest, values } = await manifestFixture();
  const rendered = renderTkeManifest({ manifest, values, skipSharedIngress: true });
  assert.equal(rendered.items.some((item) => item.kind === "Ingress" && item.metadata?.name === "opl-cloud"), false);
});

test("TKE deploy requires image digests and rolls back Cloud images with the complete ConfigMap", async () => {
  const workflow = await readWorkflow(".github/workflows/deploy-tke-production.yml");
  const currentJob = workflowJob(workflow, "deploy");
  const inputs = Object.keys(workflow.on.workflow_dispatch.inputs || {});
  const checks = serializedStep(stepsByName(currentJob).get("Check deployment inputs"));
  const capture = serializedStep(stepsByName(currentJob).get("Capture rollback image set"));
  const upload = stepsByName(currentJob).get("Upload rollback image set");
  const apply = serializedStep(stepsByName(currentJob).get("Render and apply manifest"));
  const rolloutHelper = await readFile(repoFile("tools/tke-image-rollout.sh"), "utf8");
  const stepNames = [...stepsByName(currentJob).keys()];

  assert.equal(inputs.includes("exercise_rollback"), true);
  assert.match(String(currentJob.env.OPL_EXERCISE_ROLLBACK), /inputs\.exercise_rollback/);
  assert.match(checks, /repository@sha256/);
  assert.match(checks, /OPL_TENCENT_ZONE/);
  assert.match(checks, /sha256:\[0-9a-f\]\{64\}/);
  assert.doesNotMatch(checks, /must include a non-empty container tag/);
  assert.ok(stepNames.indexOf("Capture rollback image set") < stepNames.indexOf("Upload rollback image set"));
  assert.ok(stepNames.indexOf("Upload rollback image set") < stepNames.indexOf("Render and apply manifest"));
  assert.equal(upload?.uses, "actions/upload-artifact@v4");
  assert.match(String(upload?.with?.name), /production-rollback-images/);
  assert.match(String(upload?.with?.path), /previous-images/);
  for (const deployment of ["opl-cloud-control-plane", "opl-cloud-ledger", "opl-cloud-fabric"]) {
    assert.match(capture, new RegExp(deployment));
  }
  assert.match(capture, /get configmap opl-cloud-config[\s\S]*-o json[\s\S]*opl-cloud-config\.json/);
  assert.doesNotMatch(capture, /workspace-images\.tsv|list_workspace_images/);
  assert.doesNotMatch(rolloutHelper, /oplcloud\.cn\/workspace-id|set_workspace_images|wait_workspace_rollouts/);
  assert.match(apply, /source tools\/tke-image-rollout\.sh/);
  assert.match(apply, /apply_candidate_images/);
  assert.match(apply, /restore_previous_images/);
  assert.match(apply, /if \[ "\$OPL_EXERCISE_ROLLBACK" = "true" \]/);
  assert.match(apply, /restore_previous_images[\s\S]*apply_candidate_images/);
  assert.match(apply, /trap .*rollback.* ERR/);
  assert.doesNotMatch(apply, /set \+e/);
});

test("TKE deploy or bootstrap readiness failure schedules rollback from the captured image set", async () => {
  const workflow = await readWorkflow(".github/workflows/deploy-tke-production.yml");
  const deploy = workflowJob(workflow, "deploy");
  const rollback = workflowJob(workflow, "rollback-live-qa");
  const steps = stepsByName(rollback);
  const restore = serializedStep(steps.get("Restore previous Cloud and App images"));

  assert.match(String(deploy.outputs?.rollback_image_set), /rollback_snapshot\.outputs\.artifact-id/);
  assert.equal(stepsByName(deploy).get("Upload rollback image set")?.id, "rollback_snapshot");
  assert.deepEqual(rollback.needs, ["deploy", "bootstrap-readiness"]);
  assert.match(String(rollback.if), /always\(\).*needs\.deploy\.outputs\.rollback_image_set != ''.*inputs\.bootstrap_mode.*needs\.bootstrap-readiness\.result != 'success'.*!inputs\.bootstrap_mode.*needs\.deploy\.result != 'success'/);
  assert.deepEqual(rollback["runs-on"], ["self-hosted", "tencent-cloud", "opl-cloud", "tke-vpc"]);
  assert.equal(rollback.env.TENCENT_DEPLOY_KUBECONFIG_PATH, deploy.env.TENCENT_DEPLOY_KUBECONFIG_PATH);
  assert.equal(steps.get("Set up Node")?.uses, "actions/setup-node@v4");
  assert.equal(steps.get("Set up Node")?.with?.["node-version"], "22");
  assert.equal(steps.get("Download rollback image set")?.uses, "actions/download-artifact@v4");
  assert.equal(Object.hasOwn(rollback.env, "OPL_CLOUD_IMAGE"), false);
  assert.match(restore, /source tools\/tke-image-rollout\.sh/);
  assert.match(restore, /restore_previous_images/);
  assert.doesNotMatch(restore, /set \+e/);
});

test("TKE retires the legacy global Workspace secret only after successful ordinary deploy", async () => {
  const workflow = await readWorkflow(".github/workflows/deploy-tke-production.yml");
  const deploy = workflowJob(workflow, "deploy");
  const cleanup = workflowJob(workflow, "retire-legacy-workspace-secret");
  const rollback = workflowJob(workflow, "rollback-live-qa");
  const retire = serializedStep(stepsByName(cleanup).get("Retire legacy global Workspace secret"));

  assert.equal(cleanup.needs, "deploy");
  assert.equal(cleanup.if, "${{ inputs.roll_forward_mode == 'none' && !inputs.bootstrap_mode && needs.deploy.result == 'success' }}");
  assert.deepEqual(cleanup["runs-on"], ["self-hosted", "tencent-cloud", "opl-cloud", "tke-vpc"]);
  assert.equal(cleanup.environment, "production");
  assert.notEqual(cleanup["continue-on-error"], true);
  assert.equal(cleanup.env.TENCENT_DEPLOY_KUBECONFIG_PATH, deploy.env.TENCENT_DEPLOY_KUBECONFIG_PATH);
  assert.match(retire, /delete secret opl-cloud-workspace-codex --ignore-not-found/);
  assert.match(retire, /get secret opl-cloud-workspace-codex --ignore-not-found -o name/);
  assert.doesNotMatch(retire, /--selector|delete secrets|delete secret .*\*-env/);
  assert.deepEqual(rollback.needs, ["deploy", "bootstrap-readiness"]);
  assert.doesNotMatch(String(rollback.if), /retire-legacy-workspace-secret|cleanup/);
});

test("legacy global Workspace secret retirement verifies absence and propagates every kubectl failure", async () => {
  const workflow = await readWorkflow(".github/workflows/deploy-tke-production.yml");
  const retire = stepsByName(workflowJob(workflow, "retire-legacy-workspace-secret")).get("Retire legacy global Workspace secret")?.run;
  const root = await mkdtemp(join(tmpdir(), "opl-legacy-secret-cleanup-"));
  const kubectlLog = join(root, "kubectl.log");
  const harness = `
    kubectl() {
      printf '%s\\n' "$*" >> "$TEST_KUBECTL_LOG"
      case " $* " in
        *" delete secret opl-cloud-workspace-codex --ignore-not-found "*)
          [ "\${TEST_DELETE_FAIL:-0}" != "1" ] || return 42
          ;;
        *" get secret opl-cloud-workspace-codex --ignore-not-found -o name "*)
          [ "\${TEST_GET_FAIL:-0}" != "1" ] || return 43
          [ "\${TEST_SECRET_REMAINS:-0}" != "1" ] || printf 'secret/opl-cloud-workspace-codex\\n'
          ;;
        *) return 64 ;;
      esac
    }
${retire}
  `;
  const runCleanup = (extraEnv = {}) => spawnSync("bash", ["-c", harness], {
    cwd: fileURLToPath(repoFile(".")),
    encoding: "utf8",
    env: {
      ...process.env,
      KUBECONFIG: "/dev/null",
      OPL_K8S_NAMESPACE: "opl-test",
      TEST_KUBECTL_LOG: kubectlLog,
      ...extraEnv
    }
  });

  try {
    await writeFile(kubectlLog, "");
    const success = runCleanup();
    assert.equal(success.status, 0, success.stderr);
    assert.deepEqual((await readFile(kubectlLog, "utf8")).trim().split("\n"), [
      "--kubeconfig /dev/null -n opl-test delete secret opl-cloud-workspace-codex --ignore-not-found",
      "--kubeconfig /dev/null -n opl-test get secret opl-cloud-workspace-codex --ignore-not-found -o name"
    ]);
    assert.notEqual(runCleanup({ TEST_DELETE_FAIL: "1" }).status, 0);
    assert.notEqual(runCleanup({ TEST_GET_FAIL: "1" }).status, 0);
    assert.notEqual(runCleanup({ TEST_SECRET_REMAINS: "1" }).status, 0);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("TKE rollback restores the complete ConfigMap and never rolls existing Workspaces", async () => {
  const functions = await readFile(repoFile("tools/tke-image-rollout.sh"), "utf8");
  assert.doesNotMatch(functions, /set \+e/);
  assert.doesNotMatch(functions, /oplcloud\.cn\/workspace-id|set_workspace_images|wait_workspace_rollouts/);
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
      writeFile(join(rollbackDir, "opl-cloud-config.json"), JSON.stringify({
        apiVersion: "v1",
        kind: "ConfigMap",
        metadata: { name: "opl-cloud-config", namespace: "opl-test" },
        data: { OPL_WORKSPACE_IMAGE: oldWorkspace, PGSSLMODE: "disable", OPL_SUB2API_SUPPORTED_VERSIONS: "0.1.153" }
      })),
      writeFile(join(rollbackDir, "OPL_WORKSPACE_IMAGE"), oldWorkspace),
      writeFile(join(rollbackDir, "workspace-images.tsv"), `workspace-slot-1\tworkspace\t${oldWorkspace}\n`)
    ]);
    const harness = `
      set -Eeuo pipefail
      rollback_dir="$TEST_ROOT/previous-images"
      workspace_images="$rollback_dir/workspace-images.tsv"
      config_image="\${TEST_CURRENT_WORKSPACE_IMAGE:-$OPL_WORKSPACE_IMAGE}"
      declare -A images=(
        [opl-cloud-control-plane]="\${TEST_CURRENT_CLOUD_IMAGE:-$OPL_CLOUD_IMAGE}"
        [opl-cloud-ledger]="\${TEST_CURRENT_CLOUD_IMAGE:-$OPL_CLOUD_IMAGE}"
        [opl-cloud-fabric]="\${TEST_CURRENT_CLOUD_IMAGE:-$OPL_CLOUD_IMAGE}"
        [workspace-slot-1]="\${TEST_CURRENT_WORKSPACE_IMAGE:-$OPL_WORKSPACE_IMAGE}"
        [workspace-late]="\${TEST_CURRENT_WORKSPACE_IMAGE:-$OPL_WORKSPACE_IMAGE}"
      )
      : > "$TEST_ROOT/kubectl.log"
      kubectl() {
        local command="" target="" assignment="" arg last
        printf '%s ' "$@" >> "$TEST_ROOT/kubectl.log"
        printf '\n' >> "$TEST_ROOT/kubectl.log"
        for arg in "$@"; do
          case "$arg" in
            get|patch|set|rollout|apply) command="$arg" ;;
            deployment/*) target="\${arg#deployment/}" ;;
            *=*) assignment="$arg" ;;
          esac
        done
        case "$command" in
          get)
            if [[ " $* " == *" deployment -l oplcloud.cn/workspace-id -o json "* ]]; then
              if [ "\${EMPTY_WORKSPACES:-0}" = "1" ]; then
                printf '{"items":[]}'
              else
                printf '{"items":[{"metadata":{"name":"workspace-slot-1","labels":{"oplcloud.cn/workspace-id":"slot-1"}},"spec":{"template":{"spec":{"containers":[{"name":"workspace","image":"%s"}]}}}},{"metadata":{"name":"workspace-late","labels":{"oplcloud.cn/workspace-id":"late"}},"spec":{"template":{"spec":{"containers":[{"name":"workspace","image":"%s"}]}}}}]}' "\${images[workspace-slot-1]}" "\${images[workspace-late]}"
              fi
            elif [[ " $* " == *" configmap opl-cloud-config "* ]]; then
              printf '%s' "$config_image"
            else
              printf '%s' "\${images[$target]}"
            fi
            ;;
          patch)
            last="\${!#}"
            if [ "\${IGNORE_CONFIG_PATCH:-0}" != "1" ]; then
              config_image="$(node -e 'const value = JSON.parse(process.argv[1]); process.stdout.write((Array.isArray(value) ? value[0].value : value.data).OPL_WORKSPACE_IMAGE)' "$last")"
            fi
            ;;
          apply)
            last="\${!#}"
            if [ "\${IGNORE_CONFIG_PATCH:-0}" != "1" ]; then
              config_image="$(node -e 'const value = JSON.parse(process.argv[1]); process.stdout.write((Array.isArray(value) ? value[0].value : value.data).OPL_WORKSPACE_IMAGE)' "$last")"
            fi
            ;;
          set)
            if [ "$target" = "\${FAIL_TARGET:-}" ]; then
              return 42
            fi
            images[$target]="\${assignment#*=}"
            ;;
          rollout) ;;
        esac
      }
${functions}
      if [ "\${TEST_BOOTSTRAP_ONLY:-0}" = "1" ]; then
        apply_bootstrap_images
        printf '%s\n' "$config_image" "\${images[opl-cloud-control-plane]}" "\${images[opl-cloud-ledger]}" "\${images[opl-cloud-fabric]}" "\${images[workspace-slot-1]}" "\${images[workspace-late]}" > "$TEST_ROOT/bootstrap-candidate.txt"
        restore_previous_bootstrap_images
        printf '%s\n' "$config_image" "\${images[opl-cloud-control-plane]}" "\${images[opl-cloud-ledger]}" "\${images[opl-cloud-fabric]}" "\${images[workspace-slot-1]}" "\${images[workspace-late]}" > "$TEST_ROOT/bootstrap-restored.txt"
        apply_bootstrap_images
        printf '%s\n' "$config_image" "\${images[opl-cloud-control-plane]}" "\${images[opl-cloud-ledger]}" "\${images[opl-cloud-fabric]}" "\${images[workspace-slot-1]}" "\${images[workspace-late]}" > "$TEST_ROOT/bootstrap-exercised.txt"
        exit 0
      fi
      if [ "\${TEST_ROLLBACK_JOB_ONLY:-0}" = "1" ]; then
        restore_previous_images
        exit 0
      fi
      if [ "\${TEST_FAILURE_MODE:-0}" = "1" ]; then
        set +e
        restore_previous_images
        printf '%s\n' "$?" > "$TEST_ROOT/failure-status.txt"
        exit 0
      fi
      restore_previous_images
      printf '%s\n' "$config_image" "\${images[opl-cloud-control-plane]}" "\${images[opl-cloud-ledger]}" "\${images[opl-cloud-fabric]}" "\${images[workspace-slot-1]}" "\${images[workspace-late]}" > "$TEST_ROOT/restored.txt"
      apply_candidate_images
      printf '%s\n' "$config_image" "\${images[opl-cloud-control-plane]}" "\${images[opl-cloud-ledger]}" "\${images[opl-cloud-fabric]}" "\${images[workspace-slot-1]}" "\${images[workspace-late]}" > "$TEST_ROOT/candidate.txt"
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
    assert.deepEqual((await readFile(join(root, "restored.txt"), "utf8")).trim().split("\n"), [oldWorkspace, oldCloud, oldCloud, oldCloud, candidateWorkspace, candidateWorkspace]);
    assert.deepEqual((await readFile(join(root, "candidate.txt"), "utf8")).trim().split("\n"), [candidateWorkspace, candidateCloud, candidateCloud, candidateCloud, candidateWorkspace, candidateWorkspace]);

    const log = await readFile(join(root, "kubectl.log"), "utf8");
    for (const deployment of ["opl-cloud-control-plane", "opl-cloud-ledger", "opl-cloud-fabric"]) {
      assert.equal(log.match(new RegExp(`get deployment/${deployment}`, "g"))?.length, 2, `${deployment} must be read back after restore and reapply`);
    }
    assert.match(log, /patch configmap opl-cloud-config --type json -p/);
    assert.doesNotMatch(log, /(?:set image|rollout (?:restart|status)) deployment\/workspace-/);
    assert.equal(log.match(/get configmap opl-cloud-config/g)?.length, 2, "candidate and previous ConfigMap values must both be read back");

    const bootstrap = spawnSync("bash", ["-c", harness], {
      cwd: fileURLToPath(repoFile(".")),
      encoding: "utf8",
      env: {
        ...process.env,
        KUBECONFIG: "/dev/null",
        OPL_CLOUD_IMAGE: candidateCloud,
        OPL_K8S_NAMESPACE: "opl-test",
        OPL_WORKSPACE_IMAGE: candidateWorkspace,
        TEST_BOOTSTRAP_ONLY: "1",
        TEST_CURRENT_CLOUD_IMAGE: oldCloud,
        TEST_CURRENT_WORKSPACE_IMAGE: oldWorkspace,
        TEST_ROOT: root
      }
    });
    assert.equal(bootstrap.status, 0, bootstrap.stderr);
    assert.deepEqual((await readFile(join(root, "bootstrap-candidate.txt"), "utf8")).trim().split("\n"), [candidateWorkspace, candidateCloud, candidateCloud, candidateCloud, oldWorkspace, oldWorkspace]);
    assert.deepEqual((await readFile(join(root, "bootstrap-restored.txt"), "utf8")).trim().split("\n"), [oldWorkspace, oldCloud, oldCloud, oldCloud, oldWorkspace, oldWorkspace]);
    assert.deepEqual((await readFile(join(root, "bootstrap-exercised.txt"), "utf8")).trim().split("\n"), [candidateWorkspace, candidateCloud, candidateCloud, candidateCloud, oldWorkspace, oldWorkspace]);
    const bootstrapLog = await readFile(join(root, "kubectl.log"), "utf8");
    assert.doesNotMatch(bootstrapLog, /(?:set image|rollout (?:restart|status)) deployment\/workspace-/);
    assert.doesNotMatch(bootstrapLog, /get deployment -l oplcloud\.cn\/workspace-id/);

    const rollbackJobEnv = {
      ...process.env,
      KUBECONFIG: "/dev/null",
      OPL_K8S_NAMESPACE: "opl-test",
      TEST_CURRENT_CLOUD_IMAGE: candidateCloud,
      TEST_CURRENT_WORKSPACE_IMAGE: candidateWorkspace,
      TEST_ROLLBACK_JOB_ONLY: "1",
      TEST_ROOT: root
    };
    delete rollbackJobEnv.OPL_CLOUD_IMAGE;
    delete rollbackJobEnv.OPL_WORKSPACE_IMAGE;
    const rollbackOnly = spawnSync("bash", ["-c", harness], {
      cwd: fileURLToPath(repoFile(".")),
      encoding: "utf8",
      env: rollbackJobEnv
    });
    assert.equal(rollbackOnly.status, 0, rollbackOnly.stderr);

    const failedRestore = spawnSync("bash", ["-c", harness], {
      cwd: fileURLToPath(repoFile(".")),
      encoding: "utf8",
      env: {
        ...process.env,
        FAIL_TARGET: "opl-cloud-control-plane",
        KUBECONFIG: "/dev/null",
        OPL_CLOUD_IMAGE: candidateCloud,
        OPL_K8S_NAMESPACE: "opl-test",
        OPL_WORKSPACE_IMAGE: candidateWorkspace,
        TEST_FAILURE_MODE: "1",
        TEST_ROOT: root
      }
    });
    assert.equal(failedRestore.status, 0, failedRestore.stderr);
    assert.equal((await readFile(join(root, "failure-status.txt"), "utf8")).trim(), "1");
    const failedLog = await readFile(join(root, "kubectl.log"), "utf8");
    for (const deployment of ["opl-cloud-control-plane", "opl-cloud-ledger", "opl-cloud-fabric"]) {
      assert.match(failedLog, new RegExp(`set image deployment/${deployment}`), `${deployment} restore must be attempted after a sibling failure`);
    }
    assert.doesNotMatch(failedLog, /set image deployment\/workspace-/);

    const ignoredConfigPatch = spawnSync("bash", ["-c", harness], {
      cwd: fileURLToPath(repoFile(".")),
      encoding: "utf8",
      env: {
        ...process.env,
        IGNORE_CONFIG_PATCH: "1",
        KUBECONFIG: "/dev/null",
        OPL_CLOUD_IMAGE: candidateCloud,
        OPL_K8S_NAMESPACE: "opl-test",
        OPL_WORKSPACE_IMAGE: candidateWorkspace,
        TEST_FAILURE_MODE: "1",
        TEST_ROOT: root
      }
    });
    assert.equal(ignoredConfigPatch.status, 0, ignoredConfigPatch.stderr);
    assert.equal((await readFile(join(root, "failure-status.txt"), "utf8")).trim(), "1");

    await writeFile(join(rollbackDir, "workspace-images.tsv"), "");
    const emptyWorkspaces = spawnSync("bash", ["-c", harness], {
      cwd: fileURLToPath(repoFile(".")),
      encoding: "utf8",
      env: {
        ...process.env,
        EMPTY_WORKSPACES: "1",
        KUBECONFIG: "/dev/null",
        OPL_CLOUD_IMAGE: candidateCloud,
        OPL_K8S_NAMESPACE: "opl-test",
        OPL_WORKSPACE_IMAGE: candidateWorkspace,
        TEST_ROOT: root
      }
    });
    assert.equal(emptyWorkspaces.status, 0, emptyWorkspaces.stderr);
    const emptyLog = await readFile(join(root, "kubectl.log"), "utf8");
    assert.equal(emptyLog.match(/get configmap opl-cloud-config/g)?.length, 2);
    assert.doesNotMatch(emptyLog, /set image deployment\/workspace-/);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
