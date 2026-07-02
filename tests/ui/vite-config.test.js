import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import config from "../../vite.config.js";

test("Vite build splits React and ProComponents while keeping Ant Design dependencies in one vendor chunk", () => {
  const manualChunks = config.build?.rollupOptions?.output?.manualChunks;

  assert.equal(typeof manualChunks, "function");
  assert.equal(manualChunks("/repo/node_modules/react/index.js"), "react-vendor");
  assert.equal(manualChunks("/repo/node_modules/antd/es/button/index.js"), "vendor");
  assert.equal(manualChunks("/repo/node_modules/antd/es/table/index.js"), "vendor");
  assert.equal(manualChunks("/repo/node_modules/antd/es/form/index.js"), "vendor");
  assert.equal(manualChunks("/repo/node_modules/rc-table/es/Table.js"), "vendor");
  assert.equal(manualChunks("/repo/node_modules/@ant-design/cssinjs/es/index.js"), "vendor");
  assert.equal(manualChunks("/repo/node_modules/@ant-design/pro-components/es/index.js"), "pro-components");
});

test("Vite HTML preloads only the critical React vendor chunk", () => {
  const resolveDependencies = config.build?.modulePreload?.resolveDependencies;

  assert.equal(typeof resolveDependencies, "function");
  assert.deepEqual(resolveDependencies("assets/index.js", [
    "assets/react-vendor.js",
    "assets/antd-basic.js",
    "assets/pro-components.js"
  ], { hostType: "html", hostId: "index.html" }), [
    "assets/react-vendor.js"
  ]);
  assert.deepEqual(resolveDependencies("assets/lazy-users.js", [
    "assets/pro-components.js"
  ], { hostType: "js", hostId: "assets/index.js" }), [
    "assets/pro-components.js"
  ]);
});

test("public entry does not statically import Ant Design or ProComponents", async () => {
  const source = await readFile(new URL("../../src/main.jsx", import.meta.url), "utf8");

  assert.equal(source.includes("from \"antd\""), false);
  assert.equal(source.includes("from \"@ant-design/pro-components\""), false);
  assert.equal(source.includes("from \"lucide-react\""), false);
  assert.match(source, /import\(\s*["']\.\/console-app\.jsx["']\s*\)/);
});
