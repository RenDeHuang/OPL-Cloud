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

function nextSettlementAt(now = new Date()) {
  const next = new Date(now);
  next.setMinutes(0, 0, 0);
  next.setHours(next.getHours() + 1);
  return next.toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit"
  });
}

function runningDuration(startedAt) {
  if (!startedAt) return "-";
  const elapsedMs = Math.max(0, Date.now() - new Date(startedAt).getTime());
  const hours = Math.floor(elapsedMs / 3600000);
  const minutes = Math.floor((elapsedMs % 3600000) / 60000);
  if (hours <= 0) return `${minutes} 分钟`;
  return `${hours} 小时 ${minutes} 分钟`;
}

function activeHourlyEstimate(state = {}) {
  const computeHourly = (state.computeAllocations || [])
    .filter((item) => item.billingStatus === "active" && !["destroyed", "failed"].includes(item.status))
    .reduce((sum, item) => sum + Number(item.hourlyPrice || 0), 0);
  const storageHourly = (state.storageVolumes || [])
    .filter((item) => item.billingStatus === "active" && item.status !== "destroyed")
    .reduce((sum, item) => sum + Number(item.hourlyPrice || 0), 0);
  return computeHourly + storageHourly;
}

function oldestActiveResourceStartedAt(state = {}) {
  return [
    ...(state.computeAllocations || []),
    ...(state.storageVolumes || [])
  ]
    .filter((item) => item.billingStatus === "active" && item.status !== "destroyed")
    .map((item) => item.createdAt)
    .filter(Boolean)
    .sort()[0];
}

export function BillingPage({ state, wallet }) {
  const billingPolicy = state.billingPolicy || {};
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
  const hourlyEstimate = activeHourlyEstimate(state);
  const nextBillingTime = nextSettlementAt();
  const activeStartedAt = oldestActiveResourceStartedAt(state);
  const walletEvents = (state.walletTransactions || []).slice(-6).reverse().map((event) => ({
    title: event.type,
    description: event.workspaceId || event.accountId,
    meta: money(event.amount),
    tone: Number(event.amount || 0) < 0 ? "warn" : "good"
  }));

  return (
    <ConsoleSurface title="账单" eyebrow="钱包" subtitle="预付余额、冻结金额、资源用量">
      <MetricStrip
        items={[
          { label: "可用", value: money(usable), caption: "可开通计算或存储", tone: usable > 0 ? "good" : "warn" },
          { label: "冻结", value: money(wallet.frozen), caption: `余额 ${frozenPercent}%`, tone: frozenPercent > 70 ? "warn" : "info" },
          { label: "余额", value: money(wallet.balance), caption: "可用加冻结", tone: "neutral" },
          { label: "预计每小时", value: money(hourlyEstimate), caption: "活跃计算和存储", tone: hourlyEstimate > 0 ? "info" : "neutral" },
          { label: "累计充值", value: money(wallet.totalRecharged), caption: "人工充值记录", tone: "good" },
          { label: "扣费记录", value: recent.length, caption: "最近资源事件", tone: recent.length ? "info" : "neutral" }
        ]}
      />

      <div className="consoleGrid">
        <InsightPanel title="钱包拆分" eyebrow="余额">
          <div className="stackList">
            <div className="walletBar"><span style={{ width: `${frozenPercent}%` }} /></div>
            <div className="stackRow"><span>可用余额</span><strong>{money(usable)}</strong></div>
            <div className="stackRow"><span>冻结金额</span><strong>{money(wallet.frozen)}</strong></div>
            <div className="stackRow"><span>总余额</span><strong>{money(wallet.balance)}</strong></div>
            <div className="stackRow"><span>下次结算</span><strong>{nextBillingTime}</strong></div>
          </div>
        </InsightPanel>

        <InsightPanel title="资源用量" eyebrow="用量">
          <ResourceSplit
            items={[
              { label: "计算", value: `${usageQuantity(resourceUsage, "compute").toFixed(1)} 小时`, meta: "计算资源用量", status: "按小时", tone: "info" },
              { label: "存储", value: `${usageQuantity(resourceUsage, "storage").toFixed(1)} GB-小时`, meta: "存储资源用量", status: "保留", tone: "good" },
              { label: "运行时长", value: runningDuration(activeStartedAt), meta: `下次结算 ${nextBillingTime}`, status: "按小时", tone: activeStartedAt ? "info" : "neutral" },
              { label: "预计每小时", value: money(hourlyEstimate), meta: "当前活跃资源", status: "预估", tone: hourlyEstimate > 0 ? "warn" : "neutral" },
              { label: "网关", value: requestUsage.length, meta: "请求用量记录", status: "计量", tone: "info" },
              { label: "充值记录", value: state.manualTopups?.length || 0, meta: "人工充值证据", status: "已审计", tone: "good" }
            ]}
          />
        </InsightPanel>

        <InsightPanel title="计费规则" eyebrow="规则">
          <ResourceSplit
            items={[
              { label: "计算/存储", value: "预付冻结", meta: `${billingPolicy.holdDays || 7} 天 · 销毁后释放未用冻结`, status: "冻结", tone: "warn" },
              { label: "请求扣费", value: "按请求扣费", meta: "sub2api 请求写入钱包流水", status: "计量", tone: "info" },
              { label: "对账", value: state.billingReconciliation?.guard?.status || "无需对账", meta: state.billingReconciliation?.guard?.reason || "对账保护", status: "保护", tone: state.billingReconciliation?.guard?.blockNewWorkspaces ? "danger" : "good" }
            ]}
          />
        </InsightPanel>
      </div>

      <div className="consoleGrid">
        <InsightPanel title="最近扣费" eyebrow="账本">
          <ObjectTable
            rowKey={(row) => row.id}
            data={recent}
            emptyText="暂无扣费记录"
            columns={[
              { title: "类型", dataIndex: "billingType", width: 90 },
              { title: "工作区", dataIndex: "workspaceId", ellipsis: true, render: (value) => <Typography.Text ellipsis>{value || "账号"}</Typography.Text> },
              { title: "用量", render: (_, row) => `${Number(row.quantity || 0).toFixed(2)} ${row.unit || ""}` },
              { title: "金额", dataIndex: "amount", render: (value) => money(value) }
            ]}
          />
        </InsightPanel>
        <InsightPanel title="钱包流水" eyebrow="流水">
          <TimelineList items={walletEvents} emptyText="暂无钱包流水" />
        </InsightPanel>
      </div>
    </ConsoleSurface>
  );
}
