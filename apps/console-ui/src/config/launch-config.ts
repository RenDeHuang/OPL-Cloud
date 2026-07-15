export const defaultLaunchConfig = {
  siteName: "OPL Console",
  siteLogoText: "OPL",
  gatewayExternalUrl: "https://gflabtoken.cn/",
  tableDefaultPageSize: 20,
  features: {
    workspaces: true,
    support: true,
    runtimeAdmin: true,
    ledgerAdmin: true,
  }
};

function defineFlag({ key, mode, label }: any) {
  return { key, mode, label };
}

export const FeatureFlags = {
  workspaces: defineFlag({ key: "workspaces", mode: "opt-out", label: "Workspaces" }),
  support: defineFlag({ key: "support", mode: "opt-out", label: "Support" }),
  runtimeAdmin: defineFlag({ key: "runtimeAdmin", mode: "opt-in", label: "Runtime admin" }),
  ledgerAdmin: defineFlag({ key: "ledgerAdmin", mode: "opt-in", label: "Ledger admin" }),
};

export function isFeatureEnabled(flagKey, config = defaultLaunchConfig) {
  const flag = FeatureFlags[flagKey];
  if (!flag) throw new Error(`unknown feature flag: ${flagKey}`);
  const raw = config?.features?.[flag.key];
  if (typeof raw === "boolean") return raw;
  return flag.mode === "opt-out";
}
