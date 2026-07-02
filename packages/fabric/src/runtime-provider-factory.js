import { LocalDockerProvider } from "./runtime-providers/local-docker.js";
import { TencentTkeProvider } from "./runtime-providers/tencent-tke.js";

const DEFAULT_WORKSPACE_IMAGE = "ghcr.io/gaofeng21cn/one-person-lab-app:latest";

export function createRuntimeProvider({ env = process.env, rootDir = ".runtime/workspaces" } = {}) {
  const provider = env.OPL_RUNTIME_PROVIDER || "local-docker";
  if (provider === "local-docker") {
    return new LocalDockerProvider({
      rootDir,
      baseUrl: env.OPL_PUBLIC_URL || "http://127.0.0.1:8787",
      image: env.OPL_WORKSPACE_IMAGE || DEFAULT_WORKSPACE_IMAGE,
      execute: env.OPL_LOCAL_DOCKER_EXECUTE === "1"
    });
  }
  if (provider === "tencent-tke") {
    return new TencentTkeProvider({ env });
  }
  throw new Error(`unknown_runtime_provider:${provider}`);
}
