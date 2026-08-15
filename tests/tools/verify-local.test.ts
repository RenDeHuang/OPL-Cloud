import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { parse as parseYAML } from "yaml";

import {
  buildProductMatrixReceipt,
  databaseFreeGoTestSpecs,
  goModules,
  localVerificationSteps,
  parseVerifyLocalArgs,
  postgresImage,
  postgresVerificationSpecs,
  productMatrixRequiredPackages,
  productMatrixRequiredTests,
  productMatrixStages,
  runVerification
} from "../../tools/verify-local.ts";

test("verify-local exposes one default gate across Node, builds, and every Go module", () => {
  assert.deepEqual(parseVerifyLocalArgs([]), { withPostgres: false, productMatrixReceipt: "" });
  assert.deepEqual(parseVerifyLocalArgs(["--with-postgres"]), { withPostgres: true, productMatrixReceipt: "" });
  assert.deepEqual(parseVerifyLocalArgs([
    "--with-postgres", "--product-matrix-receipt", "/tmp/product-matrix.json"
  ]), { withPostgres: true, productMatrixReceipt: "/tmp/product-matrix.json" });
  assert.throws(() => parseVerifyLocalArgs(["--product-matrix-receipt", "/tmp/product-matrix.json"]), /requires --with-postgres/);
  assert.throws(() => parseVerifyLocalArgs(["--with-postgres", "--product-matrix-receipt", "matrix.json"]), /absolute path/);
  assert.throws(() => parseVerifyLocalArgs(["--with-postgres", "--product-matrix-receipt"]), /requires one value/);
  assert.throws(() => parseVerifyLocalArgs(["--production"]), /unknown verify-local argument/);

  const names = localVerificationSteps.map((step) => step.name);
  for (const expected of [
    "product boundary",
    "Node tests",
    "TypeScript typecheck",
    "TypeScript lint",
    "Console build",
    "whitepaper build",
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

function completeProductMatrixResults() {
  return postgresVerificationSpecs.map((spec) => ({
    cwd: spec.cwd,
    failed: 0,
    skipped: 0,
    passedPackages: productMatrixRequiredPackages.filter((name) => name.includes(`/${spec.cwd}/`)),
    passedTests: productMatrixRequiredTests.filter((entry) => entry.package.includes(`/${spec.cwd}/`)).map((entry) => ({ ...entry }))
  }));
}

test("Product matrix receipt is derived from exact passed packages and tests", () => {
  const source = { sha: "a".repeat(40), tree: "b".repeat(40), clean: true };
  const receipt = buildProductMatrixReceipt(source, source, completeProductMatrixResults(), "2026-08-16T00:00:00.000Z");
  assert.deepEqual(receipt.source, { sha: source.sha, tree: source.tree });
  assert.equal(receipt.zeroSkip, true);
  assert.deepEqual(receipt.stages.map((stage) => stage.name), productMatrixStages);
  assert.ok(receipt.stages.every((stage) => stage.passed === true && stage.skipped === 0));
  assert.deepEqual(receipt.cas, {
    winnerCount: 1,
    loserMutationCount: 0,
    evidenceTests: [
      "TestWorkspaceLaunchReservedStageReplayCASAllowsOneWriter",
      "TestPostgresWorkspaceLaunchConcurrentReplayResumeAllowsOneWriter"
    ]
  });
  assert.deepEqual(receipt.unknown.authorityWriteDeltas, {
    controlPlane: 0,
    sub2api: 0,
    fabric: 0,
    ledger: 0
  });
  assert.deepEqual(receipt.tests.map((entry) => ({ package: entry.package, name: entry.name })), productMatrixRequiredTests);
  for (const packageName of productMatrixRequiredPackages) {
    assert.ok(receipt.packages.some((entry) => entry.name === packageName));
  }
});

test("Product matrix receipt rejects dirty source drift and incomplete test evidence", () => {
  const source = { sha: "a".repeat(40), tree: "b".repeat(40), clean: true };
  assert.throws(() => buildProductMatrixReceipt({ ...source, clean: false }, source, completeProductMatrixResults()), /clean/);
  assert.throws(() => buildProductMatrixReceipt(source, { ...source, tree: "c".repeat(40) }, completeProductMatrixResults()), /changed/);
  const missingPackage = completeProductMatrixResults();
  missingPackage[2] = { ...missingPackage[2], passedPackages: [] };
  assert.throws(() => buildProductMatrixReceipt(source, source, missingPackage), /required package/);
  const missingTest = completeProductMatrixResults();
  missingTest[2] = { ...missingTest[2], passedTests: missingTest[2].passedTests.slice(1) };
  assert.throws(() => buildProductMatrixReceipt(source, source, missingTest), /required test/);
  const skipped = completeProductMatrixResults();
  skipped[0] = { ...skipped[0], skipped: 1 };
  assert.throws(() => buildProductMatrixReceipt(source, source, skipped), /zero skip/);
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

test("full verification adds the temporary PostgreSQL lane after the default gate", async () => {
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
