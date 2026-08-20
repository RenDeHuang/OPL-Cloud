import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { copyFile, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import YAML from "yaml";

test("portable Local-Docker assets configure from a standalone download directory", async () => {
  const composeSource = await readFile("compose.yaml", "utf8");
  const overlaySource = await readFile("deploy/portable/compose.local-workspace.yaml", "utf8");
  const fabricSource = await readFile("deploy/portable/compose.fabric-local-docker.yaml", "utf8");
  const deploymentSource = await readFile("deploy/portable/compose.deployment-customer-owned.yaml", "utf8");
  const environmentSource = await readFile("deploy/portable/opl-cloud.env.example", "utf8");
  const qualificationSource = await readFile(".github/workflows/qualification.yml", "utf8");
  const overlay = YAML.parse(overlaySource);
  const fabric = YAML.parse(fabricSource);

  assert.deepEqual(overlay.services.postgres.volumes, [{
    type: "bind",
    source: "${OPL_POSTGRES_DATA_ROOT:?Set the customer-owned PostgreSQL data root}",
    target: "/var/lib/postgresql/data",
    bind: { create_host_path: false }
  }]);

  assert.equal(fabricSource.includes("opl.cloud.fabric-provider: local-docker"), true);
  assert.deepEqual(fabric.services.fabric.cap_add, ["SYS_ADMIN"]);
  assert.equal(deploymentSource.includes("opl.cloud.deployment-mode: customer_owned"), true);
  assert.equal(overlay.services["control-plane"].environment.OPL_WORKSPACE_LAUNCH_WORKER_ENABLED, "1");

  assert.match(environmentSource, /^OPL_SUB2API_BASE_URL=https:\/\/your-sub2api\.example\.com$/m);
  assert.match(environmentSource, /^OPL_SUB2API_ADMIN_EMAIL=<replace-with-admin-email>$/m);
  assert.match(environmentSource, /^OPL_SUB2API_ADMIN_PASSWORD=<replace-with-admin-password>$/m);
  assert.match(
    environmentSource,
    /^OPL_WORKSPACE_IMAGE=<replace-with-workspace-repository@sha256:64-hex-digest>$/m
  );
  assert.match(environmentSource, /repository@sha256 reference selected for this installation/);
  assert.doesNotMatch(environmentSource, /ghcr\.io\/gaofeng21cn\/one-person-lab-webui@sha256:/);
  assert.doesNotMatch(environmentSource, /current stable Workspace release/);
  assert.match(environmentSource, /^OPL_DOCKER_SOCKET_PATH=\/var\/run\/docker\.sock$/m);
  assert.match(environmentSource, /^OPL_POSTGRES_DATA_ROOT=\/absolute\/path\/to\/opl-cloud-data\/postgres$/m);
  assert.match(environmentSource, /^OPL_FABRIC_LOCAL_DOCKER_GATEWAY_CONTAINER=opl-cloud-control-plane$/m);
  assert.match(environmentSource, /^OPL_FABRIC_LOCAL_DOCKER_STORAGE_ROOT=\/absolute\/path\/to\/opl-workspaces$/m);
  assert.match(environmentSource, /dedicated Linux 5\.14\+ ext4\/XFS\n# filesystem with project quota enabled/);
  assert.match(qualificationSource, /go test -tags opl_project_quota -run '\^TestLinuxLocalDockerProjectQuotaEnforcesHardLimit\$'/);
  assert.match(qualificationSource, /sudo env OPL_TEST_PROJECT_QUOTA_ROOT=/);
  assert.match(qualificationSource, /sudo mount -o loop,prjquota/);
  assert.match(qualificationSource, /if: \$\{\{ always\(\) \}\}/);
  assert.match(
    environmentSource,
    /docker compose --env-file \.\/opl-cloud\.env/
  );
  assert.match(environmentSource, /compose\.deployment-customer-owned\.yaml/);
  assert.match(environmentSource, /compose\.fabric-local-docker\.yaml/);
  assert.match(environmentSource, /compose\.local-workspace\.yaml/);
  assert.doesNotMatch(environmentSource, /deploy\/portable/);

  const downloadRoot = await mkdtemp(join(tmpdir(), "opl-cloud-portable-assets-"));
  try {
    await Promise.all([
      copyFile("compose.yaml", join(downloadRoot, "compose.yaml")),
      copyFile("deploy/portable/compose.local-workspace.yaml", join(downloadRoot, "compose.local-workspace.yaml")),
      copyFile("deploy/portable/compose.fabric-local-docker.yaml", join(downloadRoot, "compose.fabric-local-docker.yaml")),
      copyFile("deploy/portable/compose.deployment-customer-owned.yaml", join(downloadRoot, "compose.deployment-customer-owned.yaml")),
      copyFile("deploy/portable/opl-cloud.env.example", join(downloadRoot, "opl-cloud.env"))
    ]);
    const result = spawnSync("docker", [
      "compose",
      "--env-file", "./opl-cloud.env",
      "-f", "./compose.yaml",
      "-f", "./compose.deployment-customer-owned.yaml",
      "-f", "./compose.fabric-local-docker.yaml",
      "-f", "./compose.local-workspace.yaml",
      "config", "--quiet"
    ], { cwd: downloadRoot, encoding: "utf8" });
    assert.equal(result.error, undefined);
    assert.equal(result.status, 0, [result.stdout, result.stderr].filter(Boolean).join("\n"));

    for (const requiredName of [
      "OPL_FABRIC_LOCAL_DOCKER_GATEWAY_CONTAINER",
      "OPL_FABRIC_LOCAL_DOCKER_STORAGE_ROOT",
      "OPL_POSTGRES_DATA_ROOT"
    ]) {
      const missingEnvironment = environmentSource.replace(new RegExp(`^${requiredName}=.*\\n`, "m"), "");
      await writeFile(join(downloadRoot, "opl-cloud.missing.env"), missingEnvironment, { mode: 0o600 });
      const missingResult = spawnSync("docker", [
        "compose",
        "--env-file", "./opl-cloud.missing.env",
        "-f", "./compose.yaml",
        "-f", "./compose.deployment-customer-owned.yaml",
        "-f", "./compose.fabric-local-docker.yaml",
        "-f", "./compose.local-workspace.yaml",
        "config", "--quiet"
      ], { cwd: downloadRoot, encoding: "utf8" });
      assert.notEqual(missingResult.status, 0, `${requiredName} must be required`);
      assert.match(missingResult.stderr, new RegExp(requiredName));
    }
  } finally {
    await rm(downloadRoot, { recursive: true, force: true });
  }

  assert.match(composeSource, /^name: opl-cloud$/m);
});
