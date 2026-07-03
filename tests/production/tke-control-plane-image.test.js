import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { parse } from "yaml";

const deploymentContractPath = new URL("../../packages/contracts/opl-cloud-deployment-contract.json", import.meta.url);

async function deploymentContract() {
  return JSON.parse(await readFile(deploymentContractPath, "utf8"));
}

async function workflow(path) {
  return parse(await readFile(path, "utf8"));
}

test("OPL Cloud control-plane image build matches the deployment contract", async () => {
  const contract = (await deploymentContract()).controlPlaneImage;
  const dockerfile = await readFile(contract.file, "utf8");
  const dockerignore = (await readFile(".dockerignore", "utf8"))
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean);

  assert.ok(dockerfile.includes(`FROM ${contract.baseImage} AS build`));
  assert.ok(dockerfile.includes(`FROM ${contract.baseImage} AS runtime`));
  for (const instruction of contract.requiredInstructions) {
    assert.ok(dockerfile.includes(instruction), `Dockerfile missing ${instruction}`);
  }
  for (const ignored of contract.dockerignore) {
    assert.ok(dockerignore.includes(ignored), `.dockerignore missing ${ignored}`);
  }
});

test("OPL Cloud image release workflow matches the deployment contract", async () => {
  const contract = await deploymentContract();
  const spec = contract.imageReleaseWorkflow;
  const currentWorkflow = await workflow(spec.file);
  const currentJob = currentWorkflow.jobs[spec.job];
  assert.ok(currentJob, `workflow missing job ${spec.job}`);
  assert.deepEqual([currentJob["runs-on"]].flat(), spec.runner);
  assert.equal(currentJob.environment, contract.environment);

  const inputs = Object.keys(currentWorkflow.on.workflow_dispatch.inputs || {});
  for (const input of spec.inputs) {
    assert.ok(inputs.includes(input), `${spec.file} missing input ${input}`);
  }
  for (const [key, value] of Object.entries(spec.env)) {
    assert.equal(currentJob.env[key], value);
  }

  const steps = new Map((currentJob.steps || []).map((step) => [step.name, step]));
  for (const [stepName, tokens] of Object.entries(spec.requiredCommandsByStep)) {
    const step = steps.get(stepName);
    assert.ok(step, `${spec.file} missing step ${stepName}`);
    const text = `${step.run || ""}\n${JSON.stringify({ ...step, run: undefined })}`;
    for (const token of tokens) {
      assert.ok(text.includes(token), `${stepName} missing ${token}`);
    }
  }
});
