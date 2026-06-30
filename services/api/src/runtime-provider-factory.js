import { LocalDockerProvider } from "./runtime-providers/local-docker.js";
import { TencentCvmProvider } from "./runtime-providers/tencent-cvm.js";

export function createRuntimeProvider({ env = process.env, rootDir = ".runtime/workspaces" } = {}) {
  const provider = env.OPL_RUNTIME_PROVIDER || "local-docker";
  if (provider === "local-docker") {
    return new LocalDockerProvider({
      rootDir,
      baseUrl: env.OPL_PUBLIC_URL || "http://127.0.0.1:8787",
      image: env.OPL_WORKSPACE_IMAGE || "ghcr.io/gaofeng21cn/one-person-lab-webui:latest",
      execute: env.OPL_LOCAL_DOCKER_EXECUTE === "1"
    });
  }
  if (provider === "tencent-cvm") {
    return new TencentCvmProvider({ env });
  }
  throw new Error(`unknown_runtime_provider:${provider}`);
}
