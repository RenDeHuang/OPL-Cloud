import { readFile } from "node:fs/promises";
import { parseArgs } from "node:util";

import { validateProductionManifest } from "../services/control-plane/ops/production-manifest.ts";

function optionValue(value) {
  return value === undefined ? undefined : String(value || "true");
}

export async function runProductionManifestCli({
  argv = process.argv.slice(2),
  stdout = process.stdout,
  stderr = process.stderr
} = {}) {
  const { values: args } = parseArgs({
    args: argv,
    options: { manifest: { type: "string" } },
    strict: false,
    allowPositionals: true
  });
  const manifestPath = optionValue(args.manifest);
  if (!manifestPath) throw new Error("manifest_path_required");
  const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
  const report = validateProductionManifest(manifest);
  stdout.write(`${JSON.stringify(report, null, 2)}\n`);
  if (!report.ok) {
    stderr.write("production_manifest_invalid\n");
    return 1;
  }
  return 0;
}

if (import.meta.url === `file://${process.argv[1]}`) {
  runProductionManifestCli().then((code) => {
    process.exitCode = code;
  }).catch((error) => {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  });
}
