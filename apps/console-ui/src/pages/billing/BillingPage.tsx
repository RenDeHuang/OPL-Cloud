import React from "react";
import { Typography } from "antd";
import {
  ConsoleSurface,
  InsightPanel,
  MetricStrip,
  ObjectTable
} from "../shared/commercial-console.tsx";
import { available, money, moneyValue, resourceDebitEvents } from "../shared/formatters.ts";

type AnyRecord = Record<string, any>;

function activeHourlyEstimate(state: AnyRecord = {}) {
  const computeHourly = (state.computeAllocations || [])
    .filter((item) => item.billingStatus === "active" && !["destroyed", "failed"].includes(item.status))
    .reduce((sum, item) => sum + Number(item.hourlyPrice || 0), 0);
  const storageHourly = (state.storageVolumes || [])
    .filter((item) => item.billingStatus === "active" && item.status !== "destroyed")
    .reduce((sum, item) => sum + Number(item.hourlyEstimate || 0), 0);
  return computeHourly + storageHourly;
}

export function BillingPage({ state, wallet }: any) {
  const recent = resourceDebitEvents(state).map((item) => ({
    ...item,
    billingType: item.computeAllocationId ? "计算" : item.storageId ? "存储" : "资源",
    amount: Math.abs(moneyValue(item))
  })).slice(-12).reverse();
  const usable = available(wallet);
  const hourlyEstimate = activeHourlyEstimate(state);
  const spent = recent.reduce((sum, event) => sum + event.amount, 0);

  return (
    <ConsoleSurface title="账单" eyebrow="钱包" subtitle="余额、冻结金额和资源费用">
      <MetricStrip
        items={[
          { label: "可用", value: money(usable), caption: "可开通计算或存储", tone: usable > 0 ? "good" : "warn" },
          { label: "冻结", value: money(wallet.frozen), caption: "已预留费用", tone: Number(wallet.frozen || 0) > 0 ? "info" : "neutral" },
          { label: "余额", value: money(wallet.balance), caption: "可用加冻结", tone: "neutral" },
          { label: "资源费用", value: money(spent), caption: "最近扣费记录", tone: spent > 0 ? "warn" : "neutral" },
          { label: "预计每小时", value: money(hourlyEstimate), caption: "当前活跃资源", tone: hourlyEstimate > 0 ? "info" : "neutral" }
        ]}
      />

      <InsightPanel title="费用明细" eyebrow="扣费记录">
        <ObjectTable
          rowKey={(row) => row.id}
          data={recent}
          emptyText="暂无扣费记录"
          columns={[
            { title: "类型", dataIndex: "billingType", width: 90 },
            { title: "工作区", dataIndex: "workspaceId", ellipsis: true, render: (value) => <Typography.Text ellipsis>{value || "账号"}</Typography.Text> },
            { title: "资源", render: (_, row) => row.computeAllocationId || row.storageId || row.resourceId || "-" },
            { title: "金额", dataIndex: "amount", render: (value) => money(value) }
          ]}
        />
      </InsightPanel>
    </ConsoleSurface>
  );
}
