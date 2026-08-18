import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import YAML from "yaml";

test("portable distribution is product-owned and instance-neutral", async () => {
  const contract = JSON.parse(await readFile("packages/contracts/opl-cloud-distribution-contract.json", "utf8"));
  assert.equal(contract.productRepository, "gaofeng21cn/one-person-lab-cloud");
  assert.equal(contract.instanceHandoff.repository, "gaofeng21cn/opl-instance-medopl");
  assert.deepEqual(contract.distribution.platforms, ["linux/amd64", "linux/arm64"]);
  assert.deepEqual(contract.distribution.releaseAssets, [
    "compose.yaml",
    "compose.deployment-platform-owned.yaml",
    "compose.deployment-managed-tke.yaml",
    "compose.deployment-customer-owned.yaml",
    "compose.fabric-local-docker.yaml",
    "compose.fabric-tencent-tke.yaml",
    "compose.local-workspace.yaml",
    "opl-cloud.env.example",
    "opl-cloud-release.json",
    "SHA256SUMS"
  ]);
  assert.deepEqual(contract.distribution.assetIntegrity, {
    checksumManifest: "SHA256SUMS",
    provenance: "github_oidc_custom_attestation",
    predicateType: "https://github.com/gaofeng21cn/one-person-lab-cloud/attestations/opl-cloud-release/v1",
    signerWorkflow: ".github/workflows/release-opl-cloud-image.yml"
  });
  assert.equal(contract.portableInstallation.providerSelection, "instance_or_installer_owned");
	assert.equal(contract.portableInstallation.runtimeImagePolicy, "all_compose_images_require_registry_tag_and_sha256_digest");
  assert.equal(contract.portableInstallation.composeScope, "cloud_control_services_only");
  assert.deepEqual(contract.portableInstallation.composeDoesNotProve, [
    "workspace_create",
    "workspace_readback",
    "workspace_access",
    "workspace_delete"
  ]);
  const composeSource = await readFile("compose.yaml", "utf8");
  const localWorkspaceComposeSource = await readFile("deploy/portable/compose.local-workspace.yaml", "utf8");
  const dockerfile = await readFile("Dockerfile", "utf8");
  const compose = YAML.parse(composeSource);
  const localWorkspaceCompose = YAML.parse(localWorkspaceComposeSource);
  assert.deepEqual(Object.keys(compose.services).sort(), ["control-plane", "fabric", "ledger", "postgres"]);
  const postgresHealthcheck = compose.services.postgres.healthcheck.test as string[];
  assert.equal(postgresHealthcheck[0], "CMD-SHELL");
  const postgresReadiness = postgresHealthcheck[1];
  const postgresProbes = ["control_plane", "fabric", "ledger"].map((owner) =>
    `PGPASSWORD="$$OPL_${owner.toUpperCase()}_DATABASE_PASSWORD" psql -h 127.0.0.1 -U opl_${owner} -d opl_${owner} -Atqc 'select 1' >/dev/null`
  );
  assert.equal(postgresReadiness, postgresProbes.join(" && "));
  for (const [name, service] of Object.entries(compose.services) as Array<[string, { image?: string }]>) {
    const image = service.image || compose["x-opl-cloud-common"]?.image || "";
    assert.ok(
      /@sha256:[0-9a-f]{64}$/.test(image) || image === "${OPL_CLOUD_IMAGE:?Set OPL_CLOUD_IMAGE to an immutable GHCR digest}",
      `${name} image must require a digest-pinned value`
    );
  }
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
    "OPL_LEDGER_SERVICE_TOKEN",
    "OPL_FABRIC_RUNNER_SERVICE_TOKEN",
    "OPL_FABRIC_CAPABILITY_KEY"
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
  assert.equal(
    compose.services.fabric.environment.OPL_FABRIC_RUNNER_SERVICE_TOKEN,
    "${OPL_FABRIC_RUNNER_SERVICE_TOKEN:?Set OPL_FABRIC_RUNNER_SERVICE_TOKEN}"
  );
  assert.equal(
    compose.services.fabric.environment.OPL_FABRIC_CAPABILITY_KEY,
    "${OPL_FABRIC_CAPABILITY_KEY:?Set OPL_FABRIC_CAPABILITY_KEY}"
  );
  assert.equal(controlPlaneEnvironment.OPL_FABRIC_CAPABILITY_KEY, compose.services.fabric.environment.OPL_FABRIC_CAPABILITY_KEY);
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
  for (const name of ["FABRIC_RUNNER_SERVICE_TOKEN", "FABRIC_CAPABILITY_KEY"]) {
    const match = portableEnvironment.match(new RegExp(`^OPL_${name}=(.+)$`, "m"));
    assert.ok(match);
    assert.match(match[1], /^<replace-with-independent-fabric-(runner|capability)-32-plus-random-chars>$/);
    exampleTokens.push(match[1]);
  }
  assert.equal(new Set(exampleTokens).size, 5);
  assert.match(
    portableEnvironment,
    /^OPL_WORKSPACE_IMAGE=ghcr\.io\/gaofeng21cn\/one-person-lab-webui@sha256:caff36778d8e39ca23682445d8734d6c335ed01e337e9e86dbba9e56657db501$/m
  );
  assert.match(portableEnvironment, /current stable Workspace release is linux\/amd64 only/);
  assert.match(portableEnvironment, /^OPL_DOCKER_SOCKET_PATH=\/var\/run\/docker\.sock$/m);
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
  assert.deepEqual(Object.keys(localWorkspaceCompose.services).sort(), ["control-plane", "postgres"]);
  assert.equal(
    localWorkspaceCompose.services["control-plane"].environment.OPL_WORKSPACE_IMAGE,
    "${OPL_WORKSPACE_IMAGE:?Set OPL_WORKSPACE_IMAGE to an immutable Workspace image digest}"
  );
  assert.equal(localWorkspaceCompose.services["control-plane"].environment.OPL_WORKSPACE_LAUNCH_WORKER_ENABLED, "1");
  assert.match(dockerfile, /^FROM docker:27\.5\.1-cli@sha256:[a-f0-9]{64} AS docker-cli$/m);
  assert.match(dockerfile, /^COPY --from=docker-cli \/usr\/local\/bin\/docker \/usr\/local\/bin\/docker$/m);
  assert.match(dockerfile, /apt-get install[^\n]*ca-certificates curl/);
  assert.doesNotMatch(dockerfile, /apt-get purge[^\n]*curl/);
});

