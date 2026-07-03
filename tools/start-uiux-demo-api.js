import { createServer } from "node:http";
import { mkdir, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { emptyState } from "../packages/console/src/store.js";
import { uiuxDemoAccounts, uiuxDemoAuthSeedJson } from "./uiux-demo-fixture.js";

const root = fileURLToPath(new URL("..", import.meta.url));
const port = String(process.env.PORT || "8791");
const dataPath = process.env.OPL_CLOUD_DATA_PATH || join(root, ".runtime", "uiux-demo-state.json");
const resetState = process.env.OPL_UIUX_DEMO_RESET !== "0";
const owner = uiuxDemoAccounts.find((account) => account.role === "pi");
const admin = uiuxDemoAccounts.find((account) => account.role === "admin");

process.env.PORT = port;
process.env.OPL_CLOUD_DATA_PATH = dataPath;
process.env.OPL_CONSOLE_USERS_JSON ||= uiuxDemoAuthSeedJson();
process.env.OPL_PUBLIC_URL ||= `http://127.0.0.1:${port}`;

if (resetState) {
  await mkdir(dirname(dataPath), { recursive: true });
  await writeFile(dataPath, `${JSON.stringify(emptyState(), null, 2)}\n`);
}

const {
  appStore,
  createAuthController,
  createRequestHandler,
  service
} = await import("../packages/console/api/server.js");

const auth = createAuthController({ env: process.env, store: appStore });

async function seedAuthUser(account) {
  await auth.login(
    { email: account.email, password: account.password },
    {
      request: { headers: {} },
      response: { setHeader() {} }
    }
  );
}

async function seedBusinessChain() {
  for (const account of uiuxDemoAccounts) await seedAuthUser(account);

  const state = await appStore.read();
  if (Object.keys(state.workspaces || {}).length > 0) return;

  await service.manualTopUp({
    accountId: owner.accountId,
    amount: 500,
    reason: "uiux_demo_credit",
    operatorUserId: admin.id,
    operatorAccountId: admin.accountId
  });

  const workspace = await service.createWorkspace({
    accountId: owner.accountId,
    userId: owner.id,
    workspaceName: "OPL Demo Workspace",
    packageId: "basic"
  });

  await service.recordRequestUsage({
    accountId: owner.accountId,
    userId: owner.id,
    workspaceId: workspace.id,
    requestId: "uiux-demo-request-001",
    provider: "sub2api",
    model: "gflabtoken",
    inputTokens: 2400,
    outputTokens: 760,
    amount: 0.42,
    sourceEventId: "uiux_demo_gateway_request"
  });

  await service.createSupportTicket({
    accountId: owner.accountId,
    userId: owner.id,
    title: "Workspace URL readiness",
    category: "Workspace",
    priority: "normal",
    workspaceId: workspace.id,
    description: "Demo ticket for the controlled launch support loop.",
    author: owner.email
  });
}

await seedBusinessChain();

const server = createServer(createRequestHandler({ auth }));
server.listen(Number(port), () => {
  console.log(`OPL Console demo API listening on http://127.0.0.1:${port}`);
  console.log(`State file: ${dataPath}`);
  console.log("Demo accounts:");
  for (const account of uiuxDemoAccounts) {
    console.log(`- ${account.label}: ${account.email} / ${account.password}`);
  }
});
