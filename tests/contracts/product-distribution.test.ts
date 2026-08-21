import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import YAML from "yaml";

test("portable Compose isolates service credentials and databases", async () => {
  const compose = YAML.parse(await readFile("compose.yaml", "utf8"));

  const databaseURLs = ["control-plane", "fabric", "ledger"].map(
    (service) => compose.services[service].environment.DATABASE_URL as string
  );
  assert.equal(new Set(databaseURLs).size, 3);
  assert.match(databaseURLs[0], /\/opl_control_plane\?sslmode=disable$/);
  assert.match(databaseURLs[1], /\/opl_fabric\?sslmode=disable$/);
  assert.match(databaseURLs[2], /\/opl_ledger\?sslmode=disable$/);

  const serviceTokens = [
    compose.services["control-plane"].environment.OPL_INTERNAL_SERVICE_TOKEN,
    compose.services.fabric.environment.OPL_INTERNAL_SERVICE_TOKEN,
    compose.services.ledger.environment.OPL_INTERNAL_SERVICE_TOKEN
  ];
  assert.equal(new Set(serviceTokens).size, 3);
  assert.equal(compose.services["control-plane"].environment.OPL_FABRIC_SERVICE_TOKEN, serviceTokens[1]);
  assert.equal(compose.services["control-plane"].environment.OPL_LEDGER_SERVICE_TOKEN, serviceTokens[2]);

  for (const [name, service] of Object.entries(compose.services) as Array<[string, { image?: string }]>) {
    const image = service.image || compose["x-opl-cloud-common"]?.image || "";
    assert.ok(
      /@sha256:[0-9a-f]{64}$/.test(image) || image === "${OPL_CLOUD_IMAGE:?Set OPL_CLOUD_IMAGE to an immutable GHCR digest}",
      `${name} image must be digest-pinned`
    );
  }
});

test("Cloud Release separates read-only build from protected publication", async () => {
  const workflow = YAML.parse(await readFile(".github/workflows/release-opl-cloud-image.yml", "utf8"));
  const build = workflow.jobs.build;
  const publish = workflow.jobs.publish;
  const publisherGate = "${{ github.ref == 'refs/heads/main' && github.actor == github.triggering_actor && (github.actor == github.repository_owner || github.actor == 'RenDeHuang') }}";

  assert.deepEqual(Object.keys(workflow.on), ["workflow_dispatch"]);
  assert.equal(build.if, publisherGate);
  assert.equal(publish.if, publisherGate);
  assert.deepEqual(build.permissions, { contents: "read" });
  assert.equal(build.environment, undefined);
  assert.equal(publish.environment, "cloud-release");
  assert.equal(publish.needs, "build");
  assert.equal(publish.permissions.contents, "write");
  assert.equal(publish.permissions.packages, "write");
  assert.equal(publish.permissions["id-token"], "write");

  const buildCommands = build.steps.map((step) => step.run || "").join("\n");
  const publishCommands = publish.steps.map((step) => step.run || "").join("\n");
  assert.match(buildCommands, /type=oci,dest=artifacts\/image/);
  assert.match(buildCommands, /sha256sum --check --strict SHA256SUMS/);
  assert.match(publishCommands, /docker buildx imagetools create/);
  assert.match(publishCommands, /published_digest.*expected_digest/);
  assert.match(publishCommands, /gh release create/);
  assert.match(publishCommands, /gh release download/);
  assert.match(publishCommands, /gh attestation verify/);
  assert.match(publishCommands, /sha256sum --check --strict SHA256SUMS/);
});
