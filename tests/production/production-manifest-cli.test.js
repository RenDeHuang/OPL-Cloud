import assert from "node:assert/strict";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { runProductionManifestCli } from "../../tools/validate-production-manifest.js";

test("production manifest CLI validates the example manifest", async () => {
  let stdout = "";
  let stderr = "";

  const code = await runProductionManifestCli({
    argv: ["--manifest", "deploy/production-manifest.example.json"],
    stdout: { write: (chunk) => { stdout += chunk; } },
    stderr: { write: (chunk) => { stderr += chunk; } }
  });

  const report = JSON.parse(stdout);
  assert.equal(code, 0);
  assert.equal(report.ok, true);
  assert.equal(stderr, "");
});

test("production manifest CLI fails without leaking inline secret values", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-cloud-manifest-"));
  const manifestPath = join(root, "manifest.json");
  try {
    await writeFile(manifestPath, JSON.stringify({
      env: {
        OPL_RUNTIME_PROVIDER: { value: "unsupported-runtime" },
        DATABASE_URL: { value: "postgres://opl:secret@db.example.com:5432/opl_cloud" },
        TENCENTCLOUD_SECRET_KEY: { value: "tencent_secret" }
      }
    }));
    let stdout = "";
    let stderr = "";

    const code = await runProductionManifestCli({
      argv: ["--manifest", manifestPath],
      stdout: { write: (chunk) => { stdout += chunk; } },
      stderr: { write: (chunk) => { stderr += chunk; } }
    });

    assert.equal(code, 1);
    assert.equal(stderr, "production_manifest_invalid\n");
    assert.equal(stdout.includes("postgres://"), false);
    assert.equal(stdout.includes("tencent_secret"), false);
    assert.equal(JSON.parse(stdout).inlineSecretEnv.includes("DATABASE_URL"), true);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
