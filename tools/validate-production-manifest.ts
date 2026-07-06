import { readFile } from "node:fs/promises";

import { validateProductionManifest } from "../packages/console/src/production-manifest.ts";

function cliArgs(argv) {
  const args = {};
  for (let index = 0; index < argv.length; index += 1) {
    const item = argv[index];
    if (!item.startsWith("--")) continue;
    const key = item.slice(2);
    const value = argv[index + 1] && !argv[index + 1].startsWith("--") ? argv[++index] : "true";
    args[key] = value;
  }
  return args;
}

export async function runProductionManifestCli({
  argv = process.argv.slice(2),
  stdout = process.stdout,
  stderr = process.stderr
} = {}) {
  const args = cliArgs(argv);
  if (!args.manifest) throw new Error("manifest_path_required");
  const manifest = JSON.parse(await readFile(args.manifest, "utf8"));
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
