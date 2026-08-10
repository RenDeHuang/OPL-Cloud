import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { chmod, mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
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
const isolatedSourceCheckoutPath = ".opl-checkouts/${{ github.run_id }}-${{ github.run_attempt }}-${{ github.job }}";
const isolatedSourceCheckoutDirectory = "${{ github.workspace }}/.opl-checkouts/${{ github.run_id }}-${{ github.run_attempt }}-${{ github.job }}";
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

async function runWorkflowArtifactGate(step, filename, artifact, env = {}) {
  const root = await mkdtemp(join(tmpdir(), "opl-workflow-artifact-gate-"));
  try {
		const artifactPath = join(root, filename);
		await writeFile(artifactPath, `${JSON.stringify(artifact)}\n`);
		return spawnSync("bash", ["-c", step.run], {
      cwd: fileURLToPath(repoFile(".")),
      encoding: "utf8",
      env: {
        ...process.env,
			OPL_COMPUTE_CLAIM_ARTIFACT_DIR: root,
			OPL_WORKSPACE_LAUNCH_READBACK_ARTIFACT_DIR: root,
			OPL_WORKSPACE_LAUNCH_READBACK_RAW_RESULT_PATH: artifactPath,
			OPL_WORKSPACE_LAUNCH_CONTINUATION_RAW_RESULT_PATH: artifactPath,
			OPL_COMPUTE_CLAIM_CONTINUATION_RAW_RESULT_PATH: artifactPath,
        ...env
      }
    });
  } finally {
    await rm(root, { recursive: true, force: true });
  }
}

function canonicalJson(value) {
  if (Array.isArray(value)) return `[${value.map(canonicalJson).join(",")}]`;
  if (!value || typeof value !== "object") return JSON.stringify(value);
  return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonicalJson(value[key])}`).join(",")}}`;
}

