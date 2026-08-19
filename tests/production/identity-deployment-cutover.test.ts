import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { productionManifestRequiredEnv } from "../../services/control-plane/ops/production-manifest.ts";

const retiredInputs = [
  "OPL_CONSOLE_USERS_JSON",
  "OPL_BASIC_COMPUTE_NODE_POOL_ID",
  "OPL_PRO_COMPUTE_NODE_POOL_ID",
  "OPL_BASIC_COMPUTE_NODE_POOL_MAX_REPLICAS",
  "OPL_PRO_COMPUTE_NODE_POOL_MAX_REPLICAS"
];

test("Cloud product inputs no longer inject the retired local Console user seed", async () => {
  const paths = [
    ".env.example",
    "services/control-plane/ops/production-manifest.ts",
    "services/fabric/ops/production-readiness.ts"
  ];
  for (const path of paths) {
    const source = await readFile(path, "utf8");
    for (const retiredInput of retiredInputs) {
      assert.doesNotMatch(source, new RegExp(retiredInput), `${path}:${retiredInput}`);
    }
  }
  for (const retiredInput of retiredInputs) {
    assert.equal(productionManifestRequiredEnv().includes(retiredInput), false, retiredInput);
  }
});
