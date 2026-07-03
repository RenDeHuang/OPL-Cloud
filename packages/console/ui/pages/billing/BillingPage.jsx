import React from "react";
import { Typography } from "antd";
import {
  ConsoleSurface,
  InsightPanel,
  MetricStrip,
  ObjectTable,
  ResourceSplit,
  TimelineList
} from "../shared/commercial-console.jsx";
import { available, money, usageQuantity } from "../shared/formatters.js";

export function BillingPage({ state, wallet }) {
  const resourceUsage = state.resourceUsageLogs || [];
  const requestUsage = state.requestUsageLogs || [];
  const recent = [
    ...resourceUsage.map((item) => ({ ...item, billingType: item.resourceType === "compute" ? "计算" : "存储" })),
    ...requestUsage.map((item) => ({ ...item, billingType: "请求", quantity: 1, unit: "request" }))
  ].slice(-12).reverse();
  const usable = available(wallet);
  const frozen = Number(wallet.frozen || 0);
  const balance = Number(wallet.balance || 0);
  const frozenPercent = balance > 0 ? Math.min(100, Math.round((frozen / balance) * 100)) : 0;
  const walletEvents = (state.walletTransactions || []).slice(-6).reverse().map((event) => ({
    title: event.type,
    description: event.workspaceId || event.accountId,
    meta: money(event.amount),
    tone: Number(event.amount || 0) < 0 ? "warn" : "good"
  }));

  return (
    <ConsoleSurface title="Billing" eyebrow="Wallet" subtitle="Prepaid balance, holds, resource usage">
      <MetricStrip
        items={[
          { label: "可用", value: money(usable), caption: "can open compute or storage", tone: usable > 0 ? "good" : "warn" },
          { label: "冻结", value: money(wallet.frozen), caption: `${frozenPercent}% of balance`, tone: frozenPercent > 70 ? "warn" : "info" },
          { label: "余额", value: money(wallet.balance), caption: "available plus frozen", tone: "neutral" },
          { label: "累计充值", value: money(wallet.totalRecharged), caption: "manual top-up ledger", tone: "good" },
          { label: "扣费记录", value: recent.length, caption: "recent resource events", tone: recent.length ? "info" : "neutral" }
        ]}
      />

      <div className="consoleGrid">
        <InsightPanel title="钱包拆分" eyebrow="Balance">
          <div className="stackList">
            <div className="walletBar"><span style={{ width: `${frozenPercent}%` }} /></div>
            <div className="stackRow"><span>可用余额</span><strong>{money(usable)}</strong></div>
            <div className="stackRow"><span>冻结金额</span><strong>{money(wallet.frozen)}</strong></div>
            <div className="stackRow"><span>总余额</span><strong>{money(wallet.balance)}</strong></div>
          </div>
        </InsightPanel>

        <InsightPanel title="资源用量" eyebrow="Usage">
          <ResourceSplit
            items={[
              { label: "Compute", value: `${usageQuantity(resourceUsage, "compute").toFixed(1)} h`, meta: "compute resource usage", status: "hourly", tone: "info" },
              { label: "Storage", value: `${usageQuantity(resourceUsage, "storage").toFixed(1)} GB-h`, meta: "storage volume usage", status: "retained", tone: "good" },
              { label: "Gateway", value: requestUsage.length, meta: "request usage logs", status: "metered", tone: "info" },
              { label: "充值记录", value: state.manualTopups?.length || 0, meta: "admin top-up evidence", status: "audited", tone: "good" }
            ]}
          />
        </InsightPanel>
      </div>

      <div className="consoleGrid">
        <InsightPanel title="最近扣费" eyebrow="Ledger">
          <ObjectTable
            rowKey={(row) => row.id}
            data={recent}
            emptyText="暂无扣费记录"
            columns={[
              { title: "类型", dataIndex: "billingType", width: 90 },
              { title: "Workspace", dataIndex: "workspaceId", ellipsis: true, render: (value) => <Typography.Text ellipsis>{value || "account"}</Typography.Text> },
              { title: "用量", render: (_, row) => `${Number(row.quantity || 0).toFixed(2)} ${row.unit || ""}` },
              { title: "金额", dataIndex: "amount", render: (value) => money(value) }
            ]}
          />
        </InsightPanel>
        <InsightPanel title="钱包流水" eyebrow="Transactions">
          <TimelineList items={walletEvents} emptyText="暂无钱包流水" />
        </InsightPanel>
      </div>
    </ConsoleSurface>
  );
}
