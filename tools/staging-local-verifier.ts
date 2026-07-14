import {
  applyEnv,
  defaultStagingEnvPath,
  loadEnvFile,
  validateStagingLocalEnv
} from "./staging-env.ts";
import { PAID_CONFIRMATION, verifyProductionChain } from "./production-verifier.ts";

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

function fail(error, details = {}) {
  process.stderr.write(`${JSON.stringify({ ok: false, error, ...details }, null, 2)}\n`);
  process.exit(1);
}

const args = cliArgs(process.argv.slice(2));
const envFile = process.env.OPL_STAGING_ENV_FILE || defaultStagingEnvPath;
let loadedEnv = null;
try {
  loadedEnv = loadEnvFile({ filePath: envFile, baseEnv: process.env });
} catch (error) {
  fail(error.message, { envFile });
}
applyEnv(loadedEnv);

if (process.env.OPL_CONFIRM_REAL_CLOUD_E2E !== "1") {
  fail("real_cloud_e2e_confirmation_required", {
    required: "Set OPL_CONFIRM_REAL_CLOUD_E2E=1 in the operator shell or .env.staging.local.",
    warning: "This creates and later destroys real Tencent Cloud resources and charges the mapped Sub2API balance."
  });
}

const envReport = validateStagingLocalEnv(process.env);
if (!envReport.ready) {
  fail("staging_local_env_not_ready", { envFile, ...envReport });
}

const origin = args.origin || process.env.OPL_CONSOLE_ORIGIN || `http://127.0.0.1:${process.env.PORT || "8787"}`;

try {
  const result = await verifyProductionChain({
    origin,
    allowPrivateConsoleOrigin: true,
    authUsersJson: process.env.OPL_CONSOLE_USERS_JSON,
    accountId: args.account || process.env.OPL_VERIFY_ACCOUNT_ID,
    workspaceName: args.workspace || process.env.OPL_VERIFY_WORKSPACE_NAME,
    runId: args["run-id"] || process.env.OPL_VERIFY_RUN_ID,
    packageId: args.package || process.env.OPL_VERIFY_PACKAGE_ID,
    paidConfirmation: PAID_CONFIRMATION,
    workspaceUrlAttempts: Number(args["url-attempts"] || process.env.OPL_VERIFY_URL_ATTEMPTS || 12),
    retryDelayMs: Number(args["retry-delay-ms"] || process.env.OPL_VERIFY_RETRY_DELAY_MS || 5000)
  });
  process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
} catch (error) {
  fail(error.message, {
    cleanupErrors: error.cleanupErrors || []
  });
}
