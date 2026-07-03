import React from "react";
import { Button, Typography } from "antd";
import { ExternalLink, KeyRound } from "lucide-react";
import {
  ActionGroup,
  ConsoleSurface,
  InsightPanel,
  MetricStrip,
  ObjectTable,
  ResourceSplit,
  StatusPill
} from "../shared/commercial-console.jsx";
import { money, usageAmount } from "../shared/formatters.js";

export function GatewayPage({ state }) {
  const requestUsage = state.requestUsageLogs || [];
  return (
    <ConsoleSurface
      title="Gateway"
      eyebrow="External integration"
      subtitle="gflabtoken.cn usage and request billing"
      extra={<Button type="primary" icon={<ExternalLink size={15} />} onClick={() => window.open("https://gflabtoken.cn/", "_blank", "noopener,noreferrer")}>打开 Gateway</Button>}
    >
      <MetricStrip
        items={[
          { label: "请求", value: requestUsage.length, caption: "metered API calls", tone: requestUsage.length ? "info" : "neutral" },
          { label: "扣费", value: money(usageAmount(requestUsage)), caption: "request debit", tone: usageAmount(requestUsage) ? "warn" : "neutral" },
          { label: "可用密钥", value: 1, caption: "current lab scope", icon: <KeyRound size={16} />, tone: "good" },
          { label: "外部入口", value: "Live", caption: "gflabtoken.cn", tone: "good" },
          { label: "账单绑定", value: "Wallet", caption: "same account balance", tone: "info" }
        ]}
      />

      <div className="consoleGrid">
        <InsightPanel
          title="接入状态"
          eyebrow="Gateway"
          actions={<StatusPill label="Active" tone="good" />}
        >
          <ResourceSplit
            items={[
              { label: "入口", value: "gflabtoken.cn", meta: "external OPL Gateway", status: "external", tone: "info" },
              { label: "作用域", value: "当前实验室", meta: "billing account scoped", status: "scoped", tone: "good" },
              { label: "扣费", value: money(usageAmount(requestUsage)), meta: "request usage total", status: "metered", tone: usageAmount(requestUsage) ? "warn" : "neutral" },
              { label: "钥匙", value: "1 active", meta: "managed by Gateway", status: "ready", tone: "good" }
            ]}
          />
          <ActionGroup actions={[
            { label: "打开 Gateway", type: "primary", icon: <ExternalLink size={15} />, onClick: () => window.open("https://gflabtoken.cn/", "_blank", "noopener,noreferrer") }
          ]} />
        </InsightPanel>

        <InsightPanel title="最近请求" eyebrow="Usage">
          <ObjectTable
            rowKey={(row) => row.id}
            data={requestUsage.slice(-10).reverse()}
            emptyText="暂无请求用量"
            columns={[
              { title: "请求", dataIndex: "requestId", ellipsis: true, render: (value) => <Typography.Text ellipsis>{value}</Typography.Text> },
              { title: "Workspace", dataIndex: "workspaceId", ellipsis: true },
              { title: "金额", dataIndex: "amount", render: (value) => money(value) }
            ]}
          />
        </InsightPanel>
      </div>
    </ConsoleSurface>
  );
}
