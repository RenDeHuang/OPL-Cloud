import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { parse } from "yaml";

test("release compilation avoids target-platform emulation", async () => {
  const dockerfile = await readFile("Dockerfile", "utf8");
  const computeStages = dockerfile.match(/^FROM .* AS (?:[a-z0-9-]+-build|build)$/gm) ?? [];
  const targetGoBuilds = dockerfile.match(/CGO_ENABLED=0 GOOS=\$TARGETOS GOARCH=\$TARGETARCH go build/g) ?? [];

  assert.equal(computeStages.length, 4);
  for (const stage of computeStages) {
    assert.match(stage, /--platform=\$BUILDPLATFORM/);
  }
  assert.equal(targetGoBuilds.length, 4);
  assert.doesNotMatch(dockerfile, / AS provisioner-build$/m);
});

test("release build reuses BuildKit layers", async () => {
  const source = await readFile(".github/workflows/release-opl-cloud-image.yml", "utf8");
  const workflow = parse(source) as {
    jobs: { build: { steps: Array<{ name?: string; run?: string }> } };
  };
  const imageBuild = workflow.jobs.build.steps.find((step) => step.name === "Build multi-architecture image artifact");

  assert.ok(imageBuild?.run);
  assert.match(imageBuild.run, /--cache-from "type=gha,scope=opl-cloud-release"/);
  assert.match(imageBuild.run, /--cache-to "type=gha,mode=max,scope=opl-cloud-release"/);
});
