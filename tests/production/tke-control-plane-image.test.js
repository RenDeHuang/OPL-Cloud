import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("OPL Cloud control-plane image build includes app build output and kubectl", async () => {
  const dockerfile = await readFile("Dockerfile", "utf8");
  const dockerignore = await readFile(".dockerignore", "utf8");

  assert.match(dockerfile, /FROM node:22/);
  assert.match(dockerfile, /npm ci --no-audit --no-fund --fetch-retries=5 --fetch-retry-mintimeout=20000 --fetch-retry-maxtimeout=120000/);
  assert.match(dockerfile, /npm ci --omit=dev --no-audit --no-fund --fetch-retries=5 --fetch-retry-mintimeout=20000 --fetch-retry-maxtimeout=120000/);
  assert.match(dockerfile, /npm run build/);
  assert.match(dockerfile, /kubectl/);
  assert.match(dockerfile, /EXPOSE 8787/);
  assert.match(dockerfile, /node", "services\/api\/server\.js"/);
  assert.match(dockerignore, /^\.env/m);
  assert.match(dockerignore, /^\.runtime/m);
  assert.match(dockerignore, /^node_modules/m);
});

test("OPL Cloud image release workflow pushes the control plane image to the oplcloud TCR namespace", async () => {
  const workflow = await readFile(".github/workflows/release-opl-cloud-image.yml", "utf8");

  assert.match(workflow, /workflow_dispatch:/);
  assert.match(workflow, /publish_cloud_image:/);
  assert.match(workflow, /mirror_workspace_image:/);
  assert.match(workflow, /workspace_source_image:/);
  assert.match(workflow, /runs-on: ubuntu-latest/);
  assert.match(workflow, /uswccr\.ccs\.tencentyun\.com\/oplcloud\/opl-cloud/);
  assert.match(workflow, /uswccr\.ccs\.tencentyun\.com\/oplcloud\/one-person-lab-app/);
  assert.match(workflow, /docker login uswccr\.ccs\.tencentyun\.com --username "\$TCR_ID" --password-stdin/);
  assert.match(workflow, /if \[ "\$\{\{ inputs\.publish_cloud_image \}\}" = "true" \]; then/);
  assert.match(workflow, /docker buildx build --push -f Dockerfile -t "\$OPL_CLOUD_IMAGE_REF" "\$OPL_CLOUD_IMAGE_CONTEXT"/);
  assert.match(workflow, /if \[ "\$\{\{ inputs\.mirror_workspace_image \}\}" = "true" \]; then/);
  assert.match(workflow, /docker buildx imagetools create -t "\$OPL_WORKSPACE_IMAGE_REF" "\$WORKSPACE_SOURCE_IMAGE"/);
  assert.match(workflow, /TCR_ID: \$\{\{ secrets\.TCR_USERNAME \}\}/);
  assert.match(workflow, /TCR_SECRET: \$\{\{ secrets\.TCR_PASSWORD \}\}/);
});
