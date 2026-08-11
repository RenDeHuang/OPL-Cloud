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
  const composeSource = await readFile("compose.yaml", "utf8");
  const dockerfile = await readFile("Dockerfile", "utf8");
  const compose = YAML.parse(composeSource);
  assert.deepEqual(Object.keys(compose.services).sort(), ["control-plane", "fabric", "ledger", "postgres"]);
  assert.equal(compose.services.ledger.command[0], "/usr/local/bin/opl-ledger");
  assert.equal(compose.services.fabric.command[0], "/usr/local/bin/opl-fabric");
  const databaseURLs = ["control-plane", "fabric", "ledger"].map(
    (service) => compose.services[service].environment.DATABASE_URL as string
  );
  assert.equal(new Set(databaseURLs).size, 3);
  assert.match(databaseURLs[0], /^postgresql:\/\/opl_control_plane:.*@.*:5432\/opl_control_plane\?sslmode=disable$/);
  assert.match(databaseURLs[1], /^postgresql:\/\/opl_fabric:.*@.*:5432\/opl_fabric\?sslmode=disable$/);
  assert.match(databaseURLs[2], /^postgresql:\/\/opl_ledger:.*@.*:5432\/opl_ledger\?sslmode=disable$/);
  assert.equal(compose["x-opl-cloud-common"].environment.DATABASE_URL, undefined);
  for (const token of [
    "OPL_INTERNAL_SERVICE_TOKEN",
    "OPL_CONTROL_PLANE_SERVICE_TOKEN",
    "OPL_FABRIC_SERVICE_TOKEN",
    "OPL_LEDGER_SERVICE_TOKEN"
  ]) assert.equal(compose["x-opl-cloud-common"].environment[token], undefined);
  const controlPlaneEnvironment = compose.services["control-plane"].environment;
  const serverTokens = [
    controlPlaneEnvironment.OPL_INTERNAL_SERVICE_TOKEN,
    compose.services.fabric.environment.OPL_INTERNAL_SERVICE_TOKEN,
    compose.services.ledger.environment.OPL_INTERNAL_SERVICE_TOKEN
  ];
  assert.deepEqual(serverTokens, [
    "${OPL_CONTROL_PLANE_SERVICE_TOKEN:?Set OPL_CONTROL_PLANE_SERVICE_TOKEN}",
    "${OPL_FABRIC_SERVICE_TOKEN:?Set OPL_FABRIC_SERVICE_TOKEN}",
    "${OPL_LEDGER_SERVICE_TOKEN:?Set OPL_LEDGER_SERVICE_TOKEN}"
  ]);
  assert.equal(new Set(serverTokens).size, 3);
  assert.equal(controlPlaneEnvironment.OPL_FABRIC_SERVICE_TOKEN, serverTokens[1]);
  assert.equal(controlPlaneEnvironment.OPL_LEDGER_SERVICE_TOKEN, serverTokens[2]);
  const portableEnvironment = await readFile("deploy/portable/opl-cloud.env.example", "utf8");
  assert.doesNotMatch(portableEnvironment, /^OPL_INTERNAL_SERVICE_TOKEN=/m);
  const exampleTokens: string[] = [];
  for (const token of ["CONTROL_PLANE", "FABRIC", "LEDGER"]) {
    const match = portableEnvironment.match(new RegExp(`^OPL_${token}_SERVICE_TOKEN=(.+)$`, "m"));
    assert.ok(match);
    assert.match(match[1], new RegExp(`^<replace-with-independent-${token.toLowerCase().replace("_", "-")}-32-plus-random-chars>$`));
    exampleTokens.push(match[1]);
  }
  assert.equal(new Set(exampleTokens).size, 3);
  const postgresInit = compose.configs["opl-postgres-init"].content as string;
  assert.match(postgresInit, /PostgreSQL passwords must contain at least 32 characters/);
  assert.match(postgresInit, /PostgreSQL administrator and service passwords must be distinct/);
  for (const owner of ["control_plane", "fabric", "ledger"]) {
    assert.match(postgresInit, new RegExp(`CREATE ROLE opl_${owner} LOGIN NOSUPERUSER`));
    assert.match(postgresInit, new RegExp(`CREATE DATABASE opl_${owner} OWNER opl_${owner}`));
    assert.match(postgresInit, new RegExp(`REVOKE CONNECT, TEMPORARY ON DATABASE opl_${owner} FROM PUBLIC`));
    assert.match(postgresInit, new RegExp(`GRANT CONNECT, TEMPORARY ON DATABASE opl_${owner} TO opl_${owner}`));
  }
  assert.match(composeSource, /@\$\{OPL_POSTGRES_HOST:-172\.30\.0\.10\}:5432/);
  assert.match(composeSource, /subnet: \$\{OPL_DOCKER_SUBNET:-172\.30\.0\.0\/24\}/);
  assert.doesNotMatch(composeSource, /medopl\.cn|TENCENT_DEPLOY_|tencentyun\.com/);
  assert.doesNotMatch(composeSource, /local-docker|docker\.sock|\/var\/run\/docker\.sock/);
  assert.match(dockerfile, /apt-get install[^\n]*ca-certificates curl/);
  assert.doesNotMatch(dockerfile, /apt-get purge[^\n]*curl/);
});

test("Cloud release publishes GHCR and GitHub Release without production deployment", async () => {
  const source = await readFile(".github/workflows/release-opl-cloud-image.yml", "utf8");
  assert.match(source, /ghcr\.io\/\$\{\{ github\.repository \}\}/);
  assert.match(source, /--platform linux\/amd64,linux\/arm64/);
  assert.match(source, /gh release create/);
  assert.match(source, /opl-cloud-release\.json/);
  assert.match(source, /docker buildx imagetools inspect "\$IMAGE_REPOSITORY:\$RELEASE_TAG"/);
  assert.match(source, /\^v0\\\.\[0-9\]\+\\\.\[0-9\]\+\$/);
  assert.doesNotMatch(source, /environment:\s*production|tencentyun\.com|:latest|:stable/);
  assert.doesNotMatch(source, /--tag "\$IMAGE_REPOSITORY:sha-/);
});
