import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { parse as parseYAML } from "yaml";

import {
  databaseFreeGoTestSpecs,
  goModules,
  localVerificationSteps,
  parseVerifyLocalArgs,
  postgresImage,
  postgresVerificationSpecs,
  runVerification,
  summarizeGoTestFailures
} from "../../tools/verify-local.ts";

test("verify-local exposes one default gate across Node, builds, and every Go module", () => {
  assert.deepEqual(parseVerifyLocalArgs([]), { withPostgres: false });
  assert.deepEqual(parseVerifyLocalArgs(["--with-postgres"]), { withPostgres: true });
  assert.throws(() => parseVerifyLocalArgs(["--production"]), /unknown verify-local argument/);

  const names = localVerificationSteps.map((step) => step.name);
  for (const expected of [
    "product boundary",
    "Node source tests",
    "TypeScript typecheck",
    "TypeScript lint",
    "Console build",
    "Git whitespace"
  ]) {
    assert.ok(names.includes(expected), `missing ${expected}`);
  }
  for (const module of goModules) {
    assert.ok(names.includes(`${module} compile`));
  }
  for (const spec of databaseFreeGoTestSpecs) {
    assert.ok(names.includes(`${spec.cwd} database-free tests`));
  }
});

test("verify-local reports leaf Go test failures separately from package failures", () => {
  const summary = summarizeGoTestFailures([
    { Action: "fail", Package: "opl-cloud/services/fabric/internal/fabric", Test: "TestRuntime/invalid" },
    { Action: "fail", Package: "opl-cloud/services/fabric/internal/fabric", Test: "TestRuntime" },
    { Action: "fail", Package: "opl-cloud/services/fabric/internal/fabric" },
    { Action: "fail", Package: "opl-cloud/services/control-plane/internal/server", Test: "TestLaunch" },
    { Action: "fail", Package: "opl-cloud/services/control-plane/internal/server" }
  ]);
  assert.deepEqual(summary, {
    tests: [
      { package: "opl-cloud/services/control-plane/internal/server", name: "TestLaunch" },
      { package: "opl-cloud/services/fabric/internal/fabric", name: "TestRuntime/invalid" }
    ],
    packages: [
      "opl-cloud/services/control-plane/internal/server",
      "opl-cloud/services/fabric/internal/fabric"
    ]
  });
});

test("full local gate covers every PostgreSQL owner with the CI-only extensions", async () => {
  const compose = parseYAML(await readFile("compose.yaml", "utf8"));
  assert.equal(postgresImage, compose.services.postgres.image);
  assert.match(postgresImage, /^postgres:[^\s@]+@sha256:[0-9a-f]{64}$/);
  assert.notEqual(postgresImage, "postgres:16");
  assert.deepEqual(postgresVerificationSpecs.map((spec) => spec.cwd), [
    "services/internal/postgresmigrate",
    "services/ledger",
    "services/control-plane",
    "services/fabric"
  ]);
  assert.equal(postgresVerificationSpecs[0].race, true);
  assert.equal(postgresVerificationSpecs[2].timeout, "15m");
});

test("full verification adds the temporary PostgreSQL modules after the default checks", async () => {
  const events = [];
  const env = { OPL_POSTGRES_TESTS: "1" };
  const dependencies = {
    runStep: async (step) => { events.push(`step:${step.name}`); },
    withTemporaryPostgres: async (callback) => {
      events.push("postgres:start");
      try {
        await callback(env);
      } finally {
        events.push("postgres:stop");
      }
    },
    runPostgresVerification: async (actualEnv) => {
      assert.equal(actualEnv, env);
      events.push("postgres:tests");
    }
  };

  await runVerification({ withPostgres: true }, dependencies);
  assert.equal(events[0], `step:${localVerificationSteps[0].name}`);
  assert.deepEqual(events.slice(-3), ["postgres:start", "postgres:tests", "postgres:stop"]);

  events.length = 0;
  await runVerification({ withPostgres: false }, dependencies);
  assert.equal(events.some((event) => event.startsWith("postgres:")), false);
});
