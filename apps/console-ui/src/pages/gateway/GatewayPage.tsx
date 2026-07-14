import React from "react";
import { Button } from "antd";
import { ExternalLink, KeyRound, WalletCards } from "lucide-react";
import {
  ActionGroup,
  ConsoleSurface,
  InsightPanel,
  MetricStrip,
  ResourceSplit,
  StatusPill
} from "../shared/commercial-console.tsx";
import { usdBalance } from "../shared/formatters.ts";

export function GatewayPage({ state = {} }: any) {
  const gateway = state.gateway || {};
  const balance = state.balance || {};
  const gatewayUrl = gateway.url || "https://gflabtoken.cn";
  return (
    <ConsoleSurface
      title="gflabtoken.cn"
      eyebrow="Sub2API"
      subtitle="统一余额、API Key 与 AI 请求用量"
      extra={<Button type="primary" icon={<ExternalLink size={15} />} onClick={() => window.open(gatewayUrl, "_blank", "noopener,noreferrer")}>打开钱包</Button>}
    >
      <MetricStrip
        items={[
          { label: "实时余额", value: usdBalance(balance), caption: "与账单页相同的 Sub2API 投影", icon: <WalletCards size={16} />, tone: Number(balance.usdMicros || 0) > 0 ? "good" : "warn" },
          { label: "余额来源", value: "Sub2API", caption: "唯一可消费余额", tone: "good" },
          { label: "API Key", value: "门户管理", caption: "Console 不复制密钥", icon: <KeyRound size={16} />, tone: "info" }
        ]}
      />

      <div className="consoleGrid equal">
        <InsightPanel title="钱包入口" eyebrow="外部门户" actions={<StatusPill label="外部" tone="info" />}>
          <ResourceSplit
            items={[
              { label: "入口", value: "gflabtoken.cn", meta: "充值与 API Key 管理", status: "Sub2API", tone: "good" },
              { label: "余额", value: usdBalance(balance), meta: "Control Plane 实时读取", status: "USD", tone: "good" },
              { label: "资源扣款", value: "按月预付", meta: "由 Console 提交产品命令", status: "解耦", tone: "info" }
            ]}
          />
          <ActionGroup actions={[
            { label: "打开钱包", type: "primary", icon: <ExternalLink size={15} />, onClick: () => window.open(gatewayUrl, "_blank", "noopener,noreferrer") }
          ]} />
        </InsightPanel>

        <InsightPanel title="职责边界" eyebrow="单一真相">
          <ResourceSplit
            items={[
              { label: "Sub2API", value: "余额 / Key / AI usage", meta: "唯一钱包", status: "Owner", tone: "good" },
              { label: "Control Plane", value: "购买 / 续费 / 到期", meta: "只服务 Console", status: "编排", tone: "info" },
              { label: "Fabric", value: "计算 / 存储 / Runtime", meta: "Provider 事实", status: "资源", tone: "info" },
              { label: "Ledger", value: "Receipt / Review", meta: "只记录证据", status: "审计", tone: "info" }
            ]}
          />
        </InsightPanel>
      </div>
    </ConsoleSurface>
  );
}
