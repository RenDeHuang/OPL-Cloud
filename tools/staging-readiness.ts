import { buildTencentProvisioner, defaultProvisionerBin } from "./build-tencent-provisioner.ts";
import {
  applyEnv,
  defaultStagingEnvPath,
  loadEnvFile,
  validateStagingLocalEnv
} from "./staging-env.ts";

const envFile = process.env.OPL_STAGING_ENV_FILE || defaultStagingEnvPath;
let loadedEnv = null;
try {
  loadedEnv = loadEnvFile({ filePath: envFile, baseEnv: process.env });
} catch (error) {
  process.stderr.write(`${JSON.stringify({
    ok: false,
    error: error.message,
    envFile
  }, null, 2)}\n`);
  process.exit(1);
}
loadedEnv.OPL_TENCENT_PROVISIONER_BIN ||= defaultProvisionerBin;
applyEnv(loadedEnv);

await buildTencentProvisioner({ binPath: process.env.OPL_TENCENT_PROVISIONER_BIN });

const envReport = validateStagingLocalEnv(process.env);
let productionReport = null;
let runtimeReport = null;

if (envReport.ready) {
  const [{ productionReadiness }, { createRuntimeProvider }] = await Promise.all([
    import("../packages/console/src/production-readiness.ts"),
    import("../packages/fabric/src/runtime-provider-factory.ts")
  ]);
  productionReport = await productionReadiness({ env: process.env });
  runtimeReport = await createRuntimeProvider({ env: process.env }).readiness();
}

const result = {
  ok: Boolean(envReport.ready && productionReport?.ready && runtimeReport?.ready),
  envFile,
  env: envReport,
  production: productionReport,
  runtime: runtimeReport
};

const output = `${JSON.stringify(result, null, 2)}\n`;
if (result.ok) {
  process.stdout.write(output);
} else {
  process.stderr.write(output);
  process.exitCode = 1;
}
