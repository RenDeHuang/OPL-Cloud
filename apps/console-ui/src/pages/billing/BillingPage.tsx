import React from "react";
import { Alert, Button, Typography } from "antd";
import { getPricingCatalog } from "../../api/console-read-api.ts";
import {
  ConsoleSurface,
  InsightPanel,
  MetricStrip,
  ObjectTable,
  ResourceSplit,
  StatusPill
} from "../shared/commercial-console.tsx";
import { moneyCents, paidThrough, usdBalance, usdMicros, valueLabel } from "../shared/formatters.ts";

function entitlementTone(status = "") {
  if (status === "active") return "good";
  if (["manual_review", "past_due", "failed"].includes(status)) return "danger";
  return "warn";
}

export function BillingPage({ state, balance }: any) {
  const [catalog, setCatalog] = React.useState<any>(null);
  const [catalogError, setCatalogError] = React.useState("");
  const [catalogRun, setCatalogRun] = React.useState(0);

  React.useEffect(() => {
    let active = true;
    setCatalog(null);
    setCatalogError("");
    getPricingCatalog()
      .then((payload) => {
        if (active) setCatalog(payload);
      })
      .catch((err) => {
        if (active) setCatalogError(err.message);
      });
    return () => { active = false; };
  }, [catalogRun]);

  const resources = [
    ...(state.computeAllocations || []).map((item) => ({ ...item, resourceType: "计算" })),
    ...(state.storageVolumes || []).map((item) => ({ ...item, resourceType: "存储" }))
  ].filter((item) => item.billingOperationId || item.billingStatus);
  const plans = catalog?.packages || [];
  const storagePer10GbMonthly = catalog?.storagePer10GbMonthly || {};
  const storageStepGb = Number(catalog?.storageSize?.stepGb || 10);

  return (
    <ConsoleSurface title="账单" eyebrow="Sub2API" subtitle="实时 USD 余额与月度资源权益">
      <MetricStrip
        items={[
          { label: "Sub2API 余额", value: usdBalance(balance), caption: "gflabtoken.cn 实时余额", tone: Number(balance.usdMicros || 0) > 0 ? "good" : "warn" },
          { label: "价格目录", value: catalog?.pricingVersion || "加载中", caption: "服务端月度目录", tone: catalog ? "good" : "neutral" },
          { label: "计费周期", value: catalog?.billingUnit === "calendar_month" ? "日历月" : "-", caption: "计算与存储独立计费", tone: "info" },
          { label: "价格参考", value: catalog ? `1 USD = ${catalog.exchangeRateCnyPerUsd} CNY` : "-", caption: "固定汇率", tone: "neutral" }
        ]}
      />

      {catalogError && (
        <Alert
          type="error"
          showIcon
          message="价格目录加载失败"
          description={catalogError}
          action={<Button onClick={() => setCatalogRun((value) => value + 1)}>重试</Button>}
        />
      )}
      {!catalog && !catalogError && <Alert type="info" showIcon message="正在加载服务端价格目录" />}

      {plans.length > 0 && (
        <div className="consoleGrid equal">
          {plans.map((plan) => {
            const storageBlocks = Math.ceil(Number(plan.diskGb || storageStepGb) / storageStepGb);
            const storageCnyCents = storageBlocks * Number(storagePer10GbMonthly.cnyCents || 0);
            const storageUsdMicros = Math.ceil(storageCnyCents * 10_000 / Number(catalog.exchangeRateCnyPerUsd || 1));
            const computeCnyCents = Number(plan.price?.monthlyPriceCnyCents || 0);
            const computeUsdMicros = Number(plan.price?.chargeUsdMicros || 0);
            return (
              <InsightPanel
                key={plan.id}
                title={`${plan.name} ${plan.server}`}
                eyebrow="服务套餐"
                actions={<StatusPill label={plan.available ? "可购买" : "暂不可用"} tone={plan.available ? "good" : "warn"} />}
              >
                <ResourceSplit
                  items={[
                    { label: "计算月价", value: `${moneyCents(computeCnyCents)}/月`, meta: usdMicros(computeUsdMicros), status: `${plan.cpu}C / ${plan.memoryGb}G`, tone: "info" },
                    { label: "存储月价", value: `${moneyCents(storageCnyCents)}/月`, meta: usdMicros(storageUsdMicros), status: `${plan.diskGb}GB`, tone: "info" },
                    { label: "月度合计", value: `${moneyCents(computeCnyCents + storageCnyCents)}/月`, meta: "计算与存储分别形成权益", status: "月付", tone: "good" }
                  ]}
                />
              </InsightPanel>
            );
          })}
        </div>
      )}

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
