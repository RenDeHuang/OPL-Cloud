import assert from "node:assert/strict";
import { readFile, readdir } from "node:fs/promises";
import test from "node:test";

import { controlPlaneApiPath } from "../../apps/console-ui/src/api/console-api.ts";

const root = new URL("../../", import.meta.url);

async function text(path) {
  return readFile(new URL(path, root), "utf8");
}

async function filesUnder(directory, include) {
  const files = [];
  for (const entry of await readdir(new URL(`${directory}/`, root), { withFileTypes: true })) {
    const path = `${directory}/${entry.name}`;
    if (entry.isDirectory()) files.push(...await filesUnder(path, include));
    else if (entry.isFile() && include(path)) files.push(path);
  }
  return files;
}

function serviceReferences(source) {
  return [...source.matchAll(/opl-cloud\/services\/[A-Za-z0-9_./-]+/g)].map((match) => match[0]);
}

function goImports(source) {
  const imports = [...source.matchAll(/^\s*import\s+(?:[._\w]+\s+)?["`]([^"`]+)["`]/gm)].map((match) => match[1]);
  for (const block of source.matchAll(/\bimport\s*\(([\s\S]*?)\)/g)) {
    imports.push(...[...block[1].matchAll(/^\s*(?:[._\w]+\s+)?["`]([^"`]+)["`]/gm)].map((match) => match[1]));
  }
  return imports;
}

test("Go services remain physically isolated behind typed HTTP contracts", async () => {
  const contract = JSON.parse(await text("packages/contracts/opl-cloud-service-boundary-contract.json"));
  const physical = contract.physicalBoundaries;

  assert.equal(physical.crossServiceTransport, "typed_public_http_contracts_only");
  assert.equal(physical.crossServiceSourceImports, "forbidden");
  assert.equal(physical.crossServiceDatabaseAccess, "forbidden");
  assert.equal(physical.sourceGate, "tests/contracts/module-physical-boundaries.test.ts");
  assert.equal(physical.cloudSdkOwner, "services/fabric");
  assert.deepEqual(physical.cloudSdkImportPrefixes, ["github.com/tencentcloud/", "k8s.io/"]);
  assert.equal(physical.ciEntry, "npm test");

  const allowedShared = new Set(physical.allowedSharedGoModules);
  for (const [service, modulePath] of Object.entries(physical.serviceGoModules)) {
    const directory = contract.services[service].path;
    const moduleFile = await text(`${directory}/go.mod`);
    assert.match(moduleFile, new RegExp(`^module ${modulePath.replaceAll("/", "\\/")}$`, "m"), `${directory} module owner`);

    const files = await filesUnder(directory, (path) => path.endsWith(".go") || path.endsWith("go.mod"));
    for (const file of files) {
      const source = await text(file);
      for (const reference of serviceReferences(source)) {
        const owned = reference === modulePath || reference.startsWith(`${modulePath}/`);
        const shared = [...allowedShared].some((allowed) => reference === allowed || reference.startsWith(`${allowed}/`));
        assert.equal(owned || shared, true, `${file} crosses into ${reference}`);
      }
      if (service !== "fabric") {
        for (const prefix of physical.cloudSdkImportPrefixes) {
          assert.equal(source.includes(prefix), false, `${file} references cloud SDK ${prefix} outside Fabric`);
        }
        if (file.endsWith(".go")) {
          for (const imported of goImports(source)) {
            assert.equal(physical.cloudSdkImportPrefixes.some((prefix) => imported.startsWith(prefix)), false, `${file} imports cloud SDK ${imported} outside Fabric`);
          }
        }
      }
    }
  }

  for (const file of await filesUnder("services/internal/postgresmigrate", (path) => path.endsWith(".go") || path.endsWith("go.mod"))) {
    assert.doesNotMatch(await text(file), /opl-cloud\/services\/(?:control-plane|fabric|ledger)/, `${file} must remain domain-neutral`);
  }
});

test("Console UI reaches services only through the same-origin Control Plane API adapter", async () => {
  const contract = JSON.parse(await text("packages/contracts/opl-cloud-service-boundary-contract.json"));
  const physical = contract.physicalBoundaries;
  assert.equal(physical.consoleNetworkOwner, "apps/console-ui/src/api");
  assert.equal(physical.consoleAllowedNetworkPrefix, "/api/");

  assert.equal(controlPlaneApiPath("/api/workspaces?page=1"), "/api/workspaces?page=1");
  for (const invalid of [
    "https://gflabtoken.cn/api/users",
    "/fabric/catalog",
    "/ledger/receipts",
    "/api/../fabric/catalog",
    "/api/%2e%2e/ledger/receipts"
  ]) {
    assert.throws(() => controlPlaneApiPath(invalid), /control_plane_api_path_required/, invalid);
  }

  const files = await filesUnder("apps/console-ui/src", (path) => path.endsWith(".ts") || path.endsWith(".tsx"));
  for (const file of files) {
    const source = await text(file);
    const specifiers = [...source.matchAll(/(?:from\s+|import\s*\()\s*["']([^"']+)["']/g)].map((match) => match[1]);
    for (const specifier of specifiers) {
      assert.doesNotMatch(specifier, /(?:^|\/)services\/(?:control-plane|fabric|ledger|internal)(?:\/|$)/, `${file} runtime deep import`);
      assert.doesNotMatch(specifier, /(?:^|\/)packages\/contracts(?:\/|$)/, `${file} contract deep import`);
    }

    const networkMarkers = ["fetch(", "WebSocket(", "EventSource(", "XMLHttpRequest(", "sendBeacon("];
    if (networkMarkers.some((marker) => source.includes(marker))) {
      assert.equal(file.startsWith(`${physical.consoleNetworkOwner}/`), true, `${file} performs network access outside the API adapter`);
    }
    for (const match of source.matchAll(/\bfetch\(\s*(["'`])([^"'`]+)\1/g)) {
      assert.equal(match[2].startsWith(physical.consoleAllowedNetworkPrefix), true, `${file} calls a non-Control-Plane URL: ${match[2]}`);
    }
  }

  const transport = await text("apps/console-ui/src/api/console-api.ts");
  assert.equal((transport.match(/fetch\(controlPlaneApiPath\(path\)/g) || []).length, 2);
});
