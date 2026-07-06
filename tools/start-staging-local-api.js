import { spawn } from "node:child_process";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import { buildTencentProvisioner, defaultProvisionerBin } from "./build-tencent-provisioner.js";
import {
  applyEnv,
  defaultStagingEnvPath,
  loadEnvFile,
  validateStagingLocalEnv
} from "./staging-env.js";

const root = fileURLToPath(new URL("..", import.meta.url));
const envFile = process.env.OPL_STAGING_ENV_FILE || defaultStagingEnvPath;
let loadedEnv = null;
try {
  loadedEnv = loadEnvFile({ filePath: envFile, baseEnv: process.env });
} catch (error) {
  console.error(JSON.stringify({
    ok: false,
    error: error.message,
    envFile
  }, null, 2));
  process.exit(1);
}

loadedEnv.OPL_TENCENT_PROVISIONER_BIN ||= defaultProvisionerBin;
loadedEnv.PORT ||= "8080";
loadedEnv.CONTROL_PLANE_ADDR ||= `:${loadedEnv.PORT}`;
loadedEnv.OPL_PUBLIC_URL ||= `http://127.0.0.1:${loadedEnv.PORT}`;
applyEnv(loadedEnv);

await buildTencentProvisioner({ binPath: process.env.OPL_TENCENT_PROVISIONER_BIN });

const envReport = validateStagingLocalEnv(process.env);
if (!envReport.ready) {
  console.error(JSON.stringify({
    ok: false,
    error: "staging_local_env_not_ready",
    envFile,
    ...envReport
  }, null, 2));
  process.exit(1);
}

console.log(`OPL Control Plane local-to-staging API listening on http://127.0.0.1:${process.env.PORT}`);
console.log(`Env file: ${envFile}`);
console.log(`Runtime provider: ${process.env.OPL_RUNTIME_PROVIDER}`);
console.log(`Database: ${process.env.DATABASE_URL ? "staging PostgreSQL configured" : "missing"}`);
console.log(`Provisioner: ${process.env.OPL_TENCENT_PROVISIONER_BIN}`);
console.log(`Workspace domain: ${process.env.OPL_WORKSPACE_DOMAIN}`);
console.log(`Repo: ${root}`);

const child = spawn("go", ["run", "./cmd/control-plane"], {
  cwd: join(root, "services", "control-plane"),
  env: process.env,
  stdio: "inherit"
});

child.on("exit", (code) => {
  process.exit(code ?? 1);
});
