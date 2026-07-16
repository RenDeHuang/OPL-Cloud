import React from "react";
import { Alert, Button, Typography, message } from "antd";
import { Copy, ExternalLink, Eye, EyeOff, KeyRound, RefreshCw, WalletCards } from "lucide-react";
import { getGatewaySummary } from "../../api/console-read-api.ts";
import {
  ActionGroup,
  ConsoleSurface,
  InsightPanel,
  MetricStrip,
  ResourceSplit,
  StatusPill
} from "../shared/commercial-console.tsx";
import { paidThrough, usdBalance, usdMicros } from "../shared/formatters.ts";
import { createGatewayRequestLifecycle, maskGatewaySummary } from "./gateway-request.ts";

export function GatewayPage({ state = {} }: any) {
  const [summary, setSummary] = React.useState<any>(null);
  const [error, setError] = React.useState("");
  const [loading, setLoading] = React.useState(true);
  const [revealing, setRevealing] = React.useState(false);
  const [reloadRun, setReloadRun] = React.useState(0);
  const requestLifecycle = React.useRef(createGatewayRequestLifecycle());
  const summaryRef = React.useRef<any>(null);
  const gatewayUrl = state.gateway?.url || "https://gflabtoken.cn";

  React.useEffect(() => () => {
    requestLifecycle.current.dispose();
    summaryRef.current = null;
  }, []);

  React.useEffect(() => {
    const controller = requestLifecycle.current.start();
    summaryRef.current = null;
    setSummary(null);
    setError("");
    setLoading(true);
    setRevealing(false);
    getGatewaySummary(false, controller.signal)
      .then((payload) => {
        if (!requestLifecycle.current.isCurrent(controller)) return;
        summaryRef.current = payload;
        setSummary(payload);
      })
      .catch((err) => {
        if (requestLifecycle.current.isCurrent(controller)) setError(err.message);
      })
      .finally(() => {
        if (requestLifecycle.current.isCurrent(controller)) setLoading(false);
      });
    return () => {
      requestLifecycle.current.cancel(controller);
      summaryRef.current = null;
    };
  }, [reloadRun]);

  async function revealKey() {
    const controller = requestLifecycle.current.start();
    setRevealing(true);
    setError("");
    try {
      const payload = await getGatewaySummary(true, controller.signal);
      if (!requestLifecycle.current.isCurrent(controller)) return;
      summaryRef.current = payload;
      setSummary(payload);
    } catch (err) {
      if (requestLifecycle.current.isCurrent(controller)) setError(err.message);
    } finally {
      if (requestLifecycle.current.isCurrent(controller)) setRevealing(false);
    }
  }

  function hideKey() {
    setSummary((current) => {
      const masked = maskGatewaySummary(current);
      summaryRef.current = masked;
      return masked;
    });
  }

  async function copyKey() {
    const value = summary?.apiKey?.value;
    if (!value) return;
    try {
      await navigator.clipboard.writeText(value);
      message.success("API Key 已复制");
    } catch {
      message.error("复制失败，请重试");
    }
  }

  const apiKey = summary?.apiKey || {};
  const usage = summary?.usage || {};
  const revealed = apiKey.revealed === true && Boolean(apiKey.value);

  return (
    <ConsoleSurface
      title="OPL Gateway"
      eyebrow="Sub2API"
      subtitle="账户余额、API Key 与模型用量"
      extra={<Button icon={<ExternalLink size={15} />} onClick={() => window.open(gatewayUrl, "_blank", "noopener,noreferrer")}>打开钱包</Button>}
    >
      {loading && <Alert type="info" showIcon message="正在加载 Gateway 数据" />}
      {error && (
        <Alert
          type="error"
          showIcon
          message="Gateway 数据不可用"
          description={error}
          action={<Button icon={<RefreshCw size={14} />} onClick={() => setReloadRun((value) => value + 1)}>重试</Button>}
        />
      )}

      {summary && (
        <>
          <MetricStrip
            items={[
              { label: "实时余额", value: usdBalance(summary.balance), caption: "Sub2API USD", icon: <WalletCards size={16} />, tone: Number(summary.balance?.usdMicros || 0) > 0 ? "good" : "warn" },
              { label: "API Key", value: apiKey.name || "-", caption: apiKey.maskedValue || "-", icon: <KeyRound size={16} />, tone: apiKey.status === "active" ? "good" : "warn" },
              { label: "近 1 天用量", value: usdMicros(usage.usage1dUsdMicros), caption: `最近使用 ${paidThrough(usage.lastUsedAt)}`, tone: "info" }
            ]}
          />

          <div className="consoleGrid equal">
            <InsightPanel title="Workspace API Key" eyebrow="账户密钥" actions={<StatusPill label={apiKey.status || "unknown"} tone={apiKey.status === "active" ? "good" : "warn"} />}>
              <div className="stackList">
                <Typography.Text className="inlineCode gatewaySecretValue" aria-label={revealed ? "已显示 API Key" : "已遮罩 API Key"}>
                  {revealed ? apiKey.value : apiKey.maskedValue || "********"}
                </Typography.Text>
                <ActionGroup actions={[
                  revealed
                    ? { label: "隐藏", icon: <EyeOff size={15} />, onClick: hideKey }
                    : { label: "显示", icon: <Eye size={15} />, onClick: revealKey, disabled: revealing },
                  { label: "复制", icon: <Copy size={15} />, onClick: copyKey, disabled: !revealed }
                ]} />
              </div>
            </InsightPanel>

            <InsightPanel title="模型用量" eyebrow="当前 Key">
              <ResourceSplit
                items={[
                  { label: "近 5 小时", value: usdMicros(usage.usage5hUsdMicros), status: "实时", tone: "info" },
                  { label: "近 1 天", value: usdMicros(usage.usage1dUsdMicros), status: "实时", tone: "info" },
                  { label: "近 7 天", value: usdMicros(usage.usage7dUsdMicros), status: "实时", tone: "info" },
                  { label: "额度已用", value: usdMicros(usage.quotaUsedUsdMicros), meta: `总额度 ${usdMicros(usage.quotaUsdMicros)}`, status: "Quota", tone: "good" },
                  { label: "最近使用", value: paidThrough(usage.lastUsedAt), status: "Key DTO", tone: "neutral" }
                ]}
              />
            </InsightPanel>
          </div>
        </>
      )}
    </ConsoleSurface>
  );
}
