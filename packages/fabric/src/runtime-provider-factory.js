import { TencentTkeProvider } from "./runtime-providers/tencent-tke.js";

export function createRuntimeProvider({ env = process.env } = {}) {
  const provider = env.OPL_RUNTIME_PROVIDER || "tencent-tke";
  if (provider === "tencent-tke") {
    return new TencentTkeProvider({ env });
  }
  throw new Error(`unknown_runtime_provider:${provider}`);
}
