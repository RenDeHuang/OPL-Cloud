import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import YAML from "yaml";

const workflowUrl = new URL("../../.github/workflows/verify-production-faults.yml", import.meta.url);

test("production fault workflow is a bounded resource-scoped five-scenario drill", async () => {
  const text = await readFile(workflowUrl, "utf8");
  const workflow = YAML.parse(text);
  const job = workflow.jobs.faults;
  const steps = job.steps;
  const runText = steps.map((step) => step.run || "").join("\n");

  assert.equal(workflow.concurrency.group, "production-resource-verification");
  assert.equal(workflow.concurrency["cancel-in-progress"], false);
  assert.equal(workflow.on.workflow_dispatch.inputs.confirm_resource_faults.required, true);
  assert.deepEqual(job["runs-on"], ["self-hosted", "tencent-cloud", "opl-cloud", "tke-vpc"]);
  assert.ok(job["timeout-minutes"] <= 150);
  assert.equal(job.env.OPL_CONSOLE_ORIGIN, "https://cloud.medopl.cn");
  assert.match(runText, /RUN_RESOURCE_SCOPED_FAULTS/);
  for (const scenario of [
    "lost-response-replay", "workspace-pod-recovery", "storage-detach-reattach",
    "machine-external-delete", "browser-failure-cleanup"
  ]) assert.match(runText, new RegExp(scenario));
  assert.match(runText, /production-fault-verifier\.ts/);
  assert.match(runText, /port-forward service\/opl-cloud-ledger 18081:8081/);
  assert.match(runText, /port-forward service\/opl-cloud-fabric 18082:8082/);
  assert.match(runText, /go build .*opl-tencent-provisioner/);
  assert.ok(steps.some((step) => step.uses === "actions/upload-artifact@v4" && step.if === "always()"));
  assert.ok(steps.some((step) => step.name === "Verify exact final evidence" && step.if === "always()"));
  assert.ok(steps.some((step) => step.name === "Remove temporary credentials and drill files" && step.if === "always()"));

  const prepare = steps.find((step) => step.name === "Prepare kubeconfig and internal credentials").run;
  assert.ok(
    prepare.indexOf('echo "OPL_FAULT_SECRET_DIR=$secret_dir" >> "$GITHUB_ENV"') < prepare.indexOf('kubeconfig="$secret_dir/kubeconfig"'),
    "the secret directory must be exported before any credential write can fail"
  );

  assert.doesNotMatch(text, /one-person-lab-app/);
  assert.doesNotMatch(text, /(database|account|nodepool|node pool|network|shared service)[-_ ]?(fault|delete|failure)/i);
  assert.doesNotMatch(runText, /kubectl[^\n]*delete[^\n]*\s-l\s/);
  assert.doesNotMatch(runText, /(DeleteNodePool|DROP TABLE|TRUNCATE|iptables|tc qdisc)/);
  assert.doesNotMatch(text, /OPL_CONSOLE_ORIGIN:\s*\$\{\{/);
});
