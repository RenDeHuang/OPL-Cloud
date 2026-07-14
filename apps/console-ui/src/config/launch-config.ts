export const defaultLaunchConfig = {
  siteName: "OPL Console",
  siteLogoText: "OPL",
  gatewayExternalUrl: "https://gflabtoken.cn/",
  tableDefaultPageSize: 20,
  features: {
    workspaces: true,
    billing: true,
    support: true,
    alerts: true,
    gatewayExternalLink: true,
    runtimeAdmin: true,
    ledgerAdmin: true,
    registration: false,
    invites: false,
    sso: false,
    fabricAdmin: false,
    governanceAdmin: false,
    adminSettings: false,
  }
};

function defineFlag({ key, mode, label }: any) {
  return { key, mode, label };
}

export const FeatureFlags = {
  workspaces: defineFlag({ key: "workspaces", mode: "opt-out", label: "Workspaces" }),
  billing: defineFlag({ key: "billing", mode: "opt-out", label: "Billing" }),
  support: defineFlag({ key: "support", mode: "opt-out", label: "Support" }),
  alerts: defineFlag({ key: "alerts", mode: "opt-out", label: "Alerts" }),
  gatewayExternalLink: defineFlag({ key: "gatewayExternalLink", mode: "opt-in", label: "Gateway external link" }),
  runtimeAdmin: defineFlag({ key: "runtimeAdmin", mode: "opt-in", label: "Runtime admin" }),
  ledgerAdmin: defineFlag({ key: "ledgerAdmin", mode: "opt-in", label: "Ledger admin" }),
  registration: defineFlag({ key: "registration", mode: "opt-in", label: "Registration" }),
  invites: defineFlag({ key: "invites", mode: "opt-in", label: "Invites" }),
  sso: defineFlag({ key: "sso", mode: "opt-in", label: "SSO" }),
  fabricAdmin: defineFlag({ key: "fabricAdmin", mode: "opt-in", label: "Fabric admin" }),
  governanceAdmin: defineFlag({ key: "governanceAdmin", mode: "opt-in", label: "Governance admin" }),
  adminSettings: defineFlag({ key: "adminSettings", mode: "opt-in", label: "Admin settings" }),
};

export function isFeatureEnabled(flagKey, config = defaultLaunchConfig) {
  const flag = FeatureFlags[flagKey];
  if (!flag) throw new Error(`unknown feature flag: ${flagKey}`);
  const raw = config?.features?.[flag.key];
  if (typeof raw === "boolean") return raw;
  return flag.mode === "opt-out";
}
