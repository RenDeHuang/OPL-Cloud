import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import YAML from "yaml";

test("portable distribution is product-owned and instance-neutral", async () => {
  const contract = JSON.parse(await readFile("packages/contracts/opl-cloud-distribution-contract.json", "utf8"));
  assert.equal(contract.productRepository, "gaofeng21cn/one-person-lab-cloud");
  assert.equal(contract.instanceHandoff.repository, "gaofeng21cn/opl-instance-medopl");
  assert.deepEqual(contract.distribution.platforms, ["linux/amd64", "linux/arm64"]);
  assert.equal(contract.portableInstallation.providerSelection, "instance_or_installer_owned");
  assert.equal(contract.portableInstallation.composeScope, "cloud_control_services_only");
  assert.deepEqual(contract.portableInstallation.composeDoesNotProve, [
    "workspace_create",
    "workspace_readback",
    "workspace_access",
    "workspace_delete"
  ]);
  assert.equal(contract.evidenceOwners.currentCapability, "docs/status.md");

  const composeSource = await readFile("compose.yaml", "utf8");
  const compose = YAML.parse(composeSource);
  assert.deepEqual(Object.keys(compose.services).sort(), ["control-plane", "fabric", "ledger", "postgres"]);
  assert.equal(compose.services.ledger.command[0], "/usr/local/bin/opl-ledger");
  assert.equal(compose.services.fabric.command[0], "/usr/local/bin/opl-fabric");
  assert.doesNotMatch(composeSource, /medopl\.cn|TENCENT_DEPLOY_|tencentyun\.com/);
  assert.doesNotMatch(composeSource, /local-docker|docker\.sock|\/var\/run\/docker\.sock/);
});

test("Cloud release publishes GHCR and GitHub Release without production deployment", async () => {
  const source = await readFile(".github/workflows/release-opl-cloud-image.yml", "utf8");
  assert.match(source, /ghcr\.io\/\$\{\{ github\.repository \}\}/);
  assert.match(source, /--platform linux\/amd64,linux\/arm64/);
  assert.match(source, /gh release create/);
  assert.match(source, /opl-cloud-release\.json/);
  assert.doesNotMatch(source, /environment:\s*production|tencentyun\.com|:latest|:stable/);
});

test("current status and roadmap do not claim the local Docker Workspace gap is closed", async () => {
  const [status, roadmap, fabricMain] = await Promise.all([
    readFile("docs/status.md", "utf8"),
    readFile("docs/roadmap.md", "utf8"),
    readFile("services/fabric/cmd/fabric/main.go", "utf8")
  ]);

  assert.match(status, /local-docker.*not yet completed.*launch.*readback.*recovery/is);
  assert.match(status, /Tencent TKE.*medopl instance implementation fact/is);
  assert.match(fabricMain, /NewTencentProvider\(\)/);
  assert.match(roadmap, /MVP-LOCAL-WORKSPACE-GATEWAY-01/);
  assert.match(roadmap, /only `P0` lane/);
  assert.equal(roadmap.match(/\| `[^`]+` \| `(?:next|planned|candidate|later|external_owner|in_review)` \| `P0` \|/g)?.length, 1);
});
