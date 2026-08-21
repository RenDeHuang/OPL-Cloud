import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { parse } from "yaml";

const workflowPath = new URL("../../.github/workflows/clean-host-qualification.yml", import.meta.url);

test("clean-host workflow delegates qualification to the reusable local runner", async () => {
  const workflow = parse(await readFile(workflowPath, "utf8"));
  const trigger = workflow.on ?? workflow["on"];
  const job = workflow.jobs?.clean_host_qualification;
  const steps = job?.steps as Array<{
    name?: string;
    run?: string;
    uses?: string;
    with?: Record<string, unknown>;
    env?: Record<string, string>;
    if?: string;
  }>;

  assert.equal(job?.["runs-on"], "ubuntu-latest");
  assert.equal(job?.environment, undefined);
  for (const input of ["ref", "cloud_image_ref", "workspace_image_ref"]) {
    assert.equal(trigger?.workflow_dispatch?.inputs?.[input]?.required, true);
  }

  const checkout = steps.find((step) => step.name === "Checkout exact product source");
  assert.equal(checkout?.with?.ref, "${{ inputs.ref }}");
  assert.equal(checkout?.with?.["persist-credentials"], false);
  assert.ok(steps.some((step) => step.name === "Install locked dependencies" && /npm ci/.test(step.run || "")));

  const qualification = steps.find((step) => step.name === "Run local Workspace qualification");
  assert.ok(qualification?.run, "missing reusable qualification invocation");
  assert.match(qualification.run, /npm run qualify:local:workspace --/);
  assert.match(qualification.run, /--source-sha "\$PRODUCT_SHA"/);
  assert.match(qualification.run, /--cloud-image "\$CLOUD_IMAGE_REF"/);
  assert.match(qualification.run, /--workspace-image "\$WORKSPACE_IMAGE_REF"/);
  assert.match(qualification.run, /--authority-mode fixture/);
  assert.match(qualification.run, /--receipt "\$RUNNER_TEMP\/local-workspace-qualification\.json"/);

  const upload = steps.find((step) => step.name === "Upload local qualification receipt");
  assert.match(upload?.uses || "", /^actions\/upload-artifact@[0-9a-f]{40}$/);
  assert.equal(upload?.if, "${{ always() }}");
  assert.equal(upload?.with?.name, "local-workspace-qualification");
  assert.equal(upload?.with?.path, "${{ runner.temp }}/local-workspace-qualification.json");
  assert.equal(upload?.with?.["if-no-files-found"], "error");
});