test("Cloud release publishes GHCR and GitHub Release without production deployment", async () => {
  const source = await readFile(".github/workflows/release-opl-cloud-image.yml", "utf8");
  const workflow = YAML.parse(source);
  const build = workflow.jobs.build;
  const publish = workflow.jobs.publish;
  const ownerOnlyRelease = "${{ github.ref == 'refs/heads/main' && github.actor == github.repository_owner && github.triggering_actor == github.repository_owner }}";
  assert.deepEqual(Object.keys(workflow.on), ["workflow_dispatch"]);
  assert.deepEqual(Object.keys(workflow.jobs).sort(), ["build", "publish"]);
  assert.equal(build.if, ownerOnlyRelease);
  assert.equal(publish.if, ownerOnlyRelease);
  assert.deepEqual(build.permissions, { contents: "read" });
  assert.equal(build.environment, undefined);
  assert.equal(build.env.GH_TOKEN, undefined);
  assert.equal(build.steps.find((step: { name?: string }) => step.name === "Checkout exact product source")?.with?.["persist-credentials"], false);
  assert.deepEqual(publish.permissions, {
    actions: "read",
    "artifact-metadata": "write",
    attestations: "write",
    contents: "write",
    "id-token": "write",
    packages: "write"
  });
  assert.equal(publish.environment, "cloud-release");
  assert.equal(publish.needs, "build");

  const buildSteps = build.steps as Array<{ id?: string; name?: string; run?: string; uses?: string; with?: Record<string, unknown> }>;
  const publishSteps = publish.steps as Array<{ id?: string; name?: string; run?: string; uses?: string; with?: Record<string, unknown> }>;
  const stepRun = (steps: typeof buildSteps, name: string) => {
    const run = steps.find((step) => step.name === name)?.run;
    assert.ok(run, `missing run script for ${name}`);
    return run;
  };
  const validationRun = stepRun(buildSteps, "Validate portable product boundary");
  const imageBuildRun = stepRun(buildSteps, "Build multi-architecture image artifact");
  const manifestRun = stepRun(buildSteps, "Create release manifest");
  const artifactVerificationRun = stepRun(publishSteps, "Verify immutable build artifact");
  const imagePublishRun = stepRun(publishSteps, "Publish multi-architecture image");
  const publishRun = stepRun(publishSteps, "Publish GitHub Release");
  const readbackRun = stepRun(publishSteps, "Read back release");

  const checkoutAction = "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1";
  const uploadAction = "actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02";
  assert.equal(buildSteps.find((step) => step.name === "Checkout exact product source")?.uses, checkoutAction);
  const upload = buildSteps.find((step) => step.id === "release_artifact");
  assert.equal(upload?.uses, uploadAction);
  assert.equal(upload?.with?.["if-no-files-found"], "error");
  assert.equal(build.outputs.artifact_id, "${{ steps.release_artifact.outputs.artifact-id }}");
  assert.equal(build.outputs.artifact_digest, "${{ steps.release_artifact.outputs.artifact-digest }}");
  assert.equal(build.outputs.image_digest, "${{ steps.image.outputs.digest }}");

  const buildCommands = buildSteps.map((step) => step.run ?? "").join("\n");
  const publishCommands = publishSteps.map((step) => step.run ?? "").join("\n");
  assert.doesNotMatch(buildCommands, /docker login|--push|gh release create|docker buildx imagetools create/);
  assert.match(imageBuildRun, /--output "type=oci,dest=artifacts\/image,tar=false,name=\$IMAGE_REPOSITORY:\$RELEASE_TAG"/);
  assert.match(imageBuildRun, /oci-layout:\/\/\$PWD\/artifacts\/image:\$RELEASE_TAG/);
  const attestationAction = "actions/attest@508db95dd578ae2727ebd6217d5ba78e4fbda05d";
  const attestation = publishSteps.find((step) => step.name === "Attest release assets");
  assert.equal(attestation?.uses, attestationAction);
  assert.match(String(attestation?.with?.["subject-path"]), /artifacts\/release\/SHA256SUMS/);
  assert.equal(attestation?.with?.["predicate-type"], "https://github.com/gaofeng21cn/one-person-lab-cloud/attestations/opl-cloud-release/v1");
  assert.equal(attestation?.with?.["predicate-path"], "artifacts/release-provenance.json");
  assert.doesNotMatch(publishCommands, /npm (ci|run)|docker buildx build|\.\/tools\//);
  assert.match(stepRun(publishSteps, "Log in to GHCR"), /docker buildx version/);
  assert.match(artifactVerificationRun, /actions\/artifacts\/\$ARTIFACT_ID/);
  assert.match(artifactVerificationRun, /\.workflow_run\.id == \(\$run_id \| tonumber\)/);
  assert.match(artifactVerificationRun, /\^\[0-9a-f\]\{64\}\$/);
  assert.match(artifactVerificationRun, /\.digest == \$digest or \.digest == \("sha256:" \+ \$digest\)/);
  assert.match(artifactVerificationRun, /sha256sum --check --status/);
  assert.match(manifestRun, /sha256sum --check --strict SHA256SUMS/);
  assert.match(artifactVerificationRun, /sha256sum --check --strict SHA256SUMS/);
  assert.match(artifactVerificationRun, /workflowSha/);
  assert.match(artifactVerificationRun, /productSha/);
  assert.match(artifactVerificationRun, /checksumManifestSha256/);
  assert.match(imagePublishRun, /docker buildx imagetools create/);
  assert.match(imagePublishRun, /oci-layout:\/\/\$PWD\/artifacts\/image:\$RELEASE_TAG/);
  assert.match(imagePublishRun, /published_digest.*expected_digest/);

  assert.match(source, /ghcr\.io\/\$\{\{ github\.repository \}\}/);
  assert.match(source, /--platform linux\/amd64,linux\/arm64/);
  assert.match(source, /docker buildx imagetools inspect "\$IMAGE_REPOSITORY:\$RELEASE_TAG"/);
  assert.match(source, /\^v0\\\.\[0-9\]\+\\\.\[0-9\]\+\$/);
  assert.doesNotMatch(source, /environment:\s*production|tencentyun\.com|:latest|:stable/);
  assert.doesNotMatch(source, /--tag "\$IMAGE_REPOSITORY:sha-/);

  assert.match(validationRun, /compose\.deployment-customer-owned\.yaml/);
  assert.match(validationRun, /compose\.fabric-local-docker\.yaml/);

  const manifestAssets = manifestRun.match(/assets:(\[[^\]]+\])/);
  assert.ok(manifestAssets);
  assert.deepEqual(JSON.parse(manifestAssets[1]), [
    "compose.yaml",
    "compose.deployment-platform-owned.yaml",
    "compose.deployment-managed-tke.yaml",
    "compose.deployment-customer-owned.yaml",
    "compose.fabric-local-docker.yaml",
    "compose.fabric-tencent-tke.yaml",
    "compose.local-workspace.yaml",
    "opl-cloud.env.example",
    "SHA256SUMS"
  ]);

  assert.match(publishRun, /^gh release create "\$RELEASE_TAG"/m);
  const publishedAssetPaths = publishRun
    .split("\n")
    .map((line) => line.trim().replace(/ \\$/, ""))
    .filter((line) => /^artifacts\/release\//.test(line));
  assert.deepEqual(publishedAssetPaths, [
    "artifacts/release/compose.yaml",
    "artifacts/release/compose.deployment-platform-owned.yaml",
    "artifacts/release/compose.deployment-managed-tke.yaml",
    "artifacts/release/compose.deployment-customer-owned.yaml",
    "artifacts/release/compose.fabric-local-docker.yaml",
    "artifacts/release/compose.fabric-tencent-tke.yaml",
    "artifacts/release/compose.local-workspace.yaml",
    "artifacts/release/opl-cloud.env.example",
    "artifacts/release/opl-cloud-release.json",
    "artifacts/release/SHA256SUMS"
  ]);

  const readbackAssets = readbackRun.match(/== (\(\[[^\]]+\] \| sort\))/);
  assert.ok(readbackAssets);
  assert.deepEqual(JSON.parse(readbackAssets[1].slice(1, -8)), [
    "SHA256SUMS",
    "compose.deployment-customer-owned.yaml",
    "compose.deployment-managed-tke.yaml",
    "compose.deployment-platform-owned.yaml",
    "compose.fabric-local-docker.yaml",
    "compose.fabric-tencent-tke.yaml",
    "compose.local-workspace.yaml",
    "compose.yaml",
    "opl-cloud-release.json",
    "opl-cloud.env.example"
  ]);
  assert.match(readbackRun, /gh release download/);
  assert.match(readbackRun, /sha256sum --check --strict SHA256SUMS/);
  assert.match(readbackRun, /gh attestation verify/);
  assert.match(readbackRun, /--signer-workflow/);
  assert.match(readbackRun, /--source-digest/);
  assert.match(readbackRun, /--source-ref/);
  assert.match(readbackRun, /--predicate-type/);
  assert.match(readbackRun, /verificationResult\.statement\.predicate\.productSha/);
  assert.match(readbackRun, /verificationResult\.statement\.predicate\.checksumManifestSha256/);
  assert.match(readbackRun, /--deny-self-hosted-runners/);
});
