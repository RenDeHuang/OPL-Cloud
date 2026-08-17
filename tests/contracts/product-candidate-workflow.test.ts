import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { readFile } from "node:fs/promises";
import { promisify } from "node:util";
import test from "node:test";
import YAML from "yaml";

const workflowPath = ".github/workflows/build-opl-cloud-candidate.yml";
const execFileAsync = promisify(execFile);

test("Cloud candidate workflow builds one non-Release linux/amd64 candidate", async () => {
  const source = await readFile(workflowPath, "utf8");
  const workflow = YAML.parse(source);
  const ownerOnly = "${{ github.ref == 'refs/heads/main' && github.actor == github.repository_owner && github.triggering_actor == github.repository_owner }}";

  assert.deepEqual(Object.keys(workflow.on), ["workflow_dispatch"]);
  assert.deepEqual(Object.keys(workflow.on.workflow_dispatch.inputs).sort(), ["product_sha", "workspace_image"]);
  assert.equal(workflow.on.workflow_dispatch.inputs.product_sha.required, true);
  assert.equal(workflow.on.workflow_dispatch.inputs.workspace_image.required, true);
  assert.deepEqual(Object.keys(workflow.jobs), ["candidate"]);

  const job = workflow.jobs.candidate;
  assert.equal(job.if, ownerOnly);
  assert.equal(job.environment, undefined);
  assert.deepEqual(job.permissions, { contents: "read", packages: "write" });
  assert.equal(job.env.PRODUCT_SHA, "${{ inputs.product_sha }}");
  assert.equal(job.env.WORKSPACE_IMAGE, "${{ inputs.workspace_image }}");
  assert.equal(job.env.IMAGE_REPOSITORY, "ghcr.io/${{ github.repository }}");

  const steps = job.steps as Array<{ name?: string; id?: string; uses?: string; run?: string; with?: Record<string, unknown> }>;
  const step = (name: string) => {
    const value = steps.find((candidate) => candidate.name === name);
    assert.ok(value, `missing ${name}`);
    return value;
  };
  assert.equal(step("Checkout exact product source").uses, "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1");
  assert.equal(step("Checkout exact product source").with?.ref, "${{ inputs.product_sha }}");
  assert.equal(step("Checkout exact product source").with?.["fetch-depth"], 0);
  assert.equal(step("Checkout exact product source").with?.["persist-credentials"], false);
  assert.equal(step("Set up Docker Buildx").uses, "docker/setup-buildx-action@bb05f3f5519dd87d3ba754cc423b652a5edd6d2c");

  const verify = step("Verify candidate identity").run || "";
  assert.match(verify, /git merge-base --is-ancestor/);
  assert.match(verify, /git rev-parse "\$PRODUCT_SHA\^\{tree\}"/);
  assert.match(verify, /workspace_image must be an immutable repository@sha256 reference/);
  const commands = steps.map((value) => value.run || "").join("\n");
  const build = step("Build and publish linux amd64 candidate").run || "";
  assert.match(build, /docker buildx build/);
  assert.match(build, /--platform linux\/amd64/);
  assert.match(build, /--push/);
  assert.match(build, /org\.opencontainers\.image\.revision=\$PRODUCT_SHA/);
  assert.match(build, /candidate-\$PRODUCT_SHA-\$GITHUB_RUN_ID-\$GITHUB_RUN_ATTEMPT/);
  assert.equal((build.match(/docker buildx build/g) || []).length, 1);

  const readback = step("Read back candidate digest platform and revision").run || "";
  assert.match(readback, /docker buildx imagetools inspect/);
  assert.match(readback, /org\.opencontainers\.image\.revision/);
  assert.match(readback, /linux/);
  assert.match(readback, /amd64/);

  const receipt = step("Write and validate neutral candidate receipt").run || "";
  assert.match(receipt, /tools\/cloud-candidate-receipt\.ts validate/);
  assert.match(receipt, /tools\/cloud-candidate-receipt\.ts digest/);
  assert.match(receipt, /candidate\.json/);
  const upload = steps.find((value) => value.id === "candidate_artifact");
  assert.equal(upload?.uses, "actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02");
  assert.equal(upload?.with?.name, "opl-cloud-candidate-${{ inputs.product_sha }}");
  assert.equal(upload?.with?.["if-no-files-found"], "error");

  assert.doesNotMatch(commands, /gh release|git tag|refs\/tags|environment:\s*production|kubectl|tencentyun\.com|medopl\.cn/);
  assert.doesNotMatch(source, /releaseTag|linux\/arm64|workflow_call/);
});

test("product boundary admits the candidate workflow without transferring Instance authority", async () => {
  const { stdout, stderr } = await execFileAsync(process.execPath, ["tools/validate-product-boundary.mjs"]);
  assert.match(stdout, /OPL Cloud product distribution boundary is valid/);
  assert.equal(stderr, "");
});
