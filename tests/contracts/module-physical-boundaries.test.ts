import assert from "node:assert/strict";
import { readFile, readdir } from "node:fs/promises";
import test from "node:test";

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

test("Go services remain physically isolated behind typed HTTP contracts", async () => {
  const contract = JSON.parse(await text("packages/contracts/opl-cloud-service-boundary-contract.json"));
  const physical = contract.physicalBoundaries;

  assert.equal(physical.crossServiceTransport, "typed_public_http_contracts_only");
  assert.equal(physical.crossServiceSourceImports, "forbidden");
  assert.equal(physical.crossServiceDatabaseAccess, "forbidden");

  const allowedShared = new Set(physical.allowedSharedGoModules);
  for (const [service, modulePath] of Object.entries(physical.serviceGoModules)) {
    const directory = contract.services[service].path;
    const moduleFile = await text(`${directory}/go.mod`);
    assert.match(moduleFile, new RegExp(`^module ${modulePath.replaceAll("/", "\\/")}$`, "m"), `${directory} module owner`);

    const files = await filesUnder(directory, (path) => path.endsWith(".go") || path.endsWith("go.mod"));
    for (const file of files) {
      for (const reference of serviceReferences(await text(file))) {
        const owned = reference === modulePath || reference.startsWith(`${modulePath}/`);
        const shared = [...allowedShared].some((allowed) => reference === allowed || reference.startsWith(`${allowed}/`));
        assert.equal(owned || shared, true, `${file} crosses into ${reference}`);
      }
    }
  }
});

test("Console UI does not deep-import runtime services or machine contracts", async () => {
  const files = await filesUnder("apps/console-ui/src", (path) => path.endsWith(".ts") || path.endsWith(".tsx"));
  for (const file of files) {
    const source = await text(file);
    const specifiers = [...source.matchAll(/(?:from\s+|import\s*\()\s*["']([^"']+)["']/g)].map((match) => match[1]);
    for (const specifier of specifiers) {
      assert.doesNotMatch(specifier, /(?:^|\/)services\/(?:control-plane|fabric|ledger|internal)(?:\/|$)/, `${file} runtime deep import`);
      assert.doesNotMatch(specifier, /(?:^|\/)packages\/contracts(?:\/|$)/, `${file} contract deep import`);
    }
  }
});
