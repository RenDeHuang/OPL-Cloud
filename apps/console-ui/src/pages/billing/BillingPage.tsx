import { Typography } from "antd";
import {
  ConsoleSurface,
  InsightPanel,
  MetricStrip,
  ObjectTable,
  StatusPill
} from "../shared/commercial-console.tsx";
import { moneyCents, paidThrough, usdBalance, usdMicros, valueLabel } from "../shared/formatters.ts";

function entitlementTone(status = "") {
  if (status === "active") return "good";
  if (["manual_review", "past_due", "failed"].includes(status)) return "danger";
  return "warn";
}

export function BillingPage({ state, balance }: any) {
  const resources = [
    ...(state.computeAllocations || []).map((item) => ({ ...item, resourceType: "计算" })),
    ...(state.storageVolumes || []).map((item) => ({ ...item, resourceType: "存储" }))
  ].filter((item) => item.billingOperationId || item.billingStatus);

  return (
    <ConsoleSurface title="账单" eyebrow="Sub2API" subtitle="实时 USD 余额与月度资源权益">
      <MetricStrip
        items={[
          { label: "Sub2API 余额", value: usdBalance(balance), caption: "gflabtoken.cn 实时余额", tone: Number(balance.usdMicros || 0) > 0 ? "good" : "warn" },
          { label: "Basic 2C4G", value: "¥350.00/月", caption: "$50.000000", tone: "info" },
          { label: "Pro 8C16G", value: "¥1,500.00/月", caption: "$214.285715", tone: "info" },
          { label: "存储", value: "每 10GB ¥18/月", caption: "$2.571429 起", tone: "info" },
          { label: "价格参考", value: "1 USD = 7 CNY", caption: "当前目录固定汇率", tone: "neutral" }
        ]}
      />

      <InsightPanel title="月度权益" eyebrow="计算与存储">
        <ObjectTable
          rowKey={(row) => row.id}
          data={resources}
          emptyText="暂无月度资源"
          columns={[
            { title: "类型", dataIndex: "resourceType", width: 80 },
            { title: "资源", render: (_, row) => <Typography.Text ellipsis>{row.name || row.id}</Typography.Text> },
            { title: "参考月价", dataIndex: "monthlyPriceCnyCents", render: (value) => moneyCents(value) },
            { title: "钱包扣款", dataIndex: "chargeUsdMicros", render: (value) => usdMicros(value) },
            { title: "有效期至", dataIndex: "paidThrough", render: (value) => paidThrough(value) },
            { title: "续费", dataIndex: "autoRenew", render: (value, row) => value ? "自动续费" : row.resourceType === "存储" ? "到期保留" : "到期停止" },
            { title: "状态", dataIndex: "billingStatus", render: (value) => <StatusPill label={valueLabel(value)} tone={entitlementTone(value)} /> }
          ]}
        />
      </InsightPanel>
    </ConsoleSurface>
  );
}