function runImageMetadata(step, appSha, shellSha, frameworkSha, {
  publishCloudImage = false,
  publishWorkspaceImage = true
} = {}) {
  return spawnSync("bash", ["-c", step.run], {
    cwd: fileURLToPath(repoFile(".")),
    encoding: "utf8",
    env: {
      ...process.env,
      GITHUB_ENV: "/dev/null",
      GITHUB_OUTPUT: "/dev/null",
      OPL_CLOUD_IMAGE_REPOSITORY: "registry.example.test/opl/cloud",
      OPL_WORKSPACE_IMAGE_REPOSITORY: "registry.example.test/opl/workspace",
      OPL_CLOUD_SHA: cloudCandidateSha,
      REQUESTED_IMAGE_TAG: "cloud-test",
      REQUESTED_WORKSPACE_APP_SHA: appSha,
      REQUESTED_WORKSPACE_SHELL_SHA: shellSha,
      REQUESTED_WORKSPACE_FRAMEWORK_SHA: frameworkSha,
      PUBLISH_CLOUD_IMAGE: publishCloudImage ? "true" : "false",
      PUBLISH_WORKSPACE_IMAGE: publishWorkspaceImage ? "true" : "false"
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
  for (const key of spec.forbiddenEnv || []) {
    assert.equal(Object.hasOwn(currentJob.env || {}, key), false, `${spec.file} ${spec.job} contains env ${key}`);
  }
  for (const [stepName, tokens] of Object.entries(spec.requiredCommandsByStep || {})) {
    const text = serializedStep(stepMap.get(stepName));
    for (const token of tokens) assert.ok(text.includes(token), `${spec.file} ${stepName} missing ${token}`);
  }

  const currentJobText = JSON.stringify(currentJob);
  for (const token of spec.forbiddenJobRunTokens || []) assert.equal(currentJobText.includes(token), false, `${spec.file} ${spec.job} contains ${token}`);
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
      OPL_RELEASE_SHA: cloudMainSha,
      OPL_IMAGE_PULL_SECRET_NAME: "pull-test",
      OPL_TENCENT_ZONE: "ap-guangzhou-3",
      TENCENTCLOUD_REGION: "ap-guangzhou",
      OPL_SUB2API_BASE_URL: "https://wallet.example.test",
      OPL_SUB2API_REQUEST_TIMEOUT_MS: "7000",
      OPL_MONTHLY_BILLING_WORKER_ENABLED: "1",
      OPL_MONTHLY_BILLING_INTERVAL_MS: "60000",
      OPL_WORKSPACE_LAUNCH_WORKER_ENABLED: "1",
      OPL_WORKSPACE_LAUNCH_INTERVAL_MS: "10000",
      OPL_BASIC_COMPUTE_INSTANCE_TYPE: "S5.MEDIUM4",
      OPL_PRO_COMPUTE_INSTANCE_TYPE: "S5.2XLARGE16",
      OPL_SYSTEM_COMPUTE_MACHINE_TYPE: "NativeCVM",
      OPL_SYSTEM_COMPUTE_CVM_ID: "ins-systemtest",
      OPL_BASIC_COMPUTE_NODE_POOL_ID: "np-basic-test",
      OPL_PRO_COMPUTE_NODE_POOL_ID: "np-pro-test",
      OPL_BASIC_COMPUTE_NODE_POOL_MAX_REPLICAS: "50",
      OPL_PRO_COMPUTE_NODE_POOL_MAX_REPLICAS: "50"
    }
  };
}

const cloudRolloutTargets = [
  { name: "opl-cloud-control-plane", component: "control-plane", container: "control-plane" },
  { name: "opl-cloud-ledger", component: "ledger", container: "ledger" },
  { name: "opl-cloud-fabric", component: "fabric", container: "fabric" }
];

function rolloutFixture(image, revision = "241") {
  const deployments = [];
  const replicasets = [];
  const pods = [];
  for (const target of cloudRolloutTargets) {
    const deploymentUid = `deployment-${target.component}-uid`;
    const replicaSetName = `${target.name}-current`;
    const replicaSetUid = `replicaset-${target.component}-uid`;
    deployments.push({
      metadata: {
        name: target.name,
        uid: deploymentUid,
        generation: 2,
        annotations: { "deployment.kubernetes.io/revision": revision }
      },
      spec: { replicas: 1, template: { spec: { containers: [{ name: target.container, image }] } } },
      status: {
        observedGeneration: 2,
        updatedReplicas: 1,
        readyReplicas: 1,
        availableReplicas: 1,
        unavailableReplicas: 0
      }
    });
    replicasets.push({
      metadata: {
        name: replicaSetName,
        uid: replicaSetUid,
        annotations: { "deployment.kubernetes.io/revision": revision },
        ownerReferences: [{
          apiVersion: "apps/v1",
          kind: "Deployment",
          name: target.name,
          uid: deploymentUid,
          controller: true
        }]
      },
      spec: { replicas: 1, template: { spec: { containers: [{ name: target.container, image }] } } }
    });
    pods.push({
      metadata: {
        name: `${replicaSetName}-pod`,
        labels: { "app.kubernetes.io/component": target.component },
        ownerReferences: [{
          apiVersion: "apps/v1",
          kind: "ReplicaSet",
          name: replicaSetName,
          uid: replicaSetUid,
          controller: true
        }]
      },
      spec: { containers: [{ name: target.container, image }] },
      status: {
        conditions: [{ type: "Ready", status: "True" }],
        containerStatuses: [{ name: target.container, state: { running: {} } }]
      }
    });
  }
  return { deployments, replicasets, pods };
}

function addHistoricalEvictedFabricPod(fixture, image, revision = "236") {
  const deployment = fixture.deployments.find((item) => item.metadata.name === "opl-cloud-fabric");
  const replicaSetName = "opl-cloud-fabric-historical";
  const replicaSetUid = "replicaset-fabric-historical-uid";
  fixture.replicasets.push({
    metadata: {
      name: replicaSetName,
      uid: replicaSetUid,
      annotations: { "deployment.kubernetes.io/revision": revision },
      ownerReferences: [{
        apiVersion: "apps/v1",
        kind: "Deployment",
        name: deployment.metadata.name,
        uid: deployment.metadata.uid,
        controller: true
      }]
    },
    spec: { replicas: 0, template: { spec: { containers: [{ name: "fabric", image }] } } }
  });
  fixture.pods.push({
    metadata: {
      name: `${replicaSetName}-evicted`,
      labels: { "app.kubernetes.io/component": "fabric" },
      ownerReferences: [{
        apiVersion: "apps/v1",
        kind: "ReplicaSet",
        name: replicaSetName,
        uid: replicaSetUid,
        controller: true
      }]
    },
    spec: { containers: [{ name: "fabric", image }] },
    status: { reason: "Evicted" }
  });
}

function setRolloutPodFailure(pod, reason) {
  pod.status = { conditions: [], containerStatuses: [] };
  if (reason === "Evicted") {
    pod.status.reason = reason;
  } else if (reason === "Unschedulable") {
    pod.status.conditions = [{ type: "PodScheduled", status: "False", reason }];
  } else {
    pod.status.containerStatuses = [{ name: "fabric", state: { waiting: { reason } } }];
  }
}

async function runRolloutObserver({ fixture, image, mode = "candidate", previousImage = image }) {
  const functions = await readFile(repoFile("tools/tke-image-rollout.sh"), "utf8");
  const root = await mkdtemp(join(tmpdir(), "opl-rollout-owner-chain-"));
  const rollbackDir = join(root, "previous-images");
  const commandLog = join(root, "commands.log");
  await mkdir(rollbackDir);
  await Promise.all([
    writeFile(commandLog, ""),
    ...cloudRolloutTargets.map((target) => writeFile(join(rollbackDir, target.name), previousImage))
  ]);
  const harness = `
kubectl() {
  printf 'kubectl %s\\n' "$*" >> "$TEST_COMMAND_LOG"
  case " $* " in
    *" get nodes -o json "*) printf '%s' "$TEST_NODES_JSON" ;;
    *" get deployment opl-cloud-control-plane opl-cloud-ledger opl-cloud-fabric -o json "*) printf '%s' "$TEST_DEPLOYMENTS_JSON" ;;
    *" get replicasets -l app.kubernetes.io/name=opl-cloud -o json "*) printf '%s' "$TEST_REPLICASETS_JSON" ;;
    *" get pods -l app.kubernetes.io/name=opl-cloud -o json "*) printf '%s' "$TEST_PODS_JSON" ;;
    *) return 64 ;;
  esac
}
sleep() {
  printf 'sleep %s\\n' "$*" >> "$TEST_COMMAND_LOG"
  exit 73
}
${functions}
rollback_dir="$TEST_ROLLBACK_DIR"
wait_cloud_rollouts "$TEST_MODE"
`;
  try {
    const result = spawnSync("bash", ["-c", harness], {
      cwd: fileURLToPath(repoFile(".")),
      encoding: "utf8",
      env: {
        ...process.env,
        KUBECONFIG: "/dev/null",
        OPL_CLOUD_IMAGE: image,
        OPL_K8S_NAMESPACE: "opl-test",
        OPL_ROLLOUT_POLL_SECONDS: "1",
        OPL_ROLLOUT_STATE_DIR: join(root, "state"),
        OPL_ROLLOUT_TIMEOUT_SECONDS: "5",
        TEST_COMMAND_LOG: commandLog,
        TEST_DEPLOYMENTS_JSON: JSON.stringify({ items: fixture.deployments }),
        TEST_MODE: mode,
        TEST_NODES_JSON: JSON.stringify({ items: [{
          metadata: { name: "10.66.0.42" },
          status: { conditions: [{ type: "DiskPressure", status: "False" }] }
        }] }),
        TEST_PODS_JSON: JSON.stringify({ items: fixture.pods }),
        TEST_REPLICASETS_JSON: JSON.stringify({ items: fixture.replicasets }),
        TEST_ROLLBACK_DIR: rollbackDir
      }
    });
    return { result, commands: await readFile(commandLog, "utf8") };
  } finally {
    await rm(root, { recursive: true, force: true });
  }
}

test("TKE deploy workflow matches the current deployment contract", async () => {
  const contract = await readJson(deploymentContractPath);
  const deployWorkflow = await readWorkflow(contract.deployWorkflow.file);
  assertWorkflowContract(deployWorkflow, contract.deployWorkflow, contract);
  assertWorkflowContract(deployWorkflow, contract.productionDiagnosticsJob, contract);
  assert.ok(contract.deployWorkflow.requiredEnv.includes("OPL_TENCENT_ZONE"));
  assert.deepEqual(contract.deployWorkflow.preDebitTencentMutationGate, {
    env: "RUN_TENCENT_CREATE_RELEASE_EXECUTION",
    requiredValue: "1",
    enforcement: "shared_tencent_monthly_preflight"
  });
  assert.deepEqual(contract.deployWorkflow.preDebitTencentIamGate, {
    proofMode: "production_runner_deployment_attestation",
    releaseBinding: "exact_merged_OPL_RELEASE_SHA",
    identityAuthority: "live_STS_GetCallerIdentity_at_deploy_and_preflight",
    identityFields: ["type", "principalId", "accountId", "userId"],
    policyDigestEnv: "TENCENT_MUTATION_IAM_POLICY_DIGEST",
    policyDigestAuthority: "protected_release_owner_verified_production_policy_digest",
    requiredTencentActions: ["tag:TagResources", "tag:ModifyResourcesTagValue"],
    runtimeSecretKey: "OPL_TENCENT_PREDEBIT_IAM_ATTESTATION",
    consumer: "opl-cloud-fabric_only",
    tencentTagWriteCalls: 0,
    failure: "before_kubernetes_rbac_capacity_sub2api_debit_and_provider_write"
  });
  for (const key of [
    "OPL_OPERATOR_CIDRS",
    "OPL_TRUSTED_PROXY_CIDRS",
    "OPL_CODEX_BASE_URL",
    "OPL_GATEWAY_PUBLIC_BASE_URL",
    "OPL_PROVIDER_ACCEPTANCE_TOKEN",
    "OPL_VERIFY_BASIC_ACCOUNT_ID",
    "OPL_VERIFY_PRO_ACCOUNT_ID",
    "OPL_VERIFY_MUTATION_APPROVAL_ID"
  ]) {
    assert.equal(contract.deployWorkflow.requiredEnv.includes(key), false, key);
  }
  for (const key of [
    "OPL_SYSTEM_COMPUTE_NODE_POOL_ID",
    "OPL_SYSTEM_COMPUTE_MACHINE_ID",
    "OPL_SYSTEM_COMPUTE_NODE_NAME",
    "OPL_SYSTEM_COMPUTE_MACHINE_TYPE",
    "OPL_SYSTEM_COMPUTE_CVM_ID",
    "OPL_BASIC_COMPUTE_NODE_POOL_ID",
    "OPL_PRO_COMPUTE_NODE_POOL_ID",
    "OPL_BASIC_COMPUTE_NODE_POOL_MAX_REPLICAS",
    "OPL_PRO_COMPUTE_NODE_POOL_MAX_REPLICAS"
  ]) assert.equal(contract.deployWorkflow.requiredEnv.includes(key), true, key);
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
  assertWorkflowContract(deployWorkflow, contract.productionRolloutClusterVerifierJob, contract);
  assertWorkflowContract(deployWorkflow, contract.productionPublicReadOnlyVerifierJob, contract);
  assert.equal(contract.productionReleaseGateJob.bootstrapConclusion, "release_incomplete_failure");
  assertWorkflowContract(deployWorkflow, contract.productionReleaseGateJob, contract);
  assert.equal(contract.productionLegacySecretCleanupJob, undefined);
  assertWorkflowContract(deployWorkflow, contract.productionFailureDiagnosticsJob, contract);
  assert.deepEqual(contract.deployWorkflow.nodeStoragePreflight, {
    identitySource: "kubectl_get_nodes_tke_native_identity_labels",
    identityBindings: {
      nodePoolId: {
        env: "OPL_SYSTEM_COMPUTE_NODE_POOL_ID",
        nodeLabel: "node.tke.cloud.tencent.com/machineset"
      },
      machineId: {
        env: "OPL_SYSTEM_COMPUTE_MACHINE_ID",
        nodeLabel: "node.tke.cloud.tencent.com/machine"
      },
      nodeName: {
        env: "OPL_SYSTEM_COMPUTE_NODE_NAME",
        nodeField: "metadata.name"
      }
    },
    candidateScope: "configured_system_node_only",
    nonSystemNodes: "ignored_for_preflight_identity_health_and_storage",
    purchaseCapacityGates: "bootstrap_only_not_ordinary_rollout",
    diskPressure: "False",
    source: "kubelet_stats_summary",
    filesystems: ["nodefs", "imagefs"],
    minimumAvailableBytes: 25 * 1024 * 1024 * 1024,
    failureBehavior: "fail_before_any_kubectl_apply"
  });
  assert.doesNotMatch(JSON.stringify(contract.deployWorkflow.nodeStoragePreflight), /\b(np|ins)-[A-Za-z0-9-]+\b|\b10\.\d+\.\d+\.\d+\b/);
  assert.deepEqual(contract.deployWorkflow.cloudRollout, {
    deployments: ["opl-cloud-control-plane", "opl-cloud-ledger", "opl-cloud-fabric"],
    candidateRevisionPerDeployment: 1,
    candidateMutation: "single_manifest_apply",
    sharedDeadlineSeconds: 300,
    failFastReasons: ["Evicted", "DiskPressure", "ImagePullBackOff", "CrashLoopBackOff", "Unschedulable"],
    observerScope: {
      deployment: "current_uid_revision_and_expected_image",
      replicaSet: "deployment_controller_owner_uid_name_revision_and_expected_image",
      pod: "replicaset_controller_owner_uid",
      historicalTerminalPods: "diagnostics_only",
      candidateAndRollback: "same_owner_chain_rules"
    }
  });
  assert.equal(contract.productionRollbackJob.trigger, "post_diagnostics_artifact_upload");
  assertWorkflowContract(deployWorkflow, contract.productionRollbackJob, contract);
  assert.deepEqual(contract.imageReleaseWorkflow.outputs, ["cloud_image", "workspace_image"]);
  assert.equal(contract.imageReleaseWorkflow.skippedOutput, "empty");
  assert.deepEqual(contract.imageReleaseWorkflow.workspaceSourceInputs, {
    required: false,
    requiredWhen: "publish_workspace_image=true",
    cloudOnlyBehavior: "not_validated_not_cloned_not_built"
  });
  assert.deepEqual(contract.deployWorkflow.workspaceImageInputPolicy, {
    required: false,
    ordinaryAuthority: "opl-cloud-config.data.OPL_WORKSPACE_IMAGE",
    providedValue: "must_equal_current_production_digest",
    bootstrapWithoutConfigMap: "explicit_digest_required",
    existingWorkspaceDeployments: "preserved_without_restart"
  });
  assert.doesNotMatch(JSON.stringify(contract), /paid_confirmation|OPL_VERIFY_PAID_CONFIRMATION|OPL_VERIFY_MODEL_ACCESS_KEY/);
});

test("production self-hosted jobs use one run-and-job isolated source checkout", async () => {
  const workflowJobs = new Map([
    [".github/workflows/deploy-tke-production.yml", [
      "deploy",
      "promote-workspace-image",
      "verify-rollout-cluster",
      "capture-rollout-failure",
      "rollback-live-qa"
    ]],
    [".github/workflows/production-basic-customer-operation.yml", [
      "prepare-basic-customer-operation",
      "workspace-identity-diagnose",
      "acceptance-b-account-reconcile",
      "acceptance-b-account-prepare",
      "acceptance-b-fresh-order",
      "controlled-pilot-closed-validate",
      "compute-pool-head-terminalization",
      "fabric-ledger-readback",
      "recovery-acceptance-funding-prepare",
      "recovery-acceptance-extra-funding-prepare",
      "recovery-acceptance-original-launch",
      "recovery-acceptance-canary",
      "recovery-plan-operation",
      "compute-claim-readback"
    ]],
    [".github/workflows/production-node-drift-diagnostic.yml", ["diagnose"]]
  ]);

  for (const [path, expectedJobs] of workflowJobs) {
    const workflow = await readWorkflow(path);
    const actualJobs = Object.entries(workflow.jobs || {})
      .filter(([, currentJob]) => JSON.stringify(currentJob["runs-on"]).includes("self-hosted"))
      .filter(([, currentJob]) => (currentJob.steps || []).some((step) => step.uses === "actions/checkout@v4"))
      .map(([name]) => name);
    assert.deepEqual(actualJobs, expectedJobs, `${path} isolated checkout coverage drifted`);

    for (const name of expectedJobs) {
      const currentJob = workflowJob(workflow, name);
      const steps = currentJob.steps || [];
      const checkoutIndex = steps.findIndex((step) => step.uses === "actions/checkout@v4");
      const checkout = steps[checkoutIndex];
      const prepare = steps[checkoutIndex - 1];
      const branchAuthority = steps[checkoutIndex + 1];
      const cleanup = steps.at(-1);

      assert.ok(checkoutIndex > 0, `${path}:${name} checkout must follow isolated-directory preparation`);
      assert.equal(checkout.with?.path, isolatedSourceCheckoutPath, `${path}:${name} checkout path`);
      assert.equal(prepare?.name, "Prepare isolated source checkout", `${path}:${name} prepare step`);
      assert.equal(prepare?.id, "prepare_source", `${path}:${name} prepare step id`);
      assert.equal(prepare?.env?.OPL_SOURCE_CHECKOUT, isolatedSourceCheckoutDirectory, `${path}:${name} prepare directory`);
      assert.match(serializedStep(prepare), /expected="\$GITHUB_WORKSPACE\/\.opl-checkouts\/\$GITHUB_RUN_ID-\$GITHUB_RUN_ATTEMPT-\$GITHUB_JOB"/);
      assert.match(serializedStep(prepare), /mkdir -p "\$GITHUB_WORKSPACE\/\.opl-checkouts"/);
      assert.match(serializedStep(prepare), /mkdir "\$OPL_SOURCE_CHECKOUT"/);
      assert.doesNotMatch(serializedStep(prepare), /\brm\b|find .* -delete|\*/);

      assert.equal(branchAuthority?.name, "Verify remote branch authority", `${path}:${name} branch authority step`);
      assert.match(serializedStep(branchAuthority), /git ls-remote --heads origin/);
      assert.match(serializedStep(branchAuthority), /refs\/heads\/main/);
      assert.match(serializedStep(branchAuthority), /"\$\{#remote_heads\[@\]\}" -ne 1/);
      assert.match(serializedStep(branchAuthority), /"\$GITHUB_REF" != "refs\/heads\/main"/);
      assert.match(serializedStep(branchAuthority), /head_sha="\$\(git rev-parse HEAD\)"/);
      assert.match(serializedStep(branchAuthority), /"\$GITHUB_SHA" != "\$head_sha"/);
      assert.match(serializedStep(branchAuthority), /"\$remote_main_sha" != "\$head_sha"/);
      assert.doesNotMatch(serializedStep(branchAuthority), /git (?:push|update-ref|branch -D|remote remove)|--delete/);

      assert.equal(cleanup?.name, "Remove isolated source checkout", `${path}:${name} cleanup step`);
      assert.equal(String(cleanup?.if), "always() && steps.prepare_source.outcome == 'success'", `${path}:${name} cleanup condition`);
      assert.equal(cleanup?.env?.OPL_SOURCE_CHECKOUT, isolatedSourceCheckoutDirectory, `${path}:${name} cleanup directory`);
      assert.match(serializedStep(cleanup), /source_dir="\$\{OPL_SOURCE_CHECKOUT:-\}"/);
      assert.match(serializedStep(cleanup), /expected="\$GITHUB_WORKSPACE\/\.opl-checkouts\/\$GITHUB_RUN_ID-\$GITHUB_RUN_ATTEMPT-\$GITHUB_JOB"/);
      if (name === "fabric-ledger-readback") {
        assert.match(serializedStep(cleanup), /rm -rf -- "\$source_dir"/);
        assert.doesNotMatch(serializedStep(cleanup), /find|\.worktrees|"\$GITHUB_WORKSPACE"|"\$RUNNER_TEMP"|\*/);
      } else {
        assert.match(serializedStep(cleanup), /find "\$source_dir" -mindepth 1 -delete/);
        assert.match(serializedStep(cleanup), /rmdir "\$source_dir"/);
        assert.doesNotMatch(serializedStep(cleanup), /\brm\b|\.worktrees|find "\$GITHUB_WORKSPACE"|find "\$RUNNER_TEMP"|\*/);
      }

      for (const step of steps.slice(checkoutIndex + 1, -1)) {
        if (step.run) {
          assert.equal(step["working-directory"], isolatedSourceCheckoutDirectory, `${path}:${name}:${step.name} working directory`);
        }
        if (["actions/upload-artifact@v4", "actions/download-artifact@v4"].includes(step.uses)) {
          assert.match(String(step.with?.path || ""), /^\$\{\{ (?:runner\.temp|env\.[A-Z0-9_]+) \}\}/, `${path}:${name}:${step.name} artifact path`);
        }
      }
    }
  }

  const contract = await readJson(deploymentContractPath);
  assert.deepEqual(contract.selfHostedSourceCheckoutIsolation, {
    workflows: Object.fromEntries(workflowJobs),
    relativePath: isolatedSourceCheckoutPath,
    absolutePath: isolatedSourceCheckoutDirectory,
    reuseDefaultWorkspaceRepository: false,
    prepare: "create_exact_empty_current_run_attempt_job_directory_fail_if_present_and_record_success",
    runSteps: "execute_from_exact_isolated_source_directory",
    cleanup: "only_after_prepare_success_delete_contents_and_rmdir_exact_current_run_attempt_job_directory",
    persistentRunnerState: "preserved",
    remoteBranchAuthority: {
      command: "git_ls_remote_heads_origin",
      acceptedHeads: ["refs/heads/main"],
      workflowRef: "refs/heads/main",
      sourceBinding: "github_sha_equals_checkout_head_equals_remote_main_sha",
      additionalHeads: "fail_closed",
      remoteMutation: 0
    }
  });
});

test("Workspace image promotion is an explicit main-only ConfigMap CAS with rollback", async () => {
  const [workflow, contract] = await Promise.all([
    readWorkflow(".github/workflows/deploy-tke-production.yml"),
    readJson(deploymentContractPath)
  ]);
  const inputs = workflow.on.workflow_dispatch.inputs;
  assert.equal(inputs.promote_workspace_image?.type, "boolean");
  assert.equal(inputs.promote_workspace_image?.default, false);
  assert.equal(inputs.workspace_promotion_confirmation?.type, "string");
  assert.equal(inputs.workspace_promotion_confirmation?.default, "");
  const promote = workflowJob(workflow, "promote-workspace-image");
  const rollback = workflowJob(workflow, "rollback-workspace-image-promotion");
  assert.match(String(promote.if), /inputs\.promote_workspace_image/);
  assert.match(String(promote.if), /!inputs\.diagnostics_only/);
  assert.match(String(promote.if), /!inputs\.bootstrap_mode/);
  assert.deepEqual(promote["runs-on"], ["self-hosted", "tencent-cloud", "opl-cloud", "tke-vpc"]);
  assert.equal(promote.environment, "production");
  const promoteText = serializedRuns(promote);
  for (const token of [
    "refs/heads/main",
    "git ls-remote origin refs/heads/main",
    "PROMOTE_WORKSPACE_IMAGE",
    "repository@sha256",
    "get configmap opl-cloud-config",
    "rollback",
    "replace -f",
    "OPL_WORKSPACE_IMAGE",
    "Workspace image promotion"
  ]) assert.match(promoteText, new RegExp(token.replace(/[.*+?^${}()|[\\]\\\\]/g, "\\$&")), token);
  assert.doesNotMatch(promoteText, /set image|rollout restart|apply -f .*rendered|tencentcloud|CreateCvm|CreateDisks|POST/);
  assert.deepEqual(rollback.needs, ["promote-workspace-image"]);
  assert.match(String(rollback.if), /always\(\)/);
  assert.match(String(rollback.if), /needs\.promote-workspace-image\.result != 'success'/);
  assert.equal(stepsByName(rollback).get("Download Workspace image promotion rollback snapshot")?.uses, "actions/download-artifact@v4");
  const rollbackText = serializedRuns(rollback);
  assert.match(rollbackText, /restore|replace -f "\$restore"/);
  assert.doesNotMatch(rollbackText, /set image|rollout restart|CreateCvm|CreateDisks/);
  assert.deepEqual(contract.workspaceImagePromotion, {
    file: ".github/workflows/deploy-tke-production.yml",
    job: "promote-workspace-image",
    rollbackJob: "rollback-workspace-image-promotion",
    confirmation: "PROMOTE_WORKSPACE_IMAGE",
    trigger: "main_only_explicit_promotion_not_bootstrap_or_diagnostics",
    mutation: "ConfigMap_OPL_WORKSPACE_IMAGE_only",
    concurrency: "production-resource-verification",
    existingWorkspaceDeployments: "preserved_without_restart",
    providerMutationCount: 0,
    cloudRollout: false,
    requestedImage: "immutable_tcr_repository_digest",
    currentValueGate: "CAS_test_current_ConfigMap_value",
    rollback: "snapshot_and_restore_previous_ConfigMap_data"
  });
});

test("Fabric MonthlyPreflight diagnostics runs inside the Ready Pod and is read only", async () => {
  const contract = await readJson(deploymentContractPath);
  const diagnostics = contract.fabricMonthlyPreflightDiagnosticsWorkflow;
  const workflow = await readWorkflow(diagnostics.file);
  const job = workflowJob(workflow, diagnostics.job);
  const runs = serializedRuns(job);

  assert.deepEqual(job["runs-on"], ["self-hosted", "tencent-cloud", "opl-cloud", "tke-vpc"]);
  assert.match(runs, /get deployment\/opl-cloud-fabric/);
  assert.match(runs, /get replicasets/);
  assert.match(runs, /get pods/);
  assert.match(runs, /ownerReferences/);
  assert.match(runs, /kubectl[^\n]+exec -i/);
  assert.match(runs, /http:\/\/127\.0\.0\.1:8082\/fabric\/monthly-preflight-report/);
  assert.match(runs, /searchParams\.set\("zone", zone\)/);
  assert.doesNotMatch(runs, /searchParams\.set\("packageId"/);
  assert.doesNotMatch(runs, /searchParams\.set\("sizeGb"/);
  assert.match(runs, /packageId[^\n]+basic/);
  assert.match(runs, /packageId[^\n]+pro/);
  assert.match(runs, /OPL_INTERNAL_SERVICE_TOKEN/);
  assert.match(JSON.stringify(job.steps), /actions\/upload-artifact@v4/);
  for (const forbidden of [" apply ", " patch ", " delete ", " scale ", " create ", "/api/workspace-launches", "control-plane"]) {
    assert.equal(runs.includes(forbidden), false, forbidden);
  }
});

test("dedicated NodePool bootstrap is the only manual CreateNodePool workflow", async () => {
  const workflow = await readWorkflow(".github/workflows/bootstrap-tke-workspace-nodepools.yml");
  const job = workflowJob(workflow, "bootstrap");
  const runs = serializedRuns(job);
  const inputs = workflow.on.workflow_dispatch.inputs;

  assert.deepEqual(job["runs-on"], ["self-hosted", "tencent-cloud", "opl-cloud", "tke-vpc"]);
  assert.equal(job.environment, "production");
  assert.equal(workflow.concurrency.group, "production-resource-verification");
  assert.equal(inputs.merged_sha.required, true);
  assert.equal(inputs.basic_resolved_instance_type, undefined);
  assert.equal(inputs.pro_resolved_instance_type, undefined);
  assert.equal(inputs.basic_max_replicas, undefined);
  assert.equal(inputs.pro_max_replicas, undefined);
  assert.equal(inputs.mutation_confirmation.required, false);
  assert.equal(inputs.mutate_missing_pools.default, "false");
  assert.equal(job.env.OPL_BASIC_COMPUTE_INSTANCE_TYPE, "");
  assert.equal(job.env.OPL_PRO_COMPUTE_INSTANCE_TYPE, "");
  assert.equal(job.env.OPL_SYSTEM_COMPUTE_MACHINE_TYPE, "${{ vars.OPL_SYSTEM_COMPUTE_MACHINE_TYPE }}");
  assert.equal(job.env.OPL_SYSTEM_COMPUTE_CVM_ID, "${{ vars.OPL_SYSTEM_COMPUTE_CVM_ID }}");
  assert.equal(job.env.OPL_BASIC_COMPUTE_NODE_POOL_MAX_REPLICAS, "50");
  assert.equal(job.env.OPL_PRO_COMPUTE_NODE_POOL_MAX_REPLICAS, "50");
  assert.equal(String(job.if), "${{ github.ref == 'refs/heads/main' && github.sha == inputs.merged_sha }}");
  const checkout = stepsByName(job).get("Checkout exact source");
  assert.equal(checkout.with.ref, "${{ inputs.merged_sha }}");
  assert.equal(checkout.with["fetch-depth"], 0);
  const sourceGate = serializedStep(stepsByName(job).get("Verify mutation source"));
  assert.match(sourceGate, /refs\/remotes\/origin\/main/);
  assert.match(sourceGate, /git rev-parse HEAD/);
  assert.match(sourceGate, /OPL_MERGED_SHA/);
  assert.match(runs, /bootstrap_compute_node_pools/);
  assert.match(String(job.env.RUN_TENCENT_NODE_POOL_BOOTSTRAP), /mutation_confirmation/);
  assert.match(String(job.env.RUN_TENCENT_NODE_POOL_BOOTSTRAP), /CREATE_MISSING_WORKSPACE_NODEPOOLS/);
  assert.equal(job.env.RUN_TENCENT_NODE_POOL_BOOTSTRAP_CONFIRMATION, "${{ inputs.mutation_confirmation }}");
  assert.doesNotMatch(runs, /get node "\$OPL_SYSTEM_COMPUTE_NODE_NAME" -o json|providerID/);
  assert.match(runs, /actions\/upload-artifact@v4|bootstrap-nodepool-report/);
  assert.match(runs, /workspace_sku_inventory/);
  assert.match(runs, /requiredCapacity[^\n]+1/);
  assert.match(runs, /recommendedInstanceType/);
  assert.match(runs, /pre-mutation-sku-inventory\.json/);
  assert.match(runs, /prepaidQuotaRemaining/);
  assert.match(runs, /subnets/);
  assert.match(runs, /tkeClusterNodeLimit/);
  assert.match(runs, /protectedSystem/);
  assert.match(runs, /poolCheckStatus/);
  assert.match(runs, /machineCheckStatus/);
  assert.match(runs, /nodeCheckStatus/);
  assert.match(runs, /cvmCheckStatus/);
  assert.match(runs, /cvmApplicable/);
  assert.match(runs, /OPL_SYSTEM_COMPUTE_MACHINE_TYPE=\$\{system\.machineType\}/);
  assert.ok(
    [...stepsByName(job).keys()].indexOf("Build current provisioner") <
      [...stepsByName(job).keys()].indexOf("Inventory and select Workspace SKUs")
  );
  assert.match(runs, /"requestid"/);
  assert.match(runs, /OPL_BASIC_COMPUTE_INSTANCE_TYPE/);
  assert.match(runs, /OPL_PRO_COMPUTE_INSTANCE_TYPE/);
  assert.match(runs, /instanceType/);
  const reportGate = serializedStep(stepsByName(job).get("Validate bootstrap report"));
  assert.match(reportGate, /sku-inventory\.json/);
  assert.match(reportGate, /current\.candidates/);
  assert.match(reportGate, /nodePoolId/);
  assert.match(reportGate, /nodePoolInventoryBeforeMutation/);
  assert.match(reportGate, /bootstrap_nodepool_inventory_before_mutation_invalid/);
  assert.doesNotMatch(runs, /workspace-launches|control-plane|ScaleNodePool|DeleteClusterMachines/);
});

test("bootstrap report validation preserves an earlier inventory failure when bootstrap did not run", async () => {
  const workflow = await readWorkflow(".github/workflows/bootstrap-tke-workspace-nodepools.yml");
  const steps = stepsByName(workflowJob(workflow, "bootstrap"));
  const validation = steps.get("Validate bootstrap report");
  const upload = steps.get("Upload bootstrap report");
  const root = await mkdtemp(join(tmpdir(), "opl-bootstrap-report-"));

  try {
    await writeFile(join(root, "sku-inventory.json"), JSON.stringify({
      ok: false,
      errorCode: "workspace_sku_inventory_unavailable",
      mutationCount: 0
    }));
    const result = spawnSync("bash", ["-c", validation.run], {
      cwd: fileURLToPath(repoFile(".")),
      encoding: "utf8",
      env: {
        ...process.env,
        OPL_BOOTSTRAP_ARTIFACT_DIR: root,
        BOOTSTRAP_ACTION_OUTCOME: "skipped"
      }
    });

    assert.equal(result.status, 0, `${result.stdout}\n${result.stderr}`);
    assert.doesNotMatch(`${result.stdout}\n${result.stderr}`, /ENOENT/);
    assert.equal(String(upload.if), "always()");
    assert.match(String(upload.with.path), /bootstrap-nodepool-report/);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("manual production Basic customer operation supports full and recovered precharge approvals behind merged-main", async () => {
  const path = ".github/workflows/production-basic-customer-operation.yml";
  const workflow = await readWorkflow(path);
  const prepareJob = workflowJob(workflow, "prepare-basic-customer-operation");
  const completeJob = workflowJob(workflow, "complete-basic-customer-operation");
  const inputs = workflow.on.workflow_dispatch.inputs;
  const prepareSteps = stepsByName(prepareJob);
  const completeSteps = stepsByName(completeJob);
  const prepareRuns = serializedRuns(prepareJob);
  const completeRuns = serializedRuns(completeJob);

  assert.deepEqual(Object.keys(workflow.on), ["workflow_dispatch"]);
  assert.deepEqual(prepareJob["runs-on"], ["self-hosted", "tencent-cloud", "opl-cloud", "tke-vpc"]);
  assert.equal(completeJob["runs-on"], "ubuntu-latest");
  assert.equal(completeJob.needs, "prepare-basic-customer-operation");
  assert.equal(prepareJob.environment, "production");
  assert.equal(completeJob.environment, "production");
  assert.equal(workflow.concurrency.group, "production-resource-verification");
  assert.equal(workflow.concurrency["cancel-in-progress"], false);
  assert.equal(inputs.merged_sha.required, true);
  assert.equal(inputs.approval_id.required, false);
  assert.equal(inputs.funding_mode.type, "choice");
  assert.equal(inputs.funding_mode.required, true);
  assert.equal(inputs.funding_mode.default, "operator_precharge");
  assert.deepEqual(inputs.funding_mode.options, ["operator_precharge", "operator_precharge_recovery"]);
  for (const name of ["confirm_account_provision", "confirm_wallet_recharge", "confirm_workspace_purchase", "confirm_single_model_request"]) {
    assert.equal(inputs[name].type, "boolean");
    assert.equal(inputs[name].required, true);
    assert.equal(inputs[name].default, false);
  }
  for (const job of [prepareJob, completeJob]) {
    assert.match(String(job.if), /inputs\.funding_mode == 'operator_precharge'/);
    assert.match(String(job.if), /inputs\.funding_mode == 'operator_precharge_recovery'/);
    assert.match(String(job.if), /inputs\.confirm_account_provision/);
    assert.match(String(job.if), /inputs\.confirm_wallet_recharge/);
    assert.match(String(job.if), /inputs\.confirm_workspace_purchase/);
    assert.match(String(job.if), /inputs\.confirm_single_model_request/);
    assert.match(String(job.if), /!inputs\.confirm_account_provision/);
    assert.match(String(job.if), /!inputs\.confirm_wallet_recharge/);
  }
  for (const job of [prepareJob, completeJob]) {
    assert.match(String(job.if), /github\.ref == 'refs\/heads\/main'/);
    assert.match(String(job.if), /github\.sha == inputs\.merged_sha/);
  }

  for (const steps of [prepareSteps, completeSteps]) {
    const checkout = steps.get("Checkout exact merged main");
    assert.equal(checkout.with.ref, "${{ inputs.merged_sha }}");
    assert.equal(checkout.with["fetch-depth"], 0);
    const sourceGate = serializedStep(steps.get("Verify exact origin main"));
    assert.match(sourceGate, /git rev-parse HEAD/);
    assert.match(sourceGate, /refs\/remotes\/origin\/main/);
    assert.match(sourceGate, /OPL_MERGED_SHA/);

    const recoveryResumeGate = steps.get("Reject recovered-precharge checkpoint resume");
    const recoveryResumeRun = serializedStep(recoveryResumeGate);
    assert.match(recoveryResumeRun, /operator_precharge_recovery/);
    assert.match(recoveryResumeRun, /OPL_BASIC_CANARY_RESUME_RUN_ID/);
    assert.match(recoveryResumeRun, /exit 1/);
    const emptyRecoveryResume = spawnSync("bash", ["-c", recoveryResumeGate?.run || ""], {
      cwd: fileURLToPath(repoFile(".")),
      encoding: "utf8",
      env: {
        ...process.env,
        OPL_BASIC_CANARY_FUNDING_MODE: "operator_precharge_recovery",
        OPL_BASIC_CANARY_RESUME_RUN_ID: ""
      }
    });
    assert.equal(emptyRecoveryResume.status, 0, `${emptyRecoveryResume.stdout}\n${emptyRecoveryResume.stderr}`);
    const staleRecoveryResume = spawnSync("bash", ["-c", recoveryResumeGate?.run || ""], {
      cwd: fileURLToPath(repoFile(".")),
      encoding: "utf8",
      env: {
        ...process.env,
        OPL_BASIC_CANARY_FUNDING_MODE: "operator_precharge_recovery",
        OPL_BASIC_CANARY_RESUME_RUN_ID: "30326406621"
      }
    });
    assert.notEqual(staleRecoveryResume.status, 0);
  }
  const priorCheckpointDownload = prepareSteps.get("Download prior redacted checkpoint");
  assert.match(String(priorCheckpointDownload?.if || ""), /inputs\.funding_mode == 'operator_precharge'/);
  assert.match(String(priorCheckpointDownload?.if || ""), /inputs\.resume_run_id != ''/);
  assert.doesNotMatch(prepareRuns, /npm ci|playwright|chromium/);
  assert.match(completeRuns, /npm ci/);
  assert.match(completeRuns, /playwright install --with-deps chromium/);
  assert.match(prepareRuns, /production-live-qa\.ts --basic-customer-canary/);
  assert.match(prepareRuns, /--phase prepare/);
  assert.match(completeRuns, /production-live-qa\.ts --basic-customer-canary/);
  assert.match(completeRuns, /--phase complete/);
  assert.match(completeRuns, /--prepared-evidence/);
  for (const flag of ["--allow-account-provision", "--allow-wallet-recharge", "--allow-existing-precharge-recovery", "--allow-workspace-purchase", "--allow-model-write"]) {
    assert.match(prepareRuns, new RegExp(flag));
    assert.match(completeRuns, new RegExp(flag));
  }
  assert.match(prepareRuns, /--funding-mode "\$OPL_BASIC_CANARY_FUNDING_MODE"/);
  assert.match(completeRuns, /--funding-mode "\$OPL_BASIC_CANARY_FUNDING_MODE"/);
  assert.match(prepareRuns, /--approval-id "\$OPL_BASIC_CANARY_APPROVAL_ID"/);
  assert.match(prepareRuns, /OPL_BASIC_CANARY_CHECKPOINT_PATH/);
  assert.match(JSON.stringify(prepareJob.steps), /actions\/upload-artifact@v4/);
  assert.match(JSON.stringify(completeJob.steps), /actions\/download-artifact@v4/);
  assert.match(JSON.stringify(completeJob.steps), /actions\/upload-artifact@v4/);
  for (const job of [prepareJob, completeJob]) {
    assert.equal(job.env.OPL_BASIC_CANARY_APPROVAL_ID, "${{ inputs.approval_id }}");
    assert.equal(job.env.OPL_BASIC_CANARY_FUNDING_MODE, "${{ inputs.funding_mode }}");
    assert.equal(job.env.OPL_BASIC_CANARY_RESUME_RUN_ID, "${{ inputs.resume_run_id }}");
    assert.equal(job.env.OPL_MERGED_SHA, "${{ inputs.merged_sha }}");
    assert.equal(job.env.OPL_BASIC_CANARY_APPROVAL_JSON, "${{ secrets.OPL_BASIC_CANARY_APPROVAL_JSON }}");
    assert.equal(job.env.OPL_BASIC_CANARY_CUSTOMER_PASSWORD, "${{ secrets.OPL_BASIC_CANARY_CUSTOMER_PASSWORD }}");
  }
  assert.equal(prepareJob.env.OPL_INTERNAL_SERVICE_TOKEN, undefined);
  assert.equal(prepareJob.env.OPL_FABRIC_INTERNAL_ORIGIN, undefined);
  assert.match(prepareRuns, /get secret opl-cloud-internal-service/);
  assert.match(prepareRuns, /\.data\.OPL_INTERNAL_SERVICE_TOKEN/);
  assert.match(prepareRuns, /::add-mask::/);
  assert.match(prepareRuns, /get deployment\/opl-cloud-fabric/);
  assert.match(prepareRuns, /deployment\.kubernetes\.io\/revision/);
  assert.match(prepareRuns, /ownerReferences/);
  assert.match(prepareRuns, /port-forward[^\n]+pod\/\$fabric_pod[^\n]+18082:8082/);
  assert.match(prepareRuns, /OPL_FABRIC_INTERNAL_ORIGIN=http:\/\/127\.0\.0\.1:18082/);
  assert.match(prepareRuns, /trap[^\n]+EXIT/);
  assert.match(prepareRuns, /kill[^\n]+port_forward_pid/);
  assert.doesNotMatch(JSON.stringify(completeJob), /KUBECONFIG|TENCENT_|OPL_INTERNAL_SERVICE_TOKEN|port-forward|kubectl/);
  const deployment = await readJson(repoFile("packages/contracts/opl-cloud-deployment-contract.json"));
  assert.deepEqual(deployment.productionBasicCustomerCanary.runnerIsolation, {
    prepare: "self_hosted_tke_vpc_revision_fabric_kubernetes_and_business_authority",
    complete: "ubuntu_latest_public_browser_websocket_and_single_model_request",
    sharedConcurrency: "production-resource-verification",
    hostedRunnerKubeconfig: false,
    vpcRunnerBrowser: false
  });
  const liveQa = await readFile(repoFile("tools/production-live-qa.ts"), "utf8");
  assert.match(liveQa, /readBasicCanaryCloudRevisionEvidence/);
  assert.match(liveQa, /deployment\.kubernetes\.io\/revision/);
  assert.match(liveQa, /production_basic_canary_model_result_unknown/);
  assert.doesNotMatch(JSON.stringify(workflow.on), /pull_request|push|workflow_call|schedule/);

  for (const other of [".github/workflows/pull-request-ci.yml", ".github/workflows/deploy-tke-production.yml", ".github/workflows/release-opl-cloud-image.yml"]) {
    assert.doesNotMatch(await readFile(repoFile(other), "utf8"), /production-basic-customer-operation/);
  }
});

test("Workspace identity diagnosis is a main-only production runner readback with no mutation capability", async () => {
  const workflow = await readWorkflow(".github/workflows/production-basic-customer-operation.yml");
  const inputs = workflow.on.workflow_dispatch.inputs;
  assert.ok(inputs.operation_mode.options.includes("workspace_identity_diagnose"));
  assert.equal(inputs.workspace_identity_account_id.required, false);
  assert.equal(inputs.workspace_identity_workspace_id.required, false);

  const job = workflowJob(workflow, "workspace-identity-diagnose");
  const runs = serializedRuns(job);
  assert.deepEqual(job["runs-on"], ["self-hosted", "tencent-cloud", "opl-cloud", "tke-vpc"]);
  assert.equal(job.environment, "production");
  assert.match(String(job.if), /github\.ref == 'refs\/heads\/main'/);
  assert.match(String(job.if), /github\.sha == inputs\.merged_sha/);
  assert.match(String(job.if), /inputs\.operation_mode == 'workspace_identity_diagnose'/);
  assert.match(String(job.if), /inputs\.workspace_identity_account_id != ''/);
  assert.match(String(job.if), /inputs\.workspace_identity_workspace_id != ''/);
  for (const confirmation of ["account_provision", "wallet_recharge", "workspace_purchase", "single_model_request"]) {
    assert.match(String(job.if), new RegExp(`!inputs\\.confirm_${confirmation}`));
  }
  assert.equal(job.env.OPL_WORKSPACE_IDENTITY_ACCOUNT_ID, "${{ inputs.workspace_identity_account_id }}");
  assert.equal(job.env.OPL_WORKSPACE_IDENTITY_WORKSPACE_ID, "${{ inputs.workspace_identity_workspace_id }}");
  assert.equal(job.env.OPL_WORKSPACE_IDENTITY_CUSTOMER_EMAIL, "${{ inputs.customer_email }}");
  assert.equal(job.env.OPL_WORKSPACE_IDENTITY_CUSTOMER_PASSWORD, "${{ secrets.OPL_BASIC_CANARY_CUSTOMER_PASSWORD }}");
  assert.match(runs, /production-live-qa\.ts --workspace-identity-diagnose/);
  assert.match(runs, /workspace-identity-diagnosis\.json/);
  assert.match(JSON.stringify(job.steps), /actions\/upload-artifact@v4/);
  assert.doesNotMatch(JSON.stringify(job), /KUBECONFIG|TENCENT_|OPL_INTERNAL_SERVICE_TOKEN/);
  assert.doesNotMatch(runs, /kubectl|--basic-customer-canary|--compute-claim-recover|allow-workspace-purchase|allow-wallet-recharge|allow-account-provision|allow-model-write|create_storage_volume|CreateComputeAllocation|scale|debit|refund/i);
});

test("Acceptance B fresh order is a separately approved exact-count production closure", async () => {
  const workflow = await readWorkflow(".github/workflows/production-basic-customer-operation.yml");
  const inputs = workflow.on.workflow_dispatch.inputs;
  assert.ok(inputs.operation_mode.options.includes("acceptance_b_fresh_order"));
  assert.equal(inputs.confirm_workspace_purchase.type, "boolean");
  assert.equal(inputs.confirm_single_model_request.type, "boolean");

  const job = workflowJob(workflow, "acceptance-b-fresh-order");
  const runs = serializedRuns(job);
  assert.deepEqual(job["runs-on"], ["self-hosted", "tencent-cloud", "opl-cloud", "tke-vpc"]);
  assert.equal(job.environment, "production");
  assert.match(String(job.if), /github\.ref == 'refs\/heads\/main'/);
  assert.match(String(job.if), /github\.sha == inputs\.merged_sha/);
  assert.match(String(job.if), /inputs\.operation_mode == 'acceptance_b_fresh_order'/);
  assert.match(String(job.if), /inputs\.operation_mode == 'acceptance_b_fresh_readback'/);
  assert.match(String(job.if), /inputs\.confirm_workspace_purchase/);
  assert.match(String(job.if), /!inputs\.confirm_single_model_request/);
  assert.match(String(job.if), /!inputs\.confirm_account_provision/);
  assert.match(String(job.if), /!inputs\.confirm_wallet_recharge/);
  assert.match(String(job.if), /inputs\.resume_run_id == ''/);

  assert.equal(job.env.OPL_MERGED_SHA, "${{ inputs.merged_sha }}");
  assert.equal(job.env.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_APPROVAL_ID, "${{ inputs.approval_id }}");
  assert.equal(job.env.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_APPROVAL_JSON, "${{ secrets.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_APPROVAL_JSON }}");
  assert.equal(job.env.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_ARTIFACT_PATH, undefined);
  assert.equal(job.env.OPL_SUB2API_ADMIN_EMAIL, "${{ secrets.OPL_SUB2API_ADMIN_EMAIL }}");
  assert.equal(job.env.OPL_SUB2API_ADMIN_PASSWORD, "${{ secrets.OPL_SUB2API_ADMIN_PASSWORD }}");
  assert.equal(job.env.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_CUSTOMER_PASSWORD, "${{ secrets.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_CUSTOMER_PASSWORD }}");
  assert.equal(job.env.OPL_BASIC_CANARY_CUSTOMER_PASSWORD, undefined);
  assert.equal(job.env.OPL_INTERNAL_SERVICE_TOKEN, undefined);
  assert.equal(job.env.TENCENT_DEPLOY_KUBECONFIG_B64, "${{ secrets.TENCENT_DEPLOY_KUBECONFIG_B64 }}");
  assert.equal(job.env.TENCENT_DEPLOY_KUBECONFIG, "${{ secrets.TENCENT_DEPLOY_KUBECONFIG }}");
  const acceptanceStep = stepsByName(job).get("Run one fresh Basic order with authoritative readback");
  const validationStep = stepsByName(job).get("Validate redacted Acceptance B evidence and exact counts");
  for (const step of [acceptanceStep, validationStep]) {
    assert.equal(step?.env?.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_ARTIFACT_PATH, "${{ runner.temp }}/production-basic-acceptance-b/evidence.json");
  }
  assert.match(runs, /git ls-remote --heads origin/);
  assert.match(runs, /node tools\/production-basic-acceptance-b\.ts --run/);
  assert.match(runs, /validateProductionBasicAcceptanceBArtifact/);
  assert.doesNotMatch(runs, /Acceptance B write count mismatch/);
  assert.match(runs, /podImageId/);
  assert.match(runs, /statusCode.*200|statusCode === 200/);
  assert.match(JSON.stringify(job.steps), /actions\/upload-artifact@v4/);
  assert.match(runs, /redacted|forbiddenFields|password|token|secret/);
  for (const forbidden of [
    /--basic-customer-canary/,
    /--allow-model-write/,
    /--allow-account-provision/,
    /--allow-wallet-recharge/,
    /wallet-adjustments/,
    /send_model_request/
  ]) assert.doesNotMatch(runs, forbidden);
  const acceptanceTool = await readFile(repoFile("tools/production-basic-acceptance-b.ts"), "utf8");
  assert.match(acceptanceTool, /x-opl-acceptance-b-capability/i);
  assert.match(acceptanceTool, /x-opl-acceptance-b-approval-id/i);
  for (const forbiddenWrite of ["provision_account", "adjust_wallet", "refund", "renew", "delete", "replace", "send_model_request"]) {
    assert.match(acceptanceTool, new RegExp(forbiddenWrite));
  }
});

test("Acceptance B funding approval ID is passed through a job environment boundary", async () => {
  const workflow = await readWorkflow(".github/workflows/production-basic-customer-operation.yml");
  const job = workflowJob(workflow, "recovery-acceptance-funding-prepare");
  const runs = serializedRuns(job);
  assert.equal(job.env.OPL_RECOVERY_ACCEPTANCE_FUNDING_APPROVAL_ID, "${{ inputs.approval_id }}");
  assert.match(runs, /--approval-id "\$OPL_RECOVERY_ACCEPTANCE_FUNDING_APPROVAL_ID"/);
  assert.doesNotMatch(runs, /--approval-id "\$\{\{ inputs\.approval_id \}\}"/);
});

test("Acceptance B account preparation is a production-only prepare gate with isolated secrets and no launch capability", async () => {
  const workflow = await readWorkflow(".github/workflows/production-basic-customer-operation.yml");
  const inputs = workflow.on.workflow_dispatch.inputs;
  assert.ok(inputs.operation_mode.options.includes("acceptance_b_account_prepare"));
  const job = workflowJob(workflow, "acceptance-b-account-prepare");
  const runs = serializedRuns(job);
  assert.deepEqual(job["runs-on"], ["self-hosted", "tencent-cloud", "opl-cloud", "tke-vpc"]);
  assert.equal(job.environment, "production");
  assert.match(String(job.if), /github\.ref == 'refs\/heads\/main'/);
  assert.match(String(job.if), /github\.sha == inputs\.merged_sha/);
  assert.match(String(job.if), /inputs\.operation_mode == 'acceptance_b_account_prepare'/);
  assert.match(String(job.if), /inputs\.confirm_account_provision/);
  assert.match(String(job.if), /inputs\.confirm_wallet_recharge/);
  for (const confirmation of ["workspace_purchase", "single_model_request", "recovery_plan_execute"]) {
    assert.match(String(job.if), new RegExp(`!inputs\\.confirm_${confirmation}`));
  }
  assert.match(String(job.if), /inputs\.resume_run_id == ''/);
  for (const secret of [
    "OPL_SUB2API_ADMIN_EMAIL",
    "OPL_SUB2API_ADMIN_PASSWORD",
    "OPL_PRODUCTION_BASIC_ACCEPTANCE_B_CUSTOMER_EMAIL",
    "OPL_PRODUCTION_BASIC_ACCEPTANCE_B_CUSTOMER_PASSWORD"
  ]) assert.equal(job.env[secret], undefined, `${secret} must not be job-scoped`);
  const prepareStep = stepsByName(job).get("Prepare account and wallet with authoritative readback");
  assert.equal(prepareStep?.env?.OPL_SUB2API_ADMIN_EMAIL, "${{ secrets.OPL_SUB2API_ADMIN_EMAIL }}");
  assert.equal(prepareStep?.env?.OPL_SUB2API_ADMIN_PASSWORD, "${{ secrets.OPL_SUB2API_ADMIN_PASSWORD }}");
  assert.equal(prepareStep?.env?.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_CUSTOMER_EMAIL, "${{ secrets.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_CUSTOMER_EMAIL }}");
  assert.equal(prepareStep?.env?.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_CUSTOMER_PASSWORD, "${{ secrets.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_CUSTOMER_PASSWORD }}");
  assert.equal(job.env.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_APPROVAL_JSON, undefined);
  assert.equal(job.env.OPL_BASIC_CANARY_CUSTOMER_PASSWORD, undefined);
  assert.equal(job.env.TENCENT_DEPLOY_KUBECONFIG, undefined);
  assert.equal(job.env.OPL_INTERNAL_SERVICE_TOKEN, undefined);
  assert.match(runs, /git ls-remote --heads origin/);
  assert.match(runs, /node tools\/production-basic-acceptance-b\.ts --prepare-account/);
  assert.match(runs, /validateProductionBasicAcceptanceBPrepareReadback/);
  assert.match(JSON.stringify(job.steps), /actions\/upload-artifact@v4/);
  assert.doesNotMatch(runs, /workspace-launches|model_request|send_model_request|kubectl|port-forward|debit|cvm|cbs|runtime|receipt/i);
  assert.doesNotMatch(JSON.stringify(job), /OPL_PRODUCTION_BASIC_ACCEPTANCE_B_APPROVAL_JSON|OPL_BASIC_CANARY_CUSTOMER_PASSWORD|TENCENT_DEPLOY_KUBECONFIG|OPL_INTERNAL_SERVICE_TOKEN/);
});

test("server-owned Recovery Plan validation is a zero-mutation production mode", async () => {
  const workflow = await readWorkflow(".github/workflows/production-basic-customer-operation.yml");
  const validatorSource = await readFile(repoFile("tools/production-live-qa.ts"), "utf8");
  const inputs = workflow.on.workflow_dispatch.inputs;
  const validate = workflowJob(workflow, "recovery-plan-operation");
  const runs = serializedRuns(validate);

  assert.ok(inputs.operation_mode.options.includes("compute_claim_validate"));
  assert.match(String(validate.if), /inputs\.operation_mode == 'compute_claim_validate'/);
  assert.match(String(validate.if), /inputs\.recovery_plan_launch_operation_id != ''/);
  assert.match(String(validate.if), /inputs\.recovery_plan_id != ''/);
  assert.match(String(validate.if), /inputs\.recovery_plan_digest != ''/);
  assert.deepEqual(validate["runs-on"], ["self-hosted", "tencent-cloud", "opl-cloud", "tke-vpc"]);
  assert.equal(validate.environment, "production");
  assert.equal(validate.env.OPL_RECOVERY_PLAN_LAUNCH_OPERATION_ID, "${{ inputs.recovery_plan_launch_operation_id }}");
  assert.equal(validate.env.OPL_RECOVERY_PLAN_ID, "${{ inputs.recovery_plan_id }}");
  assert.equal(validate.env.OPL_RECOVERY_PLAN_DIGEST, "${{ inputs.recovery_plan_digest }}");
  assert.equal(validate.env.OPL_COMPUTE_CLAIM_RECOVERY_APPROVAL_JSON, undefined);
  assert.equal(validate.env.KUBECONFIG, undefined);
  assert.match(runs, /production-live-qa\.ts --recovery-plan-validate/);
  assert.match(runs, /--plan-id/);
  assert.match(runs, /--plan-digest/);
  assert.match(runs, /grep -Fq -- '--recovery-plan-validate' tools\/production-live-qa\.ts/);
  assert.match(runs, /validateProductionWorkspaceRecoveryPlanArtifact/);
  assert.match(validatorSource, /runnerDirectMutationCounts/);
  assert.match(validatorSource, /mismatches/);
  assert.doesNotMatch(runs, /get secret opl-cloud-internal-service|KUBECONFIG|target_json|approval_json|compute-claim-recovery\/claim|--compute-claim-recover|ClaimComputeRecovery|create_original_cbs|reuse_original_cbs/i);
  assert.doesNotMatch(JSON.stringify(validate), /TENCENTCLOUD|OPL_INTERNAL_SERVICE_TOKEN|OPL_BASIC_CANARY_CUSTOMER_PASSWORD/);
});

test("server-owned Recovery Plan diagnosis and execution are exact original-order production modes", async () => {
  const workflow = await readWorkflow(".github/workflows/production-basic-customer-operation.yml");
  const deployment = await readJson(deploymentContractPath);
  const validatorSource = await readFile(repoFile("tools/production-live-qa.ts"), "utf8");
  const inputs = workflow.on.workflow_dispatch.inputs;
  const operation = workflowJob(workflow, "recovery-plan-operation");
  const runs = serializedRuns(operation);

  assert.ok(inputs.operation_mode.options.includes("recovery_plan_diagnose"));
  assert.ok(inputs.operation_mode.options.includes("recovery_plan_execute"));
  assert.equal(inputs.confirm_recovery_plan_execute.type, "boolean");
  assert.match(String(operation.if), /inputs\.operation_mode == 'recovery_plan_diagnose'/);
  assert.match(String(operation.if), /inputs\.operation_mode == 'compute_claim_validate'/);
  assert.match(String(operation.if), /inputs\.operation_mode == 'recovery_plan_execute'/);
  assert.deepEqual(operation["runs-on"], ["self-hosted", "tencent-cloud", "opl-cloud", "tke-vpc"]);
  assert.equal(operation["timeout-minutes"], 40);
  assert.equal(operation.environment, "production");
  assert.equal(operation.env.OPL_SUB2API_ADMIN_EMAIL, "${{ secrets.OPL_SUB2API_ADMIN_EMAIL }}");
  assert.equal(operation.env.OPL_SUB2API_ADMIN_PASSWORD, "${{ secrets.OPL_SUB2API_ADMIN_PASSWORD }}");
  assert.doesNotMatch(JSON.stringify(operation), /KUBECONFIG|TENCENTCLOUD|OPL_INTERNAL_SERVICE_TOKEN|OPL_BASIC_CANARY_CUSTOMER_PASSWORD/);
  assert.match(runs, /production-live-qa\.ts --recovery-plan-diagnose/);
  assert.match(runs, /production-live-qa\.ts --recovery-plan-validate/);
  assert.match(runs, /production-live-qa\.ts --recovery-plan-execute/);
  assert.match(runs, /grep -Fq -- '--recovery-plan-diagnose' tools\/production-live-qa\.ts/);
  assert.match(runs, /grep -Fq -- '--recovery-plan-validate' tools\/production-live-qa\.ts/);
  assert.match(runs, /grep -Fq -- '--recovery-plan-execute' tools\/production-live-qa\.ts/);
  assert.match(runs, /CONTINUE_RECOVERY_PLAN/);
  assert.equal(stepsByName(operation).get("Require exact redacted Recovery Plan evidence")?.if, "always()");
  assert.match(runs, /validBlockedDiagnose/);
  assert.match(runs, /validateProductionWorkspaceRecoveryPlanArtifact/);
  assert.match(runs, /mode === "compute_claim_validate" \? "recovery_plan_validate" : mode/);
  assert.match(runs, /production-live-qa\.ts/);
  assert.doesNotMatch(runs, /const common = result\?\.schemaVersion/);
  assert.match(validatorSource, /runnerDirectMutationCounts/);
  assert.match(validatorSource, /controlPlaneExecutionMutationCounts/);
  assert.match(validatorSource, /successorGate/);
  assert.match(validatorSource, /persistedMutationState/);
  assert.match(validatorSource, /fabricLedgerState/);
  assert.match(validatorSource, /RECOVERY_PLAN_PROVIDER_IDENTITY_PREDICATES/);
  assert.match(validatorSource, /workspaceRecoveryProviderIdentityFailure/);
  assert.doesNotMatch(runs, /--basic-customer-canary|allow-workspace-purchase|allow-wallet-recharge|allow-account-provision|\/api\/workspace-launches/);
  assert.deepEqual(deployment.productionWorkspaceRecoveryPlan.workflowModes, [
    "recovery_plan_diagnose",
    "compute_claim_validate",
    "recovery_plan_execute"
  ]);
  assert.equal(deployment.productionWorkspaceRecoveryPlan.executeRouteInWorkflow, true);
  assert.equal(deployment.productionWorkspaceRecoveryPlan.consoleExecution.githubRunRequired, true);
  assert.deepEqual(deployment.productionWorkspaceRecoveryPlan.artifact.executeFields, [
    "executionId", "runId", "url", "receiptId", "controlPlaneExecutionMutationCounts", "computeClaimEvidence"
  ]);
  assert.deepEqual(deployment.productionWorkspaceRecoveryPlan.artifact.computeClaimEvidenceFields, [
    "bindingClassification", "mismatchField", "expectedDigest", "actualDigest", "mutationLedger", "mutationLedgerOutcome",
    "cvm", "node", "ledgerFailureStage", "ledgerProviderErrorClass", "failureStage", "providerErrorClass", "reconciliation"
  ]);
  assert.equal(deployment.productionWorkspaceRecoveryPlan.artifact.computeClaimEvidenceAuthority,
    "Fabric_ProveComputeClaimRecovery_plus_persisted_identity_evidence_digest_only_projection");
  assert.equal(deployment.productionWorkspaceRecoveryPlan.artifact.computeClaimEvidenceConsumer,
    "original_Launch_Recovery_Claim_readback_and_production_live_qa_artifact_validator");
  assert.deepEqual(deployment.productionWorkspaceRecoveryPlan.artifact.successorGateFields, [
    "applicable", "allowed", "planState", "executionState", "completionState", "leaseState",
    "identityState", "persistedMutationState", "fabricLedgerState"
  ]);
  assert.deepEqual(deployment.productionWorkspaceRecoveryPlan.artifact.failureFields, [
    "failureStage", "readbackError", "errorCode", "providerIdentityFailure", "computeClaimEvidence"
  ]);
  assert.deepEqual(deployment.productionWorkspaceRecoveryPlan.artifact.providerIdentityFailureFields, [
    "predicate", "expectedDigest", "actualDigest"
  ]);
  assert.equal(deployment.productionWorkspaceRecoveryPlan.artifact.providerIdentityFailureAuthority,
    "Fabric_ProveComputeClaimRecovery_digest_only_optional_on_deterministic_identity_mismatch");
  assert.equal(deployment.productionWorkspaceRecoveryPlan.artifact.validator, "tools/production-live-qa.ts#validateProductionWorkspaceRecoveryPlanArtifact");
});

test("original Launch compute claim readback is a single GET-only production mode", async () => {
  const workflow = await readWorkflow(".github/workflows/production-basic-customer-operation.yml");
  const inputs = workflow.on.workflow_dispatch.inputs;
  const job = workflowJob(workflow, "compute-claim-readback");
  const runs = serializedRuns(job);
  assert.ok(inputs.operation_mode.options.includes("compute_claim_readback"));
  assert.match(String(job.if), /inputs\.operation_mode == 'compute_claim_readback'/);
  assert.match(String(job.if), /inputs\.workspace_identity_account_id != ''/);
  assert.match(String(job.if), /inputs\.recovery_plan_launch_operation_id != ''/);
  assert.deepEqual(job["runs-on"], ["self-hosted", "tencent-cloud", "opl-cloud", "tke-vpc"]);
  assert.equal(job.environment, "production");
  assert.equal(job.env.OPL_RECOVERY_PLAN_ACCOUNT_ID, "${{ inputs.workspace_identity_account_id }}");
  assert.equal(job.env.OPL_RECOVERY_PLAN_LAUNCH_OPERATION_ID, "${{ inputs.recovery_plan_launch_operation_id }}");
  const producer = job.steps.find((step) => step.id === "readback");
  assert.ok(producer);
  assert.equal(producer["continue-on-error"], undefined);
  const fabricLedger = stepsByName(job).get("Log current redacted Fabric ledger authority");
  assert.ok(fabricLedger);
  const fabricLedgerRun = serializedStep(fabricLedger);
  assert.match(runs, /--workspace-compute-claim-readback/);
  assert.match(runs, /kubectl .* get pods/);
  assert.match(runs, /kubectl .* get secret opl-cloud-internal-service/);
  assert.match(runs, /--fabric-pod/);
  assert.match(runs, /compute-claim-readback\.json/);
  assert.match(runs, /mutationCounts/);
  for (const field of ["producerExitCode", "status", "failureStage", "errorCode", "firstFalsePredicate", "mutationOutcome"]) {
    assert.match(runs, new RegExp(field));
  }
  assert.match(runs, /status === "evidence_only"/);
  assert.match(runs, /status === "blocked"/);
  assert.match(runs, /Compute claim readback producer blocked/);
  assert.match(fabricLedgerRun, /\/fabric\/operations/);
  assert.match(fabricLedgerRun, /\/fabric\/compute-allocations\//);
  assert.match(fabricLedgerRun, /\/fabric\/machine-ownerships\//);
  assert.match(fabricLedgerRun, /\/fabric\/compute-claim-recovery\/identity-evidence/);
  assert.match(fabricLedgerRun, /fabric\.operationId/);
  assert.match(fabricLedgerRun, /binding\.requestHash/);
  assert.match(fabricLedgerRun, /expectedDigest/);
  assert.match(fabricLedgerRun, /actualDigest/);
  assert.match(fabricLedgerRun, /firstFalseIdentityCheck/);
  assert.match(fabricLedgerRun, /mutationLedgerDigest/);
  assert.match(fabricLedgerRun, /reconciliation/);
  assert.match(fabricLedgerRun, /FABRIC_LEDGER_AUTHORITY=/);
  assert.match(fabricLedgerRun, /FABRIC_LEDGER_AUTHORITY_ERROR=/);
  for (const field of ["failureBoundary", "exactFunction", "reasonCode", "firstFalsePredicate", "expected", "actual", "authority", "mutationOutcome"]) {
    assert.match(fabricLedgerRun, new RegExp(field));
  }
  assert.match(fabricLedgerRun, /function requireCurrentFabricAuthority/);
  assert.match(fabricLedgerRun, /function validateIdentityChecks/);
  assert.match(fabricLedgerRun, /fabric\.identityEvidence\.checks\.atIndex/);
  assert.match(fabricLedgerRun, /function currentFabricAuthoritySnapshot/);
  assert.match(fabricLedgerRun, /currentAuthority: currentFabricAuthoritySnapshot\(\)/);
  assert.match(fabricLedgerRun, /mutationEvidenceProjection/);
  assert.match(fabricLedgerRun, /reconciliationProjection/);
  assert.match(fabricLedgerRun, /actual: safeActual \? String\(actual\) : sha256\(actual\)/);
  assert.match(fabricLedgerRun, /check\.actual !== undefined && typeof check\.actual !== "string"/);
  assert.match(fabricLedgerRun, /actualDigest: sha256\(check\.actual \?\? ""\)/);
  assert.match(fabricLedgerRun, /status: "confirmed_zero", attempted: 0, accepted: 0, confirmed: 0, unknown: 0/);
  assert.match(fabricLedgerRun, /fabric\.identityEvidence\.checks/);
  assert.match(fabricLedgerRun, /fabric\.identityEvidence\.providerErrorClass/);
  assert.doesNotMatch(fabricLedgerRun, /recovery-plan\/(?:diagnose|validate|execute)|compute-claim-recovery\/(?:proof|claim)|CreateComputeAllocation|CreateDisks|create_storage_volume|patch node|scale|debit|refund|delete|replace/i);
  assert.doesNotMatch(runs, /recovery-plan\/(?:diagnose|validate|execute)|--recovery-plan-(?:diagnose|validate|execute)|CreateDisks|create_storage_volume|patch node|debit|refund|create_cvm/i);
  assert.match(JSON.stringify(job), /OPL_SUB2API_ADMIN_PASSWORD/);
  assert.doesNotMatch(JSON.stringify(job), /OPL_BASIC_CANARY_CUSTOMER_PASSWORD|approval_json/);
});

test("Fabric recovery ledger readback is artifact-bound, read-only, and cannot replay Diagnose or claim", async () => {
  const workflow = await readWorkflow(".github/workflows/production-basic-customer-operation.yml");
  const deployment = await readJson(deploymentContractPath);
  const inputs = workflow.on.workflow_dispatch.inputs;
  const job = workflowJob(workflow, "fabric-ledger-readback");
  const runs = serializedRuns(job);
  const downloads = (job.steps || []).filter((step) => step.uses === "actions/download-artifact@v4");

  assert.ok(inputs.operation_mode.options.includes("fabric_ledger_readback"));
  for (const name of ["recovery_evidence_run_id", "recovery_evidence_artifact_id", "recovery_evidence_artifact_digest"]) {
    assert.equal(inputs[name].required, false, name);
  }
  assert.match(String(job.if), /inputs\.operation_mode == 'fabric_ledger_readback'/);
  assert.match(String(job.if), /inputs\.workspace_identity_account_id != ''/);
  assert.match(String(job.if), /inputs\.recovery_plan_launch_operation_id != ''/);
  assert.match(String(job.if), /inputs\.recovery_evidence_run_id != ''/);
  assert.match(String(job.if), /inputs\.recovery_evidence_artifact_id != ''/);
  assert.match(String(job.if), /inputs\.recovery_evidence_artifact_digest != ''/);
  assert.deepEqual(job["runs-on"], ["self-hosted", "tencent-cloud", "opl-cloud", "tke-vpc"]);
  assert.equal(job.environment, "production");
  assert.equal(job.env.OPL_RECOVERY_EVIDENCE_RUN_ID, "${{ inputs.recovery_evidence_run_id }}");
  assert.equal(job.env.OPL_RECOVERY_EVIDENCE_ARTIFACT_ID, "${{ inputs.recovery_evidence_artifact_id }}");
  assert.equal(job.env.OPL_RECOVERY_EVIDENCE_ARTIFACT_DIGEST, "${{ inputs.recovery_evidence_artifact_digest }}");
  assert.deepEqual(downloads.map((step) => step.with), [{
    "artifact-ids": "${{ inputs.recovery_evidence_artifact_id }}",
    "run-id": "${{ inputs.recovery_evidence_run_id }}",
    path: "${{ runner.temp }}/production-fabric-ledger-readback/source",
    "github-token": "${{ github.token }}",
    repository: "${{ github.repository }}",
    "merge-multiple": true
  }]);
  assert.match(runs, /api\.github\.com\/repos\/\$GITHUB_REPOSITORY\/actions\/artifacts\/\$OPL_RECOVERY_EVIDENCE_ARTIFACT_ID/);
  assert.match(runs, /api\.github\.com\/repos\/\$GITHUB_REPOSITORY\/actions\/runs\/\$OPL_RECOVERY_EVIDENCE_RUN_ID/);
  assert.match(runs, /production-basic-compute-claim-evidence/);
  assert.match(runs, /Production Basic Customer Operation/);
  assert.match(runs, /workflow_dispatch/);
  assert.match(runs, /target\.deadline === proof\.deadline/);
  assert.match(runs, /compute-claim-recovery\.json/);
  assert.match(runs, /target\.launchOperationId/);
  assert.match(runs, /target\.accountId/);
  assert.match(runs, /\/fabric\/compute-claim-recovery\/identity-evidence/);
  for (const field of [
    "fabric.operationId", "fabric.operationIdempotencyKey", "fabric.operationRequestHash",
    "binding.present", "binding.valid", "binding.compatibility", "binding.launchOperationId",
    "binding.idempotencyKey", "binding.targetHash", "binding.requestHash"
  ]) assert.match(runs, new RegExp(field.replaceAll(".", "\\.")));
  assert.match(runs, /mutationLedgerOutcome/);
  assert.match(runs, /bindingClassification/);
  assert.match(runs, /bindingDigest/);
  assert.match(runs, /current.*compute-claim.*request-hash-reconciliation.*known-legacy.*other/s);
  for (const field of ["attempted", "confirmed", "unknown", "missing", "failureStage", "providerErrorClass"]) {
    assert.match(runs, new RegExp(field));
  }
  assert.match(runs, /recoverable_cvm_only/);
  assert.match(runs, /operator_compensation_required/);
  assert.match(runs, /cvm\.attempted > 0/);
  assert.match(runs, /cvm\.confirmed === cvm\.attempted/);
  assert.match(runs, /node\.attempted === 0/);
  assert.match(runs, /check\.matches === \(check\.expectedDigest === check\.actualDigest\)/);
  assert.match(runs, /check\.matches === \(check\.expected === check\.actual\)/);
  assert.match(runs, /Object\.keys\(value\)\.sort\(\)\.join\(","\) !== "attempted,confirmed,missing,unknown"/);
  assert.match(runs, /value\.attempted > maximum/);
  assert.match(runs, /value\.confirmed \+ value\.unknown > value\.attempted/);
  assert.match(runs, /new Set\(value\.missing\)\.size !== value\.missing\.length/);
  assert.match(runs, /failureStages\.has\(artifact\?\.failureStage\)/);
  assert.match(runs, /providerErrorClasses\.has\(artifact\?\.providerErrorClass\)/);
  assert.match(runs, /artifact\.mutationLedgerOutcome === "nonzero"/);
  assert.match(runs, /artifact\?\.compensationReason === "fabric_ledger_readback_not_completed"/);
  assert.match(runs, /runnerDirectMutationCounts/);
  assert.doesNotMatch(runs, /mutationLedgerDigest|recovery-plan\/(?:diagnose|validate|execute)|--recovery-plan-(?:diagnose|validate|execute)|compute-claim-recovery\/(?:proof|claim)|ClaimComputeRecovery|CreateComputeAllocation|CreateDisks|create_storage_volume|scale|debit|refund|delete|replace/i);
  assert.doesNotMatch(JSON.stringify(job), /TENCENTCLOUD_SECRET|OPL_[A-Z_]*APPROVAL_JSON|target_json|cloud_digest|workspace_digest|recovery_confirmation/i);
  assert.deepEqual(deployment.productionFabricLedgerReadback, {
    workflow: ".github/workflows/production-basic-customer-operation.yml",
    workflowMode: "fabric_ledger_readback",
    job: "fabric-ledger-readback",
    runner: ["self-hosted", "tencent-cloud", "opl-cloud", "tke-vpc"],
    sourceAuthority: "github_actions_artifact_metadata_run_head_sha_digest_and_exact_redacted_payload",
    targetAuthority: "downloaded_artifact_target_only_no_caller_supplied_resource_identity",
    endpoint: "POST /fabric/compute-claim-recovery/identity-evidence",
    readOnly: true,
    diagnoseCalls: 0,
    providerCalls: 0,
    requiredMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 },
    artifactSchemaVersion: 2,
    bindingClassifications: ["current", "compute-claim", "request-hash-reconciliation", "known-legacy", "other"],
    requestHashReconciliation: "readback_schema_only_never_recoverable_cvm_only_or_workflow_mutation_authority",
    providerMutationFields: ["attempted", "confirmed", "unknown", "missing", "failureStage", "providerErrorClass"],
    recoverableCVMOnly: "recognized_current_or_compute_claim_binding_and_cvm_attempted_positive_fully_confirmed_unknown_zero_missing_empty_and_node_attempted_zero",
    knownLegacy: "recovery_exec_lowerhex20_old_request_hash_exact_match_classification_only_operator_compensation_required_never_binding_takeover",
    artifact: "redacted_binding_class_digest_and_exact_persisted_cvm_node_mutation_evidence"
  });
});

test("Recovery Acceptance canary is default-off, allowlisted, original-launch-only, and redacted", async () => {
  const workflow = await readWorkflow(".github/workflows/production-basic-customer-operation.yml");
  const deployment = await readJson(deploymentContractPath);
  const inputs = workflow.on.workflow_dispatch.inputs;
  const job = workflowJob(workflow, "recovery-acceptance-canary");
  const runs = serializedRuns(job);

  assert.ok(inputs.operation_mode.options.includes("recovery_acceptance_canary"));
  assert.equal(inputs.confirm_recovery_acceptance_canary.type, "boolean");
  assert.equal(inputs.recovery_acceptance_account_id.required, false);
  assert.equal(inputs.recovery_acceptance_launch_operation_id.required, false);
  assert.match(String(job.if), /inputs\.operation_mode == 'recovery_acceptance_canary'/);
  assert.match(String(job.if), /inputs\.confirm_recovery_acceptance_canary/);
  assert.match(String(job.if), /inputs\.recovery_acceptance_account_id != ''/);
  assert.match(String(job.if), /inputs\.recovery_acceptance_launch_operation_id != ''/);
  assert.match(String(job.if), /!inputs\.confirm_workspace_purchase/);
  assert.match(String(job.if), /!inputs\.confirm_recovery_plan_execute/);
  assert.deepEqual(job["runs-on"], ["self-hosted", "tencent-cloud", "opl-cloud", "tke-vpc"]);
  assert.equal(job.environment, "production");
  assert.equal(job.env.OPL_RECOVERY_ACCEPTANCE_CANARY_APPROVAL_JSON, "${{ secrets.OPL_RECOVERY_ACCEPTANCE_CANARY_APPROVAL_JSON }}");
  assert.equal(job.env.OPL_RECOVERY_ACCEPTANCE_CANARY_ACCOUNT_ID, "${{ inputs.recovery_acceptance_account_id }}");
  assert.equal(job.env.OPL_RECOVERY_ACCEPTANCE_CANARY_LAUNCH_OPERATION_ID, "${{ inputs.recovery_acceptance_launch_operation_id }}");
  assert.equal(job.env.OPL_RECOVERY_ACCEPTANCE_CANARY_ENABLED, "${{ vars.OPL_RECOVERY_ACCEPTANCE_CANARY_ENABLED || '0' }}");
  assert.equal(job.env.OPL_RECOVERY_ACCEPTANCE_CANARY_ACCOUNT_IDS, "${{ vars.OPL_RECOVERY_ACCEPTANCE_CANARY_ACCOUNT_IDS || '' }}");
  assert.equal(job.env.OPL_SUB2API_ADMIN_EMAIL, "${{ secrets.OPL_SUB2API_ADMIN_EMAIL }}");
  assert.equal(job.env.OPL_SUB2API_ADMIN_PASSWORD, "${{ secrets.OPL_SUB2API_ADMIN_PASSWORD }}");
  assert.match(runs, /git ls-remote --heads origin/);
  assert.match(runs, /node tools\/recovery-acceptance-canary\.ts --recovery-acceptance-canary/);
  assert.match(runs, /recovery-acceptance-canary\.json/);
  assert.match(runs, /controlPlaneMutationCounts/);
  assert.match(runs, /runnerDirectMutationCounts/);
  assert.match(JSON.stringify(job.steps), /actions\/upload-artifact@v4/);
  assert.doesNotMatch(runs, /kubectl|port-forward|--basic-customer-canary|allow-workspace-purchase|allow-model-write|debit|create_cvm|create_cbs|send_model_request/i);
  assert.doesNotMatch(JSON.stringify(job), /OPL_BASIC_CANARY_CUSTOMER_PASSWORD|TENCENT_DEPLOY_KUBECONFIG|OPL_INTERNAL_SERVICE_TOKEN/);
  assert.deepEqual(deployment.productionRecoveryAcceptanceCanary, {
    workflow: ".github/workflows/production-basic-customer-operation.yml",
    workflowMode: "recovery_acceptance_canary",
    job: "recovery-acceptance-canary",
    runner: ["self-hosted", "tencent-cloud", "opl-cloud", "tke-vpc"],
    environment: "production",
    defaultEnabled: false,
    requiredConfirmation: "confirm_recovery_acceptance_canary",
    workflowInputs: ["merged_sha", "recovery_acceptance_account_id", "recovery_acceptance_launch_operation_id", "confirm_recovery_acceptance_canary"],
    approvalSecret: "OPL_RECOVERY_ACCEPTANCE_CANARY_APPROVAL_JSON",
    allowlistEnv: "OPL_RECOVERY_ACCEPTANCE_CANARY_ACCOUNT_IDS",
    releaseBindingEnv: ["OPL_RELEASE_SHA", "OPL_CLOUD_IMAGE"],
    route: "POST /api/operator/workspace-launches/{operationId}/recovery-acceptance/manual-review",
    precondition: "original_launch_preparing_storage_fulfilling_compute_and_node_confirmed_storage_not_started",
    allowedWrites: ["persist_original_launch_manual_review_recovery_acceptance"],
    forbiddenWrites: ["submit_second_workspace_launch", "debit", "create_cvm", "claim_second_node", "create_cbs", "provider_mutation", "fabric_mutation", "database_fabric_ledger_fabrication"],
    mutationCounts: { database: 1, sub2api: 0, tencent: 0, kubernetes: 0 },
    unknownPost: "GET_original_launch_by_same_operation_identity_without_retry",
    artifact: {
      schemaVersion: 1,
      fields: ["operationMode", "status", "recoveryEligible", "errorCode", "release", "target", "approval", "manualReview", "controlPlaneMutationCounts", "runnerDirectMutationCounts", "verifiedAt"],
      forbidden: ["password", "secret", "token", "cookie", "nonce", "csrf", "raw_launch_operation_id", "raw_workspace_id"]
    },
    currentState: "implemented_and_locally_fake_tested_not_executed"
  });
});

test("disabled controlled Basic Pilot has one production fail-closed proof with no provider capability", async () => {
  const workflow = await readWorkflow(".github/workflows/production-basic-customer-operation.yml");
  const deployment = await readJson(deploymentContractPath);
  const inputs = workflow.on.workflow_dispatch.inputs;
  const job = workflowJob(workflow, "controlled-pilot-closed-validate");
  const runs = serializedRuns(job);
  const boundary = deployment.productionControlledBasicPilotClosedValidation;

  assert.ok(inputs.operation_mode.options.includes("controlled_pilot_closed_validate"));
  assert.equal(boundary.workflowMode, "controlled_pilot_closed_validate");
  assert.equal(boundary.expectedRejection, "workspace_launch_admission_disabled");
  assert.deepEqual(boundary.requiredMutationCounts, { database: 0, sub2api: 0, fabric: 0, tencent: 0, kubernetes: 0 });
  assert.deepEqual(boundary.authorityReadbacks, ["control_plane_launches", "sub2api_wallet", "sub2api_keys", "control_plane_workspaces", "ledger_receipts"]);
  assert.match(String(job.if), /inputs\.operation_mode == 'controlled_pilot_closed_validate'/);
  for (const confirmation of ["account_provision", "wallet_recharge", "workspace_purchase", "single_model_request"]) {
    assert.match(String(job.if), new RegExp(`!inputs\\.confirm_${confirmation}`));
  }
  assert.deepEqual(job["runs-on"], ["self-hosted", "tencent-cloud", "opl-cloud", "tke-vpc"]);
  assert.equal(job.environment, "production");
  assert.match(runs, /production-live-qa\.ts --controlled-pilot-closed-validate/);
  assert.match(runs, /workspace_launch_admission_disabled/);
  assert.match(runs, /mutationCounts/);
  assert.match(runs, /authorityDigests/);
  assert.doesNotMatch(JSON.stringify(job), /KUBECONFIG|TENCENTCLOUD|OPL_INTERNAL_SERVICE_TOKEN|allow-workspace-purchase|basic-customer-canary/);
});

test("operator terminalizes only one exact blocked compute pool head with one CAS and zero provider mutation", async () => {
  const workflow = await readWorkflow(".github/workflows/production-basic-customer-operation.yml");
  const deployment = await readJson(deploymentContractPath);
  const inputs = workflow.on.workflow_dispatch.inputs;
  const job = workflowJob(workflow, "compute-pool-head-terminalization");
  const runs = serializedRuns(job);
  const boundary = deployment.productionComputePoolHeadTerminalization;

  assert.ok(inputs.operation_mode.options.includes("compute_pool_head_terminalize"));
  assert.equal(Object.keys(inputs).length, 25);
  assert.equal(inputs.confirm_compute_pool_head_terminalization, undefined);
  assert.match(String(job.if), /inputs\.operation_mode == 'compute_pool_head_terminalize'/);
  assert.match(String(job.if), /inputs\.approval_id != ''/);
  assert.doesNotMatch(String(job.if), /inputs\.confirm_compute_pool_head_terminalization/);
  assert.deepEqual(job["runs-on"], ["self-hosted", "tencent-cloud", "opl-cloud", "tke-vpc"]);
  assert.equal(job.environment, "production");
  assert.equal(job.env.OPL_POOL_HEAD_TERMINALIZATION_APPROVAL_ID, "${{ inputs.approval_id }}");
  assert.equal(job.env.OPL_BASIC_COMPUTE_NODE_POOL_ID, "${{ vars.OPL_BASIC_COMPUTE_NODE_POOL_ID }}");
  assert.match(runs, /GET exact candidate|candidate_status/);
  assert.equal((runs.match(/--data-binary "@\$raw_root\/request\.json"/g) || []).length, 1);
  assert.match(runs, /result_status/);
  assert.match(runs, /final-pool-head\.json/);
  assert.match(runs, /response_unknown_reconciled/);
  assert.match(runs, /terminal_unprovable/);
  assert.match(runs, /runnerDirectMutationCounts: \{ sub2api: 0, tencent: 0, kubernetes: 0 \}/);
  assert.match(runs, /production-compute-pool-head-terminalization-raw/);
  assert.match(runs, /OPL_POOL_HEAD_TERMINALIZATION_RAW_ROOT/);
  assert.doesNotMatch(JSON.stringify(job), /OPL_POOL_HEAD_TERMINIZATION_/);
  assert.match(runs, /find "\$raw_root" -mindepth 1 -delete/);
  assert.doesNotMatch(runs, /CreateComputeAllocation|CreateDisks|TagResources|ModifyResourcesTagValue|kubectl[^\n]*(?:apply|patch|delete)|wallet-adjustments|workspace-launches/);
  assert.deepEqual(boundary, {
    workflow: ".github/workflows/production-basic-customer-operation.yml",
    workflowMode: "compute_pool_head_terminalize",
    job: "compute-pool-head-terminalization",
    runner: ["self-hosted", "tencent-cloud", "opl-cloud", "tke-vpc"],
    environment: "production",
    requiredConfirmation: "operation_mode_compute_pool_head_terminalize_and_nonempty_approval_id",
    inputs: ["merged_sha", "operation_mode", "approval_id"],
    nodePoolSource: "production_OPL_BASIC_COMPUTE_NODE_POOL_ID_variable",
    sourceGate: "github_sha_checkout_head_and_only_remote_main_equal_exact_merged_sha",
    deploymentGate: "production_configmap_release_sha_and_current_ready_fabric_revision_image_digest",
    sequence: ["GET_exact_candidate", "POST_single_cas", "GET_exact_result", "GET_final_pool_head"],
    postMaximum: 1,
    unknownPost: "GET_exact_result_only_never_second_POST",
    terminalResult: "fabric_operation_failed_terminal_unprovable_and_original_control_plane_launch_unchanged",
    mutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 },
    artifact: {
      fields: ["operationMode", "status", "errorCode", "release", "nodePoolIdDigest", "approvalIdDigest", "candidate", "terminalization", "finalPoolHead", "casPostCount", "responseDisposition", "runnerDirectMutationCounts"],
      forbidden: ["accountId", "workspaceId", "launchOperationId", "computeAllocationId", "machineName", "nodeName", "cvmInstanceId", "privateIp", "password", "token", "credential"]
    },
    rawEvidenceCleanup: "always_remove_exact_runner_temp_directory_after_artifact_projection",
    forbidden: ["second_post", "sub2api_write", "tencent_write", "kubernetes_write", "refund", "resource_reuse", "direct_sql"]
  });
});

test("legacy target-based Recovery workflow surfaces are removed", async () => {
  const workflow = await readWorkflow(".github/workflows/production-basic-customer-operation.yml");
  const source = await readFile(repoFile(".github/workflows/production-basic-customer-operation.yml"), "utf8");
  for (const input of [
    "diagnose_target_json", "compute_claim_target_json", "compute_claim_cloud_digest",
    "workspace_launch_target_json", "workspace_launch_cloud_digest",
    "confirm_compute_claim_recovery", "confirm_workspace_launch_readback_recovery", "recovery_plan_account_id"
  ]) assert.equal(workflow.on.workflow_dispatch.inputs[input], undefined, input);
  for (const job of [
    "manual-review-diagnose", "workspace-launch-readback-diagnose", "workspace-launch-readback-recover",
    "compute-claim-diagnose", "compute-claim-recover"
  ]) assert.equal(workflow.jobs[job], undefined, job);
  assert.doesNotMatch(source, /OPL_COMPUTE_CLAIM_RECOVERY_APPROVAL_JSON|compute-claim-recovery\/(?:validate|claim)|workspace-launch-readback-(?:diagnose|recover)/);
});

test("recovered Workspace E2E is a separate hosted mode with no resource mutation capability or hard-coded customer", async () => {
  const workflow = await readWorkflow(".github/workflows/production-basic-customer-operation.yml");
  const deployment = await readJson(deploymentContractPath);
  const boundary = deployment.productionRecoveredWorkspaceE2E;
  const inputs = workflow.on.workflow_dispatch.inputs;
  assert.deepEqual(boundary.workflowInputs, [
    "merged_sha", "approval_id", "customer_email", "recovery_plan_launch_operation_id",
    "recovery_plan_id", "recovery_plan_digest", "confirm_single_model_request"
  ]);
  assert.equal(boundary.planReferenceAuthority, "persisted_control_plane_recovery_plan_projection_on_original_launch");
  assert.equal(boundary.resourceClosureArtifactDependency, false);
  assert.deepEqual(boundary.forbiddenInputs, ["resource_closure_run_id", "continuation_evidence", "recovery_approval_json", "resource_identity"]);
  assert.deepEqual(inputs.operation_mode.options, [
    "customer_operation",
    "workspace_identity_diagnose",
    "acceptance_b_account_reconcile",
    "acceptance_b_account_prepare",
    "recovery_plan_diagnose",
    "acceptance_b_fresh_order",
    "acceptance_b_fresh_readback",
    "compute_claim_validate",
    "fabric_ledger_readback",
    "compute_pool_head_terminalize",
    "recovery_plan_execute",
    "recovery_acceptance_canary",
    "recovery_acceptance_funding_prepare",
    "recovery_acceptance_extra_funding_prepare",
    "recovery_acceptance_original_launch",
    "controlled_pilot_closed_validate",
    "compute_claim_readback",
    "recovered_workspace_e2e"
  ]);
  assert.equal(inputs.customer_email.required, false);
  assert.equal(inputs.resource_closure_run_id, undefined);
  assert.equal(inputs.recovery_plan_launch_operation_id.required, false);
  assert.equal(inputs.recovery_plan_id.required, false);
  assert.equal(inputs.recovery_plan_digest.required, false);

  const job = workflowJob(workflow, "recovered-workspace-e2e");
  const runs = serializedRuns(job);
  assert.equal(job["runs-on"], "ubuntu-latest");
  assert.equal(job.environment, "production");
  assert.match(String(job.if), /inputs\.operation_mode == 'recovered_workspace_e2e'/);
  assert.match(String(job.if), /inputs\.confirm_single_model_request/);
  for (const confirmation of ["account_provision", "wallet_recharge", "workspace_purchase"]) {
    assert.match(String(job.if), new RegExp(`!inputs\\.confirm_${confirmation}`));
  }
  assert.equal(job.env.OPL_RECOVERED_WORKSPACE_CUSTOMER_EMAIL, "${{ inputs.customer_email }}");
  assert.equal(job.env.OPL_RECOVERED_WORKSPACE_E2E_APPROVAL_JSON, undefined);
  assert.equal(job.env.OPL_RECOVERED_WORKSPACE_EXPECTED_MODEL, "${{ vars.OPL_CODEX_MODEL || 'gpt-5.5' }}");
  assert.doesNotMatch(JSON.stringify(job.steps), /actions\/download-artifact@v4/);
  assert.match(runs, /production-live-qa\.ts --recovered-workspace-e2e/);
  assert.match(runs, /--approval-id "\$OPL_RECOVERED_WORKSPACE_E2E_APPROVAL_ID"/);
  assert.match(runs, /--launch-operation-id "\$OPL_RECOVERY_PLAN_LAUNCH_OPERATION_ID"/);
  assert.match(runs, /--recovery-plan-id "\$OPL_RECOVERY_PLAN_ID"/);
  assert.match(runs, /--recovery-plan-digest "\$OPL_RECOVERY_PLAN_DIGEST"/);
  assert.doesNotMatch(runs, /continuation-evidence|workspace-launch-continuation\.json/);
  assert.doesNotMatch(JSON.stringify(job), /KUBECONFIG|TENCENT_|OPL_INTERNAL_SERVICE_TOKEN|kubectl|port-forward/);
  assert.doesNotMatch(runs, /--basic-customer-canary|--compute-claim-recover|allow-workspace-purchase|allow-wallet-recharge|allow-account-provision|create_storage_volume|CreateComputeAllocation|scale|debit|refund/i);

  const source = await readFile(repoFile(".github/workflows/production-basic-customer-operation.yml"), "utf8");
  assert.doesNotMatch(source, /huangrende@fenggaolab\.org/i);
});

test("manual cleanup workflows invoke the shared four-identity protected-resource guard", async () => {
  for (const [path, mutationBoundary] of [
    [".github/workflows/cleanup-tke-nodepool-machines.yml", 'action: "destroy_compute_allocation"'],
    [".github/workflows/cleanup-tke-compute-residual.yml", 'kubectl --kubeconfig "$KUBECONFIG" -n "$OPL_K8S_NAMESPACE" delete']
  ]) {
    const source = await readFile(repoFile(path), "utf8");
    for (const token of [
      "protected_resource_check",
      "OPL_SYSTEM_COMPUTE_NODE_POOL_ID",
      "OPL_SYSTEM_COMPUTE_MACHINE_ID",
      "OPL_SYSTEM_COMPUTE_NODE_NAME",
      "OPL_SYSTEM_COMPUTE_MACHINE_TYPE",
      "OPL_SYSTEM_COMPUTE_CVM_ID",
      "OPL_BASIC_COMPUTE_NODE_POOL_ID",
      "OPL_PRO_COMPUTE_NODE_POOL_ID"
    ]) assert.match(source, new RegExp(token), `${path}:${token}`);
    assert.ok(source.indexOf("protected_resource_check") < source.indexOf(mutationBoundary), path);
  }
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
  const source = JSON.stringify(deploy);
  assert.equal(deployWorkflow.jobs["live-qa"], undefined);
  assert.equal(deployWorkflow.on.workflow_dispatch.inputs.live_qa_approval_id, undefined);
  assert.doesNotMatch(source, /OPL_VERIFY_|OPL_PROVIDER_ACCEPTANCE_TOKEN|production-provider-acceptance/);
  assert.doesNotMatch(source, /OPL_OPERATOR_CIDRS|OPL_TRUSTED_PROXY_CIDRS/);
  assert.match(source, /OPL_BASIC_COMPUTE_NODE_POOL_ID/);
  assert.match(source, /OPL_PRO_COMPUTE_NODE_POOL_ID/);
  assert.doesNotMatch(source, /OPL_CODEX_BASE_URL|OPL_GATEWAY_PUBLIC_BASE_URL/);
  assert.match(inputGate.run, /OPL_BASIC_COMPUTE_NODE_POOL_ID/);
  assert.match(inputGate.run, /OPL_PRO_COMPUTE_NODE_POOL_ID/);
  assert.doesNotMatch(inputGate.run, /GATEWAY_PUBLIC_BASE_URL|PROVIDER_ACCEPTANCE|OPL_VERIFY_/);
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
  assert.equal(deploy.if, "${{ !inputs.diagnostics_only && !inputs.promote_workspace_image }}");
  assert.equal(diagnose.if, "${{ inputs.diagnostics_only }}");
  assert.match(String(releaseGate.if), /!inputs\.diagnostics_only/);
  assert.match(String(releaseGate.if), /!inputs\.promote_workspace_image/);
  assert.deepEqual(diagnose["runs-on"], ["self-hosted", "tencent-cloud", "opl-cloud", "tke-vpc"]);
  assert.equal(diagnose.environment, "production");
  assert.match(runs, /get nodes -o wide/);
  assert.match(runs, /get deploy,rs,pod -o wide/);
  assert.match(runs, /get events/);
  assert.match(runs, /describe "\$pod"/);
  assert.match(runs, /logs/);
  assert.doesNotMatch(runs, /\b(?:apply|delete|patch|scale)\b|set image|rollout restart/);
});

test("targeted Fresh compute diagnostics prove the exact allocation and MachineOwnership binding without writes", async () => {
  const [workflow, contract] = await Promise.all([
    readWorkflow(".github/workflows/deploy-tke-production.yml"),
    readJson(deploymentContractPath)
  ]);
  const diagnose = workflowJob(workflow, "diagnose");
  const steps = stepsByName(diagnose);
  const readback = serializedStep(steps.get("Read exact Fresh compute operation"));

  assert.equal(workflow.on.workflow_dispatch.inputs.fresh_compute_readback_operation_id.type, "string");
  assert.equal(workflow.on.workflow_dispatch.inputs.fresh_compute_readback_operation_id.default, "");
  assert.match(readback, /GET \/fabric\/operations/);
  assert.match(readback, /GET \/fabric\/compute-allocations\/\{id\}/);
  assert.match(readback, /\/fabric\/machine-ownerships\//);
  assert.match(readback, /\/fabric\/compute-pool-head\?/);
  for (const field of [
    "allocationMatchesOperation",
    "ownershipMatchesAllocation",
    "productionReleaseIdentitySha256",
    "instanceIdentitySha256",
    "customerBindingSha256",
    "nodePoolIdentitySha256",
    "machineIdentitySha256",
    "nodeIdentitySha256",
    "computeClaimCvm",
    "computeClaimNode",
    "poolHeadAuthoritative",
    "poolHead"
  ]) assert.match(readback, new RegExp(field));
  assert.match(readback, /get configmap opl-cloud-config -o jsonpath=.*OPL_RELEASE_SHA/);
  for (const identityField of ["resourceId", "accountId", "workspaceId", "packageId", "nodePoolId", "machineId", "instanceId", "nodeName"]) {
    assert.match(readback, new RegExp(`ownership\\?\\.${identityField}`));
  }
  assert.match(readback, /runnerDirectMutationCounts:\s*\{\s*sub2api:\s*0,\s*tencent:\s*0,\s*kubernetes:\s*0,\s*fabric:\s*0\s*\}/);
  assert.doesNotMatch(readback, /method:\s*["']POST["']|\b(?:kubectl|curl)\s+(?:apply|delete|patch|scale)\b/);
  assert.deepEqual(contract.deployWorkflow.freshComputeReadback, {
    authority: "fabric_operations_allocation_machine_ownership_and_pool_head_get_only",
    binding: ["production_release", "launch_operation", "compute_allocation", "customer", "workspace", "package", "node_pool", "machine", "instance", "node"],
    mutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0, fabric: 0 },
    artifact: "redacted_digests_statuses_and_match_booleans_only"
  });
});

test("ordinary TKE release isolates cluster and public read-only verifiers", async () => {
  const [workflow, contract] = await Promise.all([
    readWorkflow(".github/workflows/deploy-tke-production.yml"),
    readJson(deploymentContractPath)
  ]);
  const clusterVerifier = workflowJob(workflow, "verify-rollout-cluster");
  const publicVerifier = workflowJob(workflow, "verify-rollout-public-read-only");
  const releaseGate = workflowJob(workflow, "release-gate");
  const diagnostics = workflowJob(workflow, "capture-rollout-failure");
  const rollback = workflowJob(workflow, "rollback-live-qa");
  const clusterRun = serializedRuns(clusterVerifier);
  const publicRun = serializedRuns(publicVerifier);
  const releaseRun = serializedRuns(releaseGate);

  assert.equal(workflow.jobs["verify-rollout-read-only"], undefined);
  assert.equal(clusterVerifier.needs, "deploy");
  assert.match(String(clusterVerifier.if), /!inputs\.bootstrap_mode.*needs\.deploy\.result == 'success'/);
  assert.deepEqual(clusterVerifier["runs-on"], ["self-hosted", "tencent-cloud", "opl-cloud", "tke-vpc"]);
  assert.equal(clusterVerifier.environment, "production");
  assert.match(clusterRun, /imageID|imageId/);
  assert.match(clusterRun, /metadata\?\.deletionTimestamp/);
  assert.match(clusterRun, /condition\.type === "Ready"/);
  assert.match(clusterRun, /deployment\.kubernetes\.io\/revision/);
  assert.match(clusterRun, /get replicasets/);
  assert.match(clusterRun, /ownerReferences/);
  assert.match(clusterRun, /owner\.controller === true/);
  assert.match(clusterRun, /owner\.kind === kind/);
  assert.match(clusterRun, /owner\.name === name/);
  assert.match(clusterRun, /owner\.uid === uid/);
  assert.match(clusterRun, /OPL_CLOUD_IMAGE/);
  assert.match(clusterRun, /OPL_WORKSPACE_IMAGE/);
  assert.match(clusterRun, /get configmap opl-cloud-config/);
  assert.doesNotMatch(clusterRun, /playwright|npm ci|production-live-qa|api\/healthz|api\/production\/readiness/i);
  for (const key of ["OPL_CONSOLE_ORIGIN", "OPL_SUB2API_ADMIN_EMAIL", "OPL_SUB2API_ADMIN_PASSWORD", "OPL_VERIFY_REQUEST_TIMEOUT_MS"]) {
    assert.equal(Object.hasOwn(clusterVerifier.env, key), false, `cluster verifier must not receive ${key}`);
  }

  assert.deepEqual(publicVerifier.needs, ["deploy", "verify-rollout-cluster"]);
  assert.match(String(publicVerifier.if), /!inputs\.bootstrap_mode.*needs\.deploy\.result == 'success'.*needs\.verify-rollout-cluster\.result == 'success'/);
  assert.equal(publicVerifier["runs-on"], "ubuntu-latest");
  assert.equal(publicVerifier.environment, "production");
  assert.equal(publicVerifier.env.OPL_SUB2API_ADMIN_EMAIL, "${{ secrets.OPL_SUB2API_ADMIN_EMAIL }}");
  assert.equal(publicVerifier.env.OPL_SUB2API_ADMIN_PASSWORD, "${{ secrets.OPL_SUB2API_ADMIN_PASSWORD }}");
  assert.equal(Object.hasOwn(publicVerifier.env, "OPL_VERIFY_AUTH_USERS_JSON"), false);
  assert.equal(Object.hasOwn(publicVerifier.env, "OPL_VERIFY_ACCOUNT_ID"), false);
  assert.match(publicRun, /npm ci/);
  assert.match(publicRun, /playwright install --with-deps chromium/);
  assert.match(publicRun, /production-live-qa\.ts --read-only/);
  assert.match(publicRun, /api\/healthz/);
  assert.match(publicRun, /api\/production\/readiness/);
  assert.match(publicRun, /retired|404/);
  assert.doesNotMatch(JSON.stringify(publicVerifier), /KUBECONFIG|TENCENT_DEPLOY|TENCENTCLOUD|kubectl/i);
  assert.doesNotMatch(publicRun, /purchase|redeem|model request|provider mutation|--allow-gateway-write|--allow-model-write/i);

  assert.equal(contract.productionReadOnlyRolloutVerifierJob, undefined);
  assert.equal(contract.productionRolloutClusterVerifierJob.job, "verify-rollout-cluster");
  assert.equal(contract.productionRolloutClusterVerifierJob.readOnly, true);
  assert.deepEqual(contract.productionRolloutClusterVerifierJob.podRequirements, ["three_cloud_pods_ready", "candidate_cloud_image_id"]);
  assert.equal(contract.productionRolloutClusterVerifierJob.workspaceRequirement, "configmap_digest_unchanged");
  assert.deepEqual(contract.productionRolloutClusterVerifierJob.secretEnv, [
    "TENCENT_DEPLOY_KUBECONFIG_B64",
    "TENCENT_DEPLOY_KUBECONFIG"
  ]);
  assert.equal(contract.productionPublicReadOnlyVerifierJob.job, "verify-rollout-public-read-only");
  assert.equal(contract.productionPublicReadOnlyVerifierJob.readOnly, true);
  assert.equal(contract.productionPublicReadOnlyVerifierJob.businessMutationCount, 0);
  assert.deepEqual(contract.productionPublicReadOnlyVerifierJob.secretEnv, [
    "OPL_SUB2API_ADMIN_EMAIL",
    "OPL_SUB2API_ADMIN_PASSWORD"
  ]);
  assert.deepEqual(releaseGate.needs, ["deploy", "bootstrap-readiness", "verify-rollout-cluster", "verify-rollout-public-read-only", "rollback-live-qa"]);
  assert.match(releaseRun, /needs\.deploy\.result.*success/);
  assert.match(releaseRun, /needs\.verify-rollout-cluster\.result.*success/);
  assert.match(releaseRun, /needs\.verify-rollout-public-read-only\.result.*success/);
  assert.match(releaseRun, /needs\.rollback-live-qa\.result.*skipped/);
  assert.match(String(diagnostics.if), /verify-rollout-cluster/);
  assert.match(String(diagnostics.if), /verify-rollout-public-read-only/);
  assert.match(String(rollback.if), /capture-rollout-failure/);
  assert.deepEqual(rollback.needs, ["deploy", "bootstrap-readiness", "verify-rollout-cluster", "verify-rollout-public-read-only", "capture-rollout-failure"]);
  assert.deepEqual(contract.productionReleaseGateJob.needs, ["deploy", "bootstrap-readiness", "verify-rollout-cluster", "verify-rollout-public-read-only", "rollback-live-qa"]);
});

test("TKE bootstrap deploy is approved, read only, and cannot complete a release", async () => {
  const workflow = await readWorkflow(".github/workflows/deploy-tke-production.yml");
  const input = workflow.on.workflow_dispatch.inputs.bootstrap_mode;
  const deploy = workflowJob(workflow, "deploy");
  const bootstrap = workflowJob(workflow, "bootstrap-readiness");
  const releaseGate = workflowJob(workflow, "release-gate");
  const diagnostics = workflowJob(workflow, "capture-rollout-failure");
  const rollback = workflowJob(workflow, "rollback-live-qa");
  const rolloutRun = serializedStep(stepsByName(deploy).get("Render and apply manifest"));
  const rollbackRun = serializedStep(stepsByName(rollback).get("Restore previous Cloud images and ConfigMap"));
  const bootstrapRun = serializedRuns(bootstrap);
  const releaseRun = serializedRuns(releaseGate);

  assert.equal(input.type, "boolean");
  assert.equal(input.default, false);
  assert.equal(deploy.environment, "production");
  assert.equal(deploy.env.OPL_BOOTSTRAP_MODE, "${{ inputs.bootstrap_mode }}");
  assert.match(String(deploy.env.OPL_MONTHLY_BILLING_WORKER_ENABLED), /inputs\.bootstrap_mode.*'0'/);
  assert.equal(bootstrap.needs, "deploy");
  assert.equal(bootstrap.if, "${{ inputs.bootstrap_mode && needs.deploy.result == 'success' }}");
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

  assert.deepEqual(releaseGate.needs, ["deploy", "bootstrap-readiness", "verify-rollout-cluster", "verify-rollout-public-read-only", "rollback-live-qa"]);
  assert.equal(releaseGate.if, "${{ always() && !inputs.diagnostics_only && !inputs.promote_workspace_image }}");
  assert.match(releaseRun, /release incomplete/i);
  assert.match(releaseRun, /releaseComplete.*false/s);
  assert.match(releaseRun, /exit 1/);
  assert.deepEqual(rollback.needs, ["deploy", "bootstrap-readiness", "verify-rollout-cluster", "verify-rollout-public-read-only", "capture-rollout-failure"]);
  assert.match(String(diagnostics.if), /inputs\.bootstrap_mode.*needs\.bootstrap-readiness\.result != 'success'/);
  assert.match(String(diagnostics.if), /!inputs\.bootstrap_mode.*needs\.deploy\.result != 'success'/);
  assert.doesNotMatch(String(rollback.if), /release-gate/);
  assert.match(rolloutRun, /OPL_BOOTSTRAP_MODE[\s\S]*apply_bootstrap_images/);
  assert.doesNotMatch(rolloutRun, /restore_previous_bootstrap_images/);
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
  assert.equal(inputs.workspace_app_main_sha.required, false);
  assert.equal(inputs.workspace_shell_main_sha.required, false);
  assert.equal(inputs.workspace_framework_main_sha.required, false);
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

  const cloudOnly = runImageMetadata(steps.get("Image metadata"), "", "", "", {
    publishCloudImage: true,
    publishWorkspaceImage: false
  });
  assert.equal(cloudOnly.status, 0, cloudOnly.stderr);
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

test("TKE deploy installs Sub2API credentials and isolated production acceptance approvals", async () => {
  const workflow = await readWorkflow(".github/workflows/deploy-tke-production.yml");
  const currentJob = workflowJob(workflow, "deploy");
  const steps = stepsByName(currentJob);
  const prepare = serializedStep(steps.get("Prepare kubeconfig"));
  const install = serializedStep(steps.get("Install Kubernetes secrets"));
  const cleanup = steps.get("Remove deployment secrets");

  assert.match(install, /create secret generic opl-cloud-sub2api/);
  assert.match(install, /--from-file=OPL_SUB2API_ADMIN_EMAIL/);
  assert.match(install, /--from-file=OPL_SUB2API_ADMIN_PASSWORD/);
  assert.match(install, /create secret generic opl-cloud-recovery-acceptance-canary/);
  assert.match(install, /--from-file=OPL_RECOVERY_ACCEPTANCE_CANARY_APPROVAL_JSON="\$secret_dir\/recovery-acceptance-canary-approval"/);
  assert.equal(currentJob.env.OPL_RECOVERY_ACCEPTANCE_CANARY_ENABLED, "${{ vars.OPL_RECOVERY_ACCEPTANCE_CANARY_ENABLED || '0' }}");
  assert.equal(currentJob.env.OPL_RECOVERY_ACCEPTANCE_CANARY_ACCOUNT_IDS, "${{ vars.OPL_RECOVERY_ACCEPTANCE_CANARY_ACCOUNT_IDS || '' }}");
  assert.equal(currentJob.env.OPL_RECOVERY_ACCEPTANCE_CANARY_APPROVAL_JSON, "${{ secrets.OPL_RECOVERY_ACCEPTANCE_CANARY_APPROVAL_JSON }}");
  assert.match(install, /create secret generic opl-cloud-acceptance-b/);
  assert.match(install, /--from-file=OPL_PRODUCTION_BASIC_ACCEPTANCE_B_APPROVAL_JSON="\$secret_dir\/production-basic-acceptance-b-approval"/);
  assert.equal(currentJob.env.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_APPROVAL_JSON, "${{ secrets.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_APPROVAL_JSON }}");
  assert.equal(Object.hasOwn(currentJob.env, "OPL_PROVIDER_ACCEPTANCE_TOKEN"), false);
  assert.doesNotMatch(install, /provider-acceptance|OPL_PROVIDER_ACCEPTANCE_TOKEN/);
  assert.doesNotMatch(install, /OPL_BASIC_CANARY_CUSTOMER_PASSWORD|OPL_PRODUCTION_BASIC_ACCEPTANCE_B_CUSTOMER_PASSWORD/);
  assert.match(serializedStep(steps.get("Check deployment inputs")), /OPL_RECOVERY_ACCEPTANCE_CANARY_ENABLED/);
  assert.match(serializedStep(steps.get("Check deployment inputs")), /dedicated approval Secret/);
  assert.equal(currentJob.env.OPL_TENCENT_ZONE, "${{ vars.OPL_TENCENT_ZONE || 'na-siliconvalley-1' }}");
  assert.equal(currentJob.env.TENCENTCLOUD_REGION, "${{ vars.TENCENTCLOUD_REGION || 'na-siliconvalley' }}");
  assert.equal(currentJob.env.OPL_BASIC_COMPUTE_INSTANCE_TYPE, "${{ vars.OPL_BASIC_COMPUTE_INSTANCE_TYPE }}");
  assert.equal(currentJob.env.OPL_PRO_COMPUTE_INSTANCE_TYPE, "${{ vars.OPL_PRO_COMPUTE_INSTANCE_TYPE }}");
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
  const workflow = await readWorkflow(".github/workflows/deploy-tke-production.yml");

  assert.equal(workflowJob(workflow, "deploy").env.OPL_RELEASE_SHA, "${{ github.sha }}");

  for (const key of [
    "OPL_MONTHLY_BILLING_WORKER_ENABLED",
    "OPL_MONTHLY_BILLING_INTERVAL_MS",
    "OPL_WORKSPACE_LAUNCH_WORKER_ENABLED",
    "OPL_WORKSPACE_LAUNCH_INTERVAL_MS",
    "OPL_CONTROLLED_BASIC_PILOT_ENABLED",
    "OPL_CONTROLLED_BASIC_PILOT_ACCOUNT_IDS",
    "OPL_CONTROLLED_BASIC_PILOT_MAX_IN_FLIGHT",
    "OPL_RECOVERY_ACCEPTANCE_CANARY_ENABLED",
    "OPL_RECOVERY_ACCEPTANCE_CANARY_ACCOUNT_IDS",
    "OPL_RECOVERY_ACCEPTANCE_CANARY_APPROVAL_JSON",
    "OPL_PRODUCTION_BASIC_ACCEPTANCE_B_APPROVAL_JSON",
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
  assert.equal(config.data.OPL_RELEASE_SHA, values.OPL_RELEASE_SHA);
  assert.equal(config.data.OPL_SUB2API_BASE_URL, values.OPL_SUB2API_BASE_URL);
  assert.equal(config.data.OPL_SUB2API_REQUEST_TIMEOUT_MS, "7000");
  assert.equal(config.data.OPL_TENCENT_ZONE, "ap-guangzhou-3");
  assert.equal(config.data.OPL_MONTHLY_BILLING_INTERVAL_MS, "60000");
  assert.equal(config.data.OPL_CONTROLLED_BASIC_PILOT_ENABLED, "0");
  assert.equal(config.data.OPL_CONTROLLED_BASIC_PILOT_ACCOUNT_IDS, "");
  assert.equal(config.data.OPL_CONTROLLED_BASIC_PILOT_MAX_IN_FLIGHT, "1");
  assert.equal(config.data.OPL_OPERATOR_CIDRS, undefined);
  assert.equal(config.data.OPL_TRUSTED_PROXY_CIDRS, undefined);
  assert.doesNotMatch(source, /postgresql:\/\//i);
  const controlPlane = rendered.items.find((item) => item.kind === "Deployment" && item.metadata.name === "opl-cloud-control-plane");
  assert.deepEqual(controlPlane.spec.template.spec.containers[0].envFrom, [{ configMapRef: { name: "opl-cloud-config" } }]);
  const sub2apiEnv = controlPlane.spec.template.spec.containers[0].env.filter((item) => item.name.startsWith("OPL_SUB2API_ADMIN_"));
  assert.equal(sub2apiEnv.length, 2);
  assert.equal(sub2apiEnv.every((item) => item.valueFrom?.secretKeyRef && item.value === undefined), true);
  const acceptanceBEnv = controlPlane.spec.template.spec.containers[0].env.find((item) => item.name === "OPL_PRODUCTION_BASIC_ACCEPTANCE_B_APPROVAL_JSON");
  assert.deepEqual(acceptanceBEnv?.valueFrom?.secretKeyRef, { name: "opl-cloud-acceptance-b", key: "OPL_PRODUCTION_BASIC_ACCEPTANCE_B_APPROVAL_JSON" });

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

test("TKE manifest renderer keeps the controlled Basic Pilot closed unless explicitly configured", async () => {
  const { manifest, values } = await manifestFixture();
  assert.throws(
    () => renderTkeManifest({ manifest, values: { ...values, OPL_CONTROLLED_BASIC_PILOT_ENABLED: "true" } }),
    /controlled_basic_pilot_enabled_invalid/
  );
  assert.throws(
    () => renderTkeManifest({ manifest, values: { ...values, OPL_CONTROLLED_BASIC_PILOT_ENABLED: "1", OPL_CONTROLLED_BASIC_PILOT_ACCOUNT_IDS: "" } }),
    /controlled_basic_pilot_account_ids_required/
  );
  assert.throws(
    () => renderTkeManifest({ manifest, values: { ...values, OPL_CONTROLLED_BASIC_PILOT_MAX_IN_FLIGHT: "0" } }),
    /controlled_basic_pilot_max_in_flight_invalid/
  );
  const enabled = renderTkeManifest({
    manifest,
    values: { ...values, OPL_CONTROLLED_BASIC_PILOT_ENABLED: "1", OPL_CONTROLLED_BASIC_PILOT_ACCOUNT_IDS: "acct-pilot" }
  });
  const config = enabled.items.find((item) => item.kind === "ConfigMap");
  assert.equal(config.data.OPL_CONTROLLED_BASIC_PILOT_ENABLED, "1");
  assert.equal(config.data.OPL_CONTROLLED_BASIC_PILOT_ACCOUNT_IDS, "acct-pilot");
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
      ...["opl-cloud-control-plane", "opl-cloud-ledger", "opl-cloud-fabric"].map((name) => writeFile(join(rollbackDir, `${name}.deployment.json`), JSON.stringify({ metadata: { annotations: { "deployment.kubernetes.io/revision": "1" } } }))),
      writeFile(join(rollbackDir, "opl-cloud-config.json"), JSON.stringify({ data: values })),
      writeFile(join(rollbackDir, "node-storage-preflight.json"), "{}"),
      writeFile(kubectlLog, "")
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

test("TKE deploy preflights node storage and creates one candidate revision per Cloud Deployment", async () => {
  const workflow = await readWorkflow(".github/workflows/deploy-tke-production.yml");
  const currentJob = workflowJob(workflow, "deploy");
  const inputs = Object.keys(workflow.on.workflow_dispatch.inputs || {});
  const checks = serializedStep(stepsByName(currentJob).get("Check deployment inputs"));
  const preflight = serializedStep(stepsByName(currentJob).get("Preflight rollout node storage"));
  const capture = serializedStep(stepsByName(currentJob).get("Capture rollback image set"));
  const upload = stepsByName(currentJob).get("Upload rollback image set");
  const apply = serializedStep(stepsByName(currentJob).get("Render and apply manifest"));
  const rolloutHelper = await readFile(repoFile("tools/tke-image-rollout.sh"), "utf8");
  const stepNames = [...stepsByName(currentJob).keys()];

  assert.equal(inputs.includes("exercise_rollback"), false);
  assert.equal(workflow.on.workflow_dispatch.inputs.workspace_image.required, false);
  assert.equal(Object.hasOwn(currentJob.env, "OPL_EXERCISE_ROLLBACK"), false);
  assert.match(checks, /repository@sha256/);
  assert.match(checks, /get configmap opl-cloud-config/);
  assert.match(checks, /--ignore-not-found[\s\S]*2>\/dev\/null \|\| true/);
  assert.match(checks, /current_workspace_image/);
  assert.match(checks, /requested_workspace_image/);
  assert.match(checks, /must match the current production Workspace image/);
  assert.match(checks, /OPL_TENCENT_ZONE/);
  assert.match(checks, /sha256:\[0-9a-f\]\{64\}/);
  assert.doesNotMatch(checks, /must include a non-empty container tag/);
  assert.ok(stepNames.indexOf("Preflight rollout node storage") < stepNames.indexOf("Install Kubernetes secrets"));
  assert.match(preflight, /source tools\/tke-image-rollout\.sh/);
  assert.match(preflight, /preflight_rollout_storage/);
  assert.match(rolloutHelper, /DiskPressure/);
  assert.match(rolloutHelper, /stats\/summary/);
  assert.match(rolloutHelper, /nodefs/);
  assert.match(rolloutHelper, /imagefs/);
  assert.match(rolloutHelper, /26843545600/);
  assert.ok(stepNames.indexOf("Capture rollback image set") < stepNames.indexOf("Upload rollback image set"));
  assert.ok(stepNames.indexOf("Upload rollback image set") < stepNames.indexOf("Render and apply manifest"));
  assert.equal(upload?.uses, "actions/upload-artifact@v4");
  assert.match(String(upload?.with?.name), /production-rollback-images/);
  assert.match(String(upload?.with?.path), /previous-images/);
  for (const deployment of ["opl-cloud-control-plane", "opl-cloud-ledger", "opl-cloud-fabric"]) {
    assert.match(capture, new RegExp(deployment));
  }
  assert.match(capture, /get configmap opl-cloud-config[\s\S]*-o json[\s\S]*opl-cloud-config\.json/);
  assert.match(capture, /deployment\.json/);
  assert.doesNotMatch(capture, /workspace-images\.tsv|list_workspace_images/);
  assert.doesNotMatch(rolloutHelper, /oplcloud\.cn\/workspace-id|set_workspace_images|wait_workspace_rollouts/);
  assert.match(apply, /source tools\/tke-image-rollout\.sh/);
  assert.match(apply, /apply_candidate_images/);
  assert.doesNotMatch(apply, /restore_previous|OPL_EXERCISE_ROLLBACK|trap .*ERR|set image|rollout restart/);
  const candidateFunction = rolloutHelper.match(/apply_candidate_images\(\) \{([\s\S]*?)\n\}/)?.[1] || "";
  assert.match(candidateFunction, /wait_cloud_rollouts candidate/);
  assert.doesNotMatch(candidateFunction, /set_cloud_images|rollout restart|set image|patch_workspace_image/);
  assert.match(rolloutHelper, /OPL_ROLLOUT_TIMEOUT_SECONDS:-300/);
  for (const reason of ["Evicted", "DiskPressure", "ImagePullBackOff", "CrashLoopBackOff", "Unschedulable"]) {
    assert.match(rolloutHelper, new RegExp(reason));
  }
  assert.doesNotMatch(apply, /set \+e/);
});

test("TKE rollout storage preflight checks only the configured system Node and ignores customer Nodes", async () => {
  const functions = await readFile(repoFile("tools/tke-image-rollout.sh"), "utf8");
  const root = await mkdtemp(join(tmpdir(), "opl-system-node-preflight-"));
  const commandLog = join(root, "kubectl.log");
  const provisioner = join(root, "provisioner");
  const provisionerLog = join(root, "provisioner.log");
  const systemPool = "np-system-test";
  const systemMachine = "machine-system-test";
  const systemNode = "node-system.test";
  const makeNode = (name, {
    workload = "medopl",
    nodePoolId = name === systemNode ? systemPool : `np-${name}`,
    machineId = name === systemNode ? systemMachine : `machine-${name}`,
    ready = true,
    diskPressure = false,
    unschedulable = false,
    taints = []
  } = {}) => ({
    metadata: { name, labels: {
      "medopl.cn/workload": workload,
      "node.tke.cloud.tencent.com/machineset": nodePoolId,
      "node.tke.cloud.tencent.com/machine": machineId
    } },
    spec: {
      ...(unschedulable ? { unschedulable } : {}),
      ...(taints.length > 0 ? { taints } : {})
    },
    status: {
      conditions: [
        { type: "Ready", status: ready ? "True" : "False" },
        { type: "DiskPressure", status: diskPressure ? "True" : "False" }
      ]
    }
  });
  const makeStats = (nodefsAvailable = 26 * 1024 ** 3, imagefsAvailable = nodefsAvailable) => JSON.stringify({ node: {
    fs: { capacityBytes: 100 * 1024 ** 3, availableBytes: nodefsAvailable },
    runtime: { imageFs: { capacityBytes: 100 * 1024 ** 3, availableBytes: imagefsAvailable } }
  } });

  await writeFile(provisioner, "#!/usr/bin/env bash\nprintf 'called\\n' >> \"$TEST_PROVISIONER_LOG\"\nexit 91\n");
  await chmod(provisioner, 0o700);

  const run = async ({
    nodes,
    stats = makeStats(),
    expectStatus,
    expectError = "",
    expectApply = false,
    expectStatsNode = systemNode
  }) => {
    await writeFile(commandLog, "");
    await writeFile(provisionerLog, "");
    const result = spawnSync("bash", ["-c", `
      set -euo pipefail
      kubectl() {
        printf 'kubectl %s\\n' "$*" >> "$TEST_COMMAND_LOG"
        case " $* " in
          *" get nodes -o json "*) printf '%s' "$TEST_NODES_JSON" ;;
          *" get --raw /api/v1/nodes/"*"/proxy/stats/summary "*) printf '%s' "$TEST_STATS_JSON" ;;
          *" apply -f candidate.json "*) : ;;
          *) return 64 ;;
        esac
      }
      ${functions}
      set +e
      preflight_rollout_storage "$TEST_ROOT/preflight.json"
      status=$?
      set -e
      if [ "$status" -eq 0 ]; then
        kubectl --kubeconfig "$KUBECONFIG" apply -f candidate.json
      fi
      exit "$status"
    `], {
      cwd: fileURLToPath(repoFile(".")),
      encoding: "utf8",
      env: {
        ...process.env,
        KUBECONFIG: "/dev/null",
        OPL_K8S_NAMESPACE: "opl-test",
        OPL_TENCENT_PROVISIONER_BIN: provisioner,
        OPL_SYSTEM_COMPUTE_NODE_POOL_ID: systemPool,
        OPL_SYSTEM_COMPUTE_MACHINE_ID: systemMachine,
        OPL_SYSTEM_COMPUTE_NODE_NAME: systemNode,
        TEST_COMMAND_LOG: commandLog,
        TEST_PROVISIONER_LOG: provisionerLog,
        TEST_ROOT: root,
        TEST_NODES_JSON: JSON.stringify({ items: nodes }),
        TEST_STATS_JSON: stats
      }
    });
    const commands = await readFile(commandLog, "utf8");
    assert.equal(await readFile(provisionerLog, "utf8"), "", "ordinary rollout must not call Workspace SKU/capacity inventory");
    assert.equal(result.status, expectStatus, `${result.stdout}\n${result.stderr}`);
    if (expectError) assert.match(`${result.stdout}\n${result.stderr}`, new RegExp(expectError));
    assert.equal((commands.match(/ apply -f candidate\.json/g) || []).length, expectApply ? 1 : 0, commands);
    let evidence;
    if (expectApply) {
      assert.match(commands, new RegExp(`get --raw /api/v1/nodes/${expectStatsNode}/proxy/stats/summary`));
      assert.equal((commands.match(/ get --raw \/api\/v1\/nodes\/.+\/proxy\/stats\/summary/g) || []).length, 1, commands);
      evidence = JSON.parse(await readFile(join(root, "preflight.json"), "utf8"));
      assert.equal(evidence.systemNodeCount, 1);
      assert.equal(evidence.systemIdentity.nodePoolMatched, true);
      assert.equal(evidence.systemIdentity.machineMatched, true);
      assert.equal(evidence.systemIdentity.nodeMatched, true);
      assert.deepEqual(evidence.nodes.map((node) => node.name), [systemNode]);
    }
    if (!expectApply) assert.doesNotMatch(commands, / apply -f /);
    return { result, commands, evidence };
  };

  try {
    await run({
      nodes: [makeNode(systemNode)],
      expectStatus: 0,
      expectApply: true
    });
    await run({
      nodes: [
        makeNode(systemNode),
        makeNode("node-customer-a", { workload: "medopl", taints: [{ key: "tenant.example/reserved", effect: "NoSchedule" }] }),
        makeNode("node-customer-b", { workload: "medopl", unschedulable: true }),
        makeNode("node-customer-c", { workload: "other", taints: [{ key: "tenant.example/preferred", effect: "PreferNoSchedule" }] })
      ],
      expectStatus: 0,
      expectApply: true
    });
    await run({
      nodes: [makeNode(systemNode), makeNode("node-customer-a"), makeNode("node-customer-b"), makeNode("node-customer-c")],
      expectStatus: 0,
      expectApply: true
    });

    const failures = [
      { name: "system Node missing", nodes: [makeNode("node-customer-a")], error: "rollout_system_node_missing" },
      { name: "system identity drift", nodes: [makeNode(systemNode, { nodePoolId: "np-other", machineId: "machine-other" })], error: "rollout_system_identity_mismatch" },
      { name: "Machine is not in configured NodePool", nodes: [makeNode(systemNode, { nodePoolId: "np-other" })], error: "rollout_system_identity_mismatch" },
      { name: "Node is not in configured Machine", nodes: [makeNode(systemNode, { machineId: "machine-other" })], error: "rollout_system_identity_mismatch" },
      { name: "multiple authoritative system candidates", nodes: [makeNode(systemNode), makeNode("node-system-duplicate.test", { nodePoolId: systemPool, machineId: systemMachine })], error: "rollout_system_identity_ambiguous" },
      { name: "NotReady", nodes: [makeNode(systemNode, { ready: false })], error: "rollout_node_not_ready" },
      { name: "DiskPressure", nodes: [makeNode(systemNode, { diskPressure: true })], error: "rollout_node_DiskPressure" },
      { name: "unschedulable", nodes: [makeNode(systemNode, { unschedulable: true })], error: "rollout_node_Unschedulable" },
      { name: "nodefs below minimum", nodes: [makeNode(systemNode)], stats: makeStats(24 * 1024 ** 3), error: "rollout_nodefs_below_25GiB" },
      { name: "imagefs below minimum", nodes: [makeNode(systemNode)], stats: makeStats(26 * 1024 ** 3, 24 * 1024 ** 3), error: "rollout_imagefs_below_25GiB" }
    ];
    for (const failure of failures) {
      await run({ ...failure, expectStatus: 1, expectError: failure.error, expectApply: false });
    }
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("TKE failure uploads complete diagnostics before the only rollback job", async () => {
  const workflow = await readWorkflow(".github/workflows/deploy-tke-production.yml");
  const deploy = workflowJob(workflow, "deploy");
  const diagnostics = workflowJob(workflow, "capture-rollout-failure");
  const rollback = workflowJob(workflow, "rollback-live-qa");
  const diagnosticSteps = stepsByName(diagnostics);
  const steps = stepsByName(rollback);
  const restore = serializedStep(steps.get("Restore previous Cloud images and ConfigMap"));
  const capture = serializedStep(diagnosticSteps.get("Capture failed rollout diagnostics"));
  const upload = diagnosticSteps.get("Upload failed rollout diagnostics");

  assert.match(String(deploy.outputs?.rollback_image_set), /rollback_snapshot\.outputs\.artifact-id/);
  assert.equal(stepsByName(deploy).get("Upload rollback image set")?.id, "rollback_snapshot");
  assert.deepEqual(diagnostics.needs, ["deploy", "bootstrap-readiness", "verify-rollout-cluster", "verify-rollout-public-read-only"]);
  assert.match(String(diagnostics.if), /always\(\).*needs\.deploy\.outputs\.rollback_image_set != ''.*needs\.deploy\.result != 'success'.*needs\.verify-rollout-cluster\.result != 'success'.*needs\.verify-rollout-public-read-only\.result != 'success'/);
  assert.match(capture, /capture_rollout_diagnostics/);
  for (const token of ["deployments.json", "replicasets.json", "pods.json", "nodes.json", "events.json", "stats-summary", "ownerReferences", "deletionTimestamp", "previous"]) {
    assert.match(`${capture}\n${await readFile(repoFile("tools/tke-image-rollout.sh"), "utf8")}`, new RegExp(token));
  }
  assert.equal(upload?.uses, "actions/upload-artifact@v4");
  assert.match(String(upload?.with?.name), /production-rollout-diagnostics/);
  assert.ok([...diagnosticSteps.keys()].indexOf("Capture failed rollout diagnostics") < [...diagnosticSteps.keys()].indexOf("Upload failed rollout diagnostics"));
  assert.deepEqual(rollback.needs, ["deploy", "bootstrap-readiness", "verify-rollout-cluster", "verify-rollout-public-read-only", "capture-rollout-failure"]);
  assert.match(String(rollback.if), /needs\.capture-rollout-failure\.result == 'success'/);
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

test("ordinary TKE rollout preserves every Workspace deployment and Secret", async () => {
  const workflow = await readWorkflow(".github/workflows/deploy-tke-production.yml");
  const deploy = workflowJob(workflow, "deploy");
  const source = JSON.stringify(workflow);
  const helper = await readFile(repoFile("tools/tke-image-rollout.sh"), "utf8");
  assert.equal(workflow.jobs["retire-legacy-workspace-secret"], undefined);
  assert.doesNotMatch(source, /delete secret opl-cloud-workspace-codex|deployment\/workspace-|oplcloud\.cn\/workspace-id/);
  assert.doesNotMatch(helper, /deployment\/workspace-|oplcloud\.cn\/workspace-id/);
  assert.match(String(deploy.outputs?.workspace_image), /deployment_inputs\.outputs\.workspace_image/);
});

test("TKE storage preflight blocks every apply below 25 GiB and records nodefs/imagefs facts", async () => {
  const functions = await readFile(repoFile("tools/tke-image-rollout.sh"), "utf8");
  const root = await mkdtemp(join(tmpdir(), "opl-storage-preflight-"));
  const commandLog = join(root, "kubectl.log");
  const systemPool = "np-storage-test";
  const systemMachine = "machine-storage-test";
  const systemNode = "node-storage.test";
  const nodes = JSON.stringify({ items: [{
    metadata: { name: systemNode, labels: {
      "medopl.cn/workload": "medopl",
      "node.tke.cloud.tencent.com/machineset": systemPool,
      "node.tke.cloud.tencent.com/machine": systemMachine
    } },
    spec: {},
    status: { conditions: [
      { type: "Ready", status: "True" },
      { type: "DiskPressure", status: "False" }
    ] }
  }] });
  const stats = (availableBytes) => JSON.stringify({ node: {
    nodeName: systemNode,
    fs: { capacityBytes: 100 * 1024 ** 3, availableBytes },
    runtime: { imageFs: { capacityBytes: 100 * 1024 ** 3, availableBytes } }
  } });
  const harness = `
set -euo pipefail
kubectl() {
  printf '%s\\n' "$*" >> "$TEST_COMMAND_LOG"
  case " $* " in
    *" get nodes -o json "*) printf '%s' "$TEST_NODES_JSON" ;;
    *" get --raw /api/v1/nodes/${systemNode}/proxy/stats/summary "*) printf '%s' "$TEST_STATS_JSON" ;;
    *" apply -f candidate.json "*) ;;
    *) return 64 ;;
  esac
}
${functions}
preflight_rollout_storage "$TEST_ROOT/preflight.json"
kubectl --kubeconfig "$KUBECONFIG" apply -f candidate.json
`;
  const run = (availableBytes) => spawnSync("bash", ["-c", harness], {
    cwd: fileURLToPath(repoFile(".")),
    encoding: "utf8",
    env: {
      ...process.env,
      KUBECONFIG: "/dev/null",
      OPL_K8S_NAMESPACE: "opl-test",
      OPL_SYSTEM_COMPUTE_NODE_POOL_ID: systemPool,
      OPL_SYSTEM_COMPUTE_MACHINE_ID: systemMachine,
      OPL_SYSTEM_COMPUTE_NODE_NAME: systemNode,
      TEST_COMMAND_LOG: commandLog,
      TEST_NODES_JSON: nodes,
      TEST_ROOT: root,
      TEST_STATS_JSON: stats(availableBytes)
    }
  });

  try {
    await writeFile(commandLog, "");
    const lowDisk = run(24 * 1024 ** 3);
    assert.notEqual(lowDisk.status, 0);
    assert.doesNotMatch(await readFile(commandLog, "utf8"), / apply /);

    await writeFile(commandLog, "");
    const enoughDisk = run(26 * 1024 ** 3);
    assert.equal(enoughDisk.status, 0, enoughDisk.stderr);
    assert.match(await readFile(commandLog, "utf8"), / apply -f candidate\.json/);
    const evidence = JSON.parse(await readFile(join(root, "preflight.json"), "utf8"));
    assert.equal(evidence.minimumAvailableBytes, 25 * 1024 ** 3);
    assert.equal(evidence.nodes[0].nodefs.availableBytes, 26 * 1024 ** 3);
    assert.equal(evidence.nodes[0].imagefs.availableBytes, 26 * 1024 ** 3);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("TKE shared rollout observer fails before sleeping on DiskPressure", async () => {
  const functions = await readFile(repoFile("tools/tke-image-rollout.sh"), "utf8");
  const root = await mkdtemp(join(tmpdir(), "opl-rollout-observer-"));
  const commandLog = join(root, "commands.log");
  const nodes = JSON.stringify({ items: [{
    metadata: { name: "10.66.0.42" },
    status: { conditions: [{ type: "DiskPressure", status: "True", reason: "KubeletHasDiskPressure" }] }
  }] });
  const harness = `
kubectl() {
  printf 'kubectl %s\\n' "$*" >> "$TEST_COMMAND_LOG"
  case " $* " in
    *" get nodes -o json "*) printf '%s' "$TEST_NODES_JSON" ;;
    *) return 64 ;;
  esac
}
sleep() { printf 'sleep %s\\n' "$*" >> "$TEST_COMMAND_LOG"; }
${functions}
wait_cloud_rollouts candidate
`;
  try {
    const result = spawnSync("bash", ["-c", harness], {
      cwd: fileURLToPath(repoFile(".")),
      encoding: "utf8",
      env: {
        ...process.env,
        KUBECONFIG: "/dev/null",
        OPL_CLOUD_IMAGE: `registry.example.test/opl/cloud@sha256:${"b".repeat(64)}`,
        OPL_K8S_NAMESPACE: "opl-test",
        TEST_COMMAND_LOG: commandLog,
        TEST_NODES_JSON: nodes,
        TEST_ROOT: root
      }
    });
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /DiskPressure/);
    assert.doesNotMatch(await readFile(commandLog, "utf8"), /^sleep /m);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("TKE rollout observer ignores an Evicted Pod from a historical revision", async () => {
  const image = `registry.example.test/opl/cloud@sha256:${"b".repeat(64)}`;
  const fixture = rolloutFixture(image);
  addHistoricalEvictedFabricPod(fixture, `registry.example.test/opl/cloud@sha256:${"a".repeat(64)}`);

  const { result, commands } = await runRolloutObserver({ fixture, image });

  assert.equal(result.status, 0, result.stderr);
  assert.match(commands, /get replicasets -l app\.kubernetes\.io\/name=opl-cloud -o json/);
  assert.doesNotMatch(commands, /^sleep /m);
});

test("TKE rollout observer fail-fast reasons apply only to the current revision Pod", async () => {
  const image = `registry.example.test/opl/cloud@sha256:${"b".repeat(64)}`;
  for (const reason of ["Evicted", "Unschedulable", "ImagePullBackOff", "CrashLoopBackOff"]) {
    const fixture = rolloutFixture(image);
    const pod = fixture.pods.find((item) => item.metadata.labels["app.kubernetes.io/component"] === "fabric");
    setRolloutPodFailure(pod, reason);

    const { result, commands } = await runRolloutObserver({ fixture, image });

    assert.notEqual(result.status, 0, `${reason} must fail the current revision`);
    assert.match(result.stderr, new RegExp(`rollout_${reason}:${pod.metadata.name}`));
    assert.doesNotMatch(commands, /^sleep /m, `${reason} must fail before sleeping`);
  }
});

test("TKE rollback observer ignores historical eviction and confirms the restored image", async () => {
  const previousImage = `registry.example.test/opl/cloud@sha256:${"a".repeat(64)}`;
  const candidateImage = `registry.example.test/opl/cloud@sha256:${"b".repeat(64)}`;
  const fixture = rolloutFixture(previousImage, "242");
  addHistoricalEvictedFabricPod(fixture, candidateImage, "241");

  const { result, commands } = await runRolloutObserver({
    fixture,
    image: candidateImage,
    mode: "previous",
    previousImage
  });

  assert.equal(result.status, 0, result.stderr);
  assert.doesNotMatch(commands, /^sleep /m);
});

test("TKE rollout observer treats ReplicaSet and Pod owner UID mismatches as pending", async () => {
  const image = `registry.example.test/opl/cloud@sha256:${"b".repeat(64)}`;
  for (const ownerKind of ["replicaset", "pod"]) {
    const fixture = rolloutFixture(image);
    const replicaSet = fixture.replicasets.find((item) => item.metadata.name === "opl-cloud-fabric-current");
    const pod = fixture.pods.find((item) => item.metadata.labels["app.kubernetes.io/component"] === "fabric");
    if (ownerKind === "replicaset") {
      replicaSet.metadata.ownerReferences[0].uid = "wrong-deployment-uid";
    } else {
      pod.metadata.ownerReferences[0].uid = "wrong-replicaset-uid";
    }
    setRolloutPodFailure(pod, "Evicted");

    const { result, commands } = await runRolloutObserver({ fixture, image });

    assert.equal(result.status, 73, `${ownerKind} owner mismatch must remain pending: ${result.stderr}`);
    assert.doesNotMatch(result.stderr, /rollout_Evicted/);
    assert.match(commands, /^sleep 1$/m);
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
      ...["opl-cloud-control-plane", "opl-cloud-ledger", "opl-cloud-fabric"].map((name) => writeFile(join(rollbackDir, `${name}.deployment.json`), JSON.stringify({ metadata: { annotations: { "deployment.kubernetes.io/revision": "1" } } }))),
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
            if [[ " $* " == *" get nodes -o json "* ]]; then
              printf '{"items":[{"metadata":{"name":"node-1"},"status":{"conditions":[{"type":"DiskPressure","status":"False"}]}}]}'
            elif [[ " $* " == *" get deployment opl-cloud-control-plane opl-cloud-ledger opl-cloud-fabric -o json "* ]]; then
              printf '{"items":[{"metadata":{"name":"opl-cloud-control-plane","uid":"deployment-control-plane","generation":2,"annotations":{"deployment.kubernetes.io/revision":"2"}},"spec":{"replicas":1,"template":{"spec":{"containers":[{"name":"control-plane","image":"%s"}]}}},"status":{"observedGeneration":2,"updatedReplicas":1,"readyReplicas":1,"availableReplicas":1,"unavailableReplicas":0}},{"metadata":{"name":"opl-cloud-ledger","uid":"deployment-ledger","generation":2,"annotations":{"deployment.kubernetes.io/revision":"2"}},"spec":{"replicas":1,"template":{"spec":{"containers":[{"name":"ledger","image":"%s"}]}}},"status":{"observedGeneration":2,"updatedReplicas":1,"readyReplicas":1,"availableReplicas":1,"unavailableReplicas":0}},{"metadata":{"name":"opl-cloud-fabric","uid":"deployment-fabric","generation":2,"annotations":{"deployment.kubernetes.io/revision":"2"}},"spec":{"replicas":1,"template":{"spec":{"containers":[{"name":"fabric","image":"%s"}]}}},"status":{"observedGeneration":2,"updatedReplicas":1,"readyReplicas":1,"availableReplicas":1,"unavailableReplicas":0}}]}' "\${images[opl-cloud-control-plane]}" "\${images[opl-cloud-ledger]}" "\${images[opl-cloud-fabric]}"
            elif [[ " $* " == *" get replicasets -l app.kubernetes.io/name=opl-cloud -o json "* ]]; then
              printf '{"items":[{"metadata":{"name":"opl-cloud-control-plane-current","uid":"replicaset-control-plane","annotations":{"deployment.kubernetes.io/revision":"2"},"ownerReferences":[{"controller":true,"kind":"Deployment","name":"opl-cloud-control-plane","uid":"deployment-control-plane"}]},"spec":{"template":{"spec":{"containers":[{"name":"control-plane","image":"%s"}]}}}},{"metadata":{"name":"opl-cloud-ledger-current","uid":"replicaset-ledger","annotations":{"deployment.kubernetes.io/revision":"2"},"ownerReferences":[{"controller":true,"kind":"Deployment","name":"opl-cloud-ledger","uid":"deployment-ledger"}]},"spec":{"template":{"spec":{"containers":[{"name":"ledger","image":"%s"}]}}}},{"metadata":{"name":"opl-cloud-fabric-current","uid":"replicaset-fabric","annotations":{"deployment.kubernetes.io/revision":"2"},"ownerReferences":[{"controller":true,"kind":"Deployment","name":"opl-cloud-fabric","uid":"deployment-fabric"}]},"spec":{"template":{"spec":{"containers":[{"name":"fabric","image":"%s"}]}}}}]}' "\${images[opl-cloud-control-plane]}" "\${images[opl-cloud-ledger]}" "\${images[opl-cloud-fabric]}"
            elif [[ " $* " == *" get pods -l app.kubernetes.io/name=opl-cloud -o json "* ]]; then
              printf '{"items":[{"metadata":{"name":"opl-cloud-control-plane-current-pod","labels":{"app.kubernetes.io/component":"control-plane"},"ownerReferences":[{"controller":true,"kind":"ReplicaSet","name":"opl-cloud-control-plane-current","uid":"replicaset-control-plane"}]},"status":{"containerStatuses":[{"name":"control-plane","state":{"running":{}}}]}},{"metadata":{"name":"opl-cloud-ledger-current-pod","labels":{"app.kubernetes.io/component":"ledger"},"ownerReferences":[{"controller":true,"kind":"ReplicaSet","name":"opl-cloud-ledger-current","uid":"replicaset-ledger"}]},"status":{"containerStatuses":[{"name":"ledger","state":{"running":{}}}]}},{"metadata":{"name":"opl-cloud-fabric-current-pod","labels":{"app.kubernetes.io/component":"fabric"},"ownerReferences":[{"controller":true,"kind":"ReplicaSet","name":"opl-cloud-fabric-current","uid":"replicaset-fabric"}]},"status":{"containerStatuses":[{"name":"fabric","state":{"running":{}}}]}}]}'
            elif [[ " $* " == *" configmap opl-cloud-config "* ]]; then
              printf '%s' "$config_image"
            elif [[ " $* " == *" deployment/"*"jsonpath={.metadata.annotations.deployment"* ]]; then
              printf '2'
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
      apply_candidate_state() {
        config_image="$OPL_WORKSPACE_IMAGE"
        images[opl-cloud-control-plane]="$OPL_CLOUD_IMAGE"
        images[opl-cloud-ledger]="$OPL_CLOUD_IMAGE"
        images[opl-cloud-fabric]="$OPL_CLOUD_IMAGE"
      }
      if [ "\${TEST_BOOTSTRAP_ONLY:-0}" = "1" ]; then
        apply_candidate_state
        apply_bootstrap_images
        printf '%s\n' "$config_image" "\${images[opl-cloud-control-plane]}" "\${images[opl-cloud-ledger]}" "\${images[opl-cloud-fabric]}" "\${images[workspace-slot-1]}" "\${images[workspace-late]}" > "$TEST_ROOT/bootstrap-candidate.txt"
        restore_previous_bootstrap_images
        printf '%s\n' "$config_image" "\${images[opl-cloud-control-plane]}" "\${images[opl-cloud-ledger]}" "\${images[opl-cloud-fabric]}" "\${images[workspace-slot-1]}" "\${images[workspace-late]}" > "$TEST_ROOT/bootstrap-restored.txt"
        apply_candidate_state
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
      apply_candidate_state
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
        OPL_WORKSPACE_IMAGE: oldWorkspace,
        TEST_ROOT: root
      }
    });
    assert.equal(result.status, 0, result.stderr);
    assert.deepEqual((await readFile(join(root, "restored.txt"), "utf8")).trim().split("\n"), [oldWorkspace, oldCloud, oldCloud, oldCloud, oldWorkspace, oldWorkspace]);
    assert.deepEqual((await readFile(join(root, "candidate.txt"), "utf8")).trim().split("\n"), [oldWorkspace, candidateCloud, candidateCloud, candidateCloud, oldWorkspace, oldWorkspace]);

    const log = await readFile(join(root, "kubectl.log"), "utf8");
    for (const deployment of ["opl-cloud-control-plane", "opl-cloud-ledger", "opl-cloud-fabric"]) {
      assert.equal(log.match(new RegExp(`set image deployment/${deployment}`, "g"))?.length, 1, `${deployment} must receive exactly one rollback image mutation`);
      assert.equal(log.match(new RegExp(`get deployment/${deployment}`, "g"))?.length, 1, `${deployment} candidate revision must be checked once`);
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
        OPL_WORKSPACE_IMAGE: oldWorkspace,
        TEST_BOOTSTRAP_ONLY: "1",
        TEST_CURRENT_CLOUD_IMAGE: oldCloud,
        TEST_CURRENT_WORKSPACE_IMAGE: oldWorkspace,
        TEST_ROOT: root
      }
    });
    assert.equal(bootstrap.status, 0, bootstrap.stderr);
    assert.deepEqual((await readFile(join(root, "bootstrap-candidate.txt"), "utf8")).trim().split("\n"), [oldWorkspace, candidateCloud, candidateCloud, candidateCloud, oldWorkspace, oldWorkspace]);
    assert.deepEqual((await readFile(join(root, "bootstrap-restored.txt"), "utf8")).trim().split("\n"), [oldWorkspace, oldCloud, oldCloud, oldCloud, oldWorkspace, oldWorkspace]);
    assert.deepEqual((await readFile(join(root, "bootstrap-exercised.txt"), "utf8")).trim().split("\n"), [oldWorkspace, candidateCloud, candidateCloud, candidateCloud, oldWorkspace, oldWorkspace]);
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
