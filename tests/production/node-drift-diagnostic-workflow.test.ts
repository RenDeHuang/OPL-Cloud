import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { parse } from "yaml";

import * as diagnostic from "../../tools/production-node-drift-diagnostic.ts";

const repoFile = (path) => new URL(`../../${path}`, import.meta.url);
const readText = (path) => readFile(repoFile(path), "utf8");
const readJSON = async (path) => JSON.parse(await readText(path));

test("dedicated Node drift workflow is launch-derived, single-purpose, and GET-only", async () => {
  const [workflowText, scriptText, deployment] = await Promise.all([
    readText(".github/workflows/production-node-drift-diagnostic.yml"),
    readText("tools/production-node-drift-diagnostic.ts"),
    readJSON("packages/contracts/opl-cloud-deployment-contract.json")
  ]);
  const workflow = parse(workflowText);
  const inputs = workflow.on?.workflow_dispatch?.inputs;
  assert.deepEqual(Object.keys(inputs).sort(), ["confirmation", "launch_operation_id", "merged_sha"]);
  for (const forbiddenInput of ["account_id", "workspace_id", "compute_allocation_id", "machine_name", "node_pool_id", "node_name", "resource_version"]) {
    assert.equal(inputs[forbiddenInput], undefined);
  }
  assert.deepEqual(workflow.permissions, { actions: "read", contents: "read" });
  const job = workflow.jobs?.diagnose;
  assert.ok(job);
  assert.deepEqual(job["runs-on"], ["self-hosted", "tencent-cloud", "opl-cloud", "tke-vpc"]);
  assert.equal(job.environment, "production");
  assert.equal(workflow.concurrency?.["cancel-in-progress"], false);
  assert.equal(job.env.DATABASE_URL, "${{ secrets.DATABASE_URL }}");
  assert.equal(job.env.OPL_SUB2API_BASE_URL, undefined);
  assert.equal(job.env.OPL_SUB2API_ADMIN_EMAIL, undefined);
  assert.equal(job.env.OPL_SUB2API_ADMIN_PASSWORD, undefined);
  assert.equal(job.env.OPL_NODE_DRIFT_RAW_DIR, undefined);
  assert.equal(job.env.OPL_NODE_DRIFT_ARTIFACT_DIR, undefined);
  assert.equal(job.env.OPL_NODE_DRIFT_EXPECTED_CUSTOMER_EMAIL, "${{ secrets.OPL_PRODUCTION_BASIC_ACCEPTANCE_B_CUSTOMER_EMAIL }}");
  assert.equal(job.env.OPL_NODE_DRIFT_CUSTOMER_PASSWORD, undefined);
  assert.doesNotMatch(workflowText, /OPL_BASIC_CANARY_CUSTOMER_PASSWORD/);
  const transportStep = job.steps.find((step) => step.name === "Prepare GET-only transports and runner temporary evidence boundary");
  assert.ok(transportStep?.run);
  assert.equal(transportStep.env.OPL_NODE_DRIFT_RAW_DIR, "${{ runner.temp }}/production-node-drift-diagnostic-raw");
  assert.equal(transportStep.env.OPL_NODE_DRIFT_ARTIFACT_DIR, "${{ runner.temp }}/production-node-drift-diagnostic");
  assert.match(transportStep.run, /--identity-out/);
  assert.match(transportStep.run, /control-plane-identity\.json/);
  assert.ok(transportStep.run.indexOf("--identity-out") < transportStep.run.indexOf("kubectl"));
  assert.match(workflowText, /github\.ref == 'refs\/heads\/main'/);
  assert.match(workflowText, /git ls-remote --heads origin/);
  assert.match(workflowText, /production-node-drift-diagnostic\.ts/);
  assert.ok(workflowText.includes(deployment.immutableGithubDependencies["actions/upload-artifact"].ref));
  assert.match(workflowText, /steps\.artifact_gate\.outcome == 'success'/);
  assert.match(workflowText, /runner\.temp/);
  assert.doesNotMatch(workflowText, /(?:kubectl|oc)\s+(?:patch|apply|delete|create|replace|edit|label|annotate|taint|scale|rollout|set)\b/i);
  assert.doesNotMatch(workflowText, /\/fabric\/(?:compute-claim-recovery|workspace-launch-stage-readback)/);
  assert.doesNotMatch(workflowText, /recovery-plan\/(?:diagnose|validate|execute)/);
  assert.doesNotMatch(workflowText, /(?:node_name|machine_name|node_pool_id|account_id|workspace_id):\s*\$\{\{\s*inputs\./i);

  assert.match(scriptText, /READ_ONLY_KUBECTL = new Set\(\["api-resources", "get"\]\)/);
  assert.match(scriptText, /READ_ONLY_TENCENT_ACTIONS = new Set\(\["DescribeLogSwitches", "SearchLog"\]\)/);
  assert.match(scriptText, /default_transaction_read_only=on/);
  assert.match(scriptText, /control_plane_runtime_operations/);
  assert.match(scriptText, /control_plane_accounts/);
  assert.match(scriptText, /control_plane_users/);
  assert.match(scriptText, /recoveryExecution'->>'startedAt/);
  assert.match(scriptText, /identity\.launch\.executeStartedAt/);
  assert.match(scriptText, /ownerUserId/);
  assert.match(scriptText, /APPROVED_CUSTOMER_EMAIL_DIGEST/);
  assert.match(scriptText, /JSON\.parse\(String\(result\?\.LogJson \|\| ""\)\)/);
  assert.doesNotMatch(scriptText, /result\?\.(?:Content|content)/);
  assert.doesNotMatch(scriptText, /absent_or_unavailable/);
  assert.match(scriptText, /FABRIC_GET_PATH/);
  assert.doesNotMatch(scriptText, /\/api\/operator\//);
  assert.doesNotMatch(scriptText, /\/api\/operator\/workspaces/);
  assert.doesNotMatch(scriptText, /\/api\/v1\/auth\/login/);
  assert.doesNotMatch(scriptText, /\/api\/v1\/admin\/users\//);
  assert.match(scriptText, /control_plane\.persisted_sub2api_binding/);
  assert.match(scriptText, /chargeConfirmation'->>'userId/);
  assert.match(scriptText, /account\.sub2api_user_id/);
  assert.match(scriptText, /ownerUserDigest/);
  assert.match(scriptText, /auditRequestOwnedFields/);
  assert.match(scriptText, /auditRequestOwnershipState/);
  assert.match(scriptText, /writer_owned_fields_unavailable/);
  assert.match(scriptText, /current_uid_create_writer_unavailable/);
  assert.match(scriptText, /Number\.isFinite\(Date\.parse\(entry\.time\)\)/);
  assert.doesNotMatch(scriptText, /entry\.time > targetWrite\.time/);
  assert.match(scriptText, /return text \? sha256\(text\) : "unavailable"/);
  assert.match(scriptText, /\\bBearer\\s\+/);
  assert.match(scriptText, /currentNodeUID/);
  assert.match(scriptText, /DIAGNOSTIC_INCONCLUSIVE/);
  assert.match(scriptText, /mutationOutcome: \{ attempted: 0, accepted: 0, confirmed: 0, unknown: 0 \}/);
  assert.doesNotMatch(scriptText, /\/fabric\/(?:compute-claim-recovery\/claim|workspace-launch-stage-readback\/converge)/);

  const contract = deployment.nodeDriftGetOnlyDiagnostic;
  assert.equal(contract.file, ".github/workflows/production-node-drift-diagnostic.yml");
  assert.deepEqual(contract.inputs, ["merged_sha", "launch_operation_id", "confirmation"]);
  assert.equal(contract.resourceIdentitySource, "control_plane_and_fabric_authoritative_get_from_original_launch_only");
  assert.equal(contract.originalLaunchAuthority, "control_plane_runtime_operations_exact_one_read_only");
  assert.equal(contract.originalLaunchRequiredIdentity, "account_id_owner_user_id_workspace_id_compute_allocation_id");
  assert.equal(contract.customerIdentityGate, "approved_normalized_email_sha256_plus_original_launch_account_owner_plus_control_plane_account_user_email_plus_persisted_sub2api_user_id_binding");
  assert.equal(contract.sub2apiIdentityAuthority, "control_plane.persisted_sub2api_binding");
  assert.equal(contract.sub2apiRealtimeRead, false);
  assert.ok(contract.readAuthorities.includes("control_plane_persisted_sub2api_binding"));
  assert.ok(!contract.readAuthorities.some((authority) => authority.startsWith("sub2api_")));
  assert.equal(contract.identityGateOrdering, "before_fabric_tencent_and_target_kubernetes_get");
  assert.equal(contract.postgresSession, "default_transaction_read_only_on");
  assert.equal(contract.tkeAuditResultContract, "cls_search_log_v20201016_results_log_info_log_json");
  assert.equal(contract.auditWindow, "original_launch_persisted_recovery_execution_started_at_plus_or_minus_15_minutes");
  assert.deepEqual(contract.mutationCounts, { sub2api: 0, tencent: 0, kubernetes: 0 });
  assert.equal(contract.rawEvidence, "runner_temp_0600_never_uploaded");
  assert.equal(contract.artifact, "allowlisted_redacted_root_cause_packet_only");
  assert.deepEqual(contract.forbidden, ["successor", "validate", "execute", "fabric_claim", "provider_mutation", "storage_mutation"]);
});

test("original Launch owner remains the identity authority", () => {
  assert.equal(typeof diagnostic.assertOriginalLaunchOwner, "function");
  assert.throws(() => diagnostic.assertOriginalLaunchOwner({}, {}), /node_drift_original_launch_owner_mismatch/);
});

test("approved customer identity is enforced by normalized email digest", () => {
  assert.equal(typeof diagnostic.assertApprovedCustomerEmailDigests, "function");
  const approved = "sha256:d241839999cab1dbb0fc96c4dda28f9433ccfa68e12246e1b2ed0726d19ec376";
  assert.doesNotThrow(() => diagnostic.assertApprovedCustomerEmailDigests([approved, approved]));
  assert.throws(() => diagnostic.assertApprovedCustomerEmailDigests([]), /node_drift_approved_customer_identity_mismatch/);
});
